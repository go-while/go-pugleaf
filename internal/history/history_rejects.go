package history

// Remembered rejects (plan nntp-peering, INN "remembertrash"): a message-id refused for a
// permanent reason gets a row whose newsgroups column holds one reject token "r<unixday>"
// instead of group tokens. Exists then reports it as known (CHECK 438, IHAVE 435,
// TAKETHIS 439), so a peer stops offering it, while every token parser skips the token: a
// reject-only row has no locations. Accepting the id later replaces the token (sqlAddArticle)
// and a reject older than the HistoryRejectDays config key is pruned, by
// `expire-news -trim-history` (PruneRejects) and by the daily prune of the writer goroutine
// (SetRejectDaysFunc + pruneRejectStep).

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

const (
	// secondsPerDay is the length of a reject day: the token counts whole days since the epoch
	secondsPerDay = 86400

	// pruneRejectBatch is the number of rows one prune batch deletes from one table
	pruneRejectBatch = 500

	// rejectPruneInterval is the distance between two prune cycles of the writer goroutine
	rejectPruneInterval = 24 * time.Hour

	// rejectPruneFirstDelay is how long after its start the writer runs its first prune cycle
	rejectPruneFirstDelay = 10 * time.Minute

	// rejectPruneStepBudget bounds the time one prune step of the writer goroutine spends
	// deleting, so the writes of the next flush are never held back for long
	rejectPruneStepBudget = 20 * time.Millisecond

	// pruneRetries and pruneRetryDelay bound the retries of one prune statement on a busy database
	pruneRetries    = 3
	pruneRetryDelay = 100 * time.Millisecond
)

// sqlRejectOnlyRow matches a row whose whole newsgroups column is one reject token: a group
// token is a decimal id, so only a remembered reject can start with 'r'.
const sqlRejectOnlyRow = `substr(newsgroups,1,1)='r' AND instr(newsgroups,',')=0`

const (
	// write the reject token of a message-id the index does not know, or refresh the day of a
	// row that holds nothing but a reject token. A row with group tokens is left alone: that
	// id is known anyway. args: ?1 messageID, ?2 reject token
	sqlAddReject = `INSERT INTO %s(message_id,newsgroups) VALUES(?1,?2) ON CONFLICT(message_id) DO UPDATE SET newsgroups=?2` +
		` WHERE coalesce(newsgroups,'')='' OR (` + sqlRejectOnlyRow + `)`
	// the message-ids of one prune batch: rejects remembered before day ?1. args: ?1 day, ?2 limit
	sqlSelectExpiredRejects = `SELECT message_id FROM %s WHERE ` + sqlRejectOnlyRow +
		` AND CAST(substr(newsgroups,2) AS INTEGER) < ?1 LIMIT ?2`
)

// unixDay returns the number of whole days between the epoch and t (the value of a reject token)
func unixDay(t time.Time) int64 {
	return t.Unix() / secondsPerDay
}

// rejectToken returns the CSV token of a reject remembered on day
func rejectToken(day int64) string {
	return "r" + strconv.FormatInt(day, 10)
}

// isRejectToken reports whether tok is a reject token ("r<unixday>")
func isRejectToken(tok string) bool {
	return len(tok) > 1 && tok[0] == 'r'
}

