package feed

// Tests of the sender's bookkeeping (plan nntp-peering, slice feed-sender): claiming,
// acknowledging, the retry backoff, the peer reload and the counters. The protocol itself is
// tested against a fake NNTP peer in sender_conn_test.go. No test asserts on wall-clock time:
// the sender's clock is a fake one and every wait is a hang guard (sndWait).

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSenderNeedsConfig: the three required fields are checked by NewSender.
func TestSenderNeedsConfig(t *testing.T) {
	q := newSndQueue()
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"no queue", Config{Peers: &sndPeerSource{}, Articles: newSndArticles()}},
		{"no peers", Config{Queue: q, Articles: newSndArticles()}},
		{"no articles", Config{Queue: q, Peers: &sndPeerSource{}}},
	} {
		if _, err := NewSender(tc.cfg); err == nil {
			t.Errorf("NewSender with %s: no error", tc.name)
		}
	}
	s, err := NewSender(Config{Queue: q, Peers: &sndPeerSource{}, Articles: newSndArticles()})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	// The defaults are filled and the real clock and dialer are installed.
	if s.cfg.PollInterval != DefaultPollInterval || s.cfg.Batch != DefaultBatch ||
		s.cfg.Lease != DefaultLease || s.cfg.Clock == nil || s.cfg.Dial == nil {
		t.Errorf("defaults not filled: %+v", s.cfg)
	}
	// Stop before Start returns at once; Start afterwards is refused.
	s.Stop()
	if err := s.Start(); err == nil {
		t.Error("Start after Stop: no error")
	}
}

// TestSenderBackoff: 30 s doubling per failed attempt up to 30 min.
func TestSenderBackoff(t *testing.T) {
	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute,
	}
	for i, w := range want {
		if got := backoffDelay(i, DefaultBackoffBase, DefaultBackoffMax); got != w {
			t.Errorf("backoffDelay(%d) = %v, want %v", i, got, w)
		}
	}
	if got := backoffDelay(1000, DefaultBackoffBase, DefaultBackoffMax); got != DefaultBackoffMax {
		t.Errorf("backoffDelay(1000) = %v, want %v", got, DefaultBackoffMax)
	}
	if got := backoffDelay(-1, 0, 0); got != DefaultBackoffBase {
		t.Errorf("backoffDelay(-1, 0, 0) = %v, want %v", got, DefaultBackoffBase)
	}
}

