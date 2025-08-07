package database

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/utils"
)

// SQLite safety limits: split large batches to avoid parameter/length limits
// SQLite 3.34.1+ supports up to 32766 (vs 999 in older versions)
var BatchInterval = 5 * time.Second // @AI: DO NOT CHANGE THIS!!!!
var maxBatchSize = 4000             // @AI: DO NOT CHANGE THIS!!!!
const maxTasks = 100

var InitialShutDownCounter = 20 // (div by two = waiting time before shutdown!)

// don't process more than N groups in parallel: better have some cpu & mem when importing hard!
var LimitBatchParallel = 16

var InitialBatchChannelSize = 16384 // @AI: DO NOT CHANGE THIS!!!! per group cache channel size. should be less or equal to MaxBatch in processor aka MaxReadLinesXover in nntp-client-commands

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

// MsgIdTmpCacheItem represents a cached message ID item - matches processor definition
type MsgIdTmpCacheItem struct {
	MessageId    string
	ArtNum       int64
	RootArticle  int64 // Thread root article number (0 if this IS the root)
	IsThreadRoot bool  // True if this article is a thread root
}

// OverviewBatch represents a staged overview waiting for batch processing
type OverviewBatch struct {
	Article   *models.Article
	Newsgroup *string
}

// ThreadCacheBatch represents a staged thread cache initialization waiting for batch processing
type ThreadCacheBatch struct {
	Newsgroup  string
	ThreadRoot int64
	Article    *models.Article
}

type ThreadingProcessor interface {
	MsgIdExists(group *string, messageID string) bool
	// Add methods for history and cache operations
	AddProcessedArticleToHistory(msgIdItem *history.MessageIdItem, newsgroup *string, articleNumber int64)
	// Add method for finding thread roots - matches proc_MsgIDtmpCache.go signature (updated to use pointer)
	FindThreadRootInCache(groupName *string, refs []string) *MsgIdTmpCacheItem
	CheckNoMoreWorkInHistory() bool
}

// SetProcessor sets the threading processor callback interface
func (c *SQ3batch) SetProcessor(proc ThreadingProcessor) {
	c.proc = proc
}

type SQ3batch struct {
	db           *Database          // Reference to the main database
	proc         ThreadingProcessor // Threading processor interface for message ID checks
	orchestrator *BatchOrchestrator // Smart orchestrator for batch processing

	GMux     sync.RWMutex           // Mutex for TasksMap to ensure thread safety
	TasksMap map[string]*BatchTasks // Map which holds newsgroup cron taskspointers
}

type BatchTasks struct {
	Newsgroup *string

	// Single unified batch processing
	BATCHmux        sync.RWMutex
	BATCHchan       chan *OverviewBatch // Single channel for all batch items
	BATCHprocessing bool                // Flag to indicate if batch processing is ongoing
}

func NewSQ3batch(db *Database) *SQ3batch {
	batch := &SQ3batch{
		db:       db,
		TasksMap: make(map[string]*BatchTasks, 128), // Initialize TasksMap with a capacity for initial cap of 4096 Newsgroups
	}
	batch.orchestrator = NewBatchOrchestrator(batch)
	return batch
}

// ArticleWrap wraps an Article with its number for batch processing
// LEGACY: This structure is kept for backward compatibility with threading channels
// but is no longer used in the modern batch processing pipeline. New batch processing
// works directly with OverviewBatch + article numbers for better performance.
type ArticleWrap struct {
	Newsgroup *string // Newsgroup name (don't store GroupDBs pointer - it might be closed)
	A         *models.Article
	T         *models.Thread   // Optional thread information, if available
	O         *models.Overview // Optional overview information, if available
	N         int64            // Article number (restored for new batching architecture)
}

// #1 Entry Point for capturing articles / overview data for batched processing
func (sq *SQ3batch) BatchCaptureOverviewForLater(newsgroupPtr *string, article *models.Article) {
	BatchDividerChan <- &OverviewBatch{
		Article:   article,
		Newsgroup: newsgroupPtr,
	}
}

// GetNewsgroupPointer returns a pointer to the newsgroup name in TasksMap
func (sq *SQ3batch) GetNewsgroupPointer(newsgroup string) *string {
	sq.GMux.RLock() // Lock the mutex to ensure thread safety
	if ptr, exists := sq.TasksMap[newsgroup]; exists {
		sq.GMux.RUnlock()
		return ptr.Newsgroup
	}
	sq.GMux.RUnlock()
	// Create a new key and add it to the map
	sq.GMux.Lock()
	if ptr, exists := sq.TasksMap[newsgroup]; exists {
		sq.GMux.Unlock()
		return ptr.Newsgroup
	}
	batchTasks := &BatchTasks{
		Newsgroup: &newsgroup,
		BATCHchan: make(chan *OverviewBatch, InitialBatchChannelSize),
	}
	sq.TasksMap[newsgroup] = batchTasks
	ptr := sq.TasksMap[newsgroup]
	sq.GMux.Unlock()
	return ptr.Newsgroup
}

// GetChan returns the channel for a specific newsgroup
func (sq *SQ3batch) GetChan(newsgroup *string) chan *OverviewBatch {
	sq.GMux.RLock()
	defer sq.GMux.RUnlock()
	if ptr, exists := sq.TasksMap[*newsgroup]; exists {
		return ptr.BATCHchan
	}
	return nil
}

// GetOrCreateTasksMapKey returns a pointer to the BatchTasks for a specific newsgroup
func (sq *SQ3batch) GetOrCreateTasksMapKey(newsgroup string) *BatchTasks {
	// Check if the group already exists in the map
	sq.GMux.RLock() // Lock the mutex to ensure thread safety
	if bt, exists := sq.TasksMap[newsgroup]; exists {
		sq.GMux.RUnlock()
		return bt // Return existing ptr
	}
	sq.GMux.RUnlock()
	// Create a new key and add it to the map
	sq.GMux.Lock()
	if batchTasks, exists := sq.TasksMap[newsgroup]; exists {
		sq.GMux.Unlock()
		return batchTasks
	}
	batchTasks := &BatchTasks{
		Newsgroup: &newsgroup,
		BATCHchan: make(chan *OverviewBatch, InitialBatchChannelSize),
	}
	sq.TasksMap[newsgroup] = batchTasks
	sq.GMux.Unlock()
	return batchTasks
}

