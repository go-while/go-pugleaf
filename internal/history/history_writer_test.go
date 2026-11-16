package history

import (
	"database/sql"
	"fmt"
	"testing"
)

// histTestWriterSnapshot copies the cached statements of every dbWriter. Call it only while the
// writer is idle (after histTestWaitIdle): the flush goroutines own h.writers.
func histTestWriterSnapshot(h *History) (writers [historyNumDBs]*dbWriter, stmts map[string]*sql.Stmt) {
	stmts = make(map[string]*sql.Stmt)
	for i, w := range h.writers {
		writers[i] = w
		if w == nil {
			continue
		}
		for k, s := range w.stmts {
			stmts[fmt.Sprintf("%x/%s", i, k)] = s
		}
	}
	return writers, stmts
}

// The write connection and its prepared statements survive the commit: a second flush that
// touches the same tables prepares nothing new and reuses the same statements.
func TestHistoryWriterKeepsStatementsAcrossFlushes(t *testing.T) {
	h, _ := histTestNew(t, nil)

	const n = 300
	for i := 0; i < n; i++ {
		h.AddArticleAt(fmt.Sprintf("<w%d@test>", i), 7, int64(i+1))
	}
	histTestWaitIdle(t, h)
	writers1, stmts1 := histTestWriterSnapshot(h)
	if len(stmts1) == 0 {
		t.Fatal("no cached statements after the first flush")
	}

	// same message-IDs (same tables), now in a second group: UPSERT path again
	for i := 0; i < n; i++ {
		h.AddArticleAt(fmt.Sprintf("<w%d@test>", i), 8, int64(i+1))
	}
	histTestWaitIdle(t, h)
	writers2, stmts2 := histTestWriterSnapshot(h)

	for i := range writers1 {
		if writers1[i] != nil && writers1[i] != writers2[i] {
			t.Fatalf("db %x: write connection replaced between flushes", i)
		}
	}
	if len(stmts2) != len(stmts1) {
		t.Fatalf("second flush on the same tables prepared new statements: %d -> %d", len(stmts1), len(stmts2))
	}
	for k, s := range stmts1 {
		if stmts2[k] != s {
			t.Fatalf("statement %s was prepared again", k)
		}
	}

	for i := 0; i < n; i++ {
		mid := fmt.Sprintf("<w%d@test>", i)
		got := histTestGroups(t, h, mid)
		if len(got) != 2 || got[0] != 7 || got[1] != 8 {
			t.Fatalf("%s: groups %v, want [7 8]", mid, got)
		}
	}
}

// Removes, rejects and renumbers run on the cached statements too, mixed with adds in one flush.
func TestHistoryWriterMixedOpsWithCachedStatements(t *testing.T) {
	h, _ := histTestNew(t, nil)

	h.AddArticleAt("<m1@test>", 1, 10)
	h.AddArticleAt("<m1@test>", 2, 20)
	h.AddArticleAt("<m2@test>", 1, 11)
	h.AddReject("<r1@test>")
	histTestWaitIdle(t, h)

	h.RemoveArticleAt("<m1@test>", 1, 10)
	h.AddArticleAt("<m2@test>", 1, 12) // renumber
	h.RemoveArticle("<m2@test>", 3)    // not there: no-op
	histTestWaitIdle(t, h)

	if got := histTestGroups(t, h, "<m1@test>"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("<m1@test>: groups %v, want [2]", got)
	}
	locs, err := h.LookupLocations("<m2@test>")
	if err != nil {
		t.Fatalf("LookupLocations: %v", err)
	}
	if len(locs) != 1 || locs[0].GroupID != 1 || locs[0].ArticleNum != 12 {
		t.Fatalf("<m2@test>: locations %+v, want group 1 article 12", locs)
	}

	h.RemoveArticleAt("<m2@test>", 1, 12)
	histTestWaitIdle(t, h)
	if ok, err := h.Exists("<m2@test>"); err != nil || ok {
		t.Fatalf("<m2@test> after its last remove: exists=%v err=%v, want gone", ok, err)
	}
}

// MaxConnections 1 is raised to 2: the writer keeps one connection per file pinned, and
// lookups still get a connection besides it.
func TestHistoryMaxConnectionsOneRaisedToTwo(t *testing.T) {
	histTestEnable(t, true)
	cfg := histTestConfig(t.TempDir() + "/history")
	cfg.MaxConnections = 1
	h, err := NewHistory(cfg, nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	if cfg.MaxConnections != 2 {
		t.Fatalf("MaxConnections = %d, want 2", cfg.MaxConnections)
	}

	for i := 0; i < 50; i++ {
		h.AddArticle(fmt.Sprintf("<c%d@test>", i), 5)
	}
	histTestWaitIdle(t, h)
	for i := 0; i < 50; i++ {
		if got := histTestGroups(t, h, fmt.Sprintf("<c%d@test>", i)); len(got) != 1 || got[0] != 5 {
			t.Fatalf("<c%d@test>: groups %v, want [5]", i, got)
		}
	}
}

// Close closes the write connections and drops them; a second Close is a no-op.
func TestHistoryCloseReleasesWriters(t *testing.T) {
	h, _ := histTestNew(t, nil)
	h.AddArticle("<close@test>", 1)
	histTestWaitIdle(t, h)
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i, w := range h.writers {
		if w != nil {
			t.Fatalf("db %x: writer still set after Close", i)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
