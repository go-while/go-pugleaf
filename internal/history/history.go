package history

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
)

// ENABLE_HISTORY is read ONCE by NewHistory. Changing it later does not affect a running instance.
var ENABLE_HISTORY = false // EXPERIMENTAL !

var HistoryDEBUG = false // Set to true for spammy debug logs

// ErrHistoryClosed is returned by reads after Close()
var ErrHistoryClosed = errors.New("history is closed")

const (

	// History file constants
	DefaultHistoryDir = "./data/history" // TODO set via config
	HistoryFileName   = "history.dat"    // legacy, used by cmd/history-rebuild

	// Cache configuration
	DefaultCacheExpires = 15 // seconds
	DefaultCachePurge   = 5  // seconds

	// History Write Batching configuration
	DefaultBatchSize    = 10000 // Number of ops per flush
	DefaultBatchTimeout = 1000  // Milliseconds between forced flushes. maps to h.config.BatchTimeout

	// Sharding configuration constants
	SHARD_16_256 = 2 // 16 DBs with 256 tables each (recommended)
)

const (
	historyNumDBs = 16 // must match GetShardConfig

	// Begin retry on busy/locked (busy_timeout already waits 30s per attempt)
	beginRetryBaseDelay = 100 * time.Millisecond
	beginRetryMaxDelay  = 5 * time.Second
	beginMaxRetries     = 100

	// writer poll interval while shutting down
	shutdownPollInterval = 50 * time.Millisecond
)

// history op kinds
const (
	opAdd    uint8 = 1
	opRemove uint8 = 2
)

// historyOp is one queued write: add or remove groupID for messageID
type historyOp struct {
	op        uint8
	messageID string
	groupID   int64
}

// routedOp is a historyOp with its target table in the routed database file
type routedOp struct {
	historyOp
	tableName string
}

const (
	// add groupID to the CSV if it is not already in it
	sqlAddArticle = `INSERT INTO %s(message_id,newsgroups) VALUES(?,?) ON CONFLICT(message_id) DO UPDATE SET newsgroups = CASE WHEN coalesce(newsgroups,'')='' THEN excluded.newsgroups ELSE newsgroups||','||excluded.newsgroups END WHERE instr(','||coalesce(newsgroups,'')||',', ','||excluded.newsgroups||',') = 0`
	// remove groupID from the CSV, then drop the row if no group is left
	sqlRemoveArticle = `UPDATE %s SET newsgroups = trim(replace(','||newsgroups||',', ','||?||',', ','), ',') WHERE message_id=?`
	sqlDeleteEmpty   = `DELETE FROM %s WHERE message_id=? AND coalesce(newsgroups,'')=''`
)

