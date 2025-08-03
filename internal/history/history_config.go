package history

import (
	"bufio"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
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

// ThreadingInfo holds per-group threading information for a message
type ThreadingInfo struct {
	RootArticle  int64 // Thread root article number (0 if this IS the root)
	ChildArticle int64 // Child article reference (for threading hierarchy)
	IsThreadRoot bool  // True if this article is a thread root
	ArtNum       int64 // Article number in this specific group
}

type MessageIdItem struct {
	Mux                sync.RWMutex // Protects all fields below
	CachedEntryExpires time.Time    // Exported field for cache entry expiration
	MessageIdHash      string       // Computed hash of the message-ID
	StorageToken       string       // pointer to storage token
	MessageId          string       // pointer to article.messageid
	ArtNum             int64        // Article number in the history (primary/first occurrence)
	GroupName          *string      // Group name this article belongs to (primary group)
	Arrival            int64        // When article arrived
	Response           int          // @AI IGNORE Response``` FOR THE MOMENT; RESPONSE LOGIC NEEDS PROPER THINKING! NOT A JOB FOR NOW!
	FileOffset         int64        // File offset in history.dat where this entry is stored

	// Group-specific threading information (replaces global threading fields)
	GroupThreading map[*string]*ThreadingInfo // Per-group threading info for crossposted articles
	//TmpExpires     time.Time                  // Expiration time for temporary cache functionality
}

// DatabaseWorkChecker interface allows History to check if database batch system has pending work
type DatabaseWorkChecker interface {
	CheckNoMoreWorkInMaps() bool
}

// History manages message-ID history tracking using INN2-style architecture
type History struct {
	config          *HistoryConfig
	mux             sync.RWMutex
	historyFile     *os.File
	HistoryFilePath string
	offset          int64

	// Database backend (SQLite with sharding)
	db SQLite3ShardedPool

	// L1 cache for recent lookups
	//l1Cache *L1CACHE

	// Channels for async operations
	lookupChan chan *MessageIdItem
	writerChan chan *MessageIdItem
	dbChan     chan *MessageIdItem

	// Shutdown signaling (similar to db_batch.go pattern)
	stopChan chan struct{}
	tickChan chan struct{}
	// Batching for high-throughput writes
	//pendingBatch []*MessageIdItem
	batchMux sync.RWMutex
	//flushMux   sync.Mutex // Prevents concurrent flush operations
	lastFlush  time.Time
	processing bool

	// Wait group for graceful shutdown (passed from main application)
	mainWG *sync.WaitGroup

	// Database work checker interface for coordinated shutdown
	dbWorkChecker DatabaseWorkChecker

	// Buffered writer for efficient file operations
	fileWriter *bufio.Writer

	// Statistics
	stats *HistoryStats
}

// HistoryEntry represents an entry in the history system
// HistoryStats tracks statistics for the history system
type HistoryStats struct {
	mux              sync.RWMutex
	TotalLookups     int64
	TotalFileLookups int64
	TotalAdds        int64
	CacheHits        int64
	CacheMisses      int64
	Duplicates       int64
	Errors           int64
}

// HistoryConfig holds configuration for the history system
type HistoryConfig struct {
	HistoryDir      string `yaml:"history_dir" json:"history_dir"`
	CacheExpires    int64  `yaml:"cache_expires" json:"cache_expires"`
	CachePurge      int64  `yaml:"cache_purge" json:"cache_purge"`
	ShardMode       int    `yaml:"shard_mode" json:"shard_mode"`
	MaxConnections  int    `yaml:"max_connections" json:"max_connections"`
	UseShortHashLen int    `yaml:"use_short_hash_len" json:"use_short_hash_len"` // 2-7 chars stored in DB (default 3)

	// Batching configuration for high-throughput writes
	BatchSize    int   `yaml:"batch_size" json:"batch_size"`       // Number of entries to batch (default 200)
	BatchTimeout int64 `yaml:"batch_timeout" json:"batch_timeout"` // Timeout in milliseconds for forced flush (default 5000)
	//WriterChanSize int   `yaml:"writer_chan_size" json:"writer_chan_size"` // Writer channel buffer size (default 65535)
}

// DefaultConfig returns a default history configuration
func DefaultConfig() *HistoryConfig {
	return &HistoryConfig{
		HistoryDir:      DefaultHistoryDir,
		CacheExpires:    DefaultCacheExpires,
		CachePurge:      DefaultCachePurge,
		ShardMode:       SHARD_16_256, // 16 databases with 256 tables
		MaxConnections:  32,           //
		UseShortHashLen: 7,            // 3+7 = 10 bits of entropy

		// Batching configuration for high throughput
		BatchSize:    DefaultBatchSize,
		BatchTimeout: DefaultBatchTimeout,
		//WriterChanSize: DefaultWriterChanSize,
	}
}

// ValidateConfig validates and adjusts configuration values
func (c *HistoryConfig) ValidateConfig() error {
	if c.UseShortHashLen < 2 {
		log.Printf("WARN: UseShortHashLen %d too small, adjusting to 2", c.UseShortHashLen)
		c.UseShortHashLen = 2
	}
	if c.UseShortHashLen > 7 {
		log.Printf("WARN: UseShortHashLen %d too large, adjusting to 7", c.UseShortHashLen)
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

	if c.MaxConnections <= 0 {
		c.MaxConnections = 8
	}

	// Validate batching configuration
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.BatchSize > 10000 { // Reasonable upper limit
		log.Printf("WARN: BatchSize %d very large, consider reducing for memory usage", c.BatchSize)
	}

	if c.BatchTimeout <= 0 {
		c.BatchTimeout = DefaultBatchTimeout
	}
	/*
		if c.WriterChanSize <= 0 {
			c.WriterChanSize = DefaultWriterChanSize
		}
	*/
	return nil
}

// applyPerformanceSettings applies SQLite performance optimizations via PRAGMA
// Following the same pattern as group databases to avoid locking issues
func applyPerformanceSettings(db *sql.DB, mode int) error {
	// Critical: Apply WAL mode FIRST to eliminate all locking issues
	criticalPragmas := []string{
		"PRAGMA journal_mode = WAL",    // MUST be first - eliminates read/write locks
		"PRAGMA locking_mode = NORMAL", // Ensure normal locking (not EXCLUSIVE)
		"PRAGMA synchronous = OFF",     // performance
		"PRAGMA busy_timeout = 30000",  // 30s busy timeout BEFORE other operations
	}

	// Apply critical settings first - use simple Exec() like group databases
	for _, pragma := range criticalPragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("ERROR: Critical PRAGMA failed: %s - %v", pragma, err)
			return fmt.Errorf("critical PRAGMA failed: %s - %v", pragma, err)
		}
		//log.Printf("INFO: Applied %s", pragma)
	}

	// Apply remaining performance optimizations
	pragmas := []string{
		"PRAGMA cache_size = -16000", // MB cache = /-1000
		"PRAGMA temp_store = MEMORY", // Temp tables/indices in RAM
		"PRAGMA mmap_size = 16777216",
		"PRAGMA page_size = 4096",          // 4KB page size (default)
		"PRAGMA wal_autocheckpoint = 2000", // Checkpoint every N pages
		//"PRAGMA foreign_keys = OFF",        // Disable FK checks (we don't use them)
		"PRAGMA auto_vacuum = INCREMENTAL", // Incremental vacuum for space reclaim
		"PRAGMA wal_checkpoint(TRUNCATE)",  // Clean WAL on startup
		//"PRAGMA analysis_limit = 1000",     // Limit ANALYZE for faster startup
		//"PRAGMA optimize",                  // Run query planner optimizations
	}

	// Apply cache size based on shard mode
	cacheSize := getAdaptiveCacheSize(mode)
	pragmas = append(pragmas, fmt.Sprintf("PRAGMA cache_size = %d", cacheSize))

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("WARN: Failed to execute %s: %v", pragma, err)
			// Continue with other pragmas even if one fails
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
	return info.IsDir()
}

