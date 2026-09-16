package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	lo1AuditPeerMsgID   = "<peer.1@peer.example.invalid>"
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
	// lo1AuditPeerHeader is what a peer-fetched article stores in headers_json:
	// its real header block, which the web-post allow-list must not judge.
	lo1AuditPeerHeader = "Path: news.example.invalid!not-for-mail\n" +
		"From: Someone <someone@peer.example.invalid>\n" +
		"Newsgroups: smoke.test\n" +
		"Subject: Re: a normal peer article\n" +
		"Date: Tue, 16 Sep 2026 10:00:00 +0000\n" +
		"Message-ID: <peer.1@peer.example.invalid>\n" +
		"References: <peer.0@peer.example.invalid>\n" +
		"Organization: A poorly-maintained news server\n" +
		"User-Agent: slrn/1.0.3\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=UTF-8\n" +
		"Xref: news.example.invalid smoke.test:42"
	lo1AuditPeerPath = "news.example.invalid!not-for-mail"
)

// lo1AuditSeed says what the fixture data directory holds.
type lo1AuditSeed struct {
	dir        string // base directory (default t.TempDir())
	injected   bool   // a post_queue row plus an article with smuggled header lines
	stray      bool   // an unqueued web post with a CR subject, plus a clean peer article
	goneGroup  bool   // a post_queue row for a group whose database file does not exist
	skipClean  bool   // leave out the clean web post
	noGroupDir bool   // do not create the group database at all
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

// lo1AuditOpenRW opens a fixture database read-write and in WAL mode, the way
// every pugleaf database runs (the tool itself never opens one read-write).
func lo1AuditOpenRW(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	// A file URI, so a fixture path containing "?" is not read as DSN options.
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs %s: %v", path, err)
	}
	uri := url.URL{Scheme: "file", Path: abs}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		t.Fatalf("journal_mode=WAL on %s: %v", path, err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode of %s = %q, want wal", path, mode)
	}
	return db
}

