package feed

// The outgoing feed sender (plan nntp-peering wave 3, slice feed-sender): one goroutine set per
// peer with out_enabled, each with up to max_conns persistent connections that drain the queue
// of that peer to it. The protocol side is sender_conn.go (the internal/nntp client); this file
// is the peer bookkeeping: which peers are fed, claiming and acknowledging queue items, the
// retry backoff and the per-peer counters.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/peerstats"
)

// Defaults of the Config fields; a zero field takes the default.
const (
	// DefaultPollInterval is how long a connection worker waits before it claims again after
	// it found nothing to send.
	DefaultPollInterval = 5 * time.Second
	// DefaultReloadInterval is how often the peer list is read again (a peer that gains or
	// loses out_enabled is picked up within this).
	DefaultReloadInterval = 30 * time.Second
	// DefaultStatsInterval is how often the counters are written and logged.
	DefaultStatsInterval = time.Minute
	// DefaultLease is the lease of a claimed item: a crashed sender's rows come back after it.
	DefaultLease = 10 * time.Minute
	// DefaultBatch is how many items one connection claims at once; it is also the bound of
	// the CHECK/TAKETHIS commands in flight on that connection.
	DefaultBatch = 50
	// DefaultBatchBytes bounds what one send of a batch puts on the wire: a claimed batch is
	// split into runs of at most this many octets, so a batch of large articles does not sit
	// in one pipeline.
	DefaultBatchBytes = 8 << 20
	// DefaultBackoffBase is the delay before the first retry of a deferred item and after the
	// first failed connect.
	DefaultBackoffBase = 30 * time.Second
	// DefaultBackoffMax caps the doubling of DefaultBackoffBase.
	DefaultBackoffMax = 30 * time.Minute
	// DefaultIdleClose is how long a connection is kept open with nothing to send. It stays
	// below DefaultReadTimeout, so an idle connection is closed here instead of being torn
	// down by the idle read timeout.
	DefaultIdleClose = 30 * time.Second
	// DefaultConnectTimeout bounds dial, TLS handshake, greeting and AUTHINFO of one connect.
	// A connect cannot be interrupted, so this is the longest a Stop or a peer reload waits.
	DefaultConnectTimeout = 10 * time.Second
	// DefaultReadTimeout and DefaultWriteTimeout are the idle timeouts of one connection,
	// shorter than the client's own defaults: they are the last resort behind CloseNow for a
	// peer that stops answering.
	DefaultReadTimeout  = time.Minute
	DefaultWriteTimeout = time.Minute
)

// PeerSource lists the configured peers, enabled or not. *database.Database implements it with
// GetPeers. The sender feeds a peer that is enabled and has out_enabled, and it keeps the queue
// of every peer whose row still has out_enabled: a peer that is only disabled (its inbound
// switch) keeps what is queued for it, so one click on the admin page does not throw a feed
// away.
type PeerSource interface {
	GetPeers() ([]*models.Peer, error)
}

// ArticleSource reads stored articles. *database.Database implements it with GetArticlesByIDs:
// the sender asks per newsgroup, with the message-ids of one claimed batch.
type ArticleSource interface {
	GetArticlesByIDs(newsgroup *string, wantedIDs []*string) ([]*models.Article, error)
}

// Clock is the sender's view of the time, so a test can pin it (the next attempt of a deferred
// item is computed from it).
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// queuePruner and queuePrunerCount are the two shapes the queue's Prune may have (queue.go is
// written in a parallel slice): the sender calls whichever the queue offers, once per peer
// reload, and nothing at all when it offers neither.
type queuePruner interface {
	Prune(now time.Time) error
}

type queuePrunerCount interface {
	Prune(now time.Time) (int64, error)
}

// Config configures a Sender. Queue, Peers and Articles are required; every duration and count
// takes its default when zero.
type Config struct {
	Queue    Queue         // the durable per-peer queue (queue.go)
	Peers    PeerSource    // the peer list, re-read every ReloadInterval
	Articles ArticleSource // where the queued articles are read from
	Hostname string        // our Path identity, prepended to the relayed Path exactly once

	Clock Clock  // nil: the real time
	Dial  Dialer // nil: DialPeer with the timeouts below
	// Stats counts the outgoing traffic per peer and hour (plan peering-stats); nil counts
	// nothing (peerstats.Nop). The per-peer counters of Stats/feed_stats are kept as well.
	Stats peerstats.Collector

	PollInterval   time.Duration
	ReloadInterval time.Duration
	StatsInterval  time.Duration
	Lease          time.Duration
	Batch          int
	BatchBytes     int
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	IdleClose      time.Duration
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration // 0: DefaultReadTimeout, <0: no idle read timeout
	WriteTimeout   time.Duration // 0: DefaultWriteTimeout, <0: no idle write timeout
}

