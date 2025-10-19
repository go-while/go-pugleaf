package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// GroupResult tracks the result of checking/repairing a newsgroup database
type GroupResult struct {
	Name                   string
	Status                 string // "OK", "INCONSISTENT", "REPAIRED", "FAILED", "EMPTY", "ERROR"
	InitialInconsistencies bool
	FinalInconsistencies   bool
	ArticleCount           int64
	ErrorMessage           string
}

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	database.NO_CACHE_BOOT = true // prevents booting caches
	log.Printf("go-pugleaf Database Recovery Tool (version: %s)", config.AppVersion)
	var (
		dbPath              = flag.String("db", "data", "Data Path to main data directory (required)")
		newsgroup           = flag.String("group", "$all", "Newsgroup name to check (required) (\\$all to check for all or news.* to check for all in that hierarchy)")
		verbose             = flag.Bool("v", true, "Verbose output")
		repair              = flag.Bool("repair", false, "Attempt to repair detected inconsistencies")
		parseDates          = flag.Bool("parsedates", false, "Check and log date parsing differences between date_string and date_sent")
		rewriteDates        = flag.Bool("rewritedates", false, "Rewrite incorrect dates (requires -parsedates)")
		rebuildThreads      = flag.Bool("rebuild-threads", false, "Rebuild all thread relationships from scratch (destructive)")
		maxPar              = flag.Int("max-par", 1, "use with -rebuild-threads to process N newsgroups")
		dataDir             = flag.String("data", "./data", "Directory to store database files")
		scanOutOfOrderCheck = flag.Bool("scan-out-of-order-overview", false, "Scan newsgroups for articles with out-of-order date_sent values")
		reorderByDateSent   = flag.Bool("reorder-by-datesent", false, "Reorder all articles by date_sent and write to new database file with .new extension")
	)
	flag.Parse()

	if *dbPath == "" || *newsgroup == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s -db <database-path> -group <newsgroup-name> [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExample:\n")
		fmt.Fprintf(os.Stderr, "  %s -db /path/to/databases -group comp.lang.go\n", os.Args[0])
		os.Exit(1)
	}

	// Validate flag combinations
	if *rewriteDates && !*parseDates {
		fmt.Fprintf(os.Stderr, "Error: -rewritedates requires -parsedates flag\n")
		os.Exit(1)
	}
	if *verbose {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}

	// Validate database path
	if _, err := os.Stat(*dbPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: Database path '%s' does not exist\n", *dbPath)
		os.Exit(1)
	}

	// Initialize configuration
	mainConfig := config.NewDefaultConfig()
	log.Printf("go-pugleaf Database Recovery Tool (version: %s)", mainConfig.AppVersion)

	// Initialize database connection
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = *dataDir

	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Shutdown()
	var newsgroups []*models.Newsgroup
	isWildcard := strings.HasSuffix(*newsgroup, "*")
	if *newsgroup != "$all" && *newsgroup != "" && !isWildcard {
		// if is comma separated check for multiple newsgroups
		if strings.Contains(*newsgroup, ",") {
			for _, grpName := range strings.Split(*newsgroup, ",") {
				if grpName == "" {
					continue
				}
				newsgroups = append(newsgroups, &models.Newsgroup{
					Name: strings.TrimSpace(grpName),
				})
			}

		} else {
			newsgroups = append(newsgroups, &models.Newsgroup{
				Name: *newsgroup,
			})
		}
	} else if isWildcard {
		// strip * from newsgroup and add only newsgroups matching the strings prefix
		prefix := strings.TrimSuffix(*newsgroup, "*")
		allGroups, err := db.MainDBGetAllNewsgroups()
		if err != nil {
			log.Fatalf("failed to get newsgroups from database: %v", err)
		}
		for _, grp := range allGroups {
			if strings.HasPrefix(grp.Name, prefix) {
				newsgroups = append(newsgroups, grp)
			}
		}
	} else {
		newsgroups, err = db.MainDBGetAllNewsgroups()
		if err != nil {
			log.Fatalf("failed to get newsgroups from database: %v", err)
		}
	}
	fmt.Printf("🔍 Starting Database Recovery Tool for go-pugleaf\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("📂 Data Path: %s\n", *dbPath)
	fmt.Printf("📊 Newsgroups:    %d\n", len(newsgroups))
	fmt.Printf("🔧 Repair Mode:   %v\n", *repair)
	fmt.Printf("🧵 Rebuild Threads: %v\n", *rebuildThreads)
	fmt.Printf("📅 Parse Dates:   %v\n", *parseDates)
	fmt.Printf("🔄 Rewrite Dates: %v\n", *rewriteDates)
	fmt.Printf("\n")
	parChan := make(chan struct{}, *maxPar)
	var parMux sync.Mutex
	var wg sync.WaitGroup
	// If only thread rebuilding is requested, run that and exit
	if *rebuildThreads {
		start := time.Now()
		fmt.Printf("🧵 Starting thread rebuild process...\n")
		fmt.Printf("=====================================\n")
		var totalArticles, totalThreadsRebuilt int64
		for i, newsgroup := range newsgroups {
			parChan <- struct{}{} // get lock
			wg.Add(1)
			go func(newsgroup *models.Newsgroup, wg *sync.WaitGroup) {
				defer func(wg *sync.WaitGroup) {
					<-parChan // release lock
					wg.Done()
				}(wg)
				fmt.Printf("🧵 [%d/%d] Rebuilding threads for newsgroup: %s\n", i+1, len(newsgroups), newsgroup.Name)
				report, err := db.RebuildThreadsFromScratch(newsgroup.Name, *verbose, nil)
				if err != nil {
					fmt.Printf("❌ Failed to rebuild threads for '%s': %v\n", newsgroup.Name, err)
					return
				}
				report.PrintReport()
				parMux.Lock()
				totalArticles += report.TotalArticles
				totalThreadsRebuilt += report.ThreadsRebuilt
				parMux.Unlock()
			}(newsgroup, &wg)
		}
		wg.Wait()
		parMux.Lock()
		fmt.Printf("\n🧵 Thread rebuild completed (%d newsgroups) took: %d ms\n", len(newsgroups), time.Since(start).Milliseconds())
		fmt.Printf("   Total articles processed: %d\n", totalArticles)
		fmt.Printf("   Total threads rebuilt: %d\n", totalThreadsRebuilt)
		parMux.Unlock()
		os.Exit(0)
	}

	// If only date parsing is requested, run that and exit
	if *parseDates {
		fmt.Printf("📅 Starting date parsing analysis...\n")
		fmt.Printf("=====================================\n")
		totalFixed, totalChecked, err := checkAndFixDates(db, newsgroups, *rewriteDates, *verbose)
		if err != nil {
			log.Fatalf("Date parsing analysis failed: %v", err)
		}
		fmt.Printf("\n📅 Date parsing analysis completed:\n")
		fmt.Printf("   Articles checked: %d\n", totalChecked)
		fmt.Printf("   Date mismatches found: %d\n", totalFixed)
		if *rewriteDates {
			fmt.Printf("   Dates corrected: %d\n", totalFixed)
		}
		os.Exit(0)
	}

	// If only out-of-order scan is requested, run that and exit
	if *scanOutOfOrderCheck {
		fmt.Printf("🔍 Starting out-of-order article scan...\n")
		fmt.Printf("=====================================\n")
		err := scanOutOfOrderOverview(db, newsgroups, *verbose)
		if err != nil {
			log.Fatalf("Out-of-order scan failed: %v", err)
		}
		os.Exit(0)
	}

	// If reorder-by-datesent is requested, run that and exit
	if *reorderByDateSent {
		fmt.Printf("🔄 Starting article reordering by date_sent...\n")
		fmt.Printf("=====================================\n")
		err := reorderArticlesByDateSent(db, newsgroups, *verbose, *maxPar)
		if err != nil {
			log.Fatalf("Article reordering failed: %v", err)
		}
		os.Exit(0)
	}

	// Initialize tracking variables
	var results []GroupResult
	var (
		totalGroups        = len(newsgroups)
		emptyGroups        = 0
		cleanGroups        = 0
		inconsistentGroups = 0
		repairedGroups     = 0
		failedGroups       = 0
		errorGroups        = 0
	)

	// Process each newsgroup
	for i, newsgroup := range newsgroups {
		fmt.Printf("🔍 [%d/%d] Checking newsgroup: %s\n", i+1, totalGroups, newsgroup.Name)
		result := GroupResult{Name: newsgroup.Name}

		// Run consistency check
		report, err := db.CheckDatabaseConsistency(newsgroup.Name)
		if err != nil {
			result.Status = "ERROR"
			result.ErrorMessage = err.Error()
			results = append(results, result)
			errorGroups++
			fmt.Printf("❌ Failed to check database consistency: %v\n\n", err)
			continue
		}

		// Print the report
		report.PrintReport()
		result.ArticleCount = report.ArticleCount
		result.InitialInconsistencies = report.HasInconsistencies

		// Skip empty newsgroups
		if report.ArticleCount == 0 && report.OverviewCount == 0 && report.ThreadCount == 0 {
			result.Status = "EMPTY"
			results = append(results, result)
			emptyGroups++
			fmt.Printf("📭 Empty newsgroup, skipping...\n\n")
			continue
		}

		if *repair && report.HasInconsistencies {
			fmt.Printf("🔧 Starting repair process...\n")
			fmt.Printf("📋 Issues detected:\n")

			// Show what specific problems we found
			if len(report.OrphanedThreads) > 0 {
				fmt.Printf("   🧵 %d orphaned thread entries\n", len(report.OrphanedThreads))
			}
			if len(report.OrphanedOverviews) > 0 {
				fmt.Printf("   📋 %d orphaned overview entries\n", len(report.OrphanedOverviews))
			}
			if len(report.MissingOverviews) > 0 {
				fmt.Printf("   ❌ %d missing overview entries\n", len(report.MissingOverviews))
			}
			if report.ArticleCount != report.OverviewCount {
				fmt.Printf("   📊 Article/overview count mismatch: %d articles vs %d overviews\n", report.ArticleCount, report.OverviewCount)
			}
			fmt.Printf("\n")

			if err := repairDatabase(db, newsgroup.Name, report); err != nil {
				result.Status = "FAILED"
				result.ErrorMessage = err.Error()
				results = append(results, result)
				failedGroups++
				fmt.Fprintf(os.Stderr, "❌ Repair failed for '%s': %v\n\n", newsgroup.Name, err)
				continue // Continue to next newsgroup instead of exiting
			}
			fmt.Printf("🔍 Repair completed. Re-running consistency check...\n\n")

			// Optionally rebuild threads after repair if there were thread-related issues
			if len(report.OrphanedThreads) > 0 {
				fmt.Printf("🧵 Rebuilding thread relationships after repair...\n")
				threadReport, err := db.RebuildThreadsFromScratch(newsgroup.Name, *verbose, nil)
				if err != nil {
					fmt.Printf("❌ Failed to rebuild threads: %v\n", err)
				} else {
					fmt.Printf("✅ Thread rebuild completed: %d threads rebuilt from %d articles\n",
						threadReport.ThreadsRebuilt, threadReport.TotalArticles)
				}
			}

			// Re-run consistency check after repair
			report, err = db.CheckDatabaseConsistency(newsgroup.Name)
			if err != nil {
				result.Status = "ERROR"
				result.ErrorMessage = "Failed to re-check after repair: " + err.Error()
				//results = append(results, result)
				errorGroups++
				if report != nil {
					report.PrintReport()
				}
				fmt.Fprintf(os.Stderr, "❌ Failed to re-check consistency for '%s': %v\n\n", newsgroup.Name, err)
				os.Exit(1) // !!! WE EXIT HERE!!! DO NOT CHANGE !!!
			}
			report.PrintReport()
			result.FinalInconsistencies = report.HasInconsistencies
		}

		if report.HasInconsistencies {
			result.Status = "INCONSISTENT"
			results = append(results, result)
			inconsistentGroups++

			// Show specific unfixable issues
			fmt.Printf("⚠️  Database still has inconsistencies after repair:\n")
			if !*repair {
				fmt.Printf("   💡 Run with -repair flag to attempt fixes\n")
			} else {
				fmt.Printf("   🔍 Issues found that couldn't be automatically fixed:\n")
				if report.ArticleCount != report.OverviewCount {
					fmt.Printf("      📊 Count mismatch: %d articles ≠ %d overviews\n", report.ArticleCount, report.OverviewCount)
				}
				if report.ArticlesMaxNum != report.OverviewMaxNum {
					fmt.Printf("      📈 Max number mismatch: articles=%d, overviews=%d\n", report.ArticlesMaxNum, report.OverviewMaxNum)
				}
				if len(report.OrphanedThreads) > 0 {
					fmt.Printf("      🧵 %d orphaned threads still remain\n", len(report.OrphanedThreads))
				}
				if len(report.OrphanedOverviews) > 0 {
					fmt.Printf("      📋 %d orphaned overviews still remain\n", len(report.OrphanedOverviews))
				}
				if len(report.MissingOverviews) > 0 {
					fmt.Printf("      ❌ %d overviews still missing\n", len(report.MissingOverviews))
				}
				fmt.Printf("   💭 This may require manual database inspection or rebuilding\n")
			}
			fmt.Printf("   ⏭️  Continuing to next group...\n\n")
			continue // Continue to next newsgroup instead of exiting
		}

		if result.InitialInconsistencies && !report.HasInconsistencies {
			result.Status = "REPAIRED"
			repairedGroups++
		} else {
			result.Status = "OK"
			cleanGroups++
		}
		results = append(results, result)
		fmt.Printf("✅ Database consistency check completed successfully.\n\n")
	}

	// Print comprehensive summary
	printSummary(results, totalGroups, emptyGroups, cleanGroups, inconsistentGroups, repairedGroups, failedGroups, errorGroups, *repair)
}

