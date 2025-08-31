package database

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// createDirIfNotExists creates a directory if it doesn't exist
func createDirIfNotExists(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, 0755)
	}
	return nil
}

// fileExists checks if a file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// MoveFile moves/renames a file
func MoveFile(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func RsyncDIR(oldpath, newpath string) error {
	// Stream rsync output so progress is visible in logs/STDOUT
	args := []string{"-va", "--progress", oldpath, newpath}
	log.Printf("[RSYNC] running: rsync %v", args)

	cmd := exec.Command("rsync", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("rsync stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("rsync stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("rsync start: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Split function that treats both \n and \r as record delimiters
	scanCRorLF := func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		for i, b := range data {
			if b == '\n' || b == '\r' {
				return i + 1, data[:i], nil
			}
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	}

	// Helper to stream lines to log (handles both CR and LF delimited progress)
	stream := func(prefix string, s *bufio.Scanner) {
		defer wg.Done()
		// Increase buffer for long lines from rsync
		buf := make([]byte, 0, 64*1024)
		s.Buffer(buf, 1024*1024)
		s.Split(scanCRorLF)
		for s.Scan() {
			txt := strings.TrimSpace(s.Text())
			if txt == "" {
				continue
			}
			log.Printf("[RSYNC] %s%s", prefix, txt)
		}
	}

	go stream("", bufio.NewScanner(stdout))
	go stream("ERR: ", bufio.NewScanner(stderr))

	// Wait for rsync to finish and for log streams to drain
	waitErr := cmd.Wait()
	wg.Wait()

	if waitErr != nil {
		return fmt.Errorf("rsync failed: %w", waitErr)
	}
	log.Printf("[RSYNC] done")
	return nil
}

// MD5Hash generates an MD5 hash for a given input string
func MD5Hash(input string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(input)))
}

// Replace dots and other special characters with underscores
var regexSanitizeGroup1 = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
var regexSanitizeGroup2 = regexp.MustCompile(`_+`)

// sanitizeGroupName converts a newsgroup name to a safe filename
func SanitizeGroupName(groupName string) string {
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
