package processor

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"sort"
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
// block has none. DownloadArticles calls it for every downloaded article.
func fetchedMsgIDHeader(headersJSON string) string {
	// The same matcher as the A-intake-3 check (common.HeaderValue): whitespace before
	// the colon and folded continuation lines are handled, so every shape the intake
	// would reject is caught here, once per run, instead of once per article there.
	if headersJSON == "" {
		return ""
	}
	return strings.TrimSpace(common.HeaderValue(strings.Split(headersJSON, "\n"), "Message-ID"))
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

	// The search assumes that Dates mostly rise with article numbers. Every step reads a window
	// of articles and decides by the median of their Dates, so misdated articles (a late feed,
	// a stuck clock, a forged Date) move it only when they are the majority of a window.
	// Windows with fewer than two readable Dates, holes and undated articles move the search
	// down (more is fetched, nothing skipped); an XOVER error other than 423 ends it with that
	// error (the caller writes no progress). There is no shortcut on the first or the last
	// article: a late article with an old Date at the top, or a stale low water mark, would
	// mislead it. The start found is never above the first article dated on/after the target
	// (by window evidence) and may lie up to about one window below it.
	enforceLimit := true

	// Every article up to FetchStart is before the target (by window evidence); FetchEnd is an
	// upper bound of the start. Every step moves one of them strictly.
	groupInfo.FetchStart = groupInfo.First - 1
	groupInfo.FetchEnd = groupInfo.Last
	for groupInfo.FetchEnd-groupInfo.FetchStart > 1 {
		if common.WantShutdown() {
			return 0, fmt.Errorf("FindStartArticleByDate: common.WantShutdown() group '%s'", groupName)
		}
		mid := groupInfo.FetchStart + (groupInfo.FetchEnd-groupInfo.FetchStart)/2
		upper := groupInfo.FetchEnd - 1

		w, err := proc.probeDateRight(groupName, mid, upper, targetDate, enforceLimit)
		if err != nil {
			return 0, fmt.Errorf("FindStartArticleByDate: group '%s': articles from %d: %w", groupName, mid, err)
		}
		if w.existing {
			decideDateWindow(groupInfo, w)
			continue
		}

		// mid..w.to holds no article. The numbers just below mid decide whether the hole may
		// be crossed upward.
		l, err := proc.probeDateLeft(groupName, groupInfo.FetchStart+1, mid-1, targetDate, enforceLimit)
		if err != nil {
			return 0, fmt.Errorf("FindStartArticleByDate: group '%s': articles below %d: %w", groupName, mid, err)
		}
		crossTo := w.to // cross the hole: everything up to crossTo is before the target or absent
		if w.to >= upper {
			crossTo = upper // mid..FetchEnd-1 is empty: crossing it makes FetchEnd the start
		}
		switch {
		case !l.existing && l.from <= groupInfo.FetchStart+1:
			// FetchStart+1..w.to is empty
			groupInfo.FetchStart = crossTo
		case l.allBefore():
			// the articles right below the hole are before the target
			groupInfo.FetchStart = crossTo
		case l.existing || w.to >= upper:
			// an article right below the hole is on/after the target or undated, or there is
			// nothing above the hole: start lower
			groupInfo.FetchEnd = mid
		default:
			// empty on both sides of mid and the range goes on (a purge, a stale low water
			// mark): find the next articles above the hole and decide there
			g, err := proc.probeDateFind(groupName, w.to+1, upper, targetDate, enforceLimit)
			if err != nil {
				return 0, fmt.Errorf("FindStartArticleByDate: group '%s': articles above %d: %w", groupName, w.to, err)
			}
			if g.existing {
				decideDateWindow(groupInfo, g)
			} else {
				groupInfo.FetchEnd = mid
			}
		}
	}
	if common.WantShutdown() {
		return 0, fmt.Errorf("FindStartArticleByDate: common.WantShutdown() group '%s'", groupName)
	}
	start := groupInfo.FetchEnd
	if start < groupInfo.First {
		start = groupInfo.First
	}
	log.Printf("Found start article: %d, ng: %s", start, groupName)
	return start, nil
}

// dateProbeWindow is how many article numbers one date probe reads; dateProbeWide is the
// second, wider probe over a window with fewer than dateMinReadable readable Dates (holes cost
// little to read). Both are capped at nntp.MaxReadLinesXover (the fetcher's -max-batch), which
// XOver's enforceLimit applies.
const (
	dateProbeWindow = 64
	dateProbeWide   = 1000
	dateMinReadable = 2
)

// dateWindow is what one XOVER window from..to showed, measured against the target date.
type dateWindow struct {
	from, to      int64 // the numbers asked for
	existing      bool  // any article in the window
	firstExisting int64 // lowest article number in the window
	lastExisting  int64 // highest article number in the window
	readable      int   // articles with a readable Date
	before        int   // of them, dated before the target
	lastReadable  int64 // highest article number with a readable Date
	lowerMedian   int64 // number of the readable article at the lower median (number order)
}

// enough reports whether the window has enough readable Dates to decide anything.
func (w dateWindow) enough() bool { return w.readable >= dateMinReadable }

// medianBefore reports whether most readable Dates of the window are before the target.
func (w dateWindow) medianBefore() bool { return w.enough() && 2*w.before > w.readable }

// allBefore reports whether every article of the window has a readable Date before the target.
func (w dateWindow) allBefore() bool {
	return w.enough() && w.before == w.readable && w.lastReadable == w.lastExisting
}

