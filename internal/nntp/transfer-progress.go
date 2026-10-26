package nntp

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3"
)

// TransferProgressDB manages the SQLite database for tracking transfer progress
type TransferProgressDB struct {
	db         *sql.DB
	remoteID   int64
	remoteName string
	mu         sync.RWMutex
}

// OpenTransferProgressDB opens or creates the transfer progress database
func OpenTransferProgressDB(dataDir, remoteName string) (*TransferProgressDB, error) {
	// Create data directory if it doesn't exist
	progressDir := filepath.Join(dataDir, "transfer-progress")
	if err := os.MkdirAll(progressDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create progress directory: %v", err)
	}

	dbPath := filepath.Join(progressDir, "transfer-progress.db")
	// busy_timeout and WAL go into the DSN so that every pooled connection gets both
	// (a PRAGMA Exec reaches only the one connection it runs on).
	db, err := sql.Open("sqlite3", dbPath+"?_busy_timeout=30000&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	// Create tables if they don't exist
	if err := createTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create tables: %v", err)
	}

	tpdb := &TransferProgressDB{
		db:         db,
		remoteName: remoteName,
	}

	// Get or create remote ID
	remoteID, err := tpdb.getOrCreateRemoteTransferProgress(remoteName)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to get/create remote: %v", err)
	}
	tpdb.remoteID = remoteID

	log.Printf("Transfer progress database opened: %s (remote_id=%d, hostname=%s)", dbPath, remoteID, remoteName)
	return tpdb, nil
}

