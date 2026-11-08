package feed

// Protocol tests of the outgoing feed against a fake transit peer (nntp-testkit's
// fakeserver.PeerHandler): the streaming path (CHECK/TAKETHIS), the IHAVE fallback, AUTHINFO,
// a connection that drops, and one run through the whole sender with the real dialer.

import (
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/nntp-testkit/fakeserver"
)

// sndIdentity is our Path identity in these tests.
const sndIdentity = "us.invalid"

const sndGroup = "feed.test"

// sndRowFor is a peer row that feeds the fake server.
func sndRowFor(s *fakeserver.Server, tune func(*models.Peer)) *models.Peer {
	p := &models.Peer{
		ID: 1, Name: "fakepeer", PathToken: "fake.invalid", Enabled: true,
		OutEnabled: true, OutHost: s.Host(), OutPort: s.Port(), MaxConns: 1,
	}
	if tune != nil {
		tune(p)
	}
	return p
}

func sndDialOpts() DialOptions {
	return DialOptions{Identity: sndIdentity, Batch: 8, ConnectTimeout: sndWait}
}

// sndDialReal connects to the fake server and closes the connection when the test ends.
func sndDialReal(t *testing.T, s *fakeserver.Server, tune func(*models.Peer)) Transport {
	t.Helper()
	tr, err := DialPeer(sndRowFor(s, tune), sndDialOpts())
	if err != nil {
		t.Fatalf("DialPeer: %v", err)
	}
	t.Cleanup(tr.Close)
	return tr
}

// sndOutgoing turns message-ids into a batch of stored articles.
func sndOutgoing(ids ...string) []Outgoing {
	out := make([]Outgoing, 0, len(ids))
	for _, id := range ids {
		out = append(out, Outgoing{Article: sndArticle(id, sndGroup), Newsgroup: sndGroup})
	}
	return out
}

// sndCheckResult asserts one result.
func sndCheckResult(t *testing.T, res []Result, i int, id string, code int, outcome Outcome) {
	t.Helper()
	if i >= len(res) {
		t.Fatalf("only %d result(s), wanted %d", len(res), i+1)
	}
	r := res[i]
	if r.MessageID != id || r.Code != code || r.Outcome != outcome {
		t.Errorf("result %d = %+v, want %s code %d outcome %d", i, r, id, code, outcome)
	}
}

// TestFeedStreamOutcomes: on a streaming connection 238 then 239 finishes an article, 438
// (the peer has it) and 439 (it refuses it after reading it) finish it too, and 431 defers it.
// The connection stays usable for the next batch.
func TestFeedStreamOutcomes(t *testing.T) {
	var (
		accept  = "<st1@test.invalid>"
		have    = "<st2@test.invalid>"
		retry   = "<st3@test.invalid>"
		reject  = "<st4@test.invalid>"
		second  = "<st5@test.invalid>"
		wires   = map[string][]string{}
		wiresMu = make(chan struct{}, 1)
	)
	wiresMu <- struct{}{}
	s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(&fakeserver.PeerOptions{
		Have:         map[string]bool{have: true},
		Defer431Once: map[string]bool{retry: true},
		Reject:       map[string]bool{reject: true},
		OnArticle: func(id string, wire []string) {
			<-wiresMu
			wires[id] = wire
			wiresMu <- struct{}{}
		},
	})})
	tr := sndDialReal(t, s, nil)
	if tr.Mode() != "stream" {
		t.Fatalf("Mode = %q, want stream", tr.Mode())
	}

	res, offered, err := tr.Send(sndOutgoing(accept, have, retry, reject))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(res) != 4 {
		t.Fatalf("%d result(s), want 4", len(res))
	}
	// One offer per article: the four CHECKs, not the TAKETHIS on top of a wanted one.
	if offered != 4 {
		t.Errorf("offered = %d, want 4 CHECKs", offered)
	}
	sndCheckResult(t, res, 0, accept, 239, OutcomeDone)
	sndCheckResult(t, res, 1, have, 438, OutcomeDone)
	sndCheckResult(t, res, 2, retry, 431, OutcomeDefer)
	sndCheckResult(t, res, 3, reject, 439, OutcomeDone)
	if res[2].Err == "" {
		t.Error("the deferred result has no reason")
	}

	// The same connection takes the next batch.
	res, _, err = tr.Send(sndOutgoing(second))
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	sndCheckResult(t, res, 0, second, 239, OutcomeDone)

	// Relay as stored: our identity is prepended to the stored Path exactly once.
	<-wiresMu
	wire := wires[accept]
	wiresMu <- struct{}{}
	var paths []string
	for _, l := range wire {
		if strings.HasPrefix(l, "Path:") {
			paths = append(paths, l)
		}
		if l == "" {
			break
		}
	}
	if len(paths) != 1 {
		t.Fatalf("%d Path line(s) on the wire: %q", len(paths), wire)
	}
	if want := "Path: " + sndIdentity + "!up.invalid!not-for-mail"; paths[0] != want {
		t.Errorf("Path = %q, want %q", paths[0], want)
	}
}

