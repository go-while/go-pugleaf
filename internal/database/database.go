// Package database provides database abstraction and management for go-pugleaf
package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite3 driver
)

var DBidleTimeOut = 1 * time.Hour // HARDCODED cleanupIdleGroups

// GetMainDB returns the main database connection for direct access
// This should only be used by specialized tools like importers
func (db *Database) GetMainDB() *sql.DB {
	return db.mainDB
}

func (db *Database) CronDB() {
	db.cronDBEvery(10 * time.Second)
}

// cronDBEvery closes idle group databases on every tick until Shutdown has marked the
// group databases closed. It deliberately does not stop on StopChan: the batch drain
// keeps opening group databases after that channel is closed, and without this loop a
// large backlog would exceed MaxOpenDatabases and the fd limit during the final flush.
// CronDB is started outside db.WG (db_init.go), so db.WG.Wait() never waits for it.
func (db *Database) cronDBEvery(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for range ticker.C {
		db.MainMutex.RLock()
		shutdown := db.groupDBsShutdown
		db.MainMutex.RUnlock()
		if shutdown {
			return
		}
		db.cleanupIdleGroups()
	}
}

// groupDBToClose is a group database removed from the map that still has to be closed.
type groupDBToClose struct {
	name string
	age  time.Duration
	db   *sql.DB
}

func (db *Database) cleanupIdleGroups() {
	db.cleanupIdleGroupsWith(DBidleTimeOut)
}

// cleanupIdleGroupsWith closes group databases without workers that were idle longer
// than idle. When MaxOpenDatabases or more are open, the oldest databases without
// workers are closed regardless of idle time until at most MaxOpenDatabases/2 remain.
// Entries are marked CLOSED and removed under the locks; the *sql.DB is closed after
// the locks are released.
func (db *Database) cleanupIdleGroupsWith(idle time.Duration) {
	db.cleanupGroupDBsWithLimit(idle, MaxOpenDatabases)
}

// cleanupGroupDBsWithLimit is cleanupIdleGroupsWith with maxOpen in place of MaxOpenDatabases (tests).
func (db *Database) cleanupGroupDBsWithLimit(idle time.Duration, maxOpen int) {
	type candidate struct {
		name    string
		groupDB *GroupDB
		age     time.Duration
	}

	db.MainMutex.RLock()
	force := db.openDBsNum >= maxOpen
	candidates := make([]candidate, 0, len(db.groupDB))
	for groupName, groupDB := range db.groupDB {
		if groupDB == nil {
			log.Printf("cleanupIdleGroups Warning: GroupDB for '%s' is nil, skipping", groupName)
			continue
		}
		groupDB.mux.RLock()
		candidates = append(candidates, candidate{name: groupName, groupDB: groupDB, age: time.Since(groupDB.Idle)})
		groupDB.mux.RUnlock()
	}
	db.MainMutex.RUnlock()
	if len(candidates) == 0 {
		return
	}

	// Sort by age (oldest first)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].age > candidates[j].age })

	var toClose []groupDBToClose
	db.MainMutex.Lock()
	for _, c := range candidates {
		if force && db.openDBsNum <= maxOpen/2 {
			break
		}
		if db.groupDB[c.name] != c.groupDB {
			continue // already removed or replaced
		}
		g := c.groupDB
		g.mux.Lock()
		if g.Workers < 0 {
			log.Printf("Warning: Negative worker count for group '%s': %d", c.name, g.Workers)
		}
		if g.state == stateCREATED && g.Workers == 0 && (force || time.Since(g.Idle) > idle) {
			g.state = stateCLOSED
			toClose = append(toClose, groupDBToClose{name: c.name, age: time.Since(g.Idle), db: g.DB})
			delete(db.groupDB, c.name)
			db.openDBsNum--
		}
		g.mux.Unlock()
	}
	openNow := db.openDBsNum
	db.MainMutex.Unlock()

	closedCount := 0
	for _, c := range toClose {
		if c.db == nil {
			continue
		}
		if err := c.db.Close(); err != nil {
			log.Printf("[DATABASE] Failed to close group database for '%s': %v", c.name, err)
			continue
		}
		closedCount++
		if force {
			log.Printf("Force closed idle DB ng: '%s' (age: %v)", c.name, c.age)
		}
	}
	if force {
		log.Printf("Force closed %d databases due to exceeding limit (%d >= %d)", closedCount, openNow+len(toClose), maxOpen)
	}
}