// NewHistory creates a new history manager.
// The mainWG parameter (may be nil) is the main application's waitgroup: when enabled
// it gets +1 for the writer goroutine, which calls Done() after its final flush.
func NewHistory(cfg *HistoryConfig, mainWG *sync.WaitGroup) (*History, error) {
	enabled := ENABLE_HISTORY // read once

	if cfg == nil {
		cfg = DefaultConfig()
	}
	if err := cfg.ValidateConfig(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	h := &History{
		config:     cfg,
		enabled:    enabled,
		closeChan:  make(chan struct{}),
		writerDone: make(chan struct{}),
	}
	if !enabled {
		log.Printf("[HISTORY] history index disabled")
		return h, nil
	}

	// Create history directory
	if !dirExists(cfg.HistoryDir) {
		if !mkdir(cfg.HistoryDir) {
			return nil, fmt.Errorf("failed to create history directory: %s", cfg.HistoryDir)
		}
	}

	// Initialize database backend
	if err := h.initDatabase(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	h.opChan = make(chan historyOp, cfg.BatchSize*2)
	h.mainWG = mainWG
	h.writerAlive = true
	if mainWG != nil {
		mainWG.Add(1)
	}
	go h.writerWorker() // mainWG.Done() when finished
	return h, nil
}

// Enabled reports whether this instance maintains the history index
func (h *History) Enabled() bool {
	return h != nil && h.enabled
}

// AddArticle queues "messageID is stored in groupID". Idempotent. Blocks when the queue is full (backpressure).
func (h *History) AddArticle(messageID string, groupID int64) {
	h.enqueue(opAdd, messageID, groupID)
}

// RemoveArticle queues "messageID is no longer stored in groupID" (trimming).
func (h *History) RemoveArticle(messageID string, groupID int64) {
	h.enqueue(opRemove, messageID, groupID)
}

// enqueue returns true if the op was queued
func (h *History) enqueue(kind uint8, messageID string, groupID int64) bool {
	if h == nil || !h.enabled {
		return false
	}
	if messageID == "" || groupID <= 0 {
		if n := h.invalidOps.Add(1); n <= 10 || n%10000 == 0 {
			log.Printf("[HISTORY] WARN: ignoring invalid op=%d messageID='%s' groupID=%d (invalid ops: %d)", kind, messageID, groupID, n)
		}
		return false
	}
	// count first, then check closed: the writer checks closed before pending, so it can not miss this op
	h.pending.Add(1)
	if h.closed.Load() {
		h.pending.Add(-1)
		if h.closedWarned.CompareAndSwap(false, true) {
			log.Printf("[HISTORY] WARN: op after Close() dropped: op=%d messageID='%s' groupID=%d (further drops are not logged)", kind, messageID, groupID)
		}
		return false
	}
	h.opChan <- historyOp{op: kind, messageID: messageID, groupID: groupID}
	if kind == opAdd {
		h.stats.adds.Add(1)
	} else {
		h.stats.removes.Add(1)
	}
	return true
}

// Exists checks if a message-ID has a row in the history index
func (h *History) Exists(messageID string) (bool, error) {
	if h == nil || !h.enabled || messageID == "" {
		return false, nil
	}
	if h.dbClosed.Load() {
		return false, ErrHistoryClosed
	}
	db, tableName, err := h.readDB(messageID)
	if err != nil {
		return false, err
	}
	h.stats.lookups.Add(1)
	var one int
	err = db.QueryRow("SELECT 1 FROM "+tableName+" WHERE message_id = ? LIMIT 1", messageID).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		h.stats.errors.Add(1)
		return false, fmt.Errorf("history exists query failed: %w", err)
	}
	h.stats.duplicates.Add(1)
	return true, nil
}

// LookupGroups returns the newsgroup IDs stored for messageID.
// nil, nil = not found; a non-nil (maybe empty) slice = row exists.
func (h *History) LookupGroups(messageID string) ([]int64, error) {
	if h == nil || !h.enabled || messageID == "" {
		return nil, nil
	}
	if h.dbClosed.Load() {
		return nil, ErrHistoryClosed
	}
	db, tableName, err := h.readDB(messageID)
	if err != nil {
		return nil, err
	}
	h.stats.lookups.Add(1)
	var newsgroups sql.NullString
	err = db.QueryRow("SELECT newsgroups FROM "+tableName+" WHERE message_id = ?", messageID).Scan(&newsgroups)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		h.stats.errors.Add(1)
		return nil, fmt.Errorf("history lookup query failed: %w", err)
	}
	h.stats.duplicates.Add(1)
	return parseGroupIDs(newsgroups.String), nil
}

// parseGroupIDs parses a CSV of group IDs, skipping empty/invalid parts. Never returns nil.
func parseGroupIDs(csv string) []int64 {
	ids := make([]int64, 0, strings.Count(csv, ",")+1)
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			if HistoryDEBUG {
				log.Printf("[HISTORY] WARN: invalid group id '%s' in newsgroups '%s'", part, csv)
			}
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// readDB routes messageID to its database pool and table
func (h *History) readDB(messageID string) (*sql.DB, string, error) {
	dbIndex, tableName, err := h.routeHash(messageID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to route hash: %v", err)
	}
	db, err := h.db.GetShardedDB(dbIndex, false)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get database connection: %v", err)
	}
	return db, tableName, nil
}

// Add queues all NewsgroupIDs of msgIdItem.
//
// Deprecated: removed after wave 2 integration. Use AddArticle.
func (h *History) Add(msgIdItem *MessageIdItem) bool {
	if msgIdItem == nil {
		return false
	}
	if h == nil || !h.enabled {
		msgIdItem.Mux.Lock()
		msgIdItem.Response = CaseDupes
		msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
		msgIdItem.Mux.Unlock()
		return false
	}
	msgIdItem.Mux.Lock()
	messageID := msgIdItem.MessageId
	groupIDs := append([]int64(nil), msgIdItem.NewsgroupIDs...)
	msgIdItem.Response = CaseDupes
	msgIdItem.CachedEntryExpires = time.Now().Add(CachedEntryTTL)
	msgIdItem.Mux.Unlock()

	// queue outside the item lock: enqueue may block on backpressure
	queued := false
	for _, groupID := range groupIDs {
		if h.enqueue(opAdd, messageID, groupID) {
			queued = true
		}
	}
	return queued
}

