// Command nntp-fetcher downloads articles from the configured NNTP provider into go-pugleaf.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

const BW_V4_Prefix = "24.182.239.6"   // news.blueworldhosting.com.
const ET_V4 = "157.180.91.226"        // news.eternal-september.org.
const ET_V6 = "2a01:4f9:c012:f55a::1" // news.eternal-september.org.

// showUsageExamples prints the -help text: what the fetcher does and examples
// that use only the flags defined in main.
func showUsageExamples() {
	fmt.Println("\n=== NNTP Fetcher (cmd/nntp-fetcher, built as build/pugleaf-fetcher) ===")
	fmt.Println("Downloads articles from the enabled providers into the local database, and")
	fmt.Println("can add groups from a provider's newsgroup list to the database.")
	fmt.Println("Each run fetches at most -max-batch articles per group and then exits (run it from cron).")
	fmt.Println("For newsgroup analysis use cmd/nntp-analyze instead.")
	fmt.Println()
	fmt.Println("All flags: ./pugleaf-fetcher -h")
	fmt.Println("Every mode reads and writes the databases below -data (default ./data).")
	fmt.Println("-nntphostname is needed on the first run; later runs reuse the value stored in the database.")
	fmt.Println()
	fmt.Println("Providers and routing (web admin: providers 'routing' field and 'Global routing'):")
	fmt.Println("  Without routing every group comes from the primary: the first enabled provider (by priority).")
	fmt.Println("  A provider with routing patterns (e.g. 'gmane.*,gwene.*') is used only for the groups they")
	fmt.Println("  want, before the primary; a global route pins groups to its provider(s) only (no fallback).")
	fmt.Println("  Each group is fetched from ONE provider per run: the first candidate whose LIST has it.")
	fmt.Println("  One pass per provider, in priority order; '[FETCHER-ROUTE]' lines log assignments and skips.")
	fmt.Println("  ./pugleaf-fetcher -routing-dry-run   (print 'ROUTE group -> provider' / 'SKIP ...' and exit)")
	fmt.Println("  ./pugleaf-fetcher -provider gmane     (run only the pass of provider 'gmane'; the LIST of every")
	fmt.Println("                                        provider taking part is still read: the assignment is global)")
	fmt.Println("  A group that already has articles and has no progress on its provider (the routing moved it,")
	fmt.Println("  or a new provider) starts at its newest local article date: older articles of that provider")
	fmt.Println("  are not fetched (a '[FETCHER-ROUTE]' line names the group). Fetch them with:")
	fmt.Println("  ./pugleaf-fetcher -download-start-date 2020-01-01 -group 'gmane.*' -provider gmane")
	fmt.Println()
	fmt.Println("Article downloading (active groups that exist in the database and on the provider):")
	fmt.Println("  ./pugleaf-fetcher -data ./data -nntphostname news.example.org   (first run: store the hostname)")
	fmt.Println("  ./pugleaf-fetcher                     (all active groups; -group '' and -group '$all' do the same)")
	fmt.Println("  ./pugleaf-fetcher -group alt.test")
	fmt.Println("  ./pugleaf-fetcher -group 'alt.*'      (every database group with the prefix 'alt.'; quote the '*')")
	fmt.Println("  ./pugleaf-fetcher -group 'tin.*,lists.*;de.test'   (several patterns: space, ',' or ';')")
	fmt.Println("  ./pugleaf-fetcher -group alt.test -download-start-date 2024-12-31")
	fmt.Println("                                        (rescan from that date, ignoring the stored progress)")
	fmt.Println("  ./pugleaf-fetcher -fetch-active-only=false   (download ONLY the inactive groups)")
	fmt.Println()
	fmt.Println("Newsgroup list update (the primary or -provider; only groups that provider may serve")
	fmt.Println("by the routing; new groups are added as inactive):")
	fmt.Println("  ./pugleaf-fetcher -fetch-newsgroups-from-remote '$all'     (dry run: list what would be added)")
	fmt.Println("  ./pugleaf-fetcher -fetch-newsgroups-from-remote 'comp.*' -fetch-newsgroups-force   (really add them)")
	fmt.Println("  ./pugleaf-fetcher -fetch-newsgroups-from-remote '$all' -exclude-prefix 'alt.binaries.*,de.test'")
	fmt.Println("                                        (a trailing '*' excludes a prefix, otherwise the exact name)")
	fmt.Println("  ./pugleaf-fetcher -fetch-newsgroups-from-remote '$all' -fetch-newsgroups-force -provider gmane")
	fmt.Println()
	fmt.Println("Progress reset (sets the stored last article number of the primary or -provider, then exits):")
	fmt.Println("  ./pugleaf-fetcher -group alt.test -reset-progress 1000   (next run starts at article 1001)")
	fmt.Println("  ./pugleaf-fetcher -group 'alt.*' -reset-progress 1000   (only the groups that start with alt.)")
	fmt.Println("  Without -group, or with '$all', it resets EVERY group in the database. Values <= 0 do nothing.")
	fmt.Println()
	fmt.Println("Tuning (defaults in brackets):")
	fmt.Println("  -max-batch N           articles per group and run, clamped to 10-1000 [1000]")
	fmt.Println("  -max-batch-threads N   newsgroup batches processed concurrently, 1-1024 [4]")
	fmt.Println("  -max-queue N           articles queued in db_batch over all groups [1280]")
	fmt.Println("  -download-max-par N    groups downloaded in parallel [1]")
	fmt.Println("  -history=false         do not use the message-id history index [true]")
	fmt.Println("  -reuse-crossposts=false  download crossposts again instead of copying them (needs -history) [true]")
	fmt.Println("  -useshorthashlen N     history short hash length 2-7, fixed after the first run [7]")
	fmt.Println()
}

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	database.DBidleTimeOut = 15 * time.Second
	database.NO_CACHE_BOOT = true // prevents booting caches
	log.Printf("Starting go-pugleaf NNTP Fetcher (version %s)", config.AppVersion)
	// Command line flags for NNTP fetcher configuration
	var newsgroups []*models.Newsgroup
	var (
		maxBatchThreads    = flag.Int("max-batch-threads", 4, "Limit how many newsgroup batches will be processed concurrently (1-1024); more can eat your memory and disk IO!")
		maxBatch           = flag.Int("max-batch", 1000, "Maximum number of articles to process in a batch (10-1000; values above 1000 are clamped to 1000)")
		maxQueued          = flag.Int("max-queue", 1280, "Limit db_batch to have max N articles queued over all newsgroups")
		fetchNewsgroup     = flag.String("group", "", "Newsgroups to fetch: one or more patterns separated by space, ',' or ';', each a name, 'prefix.*' (final wildcard) or '$all' (default: empty = all groups once up to -max-batch)")
		nntphostname       = flag.String("nntphostname", "", "Your NNTP hostname: required on the first run, later runs fall back to the value stored in the database")
		useHistory         = flag.Bool("history", true, "maintain and use the message-id history index (<data>/history)")
		useShortHashLenPtr = flag.Int("useshorthashlen", 7, "short hash length for history storage (2-7) - NOTE: cannot be changed once set!")
		fetchActiveOnly    = flag.Bool("fetch-active-only", true, "Download only active newsgroups; set to false to download ONLY the inactive newsgroups")
		excludePrefix      = flag.String("exclude-prefix", "", "use with -fetch-newsgroups-from-remote: comma-separated names to exclude; a final * excludes a prefix, e.g. alt.*,comp.lang.c (default: empty = no exclusion)")
		downloadMaxPar     = flag.Int("download-max-par", 1, "download this many groups in parallel, can eat your memory!")
		updateList         = flag.String("fetch-newsgroups-from-remote", "", "get the newsgroup list from the primary provider (or -provider) and list the groups missing in the database that the routing lets this provider serve ('group.*', a name prefix or '$all'); dry run unless -fetch-newsgroups-force is set; exits afterwards")
		updateListForce    = flag.Bool("fetch-newsgroups-force", false, "use with -fetch-newsgroups-from-remote to really add the new groups to the database (as inactive)")
		dataDir            = flag.String("data", "./data", "Directory to store database files")
		reuseCrossposts    = flag.Bool("reuse-crossposts", true, "copy crossposted articles already stored in another group (needs history) instead of downloading them again")
		// Download options with date filtering
		downloadStartDate = flag.String("download-start-date", "", "Rescan from this date (YYYY-MM-DD), ignoring the stored progress")
		resetProgress     = flag.Int64("reset-progress", 0, "Set the stored last article number of the primary provider (or -provider) to N for the -group selection (names, 'prefix.*' = the groups in the database with that prefix, empty/$all = every group in the database) and exit; 0 = off")
		providerName      = flag.String("provider", "", "Use only this provider (by name): only its pass of the fetch run (the LIST of every provider taking part is still read, the assignment is global), and the provider of -reset-progress and -fetch-newsgroups-from-remote (default: every pass; the primary for those two)")
		routingDryRun     = flag.Bool("routing-dry-run", false, "Print the group -> provider assignment and the skipped groups of this run, then exit without fetching articles")
		showHelp          = flag.Bool("help", false, "Show usage examples and exit")
	)
	flag.Parse()
	history.ENABLE_HISTORY = *useHistory
	processor.ReuseCrossposts = *reuseCrossposts && *useHistory
	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}
	if err := checkFlagCombos(*routingDryRun, *resetProgress, *updateList); err != nil {
		fmt.Fprintf(os.Stderr, "[FETCHER]: %v\n", err)
		os.Exit(2)
	}
	// Validate -download-start-date before anything is opened (the workers use the parsed value)
	var downloadStart time.Time
	if *downloadStartDate != "" {
		t, err := time.Parse("2006-01-02", *downloadStartDate)
		if err != nil {
			log.Fatalf("[FETCHER]: Invalid -download-start-date '%s': %v (expected YYYY-MM-DD)", *downloadStartDate, err)
		}
		downloadStart = t
	}
	if *updateList != "" {
		// same signal handling as the fetch path: a signal stops the run between groups,
		// UpdateNewsgroupList returns and its database is shut down
		startFetcherSignalHandler()
		// UpdateNewsgroupList closes its database before it returns
		if err := UpdateNewsgroupList(*dataDir, updateList, excludePrefix, *updateListForce, *providerName); err != nil {
			log.Printf("Newsgroup list update failed: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	clampFetcherFlags(maxBatch, maxBatchThreads, maxQueued, downloadMaxPar)
	// Validate command-line flag
	if *useShortHashLenPtr < 2 || *useShortHashLenPtr > 7 {
		log.Fatalf("Invalid UseShortHashLen: %d (must be between 2 and 7)", *useShortHashLenPtr)
	}

	nntp.MaxReadLinesXover = int64(*maxBatch)
	processor.MaxBatchSize = int64(*maxBatch)

	// -group: several patterns (space, ',' or ';'); 'tin.*,lists.*' used to be one prefix
	sel := parseGroupSelection(*fetchNewsgroup)
	log.Printf("[FETCHER]: -group selection: %s", sel)

	// Initialize database (default config, data in ./data)
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = *dataDir
	dbConfig.MaxDBbatch = *maxBatch
	dbConfig.MaxDBthreads = *maxBatchThreads
	dbConfig.MaxQueued = *maxQueued

	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize progress database once to avoid opening/closing for each group
	progressDB, err := database.NewProgressDB(filepath.Join(*dataDir, "progress.db"))
	if err != nil {
		fetcherShutdown(db) // the main DB is open: shut it down before exiting
		log.Printf("[FETCHER]: Failed to initialize progress database: %v", err)
		os.Exit(1)
	}
	defer progressDB.Close()

	// Set up cross-platform signal handling for graceful shutdown. Listen at once: a
	// signal during the provider/newsgroup setup (a slow remote LIST) must not sit in
	// the channel until the fetch loop starts.
	startFetcherSignalHandler()

	var useShortHashLen int
	if !*routingDryRun { // a dry run stores nothing and needs no hostname
		// Get UseShortHashLen from database (with safety check)
		storedUseShortHashLen, isLocked, err := db.GetHistoryUseShortHashLen(*useShortHashLenPtr)
		if err != nil {
			log.Fatalf("Failed to get UseShortHashLen from database: %v", err)
		}
		if !isLocked {
			// First run: store the provided value
			useShortHashLen = *useShortHashLenPtr
			err = db.SetHistoryUseShortHashLen(useShortHashLen)
			if err != nil {
				log.Fatalf("Failed to store UseShortHashLen in database: %v", err)
			}
			log.Printf("First run: UseShortHashLen set to %d and stored in database", useShortHashLen)
		} else {
			// Subsequent runs: use stored value and warn if different
			useShortHashLen = storedUseShortHashLen
			if *useShortHashLenPtr != useShortHashLen {
				log.Printf("WARNING: Command-line UseShortHashLen (%d) differs from stored value (%d). Using stored value to prevent data corruption.", *useShortHashLenPtr, useShortHashLen)
			}
			log.Printf("Using stored UseShortHashLen: %d", useShortHashLen)
		}

		// Set hostname in processor with database fallback support
		if err := processor.SetHostname(*nntphostname, db); err != nil {
			log.Fatalf("Failed to set NNTP hostname: %v", err)
		}
	}

	providers, err := db.GetProviders()
	if err != nil || len(providers) == 0 {
		// handle error appropriately
		log.Printf("Failed to get providers (%d): %v", len(providers), err)
		return
	}
	log.Printf("Loaded %d providers from database", len(providers))
	routes, err := db.GetEnabledGroupRoutes()
	if err != nil {
		log.Fatalf("[FETCHER-ROUTE]: Failed to get global routes: %v", err)
	}
	// Routing (plan fetcher-group-routing): without routing and pins the router's only
	// participant is the first enabled usable provider, the one the fetcher always used.
	router, err := newFetchRouter(providers, routes)
	if err != nil {
		log.Fatalf("[FETCHER-ROUTE]: Invalid routing: %v", err)
	}
	participants := router.Participants()
	if len(participants) == 0 {
		log.Fatalf("[FETCHER]: No enabled provider backend available (%d providers, none enabled)", len(providers))
	}
	if len(routes) > 0 || len(participants) > 1 {
		log.Printf("[FETCHER-ROUTE]: %d enabled global routes, %d providers take part", len(routes), len(participants))
	}
	var onlyProvider *models.Provider // -provider: only its pass runs
	if *providerName != "" && *resetProgress <= 0 {
		onlyProvider, err = resolveProviderFlag(providers, router, *providerName)
		if err != nil {
			log.Fatalf("[FETCHER-ROUTE]: %v", err)
		}
		log.Printf("[FETCHER-ROUTE]: -provider '%s': only its pass runs", onlyProvider.Name)
	}

	// Get all newsgroups from database using admin function (includes empty groups)
	if sel.needsDB() {
		ngs, err := db.MainDBGetAllNewsgroups()
		if err != nil {
			log.Printf("[FETCHER]: Failed to get newsgroups from database: %v", err)
			return
		}
		if len(ngs) == 0 {
			fmt.Println("[FETCHER]: No newsgroups found in database")
			return
		}
		fmt.Printf("[FETCHER]: %d newsgroups in database\n", len(ngs))
		newsgroups = selectNewsgroups(ngs, sel)
	} else {
		newsgroups = selectNewsgroups(nil, sel)
	}
	if !sel.all {
		log.Printf("[FETCHER]: -group selected %d newsgroups", len(newsgroups))
	}

	if *resetProgress > 0 && !*routingDryRun {
		rp, err := resetProgressProvider(providers, router, *providerName)
		if err != nil {
			log.Fatalf("[FETCHER]: -reset-progress: %v", err)
		}
		// decision D-O1: a wildcard -group resets only the groups with that prefix
		log.Printf("[FETCHER]: Resetting download progress to %d for %d newsgroups (%s) on provider '%s'", *resetProgress, len(newsgroups), sel, rp.Name)
		for _, ng := range newsgroups {
			err = progressDB.UpdateProgress(rp.Name, ng.Name, *resetProgress)
			if err != nil {
				log.Fatalf("Failed to update progress for provider '%s' group '%s': %v", rp.Name, ng.Name, err)
			}
		}
		log.Printf("[FETCHER]: Download progress reset completed.")
		os.Exit(0)
	}

	// the -fetch-active-only selection before the routing, so its counts and skip logs
	// only cover groups a pass would fetch (the feeder checks it again)
	newsgroups = activeSelection(newsgroups, *fetchActiveOnly, db.MainDBGetNewsgroup)
	log.Printf("[FETCHER]: %d newsgroups in the selection (fetch-active-only=%t)", len(newsgroups), *fetchActiveOnly)
	if len(newsgroups) == 0 {
		log.Printf("[FETCHER]: No newsgroups to process.")
		return
	}

	// One pool per participant (connection caps unchanged) and its cached LIST: the LIST
	// gates which groups the provider gets; a failed LIST carries nothing.
	byID := make(map[int64]*models.Provider, len(providers))
	for _, p := range providers {
		if _, dup := byID[p.ID]; !dup {
			byID[p.ID] = p
		}
	}
	names := make(map[int64]string, len(participants))
	pools := make(map[int64]*nntp.Pool, len(participants))
	allPools := make([]*nntp.Pool, 0, len(participants))
	lists := make(map[int64]map[string]bool, len(participants))
	var listErr error
	for _, id := range participants {
		p := byID[id]
		p.MaxConns = capProviderConns(p)
		/* disabled
		if p.MaxConns > *maxBatch {
			p.MaxConns = *maxBatch // limit conns to maxBatch
		}
		*/
		log.Printf("[FETCHER]: Provider '%s' (ID: %d, Host: %s, Port: %d, SSL: %v, MaxConns: %d)", p.Name, p.ID, p.Host, p.Port, p.SSL, p.MaxConns)
		pool := nntp.NewPool(fetcherBackendConfig(p, p.MaxConns))
		pools[id] = pool
		allPools = append(allPools, pool)
		names[id] = p.Name
		log.Printf("[FETCHER]: Created connection pool for provider '%s' with max %d connections", p.Name, p.MaxConns)
		remoteGroups, err := pool.FileCachedListNewsgroups(filepath.Join(*dataDir, "cache"))
		if err != nil || len(remoteGroups) == 0 {
			if err == nil {
				err = errors.New("empty newsgroup list")
			}
			listErr = err
			log.Printf("[FETCHER-ROUTE]: provider '%s': failed to fetch newsgroup list from remote server: %v (it gets no groups this run)", p.Name, err)
			continue
		}
		quickRGLookup := make(map[string]bool, len(remoteGroups))
		for _, rg := range remoteGroups {
			quickRGLookup[rg] = true
		}
		lists[id] = quickRGLookup
	}
	if len(lists) == 0 {
		closeFetchPools(allPools)
		log.Fatalf("failed to fetch newsgroup list from remote server: %v", listErr)
	}

	// each group goes to ONE provider this run: the first candidate whose LIST has it
	assign := assignGroups(newsgroups, router, lists)
	logRouteSummary(assign, participants, names)

	if *routingDryRun {
		var only int64
		if onlyProvider != nil {
			only = onlyProvider.ID
		}
		if err := writeRouteDryRun(os.Stdout, assign, names, only); err != nil {
			log.Printf("[FETCHER-ROUTE]: writing the dry run failed: %v", err)
		}
		closeFetchPools(allPools)
		if err := progressDB.Close(); err != nil {
			log.Printf("[FETCHER]: Failed to close progress database: %v", err)
		}
		fetcherShutdown(db)
		os.Exit(0)
	}

	var passes []fetchPass
	total := 0
	for _, id := range participants {
		groups := assign.byProvider[id]
		if len(groups) == 0 || (onlyProvider != nil && id != onlyProvider.ID) {
			closeFetchPools([]*nntp.Pool{pools[id]}) // no pass: idle pool closed at once
			continue
		}
		passes = append(passes, fetchPass{provider: byID[id], pool: pools[id], groups: groups})
		total += len(groups)
	}
	if len(passes) == 0 {
		log.Printf("[FETCHER]: No valid newsgroups to process after checking remote server list.")
		return
	}
	log.Printf("[FETCHER]: Starting fetch for %d newsgroups", total)

	proc, err := processor.NewProcessorE(db, passes[0].pool, useShortHashLen) // each pass sets proc.Pool
	if err != nil {
		log.Fatalf("[FETCHER]: Failed to create processor: %v", err)
	}
	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate

	opts := passOptions{
		db:                db,
		progressDB:        progressDB,
		fetchActiveOnly:   *fetchActiveOnly,
		downloadStartDate: *downloadStartDate,
		downloadStart:     downloadStart,
		downloadMaxPar:    *downloadMaxPar,
	}
	var totals passStats
	for i, ps := range passes {
		if common.WantShutdown() {
			log.Printf("[FETCHER-ROUTE]: shutdown requested, %d of %d provider passes not run", len(passes)-i, len(passes))
			for _, rest := range passes[i:] {
				closeFetchPools([]*nntp.Pool{rest.pool})
			}
			break
		}
		if ps.pool.Backend.Provider == nil || ps.pool.Backend.Provider.Name == "" {
			log.Fatalf("No provider backend available: '%#v'", ps.pool.Backend.Provider)
		}
		if len(passes) > 1 {
			log.Printf("[FETCHER-ROUTE]: pass %d/%d: provider '%s' with %d groups", i+1, len(passes), ps.provider.Name, len(ps.groups))
		}
		// Pass reset: every pass gets the processor on its pool and a fresh batch queue
		// (the previous pass closed its GetQ). The previous pass's goroutines that read
		// processor.Batch or proc.Pool have all ended (runPass joins them).
		proc.Pool = ps.pool
		bq := processor.NewBatchQueue()
		processor.Batch = bq
		st := runPass(proc, ps.pool, bq, ps.groups, opts)
		closeFetchPools([]*nntp.Pool{ps.pool})
		totals.add(st)
	}
	// Signal background tasks to stop
	close(db.StopChan)

	// Close the proc/processor (flushes history, stops processing)
	if proc != nil {
		if err := proc.Close(); err != nil {
			log.Printf("[FETCHER] Warning: Failed to close proc: %v", err)
		} else {
			log.Printf("[FETCHER] proc/processor closed successfully")
		}
	}
	db.WG.Wait()

	if err := db.Shutdown(); err != nil {
		log.Printf("[FETCHER]: Failed to shutdown database: %v", err)
		os.Exit(1)
	} else {
		log.Printf("[FETCHER]: Database shutdown successfully")
	}

	log.Printf("[FETCHER]: Total downloaded: %d articles, notfound: %d (newsgroups: %d)", totals.downloaded, totals.notfound, totals.queued)

	logProviderBytes(allPools)

	log.Printf("[FETCHER]: Graceful shutdown completed. Exiting here.")
}

// fetchPass is one provider's part of a run: its pool and the groups assigned to it.
type fetchPass struct {
	provider *models.Provider
	pool     *nntp.Pool
	groups   []*models.Newsgroup
}

// closeFetchPools closes pools, logging errors.
func closeFetchPools(pools []*nntp.Pool) {
	for _, pool := range pools {
		if pool == nil {
			continue
		}
		if err := pool.ClosePool(); err != nil {
			log.Printf("[FETCHER]: closing pool %s failed: %v", pool.Backend.Host, err)
		}
	}
}

// returnBatchItem hands a fetched (or failed) item back to its DownloadArticles run. After a
// shutdown signal nobody processes results any more (drainLateResults stops too), so the item
// is dropped instead of blocking: runPass waits for the article workers.
func returnBatchItem(item *processor.BatchItem) {
	select {
	case item.ReturnQ <- item:
	case <-common.ShutdownChan:
	}
}

// passOptions are the run-wide settings every provider pass uses.
type passOptions struct {
	db                *database.Database
	progressDB        *database.ProgressDB
	fetchActiveOnly   bool
	downloadStartDate string // -download-start-date as given ("" = off)
	downloadStart     time.Time
	downloadMaxPar    int
}

// passStats are the counters of one provider pass.
type passStats struct {
	downloaded, notfound, queued, todo uint64
	groupNotFound                      uint64   // assigned groups the provider answered 411
	groupNotFoundNames                 []string // the first routeLogFirst of them
}

// add sums the counters of another pass into st (names are not kept).
func (st *passStats) add(o passStats) {
	st.downloaded += o.downloaded
	st.notfound += o.notfound
	st.queued += o.queued
	st.todo += o.todo
	st.groupNotFound += o.groupNotFound
}

// runPass fetches groups from one provider: pool is proc.Pool and bq is processor.Batch for
// this pass (the caller sets both). It returns when every group is done or a shutdown stopped
// the pass, after all goroutines it started (feeder, check, download and article workers) have
// ended, so the caller may swap proc.Pool and processor.Batch for the next pass. A 411 on an
// assigned group is counted and logged; the group is not tried on another provider this run.
func runPass(proc *processor.Processor, pool *nntp.Pool, bq *processor.BatchQueue, newsgroups []*models.Newsgroup, o passOptions) passStats {
	db, progressDB := o.db, o.progressDB
	providerName := pool.Backend.Provider.Name
	fetchDoneChan := make(chan error, 1)

	log.Printf("[FETCHER]: Provider: %s @ MaxConns: %d", providerName, pool.Backend.MaxConns)
	DownloadMaxPar := o.downloadMaxPar // -download-max-par: number of download workers (groups fetched in parallel); TodoQ capacity stays processor.DownloadMaxPar
	DLParChan := make(chan struct{}, DownloadMaxPar)
	var mux sync.Mutex
	var st passStats
	// scan group worker
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		defer close(bq.Check)
		feedCheckQueue(newsgroups, "", o.fetchActiveOnly, db.MainDBGetNewsgroup, bq.Check, common.WantShutdown,
			func() {
				mux.Lock()
				st.queued++
				if st.queued%1000 == 0 {
					log.Printf("[FETCHER]: Queued %d/%d newsgroups", st.queued, len(newsgroups))
				}
				mux.Unlock()
			})
	}()
	var wgCheck sync.WaitGroup
	startDates := make(map[string]time.Time) // per-group date-based download start (UTC midnight)
	dateStarts := newDateStartLog(providerName, log.Printf)
	limitCheckWorker := runtime.NumCPU() / 2
	if limitCheckWorker < 1 {
		limitCheckWorker = 1
	}
	if pool.Backend.MaxConns < limitCheckWorker {
		limitCheckWorker = pool.Backend.MaxConns
	}
	if limitCheckWorker < 1 {
		log.Printf("[FETCHER]: Invalid limitCheckWorker: %d (overwrite to 1), pool.Backend.MaxConns: %d", limitCheckWorker, pool.Backend.MaxConns)
		limitCheckWorker = 1
	}
	log.Printf("[FETCHER]: Starting %d check group workers", limitCheckWorker)
	for i := 1; i <= limitCheckWorker; i++ {
		wgCheck.Add(1)
		go func(worker int, wgCheck *sync.WaitGroup, progressDB *database.ProgressDB) {
			defer wgCheck.Done()
			for ng := range bq.Check {
				if common.WantShutdown() {
					//log.Printf("[FETCHER]: Batch.Check shutdown")
					continue // drain until the feeder stops and closes Check
				}
				/* disabled */
				if db.IsDBshutdown() && len(bq.Check) == 0 {
					//log.Printf("[FETCHER]: Batch.Check DB shutdown")
					return
				}
				groupInfo, err := pool.SelectGroup(*ng)
				if err != nil || groupInfo == nil {
					switch err {
					case nntp.ErrNewsgroupNotFound:
						// 411 on a group the routing assigned to this provider: no
						// failover this run, counted for the pass summary
						mux.Lock()
						st.groupNotFound++
						if len(st.groupNotFoundNames) < routeLogFirst {
							st.groupNotFoundNames = append(st.groupNotFoundNames, *ng)
						}
						mux.Unlock()
						continue
					case io.EOF:
						log.Printf("[FETCHER]: pool.SelectGroup failed. connection EOF. skipping ng: '%s'", *ng)
						continue
					default:
						log.Printf("[FETCHER]: Error in select ng='%s' groupInfo='%#v' err='%v'", *ng, groupInfo, err)
						continue
					}
				}
				if groupInfo.Last == 0 || groupInfo.Last < groupInfo.First {
					//log.Printf("[FETCHER]: Empty group '%s'", *ng)
					continue
				}
				//log.Printf("[FETCHER]: ng '%s', REMOTE groupInfo: %#v", *ng, groupInfo),
				var lastArticle int64
				if o.downloadStartDate != "" {
					lastArticle = -1
				} else {
					mux.Lock()
					lastArticle, err = progressDB.GetLastArticle(providerName, *ng)
					if err != nil || lastArticle < -1 {
						log.Printf("[FETCHER]: Failed to get last article for group '%s' from provider '%s': %v", *ng, providerName, err)
						mux.Unlock()
						continue
					}
					mux.Unlock()
				}
				switch lastArticle {
				case 0:
					// Open group DB only when we need to check last-article date
					groupDB, err := proc.DB.GetGroupDB(*ng)
					if err != nil {
						log.Printf("[FETCHER]: Failed to get group DBs for newsgroup '%s': %v", *ng, err)
						continue
					}
					lastArticleDate, checkDateErr := proc.DB.GetLastArticleDate(groupDB)
					// ensure close regardless of errors
					if ferr := proc.DB.ForceCloseGroupDB(groupDB); ferr != nil {
						log.Printf("[FETCHER]: ForceCloseGroupDB error for '%s': %v", *ng, ferr)
					}
					if checkDateErr != nil {
						log.Printf("[FETCHER]: Failed to get last article date for '%s': %v", *ng, checkDateErr)
						continue
					}

					// If group has existing articles, use date-based download instead
					// (also when the routing moved the group to this provider): the
					// provider's older articles are skipped, dateStarts warns with the
					// -download-start-date command (the user, 2026-11-27)
					if lastArticleDate != nil {
						y, m, d := lastArticleDate.Date()
						start := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
						dateStarts.note(*ng, start)
						mux.Lock()
						startDates[*ng] = start
						mux.Unlock()
					}

				case -1: // User-requested date rescan
					//log.Printf("[FETCHER]: Date rescan '%s'", *ng)
					// Set date-based download from epoch for complete rescan
					mux.Lock()
					if o.downloadStartDate != "" {
						startDates[*ng] = o.downloadStart
					}
					mux.Unlock()
					// Reset lastArticle to 0 and fall through to normal range processing
					lastArticle = 0
				default:
					// pass
				}
				if groupInfo.First > lastArticle {
					lastArticle = groupInfo.First - 1
				}
				//log.Printf("DEBUG-RANGE: ng='%s' lastArticle=%d (after switch)", *ng, lastArticle)
				start := lastArticle + 1                  // Start from the first article in the remote group
				end := start + processor.MaxBatchSize - 1 // End at the last article in the remote group
				//log.Printf("DEBUG-RANGE: ng='%s' calculated start=%d end=%d groupInfo.Last=%d", *ng, start, end, groupInfo.Last)

				// For date-based downloads, don't cap end to groupInfo.Last since they use date filtering
				mux.Lock()
				_, isDateBased := startDates[*ng]
				mux.Unlock()
				if !isDateBased && end > groupInfo.Last {
					end = groupInfo.Last
				}
				if start > end {
					//log.Printf("[FETCHER]: OK ng: '%s' start=%d end=%d (remote: first=%d last=%d)", *ng, start, end, groupInfo.First, groupInfo.Last)
					continue
				}
				toFetch := end - start + 1
				if toFetch > nntp.MaxReadLinesXover {
					// Limit to N articles per batch fetch
					end = start + nntp.MaxReadLinesXover - 1
					toFetch = end - start + 1
					//log.Printf("DownloadArticles: Limiting fetch for %s to %d articles (start=%d, end=%d)", newsgroup, toFetch, start, end)
				}
				if toFetch <= 0 {
					//log.Printf("[FETCHER]: OK ng: '%s' (start=%d, end=%d) toFetch=%d", *ng, start, end, toFetch)
					continue
				}

				groupInfo.FetchStart = start
				groupInfo.FetchEnd = end
				select {
				case bq.TodoQ <- groupInfo:
				case <-common.ShutdownChan:
					continue // the download workers stop: drain Check until the feeder closes it
				}
				log.Printf("[FETCHER]: TodoQ '%s' toFetch=%d start=%d end=%d", *ng, toFetch, start, end)
				//time.Sleep(time.Second * 2)
			}
			//log.Printf("[FETCHER]: Worker %d finished feeding TodoQ", worker)
		}(i, &wgCheck, progressDB)
	} // end for scan group worker
	checkDone := make(chan struct{})
	go func(wgCheck *sync.WaitGroup) {
		defer close(checkDone)
		wgCheck.Wait()
		close(bq.TodoQ)
		log.Printf("[FETCHER]: All newsgroups queued for processing, closing TodoQ")
	}(&wgCheck)

	// download worker
	var wgGet sync.WaitGroup
	for i := 1; i <= pool.Backend.MaxConns; i++ {
		// fire up async goroutines to fetch articles
		wgGet.Add(1)
		go func(worker int) {
			defer wgGet.Done()
			//log.Printf("DownloadArticles: Worker %d group '%s' start", worker, groupName)
			for item := range bq.GetQ { // fed from internal/processor/proc_DLArt.go:~L314 (Batch.GetQ); closed by runPass after the pass
				/* disabled
				if common.WantShutdown() {
					//log.Printf("[FETCHER]: Batch.GetQ shutdown")
					return
				}
				*/
				if common.WantShutdown() {
					// keep draining so runPass can join the workers, but download nothing
					// more: no progress is written after a shutdown signal
					continue
				}
				/* disabled */
				//log.Printf("DownloadArticles: Worker %d GetArticle group '%s' article (%s)", worker, *item.GroupName, *item.MessageID)
				art, err := pool.GetArticle(item.MessageID, true)
				if err != nil || art == nil {
					// Copy what the log lines need before the item is handed back: the
					// consumer (DownloadArticles' processing loop or drainLateResults)
					// nils item.MessageID/GroupName as soon as it takes the item, so a
					// read after the send is a race and a nil dereference.
					msgID, groupName := *item.MessageID, *item.GroupName
					item.Error = err      // Set error on item
					returnBatchItem(item) // Send failed item back
					switch err {
					case nntp.ErrArticleNotFound, nntp.ErrArticleRemoved:
						// article not found, not a big deal
						mux.Lock()
						st.notfound++
						mux.Unlock()
						continue
					case io.EOF:
						log.Printf("ERROR DownloadArticles: pool.GetArticle failed. connection EOF ... continue! ng: '%s'", groupName)
						//common.ForceShutdown()
						//return
						continue
					default:
						log.Printf("ERROR DownloadArticles: proc.Pool.GetArticle '%s' err='%v' ... continue! ng: '%s'", msgID, err, groupName)
						//common.ForceShutdown()
						//return
						continue
					}
				}
				item.Article = art    // set pointer
				returnBatchItem(item) // Send back the successfully downloaded article
				mux.Lock()
				st.downloaded++
				mux.Unlock()
				if db.IsDBshutdown() && len(bq.GetQ) == 0 {
					log.Printf("[FETCHER]: Batch.GetQ DB shutdown")
					return
				}
				//log.Printf("DownloadArticles: Worker %d GetArticle OK group '%s' article (%s)", worker, *item.GroupName, *item.MessageID)
			} // end for item
		}(i)
	} // end for runthis

	var waitHere sync.WaitGroup
	waitHere.Add(DownloadMaxPar)
	for i := 1; i <= DownloadMaxPar; i++ {
		go func(waitHere *sync.WaitGroup) {
			defer waitHere.Done()
			defer func() {
				select {
				case fetchDoneChan <- nil:
				default:
				}
			}()
			for {
				if common.WantShutdown() && len(bq.TodoQ) == 0 {
					//log.Printf("[FETCHER]: Worker received shutdown signal, stopping")
					return
				}
				select {
				case _, ok := <-common.ShutdownChan:
					if !ok {
						//log.Printf("[FETCHER]: Worker received shutdown signal, stopping")
					}
					return
				case groupInfo := <-bq.TodoQ:
					if groupInfo == nil {
						//log.Printf("[FETCHER]: TodoQ closed, worker stopping")
						return
					}
					if common.WantShutdown() && len(bq.TodoQ) == 0 {
						//log.Printf("[FETCHER]: Worker received shutdown signal, stopping")
						return
					}
					/* disabled */
					// Check if database is shutting down
					if db.IsDBshutdown() && len(bq.TodoQ) == 0 {
						log.Printf("[FETCHER]: TodoQ Database shutdown detected, stopping processing")
						return
					}
					/*
						realMem, err := getRealMemoryUsage()
						// Emergency stop if RSS exceeds N GB
						if err == nil && realMem > 12*1024*1024*1024 {
							log.Printf("[MEMORY-EMERGENCY] RSS HIGH! rebooting")
							return
						}
						if err != nil {
							log.Printf("[FETCHER]: Failed to get real memory usage: %v", err)
						}
					*/

					nga, err := db.MainDBGetNewsgroup(groupInfo.Name)
					if err != nil {
						log.Printf("Error in processor.Batch.TodoQ: MainDBGetNewsgroup err='%v'", err)
						continue
					}
					mux.Lock()
					st.todo++
					//log.Printf("[FETCHER]: start *importOverview=%t '%s' (%d-%d) [%d/%d|Q:%d]  --- ", *importOverview, ng.Name, ng.First, ng.Last, todo, queued, len(bq.TodoQ))
					mux.Unlock()
					// Import articles for the selected group

					//log.Printf("[FETCHER]: Downloading articles for newsgroup: %s", ng.Name)

					// Check if date-based downloading is requested
					var startDate time.Time
					useStartDate := false
					mux.Lock()
					if sd, ok := startDates[groupInfo.Name]; ok {
						startDate, useStartDate = sd, true
					} else if o.downloadStartDate != "" {
						startDate, useStartDate = o.downloadStart, true
					}
					mux.Unlock()
					if useStartDate {
						log.Printf("[FETCHER]: Starting ng: '%s' from date: %s", groupInfo.Name, startDate.Format("2006-01-02"))
						//time.Sleep(3 * time.Second) // debug sleep
						err := proc.DownloadArticlesFromDate(groupInfo.Name, startDate, DLParChan, progressDB, groupInfo, common.ShutdownChan)
						if err != nil {
							if err == processor.ErrIsEmptyGroup {
								if err := progressDB.UpdateProgress(providerName, groupInfo.Name, 0); err != nil {
									log.Printf("[FETCHER]: UpdateProgress for empty group '%s' failed: %v", groupInfo.Name, err)
								}
								continue
							}
							log.Printf("[FETCHER]: DownloadArticlesFromDate5 failed: %v", err)
							continue
						}
					} else if nga.ExpiryDays > 0 {
						// Check if group already has articles to decide between initial vs incremental download
						// Use optimized main database check instead of opening group database
						articleCount, err := db.GetArticleCountFromMainDB(groupInfo.Name)
						if err != nil {
							log.Printf("[FETCHER]: Failed to get article count from main DB for '%s': %v", groupInfo.Name, err)
							continue
						}
						if articleCount == 0 {
							// Initial download: use expiry_days to avoid downloading old articles
							startDate := time.Now().AddDate(0, 0, -nga.ExpiryDays)
							log.Printf("[FETCHER]: Initial download for group with expiry_days=%d, starting from calculated date: %s", nga.ExpiryDays, startDate.Format("2006-01-02"))
							//time.Sleep(3 * time.Second) // debug sleep
							err = proc.DownloadArticlesFromDate(groupInfo.Name, startDate, DLParChan, progressDB, groupInfo, common.ShutdownChan)
							if err != nil {
								if err == processor.ErrIsEmptyGroup {
									if err := progressDB.UpdateProgress(providerName, groupInfo.Name, 0); err != nil {
										log.Printf("[FETCHER]: UpdateProgress for empty group '%s' failed: %v", groupInfo.Name, err)
									}
									continue
								}
								log.Printf("[FETCHER]: DownloadArticlesFromDate6 failed: %v", err)
								continue
							}
						} else {
							// Incremental download: continue from where we left off
							log.Printf("[FETCHER]: Incremental download for newsgroup: '%s' (has %d existing articles)", groupInfo.Name, articleCount)
							//time.Sleep(3 * time.Second) // debug sleep
							err = proc.DownloadArticles(groupInfo.Name, DLParChan, progressDB, groupInfo.FetchStart, groupInfo.FetchEnd, common.ShutdownChan)
							if err != nil {
								log.Printf("[FETCHER]: DownloadArticles7 failed: %v", err)
								continue
							}
						}
					} else {
						log.Printf("[FETCHER]: Downloading articles for newsgroup: '%s' (%d - %d) (no expiry limit)", groupInfo.Name, groupInfo.FetchStart, groupInfo.FetchEnd)
						//time.Sleep(3 * time.Second) // debug sleep
						err = proc.DownloadArticles(groupInfo.Name, DLParChan, progressDB, groupInfo.FetchStart, groupInfo.FetchEnd, common.ShutdownChan)
						if err != nil {
							if err != processor.ErrUpToDate {
								log.Printf("[FETCHER]: DownloadArticles8 failed: %v", err)
							}
							continue
						}
					}
					if err != nil {
						if err != processor.ErrUpToDate {
							log.Printf("DownloadArticles9 failed: %v", err)
						}
						continue
					}
				}
			}
		}(&waitHere)
	}
	// Wait for either shutdown signal or server error
	select {
	case _, ok := <-common.ShutdownChan:
		if !ok {
			log.Printf("[FETCHER]: common.ShutdownChan closed, initiating graceful shutdown...")
		}
	case err := <-fetchDoneChan:
		log.Printf("[FETCHER]: got fetchDoneChan: DONE! err='%v'", err)
	}
	waitHere.Wait()
	// Every DownloadArticles call has returned and its feeder has stopped (DownloadArticles
	// waits for it): nothing sends to GetQ any more. Close it so the article workers end,
	// then wait for the check side (on a shutdown it drains Check until the feeder stops).
	// Single-sender invariant: processor.feedDownloadRun is the only sender on GetQ and
	// DownloadArticles returns only after it stopped; a new GetQ sender, or a download path
	// that returns before its feeder stopped, would make this close panic (pinned by
	// processor's TestDLArtPassSwapFeederJoined). The download workers (waitHere) are the
	// only callers of DownloadArticles in a pass.
	close(bq.GetQ)
	wgGet.Wait()
	<-checkDone
	<-feedDone

	mux.Lock()
	defer mux.Unlock()
	log.Printf("[FETCHER-ROUTE]: provider '%s' pass done: downloaded %d articles, notfound %d (newsgroups queued: %d)", providerName, st.downloaded, st.notfound, st.queued)
	if st.groupNotFound > 0 {
		log.Printf("[FETCHER-ROUTE]: provider '%s': %d assigned groups answered 411 (no such group), not tried on another provider this run: %s",
			providerName, st.groupNotFound, firstNames(st.groupNotFoundNames))
	}
	dateStarts.summary()
	return st
}

// logProviderBytes prints one summary line per provider pool at the end of the
// run: the octets the NNTP layer moved, and for a provider with COMPRESS
// DEFLATE on (plan fetcher-compress-deflate) what crossed the socket and how
// much deflate saved on the received stream.
func logProviderBytes(pools []*nntp.Pool) {
	for _, pool := range pools {
		if pool == nil || pool.Backend == nil {
			continue
		}
		name := pool.Backend.Host
		if pool.Backend.Provider != nil && pool.Backend.Provider.Name != "" {
			name = pool.Backend.Provider.Name
		}
		st := pool.Stats()
		if !pool.Backend.Compress {
			log.Printf("[FETCHER] provider '%s': rx %s tx %s", name, humanBytes(st.RxBytesRaw), humanBytes(st.TxBytesRaw))
			continue
		}
		log.Printf("[FETCHER] provider '%s': rx %s (wire %s, compressed %.1f%%) tx %s",
			name, humanBytes(st.RxBytesRaw), humanBytes(st.RxBytesWire),
			savedPercent(st.RxBytesRaw, st.RxBytesWire), humanBytes(st.TxBytesRaw))
	}
}

// savedPercent is how much of raw the wire stream did not carry, in percent;
// 0 when nothing was received.
func savedPercent(raw, wire int64) float64 {
	if raw <= 0 || wire >= raw { // deflate framing can exceed a few short replies: never a negative share
		return 0
	}
	return float64(raw-wire) / float64(raw) * 100
}

// humanBytes formats an octet count with a binary unit (1 decimal above KiB).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

const (
	fetcherMinBatch = 10
	// fetcherMaxBatch is the upper bound of -max-batch: the database clamps its
	// batch size (MaxDBbatch) to 1000 too (internal/database/db_batch.go), and a
	// larger value only grows the per-group XOVER range and ReturnQ buffers.
	fetcherMaxBatch = 1000
)

// startFetcherSignalHandler registers SIGINT/SIGTERM and turns the first one into
// common.ForceShutdown (the workers and UpdateNewsgroupList poll common.WantShutdown).
func startFetcherSignalHandler() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM) // Ctrl+C and SIGTERM take the same graceful path
	go func() {
		<-sigChan
		log.Printf("[FETCHER]: sigChan received shutdown signal")
		common.ForceShutdown() // signal all worker goroutines to stop
	}()
}

