package feed

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// enqueue_test.go covers the enqueue side of the outgoing feed (plan nntp-peering, slice
// feed-queue): which peers a committed article is queued for, that a full channel drops
// instead of blocking the batch writer, and that Stop drains what is in flight.
//
// The clock is injected everywhere a window is checked (peer cache, moderation cache, the
// rate-limited log), so no test sleeps or asserts on wall-clock time; the only time.After
// in here is the hang guard of the overflow test (testWait, PUGLEAF_TEST_DEADLINE).

// enqStore is a Store for the tests: a fixed peer list plus newsgroup statuses, with call
// counters so the caching can be checked.
type enqStore struct {
	mu         sync.Mutex
	peers      []*models.Peer
	peerErr    error
	status     map[string]string // newsgroup -> newsgroups.status
	peerCalls  int
	groupCalls int
}

func (s *enqStore) GetEnabledPeers() ([]*models.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peerCalls++
	if s.peerErr != nil {
		return nil, s.peerErr
	}
	return s.peers, nil
}

func (s *enqStore) GetIntakeGroup(name string) (*database.IntakeGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groupCalls++
	status, ok := s.status[name]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &database.IntakeGroup{ID: 1, Name: name, Active: true, Status: status}, nil
}

func (s *enqStore) counts() (peers, groups int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerCalls, s.groupCalls
}

// enqPeer is a peers row with the defaults of the migration.
func enqPeer(id int64, name, token, patterns, flags string) *models.Peer {
	return &models.Peer{
		ID: id, Name: name, PathToken: token,
		OutPatterns: patterns, OutFlags: flags,
		OutEnabled: true, Enabled: true, MaxConns: 4,
	}
}

// enqSnap is a committed article with our identity already in its Path.
func enqSnap(id string, groups []string) Snapshot {
	return Snapshot{
		MessageID:  id,
		Path:       "ours.invalid!.POSTED!not-for-mail",
		Newsgroups: groups,
		Newsgroup:  groups[0],
		Bytes:      2000,
		Lines:      20,
	}
}

// enqPeerIDs are the peers an item list names, in order.
func enqPeerIDs(items []Item) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.PeerID)
	}
	return out
}

func enqSameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// enqNew returns an unstarted Enqueuer over store with a clock the caller can move.
func enqNew(t *testing.T, q Queue, store Store, cfg EnqueuerConfig) (*Enqueuer, *time.Time) {
	t.Helper()
	e := NewEnqueuer(q, store, cfg)
	now := qEpoch
	e.now = func() time.Time { return now }
	return e, &now
}

// TestEnqueueSelectSkipsPeersNamedInThePath: a peer whose path_token is a Path element
// already has the article; the keyword suffixes our own rewrite and other servers append
// do not hide it, and that is also how the peer an article came from is skipped.
func TestEnqueueSelectSkipsPeersNamedInThePath(t *testing.T) {
	store := &enqStore{peers: []*models.Peer{
		enqPeer(1, "alpha", "alpha.invalid", "*", ""),
		enqPeer(2, "beta", "beta.invalid", "*", ""),
		enqPeer(3, "gamma", "gamma.invalid", "*", ""),
		enqPeer(4, "delta", "delta.invalid", "*", ""),
	}}
	e, _ := enqNew(t, nil, store, EnqueuerConfig{})

	// alpha wrote the Path element plainly, beta is there as our MISMATCH rewrite, gamma
	// through another server's .SEEN form; only delta is left.
	snap := enqSnap("<a@x>", []string{"alt.test"})
	snap.Path = "ours.invalid!beta.invalid.MISMATCH!gamma.invalid.SEEN.192.0.2.1!alpha.invalid!not-for-mail"
	if got := enqPeerIDs(e.Select(snap)); !enqSameIDs(got, []int64{4}) {
		t.Errorf("Select queued for peers %v, want [4]: a peer named in the Path must be skipped", got)
	}

	// A host whose name only starts like a keyword component keeps its name.
	snap.Path = "ours.invalid!alpha.invalidish!not-for-mail"
	if got := enqPeerIDs(e.Select(snap)); !enqSameIDs(got, []int64{1, 2, 3, 4}) {
		t.Errorf("Select queued for peers %v, want every peer: %q is not alpha.invalid", got, snap.Path)
	}
	// Case does not matter.
	snap.Path = "ours.invalid!ALPHA.INVALID!not-for-mail"
	if got := enqPeerIDs(e.Select(snap)); !enqSameIDs(got, []int64{2, 3, 4}) {
		t.Errorf("Select queued for peers %v, want [2 3 4]: the Path comparison is case-insensitive", got)
	}
}

