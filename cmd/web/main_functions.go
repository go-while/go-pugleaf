package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/preloader"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var testFormats = []string{
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05+07:00",
	"2006-01-02 15:04:05",
}

const query_updateNewsgroupLastActivity = "SELECT id, name FROM newsgroups WHERE message_count > 0"

// updateNewsgroupLastActivity updates newsgroups' updated_at field based on their latest article
func updateNewsgroupLastActivity(db *database.Database, updateNGAConcurrent int) error {
	updatedCount := 0
	totalProcessed := 0
	var mux sync.Mutex
	parChan := make(chan struct{}, updateNGAConcurrent)
	var wg sync.WaitGroup
	var id int
	var name string
	// Get newsgroups
	rows, err := db.GetMainDB().Query(query_updateNewsgroupLastActivity)
	if err != nil {
		return fmt.Errorf("failed to query newsgroups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("error [WEB]: updateNewsgroupLastActivity rows.Scan newsgroup: %v", err)
		}
		wg.Add(1)
		parChan <- struct{}{} // acquire a slot
		go func(id int, name string, mux *sync.Mutex, wg *sync.WaitGroup) {
			defer func(wg *sync.WaitGroup) {
				<-parChan // release slot
				mux.Lock()
				totalProcessed++
				if totalProcessed%1000 == 0 {
					log.Printf("[WEB]: Processed %d newsgroups, updated %d so far", totalProcessed, updatedCount)
				}
				mux.Unlock()
				wg.Done()
			}(wg)
			groupDB, err := db.GetGroupDB(name)
			if err != nil {
				log.Printf("[WEB]: ERROR updateNewsgroupLastActivity GetGroupDB %s: %v", name, err)
				return
			}
			if err := updateNewsGroupActivityValue(db, id, groupDB); err == nil {
				mux.Lock()
				updatedCount++
				mux.Unlock()
			} else {
				log.Printf("[WEB]: ERROR updateNewsGroupActivityValue %s: %v", name, err)
			}
		}(id, name, &mux, &wg)
	}
	// Check for iteration errors
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error updateNewsgroupLastActivity iterating newsgroups: %w", err)
	}
	wg.Wait()
	mux.Lock()
	log.Printf("[WEB]: updateNewsgroupLastActivity completed: processed %d total newsgroups, updated %d", totalProcessed, updatedCount)
	mux.Unlock()
	return nil
}

const query_updateNewsGroupActivityValue1 = "SELECT MAX(date_sent) FROM articles WHERE hide = 0"
const query_updateNewsGroupActivityValue2 = "UPDATE newsgroups SET updated_at = ? WHERE id = ? AND updated_at != ?"

func updateNewsGroupActivityValue(db *database.Database, id int, groupDB *database.GroupDB) error {
	defer db.ForceCloseGroupDB(groupDB)

	var formattedDate string
	var parsedDate time.Time
	var latestDate sql.NullString

	/*
		_, err = database.RetryableExec(groupDB.DB, "UPDATE articles SET spam = 1 WHERE spam = 0 AND hide = 1", nil)
		if err != nil {
			db.ForceCloseGroupDB(groupDB)
			log.Printf("[WEB]: Failed to update spam flags for newsgroup %s: %v", name, err)
			continue
		}
	*/

	// Query the latest article date from the group's articles table (excluding hidden articles)
	rows, err := database.RetryableQuery(groupDB.DB, query_updateNewsGroupActivityValue1, nil, latestDate)
	//groupDB.Return(db) // Always return the database connection
	if err != nil {
		log.Printf("[WEB]: updateNewsgroupLastActivity RetryableQueryRowScan %s: %v", groupDB.Newsgroup, err)
		return err
	}
	defer rows.Close()
	for rows.Next() {
		rows.Scan(&latestDate)
		// Only update if we found a latest date
		if !latestDate.Valid {
			return fmt.Errorf("error updateNewsgroupLastActivity no valid latestDate in ng: '%s'", groupDB.Newsgroup)
		}

		// Parse the date and format it consistently as UTC
		if latestDate.String == "" {
			log.Printf("[WEB]: updateNewsgroupLastActivity empty latestDate.String in ng: '%s'", groupDB.Newsgroup)
			return fmt.Errorf("error updateNewsgroupLastActivity empty latestDate.String in ng: '%s'", groupDB.Newsgroup)
		}
		// Try multiple date formats to handle various edge cases
		for _, format := range testFormats {
			parsedDate, err = time.Parse(format, latestDate.String)
			if err == nil {
				break
			}
		}
		if err != nil {
			log.Printf("[WEB]: updateNewsgroupLastActivity parsing date '%s' for %s: %v", latestDate.String, groupDB.Newsgroup, err)
			return err
		}

		// Format as UTC without timezone info to match db_batch.go format
		formattedDate = parsedDate.UTC().Format("2006-01-02 15:04:05")
		result, err := db.GetMainDB().Exec(query_updateNewsGroupActivityValue2, formattedDate, id, formattedDate)
		if err != nil {
			log.Printf("[WEB]: error updateNewsgroupLastActivity updating newsgroup %s: %v", groupDB.Newsgroup, err)
			return err
		}
		if _, err := result.RowsAffected(); err != nil {
			log.Printf("[WEB]: updateNewsgroupLastActivity: '%s' dateStr=%s formattedDate=%s", groupDB.Newsgroup, latestDate.String, formattedDate)
		}
	}
	return nil
}

const query_hideFuturePosts = "SELECT id, name FROM newsgroups WHERE message_count > 0 AND active = 1"

