package database

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RecoverDatabase attempts to recover the database by checking for missing articles and last_insert_ids mismatches
var RescanBatchSize int64 = 1000

func (db *Database) GetLatestArticleNumberFromOverview(newsgroup string) (int64, error) {
	// Since overview table is unified with articles, query articles table instead
	groupDB, err := db.GetGroupDB(newsgroup)
	if err != nil {
		return 0, err
	}
	defer groupDB.Return()

	var latestArticle int64
	err = RetryableQueryRowScan(groupDB.DB, `
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
	rows, err := RetryableQuery(db.GetMainDB(), `
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
	groupDB, err := db.GetGroupDB(newsgroup)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get group databases: %v", err))
		return report, nil
	}
	defer groupDB.Return()

	// 3. Get max article numbers from each table (handle NULL for empty tables)
	err = RetryableQueryRowScan(groupDB.DB, "SELECT COALESCE(MAX(article_num), 0) FROM articles", []interface{}{}, &report.ArticlesMaxNum)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get max article_num from articles: %v", err))
	}

	// Since overview is now unified with articles, OverviewMaxNum equals ArticlesMaxNum
	report.OverviewMaxNum = report.ArticlesMaxNum

	err = RetryableQueryRowScan(groupDB.DB, "SELECT COALESCE(MAX(root_article), 0) FROM threads", []interface{}{}, &report.ThreadsMaxNum)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get max root_article from threads: %v", err))
	}

	// 4. Get counts from each table
	err = RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", []interface{}{}, &report.ArticleCount)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get article count: %v", err))
	}

	// Since overview is now unified with articles, OverviewCount equals ArticleCount
	report.OverviewCount = report.ArticleCount

	err = RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM threads", []interface{}{}, &report.ThreadCount)
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
func (db *Database) findMissingArticles(groupDB *GroupDB, maxArticleNum int64) []int64 {
	var missing []int64
	if maxArticleNum <= 0 {
		return missing
	}

	var offset int64 = 0
	var totalProcessed int64 = 0

	log.Printf("Checking for missing articles in batches of %d (max article: %d)", RescanBatchSize, maxArticleNum)

	for offset < maxArticleNum {
		// Get batch of article numbers
		rows, err := RetryableQuery(groupDB.DB,
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
		if totalProcessed%10000 == 0 {
			log.Printf("Processed %d articles, found %d missing so far", totalProcessed, len(missing))
		}
	}

	log.Printf("Missing article check complete: processed %d articles, found %d missing", totalProcessed, len(missing))
	return missing
}

// findOrphanedThreads finds thread entries pointing to non-existent articles using batched processing
func (db *Database) findOrphanedThreads(groupDB *GroupDB) []int64 {
	var orphaned []int64

	log.Printf("Building article index in batches of %d", RescanBatchSize)

	// Build a map of existing article numbers using batched processing
	articleNums := make(map[int64]bool)
	var offset int64 = 0
	var totalArticles int64 = 0

	for {
		// Get batch of article numbers
		rows, err := RetryableQuery(groupDB.DB,
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
		if totalArticles%10000 == 0 {
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
		rows, err := RetryableQuery(groupDB.DB,
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

const query_RebuildThreadsFromScratch1 = "SELECT COUNT(*) FROM articles"
const query_RebuildThreadsFromScratch2 = "SELECT COUNT(*) FROM threads"
const query_RebuildThreadsFromScratch3 = "DELETE FROM %s"
const query_RebuildThreadsFromScratch4 = "DELETE FROM sqlite_sequence WHERE name = 'threads'"

// query_RebuildThreadsFromScratch5 pages the message-id mapping by article_num (the rowid),
// not by date_sent with LIMIT/OFFSET: the mapping does not care about the order, OFFSET
// rescans the sorted set for every batch, and rows with equal date_sent can land in two
// batches or in none.
const query_RebuildThreadsFromScratch5 = `SELECT article_num, message_id FROM articles WHERE article_num > ? ORDER BY article_num LIMIT ?`

// RebuildThreadsFromScratch completely rebuilds all thread relationships for a newsgroup
// This function deletes all existing threads and rebuilds them from article 1 based on message references
// If groupDB is provided (not nil), it will use that instead of opening the newsgroup's database
func (db *Database) RebuildThreadsFromScratch(newsgroup string, verbose bool, groupDB *GroupDB) (*ThreadRebuildReport, error) {
	report := &ThreadRebuildReport{
		Newsgroup: newsgroup,
		StartTime: time.Now(),
		Errors:    []string{},
	}

	if verbose {
		log.Printf("RebuildThreadsFromScratch: Starting complete thread rebuild for newsgroup '%s'", newsgroup)
	}

	// TIMER: Get group database
	stepStart := time.Now()
	var shouldCloseDB bool
	if groupDB == nil {
		var err error
		groupDB, err = db.GetGroupDB(newsgroup)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("Failed to get group database: %v", err))
			return report, err
		}
		shouldCloseDB = true
	}
	if verbose {
		log.Printf("⏱️  TIMER: GetGroupDB took %v", time.Since(stepStart))
	}

	if shouldCloseDB {
		defer groupDB.Return()
	}

	// TIMER: Get total article count
	stepStart = time.Now()
	var err error
	err = RetryableQueryRowScan(groupDB.DB, query_RebuildThreadsFromScratch1, []interface{}{}, &report.TotalArticles)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get article count: %v", err))
		return report, err
	}
	if verbose {
		log.Printf("⏱️  TIMER: Get article count took %v", time.Since(stepStart))
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

	// TIMER: Step 1: Clear existing thread data
	stepStart = time.Now()
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
	tx.QueryRow(query_RebuildThreadsFromScratch2).Scan(&existingThreads)
	report.ThreadsDeleted = existingThreads

	// Clear thread-related tables in dependency order
	tables := []string{"tree_stats", "cached_trees", "thread_cache", "threads"}
	for _, table := range tables {
		tableStart := time.Now()
		_, err = tx.Exec(fmt.Sprintf(query_RebuildThreadsFromScratch3, table))
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("Failed to clear table %s: %v", table, err))
			return report, err
		}
		if verbose {
			log.Printf("⏱️    TIMER: Clear table %s took %v", table, time.Since(tableStart))
		}
	}

	// Reset auto-increment for threads table
	_, err = tx.Exec(query_RebuildThreadsFromScratch4)
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
		log.Printf("⏱️  TIMER: Clear existing thread data (total) took %v", time.Since(stepStart))
	}

	// TIMER: Step 2: Build message-ID to article-number mapping
	stepStart = time.Now()
	if verbose {
		log.Printf("RebuildThreadsFromScratch: Building message-ID mapping...")
	}

	msgIDToArticleNum := make(map[string]int64)
	var lastNum int64
	var totalBatches int64 = 0

	for {
		batchStart := time.Now()

		// Load batch of article mappings
		rows, err := RetryableQuery(groupDB.DB, query_RebuildThreadsFromScratch5, lastNum, RescanBatchSize)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("Failed to query articles batch: %v", err))
			return report, err
		}

		var batchCount int64
		for rows.Next() {
			var articleNum int64
			var messageID string
			if err := rows.Scan(&articleNum, &messageID); err != nil {
				rows.Close()
				report.Errors = append(report.Errors, fmt.Sprintf("Failed to scan article mapping: %v", err))
				return report, err
			}
			msgIDToArticleNum[messageID] = articleNum
			lastNum = articleNum
			batchCount++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			report.Errors = append(report.Errors, fmt.Sprintf("Failed to iterate articles batch: %v", err))
			return report, err
		}
		rows.Close()

		if batchCount == 0 {
			break
		}
		totalBatches++

		if verbose {
			log.Printf("RebuildThreadsFromScratch: Built message-ID mapping: %d articles (batch took %v)",
				len(msgIDToArticleNum), time.Since(batchStart))
		}
		if batchCount < RescanBatchSize {
			break
		}
	}

	if verbose {
		log.Printf("RebuildThreadsFromScratch: Message-ID mapping complete: %d entries", len(msgIDToArticleNum))
		log.Printf("⏱️  TIMER: Build message-ID mapping (total) took %v (%d batches)", time.Since(stepStart), totalBatches)
	}

	// TIMER: Step 3: Pass 1 - the parent of every article, per group: the nearest reference
	// (last to first) that exists in this group. No hit means the article is a thread root
	// here, with or without References - the same per-group rule intake applies
	// (db_batch.go, batchProcessThreading).
	stepStart = time.Now()
	if verbose {
		log.Printf("RebuildThreadsFromScratch: Building the parent map...")
	}

	st := newThreadRebuildState(msgIDToArticleNum)
	if err := db.buildThreadParentMap(groupDB, st); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to build the parent map: %v", err))
		return report, err
	}
	if verbose {
		log.Printf("⏱️  TIMER: Build the parent map took %v (%d articles with a parent in this group)",
			time.Since(stepStart), len(st.parentOf))
	}

	// TIMER: Step 4: Insert the thread roots. They go in before the replies: pass 2 resolves
	// a root of its own for every article, and a group whose articles were filed out of order
	// has roots with a higher article number than their children, so a child's thread_cache
	// append has to wait until every root's row exists.
	stepStart = time.Now()
	if verbose {
		log.Printf("RebuildThreadsFromScratch: Inserting thread roots...")
	}

	rootsBuilt, err := db.rebuildThreadRoots(groupDB, st)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to insert thread roots: %v", err))
		return report, err
	}
	report.ThreadsRebuilt = rootsBuilt
	if verbose {
		log.Printf("⏱️  TIMER: Insert %d thread roots took %v", rootsBuilt, time.Since(stepStart))
	}

	// TIMER: Step 5: Insert the replies, append them to their root's thread_cache row and
	// recompute articles.reply_count from the direct children.
	stepStart = time.Now()
	if verbose {
		log.Printf("RebuildThreadsFromScratch: Inserting replies...")
	}

	repliesBuilt, err := db.rebuildThreadReplies(groupDB, st, verbose)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to insert replies: %v", err))
		return report, err
	}
	if verbose {
		log.Printf("⏱️  TIMER: Insert %d replies took %v", repliesBuilt, time.Since(stepStart))
	}

	// One line per group: a reference cycle (a self-reference included) is broken by making
	// the article where it is detected a thread root.
	if st.cycleRoots > 0 {
		log.Printf("[RESCAN] group '%s': %d article(s) made thread roots to break a reference cycle (%d of them reference themselves)",
			newsgroup, st.cycleRoots, st.selfRefs)
	}

	report.EndTime = time.Now()
	report.Duration = report.EndTime.Sub(report.StartTime)

	log.Printf("[RESCAN] group '%s': thread rebuild done: %d articles, %d thread roots, %d replies, %d rows deleted, took %v",
		newsgroup, report.TotalArticles, rootsBuilt, repliesBuilt, report.ThreadsDeleted, report.Duration)

	if verbose {
		log.Printf("RebuildThreadsFromScratch: Completed successfully for newsgroup '%s'", newsgroup)
		log.Printf("  - Articles processed: %d", report.TotalArticles)
		log.Printf("  - Threads deleted: %d", report.ThreadsDeleted)
		log.Printf("  - Threads rebuilt: %d", report.ThreadsRebuilt)
		log.Printf("  - Duration: %d ms", report.Duration.Milliseconds())
	}
	msgIDToArticleNum = nil
	return report, nil
}

