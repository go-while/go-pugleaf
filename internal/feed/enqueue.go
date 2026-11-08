package feed

// The enqueue side of the outgoing transit feed (plan nntp-peering, slice feed-queue).
//
// internal/database calls the commit-point hook once per committed non-bulk article
// (database.SetFeedHook, db_batch.go). The hook runs in the batch writer's goroutine, so it
// only hands the snapshot to a bounded channel and returns; one goroutine of its own then
// decides which peers want the article and writes the rows in batches. A full channel drops
// the snapshot and counts it: a dropped article is not fed, which is what INN calls a
// missed article, and there is no catch-up scan in this plan (decision of waves 2+3).

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// Defaults of an Enqueuer.
const (
	// DefaultQueueSize is the capacity of the snapshot channel: how many committed
	// articles may wait for the enqueuer while the queue file is busy.
	DefaultQueueSize = 8192
	// DefaultBatchMax is how many queue rows are written in one transaction.
	DefaultBatchMax = 512
	// DefaultFlushInterval is how long a partial batch waits for more rows.
	DefaultFlushInterval = 250 * time.Millisecond
	// peerListTTL is how long the out-feed peer list is reused before the peers table is
	// read again, so an admin edit applies without a restart (the same window as
	// internal/nntp's PeerRegistry).
	peerListTTL = 30 * time.Second
	// peerListErrorTTL is the shorter window after a failed read of the peers table.
	peerListErrorTTL = 5 * time.Second
	// moderationTTL is how long the moderation status of a newsgroup is reused (only read
	// at all when some peer's out_flags carry Nm or Nu).
	moderationTTL = 5 * time.Minute
	// moderationCacheMax bounds the moderation cache; past it the cache is dropped and
	// filled again, so it cannot grow with the number of groups seen.
	moderationCacheMax = 8192
	// feedLogEvery rate-limits the "queue full" and "cannot queue" log lines.
	feedLogEvery = time.Minute
)

// Snapshot is one committed article as the enqueue side sees it: the fields the peer
// selection needs, with no pointer back into the models.Article it came from.
type Snapshot struct {
	MessageID string
	// Path is the stored Path header with this server's identity already prepended.
	Path string
	// Newsgroups are the groups of the article's Newsgroups header.
	Newsgroups []string
	// Newsgroup is one group the article is stored in: where the sender reads it from.
	Newsgroup string
	// Bytes is the wire size, Lines the body line count.
	Bytes int
	Lines int
	// Moderated is the moderation status of the newsgroups, filled by the Enqueuer only
	// when some peer's out_flags need it (Nm/Nu). A group that is not in the map counts
	// as unmoderated.
	Moderated map[string]bool
}

// SnapshotFromDB converts the batch writer's snapshot into the enqueue side's.
func SnapshotFromDB(a database.FeedArticle) Snapshot {
	return Snapshot{
		MessageID:  a.MessageID,
		Path:       a.Path,
		Newsgroups: a.Newsgroups,
		Newsgroup:  a.Newsgroup,
		Bytes:      a.Bytes,
		Lines:      a.Lines,
	}
}

// Store is the part of *database.Database the Enqueuer reads: the peers it feeds and the
// moderation status of a newsgroup.
type Store interface {
	// GetEnabledPeers returns the enabled peers (table peers).
	GetEnabledPeers() ([]*models.Peer, error)
	// GetIntakeGroup returns the main-DB row of a newsgroup; its Status is 'm' for a
	// moderated group.
	GetIntakeGroup(name string) (*database.IntakeGroup, error)
}

// EnqueuerConfig are the tunables of an Enqueuer; a zero field takes its default.
type EnqueuerConfig struct {
	QueueSize     int
	BatchMax      int
	FlushInterval time.Duration
}

// outPeer is one peer of the out feed with its selection fields parsed once per peer-list
// reload, so the hot path does no parsing.
type outPeer struct {
	id       int64
	name     string
	token    string // path_token: a Path that names it means the peer has the article
	patterns string // out_patterns, as stored (INN newsfeeds(5) wildmat list)
	flags    Flags
}

// moderationEntry is one cached newsgroup status.
type moderationEntry struct {
	moderated bool
	at        time.Time
}

