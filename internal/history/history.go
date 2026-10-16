package history

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
)

// ENABLE_HISTORY is read ONCE by NewHistory. Changing it later does not affect a running instance.
var ENABLE_HISTORY = false // EXPERIMENTAL !

var HistoryDEBUG = false // Set to true for spammy debug logs

// ErrHistoryClosed is returned by reads after Close()
var ErrHistoryClosed = errors.New("history is closed")

const (

	// History file constants
	DefaultHistoryDir = "./data/history" // TODO set via config

	// Cache configuration
	DefaultCacheExpires = 15 // seconds
	DefaultCachePurge   = 5  // seconds

	// History Write Batching configuration
	DefaultBatchSize    = 10000 // Number of ops per flush
	DefaultBatchTimeout = 1000  // Milliseconds between forced flushes. maps to h.config.BatchTimeout

	// Sharding configuration constants
	SHARD_16_256 = 2 // 16 DBs with 256 tables each (recommended)
)

const (
	historyNumDBs = 16 // must match GetShardConfig

	// Begin retry on busy/locked (busy_timeout already waits 30s per attempt)
	beginRetryBaseDelay = 100 * time.Millisecond
	beginRetryMaxDelay  = 5 * time.Second
	beginMaxRetries     = 100

	// writer poll interval while shutting down
	shutdownPollInterval = 50 * time.Millisecond
)

// history op kinds
const (
	opAdd    uint8 = 1
	opRemove uint8 = 2
)

// historyOp is one queued write: add or remove groupID for messageID
type historyOp struct {
	op         uint8
	messageID  string
	groupID    int64
	articleNum int64 // article number in groupID; 0 = unknown (plan xref-history)
}

// Location is one place an article is stored: a main-DB newsgroup ID and the article
// number in that group's database (0 when the index row predates the numbers).
type Location struct {
	GroupID    int64
	ArticleNum int64
}

// routedOp is a historyOp with its target table in the routed database file
type routedOp struct {
	historyOp
	tableName string
}

// History row encoding (plan xref-history): newsgroups is a CSV of tokens, one per group the
// article is stored in. A token is "groupID:articleNum" when the number is known, or the plain
// "groupID" of rows written before the numbers were recorded (number unknown). Both formats may
// appear in one row; readers accept both (parseLocations).
//
// A group is never stored twice. An add with a number is authoritative: it upgrades a plain "id"
// token of the group in place to "id:num" (token order kept) and rewrites "id:<other>" to
// "id:num" (setGroupToken, only when the UPSERT changed nothing), so a history-rebuild over an
// existing index fills in the numbers of old rows and fixes renumbered groups (recover-db).
// An add without a number never appends a plain token next to an existing token of the group
// and never downgrades "id:num".
const (
	// add a token ("id:num" or plain "id"): skip if the row holds "id:<anything>" (prefix probe
	// ",id:") or the plain "id" while the token has no number (exact probe ",id,"); upgrade a
	// plain "id" in place to the numbered token; otherwise append. A skipped numbered add is
	// followed by the renumber check in Go (executeDBTransaction, renumberGroupToken).
	// args: ?1 messageID, ?2 token, ?3 groupID
	sqlAddArticle = `INSERT INTO %s(message_id,newsgroups) VALUES(?1,?2) ON CONFLICT(message_id) DO UPDATE SET newsgroups = CASE` +
		` WHEN coalesce(newsgroups,'')='' THEN excluded.newsgroups` +
		` WHEN instr(','||newsgroups||',', ','||?3||',') > 0 THEN trim(replace(','||newsgroups||',', ','||?3||',', ','||excluded.newsgroups||','), ',')` +
		` ELSE newsgroups||','||excluded.newsgroups END` +
		` WHERE instr(','||coalesce(newsgroups,'')||',', ','||?3||':') = 0` +
		` AND (instr(','||coalesce(newsgroups,'')||',', ','||?3||',') = 0 OR excluded.newsgroups <> ?3)`
	// remove the exact token "id:num" and a plain "id" token of the same group (number unknown).
	// Changes the row only when one of them is present, so RowsAffected()==0 means "not removed":
	// the caller then strips every token of the group (a stale number, see RemoveArticleAt).
	// args: ?1 "id:num", ?2 "id", ?3 messageID
	sqlRemoveArticle = `UPDATE %s SET newsgroups = trim(replace(replace(','||newsgroups||',', ','||?1||',', ','), ','||?2||',', ','), ',')` +
		` WHERE message_id=?3 AND (instr(','||newsgroups||',', ','||?1||',') > 0 OR instr(','||newsgroups||',', ','||?2||',') > 0)`
	// remove with an unknown number: read-modify-write in Go (stripGroupTokens)
	sqlSelectNewsgroups = `SELECT newsgroups FROM %s WHERE message_id=?`
	sqlSetNewsgroups    = `UPDATE %s SET newsgroups=? WHERE message_id=?`
	// drop the row if no group is left
	sqlDeleteEmpty = `DELETE FROM %s WHERE message_id=? AND coalesce(newsgroups,'')=''`
)