// query_threadRebuildRefs reads pass 1's input: only the articles that have a References
// header can have a parent. query_threadRebuildArticles reads pass 2's input, every article
// with the date its thread_cache row needs. Both page by article_num (the rowid).
const query_threadRebuildRefs = `SELECT article_num, "references" FROM articles WHERE article_num > ? AND "references" IS NOT NULL AND "references" != '' ORDER BY article_num LIMIT ?`
const query_threadRebuildArticles = `SELECT article_num, date_sent FROM articles WHERE article_num > ? ORDER BY article_num LIMIT ?`

const query_processThreadBatch2 = "INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, ?, ?, 0, 0)"
const query_processThreadBatch3 = "INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, ?, ?, ?, 0)"

// articles.reply_count is recomputed from scratch: zero for the whole group, then one
// increment per direct child. The increment (instead of a plain set) is what lets a parent's
// children arrive in more than one chunk.
const query_threadRebuildResetReplyCounts = "UPDATE articles SET reply_count = 0"
const query_threadRebuildBumpReplyCount = "UPDATE articles SET reply_count = reply_count + ? WHERE article_num = ?"

// threadPlace is where one article sits in the threads of its group: the article number of
// its thread root and its depth below that root (0 = the article is the root itself).
type threadPlace struct {
	root  int64
	depth int
}