// withDefaults returns cfg with every zero field filled.
func (cfg Config) withDefaults() Config {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Stats == nil {
		cfg.Stats = peerstats.Nop
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.ReloadInterval <= 0 {
		cfg.ReloadInterval = DefaultReloadInterval
	}
	if cfg.StatsInterval <= 0 {
		cfg.StatsInterval = DefaultStatsInterval
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultLease
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultBatch
	}
	if cfg.BatchBytes <= 0 {
		cfg.BatchBytes = DefaultBatchBytes
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = DefaultReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = DefaultBackoffBase
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = DefaultBackoffMax
	}
	if cfg.BackoffMax < cfg.BackoffBase {
		cfg.BackoffMax = cfg.BackoffBase
	}
	if cfg.IdleClose <= 0 {
		cfg.IdleClose = DefaultIdleClose
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	return cfg
}

// Sender drains the queue to every peer with out_enabled.
type Sender struct {
	cfg  Config
	done chan struct{}

	wg sync.WaitGroup // every goroutine of the sender and of its peer feeds

	mu       sync.Mutex
	peers    map[int64]*peerFeed
	transit  map[int64]bool // peers whose feed is being stopped or restarted right now
	started  bool
	stopped  bool
	noHostOf map[int64]bool // peers logged as "out_enabled without out_host" (log once)

	stopOnce sync.Once
}

// NewSender checks the configuration and returns a sender that is not running yet.
func NewSender(cfg Config) (*Sender, error) {
	if cfg.Queue == nil {
		return nil, fmt.Errorf("feed: sender needs a Queue")
	}
	if cfg.Peers == nil {
		return nil, fmt.Errorf("feed: sender needs a PeerSource")
	}
	if cfg.Articles == nil {
		return nil, fmt.Errorf("feed: sender needs an ArticleSource")
	}
	if strings.TrimSpace(cfg.Hostname) == "" {
		// Without our identity the relayed Path would not carry this server, so a peer could
		// feed the article straight back: never start the feed like that.
		return nil, fmt.Errorf("feed: sender needs a Hostname (our Path identity)")
	}
	cfg = cfg.withDefaults()
	s := &Sender{
		cfg:      cfg,
		done:     make(chan struct{}),
		peers:    make(map[int64]*peerFeed),
		transit:  make(map[int64]bool),
		noHostOf: make(map[int64]bool),
	}
	if s.cfg.Dial == nil {
		opts := DialOptions{
			Identity:       cfg.Hostname,
			ConnectTimeout: cfg.ConnectTimeout,
			ReadTimeout:    cfg.ReadTimeout,
			WriteTimeout:   cfg.WriteTimeout,
			Batch:          cfg.Batch,
		}
		s.cfg.Dial = func(p *models.Peer) (Transport, error) { return DialPeer(p, opts) }
	}
	return s, nil
}

// Start reads the peer list and starts the feed of every peer with out_enabled. It returns an
// error when it is called twice or after Stop.
func (s *Sender) Start() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return fmt.Errorf("feed: sender already stopped")
	}
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("feed: sender already started")
	}
	s.started = true
	s.mu.Unlock()

	s.wg.Add(1)
	go s.supervise()
	return nil
}

// Stop stops every peer feed and waits for all goroutines. The counters of every peer are
// written to the queue one last time. It is safe to call it more than once and before Start.
//
// Every open connection is closed socket first (BackendConn.CloseNow), so a worker waiting for
// an answer returns at once whichever protocol it was in. The one wait that cannot be
// interrupted is a connect that is already running, so Stop takes at most ConnectTimeout
// (10 s by default) plus the queue calls of the last acknowledgement.
func (s *Sender) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
		close(s.done)
	})
	s.wg.Wait()
}

func (s *Sender) now() time.Time { return s.cfg.Clock.Now() }

// stopping reports whether Stop was called.
func (s *Sender) stopping() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// supervise re-reads the peer list every ReloadInterval until Stop.
func (s *Sender) supervise() {
	defer s.wg.Done()
	s.reload()
	s.prune()
	t := time.NewTicker(s.cfg.ReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.reload()
			s.prune()
		}
	}
}

// prune asks the queue to drop what it no longer needs, when it offers Prune at all (the
// queue implementation is written in a parallel slice: both shapes are accepted).
func (s *Sender) prune() {
	switch q := s.cfg.Queue.(type) {
	case queuePrunerCount:
		if n, err := q.Prune(s.now()); err != nil {
			log.Printf("[FEED] queue prune failed: %v", err)
		} else if n > 0 {
			log.Printf("[FEED] queue prune removed %d rows", n)
		}
	case queuePruner:
		if err := q.Prune(s.now()); err != nil {
			log.Printf("[FEED] queue prune failed: %v", err)
		}
	}
}

