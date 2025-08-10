package database

import (
	"database/sql"
	"log"
	"math/rand"
	"strings"
	"time"
)

const (
	maxRetries = 1000
	baseDelay  = 10 * time.Millisecond
	maxDelay   = 25 * time.Millisecond
)

// isRetryableError checks if the error is a retryable SQLite error
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "database table is locked") ||
		strings.Contains(errStr, "busy") ||
		strings.Contains(errStr, "locked")
}

// retryableExec executes a SQL statement with retry logic for lock conflicts
func retryableExec(db *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	var result sql.Result
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err = db.Exec(query, args...)

		if !isRetryableError(err) {
			return result, err
		}

		if attempt < maxRetries-1 {
			// Exponential backoff with jitter
			delay := time.Duration(attempt+1) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}

			// Add random jitter (up to 50% of delay)
			jitter := time.Duration(rand.Int63n(int64(delay) / 2))
			time.Sleep(delay + jitter)

			log.Printf("[WARN] SQLite retry attempt %d/%d for query (first 50 chars): %s... Error: %v",
				attempt+1, maxRetries, truncateString(query, 50), err)
		}
	}

	return result, err
}

// retryableQueryRow executes a query that returns a single row with retry logic
func retryableQueryRow(db *sql.DB, query string, args ...interface{}) *sql.Row {
	// For QueryRow, we can't detect errors until Scan() is called
	// Return the row directly - callers should handle retryable errors in their Scan() calls
	return db.QueryRow(query, args...)
}

// retryableQueryRowScan executes a QueryRow and Scan with retry logic
func retryableQueryRowScan(db *sql.DB, query string, args []interface{}, dest ...interface{}) error {
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		row := db.QueryRow(query, args...)
		err = row.Scan(dest...)

		if !isRetryableError(err) {
			return err
		}

		if attempt < maxRetries-1 {
			// Exponential backoff with jitter
			delay := time.Duration(attempt+1) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}

			// Add random jitter (up to 50% of delay)
			jitter := time.Duration(rand.Int63n(int64(delay) / 2))
			time.Sleep(delay + jitter)

			log.Printf("SQLite retry attempt %d/%d for QueryRow scan (first 50 chars): %s... Error: %v",
				attempt+1, maxRetries, truncateString(query, 50), err)
		}
	}

	return err
}

// retryableQuery executes a query that returns multiple rows with retry logic
func retryableQuery(db *sql.DB, query string, args ...interface{}) (*sql.Rows, error) {
	var rows *sql.Rows
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		rows, err = db.Query(query, args...)

		if !isRetryableError(err) {
			return rows, err
		}

		if attempt < maxRetries-1 {
			// Exponential backoff with jitter
			delay := time.Duration(attempt+1) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}

			// Add random jitter (up to 50% of delay)
			jitter := time.Duration(rand.Int63n(int64(delay) / 2))
			time.Sleep(delay + jitter)

			log.Printf("SQLite retry attempt %d/%d for query (first 50 chars): %s... Error: %v",
				attempt+1, maxRetries, truncateString(query, 50), err)
		}
	}

	return rows, err
}

// retryableTransactionExec executes a transaction with retry logic
func retryableTransactionExec(db *sql.DB, txFunc func(*sql.Tx) error) error {
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		tx, err := db.Begin()
		if err != nil {
			if !isRetryableError(err) {
				return err
			}
			if attempt < maxRetries-1 {
				// Exponential backoff with jitter
				delay := time.Duration(attempt+1) * baseDelay
				if delay > maxDelay {
					delay = maxDelay
				}
				// Add random jitter (up to 50% of delay)
				jitter := time.Duration(rand.Int63n(int64(delay) / 2))
				time.Sleep(delay + jitter)
				log.Printf("SQLite retry attempt %d/%d for transaction begin: %v", attempt+1, maxRetries, err)
				continue
			}
			return err
		}

		err = txFunc(tx)
		if err != nil {
			tx.Rollback()
			if !isRetryableError(err) {
				return err
			}
			if attempt < maxRetries-1 {
				// Exponential backoff with jitter
				delay := time.Duration(attempt+1) * baseDelay
				if delay > maxDelay {
					delay = maxDelay
				}
				// Add random jitter (up to 50% of delay)
				jitter := time.Duration(rand.Int63n(int64(delay) / 2))
				time.Sleep(delay + jitter)
				log.Printf("SQLite retry attempt %d/%d for transaction: %v", attempt+1, maxRetries, err)
				continue
			}
			return err
		}

		err = tx.Commit()
		if !isRetryableError(err) {
			return err
		}

		if attempt < maxRetries-1 {
			// Exponential backoff with jitter
			delay := time.Duration(attempt+1) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}
			// Add random jitter (up to 50% of delay)
			jitter := time.Duration(rand.Int63n(int64(delay) / 2))
			time.Sleep(delay + jitter)
			log.Printf("SQLite retry attempt %d/%d for transaction commit: %v", attempt+1, maxRetries, err)
		}
	}

	return err
}

