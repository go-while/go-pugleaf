// NNTP article transfer tool for go-pugleaf
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"

	"github.com/redis/go-redis/v9"
)

var dbBatchSize int64 = 1000 // Load 1000 articles from DB at a time
var VERBOSE bool

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
	fmt.Println("File-based Filtering:")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.* -file-include include.txt")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.* -file-exclude exclude.txt")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.* -file-include include.txt -file-exclude exclude.txt")
	fmt.Println("  # File format: one pattern per line, supports wildcards (*), # for comments")
	fmt.Println()
	fmt.Println("Transfer All Newsgroups:")
	fmt.Println("  ./nntp-transfer -host news.server.local -group '$all'")
	fmt.Println("  ./nntp-transfer -host news.server.local -group '$all' -file-exclude exclude.txt")
	fmt.Println()
	fmt.Println("Force Include Only Mode:")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.* -file-include include.txt -force-include-only")
	fmt.Println("  # Applies -group pattern first, then only transfers newsgroups that also match include file patterns")
	fmt.Println()
	fmt.Println("Redis Cache Management:")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.test -redis-cache=true -redis-ttl 86400")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.test -redis-clear-cache")
	fmt.Println("  # Use -redis-clear-cache to start fresh (clears all cached message IDs)")
	fmt.Println()

	fmt.Println("Show ALL command line flags:")
	fmt.Println("  ./nntp-transfer -h")
	fmt.Println()
}

var appVersion = "-unset-"

var redisCtx = context.Background()
var REDIS_TTL time.Duration = 3600 * time.Second // default 1h

var BatchCheck int

func main() {
	common.VerboseHeaders = false
	config.AppVersion = appVersion
	database.NO_CACHE_BOOT = true // prevents booting caches and several other not needed functions
	log.Printf("Starting go-pugleaf NNTP Transfer Tool (version %s)", config.AppVersion)

	// Command line flags for NNTP transfer configuration
	var (
		// Required flags
		transferGroup = flag.String("group", "", "Newsgroup to transfer (supports wildcards like alt.* or news.admin.*, or use $all for all newsgroups)")

		// Connection configuration
		host     = flag.String("host", "", "Target NNTP hostname")
		port     = flag.Int("port", 433, "Target NNTP port (common: 119 -ssl=false OR 563 -ssl=true)")
		username = flag.String("username", "", "Target NNTP username")
		password = flag.String("password", "", "Target NNTP password")
		ssl      = flag.Bool("ssl", false, "Use SSL/TLS connection")
		timeout  = flag.Int("timeout", 30, "Connection timeout in seconds")

		// Proxy configuration
		proxySocks4   = flag.String("socks4", "", "SOCKS4 proxy address (host:port)")
		proxySocks5   = flag.String("socks5", "", "SOCKS5 proxy address (host:port)")
		proxyUsername = flag.String("proxy-username", "", "Proxy authentication username")
		proxyPassword = flag.String("proxy-password", "", "Proxy authentication password")

		// Transfer configuration
		batchCheck      = flag.Int("batch-check", 100, "Number of message IDs/articles to send in streamed CHECK/TAKETHIS")
		batchDB         = flag.Int64("batch-db", 1000, "Fetch N articles from DB in a batch")
		maxThreads      = flag.Int("max-threads", 1, "Transfer N newsgroups in concurrent threads. Each thread uses 1 connection.")
		redisCache      = flag.Bool("redis-cache", true, "Use Redis caching for message IDs")
		redisAddr       = flag.String("redis-addr", "localhost:6379", "Redis server address")
		redisPass       = flag.String("redis-pass", "", "Redis server password")
		redisTTL        = flag.Uint("redis-ttl", 3600, "Redis cache TTL in seconds (default: 3600 = 1 hour)")
		redisDB         = flag.Int("redis-db", 0, "Redis database number")
		redisClearCache = flag.Bool("redis-flushdb", false, " Warning! THIS DELETES ALL CACHED DATA on startup by executing FlushDB which flushes ALL keys in the selected Redis DB!")

		// Operation options
		dryRun   = flag.Bool("dry-run", false, "Show what would be transferred without actually sending")
		testConn = flag.Bool("test-conn", false, "Test connection and exit")
		showHelp = flag.Bool("help", false, "Show usage examples and exit")

		// Date filtering options
		startDate = flag.String("date-beg", "", "Start date for article transfer (format: 2006-01-02 [YYYY-MM-DD] or 2006-01-02T15:04:05)")
		endDate   = flag.String("date-end", "", "End date for article transfer (format: 2006-01-02 [YYYY-MM-DD] or 2006-01-02T15:04:05)")

		// header filtering
		ignoreGoogleHeaders = flag.Bool("ignore-google-headers", true, "Ignores specific header: 'X-Google-*'")
		RewriteDates        = flag.Bool("rewrite-dates", false, "Rewrite invalid date headers (e.g. utzoo articles) needs '-dry-run -date-beg 1969-01-01 -date-end 1979-01-01' to see results")
		debugCapture        = flag.Bool("debug-capture", false, "Capture debug information. use with -dry-run -date-beg 1979-01-01 -date-end 1983-01-01 to see results")
		dataDir             = flag.String("data", "./data", "Directory to store database files")

		// History configuration
		useShortHashLen = flag.Int("useshorthashlen", 7, "Short hash length for history storage (2-7, default: 7)")

		// Newsgroup filtering options
		fileInclude      = flag.String("file-include", "", "File containing newsgroup patterns to include (one per line)")
		fileExclude      = flag.String("file-exclude", "", "File containing newsgroup patterns to exclude (one per line)")
		forceIncludeOnly = flag.Bool("force-include-only", false, "When set, only transfer newsgroups that match patterns in include file (ignores -group pattern)")
	)
	flag.Parse()
	common.IgnoreGoogleHeaders = *ignoreGoogleHeaders

	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}

	if *transferGroup == "" {
		log.Fatalf("Error: -group must be set!")
	}

	// Validate batch size
	if *batchCheck < 1 || *batchCheck > 10000 {
		log.Fatalf("Error: batch-check must be between 1 and 10000 (got %d)", *batchCheck)
	}
	BatchCheck = *batchCheck

	// Validate batch size
	if *batchDB < 100 {
		*batchDB = 100
	}
	dbBatchSize = *batchDB

	// Validate thread count
	if *maxThreads < 1 || *maxThreads > 500 {
		log.Fatalf("Error: max-threads must be between 1 and 500 (got %d)", *maxThreads)
	}
	nntp.NNTPTransferThreads = *maxThreads

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
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = *dataDir

	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// setup redis cache for message IDs
	REDIS_TTL = time.Duration(*redisTTL) * time.Second
	var redisCli *redis.Client = nil
	if *redisCache {
		redisCli = redis.NewClient(&redis.Options{
			Addr:     *redisAddr,
			Password: *redisPass,
			DB:       *redisDB,
		})
		if redisCli == nil {
			log.Printf("Failed to create Redis client")
		} else {
			defer redisCli.Close()

			// Test Redis connection
			if err := redisCli.Ping(redisCtx).Err(); err != nil {
				log.Printf("WARNING: Redis connection test failed: %v", err)
				log.Printf("Continuing without Redis cache...")
				redisCli = nil
			} else {
				log.Printf("Redis connection established: %s (DB: %d)", *redisAddr, *redisDB)

				// Clear Redis cache if requested
				if *redisClearCache {
					log.Printf("Clearing Redis cache (DB: %d)...", *redisDB)
					if err := redisCli.FlushDB(redisCtx).Err(); err != nil {
						log.Printf("ERROR: Failed to clear Redis cache: %v", err)
					} else {
						log.Printf("Redis cache cleared successfully")
					}
				}
			}
		}
	}

	// Set up cross-platform signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt) // Cross-platform (Ctrl+C on both Windows and Linux)

	//db.WG.Add(2) // Adds to wait group for db_batch.go cron jobs
	db.WG.Add(1) // Adds for history: one for writer worker

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
	nntphostname, err := db.GetConfigValue("local_nntp_hostname")
	if err != nil || nntphostname == "" {
		log.Printf("Failed to get local_nntp_hostname from database: %v", err)
		os.Exit(1)
	}

	pool := nntp.NewPool(backendConfig)
	log.Printf("Created connection pool for target server '%s:%d' with max %d connections", *host, *port, *maxThreads)

	// Get newsgroups to transfer
	newsgroups, err := getNewsgroupsToTransfer(db, *transferGroup, *fileInclude, *fileExclude, *forceIncludeOnly)
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
	transferDoneChan := make(chan error, 1)
	// special debug mode to find articles with bad date header... before usenet existed...
	if *debugCapture {
		log.Printf("Debug capture mode enabled - capturing articles without sending")
		*dryRun = true
	}
	// Start NNTP worker pool
	if !*dryRun {
		log.Printf("Starting NNTP connection worker pool...")
		go BootConnWorkers(pool, redisCli)
		time.Sleep(2 * time.Second) // Give workers time to establish connections
	}
	// Start transfer process
	var wgP sync.WaitGroup
	wgP.Add(2)
	go func(wgP *sync.WaitGroup, redisCli *redis.Client) {
		defer wgP.Done()
		resultChan := make(chan error, 1)
		resultChan <- runTransfer(db, newsgroups, *batchCheck, *maxThreads, *dryRun, startTime, endTime, *debugCapture, wgP, redisCli)
		result := <-resultChan
		if !*debugCapture {
			transferDoneChan <- result
			return
		}

		// when transfer is done: process debugCapture flag
		// used to debug and rewrite utzoo articles with invalid date headers
		// rewrite means, database update of DateSent and DateString fields! could be destructive!
		// you did create a backup before, right?
		debugMutex.Lock()
		defer debugMutex.Unlock()
		for newsgroup, articles := range debugArticles {
			fmt.Printf("Debug capture - Newsgroup: %s, Articles: %d\n", newsgroup, len(articles))

			// Get group database for updates if needed
			groupDBs, err := db.GetGroupDBs(newsgroup)
			if err != nil {
				fmt.Printf("! Error getting group database for %s: %v\n", newsgroup, err)
				continue
			}

			for _, article := range articles {
				fmt.Printf("# %s: #%d : '%s' | orgDate='%s' parsed='%#v'\n", newsgroup, article.DBArtNum, article.MessageID, article.DateString, article.DateSent)

				// Track original values to detect changes
				originalDateSent := article.DateSent
				originalDateString := article.DateString

				headers, err := common.ReconstructHeaders(article, true, &nntphostname, newsgroup)
				fmt.Printf("### ORG HEADER: '%s'\n%s\n", article.MessageID, article.HeadersJSON)
				if err != nil {
					fmt.Printf("! Error reconstructing headers for article '%s': %v\n", article.MessageID, err)
					continue
				}

				if *RewriteDates {
					// Check if DateSent or DateString were updated and update database
					if !article.DateSent.Equal(originalDateSent) || article.DateString != originalDateString {
						fmt.Printf("! Date corrected for %s:\n    DateSent '%s' -> '%s'\n    DateString: '%s' -> '%s'\n",
							article.MessageID,
							originalDateSent.UTC().Format(time.RFC1123Z),
							article.DateSent.UTC().Format(time.RFC1123Z),
							originalDateString,
							article.DateString)

						if err := db.UpdateArticleDateSent(groupDBs, article.MessageID, article.DateSent, article.DateString); err != nil {
							fmt.Printf("! Error updating database for article '%s': %v\n", article.MessageID, err)
						} else {
							fmt.Printf("! Database updated for article '%s'\n", article.MessageID)
						}
					}
				}

				fmt.Printf("### NEW HEADER: '%s' REWRITE\n", article.MessageID)
				for _, line := range headers {
					fmt.Printf("%s\n", line)
				}
				fmt.Printf("### EOF HEADER '%s' bodyBytes=%d ###\n\n", article.MessageID, len(article.BodyText))
				//fmt.Printf("%s\n", article.BodyText)
				//fmt.Printf("### BODY EOF '%s' ###\n\n", article.MessageID)
			}
			groupDBs.Return(db)
		}
		transferDoneChan <- result
	}(&wgP, redisCli)
	wgP.Wait()
	// Wait for either shutdown signal or transfer completion
	select {
	case <-sigChan:
		log.Printf("Received shutdown signal, initiating graceful shutdown...")
		common.ForceShutdown()
	case err := <-transferDoneChan:
		if err != nil {
			log.Printf("Transfer completed with error: %v", err)
		} else {
			log.Printf("Transfer completed successfully")
		}
	}

	pool.ClosePool()

	// Close processor
	if proc != nil {
		if err := proc.Close(); err != nil {
			log.Printf("Warning: Failed to close processor: %v", err)
		} else {
			log.Printf("Processor closed successfully")
		}
	}

	// Wait for database operations to complete
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
		if err := rows.Scan(&a.DBArtNum, &a.MessageID, &a.Subject, &a.FromHeader, &a.DateSent, &a.DateString, &a.References, &a.Bytes, &a.Lines, &a.ReplyCount, &a.Path, &a.HeadersJSON, &a.BodyText, &a.ImportedAt); err != nil {
			return nil, err
		}
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