// reload decides, from the current peer rows, which feeds run:
//
//   - fed: the row is enabled, has out_enabled and an out_host;
//   - stopped but queued: the row still has out_enabled but the peer is disabled (or has no
//     out_host) - nothing is sent and the queue is kept, so enabling the peer again resumes it;
//   - dropped: the row is gone or out_enabled is cleared - the feed stops and Queue.DropPeer
//     deletes its queue and counters.
//
// A peer whose connection settings changed is stopped and started again with its counters. The
// stopping, dropping and restarting of one peer runs in its own goroutine, so a peer that is
// stuck in a connect does not hold up the reload of the others; the peer is marked in transit
// until that goroutine is done, so no second feed is started for it meanwhile.
func (s *Sender) reload() {
	peers, err := s.cfg.Peers.GetPeers()
	if err != nil {
		// Fail closed: keep the feeds that run, never drop a queue on a read error.
		log.Printf("[FEED] peer list: %v", err)
		return
	}
	var (
		want = make(map[int64]*models.Peer, len(peers)) // feed these
		keep = make(map[int64]bool, len(peers))         // row present with out_enabled: keep its queue
	)
	for _, p := range peers {
		if p == nil || !p.OutEnabled {
			continue
		}
		keep[p.ID] = true
		if p.Enabled {
			want[p.ID] = p
		}
	}

	type restart struct {
		old  *peerFeed
		next *models.Peer // nil: the feed is not started again
		drop bool         // call Queue.DropPeer after the feed stopped
	}
	var moves []restart
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	for id := range s.noHostOf {
		if _, ok := want[id]; !ok {
			delete(s.noHostOf, id) // the peer is gone or off: warn again if it comes back
		}
	}
	for id, pf := range s.peers {
		w, ok := want[id]
		if !ok {
			delete(s.peers, id)
			s.transit[id] = true
			moves = append(moves, restart{old: pf, drop: !keep[id]})
			continue
		}
		if peerFingerprint(w) != pf.fp {
			delete(s.peers, id)
			s.transit[id] = true
			moves = append(moves, restart{old: pf, next: w})
		}
	}
	for id, p := range want {
		if s.peers[id] != nil || s.transit[id] {
			continue
		}
		if p.OutHost == "" {
			if !s.noHostOf[id] {
				s.noHostOf[id] = true
				log.Printf("[FEED] peer %s: out_enabled without out_host: nothing is sent, the queue is kept", p.Name)
			}
			continue
		}
		delete(s.noHostOf, id) // it has a host again: warn once more if it loses it
		s.startPeerLocked(p, nil)
	}
	s.mu.Unlock()

	s.dropOrphans(keep)

	for _, m := range moves {
		mv := m
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.transition(mv.old, mv.next, mv.drop)
		}()
	}
}

// dropOrphans deletes the queue of every peer the queue file still holds counters for whose row
// is gone or has no out_enabled any more. keep names the peers whose queue stays. This also
// clears what an earlier run of the sender left behind (a peer switched off while it was not
// running); a peer being stopped right now is left to its own transition.
func (s *Sender) dropOrphans(keep map[int64]bool) {
	stats, err := s.cfg.Queue.ReadStats()
	if err != nil {
		log.Printf("[FEED] reading the stored counters failed: %v", err)
		return
	}
	for id := range stats {
		if keep[id] {
			continue
		}
		s.mu.Lock()
		busy := s.peers[id] != nil || s.transit[id]
		s.mu.Unlock()
		if busy {
			continue
		}
		s.dropQueue(id, "")
	}
}

// transition stops one feed, drops its queue when the peer lost its outgoing feed, and starts
// the replacement feed with the counters of the old one. It runs in its own goroutine.
func (s *Sender) transition(old *peerFeed, next *models.Peer, drop bool) {
	id := old.peer.ID
	old.stop()
	if drop {
		s.dropQueue(id, old.peer.Name)
	} else if next == nil {
		log.Printf("[FEED] peer %s: not enabled, feed stopped (its queue is kept)", old.peer.Name)
	}
	st := old.snapshot()

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.transit, id)
	if s.stopped || next == nil {
		return
	}
	if next.OutHost == "" || s.peers[id] != nil {
		return // picked up by the next reload
	}
	log.Printf("[FEED] peer %s: settings changed, feed restarted", next.Name)
	// A restarted feed keeps the counters of the stopped one: a settings change is not a
	// reason for the admin page to start at zero.
	s.startPeerLocked(next, &st)
}

