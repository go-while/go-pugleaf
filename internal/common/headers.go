// Package common provides shared utilities for go-pugleaf
package common

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/go-while/go-pugleaf/internal/models"
)

// IgnoreHeadersMap is a map version of IgnoreHeaders for fast lookup
var IgnoreHeadersMap = map[string]bool{
	"Message-ID": true,
	"Subject":    true,
	"From":       true,
	"Date":       true,
	"References": true,
	"Path":       true,
	"Xref":       true,
	"X-Ref":      true,
}

// isRFC822Compliant checks if a date string is RFC 822/1123 compliant for Usenet
func isRFC822Compliant(dateStr string) bool {
	// Try to parse with common RFC formats used in Usenet
	formats := []string{
		time.RFC1123,                     // "Mon, 02 Jan 2006 15:04:05 MST"
		time.RFC1123Z,                    // "Mon, 02 Jan 2006 15:04:05 -0700"
		time.RFC822,                      // "02 Jan 06 15:04 MST"
		time.RFC822Z,                     // "02 Jan 06 15:04 -0700"
		"Mon, 2 Jan 2006 15:04:05 MST",   // Single digit day
		"Mon, 2 Jan 2006 15:04:05 -0700", // Single digit day with timezone
	}

	for _, format := range formats {
		if _, err := time.Parse(format, dateStr); err == nil {
			return true
		}
	}
	return false
}

// ReconstructHeaders reconstructs the header lines from an article for transmission
func ReconstructHeaders(article *models.Article) ([]string, error) {
	var headers []string

	// Add basic headers that we know about
	if article.MessageID == "" {
		return nil, fmt.Errorf("article missing Message-ID")
	}
	if article.Subject == "" {
		return nil, fmt.Errorf("article missing Subject")
	}
	if article.FromHeader == "" {
		return nil, fmt.Errorf("article missing From header")
	}

	// Check if DateString is RFC Usenet compliant, use DateSent if not
	var dateHeader string
	if article.DateString != "" {
		// Check if DateString is RFC-compliant by trying to parse it
		if isRFC822Compliant(article.DateString) {
			dateHeader = article.DateString
		} else {
			// DateString is not RFC compliant, use DateSent instead
			if !article.DateSent.IsZero() {
				dateHeader = article.DateSent.UTC().Format(time.RFC1123)
				log.Printf("Using DateSent instead of non-compliant DateString for article %s", article.MessageID)
			} else {
				return nil, fmt.Errorf("article has non-compliant DateString and zero DateSent")
			}
		}
	} else {
		// No DateString, try DateSent
		if !article.DateSent.IsZero() {
			dateHeader = article.DateSent.UTC().Format(time.RFC1123)
		} else {
			return nil, fmt.Errorf("article missing Date header (both DateString and DateSent are empty)")
		}
	}

	if article.References == "" {
		return nil, fmt.Errorf("article missing References header")
	}
	if article.Path == "" {
		return nil, fmt.Errorf("article missing Path header")
	}
	headers = append(headers, "Message-ID: "+article.MessageID)
	headers = append(headers, "Subject: "+article.Subject)
	headers = append(headers, "From: "+article.FromHeader)
	headers = append(headers, "Date: "+dateHeader)
	headers = append(headers, "References: "+article.References)
	headers = append(headers, "Path: "+article.Path)
	moreHeaders := strings.Split(article.HeadersJSON, "\n")
	ignoreLine := false
	isSpacedLine := false
	ignoredLines := 0
	headersMap := make(map[string]bool)

	for i, headerLine := range moreHeaders {
		if len(headerLine) == 0 {
			log.Printf("Empty headerline=%d in msgId='%s'", i, article.MessageID)
			continue
		}
		isSpacedLine = strings.HasPrefix(headerLine, " ") || strings.HasPrefix(headerLine, "\t")
		if isSpacedLine && ignoreLine {
			ignoredLines++
			continue
		} else {
			ignoreLine = false
		}
		if !isSpacedLine {
			// check if first char is lowercase
			if unicode.IsLower(rune(headerLine[0])) {
				log.Printf("Lowercase header: '%s' line=%d in msgId='%s'", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue
			}
			header := strings.SplitN(headerLine, ":", 2)[0]
			if len(header) == 0 {
				log.Printf("Invalid header: '%s' line=%d in msgId='%s'", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue
			}
			if IgnoreHeadersMap[header] {
				ignoreLine = true
				continue
			}
			if headersMap[header] {
				log.Printf("Duplicate header: '%s' line=%d in msgId='%s'", headerLine, i, article.MessageID)
				ignoreLine = true
				continue
			}
			headersMap[header] = true
		}
		headers = append(headers, headerLine)
	}
	log.Printf("Reconstructed %d header lines, ignored %d: msgId='%s'", len(headers), ignoredLines, article.MessageID)
	return headers, nil
}
