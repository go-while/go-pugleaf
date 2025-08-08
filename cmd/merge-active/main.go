// Tool to merge and process NNTP active files
package main

import (
	"bufio"
	"crypto/sha256"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-while/go-pugleaf/internal/processor"
)

// ActiveEntry represents a newsgroup entry from an active file
type ActiveEntry struct {
	GroupName  string
	HighWater  string // Keep as string to preserve original format
	LowWater   string // Keep as string to preserve original format
	Status     string
	Source     string // Which file this entry came from
	LineNumber int
}

// OverviewEntry represents a single overview line
type OverviewEntry struct {
	ArticleNumber string
	Subject       string
	From          string
	Date          string
	MessageID     string
	References    string
	Bytes         string
	Lines         string
	GroupName     string // Added for tracking which group this came from
}

// ActiveMap holds all newsgroup entries by name
type ActiveMap map[string]*ActiveEntry

// OverviewMap holds overview entries by message ID
type OverviewMap map[string]*OverviewEntry

func main() {
	// Disable strict group validation to allow legitimate groups with + and _ characters
	processor.UseStrictGroupValidation = false

	// Command line flags
	var filterMalformed bool
	var processOverview bool
	flag.BoolVar(&filterMalformed, "filter", false, "Filter out malformed group names and write to active.file.new")
	flag.BoolVar(&processOverview, "overview", false, "Process overview files for groups with <100 messages from ../active_files/local-mode.active")
	flag.Parse()

	// Handle filter mode
	if filterMalformed {
		if flag.NArg() < 1 {
			log.Fatalf("Filter mode requires an input file. Usage: %s -filter <input_file>", os.Args[0])
		}
		inputFile := flag.Arg(0)
		if err := filterAndWriteCleanFile(inputFile); err != nil {
			log.Fatalf("Failed to filter file: %v", err)
		}
		return
	}

	// Handle overview processing mode
	if processOverview {
		if err := processOverviewFiles(); err != nil {
			log.Fatalf("Failed to process overview files: %v", err)
		}
		return
	}

	activeDir := "active_files"
	primaryFile := "active.isc"

	// Check if active_files directory exists
	if _, err := os.Stat(activeDir); os.IsNotExist(err) {
		log.Fatalf("Active files directory not found: %s", activeDir)
	}

	primaryPath := filepath.Join(activeDir, primaryFile)

	// Load primary active.isc file
	fmt.Printf("Loading primary active file: %s\n", primaryPath)
	primaryMap, err := loadActiveFile(primaryPath)
	if err != nil {
		log.Fatalf("Failed to load primary active file: %v", err)
	}
	fmt.Printf("Loaded %d newsgroups from primary file\n", len(primaryMap))

	// Find all other *.active files
	activeFiles, err := filepath.Glob(filepath.Join(activeDir, "*.active"))
	if err != nil {
		log.Fatalf("Failed to find active files: %v", err)
	}

	// Also check for other active.* files (not just *.active)
	otherActiveFiles, err := filepath.Glob(filepath.Join(activeDir, "active.*"))
	if err != nil {
		log.Printf("Warning: Failed to find active.* files: %v", err)
	} else {
		// Add non-primary active.* files
		for _, file := range otherActiveFiles {
			base := filepath.Base(file)
			if base != primaryFile {
				activeFiles = append(activeFiles, file)
			}
		}
	}

	fmt.Printf("Found %d additional active files to process\n", len(activeFiles))

	addedCount := 0
	processedFiles := 0

	// Process each additional active file
	for _, filePath := range activeFiles {
		fileName := filepath.Base(filePath)

		// Skip the primary file if it appears in the glob
		if fileName == primaryFile {
			continue
		}

		fmt.Printf("Processing: %s\n", fileName)

		fileMap, err := loadActiveFile(filePath)
		if err != nil {
			log.Printf("Warning: Failed to load %s: %v", fileName, err)
			continue
		}

		processedFiles++
		fileAddedCount := 0

		// Check each entry in this file
		for groupName, entry := range fileMap {
			if _, exists := primaryMap[groupName]; !exists {
				// This group is not in primary map, add it
				primaryMap[groupName] = entry
				fileAddedCount++
				addedCount++
			}
		}

		fmt.Printf("  Added %d new newsgroups from %s (file had %d total)\n",
			fileAddedCount, fileName, len(fileMap))
	}

	// Write merged active file
	outputPath := filepath.Join(activeDir, "active.merged")
	fmt.Printf("Writing merged active file to: %s\n", outputPath)

	if err := writeMergedActiveFile(outputPath, primaryMap); err != nil {
		log.Fatalf("Failed to write merged file: %v", err)
	}

	fmt.Printf("=== MERGE COMPLETE ===\n")
	fmt.Printf("Primary file: %s (%d groups)\n", primaryFile, len(primaryMap)-addedCount)
	fmt.Printf("Processed files: %d\n", processedFiles)
	fmt.Printf("Added newsgroups: %d\n", addedCount)
	fmt.Printf("Total newsgroups: %d\n", len(primaryMap))
	fmt.Printf("Output written to: %s\n", outputPath)
	fmt.Println("Done.")
}