// threadRootRow is one article of the group: its number and its date (the date a thread_cache
// row of a root starts from).
type threadRootRow struct {
	articleNum int64
	dateSent   time.Time
}

// threadReplyRow is one reply with its resolved place in the thread of its group.
type threadReplyRow struct {
	articleNum int64
	parentNum  int64
	rootNum    int64
	depth      int
}

// threadRebuildState holds the whole-group threading state of one RebuildThreadsFromScratch
// run and applies the per-group rule intake applies as well (db_batch.go,
// batchProcessThreading): an article whose References name no article of this group is a
// thread root here, with or without References; an article that has such a reference joins
// the thread of the nearest one (last reference first). depth is the parent's depth + 1, not
// the position in References: trimmed References (RFC 5537 3.4.4) make the position wrong.
type threadRebuildState struct {
	msgIDToArticleNum map[string]int64      // message-id -> article_num, every article of the group
	parentOf          map[int64]int64       // article_num -> parent article_num (pass 1, replies only)
	places            map[int64]threadPlace // pass 2 memo
	cycleRoots        int64                 // articles made roots to break a reference cycle
	selfRefs          int64                 // of those: articles whose nearest reference is themselves
	walk              []int64               // scratch: the chain placeOf is currently walking up
	onWalk            map[int64]int         // scratch: article_num -> its index in walk
}

