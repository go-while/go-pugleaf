package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-while/go-pugleaf/internal/models"
)

// Global routing pins (table group_routes, migration 0039, plan fetcher-group-routing): a group
// that an enabled route's patterns want is fetched by nntp-fetcher only from that route's provider.
// The pattern syntax is the one of providers.routing; nntp.ValidateRoutingPatterns checks it
// (this package cannot import nntp), and nntp.NewGroupRouter refuses a stored invalid pattern.

// ErrProviderHasRoutes is returned by DeleteProvider while group_routes reference the provider.
var ErrProviderHasRoutes = errors.New("provider is used by global routes")

// MaxGroupRouteNotes bounds group_routes.notes (bytes).
const MaxGroupRouteNotes = 1024

const groupRouteColumns = `id, patterns, provider_id, priority, enabled, notes, created_at`

const query_CountGroupRoutesForProvider = `SELECT COUNT(*) FROM group_routes WHERE provider_id = ?`

func scanGroupRoute(scan func(dest ...interface{}) error) (*models.GroupRoute, error) {
	var r models.GroupRoute
	if err := scan(&r.ID, &r.Patterns, &r.ProviderID, &r.Priority, &r.Enabled, &r.Notes, &r.CreatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *Database) queryGroupRoutes(where string, args ...interface{}) ([]*models.GroupRoute, error) {
	rows, err := RetryableQuery(db.mainDB, `SELECT `+groupRouteColumns+` FROM group_routes `+where+` ORDER BY priority ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.GroupRoute
	for rows.Next() {
		r, err := scanGroupRoute(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetGroupRoutes returns all routes ordered by priority, then id.
func (db *Database) GetGroupRoutes() ([]*models.GroupRoute, error) {
	return db.queryGroupRoutes("")
}

// GetEnabledGroupRoutes returns the enabled routes ordered by priority, then id.
func (db *Database) GetEnabledGroupRoutes() ([]*models.GroupRoute, error) {
	return db.queryGroupRoutes("WHERE enabled = 1")
}

// GetGroupRouteByID returns one route; sql.ErrNoRows when there is none.
func (db *Database) GetGroupRouteByID(id int64) (*models.GroupRoute, error) {
	return scanGroupRoute(func(dest ...interface{}) error {
		return RetryableQueryRowScan(db.mainDB, `SELECT `+groupRouteColumns+` FROM group_routes WHERE id = ?`, []interface{}{id}, dest...)
	})
}

// CountGroupRoutesForProvider returns how many routes (enabled or not) pin groups to providerID.
func (db *Database) CountGroupRoutesForProvider(providerID int64) (int, error) {
	var n int
	if err := RetryableQueryRowScan(db.mainDB, query_CountGroupRoutesForProvider, []interface{}{providerID}, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// ValidateGroupRoute checks the fields of a route before it is written and trims Patterns and
// Notes: patterns not empty, a provider id, notes at most MaxGroupRouteNotes bytes. It does not
// check the pattern syntax (nntp.ValidateRoutingPatterns) nor that the provider exists (Insert and
// Update do).
func ValidateGroupRoute(r *models.GroupRoute) error {
	if r == nil {
		return errors.New("group route is nil")
	}
	r.Patterns = strings.TrimSpace(r.Patterns)
	r.Notes = strings.TrimSpace(r.Notes)
	if r.Patterns == "" {
		return errors.New("group route: patterns are empty")
	}
	if r.ProviderID <= 0 {
		return fmt.Errorf("group route %q: no provider", r.Patterns)
	}
	if len(r.Notes) > MaxGroupRouteNotes {
		return fmt.Errorf("group route %q: notes longer than %d bytes", r.Patterns, MaxGroupRouteNotes)
	}
	return nil
}

// groupRouteProviderExists returns an error when providerID is not a providers row.
func (db *Database) groupRouteProviderExists(providerID int64) error {
	p, err := db.GetProviderByID(providerID)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("group route: provider %d does not exist", providerID)
	}
	return nil
}

// InsertGroupRoute validates and inserts a route and returns its id (also set in r.ID).
func (db *Database) InsertGroupRoute(r *models.GroupRoute) (int64, error) {
	if err := ValidateGroupRoute(r); err != nil {
		return 0, err
	}
	if err := db.groupRouteProviderExists(r.ProviderID); err != nil {
		return 0, err
	}
	res, err := RetryableExec(db.mainDB, `INSERT INTO group_routes (patterns, provider_id, priority, enabled, notes) VALUES (?, ?, ?, ?, ?)`,
		r.Patterns, r.ProviderID, r.Priority, r.Enabled, r.Notes)
	if err != nil {
		return 0, fmt.Errorf("failed to add group route %q: %w", r.Patterns, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	r.ID = id
	return id, nil
}

// UpdateGroupRoute validates and writes every field of r except id and created_at.
func (db *Database) UpdateGroupRoute(r *models.GroupRoute) error {
	if err := ValidateGroupRoute(r); err != nil {
		return err
	}
	if err := db.groupRouteProviderExists(r.ProviderID); err != nil {
		return err
	}
	res, err := RetryableExec(db.mainDB, `UPDATE group_routes SET patterns = ?, provider_id = ?, priority = ?, enabled = ?, notes = ? WHERE id = ?`,
		r.Patterns, r.ProviderID, r.Priority, r.Enabled, r.Notes, r.ID)
	if err != nil {
		return fmt.Errorf("failed to update group route %d: %w", r.ID, err)
	}
	return requireGroupRouteRow(res, r.ID)
}

// DeleteGroupRoute removes a route.
func (db *Database) DeleteGroupRoute(id int64) error {
	res, err := RetryableExec(db.mainDB, `DELETE FROM group_routes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete group route %d: %w", id, err)
	}
	return requireGroupRouteRow(res, id)
}

// ToggleGroupRoute flips the enabled flag of a route.
func (db *Database) ToggleGroupRoute(id int64) error {
	res, err := RetryableExec(db.mainDB, `UPDATE group_routes SET enabled = CASE WHEN enabled = 0 THEN 1 ELSE 0 END WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to toggle group route %d: %w", id, err)
	}
	return requireGroupRouteRow(res, id)
}

// requireGroupRouteRow returns sql.ErrNoRows (wrapped) when the statement touched no route.
func requireGroupRouteRow(res sql.Result, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("group route %d: %w", id, sql.ErrNoRows)
	}
	return nil
}
