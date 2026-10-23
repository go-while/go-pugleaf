package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ftaExitTimeout bounds the wait for background goroutines in ftaExit.
const ftaExitTimeout = 60 * time.Second

var (
	ftaStopOnce sync.Once
	// ftaExitMu serializes ftaExit: the first caller tears down and exits, a concurrent
	// second one (the signal handler vs. main) waits here until the process is gone.
	ftaExitMu sync.Mutex
	// ftaStopReq is set by the first SIGINT/SIGTERM: main stops after the current newsgroup.
	ftaStopReq atomic.Bool
)

// ftaExitSignaled is the exit code of a run ended by a signal.
const ftaExitSignaled = 130

// ftaExit tears the database down (close StopChan once, wait for the background
// goroutines up to ftaExitTimeout, Shutdown) and exits with code. It is the only way
// out of main once the database is open.
func ftaExit(db *database.Database, code int) {
	ftaExitMu.Lock() // never unlocked: os.Exit below ends the process
	ftaExitLocked(db, code)
}

// ftaExitNow is the second-signal exit: it tears down like ftaExit unless a teardown
// already holds ftaExitMu (it may be waiting on db.WG for up to a minute); then it
// exits at once without waiting for it.
func ftaExitNow(db *database.Database) {
	if !ftaExitMu.TryLock() {
		log.Printf("[FIX-THREAD-ACTIVITY] teardown already running: exiting now")
		os.Exit(ftaExitSignaled)
	}
	ftaExitLocked(db, ftaExitSignaled) // never unlocked: it ends the process
}

