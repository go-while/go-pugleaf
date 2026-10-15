package database

import (
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// query_NNTPNewGroupsSince selects the LIST ACTIVE columns of NNTPActiveNewsgroups for
// the active groups created at or after the parameter (UTC "YYYY-MM-DD HH:MM:SS").
// datetime() normalizes both the CURRENT_TIMESTAMP form and Go's
// "2006-01-02 15:04:05.999999999-07:00" form to UTC before comparing.
const query_NNTPNewGroupsSince = `SELECT name, COALESCE(description,''), COALESCE(last_article,0),
	COALESCE(low_water,1), COALESCE(status,'y')
	FROM newsgroups
	WHERE active = 1 AND created_at IS NOT NULL AND datetime(created_at) >= datetime(?)
	ORDER BY name`

// NNTPNewGroupsSince returns the active newsgroups whose created_at is at or after
// since, ordered by name, with Name, Description, LastArticle, LowWater, Status and
// Active set (NEWGROUPS, RFC 3977 §7.3). Groups without created_at are left out.
func (db *Database) NNTPNewGroupsSince(since time.Time) ([]*models.Newsgroup, error) {
	rows, err := RetryableQuery(db.mainDB, query_NNTPNewGroupsSince, since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Newsgroup
	for rows.Next() {
		g := &models.Newsgroup{Active: true}
		if err := rows.Scan(&g.Name, &g.Description, &g.LastArticle, &g.LowWater, &g.Status); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
