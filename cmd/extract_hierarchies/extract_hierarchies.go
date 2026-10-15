package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3"
)

// hierarchiesMigration is the embedded migration that holds the hierarchy list.
const hierarchiesMigration = "migrations/0003_main_create_hierarchies.sql"

func main() {
	activeFile := flag.String("active", "active_files/active.2025-06", "Active file to split (groupname low high status per line)")
	outputDir := flag.String("out", "active_files/hierarchies", "Output directory for the <hierarchy>.active files")
	flag.Parse()

	supportedHierarchies, err := createDatabaseAndReadHierarchies()
	if err != nil {
		log.Fatalf("Error initializing hierarchies: %v", err)
	}

	// Create output directory
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Open the active file
	file, err := os.Open(*activeFile)
	if err != nil {
		log.Fatalf("Failed to open active file (set -active): %v", err)
	}
	defer file.Close()

	// Sort hierarchies by length (longest first) to match most specific hierarchy first
	sort.Slice(supportedHierarchies, func(i, j int) bool {
		return len(supportedHierarchies[i]) > len(supportedHierarchies[j])
	})

	fmt.Printf("Loaded %d hierarchies, sorted by specificity (longest first)\n", len(supportedHierarchies))

	hierarchyGroups, unknownGroups, totalGroups, err := groupActiveLines(file, supportedHierarchies)
	if err != nil {
		log.Fatalf("Error reading active file: %v", err)
	}

	// Write each hierarchy to its own file
	processedGroups := 0
	writeErrors := 0
	for hierarchy, groups := range hierarchyGroups {
		if len(groups) == 0 {
			continue
		}

		// Sort groups alphabetically
		sort.Strings(groups)

		filename := filepath.Join(*outputDir, hierarchy+".active")
		if err := writeLines(filename, groups); err != nil {
			log.Printf("Failed to write file %s: %v", filename, err)
			writeErrors++
			continue
		}

		processedGroups += len(groups)
		fmt.Printf("Created %s with %d groups\n", filename, len(groups))
	}

	// Write unknown/unmatched groups to a separate file
	if len(unknownGroups) > 0 {
		sort.Strings(unknownGroups)
		filename := filepath.Join(*outputDir, "unknown.active")
		if err := writeLines(filename, unknownGroups); err != nil {
			log.Printf("Failed to write unknown groups file: %v", err)
			writeErrors++
		} else {
			fmt.Printf("Created %s with %d unmatched groups\n", filename, len(unknownGroups))
		}
	}

	// Summary
	fmt.Printf("\nSummary:\n")
	fmt.Printf("Total groups processed: %d\n", totalGroups)
	fmt.Printf("Groups matched to hierarchies: %d\n", processedGroups)
	fmt.Printf("Unmatched groups: %d\n", len(unknownGroups))
	fmt.Printf("Number of hierarchies with groups: %d\n", len(hierarchyGroups))

	// Show hierarchy statistics
	fmt.Printf("\nHierarchy statistics:\n")
	type hierarchyStat struct {
		name  string
		count int
	}

	var stats []hierarchyStat
	for hierarchy, groups := range hierarchyGroups {
		if len(groups) > 0 {
			stats = append(stats, hierarchyStat{hierarchy, len(groups)})
		}
	}

	// Sort by group count (descending)
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].count > stats[j].count
	})

	fmt.Printf("Top hierarchies by group count:\n")
	for i, stat := range stats {
		if i < 20 { // Show top 20
			fmt.Printf("  %-15s: %6d groups\n", stat.name, stat.count)
		}
	}

	fmt.Printf("\nFiles created in: %s/\n", *outputDir)
	if writeErrors > 0 {
		log.Printf("%d output file(s) could not be written", writeErrors)
		os.Exit(1)
	}
}

// groupActiveLines reads an active file and sorts its lines into hierarchies.
// hierarchies must be sorted longest first so the most specific one matches.
func groupActiveLines(r io.Reader, hierarchies []string) (map[string][]string, []string, int, error) {
	hierarchyGroups := make(map[string][]string)
	unknownGroups := []string{}
	totalGroups := 0

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Parse the line: groupname low high status
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}

		groupName := parts[0]
		totalGroups++

		// Find which hierarchy this group belongs to (most specific first)
		matched := false
		for _, hierarchy := range hierarchies {
			if strings.HasPrefix(groupName, hierarchy+".") || groupName == hierarchy {
				hierarchyGroups[hierarchy] = append(hierarchyGroups[hierarchy], line)
				matched = true
				break // Stop at first (most specific) match
			}
		}

		if !matched {
			unknownGroups = append(unknownGroups, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, 0, err
	}
	return hierarchyGroups, unknownGroups, totalGroups, nil
}

// writeLines writes one line per entry and reports write, flush and close errors.
func writeLines(filename string, lines []string) error {
	outFile, err := os.Create(filename)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(outFile)
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			_ = outFile.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = outFile.Close()
		return err
	}
	return outFile.Close()
}

// createDatabaseAndReadHierarchies loads the hierarchy list from the embedded
// migration into an in-memory SQLite database and returns the hierarchy names.
// It reads no file from the cwd and writes no database file.
func createDatabaseAndReadHierarchies() ([]string, error) {
	sqlBytes, err := database.EmbeddedMigrationsFS.ReadFile(hierarchiesMigration)
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded %s: %v", hierarchiesMigration, err)
	}

	db, err := sql.Open("sqlite3", "file::memory:")
	if err != nil {
		return nil, fmt.Errorf("failed to create database: %v", err)
	}
	defer db.Close()
	// Every pool connection would get its own empty in-memory database.
	db.SetMaxOpenConns(1)

	// Execute the SQL commands
	if _, err := database.RetryableExec(db, string(sqlBytes)); err != nil {
		return nil, fmt.Errorf("failed to execute SQL: %v", err)
	}

	// Query all hierarchy names
	rows, err := database.RetryableQuery(db, "SELECT name FROM hierarchies ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("failed to query hierarchies: %v", err)
	}
	defer rows.Close()

	var hierarchies []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan hierarchy name: %v", err)
		}
		hierarchies = append(hierarchies, name)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %v", err)
	}

	log.Printf("Loaded %d hierarchies from the embedded migration", len(hierarchies))
	return hierarchies, nil
}
