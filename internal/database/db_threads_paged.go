package database

import (
	"database/sql"

	"github.com/go-while/go-pugleaf/internal/models"
)

const query_GetThreadsPaged = `SELECT id, root_article, parent_article, child_article, depth, thread_order FROM threads ORDER BY id LIMIT ? OFFSET ?`

// GetThreadsPaged returns at most limit rows of the threads table ordered by id, starting at offset.
func (db *Database) GetThreadsPaged(groupDB *GroupDB, limit, offset int) ([]*models.Thread, error) {
	rows, err := RetryableQuery(groupDB.DB, query_GetThreadsPaged, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*models.Thread, 0, max(0, min(limit, 1024)))
	for rows.Next() {
		var t models.Thread
		var parentArticle sql.NullInt64
		if err := rows.Scan(&t.ID, &t.RootArticle, &parentArticle, &t.ChildArticle, &t.Depth, &t.ThreadOrder); err != nil {
			return nil, err
		}
		if parentArticle.Valid {
			t.ParentArticle = &parentArticle.Int64
		}
		out = append(out, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
