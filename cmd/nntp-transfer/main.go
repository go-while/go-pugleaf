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
	"strconv"
	"strings"
	"sync"
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

func main() {
	common.VerboseHeaders = false
	config.AppVersion = appVersion
	database.NO_CACHE_BOOT = true // prevents booting caches
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
		batchCheck      = flag.Int("batch-check", 25, "Number of message IDs/articles to send in streamed CHECK/TAKETHIS")
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
	nntphostname, err := db.GetConfigValue("local_nntp_hostname")
	if err != nil || nntphostname == "" {
		log.Printf("Failed to get local_nntp_hostname from database: %v", err)
		os.Exit(1)
	}

	pool := nntp.NewPool(backendConfig)
	defer pool.ClosePool()

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
	shutdownChan := make(chan struct{})
	transferDoneChan := make(chan error, 1)
	// special debug mode to find articles with bad date header... before usenet existed...
	if *debugCapture {
		log.Printf("Debug capture mode enabled - capturing articles without sending")
		*dryRun = true
	}
	// Start transfer process
	var wgP sync.WaitGroup
	wgP.Add(2)
	go func(wgP *sync.WaitGroup, redisCli *redis.Client) {
		defer wgP.Done()
		resultChan := make(chan error, 1)
		resultChan <- runTransfer(db, proc, pool, newsgroups, *batchCheck, *maxThreads, *dryRun, startTime, endTime, shutdownChan, *debugCapture, wgP, redisCli)
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
		for groupName, articles := range debugArticles {
			fmt.Printf("Debug capture - Newsgroup: %s, Articles: %d\n", groupName, len(articles))

			// Get group database for updates if needed
			groupDBs, err := db.GetGroupDBs(groupName)
			if err != nil {
				fmt.Printf("! Error getting group database for %s: %v\n", groupName, err)
				continue
			}

			for _, article := range articles {
				fmt.Printf("# %s: #%d : '%s' | orgDate='%s' parsed='%#v'\n", groupName, article.DBArtNum, article.MessageID, article.DateString, article.DateSent)

				// Track original values to detect changes
				originalDateSent := article.DateSent
				originalDateString := article.DateString

				headers, err := common.ReconstructHeaders(article, true, &nntphostname)
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

// runTransfer performs the actual article transfer process
func runTransfer(db *database.Database, proc *processor.Processor, pool *nntp.Pool, newsgroups []*models.Newsgroup, batchCheck int, maxThreads int, dryRun bool, startTime, endTime *time.Time, shutdownChan <-chan struct{}, debugCapture bool, wgP *sync.WaitGroup, redisCli *redis.Client) error {
	defer wgP.Done()
	var totalTransferred, nothingInDateRange, totalRedisCacheHits uint64
	var totalUnwanted, totalRejected, totalTXErrors, totalConnErrors uint64
	var transferMutex sync.Mutex
	maxThreadsChan := make(chan struct{}, maxThreads)
	var wg sync.WaitGroup
	// Process each newsgroup
	log.Printf("Starting transfer for %d newsgroups", len(newsgroups))
	for _, newsgroup := range newsgroups {
		if proc.WantShutdown(shutdownChan) {
			transferMutex.Lock()
			log.Printf("Shutdown requested, stopping transfer. Total transferred: %d articles", totalTransferred)
			transferMutex.Unlock()
			return nil
		}
		maxThreadsChan <- struct{}{} // acquire a thread slot
		wg.Add(1)
		go func(ng *models.Newsgroup, wg *sync.WaitGroup, redisCli *redis.Client) {
			defer func(wg *sync.WaitGroup) {
				wg.Done()
				<-maxThreadsChan // release the thread slot
			}(wg)
			if proc.WantShutdown(shutdownChan) {
				return
			}
			start := time.Now()
			if VERBOSE {
				log.Printf("Starting transfer for newsgroup: %s", newsgroup.Name)
			}
			transferred, checked, rc, unwanted, rejected, txErrors, connErrors, err := transferNewsgroup(db, proc, pool, newsgroup, batchCheck, dryRun, startTime, endTime, shutdownChan, debugCapture, redisCli)

			transferMutex.Lock()
			totalTransferred += transferred
			totalRedisCacheHits += rc
			totalUnwanted += unwanted
			totalRejected += rejected
			totalTXErrors += txErrors
			totalConnErrors += connErrors
			if err == ErrNotInDateRange {
				nothingInDateRange++
				err = nil // not a real error
			}
			transferMutex.Unlock()

			if err != nil {
				log.Printf("Error transferring newsgroup %s: %v", newsgroup.Name, err)
			} else {
				if startTime == nil && endTime == nil {
					log.Printf("DONE runTransfer Newsgroup '%s' | transferred %d articles. checked %d. took %v", newsgroup.Name, transferred, checked, time.Since(start))
				}
			}
		}(newsgroup, &wg, redisCli)
	}

	// Wait for all transfers to complete
	wg.Wait()
	if nothingInDateRange > 0 {
		log.Printf("Note: %d newsgroups had no articles in the specified date range", nothingInDateRange)
	}
	for _, result := range results {
		log.Print(result)
	}
	log.Printf("Summary: Total %d articles transferred | redis_cache_hits: %d | unwanted: %d | rejected: %d | TX_Errors: %d | connErrors: %d", totalTransferred, totalRedisCacheHits, totalUnwanted, totalRejected, totalTXErrors, totalConnErrors)
	return nil
}

type takeThisMode struct {
	Unwanted             uint64
	Rejected             uint64
	TX_Errors            uint64
	connErrors           uint64
	takeThisSuccessCount uint64
	takeThisTotalCount   uint64
	useCheckMode         bool // Start with TAKETHIS mode (false)
}

var debugArticles = make(map[string][]*models.Article)
var debugMutex sync.Mutex
var ErrNotInDateRange = fmt.Errorf("article not in specified date range")

// transferNewsgroup transfers articles from a single newsgroup
func transferNewsgroup(db *database.Database, proc *processor.Processor, pool *nntp.Pool, newsgroup *models.Newsgroup, batchCheck int, dryRun bool, startTime, endTime *time.Time, shutdownChan <-chan struct{}, debugCapture bool, redisCli *redis.Client) (transferred uint64, checked uint64, redis_cache_hits uint64, unwanted uint64, rejected uint64, txErrors uint64, connErrors uint64, err error) {

	// Get group database
	groupDBs, err := db.GetGroupDBs(newsgroup.Name)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("failed to get group DBs for newsgroup '%s': %v", newsgroup.Name, err)
	}
	defer func() {
		if ferr := db.ForceCloseGroupDBs(groupDBs); ferr != nil {
			log.Printf("ForceCloseGroupDBs error for '%s': %v", newsgroup.Name, ferr)
		}
	}()

	// Get total article count first with date filtering
	totalArticles, err := getArticleCountWithDateFilter(groupDBs, startTime, endTime)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("failed to get article count for newsgroup '%s': %v", newsgroup.Name, err)
	}

	if totalArticles == 0 {

		if startTime != nil || endTime != nil {
			if VERBOSE {
				log.Printf("No articles found in newsgroup: %s (within specified date range)", newsgroup.Name)
			}
			return 0, 0, 0, 0, 0, 0, 0, ErrNotInDateRange
		} else {
			log.Printf("No articles found in newsgroup: %s", newsgroup.Name)
		}

		return 0, 0, 0, 0, 0, 0, 0, nil
	}

	if dryRun {
		if startTime != nil || endTime != nil {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s (within specified date range)", totalArticles, newsgroup.Name)
		} else {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s", totalArticles, newsgroup.Name)
		}
		if !debugCapture {
			return 0, 0, 0, 0, 0, 0, 0, nil
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
	// Process articles in database batches (much larger than network batches)
	ttMode := &takeThisMode{
		useCheckMode: true, // start with CHECK mode
	}
	start := time.Now()
	for offset := ioffset; offset < totalArticles; offset += dbBatchSize {
		if proc.WantShutdown(shutdownChan) {
			log.Printf("WantShutdown in newsgroup: %s: Transferred %d articles", newsgroup.Name, transferred)
			return transferred, checked, redis_cache_hits, ttMode.Unwanted, ttMode.Rejected, ttMode.TX_Errors, ttMode.connErrors, nil
		}

		// Load batch from database with date filtering
		articles, err := getArticlesBatchWithDateFilter(groupDBs, offset, startTime, endTime)
		if err != nil {
			log.Printf("Error loading article batch (offset %d) for newsgroup %s: %v", offset, newsgroup.Name, err)
			continue
		}

		if len(articles) == 0 {
			//log.Printf("No more articles in newsgroup %s (offset %d)", newsgroup.Name, offset)
			break
		}
		if dryRun && debugCapture {
			debugMutex.Lock()
			debugArticles[newsgroup.Name] = append(debugArticles[newsgroup.Name], articles...)
			debugMutex.Unlock()
			return 0, 0, 0, 0, 0, 0, 0, nil
		}
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Loaded %d articles from database (offset %d)", newsgroup.Name, len(articles), offset)
		}
		isleep := time.Second
		// Process articles in network batches
		for i := 0; i < len(articles); i += batchCheck {
			if proc.WantShutdown(shutdownChan) {
				log.Printf("WantShutdown in newsgroup: %s: Transferred %d articles", newsgroup.Name, transferred)
				return transferred, checked, redis_cache_hits, ttMode.Unwanted, ttMode.Rejected, ttMode.TX_Errors, ttMode.connErrors, nil
			}
			if !ttMode.useCheckMode && ttMode.takeThisTotalCount >= 100 {
				ttMode.takeThisSuccessCount = 0
				ttMode.takeThisTotalCount = 0
			}
			// Determine end index for the batch
			end := i + batchCheck
			if end > len(articles) {
				end = len(articles)
			}
			// forever: will process this batch until successful or shutdown
		forever:
			for {
				if proc.WantShutdown(shutdownChan) {
					log.Printf("WantShutdown in newsgroup: %s: Transferred %d articles", newsgroup.Name, transferred)
					return transferred, checked, redis_cache_hits, ttMode.Unwanted, ttMode.Rejected, ttMode.TX_Errors, ttMode.connErrors, nil
				}
				if isleep > time.Minute {
					isleep = time.Minute
				}
				if isleep > time.Second {
					log.Printf("Newsgroup: '%s' | Sleeping %v before retrying batch %d-%d (transferred %d so far)", newsgroup.Name, isleep, i+1, end, transferred)
					time.Sleep(isleep)
				}
				// Get connection from pool
				conn, err := pool.Get(nntp.MODE_STREAM_MV)
				if err != nil {
					log.Printf("Newsgroup: '%s' | Failed to get connection from pool: %v", newsgroup.Name, err)
					isleep = isleep * 2
					continue forever
				}

				if conn.ModeReader {
					if VERBOSE {
						log.Printf("got connection in reader mode, closing and getting a new one")
					}
					conn.ForceClose = true
					pool.Put(conn)
					continue forever
				}

				batchTransferred, batchChecked, TTsuccessRate, rc, berr := processBatch(conn, newsgroup.Name, ttMode, articles[i:end], redisCli)
				transferred += batchTransferred
				redis_cache_hits += rc
				checked += batchChecked
				if berr != nil {
					log.Printf("Newsgroup: '%s' | Error processing network batch: %v ... retry", newsgroup.Name, berr)
					if !conn.ForceClose {
						conn.ForceClose = true
						pool.Put(conn)
					}
					isleep = isleep * 2
					continue forever
				}
				if VERBOSE || (transferred >= 1000 && transferred%1000 == 0) || (checked >= 1000 && checked%1000 == 0) {
					log.Printf("Newsgroup: '%s' | BatchDone (offset %d/%d) %d-%d TX:%d check=%t ttRate=%.1f%% checked=%d redis_cache_hits=%d/%d", newsgroup.Name, offset, totalArticles, i+1, end, batchTransferred, ttMode.useCheckMode, TTsuccessRate, batchChecked, rc, redis_cache_hits)
				}
				pool.Put(conn)
				break forever
			}
		}

		// Clear articles slice to free memory
		for i := range articles {
			articles[i] = nil // free memory
		}
		remainingArticles -= int64(len(articles))
		var batchSuccessRate float64
		if transferred > 0 {
			batchSuccessRate = float64(transferred) / float64(len(articles)) * 100.0
		}
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Done (offset %d/%d) total: %d/%d (unw: %d / rej: %d) (Check=%t) ttRate=%.1f%%", newsgroup.Name, offset, totalArticles, transferred, remainingArticles, ttMode.Unwanted, ttMode.Rejected, ttMode.useCheckMode, batchSuccessRate)
		}
		articles = nil // free memory
	} // end for offset range totalArticles
	result := fmt.Sprintf("END Newsgroup: '%s' | total transferred: %d articles / total articles: %d (unwanted: %d | rejected: %d | checked: %d) TX_Errors: %d, connErrors: %d, took %v", newsgroup.Name, transferred, totalArticles, ttMode.Unwanted, ttMode.Rejected, checked, ttMode.TX_Errors, ttMode.connErrors, time.Since(start))
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
	return transferred, checked, redis_cache_hits, ttMode.Unwanted, ttMode.Rejected, ttMode.TX_Errors, ttMode.connErrors, nil
} // end func transferNewsgroup

var results []string
var rejectedArticles = make(map[string][]string)
var resultsMutex sync.RWMutex
var lowerLevel float64 = 90.0
var upperLevel float64 = 95.0

// processBatch processes a batch of articles using NNTP streaming protocol (RFC 4644)
// Uses TAKETHIS primarily, falls back to CHECK when success rate < 95%
func processBatch(conn *nntp.BackendConn, newsgroup string, ttMode *takeThisMode, articles []*models.Article, redisCli *redis.Client) (transferred uint64, checked uint64, successRate float64, redis_cache_hits uint64, err error) {

	if len(articles) == 0 {
		return 0, 0, 0, 0, nil
	}

	// Calculate success rate to determine whether to use CHECK or TAKETHIS
	if ttMode.takeThisTotalCount > 0 {
		successRate = float64(ttMode.takeThisSuccessCount) / float64(ttMode.takeThisTotalCount) * 100.0
	}

	// Switch to CHECK mode if TAKETHIS success rate drops below lowerLevel
	if successRate < lowerLevel && ttMode.takeThisTotalCount >= 10 { // Need at least 10 attempts for meaningful stats
		ttMode.useCheckMode = true
		//log.Printf("newsgroup %s: TAKETHIS success rate %.1f%% < %d%%, switching to CHECK mode", newsgroup, successRate, lowerLevel)
	} else if successRate >= upperLevel && ttMode.takeThisTotalCount >= 20 { // Switch back when rate improves
		ttMode.useCheckMode = false
		//log.Printf("newsgroup %s: TAKETHIS success rate %.1f%% >= %d%%, switching back to TAKETHIS mode", newsgroup, successRate, upperLevel)
	}

	articleMap := make(map[string]*models.Article)
	for _, article := range articles {
		articleMap[article.MessageID] = article
	}

	switch ttMode.useCheckMode {
	case true: // ttMode.useCheckMode
		// CHECK mode: verify articles are wanted before sending
		//log.Printf("Newsgroup: '%s' | CHECK: %d articles (success rate: %.1f%%)", newsgroup, len(articles), successRate)

		messageIds := make([]*string, len(articles))
		for i, article := range articles {
			if article == nil {
				continue
			}
			if strings.Contains(article.MessageID, ">?<") {
				log.Printf("ERROR: Invalid message ID contains '>?<' character: '%s' - skipping article", article.MessageID)
				messageIds[i] = nil
				os.Exit(1)
				continue
			}
			if len(article.MessageID) > 128 { // Reasonable message ID length limit
				log.Printf("WARN: long message ID: '%s' (%d chars)", article.MessageID, len(article.MessageID))
			}
			messageIds[i] = &article.MessageID
		}
		if len(messageIds) == 0 {
			log.Printf("No message IDs in batch, skipping")
			return transferred, checked, successRate, redis_cache_hits, nil
		}

		// Remove nil entries and batch check Redis cache using pipeline
		validMessageIds := make([]*string, 0, len(messageIds))
		validArticles := make([]*models.Article, 0, len(articles))
		validArticleMap := make(map[string]*models.Article)

		// Collect non-nil message IDs for batch Redis check
		nonNilIndices := make([]int, 0, len(messageIds))
		nonNilMsgIds := make([]*string, 0, len(messageIds))
		for i, msgID := range messageIds {
			if msgID != nil {
				nonNilIndices = append(nonNilIndices, i)
				nonNilMsgIds = append(nonNilMsgIds, msgID)
			}
		}

		// Batch check Redis cache using pipeline (1 round trip for all keys)
		if redisCli != nil && len(nonNilMsgIds) > 0 {
			pipe := redisCli.Pipeline()
			cmds := make([]*redis.IntCmd, len(nonNilMsgIds))

			// Queue all EXISTS commands
			for i, msgID := range nonNilMsgIds {
				cmds[i] = pipe.Exists(redisCtx, *msgID)
			}

			// Execute all in one network round trip
			_, err := pipe.Exec(redisCtx)
			if err != nil && VERBOSE {
				log.Printf("Newsgroup: '%s' | Redis pipeline error: %v", newsgroup, err)
			}

			// Process results
			for i, cmd := range cmds {
				idx := nonNilIndices[i]
				msgID := nonNilMsgIds[i]

				exists, cmdErr := cmd.Result()
				if cmdErr == nil && exists > 0 {
					// Cached in Redis - skip this article
					if VERBOSE {
						log.Printf("Newsgroup: '%s' | Message ID '%s' is cached in Redis (skip [CHECK])", newsgroup, *msgID)
					}
					redis_cache_hits++
					continue
				}

				// Not cached - add to valid list
				validMessageIds = append(validMessageIds, msgID)
				validArticles = append(validArticles, articles[idx])
				validArticleMap[*msgID] = articles[idx]
			}
		} else {
			// No Redis - add all non-nil message IDs
			for i, msgID := range nonNilMsgIds {
				idx := nonNilIndices[i]
				validMessageIds = append(validMessageIds, msgID)
				validArticles = append(validArticles, articles[idx])
				validArticleMap[*msgID] = articles[idx]
			}
		}

		if len(validMessageIds) == 0 {
			log.Printf("WARN: No valid message IDs found in batch, skipping")
			return transferred, checked, successRate, redis_cache_hits, nil
		}
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Sending CHECK commands for %d valid articles (filtered from %d)", newsgroup, len(validMessageIds), len(articles))
		}

		// Send CHECK commands for all message IDs
		checkResponses, err := conn.CheckMultiple(validMessageIds)
		if err != nil {
			ttMode.connErrors++
			conn.ForceClose = true
			conn.Pool.Put(conn)
			return transferred, checked, successRate, redis_cache_hits, fmt.Errorf("Newsgroup: '%s' | failed to send CHECK command: %v", newsgroup, err)
		}

		// Find wanted articles
		wantedIds := make([]*string, 0)
		for _, response := range checkResponses {
			checked++
			if response.Wanted {
				wantedIds = append(wantedIds, response.MessageID)
			} else {
				//log.Printf("Unwanted Article '%s': response=%d", *response.MessageID, response.Code)
				ttMode.Unwanted++
			}
		}

		if len(wantedIds) == 0 {
			//log.Printf("No articles wanted by server in this batch")
			if !ttMode.useCheckMode {
				ttMode.useCheckMode = true
				ttMode.takeThisSuccessCount = 0
				ttMode.takeThisTotalCount = uint64(len(validMessageIds))
			}
			return transferred, checked, successRate, redis_cache_hits, nil
		}
		if ttMode.useCheckMode && len(wantedIds) == len(validMessageIds) {
			// use TAKETHIS mode if all articles are wanted
			ttMode.useCheckMode = false
			ttMode.takeThisSuccessCount = 0
			ttMode.takeThisTotalCount = 0
		}
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Server wants: %d/%d articles in batch", newsgroup, len(wantedIds), len(validMessageIds))
		}
		var validTakeThisArticles []*models.Article
		// Send TAKETHIS for wanted articles
		for _, msgId := range wantedIds {
			article, exists := validArticleMap[*msgId]
			if !exists {
				log.Printf("WARN: Article not found in validArticleMap for msgId: %s", *msgId)
				continue
			}
			validTakeThisArticles = append(validTakeThisArticles, article)
		}

		txcount, rc, err := sendArticlesBatchViaTakeThis(conn, validTakeThisArticles, ttMode, newsgroup, redisCli)
		transferred += txcount
		redis_cache_hits += rc
		if conn.ForceClose {
			return transferred, checked, successRate, redis_cache_hits, fmt.Errorf("Newsgroup: '%s' | connection marked for close, aborting batch. err='%v'", newsgroup, err)
		}
		if err != nil {
			log.Printf("Newsgroup: '%s' | Failed to send CHECKED TAKETHIS: %v", newsgroup, err)
			return transferred, checked, successRate, redis_cache_hits, fmt.Errorf("failed to send CHECKED TAKETHIS batch: %v", err)
		}
	// end case ttMode.useCheckMode
	// case !ttMode.useCheckMode
	case false:
		// TAKETHIS mode: send articles directly and track success rate
		//log.Printf("Newsgroup: '%s' | TAKETHIS: %d articles (success rate: %.1f%%)", newsgroup, len(articles), successRate)

		// Validate articles before sending in TAKETHIS mode
		validTakeThisArticles := make([]*models.Article, 0, len(articles))
		for _, article := range articles {
			if article == nil {
				continue
			}
			if strings.Contains(article.MessageID, ">?<") {
				log.Printf("ERROR: Invalid message ID contains '>?<' character in TAKETHIS mode: '%s' - skipping", article.MessageID)
				os.Exit(1)
				continue
			}
			if len(article.MessageID) > 128 {
				log.Printf("WARN: Message ID very long in TAKETHIS mode (%d chars): '%.100s...'", len(article.MessageID), article.MessageID)
			}
			validTakeThisArticles = append(validTakeThisArticles, article)
		}

		if len(validTakeThisArticles) == 0 {
			log.Printf("WARN: No valid articles for TAKETHIS mode, skipping batch")
			return transferred, checked, successRate, redis_cache_hits, nil
		}

		if len(validTakeThisArticles) != len(articles) {
			log.Printf("Newsgroup: '%s' | Filtered articles for TAKETHIS: %d valid from %d total", newsgroup, len(validTakeThisArticles), len(articles))
		}

		txcount, rc, err := sendArticlesBatchViaTakeThis(conn, validTakeThisArticles, ttMode, newsgroup, redisCli)
		transferred += txcount
		redis_cache_hits += rc
		if conn.ForceClose {
			return transferred, checked, successRate, redis_cache_hits, fmt.Errorf("Newsgroup: '%s' | connection marked for close, aborting batch. err='%v'", newsgroup, err)
		}
		if err != nil {
			return transferred, checked, successRate, redis_cache_hits, fmt.Errorf("failed to send TAKETHIS batch: %v", err)
		}
		if txcount == 0 {
			if !ttMode.useCheckMode {
				ttMode.useCheckMode = true
				ttMode.takeThisSuccessCount = 0
				ttMode.takeThisTotalCount = uint64(len(validTakeThisArticles))
			}
		}
		return transferred, checked, successRate, redis_cache_hits, nil
	} // end case !ttMode.useCheckMode
	// end switch ttMode.useCheckMode

	return transferred, checked, successRate, redis_cache_hits, nil
} // end func processBatch

