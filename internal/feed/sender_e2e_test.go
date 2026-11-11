package feed

// End-to-end tests of the outgoing feed with the real dialer and the real SQLite queue against
// nntp-testkit's fake transit peer: what a 400 ("the peer is going away") does to a batch, what
// a peer that drops mid-batch leaves in the queue, a peer that stalls, and a small batch
// window. Sources: diablo-nntp's dnewslink_defer_test.go (D-66), dnewslink_flags_test.go
// (D-73), refile_test.go (D-63), dnewslinkterm_test.go and dnewslink_smallmax_test.go (D-68).
//
// Identifiers start with fe2e so they never collide with the other tests of this package.
// No test asserts on wall-clock time: every wait is the hang guard sndWait
// (PUGLEAF_TEST_DEADLINE), and the few durations that are configured bound the sender itself,
// they are not assertions.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	testkit "github.com/go-while/nntp-testkit"
	"github.com/go-while/nntp-testkit/fakeserver"
)

// fe2eStallConnect bounds the connect of the stalled-peer test. A connect that is already
// running cannot be interrupted, so it is also how long that test's Stop may take (see
// Sender.Stop): a bound of the sender, not an assertion on time.
const fe2eStallConnect = 200 * time.Millisecond

// fe2eFixture is a sender with the real DialPeer and the real queue file in a temp dir, so the
// rows left behind after a run are the ones a peer would really leave.
type fe2eFixture struct {
	s     *Sender
	q     *SQLiteQueue
	peers *sndPeerSource
	arts  *sndArticles
}

// fe2eSender wires a sender to the fake peer srv. The retry backoff is as short as the queue
// can express (next_attempt has a one-second granularity), so a test that waits for the second
// connection does not wait for a real backoff.
func fe2eSender(t *testing.T, tune func(*Config)) *fe2eFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feed-queue.sq3")
	q, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() {
		if err := q.Close(); err != nil {
			t.Errorf("queue Close: %v", err)
		}
	})
	f := &fe2eFixture{q: q, peers: &sndPeerSource{}, arts: newSndArticles()}
	cfg := Config{
		Queue:          q,
		Peers:          f.peers,
		Articles:       f.arts,
		Hostname:       sndIdentity,
		PollInterval:   time.Millisecond,
		ReloadInterval: time.Hour, // the peer list is read once, by Start
		StatsInterval:  time.Hour, // the counters are flushed by Stop
		BackoffBase:    time.Millisecond,
		BackoffMax:     time.Millisecond,
		ConnectTimeout: sndWait,
		Batch:          8,
	}
	if tune != nil {
		tune(&cfg)
	}
	s, err := NewSender(cfg)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	f.s = s
	// Stop before the queue file is closed (cleanups run last in, first out).
	t.Cleanup(s.Stop)
	return f
}

// start stores the articles, queues them for peer 1 in the given order, points the sender at
// srv and starts it.
func (f *fe2eFixture) start(t *testing.T, srv *fakeserver.Server, name string, ids ...string) {
	t.Helper()
	items := make([]Item, 0, len(ids))
	for _, id := range ids {
		f.arts.add(sndGroup, sndArticle(id, sndGroup))
		items = append(items, Item{PeerID: 1, MessageID: id, Newsgroup: sndGroup})
	}
	if err := f.q.Enqueue(items); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	f.peers.set(sndRowFor(srv, func(p *models.Peer) { p.Name = name }))
	if err := f.s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

// fe2eRow is one queue row as it is stored.
type fe2eRow struct {
	id       string
	attempts int
	lastErr  string
}

// rows reads the queue rows of peer 1, in message-id order.
func (f *fe2eFixture) rows(t *testing.T) []fe2eRow {
	t.Helper()
	db := f.q.handle()
	if db == nil {
		t.Fatal("the queue is closed")
	}
	rows, err := database.RetryableQuery(db,
		"SELECT message_id, attempts, last_error FROM feed_queue WHERE peer_id = 1 ORDER BY message_id")
	if err != nil {
		t.Fatalf("reading the queue rows: %v", err)
	}
	defer rows.Close()
	var out []fe2eRow
	for rows.Next() {
		var r fe2eRow
		if err := rows.Scan(&r.id, &r.attempts, &r.lastErr); err != nil {
			t.Fatalf("scanning a queue row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the queue rows: %v", err)
	}
	return out
}

// tried reports whether the peer 1 rows are exactly want (sorted) and every one of them came
// back with an attempt and a reason.
func (f *fe2eFixture) tried(t *testing.T, want ...string) bool {
	t.Helper()
	rows := f.rows(t)
	if len(rows) != len(want) {
		return false
	}
	for i, r := range rows {
		if r.id != want[i] || r.attempts == 0 || r.lastErr == "" {
			return false
		}
	}
	return true
}

// fe2eIDs is the message-ids of the rows, for a failure message.
func fe2eIDs(rows []fe2eRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.id)
	}
	return out
}

