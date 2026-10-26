package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <active_files_dir>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s active_files/isc\n", os.Args[0])
		os.Exit(1)
	}

	activeFilesDir := os.Args[1]

	descriptions, err := mergeDescriptionFiles(activeFilesDir)
	if err != nil {
		log.Fatalf("%v", err)
	}

	// Write merged descriptions to output file
	outputFile := filepath.Join(activeFilesDir, "newsgroups.descriptions")
	if err := writeDescriptions(outputFile, descriptions); err != nil {
		log.Fatalf("Error writing output file: %v", err)
	}

	log.Printf("Successfully wrote %d newsgroup descriptions to %s", len(descriptions), outputFile)
}

// mergeDescriptionFiles reads newsgroups.isc.latest first and then the
// newsgroups.isc.year.* files newest first. The first file that has a group wins,
// so a newer description is never replaced by an older one.
func mergeDescriptionFiles(activeFilesDir string) (map[string]string, error) {
	// Initialize the newsgroup descriptions map
	descriptions := make(map[string]string)

	// Read the latest newsgroups file first
	latestFile := filepath.Join(activeFilesDir, "newsgroups.isc.latest")
	if err := readNewsgroupsFile(latestFile, descriptions); err != nil {
		log.Printf("Warning: Could not read %s: %v", latestFile, err)
	} else {
		log.Printf("Read %d descriptions from %s", len(descriptions), latestFile)
	}

	// Read all year files and merge missing groups
	yearFiles, err := filepath.Glob(filepath.Join(activeFilesDir, "newsgroups.isc.year.*"))
	if err != nil {
		return nil, fmt.Errorf("error finding year files: %w", err)
	}
	// Newest year first: Glob sorts ascending, and the first file with a group wins.
	sort.Sort(sort.Reverse(sort.StringSlice(yearFiles)))

	for _, yearFile := range yearFiles {
		initialCount := len(descriptions)
		if err := readNewsgroupsFile(yearFile, descriptions); err != nil {
			log.Printf("Warning: Could not read %s: %v", yearFile, err)
			continue
		}
		addedCount := len(descriptions) - initialCount
		log.Printf("Read %s: added %d new descriptions (total: %d)",
			filepath.Base(yearFile), addedCount, len(descriptions))
	}
	return descriptions, nil
}

// readNewsgroupsFile reads a newsgroups file and adds entries to the descriptions map
// Only adds entries that don't already exist in the map
func readNewsgroupsFile(filename string, descriptions map[string]string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse the line: newsgroup\t\tdescription
		// The format appears to use tabs as separators
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			// Try splitting by multiple spaces as fallback
			fields := strings.Fields(line)
			if len(fields) < 2 {
				log.Printf("Warning: Skipping malformed line %d in %s: %s", lineNum, filename, line)
				continue
			}
			// First field is newsgroup, rest is description
			newsgroup := fields[0]
			description := strings.Join(fields[1:], " ")

			// Only add if not already present
			if _, exists := descriptions[newsgroup]; !exists {
				descriptions[newsgroup] = description
			}
			continue
		}

		// Standard tab-separated format
		newsgroup := strings.TrimSpace(parts[0])
		description := strings.TrimSpace(strings.Join(parts[1:], "\t"))

		// Skip empty newsgroup names
		if newsgroup == "" {
			continue
		}

		// Only add if not already present (latest file takes precedence)
		if _, exists := descriptions[newsgroup]; !exists {
			descriptions[newsgroup] = description
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading file: %v", err)
	}

	return nil
}

// writeDescriptions writes the merged descriptions to an output file
// The output is sorted by newsgroup name for consistency.
// Write, flush and close errors are returned, so a full disk does not leave a
// silently truncated file behind a success message.
func writeDescriptions(filename string, descriptions map[string]string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}

	// Sort newsgroup names for consistent output
	newsgroups := make([]string, 0, len(descriptions))
	for newsgroup := range descriptions {
		newsgroups = append(newsgroups, newsgroup)
	}
	sort.Strings(newsgroups)

	writer := bufio.NewWriter(file)

	// Write each newsgroup and description
	for _, newsgroup := range newsgroups {
		description := descriptions[newsgroup]
		// Use tab separation to match input format
		if _, err := fmt.Fprintf(writer, "%s\t%s\n", newsgroup, description); err != nil {
			_ = file.Close()
			return fmt.Errorf("write %s: %w", filename, err)
		}
	}

	if err := writer.Flush(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush %s: %w", filename, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", filename, err)
	}
	return nil
}
