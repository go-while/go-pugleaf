package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	_ "github.com/mattn/go-sqlite3"
)

// ActiveDB manages the active.db database for newsgroup registry
type ActiveDB struct {
	db   *sql.DB
	path string
}

// NewActiveDB creates a new active database connection
func NewActiveDB(dataDir string) (*ActiveDB, error) {
	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(dataDir, "active.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open active database: %w", err)
	}

	activeDB := &ActiveDB{
		db:   db,
		path: dbPath,
	}

	// Initialize schema
	if err := activeDB.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return activeDB, nil
}

// initSchema creates the necessary tables
func (a *ActiveDB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS newsgroups (
		group_id INTEGER PRIMARY KEY AUTOINCREMENT,
		group_name TEXT UNIQUE NOT NULL,
		description TEXT,
		high_water INTEGER DEFAULT 0,
		low_water INTEGER DEFAULT 1,
		message_count INTEGER DEFAULT 0,
		status CHAR(1) DEFAULT 'y',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE UNIQUE INDEX IF NOT EXISTS idx_group_name ON newsgroups(group_name);
	CREATE INDEX IF NOT EXISTS idx_status ON newsgroups(status);
	CREATE INDEX IF NOT EXISTS idx_group_id ON newsgroups(group_id);
	`

	_, err := a.db.Exec(schema)
	return err
}

// Close closes the database connection
func (a *ActiveDB) Close() error {
	return a.db.Close()
}

// AddNewsgroup adds a new newsgroup to active.db
func (a *ActiveDB) AddNewsgroup(groupName, description string) (*models.ActiveNewsgroup, error) {
	query := `
	INSERT INTO newsgroups (group_name, description, created_at, updated_at)
	VALUES (?, ?, ?, ?)
	`

	now := time.Now()
	result, err := a.db.Exec(query, groupName, description, now, now)
	if err != nil {
		return nil, fmt.Errorf("failed to insert newsgroup: %w", err)
	}

	groupID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get insert ID: %w", err)
	}

	// Return the created newsgroup
	return a.GetNewsgroupByID(int(groupID))
}

// GetNewsgroup gets a newsgroup by name
func (a *ActiveDB) GetNewsgroup(groupName string) (*models.ActiveNewsgroup, error) {
	query := `
	SELECT group_id, group_name, description, high_water, low_water,
	       message_count, status, created_at, updated_at
	FROM newsgroups
	WHERE group_name = ?
	`

	row := a.db.QueryRow(query, groupName)

	var ng models.ActiveNewsgroup
	err := row.Scan(&ng.GroupID, &ng.GroupName, &ng.Description, &ng.HighWater,
		&ng.LowWater, &ng.MessageCount, &ng.Status, &ng.CreatedAt, &ng.UpdatedAt)

	if err != nil {
		return nil, err
	}

	return &ng, nil
}

// GetNewsgroupByID gets a newsgroup by ID
func (a *ActiveDB) GetNewsgroupByID(groupID int) (*models.ActiveNewsgroup, error) {
	query := `
	SELECT group_id, group_name, description, high_water, low_water,
	       message_count, status, created_at, updated_at
	FROM newsgroups
	WHERE group_id = ?
	`

	row := a.db.QueryRow(query, groupID)

	var ng models.ActiveNewsgroup
	err := row.Scan(&ng.GroupID, &ng.GroupName, &ng.Description, &ng.HighWater,
		&ng.LowWater, &ng.MessageCount, &ng.Status, &ng.CreatedAt, &ng.UpdatedAt)

	if err != nil {
		return nil, err
	}

	return &ng, nil
}

// ListNewsgroups lists all newsgroups
func (a *ActiveDB) ListNewsgroups() ([]*models.ActiveNewsgroup, error) {
	query := `
	SELECT group_id, group_name, description, high_water, low_water,
	       message_count, status, created_at, updated_at
	FROM newsgroups
	ORDER BY group_name
	`

	rows, err := a.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var newsgroups []*models.ActiveNewsgroup
	for rows.Next() {
		var ng models.ActiveNewsgroup
		err := rows.Scan(&ng.GroupID, &ng.GroupName, &ng.Description, &ng.HighWater,
			&ng.LowWater, &ng.MessageCount, &ng.Status, &ng.CreatedAt, &ng.UpdatedAt)
		if err != nil {
			return nil, err
		}
		newsgroups = append(newsgroups, &ng)
	}

	return newsgroups, rows.Err()
}

// UpdateWatermarks updates high/low water marks for a newsgroup
func (a *ActiveDB) UpdateWatermarks(groupID int, highWater, lowWater int) error {
	query := `
	UPDATE newsgroups
	SET high_water = ?, low_water = ?, updated_at = ?
	WHERE group_id = ?
	`

	_, err := a.db.Exec(query, highWater, lowWater, time.Now(), groupID)
	return err
}

// UpdateMessageCount updates the message count for a newsgroup
func (a *ActiveDB) UpdateMessageCount(groupID int, messageCount int) error {
	query := `
	UPDATE newsgroups
	SET message_count = ?, updated_at = ?
	WHERE group_id = ?
	`

	_, err := a.db.Exec(query, messageCount, time.Now(), groupID)
	return err
}

// SetStatus sets the status of a newsgroup
func (a *ActiveDB) SetStatus(groupID int, status string) error {
	query := `
	UPDATE newsgroups
	SET status = ?, updated_at = ?
	WHERE group_id = ?
	`

	_, err := a.db.Exec(query, status, time.Now(), groupID)
	return err
}

// RemoveNewsgroup removes a newsgroup from active.db
func (a *ActiveDB) RemoveNewsgroup(groupID int) error {
	query := `DELETE FROM newsgroups WHERE group_id = ?`
	_, err := a.db.Exec(query, groupID)
	return err
}

// GetDB returns the underlying database connection for migration purposes
func (a *ActiveDB) GetDB() *sql.DB {
	return a.db
}