// hideFuturePosts finds articles posted more than 48 hours in the future and properly marks them as spam
func hideFuturePosts(db *database.Database) error {
	// Calculate the cutoff time (current time + 48 hours)
	cutoffTime := time.Now().Add(48 * time.Hour)

	// First, get all newsgroups from the main database
	rows, err := db.GetMainDB().Query(query_hideFuturePosts)
	if err != nil {
		return fmt.Errorf("failed to query newsgroups: %w", err)
	}
	defer rows.Close()

	updatedArticles := 0
	processedGroups := 0
	skippedGroups := 0

	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			log.Printf("[WEB]: Future posts migration error scanning newsgroup: %v", err)
			continue
		}

		// Get the group database for this newsgroup
		groupDB, err := db.GetGroupDB(name)
		if err != nil {
			log.Printf("[WEB]: Future posts migration error getting group DB for %s: %v", name, err)
			skippedGroups++
			continue
		}

		// Find articles that are posted more than 48 hours in the future and not already hidden
		articleRows, err := groupDB.DB.Query("SELECT article_num FROM articles WHERE date_sent > ? AND hide = 0", cutoffTime.Format("2006-01-02 15:04:05"))
		if err != nil {
			log.Printf("[WEB]: Future posts migration error querying articles for %s: %v", name, err)
			db.ForceCloseGroupDB(groupDB)
			skippedGroups++
			continue
		}

		var futureArticles []int64
		for articleRows.Next() {
			var articleNum int64
			if err := articleRows.Scan(&articleNum); err != nil {
				log.Printf("[WEB]: Future posts migration error scanning article for %s: %v", name, err)
				continue
			}
			futureArticles = append(futureArticles, articleNum)
		}
		articleRows.Close()
		db.ForceCloseGroupDB(groupDB)

		// Process each future article using the proper spam increment system
		groupArticleCount := 0
		for _, articleNum := range futureArticles {
			// Use the proper spam increment function which:
			// 1. Increments spam counter (spam = spam + 1)
			// 2. Adds entry to main spam table for admin tracking
			// 3. Handles proper error logging
			if err := db.IncrementArticleSpam(name, articleNum); err != nil {
				log.Printf("[WEB]: Future posts migration error incrementing spam for %s article %d: %v", name, articleNum, err)
				continue
			}

			// Also set the hide flag for these future-dated articles
			groupDB, err := db.GetGroupDB(name)
			if err != nil {
				log.Printf("[WEB]: Future posts migration error getting group DB for hide update %s: %v", name, err)
				continue
			}
			_, err = database.RetryableExec(groupDB.DB, "UPDATE articles SET hide = 1 WHERE article_num = ?", articleNum)
			db.ForceCloseGroupDB(groupDB)

			if err != nil {
				log.Printf("[WEB]: Future posts migration error setting hide flag for %s article %d: %v", name, articleNum, err)
				continue
			}

			groupArticleCount++
		}

		if groupArticleCount > 0 {
			log.Printf("[WEB]: Processed %d future posts in newsgroup %s (marked as spam and hidden)", groupArticleCount, name)
			updatedArticles += groupArticleCount
		}
		processedGroups++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating newsgroups: %w", err)
	}

	log.Printf("[WEB]: Future posts migration completed: processed %d groups, marked %d articles as spam, skipped %d groups", processedGroups, updatedArticles, skippedGroups)
	log.Printf("[WEB]: Future posts are now properly tracked in spam system and will appear in admin spam management")
	return nil
}

// writeActiveFileFromDB writes an NNTP active file from the main database newsgroups table
func writeActiveFileFromDB(db *database.Database, filePath string, activeOnly bool) error {
	// Query newsgroups from main database with all fields needed for active file
	active := 0
	if activeOnly {
		active = 1
	}
	rows, err := db.GetMainDB().Query(`
		SELECT name, high_water, low_water, status
		FROM newsgroups
		WHERE active = ?
		ORDER BY name
	`, active)
	if err != nil {
		return fmt.Errorf("failed to query newsgroups: %w", err)
	}
	defer rows.Close()

	// Create the output file
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create active file '%s': %w", filePath, err)
	}
	defer file.Close()

	totalGroups := 0
	log.Printf("[WEB]: Writing active file to: %s (writeActiveOnly=%t)", filePath, activeOnly)

	// Write each newsgroup in NNTP active file format: groupname highwater lowwater status
	for rows.Next() {
		var name string
		var highWater, lowWater int64
		var status string

		if err := rows.Scan(&name, &highWater, &lowWater, &status); err != nil {
			log.Printf("[WEB]: Warning: Failed to scan newsgroup row: %v", err)
			continue
		}

		// Validate status field (should be single character)
		if len(status) != 1 {
			log.Printf("[WEB]: Warning: Invalid status '%s' for group '%s', using 'y'", status, name)
			status = "y"
		}

		// Write in NNTP active file format: groupname highwater lowwater status
		line := fmt.Sprintf("%s %d %d %s\n", name, highWater, lowWater, status)
		if _, err := file.WriteString(line); err != nil {
			return fmt.Errorf("failed to write line for group '%s': %w", name, err)
		}

		totalGroups++
		if totalGroups%1000 == 0 {
			log.Printf("[WEB]: Written %d groups to active file...", totalGroups)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating newsgroups: %w", err)
	}

	log.Printf("[WEB]: Successfully wrote %d newsgroups to active file: %s", totalGroups, filePath)
	return nil
}

// rsyncFlagsError returns an error when -rsync-remove-source is used without -force
// (A-ops-11): moving the group databases deletes them from the data directory, so the
// operator has to confirm it. It returns nil when no rsync is requested.
func rsyncFlagsError(newdatadir string, removeSource, force bool) error {
	if newdatadir == "" || !removeSource || force {
		return nil
	}
	return errors.New("-rsync-remove-source deletes the source group databases after the copy: add -force to confirm (stop fetcher, nntp-server and web first)")
}

// errGroupDBBusy is returned by checkpointGroupForCopy when the WAL checkpoint of a
// group database is blocked: another connection (normally another process) uses it.
var errGroupDBBusy = errors.New("group database is busy (open elsewhere?)")

// checkpointGroupForCopy prepares one group database for a file copy (A-ops-11): it
// opens the group, runs PRAGMA wal_checkpoint(TRUNCATE) so the WAL content is in the
// .db file, and closes the group again so this process holds no handle on it. A
// checkpoint that reports busy (a reader or writer on another connection blocked it)
// returns an error wrapping errGroupDBBusy: the group must not be copied. busyWait > 0
// bounds how long the checkpoint waits for those locks; 0 keeps the connection's
// busy_timeout. The checkpoint runs on one pinned connection (busy_timeout is per
// connection) and not through the Retryable* helpers: busy means refuse, not retry.
func checkpointGroupForCopy(db *database.Database, newsgroup string, busyWait time.Duration) (err error) {
	groupDB, err := db.GetGroupDB(newsgroup)
	if err != nil {
		return fmt.Errorf("open group database %s: %w", newsgroup, err)
	}
	defer func() {
		if cerr := db.ForceCloseGroupDB(groupDB); cerr != nil && err == nil {
			err = fmt.Errorf("close group database %s: %w", newsgroup, cerr)
		}
	}()
	ctx := context.Background()
	conn, err := groupDB.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("group database %s: get connection: %w", newsgroup, err)
	}
	defer conn.Close()
	if busyWait > 0 {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", busyWait.Milliseconds())); err != nil {
			return fmt.Errorf("group database %s: set busy_timeout: %w", newsgroup, err)
		}
		defer func() {
			// the connection goes back to the pool when another worker still holds the group
			if _, rerr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", database.SQLITE_busy_timeout)); rerr != nil {
				log.Printf("[RSYNC]: Warning: group database %s: restore busy_timeout: %v", newsgroup, rerr)
			}
		}()
	}
	var busy, walFrames, checkpointed int64
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &walFrames, &checkpointed); err != nil {
		return fmt.Errorf("group database %s: wal_checkpoint: %w", newsgroup, err)
	}
	if busy != 0 {
		return fmt.Errorf("group database %s: wal_checkpoint(TRUNCATE) blocked (wal frames %d, checkpointed %d): %w", newsgroup, walFrames, checkpointed, errGroupDBBusy)
	}
	return nil
}

