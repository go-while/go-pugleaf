package nntp

// Inbound peering (plan nntp-peering, slice peer-acl): which connections may feed us.
//
// Only a peer (table peers, internal/database/peers.go) may use IHAVE, CHECK, TAKETHIS and
// MODE STREAM. A connection becomes a peer connection either by its address (an enabled peer
// without in_username whose hosts hold the client IP: it may feed without AUTHINFO) or by a
// peer login (AUTHINFO USER/PASS against peers.in_username/in_password_hash, and, when that
// peer has hosts, only from one of them). nntp_users.posting means POST and nothing else.
//
// The peer list is cached in a PeerRegistry for peerListTTL and reloaded lazily on the next
// match, so an admin edit applies without a restart; resolved host names are cached for
// peerDNSTTL. The registry starts no goroutine, so there is nothing to stop on shutdown.

import (
	"context"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/peerstats"
)

// Cache windows and the DNS timeout of a PeerRegistry.
const (
	// peerListTTL is how long the enabled-peer list is reused before it is read again.
	peerListTTL = 30 * time.Second
	// peerListErrorTTL is the shorter window after a failed read: the main DB may be
	// locked for a moment, but a failing read must not run on every connection.
	peerListErrorTTL = 5 * time.Second
	// peerLogEvery rate-limits the "cannot read"/"cannot resolve" log lines, so a lasting
	// fault costs one line per minute instead of one per connection.
	peerLogEvery = time.Minute
	// peerDNSTTL is how long a resolved hosts entry is reused.
	peerDNSTTL = 5 * time.Minute
	// peerDNSRetry bounds how often one host name is resolved again after a miss, and is
	// also the single-flight window: while one resolution runs (or just ran) every other
	// connection answers from the cached addresses instead of resolving too.
	peerDNSRetry = 10 * time.Second
	// peerDNSGrace is how long the last successful answer still decides a match while the
	// resolver keeps failing. An authoritative "no such host" is not a failure: it clears
	// the addresses at once.
	peerDNSGrace = 3 * peerDNSTTL
	// peerDNSTimeout bounds one resolution, so a command never waits long on DNS.
	peerDNSTimeout = 2 * time.Second
)

// PeerStore is the part of *database.Database a PeerRegistry uses.
type PeerStore interface {
	// GetEnabledPeers returns the enabled peers (table peers).
	GetEnabledPeers() ([]*models.Peer, error)
	// AuthenticatePeer checks an AUTHINFO login against peers.in_username and
	// in_password_hash; it returns database.ErrPeerAuth for an unknown login, a wrong
	// password or a disabled peer.
	AuthenticatePeer(username, password string) (*models.Peer, error)
}

// PeerAllStore is the optional part of a PeerStore: the peer rows whether they are enabled
// or not, read in the same cached reload as the enabled ones. *database.Database implements
// it with GetPeers. A store without it leaves MatchAnyIP empty, so the denied feed commands
// of a disabled peer row are counted under peerstats.UnknownPeer instead of that row
// (plan peering-stats).
type PeerAllStore interface {
	// GetPeers returns every peer row ordered by name.
	GetPeers() ([]*models.Peer, error)
}

// peerDNSEntry is one cached hosts host name.
type peerDNSEntry struct {
	ips         []net.IP  // the addresses of the last successful resolution
	resolvedAt  time.Time // when that resolution happened (zero: none known)
	triedAt     time.Time // when a resolution was last started (also when it failed)
	resolving   bool      // a resolution of this name is running right now
	errLoggedAt time.Time
}

// PeerRegistry answers "is this connection a peer?" from the peers table. Its methods are
// safe on a nil receiver (a server without peers) and for concurrent use.
type PeerRegistry struct {
	store PeerStore

	mu          sync.Mutex
	peers       []*models.Peer // the last successfully loaded usable peers (read-only)
	all         []*models.Peer // the last successfully loaded rows, enabled or not (read-only)
	loadedAt    time.Time      // when the last read was attempted, successful or not
	loadFailed  bool           // that attempt failed: retry after peerListErrorTTL
	loading     bool           // a read is running; other callers use the last good list
	errLoggedAt time.Time      // rate limit of the read-error log line
	allLoggedAt time.Time      // the same for the GetPeers read of r.all
	names       string         // the loaded peer names, to log only real changes
	skipped     string         // the names skipped as unusable, likewise
	dns         map[string]*peerDNSEntry

	// Injection points for the tests (same package). resolve and now are never nil after
	// NewPeerRegistry.
	resolve func(ctx context.Context, host string) ([]net.IP, error)
	now     func() time.Time
}

