package peerstats

// The hourly buckets on disk (plan peering-stats): one SQLite file of its own,
// data/cfg/peer-stats.sq3 (Path), written only by nntp-server (Open) and read read-only by
// the web process (OpenReadOnly). The file layout and the DSN follow internal/feed/queue.go,
// the other cfg-level database of its own.
//
// Every statement goes through the internal/database retry helpers, so a busy database is
// retried instead of losing a bucket, and every write runs in one transaction per call.

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

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3" // the store uses the driver go-pugleaf already uses
)

// ErrStoreClosed is returned by every method of a store that is not (or no longer) open.
var ErrStoreClosed = errors.New("peer stats store is not open")

// storeSchema is created on open; every statement is IF NOT EXISTS, so opening an existing
// file only adds what is missing. A later schema change needs its own migration code keyed
// on PRAGMA user_version, not an edited CREATE statement.
var storeSchema = []string{
	`CREATE TABLE IF NOT EXISTS peer_stats_hourly (
		peer_id      INTEGER NOT NULL,
		peer_name    TEXT    NOT NULL DEFAULT '',
		dir          TEXT    NOT NULL,
		hour         INTEGER NOT NULL,
		offered      INTEGER NOT NULL DEFAULT 0,
		accepted     INTEGER NOT NULL DEFAULT 0,
		refused      INTEGER NOT NULL DEFAULT 0,
		rejected     INTEGER NOT NULL DEFAULT 0,
		duplicate    INTEGER NOT NULL DEFAULT 0,
		deferred     INTEGER NOT NULL DEFAULT 0,
		denied       INTEGER NOT NULL DEFAULT 0,
		missing      INTEGER NOT NULL DEFAULT 0,
		acc_bytes    INTEGER NOT NULL DEFAULT 0,
		rej_bytes    INTEGER NOT NULL DEFAULT 0,
		dup_bytes    INTEGER NOT NULL DEFAULT 0,
		conns        INTEGER NOT NULL DEFAULT 0,
		conn_seconds INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (peer_id, dir, hour)
	)`,
	// ReadRange reads "every peer in a time range": the index makes that a range scan
	// over the hours instead of a table scan over the whole retention period.
	`CREATE INDEX IF NOT EXISTS idx_peer_stats_hour ON peer_stats_hourly(hour)`,
}

// storeColumns is the column list of the counters, in the order addBucketSQL binds and
// readRangeSQL scans them.
const storeColumns = `offered, accepted, refused, rejected, duplicate, deferred, denied, ` +
	`missing, acc_bytes, rej_bytes, dup_bytes, conns, conn_seconds`

// addBucketSQL adds the counters of one bucket to its row and inserts the row when it is
// missing. peer_name follows the latest name of the peer, so a renamed peer shows up under
// its new name without touching the old rows' counters.
const addBucketSQL = `INSERT INTO peer_stats_hourly
	(peer_id, peer_name, dir, hour, ` + storeColumns + `)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(peer_id, dir, hour) DO UPDATE SET
		peer_name    = excluded.peer_name,
		offered      = offered      + excluded.offered,
		accepted     = accepted     + excluded.accepted,
		refused      = refused      + excluded.refused,
		rejected     = rejected     + excluded.rejected,
		duplicate    = duplicate    + excluded.duplicate,
		deferred     = deferred     + excluded.deferred,
		denied       = denied       + excluded.denied,
		missing      = missing      + excluded.missing,
		acc_bytes    = acc_bytes    + excluded.acc_bytes,
		rej_bytes    = rej_bytes    + excluded.rej_bytes,
		dup_bytes    = dup_bytes    + excluded.dup_bytes,
		conns        = conns        + excluded.conns,
		conn_seconds = conn_seconds + excluded.conn_seconds`

// readRangeSQL is ordered the way the contract promises: hour, peer, dir.
const readRangeSQL = `SELECT peer_id, peer_name, dir, hour, ` + storeColumns + `
	FROM peer_stats_hourly WHERE hour >= ? AND hour < ?
	ORDER BY hour, peer_id, dir`

// sqliteStore is the Store of api.go, backed by one SQLite file.
type sqliteStore struct {
	// db is behind an atomic pointer because Close may run while the collector's flush
	// goroutine is in one of the methods: every method loads it once and answers
	// ErrStoreClosed on nil, and Close swaps it out (internal/feed/queue.go does the
	// same, for the same reason).
	db       atomic.Pointer[sql.DB]
	path     string
	readOnly bool
}

// sqliteStore is the implementation the contract in api.go describes.
var _ Store = (*sqliteStore)(nil)

// storeDSN is the DSN the store file is opened with. The mattn driver hands every "file:"
// DSN to SQLite's URI parser, so the path has to be escaped (queueDSN of internal/feed does
// the same): an unescaped '?' or '#' in it would cut the path short and drop the parameters.
func storeDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_busy_timeout=30000&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
}

