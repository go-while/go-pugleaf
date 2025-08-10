// Package database provides database abstraction and management for go-pugleaf
package database

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite3 driver
)

const DBidleTimeOut = 1 * time.Hour // HARDCODED cleanupIdleGroups

// GetMainDB returns the main database connection for direct access
// This should only be used by specialized tools like importers
func (db *Database) GetMainDB() *sql.DB {
	return db.mainDB
}

func (db *Database) CronDB() {
	baseSleep := 10 * time.Second
	for {
		// Adaptive sleep: longer intervals during heavy load to reduce mutex contention
		time.Sleep(baseSleep)
		db.cleanupIdleGroups()
	}
}

func (db *Database) cleanupIdleGroups() {
	db.MainMutex.RLock()
	shouldClose := db.openDBsNum >= MaxOpenDatabases // TODO HARDCODED
	if shouldClose {
		// force close oldest groupDBs until 20% under limit of MaxOpenDatabases (* 0.8)
		targetClose := MaxOpenDatabases / 5 // Close 20% of max to get under limit (256/5 = 51)
		closedCount := 0

		// Find oldest databases to close
		type dbAge struct {
			name string
			age  time.Duration
		}
		var candidates []dbAge

		for groupName, groupDBs := range db.groupDBs {
			if groupDBs == nil {
				log.Printf("cleanupIdleGroups Warning: GroupDBs for '%s' is nil, skipping", groupName)
				continue
			}
			groupDBs.mux.RLock()
			candidates = append(candidates, dbAge{
				name: groupName,
				age:  time.Since(groupDBs.Idle),
			})
			groupDBs.mux.RUnlock()
		}

		// Sort by age (oldest first)
		for i := 0; i < len(candidates)-1; i++ {
			for j := i + 1; j < len(candidates); j++ {
				if candidates[i].age < candidates[j].age {
					candidates[i], candidates[j] = candidates[j], candidates[i]
				}
			}
		}

		db.MainMutex.RUnlock()

		// Close oldest databases
		for _, candidate := range candidates {
			if closedCount >= targetClose {
				break
			}

			db.MainMutex.Lock()
			groupDBs := db.groupDBs[candidate.name]
			if groupDBs != nil {
				groupDBs.mux.Lock()
				if groupDBs.Workers == 0 {
					if err := groupDBs.Close("force cleanup"); err != nil {
						log.Printf("Failed to force close group database for '%s': %v", candidate.name, err)
					} else {
						delete(db.groupDBs, candidate.name)
						db.openDBsNum--
						closedCount++
						log.Printf("Force closed idle DB ng: '%s' (age: %v)", candidate.name, candidate.age)
					}
				}
				groupDBs.mux.Unlock()
			}
			db.MainMutex.Unlock()
		}
		log.Printf("Force closed %d databases due to exceeding limit (%d >= %d)", closedCount, db.openDBsNum+closedCount, MaxOpenDatabases)
		return
	}
	db.MainMutex.RUnlock()

	db.MainMutex.Lock()
	// normal idle processing with idle time
	for groupName, groupDBs := range db.groupDBs {
		if groupDBs == nil {
			log.Printf("cleanupIdleGroups Warning: GroupDBs for '%s' is nil, skipping", groupName)
			continue
		}

		// Use a non-blocking check to avoid holding locks too long
		groupDBs.mux.Lock()
		if groupDBs.Workers < 0 {
			log.Printf("Warning: Negative worker count for group '%s': %d", groupName, groupDBs.Workers)
		}
		isIdle := (groupDBs.Workers == 0 && time.Since(groupDBs.Idle) > DBidleTimeOut)
		if isIdle {
			// Mark for closure and remove from active map immediately
			if err := groupDBs.Close("cleanupIdleGroups"); err != nil {
				log.Printf("Failed to close group database for '%s': %v", groupDBs.Newsgroup, err)
				groupDBs.mux.Unlock()
				continue
			}
			//groupsToClose = append(groupsToClose, groupDBs)
			delete(db.groupDBs, groupName)
			db.openDBsNum--
		}
		groupDBs.mux.Unlock()
	}
	db.MainMutex.Unlock()
	/*
		// Second pass: close databases WITHOUT holding the main mutex
		for _, groupDBs := range groupsToClose {
			groupDBs.mux.Lock()
			if groupDBs.Workers > 0 {
				groupDBs.mux.Unlock()
				continue
			}
			log.Printf("Close idle DB ng: '%s'", groupDBs.Newsgroup)

			groupDBs.mux.Unlock()
		}
	*/
}

