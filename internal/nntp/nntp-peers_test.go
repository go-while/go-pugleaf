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
// match read it again, and a failed read keeps the list that was there.
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
	store.mu.Lock()
	store.peers = nil
	store.mu.Unlock()
	clock.advance(peerListTTL)
	if p := reg.MatchIP(ip); p != nil {
		t.Fatalf("peer %q still matched after it was removed", p.Name)
	}
	if loads, _ := store.counters(); loads != 2 {
		t.Fatalf("GetEnabledPeers called %d times, want 2", loads)
	}

	// Invalidate applies an edit at once
	store.mu.Lock()
	store.peers = []*models.Peer{peerSpec("ip4", "192.0.2.10", "")}
	store.mu.Unlock()
	if reg.MatchIP(ip) != nil {
		t.Fatal("the cached empty list was not used")
	}
	reg.Invalidate()
	if reg.MatchIP(ip) == nil {
		t.Fatal("Invalidate did not make the registry read the table again")
	}

	// a failed read keeps the peers that were loaded
	store.mu.Lock()
	store.loadErr = errors.New("main DB locked")
	store.mu.Unlock()
	clock.advance(peerListTTL)
	if reg.MatchIP(ip) == nil {
		t.Fatal("a failed read dropped the known peers")
	}
}

// TestPeerDNSCache: a resolved hosts name is cached for peerDNSTTL, a miss resolves again
// at most once per peerDNSRetry, and a failed resolution keeps the last addresses.
func TestPeerDNSCache(t *testing.T) {
	store := &peerTestStore{peers: []*models.Peer{peerSpec("byname", "peer.test.invalid", "")}}
	clock := peerTestClockAt()
	var mu sync.Mutex
	lookups := 0
	ips := []net.IP{net.ParseIP("192.0.2.77")}
	var lookupErr error
	reg := peerTestRegistry(store, clock, func(string) ([]net.IP, error) {
		mu.Lock()
		defer mu.Unlock()
		lookups++
		if lookupErr != nil {
			return nil, lookupErr
		}
		return ips, nil
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return lookups
	}
	hit, miss := net.ParseIP("192.0.2.77"), net.ParseIP("192.0.2.78")

	if reg.MatchIP(hit) == nil {
		t.Fatal("first match failed")
	}
	if n := count(); n != 1 {
		t.Fatalf("%d lookups for the first match, want 1", n)
	}
	// a hit inside the TTL costs no lookup
	clock.advance(peerDNSTTL / 2)
	if reg.MatchIP(hit) == nil || count() != 1 {
		t.Fatalf("a cached hit cost %d lookups", count())
	}
	// a miss resolves again, but not once per connection
	for i := 0; i < 5; i++ {
		if reg.MatchIP(miss) != nil {
			t.Fatal("an address that is not the peer's matched")
		}
	}
	if n := count(); n != 2 {
		t.Fatalf("%d lookups for 5 misses, want 2 (one retry, then the retry window)", n)
	}
	// after the retry window a miss may ask again
	clock.advance(peerDNSRetry)
	if reg.MatchIP(miss) != nil {
		t.Fatal("miss matched")
	}
	if n := count(); n != 3 {
		t.Fatalf("%d lookups after the retry window, want 3", n)
	}
	// the peer moved: the new address is picked up without a restart
	mu.Lock()
	ips = []net.IP{miss}
	mu.Unlock()
	clock.advance(peerDNSRetry)
	if reg.MatchIP(miss) == nil {
		t.Fatal("the peer's new address was not picked up")
	}
	// a DNS failure keeps the last known addresses
	mu.Lock()
	lookupErr = errors.New("server misbehaving")
	mu.Unlock()
	clock.advance(peerDNSTTL)
	if reg.MatchIP(miss) == nil {
		t.Fatal("a DNS failure cut the peer off although its last address was known")
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
