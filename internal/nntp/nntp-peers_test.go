package nntp

// Tests of the inbound peer ACL (plan nntp-peering, slice peer-acl): the PeerRegistry
// (address, CIDR and host-name matching with its DNS cache, peer AUTHINFO) and, on the
// wire, the refusals this layer decides (Path loop, in_patterns) with their remembered
// rejects, plus the Path rewrite of an accepted feed article.
//
// Identifiers use the peer / TestPeer prefix. No test waits on the wall clock: the
// registry's clock and resolver are injected.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/nntp-testkit/client"
)

// peerTestStore is a PeerStore with a fixed peer list and a scripted login.
type peerTestStore struct {
	mu       sync.Mutex
	peers    []*models.Peer
	logins   map[string]string // in_username -> password
	loads    int               // GetEnabledPeers calls
	loadErr  error
	authCall int
}

func (s *peerTestStore) GetEnabledPeers() ([]*models.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.peers, nil
}

func (s *peerTestStore) AuthenticatePeer(username, password string) (*models.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authCall++
	if s.logins[username] != password || password == "" {
		return nil, database.ErrPeerAuth
	}
	for _, p := range s.peers {
		if p.InUsername == username {
			return p, nil
		}
	}
	return nil, database.ErrPeerAuth
}

func (s *peerTestStore) counters() (loads, auths int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads, s.authCall
}

func (s *peerTestStore) setPeers(peers []*models.Peer) {
	s.mu.Lock()
	s.peers = peers
	s.mu.Unlock()
}

func (s *peerTestStore) setErr(err error) {
	s.mu.Lock()
	s.loadErr = err
	s.mu.Unlock()
}

// peerTestClock is a hand-driven clock for the registry.
type peerTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *peerTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *peerTestClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// peerTestRegistry builds a registry over store with an injected clock and resolver.
func peerTestRegistry(store PeerStore, clock *peerTestClock, resolve func(string) ([]net.IP, error)) *PeerRegistry {
	r := NewPeerRegistry(store)
	r.now = clock.now
	if resolve != nil {
		r.resolve = func(_ context.Context, host string) ([]net.IP, error) { return resolve(host) }
	}
	return r
}

func peerTestClockAt() *peerTestClock {
	return &peerTestClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
}

// peerSpec returns a peer row for the registry tests.
func peerSpec(name, hosts, login string) *models.Peer {
	return &models.Peer{
		Name: name, PathToken: name + ".invalid", Hosts: hosts, InUsername: login,
		InPatterns: "*", MaxConns: 4, Enabled: true,
	}
}