// fe2eStop calls Stop on its own goroutine and fails when it does not return within the hang
// guard: a worker that waits for a stalled peer must not keep the sender alive.
func fe2eStop(t *testing.T, s *Sender) {
	t.Helper()
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(sndWait):
		t.Fatal("Stop did not return: a worker is still waiting for the stalled peer")
	}
}

// TestFeedStreamCheck400EndsConnection: 400 to a CHECK means the peer is going away (RFC 3977
// 3.2.1, RFC 4644 2.3). That article and every later one of the batch is deferred and Send
// returns an error, so the sender drops the connection instead of pipelining into a dead one.
// Source: diablo D-66 (dnewslink_defer_test.go), where the 400 is the last answer of the batch.
func TestFeedStreamCheck400EndsConnection(t *testing.T) {
	t.Run("last answer of the batch", func(t *testing.T) {
		a1, a2, a3 := "<fe2e-a1@test.invalid>", "<fe2e-a2@test.invalid>", "<fe2e-a3@test.invalid>"
		s := fakeserver.NewPeer(t, fakeserver.Options{Deadline: sndWait}, &fakeserver.PeerOptions{
			Defer431Once: map[string]bool{a1: true, a2: true},
			// The two CHECKs above are answered, the third one gets the 400 and the peer
			// closes the connection.
			DropAfterChecks: 2,
			DropReply:       "400 going away",
		})
		tr := sndDialReal(t, s, nil)
		res, offered, err := tr.Send(sndOutgoing(a1, a2, a3))
		if err == nil {
			t.Fatal("Send: no error after a 400, so the sender would keep the dead connection")
		}
		if offered != 3 {
			t.Errorf("offered = %d, want 3 CHECKs", offered)
		}
		sndCheckResult(t, res, 0, a1, 431, OutcomeDefer)
		sndCheckResult(t, res, 1, a2, 431, OutcomeDefer)
		sndCheckResult(t, res, 2, a3, 400, OutcomeDefer)
		if n := s.Count("TAKETHIS"); n != 0 {
			t.Errorf("%d TAKETHIS after the 400", n)
		}
	})

	t.Run("in the middle of the batch", func(t *testing.T) {
		b1, b2, b3 := "<fe2e-b1@test.invalid>", "<fe2e-b2@test.invalid>", "<fe2e-b3@test.invalid>"
		// The peer answers the second CHECK 400 and keeps the connection: the articles behind
		// it must come back as well, and nothing may be offered after it.
		s := fakeserver.NewPeer(t, fakeserver.Options{
			Deadline: sndWait,
			Faults:   fakeserver.Faults{OverrideLine: map[string][]string{"CHECK " + b2: {"400 going away"}}},
		}, &fakeserver.PeerOptions{Defer431Once: map[string]bool{b1: true}})
		tr := sndDialReal(t, s, nil)
		res, offered, err := tr.Send(sndOutgoing(b1, b2, b3))
		if err == nil {
			t.Fatal("Send: no error after a 400")
		}
		if offered != 3 {
			t.Errorf("offered = %d, want 3 CHECKs", offered)
		}
		sndCheckResult(t, res, 0, b1, 431, OutcomeDefer)
		sndCheckResult(t, res, 1, b2, 400, OutcomeDefer)
		sndCheckResult(t, res, 2, b3, 0, OutcomeDefer)
		if res[2].Err == "" {
			t.Error("the article behind the 400 came back without a reason")
		}
		if n := s.Count("TAKETHIS"); n != 0 {
			t.Errorf("%d TAKETHIS after the 400", n)
		}
	})
}

