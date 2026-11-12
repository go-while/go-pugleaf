package main

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

func TestAdfRecoverResolveDataDir(t *testing.T) {
	cases := []struct {
		name            string
		db, data        string
		dbSet, dataSet  bool
		want            string
		wantErr         bool
		wantErrContains string
	}{
		{name: "only db", db: "/srv/pug", data: "./data", dbSet: true, want: "/srv/pug"},
		{name: "only data", db: "", data: "/srv/pug", dataSet: true, want: "/srv/pug"},
		{name: "both equal", db: "/srv/pug", data: "/srv/pug", dbSet: true, dataSet: true, want: "/srv/pug"},
		{name: "both equal after clean", db: "/srv/pug/", data: "/srv//pug", dbSet: true, dataSet: true, want: "/srv//pug"},
		{name: "both different", db: "/srv/pug", data: "/srv/pug-other", dbSet: true, dataSet: true, wantErr: true,
			wantErrContains: "-db is a deprecated alias of -data; they disagree"},
		{name: "neither", db: "", data: "./data", want: "./data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRecoverDataDir(tc.db, tc.data, tc.dbSet, tc.dataSet)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got dir %q", got)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAdfRecoverRewriteQueryKeepsFlags(t *testing.T) {
	q := strings.ToLower(query_rewriteDateSent)
	for _, col := range []string{"hide", "spam"} {
		if strings.Contains(q, col) {
			t.Fatalf("query_rewriteDateSent touches %q: %s", col, query_rewriteDateSent)
		}
	}
	if !strings.Contains(q, "set date_sent = ?") || !strings.Contains(q, "where article_num = ?") {
		t.Fatalf("query_rewriteDateSent does not update date_sent by article_num: %s", query_rewriteDateSent)
	}
}

func TestAdfRecoverWriteModes(t *testing.T) {
	cases := []struct {
		name                              string
		repair, rebuild, rewrite, reorder bool
		want                              []string
	}{
		{name: "none", want: nil},
		{name: "repair", repair: true, want: []string{"-repair"}},
		{name: "rewrite", rewrite: true, want: []string{"-rewritedates"}},
		{name: "all", repair: true, rebuild: true, rewrite: true, reorder: true,
			want: []string{"-repair", "-rebuild-threads", "-rewritedates", "-reorder-by-datesent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recoverWriteModes(tc.repair, tc.rebuild, tc.rewrite, tc.reorder)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAdfRecoverShouldRewrite(t *testing.T) {
	stored := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	plus2h := time.FixedZone("+0200", 2*3600)
	cases := []struct {
		name     string
		ok       bool
		reparsed time.Time
		want     bool
	}{
		{name: "parse failed", ok: false, reparsed: stored.Add(10 * time.Hour), want: false},
		{name: "parse failed zero", ok: false, reparsed: time.Time{}, want: false},
		{name: "equal", ok: true, reparsed: stored, want: false},
		{name: "within a second", ok: true, reparsed: stored.Add(time.Second), want: false},
		{name: "same instant other zone", ok: true, reparsed: time.Date(2020, 1, 1, 2, 0, 0, 0, plus2h), want: false},
		{name: "later", ok: true, reparsed: stored.Add(10 * time.Hour), want: true},
		{name: "earlier", ok: true, reparsed: stored.Add(-2 * time.Second), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRewrite(tc.ok, tc.reparsed, stored); got != tc.want {
				t.Fatalf("shouldRewrite(%v, %v, %v) = %v, want %v", tc.ok, tc.reparsed, stored, got, tc.want)
			}
		})
	}
}

// TestAdfRecoverReorderRebuildsThreads drives -reorder-by-datesent end to end on a scratch
// data root (adfOptOpenDB, below t.TempDir()): it renumbers a group's articles by date_sent
// into the .new database and rebuilds that database's threads with RebuildThreadsFromScratch
// (internal/database/db_rescan.go). This is the optional test of slice "rebuild" of
// .claude/plans/wip/thread-orphan-followups.md: the follow-up whose References name an
// article of another group is a thread root here, and the parent/child pairs are the new
// article numbers.
func TestAdfRecoverReorderRebuildsThreads(t *testing.T) {
	db := adfOptOpenDB(t)
	const group = "adf.recover.reorder"
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: group, Active: true}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", group, err)
	}
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	// The source numbers are in the wrong order on purpose: article 1 is the newest.
	arts := []struct {
		num            int64
		mid, refs, day string
	}{
		{1, "<reply@adf-reorder>", "<root@adf-reorder>", "2024-02-01"},
		{2, "<root@adf-reorder>", "", "2024-01-01"},
		{3, "<orphan@adf-reorder>", "<elsewhere@other.group>", "2024-03-01"},
	}
	for _, art := range arts {
		if _, err := gdb.DB.Exec(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, path, headers_json, body_text, downloaded)
			 VALUES (?, ?, 'adf reorder', 'tester@adf.invalid', ?, ?, ?, 1, 1, 'not-for-mail', '', 'body', 1)`,
			art.num, art.mid, art.day+" 10:00:00", art.day, art.refs,
		); err != nil {
			gdb.Return()
			t.Fatalf("insert article %d: %v", art.num, err)
		}
	}
	gdb.Return() // reorderSingleNewsgroup opens (and force-closes) the source itself

	processed, err := reorderSingleNewsgroup(db, group, false)
	if err != nil {
		t.Fatalf("reorderSingleNewsgroup(%s): %v", group, err)
	}
	if processed != int64(len(arts)) {
		t.Fatalf("reordered %d articles, want %d", processed, len(arts))
	}

	// Reopen the .new database the reorder wrote and read what the rebuild put there.
	destDB, destPath, err := db.GetGroupDBWithSuffix(group, "new")
	if err != nil {
		t.Fatalf("GetGroupDBWithSuffix(%s, new): %v", group, err)
	}
	defer func() {
		if err := database.CloseGroupDBDirectly(destDB); err != nil {
			t.Errorf("close %s: %v", destPath, err)
		}
	}()

	nums := make(map[string]int64, len(arts))
	rows, err := destDB.Query("SELECT message_id, article_num FROM articles")
	if err != nil {
		t.Fatalf("select articles of %s: %v", destPath, err)
	}
	for rows.Next() {
		var mid string
		var num int64
		if err := rows.Scan(&mid, &num); err != nil {
			rows.Close()
			t.Fatalf("scan article: %v", err)
		}
		nums[mid] = num
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate articles: %v", err)
	}
	// Renumbered by date_sent: the root, then its reply, then the orphan follow-up.
	for mid, want := range map[string]int64{
		"<root@adf-reorder>":   1,
		"<reply@adf-reorder>":  2,
		"<orphan@adf-reorder>": 3,
	} {
		if nums[mid] != want {
			t.Fatalf("article_num of %s = %d, want %d (all: %v)", mid, nums[mid], want, nums)
		}
	}

	type threadRow struct {
		root, parent, child int64
		depth               int
	}
	var got []threadRow
	rows, err = destDB.Query("SELECT root_article, parent_article, child_article, depth FROM threads ORDER BY child_article")
	if err != nil {
		t.Fatalf("select threads of %s: %v", destPath, err)
	}
	for rows.Next() {
		var row threadRow
		var parent sql.NullInt64
		if err := rows.Scan(&row.root, &parent, &row.child, &row.depth); err != nil {
			rows.Close()
			t.Fatalf("scan threads row: %v", err)
		}
		row.parent = parent.Int64 // NULL -> 0
		got = append(got, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate threads: %v", err)
	}
	want := []threadRow{
		{root: 1, parent: 0, child: 1, depth: 0}, // the root
		{root: 1, parent: 1, child: 2, depth: 1}, // its reply
		{root: 3, parent: 0, child: 3, depth: 0}, // the follow-up of another group's thread
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("threads rows = %v, want %v", got, want)
	}
}