// TestEnqueueSelectOutPatterns: INN newsfeeds(5) semantics, "!" excludes one group and "@"
// poisons the whole article.
func TestEnqueueSelectOutPatterns(t *testing.T) {
	store := &enqStore{peers: []*models.Peer{
		enqPeer(1, "all", "all.invalid", "*", ""),
		enqPeer(2, "nocontrol", "nocontrol.invalid", "*,!control*", ""),
		enqPeer(3, "poison", "poison.invalid", "*,@alt.binaries*", ""),
		enqPeer(4, "comp", "comp.invalid", "comp.*", ""),
	}}
	e, _ := enqNew(t, nil, store, EnqueuerConfig{})

	if got := enqPeerIDs(e.Select(enqSnap("<a@x>", []string{"alt.test"}))); !enqSameIDs(got, []int64{1, 2, 3}) {
		t.Errorf("alt.test went to %v, want [1 2 3] (comp.* does not want it)", got)
	}
	if got := enqPeerIDs(e.Select(enqSnap("<b@x>", []string{"control.cancel"}))); !enqSameIDs(got, []int64{1, 3}) {
		t.Errorf("control.cancel went to %v, want [1 3] (!control* excludes it)", got)
	}
	// Crossposted to a poisoned group: peer 3 gets nothing of it, although alt.test is wanted.
	if got := enqPeerIDs(e.Select(enqSnap("<c@x>", []string{"alt.test", "alt.binaries.pics"}))); !enqSameIDs(got, []int64{1, 2}) {
		t.Errorf("a crosspost into a poisoned group went to %v, want [1 2]", got)
	}
	// "*" matches junk for peers 1..3; comp.* does not, so peer 4 gets no row.
	if got := enqPeerIDs(e.Select(enqSnap("<d@x>", []string{"junk"}))); !enqSameIDs(got, []int64{1, 2, 3}) {
		t.Errorf("junk went to %v, want [1 2 3]", got)
	}
	if got := enqPeerIDs(e.Select(enqSnap("<e@x>", []string{"comp.lang.go"}))); !enqSameIDs(got, []int64{1, 2, 3, 4}) {
		t.Errorf("comp.lang.go went to %v, want every peer", got)
	}
}