func writeMergedActiveFile(outputPath string, primaryMap ActiveMap) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create merged active file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)

	for _, entry := range primaryMap {
		line := fmt.Sprintf("%s %s %s %s\n", entry.GroupName, entry.HighWater, entry.LowWater, entry.Status)
		if _, err := writer.WriteString(line); err != nil {
			return fmt.Errorf("failed to write line for group '%s': %w", entry.GroupName, err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush merged active file: %w", err)
	}

	fmt.Printf("Merged active file written successfully: %s\n", outputPath)
	return nil
}

// loadActiveFile loads an NNTP active file into a map
// File format: <groupname> <highwater> <lowwater> <status>
func loadActiveFile(filePath string) (ActiveMap, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open active file: %w", err)
	}
	defer file.Close()

	activeMap := make(ActiveMap)
	scanner := bufio.NewScanner(file)
	lineNum := 0

	fmt.Printf("Loading active file: %s...\n", filePath)

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines
		if line == "" {
			continue
		}

		// Parse the line
		entry, err := parseActiveLine(line, lineNum, filePath)
		if err != nil {
			log.Printf("Warning: %v", err)
			continue
		}

		// Check for duplicates
		if existing, exists := activeMap[entry.GroupName]; exists {
			log.Printf("Warning: Duplicate group '%s' found at line %d (previous at line %d)",
				entry.GroupName, lineNum, existing.LineNumber)
			// Keep the later entry
		}

		activeMap[entry.GroupName] = entry

		// Progress logging every 10000 lines
		if lineNum%10000 == 0 {
			fmt.Printf("Processed %d lines, loaded %d unique groups...\n", lineNum, len(activeMap))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	fmt.Printf("Finished loading: processed %d lines, loaded %d unique newsgroups\n", lineNum, len(activeMap))
	return activeMap, nil
}

// parseActiveLine parses a single line from an active file
func parseActiveLine(line string, lineNum int, source string) (*ActiveEntry, error) {
	// Split by whitespace - format: groupname highwater lowwater status
	parts := strings.Fields(line)
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid active line format at line %d: %s", lineNum, line)
	}

	groupName := strings.TrimSpace(parts[0])
	highWater := strings.TrimSpace(parts[1])
	lowWater := strings.TrimSpace(parts[2])
	status := strings.TrimSpace(parts[3])

	// Validate group name
	if groupName == "" {
		return nil, fmt.Errorf("empty group name at line %d", lineNum)
	}

	// Validate status (should be single character: y, n, m, j, x, =...)
	if len(status) != 1 {
		return nil, fmt.Errorf("invalid status '%s' at line %d (should be single character)", status, lineNum)
	}

	return &ActiveEntry{
		GroupName:  groupName,
		HighWater:  highWater,
		LowWater:   lowWater,
		Status:     status,
		LineNumber: lineNum,
		Source:     source,
	}, nil
}

