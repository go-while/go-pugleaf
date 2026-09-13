package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// maxPrintedMisses limits the misses printed with -validate-only -verbose
const maxPrintedMisses = 100

// trimChunkSize is the number of history rows read per query during -trim
const trimChunkSize = 10000

type RebuildStats struct {
	GroupsTotal       int64
	GroupsProcessed   int64
	GroupsSkipped     int64 // resumed or without group DB file
	ArticlesProcessed int64
	HistoryQueued     int64 // AddArticle calls (rebuild)
	HistoryFound      int64 // validate: message-id found with this group
	HistoryMissing    int64 // validate: message-id not in history
	HistoryWrongGroup int64 // validate: message-id in history, but without this group
	Errors            int64
	StartTime         time.Time
	EndTime           time.Time // set when scanning is done (before flush/shutdown)
	lastProgress      int64
}

type TrimStats struct {
	RowsScanned    int64
	GroupRefs      int64
	Removed        int64
	GroupsMissing  int64 // refs to a group ID not in the main DB (or without group DB file)
	ArticleMissing int64 // refs where the group DB does not have the article
	Errors         int64
	StartTime      time.Time
	EndTime        time.Time
}

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	log.Printf("Starting go-pugleaf History Rebuild Tool (version %s)", config.AppVersion)

	var (
		batchSize        = flag.Int("batch-size", 10000, "Article number range scanned per query")
		progressInterval = flag.Int("progress", 100000, "Show progress every N articles")
		validateOnly     = flag.Bool("validate-only", false, "Only check that every article of every group is in the history index (no writes)")
		analyzeOnly      = flag.Bool("analyze-only", false, "Only analyze the history database files (read-only row counts and groups-per-message-id histogram)")
		trim             = flag.Bool("trim", false, "Remove history entries whose article no longer exists in the group DB (slow full sweep)")
		restart          = flag.Bool("restart", false, "Rebuild: ignore and remove the rebuild.progress file and start with the first group")
		verbose          = flag.Bool("verbose", false, "Enable verbose logging")
		useShortHashLen  = flag.Int("useshorthashlen", 7, "No effect since Nov 2025 (history stores full message-ids), accepted for compatibility")
		pprofAddr        = flag.String("pprof", "", "Enable pprof HTTP server on specified address (e.g., ':6060')")
		dataDir          = flag.String("data", "./data", "Directory to store database files")
	)
	flag.Parse()
	_ = useShortHashLen

	modes := 0
	for _, m := range []bool{*validateOnly, *analyzeOnly, *trim} {
		if m {
			modes++
		}
	}
	if modes > 1 {
		log.Fatalf("Only one of -validate-only, -analyze-only, -trim can be used")
	}
	if *batchSize <= 0 {
		log.Fatalf("-batch-size must be > 0")
	}
	if *progressInterval <= 0 {
		*progressInterval = 100000
	}

	if *pprofAddr != "" {
		go func() {
			log.Printf("Starting pprof server on %s", *pprofAddr)
			if err := http.ListenAndServe(*pprofAddr, nil); err != nil {
				log.Printf("pprof server failed: %v", err)
			}
		}()
	}

	historyDir := filepath.Join(*dataDir, "history")

	fmt.Println("Go-Pugleaf History Rebuild Utility")
	fmt.Println("==================================")
	fmt.Printf("  Data dir:       %s\n", *dataDir)
	fmt.Printf("  History dir:    %s\n", historyDir)
	switch {
	case *analyzeOnly:
		fmt.Printf("  Mode:           analyze (read-only)\n")
	case *validateOnly:
		fmt.Printf("  Mode:           validate (read-only)\n")
	case *trim:
		fmt.Printf("  Mode:           trim\n")
	default:
		fmt.Printf("  Mode:           rebuild (restart=%t)\n", *restart)
	}
	fmt.Println()

	// analyze reads the history files directly, no main DB needed
	if *analyzeOnly {
		res, err := analyzeHistoryDir(historyDir)
		if err != nil {
			log.Fatalf("Failed to analyze history: %v", err)
		}
		printHistoryAnalysis(res, *verbose)
		if res.MissingFiles > 0 {
			os.Exit(1)
		}
		return
	}

	if (*validateOnly || *trim) && !history.DirExists(historyDir) {
		log.Fatalf("History directory %s does not exist, nothing to validate/trim", historyDir)
	}

	database.NO_CACHE_BOOT = true // prevents booting caches
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = *dataDir
	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	history.ENABLE_HISTORY = true
	hcfg := history.DefaultConfig()
	hcfg.HistoryDir = historyDir
	h, err := history.NewHistory(hcfg, db.WG)
	if err != nil {
		log.Printf("Failed to open history: %v", err)
		shutdownDatabase(db)
		os.Exit(1)
	}

	// Ctrl+C: stop after the current range/chunk, then flush and shut down
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigChan
		log.Printf("[HISTORY-REBUILD]: Received shutdown signal, stopping after the current range...")
		cancel()
	}()

	var errCount int64
	var trimStats *TrimStats
	var groupStats *RebuildStats
	if *trim {
		trimStats = runTrim(ctx, db, h, *dataDir, historyDir, *verbose)
		trimStats.EndTime = time.Now()
		errCount = trimStats.Errors
	} else {
		groupStats = runGroups(ctx, db, h, *dataDir, historyDir, *batchSize, *progressInterval, *validateOnly, *restart, *verbose)
		groupStats.EndTime = time.Now()
		errCount = groupStats.Errors
	}

	// Shutdown: history first (blocks until all queued ops are committed), then the database
	log.Printf("[HISTORY-REBUILD]: Closing history (pending=%d)...", h.GetStats().Pending)
	if err := h.Close(); err != nil {
		log.Printf("[HISTORY-REBUILD]: Failed to close history: %v", err)
		errCount++
	}
	hs := h.GetStats()
	log.Printf("[HISTORY-REBUILD]: History closed: adds=%d removes=%d committed=%d flushes=%d errors=%d",
		hs.TotalAdds, hs.TotalRemoves, hs.TotalCommitted, hs.Flushes, hs.Errors)
	errCount += hs.Errors
	if !shutdownDatabase(db) {
		errCount++
	}

	if trimStats != nil {
		trimStats.PrintFinal(ctx.Err() != nil)
	} else {
		groupStats.PrintFinal(*validateOnly, ctx.Err() != nil)
	}

	switch {
	case errCount > 0:
		fmt.Printf("\nCompleted with %d errors. Check logs for details.\n", errCount)
		os.Exit(1)
	case ctx.Err() != nil:
		fmt.Println("\nInterrupted. Queued history ops were flushed; re-run to continue.")
		os.Exit(130)
	}
}

