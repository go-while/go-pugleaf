// Post-queue tool for go-pugleaf
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/postmgr"
)

var appVersion = "-unset-"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run returns the process exit code for the command line args (without the program name):
// 0 ok, 1 error, 2 bad flags, 130 one-shot run stopped by a signal (before the batch started,
// or the batch returned postmgr.ErrStopped). A batch that finished before the signal arrived
// exits 0. SIGINT/SIGTERM are handled from the same point as before (after the cleanup flags),
// see runWith.
func run(args []string) int {
	return runWith(args, nil)
}

// runWith is run with an optional shutdown channel. stopChan == nil installs the SIGINT/SIGTERM
// handler (notifyShutdown) after the cleanup flags are handled, as the tool always did; tests pass
// their own channel and close it instead of sending a signal.
// Nothing after OpenDatabase exits the process: every return goes through the deferred
// shutdown, which runs after the deferred close of the posting pools.
func runWith(args []string, stopChan <-chan struct{}) (code int) {
	config.AppVersion = appVersion
	database.DBidleTimeOut = 15 * time.Second
	database.NO_CACHE_BOOT = true // prevents booting caches
	log.Printf("Starting go-pugleaf Post Queue Tool (version %s)", config.AppVersion)

	// Command line flags
	fs := flag.NewFlagSet("post-queue", flag.ContinueOnError)
	var (
		showHelp      = fs.Bool("help", false, "Show usage examples and exit")
		daemon        = fs.Bool("daemon", false, "Run as a daemon")
		limit         = fs.Int("max-batch", 100, "Post max N articles in a batch")
		cleanupPosted = fs.Bool("cleanup-posted-entries", false, "Delete all successfully posted entries from post_queue table and exit")
		cleanupOlder  = fs.Int("cleanup-older", 0, "Delete all post_queue entries older than N days and exit (0 = disabled)")
		dataDir       = fs.String("data", "./data", "Directory to store database files")
	)
	if err := fs.Parse(args); err != nil {
		// Parse already printed the error and the usage, like flag.ExitOnError, which exits 0
		// for -h/-help requests it does not define (flag.ErrHelp) and 2 otherwise.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *showHelp {
		showUsageExamples()
		return 0
	}

	// Initialize database
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = *dataDir

	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Printf("Failed to initialize database: %v", err)
		return 1
	}
	defer func() {
		if !shutdown(db) && code == 0 {
			code = 1
		}
	}()

	// Handle cleanup flags
	if *cleanupPosted || *cleanupOlder > 0 {
		if *cleanupPosted && *cleanupOlder > 0 {
			log.Printf("Cleaning up successfully posted entries AND entries older than %d days from post_queue table...", *cleanupOlder)
		} else if *cleanupPosted {
			log.Printf("Cleaning up successfully posted entries from post_queue table...")
		} else {
			log.Printf("Cleaning up post_queue entries older than %d days...", *cleanupOlder)
		}

		deletedCount, err := db.CleanupPostedEntries(*cleanupPosted, *cleanupOlder)
		if err != nil {
			log.Printf("Failed to cleanup entries: %v", err)
			return 1
		}
		log.Printf("Successfully deleted %d entries from post_queue table", deletedCount)
		return 0
	}

	// Set up signal handling for graceful shutdown
	shutdownChan := stopChan
	if shutdownChan == nil {
		sigShutdown, stopSignals := notifyShutdown()
		defer stopSignals()
		shutdownChan = sigShutdown
	}
	runDone := make(chan struct{})
	defer close(runDone)

	// Rows left in_processing by a killed or crashed earlier run are never selected again:
	// return them to pending before this run selects. This assumes only one post-queue runs
	// at a time: the web cron scheduler runs the executions of a job one after another
	// (internal/web/cronjobs.go runJobScheduler waits for executeJob). A second post-queue
	// started at the same time would reset rows the first one is posting (possible repost).
	if err := db.ResetAllPostQueueProcessing(); err != nil {
		log.Printf("Failed to reset stale in_processing post_queue entries: %v", err)
		return 1
	}

	// Get providers with posting enabled
	providers, err := getPostingProviders(db)
	if err != nil {
		log.Printf("Failed to get posting providers: %v", err)
		return 1
	}

	if len(providers) == 0 {
		log.Printf("No providers with posting enabled found")
		return 0
	}

	//log.Printf("Found %d providers with posting enabled", len(providers))

	// Create connection pools for posting providers
	pools, err := createPostingPools(providers)
	if err != nil {
		log.Printf("Failed to create posting pools: %v", err)
		return 1
	}
	defer func() {
		for _, pool := range pools {
			pool.ClosePool()
		}
	}()

	// Create poster manager
	posterManager := postmgr.NewPosterManager(db, pools)

	// Start the posting loop
	//log.Printf("Starting post queue processing...")

	if !*daemon {
		// A signal during startup (reset, providers, pools): don't start the batch at all.
		select {
		case <-shutdownChan:
			log.Printf("Shutdown requested before processing, no posts sent")
			return 130
		default:
		}
		// A shutdown signal stops the batch: the rows not posted yet go back to pending.
		go func() {
			select {
			case <-shutdownChan:
				posterManager.Stop()
			case <-runDone:
			}
		}()
		done, err := posterManager.ProcessPendingPosts(*limit)
		if errors.Is(err, postmgr.ErrStopped) {
			log.Printf("Stopped by shutdown signal after %d pending posts, the rest is back to pending", done)
			return 130
		}
		if err != nil {
			log.Printf("Error processing pending posts: %v", err)
			return 1
		}
		log.Printf("Post queue run done: %d pending posts worked on (posted + failed)", done)
		return 0
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go posterManager.Run(*limit, shutdownChan, &wg)
	wg.Wait()

	log.Printf("Post Queue Tool has shut down gracefully")
	return 0
}