// lo1AuditBuild creates a scratch data directory (under t.TempDir() unless the
// seed names one) and returns it with the main and group database paths.
func lo1AuditBuild(t *testing.T, seed lo1AuditSeed) (dataDir, mainPath, groupPath string) {
	t.Helper()
	dataDir = seed.dir
	if dataDir == "" {
		dataDir = t.TempDir()
	}
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
	article := func(num int64, messageID, subject, from, headers, path string) {
		t.Helper()
		if _, err := groupDB.Exec(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
			                       "references", bytes, lines, path, headers_json, body_text)
			 VALUES (?, ?, ?, ?, '2026-09-16 10:00:00', '', '', 4, 1, ?, ?, 'body')`,
			num, messageID, subject, from, path, headers); err != nil {
			t.Fatalf("insert article %d: %v", num, err)
		}
	}
	if !seed.skipClean {
		article(1, lo1AuditCleanMsgID, "Leftovers smoke", lo1AuditCleanFrom, lo1AuditCleanHeader, webPostPath)
	}
	if seed.injected {
		article(900001, lo1AuditDirtyMsgID, "x", lo1AuditDirtyFrom, lo1AuditDirtyHeader, webPostPath)
	}
	if seed.stray {
		article(900002, lo1AuditStrayMsgID, "stray\rInjected: yes", lo1AuditCleanFrom, lo1AuditCleanHeader, webPostPath)
		article(900003, lo1AuditPeerMsgID, "Re: a normal peer article", "Someone <someone@peer.example.invalid>",
			lo1AuditPeerHeader, lo1AuditPeerPath)
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

// lo1AuditDirEntries lists a directory, so a test can prove that no -shm, -wal
// or other sibling file appeared.
func lo1AuditDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// lo1AuditState is the sha256 of both databases plus the listing of the two
// directories that hold them.
type lo1AuditState struct {
	mainSum, groupSum       string
	mainEntries, grpEntries []string
}

func lo1AuditSnapshot(t *testing.T, mainPath, groupPath string) lo1AuditState {
	t.Helper()
	return lo1AuditState{
		mainSum:     lo1AuditSHA256(t, mainPath),
		groupSum:    lo1AuditSHA256(t, groupPath),
		mainEntries: lo1AuditDirEntries(t, filepath.Dir(mainPath)),
		grpEntries:  lo1AuditDirEntries(t, filepath.Dir(groupPath)),
	}
}

// lo1AuditAssertUntouched fails when the audit changed a database file or left
// anything behind in the directories it read from.
func lo1AuditAssertUntouched(t *testing.T, before lo1AuditState, mainPath, groupPath string) {
	t.Helper()
	after := lo1AuditSnapshot(t, mainPath, groupPath)
	if after.mainSum != before.mainSum {
		t.Errorf("main database changed: %s -> %s", before.mainSum, after.mainSum)
	}
	if after.groupSum != before.groupSum {
		t.Errorf("group database changed: %s -> %s", before.groupSum, after.groupSum)
	}
	if strings.Join(after.mainEntries, ",") != strings.Join(before.mainEntries, ",") {
		t.Errorf("%s changed: %v -> %v", filepath.Dir(mainPath), before.mainEntries, after.mainEntries)
	}
	if strings.Join(after.grpEntries, ",") != strings.Join(before.grpEntries, ",") {
		t.Errorf("%s changed: %v -> %v", filepath.Dir(groupPath), before.grpEntries, after.grpEntries)
	}
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
	before := lo1AuditSnapshot(t, mainPath, groupPath)

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
	lo1AuditAssertUntouched(t, before, mainPath, groupPath)
}

func TestLo1AuditCleanDataIsQuiet(t *testing.T) {
	dataDir, mainPath, groupPath := lo1AuditBuild(t, lo1AuditSeed{})
	before := lo1AuditSnapshot(t, mainPath, groupPath)

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if stdout != "checked=1 flagged=0 missing=0\n" {
		t.Errorf("stdout = %q, want only the clean summary", stdout)
	}
	lo1AuditAssertUntouched(t, before, mainPath, groupPath)
}

// TestLo1AuditLeavesNoWalIndex is the read-only guarantee in its sharpest form:
// the databases are in WAL mode, and the audit must not create the -shm/-wal
// wal-index that a plain read-only open would leave behind.
func TestLo1AuditLeavesNoWalIndex(t *testing.T) {
	dataDir, mainPath, groupPath := lo1AuditBuild(t, lo1AuditSeed{injected: true})
	for _, p := range []string{mainPath, groupPath} {
		for _, suffix := range []string{"-shm", "-wal"} {
			if _, err := os.Stat(p + suffix); err == nil {
				t.Fatalf("fixture already has %s%s", p, suffix)
			}
		}
	}
	before := lo1AuditSnapshot(t, mainPath, groupPath)

	if rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir, "-all"); rc != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	for _, p := range []string{mainPath, groupPath} {
		for _, suffix := range []string{"-shm", "-wal"} {
			if _, err := os.Stat(p + suffix); !os.IsNotExist(err) {
				t.Errorf("the audit created %s%s (stat err = %v)", p, suffix, err)
			}
		}
	}
	lo1AuditAssertUntouched(t, before, mainPath, groupPath)
}

// TestLo1AuditReadOnlyDataDir proves the audit needs no write access at all.
func TestLo1AuditReadOnlyDataDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions would not be enforced")
	}
	dataDir, mainPath, groupPath := lo1AuditBuild(t, lo1AuditSeed{injected: true})
	dirs := []string{filepath.Dir(mainPath), filepath.Dir(groupPath)}
	for _, d := range dirs {
		if err := os.Chmod(d, 0555); err != nil {
			t.Fatalf("chmod %s: %v", d, err)
		}
	}
	t.Cleanup(func() {
		for _, d := range dirs {
			if err := os.Chmod(d, 0755); err != nil {
				t.Errorf("restore %s: %v", d, err)
			}
		}
	})

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stdout, lo1AuditDirtyMsgID) {
		t.Errorf("output does not name the injected message-id: %q", stdout)
	}
}

// TestLo1AuditPendingWAL covers a database that a writer still holds open: an
// immutable open would ignore its write-ahead log, so the audit falls back to a
// plain read-only open and says so, and -strict refuses it instead.
func TestLo1AuditPendingWAL(t *testing.T) {
	dataDir, mainPath, _ := lo1AuditBuild(t, lo1AuditSeed{})
	writer := lo1AuditOpenRW(t, mainPath)
	defer func() {
		if err := writer.Close(); err != nil {
			t.Errorf("close writer: %v", err)
		}
	}()
	if _, err := writer.Exec(`INSERT INTO newsgroups (name) VALUES ('lo1audit.pending')`); err != nil {
		t.Fatalf("writer insert: %v", err)
	}
	if st, err := os.Stat(mainPath + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("no pending -wal for the test (err=%v)", err)
	}

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stderr, "may create -shm/-wal files") {
		t.Errorf("stderr = %q, want the fallback notice", stderr)
	}
	// The fallback still reads the writer's committed frames, which an
	// immutable open would miss.
	if !strings.Contains(stdout, "checked=1 flagged=0 missing=0") {
		t.Errorf("summary = %q, want checked=1 flagged=0 missing=0", stdout)
	}

	rc, stdout, stderr = lo1AuditRun(t, "-data", dataDir, "-strict")
	if rc != 2 {
		t.Fatalf("exit code with -strict = %d, want 2 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stderr, "-strict") || !strings.Contains(stderr, "-wal holds") {
		t.Errorf("stderr = %q, want the pending -wal explanation", stderr)
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

// TestLo1AuditDeletedNewsgroup covers a post_queue row whose newsgroup row is
// gone: it has no group database, but it must not vanish from the summary.
func TestLo1AuditDeletedNewsgroup(t *testing.T) {
	dataDir, mainPath, _ := lo1AuditBuild(t, lo1AuditSeed{})
	mainDB := lo1AuditOpenRW(t, mainPath)
	if _, err := mainDB.Exec(
		`INSERT INTO post_queue (newsgroup_id, message_id, created, posted_to_remote)
		 VALUES (4242, '<orphan.1@lo1audit.invalid>', '2026-09-16 10:00:00', 1)`); err != nil {
		t.Fatalf("insert orphan post_queue: %v", err)
	}
	if err := mainDB.Close(); err != nil {
		t.Fatalf("close main db: %v", err)
	}

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stdout, "checked=1 flagged=0 missing=1") {
		t.Errorf("summary = %q, want checked=1 flagged=0 missing=1", stdout)
	}
	if !strings.Contains(stderr, "deleted newsgroup") || !strings.Contains(stderr, "<orphan.1@lo1audit.invalid>") {
		t.Errorf("stderr = %q, want the orphan row reported", stderr)
	}
}

func TestLo1AuditQueuedMessageIDWithoutArticle(t *testing.T) {
	dataDir, mainPath, _ := lo1AuditBuild(t, lo1AuditSeed{})
	// Queue a message-id that has no article in the group database.
	mainDB := lo1AuditOpenRW(t, mainPath)
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

// TestLo1AuditAllScansUnqueuedArticles checks both halves of -all: an unqueued
// web post is audited, and an ordinary peer article is not judged by the
// web-post header allow-list.
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
		t.Errorf("the stray web post was not reported with -all: %q", stdout)
	}
	if strings.Contains(stdout, lo1AuditPeerMsgID) {
		t.Errorf("the peer article was flagged with -all: %q", stdout)
	}
	if !strings.Contains(stdout, "checked=3 flagged=1 missing=0") {
		t.Errorf("summary = %q, want checked=3 flagged=1 missing=0", stdout)
	}
}

// TestLo1AuditDataDirWithQuestionMark covers the URI escaping of the DSN.
func TestLo1AuditDataDirWithQuestionMark(t *testing.T) {
	base := filepath.Join(t.TempDir(), "data?is#here")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", base, err)
	}
	dataDir, _, _ := lo1AuditBuild(t, lo1AuditSeed{dir: base, injected: true})

	rc, stdout, stderr := lo1AuditRun(t, "-data", dataDir)
	if rc != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", rc, stdout, stderr)
	}
	if !strings.Contains(stdout, lo1AuditDirtyMsgID) {
		t.Errorf("output does not name the injected message-id: %q", stdout)
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

func TestLo1AuditHelpExitsZero(t *testing.T) {
	rc, _, stderr := lo1AuditRun(t, "-h")
	if rc != 0 {
		t.Fatalf("exit code for -h = %d, want 0 (stderr=%q)", rc, stderr)
	}
	if !strings.Contains(stderr, "-data") {
		t.Errorf("usage = %q, want the flag list", stderr)
	}
	if rc, _, _ := lo1AuditRun(t, "-nosuchflag"); rc != 2 {
		t.Errorf("exit code for an unknown flag = %d, want 2", rc)
	}
}

func TestLo1AuditCheckArticleReasons(t *testing.T) {
	clean := articleRow{articleNum: 1, messageID: lo1AuditCleanMsgID, subject: "Leftovers smoke",
		fromHeader: lo1AuditCleanFrom, headersJSON: lo1AuditCleanHeader, path: webPostPath}
	with := func(f func(*articleRow)) articleRow {
		a := clean
		f(&a)
		return a
	}
	tests := []struct {
		name    string
		article articleRow
		webPost bool
		want    []string
	}{
		{"clean", clean, true, nil},
		{"cr in from", with(func(a *articleRow) { a.fromHeader = lo1AuditDirtyFrom }), true,
			[]string{"contains a carriage return", "contains a line feed"}},
		{"nul in subject", with(func(a *articleRow) { a.subject = "s\x00b" }), true,
			[]string{"contains a NUL byte"}},
		{"lf in references", with(func(a *articleRow) { a.references = "<a@b>\nControl: cancel" }), true,
			[]string{"contains a line feed"}},
		{"bad message-id", with(func(a *articleRow) { a.messageID = "bnews.1" }), true,
			[]string{"does not match"}},
		{"unexpected header", with(func(a *articleRow) { a.headersJSON += "\nControl: cancel <x@y>" }), true,
			[]string{"unexpected header line"}},
		{"duplicate from", with(func(a *articleRow) { a.headersJSON += "\nFrom: Someone Else <x@y.invalid>" }), true,
			[]string{"duplicate header line"}},
		{"peer article headers are not judged",
			with(func(a *articleRow) {
				a.messageID = lo1AuditPeerMsgID
				a.headersJSON = lo1AuditPeerHeader
				a.path = lo1AuditPeerPath
			}), false, nil},
		{"peer article with CR is still flagged",
			with(func(a *articleRow) {
				a.messageID = lo1AuditPeerMsgID
				a.headersJSON = lo1AuditPeerHeader
				a.path = lo1AuditPeerPath
				a.subject = "peer\rControl: cancel"
			}), false, []string{"contains a carriage return"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := checkArticle(tt.article, tt.webPost)
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

func TestLo1AuditShortenKeepsRunesAndEscapes(t *testing.T) {
	// A rune at the cut and a tab just before it: neither may be split.
	long := strings.Repeat("ä", 59) + "\t" + strings.Repeat("b", 20)
	got := shorten(long)
	if strings.ContainsAny(got, "\t\r\n\x00") {
		t.Fatalf("shorten kept a control byte: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("shorten did not truncate: %q", got)
	}
	if !strings.HasSuffix(strings.TrimSuffix(got, "..."), `\t`) {
		t.Fatalf("shorten cut the escape in half: %q", got)
	}
	if short := shorten("plain"); short != "plain" {
		t.Fatalf("shorten(%q) = %q", "plain", short)
	}
}

func TestLo1AuditReadOnlyDSN(t *testing.T) {
	dsn, err := readOnlyDSN(filepath.Join(t.TempDir(), "x?y", "pugleaf.sq3"), true)
	if err != nil {
		t.Fatalf("readOnlyDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:///") {
		t.Errorf("dsn = %q, want an absolute file URI", dsn)
	}
	if !strings.Contains(dsn, "immutable=1") || !strings.Contains(dsn, "mode=ro") {
		t.Errorf("dsn = %q, want mode=ro and immutable=1", dsn)
	}
	if strings.Contains(dsn, "x?y") {
		t.Errorf("dsn = %q, the question mark in the path is not escaped", dsn)
	}
	plain, err := readOnlyDSN(filepath.Join(t.TempDir(), "pugleaf.sq3"), false)
	if err != nil {
		t.Fatalf("readOnlyDSN: %v", err)
	}
	if strings.Contains(plain, "immutable") {
		t.Errorf("dsn = %q, want no immutable parameter", plain)
	}
}
