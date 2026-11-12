package nntp

// Tests of the incoming peer statistics (plan peering-stats, slice stats-in): what a peer
// connection and a denied feed command report to the peerstats.Collector. The counting rules
// are the ones in internal/peerstats/api.go.
//
// Identifiers start with ps / TestPeerStats. No test asserts on wall-clock time: the only
// wait is the hang guard hDeadline (plan test-timing-hardening).

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/peerstats"
	testkit "github.com/go-while/nntp-testkit"
	"github.com/go-while/nntp-testkit/client"
)

// psEvent is one recorded Connected or Closed call.
type psEvent struct {
	Peer   peerstats.PeerRef
	Dir    peerstats.Dir
	Remote string             // Connected only
	Conn   peerstats.Counters // Closed only: the totals of that connection
	Dur    time.Duration      // Closed only
}

// psCollector records every call of the Collector interface.
type psCollector struct {
	mu     sync.Mutex
	conns  []psEvent
	closes []psEvent
	adds   []peerstats.Bucket // one entry per Add, with the ref and direction
	queues map[int64]int64
}

func newPsCollector() *psCollector {
	return &psCollector{queues: make(map[int64]int64)}
}

func (c *psCollector) Connected(p peerstats.PeerRef, d peerstats.Dir, remote string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conns = append(c.conns, psEvent{Peer: p, Dir: d, Remote: remote})
}

func (c *psCollector) Add(p peerstats.PeerRef, d peerstats.Dir, delta peerstats.Counters) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adds = append(c.adds, peerstats.Bucket{Peer: p, Dir: d, Counters: delta})
}

func (c *psCollector) Closed(p peerstats.PeerRef, d peerstats.Dir, conn peerstats.Counters, dur time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes = append(c.closes, psEvent{Peer: p, Dir: d, Conn: conn, Dur: dur})
}

func (c *psCollector) SetQueue(p peerstats.PeerRef, depth int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queues[p.ID] = depth
}

// total sums the Add deltas of one peer and direction.
func (c *psCollector) total(p peerstats.PeerRef, d peerstats.Dir) peerstats.Counters {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out peerstats.Counters
	for _, a := range c.adds {
		if a.Peer == p && a.Dir == d {
			out.Add(a.Counters)
		}
	}
	return out
}

// sum sums every Add, whatever the peer (for "nothing was counted" checks).
func (c *psCollector) sum() peerstats.Counters {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out peerstats.Counters
	for _, a := range c.adds {
		out.Add(a.Counters)
	}
	return out
}

func (c *psCollector) connected() []psEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]psEvent(nil), c.conns...)
}

func (c *psCollector) closed() []psEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]psEvent(nil), c.closes...)
}

// psStore is a PeerStore that also offers GetPeers, so the registry knows the disabled rows
// (the Denied attribution of MatchAnyIP).
type psStore struct {
	all []*models.Peer
}

func (s *psStore) GetPeers() ([]*models.Peer, error) { return s.all, nil }

