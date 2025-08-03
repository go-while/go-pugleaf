package history

import (
	"database/sql"
	"fmt"
	"log"
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

// NewSQLite3DB creates a new SQLite3 database pool
func NewSQLite3DB(opts *SQLite3Opts, createTables bool, useShortHashLen int, mode int) (*SQLite3DB, error) {
	log.Printf("Opening database: %s", opts.dbPath)

	// Open database with just the file path, no connection parameters
	// This follows the same pattern as group databases to avoid locking issues
	connectionString := opts.dbPath
	if opts.params != "" {
		connectionString += opts.params
	}

	db, err := sql.Open("sqlite3", connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	db.SetMaxOpenConns(opts.maxOpen)
	db.SetMaxIdleConns(opts.initOpen)
	db.SetConnMaxLifetime(time.Duration(opts.timeout) * time.Second)

	log.Printf("Testing database connection for: %s", opts.dbPath)
	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %v", err)
	}

	log.Printf("Applying performance settings for: %s", opts.dbPath)
	// Apply additional high-performance settings
	if err := applyPerformanceSettings(db, mode); err != nil {
		log.Printf("WARN: Failed to apply some performance settings: %v", err)
	}
	log.Printf("Performance settings applied for: %s", opts.dbPath)

	DB := &SQLite3DB{
		dbPath:   opts.dbPath,
		params:   opts.params,
		maxOpen:  opts.maxOpen,
		initOpen: opts.initOpen,
		timeout:  opts.timeout,
		DB:       db,
	}
	return DB, nil
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