// startPeerLocked starts the feed of p, optionally with the counters of the feed it replaces.
// The caller holds s.mu and has checked that no feed and no transition exists for p.
func (s *Sender) startPeerLocked(p *models.Peer, carry *Stats) {
	pf := newPeerFeed(s, p)
	if carry != nil {
		pf.stats, pf.logged = *carry, *carry // nothing else touches pf before start
		pf.stats.PeerID, pf.logged.PeerID = p.ID, p.ID
	}
	s.peers[p.ID] = pf
	pf.start()
	log.Printf("[FEED] peer %s: feeding %s:%d with up to %d connection(s)", p.Name, p.OutHost, pf.port(), pf.conns)
}

// dropQueue deletes the queue and the counters of a peer that has no outgoing feed any more.
func (s *Sender) dropQueue(id int64, name string) {
	if name == "" {
		name = fmt.Sprintf("#%d", id)
	}
	if err := s.cfg.Queue.DropPeer(id); err != nil {
		log.Printf("[FEED] peer %s: dropping the queue failed: %v", name, err)
		return
	}
	log.Printf("[FEED] peer %s: outgoing feed off, queue and counters dropped", name)
}

// peerFingerprint is the part of a peer the running feed depends on: a change restarts it.
func peerFingerprint(p *models.Peer) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%t\x00%s\x00%s\x00%d",
		p.Name, p.OutHost, p.OutPort, p.OutTLS, p.OutUsername, p.OutPassword, p.MaxConns)
}

// peerFeed is the feed of one peer: max_conns connection workers and the counter writer.
type peerFeed struct {
	s     *Sender
	peer  *models.Peer // snapshot taken at the reload that started this feed; never mutated
	fp    string
	conns int
	done  chan struct{}
	wg    sync.WaitGroup

	mu        sync.Mutex
	stats     Stats
	logged    Stats // the counters of the last log line, to stay quiet while nothing happens
	live      map[int]Transport
	badGroups map[string]bool // newsgroups whose read failure was logged already
	closed    bool            // no new connection is opened
	stopOnce  sync.Once
}

// maxPeerConns bounds the connections of one peer whatever its row says (ValidatePeer keeps
// max_conns in 1..64; a hand-edited row must not start a thousand goroutines).
const maxPeerConns = 64

func newPeerFeed(s *Sender, p *models.Peer) *peerFeed {
	conns := p.MaxConns
	if conns <= 0 {
		conns = 1
	}
	if conns > maxPeerConns {
		log.Printf("[FEED] peer %s: max_conns %d is above %d: using %d", p.Name, conns, maxPeerConns, maxPeerConns)
		conns = maxPeerConns
	}
	snap := *p
	return &peerFeed{
		s:         s,
		peer:      &snap,
		fp:        peerFingerprint(p),
		conns:     conns,
		done:      make(chan struct{}),
		stats:     Stats{PeerID: p.ID},
		live:      make(map[int]Transport),
		badGroups: make(map[string]bool),
	}
}

// ref names this peer in the peering statistics (plan peering-stats).
func (pf *peerFeed) ref() peerstats.PeerRef {
	return peerstats.PeerRef{ID: pf.peer.ID, Name: pf.peer.Name}
}

// connStats accumulates the statistics counters of one sender connection, so Closed can
// report the totals of that connection. Only the worker goroutine that owns the connection
// touches it, so it needs no lock.
type connStats struct {
	start time.Time
	total peerstats.Counters
}

// addStats counts delta for this peer in the peering statistics, and in the totals of the
// open connection when cs is not nil (a counter that belongs to no connection, a missing
// article or a failed connect, passes nil). It is never called while pf.mu is held.
func (pf *peerFeed) addStats(cs *connStats, delta peerstats.Counters) {
	if delta.IsZero() {
		return
	}
	if cs != nil {
		cs.total.Add(delta)
	}
	pf.s.cfg.Stats.Add(pf.ref(), peerstats.Out, delta)
}

// statsConnected opens the statistics of one sender connection.
func (pf *peerFeed) statsConnected() *connStats {
	pf.s.cfg.Stats.Connected(pf.ref(), peerstats.Out, fmt.Sprintf("%s:%d", pf.peer.OutHost, pf.port()))
	return &connStats{start: pf.s.now()}
}

// statsClosed closes the statistics of one sender connection (its totals and how long it
// was open).
func (pf *peerFeed) statsClosed(cs *connStats) {
	if cs == nil {
		return
	}
	dur := pf.s.now().Sub(cs.start)
	if dur < 0 {
		dur = 0
	}
	pf.s.cfg.Stats.Closed(pf.ref(), peerstats.Out, cs.total, dur)
}

func (pf *peerFeed) port() int {
	if pf.peer.OutPort > 0 {
		return pf.peer.OutPort
	}
	return 119
}

