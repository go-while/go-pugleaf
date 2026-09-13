package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	_ "github.com/mattn/go-sqlite3"
)

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
	for _, mid := range []string{"<1@test>", "<2@test>", "<3@test>", "<4@test>", "<5@test>"} {
		h.AddArticle(mid, groupID)
	}
	h.AddArticle("<1@test>", otherGroupID) // crossposted
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
	deadline := time.Now().Add(10 * time.Second)
	for h.GetStats().Pending != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("history ops not committed in time: %+v", h.GetStats())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