// TestEnqueueSelectOutFlags: one peer per flag of the subset.
func TestEnqueueSelectOutFlags(t *testing.T) {
	store := &enqStore{
		peers: []*models.Peer{
			enqPeer(1, "small", "small.invalid", "*", "<1000"),
			enqPeer(2, "big", "big.invalid", "*", ">1000"),
			enqPeer(3, "hops", "hops.invalid", "*", "H2"),
			enqPeer(4, "groups", "groups.invalid", "*", "G1"),
			enqPeer(5, "mod", "mod.invalid", "*", "Nm"),
			enqPeer(6, "unmod", "unmod.invalid", "*", "Nu"),
		},
		status: map[string]string{"alt.moderated": "m", "alt.test": "y", "alt.two": "y"},
	}
	e, _ := enqNew(t, nil, store, EnqueuerConfig{})

	// 2000 bytes, 3 Path hops, one unmoderated group: too big for <1000, big enough for
	// >1000, too many hops for H2, one group is fine for G1, unmoderated so Nu only.
	if got := enqPeerIDs(e.Select(enqSnap("<a@x>", []string{"alt.test"}))); !enqSameIDs(got, []int64{2, 4, 6}) {
		t.Errorf("the 2000-byte article went to %v, want [2 4 6]", got)
	}
	// Exactly 1000 bytes satisfies both <1000 and >1000 (INN's comparison).
	exact := enqSnap("<exact@x>", []string{"alt.test"})
	exact.Bytes = 1000
	exact.Path = "ours.invalid!not-for-mail"
	if got := enqPeerIDs(e.Select(exact)); !enqSameIDs(got, []int64{1, 2, 3, 4, 6}) {
		t.Errorf("the 1000-byte article went to %v, want [1 2 3 4 6]: exactly N satisfies <N and >N", got)
	}
	// A small article with two hops and two groups.
	small := enqSnap("<b@x>", []string{"alt.test", "alt.two"})
	small.Bytes = 500
	small.Path = "ours.invalid!not-for-mail"
	if got := enqPeerIDs(e.Select(small)); !enqSameIDs(got, []int64{1, 3, 6}) {
		t.Errorf("the small two-group article went to %v, want [1 3 6]", got)
	}
	// A moderated group goes to Nm, not to Nu.
	moderated := enqSnap("<c@x>", []string{"alt.moderated"})
	if got := enqPeerIDs(e.Select(moderated)); !enqSameIDs(got, []int64{2, 4, 5}) {
		t.Errorf("the moderated article went to %v, want [2 4 5]", got)
	}
	// A crosspost into a moderated and an unmoderated group reaches BOTH the Nm and the
	// Nu peer, each for its own group: INN narrows the per-group subscription
	// (innd/newsfeeds.c SITEparseone) and feeds the site if any group survives
	// (innd/art.c ARTpost). G1 drops out because the article has two newsgroups.
	mixed := enqSnap("<d@x>", []string{"alt.moderated", "alt.test"})
	mixed.Path = "ours.invalid!not-for-mail"
	if got := enqPeerIDs(e.Select(mixed)); !enqSameIDs(got, []int64{2, 3, 5, 6}) {
		t.Errorf("the mixed crosspost went to %v, want [2 3 5 6]", got)
	}
	// With out_patterns that subscribe only to the unmoderated group, Nm has nothing
	// left and the Nm peer gets no row.
	narrow := &enqStore{
		peers: []*models.Peer{
			enqPeer(5, "mod", "mod.invalid", "alt.test", "Nm"),
			enqPeer(6, "unmod", "unmod.invalid", "alt.test", "Nu"),
		},
		status: map[string]string{"alt.moderated": "m", "alt.test": "y"},
	}
	eNarrow, _ := enqNew(t, nil, narrow, EnqueuerConfig{})
	if got := enqPeerIDs(eNarrow.Select(mixed)); !enqSameIDs(got, []int64{6}) {
		t.Errorf("with only alt.test subscribed the mixed crosspost went to %v, want [6]", got)
	}
}

// TestEnqueueSelectSkipsUnusablePeers: a peer row that cannot be used is left out, and a
// peers table that cannot be read queues nothing (fail closed).
func TestEnqueueSelectSkipsUnusablePeers(t *testing.T) {
	disabled := enqPeer(2, "disabled", "dis.invalid", "*", "")
	disabled.Enabled = false
	noOut := enqPeer(3, "noout", "noout.invalid", "*", "")
	noOut.OutEnabled = false
	store := &enqStore{peers: []*models.Peer{
		enqPeer(1, "good", "good.invalid", "*", ""),
		disabled,
		noOut,
		enqPeer(4, "notoken", "", "*", ""),
		enqPeer(5, "badflags", "bad.invalid", "*", "Ap,Tm"),
		nil,
	}}
	e, _ := enqNew(t, nil, store, EnqueuerConfig{})
	if got := enqPeerIDs(e.Select(enqSnap("<a@x>", []string{"alt.test"}))); !enqSameIDs(got, []int64{1}) {
		t.Errorf("Select queued for %v, want [1]: disabled, out-disabled, token-less and unparsable rows are skipped", got)
	}

	broken := &enqStore{peerErr: fmt.Errorf("main database is locked")}
	e2, _ := enqNew(t, nil, broken, EnqueuerConfig{})
	if got := e2.Select(enqSnap("<b@x>", []string{"alt.test"})); len(got) != 0 {
		t.Errorf("Select with an unreadable peers table queued %d row(s), want 0", len(got))
	}
	// An article without a message-id is never queued, and neither is one with no peers.
	if got := e.Select(Snapshot{Newsgroups: []string{"alt.test"}}); len(got) != 0 {
		t.Errorf("Select of an article without a message-id queued %d row(s), want 0", len(got))
	}
}

