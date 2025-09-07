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
var BatchInterval = 3 * time.Second
var MaxBatchSize int = 100

// don't process more than N groups in parallel: better have some cpu & mem when importing hard!
var MaxBatchThreads = 16                   // -max-batch-threads N
var MaxQueued = 16384                      // -max-queue N
var InitialBatchChannelSize = MaxBatchSize // @AI: DO NOT CHANGE THIS!!!! per group cache channel size. should be less or equal to MaxBatch in processor aka MaxReadLinesXover in nntp-client-commands

// Cache for placeholder strings to avoid rebuilding them repeatedly
var placeholderCache sync.Map // map[int]string

const InitialShutDownCounter = 10

// getPlaceholders returns a comma-separated string of SQL placeholders (?) for the given count
func getPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}

	if count == MaxBatchSize {
		if v, ok := placeholderCache.Load(count); ok {
			return v.(string)
		}
	}

	var s string

	if count == 1 {
		s = "?"
	} else {
		s = strings.Repeat("?, ", count-1) + "?"
	}

	if count == MaxBatchSize {
		placeholderCache.Store(count, s)
	}
	return s
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
	if MaxBatchSize > 1000 {
		//log.Printf("[BATCH] MaxBatchSize is set to %d, reduced to 1000 in db_batch", MaxBatchSize)
		MaxBatchSize = 1000
	}
}

