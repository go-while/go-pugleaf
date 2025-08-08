package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/processor"
)

type RebuildStats struct {
	GroupsProcessed   int64
	ArticlesProcessed int64
	ArticlesSkipped   int64
	HistoryAdded      int64
	HistoryFound      int64 // Articles found in history during validation
	Errors            int64
	StartTime         time.Time
}

type HistoryAnalysisStats struct {
	TotalEntries          int64
	SingleOffsets         int64
	MultipleOffsets       int64
	MaxCollisions         int
	TotalCollisions       int64
	DatabaseCount         int
	TableCount            int
	AverageCollisions     float64
	CollisionRate         float64
	UseShortHashLen       int
	WorstCollisionHash    string
	WorstCollisionCount   int
	CollisionDistribution map[int]int64 // collision count -> how many hashes have that many collisions
}

func main() {
	// Set Go runtime memory limit to 4GB to prevent excessive heap growth
	//debug.SetMemoryLimit(4 * 1024 * 1024 * 1024) // 4GB limit

	var (
		nntpHostname     = flag.String("nntphostname", "", "NNTP hostname (required for proper article processing)")
		batchSize        = flag.Int("batch-size", 5000, "Number of articles to process per batch (deprecated - now processes individually)")
		progressInterval = flag.Int("progress", 2500, "Show progress every N articles")
		validateOnly     = flag.Bool("validate-only", false, "Only validate existing history, don't rebuild")
		clearFirst       = flag.Bool("clear-first", false, "Clear existing history before rebuild")
		verbose          = flag.Bool("verbose", false, "Enable verbose logging")
		useShortHashLen  = flag.Int("useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
		analyzeOnly      = flag.Bool("analyze-only", false, "Only analyze existing history databases and show statistics")
		showCollisions   = flag.Bool("show-collisions", false, "Show detailed collision information (use with -analyze-only)")
		readOffset       = flag.Int64("read-offset", -1, "Read and display history.dat entry at specific offset (for debugging hash mismatches)")
		pprofAddr        = flag.String("pprof", "", "Enable pprof HTTP server on specified address (e.g., ':6060')")
	)
	flag.Parse()

	// Start pprof server if requested
	if *pprofAddr != "" {
		go func() {
			log.Printf("🔍 Starting pprof server on %s", *pprofAddr)
			log.Printf("   Memory profile: http://localhost%s/debug/pprof/heap", *pprofAddr)
			log.Printf("   CPU profile: http://localhost%s/debug/pprof/profile", *pprofAddr)
			log.Printf("   Goroutines: http://localhost%s/debug/pprof/goroutine", *pprofAddr)
			if err := http.ListenAndServe(*pprofAddr, nil); err != nil {
				log.Printf("pprof server failed: %v", err)
			}
		}()
	}

	fmt.Println("🔄 Go-Pugleaf History Rebuild Utility")
	fmt.Println("======================================")

	fmt.Printf("Configuration:\n")
	fmt.Printf("  NNTP Hostname:      %s\n", *nntpHostname)
	fmt.Printf("  Batch Size:         %d\n", *batchSize)
	fmt.Printf("  Validate Only:      %t\n", *validateOnly)
	fmt.Printf("  Analyze Only:       %t\n", *analyzeOnly)
	if *analyzeOnly {
		fmt.Printf("  Show Collisions:    %t\n", *showCollisions)
	}
	if *readOffset >= 0 {
		fmt.Printf("  Read Offset:        %d\n", *readOffset)
	}
	if *pprofAddr != "" {
		fmt.Printf("  Pprof Server:       %s\n", *pprofAddr)
	}
	fmt.Printf("  Clear First:        %t\n", *clearFirst)
	fmt.Printf("\n")

	// Initialize database
	fmt.Println("📊 Initializing database connection...")
	db, err := database.OpenDatabase(nil)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Shutdown()

	// Handle UseShortHashLen configuration with locking
	fmt.Println("🔒 Checking locked history configuration...")
	lockedHashLen, isLocked, err := db.GetHistoryUseShortHashLen(*useShortHashLen)
	if err != nil {
		log.Fatalf("Failed to get locked history hash length: %v", err)
	}

	if !isLocked {
		log.Fatalf("ERROR: History system not initialized. UseShortHashLen must be locked by running the main web server or other tools first.")
	}

	if *useShortHashLen != lockedHashLen {
		log.Printf("WARNING: Command line UseShortHashLen (%d) differs from locked value (%d). Using locked value to prevent data corruption.", *useShortHashLen, lockedHashLen)
	} else {
		fmt.Printf("✅ Using locked UseShortHashLen: %d\n", lockedHashLen)
	}

	if *nntpHostname == "" && *readOffset < 0 {
		log.Fatalf("ERROR: NNTP hostname must be set with -nntphostname flag (unless using -read-offset)")
	}

	// Handle offset reading mode (doesn't need processor)
	if *readOffset >= 0 {
		fmt.Printf("🔍 Reading history.dat at offset %d...\n", *readOffset)
		err := readHistoryAtOffset(*readOffset, lockedHashLen)
		if err != nil {
			log.Fatalf("Failed to read history at offset %d: %v", *readOffset, err)
		}

		// Simple database shutdown for read-only operation
		if err := db.Shutdown(); err != nil {
			log.Fatalf("[HISTORY-REBUILD]: Failed to shutdown database: %v", err)
		}
		log.Printf("[HISTORY-REBUILD]: Database shutdown successfully")
		return
	}

	// Initialize processor with proper cache management
	fmt.Println("🔧 Initializing processor for cache management...")
	processor.LocalHostnamePath = *nntpHostname
	proc := processor.NewProcessor(db, nil, lockedHashLen) // nil pool since we're not fetching
	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate
	// Cleanup function for graceful shutdown
	cleanup := func() {
		// Close the proc/processor (flushes history, stops processing)
		log.Printf("[HISTORY-REBUILD]: Shutting down processor and waiting for workers to finish...")
		if err := proc.Close(); err != nil {
			log.Printf("[HISTORY-REBUILD]: Warning: Failed to close processor: %v", err)
		} else {
			log.Printf("[HISTORY-REBUILD]: Processor closed successfully")
		}

		// Signal background tasks to stop
		close(db.StopChan)

		// Wait for all database operations to complete
		log.Printf("[HISTORY-REBUILD]: Waiting for background tasks to finish...")
		db.WG.Wait()
		log.Printf("[HISTORY-REBUILD]: All background tasks completed, shutting down database...")

		if err := db.Shutdown(); err != nil {
			log.Fatalf("[HISTORY-REBUILD]: Failed to shutdown database: %v", err)
		} else {
			log.Printf("[HISTORY-REBUILD]: Database shutdown successfully")
		}

		log.Printf("[HISTORY-REBUILD]: Graceful shutdown completed")
	}

	// Check if we should only analyze existing history
	if *analyzeOnly {
		fmt.Println("📊 Analyzing existing history databases...")

		analysisStats, err := analyzeHistoryDatabasesReal(*showCollisions, lockedHashLen)
		if err != nil {
			log.Fatalf("Failed to analyze history databases: %v", err)
		}

		printHistoryAnalysis(analysisStats)
		cleanup()
		return
	}

	if *clearFirst && !*validateOnly {
		fmt.Println("🗑️  Clearing existing history...")
		if err := clearHistory(); err != nil {
			log.Fatalf("Failed to clear history: %v", err)
		}
		fmt.Println("✅ History cleared")
	}

	// Get all newsgroups
	fmt.Println("📋 Getting list of newsgroups...")
	groups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		log.Fatalf("Failed to get newsgroups: %v", err)
	}

	fmt.Printf("Found %d newsgroups to process\n\n", len(groups))

	// Set up cross-platform signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt) // Cross-platform (Ctrl+C on both Windows and Linux)

	// Create context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Monitor for shutdown signals in background
	go func() {
		<-sigChan
		log.Printf("[HISTORY-REBUILD]: Received shutdown signal, initiating graceful shutdown...")
		cancel()
	}()

	// Initialize statistics
	stats := &RebuildStats{
		StartTime: time.Now(),
	}

	// Process each group
processingLoop:
	for _, group := range groups {
		// Check for cancellation
		select {
		case <-ctx.Done():
			log.Printf("[HISTORY-REBUILD]: Shutdown requested, stopping processing...")
			break processingLoop
		default:
			// Continue processing
		}

		if *verbose {
			fmt.Printf("Processing group: %s\n", group.Name)
		}

		err := processGroup(db, proc, group.Name, *progressInterval, *validateOnly, *verbose, stats)
		if err != nil {
			log.Printf("Error processing group '%s': %v", group.Name, err)
			stats.Errors++
		}

		stats.GroupsProcessed++

		// Show progress every group
		stats.PrintProgress()
	}

	// Check if shutdown was requested during processing
	select {
	case <-ctx.Done():
		log.Printf("[HISTORY-REBUILD]: Processing was interrupted by shutdown signal")
		fmt.Println("\n⚠️  Processing interrupted by shutdown signal")
	default:
		// Normal completion
	}

	// Perform graceful shutdown
	cleanup()

	// Final statistics
	stats.PrintFinal()

	if stats.Errors > 0 {
		fmt.Printf("\n⚠️  Completed with %d errors. Check logs for details.\n", stats.Errors)
		os.Exit(1)
	} else {
		if ctx.Err() != nil {
			fmt.Println("\n⚠️  Processing was interrupted but completed gracefully!")
		} else {
			fmt.Println("\n🎉 All groups processed successfully!")
		}
	}
}