// Close closes all database connections
func (db *Database) Shutdown() error {
	var errs []error

	// Close per-group databases first (thousands of them)
	var toClose []groupDBToClose
	db.MainMutex.Lock()
	db.groupDBsShutdown = true
	log.Printf("[DATABASE] Closing %d group databases...", len(db.groupDB))
	for groupName, groupDB := range db.groupDB {
		if groupDB == nil {
			continue
		}
		groupDB.mux.Lock()
		// An entry still initializing (state 0) keeps its *sql.DB: its creator sees
		// CLOSED and closes it, so it is not closed under a running migration.
		if groupDB.state != 0 && groupDB.DB != nil {
			toClose = append(toClose, groupDBToClose{name: groupName, db: groupDB.DB})
		}
		groupDB.state = stateCLOSED
		groupDB.mux.Unlock()
	}
	// Clear the group databases map
	db.groupDB = make(map[string]*GroupDB)
	db.openDBsNum = 0
	db.MainMutex.Unlock()

	groupCloseErrors := 0
	for _, c := range toClose {
		if err := c.db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close group database %s: %w", c.name, err))
			groupCloseErrors++
		}
	}
	if groupCloseErrors > 0 {
		log.Printf("[DATABASE] Failed to close %d group databases", groupCloseErrors)
	}
	log.Printf("[DATABASE] Group databases closed")

	// Close main database last
	if db.mainDB != nil {
		if err := db.mainDB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close main database: %w", err))
		} else {
			log.Printf("[DATABASE] Main database closed")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing databases: %v", errs)
	}

	log.Printf("[DATABASE] All databases closed successfully")
	return nil
}

// GetDataDir returns the data directory path
func (db *Database) GetDataDir() string {
	return db.dbconfig.DataDir
}

// Stats returns database statistics
type Stats struct {
	MainDB struct {
		OpenConnections int
		IdleConnections int
		WaitCount       int64
		WaitDuration    time.Duration
	}
	GroupDB map[string]struct {
		OpenConnections int
		IdleConnections int
		WaitCount       int64
		WaitDuration    time.Duration
	}
}

// GetDatabaseStats returns database connection statistics
func (db *Database) GetDatabaseStats() *Stats {
	stats := &Stats{
		GroupDB: make(map[string]struct {
			OpenConnections int
			IdleConnections int
			WaitCount       int64
			WaitDuration    time.Duration
		}),
	}

	// Main database stats
	if db.mainDB != nil {
		dbStats := db.mainDB.Stats()
		stats.MainDB.OpenConnections = dbStats.OpenConnections
		stats.MainDB.IdleConnections = dbStats.Idle
		stats.MainDB.WaitCount = dbStats.WaitCount
		stats.MainDB.WaitDuration = dbStats.WaitDuration
	}

	// Group database stats
	db.MainMutex.RLock()
	defer db.MainMutex.RUnlock()
	for groupName, groupDB := range db.groupDB {
		if groupDB != nil {
			groupDB.mux.RLock()
			if groupDB.DB != nil {
				dbStats := groupDB.DB.Stats()
				stats.GroupDB[groupName] = struct {
					OpenConnections int
					IdleConnections int
					WaitCount       int64
					WaitDuration    time.Duration
				}{
					OpenConnections: dbStats.OpenConnections,
					IdleConnections: dbStats.Idle,
					WaitCount:       dbStats.WaitCount,
					WaitDuration:    dbStats.WaitDuration,
				}
			}
			groupDB.mux.RUnlock()
		}
	}

	return stats
}

// GetHistoryUseShortHashLen retrieves the UseShortHashLen setting from the database
// Returns the stored value, or the provided default if not found
func (db *Database) GetHistoryUseShortHashLen(defaultValue int) (int, bool, error) {
	var value string
	var locked string

	// Get the UseShortHashLen value
	err := RetryableQueryRowScan(db.mainDB, "SELECT value FROM config WHERE key = ?", []interface{}{"history_use_short_hash_len"}, &value)
	if err != nil {
		if err == sql.ErrNoRows {
			// Not found, use default
			return defaultValue, false, nil
		}
		return 0, false, fmt.Errorf("failed to query history_use_short_hash_len: %w", err)
	}

	// Check if config is locked
	err = RetryableQueryRowScan(db.mainDB, "SELECT value FROM config WHERE key = ?", []interface{}{"history_config_locked"}, &locked)
	if err != nil && err != sql.ErrNoRows {
		return 0, false, fmt.Errorf("failed to query history_config_locked: %w", err)
	}

	// Parse the value
	var hashLen int
	if _, err := fmt.Sscanf(value, "%d", &hashLen); err != nil {
		return 0, false, fmt.Errorf("invalid history_use_short_hash_len value in database: %s", value)
	}

	isLocked := (locked == "true")
	return hashLen, isLocked, nil
}