// NewPeerRegistry returns a registry over store. A nil store (or a nil registry) matches
// nothing: no connection is a peer connection and no login is a peer login.
func NewPeerRegistry(store PeerStore) *PeerRegistry {
	return &PeerRegistry{
		store: store,
		dns:   make(map[string]*peerDNSEntry),
		resolve: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		now: time.Now,
	}
}

// Invalidate drops the cached peer list, so the next match reads the table again. The admin
// UI calls it after an edit; without it the edit applies within peerListTTL anyway.
func (r *PeerRegistry) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.loadFailed = false
	r.loadedAt = time.Time{}
	r.mu.Unlock()
}

// Enabled returns the enabled peers, reading the table again when the cache is stale, as a
// new slice (a startup log line, the admin UI). The models.Peer values it points at are
// shared with the live registry and must only be read.
func (r *PeerRegistry) Enabled() []*models.Peer {
	peers := r.enabledPeers()
	if len(peers) == 0 {
		return nil
	}
	return append(make([]*models.Peer, 0, len(peers)), peers...)
}

// enabledPeers returns the cached usable peers, reading the table again when the cache is
// stale (peerListTTL, or peerListErrorTTL after a failed read). The returned slice and the
// peers it holds are shared: callers only read them. The read runs outside the mutex, and
// only one at a time: every other caller meanwhile answers from the last good list (nil
// until the first successful read, so nothing may feed until the table could be read).
//
// The same reload also fills r.all with every row of a store that offers GetPeers
// (MatchAnyIP, plan peering-stats), so the two lists come from one read of the table.
func (r *PeerRegistry) enabledPeers() []*models.Peer {
	if r == nil || r.store == nil {
		return nil
	}
	r.mu.Lock()
	now := r.now()
	ttl := peerListTTL
	if r.loadFailed {
		ttl = peerListErrorTTL
	}
	if r.loading || (!r.loadedAt.IsZero() && now.Sub(r.loadedAt) < ttl) {
		peers := r.peers
		r.mu.Unlock()
		return peers
	}
	r.loading = true
	r.mu.Unlock()

	peers, err := r.store.GetEnabledPeers()
	var all []*models.Peer
	allErr := errNoPeerAllStore
	if s, ok := r.store.(PeerAllStore); ok {
		all, allErr = s.GetPeers()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.loading = false
	r.loadedAt = r.now()
	if allErr == nil {
		r.all = all
	} else if !errors.Is(allErr, errNoPeerAllStore) {
		// Only the Denied attribution of the statistics uses this list, so a failed read
		// keeps the last one and never stops the peer match below.
		if r.allLoggedAt.IsZero() || r.loadedAt.Sub(r.allLoggedAt) >= peerLogEvery {
			r.allLoggedAt = r.loadedAt
			log.Printf("[NNTP-PEER] cannot read all peer rows, keeping %d known row(s): %v", len(r.all), allErr)
		}
	}
	if err != nil {
		r.loadFailed = true
		if r.errLoggedAt.IsZero() || r.loadedAt.Sub(r.errLoggedAt) >= peerLogEvery {
			r.errLoggedAt = r.loadedAt
			log.Printf("[NNTP-PEER] cannot read the peer list, keeping %d known peer(s): %v", len(r.peers), err)
		}
		return r.peers
	}
	r.loadFailed = false
	usable, skipped := peerUsable(peers)
	r.peers = usable
	if names := peerNames(usable); names != r.names {
		r.names = names
		if names == "" {
			log.Printf("[NNTP-PEER] no enabled peers: nothing may feed this server")
		} else {
			log.Printf("[NNTP-PEER] enabled peers: %s", names)
		}
	}
	if names := peerNames(skipped); names != r.skipped {
		r.skipped = names
		if names != "" {
			log.Printf("[NNTP-PEER] peer(s) ignored, no path_token in their row: %s", names)
		}
	}
	return r.peers
}

// errNoPeerAllStore marks a store that does not offer GetPeers: nothing is logged for it.
var errNoPeerAllStore = errors.New("peer store without GetPeers")

// allPeers returns the cached peer rows, enabled or not, from the same reload as
// enabledPeers (nil for a store without GetPeers). The slice and its peers are shared:
// callers only read them.
func (r *PeerRegistry) allPeers() []*models.Peer {
	if r == nil || r.store == nil {
		return nil
	}
	r.enabledPeers() // one cached reload fills both lists
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.all
}

// peerUsable splits the loaded peers into the ones that may be matched and the ones whose
// row cannot be used. A peer without a path_token is skipped: there would be nothing to
// compare the incoming Path against and nothing to write into it, so it could never be fed
// correctly. database.ValidatePeer refuses such a row, so it can only come from a
// hand-edited table; skipping it (instead of guessing) keeps it out of the feed and the log
// line of enabledPeers tells the admin why.
func peerUsable(peers []*models.Peer) (usable, skipped []*models.Peer) {
	for _, p := range peers {
		if strings.TrimSpace(p.PathToken) == "" {
			skipped = append(skipped, p)
			continue
		}
		usable = append(usable, p)
	}
	return usable, skipped
}

// peerNames joins the peer names for the log lines of enabledPeers.
func peerNames(peers []*models.Peer) string {
	names := make([]string, 0, len(peers))
	for _, p := range peers {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// MatchIP returns the enabled peer that may feed from ip without a login (no in_username and
// hosts that hold ip), or nil. The first match in name order wins.
func (r *PeerRegistry) MatchIP(ip net.IP) *models.Peer {
	if ip == nil {
		return nil
	}
	for _, p := range r.enabledPeers() {
		if p.InUsername != "" || p.Hosts == "" {
			continue
		}
		if r.hostsMatch(p, ip) {
			return p
		}
	}
	return nil
}

// MatchAnyIP returns the peer row whose hosts hold ip, enabled or not, or nil. The first
// match in name order wins. It answers "which peer row is this address configured as?" for
// the statistics of a feed command that was denied (plan peering-stats): a row without
// hosts (a peer that may only log in) can never be recognised by its address, so it never
// matches. It decides nothing about permissions; MatchIP does that.
func (r *PeerRegistry) MatchAnyIP(ip net.IP) *models.Peer {
	if ip == nil {
		return nil
	}
	for _, p := range r.allPeers() {
		if p == nil || p.Hosts == "" {
			continue
		}
		if r.hostsMatch(p, ip) {
			return p
		}
	}
	return nil
}

// LoginPossible reports whether an AUTHINFO could still make a connection from ip a peer
// connection: some enabled peer has a login and either no hosts or hosts that hold ip. It
// decides between 480 and 502 on the feed commands.
func (r *PeerRegistry) LoginPossible(ip net.IP) bool {
	for _, p := range r.enabledPeers() {
		if p.InUsername == "" {
			continue
		}
		if p.Hosts == "" || r.hostsMatch(p, ip) {
			return true
		}
	}
	return false
}

// Login handles AUTHINFO PASS for a peer. isPeerLogin reports whether username is the login
// of an enabled peer; only then was this a peer login and the nntp_users path must not run
// (one login name belongs to one side). peer is the authenticated peer, or nil when the
// password did not match, the peer was disabled meanwhile, or its hosts do not hold ip.
func (r *PeerRegistry) Login(username, password string, ip net.IP) (peer *models.Peer, isPeerLogin bool) {
	if r == nil || r.store == nil || username == "" {
		return nil, false
	}
	known := false
	for _, p := range r.enabledPeers() {
		if p.InUsername != "" && p.InUsername == username {
			known = true
			break
		}
	}
	if !known {
		return nil, false
	}
	p, err := r.store.AuthenticatePeer(username, password)
	if err != nil {
		return nil, true
	}
	if p.Hosts != "" && !r.hostsMatch(p, ip) {
		log.Printf("[NNTP-PEER] peer %q logged in from %s, which its hosts do not hold: refused", p.Name, ip)
		return nil, true
	}
	return p, true
}

// hostsMatch reports whether ip is one of p's hosts entries. Addresses and CIDRs are matched
// without any lookup; host names are resolved through the registry's DNS cache, so a peer
// with a literal address never waits on DNS.
func (r *PeerRegistry) hostsMatch(p *models.Peer, ip net.IP) bool {
	if ip == nil {
		return false
	}
	var names []string
	for _, h := range database.ParsePeerHosts(p.Hosts) {
		if hip := net.ParseIP(h); hip != nil {
			if hip.Equal(ip) {
				return true
			}
			continue
		}
		if _, cidr, err := net.ParseCIDR(h); err == nil {
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		names = append(names, h)
	}
	for _, name := range names {
		if r.nameHasIP(name, ip) {
			return true
		}
	}
	return false
}

// nameHasIP reports whether the host name name resolves to ip. A cached answer younger than
// peerDNSTTL decides at once; otherwise one connection resolves the name again and every
// other one answers from the cached addresses meanwhile (single flight, bounded by
// peerDNSRetry), so a burst of connections costs one lookup and only one of them can wait
// on the resolver. An address change is therefore picked up without a restart, and a
// resolver outage neither stalls the accept path nor cuts the peer off at once.
func (r *PeerRegistry) nameHasIP(name string, ip net.IP) bool {
	r.mu.Lock()
	now := r.now()
	if r.dns == nil {
		r.dns = make(map[string]*peerDNSEntry)
	}
	e := r.dns[name]
	if e == nil {
		e = &peerDNSEntry{}
		r.dns[name] = e
	}
	have := peerDNSHas(e, ip, now)
	fresh := !e.resolvedAt.IsZero() && now.Sub(e.resolvedAt) < peerDNSTTL
	// Claim the resolution under the lock (resolving plus triedAt): from here on every
	// other caller takes the branch below until this one is done.
	if (fresh && have) || e.resolving || (!e.triedAt.IsZero() && now.Sub(e.triedAt) < peerDNSRetry) {
		r.mu.Unlock()
		return have
	}
	e.resolving, e.triedAt = true, now
	r.mu.Unlock()

	// Resolved outside the lock: a slow resolver must not block other connections.
	ctx, cancel := context.WithTimeout(context.Background(), peerDNSTimeout)
	defer cancel()
	ips, err := r.resolve(ctx, name)

	r.mu.Lock()
	defer r.mu.Unlock()
	now = r.now()
	e.resolving, e.triedAt = false, now
	if err == nil {
		e.ips, e.resolvedAt, e.errLoggedAt = ips, now, time.Time{}
		return peerDNSHas(e, ip, now)
	}
	// The log line of a lasting fault is rate-limited: the retry runs every peerDNSRetry
	// as long as connections keep arriving.
	logIt := e.errLoggedAt.IsZero() || now.Sub(e.errLoggedAt) >= peerLogEvery
	if logIt {
		e.errLoggedAt = now
	}
	if peerDNSNotFound(err) {
		// Authoritative: the name has no address any more, so the peer has none either.
		had := !e.resolvedAt.IsZero()
		e.ips, e.resolvedAt = nil, time.Time{}
		if logIt {
			if had {
				log.Printf("[NNTP-PEER] peer host %q no longer resolves, dropping its addresses: %v", name, err)
			} else {
				log.Printf("[NNTP-PEER] peer host %q does not resolve: %v", name, err)
			}
		}
		return false
	}
	// A resolver fault (timeout, SERVFAIL, no network): keep the last answer, but only
	// within peerDNSGrace, so a peer cannot stay matched by a stale address for ever.
	if logIt {
		log.Printf("[NNTP-PEER] cannot resolve peer host %q, using the last answer for at most %s: %v", name, peerDNSGrace, err)
	}
	return peerDNSHas(e, ip, now)
}

// peerDNSHas reports whether the cached answer of e may decide that ip is one of the name's
// addresses: there has to be an answer, and it must not be older than peerDNSGrace (within
// peerDNSTTL it is fresh; between the two it is only used while a re-resolution fails).
func peerDNSHas(e *peerDNSEntry, ip net.IP, now time.Time) bool {
	if e == nil || e.resolvedAt.IsZero() || now.Sub(e.resolvedAt) >= peerDNSGrace {
		return false
	}
	for _, c := range e.ips {
		if c.Equal(ip) {
			return true
		}
	}
	return false
}

// peerDNSNotFound reports whether err is an authoritative "no such host" (NXDOMAIN or a
// name without addresses), as opposed to a resolver fault.
func peerDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// ---------------------------------------------------------------------------
// Connection side

// remoteIP returns the IP of the client, nil when the connection has no host:port address
// (net.Pipe in the tests).
func (c *ClientConnection) remoteIP() net.IP {
	if c.conn == nil || c.conn.RemoteAddr() == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(c.conn.RemoteAddr().String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// resolvePeer looks the connection up in the peer registry before the greeting: an enabled
// peer without a login whose hosts hold this address may feed at once. Peers with a login
// become peer connections in handleAuthInfo.
func (c *ClientConnection) resolvePeer() {
	if c.server == nil || c.peer != nil {
		return
	}
	if p := c.server.Peers.MatchIP(c.remoteIP()); p != nil {
		c.peer = p
		log.Printf("[NNTP-PEER] %s may feed as peer %q (matched by address)", c.conn.RemoteAddr(), p.Name)
	}
}

// peerName is the matched peer's name, "" on a connection that is no peer (for log lines).
func (c *ClientConnection) peerName() string {
	if c.peer == nil {
		return ""
	}
	return c.peer.Name
}

// ---------------------------------------------------------------------------
// Incoming peer statistics (plan peering-stats, slice stats-in)
//
// Every peer connection and every connection whose feed command is denied counts for one
// peerstats.PeerRef: the peer it feeds as, the peer row its address is configured as (a
// disabled row that tried) or peerstats.UnknownPeer. The counters of the connection are
// kept here as well, so Closed can report the totals of that one connection.
//
// Only the connection's own goroutine touches connPeerStats, so it needs no lock, and the
// collector is called without holding any lock of this package.

// peerStatsCheckedMax bounds the set of message-ids a connection CHECKed, so a long
// streaming session cannot grow it without end. When an id falls out of the set, the
// TAKETHIS that follows its CHECK counts as an offer of its own.
const peerStatsCheckedMax = 4096

// connPeerStats are the peer statistics of one client connection.
type connPeerStats struct {
	ref     peerstats.PeerRef
	total   peerstats.Counters  // everything counted on this connection, for Closed
	started time.Time           // when the connection began to count for ref
	checked map[string]struct{} // ids CHECKed here and not transferred yet
	order   []string            // insertion order of checked, to bound it
}

// peerStatsCollector is the server's collector, peerstats.Nop when there is none.
func (c *ClientConnection) peerStatsCollector() peerstats.Collector {
	if c == nil || c.server == nil {
		return peerstats.Nop
	}
	return c.server.PeerStats()
}

// peerStatsPeer opens the counters of a peer connection (the address match of resolvePeer
// or a peer login): Connected once, as soon as the connection became a peer connection.
func (c *ClientConnection) peerStatsPeer() {
	if c.peer == nil {
		return
	}
	ref := peerstats.PeerRef{ID: c.peer.ID, Name: c.peer.Name}
	if c.pstats != nil && c.pstats.ref == ref {
		return
	}
	c.peerStatsOpen(ref)
}

// peerStatsOpen starts counting this connection for ref. A connection that counted for
// another ref before (a denial attributed to a peer row that was enabled afterwards) is
// closed out first, so its counters are not mixed up.
func (c *ClientConnection) peerStatsOpen(ref peerstats.PeerRef) {
	if c.pstats != nil {
		if c.pstats.ref == ref {
			return
		}
		c.peerStatsClose()
	}
	c.pstats = &connPeerStats{ref: ref, started: time.Now()}
	remote := ""
	if c.conn != nil && c.conn.RemoteAddr() != nil {
		remote = c.conn.RemoteAddr().String()
	}
	c.peerStatsCollector().Connected(ref, peerstats.In, remote)
}

// peerStatsClose ends the counting of this connection (Closed with its totals). It is
// called when the session ends and does nothing on a connection that never counted.
func (c *ClientConnection) peerStatsClose() {
	ps := c.pstats
	if ps == nil {
		return
	}
	c.pstats = nil
	c.peerStatsCollector().Closed(ps.ref, peerstats.In, ps.total, time.Since(ps.started))
}

// peerStatsAdd counts delta for this connection. A peer connection that has not been
// opened yet is opened here, so no counter is lost without its Connected.
func (c *ClientConnection) peerStatsAdd(delta peerstats.Counters) {
	if delta.IsZero() {
		return
	}
	c.peerStatsPeer()
	ps := c.pstats
	if ps == nil {
		return
	}
	ps.total.Add(delta)
	c.peerStatsCollector().Add(ps.ref, peerstats.In, delta)
}

// peerStatsDenied counts one feed command (IHAVE, CHECK, TAKETHIS, MODE STREAM) that was
// refused because this connection is no enabled peer (502/480). It is attributed to the
// peer row this address is configured as, enabled or not, and to peerstats.UnknownPeer
// when no row holds it; the first denial opens the counters of the connection, so a plain
// reader that never sends a feed command produces no event at all.
func (c *ClientConnection) peerStatsDenied() {
	if c.server == nil {
		return
	}
	if c.pstats == nil {
		ref := peerstats.UnknownPeer
		if p := c.server.Peers.MatchAnyIP(c.remoteIP()); p != nil {
			ref = peerstats.PeerRef{ID: p.ID, Name: p.Name}
		}
		c.peerStatsOpen(ref)
	}
	c.peerStatsAdd(peerstats.Counters{Denied: 1})
}

// peerStatsOffer reports whether this feed command is a new offer and remembers it: a CHECK
// and the TAKETHIS (or IHAVE) of the same id on the same connection are one offer, as in
// innd. check says whether the command was a CHECK; messageID is already valid, "" when the
// command carried none.
func (c *ClientConnection) peerStatsOffer(check bool, messageID string) bool {
	c.peerStatsPeer()
	ps := c.pstats
	if ps == nil {
		return false
	}
	if messageID == "" {
		return true
	}
	if _, ok := ps.checked[messageID]; ok {
		if !check {
			// The transfer of a CHECKed id: it was counted as an offer by the CHECK, and
			// the next CHECK of the same id starts a new offer.
			delete(ps.checked, messageID)
		}
		return false
	}
	if check {
		if ps.checked == nil {
			ps.checked = make(map[string]struct{})
		}
		if len(ps.order) >= peerStatsCheckedMax {
			delete(ps.checked, ps.order[0])
			ps.order = append(ps.order[:0], ps.order[1:]...)
		}
		ps.checked[messageID] = struct{}{}
		ps.order = append(ps.order, messageID)
	}
	return true
}

// peerFeedCounters maps one answer to a feed command to the counters of the peer that sent
// it (plan peering-stats, "Counting rules"): offer says whether the command was a new
// offer, code is the reply code, size the wire size of the article when it was read and dup
// whether the store found the transferred article to be a duplicate (counted as Duplicate
// instead of Rejected, as innd counts it).
func peerFeedCounters(offer bool, code int, size int64, dup bool) peerstats.Counters {
	var d peerstats.Counters
	if offer {
		d.Offered = 1
	}
	switch {
	case dup:
		d.Duplicate, d.DupBytes = 1, size
	case code == 235 || code == 239:
		d.Accepted, d.AccBytes = 1, size
	case code == 435 || code == 438:
		d.Refused = 1
	case code == 437 || code == 439:
		d.Rejected, d.RejBytes = 1, size
	case code == 436 || code == 431:
		d.Deferred = 1
	}
	return d
}

// feedWireSize is the wire size of an article a peer transferred, 0 when none was read.
func feedWireSize(head, body []string) int64 {
	if len(head) == 0 && len(body) == 0 {
		return 0
	}
	return int64(common.WireArticleSize(head, body))
}

// ---------------------------------------------------------------------------
// Path (RFC 5537 §3.2.1)

// feedPath prepends this server's path identity to the Path of an article received from a
// peer (INN convention): when the leftmost Path element is the peer's path_token the article
// came the way the peer claims and only "<identity>!" is prepended; otherwise the token the
// peer should have written is inserted as "<token>.MISMATCH!". Without an identity or without
// a Path header the head is returned unchanged (RFC 5537 §3.6: no Path is invented). A peer
// without a path_token never gets here: peerUsable keeps such a row out of the registry, so
// it cannot feed at all.
func (c *ClientConnection) feedPath(head []string) []string {
	identity := c.pathIdentity()
	if identity == "" || len(common.HeaderValues(head, "Path")) == 0 {
		return head
	}
	old := common.HeaderValue(head, "Path")
	prefix := identity + "!"
	if token := c.peerPathToken(); token != "" && !strings.EqualFold(pathLeftmost(old), token) {
		prefix = identity + "!" + token + ".MISMATCH!"
	}
	return common.ReplaceHeader(head, "Path", prefix+old)
}

// peerPathToken is the matched peer's Path token, "" on a connection that is no peer.
func (c *ClientConnection) peerPathToken() string {
	if c.peer == nil {
		return ""
	}
	return strings.TrimSpace(c.peer.PathToken)
}

// pathLeftmost returns the leftmost element of a Path header value ("" when there is none).
func pathLeftmost(path string) string {
	if i := strings.IndexByte(path, '!'); i >= 0 {
		path = path[:i]
	}
	return strings.TrimSpace(path)
}

// pathHasIdentity reports whether path already holds identity as one of its elements: the
// article has been here before and is a loop (RFC 5537 §3.2.1). Elements are compared without
// case and without the keyword suffixes this and other servers append, so "ours.MISMATCH",
// "ours.SEEN.192.0.2.1" and "ours.POSTED" all count as "ours".
func pathHasIdentity(path, identity string) bool {
	if identity == "" || path == "" {
		return false
	}
	for _, el := range strings.Split(path, "!") {
		if strings.EqualFold(pathElementHost(el), identity) {
			return true
		}
	}
	return false
}

// pathElementHost strips the RFC 5537 §3.2.1 keyword suffix of one Path element: the first
// dot-separated component that is exactly "SEEN", "MISMATCH" or "POSTED" (case-insensitive)
// and everything after it, which covers both "ours.MISMATCH" and "ours!.SEEN.192.0.2.1".
// Only a whole component counts, so a host named "ours.seenix.example" or "ours.mismatched"
// keeps its name and is not read as this server.
func pathElementHost(element string) string {
	el := strings.TrimSpace(element)
	start := 0
	for i := 0; i <= len(el); i++ {
		if i < len(el) && el[i] != '.' {
			continue
		}
		// el[start:i] is one component; start > 0 means it follows a '.'
		if start > 0 && pathKeyword(el[start:i]) {
			return el[:start-1]
		}
		start = i + 1
	}
	return el
}

// PathElementHost is pathElementHost under an exported name, for the packages that have to
// read a Path the same way this one does (internal/feed: the outgoing feed skips a peer
// whose path_token the Path already names). It changes nothing about the behaviour.
func PathElementHost(element string) string {
	return pathElementHost(element)
}

// PathHasElement is pathHasIdentity under its general name: it reports whether path holds
// host as one of its elements, ignoring case and the RFC 5537 §3.2.1 keyword suffixes, so
// "host.MISMATCH" and "host.SEEN.192.0.2.1" both count. The inbound loop check uses it with
// this server's identity; the outgoing feed (internal/feed) uses it with a peer's path_token.
func PathHasElement(path, host string) bool {
	return pathHasIdentity(path, host)
}

// pathKeyword reports whether component is one of the RFC 5537 §3.2.1 Path keywords.
func pathKeyword(component string) bool {
	return strings.EqualFold(component, "SEEN") ||
		strings.EqualFold(component, "MISMATCH") ||
		strings.EqualFold(component, "POSTED")
}

// ---------------------------------------------------------------------------
// Feed checks the NNTP layer decides (the processor never sees these articles)

// rememberReject records messageID as permanently refused by this layer, so the peer stops
// offering it (plan nntp-peering; the history index keeps it for HistoryRejectDays). It is
// called for the refusals the NNTP layer decides on its own: the Path loop, the peer's
// in_patterns, an article over MaxArticleBytes, an unparsable article and a Message-ID that
// does not match the command. Not for the refusals the processor decides (it remembers
// those itself), not for temporary failures (436/431/400) and not for a duplicate (history
// already knows the id). messageID is always a valid message-id at the call sites.
func (c *ClientConnection) rememberReject(messageID string) {
	if c.server == nil || c.server.Processor == nil || messageID == "" {
		return
	}
	c.server.Processor.RememberReject(messageID)
}

// feedCheck runs the checks on an article offered by IHAVE or TAKETHIS that this layer
// decides: the Path loop check and the peer's in_patterns. It returns "" when the article is
// wanted and otherwise the reason for the 437/439 reply (a fixed string, never client input).
// The caller also remembers the message-id as rejected, so the peer stops offering it.
func (c *ClientConnection) feedCheck(head []string) string {
	if pathHasIdentity(common.HeaderValue(head, "Path"), c.pathIdentity()) {
		return "path loop"
	}
	patterns := "*"
	if c.peer != nil {
		patterns = c.peer.InPatterns
	}
	groups, err := common.ParseNewsgroups(common.HeaderValue(head, "Newsgroups"))
	if err != nil || len(groups) == 0 {
		return "" // the intake checks the Newsgroups header and answers for it
	}
	res := MatchNewsgroupPatterns(patterns, groups)
	if res.Poisoned {
		return "newsgroup not accepted from this peer"
	}
	if len(res.Wanted) == 0 {
		return "no newsgroup accepted from this peer"
	}
	return ""
}