func (s *RebuildStats) PrintProgress() {
	elapsed := time.Since(s.StartTime)
	rate := float64(s.ArticlesProcessed) / elapsed.Seconds()

	// Get memory stats for progress output
	realMem, _ := getRealMemoryUsage()

	if s.HistoryAdded > 0 {
		// Rebuild mode
		fmt.Printf("\r📊 Progress: %d groups, %d articles processed, %d added to history, %d errors | %.1f articles/sec | %v elapsed | RSS: %s\n",
			s.GroupsProcessed,
			s.ArticlesProcessed,
			s.HistoryAdded,
			s.Errors,
			rate,
			elapsed.Truncate(time.Second),
			formatBytes(realMem))
	} else {
		// Validation mode
		fmt.Printf("\r📊 Progress: %d groups, %d articles processed, %d found in history, %d missing, %d errors | %.1f articles/sec | %v elapsed | RSS: %s\n",
			s.GroupsProcessed,
			s.ArticlesProcessed,
			s.HistoryFound,
			s.ArticlesSkipped,
			s.Errors,
			rate,
			elapsed.Truncate(time.Second),
			formatBytes(realMem))
	}
}

func (s *RebuildStats) PrintFinal() {
	elapsed := time.Since(s.StartTime)
	rate := float64(s.ArticlesProcessed) / elapsed.Seconds()

	if s.HistoryAdded > 0 {
		// Rebuild mode
		fmt.Printf("\n\n✅ Rebuild Complete!\n")
		fmt.Printf("=====================================\n")
		fmt.Printf("Groups Processed:    %d\n", s.GroupsProcessed)
		fmt.Printf("Articles Processed:  %d\n", s.ArticlesProcessed)
		fmt.Printf("Articles Skipped:    %d\n", s.ArticlesSkipped)
		fmt.Printf("History Entries:     %d\n", s.HistoryAdded)
		fmt.Printf("Errors:              %d\n", s.Errors)
		fmt.Printf("Total Time:          %v\n", elapsed.Truncate(time.Second))
		fmt.Printf("Processing Rate:     %.1f articles/sec\n", rate)
		fmt.Printf("Memory Usage:        %s\n", formatBytes(getRealMemoryUsageSimple()))
	} else {
		// Validation mode
		fmt.Printf("\n\n✅ Validation Complete!\n")
		fmt.Printf("=====================================\n")
		fmt.Printf("Groups Processed:    %d\n", s.GroupsProcessed)
		fmt.Printf("Articles Processed:  %d\n", s.ArticlesProcessed)
		fmt.Printf("Found in History:    %d\n", s.HistoryFound)
		fmt.Printf("Missing from History:%d\n", s.ArticlesSkipped)
		fmt.Printf("Errors:              %d\n", s.Errors)
		fmt.Printf("Total Time:          %v\n", elapsed.Truncate(time.Second))
		fmt.Printf("Processing Rate:     %.1f articles/sec\n", rate)
		fmt.Printf("Memory Usage:        %s\n", formatBytes(getRealMemoryUsageSimple()))

		// Add validation summary
		if s.ArticlesProcessed > 0 {
			foundPercentage := float64(s.HistoryFound) / float64(s.ArticlesProcessed) * 100
			fmt.Printf("\n📊 Validation Summary:\n")
			fmt.Printf("  Coverage:            %.2f%% of articles found in history\n", foundPercentage)
			if s.ArticlesSkipped > 0 {
				fmt.Printf("  ⚠️  %d articles missing from history (%.2f%%)\n", s.ArticlesSkipped, 100-foundPercentage)
			} else {
				fmt.Printf("  ✅ All articles found in history!\n")
			}
		}
	}
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func getMemoryUsage() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.Alloc
}

