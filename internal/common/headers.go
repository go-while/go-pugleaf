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

var VerboseHeaders bool = false
var IgnoreGoogleHeaders bool = false

// IgnoreHeadersMap is a map version of IgnoreHeaders for fast lookup
var IgnoreHeadersMap = map[string]bool{
	"message-id": true,
	"subject":    true,
	"from":       true,
	"date":       true,
	"references": true,
	"path":       true,
	"xref":       true,
}

var formats = []string{
	time.RFC1123Z,                     // "Mon, 02 Jan 2006 15:04:05 -0700"
	time.RFC1123,                      // "Mon, 02 Jan 2006 15:04:05 MST"
	time.RFC822,                       // "02 Jan 06 15:04 MST"
	time.RFC822Z,                      // "02 Jan 06 15:04 -0700"
	"Mon, _2 Jan 2006 15:04:05 MST",   // Single digit day
	"Mon, _2 Jan 2006 15:04:05 -0700", // Single digit day with timezone
}

// isRFCdate checks if a date string is RFC compliant for Usenet
func isRFCdate(dateStr string) bool {
	// Try to parse with common RFC formats used in Usenet

	for _, format := range formats {
		if _, err := time.Parse(format, dateStr); err == nil {
			return true
		}
	}
	return false
}

const pathHeader_inv1 string = "Path: pugleaf.invalid!.TX!not-for-mail"
const pathHeader_inv2 string = "X-Path: pugleaf.invalid!.TX!not-for-mail"

// extractDateReceivedHeader extracts the Date-Received header value from HeadersJSON
func extractDateReceivedHeader(headersJSON string) string {
	headerLines := strings.Split(headersJSON, "\n")
	for _, line := range headerLines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "date-received:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// parseDateReceivedHeader parses Date-Received header using common NNTP date formats
func parseDateReceivedHeader(dateStr string) time.Time {
	if dateStr == "" {
		return time.Time{}
	}
	// Date-Received: Sat, 26-Sep-87 10:35:30 EDT
	// Common Date-Received formats seen in NNTP
	dateFormats := []string{
		"Mon, _2-Jan-06 15:04:05 MST",   // e.g., "Sat, 26-Sep-87 10:35:30 EDT"
		"Mon, 02-Jan-06 15:04:05 MST",   // e.g., "Sat, 26-Sep-87 10:35:30 EDT" with leading zero
		"Mon, _2-Jan-2006 15:04:05 MST", // 4-digit year version
		"Mon, 02-Jan-2006 15:04:05 MST", // 4-digit year with leading zero
		time.RFC1123Z,                   // Standard RFC format
		time.RFC1123,                    // Standard RFC format
		time.RFC822Z,                    // RFC822 with timezone
		time.RFC822,                     // RFC822
	}

	for _, format := range dateFormats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t
		}
	}

	return time.Time{}
}

