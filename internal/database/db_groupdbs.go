package database

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

const MaxOpenDatabases = 256

const stateDONE = 1

// GroupDBs holds a single database connection for a group (contains all tables)
type GroupDBs struct {
	state     int64 // 0 = not initialized, 1 = initialized
	mux       sync.RWMutex
	Newsgroup string    // Name of the newsgroup
	Idle      time.Time // Last time this group was used
	Workers   int64     // how many are working with this DB
	DB        *sql.DB   // Single database containing articles, overview, threads, etc.
}

func (dbs *GroupDBs) IncrementWorkers() {
	dbs.mux.Lock()
	dbs.Workers++
	//log.Printf("DEBUG: IncrementWorkers for group '%s': %d", dbs.Newsgroup, dbs.Workers)
	dbs.Idle = time.Now() // Update idle time to now
	dbs.mux.Unlock()
}

func (dbs *GroupDBs) Return(db *Database) {
	dbs.mux.Lock()
	dbs.Idle = time.Now() // Update idle time to now
	dbs.Workers--
	dbs.mux.Unlock()
	/*
		if dbs.Workers < 0 {
			log.Printf("Warning: Worker count went negative for group '%s'", dbs.Newsgroup)
			return
		}
		//log.Printf("DEBUG: Return for group '%s': %d", dbs.Newsgroup, dbs.Workers)

		// Check if we need to close, but don't do it while holding dbs.mux
		workerCount := dbs.Workers
		ng := dbs.Newsgroup
		dbs.mux.Unlock()

		// Check openDBsNum with proper synchronization
		db.MainMutex.RLock()
		shouldClose := workerCount == 0 && db.openDBsNum >= MaxOpenDatabases // TODO HARDCODED
		db.MainMutex.RUnlock()

		// If we need to close, acquire locks in the same order as GetGroupDBs to prevent deadlock
		if shouldClose {
			db.MainMutex.Lock()
			dbs.mux.Lock()
			// Double-check condition after re-acquiring locks
			if dbs.Workers == 0 && db.openDBsNum >= MaxOpenDatabases { // TODO hardcoded limit
				log.Printf("Closing group databases for '%s' due to no more open workers and high open DBs count (%d)", ng, db.openDBsNum)
				err := dbs.Close() // Close DBs if no workers are left
				if err != nil {
					log.Printf("Failed to close group databases for '%s': %v", ng, err)
				} else {
					db.groupDBs[ng] = nil // Remove from groupDBs map
					delete(db.groupDBs, ng)
					dbs.DB = nil
					db.openDBsNum--
				}
			}
			dbs.mux.Unlock()
			db.MainMutex.Unlock()
		}
	*/
	//dbs = nil
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
	dbs.Newsgroup = ""
	dbs.DB = nil
	return nil
}