// resetProgressTargets returns the groups -reset-progress changes for a single wildcard
// -group (wildcardNG is the prefix before the final *): only the groups with that prefix,
// otherwise all of them (decision D-O1). main selects with selectNewsgroups, which takes
// several patterns; this is its single-prefix form.
func resetProgressTargets(newsgroups []*models.Newsgroup, wildcardNG string) []*models.Newsgroup {
	if wildcardNG == "" {
		return newsgroups
	}
	return selectNewsgroups(newsgroups, groupSelection{prefixes: []string{wildcardNG}})
}

// clampFetcherFlags brings the numeric fetcher flags into their valid ranges.
// -max-batch above fetcherMaxBatch is clamped with a warning.
func clampFetcherFlags(maxBatch, maxBatchThreads, maxQueued, downloadMaxPar *int) {
	if *downloadMaxPar < 1 {
		*downloadMaxPar = 1
	}
	if *maxBatch < fetcherMinBatch {
		*maxBatch = fetcherMinBatch
	}
	if *maxBatch > fetcherMaxBatch {
		log.Printf("[WARN] -max-batch %d is above the database batch cap, clamped to %d", *maxBatch, fetcherMaxBatch)
		*maxBatch = fetcherMaxBatch
	}
	if *maxBatchThreads < 1 {
		*maxBatchThreads = 1
	}
	if *maxBatchThreads > 1024 {
		log.Printf("[WARN] -max-batch-threads %d clamped to 1024 (valid: 1-1024, recommended: 1-16)", *maxBatchThreads)
		*maxBatchThreads = 1024
	}
	if *maxQueued < 1 {
		*maxQueued = 1
	}
}

