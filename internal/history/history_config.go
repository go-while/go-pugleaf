package history

import (
	"context"
	"crypto/md5"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-sqlite3"
)

const (
	CaseLock  = 0xFF // internal cache state. reply with CaseRetry while CaseLock
	CasePass  = 0xF1 // is a reply to L1Lock and IndexQuery
	CaseDupes = 0x1C // is a reply and cache state
	CaseRetry = 0x2C // is a reply to if CaseLock or CaseWrite or if history.dat returns EOF
	//CaseAdded = 0x3C // is a reply to WriterChan:responseChan
	CaseWrite = 0x4C // internal cache state. is not a reply. reply with CaseRetry while CaseWrite is happening
	CaseError = 0xE1 // some things drop this error
)

const (
	StorageTypeSQLite3 = 0x17 // Storage type for SQLite3
	//StorageTypeMariaDB = 0x23 // Storage type for MariaDB
)

const DefaultStorageType = StorageTypeSQLite3 // Default storage type for history (no other available, maybe in the future!)

// SQLite3Opts holds configuration for SQLite3 database
type SQLite3Opts struct {
	dbPath   string
	params   string
	maxOpen  int
	initOpen int
	timeout  int64
	// mmapBytes is the PRAGMA mmap_size of every connection (0 = historyDefaultMmapBytes)
	mmapBytes int64
}

type MessageIdItem struct {
	Mux                sync.RWMutex // Protects all fields below
	CachedEntryExpires time.Time    // Exported field for cache entry expiration
	MessageId          string       // pointer to article.messageid
	Response           int
}

// DatabaseWorkChecker interface allows History to check if database batch system has pending work
type DatabaseWorkChecker interface {
	CheckNoMoreWorkInMaps() bool
}

// History manages the global message-ID history index:
// message-id -> comma-separated main-DB newsgroup IDs where the article is stored.
// Group DBs are the source of truth, the index is idempotent and rebuildable.
type History struct {
	config  *HistoryConfig
	enabled bool // copied once from ENABLE_HISTORY in NewHistory

	// Database backend (SQLite with sharding)
	db *SQLite3ShardedDB

	// One write connection per database file with its prepared statements, kept across
	// flushes (see dbWriter). Used only by executeDBTransaction, which runs one goroutine per
	// file and never two for the same file; Close closes them after the writer's final flush.
	writers [historyNumDBs]*dbWriter

	// Writer queue: small value ops, no shared pointers
	opChan  chan historyOp
	pending atomic.Int64 // ops counted but not yet committed (queued, in channel or in current batch)
	// Optional importer budget, initialized before any operation is submitted.
	admission    chan struct{}
	admissionErr atomic.Pointer[error]

	// Shutdown signaling
	closed      atomic.Bool   // set by Close(): no new ops accepted
	dbClosed    atomic.Bool   // set right before the DB pools get closed: no more reads
	closeChan   chan struct{} // closed by Close() to wake up the writer
	closeOnce   sync.Once
	writerDone  chan struct{} // closed by the writer after its final flush + checkpoint
	writerAlive bool          // writer goroutine was started (immutable after NewHistory)

	// Wait group for graceful shutdown (passed from main application, may be nil)
	mainWG *sync.WaitGroup

	// Database work checker interface for coordinated shutdown
	checkerMux    sync.RWMutex
	dbWorkChecker DatabaseWorkChecker

	// Log throttling
	invalidOps   atomic.Int64
	closedWarned atomic.Bool

	// Remembered rejects (plan nntp-peering): the retention in days read through the function
	// the owner wires (nil = nothing is pruned automatically) and the clock of the reject days
	// (nil = time.Now, replaced by tests). The prune state, its advisory lock and pruneDone
	// belong to the prune goroutine alone (pruneWorker); Close waits for pruneDone before it
	// touches them.
	rejectDaysFn atomic.Pointer[func() int]
	clock        atomic.Pointer[func() time.Time]
	prune        rejectPruneState
	pruneLock    *os.File      // advisory flock of the prune owner (nil: not held)
	pruneDone    chan struct{} // closed by the prune goroutine when it stopped

	// Statistics
	stats historyCounters
}