func newThreadRebuildState(msgIDToArticleNum map[string]int64) *threadRebuildState {
	return &threadRebuildState{
		msgIDToArticleNum: msgIDToArticleNum,
		parentOf:          make(map[int64]int64),
		places:            make(map[int64]threadPlace),
		onWalk:            make(map[int64]int),
	}
}

// placeOf is pass 2 for one article: it follows parentOf up to an article without a parent,
// which is the thread root, and counts the hops as the depth. Results are memoized, so the
// whole group costs one walk per chain. A reference cycle is broken where it is detected: that
// article becomes a thread root (counted in cycleRoots) and the resolution starts over, which
// terminates because every round memoizes one more root.
func (st *threadRebuildState) placeOf(articleNum int64) threadPlace {
	if place, ok := st.places[articleNum]; ok {
		return place
	}
	if _, hasParent := st.parentOf[articleNum]; !hasParent {
		// A root without children: no need to keep it in the memo.
		return threadPlace{root: articleNum, depth: 0}
	}
	for {
		st.walk = st.walk[:0]
		for num := range st.onWalk {
			delete(st.onWalk, num)
		}
		base, restart := st.climbToRoot(articleNum)
		if restart {
			continue
		}
		// Hand the depths back down the chain: walk holds the unresolved articles, the one
		// closest to the root last.
		for i := len(st.walk) - 1; i >= 0; i-- {
			base = threadPlace{root: base.root, depth: base.depth + 1}
			st.places[st.walk[i]] = base
		}
		return st.places[articleNum]
	}
}

// climbToRoot walks the parents of articleNum until it reaches a memoized place or an article
// without a parent, collecting the unresolved articles in st.walk. It reports restart = true
// when it had to break a cycle: the caller then resolves articleNum again, now that the
// article the cycle was detected at is a memoized root.
func (st *threadRebuildState) climbToRoot(articleNum int64) (threadPlace, bool) {
	cur := articleNum
	for {
		if place, ok := st.places[cur]; ok {
			return place, false
		}
		if _, onWalk := st.onWalk[cur]; onWalk {
			// Back where we have already been: break the cycle here.
			st.places[cur] = threadPlace{root: cur, depth: 0}
			st.cycleRoots++
			return threadPlace{}, true
		}
		st.onWalk[cur] = len(st.walk)
		st.walk = append(st.walk, cur)

		parent, hasParent := st.parentOf[cur]
		if !hasParent || parent == cur {
			// No parent in this group, or an article referencing itself: cur is the root.
			if hasParent {
				st.cycleRoots++
				st.selfRefs++
			}
			place := threadPlace{root: cur, depth: 0}
			st.places[cur] = place
			st.walk = st.walk[:len(st.walk)-1]
			delete(st.onWalk, cur)
			return place, false
		}
		cur = parent
	}
}

