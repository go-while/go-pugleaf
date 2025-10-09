// NNTP article transfer tool for go-pugleaf
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	_ "net/http/pprof" // Memory profiling
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
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
	fmt.Println("Memory Profiling & Monitoring:")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.* -mem-stats")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.* -pprof-port 6060")
	fmt.Println("  ./nntp-transfer -host news.server.local -group alt.* -gc-percent 50")
	fmt.Println("  # -mem-stats: Log memory stats every 30 seconds")
	fmt.Println("  # -pprof-port: Enable pprof at http://localhost:6060/debug/pprof/")
	fmt.Println("  # -gc-percent: Lower values = more GC, less memory (default 100)")
	fmt.Println("  # Get heap profile: curl http://localhost:6060/debug/pprof/heap > heap.prof")
	fmt.Println("  # Analyze: go tool pprof heap.prof")
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

		// Web server and profiling options
		webPort   = flag.Int("web-port", 0, "Enable web server on this port to view results (e.g. 8080, default: disabled)")
		pprofPort = flag.Int("pprof-port", 0, "Enable pprof profiling server on this port (e.g., 6060). Access at http://localhost:PORT/debug/pprof/")
		memStats  = flag.Bool("mem-stats", false, "Log memory statistics every 30 seconds")
		gcPercent = flag.Int("gc-percent", 100, "Set GOGC percentage (default 100). Lower values = more frequent GC, less memory")
	)
	flag.Parse()
	common.IgnoreGoogleHeaders = *ignoreGoogleHeaders

	// Configure garbage collector
	if *gcPercent != 100 {
		old := debug.SetGCPercent(*gcPercent)
		log.Printf("Set GOGC from %d to %d (lower = more GC, less memory)", old, *gcPercent)
	}

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

	// Start web server if port is specified
	if *webPort > 0 {
		go startWebServer(*webPort)
	}

	// Start pprof server if port is specified
	if *pprofPort > 0 {
		go func() {
			addr := fmt.Sprintf("localhost:%d", *pprofPort)
			log.Printf("Starting pprof server on http://%s/debug/pprof/", addr)
			log.Printf("  Heap profile: http://%s/debug/pprof/heap", addr)
			log.Printf("  Goroutines:   http://%s/debug/pprof/goroutine", addr)
			log.Printf("  Allocs:       http://%s/debug/pprof/allocs", addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				log.Printf("pprof server error: %v", err)
			}
		}()
	}

	// Start memory stats monitoring if enabled
	if *memStats {
		go monitorMemoryStats()
	}

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
	wgP.Wait()
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
func getArticlesBatchWithDateFilter(db *database.Database, ng *models.Newsgroup, offset int64, startTime, endTime *time.Time) ([]*models.Article, error) {
	// Get group database
	groupDBs, err := db.GetGroupDBs(ng.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get group DBs for newsgroup '%s': %v", ng.Name, err)
	}
	defer func() {
		if ferr := db.ForceCloseGroupDBs(groupDBs); ferr != nil {
			log.Printf("ForceCloseGroupDBs error for '%s': %v", ng.Name, ferr)
		}
	}()
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

	start := time.Now()
	var count int64
	err := groupDBs.DB.QueryRow(query, args...).Scan(&count)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		log.Printf("WARNING: Slow COUNT query for group '%s' took %v (count=%d)", groupDBs.Newsgroup, elapsed, count)
	}

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
	for _, ng := range newsgroups {
		if common.WantShutdown() {
			log.Printf("Aborted before next: %s", ng.Name)
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
				log.Printf("Aborted before next: %s", ng.Name)
				return
			}
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | Start", ng.Name)
			}
			err := transferNewsgroup(db, ng, batchCheck, dryRun, startTime, endTime, debugCapture, redisCli)
			if err == ErrNotInDateRange {
				transferMutex.Lock()
				nothingInDateRange++
				transferMutex.Unlock()
				err = nil // not a real error
			}
			if err != nil {
				log.Printf("Error transferring newsgroup %s: %v", ng.Name, err)
			}
		}(ng, &wg, redisCli)
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
func processRequeuedJobs(newsgroup string, ttMode *nntp.TakeThisMode, ttResponses chan *nntp.TTSetup, redisCli *redis.Client) (int, error) {
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
		responseChan, err := processBatch(ttMode, job.Articles, redisCli, job.BatchStart, job.BatchEnd, -1, job.OffsetQ, job.NGTProgress)
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
			ttResponses <- &nntp.TTSetup{
				ResponseChan: responseChan,
			}
		}
	}

	log.Printf("Newsgroup: '%s' | Successfully processed %d requeued jobs", newsgroup, len(queuedJobs))
	return len(queuedJobs), nil
}