// TestEnqueuePeerListIsCached: the peers table is read once per peerListTTL, and a failed
// read is retried after the shorter window.
func TestEnqueuePeerListIsCached(t *testing.T) {
	store := &enqStore{peers: []*models.Peer{enqPeer(1, "one", "one.invalid", "*", "")}}
	e, now := enqNew(t, nil, store, EnqueuerConfig{})

	for i := 0; i < 5; i++ {
		e.Select(enqSnap("<a@x>", []string{"alt.test"}))
	}
	if reads, _ := store.counts(); reads != 1 {
		t.Fatalf("the peers table was read %d times for 5 articles, want 1", reads)
	}
	*now = qEpoch.Add(peerListTTL - time.Nanosecond)
	e.Select(enqSnap("<b@x>", []string{"alt.test"}))
	if reads, _ := store.counts(); reads != 1 {
		t.Errorf("the peers table was read %d times inside the TTL, want 1", reads)
	}
	*now = qEpoch.Add(peerListTTL)
	e.Select(enqSnap("<c@x>", []string{"alt.test"}))
	if reads, _ := store.counts(); reads != 2 {
		t.Errorf("the peers table was read %d times after the TTL, want 2", reads)
	}

	// InvalidatePeers makes the next article read again.
	e.InvalidatePeers()
	e.Select(enqSnap("<d@x>", []string{"alt.test"}))
	if reads, _ := store.counts(); reads != 3 {
		t.Errorf("the peers table was read %d times after InvalidatePeers, want 3", reads)
	}

	// A failed read keeps the last good list and is retried after peerListErrorTTL.
	store.mu.Lock()
	store.peerErr = fmt.Errorf("locked")
	store.mu.Unlock()
	*now = qEpoch.Add(10 * peerListTTL)
	if got := enqPeerIDs(e.Select(enqSnap("<e@x>", []string{"alt.test"}))); !enqSameIDs(got, []int64{1}) {
		t.Errorf("after a failed read Select queued for %v, want the last good list [1]", got)
	}
	before, _ := store.counts()
	*now = qEpoch.Add(10*peerListTTL + peerListErrorTTL - time.Nanosecond)
	e.Select(enqSnap("<f@x>", []string{"alt.test"}))
	if reads, _ := store.counts(); reads != before {
		t.Errorf("the failed read was retried inside peerListErrorTTL (%d -> %d)", before, reads)
	}
	*now = qEpoch.Add(10*peerListTTL + peerListErrorTTL)
	e.Select(enqSnap("<g@x>", []string{"alt.test"}))
	if reads, _ := store.counts(); reads != before+1 {
		t.Errorf("the failed read was not retried after peerListErrorTTL (%d -> %d)", before, reads)
	}
}