// SetHistoryUseShortHashLen stores the UseShortHashLen setting in the database
// This should only be called on first initialization
func (db *Database) SetHistoryUseShortHashLen(value int) error {
	// Validate range
	if value < 2 || value > 7 {
		return fmt.Errorf("UseShortHashLen must be between 2 and 7, got %d", value)
	}

	// Check if already locked
	hashLen, isLocked, err := db.GetHistoryUseShortHashLen(value)
	if err != nil {
		return fmt.Errorf("failed to check existing config: %w", err)
	}

	if isLocked {
		return fmt.Errorf("history configuration is locked, cannot change UseShortHashLen from %d to %d", hashLen, value)
	}

	// Store the value
	_, err = RetryableExec(db.mainDB, "INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)",
		"history_use_short_hash_len", fmt.Sprintf("%d", value))
	if err != nil {
		return fmt.Errorf("failed to store history_use_short_hash_len: %w", err)
	}

	// Lock the configuration to prevent future changes
	_, err = RetryableExec(db.mainDB, "INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)",
		"history_config_locked", "true")
	if err != nil {
		return fmt.Errorf("failed to lock history configuration: %w", err)
	}

	log.Printf("History UseShortHashLen set to %d and locked", value)
	return nil
}

// InitializeSystemStatus sets up the system status on startup
func (db *Database) InitializeSystemStatus(appVersion string) error {
	if db.mainDB == nil {
		return fmt.Errorf("main database not initialized")
	}

	// Update the system status with current app info and set to running state
	query := `UPDATE system_status SET
		app_version = ?,
		pid = ?,
		hostname = ?,
		last_heartbeat = CURRENT_TIMESTAMP,
		shutdown_state = ''
		WHERE id = 1`
	hostname, _ := os.Hostname()
	pid := os.Getpid()

	_, err := RetryableExec(db.mainDB, query, appVersion, pid, hostname)
	if err != nil {
		return fmt.Errorf("failed to initialize system status: %w", err)
	}

	log.Printf("[DATABASE] System status initialized: version=%s, pid=%d", appVersion, pid)
	return nil
}

// GetNewsgroupID returns the ID of a newsgroup by name
func (db *Database) GetNewsgroupID(groupName string) (int, error) {
	var id int
	err := RetryableQueryRowScan(db.mainDB, "SELECT id FROM newsgroups WHERE name = ?", []interface{}{groupName}, &id)
	if err != nil {
		return 0, fmt.Errorf("failed to get newsgroup ID for '%s': %w", groupName, err)
	}
	return id, nil
}