func mkdir(path string) bool {
	err := os.MkdirAll(path, 0755)
	return err == nil
}

// getAdaptiveCacheSize returns optimal cache_size for each sharding mode
func getAdaptiveCacheSize(mode int) int {
	switch mode {
	case SHARD_16_256:
		return 2000 // ~8MB per DB, total ~128MB
	default:
		log.Fatalf("ERROR: Unsupported shard mode %d", mode)
	}
	return 0 // Unreachable, but keeps compiler happy
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

/*
// GetHashPrefix returns the configured length prefix of a hash
func (h *History) xxxGetHashPrefix(hash string) string {
	if len(hash) < h.config.HashPrefixLen {
		return hash
	}
	return hash[:h.config.HashPrefixLen]
}
*/

// initDatabase initializes the database backend with sharding
func (h *History) initDatabase() error {
	// Use sharded database implementation
	config := &ShardConfig{
		Mode:         h.config.ShardMode,
		BaseDir:      h.config.HistoryDir,
		MaxOpenPerDB: h.config.MaxConnections,
		//Timeout:      30,
	}

	shardedDB, err := NewSQLite3ShardedDB(config, true, h.config.UseShortHashLen)
	if err != nil {
		return fmt.Errorf("failed to initialize SQLite3 sharded system: %v", err)
	}

	h.db = shardedDB
	numDBs, tablesPerDB, description := GetShardConfig(h.config.ShardMode)
	log.Printf("SQLite3 sharded system initialized: %s (%d DBs, %d tables per DB)",
		description, numDBs, tablesPerDB)
	return nil
}

// openHistoryFile opens or creates the history.dat file
func (h *History) openHistoryFile() error {
	h.HistoryFilePath = filepath.Join(h.config.HistoryDir, HistoryFileName)

	var err error
	h.historyFile, err = os.OpenFile(h.HistoryFilePath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open history file %s: %v", h.HistoryFilePath, err)
	}
	// Initialize buffered writer for efficient file operations
	h.fileWriter = bufio.NewWriterSize(h.historyFile, 1024*1024)

	// Get current file offset
	h.offset, err = h.historyFile.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("failed to seek to end of history file: %v", err)
	}

	log.Printf("History file opened: %s (offset: %d)", h.HistoryFilePath, h.offset)
	return nil
}