// rsyncInactiveGroupsToDir copies the databases of all inactive groups to newdatadir/db
// and, with removeSource (which needs force, see rsyncFlagsError), removes the copied
// source files. Each group is checkpointed and closed first (checkpointGroupForCopy); a
// busy group is refused and left in place, the others are still copied, and the run
// then returns an error with the number of refused groups.
func rsyncInactiveGroupsToDir(db *database.Database, newdatadir string, removeSource, force bool) error {
	if newdatadir == "" {
		return errors.New("empty target directory")
	}
	if err := rsyncFlagsError(newdatadir, removeSource, force); err != nil {
		return err
	}

	// check if newdatadir exists
	if _, err := os.Stat(newdatadir); os.IsNotExist(err) {
		if err := os.MkdirAll(newdatadir, 0755); err != nil {
			log.Printf("[WEB]: Warning: Failed to create new data directory '%s': %v", newdatadir, err)
			return err
		}
	}

	rows, err := database.RetryableQuery(db.GetMainDB(), `
		SELECT name
		FROM newsgroups
		WHERE active = 0
		ORDER BY name
	`)
	if err != nil {
		return fmt.Errorf("failed to query newsgroups: %w", err)
	}
	defer rows.Close()
	basedirNew := filepath.Join(newdatadir, "/db/")
	refused := 0
	for rows.Next() {
		var newsgroup string
		if err := rows.Scan(&newsgroup); err != nil {
			log.Printf("[WEB]: Warning: Failed to scan newsgroup row: %v", err)
			return err
		}
		groupsHash := database.MD5Hash(newsgroup)
		baseGroupDBdir := filepath.Join(db.GetDataDir(), "/db/"+groupsHash)
		baseGroupDBdirNew := filepath.Join(basedirNew, groupsHash)
		sanitizedName := database.SanitizeGroupName(newsgroup)
		groupDBfileOld := filepath.Join(baseGroupDBdir + "/" + sanitizedName + ".db")
		if !database.FileExists(groupDBfileOld) {
			log.Printf("[RSYNC]: Group database file does not exist: %s", groupDBfileOld)
			continue
		}
		// fold the WAL into the .db file and close the group; a busy group is refused
		if err := checkpointGroupForCopy(db, newsgroup, 0); err != nil {
			if errors.Is(err, errGroupDBBusy) {
				log.Printf("[RSYNC]: REFUSED %s: %v (stop fetcher, nntp-server and web, then run again)", newsgroup, err)
				refused++
				continue
			}
			return err
		}
		if _, err := os.Stat(baseGroupDBdirNew); os.IsNotExist(err) {
			if err := os.MkdirAll(baseGroupDBdirNew, 0755); err != nil {
				log.Printf("[WEB]: Warning: Failed to create new data directory '%s': %v", newdatadir, err)
				return err
			}
		}
		start := time.Now()
		if err := database.RsyncDIR(baseGroupDBdir, basedirNew, removeSource); err != nil {
			log.Printf("[RSYNC]: Warning: Failed to rsync group database file baseGroupDBdir='%s' to basedirNew='%s' (baseGroupDBdirNew=%s): %v", baseGroupDBdir, basedirNew, baseGroupDBdirNew, err)
			return err
		}
		groupDBfileNew := filepath.Join(baseGroupDBdirNew + "/" + sanitizedName + ".db")
		if !database.FileExists(groupDBfileNew) {
			log.Printf("[RSYNC]: ERROR: new group database file not found: %s", groupDBfileNew)
			return fmt.Errorf("error new group database file not found: %s", groupDBfileNew)
		}
		log.Printf("[RSYNC]: OK %s (%v) '%s' to '%s'", newsgroup, time.Since(start), baseGroupDBdir, baseGroupDBdirNew)
	}
	// Without this the rows of a result set cut short by an I/O or lock error look exactly
	// like a complete one, and a partial group list would be reported as a full rsync.
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating newsgroups: %w", err)
	}
	if refused > 0 {
		return fmt.Errorf("%d inactive group(s) refused: database busy (open elsewhere)", refused)
	}
	return nil
}

