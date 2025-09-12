package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	database.NO_CACHE_BOOT = true // prevents booting caches
	log.Printf("go-pugleaf Date Parser (version: %s)", config.AppVersion)
	if len(os.Args) < 2 {
		fmt.Println("Usage: parsedates \"date string\"")
		fmt.Println("Example: parsedates \"27 May 09 1:01:53 PM\"")
		os.Exit(1)
	}

	dateStr := os.Args[1]
	fmt.Printf("Original date string: %s\n", dateStr)

	// Parse using the NNTP date parser
	parsedTime := processor.ParseNNTPDate(dateStr)

	if parsedTime.IsZero() {
		fmt.Println("❌ Failed to parse date")
	} else {
		fmt.Printf("✅ Parsed successfully:\n")
		fmt.Printf("   RFC3339: %s\n", parsedTime.Format(time.RFC3339))
		fmt.Printf("   Human:   %s\n", parsedTime.Format("Monday, 2 January 2006 15:04:05 MST"))
		fmt.Printf("   Year:    %d\n", parsedTime.Year())
		fmt.Printf("   Month:   %s (%d)\n", parsedTime.Month(), int(parsedTime.Month()))
		fmt.Printf("   Day:     %d\n", parsedTime.Day())
		fmt.Printf("   Time:    %02d:%02d:%02d\n", parsedTime.Hour(), parsedTime.Minute(), parsedTime.Second())
	}
}
