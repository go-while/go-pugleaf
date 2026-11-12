package main

import (
	"fmt"
	"log"
	"sync"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/feed"
)

// Web posts for the peers (outgoing transit feed): the post queue worker files a web post
// through this process's batch writer, so the feed hook must be installed here as well.
// The enqueuer writes into the same queue file as nntp-server's (data/cfg/feed-queue.sq3,
// SQLite WAL, so both processes may write); nntp-server's sender delivers the rows.

var webFeed struct {
	mu  sync.Mutex
	q   *feed.SQLiteQueue
	enq *feed.Enqueuer
}

// startWebFeed opens the feed queue and installs the enqueuer as the batch writer's feed hook.
func startWebFeed(db *database.Database, dataDir string) error {
	q, err := feed.Open(feed.QueuePath(dataDir))
	if err != nil {
		return fmt.Errorf("feed queue: %w", err)
	}
	enq := feed.NewEnqueuer(q, db, feed.EnqueuerConfig{})
	enq.Start()
	database.SetFeedHook(enq.Hook())
	webFeed.mu.Lock()
	webFeed.q, webFeed.enq = q, enq
	webFeed.mu.Unlock()
	log.Printf("[WEB]: web posts are queued for the peers (%s)", q.Path())
	return nil
}

// stopWebFeed removes the hook, stops the enqueuer (it writes what it still holds) and closes
// the queue. teardownDB calls it after the batch writers finished and before the database
// shuts down; it does nothing when the feed was not started, and runs once.
func stopWebFeed() {
	webFeed.mu.Lock()
	q, enq := webFeed.q, webFeed.enq
	webFeed.q, webFeed.enq = nil, nil
	webFeed.mu.Unlock()
	if enq == nil {
		return
	}
	database.SetFeedHook(nil)
	enq.Stop()
	if err := q.Close(); err != nil {
		log.Printf("[WEB]: closing the feed queue: %v", err)
	}
}