// IncrementArticleSpam increments the spam counter for a specific article
func (db *Database) IncrementArticleSpam(groupName string, articleNum int64) error {
	log.Printf("DEBUG: IncrementArticleSpam called with group=%s, articleNum=%d", groupName, articleNum)

	// Get newsgroup ID first
	newsgroupID, err := db.GetNewsgroupID(groupName)
	if err != nil {
		log.Printf("DEBUG: Failed to get newsgroup ID for %s: %v", groupName, err)
		return fmt.Errorf("failed to get newsgroup ID: %w", err)
	}
	log.Printf("DEBUG: Found newsgroupID=%d for group %s", newsgroupID, groupName)

	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		log.Printf("DEBUG: Failed to get group databases for %s: %v", groupName, err)
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDB.Return()

	// Update spam counter in group database
	result, err := RetryableExec(groupDB.DB, "UPDATE articles SET spam = spam + 1 WHERE article_num = ?", articleNum)
	if err != nil {
		log.Printf("DEBUG: Failed to update spam count in group DB: %v", err)
		return fmt.Errorf("failed to increment spam count: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	log.Printf("DEBUG: Updated %d rows in articles table for article %d", rowsAffected, articleNum)

	// Add to main database spam table
	result2, err := RetryableExec(db.mainDB, "INSERT OR IGNORE INTO spam (newsgroup_id, article_num) VALUES (?, ?)", newsgroupID, articleNum)
	if err != nil {
		log.Printf("DEBUG: Failed to insert into spam table: %v", err)
		return fmt.Errorf("failed to add to spam table: %w", err)
	}

	rowsAffected2, _ := result2.RowsAffected()
	log.Printf("DEBUG: Inserted %d rows into spam table (0 means already exists)", rowsAffected2)

	return nil
}

// IncrementArticleHide increments the hide counter for a specific article
func (db *Database) IncrementArticleHide(groupName string, articleNum int64) error {
	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDB.Return()

	_, err = RetryableExec(groupDB.DB, "UPDATE articles SET hide = 1 WHERE article_num = ? AND spam > 0", articleNum)
	if err != nil {
		return fmt.Errorf("failed to increment hide count: %w", err)
	}

	return nil
}

// UnHideArticle sets the hide counter to zero for a specific article
func (db *Database) UnHideArticle(groupName string, articleNum int64) error {
	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDB.Return()

	_, err = RetryableExec(groupDB.DB, "UPDATE articles SET hide = 0 WHERE article_num = ?", articleNum)
	if err != nil {
		return fmt.Errorf("failed to unhide: %w", err)
	}

	return nil
}

// DecrementArticleSpam decrements the spam counter for a specific article (admin only)
func (db *Database) DecrementArticleSpam(groupName string, articleNum int64) error {
	log.Printf("DEBUG: DecrementArticleSpam called with group=%s, articleNum=%d", groupName, articleNum)

	// Get newsgroup ID first
	newsgroupID, err := db.GetNewsgroupID(groupName)
	if err != nil {
		log.Printf("DEBUG: Failed to get newsgroup ID for %s: %v", groupName, err)
		return fmt.Errorf("failed to get newsgroup ID: %w", err)
	}
	log.Printf("DEBUG: Found newsgroupID=%d for group %s", newsgroupID, groupName)

	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		log.Printf("DEBUG: Failed to get group databases for %s: %v", groupName, err)
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDB.Return()

	// Check current spam count first
	var currentSpam int
	err = RetryableQueryRowScan(groupDB.DB, "SELECT spam FROM articles WHERE article_num = ?", []interface{}{articleNum}, &currentSpam)
	if err != nil {
		log.Printf("DEBUG: Failed to get current spam count: %v", err)
		return fmt.Errorf("failed to get current spam count: %w", err)
	}

	if currentSpam <= 0 {
		log.Printf("DEBUG: Article %d already has spam count of %d, cannot decrement", articleNum, currentSpam)
		return fmt.Errorf("article spam count is already %d, cannot decrement below 0", currentSpam)
	}

	// Decrement spam counter in group database
	result, err := RetryableExec(groupDB.DB, "UPDATE articles SET spam = spam - 1 WHERE article_num = ? AND spam > 0", articleNum)
	if err != nil {
		log.Printf("DEBUG: Failed to decrement spam count in group DB: %v", err)
		return fmt.Errorf("failed to decrement spam count: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	log.Printf("DEBUG: Decremented spam count for %d rows in articles table for article %d", rowsAffected, articleNum)

	// If spam count reaches 0, remove from main database spam table and clear all user flags
	if currentSpam == 1 {
		result2, err := RetryableExec(db.mainDB, "DELETE FROM spam WHERE newsgroup_id = ? AND article_num = ?", newsgroupID, articleNum)
		if err != nil {
			log.Printf("DEBUG: Failed to remove from spam table: %v", err)
			return fmt.Errorf("failed to remove from spam table: %w", err)
		}

		rowsAffected2, _ := result2.RowsAffected()
		log.Printf("DEBUG: Removed %d rows from spam table", rowsAffected2)

		// Also remove all user spam flags for this article
		result3, err := RetryableExec(db.mainDB, "DELETE FROM user_spam_flags WHERE newsgroup_id = ? AND article_num = ?", newsgroupID, articleNum)
		if err != nil {
			log.Printf("DEBUG: Failed to clear user spam flags: %v", err)
			return fmt.Errorf("failed to clear user spam flags: %w", err)
		}

		rowsAffected3, _ := result3.RowsAffected()
		log.Printf("DEBUG: Cleared %d user spam flags for article %d", rowsAffected3, articleNum)
	}

	return nil
}

// HasUserFlaggedSpam checks if a user has already flagged a specific article as spam
func (db *Database) HasUserFlaggedSpam(userID int64, groupName string, articleNum int64) (bool, error) {
	// Get newsgroup ID
	newsgroupID, err := db.GetNewsgroupID(groupName)
	if err != nil {
		return false, fmt.Errorf("failed to get newsgroup ID: %w", err)
	}

	var count int
	err = RetryableQueryRowScan(db.mainDB, `
		SELECT COUNT(*) FROM user_spam_flags
		WHERE user_id = ? AND newsgroup_id = ? AND article_num = ?`,
		[]interface{}{userID, newsgroupID, articleNum}, &count)

	if err != nil {
		return false, fmt.Errorf("failed to check user spam flag: %w", err)
	}

	return count > 0, nil
}

// RecordUserSpamFlag records that a user has flagged an article as spam
func (db *Database) RecordUserSpamFlag(userID int64, groupName string, articleNum int64) error {
	// Get newsgroup ID
	newsgroupID, err := db.GetNewsgroupID(groupName)
	if err != nil {
		return fmt.Errorf("failed to get newsgroup ID: %w", err)
	}

	_, err = RetryableExec(db.mainDB, `
		INSERT OR IGNORE INTO user_spam_flags (user_id, newsgroup_id, article_num)
		VALUES (?, ?, ?)`,
		userID, newsgroupID, articleNum)

	if err != nil {
		return fmt.Errorf("failed to record user spam flag: %w", err)
	}

	return nil
}
