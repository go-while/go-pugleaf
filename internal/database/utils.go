package database

import (
	"crypto/md5"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// createDirIfNotExists creates a directory if it doesn't exist
func createDirIfNotExists(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, 0755)
	}
	return nil
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// MD5Hash generates an MD5 hash for a given input string
func MD5Hash(input string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(input)))
}

// Replace dots and other special characters with underscores
var regexSanitizeGroup1 = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
var regexSanitizeGroup2 = regexp.MustCompile(`_+`)

// sanitizeGroupName converts a newsgroup name to a safe filename
func sanitizeGroupName(groupName string) string {
	sanitized := regexSanitizeGroup1.ReplaceAllString(groupName, "_")

	// Remove multiple consecutive underscores
	sanitized = regexSanitizeGroup2.ReplaceAllString(sanitized, "_")

	// Trim underscores from start and end
	sanitized = strings.Trim(sanitized, "_")

	// Ensure it's not empty
	if sanitized == "" {
		sanitized = "junk"
	}

	return sanitized
}
