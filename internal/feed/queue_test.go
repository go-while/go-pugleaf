package feed

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// queue_test.go covers the SQLite outgoing queue (plan nntp-peering, slice feed-queue).
// Every time is injected (qNow / the now argument of Claim and Prune), so no test sleeps or
// asserts on wall-clock time.

// testWait is the hang guard of this package's tests: it only stops a hung test, a passing
// test never waits on it. Override with PUGLEAF_TEST_DEADLINE (a Go duration, e.g. 120s);
// default 60 s. Plan test-timing-hardening D3.
var testWait = testWaitFromEnv(60 * time.Second)

func testWaitFromEnv(def time.Duration) time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// qEpoch is the fixed "now" the queue tests build their times from.
var qEpoch = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// qOpen opens a queue in a temp dir with an injectable clock pinned to qEpoch.
func qOpen(t *testing.T) (*SQLiteQueue, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feed-queue.sq3")
	q, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() {
		if err := q.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	now := qEpoch
	q.now = func() time.Time { return now }
	return q, &now
}

func qItem(peer int64, id, group string) Item {
	return Item{PeerID: peer, MessageID: id, Newsgroup: group}
}

func qIDs(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.MessageID)
	}
	return out
}

func qEqual(got, want []string) bool {
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

func TestQueueOpenCreatesTheFile(t *testing.T) {
	q, _ := qOpen(t)
	if _, err := os.Stat(q.Path()); err != nil {
		t.Fatalf("the queue file was not created: %v", err)
	}
	// Opening the same file again must only add what is missing.
	again, err := Open(q.Path())
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Open(""); err == nil {
		t.Error("Open(\"\") returned no error")
	}
}

func TestQueueEnqueueIsIdempotent(t *testing.T) {
	q, now := qOpen(t)
	if err := q.Enqueue([]Item{qItem(1, "<a@x>", "alt.a"), qItem(1, "<b@x>", "alt.b")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// A second offer of the same id must not reset attempts or the backoff.
	if err := q.Defer(1, "<a@x>", "431 try again", qEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("Defer: %v", err)
	}
	*now = qEpoch.Add(time.Minute)
	if err := q.Enqueue([]Item{qItem(1, "<a@x>", "alt.other")}); err != nil {
		t.Fatalf("Enqueue again: %v", err)
	}
	depth, err := q.Depth(1)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 2 {
		t.Fatalf("Depth = %d after re-enqueueing a queued id, want 2", depth)
	}
	// Only <b@x> is due; <a@x> keeps its deferred next attempt.
	items, err := q.Claim(1, 10, qEpoch.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !qEqual(qIDs(items), []string{"<b@x>"}) {
		t.Errorf("Claim returned %v, want [<b@x>]: the re-enqueue reset the deferred row", qIDs(items))
	}
	// Items without a peer or a message-id are skipped, and an empty batch is a no-op.
	if err := q.Enqueue(nil); err != nil {
		t.Fatalf("Enqueue(nil): %v", err)
	}
	if err := q.Enqueue([]Item{qItem(0, "<c@x>", "alt.c"), qItem(2, "", "alt.c")}); err != nil {
		t.Fatalf("Enqueue(invalid): %v", err)
	}
	if d, err := q.Depth(2); err != nil || d != 0 {
		t.Errorf("Depth(peer 2) = %d, %v; want 0, nil", d, err)
	}
}

func TestQueueClaimOrderLeaseAndDue(t *testing.T) {
	q, now := qOpen(t)
	// Three items enqueued at rising times, so "oldest first" is decidable.
	for i, id := range []string{"<1@x>", "<2@x>", "<3@x>"} {
		*now = qEpoch.Add(time.Duration(i) * time.Second)
		if err := q.Enqueue([]Item{qItem(7, id, "alt.q")}); err != nil {
			t.Fatalf("Enqueue %s: %v", id, err)
		}
	}
	claimAt := qEpoch.Add(time.Minute)
	first, err := q.Claim(7, 2, claimAt, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !qEqual(qIDs(first), []string{"<1@x>", "<2@x>"}) {
		t.Fatalf("Claim(max 2) = %v, want [<1@x> <2@x>] (oldest first)", qIDs(first))
	}
	if first[0].Newsgroup != "alt.q" || first[0].Attempts != 0 || first[0].LastError != "" {
		t.Errorf("claimed item = %+v, want newsgroup alt.q, 0 attempts, no error", first[0])
	}
	// The lease moved the two claimed rows out of the way: only <3@x> is due now.
	second, err := q.Claim(7, 10, claimAt, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim again: %v", err)
	}
	if !qEqual(qIDs(second), []string{"<3@x>"}) {
		t.Errorf("the second Claim returned %v, want [<3@x>]: the lease did not hold", qIDs(second))
	}
	// Once the lease has run out the rows come back by themselves (a crashed sender).
	back, err := q.Claim(7, 10, claimAt.Add(31*time.Second), 30*time.Second)
	if err != nil {
		t.Fatalf("Claim after the lease: %v", err)
	}
	if !qEqual(qIDs(back), []string{"<1@x>", "<2@x>", "<3@x>"}) {
		t.Errorf("Claim after the lease = %v, want all three back", qIDs(back))
	}
	// max <= 0 claims nothing, and another peer's queue is untouched.
	if items, err := q.Claim(7, 0, claimAt, time.Minute); err != nil || len(items) != 0 {
		t.Errorf("Claim(max 0) = %v, %v; want nothing", qIDs(items), err)
	}
	if items, err := q.Claim(8, 10, claimAt, time.Minute); err != nil || len(items) != 0 {
		t.Errorf("Claim(other peer) = %v, %v; want nothing", qIDs(items), err)
	}
}

func TestQueueDoneAndDefer(t *testing.T) {
	q, _ := qOpen(t)
	if err := q.Enqueue([]Item{qItem(3, "<a@x>", "alt.a"), qItem(3, "<b@x>", "alt.b"), qItem(3, "<c@x>", "alt.c")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.Done(3, []string{"<a@x>", "", "<a@x>", "<nosuch@x>"}); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if err := q.Done(3, nil); err != nil {
		t.Fatalf("Done(nil): %v", err)
	}
	if d, err := q.Depth(3); err != nil || d != 2 {
		t.Fatalf("Depth after Done = %d, %v; want 2, nil", d, err)
	}

	next := qEpoch.Add(5 * time.Minute)
	if err := q.Defer(3, "<b@x>", "400 server\r\nbusy", next); err != nil {
		t.Fatalf("Defer: %v", err)
	}
	if err := q.Defer(3, "<b@x>", "431 later", next); err != nil {
		t.Fatalf("Defer again: %v", err)
	}
	if err := q.Defer(3, "<gone@x>", "x", next); err != nil {
		t.Fatalf("Defer of a settled item must not fail: %v", err)
	}
	if err := q.Defer(3, "", "x", next); err != nil {
		t.Fatalf("Defer(\"\"): %v", err)
	}
	items, err := q.Claim(3, 10, next, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	var b Item
	for _, it := range items {
		if it.MessageID == "<b@x>" {
			b = it
		}
	}
	if b.Attempts != 2 {
		t.Errorf("attempts after two Defers = %d, want 2", b.Attempts)
	}
	if b.LastError != "431 later" {
		t.Errorf("last error = %q, want %q", b.LastError, "431 later")
	}
	// A multi-line error is stored on one line.
	if got := queueTrimError("400 server\r\nbusy"); got != "400 server  busy" {
		t.Errorf("queueTrimError = %q, want a single line", got)
	}
}

func TestQueueDropPeer(t *testing.T) {
	q, _ := qOpen(t)
	if err := q.Enqueue([]Item{qItem(1, "<a@x>", "alt.a"), qItem(2, "<a@x>", "alt.a")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.WriteStats(Stats{PeerID: 1, Offered: 5, Accepted: 4}); err != nil {
		t.Fatalf("WriteStats: %v", err)
	}
	if err := q.DropPeer(1); err != nil {
		t.Fatalf("DropPeer: %v", err)
	}
	if d, err := q.Depth(1); err != nil || d != 0 {
		t.Errorf("Depth(1) after DropPeer = %d, %v; want 0, nil", d, err)
	}
	if d, err := q.Depth(2); err != nil || d != 1 {
		t.Errorf("Depth(2) after DropPeer(1) = %d, %v; want 1, nil", d, err)
	}
	stats, err := q.ReadStats()
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if _, ok := stats[1]; ok {
		t.Error("the stats of the dropped peer are still stored")
	}
}

func TestQueueStatsRoundTrip(t *testing.T) {
	q, _ := qOpen(t)
	updated := qEpoch.Add(-time.Hour).Truncate(time.Second)
	in := Stats{PeerID: 11, Offered: 10, Accepted: 6, Refused: 2, Rejected: 1, Deferred: 1,
		LastError: "436 try later", UpdatedAt: updated}
	if err := q.WriteStats(in); err != nil {
		t.Fatalf("WriteStats: %v", err)
	}
	if err := q.Enqueue([]Item{qItem(11, "<a@x>", "alt.a"), qItem(11, "<b@x>", "alt.b"),
		qItem(12, "<c@x>", "alt.c")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	stats, err := q.ReadStats()
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	got, ok := stats[11]
	if !ok {
		t.Fatal("no stats for peer 11")
	}
	want := in
	want.QueueDepth = 2
	// UpdatedAt comes back in the local zone (stored as a unix second), so it is compared
	// with Equal and the rest field by field.
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("ReadStats()[11].UpdatedAt = %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
	got.UpdatedAt, want.UpdatedAt = time.Time{}, time.Time{}
	if got != want {
		t.Errorf("ReadStats()[11] = %+v, want %+v", got, want)
	}
	// A peer with a backlog but no counters yet is reported with its depth, so the admin
	// page shows the queue before the sender has written anything.
	only, ok := stats[12]
	if !ok || only.QueueDepth != 1 || only.Offered != 0 {
		t.Errorf("ReadStats()[12] = %+v, %v; want a zero-counter entry with QueueDepth 1", only, ok)
	}
	// A second write replaces the counters.
	in.Offered, in.LastError = 99, ""
	if err := q.WriteStats(in); err != nil {
		t.Fatalf("WriteStats again: %v", err)
	}
	stats, err = q.ReadStats()
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if stats[11].Offered != 99 || stats[11].LastError != "" {
		t.Errorf("after the second write: %+v, want Offered 99 and no last error", stats[11])
	}
	if err := q.WriteStats(Stats{}); err == nil {
		t.Error("WriteStats without a peer id returned no error")
	}
	// UpdatedAt zero takes the queue's clock.
	if err := q.WriteStats(Stats{PeerID: 13, Offered: 1}); err != nil {
		t.Fatalf("WriteStats: %v", err)
	}
	stats, err = q.ReadStats()
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if !stats[13].UpdatedAt.Equal(qEpoch) {
		t.Errorf("UpdatedAt = %v, want the queue clock %v", stats[13].UpdatedAt, qEpoch)
	}
}

func TestQueuePruneGivesUp(t *testing.T) {
	q, now := qOpen(t)
	if err := q.Enqueue([]Item{qItem(1, "<old@x>", "alt.a"), qItem(2, "<old2@x>", "alt.a")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	*now = qEpoch.Add(DefaultGiveUpAge)
	if err := q.Enqueue([]Item{qItem(1, "<fresh@x>", "alt.a")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Nothing is old enough one second before the give-up age has passed.
	dropped, err := q.Prune(qEpoch.Add(DefaultGiveUpAge - time.Second))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("Prune dropped %d item(s) before the give-up age, want 0", dropped)
	}
	dropped, err = q.Prune(qEpoch.Add(DefaultGiveUpAge))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if dropped != 2 {
		t.Errorf("Prune dropped %d item(s), want 2 (one per peer)", dropped)
	}
	if d, err := q.Depth(1); err != nil || d != 1 {
		t.Errorf("Depth(1) after the prune = %d, %v; want 1 (the fresh item stays)", d, err)
	}
	if d, err := q.Depth(2); err != nil || d != 0 {
		t.Errorf("Depth(2) after the prune = %d, %v; want 0", d, err)
	}
	// giveUp 0 keeps everything for ever.
	q.giveUp = 0
	if dropped, err := q.Prune(qEpoch.Add(10 * DefaultGiveUpAge)); err != nil || dropped != 0 {
		t.Errorf("Prune with giveUp 0 = %d, %v; want 0, nil", dropped, err)
	}
}

func TestQueueClosedMethodsReturnAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed-queue.sq3")
	q, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := q.Enqueue([]Item{qItem(1, "<a@x>", "alt.a")}); err != ErrQueueClosed {
		t.Errorf("Enqueue on a closed queue = %v, want ErrQueueClosed", err)
	}
	if _, err := q.Claim(1, 1, qEpoch, time.Minute); err != ErrQueueClosed {
		t.Errorf("Claim on a closed queue = %v, want ErrQueueClosed", err)
	}
	if err := q.Done(1, []string{"<a@x>"}); err != ErrQueueClosed {
		t.Errorf("Done on a closed queue = %v, want ErrQueueClosed", err)
	}
	if err := q.Defer(1, "<a@x>", "x", qEpoch); err != ErrQueueClosed {
		t.Errorf("Defer on a closed queue = %v, want ErrQueueClosed", err)
	}
	if _, err := q.Depth(1); err != ErrQueueClosed {
		t.Errorf("Depth on a closed queue = %v, want ErrQueueClosed", err)
	}
	if err := q.DropPeer(1); err != ErrQueueClosed {
		t.Errorf("DropPeer on a closed queue = %v, want ErrQueueClosed", err)
	}
	if err := q.WriteStats(Stats{PeerID: 1}); err != ErrQueueClosed {
		t.Errorf("WriteStats on a closed queue = %v, want ErrQueueClosed", err)
	}
	if _, err := q.ReadStats(); err != ErrQueueClosed {
		t.Errorf("ReadStats on a closed queue = %v, want ErrQueueClosed", err)
	}
	if _, err := q.Prune(qEpoch); err != ErrQueueClosed {
		t.Errorf("Prune on a closed queue = %v, want ErrQueueClosed", err)
	}
}

func TestQueueChunkedIDLists(t *testing.T) {
	q, _ := qOpen(t)
	const n = queueIDChunk*2 + 7
	items := make([]Item, 0, n)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := qChunkID(i)
		items = append(items, qItem(5, id, "alt.chunk"))
		ids = append(ids, id)
	}
	if err := q.Enqueue(items); err != nil {
		t.Fatalf("Enqueue %d items: %v", n, err)
	}
	// Claim leases every row with an IN list spread over several chunks.
	claimed, err := q.Claim(5, n, qEpoch, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != n {
		t.Fatalf("Claim returned %d items, want %d", len(claimed), n)
	}
	if again, err := q.Claim(5, n, qEpoch, time.Minute); err != nil || len(again) != 0 {
		t.Errorf("the second Claim returned %d items, want 0: a chunk was not leased", len(again))
	}
	if err := q.Done(5, ids); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if d, err := q.Depth(5); err != nil || d != 0 {
		t.Errorf("Depth after Done of every id = %d, %v; want 0, nil", d, err)
	}
}

func qChunkID(i int) string {
	return fmt.Sprintf("<chunk-%d@x>", i)
}

func TestQueuePlaceholdersAndHelpers(t *testing.T) {
	if got := queuePlaceholders(0); got != "" {
		t.Errorf("queuePlaceholders(0) = %q, want empty", got)
	}
	if got := queuePlaceholders(1); got != "?" {
		t.Errorf("queuePlaceholders(1) = %q, want ?", got)
	}
	if got := queuePlaceholders(3); got != "?, ?, ?" {
		t.Errorf("queuePlaceholders(3) = %q", got)
	}
	if got := queueNonEmpty([]string{"a", "", "a", "b"}); !qEqual(got, []string{"a", "b"}) {
		t.Errorf("queueNonEmpty = %v, want [a b]", got)
	}
	long := make([]byte, queueErrorMax+50)
	for i := range long {
		long[i] = 'x'
	}
	if got := queueTrimError(string(long)); len(got) != queueErrorMax {
		t.Errorf("queueTrimError kept %d bytes, want %d", len(got), queueErrorMax)
	}
}

func TestQueuePathAndDSN(t *testing.T) {
	if got := QueuePath("/data/root"); got != filepath.Join("/data/root", "cfg", DefaultQueueFile) {
		t.Errorf("QueuePath = %q", got)
	}
	// A '?' in the path must stay part of the path, not start the parameters.
	dsn := queueDSN("/tmp/odd?name/feed-queue.sq3")
	if !filepath.IsAbs("/tmp/odd?name") {
		t.Skip("path semantics differ on this platform")
	}
	if want := "file:/tmp/odd%3Fname/feed-queue.sq3?"; len(dsn) < len(want) || dsn[:len(want)] != want {
		t.Errorf("queueDSN = %q, want it to start with %q", dsn, want)
	}
}
