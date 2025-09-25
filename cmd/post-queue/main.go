// Post-queue tool for go-pugleaf
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/postmgr"
)

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	database.DBidleTimeOut = 15 * time.Second
	database.NO_CACHE_BOOT = true // prevents booting caches
	log.Printf("Starting go-pugleaf Post Queue Tool (version %s)", config.AppVersion)

	// Command line flags
	var (
		showHelp      = flag.Bool("help", false, "Show usage examples and exit")
		daemon        = flag.Bool("daemon", false, "Run as a daemon")
		limit         = flag.Int("max-batch", 100, "Post max N articles in a batch")
		cleanupPosted = flag.Bool("cleanup-posted-entries", false, "Delete all successfully posted entries from post_queue table and exit")
		cleanupOlder  = flag.Int("cleanup-older", 0, "Delete all post_queue entries older than N days and exit (0 = disabled)")
	)
	flag.Parse()

	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}

	// Initialize database
	db, err := database.OpenDatabase(nil)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Shutdown()

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
			log.Fatalf("Failed to cleanup entries: %v", err)
		}
		log.Printf("Successfully deleted %d entries from post_queue table", deletedCount)
		os.Exit(0)
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	shutdownChan := make(chan struct{})

	go func() {
		<-sigChan
		log.Printf("Received shutdown signal, initiating graceful shutdown...")
		close(shutdownChan)
	}()

	// Get providers with posting enabled
	providers, err := getPostingProviders(db)
	if err != nil {
		log.Fatalf("Failed to get posting providers: %v", err)
	}

	if len(providers) == 0 {
		log.Printf("No providers with posting enabled found")
		os.Exit(0)
	}

	//log.Printf("Found %d providers with posting enabled", len(providers))

	// Create connection pools for posting providers
	pools, err := createPostingPools(providers)
	if err != nil {
		log.Fatalf("Failed to create posting pools: %v", err)
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
		done, err := posterManager.ProcessPendingPosts(*limit)
		if err != nil {
			log.Printf("Error processing pending posts: %v", err)
			os.Exit(1)
		}
		log.Printf("Processed %d pending posts", done)
		os.Exit(0)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go posterManager.Run(*limit, shutdownChan, &wg)
	wg.Wait()

	log.Printf("Post Queue Tool has shut down gracefully")

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
	fmt.Println("Advanced Options:")
	fmt.Println("  ./post-queue -max-providers 5 -max-concurrent 3")
	fmt.Println("  ./post-queue -retry-attempts 5 -retry-delay 60s")
	fmt.Println("  ./post-queue -check-interval 30s")
	fmt.Println("  ./post-queue -dry-run (test mode without actual posting)")
	fmt.Println()
	fmt.Println("Monitoring:")
	fmt.Println("  ./post-queue -max-concurrent 1 -check-interval 10s")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -help                     Show this help message")
	fmt.Println("  -daemon                   Run as continuous daemon")
	fmt.Println("  -max-batch N              Process max N articles per batch (default: 100)")
	fmt.Println("  -cleanup-posted-entries   Delete all successfully posted entries and exit")
	fmt.Println("  -cleanup-older N          Delete posted entries older than N days and exit")
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
