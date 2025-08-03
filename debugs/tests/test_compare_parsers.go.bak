package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Standard layouts including the new malformed timezone formats
var testLayouts = []string{
	// Standard Go time formats
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	time.RFC850,

	// Common NNTP formats
	"Mon, 02 Jan 2006 15:04:05 -0700",
	"Mon, _2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"02 Jan 2006 15:04:05 -0700",
	"_2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 -0700",
	"Mon, 02 Jan 06 15:04:05 -0700",
	"Mon, _2 Jan 06 15:04:05 -0700",
	"Mon, 2 Jan 06 15:04:05 -0700",
	"02 Jan 06 15:04:05 -0700",
	"_2 Jan 06 15:04:05 -0700",
	"2 Jan 06 15:04:05 -0700",

	// NEW: Malformed timezone formats
	"_2 Jan 06 15:04:05 +-0700",
	"02 Jan 06 15:04:05 +-0700",
	"_2 Jan 2006 15:04:05 +-0700",
	"02 Jan 2006 15:04:05 +-0700",
	"Mon, _2 Jan 06 15:04:05 +-0700",
	"Mon, 02 Jan 06 15:04:05 +-0700",
	"Mon, _2 Jan 2006 15:04:05 +-0700",
	"Mon, 02 Jan 2006 15:04:05 +-0700",

	// Single-digit timezone and GMT variants
	"Mon, _2 Jan 06 15:04:05 -7",
	"Mon, 02 Jan 06 15:04:05 -7",
	"Mon, _2 Jan 06 15:04:05 -1",
	"Mon, 02 Jan 06 15:04:05 -1",
	"Wed, _2 Nov 93 19:45:40 -1",
	"Fri, _2 Jan 05 12:00:00 GMT",
	"Mon, _2 Jan 05 15:04:05 GMT",
	"Tue, 02 Jan 05 15:04:05 GMT",
}

// Standard layout parsing
func parseWithLayouts(dateStr string) (time.Time, string) {
	if dateStr == "" {
		return time.Time{}, "empty string"
	}

	// Clean up like the real parser does
	parenRe := regexp.MustCompile(`\s*\([^)]*\)$`)
	dateStr = parenRe.ReplaceAllString(dateStr, "")
	dateStr = strings.TrimSpace(dateStr)

	for _, layout := range testLayouts {
		t, err := time.Parse(layout, dateStr)
		if err == nil {
			return t, layout
		}
	}
	return time.Time{}, "no layout matched"
}

