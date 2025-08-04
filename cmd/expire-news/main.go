// News article expiration tool for go-pugleaf
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
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
}

func main() {
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
		hostnamePath  = flag.String("nntphostname", "", "Your hostname (required)")
		showHelp      = flag.Bool("help", false, "Show usage examples and exit")
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

	if *hostnamePath == "" {
		log.Fatal("Error: -nntphostname flag is required")
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

	// Load config
	mainConfig := config.NewDefaultConfig()
	appVersion = mainConfig.AppVersion
	mainConfig.Server.Hostname = *hostnamePath

	// Initialize database
	db, err := database.OpenDatabase(nil)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer func() {
		if err := db.Shutdown(); err != nil {
			log.Printf("Failed to shutdown database: %v", err)
		}
	}()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	// Get newsgroups to process
	newsgroups, err := getNewsgroupsToExpire(db, *targetGroup)
	if err != nil {
		log.Fatalf("Failed to get newsgroups: %v", err)
	}

	if len(newsgroups) == 0 {
		log.Printf("No newsgroups found matching pattern: %s", *targetGroup)
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

	for i, ng := range newsgroups {
		select {
		case <-sigChan:
			log.Printf("Received shutdown signal, stopping...")
			return
		default:
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
				expired, scanned, err := expireArticlesInGroup(db, ng.Name, cutoffDate, *batchSize, *dryRun)
				if err != nil {
					log.Printf("Error expiring articles in %s: %v", ng.Name, err)
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

			pruned, scanned, err := pruneArticlesInGroup(db, ng.Name, ng.MaxArticles, *batchSize, *dryRun)
			if err != nil {
				log.Printf("Error pruning articles in %s: %v", ng.Name, err)
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
func expireArticlesInGroup(db *database.Database, groupName string, cutoffDate time.Time, batchSize int, dryRun bool) (int, int, error) {
	// Get group database
	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get group database: %v", err)
	}
	defer groupDBs.Return(db)

	totalExpired := 0
	totalScanned := 0
	cutoffTimestamp := cutoffDate.Unix()

	// Process articles in batches
	offset := 0
	for {
		// Get batch of articles
		articles, err := getArticleBatch(groupDBs, offset, batchSize)
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

			// Check if article is older than cutoff (using DateSent instead of PostedAt)
			if article.DateSent.Unix() < cutoffTimestamp {
				expiredInBatch++
				articlesToDelete = append(articlesToDelete, article.ArticleNum)

				if len(articlesToDelete) > 100 { // Log every 100 deletions
					if dryRun {
						log.Printf("  Would delete articles up to ID %d...", article.ArticleNum)
					} else {
						log.Printf("  Deleting articles up to ID %d...", article.ArticleNum)
					}
				}
			}
		}

		// Delete articles in this batch if not dry run
		if !dryRun && len(articlesToDelete) > 0 {
			if err := deleteArticles(groupDBs, articlesToDelete); err != nil {
				return totalExpired, totalScanned, fmt.Errorf("failed to delete articles: %v", err)
			}
		}

		totalExpired += expiredInBatch
		offset += len(articles)

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

// getArticleBatch retrieves a batch of articles from the group database
func getArticleBatch(groupDBs *database.GroupDBs, offset, limit int) ([]*models.Article, error) {
	query := `
		SELECT article_num, date_sent
		FROM articles
		ORDER BY article_num
		LIMIT ? OFFSET ?
	`

	rows, err := groupDBs.DB.Query(query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []*models.Article
	for rows.Next() {
		article := &models.Article{}
		err := rows.Scan(&article.ArticleNum, &article.DateSent)
		if err != nil {
			return nil, err
		}
		articles = append(articles, article)
	}

	return articles, rows.Err()
}

// deleteArticles removes articles from the database using proper batch operations
func deleteArticles(groupDBs *database.GroupDBs, articleNums []int64) error {
	if len(articleNums) == 0 {
		return nil
	}

	// Begin transaction
	tx, err := groupDBs.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

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
	return tx.Commit()
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
func pruneArticlesInGroup(db *database.Database, groupName string, maxArticles int, batchSize int, dryRun bool) (int, int, error) {
	// Get group database
	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get group database: %v", err)
	}
	defer groupDBs.Return(db)

	// First count total articles
	var totalArticles int
	err = groupDBs.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&totalArticles)
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

	rows, err := groupDBs.DB.Query(query, articlesToRemove)
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
			if err := deleteArticles(groupDBs, batch); err != nil {
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
	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group database: %v", err)
	}
	defer groupDBs.Return(db)

	// Count current articles
	var messageCount int64
	err = groupDBs.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&messageCount)
	if err != nil {
		return fmt.Errorf("failed to count articles: %v", err)
	}

	// Get the highest article number
	var lastArticle int64
	err = groupDBs.DB.QueryRow("SELECT COALESCE(MAX(article_num), 0) FROM articles").Scan(&lastArticle)
	if err != nil {
		return fmt.Errorf("failed to get last article: %v", err)
	}

	// Update the main newsgroups table
	_, err = db.GetMainDB().Exec(`
		UPDATE newsgroups
		SET message_count = ?, last_article = ?, updated_at = CURRENT_TIMESTAMP
		WHERE name = ?`,
		messageCount, lastArticle, groupName)
	if err != nil {
		return fmt.Errorf("failed to update newsgroup counters: %v", err)
	}

	log.Printf("Updated %s counters: %d articles, last article %d", groupName, messageCount, lastArticle)
	return nil
}
