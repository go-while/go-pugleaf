package processor

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

var LOOPS_PER_GROUPS = 1

type BatchQueue struct {
	Mutex       sync.RWMutex
	GetQ        chan *BatchItem        // Channel to get articles for processing
	Check       chan *string           // Channel to check newsgroups
	TodoQ       chan *nntp.GroupInfo   // Channel to do newsgroups
	GroupQueues map[string]*GroupBatch // Per-newsgroup queues
}

type GroupBatch struct {
	ReturnQ chan *BatchItem // Per-group channel to hold batch items to return
}

type BatchItem struct {
	MessageID  *string
	ArticleNum int64
	GroupName  *string
	Article    *models.Article
	Error      error
	ReturnQ    chan *BatchItem // Channel to return processed items
}

var ErrUpToDate = fmt.Errorf("up2date")
var errIsDuplicateError = fmt.Errorf("isDup")

// GetOrCreateGroupBatch returns the GroupBatch for a newsgroup, creating it if necessary
// This now spawns a dedicated worker goroutine for efficient per-group processing
func (bq *BatchQueue) GetOrCreateGroupBatch(newsgroup string) *GroupBatch {
	bq.Mutex.Lock()
	defer bq.Mutex.Unlock()

	if bq.GroupQueues == nil {
		bq.GroupQueues = make(map[string]*GroupBatch)
	}

	groupBatch, exists := bq.GroupQueues[newsgroup]
	if !exists {
		groupBatch = &GroupBatch{
			ReturnQ: make(chan *BatchItem, MaxBatchSize),
		}
		bq.GroupQueues[newsgroup] = groupBatch
		//log.Printf("Created new GroupBatch: %s", newsgroup)
	}

	return groupBatch
}