// ReconstructHeaders reconstructs the header lines from an article for transmission
func ReconstructHeaders(article *models.Article, withPath bool, nntphostname *string) ([]string, error) {
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
		if isRFCdate(article.DateString) {
			dateHeader = article.DateString
		} else {
			// DateString is not RFC compliant, try DateSent first
			if !article.DateSent.IsZero() && article.DateSent.Year() >= 1979 {
				dateHeader = article.DateSent.UTC().Format(time.RFC1123Z)
				if VerboseHeaders {
					log.Printf("Using DateSent '%s' instead of DateString '%s' for article %s", dateHeader, article.DateString, article.MessageID)
				}
			} else {
				// DateSent is zero or invalid (e.g., 1969 epoch), try Date-Received header as fallback
				dateReceivedStr := extractDateReceivedHeader(article.HeadersJSON)
				if dateReceivedStr != "" {
					parsedTime := parseDateReceivedHeader(dateReceivedStr)
					if !parsedTime.IsZero() && parsedTime.Year() >= 1979 {
						dateHeader = parsedTime.UTC().Format(time.RFC1123Z)
						//if VerboseHeaders {
						log.Printf("Using Date-Received '%s' (parsed as '%s') instead of invalid DateString '%s' and invalid DateSent (year %d) for article %s", dateReceivedStr, dateHeader, article.DateString, article.DateSent.Year(), article.MessageID)
						//}
					} else {
						log.Printf("ERROR common.ReconstructHeaders: Non-compliant DateString '%s', invalid DateSent (year %d), and invalid Date-Received '%s' for article %s", article.DateString, article.DateSent.Year(), dateReceivedStr, article.MessageID)
						return nil, fmt.Errorf("article has non-compliant DateString, invalid DateSent (year %d), and invalid Date-Received msgId='%s'", article.DateSent.Year(), article.MessageID)
					}
				} else {
					log.Printf("ERROR common.ReconstructHeaders: Non-compliant DateString '%s', invalid DateSent (year %d), and no Date-Received header for article %s", article.DateString, article.DateSent.Year(), article.MessageID)
					log.Printf("DEBUG article %s HeadersJSON: %s", article.MessageID, article.HeadersJSON)
					return nil, fmt.Errorf("article has non-compliant DateString, invalid DateSent (year %d), and no Date-Received header msgId='%s'", article.DateSent.Year(), article.MessageID)
				}
			}
		}
	} else {
		// No DateString, try DateSent
		if !article.DateSent.IsZero() && article.DateSent.Year() >= 1979 {
			dateHeader = article.DateSent.UTC().Format(time.RFC1123)
		} else {
			// DateSent is also zero or invalid, try Date-Received header as fallback
			dateReceivedStr := extractDateReceivedHeader(article.HeadersJSON)
			if dateReceivedStr != "" {
				parsedTime := parseDateReceivedHeader(dateReceivedStr)
				if !parsedTime.IsZero() && parsedTime.Year() >= 1979 {
					dateHeader = parsedTime.UTC().Format(time.RFC1123Z)
					if VerboseHeaders {
						log.Printf("Using Date-Received '%s' (parsed as '%s') when DateString is empty and DateSent is invalid (year %d) for article %s", dateReceivedStr, dateHeader, article.DateSent.Year(), article.MessageID)
					}
				} else {
					return nil, fmt.Errorf("article missing Date header (DateString empty, DateSent invalid year %d, and invalid Date-Received '%s') msgId='%s'", article.DateSent.Year(), dateReceivedStr, article.MessageID)
				}
			} else {
				return nil, fmt.Errorf("article missing Date header (DateString empty, DateSent invalid year %d, and no Date-Received header) msgId='%s'", article.DateSent.Year(), article.MessageID)
			}
		}
	}
	headers = append(headers, "Message-ID: "+article.MessageID)
	headers = append(headers, "Subject: "+article.Subject)
	headers = append(headers, "From: "+article.FromHeader)
	headers = append(headers, "Date: "+dateHeader)
	if article.References != "" {
		headers = append(headers, "References: "+article.References)
	}
	switch withPath {
	case true:
		if article.Path != "" {
			if nntphostname != nil && *nntphostname != "" {
				headers = append(headers, "Path: "+*nntphostname+"!.TX!"+article.Path)
			} else {
				headers = append(headers, "Path: "+article.Path)
			}
		} else {
			headers = append(headers, pathHeader_inv1)
		}
	case false:
		if article.Path != "" {
			if nntphostname != nil && *nntphostname != "" {
				headers = append(headers, "X-Path: "+*nntphostname+"!.TX!"+article.Path)
			} else {
				headers = append(headers, "X-Path: "+article.Path)
			}
		} else {
			headers = append(headers, pathHeader_inv2)
		}
	}
	moreHeaders := strings.Split(article.HeadersJSON, "\n")
	ignoreLine := false
	isSpacedLine := false
	ignoredLines := 0
	headersMap := make(map[string]bool)

	for i, headerLine := range moreHeaders {
		if len(headerLine) == 0 {
			log.Printf("Empty headerline=%d in msgId='%s' (continue)", i, article.MessageID)
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
			if len(headerLine) < 4 { // "X: A"
				log.Printf("Short header: '%s' line=%d in msgId='%s' (continue)", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue
			}
			// check if first char is lowercase
			if unicode.IsLower(rune(headerLine[0])) {
				headerLine = strings.ToUpper(string(headerLine[0])) + headerLine[1:]
				if VerboseHeaders {
					log.Printf("Lowercase header: '%s' line=%d in msgId='%s' (rewrote)", headerLine, i, article.MessageID)
				}
			}
			header := strings.SplitN(headerLine, ":", 2)[0]
			if len(header) == 0 {
				log.Printf("Invalid header: '%s' line=%d in msgId='%s' (continue)", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue
			}
			if IgnoreHeadersMap[strings.ToLower(header)] {
				ignoreLine = true
				continue
			}
			if IgnoreGoogleHeaders && strings.HasPrefix(strings.ToLower(header), "x-google") {
				ignoreLine = true
				continue
			}

			if !strings.HasPrefix(header, "X-") {
				if headersMap[strings.ToLower(header)] {
					log.Printf("Duplicate header: '%s' line=%d in msgId='%s' (continue)", headerLine, i, article.MessageID)
					ignoreLine = true
					continue
				}
				headersMap[strings.ToLower(header)] = true
			}
		}
		headers = append(headers, headerLine)
	}
	if VerboseHeaders && ignoredLines > 0 {
		log.Printf("Reconstructed %d header lines, ignored %d: msgId='%s'", len(headers), ignoredLines, article.MessageID)
	}
	return headers, nil
}