// Lookup returns CaseDupes (+ group IDs) if msgIdItem.MessageId is in the index, CasePass if not, CaseError on error.
//
// Deprecated: removed after wave 2 integration. Use LookupGroups / Exists.
func (h *History) Lookup(msgIdItem *MessageIdItem, quick bool) (response int, newsgroupIDs []int64, err error) {
	if h == nil || !h.enabled {
		return CasePass, nil, nil
	}
	exists, newsgroupIDs, err := h.LookupMID(msgIdItem, quick)
	if err != nil {
		return CaseError, nil, err
	}
	if exists {
		return CaseDupes, newsgroupIDs, nil
	}
	return CasePass, nil, nil
}

// LookupMID checks if msgIdItem.MessageId is in the index. When !quick it stores the group IDs in msgIdItem.NewsgroupIDs.
//
// Deprecated: removed after wave 2 integration. Use LookupGroups / Exists.
func (h *History) LookupMID(msgIdItem *MessageIdItem, quick bool) (exists bool, newsgroupIDs []int64, err error) {
	if h == nil || !h.enabled {
		return false, nil, nil
	}
	if msgIdItem == nil {
		return false, nil, fmt.Errorf("LookupMID called with nil MessageIdItem")
	}
	msgIdItem.Mux.RLock()
	messageID := msgIdItem.MessageId
	msgIdItem.Mux.RUnlock()

	newsgroupIDs, err = h.LookupGroups(messageID)
	if err != nil {
		log.Printf("[HISTORY] ERROR: Lookup failed for msgId='%s': %v", messageID, err)
		return false, nil, err
	}
	if newsgroupIDs == nil {
		return false, nil, nil
	}
	if !quick {
		itemIDs := make([]int64, len(newsgroupIDs))
		copy(itemIDs, newsgroupIDs)
		msgIdItem.Mux.Lock()
		msgIdItem.NewsgroupIDs = itemIDs
		msgIdItem.Mux.Unlock()
	}
	return true, newsgroupIDs, nil
}

// GetStats returns current statistics
func (h *History) GetStats() HistoryStats {
	if h == nil {
		return HistoryStats{}
	}
	return HistoryStats{
		TotalLookups:   h.stats.lookups.Load(),
		TotalAdds:      h.stats.adds.Load(),
		TotalRemoves:   h.stats.removes.Load(),
		TotalCommitted: h.stats.committed.Load(),
		Flushes:        h.stats.flushes.Load(),
		Pending:        h.pending.Load(),
		Duplicates:     h.stats.duplicates.Load(),
		Errors:         h.stats.errors.Load(),
	}
}

// CheckNoMoreWorkInHistory returns true if no op is waiting to be committed
func (h *History) CheckNoMoreWorkInHistory() bool {
	if h == nil {
		return true
	}
	return h.pending.Load() == 0
}

// SetDatabaseWorkChecker sets the database work checker interface for coordinated shutdown
func (h *History) SetDatabaseWorkChecker(checker DatabaseWorkChecker) {
	if h == nil {
		return
	}
	h.checkerMux.Lock()
	h.dbWorkChecker = checker
	h.checkerMux.Unlock()
}

// noMoreDBWork asks the database batch system (if set) whether it is idle. Called without holding any lock.
func (h *History) noMoreDBWork() bool {
	h.checkerMux.RLock()
	checker := h.dbWorkChecker
	h.checkerMux.RUnlock()
	return checker == nil || checker.CheckNoMoreWorkInMaps()
}

// Close stops accepting ops, waits for the writer's final flush and closes the databases.
// Idempotent; concurrent callers all return after the final flush.
func (h *History) Close() error {
	if h == nil {
		return nil
	}
	var err error
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		close(h.closeChan)
		if !h.enabled {
			return
		}
		log.Printf("[HISTORY] Closing down history system (pending=%d)...", h.pending.Load())
		if h.writerAlive {
			<-h.writerDone
		}
		h.dbClosed.Store(true)
		if h.db != nil {
			err = h.db.Close()
		}
		log.Printf("[HISTORY] History system closed")
	})
	return err
}