// CheckNoMoreWorkInMaps checks if all batch channels are empty and not processing
func (c *SQ3batch) CheckNoMoreWorkInMaps() bool {
	if len(BatchDividerChan) > 0 {
		return false
	}
	if c.proc == nil {
		log.Printf("CheckNoMoreWorkInMaps c.proc not set")
		return true
	}
	if !c.proc.CheckNoMoreWorkInHistory() {
		log.Printf("[CRON-SHUTDOWN] History still has work")
		return false
	}
	c.GMux.RLock()         // Lock the mutex to ensure thread safety
	defer c.GMux.RUnlock() // Ensure we unlock the mutex when done
	// Check if all maps are empty and not processing
	//log.Printf("[CRON-SHUTDOWN] do CheckNoMoreWorkInMaps...")

	// Iterate through all tasks to check channels
	for newsgroup, tasks := range c.TasksMap {
		tasks.BATCHmux.RLock()
		isEmpty := len(tasks.BATCHchan) == 0 && !tasks.BATCHprocessing
		batchChan, batchProc := len(tasks.BATCHchan), tasks.BATCHprocessing
		tasks.BATCHmux.RUnlock()

		if !isEmpty {
			log.Printf("[CRON-SHUTDOWN] Work remaining in group '%s': BATCH(chan:%d,proc:%v)",
				newsgroup, batchChan, batchProc)
			return false // If any channel has work or is processing, return false
		}
	}

	//log.Printf("[CRON-SHUTDOWN] All clear! No more work!")
	return true
}

var QueryChan = make(chan struct{}, LimitBatchParallel)
var LimitChan = make(chan struct{}, LimitBatchParallel)
var LPending = make(chan struct{}, 1)

func LockQueryChan() {
	//QueryChan <- struct{}{}
}

func ReturnQueryChan() {
	//<-QueryChan
}

func LockLimitChan() bool {
	select {
	case LimitChan <- struct{}{}:
		// Successfully locked
	default:
		return false
	}
	return true
}

func ReturnLimitChan() {
	<-LimitChan
}

func LockPending() bool {
	select {
	case LPending <- struct{}{}:
		// Successfully locked
	default:
		return false
	}
	return true
}

func ReturnPending() {
	<-LPending
}

// processAllPendingBatches processes all pending batches in the correct sequential order
func (c *SQ3batch) processAllPendingBatches(wgProcessAllBatches *sync.WaitGroup) {
	if !LockPending() {
		log.Printf("[BATCH] processAllPendingBatches: LockPending failed")
		return
	}
	defer ReturnPending()

	// Get a snapshot of tasks to avoid holding the lock too long
	//log.Printf("[BATCH-DEBUG] processAllPendingBatches: acquiring GMux.RLock to get tasks snapshot")
	c.GMux.RLock()
	tasksToProcess := make([]*BatchTasks, 0, len(c.TasksMap)) // Pre-allocate with capacity
	queuedItems := 0
	for _, task := range c.TasksMap {
		queued := len(task.BATCHchan)
		if queued > 0 { // process only below threshold
			tasksToProcess = append(tasksToProcess, task)
		}
		queuedItems += queued
	}
	c.GMux.RUnlock()
	if len(tasksToProcess) == 0 && queuedItems == 0 {
		return
	}
	//log.Printf("[BATCH-DEBUG] processAllPendingBatches: released GMux.RLock, found %d tasks with work (%d total queued items)", len(tasksToProcess), queuedItems)

	//start := time.Now()
	// Process each task without holding the main lock
	toProcess := len(tasksToProcess)
	launched, launchedTotal := 0, 0

	log.Printf("[BATCH] processAllPendingBatches! %d tasks toProcess, %d queued", toProcess, queuedItems)
	//doWork:
	//log.Printf("[BATCH-DEBUG] processAllPendingBatches: entering doWork loop, toProcess=%d", toProcess)
	//startLoop := time.Now() // Reset start time for each iteration
	for _, task := range tasksToProcess {
		if len(task.BATCHchan) == 0 {
			toProcess--
			//log.Printf("[BATCH-DEBUG] processAllPendingBatches: task '%s' has empty channel, marking as done", *task.Newsgroup)
			continue
		}
		// Check if this newsgroup is already processing
		//log.Printf("[BATCH-DEBUG] processAllPendingBatches: checking if task '%s' is already processing", *task.Newsgroup)
		task.BATCHmux.Lock()
		if task.BATCHprocessing {
			task.BATCHmux.Unlock()
			toProcess--
			log.Printf("[BATCH-DEBUG] processAllPendingBatches: task '%s' already processing, skipping", *task.Newsgroup)
			continue
		}
		//log.Printf("[BATCH-DEBUG] processAllPendingBatches: setting task '%s' processing flag to true", *task.Newsgroup)
		task.BATCHprocessing = true
		task.BATCHmux.Unlock()

		if !LockLimitChan() {
			//log.Printf("[BATCH-DEBUG] processAllPendingBatches: LimitChan acquisition failed for task '%s', resetting processing flag", *task.Newsgroup)
			task.BATCHmux.Lock()
			task.BATCHprocessing = false
			task.BATCHmux.Unlock()
			time.Sleep(100 * time.Millisecond)
			continue
		}

		//log.Printf("[BATCH-DEBUG] processAllPendingBatches: LimitChan acquired for task '%s', launching goroutine", *task.Newsgroup)
		toProcess--
		launched++
		launchedTotal++
		wgProcessAllBatches.Add(1)

		go func(task *BatchTasks, wgProcessAllBatches *sync.WaitGroup) {
			defer wgProcessAllBatches.Done()
			//log.Printf("[BATCH] RUN processAllPendingBatches: processNewsgroupBatch task='%s'", *task.Newsgroup)
			gostart := time.Now()
			c.processNewsgroupBatch(task)
			log.Printf("[BATCH] END processAllPendingBatches: processNewsgroupBatch task='%s' took %v", *task.Newsgroup, time.Since(gostart))
		}(task, wgProcessAllBatches) // Pass the task and wait group
		if toProcess <= 0 {
			//log.Printf("[BATCH-DEBUG] processAllPendingBatches: no more tasks to process, breaking out of loop")
			break
		}
	} // end for tasksToProcess
	//log.Printf("[BATCH] processAllPendingBatches: launched %d goroutines, remaining tasks %d / %d, done %d", launched, len(tasksToProcess)-toProcess, len(tasksToProcess), len(doneProcessing))
	wgProcessAllBatches.Wait() // Wait for all goroutines to finish
	if toProcess == 0 {
		log.Printf("[BATCH] processAllPendingBatches: all %d tasks processed, nothing left to do", len(tasksToProcess))
	} else {
		log.Printf("[BATCH-DEBUG] processAllPendingBatches: still have %d tasks left to process", toProcess)
	}
}

