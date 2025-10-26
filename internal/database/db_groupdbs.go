package database

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"
)

const MaxOpenDatabases = 256

const stateCREATED = 1

// GroupDB holds a single database connection for a group
type GroupDB struct {
	state        int64 // 0 = not initialized, 1 = initialized
	mux          sync.RWMutex
	Newsgroup    string    // Name of the newsgroup TODO: remove and use ptr below
	NewsgroupPtr *string   // pointer to the newsgroup
	Idle         time.Time // Last time this group was used
	Workers      int64     // how many are working with this DB
	DB           *sql.DB   // Single database containing articles, overview, threads, etc.
}

// GetGroupDB returns groupDB for a specific newsgroup
func (db *Database) GetGroupDB(groupName string) (*GroupDB, error) {

	if db.dbconfig == nil {
		log.Printf(("Database configuration is not set, cannot get group DBs for '%s'"), groupName)
		return nil, fmt.Errorf("database configuration is not set")
	}

	db.MainMutex.Lock() //mux #d2ef40e0
	groupDB := db.groupDB[groupName]
	if groupDB != nil {
		db.MainMutex.Unlock() //mux #d2ef40e0
		for {
			groupDB.mux.RLock()
			if groupDB.state == stateCREATED {
				groupDB.mux.RUnlock()
				groupDB.IncrementWorkers()
				return groupDB, nil
			}
			groupDB.mux.RUnlock()
			time.Sleep(10 * time.Millisecond)
		}
	} else {
		groupDB = &GroupDB{
			Newsgroup:    groupName,
			NewsgroupPtr: db.Batch.GetNewsgroupPointer(groupName),
			DB:           nil,
			Idle:         time.Now(),
			Workers:      1,
		}
		db.groupDB[groupName] = groupDB
		db.MainMutex.Unlock() //mux #d2ef40e0

		groupsHash := GroupHashMap.GroupToHash(groupName)

		//log.Printf("Open DB for newsgroup '%s' hash='%s' db.openDBsNum=%d db.groupDB=%d", groupName, groupsHash, db.openDBsNum, len(db.groupDB))

		// Create single database filename
		baseGroupDBdir := filepath.Join(db.dbconfig.DataDir, "/db/"+groupsHash)
		if err := createDirIfNotExists(baseGroupDBdir); err != nil {
			db.removePartialInitializedGroupDB(groupName)
			return nil, fmt.Errorf("failed to create group %s database directory: %w", groupName, err)
		}
		groupDBfile := filepath.Join(baseGroupDBdir + "/" + SanitizeGroupName(groupName) + ".db")

		// Check if database file already exists
		dbExists := FileExists(groupDBfile)

		// Open single database
		groupsDB, err := sql.Open("sqlite3", groupDBfile)
		if err != nil {
			db.removePartialInitializedGroupDB(groupName)
			return nil, err
		}

		// Apply pragmas (optimized for existing vs new DBs)
		var pragmaErr error
		if dbExists {
			// Use optimized pragmas for existing DBs (no page_size)
			pragmaErr = db.applySQLitePragmasGroupDB(groupsDB)
		}
		if pragmaErr != nil {
			if cerr := groupsDB.Close(); cerr != nil {
				log.Printf("Failed to close groupsDB %s during pragma error: %v", groupName, cerr)
			}
			db.removePartialInitializedGroupDB(groupName)
			return nil, pragmaErr
		}

		groupDB.mux.Lock()
		groupDB.Idle = time.Now()
		groupDB.DB = groupsDB
		groupDB.mux.Unlock()

		// Apply schemas using the new migration system instead of direct file application
		// Apply all migrations to ensure schema is up to date
		if err := db.migrateGroupDB(groupDB); err != nil {
			if cerr := groupsDB.Close(); cerr != nil {
				log.Printf("Failed to close groupsDB %s during migration error: %v", groupName, cerr)
			}
			db.removePartialInitializedGroupDB(groupName)
			return nil, fmt.Errorf("failed to migrate group database %s: %w", groupName, err)
		}

		db.MainMutex.Lock()
		db.openDBsNum++
		db.MainMutex.Unlock()

		groupDB.mux.Lock()
		groupDB.state = stateCREATED
		groupDB.mux.Unlock()

		return groupDB, nil
	}
}

