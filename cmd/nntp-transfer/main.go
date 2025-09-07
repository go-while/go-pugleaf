// NNTP article transfer tool for go-pugleaf
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var IgnoreHeaders = []string{"Message-ID", "Subject", "From", "Date", "References", "Path"}
var IgnoreHeadersMap = make(map[string]bool)

// showUsageExamples displays usage examples for NNTP transfer
func showUsageExamples() {
	fmt.Println("\n=== NNTP Transfer Tool - Usage Examples ===")
	fmt.Println("The NNTP transfer tool sends articles via CHECK/TAKETHIS commands.")
	fmt.Println()
	fmt.Println("Basic Usage:")
	fmt.Println("  ./nntp-transfer -nntphostname your.domain.com -group alt.test")
	fmt.Println("  ./nntp-transfer -nntphostname your.domain.com -group alt.*")
	fmt.Println("  ./nntp-transfer -nntphostname your.domain.com -group news.admin.*")
	fmt.Println()
	fmt.Println("Connection Configuration:")
	fmt.Println("  ./nntp-transfer -host news.server.com -port 563 -group alt.test")
	fmt.Println("  ./nntp-transfer -username user -password pass -group alt.test")
	fmt.Println("  ./nntp-transfer -ssl=false -port 119 -group alt.test")
	fmt.Println()
	fmt.Println("Performance Tuning:")
	fmt.Println("  ./nntp-transfer -batch-size 25 -max-threads 4 -group alt.test")
	fmt.Println("  ./nntp-transfer -check-timeout 30 -group alt.test")
	fmt.Println()
	fmt.Println("Dry Run Mode:")
	fmt.Println("  ./nntp-transfer -dry-run -group alt.test")
	fmt.Println()
}

