// NNTP newsgroup analyzer for go-pugleaf
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// readGroupsFromActiveFile reads newsgroup names from an active file
// Active file format: "groupname high_article_number low_article_number posting_status"
func readGroupsFromActiveFile(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open active file '%s': %w", filePath, err)
	}
	defer file.Close()

	var groups []string
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse active file format: groupname high low status
		fields := strings.Fields(line)
		if len(fields) < 4 {
			// Skip malformed lines but don't fail
			fmt.Printf("Warning: Skipping malformed line %d in active file: %s\n", lineNum, line)
			continue
		}

		groupName := fields[0]
		if groupName == "" {
			continue
		}

		groups = append(groups, groupName)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading active file '%s': %w", filePath, err)
	}

	if len(groups) == 0 {
		return nil, fmt.Errorf("no valid newsgroups found in active file '%s'", filePath)
	}

	return groups, nil
}

// showUsageExamples displays usage examples for nntp-analyze
func showUsageExamples() {
	fmt.Println("\n=== NNTP Analyzer - Newsgroup Analysis Tool ===")
	fmt.Println("The NNTP analyzer provides comprehensive newsgroup analysis capabilities.")
	fmt.Println("It fetches XOVER data, caches it locally, and provides detailed statistics.")
	fmt.Println()
	fmt.Println("Basic Commands:")
	fmt.Println("  Basic analysis:")
	fmt.Println("    ./nntp-analyze -group alt.test")
	fmt.Println()
	fmt.Println("  Analyze all newsgroups in database:")
	fmt.Println("    ./nntp-analyze -group \"$all\"")
	fmt.Println()
	fmt.Println("  Analyze groups from active file:")
	fmt.Println("    ./nntp-analyze -groups-file /path/to/active")
	fmt.Println("    ./nntp-analyze -groups-file active.i2pn2.txt")
	fmt.Println()
	fmt.Println("  Force refresh analysis (ignore cache):")
	fmt.Println("    ./nntp-analyze -group alt.test -force-refresh")
	fmt.Println()
	fmt.Println("  Limit analysis to specific number of articles:")
	fmt.Println("    ./nntp-analyze -group alt.test -max-analyze 5000")
	fmt.Println()
	fmt.Println("Date Range Analysis:")
	fmt.Println("  ./nntp-analyze -group alt.test -start-date 2000-01-01 -end-date 2024-12-31")
	fmt.Println("  ./nntp-analyze -group alt.test -start-date 2024-12-31")
	fmt.Println()
	fmt.Println("Export Results:")
	fmt.Println("  ./nntp-analyze -group alt.test -export json")
	fmt.Println("  ./nntp-analyze -group alt.test -export csv")
	fmt.Println()
	fmt.Println("Cache Management:")
	fmt.Println("  ./nntp-analyze -group alt.test -cache-stats")
	fmt.Println("  ./nntp-analyze -group alt.test -validate-cache")
	fmt.Println("  ./nntp-analyze -group alt.test -clear-cache")
	fmt.Println()
	fmt.Println("Server Configuration:")
	fmt.Println("  ./nntp-analyze -group alt.test -host news.server.com -port 563")
	fmt.Println("  ./nntp-analyze -group alt.test -username user -password pass")
	fmt.Println()
	fmt.Println("Advanced Usage (Groups File):")
	fmt.Println("  ./nntp-analyze -groups-file active.txt -force-refresh")
	fmt.Println("  ./nntp-analyze -groups-file active.txt -max-analyze 1000")
	fmt.Println("  ./nntp-analyze -groups-file active.txt -export json")
	fmt.Println()
	fmt.Println("Analysis Output includes:")
	fmt.Println("  - Total article count and byte size")
	fmt.Println("  - Article number range (first/last)")
	fmt.Println("  - Date range (oldest/newest)")
	fmt.Println("  - Articles per day average")
	fmt.Println("  - Cache statistics")
	fmt.Println("  - Export capabilities (JSON/CSV)")
	fmt.Println()
}

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	log.Printf("Starting go-pugleaf NNTP Analyzer (version %s)", config.AppVersion)

	// Command line flags for NNTP server connection
	var (
		host       = flag.String("host", "lux-feed1.newsdeef.eu", "NNTP hostname")
		port       = flag.Int("port", 563, "NNTP port")
		username   = flag.String("username", "read", "NNTP username")
		password   = flag.String("password", "only", "NNTP password")
		ssl        = flag.Bool("ssl", true, "Use SSL/TLS connection")
		timeout    = flag.Int("timeout", 30, "Connection timeout in seconds")
		group      = flag.String("group", "", "Newsgroup to analyze (required) \\$all or alt.*")
		groupsFile = flag.String("groups-file", "", "Path to active file containing newsgroups to analyze (format: 'groupname high low status' per line)")

		// Analysis options
		forceRefresh       = flag.Bool("force-refresh", false, "Force refresh cached analysis data (default: false)")
		maxAnalyzeArticles = flag.Int64("max-analyze", 0, "Maximum number of articles to analyze (0 = no limit)")
		startDate          = flag.String("start-date", "", "Start date for analysis (YYYY-MM-DD format)")
		endDate            = flag.String("end-date", "", "End date for analysis (YYYY-MM-DD format)")
		exportFormat       = flag.String("export", "", "Export analysis results (json|csv)")
		validateCache      = flag.Bool("validate-cache", false, "Validate cache integrity for the group")
		clearCache         = flag.Bool("clear-cache", false, "Clear cached data for the group")
		cacheStats         = flag.Bool("cache-stats", false, "Show cache statistics for the group")
		showHelp           = flag.Bool("help", false, "Show usage examples and exit")
	)
	flag.Parse()

	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}
	if *exportFormat == "csv" {
		log.Printf("Exporting analysis results to CSV format:")
		fmt.Println(processor.GetAnalysisCSVHeader())
		processor.GetAnalysisCSVHeader()
		time.Sleep(3 * time.Second) // Give time to see the header
	}
	// Validate required parameters
	if *group == "" && *groupsFile == "" {
		fmt.Println("Error: Either -group or -groups-file parameter is required")
		fmt.Println("Use -help to see usage examples")
		os.Exit(1)
	}

	// Handle groups from active file
	if *groupsFile != "" {
		if err := analyzeGroupsFromFile(*groupsFile, host, port, username, password, ssl, timeout,
			forceRefresh, maxAnalyzeArticles, startDate, endDate,
			exportFormat, validateCache, clearCache, cacheStats); err != nil {
			log.Fatalf("Analysis of groups from file failed: %v", err)
		}
		return
	}

	// Handle case for analyzing all groups
	if *group == "$all" || strings.HasSuffix(*group, "*") {
		if err := analyzeAllGroups(group, host, port, username, password, ssl, timeout,
			forceRefresh, maxAnalyzeArticles, startDate, endDate,
			exportFormat, validateCache, clearCache, cacheStats); err != nil {
			log.Fatalf("Analysis of all groups failed: %v", err)
		}
		return
	}

	// If groups-file is specified, read groups from file
	if *groupsFile != "" {
		fmt.Printf("Reading newsgroups from file: %s\n", *groupsFile)
		if err := readGroupsFromFile(*groupsFile, host, port, username, password, ssl, timeout,
			forceRefresh, maxAnalyzeArticles, startDate, endDate,
			exportFormat, validateCache, clearCache, cacheStats); err != nil {
			log.Fatalf("Analysis of groups from file failed: %v", err)
		}
		return
	}

	// Run analysis for single group
	if err := processor.AnalyzeModeStandalone(host, port, username, password, ssl, timeout, group,
		forceRefresh, maxAnalyzeArticles, startDate, endDate,
		exportFormat, validateCache, clearCache, cacheStats); err != nil {
		log.Fatalf("Analysis failed: %v", err)
	}
}

