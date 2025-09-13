package processor

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// PostQueueWorker processes articles from the web posting queue
type PostQueueWorker struct {
	processor *Processor
	stopCh    chan struct{}
}

// NewPostQueueWorker creates a new post queue worker
func (processor *Processor) NewPostQueueWorker() *PostQueueWorker {
	return &PostQueueWorker{
		processor: processor,
		stopCh:    make(chan struct{}),
	}
}

// Start begins processing articles from the post queue
func (w *PostQueueWorker) Start() {
	log.Printf("PostQueueWorker: Starting worker")
	go w.processLoop()
}

// Stop gracefully stops the worker
func (w *PostQueueWorker) Stop() {
	log.Printf("PostQueueWorker: Stopping worker")
	close(w.stopCh)
}

// processLoop is the main processing loop that reads from the queue
func (w *PostQueueWorker) processLoop() {
	for {
		select {
		case <-w.stopCh:
			log.Printf("PostQueueWorker: Worker stopped")
			return

		case article := <-models.PostQueueChannel:
			if article == nil {
				log.Printf("PostQueueWorker: Received nil article, skipping")
				continue
			}

			log.Printf("PostQueueWorker: Processing article %s", article.MessageID)
			err := w.processArticle(article)
			if err != nil {
				log.Printf("PostQueueWorker: Error processing article %s: %v", article.MessageID, err)
				// TODO: Implement retry logic or dead letter queue
			} else {
				log.Printf("PostQueueWorker: Successfully processed article %s", article.MessageID)
			}

		case <-time.After(30 * time.Second):
			// Periodic heartbeat to ensure the worker is alive
			// Note: len() on channels is safe and atomic
			queueLen := len(models.PostQueueChannel)
			if queueLen > 0 {
				log.Printf("PostQueueWorker: Worker alive, queue length: %d", queueLen)
			}
		}
	}
}

// processArticle processes a single article from the queue
func (w *PostQueueWorker) processArticle(article *models.Article) error {
	// Get newsgroups from the article's headers
	newsgroupsHeader, exists := article.Headers["newsgroups"]
	if !exists || len(newsgroupsHeader) == 0 {
		log.Printf("PostQueueWorker: Article %s has no newsgroups header", article.MessageID)
		return nil
	}

	// Parse newsgroups (comma-separated)
	newsgroups := parseNewsgroups(newsgroupsHeader[0])
	if len(newsgroups) == 0 {
		log.Printf("PostQueueWorker: Article %s has no valid newsgroups", article.MessageID)
		return nil
	}

	log.Printf("PostQueueWorker: Processing article %s for newsgroups: %v", article.MessageID, newsgroups)
	errs := 0
	// Process the article for each newsgroup
	for _, newsgroup := range newsgroups {
		err := w.processArticleForNewsgroup(article, newsgroup)
		if err != nil {
			errs++
			log.Printf("PostQueueWorker: Error processing article %s for newsgroup %s: %v",
				article.MessageID, newsgroup, err)
			// Continue with other newsgroups even if one fails
		}
	}
	// Record in post_queue table for tracking
	// Get newsgroup ID
	newsgroupModel, err := w.processor.DB.GetNewsgroupByName(newsgroups[0])
	if err != nil {
		log.Printf("Warning: Failed to get newsgroup %s for post queue recording: %v", newsgroups[0], err)
		return fmt.Errorf("failed to get newsgroup %s: %v", newsgroups[0], err)
	}

	// Insert into post_queue table
	err = w.processor.DB.InsertPostQueueEntry(newsgroupModel.ID, article.MessageID)
	if err != nil {
		log.Printf("Warning: Failed to insert post_queue entry for newsgroup %s, message_id %s: %v",
			newsgroups[0], article.MessageID, err)
	}
	return nil
}

// processArticleForNewsgroup processes an article for a specific newsgroup
func (w *PostQueueWorker) processArticleForNewsgroup(article *models.Article, newsgroup string) error {
	// Get newsgroup from database
	ng, err := w.processor.DB.GetNewsgroupByName(newsgroup)
	if err != nil {
		return err
	}

	if !ng.Active {
		log.Printf("PostQueueWorker: Newsgroup %s is not active, skipping", newsgroup)
		return nil
	}

	// Get group database connection
	groupDBs, err := w.processor.DB.GetGroupDBs(newsgroup)
	if err != nil {
		return err
	}
	defer groupDBs.Return(w.processor.DB)

	// Use the existing threading function to process the article
	// This will handle all the threading logic, database insertion, etc.
	//log.Printf("PostQueueWorker: Threading article %s in newsgroup %s", article.MessageID, newsgroup)

	// Process through the threading system using the processor's method
	_, err = w.processor.processArticle(article, newsgroup, true)
	if err != nil {
		return err
	}

	log.Printf("PostQueueWorker: sent article %s in newsgroup %s to processArticle", article.MessageID, newsgroup)
	return nil
}

// parseNewsgroups parses a comma-separated list of newsgroups
func parseNewsgroups(newsgroupsStr string) []string {
	if newsgroupsStr == "" {
		return nil
	}

	// Split by comma and clean up
	parts := strings.Split(newsgroupsStr, ",")
	var newsgroups []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			newsgroups = append(newsgroups, part)
		}
	}
	return newsgroups
}
