package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
)

var GroupHashMap *GHmap // Global variable for group hash map

var ENABLE_ARTICLE_CACHE = true
var NNTP_AUTH_CACHE_TIME = 15 * time.Minute
var FETCH_MODE = false // set true in fetcher/main.go

// Database represents the main database connection and per-group database pool
type Database struct {
	//proc *processor.Processor // Reference to the Processor for threading and other operations
	// Main database connection for system data
	mainDB *sql.DB

	// Per-group database connections (cached)
	groupDBs   map[string]*GroupDBs // map with open database pointers
	openDBsNum int                  // Total number of open group databases

	MainMutex sync.RWMutex

	// Database configuration
	dbconfig *DBConfig

	// Caches
	SectionsCache  *GroupSectionDBCache
	MemThreadCache *MemCachedThreads
	ArticleCache   *ArticleCache   // LRU cache for individual articles
	NNTPAuthCache  *NNTPAuthCache  // Authentication cache for NNTP users
	HierarchyCache *HierarchyCache // Fast hierarchy and group browsing cache
	Batch          *SQ3batch       // sqlite3 Batch operations

	WG       *sync.WaitGroup
	StopChan chan struct{} // Channel to signal shutdown
}

// Config represents database configuration
type DBConfig struct {
	// Directory to store database files
	DataDir string

	// Connection pool settings
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration

	// Performance settings
	WALMode   bool   // Write-Ahead Logging
	SyncMode  string // OFF, NORMAL, FULL
	CacheSize int    // KB
	TempStore string // MEMORY, FILE

	// Backup settings
	BackupEnabled  bool
	BackupInterval time.Duration
	BackupDir      string

	// Cache settings
	ArticleCacheSize   int           // Maximum number of cached articles
	ArticleCacheExpiry time.Duration // Cache expiry duration
}

// DefaultDBConfig returns default database configuration
func DefaultDBConfig() (dbconfig *DBConfig) {
	return &DBConfig{
		DataDir:            "./data",
		MaxOpenConns:       100, // Increased from 15 to support high concurrency
		MaxIdleConns:       25,  // Increased from 5 to keep connections ready
		ConnMaxLifetime:    0,   // Unlimited for SQLite - connections don't need to be recycled
		WALMode:            true,
		SyncMode:           "NORMAL",
		CacheSize:          -16384, // -16384 == 1024 KB * 16384 = 16MB cache
		TempStore:          "MEMORY",
		BackupEnabled:      false,              // TODO pass flag to enable backups
		BackupInterval:     24 * 7 * time.Hour, // weekly backup
		BackupDir:          "./backups",
		ArticleCacheSize:   1000,             // Default cache size
		ArticleCacheExpiry: 15 * time.Minute, // Default cache expiry
	}
}

var GlobalDBMutex sync.Mutex // Mutex to protect database operations
var INIT bool

// New creates a new Database instance
func OpenDatabase(dbconfig *DBConfig) (*Database, error) {
	new := false
	GlobalDBMutex.Lock()
	defer GlobalDBMutex.Unlock()
	if INIT {
		return nil, fmt.Errorf("database already initialized")
	}
	INIT = true
	if dbconfig == nil {
		dbconfig = DefaultDBConfig()
		new = true
	}

	db := &Database{
		dbconfig: dbconfig,
		groupDBs: make(map[string]*GroupDBs),
		WG:       &sync.WaitGroup{}, // Initialize wait group for background tasks

	}

	// Initialize main database
	if err := db.initMainDB(); err != nil {
		return nil, fmt.Errorf("failed to initialize main database: %w", err)
	}

	// Run migrations to ensure all tables exist
	if err := db.Migrate(); err != nil {
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	// Check previous shutdown state and initialize system status
	if wasClean, err := db.CheckPreviousShutdown(); err != nil {
		log.Printf("Warning: Failed to check previous shutdown state: %v", err)
	} else if !wasClean {
		log.Printf("WARNING: Previous shutdown was not clean - database may need recovery")
	}

	// Initialize system status for this startup
	hostname, _ := os.Hostname()
	if err := db.InitializeSystemStatus("go-pugleaf", os.Getpid(), hostname); err != nil {
		log.Printf("Warning: Failed to initialize system status: %v", err)
	}

	if new {
		// Load default providers on first boot
		if err := db.LoadDefaultProviders(); err != nil {
			log.Printf("Failed to add default providers to database: %v", err)
			return nil, fmt.Errorf("failed to sync config providers: %w", err)
		}
	}
	db.StopChan = make(chan struct{}, 1) // Channel to signal shutdown (will get closed)
	log.Printf("pugLeaf DB init config: %+v FETCH_MODE=%t", dbconfig, FETCH_MODE)
	if !FETCH_MODE {
		db.SectionsCache = NewGroupSectionDBCache()
		db.MemThreadCache = NewMemCachedThreads()
		db.HierarchyCache = NewHierarchyCache() // Initialize hierarchy cache for fast browsing
		// Start hierarchy cache warming in background
		go db.HierarchyCache.WarmCache(db)
	}
	GroupHashMap = NewGHmap()
	db.Batch = NewSQ3batch(db) // Initialize SQ3batch for batch operations

	// Start other DB cron tasks
	go db.CronDB()

	// Start the smart orchestrator that monitors channel thresholds and timers
	go db.Batch.orchestrator.StartOrch() // main wg waitGroup Add(+1)
	// Periodically expire idle per-group batch state (keeps memory bounded during scans)
	go db.Batch.ExpireCache()

	db.NNTPAuthCache = NewNNTPAuthCache(NNTP_AUTH_CACHE_TIME)
	//log.Printf("[WEB]: NNTP authentication cache initialized (15 minute TTL)")

	// Start article cache cleanup routine
	if ENABLE_ARTICLE_CACHE {
		db.ArticleCache = NewArticleCache(db.dbconfig.ArticleCacheSize, db.dbconfig.ArticleCacheExpiry, db)
		log.Printf("Article cache initialized with size %d and expiry %s", db.dbconfig.ArticleCacheSize, db.dbconfig.ArticleCacheExpiry)
	} else {
		log.Println("Article cache is disabled")
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute) // Cleanup every 5 minutes
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if db.ArticleCache != nil {
					db.ArticleCache.Cleanup()
				}
			case <-db.StopChan:
				return
			}
		}
	}()

	log.Printf("Database initialized: %+v", db)
	return db, nil
}

