package feed

// Tests of the outgoing peering statistics (plan peering-stats, slice stats-in): what the
// sender reports to the peerstats.Collector while it drains a queue to a peer. The per-peer
// counters of feed_stats are tested in sender_test.go and stay as they are.
//
// Identifiers start with sps / TestFeedPeerStats. No test asserts on wall-clock time: the
// sender's clock is the fake one and every wait is the hang guard sndWait.

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/peerstats"
	"github.com/go-while/nntp-testkit/fakeserver"
)

// spsEvent is one recorded Connected or Closed call.
type spsEvent struct {
	Peer   peerstats.PeerRef
	Dir    peerstats.Dir
	Remote string
	Conn   peerstats.Counters
	Dur    time.Duration
}

// spsCollector records every call of the Collector interface.
type spsCollector struct {
	mu     sync.Mutex
	conns  []spsEvent
	closes []spsEvent
	adds   []peerstats.Bucket
	queues []int64
}

func newSpsCollector() *spsCollector { return &spsCollector{} }

func (c *spsCollector) Connected(p peerstats.PeerRef, d peerstats.Dir, remote string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conns = append(c.conns, spsEvent{Peer: p, Dir: d, Remote: remote})
}

func (c *spsCollector) Add(p peerstats.PeerRef, d peerstats.Dir, delta peerstats.Counters) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adds = append(c.adds, peerstats.Bucket{Peer: p, Dir: d, Counters: delta})
}

func (c *spsCollector) Closed(p peerstats.PeerRef, d peerstats.Dir, conn peerstats.Counters, dur time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes = append(c.closes, spsEvent{Peer: p, Dir: d, Conn: conn, Dur: dur})
}

func (c *spsCollector) SetQueue(p peerstats.PeerRef, depth int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queues = append(c.queues, depth)
}

// total sums the Add deltas of one direction, whatever the peer, and also returns the refs
// the deltas were counted for.
func (c *spsCollector) total(d peerstats.Dir) (peerstats.Counters, []peerstats.PeerRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var (
		out  peerstats.Counters
		refs []peerstats.PeerRef
		seen = make(map[peerstats.PeerRef]bool)
	)
	for _, a := range c.adds {
		if a.Dir != d {
			continue
		}
		out.Add(a.Counters)
		if !seen[a.Peer] {
			seen[a.Peer] = true
			refs = append(refs, a.Peer)
		}
	}
	return out, refs
}

func (c *spsCollector) connected() []spsEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]spsEvent(nil), c.conns...)
}

func (c *spsCollector) closed() []spsEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]spsEvent(nil), c.closes...)
}

func (c *spsCollector) queueDepths() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.queues...)
}

// TestFeedPeerStatsOffered: the sender reports one offer per article, the peer's verdicts
// with their octets, and one Connected and Closed per connection.
func TestFeedPeerStatsOffered(t *testing.T) {
	have := "<sps-have@test.invalid>"
	reject := "<sps-rej@test.invalid>"
	s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(&fakeserver.PeerOptions{
		Have:   map[string]bool{have: true},
		Reject: map[string]bool{reject: true},
	})})
	col := newSpsCollector()
	f := newSndFixture(t, func(c *Config) {
		c.Dial = nil // the real DialPeer
		c.Hostname = sndIdentity
		c.ConnectTimeout = sndWait
		c.Stats = col
	})
	ids := []string{"<sps-ok@test.invalid>", have, reject}
	var items []Item
	for _, id := range ids {
		f.arts.add(sndGroup, sndArticle(id, sndGroup))
		items = append(items, Item{PeerID: 1, MessageID: id, Newsgroup: sndGroup})
	}
	if err := f.q.Enqueue(items); err != nil {
		t.Fatal(err)
	}
	row := sndRowFor(s, func(p *models.Peer) { p.Name = "spspeer" })
	f.peers.set(row)
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the queue to be drained", func() bool { return len(f.q.doneIDs()) == len(ids) })
	f.s.Stop()

	want := peerstats.PeerRef{ID: row.ID, Name: row.Name}
	got, refs := col.total(peerstats.Out)
	if len(refs) != 1 || refs[0] != want {
		t.Fatalf("counted for %+v, want only %+v", refs, want)
	}
	if got.Offered != 3 || got.Accepted != 1 || got.Refused != 1 || got.Rejected != 1 {
		t.Errorf("counters = %+v, want offered 3, accepted 1, refused 1, rejected 1", got)
	}
	if got.AccBytes <= 0 || got.RejBytes <= 0 {
		t.Errorf("counters = %+v, want octets for the accepted and the rejected article", got)
	}
	conns := col.connected()
	if len(conns) == 0 {
		t.Fatal("no Connected event")
	}
	for _, e := range conns {
		if e.Peer != want || e.Dir != peerstats.Out {
			t.Errorf("Connected = %+v, want an Out event of %+v", e, want)
		}
		if e.Remote != fmt.Sprintf("%s:%d", row.OutHost, row.OutPort) {
			t.Errorf("Connected remote = %q, want %s:%d", e.Remote, row.OutHost, row.OutPort)
		}
	}
	// Every connection that was opened is closed again, and the totals of the one that sent
	// carry the verdicts.
	closes := col.closed()
	if len(closes) != len(conns) {
		t.Errorf("%d Closed event(s) for %d Connected", len(closes), len(conns))
	}
	var sent peerstats.Counters
	for _, e := range closes {
		if e.Peer != want || e.Dir != peerstats.Out {
			t.Errorf("Closed = %+v, want an Out event of %+v", e, want)
		}
		sent.Add(e.Conn)
	}
	if sent.Offered != got.Offered || sent.Accepted != got.Accepted {
		t.Errorf("Closed totals = %+v, want the counters of the run %+v", sent, got)
	}
	// The queue depth of the final flush is reported.
	if depths := col.queueDepths(); len(depths) == 0 || depths[len(depths)-1] != 0 {
		t.Errorf("queue depths = %v, want the drained queue (0) last", depths)
	}
}

