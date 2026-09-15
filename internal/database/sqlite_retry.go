package database

import (
	"database/sql"
	"errors"
	"log"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mattn/go-sqlite3"
)

const (
	maxRetries = 10000
	baseDelay  = 10 * time.Millisecond
	maxDelay   = 2500 * time.Millisecond
)

// SQLiteMaxRetryWait caps the total time a Retryable* helper keeps retrying a
// busy/locked SQLite operation. Tools may raise it before opening the database.
var SQLiteMaxRetryWait = 5 * time.Minute

// isRetryableSQLiteError reports whether err is SQLITE_BUSY or SQLITE_LOCKED.
func isRetryableSQLiteError(err error) bool {
	if err == nil {
		return false
	}
	var se sqlite3.Error
	if errors.As(err, &se) && (se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked) {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "database table is locked")
}

// isRetryableError checks if the error is a retryable SQLite error
func isRetryableError(err error) bool {
	return isRetryableSQLiteError(err)
}

// retryLogThrottle reports whether retry attempt n (1-based) is logged.
func retryLogThrottle(n int) bool {
	switch n {
	case 1, 10, 100, 1000:
		return true
	}
	return false
}

// retryBackoff decides whether a failed attempt (0-based) is retried. It gives up
// (and logs) after maxRetries attempts or once SQLiteMaxRetryWait has passed since
// start; otherwise it sleeps with backoff and jitter and returns true.
func retryBackoff(start time.Time, attempt int, what string, err error) bool {
	elapsed := time.Since(start)
	if attempt >= maxRetries-1 || elapsed > SQLiteMaxRetryWait {
		log.Printf("[DATABASE] (#%d) SQLite giving up after %d attempts (%v) for %s: %v",
			atomic.LoadUint64(&queryID), attempt+1, elapsed, what, err)
		return false
	}
	// Linear backoff with jitter
	delay := time.Duration(attempt+1) * baseDelay
	if delay > maxDelay {
		delay = maxDelay
	}
	// Add random jitter (up to 50% of delay)
	jitter := time.Duration(rand.Int63n(int64(delay) / 2))
	if retryLogThrottle(attempt + 1) {
		log.Printf("[DATABASE] (#%d) SQLite retry attempt %d for %s: %v (took %v, retry in %v)",
			atomic.LoadUint64(&queryID), attempt+1, what, err, elapsed, delay+jitter)
	}
	time.Sleep(delay + jitter)
	return true
}

func retryQueryLabel(query string) string {
	return "query (first 50 chars): " + truncateString(query, 50) + "..."
}

// retryableExec executes a SQL statement with retry logic for lock conflicts
func RetryableExec(db *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	atomic.AddUint64(&queryID, 1)
	for attempt := 0; ; attempt++ {
		result, err := db.Exec(query, args...)
		if !isRetryableError(err) {
			return result, err
		}
		if !retryBackoff(start, attempt, retryQueryLabel(query), err) {
			return result, err
		}
	}
}

// retryableExecPtr executes a SQL statement with retry logic for lock conflicts
func RetryableExecPtr(db *sql.DB, query *strings.Builder, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	atomic.AddUint64(&queryID, 1)
	for attempt := 0; ; attempt++ {
		result, err := db.Exec(query.String(), args...)
		if !isRetryableError(err) {
			return result, err
		}
		if !retryBackoff(start, attempt, retryQueryLabel(query.String()), err) {
			return result, err
		}
	}
}

// retryableQueryRowScan executes a QueryRow and Scan with retry logic
func RetryableQueryRowScan(db *sql.DB, query string, args []interface{}, dest ...interface{}) error {
	start := time.Now()
	atomic.AddUint64(&queryID, 1)
	for attempt := 0; ; attempt++ {
		err := db.QueryRow(query, args...).Scan(dest...)
		if !isRetryableError(err) {
			return err
		}
		if !retryBackoff(start, attempt, "QueryRow scan "+retryQueryLabel(query), err) {
			return err
		}
	}
}

// retryableQuery executes a query that returns multiple rows with retry logic
func RetryableQuery(db *sql.DB, query string, args ...interface{}) (*sql.Rows, error) {
	start := time.Now()
	atomic.AddUint64(&queryID, 1)
	for attempt := 0; ; attempt++ {
		rows, err := db.Query(query, args...)
		if !isRetryableError(err) {
			return rows, err
		}
		// Close the rows if we got them but had a retryable error
		if rows != nil {
			rows.Close()
		}
		if !retryBackoff(start, attempt, retryQueryLabel(query), err) {
			return nil, err
		}
	}
}

// retryableTransactionExec executes a transaction with retry logic
func RetryableTransactionExec(db *sql.DB, txFunc func(*sql.Tx) error) error {
	var err error
	start := time.Now()
	atomic.AddUint64(&queryID, 1)
	for attempt := 0; ; attempt++ {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			err = beginErr
			if !isRetryableError(err) || !retryBackoff(start, attempt, "transaction begin", err) {
				return err
			}
			continue
		}

		err = txFunc(tx)
		if err != nil {
			tx.Rollback()
			if !isRetryableError(err) || !retryBackoff(start, attempt, "transaction", err) {
				return err
			}
			continue
		}

		err = tx.Commit()
		if !isRetryableError(err) {
			return err
		}
		if !retryBackoff(start, attempt, "transaction commit", err) {
			return err
		}
	}
}

// truncateString truncates a string to the specified length
func truncateString(s string, length int) string {
	if len(s) <= length {
		return s
	}
	return s[:length]
}

var queryID uint64

// retryableStmtExec executes a prepared statement with retry logic for lock conflicts
func RetryableStmtExec(stmt *sql.Stmt, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	atomic.AddUint64(&queryID, 1)
	for attempt := 0; ; attempt++ {
		result, err := stmt.Exec(args...)
		if !isRetryableError(err) {
			return result, err
		}
		if !retryBackoff(start, attempt, "prepared statement exec", err) {
			return result, err
		}
	}
}

// retryableStmtQueryRowScan executes a prepared statement QueryRow and Scan with retry logic
func RetryableStmtQueryRowScan(stmt *sql.Stmt, args []interface{}, dest ...interface{}) error {
	start := time.Now()
	atomic.AddUint64(&queryID, 1)
	for attempt := 0; ; attempt++ {
		err := stmt.QueryRow(args...).Scan(dest...)
		if !isRetryableError(err) {
			return err
		}
		if !retryBackoff(start, attempt, "prepared statement QueryRow scan", err) {
			return err
		}
	}
}
