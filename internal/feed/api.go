// Package feed is the outgoing transit feed of the NNTP server (plan nntp-peering, waves 2-3):
// articles accepted from a peer or posted locally are queued per peer (queue.go, slice
// feed-queue) and sent to each peer over persistent streaming connections (sender.go, slice
// feed-sender). This file is the contract both slices build against.
package feed

import (
	"time"
)

// DefaultQueueFile is the queue's SQLite file name under the data root's cfg directory
// (data/cfg/feed-queue.sq3): the hot queue stays out of the main DB.
const DefaultQueueFile = "feed-queue.sq3"

// Item is one queued article for one peer.
type Item struct {
	PeerID    int64
	MessageID string
	Newsgroup string // a group the article is stored in (where the sender reads it from)
	Attempts  int    // failed send attempts so far
	LastError string
}

// Stats are a peer's outgoing counters. The sender adds to them and writes them to the queue
// file (at most once a minute and on shutdown) so another process (the web admin page) can read
// them; QueueDepth is filled by ReadStats from the queue itself.
type Stats struct {
	PeerID     int64
	Offered    int64 // CHECK or IHAVE sent
	Accepted   int64 // 238+239 / 335+235
	Refused    int64 // 438 / 435: the peer has it
	Rejected   int64 // 439 / 437: the peer does not want it
	Deferred   int64 // 431 / 436 / 400 / network errors: retried later
	QueueDepth int64
	LastError  string
	UpdatedAt  time.Time
}

// Queue is the durable per-peer outgoing queue (implemented in queue.go).
// All methods are safe for concurrent use.
type Queue interface {
	// Enqueue adds items; an item already queued for that peer is left as it is.
	Enqueue(items []Item) error
	// Claim returns up to max items of peerID that are due (next attempt <= now) and leases
	// them: their next attempt moves to now+lease, so a crashed sender's rows come back by
	// themselves. Oldest first.
	Claim(peerID int64, max int, now time.Time, lease time.Duration) ([]Item, error)
	// Done deletes items that are finished (sent, or refused/rejected by the peer).
	Done(peerID int64, messageIDs []string) error
	// Defer records a failed attempt: attempts+1, last_error, next attempt at next.
	Defer(peerID int64, messageID string, lastErr string, next time.Time) error
	// Depth returns how many items are queued for peerID.
	Depth(peerID int64) (int64, error)
	// DropPeer deletes every item and the stats of a peer (peer deleted or out feed disabled).
	DropPeer(peerID int64) error
	// WriteStats stores a peer's counters (replacing the stored ones).
	WriteStats(s Stats) error
	// ReadStats returns the stored counters of every peer that has any, with QueueDepth filled.
	ReadStats() (map[int64]Stats, error)
	// Close closes the queue file.
	Close() error
}
