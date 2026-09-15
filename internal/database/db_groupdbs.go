package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"
)

const MaxOpenDatabases = 256

// GroupDB states. A GroupDB starts in state 0 (init) while its creator opens and
// migrates the database.
//
// Locking: Database.MainMutex is always taken before GroupDB.mux, never the other way.
// Workers are only added while holding MainMutex (read or write) and GroupDB.mux with
// the state stateCREATED. Every state change away from init/CREATED (FAILED, CLOSED)
// and the matching delete from Database.groupDB happen while holding MainMutex.Lock and
// GroupDB.mux, so an entry found in the map under MainMutex.RLock is never CLOSED or
// FAILED-and-removed before its worker is counted. The *sql.DB is closed outside the locks.
const (
	stateCREATED = 1 // open and usable
	stateFAILED  = 2 // initialization failed, entry is removed from the map
	stateCLOSED  = 3 // closed (idle cleanup, ForceCloseGroupDB, Shutdown)
)

var (
	errGroupDBClosed     = errors.New("group database is closed")
	errGroupDBInitFailed = errors.New("group database initialization failed")
)

// GroupDB holds a single database connection for a group
type GroupDB struct {
	state        int64 // 0 = init, 1 = CREATED, 2 = FAILED, 3 = CLOSED (guarded by mux)
	mux          sync.RWMutex
	Newsgroup    string    // Name of the newsgroup TODO: remove and use ptr below
	NewsgroupPtr *string   // pointer to the newsgroup
	Idle         time.Time // Last time this group was used
	Workers      int64     // how many are working with this DB
	DB           *sql.DB   // Single database containing articles, overview, threads, etc.
}

// NewsgroupDBsIDcache cache maps newsgroup IDs to names
var NewsgroupDBsIDcache = &NewsgroupDBsIDcacheStruct{
	cache: make(map[int64]string),
}

type NewsgroupDBsIDcacheStruct struct {
	mux   sync.RWMutex
	cache map[int64]string // map[newsgroupID]newsgroupName
}

func (c *NewsgroupDBsIDcacheStruct) GetNewsgroupNameByID(newsgroupID int64, db *Database) (ngname string, exists bool) {
	c.mux.RLock()
	ngname, exists = c.cache[newsgroupID]
	c.mux.RUnlock()
	if !exists {
		ng, err := db.MainDBGetNewsgroupByID(newsgroupID)
		if err != nil {
			log.Printf("GetNewsgroupNameByID: failed to get newsgroup name for ID %d: %v", newsgroupID, err)
			return
		}
		c.mux.Lock()
		c.cache[newsgroupID] = ng.Name
		c.mux.Unlock()
		ngname = ng.Name
		exists = true
	}
	return ngname, exists
}

func (c *NewsgroupDBsIDcacheStruct) SetNewsgroupNameByID(newsgroupID int64, newsgroupName string) {
	c.mux.Lock()
	c.cache[newsgroupID] = newsgroupName
	c.mux.Unlock()
}

func (db *Database) GetAnyNewsgroupDBfromIDs(newsgroupIDs []int64) (*GroupDB, error) {
	for _, ngID := range newsgroupIDs {
		if ngName, exists := NewsgroupDBsIDcache.GetNewsgroupNameByID(ngID, db); exists {
			return db.GetGroupDB(ngName)
		}
	}
	return nil, fmt.Errorf("failed to get any newsgroup DB for IDs: %v", newsgroupIDs)
}

func (db *Database) GetNewsgroupsDBbyID(newsgroupID int64) (*GroupDB, error) {
	if ngName, exists := NewsgroupDBsIDcache.GetNewsgroupNameByID(newsgroupID, db); exists {
		return db.GetGroupDB(ngName)
	}
	return nil, fmt.Errorf("failed to get newsgroup DB for ID: %d", newsgroupID)
}

// acquire adds a worker when the group database is CREATED and returns the state it
// saw and whether a worker was added. The caller must hold db.MainMutex (read or
// write lock) and must have found dbs in db.groupDB under that lock; this makes the
// check and the increment atomic with respect to every closer.
func (dbs *GroupDB) acquire() (state int64, ok bool) {
	dbs.mux.Lock()
	defer dbs.mux.Unlock()
	if dbs.state == stateCREATED && dbs.DB != nil {
		dbs.Workers++
		dbs.Idle = time.Now()
		return dbs.state, true
	}
	return dbs.state, false
}