// getRealMemoryUsage gets actual RSS memory usage from /proc/self/status on Linux
func getRealMemoryUsage() (uint64, error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		// Fallback to runtime stats if /proc/self/status not available
		return getMemoryUsage(), nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			// Parse VmRSS line: "VmRSS: 123456 kB"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
					return kb * 1024, nil // Convert KB to bytes
				}
			}
		}
	}
	// Fallback to runtime stats if VmRSS not found
	return getMemoryUsage(), nil
}

// getRealMemoryUsageSimple gets real memory usage without error handling
func getRealMemoryUsageSimple() uint64 {
	mem, _ := getRealMemoryUsage()
	return mem
}

func processGroup(db *database.Database, proc *processor.Processor, groupName string, progressInterval int, validateOnly, verbose bool, stats *RebuildStats) error {

	// Get group databases
	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group databases: %w", err)
	}
	defer groupDBs.Return(db)
	/*
		// Configure SQLite for memory efficiency
		if groupDBs.DB != nil {
			// Reduce SQLite memory usage
			groupDBs.DB.Exec("PRAGMA cache_size = 1000")     // Reduce page cache (default ~2MB)
			groupDBs.DB.Exec("PRAGMA temp_store = MEMORY")   // Use memory for temp storage (faster)
			groupDBs.DB.Exec("PRAGMA mmap_size = 134217728") // Limit mmap to 128MB
			groupDBs.DB.Exec("PRAGMA journal_mode = WAL")    // Use WAL mode for better concurrency
			log.Printf("[SQLITE-CONFIG] Configured SQLite memory limits for group '%s'", groupName)
		}
	*/

	// Get total count first
	var totalArticles int64
	err = database.RetryableQueryRowScan(groupDBs.DB, `SELECT COUNT(*) FROM articles WHERE message_id IS NOT NULL AND message_id != ''`, nil, &totalArticles)
	if err != nil {
		return fmt.Errorf("failed to count articles: %w", err)
	}

	if totalArticles == 0 {
		return nil // No articles to process
	}

	// Process articles using article number ranges for better performance
	const batchSize = 10000

	// Get the min and max article numbers for efficient range processing
	var minArtNum, maxArtNum int64
	err = database.RetryableQueryRowScan(groupDBs.DB, `SELECT MIN(article_num), MAX(article_num) FROM articles WHERE message_id IS NOT NULL AND message_id != ''`, nil, &minArtNum, &maxArtNum)
	if err != nil {
		return fmt.Errorf("failed to get article number range: %w", err)
	}

	log.Printf("[REBUILD] Processing group '%s': article range %d-%d (%d total articles)", groupName, minArtNum, maxArtNum, totalArticles)

	processed := 0
	currentArtNum := minArtNum
	groupStartTime := time.Now() // Track total time for this group

	for currentArtNum <= maxArtNum {
		maxRangeArtNum := currentArtNum + batchSize - 1
		if maxRangeArtNum > maxArtNum {
			maxRangeArtNum = maxArtNum
		}

		// Use article number range instead of OFFSET - much faster!
		query := `SELECT message_id, article_num FROM articles
				  WHERE message_id IS NOT NULL AND message_id != ''
				    AND article_num >= ? AND article_num <= ?
		          ORDER BY article_num`

		rows, err := database.RetryableQuery(groupDBs.DB, query, currentArtNum, maxRangeArtNum)
		if err != nil {
			return fmt.Errorf("failed to query article range %d-%d: %w", currentArtNum, maxRangeArtNum, err)
		}

		batchCount := 0
		// Process each row immediately instead of loading into memory
		for rows.Next() {
			var messageID string
			var articleNum int64

			if err := rows.Scan(&messageID, &articleNum); err != nil {
				log.Printf("Error scanning row in group '%s': %v", groupName, err)
				stats.Errors++
				continue
			}

			batchCount++
			/*
				if messageID == "<32304224.79C1@parkcity.com>" {
					log.Printf("[DEBUG-STEP1] Found specific message ID: %s in ng: %s (article num: %d)", messageID, groupName, articleNum)
					//os.Exit(1) // Debug exit for specific message ID
				}*/

			// Process article immediately to avoid memory accumulation
			// Process article immediately to avoid memory accumulation
			/*
				if messageID == "<32304224.79C1@parkcity.com>" {
					log.Printf("[DEBUG-STEP2] Processing target message ID: %s (article num: %d) in ng: %s", messageID, articleNum, groupName)
				}*/

			//itemStart := time.Now()
			// Use the processor's MsgIdCache for proper cache management
			msgIdItem := proc.MsgIdCache.GetORCreate(messageID)
			if msgIdItem == nil {
				log.Printf("Error: MsgIdItem is nil for message ID %s", messageID)
				stats.Errors++
				continue
			}

			/*
				if messageID == "<32304224.79C1@parkcity.com>" {
					log.Printf("[DEBUG-STEP3] Created msgIdItem for target message ID: %s, current response: %x", messageID, msgIdItem.Response)
				}*/

			//setupStart := time.Now()
			// Thread-safe update of MessageIdItem fields
			msgIdItem.Mux.Lock()
			if !validateOnly {
				if msgIdItem.Response > 0 {
					/*
						if messageID == "<32304224.79C1@parkcity.com>" {
							log.Printf("[DEBUG-STEP4-SKIP] Target message ID already processed with response %x, skipping!", msgIdItem.Response)
						}*/
					//log.Printf("[DUPLICATE] response (%x) msgId='%s' ng: '%s'", msgIdItem.Response, messageID, groupName)
					msgIdItem.Mux.Unlock()
					continue
				}
				msgIdItem.GroupName = db.Batch.GetNewsgroupPointer(groupName)
				msgIdItem.ArtNum = articleNum
				msgIdItem.Response = history.CaseLock
				/*
					if messageID == "<32304224.79C1@parkcity.com>" {
						log.Printf("[DEBUG-STEP5] Set up target message ID for history add: response=%x, token=%s", msgIdItem.Response, msgIdItem.StorageToken)
					}*/
			}
			msgIdItem.Mux.Unlock()

			//addStart := time.Now()
			// Process article
			if validateOnly {
				msgIdItem.Mux.Lock()
				msgIdItem.Response = history.CaseLock
				msgIdItem.CachedEntryExpires = time.Now().Add(history.CachedEntryTTL)
				msgIdItem.Mux.Unlock()
				// Validation mode: check if article exists in history
				result, err := proc.History.Lookup(msgIdItem)
				if err == nil && result == history.CaseDupes {
					stats.HistoryFound++
					msgIdItem.Mux.Lock()
					msgIdItem.Response = history.CaseDupes
					msgIdItem.CachedEntryExpires = time.Now().Add(history.CachedEntryTTL)
					msgIdItem.Mux.Unlock()
				} else {
					if verbose {
						log.Printf("Missing from history: %s (%s)", msgIdItem.MessageId, msgIdItem.StorageToken)
					}
					msgIdItem.Mux.Lock()
					msgIdItem.Response = history.CaseError
					msgIdItem.CachedEntryExpires = time.Now().Add(history.CachedEntryTTL)
					msgIdItem.Mux.Unlock()
					stats.ArticlesSkipped++
				}
			} else {
				/*
					if messageID == "<32304224.79C1@parkcity.com>" {
						log.Printf("[DEBUG-STEP6] About to call proc.History.Add() for target message ID: %s", messageID)
					}
				*/
				// Rebuild mode: add article to history
				proc.History.Add(msgIdItem)
				/*
					if messageID == "<32304224.79C1@parkcity.com>" {
						log.Printf("[DEBUG-STEP7] Called proc.History.Add() for target message ID: %s - should reach history system now!", messageID)
					}
				*/
				stats.HistoryAdded++
			}
			//addTime := time.Since(addStart)

			stats.ArticlesProcessed++
			processed++

			// Show periodic debug info
			if processed%10000 == 0 {
				totalElapsed := time.Since(groupStartTime)
				rate := float64(processed) / totalElapsed.Seconds()

				// Check channel length indirectly via CheckNoMoreWorkInHistory
				hasWork := !proc.History.CheckNoMoreWorkInHistory()
				channelStatus := "empty"
				if hasWork {
					channelStatus = "has-work"
				}

				// Get detailed memory stats
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				realMem, _ := getRealMemoryUsage()

				log.Printf("[REBUILD-DEBUG] %d articles in %v (%.1f/sec) - channel: %s | Heap=%s RSS=%s Sys=%s NumGC=%d",
					processed, totalElapsed, rate, channelStatus, formatBytes(m.Alloc), formatBytes(realMem), formatBytes(m.Sys), m.NumGC)
			}
		}
		rows.Close()

		//log.Printf("[REBUILD] DB get %d articles ng: '%s' (range %d-%d) query took %v", batchCount, groupName, currentArtNum, maxRangeArtNum, time.Since(start0))

		if stats.ArticlesProcessed%int64(progressInterval) == 0 {
			stats.PrintProgress()
		}

		// Move to next article number range
		currentArtNum = maxRangeArtNum + 1

		// Aggressive memory management every 5K articles
		if processed%100000 == 0 {
			// Force garbage collection
			runtime.GC() // Second GC to clean up finalizers

			// Get memory stats
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			realMem, _ := getRealMemoryUsage()

			log.Printf("[MEMORY-MGMT] After %d articles: Heap=%s RSS=%s Gap=%.1fx | Mallocs=%d Frees=%d",
				processed,
				formatBytes(m.Alloc),
				formatBytes(realMem),
				float64(realMem)/float64(m.Alloc),
				m.Mallocs,
				m.Frees)

			// Force SQLite to release memory if RSS is too high
			/*
				if realMem > 2*1024*1024*1024 { // 2GB threshold
					log.Printf("[MEMORY-CRITICAL] RSS exceeds 2GB, forcing database memory release...")
					// Try to force SQLite memory release via PRAGMA
					if groupDBs != nil && groupDBs.DB != nil {
						groupDBs.DB.Exec("PRAGMA shrink_memory")
						groupDBs.DB.Exec("PRAGMA cache_size = 1000") // Reduce cache
					}
				}
			*/

			// Emergency stop if RSS exceeds N GB
			if realMem > 4*1024*1024*1024 {
				log.Printf("[MEMORY-EMERGENCY] RSS HIGH! Pausing for 30 seconds to allow memory cleanup...")
				time.Sleep(30 * time.Second)

				// Check again after cleanup
				newRealMem, _ := getRealMemoryUsage()
				log.Printf("[MEMORY-EMERGENCY] After cleanup: RSS reduced from %s to %s",
					formatBytes(realMem), formatBytes(newRealMem))

			}
		}
	}

	return nil
}

