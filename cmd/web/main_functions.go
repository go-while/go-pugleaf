package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var testFormats = []string{
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05+07:00",
	"2006-01-02 15:04:05",
}

// updateNewsgroupLastActivity updates newsgroups' updated_at field based on their latest article
func updateNewsgroupLastActivity(db *database.Database) error {
	updatedCount := 0
	totalProcessed := 0
	var id int
	var name string
	var formattedDate string
	var parsedDate time.Time
	var latestDate sql.NullString
	// Get newsgroups
	rows, err := db.GetMainDB().Query("SELECT id, name FROM newsgroups WHERE message_count > 0")
	if err != nil {
		return fmt.Errorf("failed to query newsgroups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("error [WEB]: updateNewsgroupLastActivity rows.Scan newsgroup: %v", err)
		}
		if err := updateNewsGroupActivityValue(db, &id, &name, &latestDate, &parsedDate, &formattedDate); err == nil {
			updatedCount++
		}
		totalProcessed++
		log.Printf("[WEB]: Processed %d newsgroups, updated %d so far", totalProcessed, updatedCount)
	}
	// Check for iteration errors
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error updateNewsgroupLastActivity iterating newsgroups: %w", err)
	}

	log.Printf("[WEB]: updateNewsgroupLastActivity completed: processed %d total newsgroups, updated %d", totalProcessed, updatedCount)
	return nil
}

const ActivityQuery = "UPDATE newsgroups SET updated_at = ? WHERE id = ? AND updated_at != ?"

func updateNewsGroupActivityValue(db *database.Database, id *int, name *string, latestDate *sql.NullString, parsedDate *time.Time, formattedDate *string) error {
	// Get the group database for this newsgroup
	groupDBs, err := db.GetGroupDBs(*name)
	if err != nil {
		log.Printf("[WEB]: updateNewsgroupLastActivity GetGroupDB %s: %v", *name, err)
		return err
	}

	/*
		_, err = database.RetryableExec(groupDBs.DB, "UPDATE articles SET spam = 1 WHERE spam = 0 AND hide = 1", nil)
		if err != nil {
			db.ForceCloseGroupDBs(groupDBs)
			log.Printf("[WEB]: Failed to update spam flags for newsgroup %s: %v", name, err)
			continue
		}
	*/

	// Query the latest article date from the group's articles table (excluding hidden articles)
	rows, err := database.RetryableQuery(groupDBs.DB, "SELECT MAX(date_sent) FROM articles WHERE hide = 0 LIMIT 1", nil, latestDate)
	//groupDBs.Return(db) // Always return the database connection
	if err != nil {
		log.Printf("[WEB]: updateNewsgroupLastActivity RetryableQueryRowScan %s: %v", *name, err)
		return err
	}
	defer rows.Close()
	defer db.ForceCloseGroupDBs(groupDBs)
	for rows.Next() {
		// Only update if we found a latest date
		if latestDate.Valid {
			// Parse the date and format it consistently as UTC
			if latestDate.String == "" {
				log.Printf("[WEB]: updateNewsgroupLastActivity empty latestDate.String in ng: '%s'", *name)
				return fmt.Errorf("error updateNewsgroupLastActivity empty latestDate.String in ng: '%s'", *name)
			}
			// Try multiple date formats to handle various edge cases
			for _, format := range testFormats {
				*parsedDate, err = time.Parse(format, latestDate.String)
				if err == nil {
					break
				}
			}
			if err != nil {
				log.Printf("[WEB]: updateNewsgroupLastActivity parsing date '%s' for %s: %v", latestDate.String, *name, err)
				return err
			}

			// Format as UTC without timezone info to match db_batch.go format
			*formattedDate = parsedDate.UTC().Format("2006-01-02 15:04:05")
			result, err := db.GetMainDB().Exec(ActivityQuery, *formattedDate, *id, *formattedDate)
			if err != nil {
				log.Printf("[WEB]: error updateNewsgroupLastActivity updating newsgroup %s: %v", *name, err)
				return err
			}
			if _, err := result.RowsAffected(); err != nil {
				log.Printf("[WEB]: updateNewsgroupLastActivity: '%s' dateStr=%s formattedDate=%s", *name, latestDate.String, *formattedDate)
			}

		}
	}
	return nil
}

