package main

import (
	"crypto/tls"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

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

	idleTimeout         time.Duration
	articleDelay        time.Duration
	groupDelay          time.Duration
	errorDelay          time.Duration
	plaintextAuth       string
	readAuth            bool
	enforceUserMaxConns bool
	maxArtSize          int
)

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	log.Printf("Starting go-pugleaf dedicated NNTP Server (version: %s)", config.AppVersion)

	flag.StringVar(&nntphostname, "nntphostname", "", "Your hostname must be set!")
	flag.BoolVar(&useHistory, "history", true, "maintain and use the message-id history index (data/history)")
	flag.IntVar(&nntptcpport, "nntptcpport", 0, "NNTP TCP port")
	flag.IntVar(&nntptlsport, "nntptlsport", 0, "NNTP TLS port")
	flag.StringVar(&nntpcertFile, "nntpcertfile", "", "NNTP TLS certificate file for the TLS port and STARTTLS (/path/to/fullchain.pem)")
	flag.StringVar(&nntpkeyFile, "nntpkeyfile", "", "NNTP TLS key file for the TLS port and STARTTLS (/path/to/privkey.pem)")
	flag.IntVar(&useShortHashLen, "useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
	flag.IntVar(&maxConnections, "maxconnections", 500, "maximum number of concurrent client connections (all clients)")
	flag.StringVar(&dataDir, "data", "./data", "Directory to store database files")
	flag.DurationVar(&idleTimeout, "idletimeout", nntp.DefaultNNTPcliconnTimeout, "close a client connection after this long without a command (RFC 3977: at least 3m)")
	flag.DurationVar(&articleDelay, "articledelay", nntp.DefaultArticleCmdDelay, "delay before each ARTICLE/HEAD/BODY/STAT reply")
	flag.DurationVar(&groupDelay, "groupdelay", nntp.DefaultGroupCmdDelay, "delay before each GROUP reply")
	flag.DurationVar(&errorDelay, "errordelay", nntp.DefaultErrorDelay, "delay before each error reply (412/420/423/430/481/482/500/501)")
	flag.StringVar(&plaintextAuth, "plaintextauth", "accept", "AUTHINFO on connections without TLS: accept (not advertised), advertise or refuse (483)")
	flag.BoolVar(&readAuth, "readauth", false, "require AUTHINFO before reader commands (GROUP, ARTICLE, XOVER, ...)")
	flag.BoolVar(&enforceUserMaxConns, "enforce-user-maxconns", false, "limit authenticated connections per NNTP user to its maxconns value")
	flag.IntVar(&maxArtSize, "maxartsize", 0, "maximum article size in bytes (0 = config default 32768)")
	flag.Parse()
	history.ENABLE_HISTORY = useHistory

	plaintextPolicy, err := nntp.ParsePlaintextAuthPolicy(plaintextAuth)
	if err != nil {
		log.Fatalf("[NNTP]: -plaintextauth: %v", err)
	}
	if articleDelay < 0 || groupDelay < 0 || errorDelay < 0 {
		log.Fatalf("[NNTP]: -articledelay, -groupdelay and -errordelay must not be negative")
	}
	if idleTimeout <= 0 {
		log.Fatalf("[NNTP]: -idletimeout must be greater than 0")
	}
	if maxArtSize < 0 {
		log.Fatalf("[NNTP]: -maxartsize must not be negative")
	}

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
	} else {
		mainConfig.Server.NNTP.TLSPort = 0
		log.Printf("[NNTP]: No NNTP TLS port flag provided")
	}
	// The certificate serves the TLS port and STARTTLS on the plain port (RFC 4642).
	if (nntpcertFile == "") != (nntpkeyFile == "") {
		log.Fatalf("[NNTP]: -nntpcertfile and -nntpkeyfile must be given together")
	}
	mainConfig.Server.NNTP.TLSCert = nntpcertFile
	mainConfig.Server.NNTP.TLSKey = nntpkeyFile
	if nntpcertFile != "" {
		if _, err := tls.LoadX509KeyPair(nntpcertFile, nntpkeyFile); err != nil {
			log.Fatalf("[NNTP]: cannot load TLS certificate: %v", err)
		}
		log.Printf("[NNTP]: STARTTLS enabled (certificate %s)", nntpcertFile)
	}

	// Note: hostname can be empty here since SetHostname will check database for fallback
	if maxConnections <= 0 {
		log.Fatalf("[NNTP]: Error: max connections must be greater than 0")
	}
	if maxConnections > 500 { // Default is 500, but allow higher if specified
		log.Printf("[NNTP]: WARNING! Setting max connections to %d: You may hit filedescriptor limits! rise ulimit -n to maxConnections * 2 !", maxConnections)
	}
	mainConfig.Server.NNTP.MaxConns = maxConnections
	if maxArtSize > 0 {
		mainConfig.Server.NNTP.MaxArtSize = maxArtSize
	}
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
	// the greeting uses the resolved hostname (flag or database fallback)
	mainConfig.Server.Hostname = processor.LocalNNTPHostname

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
	nntpServer.IdleTimeout = idleTimeout
	nntpServer.ArticleCmdDelay = articleDelay
	nntpServer.GroupCmdDelay = groupDelay
	nntpServer.ErrorDelay = errorDelay
	nntpServer.PlaintextAuth = plaintextPolicy
	nntpServer.ReadRequiresAuth = readAuth
	nntpServer.EnforceUserMaxConns = enforceUserMaxConns
	log.Printf("[NNTP]: idletimeout=%s articledelay=%s groupdelay=%s errordelay=%s plaintextauth=%s readauth=%v enforce-user-maxconns=%v maxartsize=%d",
		idleTimeout, articleDelay, groupDelay, errorDelay, plaintextAuth, readAuth, enforceUserMaxConns, nntpServer.MaxArticleBytes())
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
