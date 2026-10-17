package history

import "database/sql"

// SQLite3ShardedPool interface for sharded database operations
type SQLite3ShardedPool interface {
	GetShardedDB(dbIndex int, write bool) (*sql.DB, error)
	Close() error
}

// compile-time check
var _ SQLite3ShardedPool = (*SQLite3ShardedDB)(nil)