// TestFeedStreamInvalidID: an article whose stored message-id can never be written is dropped
// before anything goes on the wire.
func TestFeedStreamInvalidID(t *testing.T) {
	s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(&fakeserver.PeerOptions{})})
	tr := sndDialReal(t, s, nil)
	res, offered, err := tr.Send(sndOutgoing("not-a-message-id"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	sndCheckResult(t, res, 0, "not-a-message-id", 0, OutcomeDrop)
	if offered != 0 {
		t.Errorf("offered = %d, want 0", offered)
	}
	if n := s.Count("CHECK"); n != 0 {
		t.Errorf("%d CHECK(s) sent for an invalid id", n)
	}
}

// TestFeedIHaveFallback: a peer without streaming is fed with IHAVE; 235 and 435 finish an
// article, 437 finishes it as rejected and 436 defers it.
func TestFeedIHaveFallback(t *testing.T) {
	var (
		accept = "<ih1@test.invalid>"
		have   = "<ih2@test.invalid>"
		retry  = "<ih3@test.invalid>"
		reject = "<ih4@test.invalid>"
	)
	s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(&fakeserver.PeerOptions{
		NoStream:     true,
		Have:         map[string]bool{have: true},
		Defer436Once: map[string]bool{retry: true},
		Reject:       map[string]bool{reject: true},
	})})
	tr := sndDialReal(t, s, nil)
	if tr.Mode() != "ihave" {
		t.Fatalf("Mode = %q, want ihave", tr.Mode())
	}
	res, offered, err := tr.Send(sndOutgoing(accept, have, retry, reject))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if offered != 4 {
		t.Errorf("offered = %d, want 4", offered)
	}
	sndCheckResult(t, res, 0, accept, 235, OutcomeDone)
	sndCheckResult(t, res, 1, have, 435, OutcomeDone)
	sndCheckResult(t, res, 2, retry, 436, OutcomeDefer)
	sndCheckResult(t, res, 3, reject, 437, OutcomeDone)
	if n := s.Count("CHECK"); n != 0 {
		t.Errorf("%d CHECK(s) sent to a peer without streaming", n)
	}
}

