package database

import (
	"database/sql"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// SQLite safety limits: split large batches to avoid parameter/length limits
// SQLite 3.34.1+ supports up to 32766 (vs 999 in older versions)
var BatchInterval = 1 * time.Second
var MaxBatchSize int = 100

// don't process more than N groups in parallel: better have some cpu & mem when importing hard!
var LimitBatchParallel = 16

var InitialBatchChannelSize = MaxBatchSize * 4 // @AI: DO NOT CHANGE THIS!!!! per group cache channel size. should be less or equal to MaxBatch in processor aka MaxReadLinesXover in nntp-client-commands

// Cache for placeholder strings to avoid rebuilding them repeatedly
var placeholderCache sync.Map // map[int]string

const InitialShutDownCounter = 10

// getPlaceholders returns a comma-separated string of SQL placeholders (?) for the given count
func getPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	if v, ok := placeholderCache.Load(count); ok {
		return v.(string)
	}
	var s string
	if count == 1 {
		s = "?"
	} else {
		s = strings.Repeat("?, ", count-1) + "?"
	}
	placeholderCache.Store(count, s)
	return s
}

// Pools to reduce allocation churn for hot-path buffers
var argsPool = sync.Pool{ // *[]any with cap up to MaxBatchSize
	New: func() any {
		buf := make([]any, 0, MaxBatchSize)
		return &buf
	},
}

var idToArticleNumPool = sync.Pool{ // *map[string]int64 pre-sized
	New: func() any {
		m := make(map[string]int64, MaxBatchSize)
		return &m
	},
}

// MsgIdTmpCacheItem represents a cached message ID item - matches processor definition
type MsgIdTmpCacheItem struct {
	MessageId    string
	ArtNum       int64
	RootArticle  int64 // Thread root article number (0 if this IS the root)
	IsThreadRoot bool  // True if this article is a thread root
}

// OverviewBatch represents a staged overview waiting for batch processing
/*
type OverviewBatch struct {
	Article *models.Article
	//Newsgroup *string
}
*/

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
	Mux             sync.RWMutex
	BATCHchan       chan *models.Article // Single channel for all batch items
	BATCHprocessing bool                 // Flag to indicate if batch processing is ongoing
	Expires         time.Time
}

func NewSQ3batch(db *Database) *SQ3batch {
	batch := &SQ3batch{
		db:       db,
		TasksMap: make(map[string]*BatchTasks, 128), // Initialize TasksMap with a capacity for initial cap of 4096 Newsgroups
	}
	batch.orchestrator = NewBatchOrchestrator(batch)
	return batch
}

// #1 Entry Point for capturing articles / overview data for batched processing
func (sq *SQ3batch) BatchCaptureOverviewForLater(newsgroupPtr *string, article *models.Article) {
	article.Mux.Lock()
	if !slices.Contains(article.NewsgroupsPtr, newsgroupPtr) {
		article.NewsgroupsPtr = append(article.NewsgroupsPtr, newsgroupPtr)
	}
	article.Mux.Unlock()
	article.ProcessQueue <- newsgroupPtr // Add to process queue for later processing
	BatchDividerChan <- article
}

func (sq *SQ3batch) ExpireCache() {
	time.Sleep(15 * time.Second)
	sq.GMux.Lock()
	defer sq.GMux.Unlock()
	for k, task := range sq.TasksMap {
		task.Mux.Lock()
		if task.Expires.Before(time.Now()) && len(task.BATCHchan) == 0 {
			log.Printf("[BATCH] Expiring cache for newsgroup '%s'", k)
			// Close the channel to stop further processing
			// Remove from TasksMap
			delete(sq.TasksMap, k)
			task.Mux.Unlock()
			continue
		}
		//log.Printf("[BATCH] Keeping cache for newsgroup '%s', expires at %v", k, task.Expires)
		task.Mux.Unlock()
	}
	go sq.ExpireCache()
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
		// BATCHchan will be created lazily on first enqueue to reduce memory during scan-only paths
		BATCHchan: nil,
		Expires:   time.Now().Add(120 * time.Second), // Set initial expiration time
	}
	sq.TasksMap[newsgroup] = batchTasks
	ptr := sq.TasksMap[newsgroup]
	sq.GMux.Unlock()
	return ptr.Newsgroup
}

