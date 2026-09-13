// News article expiration tool for go-pugleaf
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

var appVersion = "-unset-"

// showUsageExamples displays usage examples for expiring articles
func showUsageExamples() {
	fmt.Println("\n=== News Article Expiration Tool ===")
	fmt.Println("This tool expires (deletes) old articles from newsgroups based on age.")
	fmt.Println()
	fmt.Println("Basic Usage:")
	fmt.Println("  ./expire-news -group '$all' -days 30")
	fmt.Println("  ./expire-news -group news.admin.peering -days 90")
	fmt.Println("  ./expire-news -group alt.* -days 7")
	fmt.Println("  ./expire-news -group comp.* -days 60")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -group: Newsgroup(s) to expire ('$all' for all groups, prefix.* for wildcard)")
	fmt.Println("  -days: Delete articles older than N days (required unless using -prune)")
	fmt.Println("  -dry-run: Show what would be deleted without actually deleting")
	fmt.Println("  -batch-size: Number of articles to process per batch (default: 1000)")
	fmt.Println("  -respect-expiry: Honor per-group expiry_days settings from database")
	fmt.Println("  -prune: Remove oldest articles to respect max_articles limit per group")
	fmt.Println("  -trim-history: Also remove deleted articles from the message-id history index")
	fmt.Println("                 (default: history entries are kept, so expired articles are still")
	fmt.Println("                 rejected as duplicates when offered again)")
	fmt.Println()
	fmt.Println("Safety Features:")
	fmt.Println("  - Always runs in dry-run mode first unless -force is specified")
	fmt.Println("  - Respects per-group expiry_days settings when -respect-expiry is used")
	fmt.Println("  - Respects per-group max_articles settings when -prune is used")
	fmt.Println("  - Processes articles in batches to avoid memory issues")
	fmt.Println("  - Updates newsgroup counters after successful operations")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  # Dry run: see what would be expired in all groups older than 30 days")
	fmt.Println("  ./expire-news -group '$all' -days 30 -dry-run")
	fmt.Println()
	fmt.Println("  # Actually expire articles in alt.* groups older than 7 days")
	fmt.Println("  ./expire-news -group 'alt.*' -days 7 -force")
	fmt.Println()
	fmt.Println("  # Use per-group expiry settings from database")
	fmt.Println("  ./expire-news -group '$all' -respect-expiry -force")
	fmt.Println()
	fmt.Println("  # Prune groups to respect max_articles limit")
	fmt.Println("  ./expire-news -group '$all' -prune -force")
	fmt.Println()
	fmt.Println("  # Combine expiry and pruning")
	fmt.Println("  ./expire-news -group '$all' -days 30 -prune -force")
	fmt.Println()
	fmt.Println("  # Expire and forget the message-ids of deleted articles in the history index")
	fmt.Println("  ./expire-news -group '$all' -days 30 -force -trim-history")
	fmt.Println()
}