// shutdownDatabase stops the database background workers and closes all databases
func shutdownDatabase(db *database.Database) bool {
	close(db.StopChan)
	db.WG.Wait()
	if err := db.Shutdown(); err != nil {
		log.Printf("[HISTORY-REBUILD]: Failed to shutdown database: %v", err)
		return false
	}
	return true
}

// runGroups rebuilds (default) or validates the history index from all group databases
func runGroups(ctx context.Context, db *database.Database, h *history.History, dataDir, historyDir string,
	batchSize, progressInterval int, validateOnly, restart, verbose bool) *RebuildStats {

	stats := &RebuildStats{StartTime: time.Now()}

	groups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		log.Printf("Failed to get newsgroups: %v", err)
		stats.Errors++
		return stats
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	stats.GroupsTotal = int64(len(groups))

	lastDone := ""
	if !validateOnly {
		if restart {
			if err := removeRebuildProgress(historyDir); err != nil {
				log.Printf("Failed to remove progress file: %v", err)
				stats.Errors++
				return stats
			}
		} else {
			lastDone, err = readRebuildProgress(historyDir)
			if err != nil {
				log.Printf("Failed to read progress file: %v", err)
				stats.Errors++
				return stats
			}
			if lastDone != "" {
				log.Printf("[REBUILD] Resuming after group '%s' (use -restart to start over)", lastDone)
			}
		}
	}
	fmt.Printf("Found %d newsgroups to process\n\n", len(groups))

	for _, group := range groups {
		if ctx.Err() != nil {
			break
		}
		if skipGroupForResume(group.Name, lastDone) {
			stats.GroupsSkipped++
			continue
		}
		if !database.FileExists(groupDBFilePath(dataDir, group.Name)) {
			if verbose {
				log.Printf("[REBUILD] Skipping group '%s': no group DB file", group.Name)
			}
			stats.GroupsSkipped++
			if !validateOnly {
				if err := writeRebuildProgress(historyDir, group.Name); err != nil {
					log.Printf("Failed to write progress file: %v", err)
					stats.Errors++
				}
			}
			continue
		}

		completed, err := processGroup(ctx, db, h, group, batchSize, progressInterval, validateOnly, verbose, stats)
		if err != nil {
			log.Printf("Error processing group '%s': %v", group.Name, err)
			stats.Errors++
			continue
		}
		if !completed {
			break // interrupted inside the group: do not mark it done
		}
		stats.GroupsProcessed++
		if !validateOnly {
			if err := writeRebuildProgress(historyDir, group.Name); err != nil {
				log.Printf("Failed to write progress file: %v", err)
				stats.Errors++
			}
		}
		stats.PrintProgress(h, validateOnly)
	}

	if !validateOnly && ctx.Err() == nil {
		// loop finished (not interrupted): the next run starts from the beginning again
		if err := removeRebuildProgress(historyDir); err != nil {
			log.Printf("Failed to remove progress file: %v", err)
		}
	}
	return stats
}

