package database

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// feed_hook_test.go covers the commit-point hook of the outgoing transit feed (plan
// nntp-peering, slice feed-queue): it fires once per ARTICLE, not once per group copy, only
// for articles the NNTP feed or an NNTP POST brought in (models.Article.FeedOut), and the
// snapshot it hands over is taken before the batch writer clears the article.
//
// It uses the batch harness of fu_batch_test.go (fuBatchIsolatedDB, adfBatchQueue,
// fuBatchRun), so every wait is that harness's hang guard (testWait, PUGLEAF_TEST_DEADLINE).

// fhSink collects the snapshots the hook is called with. The batch writer calls the hook
// from its own goroutine, so the slice is guarded.
type fhSink struct {
	mu    sync.Mutex
	calls []FeedArticle
}

func (s *fhSink) hook(a FeedArticle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, a)
}

func (s *fhSink) snapshots() []FeedArticle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]FeedArticle(nil), s.calls...)
}

// fhInstall installs the sink as the process-wide feed hook for one test. SetFeedHook is a
// package global, so no test using it may run with t.Parallel.
func fhInstall(t *testing.T) *fhSink {
	t.Helper()
	s := &fhSink{}
	SetFeedHook(s.hook)
	t.Cleanup(func() { SetFeedHook(nil) })
	return s
}

// fhArticle builds an article as the non-bulk intake leaves it for the batch writer: a
// stored header block with the Newsgroups header, a Path with our identity, and the
// NewsgroupsPtr of every group it is captured for.
func fhArticle(messageID string, groups []*string, feedOut bool) *models.Article {
	now := time.Now().UTC()
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, *g)
	}
	head := []string{
		"Path: ours.invalid!peer.invalid!not-for-mail",
		"From: fh <fh@test.invalid>",
		"Newsgroups: " + strings.Join(names, ","),
		"Subject: fh-hook",
		"Message-ID: " + messageID,
	}
	return &models.Article{
		MessageID:     messageID,
		Subject:       "fh-hook",
		FromHeader:    "fh <fh@test.invalid>",
		DateSent:      now,
		DateString:    now.Format(time.RFC1123Z),
		Bytes:         4242,
		Lines:         7,
		Path:          "ours.invalid!peer.invalid!not-for-mail",
		HeadersJSON:   strings.Join(head, "\n"),
		ArticleNums:   make(map[*string]int64),
		NewsgroupsPtr: groups,
		IsThrRoot:     true,
		FeedOut:       feedOut,
	}
}

func fhMsgID(what string) string {
	return fmt.Sprintf("<fh-%s-%d@test.invalid>", what, w0Seq.Add(1))
}

// TestFeedHookFiresOncePerCrosspostedArticle: an article stored in two groups is handed to
// the hook exactly once, by the batch of its last group copy, with the Newsgroups header it
// carries and the group it can be read from.
func TestFeedHookFiresOncePerCrosspostedArticle(t *testing.T) {
	logs := &fuBatchLogs{}
	db := fuBatchIsolatedDB(t)
	sink := fhInstall(t)

	one := w0Name("fhhook.one")
	two := w0Name("fhhook.two")
	task1 := db.Batch.GetOrCreateTasksMapKey(one)
	task2 := db.Batch.GetOrCreateTasksMapKey(two)
	msgID := fhMsgID("cross")
	art := fhArticle(msgID, []*string{task1.Newsgroup, task2.Newsgroup}, true)

	adfBatchQueue(db.Batch, task1, art)
	fuBatchRun(t, db, task1, adfBatchWait, logs)
	if got := sink.snapshots(); len(got) != 0 {
		t.Fatalf("the hook fired %d time(s) after the first of two group copies, want 0: it must fire once per article, not per copy", len(got))
	}

	adfBatchQueue(db.Batch, task2, art)
	fuBatchRun(t, db, task2, adfBatchWait, logs)
	got := sink.snapshots()
	if len(got) != 1 {
		t.Fatalf("the hook fired %d time(s) for one crossposted article, want 1", len(got))
	}
	snap := got[0]
	if snap.MessageID != msgID {
		t.Errorf("snapshot MessageID = %q, want %q", snap.MessageID, msgID)
	}
	if snap.Newsgroup != two {
		t.Errorf("snapshot Newsgroup = %q, want %q (the group of the last copy)", snap.Newsgroup, two)
	}
	want := []string{one, two}
	if len(snap.Newsgroups) != 2 || snap.Newsgroups[0] != want[0] || snap.Newsgroups[1] != want[1] {
		t.Errorf("snapshot Newsgroups = %v, want %v (the article's Newsgroups header)", snap.Newsgroups, want)
	}
	if snap.Path != "ours.invalid!peer.invalid!not-for-mail" {
		t.Errorf("snapshot Path = %q, want the stored Path header", snap.Path)
	}
	if snap.Bytes != 4242 || snap.Lines != 7 {
		t.Errorf("snapshot Bytes/Lines = %d/%d, want 4242/7", snap.Bytes, snap.Lines)
	}
}