// Open opens (and creates) the store file at path and brings its schema up. The writer
// (nntp-server) uses this one.
func Open(path string) (Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("peer stats: no path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("peer stats: create %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite3", storeDSN(path))
	if err != nil {
		return nil, fmt.Errorf("peer stats %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, storeCloseErr(db, fmt.Errorf("peer stats %s: %w", path, err))
	}
	for _, stmt := range storeSchema {
		if _, err := database.RetryableExec(db, stmt); err != nil {
			return nil, storeCloseErr(db, fmt.Errorf("peer stats %s schema: %w", path, err))
		}
	}
	log.Printf("[PEERSTATS] store db=%q", path)
	s := &sqliteStore{path: path}
	s.db.Store(db)
	return s, nil
}

// OpenReadOnly opens an existing store file read-only, for a process that only reads the
// counters (the web admin page; the file belongs to the nntp-server). It never creates the
// file or its schema: a missing file returns an error wrapping os.ErrNotExist, which the web
// page shows as "no data yet". AddBuckets and Prune fail.
func OpenReadOnly(path string) (Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("peer stats: no path")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("peer stats %s: %w", path, err)
	}
	db, err := sql.Open("sqlite3", storeDSN(path)+"&mode=ro")
	if err != nil {
		return nil, fmt.Errorf("peer stats %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, storeCloseErr(db, fmt.Errorf("peer stats %s: %w", path, err))
	}
	s := &sqliteStore{path: path, readOnly: true}
	s.db.Store(db)
	return s, nil
}

// storeCloseErr closes db on an open that failed and keeps the original error.
func storeCloseErr(db *sql.DB, err error) error {
	if cerr := db.Close(); cerr != nil {
		log.Printf("[PEERSTATS] closing the store file after a failed open: %v", cerr)
	}
	return err
}

// handle returns the open database, or nil when the store was never opened or is closed.
func (s *sqliteStore) handle() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db.Load()
}

// Path is the store file this store was opened from (log lines, the admin page).
func (s *sqliteStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Close closes the store file. It is safe to call twice.
func (s *sqliteStore) Close() error {
	if s == nil {
		return nil
	}
	db := s.db.Swap(nil)
	if db == nil {
		return nil
	}
	return db.Close()
}

// AddBuckets adds the counters of each bucket to its stored row, in one transaction. Buckets
// with no counter set at all are skipped, so an idle peer never creates a row.
func (s *sqliteStore) AddBuckets(b []Bucket) error {
	db := s.handle()
	if db == nil {
		return ErrStoreClosed
	}
	if s.readOnly {
		return fmt.Errorf("peer stats %s: opened read-only", s.path)
	}
	if len(b) == 0 {
		return nil
	}
	return database.RetryableTransactionExec(db, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(addBucketSQL)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, bk := range b {
			if bk.Counters.IsZero() {
				continue
			}
			c := bk.Counters
			if _, err := stmt.Exec(bk.Peer.ID, bk.Peer.Name, bk.Dir.String(), bk.Hour,
				c.Offered, c.Accepted, c.Refused, c.Rejected, c.Duplicate,
				c.Deferred, c.Denied, c.Missing, c.AccBytes, c.RejBytes,
				c.DupBytes, c.Conns, c.ConnSeconds); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReadRange returns the buckets with fromHour <= Hour < toHour, ordered by hour, peer, dir.
func (s *sqliteStore) ReadRange(fromHour, toHour int64) ([]Bucket, error) {
	db := s.handle()
	if db == nil {
		return nil, ErrStoreClosed
	}
	if toHour <= fromHour {
		return nil, nil
	}
	rows, err := database.RetryableQuery(db, readRangeSQL, fromHour, toHour)
	if err != nil {
		return nil, fmt.Errorf("peer stats %s read: %w", s.path, err)
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		var dir string
		c := &b.Counters
		if err := rows.Scan(&b.Peer.ID, &b.Peer.Name, &dir, &b.Hour,
			&c.Offered, &c.Accepted, &c.Refused, &c.Rejected, &c.Duplicate,
			&c.Deferred, &c.Denied, &c.Missing, &c.AccBytes, &c.RejBytes,
			&c.DupBytes, &c.Conns, &c.ConnSeconds); err != nil {
			return nil, fmt.Errorf("peer stats %s read: %w", s.path, err)
		}
		b.Dir = dirFromString(dir)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("peer stats %s read: %w", s.path, err)
	}
	return out, nil
}

// Prune deletes the buckets older than beforeHour and returns how many rows went.
func (s *sqliteStore) Prune(beforeHour int64) (int64, error) {
	db := s.handle()
	if db == nil {
		return 0, ErrStoreClosed
	}
	if s.readOnly {
		return 0, fmt.Errorf("peer stats %s: opened read-only", s.path)
	}
	res, err := database.RetryableExec(db, `DELETE FROM peer_stats_hourly WHERE hour < ?`, beforeHour)
	if err != nil {
		return 0, fmt.Errorf("peer stats %s prune: %w", s.path, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("peer stats %s prune: %w", s.path, err)
	}
	return n, nil
}

// dirFromString is the inverse of Dir.String for the stored dir column.
func dirFromString(s string) Dir {
	switch s {
	case In.String():
		return In
	case Out.String():
		return Out
	}
	return 0
}
