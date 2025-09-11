package database

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// RecoverDatabase attempts to recover the database by checking for missing articles and last_insert_ids mismatches
var RescanBatchSize int64 = 25000

func (db *Database) Rescan(newsgroup string) error {
	if newsgroup == "" {
		return nil // Nothing to rescan
	}
	// first look into the maindb newsgroups table and get the latest numbers
	latest, err := db.GetLatestArticleNumbers(newsgroup)
	if err != nil {
		return err
	}
	// open groupDBs
	groupDB, err := db.GetGroupDBs(newsgroup)
	if err != nil {
		return err
	}
	defer groupDB.Return(db)
	// Get the latest article number from the groupDB
	latestArticle, err := db.GetLatestArticleNumberFromOverview(newsgroup)
	if err != nil {
		return err
	}
	// Compare with the latest from the mainDB
	if latestArticle > latest[newsgroup] {
		log.Printf("Found new articles in group '%s': %d (latest: %d)", newsgroup, latestArticle, latest[newsgroup])
		// TODO: Handle new articles (e.g., fetch and insert into mainDB)
	}
	return nil
}

func (db *Database) GetLatestArticleNumberFromOverview(newsgroup string) (int64, error) {
	// Since overview table is unified with articles, query articles table instead
	groupDB, err := db.GetGroupDBs(newsgroup)
	if err != nil {
		return 0, err
	}
	defer groupDB.Return(db)

	var latestArticle int64
	err = retryableQueryRowScan(groupDB.DB, `
		SELECT MAX(article_num)
		FROM articles
	`, []interface{}{}, &latestArticle)
	if err != nil {
		return 0, err
	}

	return latestArticle, nil
}

func (db *Database) GetLatestArticleNumbers(newsgroup string) (map[string]int64, error) {
	// Query the latest article numbers for the specified newsgroup
	rows, err := retryableQuery(db.GetMainDB(), `
		SELECT name, last_article
		FROM newsgroups
		WHERE name = ?
	`, newsgroup)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	latest := make(map[string]int64)
	for rows.Next() {
		var group string
		var lastID int64
		if err := rows.Scan(&group, &lastID); err != nil {
			return nil, err
		}
		latest[group] = lastID
	}

	return latest, nil
}

// ConsistencyReport represents the results of a database consistency check
type ConsistencyReport struct {
	Newsgroup           string
	MainDBLastArticle   int64
	ArticlesMaxNum      int64
	OverviewMaxNum      int64
	ThreadsMaxNum       int64
	ArticleCount        int64
	OverviewCount       int64
	ThreadCount         int64
	MissingArticles     []int64
	MissingOverviews    []int64
	OrphanedOverviews   []int64 // New: overview entries without articles
	OrphanedThreads     []int64
	MessageIDMismatches []string
	Errors              []string
	HasInconsistencies  bool
}