// Enqueuer turns committed articles into queue rows. It is safe for concurrent use; Submit
// never blocks.
type Enqueuer struct {
	q     Queue
	store Store

	in       chan Snapshot
	batchMax int
	flush    time.Duration
	wg       sync.WaitGroup

	// submission state: the read lock is held for the (non-blocking) channel send, so
	// Stop can close the channel without racing a send.
	subMu   sync.RWMutex
	started bool
	stopped bool

	dropped  atomic.Int64
	enqueued atomic.Int64

	dropLog  rateLog // "queue full"
	writeLog rateLog // "cannot queue"

	peerMu       sync.Mutex
	peers        []*outPeer
	peersAt      time.Time
	peersFailed  bool
	peersLoading bool
	peerErrAt    time.Time
	peerNames    string // the loaded out-feed peers, to log only real changes
	peerSkipped  string // the peers skipped as unusable, likewise

	modMu sync.Mutex
	mod   map[string]moderationEntry

	// now is time.Now in production; the tests replace it before Start.
	now func() time.Time
}

// NewEnqueuer returns a stopped Enqueuer over q and store. Start begins its goroutine.
func NewEnqueuer(q Queue, store Store, cfg EnqueuerConfig) *Enqueuer {
	size := cfg.QueueSize
	if size <= 0 {
		size = DefaultQueueSize
	}
	batch := cfg.BatchMax
	if batch <= 0 {
		batch = DefaultBatchMax
	}
	flush := cfg.FlushInterval
	if flush <= 0 {
		flush = DefaultFlushInterval
	}
	e := &Enqueuer{
		q:        q,
		store:    store,
		in:       make(chan Snapshot, size),
		batchMax: batch,
		flush:    flush,
		mod:      make(map[string]moderationEntry),
		now:      time.Now,
	}
	e.dropLog.every = feedLogEvery
	e.writeLog.every = feedLogEvery
	return e
}

// Start begins the enqueue goroutine. Calling it twice, or after Stop, does nothing.
func (e *Enqueuer) Start() {
	if e == nil {
		return
	}
	e.subMu.Lock()
	defer e.subMu.Unlock()
	if e.started || e.stopped {
		return
	}
	e.started = true
	e.wg.Add(1)
	go e.run()
}

// Stop closes the channel, lets the goroutine drain what is in it and write the last batch,
// and returns when it has. Submit is a no-op afterwards. Calling it twice is safe.
func (e *Enqueuer) Stop() {
	if e == nil {
		return
	}
	e.subMu.Lock()
	if e.stopped {
		e.subMu.Unlock()
		return
	}
	e.stopped = true
	started := e.started
	close(e.in)
	e.subMu.Unlock()
	if started {
		e.wg.Wait()
	}
	if dropped := e.dropped.Load(); dropped > 0 {
		log.Printf("[FEED] enqueue stopped: %d article(s) queued, %d dropped on a full queue",
			e.enqueued.Load(), dropped)
	}
}

// Hook returns the function to install with database.SetFeedHook.
func (e *Enqueuer) Hook() database.FeedHook {
	if e == nil {
		return nil
	}
	return func(a database.FeedArticle) {
		e.Submit(SnapshotFromDB(a))
	}
}

// Submit hands one committed article to the enqueuer. It never blocks: when the channel is
// full the snapshot is dropped, counted and logged (rate-limited), because the caller is
// the batch writer's goroutine and intake must not wait on the feed.
func (e *Enqueuer) Submit(a Snapshot) {
	if e == nil || a.MessageID == "" {
		return
	}
	e.subMu.RLock()
	defer e.subMu.RUnlock()
	if e.stopped {
		return
	}
	select {
	case e.in <- a:
	default:
		e.dropped.Add(1)
		if n, ok := e.dropLog.add(1, e.now()); ok {
			log.Printf("[FEED] queue full, %d articles not queued", n)
		}
	}
}

// Dropped is how many snapshots were dropped on a full channel so far.
func (e *Enqueuer) Dropped() int64 {
	if e == nil {
		return 0
	}
	return e.dropped.Load()
}

// Enqueued is how many queue rows were written so far (one article can be several rows).
func (e *Enqueuer) Enqueued() int64 {
	if e == nil {
		return 0
	}
	return e.enqueued.Load()
}