// DownloadArticles fetches full articles and stores them in the articles DB.
func (proc *Processor) DownloadArticles(newsgroup string, ignoreInitialTinyGroups int64, DLParChan chan struct{}, progressDB *database.ProgressDB, start int64, end int64) error {
	//log.Printf("DEBUG-DownloadArticles: ng='%s' called with start=%d end=%d", newsgroup, start, end)
	DLParChan <- struct{}{} // aquire lock
	defer func() {
		<-DLParChan // free slot
	}()

	// Get or create the group-specific batch channels
	groupBatch := Batch.GetOrCreateGroupBatch(newsgroup)
	// Note: Don't defer close here - let the main loop manage group batch lifecycle

	// Note: We don't shut down the database here as it's shared with the main application
	// progressDB is now passed as parameter to avoid opening/closing for each group

	if proc.Pool == nil {
		return fmt.Errorf("DownloadArticles: NNTP pool is nil for group '%s'", newsgroup)
	}
	//log.Printf("DownloadArticles: ng: '%s' @ (%s)", newsgroup, providerName)
	groupDBs, err := proc.DB.GetGroupDBs(newsgroup)
	if err != nil {
		log.Printf("Failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
		if groupDBs != nil {
			if err := proc.DB.ForceCloseGroupDBs(groupDBs); err != nil {
				log.Printf("error in DownloadArticles ForceCloseGroupDBs err='%v'", err)
			}
			//groupDBs.Return(proc.DB) // Return connection even on error
		}
		log.Printf("DownloadArticles: Failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
		return fmt.Errorf("error in DownloadArticles: failed to get group DBs err='%v'", err)
	}
	defer proc.DB.ForceCloseGroupDBs(groupDBs)
	if proc.DB.IsDBshutdown() {
		return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
	}
	//remaining := groupInfo.Last - end
	//log.Printf("DownloadArticles: Fetching XHDR for %s from %d to %d (last known: %d, remaining: %d)", newsgroup, start, end, groupInfo.Last, remaining)
	var mux sync.Mutex
	var lastGoodEnd int64 = start
	toFetch := end - start + 1 // +1 because ranges are inclusive (start=1, end=3 means articles 1,2,3)
	xhdrChan := make(chan *nntp.HeaderLine, MaxBatchSize)
	errChan := make(chan error, 1)
	//log.Printf("Launch XHdrStreamed: '%s' toFetch=%d start=%d end=%d", newsgroup, toFetch, start, end)
	go func(mux *sync.Mutex) {
		aerr := proc.Pool.XHdrStreamed(newsgroup, "message-id", start, end, xhdrChan)
		if aerr != nil {
			log.Printf("Failed to fetch message IDs for group '%s': err='%v' toFetch=%d", newsgroup, aerr, toFetch)
			errChan <- aerr
			return
		}
		errChan <- nil
	}(&mux)
	if proc.DB.IsDBshutdown() {
		return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
	}
	//log.Printf("DownloadArticles: XHDR is fetching %d msgIds ng: '%s' (%d to %d)", len(messageIDs), newsgroup, start, end)
	releaseChan := make(chan struct{}, 1)
	notifyChan := make(chan int64, 1)
	go func() {
		// launch to background and feed queue
		//log.Printf("DownloadArticles: Fetching %d articles for group '%s' using %d goroutines", toFetch, newsgroup, proc.Pool.Backend.MaxConns)
		var exists, queued int64
		for hdr := range xhdrChan {
			/*
				if !CheckMessageIdFormat(hdr.Value) {
					log.Printf("[FETCHER]: Invalid message ID format: '%s'", hdr.Value)
					groupBatch.ReturnQ <- &BatchItem{Error: errIsDuplicateError}
					continue
				}
			*/
			//log.Printf("DownloadArticles: Checking if article '%s' exists in group '%s'", msgID.Value, newsgroup)
			if groupDBs.ExistsMsgIdInArticlesDB(hdr.Value) {
				exists++
				groupBatch.ReturnQ <- &BatchItem{Error: errIsDuplicateError}
				continue
			}
			msgIdItem := history.MsgIdCache.GetORCreate(hdr.Value)
			msgIdItem.Mux.Lock()
			msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
			msgIdItem.Response = history.CaseLock
			msgIdItem.Mux.Unlock()
			item := &BatchItem{
				MessageID: &msgIdItem.MessageId, // Use pointer to avoid copying
				GroupName: proc.DB.Batch.GetNewsgroupPointer(newsgroup),
			}
			item.ReturnQ = groupBatch.ReturnQ
			Batch.GetQ <- item // send to fetcher/main.go: for item := range processor.Batch.Queue
			queued++
			//log.Printf("DownloadArticles: Queued article %d (%s) for group '%s'", hdr.ArticleNum, hdr.Value, *item.GroupName)
			hdr.Value = ""
			hdr.ArticleNum = 0
		} // end for xhdrChan
		//log.Printf("DownloadArticles: XHdr closed, finished feeding batch queue %d articles for group '%s' (existing: %d) total=%d", queued, newsgroup, exists, queued+exists)
		if queued == 0 {
			releaseChan <- struct{}{}
		} else {
			notifyChan <- queued
		}
	}()
	var dups, lastDups, gots, lastGots, notf, lastNotf, errs, lastErrs int64
	aliveCheck := 5 * time.Second
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	startTime := time.Now()
	nextCheck := startTime.Add(aliveCheck)
	deathCounter := 0 // Counter to track if we are stuck
	bulkmode := true
	var gotQueued int64 = -1
	// Start processing loop
forProcessing:
	for {
		select {
		case gotQueued = <-notifyChan:
		case <-releaseChan:
			//log.Printf("DownloadArticles: releaseChan triggered '%s'", newsgroup)
			break forProcessing
		case <-ticker.C:
			// Periodically check if we are done or stuck
			if gotQueued > 0 && gots+errs+notf == gotQueued {
				//log.Printf("OK-DA1: '%s' (dups: %d, gots: %d, notf: %d, errs: %d, gotQueued: %d)", newsgroup, dups, gots, notf, errs, gotQueued)
				break forProcessing // Exit processing loop if all items are processed
			}
			if dups > lastDups || gots > lastGots || notf > lastNotf || errs > lastErrs {
				nextCheck = time.Now().Add(aliveCheck) // Reset last check time
				lastDups = dups
				lastGots = gots
				lastNotf = notf
				lastErrs = errs
				deathCounter = 0 // Reset death counter on progress
			}
			if nextCheck.Before(time.Now()) {
				// If we haven't made progress in N seconds, log a warning
				log.Printf("DownloadArticles: '%s' Stuck? %d articles processed (%d dups, %d gots, %d notf, %d errs, gotQueued: %d) (since Start=%v)", newsgroup, dups+gots+notf+errs, dups, gots, notf, errs, gotQueued, time.Since(startTime))
				nextCheck = time.Now().Add(aliveCheck) // Reset last check time
				deathCounter++
			}
			if deathCounter > 3 { // If we are stuck for too long
				log.Printf("DownloadArticles: '%s' Timeout... stopping import deathCounter=%d", newsgroup, deathCounter)
				return fmt.Errorf("DownloadArticles: '%s' Timeout... %d articles processed (%d dups, %d got, %d errs)", newsgroup, dups+gots+notf+errs, dups, gots, errs)
			}

		case item := <-groupBatch.ReturnQ:
			//log.Printf("DEBUG-RETURN: received item: Error=%v, Article=%v", item != nil && item.Error != nil, item != nil && item.Article != nil)
			if item == nil || item.Error != nil || item.Article == nil {
				if item != nil {
					switch item.Error {
					case errIsDuplicateError:
						dups++
					case nntp.ErrArticleNotFound:
						notf++
					case nntp.ErrArticleRemoved:
						notf++
					default:
						log.Printf("DownloadArticles: '%s' Error fetching article %s: %v .. continue", newsgroup, *item.MessageID, item.Error)
						errs++
					}
				} else {
					log.Printf("ERROR in DownloadArticles: received nil item (errs %d)", errs)
					errs++
				}
			} else if item.Error == nil && item.Article != nil {
				if proc.DB.IsDBshutdown() {
					return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
				}
				//log.Printf("DownloadArticles --> proc.processArticle '%s' in group '%s'", *item.MessageID, newsgroup)
				response, err := proc.processArticle(item.Article, newsgroup, bulkmode)
				if err != nil {
					log.Printf("DownloadArticles: '%s' Failed to process article (%s): response=%d err='%v'", newsgroup, *item.MessageID, response, err)
					errs++
				} else {
					gots++
				}
			} else {
				log.Printf("LOST CASE IN DownloadArticles item='%#v'", item)
				errs++
			}
			item.Article = nil
			item.MessageID = nil
			item.GroupName = nil
			item.Error = nil
			item.ReturnQ = nil
			if gotQueued > 0 && gots+errs+notf == gotQueued {
				//log.Printf("OK-DA2: '%s' (dups: %d, gots: %d, notf: %d, errs: %d, gotQueued: %d)", newsgroup, dups, gots, notf, errs, gotQueued)
				break forProcessing // Exit processing loop if all items are processed
			}
		}
	} // end for processing routine (counts only)

	if proc.DB.IsDBshutdown() {
		return fmt.Errorf("DownloadArticles: Database shutdown detected for group '%s'", newsgroup)
	}
	xerr := <-errChan
	if xerr != nil {
		end = lastGoodEnd
	}
	if gotQueued > 0 {
		err = progressDB.UpdateProgress(proc.Pool.Backend.Provider.Name, newsgroup, end)
		if err != nil {
			log.Printf("Failed to update progress for provider '%s' group '%s': %v", proc.Pool.Backend.Provider.Name, newsgroup, err)
		}
	}
	log.Printf("DownloadArticles: '%s' processed %d articles [gotQueued=%d] (dups: %d, gots: %d, errs: %d, adds: %d) in %v end=%d", newsgroup, gots+errs+dups, gotQueued, dups, gots, errs, GroupCounter.GetReset(newsgroup), time.Since(startTime), end)
	// do another one if we haven't run enough times
	runtime.GC()

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
			log.Printf("Target date %s is before first article %d (date: %s), returning first article. ng: %s",
				targetDate.Format("2006-01-02"), first, firstArticleDate.Format("2006-01-02"), groupName)
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

		log.Printf("Scanning: %s - Article %d has date %s", groupName, mid, articleDate.Format("2006-01-02"))

		if articleDate.Before(targetDate) {
			first = mid
		} else {
			last = mid
		}
	}

	log.Printf("Found start article: %d, ng: %s", last, groupName)
	return last, nil
}

// DownloadArticlesFromDate fetches articles starting from a specific date
// Uses special progress tracking: sets progress to startArticle-1, or -1 if starting from article 1
// This prevents DownloadArticles from using "no progress detected" logic for existing groups
func (proc *Processor) DownloadArticlesFromDate(groupName string, startDate time.Time, ignoreInitialTinyGroups int64, DLParChan chan struct{}, progressDB *database.ProgressDB) error {
	//log.Printf("DownloadArticlesFromDate: Starting download from date %s for group '%s'", startDate.Format("2006-01-02"), groupName)

	// Find the starting article number based on date
	startArticle, err := proc.FindStartArticleByDate(groupName, startDate)
	if err != nil {
		return fmt.Errorf("failed to find start article for date %s: %w", startDate.Format("2006-01-02"), err)
	}

	// Open progress DB and temporarily override the last article position
	// so DownloadArticles will start from our desired article number
	// progressDB is now passed as parameter to avoid opening/closing for each group

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
		return fmt.Errorf("error in DownloadArticlesFromDate: Could not get current progress for %s/%s: %v", providerName, groupName, err)

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

	//log.Printf("DownloadArticlesFromDate: Set progress to %d (date rescan), will start downloading from article %d", tempProgress, startArticle)

	// Get group info to calculate proper download range
	groupInfo, err := proc.Pool.SelectGroup(groupName)
	if err != nil {
		return fmt.Errorf("failed to select group for date download: %w", err)
	}

	// Calculate download range: start from found article, end at current group last or startArticle + MaxBatch
	downloadStart := startArticle
	downloadEnd := startArticle + MaxBatchSize - 1
	if downloadEnd > groupInfo.Last {
		downloadEnd = groupInfo.Last
	}

	//log.Printf("DownloadArticlesFromDate: Downloading range %d-%d for group '%s' (group last: %d)",	downloadStart, downloadEnd, groupName, groupInfo.Last)

	// Now use the high-performance DownloadArticles function with proper article ranges
	err = proc.DownloadArticles(groupName, ignoreInitialTinyGroups, DLParChan, progressDB, downloadStart, downloadEnd)

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

	//log.Printf("DownloadArticlesFromDate: Successfully completed download from date %s for group '%s'",	startDate.Format("2006-01-02"), groupName)
	return err // Return the result from DownloadArticles (including ErrUpToDate)
}
