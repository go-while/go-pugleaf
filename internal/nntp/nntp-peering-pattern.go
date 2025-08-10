package nntp

import (
	"strings"
)

// PatternMatchResult represents the result of pattern matching
type PatternMatchResult struct {
	Matched     bool   // Whether any pattern matched
	Action      string // "send", "exclude", "reject"
	Pattern     string // The specific pattern that matched
	Explanation string // Human-readable explanation
}

// MatchNewsgroupPatterns evaluates newsgroup patterns according to INN2 rules
// Returns the final decision and which pattern caused it
func MatchNewsgroupPatterns(newsgroup string, sendPatterns, excludePatterns, rejectPatterns []string) PatternMatchResult {
	// First check reject patterns (@patterns) - these override everything
	for _, pattern := range rejectPatterns {
		if matchSinglePattern(newsgroup, pattern) {
			return PatternMatchResult{
				Matched:     true,
				Action:      "reject",
				Pattern:     pattern,
				Explanation: "Article rejected: " + pattern,
			}
		}
	}

	// Check if newsgroup matches send patterns
	sendMatch := false
	sendPattern := ""
	for _, pattern := range sendPatterns {
		if matchSinglePattern(newsgroup, pattern) {
			sendMatch = true
			sendPattern = pattern
			break
		}
	}

	if !sendMatch {
		return PatternMatchResult{
			Matched:     false,
			Action:      "no-send",
			Pattern:     "",
			Explanation: "Newsgroup does not match any send patterns",
		}
	}

	// Check exclude patterns (!patterns) - these exclude from send but allow crossposting
	for _, pattern := range excludePatterns {
		if matchSinglePattern(newsgroup, pattern) {
			return PatternMatchResult{
				Matched:     true,
				Action:      "exclude",
				Pattern:     pattern,
				Explanation: "Newsgroup excluded from send: " + pattern,
			}
		}
	}

	// If we get here, the newsgroup should be sent
	// Newsgroup matches send pattern and is not excluded
	return PatternMatchResult{
		Matched:     true,
		Action:      "send",
		Pattern:     sendPattern,
		Explanation: "OK",
	}
}

// MatchArticleForPeer determines if an article should be sent to a peer
// Takes into account all newsgroups in the article (crossposting)
func MatchArticleForPeer(newsgroups []string, sendPatterns, excludePatterns, rejectPatterns []string) PatternMatchResult {
	// Check reject patterns first - if ANY newsgroup matches @pattern, reject entire article
	for _, newsgroup := range newsgroups {
		for _, pattern := range rejectPatterns {
			if matchSinglePattern(newsgroup, pattern) {
				return PatternMatchResult{
					Matched:     true,
					Action:      "reject",
					Pattern:     pattern,
					Explanation: "Article rejected due to newsgroup: " + newsgroup,
				}
			}
		}
	}

	// For crossposted articles, check if at least one newsgroup should be sent
	validNewsgroups := []string{}
	excludedNewsgroups := []string{}

	for _, newsgroup := range newsgroups {
		result := MatchNewsgroupPatterns(newsgroup, sendPatterns, excludePatterns, rejectPatterns)

		switch result.Action {
		case "send":
			validNewsgroups = append(validNewsgroups, newsgroup)
		case "exclude":
			excludedNewsgroups = append(excludedNewsgroups, newsgroup)
		}
	}

	// If at least one newsgroup should be sent, send the article
	if len(validNewsgroups) > 0 {
		return PatternMatchResult{
			Matched:     true,
			Action:      "send",
			Pattern:     "",
			Explanation: "Article has valid newsgroups for sending",
		}
	}

	// No valid newsgroups found
	if len(excludedNewsgroups) > 0 {
		return PatternMatchResult{
			Matched:     false,
			Action:      "exclude",
			Pattern:     "",
			Explanation: "All matching newsgroups are excluded",
		}
	}

	return PatternMatchResult{
		Matched:     false,
		Action:      "no-send",
		Pattern:     "",
		Explanation: "No newsgroups match send patterns",
	}
}

// matchSinglePattern matches a newsgroup against a single pattern
// Supports INN2 pattern syntax: wildcards (*), exclusions (!), rejections (@)
func matchSinglePattern(newsgroup, pattern string) bool {
	// Handle special pattern prefixes
	if strings.HasPrefix(pattern, "!") {
		// Exclusion pattern - remove ! and match normally
		return matchWildcard(newsgroup, pattern[1:])
	}

	if strings.HasPrefix(pattern, "@") {
		// Rejection pattern - remove @ and match normally
		return matchWildcard(newsgroup, pattern[1:])
	}

	// Regular pattern
	return matchWildcard(newsgroup, pattern)
}

// matchWildcard performs wildcard matching similar to INN2
// Supports * for any characters and ? for single character
func matchWildcard(text, pattern string) bool {
	return matchWildcardRecursive(text, pattern, 0, 0)
}

// matchWildcardRecursive is the recursive implementation of wildcard matching
func matchWildcardRecursive(text, pattern string, textIdx, patternIdx int) bool {
	// If we've reached the end of both strings, it's a match
	if patternIdx == len(pattern) && textIdx == len(text) {
		return true
	}

	// If we've reached the end of pattern but not text, no match
	if patternIdx == len(pattern) {
		return false
	}

	// If current pattern character is '*'
	if pattern[patternIdx] == '*' {
		// Try matching the rest of the pattern with current position in text
		for i := textIdx; i <= len(text); i++ {
			if matchWildcardRecursive(text, pattern, i, patternIdx+1) {
				return true
			}
		}
		return false
	}

	// If we've reached the end of text but not pattern, no match (unless pattern is all *)
	if textIdx == len(text) {
		// Check if remaining pattern is all '*'
		for i := patternIdx; i < len(pattern); i++ {
			if pattern[i] != '*' {
				return false
			}
		}
		return true
	}

	// If current pattern character is '?' or matches current text character
	if pattern[patternIdx] == '?' || pattern[patternIdx] == text[textIdx] {
		return matchWildcardRecursive(text, pattern, textIdx+1, patternIdx+1)
	}

	// No match
	return false
}

// ValidatePatterns validates a list of patterns for syntax errors
func ValidatePatterns(patterns []string) []string {
	var errors []string

	for _, pattern := range patterns {
		if pattern == "" {
			errors = append(errors, "Empty pattern not allowed")
			continue
		}

		// Check for invalid characters or malformed patterns
		if strings.Contains(pattern, "**") {
			errors = append(errors, "Double wildcards (**) not supported in pattern: "+pattern)
		}

		// Additional validation can be added here
	}

	return errors
}

// GetPatternType returns the type of pattern (!exclude, @reject, or normal)
func GetPatternType(pattern string) string {
	if strings.HasPrefix(pattern, "!") {
		return "exclude"
	}
	if strings.HasPrefix(pattern, "@") {
		return "reject"
	}
	return "normal"
}

// NormalizePattern removes special prefixes and returns the core pattern
func NormalizePattern(pattern string) string {
	if strings.HasPrefix(pattern, "!") || strings.HasPrefix(pattern, "@") {
		return pattern[1:]
	}
	return pattern
}