// CheckDatabaseConsistency performs a comprehensive consistency check for a newsgroup
func (db *Database) CheckDatabaseConsistency(newsgroup string) (*ConsistencyReport, error) {
	report := &ConsistencyReport{
		Newsgroup:           newsgroup,
		MissingArticles:     []int64{},
		MissingOverviews:    []int64{},
		OrphanedOverviews:   []int64{},
		OrphanedThreads:     []int64{},
		MessageIDMismatches: []string{},
		Errors:              []string{},
	}

	// 1. Get main DB newsgroup info
	mainDBInfo, err := db.GetLatestArticleNumbers(newsgroup)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get main DB info: %v", err))
		return report, nil
	}
	if lastArticle, exists := mainDBInfo[newsgroup]; exists {
		report.MainDBLastArticle = lastArticle
	}

	// 2. Get group databases
	groupDB, err := db.GetGroupDBs(newsgroup)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get group databases: %v", err))
		return report, nil
	}
	defer groupDB.Return(db)

	// 3. Get max article numbers from each table (handle NULL for empty tables)
	err = retryableQueryRowScan(groupDB.DB, "SELECT COALESCE(MAX(article_num), 0) FROM articles", []interface{}{}, &report.ArticlesMaxNum)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get max article_num from articles: %v", err))
	}

	// Since overview is now unified with articles, OverviewMaxNum equals ArticlesMaxNum
	report.OverviewMaxNum = report.ArticlesMaxNum

	err = retryableQueryRowScan(groupDB.DB, "SELECT COALESCE(MAX(root_article), 0) FROM threads", []interface{}{}, &report.ThreadsMaxNum)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get max root_article from threads: %v", err))
	}

	// 4. Get counts from each table
	err = retryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", []interface{}{}, &report.ArticleCount)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get article count: %v", err))
	}

	// Since overview is now unified with articles, OverviewCount equals ArticleCount
	report.OverviewCount = report.ArticleCount

	err = retryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM threads", []interface{}{}, &report.ThreadCount)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get thread count: %v", err))
	}

	// 5. Find missing articles (gaps in article numbering)
	report.MissingArticles = db.findMissingArticles(groupDB, report.ArticlesMaxNum)

	// Since overview is unified with articles, there are no missing or orphaned overviews
	report.MissingOverviews = []int64{}  // No longer needed
	report.OrphanedOverviews = []int64{} // No longer needed
	//report.OrphanedOverviews = db.findOrphanedOverviews(groupDB)

	// 8. Find orphaned threads (threads pointing to non-existent articles)
	report.OrphanedThreads = db.findOrphanedThreads(groupDB)

	// Since overview is unified with articles, no message ID mismatches are possible
	report.MessageIDMismatches = []string{} // No longer needed

	// 10. Determine if there are inconsistencies (simplified for unified schema)
	report.HasInconsistencies = len(report.MissingArticles) > 0 ||
		len(report.OrphanedThreads) > 0 ||
		len(report.Errors) > 0 ||
		report.MainDBLastArticle != report.ArticlesMaxNum
		// Removed overview-related checks since overview is unified with articles

	return report, nil
}

// findMissingArticles finds gaps in article numbering using batched processing
func (db *Database) findMissingArticles(groupDB *GroupDBs, maxArticleNum int64) []int64 {
	var missing []int64
	if maxArticleNum <= 0 {
		return missing
	}

	var offset int64 = 0
	var totalProcessed int64 = 0

	log.Printf("Checking for missing articles in batches of %d (max article: %d)", RescanBatchSize, maxArticleNum)

	for offset < maxArticleNum {
		// Get batch of article numbers
		rows, err := retryableQuery(groupDB.DB,
			"SELECT article_num FROM articles WHERE article_num > ? ORDER BY article_num LIMIT ?",
			offset, RescanBatchSize)
		if err != nil {
			log.Printf("Error fetching article batch starting at %d: %v", offset, err)
			break
		}

		var batchArticles []int64
		for rows.Next() {
			var num int64
			if err := rows.Scan(&num); err != nil {
				continue
			}
			batchArticles = append(batchArticles, num)
		}
		rows.Close()

		if len(batchArticles) == 0 {
			break // No more articles
		}

		// Find gaps in this batch
		expectedNum := offset + 1
		for _, actualNum := range batchArticles {
			for expectedNum < actualNum {
				missing = append(missing, expectedNum)
				expectedNum++
			}
			expectedNum = actualNum + 1
		}

		// Update offset to the last article number in this batch
		offset = batchArticles[len(batchArticles)-1]
		totalProcessed += int64(len(batchArticles))

		// Progress reporting for large groups
		if totalProcessed%100000 == 0 {
			log.Printf("Processed %d articles, found %d missing so far", totalProcessed, len(missing))
		}
	}

	log.Printf("Missing article check complete: processed %d articles, found %d missing", totalProcessed, len(missing))
	return missing
}