// historyToken returns the CSV token for groupID: "id:num", or the plain "id" when num is unknown (<= 0)
func historyToken(groupID, articleNum int64) string {
	id := strconv.FormatInt(groupID, 10)
	if articleNum <= 0 {
		return id
	}
	return id + ":" + strconv.FormatInt(articleNum, 10)
}

// setGroupToken rewrites the first token of groupID (plain "id" or "id:<any>") to token and drops
// further tokens of groupID. Other tokens are kept verbatim and in order. A csv without a token of
// groupID is returned unchanged. changed reports whether the result differs from csv.
func setGroupToken(csv string, groupID int64, token string) (out string, changed bool) {
	id := strconv.FormatInt(groupID, 10)
	parts := strings.Split(csv, ",")
	kept := make([]string, 0, len(parts))
	found := false
	for _, part := range parts {
		tok := strings.TrimSpace(part)
		if tok == id || strings.HasPrefix(tok, id+":") {
			if found {
				continue
			}
			found = true
			if tok == token {
				kept = append(kept, part) // already right: keep verbatim
			} else {
				kept = append(kept, token)
			}
			continue
		}
		kept = append(kept, part)
	}
	if !found {
		return csv, false
	}
	out = strings.Join(kept, ",")
	return out, out != csv
}

// stripGroupTokens drops every token of groupID (plain "id" or "id:num") and empty parts from csv.
// Other tokens are kept verbatim and in order. changed reports whether the result differs from csv.
func stripGroupTokens(csv string, groupID int64) (out string, changed bool) {
	id := strconv.FormatInt(groupID, 10)
	parts := strings.Split(csv, ",")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		tok := strings.TrimSpace(part)
		if tok == "" {
			continue
		}
		if tok == id || strings.HasPrefix(tok, id+":") {
			continue
		}
		kept = append(kept, part)
	}
	out = strings.Join(kept, ",")
	return out, out != csv
}

