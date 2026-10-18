package processor

// Package importer provides functionality to import newsgroup data from an NNTP server
// into a local database, handling both overview and article imports.

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
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
	// AnalyzeDataDir is the -data root below which the analyzer keeps its overview
	// cache (<AnalyzeDataDir>/cache/<host>/). Empty: DB.GetDataDir() is used; with
	// neither set the cache functions return an error (no cwd-relative fallback).
	AnalyzeDataDir string

	// xrefMiss remembers group IDs of history tokens that the main DB does not know (a
	// deleted group), so XrefLocations does not query and log them on every served article.
	xrefMiss xrefMissCache
}

// xrefMissTTL is how long XrefLocations skips a group ID the main DB did not know.
const xrefMissTTL = 5 * time.Minute

// xrefMissMax bounds the negative cache: past it all entries are dropped.
const xrefMissMax = 4096

// xrefMissCache is the negative cache of unknown group IDs used by XrefLocations.
type xrefMissCache struct {
	mu sync.Mutex
	m  map[int64]time.Time // group ID -> expiry
}

// known reports whether id is a remembered miss that has not expired at now.
func (c *xrefMissCache) known(id int64, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.m[id]
	if !ok {
		return false
	}
	if now.After(exp) {
		delete(c.m, id)
		return false
	}
	return true
}

// add remembers id as unknown until now + xrefMissTTL; past xrefMissMax entries the map is reset.
func (c *xrefMissCache) add(id int64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= xrefMissMax {
		c.m = make(map[int64]time.Time)
	}
	c.m[id] = now.Add(xrefMissTTL)
}

var (
	DownloadMaxPar int = 10 // HARDCODED Maximum number of parallel newsgroups downloads

	// The maximum number of crossposts per article is the config key MaxCrossPosts
	// (Database.MaxCrossPosts, set on the admin settings page).

	// these list of ' var ' can be set after importing the lib before starting!!
	LocalNNTPHostname string = "" // Hostname must be set before processing articles

	// MaxBatch defines the maximum number of articles to fetch in a single batch
	MaxBatchSize int64 = 1000

	// RunRSLIGHTImport is used to indicate if the importer should run the legacy RockSolid Light importer
	RunRSLIGHTImport = false

	// Global Batch Queue (proc_DLArt.go)
	Batch = &BatchQueue{
		Check: make(chan *string, 1),                      // check newsgroups
		TodoQ: make(chan *nntp.GroupInfo, DownloadMaxPar), // todo newsgroups
		GetQ:  make(chan *BatchItem, 128),                 // get articles
	}
)

// Deprecated: use NewProcessorE. Kept for callers that still exit on error.
func NewProcessor(db *database.Database, nntpPool *nntp.Pool, useShortHashLen int) *Processor {
	proc, err := NewProcessorE(db, nntpPool, useShortHashLen)
	if err != nil {
		log.Fatalf("FATAL: %v", err)
	}
	return proc
}