// getNewsgroupsToTransfer returns newsgroups matching the specified pattern and file filters
func getNewsgroupsToTransfer(db *database.Database, groupPattern, fileInclude, fileExclude string, forceIncludeOnly bool) ([]*models.Newsgroup, error) {
	var newsgroups []*models.Newsgroup

	// Load include/exclude patterns from files if specified
	var includePatterns, excludePatterns []string
	var includeLookup, excludeLookup map[string]bool
	var hasIncludeWildcards, hasExcludeWildcards bool
	var err error

	if fileInclude != "" {
		includePatterns, err = loadPatternsFromFile(fileInclude)
		if err != nil {
			return nil, fmt.Errorf("failed to load include patterns from %s: %v", fileInclude, err)
		}
		log.Printf("Loaded %d include patterns from %s", len(includePatterns), fileInclude)

		// Create fast lookup map for exact matches and detect wildcards
		includeLookup = make(map[string]bool)
		for _, pattern := range includePatterns {
			if strings.Contains(pattern, "*") {
				hasIncludeWildcards = true
			} else {
				includeLookup[pattern] = true
			}
		}
		log.Printf("Created fast lookup for %d exact include patterns, wildcards: %v", len(includeLookup), hasIncludeWildcards)
	}

	if fileExclude != "" {
		excludePatterns, err = loadPatternsFromFile(fileExclude)
		if err != nil {
			return nil, fmt.Errorf("failed to load exclude patterns from %s: %v", fileExclude, err)
		}
		log.Printf("Loaded %d exclude patterns from %s", len(excludePatterns), fileExclude)

		// Create fast lookup map for exact matches and detect wildcards
		excludeLookup = make(map[string]bool)
		for _, pattern := range excludePatterns {
			if strings.Contains(pattern, "*") {
				hasExcludeWildcards = true
			} else {
				excludeLookup[pattern] = true
			}
		}
		log.Printf("Created fast lookup for %d exact exclude patterns, wildcards: %v", len(excludeLookup), hasExcludeWildcards)
	}

	// Get all newsgroups from database
	log.Printf("Loading newsgroups from database...")
	start := time.Now()
	allNewsgroups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return nil, fmt.Errorf("failed to get newsgroups from database: %v", err)
	}
	log.Printf("Loaded %d newsgroups from database in %v", len(allNewsgroups), time.Since(start))

	// Handle force-include-only mode
	if forceIncludeOnly {
		if len(includePatterns) == 0 {
			return nil, fmt.Errorf("force-include-only flag requires include file to be specified")
		}
		log.Printf("Force-include-only mode: filtering newsgroups using group pattern '%s' and %d include patterns", groupPattern, len(includePatterns))

		// First filter by group pattern, then by include patterns
		var groupFiltered []*models.Newsgroup

		// Handle $all pattern
		if groupPattern == "$all" {
			groupFiltered = allNewsgroups
		} else {
			// Handle wildcard patterns
			suffixWildcard := strings.HasSuffix(groupPattern, "*")
			if suffixWildcard {
				wildcardPrefix := strings.TrimSuffix(groupPattern, "*")
				for _, ng := range allNewsgroups {
					if strings.HasPrefix(ng.Name, wildcardPrefix) {
						groupFiltered = append(groupFiltered, ng)
					}
				}
			} else {
				// Exact match
				for _, ng := range allNewsgroups {
					if ng.Name == groupPattern {
						groupFiltered = append(groupFiltered, ng)
						break
					}
				}
			}
		}

		// Now apply include patterns to group-filtered newsgroups
		start = time.Now()
		for _, ng := range groupFiltered {
			// Fast exact match check first
			if includeLookup[ng.Name] {
				newsgroups = append(newsgroups, ng)
			} else if hasIncludeWildcards {
				// Only check wildcard patterns if wildcards exist
				if matchesAnyWildcardPattern(ng.Name, includePatterns) {
					newsgroups = append(newsgroups, ng)
				}
			}
			// If no wildcards exist and no exact match, skip this newsgroup
		}
		log.Printf("Applied include pattern filtering in %v", time.Since(start))
		return newsgroups, nil
	}

	// Handle $all pattern (transfer all newsgroups, but still apply file filters)
	if groupPattern == "$all" {
		log.Printf("Using $all pattern: transferring all newsgroups with file filters applied")
		start := time.Now()
		for _, ng := range allNewsgroups {
			if shouldIncludeNewsgroup(ng.Name, includePatterns, excludePatterns, includeLookup, excludeLookup, hasIncludeWildcards, hasExcludeWildcards) {
				newsgroups = append(newsgroups, ng)
			}
		}
		log.Printf("Filtered %d newsgroups from %d total in %v", len(newsgroups), len(allNewsgroups), time.Since(start))
		return newsgroups, nil
	}

	// Handle wildcard patterns
	suffixWildcard := strings.HasSuffix(groupPattern, "*")
	var wildcardPrefix string

	if suffixWildcard {
		wildcardPrefix = strings.TrimSuffix(groupPattern, "*")
		log.Printf("Using wildcard newsgroup prefix: '%s'", wildcardPrefix)
	}

	// Filter newsgroups based on pattern
	start = time.Now()
	if suffixWildcard {
		for _, ng := range allNewsgroups {
			if strings.HasPrefix(ng.Name, wildcardPrefix) {
				if shouldIncludeNewsgroup(ng.Name, includePatterns, excludePatterns, includeLookup, excludeLookup, hasIncludeWildcards, hasExcludeWildcards) {
					newsgroups = append(newsgroups, ng)
				}
			}
		}
	} else {
		// Exact match
		for _, ng := range allNewsgroups {
			if ng.Name == groupPattern {
				if shouldIncludeNewsgroup(ng.Name, includePatterns, excludePatterns, includeLookup, excludeLookup, hasIncludeWildcards, hasExcludeWildcards) {
					newsgroups = append(newsgroups, ng)
				}
				break
			}
		}
	}
	log.Printf("Pattern filtering completed in %v, found %d matching newsgroups", time.Since(start), len(newsgroups))

	return newsgroups, nil
}

