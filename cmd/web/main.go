// Simple web server demo for go-pugleaf
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/preloader"
	"github.com/go-while/go-pugleaf/internal/processor"
	"github.com/go-while/go-pugleaf/internal/web"
)

var (
	appVersion = "-"
	webmutex   sync.Mutex

	// command-line flags
	hostnamePath            string
	isleep                  int64
	webport                 int
	webssl                  bool
	withnntp                bool
	withfetch               bool
	webcertFile             string
	webkeyFile              string
	nntptcpport             int
	nntptlsport             int
	nntpcertFile            string
	nntpkeyFile             string
	forceReloadDesc         bool
	importActiveFile        string
	importDescFile          string
	importCreateMissing     bool
	repairWatermarks        bool
	maxSanArtCache          int
	maxSanArtCacheExpiry    int
	maxNGpageCache          int
	maxNGpageCacheExpiry    int
	maxArticleCache         int
	maxArticleCacheExpiry   int
	useShortHashLen         int
	ignoreInitialTinyGroups int64

	// Migration flags
	updateNewsgroupActivity    bool
	updateNewsgroupsHideFuture bool

	// Bridge flags (disabled by default)
	enableFediverse   bool
	fediverseDomain   string
	fediverseBaseURL  string
	enableMatrix      bool
	matrixHomeserver  string
	matrixAccessToken string
	matrixUserID      string
)

// ProcessorAdapter adapts the processor.Processor to implement nntp.ArticleProcessor interface
type ProcessorAdapter struct {
	processor *processor.Processor
}

// NewProcessorAdapter creates a new processor adapter
func NewProcessorAdapter(proc *processor.Processor) *ProcessorAdapter {
	return &ProcessorAdapter{processor: proc}
}

// ProcessIncomingArticle processes an incoming article
func (pa *ProcessorAdapter) ProcessIncomingArticle(article *models.Article) (int, error) {
	// Forward the Article directly to the processor
	// No conversions needed since both use models.Article
	return pa.processor.ProcessIncomingArticle(article)
}

// Lookup checks if a message-ID exists in history
func (pa *ProcessorAdapter) Lookup(msgIdItem *history.MessageIdItem) (int, error) {
	return pa.processor.History.Lookup(msgIdItem)
}

// CheckNoMoreWorkInHistory checks if there's no more work in history
func (pa *ProcessorAdapter) CheckNoMoreWorkInHistory() bool {
	return pa.processor.CheckNoMoreWorkInHistory()
}

// updateNewsgroupLastActivity updates newsgroups' updated_at field based on their latest article
func updateNewsgroupLastActivity(db *database.Database) error {
	// First, get all newsgroups from the main database
	rows, err := db.GetMainDB().Query("SELECT id, name FROM newsgroups WHERE message_count > 0")
	if err != nil {
		return fmt.Errorf("failed to query newsgroups: %w", err)
	}
	defer rows.Close()

	updatedCount := 0
	var id int
	var name string
	for rows.Next() {
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("error [WEB]: updateNewsgroupLastActivity rows.Scan newsgroup: %v", err)
		}

		// Get the group database for this newsgroup
		groupDBs, err := db.GetGroupDBs(name)
		if err != nil {
			return fmt.Errorf("error [WEB]: updateNewsgroupLastActivity GetGroupDB %s: %v", name, err)

		}

		_, err = database.RetryableExec(groupDBs.DB, "UPDATE articles SET spam = 1 WHERE spam = 0 AND hide = 1", nil)
		if err != nil {
			return fmt.Errorf("failed to query newsgroups: %w", err)
		}

		// Query the latest article date from the group's articles table (excluding hidden articles)
		var latestDate sql.NullString
		err = database.RetryableQueryRowScan(groupDBs.DB, "SELECT MAX(date_sent) FROM articles WHERE hide = 0", nil, &latestDate)
		groupDBs.Return(db) // Always return the database connection
		if err != nil {
			return fmt.Errorf("error [WEB]: updateNewsgroupLastActivity RetryableQueryRowScan %s: %v", name, err)
		}

		// Only update if we found a latest date
		if latestDate.Valid {
			// Parse the date and format it consistently as UTC
			dateStr := latestDate.String
			var parsedDate time.Time
			var err error

			// Clean up malformed timestamps with extra dashes or spaces
			//dateStr = strings.ReplaceAll(dateStr, "- ", " ")
			//dateStr = strings.TrimSpace(dateStr)
			// Try multiple date formats to handle various edge cases
			formats := []string{
				"2006-01-02 15:04:05-07:00",
				"2006-01-02 15:04:05+07:00",
			}

			for _, format := range formats {
				parsedDate, err = time.Parse(format, dateStr)
				if err == nil {
					break
				}
			}

			if err != nil {
				return fmt.Errorf("error [WEB]: updateNewsgroupLastActivity parsing date '%s' for %s: %v", dateStr, name, err)
			}

			// Format as UTC without timezone info to match db_batch.go format
			formattedDate := parsedDate.UTC().Format("2006-01-02 15:04:05")
			_, err = db.GetMainDB().Exec("UPDATE newsgroups SET updated_at = ? WHERE id = ?", formattedDate, id)
			if err != nil {
				log.Printf("[WEB]: error updateNewsgroupLastActivity updating newsgroup %s: %v", name, err)
				continue
			}
			log.Printf("[WEB]: updateNewsgroupLastActivity: '%s' dateStr=%s formattedDate=%s", name, dateStr, formattedDate)
			updatedCount++
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error updateNewsgroupLastActivity iterating newsgroups: %w", err)
	}

	log.Printf("[WEB]: updateNewsgroupLastActivity updated %d newsgroups", updatedCount)
	return nil
}

