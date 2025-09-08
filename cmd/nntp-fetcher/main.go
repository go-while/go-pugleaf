// Real NNTP provider test for go-pugleaf
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// showUsageExamples displays usage examples for connection testing
func showUsageExamples() {
	fmt.Println("\n=== NNTP Fetcher - Connection Testing Examples ===")
	fmt.Println("The NNTP fetcher is used for testing connections and downloading articles.")
	fmt.Println("For newsgroup analysis, use the separate nntp-analyze command.")
	fmt.Println()
	fmt.Println("Connection Testing:")
	fmt.Println("  ./nntp-fetcher -test-conn -group alt.test")
	fmt.Println()
	fmt.Println("Article Downloading:")
	fmt.Println("  ./nntp-fetcher -group alt.* (downloads all groups with prefix alt.*)")
	fmt.Println("  ./nntp-fetcher -group alt.test")
	fmt.Println("  ./nntp-fetcher -group alt.test -xover-copy (use xover-copy to do identical copy from remote server!)")
	fmt.Println("  ./nntp-fetcher -group alt.test -download-start-date 2024-12-31")
	fmt.Println()
	fmt.Println("Newsgroup List Update:")
	fmt.Println("  ./nntp-fetcher -update-list (fetch remote newsgroup list and add new groups to database)")
	fmt.Println()
	fmt.Println("Server Configuration:")
	fmt.Println("  ./nntp-fetcher -test-conn -host news.server.com -port 563")
	fmt.Println("  ./nntp-fetcher -test-conn -username user -password pass")
	fmt.Println()
	fmt.Println("Note: For newsgroup analysis use cmd/nntp-analyze instead")
	fmt.Println()
}

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	database.DBidleTimeOut = 15 * time.Second
	database.FETCH_MODE = true // prevents booting caches
	log.Printf("Starting go-pugleaf NNTP Fetcher (version %s)", config.AppVersion)
	// Command line flags for NNTP fetcher configuration
	var newsgroups []*models.Newsgroup
	var (
		maxBatchThreads         = flag.Int("max-batch-threads", 16, "Limit how many newsgroup batches will be processed concurrently (default: 16) more can eat your memory and disk IO!")
		maxBatch                = flag.Int("max-batch", 128, "Maximum number of articles to process in a batch (recommended: 100)")
		maxLoops                = flag.Int("max-loops", 1, "Loop a group this many times and fetch `-max-batch N` every loop")
		maxQueued               = flag.Int("max-queue", 16384, "Limit db_batch to have max N articles queued over all newsgroups")
		ignoreInitialTinyGroups = flag.Int64("ignore-initial-tiny-groups", 0, "If > 0: initial fetch ignores tiny groups with fewer articles than this (default: 0)")
		importOverview          = flag.Bool("xover-copy", false, "Do not use xover-copy unless you want to Copy xover data from remote server and then articles. instead of normal 'xhdr message-id' --> articles (default: false)")
		fetchNewsgroup          = flag.String("group", "", "Newsgroup to fetch (default: empty = all groups once up to max-batch) or rocksolid.* with final wildcard to match prefix.*")
		hostnamePath            = flag.String("nntphostname", "", "Your hostname must be set!")
		useShortHashLenPtr      = flag.Int("useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
		fetchActiveOnly         = flag.Bool("fetch-active-only", true, "Fetch only active newsgroups (default: true)")
		downloadMaxPar          = flag.Int("download-max-par", 1, "run this many groups in parallel, can eat your memory! (default: 1)")
		updateList              = flag.String("update-newsgroups-from-remote", "", "Fetch remote newsgroup list from first enabled provider and add new groups to database (default: empty, nothing. use \"group.*\" or \"\\$all\")")
		// Download options with date filtering
		downloadStartDate = flag.String("download-start-date", "", "Start downloading articles from this date (YYYY-MM-DD format)")
		showHelp          = flag.Bool("help", false, "Show usage examples and exit")
	)
	flag.Parse()
	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}
	if *updateList != "" {
		if err := UpdateNewsgroupList(updateList); err != nil {
			log.Fatalf("Newsgroup list update failed: %v", err)
		}
		os.Exit(0)
	}

	if *downloadMaxPar < 1 {
		*downloadMaxPar = 1
	}
	if *maxBatch < 10 {
		*maxBatch = 10
	}
	if *maxLoops != 1 {
		*maxLoops = 1 // hardcoded to 1 TODO code path removed
	}
	if *maxBatchThreads < 1 {
		*maxBatchThreads = 1
	}
	if *maxQueued < 1 {
		*maxQueued = 1
	}
	if *maxBatchThreads > 128 {
		*maxBatchThreads = 128
		log.Printf("[WARN] max batch threads: %d (should be between 1 and 128. recommended: 16)", *maxBatchThreads)
	}
	if *maxBatch > 1000 {
		log.Printf("[WARN] max batch: %d (should be between 100 and 1000)", *maxBatch)
	}
	if *hostnamePath == "" {
		log.Fatalf("[NNTP]: Error: hostname must be set!")
	}
	// Validate command-line flag
	if *useShortHashLenPtr < 2 || *useShortHashLenPtr > 7 {
		log.Fatalf("Invalid UseShortHashLen: %d (must be between 2 and 7)", *useShortHashLenPtr)
	}

	database.InitialBatchChannelSize = *maxBatch * *maxLoops
	database.MaxBatchThreads = *maxBatchThreads
	database.MaxBatchSize = *maxBatch
	database.MaxQueued = *maxQueued
	nntp.MaxReadLinesXover = int64(*maxBatch)
	processor.LocalHostnamePath = *hostnamePath
	processor.XoverCopy = *importOverview
	processor.MaxBatchSize = int64(*maxBatch)
	//processor.LOOPS_PER_GROUPS = *maxLoops

	// Set global max read lines for xover

	mainConfig := config.NewDefaultConfig()
	mainConfig.Server.Hostname = *hostnamePath

	// Initialize database (default config, data in ./data)
	db, err := database.OpenDatabase(nil)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize progress database once to avoid opening/closing for each group
	progressDB, err := database.NewProgressDB("data/progress.db")
	if err != nil {
		log.Fatalf("Failed to initialize progress database: %v", err)
	}
	defer progressDB.Close()

	// Set up cross-platform signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt) // Cross-platform (Ctrl+C on both Windows and Linux)

	db.WG.Add(2) // Adds to wait group for db_batch.go cron jobs
	db.WG.Add(1) // Adds for history: one for writer worker
	db.WG.Add(1) // this fetch loop below

	// Get UseShortHashLen from database (with safety check)
	storedUseShortHashLen, isLocked, err := db.GetHistoryUseShortHashLen(*useShortHashLenPtr)
	if err != nil {
		log.Fatalf("Failed to get UseShortHashLen from database: %v", err)
	}
	var useShortHashLen int
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

	providers, err := db.GetProviders()
	if err != nil || len(providers) == 0 {
		// handle error appropriately
		log.Printf("Failed to get providers (%d): %v", len(providers), err)
		return
	}
	log.Printf("Loaded %d providers from database", len(providers))

	// Get all newsgroups from database using admin function (includes empty groups)
	suffixWildcard := strings.HasSuffix(*fetchNewsgroup, "*")
	var wildcardNG string
	if suffixWildcard {
		// cut string by final *
		wildcardNG = strings.TrimSuffix(*fetchNewsgroup, "*")
		log.Printf("[FETCHER]: Using wildcard newsgroup prefix: '%s'", wildcardNG)
		time.Sleep(3 * time.Second) // debug sleep
	}
	if fetchNewsgroup == nil || *fetchNewsgroup == "" || *fetchNewsgroup == "$all" || suffixWildcard {
		ngs, err := db.MainDBGetAllNewsgroups()
		if err != nil {
			log.Printf("[FETCHER]: Failed to get newsgroups from database: %v", err)
			return
		}
		newsgroups = ngs
		if len(newsgroups) == 0 {
			fmt.Println("[FETCHER]: No newsgroups found in database")
			return
		}
		fmt.Printf("[FETCHER]: %d newsgroups in database\n", len(newsgroups))
	} else if *fetchNewsgroup != "" {
		newsgroups = append(newsgroups, &models.Newsgroup{Name: *fetchNewsgroup})
	}

	pools := make([]*nntp.Pool, 0, len(providers))
	for _, p := range providers {
		if !p.Enabled || p.Host == "" || p.Port <= 0 || p.MaxConns <= 0 {
			log.Printf("Ignore disabled Provider: %s", p.Name)
			continue
		}
		if strings.Contains(p.Host, "eternal-september") && p.MaxConns > 3 {
			p.MaxConns = 3
		} else if strings.Contains(p.Host, "blueworld-hosting") && p.MaxConns > 3 {
			p.MaxConns = 3
		}
		if p.MaxConns > *maxBatch {
			p.MaxConns = *maxBatch // limit conns to maxBatch
		}
		log.Printf("Provider: %s (ID: %d, Host: %s, Port: %d, SSL: %v, MaxConns: %d)",
			p.Name, p.ID, p.Host, p.Port, p.SSL, p.MaxConns)

		// Convert models.Provider to config.Provider for the BackendConfig
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
		}

		backendConfig := &nntp.BackendConfig{
			Host:     p.Host,     // copy values to first level
			Port:     p.Port,     // copy values to first level
			SSL:      p.SSL,      // copy values to first level
			Username: p.Username, // copy values to first level
			Password: p.Password, // copy values to first level
			MaxConns: p.MaxConns, // copy values to first level
			//ConnectTimeout: 30 * time.Second,
			//ReadTimeout:    60 * time.Second,
			//WriteTimeout:   30 * time.Second,
			Provider: configProvider, // Set the Provider field
		}
		pool := nntp.NewPool(backendConfig)
		pool.StartCleanupWorker(5 * time.Second)
		pools = append(pools, pool)
		log.Printf("Created connection pool for provider '%s' with max %d connections", p.Name, p.MaxConns)
		defer pool.ClosePool()
		break // Only use the first provider for import
	}

	fetchDoneChan := make(chan error, 1)
	shutdownChan := make(chan struct{}) // For graceful shutdown signaling
	go func() {
		<-sigChan
		log.Printf("[FETCHER]: Received shutdown signal, initiating graceful shutdown...")
		// Signal all worker goroutines to stop
		close(shutdownChan)
	}()
	proc := processor.NewProcessor(db, pools[0], useShortHashLen) // Use first pool for import
	if proc == nil {
		log.Fatalf("[FETCHER]: Failed to create processor: %v", err)
	}
	// Set up the date parser adapter to use processor's ParseNNTPDate
	database.GlobalDateParser = processor.ParseNNTPDate

	if proc.Pool.Backend.Provider == nil || proc.Pool.Backend.Provider.Name == "" {
		log.Fatalf("No provider backend available: '%#v'", proc.Pool.Backend.Provider)
	}
	log.Printf("[FETCHER]: Provider: %s @ MaxConns: %d", proc.Pool.Backend.Provider.Name, proc.Pool.Backend.MaxConns)
	DownloadMaxPar := *downloadMaxPar // unchangeable (code not working yet)
	DLParChan := make(chan struct{}, DownloadMaxPar)
	var mux sync.Mutex
	downloaded := 0
	// scan group worker
	queued := 0
	todo := 0
	go func() {
		for _, ng := range newsgroups {
			if wildcardNG != "" && !strings.HasPrefix(ng.Name, wildcardNG) {
				//log.Printf("[FETCHER] Skipping newsgroup '%s' as it does not match prefix '%s'", ng.Name, wildcardNG)
				continue
			}
			nga, err := db.MainDBGetNewsgroup(ng.Name)
			if err != nil || nga == nil || *fetchActiveOnly && !nga.Active {
				//log.Printf("[FETCHER] ignore newsgroup '%s' err='%v' ng='%#v'", ng.Name, err, ng)
				continue
			}
			if db.IsDBshutdown() {
				//log.Printf("[FETCHER]: Database shutdown detected, stopping processing")
				return
			}
			processor.Batch.Check <- &ng.Name
			//log.Printf("Checking ng: %s", ng.Name)
			mux.Lock()
			queued++
			mux.Unlock()
		}
		close(processor.Batch.Check)
		log.Printf("Queued %d newsgroups", queued)
	}()
	var wgCheck sync.WaitGroup
	startDates := make(map[string]string)
	for i := 1; i <= proc.Pool.Backend.MaxConns; i++ {
		wgCheck.Add(1)
		go func(worker int, wgCheck *sync.WaitGroup, progressDB *database.ProgressDB) {
			defer wgCheck.Done()
			for ng := range processor.Batch.Check {
				groupInfo, err := proc.Pool.SelectGroup(*ng)
				if err != nil || groupInfo == nil {
					if err == nntp.ErrNewsgroupNotFound {
						//log.Printf("[FETCHER]: Newsgroup not found: '%s'", *ng)
						continue
					}
					log.Printf("[FETCHER]: Error in select ng='%s' groupInfo='%#v' err='%v'", *ng, groupInfo, err)
					continue
				}
				//log.Printf("[FETCHER]: ng '%s', REMOTE groupInfo: %#v", *ng, groupInfo),
				var lastArticle int64
				if *downloadStartDate != "" {
					lastArticle = -1
				} else {
					mux.Lock()
					lastArticle, err = progressDB.GetLastArticle(proc.Pool.Backend.Provider.Name, *ng)
					if err != nil || lastArticle < -1 {
						log.Printf("[FETCHER]: Failed to get last article for group '%s' from provider '%s': %v", *ng, proc.Pool.Backend.Provider.Name, err)
						mux.Unlock()
						continue
					}
					mux.Unlock()
				}
				switch lastArticle {
				case 0:
					// Open group DB only when we need to check last-article date
					groupDBs, err := proc.DB.GetGroupDBs(*ng)
					if err != nil {
						log.Printf("[FETCHER]: Failed to get group DBs for newsgroup '%s': %v", *ng, err)
						continue
					}
					lastArticleDate, checkDateErr := proc.DB.GetLastArticleDate(groupDBs)
					// ensure close regardless of errors
					if ferr := proc.DB.ForceCloseGroupDBs(groupDBs); ferr != nil {
						log.Printf("[FETCHER]: ForceCloseGroupDBs error for '%s': %v", *ng, ferr)
					}
					if checkDateErr != nil {
						log.Printf("[FETCHER]: Failed to get last article date for '%s': %v", *ng, checkDateErr)
						continue
					}

					// If group has existing articles, use date-based download instead
					if lastArticleDate != nil {
						log.Printf("[FETCHER]: No progress for provider '%s' but group '%s' has existing articles, switching to date-based download from: %s",
							proc.Pool.Backend.Provider.Name, *ng, lastArticleDate.Format("2006-01-02"))
						mux.Lock()
						startDates[*ng] = lastArticleDate.Format("2006-01-02")
						mux.Unlock()
						//go proc.DownloadArticlesFromDate(*ng, *lastArticleDate, 0, DLParChan, progressDB) // Use 0 for ignore threshold since group already exists
					}

				case -1: // User-requested date rescan
					//log.Printf("[FETCHER]: Date rescan '%s'", *ng)
					// Set date-based download from epoch for complete rescan
					mux.Lock()
					if *downloadStartDate != "" {
						startDates[*ng] = *downloadStartDate
					}
					mux.Unlock()
					// Reset lastArticle to 0 and fall through to normal range processing
					lastArticle = 0
				default:
					// pass
				}
				//log.Printf("DEBUG-RANGE: ng='%s' lastArticle=%d (after switch)", *ng, lastArticle)
				start := lastArticle + 1                  // Start from the first article in the remote group
				end := start + processor.MaxBatchSize - 1 // End at the last article in the remote group
				//log.Printf("DEBUG-RANGE: ng='%s' calculated start=%d end=%d groupInfo.Last=%d", *ng, start, end, groupInfo.Last)

				// For date-based downloads, don't cap end to groupInfo.Last since they use date filtering
				mux.Lock()
				isDateBased := startDates[*ng] != ""
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

				groupInfo.First = start
				groupInfo.Last = end
				processor.Batch.TodoQ <- groupInfo
				log.Printf("[FETCHER]: TodoQ '%s' toFetch=%d start=%d end=%d", *ng, toFetch, start, end)
				//time.Sleep(time.Second * 2)
			}
			//log.Printf("[FETCHER]: Worker %d finished feeding TodoQ", worker)
		}(i, &wgCheck, progressDB)
	} // end for scan group worker
	go func(wgCheck *sync.WaitGroup) {
		wgCheck.Wait()
		close(processor.Batch.TodoQ)
		log.Printf("[FETCHER]: All newsgroups queued for processing, closing TodoQ")
	}(&wgCheck)

	// download worker
	for i := 1; i <= proc.Pool.Backend.MaxConns; i++ {
		// fire up async goroutines to fetch articles
		go func(worker int) {
			//log.Printf("DownloadArticles: Worker %d group '%s' start", worker, groupName)
			for item := range processor.Batch.GetQ {
				if proc.WantShutdown(shutdownChan) {
					log.Printf("DownloadArticles: Worker %d received shutdown signal, stopping", worker)
					return
				}
				//log.Printf("DownloadArticles: Worker %d GetArticle group '%s' article (%s)", worker, *item.GroupName, *item.MessageID)
				art, err := proc.Pool.GetArticle(item.MessageID, true)
				if err != nil || art == nil {
					log.Printf("ERROR DownloadArticles: proc.Pool.GetArticle '%s' err='%v' .. continue", *item.MessageID, err)
					item.Error = err     // Set error on item
					item.ReturnQ <- item // Send failed item back
					continue
				}
				item.Article = art   // set pointer
				item.ReturnQ <- item // Send back the successfully downloaded article
				mux.Lock()
				downloaded++
				mux.Unlock()
				//log.Printf("DownloadArticles: Worker %d GetArticle OK group '%s' article (%s)", worker, *item.GroupName, *item.MessageID)
			} // end for item
		}(i)
	} // end for runthis

	var waitHere sync.WaitGroup
	waitHere.Add(DownloadMaxPar)
	for i := 1; i <= DownloadMaxPar; i++ {
		go func(waitHere *sync.WaitGroup) {
			defer waitHere.Done()
			errChan := make(chan error, len(newsgroups))
			defer func() {
				select {
				case fetchDoneChan <- nil:
				default:
				}
			}()
			for {
				select {
				case _, ok := <-shutdownChan:
					if !ok {
						log.Printf("[FETCHER]: Worker received shutdown signal, stopping")
					}
					return
				case ng := <-processor.Batch.TodoQ:
					if ng == nil {
						//log.Printf("[FETCHER]: TodoQ closed, worker stopping")
						return
					}
					if db.IsDBshutdown() {
						//log.Printf("[FETCHER]: TodoQ Database shutdown detected, stopping processing. still queued in TodoQ: %d", len(processor.Batch.TodoQ))
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

					nga, err := db.MainDBGetNewsgroup(ng.Name)
					if err != nil {
						log.Printf("Error in processor.Batch.TodoQ: MainDBGetNewsgroup err='%v'", err)
						errChan <- err
						continue
					}
					mux.Lock()
					todo++
					//log.Printf("[FETCHER]: start *importOverview=%t '%s' (%d-%d) [%d/%d|Q:%d]  --- ", *importOverview, ng.Name, ng.First, ng.Last, todo, queued, len(processor.Batch.TodoQ))
					mux.Unlock()
					// Import articles for the selected group
					switch *importOverview {
					case false:
						//log.Printf("[FETCHER]: Downloading articles for newsgroup: %s", ng.Name)

						// Check if date-based downloading is requested
						var useStartDate string
						mux.Lock()
						if startDates[ng.Name] != "" {
							useStartDate = startDates[ng.Name]
						} else if *downloadStartDate != "" {
							useStartDate = *downloadStartDate
						}
						mux.Unlock()
						if useStartDate != "" {
							startDate, err := time.Parse("2006-01-02", useStartDate)
							if err != nil {
								log.Fatalf("[FETCHER]: Invalid start date format '%s': %v (expected YYYY-MM-DD)", useStartDate, err)
							}
							log.Printf("[FETCHER]: Starting ng: '%s' from date: %s", ng.Name, startDate.Format("2006-01-02"))
							//time.Sleep(3 * time.Second) // debug sleep
							err = proc.DownloadArticlesFromDate(ng.Name, startDate, *ignoreInitialTinyGroups, DLParChan, progressDB, shutdownChan)
							if err != nil {
								log.Printf("[FETCHER]: DownloadArticlesFromDate5 failed: %v", err)
								errChan <- err
								continue
							}
						} else if nga.ExpiryDays > 0 {
							// Check if group already has articles to decide between initial vs incremental download
							// Use optimized main database check instead of opening group database
							articleCount, err := db.GetArticleCountFromMainDB(ng.Name)
							if err != nil {
								log.Printf("[FETCHER]: Failed to get article count from main DB for '%s': %v", ng.Name, err)
								errChan <- err
								continue
							}
							if articleCount == 0 {
								// Initial download: use expiry_days to avoid downloading old articles
								startDate := time.Now().AddDate(0, 0, -nga.ExpiryDays)
								log.Printf("[FETCHER]: Initial download for group with expiry_days=%d, starting from calculated date: %s", nga.ExpiryDays, startDate.Format("2006-01-02"))
								//time.Sleep(3 * time.Second) // debug sleep
								err = proc.DownloadArticlesFromDate(ng.Name, startDate, *ignoreInitialTinyGroups, DLParChan, progressDB, shutdownChan)

								if err != nil {
									errChan <- err
									log.Printf("[FETCHER]: DownloadArticlesFromDate6 failed: %v", err)
									continue
								}
							} else {
								// Incremental download: continue from where we left off
								log.Printf("[FETCHER]: Incremental download for newsgroup: '%s' (has %d existing articles)", ng.Name, articleCount)
								//time.Sleep(3 * time.Second) // debug sleep
								err = proc.DownloadArticles(ng.Name, *ignoreInitialTinyGroups, DLParChan, progressDB, ng.First, ng.Last, shutdownChan)
								if err != nil {
									log.Printf("[FETCHER]: DownloadArticles7 failed: %v", err)
									errChan <- err
									continue
								}
							}
						} else {
							log.Printf("[FETCHER]: Downloading articles for newsgroup: '%s' (%d - %d) (no expiry limit)", ng.Name, ng.First, ng.Last)
							//time.Sleep(3 * time.Second) // debug sleep
							err = proc.DownloadArticles(ng.Name, *ignoreInitialTinyGroups, DLParChan, progressDB, ng.First, ng.Last, shutdownChan)
							if err != nil {
								if err != processor.ErrUpToDate {
									log.Printf("[FETCHER]: DownloadArticles8 failed: %v", err)
								}
								errChan <- err
								continue
							}
						}
						if err != nil {
							if err != processor.ErrUpToDate {
								log.Printf("DownloadArticles9 failed: %v", err)
							}
							continue
						}
						//}(DLParChan) // end go func
						/*
							case true:
								log.Printf("[FETCHER]: Experimental! Start DownloadArticlesViaOverview for group '%s'", ng.Name)
								err = proc.DownloadArticlesViaOverview(ng.Name)
								if err != nil {
									log.Printf("[FETCHER]: DownloadArticlesViaOverview failed: %v", err)
									continue
								}
								fmt.Println("[FETCHER]: ✓ Article import complete.")

								groupDBs, err := db.GetGroupDBs(ng.Name)
								if err != nil {
									log.Fatalf("[FETCHER]: Failed to get group DBs for '%s': %v", ng.Name, err)
								}
								defer groupDBs.Return(db)
						*/
					}
				}
			}
		}(&waitHere)
	}
	db.WG.Done()
	// Wait for either shutdown signal or server error
	select {
	case _, ok := <-shutdownChan:
		if !ok {
			log.Printf("[FETCHER]: Shutdown channel closed, initiating graceful shutdown...")
		}
	case err := <-fetchDoneChan:
		log.Printf("[FETCHER]: DONE! err='%v'", err)
	}
	waitHere.Wait()
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

	mux.Lock()
	log.Printf("[FETCHER]: Total downloaded: %d articles (newsgroups: %d)", downloaded, queued)
	mux.Unlock()

	log.Printf("[FETCHER]: Graceful shutdown completed. Exiting here.")
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

// UpdateNewsgroupList fetches the remote newsgroup list from the first enabled provider
// and adds all groups to the database that we don't already have
func UpdateNewsgroupList(updateList *string) error {
	log.Printf("Starting newsgroup list update from remote server...")

	// Initialize database
	db, err := database.OpenDatabase(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	// Get providers from database
	providers, err := db.GetProviders()
	if err != nil || len(providers) == 0 {
		return fmt.Errorf("failed to get providers (%d): %v", len(providers), err)
	}

	// Find first enabled provider
	var firstProvider *models.Provider
	for _, p := range providers {
		if p.Enabled && p.Host != "" && p.Port > 0 {
			firstProvider = p
			break
		}
	}

	if firstProvider == nil {
		return fmt.Errorf("no enabled providers found in database")
	}

	log.Printf("Using provider: %s (Host: %s, Port: %d, SSL: %v)",
		firstProvider.Name, firstProvider.Host, firstProvider.Port, firstProvider.SSL)

	// Create NNTP backend config using the first enabled provider
	backendConfig := &nntp.BackendConfig{
		Host:     firstProvider.Host,
		Port:     firstProvider.Port,
		SSL:      firstProvider.SSL,
		Username: firstProvider.Username,
		Password: firstProvider.Password,
		MaxConns: 1, // Only need one connection for list retrieval
	}

	// Create NNTP pool
	pool := nntp.NewPool(backendConfig)
	defer pool.ClosePool()

	// Get a connection from the pool
	conn, err := pool.Get()
	if err != nil {
		return fmt.Errorf("failed to get NNTP connection: %w", err)
	}
	defer pool.Put(conn)

	log.Printf("Connected to %s:%d, fetching newsgroup list...", firstProvider.Host, firstProvider.Port)

	// Fetch the complete newsgroup list
	remoteGroups, err := conn.ListGroups()
	if err != nil {
		return fmt.Errorf("failed to fetch newsgroup list: %w", err)
	}

	log.Printf("Fetched %d newsgroups from remote server", len(remoteGroups))

	// Parse the update pattern to determine filtering
	updatePattern := *updateList
	var groupPrefix string
	addAllGroups := false

	if updatePattern == "$all" {
		addAllGroups = true
		log.Printf("Adding all newsgroups from remote server")
	} else if strings.HasSuffix(updatePattern, "*") {
		groupPrefix = strings.TrimSuffix(updatePattern, "*")
		log.Printf("Adding newsgroups with prefix: '%s'", groupPrefix)
	} else if updatePattern != "" {
		groupPrefix = updatePattern
		log.Printf("Adding newsgroups matching: '%s'", groupPrefix)
	} else {
		return fmt.Errorf("invalid update pattern: '%s' (use 'group.*' or '$all')", updatePattern)
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

	log.Printf("Found %d existing newsgroups in local database", len(localGroups))

	// Add new newsgroups that don't exist locally and match the pattern
	newGroupCount := 0
	skippedCount := 0
	for _, remoteGroup := range remoteGroups {
		// Apply prefix filtering
		if !addAllGroups {
			if groupPrefix != "" && !strings.HasPrefix(remoteGroup.Name, groupPrefix) {
				skippedCount++
				continue
			}
		}

		if !existingGroups[remoteGroup.Name] {
			// Create a new newsgroup model
			newGroup := &models.Newsgroup{
				Name:      remoteGroup.Name,
				Active:    true,             // Default to active
				Status:    "y",              // Default posting status
				CreatedAt: time.Now().UTC(), // Default created at
			}

			// Insert the new newsgroup
			err := db.InsertNewsgroup(newGroup)
			if err != nil {
				log.Printf("Failed to insert newsgroup '%s': %v", remoteGroup.Name, err)
				continue
			}

			log.Printf("Added new newsgroup: %s", remoteGroup.Name)
			newGroupCount++
		}
	}

	log.Printf("Newsgroup list update completed: %d new groups added, %d skipped (prefix filter), out of %d remote groups",
		newGroupCount, skippedCount, len(remoteGroups))

	return nil
}
