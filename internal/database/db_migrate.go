package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Global migration cache to avoid repeated file system operations
var (
	migrationCache     []*MigrationFile
	migrationCacheMux  sync.RWMutex
	migrationCacheInit bool

	// Cache for databases that have already been fully migrated
	migratedDBsCache map[string]bool // key: dbType:filename
	migratedDBsMux   sync.RWMutex
)

// initMigratedDBsCache initializes the migrated databases cache
func initMigratedDBsCache() {
	migratedDBsMux.Lock()
	if migratedDBsCache == nil {
		migratedDBsCache = make(map[string]bool)
	}
	migratedDBsMux.Unlock()
}

// MigrationType represents the type of database that migrations apply to
type MigrationType string

const (
	MigrationTypeMain   MigrationType = "main"
	MigrationTypeActive MigrationType = "active"
	MigrationTypeGroup  MigrationType = "group" // Single migration type for group databases
)

// MigrationFile represents a migration file with its metadata
type MigrationFile struct {
	FileName    string
	Version     int
	Type        MigrationType
	Description string
	FilePath    string
}

// Migrate applies database migrations to all database types
func (db *Database) Migrate() error {
	// Apply main database migrations
	if err := db.migrateMainDB(); err != nil {
		return fmt.Errorf("failed to migrate main database: %w", err)
	}

	// Apply active database migrations
	if err := db.migrateActiveDB(); err != nil {
		return fmt.Errorf("failed to migrate active database: %w", err)
	}

	return nil
}

// parseMigrationFileName parses a migration file name to extract metadata
func parseMigrationFileName(fileName string) (*MigrationFile, error) {
	if !strings.HasSuffix(fileName, ".sql") {
		return nil, fmt.Errorf("migration file must have .sql extension: %s", fileName)
	}
	//log.Printf("Parsing migration file: %s", fileName)
	// Remove .sql extension
	name := strings.TrimSuffix(fileName, ".sql")
	parts := strings.SplitN(name, "_", 3)

	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid migration file name format: %s (expected format: 0001_type_description.sql)", fileName)
	}

	// Parse version number
	version, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid version number in migration file: %s", fileName)
	}

	// Determine migration type
	var migrationType MigrationType
	switch parts[1] {
	case "main":
		migrationType = MigrationTypeMain
	case "active":
		migrationType = MigrationTypeActive
	case "single":
		// All group-related migrations now use the single group type
		migrationType = MigrationTypeGroup
	default:
		return nil, fmt.Errorf("invalid database migration name")
	}
	//log.Printf("Parsed migration file: %s, version: %d, type: %s, description: %s", fileName, version, migrationType, strings.Join(parts[2:], "_"))
	return &MigrationFile{
		FileName:    fileName,
		Version:     version,
		Type:        migrationType,
		Description: strings.Join(parts[2:], "_"),
		FilePath:    filepath.Join("./migrations", fileName),
	}, nil
}

// getMigrationFiles reads and parses all migration files from the migrations directory
func getMigrationFiles() ([]*MigrationFile, error) {
	// Check the cache first
	migrationCacheMux.RLock()
	if migrationCacheInit {
		// Return a copy of the cached slice to avoid concurrent map iteration
		cachedMigrations := make([]*MigrationFile, len(migrationCache))
		copy(cachedMigrations, migrationCache)
		migrationCacheMux.RUnlock()
		return cachedMigrations, nil
	}
	migrationCacheMux.RUnlock()

	// Cache not initialized, read from file system
	files, err := os.ReadDir("./migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}
	var migrations []*MigrationFile
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".sql") {
			continue
		}

		migration, err := parseMigrationFileName(f.Name())
		if err != nil {
			// Log warning but continue with other migrations
			fmt.Printf("Warning: skipping invalid migration file %s: %v\n", f.Name(), err)
			continue
		}

		migrations = append(migrations, migration)
	}

	// Sort by version number
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	// Update the cache
	migrationCacheMux.Lock()
	migrationCache = migrations
	migrationCacheInit = true
	migrationCacheMux.Unlock()

	return migrations, nil
}

// ensureMigrationsTable creates the schema_migrations table if it doesn't exist
func ensureMigrationsTable(db *sql.DB, dbType string) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT NOT NULL UNIQUE,
		db_type TEXT NOT NULL DEFAULT '',
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table for %s: %w", dbType, err)
	}
	return nil
}

// getAppliedMigrations returns a map of applied migration filenames for a specific database
func getAppliedMigrations(db *sql.DB, dbType string) (map[string]bool, error) {
	applied := make(map[string]bool)

	rows, err := db.Query(`SELECT filename FROM schema_migrations WHERE db_type = ? OR db_type = ''`, dbType)
	if err != nil {
		log.Printf("Failed to query applied migrations for %s: %v", dbType, err)
		return nil, fmt.Errorf("failed to query applied migrations for %s: %w", dbType, err)
	}
	defer rows.Close()

	for rows.Next() {
		var fname string
		if err := rows.Scan(&fname); err != nil {
			log.Printf("Failed to scan migration filename for %s: %v", dbType, err)
			return nil, fmt.Errorf("failed to scan migration filename for %s: %w", dbType, err)
		}
		applied[fname] = true
	}

	if err := rows.Err(); err != nil {
		log.Printf("Error iterating migration rows for %s: %v", dbType, err)
		return nil, fmt.Errorf("error iterating migration rows for %s: %w", dbType, err)
	}

	return applied, nil
}

