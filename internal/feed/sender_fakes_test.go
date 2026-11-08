package feed

// Fakes shared by the sender tests: a queue (the real one is written in the feed-queue slice),
// a peer list, an article store, a clock and a transport. Nothing here talks to a socket; the
// protocol paths are tested against a fake NNTP peer in sender_conn_test.go.
//
// Everything here is named snd* so these helpers never collide with the queue tests of the
// same package.

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// sndWait is the hang guard of this package's tests: a passing test never waits on it.
// Override with PUGLEAF_TEST_DEADLINE (plan test-timing-hardening D3).
var sndWait = sndWaitFromEnv(60 * time.Second)

func sndWaitFromEnv(def time.Duration) time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// sndWaitFor polls cond until it holds or the hang guard expires.
func sndWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(sndWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// sndClock is a clock a test moves by hand.
type sndClock struct {
	mu sync.Mutex
	t  time.Time
}

func newSndClock() *sndClock {
	return &sndClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
}

func (c *sndClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *sndClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// sndDefer is one recorded Queue.Defer call.
type sndDefer struct {
	PeerID    int64
	MessageID string
	Err       string
	Next      time.Time
}

type sndRow struct {
	item Item
	next time.Time
}

// sndQueue is an in-memory Queue.
type sndQueue struct {
	mu      sync.Mutex
	rows    map[int64][]*sndRow
	stats   map[int64]Stats
	done    []string
	defers  []sndDefer
	dropped []int64
	claims  int
	writes  int
	prunes  int

	claimErr error
	doneErr  error
	depthErr error
}

func newSndQueue() *sndQueue {
	return &sndQueue{rows: make(map[int64][]*sndRow), stats: make(map[int64]Stats)}
}

func (q *sndQueue) Enqueue(items []Item) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, it := range items {
		q.rows[it.PeerID] = append(q.rows[it.PeerID], &sndRow{item: it})
	}
	return nil
}

func (q *sndQueue) Claim(peerID int64, max int, now time.Time, lease time.Duration) ([]Item, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims++
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	var out []Item
	for _, r := range q.rows[peerID] {
		if len(out) >= max {
			break
		}
		if r.next.After(now) {
			continue
		}
		r.next = now.Add(lease)
		out = append(out, r.item)
	}
	return out, nil
}

func (q *sndQueue) Done(peerID int64, messageIDs []string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.doneErr != nil {
		return q.doneErr
	}
	gone := make(map[string]bool, len(messageIDs))
	for _, id := range messageIDs {
		gone[id] = true
		q.done = append(q.done, id)
	}
	kept := q.rows[peerID][:0]
	for _, r := range q.rows[peerID] {
		if !gone[r.item.MessageID] {
			kept = append(kept, r)
		}
	}
	q.rows[peerID] = kept
	return nil
}

func (q *sndQueue) Defer(peerID int64, messageID string, lastErr string, next time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.defers = append(q.defers, sndDefer{PeerID: peerID, MessageID: messageID, Err: lastErr, Next: next})
	for _, r := range q.rows[peerID] {
		if r.item.MessageID == messageID {
			r.item.Attempts++
			r.item.LastError = lastErr
			r.next = next
		}
	}
	return nil
}

func (q *sndQueue) Depth(peerID int64) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.depthErr != nil {
		return 0, q.depthErr
	}
	return int64(len(q.rows[peerID])), nil
}

func (q *sndQueue) DropPeer(peerID int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dropped = append(q.dropped, peerID)
	delete(q.rows, peerID)
	delete(q.stats, peerID)
	return nil
}

func (q *sndQueue) WriteStats(s Stats) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.writes++
	q.stats[s.PeerID] = s
	return nil
}

func (q *sndQueue) ReadStats() (map[int64]Stats, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[int64]Stats, len(q.stats))
	for k, v := range q.stats {
		out[k] = v
	}
	return out, nil
}

func (q *sndQueue) Close() error { return nil }

// Prune is the optional method the sender calls through its small interface.
func (q *sndQueue) Prune(now time.Time) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prunes++
	return 0, nil
}