// TestFeedStreamAuth: out_username/out_password are sent, and a wrong password fails the dial
// instead of silently feeding nothing.
func TestFeedStreamAuth(t *testing.T) {
	opts := &fakeserver.PeerOptions{User: "peerlogin", Pass: "peersecret"}
	s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(opts)})
	tr := sndDialReal(t, s, func(p *models.Peer) {
		p.OutUsername, p.OutPassword = "peerlogin", "peersecret"
	})
	res, _, err := tr.Send(sndOutgoing("<auth1@test.invalid>"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	sndCheckResult(t, res, 0, "<auth1@test.invalid>", 239, OutcomeDone)

	bad, err := DialPeer(sndRowFor(s, func(p *models.Peer) {
		p.OutUsername, p.OutPassword = "peerlogin", "wrong"
	}), sndDialOpts())
	if err == nil {
		bad.Close()
		t.Fatal("DialPeer with a wrong password: no error")
	}
}

// sndDropHandler answers the greeting's MODE STREAM and the first CHECK and then closes the
// connection, like a peer that goes away mid-batch.
func sndDropHandler(after int) func(*fakeserver.Conn) {
	return func(c *fakeserver.Conn) {
		seen := 0
		for {
			line, err := c.ReadLine()
			if err != nil {
				return
			}
			switch {
			case strings.EqualFold(line, "MODE STREAM"):
				if err := c.WriteLines("203 streaming ok"); err != nil {
					return
				}
			case strings.HasPrefix(line, "CHECK "):
				seen++
				if seen > after {
					return // close without answering
				}
				if err := c.WriteLines("238 " + strings.TrimPrefix(line, "CHECK ")); err != nil {
					return
				}
			default:
				if err := c.WriteLines("500 unexpected"); err != nil {
					return
				}
			}
		}
	}
}

// sndRefuseStreamHandler answers MODE STREAM with reply and takes IHAVE afterwards.
func sndRefuseStreamHandler(reply string) func(*fakeserver.Conn) {
	return func(c *fakeserver.Conn) {
		for {
			line, err := c.ReadLine()
			if err != nil {
				return
			}
			var out []string
			switch {
			case strings.EqualFold(line, "MODE STREAM"):
				out = []string{reply}
			case strings.HasPrefix(line, "IHAVE "):
				out = []string{"435 not wanted"}
			default:
				out = []string{"500 unexpected"}
			}
			if err := c.WriteLines(out...); err != nil {
				return
			}
		}
	}
}

// TestFeedModeStreamFallback: a status the peer answers to MODE STREAM (500/501 without
// streaming, 480, 502) switches the connection to IHAVE, while a connection that breaks during
// MODE STREAM is a network error: the dial fails and the sender reconnects with its backoff.
func TestFeedModeStreamFallback(t *testing.T) {
	for _, reply := range []string{"501 Unknown MODE variant", "502 Transfer permission denied", "480 authentication required"} {
		s := fakeserver.New(t, fakeserver.Options{Handler: sndRefuseStreamHandler(reply)})
		tr, err := DialPeer(sndRowFor(s, nil), sndDialOpts())
		if err != nil {
			t.Fatalf("DialPeer with MODE STREAM %q: %v", reply, err)
		}
		if tr.Mode() != "ihave" {
			t.Errorf("MODE STREAM %q: mode = %q, want ihave", reply, tr.Mode())
		}
		res, _, err := tr.Send(sndOutgoing("<ms1@test.invalid>"))
		if err != nil {
			t.Errorf("MODE STREAM %q: Send: %v", reply, err)
		} else {
			sndCheckResult(t, res, 0, "<ms1@test.invalid>", 435, OutcomeDone)
		}
		tr.Close()
	}

	// The peer closes the connection instead of answering: not a refusal of streaming.
	s := fakeserver.New(t, fakeserver.Options{Handler: func(c *fakeserver.Conn) {}})
	tr, err := DialPeer(sndRowFor(s, nil), sndDialOpts())
	if err == nil {
		mode := tr.Mode()
		tr.Close()
		t.Fatalf("DialPeer on a connection that broke during MODE STREAM: no error (mode %q)", mode)
	}
}

// TestFeedStreamConnectionDrop: a peer that disappears mid-batch leaves every unanswered
// article deferred and the error tells the sender to close the connection.
func TestFeedStreamConnectionDrop(t *testing.T) {
	s := fakeserver.New(t, fakeserver.Options{Handler: sndDropHandler(1)})
	tr := sndDialReal(t, s, nil)
	first, second := "<drop1@test.invalid>", "<drop2@test.invalid>"
	res, offered, err := tr.Send(sndOutgoing(first, second))
	if err == nil {
		t.Fatal("Send on a dropped connection: no error")
	}
	if offered != 2 {
		t.Errorf("offered = %d, want 2 CHECKs", offered)
	}
	if len(res) != 2 {
		t.Fatalf("%d result(s), want 2", len(res))
	}
	// The first article was wanted but never acknowledged, the second never answered: both
	// come back later.
	for i, r := range res {
		if r.Outcome != OutcomeDefer {
			t.Errorf("result %d = %+v, want a Defer", i, r)
		}
		if r.Err == "" {
			t.Errorf("result %d has no reason", i)
		}
	}
}

// TestFeedSenderEndToEnd: the whole sender with the real dialer drains a queue to a fake peer,
// writes the counters and leaves nothing behind.
func TestFeedSenderEndToEnd(t *testing.T) {
	have := "<e2e2@test.invalid>"
	s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(&fakeserver.PeerOptions{
		Have: map[string]bool{have: true},
	})})
	f := newSndFixture(t, func(c *Config) {
		c.Dial = nil // the real DialPeer
		c.Hostname = sndIdentity
		c.ConnectTimeout = sndWait
	})
	ids := []string{"<e2e1@test.invalid>", have, "<e2e3@test.invalid>"}
	var items []Item
	for _, id := range ids {
		f.arts.add(sndGroup, sndArticle(id, sndGroup))
		items = append(items, Item{PeerID: 1, MessageID: id, Newsgroup: sndGroup})
	}
	if err := f.q.Enqueue(items); err != nil {
		t.Fatal(err)
	}
	f.peers.set(sndRowFor(s, func(p *models.Peer) { p.Name = "e2epeer" }))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	sndWaitFor(t, "the queue to be drained", func() bool { return len(f.q.doneIDs()) == len(ids) })
	f.s.Stop()

	st, ok := f.q.storedStats(1)
	if !ok {
		t.Fatal("no counters stored")
	}
	if st.Accepted != 2 || st.Refused != 1 || st.Deferred != 0 || st.QueueDepth != 0 {
		t.Errorf("counters = %+v, want accepted 2 refused 1 deferred 0 depth 0", st)
	}
	if n := s.Accepted(); n < 1 {
		t.Errorf("the peer accepted %d connection(s)", n)
	}
}