// GetChan returns the channel for a specific newsgroup
func (sq *SQ3batch) GetChan(newsgroup *string) chan *models.Article {
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
		// BATCHchan will be created lazily on first enqueue to reduce memory during scan-only paths
		BATCHchan: nil,
		Expires:   time.Now().Add(120 * time.Second), // Set initial expiration time
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
		tasks.Mux.RLock()
		isEmpty := len(tasks.BATCHchan) == 0 && !tasks.BATCHprocessing
		batchChan, batchProc := len(tasks.BATCHchan), tasks.BATCHprocessing
		tasks.Mux.RUnlock()

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
		task.Mux.RLock()
		if task.BATCHchan == nil {
			task.Mux.RUnlock()
			continue
		}
		task.Mux.RUnlock()
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
		task.Mux.Lock()
		if task.BATCHprocessing {
			task.Mux.Unlock()
			toProcess--
			log.Printf("[BATCH-DEBUG] processAllPendingBatches: task '%s' already processing, skipping", *task.Newsgroup)
			continue
		}
		//log.Printf("[BATCH-DEBUG] processAllPendingBatches: setting task '%s' processing flag to true", *task.Newsgroup)
		task.BATCHprocessing = true
		task.Mux.Unlock()

		if !LockLimitChan() {
			//log.Printf("[BATCH-DEBUG] processAllPendingBatches: LimitChan acquisition failed for task '%s', resetting processing flag", *task.Newsgroup)
			task.Mux.Lock()
			task.BATCHprocessing = false
			task.Mux.Unlock()
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
	task.Mux.Lock()
	task.Expires = time.Now().Add(120 * time.Second) // extend expiration time
	task.Mux.Unlock()
	defer func(task *BatchTasks, startTime time.Time) {
		task.Mux.Lock()
		task.BATCHprocessing = false
		task.Expires = time.Now().Add(120 * time.Second) // extend expiration time
		task.Mux.Unlock()
		//totalDuration := time.Since(startTime)
		//log.Printf("[BATCH] processNewsgroupBatch newsgroup '%s' took %v: now Return LimitChan", *task.Newsgroup, totalDuration)
		ReturnLimitChan()
		//log.Printf("[BATCH] processNewsgroupBatch newsgroup '%s' returned LimitChan", *task.Newsgroup)
	}(task, startTime)

	// Collect all batches for this newsgroup
	batches := make([]*models.Article, 0, MaxBatchSize)

	// Drain the channel
drainChannel:
	for len(batches) < MaxBatchSize {
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
	deferred := false
	var groupDBs *GroupDBs
	var err error
retry:
	if groupDBs == nil {
		// Get database connection for this newsgroup
		groupDBs, err = c.db.GetGroupDBs(*task.Newsgroup)
		if err != nil {
			log.Printf("[BATCH] processNewsgroupBatch Failed to get database for group '%s': %v", *task.Newsgroup, err)
			return
		}
	}
	if !deferred {
		defer groupDBs.Return(c.db) // Ensure proper cleanup
		deferred = true
	}

	// PHASE 1: Insert complete articles (overview + article data unified) and set article numbers directly on batches
	if err := c.batchInsertOverviews(*task.Newsgroup, batches, groupDBs); err != nil {
		log.Printf("[BATCH] processNewsgroupBatch Failed to process small batch for group '%s': %v", *task.Newsgroup, err)
		time.Sleep(time.Second)
		goto retry
	}

	// Update all articles with their assigned numbers for subsequent processing
	// and compute max article number for newsgroup stats
	var maxArticleNum int64 = 0
	for _, article := range batches {
		if article == nil {
			continue
		}
		article.Mux.RLock()
		if article.ArticleNums[task.Newsgroup] > maxArticleNum {
			maxArticleNum = article.ArticleNums[task.Newsgroup]
		}
		article.Mux.RUnlock()
	}

	// PHASE 2: Process threading for all articles (reusing the same DB connection)
	//log.Printf("[BATCH] processNewsgroupBatch Starting threading phase for %d articles in group '%s'", len(batches), *task.Newsgroup)
	start := time.Now()
	err = c.batchProcessThreading(task.Newsgroup, batches, groupDBs)
	threadingDuration := time.Since(start)
	if err != nil {
		log.Printf("[BATCH] processNewsgroupBatch Failed to process threading for group '%s' after %v: %v", *task.Newsgroup, threadingDuration, err)
		return
	}
	//log.Printf("[BATCH] processNewsgroupBatch Completed threading phase for group '%s' in %v", *task.Newsgroup, threadingDuration)

	// PHASE 3: Handle history and processor cache updates
	//log.Printf("[BATCH] processNewsgroupBatch Starting history/cache updates for %d articles in group '%s'", len(batches), *task.Newsgroup)
	start = time.Now()
	batchCount := len(batches)
	for i, article := range batches {
		//log.Printf("[BATCH] processNewsgroupBatch Updating history/cache for article %d/%d in group '%s'", i+1, len(batches), *task.Newsgroup)
		// Read article number under read lock to avoid concurrent map access
		article.Mux.RLock()
		artNum := article.ArticleNums[task.Newsgroup]
		article.Mux.RUnlock()
		c.proc.AddProcessedArticleToHistory(article.MsgIdItem, task.Newsgroup, artNum)
		article.Mux.Lock()
		if len(article.NewsgroupsPtr) > 0 {
			index := -1
			for j, ptr := range article.NewsgroupsPtr {
				if ptr != task.Newsgroup {
					continue
				}
				index = j
			}
			if index > -1 {
				article.NewsgroupsPtr = slices.Delete(article.NewsgroupsPtr, index, index+1)
			}
			if len(article.NewsgroupsPtr) > 0 {
				article.Mux.Unlock()
				// Still referenced by other groups; skip clearing and continue
				continue
			}
		}
		article.Mux.Unlock()
		// The MsgIdItem is now in history system, clear the Article's reference to it
		article.MessageID = ""
		article.Subject = ""
		article.FromHeader = ""
		article.DateSent = time.Time{}
		article.DateString = ""
		article.References = ""
		article.HeadersJSON = ""
		article.BodyText = ""
		article.Path = ""
		article.MsgIdItem = nil
		article = nil
		batches[i] = nil
	}
	//historyDuration := time.Since(start)
	//log.Printf("[BATCH] processNewsgroupBatch Completed history/cache updates for group '%s' in %v", *task.Newsgroup, historyDuration)
	batches = nil
	// Update newsgroup statistics with retryable transaction to avoid race conditions
	increment := batchCount
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
				*task.Newsgroup, increment, maxArticleNum, time.Now().UTC().Format("2006-01-02 15:04:05"))
			return txErr
		})

		if err == nil {
			//log.Printf("[BATCH] processNewsgroupBatch Updated newsgroup '%s' stats: +%d articles, max_article=%d", *task.Newsgroup, increment, maxArticleNum)

			// Update hierarchy cache with new stats instead of invalidating
			if c.db.HierarchyCache != nil {
				c.db.HierarchyCache.UpdateNewsgroupStats(*task.Newsgroup, increment, maxArticleNum)
			}
		}
	}
	if err != nil {
		log.Printf("[BATCH] processNewsgroupBatch Failed to update newsgroup stats for '%s': %v", *task.Newsgroup, err)
	}
	log.Printf("[BATCH] processNewsgroupBatch processed %d articles, newsgroup '%s' took %v", increment, *task.Newsgroup, time.Since(start))
}

