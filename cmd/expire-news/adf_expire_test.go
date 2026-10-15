package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3"
)

// adfExpireGroupDB creates a group database in the toolsTestGroupDB style with the derived
// thread tables: thread 1 (replies 2, 3) and thread 4 (reply 5).
func adfExpireGroupDB(t *testing.T) *database.GroupDB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "group.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE articles (article_num INTEGER PRIMARY KEY, message_id TEXT NOT NULL UNIQUE, date_sent DATETIME)`,
		`CREATE TABLE threads (id INTEGER PRIMARY KEY AUTOINCREMENT, root_article INTEGER, parent_article INTEGER, child_article INTEGER)`,
		`CREATE TABLE thread_cache (thread_root INTEGER NOT NULL PRIMARY KEY, message_count INTEGER DEFAULT 1, child_articles TEXT DEFAULT '')`,
		`CREATE TABLE cached_trees (id INTEGER PRIMARY KEY AUTOINCREMENT, thread_root INTEGER NOT NULL, article_num INTEGER NOT NULL, UNIQUE(thread_root, article_num))`,
		`CREATE TABLE tree_stats (thread_root INTEGER PRIMARY KEY, total_nodes INTEGER DEFAULT 1, FOREIGN KEY (thread_root) REFERENCES thread_cache(thread_root) ON DELETE CASCADE)`,
		`INSERT INTO articles VALUES (1, '<1@test>', '2000-01-01 00:00:00')`,
		`INSERT INTO articles VALUES (2, '<2@test>', '2000-01-01 00:00:00')`,
		`INSERT INTO articles VALUES (3, '<3@test>', '2000-01-01 00:00:00')`,
		`INSERT INTO articles VALUES (4, '<4@test>', '2999-01-01 00:00:00')`,
		`INSERT INTO articles VALUES (5, '<5@test>', '2999-01-01 00:00:00')`,
		`INSERT INTO thread_cache VALUES (1, 3, '2,3'), (4, 2, '5')`,
		`INSERT INTO cached_trees (thread_root, article_num) VALUES (1, 1), (1, 2), (1, 3), (4, 4), (4, 5)`,
		`INSERT INTO tree_stats VALUES (1, 3), (4, 2)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return &database.GroupDB{Newsgroup: "test.group", DB: db}
}

func adfExpireCount(t *testing.T, g *database.GroupDB, query string) int {
	t.Helper()
	var n int
	if err := g.DB.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestAdfExpireDeletesDerivedThreadRows: deleting a thread root also deletes its thread_cache,
// cached_trees and tree_stats rows (INT-5); other threads keep theirs.
func TestAdfExpireDeletesDerivedThreadRows(t *testing.T) {
	g := adfExpireGroupDB(t)
	if err := deleteArticles(g, []int64{1, 2, 3}, historyTrim{}); err != nil {
		t.Fatalf("deleteArticles: %v", err)
	}
	for _, table := range []string{"thread_cache", "cached_trees", "tree_stats"} {
		if n := adfExpireCount(t, g, "SELECT COUNT(*) FROM "+table+" WHERE thread_root = 1"); n != 0 {
			t.Errorf("%s: %d rows of the deleted root 1 left", table, n)
		}
	}
	if n := adfExpireCount(t, g, "SELECT COUNT(*) FROM thread_cache WHERE thread_root = 4"); n != 1 {
		t.Errorf("thread_cache rows of root 4 = %d, want 1", n)
	}
	if n := adfExpireCount(t, g, "SELECT COUNT(*) FROM cached_trees WHERE thread_root = 4"); n != 2 {
		t.Errorf("cached_trees rows of root 4 = %d, want 2", n)
	}
	if n := adfExpireCount(t, g, "SELECT COUNT(*) FROM tree_stats WHERE thread_root = 4"); n != 1 {
		t.Errorf("tree_stats rows of root 4 = %d, want 1", n)
	}
	if n := adfExpireCount(t, g, "SELECT COUNT(*) FROM articles"); n != 2 {
		t.Errorf("articles left = %d, want 2", n)
	}
}

// TestAdfExpireDerivedRowsMissingTables: a group database without the derived tables (old or
// minimal schema) still deletes its articles.
func TestAdfExpireDerivedRowsMissingTables(t *testing.T) {
	g := toolsTestGroupDB(t)
	if err := deleteArticles(g, []int64{1}, historyTrim{}); err != nil {
		t.Fatalf("deleteArticles without derived tables: %v", err)
	}
	if n := adfExpireCount(t, g, "SELECT COUNT(*) FROM articles"); n != 4 {
		t.Errorf("articles left = %d, want 4", n)
	}
}