func main() {
	log.Printf("Starting go-pugleaf NNTP Transfer Tool (version %s)", config.AppVersion)

	// Command line flags for NNTP transfer configuration
	var (
		// Required flags
		hostnamePath  = flag.String("nntphostname", "", "Your hostname must be set!")
		transferGroup = flag.String("group", "", "Newsgroup to transfer (supports wildcards like alt.* or news.*)")

		// Connection configuration
		host     = flag.String("host", "news.server.com", "Target NNTP hostname")
		port     = flag.Int("port", 563, "Target NNTP port")
		username = flag.String("username", "", "Target NNTP username")
		password = flag.String("password", "", "Target NNTP password")
		ssl      = flag.Bool("ssl", true, "Use SSL/TLS connection")
		timeout  = flag.Int("timeout", 30, "Connection timeout in seconds")

		// Transfer configuration
		batchSize  = flag.Int("batch-size", 25, "Number of message-IDs to send in a single CHECK command")
		maxThreads = flag.Int("max-threads", 4, "Maximum number of concurrent transfer threads")

		// Operation options
		dryRun   = flag.Bool("dry-run", false, "Show what would be transferred without actually sending")
		testConn = flag.Bool("test-conn", false, "Test connection and exit")
		showHelp = flag.Bool("help", false, "Show usage examples and exit")

		// History configuration
		useShortHashLen = flag.Int("useshorthashlen", 7, "Short hash length for history storage (2-7, default: 7)")
	)
	flag.Parse()

	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}

	// Validate required flags
	if *hostnamePath == "" {
		log.Fatalf("Error: -nntphostname must be set!")
	}

	if *transferGroup == "" {
		log.Fatalf("Error: -group must be set!")
	}

	// Validate batch size
	if *batchSize < 1 || *batchSize > 100 {
		log.Fatalf("Error: batch-size must be between 1 and 100 (got %d)", *batchSize)
	}

	// Validate thread count
	if *maxThreads < 1 || *maxThreads > 32 {
		log.Fatalf("Error: max-threads must be between 1 and 32 (got %d)", *maxThreads)
	}

	// Validate UseShortHashLen
	if *useShortHashLen < 2 || *useShortHashLen > 7 {
		log.Fatalf("Invalid UseShortHashLen: %d (must be between 2 and 7)", *useShortHashLen)
	}

	// Test connection if requested
	if *testConn {
		if err := testConnection(host, port, username, password, ssl, timeout); err != nil {
			log.Fatalf("Connection test failed: %v", err)
		}
		log.Printf("Connection test successful!")
		os.Exit(0)
	}
	for _, header := range IgnoreHeaders {
		IgnoreHeadersMap[header] = true
	}

	// Initialize configuration
	mainConfig := config.NewDefaultConfig()
	mainConfig.Server.Hostname = *hostnamePath

	// Initialize database (default config, data in ./data)
	db, err := database.OpenDatabase(nil)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Set up cross-platform signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt) // Cross-platform (Ctrl+C on both Windows and Linux)

	db.WG.Add(1) // Add for this transfer process

	// Get UseShortHashLen from database (with safety check)
	storedUseShortHashLen, isLocked, err := db.GetHistoryUseShortHashLen(*useShortHashLen)
	if err != nil {
		log.Fatalf("Failed to get UseShortHashLen from database: %v", err)
	}
	var finalUseShortHashLen int
	if !isLocked {
		// First run: store the provided value
		finalUseShortHashLen = *useShortHashLen
		err = db.SetHistoryUseShortHashLen(finalUseShortHashLen)
		if err != nil {
			log.Fatalf("Failed to store UseShortHashLen in database: %v", err)
		}
		log.Printf("First run: UseShortHashLen set to %d and stored in database", finalUseShortHashLen)
	} else {
		// Subsequent runs: use stored value and warn if different
		finalUseShortHashLen = storedUseShortHashLen
		if *useShortHashLen != finalUseShortHashLen {
			log.Printf("WARNING: Command-line UseShortHashLen (%d) differs from stored value (%d). Using stored value to prevent data corruption.", *useShortHashLen, finalUseShortHashLen)
		}
		log.Printf("Using stored UseShortHashLen: %d", finalUseShortHashLen)
	}

	// Create target server connection pool
	targetProvider := &config.Provider{
		Name:       "transfer-target",
		Host:       *host,
		Port:       *port,
		SSL:        *ssl,
		Username:   *username,
		Password:   *password,
		MaxConns:   *maxThreads,
		Enabled:    true,
		Priority:   1,
		MaxArtSize: 0, // No size limit for transfers
	}

	backendConfig := &nntp.BackendConfig{
		Host:           *host,
		Port:           *port,
		SSL:            *ssl,
		Username:       *username,
		Password:       *password,
		MaxConns:       *maxThreads,
		Provider:       targetProvider,
		ConnectTimeout: time.Duration(*timeout) * time.Second,
	}

	pool := nntp.NewPool(backendConfig)
	pool.StartCleanupWorker(5 * time.Second)
	defer pool.ClosePool()

	log.Printf("Created connection pool for target server '%s:%d' with max %d connections", *host, *port, *maxThreads)

	// Get newsgroups to transfer
	newsgroups, err := getNewsgroupsToTransfer(db, *transferGroup)
	if err != nil {
		log.Fatalf("Failed to get newsgroups: %v", err)
	}

	if len(newsgroups) == 0 {
		log.Printf("No newsgroups found matching pattern: %s", *transferGroup)
		os.Exit(0)
	}

	log.Printf("Found %d newsgroups to transfer", len(newsgroups))

	// Initialize processor for article handling
	processor.LocalHostnamePath = *hostnamePath
	proc := processor.NewProcessor(db, pool, finalUseShortHashLen)
	if proc == nil {
		log.Fatalf("Failed to create processor")
	}

	// Set up shutdown handling
	shutdownChan := make(chan struct{})
	transferDoneChan := make(chan error, 1)

	// Start transfer process
	go func() {
		transferDoneChan <- runTransfer(db, pool, newsgroups, *batchSize, *dryRun, shutdownChan)
	}()

	// Wait for either shutdown signal or transfer completion
	select {
	case <-sigChan:
		log.Printf("Received shutdown signal, initiating graceful shutdown...")
		close(shutdownChan)
	case err := <-transferDoneChan:
		if err != nil {
			log.Printf("Transfer completed with error: %v", err)
		} else {
			log.Printf("Transfer completed successfully")
		}
	}

	// Close processor
	if proc != nil {
		if err := proc.Close(); err != nil {
			log.Printf("Warning: Failed to close processor: %v", err)
		} else {
			log.Printf("Processor closed successfully")
		}
	}

	// Wait for database operations to complete
	db.WG.Done()
	db.WG.Wait()

	// Shutdown database
	if err := db.Shutdown(); err != nil {
		log.Printf("Failed to shutdown database: %v", err)
		os.Exit(1)
	} else {
		log.Printf("Database shutdown successfully")
	}

	log.Printf("Graceful shutdown completed. Exiting.")
}

