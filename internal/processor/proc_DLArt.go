package processor

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

type BatchQueue struct {
	Mutex sync.RWMutex
	Queue chan *batchItem // Channel to hold batch items to download
}

type batchItem struct {
	MessageID  *string
	ArticleNum *int64
	GroupName  *string
	Article    *models.Article
	Error      error
	ReturnChan chan *batchItem
}

var ErrUpToDate = fmt.Errorf("up2date")

type selectResult struct {
	groupInfo *nntp.GroupInfo
	err       error
}

// DownloadArticles fetches full articles and stores them in the articles DB.
func (proc *Processor) DownloadArticles(newsgroup string, ignoreInitialTinyGroups int64, DLParChan chan struct{}) error {
	/*
		DLParChan <- struct{}{} // aquire lock
		defer func() {
			<-DLParChan // free slot
		}()
	*/
	// Note: We don't shut down the database here as it's shared with the main application
	progressDB, err := database.NewProgressDB("data/progress.db")
	if err != nil {
		log.Fatal(err)
	}
	defer progressDB.Close()

	if proc.Pool == nil {
		return fmt.Errorf("DownloadArticles: NNTP pool is nil for group '%s'", newsgroup)
	}

	// Add timeout for SelectGroup to prevent hanging
	resultChan := make(chan *selectResult, 1)
	go func() {
		groupInfo, err := proc.Pool.SelectGroup(newsgroup)
		resultChan <- &selectResult{groupInfo: groupInfo, err: err}
	}()

	// Wait for result with timeout
	var groupInfo *nntp.GroupInfo
	select {
	case result := <-resultChan:
		if result.err != nil {
			return fmt.Errorf("DownloadArticles: Failed to select group '%s': %v", newsgroup, result.err)
		}
		groupInfo = result.groupInfo
		//log.Printf("DEBUG: Successfully selected group '%s', groupInfo: %+v", groupName, groupInfo)
	case <-time.After(13 * time.Second):
		return fmt.Errorf("DownloadArticles: Timeout selecting group '%s' after 13 seconds", newsgroup)
	}
	// check if group has at least N articles or ignore fetching
	localDBnewsgroupInfo, err := proc.DB.GetActiveNewsgroupByName(newsgroup)
	if err != nil {
		log.Printf("DownloadArticles: Failed to get local newsgroup info for '%s': %v", newsgroup, err)
		return fmt.Errorf("DownloadArticles: Failed to get local newsgroup info for '%s': %v", newsgroup, err)
	}
	if localDBnewsgroupInfo.MessageCount == 0 && ignoreInitialTinyGroups > 0 && groupInfo.Count < ignoreInitialTinyGroups {
		log.Printf("DownloadArticles: Initial Fetch, Skipping group '%s' with only %d articles (ignore threshold: %d)", newsgroup, groupInfo.Count, ignoreInitialTinyGroups)
		return nil
	}

	// Get the provider name for progress tracking
	providerName := "unknown"
	if proc.Pool.Backend.Provider != nil {
		providerName = proc.Pool.Backend.Provider.Name
	}
	if providerName == "unknown" {
		return fmt.Errorf("errror in DownloadArticles: Provider name is unknown, cannot proceed with group '%s'", newsgroup)
	}
	log.Printf("DownloadArticles: ng: '%s' @ (%s)", newsgroup, providerName)
	run := 0
doWork:
	if proc.DB.IsDBshutdown() {
		return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
	}

	lastArticle, err := progressDB.GetLastArticle(providerName, newsgroup)
	if err != nil {
		log.Printf("DownloadArticles: Failed to get last article for group '%s' from provider '%s': %v", newsgroup, providerName, err)
		return fmt.Errorf("DownloadArticles: Failed to get last article for group '%s' from provider '%s': %v", newsgroup, providerName, err)
	}

	// Handle special progress values:
	// progress = 0: No previous progress (new provider/group)
	// progress = -1: User-requested date rescan (skip auto-detection)
	if lastArticle == 0 {
		checkGroupDBs, checkGroupErr := proc.DB.GetGroupDBs(newsgroup)
		if checkGroupErr != nil {
			return fmt.Errorf("DownloadArticles: Failed to get group DBs for '%s': %v", newsgroup, checkGroupErr)
		}

		lastArticleDate, checkDateErr := proc.DB.GetLastArticleDate(checkGroupDBs)
		checkGroupDBs.Return(proc.DB) // Return the database connection immediately

		if checkDateErr != nil {
			return fmt.Errorf("DownloadArticles: Failed to get last article date for '%s': %v", newsgroup, checkDateErr)
		}

		// If group has existing articles, use date-based download instead
		if lastArticleDate != nil {
			log.Printf("DownloadArticles: No progress for provider '%s' but group '%s' has existing articles, switching to date-based download from: %s",
				providerName, newsgroup, lastArticleDate.Format("2006-01-02"))
			return proc.DownloadArticlesFromDate(newsgroup, *lastArticleDate, 0, DLParChan) // Use 0 for ignore threshold since group already exists
		}
	} else if lastArticle == -1 {
		// User-requested date rescan - reset to start from beginning
		lastArticle = 0
		log.Printf("DownloadArticles: Date rescan mode for group '%s', starting from beginning", newsgroup)
	}
	start := lastArticle + 1           // Start from the first article in the remote group
	end := start + int64(MaxBatch) - 1 // End at the last article in the remote group
	if end > groupInfo.Last {
		end = groupInfo.Last
	}
	if start > end {
		log.Printf("DownloadArticles: No new data to import for newsgroup '%s' start=%d end=%d (remote: first=%d last=%d)", newsgroup, start, end, groupInfo.First, groupInfo.Last)
		return ErrUpToDate
	}
	toFetch := end - start
	if toFetch > nntp.MaxReadLinesXover {
		// Limit to N articles per batch fetch
		end = start + nntp.MaxReadLinesXover - 1
		toFetch = end - start
		log.Printf("DownloadArticles: Limiting fetch for %s to %d articles (start=%d, end=%d)", newsgroup, toFetch, start, end)
	}
	if toFetch <= 0 {
		log.Printf("DownloadArticles: No data to fetch for newsgroup '%s' (start=%d, end=%d)", newsgroup, start, end)
		return nil
	}

	if proc.DB.IsDBshutdown() {
		return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
	}
	log.Printf("DownloadArticles: Fetching XHDR for %s from %d to %d (last known: %d)", newsgroup, start, end, groupInfo.Last)
	messageIDs, err := proc.Pool.XHdr(newsgroup, "message-id", start, end)
	if err != nil || len(messageIDs) == 0 {
		log.Printf("Failed to fetch message IDs for group '%s': %v", newsgroup, err)
		return err
	}
	if proc.DB.IsDBshutdown() {
		return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
	}
	log.Printf("DownloadArticles: XHDR fetched %d msgIds ng: '%s' (%d to %d)", len(messageIDs), newsgroup, start, end)
	//log.Printf("proc.Pool.Backend=%#v", proc.Pool.Backend)
	//batchQueue := make(chan *batchItem, len(messageIDs))
	returnChan := make(chan *batchItem, len(messageIDs))
	log.Printf("DownloadArticles: Fetching %d articles for group '%s' using %d goroutines", len(messageIDs), newsgroup, proc.Pool.Backend.MaxConns)

	/*
		// launch goroutines to fetch articles in parallel
		runthis := proc.Pool.Backend.MaxConns
		if len(messageIDs) < runthis { // if we have less articles to fetch than max connections
			runthis = len(messageIDs) // Limit goroutines to number of articles
		}
		if runthis < 1 {
			return fmt.Errorf("no connections at backend provider??")
		}

		mutex := &sync.Mutex{} // Mutex to protect shared state
		downloaded := 0
		quit := 0

		log.Printf("DownloadArticles: Fetching %d articles for group '%s' using %d goroutines", len(messageIDs), newsgroup, runthis)
		for i := 1; i <= runthis; i++ {
			// fire up async goroutines to fetch articles
			go func(worker int, mutex *sync.Mutex) {
				//log.Printf("DownloadArticles: Worker %d group '%s' start", worker, groupName)
				defer func() {
					//log.Printf("DownloadArticles: Worker %d group '%s' quit", worker, groupName)
					mutex.Lock()
					quit++
					mutex.Unlock()
				}()
				for item := range batchQueue {
					//log.Printf("DownloadArticles: Worker %d processing group '%s' article (%s)", worker, *item.GroupName, *item.MessageID)
					art, err := proc.Pool.GetArticle(*item.MessageID)
					if err != nil {
						log.Printf("ERROR DownloadArticles: group '%s' proc.Pool.GetArticle %s: %v .. continue", newsgroup, *item.MessageID, err)
						item.Error = err   // Set error on item
						returnChan <- item // Send failed item back
						continue
					}
					item.Article = art // set pointer
					returnChan <- item // Send back the successfully downloaded article
					mutex.Lock()
					downloaded++
					mutex.Unlock()
					//log.Printf("DownloadArticles: Worker %d downloaded group '%s' article (%s)", worker, *item.GroupName, *item.MessageID)
				} // end for item
			}(i, mutex)
		} // end for runthis
	*/

	// for every undownloaded overview entry, create a batch item
	batchList := make([]*batchItem, 0, len(messageIDs)) // Slice to hold batch items
	//skipped := 0
	groupDBs, err := proc.DB.GetGroupDBs(newsgroup)
	if err != nil {
		log.Printf("Failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
		if groupDBs != nil {
			groupDBs.Return(proc.DB) // Return connection even on error
		}
		log.Printf("DownloadArticles: Failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
		return fmt.Errorf("error in DownloadArticles: failed to get group DBs err='%v'", err)
	}
	exists := 0
	defer groupDBs.Return(proc.DB)
	for _, msgID := range messageIDs {
		if groupDBs.ExistsMsgIdInArticlesDB(msgID.Value) {
			exists++
			returnChan <- nil
			continue
		}
		msgIdItem := history.MsgIdCache.GetORCreate(msgID.Value)
		msgIdItem.Mux.Lock()
		msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
		msgIdItem.Response = history.CaseLock
		msgIdItem.Mux.Unlock()
		/* TODO: check if article exists or not?!
		 	since we don't process crossposts with bulkmode ...
			checking here if an article already exists will actually  not file the article to this group
			so checking should happen earlier on network level in ihave/check/takethis
		if response, err := proc.History.Lookup(msgIdItem) response != history.CasePass {
			skipped++
			returnChan <- nil
			log.Printf("DownloadArticles: group '%s' Skipping article %s as it is already in history", groupName, msgID.Value)
			continue // Skip if already in history
		}
		*/
		item := &batchItem{
			MessageID:  &msgIdItem.MessageId, // Use pointer to avoid copying
			GroupName:  proc.DB.Batch.GetNewsgroupPointer(newsgroup),
			ReturnChan: returnChan,
		}
		Batch.Queue <- item                 // send to batch queue
		batchList = append(batchList, item) // also add to batchList for later processing
		//log.Printf("DownloadArticles: Queued article %d (%s) for group '%s'", num, msgID, groupName)
	} // end for undl

	gots, lastGots := 0, 0
	errs, lastErrs := 0, 0
	aliveCheck := 10 * time.Second
	ticker := time.NewTicker(100 * time.Millisecond)
	startTime := time.Now()
	nextCheck := startTime.Add(aliveCheck)
	deathCounter := 0 // Counter to track if we are stuck
	// Start processing loop
forProcessing:
	for {
		select {
		case <-ticker.C:
			//Batch.Mutex.Lock()
			//log.Printf("DownloadArticles: backfilling group '%s' (gots: %d, errs: %d) batchQueue=%d deathcounter=%d downloaded=%d quit=%d", groupName, gots, errs, len(batchQueue), deathCounter, downloaded, quit)
			//Batch.Mutex.Unlock()
			// Periodically check if we are done or stuck
			if gots+errs >= len(messageIDs) {
				log.Printf("DownloadArticles: group '%s' All %d (gots: %d, errs: %d) articles processed, closing batch channel", newsgroup, gots+errs, gots, errs)
				//close(batchQueue)   // Close channel to stop goroutines
				break forProcessing // Exit processing loop if all items are processed
			}
			if gots > lastGots || errs > lastErrs {
				nextCheck = time.Now().Add(aliveCheck) // Reset last check time
				lastGots = gots
				lastErrs = errs
				deathCounter = 0 // Reset death counter on progress
			}
			if nextCheck.Before(time.Now()) {
				// If we haven't made progress in N seconds, log a warning
				log.Printf("DownloadArticles: group '%s' Stuck? %d articles processed (%d got, %d errs) in last 10 seconds (since Start=%v)", newsgroup, gots+errs, gots, errs, time.Since(startTime))
				nextCheck = time.Now().Add(aliveCheck) // Reset last check time
				deathCounter++
			}
			if deathCounter > 6 { // If we are stuck for too long
				log.Printf("DownloadArticles: group '%s' Timeout... stopping import deathCounter=%d", newsgroup, deathCounter)
				close(Batch.Queue) // Close channel to stop goroutines
				return fmt.Errorf("DownloadArticles: group '%s' Timeout... %d articles processed (%d got, %d errs)", newsgroup, gots+errs, gots, errs)
			}

		case item := <-returnChan:
			if item == nil || item.Error != nil {
				if item != nil {
					log.Printf("DownloadArticles: group '%s' Error fetching article %s: %v .. continue", newsgroup, *item.MessageID, item.Error)
				}
				errs++
			} else {
				gots++
				//log.Printf("DownloadArticles: group '%s' fetched article (%s) gots=%d", groupName, *item.MessageID, gots)
			}
		}
	}

	// now loop over the batchList and insert articles
	for _, item := range batchList {
		if item == nil {
			continue // Skip nil items (not fetched)
		}
		if item.Article == nil {
			log.Printf("DownloadArticles: group '%s' Article '%s' was not fetched successfully, continue", newsgroup, *item.MessageID)
			continue
			//return fmt.Errorf("internal/importer:  group '%s' Article '%s' was not fetched successfully", newsgroup, *item.MessageID)
		}
		if proc.DB.IsDBshutdown() {
			return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
		}
		bulkmode := true
		response, err := proc.processArticle(item.Article, newsgroup, bulkmode)
		if err != nil {
			log.Printf("DownloadArticles:  group '%s' Failed to process article (%s): %v", newsgroup, *item.MessageID, err)
			continue // Skip this item on error
		}
		if response == history.CasePass {
			//log.Printf("DownloadArticles:  group '%s' imported article (%s)", groupName, *item.MessageID)
		}

		// Trigger GC periodically during large batch processing

	} // end for batchList

	//runtime.GC()

	if proc.DB.IsDBshutdown() {
		return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
	}

	err = progressDB.UpdateProgress(providerName, newsgroup, end)
	if err != nil {
		log.Printf("Failed to update progress for provider '%s' group '%s': %v", providerName, newsgroup, err)
	}
	log.Printf("DownloadArticles: progressDB group '%s' processed %d articles (gots: %d, errs: %d) in %v run=%d end=%d", newsgroup, gots+errs, gots, errs, time.Since(startTime), run, end)

	run++
	// do another one if we haven't run enough times
	if run < 4 {
		goto doWork
	}

	counters := GroupCounter.GetResetAll()
	for ngc, count := range counters {
		log.Printf("Downloaded ng:%s articles: %d", ngc, count)
	}
	if proc.DB.IsDBshutdown() {
		return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
	}
	return nil
} // end func DownloadArticles

// FindStartArticleByDate finds the first article number on or after the given date
// using a simple binary search approach with XOVER data
func (proc *Processor) FindStartArticleByDate(groupName string, targetDate time.Time) (int64, error) {
	// Get group info
	groupInfo, err := proc.Pool.SelectGroup(groupName)
	if err != nil {
		return 0, fmt.Errorf("failed to select group: %w", err)
	}

	first := groupInfo.First
	last := groupInfo.Last

	log.Printf("Finding start article for date %s in group %s (range %d-%d)",
		targetDate.Format("2006-01-02"), groupName, first, last)

	// Check if target date is before the first article
	enforceLimit := true
	firstOverviews, err := proc.Pool.XOver(groupName, first, first, enforceLimit)
	if err == nil && len(firstOverviews) > 0 {
		firstArticleDate := ParseNNTPDate(firstOverviews[0].Date)
		if !firstArticleDate.IsZero() && targetDate.Before(firstArticleDate) {
			log.Printf("Target date %s is before first article %d (date: %s), returning first article",
				targetDate.Format("2006-01-02"), first, firstArticleDate.Format("2006-01-02"))
			return first, nil
		}
	}

	// Binary search using 50% approach
	for last-first > 1 {
		mid := first + (last-first)/2

		// Get XOVER for this article
		overviews, err := proc.Pool.XOver(groupName, mid, mid, enforceLimit)
		if err != nil || len(overviews) == 0 {
			// Article doesn't exist, try moving up
			first = mid
			continue
		}
		if proc.DB.IsDBshutdown() {
			return 0, fmt.Errorf("FindStartArticleByDate: Database shutdown detected for group '%s'", groupName)
		}
		articleDate := ParseNNTPDate(overviews[0].Date)
		if articleDate.IsZero() {
			first = mid
			continue
		}

		log.Printf("Article %d has date %s", mid, articleDate.Format("2006-01-02"))

		if articleDate.Before(targetDate) {
			first = mid
		} else {
			last = mid
		}
	}

	log.Printf("Found start article: %d", last)
	return last, nil
}

// DownloadArticlesFromDate fetches articles starting from a specific date
// Uses special progress tracking: sets progress to startArticle-1, or -1 if starting from article 1
// This prevents DownloadArticles from using "no progress detected" logic for existing groups
func (proc *Processor) DownloadArticlesFromDate(groupName string, startDate time.Time, ignoreInitialTinyGroups int64, DLParChan chan struct{}) error {
	log.Printf("DownloadArticlesFromDate: Starting download from date %s for group '%s'",
		startDate.Format("2006-01-02"), groupName)

	// Find the starting article number based on date
	startArticle, err := proc.FindStartArticleByDate(groupName, startDate)
	if err != nil {
		return fmt.Errorf("failed to find start article for date %s: %w", startDate.Format("2006-01-02"), err)
	}

	// Open progress DB and temporarily override the last article position
	// so DownloadArticles will start from our desired article number
	progressDB, err := database.NewProgressDB("data/progress.db")
	if err != nil {
		return fmt.Errorf("failed to open progress DB: %w", err)
	}
	defer progressDB.Close()

	// Get the provider name for progress tracking
	providerName := "unknown"
	if proc.Pool.Backend.Provider != nil {
		providerName = proc.Pool.Backend.Provider.Name
	}
	if providerName == "unknown" {
		return fmt.Errorf("provider name is unknown, cannot proceed with group '%s'", groupName)
	}

	// Store the current progress to restore later if needed
	currentProgress, err := progressDB.GetLastArticle(providerName, groupName)
	if err != nil {
		log.Printf("Warning: Could not get current progress for %s/%s: %v", providerName, groupName, err)
		currentProgress = 0
	}

	// Set progress to startArticle-1 with special marker for date rescan
	// If startArticle is 1, we need to use a special value to avoid confusion with "no progress"
	tempProgress := startArticle - 1
	if tempProgress == 0 {
		// Use -1 to indicate user-requested date rescan starting from article 1
		tempProgress = -1
	}
	err = progressDB.UpdateProgress(providerName, groupName, tempProgress)
	if err != nil {
		return fmt.Errorf("failed to set temporary progress: %w", err)
	}

	log.Printf("DownloadArticlesFromDate: Set progress to %d (date rescan), will start downloading from article %d", tempProgress, startArticle)

	// Now use the high-performance DownloadArticles function
	err = proc.DownloadArticles(groupName, ignoreInitialTinyGroups, DLParChan)

	// If there was an error and we haven't made progress, restore the original progress
	if err != nil && err != ErrUpToDate {
		// Check if we made any progress
		newProgress, progressErr := progressDB.GetLastArticle(providerName, groupName)
		if progressErr == nil && newProgress <= tempProgress {
			// No progress made, restore original
			if currentProgress > tempProgress {
				restoreErr := progressDB.UpdateProgress(providerName, groupName, currentProgress)
				if restoreErr != nil {
					log.Printf("Warning: Failed to restore original progress: %v", restoreErr)
				} else {
					log.Printf("Restored original progress to %d after failed date-based download", currentProgress)
				}
			}
		}
		return fmt.Errorf("DownloadArticlesFromDate failed: %w", err)
	}

	log.Printf("DownloadArticlesFromDate: Successfully completed download from date %s for group '%s'",
		startDate.Format("2006-01-02"), groupName)
	return err // Return the result from DownloadArticles (including ErrUpToDate)
}