// writerWorker collects ops and flushes them every BatchTimeout or when BatchSize is reached.
// Exits when Close() was called, nothing is pending and the database batch system is idle.
func (h *History) writerWorker() {
	defer func() {
		h.checkpointAll()
		close(h.writerDone)
		if h.mainWG != nil {
			h.mainWG.Done()
		}
		log.Printf("[HISTORY] History writer worker stopped")
	}()
	log.Printf("[HISTORY] History writer worker started (BatchSize=%d, BatchTimeout=%d ms)", h.config.BatchSize, h.config.BatchTimeout)

	ticker := time.NewTicker(time.Duration(h.config.BatchTimeout) * time.Millisecond)
	defer ticker.Stop()

	batch := make([]historyOp, 0, h.config.BatchSize)
	closeChan := h.closeChan
	lastWaitLog := time.Now()
	for {
		select {
		case op := <-h.opChan:
			batch = append(batch, op)
			if len(batch) >= h.config.BatchSize {
				h.flushBatch(batch)
				batch = batch[:0]
			}
			continue

		case <-closeChan:
			closeChan = nil // fire once
			if time.Duration(h.config.BatchTimeout)*time.Millisecond > shutdownPollInterval {
				ticker.Reset(shutdownPollInterval)
			}

		case <-ticker.C:
		}

		if len(batch) > 0 {
			h.flushBatch(batch)
			batch = batch[:0]
		}

		// check closed BEFORE pending (see enqueue)
		if !h.closed.Load() || h.pending.Load() != 0 {
			continue
		}
		if !h.noMoreDBWork() {
			if time.Since(lastWaitLog) >= 10*time.Second {
				log.Printf("[HISTORY] writer shutdown: waiting for database batch system to finish")
				lastWaitLog = time.Now()
			}
			continue
		}
		if h.pending.Load() != 0 {
			continue // the batch system queued more work meanwhile
		}
		return
	}
}

// checkpointAll truncates the WAL of every database file
func (h *History) checkpointAll() {
	if h.db == nil {
		return
	}
	for i, pool := range h.db.DBPools {
		if pool == nil || pool.DB == nil {
			continue
		}
		if _, err := pool.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			log.Printf("[HISTORY] WARN: wal_checkpoint(TRUNCATE) failed for db %x: %v", i, err)
		}
	}
}

// flushBatch writes batch grouped by database file (op order kept within a file), files in parallel
func (h *History) flushBatch(batch []historyOp) {
	n := len(batch)
	if n == 0 {
		return
	}
	start := time.Now()
	var perFile [historyNumDBs][]routedOp
	for _, op := range batch {
		dbIndex, tableName, err := h.routeHash(op.messageID)
		if err != nil {
			log.Printf("[HISTORY] ERROR: dropping op for messageID='%s': %v", op.messageID, err)
			h.stats.errors.Add(1)
			continue
		}
		perFile[dbIndex] = append(perFile[dbIndex], routedOp{historyOp: op, tableName: tableName})
	}

	var wg sync.WaitGroup
	for dbIndex := range perFile {
		ops := perFile[dbIndex]
		if len(ops) == 0 {
			continue
		}
		wg.Add(1)
		go func(dbIdx int, ops []routedOp) {
			defer wg.Done()
			if err := h.executeDBTransaction(dbIdx, ops); err != nil {
				log.Printf("[HISTORY] ERROR: dropping %d ops for database %x: %v", len(ops), dbIdx, err)
				h.stats.errors.Add(1)
				return
			}
			h.stats.committed.Add(int64(len(ops)))
		}(dbIndex, ops)
	}
	wg.Wait()

	h.stats.flushes.Add(1)
	h.pending.Add(-int64(n))
	if HistoryDEBUG {
		log.Printf("[HISTORY] flushBatch: %d ops took %v (pending=%d)", n, time.Since(start), h.pending.Load())
	}
}

// isBusyError reports SQLITE_BUSY / SQLITE_LOCKED
func isBusyError(err error) bool {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "database is locked") || strings.Contains(errStr, "busy")
}

