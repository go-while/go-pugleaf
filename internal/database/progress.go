// Package database provides fetching progress tracking
package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ProgressDB tracks fetching progress for newsgroups per backend
type ProgressDB struct {
	db *sql.DB
}

// ProgressEntry represents the fetching progress for a newsgroup on a backend
type ProgressEntry struct {
	ID            int       `db:"id"`
	BackendName   string    `db:"backend_name"`
	NewsgroupName string    `db:"newsgroup_name"`
	LastArticle   int64     `db:"last_article"`
	LastFetched   time.Time `db:"last_fetched"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

// NewProgressDB creates a new progress tracking database
func NewProgressDB(dataDir string) (*ProgressDB, error) {
	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(dataDir, "progress.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open progress database: %w", err)
	}

	progressDB := &ProgressDB{db: db}

	// Initialize schema
	if err := progressDB.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	//log.Printf("Progress database initialized at: %s", dbPath)
	return progressDB, nil
}

// initSchema creates the progress tracking table
func (p *ProgressDB) initSchema() error {
	schema := `
		CREATE TABLE IF NOT EXISTS progress (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			backend_name TEXT NOT NULL,
			newsgroup_name TEXT NOT NULL,
			last_article INTEGER NOT NULL DEFAULT 0,
			last_fetched DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(backend_name, newsgroup_name)
		);

		CREATE INDEX IF NOT EXISTS idx_progress_backend_group
		ON progress(backend_name, newsgroup_name);

		CREATE INDEX IF NOT EXISTS idx_progress_last_fetched
		ON progress(last_fetched);
	`

	_, err := p.db.Exec(schema)
	return err
}

// GetLastArticle returns the last fetched article number for a newsgroup on a backend
func (p *ProgressDB) GetLastArticle(backendName, newsgroupName string) (int64, error) {
	var lastArticle int64
	err := retryableQueryRowScan(p.db, `
		SELECT last_article FROM progress
		WHERE backend_name = ? AND newsgroup_name = ?
	`, []interface{}{backendName, newsgroupName}, &lastArticle)

	if err == sql.ErrNoRows {
		return 0, nil // No previous progress, start from 0
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get last article: %w", err)
	}

	return lastArticle, nil
}

// UpdateProgress updates the fetching progress for a newsgroup on a backend
func (p *ProgressDB) UpdateProgress(backendName, newsgroupName string, lastArticle int64) error {
	_, err := retryableExec(p.db, `
		INSERT INTO progress (backend_name, newsgroup_name, last_article, last_fetched, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(backend_name, newsgroup_name) DO UPDATE SET
			last_article = excluded.last_article,
			last_fetched = excluded.last_fetched,
			updated_at = excluded.updated_at
	`, backendName, newsgroupName, lastArticle)

	if err != nil {
		return fmt.Errorf("failed to update progress: %w", err)
	}

	log.Printf("Updated progress: %s/%s -> article %d", backendName, newsgroupName, lastArticle)
	return nil
}

// GetAllProgress returns all progress entries
func (p *ProgressDB) GetAllProgress() ([]*ProgressEntry, error) {
	rows, err := retryableQuery(p.db, `
		SELECT id, backend_name, newsgroup_name, last_article,
			   COALESCE(last_fetched, '') as last_fetched,
			   created_at, updated_at
		FROM progress
		ORDER BY backend_name, newsgroup_name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query progress: %w", err)
	}
	defer rows.Close()

	var entries []*ProgressEntry
	for rows.Next() {
		var entry ProgressEntry
		var lastFetchedStr string
		err := rows.Scan(&entry.ID, &entry.BackendName, &entry.NewsgroupName,
			&entry.LastArticle, &lastFetchedStr, &entry.CreatedAt, &entry.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan progress row: %w", err)
		}

		// Parse last_fetched if not empty
		if lastFetchedStr != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", lastFetchedStr); err == nil {
				entry.LastFetched = t
			}
		}

		entries = append(entries, &entry)
	}

	return entries, nil
}

// GetProgressForBackend returns progress entries for a specific backend
func (p *ProgressDB) GetProgressForBackend(backendName string) ([]*ProgressEntry, error) {
	rows, err := retryableQuery(p.db, `
		SELECT id, backend_name, newsgroup_name, last_article,
			   COALESCE(last_fetched, '') as last_fetched,
			   created_at, updated_at
		FROM progress
		WHERE backend_name = ?
		ORDER BY newsgroup_name
	`, backendName)
	if err != nil {
		return nil, fmt.Errorf("failed to query progress for backend: %w", err)
	}
	defer rows.Close()

	var entries []*ProgressEntry
	for rows.Next() {
		var entry ProgressEntry
		var lastFetchedStr string
		err := rows.Scan(&entry.ID, &entry.BackendName, &entry.NewsgroupName,
			&entry.LastArticle, &lastFetchedStr, &entry.CreatedAt, &entry.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan progress row: %w", err)
		}

		// Parse last_fetched if not empty
		if lastFetchedStr != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", lastFetchedStr); err == nil {
				entry.LastFetched = t
			}
		}

		entries = append(entries, &entry)
	}

	return entries, nil
}

// Close closes the progress database
func (p *ProgressDB) Close() error {
	if p.db != nil {
		return p.db.Close()
	}
	return nil
}
