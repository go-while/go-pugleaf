// Package postmgr provides article posting management for go-pugleaf
package postmgr

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// ErrStopped is returned by ProcessPendingPosts when a stop (Stop or Run's shutdown signal)
// ended the batch before every selected entry was worked on; those entries are back to pending.
var ErrStopped = errors.New("post queue processing stopped")

// errPostRejected marks a provider's 441 answer: the provider refused this article. Only a
// rejection by every pool counts as a post_queue attempt (see processEntry).
var errPostRejected = errors.New("article rejected by provider (441)")

// defaultIdlePause is Run's pause after a batch that did not fill the limit.
const defaultIdlePause = 10 * time.Second

// PosterManager manages the posting of articles to multiple providers
type PosterManager struct {
	DB       *database.Database
	pools    []*nntp.Pool
	stopCh   chan struct{}
	stopOnce sync.Once // stopCh is closed by Run (shutdown signal) or Stop, whichever comes first
	// ctx is cancelled together with the close of stopCh: it interrupts a pool wait and an
	// in-flight POST (the connection is force-closed, see nntp.PostArticleContext).
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// idlePause is Run's pause after a batch that did not fill the limit (default 10 s); a field
	// so tests can set it before Run starts.
	idlePause time.Duration
}

// NewPosterManager creates a new poster manager
func NewPosterManager(db *database.Database, pools []*nntp.Pool) *PosterManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &PosterManager{
		DB:        db,
		pools:     pools,
		stopCh:    make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
		idlePause: defaultIdlePause,
	}
}

// closeStop closes stopCh once and cancels ctx, which interrupts a running POST.
func (pm *PosterManager) closeStop() {
	pm.stopOnce.Do(func() {
		close(pm.stopCh)
		pm.cancel()
	})
}

// stopped reports whether a stop was requested.
func (pm *PosterManager) stopped() bool {
	select {
	case <-pm.stopCh:
		return true
	default:
		return false
	}
}

// sleep waits for d or until a stop is requested; it returns false on stop.
func (pm *PosterManager) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-pm.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

// Run starts the poster manager. A close of shutdownChan stops it, also in the middle of a batch:
// the rows of the batch not yet posted are reset to pending (see ProcessPendingPosts).
func (pm *PosterManager) Run(limit int, shutdownChan <-chan struct{}, wg *sync.WaitGroup) {
	log.Printf("PosterManager: Starting with %d pools", len(pm.pools))
	defer wg.Done()

	// Forward the shutdown signal to stopCh so that a running batch and the sleep see it.
	runDone := make(chan struct{})
	defer close(runDone)
	go func() {
		select {
		case <-shutdownChan:
			log.Printf("PosterManager: Received shutdown signal")
			pm.closeStop()
		case <-pm.stopCh:
		case <-runDone:
		}
	}()

	for {
		if pm.stopped() {
			pm.wg.Wait()
			return
		}
		// Check for pending posts
		done, err := pm.ProcessPendingPosts(limit)
		if err != nil && !errors.Is(err, ErrStopped) {
			log.Printf("PosterManager: Error processing pending posts: %v", err)
		}
		pause := pm.idlePause
		if pause <= 0 {
			pause = defaultIdlePause
		}
		if done == limit {
			pause = 1 * time.Second
		}
		if !pm.sleep(pause) {
			pm.wg.Wait()
			return
		}
	}
}

// Stop stops the poster manager. It is safe to call more than once and together with Run.
func (pm *PosterManager) Stop() {
	pm.closeStop()
	pm.wg.Wait()
}