// historyCounters holds the live statistics counters
type historyCounters struct {
	lookups    atomic.Int64
	adds       atomic.Int64
	removes    atomic.Int64
	rejects    atomic.Int64 // AddReject ops queued
	pruned     atomic.Int64 // expired rejects deleted
	duplicates atomic.Int64
	errors     atomic.Int64
	flushes    atomic.Int64
	committed  atomic.Int64
}

// HistoryStats is a point-in-time snapshot of the history statistics
type HistoryStats struct {
	TotalLookups     int64
	TotalFileLookups int64 // legacy, always 0
	TotalAdds        int64 // AddArticle ops queued
	TotalRemoves     int64 // RemoveArticle ops queued
	TotalRejects     int64 // AddReject ops queued
	RejectsPruned    int64 // expired rejects deleted
	TotalCommitted   int64 // ops written to the index
	Flushes          int64
	Pending          int64
	CacheHits        int64 // legacy, always 0
	CacheMisses      int64 // legacy, always 0
	Duplicates       int64 // lookups that found an existing row
	Errors           int64
}

// HistoryConfig holds configuration for the history system
type HistoryConfig struct {
	HistoryDir      string `yaml:"history_dir" json:"history_dir"`
	CacheExpires    int64  `yaml:"cache_expires" json:"cache_expires"`
	CachePurge      int64  `yaml:"cache_purge" json:"cache_purge"`
	ShardMode       int    `yaml:"shard_mode" json:"shard_mode"`
	MaxConnections  int    `yaml:"max_connections" json:"max_connections"`
	UseShortHashLen int    `yaml:"use_short_hash_len" json:"use_short_hash_len"` // legacy, unused by the message_id schema

	// Batching configuration for high-throughput writes
	BatchSize    int   `yaml:"batch_size" json:"batch_size"`       // Number of ops per flush (default DefaultBatchSize)
	BatchTimeout int64 `yaml:"batch_timeout" json:"batch_timeout"` // Milliseconds between forced flushes (default DefaultBatchTimeout)

	// SQLite memory settings, applied on every connection of every database file (plan
	// history-cache-knobs). 0 = the default; ValidateConfig clamps to MaxPageCacheMB / MaxMmapMB.
	// The page cache is private to each connection (up to MaxConnections per file x 16 files,
	// so the total can reach that many times the value; cache_size is a soft limit). The mmap
	// is the setting that shares memory: mapped pages are the kernel's page cache pages of the
	// file, shared by all connections and processes. On ZFS those pages sit in the Linux page
	// cache on top of the ARC (the data is held twice), so there the ARC size matters more and
	// a large mmap may bring little. A mapping also turns a disk I/O error on a mapped page, or a
	// history file truncated or replaced underneath a running process, into SIGBUS instead of an
	// error return (SQLite behaviour).
	PageCacheMB int `yaml:"page_cache_mb" json:"page_cache_mb"` // page cache per connection in MiB (0 = 8000 KiB, _cache_size=-8000)
	MmapMB      int `yaml:"mmap_mb" json:"mmap_mb"`             // PRAGMA mmap_size per connection in MiB (0 = 16 MiB)
}

const (
	// historyDefaultCacheKiB is the page cache per connection when PageCacheMB is 0: the
	// fixed _cache_size=-8000 (KiB) of the history DSN before PageCacheMB existed.
	historyDefaultCacheKiB = 8000
	// historyDefaultMmapBytes is the mmap size per connection when MmapMB is 0: the fixed
	// PRAGMA mmap_size = 16777216 before MmapMB existed.
	historyDefaultMmapBytes int64 = 16 << 20

	// MaxPageCacheMB caps PageCacheMB. SQLite fills a page cache only as pages are read, so
	// the cap bounds the worst case: up to MaxConnections connections per file, 16 files.
	// Sensible values are a few tens of MiB; the cap is far above that on purpose.
	MaxPageCacheMB = 4096
	// MaxMmapMB caps MmapMB just below SQLite's own compile-time limit SQLITE_MAX_MMAP_SIZE
	// (0x7fff0000 = 2 GiB - 64 KiB in the bundled go-sqlite3 build), which SQLite applies
	// silently: a larger value would be logged but not used.
	MaxMmapMB = 2047
)

