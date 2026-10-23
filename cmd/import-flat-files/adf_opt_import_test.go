package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// adfOptImportReset points the tool at a fresh temp output dir and zeroes the counters.
func adfOptImportReset(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	oldDB, oldDry, oldUpd := *dbPath, *dryRun, *update
	*dbPath, *dryRun, *update = out, false, false
	atomic.StoreInt64(&totalProcessed, 0)
	atomic.StoreInt64(&totalErrors, 0)
	atomic.StoreInt64(&totalSkipped, 0)
	atomic.StoreInt64(&totalExisting, 0)
	t.Cleanup(func() { *dbPath, *dryRun, *update = oldDB, oldDry, oldUpd })
	return out
}

func adfOptImportWritePair(t *testing.T, headRoot, bodyRoot, rel, name, head, body string) {
	t.Helper()
	for _, p := range []struct{ root, ext, content string }{{headRoot, ".head", head}, {bodyRoot, ".body", body}} {
		dir := filepath.Join(p.root, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+p.ext), []byte(p.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func adfOptImportRun(t *testing.T, headRoot, bodyRoot string) *DBManager {
	t.Helper()
	dm := NewDBManager()
	t.Cleanup(dm.Close)
	articles := NewFileScanner(headRoot, bodyRoot).ScanFiles()
	var wg sync.WaitGroup
	wg.Add(1)
	go worker(0, articles, dm, &wg)
	wg.Wait()
	return dm
}

// TestAdfOptImportBadNameSkipsOnlyThatFile: A-tools-17. A file whose name does not
// start with 3 hex digits is skipped on its own; the valid article of the same batch
// is stored, tables are created on demand (not all 65,536), and a re-run counts the
// stored article as existing instead of as an error.
func TestAdfOptImportBadNameSkipsOnlyThatFile(t *testing.T) {
	out := adfOptImportReset(t)
	src := t.TempDir()
	headRoot, bodyRoot := filepath.Join(src, "head"), filepath.Join(src, "body")
	good := "abc0123456789abcdef0123456789abcdef"
	bad := "zz-x0123456789abcdef0123456789abcdef"
	adfOptImportWritePair(t, headRoot, bodyRoot, "0/0/1", good, "Subject: good\n", "good body\n")
	adfOptImportWritePair(t, headRoot, bodyRoot, "0/0/1", bad, "Subject: bad\n", "bad body\n")

	adfOptImportRun(t, headRoot, bodyRoot)

	if got := atomic.LoadInt64(&totalProcessed); got != 1 {
		t.Fatalf("totalProcessed = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&totalSkipped); got != 1 {
		t.Fatalf("totalSkipped = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&totalErrors); got != 0 {
		t.Fatalf("totalErrors = %d, want 0", got)
	}

	db, err := sql.Open("sqlite3", filepath.Join(out, "00.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var head, body string
	if err := db.QueryRow("SELECT head, body FROM articles_1abc WHERE messageid_hash = ?", good[3:]).Scan(&head, &body); err != nil {
		t.Fatalf("stored article not found: %v", err)
	}
	if head != "Subject: good\n" || body != "good body\n" {
		t.Fatalf("stored head/body = %q/%q", head, body)
	}
	var tables int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 1 {
		t.Fatalf("tables in 00.db = %d, want 1 (created on demand)", tables)
	}
	var indexes int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_%'").Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 {
		t.Fatalf("redundant idx_ indexes = %d, want 0", indexes)
	}

	// Second run over the same files: the duplicate is ignored, not an error.
	atomic.StoreInt64(&totalProcessed, 0)
	atomic.StoreInt64(&totalSkipped, 0)
	adfOptImportRun(t, headRoot, bodyRoot)
	if p, e, x := atomic.LoadInt64(&totalProcessed), atomic.LoadInt64(&totalErrors), atomic.LoadInt64(&totalExisting); p != 0 || e != 0 || x != 1 {
		t.Fatalf("re-run processed/errors/existing = %d/%d/%d, want 0/0/1", p, e, x)
	}
}

// TestAdfOptImportUpdateModeNewTable: -update on a table that does not exist yet
// must not fail the article ("no such table"); it is imported.
func TestAdfOptImportUpdateModeNewTable(t *testing.T) {
	adfOptImportReset(t)
	*update = true
	src := t.TempDir()
	headRoot, bodyRoot := filepath.Join(src, "head"), filepath.Join(src, "body")
	adfOptImportWritePair(t, headRoot, bodyRoot, "f/e/d", "0123456789abcdef0123456789abcdef", "H\n", "B\n")

	adfOptImportRun(t, headRoot, bodyRoot)

	if p, e := atomic.LoadInt64(&totalProcessed), atomic.LoadInt64(&totalErrors); p != 1 || e != 0 {
		t.Fatalf("processed/errors = %d/%d, want 1/0", p, e)
	}
}

// TestAdfOptImportValidateArticle covers the routing checks.
func TestAdfOptImportValidateArticle(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef"
	cases := []struct {
		a  Article
		ok bool
	}{
		{Article{MessageIDHash: hash, Dir1: "0", Dir2: "a", Dir3: "f"}, true},
		{Article{MessageIDHash: strings.ToUpper(hash), Dir1: "0", Dir2: "A", Dir3: "F"}, true},
		{Article{MessageIDHash: hash[:31], Dir1: "0", Dir2: "0", Dir3: "0"}, false},
		{Article{MessageIDHash: "zz" + hash, Dir1: "0", Dir2: "0", Dir3: "0"}, false},
		{Article{MessageIDHash: "0;" + hash, Dir1: "0", Dir2: "0", Dir3: "0"}, false},
		{Article{MessageIDHash: hash, Dir1: "0", Dir2: "0", Dir3: "g"}, false},
		{Article{MessageIDHash: hash, Dir1: "00", Dir2: "0", Dir3: "0"}, false},
		{Article{MessageIDHash: hash, Dir1: "0", Dir2: "", Dir3: "0"}, false},
	}
	for i, c := range cases {
		err := validateArticle(&c.a)
		if (err == nil) != c.ok {
			t.Errorf("case %d (%+v): err = %v, want ok=%v", i, c.a, err, c.ok)
		}
		if err == nil && !validTableName(getTableName(&c.a)) {
			t.Errorf("case %d: table name %q not valid", i, getTableName(&c.a))
		}
	}
}

// TestAdfOptImportReadFileLongLine: readFile handles lines over bufio's 64 KiB
// default and keeps the content verbatim (CRLF included).
func TestAdfOptImportReadFileLongLine(t *testing.T) {
	content := "Subject: x\r\n" + strings.Repeat("a", 200*1024) + "\r\nlast"
	p := filepath.Join(t.TempDir(), "long.body")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFile(p)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if got != content {
		t.Fatalf("readFile returned %d bytes, want %d verbatim", len(got), len(content))
	}
}
