// Package main provides a command-line utility to import legacy SQLite databases
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var appVersion = "-unset-"

// importOptions holds the flags that the import steps after the database open need.
type importOptions struct {
	resetGroups     bool
	dataDir         string
	legacyPath      string
	sqliteDir       string
	threads         int
	useShortHashLen int
	nntphostname    string
}

func main() {
	os.Exit(run())
}

// run parses the flags, opens the database, runs the import and always tears down
// the processor and the database before it returns the exit code.
func run() int {
	config.AppVersion = appVersion
	log.Printf("Starting go-pugleaf RSLIGHT-IMPORT (version: %s)", config.AppVersion)
	var (
		resetGroups     = flag.Bool("YESresetallgroupsYES", false, "this will reset all message counters to 0 and empty out all! repeat! ALL! newsgroups articles, threads, overview...")
		dataDir         = flag.String("data", "./data", "Directory to store NEW database files")
		legacyPath      = flag.String("etc", "", "Path to legacy RockSolid Light configs to import sections (/etc/rslight) ")
		sqliteDir       = flag.String("spool", "", "Path to legacy RockSolid spool directory containing SQLite files *.db3")
		threads         = flag.Int("threads", 1, "parallel import threads (default: 1)")
		useShortHashLen = flag.Int("useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
		nntphostname    = flag.String("nntphostname", "", "your hostname must be set")
		useHistory      = flag.Bool("history", true, "maintain and use the message-id history index (data/history)")
		pprofAddr       = flag.String("pprof", "", "serve net/http/pprof on this loopback address (e.g. localhost:6060); empty = off")
	)

	flag.Parse()
	history.ENABLE_HISTORY = *useHistory

	if *dataDir == "" || *legacyPath == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s [-data <path>] [-etc <path>] [-spool <path>] [-resetallgroups]\n", os.Args[0])
		fmt.Println("          -data <path>            Directory to store NEW database files (default: ./data)")
		fmt.Println("          -etc <path>             Path to legacy RockSolid Light configs (default: /etc/rslight)")
		fmt.Println("          -spool <path>           Path to legacy RockSolid spool directory (default: /var/spool/rslight)")
		fmt.Println("          -threads <n>            Number of parallel import threads (default: 1)")
		fmt.Println("          -use-short-hash-len <n> short hash length for history storage (2-7, default: 7)")
		fmt.Println("          -nntphostname <host>    your NNTP hostname (required)")
		fmt.Println("          -pprof <addr>           serve pprof on a loopback address, e.g. localhost:6060 (default: off)")
		fmt.Println("          -YESresetallgroupsYES   ⚠️  DANGER: Permanently deletes ALL articles, threads,")
		fmt.Println("                                  overview, and cache data from ALL newsgroups!")
		fmt.Println("                                  Resets all message counters to 0. Cannot be undone!")
		fmt.Println("                                  Delete data/history folder manually!")
		return 1
	}

	if *pprofAddr != "" {
		addr, err := loopbackPprofAddr(*pprofAddr)
		if err != nil {
			log.Printf("[RSLIGHT-IMPORT] %v", err)
			return 1
		}
		pprofSrv := &http.Server{Addr: addr, Handler: newPprofMux(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			log.Printf("[RSLIGHT-IMPORT] Starting pprof server on %s", addr)
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("[RSLIGHT-IMPORT] pprof server failed: %v", err)
			}
		}()
		defer func() {
			if err := pprofSrv.Close(); err != nil {
				log.Printf("[RSLIGHT-IMPORT] pprof server close: %v", err)
			}
		}()
	}

	opts := importOptions{
		resetGroups:     *resetGroups,
		dataDir:         *dataDir,
		legacyPath:      *legacyPath,
		sqliteDir:       *sqliteDir,
		threads:         *threads,
		useShortHashLen: *useShortHashLen,
		nntphostname:    *nntphostname,
	}

	mainConfig := config.NewDefaultConfig()
	mainConfig.AppVersion = appVersion
	mainConfig.Server.Hostname = opts.nntphostname
	log.Printf("Starting go-pugleaf RSLIGHT-IMPORT (version: %s)", appVersion)

	// Create database configuration
	dbconfig := database.DefaultDBConfig()
	dbconfig.DataDir = opts.dataDir

	// Initialize database
	db, err := database.OpenDatabase(dbconfig)
	if err != nil {
		log.Printf("Failed to initialize database: %v", err)
		return 1
	}

	proc, err := runImport(db, opts)
	code := 0
	if err != nil {
		log.Printf("[RSLIGHT-IMPORT] ERROR: %v", err)
		code = 1
	}
	if !shutdown(proc, db) {
		code = 1
	}
	if code == 0 {
		log.Printf("RSLIGHT import completed successfully!")
	}
	return code
}