// loadPatternsFromFile loads newsgroup patterns from a file (one per line)
func loadPatternsFromFile(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return patterns, nil
}

// shouldIncludeNewsgroup determines if a newsgroup should be included based on include/exclude patterns
func shouldIncludeNewsgroup(newsgroup string, includePatterns, excludePatterns []string, includeLookup, excludeLookup map[string]bool, hasIncludeWildcards, hasExcludeWildcards bool) bool {
	// If include patterns are specified, newsgroup must match at least one
	if len(includePatterns) > 0 {
		// First check exact matches (fast O(1) lookup)
		if includeLookup[newsgroup] {
			// Still need to check excludes
		} else if hasIncludeWildcards {
			// Only check wildcard patterns if wildcards exist
			included := false
			for _, pattern := range includePatterns {
				if strings.Contains(pattern, "*") && matchesPattern(newsgroup, pattern) {
					included = true
					break
				}
			}
			if !included {
				return false
			}
		} else {
			// No wildcards and no exact match = not included
			return false
		}
	}

	// If exclude patterns are specified, newsgroup must not match any
	if len(excludePatterns) > 0 {
		// First check exact matches (fast O(1) lookup)
		if excludeLookup[newsgroup] {
			return false
		}
		// Only check wildcard patterns if wildcards exist
		if hasExcludeWildcards {
			for _, pattern := range excludePatterns {
				if strings.Contains(pattern, "*") && matchesPattern(newsgroup, pattern) {
					return false
				}
			}
		}
	}

	return true
}

// matchesPattern checks if a newsgroup name matches a pattern (supports wildcard *)
func matchesPattern(newsgroup, pattern string) bool {
	if pattern == "*" {
		return true
	}

	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(newsgroup, prefix)
	}

	if strings.HasPrefix(pattern, "*") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(newsgroup, suffix)
	}

	// Exact match
	return newsgroup == pattern
}

// matchesAnyPattern checks if a newsgroup name matches any of the given patterns
func matchesAnyPattern(newsgroup string, patterns []string) bool {
	for _, pattern := range patterns {
		if matchesPattern(newsgroup, pattern) {
			return true
		}
	}
	return false
}

// matchesAnyWildcardPattern checks if a newsgroup name matches any wildcard patterns (skips exact matches)
func matchesAnyWildcardPattern(newsgroup string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(pattern, "*") && matchesPattern(newsgroup, pattern) {
			return true
		}
	}
	return false
}

var totalTransferred, totalUnwanted, totalRejected, totalRedisCacheHits, totalTXErrors, totalConnErrors, nothingInDateRange uint64
var transferMutex sync.Mutex

// runTransfer performs the actual article transfer process
func runTransfer(db *database.Database, newsgroups []*models.Newsgroup, batchCheck int, maxThreads int, dryRun bool, startTime, endTime *time.Time, debugCapture bool, wgP *sync.WaitGroup, redisCli *redis.Client) error {
	defer wgP.Done()
	maxThreadsChan := make(chan struct{}, maxThreads)
	var wg sync.WaitGroup
	log.Printf("Todo: %d newsgroups", len(newsgroups))
	// Process each newsgroup
	for _, newsgroup := range newsgroups {
		if common.WantShutdown() {
			log.Printf("Aborted before next: %s", newsgroup.Name)
			return nil
		}
		maxThreadsChan <- struct{}{} // acquire a thread slot
		wg.Add(1)
		go func(ng *models.Newsgroup, wg *sync.WaitGroup, redisCli *redis.Client) {
			defer func(wg *sync.WaitGroup) {
				wg.Done()
				<-maxThreadsChan // release the thread slot
			}(wg)
			if common.WantShutdown() {
				log.Printf("Aborted before next: %s", newsgroup.Name)
				return
			}
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | Start", newsgroup.Name)
			}
			/*
				transferred, checked, rc, unwanted, rejected, txErrors, connErrors, err := transferNewsgroup(db, proc, pool, newsgroup, batchCheck, dryRun, startTime, endTime, debugCapture, redisCli)

				transferMutex.Lock()
				totalTransferred += transferred
				totalRedisCacheHits += rc
				totalUnwanted += unwanted
				totalRejected += rejected
				totalTXErrors += txErrors
				totalConnErrors += connErrors
				transferMutex.Unlock()
			*/
			err := transferNewsgroup(db, newsgroup, batchCheck, dryRun, startTime, endTime, debugCapture, redisCli)
			if err == ErrNotInDateRange {
				transferMutex.Lock()
				nothingInDateRange++
				transferMutex.Unlock()
				err = nil // not a real error
			}
			if err != nil {
				log.Printf("Error transferring newsgroup %s: %v", newsgroup.Name, err)
			}
		}(newsgroup, &wg, redisCli)
	}

	// Wait for all transfers to complete
	wg.Wait()
	transferMutex.Lock()
	defer transferMutex.Unlock()
	if nothingInDateRange > 0 {
		log.Printf("Note: %d newsgroups had no articles in the specified date range", nothingInDateRange)
	}
	resultsMutex.Lock()
	for _, result := range results {
		log.Print(result)
	}
	resultsMutex.Unlock()
	log.Printf("Summary: transferred: %d | redis_cache_hits: %d | unwanted: %d | rejected: %d | TX_Errors: %d | connErrors: %d",
		totalTransferred, totalRedisCacheHits, totalUnwanted, totalRejected, totalTXErrors, totalConnErrors)
	return nil
}

var debugArticles = make(map[string][]*models.Article)
var debugMutex sync.Mutex
var ErrNotInDateRange = fmt.Errorf("article not in specified date range")

// processRequeuedJobs processes any failed jobs that were requeued for retry
// Returns the number of jobs processed successfully
func processRequeuedJobs(newsgroup string, ttMode *nntp.TakeThisMode, ttResponses chan chan *nntp.TTResponse, redisCli *redis.Client) (int, error) {
	var queuedJobs []*nntp.CHTTJob
	jobRequeueMutex.Lock()
	if jobs, exists := jobRequeue[ttMode.Newsgroup]; exists {
		queuedJobs = jobs
		// clear requeue
		delete(jobRequeue, ttMode.Newsgroup)
	}
	jobRequeueMutex.Unlock()

	if len(queuedJobs) == 0 {
		return 0, nil
	}

	log.Printf("Newsgroup: '%s' | Processing %d failed requeued jobs", newsgroup, len(queuedJobs))
	for i, job := range queuedJobs {
		if common.WantShutdown() {
			log.Printf("WantShutdown while processing requeued jobs for '%s'", newsgroup)
			// Put remaining jobs back in queue
			if i < len(queuedJobs) {
				jobRequeueMutex.Lock()
				jobRequeue[ttMode.Newsgroup] = slices.Insert(jobRequeue[ttMode.Newsgroup], 0, queuedJobs[i:]...)
				jobRequeueMutex.Unlock()
			}
			return i, nil
		}

		log.Printf("Newsgroup: '%s' | Processing requeued job %d/%d with %d articles", newsgroup, i+1, len(queuedJobs), len(job.Articles))
		// pass articles to CHECK or TAKETHIS queue (async!)
		responseChan, err := processBatch(ttMode, job.Articles, redisCli)
		if err != nil {
			log.Printf("Newsgroup: '%s' | Error processing requeued batch: %v", newsgroup, err)
			jobRequeueMutex.Lock()
			// insert remaining jobs back to slot 0
			jobRequeue[ttMode.Newsgroup] = slices.Insert(jobRequeue[ttMode.Newsgroup], 0, queuedJobs[i:]...)
			jobRequeueMutex.Unlock()
			return i, fmt.Errorf("error processing requeued batch for newsgroup '%s': %v", newsgroup, err)
		}
		if responseChan != nil {
			// pass the response channel to the collector channel: ttResponses
			ttResponses <- responseChan
		}
	}

	log.Printf("Newsgroup: '%s' | Successfully processed %d requeued jobs", newsgroup, len(queuedJobs))
	return len(queuedJobs), nil
}