// readGroupsFromFile reads newsgroups from the specified file and performs analysis on each group
func readGroupsFromFile(filePath string, host *string, port *int, username *string, password *string, ssl *bool, timeout *int,
	forceRefresh *bool, maxAnalyzeArticles *int64, startDate *string, endDate *string,
	exportFormat *string, validateCache *bool, clearCache *bool, cacheStats *bool) error {

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open groups file: %w", err)
	}
	defer file.Close()

	// Read and trim newsgroup names from the file
	var newsgroups []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			newsgroups = append(newsgroups, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading groups file: %w", err)
	}

	fmt.Printf("Found %d newsgroups in file\n", len(newsgroups))

	// Analyze each newsgroup
	for i, ng := range newsgroups {
		fmt.Printf("\n--- Analyzing %d/%d: %s ---\n", i+1, len(newsgroups), ng)

		// Run analysis for the group
		if err := processor.AnalyzeModeStandalone(host, port, username, password, ssl, timeout, &ng,
			forceRefresh, maxAnalyzeArticles, startDate, endDate,
			exportFormat, validateCache, clearCache, cacheStats); err != nil {
			fmt.Printf("⚠ Analysis failed for '%s': %v\n", ng, err)
		} else {
			fmt.Printf("✓ Analysis completed for '%s'\n", ng)
		}
	}

	return nil
}

