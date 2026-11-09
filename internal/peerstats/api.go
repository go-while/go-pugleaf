// Package peerstats counts what peers send us and what we send them (plan peering-stats): per
// peer, per direction and per hour, kept in data/cfg/peer-stats.sq3, written by nntp-server and
// read by the web admin page, plus an optional log file in INN's innd/innfeed line format.
//
// This file is the contract the other files and packages code against; the implementation is in
// collector.go (Service), store.go (Open, OpenReadOnly) and innlog.go.
package peerstats

import (
	"path/filepath"
	"time"
)

// DefaultFile is the store file name below <data>/cfg.
const DefaultFile = "peer-stats.sq3"

// Path returns the store path of a data root.
func Path(dataDir string) string { return filepath.Join(dataDir, "cfg", DefaultFile) }

// Dir is the direction of a counter: In = a peer feeds us, Out = we feed a peer.
type Dir uint8

const (
	In  Dir = 1
	Out Dir = 2
)

// String returns "in" or "out" (the dir column of the store).
func (d Dir) String() string {
	switch d {
	case In:
		return "in"
	case Out:
		return "out"
	}
	return "?"
}

// UnknownPeer is the PeerRef of feed commands from a host that matches no peer row.
var UnknownPeer = PeerRef{ID: 0, Name: "unknown"}

// PeerRef names a peer in counters and log lines: its peers.id and peers.name.
type PeerRef struct {
	ID   int64
	Name string
}

// Counters are the numbers of one peer and direction. In Add they are deltas; in a Bucket totals.
//
// Incoming: Offered = IHAVE/CHECK/TAKETHIS (a CHECK and the TAKETHIS of the same id count once),
// Accepted = 235/239, Refused = 435/438, Rejected = 437/439, Duplicate = a transferred article
// that was a duplicate at store time, Deferred = 436/431, Denied = a feed command refused because
// the connection is no enabled peer (502/480), Missing = 0.
// Outgoing: Offered = CHECK/IHAVE sent, Accepted/Refused/Rejected/Deferred as answered by the
// peer, Missing = queued article no longer in the database, Denied = the peer refused us
// (502/480 on MODE STREAM/IHAVE/CHECK or the greeting).
// Bytes are wire sizes of the article (head + body). Conns counts connections that started,
// ConnSeconds the seconds of connections that ended (added on Closed).
type Counters struct {
	Offered     int64
	Accepted    int64
	Refused     int64
	Rejected    int64
	Duplicate   int64
	Deferred    int64
	Denied      int64
	Missing     int64
	AccBytes    int64
	RejBytes    int64
	DupBytes    int64
	Conns       int64
	ConnSeconds int64
}

// Add adds o to c.
func (c *Counters) Add(o Counters) {
	c.Offered += o.Offered
	c.Accepted += o.Accepted
	c.Refused += o.Refused
	c.Rejected += o.Rejected
	c.Duplicate += o.Duplicate
	c.Deferred += o.Deferred
	c.Denied += o.Denied
	c.Missing += o.Missing
	c.AccBytes += o.AccBytes
	c.RejBytes += o.RejBytes
	c.DupBytes += o.DupBytes
	c.Conns += o.Conns
	c.ConnSeconds += o.ConnSeconds
}

// IsZero reports whether every counter is 0.
func (c Counters) IsZero() bool { return c == Counters{} }

// Bucket is one hour of one peer and direction. Hour is the Unix time of the start of the hour (UTC).
type Bucket struct {
	Peer PeerRef
	Dir  Dir
	Hour int64
	Counters
}

// HourOf returns the Bucket.Hour that t falls into.
func HourOf(t time.Time) int64 { u := t.Unix(); return u - u%3600 }

// Collector receives the events. All methods are safe for concurrent use and never block on I/O;
// the implementation buffers and writes from its own goroutine.
type Collector interface {
	// Connected records the start of a connection (Conns++) and writes the INN "connected" line.
	// remote is the peer's address (in) or host:port we connected to (out).
	Connected(p PeerRef, d Dir, remote string)
	// Add adds delta to the current hour of p and d.
	Add(p PeerRef, d Dir, delta Counters)
	// Closed records the end of a connection: ConnSeconds += dur, and writes the INN "closed" line
	// (incoming, innd) or the per-host checkpoint (outgoing, innfeed) with conn, the totals of
	// that one connection (already counted through Add; Closed adds nothing else to the buckets).
	Closed(p PeerRef, d Dir, conn Counters, dur time.Duration)
	// SetQueue reports the outgoing queue depth of p (innfeed "queue" field, page column).
	SetQueue(p PeerRef, depth int64)
}

// Store keeps the hourly buckets. Open (read-write, nntp-server) and OpenReadOnly (web) return one.
type Store interface {
	// AddBuckets adds the counters of each bucket to the stored row (insert when missing).
	AddBuckets(b []Bucket) error
	// ReadRange returns the buckets with fromHour <= Hour < toHour, ordered by hour, peer, dir.
	ReadRange(fromHour, toHour int64) ([]Bucket, error)
	// Prune deletes the buckets with Hour < beforeHour and returns how many.
	Prune(beforeHour int64) (int64, error)
	Close() error
}

// Nop is a Collector that does nothing (no store configured, tests).
var Nop Collector = nop{}

type nop struct{}

func (nop) Connected(PeerRef, Dir, string)               {}
func (nop) Add(PeerRef, Dir, Counters)                   {}
func (nop) Closed(PeerRef, Dir, Counters, time.Duration) {}
func (nop) SetQueue(PeerRef, int64)                      {}