// TestEnqueueModerationIsCachedAndOnlyReadWhenNeeded: the newsgroups row is only read when
// some peer carries Nm or Nu, and then at most once per moderationTTL.
func TestEnqueueModerationIsCachedAndOnlyReadWhenNeeded(t *testing.T) {
	plain := &enqStore{
		peers:  []*models.Peer{enqPeer(1, "plain", "plain.invalid", "*", "<100000")},
		status: map[string]string{"alt.test": "y"},
	}
	e, _ := enqNew(t, nil, plain, EnqueuerConfig{})
	e.Select(enqSnap("<a@x>", []string{"alt.test"}))
	if _, groups := plain.counts(); groups != 0 {
		t.Errorf("the newsgroups row was read %d times without Nm/Nu, want 0", groups)
	}

	store := &enqStore{
		peers:  []*models.Peer{enqPeer(1, "mod", "mod.invalid", "*", "Nm")},
		status: map[string]string{"alt.moderated": "m"},
	}
	e2, now := enqNew(t, nil, store, EnqueuerConfig{})
	for i := 0; i < 4; i++ {
		if got := enqPeerIDs(e2.Select(enqSnap("<b@x>", []string{"alt.moderated"}))); !enqSameIDs(got, []int64{1}) {
			t.Fatalf("the moderated article went to %v, want [1]", got)
		}
	}
	if _, groups := store.counts(); groups != 1 {
		t.Errorf("the newsgroups row was read %d times for 4 articles, want 1 (cached)", groups)
	}
	*now = qEpoch.Add(moderationTTL)
	e2.Select(enqSnap("<c@x>", []string{"alt.moderated"}))
	if _, groups := store.counts(); groups != 2 {
		t.Errorf("the newsgroups row was read %d times after moderationTTL, want 2", groups)
	}
	// A group the main DB has no row for has no known status, so Nm cannot count it as
	// subscribed: no row. The absence is cached too, so the next article does not read
	// the main DB again for it.
	if got := e2.Select(enqSnap("<d@x>", []string{"alt.unknown"})); len(got) != 0 {
		t.Errorf("Nm accepted a group of unknown status (%d row(s))", len(got))
	}
	_, afterUnknown := store.counts()
	if got := e2.Select(enqSnap("<d2@x>", []string{"alt.unknown"})); len(got) != 0 {
		t.Errorf("Nm accepted a group of unknown status on the second try (%d row(s))", len(got))
	}
	if _, groups := store.counts(); groups != afterUnknown {
		t.Errorf("the unknown group was read again (%d -> %d): its absence must be cached too",
			afterUnknown, groups)
	}
	// The cache is dropped instead of growing without bound.
	e2.modMu.Lock()
	for i := 0; i < moderationCacheMax; i++ {
		e2.mod[fmt.Sprintf("alt.fill.%d", i)] = moderationEntry{at: *now}
	}
	e2.modMu.Unlock()
	e2.Select(enqSnap("<e@x>", []string{"alt.moderated2"}))
	e2.modMu.Lock()
	size := len(e2.mod)
	e2.modMu.Unlock()
	if size > moderationCacheMax {
		t.Errorf("the moderation cache holds %d entries, more than the %d bound", size, moderationCacheMax)
	}
}

// TestEnqueueSubmitDropsOnAFullChannel: Submit never blocks the batch writer; a full
// channel costs a counted, rate-limited drop.
func TestEnqueueSubmitDropsOnAFullChannel(t *testing.T) {
	store := &enqStore{peers: []*models.Peer{enqPeer(1, "one", "one.invalid", "*", "")}}
	// Not started: nothing drains the channel, so it fills after two snapshots.
	e, now := enqNew(t, nil, store, EnqueuerConfig{QueueSize: 2})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			e.Submit(enqSnap(fmt.Sprintf("<%d@x>", i), []string{"alt.test"}))
		}
	}()
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatalf("Submit blocked for %v on a full channel: intake must never wait on the feed", testWait)
	}
	if got := e.Dropped(); got != 8 {
		t.Errorf("Dropped() = %d after 10 submits into a channel of 2, want 8", got)
	}
	if got := e.Enqueued(); got != 0 {
		t.Errorf("Enqueued() = %d without a running goroutine, want 0", got)
	}
	// The drop line is rate-limited: the first drop logs, the rest accumulate.
	*now = qEpoch.Add(feedLogEvery)
	e.Submit(enqSnap("<late@x>", []string{"alt.test"}))
	if got := e.Dropped(); got != 9 {
		t.Errorf("Dropped() = %d, want 9", got)
	}
	// An article without a message-id is not even counted.
	e.Submit(Snapshot{})
	if got := e.Dropped(); got != 9 {
		t.Errorf("Dropped() = %d after submitting an article without a message-id, want 9", got)
	}
}

// TestEnqueueStopDrains: Stop writes what is still in the channel and returns; Submit after
// Stop is a no-op (and never panics on a closed channel).
func TestEnqueueStopDrains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed-queue.sq3")
	q, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := q.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	store := &enqStore{peers: []*models.Peer{
		enqPeer(1, "one", "one.invalid", "*", ""),
		enqPeer(2, "two", "two.invalid", "*", ""),
	}}
	// A flush interval that never fires in this test: only Stop writes, so the result does
	// not depend on timing.
	e, _ := enqNew(t, q, store, EnqueuerConfig{BatchMax: 1000, FlushInterval: time.Hour})
	e.Start()
	e.Start() // idempotent
	const articles = 25
	for i := 0; i < articles; i++ {
		e.Submit(enqSnap(fmt.Sprintf("<drain-%d@x>", i), []string{"alt.test"}))
	}
	e.Stop()
	e.Stop() // idempotent
	e.Submit(enqSnap("<after-stop@x>", []string{"alt.test"}))

	if got := e.Dropped(); got != 0 {
		t.Fatalf("Dropped() = %d, want 0: the default channel holds far more than %d", got, articles)
	}
	if got := e.Enqueued(); got != 2*articles {
		t.Errorf("Enqueued() = %d, want %d (one row per peer per article)", got, 2*articles)
	}
	for _, peer := range []int64{1, 2} {
		depth, err := q.Depth(peer)
		if err != nil {
			t.Fatalf("Depth(%d): %v", peer, err)
		}
		if depth != articles {
			t.Errorf("Depth(%d) = %d, want %d", peer, depth, articles)
		}
	}
	items, err := q.Claim(1, 1, qEpoch.Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(items) != 1 || items[0].Newsgroup != "alt.test" {
		t.Errorf("claimed %+v, want one item of alt.test", items)
	}
}