// feedStats counts what feedCheckQueue queued and skipped.
type feedStats struct {
	queued, skippedWildcard, skippedInActive, skippedActive, skippedLookup uint64
}

// feedCheckQueue sends the name of every newsgroup that matches wildcardNG (empty:
// all) and the -fetch-active-only selection to out. A group whose lookup fails is
// logged and skipped; the remaining groups are still queued. It stops early when
// wantShutdown reports true. onQueued (may be nil) runs after each send.
// The caller closes out.
func feedCheckQueue(newsgroups []*models.Newsgroup, wildcardNG string, fetchActiveOnly bool,
	lookup func(string) (*models.Newsgroup, error), out chan<- *string, wantShutdown func() bool, onQueued func()) feedStats {
	var st feedStats
	for _, ng := range newsgroups {
		if wantShutdown() {
			//log.Printf("[FETCHER]: Feed Batch.Check common.WantShutdown()")
			return st
		}
		if wildcardNG != "" && !strings.HasPrefix(ng.Name, wildcardNG) {
			//log.Printf("[FETCHER] Skipping newsgroup '%s' as it does not match prefix '%s'", ng.Name, wildcardNG)
			st.skippedWildcard++
			continue
		}
		nga, err := lookup(ng.Name)
		if err != nil || nga == nil {
			log.Printf("[FETCHER]: Failed to get newsgroup '%s' from database, skipping it: err='%v'", ng.Name, err)
			st.skippedLookup++
			continue
		}

		if fetchActiveOnly && !nga.Active {
			//log.Printf("[FETCHER]: ignore inactive newsgroup '%s'", ng.Name)
			st.skippedInActive++
			continue
		}

		if !fetchActiveOnly && nga.Active {
			//log.Printf("[FETCHER]: ignore active newsgroup '%s'", ng.Name)
			st.skippedActive++
			continue
		}

		out <- &ng.Name
		//log.Printf("Checking ng: %s", ng.Name)
		st.queued++
		if onQueued != nil {
			onQueued()
		}
	}
	totalSkipped := st.skippedWildcard + st.skippedInActive + st.skippedActive + st.skippedLookup
	log.Printf("[FETCHER]: Feeding Queue Done: %d/%d newsgroups (skipped: %d). Wildcard skipped: %d, skippedInActive: %d, skippedActive: %d, lookup failed: %d. fetchActiveOnly=%t",
		st.queued, len(newsgroups), totalSkipped, st.skippedWildcard, st.skippedInActive, st.skippedActive, st.skippedLookup, fetchActiveOnly)
	return st
}