// transferNewsgroup transfers articles from a single newsgroup
func transferNewsgroup(db *database.Database, newsgroup *models.Newsgroup, batchCheck int, dryRun bool, startTime, endTime *time.Time, debugCapture bool, redisCli *redis.Client) error {

	// Get group database
	groupDBs, err := db.GetGroupDBs(newsgroup.Name)
	if err != nil {
		return fmt.Errorf("failed to get group DBs for newsgroup '%s': %v", newsgroup.Name, err)
	}
	defer func() {
		if ferr := db.ForceCloseGroupDBs(groupDBs); ferr != nil {
			log.Printf("ForceCloseGroupDBs error for '%s': %v", newsgroup.Name, ferr)
		}
	}()

	// Get total article count first with date filtering
	totalArticles, err := getArticleCountWithDateFilter(groupDBs, startTime, endTime)
	if err != nil {
		return fmt.Errorf("failed to get article count for newsgroup '%s': %v", newsgroup.Name, err)
	}

	if totalArticles == 0 {

		if startTime != nil || endTime != nil {
			if VERBOSE {
				log.Printf("No articles found in newsgroup: %s (within specified date range)", newsgroup.Name)
			}
			return ErrNotInDateRange
		} else {
			log.Printf("No articles found in newsgroup: %s", newsgroup.Name)
		}

		return nil
	}

	if dryRun {
		if startTime != nil || endTime != nil {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s (within specified date range)", totalArticles, newsgroup.Name)
		} else {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s", totalArticles, newsgroup.Name)
		}
		if !debugCapture {
			return nil
		}
	}

	if !dryRun && !debugCapture {
		if startTime != nil || endTime != nil {
			log.Printf("Found %d articles in newsgroup %s (within specified date range) - processing in batches", totalArticles, newsgroup.Name)
		} else {
			log.Printf("Found %d articles in newsgroup %s - processing in batches", totalArticles, newsgroup.Name)
		}
	}
	//time.Sleep(3 * time.Second) // debug sleep
	var ioffset int64
	remainingArticles := totalArticles
	ttMode := &nntp.TakeThisMode{
		Newsgroup: &newsgroup.Name,
		CheckMode: true,
	}
	ttResponses := make(chan chan *nntp.TTResponse, totalArticles/int64(batchCheck)+2)
	start := time.Now()

	// WaitGroup to ensure collector goroutine finishes before returning
	var collectorWG sync.WaitGroup
	collectorWG.Add(1)

	// WaitGroup to track individual response channel processors
	var responseWG sync.WaitGroup

	go func() {
		defer collectorWG.Done()
		var amux sync.Mutex
		var transferred, unwanted, rejected, checked, txErrors, connErrors uint64
		var num uint64
		for responseChan := range ttResponses {
			if responseChan == nil {
				log.Printf("Newsgroup: '%s' | Warning: nil TT response channel received in collector!?", newsgroup.Name)
				continue
			}
			num++
			log.Printf("Newsgroup: '%s' | Starting response channel processor num %d (goroutines: %d)", newsgroup.Name, num, runtime.NumGoroutine())
			responseWG.Add(1)
			go func(rc chan *nntp.TTResponse, num uint64) {
				defer responseWG.Done()
				defer log.Printf("Newsgroup: '%s' | Ending response channel processor num %d (goroutines: %d)", newsgroup.Name, num, runtime.NumGoroutine())
				for resp := range rc {
					if resp == nil {
						log.Printf("Newsgroup: '%s' | Warning: nil TT response channel received!?", newsgroup.Name)
						return
					}
					if resp.Err != nil {
						log.Printf("Newsgroup: '%s' | Error in TT response: err='%v' job='%#v'", newsgroup.Name, resp.Err, resp.Job)
						return
					}
					if resp.Job == nil {
						log.Printf("Newsgroup: '%s' | Warning: nil Job in TT response without error!?", newsgroup.Name)
						return
					}
					// get numbers
					amux.Lock()
					resp.Job.GetUpdateCounters(&transferred, &unwanted, &rejected, &checked, &txErrors, &connErrors)
					amux.Unlock()

					// free memory - CRITICAL: Lock and unlock in same scope, not with defer!
					resp.Job.Mux.Lock()

					// Clean up Articles and their internal fields
					for i := range resp.Job.Articles {
						if resp.Job.Articles[i] != nil {
							// Clean article internal fields to free memory
							resp.Job.Articles[i].RefSlice = nil
							resp.Job.Articles[i].NNTPhead = nil
							resp.Job.Articles[i].NNTPbody = nil
							resp.Job.Articles[i].Headers = nil
							resp.Job.Articles[i].ArticleNums = nil
							resp.Job.Articles[i].NewsgroupsPtr = nil
							resp.Job.Articles[i].ProcessQueue = nil
							resp.Job.Articles[i].MsgIdItem = nil
							resp.Job.Articles[i] = nil
						}
					}
					resp.Job.Articles = nil

					// Clean up ArticleMap - nil the keys (pointers) before deleting
					for msgid := range resp.Job.ArticleMap {
						resp.Job.ArticleMap[msgid] = nil
						delete(resp.Job.ArticleMap, msgid)
					}
					resp.Job.ArticleMap = nil

					// NOTE: Do NOT clean up MessageIDs or WantedIDs here!
					// The CHECK worker may still be using them (race condition).
					// They will be cleaned up in BootConnWorkers when the job is requeued or discarded.

					resp.Job.Mux.Unlock()

				}
			}(responseChan, num)
		}
		log.Printf("Newsgroup: '%s' | Collector: ttResponses closed, waiting for %d response processors to finish...", newsgroup.Name, num)
		// Wait for all response channel processors to finish
		responseWG.Wait()
		log.Printf("Newsgroup: '%s' | Collector: all response processors closed", newsgroup.Name)
		amux.Lock()
		result := fmt.Sprintf("END Newsgroup: '%s' | transferred: %d/%d (unwanted: %d | rejected: %d | checked: %d | TX_Errors: %d | connErrors: %d | took %v",
			newsgroup.Name, transferred, totalArticles, unwanted, rejected, checked, txErrors, connErrors, time.Since(start))
		amux.Unlock()
		//log.Print(result)
		resultsMutex.Lock()
		results = append(results, result)
		if VERBOSE {
			for _, msgId := range rejectedArticles[newsgroup.Name] {
				// prints all at the end again
				log.Printf("END Newsgroup: '%s' | REJECTED '%s'", newsgroup.Name, msgId)
			}
			delete(rejectedArticles, newsgroup.Name) // free memory
		}
		resultsMutex.Unlock()
	}()

	// Get articles in database batches (much larger than network batches)
	for offset := ioffset; offset < totalArticles; offset += dbBatchSize {
		if common.WantShutdown() {
			log.Printf("WantShutdown in newsgroup: '%s' offset: %d", newsgroup.Name, offset)
			return nil
		}
		// Process any requeued jobs first (from previous failed batches)
		if _, err := processRequeuedJobs(newsgroup.Name, ttMode, ttResponses, redisCli); err != nil {
			return err
		}
		start := time.Now()
		// Load batch from database with date filtering
		articles, err := getArticlesBatchWithDateFilter(groupDBs, offset, startTime, endTime)
		if err != nil {
			log.Printf("Error loading article batch (offset %d) for newsgroup %s: %v", offset, newsgroup.Name, err)
			return fmt.Errorf("failed to load article batch (offset %d) for newsgroup '%s': %v", offset, newsgroup.Name, err)
		}

		if len(articles) == 0 {
			//log.Printf("No more articles in newsgroup %s (offset %d)", newsgroup.Name, offset)
			break
		}
		if dryRun && debugCapture {
			debugMutex.Lock()
			debugArticles[newsgroup.Name] = append(debugArticles[newsgroup.Name], articles...)
			debugMutex.Unlock()
			return nil
		}
		//if VERBOSE {
		log.Printf("Newsgroup: '%s' | Loaded %d articles from database (offset %d) took %v", newsgroup.Name, len(articles), offset, time.Since(start))
		//}
		// Process articles in network batches
		for i := 0; i < len(articles); i += batchCheck {
			if common.WantShutdown() {
				log.Printf("WantShutdown in newsgroup: '%s' (offset %d)", newsgroup.Name, offset)
				return nil
			}
			// Determine end index for the batch
			end := i + batchCheck
			if end > len(articles) {
				end = len(articles)
			}
			// pass articles to CHECK or TAKETHIS queue (async!)
			responseChan, err := processBatch(ttMode, articles[i:end], redisCli)
			if err != nil {
				log.Printf("Newsgroup: '%s' | Error processing batch %d-%d: %v", newsgroup.Name, i+1, end, err)
				return fmt.Errorf("error processing batch %d-%d for newsgroup '%s': %v", i+1, end, newsgroup.Name, err)
			}
			// pass the response channel to the collector channel: ttResponses
			ttResponses <- responseChan
		}
		remainingArticles -= int64(len(articles))
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Pushed to queue (offset %d/%d) remaining: %d (Check=%t)", newsgroup.Name, offset, totalArticles, remainingArticles, ttMode.GetMode())
			//log.Printf("Newsgroup: '%s' | Pushed (offset %d/%d) total: %d/%d (unw: %d / rej: %d) (Check=%t)", newsgroup.Name, offset, totalArticles, transferred, remainingArticles, ttMode.Unwanted, ttMode.Rejected, ttMode.GetMode())
		}
	} // end for offset range totalArticles

	log.Printf("Newsgroup: '%s' | Main article loop completed, checking for requeued jobs...", newsgroup.Name)

	// Process any remaining requeued jobs after main loop completes
	// This handles failures that occurred in the last batch
	for {
		if common.WantShutdown() {
			log.Printf("WantShutdown during final requeue processing for '%s'", newsgroup.Name)
			break
		}
		processed, err := processRequeuedJobs(newsgroup.Name, ttMode, ttResponses, redisCli)
		if err != nil {
			log.Printf("Newsgroup: '%s' | Error in final requeue processing: %v", newsgroup.Name, err)
			// Don't return error, just log it - we've already processed most articles
			break
		}
		if processed == 0 {
			// No more requeued jobs to process
			break
		}
		//log.Printf("Newsgroup: '%s' | Processed %d requeued jobs in final pass", newsgroup.Name, processed)
		// Loop again to check if any of those jobs failed and were requeued
	}

	log.Printf("Newsgroup: '%s' | Final requeue processing completed, closing ttResponses channel...", newsgroup.Name)

	// Close the ttResponses channel to signal collector goroutine to finish
	close(ttResponses)

	log.Printf("Newsgroup: '%s' | ttResponses channel closed, waiting for collector to finish...", newsgroup.Name)

	// Wait for collector goroutine to finish processing all responses
	collectorWG.Wait()

	log.Printf("Newsgroup: '%s' | All jobs completed and responses collected", newsgroup.Name)

	return nil
} // end func transferNewsgroup

var results []string
var rejectedArticles = make(map[string][]string)
var resultsMutex sync.RWMutex
var lowerLevel float64 = 90.0
var upperLevel float64 = 95.0