func (s *psStore) GetEnabledPeers() ([]*models.Peer, error) {
	var out []*models.Peer
	for _, p := range s.all {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *psStore) AuthenticatePeer(string, string) (*models.Peer, error) {
	return nil, fmt.Errorf("no login in these tests")
}

// psRegistry is a registry over rows that may be disabled.
func psRegistry(peers ...*models.Peer) *PeerRegistry {
	return NewPeerRegistry(&psStore{all: peers})
}

// psRow returns a peer row named ps<seq> with hosts and the enabled flag.
func psRow(hosts string, enabled bool) *models.Peer {
	n := hSeq.Add(1)
	return &models.Peer{
		ID: n, Name: fmt.Sprintf("pspeer%04d", n), PathToken: fmt.Sprintf("pspeer%04d.test.invalid", n),
		Hosts: hosts, InPatterns: "*", MaxConns: 4, Enabled: enabled,
	}
}

// psServer is a server with a collector installed; it returns the server and the collector.
func psServer(t *testing.T, proc ArticleProcessor, reg *PeerRegistry) (*hServer, *psCollector) {
	t.Helper()
	hs := newTestServer(t, hServerOpts{Proc: proc, Peers: reg, Hostname: "us.ps.invalid"})
	col := newPsCollector()
	hs.S.SetPeerStats(col)
	return hs, col
}

// psArticleRaw is one article as the kit transfer commands take it: CRLF line ends, not
// dot-stuffed, no terminator.
func psArticleRaw(group, id string) []byte {
	return []byte("Path: peer.ps.invalid!not-for-mail\r\n" +
		"From: Tester <tester@test.invalid>\r\n" +
		"Newsgroups: " + group + "\r\n" +
		"Subject: ps\r\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: " + id + "\r\n" +
		"\r\n" +
		"a body line\r\n")
}

// psArticle is psArticleRaw in the form a client sends after IHAVE or TAKETHIS: dot-stuffed
// and with the terminating ".\r\n" (kit client.StuffBytes).
func psArticle(group, id string) string {
	return string(client.StuffBytes(psArticleRaw(group, id)))
}

// psIHave sends IHAVE and, after a 335, the article; it returns the final status (or the
// first reply when the article was refused before the transfer).
func psIHave(t *testing.T, c *client.Conn, group, id string) (int, string) {
	t.Helper()
	code, line, _ := c.Ihave(id, psArticleRaw(group, id))
	return code, line
}

// psRef is the PeerRef of a peer row.
func psRef(p *models.Peer) peerstats.PeerRef {
	return peerstats.PeerRef{ID: p.ID, Name: p.Name}
}

// TestPeerStatsIHaveAccepted: an accepted IHAVE is one offer, one accepted article with its
// wire size, one Connected and one Closed with the totals of that connection.
func TestPeerStatsIHaveAccepted(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	peer := psRow("127.0.0.1", true)
	hs, col := psServer(t, proc, psRegistry(peer))
	ref := psRef(peer)

	c := hs.dial(t)
	g := hGroupName("ps.acc")
	id := hMessageID(g, 1)
	if code, line := psIHave(t, c, g, id); code != 235 {
		t.Fatalf("IHAVE: got %d %q, want 235", code, line)
	}

	conns := col.connected()
	if len(conns) != 1 || conns[0].Peer != ref || conns[0].Dir != peerstats.In {
		t.Fatalf("Connected = %+v, want one In event of %+v", conns, ref)
	}
	if !strings.HasPrefix(conns[0].Remote, "127.0.0.1:") {
		t.Errorf("Connected remote = %q, want the client address", conns[0].Remote)
	}
	got := col.total(ref, peerstats.In)
	if got.Offered != 1 || got.Accepted != 1 || got.AccBytes <= 0 {
		t.Errorf("counters = %+v, want offered 1, accepted 1, acc bytes > 0", got)
	}
	if got.Refused != 0 || got.Rejected != 0 || got.Duplicate != 0 || got.Denied != 0 {
		t.Errorf("counters = %+v, want nothing refused, rejected, duplicate or denied", got)
	}

	// The connection ends: exactly one Closed with the totals counted on it.
	c.Close()
	testkit.WaitFor(t, hDeadline, "Closed after the connection ended", func() bool { return len(col.closed()) == 1 })
	cl := col.closed()[0]
	if cl.Peer != ref || cl.Dir != peerstats.In {
		t.Errorf("Closed = %+v, want the In event of %+v", cl, ref)
	}
	if cl.Conn != got {
		t.Errorf("Closed counters = %+v, want the totals of the connection %+v", cl.Conn, got)
	}
}

// TestPeerStatsIHaveRefusedAndDuplicate: a 435 before the transfer is Refused, a duplicate
// found at store time is Duplicate with its octets and never Rejected.
func TestPeerStatsIHaveRefusedAndDuplicate(t *testing.T) {
	t.Run("have it already", func(t *testing.T) {
		proc := &hFakeProcessor{checkResult: history.CaseDupes}
		peer := psRow("127.0.0.1", true)
		hs, col := psServer(t, proc, psRegistry(peer))
		c := hs.dial(t)
		g := hGroupName("ps.ref")
		if code, line := psIHave(t, c, g, hMessageID(g, 1)); code != 435 {
			t.Fatalf("IHAVE: got %d %q, want 435", code, line)
		}
		got := col.total(psRef(peer), peerstats.In)
		if got.Offered != 1 || got.Refused != 1 || got.Rejected != 0 || got.Duplicate != 0 {
			t.Errorf("counters = %+v, want offered 1 and refused 1", got)
		}
	})

	t.Run("duplicate at store time", func(t *testing.T) {
		proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CaseDupes}
		peer := psRow("127.0.0.1", true)
		hs, col := psServer(t, proc, psRegistry(peer))
		c := hs.dial(t)
		g := hGroupName("ps.dup")
		if code, line := psIHave(t, c, g, hMessageID(g, 1)); code != 437 {
			t.Fatalf("IHAVE: got %d %q, want 437", code, line)
		}
		got := col.total(psRef(peer), peerstats.In)
		if got.Offered != 1 || got.Duplicate != 1 || got.DupBytes <= 0 || got.Rejected != 0 {
			t.Errorf("counters = %+v, want offered 1, duplicate 1 with its octets and nothing rejected", got)
		}
	})
}

