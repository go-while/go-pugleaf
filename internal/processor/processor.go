package processor

// Package importer provides functionality to import newsgroup data from an NNTP server
// into a local database, handling both overview and article imports.

import (
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

const DebugProcessorHeaders bool = false // flag for legacy header processing

type Processor struct {
	DB   *database.Database
	Pool *nntp.Pool
	// Cache         *MsgTmpCache // REMOVED: Migrated to MsgIdCache
	ThreadCounter *Counter
	LegacyCounter *Counter // Counter for legacy imports, if needed
	//HisResChan    chan int                // Channel for history responses
	History       *history.History        // History system for duplicate detection
	MsgIdCache    *history.MsgIdItemCache // Cache for message ID items
	BridgeManager *BridgeManager          // Bridge manager for Fediverse/Matrix (optional)
}

var (
	// these list of ' var ' can be set after importing the lib before starting!!

	// MaxBatch defines the maximum number of articles to fetch in a single batch
	MaxBatchSize int64 = 100

	// UseStrictGroupValidation for group names, false allows upper-case in group names
	UseStrictGroupValidation = true

	// must be set to true before booting and running DownloadArticlesViaOverview or ImportOverview!
	XoverCopy = false

	// RunRSLIGHTImport is used to indicate if the importer should run the legacy RockSolid Light importer
	RunRSLIGHTImport = false
	DownloadMaxPar   = 16

	// Global Batch Queue (proc_DLArt.go)
	Batch = &BatchQueue{
		Check:       make(chan *string),           // check newsgroups
		TodoQ:       make(chan *nntp.GroupInfo),   // todo newsgroups
		GetQ:        make(chan *BatchItem),        // get articles, blocking channel
		GroupQueues: make(map[string]*GroupBatch), // per-newsgroup queues
	}

	// Do NOT change this here! these are needed for runtime !
	// validGroupNameRegex validates newsgroup names according to RFC standards
	// Pattern: lowercase alphanumeric start, components separated by dots, no trailing dots/hyphens
	validGroupNameRegexStrict = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(?:\.[a-z0-9][a-z0-9-]*)+$`)
	validGroupNameRegexchar   = regexp.MustCompile(`^[a-zA-Z0-9]{1,255}$`)
	validGroupNameRegexLazy   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+&-]*$`)
	validGroupNameRegexSingle = regexp.MustCompile(`^[A-Za-z0-9-_+&][A-Za-z0-9-_+&]{1,64}$`)
	validGroupNameRegexCaps   = regexp.MustCompile(`^[A-Za-z0-9-_+&][A-Za-z0-9-_+&]*(?:\.[A-Za-z0-9-_+&][A-Za-z0-9-_+&]*)+$`)
	//errorUp2date              = fmt.Errorf("up2date")
)

func NewProcessor(db *database.Database, nntpPool *nntp.Pool, useShortHashLen int) *Processor {
	if LocalHostnamePath == "" {
		log.Fatalf("FATAL: LocalHostnamePath is not set, please configure the hostname before starting the processor")
	}

	// Perform comprehensive hostname validation (similar to INN2)
	if err := validateHostname(LocalHostnamePath); err != nil {
		log.Fatalf("FATAL: Hostname validation failed: %v", err)
	}
	// Ensure main database exists and is properly set up before any import operations
	if err := db.Migrate(); err != nil {
		log.Printf("Warning: Failed to ensure main database setup: %v", err)
	}

	// Initialize history system with 16-DB sharding
	historyConfig := &history.HistoryConfig{
		HistoryDir:      "data/history",
		CacheExpires:    60,                   // HARDCODED
		CachePurge:      15,                   // HARDCODED
		ShardMode:       history.SHARD_16_256, // CAN NOT BE CHANGED ! ! 16 databases with 256 tables each
		MaxConnections:  8,                    // HARDCODED
		UseShortHashLen: useShortHashLen,      // Use the configurable short hash length
	}

	hist, err := history.NewHistory(historyConfig, db.WG)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize history system: %v", err)
	}

	proc := &Processor{
		DB:            db,
		Pool:          nntpPool,
		ThreadCounter: NewCounter(),
		LegacyCounter: NewCounter(),
		History:       hist,
		MsgIdCache:    history.NewMsgIdItemCache(),
		BridgeManager: nil, // Initialize as nil (disabled by default)
	}

	proc.DB.Batch.SetProcessor(proc) // Set the processor in the database instance for threading checks

	// Set up bidirectional connection: History can check if database batch system has work
	hist.SetDatabaseWorkChecker(proc.DB.Batch)

	// Start cache cleanup routine for automatic memory management
	if proc.MsgIdCache != nil {
		proc.MsgIdCache.StartCleanupRoutine()
	}

	// DISABLED: threading processor - now handled by SQ3CronProcessThreading in db_batch.go
	return proc
}

func (proc *Processor) CheckNoMoreWorkInHistory() bool {
	return proc.History.CheckNoMoreWorkInHistory()
}

