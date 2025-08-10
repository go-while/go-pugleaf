package history

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const ENABLE_HISTORY = false // EXPERIMENTAL !

var HistoryDEBUG = false                // Set to true for spammy debug logs
var MaxLookupWorkers = runtime.NumCPU() // Use number of CPU cores for lookup workers

const (
	//MaxLookupWorkers = 64

	// History file constants
	DefaultHistoryDir = "data/history" // TODO set via config
	HistoryFileName   = "history.dat"

	// Cache configuration
	DefaultCacheExpires = 15 // seconds
	DefaultCachePurge   = 5  // seconds

	// History Write Batching configuration
	DefaultBatchSize    = 10000 // Number of entries to batch before flushing (reduced from 10000 to fix memory bloat)
	DefaultBatchTimeout = 5000  // Milliseconds to wait before forced flush

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
	MaxLookupWorkers = MaxLookupWorkers / 2
	if MaxLookupWorkers < 1 {
		MaxLookupWorkers = 1
	}
	h := &History{
		config:     config,
		stats:      &HistoryStats{},
		tickChan:   make(chan struct{}, 1),
		lookupChan: make(chan *MessageIdItem, MaxLookupWorkers*2),
		writerChan: make(chan *MessageIdItem, int(config.BatchSize*2)),
		dbChan:     make(chan *MessageIdItem, int(config.BatchSize*2)),
		stopChan:   make(chan struct{}),
		lastFlush:  time.Now(),
		mainWG:     mainWG, // Store the main application's waitgroup
	}
	if ENABLE_HISTORY {
		// Initialize database backend
		if err := h.initDatabase(); err != nil {
			return nil, fmt.Errorf("failed to initialize database: %w", err)
		}

		// Open history file
		if err := h.openHistoryFile(); err != nil {
			return nil, fmt.Errorf("failed to open history file: %w", err)
		}

		// Start worker goroutines with main application waitgroup coordination
		h.bootLookupWorkers()
	}
	go h.writerWorker() // +1 wg mainWG waitGroup
	return h, nil
}