// TestPeerStatsIHaveRejected: an article the peer may not send us (in_patterns) is Rejected
// with the octets it transferred.
func TestPeerStatsIHaveRejected(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	peer := psRow("127.0.0.1", true)
	peer.InPatterns = "nothing.for.this.peer"
	hs, col := psServer(t, proc, psRegistry(peer))
	c := hs.dial(t)
	g := hGroupName("ps.rej")
	if code, line := psIHave(t, c, g, hMessageID(g, 1)); code != 437 {
		t.Fatalf("IHAVE: got %d %q, want 437", code, line)
	}
	got := col.total(psRef(peer), peerstats.In)
	if got.Offered != 1 || got.Rejected != 1 || got.RejBytes <= 0 || got.Accepted != 0 {
		t.Errorf("counters = %+v, want offered 1 and rejected 1 with its octets", got)
	}
}

// TestPeerStatsCheckThenTakeThisOneOffer: a CHECK and the TAKETHIS of the same id are one
// offer (like innd), and the article counts as accepted once.
func TestPeerStatsCheckThenTakeThisOneOffer(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	peer := psRow("127.0.0.1", true)
	hs, col := psServer(t, proc, psRegistry(peer))
	ref := psRef(peer)

	c := hs.dial(t)
	g := hGroupName("ps.chk")
	id := hMessageID(g, 1)
	code, line := c.Cmd("CHECK %s", id)
	if code != 238 {
		t.Fatalf("CHECK: got %d %q, want 238", code, line)
	}
	if err := c.Send("TAKETHIS " + id + "\r\n" + psArticle(g, id)); err != nil {
		t.Fatalf("send TAKETHIS: %v", err)
	}
	code, line, err := c.ReadStatus()
	if err != nil {
		t.Fatalf("TAKETHIS reply: %v (%q)", err, line)
	}
	if code != 239 {
		t.Fatalf("TAKETHIS: got %d %q, want 239", code, line)
	}
	got := col.total(ref, peerstats.In)
	if got.Offered != 1 || got.Accepted != 1 || got.AccBytes <= 0 {
		t.Errorf("counters = %+v, want one offer and one accepted article", got)
	}

	// A second CHECK of the same id starts a new offer.
	if code, line := c.Cmd("CHECK %s", id); code != 238 {
		t.Fatalf("second CHECK: got %d %q, want 238", code, line)
	}
	if got := col.total(ref, peerstats.In).Offered; got != 2 {
		t.Errorf("offered after the second CHECK = %d, want 2", got)
	}
}