// repairDatabase attempts to repair detected inconsistencies
func repairDatabase(db *database.Database, newsgroup string, report *database.ConsistencyReport) error {
	fmt.Printf("Starting repair process for %s...\n", newsgroup)

	// Get group databases
	groupDB, err := db.GetGroupDBs(newsgroup)
	if err != nil {
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDB.Return(db)

	repairCount := 0

	// Count all possible repairs
	totalIssues := len(report.OrphanedThreads) + len(report.OrphanedOverviews) + len(report.MissingOverviews)

	// Add count mismatches as repairable
	if report.ArticleCount != report.OverviewCount {
		totalIssues++
	}
	if report.ArticlesMaxNum != report.OverviewMaxNum {
		totalIssues++
	}
	if report.MainDBLastArticle != report.ArticlesMaxNum {
		totalIssues++
	}

	if totalIssues == 0 {
		fmt.Printf("No repairable inconsistencies detected\n")
		return fmt.Errorf("no automated repair available for detected inconsistencies")
	}

	fmt.Printf("Found %d repairable issues, attempting repairs...\n", totalIssues)

	// Begin transaction for all repairs
	tx, err := groupDB.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Repair 1: Remove orphaned threads
	if len(report.OrphanedThreads) > 0 {
		fmt.Printf("🧵 Removing %d orphaned thread entries...\n", len(report.OrphanedThreads))
		removedThreads := 0
		for i, threadArticle := range report.OrphanedThreads {
			result, err := tx.Exec("DELETE FROM threads WHERE root_article = ?", threadArticle)
			if err != nil {
				fmt.Printf("   ⚠️  Warning: Could not delete orphaned thread %d: %v\n", threadArticle, err)
				continue
			}
			if affected, _ := result.RowsAffected(); affected > 0 {
				removedThreads++
				repairCount++
			}
			if i < 5 {
				fmt.Printf("   ✅ Removed thread root_article=%d\n", threadArticle)
			} else if i == 5 {
				fmt.Printf("   ✅ ... and %d more\n", len(report.OrphanedThreads)-5)
			}
		}
		fmt.Printf("   🎯 Successfully removed %d/%d orphaned thread entries\n\n", removedThreads, len(report.OrphanedThreads))
	}

	// Repair 2: Remove orphaned overview entries (no longer needed with unified schema)
	if len(report.OrphanedOverviews) > 0 {
		fmt.Printf("📋 Skipping orphaned overview removal (unified schema)...\n\n")
	}

	// Repair 3: Create missing overview entries (no longer needed with unified schema)
	if len(report.MissingOverviews) > 0 {
		fmt.Printf("📝 Skipping missing overview creation (unified schema)...\n\n")
	}

	// Repair 4: Fix main database last_article mismatch
	if report.MainDBLastArticle != report.ArticlesMaxNum && report.ArticlesMaxNum > 0 {
		fmt.Printf("🔧 Fixing main DB last_article mismatch (%d → %d)...\n", report.MainDBLastArticle, report.ArticlesMaxNum)
		_, err := db.GetMainDB().Exec("UPDATE newsgroups SET last_article = ? WHERE name = ?", report.ArticlesMaxNum, newsgroup)
		if err != nil {
			fmt.Printf("   ❌ Failed to update main DB: %v\n", err)
		} else {
			fmt.Printf("   ✅ Main database last_article updated\n")
			repairCount++
		}
	}

	// Repair 5: Overview count mismatch analysis no longer needed with unified schema
	if report.ArticleCount != report.OverviewCount {
		fmt.Printf("📊 Skipping article/overview count analysis (unified schema)...\n\n")
	}

	// Repair 6: Check for articles without proper thread association
	fmt.Printf("🧵 Checking threading consistency...\n")
	var articlesWithoutThreading int64
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM articles a
		WHERE NOT EXISTS (
			SELECT 1 FROM threads t
			WHERE t.root_article = a.article_num
			   OR (t.child_articles IS NOT NULL AND t.child_articles LIKE '%,' || a.article_num || ',%')
		)
	`).Scan(&articlesWithoutThreading)

	if err != nil {
		fmt.Printf("   ❌ Could not check threading: %v\n", err)
	} else if articlesWithoutThreading > 0 {
		fmt.Printf("   ⚠️  Found %d articles without proper thread association\n", articlesWithoutThreading)
		fmt.Printf("   💡 Consider running full threading rebuild for optimal performance\n")
	} else {
		fmt.Printf("   ✅ All articles properly associated with threads\n")
	}
	fmt.Printf("\n")

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit repairs: %w", err)
	}

	fmt.Printf("🎉 Repair completed: %s (%d fixes applied)\n", newsgroup, repairCount)
	return nil
}

/*
// createMissingOverview creates a missing overview entry from the corresponding article
func createMissingOverview(groupDB *database.GroupDBs, articleNum int64) error {
	var article struct {
		MessageID  string
		Subject    string
		FromHeader string
		DateSent   string
		DateString string
		References string
		Bytes      int
		Lines      int
		ReplyCount int
	}

	// Get article data
	err := database.RetryableQueryRowScan(groupDB.DB, `
		SELECT message_id, subject, from_header, date_sent, date_string, references, bytes, lines, reply_count
		FROM articles WHERE article_num = ?
	`, []interface{}{articleNum}, &article.MessageID, &article.Subject, &article.FromHeader,
		&article.DateSent, &article.DateString, &article.References,
		&article.Bytes, &article.Lines, &article.ReplyCount)
	if err != nil {
		return fmt.Errorf("failed to get article data: %w", err)
	}

	// Overview functionality now integrated into articles table - no separate insert needed
	// With unified schema, articles already contain all overview data
	return nil
}

// createMissingOverviewTx creates a missing overview entry from the corresponding article within a transaction
func createMissingOverviewTx(tx *sql.Tx, articleNum int64) error {
	var article struct {
		MessageID  string
		Subject    string
		FromHeader string
		DateSent   string
		DateString string
		References string
		Bytes      int
		Lines      int
		ReplyCount int
	}

	// Get article data
	err := tx.QueryRow(`
		SELECT message_id, subject, from_header, date_sent, date_string, references, bytes, lines, reply_count
		FROM articles WHERE article_num = ?
	`, articleNum).Scan(&article.MessageID, &article.Subject, &article.FromHeader,
		&article.DateSent, &article.DateString, &article.References,
		&article.Bytes, &article.Lines, &article.ReplyCount)
	if err != nil {
		return fmt.Errorf("failed to get article data: %w", err)
	}

	// Overview functionality now integrated into articles table - no separate insert needed
	// With unified schema, articles already contain all overview data
	return nil
}

// updateMainDBStats updates the main database newsgroup statistics
func updateMainDBStats(db *database.Database, newsgroup string, report *database.ConsistencyReport) error {
	// With unified schema, only articles table max matters
	maxArticle := report.ArticlesMaxNum

	// Update main database newsgroup entry
	_, err := db.GetMainDB().Exec(`
		UPDATE newsgroups
		SET last_article = ?, message_count = ?, high_water = ?, updated_at = datetime('now')
		WHERE name = ?
	`, maxArticle, report.ArticleCount, maxArticle, newsgroup)

	return err
}
*/

// printSummary prints a comprehensive summary of all database operations
func printSummary(results []GroupResult, totalGroups, emptyGroups, cleanGroups, inconsistentGroups, repairedGroups, failedGroups, errorGroups int, repairMode bool) {
	fmt.Printf("\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("DATABASE RECOVERY SUMMARY\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("Total newsgroups processed: %d\n", totalGroups)
	fmt.Printf("Empty groups (skipped):     %d\n", emptyGroups)
	fmt.Printf("Clean groups:               %d\n", cleanGroups)
	fmt.Printf("Successfully repaired:      %d\n", repairedGroups)
	fmt.Printf("Still inconsistent:         %d\n", inconsistentGroups)
	fmt.Printf("Repair failed:              %d\n", failedGroups)
	fmt.Printf("Check errors:               %d\n", errorGroups)
	fmt.Printf("\n")

	// Calculate percentages
	nonEmptyGroups := totalGroups - emptyGroups
	if nonEmptyGroups > 0 {
		successRate := float64(cleanGroups+repairedGroups) / float64(nonEmptyGroups) * 100
		fmt.Printf("Success rate (non-empty):   %.1f%%\n", successRate)
	}

	// List problematic groups
	problemGroups := 0
	var inconsistentList []string
	var failedList []string
	var errorList []string

	for _, result := range results {
		switch result.Status {
		case "INCONSISTENT":
			inconsistentList = append(inconsistentList, result.Name)
			problemGroups++
		case "FAILED":
			failedList = append(failedList, fmt.Sprintf("%s (%s)", result.Name, result.ErrorMessage))
			problemGroups++
		case "ERROR":
			errorList = append(errorList, fmt.Sprintf("%s (%s)", result.Name, result.ErrorMessage))
			problemGroups++
		}
	}

	if problemGroups > 0 {
		fmt.Printf("\n🚨 WARNING: %d GROUPS HAVE PROBLEMS!\n", problemGroups)
		fmt.Printf("=====================================\n")

		if len(inconsistentList) > 0 {
			fmt.Printf("\n⚠️  INCONSISTENT GROUPS (%d):\n", len(inconsistentList))
			for i, group := range inconsistentList {
				fmt.Printf("   %d. %s\n", i+1, group)
				if i >= 19 && len(inconsistentList) > 20 {
					fmt.Printf("   ... and %d more\n", len(inconsistentList)-20)
					break
				}
			}
		}

		if len(failedList) > 0 {
			fmt.Printf("\n❌ REPAIR FAILED GROUPS (%d):\n", len(failedList))
			for i, group := range failedList {
				fmt.Printf("   %d. %s\n", i+1, group)
				if i >= 19 && len(failedList) > 20 {
					fmt.Printf("   ... and %d more\n", len(failedList)-20)
					break
				}
			}
		}

		if len(errorList) > 0 {
			fmt.Printf("\n💥 CHECK ERROR GROUPS (%d):\n", len(errorList))
			for i, group := range errorList {
				fmt.Printf("   %d. %s\n", i+1, group)
				if i >= 19 && len(errorList) > 20 {
					fmt.Printf("   ... and %d more\n", len(errorList)-20)
					break
				}
			}
		}

		fmt.Printf("\n🚨 URGENT ACTION REQUIRED!\n")
		if !repairMode {
			fmt.Printf("Run with -repair flag to attempt automatic fixes.\n")
		} else {
			fmt.Printf("Manual intervention may be required for remaining issues.\n")
		}
		fmt.Printf("=====================================\n")
	} else {
		if nonEmptyGroups > 0 {
			fmt.Printf("\n✅ ALL NON-EMPTY DATABASES ARE HEALTHY!\n")
		} else {
			fmt.Printf("\n📭 ALL DATABASES ARE EMPTY\n")
		}
		fmt.Printf("=====================================\n")
	}

	// Detailed breakdown for large sets
	if totalGroups > 10 {
		fmt.Printf("\nDETAILED BREAKDOWN:\n")
		statusCounts := make(map[string]int)
		for _, result := range results {
			statusCounts[result.Status]++
		}

		for status, count := range statusCounts {
			percentage := float64(count) / float64(totalGroups) * 100
			emoji := getStatusEmoji(status)
			fmt.Printf("  %s %-12s: %4d groups (%.1f%%)\n", emoji, status, count, percentage)
		}
	}

	// Exit with appropriate code
	fmt.Printf("\n")
	if problemGroups > 0 {
		fmt.Printf("⚠️  Exiting with code 1 due to database problems\n")
		os.Exit(1)
	} else {
		fmt.Printf("✅ Exiting with code 0 - all databases healthy\n")
		os.Exit(0)
	}
}

// getStatusEmoji returns appropriate emoji for each status
func getStatusEmoji(status string) string {
	switch status {
	case "OK":
		return "✅"
	case "EMPTY":
		return "📭"
	case "REPAIRED":
		return "🔧"
	case "INCONSISTENT":
		return "⚠️ "
	case "FAILED":
		return "❌"
	case "ERROR":
		return "💥"
	default:
		return "❓"
	}
}

// DateProblem captures information about a problematic date
type DateProblem struct {
	Newsgroup   *string
	ArticleNum  int64
	MessageID   *string
	DateString  *string
	StoredDate  time.Time
	ParsedDate  time.Time
	Difference  time.Duration
	ParseFailed bool
}

// checkAndFixDates analyzes date_string vs date_sent mismatches and optionally fixes them
func checkAndFixDates(db *database.Database, newsgroups []*models.Newsgroup, rewriteDates, verbose bool) (int64, int64, error) {
	var totalFixed, totalChecked int64
	var allProblems []DateProblem

	for i, newsgroup := range newsgroups {
		fmt.Printf("📅 [%d/%d] Checking dates in newsgroup: %s\n", i+1, len(newsgroups), newsgroup.Name)

		groupDB, err := db.GetGroupDBs(newsgroup.Name)
		if err != nil {
			fmt.Printf("   ❌ Failed to get group database: %v\n", err)
			continue
		}

		fixed, checked, problems, err := checkGroupDates(groupDB, newsgroup.Name, rewriteDates, verbose)
		if err != nil {
			fmt.Printf("   ❌ Failed to check dates: %v\n", err)
			groupDB.Return(db)
			continue
		}

		totalFixed += fixed
		totalChecked += checked
		allProblems = append(allProblems, problems...)
		groupDB.Return(db)

		if checked > 0 {
			if fixed > 0 {
				fmt.Printf("   📊 Checked: %d articles, Found: %d mismatches", checked, fixed)
				if rewriteDates {
					fmt.Printf(", Fixed: %d\n", fixed)
				} else {
					fmt.Printf("\n")
				}
			} else {
				fmt.Printf("   ✅ Checked: %d articles, no date mismatches found\n", checked)
			}
		} else {
			fmt.Printf("   📭 No articles to check\n")
		}
	}

	// Print summary of all problematic dates
	if len(allProblems) > 0 {
		printDateProblemsSummary(allProblems, rewriteDates)
	}

	return totalFixed, totalChecked, nil
}

// checkGroupDates checks and optionally fixes date mismatches in a single newsgroup
func checkGroupDates(groupDB *database.GroupDBs, newsgroupName string, rewriteDates, verbose bool) (int64, int64, []DateProblem, error) {
	// Query all articles with their date information - get date_sent as string to avoid timezone parsing issues
	rows, err := database.RetryableQuery(groupDB.DB, `
		SELECT article_num, message_id, date_string, date_sent
		FROM articles
		WHERE date_string IS NOT NULL AND date_string != ''
		ORDER BY article_num
	`)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to query articles: %w", err)
	}
	defer rows.Close()

	var fixed, checked int64
	var problems []DateProblem
	var tx *sql.Tx

	if rewriteDates {
		tx, err = groupDB.DB.Begin()
		if err != nil {
			return 0, 0, nil, fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()
	}

	for rows.Next() {
		var articleNum int64
		var messageID, dateString, dateSentStr string

		err := rows.Scan(&articleNum, &messageID, &dateString, &dateSentStr)
		if err != nil {
			if verbose {
				fmt.Printf("   ⚠️  Error scanning article %d: %v\n", articleNum, err)
			}
			continue
		}

		checked++

		// print progress every N
		if checked%database.RescanBatchSize == 0 {
			fmt.Printf("   📊 Checked %d articles so far...\n", checked)
		}

		// Parse the stored date string (should be in UTC format)
		var dateSent time.Time
		dateSent, err = time.Parse("2006-01-02 15:04:05", dateSentStr)
		if err != nil {
			// Try RFC3339 format as fallback
			dateSent, err = time.Parse(time.RFC3339, dateSentStr)
			if err != nil {
				if verbose {
					fmt.Printf("   ⚠️  Article %d: Could not parse stored date_sent '%s': %v\n", articleNum, dateSentStr, err)
				}
				continue
			}
		}

		// Re-parse the original date string using the fixed parser
		reparsedDate := processor.ParseNNTPDate(dateString)
		if reparsedDate.IsZero() {
			problem := DateProblem{
				Newsgroup:   &newsgroupName,
				ArticleNum:  articleNum,
				MessageID:   &messageID,
				DateString:  &dateString,
				StoredDate:  dateSent,
				ParseFailed: true,
			}
			problems = append(problems, problem)

			if verbose {
				fmt.Printf("   ⚠️  Article %d: Could not re-parse date string '%s' stored dateSent='%s'\n", articleNum, dateString, dateSent)
			}

			// Safety check: stop scanning if we have too many problems
			/*
				if len(problems) > 10000 {
					fmt.Printf("   🚨 WARNING: Found more than 10,000 date problems! Stopping scan to prevent memory issues.\n")
					fmt.Printf("   📊 This indicates a systematic date parsing problem that needs investigation.\n")
					break
				}
				continue
			*/
		}

		// Compare the re-parsed date with the stored date (both in UTC)
		// Allow for small differences (within 1 second) due to precision issues
		dateSentUTC := dateSent.UTC()
		reparsedDateUTC := reparsedDate.UTC()
		diff := reparsedDateUTC.Sub(dateSentUTC)
		if diff < 0 {
			diff = -diff
		}

		if diff > time.Second {
			fixed++
			problem := DateProblem{
				Newsgroup:  &newsgroupName,
				ArticleNum: articleNum,
				MessageID:  &messageID,
				DateString: &dateString,
				StoredDate: dateSent,
				ParsedDate: reparsedDate,
				Difference: diff,
			}
			problems = append(problems, problem)

			if verbose {
				fmt.Printf("   🔧 Article %d (%s): '%s' stored as %v, should be %v (diff: %v)\n",
					articleNum, messageID, dateString, dateSent.Format(time.RFC3339), reparsedDate.Format(time.RFC3339), diff)
			}

			if rewriteDates {
				// Format as UTC string to avoid timezone encoding issues
				reparsedDateStr := reparsedDate.UTC().Format("2006-01-02 15:04:05")
				_, err := tx.Exec("UPDATE articles SET date_sent = ?, hide = 0, spam = 0 WHERE article_num = ?", reparsedDateStr, articleNum)
				if err != nil {
					if verbose {
						fmt.Printf("   ❌ Failed to update article %d: %v\n", articleNum, err)
					}
					continue
				}
			}

			// Safety check: stop scanning if we have too many problems
			if len(problems) > 10000 {
				fmt.Printf("   🚨 WARNING: Found more than 10,000 date problems! Stopping scan to prevent memory issues.\n")
				fmt.Printf("   📊 This indicates a systematic date parsing problem that needs investigation.\n")
				break
			}
		}
	}

	if err = rows.Err(); err != nil {
		return fixed, checked, problems, fmt.Errorf("error iterating rows: %w", err)
	}

	if rewriteDates && tx != nil {
		if err := tx.Commit(); err != nil {
			return fixed, checked, problems, fmt.Errorf("failed to commit date fixes: %w", err)
		}
	}

	return fixed, checked, problems, nil
}

// printDateProblemsSummary prints a comprehensive summary of all date problems found
func printDateProblemsSummary(problems []DateProblem, rewriteDates bool) {
	if len(problems) == 0 {
		return
	}

	fmt.Printf("\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("DATE PROBLEMS SUMMARY\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("Total problematic dates found: %d\n", len(problems))

	// Categorize problems
	var parseFailures []DateProblem
	var dateMismatches []DateProblem

	for _, problem := range problems {
		if problem.ParseFailed {
			parseFailures = append(parseFailures, problem)
		} else {
			dateMismatches = append(dateMismatches, problem)
		}
	}

	// Print parse failures
	if len(parseFailures) > 0 {
		fmt.Printf("\n❌ UNPARSEABLE DATE STRINGS (%d):\n", len(parseFailures))
		fmt.Printf("These date strings could not be parsed at all:\n")

		// Group by unique date strings to avoid repetition
		uniqueFailures := make(map[string][]DateProblem)
		for _, failure := range parseFailures {
			uniqueFailures[*failure.DateString] = append(uniqueFailures[*failure.DateString], failure)
		}

		count := 0
		for dateStr, failures := range uniqueFailures {
			count++
			fmt.Printf("   %d. \"%s\" (found %d times)\n", count, dateStr, len(failures))
			if len(failures) <= 5 {
				for _, failure := range failures {
					fmt.Printf("      → %s article %d (%s)\n", *failure.Newsgroup, failure.ArticleNum, *failure.MessageID)
				}
			} else {
				for i := 0; i < 3; i++ {
					fmt.Printf("      → %s article %d (%s)\n", *failures[i].Newsgroup, failures[i].ArticleNum, *failures[i].MessageID)
				}
				fmt.Printf("      → ... and %d more occurrences\n", len(failures)-3)
			}
		}
	}

	// Print date mismatches
	if len(dateMismatches) > 0 {
		fmt.Printf("\n🔧 DATE MISMATCHES (%d):\n", len(dateMismatches))
		if rewriteDates {
			fmt.Printf("These dates were corrected:\n")
		} else {
			fmt.Printf("These dates need correction (run with -rewritedates to fix):\n")
		}

		// Show worst offenders first (largest time differences)
		sortedMismatches := make([]DateProblem, len(dateMismatches))
		copy(sortedMismatches, dateMismatches)

		// Simple bubble sort by difference (descending)
		for i := 0; i < len(sortedMismatches)-1; i++ {
			for j := 0; j < len(sortedMismatches)-i-1; j++ {
				if sortedMismatches[j].Difference < sortedMismatches[j+1].Difference {
					sortedMismatches[j], sortedMismatches[j+1] = sortedMismatches[j+1], sortedMismatches[j]
				}
			}
		}

		for i, problem := range sortedMismatches {
			fmt.Printf("   %d. %s article %d (%s)\n", i+1, *problem.Newsgroup, problem.ArticleNum, *problem.MessageID)
			fmt.Printf("      Date string: \"%s\"\n", *problem.DateString)
			fmt.Printf("      Stored as:   %s\n", problem.StoredDate.Format(time.RFC3339))
			fmt.Printf("      Should be:   %s\n", problem.ParsedDate.Format(time.RFC3339))
			fmt.Printf("      Difference:  %v\n", problem.Difference)
			fmt.Printf("\n")
		}
	}

	// Group problems by newsgroup for statistics
	groupStats := make(map[string]int)
	for _, problem := range problems {
		groupStats[*problem.Newsgroup]++
	}

	if len(groupStats) > 1 {
		fmt.Printf("\nPROBLEMS BY NEWSGROUP:\n")
		for group, count := range groupStats {
			fmt.Printf("   %s: %d problems\n", group, count)
		}
	}

	fmt.Printf("\n💡 RECOMMENDATIONS:\n")
	if len(parseFailures) > 0 {
		fmt.Printf("   • Add missing date layouts to NNTPDateLayouts in proc-utils.go\n")
		fmt.Printf("   • Use 'go run cmd/parsedates/main.go \"date string\"' to test parsing\n")
	}
	if len(dateMismatches) > 0 && !rewriteDates {
		fmt.Printf("   • Run with -rewritedates flag to fix date mismatches\n")
	}
	fmt.Printf("=====================================\n")
}

// OutOfOrderArticle represents an article that has an out-of-order date_sent
type OutOfOrderArticle struct {
	Newsgroup      string
	ArticleNum     int64
	DateSent       time.Time
	PreviousArtNum int64
	PreviousDate   time.Time
}

// scanOutOfOrderOverview scans newsgroups for articles with out-of-order date_sent values
func scanOutOfOrderOverview(db *database.Database, newsgroups []*models.Newsgroup, verbose bool) error {
	const batchSize = 10000
	var totalArticles int64
	var totalOutOfOrder int64
	var allProblems []OutOfOrderArticle

	for _, newsgroup := range newsgroups {
		fmt.Printf("\n📊 Scanning newsgroup: %s\n", newsgroup.Name)

		// Get newsgroup database
		groupDBs, err := db.GetGroupDBs(newsgroup.Name)
		if err != nil {
			fmt.Printf("❌ Failed to get database for '%s': %v\n", newsgroup.Name, err)
			continue
		}
		if groupDBs == nil || groupDBs.DB == nil {
			fmt.Printf("⚠️  No database found for '%s', skipping...\n", newsgroup.Name)
			continue
		}

		// Get total article count
		var count int64
		err = groupDBs.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&count)
		if err != nil {
			fmt.Printf("❌ Failed to count articles: %v\n", err)
			continue
		}

		if count == 0 {
			fmt.Printf("📭 Empty newsgroup, skipping...\n")
			continue
		}

		fmt.Printf("   Total articles: %d\n", count)
		totalArticles += count

		var groupProblems []OutOfOrderArticle
		var lastArticleNum int64
		var lastDateSent time.Time
		offset := int64(0)
		query := `
				SELECT article_num, date_sent
				FROM articles
				ORDER BY article_num ASC
				LIMIT ? OFFSET ?
			`
		for {
			// Query articles ordered by date_sent ASC using OFFSET pagination

			rows, err := groupDBs.DB.Query(query, batchSize, offset)
			if err != nil {
				return fmt.Errorf("failed to query articles at offset %d: %v", offset, err)
			}

			articlesInBatch := 0
			for rows.Next() {
				var articleNum int64
				var dateSentStr string

				if err := rows.Scan(&articleNum, &dateSentStr); err != nil {
					rows.Close()
					return fmt.Errorf("failed to scan article: %v", err)
				}

				// Parse date_sent
				dateSent, err := time.Parse("2006-01-02 15:04:05", dateSentStr)
				if err != nil {
					rows.Close()
					return fmt.Errorf("failed to parse date_sent '%s': %v", dateSentStr, err)
				}

				// Check if this article's date is before the previous one
				if offset > 0 || articlesInBatch > 0 {
					if dateSent.Before(lastDateSent) {
						problem := OutOfOrderArticle{
							Newsgroup:      newsgroup.Name,
							ArticleNum:     articleNum,
							DateSent:       dateSent,
							PreviousArtNum: lastArticleNum,
							PreviousDate:   lastDateSent,
						}
						groupProblems = append(groupProblems, problem)
						allProblems = append(allProblems, problem)
					}
				}

				lastArticleNum = articleNum
				lastDateSent = dateSent
				articlesInBatch++
			}
			rows.Close()

			if articlesInBatch == 0 {
				break
			}

			offset += int64(articlesInBatch)

			if verbose && offset%100000 == 0 {
				fmt.Printf("   Progress: %d/%d articles scanned...\n", offset, count)
			}
		}

		if len(groupProblems) > 0 {
			fmt.Printf("⚠️  Found %d out-of-order articles in %s\n", len(groupProblems), newsgroup.Name)
			totalOutOfOrder += int64(len(groupProblems))

			// Show first few problems
			showCount := len(groupProblems)
			if showCount > 10 {
				showCount = 10
			}
			for i := 0; i < showCount; i++ {
				p := groupProblems[i]
				fmt.Printf("   • Article %d (%s) comes AFTER article %d (%s)\n",
					p.ArticleNum, p.DateSent.Format("2006-01-02 15:04:05"),
					p.PreviousArtNum, p.PreviousDate.Format("2006-01-02 15:04:05"))
			}
			if len(groupProblems) > 10 {
				fmt.Printf("   ... and %d more out-of-order articles\n", len(groupProblems)-10)
			}
		} else {
			fmt.Printf("✅ All articles are in chronological order by date_sent\n")
		}
	}

	// Print summary
	fmt.Printf("\n=====================================\n")
	fmt.Printf("📊 OUT-OF-ORDER SCAN SUMMARY\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("Total articles scanned: %d\n", totalArticles)
	fmt.Printf("Out-of-order articles:  %d\n", totalOutOfOrder)
	if totalOutOfOrder > 0 {
		fmt.Printf("\n⚠️  WARNING: Found articles with dates that don't match their chronological position!\n")
		fmt.Printf("This can cause issues with keyset pagination in nntp-transfer.\n")
		fmt.Printf("\n💡 RECOMMENDATIONS:\n")
		fmt.Printf("   • Review the import process for late-arriving articles\n")
		fmt.Printf("   • Consider using article_num-based pagination for affected newsgroups\n")
		fmt.Printf("   • The stuck article detection in nntp-transfer should handle these cases\n")
	} else {
		fmt.Printf("\n✅ All articles are properly ordered by date_sent across all newsgroups!\n")
	}
	fmt.Printf("=====================================\n")

	return nil
}

// reorderArticlesByDateSent reorders articles by date_sent and writes to a new database
func reorderArticlesByDateSent(db *database.Database, newsgroups []*models.Newsgroup, verbose bool, maxPar int) error {
	fmt.Printf("Processing %d newsgroups with max parallelism: %d\n\n", len(newsgroups), maxPar)

	parChan := make(chan struct{}, maxPar)
	var wg sync.WaitGroup
	var totalArticles, totalNewsgroups int64
	var mu sync.Mutex

	for i, ng := range newsgroups {
		parChan <- struct{}{} // acquire lock
		wg.Add(1)

		go func(ng *models.Newsgroup, index int) {
			defer func() {
				<-parChan // release lock
				wg.Done()
			}()

			fmt.Printf("🔄 [%d/%d] Processing: %s\n", index+1, len(newsgroups), ng.Name)

			articleCount, err := reorderSingleNewsgroup(db, ng.Name, verbose)
			if err != nil {
				fmt.Printf("❌ [%d/%d] Failed to reorder %s: %v\n", index+1, len(newsgroups), ng.Name, err)
				return
			}

			mu.Lock()
			totalArticles += articleCount
			if articleCount > 0 {
				totalNewsgroups++
			}
			mu.Unlock()

			if articleCount > 0 {
				fmt.Printf("✅ [%d/%d] Completed: %s (%d articles)\n", index+1, len(newsgroups), ng.Name, articleCount)
			} else {
				fmt.Printf("⚠️  [%d/%d] Skipped: %s (no articles)\n", index+1, len(newsgroups), ng.Name)
			}
		}(ng, i)
	}

	wg.Wait()

	fmt.Printf("\n=====================================\n")
	fmt.Printf("📊 REORDER SUMMARY\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("Newsgroups processed: %d\n", totalNewsgroups)
	fmt.Printf("Total articles reordered: %d\n", totalArticles)
	fmt.Printf("=====================================\n")

	return nil
}

// reorderSingleNewsgroup reorders articles in a single newsgroup by date_sent
func reorderSingleNewsgroup(db *database.Database, newsgroupName string, verbose bool) (int64, error) {
	// Open source database
	sourceDB, err := db.GetGroupDBs(newsgroupName)
	if err != nil {
		return 0, fmt.Errorf("failed to open source database: %w", err)
	}
	defer db.ForceCloseGroupDBs(sourceDB)

	// Count articles in source database
	var totalArticles int64
	err = sourceDB.DB.QueryRow("SELECT COUNT(*) FROM articles WHERE date_sent IS NOT NULL").Scan(&totalArticles)
	if err != nil {
		return 0, fmt.Errorf("failed to count articles: %w", err)
	}

	if totalArticles == 0 {
		return 0, nil // No articles to process
	}

	// Open destination database with .new suffix
	destDB, destPath, err := db.GetGroupDBsWithSuffix(newsgroupName, ".new")
	if err != nil {
		return 0, fmt.Errorf("failed to create destination database: %w", err)
	}
	defer database.CloseGroupDBDirectly(destDB)

	if verbose {
		fmt.Printf("   Source: %s\n", sourceDB.Newsgroup)
		fmt.Printf("   Dest:   %s\n", destPath)
		fmt.Printf("   Articles to reorder: %d\n", totalArticles)
	}

	// Process articles in batches to avoid memory issues with large newsgroups
	const batchSize = 1000
	var newArticleNum int64 = 1
	var processed int64
	var offset int64 = 0

	// Create a temporary newsgroup model for GetArticlesBatchWithDateFilter
	tempNG := &models.Newsgroup{
		Name: newsgroupName,
	}

	// Prepare insert statement for reuse across batches
	insertSQL := `INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
	                                    "references", bytes, lines, reply_count, path, headers_json, body_text,
	                                    imported_at, spam, hide)
	              VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	for offset < totalArticles {
		// Use existing GetArticlesBatchWithDateFilter function to fetch articles
		// Pass nil for startTime/endTime to get all articles, ordered by date_sent
		articles, err := db.GetArticlesBatchWithDateFilter(tempNG, offset, nil, nil, batchSize)
		if err != nil {
			return processed, fmt.Errorf("failed to query source database at offset %d: %w", offset, err)
		}

		// Break if no articles returned
		if len(articles) == 0 {
			break
		}

		// Begin transaction for this batch
		tx, err := destDB.Begin()
		if err != nil {
			return processed, fmt.Errorf("failed to begin transaction at offset %d: %w", offset, err)
		}

		stmt, err := tx.Prepare(insertSQL)
		if err != nil {
			tx.Rollback()
			return processed, fmt.Errorf("failed to prepare insert statement: %w", err)
		}

		// Process batch
		batchCount := 0
		for _, article := range articles {
			if article == nil {
				continue
			}

			// Determine spam/hide flags (these are stored as integers in DB)
			spam := 0
			hide := 0
			// Note: Article model doesn't have Spam/Hide fields, so we default to 0

			// Insert with new sequential article_num
			_, err = stmt.Exec(
				newArticleNum, // new sequential article_num
				article.MessageID,
				article.Subject,
				article.FromHeader,
				article.DateSent,
				article.DateString,
				article.References,
				article.Bytes,
				article.Lines,
				article.ReplyCount,
				article.Path,
				article.HeadersJSON,
				article.BodyText,
				article.ImportedAt,
				spam,
				hide,
			)
			if err != nil {
				stmt.Close()
				tx.Rollback()
				return processed, fmt.Errorf("failed to insert article %d: %w", newArticleNum, err)
			}

			newArticleNum++
			processed++
			batchCount++
		}

		stmt.Close()

		// Commit batch transaction
		if err = tx.Commit(); err != nil {
			return processed, fmt.Errorf("failed to commit transaction at offset %d: %w", offset, err)
		}

		if verbose {
			fmt.Printf("   Progress: %d/%d articles (batch: %d)\n", processed, totalArticles, batchCount)
		}

		// Break if we got fewer articles than batch size (end of data)
		if batchCount < int(batchSize) {
			break
		}

		offset += int64(batchCount)
	}

	// Close destination database before rebuilding threads
	if err := database.CloseGroupDBDirectly(destDB); err != nil {
		log.Printf("Warning: failed to close destination database: %v", err)
	}

	// Rebuild threads in the new database
	if verbose {
		fmt.Printf("   Rebuilding thread relationships...\n")
	}

	// Reopen the destination database for thread rebuild
	destDB, err = sql.Open("sqlite3", destPath)
	if err != nil {
		return processed, fmt.Errorf("failed to reopen destination database for thread rebuild: %w", err)
	}
	defer destDB.Close()

	// Create a temporary GroupDBs wrapper for the new database
	tempGroupDBs := &database.GroupDBs{
		Newsgroup: newsgroupName,
		DB:        destDB,
	}

	// Rebuild threads using the existing RebuildThreadsFromScratch function
	var report *database.ThreadRebuildReport
	report, err = db.RebuildThreadsFromScratch(newsgroupName, verbose, tempGroupDBs)
	if err != nil {
		return processed, fmt.Errorf("failed to rebuild threads: %w", err)
	}

	if verbose {
		fmt.Printf("   Thread rebuild complete: %d threads from %d articles\n",
			report.ThreadsRebuilt, report.TotalArticles)
	}

	return processed, nil
}