// start launches the connection workers, the counter writer and the watcher that force-closes
// the open connections when the feed or the whole sender stops.
func (pf *peerFeed) start() {
	pf.add(func() { pf.watch() })
	pf.add(func() { pf.statsLoop() })
	for i := 0; i < pf.conns; i++ {
		idx := i
		pf.add(func() { pf.connWorker(idx) })
	}
}

// add runs fn in a goroutine counted by both the feed and the sender, so Sender.Stop waits
// for it.
func (pf *peerFeed) add(fn func()) {
	pf.wg.Add(1)
	pf.s.wg.Add(1)
	go func() {
		defer pf.s.wg.Done()
		defer pf.wg.Done()
		fn()
	}()
}

// stop ends this feed and waits for its goroutines (the counter writer stores the counters one
// last time on its way out).
func (pf *peerFeed) stop() {
	pf.stopOnce.Do(func() { close(pf.done) })
	pf.wg.Wait()
}

// watch force-closes every open connection once the feed or the sender stops, so a worker
// waiting for a peer's answer unwinds instead of waiting for its idle timeout.
func (pf *peerFeed) watch() {
	select {
	case <-pf.done:
	case <-pf.s.done:
	}
	pf.mu.Lock()
	pf.closed = true
	live := make([]Transport, 0, len(pf.live))
	for i, t := range pf.live {
		live = append(live, t)
		delete(pf.live, i)
	}
	pf.mu.Unlock()
	for _, t := range live {
		t.Close()
	}
}

// stopping reports whether this feed or the whole sender is stopping.
func (pf *peerFeed) stopping() bool {
	select {
	case <-pf.done:
		return true
	default:
	}
	return pf.s.stopping()
}

// sleep waits for d; it returns false when the feed stopped meanwhile.
func (pf *peerFeed) sleep(d time.Duration) bool {
	if d <= 0 {
		return !pf.stopping()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-pf.done:
		return false
	case <-pf.s.done:
		return false
	case <-t.C:
		return true
	}
}

// addConn remembers an open connection so watch can close it. It returns false when the feed is
// already stopping: the caller must close the connection and return. The stop channels are
// checked as well as the flag the watcher sets, because a connect that was running while the
// stop began finishes before the watcher gets to run.
func (pf *peerFeed) addConn(idx int, t Transport) bool {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.closed || pf.stopping() {
		return false
	}
	pf.live[idx] = t
	return true
}

// dropConn closes one connection and forgets it.
func (pf *peerFeed) dropConn(idx int, t Transport) {
	pf.mu.Lock()
	if pf.live[idx] == t {
		delete(pf.live, idx)
	}
	pf.mu.Unlock()
	t.Close()
}

// connWorker is one of the peer's connections: claim, send, acknowledge, repeat.
func (pf *peerFeed) connWorker(idx int) {
	var (
		t         Transport
		connFails int
		idleSince time.Time
		cs        *connStats // the statistics of the open connection (plan peering-stats)
	)
	defer func() {
		if t != nil {
			pf.dropConn(idx, t)
			pf.statsClosed(cs)
		}
	}()
	for {
		if pf.stopping() {
			return
		}
		items, err := pf.s.cfg.Queue.Claim(pf.peer.ID, pf.s.cfg.Batch, pf.s.now(), pf.s.cfg.Lease)
		if err != nil {
			pf.setLastError("claim: " + err.Error())
			log.Printf("[FEED] peer %s: claim failed: %v", pf.peer.Name, err)
			if !pf.sleep(pf.s.cfg.PollInterval) {
				return
			}
			continue
		}
		if len(items) == 0 {
			now := pf.s.now()
			if idleSince.IsZero() {
				idleSince = now
			}
			if t != nil && now.Sub(idleSince) >= pf.s.cfg.IdleClose {
				pf.dropConn(idx, t)
				pf.statsClosed(cs)
				t, cs = nil, nil
			}
			if !pf.sleep(pf.s.cfg.PollInterval) {
				return
			}
			continue
		}
		idleSince = time.Time{}
		// A stop between the claim and the send leaves the batch claimed: its lease runs out
		// and the items come back by themselves, without an attempt and without a last error.
		if pf.stopping() {
			return
		}

		// The articles are read before a connection is opened: a batch whose articles are all
		// gone is finished without dialing the peer.
		attempts := make(map[string]int, len(items))
		for _, it := range items {
			attempts[it.MessageID] = it.Attempts
		}
		out, done, deferred := pf.load(items, cs)
		pf.finish(done, deferred, attempts)
		if pf.stopping() {
			return // the rest of the batch stays claimed
		}
		if len(out) == 0 {
			continue
		}

		if t == nil {
			t, err = pf.dial(idx)
			if errors.Is(err, errStopping) {
				return // the batch stays claimed, see above
			}
			if err != nil {
				if isPeerDenied(err) {
					// The peer refuses our feed (502/480): visible as Denied, so the
					// backlog below has a reason (plan peering-stats).
					pf.addStats(nil, peerstats.Counters{Denied: 1})
				}
				pf.deferAll(out, attempts, err.Error())
				pf.setLastError(err.Error())
				connFails++
				log.Printf("[FEED] peer %s: connect to %s:%d failed: %v (retry in %v)",
					pf.peer.Name, pf.peer.OutHost, pf.port(), err, pf.backoff(connFails-1))
				if !pf.sleep(pf.backoff(connFails - 1)) {
					return
				}
				continue
			}
			connFails = 0
			cs = pf.statsConnected()
		}

		if pf.sendBatch(t, out, attempts, cs) {
			continue
		}
		// The connection is unusable: close it and wait before the next connect.
		pf.dropConn(idx, t)
		pf.statsClosed(cs)
		t, cs = nil, nil
		connFails++
		if !pf.sleep(pf.backoff(connFails - 1)) {
			return
		}
	}
}