// TestFeedStreamTakethis400EndsConnection: a 400 to TAKETHIS ends the connection too. The
// article is deferred and counted as transferred (its body was on the wire), and the rest of
// the batch comes back with it.
func TestFeedStreamTakethis400EndsConnection(t *testing.T) {
	c1, c2 := "<fe2e-c1@test.invalid>", "<fe2e-c2@test.invalid>"
	s := fakeserver.NewPeer(t, fakeserver.Options{
		Deadline: sndWait,
		Faults:   fakeserver.Faults{OverrideLine: map[string][]string{"TAKETHIS " + c1: {"400 going away"}}},
	}, &fakeserver.PeerOptions{})
	tr := sndDialReal(t, s, nil)
	res, offered, err := tr.Send(sndOutgoing(c1, c2))
	if err == nil {
		t.Fatal("Send: no error after a 400 to TAKETHIS")
	}
	if offered != 2 {
		t.Errorf("offered = %d, want 2 CHECKs", offered)
	}
	sndCheckResult(t, res, 0, c1, 400, OutcomeDefer)
	if !res[0].Transferred {
		t.Error("the article was on the wire before the 400: it must count as transferred")
	}
	sndCheckResult(t, res, 1, c2, 0, OutcomeDefer)
	if res[1].Err == "" {
		t.Error("the article behind the 400 came back without a reason")
	}
}

// TestFeedIHave400EndsConnection: the IHAVE fallback stops at a 400 as well. The peer takes
// the first article, answers the second IHAVE "400 going away" and would happily read a third
// command; the transport must defer the second and the third article and return an error, so
// the sender drops the connection instead of feeding into a peer that is going away. A 400
// after the article (instead of the 335) takes the same branch and then counts the octets that
// were already on the wire; the fake peer cannot answer that way (Faults do not reach the
// final IHAVE reply), so only this position is on the wire here.
func TestFeedIHave400EndsConnection(t *testing.T) {
	e1, e2, e3 := "<fe2e-e1@test.invalid>", "<fe2e-e2@test.invalid>", "<fe2e-e3@test.invalid>"
	s := fakeserver.NewPeer(t, fakeserver.Options{
		Deadline: sndWait,
		Faults:   fakeserver.Faults{OverrideLine: map[string][]string{"IHAVE " + e2: {"400 going away"}}},
	}, &fakeserver.PeerOptions{NoStream: true})
	tr := sndDialReal(t, s, nil)
	if tr.Mode() != "ihave" {
		t.Fatalf("Mode = %q, want ihave", tr.Mode())
	}
	res, offered, err := tr.Send(sndOutgoing(e1, e2, e3))
	if err == nil {
		t.Fatal("Send: no error after a 400, so the sender would keep the dead connection")
	}
	if offered != 2 {
		t.Errorf("offered = %d, want the 2 IHAVEs that were answered", offered)
	}
	sndCheckResult(t, res, 0, e1, 235, OutcomeDone)
	sndCheckResult(t, res, 1, e2, 400, OutcomeDefer)
	sndCheckResult(t, res, 2, e3, 0, OutcomeDefer)
	if !strings.Contains(res[2].Err, "400") {
		t.Errorf("the article behind the 400 came back with %q, want the 400 as its reason", res[2].Err)
	}
	if n := s.Count("IHAVE"); n != 2 {
		t.Errorf("the peer saw %d IHAVE(s), want 2: nothing may be offered after the 400", n)
	}
}