// decideDateWindow moves gi.FetchStart or gi.FetchEnd by a window that holds articles and lies
// in FetchStart+1..FetchEnd-1: up to its last readable article when every article is before
// the target, up to its lower median when most Dates are before it, otherwise (most on/after
// the target, or too few readable Dates) down to its first article.
func decideDateWindow(gi *nntp.GroupInfo, w dateWindow) {
	switch {
	case w.allBefore():
		gi.FetchStart = w.lastReadable
	case w.medianBefore():
		gi.FetchStart = w.lowerMedian
	default:
		gi.FetchEnd = w.firstExisting
	}
}

// probeDateWindow reads the overview of from..min(from+size-1, upTo). A 423 is an empty
// window; any other XOVER failure (timeout, dropped connection, 4xx/5xx) is returned: the
// caller must not take it for "nothing there".
func (proc *Processor) probeDateWindow(groupName string, from, upTo, size int64, target time.Time, enforceLimit bool) (dateWindow, error) {
	if enforceLimit && nntp.MaxReadLinesXover > 0 && size > nntp.MaxReadLinesXover {
		size = nntp.MaxReadLinesXover // XOver would cut the range there: never claim more than was read
	}
	to := from + size - 1
	if to > upTo {
		to = upTo
	}
	if to < from {
		to = from
	}
	w := dateWindow{from: from, to: to}
	overviews, err := proc.Pool.XOver(groupName, from, to, enforceLimit)
	if err != nil {
		if errors.Is(err, nntp.ErrNoArticlesInRange) {
			return w, nil
		}
		return w, err
	}
	inRange := make([]nntp.OverviewLine, 0, len(overviews))
	for _, ov := range overviews {
		if ov.ArticleNum >= from && ov.ArticleNum <= to {
			inRange = append(inRange, ov)
		}
	}
	sort.Slice(inRange, func(i, j int) bool { return inRange[i].ArticleNum < inRange[j].ArticleNum })
	var readableNums []int64
	for _, ov := range inRange {
		if !w.existing {
			w.firstExisting = ov.ArticleNum
		}
		w.existing = true
		w.lastExisting = ov.ArticleNum
		d := ParseNNTPDate(ov.Date)
		if d.IsZero() {
			continue
		}
		w.readable++
		w.lastReadable = ov.ArticleNum
		readableNums = append(readableNums, ov.ArticleNum)
		if d.Before(target) {
			w.before++
		}
	}
	if len(readableNums) > 0 {
		w.lowerMedian = readableNums[(len(readableNums)-1)/2]
	}
	return w, nil
}

// probeDateRight probes from upward: a dateProbeWindow window, and a dateProbeWide one when
// that held fewer than dateMinReadable readable Dates.
func (proc *Processor) probeDateRight(groupName string, from, upTo int64, target time.Time, enforceLimit bool) (dateWindow, error) {
	w, err := proc.probeDateWindow(groupName, from, upTo, dateProbeWindow, target, enforceLimit)
	if err != nil || w.enough() || w.to >= upTo {
		return w, err
	}
	return proc.probeDateWindow(groupName, from, upTo, dateProbeWide, target, enforceLimit)
}

// probeDateLeft probes the numbers just below hi (down to lo): a dateProbeWindow window, and
// a dateProbeWide one when that held fewer than dateMinReadable readable Dates.
func (proc *Processor) probeDateLeft(groupName string, lo, hi int64, target time.Time, enforceLimit bool) (dateWindow, error) {
	if hi < lo {
		return dateWindow{from: lo, to: hi}, nil
	}
	from := max(lo, hi-dateProbeWindow+1)
	w, err := proc.probeDateWindow(groupName, from, hi, dateProbeWindow, target, enforceLimit)
	if err != nil || w.enough() || from <= lo {
		return w, err
	}
	from = max(lo, hi-dateProbeWide+1)
	return proc.probeDateWindow(groupName, from, hi, dateProbeWide, target, enforceLimit)
}

// probeDateFind looks for the next articles in from..upTo above an empty window: windows of
// dateProbeWindow at from, from+1000, from+3000, ... (the step doubling); when a step passes
// upTo it bisects between the last empty window and upTo. It returns the first window found
// that holds any article, or an empty window when none was found.
func (proc *Processor) probeDateFind(groupName string, from, upTo int64, target time.Time, enforceLimit bool) (dateWindow, error) {
	empty := dateWindow{from: from, to: upTo}
	lo, step := from, int64(dateProbeWide)
	for lo <= upTo {
		w, err := proc.probeDateWindow(groupName, lo, upTo, dateProbeWindow, target, enforceLimit)
		if err != nil || w.existing {
			return w, err
		}
		if w.to >= upTo {
			return empty, nil
		}
		next := lo + step
		step *= 2
		if next <= upTo {
			lo = next
			continue
		}
		// bisect w.to+1..upTo for a window with articles
		a, b := w.to+1, upTo
		for a <= b {
			m := a + (b-a)/2
			w, err := proc.probeDateWindow(groupName, m, b, dateProbeWindow, target, enforceLimit)
			if err != nil || w.existing {
				return w, err
			}
			if w.to >= b {
				b = m - 1 // m..b is empty: look lower
			} else {
				a = w.to + 1
			}
		}
		return empty, nil
	}
	return empty, nil
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
