package main

// Tests of the followup-leftovers slice fl-ops-tools (recover-db): repair step 6 counts
// articles without a threads row through the real child_article column, -rewritedates
// counts a fix only after its UPDATE succeeded, and the parse-failure list is capped.
// Databases live below t.TempDir() (adfOptOpenDB); ./data is never used.

import (
	"fmt"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// flOpsGroupDB creates the newsgroup and returns its group database.
func flOpsGroupDB(t *testing.T, db *database.Database, name string) *database.GroupDB {
	t.Helper()
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Active: true}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	gdb, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", name, err)
	}
	t.Cleanup(gdb.Return)
	return gdb
}

func flOpsInsertArticle(t *testing.T, gdb *database.GroupDB, num int64, dateSent, dateString string) {
	t.Helper()
	if _, err := gdb.DB.Exec(
		`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, path, headers_json, body_text)
		 VALUES (?, ?, 'fl-ops', 'tester@fl-ops.invalid', ?, ?, '', 1, 1, 'not-for-mail', '', 'body')`,
		num, fmt.Sprintf("<fl-ops.%d@fl-ops.invalid>", num), dateSent, dateString,
	); err != nil {
		t.Fatalf("insert article %d: %v", num, err)
	}
}

func TestFlOpsRecoverArticlesWithoutThreading(t *testing.T) {
	db := adfOptOpenDB(t)
	gdb := flOpsGroupDB(t, db, "fl.ops.recover.threads")
	for num := int64(1); num <= 4; num++ {
		flOpsInsertArticle(t, gdb, num, "2024-01-01 00:00:00", "Mon, 01 Jan 2024 00:00:00 +0000")
	}
	// 1 is a root (own row), 2 a child of 1, 3 only appears as a root_article, 4 has no row.
	for _, row := range [][3]any{{1, nil, 1}, {1, 1, 2}, {3, nil, 99}} {
		if _, err := gdb.DB.Exec(`INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, ?, ?, 0, 0)`,
			row[0], row[1], row[2]); err != nil {
			t.Fatalf("insert thread %v: %v", row, err)
		}
	}
	tx, err := gdb.DB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	n, err := countArticlesWithoutThreading(tx)
	if err != nil {
		t.Fatalf("countArticlesWithoutThreading: %v (the query must only use real threads columns)", err)
	}
	if n != 1 {
		t.Fatalf("articles without threading = %d, want 1 (article 4)", n)
	}
}

func TestFlOpsRecoverDateFixCountsAndCap(t *testing.T) {
	db := adfOptOpenDB(t)
	gdb := flOpsGroupDB(t, db, "fl.ops.recover.dates")
	const parseable = "Mon, 01 Jan 2024 12:00:00 +0000"
	// 1 and 2: mismatches; the UPDATE of 2 fails (trigger), so only 1 counts as fixed.
	flOpsInsertArticle(t, gdb, 1, "2000-01-01 00:00:00", parseable)
	flOpsInsertArticle(t, gdb, 2, "2000-01-01 00:00:00", parseable)
	// 3: already correct.
	flOpsInsertArticle(t, gdb, 3, "2024-01-01 12:00:00", parseable)
	// More unparseable date strings than the list keeps.
	failures := int64(maxListedParseFailures + 7)
	for i := int64(0); i < failures; i++ {
		flOpsInsertArticle(t, gdb, 100+i, "2024-01-01 12:00:00", "not a date at all")
	}
	if _, err := gdb.DB.Exec(`CREATE TRIGGER fl_ops_fail_update BEFORE UPDATE OF date_sent ON articles
		WHEN NEW.article_num = 2 BEGIN SELECT RAISE(ABORT, 'fl-ops forced failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	res, err := checkGroupDates(gdb, "fl.ops.recover.dates", true, false)
	if err != nil {
		t.Fatalf("checkGroupDates: %v", err)
	}
	if res.Mismatches != 2 || res.Fixed != 1 {
		t.Fatalf("mismatches=%d fixed=%d, want 2 and 1 (a failed UPDATE is not a fix)", res.Mismatches, res.Fixed)
	}
	if res.ParseFailures != failures {
		t.Fatalf("parse failures = %d, want %d", res.ParseFailures, failures)
	}
	listed := 0
	for _, p := range res.Problems {
		if p.ParseFailed {
			listed++
		}
	}
	if listed != maxListedParseFailures {
		t.Fatalf("listed parse failures = %d, want the cap %d", listed, maxListedParseFailures)
	}
	if got := res.omittedParseFailures(); got != 7 {
		t.Fatalf("omitted parse failures = %d, want 7", got)
	}

	var stored string
	if err := gdb.DB.QueryRow("SELECT date_sent FROM articles WHERE article_num = 1").Scan(&stored); err != nil {
		t.Fatalf("read article 1: %v", err)
	}
	if stored != "2024-01-01 12:00:00" && stored != "2024-01-01T12:00:00Z" {
		t.Fatalf("article 1 date_sent = %q, want the rewritten 2024-01-01 12:00:00", stored)
	}
}
