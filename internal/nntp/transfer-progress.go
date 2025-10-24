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

// TransferResult represents a single transfer result record
type TransferResult struct {
	RemoteID   int64
	Newsgroup  string
	Timestamp  time.Time
	StartDate  *time.Time // Can be nil for no start date filter
	EndDate    *time.Time // Can be nil for no end date filter
	Sent       int64
	Unwanted   int64
	Checked    int64
	Rejected   int64
	Retry      int64
	Skipped    int64
	TXErrors   int64
	ConnErrors int64
}

// OpenTransferProgressDB opens or creates the transfer progress database
func OpenTransferProgressDB(dataDir, remoteName string) (*TransferProgressDB, error) {
	// Create data directory if it doesn't exist
	progressDir := filepath.Join(dataDir, "transfer-progress")
	if err := os.MkdirAll(progressDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create progress directory: %v", err)
	}

	dbPath := filepath.Join(progressDir, "transfer-progress.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	// Enable WAL mode for better concurrent performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set WAL mode: %v", err)
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

// GetRecentTransfers returns recent transfer records for the current remote
func (tpdb *TransferProgressDB) GetRecentTransfers(limit int) ([]TransferResult, error) {
	tpdb.mu.RLock()
	defer tpdb.mu.RUnlock()

	query := `
		SELECT remote_id, newsgroup, timestamp, start_date, end_date, sent, unwanted, checked,
		       rejected, retry, skipped, tx_errors, conn_errors
		FROM transfers
		WHERE remote_id = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := database.RetryableQuery(tpdb.db, query, tpdb.remoteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TransferResult
	for rows.Next() {
		var r TransferResult
		var timestampStr string
		var startDateStr, endDateStr sql.NullString
		err := rows.Scan(
			&r.RemoteID,
			&r.Newsgroup,
			&timestampStr,
			&startDateStr,
			&endDateStr,
			&r.Sent,
			&r.Unwanted,
			&r.Checked,
			&r.Rejected,
			&r.Retry,
			&r.Skipped,
			&r.TXErrors,
			&r.ConnErrors,
		)
		if err != nil {
			return nil, err
		}

		// Parse timestamp
		r.Timestamp, err = time.Parse("2006-01-02 15:04:05", timestampStr)
		if err != nil {
			return nil, err
		}

		// Parse start_date if present
		if startDateStr.Valid {
			parsedStart, err := time.Parse("2006-01-02 15:04:05", startDateStr.String)
			if err != nil {
				return nil, err
			}
			r.StartDate = &parsedStart
		}

		// Parse end_date if present
		if endDateStr.Valid {
			parsedEnd, err := time.Parse("2006-01-02 15:04:05", endDateStr.String)
			if err != nil {
				return nil, err
			}
			r.EndDate = &parsedEnd
		}

		results = append(results, r)
	}

	return results, rows.Err()
}

// NewsgroupExists checks if a newsgroup already has transfer results for the current remote
// with exactly the same start_date and end_date (empty string represents no filter)
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

// Close closes the database connection
func (tpdb *TransferProgressDB) Close() error {
	if tpdb.db != nil {
		return tpdb.db.Close()
	}
	return nil
}