// buildThreadParentMap is pass 1: the parent of every article of the group is the nearest
// reference (last to first) that exists in this group.
func (db *Database) buildThreadParentMap(groupDB *GroupDB, st *threadRebuildState) error {
	var lastNum int64
	for {
		rows, err := RetryableQuery(groupDB.DB, query_threadRebuildRefs, lastNum, RescanBatchSize)
		if err != nil {
			return fmt.Errorf("failed to query references: %w", err)
		}

		var batchCount int64
		for rows.Next() {
			var articleNum int64
			var references string
			if err := rows.Scan(&articleNum, &references); err != nil {
				rows.Close()
				return fmt.Errorf("failed to scan references: %w", err)
			}
			batchCount++
			lastNum = articleNum

			refs := db.parseReferences(references)
			for i := len(refs) - 1; i >= 0; i-- {
				if parentNum, exists := st.msgIDToArticleNum[refs[i]]; exists {
					st.parentOf[articleNum] = parentNum
					break
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("error iterating references: %w", err)
		}
		rows.Close()

		if batchCount < RescanBatchSize {
			return nil
		}
	}
}

// readThreadArticleChunk reads the next chunk of articles after afterNum, ascending.
func readThreadArticleChunk(groupDB *GroupDB, afterNum int64) ([]threadRootRow, error) {
	rows, err := RetryableQuery(groupDB.DB, query_threadRebuildArticles, afterNum, RescanBatchSize)
	if err != nil {
		return nil, fmt.Errorf("failed to query articles: %w", err)
	}
	defer rows.Close()

	chunk := make([]threadRootRow, 0, RescanBatchSize)
	for rows.Next() {
		var row threadRootRow
		if err := rows.Scan(&row.articleNum, &row.dateSent); err != nil {
			return nil, fmt.Errorf("failed to scan article: %w", err)
		}
		chunk = append(chunk, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating articles: %w", err)
	}
	return chunk, nil
}

// rebuildThreadRoots inserts the threads row of every thread root of the group and
// initializes its thread_cache row. Row shape: (root, NULL, root, 0), the one intake writes
// (db_batch.go, batchProcessThreadRoots).
func (db *Database) rebuildThreadRoots(groupDB *GroupDB, st *threadRebuildState) (int64, error) {
	var lastNum, rootsBuilt int64
	for {
		chunk, err := readThreadArticleChunk(groupDB, lastNum)
		if err != nil {
			return rootsBuilt, err
		}
		if len(chunk) == 0 {
			return rootsBuilt, nil
		}
		lastNum = chunk[len(chunk)-1].articleNum

		roots := make([]threadRootRow, 0, len(chunk))
		for _, row := range chunk {
			if st.placeOf(row.articleNum).depth == 0 {
				roots = append(roots, row)
			}
		}

		if len(roots) > 0 {
			if err := insertThreadRoots(groupDB, roots); err != nil {
				return rootsBuilt, err
			}
			rootsBuilt += int64(len(roots))

			if err := db.batchInitializeThreadCache(groupDB, roots); err != nil {
				// Cache errors do not fail the rebuild: the threads rows are written, which
				// is what a later rebuild re-derives the cache from. The children of a root
				// whose row is missing are skipped by updateThreadCacheWithChildren (its
				// skipped count is logged with -v).
				log.Printf("[RESCAN] group '%s': failed to initialize thread_cache for %d roots: %v",
					groupDB.Newsgroup, len(roots), err)
			}
		}

		if int64(len(chunk)) < RescanBatchSize {
			return rootsBuilt, nil
		}
	}
}

// insertThreadRoots writes one chunk of thread roots in a single transaction.
func insertThreadRoots(groupDB *GroupDB, roots []threadRootRow) error {
	return RetryableTransactionExec(groupDB.DB, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(query_processThreadBatch2)
		if err != nil {
			return fmt.Errorf("failed to prepare thread root insert statement: %w", err)
		}
		defer stmt.Close()

		for _, root := range roots {
			// root_article = child_article = this article, parent_article = NULL, depth 0.
			if _, err := stmt.Exec(root.articleNum, nil, root.articleNum); err != nil {
				return fmt.Errorf("failed to insert thread root for article %d: %w", root.articleNum, err)
			}
		}
		return nil
	})
}

// rebuildThreadReplies inserts the threads row of every reply of the group, appends the
// replies to their root's thread_cache row and recomputes articles.reply_count as the number
// of direct children. Row shape: (root, parent, child, parent depth + 1), the shape the web
// tree and the intake rule share.
func (db *Database) rebuildThreadReplies(groupDB *GroupDB, st *threadRebuildState, verbose bool) (int64, error) {
	if _, err := RetryableExec(groupDB.DB, query_threadRebuildResetReplyCounts); err != nil {
		return 0, fmt.Errorf("failed to reset reply counts: %w", err)
	}

	var lastNum, repliesBuilt int64
	for {
		chunk, err := readThreadArticleChunk(groupDB, lastNum)
		if err != nil {
			return repliesBuilt, err
		}
		if len(chunk) == 0 {
			return repliesBuilt, nil
		}
		lastNum = chunk[len(chunk)-1].articleNum

		replies := make([]threadReplyRow, 0, len(chunk))
		for _, row := range chunk {
			place := st.placeOf(row.articleNum)
			if place.depth == 0 {
				continue // a thread root, inserted by rebuildThreadRoots
			}
			replies = append(replies, threadReplyRow{
				articleNum: row.articleNum,
				parentNum:  st.parentOf[row.articleNum],
				rootNum:    place.root,
				depth:      place.depth,
			})
		}

		if len(replies) > 0 {
			if err := insertThreadReplies(groupDB, replies); err != nil {
				return repliesBuilt, err
			}
			repliesBuilt += int64(len(replies))

			// thread_cache: append this chunk's children to their root, and count the direct
			// children of each parent for articles.reply_count.
			cacheUpdates := make(map[int64][]int64, len(replies))
			replyCounts := make(map[int64]int64, len(replies))
			for _, reply := range replies {
				cacheUpdates[reply.rootNum] = append(cacheUpdates[reply.rootNum], reply.articleNum)
				replyCounts[reply.parentNum]++
			}

			if err := db.updateThreadCacheWithChildren(groupDB, cacheUpdates, verbose); err != nil {
				// As before: a cache error does not fail the rebuild.
				log.Printf("[RESCAN] group '%s': failed to update thread_cache for %d roots: %v",
					groupDB.Newsgroup, len(cacheUpdates), err)
			}
			if err := bumpThreadReplyCounts(groupDB, replyCounts); err != nil {
				return repliesBuilt, err
			}
		}

		if int64(len(chunk)) < RescanBatchSize {
			return repliesBuilt, nil
		}
	}
}

// insertThreadReplies writes one chunk of replies in a single transaction.
func insertThreadReplies(groupDB *GroupDB, replies []threadReplyRow) error {
	return RetryableTransactionExec(groupDB.DB, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(query_processThreadBatch3)
		if err != nil {
			return fmt.Errorf("failed to prepare reply insert statement: %w", err)
		}
		defer stmt.Close()

		for _, reply := range replies {
			if _, err := stmt.Exec(reply.rootNum, reply.parentNum, reply.articleNum, reply.depth); err != nil {
				return fmt.Errorf("failed to insert reply for article %d: %w", reply.articleNum, err)
			}
		}
		return nil
	})
}

// bumpThreadReplyCounts adds one chunk's direct children to articles.reply_count.
func bumpThreadReplyCounts(groupDB *GroupDB, counts map[int64]int64) error {
	if len(counts) == 0 {
		return nil
	}
	return RetryableTransactionExec(groupDB.DB, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(query_threadRebuildBumpReplyCount)
		if err != nil {
			return fmt.Errorf("failed to prepare reply count update statement: %w", err)
		}
		defer stmt.Close()

		for articleNum, count := range counts {
			if _, err := stmt.Exec(count, articleNum); err != nil {
				return fmt.Errorf("failed to update reply_count of article %d: %w", articleNum, err)
			}
		}
		return nil
	})
}