// hideFuturePosts updates articles' hide field to 1 if they are posted more than 48 hours in the future
func hideFuturePosts(db *database.Database) error {
	// Calculate the cutoff time (current time + 48 hours)
	cutoffTime := time.Now().Add(48 * time.Hour)

	// First, get all newsgroups from the main database
	rows, err := db.GetMainDB().Query("SELECT id, name FROM newsgroups WHERE message_count > 0")
	if err != nil {
		return fmt.Errorf("failed to query newsgroups: %w", err)
	}
	defer rows.Close()

	updatedArticles := 0
	processedGroups := 0
	skippedGroups := 0

	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			log.Printf("[WEB]: Future posts migration error scanning newsgroup: %v", err)
			continue
		}

		// Get the group database for this newsgroup
		groupDBs, err := db.GetGroupDBs(name)
		if err != nil {
			log.Printf("[WEB]: Future posts migration error getting group DB for %s: %v", name, err)
			skippedGroups++
			continue
		}

		// Update articles that are posted more than 48 hours in the future
		result, err := database.RetryableExec(groupDBs.DB, "UPDATE articles SET hide = 1, spam = 1 WHERE date_sent > ? AND hide = 0", cutoffTime.Format("2006-01-02 15:04:05"))
		groupDBs.Return(db) // Always return the database connection

		if err != nil {
			log.Printf("[WEB]: Future posts migration error updating articles for %s: %v", name, err)
			skippedGroups++
			continue
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			log.Printf("[WEB]: Future posts migration error getting rows affected for %s: %v", name, err)
			skippedGroups++
			continue
		}

		if rowsAffected > 0 {
			log.Printf("[WEB]: Hidden %d future posts in newsgroup %s", rowsAffected, name)
			updatedArticles += int(rowsAffected)
		}
		processedGroups++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating newsgroups: %w", err)
	}

	log.Printf("[WEB]: Future posts migration completed: processed %d groups, hidden %d articles, skipped %d groups", processedGroups, updatedArticles, skippedGroups)
	return nil
}