// TestPeerStatsRepeatedCheck: a peer that CHECKs the same id twice and never transfers it
// gets one offer per CHECK, so the offers match the answers it was given.
func TestPeerStatsRepeatedCheck(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CaseRetry} // every CHECK answers 431
	peer := psRow("127.0.0.1", true)
	hs, col := psServer(t, proc, psRegistry(peer))

	c := hs.dial(t)
	id := hMessageID(hGroupName("ps.rep"), 1)
	for i := 0; i < 2; i++ {
		if code, line := c.Cmd("CHECK %s", id); code != 431 {
			t.Fatalf("CHECK %d: got %d %q, want 431", i+1, code, line)
		}
	}
	got := col.total(psRef(peer), peerstats.In)
	if got.Offered != 2 || got.Deferred != 2 {
		t.Errorf("counters = %+v, want offered 2 and deferred 2 (one answer per CHECK)", got)
	}
}

// TestPeerStatsDeniedDisabledRow: the feed commands of a peer row that is disabled count as
// Denied for that row, so the admin sees what it tried before it is enabled.
func TestPeerStatsDeniedDisabledRow(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CasePass}
	peer := psRow("127.0.0.1", false)
	hs, col := psServer(t, proc, psRegistry(peer))
	ref := psRef(peer)

	c := hs.dial(t)
	if code, line := c.Cmd("MODE STREAM"); code != 502 {
		t.Fatalf("MODE STREAM: got %d %q, want 502", code, line)
	}
	if code, line := c.Cmd("CHECK %s", hMessageID("ps.den", 1)); code != 502 {
		t.Fatalf("CHECK: got %d %q, want 502", code, line)
	}
	if code, line := c.Cmd("IHAVE %s", hMessageID("ps.den", 2)); code != 502 {
		t.Fatalf("IHAVE: got %d %q, want 502", code, line)
	}
	got := col.total(ref, peerstats.In)
	if got.Denied != 3 {
		t.Errorf("denied = %d, want 3 (MODE STREAM, CHECK, IHAVE)", got.Denied)
	}
	if got.Offered != 0 || got.Accepted != 0 {
		t.Errorf("counters = %+v, want nothing but denied", got)
	}
	conns := col.connected()
	if len(conns) != 1 || conns[0].Peer != ref {
		t.Fatalf("Connected = %+v, want one event of the disabled row %+v", conns, ref)
	}
	c.Close()
	testkit.WaitFor(t, hDeadline, "Closed of the denied connection", func() bool { return len(col.closed()) == 1 })
	if cl := col.closed()[0]; cl.Peer != ref || cl.Conn.Denied != 3 {
		t.Errorf("Closed = %+v, want the denied totals of %+v", cl, ref)
	}
}

// TestPeerStatsDeniedLoginPeerNotCounted: the 480 of an enabled peer that may feed once it
// logs in is no refusal: it asks for AUTHINFO, so nothing is counted. The same row disabled
// gets 502 and is counted.
func TestPeerStatsDeniedLoginPeerNotCounted(t *testing.T) {
	t.Run("enabled, waiting for the login", func(t *testing.T) {
		proc := &hFakeProcessor{checkResult: history.CasePass}
		peer := psRow("127.0.0.1", true)
		peer.InUsername = peer.Name + "in" // may feed only after AUTHINFO
		hs, col := psServer(t, proc, psRegistry(peer))

		c := hs.dial(t)
		if code, line := c.Cmd("MODE STREAM"); code != 480 {
			t.Fatalf("MODE STREAM: got %d %q, want 480", code, line)
		}
		if code, line := c.Cmd("CHECK %s", hMessageID("ps.login", 1)); code != 480 {
			t.Fatalf("CHECK: got %d %q, want 480", code, line)
		}
		if conns := col.connected(); len(conns) != 0 {
			t.Errorf("Connected = %+v, want none: the peer was only asked to log in", conns)
		}
		if got := col.sum(); !got.IsZero() {
			t.Errorf("counters = %+v, want nothing counted for a 480", got)
		}
	})

	t.Run("disabled, turned away", func(t *testing.T) {
		proc := &hFakeProcessor{checkResult: history.CasePass}
		peer := psRow("127.0.0.1", false)
		peer.InUsername = peer.Name + "in"
		hs, col := psServer(t, proc, psRegistry(peer))

		c := hs.dial(t)
		if code, line := c.Cmd("MODE STREAM"); code != 502 {
			t.Fatalf("MODE STREAM: got %d %q, want 502", code, line)
		}
		if got := col.total(psRef(peer), peerstats.In).Denied; got != 1 {
			t.Errorf("denied = %d, want 1 for the disabled row", got)
		}
	})
}