// processNewsgroupBatch processes a single newsgroup's batch in the correct sequential order:
// 1. Complete article insertion (unified overview + article data)
// 2. Threading processing (relationships)
// 3. Thread cache updates
func (c *SQ3batch) processNewsgroupBatch(task *BatchTasks) {
	startTime := time.Now()
	defer func(task *BatchTasks, startTime time.Time) {
		task.BATCHmux.Lock()
		task.BATCHprocessing = false
		task.BATCHmux.Unlock()
		//totalDuration := time.Since(startTime)
		//log.Printf("[BATCH] processNewsgroupBatch newsgroup '%s' took %v: now Return LimitChan", *task.Newsgroup, totalDuration)
		ReturnLimitChan()
		//log.Printf("[BATCH] processNewsgroupBatch newsgroup '%s' returned LimitChan", *task.Newsgroup)
	}(task, startTime)

	// Collect all batches for this newsgroup
	batches := make([]*OverviewBatch, 0, maxBatchSize)

	// Drain the channel
drainChannel:
	for len(batches) < maxBatchSize {
		select {
		case batch := <-task.BATCHchan:
			batches = append(batches, batch)
		default:
			break drainChannel // Channel empty
		}
	}

	if len(batches) == 0 {
		return
	}

	log.Printf("[BATCH] processNewsgroupBatch %d tasks: newsgroup '%s'", len(batches), *task.Newsgroup)

retry:
	/*
		// Get database connection for this newsgroup
		groupDBs, err := c.db.GetGroupDBs(*task.Newsgroup)
		if err != nil {
			log.Printf("[BATCH] processNewsgroupBatch Failed to get database for group '%s': %v", *task.Newsgroup, err)
			return
		}
	*/
	// PHASE 1: Insert complete articles (overview + article data unified) and get article numbers
	articleNumbers, groupDBs, err := c.batchInsertOverviewsWithDBs(*task.Newsgroup, batches)
	if err != nil {
		log.Printf("[BATCH] processNewsgroupBatch Failed to process small batch for group '%s': %v", *task.Newsgroup, err)
		time.Sleep(time.Second)
		goto retry
	}
	defer groupDBs.Return(c.db) // Ensure proper cleanup
	if len(articleNumbers) != len(batches) {
		log.Printf("[BATCH] processNewsgroupBatch Warning: Expected %d article numbers, got %d", len(batches), len(articleNumbers))
		return
	}

	// Update all articles with their assigned numbers for subsequent processing
	// REFACTORED: Previously used ArticleWrap intermediary structure, now works directly
	// with OverviewBatch + article numbers for better performance and less memory overhead
	var maxArticleNum int64 = 0
	for i, batch := range batches {
		// Update the article with its number for subsequent processing
		batch.Article.ArticleNum = articleNumbers[i]

		// Track max article number for newsgroup stats
		if articleNumbers[i] > maxArticleNum {
			maxArticleNum = articleNumbers[i]
		}
	}

	// PHASE 2: Process threading for all articles (reusing the same DB connection)
	//log.Printf("[BATCH] processNewsgroupBatch Starting threading phase for %d articles in group '%s'", len(batches), *task.Newsgroup)
	start := time.Now()
	err = c.batchProcessThreadingWithDBs(task.Newsgroup, batches, articleNumbers, groupDBs)
	threadingDuration := time.Since(start)
	if err != nil {
		log.Printf("[BATCH] processNewsgroupBatch Failed to process threading for group '%s' after %v: %v", *task.Newsgroup, threadingDuration, err)
		return
	}
	//log.Printf("[BATCH] processNewsgroupBatch Completed threading phase for group '%s' in %v", *task.Newsgroup, threadingDuration)

	// PHASE 3: Handle history and processor cache updates
	//log.Printf("[BATCH] processNewsgroupBatch Starting history/cache updates for %d articles in group '%s'", len(batches), *task.Newsgroup)
	start = time.Now()
	for i, batch := range batches {
		//log.Printf("[BATCH] processNewsgroupBatch Updating history/cache for article %d/%d in group '%s'", i+1, len(batches), *task.Newsgroup)
		c.proc.AddProcessedArticleToHistory(batch.Article.MsgIdItem, task.Newsgroup, articleNumbers[i])
		// Clear references to help with memory management
		//batch.Article = nil
	}
	//historyDuration := time.Since(start)
	//log.Printf("[BATCH] processNewsgroupBatch Completed history/cache updates for group '%s' in %v", *task.Newsgroup, historyDuration)

	// Clear batches slice and trigger GC for large batches
	/*
		if len(batches) > 100 {
			runtime.GC()
		}
	*/
	batches = nil
	// Update newsgroup statistics with retryable transaction to avoid race conditions
	increment := len(articleNumbers)
	// Safety check for nil database connection
	if c.db == nil || c.db.mainDB == nil {
		log.Printf("[BATCH] processNewsgroupBatch Main database connection is nil, cannot update newsgroup stats for '%s'", *task.Newsgroup)
		err = fmt.Errorf("processNewsgroupBatch main database connection is nil")
	} else {
		//LockQueryChan()
		//defer ReturnQueryChan()
		// Use retryable transaction to prevent race conditions between concurrent batches
		err = retryableTransactionExec(c.db.mainDB, func(tx *sql.Tx) error {
			// Use UPSERT to handle both new and existing newsgroups
			_, txErr := tx.Exec(`
				INSERT INTO newsgroups (name, message_count, last_article, updated_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(name) DO UPDATE SET
					message_count = message_count + excluded.message_count,
					last_article = CASE
						WHEN excluded.last_article > last_article THEN excluded.last_article
						ELSE last_article
					END,
					updated_at = excluded.updated_at`,
				*task.Newsgroup, increment, maxArticleNum, time.Now())
			return txErr
		})

		if err == nil {
			//log.Printf("[BATCH] processNewsgroupBatch Updated newsgroup '%s' stats: +%d articles, max_article=%d", *task.Newsgroup, increment, maxArticleNum)
		}
	}
	if err != nil {
		log.Printf("[BATCH] processNewsgroupBatch Failed to update newsgroup stats for '%s': %v", *task.Newsgroup, err)
	}

	log.Printf("[BATCH] processNewsgroupBatch processed %d articles, newsgroup '%s' took %v", increment, *task.Newsgroup, time.Since(start))
}

