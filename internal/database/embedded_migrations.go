package database

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed migrations/*.sql
var EmbeddedMigrationsFS embed.FS

// Global migration cache for embedded files
var (
	embeddedMigrationCache     []*MigrationFile
	embeddedMigrationCacheMux  sync.RWMutex
	embeddedMigrationCacheInit bool
)

// SetEmbeddedMigrations sets the embedded filesystem for migrations
func SetEmbeddedMigrations(fs embed.FS) {
	EmbeddedMigrationsFS = fs
}

// getEmbeddedMigrationFiles reads and parses all migration files from embedded filesystem
func getEmbeddedMigrationFiles() ([]*MigrationFile, error) {
	// Check the cache first
	embeddedMigrationCacheMux.RLock()
	if embeddedMigrationCacheInit {
		// Return a copy of the cached slice to avoid concurrent access issues
		cachedMigrations := make([]*MigrationFile, len(embeddedMigrationCache))
		copy(cachedMigrations, embeddedMigrationCache)
		embeddedMigrationCacheMux.RUnlock()
		return cachedMigrations, nil
	}
	embeddedMigrationCacheMux.RUnlock()

	// Check if embedded filesystem is available
	if EmbeddedMigrationsFS == (embed.FS{}) {
		// Fall back to regular file system migration loading
		return getMigrationFiles()
	}

	// Cache not initialized, read from embedded filesystem
	files, err := fs.ReadDir(EmbeddedMigrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded migrations directory: %w", err)
	}

	var migrations []*MigrationFile
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".sql") {
			continue
		}

		migration, err := parseEmbeddedMigrationFileName(f.Name())
		if err != nil {
			// Log warning but continue with other migrations
			fmt.Printf("Warning: skipping invalid embedded migration file %s: %v\n", f.Name(), err)
			continue
		}

		migrations = append(migrations, migration)
	}

	// Sort by version number
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	// Update the cache
	embeddedMigrationCacheMux.Lock()
	embeddedMigrationCache = migrations
	embeddedMigrationCacheInit = true
	embeddedMigrationCacheMux.Unlock()

	return migrations, nil
}

// parseEmbeddedMigrationFileName parses a migration filename from embedded filesystem
func parseEmbeddedMigrationFileName(fileName string) (*MigrationFile, error) {
	// Remove .sql extension
	base := strings.TrimSuffix(fileName, ".sql")
	
	// Split by underscore
	parts := strings.Split(base, "_")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid migration filename format: %s (expected format: NNNN_type_description.sql)", fileName)
	}

	// Parse version number
	version, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid version number in filename %s: %w", fileName, err)
	}

	// Parse migration type
	var migrationType MigrationType
	switch parts[1] {
	case "main":
		migrationType = MigrationTypeMain
	case "active":
		migrationType = MigrationTypeActive
	case "group", "single": // Support both 'group' and 'single' for backward compatibility
		migrationType = MigrationTypeGroup
	default:
		return nil, fmt.Errorf("unknown migration type in filename %s: %s", fileName, parts[1])
	}

	return &MigrationFile{
		FileName:    fileName,
		Version:     version,
		Type:        migrationType,
		Description: strings.Join(parts[2:], "_"),
		FilePath:    filepath.Join("migrations", fileName), // Virtual path for embedded files
		IsEmbedded:  true, // Mark as embedded
	}, nil
}

// readEmbeddedMigrationContent reads the content of an embedded migration file
func readEmbeddedMigrationContent(migration *MigrationFile) (string, error) {
	if EmbeddedMigrationsFS == (embed.FS{}) {
		return "", fmt.Errorf("embedded migrations filesystem not initialized")
	}
	
	content, err := fs.ReadFile(EmbeddedMigrationsFS, migration.FilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read embedded migration file %s: %w", migration.FilePath, err)
	}
	return string(content), nil
}
