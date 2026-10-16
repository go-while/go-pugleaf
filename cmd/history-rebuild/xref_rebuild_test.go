package main

// Tests of the xref-history slice xref-rebuild-trim: the rebuild passes the article numbers to
// the history index, -validate-only counts tokens without a number, -trim removes the exact
// token. The databases live below t.TempDir(); ./data is never used.
//
// Until the history writer stores "groupID:articleNum" tokens (slice xref-history), AddArticleAt
// stores plain group IDs. The tests therefore assert on the calls (recordingHistory) and on rows
// written directly into the scratch history files; the checks that need numbers back from the
// real writer run only when historyStoresNumbers reports it ("needs xref-history writer").

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// xrefTestWait is the hang guard of these tests: it only stops a hung test, a passing test never
// waits on it. Override with PUGLEAF_TEST_DEADLINE (a Go duration); default 60 s.
func xrefTestWait() time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// histCall is one recorded AddArticleAt/RemoveArticleAt call
type histCall struct {
	messageID  string
	groupID    int64
	articleNum int64
}

// recordingHistory implements historyIndex: records the writes, answers lookups from locs
type recordingHistory struct {
	mu      sync.Mutex
	adds    []histCall
	removes []histCall
	locs    map[string][]history.Location
}

func (r *recordingHistory) AddArticleAt(messageID string, groupID, articleNum int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adds = append(r.adds, histCall{messageID, groupID, articleNum})
}

func (r *recordingHistory) RemoveArticleAt(messageID string, groupID, articleNum int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removes = append(r.removes, histCall{messageID, groupID, articleNum})
}

func (r *recordingHistory) LookupLocations(messageID string) ([]history.Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.locs[messageID], nil
}

func (r *recordingHistory) GetStats() history.HistoryStats { return history.HistoryStats{} }

func sortCalls(c []histCall) []histCall {
	out := append([]histCall(nil), c...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].messageID != out[j].messageID {
			return out[i].messageID < out[j].messageID
		}
		return out[i].groupID < out[j].groupID
	})
	return out
}

// xrefTestDB opens a scratch main database below t.TempDir() and tears it down at the end of the test
func xrefTestDB(t *testing.T) *database.Database {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		t.Skip("OpenDatabase refuses to run with euid < 1000")
	}
	dir := t.TempDir()
	database.NO_CACHE_BOOT = true
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() {
		close(db.StopChan)
		db.WG.Wait()
		if err := db.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return db
}

// xrefTestGroup creates a newsgroup in the main DB and its group DB with the given
// article_num -> message-id rows; returns the main-DB group ID
func xrefTestGroup(t *testing.T, db *database.Database, name string, articles map[int64]string) int64 {
	t.Helper()
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Active: true}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	ng, err := db.MainDBGetNewsgroup(name)
	if err != nil || ng.ID <= 0 {
		t.Fatalf("MainDBGetNewsgroup %s: %v", name, err)
	}
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", name, err)
	}
	defer groupDB.Return()
	for num, mid := range articles {
		if _, err := groupDB.DB.Exec("INSERT INTO articles (article_num, message_id) VALUES (?, ?)", num, mid); err != nil {
			t.Fatalf("insert %s article %d: %v", name, num, err)
		}
	}
	return ng.ID
}