// AddProcessedArticleToHistory adds a successfully processed article to history with correct group and article number
func (proc *Processor) AddProcessedArticleToHistory(msgIdItem *history.MessageIdItem, newsgroupPtr *string, articleNumber int64) {
	if msgIdItem == nil || newsgroupPtr == nil {
		//log.Print("ERROR: addProcessedArticleToHistory called with nil MessageIdItem or newsgroupPtr")
		return
	}
	if *newsgroupPtr == "" || articleNumber <= 0 {
		//log.Printf("ERROR: addProcessedArticleToHistory called with invalid parameters: newsgroupPtr='%s', articleNumber=%d msgIdItem='%#v'", *newsgroupPtr, articleNumber, msgIdItem)
		return
	}

	msgIdItem.Mux.Lock()
	if msgIdItem.FileOffset > 0 || msgIdItem.ArtNum > 0 || msgIdItem.GroupName != nil {
		msgIdItem.Response = history.CaseDupes
		msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
		//log.Printf("ERROR: addProcessedArticleToHistory called with existing FileOffset %d or ArtNum %d or GroupName '%v', ignoring new values for msgIdItem='%#v'", msgIdItem.FileOffset, msgIdItem.ArtNum, *msgIdItem.GroupName, msgIdItem)
		msgIdItem.Mux.Unlock()
		return
	}
	if msgIdItem.GroupName == nil && msgIdItem.ArtNum <= 0 {
		msgIdItem.GroupName = newsgroupPtr
		msgIdItem.ArtNum = articleNumber // Set article number if not already set
		//msgIdItem.StorageToken = fmt.Sprintf("%s:%d", *newsgroupPtr, articleNumber) // Set the storage token in the item
	} else {
		msgIdItem.Response = history.CaseDupes
		msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
		//log.Printf("WARNING: addProcessedArticleToHistory called with existing GroupName '%s' or ArtNum %d, ignoring new values for msgIdItem='%#v'", *msgIdItem.GroupName, msgIdItem.ArtNum, msgIdItem)
		msgIdItem.Mux.Unlock()
		return
	}
	msgIdItem.Mux.Unlock()

	// Add to history channel
	proc.History.Add(msgIdItem)
}

// FindThreadRootInCache - public wrapper for the interface
func (proc *Processor) FindThreadRootInCache(newsgroupPtr *string, refs []string) *database.MsgIdTmpCacheItem {
	item := proc.MsgIdCache.FindThreadRootInCache(newsgroupPtr, refs)
	if item == nil {
		return nil
	}

	// Convert from history.MessageIdItem to database.MsgIdTmpCacheItem for interface compatibility
	item.Mux.RLock()
	defer item.Mux.RUnlock()

	// Get group-specific threading information
	threadingInfo, exists := item.GroupThreading[newsgroupPtr]
	if !exists {
		return nil // No threading info for this group
	}

	result := &database.MsgIdTmpCacheItem{
		MessageId:    item.MessageId,
		ArtNum:       threadingInfo.ArtNum,
		RootArticle:  threadingInfo.RootArticle,
		IsThreadRoot: threadingInfo.IsThreadRoot,
	}

	return result
}

// GetHistoryStats returns current history statistics
func (proc *Processor) GetHistoryStats() history.HistoryStats {
	if proc.History != nil {
		return proc.History.GetStats()
	}
	return history.HistoryStats{}
}

// Close gracefully shuts down the processor and history system
func (proc *Processor) Close() error {
	log.Printf("Shutting down processor...")

	// Wait for all batch processing to complete before closing
	proc.WaitForBatchCompletion()

	// Close history system
	if proc.History != nil {
		return proc.History.Close()
	}

	return nil
}

// WaitForBatchCompletion waits for all pending batch operations to complete
// This should be called before closing the processor to ensure all articles are processed
func (proc *Processor) WaitForBatchCompletion() {
	if proc.DB == nil || proc.DB.Batch == nil {
		return
	}

	log.Printf("[PROCESSOR] Waiting for all batch processing to complete...")

	maxWaitTime := 60 * time.Second // Maximum wait time
	checkInterval := 1 * time.Second
	startTime := time.Now()

	for {
		if proc.DB.Batch.CheckNoMoreWorkInMaps() {
			log.Printf("[PROCESSOR] All batch processing completed")
			return
		}

		elapsed := time.Since(startTime)
		if elapsed > maxWaitTime {
			log.Printf("[PROCESSOR] Warning: Timeout waiting for batch completion after %v", elapsed)
			return
		}

		// Show progress every 5 seconds
		if int(elapsed.Seconds())%5 == 0 {
			log.Printf("[PROCESSOR] Still waiting for batch processing... (elapsed: %v)", elapsed)
		}

		time.Sleep(checkInterval)
	}
}

// Public methods for NNTP server integration

// Lookup looks up a message-ID in history and returns the storage token in the item
func (proc *Processor) Lookup(msgIdItem *history.MessageIdItem) (int, error) {
	return proc.History.Lookup(msgIdItem)
}

