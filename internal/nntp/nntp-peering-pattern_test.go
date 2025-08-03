package nntp

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseActiveFile reads the active.i2pn2.txt file and extracts newsgroup names
func parseActiveFile(t *testing.T) []string {
	// Use absolute path to active file
	//activeFile := "/home/fed/WORKSPACES/workspace.go-pugleaf/go-pugleaf/active.i2pn2.txt"
	//activeFile := "/home/fed/WORKSPACES/workspace.go-pugleaf/go-pugleaf/active.file.test"
	//activeFile := "/home/fed/WORKSPACES/workspace.go-pugleaf/go-pugleaf/cubenet-nl.active"
	activeFile := "/home/fed/WORKSPACES/workspace.go-pugleaf/go-pugleaf/ninja-nl.active"

	file, err := os.Open(activeFile)
	if err != nil {
		t.Skipf("Skipping test: cannot open active file %s: %v", activeFile, err)
		return nil
	}
	defer file.Close()

	var newsgroups []string
	scanner := bufio.NewScanner(file)

	// Skip header lines until we find the first newsgroup
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "fed@") ||
			strings.HasPrefix(line, "Trying") || strings.HasPrefix(line, "Connected") ||
			strings.HasPrefix(line, "Escape") || strings.HasPrefix(line, "200 ") ||
			strings.HasPrefix(line, "list") || strings.HasPrefix(line, "215 ") {
			continue
		}

		// Parse newsgroup line format: "group high low status"
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			newsgroups = append(newsgroups, fields[0])
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("Error reading active file: %v", err)
	}

	t.Logf("Loaded %d newsgroups from active file", len(newsgroups))
	return newsgroups
}

func TestBasicPatternMatching(t *testing.T) {
	// Test basic wildcard matching
	testCases := []struct {
		newsgroup       string
		sendPatterns    []string
		excludePatterns []string
		rejectPatterns  []string
		expectedAction  string
	}{
		{
			newsgroup:       "comp.lang.go",
			sendPatterns:    []string{"comp.*"},
			excludePatterns: []string{},
			rejectPatterns:  []string{},
			expectedAction:  "send",
		},
		{
			newsgroup:       "alt.sex.stories",
			sendPatterns:    []string{"*"},
			excludePatterns: []string{},
			rejectPatterns:  []string{"@alt.sex.*"},
			expectedAction:  "reject",
		},
		{
			newsgroup:       "comp.test",
			sendPatterns:    []string{"comp.*"},
			excludePatterns: []string{"!*.test"},
			rejectPatterns:  []string{},
			expectedAction:  "exclude",
		},
		{
			newsgroup:       "alt.music",
			sendPatterns:    []string{"comp.*"},
			excludePatterns: []string{},
			rejectPatterns:  []string{},
			expectedAction:  "no-send",
		},
	}

	for _, tc := range testCases {
		result := MatchNewsgroupPatterns(tc.newsgroup, tc.sendPatterns, tc.excludePatterns, tc.rejectPatterns)
		if result.Action != tc.expectedAction {
			t.Errorf("Newsgroup %s: expected action %s, got %s (explanation: %s)",
				tc.newsgroup, tc.expectedAction, result.Action, result.Explanation)
		}
	}
}

