// Simple web server demo for go-pugleaf
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/preloader"
	"github.com/go-while/go-pugleaf/internal/processor"
	"github.com/go-while/go-pugleaf/internal/web"
)

var (
	webmutex sync.Mutex

	// command-line flags
	hostnamePath          string
	isleep                int64
	webport               int
	webssl                bool
	withnntp              bool
	withfetch             bool
	webcertFile           string
	webkeyFile            string
	nntptcpport           int
	nntptlsport           int
	nntpcertFile          string
	nntpkeyFile           string
	forceReloadDesc       bool
	importActiveFile      string
	importDescFile        string
	importCreateMissing   bool
	repairWatermarks      bool
	maxSanArtCache        int
	maxSanArtCacheExpiry  int
	maxNGpageCache        int
	maxNGpageCacheExpiry  int
	maxArticleCache       int
	maxArticleCacheExpiry int
	useShortHashLen       int
	rsyncInactiveGroups   string
	rsyncRemoveSource     bool
	//ignoreInitialTinyGroups int64 // code path disabled

	// Migration flags
	updateNewsgroupActivity    bool
	updateNewsgroupsHideFuture bool
	writeActiveFile            string
	writeActiveOnly            bool

	// Compare flags
	compareActiveFile        string
	compareActiveMinArticles int64

	// Bridge flags (disabled by default)
	/* code path disabled (not tested)
	enableFediverse   bool
	fediverseDomain   string
	fediverseBaseURL  string
	enableMatrix      bool
	matrixHomeserver  string
	matrixAccessToken string
	matrixUserID      string
	*/
)

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion

	// Initialize embedded filesystems
	database.SetEmbeddedMigrations(database.EmbeddedMigrationsFS)

	flag.IntVar(&maxSanArtCache, "maxsanartcache", 10000, "maximum number of cached sanitized articles (default: 10000)")
	flag.IntVar(&maxSanArtCacheExpiry, "maxsanartcacheexpiry", 30, "expiry of cached sanitized articles in minutes (default: 30 minutes)")
	flag.IntVar(&maxNGpageCache, "maxngpagecache", 4096, "maximum number of cached newsgroup pages (25 groups per page) (default: 4K pages) [~12-16 KB/entry * 4096 = ~64 MB (+overhead) with 100k active groups!]")
	flag.IntVar(&maxNGpageCacheExpiry, "maxngpagecacheexpiry", 5, "expiry of cached newsgroup pages in minutes (default: 5 minutes)")
	flag.IntVar(&maxArticleCache, "maxarticlecache", 10000, "maximum number of cached articles (default: 10000) [~8-12 KB/entry] 10000 = ~128 MB")
	flag.IntVar(&maxArticleCacheExpiry, "maxarticlecacheexpiry", 60, "expiry of cached articles in minutes (default: 60 minutes)")
	flag.IntVar(&useShortHashLen, "useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
	flag.IntVar(&webport, "webport", 0, "Web server port (default: 11980 (no ssl) or 19443 (webssl))")
	flag.BoolVar(&webssl, "webssl", false, "Enable SSL")
	//flag.BoolVar(&withnntp, "withnntp", false, "Start NNTP server with default ports 1119/1563")
	//flag.BoolVar(&withfetch, "withfetch", false, "Enable internal Cronjob to fetch new articles")
	//flag.Int64Var(&isleep, "isleep", 300, "Sleeps in fetch routines. if started with: -withfetch (default: 300 seconds = 5min)")
	//flag.Int64Var(&ignoreInitialTinyGroups, "ignore-initial-tiny-groups", 0, "If > 0: initial fetch ignores tiny groups with fewer articles than this (default: 0)")
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
	flag.StringVar(&writeActiveFile, "write-active-file", "", "Write NNTP active file from main database newsgroups table to specified path")
	flag.BoolVar(&writeActiveOnly, "write-active-only", true, "use with -write-active-file (false writes only non active groups!)")
	flag.StringVar(&rsyncInactiveGroups, "rsync-inactive-groups", "", "path to new data dir, uses rsync to copy all inactive group databases to new data folder.")
	flag.BoolVar(&rsyncRemoveSource, "rsync-remove-source", false, "use with -rsync-inactive-groups. if set, removes source files after moving inactive groups (default: false)")
	flag.StringVar(&compareActiveFile, "compare-active", "", "Compare active file with database and show missing groups (format: groupname highwater lowwater status)")
	flag.Int64Var(&compareActiveMinArticles, "compare-active-min-articles", 0, "use with -compare-active: only show groups with more than N articles (calculated as high-low)")
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

	// Initialize progress database once to avoid opening/closing for each group
	progressDB, err := database.NewProgressDB("data/progress.db")
	if err != nil {
		log.Fatalf("[WEB]: Failed to initialize progress database: %v", err)
	}
	defer progressDB.Close()

	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate
	//log.Printf("[WEB]: Date parser adapter initialized with processor.ParseNNTPDate")

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

	// Write active file if requested
	if writeActiveFile != "" {
		log.Printf("[WEB]: Writing active file from main database to: %s", writeActiveFile)
		if err := writeActiveFileFromDB(db, writeActiveFile, writeActiveOnly); err != nil {
			log.Printf("[WEB]: Error: Failed to write active file: %v", err)
			os.Exit(1)
		} else {
			log.Printf("[WEB]: Active file written successfully")
			os.Exit(0)
		}
	}

	// rsyncInactiveGroups
	if rsyncInactiveGroups != "" {
		log.Printf("[RSYNC]: inactive groups to: %s", rsyncInactiveGroups)
		if err := rsyncInactiveGroupsToDir(db, rsyncInactiveGroups); err != nil {
			log.Printf("[RSYNC]: Error: Failed to rsync inactive groups: %v", err)
			os.Exit(1)
		} else {
			log.Printf("[RSYNC]: Inactive groups synced successfully")
			os.Exit(0)
		}
	}

	// compareActiveFile
	if compareActiveFile != "" {
		log.Printf("[WEB]: Comparing active file with database: %s (min articles: %d)", compareActiveFile, compareActiveMinArticles)
		if err := compareActiveFileWithDatabase(db, compareActiveFile, compareActiveMinArticles); err != nil {
			log.Printf("[WEB]: Error: Failed to compare active file: %v", err)
			os.Exit(1)
		} else {
			log.Printf("[WEB]: Active file comparison completed successfully")
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
		go FetchRoutine(db, proc, finalUseShortHashLen, true, isleep, DLParChan, progressDB) // Start the processor routine in a separate goroutine
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