// beginTx starts a write transaction (_txlock=immediate: lock contention surfaces here).
// Retries busy/locked errors with capped backoff, exits the process after beginMaxRetries.
func (h *History) beginTx(dbIndex int, db *sql.DB) (*sql.Tx, error) {
	delay := beginRetryBaseDelay
	for attempt := 1; ; attempt++ {
		tx, err := db.Begin()
		if err == nil {
			return tx, nil
		}
		if !isBusyError(err) {
			return nil, err
		}
		if attempt >= beginMaxRetries {
			log.Fatalf("[HISTORY] FATAL: failed to begin transaction on database %x after %d attempts: %v", dbIndex, attempt, err)
		}
		if attempt%10 == 0 {
			log.Printf("[HISTORY] WARN: database %x busy, begin attempt %d/%d: %v", dbIndex, attempt, beginMaxRetries, err)
		}
		time.Sleep(delay)
		delay *= 2
		if delay > beginRetryMaxDelay {
			delay = beginRetryMaxDelay
		}
	}
}

// executeDBTransaction writes all ops of one database file in one transaction
func (h *History) executeDBTransaction(dbIndex int, ops []routedOp) error {
	start := time.Now()
	db, err := h.db.GetShardedDB(dbIndex, true)
	if err != nil {
		return fmt.Errorf("failed to get database connection: %v", err)
	}

	tx, err := h.beginTx(dbIndex, db)
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

	// prepared statements per table + query, valid for this transaction
	stmts := make(map[string]*sql.Stmt)
	getStmt := func(query string, tableName string) (*sql.Stmt, error) {
		key := query + tableName
		if stmt, ok := stmts[key]; ok {
			return stmt, nil
		}
		stmt, perr := tx.Prepare(fmt.Sprintf(query, tableName))
		if perr != nil {
			return nil, perr
		}
		stmts[key] = stmt
		return stmt, nil
	}

	var stmt *sql.Stmt
	for _, rop := range ops {
		groupID := strconv.FormatInt(rop.groupID, 10)
		switch rop.op {
		case opAdd:
			if stmt, err = getStmt(sqlAddArticle, rop.tableName); err != nil {
				err = fmt.Errorf("prepare add %s: %v", rop.tableName, err)
				return err
			}
			if _, err = stmt.Exec(rop.messageID, groupID); err != nil {
				err = fmt.Errorf("add messageID='%s' group=%s table=%s: %v", rop.messageID, groupID, rop.tableName, err)
				return err
			}

		case opRemove:
			if stmt, err = getStmt(sqlRemoveArticle, rop.tableName); err != nil {
				err = fmt.Errorf("prepare remove %s: %v", rop.tableName, err)
				return err
			}
			if _, err = stmt.Exec(groupID, rop.messageID); err != nil {
				err = fmt.Errorf("remove messageID='%s' group=%s table=%s: %v", rop.messageID, groupID, rop.tableName, err)
				return err
			}
			if stmt, err = getStmt(sqlDeleteEmpty, rop.tableName); err != nil {
				err = fmt.Errorf("prepare delete %s: %v", rop.tableName, err)
				return err
			}
			if _, err = stmt.Exec(rop.messageID); err != nil {
				err = fmt.Errorf("delete empty messageID='%s' table=%s: %v", rop.messageID, rop.tableName, err)
				return err
			}

		default:
			err = fmt.Errorf("unknown history op %d", rop.op)
			return err
		}
	}

	// Commit the transaction (closes the prepared statements)
	if err = tx.Commit(); err != nil {
		err = fmt.Errorf("failed to commit transaction: %v", err)
		return err
	}
	if HistoryDEBUG {
		log.Printf("[HISTORY] executeDBTransaction: dbIndex=%d, committed %d ops in %v", dbIndex, len(ops), time.Since(start))
	}
	return nil
}

// routeHash routes a message-ID to the correct database and table
// 1st md5 hex char -> database index (0-f), 2nd+3rd chars -> table name (_00-_ff)
func (h *History) routeHash(messageID string) (dbIndex int, tableName string, err error) {
	hash := ComputeMessageIDHash(messageID)[:3]

	dbIndex, err = hexToInt(hash[0:1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid hex char for database: %s", hash[0:1])
	}
	if dbIndex >= historyNumDBs {
		return 0, "", fmt.Errorf("database index %d exceeds available databases %d", dbIndex, historyNumDBs)
	}
	return dbIndex, "_" + hash[1:3], nil
}