const query_processGroup = `SELECT message_id, article_num FROM articles
				  WHERE message_id IS NOT NULL AND message_id != ''
				    AND article_num >= ? AND article_num <= ?
		          ORDER BY article_num`

const query_processGroupNext = `SELECT MIN(article_num) FROM articles WHERE message_id IS NOT NULL AND message_id != '' AND article_num > ?`

// processGroup scans one group DB by article_num ranges. completed=false if ctx was cancelled.
func processGroup(ctx context.Context, db *database.Database, h *history.History, group *models.Newsgroup,
	batchSize, progressInterval int, validateOnly, verbose bool, stats *RebuildStats) (completed bool, err error) {

	if group.ID <= 0 {
		return false, fmt.Errorf("invalid group ID %d", group.ID)
	}
	groupDB, err := db.GetGroupDB(group.Name)
	if err != nil {
		return false, fmt.Errorf("failed to get group database: %w", err)
	}
	defer groupDB.Return()

	var minArtNum, maxArtNum sql.NullInt64
	err = database.RetryableQueryRowScan(groupDB.DB, `SELECT MIN(article_num), MAX(article_num) FROM articles WHERE message_id IS NOT NULL AND message_id != ''`, nil, &minArtNum, &maxArtNum)
	if err != nil {
		return false, fmt.Errorf("failed to get article number range: %w", err)
	}
	if !minArtNum.Valid || !maxArtNum.Valid {
		return true, nil // no articles
	}
	if verbose {
		log.Printf("[REBUILD] Processing group '%s' (id=%d): article range %d-%d", group.Name, group.ID, minArtNum.Int64, maxArtNum.Int64)
	}

	var groupArticles int64
	groupStart := time.Now()
	current := minArtNum.Int64
	for current <= maxArtNum.Int64 {
		if ctx.Err() != nil {
			return false, nil
		}
		rangeEnd := current + int64(batchSize) - 1
		if rangeEnd > maxArtNum.Int64 {
			rangeEnd = maxArtNum.Int64
		}
		n, err := processRange(groupDB, h, group, current, rangeEnd, validateOnly, verbose, stats)
		if err != nil {
			return false, err
		}
		groupArticles += n

		if stats.ArticlesProcessed-stats.lastProgress >= int64(progressInterval) {
			stats.lastProgress = stats.ArticlesProcessed
			stats.PrintProgress(h, validateOnly)
		}

		if rangeEnd >= maxArtNum.Int64 {
			break
		}
		current = rangeEnd + 1
		if n == 0 {
			// sparse numbering: jump to the next existing article
			var next sql.NullInt64
			if err := database.RetryableQueryRowScan(groupDB.DB, query_processGroupNext, []interface{}{rangeEnd}, &next); err != nil {
				return false, fmt.Errorf("failed to find next article after %d: %w", rangeEnd, err)
			}
			if !next.Valid {
				break
			}
			current = next.Int64
		}
	}
	if verbose || groupArticles >= 10000 {
		log.Printf("[REBUILD] Group '%s': %d articles in %v", group.Name, groupArticles, time.Since(groupStart).Truncate(time.Millisecond))
	}
	return true, nil
}