type SQ3batch struct {
	db           *Database          // Reference to the main database
	proc         ThreadingProcessor // Threading processor interface for message ID checks
	orchestrator *BatchOrchestrator // Smart orchestrator for batch processing

	GMux               sync.RWMutex           // Mutex for TasksMap to ensure thread safety
	TasksMap           map[string]*BatchTasks // Map which holds newsgroup cron taskspointers
	TmpTasksChans      chan chan *BatchTasks  // holds temporary batchTasks channels
	TmpArticleSlices   chan []*models.Article // holds temporary article slices
	TmpStringSlices    chan []string          // holds temporary string slices for valuesClauses, messageIDs etc
	TmpStringPtrSlices chan []*string         // holds temporary string slices for valuesClauses, messageIDs etc
	TmpInterfaceSlices chan []interface{}     // holds temporary interface slices for args in threading operations
	queued             int                    // number of queued articles for batch processing
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
		db:                 db,
		TasksMap:           make(map[string]*BatchTasks, 128), // Initialize TasksMap
		TmpTasksChans:      make(chan chan *BatchTasks, 128),
		TmpArticleSlices:   make(chan []*models.Article, 128),
		TmpStringSlices:    make(chan []string, 128),
		TmpStringPtrSlices: make(chan []*string, 128),
		TmpInterfaceSlices: make(chan []interface{}, 128),
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
			//log.Printf("[BATCH] Expiring cache for newsgroup '%s'", k)
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

var QueryChan = make(chan struct{}, MaxBatchThreads)
var LimitChan = make(chan struct{}, MaxBatchThreads)
var LPending = make(chan struct{}, 1)

const LockLimitBlocking = true

func LockLimitChan() bool {
	if !LockLimitBlocking {
		select {
		case LimitChan <- struct{}{}:
			// Successfully locked
			return true
		default:
			// pass
		}
	} else {
		LimitChan <- struct{}{}
		return true
	}
	return false
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

func (c *SQ3batch) returnTmpTasksChan(tasksChan chan *BatchTasks) {
	select {
	case c.TmpTasksChans <- tasksChan:
		// Successfully returned to pool
	default:
		// Pool is full, discard the channel
	}
}

func (c *SQ3batch) getOrCreateTmpTasksChan() (tasksChan chan *BatchTasks) {
	select {
	case tasksChan = <-c.TmpTasksChans:
		return tasksChan
	default:
		return make(chan *BatchTasks, 128)
	}
}

// processAllPendingBatches processes all pending batches in the correct sequential order
func (c *SQ3batch) processAllPendingBatches(wgProcessAllBatches *sync.WaitGroup, limit int) {
	if !LockPending() {
		log.Printf("[BATCH] processAllPendingBatches: LockPending failed")
		return
	}
	defer ReturnPending()

	// Get a snapshot of tasks to avoid holding the lock too long
	//log.Printf("[BATCH-DEBUG] processAllPendingBatches: acquiring GMux.RLock to get tasks snapshot")
	c.GMux.RLock()
	tasksToProcess := c.getOrCreateTmpTasksChan()
	defer c.returnTmpTasksChan(tasksToProcess)
	queued := 0
fill:
	for _, task := range c.TasksMap {
		task.Mux.RLock()
		if task.BATCHchan == nil || task.BATCHprocessing {
			task.Mux.RUnlock()
			continue
		}
		task.Mux.RUnlock()
		if len(task.BATCHchan) > 0 && len(task.BATCHchan) <= limit {
			select {
			case tasksToProcess <- task: // Send the task to the channel
			default:
				break fill
			}
		}
		queued += len(task.BATCHchan)
	}
	c.GMux.RUnlock()
	if len(tasksToProcess) == 0 {
		return
	}

	//log.Printf("[BATCH-DEBUG] processAllPendingBatches: released GMux.RLock, found %d tasks with work (%d total queued items)", len(tasksToProcess), queuedItems)

	//start := time.Now()

	log.Printf("[BATCH] processAllPendingBatches! %d newsgroups toProcess: total %d articles queued", len(tasksToProcess), queued)
	//doWork:
	//log.Printf("[BATCH-DEBUG] processAllPendingBatches: entering doWork loop, toProcess=%d", toProcess)
	//startLoop := time.Now() // Reset start time for each iteration
process:
	for {
		select {
		default:
			// No more tasks
			break process
		case task := <-tasksToProcess:
			if len(task.BATCHchan) == 0 {
				//log.Printf("[BATCH-DEBUG] processAllPendingBatches: task '%s' has empty channel, marking as done", *task.Newsgroup)
				continue process
			}

			// Check if this newsgroup is already processing
			//log.Printf("[BATCH-DEBUG] processAllPendingBatches: checking if task '%s' is already processing", *task.Newsgroup)
			task.Mux.Lock()
			if task.BATCHprocessing {
				task.Mux.Unlock()
				//log.Printf("[BATCH-DEBUG] processAllPendingBatches: task '%s' already processing, skipping", *task.Newsgroup)
				continue process
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
				continue process
			}

			//log.Printf("[BATCH-DEBUG] processAllPendingBatches: LimitChan acquired for task '%s', launching goroutine", *task.Newsgroup)
			wgProcessAllBatches.Add(1)

			go func(task *BatchTasks, wgProcessAllBatches *sync.WaitGroup) {
				defer wgProcessAllBatches.Done()
				//log.Printf("[BATCH] RUN processAllPendingBatches: processNewsgroupBatch task='%s'", *task.Newsgroup)
				gostart := time.Now()
				c.processNewsgroupBatch(task)
				log.Printf("[BATCH] END processAllPendingBatches: processNewsgroupBatch task='%s' took %v", *task.Newsgroup, time.Since(gostart))
			}(task, wgProcessAllBatches) // Pass the task and wait group
		}
	} // end for tasksToProcess
	//log.Printf("[BATCH] processAllPendingBatches: launched %d goroutines, remaining tasks %d / %d, done %d", launched, len(tasksToProcess)-toProcess, len(tasksToProcess), len(doneProcessing))
	wgProcessAllBatches.Wait() // Wait for all goroutines to finish
}

// processNewsgroupBatch processes a single newsgroup's batch in the correct sequential order:
// 1. Complete article insertion (unified overview + article data)
// 2. Threading processing (relationships)
// 3. Thread cache updates
const query_processNewsgroupBatch = `
				INSERT INTO newsgroups (name, message_count, last_article, updated_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(name) DO UPDATE SET
					message_count = message_count + excluded.message_count,
					last_article = CASE
						WHEN excluded.last_article > last_article THEN excluded.last_article
						ELSE last_article
					END,
					updated_at = excluded.updated_at`

func (c *SQ3batch) returnModelsArticleSlice(batches []*models.Article) {
	for i := range batches {
		batches[i] = nil
	}
	select {
	case c.TmpArticleSlices <- batches:
	default:
		log.Printf("[BATCH] returnModelsArticleSlice: TmpArticleSlices full, discarding slice of len %d", len(batches))
	}
}

func (c *SQ3batch) getOrCreateModelsArticleSlice() []*models.Article {
	select {
	case retchan := <-c.TmpArticleSlices:
		return retchan
	default:
	}
	return make([]*models.Article, 0, MaxBatchSize)
}

func (c *SQ3batch) getOrCreateStringSlice() []string {
	select {
	case retslice := <-c.TmpStringSlices:
		return retslice
	default:
	}
	return make([]string, 0, MaxBatchSize)
}

func (c *SQ3batch) returnStringSlice(slice []string) {
	// Clear the slice contents
	for i := range slice {
		slice[i] = ""
	}
	slice = slice[:0]
	select {
	case c.TmpStringSlices <- slice:
	default:
		// Pool is full, discard the slice
	}
}

func (c *SQ3batch) getOrCreateStringPtrSlice() []*string {
	select {
	case retslice := <-c.TmpStringPtrSlices:
		return retslice
	default:
	}
	return make([]*string, 0, MaxBatchSize)
}

func (c *SQ3batch) returnStringPtrSlice(slice []*string) {
	// Clear the slice contents
	for i := range slice {
		slice[i] = nil
	}
	slice = slice[:0]
	select {
	case c.TmpStringPtrSlices <- slice:
	default:
		// Pool is full, discard the slice
	}
}

func (c *SQ3batch) returnInterfaceSlice(slice []interface{}) {
	// Clear the slice contents
	for i := range slice {
		slice[i] = nil
	}
	slice = slice[:0]
	select {
	case c.TmpInterfaceSlices <- slice:
	default:
		// Pool is full, discard the slice
	}
}

func (c *SQ3batch) getOrCreateInterfaceSlice() []interface{} {
	select {
	case retslice := <-c.TmpInterfaceSlices:
		return retslice
	default:
	}
	return make([]interface{}, 0, MaxBatchSize*3) // up to 3x for reply count updates
}

func (sq *SQ3batch) processNewsgroupBatch(task *BatchTasks) {
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
	batches := sq.getOrCreateModelsArticleSlice()
	defer sq.returnModelsArticleSlice(batches)

	// Drain the channel
drainChannel:
	for len(batches) < MaxBatchSize {
		select {
		case article := <-task.BATCHchan:
			batches = append(batches, article)
		default:
			break drainChannel // Channel empty
		}
	}

	if len(batches) == 0 {
		return
	}

	log.Printf("[BATCH] processNewsgroupBatch: ng: '%s' with %d articles (more queued: %d)", *task.Newsgroup, len(batches), len(task.BATCHchan))

retry1:
	// Get database connection for this newsgroup
	groupDBs, err := sq.db.GetGroupDBs(*task.Newsgroup)
	if err != nil {
		log.Printf("[BATCH] processNewsgroupBatch Failed to get database for group '%s': %v", *task.Newsgroup, err)
		return
	}

	// PHASE 1: Insert complete articles (overview + article data unified) and set article numbers directly on batches
	if err := sq.batchInsertOverviews(*task.Newsgroup, batches, groupDBs); err != nil {
		time.Sleep(time.Second)
		if groupDBs != nil {
			groupDBs.Return(sq.db)
			log.Printf("[BATCH] processNewsgroupBatch Failed1 to process batch for group '%s': %v groupDBs='%#v'", *task.Newsgroup, err, groupDBs)
			groupDBs = nil
		}
		goto retry1
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
	//start := time.Now()
retry2:
	if groupDBs == nil {
		groupDBs, err = sq.db.GetGroupDBs(*task.Newsgroup)
		if err != nil {
			log.Printf("[BATCH] processNewsgroupBatch Failed2 to get database for group '%s': %v", *task.Newsgroup, err)
			return
		}
	}
	if err := sq.batchProcessThreading(task.Newsgroup, batches, groupDBs); err != nil {
		time.Sleep(time.Second)
		if groupDBs != nil {
			groupDBs.Return(sq.db)
			log.Printf("[BATCH] processNewsgroupBatch Failed2 to process threading for group '%s': %v groupDBs='%#v'", *task.Newsgroup, err, groupDBs)
			groupDBs = nil
		}
		goto retry2
	}
	defer groupDBs.Return(sq.db)
	//threadingDuration := time.Since(start)
	//log.Printf("[BATCH] processNewsgroupBatch Completed threading phase for group '%s' in %v", *task.Newsgroup, threadingDuration)

	// PHASE 3: Handle history and processor cache updates
	//log.Printf("[BATCH] processNewsgroupBatch Starting history/cache updates for %d articles in group '%s'", len(batches), *task.Newsgroup)
	//start = time.Now()

	for _, article := range batches {
		//log.Printf("[BATCH] processNewsgroupBatch Updating history/cache for article %d/%d in group '%s'", i+1, len(batches), *task.Newsgroup)
		// Read article number under read lock to avoid concurrent map access
		article.Mux.RLock()
		sq.proc.AddProcessedArticleToHistory(article.MsgIdItem, task.Newsgroup, article.ArticleNums[task.Newsgroup])
		article.Mux.RUnlock()
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
		article.ArticleNums = nil
		article.NewsgroupsPtr = nil
		article.NNTPhead = nil
		article.NNTPbody = nil
		article.MsgIdItem = nil
		article.ProcessQueue = nil
		article.RefSlice = nil
		article.Mux.Unlock()
		article = nil
	}
	//historyDuration := time.Since(start)
	//log.Printf("[BATCH] processNewsgroupBatch Completed history/cache updates for group '%s' in %v", *task.Newsgroup, historyDuration)
	// Update newsgroup statistics with retryable transaction to avoid race conditions
	// Safety check for nil database connection
	if sq.db == nil || sq.db.mainDB == nil {
		log.Printf("[BATCH] processNewsgroupBatch Main database connection is nil, cannot update newsgroup stats for '%s'", *task.Newsgroup)
		err = fmt.Errorf("processNewsgroupBatch main database connection is nil")
	} else {
		//LockQueryChan()
		//defer ReturnQueryChan()
		// Use retryable transaction to prevent race conditions between concurrent batches
		err = retryableTransactionExec(sq.db.mainDB, func(tx *sql.Tx) error {
			// Use UPSERT to handle both new and existing newsgroups
			_, txErr := tx.Exec(query_processNewsgroupBatch,
				*task.Newsgroup, len(batches), maxArticleNum, time.Now().UTC().Format("2006-01-02 15:04:05"))
			return txErr
		})

		if err != nil {
			log.Printf("[BATCH] processNewsgroupBatch Failed to update newsgroup stats for '%s': %v", *task.Newsgroup, err)
			return
		}
		//log.Printf("[BATCH] processNewsgroupBatch Updated newsgroup '%s' stats: +%d articles, max_article=%d", *task.Newsgroup, increment, maxArticleNum)

		// Update hierarchy cache with new stats instead of invalidating
		if sq.db.HierarchyCache != nil {
			sq.db.HierarchyCache.UpdateNewsgroupStats(*task.Newsgroup, len(batches), maxArticleNum)
		}
	}
	if err != nil {
		log.Printf("[BATCH] processNewsgroupBatch Failed to update newsgroup stats for '%s': %v", *task.Newsgroup, err)
	}
	log.Printf("[BATCH-END] newsgroup '%s' processed articles: %d (took %v)", *task.Newsgroup, len(batches), time.Since(startTime))
	sq.GMux.Lock()
	sq.queued -= len(batches)
	sq.GMux.Unlock()
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

		if err := c.processOverviewBatch(groupDBs, batches[i:end]); err != nil {
			log.Printf("[OVB-BATCH] Failed to process chunk %d-%d for group '%s': %v", i, end, newsgroup, err)
			return fmt.Errorf("failed to process chunk %d-%d for group '%s': %w", i, end, newsgroup, err)
		}
	}
	return nil
}

const query_processOverviewBatch = `INSERT OR IGNORE INTO articles (message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, path, headers_json, body_text, downloaded, imported_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
const query_processOverviewBatch2 = `SELECT message_id, article_num FROM articles WHERE message_id IN (`

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
	stmt, err := tx.Prepare(query_processOverviewBatch)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	// Execute the prepared statement for each batch item
	for _, article := range batches {
		// Format DateSent as UTC string to avoid timezone encoding issues

		_, err := stmt.Exec(
			article.MessageID,  // message_id
			article.Subject,    // subject
			article.FromHeader, // from_header
			article.DateSent.UTC().Format("2006-01-02 15:04:05"), // date_sent (formatted as UTC string)
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
	args := make([]any, 0, len(batches))
	for _, article := range batches {
		args = append(args, article.MessageID)
	}
	// ORDER BY not needed; we map by message_id
	query := query_processOverviewBatch2 + getPlaceholders(len(args)) + `)`
	log.Printf("[OVB-BATCH] group '%s': Selecting article numbers for %d articles queryLen=%d", groupDBs.Newsgroup, len(batches), len(query))
	rows, err := retryableQuery(groupDBs.DB, query, args...)
	if err != nil {
		log.Printf("[OVB-BATCH] group '%s': Failed to execute batch select: %v", groupDBs.Newsgroup, err)
		return fmt.Errorf("failed to execute batch select for group '%s': %w", groupDBs.Newsgroup, err)
	}
	defer rows.Close()
	newsgroupPtr := c.GetNewsgroupPointer(groupDBs.Newsgroup)

	var messageID string
	var articleNum, idToArticleNum, timeSpent, spentms, loops int64
	start := time.Now()
	// Iterate through the results and map article numbers back to batches
	for rows.Next() {
		if err := rows.Scan(&messageID, &articleNum); err != nil {
			log.Printf("[OVB-BATCH] group '%s': Failed to scan article number: %v", groupDBs.Newsgroup, err)
			continue
		}
		// O(n²) complexity: nested loop through batches for each DB row
		// Acceptable for small batch sizes (≤100), eliminates map allocation overhead
		startN := time.Now()
	forBatches:
		for _, article := range batches {
			loops++
			// Assign article numbers directly to batch Articles
			article.Mux.Lock()
			if article.MessageID == messageID {
				if article.ArticleNums[newsgroupPtr] == 0 {
					article.ArticleNums[newsgroupPtr] = articleNum
				} else {
					log.Printf("[OVB-BATCH] group '%s': Article with message_id %s already assigned article number %d, did not reassign from db: %d", groupDBs.Newsgroup, messageID, article.ArticleNums[newsgroupPtr], articleNum)
				}
				article.Mux.Unlock()
				timeSpent += time.Since(startN).Microseconds()
				break forBatches
			}
			article.Mux.Unlock()
		}
		idToArticleNum++
	}
	took := time.Since(start).Milliseconds()
	if timeSpent > 1000 {
		spentms = timeSpent / 1000
	}
	log.Printf("[OVB-BATCH] group '%s': assigned %d/%d articles (took %d ms, spent %d microsec (%d ms) loops: %d)", groupDBs.Newsgroup, idToArticleNum, len(batches), took, timeSpent, spentms, loops)
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
		if err := c.batchProcessThreadRoots(groupDBs, batches); err != nil {
			log.Printf("[THR-BATCH] group '%s': Failed to batch process thread roots: %v", *groupName, err)
			// Continue processing - don't fail the whole batch
		}
	}

	// Process replies
	if replies > 0 {
		if err := c.batchProcessReplies(groupDBs, batches); err != nil {
			log.Printf("[THR-BATCH] group '%s': Failed to batch process replies: %v", *groupName, err)
			// Continue processing - don't fail the whole batch
		}
	}

	return nil
}

const query_batchProcessThreadRoots = "INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, ?, ?, 0, 0)"

// batchProcessThreadRoots processes thread root articles in TRUE batch
func (c *SQ3batch) batchProcessThreadRoots(groupDBs *GroupDBs, rootBatches []*models.Article) error {
	if len(rootBatches) == 0 {
		return nil
	}

	// Use a transaction with prepared statement for cleaner, more efficient execution
	tx, err := groupDBs.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Prepare the INSERT statement once
	stmt, err := tx.Prepare(query_batchProcessThreadRoots)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	// Single loop: execute statement for each valid root article
	newsgroupPtr := c.GetNewsgroupPointer(groupDBs.Newsgroup)
	processedCount := 0
	var threadCacheEntries []struct {
		articleNum int64
		article    *models.Article
	}

	for _, article := range rootBatches {
		if article == nil {
			continue
		}

		article.Mux.RLock()
		if article.ArticleNums[newsgroupPtr] <= 0 || !article.IsThrRoot {
			article.Mux.RUnlock()
			continue
		}

		articleNum := article.ArticleNums[newsgroupPtr]

		// Execute the prepared statement directly - for thread roots, parent_article is NULL
		_, err := stmt.Exec(articleNum, nil, articleNum) // root_article, parent_article, child_article
		if err != nil {
			article.Mux.RUnlock()
			return fmt.Errorf("failed to execute thread insert for article %d: %w", articleNum, err)
		}

		// Collect data for post-processing OUTSIDE the transaction
		threadCacheEntries = append(threadCacheEntries, struct {
			articleNum int64
			article    *models.Article
		}{articleNum, article})

		processedCount++
		article.Mux.RUnlock()
	}

	if processedCount == 0 {
		return nil // No articles were processed
	}

	// Commit the transaction BEFORE doing thread cache operations
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction in batchProcessThreadRoots: %w", err)
	}

	// Do post-processing AFTER transaction is committed to avoid SQLite lock conflicts
	for _, entry := range threadCacheEntries {
		entry.article.Mux.RLock()
		if err := c.db.InitializeThreadCache(groupDBs, entry.articleNum, entry.article); err != nil {
			log.Printf("[P-BATCH] group '%s': Failed to initialize thread cache for root %d: %v", groupDBs.Newsgroup, entry.articleNum, err)
			// Don't fail the whole operation for cache errors
		}
		entry.article.Mux.RUnlock()
	}

	return nil
}

// batchProcessReplies processes reply articles in TRUE batch
func (c *SQ3batch) batchProcessReplies(groupDBs *GroupDBs, replyBatches []*models.Article) error {
	if len(replyBatches) == 0 {
		return nil
	}
	parentMessageIDs := make(map[*string]int, MaxBatchSize) // Pre-allocate map with expected size
	defer func() {
		for k := range parentMessageIDs {
			delete(parentMessageIDs, k)
		}
	}()
	// Create replyData slice (keep as direct allocation since it contains complex struct)
	replyData := make([]struct {
		articleNum int64
		threadRoot int64
		childDate  time.Time
	}, 0, len(replyBatches)) // Pre-allocate slice with capacity

	newsgroupPtr := c.GetNewsgroupPointer(groupDBs.Newsgroup)
	// Process each reply to gather data
	preAllocThreadRoots := 0
	for _, article := range replyBatches {
		if article == nil {
			continue
		}

		article.Mux.RLock()
		if article.ArticleNums[newsgroupPtr] <= 0 || !article.IsReply {
			article.Mux.RUnlock()
			continue
		}
		//parentMessageID := article.RefSlice[len(article.RefSlice)-1] // Use the last reference as the parent
		parentMessageIDs[&article.RefSlice[len(article.RefSlice)-1]]++

		// Find thread root and collect data
		var threadRoot int64
		if root, err := c.findThreadRoot(groupDBs, article.RefSlice); err == nil {
			threadRoot = root
		}

		replyData = append(replyData, struct {
			articleNum int64
			threadRoot int64
			childDate  time.Time
		}{article.ArticleNums[newsgroupPtr], threadRoot, article.DateSent})
		if threadRoot > 0 {
			preAllocThreadRoots++
		}
		article.Mux.RUnlock()
	}

	// Batch update reply counts for articles table (single call since overview is unified)
	if len(parentMessageIDs) > 0 {
		if err := c.batchUpdateReplyCounts(groupDBs, parentMessageIDs); err != nil {
			log.Printf("[P-BATCH] group '%s': Failed to batch update article reply counts: %v", groupDBs.Newsgroup, err)
		}
	}

	if preAllocThreadRoots > 0 {
		threadUpdates := make(map[int64][]threadCacheUpdateData, preAllocThreadRoots)
		//log.Printf("[P-BATCH] group '%s': Pre-allocated thread updates map with capacity %d", groupDBs.Newsgroup, preAllocThreadRoots)
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
			log.Printf("[P-BATCH] group '%s': Updated thread cache for %d thread roots", groupDBs.Newsgroup, len(threadUpdates))
		}
	}

	return nil
}

var query_batchUpdateReplyCounts1 string = "WHEN message_id = ? THEN reply_count + ? "
var query_batchUpdateReplyCounts2 string = "UPDATE articles SET reply_count = CASE %s END WHERE message_id IN (%s)"

// batchUpdateReplyCounts performs batch update of reply counts using CASE WHEN
func (c *SQ3batch) batchUpdateReplyCounts(groupDBs *GroupDBs, parentCounts map[*string]int) error {
	if len(parentCounts) == 0 {
		return nil
	}

	// Get pooled slices to avoid repeated memory allocations
	messageIDs := c.getOrCreateStringPtrSlice()
	args := c.getOrCreateInterfaceSlice()

	defer func() {
		// Reset and return all to pools
		c.returnStringPtrSlice(messageIDs)
		c.returnInterfaceSlice(args)
	}()

	// Build args efficiently - no string copying needed
	for messageID, count := range parentCounts {
		messageIDs = append(messageIDs, messageID)
		args = append(args, messageID, count) // CASE WHEN args
	}

	// Add message IDs for WHERE IN clause
	for _, messageID := range messageIDs {
		args = append(args, messageID) // WHERE IN args
	}

	// Build the complete batch UPDATE statement with a single sprintf
	// Use strings.Repeat for efficient SQL building - zero string copies
	// Execute the batch UPDATE
	//log.Printf("[P-BATCH] group '%s': update batch reply count for %d articles (queryLen=%d)", groupDBs.Newsgroup, len(messageIDs), len(sql))
	_, err := retryableExec(groupDBs.DB, fmt.Sprintf(query_batchUpdateReplyCounts2, strings.Repeat(query_batchUpdateReplyCounts1, len(messageIDs)), getPlaceholders(len(messageIDs))), args...)
	if err != nil {
		log.Printf("[P-BATCH] group '%s': Failed to execute batch reply count update: %v", groupDBs.Newsgroup, err)
	}
	return err
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
func (sq *SQ3batch) batchUpdateThreadCache(groupDBs *GroupDBs, threadUpdates map[int64][]threadCacheUpdateData) error {
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
				_, err = retryableStmtExec(initStmt, threadRoot, firstUpdateDateUTC, firstUpdate.childArticleNum, firstUpdateDateUTC)
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
			if sq.db.MemThreadCache != nil {
				sq.db.MemThreadCache.UpdateThreadMetadata(groupDBs.Newsgroup, threadRoot, newCount, lastActivity, newChildren)
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
			log.Printf("[ORCHESTRATOR1] Database shutdown detected ShutDownCounter=%d", ShutDownCounter)
			o.batch.processAllPendingBatches(&wgProcessAllBatches, MaxBatchSize)
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
				//log.Printf("[ORCHESTRATOR1] Timer triggered - processing all pending batches smaller than MaxBatchSize")
				o.batch.processAllPendingBatches(&wgProcessAllBatches, MaxBatchSize-1)
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
	sleep := 1000 * 1000 // @AI: DO NOT CHANGE THIS!!!!
	ShutDownCounter := InitialShutDownCounter
	wantShutdown := false
	newSleep := sleep
	for {
		// Sleep briefly to avoid busy waiting
		time.Sleep(time.Duration(sleep/2) * time.Microsecond)
		// Check all channels for threshold breaches
		hasWork := o.checkThresholds()
		//log.Printf("[ORCHESTRATOR2] Current sleep interval: (%d ms) hasWork=%t", sleep, hasWork)
		if o.batch.db.IsDBshutdown() {
			if o.batch.proc == nil {
				log.Printf("[ORCHESTRATOR2] o.batch.proc not set. shutting down.")
				return
			}
			log.Printf("[ORCHESTRATOR2] Database shutdown detected ShutDownCounter=%d", ShutDownCounter)
			sleep = 500 * 1000
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
				newSleep = int(float64(sleep) * 1.005) // +0.5%
				if newSleep <= sleep {
					newSleep = sleep + 100 // Force at least 100 micros increment: example if we hit 5ms and we have no more work 5ms * 1.05 results still in 5 and we will turn like crazy in cycles!
				}
				sleep = newSleep
				if sleep > 1000*1000 { // @AI: DO NOT CHANGE THIS!!!!
					sleep = 1000 * 1000 // @AI: DO NOT CHANGE THIS!!!!
				} // @AI: DO NOT CHANGE THIS!!!!
			} else {
				// Fast recovery when work is found
				sleep = sleep / 4    // @AI: DO NOT CHANGE THIS!!!!
				if sleep < 16*1000 { // @AI: DO NOT CHANGE THIS!!!!
					sleep = 16 * 1000 // @AI: DO NOT CHANGE THIS!!!!
				}
			}
		}
	}
}

// checkThresholds monitors all channels and sends notifications when thresholds are exceeded
func (o *BatchOrchestrator) checkThresholds() (haswork bool) {
	tasksToProcess := make(chan *BatchTasks, 128) // Use buffered channel to avoid blocking
	o.batch.GMux.RLock()
fillQ:
	for _, task := range o.batch.TasksMap {
		task.Mux.RLock()
		if task.BATCHprocessing || len(task.BATCHchan) < MaxBatchSize {
			task.Mux.RUnlock()
			continue
		}
		task.Mux.RUnlock()
		select {
		case tasksToProcess <- task:
		default:
			// chan full
			break fillQ
		}
	}
	o.batch.GMux.RUnlock()
	close(tasksToProcess)
	if len(tasksToProcess) == 0 {
		return false // No tasks to process
	}
	//log.Printf("[ORCHESTRATOR] Checking %d groups for threshold breaches", len(tasksToProcess))

	totalQueued := 0
	batchCount := 0
	for task := range tasksToProcess {
		batchCount = len(task.BATCHchan)
		if batchCount == 0 {
			continue // skip empty channels
		}
		totalQueued += batchCount

		if batchCount >= MaxBatchSize {
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
				//log.Printf("[BATCH-BIG] Threshold exceeded for group '%s': %d articles (threshold: %d)", *task.Newsgroup, batchCount, MaxBatchSize)
				go o.batch.processNewsgroupBatch(task)
				totalQueued -= batchCount
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

	return haswork
}

var BatchDividerChan = make(chan *models.Article, 1)

// BatchDivider reads incoming articles and routes them to the appropriate per-newsgroup channel
// It also enforces the global MaxQueued limit to prevent overload
// Each newsgroup channel is created lazily on first use
// This runs as a single goroutine to avoid locking issues

func (sq *SQ3batch) BatchDivider() {
	var tmpQueued, realQueue int
	var maxQueue int = MaxQueued / 100 * 80
	var target int = MaxQueued / 100 * 20
	for {
		var newsgroupPtr *string
		task := <-BatchDividerChan
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
		tasks := sq.GetOrCreateTasksMapKey(*newsgroupPtr)
		tasks.Mux.Lock()
		// Lazily create the per-group channel on first enqueue
		if tasks.BATCHchan == nil {
			tasks.BATCHchan = make(chan *models.Article, InitialBatchChannelSize)
		}
		tasks.Mux.Unlock()
		if realQueue >= maxQueue {
			log.Printf("[BATCH-DIVIDER] MaxQueued reached (%d), waiting to enqueue more (current Queue=%d, tmpQueued=%d)", MaxQueued, realQueue, tmpQueued)

			for {
				time.Sleep(100 * time.Millisecond)
				sq.GMux.RLock()
				if sq.queued <= target {
					realQueue = sq.queued
					sq.GMux.RUnlock()
					break
				}
				sq.GMux.RUnlock()
			}
		}
		tasks.BATCHchan <- task
		tmpQueued++
		if tmpQueued >= MaxBatchSize {
			sq.GMux.Lock()
			//log.Printf("[BATCH-DIVIDER] Enqueued %d articles to group '%s' (current Queue=%d, tmpQueued=%d)", tmpQueued, *newsgroupPtr, realQueue, tmpQueued)
			sq.queued += tmpQueued
			tmpQueued = 0
			realQueue = sq.queued
			sq.GMux.Unlock()
		}
	}
}
