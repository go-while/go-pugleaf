package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	var err error
	supportedHierarchies, err := createDatabaseAndReadHierarchies()
	if err != nil {
		log.Fatalf("Error initializing hierarchies: %v", err)
	}

	activeFile := "active_files/active.2025-06"
	outputDir := "active_files/hierarchies"

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Open the active file
	file, err := os.Open(activeFile)
	if err != nil {
		log.Fatalf("Failed to open active file: %v", err)
	}
	defer file.Close()

	// Maps to store groups by hierarchy
	hierarchyGroups := make(map[string][]string)
	unknownGroups := []string{}
	totalGroups := 0

	// Sort hierarchies by length (longest first) to match most specific hierarchy first
	sort.Slice(supportedHierarchies, func(i, j int) bool {
		return len(supportedHierarchies[i]) > len(supportedHierarchies[j])
	})

	fmt.Printf("Loaded %d hierarchies, sorted by specificity (longest first)\n", len(supportedHierarchies))

	// Read the active file line by line
	scanner := bufio.NewScanner(file)
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
		for _, hierarchy := range supportedHierarchies {
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
		log.Fatalf("Error reading active file: %v", err)
	}

	// Write each hierarchy to its own file
	processedGroups := 0
	for hierarchy, groups := range hierarchyGroups {
		if len(groups) == 0 {
			continue
		}

		// Sort groups alphabetically
		sort.Strings(groups)

		filename := filepath.Join(outputDir, hierarchy+".active")
		outFile, err := os.Create(filename)
		if err != nil {
			log.Printf("Failed to create file %s: %v", filename, err)
			continue
		}

		for _, group := range groups {
			fmt.Fprintln(outFile, group)
		}
		outFile.Close()

		processedGroups += len(groups)
		fmt.Printf("Created %s with %d groups\n", filename, len(groups))
	}

	// Write unknown/unmatched groups to a separate file
	if len(unknownGroups) > 0 {
		sort.Strings(unknownGroups)
		filename := filepath.Join(outputDir, "unknown.active")
		outFile, err := os.Create(filename)
		if err != nil {
			log.Printf("Failed to create unknown groups file: %v", err)
		} else {
			for _, group := range unknownGroups {
				fmt.Fprintln(outFile, group)
			}
			outFile.Close()
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

	fmt.Printf("\nFiles created in: %s/\n", outputDir)
}

// createDatabaseAndReadHierarchies creates a SQLite database from the SQL file and returns hierarchy names
func createDatabaseAndReadHierarchies() ([]string, error) {
	// Create sample.db
	db, err := sql.Open("sqlite3", "sample.db")
	if err != nil {
		return nil, fmt.Errorf("failed to create database: %v", err)
	}
	defer db.Close()

	// Read and execute the SQL schema file
	sqlFile := "migrations/0003_main_create_hierarchies.sql"
	sqlBytes, err := os.ReadFile(sqlFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read SQL file %s: %v", sqlFile, err)
	}

	// Execute the SQL commands
	_, err = db.Exec(string(sqlBytes))
	if err != nil {
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

	log.Printf("Loaded %d hierarchies from database", len(hierarchies))
	return hierarchies, nil
}