// analyzeAllGroups performs analysis on all newsgroups found in the database using a shared connection pool
func analyzeAllGroups(group *string, host *string, port *int, username *string, password *string, ssl *bool, timeout *int,
	forceRefresh *bool, maxAnalyzeArticles *int64, startDate *string, endDate *string,
	exportFormat *string, validateCache *bool, clearCache *bool, cacheStats *bool) error {

	fmt.Printf("=== Analyzing Newsgroups: %s ===", *group)
	suffixWildcard := strings.HasSuffix(*group, "*")
	var wildcardNG string
	if suffixWildcard {
		// cut string by final *
		wildcardNG = strings.TrimSuffix(*group, "*")
		log.Printf("Using wildcard newsgroup prefix: '%s'", wildcardNG)
		time.Sleep(3 * time.Second) // debug sleep
	}
	// Initialize database to get list of newsgroups
	db, err := database.OpenDatabase(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer close(db.StopChan)

	// Get all newsgroups from database using admin function (includes empty groups)
	newsgroups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return fmt.Errorf("failed to get newsgroups from database: %w", err)
	}

	fmt.Printf("Found %d newsgroups in database (fetched %d)\n", len(newsgroups), len(newsgroups))

	if len(newsgroups) == 0 {
		fmt.Println("No newsgroups found in database")
		return nil
	}

	// Create NNTP connection configuration
	backendConfig := &nntp.BackendConfig{
		Host:     *host,
		Port:     *port,
		SSL:      *ssl,
		Username: *username,
		Password: *password,
		MaxConns: 5,
		//ConnectTimeout: time.Duration(*timeout) * time.Second,
		//ReadTimeout:    60 * time.Second,
		//WriteTimeout:   30 * time.Second,
	}

	fmt.Printf("Creating shared NNTP connection pool for %s:%d (SSL: %v)\n", *host, *port, *ssl)

	// Create shared connection pool - this will be reused for all groups
	pool := nntp.NewPool(backendConfig)
	defer pool.ClosePool() // Only close when ALL analysis is done
	pool.StartCleanupWorker(15 * time.Second)

	// Create processor instance that will be reused for all groups
	proc := &processor.Processor{
		Pool: pool,
	}

	var successCount, errorCount int
	var allGroupsResults []*processor.GroupAnalysis
	var totalArticles, totalBytes int64
	var errorDetails []string // Track the actual error messages with group names
	overallSizeStats := &processor.ArticleSizeStats{}

	// Analyze each newsgroup using the shared processor and pool
	skipped := 0
	var scanGroups []*models.Newsgroup
	for _, ng := range newsgroups {
		if suffixWildcard && !strings.HasPrefix(ng.Name, wildcardNG) {
			//fmt.Printf("Skipping group '%s' (does not match wildcard prefix '%s')\n", ng.Name, wildcardNG)
			skipped++
			continue
		}
		scanGroups = append(scanGroups, ng)
	}
	for i, ng := range scanGroups {

		fmt.Printf("\n--- Analyzing %d/%d: %s ---\n", i+1, len(scanGroups), ng.Name)

		// Use the processor method - no more pool creation/destruction per group!
		analysis, err := proc.AnalyzeMode(ng.Name, *forceRefresh, *maxAnalyzeArticles,
			*startDate, *endDate, *exportFormat, *validateCache, *clearCache, *cacheStats)

		if err != nil {
			fmt.Printf("⚠ Analysis failed for '%s': %v\n", ng.Name, err)
			errorCount++
			errorDetails = append(errorDetails, fmt.Sprintf("%s: %v", ng.Name, err))
		} else {
			fmt.Printf("✓ Analysis completed for '%s'\n", ng.Name)
			successCount++

			// Collect results for aggregate statistics
			if analysis != nil {
				allGroupsResults = append(allGroupsResults, analysis)
				totalArticles += analysis.CachedArticles
				totalBytes += analysis.TotalBytes

				// Aggregate size statistics
				if analysis.SizeStats != nil {
					overallSizeStats.Under4K += analysis.SizeStats.Under4K
					overallSizeStats.Size4to16K += analysis.SizeStats.Size4to16K
					overallSizeStats.Size16to32K += analysis.SizeStats.Size16to32K
					overallSizeStats.Size32to64K += analysis.SizeStats.Size32to64K
					overallSizeStats.Size64to128K += analysis.SizeStats.Size64to128K
					overallSizeStats.Size128to256K += analysis.SizeStats.Size128to256K
					overallSizeStats.Size256to512K += analysis.SizeStats.Size256to512K
					overallSizeStats.Over512K += analysis.SizeStats.Over512K
					overallSizeStats.TotalBytes += analysis.SizeStats.TotalBytes
					overallSizeStats.TotalCount += analysis.SizeStats.TotalCount
				}
			}
		}
	}

	// Summary
	fmt.Printf("\n=== Analysis Summary ===\n")
	fmt.Printf("Total groups: %d\n", len(newsgroups))
	fmt.Printf("Successful: %d\n", successCount)
	fmt.Printf("Errors: %d\n", errorCount)

	if successCount > 0 {
		fmt.Printf("\n=== Aggregate Statistics ===\n")
		fmt.Printf("Total Articles Analyzed: %s\n", formatNumberWithCommas(totalArticles))
		fmt.Printf("Total Size: %s\n", formatBytes(totalBytes))
		if totalArticles > 0 {
			fmt.Printf("Average Article Size: %s\n", formatBytes(totalBytes/totalArticles))
		}

		// Show overall size distribution
		if overallSizeStats.TotalCount > 0 {
			fmt.Printf("\n=== Overall Article Size Distribution ===\n")
			overallSizeStats.PrintSizeDistribution()
		}

		// Show top 10 largest groups by article count
		if len(allGroupsResults) > 0 {
			fmt.Printf("\n=== Top 10 Groups by Article Count ===\n")
			printTopGroupsByArticles(allGroupsResults, 10)
		}

		// Show top 10 largest groups by total size
		if len(allGroupsResults) > 0 {
			fmt.Printf("\n=== Top 10 Groups by Total Size ===\n")
			printTopGroupsBySize(allGroupsResults, 10)
		}
	}

	if errorCount > 0 {
		fmt.Printf("⚠ %d groups had analysis errors:\n", errorCount)
		for _, errorDetail := range errorDetails {
			fmt.Printf("  - %s\n", errorDetail)
		}
	} else {
		fmt.Println("✓ All groups analyzed successfully!")
	}

	if len(processor.InvalidDates) > 0 {
		fmt.Println("\n=== Invalid Dates Found ===")
		for msgID, date := range processor.InvalidDates {
			fmt.Printf("Message ID: %s, Date: %s\n", msgID, date)
		}
	} else {
		fmt.Println("No invalid dates found in analyzed groups.")
	}

	fmt.Println("\n✓ Shared NNTP pool will now be closed")
	return nil
}