// NewHistory creates a new history manager.
// The mainWG parameter (may be nil) is the main application's waitgroup: when enabled
// it gets +1 for the writer goroutine, which calls Done() after its final flush.
func NewHistory(cfg *HistoryConfig, mainWG *sync.WaitGroup) (*History, error) {
	enabled := ENABLE_HISTORY // read once

	if cfg == nil {
		cfg = DefaultConfig()
	}
	if err := cfg.ValidateConfig(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	h := &History{
		config:     cfg,
		enabled:    enabled,
		closeChan:  make(chan struct{}),
		writerDone: make(chan struct{}),
	}
	if !enabled {
		log.Printf("[HISTORY] history index disabled")
		return h, nil
	}

	// Create history directory
	if !dirExists(cfg.HistoryDir) {
		if !mkdir(cfg.HistoryDir) {
			return nil, fmt.Errorf("failed to create history directory: %s", cfg.HistoryDir)
		}
	}

	// Initialize database backend
	if err := h.initDatabase(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	h.opChan = make(chan historyOp, cfg.BatchSize*2)
	h.mainWG = mainWG
	h.writerAlive = true
	if mainWG != nil {
		mainWG.Add(1)
	}
	go h.writerWorker() // mainWG.Done() when finished
	return h, nil
}

// Enabled reports whether this instance maintains the history index
func (h *History) Enabled() bool {
	return h != nil && h.enabled
}

// AddArticle queues "messageID is stored in groupID". Idempotent. Blocks when the queue is full (backpressure).
// The article number stays unknown; use AddArticleAt when it is known.
func (h *History) AddArticle(messageID string, groupID int64) {
	h.enqueue(opAdd, messageID, groupID, 0)
}

// AddArticleAt is AddArticle with the article number of messageID in groupID (0 = unknown).
// The row then holds the token "groupID:articleNum": a plain "groupID" token (number unknown) is
// upgraded in place and a token with a different number is rewritten (the latest numbered add wins).
func (h *History) AddArticleAt(messageID string, groupID, articleNum int64) {
	h.enqueue(opAdd, messageID, groupID, articleNum)
}

// RemoveArticle queues "messageID is no longer stored in groupID" (trimming). The number is
// unknown, so every token of groupID (plain or with a number) is removed.
func (h *History) RemoveArticle(messageID string, groupID int64) {
	h.enqueue(opRemove, messageID, groupID, 0)
}

// RemoveArticleAt is RemoveArticle with the article number the row holds for groupID (0 = unknown).
// It removes the exact token "groupID:articleNum" or a plain "groupID" token in one statement. A
// group DB holds a message-id at most once, so a token of groupID with a different number can only
// be a stale number (renumbering): when neither token matched, every token of groupID is removed.
func (h *History) RemoveArticleAt(messageID string, groupID, articleNum int64) {
	h.enqueue(opRemove, messageID, groupID, articleNum)
}

// enqueue returns true if the op was queued
func (h *History) enqueue(kind uint8, messageID string, groupID, articleNum int64) bool {
	if h == nil || !h.enabled {
		return false
	}
	if messageID == "" || groupID <= 0 {
		if n := h.invalidOps.Add(1); n <= 10 || n%10000 == 0 {
			log.Printf("[HISTORY] WARN: ignoring invalid op=%d messageID='%s' groupID=%d (invalid ops: %d)", kind, messageID, groupID, n)
		}
		return false
	}
	// count first, then check closed: the writer checks closed before pending, so it can not miss this op
	h.pending.Add(1)
	if h.closed.Load() {
		h.pending.Add(-1)
		if h.closedWarned.CompareAndSwap(false, true) {
			log.Printf("[HISTORY] WARN: op after Close() dropped: op=%d messageID='%s' groupID=%d (further drops are not logged)", kind, messageID, groupID)
		}
		return false
	}
	h.opChan <- historyOp{op: kind, messageID: messageID, groupID: groupID, articleNum: articleNum}
	if kind == opAdd {
		h.stats.adds.Add(1)
	} else {
		h.stats.removes.Add(1)
	}
	return true
}

// Exists checks if a message-ID has a row in the history index
func (h *History) Exists(messageID string) (bool, error) {
	if h == nil || !h.enabled || messageID == "" {
		return false, nil
	}
	if h.dbClosed.Load() {
		return false, ErrHistoryClosed
	}
	db, tableName, err := h.readDB(messageID)
	if err != nil {
		return false, err
	}
	h.stats.lookups.Add(1)
	var one int
	err = db.QueryRow("SELECT 1 FROM "+tableName+" WHERE message_id = ? LIMIT 1", messageID).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		h.stats.errors.Add(1)
		return false, fmt.Errorf("history exists query failed: %w", err)
	}
	h.stats.duplicates.Add(1)
	return true, nil
}

// LookupGroups returns the newsgroup IDs stored for messageID.
// nil, nil = not found; a non-nil (maybe empty) slice = row exists.
func (h *History) LookupGroups(messageID string) ([]int64, error) {
	if h == nil || !h.enabled || messageID == "" {
		return nil, nil
	}
	if h.dbClosed.Load() {
		return nil, ErrHistoryClosed
	}
	db, tableName, err := h.readDB(messageID)
	if err != nil {
		return nil, err
	}
	h.stats.lookups.Add(1)
	var newsgroups sql.NullString
	err = db.QueryRow("SELECT newsgroups FROM "+tableName+" WHERE message_id = ?", messageID).Scan(&newsgroups)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		h.stats.errors.Add(1)
		return nil, fmt.Errorf("history lookup query failed: %w", err)
	}
	h.stats.duplicates.Add(1)
	return parseGroupIDs(newsgroups.String), nil
}

