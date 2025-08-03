package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Test the improved 2-digit year logic
func testDateParsing(dateStr string) {
	fmt.Printf("Testing: %s\n", dateStr)

	// Simulate the logic from bruteForceDateParse
	var year, month, day int
	var hour, min, sec int = 12, 0, 0

	// Month detection (simplified)
	monthMap := map[string]time.Month{
		"jan": time.January, "feb": time.February, "mar": time.March,
		"apr": time.April, "may": time.May, "jun": time.June,
		"jul": time.July, "aug": time.August, "sep": time.September,
		"oct": time.October, "nov": time.November, "dec": time.December,
	}

	lowerDateStr := strings.ToLower(dateStr)
	for monthName, monthNum := range monthMap {
		if strings.Contains(lowerDateStr, monthName) {
			month = int(monthNum)
			break
		}
	}

	// Day detection (simplified)
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

	// Time detection (simplified)
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

	// NEW LOGIC: Test improved 2-digit year detection
	twoDigitYearRegex := regexp.MustCompile(`\b([0-9]\d)\b`)
	allMatches := twoDigitYearRegex.FindAllString(dateStr, -1)

	fmt.Printf("  All 2-digit matches: %v\n", allMatches)
	fmt.Printf("  Month: %d, Day: %d\n", month, day)

	// First pass: look for numbers >= 60
	for _, match := range allMatches {
		if y, err := strconv.Atoi(match); err == nil {
			if y >= 60 {
				if y >= 69 {
					year = 1900 + y
				} else {
					year = 2000 + y
				}
				fmt.Printf("  Found year >= 60: %s -> %d\n", match, year)
				break
			}
		}
	}

	// Second pass: look for numbers 32-59
	if year == 0 {
		for _, match := range allMatches {
			if y, err := strconv.Atoi(match); err == nil {
				if y >= 32 && y <= 59 {
					year = 2000 + y
					fmt.Printf("  Found year 32-59: %s -> %d\n", match, year)
					break
				}
			}
		}
	}

	// Third pass: handle 00-31 - be more selective
	if year == 0 && len(allMatches) > 0 {
		for _, match := range allMatches {
			if y, err := strconv.Atoi(match); err == nil {
				// Skip numbers that are definitely day/month if we already have them
				if (y == day && day > 0) || (y == month && month > 0) {
					fmt.Printf("  Skipping known day/month: %s\n", match)
					continue
				}
				// Skip obvious time components (check if appeared in time context)
				timePattern1 := ":" + match
				timePattern2 := ":" + match + ":"
				timePattern3 := ":" + match + " "
				if y <= 23 && strings.Contains(dateStr, timePattern1) {
					fmt.Printf("  Skipping time component (hour): %s\n", match)
					continue
				}
				if y <= 59 && (strings.Contains(dateStr, timePattern2) || strings.Contains(dateStr, timePattern3)) {
					fmt.Printf("  Skipping time component (min/sec): %s\n", match)
					continue
				}
				if y >= 69 {
					year = 1900 + y
				} else {
					year = 2000 + y
				}
				fmt.Printf("  Found year 00-31: %s -> %d\n", match, year)
				break
			}
		}
	}

	if year > 0 && month > 0 && day > 0 {
		result := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
		fmt.Printf("  Result: %s\n", result.Format("Mon, 02 Jan 2006 15:04:05"))
	} else {
		fmt.Printf("  Could not parse completely (year:%d, month:%d, day:%d)\n", year, month, day)
	}
	fmt.Println()
}

func main() {
	testCases := []string{
		"Wed, 24 Nov 93 19:45:40 -1",    // Original case - should be 1993
		"Mon, 15 Dec 13 08:30:00 +0000", // Should be 2013
		"Fri, 3 Jan 05 12:00:00 GMT",    // Should be 2005
		"Thu, 25 Aug 99 23:59:59 EST",   // Should be 1999
		"Tue, 1 Feb 00 06:30:15 PST",    // Should be 2000
		"Sun, 31 Jul 45 14:22:33 -0500", // Should be 2045
	}

	for _, tc := range testCases {
		testDateParsing(tc)
	}
}
