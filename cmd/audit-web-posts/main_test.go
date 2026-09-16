package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3" // SQLite3 driver
)

const (
	lo1AuditGroup       = "smoke.test"
	lo1AuditGoneGroup   = "zz.audit.gone"
	lo1AuditCleanMsgID  = "<1757000000$0badc0de@lo1audit.test.invalid>"
	lo1AuditDirtyMsgID  = "<inject.1@lo1audit.invalid>"
	lo1AuditStrayMsgID  = "<stray.1@lo1audit.invalid>"
	lo1AuditCleanFrom   = "Clean User <noreply@pugleaf.net.invalid>"
	lo1AuditDirtyFrom   = "Evil\r\nControl: cancel <x@y>"
	lo1AuditCleanHeader = "MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=\"UTF-8\"\n" +
		"Content-Transfer-Encoding: 8bit\n" +
		"X-pugleaf-Trace: lo1audit.test.invalid;\n" +
		" nonce=\"1757000000\"; mail-complaints-to=\"abuse@lo1audit.test.invalid\";\n" +
		" posting-account=\"deadbeef\";\n" +
		"From: Clean User <noreply@pugleaf.net.invalid>\n" +
		"Newsgroups: smoke.test\n" +
		"Lines: 1\n" +
		"Bytes: 4"
	lo1AuditDirtyHeader = "From: Evil\r\nControl: cancel <x@y>\nSubject: x\nNewsgroups: smoke.test"
)

// lo1AuditSeed says what the fixture data directory holds.
type lo1AuditSeed struct {
	injected   bool // a post_queue row plus an article with smuggled header lines
	stray      bool // an article with a CR subject that has no post_queue row
	goneGroup  bool // a post_queue row for a group whose database file does not exist
	skipClean  bool // leave out the clean web post
	noGroupDir bool // do not create the group database at all
}

// lo1AuditMainSchema uses the same column definitions as the migrations
// (0001_main_schema.sql for newsgroups, 0016/0017_main_*post_queue* for post_queue).
const lo1AuditMainSchema = `
CREATE TABLE IF NOT EXISTS newsgroups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    last_article INTEGER,
    message_count INTEGER,
    active BOOLEAN NOT NULL DEFAULT 1,
    expiry_days INTEGER NOT NULL DEFAULT 0,
    max_articles INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS post_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    newsgroup_id INTEGER NOT NULL,
    message_id TEXT,
    created DATETIME DEFAULT CURRENT_TIMESTAMP,
    posted_to_remote INTEGER NOT NULL DEFAULT 0 CHECK (posted_to_remote IN (0, 1)),
    in_processing INTEGER NOT NULL DEFAULT 0 CHECK (in_processing IN (0, 1)),
    FOREIGN KEY (newsgroup_id) REFERENCES newsgroups(id) ON DELETE CASCADE
);
`

// lo1AuditOpenRW opens a fixture database read-write (the tool itself never does).
func lo1AuditOpenRW(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return db
}

