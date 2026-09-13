package history

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// histTestEnable sets ENABLE_HISTORY for the test and restores it afterwards
func histTestEnable(t *testing.T, enabled bool) {
	t.Helper()
	old := ENABLE_HISTORY
	ENABLE_HISTORY = enabled
	t.Cleanup(func() { ENABLE_HISTORY = old })
}

func histTestConfig(dir string) *HistoryConfig {
	cfg := DefaultConfig()
	cfg.HistoryDir = dir
	cfg.BatchTimeout = 50
	cfg.BatchSize = 100
	cfg.MaxConnections = 4
	return cfg
}

// histTestNew creates an enabled instance in a temp dir; closed via t.Cleanup
func histTestNew(t *testing.T, wg *sync.WaitGroup) (*History, string) {
	t.Helper()
	histTestEnable(t, true)
	dir := filepath.Join(t.TempDir(), "history")
	h, err := NewHistory(histTestConfig(dir), wg)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h, dir
}

// histTestWaitIdle waits until all queued ops are committed
func histTestWaitIdle(t *testing.T, h *History) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !h.CheckNoMoreWorkInHistory() {
		if time.Now().After(deadline) {
			t.Fatalf("history still has pending work: %d", h.pending.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func histTestGroups(t *testing.T, h *History, messageID string) []int64 {
	t.Helper()
	ids, err := h.LookupGroups(messageID)
	if err != nil {
		t.Fatalf("LookupGroups(%s): %v", messageID, err)
	}
	return ids
}

// histTestRawRow reads a row directly from the files with a separate connection
func histTestRawRow(t *testing.T, dir, messageID string) (newsgroups sql.NullString, found bool) {
	t.Helper()
	hash := ComputeMessageIDHash(messageID)
	path := filepath.Join(dir, fmt.Sprintf("hashdb_%s.sqlite3", hash[0:1]))
	db, err := sql.Open(historyDriverName, path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	err = db.QueryRow("SELECT newsgroups FROM _"+hash[1:3]+" WHERE message_id = ?", messageID).Scan(&newsgroups)
	if err == sql.ErrNoRows {
		return newsgroups, false
	}
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	return newsgroups, true
}

func TestHistoryCore(t *testing.T) {
	h, dir := histTestNew(t, nil)

	if !h.Enabled() {
		t.Fatal("expected enabled instance")
	}
	for i := 0; i < 16; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("hashdb_%x.sqlite3", i))); err != nil {
			t.Fatalf("db file %x missing: %v", i, err)
		}
	}

	t.Run("DuplicateInOneFlushDoesNotWedgeWriter", func(t *testing.T) {
		m := "<dup-same-flush@test>"
		h.AddArticle(m, 7)
		h.AddArticle(m, 7)
		h.AddArticle(m, 7)
		histTestWaitIdle(t, h)
		if got := histTestGroups(t, h, m); !reflect.DeepEqual(got, []int64{7}) {
			t.Fatalf("groups = %v, want [7]", got)
		}
		// next flush: same id again plus unrelated ids must still commit
		h.AddArticle(m, 7)
		h.AddArticle("<after-dup-1@test>", 1)
		h.AddArticle("<after-dup-2@test>", 2)
		histTestWaitIdle(t, h)
		if got := histTestGroups(t, h, m); !reflect.DeepEqual(got, []int64{7}) {
			t.Fatalf("groups = %v, want [7]", got)
		}
		for _, id := range []string{"<after-dup-1@test>", "<after-dup-2@test>"} {
			if ok, err := h.Exists(id); err != nil || !ok {
				t.Fatalf("Exists(%s) = %v, %v", id, ok, err)
			}
		}
	})

	t.Run("MergeGroupsAndRemove", func(t *testing.T) {
		m := "<merge@test>"
		h.AddArticle(m, 1)
		h.AddArticle(m, 2)
		h.AddArticle(m, 1)
		histTestWaitIdle(t, h)
		if got := histTestGroups(t, h, m); !reflect.DeepEqual(got, []int64{1, 2}) {
			t.Fatalf("groups = %v, want [1 2]", got)
		}

		h.RemoveArticle(m, 1)
		histTestWaitIdle(t, h)
		if got := histTestGroups(t, h, m); !reflect.DeepEqual(got, []int64{2}) {
			t.Fatalf("after remove 1: groups = %v, want [2]", got)
		}

		h.RemoveArticle(m, 2)
		histTestWaitIdle(t, h)
		if got := histTestGroups(t, h, m); got != nil {
			t.Fatalf("after remove 2: groups = %v, want nil", got)
		}
		if ok, err := h.Exists(m); err != nil || ok {
			t.Fatalf("Exists after remove = %v, %v", ok, err)
		}

		// removing something that is not there is a no-op
		h.RemoveArticle("<never-added@test>", 3)
		histTestWaitIdle(t, h)
	})

	t.Run("AddThenRemoveInSameFlushKeepsOrder", func(t *testing.T) {
		m := "<order@test>"
		h.AddArticle(m, 10)
		h.AddArticle(m, 11)
		h.RemoveArticle(m, 10)
		histTestWaitIdle(t, h)
		if got := histTestGroups(t, h, m); !reflect.DeepEqual(got, []int64{11}) {
			t.Fatalf("groups = %v, want [11]", got)
		}
	})

	t.Run("LegacyEmptyRowMerges", func(t *testing.T) {
		for _, legacy := range []struct {
			id  string
			val any
		}{{"<legacy-empty@test>", ""}, {"<legacy-null@test>", nil}} {
			dbIndex, tableName, err := h.routeHash(legacy.id)
			if err != nil {
				t.Fatal(err)
			}
			db, _ := h.db.GetShardedDB(dbIndex, true)
			if _, err := db.Exec("INSERT INTO "+tableName+"(message_id,newsgroups) VALUES(?,?)", legacy.id, legacy.val); err != nil {
				t.Fatalf("insert legacy row: %v", err)
			}
			got := histTestGroups(t, h, legacy.id)
			if got == nil || len(got) != 0 {
				t.Fatalf("legacy row lookup = %#v, want empty non-nil", got)
			}
			h.AddArticle(legacy.id, 5)
			histTestWaitIdle(t, h)
			if got := histTestGroups(t, h, legacy.id); !reflect.DeepEqual(got, []int64{5}) {
				t.Fatalf("legacy %v merged groups = %v, want [5]", legacy.val, got)
			}
			if raw, _ := histTestRawRow(t, dir, legacy.id); raw.String != "5" {
				t.Fatalf("raw newsgroups = %q, want \"5\"", raw.String)
			}
		}
	})

	t.Run("InvalidOpsIgnored", func(t *testing.T) {
		h.AddArticle("", 1)
		h.AddArticle("<invalid-group@test>", 0)
		h.RemoveArticle("<invalid-group@test>", -1)
		if !h.CheckNoMoreWorkInHistory() {
			t.Fatal("invalid ops must not be counted as pending")
		}
		if ok, _ := h.Exists("<invalid-group@test>"); ok {
			t.Fatal("invalid op was written")
		}
	})

	t.Run("ConcurrentOverlappingAdds", func(t *testing.T) {
		const workers = 16
		const ids = 300
		const groups = 3
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < ids; i++ {
					// every worker adds every id to groups 1..3, in different orders
					g := int64((i+w)%groups) + 1
					h.AddArticle(fmt.Sprintf("<concurrent-%d@test>", i), g)
				}
			}(w)
		}
		wg.Wait()
		histTestWaitIdle(t, h)
		for i := 0; i < ids; i++ {
			got := histTestGroups(t, h, fmt.Sprintf("<concurrent-%d@test>", i))
			if len(got) != groups {
				t.Fatalf("id %d groups = %v, want %d distinct groups", i, got, groups)
			}
			seen := map[int64]bool{}
			for _, g := range got {
				if seen[g] || g < 1 || g > groups {
					t.Fatalf("id %d groups = %v: duplicate or out of range", i, got)
				}
				seen[g] = true
			}
		}
	})

	t.Run("Stats", func(t *testing.T) {
		st := h.GetStats()
		if st.TotalAdds == 0 || st.TotalCommitted == 0 || st.Flushes == 0 || st.Pending != 0 || st.Errors != 0 {
			t.Fatalf("unexpected stats: %+v", st)
		}
	})
}