// processBatch processes a batch of articles using NNTP streaming protocol (RFC 4644)
// Uses TAKETHIS primarily, falls back to CHECK when success rate < 95%
func processBatch(ttMode *nntp.TakeThisMode, articles []*models.Article, redisCli *redis.Client) (chan *nntp.TTResponse, error) {

	if len(articles) == 0 {
		log.Printf("processBatch: no articles in this batch for newsgroup '%s'", *ttMode.Newsgroup)
		return nil, nil
	}
	doCheck := ttMode.FlipMode(lowerLevel, upperLevel)

	batchedJob := &nntp.CHTTJob{
		JobID:        atomic.AddUint64(&nntp.JobIDCounter, 1),
		Newsgroup:    ttMode.Newsgroup,
		MessageIDs:   make([]*string, 0, len(articles)),
		Articles:     make([]*models.Article, 0, len(articles)),
		ArticleMap:   make(map[*string]*models.Article, len(articles)),
		ResponseChan: make(chan *nntp.TTResponse, 1),
		TTMode:       ttMode,
	}

	switch doCheck {
	case true: // ttMode.CheckMode
		// CHECK mode: verify articles are wanted before sending
		//log.Printf("Newsgroup: '%s' | CHECK: %d articles (success rate: %.1f%%)", newsgroup, len(articles), successRate)
		// Batch check Redis cache using pipeline (1 round trip for all keys)
		var redis_cache_hits int
		if redisCli != nil && len(articles) > 0 {
			pipe := redisCli.Pipeline()
			cmds := make([]*redis.IntCmd, len(articles))

			// Queue all EXISTS commands
			for i, article := range articles {
				if article == nil {
					continue
				}
				cmds[i] = pipe.Exists(redisCtx, article.MessageID)
			}

			// Execute all in one network round trip
			_, err := pipe.Exec(redisCtx)
			if err != nil && VERBOSE {
				log.Printf("Newsgroup: '%s' | Redis pipeline error: %v", *ttMode.Newsgroup, err)
			}

			// Process results
			for i, cmd := range cmds {
				if cmd == nil || articles[i] == nil {
					continue
				}
				article := articles[i]
				exists, cmdErr := cmd.Result()
				if cmdErr == nil && exists > 0 {
					// Cached in Redis - skip this article
					if VERBOSE {
						log.Printf("Newsgroup: '%s' | Message ID '%s' is cached in Redis (skip [CHECK])", *ttMode.Newsgroup, article.MessageID)
					}
					batchedJob.Increment(nntp.IncrFLAG_REDIS_CACHED)
					redis_cache_hits++
					articles[i] = nil
					continue
				}

				// Not cached - add to valid list
				batchedJob.Articles = append(batchedJob.Articles, article)
				batchedJob.ArticleMap[&article.MessageID] = article
				batchedJob.MessageIDs = append(batchedJob.MessageIDs, &article.MessageID)
			}
		} else {
			// No Redis - add all non-nil message IDs
			for _, article := range articles {
				if article == nil {
					continue
				}
				batchedJob.Articles = append(batchedJob.Articles, article)
				batchedJob.ArticleMap[&article.MessageID] = article
				batchedJob.MessageIDs = append(batchedJob.MessageIDs, &article.MessageID)
			}
		}

		if len(batchedJob.MessageIDs) == 0 {
			log.Printf("Newsgroup: '%s' | No message IDs to check in batch. (redis_cache_hits: %d)", *ttMode.Newsgroup, redis_cache_hits)
			return nil, nil
		}
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Sending CHECK commands for %d/%d articles", *ttMode.Newsgroup, len(batchedJob.MessageIDs), len(articles))
		}

		// Assign job to worker (consistent assignment + load balancing)
		if len(CheckQueues) == 0 {
			return nil, fmt.Errorf("no workers available")
		}

		workerID := assignWorkerToNewsgroup(*ttMode.Newsgroup)
		checkQueue := CheckQueues[workerID]

		// Track queue length for load balancing
		WorkerQueueLengthMux.Lock()
		WorkerQueueLength[workerID]++
		WorkerQueueLengthMux.Unlock()

		log.Printf("Newsgroup: '%s' | Sending job #%d to Worker %d queue with %d message IDs", *ttMode.Newsgroup, batchedJob.JobID, workerID, len(batchedJob.MessageIDs))
		checkQueue <- batchedJob
		log.Printf("Newsgroup: '%s' | Job #%d sent to Worker %d successfully", *ttMode.Newsgroup, batchedJob.JobID, workerID)
		return batchedJob.ResponseChan, nil

	// end case ttMode.CheckMode
	// case !ttMode.CheckMode
	case false:
		// TAKETHIS mode: send articles directly without CHECK
		//log.Printf("Newsgroup: '%s' | TAKETHIS: %d articles", newsgroup, len(articles))

		// Validate articles before sending in TAKETHIS mode
		for _, article := range articles {
			if article == nil {
				continue
			}
			batchedJob.Articles = append(batchedJob.Articles, article)
			batchedJob.ArticleMap[&article.MessageID] = article
			batchedJob.WantedIDs = append(batchedJob.WantedIDs, &article.MessageID)
		}

		if len(batchedJob.Articles) == 0 {
			log.Printf("Newsgroup: '%s' | WARN: No valid articles for TAKETHIS mode, skipping batch", *ttMode.Newsgroup)
			return nil, nil
		}
		log.Printf("Newsgroup: '%s' | Sending job #%d to TakeThisQueue with %d articles", *ttMode.Newsgroup, batchedJob.JobID, len(batchedJob.WantedIDs))
		nntp.TakeThisQueue <- batchedJob
		log.Printf("Newsgroup: '%s' | Job #%d sent to TakeThisQueue successfully", *ttMode.Newsgroup, batchedJob.JobID)
		return batchedJob.ResponseChan, nil

	} // end case !ttMode.CheckMode
	// end switch ttMode.CheckMode

	return nil, nil
} // end func processBatch

// sendArticlesBatchViaTakeThis sends multiple articles via TAKETHIS in streaming mode
// Sends all TAKETHIS commands first, then reads all responses (true streaming)
func sendArticlesBatchViaTakeThis(conn *nntp.BackendConn, articles []*models.Article, job *nntp.CHTTJob, newsgroup string, redisCli *redis.Client) (transferred uint64, rejected uint64, redis_cached uint64, err error) {
	if len(articles) == 0 {
		return 0, 0, 0, nil
	}

	// Phase 1: Send all TAKETHIS commands without waiting for responses
	//log.Printf("Phase 1: Sending %d TAKETHIS commands...", len(articles))

	//commandIDs := make([]uint, 0, len(articles))
	//checkArticles := make([]*models.Article, 0, len(articles))

	// Batch check Redis cache using pipeline before sending TAKETHIS
	if redisCli != nil {
		pipe := redisCli.Pipeline()
		cmds := make([]*redis.IntCmd, len(articles))

		// Queue all EXISTS commands (only for non-nil articles)
		for i, article := range articles {
			if article == nil {
				continue
			}
			cmds[i] = pipe.Exists(redisCtx, article.MessageID)
		}

		// Execute all in one network round trip
		_, err := pipe.Exec(redisCtx)
		if err != nil && VERBOSE {
			log.Printf("Newsgroup: '%s' | Redis pipeline error in TAKETHIS: %v", newsgroup, err)
		}

		// Process results and filter cached articles
		for i, cmd := range cmds {
			if cmd == nil || articles[i] == nil {
				continue // Skip if command wasn't queued or article is nil
			}

			exists, cmdErr := cmd.Result()
			if cmdErr == nil && exists > 0 {
				// Cached in Redis - skip this article
				if VERBOSE {
					log.Printf("Newsgroup: '%s' | Message ID '%s' is cached in Redis (skip [TAKETHIS])", newsgroup, articles[i].MessageID)
				}
				articles[i] = nil // free memory
				redis_cached++
				continue
			}
			// Not cached - will be sent
		}
	}

	// Now send TAKETHIS for non-cached articles
	artChan := make(chan *nntp.CheckResponse, len(articles))
	// ← Also close artChan

	for _, article := range articles {
		if article == nil {
			continue // Skip cached articles
		}
		// Send TAKETHIS command with article content (non-blocking)
		cmdID, err := conn.SendTakeThisArticleStreaming(article, &processor.LocalNNTPHostname, newsgroup)
		if err != nil {
			if err == common.ErrNoNewsgroups {
				log.Printf("Newsgroup: '%s' | skipped article '%s': no newsgroups header", newsgroup, article.MessageID)
				continue
			}
			conn.ForceCloseConn()
			log.Printf("ERROR Newsgroup: '%s' | Failed to send TAKETHIS for %s: %v", newsgroup, article.MessageID, err)
			return 0, 0, redis_cached, fmt.Errorf("failed to send TAKETHIS for %s: %v", article.MessageID, err)
		}

		artChan <- &nntp.CheckResponse{
			Article: article,
			CmdId:   cmdID,
		}
	}
	close(artChan)
	//log.Printf("Sent %d TAKETHIS commands, reading responses...", len(commandIDs))
	var done []*string
	var countDone int
	// Phase 2: Read all responses in order
	for cr := range artChan {

		job.TTMode.IncrementTmp()
		takeThisResponseCode, err := conn.ReadTakeThisResponseStreaming(cr.CmdId)
		if err != nil || takeThisResponseCode == 0 {
			job.Increment(nntp.IncrFLAG_CONN_ERRORS)
			conn.ForceCloseConn()
			log.Printf("ERROR Newsgroup: '%s' | Failed to read TAKETHIS response for %s: %v", newsgroup, cr.Article.MessageID, err)
			return transferred, rejected, redis_cached, fmt.Errorf("failed to read TAKETHIS response for %s: %v", cr.Article.MessageID, err)
		}
		countDone++
		// Update success rate tracking
		switch takeThisResponseCode {
		case 239:
			job.TTMode.IncrementSuccess()
			job.Increment(nntp.IncrFLAG_TRANSFERRED)
			transferred++
		case 439:
			job.Increment(nntp.IncrFLAG_REJECTED)
			rejected++
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | Rejected article '%s': response=%d (i=%d/%d)", newsgroup, cr.Article.MessageID, takeThisResponseCode, countDone, len(articles))
				//resultsMutex.Lock()
				//rejectedArticles[newsgroup] = append(rejectedArticles[newsgroup], article.MessageID)
				//resultsMutex.Unlock()
			}
		case 400, 480, 500, 501, 502, 503, 504:
			log.Printf("ERROR Newsgroup: '%s' | Failed to transfer article '%s': response=%d (i=%d/%d)", newsgroup, cr.Article.MessageID, takeThisResponseCode, countDone, len(articles))
			job.Increment(nntp.IncrFLAG_TX_ERRORS)
			conn.ForceCloseConn()
			return transferred, rejected, redis_cached, fmt.Errorf("failed to transfer article '%s': response=%d", cr.Article.MessageID, takeThisResponseCode)

		default:
			log.Printf("ERROR Newsgroup: '%s' | Failed to transfer article '%s': unknown response=%d (i=%d/%d)", newsgroup, cr.Article.MessageID, takeThisResponseCode, countDone, len(articles))
			job.Increment(nntp.IncrFLAG_TX_ERRORS)
			continue
		}
		if redisCli != nil {
			done = append(done, &cr.Article.MessageID)
		}
		/*
			if countDone > 100 && rejected > 10 {
				failRate := float64(rejected) / float64(countDone) * 100
				if failRate > 10 {
					ttMode.CheckMode = true
					breakChan <- struct{}{}
					return transferred, redis_cached, fmt.Errorf("Newsgroup: '%s' | ABORT streamed takethis batch. failRate: %.1f%%. transferred=%d rejected=%d", newsgroup, failRate, transferred, rejected)
				}
			}
		*/
	} // end for cmdChan

	if redisCli != nil && len(done) > 0 {
		// Cache transferred or rejected message IDs in Redis using pipeline (1 round trip)
		pipe := redisCli.Pipeline()

		// Queue all SET commands
		for _, msgID := range done {
			pipe.Set(redisCtx, *msgID, "1", REDIS_TTL)
		}

		// Execute all SET commands in one network round trip
		_, err := pipe.Exec(redisCtx)
		if err != nil {
			log.Printf("Newsgroup: '%s' | Failed to cache %d message IDs in Redis: %v", newsgroup, len(done), err)
		} else if VERBOSE {
			log.Printf("Newsgroup: '%s' | Cached %d message IDs in Redis", newsgroup, len(done))
		}
	}
	if VERBOSE {
		log.Printf("Newsgroup: '%s' | Batch transferred: %d/%d articles. rejected=%d redis_cached=%d", newsgroup, transferred, len(articles), rejected, redis_cached)
	}
	return transferred, rejected, redis_cached, nil
} // end func sendArticlesBatchViaTakeThis