// compareActiveFileWithDatabase compares groups from active file with database and shows missing groups
func compareActiveFileWithDatabase(db *database.Database, activeFilePath string, minArticles int64) error {
	log.Printf("[WEB]: Comparing active file '%s' with database (min articles: %d)...", activeFilePath, minArticles)

	// Open and read the active file
	file, err := os.Open(activeFilePath)
	if err != nil {
		return fmt.Errorf("failed to open active file '%s': %w", activeFilePath, err)
	}
	defer file.Close()

	// Get all newsgroups from database for comparison
	dbGroups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return fmt.Errorf("failed to get newsgroups from database: %w", err)
	}

	// Create a map for fast lookup of existing groups
	existingGroups := make(map[string]*models.Newsgroup)
	for _, group := range dbGroups {
		existingGroups[group.Name] = group
	}

	log.Printf("[WEB]: Found %d newsgroups in database for comparison", len(dbGroups))

	// Parse active file and find missing groups
	scanner := bufio.NewScanner(file)
	lineNum := 0
	totalGroupsInFile := 0
	filteredGroups := 0
	missingGroups := 0
	var missingGroupsList []string

	fmt.Printf("\n=== Comparing Active File with Database ===\n")
	fmt.Printf("Active file: %s\n", activeFilePath)
	fmt.Printf("Min articles filter: %d\n", minArticles)
	fmt.Printf("Database groups: %d\n\n", len(dbGroups))

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse active file format: groupname high low status
		fields := strings.Fields(line)
		if len(fields) < 4 {
			log.Printf("[WEB]: Warning: Skipping malformed line %d in active file: %s", lineNum, line)
			continue
		}

		groupName := fields[0]
		highWaterStr := fields[1]
		lowWaterStr := fields[2]
		status := fields[3]

		// Skip if group name is empty
		if groupName == "" {
			continue
		}

		totalGroupsInFile++

		// Parse high/low water marks to calculate article count
		highWater, err := strconv.ParseInt(highWaterStr, 10, 64)
		if err != nil {
			log.Printf("[WEB]: Warning: Invalid high water mark '%s' at line %d for group %s", highWaterStr, lineNum, groupName)
			continue
		}

		lowWater, err := strconv.ParseInt(lowWaterStr, 10, 64)
		if err != nil {
			log.Printf("[WEB]: Warning: Invalid low water mark '%s' at line %d for group %s", lowWaterStr, lineNum, groupName)
			continue
		}

		// Calculate article count (high - low)
		articleCount := highWater - lowWater
		if articleCount < 0 {
			articleCount = 0
		}

		// Apply min articles filter
		if minArticles > 0 && articleCount < minArticles {
			continue
		}

		filteredGroups++

		// Check if group exists in database
		if _, exists := existingGroups[groupName]; !exists {
			missingGroups++
			missingGroupsList = append(missingGroupsList, fmt.Sprintf("%s (articles: %d, high: %d, low: %d, status: %s)",
				groupName, articleCount, highWater, lowWater, status))
		}

		// Log progress every 10000 lines
		if lineNum%10000 == 0 {
			log.Printf("[WEB]: Processed %d lines, found %d missing groups so far...", lineNum, missingGroups)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading active file: %w", err)
	}

	// Print results
	fmt.Printf("=== COMPARISON RESULTS ===\n")
	fmt.Printf("Total groups in active file: %d\n", totalGroupsInFile)
	fmt.Printf("Groups after min articles filter (%d): %d\n", minArticles, filteredGroups)
	fmt.Printf("Groups missing from database: %d\n\n", missingGroups)

	if missingGroups > 0 {
		fmt.Printf("=== MISSING GROUPS ===\n")
		for i, group := range missingGroupsList {
			fmt.Printf("%d. %s\n", i+1, group)
		}
		fmt.Printf("\n")
	} else {
		fmt.Printf("✓ All groups from active file (meeting criteria) are present in database!\n\n")
	}

	log.Printf("[WEB]: Active file comparison completed - %d total, %d filtered, %d missing", totalGroupsInFile, filteredGroups, missingGroups)
	return nil
}

// webShutdownWait bounds how long teardownDB waits for the database background
// tasks (db.WG) before it shuts the database down anyway.
const webShutdownWait = 60 * time.Second

var (
	// webStop is closed at the start of every shutdown (closeWebStop): it stops the
	// cmd/web background loops startHierarchyUpdater and monitorUpdateFile.
	webStop     = make(chan struct{})
	webStopOnce sync.Once
	dbStopOnce  sync.Once
)

// closeWebStop closes webStop once.
func closeWebStop() {
	webStopOnce.Do(func() { close(webStop) })
}

// closeDBStopChan closes db.StopChan once (the normal shutdown and the exit helpers
// both reach it).
func closeDBStopChan(db *database.Database) {
	dbStopOnce.Do(func() { close(db.StopChan) })
}

// teardownDB stops the cmd/web loops (webStop) and the database background tasks
// (db.StopChan), waits for db.WG at most webShutdownWait and shuts the database down.
func teardownDB(db *database.Database) error {
	closeWebStop()
	closeDBStopChan(db)
	log.Printf("[WEB]: Waiting for background tasks to finish...")
	done := make(chan struct{})
	go func() {
		db.WG.Wait()
		close(done)
	}()
	timer := time.NewTimer(webShutdownWait)
	defer timer.Stop()
	select {
	case <-done:
		log.Printf("[WEB]: All background tasks completed, shutting down database...")
	case <-timer.C:
		log.Printf("[WEB]: Background tasks did not stop within %v, shutting down database anyway", webShutdownWait)
	}
	return db.Shutdown()
}