// TestPeerStatsDeniedUnknownHost: a feed command from an address no peer row holds counts
// under peerstats.UnknownPeer.
func TestPeerStatsDeniedUnknownHost(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CasePass}
	// A row with another host: it must not catch this connection.
	hs, col := psServer(t, proc, psRegistry(psRow("192.0.2.7", false)))

	c := hs.dial(t)
	if code, line := c.Cmd("CHECK %s", hMessageID("ps.unk", 1)); code != 502 {
		t.Fatalf("CHECK: got %d %q, want 502", code, line)
	}
	if got := col.total(peerstats.UnknownPeer, peerstats.In); got.Denied != 1 {
		t.Errorf("denied under %+v = %+v, want 1", peerstats.UnknownPeer, got)
	}
	conns := col.connected()
	if len(conns) != 1 || conns[0].Peer != peerstats.UnknownPeer {
		t.Errorf("Connected = %+v, want one event of %+v", conns, peerstats.UnknownPeer)
	}
}

// TestPeerStatsReaderCountsNothing: a connection that never sends a feed command produces no
// event at all, whether or not a peer row holds its address.
func TestPeerStatsReaderCountsNothing(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CasePass}
	hs, col := psServer(t, proc, psRegistry(psRow("192.0.2.7", true)))

	c := hs.dial(t)
	if code, line := c.Cmd("DATE"); code != 111 {
		t.Fatalf("DATE: got %d %q, want 111", code, line)
	}
	if code, line := c.Cmd("QUIT"); code != 205 {
		t.Fatalf("QUIT: got %d %q, want 205", code, line)
	}
	c.Close()
	if conns := col.connected(); len(conns) != 0 {
		t.Errorf("Connected = %+v, want none for a plain reader", conns)
	}
	if got := col.sum(); !got.IsZero() {
		t.Errorf("counters = %+v, want nothing counted for a plain reader", got)
	}
	if cl := col.closed(); len(cl) != 0 {
		t.Errorf("Closed = %+v, want none for a plain reader", cl)
	}
}

// TestPeerStatsNoCollector: without a collector the feed path behaves exactly as before
// (peerstats.Nop), and the accessor never returns nil.
func TestPeerStatsNoCollector(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	peer := psRow("127.0.0.1", true)
	hs := newTestServer(t, hServerOpts{Proc: proc, Peers: psRegistry(peer), Hostname: "us.ps.invalid"})
	if hs.S.PeerStats() == nil {
		t.Fatal("PeerStats() without SetPeerStats is nil, want peerstats.Nop")
	}
	c := hs.dial(t)
	g := hGroupName("ps.nop")
	if code, line := psIHave(t, c, g, hMessageID(g, 1)); code != 235 {
		t.Fatalf("IHAVE without a collector: got %d %q, want 235", code, line)
	}
	// SetPeerStats(nil) switches the counting off again.
	hs.S.SetPeerStats(newPsCollector())
	hs.S.SetPeerStats(nil)
	if hs.S.PeerStats() == nil {
		t.Fatal("PeerStats() after SetPeerStats(nil) is nil, want peerstats.Nop")
	}
}