func TestDefaultPatterns(t *testing.T) {
	// Test that our default patterns exist and work
	if len(DefaultNoSendPatterns) == 0 {
		t.Error("DefaultNoSendPatterns should not be empty")
	}

	if len(DefaultBinaryExcludePatterns) == 0 {
		t.Error("DefaultBinaryExcludePatterns should not be empty")
	}

	if len(DefaultSexExcludePatterns) == 0 {
		t.Error("DefaultSexExcludePatterns should not be empty")
	}

	t.Logf("DefaultNoSendPatterns: %v", DefaultNoSendPatterns)
	t.Logf("DefaultBinaryExcludePatterns: %v", DefaultBinaryExcludePatterns[:3]) // Show first 3
	t.Logf("DefaultSexExcludePatterns: %v", DefaultSexExcludePatterns)

	// Test some known groups
	testCases := []struct {
		newsgroup   string
		patterns    []string
		shouldMatch bool
		description string
	}{
		{"control.cancel", DefaultNoSendPatterns, true, "control group should be excluded"},
		{"junk.test", DefaultNoSendPatterns, true, "junk group should be excluded"},
		{"comp.lang.go", DefaultNoSendPatterns, false, "comp group should not be excluded"},
		{"alt.sex.stories", DefaultSexExcludePatterns, true, "sex group should be excluded"},
		{"alt.music", DefaultSexExcludePatterns, false, "music group should not be excluded"},
	}

	for _, tc := range testCases {
		// For exclude patterns, we test them as excludePatterns
		result := MatchNewsgroupPatterns(tc.newsgroup, []string{"*"}, tc.patterns, []string{})
		matched := (result.Action == "exclude")

		if matched != tc.shouldMatch {
			t.Errorf("%s: expected match=%v, got %v (action: %s)",
				tc.description, tc.shouldMatch, matched, result.Action)
		}
	}
}

func TestRealNewsgroupsWithDefaultPatterns(t *testing.T) {
	newsgroups := parseActiveFile(t)
	if len(newsgroups) == 0 {
		t.Skip("No newsgroups loaded, skipping test")
	}

	// Test default patterns against real newsgroups
	excludeMatches := 0
	sexMatches := 0
	binaryMatches := 0

	// Sample matches for debugging
	excludeSamples := []string{}
	sexSamples := []string{}
	binarySamples := []string{}

	for _, group := range newsgroups {
		// Test exclude patterns
		result := MatchNewsgroupPatterns(group, []string{"*"}, DefaultNoSendPatterns, []string{})
		if result.Action == "exclude" {
			excludeMatches++
			if len(excludeSamples) < 10 {
				excludeSamples = append(excludeSamples, group+" ("+result.Pattern+")")
			}
		}

		// Test sex patterns (as reject patterns)
		result = MatchNewsgroupPatterns(group, []string{"*"}, []string{}, DefaultSexExcludePatterns)
		if result.Action == "reject" {
			sexMatches++
			if len(sexSamples) < 10 {
				sexSamples = append(sexSamples, group+" ("+result.Pattern+")")
			}
		}

		// Test binary patterns (as reject patterns)
		result = MatchNewsgroupPatterns(group, []string{"*"}, []string{}, DefaultBinaryExcludePatterns)
		if result.Action == "reject" {
			binaryMatches++
			if len(binarySamples) < 10 {
				binarySamples = append(binarySamples, group+" ("+result.Pattern+")")
			}
		}
	}

	t.Logf("Testing %d real newsgroups:", len(newsgroups))
	t.Logf("  DefaultNoSendPatterns excluded: %d groups", excludeMatches)
	t.Logf("  Sample excludes: %v", excludeSamples)
	t.Logf("  DefaultSexExcludePatterns rejected: %d groups", sexMatches)
	t.Logf("  Sample sex rejects: %v", sexSamples)
	t.Logf("  DefaultBinaryExcludePatterns rejected: %d groups", binaryMatches)
	t.Logf("  Sample binary rejects: %v", binarySamples)

	// Basic sanity checks
	if excludeMatches == 0 {
		t.Error("Expected some matches for DefaultNoSendPatterns")
	}
	if sexMatches == 0 {
		t.Error("Expected some matches for DefaultSexExcludePatterns")
	}
}

func TestPatternValidation(t *testing.T) {
	testCases := []struct {
		patterns     []string
		expectErrors bool
	}{
		{[]string{"comp.*", "rec.*", "!*.test"}, false},
		{[]string{"", "comp.*"}, true}, // empty pattern
		{[]string{"comp.**.*"}, true},  // double wildcard
		{[]string{"comp.*", "rec.*"}, false},
	}

	for i, tc := range testCases {
		errors := ValidatePatterns(tc.patterns)
		hasErrors := len(errors) > 0

		if hasErrors != tc.expectErrors {
			t.Errorf("Test case %d: expected errors=%v, got errors=%v (%v)",
				i, tc.expectErrors, hasErrors, errors)
		}
	}
}