/*
// AddArticleToHistory adds an article to history (public wrapper)
func (proc *Processor) AddArticleToHistory(article *nntp.Article, newsgroup string) {
	proc.addArticleToHistory(article, newsgroup)
}
*/
// ProcessIncomingArticle processes an incoming article and stores it in the database
func (proc *Processor) ProcessIncomingArticle(article *models.Article) (int, error) {
	if article == nil {
		return history.CaseError, fmt.Errorf("null article")
	}

	// Make sure we have at least one newsgroup
	if len(article.NewsgroupsPtr) == 0 {
		return history.CaseError, fmt.Errorf("no newsgroups specified in article")
	}

	// Use the primary newsgroup for processing
	// The article will be available in all cross-posted groups
	log.Printf("Processing incoming article with Message-ID: %s for primary newsgroup %s (cross-posted to %d groups)",
		article.MessageID, *article.NewsgroupsPtr[0], len(article.NewsgroupsPtr))

	bulkmode := false
	return proc.processArticle(article, *article.NewsgroupsPtr[0], bulkmode)
}

// EnableBridges enables Fediverse and/or Matrix bridges with the given configuration
func (proc *Processor) EnableBridges(config *BridgeConfig) {
	if config == nil {
		log.Printf("Processor: Bridge configuration is nil, bridges remain disabled")
		return
	}

	proc.BridgeManager = NewBridgeManager(config)
	log.Printf("Processor: Bridges initialized (Fediverse: %v, Matrix: %v)",
		config.FediverseEnabled, config.MatrixEnabled)
}

// DisableBridges disables all bridges
func (proc *Processor) DisableBridges() {
	if proc.BridgeManager != nil {
		proc.BridgeManager.Close()
		proc.BridgeManager = nil
		log.Printf("Processor: All bridges disabled")
	}
}

// SetHostname sets and validates the hostname for NNTP operations
// This must be called before creating a new processor
func SetHostname(hostname string) error {
	if err := validateHostname(hostname); err != nil {
		return fmt.Errorf("hostname validation failed: %v", err)
	}

	LocalHostnamePath = hostname
	log.Printf("Hostname set and validated: %s", hostname)
	return nil
}

// GetHostname returns the currently configured hostname
func GetHostname() string {
	return LocalHostnamePath
}

// validateHostname performs comprehensive hostname validation similar to INN2
// It checks:
// 1. Hostname format and RFC compliance
// 2. DNS resolution (A/AAAA records)
// 3. Reverse DNS resolution consistency
// 4. Comparison with system hostname
func validateHostname(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("hostname cannot be empty")
	}

	// Basic format validation
	if len(hostname) > 253 {
		return fmt.Errorf("hostname too long (max 253 characters): %s", hostname)
	}

	// Check for valid hostname format (RFC 1123)
	hostnameRegex := regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)
	if !hostnameRegex.MatchString(hostname) {
		return fmt.Errorf("invalid hostname format: %s", hostname)
	}

	// Don't allow localhost variations in production
	lowercaseHost := strings.ToLower(hostname)
	if lowercaseHost == "localhost" || lowercaseHost == "localhost.localdomain" {
		return fmt.Errorf("hostname cannot be localhost in production: %s", hostname)
	}

	// Check for IP addresses (not allowed as hostnames)
	if net.ParseIP(hostname) != nil {
		return fmt.Errorf("hostname cannot be an IP address: %s", hostname)
	}

	// Perform DNS resolution to verify the hostname exists
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("DNS resolution failed for hostname '%s': %v", hostname, err)
	}

	if len(ips) == 0 {
		return fmt.Errorf("no DNS records found for hostname: %s", hostname)
	}

	log.Printf("Hostname '%s' resolved to %d IP address(es): %v", hostname, len(ips), ips)

	// Perform reverse DNS checks for consistency
	var reverseOK bool
	for _, ip := range ips {
		names, err := net.LookupAddr(ip.String())
		if err != nil {
			log.Printf("Warning: Reverse DNS lookup failed for IP %s: %v", ip.String(), err)
			continue
		}

		// Check if any of the reverse DNS names match our hostname
		for _, name := range names {
			// Remove trailing dot if present
			name = strings.TrimSuffix(name, ".")
			if strings.EqualFold(name, hostname) {
				reverseOK = true
				log.Printf("Reverse DNS verification successful: %s -> %s -> %s", hostname, ip.String(), name)
				break
			}
		}
		if reverseOK {
			break
		}
	}

	if !reverseOK {
		// This is a warning, not a fatal error, as some valid setups may not have proper reverse DNS
		log.Printf("Warning: Reverse DNS verification failed for hostname '%s'. This may cause issues with some NNTP peers.", hostname)
	}

	// Compare with system hostname (informational)
	systemHostname, err := os.Hostname()
	if err == nil {
		if !strings.EqualFold(hostname, systemHostname) {
			log.Printf("Info: Configured hostname '%s' differs from system hostname '%s'", hostname, systemHostname)
		} else {
			log.Printf("Info: Configured hostname matches system hostname: %s", hostname)
		}
	}

	// Ensure hostname contains at least one dot (FQDN requirement for NNTP)
	if !strings.Contains(hostname, ".") {
		return fmt.Errorf("hostname must be fully qualified (contain at least one dot): %s", hostname)
	}

	log.Printf("Hostname validation successful: %s", hostname)
	return nil
}
