package history

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const ENABLE_HISTORY = true // EXPERIMENTAL !

var ErrNoMatch = fmt.Errorf("no match")

var HistoryDEBUG = false // Set to true for spammy debug logs

const (

	// History file constants
	DefaultHistoryDir = "./data/history" // TODO set via config
	HistoryFileName   = "history.dat"

	// Cache configuration
	DefaultCacheExpires = 15 // seconds
	DefaultCachePurge   = 5  // seconds

	// History Write Batching configuration
	DefaultBatchSize    = 10000 // Number of entries to batch before flushing (reduced from 10000 to fix memory bloat)
	DefaultBatchTimeout = 1000  // Milliseconds to wait before forced flush. maps to h.config.BatchTimeout

	// Sharding configuration constants
	SHARD_16_256 = 2 // 16 DBs with 256 tables each (recommended)
)

// NewHistory creates a new history manager
// The mainWG parameter should be the main application's waitgroup that will coordinate shutdown
func NewHistory(config *HistoryConfig, mainWG *sync.WaitGroup) (*History, error) {

	if config == nil {
		config = DefaultConfig()
	}

	if err := config.ValidateConfig(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Create history directory
	if !dirExists(config.HistoryDir) {
		if !mkdir(config.HistoryDir) {
			return nil, fmt.Errorf("failed to create history directory: %s", config.HistoryDir)
		}
	}
	h := &History{
		config:    config,
		stats:     &HistoryStats{},
		tickChan:  make(chan struct{}, 1),
		dbChan:    make(chan *MessageIdItem, config.BatchSize*2),
		dbQueued:  make(map[*MessageIdItem]bool, config.BatchSize*2),
		stopChan:  make(chan struct{}),
		lastFlush: time.Now(),
		mainWG:    mainWG, // Store the main application's waitgroup
	}
	if ENABLE_HISTORY {
		// Initialize database backend
		if err := h.initDatabase(); err != nil {
			return nil, fmt.Errorf("failed to initialize database: %w", err)
		}

		// Open history file
		//if err := h.openHistoryFile(); err != nil {
		//	return nil, fmt.Errorf("failed to open history file: %w", err)
		//}
	}
	go h.writerWorker() // +1 wg mainWG waitGroup
	return h, nil
}

func (h *History) AddDBQueued(msgIdItem *MessageIdItem) bool {
	h.mux.Lock()
	defer h.mux.Unlock()
	if h.dbQueued[msgIdItem] {
		return false
	}
	h.dbQueued[msgIdItem] = true
	return true
}

func (h *History) DelDBQueued(msgIdItem *MessageIdItem) {
	h.mux.Lock()
	defer h.mux.Unlock()
	delete(h.dbQueued, msgIdItem)
}

func (h *History) dbMoreQueued() bool {
	h.mux.RLock()
	defer h.mux.RUnlock()
	return len(h.dbQueued) > 0
}

func (h *History) IsDBQueued(msgIdItem *MessageIdItem) bool {
	h.mux.RLock()
	defer h.mux.RUnlock()
	return h.dbQueued[msgIdItem]
}

// Add adds a new message-ID to history
func (h *History) Add(msgIdItem *MessageIdItem) bool {
	if !ENABLE_HISTORY {
		msgIdItem.Mux.Lock()
		msgIdItem.Response = CaseDupes
		msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
		msgIdItem.Mux.Unlock()
		return false
	}
	if msgIdItem == nil {
		log.Printf("[HISTORY] ERROR: Add called with nil MessageIdItem")
		return false
	}
	if !h.AddDBQueued(msgIdItem) {
		log.Printf("[HISTORY] Add()->AddDBQueued(): msgId: '%s' already queued", msgIdItem.MessageId)
		return false
	}
	msgIdItem.Mux.Lock()
	if msgIdItem.MessageId == "" {
		log.Printf("[HISTORY] ERROR: Add called with empty MessageId item='%#v'", msgIdItem)
		msgIdItem.Mux.Unlock()
		return false
	}
	//if msgIdItem.StorageToken == "" && (msgIdItem.GroupName == nil || *msgIdItem.GroupName == "" || msgIdItem.ArtNum <= 0) {
	//	log.Printf("[HISTORY] ERROR: Add called with invalid MessageIdItem='%#v'", msgIdItem)
	//	msgIdItem.Mux.Unlock()
	//	return
	//}
	//if msgIdItem.FileOffset > 0 {
	//	log.Printf("[HISTORY] ERROR: Add called with already stored MessageIdItem='%v'", msgIdItem)
	//	msgIdItem.Mux.Unlock()
	//	return
	//}
	if msgIdItem.Response != CaseLock {
		/*
			if msgIdItem.MessageId == "<32304224.79C1@parkcity.com>" {
				log.Printf("[DEBUG-HISTORY-STEP8-FAIL] Target message ID blocked from entering history! Response: %x (expected: %x)", msgIdItem.Response, CaseLock)
			}
		*/
		msgIdItem.Mux.Unlock()
		log.Printf("[HISTORY] DUPLICATE to Add msgId='%s' case: %x != %x", msgIdItem.MessageId, msgIdItem.Response, CaseLock)
		return false
	}
	//msgIdItem.Response = CaseWrite // Set to write state // FIXMEE
	//msgIdItem.CachedEntryExpires = time.Now().Add(CachedEntryTTL)
	msgIdItem.Mux.Unlock()

	/*
		if msgIdItem.MessageId == "<32304224.79C1@parkcity.com>" {
			log.Printf("[DEBUG-HISTORY-STEP8] Target message ID entering writer channel: %s", msgIdItem.MessageId)
		}
	*/
	/*
		// Check if already exists

	*/
	if len(h.dbChan) >= h.config.BatchSize {
		select {
		case h.tickChan <- NOTIFY:
			// pass
		default:
			// full
		}
	}
	start := time.Now()
	if response, _, err := h.Lookup(msgIdItem, true); err != nil || response != CasePass {
		msgIdItem.Mux.Lock()
		if response != CaseDupes {
			log.Printf("[HISTORY] DEBUG Add()->Lookup(): msgId: '%s'response = %x != CasePass entry.msgIdItem.Response=%x", msgIdItem.MessageId, response, msgIdItem.Response)
		}
		msgIdItem.CachedEntryExpires = time.Now().Add(3 * time.Second)
		msgIdItem.Mux.Unlock()
		//h.updateStats(func(s *HistoryStats) { s.Duplicates++ })
		//if HistoryDEBUG {
		log.Printf("[HISTORY] Add()->Lookup(): Duplicate msgId: '%s' lookup took %v err='%v'", msgIdItem.MessageId, time.Since(start), err)
		//}
		return false
	}
	msgIdItem.Mux.Lock()
	msgIdItem.Response = CaseWrite // Set to write state // FIXMEE
	//msgIdItem.CachedEntryExpires = time.Now().Add(CachedEntryTTL)
	msgIdItem.Mux.Unlock()
	h.dbChan <- msgIdItem
	if HistoryDEBUG {
		log.Printf("[HISTORY] Add()->Lookup(): msgId: '%s' not found, lookup took %v (queued %d)", msgIdItem.MessageId, time.Since(start), len(h.dbChan))
	}
	return true
}

// Returns: ResponsePass (0) = not found, ResponseDuplicate (1) = found, ResponseRetry (2) = error
func (h *History) Lookup(msgIdItem *MessageIdItem, quick bool) (response int, newsgroupIDs []int64, err error) {
	if !ENABLE_HISTORY {
		return CasePass, nil, nil
	}
	found, newsgroupIDs, err := h.LookupMID(msgIdItem, quick)
	//log.Printf("[HISTORY] Lookup for msgId='%s' found='%v', err='%v'", msgIdItem.MessageId, found, err)
	if err != nil {
		log.Printf("[HISTORY] ERROR: Lookup failed for msgId='%s': %v", msgIdItem.MessageId, err)
		//h.updateStats(func(s *HistoryStats) { s.Errors++ })
		return CaseError, nil, err
	}
	//log.Printf("Lookup for msgID: '%s' found='%v', offsets='%v'", messageID, found, offsetsData)
	if found {
		//h.updateStats(func(s *HistoryStats) { s.TotalLookups++ })
		return CaseDupes, newsgroupIDs, nil
	}
	return CasePass, nil, nil
}

// Lookup checks if a message-ID exists in history
func (h *History) LookupMID(msgIdItem *MessageIdItem, quick bool) (exists bool, newsgroupIDs []int64, err error) {
	if !ENABLE_HISTORY {
		return false, nil, nil
	}
	// Route hash: 1st char -> DB, 2nd+3rd chars -> table, remaining -> stored value
	start1 := time.Now()
	dbIndex, tableName, err := h.routeHash(msgIdItem)
	if err != nil {
		return false, nil, fmt.Errorf("failed to route hash: %v", err)
	}
	if HistoryDEBUG {
		log.Printf("[HISTORY] #0 lookupInDatabase: routed hash '%s' to dbIndex=%d, tableName='%s', took %v", msgIdItem.MessageIdHash, dbIndex, tableName, time.Since(start1))
	}

	// Get database connection
	var db *sql.DB
	//start2 := time.Now()

	db, err = h.db.GetShardedDB(dbIndex, false)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get database connection: %v", err)
	}

	/*
		if HistoryDEBUG || time.Since(start2) > 1*time.Millisecond {
			log.Printf("[HISTORY] #1 lookupInDatabase: got database connection took %v", time.Since(start2))
		}
	*/

	// Query database for file offsets with optimized retry logic
	var newsgroupsData string
	start3 := time.Now()
	baseDelay := 10 * time.Millisecond

	for {
		err = db.QueryRow("SELECT newsgroups FROM "+tableName+" WHERE message_id = ?", msgIdItem.MessageId).Scan(&newsgroupsData)
		if err != nil {
			if err == sql.ErrNoRows {
				if HistoryDEBUG {
					log.Printf("[HISTORY] #2.1 lookupInDatabase: sql.ErrNoRows dbIndex=%x tableName=%s took %v", dbIndex, tableName, time.Since(start3))
				}
				return false, nil, nil // Not found
			}

			// Check if it's a retryable error (database lock/busy)
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "database is locked") || strings.Contains(errStr, "busy") {
				time.Sleep(baseDelay)
				log.Printf("[HISTORY] lookupInDatabase: retrying after error '%v'", err)
				continue
			}
		}
		break
	}
	if HistoryDEBUG {
		log.Printf("[HISTORY] #2.2 lookupInDatabase: database query took %v HistoryDEBUG=%t", time.Since(start3), HistoryDEBUG)
	}
	// Parse newsgroup IDs from comma-separated string
	newsgroupIDsSlice := strings.Split(newsgroupsData, ",")
	if quick {
		if len(newsgroupIDsSlice) > 0 {
			return true, nil, nil // Found matching entries
		}
		return false, nil, ErrNoMatch
	}
	anewsgroupIDs := make([]int64, 0, len(newsgroupIDsSlice))
	for _, ngIDsStr := range newsgroupIDsSlice {
		newsgroupID, err := strconv.ParseInt(strings.TrimSpace(ngIDsStr), 10, 64)
		if err != nil {
			log.Printf("WARN: Invalid ngIDsStr in database: %s", ngIDsStr)
			continue
		}
		anewsgroupIDs = append(anewsgroupIDs, newsgroupID) // Append to newsgroupIDs slice
	}
	if len(anewsgroupIDs) > 0 {
		msgIdItem.Mux.Lock()
		msgIdItem.NewsgroupIDs = anewsgroupIDs
		msgIdItem.Mux.Unlock()
		return true, anewsgroupIDs, nil // Found matching entries
	}
	// No matching entries found
	return false, nil, ErrNoMatch
}