// Add adds a new message-ID to history
func (h *History) Add(msgIdItem *MessageIdItem) {
	if !ENABLE_HISTORY {
		msgIdItem.Mux.Lock()
		msgIdItem.Response = CaseDupes
		msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
		msgIdItem.Mux.Unlock()
		return
	}
	if msgIdItem == nil {
		log.Printf("[HISTORY] ERROR: Add called with nil MessageIdItem")
		return
	}
	msgIdItem.Mux.Lock()
	if msgIdItem.MessageId == "" {
		log.Printf("[HISTORY] ERROR: Add called with empty MessageId item='%#v'", msgIdItem)
		msgIdItem.Mux.Unlock()
		return
	}
	if msgIdItem.StorageToken == "" && (msgIdItem.GroupName == nil || *msgIdItem.GroupName == "" || msgIdItem.ArtNum <= 0) {
		log.Printf("[HISTORY] ERROR: Add called with invalid MessageIdItem='%#v'", msgIdItem)
		msgIdItem.Mux.Unlock()
		return
	}
	if msgIdItem.FileOffset > 0 {
		log.Printf("[HISTORY] ERROR: Add called with already stored MessageIdItem='%v'", msgIdItem)
		msgIdItem.Mux.Unlock()
		return
	}
	if msgIdItem.Response != CaseLock {
		/*
			if msgIdItem.MessageId == "<32304224.79C1@parkcity.com>" {
				log.Printf("[DEBUG-HISTORY-STEP8-FAIL] Target message ID blocked from entering history! Response: %x (expected: %x)", msgIdItem.Response, CaseLock)
			}
		*/
		msgIdItem.Mux.Unlock()
		log.Printf("[HISTORY] DUPLICATE to Add msgId='%s' case: %x != %x", msgIdItem.MessageId, msgIdItem.Response, CaseLock)
		return
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
	if len(h.writerChan) >= h.config.BatchSize {
		select {
		case h.tickChan <- NOTIFY:
			// pass
		default:
			// full
		}
	}
	h.lookupChan <- msgIdItem
	// Add entry to pending batch
	//log.Printf("[HISTORY] Adding msgId='%s' to writer chan (queued %d/%d)", msgIdItem.MessageId, len(h.writerChan), cap(h.writerChan))

	// Successfully queued for batch processing
	//h.updateStats(func(s *HistoryStats) { s.TotalAdds++ })

}

func (h *History) bootLookupWorkers() {
	log.Printf("[HISTORY] Starting %d lookup workers", MaxLookupWorkers)
	for i := 1; i <= MaxLookupWorkers; i++ {
		go h.LookupWorker(i)
	}
	time.Sleep(1000 * time.Millisecond) // Give workers time to start
}

func (h *History) LookupWorker(wid int) {
	//log.Printf("[HISTORY] LookupWorker %03d started", wid)
	processed := 0
	maxWork := 65536 / MaxLookupWorkers
	if maxWork < 16384 {
		maxWork = 16384 // Ensure minimum work per worker
	}
	restart := false
	for {
		if restart {
			break
		}
		msgIdItem := <-h.lookupChan
		processed++
		if processed >= maxWork {
			restart = true
		}
		start := time.Now()
		if response, err := h.Lookup(msgIdItem); err != nil || response != CasePass {
			msgIdItem.Mux.Lock()
			if response != CaseDupes {
				log.Printf("[HISTORY] DEBUG Add()->Lookup(): msgId: '%s'response = %x != CasePass entry.msgIdItem.Response=%x", msgIdItem.MessageId, response, msgIdItem.Response)
			}
			msgIdItem.CachedEntryExpires = time.Now().Add(3 * time.Second)
			msgIdItem.Mux.Unlock()
			//h.updateStats(func(s *HistoryStats) { s.Duplicates++ })
			if HistoryDEBUG {
				log.Printf("[HISTORY] Add()->Lookup(): Duplicate msgId: '%s' lookup took %v err='%v'", msgIdItem.MessageId, time.Since(start), err)
			}
			continue
		}
		msgIdItem.Mux.Lock()
		msgIdItem.Response = CaseWrite // Set to write state // FIXMEE
		//msgIdItem.CachedEntryExpires = time.Now().Add(CachedEntryTTL)
		msgIdItem.Mux.Unlock()
		h.writerChan <- msgIdItem // Send to writer channel for processing
		if HistoryDEBUG {
			log.Printf("[HISTORY] Add()->Lookup(): msgId: '%s' not found, lookup took %v (queued %d)", msgIdItem.MessageId, time.Since(start), len(h.writerChan))
		}
	}
	//log.Printf("[HISTORY] LookupWorker (%03d/%03d) did %d/%d, restarting...", wid, MaxLookupWorkers, processed, maxWork)
	go h.LookupWorker(wid)
}

// Lookup checks if a message-ID exists in history
// Returns: ResponsePass (0) = not found, ResponseDuplicate (1) = found, ResponseRetry (2) = error
func (h *History) Lookup(msgIdItem *MessageIdItem) (int, error) {
	if !ENABLE_HISTORY {
		return CasePass, nil
	}
	found, err := h.lookupInDatabase(msgIdItem)
	//log.Printf("[HISTORY] Lookup for msgId='%s' found='%v', err='%v'", msgIdItem.MessageId, found, err)
	if err != nil {
		log.Printf("[HISTORY] ERROR: Lookup failed for msgId='%s': %v", msgIdItem.MessageId, err)
		//h.updateStats(func(s *HistoryStats) { s.Errors++ })
		return CaseError, err
	}
	//log.Printf("Lookup for msgID: '%s' found='%v', offsets='%v'", messageID, found, offsetsData)
	if found {
		//h.updateStats(func(s *HistoryStats) { s.TotalLookups++ })
		return CaseDupes, nil
	}
	return CasePass, nil
}

// lookupInDatabase looks up a hash in the sharded database
// Returns: bool (found), error
func (h *History) lookupInDatabase(msgIdItem *MessageIdItem) (bool, error) {
	// Route hash: 1st char -> DB, 2nd+3rd chars -> table, remaining -> stored value
	start1 := time.Now()
	dbIndex, tableName, shortHash, err := h.routeHash(msgIdItem.MessageId)
	if err != nil {
		return false, fmt.Errorf("failed to route hash: %v", err)
	}
	if HistoryDEBUG {
		log.Printf("[HISTORY] #0 lookupInDatabase: routed hash '%s' to dbIndex=%d, tableName='%s', shortHash='%s' took %v", msgIdItem.MessageIdHash, dbIndex, tableName, shortHash, time.Since(start1))
	}

	// Get database connection
	var db *sql.DB
	//start2 := time.Now()

	db, err = h.db.GetShardedDB(dbIndex, false)
	if err != nil {
		return false, fmt.Errorf("failed to get database connection: %v", err)
	}

	/*
		if HistoryDEBUG || time.Since(start2) > 1*time.Millisecond {
			log.Printf("[HISTORY] #1 lookupInDatabase: got database connection took %v", time.Since(start2))
		}
	*/

	// Query database for file offsets with optimized retry logic
	var offsetsData string
	start3 := time.Now()
	baseDelay := 10 * time.Millisecond

	for {
		err = db.QueryRow("SELECT o FROM "+tableName+" WHERE h = ?", shortHash).Scan(&offsetsData)
		if err != nil {
			if err == sql.ErrNoRows {
				if HistoryDEBUG {
					log.Printf("[HISTORY] #2.1 lookupInDatabase: sql.ErrNoRows dbIndex=%x tableName=%s shortHash='%s' took %v", dbIndex, tableName, shortHash, time.Since(start3))
				}
				return false, nil // Not found
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
	// Parse file offsets
	offsetStrings := strings.Split(offsetsData, ",")
	var offsets []int64 // TODO GET FROM SYNC POOL
	// Check each offset for hash collisions
	for _, offsetStr := range offsetStrings {
		offset, err := strconv.ParseInt(strings.TrimSpace(offsetStr), 10, 64)
		if err != nil {
			log.Printf("WARN: Invalid offset in database: %s", offsetStr)
			continue
		}
		offsets = append(offsets, offset) // Append to offsets slice
	}
	// Check each offset for hash collisions
	for _, offset := range offsets {
		// Read and verify the history entry at this offset
		response, err := h.readHistoryEntryAtOffset(offset, msgIdItem)
		if err != nil {
			log.Printf("WARN: Failed to read history entry at offset %d: %v", offset, err)
			continue
		}
		//h.updateStats(func(s *HistoryStats) { s.TotalFileLookups++ })
		/*
			if msgIdItem.MessageId == "<32304224.79C1@parkcity.com>" {
				log.Printf("[DEBUG-HISTORY-STEP15] Target message ID readHistoryEntryAtOffset response: %x msgIdItem='%#v'", response, msgIdItem)
			}*/

		switch response {
		case CaseError:
			log.Printf("ERROR: Failed to read history entry at offset %d: %v", offset, err)
			h.updateStats(func(s *HistoryStats) { s.Errors++ })
			return false, err

		case CaseRetry:
			continue // Hash collision, not a match

		case CaseDupes:
			// Found a matching entry, returns item with storage token added from readHistoryEntryAtOffset
			return true, nil // Found a matching entry, return storage token in Item pointer
		} // end switch response
	}
	// No matching entries found
	return false, nil
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
	log.Printf("History writer worker started (batching enabled: size=%d, timeout=%dms)",
		h.config.BatchSize, h.config.BatchTimeout)

	//counter := 100
	//shutdown := false
	// Initialize batch timeout timer

	go func(h *History) {
		ticker := time.NewTicker(50 * time.Millisecond)
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
			chansize = len(h.writerChan)
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
		defer log.Printf("History writer worker stopped")
		defer h.mainWG.Done()

		shutdownCounter := 100
		chanSize := 0
		chanlimit := false
		for {
			<-h.tickChan
			// Handle shutdown
			if h.ServerShutdown() && h.CheckNoMoreWorkInHistory() {
				//log.Printf("[HISTORY] writerWorker Server shutdown initiated, checking for pending work...")
				time.Sleep(100 * time.Millisecond)
				shutdownCounter--

				if !h.dbWorkChecker.CheckNoMoreWorkInMaps() {
					shutdownCounter = 100
					continue
				}
				h.flushPendingBatch()
				if shutdownCounter <= 0 && len(h.writerChan) == 0 {
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
			chanSize = len(h.writerChan)
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

// readHistoryEntryAtOffset reads and parses a history entry at a specific file offset
func (h *History) readHistoryEntryAtOffset(offset int64, msgIdItem *MessageIdItem) (int, error) {
	getTimestamp := false

	msgIdItem.Mux.RLock()
	if msgIdItem.Arrival == 0 {
		getTimestamp = true
	}
	/*
		if msgIdItem.MessageIdHash == "" {
			msgIdItem.Mux.RUnlock()
			return CaseError, fmt.Errorf("readHistoryEntryAtOffset called with empty MessageIdHash")
		}
	*/

	if msgIdItem.StorageToken != "" /*|| (msgIdItem.GroupName != nil && msgIdItem.ArtNum > 0) fucksup with history rebuild... TODO REVIEW */ {
		//log.Printf("[HISTORY] readHist: already have storage token='%s' msgId='%s'", msgIdItem.StorageToken, msgIdItem.MessageId)
		// already have storage token or group/article info, no need to read file
		msgIdItem.Mux.RUnlock()
		return CaseDupes, nil
	}
	//if HistoryDEBUG {
	//	log.Printf("[HISTORY] readHistoryEntryAtOffset called for MessageIdHash: '%s' at offset %d", msgIdItem.MessageIdHash, offset)
	//}
	msgIdItem.Mux.RUnlock()

	// Open read-only file handle for this specific read
	file, err := os.Open(h.HistoryFilePath)
	if err != nil {
		return CaseError, fmt.Errorf("failed to open history file for reading: %v", err)
	}
	// Seek to offset and read line
	_, err = file.Seek(offset, io.SeekStart)
	if err != nil {
		file.Close()
		return CaseError, fmt.Errorf("failed to seek to offset %d: %v", offset, err)
	}

	reader := bufio.NewReader(file)
	line, err := reader.ReadString('\n')
	if err != nil {
		file.Close()
		return CaseError, fmt.Errorf("failed to read line at offset %d: %v", offset, err)
	}
	file.Close()
	//log.Printf("[HISTORY] readHistoryEntryAtOffset: read line at offset %d: '%s'", offset, result)

	// Parse history line: "hash storagetoken timestamp messageid"
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) < 4 {
		return CaseError, fmt.Errorf("invalid history format offset=%d line='%s'", offset, line)
	}

	//messageID := parts[0]
	//storageSystem := parts[1]
	//storageToken := parts[2]
	//timestampStr := parts[3]

	msgIdItem.Mux.Lock()
	if msgIdItem.StorageToken != "" {
		msgIdItem.Mux.Unlock()
		return CaseDupes, nil
	}
	if parts[0] != msgIdItem.MessageId {
		log.Printf("[HISTORY] readHistoryEntryAtOffset:mismatch for (msgId='%s') at offset %d parts='%#v'", msgIdItem.MessageId, offset, parts)
		msgIdItem.Mux.Unlock()
		return CaseRetry, nil
	}
	msgIdItem.StorageToken = parts[2] // Set storage token
	msgIdItem.Mux.Unlock()

	if HistoryDEBUG {
		log.Printf("[HISTORY] readHistoryEntryAtOffset: msgId='%s' at offset %d => token='%s'", msgIdItem.MessageId, offset, msgIdItem.StorageToken)
	}

	if getTimestamp {
		timestamp, err := strconv.ParseInt(parts[3][:len(parts[3])-1], 10, 64)
		if err != nil {
			return CaseError, fmt.Errorf("invalid timestamp in history file: %s", parts[3])
		}
		msgIdItem.Mux.Lock()
		msgIdItem.Arrival = timestamp // Set arrival time
		msgIdItem.Mux.Unlock()
	}
	return CaseDupes, nil
}

// routeHash routes a hash to the correct database and table
// Returns: dbIndex, tableName, shortHash (for storage), error
func (h *History) routeHash(msgId string) (int, string, string, error) {
	/*
		minCharsNeeded := 3 + h.config.UseShortHashLen // routing chars + storage chars
		if len(hash) < minCharsNeeded {
			return 0, "", "", fmt.Errorf("hash too short: need at least %d characters, got %d", minCharsNeeded, len(hash))
		}
	*/
	// For sharded database modes:
	// 1st char -> database index (0-f maps to 0-15)
	// 2nd+3rd chars -> table name (s + 00-ff)
	// 4th-Nth chars -> stored hash value (configurable 2-7 chars) (max 10 bits of entropy = 16^10 = 1,099511628×10¹² ...)
	hash := ComputeMessageIDHash(msgId)[:3+h.config.UseShortHashLen] // Compute the hash of the message ID
	dbChar := hash[0:1]
	tableChars := hash[1:3]
	shortHash := hash[3:]
	if len(shortHash) > h.config.UseShortHashLen {
		shortHash = shortHash[:h.config.UseShortHashLen] // Limit to configured length
	}

	// Convert first hex char to database index
	dbIndex, err := hexToInt(dbChar)
	if err != nil {
		return 0, "", "", fmt.Errorf("invalid hex char for database: %s", dbChar)
	}

	// Validate database index
	numDBs, _, _ := GetShardConfig(h.config.ShardMode)
	if dbIndex >= numDBs {
		return 0, "", "", fmt.Errorf("database index %d exceeds available databases %d", dbIndex, numDBs)
	}

	// Table name from 2nd+3rd hex chars (s + hex)

	return dbIndex, "s" + tableChars, shortHash, nil
}

// flushPendingBatch processes all entries in the current batch atomically
func (h *History) flushPendingBatch() {
	if len(h.writerChan) == 0 {
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
	jobs := len(h.writerChan)
	if jobs == 0 {
		return // No items to process
	}
	// Step 1: Write all entries to history file in sequence (atomic file operations)
	start1 := time.Now()
	if err := h.writeBatchToFile(); err != nil {
		log.Printf("ERROR: Failed to write batch to file: %v", err)
		return
	}
	if HistoryDEBUG {
		log.Printf("[HISTORY] done BATCH writeBatchToFile: %d entries (took %v)", jobs, time.Since(start1))
	}

	start2 := time.Now()
	// Step 2: Write all entries to database in batch (transaction-based)
	if err := h.writeBatchToDatabase(); err != nil {
		log.Printf("ERROR: Failed to write batch to database: %v", err)
		return
	}
	if HistoryDEBUG {
		log.Printf("[HISTORY] done BATCH writeBatchToDatabase: %d entries (took %v)", jobs, time.Since(start2))
	}

	// All entries processed successfully
	//if HistoryDEBUG {
	if jobs > h.config.BatchSize {
		jobs = h.config.BatchSize
	}
	log.Printf("[HISTORY] ADD BATCHED: %d (took %v)", jobs, time.Since(start1))
	//}
}

const DefaultStorageSystem = 0x1

// writeBatchToFile writes multiple entries to the history file atomically
/*
tail history.dat
3e516276738e1312fd0e8190ecdefacc de.rec.tiere.aquaristik:4491 1752178804 <3f389015$0$20726$91cee783@newsreader02.highway.telekom.at>
c4741046fca0ce7fac49df105e487cc5 de.rec.tiere.aquaristik:4492 1752178804 <bha47a$i8$07$1@news.t-online.com>
5028257d375b9b51e6440b47cc5ec810 de.rec.tiere.aquaristik:4493 1752178804 <bha6f0$qi9$03$1@news.t-online.com>
*/
func (h *History) writeBatchToFile() error {
	h.mux.Lock()
	defer h.mux.Unlock()
	/*
		// Get the ACTUAL current file position instead of relying on cached offset
		actualOffset, err := h.historyFile.Seek(0, io.SeekCurrent)
		if err != nil {
			return fmt.Errorf("failed to get current file position: %v", err)
		}

		// Verify our cached offset matches reality
		if actualOffset != h.offset {
			log.Printf("[HISTORY] WARNING: Cached offset %d != actual file position %d, correcting", h.offset, actualOffset)
			h.offset = actualOffset
		}

		// Reset the buffered writer to ensure it's positioned correctly
		h.fileWriter.Reset(h.historyFile)
	*/

	// Capture file offsets for each entry BEFORE writing
	currentOffset := h.offset
	now := time.Now().Unix()
	processed, skipped := 0, 0
	totalBytes := int64(0)
	start := time.Now()
	dbChanBefore := len(h.dbChan)
processingLoop:
	for {
		select {
		case item := <-h.writerChan:
			/*
				if item.MessageId == "<32304224.79C1@parkcity.com>" {
					// debug
					log.Printf("[HISTORY] DEBUG: Writing batch entry for MessageId '%s' at offset %d", item.MessageId, currentOffset)
				}
			*/

			item.Mux.Lock()
			// Only generate storage token if it's not already set (avoid redundant fmt.Sprintf calls)
			if item.StorageToken == "" {
				if item.GroupName == nil || item.ArtNum <= 0 {
					log.Printf("[HISTORY-ERROR] Missing storage info for item: GroupName=%v ArtNum=%d", item.GroupName, item.ArtNum)
					item.Mux.Unlock()
					skipped++
					continue // Skip items without proper storage info
				}
				item.StorageToken = fmt.Sprintf("%s:%d", *item.GroupName, item.ArtNum)
			}
			// Set the file offset before writing
			// Write directly to buffered writer
			n, err := fmt.Fprintf(h.fileWriter, "%s\t%x\t%s\t%d\n",
				item.MessageId,       // message-Id <a@b.c>
				DefaultStorageSystem, // placeholder for storage system, e.g. 0x1
				item.StorageToken,    // <newsgroup>:<artnum>
				now,                  // timestamp
			)
			if err != nil {
				item.Mux.Unlock()
				return fmt.Errorf("failed to write history line to file: %v", err)
			}
			item.Arrival = now // Set arrival time to current time
			item.FileOffset = currentOffset
			item.Mux.Unlock()
			currentOffset += int64(n)
			totalBytes += int64(n)
			h.dbChan <- item
			processed++
			if processed >= h.config.BatchSize {
				//log.Printf("[HISTORY] writeBatchToFile reached %d entries, flushing...", processed)
				// Flush batch immediately if size limit reached
				break processingLoop
			}
		default:
			//log.Printf("[HISTORY] writeBatchToFile processed %d entries, flushing...", processed)
			break processingLoop // No more items to process
		}
	}
	startFlush := time.Now()
	// Flush buffered writer to ensure all data is written
	if err := h.fileWriter.Flush(); err != nil {
		return fmt.Errorf("failed to flush history file buffer: %v", err)
	}

	// Update global offset
	h.offset += totalBytes

	/*
		// Ensure data is written to disk
		if err := h.historyFile.Sync(); err != nil {
			log.Printf("WARN: Failed to sync history file: %v", err)
			// Continue anyway, data is in OS buffer
		}
	*/
	log.Printf("writeBatchToFile: %d entries took %v h.dbChan=%d=>%d flush: %v (written: %d)", processed, time.Since(start), dbChanBefore, len(h.dbChan), time.Since(startFlush), totalBytes)
	return nil
}

// writeBatchToDatabase writes multiple entries to the database using transactions
func (h *History) writeBatchToDatabase() error {
	// Group entries by database index only - calculate routing on-demand
	dbGroups := make(map[int][]*MessageIdItem)
	processed := 0
processingLoop:
	for {
		select {
		case item := <-h.dbChan:
			/*
				minCharsNeeded := 3 + h.config.UseShortHashLen
				if len(item.MessageIdHash) < minCharsNeeded {
					return fmt.Errorf("hash too short: need at least %d characters, got %d [item='%#v']", minCharsNeeded, len(item.MessageIdHash), item)
				}
			*/
			// Calculate only database index - no temporary struct needed
			dbIndex, _, _, err := h.routeHash(item.MessageId)
			if err != nil {
				return fmt.Errorf("failed to route hash: %v", err)
			}
			if len(dbGroups[dbIndex]) == 0 {
				// First entry for this database, initialize any necessary structures
				dbGroups[dbIndex] = make([]*MessageIdItem, 0, 640) // Preallocate slice for performance
			}
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

	// Process databases in parallel for better performance
	var wg sync.WaitGroup
	errChan := make(chan error, len(dbGroups))

	for dbIndex, entries := range dbGroups {
		if len(entries) == 0 {
			continue // Skip empty groups
		}
		wg.Add(1)
		go func(dbIdx int, dbEntries []*MessageIdItem) {
			defer wg.Done()
			start := time.Now()
			if err := h.writeBatchToHashDB(dbIdx, dbEntries); err != nil {
				errChan <- fmt.Errorf("failed to write batch to database dbIdx=%d: %v", dbIdx, err)
			}
			if HistoryDEBUG {
				log.Printf("[HISTORY] writeBatchToDatabase: dbIndex=%d, processed %d entries took %v", dbIndex, len(dbEntries), time.Since(start))
			}
		}(dbIndex, entries)
	}

	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	return nil
}

// writeBatchToHashDB writes all entries for a single database in one giant transaction
func (h *History) writeBatchToHashDB(dbIndex int, entries []*MessageIdItem) error {
	const maxRetries = 1000
	const baseDelay = 100 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := h.executeDBTransaction(dbIndex, entries)
		if err == nil {
			return nil
		}

		// Check if it's a transient lock error
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "database is locked") || strings.Contains(errStr, "busy") {
			if attempt < maxRetries-1 {
				time.Sleep(baseDelay)
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
	tableHashGroups := make(map[string]map[string][]*MessageIdItem)
	for _, entry := range entries {
		// Calculate routing info on-demand (no struct allocation)
		_, tableName, shortHash, err := h.routeHash(entry.MessageId)
		if err != nil {
			return fmt.Errorf("failed to route hash for entry: %v", err)
		}

		if tableHashGroups[tableName] == nil {
			tableHashGroups[tableName] = make(map[string][]*MessageIdItem)
		}
		tableHashGroups[tableName][shortHash] = append(tableHashGroups[tableName][shortHash], entry)
		entry.Mux.Lock()
		entry.Response = CaseDupes                                 // Set response state to CaseDupes for processed articles
		entry.CachedEntryExpires = time.Now().Add(3 * time.Second) // Set cache expiration
		entry.Mux.Unlock()

	}

	// Process each table in this database with optimized batch operations

	for tableName, hashGroups := range tableHashGroups {
		//txWg.Add(1)
		//go func(tx *sql.Tx, tableName string, hashGroups map[string][]*EntryWithRouting) {
		//	defer txWg.Done()
		// Process this table in the transaction
		if err := h.processTableInTransaction(tx, tableName, hashGroups); err != nil {
			log.Printf("ERROR: Failed to process table %s in transaction: %v", tableName, err)
			/*
				err := tx.Rollback() // Rollback on error
				if err != nil {
					log.Printf("ERROR: Failed to rollback transaction: %v", err)
				}
			*/
			log.Printf("ERROR HISTORY Rolling back transaction due to error: %v", err) // @AI!! THIS IS A CRITICAL ERROR, WE MUST EXIT! DO NOT TOUCH THIS LINE OF CODE!!
			return fmt.Errorf("failed to process table %s in transaction: %v", tableName, err)
		}
		//}(tx, tableName, hashGroups)
	}
	//txWg.Wait() // Wait for all table processing goroutines to finish
	// Commit the giant transaction
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
func (h *History) processTableInTransaction(tx *sql.Tx, tableName string, hashGroups map[string][]*MessageIdItem) error {
	start := time.Now()
	for {
		// Execute efficient bulk UPSERT operations
		if len(hashGroups) > 0 {
			//start := time.Now()

			// Build bulk UPSERT with VALUES syntax
			var valuesList []string
			var args []interface{}

			for shortHash, hashEntries := range hashGroups {
				// Build new offsets for this hash - optimized for 1-2 offsets typical case
				valuesList = append(valuesList, "(?, ?)")

				var offsetString string
				if len(hashEntries) == 1 {
					// Most common case: single offset
					offsetString = fmt.Sprintf("%d", hashEntries[0].FileOffset)
				} else if len(hashEntries) == 2 {
					// Second most common case: two offsets
					offsetString = fmt.Sprintf("%d,%d", hashEntries[0].FileOffset, hashEntries[1].FileOffset)
				} else {
					// Rare case: 3+ offsets, fall back to slice+join
					var newOffsets []string
					for _, entry := range hashEntries {
						newOffsets = append(newOffsets, fmt.Sprintf("%d", entry.FileOffset))
					}
					offsetString = strings.Join(newOffsets, ",")
				}

				args = append(args, shortHash, offsetString)
				//log.Printf("[HISTORY] processTableInTransaction: hash '%s' has %d entries, offset string: '%s'", shortHash, len(hashEntries), offsetString)
			}

			// Execute single bulk UPSERT statement
			bulkUpsertQuery := fmt.Sprintf(`
			INSERT INTO %s (h, o) VALUES %s
			ON CONFLICT(h) DO UPDATE SET
				o = o || ',' || excluded.o
		`, tableName, strings.Join(valuesList, ", "))

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
				tableName, len(hashGroups), time.Since(start))
		}
	}
	return nil
}

// LookupStorageToken looks up a message-ID and returns its storage token
// This function:
// 1. Looks up the message-ID in the history database to get file offsets
// 2. Reads the actual history file entries at those offsets
// 3. Matches the hash and message-ID to handle hash collisions
// 4. Returns the storage token if found
func (h *History) xxLookupStorageToken(msgIdItem *MessageIdItem) int {
	log.Printf("ERROR LEGACY FUNCTION CALLED LookupStorageToken")
	return CaseError // Default to error case
	/*
		msgIdItem.Mux.RLock()
		iserror := msgIdItem.Response == CaseError || msgIdItem.MessageId == ""
		isset := msgIdItem.StorageToken != ""
		//ispass := msgIdItem.Response == CasePass
		msgIdItem.Mux.RUnlock()

		if iserror {
			msgIdItem.Mux.Lock()
			msgIdItem.Response = CaseError
			msgIdItem.Mux.Unlock()
			log.Printf("[HISTORY] ERROR: LookupStorageToken called with invalid MessageIdItem")
			return CaseError // Invalid input, return error code
		}
		if isset {
			return CaseDupes // Already exists, return storage token
		}
		//if ispass {
		//	return CasePass // Already checked and not found, return empty string
		//}
		// Look up in database to get file offsets
		found, err := h.lookupInDatabase(msgIdItem)
		if err != nil {
			msgIdItem.Mux.Lock()
			msgIdItem.Response = CaseError
			msgIdItem.Mux.Unlock()
			h.updateStats(func(s *HistoryStats) { s.Errors++ })
			return CaseError // Error occurred, return empty string
		}
		if !found {
			msgIdItem.Mux.Lock()
			if msgIdItem.Response == 0 {
				log.Printf("[HISTORY] LookupStorageToken: MessageIdHash '%s' not found in database, setting response to CasePass", msgIdItem.MessageIdHash)
				// Only set to CasePass if not already set to something else
				//log.Printf("[HISTORY] LookupStorageToken: MessageIdHash '%s' not found in database, setting response to CasePass", msgIdItem
				//msgIdItem.Response = CasePass // Set response state to CasePass for not found
			}
			msgIdItem.Mux.Unlock()
			return CasePass // Not found
		}
		msgIdItem.Mux.Lock()
		if msgIdItem.Response == CasePass {
			log.Printf("[HISTORY] LookupStorageToken: MessageIdHash '%s' found in database, setting response to CaseDupes", msgIdItem.MessageIdHash)
			msgIdItem.Response = CaseDupes // only set to CaseDupes if not anything else is already set
		}
		msgIdItem.Mux.Unlock()
		h.updateStats(func(s *HistoryStats) { s.TotalLookups++ })
		return CaseDupes
	*/
}

// CheckNoMoreWorkInHistory checks if there's no more pending work (similar to CheckNoMoreWorkInMaps)
func (h *History) CheckNoMoreWorkInHistory() bool {
	// Check if writer channel has pending entries
	if len(h.writerChan) > 0 {
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