// filterAndWriteCleanFile filters out malformed group names and writes a clean active file
// filterAndWriteCleanFile filters malformed group names from a single input file
func filterAndWriteCleanFile(inputFile string) error {
	fmt.Printf("=== FILTERING MALFORMED GROUP NAMES ===\n")
	fmt.Printf("Input file: %s\n", inputFile)

	// Load the input file
	activeMap, err := loadActiveFile(inputFile)
	if err != nil {
		return fmt.Errorf("failed to load input file: %w", err)
	}

	fmt.Printf("Starting with %d newsgroups\n", len(activeMap))

	validGroups := make(ActiveMap)
	invalidGroups := make(ActiveMap)
	invalidCount := 0

	// Filter groups using IsValidGroupName
	for groupName, entry := range activeMap {
		if processor.IsValidGroupName(groupName) {
			validGroups[groupName] = entry
		} else {
			fmt.Printf("Filtered out malformed group name: '%s' (from %s:%d)\n",
				groupName, entry.Source, entry.LineNumber)
			invalidGroups[groupName] = entry
			invalidCount++
		}
	}

	fmt.Printf("Filtered out %d malformed group names\n", invalidCount)

	// Create output file names
	outputPath := inputFile + ".new"
	filteredPath := inputFile + ".filtered"
	fmt.Printf("Writing %d valid groups to: %s\n", len(validGroups), outputPath)
	fmt.Printf("Writing %d filtered groups to: %s\n", len(invalidGroups), filteredPath)

	// Write the clean active file
	if err := writeCleanActiveFile(outputPath, validGroups); err != nil {
		return fmt.Errorf("failed to write clean active file: %w", err)
	}

	// Write the filtered groups file
	if len(invalidGroups) > 0 {
		if err := writeFilteredGroupsFile(filteredPath, invalidGroups); err != nil {
			return fmt.Errorf("failed to write filtered groups file: %w", err)
		}
	}

	// Calculate statistics
	printGroupStatistics(validGroups)

	// Calculate statistics for filtered groups
	if len(invalidGroups) > 0 {
		fmt.Printf("\n=== FILTERED GROUPS STATISTICS ===\n")
		printFilteredGroupStatistics(invalidGroups)
	}

	fmt.Printf("=== FILTERING COMPLETE ===\n")
	fmt.Printf("Original groups: %d\n", len(activeMap))
	fmt.Printf("Valid groups: %d\n", len(validGroups))
	fmt.Printf("Filtered out: %d\n", invalidCount)
	fmt.Printf("Clean file written to: %s\n", outputPath)
	if len(invalidGroups) > 0 {
		fmt.Printf("Filtered groups written to: %s\n", filteredPath)
	}
	fmt.Println("Done.")

	return nil
}

