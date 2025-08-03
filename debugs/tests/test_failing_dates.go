package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Test the current bruteForceDateParse logic with the failing cases
func testCurrentLogic(dateStr string) {
	fmt.Printf("Testing: %s\n", dateStr)

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
			fmt.Printf("  Found 4-digit year: %d\n", year)
		}
	}

	// Pattern 2: Find month name
	lowerDateStr := strings.ToLower(dateStr)
	for monthName, monthNum := range monthMap {
		if strings.Contains(lowerDateStr, monthName) {
			month = int(monthNum)
			fmt.Printf("  Found month: %s (%d)\n", monthName, month)
			break
		}
	}

	// Pattern 3: Find day
	dayRegex := regexp.MustCompile(`\b([1-9]|[12]\d|3[01])\b`)
	dayMatches := dayRegex.FindAllString(dateStr, -1)
	fmt.Printf("  Day candidates: %v\n", dayMatches)
	for _, dayMatch := range dayMatches {
		if d, err := strconv.Atoi(dayMatch); err == nil && d >= 1 && d <= 31 {
			if d != year && d != month {
				day = d
				fmt.Printf("  Selected day: %d\n", day)
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
		fmt.Printf("  Found time: %02d:%02d:%02d\n", hour, min, sec)
	}

	// 2-digit year logic
	if year == 0 {
		fmt.Println("  No 4-digit year found, trying 2-digit logic...")
		twoDigitYearRegex := regexp.MustCompile(`\b([0-9]\d)\b`)
		allMatches := twoDigitYearRegex.FindAllString(dateStr, -1)
		fmt.Printf("  All 2-digit matches: %v\n", allMatches)

		// First pass: >= 60
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

		// Second pass: 32-59
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

		// Third pass: 00-31
		if year == 0 && len(allMatches) > 0 {
			for _, match := range allMatches {
				if y, err := strconv.Atoi(match); err == nil {
					// Skip if already used as day/month
					if (y == day && day > 0) || (y == month && month > 0) {
						fmt.Printf("  Skipping %s (already used as day/month)\n", match)
						continue
					}
					// Apply 2-digit year logic
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
	}

	fmt.Printf("  Final result: year=%d, month=%d, day=%d, time=%02d:%02d:%02d\n", year, month, day, hour, min, sec)

	if year > 0 && month > 0 && day > 0 {
		result := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
		fmt.Printf("  Parsed date: %s\n", result.Format("Mon, 02 Jan 2006 15:04:05"))
	} else {
		fmt.Printf("  Failed to parse completely\n")
	}
	fmt.Println()
}

func main() {
	testCases := []string{
		"23 Apr 05 07:13:23 +-0400", // Should be 2005
		"16 Apr 13 21:43:49 +-0100", // Should be 2013
	}

	for _, tc := range testCases {
		testCurrentLogic(tc)
	}
}