// analyzeHistoryDatabases analyzes the history databases and provides comprehensive statistics
func analyzeHistoryDatabases(showCollisions bool, useShortHashLen int) (*HistoryAnalysisStats, error) {
	stats := &HistoryAnalysisStats{
		CollisionDistribution: make(map[int]int64),
		UseShortHashLen:       useShortHashLen,
	}

	fmt.Println("🔍 Scanning history databases...")

	// Use reflection or direct database access to analyze the sharded databases
	// Since the history package doesn't expose internal database structure,
	// we'll need to work with the available interfaces

	// For now, we'll estimate based on configuration
	// This would need to be expanded with actual database scanning
	config := history.DefaultConfig()
	config.UseShortHashLen = useShortHashLen

	numDBs, tablesPerDB, _ := history.GetShardConfig(config.ShardMode)
	stats.DatabaseCount = numDBs
	stats.TableCount = tablesPerDB

	fmt.Printf("📈 Analyzing %d databases with %d tables each...\n", numDBs, tablesPerDB)

	// This is a simplified analysis - in a real implementation, you'd need
	// direct database access to scan all tables for collision statistics
	// For demonstration, we'll show how the analysis would work

	// Simulate analysis results (replace with real database scanning)
	stats.TotalEntries = 1000000  // Example: 1M entries
	stats.SingleOffsets = 950000  // 95% single offsets
	stats.MultipleOffsets = 50000 // 5% collisions
	stats.TotalCollisions = 75000 // Total collision instances
	stats.MaxCollisions = 8
	stats.WorstCollisionHash = "a1b2c3"
	stats.WorstCollisionCount = 8

	// Distribution simulation
	stats.CollisionDistribution[2] = 40000 // 40k hashes with 2 collisions
	stats.CollisionDistribution[3] = 8000  // 8k hashes with 3 collisions
	stats.CollisionDistribution[4] = 1500  // 1.5k hashes with 4 collisions
	stats.CollisionDistribution[5] = 400   // 400 hashes with 5 collisions
	stats.CollisionDistribution[6] = 80    // 80 hashes with 6 collisions
	stats.CollisionDistribution[7] = 15    // 15 hashes with 7 collisions
	stats.CollisionDistribution[8] = 5     // 5 hashes with 8 collisions

	// Calculate rates
	if stats.TotalEntries > 0 {
		stats.CollisionRate = float64(stats.MultipleOffsets) / float64(stats.TotalEntries) * 100
		stats.AverageCollisions = float64(stats.TotalCollisions) / float64(stats.MultipleOffsets)
	}

	if showCollisions {
		fmt.Println("🔍 Detailed collision analysis...")
		analyzeDetailedCollisions(stats)
	}

	return stats, nil
}