// TestPeerMatchIP: a peer without a login is matched by address, CIDR or host name; one
// with a login is never matched by address alone.
func TestPeerMatchIP(t *testing.T) {
	store := &peerTestStore{peers: []*models.Peer{
		peerSpec("ip4", "192.0.2.10", ""),
		peerSpec("cidr", "198.51.100.0/24", ""),
		peerSpec("ip6", "2001:db8::1", ""),
		peerSpec("byname", "peer.test.invalid", ""),
		peerSpec("login", "203.0.113.5", "login-in"),
		peerSpec("nohosts", "", ""), // no hosts and no login: matched by nothing
	}}
	clock := peerTestClockAt()
	resolved := map[string][]net.IP{"peer.test.invalid": {net.ParseIP("192.0.2.77")}}
	var lookups int
	reg := peerTestRegistry(store, clock, func(host string) ([]net.IP, error) {
		lookups++
		ips, ok := resolved[host]
		if !ok {
			return nil, fmt.Errorf("no such host %q", host)
		}
		return ips, nil
	})

	for _, tc := range []struct {
		ip   string
		want string // peer name, "" = no peer
	}{
		{"192.0.2.10", "ip4"},
		{"192.0.2.11", ""},
		{"198.51.100.1", "cidr"},
		{"198.51.100.255", "cidr"},
		{"198.51.101.1", ""},
		{"2001:db8::1", "ip6"},
		{"2001:db8::2", ""},
		{"192.0.2.77", "byname"},
		{"203.0.113.5", ""}, // that peer needs its login
	} {
		got := ""
		if p := reg.MatchIP(net.ParseIP(tc.ip)); p != nil {
			got = p.Name
		}
		if got != tc.want {
			t.Errorf("MatchIP(%s) = %q, want %q", tc.ip, got, tc.want)
		}
	}
	if reg.MatchIP(nil) != nil {
		t.Error("MatchIP(nil) matched a peer")
	}
	if lookups == 0 {
		t.Error("the host-name peer was never resolved")
	}

	// LoginPossible: only the peer with a login counts, and only from its hosts.
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"203.0.113.5", true},
		{"192.0.2.10", false},
	} {
		if got := reg.LoginPossible(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("LoginPossible(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
	// A peer with a login and no hosts may log in from anywhere.
	store.peers = append(store.peers, peerSpec("anywhere", "", "anywhere-in"))
	reg.Invalidate()
	if !reg.LoginPossible(net.ParseIP("192.0.2.10")) {
		t.Error("a peer with a login and no hosts must allow a login from any address")
	}
}

// TestPeerNil: every registry method is safe on a nil receiver and on a nil store (a
// server configured without peers).
func TestPeerNil(t *testing.T) {
	for _, reg := range []*PeerRegistry{nil, NewPeerRegistry(nil)} {
		if p := reg.MatchIP(net.ParseIP("127.0.0.1")); p != nil {
			t.Errorf("MatchIP matched %v", p)
		}
		if reg.LoginPossible(net.ParseIP("127.0.0.1")) {
			t.Error("LoginPossible without peers")
		}
		if p, isPeer := reg.Login("x", "y", net.ParseIP("127.0.0.1")); p != nil || isPeer {
			t.Errorf("Login = (%v, %v), want (nil, false)", p, isPeer)
		}
		reg.Invalidate()
	}
}

// TestPeerListCache: the peer list is read once per peerListTTL, Invalidate makes the next
// match read it again, a failed read keeps the list that was there and is retried after
// peerListErrorTTL (not on every call), and the first read failing means nothing may feed.
func TestPeerListCache(t *testing.T) {
	store := &peerTestStore{peers: []*models.Peer{peerSpec("ip4", "192.0.2.10", "")}}
	clock := peerTestClockAt()
	reg := peerTestRegistry(store, clock, nil)
	ip := net.ParseIP("192.0.2.10")

	for i := 0; i < 3; i++ {
		if reg.MatchIP(ip) == nil {
			t.Fatalf("match %d: peer not found", i)
		}
	}
	if loads, _ := store.counters(); loads != 1 {
		t.Fatalf("GetEnabledPeers called %d times inside the TTL, want 1", loads)
	}

	// an admin edit applies after the TTL, without a restart
	store.setPeers(nil)
	clock.advance(peerListTTL)
	if p := reg.MatchIP(ip); p != nil {
		t.Fatalf("peer %q still matched after it was removed", p.Name)
	}
	if loads, _ := store.counters(); loads != 2 {
		t.Fatalf("GetEnabledPeers called %d times, want 2", loads)
	}

	// Invalidate applies an edit at once
	store.setPeers([]*models.Peer{peerSpec("ip4", "192.0.2.10", "")})
	if reg.MatchIP(ip) != nil {
		t.Fatal("the cached empty list was not used")
	}
	reg.Invalidate()
	if reg.MatchIP(ip) == nil {
		t.Fatal("Invalidate did not make the registry read the table again")
	}

	// A failed read keeps the peers that were loaded and is not retried on every call:
	// the next attempt comes after peerListErrorTTL, not after peerListTTL.
	store.setErr(errors.New("main DB locked"))
	clock.advance(peerListTTL)
	before, _ := store.counters()
	for i := 0; i < 4; i++ {
		if reg.MatchIP(ip) == nil {
			t.Fatalf("a failed read dropped the known peers (call %d)", i)
		}
	}
	if loads, _ := store.counters(); loads != before+1 {
		t.Fatalf("GetEnabledPeers called %d times for 4 matches after a failed read, want 1", loads-before)
	}
	clock.advance(peerListErrorTTL)
	if reg.MatchIP(ip) == nil {
		t.Fatal("the known peers were dropped")
	}
	if loads, _ := store.counters(); loads != before+2 {
		t.Fatalf("GetEnabledPeers called %d times, want one retry after peerListErrorTTL", loads-before-1)
	}
	// and it recovers
	store.setErr(nil)
	clock.advance(peerListErrorTTL)
	if reg.MatchIP(ip) == nil {
		t.Fatal("the peer is gone after the read recovered")
	}
}

// TestPeerListFirstReadFails: until the table could be read once, nothing may feed, and the
// failing read is bounded by peerListErrorTTL as well.
func TestPeerListFirstReadFails(t *testing.T) {
	store := &peerTestStore{
		peers:   []*models.Peer{peerSpec("ip4", "192.0.2.10", "")},
		loadErr: errors.New("main DB locked"),
	}
	clock := peerTestClockAt()
	reg := peerTestRegistry(store, clock, nil)
	ip := net.ParseIP("192.0.2.10")

	for i := 0; i < 3; i++ {
		if p := reg.MatchIP(ip); p != nil {
			t.Fatalf("peer %q matched although the peer list was never read", p.Name)
		}
		if reg.LoginPossible(ip) {
			t.Fatal("LoginPossible although the peer list was never read")
		}
	}
	if loads, _ := store.counters(); loads != 1 {
		t.Fatalf("GetEnabledPeers called %d times, want 1 inside peerListErrorTTL", loads)
	}
	store.setErr(nil)
	clock.advance(peerListErrorTTL)
	if reg.MatchIP(ip) == nil {
		t.Fatal("the peer is still not matched after the read succeeded")
	}
}

// TestPeerSkipsRowWithoutPathToken: a peer row without a path_token (which
// database.ValidatePeer refuses, so it can only be hand-edited into the table) can never
// feed: there is nothing to compare the incoming Path against.
func TestPeerSkipsRowWithoutPathToken(t *testing.T) {
	broken := peerSpec("broken", "192.0.2.10", "")
	broken.PathToken = "  "
	withLogin := peerSpec("broken-login", "", "broken-login-in")
	withLogin.PathToken = ""
	store := &peerTestStore{peers: []*models.Peer{broken, withLogin}, logins: map[string]string{"broken-login-in": "secret"}}
	reg := peerTestRegistry(store, peerTestClockAt(), nil)
	ip := net.ParseIP("192.0.2.10")

	if p := reg.MatchIP(ip); p != nil {
		t.Errorf("peer %q without a path_token matched by address", p.Name)
	}
	if reg.LoginPossible(ip) {
		t.Error("LoginPossible for a peer without a path_token")
	}
	if p, isPeer := reg.Login("broken-login-in", "secret", ip); p != nil || isPeer {
		t.Errorf("Login = (%v, %v) for a peer without a path_token, want (nil, false)", p, isPeer)
	}
	if n := len(reg.Enabled()); n != 0 {
		t.Errorf("Enabled() lists %d peers, want 0", n)
	}
}

// TestPeerEnabledCopy: Enabled() hands out a new slice, so a caller cannot change what the
// registry matches against.
func TestPeerEnabledCopy(t *testing.T) {
	store := &peerTestStore{peers: []*models.Peer{peerSpec("ip4", "192.0.2.10", ""), peerSpec("ip5", "192.0.2.11", "")}}
	reg := peerTestRegistry(store, peerTestClockAt(), nil)
	list := reg.Enabled()
	if len(list) != 2 {
		t.Fatalf("Enabled() = %d peers, want 2", len(list))
	}
	list[0] = nil
	if again := reg.Enabled(); len(again) != 2 || again[0] == nil {
		t.Fatalf("the registry list changed with the returned slice: %v", again)
	}
	if reg.MatchIP(net.ParseIP("192.0.2.10")) == nil {
		t.Fatal("the peer is gone after its entry was overwritten in the returned slice")
	}
}

// peerTestResolver is a scripted, counting resolver.
type peerTestResolver struct {
	mu      sync.Mutex
	ips     []net.IP
	err     error
	calls   int
	release chan struct{} // when set, a resolution blocks until it is closed
	entered chan struct{} // closed by the first resolution that blocks
}

func (r *peerTestResolver) resolve(string) ([]net.IP, error) {
	r.mu.Lock()
	r.calls++
	release, entered := r.release, r.entered
	ips, err := r.ips, r.err
	r.mu.Unlock()
	if release != nil {
		if entered != nil {
			select {
			case <-entered:
			default:
				close(entered)
			}
		}
		<-release
	}
	return ips, err
}

func (r *peerTestResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *peerTestResolver) set(ips []net.IP, err error) {
	r.mu.Lock()
	r.ips, r.err = ips, err
	r.mu.Unlock()
}

// TestPeerDNSCache: a resolved hosts name is cached for peerDNSTTL, a miss resolves again
// at most once per peerDNSRetry, a resolver fault keeps the last addresses for at most
// peerDNSGrace, and an authoritative "no such host" drops them at once.
func TestPeerDNSCache(t *testing.T) {
	store := &peerTestStore{peers: []*models.Peer{peerSpec("byname", "peer.test.invalid", "")}}
	clock := peerTestClockAt()
	res := &peerTestResolver{ips: []net.IP{net.ParseIP("192.0.2.77")}}
	reg := peerTestRegistry(store, clock, res.resolve)
	hit, miss := net.ParseIP("192.0.2.77"), net.ParseIP("192.0.2.78")

	if reg.MatchIP(hit) == nil {
		t.Fatal("first match failed")
	}
	if n := res.count(); n != 1 {
		t.Fatalf("%d lookups for the first match, want 1", n)
	}
	// a hit inside the TTL costs no lookup
	clock.advance(peerDNSTTL / 2)
	if reg.MatchIP(hit) == nil || res.count() != 1 {
		t.Fatalf("a cached hit cost %d lookups", res.count())
	}
	// a miss resolves again, but not once per connection
	for i := 0; i < 5; i++ {
		if reg.MatchIP(miss) != nil {
			t.Fatal("an address that is not the peer's matched")
		}
	}
	if n := res.count(); n != 2 {
		t.Fatalf("%d lookups for 5 misses, want 2 (one retry, then the retry window)", n)
	}
	// after the retry window a miss may ask again
	clock.advance(peerDNSRetry)
	if reg.MatchIP(miss) != nil {
		t.Fatal("miss matched")
	}
	if n := res.count(); n != 3 {
		t.Fatalf("%d lookups after the retry window, want 3", n)
	}
	// the peer moved: the new address is picked up without a restart
	res.set([]net.IP{miss}, nil)
	clock.advance(peerDNSRetry)
	if reg.MatchIP(miss) == nil {
		t.Fatal("the peer's new address was not picked up")
	}
	// A resolver fault keeps the last answer, but only within peerDNSGrace.
	res.set(nil, errors.New("server misbehaving"))
	clock.advance(peerDNSTTL)
	if reg.MatchIP(miss) == nil {
		t.Fatal("a DNS failure cut the peer off although its last address was known")
	}
	clock.advance(peerDNSGrace)
	if p := reg.MatchIP(miss); p != nil {
		t.Fatalf("peer %q still matched by an answer older than peerDNSGrace", p.Name)
	}
	// An authoritative "no such host" drops the addresses, so the peer stops matching
	// although the last answer is still inside peerDNSGrace. A cached answer that is
	// still fresh is used without asking, so the name is re-resolved once its
	// peerDNSTTL has passed.
	res.set([]net.IP{miss}, nil)
	clock.advance(peerDNSRetry)
	if reg.MatchIP(miss) == nil {
		t.Fatal("the peer was not matched again after the resolver recovered")
	}
	res.set(nil, &net.DNSError{Err: "no such host", Name: "peer.test.invalid", IsNotFound: true})
	clock.advance(peerDNSTTL)
	if p := reg.MatchIP(miss); p != nil {
		t.Fatalf("peer %q still matched after an authoritative NXDOMAIN", p.Name)
	}
	// and it is not matched again from the old answer while the name stays gone
	clock.advance(peerDNSRetry)
	if reg.MatchIP(miss) != nil {
		t.Fatal("the dropped addresses came back")
	}
}

// TestPeerDNSSingleFlight: while one resolution runs, every other connection answers from
// the cached addresses instead of resolving too, so a burst of connections costs one lookup
// and only one of them can wait on the resolver.
func TestPeerDNSSingleFlight(t *testing.T) {
	store := &peerTestStore{peers: []*models.Peer{peerSpec("byname", "peer.test.invalid", "")}}
	clock := peerTestClockAt()
	res := &peerTestResolver{
		ips:     []net.IP{net.ParseIP("192.0.2.77")},
		release: make(chan struct{}),
		entered: make(chan struct{}),
	}
	reg := peerTestRegistry(store, clock, res.resolve)
	ip := net.ParseIP("192.0.2.77")

	// one goroutine holds the resolver
	done := make(chan *models.Peer, 1)
	go func() { done <- reg.MatchIP(ip) }()
	select {
	case <-res.entered:
	case <-time.After(hDeadline):
		t.Fatal("the first resolution never started")
	}
	// every other caller returns at once, without resolving
	for i := 0; i < 20; i++ {
		if p := reg.MatchIP(ip); p != nil {
			t.Fatalf("call %d matched %q although no answer is known yet", i, p.Name)
		}
	}
	if n := res.count(); n != 1 {
		t.Fatalf("%d lookups while one resolution was running, want 1", n)
	}
	close(res.release)
	select {
	case p := <-done:
		if p == nil {
			t.Fatal("the resolving caller did not match the peer")
		}
	case <-time.After(hDeadline):
		t.Fatal("the resolving caller did not return")
	}
	// the answer is cached now: no further lookups
	for i := 0; i < 5; i++ {
		if reg.MatchIP(ip) == nil {
			t.Fatalf("call %d did not match the resolved peer", i)
		}
	}
	if n := res.count(); n != 1 {
		t.Fatalf("%d lookups in total, want 1", n)
	}
}

// TestPeerLogin: AUTHINFO against the peers table. A name that is no peer's login is not a
// peer login at all (the nntp_users path runs); a wrong password and a login from an
// address the peer's hosts do not hold both fail, and the peers table is the only place
// that is asked.
func TestPeerLogin(t *testing.T) {
	store := &peerTestStore{
		peers:  []*models.Peer{peerSpec("login", "203.0.113.5", "login-in"), peerSpec("anywhere", "", "anywhere-in")},
		logins: map[string]string{"login-in": "secret", "anywhere-in": "secret2"},
	}
	clock := peerTestClockAt()
	reg := peerTestRegistry(store, clock, nil)
	good, bad := net.ParseIP("203.0.113.5"), net.ParseIP("192.0.2.1")

	for _, tc := range []struct {
		name       string
		user, pass string
		ip         net.IP
		wantPeer   string
		wantIsPeer bool
	}{
		{"not a peer login", "t2user0001", "pw", good, "", false},
		{"empty user", "", "pw", good, "", false},
		{"right password from a known host", "login-in", "secret", good, "login", true},
		{"wrong password", "login-in", "nope", good, "", true},
		{"empty password", "login-in", "", good, "", true},
		{"right password from another host", "login-in", "secret", bad, "", true},
		{"peer without hosts from anywhere", "anywhere-in", "secret2", bad, "anywhere", true},
	} {
		p, isPeer := reg.Login(tc.user, tc.pass, tc.ip)
		got := ""
		if p != nil {
			got = p.Name
		}
		if got != tc.wantPeer || isPeer != tc.wantIsPeer {
			t.Errorf("%s: Login = (%q, %v), want (%q, %v)", tc.name, got, isPeer, tc.wantPeer, tc.wantIsPeer)
		}
	}
	// A name that is no peer's login never reaches AuthenticatePeer: no second bcrypt and
	// no way to tell peer names from user names by timing.
	if _, auths := store.counters(); auths != 5 {
		t.Errorf("AuthenticatePeer called %d times, want 5 (only the peer logins)", auths)
	}
}

// TestPeerResetSessionState (RFC 4642 §2.2.2): STARTTLS discards a peer login, and the
// peer match by address — which depends on nothing the client said — is taken again.
func TestPeerResetSessionState(t *testing.T) {
	byAddr, byLogin := peerSpec("ip4", "192.0.2.10", ""), peerSpec("login", "", "login-in")
	store := &peerTestStore{peers: []*models.Peer{byAddr, byLogin}, logins: map[string]string{"login-in": "secret"}}
	reg := peerTestRegistry(store, peerTestClockAt(), nil)

	c, _ := intkConn(t, nil, 1024, "reset.test.invalid", false)
	c.server.Peers = reg
	c.conn = intkAddrConn{Conn: c.conn, remote: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 41119}}
	c.resolvePeer()
	if c.peer == nil || c.peer.Name != "ip4" {
		t.Fatalf("peer by address = %v, want ip4", c.peer)
	}
	c.peer, c.peerLoggedIn = byLogin, true // as a peer login would leave it
	c.resetSessionState()
	if c.peerLoggedIn {
		t.Error("the peer login survived STARTTLS")
	}
	if c.peer == nil || c.peer.Name != "ip4" {
		t.Errorf("peer after the reset = %v, want the address match ip4 again", c.peer)
	}
	if c.starttlsAvailable() && c.loggedIn() {
		t.Error("loggedIn and starttlsAvailable at the same time")
	}

	// from an address no peer holds, the reset leaves no peer at all
	c2, _ := intkConn(t, nil, 1024, "reset.test.invalid", false)
	c2.server.Peers = reg
	c2.peer, c2.peerLoggedIn = byLogin, true
	c2.resetSessionState()
	if c2.peer != nil || c2.peerLoggedIn {
		t.Errorf("peer %v loggedIn=%v after the reset, want none", c2.peer, c2.peerLoggedIn)
	}
}

// ---------------------------------------------------------------------------
// On the wire

// peerFeedServer is a server with one peer that may feed from 127.0.0.1, the given
// in_patterns and the Path identity peerFeedHost.
const peerFeedHost = "feed.peer.test.invalid"

func peerFeedServer(t *testing.T, patterns string) (*hServer, *hFakeProcessor, *models.Peer) {
	t.Helper()
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	reg, peer := hPeerIP(patterns)
	hs := newTestServer(t, hServerOpts{Proc: proc, Peers: reg, Hostname: peerFeedHost})
	return hs, proc, peer
}

// peerArticle is the wire form of an article with the given Path and Newsgroups.
func peerArticle(id, path, groups string) string {
	return "Path: " + path + "\r\n" +
		"From: Peer <peer@test.invalid>\r\n" +
		"Newsgroups: " + groups + "\r\n" +
		"Subject: peer feed\r\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: " + id + "\r\n" +
		"\r\n" +
		"body\r\n.\r\n"
}

// peerIHave offers one article by IHAVE and returns the final reply.
func peerIHave(t *testing.T, c *client.Conn, id, path, groups string) (int, string) {
	t.Helper()
	code, line := c.Cmd("IHAVE %s", id)
	if err := client.ExpectCode(code, line, 335); err != nil {
		t.Fatalf("IHAVE: %v", err)
	}
	if err := c.Send(peerArticle(id, path, groups)); err != nil {
		t.Fatalf("send article: %v", err)
	}
	code, line, err := c.ReadStatus()
	if err != nil {
		t.Fatalf("IHAVE reply: %v (%q)", err, line)
	}
	return code, line
}

// peerTakeThis offers one article by TAKETHIS and returns the reply.
func peerTakeThis(t *testing.T, c *client.Conn, id, path, groups string) (int, string) {
	t.Helper()
	if err := c.Send("TAKETHIS " + id + "\r\n" + peerArticle(id, path, groups)); err != nil {
		t.Fatalf("send TAKETHIS: %v", err)
	}
	code, line, err := c.ReadStatus()
	if err != nil {
		t.Fatalf("TAKETHIS reply: %v (%q)", err, line)
	}
	return code, line
}

// TestPeerPathLoopRefused: an article whose Path already holds our identity is refused
// (IHAVE 437, TAKETHIS 439), never handed to the processor, and remembered as rejected so
// the peer stops offering it.
func TestPeerPathLoopRefused(t *testing.T) {
	g := hGroup(t, "t8.loop", 0, nil)
	hs, proc, _ := peerFeedServer(t, "*")
	c := hs.dial(t)

	idIHave := hMessageID(g, 1)
	code, line := peerIHave(t, c, idIHave, "upstream.invalid!"+peerFeedHost+"!origin.invalid", g)
	if err := client.ExpectCode(code, line, 437); err != nil {
		t.Fatalf("IHAVE of a looped article: %v", err)
	}
	if !strings.Contains(line, "path loop") {
		t.Errorf("IHAVE reply %q, want the loop reason", line)
	}

	idTake := hMessageID(g, 2)
	// the MISMATCH form of our identity counts as ours too
	code, line = peerTakeThis(t, c, idTake, "upstream.invalid!"+peerFeedHost+".MISMATCH!origin.invalid", g)
	if err := client.ExpectCode(code, line, 439); err != nil {
		t.Fatalf("TAKETHIS of a looped article: %v", err)
	}
	if f := strings.Fields(line); len(f) < 2 || f[1] != idTake {
		t.Errorf("TAKETHIS reply %q does not carry %s", line, idTake)
	}

	if n := len(proc.processedArticles()); n != 0 {
		t.Errorf("%d looped articles reached the processor, want 0", n)
	}
	if got := proc.rejectedIDs(); strings.Join(got, " ") != idIHave+" "+idTake {
		t.Errorf("remembered rejects %q, want both looped ids", got)
	}
	// the connection is still in sync
	code, line = c.Cmd("MODE READER")
	if err := client.ExpectCode(code, line, 200, 201); err != nil {
		t.Fatalf("MODE READER after the refusals: %v", err)
	}
}

// TestPeerInPatternsRefused: an article whose newsgroups the peer's in_patterns do not
// accept is refused and remembered; a "@" pattern refuses the whole crosspost, a "!"
// pattern only its own newsgroup.
func TestPeerInPatternsRefused(t *testing.T) {
	wanted := hGroup(t, "t8.pat.ok", 0, nil)
	excluded := hGroup(t, "t8.pat.no", 0, nil)
	poison := hGroup(t, "t8.pat.poison", 0, nil)
	patterns := "t8.pat.*,!" + excluded + ",@" + poison
	hs, proc, _ := peerFeedServer(t, patterns)
	c := hs.dial(t)

	// excluded on its own: nothing of the article is wanted
	idNo := hMessageID(excluded, 1)
	code, line := peerIHave(t, c, idNo, "upstream.invalid!not-for-mail", excluded)
	if err := client.ExpectCode(code, line, 437); err != nil {
		t.Fatalf("IHAVE of an excluded group: %v", err)
	}
	if !strings.Contains(line, "no newsgroup accepted") {
		t.Errorf("reply %q, want the in_patterns reason", line)
	}

	// poisoned: the crosspost is refused although one group is wanted
	idPoison := hMessageID(poison, 1)
	code, line = peerTakeThis(t, c, idPoison, "upstream.invalid!not-for-mail", wanted+","+poison)
	if err := client.ExpectCode(code, line, 439); err != nil {
		t.Fatalf("TAKETHIS of a poisoned crosspost: %v", err)
	}
	if !strings.Contains(line, "newsgroup not accepted") {
		t.Errorf("reply %q, want the poison reason", line)
	}

	if n := len(proc.processedArticles()); n != 0 {
		t.Errorf("%d refused articles reached the processor, want 0", n)
	}
	if got := proc.rejectedIDs(); strings.Join(got, " ") != idNo+" "+idPoison {
		t.Errorf("remembered rejects %q, want both refused ids", got)
	}

	// an excluded group next to a wanted one is accepted (the processor files what it
	// carries; the peer pattern only decides whether the article is wanted at all)
	idOK := hMessageID(wanted, 1)
	code, line = peerIHave(t, c, idOK, "upstream.invalid!not-for-mail", excluded+","+wanted)
	if err := client.ExpectCode(code, line, 235); err != nil {
		t.Fatalf("IHAVE of a partly wanted crosspost: %v", err)
	}
	if n := len(proc.processedArticles()); n != 1 {
		t.Fatalf("%d articles reached the processor, want 1", n)
	}
	if got := proc.rejectedIDs(); len(got) != 2 {
		t.Errorf("remembered rejects %q, want only the two refused ids", got)
	}
}

// TestPeerFeedRememberedRejects (plan nntp-peering): the permanent refusals this layer
// decides on its own are remembered, so the peer stops offering the id: an article over
// MaxArticleBytes, an unparsable one and a Message-ID that does not match the command.
// Refusals with no valid id (TAKETHIS of a malformed message-id) and the temporary answers
// are not remembered.
func TestPeerFeedRememberedRejects(t *testing.T) {
	g := hGroup(t, "t8.remember", 0, nil)
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	reg, _ := hPeerIP("")
	hs := newTestServer(t, hServerOpts{Proc: proc, Peers: reg, Hostname: peerFeedHost, MaxArtSize: 1024})
	c := hs.dial(t)

	big := strings.Repeat("x some filler to pass the byte limit\r\n", 100)
	idBig := hMessageID(g, 1)
	code, line := c.Cmd("IHAVE %s", idBig)
	if err := client.ExpectCode(code, line, 335); err != nil {
		t.Fatalf("IHAVE: %v", err)
	}
	if err := c.Send(strings.TrimSuffix(peerArticle(idBig, "upstream.invalid!x", g), ".\r\n") + big + ".\r\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
	code, line, err := c.ReadStatus()
	if err != nil || code != 437 || !strings.Contains(line, "too large") {
		t.Fatalf("IHAVE of an oversized article: code=%d line=%q err=%v, want 437 ... too large", code, line, err)
	}

	// IHAVE of an article whose Message-ID header is another one
	idMismatch := hMessageID(g, 2)
	code, line = c.Cmd("IHAVE %s", idMismatch)
	if err := client.ExpectCode(code, line, 335); err != nil {
		t.Fatalf("IHAVE: %v", err)
	}
	if err := c.Send(peerArticle(hMessageID(g, 3), "upstream.invalid!x", g)); err != nil {
		t.Fatalf("send: %v", err)
	}
	code, line, err = c.ReadStatus()
	if err != nil || code != 437 {
		t.Fatalf("IHAVE with a mismatching Message-ID: code=%d line=%q err=%v, want 437", code, line, err)
	}

	// TAKETHIS of the same two faults
	idBig2 := hMessageID(g, 4)
	if err := c.Send("TAKETHIS " + idBig2 + "\r\n" + strings.TrimSuffix(peerArticle(idBig2, "upstream.invalid!x", g), ".\r\n") + big + ".\r\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
	code, line, err = c.ReadStatus()
	if err != nil || code != 439 {
		t.Fatalf("TAKETHIS of an oversized article: code=%d line=%q err=%v, want 439", code, line, err)
	}
	idMismatch2 := hMessageID(g, 5)
	if err := c.Send("TAKETHIS " + idMismatch2 + "\r\n" + peerArticle(hMessageID(g, 6), "upstream.invalid!x", g)); err != nil {
		t.Fatalf("send: %v", err)
	}
	code, line, err = c.ReadStatus()
	if err != nil || code != 439 {
		t.Fatalf("TAKETHIS with a mismatching Message-ID: code=%d line=%q err=%v, want 439", code, line, err)
	}

	// a malformed message-id is refused but never remembered (history keys on valid ids)
	if err := c.Send("TAKETHIS not-an-id\r\n" + peerArticle(hMessageID(g, 7), "upstream.invalid!x", g)); err != nil {
		t.Fatalf("send: %v", err)
	}
	code, line, err = c.ReadStatus()
	if err != nil || code != 439 {
		t.Fatalf("TAKETHIS of a malformed id: code=%d line=%q err=%v, want 439", code, line, err)
	}

	want := []string{idBig, idMismatch, idBig2, idMismatch2}
	if got := proc.rejectedIDs(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("remembered rejects %q, want %q", got, want)
	}
	if n := len(proc.processedArticles()); n != 0 {
		t.Errorf("%d refused articles reached the processor, want 0", n)
	}
}

// TestPeerFeedDeniedWithoutAuthinfo (plan nntp-peering): with -plaintextauth refuse the
// client cannot send AUTHINFO on a plaintext connection, so a feed command answers 502
// there: asking for a login the peer cannot send would leave it with nothing to try.
func TestPeerFeedDeniedWithoutAuthinfo(t *testing.T) {
	reg, _, _ := hPeerLogin(t, "127.0.0.1")
	proc := &hFakeProcessor{checkResult: history.CasePass}

	// the control: with the default policy the same server answers 480
	accept := newTestServer(t, hServerOpts{Proc: proc, Peers: reg})
	code, line := accept.dial(t).Cmd("IHAVE <t8-denied-1@test.invalid>")
	if err := client.ExpectCode(code, line, 480); err != nil {
		t.Fatalf("IHAVE with AUTHINFO possible: %v", err)
	}

	refuse := newTestServer(t, hServerOpts{Proc: proc, Peers: reg})
	refuse.S.PlaintextAuth = PlaintextAuthRefuse
	c := refuse.dial(t)
	code, line = c.Cmd("IHAVE <t8-denied-2@test.invalid>")
	if err := client.ExpectCode(code, line, 502); err != nil {
		t.Fatalf("IHAVE with AUTHINFO refused on plaintext: %v", err)
	}
	code, line = c.Cmd("CHECK <t8-denied-3@test.invalid>")
	if err := client.ExpectCode(code, line, 502); err != nil {
		t.Fatalf("CHECK: %v", err)
	}
	code, line = c.Cmd("MODE STREAM")
	if err := client.ExpectCode(code, line, 502); err != nil {
		t.Fatalf("MODE STREAM: %v", err)
	}
}

// TestPeerFeedPathOnTheWire: an accepted feed article reaches the processor with our
// identity in front of its Path, and with "<token>.MISMATCH" when the peer did not write
// its own Path token.
func TestPeerFeedPathOnTheWire(t *testing.T) {
	g := hGroup(t, "t8.path", 0, nil)
	hs, proc, peer := peerFeedServer(t, "*")
	c := hs.dial(t)

	idMatch := hMessageID(g, 1)
	if code, line := peerIHave(t, c, idMatch, peer.PathToken+"!origin.invalid!not-for-mail", g); code != 235 {
		t.Fatalf("IHAVE with a matching Path token: %q", line)
	}
	idMismatch := hMessageID(g, 2)
	if code, line := peerIHave(t, c, idMismatch, "somewhere.else.invalid!not-for-mail", g); code != 235 {
		t.Fatalf("IHAVE with a foreign Path token: %q", line)
	}

	arts := proc.processedArticles()
	if len(arts) != 2 {
		t.Fatalf("%d articles processed, want 2", len(arts))
	}
	want := map[string]string{
		idMatch:    peerFeedHost + "!" + peer.PathToken + "!origin.invalid!not-for-mail",
		idMismatch: peerFeedHost + "!" + peer.PathToken + ".MISMATCH!somewhere.else.invalid!not-for-mail",
	}
	for _, a := range arts {
		got := common.HeaderValue(a.NNTPhead, "Path")
		if got != want[a.MessageID] {
			t.Errorf("%s: Path %q, want %q", a.MessageID, got, want[a.MessageID])
		}
		if strings.Contains(got, ".SEEN") {
			t.Errorf("%s: Path %q still carries a .SEEN element", a.MessageID, got)
		}
	}
}