func (db *Database) IsDBshutdown() bool {
	if db == nil {
		return true // If db is nil, consider it shutdown
	}
	select {
	case _, ok := <-db.StopChan:
		if !ok {
			log.Println("[DATABASE] preparing shutdown: StopChan is already closed")
		}
		return true // If StopChan is closed, database is shutdown
	default:
		return false // If StopChan is not closed, database is still running
	}
}

// initMainDB initializes the main database connection
func (db *Database) initMainDB() error {
	dbPath := filepath.Join(db.dbconfig.DataDir, "/cfg/pugleaf.sq3")
	log.Printf("Initializing main database at: %s", dbPath)

	// Create data directory if it doesn't exist
	if err := createDirIfNotExists(db.dbconfig.DataDir + "/cfg/"); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Open main database
	mainDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open main database: %w", err)
	}

	// Configure connection pool
	mainDB.SetMaxOpenConns(db.dbconfig.MaxOpenConns)
	mainDB.SetMaxIdleConns(db.dbconfig.MaxIdleConns)
	mainDB.SetConnMaxLifetime(db.dbconfig.ConnMaxLifetime)

	// Test connection
	if err := mainDB.Ping(); err != nil {
		if cerr := mainDB.Close(); cerr != nil {
			return fmt.Errorf("failed to ping main database: %w; also failed to close mainDB: %v", err, cerr)
		}
		return fmt.Errorf("failed to ping main database: %w", err)
	}

	// Apply SQLite pragmas for performance
	if err := db.applySQLitePragmas(mainDB); err != nil {
		if cerr := mainDB.Close(); cerr != nil {
			return fmt.Errorf("failed to apply SQLite pragmas: %w; also failed to close mainDB: %v", err, cerr)
		}
		return fmt.Errorf("failed to apply SQLite pragmas: %w", err)
	}

	db.mainDB = mainDB
	return nil
}

// applySQLitePragmas applies performance and configuration pragmas to SQLite connection
func (db *Database) applySQLitePragmas(conn *sql.DB) error {
	pragmas := []string{
		fmt.Sprintf("PRAGMA cache_size = %d", db.dbconfig.CacheSize),
		fmt.Sprintf("PRAGMA synchronous = %s", db.dbconfig.SyncMode),
		fmt.Sprintf("PRAGMA temp_store = %s", db.dbconfig.TempStore),
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 30000", // 30 seconds
		"PRAGMA mmap_size = 0",
	}

	if db.dbconfig.WALMode {
		pragmas = append(pragmas, "PRAGMA journal_mode = WAL")
		pragmas = append(pragmas, "PRAGMA wal_autocheckpoint = 1000")
	}

	for _, pragma := range pragmas {
		if _, err := conn.Exec(pragma); err != nil {
			return fmt.Errorf("failed to execute pragma '%s': %w", pragma, err)
		}
	}

	return nil
}

// applySQLitePragmas applies performance and configuration pragmas to SQLite connection
func (db *Database) applySQLitePragmasGroupDB(conn *sql.DB) error {
	pragmas := []string{
		"PRAGMA cache_size = 1000",
		"PRAGMA synchronous = NORMAL",
		//"PRAGMA temp_store = default",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 30000", // 30 seconds
		//"PRAGMA mmap_size = 268435456", // 256MB memory-mapped I/O
	}

	if db.dbconfig.WALMode {
		pragmas = append(pragmas, "PRAGMA journal_mode = WAL")
		pragmas = append(pragmas, "PRAGMA wal_autocheckpoint = 1000")
	}

	for _, pragma := range pragmas {
		if _, err := conn.Exec(pragma); err != nil {
			return fmt.Errorf("failed to execute pragma '%s': %w", pragma, err)
		}
	}

	return nil
}

// sync default config providers to database
func (db *Database) LoadDefaultProviders() error {
	for _, p := range config.DefaultProviders {
		// Check if provider already exists
		existing, err := db.GetProviderByName(p.Name)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to check existing provider %s: %w", p.Name, err)
		}

		// If provider doesn't exist, add it
		if existing == nil {
			provider := &models.Provider{
				Name:       p.Name,
				Enabled:    p.Enabled,
				Priority:   p.Priority,
				Grp:        p.Grp,
				Host:       p.Host,
				Port:       p.Port,
				SSL:        p.SSL,
				Username:   p.Username,
				Password:   p.Password,
				MaxConns:   p.MaxConns,
				MaxArtSize: p.MaxArtSize,
			}

			err = db.AddProvider(provider)
			if err != nil {
				return fmt.Errorf("failed to add provider %s: %w", p.Name, err)
			}

			log.Printf("Added default provider: %s (%s:%d)", provider.Name, provider.Host, provider.Port)
		}
	}

	return nil
}
