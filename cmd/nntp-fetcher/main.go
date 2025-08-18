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
	database.FETCH_MODE = true
	log.Printf("Starting go-pugleaf NNTP Fetcher (version %s)", config.AppVersion)
	// Command line flags for NNTP fetcher configuration
	var newsgroups []*models.Newsgroup
	var (
		host                    = flag.String("host", "lux-feed1.newsdeef.eu", "NNTP hostname")
		port                    = flag.Int("port", 563, "NNTP port")
		username                = flag.String("username", "read", "NNTP username")
		password                = flag.String("password", "only", "NNTP password")
		ssl                     = flag.Bool("ssl", true, "Use SSL/TLS connection")
		timeout                 = flag.Int("timeout", 30, "Connection timeout in seconds")
		testMsg                 = flag.String("message-id", "", "Test message ID to fetch (optional)")
		maxBatch                = flag.Int("max-batch", 128, "Maximum number of articles to process in a batch (recommended: 100)")
		maxLoops                = flag.Int("max-loops", 1, "Loop a group this many times and fetch `-max-batch N` every loop")
		ignoreInitialTinyGroups = flag.Int64("ignore-initial-tiny-groups", 0, "If > 0: initial fetch ignores tiny groups with fewer articles than this (default: 0)")
		importOverview          = flag.Bool("xover-copy", false, "Do not use xover-copy unless you want to Copy xover data from remote server and then articles. instead of normal 'xhdr message-id' --> articles (default: false)")
		fetchNewsgroup          = flag.String("group", "", "Newsgroup to fetch (default: empty = all groups once up to max-batch) or rocksolid.* with final wildcard to match prefix.*")
		hostnamePath            = flag.String("nntphostname", "", "Your hostname must be set!")
		testConn                = flag.Bool("test-conn", false, "Test direct connection to NNTP server and exit (default: false)")
		useShortHashLenPtr      = flag.Int("useshorthashlen", 7, "short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!")
		fetchActiveOnly         = flag.Bool("fetch-active-only", true, "Fetch only active newsgroups (default: true)")
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
	if *testConn {
		if err := ConnectionTest(host, port, username, password, ssl, timeout, *fetchNewsgroup, testMsg); err != nil {
			log.Fatalf("Connection test failed: %v", err)
		}
		os.Exit(0)
	}
	if *maxBatch < 1 || *maxBatch > 4000 {
		log.Printf("Invalid max batch size: %d (must be between 1 and 4000)", *maxBatch)
	}
	if *maxLoops < 1 || *maxLoops > 2500 {
		log.Fatalf("Invalid max batch size: %d (must be between 1 and 2500)", *maxBatch)
	}
	if *hostnamePath == "" {
		log.Fatalf("[NNTP]: Error: hostname must be set!")
	}
	database.InitialBatchChannelSize = *maxBatch * *maxLoops // set per-group queue cap to maxBatch (decouple from loops)
	database.MaxBatchSize = *maxBatch
	nntp.MaxReadLinesXover = int64(*maxBatch) // Set global max read lines for xover
	processor.LocalHostnamePath = *hostnamePath
	processor.XoverCopy = *importOverview       // Set global xover copy flag
	processor.MaxBatch = nntp.MaxReadLinesXover // Update processor MaxBatch to use the new NNTP limit
	processor.LOOPS_PER_GROUPS = *maxLoops      // Set global loops per group
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

	// Validate command-line flag
	if *useShortHashLenPtr < 2 || *useShortHashLenPtr > 7 {
		log.Fatalf("Invalid UseShortHashLen: %d (must be between 2 and 7)", *useShortHashLenPtr)
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
	//ctx := context.Background()

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
	DownloadMaxPar := 1 // unchangeable (code not working yet)
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
				log.Printf("[FETCHER]: Database shutdown detected, stopping processing")
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
					log.Printf("[FETCHER]: Error in select ng='%s' groupInfo='%#v' err='%v'", *ng, err, groupInfo)
					continue
				}
				//log.Printf("[FETCHER]: ng '%s', REMOTE groupInfo: %#v", *ng, groupInfo),
				mux.Lock()
				lastArticle, err := progressDB.GetLastArticle(proc.Pool.Backend.Provider.Name, *ng)
				if err != nil || lastArticle < -1 {
					log.Printf("[FETCHER]: Failed to get last article for group '%s' from provider '%s': %v", *ng, proc.Pool.Backend.Provider.Name, err)
					mux.Unlock()
					continue
				}
				mux.Unlock()

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

				case -1: // User-requested date rescan - reset to start from beginning
					log.Printf("[FETCHER]: Date rescan mode for group '%s', starting from beginning", *ng)
					// Set date-based download from epoch for complete rescan
					mux.Lock()
					startDates[*ng] = "1970-01-01"
					mux.Unlock()
					// Reset lastArticle to 0 and fall through to normal range processing
					lastArticle = 0
				default:
					// pass
				}
				//log.Printf("DEBUG-RANGE: ng='%s' lastArticle=%d (after switch)", *ng, lastArticle)
				start := lastArticle + 1                     // Start from the first article in the remote group
				end := start + int64(processor.MaxBatch) - 1 // End at the last article in the remote group
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
				time.Sleep(time.Second * 2)
			}
		}(i, &wgCheck, progressDB)
	} // end for scan group worker
	go func(wgCheck *sync.WaitGroup) {
		wgCheck.Wait()
		close(processor.Batch.TodoQ)
	}(&wgCheck)

	// download worker
	for i := 1; i <= proc.Pool.Backend.MaxConns; i++ {
		// fire up async goroutines to fetch articles
		go func(worker int) {
			//log.Printf("DownloadArticles: Worker %d group '%s' start", worker, groupName)
			for item := range processor.Batch.Queue {
				//log.Printf("DownloadArticles: Worker %d processing group '%s' article (%s)", worker, *item.GroupName, *item.MessageID)
				art, err := proc.Pool.GetArticle(item.MessageID)
				if err != nil {
					log.Printf("ERROR DownloadArticles: proc.Pool.GetArticle '%s' err='%v' .. continue", *item.MessageID, err)
					item.Error = err // Set error on item
					//log.Printf("DEBUG-SEND: Worker %d sending ERROR item to Return channel", worker)
					processor.Batch.Return <- item // Send failed item back
					continue
				}
				item.Article = art // set pointer
				//log.Printf("DEBUG-SEND: Worker %d sending SUCCESS item to Return channel", worker)
				processor.Batch.Return <- item // Send back the successfully downloaded article
				mux.Lock()
				downloaded++
				mux.Unlock()
				//log.Printf("DownloadArticles: Worker %d downloaded group '%s' article (%s)", worker, *item.GroupName, *item.MessageID)
			} // end for item
		}(i)
	} // end for runthis

	go func() {
		errChan := make(chan error, len(newsgroups))
		defer db.WG.Done()
		defer func() {
			fetchDoneChan <- nil
		}()
		for ng := range processor.Batch.TodoQ {
			if db.IsDBshutdown() {
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

			nga, err := db.MainDBGetNewsgroup(ng.Name)
			if err != nil {
				log.Printf("Error in processor.Batch.TodoQ: MainDBGetNewsgroup err='%v'", err)
				errChan <- err
				continue
			}
			mux.Lock()
			todo++
			log.Printf("--- Fetch '%s' (%d-%d) [%d/%d|Q:%d]  --- ", ng.Name, ng.First, ng.Last, todo, queued, len(processor.Batch.TodoQ))
			mux.Unlock()
			// Import articles for the selected group
			switch *importOverview {
			case false:
				/*
					waitLock:
						for {
							if len(DLParChan) < DownloadMaxPar {
								break waitLock
							}
							time.Sleep(time.Millisecond)
						}
						ScanParChan <- struct{}{} // aquire slot
						DLParChan <- struct{}{}   // aquire slot
						<-DLParChan               // free again
						if db.IsDBshutdown() {
							log.Printf("[FETCHER]: Database shutdown detected, stopping processing")
							return
						}
						// fire up the memory killer
						go func(DLParChan chan struct{}) {
							defer func() {
								<-ScanParChan // free slot when done
							}()
				*/
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
					err = proc.DownloadArticlesFromDate(ng.Name, startDate, *ignoreInitialTinyGroups, DLParChan, progressDB)
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
						err = proc.DownloadArticlesFromDate(ng.Name, startDate, *ignoreInitialTinyGroups, DLParChan, progressDB)

						if err != nil {
							errChan <- err
							log.Printf("[FETCHER]: DownloadArticlesFromDate6 failed: %v", err)
							continue
						}
					} else {
						// Incremental download: continue from where we left off
						log.Printf("[FETCHER]: Incremental download for newsgroup: '%s' (has %d existing articles)", ng.Name, articleCount)
						//time.Sleep(3 * time.Second) // debug sleep
						err = proc.DownloadArticles(ng.Name, *ignoreInitialTinyGroups, DLParChan, progressDB, ng.First, ng.Last)
						if err != nil {
							log.Printf("[FETCHER]: DownloadArticles7 failed: %v", err)
							errChan <- err
							continue
						}
					}
				} else {
					log.Printf("[FETCHER]: Downloading articles for newsgroup: '%s' (%d - %d) (no expiry limit)", ng.Name, ng.First, ng.Last)
					//time.Sleep(3 * time.Second) // debug sleep
					err = proc.DownloadArticles(ng.Name, *ignoreInitialTinyGroups, DLParChan, progressDB, ng.First, ng.Last)
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
		} // end for processor.Batch.TodoQ
	}()

	// Wait for either shutdown signal or server error
	select {
	case <-sigChan:
		log.Printf("[FETCHER]: Received shutdown signal, initiating graceful shutdown...")
	case err := <-fetchDoneChan:
		log.Printf("[FETCHER]: DONE! err='%v'", err)
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

	mux.Lock()
	log.Printf("[FETCHER]: Total downloaded: %d articles (newsgroups: %d)", downloaded, queued)
	mux.Unlock()

	log.Printf("[FETCHER]: Graceful shutdown completed. Exiting here.")
}

func ConnectionTest(host *string, port *int, username *string, password *string, ssl *bool, timeout *int, fetchNewsgroup string, testMsg *string) error {
	// Create a test provider config
	testProvider := &config.Provider{
		Name:     "test",
		Host:     *host,
		Port:     *port,
		SSL:      *ssl,
		Username: *username,
		Password: *password,
		MaxConns: 3,
		Enabled:  true,
		Priority: 1,
	}

	// Create Test client configuration
	backenConfig := &nntp.BackendConfig{
		Host:     *host,
		Port:     *port,
		SSL:      *ssl,
		Username: *username,
		Password: *password,
		MaxConns: 3, // Default max connections
		//ConnectTimeout: time.Duration(*timeout) * time.Second,
		//ReadTimeout:    60 * time.Second,
		//WriteTimeout:   30 * time.Second,
		Provider: testProvider, // Set the Provider field
	}

	fmt.Printf("Testing NNTP connection to %s:%d (SSL: %v)\n", *host, *port, *ssl)
	if *username != "" {
		fmt.Printf("Authentication: %s\n", *username)
	} else {
		fmt.Println("Authentication: None")
	}

	// Test 1: Basic connection only use this in a test!
	// Proper way is #2 to use the connection pool below!
	fmt.Println("\n=== Test 1: Test Basic Connection without backend Counter! ===")
	client := nntp.NewConn(backenConfig)
	start := time.Now()
	err := client.Connect()
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	fmt.Printf("✓ Connection successful (took %v)\n", time.Since(start))
	client.CloseFromPoolOnly() // only use this in a test!

	// Test 2: Connection pool
	fmt.Println("\n=== Test 2: Connection Pool ===")
	pool := nntp.NewPool(backenConfig)
	defer pool.ClosePool()

	pool.StartCleanupWorker(5 * time.Second)

	poolClient, err := pool.Get()
	if err != nil {
		log.Fatalf("Failed to get connection from pool: %v", err)
	}

	fmt.Printf("✓ Pool connection successful\n")

	stats := pool.Stats()
	fmt.Printf("Pool Stats: Max=%d, Active=%d, Idle=%d, Created=%d\n",
		stats.MaxConnections, stats.ActiveConnections, stats.IdleConnections, stats.TotalCreated)
	poolClient.Pool.Put(poolClient) // Return connection to pool

	// Test 3: List groups (first 10)
	fmt.Println("\n=== Test 3: List Groups ===")
	poolClient, err = pool.Get() // Get a connection from the pool
	if err != nil {
		log.Fatalf("Failed to get connection from pool: %v", err)
	}
	var groups []nntp.GroupInfo
	func() {
		defer poolClient.Pool.Put(poolClient) // Ensure connection is always returned
		var err error
		groups, err = poolClient.ListGroups()
		if err != nil {
			fmt.Printf("⚠ Failed to list groups: %v\n", err)
		} else {
			fmt.Printf("✓ Retrieved %d groups\n", len(groups))

			// Show first 10 groups
			limit := 10
			if len(groups) < limit {
				limit = len(groups)
			}

			fmt.Println("First groups:")
			for i := 0; i < limit; i++ {
				group := groups[i]
				fmt.Printf("  %s: %d articles (%d-%d) posting=%v\n",
					group.Name, group.Count, group.First, group.Last, group.PostingOK)
			}
		}
	}()

	// Test 4: Select a specific group (or try first available)
	poolClient, err = pool.Get() // Get a connection from the pool
	if err != nil {
		log.Fatalf("Failed to get connection from pool: %v", err)
	}
	func() {
		defer poolClient.Pool.Put(poolClient) // Ensure connection is always returned
		if fetchNewsgroup != "" {
			fmt.Printf("\n=== Test 4: Select Group '%s' ===\n", fetchNewsgroup)
			groupInfo, _, err := poolClient.SelectGroup(fetchNewsgroup)
			if err != nil {
				fmt.Printf("⚠ Failed to select group: %v\n", err)
			} else {
				fmt.Printf("✓ Group selected: %s\n", groupInfo.Name)
				fmt.Printf("  Articles: %d (%d-%d)\n", groupInfo.Count, groupInfo.First, groupInfo.Last)
				fmt.Printf("  Posting: %v\n", groupInfo.PostingOK)
			}
		} else if len(groups) > 0 {
			// Try to select the first few groups, skipping problematic ones
			fmt.Println("\n=== Test 4: Auto-select Available Group ===")
			for i, group := range groups {
				if i >= 5 { // Try max 5 groups
					break
				}

				// Skip known problematic groups
				if group.Name == "control" || group.Name == "junk" {
					fmt.Printf("Skipping problematic group: %s\n", group.Name)
					continue
				}

				fmt.Printf("Trying to select group: %s\n", group.Name)
				groupInfo, _, err := poolClient.SelectGroup(group.Name)
				if err != nil {
					fmt.Printf("⚠ Failed to select group %s: %v (trying next)\n", group.Name, err)
					continue
				}

				fmt.Printf("✓ Successfully selected group: %s\n", groupInfo.Name)
				fmt.Printf("  Articles: %d (%d-%d)\n", groupInfo.Count, groupInfo.First, groupInfo.Last)
				fmt.Printf("  Posting: %v\n", groupInfo.PostingOK)
				break
			}
		}
	}()

	// Test 5: Test specific message ID
	if *testMsg != "" {
		poolClient, err = pool.Get() // Get a connection from the pool
		if err != nil {
			log.Fatalf("Test 5 Failed to get connection from pool: %v", err)
		}
		func() {
			defer poolClient.Pool.Put(poolClient) // Ensure connection is always returned
			fmt.Printf("\n=== Test 5: Test Message ID '%s' ===\n", *testMsg)

			// Test STAT command
			exists, err := poolClient.StatArticle(*testMsg)
			if err != nil {
				fmt.Printf("⚠ STAT failed: %v\n", err)
			} else {
				fmt.Printf("✓ STAT result: exists=%v\n", exists)
			}

			if exists {
				// Test HEAD command
				article, err := poolClient.GetHead(*testMsg)
				if err != nil {
					fmt.Printf("⚠ HEAD failed: %v\n", err)
				} else {
					fmt.Printf("✓ HEAD successful, %d headers\n", len(article.Headers))

					// Show some key headers
					if subject := article.Headers["subject"]; len(subject) > 0 {
						fmt.Printf("  Subject: %s\n", subject[0])
					}
					if from := article.Headers["from"]; len(from) > 0 {
						fmt.Printf("  From: %s\n", from[0])
					}
					if date := article.Headers["date"]; len(date) > 0 {
						fmt.Printf("  Date: %s\n", date[0])
					}
				}
			}
		}()
	}

	// Test 6: XOVER (Overview data)
	if fetchNewsgroup != "" {
		poolClient, err = pool.Get() // Get a connection from the pool
		if err != nil {
			log.Fatalf("Test 6 Failed to get connection from pool: %v", err)
		}
		func() {
			defer poolClient.Pool.Put(poolClient) // Ensure connection is always returned
			fmt.Printf("\n=== Test 6: XOVER for group '%s' ===\n", fetchNewsgroup)
			groupInfo, _, err := poolClient.SelectGroup(fetchNewsgroup)
			if err != nil {
				fmt.Printf("⚠ Failed to select group for XOVER: %v\n", err)
			} else {
				// Get overview data for first 10 articles
				start := groupInfo.First
				end := start + 9
				if end > groupInfo.Last {
					end = groupInfo.Last
				}
				enforceLimit := false
				fmt.Printf("Getting XOVER data for articles %d-%d...\n", start, end)
				overviews, err := poolClient.XOver(fetchNewsgroup, start, end, enforceLimit)
				if err != nil {
					fmt.Printf("⚠ XOVER failed: %v\n", err)
				} else {
					fmt.Printf("✓ Retrieved %d overview records\n", len(overviews))
					for i, ov := range overviews {
						if i >= 3 { // Show only first 3
							break
						}
						fmt.Printf("  Article %d: %s (from: %s, %d bytes)\n",
							ov.ArticleNum, ov.Subject[:min(50, len(ov.Subject))],
							ov.From[:min(30, len(ov.From))], ov.Bytes)
					}
				}
			}
		}()
	}

	// Test 7: XHDR (Header field extraction)
	if fetchNewsgroup != "" {
		poolClient, err = pool.Get() // Get a connection from the pool
		if err != nil {
			log.Fatalf("Test 7 Failed to get connection from pool: %v", err)
		}
		func() {
			defer poolClient.Pool.Put(poolClient) // Ensure connection is always returned
			fmt.Printf("\n=== Test 7: XHDR for group '%s' ===\n", fetchNewsgroup)
			groupInfo, _, err := poolClient.SelectGroup(fetchNewsgroup)
			if err != nil {
				fmt.Printf("⚠ Failed to select group for XHDR: %v\n", err)
			} else {
				// Get subject headers for first 5 articles
				start := groupInfo.First
				end := start + 4
				if end > groupInfo.Last {
					end = groupInfo.Last
				}

				fmt.Printf("Getting XHDR Subject for articles %d-%d...\n", start, end)
				headers, err := poolClient.XHdr(fetchNewsgroup, "Subject", start, end)
				if err != nil {
					fmt.Printf("⚠ XHDR failed: %v\n", err)
				} else {
					fmt.Printf("✓ Retrieved %d subject headers\n", len(headers))
					for i, hdr := range headers {
						if i >= 3 { // Show only first 3
							break
						}
						fmt.Printf("  Article %d: %s\n", hdr.ArticleNum,
							hdr.Value[:min(60, len(hdr.Value))])
					}
				}
			}
		}()
	}

	// Test 8: LISTGROUP (Article numbers)
	if fetchNewsgroup != "" && !strings.Contains(fetchNewsgroup, "*") && !strings.Contains(fetchNewsgroup, "$") {
		poolClient, err = pool.Get() // Get a connection from the pool
		if err != nil {
			log.Fatalf("Test 8 Failed to get connection from pool: %v", err)
		}
		func() {
			defer poolClient.Pool.Put(poolClient) // Ensure connection is always returned
			fmt.Printf("\n=== Test 8: LISTGROUP for '%s' ===\n", fetchNewsgroup)
			// Get first 20 article numbers
			fmt.Printf("Getting article numbers (limited)...\n")
			articleNums, err := poolClient.ListGroup(fetchNewsgroup, 0, 0) // Get all (limited by server)
			if err != nil {
				fmt.Printf("⚠ LISTGROUP failed: %v\n", err)
			} else {
				fmt.Printf("✓ Retrieved %d article numbers\n", len(articleNums))
				if len(articleNums) > 0 {
					fmt.Printf("  First articles: ")
					for i, num := range articleNums {
						if i >= 10 { // Show first 10
							fmt.Printf("...")
							break
						}
						fmt.Printf("%d ", num)
					}
					fmt.Printf("\n  Last articles: ")
					start := len(articleNums) - 5
					if start < 0 {
						start = 0
					}
					for i := start; i < len(articleNums); i++ {
						fmt.Printf("%d ", articleNums[i])
					}
					fmt.Println()
				}
			}
		}()
	}
	return err
} // end func ConnectionTest

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