// NewProcessorE creates a Processor and returns an error instead of exiting the process
// when the hostname is missing or invalid or the history system cannot be initialized.
// The hostname checks run before db is used.
func NewProcessorE(db *database.Database, nntpPool *nntp.Pool, useShortHashLen int) (*Processor, error) {
	if LocalNNTPHostname == "" {
		// Try to get hostname from database if not set
		if err := SetHostname("", db); err != nil {
			return nil, fmt.Errorf("hostname: LocalNNTPHostname is not set and could not be retrieved from database: %w", err)
		}
	}

	// Perform comprehensive hostname validation (similar to INN2)
	if err := validateHostname(LocalNNTPHostname); err != nil {
		return nil, fmt.Errorf("hostname validation: %w", err)
	}
	// Ensure main database exists and is properly set up before any import operations
	if err := db.Migrate(); err != nil {
		log.Printf("Warning: Failed to ensure main database setup: %v", err)
	}

	// Initialize history system with 16-DB sharding
	historyConfig := &history.HistoryConfig{
		HistoryDir:      filepath.Join(db.GetDataDir(), "history"),
		CacheExpires:    60,                   // HARDCODED
		CachePurge:      15,                   // HARDCODED
		ShardMode:       history.SHARD_16_256, // CAN NOT BE CHANGED ! ! 16 databases with 256 tables each
		MaxConnections:  8,                    // HARDCODED
		UseShortHashLen: useShortHashLen,      // Use the configurable short hash length
	}

	hist, err := history.NewHistory(historyConfig, db.WG)
	if err != nil {
		return nil, fmt.Errorf("history init: %w", err)
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
	return proc, nil
}

func (proc *Processor) CheckNoMoreWorkInHistory() bool {
	return proc.History.CheckNoMoreWorkInHistory()
}

// AddArticleToHistory records a committed article (message-id -> main-DB newsgroup ID and its
// article number there, 0 = unknown) in the history index.
// Called by the database batch system after the article was stored and numbered. No-op if history is disabled.
func (proc *Processor) AddArticleToHistory(messageID string, groupID, articleNum int64) {
	proc.History.AddArticleAt(messageID, groupID, articleNum)
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
	log.Printf("[PROCESSOR] WaitForBatchCompletion")
	defer log.Printf("[PROCESSOR] Quit WaitForBatchCompletion")

	//maxWaitTime := 60 * time.Second // Maximum wait time
	checkInterval := 1 * time.Second
	startTime := time.Now()

	for {
		if proc.DB.Batch.CheckNoMoreWorkInMaps() {
			log.Printf("[PROCESSOR] All batch processing completed")
			return
		}
		elapsed := time.Since(startTime)
		/* disabled: we want to wait indefinitely
		if elapsed > maxWaitTime {
			log.Printf("[PROCESSOR] Warning: Timeout waiting for batch completion after %v", elapsed)
			return
		}
		*/
		// Show progress every N seconds
		if int(elapsed.Seconds())%10 == 0 {
			log.Printf("[PROCESSOR] Still waiting for batch processing... (elapsed: %v)", elapsed)
		}
		time.Sleep(checkInterval)
	}
}

// Public methods for NNTP server integration

// CheckMessageID reports whether an offered message-id is wanted (IHAVE/TAKETHIS pre-check).
// Returns history.CaseRetry while another goroutine is processing it (CaseLock/CaseWrite),
// history.CaseDupes if it is known, history.CaseError on a history error and history.CasePass if it is new.
// It never changes the state of the cache item.
func (proc *Processor) CheckMessageID(messageID string) int {
	if messageID == "" {
		return history.CaseError
	}
	if history.MsgIdCache != nil {
		if item := history.MsgIdCache.GetORCreate(messageID); item != nil {
			item.Mux.RLock()
			response := item.Response
			item.Mux.RUnlock()
			switch response {
			case history.CaseLock, history.CaseWrite:
				return history.CaseRetry
			case history.CaseDupes:
				return history.CaseDupes
			}
		}
	}
	exists, err := proc.History.Exists(messageID)
	if err != nil {
		log.Printf("[PROCESSOR] CheckMessageID: history error for '%s': %v", messageID, err)
		return history.CaseError
	}
	if exists {
		return history.CaseDupes
	}
	return history.CasePass
}

// FindArticleByMessageID finds an article by message-id: the current group first,
// then the groups listed in the history index. The first hit wins.
// Returns an error wrapping nntp.ErrArticleNotFound if no group has the article.
func (proc *Processor) FindArticleByMessageID(messageID, currentGroup string) (*models.Article, error) {
	if messageID == "" {
		return nil, fmt.Errorf("empty message-id: %w", nntp.ErrArticleNotFound)
	}
	if currentGroup != "" {
		if article := proc.findArticleInGroup(messageID, currentGroup); article != nil {
			return article, nil
		}
	}
	groupIDs, err := proc.History.LookupGroups(messageID)
	if err != nil {
		return nil, fmt.Errorf("history lookup for '%s' failed: %w", messageID, err)
	}
	for _, groupID := range groupIDs {
		groupName, exists := database.NewsgroupDBsIDcache.GetNewsgroupNameByID(groupID, proc.DB)
		if !exists || groupName == "" || groupName == currentGroup {
			continue
		}
		if article := proc.findArticleInGroup(messageID, groupName); article != nil {
			return article, nil
		}
	}
	return nil, fmt.Errorf("message-id '%s': %w", messageID, nntp.ErrArticleNotFound)
}

// XrefLocations returns the groups and article numbers the history index holds for
// messageID, for the server's Xref: header (-xref full), in the order of the history row.
// Locations without a number (old rows, 0 = unknown until a history-rebuild) and group IDs
// the main DB does not know are skipped; an unknown ID is remembered for xrefMissTTL, so it
// is not queried (and logged) again per served article. nil, nil = nothing known or history
// disabled.
func (proc *Processor) XrefLocations(messageID string) ([]nntp.XrefLocation, error) {
	if proc == nil || proc.History == nil || messageID == "" {
		return nil, nil
	}
	locs, err := proc.History.LookupLocations(messageID)
	if err != nil {
		return nil, fmt.Errorf("history lookup for '%s' failed: %w", messageID, err)
	}
	return proc.xrefResolve(locs, func(id int64) (string, bool) {
		return database.NewsgroupDBsIDcache.GetNewsgroupNameByID(id, proc.DB)
	}, time.Now()), nil
}

// XrefLocationsBatch is XrefLocations for many message-ids with one batched history lookup
// (History.LookupLocationsBatch): the result holds an entry for every id the history index
// knows, with the same locations XrefLocations gives for it, or an empty non-nil slice when
// none of its locations resolve ("known, nothing to add"); ids the index does not know are
// absent. nil, nil = no ids or history disabled.
func (proc *Processor) XrefLocationsBatch(messageIDs []string) (map[string][]nntp.XrefLocation, error) {
	if proc == nil || proc.History == nil || len(messageIDs) == 0 {
		return nil, nil
	}
	found, err := proc.History.LookupLocationsBatch(messageIDs)
	if err != nil {
		return nil, fmt.Errorf("history batch lookup for %d message-ids failed: %w", len(messageIDs), err)
	}
	if found == nil {
		return nil, nil
	}
	return proc.xrefResolveBatch(found, func(id int64) (string, bool) {
		return database.NewsgroupDBsIDcache.GetNewsgroupNameByID(id, proc.DB)
	}, time.Now()), nil
}

// xrefResolveBatch runs xrefResolve for every id of a batched history lookup; an id whose
// locations all resolve to nothing gets an empty non-nil slice.
func (proc *Processor) xrefResolveBatch(found map[string][]history.Location, groupName func(id int64) (string, bool), now time.Time) map[string][]nntp.XrefLocation {
	out := make(map[string][]nntp.XrefLocation, len(found))
	for id, locs := range found {
		resolved := proc.xrefResolve(locs, groupName, now)
		if resolved == nil {
			resolved = []nntp.XrefLocation{}
		}
		out[id] = resolved
	}
	return out
}

// xrefResolve maps history locations to group names with groupName (the ID cache in
// XrefLocations), skipping numbers <= 0, unknown IDs and IDs in the negative cache at now.
func (proc *Processor) xrefResolve(locs []history.Location, groupName func(id int64) (string, bool), now time.Time) []nntp.XrefLocation {
	var out []nntp.XrefLocation
	for _, l := range locs {
		if l.ArticleNum <= 0 || proc.xrefMiss.known(l.GroupID, now) {
			continue
		}
		name, exists := groupName(l.GroupID)
		if !exists || name == "" {
			proc.xrefMiss.add(l.GroupID, now)
			continue
		}
		out = append(out, nntp.XrefLocation{Group: name, ArticleNum: l.ArticleNum})
	}
	return out
}

// findArticleInGroup returns the article from one newsgroup DB or nil
func (proc *Processor) findArticleInGroup(messageID, groupName string) *models.Article {
	groupDB, err := proc.DB.GetGroupDB(groupName)
	if err != nil || groupDB == nil {
		return nil
	}
	defer groupDB.Return()
	article, err := proc.DB.GetArticleByMessageID(groupDB, messageID)
	if err != nil {
		return nil
	}
	return article
}

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
	return proc.ProcessArticle(article, *article.NewsgroupsPtr[0], bulkmode)
}