// errStopping is dial's answer when the feed is on its way out: the caller returns without
// touching the queue, so a claimed batch stays claimed until its lease runs out.
var errStopping = errors.New("feed: stopping")

// dial opens one connection and registers it for the close on stop.
func (pf *peerFeed) dial(idx int) (Transport, error) {
	if pf.stopping() {
		return nil, errStopping
	}
	t, err := pf.s.cfg.Dial(pf.peer)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("feed: dialer returned no connection")
	}
	if !pf.addConn(idx, t) {
		t.Close()
		return nil, errStopping
	}
	return t, nil
}

// backoff is the delay after n failures (30 s doubling to 30 min by default).
func (pf *peerFeed) backoff(n int) time.Duration {
	return backoffDelay(n, pf.s.cfg.BackoffBase, pf.s.cfg.BackoffMax)
}

// backoffDelay returns the delay before the next try after n failed ones: base doubled per
// failure, capped at max.
func backoffDelay(n int, base, max time.Duration) time.Duration {
	if base <= 0 {
		base = DefaultBackoffBase
	}
	if max <= 0 {
		max = DefaultBackoffMax
	}
	if max < base {
		max = base
	}
	d := base
	for i := 0; i < n && d < max; i++ {
		d *= 2
	}
	if d > max || d <= 0 {
		d = max
	}
	return d
}

// sendBatch hands one batch to the peer and acknowledges every article. The batch is split into
// runs of at most BatchBytes octets (one article always goes alone when it is larger), so a
// batch of large articles does not sit in one pipeline. It returns false when the connection
// must be closed; the articles of the runs that were not sent are deferred with that reason.
func (pf *peerFeed) sendBatch(t Transport, out []Outgoing, attempts map[string]int, cs *connStats) bool {
	for len(out) > 0 {
		run := pf.byteRun(out)
		ok, err := pf.sendRun(t, out[:run], attempts, cs)
		if ok {
			out = out[run:]
			continue
		}
		if isPeerDenied(err) {
			pf.addStats(cs, peerstats.Counters{Denied: 1}) // the peer refuses our feed
		}
		pf.setLastError(err.Error())
		log.Printf("[FEED] peer %s: %s connection lost: %v", pf.peer.Name, t.Mode(), err)
		if rest := out[run:]; len(rest) > 0 {
			pf.deferAll(rest, attempts, err.Error())
		}
		return false
	}
	return true
}

// byteRun returns how many of the articles at the front of out fit into one send (at least one).
func (pf *peerFeed) byteRun(out []Outgoing) int {
	budget := pf.s.cfg.BatchBytes
	n, size := 0, 0
	for _, o := range out {
		s := o.wireSize()
		if n > 0 && size+s > budget {
			break
		}
		size += s
		n++
	}
	if n == 0 {
		n = 1
	}
	return n
}

// sendRun offers one run of articles and acknowledges every one of them.
func (pf *peerFeed) sendRun(t Transport, out []Outgoing, attempts map[string]int, cs *connStats) (bool, error) {
	res, offered, err := t.Send(out)
	pf.record(out, res, offered, err == nil, cs)
	var (
		done     []string
		deferred []deferItem
	)
	for i, r := range res {
		id := r.MessageID
		if id == "" && i < len(out) {
			id = out[i].Article.MessageID
		}
		switch r.Outcome {
		case OutcomeDefer:
			deferred = append(deferred, deferItem{id: id, err: r.Err})
		case OutcomeDrop:
			log.Printf("[FEED] peer %s: cannot send %s: %s", pf.peer.Name, id, r.Err)
			done = append(done, id)
		default:
			done = append(done, id)
		}
	}
	pf.finish(done, deferred, attempts)
	return err == nil, err
}