// findOrphanedThreads finds thread entries pointing to non-existent articles using batched processing
func (db *Database) findOrphanedThreads(groupDB *GroupDBs) []int64 {
	var orphaned []int64

	log.Printf("Building article index in batches of %d", RescanBatchSize)

	// Build a map of existing article numbers using batched processing
	articleNums := make(map[int64]bool)
	var offset int64 = 0
	var totalArticles int64 = 0

	for {
		// Get batch of article numbers
		rows, err := retryableQuery(groupDB.DB,
			"SELECT article_num FROM articles WHERE article_num > ? ORDER BY article_num LIMIT ?",
			offset, RescanBatchSize)
		if err != nil {
			log.Printf("Error fetching article batch for orphan check starting at %d: %v", offset, err)
			return orphaned
		}

		var batchCount int64
		var lastArticle int64
		for rows.Next() {
			var num int64
			if err := rows.Scan(&num); err != nil {
				continue
			}
			articleNums[num] = true
			lastArticle = num
			batchCount++
		}
		rows.Close()

		if batchCount == 0 {
			break // No more articles
		}

		totalArticles += int64(batchCount)
		offset = lastArticle

		// Progress reporting for large groups
		if totalArticles%100000 == 0 {
			log.Printf("Indexed %d articles for orphan detection", totalArticles)
		}

		if batchCount < RescanBatchSize {
			break // Last batch
		}
	}

	log.Printf("Article index complete: %d articles indexed", totalArticles)

	// Now check thread roots in batches
	offset = 0
	var totalThreads int64 = 0

	for {
		// Get batch of distinct root_article numbers from threads table
		rows, err := retryableQuery(groupDB.DB,
			"SELECT DISTINCT root_article FROM threads WHERE root_article > ? ORDER BY root_article LIMIT ?",
			offset, RescanBatchSize)
		if err != nil {
			log.Printf("Error fetching thread batch for orphan check starting at %d: %v", offset, err)
			return orphaned
		}

		var batchCount int64
		var lastRoot int64
		for rows.Next() {
			var rootArticle int64
			if err := rows.Scan(&rootArticle); err != nil {
				continue
			}
			// Check if this root_article exists in articles table
			if !articleNums[rootArticle] {
				orphaned = append(orphaned, rootArticle)
			}
			lastRoot = rootArticle
			batchCount++
		}
		rows.Close()

		if batchCount == 0 {
			break // No more threads
		}

		totalThreads += int64(batchCount)
		offset = lastRoot

		// Progress reporting for large groups
		if totalThreads%50000 == 0 {
			log.Printf("Checked %d thread roots, found %d orphaned so far", totalThreads, len(orphaned))
		}

		if batchCount < RescanBatchSize {
			break // Last batch
		}
	}

	log.Printf("Orphaned thread check complete: checked %d thread roots, found %d orphaned", totalThreads, len(orphaned))
	return orphaned
}

// PrintConsistencyReport prints a human-readable consistency report
func (report *ConsistencyReport) PrintReport() {
	fmt.Printf("\n=== Database Consistency Report for '%s' ===\n", report.Newsgroup)

	if len(report.Errors) > 0 {
		fmt.Printf("ERRORS:\n")
		for _, err := range report.Errors {
			fmt.Printf("  - %s\n", err)
		}
		fmt.Printf("\n")
	}

	fmt.Printf("Main DB Last Article: %d\n", report.MainDBLastArticle)
	fmt.Printf("Articles Max Num:     %d\n", report.ArticlesMaxNum)
	fmt.Printf("Overview Max Num:     %d\n", report.OverviewMaxNum)
	fmt.Printf("Threads Max Num:      %d\n", report.ThreadsMaxNum)
	fmt.Printf("\n")

	fmt.Printf("Article Count:        %d\n", report.ArticleCount)
	fmt.Printf("Overview Count:       %d\n", report.OverviewCount)
	fmt.Printf("Thread Count:         %d\n", report.ThreadCount)
	fmt.Printf("\n")

	if len(report.MissingArticles) > 0 {
		fmt.Printf("Missing Articles (%d): %v\n", len(report.MissingArticles), report.MissingArticles)
	}

	if len(report.MissingOverviews) > 0 {
		fmt.Printf("Missing Overviews (%d): %v\n", len(report.MissingOverviews), report.MissingOverviews)
	}

	if len(report.OrphanedOverviews) > 0 {
		fmt.Printf("Orphaned Overviews (%d): %v\n", len(report.OrphanedOverviews), report.OrphanedOverviews)
	}

	if len(report.OrphanedThreads) > 0 {
		fmt.Printf("Orphaned Threads (%d): %v\n", len(report.OrphanedThreads), report.OrphanedThreads)
	}

	if len(report.MessageIDMismatches) > 0 {
		fmt.Printf("Message ID Mismatches (%d): %v\n", len(report.MessageIDMismatches), report.MessageIDMismatches)
	}

	if report.HasInconsistencies {
		fmt.Printf("\n❌ INCONSISTENCIES DETECTED!\n")
	} else {
		fmt.Printf("\n✅ Database is consistent.\n")
	}
	fmt.Printf("============================================\n\n")
}