// query_initializeThreadCacheSimple1 is used by batchInitializeThreadCache (thread
// rebuilds). INSERT OR REPLACE deletes an existing thread_cache row and inserts a new
// one, so with foreign_keys=ON the delete cascades to tree_stats. That is the correct
// invalidation for derived cache data, and RebuildThreadsFromScratch clears tree_stats
// (and cached_trees, thread_cache, threads) before it runs anyway.
const query_initializeThreadCacheSimple1 = `
		INSERT OR REPLACE INTO thread_cache (
			thread_root, root_date, message_count, child_articles, last_child_number, last_activity
		) VALUES (?, ?, 1, '', ?, ?)
	`

// batchInitializeThreadCache initializes thread cache for multiple root articles in a single transaction
func (db *Database) batchInitializeThreadCache(groupDB *GroupDB, threadRoots []threadRootRow) error {
	if len(threadRoots) == 0 {
		return nil
	}

	now := time.Now().UTC()
	futureLimit := now.Add(25 * time.Hour)

	// Use RetryableTransactionExec for SQLite lock safety
	err := RetryableTransactionExec(groupDB.DB, func(tx *sql.Tx) error {
		// Prepare statement for batch inserts
		stmt, err := tx.Prepare(query_initializeThreadCacheSimple1)
		if err != nil {
			return fmt.Errorf("failed to prepare thread cache insert statement: %w", err)
		}
		defer stmt.Close()

		// Batch insert all thread cache entries
		for _, root := range threadRoots {
			rootDate := root.dateSent

			// Validate root date - skip obvious future posts
			if rootDate.UTC().After(futureLimit) {
				log.Printf("batchInitializeThreadCache: Using current time for thread root %d with future date %v",
					root.articleNum, rootDate.Format("2006-01-02 15:04:05"))
				rootDate = now
			}

			_, err = stmt.Exec(
				root.articleNum,
				rootDate.UTC().Format("2006-01-02 15:04:05"),
				root.articleNum, // last_child_number starts as the root itself
				rootDate.UTC().Format("2006-01-02 15:04:05"),
			)
			if err != nil {
				return fmt.Errorf("failed to initialize thread cache for root %d: %w", root.articleNum, err)
			}
		}

		return nil // Transaction will be committed by RetryableTransactionExec
	})

	if err != nil {
		return fmt.Errorf("failed to batch initialize thread cache: %w", err)
	}

	return nil
}