// finish acknowledges the items of one batch: the finished ones are deleted, the deferred ones
// get one more attempt and a next_attempt from their own attempt count.
func (pf *peerFeed) finish(done []string, deferred []deferItem, attempts map[string]int) {
	if len(done) > 0 {
		if err := pf.s.cfg.Queue.Done(pf.peer.ID, done); err != nil {
			log.Printf("[FEED] peer %s: marking %d item(s) done failed: %v", pf.peer.Name, len(done), err)
		}
	}
	for _, d := range deferred {
		next := pf.s.now().Add(backoffDelay(attempts[d.id], pf.s.cfg.BackoffBase, pf.s.cfg.BackoffMax))
		if err := pf.s.cfg.Queue.Defer(pf.peer.ID, d.id, d.err, next); err != nil {
			log.Printf("[FEED] peer %s: deferring %s failed: %v", pf.peer.Name, d.id, err)
		}
	}
}

// deferItem is one item to put back with its reason.
type deferItem struct {
	id  string
	err string
}

// deferAll puts every article of a batch back with the same reason (the connect failed, so
// nothing was offered).
func (pf *peerFeed) deferAll(out []Outgoing, attempts map[string]int, reason string) {
	deferred := make([]deferItem, 0, len(out))
	for _, o := range out {
		deferred = append(deferred, deferItem{id: o.Article.MessageID, err: reason})
	}
	pf.finish(nil, deferred, attempts)
	pf.mu.Lock()
	pf.stats.Deferred += int64(len(deferred))
	pf.mu.Unlock()
	pf.addStats(nil, peerstats.Counters{Deferred: int64(len(deferred))})
}

// load reads the articles of a batch from their newsgroups (one query per newsgroup). An
// article that is not stored any more (expired or deleted) is finished with a log line; a
// newsgroup that cannot be read is temporary, so its items are deferred.
func (pf *peerFeed) load(items []Item, cs *connStats) (out []Outgoing, done []string, deferred []deferItem) {
	var missing int64
	byGroup := make(map[string][]int, 4)
	for i, it := range items {
		byGroup[it.Newsgroup] = append(byGroup[it.Newsgroup], i)
	}
	found := make(map[string]*models.Article, len(items))
	for group, idx := range byGroup {
		if group == "" {
			for _, i := range idx {
				log.Printf("[FEED] peer %s: queue item %s has no newsgroup: dropped",
					pf.peer.Name, items[i].MessageID)
				done = append(done, items[i].MessageID)
			}
			continue
		}
		ids := make([]*string, 0, len(idx))
		for _, i := range idx {
			id := items[i].MessageID
			ids = append(ids, &id)
		}
		g := group
		arts, err := pf.s.cfg.Articles.GetArticlesByIDs(&g, ids)
		if err != nil {
			// A newsgroup that cannot be read (its database is busy, or gone with the group)
			// is temporary for the queue: the items are deferred and the queue's own give-up
			// age drops them in the end. Logged once per peer and newsgroup, so a group that
			// does not exist any more does not fill the log at every poll.
			if pf.markBadGroup(group) {
				log.Printf("[FEED] peer %s: reading %d article(s) from %s failed: %v (logged once; the queue gives up on them in the end)",
					pf.peer.Name, len(ids), group, err)
			}
			pf.setLastError("read " + group + ": " + err.Error())
			for _, i := range idx {
				deferred = append(deferred, deferItem{id: items[i].MessageID, err: "read " + group + ": " + err.Error()})
			}
			continue
		}
		pf.clearBadGroup(group)
		for _, a := range arts {
			if a != nil {
				found[group+"\x00"+a.MessageID] = a
			}
		}
	}
	for _, it := range items {
		if it.Newsgroup == "" {
			continue
		}
		a := found[it.Newsgroup+"\x00"+it.MessageID]
		if a == nil {
			if hasDefer(deferred, it.MessageID) {
				continue // the whole newsgroup was deferred
			}
			log.Printf("[FEED] peer %s: %s is no longer stored in %s: dropped from the queue",
				pf.peer.Name, it.MessageID, it.Newsgroup)
			done = append(done, it.MessageID)
			missing++
			continue
		}
		out = append(out, Outgoing{Article: a, Newsgroup: it.Newsgroup})
	}
	// Articles that are gone from the database are the innfeed "missing" counter.
	pf.addStats(cs, peerstats.Counters{Missing: missing})
	return out, done, deferred
}

func hasDefer(deferred []deferItem, id string) bool {
	for _, d := range deferred {
		if d.id == id {
			return true
		}
	}
	return false
}