// TestSenderAckAndDefer: 239 and 438 finish an item, 431 defers it at now + the backoff of its
// own attempt count, and the counters match.
func TestSenderAckAndDefer(t *testing.T) {
	f := newSndFixture(t, nil)
	const group = "feed.test"
	accept, refuse, retry := "<ack1@test.invalid>", "<ack2@test.invalid>", "<ack3@test.invalid>"
	for _, id := range []string{accept, refuse, retry} {
		f.arts.add(group, sndArticle(id, group))
	}
	if err := f.q.Enqueue([]Item{
		{PeerID: 1, MessageID: accept, Newsgroup: group},
		{PeerID: 1, MessageID: refuse, Newsgroup: group},
		{PeerID: 1, MessageID: retry, Newsgroup: group, Attempts: 2},
	}); err != nil {
		t.Fatal(err)
	}
	f.dial.newT = func() *sndTransport {
		return &sndTransport{reply: func(_ int, arts []Outgoing) ([]Result, int, error) {
			res := make([]Result, 0, len(arts))
			for _, a := range arts {
				switch a.Article.MessageID {
				case accept:
					res = append(res, Result{MessageID: accept, Code: 239, Outcome: OutcomeDone})
				case refuse:
					res = append(res, Result{MessageID: refuse, Code: 438, Outcome: OutcomeDone})
				default:
					res = append(res, Result{MessageID: retry, Code: 431, Outcome: OutcomeDefer, Err: "CHECK 431"})
				}
			}
			return res, len(arts), nil
		}}
	}
	f.peers.set(sndPeer(1, "ackpeer"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}

	sndWaitFor(t, "two items done", func() bool { return len(f.q.doneIDs()) == 2 })
	sndWaitFor(t, "one item deferred", func() bool { return len(f.q.deferred()) == 1 })

	done := strings.Join(f.q.doneIDs(), " ")
	if !strings.Contains(done, accept) || !strings.Contains(done, refuse) {
		t.Errorf("done = %q, want %s and %s", done, accept, refuse)
	}
	d := f.q.deferred()[0]
	if d.MessageID != retry || d.PeerID != 1 {
		t.Errorf("deferred %+v, want %s of peer 1", d, retry)
	}
	// Attempts was 2, so the next attempt is 30 s doubled twice after the sender's clock.
	if want := f.clock.Now().Add(2 * time.Minute); !d.Next.Equal(want) {
		t.Errorf("next attempt = %v, want %v", d.Next, want)
	}
	if d.Err != "CHECK 431" {
		t.Errorf("last error = %q, want %q", d.Err, "CHECK 431")
	}

	f.s.Stop()
	st, ok := f.q.storedStats(1)
	if !ok {
		t.Fatal("no counters stored on Stop")
	}
	if st.Offered != 3 || st.Accepted != 1 || st.Refused != 1 || st.Deferred != 1 || st.Rejected != 0 {
		t.Errorf("counters = %+v, want offered 3 accepted 1 refused 1 deferred 1", st)
	}
	if st.QueueDepth != 1 {
		t.Errorf("stored queue depth = %d, want 1", st.QueueDepth)
	}
	if !st.UpdatedAt.Equal(f.clock.Now()) {
		t.Errorf("UpdatedAt = %v, want the sender's clock %v", st.UpdatedAt, f.clock.Now())
	}
}

// TestSenderMissingArticle: an article that is not stored any more finishes its queue item and
// no connection is opened for it.
func TestSenderMissingArticle(t *testing.T) {
	f := newSndFixture(t, nil)
	const group = "feed.test"
	gone := "<gone@test.invalid>"
	if err := f.q.Enqueue([]Item{{PeerID: 1, MessageID: gone, Newsgroup: group}}); err != nil {
		t.Fatal(err)
	}
	f.peers.set(sndPeer(1, "gonepeer"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the missing article to be dropped", func() bool { return len(f.q.doneIDs()) == 1 })
	if got := f.q.doneIDs()[0]; got != gone {
		t.Errorf("done = %q, want %q", got, gone)
	}
	if dials, _, _ := f.dial.counts(); dials != 0 {
		t.Errorf("dials = %d, want 0: nothing was sendable", dials)
	}
	if len(f.q.deferred()) != 0 {
		t.Errorf("deferred = %+v, want none", f.q.deferred())
	}
}

// TestSenderArticleReadErrorDefers: a newsgroup that cannot be read is temporary.
func TestSenderArticleReadErrorDefers(t *testing.T) {
	f := newSndFixture(t, nil)
	f.arts.err = errors.New("group db is busy")
	if err := f.q.Enqueue([]Item{{PeerID: 1, MessageID: "<busy@test.invalid>", Newsgroup: "feed.test"}}); err != nil {
		t.Fatal(err)
	}
	f.peers.set(sndPeer(1, "busypeer"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the item to be deferred", func() bool { return len(f.q.deferred()) > 0 })
	if ids := f.q.doneIDs(); len(ids) != 0 {
		t.Errorf("done = %v, want none", ids)
	}
	if d := f.q.deferred()[0]; !strings.Contains(d.Err, "group db is busy") {
		t.Errorf("last error = %q, want the read error", d.Err)
	}
}

// TestSenderConnectFailureDefers: a peer we cannot reach keeps its queue, every item gets one
// more attempt and the sender tries again.
func TestSenderConnectFailureDefers(t *testing.T) {
	f := newSndFixture(t, func(c *Config) {
		c.BackoffBase = time.Millisecond // the retry wait of the worker, not an assertion
		c.BackoffMax = time.Millisecond
	})
	const group = "feed.test"
	id := "<down@test.invalid>"
	f.arts.add(group, sndArticle(id, group))
	if err := f.q.Enqueue([]Item{{PeerID: 1, MessageID: id, Newsgroup: group}}); err != nil {
		t.Fatal(err)
	}
	f.dial.setErr(errors.New("connection refused"))
	f.peers.set(sndPeer(1, "downpeer"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the item to be deferred", func() bool { return len(f.q.deferred()) > 0 })
	// The deferred item is due only after its backoff: move the sender's clock past it.
	f.clock.Advance(time.Minute)
	sndWaitFor(t, "a second connect attempt", func() bool { dials, _, _ := f.dial.counts(); return dials >= 2 })
	d := f.q.deferred()[0]
	if !strings.Contains(d.Err, "connection refused") {
		t.Errorf("last error = %q, want the connect error", d.Err)
	}
	if ids := f.q.doneIDs(); len(ids) != 0 {
		t.Errorf("done = %v, want none: nothing was sent", ids)
	}
	f.s.Stop()
	if st, ok := f.q.storedStats(1); !ok || st.Accepted != 0 || st.Deferred == 0 {
		t.Errorf("counters = %+v (stored %v), want deferred > 0 and accepted 0", st, ok)
	}
}

// TestSenderDropPeer: a peer that loses out_enabled (or disappears) stops being fed and its
// queue and counters are dropped; a peer list that cannot be read drops nothing.
func TestSenderDropPeer(t *testing.T) {
	f := newSndFixture(t, nil)
	peer := sndPeer(7, "droppeer")
	f.peers.set(peer)
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the peer feed to start", func() bool {
		f.s.mu.Lock()
		defer f.s.mu.Unlock()
		return len(f.s.peers) == 1
	})

	// A read error must not drop anything.
	f.peers.mu.Lock()
	f.peers.err = errors.New("main db is busy")
	f.peers.mu.Unlock()
	reads := f.peers.readCount()
	sndWaitFor(t, "another peer list read", func() bool { return f.peers.readCount() > reads+1 })
	if dropped := f.q.droppedPeers(); len(dropped) != 0 {
		t.Fatalf("dropped %v on a peer list error, want none", dropped)
	}
	f.peers.mu.Lock()
	f.peers.err = nil
	f.peers.mu.Unlock()

	// out_enabled cleared: the feed stops and the queue goes.
	off := *peer
	off.OutEnabled = false
	f.peers.set(&off)
	sndWaitFor(t, "the peer to be dropped", func() bool { return len(f.q.droppedPeers()) == 1 })
	if got := f.q.droppedPeers()[0]; got != 7 {
		t.Errorf("DropPeer(%d), want 7", got)
	}
	sndWaitFor(t, "the peer feed to stop", func() bool {
		f.s.mu.Lock()
		defer f.s.mu.Unlock()
		return len(f.s.peers) == 0
	})
}

// TestSenderRestartsOnChange: a changed out_host restarts the feed of that peer (the old
// connection is closed) without dropping its queue.
func TestSenderRestartsOnChange(t *testing.T) {
	f := newSndFixture(t, nil)
	const group = "feed.test"
	id := "<chg@test.invalid>"
	f.arts.add(group, sndArticle(id, group))
	if err := f.q.Enqueue([]Item{{PeerID: 3, MessageID: id, Newsgroup: group}}); err != nil {
		t.Fatal(err)
	}
	peer := sndPeer(3, "chgpeer")
	f.peers.set(peer)
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the first connection", func() bool { dials, _, _ := f.dial.counts(); return dials >= 1 })

	moved := *peer
	moved.OutHost = "other.invalid"
	f.peers.set(&moved)
	sndWaitFor(t, "the restarted feed", func() bool {
		f.s.mu.Lock()
		defer f.s.mu.Unlock()
		pf := f.s.peers[3]
		return pf != nil && pf.peer.OutHost == "other.invalid"
	})
	if dropped := f.q.droppedPeers(); len(dropped) != 0 {
		t.Errorf("dropped %v on a settings change, want none", dropped)
	}
	// The restarted feed carries the counters of the stopped one.
	f.s.Stop()
	if st, ok := f.q.storedStats(3); !ok || st.Accepted != 1 || st.Offered != 1 {
		t.Errorf("counters after the restart = %+v (stored %v), want offered 1 accepted 1", st, ok)
	}
}

// TestSenderMaxConns: a peer with max_conns N never has more than N connections open.
func TestSenderMaxConns(t *testing.T) {
	const conns = 3
	f := newSndFixture(t, func(c *Config) { c.Batch = 1 })
	const group = "feed.test"
	var items []Item
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("<mc%d@test.invalid>", i)
		f.arts.add(group, sndArticle(id, group))
		items = append(items, Item{PeerID: 1, MessageID: id, Newsgroup: group})
	}
	if err := f.q.Enqueue(items); err != nil {
		t.Fatal(err)
	}
	peer := sndPeer(1, "mcpeer")
	peer.MaxConns = conns
	f.peers.set(peer)
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "all items to be sent", func() bool { return len(f.q.doneIDs()) == len(items) })
	dials, _, maxLive := f.dial.counts()
	if maxLive > conns {
		t.Errorf("%d connections open at once, want at most %d", maxLive, conns)
	}
	if dials > conns {
		t.Errorf("dials = %d, want at most %d: the connections are kept", dials, conns)
	}
	f.s.Stop()
	_, live, _ := f.dial.counts()
	if live != 0 {
		t.Errorf("%d connections still open after Stop", live)
	}
}

// TestSenderStopUnblocksSend: Stop closes the open connections, so a worker waiting for a
// peer's answer returns, the item is deferred and the counters are written.
func TestSenderStopUnblocksSend(t *testing.T) {
	f := newSndFixture(t, nil)
	const group = "feed.test"
	id := "<stop@test.invalid>"
	f.arts.add(group, sndArticle(id, group))
	if err := f.q.Enqueue([]Item{{PeerID: 1, MessageID: id, Newsgroup: group}}); err != nil {
		t.Fatal(err)
	}
	// Each connection blocks in Send until it is closed.
	f.dial.newT = func() *sndTransport { return &sndTransport{block: make(chan struct{})} }
	f.peers.set(sndPeer(1, "stoppeer"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the blocked send", func() bool { _, live, _ := f.dial.counts(); return live == 1 })

	stopped := make(chan struct{})
	go func() { f.s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(sndWait):
		t.Fatal("Stop did not return: a worker is still waiting for the peer")
	}
	if _, ok := f.q.storedStats(1); !ok {
		t.Error("no counters written on Stop")
	}
	if len(f.q.deferred()) != 1 {
		t.Errorf("deferred = %+v, want the item that was in flight", f.q.deferred())
	}
	if _, live, _ := f.dial.counts(); live != 0 {
		t.Error("a connection is still open after Stop")
	}
}

// TestSenderPrune: the sender calls the queue's optional Prune once per peer reload.
func TestSenderPrune(t *testing.T) {
	f := newSndFixture(t, nil)
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "two prune calls", func() bool { return f.q.pruneCalls() >= 2 })
}

// TestSenderClaimErrorKeepsRunning: a queue that cannot be claimed from is logged and retried,
// and the error is kept as the peer's last error.
func TestSenderClaimErrorKeepsRunning(t *testing.T) {
	f := newSndFixture(t, nil)
	f.q.mu.Lock()
	f.q.claimErr = errors.New("queue is locked")
	f.q.mu.Unlock()
	f.peers.set(sndPeer(1, "claimpeer"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "repeated claims", func() bool {
		f.q.mu.Lock()
		defer f.q.mu.Unlock()
		return f.q.claims >= 3
	})
	f.s.Stop()
	st, ok := f.q.storedStats(1)
	if !ok || !strings.Contains(st.LastError, "queue is locked") {
		t.Errorf("last error = %q (stored %v), want the claim error", st.LastError, ok)
	}
}

// TestSenderSkipsPeerWithoutHost: out_enabled without out_host sends nothing and keeps the
// queue (the admin can still fix the host).
func TestSenderSkipsPeerWithoutHost(t *testing.T) {
	f := newSndFixture(t, nil)
	peer := sndPeer(1, "nohost")
	peer.OutHost = ""
	f.peers.set(peer)
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "two peer list reads", func() bool { return f.peers.readCount() >= 2 })
	f.s.mu.Lock()
	running := len(f.s.peers)
	f.s.mu.Unlock()
	if running != 0 {
		t.Errorf("%d feed(s) running for a peer without out_host", running)
	}
	if dropped := f.q.droppedPeers(); len(dropped) != 0 {
		t.Errorf("dropped %v, want none: the queue is kept", dropped)
	}
}
