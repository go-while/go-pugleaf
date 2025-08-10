// Package main provides a command-line utility to import legacy SQLite databases
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	prof "github.com/go-while/go-cpu-mem-profiler"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var Prof *prof.Profiler

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	log.Printf("Starting go-pugleaf RSLIGHT-IMPORT (version: %s)", config.AppVersion)
	Prof = prof.NewProf()
	go Prof.PprofWeb(":51111")
	Prof.StartMemProfile(5*time.Minute, 30*time.Second)
	var (
		resetGroups     = flag.Bool("YESresetallgroupsYES", false, "this will reset all message counters to 0 and empty out all! repeat! ALL! newsgroups articles, threads, overview...")
		dataDir         = flag.String("data", "./data", "Directory to store NEW database files")
		legacyPath      = flag.String("etc", "", "Path to legacy RockSolid Light configs to import sections (/etc/rslight) ")
		sqliteDir       = flag.String("spool", "", "Path to legacy RockSolid spool directory containing SQLite files *.db3")
		threads         = flag.Int("threads", 1, "parallel import threads (default: 1)")
		useShortHashLen = flag.Int("useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
		hostnamePath    = flag.String("nntphostname", "", "your hostname must be set")
	)

	flag.Parse()

	if *dataDir == "" || *legacyPath == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s [-data <path>] [-etc <path>] [-spool <path>] [-resetallgroups]\n", os.Args[0])
		fmt.Println("          -data <path>            Directory to store NEW database files (default: ./data)")
		fmt.Println("          -etc <path>             Path to legacy RockSolid Light configs (default: /etc/rslight)")
		fmt.Println("          -spool <path>           Path to legacy RockSolid spool directory (default: /var/spool/rslight)")
		fmt.Println("          -threads <n>            Number of parallel import threads (default: 1)")
		fmt.Println("          -use-short-hash-len <n> short hash length for history storage (2-7, default: 3)")
		fmt.Println("          -nntphostname <host>    your NNTP hostname (required)")
		fmt.Println("          -YESresetallgroupsYES   ⚠️  DANGER: Permanently deletes ALL articles, threads,")
		fmt.Println("                                  overview, and cache data from ALL newsgroups!")
		fmt.Println("                                  Resets all message counters to 0. Cannot be undone!")
		fmt.Println("                                  Delete data/history folder manually!")
		os.Exit(1)
	}
	mainConfig := config.NewDefaultConfig()
	mainConfig.AppVersion = appVersion
	if *hostnamePath == "" {
		log.Fatalf("[RSLIGHT-IMPORT]: Error: hostname must be set!")
	}
	mainConfig.Server.Hostname = *hostnamePath
	processor.LocalHostnamePath = *hostnamePath
	log.Printf("Starting go-pugleaf RSLIGHT-IMPORT (version: %s)", appVersion)

	// Create database configuration
	dbconfig := database.DefaultDBConfig()
	dbconfig.DataDir = *dataDir

	// Initialize database
	db, err := database.OpenDatabase(dbconfig)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Run migrations to ensure sections tables exist
	if err := db.Migrate(); err != nil {
		log.Fatalf("Failed to run database migrations: %v", err)
	}

	// Handle UseShortHashLen configuration with locking
	lockedHashLen, isLocked, err := db.GetHistoryUseShortHashLen(*useShortHashLen)
	if err != nil {
		log.Fatalf("Failed to get locked history hash length: %v", err)
	}

	if !isLocked {
		// First run - lock the value from command line
		log.Printf("Locking UseShortHashLen to %d (first run)", *useShortHashLen)
		if err := db.SetHistoryUseShortHashLen(*useShortHashLen); err != nil {
			log.Fatalf("Failed to lock history hash length: %v", err)
		}
		lockedHashLen = *useShortHashLen
	} else {
		// Subsequent runs - validate against locked value
		if *useShortHashLen != lockedHashLen {
			log.Printf("WARNING: Command line UseShortHashLen (%d) differs from locked value (%d). Using locked value to prevent data corruption.", *useShortHashLen, lockedHashLen)
		} else {
			log.Printf("Using locked UseShortHashLen: %d", lockedHashLen)
		}
	}

	// Create legacy RockSolid Light proc
	processor.UseStrictGroupValidation = false // Disable strict validation for legacy imports (allows uppercase groups and other legacy quirks)
	processor.RunRSLIGHTImport = true          // Enable RSLIGHT import mode
	proc := processor.NewLegacyImporter(db, *legacyPath, *sqliteDir, lockedHashLen)
	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate

	log.Printf("Starting rslight sections import from: %s", *legacyPath)
	err = proc.ImportSections()
	if err != nil {
		log.Fatalf("Warning: Failed to import rslight sections: %v", err)
	}

	log.Printf("Sections import completed!")

	// Show summary
	err = proc.GetSectionsSummary()
	if err != nil {
		log.Fatalf("Warning: Failed to get sections summary: %v", err)
	}

	// Handle reset groups operation if requested
	if *resetGroups {
		log.Printf("*** RESET GROUPS REQUESTED ***")
		log.Printf("WARNING: This will permanently delete ALL articles, threads, overview data from ALL newsgroups!")
		log.Printf("Are you sure you want to continue? This operation cannot be undone!")
		log.Printf("Starting reset in 5 seconds... Press Ctrl+C to cancel")

		// Give user time to cancel
		for i := 5; i > 0; i-- {
			log.Printf("Reset starting in %d seconds...", i)
			time.Sleep(1 * time.Second)
		}

		log.Printf("Starting newsgroup data reset...")
		err = db.ResetAllNewsgroupData()
		if err != nil {
			log.Fatalf("Failed to reset newsgroup data: %v", err)
		}

		log.Printf("*** NEWSGROUP RESET COMPLETED SUCCESSFULLY ***")
		log.Printf("All newsgroup articles, threads, overview, and cache data has been deleted.")
		log.Printf("All message counters have been reset to 0.")
		log.Printf("The system is now ready for fresh data import.")
		os.Exit(0)
	}

	if *sqliteDir == "" {
		log.Printf("exiting here. ./rslight-proc -spool dir not set")
		os.Exit(0)
	}

	// Check if SQLite directory exists
	if _, err := os.Stat(*sqliteDir); os.IsNotExist(err) {
		log.Fatalf("SQLite directory does not exist: %s", *sqliteDir)
	}

	log.Printf("Starting SQLite import from: %s", *sqliteDir)
	log.Printf("Target database directory: %s", *dataDir)

	db.WG.Add(2) // Adds to wait group for db_batch.go cron jobs
	db.WG.Add(1) // Adds for history: one for writer worker
	err = proc.ImportAllSQLiteDatabases(*sqliteDir, *threads)
	if err != nil {
		log.Fatalf("Failed to import SQLite databases: %v", err)
	}
	log.Printf("[RSLIGHT-IMPORT] SUCCESS! Import process completed! Shutting down systems...")

	// SHUTDOWN ORDER EXPLANATION:
	// The processor and database are tightly coupled - the processor depends on the database
	// for all operations, but the database also has background workers that may still be
	// processing data written by the processor. The correct shutdown order is critical:
	//
	// 1. PROCESSOR FIRST: Close the processor (and its history system) to:
	//    - Stop accepting new articles for processing
	//    - Flush all pending history writes to disk
	//    - Ensure history files are complete and not truncated to 0 bytes
	//    - Clean up processing goroutines and channels
	//
	// 2. DATABASE WORKERS: Wait for database background tasks to complete:
	//    - Let batch inserters finish processing any queued articles
	//    - Allow database maintenance tasks to complete
	//    - Ensure all data is properly committed
	//
	// 3. DATABASE LAST: Final database shutdown:
	//    - Close all database connections
	//    - Perform final cleanup and integrity checks
	//
	// This order prevents race conditions where the database tries to close while
	// the processor is still writing to history, which was causing 0-byte history files.

	// Step 1: Close the proc/processor first (flushes history, stops processing)
	if err := proc.Close(); err != nil {
		log.Printf("[RSLIGHT-IMPORT] Warning: Failed to close proc: %v", err)
	} else {
		log.Printf("[RSLIGHT-IMPORT] proc/processor closed successfully")
	}

	// Step 2: Stop database background workers and wait for completion
	log.Printf("[RSLIGHT-IMPORT] FINISHED! initiate Database Shutdown")
	close(db.StopChan)

	log.Printf("[RSLIGHT-IMPORT] Database: waiting for background tasks to finish...")
	db.WG.Wait() // Wait for all background tasks to finish
	log.Printf("[RSLIGHT-IMPORT] Database: all background tasks completed")

	log.Printf("[RSLIGHT-IMPORT] Database: Shutdown initiated")
	// Step 3: Final database shutdown
	if err := db.Shutdown(); err != nil {
		log.Fatalf("Failed to shutdown database: %v", err)
	} else {
		log.Printf("Database shutdown successfully")
		log.Printf("RSLIGHT import completed successfully!")
	}
}