var jobRequeueMutex sync.RWMutex
var jobRequeue = make(map[*string][]*nntp.CHTTJob)

// CheckQueues holds per-worker CheckQueue channels for consistent newsgroup routing
var CheckQueues []chan *nntp.CHTTJob

// NewsgroupWorkerMap tracks which worker is assigned to each newsgroup
var NewsgroupWorkerMap = make(map[string]int)
var NewsgroupWorkerMapMux sync.RWMutex

// WorkerQueueLength tracks how many jobs are queued per worker (for load balancing)
var WorkerQueueLength []int
var WorkerQueueLengthMux sync.Mutex

// assignWorkerToNewsgroup finds the best worker for a newsgroup
// If newsgroup already assigned, returns same worker (sequential processing)
// If new newsgroup, assigns to least busy worker (load balancing)
func assignWorkerToNewsgroup(newsgroup string) int {
	// Check if already assigned
	NewsgroupWorkerMapMux.RLock()
	if workerID, exists := NewsgroupWorkerMap[newsgroup]; exists {
		NewsgroupWorkerMapMux.RUnlock()
		return workerID
	}
	NewsgroupWorkerMapMux.RUnlock()

	// Find least busy worker
	WorkerQueueLengthMux.Lock()
	if len(WorkerQueueLength) == 0 {
		WorkerQueueLengthMux.Unlock()
		return 0
	}

	minLoad := WorkerQueueLength[0]
	workerID := 0
	for i := 1; i < len(WorkerQueueLength); i++ {
		if WorkerQueueLength[i] < minLoad {
			minLoad = WorkerQueueLength[i]
			workerID = i
		}
	}
	WorkerQueueLengthMux.Unlock()

	// Assign newsgroup to this worker
	NewsgroupWorkerMapMux.Lock()
	NewsgroupWorkerMap[newsgroup] = workerID
	NewsgroupWorkerMapMux.Unlock()

	return workerID
}

// Find first empty slot
func findEmptySlot(openConns *int, workerSlots []bool, mux *sync.Mutex) int {
	mux.Lock()
	defer mux.Unlock()
	*openConns++
	for i := 0; i < len(workerSlots); i++ {
		if !workerSlots[i] {
			workerSlots[i] = true
			return i
		}
	}
	return -1
}

func UnsetWorker(openConns *int, slotID int, workerSlots []bool, mux *sync.Mutex) {
	mux.Lock()
	defer mux.Unlock()
	*openConns--
	if slotID >= 0 && slotID < len(workerSlots) {
		workerSlots[slotID] = false
	}
}

func BootConnWorkers(pool *nntp.Pool, redisCli *redis.Client) {
	openConns := 0
	workerSlots := make([]bool, nntp.NNTPTransferThreads)
	defaultSleep := time.Second
	isleep := defaultSleep
	var mux sync.Mutex
	// Create per-worker queues
	CheckQueues = make([]chan *nntp.CHTTJob, nntp.NNTPTransferThreads)
	WorkerQueueLength = make([]int, nntp.NNTPTransferThreads)
	for i := range CheckQueues {
		CheckQueues[i] = make(chan *nntp.CHTTJob) // no cap! only accepts if there is a reader!
		WorkerQueueLength[i] = 0
	}
	allEstablished := false
forever:
	for {
		time.Sleep(defaultSleep)
		if common.WantShutdown() {
			log.Printf("BootConnWorkers: WantShutdown, exiting")
			break forever
		}
		mux.Lock()
		allEstablished = openConns == nntp.NNTPTransferThreads
		mux.Unlock()
		if allEstablished {
			continue forever
		}
		//var sharedConns []*nntp.BackendConn
		bootN := nntp.NNTPTransferThreads - openConns
		if bootN <= 0 {
			log.Printf("BootConnWorkers: all %d/%d connections established", openConns, nntp.NNTPTransferThreads)
			continue forever
		}
		// get connections from pool
		log.Printf("BootConnWorkers: need %d connections (have %d), getting from pool...", bootN, openConns)
		returnSignals := make([]*ReturnSignal, bootN)
		errChan := make(chan struct{}, 1)
		newConns := 0
		for i := range bootN {
			// Get a connection from pool
			conn, err := pool.Get(nntp.MODE_STREAM_MV)
			if err != nil {
				log.Printf("BootConnWorkers failed to get connection from pool: %v ... retry in: %v", err, isleep)
				if isleep > defaultSleep {
					time.Sleep(isleep)
				}
				isleep = isleep * 2
				if isleep > time.Minute {
					isleep = time.Minute
				}
				continue forever
			}
			if conn.ModeReader {
				if VERBOSE {
					log.Printf("got connection in reader mode, closing and getting a new one")
				}
				conn.ForceCloseConn()
				continue forever
			}
			// got a connection
			slotID := findEmptySlot(&openConns, workerSlots, &mux)
			if slotID < 0 {
				log.Printf("BootConnWorkers: no empty worker slot found, closing connection")
				conn.ForceCloseConn()
				continue forever
			}
			returnSignal := &ReturnSignal{
				slotID:        slotID,
				errChan:       errChan,
				redisCli:      redisCli,
				ExitChan:      make(chan *ReturnSignal, 1),
				tmpMessageIDs: make([]*string, 0, BatchCheck),
				jobsQueued:    make(map[*nntp.CHTTJob]uint64, BatchCheck),
				jobsReadOK:    make(map[*nntp.CHTTJob]uint64, BatchCheck),
				jobMap:        make(map[*string]*nntp.CHTTJob, BatchCheck),
				jobs:          make([]*nntp.CHTTJob, 0, BatchCheck),
			}

			returnSignals[i] = returnSignal
			// assign checkQueue by openConns counter
			// so restarted workers get same channels to read from
			go CHTTWorker(slotID, conn, returnSignal, CheckQueues[slotID])
			newConns++
		}
		if newConns == 0 {
			log.Printf("BootConnWorkers: no connections obtained, retry in: %v", isleep)
			isleep = isleep * 2
			if isleep > time.Minute {
				isleep = time.Minute
			}
			continue forever
		}
		isleep = defaultSleep // reset to default
		log.Printf("BootConnWorkers: launched %d CHTT workers", newConns)
		// Monitor recently launched CHTT workers
		go func() {
			monitoring := newConns
			for {
				time.Sleep(100 * time.Millisecond)
				for i, wait := range returnSignals {
					if wait == nil {
						continue
					}
					select {
					case rs := <-wait.ExitChan:
						log.Printf("CHTTWorker (%d) exited", i)
						monitoring--

						UnsetWorker(&openConns, rs.slotID, workerSlots, &mux)
						returnSignals[i] = nil

						rs.Mux.Lock()
						if len(rs.jobs) > 0 {
							log.Printf("CHTTWorker (%d) try requeue %d jobs", i, len(rs.jobs))
							for _, job := range rs.jobs {
								if job != nil {
									// copy articles pointer
									job.Mux.Lock()
									rqj := &nntp.CHTTJob{
										JobID:     job.JobID,
										Newsgroup: job.Newsgroup,
										Articles:  job.Articles,
									}
									job.Mux.Unlock()

									jobRequeueMutex.Lock()
									jobRequeue[rqj.Newsgroup] = append(jobRequeue[rqj.Newsgroup], rqj) // TODO DEAD END
									jobRequeueMutex.Unlock()

									// unlink pointers
									job.Mux.Lock()
									if job.TTMode != nil {
										job.TTMode.Newsgroup = nil
									}
									job.Newsgroup = nil
									job.TTMode = nil
									job.ResponseChan = nil
									job.Articles = nil
									job.ArticleMap = nil
									job.MessageIDs = nil
									job.WantedIDs = nil
									job.Mux.Unlock()
								}
							}
							log.Printf("CHTTWorker (%d) did requeue %d jobs", i, len(rs.jobs))
						}

						// Clean up ReturnSignal maps and unlink pointers
						// Clean up jobMap - nil all pointers before deleting
						log.Printf("CHTTWorker (%d) cleaning up jobMap with %d entries", i, len(rs.jobMap))
						for msgID := range rs.jobMap {
							rs.jobMap[msgID] = nil
							delete(rs.jobMap, msgID)
						}
						rs.jobMap = nil

						// Clean up jobsQueued
						log.Printf("CHTTWorker (%d) cleaning up jobsQueued with %d entries", i, len(rs.jobsQueued))
						for job := range rs.jobsQueued {
							delete(rs.jobsQueued, job)
						}
						rs.jobsQueued = nil

						// Clean up jobsReadOK
						log.Printf("CHTTWorker (%d) cleaning up jobsReadOK with %d entries", i, len(rs.jobsReadOK))
						for job := range rs.jobsReadOK {
							delete(rs.jobsReadOK, job)
						}
						rs.jobsReadOK = nil

						// Clean up jobs slice - nil all pointers
						log.Printf("CHTTWorker (%d) cleaning up jobs slice with %d entries", i, len(rs.jobs))
						for idx := range rs.jobs {
							rs.jobs[idx] = nil
						}
						rs.jobs = nil

						// Clean up tmpMessageIDs (if still present)
						log.Printf("CHTTWorker (%d) cleaning up tmpMessageIDs with %d entries", i, len(rs.tmpMessageIDs))
						for idx := range rs.tmpMessageIDs {
							rs.tmpMessageIDs[idx] = nil
						}
						rs.tmpMessageIDs = nil

						rs.redisCli = nil
						rs.ExitChan = nil
						rs.errChan = nil

						rs.Mux.Unlock()
						// TODO: check remaining work and restart connection
					default:
						// Worker still running
					}
				}
				if monitoring == 0 {
					return
				}
			}
		}()
	} // end forever
} // end func BootConnWorkers