func (db *Database) removePartialInitializedGroupDB(groupName string) {
	db.MainMutex.Lock()
	db.groupDBs[groupName] = nil
	db.MainMutex.Unlock()
}

// GetGroupDBs returns the three DBs for a specific newsgroup
func (db *Database) GetGroupDBs(groupName string) (*GroupDBs, error) {

	if db.dbconfig == nil {
		log.Printf(("Database configuration is not set, cannot get group DBs for '%s'"), groupName)
		return nil, fmt.Errorf("database configuration is not set")
	}

	db.MainMutex.Lock()
	groupDBs := db.groupDBs[groupName]
	if groupDBs != nil {
		db.MainMutex.Unlock()

		for {
			groupDBs.mux.RLock()
			if groupDBs.state == stateDONE {
				groupDBs.mux.RUnlock()
				groupDBs.IncrementWorkers()
				return groupDBs, nil
			}
			groupDBs.mux.RUnlock()
			time.Sleep(10 * time.Millisecond)
		}
	} else {
		groupDBs = &GroupDBs{
			Newsgroup: groupName,
			DB:        nil,
			Idle:      time.Now(),
		}
		db.groupDBs[groupName] = groupDBs
		db.MainMutex.Unlock()

		groupsHash := GroupHashMap.GroupToHash(groupName)

		//log.Printf("Open DB for newsgroup '%s' hash='%s' db.openDBsNum=%d db.groupDBs=%d", groupName, groupsHash, db.openDBsNum, len(db.groupDBs))

		// Create single database filename
		baseGroupDBdir := filepath.Join(db.dbconfig.DataDir, "/db/"+groupsHash)
		if err := createDirIfNotExists(baseGroupDBdir); err != nil {
			db.removePartialInitializedGroupDB(groupName)
			return nil, fmt.Errorf("failed to create group database directory: %w", err)
		}
		groupDBfile := filepath.Join(baseGroupDBdir + "/" + sanitizeGroupName(groupName) + ".db")

		// Check if database file already exists
		dbExists := fileExists(groupDBfile)

		// Open single database
		groupDB, err := sql.Open("sqlite3", groupDBfile)
		if err != nil {
			db.removePartialInitializedGroupDB(groupName)
			return nil, err
		}

		// Apply pragmas (optimized for existing vs new DBs)
		var pragmaErr error
		if dbExists {
			// Use optimized pragmas for existing DBs (no page_size)
			pragmaErr = db.applySQLitePragmasGroupDB(groupDB)
		}
		if pragmaErr != nil {
			if cerr := groupDB.Close(); cerr != nil {
				log.Printf("Failed to close groupDB during pragma error: %v", cerr)
			}
			db.removePartialInitializedGroupDB(groupName)
			return nil, pragmaErr
		}

		groupDBs.mux.Lock()
		groupDBs.Idle = time.Now()
		groupDBs.DB = groupDB
		groupDBs.mux.Unlock()

		// Apply schemas using the new migration system instead of direct file application
		// Apply all migrations to ensure schema is up to date
		if err := db.migrateGroupDB(groupDBs); err != nil {
			if cerr := groupDB.Close(); cerr != nil {
				log.Printf("Failed to close groupDB during migration error: %v", cerr)
			}
			db.removePartialInitializedGroupDB(groupName)
			return nil, fmt.Errorf("failed to migrate group database %s: %w", groupName, err)
		}

		groupDBs.IncrementWorkers()

		db.MainMutex.Lock()
		db.openDBsNum++
		db.MainMutex.Unlock()

		groupDBs.mux.Lock()
		groupDBs.state = stateDONE
		groupDBs.mux.Unlock()

		return groupDBs, nil
	}
}

