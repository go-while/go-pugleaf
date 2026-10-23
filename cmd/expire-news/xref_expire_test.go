package main

// Tests of the xref-history slice xref-rebuild-trim: the history trim passes the article number
// of every deleted row, so the exact "groupID:articleNum" token is removed. Until the history
// writer stores numbers (slice xref-history), the real index keeps plain group IDs: the number
// checks against it run only when xrefHistoryStoresNumbers reports it ("needs xref-history writer").

import (
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/go-while/go-pugleaf/internal/history"
)

// xrefRemoveCall is one recorded RemoveArticleAt call
type xrefRemoveCall struct {
	messageID  string
	groupID    int64
	articleNum int64
}

// xrefRecordingRemover implements historyRemover and records the calls
type xrefRecordingRemover struct {
	mu    sync.Mutex
	calls []xrefRemoveCall
}

func (r *xrefRecordingRemover) RemoveArticleAt(messageID string, groupID, articleNum int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, xrefRemoveCall{messageID, groupID, articleNum})
}

// xrefHistoryStoresNumbers reports whether the history writer stores "groupID:articleNum" tokens
// (true once slice xref-history is merged). It adds and removes a probe row.
func xrefHistoryStoresNumbers(t *testing.T, h *history.History) bool {
	t.Helper()
	const probe, probeGroup = "<xref-number-probe@test>", 999999
	h.AddArticleAt(probe, probeGroup, 5)
	toolsTestWaitPending(t, h)
	locs, err := h.LookupLocations(probe)
	if err != nil {
		t.Fatalf("LookupLocations(probe): %v", err)
	}
	ok := len(locs) == 1 && locs[0].ArticleNum == 5
	h.RemoveArticleAt(probe, probeGroup, 0)
	if ok {
		h.RemoveArticleAt(probe, probeGroup, 5)
	}
	toolsTestWaitPending(t, h)
	return ok
}

// TestXrefExpireTrimPassesArticleNumbers: deleteArticles removes each deleted article with its
// own article number; numbers without a row are not passed.
func TestXrefExpireTrimPassesArticleNumbers(t *testing.T) {
	g := toolsTestGroupDB(t)
	rec := &xrefRecordingRemover{}
	if err := deleteArticles(g, []int64{2, 5, 99}, historyTrim{hist: rec, groupID: 7}); err != nil {
		t.Fatalf("deleteArticles: %v", err)
	}
	sort.Slice(rec.calls, func(i, j int) bool { return rec.calls[i].articleNum < rec.calls[j].articleNum })
	want := []xrefRemoveCall{{"<2@test>", 7, 2}, {"<5@test>", 7, 5}}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("RemoveArticleAt calls = %v, want %v", rec.calls, want)
	}

	// no trim: nothing is selected or removed
	if err := deleteArticles(g, []int64{1}, historyTrim{}); err != nil {
		t.Fatalf("deleteArticles without trim: %v", err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("calls after a delete without trim: %v", rec.calls)
	}
}

// TestXrefExpireTrimKeepsOtherGroupToken: against the real index, the deleted article's token
// goes and the crosspost's token of the other group stays (with its number once the writer
// stores numbers).
func TestXrefExpireTrimKeepsOtherGroupToken(t *testing.T) {
	g := toolsTestGroupDB(t)

	history.ENABLE_HISTORY = true
	cfg := history.DefaultConfig()
	cfg.HistoryDir = t.TempDir()
	cfg.BatchTimeout = 50
	h, err := history.NewHistory(cfg, nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	defer h.Close()
	numbers := xrefHistoryStoresNumbers(t, h)
	if !numbers {
		t.Logf("history writer stores no article numbers yet: number checks skipped (needs xref-history writer)")
	}

	const groupID, otherGroupID = 7, 9
	h.AddArticleAt("<3@test>", groupID, 3)
	h.AddArticleAt("<3@test>", otherGroupID, 11)
	h.AddArticleAt("<4@test>", groupID, 4)
	toolsTestWaitPending(t, h)

	if err := deleteArticles(g, []int64{3}, historyTrim{hist: h, groupID: groupID}); err != nil {
		t.Fatalf("deleteArticles: %v", err)
	}
	toolsTestWaitPending(t, h)

	locs, err := h.LookupLocations("<3@test>")
	if err != nil || len(locs) != 1 || locs[0].GroupID != otherGroupID {
		t.Fatalf("<3@test> after trim = %v, %v; want only group %d", locs, err, otherGroupID)
	}
	if numbers && locs[0].ArticleNum != 11 {
		t.Fatalf("<3@test> after trim = %v, want %d:11", locs, otherGroupID)
	}
	locs, err = h.LookupLocations("<4@test>")
	if err != nil || len(locs) != 1 || locs[0].GroupID != groupID {
		t.Fatalf("<4@test> (not deleted) = %v, %v; want group %d", locs, err, groupID)
	}
	if numbers && locs[0].ArticleNum != 4 {
		t.Fatalf("<4@test> = %v, want %d:4", locs, groupID)
	}
}