func (db *Database) ForceCloseGroupDB(groupsDB *GroupDB) error {
	if db.dbconfig == nil {
		log.Printf(("Database configuration is not set, cannot get group DBs for '%s'"), groupsDB.Newsgroup)
		return fmt.Errorf("database configuration is not set")
	}
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()
	groupsDB.mux.Lock()
	if groupsDB.Workers < 1 {
		groupsDB.mux.Unlock()
		return fmt.Errorf("error in ForceCloseGroupDB: workers <= 0")
	}
	groupsDB.Workers--
	if groupsDB.Workers > 0 {
		groupsDB.mux.Unlock()
		return nil
	}
	if err := groupsDB.Close("ForceCloseGroupDB"); err != nil {
		groupsDB.mux.Unlock()
		return fmt.Errorf("error ForceCloseGroupDB groupsDB.Close ng:'%s' err='%v'", groupsDB.Newsgroup, err)
	}
	groupsDB.mux.Unlock()
	db.openDBsNum--
	delete(db.groupDB, groupsDB.Newsgroup)
	//log.Printf("ForceCloseGroupDB: closed group DB for '%s', openDBsNum=%d, groupDB=%d", groupsDB.Newsgroup, db.openDBsNum, len(db.groupDB))
	return nil
}

func (dbs *GroupDB) IncrementWorkers() {
	dbs.mux.Lock()
	dbs.Workers++
	//log.Printf("DEBUG: IncrementWorkers for group '%s': %d", dbs.Newsgroup, dbs.Workers)
	dbs.Idle = time.Now() // Update idle time to now
	dbs.mux.Unlock()
}

func (dbs *GroupDB) Return() {
	if dbs != nil && dbs.DB != nil {
		dbs.mux.Lock()
		dbs.Idle = time.Now() // Update idle time to now
		dbs.Workers--
		dbs.mux.Unlock()
	} else {
		log.Printf("Warning: Attempted to return a nil db=%#v dbs=%#v", dbs.DB, dbs)
	}
}

func (db *GroupDB) ExistsMsgIdInArticlesDB(messageID string) bool {
	query := "SELECT 1 FROM articles WHERE message_id = ? LIMIT 1"
	var exists bool
	if err := RetryableQueryRowScan(db.DB, query, []interface{}{messageID}, &exists); err != nil {
		return false
	}
	return exists
}

func (dbs *GroupDB) Close(who string) error {
	if dbs == nil {
		log.Printf("Warning: Attempted to close nil GroupDB")
		return fmt.Errorf("nil GroupDB cannot be closed")
	}
	if dbs.DB != nil {
		if err := dbs.DB.Close(); err != nil {
			return fmt.Errorf("group DB: %w", err)
		}
	} else {
		return fmt.Errorf("group DB already closed")
	}
	return nil
}

// GetGroupDBWithSuffix opens a group database with a custom suffix (e.g., ".new")
// Returns the database connection, the full file path, and any error
func (db *Database) GetGroupDBWithSuffix(groupName, suffix string) (*sql.DB, string, error) {
	if db.dbconfig == nil {
		return nil, "", fmt.Errorf("database configuration is not set")
	}

	groupsHash := GroupHashMap.GroupToHash(groupName)
	baseGroupDBdir := filepath.Join(db.dbconfig.DataDir, "/db/"+groupsHash)

	if err := createDirIfNotExists(baseGroupDBdir); err != nil {
		return nil, "", fmt.Errorf("failed to create group database directory: %w", err)
	}

	groupDBfile := filepath.Join(baseGroupDBdir + "/" + SanitizeGroupName(groupName) + ".db" + suffix)

	// Open database
	groupDB, err := sql.Open("sqlite3", groupDBfile)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open database: %w", err)
	}

	// Apply pragmas for new database
	if err := db.applySQLitePragmasGroupDB(groupDB); err != nil {
		if cerr := groupDB.Close(); cerr != nil {
			log.Printf("Failed to close groupDB during pragma error: %v", cerr)
		}
		return nil, "", fmt.Errorf("failed to apply pragmas: %w", err)
	}

	// Apply schema/migrations
	tempGroupDB := &GroupDB{
		Newsgroup: groupName,
		DB:        groupDB,
		Idle:      time.Now(),
	}

	if err := db.migrateGroupDB(tempGroupDB); err != nil {
		if cerr := groupDB.Close(); cerr != nil {
			log.Printf("Failed to close groupDB during migration error: %v", cerr)
		}
		return nil, "", fmt.Errorf("failed to migrate group database: %w", err)
	}

	return groupDB, groupDBfile, nil
}

// CloseGroupDBDirectly closes a database connection directly
func CloseGroupDBDirectly(db *sql.DB) error {
	if db == nil {
		return nil
	}
	return db.Close()
}