// TestFeedHookSkipsBulkArticles: a fetched or imported article (bulk: FeedOut false) is
// never handed to the hook, so an archive import does not flood the peers.
func TestFeedHookSkipsBulkArticles(t *testing.T) {
	logs := &fuBatchLogs{}
	db := fuBatchIsolatedDB(t)
	sink := fhInstall(t)

	name := w0Name("fhhook.bulk")
	task := db.Batch.GetOrCreateTasksMapKey(name)
	// The ids are read before the run: once the batch writer has the article, it clears
	// the struct (that is exactly what the snapshot is taken ahead of).
	bulkID, feedID := fhMsgID("bulk"), fhMsgID("feed")
	adfBatchQueue(db.Batch, task,
		fhArticle(bulkID, []*string{task.Newsgroup}, false),
		fhArticle(feedID, []*string{task.Newsgroup}, true))
	fuBatchRun(t, db, task, adfBatchWait, logs)

	got := sink.snapshots()
	if len(got) != 1 {
		t.Fatalf("the hook fired %d time(s) for one bulk and one feed article, want 1", len(got))
	}
	if got[0].MessageID == bulkID {
		t.Errorf("the hook got the bulk article %q: bulk articles are never queued for a peer", got[0].MessageID)
	}
	if got[0].MessageID != feedID {
		t.Errorf("the hook got %q, want the feed article %q", got[0].MessageID, feedID)
	}
}

// TestFeedHookOffWithoutAHook: with no hook installed the batch writer still commits (the
// hot path is one atomic load), and a hook installed afterwards sees nothing of that batch.
func TestFeedHookOffWithoutAHook(t *testing.T) {
	logs := &fuBatchLogs{}
	db := fuBatchIsolatedDB(t)
	SetFeedHook(nil)

	name := w0Name("fhhook.off")
	task := db.Batch.GetOrCreateTasksMapKey(name)
	art := fhArticle(fhMsgID("off"), []*string{task.Newsgroup}, true)
	adfBatchQueue(db.Batch, task, art)
	fuBatchRun(t, db, task, adfBatchWait, logs)

	sink := fhInstall(t)
	if got := sink.snapshots(); len(got) != 0 {
		t.Fatalf("a hook installed after the batch saw %d snapshot(s), want 0", len(got))
	}
	if loadFeedHook() == nil {
		t.Fatal("loadFeedHook() is nil right after SetFeedHook with a function")
	}
	SetFeedHook(nil)
	if loadFeedHook() != nil {
		t.Fatal("loadFeedHook() is not nil after SetFeedHook(nil)")
	}
}

// TestFeedSnapshotWithoutAHeaderBlock: an article whose stored header block cannot be read
// falls back to the group it is stored in, so the feed never sees an empty newsgroup list.
func TestFeedSnapshotWithoutAHeaderBlock(t *testing.T) {
	art := &models.Article{MessageID: "<fh-nohdr@test.invalid>", Path: "ours.invalid!x", Bytes: 10, Lines: 1}
	snap := feedSnapshot(art, "alt.test.fh")
	if len(snap.Newsgroups) != 1 || snap.Newsgroups[0] != "alt.test.fh" {
		t.Errorf("Newsgroups without a header block = %v, want [alt.test.fh]", snap.Newsgroups)
	}

	art.HeadersJSON = "Newsgroups: alt.test.a, alt.test.b\nSubject: x"
	snap = feedSnapshot(art, "alt.test.a")
	if len(snap.Newsgroups) != 2 || snap.Newsgroups[0] != "alt.test.a" || snap.Newsgroups[1] != "alt.test.b" {
		t.Errorf("Newsgroups from the header block = %v, want [alt.test.a alt.test.b]", snap.Newsgroups)
	}
}