// rejectCSVDay parses a newsgroups column that holds nothing but a reject token and returns
// the day it was remembered on. ok is false for every other column value (a row with group
// tokens, an empty column, a malformed token).
func rejectCSVDay(csv string) (day int64, ok bool) {
	tok := strings.TrimSpace(csv)
	if !isRejectToken(tok) || strings.Contains(tok, ",") {
		return 0, false
	}
	d, err := strconv.ParseInt(tok[1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return d, true
}

// rejectTableName returns the name of table index (0-255) of a shard database
func rejectTableName(table int) string {
	return fmt.Sprintf("_%02x", table)
}

// now returns the current time. Tests replace the clock with setClock to reach the reject
// days of past and future days without waiting.
func (h *History) now() time.Time {
	if p := h.clock.Load(); p != nil {
		return (*p)()
	}
	return time.Now()
}

// setClock replaces the clock of the reject days and of the prune schedule (tests only; nil
// restores time.Now)
func (h *History) setClock(f func() time.Time) {
	if h == nil {
		return
	}
	if f == nil {
		h.clock.Store(nil)
		return
	}
	h.clock.Store(&f)
}

// SetRejectDaysFunc wires the HistoryRejectDays config key into the index: the daily prune of
// the writer goroutine calls f once per cycle for the current value (0 = keep rejects as long
// as history), so an admin edit applies without a restart. Without it the writer never prunes
// (a tool without the main database can not know the setting; `expire-news -trim-history`
// passes it to PruneRejects instead).
func (h *History) SetRejectDaysFunc(f func() int) {
	if h == nil {
		return
	}
	if f == nil {
		h.rejectDaysFn.Store(nil)
		return
	}
	h.rejectDaysFn.Store(&f)
}

// rejectDays returns the wired retention in days, 0 when none is wired or the value is negative
func (h *History) rejectDays() int {
	if h == nil {
		return 0
	}
	p := h.rejectDaysFn.Load()
	if p == nil {
		return 0
	}
	if d := (*p)(); d > 0 {
		return d
	}
	return 0
}

// AddReject queues "messageID was rejected" (INN remembertrash): the row gets the reject
// token r<unixday> instead of a group token, so Exists reports it as known and peers stop
// offering it, while LookupGroups/LookupLocations return nothing for it. A row that already
// holds group tokens is left alone. Rejects expire after the HistoryRejectDays config key.
// Idempotent; blocks when the queue is full (backpressure). No-op if history is disabled.
func (h *History) AddReject(messageID string) {
	h.enqueueReject(messageID)
}

// enqueueReject queues one opReject; it returns true if the op was queued. Rejects have their
// own path because enqueue() refuses an op without a group id.
func (h *History) enqueueReject(messageID string) bool {
	if h == nil || !h.enabled {
		return false
	}
	if messageID == "" {
		if n := h.invalidOps.Add(1); n <= 10 || n%10000 == 0 {
			log.Printf("[HISTORY] WARN: ignoring invalid reject op with empty messageID (invalid ops: %d)", n)
		}
		return false
	}
	// count first, then check closed: the writer checks closed before pending, so it can not miss this op
	h.pending.Add(1)
	if h.closed.Load() {
		h.pending.Add(-1)
		if h.closedWarned.CompareAndSwap(false, true) {
			log.Printf("[HISTORY] WARN: op after Close() dropped: op=%d messageID='%s' (further drops are not logged)", opReject, messageID)
		}
		return false
	}
	h.opChan <- historyOp{op: opReject, messageID: messageID}
	h.stats.rejects.Add(1)
	return true
}

// LookupReject reports whether messageID has a row in the index (exists) and whether that row
// holds nothing but a remembered reject (rejectOnly). The bulk (fetcher/importer) path uses it
// instead of Exists: a reject-only row is not a duplicate there, so the article is evaluated
// again (a group it was refused for may exist by now) and an accept replaces the reject token.
// The feed path keeps Exists ("any row = known").
func (h *History) LookupReject(messageID string) (exists bool, rejectOnly bool, err error) {
	if h == nil || !h.enabled || messageID == "" {
		return false, false, nil
	}
	if h.dbClosed.Load() {
		return false, false, ErrHistoryClosed
	}
	db, tableName, err := h.readDB(messageID)
	if err != nil {
		return false, false, err
	}
	h.stats.lookups.Add(1)
	var newsgroups sql.NullString
	err = db.QueryRow("SELECT newsgroups FROM "+tableName+" WHERE message_id = ?", messageID).Scan(&newsgroups)
	switch {
	case err == sql.ErrNoRows:
		return false, false, nil
	case err != nil:
		h.stats.errors.Add(1)
		return false, false, fmt.Errorf("history reject lookup failed: %w", err)
	}
	h.stats.duplicates.Add(1)
	_, rejectOnly = rejectCSVDay(newsgroups.String)
	return true, rejectOnly, nil
}

// PruneRejects deletes every remembered reject older than days days and returns the number of
// deleted rows. days <= 0 prunes nothing (0 = keep rejects as long as history), as does a
// disabled index. stop (may be nil) ends the walk between batches.
// `expire-news -trim-history` calls it with the HistoryRejectDays config key; the writer
// goroutine prunes the same rows once a day (SetRejectDaysFunc).
func (h *History) PruneRejects(days int, stop <-chan struct{}) (int64, error) {
	if h == nil || !h.enabled || days <= 0 {
		return 0, nil
	}
	return h.PruneRejectsBefore(unixDay(h.now())-int64(days), stop)
}

// PruneRejectsBefore deletes every remembered reject whose day is before day. It walks the
// shard tables one at a time in batches of pruneRejectBatch rows, so no single statement locks
// a database file for long; a row whose newsgroups column holds group tokens is never touched.
// stop (may be nil) and Close end the walk between batches, with the rows deleted so far.
func (h *History) PruneRejectsBefore(day int64, stop <-chan struct{}) (int64, error) {
	if h == nil || !h.enabled || h.db == nil {
		return 0, nil
	}
	if h.dbClosed.Load() {
		return 0, ErrHistoryClosed
	}
	var total int64
	for dbIndex := 0; dbIndex < historyNumDBs; dbIndex++ {
		for table := 0; table < historyTablesPerDB; table++ {
			tableName := rejectTableName(table)
			for {
				if h.dbClosed.Load() {
					return total, nil
				}
				select {
				case <-stop:
					return total, nil
				default:
				}
				n, more, err := h.pruneRejectTable(dbIndex, tableName, day)
				total += n
				if err != nil {
					return total, err
				}
				if !more {
					break
				}
			}
		}
	}
	return total, nil
}

// pruneRejectTable deletes at most pruneRejectBatch expired rejects of one table. more reports
// that the batch was full, so the table may hold further expired rejects. A busy database
// (the writer is flushing the same file) is retried a few times before it becomes an error.
func (h *History) pruneRejectTable(dbIndex int, tableName string, day int64) (deleted int64, more bool, err error) {
	db, err := h.db.GetShardedDB(dbIndex, true)
	if err != nil {
		return 0, false, fmt.Errorf("prune rejects: failed to get database connection: %v", err)
	}
	var ids []string
	if err = h.pruneRetry(func() error {
		var qerr error
		ids, qerr = selectExpiredRejects(db, tableName, day, pruneRejectBatch)
		return qerr
	}); err != nil {
		return 0, false, err
	}
	if len(ids) == 0 {
		return 0, false, nil
	}
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, day)
	query := deleteRejectsQuery(tableName, len(ids))
	var n int64
	if err = h.pruneRetry(func() error {
		res, xerr := db.Exec(query, args...)
		if xerr != nil {
			return fmt.Errorf("prune rejects %s: delete failed: %v", tableName, xerr)
		}
		n, xerr = res.RowsAffected()
		if xerr != nil {
			return fmt.Errorf("prune rejects %s: rows affected: %v", tableName, xerr)
		}
		return nil
	}); err != nil {
		return 0, false, err
	}
	h.stats.pruned.Add(n)
	return n, len(ids) == pruneRejectBatch, nil
}

// pruneRetry runs fn, retrying a busy/locked database pruneRetries times with a short pause.
// The prune is background work: it never waits as long as a write would.
func (h *History) pruneRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < pruneRetries; attempt++ {
		if err = fn(); err == nil || !isBusyError(err) {
			return err
		}
		if h.dbClosed.Load() {
			return err
		}
		time.Sleep(pruneRetryDelay)
	}
	return err
}

