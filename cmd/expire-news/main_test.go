package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	_ "github.com/mattn/go-sqlite3"
)

// testWait is the hang guard of this package's tests: it only stops a hung test, a passing test
// never waits on it. Override with PUGLEAF_TEST_DEADLINE (a Go duration, e.g. 120s); default 60 s.
// Plan test-timing-hardening D3.
var testWait = testWaitFromEnv(60 * time.Second)

func testWaitFromEnv(def time.Duration) time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// toolsTestGroupDB creates a minimal group database with articles 1..5
func toolsTestGroupDB(t *testing.T) *database.GroupDB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "group.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE articles (article_num INTEGER PRIMARY KEY, message_id TEXT NOT NULL UNIQUE, date_sent DATETIME)`,
		`CREATE TABLE threads (id INTEGER PRIMARY KEY AUTOINCREMENT, root_article INTEGER, parent_article INTEGER, child_article INTEGER)`,
		`INSERT INTO articles VALUES (1, '<1@test>', '2000-01-01 00:00:00')`,
		`INSERT INTO articles VALUES (2, '<2@test>', NULL)`,
		`INSERT INTO articles VALUES (3, '<3@test>', 'garbage')`,
		`INSERT INTO articles VALUES (4, '<4@test>', '2000-01-02 00:00:00')`,
		`INSERT INTO articles VALUES (5, '<5@test>', '2999-01-01 00:00:00')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return &database.GroupDB{Newsgroup: "test.group", DB: db}
}

func TestToolsTestGetArticleBatchNullDate(t *testing.T) {
	g := toolsTestGroupDB(t)
	got, err := getArticleBatch(g, 0, 10)
	if err != nil {
		t.Fatalf("getArticleBatch: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d rows, want 5", len(got))
	}
	wantValid := map[int64]bool{1: true, 2: false, 3: false, 4: true, 5: true}
	for _, c := range got {
		if c.DateSent.Valid != wantValid[c.Num] {
			t.Errorf("article %d: Valid=%t, want %t", c.Num, c.DateSent.Valid, wantValid[c.Num])
		}
	}
	if y := got[0].DateSent.Time.Year(); y != 2000 {
		t.Errorf("article 1 year = %d", y)
	}

	// keyset paging
	got, err = getArticleBatch(g, 3, 1)
	if err != nil || len(got) != 1 || got[0].Num != 4 {
		t.Fatalf("paging: %v %v", got, err)
	}
}

func TestToolsTestToNullTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		in    interface{}
		valid bool
	}{
		{nil, false},
		{"", false},
		{"not a date", false},
		{now, true},
		{time.Time{}, false},
		{"2024-05-06 07:08:09", true},
		{[]byte("2024-05-06T07:08:09Z"), true},
		{int64(1700000000), true},
	}
	for _, c := range cases {
		if got := toNullTime(c.in); got.Valid != c.valid {
			t.Errorf("toNullTime(%#v).Valid = %t, want %t", c.in, got.Valid, c.valid)
		}
	}
}

func TestToolsTestDeleteArticlesTrimHistory(t *testing.T) {
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

	const groupID, otherGroupID = 7, 9
	for i, mid := range []string{"<1@test>", "<2@test>", "<3@test>", "<4@test>", "<5@test>"} {
		h.AddArticleAt(mid, groupID, int64(i+1)) // article numbers 1..5 of toolsTestGroupDB
	}
	h.AddArticleAt("<1@test>", otherGroupID, 42) // crossposted
	toolsTestWaitPending(t, h)

	// without trim: history is kept
	if err := deleteArticles(g, []int64{4}, historyTrim{}); err != nil {
		t.Fatalf("deleteArticles (keep): %v", err)
	}
	// with trim
	if err := deleteArticles(g, []int64{1, 2}, historyTrim{hist: h, groupID: groupID}); err != nil {
		t.Fatalf("deleteArticles (trim): %v", err)
	}
	toolsTestWaitPending(t, h)

	var left int
	if err := g.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&left); err != nil || left != 2 {
		t.Fatalf("articles left = %d (%v), want 2", left, err)
	}

	check := func(mid string, want []int64) {
		t.Helper()
		got, err := h.LookupGroups(mid)
		if err != nil {
			t.Fatalf("LookupGroups(%s): %v", mid, err)
		}
		if len(got) != len(want) {
			t.Fatalf("LookupGroups(%s) = %v, want %v", mid, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("LookupGroups(%s) = %v, want %v", mid, got, want)
			}
		}
	}
	check("<1@test>", []int64{otherGroupID})
	check("<2@test>", nil)
	check("<3@test>", []int64{groupID})
	check("<4@test>", []int64{groupID}) // deleted without trim: remembered
}

func toolsTestWaitPending(t *testing.T, h *history.History) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for h.GetStats().Pending != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("history ops not committed in time: %+v", h.GetStats())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestToolsTestDeleteArticlesNoOverviewWarning: a delete batch no longer touches the legacy
// overview table (gone since the unified schema), so nothing about it is logged, and the articles
// and their thread rows are deleted.
func TestToolsTestDeleteArticlesNoOverviewWarning(t *testing.T) {
	g := toolsTestGroupDB(t)
	if _, err := g.DB.Exec(`INSERT INTO threads (root_article, parent_article, child_article) VALUES
		(1, NULL, 1), (1, 1, 2), (4, NULL, 4), (4, 4, 5)`); err != nil {
		t.Fatalf("insert threads: %v", err)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	err := deleteArticles(g, []int64{1, 2, 3}, historyTrim{})
	log.SetOutput(prev)
	if err != nil {
		t.Fatalf("deleteArticles: %v", err)
	}
	if strings.Contains(strings.ToLower(buf.String()), "overview") {
		t.Fatalf("log mentions overview:\n%s", buf.String())
	}

	var left int
	if err := g.DB.QueryRow("SELECT COUNT(*) FROM articles WHERE article_num IN (1, 2, 3)").Scan(&left); err != nil || left != 0 {
		t.Fatalf("deleted articles left = %d (%v), want 0", left, err)
	}
	if err := g.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&left); err != nil || left != 2 {
		t.Fatalf("articles left = %d (%v), want 2", left, err)
	}
	if err := g.DB.QueryRow("SELECT COUNT(*) FROM threads WHERE root_article = 1 OR child_article IN (1, 2, 3)").Scan(&left); err != nil || left != 0 {
		t.Fatalf("thread rows of deleted articles left = %d (%v), want 0", left, err)
	}
	if err := g.DB.QueryRow("SELECT COUNT(*) FROM threads").Scan(&left); err != nil || left != 2 {
		t.Fatalf("thread rows left = %d (%v), want 2", left, err)
	}
}

// toolsTestCountersGroup creates newsgroup name in the main database with articles 1..n in its
// group database (created through GetGroupDB, so it has the current schema and article_num_seq).
func toolsTestCountersGroup(t *testing.T, db *database.Database, name string, n int) {
	t.Helper()
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Active: true, LastArticle: int64(n), MessageCount: int64(n), HighWater: n, LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	g, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", name, err)
	}
	defer g.Return()
	for i := 1; i <= n; i++ {
		if _, err := g.DB.Exec("INSERT INTO articles (article_num, message_id) VALUES (?, ?)", i, fmt.Sprintf("<%d@%s>", i, name)); err != nil {
			t.Fatalf("insert article %d: %v", i, err)
		}
	}
}

// toolsTestDeleteAndCount deletes nums from the group and runs updateNewsgroupCounters.
func toolsTestDeleteAndCount(t *testing.T, db *database.Database, name string, nums []int64) *models.Newsgroup {
	t.Helper()
	g, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", name, err)
	}
	err = deleteArticles(g, nums, historyTrim{})
	g.Return()
	if err != nil {
		t.Fatalf("deleteArticles: %v", err)
	}
	if err := updateNewsgroupCounters(db, name); err != nil {
		t.Fatalf("updateNewsgroupCounters: %v", err)
	}
	ng, err := db.MainDBGetNewsgroup(name)
	if err != nil {
		t.Fatalf("MainDBGetNewsgroup %s: %v", name, err)
	}
	return ng
}

// TestToolsTestUpdateNewsgroupCountersWatermarks: after an expiry the main database row carries
// the marks LIST ACTIVE prints: high_water = last_article, low_water = first survivor. An emptied
// group keeps its place (article_num_seq): last_article = high_water = old max, low_water = old
// max + 1, so LIST ACTIVE shows low = high+1 like GROUP.
func TestToolsTestUpdateNewsgroupCountersWatermarks(t *testing.T) {
	db := adfOptExpireDB(t)

	// survivors: 1..6, delete 1, 2 and the top article 6
	toolsTestCountersGroup(t, db, "tools.test.counters.some", 6)
	ng := toolsTestDeleteAndCount(t, db, "tools.test.counters.some", []int64{1, 2, 6})
	if ng.MessageCount != 3 || ng.LastArticle != 5 || ng.HighWater != 5 || ng.LowWater != 3 {
		t.Fatalf("survivors: message_count %d, last_article %d, high_water %d, low_water %d; want 3, 5, 5, 3",
			ng.MessageCount, ng.LastArticle, ng.HighWater, ng.LowWater)
	}

	// emptied: all of 1..4 deleted, article_num_seq.last_num stays 4
	toolsTestCountersGroup(t, db, "tools.test.counters.empty", 4)
	ng = toolsTestDeleteAndCount(t, db, "tools.test.counters.empty", []int64{1, 2, 3, 4})
	if ng.MessageCount != 0 || ng.LastArticle != 4 || ng.HighWater != 4 || ng.LowWater != 5 {
		t.Fatalf("emptied: message_count %d, last_article %d, high_water %d, low_water %d; want 0, 4, 4, 5",
			ng.MessageCount, ng.LastArticle, ng.HighWater, ng.LowWater)
	}

	// never used: no article ever, next number 1
	toolsTestCountersGroup(t, db, "tools.test.counters.never", 0)
	if err := updateNewsgroupCounters(db, "tools.test.counters.never"); err != nil {
		t.Fatalf("updateNewsgroupCounters: %v", err)
	}
	ng, err := db.MainDBGetNewsgroup("tools.test.counters.never")
	if err != nil {
		t.Fatalf("MainDBGetNewsgroup: %v", err)
	}
	if ng.MessageCount != 0 || ng.LastArticle != 0 || ng.HighWater != 0 || ng.LowWater != 1 {
		t.Fatalf("never used: message_count %d, last_article %d, high_water %d, low_water %d; want 0, 0, 0, 1",
			ng.MessageCount, ng.LastArticle, ng.HighWater, ng.LowWater)
	}
}