// TestFeedPeerStatsDenied: a peer that refuses our feed (480 because it wants a login we do
// not send) counts as Denied, so the backlog has a visible reason.
func TestFeedPeerStatsDenied(t *testing.T) {
	s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(&fakeserver.PeerOptions{
		User: "peeruser", Pass: "peerpass",
	})})
	col := newSpsCollector()
	f := newSndFixture(t, func(c *Config) {
		c.Dial = nil // the real DialPeer
		c.Hostname = sndIdentity
		c.ConnectTimeout = sndWait
		c.Stats = col
	})
	id := "<sps-denied@test.invalid>"
	f.arts.add(sndGroup, sndArticle(id, sndGroup))
	if err := f.q.Enqueue([]Item{{PeerID: 1, MessageID: id, Newsgroup: sndGroup}}); err != nil {
		t.Fatal(err)
	}
	// No out_username: the peer refuses every feed command with 480.
	f.peers.set(sndRowFor(s, func(p *models.Peer) { p.Name = "spsdenied" }))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the article to be deferred", func() bool { return len(f.q.deferred()) > 0 })
	sndWaitFor(t, "the refusal to be counted", func() bool {
		got, _ := col.total(peerstats.Out)
		return got.Denied > 0
	})
	f.s.Stop()

	got, _ := col.total(peerstats.Out)
	if got.Accepted != 0 {
		t.Errorf("counters = %+v, want nothing accepted", got)
	}
	if got.Deferred == 0 {
		t.Errorf("counters = %+v, want the article deferred as well", got)
	}
	if len(f.q.doneIDs()) != 0 {
		t.Errorf("done = %v, want the article kept in the queue", f.q.doneIDs())
	}
}

// TestFeedPeerStatsMissing: an article that is gone from the database is Missing.
func TestFeedPeerStatsMissing(t *testing.T) {
	col := newSpsCollector()
	f := newSndFixture(t, func(c *Config) { c.Stats = col })
	gone := "<sps-gone@test.invalid>"
	if err := f.q.Enqueue([]Item{{PeerID: 1, MessageID: gone, Newsgroup: sndGroup}}); err != nil {
		t.Fatal(err)
	}
	f.peers.set(sndPeer(1, "spsgone"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the missing article to be dropped", func() bool { return len(f.q.doneIDs()) == 1 })
	sndWaitFor(t, "the missing article to be counted", func() bool {
		got, _ := col.total(peerstats.Out)
		return got.Missing > 0
	})
	f.s.Stop()
	got, _ := col.total(peerstats.Out)
	if got.Offered != 0 || got.Accepted != 0 {
		t.Errorf("counters = %+v, want nothing offered: the article never reached the wire", got)
	}
	if dials, _, _ := f.dial.counts(); dials != 0 {
		t.Errorf("dials = %d, want 0", dials)
	}
	if conns := col.connected(); len(conns) != 0 {
		t.Errorf("Connected = %+v, want none: no connection was opened", conns)
	}
}

// TestFeedPeerStatsNopDefault: a Config without Stats counts nothing and changes nothing.
func TestFeedPeerStatsNopDefault(t *testing.T) {
	f := newSndFixture(t, nil)
	if f.s.cfg.Stats == nil {
		t.Fatal("Config.Stats is nil after withDefaults, want peerstats.Nop")
	}
	id := "<sps-nop@test.invalid>"
	f.arts.add(sndGroup, sndArticle(id, sndGroup))
	if err := f.q.Enqueue([]Item{{PeerID: 1, MessageID: id, Newsgroup: sndGroup}}); err != nil {
		t.Fatal(err)
	}
	f.peers.set(sndPeer(1, "spsnop"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the article to be sent", func() bool { return len(f.q.doneIDs()) == 1 })
}

// TestFeedPeerStatsDeniedError: only a 480 or 502 answer marks an error as the peer refusing
// us; every other failure stays an ordinary connection error.
func TestFeedPeerStatsDeniedError(t *testing.T) {
	base := errors.New("feed: peer x: CHECK <a@b> answered 480")
	for _, tc := range []struct {
		code int
		want bool
	}{{480, true}, {502, true}, {500, false}, {501, false}, {403, false}, {0, false}} {
		err := denied(tc.code, base)
		if got := isPeerDenied(err); got != tc.want {
			t.Errorf("isPeerDenied(denied(%d, err)) = %v, want %v", tc.code, got, tc.want)
		}
		if err.Error() != base.Error() {
			t.Errorf("denied(%d, err) changed the message to %q", tc.code, err.Error())
		}
		if !errors.Is(err, base) {
			t.Errorf("denied(%d, err) does not unwrap to the original error", tc.code)
		}
	}
	if denied(480, nil) != nil {
		t.Error("denied(480, nil) is not nil")
	}
	if isPeerDenied(nil) {
		t.Error("isPeerDenied(nil) is true")
	}
}