// TestPeerStatsMatchAnyIP: MatchAnyIP answers from the rows of the same cached reload,
// enabled or not, in name order, and only for rows that have hosts.
func TestPeerStatsMatchAnyIP(t *testing.T) {
	off := psRow("127.0.0.1", false)
	off.Name = "a-disabled"
	on := psRow("127.0.0.1", true)
	on.Name = "b-enabled"
	login := psRow("", true) // login only: never matched by address
	login.Name = "c-login"
	login.InUsername = "someone"
	r := psRegistry(off, on, login)

	// First in the order of the store (name order) wins, enabled or not.
	if got := r.MatchAnyIP(net.ParseIP("127.0.0.1")); got != off {
		t.Errorf("MatchAnyIP(127.0.0.1) = %v, want the first row %q", got, off.Name)
	}
	if got := r.MatchAnyIP(net.ParseIP("127.0.0.2")); got != nil {
		t.Errorf("MatchAnyIP(127.0.0.2) = %v, want nil", got)
	}
	if got := r.MatchAnyIP(nil); got != nil {
		t.Errorf("MatchAnyIP(nil) = %v, want nil", got)
	}
	// MatchIP still only matches the enabled row without a login.
	if got := r.MatchIP(net.ParseIP("127.0.0.1")); got != on {
		t.Errorf("MatchIP(127.0.0.1) = %v, want %q", got, on.Name)
	}
	// A nil registry and a store without GetPeers answer nothing instead of panicking.
	var nilReg *PeerRegistry
	if got := nilReg.MatchAnyIP(net.ParseIP("127.0.0.1")); got != nil {
		t.Errorf("nil registry MatchAnyIP = %v, want nil", got)
	}
	if got := hPeers(psRow("127.0.0.1", true)).MatchAnyIP(net.ParseIP("127.0.0.1")); got != nil {
		t.Errorf("MatchAnyIP of a store without GetPeers = %v, want nil", got)
	}
}

// TestPeerStatsFeedCounters: the reply-to-counter mapping of the counting rules.
func TestPeerStatsFeedCounters(t *testing.T) {
	for _, tc := range []struct {
		name  string
		offer bool
		code  int
		size  int64
		dup   bool
		want  peerstats.Counters
	}{
		{"accepted ihave", true, 235, 100, false, peerstats.Counters{Offered: 1, Accepted: 1, AccBytes: 100}},
		{"accepted takethis", true, 239, 100, false, peerstats.Counters{Offered: 1, Accepted: 1, AccBytes: 100}},
		{"refused 435", true, 435, 0, false, peerstats.Counters{Offered: 1, Refused: 1}},
		{"refused 438", false, 438, 0, false, peerstats.Counters{Refused: 1}},
		{"rejected 437", true, 437, 70, false, peerstats.Counters{Offered: 1, Rejected: 1, RejBytes: 70}},
		{"rejected 439", true, 439, 70, false, peerstats.Counters{Offered: 1, Rejected: 1, RejBytes: 70}},
		{"deferred 436", true, 436, 0, false, peerstats.Counters{Offered: 1, Deferred: 1}},
		{"deferred 431", false, 431, 0, false, peerstats.Counters{Deferred: 1}},
		{"deferred 400", true, 400, 0, false, peerstats.Counters{Offered: 1, Deferred: 1}},
		{"duplicate", true, 439, 70, true, peerstats.Counters{Offered: 1, Duplicate: 1, DupBytes: 70}},
		{"check 238", true, 238, 0, false, peerstats.Counters{Offered: 1}},
		{"nothing", false, 203, 0, false, peerstats.Counters{}},
	} {
		if got := peerFeedCounters(tc.offer, tc.code, tc.size, tc.dup); got != tc.want {
			t.Errorf("%s: peerFeedCounters = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