// notifyShutdown returns a channel that is closed on SIGINT or SIGTERM, and stop, which ends the
// watcher goroutine and the signal notification (call it once, when the run returns).
func notifyShutdown() (shutdownChan <-chan struct{}, stop func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	closeChan := make(chan struct{})
	runDone := make(chan struct{})

	go func() {
		select {
		case <-sigChan:
			log.Printf("Received shutdown signal, initiating graceful shutdown...")
			close(closeChan)
		case <-runDone:
		}
	}()
	return closeChan, func() {
		close(runDone)
		signal.Stop(sigChan)
	}
}

// shutdown stops the database. OpenDatabase adds the batch orchestrators to db.WG,
// so StopChan must be closed and WG waited for before Shutdown.
func shutdown(db *database.Database) bool {
	close(db.StopChan)
	db.WG.Wait()
	if err := db.Shutdown(); err != nil {
		log.Printf("Failed to shutdown database: %v", err)
		return false
	}
	return true
}

// showUsageExamples displays usage examples
func showUsageExamples() {
	fmt.Println("\n=== Post Queue Tool - Usage Examples ===")
	fmt.Println("The Post Queue tool processes articles queued from the web interface")
	fmt.Println("and posts them to all providers with posting enabled.")
	fmt.Println()
	fmt.Println("Basic Usage:")
	fmt.Println("  ./post-queue")
	fmt.Println("  ./post-queue -daemon              # Run continuously as daemon")
	fmt.Println("  ./post-queue -max-batch 50        # Process max 50 articles per batch")
	fmt.Println()
	fmt.Println("Maintenance:")
	fmt.Println("  ./post-queue -cleanup-posted-entries         # Delete all successfully posted entries")
	fmt.Println("  ./post-queue -cleanup-older 7                # Delete posted entries older than 7 days")
	fmt.Println("  ./post-queue -cleanup-posted-entries -cleanup-older 30  # Delete posted entries older than 30 days")
	fmt.Println()
	fmt.Println("Data directory:")
	fmt.Println("  ./post-queue -data ./data-test       # Use another data root (default ./data)")
	fmt.Println()
	fmt.Println("Attempts:")
	fmt.Println("  An entry every provider rejects (441) counts one attempt; connection errors do not count.")
	fmt.Println("  After PostQueueMaxAttempts attempts (config key, default 5) the entry is parked and no")
	fmt.Println("  longer posted; retry it on the admin page (Post Queue tab) or raise the key.")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -help                     Show this help message")
	fmt.Println("  -daemon                   Run as continuous daemon")
	fmt.Println("  -max-batch N              Process max N articles per batch (default: 100)")
	fmt.Println("  -cleanup-posted-entries   Delete all successfully posted entries and exit")
	fmt.Println("  -cleanup-older N          Delete posted entries older than N days and exit")
	fmt.Println("  -data DIR                 Directory to store database files (default: ./data)")
	fmt.Println()
}

// getPostingProviders returns all enabled providers with posting capability
func getPostingProviders(db *database.Database) ([]*models.Provider, error) {
	allProviders, err := db.GetProviders()
	if err != nil {
		return nil, err
	}

	var postingProviders []*models.Provider
	for _, p := range allProviders {
		if p.Enabled && p.Posting && p.Host != "" && p.Port > 0 && p.MaxConns > 0 {
			//log.Printf("Found posting provider: %s (ID: %d, Host: %s, Port: %d, MaxConns: %d)",	p.Name, p.ID, p.Host, p.Port, p.MaxConns)
			postingProviders = append(postingProviders, p)
		}
	}

	return postingProviders, nil
}

// createPostingPools creates NNTP connection pools for posting providers
func createPostingPools(providers []*models.Provider) ([]*nntp.Pool, error) {
	pools := make([]*nntp.Pool, 0, len(providers))

	for _, p := range providers {
		// Convert models.Provider to config.Provider
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
			Posting:    p.Posting,
			// Copy proxy configuration
			ProxyEnabled:  p.ProxyEnabled,
			ProxyType:     p.ProxyType,
			ProxyHost:     p.ProxyHost,
			ProxyPort:     p.ProxyPort,
			ProxyUsername: p.ProxyUsername,
			ProxyPassword: p.ProxyPassword,
		}

		backendConfig := &nntp.BackendConfig{
			Host:     p.Host,
			Port:     p.Port,
			SSL:      p.SSL,
			Username: p.Username,
			Password: p.Password,
			MaxConns: p.MaxConns,
			Provider: configProvider,
			// Copy proxy configuration
			ProxyEnabled:  p.ProxyEnabled,
			ProxyType:     p.ProxyType,
			ProxyHost:     p.ProxyHost,
			ProxyPort:     p.ProxyPort,
			ProxyUsername: p.ProxyUsername,
			ProxyPassword: p.ProxyPassword,
		}

		pool := nntp.NewPool(backendConfig)
		pools = append(pools, pool)
		//log.Printf("Created posting pool for provider '%s' with max %d connections", p.Name, p.MaxConns)
	}

	return pools, nil
}