func main() {
	config.AppVersion = appVersion
	database.NO_CACHE_BOOT = true // prevents booting caches
	log.Printf("Starting go-pugleaf News Expiration Tool (version %s)", appVersion)
	// Command line flags
	var (
		targetGroup   = flag.String("group", "", "Newsgroup to expire ('$all', specific group, or wildcard like news.*)")
		expireDays    = flag.Int("days", 0, "Delete articles older than N days (0 = use per-group settings)")
		dryRun        = flag.Bool("dry-run", false, "Show what would be deleted without actually deleting")
		force         = flag.Bool("force", false, "Actually perform deletions (required for non-dry-run)")
		batchSize     = flag.Int("batch-size", 1000, "Number of articles to process per batch")
		respectExpiry = flag.Bool("respect-expiry", false, "Use per-group expiry_days settings from database")
		prune         = flag.Bool("prune", false, "Remove oldest articles to respect max_articles limit per group")
		trimHistory   = flag.Bool("trim-history", false, "Remove deleted articles from the history index (default: keep history entries)")
		showHelp      = flag.Bool("help", false, "Show usage examples and exit")
		dataDir       = flag.String("data", "./data", "Directory to store database files")
	)
	flag.Parse()

	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}

	// Validation
	if *targetGroup == "" {
		log.Fatal("Error: -group flag is required. Use -help for examples.")
	}

	if !*respectExpiry && *expireDays <= 0 && !*prune {
		log.Fatal("Error: -days must be > 0, or use -respect-expiry, or use -prune to use database settings")
	}

	if *batchSize <= 0 || *batchSize > 10000 {
		log.Fatal("Error: -batch-size must be between 1 and 10000")
	}

	// Safety check: require -force for actual deletions
	if !*dryRun && !*force {
		log.Fatal("Error: Must specify -force to actually delete articles, or use -dry-run to preview")
	}

	// Initialize database
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = *dataDir

	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// History index: only opened for -trim-history in live mode
	var hist *history.History
	if *trimHistory && *dryRun {
		log.Printf("DRY RUN MODE: -trim-history has no effect")
	}
	if *trimHistory && !*dryRun {
		history.ENABLE_HISTORY = true
		hcfg := history.DefaultConfig()
		hcfg.HistoryDir = filepath.Join(*dataDir, "history")
		hist, err = history.NewHistory(hcfg, db.WG)
		if err != nil {
			log.Printf("Failed to open history: %v", err)
			shutdown(db, nil)
			os.Exit(1)
		}
		log.Printf("TRIM HISTORY: deleted articles will be removed from the history index")
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	// Get newsgroups to process
	newsgroups, err := getNewsgroupsToExpire(db, *targetGroup)
	if err != nil {
		log.Printf("Failed to get newsgroups: %v", err)
		shutdown(db, hist)
		os.Exit(1)
	}

	if len(newsgroups) == 0 {
		log.Printf("No newsgroups found matching pattern: %s", *targetGroup)
		shutdown(db, hist)
		return
	}

	log.Printf("Found %d newsgroups to process", len(newsgroups))
	if *dryRun {
		log.Printf("DRY RUN MODE: No articles will actually be deleted")
	} else {
		log.Printf("LIVE MODE: Articles will be permanently deleted")
	}

	if *prune {
		log.Printf("PRUNE MODE: Will respect max_articles limits")
	}
	if *expireDays > 0 || *respectExpiry {
		log.Printf("EXPIRY MODE: Will remove old articles")
	}

	// Process each newsgroup
	totalExpired := 0
	totalScanned := 0
	errorCount := 0
	interrupted := false

groupLoop:
	for i, ng := range newsgroups {
		select {
		case <-sigChan:
			log.Printf("Received shutdown signal, stopping...")
			interrupted = true
			break groupLoop
		default:
		}

		// history trimming needs the main DB newsgroup ID
		trim := historyTrim{hist: hist}
		if hist != nil {
			mng, err := db.MainDBGetNewsgroup(ng.Name)
			if err != nil || mng.ID <= 0 {
				log.Printf("Error: can not resolve newsgroup ID of %s for -trim-history, skipping group: %v", ng.Name, err)
				errorCount++
				continue
			}
			trim.groupID = mng.ID
		}

		// Determine what operations to perform
		var operations []string
		var totalExpiredInGroup, totalScannedInGroup int

		// 1. First handle expiry by date if requested
		if (*expireDays > 0) || (*respectExpiry && ng.ExpiryDays > 0) {
			// Determine expiry days for this group
			effectiveExpireDays := *expireDays
			if *respectExpiry && ng.ExpiryDays > 0 {
				effectiveExpireDays = ng.ExpiryDays
				log.Printf("\n[%d/%d] Processing %s expiry (using group setting: %d days)",
					i+1, len(newsgroups), ng.Name, effectiveExpireDays)
			} else if *respectExpiry && ng.ExpiryDays == 0 {
				log.Printf("\n[%d/%d] Skipping %s expiry (no expiry days set)",
					i+1, len(newsgroups), ng.Name)
				effectiveExpireDays = 0
			} else {
				log.Printf("\n[%d/%d] Processing %s expiry (using command line: %d days)",
					i+1, len(newsgroups), ng.Name, effectiveExpireDays)
			}

			if effectiveExpireDays > 0 {
				// Calculate cutoff date
				cutoffDate := time.Now().AddDate(0, 0, -effectiveExpireDays)
				log.Printf("Expiring articles older than: %s", cutoffDate.Format("2006-01-02 15:04:05"))

				// Expire articles in this group
				expired, scanned, err := expireArticlesInGroup(db, ng.Name, cutoffDate, *batchSize, *dryRun, trim)
				if err != nil {
					log.Printf("Error expiring articles in %s: %v", ng.Name, err)
					errorCount++
					continue
				}

				totalExpiredInGroup += expired
				totalScannedInGroup += scanned
				operations = append(operations, fmt.Sprintf("expired %d (age)", expired))
			}
		}

		// 2. Then handle pruning by count if requested
		if *prune && ng.MaxArticles > 0 {
			log.Printf("Pruning %s to max %d articles", ng.Name, ng.MaxArticles)

			pruned, scanned, err := pruneArticlesInGroup(db, ng.Name, ng.MaxArticles, *batchSize, *dryRun, trim)
			if err != nil {
				log.Printf("Error pruning articles in %s: %v", ng.Name, err)
				errorCount++
				continue
			}

			totalExpiredInGroup += pruned
			totalScannedInGroup += scanned
			operations = append(operations, fmt.Sprintf("pruned %d (count)", pruned))
		} else if *prune && ng.MaxArticles == 0 {
			log.Printf("Skipping %s pruning (no max_articles limit set)", ng.Name)
		}

		// 3. Update newsgroup counters after processing
		if !*dryRun && totalExpiredInGroup > 0 {
			err := updateNewsgroupCounters(db, ng.Name)
			if err != nil {
				log.Printf("Warning: failed to update counters for %s: %v", ng.Name, err)
			}
		}

		// Report results
		if len(operations) == 0 {
			log.Printf("\n[%d/%d] No operations performed on %s", i+1, len(newsgroups), ng.Name)
		} else {
			operationStr := strings.Join(operations, ", ")
			if *dryRun {
				log.Printf("Would process %s: %s (scanned %d)", ng.Name, operationStr, totalScannedInGroup)
			} else {
				log.Printf("Processed %s: %s (scanned %d)", ng.Name, operationStr, totalScannedInGroup)
			}
		}

		totalExpired += totalExpiredInGroup
		totalScanned += totalScannedInGroup
	}

	// Summary
	log.Printf("\n=== Expiration Summary ===")
	if *dryRun {
		log.Printf("Would process %d articles total (scanned %d articles)", totalExpired, totalScanned)
		log.Printf("Use -force to actually perform deletions")
	} else {
		log.Printf("Processed %d articles total (scanned %d articles)", totalExpired, totalScanned)
		log.Printf("Database counters have been updated for affected newsgroups")
	}
	if interrupted {
		log.Printf("Interrupted by shutdown signal")
	}

	if !shutdown(db, hist) {
		errorCount++
	}
	if errorCount > 0 {
		log.Printf("Completed with %d errors", errorCount)
		os.Exit(1)
	}
	if interrupted {
		os.Exit(130)
	}
}