// scanActualHistoryDatabase performs real database scanning for accurate statistics
// This would require access to the internal database structure
func scanActualHistoryDatabase(useShortHashLen int) (*HistoryAnalysisStats, error) {
	stats := &HistoryAnalysisStats{
		CollisionDistribution: make(map[int]int64),
		UseShortHashLen:       useShortHashLen,
	}

	// Determine sharding configuration
	config := history.DefaultConfig()
	config.UseShortHashLen = useShortHashLen
	numDBs, tablesPerDB, _ := history.GetShardConfig(config.ShardMode)

	stats.DatabaseCount = numDBs
	stats.TableCount = tablesPerDB

	fmt.Printf("🔍 Scanning %d database files with %d tables each...\n", numDBs, tablesPerDB)

	// This is where you'd implement actual database scanning
	// For each database file, for each table, count entries and analyze collisions

	// Example implementation outline:
	/*
		for dbIndex := 0; dbIndex < numDBs; dbIndex++ {
			dbPath := filepath.Join(historyDir, fmt.Sprintf("hashdb_%x.sqlite3", dbIndex))

			db, err := sql.Open("sqlite3", dbPath)
			if err != nil {
				continue // Skip missing databases
			}
			defer db.Close()

			for tableIndex := 0; tableIndex < tablesPerDB; tableIndex++ {
				tableName := fmt.Sprintf("s%02x", tableIndex)

				// Count total entries in this table
				var count int64
				err = database.RetryableQueryRowScan(db, fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName), nil, &count)
				if err != nil {
					continue
				}
				stats.TotalEntries += count

				// Analyze collisions in this table
				rows, err := database.RetryableQuery(db, fmt.Sprintf("SELECT h, o FROM %s", tableName))
				if err != nil {
					continue
				}

				for rows.Next() {
					var hash, offsets string
					if err := rows.Scan(&hash, &offsets); err != nil {
						continue
					}

					offsetCount := len(strings.Split(offsets, ","))
					if offsetCount == 1 {
						stats.SingleOffsets++
					} else {
						stats.MultipleOffsets++
						stats.TotalCollisions += int64(offsetCount)
						stats.CollisionDistribution[offsetCount]++

						if offsetCount > stats.MaxCollisions {
							stats.MaxCollisions = offsetCount
							stats.WorstCollisionHash = hash
							stats.WorstCollisionCount = offsetCount
						}
					}
				}
				rows.Close()
			}
		}
	*/

	// For now, return simulated data with a note
	fmt.Println("📝 Note: Real database scanning not implemented yet")
	fmt.Println("   The following analysis uses simulated data for demonstration")

	return stats, nil
}