// testConnection tests the connection to the target NNTP server
func testConnection(host *string, port *int, username *string, password *string, ssl *bool, timeout *int) error {
	testProvider := &config.Provider{
		Name:     "test",
		Host:     *host,
		Port:     *port,
		SSL:      *ssl,
		Username: *username,
		Password: *password,
		MaxConns: 1,
		Enabled:  true,
		Priority: 1,
	}

	backendConfig := &nntp.BackendConfig{
		Host:           *host,
		Port:           *port,
		SSL:            *ssl,
		Username:       *username,
		Password:       *password,
		MaxConns:       1,
		Provider:       testProvider,
		ConnectTimeout: time.Duration(*timeout) * time.Second,
	}

	fmt.Printf("Testing connection to %s:%d (SSL: %v)\n", *host, *port, *ssl)
	if *username != "" {
		fmt.Printf("Authentication: %s\n", *username)
	} else {
		fmt.Println("Authentication: None")
	}

	// Test connection
	client := nntp.NewConn(backendConfig)
	start := time.Now()
	err := client.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}
	fmt.Printf("✓ Connection successful (took %v)\n", time.Since(start))
	client.CloseFromPoolOnly() // only use this in a test!

	return nil
}

// getNewsgroupsToTransfer returns newsgroups matching the specified pattern
func getNewsgroupsToTransfer(db *database.Database, groupPattern string) ([]*models.Newsgroup, error) {
	var newsgroups []*models.Newsgroup

	// Handle wildcard patterns
	suffixWildcard := strings.HasSuffix(groupPattern, "*")
	var wildcardPrefix string

	if suffixWildcard {
		wildcardPrefix = strings.TrimSuffix(groupPattern, "*")
		log.Printf("Using wildcard newsgroup prefix: '%s'", wildcardPrefix)
	}

	// Get all newsgroups from database
	allNewsgroups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return nil, fmt.Errorf("failed to get newsgroups from database: %v", err)
	}

	// Filter newsgroups based on pattern
	if suffixWildcard {
		for _, ng := range allNewsgroups {
			if strings.HasPrefix(ng.Name, wildcardPrefix) {
				newsgroups = append(newsgroups, ng)
			}
		}
	} else {
		// Exact match
		for _, ng := range allNewsgroups {
			if ng.Name == groupPattern {
				newsgroups = append(newsgroups, ng)
				break
			}
		}
	}

	return newsgroups, nil
}

// runTransfer performs the actual article transfer process
func runTransfer(db *database.Database, pool *nntp.Pool, newsgroups []*models.Newsgroup, batchSize int, dryRun bool, shutdownChan <-chan struct{}) error {
	transferSemaphore := make(chan struct{}, pool.Backend.MaxConns)

	totalTransferred := 0
	totalChecked := 0
	var transferMutex sync.Mutex

	for _, newsgroup := range newsgroups {
		select {
		case <-shutdownChan:
			log.Printf("Shutdown requested, stopping transfer")
			return nil
		default:
		}

		log.Printf("Starting transfer for newsgroup: %s", newsgroup.Name)

		// Acquire semaphore
		transferSemaphore <- struct{}{}
		defer func() { <-transferSemaphore }()

		transferred, checked, err := transferNewsgroup(db, pool, newsgroup, batchSize, dryRun, shutdownChan)

		transferMutex.Lock()
		totalTransferred += transferred
		totalChecked += checked
		transferMutex.Unlock()

		if err != nil {
			log.Printf("Error transferring newsgroup %s: %v", newsgroup.Name, err)
		} else {
			log.Printf("Completed transfer for newsgroup %s: %d articles transferred, %d articles checked",
				newsgroup.Name, transferred, checked)
		}
	}

	// Wait for all transfers to complete

	log.Printf("Transfer summary: %d total articles transferred, %d total articles checked", totalTransferred, totalChecked)
	return nil
}