// snapshot helpers (every read takes the lock).

func (q *sndQueue) doneIDs() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.done...)
}

func (q *sndQueue) deferred() []sndDefer {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]sndDefer(nil), q.defers...)
}

func (q *sndQueue) droppedPeers() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]int64(nil), q.dropped...)
}

func (q *sndQueue) storedStats(peerID int64) (Stats, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	st, ok := q.stats[peerID]
	return st, ok
}

func (q *sndQueue) pruneCalls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.prunes
}

// sndPeerSource is a peer list a test changes while the sender runs. It returns every row, as
// database.GetPeers does, enabled or not.
type sndPeerSource struct {
	mu    sync.Mutex
	peers []*models.Peer
	err   error
	reads int
}

func (p *sndPeerSource) GetPeers() ([]*models.Peer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	if p.err != nil {
		return nil, p.err
	}
	out := make([]*models.Peer, 0, len(p.peers))
	for _, pp := range p.peers {
		c := *pp
		out = append(out, &c)
	}
	return out, nil
}

func (p *sndPeerSource) set(peers ...*models.Peer) {
	p.mu.Lock()
	p.peers = peers
	p.mu.Unlock()
}

func (p *sndPeerSource) readCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reads
}

// sndArticles is an in-memory article store keyed by newsgroup and message-id.
type sndArticles struct {
	mu    sync.Mutex
	arts  map[string]map[string]*models.Article
	err   error
	calls int
}

func newSndArticles() *sndArticles {
	return &sndArticles{arts: make(map[string]map[string]*models.Article)}
}

func (a *sndArticles) add(group string, art *models.Article) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.arts[group] == nil {
		a.arts[group] = make(map[string]*models.Article)
	}
	a.arts[group][art.MessageID] = art
}

func (a *sndArticles) GetArticlesByIDs(newsgroup *string, wantedIDs []*string) ([]*models.Article, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	if newsgroup == nil {
		return nil, fmt.Errorf("no newsgroup")
	}
	var out []*models.Article
	for _, id := range wantedIDs {
		if id == nil {
			continue
		}
		if art := a.arts[*newsgroup][*id]; art != nil {
			out = append(out, art)
		}
	}
	return out, nil
}

func (a *sndArticles) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// sndTransport answers Send from a scripted function.
type sndTransport struct {
	mu     sync.Mutex
	sent   [][]string
	closed int
	block  chan struct{} // non-nil: Send waits for it or for Close
	reply  func(call int, arts []Outgoing) ([]Result, int, error)
	d      *sndDialer
}

func (t *sndTransport) Mode() string { return "fake" }

// Close is idempotent, like the real transports: the sender may close one connection from the
// worker and from the watcher of a stopping feed.
func (t *sndTransport) Close() {
	t.mu.Lock()
	t.closed++
	first := t.closed == 1
	b := t.block
	t.block = nil
	t.mu.Unlock()
	if b != nil {
		close(b)
	}
	if first && t.d != nil {
		t.d.closed()
	}
}

func (t *sndTransport) closeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func (t *sndTransport) Send(arts []Outgoing) ([]Result, int, error) {
	t.mu.Lock()
	ids := make([]string, 0, len(arts))
	for _, a := range arts {
		ids = append(ids, a.Article.MessageID)
	}
	t.sent = append(t.sent, ids)
	call := len(t.sent)
	block := t.block
	reply := t.reply
	t.mu.Unlock()

	if block != nil {
		<-block
		return sndDeferAll(arts, "closed"), 0, fmt.Errorf("feed: closed while sending")
	}
	if reply != nil {
		return reply(call, arts)
	}
	return sndAcceptAll(arts), len(arts), nil
}

func (t *sndTransport) batches() [][]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]string, 0, len(t.sent))
	for _, b := range t.sent {
		out = append(out, append([]string(nil), b...))
	}
	return out
}