// processRange handles the articles of one article_num range, returns the number of articles seen
func processRange(groupDB *database.GroupDB, h *history.History, group *models.Newsgroup, from, to int64,
	validateOnly, verbose bool, stats *RebuildStats) (int64, error) {

	rows, err := database.RetryableQuery(groupDB.DB, query_processGroup, from, to)
	if err != nil {
		return 0, fmt.Errorf("failed to query article range %d-%d: %w", from, to, err)
	}
	defer rows.Close()

	var n int64
	for rows.Next() {
		var messageID string
		var articleNum int64
		if err := rows.Scan(&messageID, &articleNum); err != nil {
			log.Printf("Error scanning row in group '%s': %v", group.Name, err)
			stats.Errors++
			continue
		}
		n++
		stats.ArticlesProcessed++

		if !validateOnly {
			h.AddArticle(messageID, group.ID)
			stats.HistoryQueued++
			continue
		}

		groupIDs, err := h.LookupGroups(messageID)
		switch {
		case err != nil:
			log.Printf("[VALIDATE] lookup failed for '%s' in '%s': %v", messageID, group.Name, err)
			stats.Errors++
		case groupIDs == nil:
			stats.HistoryMissing++
			if verbose && stats.HistoryMissing+stats.HistoryWrongGroup <= maxPrintedMisses {
				log.Printf("[VALIDATE] miss: '%s' (group '%s' article %d)", messageID, group.Name, articleNum)
			}
		case !containsID(groupIDs, group.ID):
			stats.HistoryWrongGroup++
			if verbose && stats.HistoryMissing+stats.HistoryWrongGroup <= maxPrintedMisses {
				log.Printf("[VALIDATE] group missing: '%s' (group '%s' id=%d article %d, history has %v)", messageID, group.Name, group.ID, articleNum, groupIDs)
			}
		default:
			stats.HistoryFound++
		}
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("error reading article range %d-%d: %w", from, to, err)
	}
	return n, nil
}

func containsID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// trimGroupInfo caches the result of resolving a group ID during -trim
type trimGroupInfo struct {
	name   string
	usable bool // group exists in the main DB and has a group DB file
}

// runTrim sweeps every history row and removes group IDs whose article is gone from the group DB
func runTrim(ctx context.Context, db *database.Database, h *history.History, dataDir, historyDir string, verbose bool) *TrimStats {
	ts := &TrimStats{StartTime: time.Now()}
	fmt.Println("NOTE: -trim is a slow full sweep: every history row is checked against the group databases.")

	numDBs, tablesPerDB, _ := history.GetShardConfig(history.SHARD_16_256)
	groupCache := make(map[int64]*trimGroupInfo)
	lastReport := time.Now()

	for dbIndex := 0; dbIndex < numDBs; dbIndex++ {
		path := historyFilePath(historyDir, dbIndex)
		rodb, err := openHistoryFileReadOnly(path)
		if err != nil {
			log.Printf("[TRIM] Failed to open %s read-only: %v", path, err)
			ts.Errors++
			continue
		}
		for tableIndex := 0; tableIndex < tablesPerDB; tableIndex++ {
			if ctx.Err() != nil {
				rodb.Close()
				return ts
			}
			tableName := historyTableName(tableIndex)
			if err := trimTable(ctx, db, h, rodb, tableName, dataDir, groupCache, verbose, ts); err != nil {
				log.Printf("[TRIM] Error in %s table %s: %v", path, tableName, err)
				ts.Errors++
			}
			if time.Since(lastReport) >= 10*time.Second {
				lastReport = time.Now()
				log.Printf("[TRIM] db %x table %s: scanned=%d refs=%d removed=%d errors=%d (%v elapsed)",
					dbIndex, tableName, ts.RowsScanned, ts.GroupRefs, ts.Removed, ts.Errors, time.Since(ts.StartTime).Truncate(time.Second))
			}
		}
		rodb.Close()
		log.Printf("[TRIM] Finished %s: scanned=%d removed=%d", path, ts.RowsScanned, ts.Removed)
	}
	return ts
}

type trimRow struct {
	messageID  string
	newsgroups string
}