// NEW: batchInsertUnifiedArticles - performs REAL batch insert with unified article+overview data in single SQL statement
func (c *SQ3batch) batchInsertOverviews(newsgroup string, batches []*OverviewBatch) ([]int64, error) {
	articleNumbers, _, err := c.batchInsertOverviewsWithDBs(newsgroup, batches)
	return articleNumbers, err
}

// batchInsertOverviewsWithDBs - returns both article numbers and the GroupDBs connection for reuse
func (c *SQ3batch) batchInsertOverviewsWithDBs(newsgroup string, batches []*OverviewBatch) ([]int64, *GroupDBs, error) {
	groupDBs, err := c.db.GetGroupDBs(newsgroup)
	if err != nil {
		log.Printf("[OVB-BATCH] Failed to get database for group '%s': %v", newsgroup, err)
		return nil, nil, fmt.Errorf("failed to get database for group '%s': %w", newsgroup, err)
	}
	// Note: Do NOT defer groupDBs.Return(c.db) here - caller must handle it

	if len(batches) == 0 {
		return nil, groupDBs, fmt.Errorf("no batches to process for group '%s'", newsgroup)
	}

	if len(batches) <= maxBatchSize {
		// Small batch - process directly
		articleNumbers, err := c.processSingleOverviewBatch(groupDBs, batches)
		if err != nil {
			log.Printf("[OVB-BATCH] Failed to process small batch for group '%s': %v", newsgroup, err)
			return nil, groupDBs, fmt.Errorf("failed to process small batch for group '%s': %w", newsgroup, err)
		}
		return articleNumbers, groupDBs, nil
	}

	// Large batch - split into chunks
	var allArticleNumbers []int64
	for i := 0; i < len(batches); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(batches) {
			end = len(batches)
		}

		chunk := batches[i:end]
		chunkNumbers, err := c.processSingleOverviewBatch(groupDBs, chunk)
		if err != nil {
			log.Printf("[OVB-BATCH] Failed to process chunk %d-%d for group '%s': %v", i, end, newsgroup, err)
			return nil, groupDBs, fmt.Errorf("failed to process chunk %d-%d for group '%s': %w", i, end, newsgroup, err)
		}
		if len(chunkNumbers) == 0 {
			// If any chunk fails, mark remaining as failed
			for j := i; j < len(batches); j++ {
				allArticleNumbers = append(allArticleNumbers, 0)
			}
			break
		}

		allArticleNumbers = append(allArticleNumbers, chunkNumbers...)
	}

	return allArticleNumbers, groupDBs, nil
}