// exitAfterDB is the only way out of cmd/web once the database is open (D5): it tears
// the database down (teardownDB) and then exits with code.
func exitAfterDB(db *database.Database, code int) {
	if err := teardownDB(db); err != nil {
		log.Printf("[WEB]: Failed to shutdown database: %v", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// fatalAfterDB logs like log.Fatalf and exits with code 1 through exitAfterDB.
func fatalAfterDB(db *database.Database, format string, args ...any) {
	log.Printf(format, args...)
	exitAfterDB(db, 1)
}

// maintenanceRequested reports whether the flags ask for a one-shot maintenance mode
// (A-ops-12). Those modes need only the databases: main runs them right after
// OpenDatabase (with NO_CACHE_BOOT) through runMaintenance and exits, without hostname,
// processor, progress DB or history. The descriptions-only import
// (-update-newsgroups-descriptions-file without -import-active) and
// -update-overwrite-newsgroups-descriptions are not one-shot: they continue into the server.
func maintenanceRequested() bool {
	return updateNewsgroupsHideFuture || updateNGAConcurrent > 0 || writeActiveFile != "" ||
		rsyncInactiveGroups != "" || compareActiveFile != "" || disableActiveFile != "" ||
		findOrphanDBs || updateNewsgroupsExpiryFile != "" || importActiveFile != "" || repairWatermarks
}

// runMaintenance runs the one-shot maintenance modes in their historic order and
// returns the exit code; main ends in exitAfterDB with it. The first mode that runs
// decides the result, except -update-newsgroups-hide-futureposts, which continues with
// -update-newsgroups-activity when both are set. Only reached when maintenanceRequested.
func runMaintenance(db *database.Database) int {
	// Run future posts hiding migration first if requested
	if updateNewsgroupsHideFuture {
		log.Printf("[WEB]: Starting future posts hiding migration...")
		if err := hideFuturePosts(db); err != nil {
			log.Printf("[WEB]: Warning: Future posts hiding migration failed: %v", err)
			return 1
		}
		log.Printf("[WEB]: Future posts hiding migration completed successfully")
		if updateNGAConcurrent == 0 {
			return 0
		}
	}

	// Run newsgroup activity migration after hiding future posts if requested
	if updateNGAConcurrent > 0 {
		log.Printf("[WEB]: Starting updateNewsgroupLastActivity")
		if err := updateNewsgroupLastActivity(db, updateNGAConcurrent); err != nil {
			log.Printf("[WEB]: Warning: Newsgroup activity migration failed: %v", err)
			return 1
		}
		log.Printf("[WEB]: Newsgroup activity migration completed successfully")
		return 0
	}

	// Write active file if requested
	if writeActiveFile != "" {
		log.Printf("[WEB]: Writing active file from main database to: %s", writeActiveFile)
		if err := writeActiveFileFromDB(db, writeActiveFile, writeActiveOnly); err != nil {
			log.Printf("[WEB]: Error: Failed to write active file: %v", err)
			return 1
		}
		log.Printf("[WEB]: Active file written successfully")
		return 0
	}

	// rsyncInactiveGroups
	if rsyncInactiveGroups != "" {
		log.Printf("[RSYNC]: inactive groups to: %s", rsyncInactiveGroups)
		if err := rsyncInactiveGroupsToDir(db, rsyncInactiveGroups, rsyncRemoveSource, rsyncForce); err != nil {
			log.Printf("[RSYNC]: Error: Failed to rsync inactive groups: %v", err)
			return 1
		}
		log.Printf("[RSYNC]: Inactive groups synced successfully")
		return 0
	}

	// compareActiveFile
	if compareActiveFile != "" {
		log.Printf("[WEB]: Comparing active file with database: %s (min articles: %d)", compareActiveFile, compareActiveMinArticles)
		if err := compareActiveFileWithDatabase(db, compareActiveFile, compareActiveMinArticles); err != nil {
			log.Printf("[WEB]: Error: Failed to compare active file: %v", err)
			return 1
		}
		log.Printf("[WEB]: Active file comparison completed successfully")
		return 0
	}

	// disableActiveFile
	if disableActiveFile != "" {
		log.Printf("[WEB]: Disabling newsgroups not listed in active file: %s", disableActiveFile)
		if err := disableNewsgroupsNotInActiveFile(db, disableActiveFile, disableActiveOpts{DryRun: disableActiveDryRun}); err != nil {
			log.Printf("[WEB]: Error: Failed to disable newsgroups: %v", err)
			return 1
		}
		log.Printf("[WEB]: Newsgroup disabling completed successfully")
		return 0
	}

	// findOrphanDBs
	if findOrphanDBs {
		log.Printf("[WEB]: Finding orphaned database folders...")
		if err := findOrphanedDatabases(db, dataDir, verbose); err != nil {
			log.Printf("[WEB]: Error: Failed to find orphaned databases: %v", err)
			return 1
		}
		log.Printf("[WEB]: Orphaned database scan completed successfully")
		return 0
	}

	// updateNewsgroupsExpiryFile
	if updateNewsgroupsExpiryFile != "" {
		log.Printf("[WEB]: Updating newsgroup expiry from file: %s", updateNewsgroupsExpiryFile)
		if err := updateNewsgroupsExpiryFromFile(db, updateNewsgroupsExpiryFile); err != nil {
			log.Printf("[WEB]: Error: Failed to update newsgroup expiry: %v", err)
			return 1
		}
		log.Printf("[WEB]: Newsgroup expiry update completed successfully")
		return 0
	}

	ctx := context.Background()

	// Import from an active file (with an optional descriptions file). A descriptions
	// file alone only gets here together with -repair-watermarks: it is imported first,
	// as before, and the repair below ends the run.
	if importActiveFile != "" || importDescFile != "" {
		log.Printf("[WEB]: Importing newsgroups from files...")
		if err := preloader.LoadNewsgroupsFromFiles(ctx, db, importActiveFile, importDescFile, importCreateMissing); err != nil {
			log.Printf("[WEB]: Warning: Failed to import newsgroups: %v", err)
			return 1
		}
		log.Printf("[WEB]: Newsgroups imported successfully")
		if importActiveFile != "" {
			return 0
		}
	}

	if repairWatermarks {
		log.Printf("[WEB]: Repairing corrupted newsgroup watermarks...")
		if err := preloader.RepairNewsgroupWatermarks(ctx, db); err != nil {
			log.Printf("[WEB]: Warning: Failed to repair watermarks: %v", err)
			return 1
		}
		log.Printf("[WEB]: Watermarks repaired successfully")
		return 0
	}
	return 0
}

// resolveUseShortHashLen returns the history UseShortHashLen to use (OPS-4b). The
// setting is legacy (no effect on the message-id history schema) and never fatal for
// its value: a locked stored value wins; otherwise the CLI value is stored (and
// locked), and a CLI value outside 2..7 is stored as 7 with a warning. Only database
// errors are returned.
func resolveUseShortHashLen(db *database.Database, cliValue int) (int, error) {
	stored, isLocked, err := db.GetHistoryUseShortHashLen(cliValue)
	if err != nil {
		return 0, fmt.Errorf("get history configuration: %w", err)
	}
	if isLocked {
		if cliValue != stored {
			log.Printf("[WEB]: WARNING: Command-line UseShortHashLen (%d) differs from locked database value (%d). Using database value.", cliValue, stored)
		} else {
			log.Printf("[WEB]: Using stored history UseShortHashLen: %d", stored)
		}
		return stored, nil
	}
	value := cliValue
	if value < 2 || value > 7 {
		log.Printf("[WEB]: WARNING: -useshorthashlen %d is outside 2..7 (legacy setting, no effect on the message-id history schema): using 7", cliValue)
		value = 7
	}
	if err := db.SetHistoryUseShortHashLen(value); err != nil {
		return 0, fmt.Errorf("set history configuration: %w", err)
	}
	log.Printf("[WEB]: History UseShortHashLen initialized to %d", value)
	return value, nil
}

// startHierarchyUpdater runs a background job every 10 minutes to update
// hierarchy last_updated fields based on their child newsgroups, until stop is closed.
func startHierarchyUpdater(db *database.Database, stop <-chan struct{}) {
	// Run immediately on startup
	if err := db.UpdateHierarchiesLastUpdated(); err != nil {
		log.Printf("[WEB]: Initial hierarchy update failed: %v", err)
	} else {
		// Update the hierarchy cache with new last_updated values
		if db.HierarchyCache != nil {
			if err := db.HierarchyCache.UpdateHierarchyLastUpdated(db); err != nil {
				log.Printf("[WEB]: Initial hierarchy cache update failed: %v", err)
			} else {
				log.Printf("[WEB]: Initial hierarchy cache updated successfully")
			}
		}
	}
	log.Printf("[WEB]: Hierarchy updater started, will sync hierarchy last_updated every 10 minutes")

	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			log.Printf("[WEB]: Hierarchy updater stopped")
			return
		case <-ticker.C:
		}
		if err := db.UpdateHierarchiesLastUpdated(); err != nil {
			log.Printf("[WEB]: Hierarchy update failed: %v", err)
		} else {
			// Update the hierarchy cache with new last_updated values
			if db.HierarchyCache != nil {
				if err := db.HierarchyCache.UpdateHierarchyLastUpdated(db); err != nil {
					log.Printf("[WEB]: Hierarchy cache update failed: %v", err)
				} else {
					log.Printf("[WEB]: Hierarchy cache updated successfully")
				}
			}
		}
	}
}

// monitorUpdateFile checks for the existence of an .update file every 60 seconds
// and signals for shutdown when found, then renames the file; it returns when stop
// is closed.
func monitorUpdateFile(shutdownChan chan<- bool, stop <-chan struct{}) {
	updateFilePath := ".update"
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	log.Printf("[WEB]: Update file monitor started, checking for '%s' every 60 seconds", updateFilePath)

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		// Check if .update file exists
		if _, err := os.Stat(updateFilePath); err == nil {
			log.Printf("[WEB]: Update file '%s' detected, triggering graceful shutdown", updateFilePath)

			// Rename the update file
			if err := os.Rename(updateFilePath, updateFilePath+".todo"); err != nil {
				log.Printf("[WEB]: Warning: Failed to rename update file '%s': %v", updateFilePath, err)
				continue
			} else {
				log.Printf("[WEB]: Update file '%s' renamed successfully", updateFilePath)
			}

			// Signal shutdown
			select {
			case shutdownChan <- true:
				log.Printf("[WEB]: Shutdown signal sent via update file monitor")
			default:
				log.Printf("[WEB]: Shutdown channel already signaled")
			}
			return
		}
		// File doesn't exist, continue monitoring
	}
}

