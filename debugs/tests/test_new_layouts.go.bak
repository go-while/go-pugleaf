package main

import (
	"fmt"
	"time"
)

func main() {
	// Test the new layouts
	layouts := []string{
		"_2 Jan 06 15:04:05 +-0700",
		"02 Jan 06 15:04:05 +-0700",
		"_2 Jan 2006 15:04:05 +-0700",
		"02 Jan 2006 15:04:05 +-0700",
	}

	testDates := []string{
		"23 Apr 05 07:13:23 +-0400",
		"16 Apr 13 21:43:49 +-0100",
	}

	fmt.Println("Testing new date layouts:")

	for _, dateStr := range testDates {
		fmt.Printf("\nTesting: %s\n", dateStr)
		found := false

		for _, layout := range layouts {
			if t, err := time.Parse(layout, dateStr); err == nil {
				fmt.Printf("  ✓ Matched layout: %s\n", layout)
				fmt.Printf("  ✓ Parsed result: %s\n", t.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
				found = true
				break
			}
		}

		if !found {
			fmt.Printf("  ✗ No layout matched\n")
		}
	}
}