// selectExpiredRejects returns at most limit message-ids of one table whose only token is a
// reject older than day
func selectExpiredRejects(db *sql.DB, tableName string, day int64, limit int) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf(sqlSelectExpiredRejects, tableName), day, limit)
	if err != nil {
		return nil, fmt.Errorf("prune rejects %s: select failed: %v", tableName, err)
	}
	defer rows.Close()
	ids := make([]string, 0, min(limit, 64))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("prune rejects %s: scan failed: %v", tableName, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("prune rejects %s: rows failed: %v", tableName, err)
	}
	return ids, nil
}

// deleteRejectsQuery builds the DELETE of one prune batch: n message-id placeholders followed
// by the day. The row conditions are repeated so a reject refreshed or replaced by an accept
// between the SELECT and the DELETE stays.
func deleteRejectsQuery(tableName string, n int) string {
	var q strings.Builder
	q.Grow(80 + 2*n)
	q.WriteString("DELETE FROM " + tableName + " WHERE message_id IN (?")
	for i := 1; i < n; i++ {
		q.WriteString(",?")
	}
	q.WriteString(") AND " + sqlRejectOnlyRow + " AND CAST(substr(newsgroups,2) AS INTEGER) < ?")
	return q.String()
}

// rejectPruneState is the daily prune cycle of the writer goroutine; only the writer
// goroutine reads or writes these fields.
type rejectPruneState struct {
	nextAt  time.Time // start of the next cycle
	active  bool      // a cycle is walking the tables
	day     int64     // cutoff day of the running cycle
	dbIndex int       // shard database of the next batch
	table   int       // table of the next batch
	deleted int64     // rows deleted in the running cycle
	started time.Time // start of the running cycle
}

