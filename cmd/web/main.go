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
	"syscall"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/preloader"
	"github.com/go-while/go-pugleaf/internal/processor"
	"github.com/go-while/go-pugleaf/internal/web"
)

var (

	// command-line flags
	nntphostname          string
	webport               int
	webssl                bool
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
	editCronjobs          bool
	noCronjobs            bool
	findOrphanDBs         bool
	verbose               bool
	dataDir               string
	useHistory            bool
	//ignoreInitialTinyGroups int64 // code path disabled

	// Migration flags
	updateNGAConcurrent        int
	updateNewsgroupsHideFuture bool
	writeActiveFile            string
	writeActiveOnly            bool

	// Compare flags
	compareActiveFile        string
	compareActiveMinArticles int64
	disableActiveFile        string
	disableActiveDryRun      bool

	// Expiry update flag
	updateNewsgroupsExpiryFile string

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
	models.DisableSanitizedCache = false

	// Initialize embedded filesystems
	database.SetEmbeddedMigrations(database.EmbeddedMigrationsFS)

	flag.IntVar(&maxSanArtCache, "maxsanartcache", 10000, "maximum number of cached sanitized articles (default: 10000)")
	flag.IntVar(&maxSanArtCacheExpiry, "maxsanartcacheexpiry", 30, "expiry of cached sanitized articles in minutes (default: 30 minutes)")
	flag.IntVar(&maxNGpageCache, "maxngpagecache", 4096, "maximum number of cached newsgroup pages (default: 4K pages)")
	flag.IntVar(&maxNGpageCacheExpiry, "maxngpagecacheexpiry", 5, "expiry of cached newsgroup pages in minutes (default: 5 minutes)")
	flag.IntVar(&maxArticleCache, "maxarticlecache", 10000, "maximum number of cached articles (default: 10000) [~8-12 KB/entry] 10000 = ~128 MB")
	flag.IntVar(&maxArticleCacheExpiry, "maxarticlecacheexpiry", 60, "expiry of cached articles in minutes (default: 60 minutes)")
	flag.IntVar(&useShortHashLen, "useshorthashlen", 7, "short hash length (2-7, default: 7): legacy, no effect on the message-id history schema; the first stored value stays locked, a value outside 2-7 is stored as 7")
	flag.IntVar(&webport, "webport", 0, "Web server port (default: 11980 (no ssl) or 19443 (webssl))")
	flag.BoolVar(&webssl, "webssl", false, "Enable SSL")
	//flag.Int64Var(&ignoreInitialTinyGroups, "ignore-initial-tiny-groups", 0, "If > 0: initial fetch ignores tiny groups with fewer articles than this (default: 0)")
	flag.StringVar(&nntphostname, "nntphostname", "", "your hostname must be set")
	flag.BoolVar(&useHistory, "history", true, "maintain and use the message-id history index (data/history)")
	flag.StringVar(&webcertFile, "websslcert", "", "SSL certificate file (/path/to/fullchain.pem)")
	flag.StringVar(&webkeyFile, "websslkey", "", "SSL key file (/path/to/privkey.pem)")
	flag.IntVar(&nntptcpport, "nntptcpport", 0, "ignored (no embedded NNTP server): run cmd/nntp-server")
	flag.IntVar(&nntptlsport, "nntptlsport", 0, "ignored (no embedded NNTP server): run cmd/nntp-server")
	flag.StringVar(&nntpcertFile, "nntpcertfile", "", "ignored (no embedded NNTP server): run cmd/nntp-server")
	flag.StringVar(&nntpkeyFile, "nntpkeyfile", "", "ignored (no embedded NNTP server): run cmd/nntp-server")
	flag.BoolVar(&forceReloadDesc, "update-overwrite-newsgroups-descriptions", false, "Updates (overwrites existing!) internal newsgroup descriptions from file preload/newsgroups.descriptions (default: false)")
	flag.StringVar(&importDescFile, "update-newsgroups-descriptions-file", "", "Import newsgroups from descriptions file (format: groupname\\tdescription)")
	flag.BoolVar(&importCreateMissing, "update-newsgroups-descriptions-import-create-newsgroups", false, "Create missing newsgroups when importing from descriptions file (default: false)")
	flag.BoolVar(&repairWatermarks, "repair-watermarks", false, "Repair corrupted newsgroup watermarks caused by preloader (default: false)")
	flag.IntVar(&updateNGAConcurrent, "update-newsgroups-activity", 0, "Updates newsgroup updated_at timestamps to reflect actual article activity. Set N to process this many newsgroups concurrently")
	flag.BoolVar(&updateNewsgroupsHideFuture, "update-newsgroups-hide-futureposts", false, "Hide articles posted more than 48 hours in the future (default: false)")
	flag.StringVar(&importActiveFile, "import-active", "", "Import newsgroups from NNTP active file (format: groupname highwater lowwater status)")
	flag.StringVar(&writeActiveFile, "write-active-file", "", "Write NNTP active file from main database newsgroups table to specified path")
	flag.BoolVar(&writeActiveOnly, "write-active-only", true, "use with -write-active-file (false writes only non active groups!)")
	flag.StringVar(&rsyncInactiveGroups, "rsync-inactive-groups", "", "path to new data dir, uses rsync to copy all inactive group databases to new data folder.")
	flag.BoolVar(&rsyncRemoveSource, "rsync-remove-source", false, "use with -rsync-inactive-groups. if set, removes source files after moving inactive groups (default: false)")
	flag.BoolVar(&editCronjobs, "edit-cronjobs", false, "Safety Warning / Shell Execution: Adding/Editing Crons is disabled by default. Use this flag only temporarily!")
	flag.BoolVar(&noCronjobs, "no-cronjobs", false, "use this flag to not run cron jobs")
	flag.BoolVar(&findOrphanDBs, "find-orphan-dbs", false, "Find orphaned database folders in data/db that don't correspond to any newsgroup in main database")
	flag.StringVar(&compareActiveFile, "compare-active", "", "Compare active file with database and show missing groups (format: groupname highwater lowwater status)")
	flag.StringVar(&disableActiveFile, "disable-active", "", "Disable newsgroups not listed in active file (format: groupname [highwater lowwater status], one group per line); refused when the file lists no group")
	flag.BoolVar(&disableActiveDryRun, "disable-active-dry-run", false, "use with -disable-active: print the groups that would be disabled and change nothing")
	flag.Int64Var(&compareActiveMinArticles, "compare-active-min-articles", 0, "use with -compare-active: only show groups with more than N articles (calculated as high-low)")
	flag.StringVar(&updateNewsgroupsExpiryFile, "update-newsgroups-expiry-from-file", "", "Update newsgroup expiry_days from file (format: newsgroup:days, one per line)")
	flag.BoolVar(&verbose, "verbose", false, "print more (debug) output")
	flag.StringVar(&dataDir, "data", "./data", "path to database and config directory")

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
	history.ENABLE_HISTORY = useHistory
	mainConfig := config.NewDefaultConfig()
	log.Printf("Starting go-pugleaf: Web Server (version: %s)", appVersion)

	// Debug: Log parsed flags
	log.Printf("[WEB]: Web Parsed flags - port: %d, ssl: %t, cert: %s, key: %s", webport, webssl, webcertFile, webkeyFile)
	// cmd/web has no embedded NNTP server: the NNTP flags stay parsed (scripts pass them) but do nothing
	if nntptcpport != 0 || nntptlsport != 0 || nntpcertFile != "" || nntpkeyFile != "" {
		log.Printf("[WEB]: -nntptcpport/-nntptlsport/-nntpcertfile/-nntpkeyfile are ignored: run cmd/nntp-server")
	}

	// Load configuration from file or use defaults
	//var webConfig *config.WebConfig
	webConfig := mainConfig.Server.WEB
	if webConfig.ListenPort == 0 {
		log.Printf("[WEB]: Error loading default webconfig..")
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

	// Validate port
	if webConfig.ListenPort < 1024 || webConfig.ListenPort > 65535 {
		log.Fatalf("[WEB]: Invalid port number: %d (must be between 1024 and 65535)", webConfig.ListenPort)
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

	// Initialize database with custom cache configuration
	dbConfig := database.DefaultDBConfig() // Start with defaults
	dbConfig.DataDir = dataDir             // honor -data (main DB, group DBs, history)
	// Override cache settings with command-line flag values
	dbConfig.ArticleCacheSize = maxArticleCache
	dbConfig.ArticleCacheExpiry = time.Duration(maxArticleCacheExpiry) * time.Minute

	db, err := database.OpenDatabase(dbConfig) // Pass custom dbConfig with cache settings
	if err != nil {
		log.Fatalf("[WEB]: Failed to initialize database: %v", err)
	}

	// Initialize progress database once to avoid opening/closing for each group
	// (NewProgressDB takes a directory: the default stays data/progress.db/progress.db)
	progressDB, err := database.NewProgressDB(filepath.Join(dataDir, "progress.db"))
	if err != nil {
		fatalAfterDB(db, "[WEB]: Failed to initialize progress database: %v", err)
	}
	defer progressDB.Close()

	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate
	//log.Printf("[WEB]: Date parser adapter initialized with processor.ParseNNTPDate")

	// Note: Database batch workers are started automatically by OpenDatabase() (which also adds them to db.WG)

	// Set hostname in processor with database fallback support
	if err := processor.SetHostname(nntphostname, db); err != nil {
		fatalAfterDB(db, "[WEB]: Failed to set NNTP hostname: %v", err)
	}

	// Run future posts hiding migration first if requested
	if updateNewsgroupsHideFuture {
		log.Printf("[WEB]: Starting future posts hiding migration...")
		if err := hideFuturePosts(db); err != nil {
			log.Printf("[WEB]: Warning: Future posts hiding migration failed: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Future posts hiding migration completed successfully")
			if updateNGAConcurrent == 0 {
				exitAfterDB(db, 0)
			}
		}
	}

	// Run newsgroup activity migration after hiding future posts if requested
	if updateNGAConcurrent > 0 {
		log.Printf("[WEB]: Starting updateNewsgroupLastActivity")
		if err := updateNewsgroupLastActivity(db, updateNGAConcurrent); err != nil {
			log.Printf("[WEB]: Warning: Newsgroup activity migration failed: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Newsgroup activity migration completed successfully")
			exitAfterDB(db, 0)
		}
	}

	// Write active file if requested
	if writeActiveFile != "" {
		log.Printf("[WEB]: Writing active file from main database to: %s", writeActiveFile)
		if err := writeActiveFileFromDB(db, writeActiveFile, writeActiveOnly); err != nil {
			log.Printf("[WEB]: Error: Failed to write active file: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Active file written successfully")
			exitAfterDB(db, 0)
		}
	}

	// rsyncInactiveGroups
	if rsyncInactiveGroups != "" {
		log.Printf("[RSYNC]: inactive groups to: %s", rsyncInactiveGroups)
		if err := rsyncInactiveGroupsToDir(db, rsyncInactiveGroups); err != nil {
			log.Printf("[RSYNC]: Error: Failed to rsync inactive groups: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[RSYNC]: Inactive groups synced successfully")
			exitAfterDB(db, 0)
		}
	}

	// compareActiveFile
	if compareActiveFile != "" {
		log.Printf("[WEB]: Comparing active file with database: %s (min articles: %d)", compareActiveFile, compareActiveMinArticles)
		if err := compareActiveFileWithDatabase(db, compareActiveFile, compareActiveMinArticles); err != nil {
			log.Printf("[WEB]: Error: Failed to compare active file: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Active file comparison completed successfully")
			exitAfterDB(db, 0)
		}
	}

	// disableActiveFile
	if disableActiveFile != "" {
		log.Printf("[WEB]: Disabling newsgroups not listed in active file: %s", disableActiveFile)
		if err := disableNewsgroupsNotInActiveFile(db, disableActiveFile, disableActiveOpts{DryRun: disableActiveDryRun}); err != nil {
			log.Printf("[WEB]: Error: Failed to disable newsgroups: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Newsgroup disabling completed successfully")
			exitAfterDB(db, 0)
		}
	}

	// findOrphanDBs
	if findOrphanDBs {
		log.Printf("[WEB]: Finding orphaned database folders...")
		if err := findOrphanedDatabases(db, dataDir, verbose); err != nil {
			log.Printf("[WEB]: Error: Failed to find orphaned databases: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Orphaned database scan completed successfully")
			exitAfterDB(db, 0)
		}
	}

	// updateNewsgroupsExpiryFile
	if updateNewsgroupsExpiryFile != "" {
		log.Printf("[WEB]: Updating newsgroup expiry from file: %s", updateNewsgroupsExpiryFile)
		if err := updateNewsgroupsExpiryFromFile(db, updateNewsgroupsExpiryFile); err != nil {
			log.Printf("[WEB]: Error: Failed to update newsgroup expiry: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Newsgroup expiry update completed successfully")
			exitAfterDB(db, 0)
		}
	}

	// Get or set history UseShortHashLen configuration (legacy setting, OPS-4b: never fatal for its value)
	finalUseShortHashLen, err := resolveUseShortHashLen(db, useShortHashLen)
	if err != nil {
		fatalAfterDB(db, "[WEB]: Failed to resolve history configuration: %v", err)
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
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Newsgroups imported successfully")
			if importActiveFile != "" {
				// only quit when importing via active files, not when updateting descriptions
				exitAfterDB(db, 0)
			}
		}
	}

	if repairWatermarks {
		log.Printf("[WEB]: Repairing corrupted newsgroup watermarks...")
		if err := preloader.RepairNewsgroupWatermarks(ctx, db); err != nil {
			log.Printf("[WEB]: Warning: Failed to repair watermarks: %v", err)
			exitAfterDB(db, 1)
		} else {
			log.Printf("[WEB]: Watermarks repaired successfully")
			exitAfterDB(db, 0)
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

	proc, err := NewFetchProcessor(db, finalUseShortHashLen)
	if err != nil {
		fatalAfterDB(db, "[WEB]: Error booting processor: %v", err)
	}

	postQueueWorker := proc.NewPostQueueWorker()
	postQueueWorker.Start()
	log.Printf("[WEB]: PostQueueWorker started for web posting")

	// Create and start web server in a goroutine for non-blocking startup
	server := web.NewWebServer(db, webConfig, nil, editCronjobs, noCronjobs) // no embedded NNTP server (D10)
	if !noCronjobs && server.CronManager != nil {
		db.WG.Add(1)
	}
	server.StartSessionCleanup() // stops on server.Shutdown
	// Set up cross-platform signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM) // Ctrl+C and SIGTERM take the same graceful path

	// Start web server in goroutine to make it non-blocking
	webServerErrChan := make(chan error, 1)
	go func() {
		log.Printf("[WEB]: Starting go-pugleaf web server on %s://localhost:%d", protocol, webConfig.ListenPort)
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			webServerErrChan <- err
		}
	}()

	log.Printf("[WEB]: Server started successfully. Press Ctrl+C to gracefully shutdown...")

	// Start update file monitor in a separate goroutine
	updateFileChan := make(chan bool, 1)
	go monitorUpdateFile(updateFileChan, webStop)
	// Start hierarchy last_updated synchronizer in the background; it uses the DB, so
	// the DB teardown waits for it (db.WG) after webStop made it return
	db.WG.Add(1)
	go func() {
		defer db.WG.Done()
		startHierarchyUpdater(db, webStop)
	}()
	// Wait for either shutdown signal, server error, or update file
	exitCode := 0
	select {
	case sig := <-sigChan:
		log.Printf("[WEB]: Received shutdown signal (%v), initiating graceful shutdown...", sig)
	case err := <-webServerErrChan:
		// not fatal here: the shutdown below still stops the workers and tears the DB down
		log.Printf("[WEB]: Failed to start web server: %v", err)
		exitCode = 1
	case <-updateFileChan:
		log.Printf("[WEB]: Update file detected, initiating graceful shutdown for update...")
	}

	// Stop the cmd/web background loops (hierarchy updater, update file monitor)
	closeWebStop()

	// Stop accepting web requests and wait (bounded) for active ones
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[WEB]: web server shutdown: %v", err)
	}
	cancelShutdown()

	// Stop PostQueueWorker if running
	if postQueueWorker != nil {
		log.Printf("[WEB]: Stopping PostQueueWorker...")
		postQueueWorker.Stop()
		log.Printf("[WEB]: PostQueueWorker stopped")
	}

	// Stop CronJobs if running
	if server.CronManager != nil {
		server.CronManager.StopCronManager()
	}

	// Signal background tasks to stop
	closeDBStopChan(db)

	// Close the proc/processor (flushes history, stops processing)
	if proc != nil {
		if err := proc.Close(); err != nil {
			log.Printf("[WEB] Warning: Failed to close proc: %v", err)
		} else {
			log.Printf("[WEB] proc closed successfully")
		}
	}

	// Notify orchestrator to send shutdown signals to workers
	//go db.Batch.Shutdown()
	// Wait (bounded) for all database operations to complete, then shut the DB down
	if err := teardownDB(db); err != nil {
		log.Printf("[WEB]: Failed to shutdown database: %v", err)
		os.Exit(1)
	}
	log.Printf("[WEB]: Database shutdown successfully")

	log.Printf("[WEB]: Graceful shutdown completed")
	if exitCode != 0 {
		os.Exit(exitCode) // the DB is already shut down
	}
} // end main
