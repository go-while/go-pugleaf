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
	// peerDNSTTL is how long a resolved hosts entry is reused.
	peerDNSTTL = 5 * time.Minute
	// peerDNSRetry bounds how often one host name is resolved again after a miss, so an
	// address that matches no peer cannot turn into one DNS lookup per connection.
	peerDNSRetry = 10 * time.Second
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
	ips        []net.IP  // the addresses of the last successful resolution
	resolvedAt time.Time // when that resolution happened (zero: never)
	triedAt    time.Time // when a resolution was last attempted (also on failure)
}

// PeerRegistry answers "is this connection a peer?" from the peers table. Its methods are
// safe on a nil receiver (a server without peers) and for concurrent use.
type PeerRegistry struct {
	store PeerStore

	mu       sync.Mutex
	peers    []*models.Peer // the last loaded enabled peers (read-only for callers)
	loaded   bool
	loadedAt time.Time
	names    string // the loaded peer names, to log only real changes
	dns      map[string]*peerDNSEntry

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
	r.loaded = false
	r.mu.Unlock()
}

// Enabled returns the enabled peers, reading the table again when the cache is stale. The
// result and the peers it holds are shared and read-only (a startup log line, the admin UI).
func (r *PeerRegistry) Enabled() []*models.Peer {
	return r.enabledPeers()
}

// enabledPeers returns the cached enabled peers, reloading them when the cache is older than
// peerListTTL. The result and the peers it holds are shared: callers only read them. A failed
// read keeps the previous list and is retried after peerListTTL.
func (r *PeerRegistry) enabledPeers() []*models.Peer {
	if r == nil || r.store == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.loaded && now.Sub(r.loadedAt) < peerListTTL {
		return r.peers
	}
	peers, err := r.store.GetEnabledPeers()
	r.loadedAt = now
	if err != nil {
		log.Printf("[NNTP-PEER] failed to read the peer list: %v", err)
		return r.peers // nil until the first successful read
	}
	r.peers, r.loaded = peers, true
	if names := peerNames(peers); names != r.names {
		r.names = names
		if names == "" {
			log.Printf("[NNTP-PEER] no enabled peers: nothing may feed this server")
		} else {
			log.Printf("[NNTP-PEER] enabled peers: %s", names)
		}
	}
	return r.peers
}

// peerNames joins the peer names for the log line of enabledPeers.
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
// peerDNSTTL is used; a miss on it resolves again (at most once per peerDNSRetry per name),
// so an address change is picked up without a restart. A failed resolution keeps the last
// addresses, so a DNS outage does not cut a peer off.
func (r *PeerRegistry) nameHasIP(name string, ip net.IP) bool {
	r.mu.Lock()
	now := r.now()
	e := r.dns[name]
	if e != nil {
		fresh := !e.resolvedAt.IsZero() && now.Sub(e.resolvedAt) < peerDNSTTL
		if fresh && peerIPsHave(e.ips, ip) {
			r.mu.Unlock()
			return true
		}
		if now.Sub(e.triedAt) < peerDNSRetry {
			have := peerIPsHave(e.ips, ip)
			r.mu.Unlock()
			return have // too soon to ask again
		}
	}
	r.mu.Unlock()

	// Resolved outside the lock: a slow resolver must not block other connections.
	ctx, cancel := context.WithTimeout(context.Background(), peerDNSTimeout)
	defer cancel()
	ips, err := r.resolve(ctx, name)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dns == nil {
		r.dns = make(map[string]*peerDNSEntry)
	}
	e = r.dns[name]
	if e == nil {
		e = &peerDNSEntry{}
		r.dns[name] = e
	}
	e.triedAt = r.now()
	if err != nil {
		log.Printf("[NNTP-PEER] cannot resolve peer host %q: %v", name, err)
		return peerIPsHave(e.ips, ip) // keep the last known addresses
	}
	e.ips, e.resolvedAt = ips, e.triedAt
	return peerIPsHave(e.ips, ip)
}

// peerIPsHave reports whether ips holds ip.
func peerIPsHave(ips []net.IP, ip net.IP) bool {
	for _, c := range ips {
		if c.Equal(ip) {
			return true
		}
	}
	return false
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
// a Path header the head is returned unchanged (RFC 5537 §3.6: no Path is invented).
func (c *ClientConnection) feedPath(head []string) []string {
	identity := c.pathIdentity()
	if identity == "" || len(common.HeaderValues(head, "Path")) == 0 {
		return head
	}
	old := common.HeaderValue(head, "Path")
	prefix := identity + "!"
	if c.peer != nil && c.peer.PathToken != "" && !strings.EqualFold(pathLeftmost(old), c.peer.PathToken) {
		prefix = identity + "!" + c.peer.PathToken + ".MISMATCH!"
	}
	return common.ReplaceHeader(head, "Path", prefix+old)
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

// pathElementHost strips the RFC 5537 §3.2.1 keyword suffixes (".SEEN", ".MISMATCH",
// ".POSTED", case-insensitive) and everything after them from one Path element.
func pathElementHost(element string) string {
	el := strings.TrimSpace(element)
	up := strings.ToUpper(el)
	cut := -1
	for _, kw := range []string{".SEEN", ".MISMATCH", ".POSTED"} {
		if i := strings.Index(up, kw); i >= 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if cut >= 0 {
		el = el[:cut]
	}
	return el
}

// ---------------------------------------------------------------------------
// Feed checks the NNTP layer decides (the processor never sees these articles)

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
