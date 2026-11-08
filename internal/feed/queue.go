package feed

// The durable per-peer outgoing queue (plan nntp-peering, slice feed-queue): one SQLite file
// of its own, data/cfg/feed-queue.sq3 (DefaultQueueFile), so the hot queue never writes into
// the main database. The enqueue side (enqueue.go) fills it at the batch writer's commit
// point; the sender (sender.go) claims items with a lease, deletes what the peer settled and
// defers what failed.
//
// Everything is written through the internal/database retry helpers, so a busy database is
// retried instead of losing a row, and every write runs in one transaction per call.

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3" // the queue uses the driver go-pugleaf already uses
)

// DefaultGiveUpAge is how long an item that keeps failing stays queued. Past it Prune drops
// it: a peer that has been unreachable for days is not caught up from the queue but from its
// own backlog, and the queue file must not grow without bound. INN's innfeed does the same
// with its backlog files.
const DefaultGiveUpAge = 3 * 24 * time.Hour

// queueIDChunk bounds how many message-ids go into one IN (...) list, so a large Done or
// Claim stays well under SQLite's bound-variable limit.
const queueIDChunk = 400

// ErrQueueClosed is returned by every method of a queue that is not (or no longer) open.
var ErrQueueClosed = errors.New("feed queue is not open")

// queueSchema is created on open; every statement is IF NOT EXISTS, so opening an existing
// file only adds what is missing. A later schema change needs its own migration code keyed
// on PRAGMA user_version, not an edited CREATE statement.
var queueSchema = []string{
	`CREATE TABLE IF NOT EXISTS feed_queue (
		peer_id      INTEGER NOT NULL,
		message_id   TEXT    NOT NULL,
		newsgroup    TEXT    NOT NULL DEFAULT '',
		enqueued_at  INTEGER NOT NULL,
		attempts     INTEGER NOT NULL DEFAULT 0,
		next_attempt INTEGER NOT NULL DEFAULT 0,
		last_error   TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (peer_id, message_id)
	)`,
	// Claim reads "the due items of one peer": the index makes that a range scan instead
	// of a table scan over every peer's rows.
	`CREATE INDEX IF NOT EXISTS idx_feed_queue_due ON feed_queue(peer_id, next_attempt)`,
	`CREATE TABLE IF NOT EXISTS feed_stats (
		peer_id    INTEGER PRIMARY KEY,
		offered    INTEGER NOT NULL DEFAULT 0,
		accepted   INTEGER NOT NULL DEFAULT 0,
		refused    INTEGER NOT NULL DEFAULT 0,
		rejected   INTEGER NOT NULL DEFAULT 0,
		deferred   INTEGER NOT NULL DEFAULT 0,
		last_error TEXT    NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL DEFAULT 0
	)`,
}

// SQLiteQueue is the Queue of api.go, backed by one SQLite file. Its methods are safe for
// concurrent use (the pool holds a single connection, so the writes of the enqueuer and of
// every peer sender are serialized without ever hitting SQLITE_BUSY between themselves; a
// second process, the web admin page reading the stats, is covered by WAL plus the busy
// timeout of the DSN).
type SQLiteQueue struct {
	// db is behind an atomic pointer because Close may run while a sender is in one of
	// the methods: every method loads it once and answers ErrQueueClosed on nil, and
	// Close swaps it out. Without that the plain field was read twice per method and
	// written by Close with no synchronisation, which is both a data race and a nil
	// *sql.DB panic in flight.
	db     atomic.Pointer[sql.DB]
	path   string
	giveUp time.Duration

	// now is time.Now in production; the tests replace it to pin enqueued_at.
	now func() time.Time
}

// handle returns the open database, or nil when the queue was never opened or is closed.
func (q *SQLiteQueue) handle() *sql.DB {
	if q == nil {
		return nil
	}
	return q.db.Load()
}

// SQLiteQueue is the implementation the contract in api.go describes.
var _ Queue = (*SQLiteQueue)(nil)

