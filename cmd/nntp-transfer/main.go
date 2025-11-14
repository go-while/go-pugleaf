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
var MaxQueuedJobs int = 8
var BatchCheck int64
var CHECK_FIRST bool

// statistics
var TotalNewsgroups int64
var NewsgroupsToProcess int64
var ServerHostName string
var StartDate string
var EndDate string
var GlobalSpeed uint64

var totalTransferred, totalTTSentCount, totalCheckSentCount, totalChecked, totalWanted, totalUnwanted, totalRejected, totalRetry, totalSkipped, totalRedisCacheHits, totalRedisCacheBeforeCheck, totalRedisCacheBeforeTakethis, totalTXErrors, totalConnErrors, globalTotalArticles, nothingInDateRange uint64

func CalcGlobalSpeed() {
	for {
		time.Sleep(time.Second * 3)
		var speed uint64
		nntp.ResultsMutex.Lock()
		for _, progress := range nntp.NewsgroupTransferProgressMap {
			progress.CalcSpeed()
			speed += progress.GetSpeed()
		}
		GlobalSpeed = speed
		nntp.ResultsMutex.Unlock()
	}
}

func main() {
	config.AppVersion = appVersion

	bootTime := time.Now()
	common.VERBOSE_HEADERS = false
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
		batchCheck      = flag.Int64("batch-check", 100, "Number of message IDs/articles to send in streamed CHECK/TAKETHIS")
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
		checkFirst      = flag.Bool("check-first", true, "Use CHECK command before TAKETHIS to avoid duplicates (recommended)")

		// Newsgroup filtering options
		fileInclude      = flag.String("file-include", "", "File containing newsgroup patterns to include (one per line)")
		fileExclude      = flag.String("file-exclude", "", "File containing newsgroup patterns to exclude (one per line)")
		forceIncludeOnly = flag.Bool("force-include-only", false, "When set, only transfer newsgroups that match patterns in include file (ignores -group pattern)")
		excludePrefix    = flag.String("exclude-prefix", "", "Exclude newsgroups with this prefix (comma-separated list, supports wildcards like 'alt.binaries.*')")
		useProgressDB    = flag.Bool("use-progress-db", true, "Use transfer progress database to track transferred newsgroups and stats")

		// Web server and profiling options
		webPort   = flag.Int("web-port", 0, "Enable web server on this port to view results (e.g. 8080, default: disabled)")
		pprofPort = flag.Int("pprof-port", 0, "Enable pprof profiling server on this port (e.g., 6060). Access at http://localhost:PORT/debug/pprof/")
		memStats  = flag.Bool("mem-stats", false, "Log memory statistics every 30 seconds")
		gcPercent = flag.Int("gc-percent", 50, "Set GOGC percentage (default 100). Lower values = more frequent GC, less memory")
	)
	flag.Parse()
	common.IgnoreGoogleHeaders = *ignoreGoogleHeaders
	CHECK_FIRST = *checkFirst
	ServerHostName = *host
	StartDate = *startDate
	EndDate = *endDate

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
	if *batchCheck < 1 || *batchCheck > 100000 {
		log.Fatalf("Error: batch-check must be between 1 and 100000 (got %d)", *batchCheck)
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

	db.WG.Add(2) // Adds to wait group for db_batch.go cron jobs
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
	var progressDB *nntp.TransferProgressDB = nil
	if *useProgressDB {
		// Open transfer progress database
		aprogressDB, err := nntp.OpenTransferProgressDB(*dataDir, *host+":"+strconv.Itoa(*port))
		if err != nil {
			log.Fatalf("Failed to open transfer progress database: %v", err)
		}
		progressDB = aprogressDB
		defer progressDB.Close()
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
	nntphostname, err := db.GetConfigValue(config.CFG_KEY_HOSTNAME)
	if err != nil || nntphostname == "" {
		log.Fatalf("Failed to get local_nntp_hostname from database: %v", err)
	}

	pool := nntp.NewPool(backendConfig)
	log.Printf("Created connection pool for target server '%s:%d' with max %d connections", *host, *port, *maxThreads)

	// Get newsgroups to transfer
	newsgroups, err := getNewsgroupsToTransfer(db, progressDB, startTime, endTime, *transferGroup, *fileInclude, *fileExclude, *excludePrefix, *forceIncludeOnly)
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
		go BootConnWorkers(db, pool, redisCli)
	}
	nntp.ResultsMutex.Lock()
	TotalNewsgroups = int64(len(newsgroups))
	NewsgroupsToProcess = TotalNewsgroups
	nntp.ResultsMutex.Unlock()
	go CalcGlobalSpeed()
	// Start transfer process
	var wgP sync.WaitGroup
	wgP.Add(2)
	go func(wgP *sync.WaitGroup, redisCli *redis.Client) {
		defer wgP.Done()
		resultChan := make(chan error, 1)
		resultChan <- runTransfer(db, newsgroups, *batchCheck, *maxThreads, *dryRun, startTime, endTime, *debugCapture, wgP, redisCli, progressDB)
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
		for newsgroup, messageIDs := range debugArticles {
			fmt.Printf("Debug capture - Newsgroup: %s, Articles: %d\n", newsgroup, len(messageIDs))

			// Get group database for updates if needed
			groupDB, err := db.GetGroupDB(newsgroup)
			if err != nil {
				fmt.Printf("! Error getting group database for %s: %v\n", newsgroup, err)
				continue
			}

			for _, messageId := range messageIDs {
				article, err := db.GetArticleByMessageID(groupDB, *messageId)
				if err != nil {
					fmt.Printf("! Error getting article '%s': %v\n", *messageId, err)
					continue
				}
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

						if err := db.UpdateArticleDateSent(groupDB, article.MessageID, article.DateSent, article.DateString); err != nil {
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
			groupDB.Return()
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
	// Signal background tasks to stop
	close(db.StopChan)

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
	time.Sleep(time.Second * 3) // wait for all goroutines to finish
	log.Printf("nntp-transfer exit. Runtime: %v", time.Since(bootTime))
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
func getNewsgroupsToTransfer(db *database.Database, progressDB *nntp.TransferProgressDB, startTime, endTime *time.Time, groupPattern, fileInclude, fileExclude, excludePrefix string, forceIncludeOnly bool) ([]*models.Newsgroup, error) {
	var newsgroups []*models.Newsgroup

	// Parse exclude prefix patterns (comma-separated)
	var excludePrefixes []string
	if excludePrefix != "" {
		excludePrefixes = strings.Split(excludePrefix, ",")
		for i, p := range excludePrefixes {
			excludePrefixes[i] = strings.TrimSpace(p)
			log.Printf("Excluding newsgroups with prefix: '%s'", excludePrefixes[i])
		}
	}

	// Load include/exclude patterns from files if specified
	var includePatterns, excludePatterns []string
	var includeLookup, excludeLookup map[string]bool
	var hasIncludeWildcards, hasExcludeWildcards bool
	var err error
	var notExists []string

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
	quickLookup := make(map[string]bool, len(allNewsgroups))
	for _, ng := range allNewsgroups {
		quickLookup[ng.Name] = true
	}
	for ng := range includeLookup {
		if !quickLookup[ng] {
			notExists = append(notExists, ng)
		}
	}
	if len(notExists) > 0 {
		log.Printf("%d newsgroup not found locally.", len(notExists))
		for _, ngName := range notExists {
			log.Printf(" - %s (not found locally)", ngName)
		}
	}

	// Fetch all progress newsgroups once for fast lookup (used by all paths below)
	progressMap := make(map[string]bool, 125000)
	if progressDB != nil {
		progressFetchStart := time.Now()
		progressMap, err = progressDB.GetAllProgressNewsgroups(startTime, endTime)
		if err != nil {
			log.Printf("Warning: Failed to fetch progress newsgroups: %v (will skip progress checks)", err)
			progressMap = make(map[string]bool)
		} else {
			log.Printf("Fetched %d newsgroups with existing progress in %v", len(progressMap), time.Since(progressFetchStart))
		}
	}

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
			// Check exclude prefix first
			if len(excludePrefixes) > 0 {
				if matchesExcludePrefix(ng.Name, excludePrefixes) {
					continue
				}
			}
			// Check if newsgroup already has results for this remote (fast map lookup)
			if progressMap[ng.Name] {
				log.Printf("Skipping newsgroup %s - already has transfer results for this remote", ng.Name)
				continue
			}
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
		log.Printf("Applied include pattern filtering in %v.", time.Since(start))
		log.Printf("Force-include-only mode: found %d newsgroups to transfer after filtering", len(newsgroups))
		return newsgroups, nil
	}

	// Handle $all pattern (transfer all newsgroups, but still apply file filters)
	if groupPattern == "$all" {
		log.Printf("Using $all pattern: transferring all newsgroups with file filters applied")
		start := time.Now()
		for _, ng := range allNewsgroups {
			// Check exclude prefix first
			if len(excludePrefixes) > 0 {
				if matchesExcludePrefix(ng.Name, excludePrefixes) {
					continue
				}
			}
			// Check if newsgroup already has results for this remote (fast map lookup)
			if progressMap[ng.Name] {
				log.Printf("Skipping newsgroup %s - already has transfer results for this remote", ng.Name)
				continue
			}
			if !shouldIncludeNewsgroup(ng.Name, includePatterns, excludePatterns, includeLookup, excludeLookup, hasIncludeWildcards, hasExcludeWildcards) {
				log.Printf("Excluding newsgroup %s based on include/exclude patterns", ng.Name)
				continue
			}
			newsgroups = append(newsgroups, ng)
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
				// Check exclude prefix first
				if matchesExcludePrefix(ng.Name, excludePrefixes) {
					continue
				}
				// Check if newsgroup already has results for this remote (fast map lookup)
				if progressMap[ng.Name] {
					log.Printf("Skipping newsgroup %s - already has transfer results for this remote", ng.Name)
					continue
				}
				if shouldIncludeNewsgroup(ng.Name, includePatterns, excludePatterns, includeLookup, excludeLookup, hasIncludeWildcards, hasExcludeWildcards) {
					newsgroups = append(newsgroups, ng)
				}
			}
		}
	} else {
		// Exact match
		for _, ng := range allNewsgroups {
			if ng.Name == groupPattern {
				// Check exclude prefix first
				if matchesExcludePrefix(ng.Name, excludePrefixes) {
					break
				}
				// Check if newsgroup already has results for this remote (fast map lookup)
				if progressMap[ng.Name] {
					log.Printf("Skipping newsgroup %s - already has transfer results for this remote", ng.Name)
					break
				}
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

func IgnoreNewsgroupProgress(ng *models.Newsgroup, progressDB *nntp.TransferProgressDB, startTime, endTime *time.Time) bool {
	if progressDB != nil {
		exists, err := progressDB.NewsgroupExists(ng.Name, startTime, endTime)
		if err != nil {
			log.Printf("Warning: Failed to check if newsgroup %s exists in progress DB: %v", ng.Name, err)
			return true
		} else if exists {
			//log.Printf("Skipping newsgroup %s - already has transfer results for this remote", ng.Name)
			return true
		}
	}
	return false
}

// matchesExcludePrefix checks if a newsgroup name matches any of the exclude prefixes
func matchesExcludePrefix(ngName string, excludePrefixes []string) bool {
	for _, excludePrefix := range excludePrefixes {
		if strings.HasSuffix(excludePrefix, "*") {
			pattern := strings.TrimSuffix(excludePrefix, "*")
			if strings.HasPrefix(ngName, pattern) {
				return true
			}
		} else if ngName == excludePrefix {
			return true
		}
	}
	return false
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
func runTransfer(db *database.Database, newsgroups []*models.Newsgroup, batchCheck int64, maxThreads int, dryRun bool, startTime, endTime *time.Time, debugCapture bool, wgP *sync.WaitGroup, redisCli *redis.Client, progressDB *nntp.TransferProgressDB) error {
	defer wgP.Done()
	defer log.Printf("runTransfer() quitted")
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
			err := transferNewsgroup(db, ng, batchCheck, dryRun, startTime, endTime, debugCapture, redisCli, progressDB)
			if err == ErrNotInDateRange {
				nntp.ResultsMutex.Lock()
				nothingInDateRange++
				nntp.ResultsMutex.Unlock()
				err = nil // not a real error
			}

			nntp.ResultsMutex.Lock()
			NewsgroupsToProcess--
			if err != nil {
				log.Printf("Error transferring newsgroup %s: %v", ng.Name, err)
			} else {
				log.Printf("Newsgroup: '%s' | completed transferNewsgroup() remaining newsgroups: %d", ng.Name, NewsgroupsToProcess)
			}
			nntp.ResultsMutex.Unlock()
		}(ng, &wg, redisCli)
	}
	// Wait for all transfers to complete
	wg.Wait()

	nntp.ResultsMutex.Lock()
	if nothingInDateRange > 0 {
		log.Printf("Note: %d newsgroups had no articles in the specified date range", nothingInDateRange)
	}
	for _, result := range results {
		log.Print(result)
	}
	log.Printf("Summary: total: %d | transferred: %d | cache_hits: %d (before_check: %d, before_takethis: %d) | checked: %d/%d | wanted: %d | unwanted: %d | rejected: %d | retry: %d  | skipped: %d | TX_Errors: %d | connErrors: %d",
		globalTotalArticles, totalTransferred, totalRedisCacheHits, totalRedisCacheBeforeCheck, totalRedisCacheBeforeTakethis, totalChecked, totalCheckSentCount, totalWanted, totalUnwanted, totalRejected, totalRetry, totalSkipped, totalTXErrors, totalConnErrors)
	nntp.ResultsMutex.Unlock()
	log.Printf("Debug: StructChansCap1: %d/%d", len(common.StructChansCap1), cap(common.StructChansCap1))
	return nil
}

var debugArticles = make(map[string][]*string)
var debugMutex sync.Mutex
var ErrNotInDateRange = fmt.Errorf("notinrange")

// processRequeuedJobs processes any failed jobs that were requeued for retry
// Returns the number of jobs processed successfully
func processRequeuedJobs(newsgroup string, ttMode *nntp.TakeThisMode, ttResponsesSetupChan chan *nntp.TTSetup, redisCli *redis.Client) (int, error) {
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

		log.Printf("Newsgroup: '%s' | Processing requeued job %d/%d with %d articles", newsgroup, i+1, len(queuedJobs), len(job.MessageIDs))
		// pass articles to CHECK or TAKETHIS queue (async!)
		responseChan, err := processBatch(ttMode, job.MessageIDs, redisCli, job.OffsetStart, job.OffsetQ, job.NGTProgress)
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
			ttResponsesSetupChan <- nntp.GetTTSetup(responseChan)
		}
	}

	log.Printf("Newsgroup: '%s' | Successfully processed %d requeued jobs", newsgroup, len(queuedJobs))
	return len(queuedJobs), nil
}

// transferNewsgroup transfers articles from a single newsgroup
func transferNewsgroup(db *database.Database, ng *models.Newsgroup, batchCheck int64, dryRun bool, startTime, endTime *time.Time, debugCapture bool, redisCli *redis.Client, transferProgressDB *nntp.TransferProgressDB) error {

	//log.Printf("Newsgroup: '%s' | transferNewsgroup: Starting (getting group DBs)...", ng.Name)

	// Get group database
	groupDBA, err := db.GetGroupDB(ng.Name)
	if err != nil {
		return fmt.Errorf("failed to get group DBs for newsgroup '%s': %v", ng.Name, err)
	}
	//log.Printf("Newsgroup: '%s' | transferNewsgroup: Got group DBs, querying article count...", ng.Name)
	// Initialize newsgroup progress tracking
	nntp.ResultsMutex.Lock()
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
	nntp.ResultsMutex.Unlock()

	// Get total article count first with date filtering
	totalNGArticles, err := db.GetArticleCountWithDateFilter(groupDBA, startTime, endTime)
	if err != nil {
		if ferr := db.ForceCloseGroupDB(groupDBA); ferr != nil {
			log.Printf("ForceCloseGroupDB error for '%s': %v", ng.Name, ferr)
		}
		return fmt.Errorf("failed to get article count for newsgroup '%s': %v", ng.Name, err)
	}

	//log.Printf("Newsgroup: '%s' | transferNewsgroup: Got article count (%d), closing group DBs...", ng.Name, totalArticles)

	//log.Printf("Newsgroup: '%s' | transferNewsgroup: Closed group DBs, checking if articles exist...", ng.Name)

	if totalNGArticles == 0 {
		if ferr := db.ForceCloseGroupDB(groupDBA); ferr != nil {
			log.Printf("ForceCloseGroupDB error for '%s': %v", ng.Name, ferr)
		}
		nntp.ResultsMutex.Lock()
		nntp.NewsgroupTransferProgressMap[ng.Name].Finished = true
		nntp.NewsgroupTransferProgressMap[ng.Name].LastUpdated = time.Now()
		if VERBOSE {
			results = append(results, fmt.Sprintf("END Newsgroup: '%s' | No articles to process", ng.Name))
		}
		nntp.ResultsMutex.Unlock()
		// Insert result into progress database
		if transferProgressDB != nil {
			if err := transferProgressDB.InsertResult(ng.Name, startTime, endTime, 0, 0, 0, 0, 0, 0, 0, 0); err != nil {
				log.Printf("Warning: Failed to insert result to progress DB for '%s': %v", ng.Name, err)
			}
		}
		// No articles to process
		if startTime != nil || endTime != nil {
			if VERBOSE {
				log.Printf("No articles found in newsgroup: %s (within specified date range)", ng.Name)
			}
			return ErrNotInDateRange
		} else {
			if VERBOSE {
				log.Printf("No articles found in newsgroup: %s", ng.Name)
			}
		}
		return nil
	}
	groupDBA.Return()

	// Initialize newsgroup progress tracking
	nntp.ResultsMutex.Lock()
	nntp.NewsgroupTransferProgressMap[ng.Name].TotalArticles = totalNGArticles
	nntp.NewsgroupTransferProgressMap[ng.Name].LastUpdated = time.Now()
	ngtprogress := nntp.NewsgroupTransferProgressMap[ng.Name]
	nntp.ResultsMutex.Unlock()

	if dryRun {
		if startTime != nil || endTime != nil {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s (within specified date range)", totalNGArticles, ng.Name)
		} else {
			log.Printf("DRY RUN: Would transfer %d articles from newsgroup %s", totalNGArticles, ng.Name)
		}
		if !debugCapture {
			return nil
		}
	}

	if !dryRun && !debugCapture {
		log.Printf("+ Found %d articles in newsgroup %s", totalNGArticles, ng.Name)
	}

	remainingArticles := totalNGArticles
	ttMode := &nntp.TakeThisMode{
		Newsgroup: &ng.Name,
		CheckMode: CHECK_FIRST,
	}
	ttResponsesSetupChan := make(chan *nntp.TTSetup, totalNGArticles/int64(batchCheck)+2)
	start := time.Now()

	// WaitGroup to ensure collector goroutine finishes before returning
	var collectorWG sync.WaitGroup
	collectorWG.Add(1)

	// WaitGroup to track individual batched jobs response channel processors
	var responseWG sync.WaitGroup

	go func(responseWG *sync.WaitGroup) {
		defer collectorWG.Done()
		var num uint64
		for setup := range ttResponsesSetupChan {
			if setup == nil || setup.ResponseChan == nil {
				log.Printf("Newsgroup: '%s' | Warning: nil TT response channel received in collector!?", ng.Name)
				continue
			}
			num++
			//log.Printf("Newsgroup: '%s' | Starting response channel processor num %d (goroutines: %d)", ng.Name, num, runtime.NumGoroutine())
			responseWG.Add(1)
			go func(responseChan chan *nntp.TTResponse, num uint64, responseWG *sync.WaitGroup) {
				defer responseWG.Done()
				//defer log.Printf("Newsgroup: '%s' | Quit response channel processor num %d (goroutines: %d)", ng.Name, num, runtime.NumGoroutine())

				// Read exactly ONE response from this channel (channel is buffered with cap 1)
				resp := <-responseChan // job.Response(ForceCleanUp, err) arrives here

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
				if !resp.ForceCleanUp {
					return
				}
				// free memory - CRITICAL: Lock and unlock in same scope, not with defer!
				//resp.Job.Mux.Lock()
				//if VERBOSE {
				//	log.Printf("Newsgroup: '%s' | Cleaning up TT job #%d with %d articles (ForceCleanUp)", ng.Name, resp.Job.JobID, len(resp.Job.Articles))
				//}
				//models.RecycleArticles(resp.Job.Articles)
				//resp.Job.Articles = nil

				// Clean up ArticleMap - nil the keys (pointers) before deleting
				/* disabled
				for msgid := range resp.Job.ArticleMap {
					resp.Job.ArticleMap[msgid] = nil
					delete(resp.Job.ArticleMap, msgid)
				}
				resp.Job.ArticleMap = nil
				*/
				//resp.Job.Mux.Unlock()
				nntp.RecycleTTResponseChan(responseChan)
				nntp.RecycleTTResponse(resp)
			}(setup.ResponseChan, num, responseWG)
			nntp.RecycleTTSetup(setup)
		}
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Collector: ttResponses closed, waiting for %d response processors to finish...", ng.Name, num)
		}
		// Wait for all response channel processors to finish
		responseWG.Wait()
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | Collector: all response processors closed", ng.Name)
		}

		var tmpglobalTotalArticles, tmptotalTransferred, tmptotalTTSentCount, tmptotalCheckSentCount, tmptotalRedisCacheHits, tmptotalRedisCacheBeforeCheck, tmptotalRedisCacheBeforeTakethis uint64
		var tmptotalWanted, tmptotalUnwanted, tmptotalChecked, tmptotalRejected, tmptotalRetry, tmptotalSkipped, tmptotalTXErrors, tmptotalConnErrors uint64

		ngtprogress.Mux.Lock()
		result := fmt.Sprintf("END Newsgroup: '%s' total: %d | CHECK_sent: %d | checked: %d | transferred: %d | cache_hits: %d | wanted: %d | unwanted: %d | rejected: %d | retry: %d  | skipped: %d | TX_Errors: %d | connErrors: %d | took %v",
			ng.Name, totalNGArticles,
			ngtprogress.CheckSentCount, ngtprogress.Checked,
			ngtprogress.Transferred, ngtprogress.RedisCached,
			ngtprogress.Wanted, ngtprogress.Unwanted, ngtprogress.Rejected,
			ngtprogress.Retry, ngtprogress.Skipped,
			ngtprogress.TxErrors, ngtprogress.ConnErrors,
			time.Since(start))
		// capture values
		tmpglobalTotalArticles += uint64(totalNGArticles)
		tmptotalTransferred += ngtprogress.Transferred
		tmptotalTTSentCount += ngtprogress.TTSentCount
		tmptotalCheckSentCount += ngtprogress.CheckSentCount
		tmptotalRedisCacheHits += ngtprogress.RedisCached
		tmptotalRedisCacheBeforeCheck += ngtprogress.RedisCachedBeforeCheck
		tmptotalRedisCacheBeforeTakethis += ngtprogress.RedisCachedBeforeTakethis
		tmptotalWanted += ngtprogress.Wanted
		tmptotalUnwanted += ngtprogress.Unwanted
		tmptotalChecked += ngtprogress.Checked
		tmptotalRejected += ngtprogress.Rejected
		tmptotalRetry += ngtprogress.Retry
		tmptotalSkipped += ngtprogress.Skipped
		tmptotalTXErrors += ngtprogress.TxErrors
		tmptotalConnErrors += ngtprogress.ConnErrors
		// Mark newsgroup as finished
		ngtprogress.Finished = true
		ngtprogress.LastUpdated = time.Now()
		ngtprogress.LastCronTX = ngtprogress.LastUpdated
		ngtprogress.Mux.Unlock()

		nntp.ResultsMutex.Lock()
		// capture values
		globalTotalArticles += tmpglobalTotalArticles
		totalTransferred += tmptotalTransferred
		totalTTSentCount += tmptotalTTSentCount
		totalCheckSentCount += tmptotalCheckSentCount
		totalRedisCacheHits += tmptotalRedisCacheHits
		totalRedisCacheBeforeCheck += tmptotalRedisCacheBeforeCheck
		totalRedisCacheBeforeTakethis += tmptotalRedisCacheBeforeTakethis
		totalWanted += tmptotalWanted
		totalUnwanted += tmptotalUnwanted
		totalChecked += tmptotalChecked
		totalRejected += tmptotalRejected
		totalRetry += tmptotalRetry
		totalSkipped += tmptotalSkipped
		totalTXErrors += tmptotalTXErrors
		totalConnErrors += tmptotalConnErrors
		// store result
		results = append(results, result)
		for _, msgId := range rejectedArticles[ng.Name] {
			// prints all at the end again
			log.Printf("END Newsgroup: '%s' | REJECTED '%s'", ng.Name, msgId)
		}
		delete(rejectedArticles, ng.Name) // free memory
		nntp.ResultsMutex.Unlock()

		// Insert result into progress database
		if transferProgressDB != nil {
			if err := transferProgressDB.InsertResult(
				ng.Name,
				startTime,
				endTime,
				int64(tmptotalTransferred),
				int64(tmptotalUnwanted),
				int64(tmptotalChecked),
				int64(tmptotalRejected),
				int64(tmptotalRetry),
				int64(tmptotalSkipped),
				int64(tmptotalTXErrors),
				int64(tmptotalConnErrors),
			); err != nil {
				log.Printf("Warning: Failed to insert result to progress DB for '%s': %v", ng.Name, err)
			}
		}
	}(&responseWG)

	OffsetQueue := &nntp.OffsetQueue{
		Newsgroup:     &ng.Name,
		MaxQueuedJobs: MaxQueuedJobs,
	}

	// Use simple OFFSET pagination
	var processed int64
	msgIDsChan := make(chan []*string, MaxQueuedJobs)
	go db.GetMessageIDsWithDateFilter(ng, startTime, endTime, batchCheck, msgIDsChan)
	// Get articles in database batches (much larger than network batches)
	var dbOffset int64
	//start := time.Now()
	for messageIDs := range msgIDsChan {
		if common.WantShutdown() {
			log.Printf("WantShutdown in newsgroup: '%s' (processed %d messageIDs)", ng.Name, processed)
			return nil
		}
		// Process any requeued jobs first (from previous failed batches)
		if _, err := processRequeuedJobs(ng.Name, ttMode, ttResponsesSetupChan, redisCli); err != nil {
			return err
		}
		if len(messageIDs) == 0 {
			log.Printf("No more articles in newsgroup %s (loaded %d)", ng.Name, processed)
			break
		}

		if dryRun && debugCapture {
			log.Printf("Newsgroup: '%s' | DRY RUN with debug capture: Capturing %d articles (processed %d) BROKEN TODO NEED FIX!", ng.Name, len(messageIDs), processed)
			//debugMutex.Lock()
			// TODO: broken debug catpure code fetch articles here
			debugArticles[ng.Name] = append(debugArticles[ng.Name], messageIDs...)
			//debugMutex.Unlock()
			return nil
		}

		//log.Printf("Newsgroup: '%s' | Loaded %d mids | took: %v", ng.Name, len(messageIDs), time.Since(start))
		// Process articles in network batches
		//start2 := time.Now()
		OffsetQueue.Add(1)
		if common.WantShutdown() {
			log.Printf("WantShutdown in newsgroup: '%s' (processed %d)", ng.Name, processed)
			return nil
		}
		// Determine end index for the batch
		/* disabled
		//end := i + batchCheck
		//if end > len(messageIDs) {
		//	end = len(messageIDs)
		//}
		*/
		// pass articles to CHECK or TAKETHIS queue (async!)
		responseChan, err := processBatch(ttMode, messageIDs, redisCli, dbOffset, OffsetQueue, ngtprogress)
		if err != nil {
			log.Printf("Newsgroup: '%s' | Error processing batch offset %d: %v", ng.Name, dbOffset, err)
			return fmt.Errorf("error processing batch offset %d for newsgroup '%s': %v", dbOffset, ng.Name, err)
		}
		if responseChan != nil {
			// pass the response channel to the collector channel: ttResponses
			ttResponsesSetupChan <- nntp.GetTTSetup(responseChan)
		}
		dbOffset += int64(len(messageIDs))
		processed += int64(len(messageIDs))

		OffsetQueue.Wait(MaxQueuedJobs) // wait for offset batches to finish, less than N in flight
		// articlesProcessed already incremented above after loading from DB
		remainingArticles -= int64(len(messageIDs))

		//log.Printf("Newsgroup: '%s' | Pushed to queue (processed %d/%d) remaining: %d (Check=%t) took: %v", ng.Name, processed, totalNGArticles, remainingArticles, ttMode.UseCHECK(), time.Since(start2))
		//log.Printf("Newsgroup: '%s' | Pushed (processed %d/%d) total: %d/%d (unw: %d / rej: %d) (Check=%t)", ng.Name, articlesProcessed, totalArticles, transferred, remainingArticles, ttMode.Unwanted, ttMode.Rejected, ttMode.GetMode())
		//start = time.Now()
	} // end for msgIDsChan

	log.Printf("Newsgroup: '%s' | msgIDsChan closed, checking for requeued jobs...", ng.Name)

	// Process any remaining requeued jobs after main loop completes
	// This handles failures that occurred in the last batch
	for {
		if common.WantShutdown() {
			log.Printf("WantShutdown during final requeue processing for '%s'", ng.Name)
			break
		}
		processed, err := processRequeuedJobs(ng.Name, ttMode, ttResponsesSetupChan, redisCli)
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
	close(ttResponsesSetupChan)

	//log.Printf("Newsgroup: '%s' | ttResponses channel closed, waiting for collector to finish...", ng.Name)

	// Wait for collector goroutine to finish processing all responses
	collectorWG.Wait()

	log.Printf("Newsgroup: '%s' | All jobs completed and responses collected", ng.Name)
	deassignWorker(ng.Name)
	return nil
} // end func transferNewsgroup

var results []string
var rejectedArticles = make(map[string][]string)
var LowerLevel float64 = 90.0
var UpperLevel float64 = 95.0

// processBatch processes a batch of articles using NNTP streaming protocol (RFC 4644)
// Uses TAKETHIS primarily, falls back to CHECK when success rate < 95%
func processBatch(ttMode *nntp.TakeThisMode, messageIDs []*string, redisCli *redis.Client, dbOffset int64, offsetQ *nntp.OffsetQueue, ngtprogress *nntp.NewsgroupTransferProgress) (chan *nntp.TTResponse, error) {

	if len(messageIDs) == 0 {
		log.Printf("Newsgroup: '%s' | processBatch: no articles in this batch", *ttMode.Newsgroup)
		return nil, nil
	}

	// Update newsgroup progress with current offset
	nntp.ResultsMutex.RLock()
	if progress, exists := nntp.NewsgroupTransferProgressMap[*ttMode.Newsgroup]; exists {
		progress.Mux.Lock()
		progress.OffsetStart = dbOffset
		progress.LastUpdated = time.Now()
		progress.Mux.Unlock()
	}
	nntp.ResultsMutex.RUnlock()

	ttMode.FlipMode(LowerLevel, UpperLevel)

	job := &nntp.CHTTJob{
		JobID:        atomic.AddUint64(&nntp.JobIDCounter, 1),
		Newsgroup:    ttMode.Newsgroup,
		MessageIDs:   make([]*string, 0, len(messageIDs)),
		ResponseChan: nntp.GetTTResponseChan(),
		TTMode:       ttMode,
		OffsetStart:  dbOffset,
		OffsetQ:      offsetQ,
		NGTProgress:  ngtprogress,
	}
	var redis_cached uint64

	// Batch check Redis cache using pipeline before sending CHECK
	if redisCli != nil {
		pipe := redisCli.Pipeline()
		cmds := make([]*redis.IntCmd, len(messageIDs))
		redis2Check := 0
		// Queue all EXISTS commands
		for i, msgid := range messageIDs {
			if msgid == nil {
				continue
			}
			cmds[i] = pipe.Exists(redisCtx, *msgid)
			redis2Check++
		}

		// Execute all in one network round trip
		if _, err := pipe.Exec(redisCtx); err != nil {
			log.Printf("Newsgroup: '%s' | Redis pipeline error: %v", *ttMode.Newsgroup, err)
		} else {

			// Process results and filter cached articles
			for i, cmd := range cmds {
				if cmd == nil || messageIDs[i] == nil {
					log.Printf("Newsgroup: '%s' | Warning: nil Redis command or nil message ID in batch for job #%d (skip CHECK)", *ttMode.Newsgroup, job.JobID)
					continue // Skip if command wasn't queued or message ID is nil
				}
				msgid := messageIDs[i]
				exists, cmdErr := cmd.Result()
				if cmdErr == nil && exists > 0 {
					// Cached in Redis - skip this article
					if VERBOSE {
						log.Printf("Newsgroup: '%s' | Message ID '%s' is cached in Redis in job #%d (skip CHECK)", *ttMode.Newsgroup, *msgid, job.JobID)
					}
					job.NGTProgress.Increment(nntp.IncrFLAG_REDIS_CACHED_BEFORE_CHECK, 1)
					redis_cached++
					messageIDs[i] = nil
					continue
				}
				if cmdErr != nil {
					log.Printf("Newsgroup: '%s' | Redis cache error for message ID '%s' in job #%d: %v (include in CHECK)", *ttMode.Newsgroup, *msgid, job.JobID, cmdErr)
				}
				// Not cached - add to valid list
				//job.Articles = append(job.Articles, article)
				//job.ArticleMap[&article.MessageID] = article
				job.MessageIDs = append(job.MessageIDs, msgid)
			}
		}
		if redis_cached == uint64(len(messageIDs)) {
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | All %d articles in batch are cached in Redis in job #%d (skip CHECK)", *ttMode.Newsgroup, len(messageIDs), job.JobID)
			}
			return job.QuitResponseChan(), nil

		} else if redis_cached > 0 {
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | Redis got %d/%d cached articles in job #%d (before CHECK)", *ttMode.Newsgroup, redis_cached, len(messageIDs), job.JobID)
			}
		}
	} else {
		// No Redis - add all non-nil message IDs
		for _, msgid := range messageIDs {
			if msgid == nil {
				continue
			}
			//job.Articles = append(job.Articles, article)
			//job.ArticleMap[&article.MessageID] = article
			job.MessageIDs = append(job.MessageIDs, msgid)
		}
	}
	if len(job.MessageIDs) == 0 {
		log.Printf("Newsgroup: '%s' | No message IDs to check in batch. (redis_cache_hits: %d)", *ttMode.Newsgroup, redis_cached)
		return job.QuitResponseChan(), nil
	}

	// Assign job to worker (consistent assignment + load balancing)
	QueuesMutex.RLock()
	if len(CheckQueues) == 0 {
		QueuesMutex.RUnlock()
		log.Printf("Newsgroup: '%s' | No workers available to process batch job #%d with %d message IDs", *ttMode.Newsgroup, job.JobID, len(job.MessageIDs))
		return nil, fmt.Errorf("no workers available")
	}
	QueuesMutex.RUnlock()

	workerID := assignWorkerToNewsgroup(*ttMode.Newsgroup)

	QueuesMutex.RLock()
	WorkersCheckChannel := CheckQueues[workerID]
	QueuesMutex.RUnlock()

	//log.Printf("Newsgroup: '%s' | CheckWorker (%d) queueing job #%d with %d msgIDs to worker %d. CheckQ=%d", *ttMode.Newsgroup, workerID, job.JobID, len(job.MessageIDs), workerID, len(CheckQueues[workerID]))
	WorkersCheckChannel <- job // checkQueue <- job // goto: job := <-WorkersCheckChannel
	//log.Printf("Newsgroup: '%s' | CheckWorker (%d) queued Job #%d", *ttMode.Newsgroup, workerID, job.JobID)
	return job.GetResponseChan(), nil
} // end func processBatch

// sendArticlesBatchViaTakeThis sends multiple articles via TAKETHIS in streaming mode
// Sends all TAKETHIS commands and queues ReadRequests for concurrent processing
func sendArticlesBatchViaTakeThis(conn *nntp.BackendConn, articles []*models.Article, job *nntp.CHTTJob, newsgroup string, redisCli *redis.Client, demuxer *nntp.ResponseDemuxer, readTAKETHISResponsesChan chan *nntp.ReadRequest) (redis_cached uint64, err error) {
	if len(articles) == 0 {
		return 0, nil
	}

	// Phase 1: Send all TAKETHIS commands without waiting for responses
	//log.Printf("Phase 1: Sending %d TAKETHIS commands...", len(articles))

	// Batch check Redis cache using pipeline before sending TAKETHIS
	if redisCli != nil {
		pipe := redisCli.Pipeline()
		cmds := make([]*redis.IntCmd, len(articles))
		//redis2Check := 0
		// Queue all EXISTS commands (only for non-nil articles)
		for i, article := range articles {
			if article == nil {
				continue
			}
			cmds[i] = pipe.Exists(redisCtx, article.MessageID)
			//redis2Check++
		}

		// Execute all in one network round trip
		_, err := pipe.Exec(redisCtx)
		if err != nil {
			log.Printf("Newsgroup: '%s' | Redis pipeline error in TAKETHIS: %v", newsgroup, err)
		} else {

			// Process results and filter cached articles
			for i, cmd := range cmds {
				if cmd == nil || articles[i] == nil {
					log.Printf("Newsgroup: '%s' | Warning: nil Redis command or nil article in TAKETHIS batch for job #%d (skip)", newsgroup, job.JobID)
					continue // Skip if command wasn't queued or article is nil
				}

				exists, cmdErr := cmd.Result()
				if cmdErr == nil && exists > 0 {
					// Cached in Redis - skip this article
					if VERBOSE {
						log.Printf("Newsgroup: '%s' | Message ID '%s' is cached in Redis in job #%d (skip [TAKETHIS])", newsgroup, articles[i].MessageID, job.JobID)
					}
					job.NGTProgress.Increment(nntp.IncrFLAG_REDIS_CACHED_BEFORE_TAKETHIS, 1)
					redis_cached++
					articles[i] = nil // free memory
					continue
				}
				if cmdErr != nil {
					log.Printf("Newsgroup: '%s' | Redis cache error for message ID '%s' in job #%d: %v (include in TAKETHIS)", newsgroup, articles[i].MessageID, job.JobID, cmdErr)
				}
				// Not cached - will be sent
			}
		}
		if redis_cached == uint64(len(articles)) {
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | All %d articles are cached in Redis in job #%d (skip TAKETHIS)", newsgroup, len(articles), job.JobID)
			}
			return redis_cached, nil

		} else if redis_cached > 0 {
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | Redis got %d/%d cached articles in job #%d (before TAKETHIS)", newsgroup, redis_cached, len(articles), job.JobID)
			}
		}
	}

	// Now send TAKETHIS for non-cached articles
	var sentCount int
	conn.Lock()
	var ttxBytes uint64
	start := time.Now()
	astart := start
	astart2 := start
	skipped := 0
	for n, article := range articles {
		if article == nil {
			skipped++
			continue // Skip cached article
		}
		astart = time.Now()
		// Send TAKETHIS command with article content (non-blocking)
		// This also queues the ReadRequest to readTAKETHISResponsesChan BEFORE returning
		//log.Printf("Newsgroup: '%s' | ++Pre-Send TAKETHIS '%s'", newsgroup, article.MessageID)

		// Increment pending responses counter before sending
		job.PendingResponses.Add(1)

		cmdID, txBytes, err, doContinue := conn.SendTakeThisArticleStreaming(article, &processor.LocalNNTPHostname, newsgroup, demuxer, readTAKETHISResponsesChan, job, n, len(articles)-skipped)
		astart2 = time.Now()
		job.Mux.Lock()
		job.TTxBytes += uint64(txBytes)
		job.TmpTxBytes += uint64(txBytes)
		job.Mux.Unlock()
		ttxBytes += uint64(txBytes)
		if err != nil {
			// Decrement on error since no response will come
			job.PendingResponses.Done()
			if err == common.ErrNoNewsgroups || doContinue {
				job.NGTProgress.Increment(nntp.IncrFLAG_SKIPPED, 1)
				log.Printf("Newsgroup: '%s' | skipped TAKETHIS '%s': bad newsgroups header doContinue=%t", newsgroup, article.MessageID, doContinue)
				continue
			}
			conn.Unlock()
			job.NGTProgress.Increment(nntp.IncrFLAG_CONN_ERRORS, 1)
			log.Printf("ERROR Newsgroup: '%s' | Failed to send TAKETHIS for %s: %v", newsgroup, article.MessageID, err)
			return redis_cached, fmt.Errorf("failed to send TAKETHIS for %s: %v", article.MessageID, err)
		}
		sentCount++
		job.NGTProgress.Increment(nntp.IncrFLAG_TTSentCount, 1)

		if VERBOSE {
			log.Printf("Newsgroup: '%s' | DONE TAKETHIS '%s' CmdID=%d (%d/%d sent) in %v awaiting responses astart2='%v'", newsgroup, article.MessageID, cmdID, sentCount, len(articles), time.Since(astart), time.Since(astart2))
		}
	}
	conn.Unlock()
	if VERBOSE {
		log.Printf("Newsgroup: '%s' | DONE TAKETHIS BATCH sent: %d commands. ttxBytes: %d in %v", newsgroup, sentCount, ttxBytes, time.Since(start))
	}
	return redis_cached, nil
} // end func sendArticlesBatchViaTakeThis