// Close closes all database connections
func (db *Database) Shutdown() error {
	var errs []error

	// STEP 1: Mark shutdown as in progress
	if err := db.SetShutdownState(ShutdownStateInProgress); err != nil {
		log.Printf("[DATABASE] Warning: Failed to set shutdown state: %v", err)
		// Continue with shutdown even if we can't update the state
	}

	// STEP 2: Close per-group databases first (thousands of them)
	log.Printf("[DATABASE] Closing %d group databases...", len(db.groupDBs))
	groupCloseErrors := 0
	db.MainMutex.RLock()
	for groupName, groupDBs := range db.groupDBs {
		if groupDBs != nil && groupDBs.DB != nil {
			if err := groupDBs.DB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("failed to close group database %s: %w", groupName, err))
				groupCloseErrors++
			}
		}
	}
	db.MainMutex.RUnlock()
	if groupCloseErrors > 0 {
		log.Printf("[DATABASE] Failed to close %d group databases", groupCloseErrors)
	}

	// Clear the group databases map
	db.MainMutex.Lock()
	db.groupDBs = make(map[string]*GroupDBs)
	db.MainMutex.Unlock()
	log.Printf("[DATABASE] Group databases closed")

	// STEP 4: Mark shutdown as clean BEFORE closing main database
	if err := db.SetShutdownState(ShutdownStateClean); err != nil {
		log.Printf("[DATABASE] Warning: Failed to mark shutdown as clean: %v", err)
		// Continue anyway
	}

	// STEP 5: Close main database last
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
	GroupDBs map[string]struct {
		OpenConnections int
		IdleConnections int
		WaitCount       int64
		WaitDuration    time.Duration
	}
}