// QueuePath returns the queue file of a data root: <dataDir>/cfg/feed-queue.sq3.
func QueuePath(dataDir string) string {
	return filepath.Join(dataDir, "cfg", DefaultQueueFile)
}

// queueDSN is the DSN the queue file is opened with. The mattn driver hands every "file:"
// DSN to SQLite's URI parser, so the path has to be escaped (trackDSN of cmd/mbox-importer
// does the same): an unescaped '?' or '#' in it would cut the path short and drop the
// parameters. _txlock=immediate makes every transaction a BEGIN IMMEDIATE, so Claim takes
// the write lock before it reads the rows it is about to lease.
func queueDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_busy_timeout=30000&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
}

// Open opens (and creates) the queue file at path and brings its schema up. The returned
// queue implements Queue and adds Prune, which the sender calls.
func Open(path string) (*SQLiteQueue, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("feed queue: no path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("feed queue: create %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite3", queueDSN(path))
	if err != nil {
		return nil, fmt.Errorf("feed queue %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, queueCloseErr(db, fmt.Errorf("feed queue %s: %w", path, err))
	}
	for _, stmt := range queueSchema {
		if _, err := database.RetryableExec(db, stmt); err != nil {
			return nil, queueCloseErr(db, fmt.Errorf("feed queue %s schema: %w", path, err))
		}
	}
	log.Printf("[FEED] queue db=%q", path)
	q := &SQLiteQueue{path: path, giveUp: DefaultGiveUpAge, now: time.Now}
	q.db.Store(db)
	return q, nil
}

// OpenReadOnly opens an existing queue file read-only, for a process that only reads the
// counters (the web admin page; the file belongs to the nntp-server). It never creates the file
// or its schema: a missing file returns an error wrapping os.ErrNotExist. Write methods fail.
func OpenReadOnly(path string) (*SQLiteQueue, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("feed queue: no path")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("feed queue %s: %w", path, err)
	}
	db, err := sql.Open("sqlite3", queueDSN(path)+"&mode=ro")
	if err != nil {
		return nil, fmt.Errorf("feed queue %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, queueCloseErr(db, fmt.Errorf("feed queue %s: %w", path, err))
	}
	q := &SQLiteQueue{path: path, giveUp: DefaultGiveUpAge, now: time.Now}
	q.db.Store(db)
	return q, nil
}

// queueCloseErr closes db on an open that failed and keeps the original error.
func queueCloseErr(db *sql.DB, err error) error {
	if cerr := db.Close(); cerr != nil {
		log.Printf("[FEED] closing the queue file after a failed open: %v", cerr)
	}
	return err
}

// Path is the queue file this queue was opened from (log lines, the admin page).
func (q *SQLiteQueue) Path() string {
	if q == nil {
		return ""
	}
	return q.path
}

// Close closes the queue file. It is safe to call twice.
func (q *SQLiteQueue) Close() error {
	if q == nil {
		return nil
	}
	// Swap, so two concurrent Closes cannot both close the same handle. A method that
	// loaded the handle just before this keeps using it: database/sql makes its calls
	// fail with "sql: database is closed" instead of panicking, and the queue's own
	// retry helpers pass that error straight out.
	db := q.db.Swap(nil)
	if db == nil {
		return nil
	}
	return db.Close()
}

// Enqueue adds items in one transaction; an item already queued for that peer is left as it
// is (INSERT OR IGNORE), so a re-offered article does not reset its attempts or its backoff.
// Items without a peer or a message-id are skipped.
func (q *SQLiteQueue) Enqueue(items []Item) error {
	db := q.handle()
	if db == nil {
		return ErrQueueClosed
	}
	if len(items) == 0 {
		return nil
	}
	now := q.now().Unix()
	return database.RetryableTransactionExec(db, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT OR IGNORE INTO feed_queue
			(peer_id, message_id, newsgroup, enqueued_at, attempts, next_attempt, last_error)
			VALUES (?, ?, ?, ?, 0, ?, '')`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, it := range items {
			if it.PeerID <= 0 || it.MessageID == "" {
				continue
			}
			// next_attempt = now: due at once.
			if _, err := stmt.Exec(it.PeerID, it.MessageID, it.Newsgroup, now, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// Claim returns up to max due items of peerID, oldest first, and leases them in the same
// transaction: their next attempt moves to now+lease, so a sender that dies with items in
// flight does not keep them from being sent again (they come back when the lease runs out).
func (q *SQLiteQueue) Claim(peerID int64, max int, now time.Time, lease time.Duration) ([]Item, error) {
	db := q.handle()
	if db == nil {
		return nil, ErrQueueClosed
	}
	if max <= 0 {
		return nil, nil
	}
	var items []Item
	err := database.RetryableTransactionExec(db, func(tx *sql.Tx) error {
		// A retried transaction starts over: never return rows of an aborted attempt.
		items = nil
		due, err := queueSelectDue(tx, peerID, now.Unix(), max)
		if err != nil {
			return err
		}
		if len(due) == 0 {
			return nil
		}
		next := now.Add(lease).Unix()
		for _, chunk := range queueChunks(due) {
			args := make([]interface{}, 0, len(chunk)+2)
			args = append(args, next, peerID)
			for _, it := range chunk {
				args = append(args, it.MessageID)
			}
			query := "UPDATE feed_queue SET next_attempt = ? WHERE peer_id = ? AND message_id IN (" +
				queuePlaceholders(len(chunk)) + ")"
			if _, err := tx.Exec(query, args...); err != nil {
				return err
			}
		}
		items = due
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("feed queue claim peer %d: %w", peerID, err)
	}
	return items, nil
}

// queueSelectDue reads the due items of one peer inside tx. It is its own function so the
// rows are closed before the caller writes on the same connection.
func queueSelectDue(tx *sql.Tx, peerID, now int64, max int) ([]Item, error) {
	rows, err := tx.Query(`SELECT message_id, newsgroup, attempts, last_error FROM feed_queue
		WHERE peer_id = ? AND next_attempt <= ? ORDER BY enqueued_at, message_id LIMIT ?`,
		peerID, now, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		it := Item{PeerID: peerID}
		if err := rows.Scan(&it.MessageID, &it.Newsgroup, &it.Attempts, &it.LastError); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// Done deletes the items the peer has settled (sent, or refused/rejected: both are final).
func (q *SQLiteQueue) Done(peerID int64, messageIDs []string) error {
	db := q.handle()
	if db == nil {
		return ErrQueueClosed
	}
	ids := queueNonEmpty(messageIDs)
	if len(ids) == 0 {
		return nil
	}
	err := database.RetryableTransactionExec(db, func(tx *sql.Tx) error {
		for _, chunk := range queueStringChunks(ids) {
			args := make([]interface{}, 0, len(chunk)+1)
			args = append(args, peerID)
			for _, id := range chunk {
				args = append(args, id)
			}
			query := "DELETE FROM feed_queue WHERE peer_id = ? AND message_id IN (" +
				queuePlaceholders(len(chunk)) + ")"
			if _, err := tx.Exec(query, args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("feed queue done peer %d: %w", peerID, err)
	}
	return nil
}

// Defer records a failed attempt: attempts+1, last_error and the next attempt at next. An
// item that is no longer queued (another sender settled it) is silently not updated.
func (q *SQLiteQueue) Defer(peerID int64, messageID string, lastErr string, next time.Time) error {
	db := q.handle()
	if db == nil {
		return ErrQueueClosed
	}
	if messageID == "" {
		return nil
	}
	if _, err := database.RetryableExec(db,
		`UPDATE feed_queue SET attempts = attempts + 1, last_error = ?, next_attempt = ?
			WHERE peer_id = ? AND message_id = ?`,
		queueTrimError(lastErr), next.Unix(), peerID, messageID); err != nil {
		return fmt.Errorf("feed queue defer peer %d %s: %w", peerID, messageID, err)
	}
	return nil
}

// Depth returns how many items are queued for peerID, whether they are due or not.
func (q *SQLiteQueue) Depth(peerID int64) (int64, error) {
	db := q.handle()
	if db == nil {
		return 0, ErrQueueClosed
	}
	var n int64
	if err := database.RetryableQueryRowScan(db,
		"SELECT COUNT(*) FROM feed_queue WHERE peer_id = ?", []interface{}{peerID}, &n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("feed queue depth peer %d: %w", peerID, err)
	}
	return n, nil
}

// DropPeer deletes every item and the stats of a peer: its row was deleted, or its out feed
// was turned off and the queued articles are not wanted any more.
func (q *SQLiteQueue) DropPeer(peerID int64) error {
	db := q.handle()
	if db == nil {
		return ErrQueueClosed
	}
	err := database.RetryableTransactionExec(db, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM feed_queue WHERE peer_id = ?", peerID); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM feed_stats WHERE peer_id = ?", peerID)
		return err
	})
	if err != nil {
		return fmt.Errorf("feed queue drop peer %d: %w", peerID, err)
	}
	return nil
}

// WriteStats stores a peer's counters, replacing the stored ones. QueueDepth is not stored:
// ReadStats fills it from the queue itself.
func (q *SQLiteQueue) WriteStats(s Stats) error {
	db := q.handle()
	if db == nil {
		return ErrQueueClosed
	}
	if s.PeerID <= 0 {
		return fmt.Errorf("feed queue write stats: no peer")
	}
	updated := s.UpdatedAt
	if updated.IsZero() {
		updated = q.now()
	}
	if _, err := database.RetryableExec(db,
		`INSERT INTO feed_stats (peer_id, offered, accepted, refused, rejected, deferred, last_error, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(peer_id) DO UPDATE SET offered = excluded.offered, accepted = excluded.accepted,
				refused = excluded.refused, rejected = excluded.rejected, deferred = excluded.deferred,
				last_error = excluded.last_error, updated_at = excluded.updated_at`,
		s.PeerID, s.Offered, s.Accepted, s.Refused, s.Rejected, s.Deferred,
		queueTrimError(s.LastError), updated.Unix()); err != nil {
		return fmt.Errorf("feed queue write stats peer %d: %w", s.PeerID, err)
	}
	return nil
}

// ReadStats returns the stored counters with QueueDepth filled from the queue. A peer that
// has queued items but no stored counters yet is returned as well (zero counters and its
// depth), so the admin page shows a backlog before the sender has written anything.
func (q *SQLiteQueue) ReadStats() (map[int64]Stats, error) {
	db := q.handle()
	if db == nil {
		return nil, ErrQueueClosed
	}
	out := make(map[int64]Stats)
	if err := readStatRows(db, out); err != nil {
		return nil, err
	}
	if err := readDepths(db, out); err != nil {
		return nil, err
	}
	return out, nil
}

// readStatRows fills out from feed_stats of db.
func readStatRows(db *sql.DB, out map[int64]Stats) error {
	rows, err := database.RetryableQuery(db,
		`SELECT peer_id, offered, accepted, refused, rejected, deferred, last_error, updated_at
			FROM feed_stats`)
	if err != nil {
		return fmt.Errorf("feed queue read stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s Stats
		var updated int64
		if err := rows.Scan(&s.PeerID, &s.Offered, &s.Accepted, &s.Refused, &s.Rejected,
			&s.Deferred, &s.LastError, &updated); err != nil {
			return fmt.Errorf("feed queue read stats: %w", err)
		}
		if updated > 0 {
			s.UpdatedAt = time.Unix(updated, 0)
		}
		out[s.PeerID] = s
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("feed queue read stats: %w", err)
	}
	return nil
}

// readDepths fills QueueDepth of every peer of db that has queued items, adding a
// zero-counter entry for a peer that has no stats row yet.
func readDepths(db *sql.DB, out map[int64]Stats) error {
	rows, err := database.RetryableQuery(db,
		"SELECT peer_id, COUNT(*) FROM feed_queue GROUP BY peer_id")
	if err != nil {
		return fmt.Errorf("feed queue read depths: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var peerID, depth int64
		if err := rows.Scan(&peerID, &depth); err != nil {
			return fmt.Errorf("feed queue read depths: %w", err)
		}
		s, ok := out[peerID]
		if !ok {
			s = Stats{PeerID: peerID}
		}
		s.QueueDepth = depth
		out[peerID] = s
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("feed queue read depths: %w", err)
	}
	return nil
}

// Prune drops the items that have been queued for longer than the give-up age (an item that
// keeps failing: the peer has been unreachable or keeps deferring it for days). It logs one
// line per peer that lost items and returns how many rows it deleted. The sender calls it;
// it is not part of the Queue contract because only the queue knows the age.
func (q *SQLiteQueue) Prune(now time.Time) (int64, error) {
	db := q.handle()
	if db == nil {
		return 0, ErrQueueClosed
	}
	if q.giveUp <= 0 {
		return 0, nil // keep for ever
	}
	cutoff := now.Add(-q.giveUp).Unix()
	var perPeer map[int64]int64
	var total int64
	err := database.RetryableTransactionExec(db, func(tx *sql.Tx) error {
		perPeer, total = nil, 0
		counts, sum, err := queueCountOld(tx, cutoff)
		if err != nil {
			return err
		}
		if sum == 0 {
			return nil
		}
		if _, err := tx.Exec("DELETE FROM feed_queue WHERE enqueued_at <= ?", cutoff); err != nil {
			return err
		}
		perPeer, total = counts, sum
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("feed queue prune: %w", err)
	}
	for peerID, n := range perPeer {
		log.Printf("[FEED] peer %d: %d queued article(s) dropped, still not sent after %s", peerID, n, q.giveUp)
	}
	return total, nil
}

// queueCountOld counts the rows older than cutoff per peer, for Prune's log lines.
func queueCountOld(tx *sql.Tx, cutoff int64) (map[int64]int64, int64, error) {
	rows, err := tx.Query("SELECT peer_id, COUNT(*) FROM feed_queue WHERE enqueued_at <= ? GROUP BY peer_id", cutoff)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	counts := make(map[int64]int64)
	var total int64
	for rows.Next() {
		var peerID, n int64
		if err := rows.Scan(&peerID, &n); err != nil {
			return nil, 0, err
		}
		counts[peerID] = n
		total += n
	}
	return counts, total, rows.Err()
}

// ---------------------------------------------------------------------------
// small helpers

// queuePlaceholders returns "?, ?, ..." with n entries.
func queuePlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return "?" + strings.Repeat(", ?", n-1)
}

// queueChunks splits items into chunks of at most queueIDChunk.
func queueChunks(items []Item) [][]Item {
	var out [][]Item
	for i := 0; i < len(items); i += queueIDChunk {
		end := min(i+queueIDChunk, len(items))
		out = append(out, items[i:end])
	}
	return out
}

// queueStringChunks splits ids into chunks of at most queueIDChunk.
func queueStringChunks(ids []string) [][]string {
	var out [][]string
	for i := 0; i < len(ids); i += queueIDChunk {
		end := min(i+queueIDChunk, len(ids))
		out = append(out, ids[i:end])
	}
	return out
}

// queueNonEmpty drops empty ids and duplicates, keeping the order.
func queueNonEmpty(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// queueErrorMax bounds the stored last_error, so a peer that answers with a long line
// cannot grow the queue file.
const queueErrorMax = 200

// queueTrimError bounds and single-lines an error text before it is stored. The cut is
// moved back to a rune boundary, so a multi-byte character at the limit is dropped whole
// instead of leaving an invalid UTF-8 tail in the queue file.
func queueTrimError(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
	if len(s) <= queueErrorMax {
		return s
	}
	cut := queueErrorMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
