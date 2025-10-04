// Package common provides shared utilities for go-pugleaf
package common

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/go-while/go-pugleaf/internal/models"
)

var VerboseHeaders bool = false
var IgnoreGoogleHeaders bool = false
var UseStrictGroupValidation bool = false
var ErrNoNewsgroups = fmt.Errorf("ErrNoNewsgroups")
var unwantedChars = ";:,<>#*`§()[]{}?!%$§/\\@\"'"
var (
	// Do NOT change this here! these are needed for runtime !
	// validGroupNameRegex validates newsgroup names according to RFC standards
	// Pattern: lowercase alphanumeric start, components separated by dots, no trailing dots/hyphens
	SeparatorRegex            = regexp.MustCompile(`[,;:\s]+`)
	validGroupNameRegexStrict = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(?:\.[a-z0-9][a-z0-9-]*)+$`)
	validGroupNameRegexchar   = regexp.MustCompile(`^[a-zA-Z0-9]{1,255}$`)
	validGroupNameRegexLazy   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+&-]*$`)
	validGroupNameRegexSingle = regexp.MustCompile(`^[A-Za-z0-9-_+&][A-Za-z0-9-_+&]{1,64}$`)
	validGroupNameRegexCaps   = regexp.MustCompile(`^[A-Za-z0-9-_+&][A-Za-z0-9-_+&]*(?:\.[A-Za-z0-9-_+&][A-Za-z0-9-_+&]*)+$`)
)