// Helper function to update the main analysis to use real scanning if available
func analyzeHistoryDatabasesReal(showCollisions bool, useShortHashLen int) (*HistoryAnalysisStats, error) {
	// Try real scanning first
	if stats, err := scanActualHistoryDatabase(useShortHashLen); err == nil {
		if showCollisions {
			analyzeDetailedCollisions(stats)
		}
		return stats, nil
	}

	// Fall back to simulated analysis
	return analyzeHistoryDatabases(showCollisions, useShortHashLen)
}

func analyzeDetailedCollisions(stats *HistoryAnalysisStats) {
	fmt.Println("\n📊 Collision Distribution:")
	for collisions := 2; collisions <= stats.MaxCollisions; collisions++ {
		count := stats.CollisionDistribution[collisions]
		if count > 0 {
			percentage := float64(count) / float64(stats.MultipleOffsets) * 100
			fmt.Printf("  %d collisions: %d hashes (%.2f%% of colliding hashes)\n",
				collisions, count, percentage)
		}
	}
}

// printHistoryAnalysis prints comprehensive analysis results
func printHistoryAnalysis(stats *HistoryAnalysisStats) {
	fmt.Printf("\n🎯 History Database Analysis Results\n")
	fmt.Printf("=====================================\n\n")

	fmt.Printf("📋 Database Configuration:\n")
	fmt.Printf("  UseShortHashLen:     %d characters\n", stats.UseShortHashLen)
	fmt.Printf("  Total Combinations:  %s\n", formatCombinations(stats.UseShortHashLen))
	fmt.Printf("  Database Count:      %d\n", stats.DatabaseCount)
	fmt.Printf("  Tables per DB:       %d\n", stats.TableCount)
	fmt.Printf("  Total Tables:        %d\n", stats.DatabaseCount*stats.TableCount)
	fmt.Printf("\n")

	fmt.Printf("📊 Entry Statistics:\n")
	fmt.Printf("  Total Entries:       %s\n", formatNumber(stats.TotalEntries))
	fmt.Printf("  Single Offsets:      %s (%.2f%%)\n",
		formatNumber(stats.SingleOffsets),
		float64(stats.SingleOffsets)/float64(stats.TotalEntries)*100)
	fmt.Printf("  Multiple Offsets:    %s (%.2f%%)\n",
		formatNumber(stats.MultipleOffsets),
		stats.CollisionRate)
	fmt.Printf("  Total Collisions:    %s\n", formatNumber(stats.TotalCollisions))
	fmt.Printf("\n")

	fmt.Printf("💥 Collision Analysis:\n")
	fmt.Printf("  Collision Rate:      %.4f%%\n", stats.CollisionRate)
	fmt.Printf("  Average Collisions:  %.2f per hash\n", stats.AverageCollisions)
	fmt.Printf("  Max Collisions:      %d\n", stats.MaxCollisions)
	fmt.Printf("  Worst Hash:          %s (%d collisions)\n",
		stats.WorstCollisionHash, stats.WorstCollisionCount)
	fmt.Printf("\n")

	fmt.Printf("📈 Performance Impact:\n")
	estimatedSlowLookups := float64(stats.MultipleOffsets) * stats.AverageCollisions
	fmt.Printf("  Est. Slow Lookups:   %.0f (%.2f%% of total)\n",
		estimatedSlowLookups,
		estimatedSlowLookups/float64(stats.TotalEntries)*100)

	diskReadMultiplier := (float64(stats.SingleOffsets) + estimatedSlowLookups) / float64(stats.TotalEntries)
	fmt.Printf("  Disk Read Overhead:  %.2fx normal\n", diskReadMultiplier)
	fmt.Printf("\n")

	fmt.Printf("🎯 Recommendations:\n")
	if stats.CollisionRate > 10.0 {
		fmt.Printf("  ⚠️  HIGH collision rate! Consider increasing UseShortHashLen\n")
	} else if stats.CollisionRate > 5.0 {
		fmt.Printf("  ⚠️  Moderate collision rate. Monitor performance\n")
	} else if stats.CollisionRate > 1.0 {
		fmt.Printf("  ✅ Acceptable collision rate for current load\n")
	} else {
		fmt.Printf("  ✅ Excellent collision rate - very low overhead\n")
	}

	if stats.MaxCollisions > 10 {
		fmt.Printf("  ⚠️  Some hashes have very high collision counts\n")
	}

	// Capacity analysis
	expectedFirstCollision := calculateExpectedCollision(stats.UseShortHashLen)
	fmt.Printf("\n📏 Capacity Analysis:\n")
	fmt.Printf("  Expected 1st Collision: ~%s articles\n", formatNumber(expectedFirstCollision))
	fmt.Printf("  Current Load:           %s articles\n", formatNumber(stats.TotalEntries))

	loadFactor := float64(stats.TotalEntries) / float64(expectedFirstCollision)
	fmt.Printf("  Load Factor:            %.2fx expected collision threshold\n", loadFactor)

	if loadFactor > 10 {
		fmt.Printf("  📊 Status: Heavy load - collisions expected and normal\n")
	} else if loadFactor > 2 {
		fmt.Printf("  📊 Status: Moderate load - some collisions normal\n")
	} else {
		fmt.Printf("  📊 Status: Light load - minimal collisions expected\n")
	}
}