func TestPatternHelpers(t *testing.T) {
	// Test GetPatternType
	testCases := []struct {
		pattern      string
		expectedType string
	}{
		{"comp.*", "normal"},
		{"!*.test", "exclude"},
		{"@alt.sex.*", "reject"},
	}

	for _, tc := range testCases {
		result := GetPatternType(tc.pattern)
		if result != tc.expectedType {
			t.Errorf("Pattern %s: expected type %s, got %s",
				tc.pattern, tc.expectedType, result)
		}
	}

	// Test NormalizePattern
	normalizeTests := []struct {
		pattern  string
		expected string
	}{
		{"comp.*", "comp.*"},
		{"!*.test", "*.test"},
		{"@alt.sex.*", "alt.sex.*"},
	}

	for _, tc := range normalizeTests {
		result := NormalizePattern(tc.pattern)
		if result != tc.expected {
			t.Errorf("Pattern %s: expected normalized %s, got %s",
				tc.pattern, tc.expected, result)
		}
	}
}

func TestArticleMatching(t *testing.T) {
	// Test crossposted articles
	testCases := []struct {
		newsgroups      []string
		sendPatterns    []string
		excludePatterns []string
		rejectPatterns  []string
		expectedAction  string
		description     string
	}{
		{
			newsgroups:      []string{"comp.lang.go"},
			sendPatterns:    []string{"comp.*"},
			excludePatterns: []string{},
			rejectPatterns:  []string{},
			expectedAction:  "send",
			description:     "Single allowed group",
		},
		{
			newsgroups:      []string{"comp.lang.go", "rec.humor"},
			sendPatterns:    []string{"comp.*", "rec.*"},
			excludePatterns: []string{},
			rejectPatterns:  []string{},
			expectedAction:  "send",
			description:     "Multiple allowed groups",
		},
		{
			newsgroups:      []string{"comp.lang.go", "alt.sex.stories"},
			sendPatterns:    []string{"*"},
			excludePatterns: []string{},
			rejectPatterns:  []string{"@alt.sex.*"},
			expectedAction:  "reject",
			description:     "One group causes rejection",
		},
		{
			newsgroups:      []string{"alt.music", "alt.cooking"},
			sendPatterns:    []string{"comp.*"},
			excludePatterns: []string{},
			rejectPatterns:  []string{},
			expectedAction:  "no-send",
			description:     "No matching send patterns",
		},
	}

	for _, tc := range testCases {
		result := MatchArticleForPeer(tc.newsgroups, tc.sendPatterns, tc.excludePatterns, tc.rejectPatterns)
		if result.Action != tc.expectedAction {
			t.Errorf("%s: expected action %s, got %s (explanation: %s)",
				tc.description, tc.expectedAction, result.Action, result.Explanation)
		}
	}
}

func TestPerformanceWithRealData(t *testing.T) {
	newsgroups := parseActiveFile(t)
	if len(newsgroups) == 0 {
		t.Skip("No newsgroups loaded, skipping performance test")
	}

	// Test with a realistic peer configuration
	sendPatterns := []string{"*"}
	excludePatterns := DefaultNoSendPatterns
	rejectPatterns := append(DefaultSexExcludePatterns, DefaultBinaryExcludePatterns...)

	matches := 0
	for _, group := range newsgroups {
		result := MatchNewsgroupPatterns(group, sendPatterns, excludePatterns, rejectPatterns)
		if result.Action == "send" {
			matches++
		}
	}

	t.Logf("Performance test completed:")
	t.Logf("  Processed %d newsgroups", len(newsgroups))
	t.Logf("  %d would be sent to peer", matches)
	t.Logf("  %d would be filtered out", len(newsgroups)-matches)

	percentage := float64(matches) / float64(len(newsgroups)) * 100
	t.Logf("  %.1f%% of groups would be sent", percentage)
}