// analyzeGroupsFromFile performs analysis on newsgroups listed in an active file
func analyzeGroupsFromFile(filePath string, host *string, port *int, username *string, password *string, ssl *bool, timeout *int,
	forceRefresh *bool, maxAnalyzeArticles *int64, startDate *string, endDate *string,
	exportFormat *string, validateCache *bool, clearCache *bool, cacheStats *bool) error {

	fmt.Printf("=== Analyzing Newsgroups from Active File: %s ===\n", filePath)

	// Read groups from active file
	groupNames, err := readGroupsFromActiveFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read groups from active file: %w", err)
	}

	fmt.Printf("Found %d newsgroups in active file\n", len(groupNames))

	if len(groupNames) == 0 {
		fmt.Println("No newsgroups found in active file")
		return nil
	}

	// Create NNTP connection configuration
	backendConfig := &nntp.BackendConfig{
		Host:           *host,
		Port:           *port,
		SSL:            *ssl,
		Username:       *username,
		Password:       *password,
		MaxConns:       5,
		ConnectTimeout: time.Duration(*timeout) * time.Second,
		//ReadTimeout:    60 * time.Second,
		//WriteTimeout:   30 * time.Second,
	}

	fmt.Printf("Creating shared NNTP connection pool for %s:%d (SSL: %v)\n", *host, *port, *ssl)

	// Create shared connection pool - this will be reused for all groups
	pool := nntp.NewPool(backendConfig)
	defer pool.ClosePool() // Only close when ALL analysis is done
	pool.StartCleanupWorker(15 * time.Second)

	// Create processor instance that will be reused for all groups
	proc := &processor.Processor{
		Pool: pool,
	}

	var successCount, errorCount int
	var allGroupsResults []*processor.GroupAnalysis
	var totalArticles, totalBytes int64
	var errorDetails []string // Track the actual error messages with group names
	overallSizeStats := &processor.ArticleSizeStats{}

	// Analyze each newsgroup using the shared processor and pool
	for i, groupName := range groupNames {
		fmt.Printf("\n--- Analyzing %d/%d: %s ---\n", i+1, len(groupNames), groupName)

		// Use the processor method - no more pool creation/destruction per group!
		analysis, err := proc.AnalyzeMode(groupName, *forceRefresh, *maxAnalyzeArticles,
			*startDate, *endDate, *exportFormat, *validateCache, *clearCache, *cacheStats)

		if err != nil {
			fmt.Printf("⚠ Analysis failed for '%s': %v\n", groupName, err)
			errorCount++
			errorDetails = append(errorDetails, fmt.Sprintf("%s: %v", groupName, err))
		} else {
			fmt.Printf("✓ Analysis completed for '%s'\n", groupName)
			successCount++

			// Collect results for aggregate statistics
			if analysis != nil {
				allGroupsResults = append(allGroupsResults, analysis)
				totalArticles += analysis.CachedArticles
				totalBytes += analysis.TotalBytes

				// Aggregate size statistics
				if analysis.SizeStats != nil {
					overallSizeStats.Under4K += analysis.SizeStats.Under4K
					overallSizeStats.Size4to16K += analysis.SizeStats.Size4to16K
					overallSizeStats.Size16to32K += analysis.SizeStats.Size16to32K
					overallSizeStats.Size32to64K += analysis.SizeStats.Size32to64K
					overallSizeStats.Size64to128K += analysis.SizeStats.Size64to128K
					overallSizeStats.Size128to256K += analysis.SizeStats.Size128to256K
					overallSizeStats.Size256to512K += analysis.SizeStats.Size256to512K
					overallSizeStats.Over512K += analysis.SizeStats.Over512K
					overallSizeStats.TotalBytes += analysis.SizeStats.TotalBytes
					overallSizeStats.TotalCount += analysis.SizeStats.TotalCount
				}
			}
		}
	}

	// Write summary to file
	summaryFile := fmt.Sprintf("out/%s.summary.txt", strings.ReplaceAll(filePath, "/", "_"))
	if err := writeSummaryToFile(summaryFile, groupNames, successCount, errorCount, totalArticles, totalBytes,
		overallSizeStats, allGroupsResults, errorDetails); err != nil {
		fmt.Printf("Warning: Failed to write summary to file '%s': %v\n", summaryFile, err)
	} else {
		fmt.Printf("\n✓ Summary written to: %s\n", summaryFile)
	}

	// Summary
	fmt.Printf("\n=== Analysis Summary ===\n")
	fmt.Printf("Total groups: %d\n", len(groupNames))
	fmt.Printf("Successful: %d\n", successCount)
	fmt.Printf("Errors: %d\n", errorCount)

	if successCount > 0 {
		fmt.Printf("\n=== Aggregate Statistics ===\n")
		fmt.Printf("Total Articles Analyzed: %s\n", formatNumberWithCommas(totalArticles))
		fmt.Printf("Total Size: %s\n", formatBytes(totalBytes))
		if totalArticles > 0 {
			fmt.Printf("Average Article Size: %s\n", formatBytes(totalBytes/totalArticles))
		}

		// Show overall size distribution
		if overallSizeStats.TotalCount > 0 {
			fmt.Printf("\n=== Overall Article Size Distribution ===\n")
			overallSizeStats.PrintSizeDistribution()
		}

		// Show top 10 largest groups by article count
		if len(allGroupsResults) > 0 {
			fmt.Printf("\n=== Top 10 Groups by Article Count ===\n")
			printTopGroupsByArticles(allGroupsResults, 10)
		}

		// Show top 10 largest groups by total size
		if len(allGroupsResults) > 0 {
			fmt.Printf("\n=== Top 10 Groups by Total Size ===\n")
			printTopGroupsBySize(allGroupsResults, 10)
		}
	}

	if errorCount > 0 {
		fmt.Printf("⚠ %d groups had analysis errors:\n", errorCount)
		for _, errorDetail := range errorDetails {
			fmt.Printf("  - %s\n", errorDetail)
		}
	} else {
		fmt.Println("✓ All groups analyzed successfully!")
	}

	if len(processor.InvalidDates) > 0 {
		fmt.Println("\n=== Invalid Dates Found ===")
		for msgID, date := range processor.InvalidDates {
			fmt.Printf("Message ID: %s, Date: %s\n", msgID, date)
		}
	} else {
		fmt.Println("No invalid dates found in analyzed groups.")
	}

	fmt.Println("\n✓ Shared NNTP pool will now be closed")
	return nil
}