// transferNewsgroup transfers articles from a single newsgroup
func transferNewsgroup(db *database.Database, ng *models.Newsgroup, batchCheck int, dryRun bool, startTime, endTime *time.Time, debugCapture bool, redisCli *redis.Client) error {

	//log.Printf("Newsgroup: '%s' | transferNewsgroup: Starting (getting group DBs)...", ng.Name)

	// Get group database
	groupDBsA, err := db.GetGroupDBs(ng.Name)
	if err != nil {
		return fmt.Errorf("failed to get group DBs for newsgroup '%s': %v", ng.Name, err)
	}

	//log.Printf("Newsgroup: '%s' | transferNewsgroup: Got group DBs, querying article count...", ng.Name)
	// Initialize newsgroup progress tracking
	resultsMutex.Lock()
	if _, exists := nntp.NewsgroupTransferProgressMap[ng.Name]; !exists {
		nntp.NewsgroupTransferProgressMap[ng.Name] = &nntp.NewsgroupTransferProgress{
			Newsgroup:     &ng.Name,
			Started:       time.Now(),
			LastUpdated:   time.Now(),
			LastCronTX:    time.Now(),
			Finished:      false,
			TotalArticles: 0,
		}
	}
	resultsMutex.Unlock()
	// Get total article count first with date filtering
	totalArticles, err := getArticleCountWithDateFilter(groupDBsA, startTime, endTime)
	if err != nil {
		return fmt.Errorf("failed to get article count for newsgroup '%s': %v", ng.Name, err)
	}

	//log.Printf("Newsgroup: '%s' | transferNewsgroup: Got article count (%d), closing group DBs...", ng.Name, totalArticles)

	if ferr := db.ForceCloseGroupDBs(groupDBsA); ferr != nil {
		log.Printf("ForceCloseGroupDBs error for '%s': %v", ng.Name, ferr)
	}

	//log.Printf("Newsgroup: '%s' | transferNewsgroup: Closed group DBs, checking if articles exist...", ng.Name)

	if totalArticles == 0 {
		resultsMutex.Lock()
		nntp.NewsgroupTransferProgressMap[ng.Name].Finished = true
		nntp.NewsgroupTransferProgressMap[ng.Name].LastUpdated = time.Now()
		results = append(results, fmt.Sprintf("END Newsgroup: '%s' | No articles to process", ng.Name))
		resultsMutex.Unlock()
		// No articles to process
		if startTime != nil || endTime != nil {
			if VERBOSE {
				log.Printf("No articles found in newsgroup: %s (within specified date range)", ng.Name)
			}
			return ErrNotInDateRange
		} else {
			log.Printf("No articles found in newsgroup: %s", ng.Name)
		}

		return nil
	}

	// Initialize newsgroup progress tracking
	resultsMutex.Lock()
	nntp.NewsgroupTransferProgressMap[ng.Name].TotalArticles = totalArticles
	nntp.NewsgroupTransferProgressMap[ng.Name].LastUpdated = time.Now()
	ngtprogress := nntp.NewsgroupTransferProgressMap[ng.Name]
	resultsMutex.Unlock()

	if dryRun {
		if startTime != nil || endTime != nil {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s (within specified date range)", totalArticles, ng.Name)
		} else {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s", totalArticles, ng.Name)
		}
		if !debugCapture {
			return nil
		}
	}

	if !dryRun && !debugCapture {
		if startTime != nil || endTime != nil {
			log.Printf("Found %d articles in newsgroup %s (within specified date range) - processing in batches", totalArticles, ng.Name)
		} else {
			log.Printf("Found %d articles in newsgroup %s - processing in batches", totalArticles, ng.Name)
		}
	}
	//time.Sleep(3 * time.Second) // debug sleep
	var ioffset int64
	remainingArticles := totalArticles
	ttMode := &nntp.TakeThisMode{
		Newsgroup: &ng.Name,
		CheckMode: true,
	}
	ttResponses := make(chan *nntp.TTSetup, totalArticles/int64(batchCheck)+2)
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
		for setup := range ttResponses {
			if setup == nil || setup.ResponseChan == nil {
				log.Printf("Newsgroup: '%s' | Warning: nil TT response channel received in collector!?", ng.Name)
				continue
			}
			num++
			log.Printf("Newsgroup: '%s' | Starting response channel processor num %d (goroutines: %d)", ng.Name, num, runtime.NumGoroutine())
			responseWG.Add(1)
			go func(rc chan *nntp.TTResponse, num uint64) {
				defer responseWG.Done()
				defer log.Printf("Newsgroup: '%s' | Quit response channel processor num %d (goroutines: %d)", ng.Name, num, runtime.NumGoroutine())

				// Read exactly ONE response from this channel (channel is buffered with cap 1)
				resp := <-rc // job.Response(ForceCleanUp, err) arrives here

				if resp == nil {
					log.Printf("Newsgroup: '%s' | Warning: nil TT response received!?", ng.Name)
					return
				}
				if resp.Err != nil {
					log.Printf("Newsgroup: '%s' | Error in TT response job #%d err='%v' job='%v' ForceCleanUp=%t", ng.Name, resp.Job.JobID, resp.Err, resp.Job, resp.ForceCleanUp)
				}
				if resp.Job == nil {
					log.Printf("Newsgroup: '%s' | Warning: nil Job in TT response job without error!? ForceCleanUp=%t", ng.Name, resp.ForceCleanUp)
					return
				}
				// get numbers
				amux.Lock()
				resp.Job.GetUpdateCounters(&transferred, &unwanted, &rejected, &checked, &txErrors, &connErrors)
				amux.Unlock()
				if !resp.ForceCleanUp {
					return
				}
				// free memory - CRITICAL: Lock and unlock in same scope, not with defer!
				resp.Job.Mux.Lock()
				//log.Printf("Newsgroup: '%s' | Cleaning up TT job #%d with %d articles (ForceCleanUp)", ng.Name, resp.Job.JobID, len(resp.Job.Articles))
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
				resp.Job.Mux.Unlock()
				resp.Job = nil
			}(setup.ResponseChan, num)
		}
		log.Printf("Newsgroup: '%s' | Collector: ttResponses closed, waiting for %d response processors to finish...", ng.Name, num)
		// Wait for all response channel processors to finish
		responseWG.Wait()
		log.Printf("Newsgroup: '%s' | Collector: all response processors closed", ng.Name)
		amux.Lock()
		result := fmt.Sprintf("END Newsgroup: '%s' | transferred: %d/%d  | unwanted: %d | rejected: %d | checked: %d | TX_Errors: %d | connErrors: %d | took %v",
			ng.Name, transferred, totalArticles, unwanted, rejected, checked, txErrors, connErrors, time.Since(start))
		amux.Unlock()
		//log.Print(result)
		resultsMutex.Lock()
		results = append(results, result)
		// Mark newsgroup as finished
		if progress, exists := nntp.NewsgroupTransferProgressMap[ng.Name]; exists {
			progress.Mux.Lock()
			progress.Finished = true
			progress.LastUpdated = time.Now()
			progress.TXBytes += progress.TXBytesTMP
			progress.TXBytesTMP = 0
			progress.LastCronTX = progress.LastUpdated
			progress.Mux.Unlock()
		}
		if VERBOSE {
			for _, msgId := range rejectedArticles[ng.Name] {
				// prints all at the end again
				log.Printf("END Newsgroup: '%s' | REJECTED '%s'", ng.Name, msgId)
			}
			delete(rejectedArticles, ng.Name) // free memory
		}
		resultsMutex.Unlock()
	}()
	OffsetQueue := &nntp.OffsetQueue{}
	// Get articles in database batches (much larger than network batches)
	for offset := ioffset; offset < totalArticles; offset += dbBatchSize {
		if common.WantShutdown() {
			log.Printf("WantShutdown in newsgroup: '%s' offset: %d", ng.Name, offset)
			return nil
		}
		// Process any requeued jobs first (from previous failed batches)
		if _, err := processRequeuedJobs(ng.Name, ttMode, ttResponses, redisCli); err != nil {
			return err
		}
		start := time.Now()
		// Load batch from database with date filtering
		articles, err := getArticlesBatchWithDateFilter(db, ng, offset, startTime, endTime)
		if err != nil {
			log.Printf("Error loading article batch (offset %d) for newsgroup %s: %v", offset, ng.Name, err)
			return fmt.Errorf("failed to load article batch (offset %d) for newsgroup '%s': %v", offset, ng.Name, err)
		}

		if len(articles) == 0 {
			//log.Printf("No more articles in newsgroup %s (offset %d)", ng.Name, offset)
			break
		}
		if dryRun && debugCapture {
			debugMutex.Lock()
			debugArticles[ng.Name] = append(debugArticles[ng.Name], articles...)
			debugMutex.Unlock()
			return nil
		}
		//if VERBOSE {
		var size int
		for _, a := range articles {
			size += a.Bytes
		}
		log.Printf("Newsgroup: '%s' | Loaded %d articles from database (offset %d) (Bytes=%d) took %v", ng.Name, len(articles), offset, size, time.Since(start))
		//}
		// Process articles in network batches
		for i := 0; i < len(articles); i += batchCheck {
			OffsetQueue.Add(1)
			if common.WantShutdown() {
				log.Printf("WantShutdown in newsgroup: '%s' (offset %d)", ng.Name, offset)
				return nil
			}
			// Determine end index for the batch
			end := i + batchCheck
			if end > len(articles) {
				end = len(articles)
			}
			// pass articles to CHECK or TAKETHIS queue (async!)
			responseChan, err := processBatch(ttMode, articles[i:end], redisCli, int64(i), int64(end), offset, OffsetQueue, ngtprogress)
			if err != nil {
				log.Printf("Newsgroup: '%s' | Error processing batch %d-%d: %v", ng.Name, i+1, end, err)
				return fmt.Errorf("error processing batch %d-%d for newsgroup '%s': %v", i+1, end, ng.Name, err)
			}
			// pass the response channel to the collector channel: ttResponses
			ttResponses <- &nntp.TTSetup{
				ResponseChan: responseChan,
			}
			OffsetQueue.Wait(2) // wait for offset batches to finish, less than 2 in flight
		}
		remainingArticles -= int64(len(articles))
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Pushed to queue (offset %d/%d) remaining: %d (Check=%t)", ng.Name, offset, totalArticles, remainingArticles, ttMode.UseCHECK())
			//log.Printf("Newsgroup: '%s' | Pushed (offset %d/%d) total: %d/%d (unw: %d / rej: %d) (Check=%t)", ng.Name, offset, totalArticles, transferred, remainingArticles, ttMode.Unwanted, ttMode.Rejected, ttMode.GetMode())
		}

	} // end for offset range totalArticles

	//log.Printf("Newsgroup: '%s' | Main article loop completed, checking for requeued jobs...", ng.Name)

	// Process any remaining requeued jobs after main loop completes
	// This handles failures that occurred in the last batch
	for {
		if common.WantShutdown() {
			log.Printf("WantShutdown during final requeue processing for '%s'", ng.Name)
			break
		}
		processed, err := processRequeuedJobs(ng.Name, ttMode, ttResponses, redisCli)
		if err != nil {
			log.Printf("Newsgroup: '%s' | Error in final requeue processing: %v", ng.Name, err)
			// Don't return error, just log it - we've already processed most articles
			break
		}
		if processed == 0 {
			// No more requeued jobs to process
			break
		}
		//log.Printf("Newsgroup: '%s' | Processed %d requeued jobs in final pass", ng.Name, processed)
		// Loop again to check if any of those jobs failed and were requeued
	}

	//log.Printf("Newsgroup: '%s' | Final requeue processing completed, closing ttResponses channel...", ng.Name)

	// Close the ttResponses channel to signal collector goroutine to finish
	close(ttResponses)

	//log.Printf("Newsgroup: '%s' | ttResponses channel closed, waiting for collector to finish...", ng.Name)

	// Wait for collector goroutine to finish processing all responses
	collectorWG.Wait()

	//log.Printf("Newsgroup: '%s' | All jobs completed and responses collected", ng.Name)

	return nil
} // end func transferNewsgroup

