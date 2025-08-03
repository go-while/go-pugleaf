package main

import (
	"fmt"
	"regexp"
	"strconv"
)

func main() {
	dateStr := "Wed, 24 Nov 93 19:45:40 -1"

	fmt.Printf("Testing date string: %s\n", dateStr)

	// Debug the month-year pattern step by step
	monthYearRegex := regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*[\s,]+\d{1,2}[\s,]+(\d{2})(?:\s|$|[^0-9])`)
	matches := monthYearRegex.FindAllStringSubmatch(dateStr, -1)

	fmt.Printf("All month-year matches found: %d\n", len(matches))
	for i, match := range matches {
		fmt.Printf("Match %d: %v\n", i, match)
		fmt.Printf("  Full match: '%s'\n", match[0])
		fmt.Printf("  Month: '%s'\n", match[1])
		fmt.Printf("  Year: '%s'\n", match[2])
	}

	// Test the direct filtering approach instead
	fmt.Println("\nTesting direct filtering approach:")
	twoDigitYearRegex := regexp.MustCompile(`\b([0-9]\d)\b`)
	allMatches := twoDigitYearRegex.FindAllString(dateStr, -1)

	fmt.Printf("All 2-digit matches: %v\n", allMatches)

	// Filter for likely years (>= 60)
	found := false
	for _, match := range allMatches {
		if y, err := strconv.Atoi(match); err == nil {
			fmt.Printf("Checking %s (%d): ", match, y)
			if y >= 60 {
				fmt.Printf("Likely year - ")
				if y >= 69 {
					fmt.Printf("-> %d\n", 1900+y)
				} else {
					fmt.Printf("-> %d\n", 2000+y)
				}
				found = true
				break
			} else {
				fmt.Printf("Not likely year (< 60)\n")
			}
		}
	}

	if !found {
		fmt.Println("No year >= 60 found")
	}
}