func ConnectPools(db *database.Database) []*nntp.Pool {
	log.Printf("[WEB]: Fetching providers from database...")
	providers, err := db.GetProviders()
	if err != nil {
		fatalAfterDB(db, "[WEB]: Failed to fetch providers: %v", err)
	}
	log.Printf("[WEB]: Found %d providers in database", len(providers))
	pools := make([]*nntp.Pool, 0, len(providers))
	log.Printf("[WEB]: Create provider connection pools...")
	enabledProviders := 0
	for _, p := range providers {
		if !p.Enabled || p.Host == "" || p.Port <= 0 {
			log.Printf("[WEB]: Skipping disabled/invalid provider: %s (ID: %d, Enabled: %v, Host: '%s', Port: %d)",
				p.Name, p.ID, p.Enabled, p.Host, p.Port)
			continue // Skip disabled providers
		}
		enabledProviders++
		// Convert models.Provider to config.Provider for the BackendConfig
		configProvider := &config.Provider{
			Grp:        p.Grp,
			Name:       p.Name,
			Host:       p.Host,
			Port:       p.Port,
			SSL:        p.SSL,
			Username:   p.Username,
			Password:   p.Password,
			MaxConns:   p.MaxConns,
			Enabled:    p.Enabled,
			Priority:   p.Priority,
			MaxArtSize: p.MaxArtSize,
			// Copy proxy configuration from database provider
			ProxyEnabled:  p.ProxyEnabled,
			ProxyType:     p.ProxyType,
			ProxyHost:     p.ProxyHost,
			ProxyPort:     p.ProxyPort,
			ProxyUsername: p.ProxyUsername,
			ProxyPassword: p.ProxyPassword,
		}

		backendConfig := &nntp.BackendConfig{
			Host:     p.Host,
			Port:     p.Port,
			SSL:      p.SSL,
			Username: p.Username,
			Password: p.Password,
			//ConnectTimeout: 9 * time.Second,
			//ReadTimeout:    60 * time.Second,
			//WriteTimeout:   60 * time.Second,
			MaxConns: p.MaxConns,
			Provider: configProvider, // Set the Provider field
			// Copy proxy configuration from database provider
			ProxyEnabled:  p.ProxyEnabled,
			ProxyType:     p.ProxyType,
			ProxyHost:     p.ProxyHost,
			ProxyPort:     p.ProxyPort,
			ProxyUsername: p.ProxyUsername,
			ProxyPassword: p.ProxyPassword,
		}

		pool := nntp.NewPool(backendConfig)
		pools = append(pools, pool)
		log.Printf("[WEB]: Using only first enabled provider '%s' (TODO: support multiple providers)", p.Name)
		break // For now, we only use the first enabled provider!!! TODO
	}
	log.Printf("[WEB]: %d providers, %d enabled, using %d pools", len(providers), enabledProviders, len(pools))
	return pools
}

// NewFetchProcessor creates the processor with the resolved (stored) history
// UseShortHashLen; errors of processor.NewProcessorE are returned, not fatal.
func NewFetchProcessor(db *database.Database, shortHashLen int) (*processor.Processor, error) {
	var pool *nntp.Pool
	pools := ConnectPools(db) // Get NNTP pools
	if len(pools) == 0 {
		log.Printf("[WEB]: ERROR: No enabled providers found! Cannot proceed with article fetching")
		pool = nil
	} else {
		pool = pools[0]
	}
	log.Printf("[WEB]: Creating processor instance with useShortHashLen=%d...", shortHashLen)
	proc, err := processor.NewProcessorE(db, pool, shortHashLen) // Create a new processor instance
	if err != nil {
		return nil, fmt.Errorf("create processor: %w", err)
	}
	log.Printf("[WEB]: Processor created successfully")
	return proc, nil
}

