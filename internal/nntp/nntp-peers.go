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
	loadedAt    time.Time      // when the last read was attempted, successful or not
	loadFailed  bool           // that attempt failed: retry after peerListErrorTTL
	loading     bool           // a read is running; other callers use the last good list
	errLoggedAt time.Time      // rate limit of the read-error log line
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

	r.mu.Lock()
	defer r.mu.Unlock()
	r.loading = false
	r.loadedAt = r.now()
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