// writeSummaryToFile writes the analysis summary to a file
func writeSummaryToFile(filename string, groupNames []string, successCount, errorCount int,
	totalArticles, totalBytes int64, overallSizeStats *processor.ArticleSizeStats,
	allGroupsResults []*processor.GroupAnalysis, errorDetails []string) error {

	// Create output directory if it doesn't exist
	if err := os.MkdirAll("out", 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Create the summary file
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create summary file: %w", err)
	}
	defer file.Close()

	// Write summary content to file
	fmt.Fprintf(file, "=== Analysis Summary ===\n")
	fmt.Fprintf(file, "Total groups: %d\n", len(groupNames))
	fmt.Fprintf(file, "Successful: %d\n", successCount)
	fmt.Fprintf(file, "Errors: %d\n", errorCount)

	if successCount > 0 {
		fmt.Fprintf(file, "\n=== Aggregate Statistics ===\n")
		fmt.Fprintf(file, "Total Articles Analyzed: %s\n", formatNumberWithCommas(totalArticles))
		fmt.Fprintf(file, "Total Size: %s\n", formatBytes(totalBytes))
		if totalArticles > 0 {
			fmt.Fprintf(file, "Average Article Size: %s\n", formatBytes(totalBytes/totalArticles))
		}

		// Show overall size distribution
		if overallSizeStats.TotalCount > 0 {
			fmt.Fprintf(file, "\n=== Overall Article Size Distribution ===\n")
			writeSizeDistributionToFile(file, overallSizeStats)
		}

		// Show top 10 largest groups by article count
		if len(allGroupsResults) > 0 {
			fmt.Fprintf(file, "\n=== Top 10 Groups by Article Count ===\n")
			writeTopGroupsByArticlesToFile(file, allGroupsResults, 10)
		}

		// Show top 10 largest groups by total size
		if len(allGroupsResults) > 0 {
			fmt.Fprintf(file, "\n=== Top 10 Groups by Total Size ===\n")
			writeTopGroupsBySizeToFile(file, allGroupsResults, 10)
		}
	}

	if errorCount > 0 {
		fmt.Fprintf(file, "\n⚠ %d groups had analysis errors:\n", errorCount)
		for _, errorDetail := range errorDetails {
			fmt.Fprintf(file, "  - %s\n", errorDetail)
		}
	} else {
		fmt.Fprintf(file, "\n✓ All groups analyzed successfully!\n")
	}

	if len(processor.InvalidDates) > 0 {
		fmt.Fprintf(file, "\n=== Invalid Dates Found ===\n")
		for msgID, date := range processor.InvalidDates {
			fmt.Fprintf(file, "Message ID: %s, Date: %s\n", msgID, date)
		}
	} else {
		fmt.Fprintf(file, "\nNo invalid dates found in analyzed groups.\n")
	}

	return nil
}