// getRealMemoryUsage gets actual RSS memory usage from /proc/self/status on Linux
func getRealMemoryUsage() (uint64, error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		// Fallback to runtime stats if /proc/self/status not available
		return 0, nil
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
	return 0, nil
}

// UpdateNewsgroupList fetches the remote newsgroup list from the provider named providerName
// (empty: the primary, the first enabled provider without routing) and adds the groups to the
// database below dataDir that we don't already have and that the routing lets this provider
// serve (router.Allowed). The database is shut down again before it returns.
func UpdateNewsgroupList(dataDir string, updateList *string, excludePrefix *string, updateListForce bool, providerName string) error {
	log.Printf("UpdateNewsgroupList: Starting newsgroup list update from remote server...")

	// Initialize database
	db, cleanup, err := openFetcherDB(dataDir)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer cleanup()

	// Get providers from database
	providers, err := db.GetProviders()
	if err != nil {
		return fmt.Errorf("failed to get providers: %w", err)
	}

	routes, err := db.GetEnabledGroupRoutes()
	if err != nil {
		return fmt.Errorf("failed to get global routes: %w", err)
	}
	// the fetch run's router: the list sync may only pick and fill a provider the fetcher can
	// fetch from (host, port and max conns set), and pins to an unusable provider skip their
	// groups here too
	router, err := newFetchRouter(providers, routes)
	if err != nil {
		return fmt.Errorf("invalid routing: %w", err)
	}
	if len(router.Participants()) == 0 {
		return fmt.Errorf("no enabled providers found in database")
	}

	// -provider, else the primary (without routing: the first enabled provider)
	var listProvider *models.Provider
	if providerName != "" {
		listProvider, err = resolveProviderFlag(providers, router, providerName)
	} else {
		listProvider, err = primaryProvider(providers, router)
	}
	if err != nil {
		return err
	}

	log.Printf("UpdateNewsgroupList: Using provider: %s (Host: %s, Port: %d, SSL: %v)",
		listProvider.Name, listProvider.Host, listProvider.Port, listProvider.SSL)

	// Create NNTP pool for the chosen provider (proxy settings included);
	// only one connection is needed for the list retrieval
	pool := nntp.NewPool(fetcherBackendConfig(listProvider, 1))
	defer pool.ClosePool()
	// Fetch remote newsgroup list
	remoteGroups, err := pool.ListNewsgroups()
	if err != nil {
		return fmt.Errorf("failed to fetch newsgroup list from remote server: %w", err)
	}
	if common.WantShutdown() {
		return errUpdateListStopped
	}
	log.Printf("UpdateNewsgroupList: Fetched %d newsgroups from remote server", len(remoteGroups))
	// only the groups the fetcher would fetch from this provider (routing and global routes)
	allowedGroups, notAllowed := filterAllowedGroups(remoteGroups, router, listProvider.ID)
	if notAllowed > 0 {
		log.Printf("[FETCHER-ROUTE]: UpdateNewsgroupList: %d of %d remote groups skipped: the routing does not let provider '%s' serve them",
			notAllowed, len(remoteGroups), listProvider.Name)
	}

	// Parse the update pattern to determine filtering
	updatePattern := *updateList
	var groupPrefix string
	addAllGroups := false

	if updatePattern == "$all" {
		addAllGroups = true
		log.Printf("UpdateNewsgroupList: Listing all newsgroups from remote server")
	} else if strings.HasSuffix(updatePattern, "*") {
		groupPrefix = strings.TrimSuffix(updatePattern, "*")
		log.Printf("UpdateNewsgroupList: Listing newsgroups with prefix: '%s'", groupPrefix)
	} else if updatePattern != "" {
		groupPrefix = updatePattern
		log.Printf("UpdateNewsgroupList: Listing newsgroups matching: '%s'", groupPrefix)
	} else {
		return fmt.Errorf("invalid update pattern: '%s' (use 'group.*' or '$all')", updatePattern)
	}
	var excludePrefixes []string
	if excludePrefix != nil && *excludePrefix != "" {
		excludePrefixes = strings.Split(*excludePrefix, ",")
		for i, p := range excludePrefixes {
			trimmed := strings.TrimSpace(p)
			if trimmed == "" {
				continue
			}
			excludePrefixes[i] = trimmed
			log.Printf("UpdateNewsgroupList: Excluding newsgroups with prefix: '%s'", excludePrefixes[i])
		}
	}
	// Get existing newsgroups from local database
	localGroups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return fmt.Errorf("failed to get local newsgroups: %w", err)
	}

	// Create a map of existing newsgroup names for fast lookup
	existingGroups := make(map[string]bool, len(localGroups))
	for _, group := range localGroups {
		existingGroups[group.Name] = true
	}

	log.Printf("UpdateNewsgroupList: Found %d newsgroups in local database", len(localGroups))

	newGroupCount, skippedCount, messages, err := addMissingGroups(db, allowedGroups, existingGroups,
		addAllGroups, groupPrefix, excludePrefixes, updateListForce, common.WantShutdown)
	if err != nil {
		log.Printf("Stopped: %d new groups, %d skipped so far, out of %d remote groups", newGroupCount, skippedCount, len(remoteGroups))
		return err
	}

	log.Printf("Result: %d new groups, %d skipped, out of %d remote groups with total: %d messages",
		newGroupCount, skippedCount, len(remoteGroups), messages)

	return nil
}