// pageCacheKiB is the effective page cache per connection in KiB (the N of _cache_size=-N)
func (c *HistoryConfig) pageCacheKiB() int {
	if c.PageCacheMB <= 0 {
		return historyDefaultCacheKiB
	}
	return min(c.PageCacheMB, MaxPageCacheMB) * 1024
}

// mmapBytes is the effective PRAGMA mmap_size per connection in bytes
func (c *HistoryConfig) mmapBytes() int64 {
	if c.MmapMB <= 0 {
		return historyDefaultMmapBytes
	}
	return int64(min(c.MmapMB, MaxMmapMB)) << 20
}

// clampMB returns v within 0..limit (0 = the default) and logs a correction
func clampMB(name string, v, limit int) int {
	switch {
	case v < 0:
		log.Printf("[HISTORY] WARN: %s %d is negative, using the default", name, v)
		return 0
	case v > limit:
		log.Printf("[HISTORY] WARN: %s %d is above the cap, using %d", name, v, limit)
		return limit
	}
	return v
}

// DefaultConfig returns a default history configuration
func DefaultConfig() *HistoryConfig {
	return &HistoryConfig{
		HistoryDir:      DefaultHistoryDir,
		CacheExpires:    DefaultCacheExpires,
		CachePurge:      DefaultCachePurge,
		ShardMode:       SHARD_16_256, // 16 databases with 256 tables
		MaxConnections:  32,           //
		UseShortHashLen: 7,            // legacy

		// Batching configuration for high throughput
		BatchSize:    DefaultBatchSize,
		BatchTimeout: DefaultBatchTimeout,
	}
}

// ValidateConfig validates and adjusts configuration values
func (c *HistoryConfig) ValidateConfig() error {
	if c.UseShortHashLen < 2 {
		c.UseShortHashLen = 2
	}
	if c.UseShortHashLen > 7 {
		c.UseShortHashLen = 7
	}

	if c.HistoryDir == "" {
		c.HistoryDir = DefaultHistoryDir
	}

	if c.CacheExpires <= 0 {
		c.CacheExpires = DefaultCacheExpires
	}

	if c.CachePurge <= 0 {
		c.CachePurge = DefaultCachePurge
	}

	if c.ShardMode != SHARD_16_256 {
		if c.ShardMode != 0 {
			log.Printf("[HISTORY] WARN: ShardMode %d unsupported, using SHARD_16_256", c.ShardMode)
		}
		c.ShardMode = SHARD_16_256 // unchangeable !
	}

	if c.MaxConnections <= 0 {
		c.MaxConnections = 8
	}
	if c.MaxConnections < 2 {
		c.MaxConnections = 2 // the writer keeps one connection per file (dbWriter); lookups need another
	}

	// Validate batching configuration
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.BatchSize > 100000 { // Reasonable upper limit
		log.Printf("[HISTORY] WARN: BatchSize %d very large, consider reducing for memory usage", c.BatchSize)
	}

	if c.BatchTimeout <= 0 {
		c.BatchTimeout = DefaultBatchTimeout
	}

	c.PageCacheMB = clampMB("PageCacheMB", c.PageCacheMB, MaxPageCacheMB)
	c.MmapMB = clampMB("MmapMB", c.MmapMB, MaxMmapMB)
	return nil
}

// historyDriverName is a registered database/sql driver for raw opens of a history file
// (tests, tools): the go-sqlite3 driver with the default settings hook. The pools of
// NewSQLite3DB do not open through it; they use a historyConnector with their own settings.
const historyDriverName = "sqlite3_history"

// historyDSNBase are the DSN params of every history connection except the page cache
const historyDSNBase = "?_journal_mode=WAL&_synchronous=OFF&_busy_timeout=30000&_txlock=immediate"

// historyDSNParams are the DSN params go-sqlite3 applies on every new connection, with a page
// cache of cacheKiB KiB (historyDefaultCacheKiB gives the DSN used before PageCacheMB)
func historyDSNParams(cacheKiB int) string {
	return historyDSNBase + "&_cache_size=-" + strconv.Itoa(cacheKiB)
}