func main() {
	config.AppVersion = appVersion

	flag.Int64Var(&isleep, "isleep", 300, "Sleeps in fetch routines. if started with: -withfetch (default: 300 seconds = 5min)")
	flag.IntVar(&maxSanArtCache, "maxsanartcache", 10000, "maximum number of cached sanitized articles (default: 10000)")
	flag.IntVar(&maxSanArtCacheExpiry, "maxsanartcacheexpiry", 30, "expiry of cached sanitized articles in minutes (default: 30 minutes)")
	flag.IntVar(&maxNGpageCache, "maxngpagecache", 4096, "maximum number of cached newsgroup pages (25 groups per page) (default: 4K pages) [~12-16 KB/entry * 4096 = ~64 MB (+overhead) with 100k active groups!]")
	flag.IntVar(&maxNGpageCacheExpiry, "maxngpagecacheexpiry", 5, "expiry of cached newsgroup pages in minutes (default: 5 minutes)")
	flag.IntVar(&maxArticleCache, "maxarticlecache", 10000, "maximum number of cached articles (default: 10000) [~8-12 KB/entry] 10000 = ~128 MB")
	flag.IntVar(&maxArticleCacheExpiry, "maxarticlecacheexpiry", 60, "expiry of cached articles in minutes (default: 60 minutes)")
	flag.IntVar(&useShortHashLen, "useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
	flag.IntVar(&webport, "webport", 0, "Web server port (default: 11980 (no ssl) or 19443 (webssl))")
	flag.Int64Var(&ignoreInitialTinyGroups, "ignore-initial-tiny-groups", 0, "If > 0: initial fetch ignores tiny groups with fewer articles than this (default: 0)")
	flag.BoolVar(&webssl, "webssl", false, "Enable SSL")
	//flag.BoolVar(&withnntp, "withnntp", false, "Start NNTP server with default ports 1119/1563")
	//flag.BoolVar(&withfetch, "withfetch", false, "Enable internal Cronjob to fetch new articles")
	flag.StringVar(&hostnamePath, "nntphostname", "", "your hostname must be set")
	flag.StringVar(&webcertFile, "websslcert", "", "SSL certificate file (/path/to/fullchain.pem)")
	flag.StringVar(&webkeyFile, "websslkey", "", "SSL key file (/path/to/privkey.pem)")
	flag.IntVar(&nntptcpport, "nntptcpport", 0, "NNTP TCP port")
	flag.IntVar(&nntptlsport, "nntptlsport", 0, "NNTP TLS port")
	flag.StringVar(&nntpcertFile, "nntpcertfile", "", "NNTP TLS certificate file (/path/to/fullchain.pem)")
	flag.StringVar(&nntpkeyFile, "nntpkeyfile", "", "NNTP TLS key file (/path/to/privkey.pem)")
	flag.BoolVar(&forceReloadDesc, "update-descr", false, "Updates (overwrites existing!) internal newsgroup descriptions from file preload/newsgroups.descriptions (default: false)")
	flag.StringVar(&importActiveFile, "import-active", "", "Import newsgroups from NNTP active file (format: groupname highwater lowwater status)")
	flag.StringVar(&importDescFile, "import-desc", "", "Import newsgroups from descriptions file (format: groupname\\tdescription)")
	flag.BoolVar(&importCreateMissing, "import-create", false, "Create missing newsgroups when importing from descriptions file (default: false)")
	flag.BoolVar(&repairWatermarks, "repair-watermarks", false, "Repair corrupted newsgroup watermarks caused by preloader (default: false)")
	flag.BoolVar(&updateNewsgroupActivity, "update-newsgroup-activity", false, "Updates newsgroup updated_at timestamps to reflect actual article activity (default: false)")
	flag.BoolVar(&updateNewsgroupsHideFuture, "update-newsgroups-hide-futureposts", false, "Hide articles posted more than 48 hours in the future (default: false)")
	/*
		flag.BoolVar(&enableFediverse, "enable-fediverse", false, "Enable Fediverse bridge (default: false)")
		flag.StringVar(&fediverseDomain, "fediverse-domain", "", "Fediverse domain (e.g. example.com)")
		flag.StringVar(&fediverseBaseURL, "fediverse-baseurl", "", "Fediverse base URL (e.g. https://example.com)")
		flag.BoolVar(&enableMatrix, "enable-matrix", false, "Enable Matrix bridge (default: false)")
		flag.StringVar(&matrixHomeserver, "matrix-homeserver", "", "Matrix homeserver URL (e.g. https://matrix.org)")
		flag.StringVar(&matrixAccessToken, "matrix-accesstoken", "", "Matrix access token")
		flag.StringVar(&matrixUserID, "matrix-userid", "", "Matrix user ID")
	*/
	flag.Parse()
	mainConfig := config.NewDefaultConfig()
	appVersion = mainConfig.AppVersion
	log.Printf("Starting go-pugleaf: Web Server NNTP=%t Fetch=%t (version: %s)", withnntp, withfetch, appVersion)

	// Debug: Log parsed flags
	log.Printf("[WEB]: Web Parsed flags - port: %d, ssl: %t, cert: %s, key: %s", webport, webssl, webcertFile, webkeyFile)
	log.Printf("[WEB]: NNTP Parsed flags - tcpport: %d, tlsport: %d, cert: %s, key: %s", nntptcpport, nntptlsport, nntpcertFile, nntpkeyFile)

	// Load configuration from file or use defaults
	//var webConfig *config.WebConfig
	webConfig := mainConfig.Server.WEB
	//nntpConfig := mainConfig.Server.NNTP
	if webConfig.ListenPort == 0 {
		log.Printf("[WEB]: Error loading default webconfig..")
	}
	if mainConfig.Server.NNTP.Port == 0 || mainConfig.Server.NNTP.TLSPort == 0 {
		log.Fatalf("[WEB]: Error loading default nntpconfig..")
	}
	// Debug: Log default config
	log.Printf("[WEB]: Default config loaded - port: %d, ssl: %t", webConfig.ListenPort, webConfig.SSL)

	// Override config with command-line flags if provided
	if webport > 0 {
		webConfig.ListenPort = webport
		log.Printf("[WEB]: Overriding listen port with command-line flag: %d", webConfig.ListenPort)
	} else {
		log.Printf("[WEB]: No port flag provided, using default: %d", webConfig.ListenPort)
	}
	if webssl {
		webConfig.SSL = true
		log.Printf("[WEB]: SSL enabled via command-line flag")
	}
	if webcertFile != "" {
		webConfig.CertFile = webcertFile
		log.Printf("[WEB]: SSL cert file set: %s", webConfig.CertFile)
	}
	if webkeyFile != "" {
		webConfig.KeyFile = webkeyFile
		log.Printf("[WEB]: SSL key file set: %s", webConfig.KeyFile)
	}
	log.Printf("[WEB]: Using WEB configuration: %#v", webConfig)

	// Override config with command-line flags if provided
	if withnntp && nntptcpport > 0 {
		mainConfig.Server.NNTP.Port = nntptcpport
		log.Printf("[WEB]: Overriding NNTP TCP port with command-line flag: %d", mainConfig.Server.NNTP.Port)
	} else {
		log.Printf("[WEB]: No NNTP TCP port flag provided")
		mainConfig.Server.NNTP.Port = 0
	}
	if withnntp && nntptlsport > 0 {
		mainConfig.Server.NNTP.TLSPort = nntptlsport
		mainConfig.Server.NNTP.TLSCert = nntpcertFile
		mainConfig.Server.NNTP.TLSKey = nntpkeyFile
	} else {
		mainConfig.Server.NNTP.TLSPort = 0
		mainConfig.Server.NNTP.TLSCert = ""
		mainConfig.Server.NNTP.TLSKey = ""
		log.Printf("[WEB]: No NNTP TLS port flag provided")
	}

	if hostnamePath == "" && (withfetch || withnntp) {
		log.Fatalf("[WEB]: Error: hostname must be set when starting with -withfetch or -withnntp")
	}
	mainConfig.Server.Hostname = hostnamePath
	processor.LocalHostnamePath = hostnamePath
	log.Printf("[WEB]: Using NNTP configuration %#v", mainConfig.Server.NNTP)

	// Validate port
	if webConfig.ListenPort < 1024 || webConfig.ListenPort > 65535 {
		log.Fatalf("[WEB]: Invalid port number: %d (must be between 1024 and 65535)", webConfig.ListenPort)
	}
	// Validate port
	if mainConfig.Server.NNTP.Port > 0 {
		if mainConfig.Server.NNTP.Port < 1024 || mainConfig.Server.NNTP.Port > 65535 {
			log.Fatalf("[WEB]: Invalid NNTP tcp port number: %d (must be between 1024 and 65535)", mainConfig.Server.NNTP.Port)
		}
	}
	// Validate port
	if mainConfig.Server.NNTP.TLSPort > 0 {
		if mainConfig.Server.NNTP.TLSPort < 1024 || mainConfig.Server.NNTP.TLSPort > 65535 {
			log.Fatalf("[WEB]: Invalid NNTP tls port number: %d (must be between 1024 and 65535)", mainConfig.Server.NNTP.TLSPort)
		}
	}
	/*
		// Check for environment variable override
		if portEnv := os.Getenv("PUGLEAF_WEB_PORT"); portEnv != "" {
			if p, err := strconv.Atoi(portEnv); err == nil {
				if p < 1024 || p > 65535 {
					log.Fatalf("[WEB]: Invalid port number in PUGLEAF_WEB_PORT: %s (must be between 1024 and 65535)", portEnv)
				}
				webConfig.ListenPort = p
				log.Printf("[WEB]: Port overridden by environment variable: %d", p)
			}
		}
	*/
	protocol := "http"
	if webConfig.SSL {
		protocol = "https"
	}
	log.Printf("[WEB]: Starting go-pugleaf web server on %s://localhost:%d", protocol, webConfig.ListenPort)

	// Initialize database with custom cache configuration
	dbConfig := database.DefaultDBConfig() // Start with defaults
	// Override cache settings with command-line flag values
	dbConfig.ArticleCacheSize = maxArticleCache
	dbConfig.ArticleCacheExpiry = time.Duration(maxArticleCacheExpiry) * time.Minute

	db, err := database.OpenDatabase(nil) // Pass nil to trigger first-boot logic
	if err != nil {
		log.Fatalf("[WEB]: Failed to initialize database: %v", err)
	}

	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate
	//log.Printf("[WEB]: Date parser adapter initialized with processor.ParseNNTPDate")

	// Initialize caches after database is loaded (using command-line flag values)
	db.ArticleCache = database.NewArticleCache(maxArticleCache, time.Duration(maxArticleCacheExpiry)*time.Minute)
	//log.Printf("[WEB]: Article cache initialized (max %d articles, %v expiry)", maxArticleCache, time.Duration(maxArticleCacheExpiry)*time.Minute)

	// Initialize NNTP authentication cache (15 minute TTL)
	db.NNTPAuthCache = database.NewNNTPAuthCache(15 * time.Minute)
	//log.Printf("[WEB]: NNTP authentication cache initialized (15 minute TTL)")

	// Note: Database batch workers are started automatically by OpenDatabase()
	db.WG.Add(2) // Adds to wait group for db_batch.go cron jobs
	db.WG.Add(1) // Adds for history: one for writer worker

	// Apply main database migrations
	if err := db.Migrate(); err != nil {
		log.Fatalf("[WEB]: Failed to apply database migrations: %v", err)
	}
	//log.Printf("[WEB]: Database migrations applied successfully")

	// Run future posts hiding migration first if requested
	if updateNewsgroupsHideFuture {
		log.Printf("[WEB]: Starting future posts hiding migration...")
		if err := hideFuturePosts(db); err != nil {
			log.Printf("[WEB]: Warning: Future posts hiding migration failed: %v", err)
			os.Exit(1)
		} else {
			log.Printf("[WEB]: Future posts hiding migration completed successfully")
			if !updateNewsgroupActivity {
				os.Exit(0)
			}
		}
	}

	// Run newsgroup activity migration after hiding future posts if requested
	if updateNewsgroupActivity {
		log.Printf("[WEB]: Starting newsgroup activity migration...")
		if err := updateNewsgroupLastActivity(db); err != nil {
			log.Printf("[WEB]: Warning: Newsgroup activity migration failed: %v", err)
			os.Exit(1)
		} else {
			log.Printf("[WEB]: Newsgroup activity migration completed successfully")
			os.Exit(0)
		}
	}

	// Get or set history UseShortHashLen configuration
	finalUseShortHashLen, isLocked, err := db.GetHistoryUseShortHashLen(useShortHashLen)
	if err != nil {
		log.Fatalf("[WEB]: Failed to get history configuration: %v", err)
	}

	if !isLocked {
		// First time setup - store the command-line value
		if err := db.SetHistoryUseShortHashLen(useShortHashLen); err != nil {
			log.Fatalf("[WEB]: Failed to set history configuration: %v", err)
		}
		finalUseShortHashLen = useShortHashLen
		log.Printf("[WEB]: History UseShortHashLen initialized to %d", finalUseShortHashLen)
	} else {
		// Already configured - use stored value and warn if different
		if useShortHashLen != finalUseShortHashLen {
			log.Printf("[WEB]: WARNING: Command-line UseShortHashLen (%d) differs from locked database value (%d). Using database value to prevent data corruption.", useShortHashLen, finalUseShortHashLen)
		} else {
			log.Printf("[WEB]: Using stored history UseShortHashLen: %d", finalUseShortHashLen)
		}
	}

	// Initialize sanitized content cache (N entries max, M minute expiry)
	models.InitSanitizedCache(maxSanArtCache, time.Duration(maxSanArtCacheExpiry)*time.Minute)
	//log.Printf("[WEB]: Sanitized content cache initialized")

	// Initialize newsgroup cache (N page results max, M minute expiry)
	models.InitNewsgroupCache(maxNGpageCache, time.Duration(maxNGpageCacheExpiry)*time.Minute)
	//log.Printf("[WEB]: Newsgroup cache initialized")

	// Handle newsgroup import/update operations
	ctx := context.Background()

	if importActiveFile != "" || importDescFile != "" {
		// Import newsgroups from files
		log.Printf("[WEB]: Importing newsgroups from files...")
		if err := preloader.LoadNewsgroupsFromFiles(ctx, db, importActiveFile, importDescFile, importCreateMissing); err != nil {
			log.Printf("[WEB]: Warning: Failed to import newsgroups: %v", err)
			os.Exit(1)
		} else {
			log.Printf("[WEB]: Newsgroups imported successfully")
			if importActiveFile != "" {
				// only quit when importing via active files, not when updateting descriptions
				os.Exit(0)
			}
		}
	}

	if repairWatermarks {
		log.Printf("[WEB]: Repairing corrupted newsgroup watermarks...")
		if err := preloader.RepairNewsgroupWatermarks(ctx, db); err != nil {
			log.Printf("[WEB]: Warning: Failed to repair watermarks: %v", err)
			os.Exit(1)
		} else {
			log.Printf("[WEB]: Watermarks repaired successfully")
			os.Exit(0)
		}
	}

	if forceReloadDesc {
		// Load newsgroup descriptions from preload file
		descFile := filepath.Join("preload", "newsgroups.descriptions")
		if _, err := os.Stat(descFile); err == nil {
			log.Printf("[WEB]: Loading newsgroup descriptions from %s...", descFile)
			if err := preloader.LoadNewsgroupDescriptions(ctx, db, descFile); err != nil {
				log.Printf("[WEB]: Warning: Failed to load newsgroup descriptions: %v", err)
			} else {
				log.Printf("[WEB]: Newsgroup descriptions loaded successfully")
			}
		} else {
			log.Printf("[WEB]: Newsgroup descriptions file not found at %s, skipping", descFile)
		}
	}

	// Check if we have any groups
	groups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		log.Printf("[WEB]: Warning: Could not fetch newsgroups: %v", err)
	} else {
		log.Printf("[WEB]: Found %d newsgroups in database", len(groups))
	}

	// Only create processor if integrated fetcher or nntp-server is enabled
	var proc *processor.Processor
	if withfetch || withnntp {
		proc = NewFetchProcessor(db) // Create a new processor instance
		if proc == nil {
			log.Printf("[WEB]: ERROR: No enabled providers found! Cannot proceed with article fetching")
		} else {
			log.Printf("[WEB]: Using first enabled provider for fetching articles")
		}
	}

	var nntpServer *nntp.NNTPServer
	if (nntptcpport > 0 || nntptlsport > 0) && withnntp {
		if proc == nil {
			log.Fatalf("[WEB]: Cannot start NNTP server without a processor (no enabled providers found)")
		}
		processorAdapter := NewProcessorAdapter(proc)
		log.Printf("[WEB]: Starting NNTP server with TCP port %d, TLS port %d", mainConfig.Server.NNTP.Port, mainConfig.Server.NNTP.TLSPort)
		nntpServer, err = nntp.NewNNTPServer(db, &mainConfig.Server, db.WG, processorAdapter)
		if err != nil {
			log.Fatalf("[WEB]: Failed to create NNTP server: %v", err)
		}
		if err := nntpServer.Start(); err != nil {
			log.Fatalf("[WEB]: Failed to start NNTP server: %v", err)
		}
	}

	if withfetch && proc != nil {
		DownloadMaxPar := 1
		DLParChan := make(chan struct{}, DownloadMaxPar)
		go FetchRoutine(db, proc, finalUseShortHashLen, true, isleep, DLParChan) // Start the processor routine in a separate goroutine
	}

	// Create and start web server in a goroutine for non-blocking startup
	server := web.NewServer(db, webConfig, nntpServer)

	// Set up cross-platform signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt) // Cross-platform (Ctrl+C on both Windows and Linux)

	log.Printf("[WEB]: Starting web server...")

	// Start web server in goroutine to make it non-blocking
	webServerErrChan := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			webServerErrChan <- err
		}
	}()

	log.Printf("[WEB]: Server started successfully. Press Ctrl+C to gracefully shutdown...")

	// Start update file monitor in a separate goroutine
	updateFileChan := make(chan bool, 1)
	go monitorUpdateFile(updateFileChan)
	go db.UpdateHeartbeat()      // Start heartbeat updater in the background
	go startHierarchyUpdater(db) // Start hierarchy last_updated synchronizer in the background
	// Wait for either shutdown signal, server error, or update file
	select {
	case <-sigChan:
		log.Printf("[WEB]: Received shutdown signal, initiating graceful shutdown...")
	case err := <-webServerErrChan:
		log.Fatalf("[WEB]: Failed to start web server: %v", err)
	case <-updateFileChan:
		log.Printf("[WEB]: Update file detected, initiating graceful shutdown for update...")
	}

	// Stop NNTP server if running
	if nntpServer != nil {
		log.Printf("[WEB]: Stopping NNTP server...")
		if err := nntpServer.Stop(); err != nil {
			log.Printf("[WEB]: Error stopping NNTP server: %v", err)
		} else {
			log.Printf("[WEB]: NNTP server stopped successfully")
		}
	}
	// Signal background tasks to stop
	close(db.StopChan)

	// Close the proc/processor (flushes history, stops processing)
	if proc != nil {
		if err := proc.Close(); err != nil {
			log.Printf("[RSLIGHT-IMPORT] Warning: Failed to close proc: %v", err)
		} else {
			log.Printf("[RSLIGHT-IMPORT] proc/processor closed successfully")
		}
	}

	if withfetch || withnntp {
		log.Printf("[WEB]: Signaling background tasks to stop...")
		// Notify orchestrator to send shutdown signals to workers
		//go db.Batch.Shutdown()
		// Wait for all database operations to complete
		log.Printf("[WEB]: Waiting for background tasks to finish...")
		db.WG.Wait()
		log.Printf("[WEB]: All background tasks completed, shutting down database...")
	}

	if err := db.Shutdown(); err != nil {
		log.Fatalf("[WEB]: Failed to shutdown database: %v", err)
	} else {
		log.Printf("[WEB]: Database shutdown successfully")
	}

	log.Printf("[WEB]: Graceful shutdown completed")
} // end main