// ftaExitLocked is ftaExit for a caller that holds ftaExitMu.
func ftaExitLocked(db *database.Database, code int) {
	if db != nil {
		ftaStopOnce.Do(func() {
			if db.StopChan != nil {
				close(db.StopChan)
			}
		})
		if db.WG != nil {
			done := make(chan struct{})
			go func() {
				db.WG.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(ftaExitTimeout):
				log.Printf("[FIX-THREAD-ACTIVITY] background goroutines did not stop within %v, shutting down anyway", ftaExitTimeout)
			}
		}
		if err := db.Shutdown(); err != nil {
			log.Printf("[FIX-THREAD-ACTIVITY] database shutdown failed: %v", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

// ftaSignalLoop handles the signals of sigChan: the first one calls requestStop (main
// stops after the current newsgroup), a second one calls exitNow. It returns after the
// second signal or when sigChan is closed.
func ftaSignalLoop(sigChan <-chan os.Signal, requestStop, exitNow func()) {
	sig, ok := <-sigChan
	if !ok {
		return
	}
	log.Printf("[FIX-THREAD-ACTIVITY] Received %v: stopping after the current newsgroup (send it again to stop now)", sig)
	requestStop()
	sig, ok = <-sigChan
	if !ok {
		return
	}
	log.Printf("[FIX-THREAD-ACTIVITY] Received %v again: stopping now", sig)
	exitNow()
}

// ftaHandleSignals registers SIGINT/SIGTERM for the run with the open database db;
// both ways out go through ftaExit (exit code ftaExitSignaled).
func ftaHandleSignals(db *database.Database) {
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	// ends with the process: every way out of main is ftaExit (os.Exit)
	go ftaSignalLoop(sigChan, func() { ftaStopReq.Store(true) }, func() { ftaExitNow(db) })
}

// resolveNamedGroups looks every name up in the main database (A-ops-8): GetGroupDB would
// create an empty group database for a name that is not a newsgroup. Unknown names are
// returned in unknown; any other lookup error is returned as err.
func resolveNamedGroups(db *database.Database, names []string) (found []*models.Newsgroup, unknown []string, err error) {
	for _, name := range names {
		ng, err := db.MainDBGetNewsgroup(name)
		if errors.Is(err, sql.ErrNoRows) {
			unknown = append(unknown, name)
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("failed to look up newsgroup '%s': %w", name, err)
		}
		found = append(found, ng)
	}
	return found, unknown, nil
}

func main() {
	var (
		groupName string
		dataDir   string
	)
	flag.StringVar(&groupName, "group", "", "Newsgroup name to fix (empty = fix all groups)")
	flag.StringVar(&dataDir, "data", "./data", "Directory to store database files")
	flag.Parse()

	database.NO_CACHE_BOOT = true // prevents booting caches
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = dataDir

	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	ftaHandleSignals(db)

	if groupName != "" {
		_, unknown, err := resolveNamedGroups(db, []string{groupName})
		if err != nil {
			log.Printf("[FIX-THREAD-ACTIVITY] %v", err)
			ftaExit(db, 1)
		}
		if len(unknown) > 0 {
			log.Printf("[FIX-THREAD-ACTIVITY] unknown newsgroup '%s'", groupName)
			ftaExit(db, 1)
		}
		log.Printf("Fixing thread activity for group: %s", groupName)
		if err := fixGroupThreadActivity(db, groupName); err != nil {
			log.Printf("Failed to fix group %s: %v", groupName, err)
			ftaExit(db, 1)
		}
	} else {
		log.Printf("Fixing thread activity for all groups...")
		groups, err := db.MainDBGetAllNewsgroups()
		if err != nil {
			log.Printf("Failed to get newsgroups: %v", err)
			ftaExit(db, 1)
		}

		fixed := 0
		failed := 0
		for _, group := range groups {
			if ftaStopReq.Load() {
				log.Printf("Stopped by a signal: %d groups fixed, %d errors", fixed, failed)
				ftaExit(db, ftaExitSignaled)
			}
			if group.Name == "" {
				continue
			}
			log.Printf("Fixing group: %s", group.Name)
			if err := fixGroupThreadActivity(db, group.Name); err != nil {
				log.Printf("Error fixing group %s: %v", group.Name, err)
				failed++
			} else {
				fixed++
			}
		}
		log.Printf("Completed: %d groups fixed, %d errors", fixed, failed)
	}
	ftaExit(db, 0)
}

func fixGroupThreadActivity(db *database.Database, groupName string) error {
	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group DB: %w", err)
	}
	defer groupDB.Return()

	// Get only thread cache entries that have future last_activity timestamps
	rows, err := database.RetryableQuery(groupDB.DB, `
		SELECT thread_root, child_articles, last_activity
		FROM thread_cache
		WHERE last_activity > datetime('now', '+25 hour')
		ORDER BY thread_root`)
	if err != nil {
		return fmt.Errorf("failed to query thread cache: %w", err)
	}
	defer rows.Close()

	type threadInfo struct {
		root          int64
		childArticles string
		lastActivity  time.Time
	}

	var threads []threadInfo
	for rows.Next() {
		var t threadInfo
		var lastActivityStr sql.NullString
		if err := rows.Scan(&t.root, &t.childArticles, &lastActivityStr); err != nil {
			return fmt.Errorf("failed to scan thread: %w", err)
		}
		if lastActivityStr.Valid {
			// Try SQLite format first, then RFC3339 format (same as web version)
			if parsed, err := time.Parse("2006-01-02 15:04:05", lastActivityStr.String); err == nil {
				t.lastActivity = parsed
			} else if parsed, err := time.Parse(time.RFC3339, lastActivityStr.String); err == nil {
				t.lastActivity = parsed
			} else {
				log.Printf("Thread %d: unrecognized last_activity format '%s'", t.root, lastActivityStr.String)
			}
		} else {
			log.Printf("Thread %d: last_activity is NULL", t.root)
		}
		threads = append(threads, t)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating threads: %w", err)
	}

	log.Printf("Found %d threads with future last_activity in group %s", len(threads), groupName)

	// Fix each thread
	updatedCount := 0
	for _, thread := range threads {
		// Build list of all articles in this thread
		articleNums := []int64{thread.root}
		if thread.childArticles != "" {
			parts := strings.Split(thread.childArticles, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" {
					var articleNum int64
					if _, err := fmt.Sscanf(part, "%d", &articleNum); err == nil {
						articleNums = append(articleNums, articleNum)
					}
				}
			}
		}

		// Find the most recent non-hidden article in this thread
		var maxDate time.Time
		var found bool

		for _, articleNum := range articleNums {
			var dateSent time.Time
			var hide int
			err := database.RetryableQueryRowScan(groupDB.DB, `
				SELECT date_sent, hide
				FROM articles
				WHERE article_num = ?`, []interface{}{articleNum}, &dateSent, &hide)

			if err != nil {
				continue // Article not found, skip
			}

			// Skip hidden articles and obvious future posts
			if hide != 0 || dateSent.UTC().After(time.Now().UTC().Add(25*time.Hour)) {
				continue
			}

			if !found || dateSent.After(maxDate) {
				maxDate = dateSent
				found = true
			}
		}

		// Update thread cache if we found a valid date
		if found && !maxDate.Equal(thread.lastActivity) {
			// Format as UTC string to avoid timezone encoding issues
			utcTimeStr := maxDate.UTC().Format("2006-01-02 15:04:05")

			_, err := database.RetryableExec(groupDB.DB, `
				UPDATE thread_cache
				SET last_activity = ?
				WHERE thread_root = ?`, utcTimeStr, thread.root)

			if err != nil {
				log.Printf("Failed to update thread %d: %v", thread.root, err)
				continue
			}

			updatedCount++
			if maxDate.UTC().After(time.Now().UTC()) {
				log.Printf("Thread %d: updated %v -> %v (still future!)",
					thread.root, thread.lastActivity.Format("2006-01-02 15:04:05"), maxDate.Format("2006-01-02 15:04:05"))
			}
		}
	}

	log.Printf("Group %s: updated %d/%d threads", groupName, updatedCount, len(threads))
	return nil
}