// RebuildThreadsFromScratch completely rebuilds all thread relationships for a newsgroup
// This function deletes all existing threads and rebuilds them from article 1 based on message references
func (db *Database) RebuildThreadsFromScratch(newsgroup string, verbose bool) (*ThreadRebuildReport, error) {
	report := &ThreadRebuildReport{
		Newsgroup: newsgroup,
		StartTime: time.Now(),
		Errors:    []string{},
	}

	if verbose {
		log.Printf("RebuildThreadsFromScratch: Starting complete thread rebuild for newsgroup '%s'", newsgroup)
	}

	// Get group database
	groupDB, err := db.GetGroupDBs(newsgroup)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get group database: %v", err))
		return report, err
	}
	defer groupDB.Return(db)

	// Get total article count
	err = retryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", []interface{}{}, &report.TotalArticles)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get article count: %v", err))
		return report, err
	}

	if report.TotalArticles == 0 {
		if verbose {
			log.Printf("RebuildThreadsFromScratch: No articles found in newsgroup '%s', nothing to rebuild", newsgroup)
		}
		report.ThreadsRebuilt = 0
		report.EndTime = time.Now()
		return report, nil
	}

	if verbose {
		log.Printf("RebuildThreadsFromScratch: Found %d articles to process", report.TotalArticles)
	}

	// Step 1: Clear existing thread data
	if verbose {
		log.Printf("RebuildThreadsFromScratch: Clearing existing thread data...")
	}

	tx, err := groupDB.DB.Begin()
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to begin cleanup transaction: %v", err))
		return report, err
	}
	defer tx.Rollback()

	// Get count of existing threads for reporting
	var existingThreads int64
	tx.QueryRow("SELECT COUNT(*) FROM threads").Scan(&existingThreads)
	report.ThreadsDeleted = existingThreads

	// Clear thread-related tables in dependency order
	tables := []string{"tree_stats", "cached_trees", "thread_cache", "threads"}
	for _, table := range tables {
		_, err = tx.Exec(fmt.Sprintf("DELETE FROM %s", table))
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("Failed to clear table %s: %v", table, err))
			return report, err
		}
	}

	// Reset auto-increment for threads table
	_, err = tx.Exec("DELETE FROM sqlite_sequence WHERE name = 'threads'")
	if err != nil {
		// Non-critical error
		if verbose {
			log.Printf("RebuildThreadsFromScratch: Warning - could not reset auto-increment for threads: %v", err)
		}
	}

	err = tx.Commit()
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to commit cleanup transaction: %v", err))
		return report, err
	}

	if verbose {
		log.Printf("RebuildThreadsFromScratch: Cleared %d existing thread entries", existingThreads)
	}

	// Step 2: Build message-ID to article-number mapping
	if verbose {
		log.Printf("RebuildThreadsFromScratch: Building message-ID mapping...")
	}

	msgIDToArticleNum := make(map[string]int64)
	var offset int64 = 0

	for offset < report.TotalArticles {
		currentBatchSize := RescanBatchSize
		if offset+RescanBatchSize > report.TotalArticles {
			currentBatchSize = report.TotalArticles - offset
		}

		// Load batch of article mappings
		rows, err := retryableQuery(groupDB.DB, `
			SELECT article_num, message_id
			FROM articles
			ORDER BY article_num
			LIMIT ? OFFSET ?`, currentBatchSize, offset)

		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("Failed to query articles batch: %v", err))
			return report, err
		}

		for rows.Next() {
			var articleNum int64
			var messageID string
			if err := rows.Scan(&articleNum, &messageID); err != nil {
				rows.Close()
				report.Errors = append(report.Errors, fmt.Sprintf("Failed to scan article mapping: %v", err))
				return report, err
			}
			msgIDToArticleNum[messageID] = articleNum
		}
		rows.Close()

		offset += int64(currentBatchSize)

		if verbose && offset%1000 == 0 {
			log.Printf("RebuildThreadsFromScratch: Built message-ID mapping: %d/%d articles", offset, report.TotalArticles)
		}
	}

	if verbose {
		log.Printf("RebuildThreadsFromScratch: Message-ID mapping complete: %d entries", len(msgIDToArticleNum))
	}

	// Step 3: Process articles in batches to build thread relationships
	if verbose {
		log.Printf("RebuildThreadsFromScratch: Building thread relationships...")
	}

	offset = 0
	for offset < report.TotalArticles {
		currentBatchSize := RescanBatchSize
		if offset+RescanBatchSize > report.TotalArticles {
			currentBatchSize = report.TotalArticles - offset
		}

		threadsBuilt, err := db.processThreadBatch(groupDB, msgIDToArticleNum, offset, currentBatchSize, verbose)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("Failed to process thread batch at offset %d: %v", offset, err))
			return report, err
		}

		report.ThreadsRebuilt += int64(threadsBuilt)
		offset += int64(currentBatchSize)

		if verbose && offset%1000 == 0 {
			log.Printf("RebuildThreadsFromScratch: Threading progress: %d/%d articles processed, %d threads built",
				offset, report.TotalArticles, report.ThreadsRebuilt)
		}
	}

	report.EndTime = time.Now()
	report.Duration = report.EndTime.Sub(report.StartTime)

	if verbose {
		log.Printf("RebuildThreadsFromScratch: Completed successfully for newsgroup '%s'", newsgroup)
		log.Printf("  - Articles processed: %d", report.TotalArticles)
		log.Printf("  - Threads deleted: %d", report.ThreadsDeleted)
		log.Printf("  - Threads rebuilt: %d", report.ThreadsRebuilt)
		log.Printf("  - Duration: %v", report.Duration)
	}

	return report, nil
}