var results []string
var rejectedArticles = make(map[string][]string)
var resultsMutex sync.RWMutex
var lowerLevel float64 = 90.0
var upperLevel float64 = 95.0

// processBatch processes a batch of articles using NNTP streaming protocol (RFC 4644)
// Uses TAKETHIS primarily, falls back to CHECK when success rate < 95%
func processBatch(ttMode *nntp.TakeThisMode, articles []*models.Article, redisCli *redis.Client, batchStart int64, batchEnd int64, dbOffset int64, offsetQ *nntp.OffsetQueue, ngtprogress *nntp.NewsgroupTransferProgress) (chan *nntp.TTResponse, error) {

	if len(articles) == 0 {
		log.Printf("processBatch: no articles in this batch for newsgroup '%s'", *ttMode.Newsgroup)
		return nil, nil
	}

	// Update newsgroup progress with current offset
	resultsMutex.RLock()
	if progress, exists := nntp.NewsgroupTransferProgressMap[*ttMode.Newsgroup]; exists {
		progress.Mux.Lock()
		progress.OffsetStart = dbOffset
		progress.BatchStart = batchStart
		progress.BatchEnd = batchEnd
		progress.LastUpdated = time.Now()
		progress.Mux.Unlock()
	}
	resultsMutex.RUnlock()

	ttMode.FlipMode(lowerLevel, upperLevel)

	batchedJob := &nntp.CHTTJob{
		JobID:        atomic.AddUint64(&nntp.JobIDCounter, 1),
		Newsgroup:    ttMode.Newsgroup,
		MessageIDs:   make([]*string, 0, len(articles)),
		Articles:     make([]*models.Article, 0, len(articles)),
		ArticleMap:   make(map[*string]*models.Article, len(articles)),
		ResponseChan: make(chan *nntp.TTResponse, 1),
		TTMode:       ttMode,
		OffsetStart:  dbOffset,
		BatchStart:   batchStart,
		BatchEnd:     batchEnd,
		OffsetQ:      offsetQ,
		NGTProgress:  ngtprogress,
	}
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

	// Track queue length for load balancing
	WorkerQueueLengthMux.Lock()
	WorkerQueueLength[workerID]++
	WorkerQueueLengthMux.Unlock()

	//log.Printf("Newsgroup: '%s' | CheckWorker (%d) queue job #%d with %d message IDs. CheckQ=%d", *ttMode.Newsgroup, workerID, batchedJob.JobID, len(batchedJob.MessageIDs), len(CheckQueues[workerID]))
	CheckQueues[workerID] <- batchedJob // checkQueue <- batchedJob
	log.Printf("Newsgroup: '%s' | CheckWorker (%d) queued Job #%d", *ttMode.Newsgroup, workerID, batchedJob.JobID)
	return batchedJob.ResponseChan, nil

	/* disabled
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

		// Track queue length for load balancing
		WorkerQueueLengthMux.Lock()
		WorkerQueueLength[workerID]++
		WorkerQueueLengthMux.Unlock()

		log.Printf("Newsgroup: '%s' | CheckWorker (%d) Queue job #%d with %d message IDs", *ttMode.Newsgroup, workerID, batchedJob.JobID, len(batchedJob.MessageIDs))
		CheckQueues[workerID] <- batchedJob // checkQueue <- batchedJob
		log.Printf("Newsgroup: '%s' | CheckWorker (%d) Job #%d queued", *ttMode.Newsgroup, workerID, batchedJob.JobID)
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
		log.Printf("Newsgroup: '%s' | job #%d Sending to TakeThisQueue with %d articles", *ttMode.Newsgroup, batchedJob.JobID, len(batchedJob.WantedIDs))
		nntp.TakeThisQueue <- batchedJob
		log.Printf("Newsgroup: '%s' | Job #%d sent to TakeThisQueue successfully", *ttMode.Newsgroup, batchedJob.JobID)
		return batchedJob.ResponseChan, nil

	} // end case !ttMode.CheckMode
	// end switch ttMode.CheckMode

	return nil, nil
	*/
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
	conn.Lock()
	for _, article := range articles {
		if article == nil {
			continue // Skip cached articles
		}
		// Send TAKETHIS command with article content (non-blocking)
		log.Printf("Newsgroup: '%s' | Pre-Send TAKETHIS '%s'", newsgroup, article.MessageID)
		cmdID, txBytes, err := conn.SendTakeThisArticleStreaming(article, &processor.LocalNNTPHostname, newsgroup)
		job.NGTProgress.AddNGTP(0, 0, int64(txBytes))
		if err != nil {
			if err == common.ErrNoNewsgroups {
				log.Printf("Newsgroup: '%s' | skipped TAKETHIS '%s': no newsgroups header", newsgroup, article.MessageID)
				continue
			}
			conn.Unlock()
			conn.ForceCloseConn()
			log.Printf("ERROR Newsgroup: '%s' | Failed to send TAKETHIS for %s: %v", newsgroup, article.MessageID, err)
			return 0, 0, redis_cached, fmt.Errorf("failed to send TAKETHIS for %s: %v", article.MessageID, err)
		}
		log.Printf("Newsgroup: '%s' | Sent TAKETHIS '%s' CmdID=%d", newsgroup, article.MessageID, cmdID)
		artChan <- &nntp.CheckResponse{
			Article: article,
			CmdId:   cmdID,
		}
	}
	conn.Unlock()
	close(artChan)
	//log.Printf("Sent %d TAKETHIS commands, reading responses...", len(commandIDs))
	var done []*string
	var countDone int
	// Phase 2: Read all responses in order
	log.Printf("Newsgroup: '%s' | Reading TAKETHIS responses for %d sent articles...", newsgroup, len(artChan))
	for cr := range artChan {
		log.Printf("Newsgroup: '%s' | Pre-Read TAKETHIS response for article '%s' CmdID=%d (i=%d/%d)", newsgroup, cr.Article.MessageID, cr.CmdId, countDone+1, len(articles))
		job.TTMode.IncrementTmp()
		takeThisResponseCode, err := conn.ReadTakeThisResponseStreaming(newsgroup, cr)
		if err != nil {
			job.Increment(nntp.IncrFLAG_CONN_ERRORS)
			conn.ForceCloseConn()
			log.Printf("ERROR Newsgroup: '%s' | Failed to read TAKETHIS response for %s: %v", newsgroup, cr.Article.MessageID, err)
			return transferred, rejected, redis_cached, fmt.Errorf("failed to read TAKETHIS response for %s: %v", cr.Article.MessageID, err)
		}
		log.Printf("Newsgroup: '%s' | GOT TAKETHIS response '%s': %d CmdID=%d (i=%d/%d)", newsgroup, cr.Article.MessageID, takeThisResponseCode, cr.CmdId, countDone+1, len(articles))
		countDone++
		job.NGTProgress.AddNGTP(0, 1, 0)
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
var TakeThisQueues []chan *nntp.CHTTJob

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
	NewsgroupWorkerMapMux.Lock()
	defer NewsgroupWorkerMapMux.Unlock()
	if workerID, exists := NewsgroupWorkerMap[newsgroup]; exists {
		return workerID
	}

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
	NewsgroupWorkerMap[newsgroup] = workerID

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
	TakeThisQueues = make([]chan *nntp.CHTTJob, nntp.NNTPTransferThreads)
	WorkerQueueLength = make([]int, nntp.NNTPTransferThreads)
	for i := range CheckQueues {
		CheckQueues[i] = make(chan *nntp.CHTTJob)       // no cap! only accepts if there is a reader!
		TakeThisQueues[i] = make(chan *nntp.CHTTJob, 2) // allows max 2 queued TT jobs
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
				slotID:     slotID,
				errChan:    errChan,
				redisCli:   redisCli,
				ExitChan:   make(chan *ReturnSignal, 1),
				jobsQueued: make(map[*nntp.CHTTJob]uint64, BatchCheck),
				jobsReadOK: make(map[*nntp.CHTTJob]uint64, BatchCheck),
				jobMap:     make(map[*string]*nntp.CHTTJob, BatchCheck),
				jobs:       make([]*nntp.CHTTJob, 0, BatchCheck),
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
									if len(job.Articles) == 0 {
										log.Printf("ERROR in CHTTWorker (%d) job %d has no articles, skipping requeue", i, job.JobID)
										job.Mux.Unlock()
										continue
									}
									rqj := &nntp.CHTTJob{
										JobID:       job.JobID,
										Newsgroup:   job.Newsgroup,
										Articles:    job.Articles,
										OffsetQ:     job.OffsetQ,
										NGTProgress: job.NGTProgress,
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
									job.OffsetQ = nil
									job.NGTProgress = nil
									job.Mux.Unlock()
								}
							}
							log.Printf("CHTTWorker (%d) did requeue %d jobs", i, len(rs.jobs))
						}

						// Clean up ReturnSignal maps and unlink pointers
						// Clean up jobMap - nil all pointers before deleting
						//log.Printf("CHTTWorker (%d) cleaning up jobMap with %d entries", i, len(rs.jobMap))
						for msgID := range rs.jobMap {
							rs.jobMap[msgID] = nil
							delete(rs.jobMap, msgID)
						}
						rs.jobMap = nil

						// Clean up jobsQueued
						//log.Printf("CHTTWorker (%d) cleaning up jobsQueued with %d entries", i, len(rs.jobsQueued))
						for job := range rs.jobsQueued {
							delete(rs.jobsQueued, job)
						}
						rs.jobsQueued = nil

						// Clean up jobsReadOK
						//log.Printf("CHTTWorker (%d) cleaning up jobsReadOK with %d entries", i, len(rs.jobsReadOK))
						for job := range rs.jobsReadOK {
							delete(rs.jobsReadOK, job)
						}
						rs.jobsReadOK = nil

						// Clean up jobs slice - nil all pointers
						//log.Printf("CHTTWorker (%d) cleaning up jobs slice with %d entries", i, len(rs.jobs))
						for idx := range rs.jobs {
							rs.jobs[idx] = nil
						}
						rs.jobs = nil
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
	Mux        sync.Mutex
	CHECK      bool
	RunTT      bool
	slotID     int
	ExitChan   chan *ReturnSignal
	errChan    chan struct{}
	redisCli   *redis.Client
	jobsQueued map[*nntp.CHTTJob]uint64
	jobsReadOK map[*nntp.CHTTJob]uint64
	jobMap     map[*string]*nntp.CHTTJob
	jobs       []*nntp.CHTTJob
}