// runImport runs every step after the database open. It returns the importer (nil if it
// was not created) so the caller can close it on every path.
func runImport(db *database.Database, opts importOptions) (*processor.LegacyImporter, error) {
	// Set hostname in processor with database fallback support
	if err := processor.SetHostname(opts.nntphostname, db); err != nil {
		return nil, fmt.Errorf("failed to set NNTP hostname: %w", err)
	}

	// Handle UseShortHashLen configuration with locking
	lockedHashLen, isLocked, err := db.GetHistoryUseShortHashLen(opts.useShortHashLen)
	if err != nil {
		return nil, fmt.Errorf("failed to get locked history hash length: %w", err)
	}

	if !isLocked {
		// First run - lock the value from command line
		log.Printf("Locking UseShortHashLen to %d (first run)", opts.useShortHashLen)
		if err := db.SetHistoryUseShortHashLen(opts.useShortHashLen); err != nil {
			return nil, fmt.Errorf("failed to lock history hash length: %w", err)
		}
		lockedHashLen = opts.useShortHashLen
	} else {
		// Subsequent runs - validate against locked value
		if opts.useShortHashLen != lockedHashLen {
			log.Printf("WARNING: Command line UseShortHashLen (%d) differs from locked value (%d). Using locked value to prevent data corruption.", opts.useShortHashLen, lockedHashLen)
		} else {
			log.Printf("Using locked UseShortHashLen: %d", lockedHashLen)
		}
	}

	// Create legacy RockSolid Light proc
	common.UseStrictGroupValidation = false // Disable strict validation for legacy imports (allows uppercase groups and other legacy quirks)
	processor.RunRSLIGHTImport = true       // Enable RSLIGHT import mode
	proc, err := processor.NewLegacyImporter(db, opts.legacyPath, opts.sqliteDir, lockedHashLen)
	if err != nil {
		return nil, err
	}
	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate

	log.Printf("Starting rslight sections import from: %s", opts.legacyPath)
	if err := proc.ImportSections(); err != nil {
		return proc, fmt.Errorf("failed to import rslight sections: %w", err)
	}

	log.Printf("Sections import completed!")

	// Show summary
	if err := proc.GetSectionsSummary(); err != nil {
		return proc, fmt.Errorf("failed to get sections summary: %w", err)
	}

	// Handle reset groups operation if requested
	if opts.resetGroups {
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
		if err := db.ResetAllNewsgroupData(); err != nil {
			return proc, fmt.Errorf("failed to reset newsgroup data: %w", err)
		}

		log.Printf("*** NEWSGROUP RESET COMPLETED SUCCESSFULLY ***")
		log.Printf("All newsgroup articles, threads, overview, and cache data has been deleted.")
		log.Printf("All message counters have been reset to 0.")
		log.Printf("The system is now ready for fresh data import.")
		return proc, nil
	}

	if opts.sqliteDir == "" {
		log.Printf("exiting here. ./rslight-proc -spool dir not set")
		return proc, nil
	}

	// Check if SQLite directory exists
	if _, err := os.Stat(opts.sqliteDir); err != nil {
		return proc, fmt.Errorf("SQLite directory %s: %w", opts.sqliteDir, err)
	}

	log.Printf("Starting SQLite import from: %s", opts.sqliteDir)
	log.Printf("Target database directory: %s", opts.dataDir)

	if err := proc.ImportAllSQLiteDatabases(opts.sqliteDir, opts.threads); err != nil {
		return proc, fmt.Errorf("failed to import SQLite databases: %w", err)
	}
	log.Printf("[RSLIGHT-IMPORT] SUCCESS! Import process completed! Shutting down systems...")
	return proc, nil
}

// shutdown tears down the importer and the database; it reports false on any error.
//
// SHUTDOWN ORDER EXPLANATION:
// The processor and database are tightly coupled - the processor depends on the database
// for all operations, but the database also has background workers that may still be
// processing data written by the processor. The correct shutdown order is critical:
//
//  1. PROCESSOR FIRST: Close the processor (and its history system) to:
//     - Stop accepting new articles for processing
//     - Flush all pending history writes to disk
//     - Ensure history files are complete and not truncated to 0 bytes
//     - Clean up processing goroutines and channels
//
//  2. DATABASE WORKERS: Wait for database background tasks to complete:
//     - Let batch inserters finish processing any queued articles
//     - Allow database maintenance tasks to complete
//     - Ensure all data is properly committed
//
//  3. DATABASE LAST: Final database shutdown:
//     - Close all database connections
//     - Perform final cleanup and integrity checks
//
// This order prevents race conditions where the database tries to close while
// the processor is still writing to history, which was causing 0-byte history files.
func shutdown(proc *processor.LegacyImporter, db *database.Database) (ok bool) {
	ok = true
	// Step 1: Close the proc/processor first (flushes history, stops processing)
	if proc != nil {
		if err := proc.Close(); err != nil {
			log.Printf("[RSLIGHT-IMPORT] Warning: Failed to close proc: %v", err)
			ok = false
		} else {
			log.Printf("[RSLIGHT-IMPORT] proc/processor closed successfully")
		}
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
		log.Printf("Failed to shutdown database: %v", err)
		return false
	}
	log.Printf("Database shutdown successfully")
	return ok
}