// transferNewsgroup transfers articles from a single newsgroup
func transferNewsgroup(db *database.Database, pool *nntp.Pool, newsgroup *models.Newsgroup, batchSize int, dryRun bool, shutdownChan <-chan struct{}) (int, int, error) {

	// Get group database
	groupDBs, err := db.GetGroupDBs(newsgroup.Name)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get group DBs for newsgroup '%s': %v", newsgroup.Name, err)
	}
	defer func() {
		if ferr := db.ForceCloseGroupDBs(groupDBs); ferr != nil {
			log.Printf("ForceCloseGroupDBs error for '%s': %v", newsgroup.Name, ferr)
		}
	}()

	// Get total article count first
	totalArticles, err := db.GetArticleCountFromMainDB(newsgroup.Name)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get article count for newsgroup '%s': %v", newsgroup.Name, err)
	}

	if totalArticles == 0 {
		log.Printf("No articles found in newsgroup: %s", newsgroup.Name)
		return 0, 0, nil
	}

	log.Printf("Found %d articles in newsgroup %s - processing in batches", totalArticles, newsgroup.Name)

	if dryRun {
		log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s", totalArticles, newsgroup.Name)
		return 0, int(totalArticles), nil
	}

	// Get connection from pool
	conn, err := pool.Get()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get connection from pool: %v", err)
	}
	defer pool.Put(conn)

	transferred := 0
	checked := 0

	// Process articles in database batches (much larger than network batches)
	const dbBatchSize = 1000 // Load 1000 articles from DB at a time
	for offset := 0; offset < int(totalArticles); offset += dbBatchSize {
		select {
		case <-shutdownChan:
			log.Printf("Shutdown requested, stopping transfer for newsgroup: %s", newsgroup.Name)
			return transferred, checked, nil
		default:
		}

		// Load batch from database
		articles, err := db.GetArticlesBatch(groupDBs, dbBatchSize, offset)
		if err != nil {
			log.Printf("Error loading article batch (offset %d) for newsgroup %s: %v", offset, newsgroup.Name, err)
			continue
		}

		if len(articles) == 0 {
			log.Printf("No more articles in newsgroup %s (offset %d)", newsgroup.Name, offset)
			break
		}

		log.Printf("Newsgroup %s: Loaded %d articles from database (offset %d-%d)", newsgroup.Name, len(articles), offset, offset+len(articles)-1)

		// Process articles in network batches
		for i := 0; i < len(articles); i += batchSize {
			select {
			case <-shutdownChan:
				log.Printf("Shutdown requested, stopping transfer for newsgroup: %s", newsgroup.Name)
				return transferred, checked, nil
			default:
			}

			end := i + batchSize
			if end > len(articles) {
				end = len(articles)
			}

			batch := articles[i:end]
			batchTransferred, err := processBatch(conn, batch)
			if err != nil {
				log.Printf("Error processing network batch for newsgroup %s: %v", newsgroup.Name, err)
				continue
			}

			transferred += batchTransferred
			checked += len(batch)

			log.Printf("Newsgroup %s: Network batch %d-%d processed, %d transferred in this batch", newsgroup.Name, i+1, end, batchTransferred)
		}

		// Clear articles slice to free memory
		for i := range articles {
			articles[i] = nil
		}
		articles = nil

		log.Printf("Newsgroup %s: Database batch complete (offset %d), total transferred so far: %d/%d", newsgroup.Name, offset, transferred, checked)
	}

	return transferred, checked, nil
}

// Global variables to track TAKETHIS success rate for streaming protocol
var (
	takeThisSuccessCount int
	takeThisTotalCount   int
	useCheckMode         bool // Start with TAKETHIS mode (false)
)