// processThreadBatch processes a batch of articles to build thread relationships
func (db *Database) processThreadBatch(groupDB *GroupDBs, msgIDToArticleNum map[string]int64, offset, RescanBatchSize int64, verbose bool) (int, error) {
	// Get batch of articles with their references
	rows, err := retryableQuery(groupDB.DB, `
		SELECT article_num, message_id, "references"
		FROM articles
		ORDER BY article_num
		LIMIT ? OFFSET ?
	`, RescanBatchSize, offset)
	if err != nil {
		return 0, fmt.Errorf("failed to query articles: %w", err)
	}
	defer rows.Close()

	// Start transaction for this batch
	tx, err := groupDB.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	threadStmt, err := tx.Prepare("INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return 0, fmt.Errorf("failed to prepare thread insert statement: %w", err)
	}
	defer threadStmt.Close()

	var threadsBuilt int

	for rows.Next() {
		var articleNum int64
		var messageID, references string

		err := rows.Scan(&articleNum, &messageID, &references)
		if err != nil {
			if verbose {
				log.Printf("processThreadBatch: Error scanning article: %v", err)
			}
			continue
		}

		refs := db.parseReferences(references)

		if len(refs) == 0 {
			// This is a thread root
			_, err = threadStmt.Exec(articleNum, nil, articleNum, 0, 0)
			if err != nil {
				if verbose {
					log.Printf("processThreadBatch: Failed to insert thread root for article %d: %v", articleNum, err)
				}
				continue
			}
			threadsBuilt++
		} else {
			// This is a reply - find the best parent
			var parentArticleNum int64
			var rootArticleNum int64
			depth := 1

			// Find the most recent parent in the references chain
			for i := len(refs) - 1; i >= 0; i-- {
				if parentNum, exists := msgIDToArticleNum[refs[i]]; exists {
					parentArticleNum = parentNum

					// Find the root of this thread by looking up the parent's thread entry
					err := tx.QueryRow("SELECT root_article, depth FROM threads WHERE child_article = ?", parentNum).Scan(&rootArticleNum, &depth)
					if err == nil {
						depth++ // This article is one level deeper than its parent
						break
					}
					// If parent not found in threads yet, treat parent as root
					rootArticleNum = parentNum
					depth = 1
					break
				}
			}

			// If no parent found in our database, treat this as a root
			if parentArticleNum == 0 {
				rootArticleNum = articleNum
				depth = 0
			}

			_, err = threadStmt.Exec(rootArticleNum, parentArticleNum, articleNum, depth, 0)
			if err != nil {
				if verbose {
					log.Printf("processThreadBatch: Failed to insert thread entry for article %d: %v", articleNum, err)
				}
				continue
			}
			threadsBuilt++
		}
	}

	if err = rows.Err(); err != nil {
		return threadsBuilt, fmt.Errorf("error iterating articles: %w", err)
	}

	// Commit this batch
	if err := tx.Commit(); err != nil {
		return threadsBuilt, fmt.Errorf("failed to commit thread batch: %w", err)
	}

	return threadsBuilt, nil
}