func FetchRoutine(db *database.Database, proc *processor.Processor, useShortHashLen int, boot bool, isleep int64, DLParChan chan struct{}, progressDB *database.ProgressDB) {
	/* DISABLED
	if isleep < 15 {
		isleep = 15 // min 15 sec sleep!
	}
	startTime := time.Now()
	log.Printf("[WEB]: FetchRoutine STARTED (boot=%v, useShortHashLen=%d) at %v", boot, useShortHashLen, startTime)

	webmutex.Lock()
	log.Printf("[WEB]: Acquired webmutex lock")
	defer func() {
		webmutex.Unlock()
		log.Printf("[WEB]: Released webmutex lock")
	}()

	if boot {
		log.Printf("[WEB]: Boot mode detected - waiting %v before starting...", isleep)
		select {
		case <-db.StopChan:
			log.Printf("[WEB]: Shutdown detected during boot wait, exiting FetchRoutine")
			return
		case <-time.After(time.Duration(isleep) * time.Second):
			log.Printf("[WEB]: Boot wait completed, starting article fetching")
		}
	}
	log.Printf("[WEB]: Begin article fetching process")

	defer func(isleep int64, progressDB *database.ProgressDB) {
		duration := time.Since(startTime)
		log.Printf("[WEB]: FetchRoutine COMPLETED after %v", duration)
		if isleep > 30 {
			proc.Pool.ClosePool() // Close the NNTP pool if sleep is more than 30 seconds
		}
		// Check if shutdown was requested before scheduling restart
		select {
		case <-db.StopChan:
			log.Printf("[WEB]: Shutdown detected, not restarting FetchRoutine")
			return
		default:
			// pass
		}
		// Sleep with shutdown check
	wait:
		for {
			select {
			case <-db.StopChan:
				log.Printf("[WEB]: Shutdown detected during sleep, not restarting FetchRoutine")
				return
			case <-time.After(time.Duration(isleep) * time.Second):
				break wait
			}
		}
		pools := ConnectPools(db) // Reconnect pools after sleep
		if len(pools) == 0 {
			log.Printf("[WEB]: ERROR: No enabled providers found after sleep! Cannot proceed with article fetching")
		} else {
			proc.Pool = pools[0] // Use the first pool
		}
		log.Printf("[WEB]: Sleep completed, starting new FetchRoutine goroutine")
		go FetchRoutine(db, proc, useShortHashLen, false, isleep, DLParChan, progressDB)
		log.Printf("[WEB]: New FetchRoutine goroutine launched")
	}(isleep, progressDB)

	log.Printf("[WEB]: Fetching newsgroups from database...")
	groups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		log.Printf("[WEB]: FATAL: Could not fetch newsgroups: %v", err)
		log.Printf("[WEB]: Warning: Could not fetch newsgroups: %v", err)
		return
	}

	log.Printf("[WEB]: Found %d newsgroups in database", len(groups))

	// Check for data integrity issues
	emptyNameCount := 0
	for _, group := range groups {
		if group.Name == "" {
			emptyNameCount++
			log.Printf("[WEB]: WARNING: Found newsgroup with empty name (ID: %d)", group.ID)
		}
	}
	if emptyNameCount > 0 {
		log.Printf("[WEB]: WARNING: Found %d newsgroups with empty names - this indicates a data integrity issue", emptyNameCount)
	}

	// Configure bridges if enabled
	if enableFediverse || enableMatrix {
		bridgeConfig := &processor.BridgeConfig{
			FediverseEnabled:  enableFediverse,
			FediverseDomain:   fediverseDomain,
			FediverseBaseURL:  fediverseBaseURL,
			MatrixEnabled:     enableMatrix,
			MatrixHomeserver:  matrixHomeserver,
			MatrixAccessToken: matrixAccessToken,
			MatrixUserID:      matrixUserID,
		}
		proc.EnableBridges(bridgeConfig)
		log.Printf("[WEB]: Bridge configuration applied")
	} else {
		log.Printf("[WEB]: No bridges enabled (use --enable-fediverse or --enable-matrix to enable)")
	}

	log.Printf("[WEB]: Starting to process %d newsgroups...", len(groups))
	processedCount := 0
	upToDateCount := 0
	errorCount := 0

	for i, group := range groups {
		// Check for shutdown before processing each group
		select {
		case <-db.StopChan:
			log.Printf("[WEB]: Shutdown detected, stopping newsgroup processing at group %d/%d", i+1, len(groups))
			return
		default:
			// Continue processing
		}

		// Skip groups with empty names
		if group.Name == "" {
			log.Printf("[WEB]: [%d/%d] Skipping group with empty name (ID: %d)", i+1, len(groups), group.ID)
			errorCount++
			continue
		}

		log.Printf("[WEB]: [%d/%d] Processing group: %s (ID: %d)", i+1, len(groups), group.Name, group.ID)

		// Register newsgroup with bridges if enabled
		if proc.BridgeManager != nil {
			if err := proc.BridgeManager.RegisterNewsgroup(group); err != nil {
				log.Printf("web.main.go: [%d/%d] Warning: Failed to register group %s with bridges: %v", i+1, len(groups), group.Name, err)
			}
		}

		// Check if the group is in sections DB
		err := proc.DownloadArticles(group.Name, ignoreInitialTinyGroups, DLParChan, progressDB, 0 ,0)
		if err != nil {
			if err.Error() == "up2date" {
				log.Printf("[WEB]: [%d/%d] Group %s is up to date, skipping", i+1, len(groups), group.Name)
				upToDateCount++
			} else {
				log.Printf("[WEB]: [%d/%d] ERROR processing group %s: %v", i+1, len(groups), group.Name, err)
				errorCount++

				// If we're getting too many consecutive errors, it might be an auth issue
				if errorCount > 10 && (processedCount+upToDateCount) == 0 {
					log.Printf("[WEB]: WARNING: Too many consecutive errors (%d), this might indicate authentication or connection issues", errorCount)
				}
			}
		} else {
			log.Printf("[WEB]: [%d/%d] Successfully processed group %s", i+1, len(groups), group.Name)
			processedCount++
		}
		log.Printf("[WEB]: Progress: processed=%d, up-to-date=%d, errors=%d, remaining=%d",
			processedCount, upToDateCount, errorCount, len(groups)-(i+1))
	}
	log.Printf("[WEB]: Finished processing all %d groups", len(groups))
	log.Printf("[WEB]: FINAL SUMMARY - Total groups: %d, Successfully processed: %d, Up-to-date: %d, Errors: %d",
		len(groups), processedCount, upToDateCount, errorCount)
	*/
}