// hideFuturePosts updates articles' hide field to 1 if they are posted more than 48 hours in the future
func hideFuturePosts(db *database.Database) error {
	// Calculate the cutoff time (current time + 48 hours)
	cutoffTime := time.Now().Add(48 * time.Hour)

	// First, get all newsgroups from the main database
	rows, err := db.GetMainDB().Query("SELECT id, name FROM newsgroups WHERE message_count > 0 AND active = 1")
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
		groupDBs, err := db.GetGroupDBs(name)
		if err != nil {
			log.Printf("[WEB]: Future posts migration error getting group DB for %s: %v", name, err)
			skippedGroups++
			continue
		}

		// Update articles that are posted more than 48 hours in the future
		result, err := database.RetryableExec(groupDBs.DB, "UPDATE articles SET hide = 1, spam = 1 WHERE date_sent > ? AND hide = 0", cutoffTime.Format("2006-01-02 15:04:05"))
		db.ForceCloseGroupDBs(groupDBs)

		if err != nil {
			log.Printf("[WEB]: Future posts migration error updating articles for %s: %v", name, err)
			skippedGroups++
			continue
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			log.Printf("[WEB]: Future posts migration error getting rows affected for %s: %v", name, err)
			skippedGroups++
			continue
		}

		if rowsAffected > 0 {
			log.Printf("[WEB]: Hidden %d future posts in newsgroup %s", rowsAffected, name)
			updatedArticles += int(rowsAffected)
		}
		processedGroups++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating newsgroups: %w", err)
	}

	log.Printf("[WEB]: Future posts migration completed: processed %d groups, hidden %d articles, skipped %d groups", processedGroups, updatedArticles, skippedGroups)
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

func rsyncInactiveGroupsToDir(db *database.Database, newdatadir string) error {

	// check if newdatadir exists
	if _, err := os.Stat(newdatadir); os.IsNotExist(err) {
		if err := os.MkdirAll(newdatadir, 0755); err != nil {
			log.Printf("[WEB]: Warning: Failed to create new data directory '%s': %v", newdatadir, err)
			return err
		}
	}

	rows, err := db.GetMainDB().Query(`
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
	for rows.Next() {
		var newsgroup string
		if err := rows.Scan(&newsgroup); err != nil {
			log.Printf("[WEB]: Warning: Failed to scan newsgroup row: %v", err)
			return err
		}
		groupsHash := database.MD5Hash(newsgroup)
		baseGroupDBdir := filepath.Join("data", "/db/"+groupsHash)
		baseGroupDBdirNew := filepath.Join(basedirNew, groupsHash)
		sanitizedName := database.SanitizeGroupName(newsgroup)
		groupDBfileOld := filepath.Join(baseGroupDBdir + "/" + sanitizedName + ".db")
		if !database.FileExists(groupDBfileOld) {
			log.Printf("[RSYNC]: Group database file does not exist: %s", groupDBfileOld)
			continue
		}
		if _, err := os.Stat(baseGroupDBdirNew); os.IsNotExist(err) {
			if err := os.MkdirAll(baseGroupDBdirNew, 0755); err != nil {
				log.Printf("[WEB]: Warning: Failed to create new data directory '%s': %v", newdatadir, err)
				return err
			}
		}
		start := time.Now()
		if err := database.RsyncDIR(baseGroupDBdir, basedirNew, rsyncRemoveSource); err != nil {
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

// startHierarchyUpdater runs a background job every N minutes to update
// hierarchy last_updated fields based on their child newsgroups
func startHierarchyUpdater(db *database.Database) {
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
	log.Printf("[WEB]: Hierarchy updater started, will sync hierarchy last_updated every 30 minutes")

	for {
		time.Sleep(10 * time.Minute)
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

// monitorUpdateFile checks for the existence of an .update file every 30 seconds
// and signals for shutdown when found, then removes the file
func monitorUpdateFile(shutdownChan chan<- bool) {
	updateFilePath := ".update"
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	log.Printf("[WEB]: Update file monitor started, checking for '%s' every 60 seconds", updateFilePath)

	for range ticker.C {

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
		log.Fatalf("[WEB]: Failed to fetch providers: %v", err)
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
		}

		pool := nntp.NewPool(backendConfig)
		pool.StartCleanupWorker(5 * time.Second)
		pools = append(pools, pool)
		log.Printf("[WEB]: Using only first enabled provider '%s' (TODO: support multiple providers)", p.Name)
		break // For now, we only use the first enabled provider!!! TODO
	}
	log.Printf("[WEB]: %d providers, %d enabled, using %d pools", len(providers), enabledProviders, len(pools))
	return pools
}

func NewFetchProcessor(db *database.Database) *processor.Processor {
	var pool *nntp.Pool
	pools := ConnectPools(db) // Get NNTP pools
	if len(pools) == 0 {
		log.Printf("[WEB]: ERROR: No enabled providers found! Cannot proceed with article fetching")
		pool = nil
	} else {
		pool = pools[0]
	}
	log.Printf("[WEB]: Creating processor instance with useShortHashLen=%d...", useShortHashLen)
	proc := processor.NewProcessor(db, pool, useShortHashLen) // Create a new processor instance
	log.Printf("[WEB]: Processor created successfully")
	return proc
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