// Simplified brute force parser (based on the actual implementation)
func testBruteForceDateParse(dateStr string) time.Time {
	if dateStr == "" {
		return time.Time{}
	}

	// Clean up common issues
	dateStr = strings.TrimSpace(dateStr)
	dateStr = strings.ReplaceAll(dateStr, "  ", " ")
	dateStr = strings.TrimPrefix(dateStr, "Date:")
	dateStr = strings.TrimSpace(dateStr)

	// Month name mappings
	monthMap := map[string]time.Month{
		"jan": time.January, "feb": time.February, "mar": time.March,
		"apr": time.April, "may": time.May, "jun": time.June,
		"jul": time.July, "aug": time.August, "sep": time.September,
		"oct": time.October, "nov": time.November, "dec": time.December,
	}

	var year, month, day int
	var hour, min, sec int = 12, 0, 0

	// Pattern 1: Find 4-digit year
	yearRegex := regexp.MustCompile(`\b(19[7-9]\d|20[0-9]\d)\b`)
	if yearMatch := yearRegex.FindString(dateStr); yearMatch != "" {
		if y, err := strconv.Atoi(yearMatch); err == nil {
			year = y
		}
	}

	// Pattern 2: Find month name
	lowerDateStr := strings.ToLower(dateStr)
	for monthName, monthNum := range monthMap {
		if strings.Contains(lowerDateStr, monthName) {
			month = int(monthNum)
			break
		}
	}

	// Pattern 3: Find day
	dayRegex := regexp.MustCompile(`\b([1-9]|[12]\d|3[01])\b`)
	dayMatches := dayRegex.FindAllString(dateStr, -1)
	for _, dayMatch := range dayMatches {
		if d, err := strconv.Atoi(dayMatch); err == nil && d >= 1 && d <= 31 {
			if d != year && d != month {
				day = d
				break
			}
		}
	}

	// Pattern 4: Find time
	timeRegex := regexp.MustCompile(`\b(\d{1,2}):(\d{1,2})(?::(\d{1,2}))?\b`)
	if timeMatch := timeRegex.FindStringSubmatch(dateStr); len(timeMatch) >= 3 {
		if h, err := strconv.Atoi(timeMatch[1]); err == nil && h >= 0 && h <= 23 {
			hour = h
		}
		if m, err := strconv.Atoi(timeMatch[2]); err == nil && m >= 0 && m <= 59 {
			min = m
		}
		if len(timeMatch) > 3 && timeMatch[3] != "" {
			if s, err := strconv.Atoi(timeMatch[3]); err == nil && s >= 0 && s <= 59 {
				sec = s
			}
		}
	}

	// 2-digit year logic (improved version)
	if year == 0 {
		twoDigitYearRegex := regexp.MustCompile(`\b([0-9]\d)\b`)
		allMatches := twoDigitYearRegex.FindAllString(dateStr, -1)

		// First pass: >= 60
		for _, match := range allMatches {
			if y, err := strconv.Atoi(match); err == nil {
				if y >= 60 {
					if y >= 69 {
						year = 1900 + y
					} else {
						year = 2000 + y
					}
					break
				}
			}
		}

		// Second pass: 32-59
		if year == 0 {
			for _, match := range allMatches {
				if y, err := strconv.Atoi(match); err == nil {
					if y >= 32 && y <= 59 {
						year = 2000 + y
						break
					}
				}
			}
		}

		// Third pass: 00-31
		if year == 0 && len(allMatches) > 0 {
			for _, match := range allMatches {
				if y, err := strconv.Atoi(match); err == nil {
					if (y == day && day > 0) || (y == month && month > 0) {
						continue
					}
					if y >= 69 {
						year = 1900 + y
					} else {
						year = 2000 + y
					}
					break
				}
			}
		}
	}

	// Final validation
	if year < 1970 || year > 2099 {
		year = 1990
	}
	if month < 1 || month > 12 {
		month = 1
	}
	if day < 1 || day > 31 {
		day = 1
	}

	if year >= 1970 && month >= 1 && month <= 12 {
		return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
	}

	return time.Time{}
}

func main() {
	testDates := []string{
		"Wed, 24 Nov 93 19:45:40 -1",    // Original problematic case
		"23 Apr 05 07:13:23 +-0400",     // New failing case 1
		"16 Apr 13 21:43:49 +-0100",     // New failing case 2
		"Mon, 15 Dec 13 08:30:00 +0000", // 2-digit year case
		"Fri, 3 Jan 05 12:00:00 GMT",    // Another 2-digit year
	}

	fmt.Println("Comparing Standard Layout Parsing vs Brute Force Parsing")
	fmt.Println("======================================================================")

	for i, dateStr := range testDates {
		fmt.Printf("\n%d. Testing: %s\n", i+1, dateStr)
		fmt.Println("--------------------------------------------------")

		// Test standard layout parsing
		layoutResult, matchedLayout := parseWithLayouts(dateStr)
		fmt.Printf("Layout Parser:\n")
		if !layoutResult.IsZero() {
			fmt.Printf("  ✓ Success: %s\n", layoutResult.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
			fmt.Printf("  ✓ Matched: %s\n", matchedLayout)
		} else {
			fmt.Printf("  ✗ Failed: %s\n", matchedLayout)
		}

		// Test brute force parsing
		bruteResult := testBruteForceDateParse(dateStr)
		fmt.Printf("Brute Force Parser:\n")
		if !bruteResult.IsZero() {
			fmt.Printf("  ✓ Success: %s\n", bruteResult.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
		} else {
			fmt.Printf("  ✗ Failed: Could not parse\n")
		}

		// Compare results
		fmt.Printf("Comparison:\n")
		if !layoutResult.IsZero() && !bruteResult.IsZero() {
			if layoutResult.Equal(bruteResult) {
				fmt.Printf("  ✓ Both parsers agree\n")
			} else {
				fmt.Printf("  ⚠ Different results!\n")
				fmt.Printf("    Layout:     %s\n", layoutResult.Format("2006-01-02 15:04:05"))
				fmt.Printf("    Brute Force: %s\n", bruteResult.Format("2006-01-02 15:04:05"))
			}
		} else if !layoutResult.IsZero() {
			fmt.Printf("  → Layout parser wins\n")
		} else if !bruteResult.IsZero() {
			fmt.Printf("  → Brute force parser wins\n")
		} else {
			fmt.Printf("  ✗ Both parsers failed\n")
		}
	}
}