// lo1AuditBuild creates a scratch data directory under t.TempDir() and returns
// it together with the main and group database paths.
func lo1AuditBuild(t *testing.T, seed lo1AuditSeed) (dataDir, mainPath, groupPath string) {
	t.Helper()
	dataDir = t.TempDir()
	mainPath = filepath.Join(dataDir, "cfg", "pugleaf.sq3")
	groupPath = filepath.Join(dataDir, "db", database.MD5Hash(lo1AuditGroup), database.SanitizeGroupName(lo1AuditGroup)+".db")

	mainDB := lo1AuditOpenRW(t, mainPath)
	defer func() {
		if err := mainDB.Close(); err != nil {
			t.Fatalf("close main db: %v", err)
		}
	}()
	if _, err := mainDB.Exec(lo1AuditMainSchema); err != nil {
		t.Fatalf("main schema: %v", err)
	}
	if _, err := mainDB.Exec(`INSERT INTO newsgroups (name) VALUES (?)`, lo1AuditGroup); err != nil {
		t.Fatalf("insert newsgroup: %v", err)
	}
	queue := func(messageID string, posted int) {
		t.Helper()
		if _, err := mainDB.Exec(
			`INSERT INTO post_queue (newsgroup_id, message_id, created, posted_to_remote)
			 VALUES ((SELECT id FROM newsgroups WHERE name = ?), ?, '2026-09-16 10:00:00', ?)`,
			lo1AuditGroup, messageID, posted); err != nil {
			t.Fatalf("insert post_queue %s: %v", messageID, err)
		}
	}
	if !seed.skipClean {
		queue(lo1AuditCleanMsgID, 0)
	}
	if seed.injected {
		queue(lo1AuditDirtyMsgID, 1)
	}
	if seed.goneGroup {
		if _, err := mainDB.Exec(`INSERT INTO newsgroups (name) VALUES (?)`, lo1AuditGoneGroup); err != nil {
			t.Fatalf("insert gone newsgroup: %v", err)
		}
		if _, err := mainDB.Exec(
			`INSERT INTO post_queue (newsgroup_id, message_id, created, posted_to_remote)
			 VALUES ((SELECT id FROM newsgroups WHERE name = ?), ?, '2026-09-16 10:00:00', 0)`,
			lo1AuditGoneGroup, "<gone.1@lo1audit.invalid>"); err != nil {
			t.Fatalf("insert gone post_queue: %v", err)
		}
	}
	if seed.noGroupDir {
		return dataDir, mainPath, groupPath
	}

	schema, err := fs.ReadFile(database.EmbeddedMigrationsFS, "migrations/0001_single_db_schema.sql")
	if err != nil {
		t.Fatalf("read embedded group schema: %v", err)
	}
	groupDB := lo1AuditOpenRW(t, groupPath)
	defer func() {
		if err := groupDB.Close(); err != nil {
			t.Fatalf("close group db: %v", err)
		}
	}()
	if _, err := groupDB.Exec(string(schema)); err != nil {
		t.Fatalf("group schema: %v", err)
	}
	article := func(num int64, messageID, subject, from, headers string) {
		t.Helper()
		if _, err := groupDB.Exec(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
			                       "references", bytes, lines, path, headers_json, body_text)
			 VALUES (?, ?, ?, ?, '2026-09-16 10:00:00', '', '', 4, 1, '.POSTED!not-for-mail', ?, 'body')`,
			num, messageID, subject, from, headers); err != nil {
			t.Fatalf("insert article %d: %v", num, err)
		}
	}
	if !seed.skipClean {
		article(1, lo1AuditCleanMsgID, "Leftovers smoke", lo1AuditCleanFrom, lo1AuditCleanHeader)
	}
	if seed.injected {
		article(900001, lo1AuditDirtyMsgID, "x", lo1AuditDirtyFrom, lo1AuditDirtyHeader)
	}
	if seed.stray {
		article(900002, lo1AuditStrayMsgID, "stray\rInjected: yes", lo1AuditCleanFrom, lo1AuditCleanHeader)
	}
	return dataDir, mainPath, groupPath
}

// lo1AuditSHA256 is the content hash of a file, used to prove the tool wrote nothing.
func lo1AuditSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Fatalf("close %s: %v", path, cerr)
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// lo1AuditRun drives run() and returns its exit code, stdout and stderr.
func lo1AuditRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rc := run(args, &stdout, &stderr)
	return rc, stdout.String(), stderr.String()
}

func TestLo1AuditFindsInjectedPost(t *testing.T) {
	dataDir, mainPath, groupPath := lo1AuditBuild(t, lo1AuditSeed{injected: true})
	mainBefore := lo1AuditSHA256(t, mainPath)
	groupBefore := lo1AuditSHA256(t, groupPath)

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stdout, lo1AuditDirtyMsgID) {
		t.Errorf("output does not name the injected message-id: %q", stdout)
	}
	if strings.Contains(stdout, lo1AuditCleanMsgID) {
		t.Errorf("output names the clean message-id: %q", stdout)
	}
	if !strings.Contains(stdout, "checked=2 flagged=1 missing=0") {
		t.Errorf("summary = %q, want checked=2 flagged=1 missing=0", stdout)
	}
	// Every finding is one TSV line with 7 columns and no smuggled newline.
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "checked=") {
			continue
		}
		if cols := strings.Split(line, "\t"); len(cols) != 7 {
			t.Errorf("finding line has %d columns, want 7: %q", len(cols), line)
		} else if cols[0] != lo1AuditGroup || cols[1] != "900001" || cols[3] != "1" {
			t.Errorf("finding line = %q, want group/article_num/posted_to_remote %s/900001/1", line, lo1AuditGroup)
		}
	}
	if got := lo1AuditSHA256(t, mainPath); got != mainBefore {
		t.Errorf("main database changed: %s -> %s", mainBefore, got)
	}
	if got := lo1AuditSHA256(t, groupPath); got != groupBefore {
		t.Errorf("group database changed: %s -> %s", groupBefore, got)
	}
}

func TestLo1AuditCleanDataIsQuiet(t *testing.T) {
	dataDir, mainPath, groupPath := lo1AuditBuild(t, lo1AuditSeed{})
	mainBefore := lo1AuditSHA256(t, mainPath)
	groupBefore := lo1AuditSHA256(t, groupPath)

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if stdout != "checked=1 flagged=0 missing=0\n" {
		t.Errorf("stdout = %q, want only the clean summary", stdout)
	}
	if got := lo1AuditSHA256(t, mainPath); got != mainBefore {
		t.Errorf("main database changed: %s -> %s", mainBefore, got)
	}
	if got := lo1AuditSHA256(t, groupPath); got != groupBefore {
		t.Errorf("group database changed: %s -> %s", groupBefore, got)
	}
}

func TestLo1AuditMissingGroupDB(t *testing.T) {
	dataDir, _, _ := lo1AuditBuild(t, lo1AuditSeed{goneGroup: true})
	gonePath := filepath.Join(dataDir, "db", database.MD5Hash(lo1AuditGoneGroup), database.SanitizeGroupName(lo1AuditGoneGroup)+".db")

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stdout, "checked=1 flagged=0 missing=1") {
		t.Errorf("summary = %q, want checked=1 flagged=0 missing=1", stdout)
	}
	if !strings.Contains(stderr, "no group DB for "+lo1AuditGoneGroup) {
		t.Errorf("stderr = %q, want a note about the missing group DB", stderr)
	}
	if _, err := os.Stat(gonePath); !os.IsNotExist(err) {
		t.Errorf("the tool created %s (stat err = %v)", gonePath, err)
	}
	if _, err := os.Stat(filepath.Dir(gonePath)); !os.IsNotExist(err) {
		t.Errorf("the tool created the group directory %s (stat err = %v)", filepath.Dir(gonePath), err)
	}
}

func TestLo1AuditQueuedMessageIDWithoutArticle(t *testing.T) {
	dataDir, _, _ := lo1AuditBuild(t, lo1AuditSeed{skipClean: false, noGroupDir: false})
	// Queue a message-id that has no article in the group database.
	mainDB := lo1AuditOpenRW(t, filepath.Join(dataDir, "cfg", "pugleaf.sq3"))
	if _, err := mainDB.Exec(
		`INSERT INTO post_queue (newsgroup_id, message_id, created, posted_to_remote)
		 VALUES ((SELECT id FROM newsgroups WHERE name = ?), '<nowhere.1@lo1audit.invalid>', '2026-09-16 10:00:00', 0)`,
		lo1AuditGroup); err != nil {
		t.Fatalf("insert post_queue: %v", err)
	}
	if err := mainDB.Close(); err != nil {
		t.Fatalf("close main db: %v", err)
	}

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir, "-v")
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stdout, "checked=1 flagged=0 missing=1") {
		t.Errorf("summary = %q, want checked=1 flagged=0 missing=1", stdout)
	}
}

func TestLo1AuditAllScansUnqueuedArticles(t *testing.T) {
	dataDir, _, _ := lo1AuditBuild(t, lo1AuditSeed{stray: true})

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0 without -all (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if strings.Contains(stdout, lo1AuditStrayMsgID) {
		t.Errorf("the stray article was reported without -all: %q", stdout)
	}

	rc, stdout, stderr = lo1AuditRun(t, "-data", dataDir, "-all")
	if rc != 1 {
		t.Fatalf("exit code = %d, want 1 with -all (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stdout, lo1AuditStrayMsgID) {
		t.Errorf("the stray article was not reported with -all: %q", stdout)
	}
	if !strings.Contains(stdout, "checked=2 flagged=1 missing=0") {
		t.Errorf("summary = %q, want checked=2 flagged=1 missing=0", stdout)
	}
}

func TestLo1AuditMissingDataDir(t *testing.T) {
	rc, stdout, stderr := lo1AuditRun(t, "-data", filepath.Join(t.TempDir(), "nope"))
	if rc != 2 {
		t.Fatalf("exit code = %d, want 2 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stderr, "failed to open main database") {
		t.Errorf("stderr = %q, want an open error", stderr)
	}
}

func TestLo1AuditCheckArticleReasons(t *testing.T) {
	tests := []struct {
		name    string
		article articleRow
		want    []string
	}{
		{"clean", articleRow{1, lo1AuditCleanMsgID, "Leftovers smoke", lo1AuditCleanFrom, "", lo1AuditCleanHeader}, nil},
		{"cr in from", articleRow{2, lo1AuditCleanMsgID, "s", lo1AuditDirtyFrom, "", lo1AuditCleanHeader},
			[]string{"contains a carriage return", "contains a line feed"}},
		{"nul in subject", articleRow{3, lo1AuditCleanMsgID, "s\x00b", lo1AuditCleanFrom, "", lo1AuditCleanHeader},
			[]string{"contains a NUL byte"}},
		{"lf in references", articleRow{4, lo1AuditCleanMsgID, "s", lo1AuditCleanFrom, "<a@b>\nControl: cancel", lo1AuditCleanHeader},
			[]string{"contains a line feed"}},
		{"bad message-id", articleRow{5, "bnews.1", "s", lo1AuditCleanFrom, "", lo1AuditCleanHeader},
			[]string{"does not match"}},
		{"unexpected header", articleRow{6, lo1AuditCleanMsgID, "s", lo1AuditCleanFrom, "", lo1AuditCleanHeader + "\nControl: cancel <x@y>"},
			[]string{"unexpected header line"}},
		{"duplicate from", articleRow{7, lo1AuditCleanMsgID, "s", lo1AuditCleanFrom, "", lo1AuditCleanHeader + "\nFrom: Someone Else <x@y.invalid>"},
			[]string{"duplicate header line"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := checkArticle(tt.article)
			if len(tt.want) == 0 {
				if len(found) != 0 {
					t.Fatalf("checkArticle() = %+v, want no findings", found)
				}
				return
			}
			var reasons []string
			for _, f := range found {
				reasons = append(reasons, f.reason)
			}
			joined := strings.Join(reasons, " | ")
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Errorf("checkArticle() reasons = %q, want one containing %q", joined, want)
				}
			}
		})
	}
}

func TestLo1AuditTSVSafe(t *testing.T) {
	got := tsvSafe("a\rb\nc\td\x00e")
	if strings.ContainsAny(got, "\r\n\t\x00") {
		t.Fatalf("tsvSafe kept a control byte: %q", got)
	}
	if got != `a\rb\nc\td\0e` {
		t.Fatalf("tsvSafe() = %q", got)
	}
}