// writeCleanActiveFile writes the filtered active file in standard format
func writeCleanActiveFile(outputPath string, activeMap ActiveMap) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create clean active file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)

	// Write each valid group entry
	for _, entry := range activeMap {
		line := fmt.Sprintf("%s %s %s %s\n", entry.GroupName, entry.HighWater, entry.LowWater, entry.Status)
		if _, err := writer.WriteString(line); err != nil {
			return fmt.Errorf("failed to write line for group '%s': %w", entry.GroupName, err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush clean active file: %w", err)
	}

	fmt.Printf("Clean active file written successfully: %s\n", outputPath)
	return nil
}

// writeFilteredGroupsFile writes the filtered (invalid) groups to a file for reference
func writeFilteredGroupsFile(outputPath string, invalidGroups ActiveMap) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create filtered groups file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)

	// Write each filtered group entry with source information
	for _, entry := range invalidGroups {
		line := fmt.Sprintf("%s %s %s %s\n",
			entry.GroupName, entry.HighWater, entry.LowWater, entry.Status)
		if _, err := writer.WriteString(line); err != nil {
			return fmt.Errorf("failed to write line for group '%s': %w", entry.GroupName, err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush filtered groups file: %w", err)
	}

	fmt.Printf("Filtered groups file written successfully: %s\n", outputPath)
	return nil
}

// printGroupStatistics prints statistics about message counts in newsgroups
func printGroupStatistics(activeMap ActiveMap) {
	if len(activeMap) == 0 {
		fmt.Println("No newsgroups to analyze")
		return
	}

	var totalMessages int64
	var groupsLessThan10, groupsLessThan100, groupsLessThan1000 int
	var messagesLessThan10, messagesLessThan100, messagesLessThan1000, messagesLarger int64
	var parseErrors int

	for _, entry := range activeMap {
		// Calculate message count: highwater - lowwater + 1
		highWater, err1 := strconv.ParseInt(entry.HighWater, 10, 64)
		lowWater, err2 := strconv.ParseInt(entry.LowWater, 10, 64)

		if err1 != nil || err2 != nil {
			parseErrors++
			continue
		}

		messageCount := highWater - lowWater + 1
		if messageCount < 0 {
			messageCount = 0 // Handle invalid ranges
		}

		totalMessages += messageCount

		// Count groups and messages by ranges
		if messageCount < 10 {
			groupsLessThan10++
			messagesLessThan10 += messageCount
		} else if messageCount < 100 {
			groupsLessThan100++
			messagesLessThan100 += messageCount
		} else if messageCount < 1000 {
			groupsLessThan1000++
			messagesLessThan1000 += messageCount
		} else {
			messagesLarger += messageCount
		}
	}

	totalGroups := len(activeMap)
	groupsLarger := totalGroups - groupsLessThan10 - groupsLessThan100 - groupsLessThan1000 - parseErrors

	fmt.Printf("=== GROUP STATISTICS ===\n")
	fmt.Printf("Total groups analyzed: %d\n", totalGroups)
	fmt.Printf("Total estimated messages: %d\n", totalMessages)
	if parseErrors > 0 {
		fmt.Printf("Parse errors (skipped): %d\n", parseErrors)
	}
	fmt.Printf("Groups with <10 messages: %d (%.1f%%) - %d messages (%.1f%%)\n",
		groupsLessThan10, float64(groupsLessThan10)/float64(totalGroups)*100,
		messagesLessThan10, float64(messagesLessThan10)/float64(totalMessages)*100)
	fmt.Printf("Groups with <100 messages: %d (%.1f%%) - %d messages (%.1f%%)\n",
		groupsLessThan100, float64(groupsLessThan100)/float64(totalGroups)*100,
		messagesLessThan100, float64(messagesLessThan100)/float64(totalMessages)*100)
	fmt.Printf("Groups with <1000 messages: %d (%.1f%%) - %d messages (%.1f%%)\n",
		groupsLessThan1000, float64(groupsLessThan1000)/float64(totalGroups)*100,
		messagesLessThan1000, float64(messagesLessThan1000)/float64(totalMessages)*100)
	fmt.Printf("Groups with >=1000 messages: %d (%.1f%%) - %d messages (%.1f%%)\n",
		groupsLarger, float64(groupsLarger)/float64(totalGroups)*100,
		messagesLarger, float64(messagesLarger)/float64(totalMessages)*100)
}

// printFilteredGroupStatistics prints statistics about message counts in filtered (malformed) newsgroups
func printFilteredGroupStatistics(activeMap ActiveMap) {
	if len(activeMap) == 0 {
		fmt.Println("No filtered newsgroups to analyze")
		return
	}

	var totalMessages int64
	var groupsLessThan10, groupsLessThan100, groupsLessThan1000 int
	var messagesLessThan10, messagesLessThan100, messagesLessThan1000, messagesLarger int64
	var parseErrors int

	for _, entry := range activeMap {
		// Calculate message count: highwater - lowwater + 1
		highWater, err1 := strconv.ParseInt(entry.HighWater, 10, 64)
		lowWater, err2 := strconv.ParseInt(entry.LowWater, 10, 64)

		if err1 != nil || err2 != nil {
			parseErrors++
			continue
		}

		messageCount := highWater - lowWater + 1
		if messageCount < 0 {
			messageCount = 0 // Handle invalid ranges
		}

		totalMessages += messageCount

		// Count groups and messages by ranges
		if messageCount < 10 {
			groupsLessThan10++
			messagesLessThan10 += messageCount
		} else if messageCount < 100 {
			groupsLessThan100++
			messagesLessThan100 += messageCount
		} else if messageCount < 1000 {
			groupsLessThan1000++
			messagesLessThan1000 += messageCount
		} else {
			messagesLarger += messageCount
		}
	}

	totalGroups := len(activeMap)
	groupsLarger := totalGroups - groupsLessThan10 - groupsLessThan100 - groupsLessThan1000 - parseErrors

	fmt.Printf("Total filtered groups analyzed: %d\n", totalGroups)
	fmt.Printf("Total estimated messages in filtered groups: %d\n", totalMessages)
	if parseErrors > 0 {
		fmt.Printf("Parse errors (skipped): %d\n", parseErrors)
	}
	fmt.Printf("Filtered groups with <10 messages: %d (%.1f%%) - %d messages (%.1f%%)\n",
		groupsLessThan10, float64(groupsLessThan10)/float64(totalGroups)*100,
		messagesLessThan10, float64(messagesLessThan10)/float64(totalMessages)*100)
	fmt.Printf("Filtered groups with <100 messages: %d (%.1f%%) - %d messages (%.1f%%)\n",
		groupsLessThan100, float64(groupsLessThan100)/float64(totalGroups)*100,
		messagesLessThan100, float64(messagesLessThan100)/float64(totalMessages)*100)
	fmt.Printf("Filtered groups with <1000 messages: %d (%.1f%%) - %d messages (%.1f%%)\n",
		groupsLessThan1000, float64(groupsLessThan1000)/float64(totalGroups)*100,
		messagesLessThan1000, float64(messagesLessThan1000)/float64(totalMessages)*100)
	fmt.Printf("Filtered groups with >=1000 messages: %d (%.1f%%) - %d messages (%.1f%%)\n",
		groupsLarger, float64(groupsLarger)/float64(totalGroups)*100,
		messagesLarger, float64(messagesLarger)/float64(totalMessages)*100)
}

// processOverviewFiles processes overview files for groups with <100 messages
func processOverviewFiles() error {
	fmt.Printf("=== PROCESSING OVERVIEW FILES ===\n")

	// Load the local-mode.active file
	activeFile := "../active_files/local-mode.active"
	fmt.Printf("Loading active file: %s\n", activeFile)

	activeMap, err := loadActiveFile(activeFile)
	if err != nil {
		return fmt.Errorf("failed to load active file: %w", err)
	}

	fmt.Printf("Loaded %d newsgroups from active file\n", len(activeMap))

	// Filter groups with <100 messages
	smallGroups := make(ActiveMap)
	for groupName, entry := range activeMap {
		highWater, err1 := strconv.ParseInt(entry.HighWater, 10, 64)
		lowWater, err2 := strconv.ParseInt(entry.LowWater, 10, 64)

		if err1 != nil || err2 != nil {
			continue // Skip groups with parse errors
		}

		messageCount := highWater - lowWater + 1
		if messageCount < 100 {
			smallGroups[groupName] = entry
		}
	}

	fmt.Printf("Found %d groups with <100 messages\n", len(smallGroups))

	// Process overview files
	overviewDir := "/tank0/OV/overview/nntp-server-new"
	if _, err := os.Stat(overviewDir); os.IsNotExist(err) {
		return fmt.Errorf("overview directory not found: %s", overviewDir)
	}

	overviewMap := make(OverviewMap)
	processedGroups := 0
	totalOverviewEntries := 0

	for groupName := range smallGroups {
		// Generate SHA256 hash of group name
		hash := sha256.Sum256([]byte(groupName))
		hashStr := fmt.Sprintf("%x", hash)

		overviewFile := filepath.Join(overviewDir, hashStr+".overview")

		// Check if overview file exists
		if _, err := os.Stat(overviewFile); os.IsNotExist(err) {
			return fmt.Errorf("not found overview ng:'%s' file:'%s'", groupName, overviewFile)
		}

		// Load overview file
		entries, err := loadOverviewFile(overviewFile, groupName)
		if err != nil {
			fmt.Printf("Warning: Failed to load overview file for group %s: %v\n", groupName, err)
			continue
		}

		// Add entries to overview map
		for _, entry := range entries {
			overviewMap[entry.MessageID] = entry
		}

		processedGroups++
		totalOverviewEntries += len(entries)

		if processedGroups%100 == 0 {
			fmt.Printf("Processed %d groups, loaded %d overview entries...\n", processedGroups, totalOverviewEntries)
		}
	}

	fmt.Printf("=== OVERVIEW PROCESSING COMPLETE ===\n")
	fmt.Printf("Groups with <100 messages: %d\n", len(smallGroups))
	fmt.Printf("Overview files processed: %d\n", processedGroups)
	fmt.Printf("Total overview entries loaded: %d\n", totalOverviewEntries)
	fmt.Printf("Unique message IDs: %d\n", len(overviewMap))

	// Analyze for duplicates
	analyzeDuplicates(overviewMap)

	return nil
}

// loadOverviewFile loads an overview file and parses the entries
func loadOverviewFile(filePath, groupName string) ([]*OverviewEntry, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open overview file: %w", err)
	}
	defer file.Close()

	var entries []*OverviewEntry
	scanner := bufio.NewScanner(file)

	// Increase buffer size to handle very long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max token size

	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip the first line (header)
		if lineNum == 1 {
			continue
		}

		// Stop if we hit NULL bytes (footer section)
		if strings.Contains(line, "\x00") {
			break
		}

		line = strings.TrimSpace(line)

		// Skip empty lines
		if line == "" {
			continue
		}

		entry, err := parseOverviewLine(line, groupName, lineNum)
		if err != nil {
			continue // Skip malformed lines
		}

		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading overview file: %w", err)
	}

	return entries, nil
}

