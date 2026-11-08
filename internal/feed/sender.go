package feed

// The outgoing feed sender (plan nntp-peering wave 3, slice feed-sender): one goroutine set per
// peer with out_enabled, each with up to max_conns persistent connections that drain the queue
// of that peer to it. The protocol side is sender_conn.go (the internal/nntp client); this file
// is the peer bookkeeping: which peers are fed, claiming and acknowledging queue items, the
// retry backoff and the per-peer counters.

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
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
	// DefaultBackoffBase is the delay before the first retry of a deferred item and after the
	// first failed connect.
	DefaultBackoffBase = 30 * time.Second
	// DefaultBackoffMax caps the doubling of DefaultBackoffBase.
	DefaultBackoffMax = 30 * time.Minute
	// DefaultIdleClose is how long a connection is kept open with nothing to send. It stays
	// below the client's idle read timeout, so an idle connection is closed here instead of
	// being torn down by a timeout.
	DefaultIdleClose = time.Minute
	// DefaultConnectTimeout bounds dial, TLS handshake, greeting and AUTHINFO of one connect.
	// Stop waits for a connect that is already running, so this is also the longest a Stop can
	// take beyond the answers in flight.
	DefaultConnectTimeout = 10 * time.Second
)

// PeerSource lists the peers the sender may feed. *database.Database implements it with
// GetEnabledPeers (every enabled peer; the sender picks the ones with out_enabled).
type PeerSource interface {
	GetEnabledPeers() ([]*models.Peer, error)
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

	PollInterval   time.Duration
	ReloadInterval time.Duration
	StatsInterval  time.Duration
	Lease          time.Duration
	Batch          int
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	IdleClose      time.Duration
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration // 0: the client default, <0: no idle read timeout
	WriteTimeout   time.Duration // 0: the client default, <0: no idle write timeout
}