// ProcessPendingPosts processes all pending posts in the queue.
// It returns the number of entries it worked on (posted or failed). Every failed entry goes back
// to pending with its last_error; a rejection by every provider (441) also counts an attempt, and
// an entry that reaches PostQueueMaxAttempts is parked. On stop it returns early with ErrStopped
// and resets the entries not yet worked on to pending, so the next run picks them up. A stop also
// interrupts the entry being posted: it fails, and because the stop is seen after its failure it
// goes back to pending without an attempt or last_error (the failure is the stop's own doing).
// If the provider had already received the whole article, the next run posts it again and gets
// the provider's duplicate answer (accepted, plan small-fixes-postrun Q1).
func (pm *PosterManager) ProcessPendingPosts(limit int) (int, error) {
	// Get pending posts from database
	entries, err := pm.DB.GetPendingPostQueueEntries(limit)
	if err != nil {
		return 0, fmt.Errorf("failed to get pending posts: %v", err)
	}

	if len(entries) == 0 {
		//log.Printf("No pending posts found")
		return 0, nil
	}

	log.Printf("Found %d pending posts to process", len(entries))

	var failedEntryIDs []int64
	posted, failed := 0, 0

	// stopAt resets the entries from index next on, plus failedEntryIDs (failed entries whose
	// failure could not be recorded, and the entry a stop interrupted), and returns ErrStopped.
	stopAt := func(next int) (int, error) {
		log.Printf("PosterManager: Stopping due to shutdown signal, %d of %d entries not processed", len(entries)-posted-failed, len(entries))
		log.Printf("Processed %d pending posts (%d posted, %d failed)", posted+failed, posted, failed)
		resetIDs := failedEntryIDs
		for _, rest := range entries[next:] {
			resetIDs = append(resetIDs, rest.ID)
		}
		if len(resetIDs) > 0 {
			if err := pm.DB.ResetPostQueueProcessing(resetIDs); err != nil {
				return posted + failed, fmt.Errorf("failed to reset processing state of %d entries on stop: %v", len(resetIDs), err)
			}
		}
		return posted + failed, ErrStopped
	}

	// Process each entry
	for i, entry := range entries {
		if pm.stopped() {
			return stopAt(i)
		}
		err := pm.processEntry(entry)
		if err == nil {
			posted++
			continue
		}
		if pm.stopped() && !errors.Is(err, errPostRejected) {
			// Interrupted by the stop (pool wait or POST cancelled): back to pending without an
			// attempt; see the duplicate caveat in the function comment. A rejection by every
			// provider that completed just before the stop still counts below.
			log.Printf("Entry %d (message: %s) interrupted by shutdown, back to pending: %v", entry.ID, entry.MessageID, err)
			failedEntryIDs = append(failedEntryIDs, entry.ID)
			return stopAt(i + 1)
		}
		failed++
		rejected := errors.Is(err, errPostRejected)
		log.Printf("Failed to process entry %d (message: %s, rejected=%v): %v", entry.ID, entry.MessageID, rejected, err)
		// RecordPostQueueFailure also clears in_processing; when it fails, reset the flag below.
		if rerr := pm.DB.RecordPostQueueFailure(entry.ID, err.Error(), rejected); rerr != nil {
			log.Printf("Failed to record failure of entry %d: %v", entry.ID, rerr)
			failedEntryIDs = append(failedEntryIDs, entry.ID)
		}
	}

	// Reset processing state for failed entries whose failure could not be recorded
	if len(failedEntryIDs) > 0 {
		if err := pm.DB.ResetPostQueueProcessing(failedEntryIDs); err != nil {
			log.Printf("Failed to reset processing state for failed entries: %v", err)
		}
	}

	log.Printf("Processed %d pending posts (%d posted, %d failed)", posted+failed, posted, failed)
	return len(entries), nil
}