// xrefTestHistory opens a real history index in historyDir; Close is registered as cleanup
// (a second Close by the test is harmless)
func xrefTestHistory(t *testing.T, historyDir string) *history.History {
	t.Helper()
	history.ENABLE_HISTORY = true
	cfg := history.DefaultConfig()
	cfg.HistoryDir = historyDir
	cfg.BatchTimeout = 50
	h, err := history.NewHistory(cfg, nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func xrefWaitPending(t *testing.T, h *history.History) {
	t.Helper()
	deadline := time.Now().Add(xrefTestWait())
	for h.GetStats().Pending != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("history ops not committed in time: %+v", h.GetStats())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// historyStoresNumbers reports whether the history writer stores "groupID:articleNum" tokens
// (true once slice xref-history is merged). It adds and removes a probe row.
func historyStoresNumbers(t *testing.T, h *history.History) bool {
	t.Helper()
	const probe, probeGroup = "<xref-number-probe@test>", 999999
	h.AddArticleAt(probe, probeGroup, 5)
	xrefWaitPending(t, h)
	locs, err := h.LookupLocations(probe)
	if err != nil {
		t.Fatalf("LookupLocations(probe): %v", err)
	}
	h.RemoveArticleAt(probe, probeGroup, 0)
	if len(locs) == 1 && locs[0].ArticleNum == 5 {
		h.RemoveArticleAt(probe, probeGroup, 5)
	}
	xrefWaitPending(t, h)
	return len(locs) == 1 && locs[0].ArticleNum == 5
}

// writeHistoryRow stores newsgroups for messageID directly in its routed history table
// (the history files must exist: open a History in historyDir first)
func writeHistoryRow(t *testing.T, historyDir, messageID, newsgroups string) {
	t.Helper()
	hash := history.ComputeMessageIDHash(messageID)
	dbIndex, err := strconv.ParseInt(hash[0:1], 16, 64)
	if err != nil {
		t.Fatalf("route %s: %v", messageID, err)
	}
	db, err := sql.Open("sqlite3", "file:"+historyFilePath(historyDir, int(dbIndex))+"?_busy_timeout=30000")
	if err != nil {
		t.Fatalf("open history file: %v", err)
	}
	defer db.Close()
	q := "INSERT INTO _" + hash[1:3] + "(message_id, newsgroups) VALUES(?, ?) ON CONFLICT(message_id) DO UPDATE SET newsgroups = excluded.newsgroups"
	if _, err := db.Exec(q, messageID, newsgroups); err != nil {
		t.Fatalf("write history row %s: %v", messageID, err)
	}
}

// TestXrefRebuildPassesArticleNumbers: the full rebuild adds every article with its group ID and
// article number (crosspost: one call per group with that group's number).
func TestXrefRebuildPassesArticleNumbers(t *testing.T) {
	db := xrefTestDB(t)
	idA := xrefTestGroup(t, db, "xref.test.a", map[int64]string{3: "<x@test>", 7: "<a7@test>", 20: "<a20@test>"})
	idB := xrefTestGroup(t, db, "xref.test.b", map[int64]string{1: "<x@test>", 2: "<b2@test>"})
	historyDir := filepath.Join(db.GetDataDir(), "history")
	if err := os.MkdirAll(historyDir, 0755); err != nil {
		t.Fatal(err)
	}

	// batch size 2: several ranges and a sparse jump (7 -> 20)
	rec := &recordingHistory{}
	stats := runGroups(context.Background(), db, rec, db.GetDataDir(), historyDir, 2, 100000, false, true, false)
	if stats.Errors != 0 || stats.GroupsProcessed != 2 {
		t.Fatalf("runGroups: errors=%d processed=%d", stats.Errors, stats.GroupsProcessed)
	}
	want := sortCalls([]histCall{
		{"<x@test>", idA, 3}, {"<a7@test>", idA, 7}, {"<a20@test>", idA, 20},
		{"<x@test>", idB, 1}, {"<b2@test>", idB, 2},
	})
	if got := sortCalls(rec.adds); !reflect.DeepEqual(got, want) {
		t.Fatalf("adds = %v, want %v", got, want)
	}
	if len(rec.removes) != 0 {
		t.Fatalf("rebuild removed %v", rec.removes)
	}

	// the same run against the real index, over existing rows: an old plain token is upgraded
	// in place, a wrong stored number is corrected (needs xref-history writer: a numbered add
	// replaces any token of the group; without it the wrong-number row is not written)
	h := xrefTestHistory(t, historyDir)
	numbers := historyStoresNumbers(t, h)
	if !numbers {
		t.Logf("history writer stores no article numbers yet: number checks skipped (needs xref-history writer)")
	}
	writeHistoryRow(t, historyDir, "<x@test>", strconv.FormatInt(idA, 10))
	if numbers {
		writeHistoryRow(t, historyDir, "<a7@test>", strconv.FormatInt(idA, 10)+":999")
	}
	stats = runGroups(context.Background(), db, h, db.GetDataDir(), historyDir, 2, 100000, false, true, false)
	if stats.Errors != 0 {
		t.Fatalf("runGroups (real history): errors=%d", stats.Errors)
	}
	xrefWaitPending(t, h)
	for mid, wantLocs := range map[string][]history.Location{
		"<x@test>":   {{GroupID: idA, ArticleNum: 3}, {GroupID: idB, ArticleNum: 1}},
		"<a7@test>":  {{GroupID: idA, ArticleNum: 7}},
		"<a20@test>": {{GroupID: idA, ArticleNum: 20}},
		"<b2@test>":  {{GroupID: idB, ArticleNum: 2}},
	} {
		locs, err := h.LookupLocations(mid)
		if err != nil {
			t.Fatalf("LookupLocations(%s): %v", mid, err)
		}
		sort.Slice(locs, func(i, j int) bool { return locs[i].GroupID < locs[j].GroupID })
		if len(locs) != len(wantLocs) {
			t.Fatalf("LookupLocations(%s) = %v, want %v", mid, locs, wantLocs)
		}
		for i := range wantLocs {
			if locs[i].GroupID != wantLocs[i].GroupID || (numbers && locs[i].ArticleNum != wantLocs[i].ArticleNum) {
				t.Fatalf("LookupLocations(%s) = %v, want %v", mid, locs, wantLocs)
			}
		}
	}
}

// TestXrefValidateCountsTokensWithoutNumber: -validate-only counts the found tokens without an
// article number and those with another number; misses and wrong-group entries are counted as before.
func TestXrefValidateCountsTokensWithoutNumber(t *testing.T) {
	db := xrefTestDB(t)
	idA := xrefTestGroup(t, db, "xref.test.a", map[int64]string{
		1: "<plain@test>", 2: "<num@test>", 3: "<missing@test>", 4: "<wrong@test>", 5: "<mixed@test>",
		6: "<wrongnum@test>",
	})
	historyDir := filepath.Join(db.GetDataDir(), "history")
	h := xrefTestHistory(t, historyDir)
	writeHistoryRow(t, historyDir, "<plain@test>", strconv.FormatInt(idA, 10))
	writeHistoryRow(t, historyDir, "<num@test>", strconv.FormatInt(idA, 10)+":2")
	writeHistoryRow(t, historyDir, "<wrong@test>", strconv.FormatInt(idA+100, 10)+":4")
	writeHistoryRow(t, historyDir, "<mixed@test>", strconv.FormatInt(idA+100, 10)+":9,"+strconv.FormatInt(idA, 10))
	writeHistoryRow(t, historyDir, "<wrongnum@test>", strconv.FormatInt(idA, 10)+":999")

	stats := runGroups(context.Background(), db, h, db.GetDataDir(), historyDir, 10, 100000, true, false, false)
	if stats.Errors != 0 {
		t.Fatalf("validate: errors=%d", stats.Errors)
	}
	if stats.ArticlesProcessed != 6 || stats.HistoryFound != 4 || stats.HistoryMissing != 1 || stats.HistoryWrongGroup != 1 {
		t.Fatalf("validate: processed=%d found=%d missing=%d wrong=%d, want 6/4/1/1",
			stats.ArticlesProcessed, stats.HistoryFound, stats.HistoryMissing, stats.HistoryWrongGroup)
	}
	if stats.HistoryNoNumber != 2 {
		t.Fatalf("validate: without number = %d, want 2 (<plain@test>, <mixed@test>)", stats.HistoryNoNumber)
	}
	if stats.HistoryWrongNumber != 1 {
		t.Fatalf("validate: wrong number = %d, want 1 (<wrongnum@test>)", stats.HistoryWrongNumber)
	}
	if h.GetStats().TotalAdds != 0 || h.GetStats().TotalRemoves != 0 {
		t.Fatalf("validate wrote to the index: %+v", h.GetStats())
	}
}

// TestXrefTrimRemovesExactToken: -trim removes the token of a gone article with the number of
// the row's own token ("id:num" -> num, plain "id" -> 0) and keeps the tokens of articles that exist.
func TestXrefTrimRemovesExactToken(t *testing.T) {
	db := xrefTestDB(t)
	idA := xrefTestGroup(t, db, "xref.test.a", map[int64]string{3: "<keep@test>"})
	idB := xrefTestGroup(t, db, "xref.test.b", map[int64]string{8: "<plaingone@test>"})
	idGone := idB + 100 // not in the main DB
	historyDir := filepath.Join(db.GetDataDir(), "history")
	h := xrefTestHistory(t, historyDir)
	a, b, gone := strconv.FormatInt(idA, 10), strconv.FormatInt(idB, 10), strconv.FormatInt(idGone, 10)
	// <keep@test>: a:3 exists, b:5 is gone from b, the group idGone does not exist
	writeHistoryRow(t, historyDir, "<keep@test>", a+":3,"+b+":5,"+gone+":7")
	// <plaingone@test>: old plain token for a (article gone), b exists
	writeHistoryRow(t, historyDir, "<plaingone@test>", a+","+b+":8")

	rec := &recordingHistory{}
	ts := runTrim(context.Background(), db, rec, db.GetDataDir(), historyDir, false)
	if ts.Errors != 0 {
		t.Fatalf("trim: errors=%d", ts.Errors)
	}
	if ts.RowsScanned != 2 || ts.GroupRefs != 5 || ts.Removed != 3 || ts.GroupsMissing != 1 || ts.ArticleMissing != 2 {
		t.Fatalf("trim stats = %+v, want scanned 2, refs 5, removed 3 (group gone 1, article gone 2)", *ts)
	}
	want := sortCalls([]histCall{
		{"<keep@test>", idB, 5},
		{"<keep@test>", idGone, 7},
		{"<plaingone@test>", idA, 0},
	})
	if got := sortCalls(rec.removes); !reflect.DeepEqual(got, want) {
		t.Fatalf("removes = %v, want %v", got, want)
	}
	if len(rec.adds) != 0 {
		t.Fatalf("trim added %v", rec.adds)
	}

	// the same sweep against the real index: the plain token goes now; the exact "id:num"
	// tokens only with the xref-history writer
	numbers := historyStoresNumbers(t, h)
	ts = runTrim(context.Background(), db, h, db.GetDataDir(), historyDir, false)
	if ts.Errors != 0 {
		t.Fatalf("trim (real history): errors=%d", ts.Errors)
	}
	xrefWaitPending(t, h)
	locs, err := h.LookupLocations("<plaingone@test>")
	if err != nil || len(locs) != 1 || locs[0].GroupID != idB {
		t.Fatalf("<plaingone@test> after trim = %v, %v; want only group %d", locs, err, idB)
	}
	if numbers && locs[0].ArticleNum != 8 {
		t.Fatalf("<plaingone@test> after trim = %v, want %d:8", locs, idB)
	}
	if !numbers {
		t.Logf("history writer does not remove \"id:num\" tokens yet: <keep@test> check skipped (needs xref-history writer)")
		return
	}
	locs, err = h.LookupLocations("<keep@test>")
	if err != nil || len(locs) != 1 || locs[0] != (history.Location{GroupID: idA, ArticleNum: 3}) {
		t.Fatalf("<keep@test> after trim = %v, %v; want only %d:3", locs, err, idA)
	}
}

func TestXrefParseTrimToken(t *testing.T) {
	cases := []struct {
		in       string
		id, num  int64
		wantFail bool
	}{
		{in: "12", id: 12},
		{in: "12:4711", id: 12, num: 4711},
		{in: "12:", id: 12},
		{in: "12:abc", id: 12},
		{in: "12:-3", id: 12},
		{in: "0:5", wantFail: true},
		{in: "x:5", wantFail: true},
		{in: ":5", wantFail: true},
	}
	for _, c := range cases {
		id, num, err := parseTrimToken(c.in)
		if (err != nil) != c.wantFail || id != c.id || num != c.num {
			t.Errorf("parseTrimToken(%q) = %d, %d, %v; want %d, %d, fail=%t", c.in, id, num, err, c.id, c.num, c.wantFail)
		}
	}
}