// processSingleUnifiedArticleBatch handles a single batch that's within SQLite limits
func (c *SQ3batch) processSingleOverviewBatch(groupDBs *GroupDBs, batches []*OverviewBatch) ([]int64, error) {
	// Get timestamp once for the entire batch instead of per article
	importedAt := time.Now()

	// Use a transaction for the batch insert
	tx, err := groupDBs.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Prepare the INSERT statement once
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO articles (message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, path, headers_json, body_text, downloaded, imported_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	// Execute the prepared statement for each batch item
	for _, batch := range batches {
		_, err := stmt.Exec(
			batch.Article.MessageID,   // message_id
			batch.Article.Subject,     // subject
			batch.Article.FromHeader,  // from_header
			batch.Article.DateSent,    // date_sent
			batch.Article.DateString,  // date_string
			batch.Article.References,  // references
			batch.Article.Bytes,       // bytes
			batch.Article.Lines,       // lines
			batch.Article.ReplyCount,  // reply_count
			batch.Article.Path,        // path
			batch.Article.HeadersJSON, // headers_json
			batch.Article.BodyText,    // body_text
			1,                         // downloaded (set to 1 since we have the article)
			importedAt,                // imported_at - shared timestamp for batch
		)
		if err != nil {
			return nil, fmt.Errorf("failed to execute insert for message_id %s: %w", batch.Article.MessageID, err)
		}
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Single batch SELECT query to get all article numbers at once
	batchSize := len(batches)
	args := make([]any, batchSize)
	for i, batch := range batches {
		args[i] = batch.Article.MessageID
	}

	selectSQL := `SELECT message_id, article_num FROM articles WHERE message_id IN (` +
		getPlaceholders(batchSize) + `) ORDER BY article_num`

	rows, err := groupDBs.DB.Query(selectSQL, args...)
	if err != nil {
		log.Printf("[OVB-BATCH] group '%s': Failed to execute batch select: %v", groupDBs.Newsgroup, err)
		return nil, fmt.Errorf("failed to execute batch select for group '%s': %w", groupDBs.Newsgroup, err)
	}
	defer rows.Close()

	// Create map of messageID -> article_num with pre-allocated capacity
	idToArticleNum := make(map[string]int64, batchSize)
	for rows.Next() {
		var messageID string
		var articleNum int64
		if err := rows.Scan(&messageID, &articleNum); err != nil {
			log.Printf("[OVB-BATCH] group '%s': Failed to scan article number: %v", groupDBs.Newsgroup, err)
			continue
		}
		idToArticleNum[messageID] = articleNum
	}

	//log.Printf("[OVB-BATCH] group '%s': SELECT query returned %d article numbers for %d requested messageIDs", groupDBs.Newsgroup, rowCount, len(messageIDs))

	// Build result array in same order as input batches
	articleNumbers := make([]int64, len(batches))
	foundCount := 0
	for i, batch := range batches {
		if articleNum, exists := idToArticleNum[batch.Article.MessageID]; exists {
			articleNumbers[i] = articleNum
			foundCount++
		} else {
			articleNumbers[i] = 0 // Mark as failed

			//log.Printf("[OVB-BATCH] group '%s': No article number found for messageID: %s",	groupDBs.Newsgroup, batch.Article.MessageID)
		}
	}

	//log.Printf("[OVB-BATCH] group '%s': Final result: %d/%d article numbers found",	groupDBs.Newsgroup, foundCount, len(batches))

	return articleNumbers, nil
}

// batchProcessThreading processes all threading operations for a group in a single batch
func (c *SQ3batch) batchProcessThreading(groupName *string, batches []*OverviewBatch, articleNumbers []int64) error {
	groupDBs, err := c.db.GetGroupDBs(*groupName)
	if err != nil {
		return fmt.Errorf("failed to get database for group '%s': %w", *groupName, err)
	}
	defer groupDBs.Return(c.db)

	return c.batchProcessThreadingWithDBs(groupName, batches, articleNumbers, groupDBs)
}

// batchProcessThreadingWithDBs processes all threading operations using existing GroupDBs connection
func (c *SQ3batch) batchProcessThreadingWithDBs(groupName *string, batches []*OverviewBatch, articleNumbers []int64, groupDBs *GroupDBs) error {
	if len(batches) == 0 || len(articleNumbers) == 0 {
		return nil
	}
	if len(batches) != len(articleNumbers) {
		return fmt.Errorf("mismatch between batches (%d) and article numbers (%d)", len(batches), len(articleNumbers))
	}
	//log.Printf("[THR-BATCH] group '%s': %d articles to process", *groupName, len(batches))

	// No need to get database connections - reuse existing groupDBs
	// No need for defer groupDBs.Return(c.db) - caller handles it

	// Separate articles into threads/roots and replies for batch processing
	var threadRootIndices []int
	var replyIndices []int

	for i, batch := range batches {
		// Check if this is a thread root (no references) or a reply
		refs := utils.ParseReferences(batch.Article.References)
		if len(refs) == 0 {
			threadRootIndices = append(threadRootIndices, i)
		} else {
			replyIndices = append(replyIndices, i)
		}
	}

	//log.Printf("[THR-BATCH] group '%s': Separated %d thread roots and %d replies", *groupName, len(threadRootIndices), len(replyIndices))

	// Process thread roots first (they need to exist before replies can reference them)
	if len(threadRootIndices) > 0 {
		//log.Printf("[THR-BATCH] group '%s': Processing %d thread roots", *groupName, len(threadRootIndices))
		//start := time.Now()
		if err := c.batchProcessThreadRoots(groupDBs, batches, articleNumbers, threadRootIndices); err != nil {
			log.Printf("[THR-BATCH] group '%s': Failed to batch process thread roots: %v", *groupName, err)
			// Continue processing - don't fail the whole batch
		} else {
			//log.Printf("[THR-BATCH] group '%s': Completed %d thread roots in %v", *groupName, len(threadRootIndices), time.Since(start))
		}
	}

	// Process replies
	if len(replyIndices) > 0 {
		//log.Printf("[THR-BATCH] group '%s': Processing %d replies", *groupName, len(replyIndices))
		//start := time.Now()
		if err := c.batchProcessReplies(groupDBs, batches, articleNumbers, replyIndices); err != nil {
			log.Printf("[THR-BATCH] group '%s': Failed to batch process replies: %v", *groupName, err)
			// Continue processing - don't fail the whole batch
		} else {
			//log.Printf("[THR-BATCH] group '%s': Completed %d replies in %v", *groupName, len(replyIndices), time.Since(start))
		}
	}

	return nil
}

// batchProcessThreadRoots processes thread root articles in TRUE batch
func (c *SQ3batch) batchProcessThreadRoots(groupDBs *GroupDBs, batches []*OverviewBatch, articleNumbers []int64, rootIndices []int) error {
	if len(rootIndices) == 0 {
		return nil
	}

	// Pre-allocate slices to avoid repeated memory allocations
	rootCount := len(rootIndices)
	valuesClauses := make([]string, 0, rootCount)
	args := make([]interface{}, 0, rootCount*2) // 2 args per root

	// Reuse the same placeholder string
	const rootPlaceholder = "(?, NULL, ?, 0, 0)"

	for _, i := range rootIndices {
		articleNum := articleNumbers[i]
		valuesClauses = append(valuesClauses, rootPlaceholder)
		args = append(args, articleNum, articleNum) // root_article, child_article
	}

	// Build the complete batch INSERT statement
	sql := `INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES ` +
		strings.Join(valuesClauses, ", ")

	// Execute the SINGLE batch INSERT with mutex protection and retry logic
	_, err := retryableExec(groupDBs.DB, sql, args...)

	if err != nil {
		return fmt.Errorf("failed to execute batch thread insert (%d records): %w", len(rootIndices), err)
	}

	// Post-processing: Initialize thread cache and update processor cache
	for _, i := range rootIndices {
		articleNum := articleNumbers[i]
		article := batches[i].Article
		// Initialize thread cache
		if err := c.db.InitializeThreadCache(groupDBs, articleNum, article); err != nil {
			log.Printf("[P-BATCH] group '%s': Failed to initialize thread cache for root %d: %v", groupDBs.Newsgroup, articleNum, err)
			// Don't fail the whole operation for cache errors
		}
	}

	//log.Printf("[P-BATCH] group '%s': Successfully batch inserted %d thread roots", groupDBs.Newsgroup, len(rootIndices))
	return nil
}

// batchProcessReplies processes reply articles in TRUE batch
func (c *SQ3batch) batchProcessReplies(groupDBs *GroupDBs, batches []*OverviewBatch, articleNumbers []int64, replyIndices []int) error {
	if len(replyIndices) == 0 {
		return nil
	}

	// Pre-allocate collections to avoid repeated memory allocations
	replyCount := len(replyIndices)
	parentMessageIDs := make(map[string]int, replyCount) // Pre-allocate with expected capacity
	replyData := make([]struct {
		batchIndex int
		articleNum int64
		threadRoot int64
		parentID   string
	}, 0, replyCount) // Pre-allocate slice with capacity

	// Process each reply to gather data
	//log.Printf("[P-BATCH] group '%s': Analyzing %d replies for threading data", groupDBs.Newsgroup, len(replyIndices))
	for _, i := range replyIndices {
		batch := batches[i]
		articleNum := articleNumbers[i]
		refs := utils.ParseReferences(batch.Article.References)
		if len(refs) == 0 {
			continue // Should not happen, but safety check
		}

		parentMessageID := refs[len(refs)-1] // Use the last reference as the parent
		parentMessageIDs[parentMessageID]++

		// Find thread root and collect data
		var threadRoot int64
		if c.proc != nil {
			if root, err := c.findThreadRootForBatch(groupDBs, refs); err == nil {
				threadRoot = root
			}
		}

		replyData = append(replyData, struct {
			batchIndex int
			articleNum int64
			threadRoot int64
			parentID   string
		}{i, articleNum, threadRoot, parentMessageID})
	}

	//log.Printf("[P-BATCH] group '%s': Found %d unique parent messages for %d replies", groupDBs.Newsgroup, len(parentMessageIDs), len(replyData))

	// Batch update reply counts for articles table (single call since overview is unified)
	if len(parentMessageIDs) > 0 {
		//log.Printf("[P-BATCH] group '%s': Updating reply counts for %d parent messages", groupDBs.Newsgroup, len(parentMessageIDs))
		//start := time.Now()
		if err := c.batchUpdateReplyCounts(groupDBs, parentMessageIDs, "articles"); err != nil {
			log.Printf("[P-BATCH] group '%s': Failed to batch update article reply counts: %v", groupDBs.Newsgroup, err)
		} else {
			//log.Printf("[P-BATCH] group '%s': Updated reply counts in %v", groupDBs.Newsgroup, time.Since(start))
		}
	}

	// Collect all thread cache updates for TRUE batch processing
	threadUpdates := make(map[int64][]threadCacheUpdateData) // [threadRoot] -> list of updates
	cacheUpdateCount := 0

	for _, data := range replyData {
		if data.threadRoot > 0 {
			batch := batches[data.batchIndex]
			threadUpdates[data.threadRoot] = append(threadUpdates[data.threadRoot], threadCacheUpdateData{
				childArticleNum: data.articleNum,
				childDate:       batch.Article.DateSent,
			})
			cacheUpdateCount++
		}
	}

	// Execute ALL thread cache updates in a single transaction
	if len(threadUpdates) > 0 {
		//start := time.Now()
		if err := c.batchUpdateThreadCache(groupDBs, threadUpdates); err != nil {
			log.Printf("[P-BATCH] group '%s': Failed to batch update thread cache: %v", groupDBs.Newsgroup, err)
		} else {
			//log.Printf("[P-BATCH] group '%s': Completed ALL %d thread cache updates across %d threads in single transaction in %v",				groupDBs.Newsgroup, cacheUpdateCount, len(threadUpdates), time.Since(start))
		}
	}

	//log.Printf("[P-BATCH] group '%s': Successfully batch processed %d replies", groupDBs.Newsgroup, len(replyIndices))
	return nil
}

// batchUpdateReplyCounts performs batch update of reply counts using CASE WHEN
func (c *SQ3batch) batchUpdateReplyCounts(groupDBs *GroupDBs, parentCounts map[string]int, tableName string) error {
	if len(parentCounts) == 0 {
		return nil
	}

	// Pre-allocate slices to avoid repeated memory allocations
	parentCount := len(parentCounts)
	caseWhenClauses := make([]string, 0, parentCount)
	messageIDs := make([]string, 0, parentCount)
	args := make([]interface{}, 0, parentCount*3) // messageID, count, messageID for WHERE clause

	for messageID, count := range parentCounts {
		caseWhenClauses = append(caseWhenClauses, "WHEN message_id = ? THEN reply_count + ?")
		messageIDs = append(messageIDs, messageID)
		args = append(args, messageID, count)
	}

	// Add message IDs for WHERE IN clause - use efficient placeholder generation
	for _, messageID := range messageIDs {
		args = append(args, messageID)
	}

	// Build the complete batch UPDATE statement
	sql := fmt.Sprintf(`UPDATE %s SET reply_count = CASE %s END WHERE message_id IN (%s)`,
		tableName,
		strings.Join(caseWhenClauses, " "),
		getPlaceholders(len(messageIDs)))

	// Execute the batch UPDATE with appropriate mutex
	switch tableName {
	case "articles":
		_, err := retryableExec(groupDBs.DB, sql, args...)
		return err
	}

	return fmt.Errorf("unknown table name: %s", tableName)
}

// findThreadRootForBatch is a simplified version for batch processing
func (c *SQ3batch) findThreadRootForBatch(groupDBs *GroupDBs, refs []string) (int64, error) {
	if len(refs) == 0 || c.proc == nil {
		return 0, fmt.Errorf("no references or processor not available")
	}

	// Try to find thread root in processor cache first
	groupPtr := c.GetNewsgroupPointer(groupDBs.Newsgroup)
	if cachedRoot := c.proc.FindThreadRootInCache(groupPtr, refs); cachedRoot != nil {
		return cachedRoot.ArtNum, nil
	}

	// Fall back to database search for any referenced message
	for i := len(refs) - 1; i >= 0; i-- {
		refMessageID := refs[i]

		// Check if this article is a thread root with retryable logic
		var rootArticle int64
		threadQuery := `SELECT root_article FROM threads WHERE root_article = (SELECT article_num FROM articles WHERE message_id = ? LIMIT 1) LIMIT 1`
		err := retryableQueryRowScan(groupDBs.DB, threadQuery, []interface{}{refMessageID}, &rootArticle)
		if err == nil {
			return rootArticle, nil
		}
	}

	return 0, fmt.Errorf("could not find thread root for any reference")
}

// batchUpdateThreadCache performs TRUE batch update of thread cache entries in a single transaction with retry logic
func (c *SQ3batch) batchUpdateThreadCache(groupDBs *GroupDBs, threadUpdates map[int64][]threadCacheUpdateData) error {
	if len(threadUpdates) == 0 {
		return nil
	}
	var updatedCount int
	var initializedCount int

	// Use retryableTransactionExec for SQLite lock safety
	err := retryableTransactionExec(groupDBs.DB, func(tx *sql.Tx) error {
		// Reset ShutDownCounters for each retry attempt
		updatedCount = 0
		initializedCount = 0

		// Prepare statements for batch operations
		selectStmt, err := tx.Prepare(`SELECT child_articles, message_count FROM thread_cache WHERE thread_root = ?`)
		if err != nil {
			return fmt.Errorf("failed to prepare select statement: %w", err)
		}
		defer selectStmt.Close()

		updateStmt, err := tx.Prepare(`UPDATE thread_cache SET child_articles = ?, message_count = ?, last_child_number = ?, last_activity = ? WHERE thread_root = ?`)
		if err != nil {
			return fmt.Errorf("failed to prepare update statement: %w", err)
		}
		defer updateStmt.Close()

		initStmt, err := tx.Prepare(`INSERT INTO thread_cache (thread_root, root_date, message_count, child_articles, last_child_number, last_activity) VALUES (?, ?, 1, '', ?, ?) ON CONFLICT(thread_root) DO UPDATE SET root_date = excluded.root_date, last_child_number = excluded.last_child_number, last_activity = excluded.last_activity`)
		if err != nil {
			return fmt.Errorf("failed to prepare init statement: %w", err)
		}
		defer initStmt.Close()

		// Process each thread root and its accumulated updates
		for threadRoot, updates := range threadUpdates {
			// Get current cache state with retryable logic
			var currentChildren string
			var currentCount int

			err := retryableStmtQueryRowScan(selectStmt, []interface{}{threadRoot}, &currentChildren, &currentCount)
			if err != nil {
				// Thread cache entry doesn't exist, initialize it with the first update
				firstUpdate := updates[0]
				_, err = retryableStmtExec(initStmt, threadRoot, firstUpdate.childDate, threadRoot, firstUpdate.childDate)
				if err != nil {
					log.Printf("[BATCH-CACHE] Failed to initialize thread cache for root %d after retries: %v", threadRoot, err)
					return fmt.Errorf("failed to initialize thread cache for root %d: %w", threadRoot, err)
				}
				currentChildren = ""
				currentCount = 0
				initializedCount++
			}

			// Calculate new state by applying all updates for this thread
			newChildren := currentChildren
			var lastChildNum int64
			var lastActivity time.Time

			for _, update := range updates {
				// Add child to the list
				if newChildren == "" {
					newChildren = fmt.Sprintf("%d", update.childArticleNum)
				} else {
					newChildren = newChildren + "," + fmt.Sprintf("%d", update.childArticleNum)
				}
				lastChildNum = update.childArticleNum
				if update.childDate.After(lastActivity) {
					lastActivity = update.childDate
				}
			}

			newCount := currentCount + len(updates)

			// Execute the batch update for this thread with retryable logic
			_, err = retryableStmtExec(updateStmt, newChildren, newCount, lastChildNum, lastActivity, threadRoot)

			if err != nil {
				log.Printf("[BATCH-CACHE] Failed to update thread cache for root %d after retries: %v", threadRoot, err)
				return fmt.Errorf("failed to update thread cache for root %d: %w", threadRoot, err)
			}
			updatedCount++

			// Update memory cache if available
			if c.db.MemThreadCache != nil {
				c.db.MemThreadCache.UpdateThreadMetadata(groupDBs.Newsgroup, threadRoot, newCount, lastActivity, newChildren)
			}
		}

		return nil // Transaction will be committed by retryableTransactionExec
	})

	if err != nil {
		return fmt.Errorf("failed to execute thread cache batch transaction: %w", err)
	}
	/*
		log.Printf("[BATCH-CACHE] group '%s': Successfully batch updated %d thread cache entries (initialized %d) in single retryable transaction with %d total updates",
			groupDBs.Newsgroup, updatedCount, initializedCount, len(threadUpdates))
	*/
	return nil
}

type threadCacheUpdateData struct {
	childArticleNum int64
	childDate       time.Time
}

type BatchOrchestrator struct {
	// Configuration
	BatchInterval time.Duration // Timer interval for fallback processing

	// Control
	batch *SQ3batch
}

func NewBatchOrchestrator(batch *SQ3batch) *BatchOrchestrator {
	return &BatchOrchestrator{
		batch:         batch,
		BatchInterval: BatchInterval, // Set to the global BatchInterval (15 seconds)
	}
}

// Start is a convenience method that calls StartOrchestrator
func (o *BatchOrchestrator) StartOrch() {
	go o.StartOrchestrator()
	go o.batch.BatchDivider()
	defer o.batch.db.WG.Done()
	lastFlush := time.Now() // Fixed: initialize to current time, not future time
	var wgProcessAllBatches sync.WaitGroup
	ShutDownCounter := InitialShutDownCounter
	wantShutdown := false
	for {
		time.Sleep(time.Second / 2)
		if o.batch.db.IsDBshutdown() {
			if o.batch.proc == nil {
				log.Printf("[ORCHESTRATOR1] o.batch.proc not set. shutting down.")
				return
			}
			log.Printf("[ORCHESTRATOR1] Database shutdown detected ShutDownCounter=%d", ShutDownCounter)
			o.batch.processAllPendingBatches(&wgProcessAllBatches)
			if !wantShutdown {
				wantShutdown = true
			}

			if !o.batch.CheckNoMoreWorkInMaps() {
				ShutDownCounter = InitialShutDownCounter
				continue
			} else {
				ShutDownCounter--
			}
			if ShutDownCounter <= 0 {
				return
			}
		}
		if !wantShutdown {
			if time.Since(lastFlush) > o.BatchInterval {
				//log.Printf("[ORCHESTRATOR1] Timer triggered - processing all pending batches")
				o.batch.processAllPendingBatches(&wgProcessAllBatches)
				lastFlush = time.Now()
			}
		}
	}
}

// StartOrchestrator runs the main orchestrator loop that monitors channels and sends notifications
func (o *BatchOrchestrator) StartOrchestrator() {
	log.Printf("[ORCHESTRATOR] StartOrchestrator")
	defer o.batch.db.WG.Done()

	// Start timer goroutine for fallback processing
	// Main monitoring loop
	sleep := 1500 // @AI: DO NOT CHANGE THIS!!!!
	ShutDownCounter := InitialShutDownCounter
	wantShutdown := false
	newSleep := sleep
	for {
		// Sleep briefly to avoid busy waiting
		time.Sleep(time.Duration(sleep) * time.Millisecond)
		// Check all channels for threshold breaches
		hasWork := o.checkThresholds()
		//log.Printf("[ORCHESTRATOR2] Current sleep interval: (%d ms) hasWork=%t", sleep, hasWork)
		if o.batch.db.IsDBshutdown() {
			if o.batch.proc == nil {
				log.Printf("[ORCHESTRATOR2] o.batch.proc not set. shutting down.")
				return
			}
			log.Printf("[ORCHESTRATOR2] Database shutdown detected ShutDownCounter=%d", ShutDownCounter)
			sleep = 500
			if !wantShutdown {
				wantShutdown = true
			}
			if !o.batch.CheckNoMoreWorkInMaps() {
				ShutDownCounter = InitialShutDownCounter
				continue
			} else {
				ShutDownCounter--
			}
			if ShutDownCounter <= 0 && !hasWork {
				return
			}
		}
		if !wantShutdown {
			if !hasWork {
				// Exponential backoff with jitter - ensure minimum increment of 2ms
				newSleep = int(float64(sleep) * 1.02) // +2%
				if newSleep <= sleep {
					newSleep = sleep + 2 // Force at least 2ms increment: example if we hit 5ms and we have no more work 5ms * 1.05 results still in 5 and we will turn like crazy in cycles!
				}
				sleep = newSleep
				if sleep > 1500 { // @AI: DO NOT CHANGE THIS!!!!
					sleep = 1500 // @AI: DO NOT CHANGE THIS!!!!
				} // @AI: DO NOT CHANGE THIS!!!!
			} else {
				// Fast recovery when work is found
				sleep = sleep / 3 // @AI: DO NOT CHANGE THIS!!!!
				if sleep < 25 {   // @AI: DO NOT CHANGE THIS!!!!
					sleep = 25 // @AI: DO NOT CHANGE THIS!!!!
				}
			}
		}
	}
}

// checkThresholds monitors all channels and sends notifications when thresholds are exceeded
func (o *BatchOrchestrator) checkThresholds() (haswork bool) {

	o.batch.GMux.RLock()
	tasksToProcess := make([]*BatchTasks, 0, maxTasks) // Pre-allocate with max capacity
	for _, task := range o.batch.TasksMap {
		task.BATCHmux.RLock()
		if task.BATCHprocessing || len(task.BATCHchan) < maxBatchSize {
			task.BATCHmux.RUnlock()
			continue
		}
		task.BATCHmux.RUnlock()
		tasksToProcess = append(tasksToProcess, task)
		if len(tasksToProcess) >= maxTasks {
			break
		}
	}
	o.batch.GMux.RUnlock()

	if len(tasksToProcess) == 0 {
		return false // No tasks to process
	}

	//log.Printf("[ORCHESTRATOR] Checking %d groups for threshold breaches", len(tasksToProcess))

	totalQueued := 0
	willSleep := false
	batchCount := 0
	for _, task := range tasksToProcess {
		batchCount = len(task.BATCHchan)
		if batchCount == 0 {
			continue // skip empty channels
		}
		totalQueued += batchCount

		if batchCount >= maxBatchSize || totalQueued > maxBatchSize {
			haswork = true
			task.BATCHmux.Lock()
			if task.BATCHprocessing {
				//log.Printf("[ORCHESTRATOR] Group '%s' has %d articles but already processing", *task.Newsgroup, batchCount)
				task.BATCHmux.Unlock()
				continue
			}
			task.BATCHprocessing = true
			task.BATCHmux.Unlock()

			if !LockLimitChan() {
				/*
					log.Printf("[ORCHESTRATOR] Threshold exceeded for group '%s': %d articles (threshold: %d) LimitChan acquisition failed, retry later",
						*task.Newsgroup, batchCount, maxBatchSize)
				*/
				//log.Printf("[BATCH-PROC] LimitChan acquisition failed for group '%s', resetting processing flag", *task.Newsgroup)
				task.BATCHmux.Lock()
				task.BATCHprocessing = false
				task.BATCHmux.Unlock()
				return true
			} else {
				log.Printf("[ORCHESTRATOR] Threshold exceeded for group '%s': %d articles (threshold: %d)",
					*task.Newsgroup, batchCount, maxBatchSize)
				go o.batch.processNewsgroupBatch(task)
			}
		} else if batchCount > 0 {
			// Log groups with pending work but below threshold
			/*
				log.Printf("[ORCHESTRATOR-PENDING] Group '%s' has %d articles (below threshold: %d)",
					*task.Newsgroup, batchCount, maxBatchSize)
			*/
		}
	} // end for

	/*
		if totalQueued > 0 {
			log.Printf("[ORCHESTRATOR] Total articles queued across all groups: %d", totalQueued)
		}
	*/
	if willSleep {
		time.Sleep(100 * time.Millisecond)
	}
	return haswork
}

var BatchDividerChan = make(chan *OverviewBatch, 1024)

func (sq *SQ3batch) BatchDivider() {
	var TaskChans = make(map[*string]chan *OverviewBatch)
	var task *OverviewBatch
	var tasks *BatchTasks
	//var BATCHchan chan *OverviewBatch
	for {
		task = <-BatchDividerChan
		if task == nil {
			log.Printf("[BATCH-DIVIDER] Received nil task?!")
			continue
		}
		//log.Printf("[BATCH-DIVIDER] Received task for group '%s'", *task.Newsgroup)
		if TaskChans[task.Newsgroup] == nil {
			tasks = sq.GetOrCreateTasksMapKey(*task.Newsgroup)
			if tasks.BATCHchan == nil {
				log.Printf("[BATCH-DIVIDER] ERROR: No channel found for group '%s'", *task.Newsgroup)
				continue
			}
			tasks.BATCHchan <- task
			TaskChans[task.Newsgroup] = tasks.BATCHchan
		} else {
			TaskChans[task.Newsgroup] <- task
		}
	}
}
