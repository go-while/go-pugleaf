package utils

import (
	"strings"
)

// ParseReferences splits a References header into a slice of message-IDs
// This function handles various whitespace scenarios and preserves the angle brackets
func ParseReferences(refs string) []string {
	if refs == "" {
		return []string{}
	}

	// Use strings.Fields() for robust whitespace handling (spaces, tabs, newlines)
	fields := strings.Fields(refs)

	var cleanRefs []string
	for _, ref := range fields {
		// Don't trim angle brackets - they're part of the message-ID format
		ref = strings.TrimSpace(ref)
		if ref != "" {
			cleanRefs = append(cleanRefs, ref)
		}
	}

	return cleanRefs
}