// postWaitCommitted is the commit barrier of ProcessPostedArticle (a seam for tests).
var postWaitCommitted = func(sq *database.SQ3batch, m database.CommitMark, stop <-chan struct{}, d time.Duration) error {
	return sq.WaitCommitted(m, stop, d)
}

// postArticleStored reports whether the group database of group stores messageID; it is
// the authoritative check of ProcessPostedArticle (a seam for tests).
var postArticleStored = func(proc *Processor, group, messageID string) (bool, error) {
	groupDB, err := proc.DB.GetGroupDB(group)
	if err != nil {
		if groupDB != nil {
			groupDB.Return()
		}
		return false, err
	}
	defer groupDB.Return()
	return groupDB.LookupMsgIdInArticlesDB(messageID)
}

// postRecheckDelay is the pause before ProcessPostedArticle waits again for a group whose
// barrier was satisfied while the row is still absent, so such a round cannot spin.
const postRecheckDelay = 50 * time.Millisecond

// ProcessPostedArticle is ProcessIncomingArticle for POST (plan post-430-window): when the
// article was accepted (CasePass) it waits until the article's row is in every group
// database it was captured for, at most timeout in total (<= 0: no wait and no lookups;
// the article is still stored).
//
// A commit mark is taken for every name in article.NewsgroupsPtr before ProcessArticle,
// which counts the capture synchronously (NoteCaptured). After it returned, NewsgroupsPtr
// holds exactly the groups the article was captured for minus those whose batch already
// committed it (the batch writer removes a group only on commit, never on a drop), so only
// those groups are waited for. The barrier counts per group, not per article (other
// articles of the group can satisfy or fail it), so after every WaitCommitted that returns
// nil or ErrBatchDropped the row decides: present → the group is done; absent after nil →
// wait again with a fresh mark until the shared deadline; absent after ErrBatchDropped →
// CaseError wrapping common.ErrIntakeRejected ("not stored"). A timeout or a stop with rows
// still absent → CasePass (the article is queued and will be stored) plus one [INTAKE] log
// line. A failed row lookup counts as absent for that round.
func (proc *Processor) ProcessPostedArticle(article *models.Article, stop <-chan struct{}, timeout time.Duration) (int, error) {
	if article == nil {
		return history.CaseError, fmt.Errorf("null article")
	}
	if len(article.NewsgroupsPtr) == 0 {
		return history.CaseError, fmt.Errorf("no newsgroups specified in article")
	}

	// Copy what is needed after ProcessArticle: once it returns, the batch goroutine may
	// already have cleared the struct (db_batch.go processNewsgroupBatch).
	messageID := article.MessageID
	var sq *database.SQ3batch
	if proc.DB != nil {
		sq = proc.DB.Batch
	}
	var marks map[string]database.CommitMark
	if timeout > 0 {
		marks = make(map[string]database.CommitMark, len(article.NewsgroupsPtr))
		for _, ng := range article.NewsgroupsPtr {
			if ng != nil {
				marks[*ng] = sq.CommitMark(*ng)
			}
		}
	}

	log.Printf("Processing incoming article with Message-ID: %s for primary newsgroup %s (cross-posted to %d groups)",
		messageID, *article.NewsgroupsPtr[0], len(article.NewsgroupsPtr))

	bulkmode := false
	code, err := proc.ProcessArticle(article, *article.NewsgroupsPtr[0], bulkmode)
	if code != history.CasePass || err != nil || timeout <= 0 {
		return code, err
	}

	// the groups still waiting for their commit (see above); the batch writer edits
	// NewsgroupsPtr under article.Mux
	article.Mux.Lock()
	captured := make([]string, 0, len(article.NewsgroupsPtr))
	for _, ng := range article.NewsgroupsPtr {
		if ng != nil {
			captured = append(captured, *ng)
		}
	}
	article.Mux.Unlock()

	// all groups share one deadline: the whole wait never exceeds timeout
	deadline := time.Now().Add(timeout)
	notConfirmed := func(werr error) (int, error) {
		log.Printf("[INTAKE] POST %s: commit not confirmed within %s: %v", messageID, timeout, werr)
		return history.CasePass, nil
	}
	lookupLogged := false
	for _, group := range captured {
		m, ok := marks[group]
		if !ok {
			m = sq.CommitMark(group)
		}
		for round := 0; ; round++ {
			if round > 0 {
				if werr := postRecheckPause(stop, deadline); werr != nil {
					return notConfirmed(fmt.Errorf("'%s': %w", group, werr))
				}
				m = sq.CommitMark(group)
			}
			var werr error
			if remaining := time.Until(deadline); remaining > 0 {
				werr = postWaitCommitted(sq, m, stop, remaining)
			} else {
				werr = fmt.Errorf("%w: '%s': no time left", database.ErrBatchWaitTimeout, group)
			}
			dropped := errors.Is(werr, database.ErrBatchDropped)
			if werr != nil && !dropped {
				// ErrBatchWaitTimeout or ErrBatchWaitStopped
				return notConfirmed(werr)
			}
			stored, lerr := postArticleStored(proc, group, messageID)
			if lerr != nil {
				if !lookupLogged {
					log.Printf("[INTAKE] POST %s: row lookup in '%s' failed (counted as not stored yet): %v", messageID, group, lerr)
					lookupLogged = true
				}
				stored = false
			}
			if stored {
				break
			}
			if dropped {
				log.Printf("[INTAKE] POST %s: not stored in '%s': %v", messageID, group, werr)
				return history.CaseError, fmt.Errorf("%w: not stored", common.ErrIntakeRejected)
			}
		}
	}
	return history.CasePass, nil
}