// LookupLocations returns the stored locations (group ID and article number) of messageID.
// nil, nil = not found; a non-nil (maybe empty) slice = row exists. Rows written before the
// numbers were recorded give ArticleNum 0.
func (h *History) LookupLocations(messageID string) ([]Location, error) {
	if h == nil || !h.enabled || messageID == "" {
		return nil, nil
	}
	if h.dbClosed.Load() {
		return nil, ErrHistoryClosed
	}
	db, tableName, err := h.readDB(messageID)
	if err != nil {
		return nil, err
	}
	h.stats.lookups.Add(1)
	var newsgroups sql.NullString
	err = db.QueryRow("SELECT newsgroups FROM "+tableName+" WHERE message_id = ?", messageID).Scan(&newsgroups)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		h.stats.errors.Add(1)
		return nil, fmt.Errorf("history lookup query failed: %w", err)
	}
	h.stats.duplicates.Add(1)
	return parseLocations(newsgroups.String), nil
}

// parseLocations parses a CSV of "groupID" or "groupID:articleNum" tokens, skipping
// empty/invalid parts. A token without a number (or with an invalid one) gives ArticleNum 0.
// Never returns nil.
func parseLocations(csv string) []Location {
	locs := make([]Location, 0, strings.Count(csv, ",")+1)
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idStr, numStr, hasNum := strings.Cut(part, ":")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			if HistoryDEBUG {
				log.Printf("[HISTORY] WARN: invalid group id '%s' in newsgroups '%s'", part, csv)
			}
			continue
		}
		var num int64
		if hasNum {
			if n, err := strconv.ParseInt(numStr, 10, 64); err == nil && n > 0 {
				num = n
			}
		}
		locs = append(locs, Location{GroupID: id, ArticleNum: num})
	}
	return locs
}

// parseGroupIDs parses a CSV of group IDs, skipping empty/invalid parts. Never returns nil.
// A "groupID:articleNum" token counts for its group ID.
func parseGroupIDs(csv string) []int64 {
	locs := parseLocations(csv)
	ids := make([]int64, 0, len(locs))
	for _, l := range locs {
		ids = append(ids, l.GroupID)
	}
	return ids
}

// readDB routes messageID to its database pool and table
func (h *History) readDB(messageID string) (*sql.DB, string, error) {
	dbIndex, tableName, err := h.routeHash(messageID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to route hash: %v", err)
	}
	db, err := h.db.GetShardedDB(dbIndex, false)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get database connection: %v", err)
	}
	return db, tableName, nil
}

// GetStats returns current statistics
func (h *History) GetStats() HistoryStats {
	if h == nil {
		return HistoryStats{}
	}
	return HistoryStats{
		TotalLookups:   h.stats.lookups.Load(),
		TotalAdds:      h.stats.adds.Load(),
		TotalRemoves:   h.stats.removes.Load(),
		TotalCommitted: h.stats.committed.Load(),
		Flushes:        h.stats.flushes.Load(),
		Pending:        h.pending.Load(),
		Duplicates:     h.stats.duplicates.Load(),
		Errors:         h.stats.errors.Load(),
	}
}

// CheckNoMoreWorkInHistory returns true if no op is waiting to be committed
func (h *History) CheckNoMoreWorkInHistory() bool {
	if h == nil {
		return true
	}
	return h.pending.Load() == 0
}

// SetDatabaseWorkChecker sets the database work checker interface for coordinated shutdown
func (h *History) SetDatabaseWorkChecker(checker DatabaseWorkChecker) {
	if h == nil {
		return
	}
	h.checkerMux.Lock()
	h.dbWorkChecker = checker
	h.checkerMux.Unlock()
}

// noMoreDBWork asks the database batch system (if set) whether it is idle. Called without holding any lock.
func (h *History) noMoreDBWork() bool {
	h.checkerMux.RLock()
	checker := h.dbWorkChecker
	h.checkerMux.RUnlock()
	return checker == nil || checker.CheckNoMoreWorkInMaps()
}

// Close stops accepting ops, waits for the writer's final flush and closes the databases.
// Idempotent; concurrent callers all return after the final flush.
func (h *History) Close() error {
	if h == nil {
		return nil
	}
	var err error
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		close(h.closeChan)
		if !h.enabled {
			return
		}
		log.Printf("[HISTORY] Closing down history system (pending=%d)...", h.pending.Load())
		if h.writerAlive {
			<-h.writerDone
		}
		h.dbClosed.Store(true)
		if h.db != nil {
			err = h.db.Close()
		}
		log.Printf("[HISTORY] History system closed")
	})
	return err
}