func sndAcceptAll(arts []Outgoing) []Result {
	out := make([]Result, 0, len(arts))
	for _, a := range arts {
		out = append(out, Result{MessageID: a.Article.MessageID, Code: 239, Outcome: OutcomeDone})
	}
	return out
}

func sndDeferAll(arts []Outgoing, reason string) []Result {
	out := make([]Result, 0, len(arts))
	for _, a := range arts {
		out = append(out, Result{MessageID: a.Article.MessageID, Outcome: OutcomeDefer, Err: reason})
	}
	return out
}

// sndDialer counts the connections it hands out and how many are open at once.
type sndDialer struct {
	mu      sync.Mutex
	dials   int
	live    int
	maxLive int
	err     error
	newT    func() *sndTransport
	last    *sndTransport
	before  func() // runs inside dial, before it returns: a test can hold the connect up
}

func (d *sndDialer) dial(p *models.Peer) (Transport, error) {
	d.mu.Lock()
	d.dials++
	before := d.before
	if d.err != nil {
		err := d.err
		d.mu.Unlock()
		return nil, err
	}
	if before != nil {
		d.mu.Unlock()
		before()
		d.mu.Lock()
	}
	d.live++
	if d.live > d.maxLive {
		d.maxLive = d.live
	}
	mk := d.newT
	d.mu.Unlock()

	var t *sndTransport
	if mk != nil {
		t = mk()
	} else {
		t = &sndTransport{}
	}
	t.d = d
	d.mu.Lock()
	d.last = t
	d.mu.Unlock()
	return t, nil
}

func (d *sndDialer) closed() {
	d.mu.Lock()
	d.live--
	d.mu.Unlock()
}

func (d *sndDialer) counts() (dials, live, maxLive int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials, d.live, d.maxLive
}

func (d *sndDialer) setErr(err error) {
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
}

// sndPeer is a peer row with an outgoing feed to a host that is never dialed by the fakes.
func sndPeer(id int64, name string) *models.Peer {
	return &models.Peer{
		ID: id, Name: name, PathToken: name, Enabled: true,
		OutEnabled: true, OutHost: "peer.invalid", OutPort: 119, MaxConns: 1,
	}
}

// sndArticle is a stored article with a complete header block, so the relay path keeps it.
func sndArticle(msgID, group string) *models.Article {
	headers := "Path: up.invalid!not-for-mail\n" +
		"From: A <a@b.invalid>\n" +
		"Newsgroups: " + group + "\n" +
		"Subject: feed test\n" +
		"Date: Sat, 12 Sep 2026 10:11:12 +0000\n" +
		"Message-ID: " + msgID
	return &models.Article{
		MessageID:   msgID,
		Subject:     "feed test",
		FromHeader:  "A <a@b.invalid>",
		DateString:  "Sat, 12 Sep 2026 10:11:12 +0000",
		Path:        "up.invalid!not-for-mail",
		HeadersJSON: headers,
		BodyText:    "a body line\n",
	}
}

// sndFixture is a sender with the fakes wired and short intervals.
type sndFixture struct {
	s     *Sender
	q     *sndQueue
	peers *sndPeerSource
	arts  *sndArticles
	clock *sndClock
	dial  *sndDialer
}

func newSndFixture(t *testing.T, tune func(*Config)) *sndFixture {
	t.Helper()
	f := &sndFixture{
		q:     newSndQueue(),
		peers: &sndPeerSource{},
		arts:  newSndArticles(),
		clock: newSndClock(),
		dial:  &sndDialer{},
	}
	cfg := Config{
		Queue:          f.q,
		Peers:          f.peers,
		Articles:       f.arts,
		Hostname:       "us.invalid",
		Clock:          f.clock,
		Dial:           f.dial.dial,
		PollInterval:   time.Millisecond,
		ReloadInterval: 2 * time.Millisecond,
		StatsInterval:  time.Hour, // the tests force the flush through Stop
		Batch:          10,
	}
	if tune != nil {
		tune(&cfg)
	}
	s, err := NewSender(cfg)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	f.s = s
	t.Cleanup(s.Stop)
	return f
}