// applyMigration applies a single migration to a database
func applyMigration(db *sql.DB, migration *MigrationFile, dbType string) error {
	content, err := os.ReadFile(migration.FilePath)
	if err != nil {
		// Log error and return a wrapped error
		log.Printf("Failed to read migration file %s: %v", migration.FilePath, err)
		return fmt.Errorf("failed to read migration file %s: %w", migration.FilePath, err)
	}

	// Apply the migration
	if _, err := db.Exec(string(content)); err != nil {
		log.Printf("Failed to execute migration %s for %s: %v", migration.FileName, dbType, err)
		return fmt.Errorf("failed to execute migration %s for %s: %w", migration.FileName, dbType, err)
	}

	// Record the migration as applied
	_, err = db.Exec(`INSERT INTO schema_migrations (filename, db_type) VALUES (?, ?)`, migration.FileName, dbType)
	if err != nil {
		log.Printf("Failed to record migration %s for %s: %v", migration.FileName, dbType, err)
		return fmt.Errorf("failed to record migration %s for %s: %w", migration.FileName, dbType, err)
	}

	//log.Printf("Applied migration %s to %s database", migration.FileName, dbType)
	return nil
}

// migrateActiveDB applies migrations to the active database
func (db *Database) migrateActiveDB() error {
	if db.activeDB == nil {
		log.Printf("Active database not initialized")
		return fmt.Errorf("active database not initialized")
	}

	activeDBConn := db.activeDB.GetDB()
	if activeDBConn == nil {
		log.Printf("Active database connection is nil")
		return fmt.Errorf("active database connection is nil")
	}

	// Ensure migrations table exists
	if err := ensureMigrationsTable(activeDBConn, "active"); err != nil {
		log.Printf("Failed to ensure migrations table for active database: %v", err)
		return err
	}

	// Get migration files
	migrations, err := getMigrationFiles()
	if err != nil {
		log.Printf("Failed to get migration files: %v", err)
		return err
	}

	// Get applied migrations
	applied, err := getAppliedMigrations(activeDBConn, "active")
	if err != nil {
		log.Printf("Failed to get applied migrations for active database: %v", err)
		return err
	}

	// Apply migrations for active database
	for _, migration := range migrations {
		if migration.Type == MigrationTypeActive && !applied[migration.FileName] {
			if err := applyMigration(activeDBConn, migration, "active"); err != nil {
				return err
			}
		}
	}

	return nil
}

// migrateMainDB applies migrations to the main database
func (db *Database) migrateMainDB() error {
	// Ensure migrations table exists
	if err := ensureMigrationsTable(db.mainDB, "main"); err != nil {
		log.Printf("Failed to ensure migrations table for main database: %v", err)
		return err
	}

	// Get migration files
	migrations, err := getMigrationFiles()
	if err != nil {
		log.Printf("Failed to get migration files: %v", err)
		return err
	}

	// Get applied migrations
	applied, err := getAppliedMigrations(db.mainDB, "main")
	if err != nil {
		log.Printf("Failed to get applied migrations for main database: %v", err)
		return err
	}

	// Apply migrations for main database
	for _, migration := range migrations {
		if migration.Type == MigrationTypeMain && !applied[migration.FileName] {
			if err := applyMigration(db.mainDB, migration, "main"); err != nil {
				log.Printf("Failed to apply migration %s to main database: %v", migration.FileName, err)
				return err
			}
			//log.Printf("Done: apply migration %s to main database", migration.FileName)
		}
	}

	return nil
}

// MigrateGroup applies migrations for a specific newsgroup database
func (db *Database) MigrateGroup(groupName string) error {
	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		log.Printf("Failed to get group database for %s: %v", groupName, err)
		return fmt.Errorf("failed to get group database: %w", err)
	}
	defer groupDBs.Return(db)

	return db.migrateGroupDB(groupDBs)
}

// migrateGroupDB applies migrations to a group database
func (db *Database) migrateGroupDB(groupDBs *GroupDBs) error {
	// Initialize cache if needed
	initMigratedDBsCache()

	// Get migration files
	migrations, err := getMigrationFiles()
	if err != nil {
		log.Printf("Failed to get migration files: %v", err)
		return err
	}

	// Create a cache key based on group name
	cacheKey := fmt.Sprintf("%s:group", groupDBs.Newsgroup)

	// Check if this database is already known to be fully migrated
	migratedDBsMux.RLock()
	isFullyMigrated := migratedDBsCache[cacheKey]
	migratedDBsMux.RUnlock()

	if isFullyMigrated {
		// Skip migration checks for this database
		return nil
	}

	// Ensure migrations table exists
	if err := ensureMigrationsTable(groupDBs.DB, "group"); err != nil {
		log.Printf("Failed to ensure migrations table for group: %v", err)
		return fmt.Errorf("failed to ensure migrations table for group: %w", err)
	}

	// Get applied migrations
	applied, err := getAppliedMigrations(groupDBs.DB, "group")
	if err != nil {
		log.Printf("Failed to get applied migrations for group: %v", err)
		return fmt.Errorf("failed to get applied migrations for group: %w", err)
	}

	// Check if all migrations for this type are already applied
	allApplied := true
	migrationsToApply := 0
	for _, migration := range migrations {
		if migration.Type == MigrationTypeGroup {
			migrationsToApply++
			if !applied[migration.FileName] {
				allApplied = false
				break
			}
		}
	}
	// If all migrations are applied, cache this fact
	if allApplied && migrationsToApply > 0 {
		migratedDBsMux.Lock()
		migratedDBsCache[cacheKey] = true
		migratedDBsMux.Unlock()
	}

	// Apply missing migrations for group database
	for _, migration := range migrations {
		if migration.Type == MigrationTypeGroup && !applied[migration.FileName] {
			if err := applyMigration(groupDBs.DB, migration, "group"); err != nil {
				return fmt.Errorf("failed to apply migration %s to group database: %w", migration.FileName, err)
			}
			//log.Printf("Done: apply migration %s to group database\n", migration.FileName)
		}
	}

	return nil
}