// writeSizeDistributionToFile writes article size distribution to file
func writeSizeDistributionToFile(file *os.File, stats *processor.ArticleSizeStats) {
	fmt.Fprintf(file, "< 4KB:       %8s articles (%s)\n",
		formatNumberWithCommas(stats.Under4K), formatBytes(stats.Under4K*2*1024))
	fmt.Fprintf(file, "4KB-16KB:    %8s articles (%s)\n",
		formatNumberWithCommas(stats.Size4to16K), formatBytes(stats.Size4to16K*10*1024))
	fmt.Fprintf(file, "16KB-32KB:   %8s articles (%s)\n",
		formatNumberWithCommas(stats.Size16to32K), formatBytes(stats.Size16to32K*24*1024))
	fmt.Fprintf(file, "32KB-64KB:   %8s articles (%s)\n",
		formatNumberWithCommas(stats.Size32to64K), formatBytes(stats.Size32to64K*48*1024))
	fmt.Fprintf(file, "64KB-128KB:  %8s articles (%s)\n",
		formatNumberWithCommas(stats.Size64to128K), formatBytes(stats.Size64to128K*96*1024))
	fmt.Fprintf(file, "128KB-256KB: %8s articles (%s)\n",
		formatNumberWithCommas(stats.Size128to256K), formatBytes(stats.Size128to256K*192*1024))
	fmt.Fprintf(file, "256KB-512KB: %8s articles (%s)\n",
		formatNumberWithCommas(stats.Size256to512K), formatBytes(stats.Size256to512K*384*1024))
	fmt.Fprintf(file, "> 512KB:     %8s articles (%s)\n",
		formatNumberWithCommas(stats.Over512K), formatBytes(stats.Over512K*768*1024))
}