// GetStats returns current statistics
func (h *History) GetStats() HistoryStats {
	h.stats.mux.RLock()
	defer h.stats.mux.RUnlock()
	return HistoryStats{
		TotalLookups: h.stats.TotalLookups,
		TotalAdds:    h.stats.TotalAdds,
		CacheHits:    h.stats.CacheHits,
		CacheMisses:  h.stats.CacheMisses,
		Duplicates:   h.stats.Duplicates,
		Errors:       h.stats.Errors,
	}
}

// updateStats safely updates statistics
func (h *History) updateStats(fn func(*HistoryStats)) {
	h.stats.mux.Lock()
	defer h.stats.mux.Unlock()
	fn(h.stats)
}

// Close gracefully shuts down the history system
func (h *History) Close() error {
	log.Printf("Closing down history system...")
	h.mux.Lock()
	defer h.mux.Unlock()
	// Signal workers to stop via stop channel
	close(h.stopChan)

	// Note: We don't wait for workers here anymore since the main application
	// will wait for them via the main waitgroup (mainWG)
	// Workers will continue processing until no more work remains
	log.Printf("History system shutdown initialized")
	return nil
}

var NOTIFY = struct{}{}

// writerWorker handles background writing of history entries with batching
func (h *History) writerWorker() {
	// Signal completion to main waitgroup
	if h.mainWG == nil {
		log.Fatalf("Main waitgroup is nil, cannot signal completion")
	}
	log.Printf("History writer worker started (batching enabled: BatchSize=%d, timeout: %d ms)", h.config.BatchSize, h.config.BatchTimeout)

	//counter := 100
	//shutdown := false
	// Initialize batch timeout timer

	go func(h *History) {
		ticker := time.NewTicker(250 * time.Millisecond)
		var chansize int
		var chanlimit bool
		lastChanlimit := time.Now()
		timeout := time.Duration(h.config.BatchTimeout) * time.Millisecond
		defer ticker.Stop()
		for {
			<-ticker.C
			h.batchMux.RLock()
			lastFlush := time.Since(h.lastFlush)
			h.batchMux.RUnlock()
			chansize = len(h.dbChan)
			chanlimit = chansize >= h.config.BatchSize
			if chanlimit {
				log.Printf("[HISTORY] ticker writerWorker: lastChanLimit: %v | lastFlush: %v", time.Since(lastChanlimit), lastFlush)
				lastChanlimit = time.Now()
			}
			if lastFlush >= timeout || chanlimit {
				select {
				case h.tickChan <- NOTIFY:
					// pass
					h.batchMux.Lock()
					h.lastFlush = time.Now()
					h.batchMux.Unlock()
				default:
					// full
				}
			}
		}
	}(h)

	go func(h *History) {
		defer log.Printf("History writer worker stopped (defer MainWG)")
		defer h.mainWG.Done() // (defer MainWG)

		shutdownCounter := 100
		chanSize := 0
		chanlimit := false
		for {
			<-h.tickChan
			// Handle shutdown
			if h.ServerShutdown() && h.CheckNoMoreWorkInHistory() {
				if !ENABLE_HISTORY {
					return
				}
				//log.Printf("[HISTORY] writerWorker Server shutdown initiated, checking for pending work...")
				time.Sleep(100 * time.Millisecond)
				shutdownCounter--

				if !h.dbWorkChecker.CheckNoMoreWorkInMaps() {
					shutdownCounter = 100
					continue
				}
				h.flushPendingBatch()
				if shutdownCounter <= 0 && len(h.dbChan) == 0 {
					log.Printf("[HISTORY] writerWorker CheckNoMoreWorkInHistory ok. shutting down...")
					return
				}
				select {
				case h.tickChan <- NOTIFY:
					// pass
				default:
					// full
				}
				continue
			}
			chanSize = len(h.dbChan)
			if chanSize == 0 {
				continue
			}
			h.flushPendingBatch()
			chanlimit = chanSize >= h.config.BatchSize
			if chanlimit {
				select {
				case h.tickChan <- NOTIFY:
					// pass
				default:
					// full
				}
			}
		}
	}(h)
} // end func writerWorker

