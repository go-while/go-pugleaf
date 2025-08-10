package history

import "database/sql"

// SQLite3Pool interface for database operations (legacy, single DB only)
type SQLite3Pool interface {
	GetDB(write bool) (*sql.DB, error)
	ReturnDB(db *sql.DB)
	Close() error
}

// SQLite3ShardedPool interface for sharded database operations
type SQLite3ShardedPool interface {
	GetShardedDB(dbIndex int, write bool) (*sql.DB, error)
	//ReturnShardedDB(dbIndex int, db *sql.DB)
	Close() error
}