// processBatch processes a batch of articles using NNTP streaming protocol (RFC 4644)
// Uses TAKETHIS primarily, falls back to CHECK when success rate < 95%
func processBatch(conn *nntp.BackendConn, articles []*models.Article) (int, error) {

	if len(articles) == 0 {
		return 0, nil
	}

	// Calculate success rate to determine whether to use CHECK or TAKETHIS
	var successRate float64 = 100.0 // Start optimistic
	if takeThisTotalCount > 0 {
		successRate = float64(takeThisSuccessCount) / float64(takeThisTotalCount) * 100.0
	}

	// Switch to CHECK mode if TAKETHIS success rate drops below 95%
	if successRate < 95.0 && takeThisTotalCount >= 10 { // Need at least 10 attempts for meaningful stats
		useCheckMode = true
		log.Printf("TAKETHIS success rate %.1f%% < 95%%, switching to CHECK mode", successRate)
	} else if successRate >= 98.0 && takeThisTotalCount >= 20 { // Switch back when rate improves
		useCheckMode = false
		log.Printf("TAKETHIS success rate %.1f%% >= 98%%, switching back to TAKETHIS mode", successRate)
	}

	articleMap := make(map[string]*models.Article)
	for _, article := range articles {
		articleMap[article.MessageID] = article
	}

	transferred := 0

	if useCheckMode {
		// CHECK mode: verify articles are wanted before sending
		log.Printf("Using CHECK mode for %d articles (success rate: %.1f%%)", len(articles), successRate)

		messageIds := make([]string, len(articles))
		for i, article := range articles {
			messageIds[i] = article.MessageID
		}

		// Send CHECK commands for all message IDs
		checkResponses, err := conn.CheckMultiple(messageIds)
		if err != nil {
			return 0, fmt.Errorf("failed to send CHECK command: %v", err)
		}

		// Find wanted articles
		wantedIds := make([]string, 0)
		for _, response := range checkResponses {
			if response.Wanted {
				wantedIds = append(wantedIds, response.MessageID)
			} else {
				log.Printf("Article %s not wanted by server: %d %s", response.MessageID, response.Code, response.Message)
			}
		}

		if len(wantedIds) == 0 {
			log.Printf("No articles wanted by server in this batch")
			return 0, nil
		}

		log.Printf("Server wants %d out of %d articles in batch", len(wantedIds), len(messageIds))

		// Send TAKETHIS for wanted articles
		for _, msgId := range wantedIds {
			count, err := sendArticleViaTakeThis(conn, articleMap[msgId])
			if err != nil {
				log.Printf("Failed to send TAKETHIS for %s: %v", msgId, err)
				continue
			}
			transferred += count
		}
	} else {
		// TAKETHIS mode: send articles directly and track success rate
		log.Printf("Using TAKETHIS mode for %d articles (success rate: %.1f%%)", len(articles), successRate)

		transferred, err := sendArticlesBatchViaTakeThis(conn, articles)
		if err != nil {
			return 0, fmt.Errorf("failed to send TAKETHIS batch: %v", err)
		}
		return transferred, nil
	}

	return transferred, nil
}

// sendArticlesBatchViaTakeThis sends multiple articles via TAKETHIS in streaming mode
// Sends all TAKETHIS commands first, then reads all responses (true streaming)
func sendArticlesBatchViaTakeThis(conn *nntp.BackendConn, articles []*models.Article) (int, error) {
	if len(articles) == 0 {
		return 0, nil
	}

	// Phase 1: Send all TAKETHIS commands without waiting for responses
	log.Printf("Phase 1: Sending %d TAKETHIS commands...", len(articles))

	commandIDs := make([]uint, 0, len(articles))
	validArticles := make([]*models.Article, 0, len(articles))

	for _, article := range articles {
		// Reconstruct headers for transmission
		articleHeaders, err := reconstructHeaders(article)
		if err != nil {
			log.Printf("Failed to reconstruct headers for %s: %v", article.MessageID, err)
			continue
		}

		// Send TAKETHIS command with article content (non-blocking)
		cmdID, err := conn.SendTakeThisArticleStreaming(article.MessageID, articleHeaders, article.BodyText)
		if err != nil {
			log.Printf("Failed to send TAKETHIS for %s: %v", article.MessageID, err)
			continue
		}

		commandIDs = append(commandIDs, cmdID)
		validArticles = append(validArticles, article)
	}

	log.Printf("Sent %d TAKETHIS commands, reading responses...", len(commandIDs))

	// Phase 2: Read all responses in order
	transferred := 0
	for i, cmdID := range commandIDs {
		article := validArticles[i]

		takeThisResponse, err := conn.ReadTakeThisResponseStreaming(cmdID)
		if err != nil {
			log.Printf("Failed to read TAKETHIS response for %s: %v", article.MessageID, err)
			continue
		}

		// Update success rate tracking
		takeThisTotalCount++
		if takeThisResponse.Success {
			takeThisSuccessCount++
			transferred++
		} else {
			log.Printf("Failed to transfer article %s: %d", takeThisResponse.MessageID, takeThisResponse.Code)
		}
	}

	log.Printf("Batch transfer complete: %d/%d articles transferred successfully", transferred, len(articles))
	return transferred, nil
}

// sendArticleViaTakeThis sends a single article via TAKETHIS and tracks success rate
func sendArticleViaTakeThis(conn *nntp.BackendConn, article *models.Article) (int, error) {

	// Reconstruct headers for transmission
	articleHeaders, err := reconstructHeaders(article)
	if err != nil {
		return 0, fmt.Errorf("failed to reconstruct headers: %v", err)
	}

	// Send TAKETHIS command with article content
	takeThisResponse, err := conn.TakeThisArticle(article.MessageID, articleHeaders, article.BodyText)
	if err != nil {
		return 0, fmt.Errorf("failed to send TAKETHIS: %v", err)
	}

	// Update success rate tracking
	takeThisTotalCount++
	if takeThisResponse.Success {
		takeThisSuccessCount++
		//log.Printf("Successfully transferred article: %s", article.MessageID)
		return 1, nil
	} else {
		log.Printf("Failed to transfer article %s: %d", article.MessageID, takeThisResponse.Code)
		return 0, nil
	}
}