// startHierarchyUpdater runs a background job every N minutes to update
// hierarchy last_updated fields based on their child newsgroups
func startHierarchyUpdater(db *database.Database) {
	// Run immediately on startup
	if err := db.UpdateHierarchiesLastUpdated(); err != nil {
		log.Printf("[WEB]: Initial hierarchy update failed: %v", err)
	} else {
		// Update the hierarchy cache with new last_updated values
		if db.HierarchyCache != nil {
			if err := db.HierarchyCache.UpdateHierarchyLastUpdated(db); err != nil {
				log.Printf("[WEB]: Initial hierarchy cache update failed: %v", err)
			} else {
				log.Printf("[WEB]: Initial hierarchy cache updated successfully")
			}
		}
	}
	log.Printf("[WEB]: Hierarchy updater started, will sync hierarchy last_updated every 30 minutes")

	for {
		time.Sleep(10 * time.Minute)
		if err := db.UpdateHierarchiesLastUpdated(); err != nil {
			log.Printf("[WEB]: Hierarchy update failed: %v", err)
		} else {
			// Update the hierarchy cache with new last_updated values
			if db.HierarchyCache != nil {
				if err := db.HierarchyCache.UpdateHierarchyLastUpdated(db); err != nil {
					log.Printf("[WEB]: Hierarchy cache update failed: %v", err)
				} else {
					log.Printf("[WEB]: Hierarchy cache updated successfully")
				}
			}
		}
	}
}