// parseReferences parses the references header into individual message IDs
func (db *Database) parseReferences(refs string) []string {
	if refs == "" {
		return []string{}
	}

	// Use strings.Fields() for robust whitespace handling
	fields := strings.Fields(refs)

	var cleanRefs []string
	for _, ref := range fields {
		ref = strings.TrimSpace(ref)
		if ref != "" && strings.HasPrefix(ref, "<") && strings.HasSuffix(ref, ">") {
			cleanRefs = append(cleanRefs, ref)
		}
	}

	return cleanRefs
}

// ThreadRebuildReport represents the results of a thread rebuild operation
type ThreadRebuildReport struct {
	Newsgroup      string
	TotalArticles  int64
	ThreadsDeleted int64
	ThreadsRebuilt int64
	Errors         []string
	StartTime      time.Time
	EndTime        time.Time
	Duration       time.Duration
}

// PrintReport prints a human-readable thread rebuild report
func (report *ThreadRebuildReport) PrintReport() {
	fmt.Printf("\n=== Thread Rebuild Report for '%s' ===\n", report.Newsgroup)

	if len(report.Errors) > 0 {
		fmt.Printf("ERRORS:\n")
		for _, err := range report.Errors {
			fmt.Printf("  - %s\n", err)
		}
		fmt.Printf("\n")
	}

	fmt.Printf("Articles processed:   %d\n", report.TotalArticles)
	fmt.Printf("Threads deleted:      %d\n", report.ThreadsDeleted)
	fmt.Printf("Threads rebuilt:      %d\n", report.ThreadsRebuilt)
	fmt.Printf("Duration:             %v\n", report.Duration)

	if len(report.Errors) == 0 {
		fmt.Printf("\n✅ Thread rebuild completed successfully.\n")
	} else {
		fmt.Printf("\n❌ Thread rebuild completed with errors.\n")
	}
	fmt.Printf("===========================================\n\n")
}

/* CODE REFERENCES: internal/database/models.go */