// record adds the verdicts of one run to the peer's counters (and to the peering statistics,
// where the sizes of out carry the accepted and rejected octets). A run that the peer answered
// for every article, with nothing deferred or dropped, clears the last error: the feed works
// again. out is index-aligned with res.
func (pf *peerFeed) record(out []Outgoing, res []Result, offered int, connOK bool, cs *connStats) {
	delta := peerstats.Counters{Offered: int64(offered)}
	pf.mu.Lock()
	pf.stats.Offered += int64(offered)
	clean := connOK && len(res) > 0
	for i, r := range res {
		var size int64
		if i < len(out) {
			size = statsArticleBytes(out[i])
		}
		switch r.Code {
		case 235, 239:
			pf.stats.Accepted++
			delta.Accepted++
			delta.AccBytes += size
		case 435, 438:
			pf.stats.Refused++
			delta.Refused++
		case 437, 439:
			pf.stats.Rejected++
			delta.Rejected++
			delta.RejBytes += size
		default:
			if r.Outcome == OutcomeDefer {
				pf.stats.Deferred++
				delta.Deferred++
			}
		}
		if r.Outcome != OutcomeDone || r.Err != "" {
			clean = false
		}
		if r.Err != "" {
			pf.stats.LastError = r.Err
		}
	}
	if clean {
		pf.stats.LastError = ""
	}
	pf.mu.Unlock()
	pf.addStats(cs, delta) // never under pf.mu
}

// statsArticleBytes is the size of one outgoing article for the statistics: the wire size
// the intake counted, or the stored header block and body when the row has none.
func statsArticleBytes(o Outgoing) int64 {
	if o.Article == nil {
		return 0
	}
	if o.Article.Bytes > 0 {
		return int64(o.Article.Bytes)
	}
	return int64(len(o.Article.HeadersJSON) + len(o.Article.BodyText))
}

// markBadGroup records that this newsgroup could not be read and reports whether this is the
// first time (so the caller logs it once); clearBadGroup forgets it after a read that worked.
func (pf *peerFeed) markBadGroup(group string) bool {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.badGroups[group] {
		return false
	}
	if pf.badGroups == nil {
		pf.badGroups = make(map[string]bool, 1)
	}
	pf.badGroups[group] = true
	return true
}

func (pf *peerFeed) clearBadGroup(group string) {
	pf.mu.Lock()
	if pf.badGroups[group] {
		delete(pf.badGroups, group)
	}
	pf.mu.Unlock()
}

func (pf *peerFeed) setLastError(msg string) {
	pf.mu.Lock()
	pf.stats.LastError = msg
	pf.mu.Unlock()
}

// snapshot returns a copy of the peer's counters.
func (pf *peerFeed) snapshot() Stats {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return pf.stats
}

// statsLoop writes and logs the counters every StatsInterval and once more on the way out.
func (pf *peerFeed) statsLoop() {
	defer pf.flush(true)
	t := time.NewTicker(pf.s.cfg.StatsInterval)
	defer t.Stop()
	for {
		select {
		case <-pf.done:
			return
		case <-pf.s.done:
			return
		case <-t.C:
			pf.flush(false)
		}
	}
}

// flush stores the counters in the queue file (so the web process can read them) and logs one
// line. Without final it does nothing while no counter moved and the queue is empty. A queue
// depth that cannot be read is reported as the peer's last error and never stops the write.
func (pf *peerFeed) flush(final bool) {
	depth, derr := pf.s.cfg.Queue.Depth(pf.peer.ID)
	if derr != nil {
		log.Printf("[FEED] peer %s: queue depth failed: %v", pf.peer.Name, derr)
		pf.setLastError("queue depth: " + derr.Error())
	} else {
		pf.s.cfg.Stats.SetQueue(pf.ref(), depth) // plan peering-stats
	}
	pf.mu.Lock()
	st := pf.stats
	quiet := st.Offered == pf.logged.Offered && st.Accepted == pf.logged.Accepted &&
		st.Refused == pf.logged.Refused && st.Rejected == pf.logged.Rejected &&
		st.Deferred == pf.logged.Deferred && st.LastError == pf.logged.LastError
	pf.logged = st
	pf.mu.Unlock()
	if quiet && depth == 0 && derr == nil && !final {
		return
	}
	st.PeerID = pf.peer.ID
	st.QueueDepth = depth
	st.UpdatedAt = pf.s.now()
	if err := pf.s.cfg.Queue.WriteStats(st); err != nil {
		log.Printf("[FEED] peer %s: writing the counters failed: %v", pf.peer.Name, err)
	}
	line := fmt.Sprintf("[FEED] peer %s: offered %d accepted %d refused %d rejected %d deferred %d queue %d",
		pf.peer.Name, st.Offered, st.Accepted, st.Refused, st.Rejected, st.Deferred, st.QueueDepth)
	if st.LastError != "" {
		line += " last error: " + st.LastError
	}
	log.Print(line)
}
