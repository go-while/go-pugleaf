// NNTP article transfer tool for go-pugleaf
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var dbBatchSize int64 = 1000 // Load 1000 articles from DB at a time

// showUsageExamples displays usage examples for NNTP transfer
func showUsageExamples() {
	fmt.Println("\n=== NNTP Transfer Tool - Usage Examples ===")
	fmt.Println("The NNTP transfer tool sends articles via CHECK/TAKETHIS commands.")
	fmt.Println()
	fmt.Println("Connection Configuration:")
	fmt.Println("  ./nntp-transfer -host news.server.local -group news.admin.*")
	fmt.Println("  ./nntp-transfer -host news.server.local -username user -password pass -group alt.test")
	fmt.Println("  ./nntp-transfer -host news.server.local -port 119 -ssl=false -group alt.test")
	fmt.Println()
	fmt.Println("Proxy Configuration:")
	fmt.Println("  ./nntp-transfer -host news.server.local -socks5 127.0.0.1:9050 -group alt.test")
	fmt.Println("  ./nntp-transfer -host news.server.local -socks4 proxy.example.com:1080 -group alt.test")
	fmt.Println("  ./nntp-transfer -host news.server.local -socks5 proxy.example.com:1080 -proxy-username user -proxy-password pass -group alt.test")
	fmt.Println()
	fmt.Println("Performance Tuning:")
	fmt.Println("  ./nntp-transfer -host news.server.local -max-threads 4 -group alt.*")
	fmt.Println()
	fmt.Println("Date Filtering:")
	fmt.Println("  ./nntp-transfer -host news.server.local -date-beg 2024-01-01 -group alt.test")
	fmt.Println("  ./nntp-transfer -host news.server.local -date-end 2024-12-31 -group alt.test")
	fmt.Println("  ./nntp-transfer -host news.server.local -date-beg 2024-01-01T00:00:00 -date-end 2024-01-31T23:59:59 -group alt.test")
	fmt.Println()
	fmt.Println("Dry Run Mode:")
	fmt.Println("  ./nntp-transfer -host news.server.local -dry-run -group alt.test")
	fmt.Println()

	fmt.Println("Show ALL command line flags:")
	fmt.Println("  ./nntp-transfer -h")
	fmt.Println()
}

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	database.NO_CACHE_BOOT = true // prevents booting caches
	log.Printf("Starting go-pugleaf NNTP Transfer Tool (version %s)", config.AppVersion)

	// Command line flags for NNTP transfer configuration
	var (
		// Required flags
		transferGroup = flag.String("group", "", "Newsgroup to transfer (supports wildcards like alt.* or news.admin.*)")

		// Connection configuration
		host     = flag.String("host", "", "Target NNTP hostname")
		port     = flag.Int("port", 563, "Target NNTP port (common: 119 -ssl=false OR 563 -ssl=true)")
		username = flag.String("username", "", "Target NNTP username")
		password = flag.String("password", "", "Target NNTP password")
		ssl      = flag.Bool("ssl", true, "Use SSL/TLS connection")
		timeout  = flag.Int("timeout", 30, "Connection timeout in seconds")

		// Proxy configuration
		proxySocks4   = flag.String("socks4", "", "SOCKS4 proxy address (host:port)")
		proxySocks5   = flag.String("socks5", "", "SOCKS5 proxy address (host:port)")
		proxyUsername = flag.String("proxy-username", "", "Proxy authentication username")
		proxyPassword = flag.String("proxy-password", "", "Proxy authentication password")

		// Transfer configuration
		batchCheck = flag.Int("batch-check", 25, "Number of message-IDs to send in a single CHECK command")
		batchDB    = flag.Int64("batch-db", 1000, "Fetch N articles from DB in a batch")
		maxThreads = flag.Int("max-threads", 1, "Transfer N newsgroups in concurrent threads. Each thread uses 1 connection.")

		// Operation options
		dryRun   = flag.Bool("dry-run", false, "Show what would be transferred without actually sending")
		testConn = flag.Bool("test-conn", false, "Test connection and exit")
		showHelp = flag.Bool("help", false, "Show usage examples and exit")

		// Date filtering options
		startDate = flag.String("date-beg", "", "Start date for article transfer (format: 2006-01-02 [YYYY-MM-DD] or 2006-01-02T15:04:05)")
		endDate   = flag.String("date-end", "", "End date for article transfer (format: 2006-01-02 [YYYY-MM-DD] or 2006-01-02T15:04:05)")

		// History configuration
		useShortHashLen = flag.Int("useshorthashlen", 7, "Short hash length for history storage (2-7, default: 7)")
	)
	flag.Parse()

	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}

	if *transferGroup == "" {
		log.Fatalf("Error: -group must be set!")
	}

	// Validate batch size
	if *batchCheck < 1 || *batchCheck > 100 {
		log.Fatalf("Error: batch-check must be between 1 and 100 (got %d)", *batchCheck)
	}

	// Validate batch size
	if *batchDB < 100 {
		*batchDB = 100
	}
	dbBatchSize = *batchDB

	// Validate thread count
	if *maxThreads < 1 || *maxThreads > 500 {
		log.Fatalf("Error: max-threads must be between 1 and 500 (got %d)", *maxThreads)
	}

	// Validate UseShortHashLen
	if *useShortHashLen < 2 || *useShortHashLen > 7 {
		log.Fatalf("Invalid UseShortHashLen: %d (must be between 2 and 7)", *useShortHashLen)
	}

	// Parse and validate date filters
	var startTime, endTime *time.Time
	if *startDate != "" {
		parsed, err := parseDateTime(*startDate)
		if err != nil {
			log.Fatalf("Invalid start-date format: %v. Use format: 2006-01-02 or 2006-01-02T15:04:05", err)
		}
		startTime = &parsed
		log.Printf("Filtering articles from: %s", startTime.Format("2006-01-02 15:04:05"))
	}
	if *endDate != "" {
		parsed, err := parseDateTime(*endDate)
		if err != nil {
			log.Fatalf("Invalid end-date format: %v. Use format: 2006-01-02 or 2006-01-02T15:04:05", err)
		}
		endTime = &parsed
		log.Printf("Filtering articles to: %s", endTime.Format("2006-01-02 15:04:05"))
	}
	if startTime != nil && endTime != nil && startTime.After(*endTime) {
		log.Fatalf("Start date (%s) cannot be after end date (%s)", startTime.Format("2006-01-02"), endTime.Format("2006-01-02"))
	}

	// Parse and validate proxy configuration
	var proxyConfig *ProxyConfig
	if *proxySocks4 != "" && *proxySocks5 != "" {
		log.Fatalf("Cannot specify both SOCKS4 and SOCKS5 proxy")
	}
	if *proxySocks4 != "" {
		config, err := parseProxyConfig(*proxySocks4, "socks4", *proxyUsername, *proxyPassword)
		if err != nil {
			log.Fatalf("Invalid SOCKS4 proxy configuration: %v", err)
		}
		proxyConfig = config
		log.Printf("Using SOCKS4 proxy: %s:%d", proxyConfig.Host, proxyConfig.Port)
	}
	if *proxySocks5 != "" {
		config, err := parseProxyConfig(*proxySocks5, "socks5", *proxyUsername, *proxyPassword)
		if err != nil {
			log.Fatalf("Invalid SOCKS5 proxy configuration: %v", err)
		}
		proxyConfig = config
		log.Printf("Using SOCKS5 proxy: %s:%d", proxyConfig.Host, proxyConfig.Port)
	}

	// Test connection if requested
	if *testConn {
		if err := testConnection(host, port, username, password, ssl, timeout, proxyConfig); err != nil {
			log.Fatalf("Connection test failed: %v", err)
		}
		log.Printf("Connection test successful!")
		os.Exit(0)
	}

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
		Name:       "transfer:" + *host,
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

	// Apply proxy configuration if specified
	if proxyConfig != nil {
		backendConfig.ProxyEnabled = proxyConfig.Enabled
		backendConfig.ProxyType = proxyConfig.Type
		backendConfig.ProxyHost = proxyConfig.Host
		backendConfig.ProxyPort = proxyConfig.Port
		backendConfig.ProxyUsername = proxyConfig.Username
		backendConfig.ProxyPassword = proxyConfig.Password
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
	proc := processor.NewProcessor(db, pool, finalUseShortHashLen)
	if proc == nil {
		log.Fatalf("Failed to create processor")
	}

	// Set up shutdown handling
	shutdownChan := make(chan struct{})
	transferDoneChan := make(chan error, 1)

	// Start transfer process
	go func() {
		transferDoneChan <- runTransfer(db, proc, pool, newsgroups, *batchCheck, *maxThreads, *dryRun, startTime, endTime, shutdownChan)
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

// parseDateTime parses a date string in multiple supported formats
func parseDateTime(dateStr string) (time.Time, error) {
	// Try different date formats
	formats := []string{
		"2006-01-02",           // YYYY-MM-DD
		"2006-01-02T15:04:05",  // YYYY-MM-DDTHH:MM:SS
		"2006-01-02 15:04:05",  // YYYY-MM-DD HH:MM:SS
		"2006-01-02T15:04:05Z", // YYYY-MM-DDTHH:MM:SSZ
	}

	for _, format := range formats {
		if parsed, err := time.Parse(format, dateStr); err == nil {
			return parsed, nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported date format: %s", dateStr)
}

// ProxyConfig holds proxy configuration parsed from command line flags
type ProxyConfig struct {
	Enabled  bool
	Type     string // "socks4" or "socks5"
	Host     string
	Port     int
	Username string
	Password string
}

// parseProxyConfig parses proxy address (host:port) and creates proxy configuration
func parseProxyConfig(address, proxyType, username, password string) (*ProxyConfig, error) {
	if address == "" {
		return nil, fmt.Errorf("proxy address cannot be empty")
	}

	// Parse host:port
	parts := strings.Split(address, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("proxy address must be in format host:port, got: %s", address)
	}

	host := parts[0]
	if host == "" {
		return nil, fmt.Errorf("proxy host cannot be empty")
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid proxy port: %s", parts[1])
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("proxy port must be between 1 and 65535, got: %d", port)
	}

	return &ProxyConfig{
		Enabled:  true,
		Type:     proxyType,
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
	}, nil
}

const query_getArticlesBatchWithDateFilter_selectPart = `SELECT article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, path, headers_json, body_text, imported_at FROM articles`
const query_getArticlesBatchWithDateFilter_nodatefilter = `SELECT article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, path, headers_json, body_text, imported_at FROM articles ORDER BY date_sent ASC LIMIT ? OFFSET ?`
const query_getArticlesBatchWithDateFilter_orderby = " ORDER BY date_sent ASC LIMIT ? OFFSET ?"

// getArticlesBatchWithDateFilter retrieves articles from a group database with optional date filtering
func getArticlesBatchWithDateFilter(groupDBs *database.GroupDBs, offset int64, startTime, endTime *time.Time) ([]*models.Article, error) {

	var query string
	var args []interface{}

	if startTime != nil || endTime != nil {
		// Build query with date filtering

		var whereConditions []string

		if startTime != nil {
			whereConditions = append(whereConditions, "date_sent >= ?")
			args = append(args, startTime.UTC().Format("2006-01-02 15:04:05"))
		}

		if endTime != nil {
			whereConditions = append(whereConditions, "date_sent <= ?")
			args = append(args, endTime.UTC().Format("2006-01-02 15:04:05"))
		}

		whereClause := ""
		if len(whereConditions) > 0 {
			whereClause = " WHERE " + strings.Join(whereConditions, " AND ")
		}

		query = query_getArticlesBatchWithDateFilter_selectPart + whereClause + query_getArticlesBatchWithDateFilter_orderby
		args = append(args, dbBatchSize, offset)
	} else {
		// No date filtering, use original query but with date_sent ordering
		query = query_getArticlesBatchWithDateFilter_nodatefilter
		args = []interface{}{dbBatchSize, offset}
	}

	rows, err := groupDBs.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.Article
	for rows.Next() {
		var a models.Article
		var artnum int64
		if err := rows.Scan(&artnum, &a.MessageID, &a.Subject, &a.FromHeader, &a.DateSent, &a.DateString, &a.References, &a.Bytes, &a.Lines, &a.ReplyCount, &a.Path, &a.HeadersJSON, &a.BodyText, &a.ImportedAt); err != nil {
			return nil, err
		}
		a.ArticleNums = make(map[*string]int64)
		out = append(out, &a)
	}

	return out, nil
}

// getArticleCountWithDateFilter gets the total count of articles with optional date filtering
func getArticleCountWithDateFilter(groupDBs *database.GroupDBs, startTime, endTime *time.Time) (int64, error) {
	var query string
	var args []interface{}

	if startTime != nil || endTime != nil {
		// Build count query with date filtering
		var whereConditions []string

		if startTime != nil {
			whereConditions = append(whereConditions, "date_sent >= ?")
			args = append(args, startTime.UTC().Format("2006-01-02 15:04:05"))
		}

		if endTime != nil {
			whereConditions = append(whereConditions, "date_sent <= ?")
			args = append(args, endTime.UTC().Format("2006-01-02 15:04:05"))
		}

		whereClause := ""
		if len(whereConditions) > 0 {
			whereClause = " WHERE " + strings.Join(whereConditions, " AND ")
		}

		query = "SELECT COUNT(*) FROM articles" + whereClause
	} else {
		// No date filtering
		query = "SELECT COUNT(*) FROM articles"
	}

	var count int64
	err := groupDBs.DB.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// testConnection tests the connection to the target NNTP server
func testConnection(host *string, port *int, username *string, password *string, ssl *bool, timeout *int, proxyConfig *ProxyConfig) error {
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

	// Apply proxy configuration if specified
	if proxyConfig != nil {
		backendConfig.ProxyEnabled = proxyConfig.Enabled
		backendConfig.ProxyType = proxyConfig.Type
		backendConfig.ProxyHost = proxyConfig.Host
		backendConfig.ProxyPort = proxyConfig.Port
		backendConfig.ProxyUsername = proxyConfig.Username
		backendConfig.ProxyPassword = proxyConfig.Password
	}

	fmt.Printf("Testing connection to %s:%d (SSL: %v)\n", *host, *port, *ssl)
	if *username != "" {
		fmt.Printf("Authentication: %s\n", *username)
	} else {
		fmt.Println("Authentication: None")
	}
	if proxyConfig != nil {
		fmt.Printf("Proxy: %s %s:%d\n", strings.ToUpper(proxyConfig.Type), proxyConfig.Host, proxyConfig.Port)
		if proxyConfig.Username != "" {
			fmt.Printf("Proxy Authentication: %s\n", proxyConfig.Username)
		}
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
func runTransfer(db *database.Database, proc *processor.Processor, pool *nntp.Pool, newsgroups []*models.Newsgroup, batchCheck int, maxThreads int, dryRun bool, startTime, endTime *time.Time, shutdownChan <-chan struct{}) error {

	var totalTransferred int64
	var transferMutex sync.Mutex
	maxThreadsChan := make(chan struct{}, maxThreads)
	var wg sync.WaitGroup
	// Process each newsgroup
	log.Printf("Starting transfer for %d newsgroups", len(newsgroups))
	time.Sleep(3 * time.Second)
	for _, newsgroup := range newsgroups {
		if proc.WantShutdown(shutdownChan) {
			log.Printf("Shutdown requested, stopping transfer. Total transferred: %d articles", totalTransferred)
			return nil
		}
		maxThreadsChan <- struct{}{} // acquire a thread slot
		wg.Add(1)
		go func(ng *models.Newsgroup, wg *sync.WaitGroup) {
			defer func(wg *sync.WaitGroup) {
				wg.Done()
				<-maxThreadsChan // release the thread slot
			}(wg)
			if proc.WantShutdown(shutdownChan) {
				return
			}
			start := time.Now()
			log.Printf("Starting transfer for newsgroup: %s", newsgroup.Name)
			transferred, err := transferNewsgroup(db, proc, pool, newsgroup, batchCheck, dryRun, startTime, endTime, shutdownChan)

			transferMutex.Lock()
			totalTransferred += transferred
			transferMutex.Unlock()

			if err != nil {
				log.Printf("Error transferring newsgroup %s: %v", newsgroup.Name, err)
			} else {
				log.Printf("Completed transfer for newsgroup %s: transferred %d articles. took %v",
					newsgroup.Name, transferred, time.Since(start))
			}
		}(newsgroup, &wg)
	}

	// Wait for all transfers to complete
	wg.Wait()

	log.Printf("Transfer summary: %d articles transferred", totalTransferred)
	return nil
}

// transferNewsgroup transfers articles from a single newsgroup
func transferNewsgroup(db *database.Database, proc *processor.Processor, pool *nntp.Pool, newsgroup *models.Newsgroup, batchCheck int, dryRun bool, startTime, endTime *time.Time, shutdownChan <-chan struct{}) (int64, error) {

	// Get group database
	groupDBs, err := db.GetGroupDBs(newsgroup.Name)
	if err != nil {
		return 0, fmt.Errorf("failed to get group DBs for newsgroup '%s': %v", newsgroup.Name, err)
	}
	defer func() {
		if ferr := db.ForceCloseGroupDBs(groupDBs); ferr != nil {
			log.Printf("ForceCloseGroupDBs error for '%s': %v", newsgroup.Name, ferr)
		}
	}()

	// Get total article count first with date filtering
	totalArticles, err := getArticleCountWithDateFilter(groupDBs, startTime, endTime)
	if err != nil {
		return 0, fmt.Errorf("failed to get article count for newsgroup '%s': %v", newsgroup.Name, err)
	}

	if totalArticles == 0 {
		if startTime != nil || endTime != nil {
			log.Printf("No articles found in newsgroup: %s (within specified date range)", newsgroup.Name)
		} else {
			log.Printf("No articles found in newsgroup: %s", newsgroup.Name)
		}
		return 0, nil
	}

	if dryRun {
		if startTime != nil || endTime != nil {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s (within specified date range)", totalArticles, newsgroup.Name)
		} else {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s", totalArticles, newsgroup.Name)
		}
		return 0, nil
	}

	if startTime != nil || endTime != nil {
		log.Printf("Found %d articles in newsgroup %s (within specified date range) - processing in batches", totalArticles, newsgroup.Name)
	} else {
		log.Printf("Found %d articles in newsgroup %s - processing in batches", totalArticles, newsgroup.Name)
	}
	time.Sleep(3 * time.Second)
	var transferred, ioffset int64
	remainingArticles := totalArticles
	// Process articles in database batches (much larger than network batches)
	for offset := ioffset; offset < totalArticles; offset += dbBatchSize {
		if proc.WantShutdown(shutdownChan) {
			log.Printf("WantShutdown in newsgroup: %s: Transferred %d articles", newsgroup.Name, transferred)
			return transferred, nil
		}

		// Load batch from database with date filtering
		articles, err := getArticlesBatchWithDateFilter(groupDBs, offset, startTime, endTime)
		if err != nil {
			log.Printf("Error loading article batch (offset %d) for newsgroup %s: %v", offset, newsgroup.Name, err)
			continue
		}

		if len(articles) == 0 {
			log.Printf("No more articles in newsgroup %s (offset %d)", newsgroup.Name, offset)
			break
		}

		// todo verbose flag

		log.Printf("Newsgroup %s: Loaded %d articles from database (offset %d)", newsgroup.Name, len(articles), offset)
		isleep := time.Second

		// Process articles in network batches
		for i := 0; i < len(articles); i += batchCheck {
			if proc.WantShutdown(shutdownChan) {
				log.Printf("WantShutdown in newsgroup: %s: Transferred %d articles", newsgroup.Name, transferred)
				return transferred, nil
			}

			end := i + batchCheck
			if end > len(articles) {
				end = len(articles)
			}
		forever:
			for {
				if proc.WantShutdown(shutdownChan) {
					log.Printf("WantShutdown in newsgroup: %s: Transferred %d articles", newsgroup.Name, transferred)
					return transferred, nil
				}
				// Get connection from pool
				conn, err := pool.Get()
				if err != nil {
					return transferred, fmt.Errorf("failed to get connection from pool: %v", err)
				}
				batchTransferred, berr := processBatch(conn, articles[i:end])
				if berr != nil {
					conn = nil
					pool.Put(conn)
					log.Printf("Error processing network batch for newsgroup %s: %v ... retry in %v", newsgroup.Name, err, isleep)
					time.Sleep(isleep)
					isleep = time.Duration(int64(isleep) * 2)
					if isleep > time.Minute {
						isleep = time.Minute
					}
					continue forever
				}
				isleep = time.Second
				pool.Put(conn)
				transferred += batchTransferred
				log.Printf("Newsgroup %s: batch %d-%d processed (offset %d/%d) transferred %d", newsgroup.Name, i+1, end, offset, totalArticles, batchTransferred)
				break forever
			}
		}

		// Clear articles slice to free memory
		for i := range articles {
			articles[i] = nil
		}
		remainingArticles -= int64(len(articles))
		articles = nil

		// todo verbose flag
		log.Printf("Newsgroup %s: done (offset %d/%d), total transferred: %d, remainingArticles %d", newsgroup.Name, offset, totalArticles, transferred, remainingArticles)
	}

	log.Printf("Completed newsgroup %s: total transferred: %d articles / total articles: %d", newsgroup.Name, transferred, totalArticles)
	return transferred, nil
}

// Global variables to track TAKETHIS success rate for streaming protocol
var (
	takeThisSuccessCount int
	takeThisTotalCount   int
	useCheckMode         bool // Start with TAKETHIS mode (false)
)

// processBatch processes a batch of articles using NNTP streaming protocol (RFC 4644)
// Uses TAKETHIS primarily, falls back to CHECK when success rate < 95%
func processBatch(conn *nntp.BackendConn, articles []*models.Article) (int64, error) {

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

	var transferred int64

	if useCheckMode {
		// CHECK mode: verify articles are wanted before sending
		log.Printf("Using CHECK mode for %d articles (success rate: %.1f%%)", len(articles), successRate)

		messageIds := make([]*string, len(articles))
		for i, article := range articles {
			messageIds[i] = &article.MessageID
		}

		// Send CHECK commands for all message IDs
		checkResponses, err := conn.CheckMultiple(messageIds)
		if err != nil {
			return transferred, fmt.Errorf("failed to send CHECK command: %v", err)
		}

		// Find wanted articles
		wantedIds := make([]*string, 0)
		for _, response := range checkResponses {
			if response.Wanted {
				wantedIds = append(wantedIds, response.MessageID)
			} else {
				log.Printf("Article %s not wanted by server: %d", *response.MessageID, response.Code)
			}
		}

		if len(wantedIds) == 0 {
			log.Printf("No articles wanted by server in this batch")
			return transferred, nil
		}

		log.Printf("Server wants %d out of %d articles in batch", len(wantedIds), len(messageIds))

		// Send TAKETHIS for wanted articles
		for _, msgId := range wantedIds {
			count, err := sendArticleViaTakeThis(conn, articleMap[*msgId])
			if err != nil {
				log.Printf("Failed to send TAKETHIS for %s: %v", *msgId, err)
				continue
			}
			transferred += int64(count)
		}
	} else {
		// TAKETHIS mode: send articles directly and track success rate
		log.Printf("Using TAKETHIS mode for %d articles (success rate: %.1f%%)", len(articles), successRate)

		transferred, err := sendArticlesBatchViaTakeThis(conn, articles)
		if err != nil {
			return int64(transferred), fmt.Errorf("failed to send TAKETHIS batch: %v", err)
		}
		return int64(transferred), nil
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
		// Send TAKETHIS command with article content (non-blocking)
		cmdID, err := conn.SendTakeThisArticleStreaming(article, &processor.LocalNNTPHostname)
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

		takeThisResponseCode, err := conn.ReadTakeThisResponseStreaming(cmdID)
		if err != nil {
			log.Printf("Failed to read TAKETHIS response for %s: %v", article.MessageID, err)
			continue
		}

		// Update success rate tracking
		takeThisTotalCount++
		if takeThisResponseCode == 239 {
			takeThisSuccessCount++
			transferred++
		} else {
			log.Printf("Failed to transfer article %s: %d", article.MessageID, takeThisResponseCode)
		}
	}

	log.Printf("Batch transfer complete: %d/%d articles transferred successfully", transferred, len(articles))
	return transferred, nil
}

// sendArticleViaTakeThis sends a single article via TAKETHIS and tracks success rate
func sendArticleViaTakeThis(conn *nntp.BackendConn, article *models.Article) (int, error) {

	// Send TAKETHIS command with article content
	takeThisResponseCode, err := conn.TakeThisArticle(article, &processor.LocalNNTPHostname)
	if err != nil {
		return 0, fmt.Errorf("failed to send TAKETHIS: %v", err)
	}

	// Update success rate tracking
	takeThisTotalCount++
	if takeThisResponseCode == 239 {
		takeThisSuccessCount++
		//log.Printf("Successfully transferred article: %s", article.MessageID)
		return 1, nil
	} else {
		log.Printf("Failed to transfer article %s: %d", article.MessageID, takeThisResponseCode)
		return 0, nil
	}
}