var historyDriverOnce sync.Once

// registerHistoryDriver registers the history sqlite3 driver exactly once
func registerHistoryDriver() {
	historyDriverOnce.Do(func() {
		sql.Register(historyDriverName, &sqlite3.SQLiteDriver{
			ConnectHook: performanceHook(historyDefaultMmapBytes),
		})
	})
}

// performanceHook returns the ConnectHook that applies the PRAGMAs that have no DSN parameter,
// with an mmap size of mmapBytes. It runs on every new connection (after the DSN params were
// applied) and reads only its own immutable values, so pools with different settings and the
// connection opener goroutines of database/sql share no mutable state.
func performanceHook(mmapBytes int64) func(*sqlite3.SQLiteConn) error {
	pragmas := []string{
		"PRAGMA temp_store = MEMORY",                             // Temp tables/indices in RAM
		"PRAGMA mmap_size = " + strconv.FormatInt(mmapBytes, 10), // mapped reads use the kernel page cache (see HistoryConfig.MmapMB)
		"PRAGMA wal_autocheckpoint = 2000",                       // Checkpoint every N pages
	}
	return func(conn *sqlite3.SQLiteConn) error {
		for _, pragma := range pragmas {
			if _, err := conn.Exec(pragma, nil); err != nil {
				// best-effort: performance only, never fail the connection
				log.Printf("[HISTORY] WARN: Failed to execute %s: %v", pragma, err)
			}
		}
		return nil
	}
}

// historyConnector opens the connections of one history pool (sql.OpenDB): a go-sqlite3
// driver of its own, never registered, whose ConnectHook carries this pool's mmap size, and
// the DSN with this pool's page cache. Every connection database/sql opens for the pool, at
// any time, gets the same settings; nothing global is set or registered per value.
type historyConnector struct {
	dsn string
	drv *sqlite3.SQLiteDriver
}

func newHistoryConnector(dsn string, mmapBytes int64) *historyConnector {
	return &historyConnector{
		dsn: dsn,
		drv: &sqlite3.SQLiteDriver{ConnectHook: performanceHook(mmapBytes)},
	}
}

// Connect opens a new connection. go-sqlite3 has no context-aware open; database/sql's own
// connector for a driver opened by name ignores the context the same way.
func (c *historyConnector) Connect(context.Context) (driver.Conn, error) {
	return c.drv.Open(c.dsn)
}

// Driver returns the pool's driver
func (c *historyConnector) Driver() driver.Driver {
	return c.drv
}

// Helper functions for directory operations
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return err == nil && info.IsDir()
}

func mkdir(path string) bool {
	err := os.MkdirAll(path, 0755)
	return err == nil
}

// hexToInt converts a hex string to int
func hexToInt(hexStr string) (int, error) {
	val, err := strconv.ParseInt(hexStr, 16, 64)
	return int(val), err
}

// ComputeMessageIDHash computes MD5 hash of a message-ID
func ComputeMessageIDHash(messageID string) string {
	hash := md5.Sum([]byte(messageID))
	return hex.EncodeToString(hash[:])
}

// initDatabase initializes the database backend with sharding
func (h *History) initDatabase() error {
	// Use sharded database implementation
	config := &ShardConfig{
		Mode:         h.config.ShardMode,
		BaseDir:      h.config.HistoryDir,
		MaxOpenPerDB: h.config.MaxConnections,
		CacheKiB:     h.config.pageCacheKiB(),
		MmapBytes:    h.config.mmapBytes(),
	}

	shardedDB, err := NewSQLite3ShardedDB(config, true)
	if err != nil {
		return fmt.Errorf("failed to initialize SQLite3 sharded system: %v", err)
	}

	h.db = shardedDB
	numDBs, tablesPerDB, description := GetShardConfig(h.config.ShardMode)
	log.Printf("[HISTORY] SQLite3 sharded system initialized: %s (%d DBs, %d tables per DB)",
		description, numDBs, tablesPerDB)
	return nil
}