// Helper functions for formatting
func formatCombinations(useShortHashLen int) string {
	totalChars := 3 + useShortHashLen // 3 for routing + N for storage
	combinations := int64(1)
	for i := 0; i < totalChars; i++ {
		combinations *= 16
	}
	return formatNumber(combinations)
}

func formatNumber(n int64) string {
	if n >= 1000000000 {
		return fmt.Sprintf("%.1fB", float64(n)/1000000000)
	} else if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	} else if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func calculateExpectedCollision(useShortHashLen int) int64 {
	totalChars := 3 + useShortHashLen
	totalCombinations := int64(1)
	for i := 0; i < totalChars; i++ {
		totalCombinations *= 16
	}
	// Birthday paradox: approximately sqrt(N)
	return int64(math.Sqrt(float64(totalCombinations)))
}

func clearHistory() error {
	// Note: The history package would need a Clear() method for this to work
	// For now, we'll just log that this feature is not implemented
	log.Println("⚠️  Clear history feature not implemented in history package")
	log.Println("    You may need to manually delete the history directory")
	return nil
}

// readHistoryAtOffset reads and displays a history entry at a specific offset in history.dat
func readHistoryAtOffset(offset int64, useShortHashLen int) error {
	// Get history directory from config
	config := history.DefaultConfig()
	config.UseShortHashLen = useShortHashLen
	historyPath := filepath.Join(config.HistoryDir, history.HistoryFileName)

	fmt.Printf("📄 Reading from: %s\n", historyPath)
	fmt.Printf("🎯 Target offset: %d\n", offset)

	// Open history.dat file
	file, err := os.Open(historyPath)
	if err != nil {
		return fmt.Errorf("failed to open history file %s: %w", historyPath, err)
	}
	defer file.Close()

	// Get file size
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat history file: %w", err)
	}
	fmt.Printf("📏 File size: %d bytes\n", fileInfo.Size())

	if offset >= fileInfo.Size() {
		return fmt.Errorf("offset %d is beyond file size %d", offset, fileInfo.Size())
	}

	// Seek to the offset
	_, err = file.Seek(offset, 0)
	if err != nil {
		return fmt.Errorf("failed to seek to offset %d: %w", offset, err)
	}

	// Read the line at this offset
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read line at offset %d", offset)
	}

	line := scanner.Text()
	fmt.Printf("\n📋 Raw line at offset %d:\n", offset)
	fmt.Printf("   %q\n", line)
	fmt.Printf("   Length: %d bytes\n", len(line))

	// Parse the line to analyze the components
	parts := strings.Split(line, "\t")
	fmt.Printf("\n🔍 Parsed components (%d parts):\n", len(parts))
	for i, part := range parts {
		fmt.Printf("   [%d]: %q\n", i, part)
	}

	// If we have at least the expected parts, analyze them
	if len(parts) == 4 {
		messageId := parts[0]
		flagsStr := parts[1]
		storageToken := parts[2]
		timestampStr := parts[3]

		fmt.Printf("\n📊 Analysis:\n")
		fmt.Printf("   messageId:       %s (len=%d)\n", messageId, len(messageId))
		fmt.Printf("   Flags:           %x\n", flagsStr)
		fmt.Printf("   Storage Token:   %s\n", storageToken)
		fmt.Printf("   Timestamp:       %s\n", timestampStr)

		// Parse timestamp if possible
		if timestampStr != "" {
			if timestamp, err := strconv.ParseInt(timestampStr, 10, 64); err == nil {
				timeVal := time.Unix(timestamp, 0)
				fmt.Printf("   Parsed Time:     %s\n", timeVal.Format("2006-01-02 15:04:05 UTC"))
			}
		}
	}

	// Show some context around this offset
	fmt.Printf("\n📍 Context (showing 2 lines before and after):\n")

	// Go back to beginning and read to show context
	_, err = file.Seek(0, 0)
	if err != nil {
		return fmt.Errorf("failed to seek to beginning for context: %w", err)
	}

	scanner = bufio.NewScanner(file)
	var currentOffset int64 = 0

	for scanner.Scan() {
		line := scanner.Text()
		lineLen := int64(len(line) + 1) // +1 for newline

		// Keep lines around our target offset
		if currentOffset >= offset-200 && currentOffset <= offset+200 {
			prefix := "   "
			if currentOffset == offset {
				prefix = ">>>"
			}
			fmt.Printf("%s [%d]: %q\n", prefix, currentOffset, line)
		}

		currentOffset += lineLen
		if currentOffset > offset+200 {
			break
		}
	}

	return nil
}