func (rs *ReturnSignal) UnlockTT() {
	rs.Mux.Lock()
	rs.RunTT = false
	rs.Mux.Unlock()
	log.Printf("UnlockTT: released RunTT lock")
}

func (rs *ReturnSignal) GetLockTT() {
	for {
		rs.Mux.Lock()
		if rs.RunTT {
			log.Printf("GetLockTT: RunTT already true")
			rs.Mux.Unlock()
			return
		}
		if !rs.RunTT && !rs.CHECK {
			rs.RunTT = true
			rs.Mux.Unlock()
			log.Printf("GetLockTT: acquired RunTT lock")
			return
		}
		rs.Mux.Unlock()
		log.Printf("GetLockTT: waiting for RunTT to be true...")
		time.Sleep(nntp.ReturnDelay)
	}
}

func (rs *ReturnSignal) UnlockCHECKforTTwithWait() {
	for {
		rs.Mux.Lock()
		if !rs.RunTT {
			rs.CHECK = false
			rs.RunTT = true
			rs.Mux.Unlock()
			log.Printf("UnlockCHECKforTTwithWait: switched CHECK to RunTT")
			return
		}
		rs.Mux.Unlock()
		log.Printf("UnlockCHECKforTTwithWait: waiting for RunTT to be false...")
		time.Sleep(nntp.ReturnDelay)
	}
}

func (rs *ReturnSignal) UnlockCHECKforTT() {
	rs.Mux.Lock()
	defer rs.Mux.Unlock()
	if !rs.CHECK || rs.RunTT {
		log.Printf("UnlockCHECKforTT: cannot switch to RunTT, CHECK=%t RunTT=%t", rs.CHECK, rs.RunTT)
		return
	}
	log.Printf("UnlockCHECKforTT: switched CHECK to RunTT")
	rs.CHECK = false
	rs.RunTT = true
}

func (rs *ReturnSignal) BlockCHECK() {
	rs.Mux.Lock()
	rs.CHECK = false
	rs.RunTT = true
	log.Printf("BlockCHECK: set CHECK to false (RunTT=%t)", rs.RunTT)
	rs.Mux.Unlock()
}

func (rs *ReturnSignal) LockCHECK() {
	start := time.Now()
	printLast := start
	for {
		rs.Mux.Lock()
		if !rs.RunTT {
			rs.CHECK = true
			log.Printf("LockCHECK: acquired CHECK lock (RunTT=%t) waited %v", rs.RunTT, time.Since(start))
			rs.Mux.Unlock()
			return
		}
		if time.Since(printLast) > time.Second {
			log.Printf("LockCHECK: waiting for RunTT to be false... CHECK=%t RunTT=%t", rs.CHECK, rs.RunTT)
			printLast = time.Now()
		}
		rs.Mux.Unlock()
		time.Sleep(nntp.ReturnDelay)
	}
}

func replyChan(request chan struct{}, reply chan struct{}) {
	select {
	case <-request:
		// got a reply request
		select {
		case reply <- struct{}{}: // send back
		default:
			// pass, is full
		}
	default:
		// pass, no request
	}
}

