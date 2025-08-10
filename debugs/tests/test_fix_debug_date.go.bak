package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Copied from the actual bruteForceDateParse function with the fix
func bruteForceDateParse(dateStr string) time.Time {
	if dateStr == "" {
		return time.Time{}
	}

	// Clean up common issues
	dateStr = strings.TrimSpace(dateStr)
	dateStr = strings.ReplaceAll(dateStr, "  ", " ") // Double spaces to single

	// Remove common prefixes that might interfere
	dateStr = strings.TrimPrefix(dateStr, "Date:")
	dateStr = strings.TrimSpace(dateStr)

	// Month name mappings for fuzzy matching
	monthMap := map[string]time.Month{
		"jan": time.January, "january": time.January,
		"feb": time.February, "february": time.February,
		"mar": time.March, "march": time.March,
		"apr": time.April, "april": time.April,
		"may": time.May,
		"jun": time.June, "june": time.June,
		"jul": time.July, "july": time.July,
		"aug": time.August, "august": time.August,
		"sep": time.September, "sept": time.September, "september": time.September,
		"oct": time.October, "october": time.October,
		"nov": time.November, "november": time.November,
		"dec": time.December, "december": time.December,
	}

	// Try to extract year, month, day using regex patterns
	var year, month, day int
	var hour, min, sec int = 12, 0, 0 // Default to noon if no time found

	// Pattern 1: Find 4-digit year (1970-2099)
	yearRegex := regexp.MustCompile(`\b(19[7-9]\d|20[0-9]\d)\b`)
	if yearMatch := yearRegex.FindString(dateStr); yearMatch != "" {
		if y, err := strconv.Atoi(yearMatch); err == nil {
			year = y
		}
	}

	// Pattern 2: Try to find month names
	lowerDateStr := strings.ToLower(dateStr)
	for monthName, monthNum := range monthMap {
		if strings.Contains(lowerDateStr, monthName) {
			month = int(monthNum)
			break
		}
	}

	// Pattern 3: Try to find day (1-31)
	dayRegex := regexp.MustCompile(`\b([1-9]|[12]\d|3[01])\b`)
	dayMatches := dayRegex.FindAllString(dateStr, -1)
	for _, dayMatch := range dayMatches {
		if d, err := strconv.Atoi(dayMatch); err == nil && d >= 1 && d <= 31 {
			// Prefer days that are reasonable for the month if we know the month
			if month > 0 {
				maxDays := 31
				if month == 2 {
					maxDays = 29
				} else if month == 4 || month == 6 || month == 9 || month == 11 {
					maxDays = 30
				}
				if d <= maxDays {
					day = d
					break
				}
			} else {
				day = d
				break
			}
		}
	}

	// Pattern 4: Try to find time components HH:MM:SS or HH:MM
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

	// If we couldn't find a year, try to extract 2-digit year and guess the century
	if year == 0 {
		// Look for all 2-digit numbers and filter intelligently
		twoDigitYearRegex := regexp.MustCompile(`\b([0-9]\d)\b`)
		allMatches := twoDigitYearRegex.FindAllString(dateStr, -1)

		// First pass: look for numbers >= 60 (likely years, not days/times)
		for _, match := range allMatches {
			if y, err := strconv.Atoi(match); err == nil {
				if y >= 60 { // Likely a year if >= 60
					if y >= 69 {
						year = 1900 + y
					} else {
						year = 2000 + y
					}
					break
				}
			}
		}

		// Second pass: if still no year, take any 2-digit number that's not obviously day/time
		if year == 0 && len(allMatches) > 0 {
			for _, match := range allMatches {
				if y, err := strconv.Atoi(match); err == nil {
					// Skip obvious day numbers only if we have alternatives
					if len(allMatches) > 1 && y >= 1 && y <= 31 {
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

	// Final validation and fallbacks
	if year < 1970 || year > 2099 {
		year = 1990 // Default fallback year
	}
	if month < 1 || month > 12 {
		month = 1 // Default to January
	}
	if day < 1 || day > 31 {
		day = 1 // Default to 1st
	}

	// Validate day against month
	if month == 2 && day > 29 {
		day = 28
	} else if (month == 4 || month == 6 || month == 9 || month == 11) && day > 30 {
		day = 30
	}

	// If we have at least year and month, create a date
	if year >= 1970 && month >= 1 && month <= 12 {
		return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
	}

	return time.Time{}
}

func main() {
	testDate := "Wed, 24 Nov 93 19:45:40 -1"

	fmt.Printf("Original date string: %s\n", testDate)

	result := bruteForceDateParse(testDate)
	fmt.Printf("Parsed result: %s\n", result.Format("Mon, 02 Jan 2006 15:04:05"))

	// Test a few more cases
	testCases := []string{
		"Wed, 24 Nov 93 19:45:40 -1",
		"Fri, 15 Dec 95 08:30:00 +0000",
		"Mon, 1 Jan 00 12:00:00 GMT",
		"Thu, 31 Dec 99 23:59:59 EST",
	}

	fmt.Println("\nTesting multiple cases:")
	for _, tc := range testCases {
		result := bruteForceDateParse(tc)
		fmt.Printf("Input:  %s\n", tc)
		fmt.Printf("Output: %s\n\n", result.Format("Mon, 02 Jan 2006 15:04:05"))
	}
}