var DefaultCheckTicker = 5 * time.Second

var JobsToRetry []*nntp.CHTTJob
var JobsToRetryMux sync.Mutex

type ReturnSignal struct {
	Mux           sync.Mutex
	slotID        int
	ExitChan      chan *ReturnSignal
	errChan       chan struct{}
	redisCli      *redis.Client
	tmpMessageIDs []*string
	jobsQueued    map[*nntp.CHTTJob]uint64
	jobsReadOK    map[*nntp.CHTTJob]uint64
	jobMap        map[*string]*nntp.CHTTJob
	jobs          []*nntp.CHTTJob
}

type readRequest struct {
	MsgID   *string
	retChan chan struct{}
}

func replyChan(request chan struct{}, reply chan struct{}) {
	select {
	case <-request:
		// got a reply request
		reply <- struct{}{} // send back
	default:
	}
}

func CHTTWorker(id int, conn *nntp.BackendConn, rs *ReturnSignal, checkQueue chan *nntp.CHTTJob) {
	readResponsesChan := make(chan *readRequest, BatchCheck)
	takeThisChan := make(chan *nntp.CHTTJob, nntp.NNTPTransferThreads)
	errChan := make(chan struct{}, 4)
	tickChan := make(chan struct{}, 1)
	flipflopChan := make(chan struct{}, 1)
	requestReplyJobDone := make(chan struct{}, 1)
	replyJobDone := make(chan struct{}, 1)
	rrRetChan := make(chan struct{}, BatchCheck)

	defer func(conn *nntp.BackendConn, rs *ReturnSignal) {
		conn.ForceCloseConn()
		rs.ExitChan <- rs
		errChan <- struct{}{}
	}(conn, rs)
	//lastRun := time.Now()

	// launch go routine which sends CHECK commands if threshold exceeds BatchCheck
	go func() {
		// tick every n seconds to check if any CHECKs to do
		ticker := time.NewTicker(DefaultCheckTicker)
		defer ticker.Stop()
		defer func() {
			errChan <- struct{}{}
		}()
	loop:
		for {
			select {
			case <-errChan:
				errChan <- struct{}{}
				log.Printf("CheckWorker (%d): Send CHECK got errChan signal... exiting", id)
				return

			case <-tickChan:
				if common.WantShutdown() {
					log.Printf("CheckWorker (%d): Tick WantShutdown, exiting", id)
					return
				}
				// Get the next job to process
				rs.Mux.Lock()
				if len(rs.jobs) == 0 {
					rs.Mux.Unlock()
					log.Printf("CheckWorker (%d): Ticked but no jobs in queue, continue...", id)
					continue loop
				}
				currentJob := rs.jobs[0]
				rs.jobs = rs.jobs[1:] // Remove first job from queue
				rs.Mux.Unlock()

				// Use message IDs directly from the job
				checkIds := currentJob.MessageIDs
				newsgroup := "unknown"
				if currentJob != nil && currentJob.Newsgroup != nil {
					newsgroup = *currentJob.Newsgroup
				}

				log.Printf("Newsgroup: '%s' | CheckWorker (%d): waits to check %d message IDs in batches of %d (job #%d)", newsgroup, id, len(checkIds), BatchCheck, currentJob.JobID)

				// Process checkIds in batches of BatchCheck
				for batchStart := 0; batchStart < len(checkIds); batchStart += BatchCheck {
					batchEnd := batchStart + BatchCheck
					if batchEnd > len(checkIds) {
						batchEnd = len(checkIds)
					}
					batch := checkIds[batchStart:batchEnd]

					// Lock for this batch only
					common.ChanLock(flipflopChan)
					log.Printf("Newsgroup: '%s' | CheckWorker (%d): Sending CHECK for batch %d-%d (%d messages)", newsgroup, id, batchStart, batchEnd, len(batch))

					err := conn.SendCheckMultiple(batch)
					if err != nil {
						common.ChanRelease(flipflopChan)
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): SendCheckMultiple error for batch %d-%d: %v", newsgroup, id, batchStart, batchEnd, err)
						return
					}

					// Send all message IDs to read channel
					for _, msgID := range batch {
						if msgID != nil {
							// pass message ID pointer to channel
							// to read the responses from connection
							readResponsesChan <- &readRequest{MsgID: msgID, retChan: rrRetChan}
						}
					}

					// Wait for all responses in this batch before releasing lock
					for _, msgID := range batch {
						if msgID != nil {
							<-rrRetChan
							/*disabled
							select {
							case <-rrRetChan:

							case <-time.After(time.Second * 300):
								log.Printf("CheckWorker (%d): Timeout waiting for CHECK response for msgID: %s", id, *msgID)
								common.ChanRelease(flipflopChan)
								return

							}
							*/

						}
					}
					// Release lock after batch is complete, allowing TAKETHIS to run
					common.ChanRelease(flipflopChan)
				}
				replyChan(requestReplyJobDone, replyJobDone) // see if anybody is waiting and reply
				rs.Mux.Lock()
				//lastRun = time.Now()
				// Check if there are more jobs to process
				hasMoreJobs := len(rs.jobs) > 0
				rs.Mux.Unlock()

				// Decrement queue length for this worker (job processing complete)
				WorkerQueueLengthMux.Lock()
				if id < len(WorkerQueueLength) && WorkerQueueLength[id] > 0 {
					WorkerQueueLength[id]--
				}
				WorkerQueueLengthMux.Unlock()

				// If there are more jobs waiting, immediately trigger next job processing
				if hasMoreJobs {
					select {
					case tickChan <- struct{}{}:
					default:
						// Channel full, will be processed on next tick
					}
				}

			case <-ticker.C:
				if common.WantShutdown() {
					log.Printf("CheckWorker (%d): Ticker WantShutdown, exiting", id)
					return
				}
				rs.Mux.Lock()
				hasWork := len(rs.jobs) > 0
				rs.Mux.Unlock()
				if hasWork {
					select {
					case tickChan <- struct{}{}:
					default:
						// tickChan full, tickChan will tick
					}
				}
			} // end select
		} // end forever
	}()

	// launch a go routine to read CHECK responses from the supplied connection with textproto readline
	go func() {
		var responseCount int
		var tookTime int64
		defer func() {
			errChan <- struct{}{}
		}()
	loop:
		for {
			select {
			case <-errChan:
				errChan <- struct{}{}
				log.Printf("CheckWorker (%d): Read CHECK responses got errChan signal... exiting", id)
				return

			case rr := <-readResponsesChan:
				if rr == nil || rr.MsgID == nil || rr.retChan == nil {
					log.Printf("CheckWorker (%d): Read CHECK got nil readRequest, skipping", id)
					rr.retChan <- struct{}{}
					continue loop
				}
				if common.WantShutdown() {
					log.Printf("CheckWorker (%d): Read CHECK WantShutdown, exiting", id)
					rr.retChan <- struct{}{}
					return
				}
				start := time.Now()
				code, line, err := conn.TextConn.ReadCodeLine(238)
				if code == 0 && err != nil {
					log.Printf("Failed to read CHECK response: %v", err)
					rr.retChan <- struct{}{}
					return
				}
				tookTime += time.Since(start).Milliseconds()
				responseCount++
				if responseCount >= BatchCheck {
					avg := float64(tookTime) / float64(responseCount)
					if avg > 1 {
						log.Printf("CheckWorker (%d): Read %d CHECK responses, avg latency: %.1f ms", id, responseCount, avg)
					}
					responseCount = 0
					tookTime = 0
				}
				// Parse response line
				// Format: code <message-id> [message]
				// 238 <message-id> - article wanted
				// 431 <message-id> - article not wanted
				// 438 <message-id> - article not wanted (already have it)
				// ReadCodeLine returns: code=238, message="<message-id> article wanted"
				parts := strings.Fields(line)
				if len(parts) < 1 {
					log.Printf("Malformed CHECK response: %s", line)
					rr.retChan <- struct{}{}
					return
				}
				if parts[0] != *rr.MsgID {
					log.Printf("Mismatched CHECK response: expected %s, got %s", *rr.MsgID, parts[0])
					rr.retChan <- struct{}{}
					return
				}
				rs.Mux.Lock()
				job, exists := rs.jobMap[rr.MsgID]
				rs.Mux.Unlock()
				if !exists {
					log.Printf("ERROR in CheckWorker: ReadCheckResponse msgId did not exist in jobMap: %s", *rr.MsgID)
					rr.retChan <- struct{}{}
					continue loop
				}
				rs.Mux.Lock()
				rs.jobMap[rr.MsgID] = nil // Nil the pointer before deleting
				delete(rs.jobMap, rr.MsgID)
				rs.jobsReadOK[job]++
				rs.Mux.Unlock()
				switch code {
				case 238:
					//log.Printf("Wanted Article '%s': response=%d", *msgID, code)
					job.AppendWantedMessageID(rr.MsgID)

				case 438:
					//log.Printf("Unwanted Article '%s': response=%d", *msgID, code)
					job.Increment(nntp.IncrFLAG_UNWANTED)

				case 431:
					job.Increment(nntp.IncrFLAG_RETRY)

				default:
					log.Printf("Newsgroup: '%s' | Unknown CHECK response: line='%s' code=%d expected msgID %s", *job.Newsgroup, line, code, *rr.MsgID)
				}
				// check if all jobs are done
				rs.Mux.Lock()
				queuedCount, qexists := rs.jobsQueued[job]
				readCount, rexists := rs.jobsReadOK[job]
				rs.Mux.Unlock()
				if !qexists || !rexists {
					log.Printf("Newsgroup: '%s' | ERROR in CheckWorker: queuedCount or readCount did not exist for a job?!", *job.Newsgroup)
					rr.retChan <- struct{}{}
					continue loop
				}
				if queuedCount == readCount {
					rs.Mux.Lock()
					delete(rs.jobsQueued, job)
					delete(rs.jobsReadOK, job)
					rs.Mux.Unlock()
					if len(job.WantedIDs) > 0 {
						// Pass job to TAKETHIS worker via channel
						takeThisChan <- job // local takethis chan sharing the same connection
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): Sent job #%d to TAKETHIS worker (wanted: %d/%d)", *job.Newsgroup, id, job.JobID, len(job.WantedIDs), queuedCount)
					} else {
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d got %d CHECK responses but server wants none", *job.Newsgroup, id, job.JobID, queuedCount)
						// Send response and close channel for jobs with no wanted articles
						job.Response(&nntp.TTResponse{Job: job, Err: nil})
					}
				}
				rr.retChan <- struct{}{}
			} // end select
		} // end forever
	}()

	// launch a goroutine to process TAKETHIS jobs from local channel sharing the same connection
	go func() {
		defer func() {
			errChan <- struct{}{}
		}()

		for {
			if common.WantShutdown() {
				log.Printf("CheckWorker (%d): TAKETHIS worker WantShutdown, exiting", id)
				return
			}
			var job *nntp.CHTTJob
			select {
			case job1 := <-nntp.TakeThisQueue:
				job = job1
				if job != nil {
					log.Printf("Newsgroup: '%s' | CheckWorker (%d): Received TAKETHIS job #%d from global queue (wanted: %d)", *job.Newsgroup, id, job.JobID, len(job.WantedIDs))
				}
			case job2 := <-takeThisChan:
				job = job2
				if job != nil {
					log.Printf("Newsgroup: '%s' | CheckWorker (%d): Received TAKETHIS job #%d from local CHECK channel (wanted: %d)", *job.Newsgroup, id, job.JobID, len(job.WantedIDs))
				}
			}

			// Check for nil job (channel closed)
			if job == nil {
				log.Printf("CheckWorker (%d): Received nil job, channels may be closing", id)
				continue
			}

			// Build list of wanted articles
			wantedArticles := make([]*models.Article, 0, len(job.WantedIDs))
			for _, wantedID := range job.WantedIDs {
				if wantedID != nil {
					if article, exists := job.ArticleMap[wantedID]; exists {
						wantedArticles = append(wantedArticles, article)
					}
				}
			}

			if len(wantedArticles) == 0 {
				log.Printf("Newsgroup: '%s' | CheckWorker (%d): No valid wanted articles found in ArticleMap for job #%d", *job.Newsgroup, id, job.JobID)
				job.Response(&nntp.TTResponse{Job: nil, Err: fmt.Errorf("got job without valid wanted articles")})
				continue
			}

			common.ChanLock(flipflopChan)
			log.Printf("Newsgroup: '%s' | CheckWorker (%d): Sending TAKETHIS for job #%d with %d wanted articles", *job.Newsgroup, id, job.JobID, len(wantedArticles))
			// Send TAKETHIS commands using existing function
			transferred, rejected, redis_cached, err := sendArticlesBatchViaTakeThis(conn, wantedArticles, job, *job.Newsgroup, rs.redisCli)
			common.ChanRelease(flipflopChan)

			if err != nil {
				log.Printf("Newsgroup: '%s' | CheckWorker (%d): Error in TAKETHIS job #%d: %v", *job.Newsgroup, id, job.JobID, err)
				job.Response(&nntp.TTResponse{Job: job, Err: err})
				continue
			}

			log.Printf("Newsgroup: '%s' | CheckWorker (%d): TAKETHIS job #%d completed: transferred=%d, rejected=%d, redis_cached=%d", *job.Newsgroup, id, job.JobID, transferred, rejected, redis_cached)

			// Send response back
			log.Printf("Newsgroup: '%s' | CheckWorker (%d): Sending TTresponse for job #%d to responseChan len=%d", *job.Newsgroup, id, job.JobID, len(job.ResponseChan))
			job.Response(&nntp.TTResponse{Job: job, Err: nil})
			log.Printf("Newsgroup: '%s' | CheckWorker (%d): Sent TTresponse for job #%d to responseChan", *job.Newsgroup, id, job.JobID)

		}
	}()

	for {
		select {
		case <-errChan:
			errChan <- struct{}{}
			return
		case job := <-checkQueue:
			if common.WantShutdown() {
				log.Printf("CheckWorker: WantShutdown, exiting")
				return
			}
			if job == nil || len(job.MessageIDs) == 0 {
				log.Printf("CheckWorker: empty job, skipping")
				if job != nil {
					job.Response(&nntp.TTResponse{Job: nil, Err: fmt.Errorf("got job without valid wanted articles")})
				}
				continue
			}

			// Build jobMap for tracking which message IDs belong to this job
			// and count queued messages
			rs.Mux.Lock()
			queueFull := len(rs.jobs) > 0
			if queueFull {
				log.Printf("Newsgroup: '%s' | CheckWorker (%d): got job #%d with %d message IDs. queued=%d ... waiting...", *job.Newsgroup, id, job.JobID, len(job.MessageIDs), len(rs.jobs))
				requestReplyJobDone <- struct{}{}
			}
			rs.Mux.Unlock()
			if queueFull {
				start := time.Now()
				<-replyJobDone
				log.Printf("Newsgroup: '%s' | CheckWorker (%d): waited %v for previous jobs to clear before queuing job #%d", *job.Newsgroup, id, time.Since(start), job.JobID)
			}
			rs.Mux.Lock()
			job.Mux.Lock()
			for _, msgId := range job.MessageIDs {
				if msgId != nil {
					rs.jobMap[msgId] = job
					rs.jobsQueued[job]++ // counts message ids to read check later
				}
			}
			job.Mux.Unlock()
			// Add job to processing queue
			rs.jobs = append(rs.jobs, job)
			rs.Mux.Unlock()

			// Signal ticker to process this job
			select {
			case tickChan <- struct{}{}:
			default:
				// tickChan full, will be processed on next tick
			}
		} // end select
	} // end for
} // end func CheckWorker