// batchInsertOverviews - now sets ArticleNum directly on each batch's Article and reuses the GroupDBs connection
func (c *SQ3batch) batchInsertOverviews(newsgroup string, batches []*models.Article, groupDBs *GroupDBs) error {
	if len(batches) == 0 {
		return fmt.Errorf("no batches to process for group '%s'", newsgroup)
	}

	if len(batches) <= MaxBatchSize {
		// Small batch - process directly
		if err := c.processOverviewBatch(groupDBs, batches); err != nil {
			log.Printf("[OVB-BATCH] Failed to process small batch for group '%s': %v", newsgroup, err)
			return fmt.Errorf("failed to process small batch for group '%s': %w", newsgroup, err)
		}
		return nil
	}

	// Large batch - split into chunks
	for i := 0; i < len(batches); i += MaxBatchSize {
		end := i + MaxBatchSize
		if end > len(batches) {
			end = len(batches)
		}

		chunk := batches[i:end]
		if err := c.processOverviewBatch(groupDBs, chunk); err != nil {
			log.Printf("[OVB-BATCH] Failed to process chunk %d-%d for group '%s': %v", i, end, newsgroup, err)
			return fmt.Errorf("failed to process chunk %d-%d for group '%s': %w", i, end, newsgroup, err)
		}
	}
	return nil
}