// sendArticlesBatchViaTakeThis sends multiple articles via TAKETHIS in streaming mode
// Sends all TAKETHIS commands first, then reads all responses (true streaming)
func sendArticlesBatchViaTakeThis(conn *nntp.BackendConn, articles []*models.Article, ttMode *takeThisMode, newsgroup string, redisCli *redis.Client) (transferred uint64, redis_cached uint64, err error) {
	if len(articles) == 0 {
		return 0, 0, nil
	}

	// Phase 1: Send all TAKETHIS commands without waiting for responses
	//log.Printf("Phase 1: Sending %d TAKETHIS commands...", len(articles))

	commandIDs := make([]uint, 0, len(articles))
	validArticles := make([]*models.Article, 0, len(articles))

	// Batch check Redis cache using pipeline before sending TAKETHIS
	if redisCli != nil && len(articles) > 0 {
		pipe := redisCli.Pipeline()
		cmds := make([]*redis.IntCmd, len(articles))

		// Queue all EXISTS commands
		for i, article := range articles {
			cmds[i] = pipe.Exists(redisCtx, article.MessageID)
		}

		// Execute all in one network round trip
		_, err := pipe.Exec(redisCtx)
		if err != nil && VERBOSE {
			log.Printf("Newsgroup: '%s' | Redis pipeline error in TAKETHIS: %v", newsgroup, err)
		}

		// Process results and filter cached articles
		for i, cmd := range cmds {
			article := articles[i]
			exists, cmdErr := cmd.Result()
			if cmdErr == nil && exists > 0 {
				// Cached in Redis - skip this article
				if VERBOSE {
					log.Printf("Newsgroup: '%s' | Message ID '%s' is cached in Redis (skip [TAKETHIS])", newsgroup, article.MessageID)
				}
				articles[i] = nil // free memory
				redis_cached++
				continue
			}
			// Not cached - will be sent
		}
	}

	// Now send TAKETHIS for non-cached articles
	for _, article := range articles {
		if article == nil {
			continue // Skip cached articles
		}
		// Send TAKETHIS command with article content (non-blocking)
		cmdID, err := conn.SendTakeThisArticleStreaming(article, &processor.LocalNNTPHostname)
		if err != nil {
			if err == common.ErrNoNewsgroups {
				log.Printf("Newsgroup: '%s' | skipped article '%s': no newsgroups header", newsgroup, article.MessageID)
				continue
			}
			ttMode.connErrors++
			conn.ForceClose = true
			conn.Pool.Put(conn)
			log.Printf("ERROR Newsgroup: '%s' | Failed to send TAKETHIS for %s: %v", newsgroup, article.MessageID, err)
			return 0, redis_cached, fmt.Errorf("failed to send TAKETHIS for %s: %v", article.MessageID, err)
		}

		commandIDs = append(commandIDs, cmdID)
		validArticles = append(validArticles, article)
	}

	//log.Printf("Sent %d TAKETHIS commands, reading responses...", len(commandIDs))
	var done []*string
	// Phase 2: Read all responses in order
	for i, cmdID := range commandIDs {
		article := validArticles[i]

		takeThisResponseCode, err := conn.ReadTakeThisResponseStreaming(cmdID)
		if err != nil {
			ttMode.connErrors++
			conn.ForceClose = true
			conn.Pool.Put(conn)
			log.Printf("ERROR Newsgroup: '%s' | Failed to read TAKETHIS response for %s: %v", newsgroup, article.MessageID, err)
			return transferred, redis_cached, fmt.Errorf("failed to read TAKETHIS response for %s: %v", article.MessageID, err)
		}

		// Update success rate tracking
		ttMode.takeThisTotalCount++
		switch takeThisResponseCode {
		case 239:
			ttMode.takeThisSuccessCount++
			transferred++
		case 439:
			ttMode.Rejected++
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | Rejected article '%s': response=%d (i=%d/%d)", newsgroup, article.MessageID, takeThisResponseCode, i+1, len(commandIDs))
				//resultsMutex.Lock()
				//rejectedArticles[newsgroup] = append(rejectedArticles[newsgroup], article.MessageID)
				//resultsMutex.Unlock()
			}
		case 400, 480, 500, 501, 502, 503, 504:
			log.Printf("ERROR Newsgroup: '%s' | Failed to transfer article '%s': response=%d (i=%d/%d)", newsgroup, article.MessageID, takeThisResponseCode, i+1, len(commandIDs))
			ttMode.TX_Errors++
			conn.ForceClose = true
			conn.Pool.Put(conn)
			return transferred, redis_cached, fmt.Errorf("failed to transfer article '%s': response=%d", article.MessageID, takeThisResponseCode)

		default:
			log.Printf("ERROR Newsgroup: '%s' | Failed to transfer article '%s': unknown response=%d (i=%d/%d)", newsgroup, article.MessageID, takeThisResponseCode, i+1, len(commandIDs))
			ttMode.TX_Errors++
			continue
		}
		if redisCli != nil {
			done = append(done, &article.MessageID)
		}
	} // end for commandIDs

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
		log.Printf("Newsgroup: '%s' | Batch transferred: %d/%d articles. redis_cached=%d", newsgroup, transferred, len(articles), redis_cached)
	}
	return transferred, redis_cached, nil
} // end func sendArticlesBatchViaTakeThis