// withDefaults returns cfg with every zero field filled.
func (cfg Config) withDefaults() Config {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
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
	cfg = cfg.withDefaults()
	s := &Sender{
		cfg:      cfg,
		done:     make(chan struct{}),
		peers:    make(map[int64]*peerFeed),
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
// Every open connection is force-closed first, so a worker waiting for a streaming answer
// returns at once. Two waits are not interruptible and bound how long Stop takes: a connect
// that is already running (ConnectTimeout) and, on the IHAVE fallback, the answer to one
// article (the client's idle read timeout, Config.ReadTimeout) - the client holds the
// connection's lock for that exchange, so closing the socket has to wait for it.
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

// reload starts the feed of every peer that may be fed, stops the feed of a peer that may not
// any more (deleted, disabled or out_enabled cleared: its queue and counters are dropped) and
// restarts the feed of a peer whose connection settings changed.
func (s *Sender) reload() {
	peers, err := s.cfg.Peers.GetEnabledPeers()
	if err != nil {
		// Fail closed: keep the feeds that run, never drop a queue on a read error.
		log.Printf("[FEED] peer list: %v", err)
		return
	}
	want := make(map[int64]*models.Peer, len(peers))
	for _, p := range peers {
		if p == nil || !p.Enabled || !p.OutEnabled {
			continue
		}
		want[p.ID] = p
	}

	var (
		gone    []*peerFeed // feed stopped and its queue dropped
		changed []*peerFeed // feed stopped, started again below
	)
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	for id := range s.noHostOf {
		if _, ok := want[id]; !ok {
			delete(s.noHostOf, id) // the peer is gone: warn again if it comes back
		}
	}
	for id, pf := range s.peers {
		w, ok := want[id]
		if !ok {
			delete(s.peers, id)
			gone = append(gone, pf)
			continue
		}
		if peerFingerprint(w) != pf.fp {
			delete(s.peers, id)
			changed = append(changed, pf)
		}
	}
	s.mu.Unlock()

	// Stop the outdated feeds before a replacement is started, so one peer is never fed by
	// two feeds at once.
	for _, pf := range gone {
		pf.stop()
	}
	// A restarted feed keeps the counters of the stopped one: a settings change is not a
	// reason for the admin page to start at zero.
	carry := make(map[int64]Stats, len(changed))
	for _, pf := range changed {
		pf.stop()
		carry[pf.peer.ID] = pf.snapshot()
		log.Printf("[FEED] peer %s: settings changed, feed restarted", pf.peer.Name)
	}
	for _, pf := range gone {
		if err := s.cfg.Queue.DropPeer(pf.peer.ID); err != nil {
			log.Printf("[FEED] peer %s: dropping the queue failed: %v", pf.peer.Name, err)
			continue
		}
		log.Printf("[FEED] peer %s: outgoing feed off, queue and counters dropped", pf.peer.Name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	for id, p := range want {
		if _, ok := s.peers[id]; ok {
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
		pf := newPeerFeed(s, p)
		if st, ok := carry[id]; ok {
			pf.stats, pf.logged = st, st // nothing else touches pf before start
		}
		s.peers[id] = pf
		pf.start()
		log.Printf("[FEED] peer %s: feeding %s:%d with up to %d connection(s)", p.Name, p.OutHost, pf.port(), pf.conns)
	}
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

	mu       sync.Mutex
	stats    Stats
	logged   Stats // the counters of the last log line, to stay quiet while nothing happens
	live     map[int]Transport
	closed   bool // no new connection is opened
	stopOnce sync.Once
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
		s:     s,
		peer:  &snap,
		fp:    peerFingerprint(p),
		conns: conns,
		done:  make(chan struct{}),
		stats: Stats{PeerID: p.ID},
		live:  make(map[int]Transport),
	}
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

// addConn remembers an open connection so watch can force-close it. It returns false when the
// feed is already stopping: the caller must close the connection and return.
func (pf *peerFeed) addConn(idx int, t Transport) bool {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.closed {
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
	)
	defer func() {
		if t != nil {
			pf.dropConn(idx, t)
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
				t = nil
			}
			if !pf.sleep(pf.s.cfg.PollInterval) {
				return
			}
			continue
		}
		idleSince = time.Time{}

		// The articles are read before a connection is opened: a batch whose articles are all
		// gone is finished without dialing the peer.
		attempts := make(map[string]int, len(items))
		for _, it := range items {
			attempts[it.MessageID] = it.Attempts
		}
		out, done, deferred := pf.load(items)
		pf.finish(done, deferred, attempts)
		if len(out) == 0 {
			continue
		}

		if t == nil {
			t, err = pf.dial(idx)
			if err != nil {
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
		}

		if pf.sendBatch(t, out, attempts) {
			continue
		}
		// The connection is unusable: close it and wait before the next connect.
		pf.dropConn(idx, t)
		t = nil
		connFails++
		if !pf.sleep(pf.backoff(connFails - 1)) {
			return
		}
	}
}

// dial opens one connection and registers it for the force-close on stop.
func (pf *peerFeed) dial(idx int) (Transport, error) {
	if pf.stopping() {
		return nil, fmt.Errorf("feed: stopping")
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
		return nil, fmt.Errorf("feed: stopping")
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

// sendBatch hands one batch to the peer and acknowledges every article. It returns false when
// the connection must be closed.
func (pf *peerFeed) sendBatch(t Transport, out []Outgoing, attempts map[string]int) bool {
	res, offered, err := t.Send(out)
	pf.record(res, offered)
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
	if err != nil {
		pf.setLastError(err.Error())
		log.Printf("[FEED] peer %s: %s connection lost: %v", pf.peer.Name, t.Mode(), err)
		return false
	}
	return true
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
}

// load reads the articles of a batch from their newsgroups (one query per newsgroup). An
// article that is not stored any more (expired or deleted) is finished with a log line; a
// newsgroup that cannot be read is temporary, so its items are deferred.
func (pf *peerFeed) load(items []Item) (out []Outgoing, done []string, deferred []deferItem) {
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
			log.Printf("[FEED] peer %s: reading %d article(s) from %s failed: %v",
				pf.peer.Name, len(ids), group, err)
			pf.setLastError("read " + group + ": " + err.Error())
			for _, i := range idx {
				deferred = append(deferred, deferItem{id: items[i].MessageID, err: "read " + group + ": " + err.Error()})
			}
			continue
		}
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
			continue
		}
		out = append(out, Outgoing{Article: a, Newsgroup: it.Newsgroup})
	}
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

// record adds the verdicts of one batch to the peer's counters.
func (pf *peerFeed) record(res []Result, offered int) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.stats.Offered += int64(offered)
	for _, r := range res {
		switch r.Code {
		case 235, 239:
			pf.stats.Accepted++
		case 435, 438:
			pf.stats.Refused++
		case 437, 439:
			pf.stats.Rejected++
		default:
			if r.Outcome == OutcomeDefer {
				pf.stats.Deferred++
			}
		}
		if r.Err != "" {
			pf.stats.LastError = r.Err
		}
	}
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
// line. Without final it does nothing while no counter moved and the queue is empty.
func (pf *peerFeed) flush(final bool) {
	depth, err := pf.s.cfg.Queue.Depth(pf.peer.ID)
	if err != nil {
		log.Printf("[FEED] peer %s: queue depth failed: %v", pf.peer.Name, err)
	}
	pf.mu.Lock()
	st := pf.stats
	quiet := st.Offered == pf.logged.Offered && st.Accepted == pf.logged.Accepted &&
		st.Refused == pf.logged.Refused && st.Rejected == pf.logged.Rejected &&
		st.Deferred == pf.logged.Deferred && st.LastError == pf.logged.LastError
	pf.logged = st
	pf.mu.Unlock()
	if quiet && depth == 0 && !final {
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