// processSingleUnifiedArticleBatch handles a single batch that's within SQLite limits
func (c *SQ3batch) processOverviewBatch(groupDBs *GroupDBs, batches []*models.Article) error {
	// Get timestamp once for the entire batch instead of per article
	importedAt := time.Now()

	// Use a transaction for the batch insert
	tx, err := groupDBs.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Prepare the INSERT statement once
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO articles (message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, path, headers_json, body_text, downloaded, imported_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	// Execute the prepared statement for each batch item
	for _, article := range batches {
		// Format DateSent as UTC string to avoid timezone encoding issues
		dateSentStr := article.DateSent.UTC().Format("2006-01-02 15:04:05")

		_, err := stmt.Exec(
			article.MessageID,   // message_id
			article.Subject,     // subject
			article.FromHeader,  // from_header
			dateSentStr,         // date_sent (formatted as UTC string)
			article.DateString,  // date_string
			article.References,  // references
			article.Bytes,       // bytes
			article.Lines,       // lines
			article.ReplyCount,  // reply_count
			article.Path,        // path
			article.HeadersJSON, // headers_json
			article.BodyText,    // body_text
			1,                   // downloaded (set to 1 since we have the article)
			importedAt,          // imported_at - shared timestamp for batch
		)
		if err != nil {
			return fmt.Errorf("failed to execute insert for message_id %s: %w", article.MessageID, err)
		}
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Single batch SELECT query to get all article numbers at once
	batchSize := len(batches)
	bufPtr := argsPool.Get().(*[]any)
	buf := *bufPtr
	if cap(buf) < batchSize {
		buf = make([]any, 0, MaxBatchSize)
	}
	args := buf[:batchSize]
	for i, article := range batches {
		args[i] = article.MessageID
	}
	defer func() {
		// Reset and return to pool
		buf = buf[:0]
		*bufPtr = buf
		argsPool.Put(bufPtr)
	}()

	selectSQL := `SELECT message_id, article_num FROM articles WHERE message_id IN (` +
		getPlaceholders(batchSize) + `)` // ORDER BY not needed; we map by message_id

	rows, err := retryableQuery(groupDBs.DB, selectSQL, args...)
	if err != nil {
		log.Printf("[OVB-BATCH] group '%s': Failed to execute batch select: %v", groupDBs.Newsgroup, err)
		return fmt.Errorf("failed to execute batch select for group '%s': %w", groupDBs.Newsgroup, err)
	}
	defer rows.Close()

	// Create/reuse map of messageID -> article_num with pre-allocated capacity
	mPtr := idToArticleNumPool.Get().(*map[string]int64)
	idToArticleNum := *mPtr
	// Ensure it's empty before use
	for k := range idToArticleNum {
		delete(idToArticleNum, k)
	}
	defer func() {
		// Clear and return to pool
		for k := range idToArticleNum {
			delete(idToArticleNum, k)
		}
		idToArticleNumPool.Put(mPtr)
	}()

	for rows.Next() {
		var messageID string
		var articleNum int64
		if err := rows.Scan(&messageID, &articleNum); err != nil {
			log.Printf("[OVB-BATCH] group '%s': Failed to scan article number: %v", groupDBs.Newsgroup, err)
			continue
		}
		idToArticleNum[messageID] = articleNum
	}
	newsgroupPtr := c.GetNewsgroupPointer(groupDBs.Newsgroup)
	// Assign article numbers directly to batch Articles in the same order as input
	for _, article := range batches {
		article.Mux.Lock()
		if articleNum, exists := idToArticleNum[article.MessageID]; exists {
			article.ArticleNums[newsgroupPtr] = articleNum
		} else {
			log.Printf("[OVB-BATCH] group '%s': Article with message_id %s not found in batch select, marking as failed", groupDBs.Newsgroup, article.MessageID)
			article.ArticleNums[newsgroupPtr] = 0 // Mark as failed/missing
		}
		article.Mux.Unlock()
	}

	return nil
}

// batchProcessThreading processes all threading operations using existing GroupDBs connection
func (c *SQ3batch) batchProcessThreading(groupName *string, batches []*models.Article, groupDBs *GroupDBs) error {
	if len(batches) == 0 {
		return nil
	}
	//log.Printf("[THR-BATCH] group '%s': %d articles to process", *groupName, len(batches))
	roots, replies := 0, 0
	newsgroupPtr := c.GetNewsgroupPointer(groupDBs.Newsgroup)
	for _, article := range batches {
		if article == nil {
			continue
		}
		article.Mux.RLock()
		if article.ArticleNums[newsgroupPtr] <= 0 {
			log.Printf("[THR-BATCH] ERROR batchProcessThreading NewsgroupsPtr in %s, skipping msgId='%s'", *newsgroupPtr, article.MessageID)
			article.Mux.RUnlock()
			continue
		}
		if article.IsThrRoot && !article.IsReply {
			roots++
		} else if article.IsReply && !article.IsThrRoot {
			replies++
		}
		article.Mux.RUnlock()
	}

	// Process thread roots first (they need to exist before replies can reference them)
	if roots > 0 {
		if err := c.batchProcessThreadRoots(groupDBs, batches, roots); err != nil {
			log.Printf("[THR-BATCH] group '%s': Failed to batch process thread roots: %v", *groupName, err)
			// Continue processing - don't fail the whole batch
		}
	}

	// Process replies
	if replies > 0 {
		if err := c.batchProcessReplies(groupDBs, batches, replies); err != nil {
			log.Printf("[THR-BATCH] group '%s': Failed to batch process replies: %v", *groupName, err)
			// Continue processing - don't fail the whole batch
		}
	}

	return nil
}

const rootPlaceholder = "(?, NULL, ?, 0, 0)"

// batchProcessThreadRoots processes thread root articles in TRUE batch
func (c *SQ3batch) batchProcessThreadRoots(groupDBs *GroupDBs, rootBatches []*models.Article, num int) error {
	if len(rootBatches) == 0 {
		return nil
	}

	// Pre-allocate slices to avoid repeated memory allocations
	valuesClauses := make([]string, 0, num)
	args := make([]interface{}, 0, num*2) // 2 args per root

	// Reuse the same placeholder string
	newsgroupPtr := c.GetNewsgroupPointer(groupDBs.Newsgroup)
	for _, article := range rootBatches {
		if article == nil {
			continue
		}
		article.Mux.RLock()
		if article.ArticleNums[newsgroupPtr] <= 0 || !article.IsThrRoot {
			article.Mux.RUnlock()
			continue
		}
		valuesClauses = append(valuesClauses, rootPlaceholder)
		args = append(args, article.ArticleNums[newsgroupPtr], article.ArticleNums[newsgroupPtr]) // root_article, child_article
		article.Mux.RUnlock()
	}

	// Build the complete batch INSERT statement
	sql := `INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES ` +
		strings.Join(valuesClauses, ", ")

	// Execute the SINGLE batch INSERT with mutex protection and retry logic
	_, err := retryableExec(groupDBs.DB, sql, args...)
	if err != nil {
		return fmt.Errorf("failed to execute batch thread insert (%d records): %w", len(rootBatches), err)
	}

	// Post-processing: Initialize thread cache and update processor cache
	for _, article := range rootBatches {
		if article == nil {
			continue
		}
		article.Mux.RLock()
		if article.ArticleNums[newsgroupPtr] <= 0 || !article.IsThrRoot {
			article.Mux.RUnlock()
			continue
		}
		// Initialize thread cache
		if err := c.db.InitializeThreadCache(groupDBs, article.ArticleNums[newsgroupPtr], article); err != nil {
			log.Printf("[P-BATCH] group '%s': Failed to initialize thread cache for root %d: %v", groupDBs.Newsgroup, article.ArticleNums[newsgroupPtr], err)
			// Don't fail the whole operation for cache errors
		}
		article.Mux.RUnlock()
	}

	return nil
}

// batchProcessReplies processes reply articles in TRUE batch
func (c *SQ3batch) batchProcessReplies(groupDBs *GroupDBs, replyBatches []*models.Article, num int) error {
	if len(replyBatches) == 0 {
		return nil
	}

	// Pre-allocate collections to avoid repeated memory allocations
	parentMessageIDs := make(map[string]int, num) // Pre-allocate with expected capacity
	replyData := make([]struct {
		articleNum int64
		threadRoot int64
		parentID   string
		childDate  time.Time
	}, 0, num) // Pre-allocate slice with capacity

	newsgroupPtr := c.GetNewsgroupPointer(groupDBs.Newsgroup)
	// Process each reply to gather data
	for _, article := range replyBatches {
		if article == nil {
			continue
		}

		article.Mux.RLock()
		if article.ArticleNums[newsgroupPtr] <= 0 || !article.IsReply {
			article.Mux.RUnlock()
			continue
		}
		parentMessageID := article.RefSlice[len(article.RefSlice)-1] // Use the last reference as the parent
		parentMessageIDs[parentMessageID]++

		// Find thread root and collect data
		var threadRoot int64
		if root, err := c.findThreadRoot(groupDBs, article.RefSlice); err == nil {
			threadRoot = root
		}

		replyData = append(replyData, struct {
			articleNum int64
			threadRoot int64
			parentID   string
			childDate  time.Time
		}{article.ArticleNums[newsgroupPtr], threadRoot, parentMessageID, article.DateSent})

		article.Mux.RUnlock()
	}

	// Batch update reply counts for articles table (single call since overview is unified)
	if len(parentMessageIDs) > 0 {
		if err := c.batchUpdateReplyCounts(groupDBs, parentMessageIDs, "articles"); err != nil {
			log.Printf("[P-BATCH] group '%s': Failed to batch update article reply counts: %v", groupDBs.Newsgroup, err)
		}
	}

	// Collect all thread cache updates for TRUE batch processing
	threadUpdates := make(map[int64][]threadCacheUpdateData) // [threadRoot] -> list of updates

	for _, data := range replyData {
		if data.threadRoot > 0 {
			threadUpdates[data.threadRoot] = append(threadUpdates[data.threadRoot], threadCacheUpdateData{
				childArticleNum: data.articleNum,
				childDate:       data.childDate,
			})
		}
	}

	// Execute ALL thread cache updates in a single transaction
	if len(threadUpdates) > 0 {
		if err := c.batchUpdateThreadCache(groupDBs, threadUpdates); err != nil {
			log.Printf("[P-BATCH] group '%s': Failed to batch update thread cache: %v", groupDBs.Newsgroup, err)
		}
	}

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
func (c *SQ3batch) findThreadRoot(groupDBs *GroupDBs, refs []string) (int64, error) {
	if len(refs) == 0 || c.proc == nil {
		return 0, fmt.Errorf("no references or processor not available")
	}

	// Try to find thread root in processor cache first
	if cachedRoot := c.proc.FindThreadRootInCache(c.GetNewsgroupPointer(groupDBs.Newsgroup), refs); cachedRoot != nil {
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
				// Format dates as UTC strings to avoid timezone encoding issues
				firstUpdateDateUTC := firstUpdate.childDate.UTC().Format("2006-01-02 15:04:05")
				_, err = retryableStmtExec(initStmt, threadRoot, firstUpdateDateUTC, threadRoot, firstUpdateDateUTC)
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
			// Format lastActivity as UTC string to avoid timezone encoding issues
			lastActivityUTC := lastActivity.UTC().Format("2006-01-02 15:04:05")
			_, err = retryableStmtExec(updateStmt, newChildren, newCount, lastChildNum, lastActivityUTC, threadRoot)

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
		BatchInterval: BatchInterval, // Set to the global BatchInterval
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
	tasksToProcess := make([]*BatchTasks, 0, LimitBatchParallel) // Pre-allocate with max capacity
	for _, task := range o.batch.TasksMap {
		task.Mux.RLock()
		if task.BATCHprocessing || len(task.BATCHchan) < MaxBatchSize {
			task.Mux.RUnlock()
			continue
		}
		task.Mux.RUnlock()
		tasksToProcess = append(tasksToProcess, task)
		if len(tasksToProcess) >= LimitBatchParallel {
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

		if batchCount >= MaxBatchSize || totalQueued > MaxBatchSize {
			haswork = true
			task.Mux.Lock()
			if task.BATCHprocessing {
				//log.Printf("[ORCHESTRATOR] Group '%s' has %d articles but already processing", *task.Newsgroup, batchCount)
				task.Mux.Unlock()
				continue
			}
			task.BATCHprocessing = true
			task.Mux.Unlock()

			if !LockLimitChan() {
				/*
					log.Printf("[ORCHESTRATOR] Threshold exceeded for group '%s': %d articles (threshold: %d) LimitChan acquisition failed, retry later",
						*task.Newsgroup, batchCount, MaxBatchSize)
				*/
				//log.Printf("[BATCH-PROC] LimitChan acquisition failed for group '%s', resetting processing flag", *task.Newsgroup)
				task.Mux.Lock()
				task.BATCHprocessing = false
				task.Mux.Unlock()
				return true
			} else {
				log.Printf("[ORCHESTRATOR] Threshold exceeded for group '%s': %d articles (threshold: %d)",
					*task.Newsgroup, batchCount, MaxBatchSize)
				go o.batch.processNewsgroupBatch(task)
			}
		} else if batchCount > 0 {
			// Log groups with pending work but below threshold
			/*
				log.Printf("[ORCHESTRATOR-PENDING] Group '%s' has %d articles (below threshold: %d)",
					*task.Newsgroup, batchCount, MaxBatchSize)
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

var BatchDividerChan = make(chan *models.Article, 100)

func (sq *SQ3batch) BatchDivider() {
	//var TaskChans = make(map[*string]chan *models.Article)
	var task *models.Article
	var tasks *BatchTasks
	var newsgroupPtr *string
	//var BATCHchan chan *OverviewBatch
	for {
		task = <-BatchDividerChan
		if task == nil {
			log.Printf("[BATCH-DIVIDER] Received nil task?!")
			continue
		}
		select {
		case newsgroupPtr = <-task.ProcessQueue:
		default:
			log.Printf("Error in BatchDivider, received task (%#v) but no newsgroupPtr", task)
			continue
		}
		//log.Printf("[BATCH-DIVIDER] Received task for group '%s'", *task.Newsgroup)
		//if TaskChans[newsgroupPtr] == nil {
		tasks = sq.GetOrCreateTasksMapKey(*newsgroupPtr)
		tasks.Mux.Lock()
		// Lazily create the per-group channel on first enqueue
		if tasks.BATCHchan == nil {
			tasks.BATCHchan = make(chan *models.Article, InitialBatchChannelSize)
		}
		tasks.Mux.Unlock()
		tasks.BATCHchan <- task
		//TaskChans[newsgroupPtr] = tasks.BATCHchan
		//} else {
		//	TaskChans[newsgroupPtr] <- task
		//}
	}
}
