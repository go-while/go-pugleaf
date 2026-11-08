package main

// `expire-news -trim-history` also prunes the remembered rejects of the history index that are
// older than the HistoryRejectDays setting (plan nntp-peering, slice history-rejects).

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
)

// rejTestCaptureLog redirects the standard logger into a buffer for the test
func rejTestCaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// rejTestHistory opens an enabled history index in a temp dir; closed via t.Cleanup
func rejTestHistory(t *testing.T) *history.History {
	t.Helper()
	old := history.ENABLE_HISTORY
	history.ENABLE_HISTORY = true
	t.Cleanup(func() { history.ENABLE_HISTORY = old })
	cfg := history.DefaultConfig()
	cfg.HistoryDir = t.TempDir()
	cfg.BatchTimeout = 50
	cfg.MaxConnections = 4
	h, err := history.NewHistory(cfg, nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// rejPrunerFake records the calls of pruneExpiredRejects
type rejPrunerFake struct {
	days int
	n    int64
	err  error
	ret  int64
}

func (f *rejPrunerFake) PruneRejects(days int, stop <-chan struct{}) (int64, error) {
	f.days = days
	f.n++
	return f.ret, f.err
}

func TestRejectsPruneWiring(t *testing.T) {
	logs := rejTestCaptureLog(t)

	// 0 = keep rejects as long as history: the index is not touched
	f := &rejPrunerFake{}
	if ok := pruneExpiredRejects(f, 0); !ok {
		t.Errorf("pruneExpiredRejects(0) = false, want true")
	}
	if f.n != 0 {
		t.Errorf("PruneRejects called %d times with 0 days, want 0", f.n)
	}
	if !strings.Contains(logs.String(), "remembered rejects are kept") {
		t.Errorf("no log line about the kept rejects:\n%s", logs.String())
	}

	f = &rejPrunerFake{ret: 3}
	if ok := pruneExpiredRejects(f, 30); !ok {
		t.Errorf("pruneExpiredRejects(30) = false, want true")
	}
	if f.days != 30 || f.n != 1 {
		t.Errorf("PruneRejects(days=%d) called %d times, want days 30, once", f.days, f.n)
	}
	if !strings.Contains(logs.String(), "pruned 3 remembered rejects older than 30 days") {
		t.Errorf("no log line about the pruned rejects:\n%s", logs.String())
	}

	f = &rejPrunerFake{err: errors.New("boom")}
	if ok := pruneExpiredRejects(f, 30); ok {
		t.Errorf("pruneExpiredRejects with a failing prune = true, want false")
	}
	if !strings.Contains(logs.String(), "Error pruning remembered rejects") {
		t.Errorf("no log line about the failed prune:\n%s", logs.String())
	}
}

// TestRejectsPruneRemovesExpired runs the real prune over a real index: an expired reject is
// gone, a fresh one and an accepted article stay.
func TestRejectsPruneRemovesExpired(t *testing.T) {
	h := rejTestHistory(t)
	const (
		expired = "<expire-reject-old@test.invalid>"
		fresh   = "<expire-reject-fresh@test.invalid>"
		kept    = "<expire-reject-stored@test.invalid>"
	)
	h.AddReject(expired)
	h.AddReject(fresh)
	h.AddArticleAt(kept, 7, 3)
	toolsTestWaitPending(t, h)

	// every reject of this run carries today's day: a cutoff of tomorrow expires them, one of
	// today does not (no sleeps, no wall-clock asserts)
	today := history.UnixDay(time.Now())
	if n, err := h.PruneRejectsBefore(today, nil); err != nil || n != 0 {
		t.Fatalf("PruneRejectsBefore(today) = %d, %v; want 0, nil", n, err)
	}
	if ok, err := h.Exists(expired); err != nil || !ok {
		t.Fatalf("Exists(expired) before the prune = %v, %v; want true", ok, err)
	}

	// -trim-history prunes with the HistoryRejectDays setting; PruneRejects(days) is the same
	// walk with the cutoff computed from it
	if ok := pruneExpiredRejects(h, 30); !ok {
		t.Fatalf("pruneExpiredRejects failed")
	}
	if ok, err := h.Exists(expired); err != nil || !ok {
		t.Fatalf("Exists(expired) after a 30 day prune = %v, %v; want true (it is from today)", ok, err)
	}

	n, err := h.PruneRejectsBefore(today+1, nil)
	if err != nil {
		t.Fatalf("PruneRejectsBefore: %v", err)
	}
	if n != 2 {
		t.Fatalf("PruneRejectsBefore deleted %d rows, want 2", n)
	}
	for _, id := range []string{expired, fresh} {
		if ok, err := h.Exists(id); err != nil || ok {
			t.Errorf("Exists(%s) = %v, %v; want false (pruned)", id, ok, err)
		}
	}
	ids, err := h.LookupGroups(kept)
	if err != nil || len(ids) != 1 || ids[0] != 7 {
		t.Errorf("LookupGroups(%s) = %v, %v; want [7] (an accepted article is never pruned)", kept, ids, err)
	}
}
