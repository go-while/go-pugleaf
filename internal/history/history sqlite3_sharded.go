package history

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"
)

// SQLite3DB represents a SQLite database connection pool
type SQLite3DB struct {
	dbPath   string
	params   string
	maxOpen  int
	initOpen int
	timeout  int64
	DB       *sql.DB
	mux      sync.RWMutex
}

// Close closes the database connection
func (p *SQLite3DB) Close() error {
	p.mux.Lock()
	defer p.mux.Unlock()
	if p.DB != nil {
		return p.DB.Close()
	}
	return nil
}

// SQLite3ShardedDB manages multiple SQLite databases for sharding
type SQLite3ShardedDB struct {
	DBPools     []*SQLite3DB
	shardMode   int
	numDBs      int
	tablesPerDB int
	baseDir     string
	maxOpen     int
	timeout     int64
}

// ShardConfig defines the sharding configuration
type ShardConfig struct {
	Mode         int    // Sharding mode (only SHARD_16_256)
	BaseDir      string // Base directory for database files
	MaxOpenPerDB int    // Max connections per database
	Timeout      int64  // Idle connection timeout in seconds
}

// GetShardConfig returns the configuration for a given shard mode
func GetShardConfig(mode int) (numDBs, tablesPerDB int, description string) {
	return 16, 256, "16 databases with 256 tables each" // unchangeable !
}

// NewSQLite3DB creates a new SQLite3 database pool.
// All connection settings are applied per connection: DSN params (opts.params)
// plus the ConnectHook of the registered history driver.
func NewSQLite3DB(opts *SQLite3Opts) (*SQLite3DB, error) {
	registerHistoryDriver()

	connectionString := opts.dbPath
	if opts.params != "" {
		connectionString += opts.params
	}

	db, err := sql.Open(historyDriverName, connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	db.SetMaxOpenConns(opts.maxOpen)
	db.SetMaxIdleConns(opts.initOpen)
	db.SetConnMaxIdleTime(time.Duration(opts.timeout) * time.Second)

	// Test connection (also creates the file and switches it to WAL)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database %s: %v", opts.dbPath, err)
	}

	return &SQLite3DB{
		dbPath:   opts.dbPath,
		params:   opts.params,
		maxOpen:  opts.maxOpen,
		initOpen: opts.initOpen,
		timeout:  opts.timeout,
		DB:       db,
	}, nil
}

// NewSQLite3ShardedDB creates a new sharded SQLite3 database system
func NewSQLite3ShardedDB(config *ShardConfig, createTables bool) (*SQLite3ShardedDB, error) {
	numDBs, tablesPerDB, description := GetShardConfig(config.Mode)
	log.Printf("[HISTORY] Initializing SQLite3 sharded system: %s (%d databases, %d tables per DB)", description, numDBs, tablesPerDB)

	if config.MaxOpenPerDB <= 0 {
		config.MaxOpenPerDB = 16
	}
	if config.Timeout < 30 {
		config.Timeout = 300
	}

	s := &SQLite3ShardedDB{
		shardMode:   config.Mode,
		numDBs:      numDBs,
		tablesPerDB: tablesPerDB,
		baseDir:     config.BaseDir,
		maxOpen:     config.MaxOpenPerDB,
		timeout:     config.Timeout,
		DBPools:     make([]*SQLite3DB, numDBs),
	}

	// Initialize database pools
	for i := 0; i < numDBs; i++ {
		opts := &SQLite3Opts{
			dbPath:   filepath.Join(config.BaseDir, fmt.Sprintf("hashdb_%x.sqlite3", i)),
			params:   historyDSNParams,
			maxOpen:  config.MaxOpenPerDB,
			initOpen: config.MaxOpenPerDB, // keep connections (and their settings) around
			timeout:  config.Timeout,
		}

		db, err := NewSQLite3DB(opts)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("failed to create database pool %d: %v", i, err)
		}
		s.DBPools[i] = db
	}

	if createTables {
		if err := s.CreateAllTables(); err != nil {
			s.Close()
			return nil, err
		}
	}

	log.Printf("[HISTORY] SQLite3 sharded system opened: %d databases, %d tables per DB createTables=%t",
		numDBs, tablesPerDB, createTables)
	return s, nil
}

// GetShardedDB returns a database connection for a specific shard (implements SQLite3ShardedPool interface)
func (s *SQLite3ShardedDB) GetShardedDB(dbIndex int, write bool) (*sql.DB, error) {
	if dbIndex < 0 || dbIndex >= len(s.DBPools) || s.DBPools[dbIndex] == nil {
		return nil, fmt.Errorf("database index %d out of range (0-%d)", dbIndex, len(s.DBPools)-1)
	}
	return s.DBPools[dbIndex].DB, nil
}

// Close closes all database connections (implements SQLite3ShardedPool interface)
func (s *SQLite3ShardedDB) Close() error {
	var firstErr error
	for _, db := range s.DBPools {
		if db != nil {
			if err := db.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// CreateAllTables creates the tables in every database file that lacks them
func (s *SQLite3ShardedDB) CreateAllTables() error {
	created := 0
	for dbIndex := 0; dbIndex < s.numDBs; dbIndex++ {
		did, err := s.createTablesForDB(dbIndex)
		if err != nil {
			return fmt.Errorf("failed to create tables for database %d: %v", dbIndex, err)
		}
		if did {
			created++
		}
	}
	if created > 0 {
		log.Printf("[HISTORY] Created tables in %d/%d databases", created, s.numDBs)
	}
	return nil
}

// createTablesForDB creates the 256 tables of one database file inside one transaction,
// but only if the file does not already have all of them.
func (s *SQLite3ShardedDB) createTablesForDB(dbIndex int) (created bool, err error) {
	db := s.DBPools[dbIndex].DB
	if db == nil {
		return false, fmt.Errorf("database connection is nil")
	}

	var count int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name GLOB '_[0-9a-f][0-9a-f]'`).Scan(&count); err != nil {
		return false, fmt.Errorf("failed to count tables: %v", err)
	}
	if count >= s.tablesPerDB {
		return false, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return false, fmt.Errorf("failed to begin create tables transaction: %v", err)
	}
	for _, tableName := range s.getTableNamesForDB() {
		query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				message_id TEXT NOT NULL PRIMARY KEY,
				newsgroups TEXT
			) WITHOUT ROWID;`, tableName)
		if _, err = tx.Exec(query); err != nil {
			tx.Rollback()
			return false, fmt.Errorf("failed to create table %s: %v", tableName, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit create tables transaction: %v", err)
	}
	return true, nil
}

// getTableNamesForDB returns all table names for a specific database
func (s *SQLite3ShardedDB) getTableNamesForDB() []string {
	tables := make([]string, 0, s.tablesPerDB)
	for i := 0; i < s.tablesPerDB; i++ {
		tables = append(tables, fmt.Sprintf("_%02x", i))
	}
	return tables
}