// errUpdateListStopped is returned by UpdateNewsgroupList when a signal stopped it.
var errUpdateListStopped = errors.New("newsgroup list update stopped by a shutdown signal")

// addMissingGroups lists (and with force inserts, as inactive) every remote group that
// passes the prefix/exclude filters and is not in existingGroups. wantStop is polled
// before each group: once it reports true the loop returns errUpdateListStopped (the
// groups inserted so far stay).
func addMissingGroups(db *database.Database, remoteGroups []nntp.GroupInfo, existingGroups map[string]bool,
	addAllGroups bool, groupPrefix string, excludePrefixes []string, updateListForce bool,
	wantStop func() bool) (newGroupCount, skippedCount int, messages int64, err error) {
	today := time.Now().UTC()
	//today := time.Now().UTC().Truncate(24 * time.Hour)

	// Add new newsgroups that don't exist locally and match the pattern
loopGroups:
	for _, remoteGroup := range remoteGroups {
		if wantStop() {
			return newGroupCount, skippedCount, messages, errUpdateListStopped
		}
		// Apply prefix filtering
		if !addAllGroups {
			if groupPrefix != "" && !strings.HasPrefix(remoteGroup.Name, groupPrefix) {
				skippedCount++
				continue loopGroups
			}
		}
		if len(excludePrefixes) > 0 {
			for _, excludePrefix := range excludePrefixes {
				if excludePrefix == "" {
					continue
				}
				if strings.HasSuffix(excludePrefix, "*") {
					pattern := strings.TrimSuffix(excludePrefix, "*")
					if strings.HasPrefix(remoteGroup.Name, pattern) {
						log.Printf("Excluding newsgroup: '%s' by prefix: '%s' pattern: '%s'", remoteGroup.Name, excludePrefix, pattern)
						skippedCount++
						continue loopGroups
					}
				} else if remoteGroup.Name == excludePrefix {
					log.Printf("Excluding newsgroup: '%s'", remoteGroup.Name)
					skippedCount++
					continue loopGroups
				}
			}
		}
		if !common.IsValidGroupName(remoteGroup.Name) {
			log.Printf("Skipping invalid newsgroup name: '%s'", remoteGroup.Name)
			skippedCount++
			continue loopGroups
		}
		if !existingGroups[remoteGroup.Name] {
			// Create a new newsgroup model
			newGroup := &models.Newsgroup{
				Name:      remoteGroup.Name,
				Active:    false,              // Default to inactive
				Status:    remoteGroup.Status, // newsgroups y,m,c,a status
				CreatedAt: today,              // Default created at
			}

			if updateListForce {
				// Insert the new newsgroup
				err := db.InsertNewsgroup(newGroup)
				if err != nil {
					log.Printf("Failed to insert newsgroup '%s': %v", remoteGroup.Name, err)
					continue
				}

				log.Printf("(Added new) newsgroup: '%s'", remoteGroup.Name)
			} else {
				log.Printf("(not added) newsgroup: '%s' messages=%d status=%s", remoteGroup.Name, remoteGroup.Count, remoteGroup.Status)
			}
			newGroupCount++
			messages += remoteGroup.Count
		}
	}

	return newGroupCount, skippedCount, messages, nil
}
