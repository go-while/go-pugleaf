package main

import (
	"fmt"

	"github.com/go-while/go-pugleaf/internal/processor"
)

func main() {
	// Test the problematic date that user reported
	testDate := "Wed, 30 Jun 93 21:04:13 MESZ"
	fmt.Printf("Testing date: %s\n", testDate)

	// Test our ParseNNTPDate function
	parsedTime := processor.ParseNNTPDate(testDate)
	fmt.Printf("ParseNNTPDate result: %s\n", parsedTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("Year parsed as: %d\n", parsedTime.Year())
	if parsedTime.Year() == 1993 {
		fmt.Printf("✅ SUCCESS: Year 93 correctly parsed as 1993\n")
	} else {
		fmt.Printf("❌ FAILURE: Year 93 incorrectly parsed as %d\n", parsedTime.Year())
	}

	// Test a few other problematic years around the cutoff
	fmt.Printf("\nTesting other 2-digit years:\n")
	testCases := []struct {
		date         string
		expectedYear int
	}{
		{"Wed, 30 Jun 68 21:04:13 MESZ", 2068},
		{"Wed, 30 Jun 69 21:04:13 MESZ", 1969},
		{"Wed, 30 Jun 70 21:04:13 MESZ", 1970},
		{"Wed, 30 Jun 93 21:04:13 MESZ", 1993},
		{"Wed, 30 Jun 99 21:04:13 MESZ", 1999},
		{"Wed, 30 Jun 00 21:04:13 MESZ", 2000},
		{"Wed, 30 Jun 01 21:04:13 MESZ", 2001},
	}

	for _, tc := range testCases {
		parsedTime := processor.ParseNNTPDate(tc.date)
		result := "✅"
		if parsedTime.Year() != tc.expectedYear {
			result = "❌"
		}
		fmt.Printf("%s %s -> %d (expected %d)\n", result, tc.date, parsedTime.Year(), tc.expectedYear)
	}
}