// parseOverviewLine parses a single overview line
// Format: article_number<TAB>subject<TAB>from<TAB>date<TAB>message-id<TAB>references<TAB>bytes<TAB>lines
func parseOverviewLine(line, groupName string, lineNum int) (*OverviewEntry, error) {
	parts := strings.Split(line, "\t")
	if len(parts) < 8 {
		return nil, fmt.Errorf("invalid overview line format at line %d: expected 8 fields, got %d", lineNum, len(parts))
	}

	return &OverviewEntry{
		ArticleNumber: strings.TrimSpace(parts[0]),
		Subject:       strings.TrimSpace(parts[1]),
		From:          strings.TrimSpace(parts[2]),
		Date:          strings.TrimSpace(parts[3]),
		MessageID:     strings.TrimSpace(parts[4]),
		References:    strings.TrimSpace(parts[5]),
		Bytes:         strings.TrimSpace(parts[6]),
		Lines:         strings.TrimSpace(parts[7]),
		GroupName:     groupName,
	}, nil
}

// analyzeDuplicates analyzes the overview map for duplicate message IDs across groups
func analyzeDuplicates(overviewMap OverviewMap) {
	fmt.Printf("\n=== DUPLICATE ANALYSIS ===\n")

	// Count message IDs that appear in multiple groups
	messageIDGroups := make(map[string][]string)

	for messageID, entry := range overviewMap {
		messageIDGroups[messageID] = append(messageIDGroups[messageID], entry.GroupName)
	}

	duplicateCount := 0
	crossPostCount := 0

	for messageID, groups := range messageIDGroups {
		if len(groups) > 1 {
			duplicateCount++
			// Remove duplicates from groups slice
			uniqueGroups := make(map[string]bool)
			for _, group := range groups {
				uniqueGroups[group] = true
			}
			if len(uniqueGroups) > 1 {
				crossPostCount++
				if crossPostCount <= 10 { // Show first 10 examples
					fmt.Printf("Cross-posted message: %s in groups: %v\n", messageID, keys(uniqueGroups))
				}
			}
		}
	}

	fmt.Printf("Total unique message IDs: %d\n", len(messageIDGroups))
	fmt.Printf("Duplicate message IDs: %d\n", duplicateCount)
	fmt.Printf("Cross-posted messages: %d\n", crossPostCount)

	if crossPostCount > 10 {
		fmt.Printf("... and %d more cross-posted messages\n", crossPostCount-10)
	}
}