func TestAllDefaultBinaryPatterns(t *testing.T) {
	newsgroups := parseActiveFile(t)
	if len(newsgroups) == 0 {
		t.Skip("No newsgroups loaded, skipping test")
	}

	t.Logf("Testing ALL default binary exclude patterns against %d newsgroups:", len(newsgroups))

	// Create a map to track which groups are still unmatched
	unmatchedGroups := make(map[string]bool)
	for _, group := range newsgroups {
		unmatchedGroups[group] = true
	}

	totalMatches := 0

	// Test each pattern in DefaultBinaryExcludePatterns individually
	for i, pattern := range DefaultBinaryExcludePatterns {
		matches := 0
		samples := []string{}
		matchedByThisPattern := []string{}

		for _, group := range newsgroups {
			result := MatchNewsgroupPatterns(group, []string{"*"}, []string{}, []string{pattern})
			if result.Action == "reject" {
				matches++
				matchedByThisPattern = append(matchedByThisPattern, group)
				if len(samples) < 3 {
					samples = append(samples, group)
				}
			}
		}

		// Remove matched groups from unmatched map
		for _, group := range matchedByThisPattern {
			delete(unmatchedGroups, group)
		}

		t.Logf("Pattern %d: %s -> %d matches", i+1, pattern, matches)
		if len(samples) > 0 {
			t.Logf("    Samples: %v", samples)
		}

		totalMatches += matches
	}

	t.Logf("Total pattern matches: %d (with overlaps)", totalMatches)

	// Convert remaining unmatched groups back to slice for writing
	remainingGroups := make([]string, 0, len(unmatchedGroups))
	for group := range unmatchedGroups {
		remainingGroups = append(remainingGroups, group)
	}

	t.Logf("Groups NOT matched by any binary pattern: %d", len(remainingGroups))
	t.Logf("Binary patterns coverage: %.2f%% (%d out of %d groups blocked)",
		float64(len(newsgroups)-len(remainingGroups))/float64(len(newsgroups))*100,
		len(newsgroups)-len(remainingGroups), len(newsgroups))

	// Write remaining unmatched groups to active.out file
	outputFile := "active.out"
	file, err := os.Create(outputFile)
	if err != nil {
		t.Fatalf("Failed to create output file: %v", err)
	}
	defer file.Close()

	for _, group := range remainingGroups {
		_, err := file.WriteString(group + "\n")
		if err != nil {
			t.Fatalf("Failed to write to output file: %v", err)
		}
	}

	t.Logf("Wrote %d unmatched groups to %s", len(remainingGroups), outputFile)

	// Show some samples of what's NOT being caught
	if len(remainingGroups) > 0 {
		sampleCount := 10
		if len(remainingGroups) < sampleCount {
			sampleCount = len(remainingGroups)
		}
		t.Logf("Sample groups NOT caught by binary patterns: %v", remainingGroups[:sampleCount])
	}
}

func TestSearchUnmatchedForBinaryGroups(t *testing.T) {
	// Read the active.out file to see what we missed
	file, err := os.Open("active.out")
	if err != nil {
		t.Skipf("Cannot open active.out: %v", err)
		return
	}
	defer file.Close()

	var unmatchedGroups []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			unmatchedGroups = append(unmatchedGroups, line)
		}
	}

	t.Logf("Searching %d unmatched groups for potential binary groups...", len(unmatchedGroups))

	// Search for common binary-related keywords
	binaryKeywords := []string{
		"bin", "binary", "binarie", "file", "download", "upload",
		"torrent", "image", "picture", "video", "audio", "game",
		"app", "software", "iso", "zip", "rar", "exe", "pdf",
		"ebook", "book", "comic", "manga", "anime", "tv", "movie",
		"music", "album", "song", "release", "rip", "cam", "ts",
		"hdtv", "bluray", "dvdrip", "webrip", "x264", "x265",
		"720p", "1080p", "4k", "uhd", "codec", "encode",
	}

	potentialBinary := make(map[string][]string)

	for _, keyword := range binaryKeywords {
		matches := []string{}
		for _, group := range unmatchedGroups {
			if strings.Contains(strings.ToLower(group), keyword) {
				matches = append(matches, group)
				if len(matches) >= 10 { // Limit samples
					break
				}
			}
		}
		if len(matches) > 0 {
			potentialBinary[keyword] = matches
		}
	}

	t.Logf("Found potential binary groups by keyword:")
	for keyword, matches := range potentialBinary {
		t.Logf("  %s: %d matches, samples: %v", keyword, len(matches), matches[:min(len(matches), 3)])
	}

	// Also look for suspicious patterns
	suspiciousPatterns := []string{
		".*\\.files.*",    // .files.
		".*\\.data.*",     // .data.
		".*\\.share.*",    // .share.
		".*\\.exchange.*", // .exchange.
		".*alt\\..*",      // alt.* groups we might have missed
		".*test.*",        // test groups
		".*misc.*",        // misc groups might hide binaries
	}

	t.Logf("Checking suspicious patterns:")
	for _, pattern := range suspiciousPatterns {
		matches := 0
		samples := []string{}

		for _, group := range unmatchedGroups {
			if matched, _ := filepath.Match(pattern, group); matched {
				matches++
				if len(samples) < 5 {
					samples = append(samples, group)
				}
			}
		}
		if matches > 0 {
			t.Logf("  %s: %d matches, samples: %v", pattern, matches, samples)
		}
	}
}