func (h *History) ServerShutdown() bool {
	select {
	case _, ok := <-h.stopChan:
		if !ok {
			return true
		}
	default:
		// pass
	}
	return false
}

// routeHash routes a hash to the correct database and table
// Returns: dbIndex, tableName, error
func (h *History) routeHash(item *MessageIdItem) (dbIndex int, tableName string, err error) {
	/*
		minCharsNeeded := 3 + h.config.UseShortHashLen // routing chars + storage chars
		if len(hash) < minCharsNeeded {
			return 0, "", "", fmt.Errorf("hash too short: need at least %d characters, got %d", minCharsNeeded, len(hash))
		}
	*/
	// For sharded database modes:
	// 1st char -> database index (0-f maps to 0-15)
	// 2nd+3rd chars -> table name (s + 00-ff)
	hash := ComputeMessageIDHash(item.MessageId)[:3] // Compute the hash of the message ID

	// Convert first hex char to database index
	dbIndex, err = hexToInt(hash[0:1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid hex char for database: %s", hash[0:1])
	}

	// Validate database index
	numDBs, _, _ := GetShardConfig(h.config.ShardMode)
	if dbIndex >= numDBs {
		return 0, "", fmt.Errorf("database index %d exceeds available databases %d", dbIndex, numDBs)
	}

	// Table name from 2nd+3rd hex chars (s + hex)
	return dbIndex, "_" + hash[1:3], nil
}

// flushPendingBatch processes all entries in the current batch atomically
func (h *History) flushPendingBatch() {
	if len(h.dbChan) == 0 {
		return
	}
	//log.Printf("[HISTORY] PRE Flushing batch of %d history entries", toProcess)
	h.batchMux.Lock()
	h.processing = true
	h.batchMux.Unlock()

	//log.Printf("[HISTORY] PRE processBatch of %d history entries", toProcess)
	h.processBatch()

	h.batchMux.Lock()
	h.processing = false
	h.lastFlush = time.Now()
	h.batchMux.Unlock()
}

// processBatch processes multiple entries atomically for optimal performance
func (h *History) processBatch() {
	if len(h.dbChan) == 0 {
		return // No items to process
	}
	log.Printf("[HISTORY] Starting batch processing of %d entries", len(h.dbChan))
	start2 := time.Now()
	// Step 2: Write all entries to database in batch (transaction-based)
	if jobs, err := h.writeBatchToDatabase(); err != nil {
		log.Printf("ERROR: Failed to write batch to database: %v", err)
		return
	} else {
		if HistoryDEBUG {
			log.Printf("[HISTORY] done BATCH writeBatchToDatabase: %d entries (took %v)", jobs, time.Since(start2))
		}
	}
}

const DefaultStorageSystem = 0x1

// writeBatchToDatabase writes multiple entries to the database using transactions
func (h *History) writeBatchToDatabase() (processed int, err error) {
	// Group entries by database index only - calculate routing on-demand
	dbGroups := make(map[int][]*MessageIdItem)
	var item *MessageIdItem
processingLoop:
	for {
		select {
		case item = <-h.dbChan:
			dbIndex, _, err := h.routeHash(item)
			if err != nil {
				return 0, fmt.Errorf("error in writeBatchToDatabase. failed to route hash for item '%#v': %v", item, err)
			}
			//if len(dbGroups[dbIndex]) == 0 {
			//	// First entry for this database, initialize any necessary structures
			//	dbGroups[dbIndex] = make([]*MessageIdItem, 0, 64) // Preallocate slice for performance
			//}
			// Group by database index using original MessageIdItem pointers
			dbGroups[dbIndex] = append(dbGroups[dbIndex], item)
			processed++
			if processed >= h.config.BatchSize {
				break processingLoop
			}
		default:
			break processingLoop
		}
	}
	if processed == 0 {
		return 0, nil // Nothing to process
	}

	// Process databases in parallel for better performance
	var wg sync.WaitGroup
	errChan := make(chan error, len(dbGroups))

	for dbIndex, msgIdItems := range dbGroups {
		if len(msgIdItems) == 0 {
			continue // Skip empty groups
		}
		wg.Add(1)
		go func(dbIdx int, dbEntries []*MessageIdItem) {
			defer wg.Done()
			start := time.Now()
			if err := h.writeBatchToHashDB(dbIdx, dbEntries); err != nil {
				errChan <- fmt.Errorf("failed to write batch to database dbIdx=%d: %v", dbIdx, err)
			}
			for _, item := range dbEntries {
				h.DelDBQueued(item)
			}
			if HistoryDEBUG {
				log.Printf("[HISTORY] writeBatchToDatabase: dbIndex=%d, processed %d entries took %v", dbIndex, len(dbEntries), time.Since(start))
			}
		}(dbIndex, msgIdItems)
	}

	wg.Wait()
	close(errChan)

	// Check for any errors
	var errs []error
	for aerr := range errChan {
		if aerr != nil {
			errs = append(errs, aerr)
		}
	}
	if len(errs) > 0 {
		return processed, fmt.Errorf("error in writeBatchToDatabase: %v", errs)
	}
	return
}

// writeBatchToHashDB writes all entries for a single database in one giant transaction
func (h *History) writeBatchToHashDB(dbIndex int, entries []*MessageIdItem) error {
	const maxRetries = 10000
	const baseDelay = 100 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := h.executeDBTransaction(dbIndex, entries)
		if err == nil {
			return nil
		}

		// Check if it's a transient lock error
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "database is locked") || strings.Contains(errStr, "busy") {
			if attempt < maxRetries {
				time.Sleep(baseDelay * time.Duration(attempt+1)) // Exponential backoff
				continue
			}
		}

		// Non-retryable error or max retries reached
		log.Fatalf("ERROR: Failed to write batch to database %d after %d attempts: %v", dbIndex, attempt+1, err)
		//return err
	}
	return fmt.Errorf("history writeBatchToHashDB max retries exceeded")
}