const query_updateThreadCacheWithChildren1 = "SELECT message_count, child_articles FROM thread_cache WHERE thread_root = ?"
const query_updateThreadCacheWithChildren2 = `
			UPDATE thread_cache
			SET child_articles = ?,
				message_count = ?
			WHERE thread_root = ?
		`

// updateThreadCacheWithChildren updates the thread_cache table with child article lists
func (db *Database) updateThreadCacheWithChildren(groupDB *GroupDB, rootUpdates map[int64][]int64, verbose bool) error {
	if len(rootUpdates) == 0 {
		return nil
	}

	funcStart := time.Now()

	// TIMER: Begin transaction
	stepStart := time.Now()
	tx, err := groupDB.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin thread cache transaction: %w", err)
	}
	defer tx.Rollback()
	txTime := time.Since(stepStart)

	// TIMER: Update each thread root's cache
	stepStart = time.Now()
	updatedCount := 0
	skippedCount := 0

	for rootArticle, childArticleNums := range rootUpdates {
		// Get current thread cache data
		var currentCount int
		var currentChildren string
		err := tx.QueryRow(query_updateThreadCacheWithChildren1, rootArticle).Scan(&currentCount, &currentChildren)
		if err != nil {
			//if verbose {
			//	log.Printf("updateThreadCacheWithChildren: No thread cache entry for root %d, skipping", rootArticle)
			//}
			skippedCount++
			continue
		}

		// Merge with the children already listed (a root's children can arrive in more than
		// one chunk) and write the result sorted by article number. Collecting them in a map
		// and joining the map's keys made the column's order random from run to run, while
		// intake (db_batch.go, batchUpdateThreadCache) appends children in arrival order,
		// which for a group filed in order is ascending: sorting is what makes the rebuilt
		// column equal to the one intake writes.
		childSet := make(map[int64]bool, len(childArticleNums)+1)
		for _, field := range strings.Split(currentChildren, ",") {
			if field == "" {
				continue
			}
			num, convErr := strconv.ParseInt(field, 10, 64)
			if convErr != nil {
				log.Printf("updateThreadCacheWithChildren: group '%s' root %d: dropping unparsable child_articles entry %q: %v",
					groupDB.Newsgroup, rootArticle, field, convErr)
				continue
			}
			childSet[num] = true
		}
		for _, num := range childArticleNums {
			childSet[num] = true
		}

		finalChildren := make([]int64, 0, len(childSet))
		for num := range childSet {
			finalChildren = append(finalChildren, num)
		}
		sort.Slice(finalChildren, func(i, j int) bool { return finalChildren[i] < finalChildren[j] })

		childStrs := make([]string, len(finalChildren))
		for i, num := range finalChildren {
			childStrs[i] = strconv.FormatInt(num, 10)
		}
		finalChildrenStr := strings.Join(childStrs, ",")
		newMessageCount := 1 + len(finalChildren) // 1 for root + replies

		// Update thread_cache with child articles list and correct message count
		_, err = tx.Exec(query_updateThreadCacheWithChildren2, finalChildrenStr, newMessageCount, rootArticle)

		if err != nil {
			log.Printf("updateThreadCacheWithChildren: Failed to update cache for root %d: %v", rootArticle, err)
			continue
		}
		updatedCount++

		//if verbose {
		//	log.Printf("updateThreadCacheWithChildren: Updated root %d with %d replies: %s", rootArticle, len(finalChildren), finalChildrenStr)
		//}
	}
	updateTime := time.Since(stepStart)

	// TIMER: Commit transaction
	stepStart = time.Now()
	commitErr := tx.Commit()
	commitTime := time.Since(stepStart)

	if verbose {
		log.Printf("⏱️      updateThreadCacheWithChildren: Begin=%v, Update %d roots=%v, Commit=%v, TOTAL=%v (skipped %d)",
			txTime, updatedCount, updateTime, commitTime, time.Since(funcStart), skippedCount)
	}

	return commitErr
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
	Newsgroup        string
	TotalArticles    int64
	ThreadsDeleted   int64
	ThreadsRebuilt   int64
	FutureDatesFixed int64 // New: count of future dates corrected
	Errors           []string
	StartTime        time.Time
	EndTime          time.Time
	Duration         time.Duration
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
	if report.TotalArticles > 0 {
		fmt.Printf("Articles processed:        %d\n", report.TotalArticles)
	}
	if report.ThreadsDeleted > 0 || report.ThreadsRebuilt > 0 {
		fmt.Printf("Threads deleted/rebuilt:   %d/%d\n", report.ThreadsDeleted, report.ThreadsRebuilt)
	}
	if report.FutureDatesFixed > 0 {
		fmt.Printf("Future dates fixed:        %d\n", report.FutureDatesFixed)
	}
	fmt.Printf("Duration:                  %d ms\n", report.Duration.Milliseconds())

	if len(report.Errors) == 0 {
		fmt.Printf("\n✅ Thread rebuild completed successfully.\n")
		if report.FutureDatesFixed > 0 {
			fmt.Printf("💡 Fixed %d future date issues during rebuild.\n", report.FutureDatesFixed)
		}
	} else {
		fmt.Printf("\n❌ Thread rebuild completed with errors.\n")
	}
	fmt.Printf("===========================================\n\n")
}

/* CODE REFERENCES: internal/database/models.go */