// GetStats returns database connection statistics
func (db *Database) GetStats() *Stats {
	stats := &Stats{
		GroupDBs: make(map[string]struct {
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
	for groupName, groupDBs := range db.groupDBs {
		if groupDBs != nil && groupDBs.DB != nil {
			dbStats := groupDBs.DB.Stats()
			stats.GroupDBs[groupName] = struct {
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
	}

	return stats
}

// GetHistoryUseShortHashLen retrieves the UseShortHashLen setting from the database
// Returns the stored value, or the provided default if not found
func (db *Database) GetHistoryUseShortHashLen(defaultValue int) (int, bool, error) {
	var value string
	var locked string

	// Get the UseShortHashLen value
	err := retryableQueryRowScan(db.mainDB, "SELECT value FROM config WHERE key = ?", []interface{}{"history_use_short_hash_len"}, &value)
	if err != nil {
		if err == sql.ErrNoRows {
			// Not found, use default
			return defaultValue, false, nil
		}
		return 0, false, fmt.Errorf("failed to query history_use_short_hash_len: %w", err)
	}

	// Check if config is locked
	err = retryableQueryRowScan(db.mainDB, "SELECT value FROM config WHERE key = ?", []interface{}{"history_config_locked"}, &locked)
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
	_, err = retryableExec(db.mainDB, "INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)",
		"history_use_short_hash_len", fmt.Sprintf("%d", value))
	if err != nil {
		return fmt.Errorf("failed to store history_use_short_hash_len: %w", err)
	}

	// Lock the configuration to prevent future changes
	_, err = retryableExec(db.mainDB, "INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)",
		"history_config_locked", "true")
	if err != nil {
		return fmt.Errorf("failed to lock history configuration: %w", err)
	}

	log.Printf("History UseShortHashLen set to %d and locked", value)
	return nil
}

// Shutdown state constants
const (
	ShutdownStateRunning    = "running"
	ShutdownStateInProgress = "shutting_down"
	ShutdownStateClean      = "clean_shutdown"
	ShutdownStateCrashed    = "crashed"
)

// SetShutdownState updates the shutdown state in the database
func (db *Database) SetShutdownState(state string) error {
	if db.mainDB == nil {
		return fmt.Errorf("main database not initialized")
	}

	var query string
	var args []interface{}

	switch state {
	case ShutdownStateInProgress:
		query = `UPDATE system_status SET shutdown_state = ?, shutdown_started_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = 1`
		args = []interface{}{state}
	case ShutdownStateClean:
		query = `UPDATE system_status SET shutdown_state = ?, shutdown_completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = 1`
		args = []interface{}{state}
	default:
		query = `UPDATE system_status SET shutdown_state = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`
		args = []interface{}{state}
	}

	_, err := retryableExec(db.mainDB, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update shutdown state to %s: %w", state, err)
	}

	log.Printf("[DATABASE] Shutdown state updated to: %s", state)
	return nil
}

// GetShutdownState retrieves the current shutdown state from the database
func (db *Database) GetShutdownState() (string, error) {
	if db.mainDB == nil {
		return ShutdownStateCrashed, fmt.Errorf("main database not initialized")
	}

	var state string
	err := retryableQueryRowScan(db.mainDB, "SELECT shutdown_state FROM system_status WHERE id = 1", []interface{}{}, &state)
	if err != nil {
		return ShutdownStateCrashed, fmt.Errorf("failed to get shutdown state: %w", err)
	}

	return state, nil
}

// InitializeSystemStatus sets up the system status on startup
func (db *Database) InitializeSystemStatus(appVersion string, pid int, hostname string) error {
	if db.mainDB == nil {
		return fmt.Errorf("main database not initialized")
	}

	// Update the system status with current app info and set to running state
	query := `UPDATE system_status SET
		shutdown_state = ?,
		app_version = ?,
		pid = ?,
		hostname = ?,
		shutdown_started_at = NULL,
		shutdown_completed_at = NULL,
		last_heartbeat = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = 1`

	_, err := retryableExec(db.mainDB, query, ShutdownStateRunning, appVersion, pid, hostname)
	if err != nil {
		return fmt.Errorf("failed to initialize system status: %w", err)
	}

	log.Printf("[DATABASE] System status initialized: version=%s, pid=%d, hostname=%s", appVersion, pid, hostname)
	return nil
}

// CheckPreviousShutdown checks if the previous shutdown was clean
func (db *Database) CheckPreviousShutdown() (bool, error) {
	state, err := db.GetShutdownState()
	if err != nil {
		return false, err
	}

	wasClean := (state == ShutdownStateClean)
	if !wasClean {
		log.Printf("[DATABASE] WARNING: Previous shutdown was not clean. State was: %s", state)
	} else {
		log.Printf("[DATABASE] Previous shutdown was clean")
	}

	return wasClean, nil
}

// IsShuttingDown returns true if the database is in the process of shutting down
func (db *Database) IsShuttingDown() bool {
	state, err := db.GetShutdownState()
	if err != nil {
		// If we can't read the state, assume we're shutting down to be safe
		return true
	}
	return state == ShutdownStateInProgress || state == ShutdownStateClean
}

// UpdateHeartbeat updates the last heartbeat timestamp
func (db *Database) UpdateHeartbeat() {
	ticker := time.NewTicker(60 * time.Second)
	for range ticker.C {

		if db.mainDB == nil {
			log.Printf("ERROR UpdateHeartbeat: main database not initialized")
			return
		}

		_, err := retryableExec(db.mainDB, "UPDATE system_status SET last_heartbeat = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = 1")
		if err != nil {
			log.Printf("ERROR UpdateHeartbeat: failed to update heartbeat: %v", err)
			continue
		}
	}
}

// GetNewsgroupID returns the ID of a newsgroup by name
func (db *Database) GetNewsgroupID(groupName string) (int, error) {
	var id int
	err := retryableQueryRowScan(db.mainDB, "SELECT id FROM newsgroups WHERE name = ?", []interface{}{groupName}, &id)
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

	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		log.Printf("DEBUG: Failed to get group databases for %s: %v", groupName, err)
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDBs.Return(db)

	// Update spam counter in group database
	result, err := retryableExec(groupDBs.DB, "UPDATE articles SET spam = spam + 1 WHERE article_num = ?", articleNum)
	if err != nil {
		log.Printf("DEBUG: Failed to update spam count in group DB: %v", err)
		return fmt.Errorf("failed to increment spam count: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	log.Printf("DEBUG: Updated %d rows in articles table for article %d", rowsAffected, articleNum)

	// Add to main database spam table
	result2, err := retryableExec(db.mainDB, "INSERT OR IGNORE INTO spam (newsgroup_id, article_num) VALUES (?, ?)", newsgroupID, articleNum)
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
	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDBs.Return(db)

	_, err = retryableExec(groupDBs.DB, "UPDATE articles SET hide = 1 WHERE article_num = ? AND spam > 0", articleNum)
	if err != nil {
		return fmt.Errorf("failed to increment hide count: %w", err)
	}

	return nil
}

// UnHideArticle sets the hide counter to zero for a specific article
func (db *Database) UnHideArticle(groupName string, articleNum int64) error {
	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDBs.Return(db)

	_, err = retryableExec(groupDBs.DB, "UPDATE articles SET hide = 0 WHERE article_num = ?", articleNum)
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

	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		log.Printf("DEBUG: Failed to get group databases for %s: %v", groupName, err)
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDBs.Return(db)

	// Check current spam count first
	var currentSpam int
	err = retryableQueryRowScan(groupDBs.DB, "SELECT spam FROM articles WHERE article_num = ?", []interface{}{articleNum}, &currentSpam)
	if err != nil {
		log.Printf("DEBUG: Failed to get current spam count: %v", err)
		return fmt.Errorf("failed to get current spam count: %w", err)
	}

	if currentSpam <= 0 {
		log.Printf("DEBUG: Article %d already has spam count of %d, cannot decrement", articleNum, currentSpam)
		return fmt.Errorf("article spam count is already %d, cannot decrement below 0", currentSpam)
	}

	// Decrement spam counter in group database
	result, err := retryableExec(groupDBs.DB, "UPDATE articles SET spam = spam - 1 WHERE article_num = ? AND spam > 0", articleNum)
	if err != nil {
		log.Printf("DEBUG: Failed to decrement spam count in group DB: %v", err)
		return fmt.Errorf("failed to decrement spam count: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	log.Printf("DEBUG: Decremented spam count for %d rows in articles table for article %d", rowsAffected, articleNum)

	// If spam count reaches 0, remove from main database spam table
	if currentSpam == 1 {
		result2, err := retryableExec(db.mainDB, "DELETE FROM spam WHERE newsgroup_id = ? AND article_num = ?", newsgroupID, articleNum)
		if err != nil {
			log.Printf("DEBUG: Failed to remove from spam table: %v", err)
			return fmt.Errorf("failed to remove from spam table: %w", err)
		}

		rowsAffected2, _ := result2.RowsAffected()
		log.Printf("DEBUG: Removed %d rows from spam table", rowsAffected2)
	}

	return nil
}

// HasUserFlaggedSpam checks if a user has already flagged a specific article as spam
func (db *Database) HasUserFlaggedSpam(userID int, groupName string, articleNum int64) (bool, error) {
	// Get newsgroup ID
	newsgroupID, err := db.GetNewsgroupID(groupName)
	if err != nil {
		return false, fmt.Errorf("failed to get newsgroup ID: %w", err)
	}

	var count int
	err = retryableQueryRowScan(db.mainDB, `
		SELECT COUNT(*) FROM user_spam_flags
		WHERE user_id = ? AND newsgroup_id = ? AND article_num = ?`,
		[]interface{}{userID, newsgroupID, articleNum}, &count)

	if err != nil {
		return false, fmt.Errorf("failed to check user spam flag: %w", err)
	}

	return count > 0, nil
}

// RecordUserSpamFlag records that a user has flagged an article as spam
func (db *Database) RecordUserSpamFlag(userID int, groupName string, articleNum int64) error {
	// Get newsgroup ID
	newsgroupID, err := db.GetNewsgroupID(groupName)
	if err != nil {
		return fmt.Errorf("failed to get newsgroup ID: %w", err)
	}

	_, err = retryableExec(db.mainDB, `
		INSERT OR IGNORE INTO user_spam_flags (user_id, newsgroup_id, article_num)
		VALUES (?, ?, ?)`,
		userID, newsgroupID, articleNum)

	if err != nil {
		return fmt.Errorf("failed to record user spam flag: %w", err)
	}

	return nil
}