func TestHistoryCloseFlushesAndWaitGroup(t *testing.T) {
	var wg sync.WaitGroup
	histTestEnable(t, true)
	dir := filepath.Join(t.TempDir(), "history")
	cfg := histTestConfig(dir)
	cfg.BatchTimeout = 60000 // only Close may trigger the final flush
	cfg.BatchSize = 100000
	h, err := NewHistory(cfg, &wg)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}

	// later changes to ENABLE_HISTORY must not affect the running instance
	ENABLE_HISTORY = false
	if !h.Enabled() {
		t.Fatal("instance must stay enabled")
	}

	var wgDone atomic.Bool
	go func() {
		wg.Wait()
		wgDone.Store(true)
	}()

	const n = 500
	for i := 0; i < n; i++ {
		h.AddArticle(fmt.Sprintf("<close-%d@test>", i), int64(i%5)+1)
	}
	time.Sleep(100 * time.Millisecond)
	if wgDone.Load() {
		t.Fatal("mainWG returned before Close")
	}

	// a checker that is busy for a moment must delay the shutdown
	checker := &histTestChecker{}
	checker.busyUntil.Store(time.Now().Add(150 * time.Millisecond).UnixNano())
	h.SetDatabaseWorkChecker(checker)

	start := time.Now()
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("Close did not wait for the database work checker (%v)", time.Since(start))
	}
	if !h.CheckNoMoreWorkInHistory() {
		t.Fatal("pending work after Close")
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mainWG.Wait did not return after Close")
	}

	// all ops must be in the files
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("<close-%d@test>", i)
		raw, found := histTestRawRow(t, dir, id)
		if !found || raw.String != fmt.Sprint(i%5+1) {
			t.Fatalf("%s: found=%v newsgroups=%q", id, found, raw.String)
		}
	}
	// WAL truncated by the final checkpoint
	if fi, err := os.Stat(filepath.Join(dir, "hashdb_0.sqlite3-wal")); err == nil && fi.Size() != 0 {
		t.Logf("note: WAL size after close = %d", fi.Size())
	}

	// second Close and calls after Close must not panic
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	h.AddArticle("<after-close@test>", 1)
	h.RemoveArticle("<close-1@test>", 2)
	if !h.CheckNoMoreWorkInHistory() {
		t.Fatal("ops after Close must not be pending")
	}
	if ok, err := h.Exists("<close-1@test>"); ok || err == nil {
		t.Fatalf("Exists after Close = %v, %v (want false, error)", ok, err)
	}
	if ids, err := h.LookupGroups("<close-1@test>"); ids != nil || err == nil {
		t.Fatalf("LookupGroups after Close = %v, %v", ids, err)
	}
	h.AddArticle("<after-close@test>", 1) // must not panic or block
	_ = h.GetStats()
}

