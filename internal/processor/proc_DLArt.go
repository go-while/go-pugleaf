package processor

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

var LOOPS_PER_GROUPS = 1

type BatchQueue struct {
	Mutex sync.RWMutex
	GetQ  chan *BatchItem      // Channel to get articles for processing
	Check chan *string         // Channel to check newsgroups
	TodoQ chan *nntp.GroupInfo // Channel to do newsgroups
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

// CommitWaitTimeout bounds how long DownloadArticles waits for the batch writer to commit the
// articles of a run before it records the progress (commit barrier, A-tools-5b). On a timeout
// the progress is held back and the next run fetches the range again (stored articles are
// skipped by the message-id lookup).
var CommitWaitTimeout = 5 * time.Minute

//var BatchItemDuplicateError = &BatchItem{Error: errIsDuplicateError}

// newDownloadRun returns the return queue of one DownloadArticles run. Every run gets its own
// queue (A-intake-11): results that arrive after a run returned (timeout, shutdown) can no longer
// land in a queue shared with the next run of the same group, and the queue is released with
// the run.
func newDownloadRun() *GroupBatch {
	return &GroupBatch{ReturnQ: make(chan *BatchItem, MaxBatchSize)}
}

// feedDownloadRun is the feeder of one DownloadArticles run. For every message-id received from
// xhdrChan, prepare builds the item: a duplicate (Error set) or an item that already carries its
// article goes straight to returnQ, any other item goes to getQ (the fetcher's download workers
// answer on item.ReturnQ). A nil item is skipped. Every successful send increments handed.
//
// When runDone is closed or a shutdown is requested, it stops queueing: prepare (which may use
// the group DB that the run closes after runDone) is not called again, an item prepared but not
// sent has its message-id cache entry released (resetLateMsgID), and stopped is closed.
// stopped is also closed when xhdrChan closes. In both cases it then keeps receiving and
// discarding from xhdrChan until the channel is closed, because XHdrStreamed blocks on a full
// channel and has no stop channel of its own.
//
// It returns the number of queued items (duplicates not counted) and whether every entry of
// xhdrChan was handed on (false after runDone or a shutdown).
func feedDownloadRun(xhdrChan <-chan nntp.HeaderLine, runDone <-chan struct{}, stopped chan<- struct{}, returnQ, getQ chan<- *BatchItem, prepare func(messageID string) *BatchItem, handed *atomic.Int64) (queued int64, complete bool) {
	defer func() {
		for range xhdrChan {
			// discard: the run is gone, only unblock the XHDR reader
		}
	}()
	defer close(stopped) // runs before the discard loop
	for {
		var hdr nntp.HeaderLine
		var ok bool
		select {
		case hdr, ok = <-xhdrChan:
		case <-runDone:
			return queued, false
		}
		if !ok {
			return queued, true
		}
		if common.WantShutdown() {
			log.Printf("DownloadArticlesFromDate: xhdrChan common.WantShutdown(): stopping")
			return queued, false
		}
		select {
		case <-runDone:
			return queued, false // never call prepare (group DB) after runDone
		default:
		}
		item := prepare(hdr.Value)
		if item == nil {
			continue
		}
		// Everything this loop still needs from the item is read before the send: once the
		// item is in a queue it belongs to the consumer, which overwrites its fields when
		// it is done (data race otherwise).
		isDup := item.Error == errIsDuplicateError
		target := getQ
		if item.Error != nil || item.Article != nil {
			target = returnQ
		}
		// a select with a ready send and a closed runDone picks at random: check runDone first,
		// so no download is queued for a run that is gone
		select {
		case <-runDone:
			resetLateMsgID(item.MessageID)
			return queued, false
		default:
		}
		select {
		case target <- item:
		case <-runDone:
			resetLateMsgID(item.MessageID)
			return queued, false
		}
		handed.Add(1)
		if !isDup {
			queued++
		}
	}
}

// drainLateResults receives and drops the results still owed to a DownloadArticles run that has
// returned (A-intake-11): fetcher workers answer on the run's ReturnQ after a timeout, and items
// may still be buffered there. The message-id cache entry of every dropped item is reset to
// CaseError with a 5 s TTL, as the processing loop does for failed items, so a later run can
// fetch the article again. It returns after outstanding items or when shutdownChan is closed.
func drainLateResults(returnQ <-chan *BatchItem, outstanding int64, shutdownChan <-chan struct{}) {
	for ; outstanding > 0; outstanding-- {
		var item *BatchItem
		select {
		case item = <-returnQ:
		case <-shutdownChan:
			return
		}
		if item == nil {
			continue
		}
		resetLateMsgID(item.MessageID)
		item.Article = nil
		item.MessageID = nil
		item.GroupName = nil
		item.Error = nil
		item.ReturnQ = nil
	}
}

// fetchedMsgIDHeader returns the Message-ID header of a fetched article's raw header
// block (models.Article.HeadersJSON, the header lines joined by "\n"), or "" when the
// block has none. It scans the block instead of splitting it: DownloadArticles calls it
// for every downloaded article.
func fetchedMsgIDHeader(headersJSON string) string {
	const field = "message-id:"
	for rest := headersJSON; rest != ""; {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		if len(line) >= len(field) && strings.EqualFold(line[:len(field)], field) {
			return strings.TrimSpace(line[len(field):])
		}
	}
	return ""
}

// resetLateMsgID sets the message-id cache entry of an item that a DownloadArticles run will not
// process to CaseError with a 5 s TTL, as the processing loop does for failed items, so the
// CaseLock taken by the feeder does not block a later run. A nil messageID is ignored.
func resetLateMsgID(messageID *string) {
	if messageID == nil {
		return
	}
	msgIdItem := history.MsgIdCache.GetORCreate(*messageID)
	if msgIdItem != nil && msgIdItem.MessageId == *messageID {
		msgIdItem.Mux.Lock()
		msgIdItem.CachedEntryExpires = time.Now().Add(5 * time.Second)
		msgIdItem.Response = history.CaseError
		msgIdItem.Mux.Unlock()
	}
}

// DownloadArticles fetches full articles and stores them in the articles DB.
func (proc *Processor) DownloadArticles(newsgroup string, DLParChan chan struct{}, progressDB *database.ProgressDB, start int64, end int64, shutdownChan <-chan struct{}) error {
	if common.WantShutdown() {
		return fmt.Errorf("DownloadArticles: common.WantShutdown() group '%s'", newsgroup)
	}
	//log.Printf("DEBUG-DownloadArticles: ng='%s' called with start=%d end=%d", newsgroup, start, end)
	DLParChan <- struct{}{} // aquire lock
	defer func() {
		<-DLParChan // free slot
	}()

	// per-run return queue (A-intake-11): never shared with another run of this group
	groupBatch := newDownloadRun()
	// commit barrier (A-tools-5b): drops of this group's batches from now on hold the progress back
	commitMark := proc.DB.Batch.CommitMark(newsgroup)

	// Note: We don't shut down the database here as it's shared with the main application
	// progressDB is now passed as parameter to avoid opening/closing for each group

	if proc.Pool == nil {
		return fmt.Errorf("DownloadArticles: NNTP pool is nil for group '%s'", newsgroup)
	}
	//log.Printf("DownloadArticles: ng: '%s' @ (%s)", newsgroup, providerName)
	groupDB, err := proc.DB.GetGroupDB(newsgroup)
	if err != nil {
		log.Printf("Failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
		if groupDB != nil {
			if err := proc.DB.ForceCloseGroupDB(groupDB); err != nil {
				log.Printf("error in DownloadArticles ForceCloseGroupDB err='%v'", err)
			}
			//groupDB.Return(proc.DB) // Return connection even on error
		}
		log.Printf("DownloadArticles: Failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
		return fmt.Errorf("error in DownloadArticles: failed to get group DBs err='%v'", err)
	}
	defer proc.DB.ForceCloseGroupDB(groupDB)
	// runDone is closed when this run returns, before the deferred ForceCloseGroupDB above (LIFO):
	// the feeder stops queueing and no longer touches groupDB, and the results still owed to this
	// run (handed - received) are drained in the background (A-intake-11).
	runDone := make(chan struct{})
	var feederStopped chan struct{} // closed by the feeder once it stops queueing; nil until it starts
	var handed atomic.Int64         // items given to Batch.GetQ or groupBatch.ReturnQ by the feeder
	var received int64              // items taken from groupBatch.ReturnQ by the processing loop
	defer func() {
		close(runDone)
		if feederStopped == nil {
			return
		}
		<-feederStopped // bounded by one in-flight lookup; afterwards handed is final
		if outstanding := handed.Load() - received; outstanding > 0 {
			go drainLateResults(groupBatch.ReturnQ, outstanding, shutdownChan)
		}
	}()
	if common.WantShutdown() {
		return fmt.Errorf("DownloadArticles: common.WantShutdown() group '%s'", newsgroup)
	}
	//remaining := groupInfo.Last - end
	//log.Printf("DownloadArticles: Fetching XHDR for %s from %d to %d (last known: %d, remaining: %d)", newsgroup, start, end, groupInfo.Last, remaining)
	//toFetch := end - start + 1 // +1 because ranges are inclusive (start=1, end=3 means articles 1,2,3)
	// resolve the main-DB ID once: needed to reuse crossposts stored in other groups
	reuseEnabled := ReuseCrossposts && proc.History.Enabled()
	var currentGroupID int64
	if reuseEnabled {
		if ng, err := proc.DB.MainDBGetNewsgroup(newsgroup); err != nil || ng == nil || ng.ID <= 0 {
			reuseEnabled = false // can not exclude the current group: download everything
		} else {
			currentGroupID = ng.ID
		}
	}
	var reused atomic.Int64 // written by the xhdr goroutine, read at the end
	xhdrChan := make(chan nntp.HeaderLine, MaxBatchSize)
	errChan := make(chan error, 1)
	//log.Printf("Launch XHdrStreamed: '%s' toFetch=%d start=%d end=%d", newsgroup, toFetch, start, end)
	go func() {
		// returns once the stream ended: a cut-off stream is an error, not a complete range
		errChan <- proc.Pool.XHdrStreamedWait(newsgroup, "message-id", start, end, xhdrChan, shutdownChan)
	}()

	//log.Printf("DownloadArticles: XHDR is fetching %d msgIds ng: '%s' (%d to %d)", len(messageIDs), newsgroup, start, end)
	releaseChan := make(chan struct{}, 1)
	notifyChan := make(chan int64, 1)
	feederStopped = make(chan struct{})
	go func() {
		// launch to background and feed queue
		//log.Printf("DownloadArticles: Fetching %d articles for group '%s' using %d goroutines", toFetch, newsgroup, proc.Pool.Backend.MaxConns)
		prepare := func(messageID string) *BatchItem {
			/*
				if !CheckMessageIdFormat(messageID) {
					log.Printf("[FETCHER]: Invalid message ID format: '%s'", messageID)
					return &BatchItem{Error: errIsDuplicateError}
				}
			*/
			//log.Printf("DownloadArticles: Checking if article '%s' exists in group '%s'", messageID, newsgroup)
			// A-db-3: a failed lookup is not "not stored": the item fails instead of being downloaded
			stored, err := groupDB.LookupMsgIdInArticlesDB(messageID)
			if err != nil {
				return &BatchItem{Error: fmt.Errorf("message-id lookup of '%s' in '%s': %w", messageID, newsgroup, err)}
			}
			if stored {
				return &BatchItem{Error: errIsDuplicateError}
			}
			var art *models.Article
			if reuseEnabled {
				art = proc.reuseStoredArticle(messageID, currentGroupID, newsgroup)
			}
			msgIdItem := history.MsgIdCache.GetORCreate(messageID)
			msgIdItem.Mux.Lock()
			msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
			msgIdItem.Response = history.CaseLock
			msgIdItem.Mux.Unlock()
			if art != nil {
				// crosspost already stored in another group: skip the download.
				// counts as queued, the consumer counts it as gots or errs like a downloaded article.
				reused.Add(1)
			}
			// without an article the item goes to Batch.GetQ: fetcher/main.go:~L540: for item := range processor.Batch.GetQ
			return &BatchItem{
				MessageID: &msgIdItem.MessageId, // Use pointer to avoid copying
				GroupName: proc.DB.Batch.GetNewsgroupPointer(newsgroup),
				Article:   art,
				ReturnQ:   groupBatch.ReturnQ,
			}
		}
		queued, complete := feedDownloadRun(xhdrChan, runDone, feederStopped, groupBatch.ReturnQ, Batch.GetQ, prepare, &handed)
		if !complete {
			return // run returned or shutdown: nobody waits for the counts
		}
		//log.Printf("DownloadArticles: XHdr closed, finished feeding batch queue %d articles for group '%s'", queued, newsgroup)
		if queued == 0 {
			releaseChan <- struct{}{}
		} else {
			notifyChan <- queued
		}
	}()
	var dups, lastDups, gots, lastGots, notf, lastNotf, errs, lastErrs int64
	var mismatch int64   // articles the provider served under a foreign Message-ID
	var authFailed error // set when the provider's login expired: stops this group's run
	aliveCheck := 5 * time.Second
	maxDeathCounter := 12 // Maximum number of allowed "stuck" checks done every aliveCheck interval
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	startTime := time.Now()
	nextCheck := startTime.Add(aliveCheck)
	deathCounter := 0 // Counter to track if we are stuck
	bulkmode := true
	var gotQueued int64 = -1
	if common.WantShutdown() {
		log.Printf("DownloadArticlesFromDate: common.WantShutdown(): stopping")
		return fmt.Errorf("shutdown requested")
	}
	// Start processing loop
forProcessing:
	for {
		select {
		case gotQueued = <-notifyChan:
		case <-releaseChan:
			//log.Printf("DownloadArticles: releaseChan triggered '%s'", newsgroup)
			break forProcessing
		case <-ticker.C:

			/* disabled
			if common.WantShutdown() {
				return fmt.Errorf("DownloadArticles:common.WantShutdown() group '%s'", newsgroup)
			}
			*/
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
				if common.WantShutdown() {
					return fmt.Errorf("DownloadArticles: got common.WantShutdown() in group '%s'", newsgroup)
				}
				// If we haven't made progress in N seconds, log a warning
				log.Printf("DownloadArticles: '%s' Stuck? %d articles processed (%d dups, %d gots, %d notf, %d errs, gotQueued: %d) (since Start=%v)", newsgroup, dups+gots+notf+errs, dups, gots, notf, errs, gotQueued, time.Since(startTime))
				nextCheck = time.Now().Add(aliveCheck) // Reset last check time
				deathCounter++
			}
			if deathCounter > maxDeathCounter { // If we are stuck for too long
				log.Printf("DownloadArticles: '%s' Timeout... stopping import deathCounter=%d", newsgroup, deathCounter)
				return fmt.Errorf("DownloadArticles: '%s' Timeout... %d articles processed (%d dups, %d got, %d errs)", newsgroup, dups+gots+notf+errs, dups, gots, errs)
			}

		case item := <-groupBatch.ReturnQ:
			received++
			//log.Printf("DEBUG-RETURN: received item: Error=%v, Article=%v", item != nil && item.Error != nil, item != nil && item.Article != nil)
			if item == nil || item.Error != nil || item.Article == nil {
				if item != nil {
					if item.MessageID != nil {
						msgIdItem := history.MsgIdCache.GetORCreate(*item.MessageID)
						if msgIdItem != nil && msgIdItem.MessageId == *item.MessageID {
							msgIdItem.Mux.Lock()
							msgIdItem.CachedEntryExpires = time.Now().Add(5 * time.Second)
							msgIdItem.Response = history.CaseError
							msgIdItem.Mux.Unlock()
						}
					}
					switch item.Error {
					case errIsDuplicateError:
						dups++
					case nntp.ErrArticleNotFound:
						notf++
					case nntp.ErrArticleRemoved:
						notf++
					default:
						// An expired login (480) hits every following article the same
						// way: retrying the rest of the group only burns a re-dial and a
						// re-login per article and would move the progress past articles
						// that were never fetched. Stop the group run once, keep the
						// progress and let the next run fetch the range again
						// (plan fetcher-hostile-peer, design point 2).
						if errors.Is(item.Error, nntp.ErrAuthRequired) {
							errs++
							authFailed = item.Error
							log.Printf("DownloadArticles: '%s' provider authentication expired, stopping group run: %v", newsgroup, item.Error)
							break forProcessing
						}
						if item.MessageID != nil {
							log.Printf("DownloadArticles: '%s' Error fetching article %s: %v .. continue", newsgroup, *item.MessageID, item.Error)
						} else {
							log.Printf("DownloadArticles: '%s' Error fetching item: %#v .. continue", newsgroup, item)
						}
						errs++
					}
				} else {
					log.Printf("ERROR in DownloadArticles: received nil item (errs %d)", errs)
					errs++
				}
			} else if item.Error == nil && item.Article != nil {
				// A provider whose overview is out of step with its articles answers
				// ARTICLE <id> with another article. ProcessArticle rejects that too
				// (A-intake-3), but once per article; a provider out of step does it for
				// the whole group, so the fetcher drops it here and logs once per run.
				if mid := fetchedMsgIDHeader(item.Article.HeadersJSON); mid != "" && mid != item.Article.MessageID {
					mismatch++
					resetLateMsgID(item.MessageID) // a later run may fetch it again
					if mismatch == 1 {
						log.Printf("DownloadArticles: '%s' provider out of step: ARTICLE %s answered with Message-ID %s (logged once per run)", newsgroup, item.Article.MessageID, mid)
					}
					errs++
				} else {
					//log.Printf("DownloadArticles --> proc.ProcessArticle '%s' in group '%s'", *item.MessageID, newsgroup)
					response, err := proc.ProcessArticle(item.Article, newsgroup, bulkmode)
					if err != nil {
						log.Printf("DownloadArticles: '%s' Failed to process article (%s): response=%d err='%v'", newsgroup, *item.MessageID, response, err)
						errs++
					} else {
						gots++
					}
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
	if authFailed != nil {
		// the progress stays where it was: the rest of the range was never fetched
		return fmt.Errorf("DownloadArticles: '%s' %d-%d stopped: %w", newsgroup, start, end, authFailed)
	}
	if common.WantShutdown() {
		return fmt.Errorf("DownloadArticles: common.WantShutdown() group '%s'", newsgroup)
	}
	if xerr := <-errChan; xerr != nil {
		// The message-id list of start-end is incomplete (cut-off stream, refused range): the
		// articles after the cut were never seen, so the progress stays where it was and the
		// next run fetches the range again (stored articles are skipped by the lookup).
		log.Printf("DownloadArticles: '%s' XHDR %d-%d failed, progress not updated: %v", newsgroup, start, end, xerr)
		return fmt.Errorf("DownloadArticles: '%s' XHDR %d-%d: %w", newsgroup, start, end, xerr)
	}
	if common.WantShutdown() {
		return fmt.Errorf("DownloadArticles: common.WantShutdown() group '%s'", newsgroup)
	}
	if err := proc.commitThenProgress(newsgroup, proc.Pool.Backend.Provider.Name, commitMark, progressDB, end, gots, gotQueued, dups, shutdownChan); err != nil {
		return err
	}
	// threading.go:296: GroupCounter.Increment(newsgroup) // Increment the group counter
	groupCnt := GroupCounter.GetReset(newsgroup)
	log.Printf("DownloadArticles: '%s' processed %d articles [gotQueued=%d] (dups: %d, gots: %d, errs: %d, mismatch: %d, adds: %d, reused: %d) in %v end=%d", newsgroup, gots+errs+dups, gotQueued, dups, gots, errs, mismatch, groupCnt, reused.Load(), time.Since(startTime), end)
	// do another one if we haven't run enough times
	runtime.GC()

	if common.WantShutdown() {
		return fmt.Errorf("DownloadArticles: common.WantShutdown() group '%s'", newsgroup)
	}
	return nil
} // end func DownloadArticles

// downloadWaitCommitted is the commit barrier of commitThenProgress (a seam for tests).
var downloadWaitCommitted = func(sq *database.SQ3batch, m database.CommitMark, stop <-chan struct{}, d time.Duration) error {
	return sq.WaitCommitted(m, stop, d)
}

// commitThenProgress holds the progress of providerName/newsgroup back until the articles
// queued since mark are committed, then moves it to end.
//
// Commit barrier (A-tools-5b): the progress may only pass articles that are stored. On a
// dropped batch, a stop or a timeout it is held back (D-B1) and the next run fetches the
// range again. The barrier only runs when articles were queued for storage (gots > 0); the
// progress only moves when something was queued or found as duplicate. An UpdateProgress
// error is returned.
func (proc *Processor) commitThenProgress(newsgroup, providerName string, mark database.CommitMark, progressDB *database.ProgressDB, end, gots, gotQueued, dups int64, stop <-chan struct{}) error {
	if gots > 0 {
		var sq *database.SQ3batch
		if proc.DB != nil {
			sq = proc.DB.Batch
		}
		if werr := downloadWaitCommitted(sq, mark, stop, CommitWaitTimeout); werr != nil {
			log.Printf("DownloadArticles: '%s' articles up to %d not committed, progress not updated: %v", newsgroup, end, werr)
			return fmt.Errorf("DownloadArticles: '%s' articles up to %d not committed: %w", newsgroup, end, werr)
		}
	}
	if gotQueued > 0 || dups > 0 {
		// only update progress if we actually got something
		if err := progressDB.UpdateProgress(providerName, newsgroup, end); err != nil {
			log.Printf("Failed to update progress for provider '%s' group '%s': %v", providerName, newsgroup, err)
			return fmt.Errorf("DownloadArticles: '%s' update progress for provider '%s' to %d: %w", newsgroup, providerName, end, err)
		}
	}
	return nil
}

// FindStartArticleByDate finds the first article number on or after the given date
// using a simple binary search approach with XOVER data
func (proc *Processor) FindStartArticleByDate(groupName string, targetDate time.Time, groupInfo *nntp.GroupInfo) (int64, error) {
	/*
		// Get group info
		groupInfo, err := proc.Pool.SelectGroup(groupName)
		if err != nil {
			return 0, fmt.Errorf("failed to select group: %w", err)
		}
	*/
	if groupInfo.Last == 0 {
		log.Printf("No articles in group %s", groupName)
		return 0, ErrIsEmptyGroup
	}
	// decrease targetDate by 2 days
	targetDate = targetDate.AddDate(0, 0, -2)

	log.Printf("Finding start article for date %s in group %s (range %d-%d)",
		targetDate.Format("2006-01-02"), groupName, groupInfo.First, groupInfo.Last)

	// Check if target date is before the first article
	enforceLimit := true
	firstOverviews, err := proc.Pool.XOver(groupName, groupInfo.First, groupInfo.First, enforceLimit)
	if err == nil && len(firstOverviews) > 0 {
		firstArticleDate := ParseNNTPDate(firstOverviews[0].Date)
		if !firstArticleDate.IsZero() && targetDate.Before(firstArticleDate) {
			log.Printf("Target date %s is before first article %d (date: %s), returning first article. ng: %s",
				targetDate.Format("2006-01-02"), groupInfo.First, firstArticleDate.Format("2006-01-02"), groupName)
			return groupInfo.First, nil
		} else {
			log.Printf("Target date %s is after first article %d (date: %s), checking last article. ng: %s",
				targetDate.Format("2006-01-02"), groupInfo.First, firstArticleDate.Format("2006-01-02"), groupName)
		}
	}

	// Check if target date is after the last article
	lastOverviews, err := proc.Pool.XOver(groupName, groupInfo.Last, groupInfo.Last, enforceLimit)
	if err == nil && len(lastOverviews) > 0 {
		lastArticleDate := ParseNNTPDate(lastOverviews[0].Date)
		if !lastArticleDate.IsZero() && targetDate.After(lastArticleDate) {
			log.Printf("Target date %s is after last article %d (date: %s), returning last article. ng: %s",
				targetDate.Format("2006-01-02"), groupInfo.Last, lastArticleDate.Format("2006-01-02"), groupName)
			return groupInfo.Last, nil
		} else {
			log.Printf("Target date %s is before last article %d (date: %s), starting search. ng: %s",
				targetDate.Format("2006-01-02"), groupInfo.Last, lastArticleDate.Format("2006-01-02"), groupName)
		}
	}
	groupInfo.FetchStart = groupInfo.First
	groupInfo.FetchEnd = groupInfo.Last

	// Binary search using 50% approach
	for groupInfo.FetchEnd-groupInfo.FetchStart > 1 {
		if common.WantShutdown() {
			return 0, fmt.Errorf("FindStartArticleByDate: common.WantShutdown() group '%s'", groupName)
		}
		mid := groupInfo.FetchStart + (groupInfo.FetchEnd-groupInfo.FetchStart)/2

		// Get XOVER for this article
		overviews, err := proc.Pool.XOver(groupName, mid, mid, enforceLimit)
		if err != nil || len(overviews) == 0 {
			// Article doesn't exist, try moving up
			groupInfo.FetchStart = mid
			continue
		}

		articleDate := ParseNNTPDate(overviews[0].Date)
		if articleDate.IsZero() {
			groupInfo.FetchStart = mid
			continue
		}

		log.Printf("Scanning: %s - Article %d has date %s", groupName, mid, articleDate.Format("2006-01-02"))

		if articleDate.Before(targetDate) {
			groupInfo.FetchStart = mid
		} else {
			groupInfo.FetchEnd = mid
		}
	}
	if common.WantShutdown() {
		return 0, fmt.Errorf("FindStartArticleByDate: common.WantShutdown() group '%s'", groupName)
	}
	log.Printf("Found start article: %d, ng: %s", groupInfo.FetchEnd, groupName)
	return groupInfo.FetchEnd, nil
}

var ErrIsEmptyGroup = fmt.Errorf("isEmptyGroup")

// DownloadArticlesFromDate fetches articles starting from a specific date
// Uses special progress tracking: sets progress to startArticle-1, or -1 if starting from article 1
// This prevents DownloadArticles from using "no progress detected" logic for existing groups
func (proc *Processor) DownloadArticlesFromDate(groupName string, startDate time.Time, DLParChan chan struct{}, progressDB *database.ProgressDB, groupInfo *nntp.GroupInfo, shutdownChan <-chan struct{}) error {
	//log.Printf("DownloadArticlesFromDate: Starting download from date %s for group '%s'", startDate.Format("2006-01-02"), groupName)

	// Find the starting article number based on date
	startArticle, err := proc.FindStartArticleByDate(groupName, startDate, groupInfo)
	if err != nil {
		if err == ErrIsEmptyGroup {
			return err
		}
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
	/*
		groupInfo, err := proc.Pool.SelectGroup(groupName)
		if err != nil {
			return fmt.Errorf("failed to select group for date download: %w", err)
		}
	*/

	// Calculate download range: start from found article, end at current group last or startArticle + MaxBatch
	downloadStart := startArticle
	downloadEnd := startArticle + MaxBatchSize - 1
	if downloadEnd > groupInfo.Last {
		downloadEnd = groupInfo.Last
	}
	if common.WantShutdown() {
		log.Printf("DownloadArticlesFromDate: common.WantShutdown(): stopping")
		return fmt.Errorf("shutdown requested")
	}
	//log.Printf("DownloadArticlesFromDate: Downloading range %d-%d for group '%s' (group last: %d)",	downloadStart, downloadEnd, groupName, groupInfo.Last)

	// Now use the high-performance DownloadArticles function with proper article ranges
	err = proc.DownloadArticles(groupName, DLParChan, progressDB, downloadStart, downloadEnd, shutdownChan)

	if common.WantShutdown() {
		log.Printf("DownloadArticlesFromDate: common.WantShutdown(): stopping")
		return fmt.Errorf("shutdown requested")
	}
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