// trimTable scans one history table in message_id order (keyset chunks)
func trimTable(ctx context.Context, db *database.Database, h *history.History, rodb *sql.DB, tableName, dataDir string,
	groupCache map[int64]*trimGroupInfo, verbose bool, ts *TrimStats) error {

	query := "SELECT message_id, newsgroups FROM " + tableName + " WHERE message_id > ? ORDER BY message_id LIMIT ?"
	after := ""
	for {
		if ctx.Err() != nil {
			return nil
		}
		// read the chunk fully before checking groups: keeps the read transaction short
		rows, err := rodb.Query(query, after, trimChunkSize)
		if err != nil {
			return err
		}
		chunk := make([]trimRow, 0, trimChunkSize)
		for rows.Next() {
			var r trimRow
			var ng sql.NullString
			if err := rows.Scan(&r.messageID, &ng); err != nil {
				rows.Close()
				return err
			}
			r.newsgroups = ng.String
			chunk = append(chunk, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(chunk) == 0 {
			return nil
		}

		for _, r := range chunk {
			ts.RowsScanned++
			for _, part := range strings.Split(r.newsgroups, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				groupID, err := strconv.ParseInt(part, 10, 64)
				if err != nil || groupID <= 0 {
					log.Printf("[TRIM] invalid group id '%s' for '%s'", part, r.messageID)
					ts.Errors++
					continue
				}
				ts.GroupRefs++
				info, err := resolveTrimGroup(db, dataDir, groupID, groupCache)
				if err != nil {
					log.Printf("[TRIM] failed to resolve group id %d: %v", groupID, err)
					ts.Errors++
					continue
				}
				if !info.usable {
					ts.GroupsMissing++
					h.RemoveArticle(r.messageID, groupID)
					ts.Removed++
					continue
				}
				exists, err := articleExistsInGroup(db, info.name, r.messageID)
				if err != nil {
					log.Printf("[TRIM] failed to check '%s' in '%s': %v", r.messageID, info.name, err)
					ts.Errors++
					continue
				}
				if !exists {
					ts.ArticleMissing++
					if verbose {
						log.Printf("[TRIM] remove '%s' from group '%s' (id=%d)", r.messageID, info.name, groupID)
					}
					h.RemoveArticle(r.messageID, groupID)
					ts.Removed++
				}
			}
		}
		after = chunk[len(chunk)-1].messageID
		if len(chunk) < trimChunkSize {
			return nil
		}
	}
}

// resolveTrimGroup resolves (and caches) a group ID. A group that is not in the main DB or has no
// group DB file is not usable: none of its articles exist. Other lookup errors are returned (no removal).
func resolveTrimGroup(db *database.Database, dataDir string, groupID int64, cache map[int64]*trimGroupInfo) (*trimGroupInfo, error) {
	if info, ok := cache[groupID]; ok {
		return info, nil
	}
	info := &trimGroupInfo{}
	ng, err := db.MainDBGetNewsgroupByID(groupID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		log.Printf("[TRIM] group id %d does not exist in the main DB: removing its history refs", groupID)
	case err != nil:
		return nil, err
	default:
		info.name = ng.Name
		database.NewsgroupDBsIDcache.SetNewsgroupNameByID(groupID, ng.Name)
		if database.FileExists(groupDBFilePath(dataDir, ng.Name)) {
			info.usable = true
		} else {
			log.Printf("[TRIM] group '%s' (id=%d) has no group DB file: removing its history refs", ng.Name, groupID)
		}
	}
	cache[groupID] = info
	return info, nil
}

// articleExistsInGroup is like GroupDB.ExistsMsgIdInArticlesDB, but returns query errors
// instead of reporting them as "not found" (trim must not remove entries on a DB error).
func articleExistsInGroup(db *database.Database, groupName, messageID string) (bool, error) {
	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		return false, err
	}
	defer groupDB.Return()
	var one int
	err = database.RetryableQueryRowScan(groupDB.DB, "SELECT 1 FROM articles WHERE message_id = ? LIMIT 1", []interface{}{messageID}, &one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

func (s *RebuildStats) PrintProgress(h *history.History, validateOnly bool) {
	elapsed := time.Since(s.StartTime)
	rate := float64(s.ArticlesProcessed) / elapsed.Seconds()
	realMem, _ := getRealMemoryUsage()
	if validateOnly {
		fmt.Printf("Progress: %d/%d groups (%d skipped), %d articles, %d found, %d missing, %d wrong group, %d errors | %.1f articles/sec | %v elapsed | RSS: %s\n",
			s.GroupsProcessed, s.GroupsTotal, s.GroupsSkipped, s.ArticlesProcessed, s.HistoryFound, s.HistoryMissing, s.HistoryWrongGroup, s.Errors,
			rate, elapsed.Truncate(time.Second), formatBytes(realMem))
		return
	}
	hs := h.GetStats()
	fmt.Printf("Progress: %d/%d groups (%d skipped), %d articles, %d queued, %d committed, %d pending, %d errors | %.1f articles/sec | %v elapsed | RSS: %s\n",
		s.GroupsProcessed, s.GroupsTotal, s.GroupsSkipped, s.ArticlesProcessed, s.HistoryQueued, hs.TotalCommitted, hs.Pending, s.Errors+hs.Errors,
		rate, elapsed.Truncate(time.Second), formatBytes(realMem))
}

func (s *RebuildStats) PrintFinal(validateOnly, interrupted bool) {
	elapsed := s.EndTime.Sub(s.StartTime)
	rate := float64(s.ArticlesProcessed) / elapsed.Seconds()
	fmt.Printf("\n%s %s\n", map[bool]string{true: "Validation", false: "Rebuild"}[validateOnly], finalState(interrupted))
	fmt.Printf("=====================================\n")
	fmt.Printf("Groups:              %d total, %d processed, %d skipped\n", s.GroupsTotal, s.GroupsProcessed, s.GroupsSkipped)
	fmt.Printf("Articles Processed:  %d\n", s.ArticlesProcessed)
	if validateOnly {
		fmt.Printf("Found in History:    %d\n", s.HistoryFound)
		fmt.Printf("Missing:             %d\n", s.HistoryMissing)
		fmt.Printf("Group not in entry:  %d\n", s.HistoryWrongGroup)
		if s.ArticlesProcessed > 0 {
			fmt.Printf("Coverage:            %.2f%%\n", float64(s.HistoryFound)/float64(s.ArticlesProcessed)*100)
		}
	} else {
		fmt.Printf("History Ops Queued:  %d\n", s.HistoryQueued)
	}
	fmt.Printf("Errors:              %d\n", s.Errors)
	fmt.Printf("Scan Time:           %v (without final flush)\n", elapsed.Truncate(time.Millisecond))
	fmt.Printf("Processing Rate:     %.1f articles/sec\n", rate)
}

func (ts *TrimStats) PrintFinal(interrupted bool) {
	fmt.Printf("\nTrim %s\n", finalState(interrupted))
	fmt.Printf("=====================================\n")
	fmt.Printf("History Rows Scanned: %d\n", ts.RowsScanned)
	fmt.Printf("Group References:     %d\n", ts.GroupRefs)
	fmt.Printf("Removed References:   %d (group gone: %d, article gone: %d)\n", ts.Removed, ts.GroupsMissing, ts.ArticleMissing)
	fmt.Printf("Errors:               %d\n", ts.Errors)
	fmt.Printf("Scan Time:            %v (without final flush)\n", ts.EndTime.Sub(ts.StartTime).Truncate(time.Millisecond))
}

func finalState(interrupted bool) string {
	if interrupted {
		return "Interrupted"
	}
	return "Complete"
}

// printHistoryAnalysis prints the result of analyzeHistoryDir
func printHistoryAnalysis(res *HistoryAnalysis, verbose bool) {
	fmt.Printf("History Database Analysis: %s\n", res.HistoryDir)
	fmt.Printf("=====================================\n")
	for i, fa := range res.Files {
		if fa.Missing {
			fmt.Printf("  hashdb_%x.sqlite3: MISSING\n", i)
			continue
		}
		fmt.Printf("  hashdb_%x.sqlite3: %12d rows | tables %d | min %s=%d max %s=%d\n",
			i, fa.Rows, fa.Tables, fa.MinTable, fa.MinRows, fa.MaxTable, fa.MaxRows)
		if verbose {
			for t, n := range fa.TableRows {
				fmt.Printf("      %s: %d\n", historyTableName(t), n)
			}
		}
	}
	fmt.Printf("\nTotal message-ids:   %d\n", res.TotalRows)
	if res.MissingFiles > 0 {
		fmt.Printf("Missing files:       %d\n", res.MissingFiles)
	}
	fmt.Printf("\nGroups per message-id:\n")
	keys := make([]int, 0, len(res.GroupsHistogram))
	for k := range res.GroupsHistogram {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var histTotal int64
	for _, n := range res.GroupsHistogram {
		histTotal += n
	}
	for _, k := range keys {
		pct := 0.0
		if histTotal > 0 {
			pct = float64(res.GroupsHistogram[k]) / float64(histTotal) * 100
		}
		fmt.Printf("  %4d groups: %12d (%.2f%%)\n", k, res.GroupsHistogram[k], pct)
	}
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// getRealMemoryUsage gets actual RSS memory usage from /proc/self/status on Linux
func getRealMemoryUsage() (uint64, error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.Alloc, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
					return kb * 1024, nil
				}
			}
		}
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Alloc, nil
}