// keys returns the keys of a map as a slice
func keys(m map[string]bool) []string {
	var result []string
	for k := range m {
		result = append(result, k)
	}
	return result
}

/*
// printStats prints statistics about the loaded active map
func printStats(activeMap ActiveMap) {
	if len(activeMap) == 0 {
		log.Println("No newsgroups loaded")
		return
	}

	statusCounts := make(map[string]int)
	totalArticles := 0
	maxArticles := 0
	var largestGroup string

	for _, entry := range activeMap {
		statusCounts[entry.Status]++
	}

	log.Printf("=== Active File Statistics ===")
	log.Printf("Total newsgroups: %d", len(activeMap))
	log.Printf("Total estimated articles: %d", totalArticles)
	if largestGroup != "" {
		log.Printf("Largest group: %s (%d articles)", largestGroup, maxArticles)
	}

	log.Printf("Status distribution:")
	for status, count := range statusCounts {
		percentage := float64(count) / float64(len(activeMap)) * 100
		statusDesc := getStatusDescription(status)
		log.Printf("  %s (%s): %d groups (%.1f%%)", status, statusDesc, count, percentage)
	}
}

// getStatusDescription returns a human-readable description of the status code
func getStatusDescription(status string) string {
	switch status {
	case "y":
		return "posting allowed"
	case "n":
		return "no posting allowed"
	case "m":
		return "moderated"
	case "j":
		return "articles filed in junk group"
	case "x":
		return "articles not filed"
	case "=":
		return "aliased group"
	default:
		return "unknown status"
	}
}
*/