// IgnoreHeadersMap is a map version of IgnoreHeaders for fast lookup
var IgnoreHeadersMap = map[string]bool{
	"message-id": true,
	"references": true,
	"subject":    true,
	"from":       true,
	"date":       true,
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

	// Convert common timezone abbreviations to numeric offsets
	// This handles the issue where Go's time.Parse() doesn't recognize abbreviations like EDT
	timezoneMap := map[string]string{
		"EDT": "-0400", // Eastern Daylight Time (UTC-4)
		"EST": "-0500", // Eastern Standard Time (UTC-5)
		"CDT": "-0500", // Central Daylight Time (UTC-5)
		"CST": "-0600", // Central Standard Time (UTC-6)
		"MDT": "-0600", // Mountain Daylight Time (UTC-6)
		"MST": "-0700", // Mountain Standard Time (UTC-7)
		"PDT": "-0700", // Pacific Daylight Time (UTC-7)
		"PST": "-0800", // Pacific Standard Time (UTC-8)
	}

	// Replace timezone abbreviations with numeric offsets
	normalizedDateStr := dateStr
	for abbr, offset := range timezoneMap {
		normalizedDateStr = strings.Replace(normalizedDateStr, " "+abbr, " "+offset, 1)
	}

	// Date-Received: Sat, 26-Sep-87 10:35:30 EDT -> Sat, 26-Sep-87 10:35:30 -0400
	// Common Date-Received formats seen in NNTP
	dateFormats := []string{
		"Mon, _2-Jan-06 15:04:05 -0700",   // e.g., "Sat, 26-Sep-87 10:35:30 -0400" (numeric offset)
		"Mon, 02-Jan-06 15:04:05 -0700",   // with leading zero
		"Mon, _2-Jan-2006 15:04:05 -0700", // 4-digit year version
		"Mon, 02-Jan-2006 15:04:05 -0700", // 4-digit year with leading zero
		"Mon, _2-Jan-06 15:04:05 MST",     // fallback for unrecognized abbreviations
		"Mon, 02-Jan-06 15:04:05 MST",     // fallback with leading zero
		"Mon, _2-Jan-2006 15:04:05 MST",   // fallback 4-digit year
		"Mon, 02-Jan-2006 15:04:05 MST",   // fallback 4-digit year with leading zero
		time.RFC1123Z,                     // Standard RFC format
		time.RFC1123,                      // Standard RFC format
		time.RFC822Z,                      // RFC822 with timezone
		time.RFC822,                       // RFC822
	}

	for _, format := range dateFormats {
		if t, err := time.Parse(format, normalizedDateStr); err == nil {
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
						article.DateSent = parsedTime   // Update article DateSent with corrected time
						article.DateString = dateHeader // Update DateString with corrected value
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
					article.DateSent = parsedTime   // Update article DateSent with corrected time
					article.DateString = dateHeader // Update DateString with corrected value
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
	headers = append(headers, "Date: "+dateHeader)
	headers = append(headers, "From: "+article.FromHeader)
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
	badGroups := 0
	var validNewsgroups []string

checkHeader:
	for i, headerLine := range moreHeaders {
		if len(headerLine) == 0 {
			log.Printf("Empty headerline=%d in msgId='%s' (continue)", i, article.MessageID)
			continue checkHeader
		}
		isSpacedLine = strings.HasPrefix(headerLine, " ") || strings.HasPrefix(headerLine, "\t")
		if isSpacedLine && ignoreLine {
			ignoredLines++
			continue checkHeader
		} else {
			ignoreLine = false
		}

		if !isSpacedLine {
			if len(headerLine) < 4 { // "X: A"
				log.Printf("Short header: '%s' line=%d in msgId='%s' (continue)", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue checkHeader
			}
			// check if first char is lowercase
			if unicode.IsLower(rune(headerLine[0])) {
				if VerboseHeaders {
					log.Printf("Lowercase header: '%s' line=%d in msgId='%s' (rewrite)", headerLine, i, article.MessageID)
				}
				headerLine = strings.ToUpper(string(headerLine[0])) + headerLine[1:]
			}

			// Check for proper header format: "name: value" (colon followed by space)
			colonIndex := strings.Index(headerLine, ":")
			if colonIndex == -1 {
				log.Printf("Invalid header (no colon): '%s' line=%d in msgId='%s' (skip)", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue checkHeader
			}

			// Check if header follows RFC format "name: value" (colon-space)
			if colonIndex+1 >= len(headerLine) || headerLine[colonIndex+1] != ' ' {
				// Malformed header - missing space after colon, skip it
				log.Printf("Malformed header (no colon-space): '%s' line=%d in msgId='%s' (skip)", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue checkHeader
			}

			header := strings.SplitN(headerLine, ":", 2)[0]
			// extracted header key. do some checks
			if header == "" || strings.Contains(header, " ") {
				log.Printf("Invalid header (empty or contains space): '%s' line=%d in msgId='%s' (skip)", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue checkHeader
			}
			if IgnoreHeadersMap[strings.ToLower(header)] {
				ignoreLine = true
				continue checkHeader
			}
			if IgnoreGoogleHeaders && strings.HasPrefix(strings.ToLower(header), "x-goo") {
				ignoreLine = true
				ignoredLines++
				continue checkHeader
			}
			if !strings.HasPrefix(header, "X-") {
				if headersMap[strings.ToLower(header)] {
					log.Printf("Duplicate header: '%s' line=%d in msgId='%s' (rewrite)", headerLine, i, article.MessageID)
					headerLine = "X-RW-" + headerLine
				}
				headersMap[strings.ToLower(header)] = true
			}
			if header == "Newsgroups" {
				// Check if Newsgroups header contains at least one valid newsgroup name
				// check if next headerlines are continued lines
			getLines:
				for {
					if i+1 < len(moreHeaders) {
						if !strings.HasPrefix(moreHeaders[i+1], " ") && !strings.HasPrefix(moreHeaders[i+1], "\t") {
							break getLines
						}
						headerLine += moreHeaders[i+1]
						i++
					} else {
						break getLines
					}
					if len(headerLine) > 1024 {
						log.Printf("Newsgroups header too long, exceeds total 1024 chars: '%s' line=%d in msgId='%s' (break)", headerLine, i, article.MessageID)
						break getLines
					}
				}

				// Extract only the newsgroups value (after "Newsgroups: ")
				parts := strings.SplitN(headerLine, ":", 2)
				if len(parts) != 2 {
					log.Printf("Invalid Newsgroups header format: '%s' line=%d in msgId='%s' (continue)", headerLine, i, article.MessageID)
					ignoreLine = true
					ignoredLines++
					continue checkHeader
				}
				newsgroupsValue := strings.TrimSpace(parts[1])
				newsgroupsValue = strings.ReplaceAll(newsgroupsValue, ":", " ")
				newsgroupsValue = strings.ReplaceAll(newsgroupsValue, ";", " ")
				newsgroupsValue = strings.TrimSpace(newsgroupsValue)
				if newsgroupsValue == "" {
					log.Printf("Invalid Empty Newsgroups header value: '%s' line=%d in msgId='%s' (skip)", headerLine, i, article.MessageID)
					return nil, ErrNoNewsgroups
				}
				newsgroups := SeparatorRegex.Split(newsgroupsValue, -1)
			checkGroups:
				for x, group := range newsgroups {
					/*
						if UseStrictGroupValidation && group != strings.ToLower(group) {
							log.Printf("Invalid newsgroup name not lowercase: '%s' in line=%d idx=%d in msgId='%s'", group, i, x, article.MessageID)
							badGroups++
							continue checkGroups
						}
					*/
					trimmedNG := strings.TrimSpace(group)
					if trimmedNG != strings.ToLower(group) {
						trimmedNG = strings.ToLower(group) // parse to lowercase
						badGroups++
					}
					// Clean up unwanted characters (remove each character individually)
					for _, char := range unwantedChars {
						trimmedNG = strings.ReplaceAll(trimmedNG, string(char), "")
					}
					trimmedNG = strings.TrimSpace(trimmedNG)
					trimmedNG = strings.TrimLeft(trimmedNG, ".")
					trimmedNG = strings.TrimRight(trimmedNG, ".")
					if trimmedNG == "" || strings.Contains(trimmedNG, " ") || !IsValidGroupName(trimmedNG) {
						if trimmedNG == "" {
							log.Printf("Invalid newsgroup name: '%s' empty after cleanup in line=%d idx=%d in msgId='%s'", group, i, x, article.MessageID)
						} else {
							log.Printf("Invalid newsgroup name: '%s' in line=%d idx=%d in msgId='%s'", group, i, x, article.MessageID)
						}
						badGroups++
						continue checkGroups
					}
					validNewsgroups = append(validNewsgroups, trimmedNG)
				} // end for checkGroups

				if len(validNewsgroups) == 0 {
					log.Printf("Invalid Newsgroups header: '%s' line=%d in msgId='%s' (return err)", headerLine, i, article.MessageID)
					return nil, ErrNoNewsgroups
				}

				if badGroups > 0 {
					log.Printf("Invalid Newsgroups header: '%s' line=%d in msgId='%s' has %d invalid newsgroup names (valid=%d)", headerLine, i, article.MessageID, badGroups, len(validNewsgroups))
					ignoreLine = true
					ignoredLines++
					continue checkHeader
				}

			}
		}
		headers = append(headers, headerLine)
	}
	if VerboseHeaders && ignoredLines > 0 {
		log.Printf("Reconstructed %d header lines, ignored %d: msgId='%s'", len(headers), ignoredLines, article.MessageID)
	}
	if len(validNewsgroups) == 0 {
		return nil, ErrNoNewsgroups
	}
	if badGroups > 0 {
		// append newsgroups headers with line folding
		var currentLine string = "Newsgroups: "
		for i, group := range validNewsgroups {
			if i > 0 {
				if len(currentLine)+1+len(group) > 500 {
					// line would exceed 500 chars, start a new line
					headers = append(headers, currentLine)
					currentLine = " ," + group // continuation line starts with space and comma
				} else {
					currentLine += "," + group
				}
			} else {
				currentLine += group
			}
		}
		// append any remaining line (only if it has content beyond just whitespace)
		if strings.TrimSpace(currentLine) != "" {
			headers = append(headers, currentLine)
		}
		headers = append(headers, fmt.Sprintf("X-pugleaf-debug: %d invalid newsgroups removed", badGroups))
		log.Printf("Reconstructed Newsgroups header with %d valid, removed %d. msgId='%s'", len(validNewsgroups), badGroups, article.MessageID)
		for i := range validNewsgroups {
			validNewsgroups[i] = "" // free memory
		}
		validNewsgroups = validNewsgroups[:0] // free memory
		validNewsgroups = nil
	}
	return headers, nil
}

func IsValidGroupName(name string) bool {
	if validGroupNameRegexchar.MatchString(name) {
		return true
	}

	if !UseStrictGroupValidation {

		if validGroupNameRegexLazy.MatchString(name) {
			return true
		}
		if validGroupNameRegexSingle.MatchString(name) {
			return true
		}
		// Allow both lowercase and mixed case group names
		if validGroupNameRegexCaps.MatchString(name) {
			return true
		}
		return false
	}
	if len(name) < 1 {
		log.Printf("IsValidGroupName: Group name '%s' is too short (%d characters)", name, len(name))
		return false
	}
	if len(name) > 255 {
		log.Printf("IsValidGroupName: Group name '%s' is too long (%d characters)", name, len(name))
		return false
	}
	name = strings.ToLower(name)
	// Special case for programming language groups ending with ++
	if strings.HasSuffix(name, "++") || strings.HasSuffix(name, "+") {
		// Allow C++, C+, etc. in programming contexts
		if validGroupNameRegexLazy.MatchString(strings.ReplaceAll(name, "+", "")) {
			return true
		}
	}
	if validGroupNameRegexStrict.MatchString(name) {
		return true
	}
	return false
}

func multiLineHeaderToMergedString(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) == 1 {
		return vals[0] // Fast path for single-line headers (most common case)
	}
	return strings.Join(vals, "\n") // Ultra fast for multi-line
}

// getHeaderFirst returns the first value for a header, or "" if not present
func GetHeaderFirst(headers map[string][]string, key string) string {
	if vals, ok := headers[key]; ok && len(vals) > 0 {
		// For headers that can be folded across multiple lines (like References),
		// we need to join with spaces instead of newlines to properly unfold them
		if key == "references" || key == "References" || key == "in-reply-to" || key == "In-Reply-To" {
			return multiLineHeaderToStringSpaced(vals)
		}
		return multiLineHeaderToMergedString(vals)
	}
	return ""
}

// multiLineHeaderToStringSpaced joins multi-line headers with spaces (for RFC-compliant header unfolding)
func multiLineHeaderToStringSpaced(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) == 1 {
		return vals[0] // Fast path for single-line headers
	}
	var sb strings.Builder
	for i, line := range vals {
		// Trim each line and add spaces between them
		line = strings.TrimSpace(line)
		if line == "" {
			continue // Skip empty lines
		}
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(line)
	}
	return sb.String()
}

func multiLineStringToSlice(input string) []string {
	// Replace newlines with spaces, trim leading/trailing spaces
	result := strings.Split(input, "\n")
	return result
}