// InvalidatePeers drops the cached peer list, so the next article reads the peers table
// again. Without it an admin edit applies within peerListTTL anyway.
func (e *Enqueuer) InvalidatePeers() {
	if e == nil {
		return
	}
	e.peerMu.Lock()
	e.peersAt = time.Time{}
	e.peersFailed = false
	e.peerMu.Unlock()
}

// run batches the snapshots into the queue: by count (batchMax) or every flush interval.
func (e *Enqueuer) run() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.flush)
	defer ticker.Stop()
	batch := make([]Item, 0, e.batchMax)
	for {
		select {
		case snap, ok := <-e.in:
			if !ok {
				e.write(batch)
				return
			}
			batch = append(batch, e.Select(snap)...)
			if len(batch) >= e.batchMax {
				batch = e.write(batch)
			}
		case <-ticker.C:
			batch = e.write(batch)
		}
	}
}

// write enqueues batch in one transaction and returns it empty for reuse.
func (e *Enqueuer) write(batch []Item) []Item {
	if len(batch) == 0 {
		return batch
	}
	if err := e.q.Enqueue(batch); err != nil {
		if n, ok := e.writeLog.add(int64(len(batch)), e.now()); ok {
			log.Printf("[FEED] cannot queue %d article(s) for the peers: %v", n, err)
		}
		return batch[:0]
	}
	e.enqueued.Add(int64(len(batch)))
	return batch[:0]
}

// Select returns the queue rows one article produces: one per peer that wants it. A peer is
// skipped when
//
//   - its path_token is an element of the article's Path: the peer already has the article
//     (and that also covers the peer the article came from, because the inbound Path
//     rewrite of plan nntp-peering writes the peer's token into the Path);
//   - its out_patterns do not want the article (INN newsfeeds(5) semantics, including the
//     "@group" poison that refuses the whole article);
//   - its out_flags refuse the article (size, Path hops, newsgroup count, moderation).
func (e *Enqueuer) Select(a Snapshot) []Item {
	peers := e.outPeers()
	if len(peers) == 0 || a.MessageID == "" {
		return nil
	}
	a = e.withModeration(peers, a)
	var items []Item
	for _, p := range peers {
		if nntp.PathHasElement(a.Path, p.token) {
			continue
		}
		if !nntp.MatchNewsgroupPatterns(p.patterns, a.Newsgroups).Accept() {
			continue
		}
		if !p.flags.Allows(a) {
			continue
		}
		items = append(items, Item{PeerID: p.id, MessageID: a.MessageID, Newsgroup: a.Newsgroup})
	}
	return items
}

// withModeration fills a.Moderated when some peer's out_flags need it.
func (e *Enqueuer) withModeration(peers []*outPeer, a Snapshot) Snapshot {
	if a.Moderated != nil || len(a.Newsgroups) == 0 {
		return a
	}
	need := false
	for _, p := range peers {
		if p.flags.NeedsModeration() {
			need = true
			break
		}
	}
	if !need {
		return a
	}
	a.Moderated = e.moderation(a.Newsgroups)
	return a
}

// moderation returns the moderation status of groups, from the cache or from the main DB.
// A group whose row cannot be read is left out of the map, which Flags.Allows reads as
// unmoderated (plan nntp-peering: an unknown status is treated as unmoderated).
func (e *Enqueuer) moderation(groups []string) map[string]bool {
	out := make(map[string]bool, len(groups))
	now := e.now()
	var missing []string
	e.modMu.Lock()
	for _, g := range groups {
		if entry, ok := e.mod[g]; ok && now.Sub(entry.at) < moderationTTL {
			out[g] = entry.moderated
			continue
		}
		missing = append(missing, g)
	}
	e.modMu.Unlock()
	if len(missing) == 0 || e.store == nil {
		return out
	}
	// Read outside the cache mutex: the main DB may be busy for a moment.
	fresh := make(map[string]bool, len(missing))
	for _, g := range missing {
		ng, err := e.store.GetIntakeGroup(g)
		if err != nil || ng == nil {
			continue
		}
		fresh[g] = strings.EqualFold(strings.TrimSpace(ng.Status), "m")
	}
	e.modMu.Lock()
	if len(e.mod)+len(fresh) > moderationCacheMax {
		e.mod = make(map[string]moderationEntry, len(fresh))
	}
	for g, moderated := range fresh {
		e.mod[g] = moderationEntry{moderated: moderated, at: now}
		out[g] = moderated
	}
	e.modMu.Unlock()
	return out
}