var shutdownOnce sync.Once

// shutdown closes the history index (flushes queued removals) and then the database.
// OpenDatabase adds the batch orchestrators to db.WG, so StopChan must be closed and WG waited for.
func shutdown(db *database.Database, hist *history.History) (ok bool) {
	ok = true
	shutdownOnce.Do(func() {
		if hist != nil {
			if err := hist.Close(); err != nil {
				log.Printf("Failed to close history: %v", err)
				ok = false
			}
			hs := hist.GetStats()
			log.Printf("History closed: removes=%d committed=%d errors=%d", hs.TotalRemoves, hs.TotalCommitted, hs.Errors)
			if hs.Errors > 0 {
				ok = false
			}
		}
		close(db.StopChan)
		db.WG.Wait()
		if err := db.Shutdown(); err != nil {
			log.Printf("Failed to shutdown database: %v", err)
			ok = false
		}
	})
	return ok
}

// historyTrim carries what deleteArticles needs to remove deleted articles from the history index.
// hist == nil: history entries are kept (default).
type historyTrim struct {
	hist    *history.History
	groupID int64
}

// getNewsgroupsToExpire returns newsgroups matching the target pattern
func getNewsgroupsToExpire(db *database.Database, targetGroup string) ([]*models.Newsgroup, error) {
	var newsgroups []*models.Newsgroup
	var err error

	// Handle different target patterns
	switch {
	case targetGroup == "$all":
		// Get all active newsgroups
		newsgroups, err = db.MainDBGetAllNewsgroups()
		if err != nil {
			return nil, fmt.Errorf("failed to get all newsgroups: %v", err)
		}
		// Filter to only active groups
		var activeGroups []*models.Newsgroup
		for _, ng := range newsgroups {
			nga, err := db.GetActiveNewsgroupByName(ng.Name)
			if err == nil && nga != nil && nga.Active {
				activeGroups = append(activeGroups, ng)
			}
		}
		newsgroups = activeGroups

	case strings.HasSuffix(targetGroup, "*"):
		// Wildcard pattern
		prefix := strings.TrimSuffix(targetGroup, "*")
		allGroups, err := db.MainDBGetAllNewsgroups()
		if err != nil {
			return nil, fmt.Errorf("failed to get newsgroups for wildcard: %v", err)
		}

		for _, ng := range allGroups {
			if strings.HasPrefix(ng.Name, prefix) {
				nga, err := db.GetActiveNewsgroupByName(ng.Name)
				if err == nil && nga != nil && nga.Active {
					newsgroups = append(newsgroups, ng)
				}
			}
		}

	default:
		// Specific group
		nga, err := db.GetActiveNewsgroupByName(targetGroup)
		if err != nil {
			return nil, fmt.Errorf("failed to get newsgroup '%s': %v", targetGroup, err)
		}
		if nga == nil || !nga.Active {
			return nil, fmt.Errorf("newsgroup '%s' not found or inactive", targetGroup)
		}
		newsgroups = append(newsgroups, &models.Newsgroup{
			Name:       targetGroup,
			ExpiryDays: nga.ExpiryDays,
		})
	}

	return newsgroups, nil
}