// processEntry processes a single post queue entry
func (pm *PosterManager) processEntry(entry database.PostQueueEntry) error {
	log.Printf("Processing: #%d '%s'", entry.ID, entry.MessageID)

	// Get the newsgroup by ID
	newsgroup, err := pm.DB.MainDBGetNewsgroupByID(entry.NewsgroupID)
	if err != nil {
		return fmt.Errorf("failed to get newsgroup with ID %d: %v", entry.NewsgroupID, err)
	}

	if !newsgroup.Active {
		log.Printf("Newsgroup %s is not active, skipping", newsgroup.Name)
		return pm.DB.MarkPostQueueAsPostedToRemote(entry.ID)
	}

	// Get the article from the local database
	article, err := pm.getArticleByMessageID(entry.MessageID, newsgroup.Name)
	if err != nil {
		return fmt.Errorf("failed to get article '%s': %v", entry.MessageID, err)
	}

	// Post to all available providers
	successCount := 0
	rejected := 0      // pools that answered 441
	var lastErr error  // last error of any kind
	var otherErr error // first error that is not a 441: the reason when not every pool rejected
	for _, pool := range pm.pools {
		if pm.stopped() {
			// don't try the next provider after a stop (it may have interrupted the last one)
			if lastErr != nil {
				return fmt.Errorf("%w (last provider error: %v)", ErrStopped, lastErr)
			}
			return ErrStopped
		}
		if err := pm.postToProvider(article, pool); err != nil {
			log.Printf("Failed to post article %s to provider %s: %v", entry.MessageID, pool.Backend.Provider.Name, err)
			lastErr = err
			if errors.Is(err, errPostRejected) {
				rejected++
			} else if otherErr == nil {
				otherErr = err
			}
		} else {
			successCount++
			log.Printf("Successfully posted article %s to provider %s", entry.MessageID, pool.Backend.Provider.Name)
			break
		}
	}

	if successCount > 0 {
		//log.Printf("Successfully posted article '%s'", entry.MessageID)
		return pm.DB.MarkPostQueueAsPostedToRemote(entry.ID)
	}

	if len(pm.pools) > 0 && rejected == len(pm.pools) {
		// every provider refused the article: this counts as an attempt
		return fmt.Errorf("article %s rejected by all %d providers: %w", entry.MessageID, rejected, lastErr)
	}
	if otherErr != nil {
		// at least one provider failed for another reason (connection, pool, protocol): no attempt
		// is counted; errPostRejected is not wrapped, so a partial rejection does not count either.
		// The first such error is the reason stored in last_error, not a 441 of another pool.
		return fmt.Errorf("failed to post article %s to any provider (%d of %d rejected): %v", entry.MessageID, rejected, len(pm.pools), otherErr)
	}
	return fmt.Errorf("failed to post article %s to any provider: no posting provider", entry.MessageID)
}

// getArticleByMessageID retrieves an article from the local database
func (pm *PosterManager) getArticleByMessageID(messageID, newsgroup string) (*models.Article, error) {
	// Get group database connection
	groupDB, err := pm.DB.GetGroupDB(newsgroup)
	if err != nil {
		return nil, err
	}
	defer groupDB.Return()

	// Get article by message ID using the database method (not groupDB method)
	article, err := pm.DB.GetArticleByMessageID(groupDB, messageID)
	if err != nil {
		return nil, err
	}

	return article, nil
}

// postToProvider posts an article to a specific provider
func (pm *PosterManager) postToProvider(article *models.Article, pool *nntp.Pool) error {

	peering := false // TODO: pool.Backend.Provider.Peering

	if pool.Backend.Provider.Posting {
		conn, err := pool.GetContext(pm.ctx, nntp.MODE_READER_MV)
		if err != nil {
			return fmt.Errorf("failed to get connection: %v", err)
		}
		// Use POST for posting articles (standard NNTP posting). A stop (pm.ctx) interrupts it:
		// PostArticleContext force-closes the connection and err wraps context.Canceled.
		responseCode, err := conn.PostArticleContext(pm.ctx, article)
		if responseCode == 441 {
			// Posting failed: the provider refused this article. PostArticle reports the 441 as an
			// error (the expected code was 340/240), but the connection is still in sync.
			pool.Put(conn)
			if err != nil {
				return fmt.Errorf("%w: %v", errPostRejected, err)
			}
			return errPostRejected
		}
		if err != nil {
			conn.ForceCloseConn()
			return fmt.Errorf("failed to post article: %w", err)
		}
		// Check response code
		switch responseCode {
		case 240: // Article posted successfully
			pool.Put(conn)
			return nil
		default:
			conn.ForceCloseConn()
			return fmt.Errorf("unexpected response code: %d", responseCode)
		}
	}

	if peering { // TODO: pool.Backend.Provider.Peering
		conn, err := pool.Get(nntp.MODE_STREAM_MV)
		if err != nil {
			return fmt.Errorf("failed to get connection: %v", err)
		}
		defer pool.Put(conn)
	}

	return fmt.Errorf("selected provider %s does not support posting or peering", pool.Backend.Provider.Name)
}