// TestFeedSender400KeepsTheBatchQueued: the whole sender against a peer that answers 431, 431
// and then "400 going away" and closes. All three articles stay queued, each exactly once and
// with a reason, none is marked done and the peer is never handed an article. Source: diablo
// D-66, where the deferred articles were lost because they were never written back.
func TestFeedSender400KeepsTheBatchQueued(t *testing.T) {
	d1, d2, d3 := "<fe2e-d1@test.invalid>", "<fe2e-d2@test.invalid>", "<fe2e-d3@test.invalid>"
	s := fakeserver.NewPeer(t, fakeserver.Options{Deadline: sndWait}, &fakeserver.PeerOptions{
		Defer431Once:    map[string]bool{d1: true, d2: true},
		DropAfterChecks: 2,
		DropReply:       "400 going away",
	})
	f := fe2eSender(t, nil)
	f.start(t, s, "fe2e400", d1, d2, d3)

	testkit.WaitFor(t, sndWait, "the whole batch to come back", func() bool {
		return f.tried(t, d1, d2, d3)
	})
	fe2eStop(t, f.s)

	if got := fe2eIDs(f.rows(t)); len(got) != 3 {
		t.Errorf("queue holds %v, want the three articles exactly once", got)
	}
	if n := s.Count("TAKETHIS"); n != 0 {
		t.Errorf("%d article(s) were handed to a peer that said it is going away", n)
	}
	st, err := f.q.ReadStats()
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if st[1].Accepted != 0 || st[1].Deferred == 0 || st[1].QueueDepth != 3 {
		t.Errorf("counters = %+v, want nothing accepted, the deferrals counted and depth 3", st[1])
	}
}

// TestFeedSenderPeerDropsAfterMixedOutcomes: the peer refuses the first article (438 at CHECK
// time) and then goes away during the CHECK of the second one; every connection it accepts
// does the same, so the sender redials into the same drop. The refused article is gone from the
// queue and exactly the two unsettled ones are left, each once and with a reason. Sources:
// diablo D-73 (dnewslink_flags_test.go), D-63 (refile_test.go).
func TestFeedSenderPeerDropsAfterMixedOutcomes(t *testing.T) {
	z0, z1, z2 := "<fe2e-z0@test.invalid>", "<fe2e-z1@test.invalid>", "<fe2e-z2@test.invalid>"
	s := fakeserver.NewPeer(t, fakeserver.Options{Deadline: sndWait}, &fakeserver.PeerOptions{
		Have:            map[string]bool{z0: true}, // CHECK z0 → 438: settled without a transfer
		DropAfterChecks: 1,                         // the next CHECK gets the 400 and the peer closes
		DropReply:       "400 going away",
	})
	f := fe2eSender(t, nil)
	f.start(t, s, "fe2edrop", z0, z1, z2)

	testkit.WaitFor(t, sndWait, "z0 settled and the other two back in the queue", func() bool {
		return f.tried(t, z1, z2)
	})
	testkit.WaitFor(t, sndWait, "the sender to redial after the drop", func() bool {
		return s.Accepted() >= 2
	})
	fe2eStop(t, f.s)

	if got := fe2eIDs(f.rows(t)); len(got) != 2 || got[0] != z1 || got[1] != z2 {
		t.Errorf("queue holds %v, want %v", got, []string{z1, z2})
	}
	if n := s.Count("TAKETHIS"); n != 0 {
		t.Errorf("%d TAKETHIS although the peer went away during every CHECK run", n)
	}
	st, err := f.q.ReadStats()
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if st[1].Accepted != 0 || st[1].Refused == 0 || st[1].QueueDepth != 2 {
		t.Errorf("counters = %+v, want nothing accepted, z0 refused and depth 2", st[1])
	}
}