var jobRequeueMutex sync.RWMutex
var jobRequeue = make(map[*string][]*nntp.CHTTJob)

// CheckQueues holds per-worker CheckQueue channels for consistent newsgroup routing
var QueuesMutex sync.RWMutex
var CheckQueues []chan *nntp.CHTTJob
var TakeThisQueues []chan *nntp.CHTTJob

// Demuxers holds per-worker demuxer instances for statistics tracking
var Demuxers []*nntp.ResponseDemuxer
var DemuxersMutex sync.RWMutex

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
		log.Fatalf("assignWorkerToNewsgroup: no workers available?")
	}

	minLoad := WorkerQueueLength[0]
	workerID := 0
	for wid := 1; wid < len(WorkerQueueLength); wid++ {
		if WorkerQueueLength[wid] < minLoad {
			minLoad = WorkerQueueLength[wid]
			workerID = wid
			break
		}
	}
	WorkerQueueLength[workerID]++
	WorkerQueueLengthMux.Unlock()

	// Assign newsgroup to this worker
	NewsgroupWorkerMap[newsgroup] = workerID

	return workerID
}

func deassignWorker(newsgroup string) {
	// Remove newsgroup assignment
	NewsgroupWorkerMapMux.Lock()
	workerID, exists := NewsgroupWorkerMap[newsgroup]
	if exists {
		delete(NewsgroupWorkerMap, newsgroup)
		// Decrement worker queue length
		WorkerQueueLengthMux.Lock()
		if workerID >= 0 && workerID < len(WorkerQueueLength) {
			if WorkerQueueLength[workerID] > 0 {
				WorkerQueueLength[workerID]--
			}
		}
		WorkerQueueLengthMux.Unlock()
	}
	NewsgroupWorkerMapMux.Unlock()
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

func BootConnWorkers(db *database.Database, pool *nntp.Pool, redisCli *redis.Client) {
	openConns := 0
	workerSlots := make([]bool, nntp.NNTPTransferThreads)
	defaultSleep := time.Second
	isleep := defaultSleep
	var mux sync.Mutex
	// Create per-worker queues
	QueuesMutex.Lock()
	CheckQueues = make([]chan *nntp.CHTTJob, nntp.NNTPTransferThreads)
	TakeThisQueues = make([]chan *nntp.CHTTJob, nntp.NNTPTransferThreads)
	WorkerQueueLength = make([]int, nntp.NNTPTransferThreads)
	DemuxersMutex.Lock()
	Demuxers = make([]*nntp.ResponseDemuxer, nntp.NNTPTransferThreads)
	DemuxersMutex.Unlock()
	for i := range CheckQueues {
		CheckQueues[i] = make(chan *nntp.CHTTJob)                   // no cap for CH jobs
		TakeThisQueues[i] = make(chan *nntp.CHTTJob, MaxQueuedJobs) // allows max N queued TT jobs
		WorkerQueueLength[i] = 0
	}
	QueuesMutex.Unlock()
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
		errChan := common.GetStructChanCap1()
		defer common.RecycleStructChanCap1(errChan)
		newConns := 0
		for workerID := range bootN {
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
				jobsQueued: make(map[*nntp.CHTTJob]uint64),
				jobsReadOK: make(map[*nntp.CHTTJob]uint64),
				jobMap:     make(map[*string]*nntp.CHTTJob),
				jobs:       make([]*nntp.CHTTJob, 0, MaxQueuedJobs),
			}

			returnSignals[workerID] = returnSignal
			// assign checkQueue by openConns counter
			// so restarted workers get same channels to read from
			go CHTTWorker(db, workerID, conn, returnSignal)
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
				for workerID, wait := range returnSignals {
					if wait == nil {
						continue
					}
					select {
					case rs := <-wait.ExitChan:
						WorkerQueueLengthMux.Lock()
						log.Printf("CHTTWorker (%d) exited. processed jobs: %d", workerID, WorkerQueueLength[workerID])
						WorkerQueueLengthMux.Unlock()

						monitoring--

						UnsetWorker(&openConns, rs.slotID, workerSlots, &mux)
						returnSignals[workerID] = nil

						rs.Mux.Lock()
						if len(rs.jobs) > 0 {
							log.Printf("CHTTWorker (%d) try requeue %d jobs", workerID, len(rs.jobs))
							for _, job := range rs.jobs {
								if job != nil {
									// copy articles pointer
									job.Mux.Lock()
									if len(job.MessageIDs) == 0 {
										log.Printf("ERROR in CHTTWorker (%d) job #%d has no articles, skipping requeue", workerID, job.JobID)
										job.Mux.Unlock()
										continue
									}
									rqj := &nntp.CHTTJob{
										JobID:       job.JobID,
										Newsgroup:   job.Newsgroup,
										MessageIDs:  job.MessageIDs,
										OffsetQ:     job.OffsetQ,
										NGTProgress: job.NGTProgress,
									}
									job.Mux.Unlock()

									jobRequeueMutex.Lock()
									jobRequeue[rqj.Newsgroup] = append(jobRequeue[rqj.Newsgroup], rqj)
									jobRequeueMutex.Unlock()
									log.Printf("CHTTWorker (%d) did requeue job #%d with %d articles for newsgroup '%s'", workerID, rqj.JobID, len(rqj.MessageIDs), *rqj.Newsgroup)
									// unlink pointers
									job.Mux.Lock()
									select {
									case job.ResponseChan <- nil:
									default:
									}
									if job.TTMode != nil {
										job.TTMode.Newsgroup = nil
									}
									job.Newsgroup = nil
									job.TTMode = nil
									job.MessageIDs = nil
									job.WantedIDs = nil
									job.OffsetQ = nil
									job.NGTProgress = nil
									job.Mux.Unlock()
								}
							}
							log.Printf("CHTTWorker (%d) did requeue %d jobs", workerID, len(rs.jobs))
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
	log.Printf("BootConnWorkers: quit")
} // end func BootConnWorkers

var DefaultCheckTicker = 5 * time.Second

var JobsToRetry []*nntp.CHTTJob
var JobsToRetryMux sync.Mutex

type ReturnSignal struct {
	Mux        sync.Mutex
	ConnMux    sync.Mutex // Simple mutex to serialize connection access
	slotID     int
	ExitChan   chan *ReturnSignal
	errChan    chan struct{}
	redisCli   *redis.Client
	jobsQueued map[*nntp.CHTTJob]uint64
	jobsReadOK map[*nntp.CHTTJob]uint64
	jobMap     map[*string]*nntp.CHTTJob
	jobs       []*nntp.CHTTJob
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

func CHTTWorker(db *database.Database, workerID int, conn *nntp.BackendConn, rs *ReturnSignal) {
	var mux sync.Mutex
	var runningTTJobs int // protected by local mux
	var workerWG sync.WaitGroup
	TTworkerRequestChan := make(chan struct{}, 1)
	TTworkerReleaseChan := make(chan struct{}, 1)
	readCHECKResponsesChan := make(chan *nntp.ReadRequest, 1024*1024)
	readTAKETHISResponsesChan := make(chan *nntp.ReadRequest, 1024*1024)
	errChan := make(chan struct{}, 9)

	QueuesMutex.RLock()
	WorkersTTChannel := TakeThisQueues[workerID]
	WorkersCheckChannel := CheckQueues[workerID]
	QueuesMutex.RUnlock()

	tickChan := common.GetStructChanCap1()
	requestReplyJobDone := common.GetStructChanCap1()
	replyJobDone := common.GetStructChanCap1()
	defer common.RecycleStructChanCap1(tickChan)
	defer common.RecycleStructChanCap1(requestReplyJobDone)
	defer common.RecycleStructChanCap1(replyJobDone)

	// Create ResponseDemuxer to eliminate race conditions in ReadCodeLine
	demuxer := nntp.NewResponseDemuxer(conn, errChan)

	// Store demuxer for statistics tracking
	DemuxersMutex.Lock()
	if workerID < len(Demuxers) {
		Demuxers[workerID] = demuxer
	}
	DemuxersMutex.Unlock()

	defer func(conn *nntp.BackendConn, rs *ReturnSignal) {
		conn.ForceCloseConn()
		rs.ExitChan <- rs
		common.SignalErrChan(errChan)
	}(conn, rs)
	//lastRun := time.Now()

	// Start the central response reader (CRITICAL: only ONE goroutine reads from connection)
	demuxer.Start()
	log.Printf("CheckWorker (%d): Started ResponseDemuxer", workerID)

	// launch go routine which sends CHECK commands
	workerWG.Add(1)
	go func(workerWG *sync.WaitGroup) {
		ticker := time.NewTicker(DefaultCheckTicker)
		defer func(workerWG *sync.WaitGroup) {
			conn.ForceCloseConn()
			ticker.Stop()
			common.SignalErrChan(errChan)
			workerWG.Done()
			log.Printf("CheckWorker (%d): CHECK sender goroutine exiting", workerID)

		}(workerWG)
		// tick every n seconds to check if any CHECKs to do
	loop:
		for {
			select {
			case <-errChan:
				common.SignalErrChan(errChan)
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
				if VERBOSE && len(rs.jobs) >= MaxQueuedJobs {
					log.Printf("CheckWorker (%d): Ticked and found %d jobs in queue (max: %d)", workerID, len(rs.jobs), MaxQueuedJobs)
				}
				currentJob := rs.jobs[0]
				rs.jobs = rs.jobs[1:] // Remove first job from queue
				rs.Mux.Unlock()
				if currentJob == nil {
					log.Printf("ERROR CheckWorker (%d): got nil job from queue, skipping...", workerID)
					continue loop
				}
				workerID := assignWorkerToNewsgroup(*currentJob.Newsgroup)
				requestedRelease := false
				if len(WorkersTTChannel) >= cap(WorkersTTChannel) {
					log.Printf("CheckWorker (%d): waiting... takeThisChan full (%d)", workerID, len(WorkersTTChannel))
					select {
					case TTworkerRequestChan <- struct{}{}:
						requestedRelease = true
					default:
					}
				}
			waiting:
				for {
					if len(WorkersTTChannel) < cap(WorkersTTChannel) {
						break waiting
					}
					select {
					case <-errChan:
						common.SignalErrChan(errChan)
						log.Printf("CheckWorker (%d): waiting for TakeThisChan got errChan signal... exiting", workerID)
						return
					case <-time.After(time.Millisecond * 16):
						if len(WorkersTTChannel) < cap(WorkersTTChannel) {
							break waiting
						}
					case <-TTworkerReleaseChan:
						if len(WorkersTTChannel) < cap(WorkersTTChannel) {
							break waiting
						}
						if !requestedRelease {
							TTworkerRequestChan <- struct{}{}
							requestedRelease = true
						}
					default:
						// continues waiting loop
					}
				}
				currentJob.OffsetQ.OffsetBatchDone()
				currentJob.TTMode.FlipMode(LowerLevel, UpperLevel)
				if currentJob.TTMode.UseCHECK() {
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d waits to check %d message IDs in batches of %d", *currentJob.Newsgroup, workerID, currentJob.JobID, len(currentJob.MessageIDs), BatchCheck)

					if !conn.IsConnected() {
						rs.Mux.Lock()
						rs.jobs = append([]*nntp.CHTTJob{currentJob}, rs.jobs...) // requeue at front
						rs.Mux.Unlock()
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d connection lost before SendCheckMultiple for batch (offset %d: %d-%d)", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd)
						time.Sleep(time.Second)
						return
					}
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d acquire connection lock for batch (offset %d: %d-%d) (%d messages)", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd, len(currentJob.MessageIDs))
					rs.ConnMux.Lock()
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d acquired connection lock for batch (offset %d: %d-%d) -> SendCheckMultiple", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd)
					checksSent, err := conn.SendCheckMultiple(currentJob.MessageIDs, readCHECKResponsesChan, currentJob, demuxer)
					rs.ConnMux.Unlock()
					if err != nil {
						log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d SendCheckMultiple error for batch (offset %d: %d-%d): %v", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd, err)
						time.Sleep(time.Second)
						rs.Mux.Lock()
						rs.jobs = append([]*nntp.CHTTJob{currentJob}, rs.jobs...) // requeue at front
						rs.Mux.Unlock()
						return
					}
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d Sent CHECK for batch (offset %d: %d-%d), responses will be read asynchronously...", *currentJob.Newsgroup, workerID, currentJob.JobID, currentJob.OffsetStart, currentJob.BatchStart, currentJob.BatchEnd)

					// Add CHECK sent count to progress after all batches are sent
					currentJob.NGTProgress.Mux.Lock()
					currentJob.NGTProgress.CheckSentCount += checksSent
					currentJob.NGTProgress.Mux.Unlock()
				} else {
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d skipping CHECK for %d message IDs (TAKETHIS mode)", *currentJob.Newsgroup, workerID, currentJob.JobID, len(currentJob.MessageIDs))
					currentJob.WantedIDs = currentJob.MessageIDs
					// Use blocking send with select for graceful shutdown support
					select {
					case WorkersTTChannel <- currentJob: // local takethis chan sharing the same connection
						// Job successfully enqueued
					case <-errChan:
						// Shutdown requested, requeue job and exit
						rs.Mux.Lock()
						rs.jobs = append([]*nntp.CHTTJob{currentJob}, rs.jobs...)
						rs.Mux.Unlock()
						log.Printf("CheckWorker (%d): Shutdown while waiting to enqueue TAKETHIS job", workerID)
						return
					}
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): job #%d sent to local TakeThisChan", *currentJob.Newsgroup, workerID, currentJob.JobID)
				}
				//lastRun = time.Now()
				// Check if there are more jobs to process
				//log.Printf("CheckWorker (%d): job #%d CHECK done, checking for more jobs...", workerID, currentJob.JobID)
				rs.Mux.Lock()
				hasMoreJobs := len(rs.jobs) > 0
				rs.Mux.Unlock()
				replyChan(requestReplyJobDone, replyJobDone) // see if anybody is waiting and reply
				//log.Printf("CheckWorker (%d): job #%d CHECK done, hasMoreJobs=%v", workerID, currentJob.JobID, hasMoreJobs)

				// If there are more jobs waiting, immediately trigger next job processing
				if hasMoreJobs {
					common.SignalTickChan(tickChan)
				}
				//log.Printf("CheckWorker (%d): job #%d CHECKs sent, loop to next job", workerID, currentJob.JobID)

			case <-ticker.C:
				if common.WantShutdown() {
					log.Printf("CheckWorker (%d): Ticker WantShutdown, exiting", workerID)
					return
				}
				rs.Mux.Lock()
				hasWork := len(rs.jobs) > 0
				rs.Mux.Unlock()
				if hasWork {
					//log.Printf("CheckWorker (%d): Ticker found work to do, signaling tickChan...", workerID)
					common.SignalTickChan(tickChan)
				} else {
					//log.Printf("CheckWorker (%d): Ticker found no work to do.", workerID)
				}
			} // end select
		} // end forever
	}(&workerWG)

	// launch a go routine to read CHECK responses from the supplied connection with textproto readline
	workerWG.Add(1)
	go func(workerWG *sync.WaitGroup) {
		var responseCount int
		var tookTime time.Duration
		defer func(workerWG *sync.WaitGroup) {
			conn.ForceCloseConn()
			common.SignalErrChan(errChan)
			workerWG.Done()
		}(workerWG)
	loop:
		for {
			select {
			case <-errChan:
				log.Printf("CheckWorker (%d): Read CHECK responses got errChan signal...", workerID)
				common.SignalErrChan(errChan)
				log.Printf("CheckWorker (%d): Read CHECK responses exiting", workerID)
				return

			case rr := <-readCHECKResponsesChan:
				//log.Printf("CheckWorker (%d): Read CHECK got readRequest for rr: '%v'", workerID, rr)
				if rr == nil || rr.MsgID == nil {
					log.Printf("CheckWorker (%d): Read CHECK got nil readRequest, skipping", workerID)
					continue loop
				}
				if common.WantShutdown() {
					log.Printf("CheckWorker (%d): Read CHECK WantShutdown, exiting", workerID)
					rr.ClearReadRequest(nil)
					return
				}
				//log.Printf("CheckWorker (%d): Read CHECK response (do conn check) for msgID: %s (cmdID=%d MID=%d/%d)", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs)
				if !conn.IsConnected() {
					log.Printf("CheckWorker (%d): Read CHECK connection lost, exiting", workerID)
					rr.ClearReadRequest(nil)
					return
				}
				//log.Printf("CheckWorker (%d): Pre-Read CHECK response for msgID: %s (cmdID=%d MID=%d/%d)", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs)
				start := time.Now()

				// NEW: Get pre-read response from demuxer (eliminates race condition)
				var respData *nntp.ResponseData
				select {
				case respData = <-demuxer.GetCheckResponseChan():
					// Got response from demuxer
				case <-errChan:
					log.Printf("CheckWorker (%d): Read CHECK got errChan while waiting for response", workerID)
					rr.ClearReadRequest(respData)
					return
				}

				// Verify we got the expected command ID
				if respData.CmdID != rr.CmdID {
					log.Printf("ERROR CheckWorker (%d): Command ID mismatch! Expected %d, got %d", workerID, rr.CmdID, respData.CmdID)
					rr.ClearReadRequest(respData)
					return
				}

				if respData.Code == 0 && respData.Err != nil {
					log.Printf("Failed to read CHECK response: %v", respData.Err)
					rr.ClearReadRequest(respData)
					return
				}

				took := time.Since(start)
				tookTime += took
				responseCount++
				rr.Job.NGTProgress.Increment(nntp.IncrFLAG_CHECKED, 1)
				if rr.N == 1 && took.Milliseconds() > 1000 {
					log.Printf("CheckWorker (%d): time to first response for msgID: %s (cmdID=%d MID=%d/%d) took: %v ms", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, took.Milliseconds())
					tookTime = 0
				} else if responseCount >= 10000 {
					avg := time.Duration(float64(tookTime) / float64(responseCount))
					if avg.Milliseconds() > 0 {
						log.Printf("CheckWorker (%d): Read %d CHECK responses, avg latency: %v, last: %v (cmdID=%d MID=%d/%d)", workerID, responseCount, avg, took, rr.CmdID, rr.N, rr.Reqs)
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
				parts := strings.Fields(respData.Line)
				if len(parts) < 1 {
					log.Printf("ERROR in CheckWorker: Malformed CHECK response code=%d line: '%s' (cmdID=%d MID=%d/%d)", respData.Code, respData.Line, rr.CmdID, rr.N, rr.Reqs)
					rr.ClearReadRequest(respData)
					return
				}
				if parts[0] != *rr.MsgID {
					log.Printf("ERROR in CheckWorker: Mismatched CHECK response: expected '%s', got '%s' code=%d (cmdID=%d MID=%d/%d)", *rr.MsgID, parts[0], respData.Code, rr.CmdID, rr.N, rr.Reqs)
					rr.ClearReadRequest(respData)
					return
				}
				//log.Printf("CheckWorker (%d): Got CHECK response: '%s' (cmdID=%d MID=%d/%d) took: %v ms", workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, time.Since(start).Milliseconds())

				rs.Mux.Lock()
				job, exists := rs.jobMap[rr.MsgID]
				rs.Mux.Unlock()
				job.NGTProgress.AddNGTP(1, 0, 0)
				//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG1 Processing CHECK response for msgID: %s (cmdID=%d MID=%d/%d) code=%d", *job.Newsgroup, workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, code)
				if !exists {
					log.Printf("Newsgroup: '%s' | ERROR in CheckWorker: ReadCheckResponse msgId '%s' did not exist in jobMap.", *job.Newsgroup, *rr.MsgID)
					rr.ClearReadRequest(respData)
					continue loop
				}
				//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG2 Processing CHECK response for msgID: %s (cmdID=%d MID=%d/%d) code=%d", *job.Newsgroup, workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, code)
				rs.Mux.Lock()
				rs.jobMap[rr.MsgID] = nil // Nil the pointer before deleting
				delete(rs.jobMap, rr.MsgID)
				rs.jobsReadOK[job]++
				rs.Mux.Unlock()
				switch respData.Code {
				case 238:
					//log.Printf("Newsgroup: '%s' | Got Response: Wanted Article '%s': code=%d", *job.Newsgroup, *rr.MsgID, code)
					job.AppendWantedMessageID(rr.MsgID)
					job.NGTProgress.Increment(nntp.IncrFLAG_WANTED, 1)

				case 438:
					//log.Printf("Newsgroup: '%s' | Got Response: Unwanted Article '%s': code=%d", *job.Newsgroup, *rr.MsgID, code)
					job.NGTProgress.Increment(nntp.IncrFLAG_UNWANTED, 1)
					// Cache unwanted in Redis if enabled
					if rs.redisCli != nil {
						if err := rs.redisCli.Set(redisCtx, *rr.MsgID, "1", REDIS_TTL).Err(); err != nil && VERBOSE {
							log.Printf("Newsgroup: '%s' | Failed to cache rejected message ID in Redis: %v", *rr.Job.Newsgroup, err)
						}
					}

				case 431:
					//log.Printf("Newsgroup: '%s' | Got Response: Retry Article '%s': code=%d", *job.Newsgroup, *rr.MsgID, code)
					job.NGTProgress.Increment(nntp.IncrFLAG_RETRY, 1)
					/* disabled caching of retries in Redis for now
					if rs.redisCli != nil {
						if err := rs.redisCli.Set(redisCtx, *rr.MsgID, "1", REDIS_TTL).Err(); err != nil && VERBOSE {
							log.Printf("Newsgroup: '%s' | Failed to cache retry message ID in Redis: %v", *rr.Job.Newsgroup, err)
						}
					}
					*/

				default:
					log.Printf("Newsgroup: '%s' | Unknown CHECK response: line='%s' code=%d expected msgID %s", *job.Newsgroup, respData.Line, respData.Code, *rr.MsgID)
				}
				// check if all jobs are done
				//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG3 Processing CHECK response for msgID: %s (cmdID=%d MID=%d/%d) code=%d", *job.Newsgroup, workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, code)
				rs.Mux.Lock()
				queuedCount, qexists := rs.jobsQueued[job]
				readCount, rexists := rs.jobsReadOK[job]
				rs.Mux.Unlock()
				if !qexists || !rexists {
					log.Printf("Newsgroup: '%s' | ERROR in CheckWorker: queuedCount or readCount did not exist for a job?!", *job.Newsgroup)
					rr.ClearReadRequest(respData)
					continue loop
				}
				//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG4 Processing CHECK response for msgID: %s (cmdID=%d MID=%d/%d) code=%d", *job.Newsgroup, workerID, *rr.MsgID, rr.CmdID, rr.N, rr.Reqs, code)
				rr.ClearReadRequest(respData)

				if queuedCount == readCount {
					rs.Mux.Lock()
					delete(rs.jobsQueued, job)
					delete(rs.jobsReadOK, job)
					rs.Mux.Unlock()
					if len(job.WantedIDs) > 0 {
						// Pass job to TAKETHIS worker via channel
						//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG5 job #%d got all %d CHECK responses, passing to TAKETHIS worker (wanted: %d articles) takeThisChan=%d", *job.Newsgroup, workerID, job.JobID, queuedCount, len(job.WantedIDs), len(WorkersTTChannel))
						WorkersTTChannel <- job // local takethis chan sharing the same connection
						//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG5c Sent job #%d to TAKETHIS worker (wanted: %d/%d) takeThisChan=%d", *job.Newsgroup, workerID, job.JobID, len(job.WantedIDs), queuedCount, len(WorkersTTChannel))

					} else {
						//log.Printf("Newsgroup: '%s' | CheckWorker (%d):  DEBUG6 job #%d got %d CHECK responses but server wants none", *job.Newsgroup, workerID, job.JobID, queuedCount)
						// Send response and close channel for jobs with no wanted articles
						job.Response(true, nil)
					}
				} else {
					//log.Printf("Newsgroup: '%s' | CheckWorker (%d): DEBUG6 job #%d CHECK responses so far: %d/%d readResponsesChan=%d", *job.Newsgroup, workerID, job.JobID, readCount, queuedCount, len(readResponsesChan))
				}
				continue loop
			} // end select
		} // end forever
	}(&workerWG)

	// launch a goroutine to process TAKETHIS responses concurrently
	// This follows the EXACT pattern as CHECK response reader (lines 2366-2552)
	workerWG.Add(1)
	go func(workerWG *sync.WaitGroup) {
		defer func(workerWG *sync.WaitGroup) {
			conn.ForceCloseConn()
			common.SignalErrChan(errChan)
			workerWG.Done()
		}(workerWG)

	ttloop:
		for {
			select {
			case <-errChan:
				log.Printf("TTResponseWorker (%d): got errChan signal, exiting", workerID)
				common.SignalErrChan(errChan)
				return

			case rr := <-readTAKETHISResponsesChan:
				if rr == nil || rr.MsgID == nil {
					log.Printf("TTResponseWorker (%d): got nil readRequest, skipping", workerID)
					if rr.Job != nil {
						rr.Job.PendingResponses.Done() // Mark response as handled (error case)
					}
					continue ttloop
				}
				if common.WantShutdown() {
					log.Printf("TTResponseWorker (%d): WantShutdown, exiting", workerID)
					rr.Job.PendingResponses.Done() // Mark response as handled (error case)
					rr.ClearReadRequest(nil)
					return
				}
				if !conn.IsConnected() {
					log.Printf("TTResponseWorker (%d): connection lost, exiting", workerID)
					rr.Job.PendingResponses.Done() // Mark response as handled (error case)
					rr.ClearReadRequest(nil)
					return
				}

				//log.Printf("TTResponseWorker (%d): Pre-Read TAKETHIS response for msgID: %s (cmdID=%d)", workerID, *rr.MsgID, rr.CmdID)

				// Get pre-read response from demuxer (same pattern as CHECK)
				var respData *nntp.ResponseData
				select {
				case respData = <-demuxer.GetTakeThisResponseChan():
					// Got response from demuxer
				case <-errChan:
					log.Printf("TTResponseWorker (%d): got errChan while waiting for response", workerID)
					rr.Job.PendingResponses.Done() // Mark response as handled (error case)
					rr.ClearReadRequest(respData)
					return
				}

				// Verify we got the expected command ID
				if respData.CmdID != rr.CmdID {
					log.Printf("ERROR TTResponseWorker (%d): Command ID mismatch! Expected %d, got %d", workerID, rr.CmdID, respData.CmdID)
					rr.Job.PendingResponses.Done() // Mark response as handled (error case)
					rr.ClearReadRequest(respData)
					return
				}

				if respData.Err != nil {
					log.Printf("ERROR TTResponseWorker (%d): Failed to read TAKETHIS response for %s: %v", workerID, *rr.MsgID, respData.Err)
					rr.Job.NGTProgress.Increment(nntp.IncrFLAG_CONN_ERRORS, 1)
					rr.Job.PendingResponses.Done() // Mark response as handled (error case)
					rr.ClearReadRequest(respData)
					return
				}

				rr.Job.TTMode.IncrementTmp()
				rr.Job.Mux.Lock()
				txbytes := rr.Job.TmpTxBytes
				rr.Job.TmpTxBytes = 0
				rr.Job.Mux.Unlock()
				rr.Job.NGTProgress.AddNGTP(0, 1, txbytes)

				// Handle response codes
				switch respData.Code {
				case 239:
					rr.Job.TTMode.IncrementSuccess()
					rr.Job.NGTProgress.Increment(nntp.IncrFLAG_TRANSFERRED, 1)
					// Cache transferred in Redis if enabled
					if rs.redisCli != nil {
						if err := rs.redisCli.Set(redisCtx, *rr.MsgID, "1", REDIS_TTL).Err(); err != nil && VERBOSE {
							log.Printf("Newsgroup: '%s' | Failed to cache transferred message ID in Redis: %v", *rr.Job.Newsgroup, err)
						}
					}

				case 439:
					rr.Job.NGTProgress.Increment(nntp.IncrFLAG_REJECTED, 1)
					// Cache rejection in Redis if enabled
					if rs.redisCli != nil {
						if err := rs.redisCli.Set(redisCtx, *rr.MsgID, "1", REDIS_TTL).Err(); err != nil && VERBOSE {
							log.Printf("Newsgroup: '%s' | Failed to cache rejected message ID in Redis: %v", *rr.Job.Newsgroup, err)
						}
					}
					if VERBOSE {
						log.Printf("Newsgroup: '%s' | Rejected article '%s': response=%d", *rr.Job.Newsgroup, *rr.MsgID, respData.Code)
					}

				case 400, 480, 500, 501, 502, 503, 504:
					log.Printf("ERROR Newsgroup: '%s' | Failed to transfer article '%s': response=%d", *rr.Job.Newsgroup, *rr.MsgID, respData.Code)
					rr.Job.NGTProgress.Increment(nntp.IncrFLAG_TX_ERRORS, 1)
					rr.Job.PendingResponses.Done() // Mark response handled (error case)
					rr.ClearReadRequest(respData)
					return

				default:
					log.Printf("ERROR Newsgroup: '%s' | Failed to transfer article '%s': unknown response=%d",
						*rr.Job.Newsgroup, *rr.MsgID, respData.Code)
					rr.Job.NGTProgress.Increment(nntp.IncrFLAG_TX_ERRORS, 1)
				}
				rr.Job.PendingResponses.Done()
				// Mark this response as processed
				rr.ClearReadRequest(respData)
			} // end select
		} // end for
	}(&workerWG)

	// launch a goroutine to process TAKETHIS jobs from local channel sharing the same connection
	workerWG.Add(1)
	go func(workerWG *sync.WaitGroup) {
		defer func(workerWG *sync.WaitGroup) {
			conn.ForceCloseConn()
			common.SignalErrChan(errChan)
			workerWG.Done()
		}(workerWG)
		var job *nntp.CHTTJob
		for {
			if common.WantShutdown() {
				log.Printf("TTworker (%d): WantShutdown, exiting", workerID)
				return
			}

			select {
			case job = <-WorkersTTChannel:
				// got new job
				if len(WorkersTTChannel) >= cap(WorkersTTChannel)-1 {
					// see if anybody is waiting
					select {
					case <-TTworkerRequestChan:
						// anybody IS waiting!
						select {
						case TTworkerReleaseChan <- struct{}{}:
							// sent release notify
						default:
						}
					default:
						// nobody was waiting
					}
				}

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
			// TODO: fetch articles from database for all wantedIDs
			wantedArticles, err := db.GetArticlesByIDs(job.Newsgroup, job.WantedIDs)
			if err != nil {
				log.Printf("Newsgroup: '%s' | TTworker (%d): Error fetching wanted articles from DB for job #%d: %v", *job.Newsgroup, workerID, job.JobID, err)
				job.Response(false, err)
				continue
			}

			if len(wantedArticles) == 0 {
				log.Printf("Newsgroup: '%s' | TTworker (%d): No valid wanted articles found in ArticleMap for job #%d", *job.Newsgroup, workerID, job.JobID)
				job.Response(true, nil)
				continue
			}
			mux.Lock()
			runningTTJobs++
			mux.Unlock()
			//log.Printf("Newsgroup: '%s' | TTworker (%d): Acquire connection lock to send TAKETHIS for job #%d with %d wanted articles", *job.Newsgroup, workerID, job.JobID, len(wantedArticles))
			//log.Printf("Newsgroup: '%s' | TTworker (%d): Sending TAKETHIS for job #%d with %d wanted articles", *job.Newsgroup, workerID, job.JobID, len(wantedArticles))
			// Send TAKETHIS commands using existing function
			rs.ConnMux.Lock()
			redis_cached, err := sendArticlesBatchViaTakeThis(conn, wantedArticles, job, *job.Newsgroup, rs.redisCli, demuxer, readTAKETHISResponsesChan)
			rs.ConnMux.Unlock()
			if err != nil {
				log.Printf("Newsgroup: '%s' | TTworker (%d): Error in TAKETHIS job #%d: %v", *job.Newsgroup, workerID, job.JobID, err)
				job.Response(false, err)
				rs.Mux.Lock()
				// requeue at front
				rs.jobs = append([]*nntp.CHTTJob{job}, rs.jobs...)
				rs.Mux.Unlock()
				mux.Lock()
				runningTTJobs--
				mux.Unlock()
				return
			}
			if redis_cached > 0 {
				if VERBOSE {
					log.Printf("Newsgroup: '%s' | TTworker (%d): TAKETHIS job #%d sent, redis_cached=%d", *job.Newsgroup, workerID, job.JobID, redis_cached)
				}
			}
			go func(job *nntp.CHTTJob) {
				// Wait for all TAKETHIS responses to be processed before completing job
				//log.Printf("Newsgroup: '%s' | TTworker (%d): Waiting for all TAKETHIS responses for job #%d", *job.Newsgroup, workerID, job.JobID)
				job.PendingResponses.Wait()
				//log.Printf("Newsgroup: '%s' | TTworker (%d): All TAKETHIS responses received for job #%d", *job.Newsgroup, workerID, job.JobID)
				// Send response back
				//log.Printf("Newsgroup: '%s' | TTworker (%d): Sending TTresponse for job #%d to responseChan len=%d", *job.Newsgroup, workerID, job.JobID, len(job.ResponseChan))
				job.Response(true, nil)
				//log.Printf("Newsgroup: '%s' | TTworker (%d): Sent TTresponse for job #%d to responseChan", *job.Newsgroup, workerID, job.JobID)
				mux.Lock()
				runningTTJobs--
				mux.Unlock()
			}(job)
		}
	}(&workerWG)

forever:
	for {
		select {
		case <-errChan:
			common.SignalErrChan(errChan)
			break forever

		case job := <-WorkersCheckChannel: // CheckQueues[workerID] // source: WorkersCheckChannel <- job // checkQueue <- job
			if common.WantShutdown() {
				log.Printf("CHTTworker: WantShutdown, exiting")
				break forever
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
			queueFull := len(rs.jobs) >= MaxQueuedJobs || len(WorkersTTChannel) >= MaxQueuedJobs
			if queueFull {
				log.Printf("Newsgroup: '%s' | CHTTworker (%d): got job #%d with %d message IDs. queued=%d ... waiting...", *job.Newsgroup, workerID, job.JobID, len(job.MessageIDs), len(rs.jobs))
				select {
				case requestReplyJobDone <- struct{}{}:
				default:
					log.Printf("Newsgroup: '%s' | Debug: CHTTworker (%d): job #%d could not signal requestReplyJobDone, channel full. pass", *job.Newsgroup, workerID, job.JobID)
					// pass
				}
			}
			rs.Mux.Unlock()
			if queueFull {
				start := time.Now()
				lastPrint := start
			waitForReply:
				for {
					select {
					case <-replyJobDone:
						// pass
					case <-time.After(time.Millisecond * 16):
						rs.Mux.Lock()
						queueFull = len(rs.jobs) >= MaxQueuedJobs || len(WorkersTTChannel) >= MaxQueuedJobs
						rs.Mux.Unlock()
						if !queueFull {
							break waitForReply
						}
						// log every 5s
						if time.Since(lastPrint) > time.Second {
							if common.WantShutdown() {
								break forever
							}
							rs.Mux.Lock()
							log.Printf("Newsgroup: '%s' | CHTTworker (%d): pre append job #%d waiting since %v rs.jobs=%d takeThisChan=%d", *job.Newsgroup, workerID, job.JobID, time.Since(start), len(rs.jobs), len(WorkersTTChannel))
							rs.Mux.Unlock()
							lastPrint = time.Now()
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
			common.SignalTickChan(tickChan) // goto: case <-tickChan:
			rs.Mux.Unlock()
		} // end select
	} // end for
	workerWG.Wait()
	startWait := time.Now()
	lastPrint := startWait
wait:
	for {
		mux.Lock()
		if runningTTJobs == 0 {
			mux.Unlock()
			break wait
		}
		mux.Unlock()
		select {
		case <-errChan:
			common.SignalErrChan(errChan)
			break wait
		default:
			// continue
		}
		time.Sleep(time.Millisecond * 50)
		if time.Since(lastPrint) > time.Second*5 {
			log.Printf("CHTTworker (%d): waiting since %v for %d running TAKETHIS jobs to complete before exiting...", workerID, time.Since(startWait), runningTTJobs)
			lastPrint = time.Now()
		}
	}
} // end func CHTTWorker

// monitorMemoryStats logs memory statistics periodically
func monitorMemoryStats() {

	var m runtime.MemStats
	startTime := time.Now()

	for {
		time.Sleep(30 * time.Second)
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
	http.HandleFunc("/results", handleResults)
	addr := fmt.Sprintf(":%d", port)
	log.Printf("Starting web server on http://ANY_ADDR:%s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Printf("Web server error: %v", err)
	}
}

// handleIndex serves the main page with transfer results
func handleIndex(w http.ResponseWriter, r *http.Request) {
	nntp.ResultsMutex.RLock()
	defer nntp.ResultsMutex.RUnlock()

	// HTML template for displaying results
	const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>{{.NewsgroupsToProcess}}:{{.ServerHostName}} - go-pugleaf nntp-transfer</title>
	<style>
		body {
			font-family: 'Consolas', 'Monaco', monospace;
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
		.text-light {
			color: #fdfdfdfd;
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
			height: 24px;
			background: #e0e0e0;
			border-radius: 10px;
			overflow: hidden;
			position: relative;
		}
		.progress-bar-main {
			height: 32px;
			background: #e0e0e0;
			border-radius: 10px;
			overflow: hidden;
			position: relative;
		}
		.progress-fill {
			height: 100%;
			background: linear-gradient(90deg, #285829ff, #214b23ff);
			transition: width 0.3s;
		}
		.progress-text {
			position: absolute;
			top: 0;
			left: 0;
			right: 0;
			bottom: 0;
			display: flex;
			align-items: center;
			justify-content: center;
			font-size: 11px;
			font-weight: 600;
			color: #333;
			text-shadow: 0 0 3px rgba(255,255,255,0.8);
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
	<h1>
		🚀 NNTP Transfer to {{.ServerHostName}} | <button class="refresh-btn" onclick="location.reload()">🔄 Refresh Now</button>
	</h1>
	<h2>
		Start Date: <em>{{.StartDate}}</em> to
		End Date: <em>{{.EndDate}}</em>
	</h2>
	<div class="stats">
		{{if eq .Started 0}}
			<strong>Waiting for transfers to start...</strong>
		{{else}}
			{{if eq 0 .NewsgroupsToProcess}}
				✅ All complete!
				<br><br>
				<strong>Summary Statistics:</strong><br>
				Total Articles: {{.TotalArticles}}<br>
				Cache Hits (Total): {{.TotalRedisCacheHits}}<br>
				&nbsp;&nbsp;├─ Before CHECK: {{.TotalRedisCacheBeforeCheck}}<br>
				&nbsp;&nbsp;└─ Before TAKETHIS: {{.TotalRedisCacheBeforeTakethis}}<br>
				Checked: {{.TotalChecked}}<br>
				Wanted: {{.TotalWanted}}<br>
				Unwanted: {{.TotalUnwanted}}<br>
				Transferred: {{.TotalTransferred}}<br>
				Rejected: {{.TotalRejected}}<br>
				Retry: {{.TotalRetry}}<br>
				Skipped: {{.TotalSkipped}}<br>
				TX Errors: {{.TotalTXErrors}}<br>
				Conn Errors: {{.TotalConnErrors}}
			{{else}}
				<div class="progress-bar-main" style="max-width: 640px; margin: 10px 0;">
					<div class="progress-fill" style="width: {{if gt .TotalNewsgroups 0}}{{multiply (divide (subtract .TotalNewsgroups .NewsgroupsToProcess) .TotalNewsgroups) 100}}{{else}}0{{end}}%"></div>
					<div class="progress-text">
						<strong><em class="text-light" style="font-size: 24px; color: rgba(15, 238, 234, 1)">{{subtract .TotalNewsgroups .NewsgroupsToProcess}} / {{.TotalNewsgroups}} {{if gt .TotalNewsgroups 0}} @ {{multiply (divide (subtract .TotalNewsgroups .NewsgroupsToProcess) .TotalNewsgroups) 100}}{{else}}0{{end}}%</em></strong>
					</div>
				</div>
				<br>
				<strong>Live Statistics:</strong><br>
				Total Articles: {{.TotalArticles}}<br>
				Cache Hits (Total): {{.TotalRedisCacheHits}}<br>
				&nbsp;&nbsp;├─ Before CHECK: {{.TotalRedisCacheBeforeCheck}}<br>
				&nbsp;&nbsp;└─ Before TAKETHIS: {{.TotalRedisCacheBeforeTakethis}}<br>
				Checked: {{.TotalCheckSentCount}} / {{.TotalChecked}}<br>
				Wanted: {{.TotalWanted}}<br>
				Unwanted: {{.TotalUnwanted}}<br>
				TTSentCount: {{.TotalTTSentCount}}<br>
				Transferred: {{.TotalTransferred}}<br>
				Rejected: {{.TotalRejected}}<br>
				Retry: {{.TotalRetry}}<br>
				Skipped: {{.TotalSkipped}}<br>
				TX Errors: {{.TotalTXErrors}}<br>
				Conn Errors: {{.TotalConnErrors}}
			{{end}}
		{{end}}
	</div>

	{{if .Results}}
		<h2 style="margin-top: 30px;">View <a href="/results">/results</a></h2>
	{{else}}
		<h2 style="margin-top: 30px;">No transfer results yet. Waiting for transfers to complete...</h2>
	{{end}}

	<div class="timestamp">Last updated: {{.Timestamp}}</div>

	{{if .Progress}}
	<!--<h2 style="margin-top: 30px; color: #333;">{{subtract .Started .Finished}} Newsgroups In Progress</h2>-->
	<table class="progress-table">
		<thead>
			<tr>
				<th style="width:55%">NG Workers: ( {{subtract .Started .Finished}} )</th>
				<th style="width:15% text-align: center;">Progress</th>
				<th style="width:15% text-align: center;">Speed{{if gtUint64 .GlobalSpeed 0}}<br><strong>{{.GlobalSpeed}} KByte/s</strong>{{end}}</th>
				<th style="width:15% text-align: center;">{{.LiveCH}} CH/s<br>{{.LiveTT}} TT/s</th>
			</tr>
		</thead>
		<tbody>
			{{range .Progress}}
			<tr>
				<td style="width:55%">
					<strong>{{.Name}}</strong>
					<br>
					<small>
					Started {{.Duration}} ago at {{.Started}} | idle: {{.TimeSince}}
					</small>
				</td>
				<td style="width:15%; text-align: center;">
					{{if gt .TotalArticles 0}}
						<div class="progress-bar" style="margin: 0 auto; max-width: 200px;">
							<div class="progress-fill" style="width: {{if gt .OffsetStart 0}}{{multiply (divide .OffsetStart .TotalArticles) 100}}{{else}}0{{end}}%">
								<small><em class="text-light">
								{{if gt .OffsetStart 0}}{{multiply (divide .OffsetStart .TotalArticles) 100}}{{else}}0{{end}}%
								</em></small>
							</div>
						</div>
					{{else}}
						<em>Initializing...</em>
					{{end}}
				</td>
				<td style="width:15%; text-align: center;">
					{{if gt .TotalArticles 0}}
						<small>{{.OffsetStart}}/{{.TotalArticles}}</small>
					{{end}}
					<br>
					{{if gtUint64 .SpeedKB 0}}
						<small>{{.SpeedKB}} KByte/s</small>
					{{else}}
						<em>-</em>
					{{end}}
				</td>
				<td style="width:15%" style="font-size: 12px;">CH: {{.LastArtPerfC}}/s<br>TT: {{.LastArtPerfT}}/s</td>
			</tr>
			{{end}}
		</tbody>
	</table>
	{{end}}

	{{if .DemuxerStats}}
	<h2 style="margin-top: 30px; color: #333;">Response Demuxer Statistics ({{len .DemuxerStats}} Workers)</h2>
	<small>Workers print only if they have non-empty channels or if last request is longer than 15s ago.</small>
	<table class="progress-table">
		<thead>
			<tr>
				<th style="width:20%">Worker ID</th>
				<th style="width:20%">Pending Commands</th>
				<th style="width:20%">CHECK Responses Queued</th>
				<th style="width:20%">TAKETHIS Responses Queued</th>
				<th style="width:20%">Idle Time (seconds)</th>
			</tr>
		</thead>
		<tbody>
			{{range .DemuxerStats}}
			{{if or (gt .PendingCommands 0) (gt .CheckResponsesQueued 0) (gt .TTResponsesQueued 0) (gt .IdleSeconds 15)}}
			<tr>
				<td style="width:20%; text-align: center;"><strong>Worker #{{.WorkerID}}</strong></td>
				<td style="width:20%; text-align: center;">{{.PendingCommands}}</td>
				<td style="width:20%; text-align: center;">{{.CheckResponsesQueued}}</td>
				<td style="width:20%; text-align: center;">{{.TTResponsesQueued}}</td>
				<td style="width:20%; text-align: center;">{{.IdleSeconds}}</td>
			</tr>
			{{end}}
			{{end}}
		</tbody>
	</table>
	{{end}}

</body>
</html>`

	tmpl, err := template.New("index").Funcs(template.FuncMap{
		"subtract": func(a, b int64) int64 { return a - b },
		"eq":       func(a, b int64) bool { return a == b },
		"gt":       func(a, b int64) bool { return a > b },
		"gtUint64": func(a, b uint64) bool { return a > b },
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
		SpeedKB       uint64
		Duration      string
		TimeSince     string
		LastArtPerfC  uint64
		LastArtPerfT  uint64
	}

	started := int64(len(nntp.NewsgroupTransferProgressMap))
	var finished int64
	var progressList []ProgressInfo
	var liveCH, liveTT uint64
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
			Name:        name,
			OffsetStart: progress.OffsetStart,
			//BatchStart:    progress.BatchStart,
			//BatchEnd:      progress.BatchEnd,
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
		liveCH += progress.LastArtPerfC
		liveTT += progress.LastArtPerfT
		progress.Mux.RUnlock()
	}

	// Sort progress list by newsgroup name for consistent display
	sort.Slice(progressList, func(i, j int) bool {
		return progressList[i].Name < progressList[j].Name
	})

	// Collect demuxer statistics
	type DemuxerStats struct {
		WorkerID             int
		PendingCommands      int64
		CheckResponsesQueued int64
		TTResponsesQueued    int64
		LastRequest          time.Time
		IdleSeconds          int64
	}
	var demuxerStats []DemuxerStats
	DemuxersMutex.RLock()
	for i, demux := range Demuxers {
		if demux != nil {
			pending, checkQueued, ttQueued, lastReq := demux.GetDemuxerStats()
			var idleSeconds int64
			if !lastReq.IsZero() {
				idleSeconds = int64(time.Since(lastReq).Seconds())
			}
			demuxerStats = append(demuxerStats, DemuxerStats{
				WorkerID:             i,
				PendingCommands:      pending,
				CheckResponsesQueued: checkQueued,
				TTResponsesQueued:    ttQueued,
				LastRequest:          lastReq,
				IdleSeconds:          idleSeconds,
			})
		}
	}
	DemuxersMutex.RUnlock()

	data := struct {
		TotalNewsgroups               int64
		NewsgroupsToProcess           int64
		Results                       []string
		Started                       int64
		Finished                      int64
		Progress                      []ProgressInfo
		DemuxerStats                  []DemuxerStats
		Timestamp                     string
		StartDate                     string
		EndDate                       string
		ServerHostName                string
		GlobalSpeed                   uint64
		TotalArticles                 uint64
		TotalRedisCacheHits           uint64
		TotalRedisCacheBeforeCheck    uint64
		TotalRedisCacheBeforeTakethis uint64
		TotalChecked                  uint64
		TotalCheckSentCount           uint64
		TotalUnwanted                 uint64
		TotalWanted                   uint64
		TotalTransferred              uint64
		TotalTTSentCount              uint64
		TotalRejected                 uint64
		TotalRetry                    uint64
		TotalSkipped                  uint64
		TotalTXErrors                 uint64
		TotalConnErrors               uint64
		LiveCH                        uint64
		LiveTT                        uint64
	}{
		TotalNewsgroups:               TotalNewsgroups,
		NewsgroupsToProcess:           NewsgroupsToProcess,
		Results:                       results,
		Started:                       started,
		Finished:                      finished,
		Progress:                      progressList,
		DemuxerStats:                  demuxerStats,
		Timestamp:                     time.Now().Format("2006-01-02 15:04:05"),
		StartDate:                     StartDate,
		EndDate:                       EndDate,
		ServerHostName:                ServerHostName,
		GlobalSpeed:                   GlobalSpeed,
		TotalArticles:                 globalTotalArticles,
		TotalRedisCacheHits:           totalRedisCacheHits,
		TotalRedisCacheBeforeCheck:    totalRedisCacheBeforeCheck,
		TotalRedisCacheBeforeTakethis: totalRedisCacheBeforeTakethis,
		TotalChecked:                  totalChecked,
		TotalCheckSentCount:           totalCheckSentCount,
		TotalWanted:                   totalWanted,
		TotalUnwanted:                 totalUnwanted,
		TotalTransferred:              totalTransferred,
		TotalTTSentCount:              totalTTSentCount,
		TotalRejected:                 totalRejected,
		TotalRetry:                    totalRetry,
		TotalSkipped:                  totalSkipped,
		TotalTXErrors:                 totalTXErrors,
		TotalConnErrors:               totalConnErrors,
		LiveCH:                        liveCH,
		LiveTT:                        liveTT,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Template execution error: %v", err)
	}
}

// handleResults serves the results page as plain text
func handleResults(w http.ResponseWriter, r *http.Request) {
	nntp.ResultsMutex.RLock()
	defer nntp.ResultsMutex.RUnlock()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if len(results) == 0 {
		fmt.Fprintln(w, "No transfer results yet. Waiting for transfers to complete...")
		return
	}

	for _, result := range results {
		fmt.Fprintln(w, result)
	}
}