// writerWorker collects ops and flushes them every BatchTimeout or when BatchSize is reached.
// Exits when Close() was called, nothing is pending and the database batch system is idle.
func (h *History) writerWorker() {
	defer func() {
		h.checkpointAll()
		close(h.writerDone)
		if h.mainWG != nil {
			h.mainWG.Done()
		}
		log.Printf("[HISTORY] History writer worker stopped")
	}()
	log.Printf("[HISTORY] History writer worker started (BatchSize=%d, BatchTimeout=%d ms)", h.config.BatchSize, h.config.BatchTimeout)

	ticker := time.NewTicker(time.Duration(h.config.BatchTimeout) * time.Millisecond)
	defer ticker.Stop()

	batch := make([]historyOp, 0, h.config.BatchSize)
	closeChan := h.closeChan
	lastWaitLog := time.Now()
	for {
		select {
		case op := <-h.opChan:
			batch = append(batch, op)
			if len(batch) >= h.config.BatchSize {
				h.flushBatch(batch)
				batch = batch[:0]
			}
			continue

		case <-closeChan:
			closeChan = nil // fire once
			if time.Duration(h.config.BatchTimeout)*time.Millisecond > shutdownPollInterval {
				ticker.Reset(shutdownPollInterval)
			}

		case <-ticker.C:
		}

		if len(batch) > 0 {
			h.flushBatch(batch)
			batch = batch[:0]
		}

		// check closed BEFORE pending (see enqueue)
		if !h.closed.Load() || h.pending.Load() != 0 {
			continue
		}
		if !h.noMoreDBWork() {
			if time.Since(lastWaitLog) >= 10*time.Second {
				log.Printf("[HISTORY] writer shutdown: waiting for database batch system to finish")
				lastWaitLog = time.Now()
			}
			continue
		}
		if h.pending.Load() != 0 {
			continue // the batch system queued more work meanwhile
		}
		return
	}
}

// checkpointAll truncates the WAL of every database file
func (h *History) checkpointAll() {
	if h.db == nil {
		return
	}
	for i, pool := range h.db.DBPools {
		if pool == nil || pool.DB == nil {
			continue
		}
		if _, err := pool.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			log.Printf("[HISTORY] WARN: wal_checkpoint(TRUNCATE) failed for db %x: %v", i, err)
		}
	}
}

// flushBatch writes batch grouped by database file (op order kept within a file), files in parallel
func (h *History) flushBatch(batch []historyOp) {
	n := len(batch)
	if n == 0 {
		return
	}
	start := time.Now()
	var perFile [historyNumDBs][]routedOp
	for _, op := range batch {
		dbIndex, tableName, err := h.routeHash(op.messageID)
		if err != nil {
			log.Printf("[HISTORY] ERROR: dropping op for messageID='%s': %v", op.messageID, err)
			h.stats.errors.Add(1)
			continue
		}
		perFile[dbIndex] = append(perFile[dbIndex], routedOp{historyOp: op, tableName: tableName})
	}

	var wg sync.WaitGroup
	for dbIndex := range perFile {
		ops := perFile[dbIndex]
		if len(ops) == 0 {
			continue
		}
		wg.Add(1)
		go func(dbIdx int, ops []routedOp) {
			defer wg.Done()
			if err := h.executeDBTransaction(dbIdx, ops); err != nil {
				log.Printf("[HISTORY] ERROR: dropping %d ops for database %x: %v", len(ops), dbIdx, err)
				h.stats.errors.Add(1)
				return
			}
			h.stats.committed.Add(int64(len(ops)))
		}(dbIndex, ops)
	}
	wg.Wait()

	h.stats.flushes.Add(1)
	h.pending.Add(-int64(n))
	if HistoryDEBUG {
		log.Printf("[HISTORY] flushBatch: %d ops took %v (pending=%d)", n, time.Since(start), h.pending.Load())
	}
}

// isBusyError reports SQLITE_BUSY / SQLITE_LOCKED
func isBusyError(err error) bool {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "database is locked") || strings.Contains(errStr, "busy")
}