// expireArticlesInGroup expires articles older than cutoffDate in the specified group
func expireArticlesInGroup(db *database.Database, groupName string, cutoffDate time.Time, batchSize int, dryRun bool, trim historyTrim) (int, int, error) {
	// Get group database
	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get group database: %v", err)
	}
	defer groupDB.Return()

	totalExpired := 0
	totalScanned := 0
	noDate := 0 // articles without a valid date_sent are never expired by age
	cutoffTimestamp := cutoffDate.Unix()
	defer func() {
		if noDate > 0 {
			log.Printf("  %s: skipped %d articles without a valid date_sent", groupName, noDate)
		}
	}()
	// Process articles in batches using keyset paging on article_num
	// (OFFSET paging would skip rows because we delete between pages)
	var lastArticleNum int64 = 0
	for {
		// Get batch of articles with article_num > lastArticleNum
		articles, err := getArticleBatch(groupDB, lastArticleNum, batchSize)
		if err != nil {
			return totalExpired, totalScanned, fmt.Errorf("failed to get article batch: %v", err)
		}

		if len(articles) == 0 {
			break // No more articles
		}

		// Process this batch
		expiredInBatch := 0
		var articlesToDelete []int64

		for _, article := range articles {
			totalScanned++

			if !article.DateSent.Valid {
				noDate++
				continue
			}
			// Check if article is older than cutoff (using DateSent instead of PostedAt)
			if article.DateSent.Time.Unix() < cutoffTimestamp {
				expiredInBatch++
				articlesToDelete = append(articlesToDelete, article.Num)

				if len(articlesToDelete)%100 == 0 { // Log every 100 deletions
					if dryRun {
						log.Printf("  Would delete articles up to ID %d...", article.Num)
					} else {
						log.Printf("  Deleting articles up to ID %d...", article.Num)
					}
				}
			}
		}
		// Rows are ordered by article_num, so the last one is the highest seen
		lastArticleNum = articles[len(articles)-1].Num

		// Delete articles in this batch if not dry run
		if !dryRun && len(articlesToDelete) > 0 {
			if err := deleteArticles(groupDB, articlesToDelete, trim); err != nil {
				return totalExpired, totalScanned, fmt.Errorf("failed to delete articles: %v", err)
			}
		}

		totalExpired += expiredInBatch

		// Progress update
		if totalScanned%10000 == 0 {
			log.Printf("  Processed %d articles, expired %d so far...", totalScanned, totalExpired)
		}

		// If we got fewer articles than requested, we're done
		if len(articles) < batchSize {
			break
		}
	}

	return totalExpired, totalScanned, nil
}

// expireCandidate is an article number with its date_sent, used by the age-based expiry scan
type expireCandidate struct {
	Num      int64
	DateSent sql.NullTime // NULL date_sent: Valid == false
}