// truncateString truncates a string to the specified length
func truncateString(s string, length int) string {
	if len(s) <= length {
		return s
	}
	return s[:length]
}

// retryableStmtExec executes a prepared statement with retry logic for lock conflicts
func retryableStmtExec(stmt *sql.Stmt, args ...interface{}) (sql.Result, error) {
	var result sql.Result
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err = stmt.Exec(args...)

		if !isRetryableError(err) {
			return result, err
		}

		if attempt < maxRetries-1 {
			// Exponential backoff with jitter
			delay := time.Duration(attempt+1) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}

			// Add random jitter (up to 50% of delay)
			jitter := time.Duration(rand.Int63n(int64(delay) / 2))
			time.Sleep(delay + jitter)

			log.Printf("SQLite retry attempt %d/%d for prepared statement exec. Error: %v",
				attempt+1, maxRetries, err)
		}
	}

	return result, err
}

// retryableStmtQueryRowScan executes a prepared statement QueryRow and Scan with retry logic
func retryableStmtQueryRowScan(stmt *sql.Stmt, args []interface{}, dest ...interface{}) error {
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		row := stmt.QueryRow(args...)
		err = row.Scan(dest...)

		if !isRetryableError(err) {
			return err
		}

		if attempt < maxRetries-1 {
			// Exponential backoff with jitter
			delay := time.Duration(attempt+1) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}

			// Add random jitter (up to 50% of delay)
			jitter := time.Duration(rand.Int63n(int64(delay) / 2))
			time.Sleep(delay + jitter)

			log.Printf("SQLite retry attempt %d/%d for prepared statement QueryRow scan. Error: %v",
				attempt+1, maxRetries, err)
		}
	}

	return err
}

// Exported wrapper functions for use by other packages

// RetryableExec executes a SQL statement with retry logic for lock conflicts
func RetryableExec(db *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	return retryableExec(db, query, args...)
}

// RetryableQuery executes a SQL query with retry logic for lock conflicts
func RetryableQuery(db *sql.DB, query string, args ...interface{}) (*sql.Rows, error) {
	return retryableQuery(db, query, args...)
}

// RetryableQueryRow executes a SQL query and returns a single row with retry logic
func RetryableQueryRow(db *sql.DB, query string, args ...interface{}) *sql.Row {
	return retryableQueryRow(db, query, args...)
}

// RetryableQueryRowScan executes a SQL query and scans the result with retry logic
func RetryableQueryRowScan(db *sql.DB, query string, args []interface{}, dest ...interface{}) error {
	return retryableQueryRowScan(db, query, args, dest...)
}

// RetryableTransactionExec executes a transaction with retry logic for lock conflicts
func RetryableTransactionExec(db *sql.DB, txFunc func(*sql.Tx) error) error {
	return retryableTransactionExec(db, txFunc)
}

// RetryableStmtExec executes a prepared statement with retry logic for lock conflicts
func RetryableStmtExec(stmt *sql.Stmt, args ...interface{}) (sql.Result, error) {
	return retryableStmtExec(stmt, args...)
}

// RetryableStmtQueryRowScan executes a prepared statement QueryRow and scans with retry logic
func RetryableStmtQueryRowScan(stmt *sql.Stmt, args []interface{}, dest ...interface{}) error {
	return retryableStmtQueryRowScan(stmt, args, dest...)
}