// monitorUpdateFile checks for the existence of an .update file every 30 seconds
// and signals for shutdown when found, then removes the file
func monitorUpdateFile(shutdownChan chan<- bool) {
	updateFilePath := ".update"
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	log.Printf("[WEB]: Update file monitor started, checking for '%s' every 60 seconds", updateFilePath)

	for range ticker.C {

		// Check if .update file exists
		if _, err := os.Stat(updateFilePath); err == nil {
			log.Printf("[WEB]: Update file '%s' detected, triggering graceful shutdown", updateFilePath)

			// Rename the update file
			if err := os.Rename(updateFilePath, updateFilePath+".todo"); err != nil {
				log.Printf("[WEB]: Warning: Failed to rename update file '%s': %v", updateFilePath, err)
				continue
			} else {
				log.Printf("[WEB]: Update file '%s' renamed successfully", updateFilePath)
			}

			// Signal shutdown
			select {
			case shutdownChan <- true:
				log.Printf("[WEB]: Shutdown signal sent via update file monitor")
			default:
				log.Printf("[WEB]: Shutdown channel already signaled")
			}
			return
		}
		// File doesn't exist, continue monitoring
	}
}

func ConnectPools(db *database.Database) []*nntp.Pool {
	log.Printf("[WEB]: Fetching providers from database...")
	providers, err := db.GetProviders()
	if err != nil {
		log.Fatalf("[WEB]: Failed to fetch providers: %v", err)
	}
	log.Printf("[WEB]: Found %d providers in database", len(providers))
	pools := make([]*nntp.Pool, 0, len(providers))
	log.Printf("[WEB]: Create provider connection pools...")
	enabledProviders := 0
	for _, p := range providers {
		if !p.Enabled || p.Host == "" || p.Port <= 0 {
			log.Printf("[WEB]: Skipping disabled/invalid provider: %s (ID: %d, Enabled: %v, Host: '%s', Port: %d)",
				p.Name, p.ID, p.Enabled, p.Host, p.Port)
			continue // Skip disabled providers
		}
		enabledProviders++
		// Convert models.Provider to config.Provider for the BackendConfig
		configProvider := &config.Provider{
			Grp:        p.Grp,
			Name:       p.Name,
			Host:       p.Host,
			Port:       p.Port,
			SSL:        p.SSL,
			Username:   p.Username,
			Password:   p.Password,
			MaxConns:   p.MaxConns,
			Enabled:    p.Enabled,
			Priority:   p.Priority,
			MaxArtSize: p.MaxArtSize,
		}

		backendConfig := &nntp.BackendConfig{
			Host:     p.Host,
			Port:     p.Port,
			SSL:      p.SSL,
			Username: p.Username,
			Password: p.Password,
			//ConnectTimeout: 9 * time.Second,
			//ReadTimeout:    60 * time.Second,
			//WriteTimeout:   60 * time.Second,
			MaxConns: p.MaxConns,
			Provider: configProvider, // Set the Provider field
		}

		pool := nntp.NewPool(backendConfig)
		pool.StartCleanupWorker(5 * time.Second)
		pools = append(pools, pool)
		log.Printf("[WEB]: Using only first enabled provider '%s' (TODO: support multiple providers)", p.Name)
		break // For now, we only use the first enabled provider!!! TODO
	}
	log.Printf("[WEB]: %d providers, %d enabled, using %d pools", len(providers), enabledProviders, len(pools))
	return pools
}