// postRecheckPause waits postRecheckDelay (at most until deadline) before another round of
// ProcessPostedArticle. It returns an error wrapping ErrBatchWaitTimeout when the deadline
// passed and ErrBatchWaitStopped when stop was closed.
func postRecheckPause(stop <-chan struct{}, deadline time.Time) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("%w: no time left", database.ErrBatchWaitTimeout)
	}
	pause := min(postRecheckDelay, remaining)
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-timer.C:
		if pause < postRecheckDelay {
			return fmt.Errorf("%w: no time left", database.ErrBatchWaitTimeout)
		}
		return nil
	case <-stop:
		return database.ErrBatchWaitStopped
	}
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
// If hostname is empty, it will attempt to retrieve it from the database
// If hostname is provided, it will be saved to the database and used
func SetHostname(hostname string, db *database.Database) error {
	if db == nil {
		return fmt.Errorf("database is required for hostname operations")
	}

	// If hostname is provided, validate and save it to database
	if hostname != "" {
		if err := validateHostname(hostname); err != nil {
			return fmt.Errorf("hostname validation failed: %v", err)
		}

		// Save hostname to database
		if err := db.SetConfigValue("local_nntp_hostname", hostname); err != nil {
			return fmt.Errorf("failed to save hostname to database: %v", err)
		}

		LocalNNTPHostname = hostname
		log.Printf("Hostname set, validated, and saved to database: %s", hostname)
		return nil
	}

	// If no hostname provided, try to get from database
	dbHostname, err := db.GetConfigValue("local_nntp_hostname")
	if err != nil {
		return fmt.Errorf("failed to retrieve hostname from database: %v", err)
	}

	if dbHostname == "" {
		return fmt.Errorf("hostname is empty and no hostname configured in database")
	}

	if err := validateHostname(dbHostname); err != nil {
		return fmt.Errorf("database hostname validation failed: %v", err)
	}

	LocalNNTPHostname = dbHostname
	log.Printf("Using hostname from database: %s", dbHostname)
	return nil
}

// GetHostname returns the currently configured hostname
func GetHostname() string {
	return LocalNNTPHostname
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