// beginTx starts a write transaction (_txlock=immediate: lock contention surfaces here).
// Retries busy/locked errors with capped backoff, exits the process after beginMaxRetries.
func (h *History) beginTx(dbIndex int, db *sql.DB) (*sql.Tx, error) {
	delay := beginRetryBaseDelay
	for attempt := 1; ; attempt++ {
		tx, err := db.Begin()
		if err == nil {
			return tx, nil
		}
		if !isBusyError(err) {
			return nil, err
		}
		if attempt >= beginMaxRetries {
			log.Fatalf("[HISTORY] FATAL: failed to begin transaction on database %x after %d attempts: %v", dbIndex, attempt, err)
		}
		if attempt%10 == 0 {
			log.Printf("[HISTORY] WARN: database %x busy, begin attempt %d/%d: %v", dbIndex, attempt, beginMaxRetries, err)
		}
		time.Sleep(delay)
		delay *= 2
		if delay > beginRetryMaxDelay {
			delay = beginRetryMaxDelay
		}
	}
}

// executeDBTransaction writes all ops of one database file in one transaction
func (h *History) executeDBTransaction(dbIndex int, ops []routedOp) error {
	start := time.Now()
	db, err := h.db.GetShardedDB(dbIndex, true)
	if err != nil {
		return fmt.Errorf("failed to get database connection: %v", err)
	}

	tx, err := h.beginTx(dbIndex, db)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			log.Printf("ERROR HISTORY Rolling back transaction due to error: %v", err)
			os.Exit(1) // @AI!! THIS IS A CRITICAL ERROR, WE MUST EXIT! DO NOT TOUCH THIS LINE OF CODE!!
		}
	}()

	// prepared statements per table + query, valid for this transaction
	stmts := make(map[string]*sql.Stmt)
	getStmt := func(query string, tableName string) (*sql.Stmt, error) {
		key := query + tableName
		if stmt, ok := stmts[key]; ok {
			return stmt, nil
		}
		stmt, perr := tx.Prepare(fmt.Sprintf(query, tableName))
		if perr != nil {
			return nil, perr
		}
		stmts[key] = stmt
		return stmt, nil
	}

	var stmt *sql.Stmt
	for _, rop := range ops {
		groupID := strconv.FormatInt(rop.groupID, 10)
		token := historyToken(rop.groupID, rop.articleNum)
		switch rop.op {
		case opAdd:
			if stmt, err = getStmt(sqlAddArticle, rop.tableName); err != nil {
				err = fmt.Errorf("prepare add %s: %v", rop.tableName, err)
				return err
			}
			var res sql.Result
			if res, err = stmt.Exec(rop.messageID, token, groupID); err != nil {
				err = fmt.Errorf("add messageID='%s' token=%s table=%s: %v", rop.messageID, token, rop.tableName, err)
				return err
			}
			if rop.articleNum > 0 {
				// UPSERT skipped: the row holds "id:<num>" for this group, maybe with another number
				var affected int64
				if affected, err = res.RowsAffected(); err != nil {
					err = fmt.Errorf("add messageID='%s' token=%s table=%s: rows affected: %v", rop.messageID, token, rop.tableName, err)
					return err
				}
				if affected == 0 {
					if err = h.renumberGroupToken(getStmt, rop, token); err != nil {
						return err
					}
				}
			}

		case opRemove:
			if rop.articleNum > 0 {
				if stmt, err = getStmt(sqlRemoveArticle, rop.tableName); err != nil {
					err = fmt.Errorf("prepare remove %s: %v", rop.tableName, err)
					return err
				}
				var res sql.Result
				if res, err = stmt.Exec(token, groupID, rop.messageID); err != nil {
					err = fmt.Errorf("remove messageID='%s' token=%s table=%s: %v", rop.messageID, token, rop.tableName, err)
					return err
				}
				var affected int64
				if affected, err = res.RowsAffected(); err != nil {
					err = fmt.Errorf("remove messageID='%s' token=%s table=%s: rows affected: %v", rop.messageID, token, rop.tableName, err)
					return err
				}
				if affected == 0 {
					// no exact or plain token: strip a stale-numbered token of the group
					if err = h.removeGroupTokens(getStmt, rop); err != nil {
						return err
					}
				}
			} else if err = h.removeGroupTokens(getStmt, rop); err != nil {
				return err
			}
			if stmt, err = getStmt(sqlDeleteEmpty, rop.tableName); err != nil {
				err = fmt.Errorf("prepare delete %s: %v", rop.tableName, err)
				return err
			}
			if _, err = stmt.Exec(rop.messageID); err != nil {
				err = fmt.Errorf("delete empty messageID='%s' table=%s: %v", rop.messageID, rop.tableName, err)
				return err
			}

		default:
			err = fmt.Errorf("unknown history op %d", rop.op)
			return err
		}
	}

	// Commit the transaction (closes the prepared statements)
	if err = tx.Commit(); err != nil {
		err = fmt.Errorf("failed to commit transaction: %v", err)
		return err
	}
	if HistoryDEBUG {
		log.Printf("[HISTORY] executeDBTransaction: dbIndex=%d, committed %d ops in %v", dbIndex, len(ops), time.Since(start))
	}
	return nil
}

