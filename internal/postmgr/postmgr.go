// Package postmgr provides article posting management for go-pugleaf
package postmgr

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// PosterManager manages the posting of articles to multiple providers
type PosterManager struct {
	db     *database.Database
	pools  []*nntp.Pool
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewPosterManager creates a new poster manager
func NewPosterManager(db *database.Database, pools []*nntp.Pool) *PosterManager {
	return &PosterManager{
		db:     db,
		pools:  pools,
		stopCh: make(chan struct{}),
	}
}

// Run starts the poster manager
func (pm *PosterManager) Run(limit int, shutdownChan <-chan struct{}, wg *sync.WaitGroup) {
	log.Printf("PosterManager: Starting with %d pools", len(pm.pools))
	defer wg.Done()
	for {
		select {
		case <-shutdownChan:
			log.Printf("PosterManager: Received shutdown signal")
			close(pm.stopCh)
			pm.wg.Wait()
			return
		case <-pm.stopCh:
			pm.wg.Wait()
			return
		default:
			// Check for pending posts
			done, err := pm.ProcessPendingPosts(limit)
			if err != nil {
				log.Printf("PosterManager: Error processing pending posts: %v", err)
			}
			if done == limit {
				time.Sleep(1 * time.Second)
			} else {
				time.Sleep(10 * time.Second)
			}
		}
	}
}

// Stop stops the poster manager
func (pm *PosterManager) Stop() {
	close(pm.stopCh)
	pm.wg.Wait()
}

// ProcessPendingPosts processes all pending posts in the queue
func (pm *PosterManager) ProcessPendingPosts(limit int) (int, error) {
	// Get pending posts from database
	entries, err := pm.db.GetPendingPostQueueEntries(limit)
	if err != nil {
		return 0, fmt.Errorf("failed to get pending posts: %v", err)
	}

	if len(entries) == 0 {
		log.Printf("No pending posts found")
		return 0, nil
	}

	log.Printf("Found %d pending posts to process", len(entries))

	// Process each entry
	for _, entry := range entries {
		select {
		case <-pm.stopCh:
			log.Printf("PosterManager: Stopping due to shutdown signal")
			return 0, nil
		default:
			if err := pm.processEntry(entry); err != nil {
				log.Printf("Failed to process entry %d (message: %s): %v", entry.ID, entry.MessageID, err)
			}
		}
	}

	return len(entries), nil
}

// processEntry processes a single post queue entry
func (pm *PosterManager) processEntry(entry database.PostQueueEntry) error {
	log.Printf("Processing post queue entry %d (message: %s)", entry.ID, entry.MessageID)

	// Get the newsgroup name by ID - we need to create this method or modify the query
	// For now, let's get all newsgroups and find the one with matching ID
	allNewsgroups, err := pm.db.MainDBGetAllNewsgroups()
	if err != nil {
		return fmt.Errorf("failed to get newsgroups: %v", err)
	}

	var newsgroup *models.Newsgroup
	for _, ng := range allNewsgroups {
		if ng.ID == entry.NewsgroupID {
			newsgroup = ng
			break
		}
	}

	if newsgroup == nil {
		return fmt.Errorf("newsgroup with ID %d not found", entry.NewsgroupID)
	}

	if !newsgroup.Active {
		log.Printf("Newsgroup %s is not active, skipping", newsgroup.Name)
		return pm.db.MarkPostQueueAsPostedToRemote(entry.ID)
	}

	// Get the article from the local database
	article, err := pm.getArticleByMessageID(entry.MessageID, newsgroup.Name)
	if err != nil {
		return fmt.Errorf("failed to get article %s: %v", entry.MessageID, err)
	}

	// Post to all available providers
	successCount := 0
	for _, pool := range pm.pools {
		if err := pm.postToProvider(article, pool); err != nil {
			log.Printf("Failed to post article %s to provider %s: %v",
				entry.MessageID, pool.Backend.Provider.Name, err)
		} else {
			successCount++
			log.Printf("Successfully posted article %s to provider %s",
				entry.MessageID, pool.Backend.Provider.Name)
		}
	}

	if successCount > 0 {
		log.Printf("Successfully posted article %s to %d/%d providers",
			entry.MessageID, successCount, len(pm.pools))
		return pm.db.MarkPostQueueAsPostedToRemote(entry.ID)
	}

	return fmt.Errorf("failed to post article %s to any provider", entry.MessageID)
}

// getArticleByMessageID retrieves an article from the local database
func (pm *PosterManager) getArticleByMessageID(messageID, newsgroup string) (*models.Article, error) {
	// Get group database connection
	groupDBs, err := pm.db.GetGroupDBs(newsgroup)
	if err != nil {
		return nil, err
	}
	defer groupDBs.Return(pm.db)

	// Get article by message ID using the database method (not groupDBs method)
	article, err := pm.db.GetArticleByMessageID(groupDBs, messageID)
	if err != nil {
		return nil, err
	}

	return article, nil
}

// postToProvider posts an article to a specific provider
func (pm *PosterManager) postToProvider(article *models.Article, pool *nntp.Pool) error {
	// Get connection from pool using the correct method name
	conn, err := pool.Get()
	if err != nil {
		return fmt.Errorf("failed to get connection: %v", err)
	}
	defer pool.Put(conn)

	// Use POST for posting articles (standard NNTP posting)
	responseCode, err := conn.PostArticle(article)
	if err != nil {
		return fmt.Errorf("failed to post article: %v", err)
	}

	// Check response code
	switch responseCode {
	case 240: // Article posted successfully
		return nil
	case 441: // Posting failed
		return fmt.Errorf("article posting failed (code 441)")
	default:
		return fmt.Errorf("unexpected response code: %d", responseCode)
	}
}