// executeDBTransaction executes one giant transaction for all tables in a database
func (h *History) executeDBTransaction(dbIndex int, entries []*MessageIdItem) error {
	// Get database connection
	//var txWg sync.WaitGroup
	var db *sql.DB
	var err error
	start := time.Now()
	db, err = h.db.GetShardedDB(dbIndex, true)
	if err != nil {
		return fmt.Errorf("failed to get database connection: %v", err)
	}

	// Start one giant transaction for all tables in this database
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			log.Printf("ERROR HISTORY Rolling back transaction due to error: %v", err)
			os.Exit(1) // @AI!! THIS IS A CRITICAL ERROR, WE MUST EXIT! DO NOT TOUCH THIS LINE OF CODE!!
		}
	}()

	// Group entries by table and hash within this database - calculate routing on-demand
	tableHashGroups := make(map[string][]*MessageIdItem)
	for _, msgIdItem := range entries {
		// Calculate routing info on-demand (no struct allocation)
		_, tableName, err := h.routeHash(msgIdItem)
		if err != nil {
			return fmt.Errorf("failed to route hash for entry: %v", err)
		}
		tableHashGroups[tableName] = append(tableHashGroups[tableName], msgIdItem)
		msgIdItem.Mux.Lock()
		msgIdItem.Response = CaseDupes                                 // Set response state to CaseDupes for processed articles
		msgIdItem.CachedEntryExpires = time.Now().Add(3 * time.Second) // Set cache expiration
		msgIdItem.Mux.Unlock()

	}

	// Process each table in this database with optimized batch operations
	for tableName, msgIdItems := range tableHashGroups {
		if err := h.processTableInTransaction(tx, tableName, msgIdItems); err != nil {
			return fmt.Errorf("error in history: failed to process table %s in transaction: %v", tableName, err)
		}
	}
	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %v", err)
	}
	if HistoryDEBUG {
		log.Printf("[HISTORY] executeDBTransaction: dbIndex=%d, committed %d entries in %v HistoryDEBUG=%t", dbIndex, len(entries), time.Since(start), HistoryDEBUG)
	}
	return nil
}