// getArticleBatch retrieves up to limit articles with article_num > afterNum from the group database
func getArticleBatch(groupDB *database.GroupDB, afterNum int64, limit int) ([]expireCandidate, error) {
	query := `
		SELECT article_num, date_sent
		FROM articles
		WHERE article_num > ?
		ORDER BY article_num
		LIMIT ?
	`

	rows, err := database.RetryableQuery(groupDB.DB, query, afterNum, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []expireCandidate
	for rows.Next() {
		var c expireCandidate
		var dateSent interface{}
		if err := rows.Scan(&c.Num, &dateSent); err != nil {
			return nil, err
		}
		c.DateSent = toNullTime(dateSent)
		articles = append(articles, c)
	}

	return articles, rows.Err()
}

// dateSentLayouts are tried for date_sent values the sqlite driver did not convert to time.Time
var dateSentLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// toNullTime converts a scanned date_sent value. NULL, empty or unparseable values are not Valid.
func toNullTime(v interface{}) sql.NullTime {
	var s string
	switch t := v.(type) {
	case time.Time:
		return sql.NullTime{Time: t, Valid: !t.IsZero()}
	case string:
		s = t
	case []byte:
		s = string(t)
	case int64:
		return sql.NullTime{Time: time.Unix(t, 0), Valid: t > 0}
	default:
		return sql.NullTime{}
	}
	s = strings.TrimSpace(s)
	for _, layout := range dateSentLayouts {
		if tm, err := time.Parse(layout, s); err == nil {
			return sql.NullTime{Time: tm, Valid: !tm.IsZero()}
		}
	}
	return sql.NullTime{}
}

// deleteArticles removes articles from the database using proper batch operations.
// With trim.hist set, the message-ids of the deleted articles are removed from the history index
// after the transaction has been committed.
func deleteArticles(groupDB *database.GroupDB, articleNums []int64, trim historyTrim) error {
	if len(articleNums) == 0 {
		return nil
	}

	// Begin transaction
	tx, err := groupDB.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var messageIDs []string // only collected with trim.hist

	// Process in chunks to avoid SQLite parameter limits (max ~32k parameters)
	const maxChunkSize = 5000 // Stay well under SQLite limits

	for i := 0; i < len(articleNums); i += maxChunkSize {
		end := i + maxChunkSize
		if end > len(articleNums) {
			end = len(articleNums)
		}

		chunk := articleNums[i:end]

		// Create placeholders for this chunk
		placeholders := getPlaceholders(len(chunk))

		// Convert int64 slice to interface{} slice for SQL args
		args := make([]interface{}, len(chunk))
		for j, num := range chunk {
			args[j] = num
		}

		if trim.hist != nil {
			ids, err := selectMessageIDs(tx, placeholders, args)
			if err != nil {
				return err
			}
			messageIDs = append(messageIDs, ids...)
		}

		// Delete from articles table (main table)
		query := fmt.Sprintf("DELETE FROM articles WHERE article_num IN (%s)", placeholders)
		_, err = tx.Exec(query, args...)
		if err != nil {
			return fmt.Errorf("failed to batch delete articles: %v", err)
		}

		// Delete from overview table if it exists (might not exist in unified schema)
		query = fmt.Sprintf("DELETE FROM overview WHERE article_num IN (%s)", placeholders)
		_, err = tx.Exec(query, args...)
		if err != nil {
			// Don't fail if overview table doesn't exist
			log.Printf("Warning: failed to delete overview batch (table may not exist): %v", err)
		}

		// Delete related thread entries using OR conditions for all relationships
		// This is more complex as we need to check multiple columns
		threadQuery := fmt.Sprintf(
			"DELETE FROM threads WHERE root_article IN (%s) OR child_article IN (%s) OR parent_article IN (%s)",
			placeholders, placeholders, placeholders)

		// We need to repeat args 3 times for the 3 IN clauses
		threadArgs := make([]interface{}, len(args)*3)
		copy(threadArgs[0:len(args)], args)
		copy(threadArgs[len(args):len(args)*2], args)
		copy(threadArgs[len(args)*2:], args)

		_, err = tx.Exec(threadQuery, threadArgs...)
		if err != nil {
			// Don't fail if threads table doesn't exist
			log.Printf("Warning: failed to delete thread entries (table may not exist): %v", err)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return err
	}
	if trim.hist != nil {
		for _, messageID := range messageIDs {
			trim.hist.RemoveArticle(messageID, trim.groupID)
		}
	}
	return nil
}

// selectMessageIDs returns the message-ids of the articles in one delete chunk (inside the delete transaction)
func selectMessageIDs(tx *sql.Tx, placeholders string, args []interface{}) ([]string, error) {
	rows, err := tx.Query(fmt.Sprintf("SELECT article_num, message_id FROM articles WHERE article_num IN (%s)", placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to select message-ids: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var num int64
		var messageID sql.NullString
		if err := rows.Scan(&num, &messageID); err != nil {
			return nil, fmt.Errorf("failed to scan message-id: %v", err)
		}
		if messageID.Valid && messageID.String != "" {
			ids = append(ids, messageID.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read message-ids: %v", err)
	}
	return ids, nil
}

// getPlaceholders returns a comma-separated string of SQL placeholders (?) for the given count
func getPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	if count == 1 {
		return "?"
	}
	// Simple and efficient: use strings.Repeat with Join
	return strings.Repeat("?, ", count-1) + "?"
}

// pruneArticlesInGroup removes oldest articles to keep the group under maxArticles limit
func pruneArticlesInGroup(db *database.Database, groupName string, maxArticles int, batchSize int, dryRun bool, trim historyTrim) (int, int, error) {
	// Get group database
	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get group database: %v", err)
	}
	defer groupDB.Return()

	// First count total articles
	var totalArticles int
	err = database.RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", nil, &totalArticles)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count articles: %v", err)
	}

	if totalArticles <= maxArticles {
		log.Printf("Group %s has %d articles (under limit of %d)", groupName, totalArticles, maxArticles)
		return 0, totalArticles, nil
	}

	articlesToRemove := totalArticles - maxArticles
	log.Printf("Group %s has %d articles, need to remove %d to stay under limit of %d",
		groupName, totalArticles, articlesToRemove, maxArticles)

	// Get oldest articles to remove (by article_num, which generally corresponds to age)
	query := `
		SELECT article_num
		FROM articles
		ORDER BY article_num ASC
		LIMIT ?
	`

	rows, err := database.RetryableQuery(groupDB.DB, query, articlesToRemove)
	if err != nil {
		return 0, totalArticles, fmt.Errorf("failed to query oldest articles: %v", err)
	}
	defer rows.Close()

	var articlesToDelete []int64
	for rows.Next() {
		var articleNum int64
		err := rows.Scan(&articleNum)
		if err != nil {
			return 0, totalArticles, fmt.Errorf("failed to scan article number: %v", err)
		}
		articlesToDelete = append(articlesToDelete, articleNum)
	}

	if err = rows.Err(); err != nil {
		return 0, totalArticles, fmt.Errorf("error reading article results: %v", err)
	}

	// Delete articles in batches if not dry run
	totalPruned := 0
	if !dryRun && len(articlesToDelete) > 0 {
		// Process in smaller batches to avoid transaction size issues
		for i := 0; i < len(articlesToDelete); i += batchSize {
			end := i + batchSize
			if end > len(articlesToDelete) {
				end = len(articlesToDelete)
			}

			batch := articlesToDelete[i:end]
			if err := deleteArticles(groupDB, batch, trim); err != nil {
				return totalPruned, totalArticles, fmt.Errorf("failed to delete article batch: %v", err)
			}

			totalPruned += len(batch)

			// Progress update for large deletions
			if len(articlesToDelete) > 1000 && totalPruned%1000 == 0 {
				log.Printf("  Pruned %d/%d articles so far...", totalPruned, len(articlesToDelete))
			}
		}
	} else {
		totalPruned = len(articlesToDelete)
	}

	return totalPruned, totalArticles, nil
}

// updateNewsgroupCounters updates the message count and last article number for a newsgroup
func updateNewsgroupCounters(db *database.Database, groupName string) error {
	// Get group database to count current articles
	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group database: %v", err)
	}
	defer groupDB.Return()

	// Count current articles
	var messageCount int64
	err = database.RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", nil, &messageCount)
	if err != nil {
		return fmt.Errorf("failed to count articles: %v", err)
	}

	// Get the highest article number
	var lastArticle int64
	err = database.RetryableQueryRowScan(groupDB.DB, "SELECT COALESCE(MAX(article_num), 0) FROM articles", nil, &lastArticle)
	if err != nil {
		return fmt.Errorf("failed to get last article: %v", err)
	}

	// Update the main newsgroups table
	_, err = db.GetMainDB().Exec(`
		UPDATE newsgroups SET message_count = ?, last_article = ? WHERE name = ?`,
		messageCount, lastArticle, groupName)
	if err != nil {
		return fmt.Errorf("failed to update newsgroup counters: %v", err)
	}

	log.Printf("Updated %s counters: %d articles, last article %d", groupName, messageCount, lastArticle)
	return nil
}
