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

// GroupDBs holds a single database connection for a group
type GroupDBs struct {
	state        int64 // 0 = not initialized, 1 = initialized
	mux          sync.RWMutex
	Newsgroup    string    // Name of the newsgroup TODO: remove and use ptr below
	NewsgroupPtr *string   // pointer to the newsgroup
	Idle         time.Time // Last time this group was used
	Workers      int64     // how many are working with this DB
	DB           *sql.DB   // Single database containing articles, overview, threads, etc.
}

// GetGroupDBs returns groupDB for a specific newsgroup
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
			if groupDBs.state == stateCREATED {
				groupDBs.mux.RUnlock()
				groupDBs.IncrementWorkers()
				return groupDBs, nil
			}
			groupDBs.mux.RUnlock()
			time.Sleep(10 * time.Millisecond)
		}
	} else {
		groupDBs = &GroupDBs{
			Newsgroup:    groupName,
			NewsgroupPtr: db.Batch.GetNewsgroupPointer(groupName),
			DB:           nil,
			Idle:         time.Now(),
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
		groupDBfile := filepath.Join(baseGroupDBdir + "/" + SanitizeGroupName(groupName) + ".db")

		// Check if database file already exists
		dbExists := FileExists(groupDBfile)

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
		groupDBs.state = stateCREATED
		groupDBs.mux.Unlock()

		return groupDBs, nil
	}
}

func (db *Database) ForceCloseGroupDBs(groupsDB *GroupDBs) error {
	if db.dbconfig == nil {
		log.Printf(("Database configuration is not set, cannot get group DBs for '%s'"), groupsDB.Newsgroup)
		return fmt.Errorf("database configuration is not set")
	}
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()
	groupsDB.mux.Lock()
	if groupsDB.Workers < 1 {
		groupsDB.mux.Unlock()
		return fmt.Errorf("error in ForceCloseGroupDBs: workers <= 0")
	}
	groupsDB.Workers--
	if groupsDB.Workers > 0 {
		groupsDB.mux.Unlock()
		return nil
	}
	if err := groupsDB.Close("ForceCloseGroupDBs"); err != nil {
		groupsDB.mux.Unlock()
		return fmt.Errorf("error ForceCloseGroupDBs groupsDB.Close ng:'%s' err='%v'", groupsDB.Newsgroup, err)
	}
	groupsDB.mux.Unlock()
	db.openDBsNum--
	delete(db.groupDBs, groupsDB.Newsgroup)
	//log.Printf("ForceCloseGroupDBs: closed group DB for '%s', openDBsNum=%d, groupDBs=%d", groupsDB.Newsgroup, db.openDBsNum, len(db.groupDBs))
	return nil
}

func (dbs *GroupDBs) IncrementWorkers() {
	dbs.mux.Lock()
	dbs.Workers++
	//log.Printf("DEBUG: IncrementWorkers for group '%s': %d", dbs.Newsgroup, dbs.Workers)
	dbs.Idle = time.Now() // Update idle time to now
	dbs.mux.Unlock()
}

func (dbs *GroupDBs) Return(db *Database) {
	if dbs != nil && db != nil {
		dbs.mux.Lock()
		dbs.Idle = time.Now() // Update idle time to now
		dbs.Workers--
		dbs.mux.Unlock()
	} else {
		log.Printf("Warning: Attempted to return a nil db=%#v dbs=%#v", db, dbs)
	}
}

func (db *GroupDBs) ExistsMsgIdInArticlesDB(messageID string) bool {
	query := "SELECT 1 FROM articles WHERE message_id = ? LIMIT 1"
	var exists bool
	if err := retryableQueryRowScan(db.DB, query, []interface{}{messageID}, &exists); err != nil {
		return false
	}
	return exists
}

func (dbs *GroupDBs) Close(who string) error {
	if dbs == nil {
		log.Printf("Warning: Attempted to close nil GroupDBs")
		return fmt.Errorf("nil GroupDBs cannot be closed")
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
