package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var (
	nntphostname    string
	nntptcpport     int
	nntptlsport     int
	nntpcertFile    string
	nntpkeyFile     string
	useShortHashLen int
	maxConnections  int
	dataDir         string
	useHistory      bool
)

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	log.Printf("Starting go-pugleaf dedicated NNTP Server (version: %s)", config.AppVersion)

	flag.StringVar(&nntphostname, "nntphostname", "", "Your hostname must be set!")
	flag.BoolVar(&useHistory, "history", true, "maintain and use the message-id history index (data/history)")
	flag.IntVar(&nntptcpport, "nntptcpport", 0, "NNTP TCP port")
	flag.IntVar(&nntptlsport, "nntptlsport", 0, "NNTP TLS port")
	flag.StringVar(&nntpcertFile, "nntpcertfile", "", "NNTP TLS certificate file (/path/to/fullchain.pem)")
	flag.StringVar(&nntpkeyFile, "nntpkeyfile", "", "NNTP TLS key file (/path/to/privkey.pem)")
	flag.IntVar(&useShortHashLen, "useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
	flag.IntVar(&maxConnections, "maxconnections", 500, "allow max of N authenticated connections (default: 500)")
	flag.StringVar(&dataDir, "data", "./data", "Directory to store database files")
	flag.Parse()
	history.ENABLE_HISTORY = useHistory

	mainConfig := config.NewDefaultConfig()
	mainConfig.Server.NNTP.Enabled = true
	// Override config with command-line flags if provided
	if nntptcpport > 0 {
		mainConfig.Server.NNTP.Port = nntptcpport
		log.Printf("[NNTP]: Overriding NNTP TCP port with command-line flag: %d", mainConfig.Server.NNTP.Port)
	} else {
		mainConfig.Server.NNTP.Port = 0
		log.Printf("[NNTP]: No NNTP TCP port flag provided")
	}
	if nntptlsport > 0 {
		mainConfig.Server.NNTP.TLSPort = nntptlsport
		mainConfig.Server.NNTP.TLSCert = nntpcertFile
		mainConfig.Server.NNTP.TLSKey = nntpkeyFile
	} else {
		mainConfig.Server.NNTP.TLSPort = 0
		mainConfig.Server.NNTP.TLSCert = ""
		mainConfig.Server.NNTP.TLSKey = ""
		log.Printf("[NNTP]: No NNTP TLS port flag provided")
	}

	// Note: hostname can be empty here since SetHostname will check database for fallback
	if maxConnections <= 0 {
		log.Fatalf("[NNTP]: Error: max connections must be greater than 0")
	}
	if maxConnections > 500 { // Default is 500, but allow higher if specified
		log.Printf("[NNTP]: WARNING! Setting max connections to %d: You may hit filedescriptor limits! rise ulimit -n to maxConnections * 2 !", maxConnections)
	}
	mainConfig.Server.Hostname = nntphostname
	mainConfig.Server.NNTP.MaxConns = maxConnections
	log.Printf("[NNTP]: Using NNTP configuration %#v", mainConfig.Server.NNTP)

	// Initialize database (this would normally come from your main application)
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = dataDir

	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Add waitgroup coordination for proper shutdown
	wg := &sync.WaitGroup{}

	// Set hostname in processor with database fallback support
	if err := processor.SetHostname(nntphostname, db); err != nil {
		log.Fatalf("[NNTP]: Failed to set NNTP hostname: %v", err)
	}

	// Validate command-line flag
	if useShortHashLen < 2 || useShortHashLen > 7 {
		log.Fatalf("Invalid UseShortHashLen: %d (must be between 2 and 7)", useShortHashLen)
	}

	// Get or set history UseShortHashLen configuration (default to 3 for standalone)
	finalUseShortHashLen, isLocked, err := db.GetHistoryUseShortHashLen(useShortHashLen)
	if err != nil {
		log.Fatalf("Failed to get history configuration: %v", err)
	}

	if !isLocked {
		// First time setup - store the default value
		if err := db.SetHistoryUseShortHashLen(useShortHashLen); err != nil {
			log.Fatalf("Failed to set history configuration: %v", err)
		}
		finalUseShortHashLen = useShortHashLen
		log.Printf("History UseShortHashLen initialized to %d", finalUseShortHashLen)
	} else {
		log.Printf("Using stored history UseShortHashLen: %d", finalUseShortHashLen)
	}

	// Initialize processor for article handling (needed for POST/IHAVE/TAKETHIS)
	// For dedicated NNTP server, we don't need NNTP backend pools for fetching,
	// but we do need a processor for handling incoming articles
	proc := processor.NewProcessor(db, nil, finalUseShortHashLen) // nil NNTP pool since we're receiving, not fetching

	// Create and start NNTP server with processor support
	processorAdapter := NewProcessorAdapter(proc)
	nntpServer, err := nntp.NewNNTPServer(db, &mainConfig.Server, wg, processorAdapter)
	if err != nil {
		log.Fatalf("Failed to create NNTP server: %v", err)
	}
	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate

	if err := nntpServer.Start(); err != nil {
		log.Fatalf("Failed to start NNTP server: %v", err)
	}

	log.Println("NNTP server started")

	// Wait for interrupt signal to gracefully shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down NNTP server...")
	if err := nntpServer.Stop(); err != nil {
		log.Printf("Error shutting down NNTP server: %v", err)
	}
	wg.Wait()
	log.Printf("[NNTP]: Server stopped, initiating database shutdown...")

	// Signal database background workers (batch orchestrators) to stop
	close(db.StopChan)

	// Close the processor
	if err := proc.Close(); err != nil {
		log.Printf("[NNTP]: Warning: Failed to close processor: %v", err)
	} else {
		log.Printf("[NNTP]: Processor closed successfully")
	}

	log.Printf("[NNTP]: Database: waiting for background tasks to finish...")
	db.WG.Wait()
	log.Printf("[NNTP]: Database: all background tasks completed")

	if err := db.Shutdown(); err != nil {
		log.Printf("[NNTP]: Failed to shutdown database: %v", err)
	} else {
		log.Printf("[NNTP]: Database shutdown successfully")
	}
	log.Println("NNTP server stopped")
}
