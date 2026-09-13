package history

import (
	"crypto/md5"
	"database/sql"
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
}

type MessageIdItem struct {
	Mux                sync.RWMutex // Protects all fields below
	CachedEntryExpires time.Time    // Exported field for cache entry expiration
	MessageId          string       // pointer to article.messageid
	MessageIdHash      string       // Computed hash of the message-ID
	NewsgroupIDs       []int64      // Newsgroup IDs this message-ID belongs to
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

	// Writer queue: small value ops, no shared pointers
	opChan  chan historyOp
	pending atomic.Int64 // ops counted but not yet committed (queued, in channel or in current batch)

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

	// Statistics
	stats historyCounters
}

// historyCounters holds the live statistics counters
type historyCounters struct {
	lookups    atomic.Int64
	adds       atomic.Int64
	removes    atomic.Int64
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
	return nil
}

// historyDriverName is the database/sql driver used for the history DBs.
// It is the regular go-sqlite3 driver plus a ConnectHook, so every pooled
// connection gets the same settings (a plain db.Exec("PRAGMA ...") only reaches one connection).
const historyDriverName = "sqlite3_history"

// historyDSNParams are applied by go-sqlite3 on every new connection
const historyDSNParams = "?_journal_mode=WAL&_synchronous=OFF&_busy_timeout=30000&_txlock=immediate&_cache_size=-8000"

var historyDriverOnce sync.Once

// registerHistoryDriver registers the history sqlite3 driver exactly once
func registerHistoryDriver() {
	historyDriverOnce.Do(func() {
		sql.Register(historyDriverName, &sqlite3.SQLiteDriver{
			ConnectHook: applyPerformanceSettings,
		})
	})
}

// applyPerformanceSettings applies the PRAGMAs that have no DSN parameter.
// Runs as ConnectHook on every new connection (after the DSN params were applied).
func applyPerformanceSettings(conn *sqlite3.SQLiteConn) error {
	pragmas := []string{
		"PRAGMA temp_store = MEMORY",       // Temp tables/indices in RAM
		"PRAGMA mmap_size = 16777216",      // 16MB mmap
		"PRAGMA wal_autocheckpoint = 2000", // Checkpoint every N pages
	}
	for _, pragma := range pragmas {
		if _, err := conn.Exec(pragma, nil); err != nil {
			// best-effort: performance only, never fail the connection
			log.Printf("[HISTORY] WARN: Failed to execute %s: %v", pragma, err)
		}
	}
	return nil
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