type histTestChecker struct {
	busyUntil atomic.Int64
	calls     atomic.Int64
}

func (c *histTestChecker) CheckNoMoreWorkInMaps() bool {
	c.calls.Add(1)
	return time.Now().UnixNano() >= c.busyUntil.Load()
}

func TestHistoryReopenKeepsData(t *testing.T) {
	histTestEnable(t, true)
	dir := filepath.Join(t.TempDir(), "history")
	h, err := NewHistory(histTestConfig(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	h.AddArticle("<reopen@test>", 42)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	h2, err := NewHistory(histTestConfig(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if got := histTestGroups(t, h2, "<reopen@test>"); !reflect.DeepEqual(got, []int64{42}) {
		t.Fatalf("groups after reopen = %v", got)
	}
	var count int
	db, _ := h2.db.GetShardedDB(0, false)
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name GLOB '_[0-9a-f][0-9a-f]'`).Scan(&count); err != nil || count != 256 {
		t.Fatalf("table count = %d, %v", count, err)
	}
	// every pooled connection has the per-connection settings
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			var timeout, tempStore int
			var mode string
			if err := conn.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 30000 {
				t.Errorf("busy_timeout = %d, %v", timeout, err)
			}
			if err := conn.QueryRowContext(t.Context(), "PRAGMA temp_store").Scan(&tempStore); err != nil || tempStore != 2 {
				t.Errorf("temp_store = %d, %v", tempStore, err)
			}
			if err := conn.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
				t.Errorf("journal_mode = %q, %v", mode, err)
			}
		}()
	}
	wg.Wait()
}

func TestHistoryDisabled(t *testing.T) {
	histTestEnable(t, false)
	dir := filepath.Join(t.TempDir(), "history-disabled")
	var wg sync.WaitGroup
	h, err := NewHistory(histTestConfig(dir), &wg)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	// later changes must not enable a running instance
	ENABLE_HISTORY = true

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled history touched mainWG")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("disabled history created directory: %v", err)
	}
	if h.Enabled() {
		t.Fatal("expected disabled instance")
	}

	h.AddArticle("<m@test>", 1)
	h.RemoveArticle("<m@test>", 1)
	if !h.CheckNoMoreWorkInHistory() {
		t.Fatal("disabled history has pending work")
	}
	if ok, err := h.Exists("<m@test>"); ok || err != nil {
		t.Fatalf("Exists = %v, %v", ok, err)
	}
	if ids, err := h.LookupGroups("<m@test>"); ids != nil || err != nil {
		t.Fatalf("LookupGroups = %v, %v", ids, err)
	}

	h.AddArticle("<m@test>", 1)
	h.RemoveArticle("<m@test>", 1)
	if !h.CheckNoMoreWorkInHistory() {
		t.Fatal("disabled history must never report pending work")
	}
	h.SetDatabaseWorkChecker(&histTestChecker{})
	_ = h.GetStats()
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("disabled history created directory: %v", err)
	}
}

func TestHistoryNilReceiver(t *testing.T) {
	var h *History
	h.AddArticle("<x@test>", 1)
	h.RemoveArticle("<x@test>", 1)
	h.SetDatabaseWorkChecker(nil)
	if h.Enabled() || !h.CheckNoMoreWorkInHistory() {
		t.Fatal("nil history must be disabled and idle")
	}
	if ok, err := h.Exists("<x@test>"); ok || err != nil {
		t.Fatal("nil Exists")
	}
	if ids, err := h.LookupGroups("<x@test>"); ids != nil || err != nil {
		t.Fatal("nil LookupGroups")
	}
	h.AddArticle("<x@test>", 1)
	h.RemoveArticle("<x@test>", 1)
	_ = h.GetStats()
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryParseGroupIDs(t *testing.T) {
	for in, want := range map[string][]int64{
		"":          {},
		"1":         {1},
		"1,2":       {1, 2},
		",1,,x,-3,": {1},
		" 4 , 5":    {4, 5},
	} {
		got := parseGroupIDs(in)
		if got == nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseGroupIDs(%q) = %#v, want %#v", in, got, want)
		}
	}
}