// TestEnqueueBatchMaxFlushes: a batch that reaches BatchMax is written without waiting for
// the flush interval (which is set so long that it cannot be what wrote the rows).
func TestEnqueueBatchMaxFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed-queue.sq3")
	q, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := q.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	store := &enqStore{peers: []*models.Peer{enqPeer(1, "one", "one.invalid", "*", "")}}
	e, _ := enqNew(t, q, store, EnqueuerConfig{BatchMax: 1, FlushInterval: time.Hour})
	e.Start()
	defer e.Stop()
	e.Submit(enqSnap("<batch@x>", []string{"alt.test"}))

	// Poll instead of sleeping: the write happens in the enqueue goroutine, and the wait
	// is only a hang guard.
	deadline := time.Now().Add(testWait)
	for e.Enqueued() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := e.Enqueued(); got != 1 {
		t.Fatalf("Enqueued() = %d within %v, want 1: BatchMax did not flush", got, testWait)
	}
	if depth, err := q.Depth(1); err != nil || depth != 1 {
		t.Errorf("Depth(1) = %d, %v; want 1, nil", depth, err)
	}
}

// TestEnqueueHookConvertsTheBatchSnapshot: the function handed to database.SetFeedHook
// carries every field through.
func TestEnqueueHookConvertsTheBatchSnapshot(t *testing.T) {
	store := &enqStore{peers: []*models.Peer{enqPeer(1, "one", "one.invalid", "*", "")}}
	e, _ := enqNew(t, nil, store, EnqueuerConfig{QueueSize: 4})
	hook := e.Hook()
	if hook == nil {
		t.Fatal("Hook() is nil")
	}
	hook(database.FeedArticle{
		MessageID: "<hook@x>", Path: "ours.invalid!x", Newsgroups: []string{"alt.a", "alt.b"},
		Newsgroup: "alt.b", Bytes: 1234, Lines: 9,
	})
	got := <-e.in
	want := Snapshot{MessageID: "<hook@x>", Path: "ours.invalid!x", Newsgroup: "alt.b", Bytes: 1234, Lines: 9}
	if got.MessageID != want.MessageID || got.Path != want.Path || got.Newsgroup != want.Newsgroup ||
		got.Bytes != want.Bytes || got.Lines != want.Lines {
		t.Errorf("the hook produced %+v, want %+v", got, want)
	}
	if len(got.Newsgroups) != 2 || got.Newsgroups[0] != "alt.a" || got.Newsgroups[1] != "alt.b" {
		t.Errorf("Newsgroups = %v, want [alt.a alt.b]", got.Newsgroups)
	}
	var nilE *Enqueuer
	if nilE.Hook() != nil || nilE.Dropped() != 0 || nilE.Enqueued() != 0 {
		t.Error("the methods of a nil Enqueuer are not no-ops")
	}
	nilE.Submit(Snapshot{MessageID: "<x@x>"})
	nilE.Start()
	nilE.Stop()
	nilE.InvalidatePeers()
}