// GetGroupDB returns groupDB for a specific newsgroup
func (db *Database) GetGroupDB(groupName string) (*GroupDB, error) {

	if db.dbconfig == nil {
		log.Printf(("Database configuration is not set, cannot get group DBs for '%s'"), groupName)
		return nil, fmt.Errorf("database configuration is not set")
	}

	deadline := time.Now().Add(60 * time.Second)
	var waitingOn *GroupDB // entry seen in init on the previous iteration
	for {
		if waitingOn != nil {
			// the entry we waited for failed to initialize: don't retry the init ourselves
			waitingOn.mux.RLock()
			failed := waitingOn.state == stateFAILED
			waitingOn.mux.RUnlock()
			if failed {
				return nil, fmt.Errorf("failed to get group database %s: %w", groupName, errGroupDBInitFailed)
			}
			waitingOn = nil
		}
		// fast path: existing entry, acquired while still holding the read lock
		db.MainMutex.RLock()
		if db.groupDBsShutdown {
			db.MainMutex.RUnlock()
			return nil, fmt.Errorf("failed to get group database %s: %w", groupName, errGroupDBClosed)
		}
		groupDB := db.groupDB[groupName]
		var state int64
		var ok bool
		if groupDB != nil {
			state, ok = groupDB.acquire()
		}
		db.MainMutex.RUnlock()

		if groupDB == nil {
			// slow path: create the entry unless someone else did meanwhile
			db.MainMutex.Lock() //mux #d2ef40e0
			if db.groupDB[groupName] == nil && !db.groupDBsShutdown {
				groupDB = &GroupDB{
					Newsgroup:    groupName,
					NewsgroupPtr: db.Batch.GetNewsgroupPointer(groupName),
					DB:           nil,
					Idle:         time.Now(),
					Workers:      1,
				}
				db.groupDB[groupName] = groupDB
				db.MainMutex.Unlock() //mux #d2ef40e0
				if err := db.initGroupDB(groupDB); err != nil {
					return nil, err
				}
				return groupDB, nil
			}
			db.MainMutex.Unlock() //mux #d2ef40e0
			// created by someone else or shut down: look it up again
			continue
		}

		if ok {
			return groupDB, nil
		}
		if state == stateFAILED {
			return nil, fmt.Errorf("failed to get group database %s: %w", groupName, errGroupDBInitFailed)
		}
		// init (or, defensively, CLOSED): wait and look it up again
		if time.Now().After(deadline) {
			if state == stateCLOSED {
				return nil, fmt.Errorf("failed to get group database %s: %w", groupName, errGroupDBClosed)
			}
			return nil, fmt.Errorf("timeout waiting for group database '%s' initialization", groupName)
		}
		waitingOn = groupDB
		time.Sleep(10 * time.Millisecond)
	}
}

// initGroupDB opens and migrates the database of a new map entry created by GetGroupDB.
// On failure the entry is marked FAILED (waiters return an error) and removed from the map.
func (db *Database) initGroupDB(groupDB *GroupDB) error {
	groupName := groupDB.Newsgroup
	var groupsDB *sql.DB

	fail := func(err error) error {
		db.MainMutex.Lock()
		groupDB.mux.Lock()
		if groupDB.state == 0 {
			groupDB.state = stateFAILED
		}
		groupDB.DB = nil
		if db.groupDB[groupName] == groupDB {
			delete(db.groupDB, groupName)
		}
		groupDB.mux.Unlock()
		db.MainMutex.Unlock()
		if groupsDB != nil {
			if cerr := groupsDB.Close(); cerr != nil {
				log.Printf("[DATABASE] Failed to close groupsDB %s after init error: %v", groupName, cerr)
			}
		}
		return err
	}

	groupsHash := GroupHashMap.GroupToHash(groupName)

	//log.Printf("Open DB for newsgroup '%s' hash='%s' db.openDBsNum=%d db.groupDB=%d", groupName, groupsHash, db.openDBsNum, len(db.groupDB))

	// Create single database filename
	baseGroupDBdir := filepath.Join(db.dbconfig.DataDir, "/db/"+groupsHash)
	if err := createDirIfNotExists(baseGroupDBdir); err != nil {
		return fail(fmt.Errorf("failed to create group %s database directory: %w", groupName, err))
	}
	groupDBfile := filepath.Join(baseGroupDBdir + "/" + SanitizeGroupName(groupName) + ".db")

	// Open single database; pragmas run on every connection via the driver's ConnectHook
	db.ensureGroupConnPragmas()
	var err error
	groupsDB, err = sql.Open(driverNameGroup, groupDBfile)
	if err != nil {
		return fail(fmt.Errorf("failed to open group database %s: %w", groupName, err))
	}
	groupsDB.SetMaxIdleConns(2)
	groupsDB.SetConnMaxIdleTime(5 * time.Minute)

	groupDB.mux.Lock()
	groupDB.Idle = time.Now()
	groupDB.DB = groupsDB
	groupDB.mux.Unlock()

	// Apply schemas using the new migration system instead of direct file application
	// Apply all migrations to ensure schema is up to date
	if err := db.migrateGroupDB(groupDB, true); err != nil {
		return fail(fmt.Errorf("failed to migrate group database %s: %w", groupName, err))
	}

	db.MainMutex.Lock()
	groupDB.mux.Lock()
	if groupDB.state != 0 || db.groupDB[groupName] != groupDB {
		// Shutdown closed the entry while it was initializing (Shutdown leaves the
		// *sql.DB of an entry in init to its creator)
		groupDB.DB = nil
		groupDB.mux.Unlock()
		db.MainMutex.Unlock()
		if cerr := groupsDB.Close(); cerr != nil {
			log.Printf("[DATABASE] Failed to close groupsDB %s after shutdown during init: %v", groupName, cerr)
		}
		return fmt.Errorf("failed to get group database %s: %w", groupName, errGroupDBClosed)
	}
	groupDB.state = stateCREATED
	groupDB.Idle = time.Now()
	db.openDBsNum++
	groupDB.mux.Unlock()
	db.MainMutex.Unlock()

	return nil
}