func TestAnalyzeUnmatchedGroupsToFiles(t *testing.T) {
	// Read the active.out file to see what we missed
	file, err := os.Open("active.out")
	if err != nil {
		t.Skipf("Cannot open active.out: %v", err)
		return
	}
	defer file.Close()

	var unmatchedGroups []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			unmatchedGroups = append(unmatchedGroups, line)
		}
	}

	t.Logf("Analyzing %d unmatched groups and saving results to files...", len(unmatchedGroups))

	// Search for specific binary-related keywords and save to files
	binaryKeywords := []string{
		"torrent", "download", "upload", "file", "picture", "image",
		"video", "audio", "music", "movie", "tv", "game", "app",
		"software", "iso", "zip", "rar", "exe", "pdf", "ebook",
		"warez", "crack", "serial", "patch", "rip", "cam", "hdtv",
		"anime", "manga", "comic", "album", "song", "bin",
	}

	// Create output files for each keyword
	for _, keyword := range binaryKeywords {
		matches := []string{}
		for _, group := range unmatchedGroups {
			if strings.Contains(strings.ToLower(group), keyword) {
				matches = append(matches, group)
			}
		}

		if len(matches) > 0 {
			filename := "analysis_" + keyword + ".txt"
			outFile, err := os.Create(filename)
			if err != nil {
				t.Logf("Error creating %s: %v", filename, err)
				continue
			}

			for _, match := range matches {
				outFile.WriteString(match + "\n")
			}
			outFile.Close()

			t.Logf("Keyword '%s': %d groups -> %s", keyword, len(matches), filename)
		}
	}

	// Check for specific suspicious patterns
	patterns := map[string]string{
		"alt_sex":         "alt.sex",
		"alt_picture":     "alt.picture",
		"files_groups":    ".files.",
		"download_groups": ".download",
		"share_groups":    ".share",
		"alt_misc":        "alt.*.misc",
	}

	for name, pattern := range patterns {
		matches := []string{}

		for _, group := range unmatchedGroups {
			groupLower := strings.ToLower(group)
			matched := false

			if strings.Contains(pattern, "*") {
				// Handle wildcard patterns like "alt.*.misc"
				parts := strings.Split(pattern, "*")
				if len(parts) == 2 {
					matched = strings.HasPrefix(groupLower, strings.ToLower(parts[0])) &&
						strings.HasSuffix(groupLower, strings.ToLower(parts[1]))
				}
			} else {
				matched = strings.Contains(groupLower, strings.ToLower(pattern))
			}

			if matched {
				matches = append(matches, group)
			}
		}

		if len(matches) > 0 {
			filename := "analysis_pattern_" + name + ".txt"
			outFile, err := os.Create(filename)
			if err != nil {
				t.Logf("Error creating %s: %v", filename, err)
				continue
			}

			for _, match := range matches {
				outFile.WriteString(match + "\n")
			}
			outFile.Close()

			t.Logf("Pattern '%s': %d groups -> %s", pattern, len(matches), filename)
		}
	}

	t.Logf("Analysis complete! Check the analysis_*.txt files to see potential binary groups we missed.")
	t.Logf("You can now manually review these files to see if we need additional patterns.")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