// findOrphanedDatabases scans data/db folder for MD5 hashed directories and checks if they correspond to newsgroups in main database
func findOrphanedDatabases(db *database.Database, dataDir string, verbose bool) error {
	if dataDir == "" {
		dataDir = "data"
	}
	dbPath := filepath.Join(dataDir, "db")

	// Check if db directory exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return fmt.Errorf("database directory does not exist: %s", dbPath)
	}

	log.Printf("[ORPHAN-DB]: Scanning database folders in: %s", dbPath)

	// Read all directories in data/db
	entries, err := os.ReadDir(dbPath)
	if err != nil {
		return fmt.Errorf("failed to read database directory: %w", err)
	}

	// Get all newsgroups from main database and create a map of their MD5 hashes
	rows, err := db.GetMainDB().Query("SELECT name FROM newsgroups")
	if err != nil {
		return fmt.Errorf("failed to query newsgroups: %w", err)
	}
	defer rows.Close()

	validHashes := make(map[string]string) // hash -> newsgroup name
	totalNewsgroups := 0

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("failed to scan newsgroup name: %w", err)
		}
		hash := database.MD5Hash(name)
		validHashes[hash] = name
		totalNewsgroups++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating newsgroups: %w", err)
	}

	log.Printf("[ORPHAN-DB]: Loaded %d newsgroups from main database", totalNewsgroups)

	// Check each directory in data/db
	orphanedCount := 0
	totalDirs := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirName := entry.Name()
		totalDirs++

		// Check if this hash corresponds to a known newsgroup
		if newsgroupName, exists := validHashes[dirName]; exists {
			// Valid directory - corresponds to a newsgroup
			if verbose {
				log.Printf("[VALID] '%s' -> '%s'", dirName, newsgroupName)
			}
			continue
		} else {
			// Orphaned database directory - no matching newsgroup found
			orphanedCount++
			dirPath := filepath.Join(dbPath, dirName)

			// Get directory size
			var size int64
			err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !info.IsDir() {
					size += info.Size()
				}
				return nil
			})

			sizeStr := "unknown"
			if err == nil {
				sizeStr = formatBytes(size)
			}

			log.Printf("[ORPHAN] '%s' (size: %s)", dirName, sizeStr)
		}
	}

	log.Printf("[ORPHAN-DB]: ========================================")
	log.Printf("[ORPHAN-DB]: SCAN COMPLETE")
	log.Printf("[ORPHAN-DB]: Total database directories: %d", totalDirs)
	log.Printf("[ORPHAN-DB]: Valid directories: %d", totalDirs-orphanedCount)
	log.Printf("[ORPHAN-DB]: Orphaned directories: %d", orphanedCount)
	log.Printf("[ORPHAN-DB]: ========================================")

	if orphanedCount > 0 {
		log.Printf("[ORPHAN-DB]: WARNING: Found %d orphaned database directories that don't match any newsgroup", orphanedCount)
		log.Printf("[ORPHAN-DB]: These directories can be safely deleted if they are not needed")
	}

	return nil
}

// formatBytes converts bytes to human-readable format
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// updateNewsgroupsExpiryFromFile reads a file with newsgroup:days format and updates expiry_days
func updateNewsgroupsExpiryFromFile(db *database.Database, filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	updated := 0
	errors := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse line: newsgroup:days
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			log.Printf("[EXPIRY] Line %d: invalid format (expected 'newsgroup:days'): %s", lineNum, line)
			errors++
			continue
		}

		newsgroup := strings.TrimSpace(parts[0])
		daysStr := strings.TrimSpace(parts[1])

		// Strip optional 'd' suffix (e.g., "30d" -> "30")
		daysStr = strings.TrimSuffix(daysStr, "d")
		daysStr = strings.TrimSuffix(daysStr, "D")

		// Parse days as integer
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			log.Printf("[EXPIRY] Line %d: invalid days value '%s' for newsgroup '%s': %v", lineNum, daysStr, newsgroup, err)
			errors++
			continue
		}

		// Update database
		if err := db.UpdateNewsgroupExpiry(newsgroup, days); err != nil {
			log.Printf("[EXPIRY] Line %d: failed to update newsgroup '%s': %v", lineNum, newsgroup, err)
			errors++
			continue
		}

		updated++
		if verbose {
			log.Printf("[EXPIRY] Updated '%s' to %d days", newsgroup, days)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	log.Printf("[EXPIRY] Processed %d lines: %d updated, %d errors", lineNum, updated, errors)

	if errors > 0 {
		return fmt.Errorf("completed with %d errors", errors)
	}

	return nil
}

// disableActiveOpts are the options of disableNewsgroupsNotInActiveFile.
type disableActiveOpts struct {
	// DryRun prints the groups that would be disabled and changes nothing (-disable-active-dry-run).
	DryRun bool
}

// disableNewsgroupsNotInActiveFile disables newsgroups NOT listed in the active file.
// Each non-empty, non-comment line names a group in its first field: active file lines
// (groupname high low status) and plain group lists (one name per line) both work.
// A file that names no group is refused (error, nothing changed); with opts.DryRun the
// groups that would be disabled are printed and nothing is changed (D14).
func disableNewsgroupsNotInActiveFile(db *database.Database, activeFilePath string, opts disableActiveOpts) error {
	log.Printf("[WEB]: Disabling newsgroups not listed in active file: %s (dry-run: %t)", activeFilePath, opts.DryRun)

	// Open and read the active file
	file, err := os.Open(activeFilePath)
	if err != nil {
		return fmt.Errorf("failed to open active file '%s': %w", activeFilePath, err)
	}
	defer file.Close()

	// Parse active file to get list of groups to KEEP active
	activeGroups := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Active file format "groupname high low status" or a plain "groupname" list:
		// the group name is the first field either way
		fields := strings.Fields(line)
		activeGroups[fields[0]] = true
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading active file: %w", err)
	}

	log.Printf("[WEB]: Found %d active groups in file", len(activeGroups))
	if len(activeGroups) == 0 {
		// disabling every group is never what the caller meant: refuse, change nothing
		return fmt.Errorf("active file '%s' lists no newsgroup (%d lines read): refusing to disable every newsgroup, nothing changed", activeFilePath, lineNum)
	}

	// Get all newsgroups from database
	dbGroups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return fmt.Errorf("failed to get newsgroups from database: %w", err)
	}

	log.Printf("[WEB]: Found %d total newsgroups in database", len(dbGroups))

	// Disable groups NOT in active file
	disabledCount := 0
	alreadyInactiveCount := 0
	keptActiveCount := 0

	for _, group := range dbGroups {
		if _, exists := activeGroups[group.Name]; !exists {
			// Group not in active file - disable it
			if group.Active {
				if opts.DryRun {
					fmt.Printf("dry-run: would disable %s\n", group.Name)
					disabledCount++
					continue
				}
				if err := db.UpdateNewsgroupActive(group.Name, false); err != nil {
					log.Printf("[WEB]: Warning: Failed to disable newsgroup '%s': %v", group.Name, err)
					continue
				}
				disabledCount++
				if disabledCount%1000 == 0 {
					log.Printf("[WEB]: Disabled %d newsgroups so far...", disabledCount)
				}
			} else {
				alreadyInactiveCount++
			}
		} else {
			// Group IS in active file - keep it active
			keptActiveCount++
		}
	}

	if opts.DryRun {
		log.Printf("[WEB]: Disable operation dry-run completed (nothing changed):")
		log.Printf("[WEB]:   - Groups that would be disabled (not in active file): %d", disabledCount)
	} else {
		log.Printf("[WEB]: Disable operation completed:")
		log.Printf("[WEB]:   - Groups newly disabled (not in active file): %d", disabledCount)
	}
	log.Printf("[WEB]:   - Groups kept active (in active file): %d", keptActiveCount)
	log.Printf("[WEB]:   - Groups already inactive (not in active file): %d", alreadyInactiveCount)
	log.Printf("[WEB]:   - Total groups processed: %d", len(dbGroups))

	return nil
}