// TestEnqueueRateLog pins the rate limit of the "queue full" and "cannot queue" lines: the
// first call logs, calls inside the window only accumulate, and the next line carries the
// whole count.
func TestEnqueueRateLog(t *testing.T) {
	r := &rateLog{every: feedLogEvery}
	if n, ok := r.add(1, qEpoch); !ok || n != 1 {
		t.Fatalf("first add = %d, %v; want 1, true", n, ok)
	}
	if n, ok := r.add(3, qEpoch.Add(time.Second)); ok {
		t.Errorf("an add inside the window logged %d", n)
	}
	if n, ok := r.add(4, qEpoch.Add(feedLogEvery)); !ok || n != 7 {
		t.Errorf("add after the window = %d, %v; want 7, true", n, ok)
	}
}

// TestEnqueueInvalidateDuringAnInFlightLoad: InvalidatePeers while a read of the peers table
// is in flight must not let the stale result restart the TTL - otherwise an admin edit could
// be ignored for a whole peerListTTL. The store blocks inside GetEnabledPeers so the
// invalidation lands in the middle of the read.
func TestEnqueueInvalidateDuringAnInFlightLoad(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	store := &enqBlockingStore{
		enqStore: enqStore{peers: []*models.Peer{enqPeer(1, "stale", "stale.invalid", "*", "")}},
		entered:  entered,
		release:  release,
	}
	e, _ := enqNew(t, nil, store, EnqueuerConfig{})

	done := make(chan []int64, 1)
	go func() {
		done <- enqPeerIDs(e.Select(enqSnap("<a@x>", []string{"alt.test"})))
	}()
	select {
	case <-entered:
	case <-time.After(testWait):
		t.Fatal("the peer read was never entered")
	}
	// The edit lands while the read is in flight; the peer list it returns predates it.
	e.InvalidatePeers()
	store.mu.Lock()
	store.peers = []*models.Peer{enqPeer(2, "fresh", "fresh.invalid", "*", "")}
	store.mu.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("Select did not return")
	}
	// The next article must read the table again and see the edit.
	if got := enqPeerIDs(e.Select(enqSnap("<b@x>", []string{"alt.test"}))); !enqSameIDs(got, []int64{2}) {
		t.Errorf("after an invalidation during the read the next article went to %v, want the edited list [2]: the stale result restarted the TTL", got)
	}
}

// enqBlockingStore blocks the first GetEnabledPeers until release is closed, so a test can
// run InvalidatePeers while that read is in flight.
type enqBlockingStore struct {
	enqStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *enqBlockingStore) GetEnabledPeers() ([]*models.Peer, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return s.enqStore.GetEnabledPeers()
}

// TestEnqueueStopBoundsTheDrain: a queue whose Enqueue never returns must not hold up the
// shutdown for ever - Stop gives up after StopWait and says so.
func TestEnqueueStopBoundsTheDrain(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) }) // let the leaked goroutine finish after the test
	store := &enqStore{peers: []*models.Peer{enqPeer(1, "one", "one.invalid", "*", "")}}
	e, _ := enqNew(t, &enqStuckQueue{block: block}, store, EnqueuerConfig{
		BatchMax: 1, FlushInterval: time.Hour, StopWait: 50 * time.Millisecond,
	})
	e.Start()
	e.Submit(enqSnap("<stuck@x>", []string{"alt.test"}))

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		e.Stop()
	}()
	select {
	case <-returned:
	case <-time.After(testWait):
		t.Fatalf("Stop did not return within %v although StopWait is %v: the drain is unbounded",
			testWait, 50*time.Millisecond)
	}
}

// enqStuckQueue is a Queue whose Enqueue blocks until block is closed.
type enqStuckQueue struct {
	block chan struct{}
}

func (q *enqStuckQueue) Enqueue(items []Item) error {
	<-q.block
	return nil
}

func (q *enqStuckQueue) Claim(int64, int, time.Time, time.Duration) ([]Item, error) { return nil, nil }
func (q *enqStuckQueue) Done(int64, []string) error                                 { return nil }
func (q *enqStuckQueue) Defer(int64, string, string, time.Time) error               { return nil }
func (q *enqStuckQueue) Depth(int64) (int64, error)                                 { return 0, nil }
func (q *enqStuckQueue) DropPeer(int64) error                                       { return nil }
func (q *enqStuckQueue) WriteStats(Stats) error                                     { return nil }
func (q *enqStuckQueue) ReadStats() (map[int64]Stats, error)                        { return nil, nil }
func (q *enqStuckQueue) Close() error                                               { return nil }