func NewFetchProcessor(db *database.Database) *processor.Processor {
	var pool *nntp.Pool
	pools := ConnectPools(db) // Get NNTP pools
	if len(pools) == 0 {
		log.Printf("[WEB]: ERROR: No enabled providers found! Cannot proceed with article fetching")
		pool = nil
	} else {
		pool = pools[0]
	}
	log.Printf("[WEB]: Creating processor instance with useShortHashLen=%d...", useShortHashLen)
	proc := processor.NewProcessor(db, pool, useShortHashLen) // Create a new processor instance
	log.Printf("[WEB]: Processor created successfully")
	return proc
}

func FetchRoutine(db *database.Database, proc *processor.Processor, useShortHashLen int, boot bool, isleep int64, DLParChan chan struct{}) {
	if isleep < 15 {
		isleep = 15 // min 15 sec sleep!
	}
	startTime := time.Now()
	log.Printf("[WEB]: FetchRoutine STARTED (boot=%v, useShortHashLen=%d) at %v", boot, useShortHashLen, startTime)

	webmutex.Lock()
	log.Printf("[WEB]: Acquired webmutex lock")
	defer func() {
		webmutex.Unlock()
		log.Printf("[WEB]: Released webmutex lock")
	}()

	if boot {
		log.Printf("[WEB]: Boot mode detected - waiting %v before starting...", isleep)
		select {
		case <-db.StopChan:
			log.Printf("[WEB]: Shutdown detected during boot wait, exiting FetchRoutine")
			return
		case <-time.After(time.Duration(isleep) * time.Second):
			log.Printf("[WEB]: Boot wait completed, starting article fetching")
		}
	}
	log.Printf("[WEB]: Begin article fetching process")

	defer func(isleep int64) {
		duration := time.Since(startTime)
		log.Printf("[WEB]: FetchRoutine COMPLETED after %v", duration)
		if isleep > 30 {
			proc.Pool.ClosePool() // Close the NNTP pool if sleep is more than 30 seconds
		}
		// Check if shutdown was requested before scheduling restart
		select {
		case <-db.StopChan:
			log.Printf("[WEB]: Shutdown detected, not restarting FetchRoutine")
			return
		default:
			// pass
		}
		// Sleep with shutdown check
	wait:
		for {
			select {
			case <-db.StopChan:
				log.Printf("[WEB]: Shutdown detected during sleep, not restarting FetchRoutine")
				return
			case <-time.After(time.Duration(isleep) * time.Second):
				break wait
			}
		}
		pools := ConnectPools(db) // Reconnect pools after sleep
		if len(pools) == 0 {
			log.Printf("[WEB]: ERROR: No enabled providers found after sleep! Cannot proceed with article fetching")
		} else {
			proc.Pool = pools[0] // Use the first pool
		}
		log.Printf("[WEB]: Sleep completed, starting new FetchRoutine goroutine")
		go FetchRoutine(db, proc, useShortHashLen, false, isleep, DLParChan)
		log.Printf("[WEB]: New FetchRoutine goroutine launched")
	}(isleep)

	log.Printf("[WEB]: Fetching newsgroups from database...")
	groups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		log.Printf("[WEB]: FATAL: Could not fetch newsgroups: %v", err)
		log.Printf("[WEB]: Warning: Could not fetch newsgroups: %v", err)
		return
	}

	log.Printf("[WEB]: Found %d newsgroups in database", len(groups))

	// Check for data integrity issues
	emptyNameCount := 0
	for _, group := range groups {
		if group.Name == "" {
			emptyNameCount++
			log.Printf("[WEB]: WARNING: Found newsgroup with empty name (ID: %d)", group.ID)
		}
	}
	if emptyNameCount > 0 {
		log.Printf("[WEB]: WARNING: Found %d newsgroups with empty names - this indicates a data integrity issue", emptyNameCount)
	}

	// Configure bridges if enabled
	if enableFediverse || enableMatrix {
		bridgeConfig := &processor.BridgeConfig{
			FediverseEnabled:  enableFediverse,
			FediverseDomain:   fediverseDomain,
			FediverseBaseURL:  fediverseBaseURL,
			MatrixEnabled:     enableMatrix,
			MatrixHomeserver:  matrixHomeserver,
			MatrixAccessToken: matrixAccessToken,
			MatrixUserID:      matrixUserID,
		}
		proc.EnableBridges(bridgeConfig)
		log.Printf("[WEB]: Bridge configuration applied")
	} else {
		log.Printf("[WEB]: No bridges enabled (use --enable-fediverse or --enable-matrix to enable)")
	}

	log.Printf("[WEB]: Starting to process %d newsgroups...", len(groups))
	processedCount := 0
	upToDateCount := 0
	errorCount := 0

	for i, group := range groups {
		// Check for shutdown before processing each group
		select {
		case <-db.StopChan:
			log.Printf("[WEB]: Shutdown detected, stopping newsgroup processing at group %d/%d", i+1, len(groups))
			return
		default:
			// Continue processing
		}

		// Skip groups with empty names
		if group.Name == "" {
			log.Printf("[WEB]: [%d/%d] Skipping group with empty name (ID: %d)", i+1, len(groups), group.ID)
			errorCount++
			continue
		}

		log.Printf("[WEB]: [%d/%d] Processing group: %s (ID: %d)", i+1, len(groups), group.Name, group.ID)

		// Register newsgroup with bridges if enabled
		if proc.BridgeManager != nil {
			if err := proc.BridgeManager.RegisterNewsgroup(group); err != nil {
				log.Printf("web.main.go: [%d/%d] Warning: Failed to register group %s with bridges: %v", i+1, len(groups), group.Name, err)
			}
		}

		// Check if the group is in sections DB
		err := proc.DownloadArticles(group.Name, ignoreInitialTinyGroups, DLParChan)
		if err != nil {
			if err.Error() == "up2date" {
				log.Printf("[WEB]: [%d/%d] Group %s is up to date, skipping", i+1, len(groups), group.Name)
				upToDateCount++
			} else {
				log.Printf("[WEB]: [%d/%d] ERROR processing group %s: %v", i+1, len(groups), group.Name, err)
				errorCount++

				// If we're getting too many consecutive errors, it might be an auth issue
				if errorCount > 10 && (processedCount+upToDateCount) == 0 {
					log.Printf("[WEB]: WARNING: Too many consecutive errors (%d), this might indicate authentication or connection issues", errorCount)
				}
			}
		} else {
			log.Printf("[WEB]: [%d/%d] Successfully processed group %s", i+1, len(groups), group.Name)
			processedCount++
		}
		log.Printf("[WEB]: Progress: processed=%d, up-to-date=%d, errors=%d, remaining=%d",
			processedCount, upToDateCount, errorCount, len(groups)-(i+1))
	}
	log.Printf("[WEB]: Finished processing all %d groups", len(groups))
	log.Printf("[WEB]: FINAL SUMMARY - Total groups: %d, Successfully processed: %d, Up-to-date: %d, Errors: %d",
		len(groups), processedCount, upToDateCount, errorCount)
}