// writeTopGroupsByArticlesToFile writes top groups by article count to file
func writeTopGroupsByArticlesToFile(file *os.File, results []*processor.GroupAnalysis, topN int) {
	// Sort by cached articles descending
	sorted := make([]*processor.GroupAnalysis, len(results))
	copy(sorted, results)

	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].CachedArticles < sorted[j].CachedArticles {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	if topN > len(sorted) {
		topN = len(sorted)
	}

	for i := 0; i < topN; i++ {
		fmt.Fprintf(file, "%2d. %-50s %12s articles (%s)\n",
			i+1,
			sorted[i].GroupName,
			formatNumberWithCommas(sorted[i].CachedArticles),
			formatBytes(sorted[i].TotalBytes))
	}
}

// writeTopGroupsBySizeToFile writes top groups by total size to file
func writeTopGroupsBySizeToFile(file *os.File, results []*processor.GroupAnalysis, topN int) {
	// Sort by total bytes descending
	sorted := make([]*processor.GroupAnalysis, len(results))
	copy(sorted, results)

	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].TotalBytes < sorted[j].TotalBytes {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	if topN > len(sorted) {
		topN = len(sorted)
	}

	for i := 0; i < topN; i++ {
		avgSize := int64(0)
		if sorted[i].CachedArticles > 0 {
			avgSize = sorted[i].TotalBytes / sorted[i].CachedArticles
		}
		fmt.Fprintf(file, "%2d. %-50s %12s (%s articles, avg %s)\n",
			i+1,
			sorted[i].GroupName,
			formatBytes(sorted[i].TotalBytes),
			formatNumberWithCommas(sorted[i].CachedArticles),
			formatBytes(avgSize))
	}
}

// Helper functions for formatting and displaying results

// formatBytes formats byte counts in human readable format
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	if n < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}

// formatNumberWithCommas formats numbers with comma separators
func formatNumberWithCommas(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}

	// Convert to string and add commas
	str := fmt.Sprintf("%d", n)
	result := ""

	for i, char := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result += ","
		}
		result += string(char)
	}

	return result
}

// printTopGroupsByArticles prints the top N groups by article count
func printTopGroupsByArticles(results []*processor.GroupAnalysis, topN int) {
	// Sort by cached articles descending
	sorted := make([]*processor.GroupAnalysis, len(results))
	copy(sorted, results)

	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].CachedArticles < sorted[j].CachedArticles {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	if topN > len(sorted) {
		topN = len(sorted)
	}

	for i := 0; i < topN; i++ {
		fmt.Printf("%2d. %-50s %12s articles (%s)\n",
			i+1,
			sorted[i].GroupName,
			formatNumberWithCommas(sorted[i].CachedArticles),
			formatBytes(sorted[i].TotalBytes))
	}
}

// printTopGroupsBySize prints the top N groups by total size
func printTopGroupsBySize(results []*processor.GroupAnalysis, topN int) {
	// Sort by total bytes descending
	sorted := make([]*processor.GroupAnalysis, len(results))
	copy(sorted, results)

	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].TotalBytes < sorted[j].TotalBytes {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	if topN > len(sorted) {
		topN = len(sorted)
	}

	for i := 0; i < topN; i++ {
		avgSize := int64(0)
		if sorted[i].CachedArticles > 0 {
			avgSize = sorted[i].TotalBytes / sorted[i].CachedArticles
		}
		fmt.Printf("%2d. %-50s %12s (%s articles, avg %s)\n",
			i+1,
			sorted[i].GroupName,
			formatBytes(sorted[i].TotalBytes),
			formatNumberWithCommas(sorted[i].CachedArticles),
			formatBytes(avgSize))
	}
}