// outPeers returns the cached out-feed peers, reading the peers table again when the cache
// is stale (peerListTTL, or peerListErrorTTL after a failed read). The read runs outside
// the mutex and only one at a time; every other caller meanwhile uses the last good list
// (nil until the first successful read, so nothing is queued before the table could be
// read). The returned slice and the peers in it are shared and only read.
func (e *Enqueuer) outPeers() []*outPeer {
	if e == nil || e.store == nil {
		return nil
	}
	e.peerMu.Lock()
	now := e.now()
	ttl := peerListTTL
	if e.peersFailed {
		ttl = peerListErrorTTL
	}
	if e.peersLoading || (!e.peersAt.IsZero() && now.Sub(e.peersAt) < ttl) {
		peers := e.peers
		e.peerMu.Unlock()
		return peers
	}
	e.peersLoading = true
	e.peerMu.Unlock()

	loaded, err := e.store.GetEnabledPeers()

	e.peerMu.Lock()
	defer e.peerMu.Unlock()
	e.peersLoading = false
	e.peersAt = e.now()
	if err != nil {
		e.peersFailed = true
		if e.peerErrAt.IsZero() || e.peersAt.Sub(e.peerErrAt) >= feedLogEvery {
			e.peerErrAt = e.peersAt
			log.Printf("[FEED] cannot read the peer list, keeping %d out feed(s): %v", len(e.peers), err)
		}
		return e.peers
	}
	e.peersFailed = false
	usable, skipped := parseOutPeers(loaded)
	e.peers = usable
	if names := outPeerNames(usable); names != e.peerNames {
		e.peerNames = names
		if names == "" {
			log.Printf("[FEED] no peer has an outgoing feed: nothing is queued")
		} else {
			log.Printf("[FEED] outgoing feed(s): %s", names)
		}
	}
	if names := strings.Join(skipped, ", "); names != e.peerSkipped {
		e.peerSkipped = names
		if names != "" {
			log.Printf("[FEED] peer(s) not fed, their row cannot be used: %s", names)
		}
	}
	return e.peers
}

// parseOutPeers picks the peers with an outgoing feed and parses their selection fields.
// A peer without a path_token is skipped: without it the Path check cannot tell whether
// the peer already has an article, so feeding it would loop articles straight back.
// A peer whose out_flags do not parse is skipped too (fail closed: out_flags exist to
// restrict what is sent, so a typo must not widen the feed).
func parseOutPeers(peers []*models.Peer) (usable []*outPeer, skipped []string) {
	for _, p := range peers {
		if p == nil || !p.Enabled || !p.OutEnabled {
			continue
		}
		token := strings.TrimSpace(p.PathToken)
		if token == "" {
			skipped = append(skipped, p.Name+" (no path_token)")
			continue
		}
		flags, err := ParseFlags(p.OutFlags)
		if err != nil {
			skipped = append(skipped, p.Name+" ("+err.Error()+")")
			continue
		}
		usable = append(usable, &outPeer{
			id:       p.ID,
			name:     p.Name,
			token:    token,
			patterns: p.OutPatterns,
			flags:    flags,
		})
	}
	return usable, skipped
}

// outPeerNames joins the peer names for the log line of outPeers.
func outPeerNames(peers []*outPeer) string {
	names := make([]string, 0, len(peers))
	for _, p := range peers {
		names = append(names, p.name)
	}
	return strings.Join(names, ", ")
}

// rateLog accumulates a count and reports when its line may be logged again, so a lasting
// fault costs one line per window instead of one per article.
type rateLog struct {
	mu    sync.Mutex
	every time.Duration
	at    time.Time
	n     int64
}

// add adds n to the accumulated count and, when the window has passed, returns that count
// and true, resetting it. The first call always logs.
func (r *rateLog) add(n int64, now time.Time) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n += n
	if !r.at.IsZero() && now.Sub(r.at) < r.every {
		return 0, false
	}
	r.at = now
	total := r.n
	r.n = 0
	return total, true
}
