package history

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
)

// SQLite3ShardedDB manages multiple SQLite databases for sharding
type SQLite3ShardedDB struct {
	DBPools     []*SQLite3DB
	shardMode   int
	numDBs      int
	tablesPerDB int
	baseDir     string
	maxOpen     int
	timeout     int64
	//mux         sync.RWMutex // Mutex for thread-safe access
}

// ShardConfig defines the sharding configuration
type ShardConfig struct {
	Mode         int    // Sharding mode (0-5)
	BaseDir      string // Base directory for database files
	MaxOpenPerDB int    // Max connections per database
	Timeout      int64  // Connection timeout
}

// GetShardConfig returns the configuration for a given shard mode
func GetShardConfig(mode int) (numDBs, tablesPerDB int, description string) {
	return 16, 256, "16 databases with 256 tables each" // unchangeable !
}

// NewSQLite3ShardedDB creates a new sharded SQLite3 database system
func NewSQLite3ShardedDB(config *ShardConfig, createTables bool, useShortHashLen int) (*SQLite3ShardedDB, error) {
	numDBs, tablesPerDB, description := GetShardConfig(config.Mode)
	log.Printf("Initializing SQLite3 sharded system: %s (%d databases, %d tables per DB)", description, numDBs, tablesPerDB)

	if config.MaxOpenPerDB <= 0 {
		config.MaxOpenPerDB = 16
	}
	if config.Timeout < 30 {
		config.Timeout = 30
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
		log.Printf("Initializing database pool %d/%d...", i+1, numDBs)
		var dbPath string
		switch numDBs {
		case 1:
			dbPath = filepath.Join(config.BaseDir, "hashdb.sqlite3")
		default:
			dbPath = filepath.Join(config.BaseDir, fmt.Sprintf("hashdb_%x.sqlite3", i))
		}

		opts := &SQLite3Opts{
			dbPath:   dbPath,
			maxOpen:  config.MaxOpenPerDB,
			initOpen: 1,
			timeout:  config.Timeout,
		}

		db, err := NewSQLite3DB(opts, false, useShortHashLen, config.Mode) // Don't create tables yet
		if err != nil {
			return nil, fmt.Errorf("failed to create database pool %d: %v", i, err)
		}
		s.DBPools[i] = db
		log.Printf("Database pool %d/%d initialized successfully", i+1, numDBs)
	}

	if createTables {
		if err := s.CreateAllTables(useShortHashLen); err != nil {
			return nil, err
		}
	}

	log.Printf("SQLite3 sharded system initialized successfully: %d databases, %d tables per DB createTables=%t",
		numDBs, tablesPerDB, createTables)
	return s, nil
}

// GetShardedDB returns a database connection for a specific shard (implements SQLite3ShardedPool interface)
func (s *SQLite3ShardedDB) GetShardedDB(dbIndex int, write bool) (*sql.DB, error) {
	if dbIndex < 0 || dbIndex >= len(s.DBPools) {
		return nil, fmt.Errorf("database index %d out of range (0-%d)", dbIndex, len(s.DBPools)-1)
	}
	return s.DBPools[dbIndex].DB, nil
}

// Close closes all database connections (implements both SQLite3Pool and SQLite3ShardedPool interfaces)
func (s *SQLite3ShardedDB) Close() error {
	for _, db := range s.DBPools {
		if db != nil {
			db.Close()
		}
	}
	return nil
}

// CreateAllTables creates all required tables across all databases
func (s *SQLite3ShardedDB) CreateAllTables(useShortHashLen int) error {
	log.Printf("Creating tables for sharding mode %d (%d databases, %d tables per DB)",
		s.shardMode, s.numDBs, s.tablesPerDB)

	for dbIndex := 0; dbIndex < s.numDBs; dbIndex++ {
		if err := s.createTablesForDB(dbIndex, useShortHashLen); err != nil {
			return fmt.Errorf("failed to create tables for database %d: %v", dbIndex, err)
		}
	}

	log.Printf("Successfully created all tables across %d databases", s.numDBs)
	return nil
}

// createTablesForDB creates tables for a specific database
func (s *SQLite3ShardedDB) createTablesForDB(dbIndex int, useShortHashLen int) error {
	db := s.DBPools[dbIndex].DB
	if db == nil {
		return fmt.Errorf("database connection is nil")
	}

	// Create multiple tables per database
	tableNames := s.getTableNamesForDB()
	for _, tableName := range tableNames {
		query := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				h CHAR(%d) NOT NULL PRIMARY KEY,
				o TEXT
			) WITHOUT ROWID;
		`, tableName, useShortHashLen)

		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("failed to create table %s: %v", tableName, err)
		}

		// Create index
		indexQuery := fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_h ON %s(h);", tableName, tableName)
		if _, err := db.Exec(indexQuery); err != nil {
			log.Printf("WARN: Failed to create index for table %s: %v", tableName, err)
		}
	}
	return nil
}

// getTableNamesForDB returns all table names for a specific database
func (s *SQLite3ShardedDB) getTableNamesForDB() []string {
	var tables []string
	for i := 0; i < s.tablesPerDB; i++ {
		tables = append(tables, fmt.Sprintf("s%02x", i))
	}
	return tables
}
