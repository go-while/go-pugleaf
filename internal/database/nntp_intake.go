package database

import (
	"database/sql"
	"errors"
	"fmt"
)

// IntakeGroup is the main-DB state of a newsgroup that article intake needs. Scanned NULL-safe:
// it works for rows with a NULL hierarchy (A-db-1) or NULL counters.
type IntakeGroup struct {
	ID          int64
	Name        string
	Active      bool
	Status      string // 'y' when NULL
	MaxArtSize  int64  // 0 = no per-group limit
	LastArticle int64
	HighWater   int64
}

// query_GetIntakeGroup reads only the columns intake needs and maps NULLs to defaults, so a row
// written without hierarchy or counters (A-db-1) still resolves.
const query_GetIntakeGroup = `SELECT id, name, active, COALESCE(status,'y'), COALESCE(max_art_size,0),
	COALESCE(last_article,0), COALESCE(high_water,0) FROM newsgroups WHERE name = ?`

// GetIntakeGroup returns the intake view of the newsgroup with exactly this name.
// It returns sql.ErrNoRows (unwrapped, errors.Is works) when no row has that exact name.
func (db *Database) GetIntakeGroup(name string) (*IntakeGroup, error) {
	if db == nil || db.mainDB == nil {
		return nil, fmt.Errorf("GetIntakeGroup '%s': main database is not open", name)
	}
	var g IntakeGroup
	if err := RetryableQueryRowScan(db.mainDB, query_GetIntakeGroup, []interface{}{name},
		&g.ID, &g.Name, &g.Active, &g.Status, &g.MaxArtSize, &g.LastArticle, &g.HighWater); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("GetIntakeGroup '%s': %w", name, err)
	}
	return &g, nil
}

// LookupMsgIdInArticlesDB reports whether the group database stores an article with this
// message-id. Unlike the former ExistsMsgIdInArticlesDB (removed, plan followup-leftovers) it tells "not stored" (false, nil) apart from a
// failed lookup (false, err), so callers do not mistake a database error for a missing
// article (A-db-3).
func (db *GroupDB) LookupMsgIdInArticlesDB(messageID string) (bool, error) {
	if db == nil || db.DB == nil {
		return false, fmt.Errorf("LookupMsgIdInArticlesDB: group database is not open")
	}
	var exists bool
	if err := RetryableQueryRowScan(db.DB, "SELECT 1 FROM articles WHERE message_id = ? LIMIT 1",
		[]interface{}{messageID}, &exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("LookupMsgIdInArticlesDB '%s' in '%s': %w", messageID, db.Newsgroup, err)
	}
	return exists, nil
}