// createTables creates the necessary database tables
func createTables(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS remotes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		hostname TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL DEFAULT (datetime('now', 'utc'))
	);

	CREATE TABLE IF NOT EXISTS transfers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		remote_id INTEGER NOT NULL,
		newsgroup TEXT NOT NULL,
		timestamp TEXT NOT NULL,
		start_date TEXT NOT NULL DEFAULT "",
		end_date TEXT NOT NULL DEFAULT "",
		sent INTEGER NOT NULL DEFAULT 0,
		unwanted INTEGER NOT NULL DEFAULT 0,
		checked INTEGER NOT NULL DEFAULT 0,
		rejected INTEGER NOT NULL DEFAULT 0,
		retry INTEGER NOT NULL DEFAULT 0,
		skipped INTEGER NOT NULL DEFAULT 0,
		tx_errors INTEGER NOT NULL DEFAULT 0,
		conn_errors INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (remote_id) REFERENCES remotes(id)
	);

	CREATE INDEX IF NOT EXISTS idx_transfers_remote_newsgroup
		ON transfers(remote_id, newsgroup);

	CREATE INDEX IF NOT EXISTS idx_transfers_remote_ng_dates
		ON transfers(remote_id, newsgroup, start_date, end_date);

	CREATE INDEX IF NOT EXISTS idx_transfers_timestamp
		ON transfers(timestamp DESC);
	`

	_, err := database.RetryableExec(db, schema)
	return err
}

const query_getOrCreateRemote = "INSERT INTO remotes (hostname, created_at) VALUES (?, datetime('now', 'utc'))"

// getOrCreateRemote gets or creates a remote server record
func (tpdb *TransferProgressDB) getOrCreateRemoteTransferProgress(hostname string) (int64, error) {
	tpdb.mu.Lock()
	defer tpdb.mu.Unlock()

	// Try to get existing remote
	var id int64
	err := database.RetryableQueryRowScan(
		tpdb.db,
		"SELECT id FROM remotes WHERE hostname = ?",
		[]interface{}{hostname},
		&id,
	)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	// Create new remote
	result, err := database.RetryableExec(tpdb.db, query_getOrCreateRemote, hostname)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

const query_InsertResult = `
		INSERT INTO transfers (
			remote_id, newsgroup, timestamp, start_date, end_date, sent, unwanted, checked,
			rejected, retry, skipped, tx_errors, conn_errors
		) VALUES (?, ?, datetime('now', 'utc'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

// InsertResult inserts a transfer result into the database
func (tpdb *TransferProgressDB) InsertResult(newsgroup string, startDate, endDate *time.Time, sent, unwanted, checked, rejected, retry, skipped, txErrors, connErrors int64) error {
	tpdb.mu.Lock()
	defer tpdb.mu.Unlock()

	// Convert time pointers to strings (empty string if nil to match NOT NULL DEFAULT "")
	var startDateStr, endDateStr string
	if startDate != nil {
		startDateStr = startDate.UTC().Format("2006-01-02 15:04:05")
	} else {
		startDateStr = ""
	}
	if endDate != nil {
		endDateStr = endDate.UTC().Format("2006-01-02 15:04:05")
	} else {
		endDateStr = ""
	}

	_, err := database.RetryableExec(
		tpdb.db,
		query_InsertResult,
		tpdb.remoteID,
		newsgroup,
		startDateStr,
		endDateStr,
		sent,
		unwanted,
		checked,
		rejected,
		retry,
		skipped,
		txErrors,
		connErrors,
	)

	if err != nil {
		return fmt.Errorf("failed to insert transfer result: %v", err)
	}

	return nil
}

// GetRemoteID returns the current remote ID
func (tpdb *TransferProgressDB) GetRemoteID() int64 {
	tpdb.mu.RLock()
	defer tpdb.mu.RUnlock()
	return tpdb.remoteID
}

// GetRemoteName returns the current remote hostname
func (tpdb *TransferProgressDB) GetRemoteName() string {
	tpdb.mu.RLock()
	defer tpdb.mu.RUnlock()
	return tpdb.remoteName
}

// NewsgroupExists checks if a newsgroup already has transfer results for the current remote
// with exactly the same start_date and end_date (empty string represents no filter).
// It reports whether any such row exists, whatever its retry/error counters: it is not the
// "done" test of nntp-transfer, which is GetAllProgressNewsgroups (only a latest row with
// retry = 0, tx_errors = 0 and conn_errors = 0 marks a group as done). No caller in this tree.
func (tpdb *TransferProgressDB) NewsgroupExists(newsgroup string, startDate, endDate *time.Time) (bool, error) {
	tpdb.mu.RLock()
	defer tpdb.mu.RUnlock()

	// Convert time pointers to strings (empty string if nil to match schema)
	var startDateStr, endDateStr string
	if startDate != nil {
		startDateStr = startDate.UTC().Format("2006-01-02 15:04:05")
	} else {
		startDateStr = ""
	}
	if endDate != nil {
		endDateStr = endDate.UTC().Format("2006-01-02 15:04:05")
	} else {
		endDateStr = ""
	}

	// Query that checks for exact match including empty strings
	query := `
		SELECT COUNT(*) FROM transfers
		WHERE remote_id = ?
		AND newsgroup = ?
		AND start_date = ?
		AND end_date = ?
	`

	var count int64
	err := database.RetryableQueryRowScan(
		tpdb.db,
		query,
		[]interface{}{
			tpdb.remoteID,
			newsgroup,
			startDateStr,
			endDateStr,
		},
		&count,
	)
	if err != nil {
		return false, fmt.Errorf("failed to check newsgroup existence: %v", err)
	}

	return count > 0, nil
}

// query_getAllProgressNewsgroupsDone selects the groups whose latest transfer row for a remote and date range
// is complete: no retries (e.g. CHECK 431), no TX errors and no connection errors.
const query_getAllProgressNewsgroupsDone = `
	SELECT t.newsgroup FROM transfers t
	WHERE t.remote_id = ? AND t.start_date = ? AND t.end_date = ?
	  AND t.id = (SELECT MAX(id) FROM transfers x
	              WHERE x.remote_id = t.remote_id AND x.newsgroup = t.newsgroup
	                AND x.start_date = t.start_date AND x.end_date = t.end_date)
	  AND t.retry = 0 AND t.tx_errors = 0 AND t.conn_errors = 0
`

// GetAllProgressNewsgroups fetches the newsgroups whose latest result for the current remote and date range
// is complete (retry, tx_errors and conn_errors all 0). A group whose latest run had retries or errors is not
// included, so the next run with the same date range offers it again.
// Returns a map for O(1) lookup performance
func (tpdb *TransferProgressDB) GetAllProgressNewsgroups(startDate, endDate *time.Time) (map[string]bool, error) {
	tpdb.mu.RLock()
	defer tpdb.mu.RUnlock()

	// Convert time pointers to strings (empty string if nil to match schema)
	var startDateStr, endDateStr string
	if startDate != nil {
		startDateStr = startDate.UTC().Format("2006-01-02 15:04:05")
	} else {
		startDateStr = ""
	}
	if endDate != nil {
		endDateStr = endDate.UTC().Format("2006-01-02 15:04:05")
	} else {
		endDateStr = ""
	}

	rows, err := database.RetryableQuery(
		tpdb.db,
		query_getAllProgressNewsgroupsDone,
		tpdb.remoteID,
		startDateStr,
		endDateStr,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch progress newsgroups: %v", err)
	}
	defer rows.Close()

	progressMap := make(map[string]bool)
	for rows.Next() {
		var newsgroup string
		if err := rows.Scan(&newsgroup); err != nil {
			return nil, fmt.Errorf("failed to scan newsgroup: %v", err)
		}
		progressMap[newsgroup] = true
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating progress newsgroups: %v", err)
	}

	return progressMap, nil
}

// Close closes the database connection
func (tpdb *TransferProgressDB) Close() error {
	if tpdb.db != nil {
		return tpdb.db.Close()
	}
	return nil
}