// removeGroupTokens is the remove with an unknown article number (or with a number that matched
// no token, see RemoveArticleAt): it reads the row's CSV,
// drops every token of rop.groupID in either format and writes it back, inside the caller's
// transaction (statements from its getStmt cache). A missing row is a no-op.
func (h *History) removeGroupTokens(getStmt func(query, tableName string) (*sql.Stmt, error), rop routedOp) error {
	stmt, err := getStmt(sqlSelectNewsgroups, rop.tableName)
	if err != nil {
		return fmt.Errorf("prepare select %s: %v", rop.tableName, err)
	}
	var newsgroups sql.NullString
	err = stmt.QueryRow(rop.messageID).Scan(&newsgroups)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("select messageID='%s' table=%s: %v", rop.messageID, rop.tableName, err)
	}
	out, changed := stripGroupTokens(newsgroups.String, rop.groupID)
	if !changed {
		return nil
	}
	if stmt, err = getStmt(sqlSetNewsgroups, rop.tableName); err != nil {
		return fmt.Errorf("prepare set %s: %v", rop.tableName, err)
	}
	if _, err = stmt.Exec(out, rop.messageID); err != nil {
		return fmt.Errorf("remove messageID='%s' group=%d table=%s: %v", rop.messageID, rop.groupID, rop.tableName, err)
	}
	return nil
}

// renumberGroupToken makes a numbered add authoritative when the UPSERT changed nothing: it reads
// the row's CSV and rewrites the group's token to token if it holds another number, inside the
// caller's transaction (statements from its getStmt cache). An equal number writes nothing.
func (h *History) renumberGroupToken(getStmt func(query, tableName string) (*sql.Stmt, error), rop routedOp, token string) error {
	stmt, err := getStmt(sqlSelectNewsgroups, rop.tableName)
	if err != nil {
		return fmt.Errorf("prepare select %s: %v", rop.tableName, err)
	}
	var newsgroups sql.NullString
	err = stmt.QueryRow(rop.messageID).Scan(&newsgroups)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("select messageID='%s' table=%s: %v", rop.messageID, rop.tableName, err)
	}
	out, changed := setGroupToken(newsgroups.String, rop.groupID, token)
	if !changed {
		return nil
	}
	if stmt, err = getStmt(sqlSetNewsgroups, rop.tableName); err != nil {
		return fmt.Errorf("prepare set %s: %v", rop.tableName, err)
	}
	if _, err = stmt.Exec(out, rop.messageID); err != nil {
		return fmt.Errorf("renumber messageID='%s' token=%s table=%s: %v", rop.messageID, token, rop.tableName, err)
	}
	return nil
}

// routeHash routes a message-ID to the correct database and table
// 1st md5 hex char -> database index (0-f), 2nd+3rd chars -> table name (_00-_ff)
func (h *History) routeHash(messageID string) (dbIndex int, tableName string, err error) {
	hash := ComputeMessageIDHash(messageID)[:3]

	dbIndex, err = hexToInt(hash[0:1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid hex char for database: %s", hash[0:1])
	}
	if dbIndex >= historyNumDBs {
		return 0, "", fmt.Errorf("database index %d exceeds available databases %d", dbIndex, historyNumDBs)
	}
	return dbIndex, "_" + hash[1:3], nil
}