// ForceCloseGroupDB returns a worker and closes the group database when it was the last one.
func (db *Database) ForceCloseGroupDB(groupsDB *GroupDB) error {
	if groupsDB == nil {
		return fmt.Errorf("error in ForceCloseGroupDB: nil GroupDB")
	}
	if db.dbconfig == nil {
		log.Printf(("Database configuration is not set, cannot get group DBs for '%s'"), groupsDB.Newsgroup)
		return fmt.Errorf("database configuration is not set")
	}
	db.MainMutex.Lock()
	groupsDB.mux.Lock()
	if groupsDB.Workers < 1 {
		groupsDB.mux.Unlock()
		db.MainMutex.Unlock()
		return fmt.Errorf("error in ForceCloseGroupDB: workers <= 0")
	}
	groupsDB.Workers--
	if groupsDB.Workers > 0 || groupsDB.state != stateCREATED {
		groupsDB.mux.Unlock()
		db.MainMutex.Unlock()
		return nil
	}
	groupsDB.state = stateCLOSED
	toClose := groupsDB.DB
	groupsDB.mux.Unlock()
	if db.groupDB[groupsDB.Newsgroup] == groupsDB {
		delete(db.groupDB, groupsDB.Newsgroup)
		db.openDBsNum--
	}
	db.MainMutex.Unlock()

	// close outside the locks
	if toClose == nil {
		return fmt.Errorf("error ForceCloseGroupDB groupsDB.Close ng:'%s' err='group DB already closed'", groupsDB.Newsgroup)
	}
	if err := toClose.Close(); err != nil {
		return fmt.Errorf("error ForceCloseGroupDB groupsDB.Close ng:'%s' err='%v'", groupsDB.Newsgroup, err)
	}
	//log.Printf("ForceCloseGroupDB: closed group DB for '%s'", groupsDB.Newsgroup)
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
	if dbs == nil {
		log.Printf("Warning: Attempted to return a nil GroupDB")
		return
	}
	dbs.mux.Lock()
	dbs.Idle = time.Now() // Update idle time to now
	dbs.Workers--
	if dbs.Workers < 0 {
		log.Printf("Warning: Return() made the worker count negative for group '%s': %d", dbs.Newsgroup, dbs.Workers)
	}
	dbs.mux.Unlock()
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
	baseGroupDBdir := filepath.Join(db.dbconfig.DataDir, "/db."+suffix+"/"+groupsHash)

	if err := createDirIfNotExists(baseGroupDBdir); err != nil {
		return nil, "", fmt.Errorf("failed to create group database directory: %w", err)
	}

	groupDBfile := filepath.Join(baseGroupDBdir + "/" + SanitizeGroupName(groupName) + ".db")

	// Open database; pragmas run on every connection via the driver's ConnectHook
	db.ensureGroupConnPragmas()
	groupDB, err := sql.Open(driverNameGroup, groupDBfile)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open database: %w", err)
	}
	groupDB.SetMaxIdleConns(2)
	groupDB.SetConnMaxIdleTime(5 * time.Minute)

	// Apply schema/migrations
	tempGroupDB := &GroupDB{
		Newsgroup: groupName,
		DB:        groupDB,
		Idle:      time.Now(),
	}

	if err := db.migrateGroupDB(tempGroupDB, false); err != nil {
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