// TestFeedSenderStalledPeer: a peer that never greets, and a peer that answers MODE STREAM and
// then nothing at all. In both cases Stop returns (only the hang guard bounds that wait) and
// the article stays queued. Source: diablo dnewslinkterm_test.go, where a SIGTERM on a stalled
// peer looked unkillable.
func TestFeedSenderStalledPeer(t *testing.T) {
	t.Run("no greeting", func(t *testing.T) {
		hold := make(chan struct{}) // never released: the peer holds its greeting
		s := fakeserver.NewPeer(t, fakeserver.Options{Deadline: sndWait},
			&fakeserver.PeerOptions{HoldGreeting: hold})
		f := fe2eSender(t, func(c *Config) { c.ConnectTimeout = fe2eStallConnect })
		f.start(t, s, "fe2estall", "<fe2e-s1@test.invalid>")

		testkit.WaitFor(t, sndWait, "the peer to hold a connection", func() bool {
			return s.Accepted() >= 1
		})
		fe2eStop(t, f.s)
		if got := fe2eIDs(f.rows(t)); len(got) != 1 {
			t.Errorf("queue holds %v, want the article that was never offered", got)
		}
	})

	t.Run("silent after MODE STREAM", func(t *testing.T) {
		s := fakeserver.NewPeer(t, fakeserver.Options{Deadline: sndWait},
			&fakeserver.PeerOptions{SilentAfterModeStream: true})
		// No idle read timeout: only Stop's CloseNow may unwind the waiting worker.
		f := fe2eSender(t, func(c *Config) { c.ReadTimeout = -1 })
		f.start(t, s, "fe2esilent", "<fe2e-s2@test.invalid>")

		testkit.WaitFor(t, sndWait, "the CHECK the peer never answers", func() bool {
			return s.Count("CHECK") >= 1
		})
		fe2eStop(t, f.s)
		if got := fe2eIDs(f.rows(t)); len(got) != 1 {
			t.Errorf("queue holds %v, want the article that was never answered", got)
		}
	})
}

// TestFeedSenderSmallBatchWindow: with a window of one and of two articles the sender still
// offers all six queued articles and empties the queue. Source: diablo D-68
// (dnewslink_smallmax_test.go), where a window smaller than the drain water mark left the rest
// of the queue unsent.
func TestFeedSenderSmallBatchWindow(t *testing.T) {
	for _, batch := range []int{1, 2} {
		t.Run(fmt.Sprintf("batch %d", batch), func(t *testing.T) {
			s := fakeserver.NewPeer(t, fakeserver.Options{Deadline: sndWait}, &fakeserver.PeerOptions{})
			f := fe2eSender(t, func(c *Config) { c.Batch = batch })
			ids := make([]string, 0, 6)
			for i := 1; i <= 6; i++ {
				ids = append(ids, fmt.Sprintf("<fe2e-m%d@test.invalid>", i))
			}
			f.start(t, s, "fe2ebatch", ids...)

			testkit.WaitFor(t, sndWait, "the queue to be drained", func() bool {
				n, err := f.q.Depth(1)
				if err != nil {
					t.Fatalf("Depth: %v", err)
				}
				return n == 0
			})
			f.s.Stop()

			if n := s.Count("CHECK"); n != len(ids) {
				t.Errorf("the peer saw %d CHECK(s), want %d", n, len(ids))
			}
			if n := s.Count("TAKETHIS"); n != len(ids) {
				t.Errorf("the peer saw %d TAKETHIS, want %d", n, len(ids))
			}
			if got := fe2eIDs(f.rows(t)); len(got) != 0 {
				t.Errorf("queue still holds %v", got)
			}
			st, err := f.q.ReadStats()
			if err != nil {
				t.Fatalf("ReadStats: %v", err)
			}
			if st[1].Accepted != int64(len(ids)) || st[1].QueueDepth != 0 {
				t.Errorf("counters = %+v, want %d accepted and depth 0", st[1], len(ids))
			}
		})
	}
}