// isRFC822Compliant checks if a date string is RFC 822/1123 compliant for Usenet
func isRFC822Compliant(dateStr string) bool {
	// Try to parse with common RFC formats used in Usenet
	formats := []string{
		time.RFC1123,                     // "Mon, 02 Jan 2006 15:04:05 MST"
		time.RFC1123Z,                    // "Mon, 02 Jan 2006 15:04:05 -0700"
		time.RFC822,                      // "02 Jan 06 15:04 MST"
		time.RFC822Z,                     // "02 Jan 06 15:04 -0700"
		"Mon, 2 Jan 2006 15:04:05 MST",   // Single digit day
		"Mon, 2 Jan 2006 15:04:05 -0700", // Single digit day with timezone
	}

	for _, format := range formats {
		if _, err := time.Parse(format, dateStr); err == nil {
			return true
		}
	}
	return false
}

// reconstructHeaders reconstructs the header lines from an article for transmission
func reconstructHeaders(article *models.Article) ([]string, error) {
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
		if isRFC822Compliant(article.DateString) {
			dateHeader = article.DateString
		} else {
			// DateString is not RFC compliant, use DateSent instead
			if !article.DateSent.IsZero() {
				dateHeader = article.DateSent.UTC().Format(time.RFC1123)
				log.Printf("Using DateSent instead of non-compliant DateString for article %s", article.MessageID)
			} else {
				return nil, fmt.Errorf("article has non-compliant DateString and zero DateSent")
			}
		}
	} else {
		// No DateString, try DateSent
		if !article.DateSent.IsZero() {
			dateHeader = article.DateSent.UTC().Format(time.RFC1123)
		} else {
			return nil, fmt.Errorf("article missing Date header (both DateString and DateSent are empty)")
		}
	}

	if article.References == "" {
		return nil, fmt.Errorf("article missing References header")
	}
	if article.Path == "" {
		return nil, fmt.Errorf("article missing Path header")
	}
	headers = append(headers, "Message-ID: "+article.MessageID)
	headers = append(headers, "Subject: "+article.Subject)
	headers = append(headers, "From: "+article.FromHeader)
	headers = append(headers, "Date: "+dateHeader)
	headers = append(headers, "References: "+article.References)
	headers = append(headers, "Path: "+article.Path)
	moreHeaders := strings.Split(article.HeadersJSON, "\n")
	ignoreLine := false
	isSpacedLine := false
	ignoredLines := 0
	headersMap := make(map[string]bool)

	for i, headerLine := range moreHeaders {
		if len(headerLine) == 0 {
			log.Printf("Empty headerline=%d in msgId='%s'", i, article.MessageID)
			continue
		}
		isSpacedLine = strings.HasPrefix(headerLine, " ") || strings.HasPrefix(headerLine, "\t")
		if isSpacedLine && ignoreLine {
			ignoredLines++
			continue
		} else {
			ignoreLine = false
		}
		if !isSpacedLine {
			// check if first char is lowercase
			if unicode.IsLower(rune(headerLine[0])) {
				log.Printf("Lowercase header: '%s' line=%d in msgId='%s'", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
				continue
			}
			header := strings.SplitN(headerLine, ":", 2)[0]
			if len(header) == 0 {
				log.Printf("Invalid header: '%s' line=%d in msgId='%s'", headerLine, i, article.MessageID)
				ignoreLine = true
				ignoredLines++
			}
			if IgnoreHeadersMap[header] {
				ignoreLine = true
				continue
			}
			if headersMap[header] {
				log.Printf("Duplicate header: '%s' line=%d in msgId='%s'", headerLine, i, article.MessageID)
				ignoreLine = true
				continue
			}
			headersMap[header] = true
		}
		headers = append(headers, headerLine)
	}
	if ignoredLines > 0 {
		log.Printf("Ignored %d lines while reconstructing headers for msgId='%s'", ignoredLines, article.MessageID)
	}
	log.Printf("Reconstructed %d headers, ignored %d lines for msgId='%s'", len(headers), ignoredLines, article.MessageID)
	return headers, nil
}