func CHTTWorker(workerID int, conn *nntp.BackendConn, rs *ReturnSignal, checkQueue chan *nntp.CHTTJob) {
	readResponsesChan := make(chan *nntp.ReadRequest, BatchCheck*2)
	//rrRetChan := make(chan struct{}, BatchCheck)
	//takeThisChan := make(chan *nntp.CHTTJob, 2) // buffer 2
	errChan := make(chan struct{}, 4)
	tickChan := make(chan struct{}, 1)
	//flipflopChan := make(chan struct{}, 1)
	requestReplyJobDone := make(chan struct{}, 1)
	replyJobDone := make(chan struct{}, 1)

	defer func(conn *nntp.BackendConn, rs *ReturnSignal) {
		conn.ForceCloseConn()
		rs.ExitChan <- rs
		errChan <- struct{}{}
	}(conn, rs)
	//lastRun := time.Now()

	// launch go routine which sends CHECK commands
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
				log.Printf("CheckWorker (%d): Send CHECK got errChan signal... exiting", workerID)
				return

			case <-tickChan:
				if common.WantShutdown() {
					log.Printf("CheckWorker (%d): Tick WantShutdown, exiting", workerID)
					return
				}

				// Get the next job to process
				rs.Mux.Lock()
				if len(rs.jobs) == 0 {
					rs.Mux.Unlock()
					log.Printf("CheckWorker (%d): Ticked but no jobs in queue, continue...", workerID)
					continue loop
				}
				log.Printf("CheckWorker (%d): Ticked and found %d jobs in queue", workerID, len(rs.jobs))
				currentJob := rs.jobs[0]
				rs.jobs = rs.jobs[1:] // Remove first job from queue
				rs.Mux.Unlock()
				if currentJob == nil {
					continue loop
				}
				workerID := assignWorkerToNewsgroup(*currentJob.Newsgroup)
			waiting:
				for {
					if len(TakeThisQueues[workerID]) > 1 {
						log.Printf("CheckWorker (%d): waiting shared takeThisChan full (%d)", workerID, len(TakeThisQueues[workerID]))
						time.Sleep(time.Millisecond * 16)
						continue waiting
					}
					break
				}
				currentJob.OffsetQ.Done()
				if currentJob.TTMode.UseCHECK() {
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d waits to check %d message IDs in batches of %d", *currentJob.Newsgroup, workerID, currentJob.JobID, len(currentJob.MessageIDs), BatchCheck)

					// Process checkIds in batches of BatchCheck
					for batchStart := 0; batchStart < len(currentJob.MessageIDs); batchStart += BatchCheck {
						batchEnd := batchStart + BatchCheck
						if batchEnd > len(currentJob.MessageIDs) {
							batchEnd = len(currentJob.MessageIDs)
						}
						// Lock for this batch only

						//common.ChanLock(flipflopChan)
						if !conn.IsConnected() {
							rs.Mux.Lock()
							rs.jobs = append([]*nntp.CHTTJob{currentJob}, rs.jobs...) // requeue at front
							rs.Mux.Unlock()
							//common.ChanRelease(flipflopChan)
							log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d connection lost before SendCheckMultiple for batch (offset %d: %d-%d)", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd)
							time.Sleep(time.Second)
							return
						}
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d acquire LOCK CHECK for batch (offset %d: %d-%d) (%d messages)", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd, len(currentJob.MessageIDs[batchStart:batchEnd]))
						rs.LockCHECK()
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d acquired CHECK lock for batch (offset %d: %d-%d) -> SendCheckMultiple", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd)
						err := conn.SendCheckMultiple(currentJob.MessageIDs[batchStart:batchEnd], readResponsesChan, currentJob)
						if err != nil {
							log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d SendCheckMultiple error for batch (offset %d: %d-%d): %v", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd, err)
							time.Sleep(time.Second)
							rs.Mux.Lock()
							rs.jobs = append([]*nntp.CHTTJob{currentJob}, rs.jobs...) // requeue at front
							rs.Mux.Unlock()
							rs.BlockCHECK()
							//common.ChanRelease(flipflopChan)
							return
						}
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d Sent CHECK for batch (offset %d: %d-%d), responses will be read asynchronously...", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd)
						// NOTE: CHECK lock remains locked! It will be unlocked by the response reader
						// when all responses are processed (see rs.UnlockCHECKforTT() in response reader)
						/* disabled
						deadline := time.After(time.Minute)
						timedOut := false
						replies := 0

						for i, msgID := range currentJob.MessageIDs[batchStart:batchEnd] {
							if msgID != nil {
								//log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d waiting for rrRetChan (offset %d: %d-%d) replies=%d readResponsesChan=%d n=%d/%d", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd, replies, len(readResponsesChan), i+1, len(currentJob.MessageIDs[batchStart:batchEnd]))
								select {
								case <-deadline:
									log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d timeout waiting for rrRetChan for batch (offset %d: %d-%d) replies=%d readResponsesChan=%d n=%d/%d", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd, replies, len(readResponsesChan), i+1, len(currentJob.MessageIDs[batchStart:batchEnd]))
									timedOut = true
								case <-rrRetChan:
									// got reply
									replies++
								}
								if timedOut {
									break
								}
							}
						}
						// If timeout occurred, clean up the job
						if timedOut {
							rs.Mux.Lock()
							// requeue to front
							rs.jobs = append([]*nntp.CHTTJob{currentJob}, rs.jobs...)
							// Send failure response
							currentJob.Response(false, fmt.Errorf("CHECK response timeout"))

							// Release lock
							common.ChanRelease(flipflopChan)
							return
						}
						*/
						// Release lock after batch is complete, allowing TAKETHIS to run
						//common.ChanRelease(flipflopChan)
					}
				} else {
					log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d skipping CHECK for %d message IDs (TAKETHIS mode)", *currentJob.Newsgroup, workerID, currentJob.JobID, len(currentJob.MessageIDs))
					currentJob.WantedIDs = currentJob.MessageIDs
					//rs.UnlockCHECKforTTwithWait()
					rs.BlockCHECK()
					TakeThisQueues[workerID] <- currentJob // local takethis chan sharing the same connection
					log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d sent to local TakeThisChan", *currentJob.Newsgroup, workerID, currentJob.JobID)
				}
				//lastRun = time.Now()
				// Check if there are more jobs to process
				//log.Printf("CheckWorker (%d): job #%d CHECK done, checking for more jobs...", workerID, currentJob.JobID)
				rs.Mux.Lock()
				hasMoreJobs := len(rs.jobs) > 0
				rs.Mux.Unlock()
				replyChan(requestReplyJobDone, replyJobDone) // see if anybody is waiting and reply
				//log.Printf("CheckWorker (%d): job #%d CHECK done, hasMoreJobs=%v", workerID, currentJob.JobID, hasMoreJobs)
				// Decrement queue length for this worker (job processing complete)
				WorkerQueueLengthMux.Lock()
				if workerID < len(WorkerQueueLength) && WorkerQueueLength[workerID] > 0 {
					WorkerQueueLength[workerID]--
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
				log.Printf("CheckWorker (%d): job #%d CHECKs sent, loop to next job", workerID, currentJob.JobID)

			case <-ticker.C:
				if common.WantShutdown() {
					log.Printf("CheckWorker (%d): Ticker WantShutdown, exiting", workerID)
					return
				}
				rs.Mux.Lock()
				hasWork := len(rs.jobs) > 0
				rs.Mux.Unlock()
				if hasWork {
					select {
					case tickChan <- struct{}{}:
						log.Printf("CheckWorker (%d): Ticker ticked, sent tickChan signal", workerID)
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
		var tookTime time.Duration
		defer func() {
			errChan <- struct{}{}
		}()
	loop:
		for {
			select {
			case <-errChan:
				log.Printf("CheckWorker (%d): Read CHECK responses got errChan signal...", workerID)
				errChan <- struct{}{}
				log.Printf("CheckWorker (%d): Read CHECK responses exiting", workerID)
				return

			case rr := <-readResponsesChan:
				//log.Printf("CheckWorker (%d): Read CHECK got readRequest for rr: '%v'", workerID, rr)
				if rr == nil || rr.MsgID == nil {
					log.Printf("CheckWorker (%d): Read CHECK got nil readRequest, skipping", workerID)
					continue loop
				}
				if common.WantShutdown() {
					log.Printf("CheckWorker (%d): Read CHECK WantShutdown, exiting", workerID)
					//rr.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					return
				}
				//log.Printf("CheckWorker (%d): Read CHECK response (do conn check) for msgID: %s (cmdId:%d MID=%d/%d)", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs)
				if !conn.IsConnected() {
					log.Printf("CheckWorker (%d): Read CHECK connection lost, exiting", workerID)
					//rr.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					return
				}
				log.Printf("CheckWorker (%d): Pre-Read CHECK response for msgID: %s (cmdId:%d MID=%d/%d)", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs)
				start := time.Now()
				/* disabled
				if err := conn.SetReadDeadline(time.Now().Add(1 * time.Minute)); err != nil {
					log.Printf("Failed to set read deadline: %v", err)
					nntp.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					return
				}
				*/
				conn.TextConn.StartResponse(rr.CmdID)
				log.Printf("CheckWorker (%d): Reading CHECK response for msgID: %s (cmdId:%d MID=%d/%d)", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs)
				code, line, err := conn.TextConn.ReadCodeLine(238)
				conn.TextConn.EndResponse(rr.CmdID)
				if code == 0 && err != nil {
					log.Printf("Failed to read CHECK response: %v", err)
					//rr.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					return
				}
				/* disabled
				if err := conn.SetReadDeadline(time.Time{}); err != nil {
					log.Printf("Failed to set unset read deadline: %v", err)
					nntp.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					return
				}
				*/
				took := time.Since(start)
				tookTime += took
				responseCount++
				rr.Job.Increment(nntp.IncrFLAG_CHECKED)
				if rr.N == 1 {
					log.Printf("CheckWorker (%d): time to first response for msgID: %s (cmdId:%d MID=%d/%d) took: %v ms", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, time.Since(start).Milliseconds())
					tookTime = 0
				} else if responseCount >= 100 {
					avg := time.Duration(float64(tookTime) / float64(responseCount))
					if avg > 1 {
						log.Printf("CheckWorker (%d): Read %d CHECK responses, avg latency: %v, last: %v (cmdId:%d MID=%d/%d)", workerID, responseCount, avg, took, rr.CmdID, rr.N, rr.Reqs)
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
					log.Printf("ERROR in CheckWorker: Malformed CHECK response code=%d line: '%s' (cmdId:%d MID=%d/%d)", code, line, rr.CmdID, rr.N, rr.Reqs)
					//rr.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					return
				}
				if parts[0] != *rr.MsgID {
					log.Printf("ERROR in CheckWorker: Mismatched CHECK response: expected '%s', got '%s' code=%d (cmdId:%d MID=%d/%d)", *rr.MsgID, parts[0], code, rr.CmdID, rr.N, rr.Reqs)
					//rr.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					return
				}
				log.Printf("CheckWorker (%d): Got CHECK response: '%s' (cmdId:%d MID=%d/%d) took: %v ms", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, time.Since(start).Milliseconds())

				rs.Mux.Lock()
				job, exists := rs.jobMap[rr.MsgID]
				rs.Mux.Unlock()
				job.NGTProgress.AddNGTP(1, 0, 0)
				//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG1 Processing CHECK response for msgID: %s (cmdId:%d MID=%d/%d) code=%d", *job.Newsgroup, workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, code)
				if !exists {
					log.Printf("Newsgroup: '%s' | ERROR in CheckWorker: ReadCheckResponse msgId '%s' did not exist in jobMap.", *job.Newsgroup, *rr.MsgID)
					//rr.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					continue loop
				}
				//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG2 Processing CHECK response for msgID: %s (cmdId:%d MID=%d/%d) code=%d", *job.Newsgroup, workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, code)
				rs.Mux.Lock()
				rs.jobMap[rr.MsgID] = nil // Nil the pointer before deleting
				delete(rs.jobMap, rr.MsgID)
				rs.jobsReadOK[job]++
				rs.Mux.Unlock()
				switch code {
				case 238:
					//log.Printf("Newsgroup: '%s' | Got Response: Wanted Article '%s': code=%d", *job.Newsgroup, *rr.MsgID, code)
					job.AppendWantedMessageID(rr.MsgID)

				case 438:
					//log.Printf("Newsgroup: '%s' | Got Response: Unwanted Article '%s': code=%d", *job.Newsgroup, *rr.MsgID, code)
					job.Increment(nntp.IncrFLAG_UNWANTED)

				case 431:
					//log.Printf("Newsgroup: '%s' | Got Response: Retry Article '%s': code=%d", *job.Newsgroup, *rr.MsgID, code)
					job.Increment(nntp.IncrFLAG_RETRY)

				default:
					log.Printf("Newsgroup: '%s' | Unknown CHECK response: line='%s' code=%d expected msgID %s", *job.Newsgroup, line, code, *rr.MsgID)
				}
				// check if all jobs are done
				//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG3 Processing CHECK response for msgID: %s (cmdId:%d MID=%d/%d) code=%d", *job.Newsgroup, workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, code)
				rs.Mux.Lock()
				queuedCount, qexists := rs.jobsQueued[job]
				readCount, rexists := rs.jobsReadOK[job]
				rs.Mux.Unlock()
				if !qexists || !rexists {
					log.Printf("Newsgroup: '%s' | ERROR in CheckWorker: queuedCount or readCount did not exist for a job?!", *job.Newsgroup)
					//rr.ReturnReadRequest(rrRetChan)
					rr.ClearReadRequest()
					continue loop
				}
				//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG4 Processing CHECK response for msgID: %s (cmdId:%d MID=%d/%d) code=%d", *job.Newsgroup, workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, code)
				//rr.ReturnReadRequest(rrRetChan)
				rr.ClearReadRequest()
				if queuedCount == readCount {
					rs.Mux.Lock()
					delete(rs.jobsQueued, job)
					delete(rs.jobsReadOK, job)
					rs.Mux.Unlock()
					if len(job.WantedIDs) > 0 {
						// Pass job to TAKETHIS worker via channel
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG5 job #%d got all %d CHECK responses, passing to TAKETHIS worker (wanted: %d articles) takeThisChan=%d", *job.Newsgroup, workerID, job.JobID, queuedCount, len(job.WantedIDs), len(TakeThisQueues[workerID]))
						rs.UnlockCHECKforTT()
						/* disabled
						if len(readResponsesChan) == 0 {
							rs.UnlockCHECKforTT() // Unlock CHECK, lock for TAKETHIS
						} else {
							rs.BlockCHECK()
							log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG5a WARNING: readResponsesChan not empty (%d), delaying TAKETHIS unlock", *job.Newsgroup, workerID, len(readResponsesChan))
							go func() {
								for {
									time.Sleep(time.Millisecond)
									if len(readResponsesChan) == 0 {
										rs.UnlockCHECKforTT()
										return
									}
								}
							}()
						}*/
						TakeThisQueues[workerID] <- job // local takethis chan sharing the same connection
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG5c Sent job #%d to TAKETHIS worker (wanted: %d/%d) takeThisChan=%d", *job.Newsgroup, workerID, job.JobID, len(job.WantedIDs), queuedCount, len(TakeThisQueues[workerID]))

					} else {
						log.Printf("Newsgroup: '%s' | CheckWorker (%d):  DEBUG6 job #%d got %d CHECK responses but server wants none", *job.Newsgroup, workerID, job.JobID, queuedCount)
						// Send response and close channel for jobs with no wanted articles
						job.Response(true, nil)
						if len(TakeThisQueues[workerID]) > 0 {
							rs.UnlockCHECKforTT()
						} else {
							rs.UnlockTT()
						}
					}
				} else {
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG6 job #%d CHECK responses so far: %d/%d readResponsesChan=%d", *job.Newsgroup, workerID, job.JobID, readCount, queuedCount, len(readResponsesChan))
				}
				continue loop
			} // end select
		} // end forever
	}()

	// launch a goroutine to process TAKETHIS jobs from local channel sharing the same connection
	go func() {
		defer func() {
			errChan <- struct{}{}
		}()
		var job *nntp.CHTTJob
		for {
			if common.WantShutdown() {
				log.Printf("TTworker (%d): WantShutdown, exiting", workerID)
				return
			}

			select {
			case ajob := <-TakeThisQueues[workerID]:
				job = ajob

			case <-errChan:
				log.Printf("TTworker (%d): got errChan signal, exiting", workerID)
				errChan <- struct{}{}
				return
			}
			if job == nil {
				log.Printf("TTworker (%d): Received nil job, channels may be closing", workerID)
				continue
			}
			if len(job.WantedIDs) == 0 {
				log.Printf("Newsgroup: '%s' | TTworker (%d): job #%d has no wanted articles, skipping TAKETHIS", *job.Newsgroup, workerID, job.JobID)
				job.Response(true, nil)
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
				log.Printf("Newsgroup: '%s' | TTworker (%d): No valid wanted articles found in ArticleMap for job #%d", *job.Newsgroup, workerID, job.JobID)
				job.Response(true, nil)
				continue
			}

			log.Printf("Newsgroup: '%s' | TTworker (%d): Prepare locking to send TAKETHIS for job #%d with %d wanted articles", *job.Newsgroup, workerID, job.JobID, len(wantedArticles))
			rs.GetLockTT()
			//common.ChanLock(flipflopChan)
			log.Printf("Newsgroup: '%s' | TTworker (%d): Sending TAKETHIS for job #%d with %d wanted articles", *job.Newsgroup, workerID, job.JobID, len(wantedArticles))
			// Send TAKETHIS commands using existing function
			transferred, rejected, redis_cached, err := sendArticlesBatchViaTakeThis(conn, wantedArticles, job, *job.Newsgroup, rs.redisCli)
			//common.ChanRelease(flipflopChan)
			rs.UnlockTT()
			if err != nil {
				log.Printf("Newsgroup: '%s' | TTworker (%d): Error in TAKETHIS job #%d: %v", *job.Newsgroup, workerID, job.JobID, err)
				job.Response(false, err)
				rs.Mux.Lock()
				// requeue at front
				rs.jobs = append([]*nntp.CHTTJob{job}, rs.jobs...)
				rs.Mux.Unlock()
				return
			}

			log.Printf("Newsgroup: '%s' | TTworker (%d): TAKETHIS job #%d completed: transferred=%d, rejected=%d, redis_cached=%d", *job.Newsgroup, workerID, job.JobID, transferred, rejected, redis_cached)

			// Send response back
			log.Printf("Newsgroup: '%s' | TTworker (%d): Sending TTresponse for job #%d to responseChan len=%d", *job.Newsgroup, workerID, job.JobID, len(job.ResponseChan))
			job.Response(true, nil)
			log.Printf("Newsgroup: '%s' | TTworker (%d): Sent TTresponse for job #%d to responseChan", *job.Newsgroup, workerID, job.JobID)

		}
	}()

	for {
		select {
		case <-errChan:
			errChan <- struct{}{}
			return
		case job := <-checkQueue:
			if common.WantShutdown() {
				log.Printf("CHTTworker: WantShutdown, exiting")
				return
			}
			if job == nil || len(job.MessageIDs) == 0 {
				log.Printf("CHTTworker: empty job, skipping")
				if job != nil {
					job.Response(true, fmt.Errorf("got job without valid wanted articles"))
				}
				continue
			}

			// Build jobMap for tracking which message IDs belong to this job
			// and count queued messages
			rs.Mux.Lock()
			queueFull := len(rs.jobs) > 1 || len(TakeThisQueues[workerID]) > 1
			if queueFull {
				log.Printf("Newsgroup: '%s' | CHTTworker (%d): got job #%d with %d message IDs. queued=%d ... waiting...", *job.Newsgroup, workerID, job.JobID, len(job.MessageIDs), len(rs.jobs))
				select {
				case requestReplyJobDone <- struct{}{}:
				default:
					log.Printf("ERROR Newsgroup: '%s' | CHTTworker (%d): job #%d could not signal requestReplyJobDone, channel full", *job.Newsgroup, workerID, job.JobID)
					// pass
				}
			}
			rs.Mux.Unlock()
			if queueFull {
				start := time.Now()
				wait := start
			waitForReply:
				for {
					select {
					case <-replyJobDone:
						// pass
					case <-time.After(time.Millisecond * 16):
						rs.Mux.Lock()
						queueFull = len(rs.jobs) > 1 || len(TakeThisQueues[workerID]) > 1
						rs.Mux.Unlock()
						if !queueFull {
							break waitForReply
						}
						// log every 5s
						if time.Since(wait) > time.Second {
							log.Printf("Newsgroup: '%s' | CHTTworker (%d): pre append job #%d waiting since %v rs.jobs=%d takeThisChan=%d", *job.Newsgroup, workerID, job.JobID, time.Since(start), len(rs.jobs), len(TakeThisQueues[workerID]))
							wait = time.Now()
						}
					}
				}
				log.Printf("Newsgroup: '%s' | CHTTworker (%d): waited %v for previous jobs to clear before queuing job #%d", *job.Newsgroup, workerID, time.Since(start), job.JobID)
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
			// Signal ticker to process this job
			select {
			case tickChan <- struct{}{}:
				log.Printf("Newsgroup: '%s' | CHTTworker (%d): signal ticker start job #%d with %d message IDs. queued=%d", *job.Newsgroup, workerID, job.JobID, len(job.MessageIDs), len(rs.jobs))
			default:
				// tickChan full, will be processed on next tick
			}
			rs.Mux.Unlock()
		} // end select
	} // end for
} // end func CheckWorker

// monitorMemoryStats logs memory statistics periodically
func monitorMemoryStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var m runtime.MemStats
	startTime := time.Now()

	for {
		<-ticker.C
		runtime.ReadMemStats(&m)

		// Convert bytes to MB for readability
		allocMB := float64(m.Alloc) / 1024 / 1024
		totalAllocMB := float64(m.TotalAlloc) / 1024 / 1024
		sysMB := float64(m.Sys) / 1024 / 1024
		heapAllocMB := float64(m.HeapAlloc) / 1024 / 1024
		heapSysMB := float64(m.HeapSys) / 1024 / 1024
		heapIdleMB := float64(m.HeapIdle) / 1024 / 1024
		heapInuseMB := float64(m.HeapInuse) / 1024 / 1024

		log.Printf("=== MEMORY STATS (uptime: %v) ===", time.Since(startTime).Round(time.Second))
		log.Printf("  Alloc       = %.2f MB (currently allocated)", allocMB)
		log.Printf("  TotalAlloc  = %.2f MB (cumulative allocated)", totalAllocMB)
		log.Printf("  Sys         = %.2f MB (obtained from system)", sysMB)
		log.Printf("  HeapAlloc   = %.2f MB (heap allocated)", heapAllocMB)
		log.Printf("  HeapSys     = %.2f MB (heap from system)", heapSysMB)
		log.Printf("  HeapIdle    = %.2f MB (heap idle)", heapIdleMB)
		log.Printf("  HeapInuse   = %.2f MB (heap in use)", heapInuseMB)
		log.Printf("  NumGC       = %d (garbage collections)", m.NumGC)
		log.Printf("  Goroutines  = %d", runtime.NumGoroutine())
		log.Printf("  GCCPUFract  = %.4f%% (GC CPU fraction)", m.GCCPUFraction*100)

		// Warning if memory usage is high
		if allocMB > 1000 {
			log.Printf("  ⚠️  WARNING: High memory usage (%.2f MB)! Consider lowering -batch-check or -batch-db", allocMB)
		}
		if runtime.NumGoroutine() > 1000 {
			log.Printf("  ⚠️  WARNING: High goroutine count (%d)! Possible goroutine leak", runtime.NumGoroutine())
		}
	}
}

// startWebServer starts a simple HTTP server to display transfer results
func startWebServer(port int) {
	http.HandleFunc("/", handleIndex)
	addr := fmt.Sprintf(":%d", port)
	log.Printf("Starting web server on http://ANY_ADDR:%s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Printf("Web server error: %v", err)
	}
}

// handleIndex serves the main page with transfer results
func handleIndex(w http.ResponseWriter, r *http.Request) {
	resultsMutex.RLock()
	defer resultsMutex.RUnlock()

	// HTML template for displaying results
	const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>NNTP Transfer Results</title>
	<style>
		body {
			font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
			max-width: 1400px;
			margin: 0 auto;
			padding: 20px;
			background: #f5f5f5;
		}
		h1 {
			color: #333;
			border-bottom: 3px solid #4CAF50;
			padding-bottom: 10px;
		}
		.stats {
			background: white;
			padding: 15px;
			border-radius: 5px;
			margin: 20px 0;
			box-shadow: 0 2px 4px rgba(0,0,0,0.1);
		}
		.result {
			background: white;
			padding: 12px;
			margin: 10px 0;
			border-radius: 4px;
			border-left: 4px solid #4CAF50;
			font-family: 'Consolas', 'Monaco', monospace;
			font-size: 13px;
			box-shadow: 0 1px 3px rgba(0,0,0,0.1);
		}
		.result:hover {
			background: #f9f9f9;
		}
		.timestamp {
			color: #666;
			font-size: 11px;
			margin-top: 10px;
		}
		.empty {
			color: #999;
			text-align: center;
			padding: 40px;
			font-style: italic;
		}
		.refresh-btn {
			background: #4CAF50;
			color: white;
			border: none;
			padding: 10px 20px;
			border-radius: 4px;
			cursor: pointer;
			font-size: 14px;
		}
		.refresh-btn:hover {
			background: #45a049;
		}
		.progress-table {
			width: 100%;
			background: white;
			border-collapse: collapse;
			margin: 20px 0;
			box-shadow: 0 2px 4px rgba(0,0,0,0.1);
			border-radius: 5px;
			overflow: hidden;
		}
		.progress-table th {
			background: #4CAF50;
			color: white;
			padding: 12px;
			text-align: left;
			font-weight: 600;
		}
		.progress-table td {
			padding: 10px 12px;
			border-bottom: 1px solid #f0f0f0;
		}
		.progress-table tr:hover {
			background: #f9f9f9;
		}
		.status-badge {
			padding: 4px 8px;
			border-radius: 3px;
			font-size: 11px;
			font-weight: 600;
		}
		.status-finished {
			background: #d4edda;
			color: #155724;
		}
		.status-progress {
			background: #fff3cd;
			color: #856404;
		}
		.progress-bar {
			height: 20px;
			background: #e0e0e0;
			border-radius: 10px;
			overflow: hidden;
		}
		.progress-fill {
			height: 100%;
			background: linear-gradient(90deg, #4CAF50, #45a049);
			transition: width 0.3s;
		}
	</style>
	<script>
		function autoRefresh() {
			setTimeout(function() {
				location.reload();
			}, 3000);
		}
	</script>
</head>
<body onload="autoRefresh()">
	<h1>🚀 NNTP Transfer Results</h1>

	<div class="stats">
		{{if eq .Started 0}}
			<strong>Status:</strong> Waiting for transfers to start...<br>
		{{else}}
			<strong>Transfer Progress:</strong> {{.Finished}}/{{.Started}} newsgroups completed
			{{if eq .Finished .Started}}
				✅ All complete!
			{{else}}
				({{subtract .Started .Finished}} in progress)
			{{end}}
			<br>
		{{end}}
		<button class="refresh-btn" onclick="location.reload()">🔄 Refresh Now</button>
		<span style="margin-left: 10px; color: #666; font-size: 12px;">(Auto-refresh every 3 seconds)</span>
	</div>

	{{if .Progress}}
	<h2 style="margin-top: 30px; color: #333;">⏳ In Progress</h2>
	<table class="progress-table">
		<thead>
			<tr>
				<th>Newsgroup</th>
				<th>Progress</th>
				<th>Speed</th>
				<th>CH/s</th>
				<th>TT/s</th>
				<th>Active</th>
				<th>Started</th>
				<th>Duration</th>
			</tr>
		</thead>
		<tbody>
			{{range .Progress}}
			<tr>
				<td><strong>{{.Name}}</strong></td>
				<td>
					{{if gt .TotalArticles 0}}
						<div style="display: flex; align-items: center; gap: 10px;">
							<div class="progress-bar" style="flex: 1;">
								<div class="progress-fill" style="width: {{if gt .OffsetStart 0}}{{multiply (divide .OffsetStart .TotalArticles) 100}}{{else}}0{{end}}%"></div>
							</div>
							<span style="font-size: 11px; color: #666;">{{.OffsetStart}}/{{.TotalArticles}}</span>
						</div>
					{{else}}
						<em>Initializing...</em>
					{{end}}
				</td>
				<td style="font-size: 12px;">{{.SpeedKB}} KByte/s</td>
				<td style="font-size: 12px;">{{.LastArtPerfC}}/s</td>
				<td style="font-size: 12px;">{{.LastArtPerfT}}/s</td>
				<td style="font-size: 12px;">{{.TimeSince}} ago</td>
				<td style="font-size: 12px;">{{.Started}}</td>
				<td style="font-size: 12px;">{{.Duration}}</td>

			</tr>
			{{end}}
		</tbody>
	</table>
	{{end}}

	{{if .Results}}
		<h2 style="margin-top: 30px; color: #333;">Completed Results</h2>
		{{range .Results}}
		<div class="result">{{.}}</div>
		{{end}}
	{{else}}
		<div class="empty">No transfer results yet. Waiting for transfers to complete...</div>
	{{end}}

	<div class="timestamp">Last updated: {{.Timestamp}}</div>
</body>
</html>`

	tmpl, err := template.New("index").Funcs(template.FuncMap{
		"subtract": func(a, b int) int { return a - b },
		"eq":       func(a, b int) bool { return a == b },
		"gt":       func(a, b int64) bool { return a > b },
		"divide": func(a, b int64) float64 {
			if b == 0 {
				return 0
			}
			return float64(a) / float64(b)
		},
		"multiply": func(a float64, b int) int { return int(a * float64(b)) },
	}).Parse(htmlTemplate)
	if err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}

	// Calculate started and finished counts and collect progress details
	type ProgressInfo struct {
		Name          string
		OffsetStart   int64
		BatchStart    int64
		BatchEnd      int64
		TotalArticles int64
		Started       string
		LastUpdated   string
		Finished      bool
		SpeedKB       int64
		Duration      string
		TimeSince     string
		LastArtPerfC  int64
		LastArtPerfT  int64
	}

	started := len(nntp.NewsgroupTransferProgressMap)
	finished := 0
	var progressList []ProgressInfo

	for name, progress := range nntp.NewsgroupTransferProgressMap {
		progress.CalcSpeed()
		progress.Mux.RLock()
		if progress.Finished {
			finished++
			progress.Mux.RUnlock()
			continue // Skip finished newsgroups - they're already in results
		}

		duration := time.Since(progress.Started).Round(time.Second).String()

		progressList = append(progressList, ProgressInfo{
			Name:          name,
			OffsetStart:   progress.OffsetStart + progress.BatchStart,
			BatchStart:    progress.BatchStart,
			BatchEnd:      progress.BatchEnd,
			TotalArticles: progress.TotalArticles,
			Started:       progress.Started.Format("15:04:05"),
			LastUpdated:   progress.LastUpdated.Format("15:04:05"),
			TimeSince:     time.Since(progress.LastUpdated).Round(time.Second).String(),
			SpeedKB:       progress.LastSpeedKB,
			LastArtPerfC:  progress.LastArtPerfC,
			LastArtPerfT:  progress.LastArtPerfT,
			Finished:      false,
			Duration:      duration,
		})
		progress.Mux.RUnlock()
	}

	// Sort progress list by newsgroup name for consistent display
	sort.Slice(progressList, func(i, j int) bool {
		return progressList[i].Name < progressList[j].Name
	})

	data := struct {
		Results   []string
		Started   int
		Finished  int
		Progress  []ProgressInfo
		Timestamp string
	}{
		Results:   results,
		Started:   started,
		Finished:  finished,
		Progress:  progressList,
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Template execution error: %v", err)
	}
}