// pruneRejectStep does at most rejectPruneStepBudget of work on the daily reject prune: it
// starts a cycle when one is due, otherwise it deletes expired rejects from the current tables
// and remembers where to continue. The writer calls it between flushes, so a cycle never holds
// the writes back for long, and Close ends the cycle.
func (h *History) pruneRejectStep() {
	if h.db == nil || h.closed.Load() || h.dbClosed.Load() {
		h.prune.active = false
		return
	}
	if !h.prune.active {
		now := h.now()
		if h.prune.nextAt.IsZero() {
			h.prune.nextAt = now.Add(rejectPruneFirstDelay)
			return
		}
		if now.Before(h.prune.nextAt) {
			return
		}
		next := now.Add(rejectPruneInterval)
		days := h.rejectDays()
		if days <= 0 { // 0 = keep rejects as long as history
			h.prune.nextAt = next
			return
		}
		h.prune = rejectPruneState{nextAt: next, active: true, day: unixDay(now) - int64(days), started: now}
		log.Printf("[HISTORY] reject prune started: removing rejects older than %d days (day %d)", days, h.prune.day)
		return
	}
	// one batch per round, bounded by the real-clock budget (h.now may be a test clock)
	deadline := time.Now().Add(rejectPruneStepBudget)
	for h.prune.active {
		n, more, err := h.pruneRejectTable(h.prune.dbIndex, rejectTableName(h.prune.table), h.prune.day)
		h.prune.deleted += n
		if err != nil {
			log.Printf("[HISTORY] WARN: reject prune stopped after %d rows: %v", h.prune.deleted, err)
			h.stats.errors.Add(1)
			h.prune.active = false
			return
		}
		if !more {
			h.pruneRejectAdvance()
		}
		if h.closed.Load() || h.dbClosed.Load() {
			h.prune.active = false
			return
		}
		if time.Now().After(deadline) {
			return
		}
	}
}

// pruneRejectAdvance moves the running cycle to the next table and ends it after the last one
func (h *History) pruneRejectAdvance() {
	h.prune.table++
	if h.prune.table < historyTablesPerDB {
		return
	}
	h.prune.table = 0
	h.prune.dbIndex++
	if h.prune.dbIndex < historyNumDBs {
		return
	}
	h.prune.active = false
	if h.prune.deleted > 0 {
		log.Printf("[HISTORY] reject prune: removed %d rejects older than day %d in %v",
			h.prune.deleted, h.prune.day, time.Since(h.prune.started))
	}
}

// UnixDay returns the day of t as a reject token counts it (whole days since the epoch), so a
// caller of PruneRejectsBefore can build its cutoff from a date.
func UnixDay(t time.Time) int64 {
	return unixDay(t)
}