// processTableInTransaction efficiently processes all hash groups for a single table
func (h *History) processTableInTransaction(tx *sql.Tx, tableName string, msgIdItems []*MessageIdItem) error {
	start := time.Now()
	for {
		// Execute efficient bulk UPSERT operations
		if len(msgIdItems) > 0 {
			//start := time.Now()

			// Build bulk UPSERT with VALUES syntax
			var valuesList []string
			var args []interface{}

			for _, msgIdItem := range msgIdItems {
				// Build new offsets for this hash - optimized for 1-2 offsets typical case
				valuesList = append(valuesList, "(?,?)")

				var newsgroupIDsString string
				if len(msgIdItem.NewsgroupIDs) == 1 {
					// Most common case: single offset
					newsgroupIDsString = fmt.Sprintf("%d", msgIdItem.NewsgroupIDs[0])
				} else if len(msgIdItem.NewsgroupIDs) == 2 {
					// Second most common case: two offsets
					newsgroupIDsString = fmt.Sprintf("%d,%d", msgIdItem.NewsgroupIDs[0], msgIdItem.NewsgroupIDs[1])
				} else {
					// Rare case: 3+ offsets, fall back to slice+join
					var newOffsets []string
					for _, entry := range msgIdItem.NewsgroupIDs {
						newOffsets = append(newOffsets, fmt.Sprintf("%d", entry))
					}
					newsgroupIDsString = strings.Join(newOffsets, ",")
				}

				args = append(args, msgIdItem.MessageId, newsgroupIDsString)
				//log.Printf("[HISTORY] processTableInTransaction: hash '%s' has %d entries, offset string: '%s'", shortHash, len(hashEntries), offsetString)
			}

			// Execute single bulk UPSERT statement
			/* disabled, old
				bulkUpsertQuery := fmt.Sprintf(`
				INSERT INTO %s (message_id, newsgroups) VALUES %s
				ON CONFLICT(message_id) DO UPDATE SET
					newsgroups = newsgroups || ',' || excluded.newsgroups
			`, tableName, strings.Join(valuesList, ", "))
			*/

			// Execute single bulk UPSERT statement
			bulkUpsertQuery := fmt.Sprintf("INSERT INTO %s (message_id, newsgroups) VALUES %s", tableName, strings.Join(valuesList, ", "))
			if _, err := tx.Exec(bulkUpsertQuery, args...); err != nil {
				log.Printf("failed to bulk upsert into table %s: %v", tableName, err)
				time.Sleep(100 * time.Millisecond) // Wait before retrying
				continue                           // Retry the transaction
			}
			break
			//if HistoryDEBUG {
			//log.Printf("[HISTORY] processTableInTransaction: bulk upserted %d hashes, %d args, into table %s in %v", len(hashGroups), len(args), tableName, time.Since(start))
			//}
		}
		if HistoryDEBUG {
			log.Printf("[HISTORY] processTableInTransaction: bulk processed table %s with %d hashes in %v",
				tableName, len(msgIdItems), time.Since(start))
		}
	}
	return nil
}

// CheckNoMoreWorkInHistory checks if there's no more pending work (similar to CheckNoMoreWorkInMaps)
func (h *History) CheckNoMoreWorkInHistory() bool {
	// Check if writer channel has pending entries
	if h.dbMoreQueued() {
		return false
	}

	// Check if database channel has pending entries
	if len(h.dbChan) > 0 {
		return false
	}

	// Check if there's a pending batch
	h.batchMux.Lock()
	isProcessing := h.processing
	h.batchMux.Unlock()

	return !isProcessing
}

// SetDatabaseWorkChecker sets the database work checker interface for coordinated shutdown
func (h *History) SetDatabaseWorkChecker(checker DatabaseWorkChecker) {
	h.dbWorkChecker = checker
}
