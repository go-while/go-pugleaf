package main

import (
	"bufio"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// iaSchema is the DDL of iafetch's download DB, copied from
// /tank0/efs/GO/src/github.com/go-while/mbox2nntp/cmd/iafetch/db.go (commit f8947fc,
// `git -C ../mbox2nntp log -1 --format=%h -- cmd/iafetch/db.go`), so the reader is tested
// against the real schema (iafetch writes it with the modernc driver; the DDL is the same).
const iaSchema = `
CREATE TABLE IF NOT EXISTS items (
	identifier TEXT PRIMARY KEY,
	ord        INTEGER,
	tier       TEXT,
	list       TEXT,
	status     TEXT NOT NULL,
	files      INTEGER NOT NULL DEFAULT 0,
	bytes      INTEGER NOT NULL DEFAULT 0,
	note       TEXT NOT NULL DEFAULT '',
	updated    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
	identifier TEXT NOT NULL,
	name       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	md5        TEXT NOT NULL,
	path       TEXT NOT NULL,
	done_at    TEXT NOT NULL,
	PRIMARY KEY (identifier, name)
);
CREATE TABLE IF NOT EXISTS pieces (
	identifier TEXT NOT NULL,
	name       TEXT NOT NULL,
	piece_size INTEGER NOT NULL,
	piece      INTEGER NOT NULL,
	PRIMARY KEY (identifier, name, piece_size, piece)
);
`

// iaWriterDSN is the read-write DSN the tests (playing iafetch) create the fixture with.
// The path is escaped for the same reason iafetchDSN escapes it: a '?' or '#' in the path
// of a concatenated "file:" DSN is read as the start of the URI query by SQLite.
func iaWriterDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?_busy_timeout=10000"
}

// iaNewDB creates an iafetch DB at path in WAL mode and returns the open writer: keeping it
// open is what iafetch does while it downloads, and it keeps the -wal/-shm files on disk,
// so the reader is exercised against a live WAL database.
func iaNewDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", iaWriterDSN(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	if _, err := db.Exec(iaSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// iaItem inserts an items row; ord < 0 stores NULL (iafetch leaves ord open for items it
// did not get from a numbered list).
func iaItem(t *testing.T, db *sql.DB, identifier string, ord int64, status string) {
	t.Helper()
	var ordVal any
	if ord >= 0 {
		ordVal = ord
	}
	if _, err := db.Exec(`INSERT INTO items (identifier, ord, tier, list, status, files, bytes, note, updated)
		VALUES (?, ?, 't1', 'list', ?, 0, 0, '', '2026-11-06T00:00:00Z')`, identifier, ordVal, status); err != nil {
		t.Fatalf("insert item %s: %v", identifier, err)
	}
}

// iaFileRow inserts a files row as iafetch does (path = filepath.Join(-o, identifier, name)).
func iaFileRow(t *testing.T, db *sql.DB, identifier, name string, size int64, md5sum, path string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO files (identifier, name, size, md5, path, done_at)
		VALUES (?, ?, ?, ?, ?, '2026-11-06T00:00:00Z')`, identifier, name, size, md5sum, path); err != nil {
		t.Fatalf("insert file %s/%s: %v", identifier, name, err)
	}
}

// iaWrite creates a file of n bytes (with its parent directories) and returns n.
func iaWrite(t *testing.T, path string, n int) int64 {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return int64(n)
}

// iaSum is the md5 of a file, to show that reading iafetch's DB changes nothing.
func iaSum(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// iaNames returns "identifier/name" for every input, in order.
func iaNames(inputs []plannedInput) []string {
	out := make([]string, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, in.Identifier+"/"+in.Name)
	}
	return out
}

func iaFind(t *testing.T, inputs []plannedInput, identifier, name string) plannedInput {
	t.Helper()
	for _, in := range inputs {
		if in.Identifier == identifier && in.Name == name {
			return in
		}
	}
	t.Fatalf("input %s/%s not in %v", identifier, name, iaNames(inputs))
	return plannedInput{}
}

// TestMboxIAOrderAndReasons pins the import order (items.ord, then identifier and name,
// rows without an ord last) and the classification of the file names of an archive.org
// item. Absolute paths; the files of the "incomplete" and "dark" items are there as well.
func TestMboxIAOrderAndReasons(t *testing.T) {
	dir := t.TempDir()
	db := iaNewDB(t, filepath.Join(dir, "iafetch.sqlite"))

	iaItem(t, db, "item-b", 1, "incomplete")
	iaItem(t, db, "item-a", 2, "done")
	iaItem(t, db, "item-c", 3, "dark")
	iaItem(t, db, "item-n", -1, "done") // items row without ord
	// item-z has no items row at all.

	type row struct {
		id, name, md5 string
		bytes         int
	}
	rows := []row{
		{"item-a", "a1.mbox.zip", "aa11", 11},
		{"item-a", "sub/dir/a2.mbox.gz", "aa22", 22},
		{"item-a", "a3.iso", "aa33", 33},
		{"item-a", "a_meta.xml", "aa44", 44},
		{"item-b", "b1.usenet.xz", "bb11", 55},
		{"item-b", "b_djvu.txt", "bb22", 66},
		{"item-c", "c1.mbox.zip", "cc11", 77},
		{"item-n", "n1.mbox.zip", "nn11", 88},
		{"item-z", "z1.mbox.zip", "zz11", 99},
	}
	for _, r := range rows {
		path := filepath.Join(dir, "mirror", r.id, filepath.FromSlash(r.name))
		size := iaWrite(t, path, r.bytes)
		iaFileRow(t, db, r.id, r.name, size, r.md5, path)
	}

	inputs, err := iafetchInputs(filepath.Join(dir, "iafetch.sqlite"), "")
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}
	want := []string{
		"item-b/b1.usenet.xz", "item-b/b_djvu.txt", // ord 1
		"item-a/a1.mbox.zip", "item-a/a3.iso", "item-a/a_meta.xml", "item-a/sub/dir/a2.mbox.gz", // ord 2
		"item-c/c1.mbox.zip", // ord 3
		"item-n/n1.mbox.zip", // no ord: last, by identifier
		"item-z/z1.mbox.zip", // no items row: last, by identifier
	}
	got := iaNames(inputs)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}

	for _, c := range []struct {
		id, name  string
		supported bool
		reason    string
	}{
		{"item-a", "a1.mbox.zip", true, ""},
		{"item-a", "sub/dir/a2.mbox.gz", true, ""},
		{"item-a", "a3.iso", false, "iso"},
		{"item-a", "a_meta.xml", false, "xml"},
		{"item-b", "b1.usenet.xz", true, ""},
		{"item-b", "b_djvu.txt", false, "txt"},
		{"item-c", "c1.mbox.zip", true, ""}, // a recorded file of a "dark" item is complete
	} {
		in := iaFind(t, inputs, c.id, c.name)
		if in.Supported != c.supported || in.Reason != c.reason {
			t.Errorf("%s/%s: supported=%v reason=%q, want %v/%q", c.id, c.name, in.Supported, in.Reason, c.supported, c.reason)
		}
	}

	in := iaFind(t, inputs, "item-a", "sub/dir/a2.mbox.gz")
	if in.Source != sourceIAFetch {
		t.Errorf("Source = %q, want %q", in.Source, sourceIAFetch)
	}
	if want := filepath.Join(dir, "mirror", "item-a", "sub", "dir", "a2.mbox.gz"); in.Path != want {
		t.Errorf("Path = %q, want %q", in.Path, want)
	}
	if in.Size != 22 || in.MD5 != "aa22" {
		t.Errorf("Size/MD5 = %d/%q, want 22/aa22", in.Size, in.MD5)
	}
	if in.Mtime == 0 {
		t.Errorf("Mtime = 0 for a file on disk")
	}
}

// TestMboxIARelativePaths joins a relative files.path to the base and refuses it without one.
func TestMboxIARelativePaths(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "iafetch.sqlite")
	db := iaNewDB(t, dbPath)

	iaItem(t, db, "item-a", 1, "done")
	size := iaWrite(t, filepath.Join(dir, "ia-download", "item-a", "a1.mbox.zip"), 17)
	// iafetch ran with a relative -o, so it recorded a relative path.
	iaFileRow(t, db, "item-a", "a1.mbox.zip", size, "aa11", "ia-download/item-a/a1.mbox.zip")
	iaItem(t, db, "item-b", 2, "done")
	iaFileRow(t, db, "item-b", "b1.mbox.zip", 3, "bb11", "ia-download/item-b/b1.mbox.zip")

	inputs, err := iafetchInputs(dbPath, dir)
	if err != nil {
		t.Fatalf("iafetchInputs with base: %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("got %d inputs, want 2: %v", len(inputs), iaNames(inputs))
	}
	in := inputs[0]
	if want := filepath.Join(dir, "ia-download", "item-a", "a1.mbox.zip"); in.Path != want {
		t.Errorf("Path = %q, want %q", in.Path, want)
	}
	if !in.Supported || in.Reason != "" {
		t.Errorf("first input: supported=%v reason=%q, want true/\"\"", in.Supported, in.Reason)
	}

	// Without a base the first relative row is an error naming it; nothing is returned.
	inputs, err = iafetchInputs(dbPath, "")
	if err == nil {
		t.Fatalf("iafetchInputs without base: no error, got %v", iaNames(inputs))
	}
	if inputs != nil {
		t.Errorf("inputs = %v, want nil on error", iaNames(inputs))
	}
	for _, part := range []string{"item-a", "a1.mbox.zip", "ia-download/item-a/a1.mbox.zip", "-from-iafetch-base"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not name %q", err, part)
		}
	}
	if strings.Contains(err.Error(), "item-b") {
		t.Errorf("error %q names a later row, want the first one only", err)
	}
}

// TestMboxIADiskState pins what the state of the file on disk does to a recorded row.
func TestMboxIADiskState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "iafetch.sqlite")
	db := iaNewDB(t, dbPath)
	iaItem(t, db, "item-a", 1, "incomplete")

	// 1. recorded, but not on disk (deleted, or a tree the importer cannot see).
	iaFileRow(t, db, "item-a", "a1.mbox.zip", 100, "aa11", filepath.Join(dir, "item-a", "a1.mbox.zip"))
	// 2. on disk with another size than recorded.
	changed := filepath.Join(dir, "item-a", "a2.mbox.gz")
	iaWrite(t, changed, 7)
	iaFileRow(t, db, "item-a", "a2.mbox.gz", 100, "aa22", changed)
	// 3. on disk, size as recorded.
	ok := filepath.Join(dir, "item-a", "a3.mbox.zip")
	iaFileRow(t, db, "item-a", "a3.mbox.zip", iaWrite(t, ok, 42), "aa33", ok)
	// 4. iafetch had no size for the file (0 empty, -1 unknown): the disk size is taken.
	nosize := filepath.Join(dir, "item-a", "a4.mbox.zip")
	iaWrite(t, nosize, 9)
	iaFileRow(t, db, "item-a", "a4.mbox.zip", 0, "aa44", nosize)
	unknown := filepath.Join(dir, "item-a", "a5.mbox.zip")
	iaWrite(t, unknown, 5)
	iaFileRow(t, db, "item-a", "a5.mbox.zip", -1, "aa55", unknown)
	// 5. an unsupported file that is missing keeps its suffix reason.
	iaFileRow(t, db, "item-a", "a6.iso", 100, "aa66", filepath.Join(dir, "item-a", "a6.iso"))

	inputs, err := iafetchInputs(dbPath, "")
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}
	for _, c := range []struct {
		name      string
		supported bool
		reason    string
		size      int64
		mtime     bool // true: a modification time is expected
		pending   bool // true: worth another look on the next scan
	}{
		{"a1.mbox.zip", false, iaReasonMissing, 100, false, true},
		{"a2.mbox.gz", false, iaReasonSizeChanged, 100, true, true},
		{"a3.mbox.zip", true, "", 42, true, false},
		{"a4.mbox.zip", true, "", 9, true, false}, // Size from disk
		{"a5.mbox.zip", true, "", 5, true, false}, // Size from disk
		{"a6.iso", false, "iso", 100, false, false},
	} {
		in := iaFind(t, inputs, "item-a", c.name)
		if in.Supported != c.supported || in.Reason != c.reason {
			t.Errorf("%s: supported=%v reason=%q, want %v/%q", c.name, in.Supported, in.Reason, c.supported, c.reason)
		}
		if in.Size != c.size {
			t.Errorf("%s: Size = %d, want %d", c.name, in.Size, c.size)
		}
		if (in.Mtime != 0) != c.mtime {
			t.Errorf("%s: Mtime = %d, want set=%v", c.name, in.Mtime, c.mtime)
		}
		if got := iafetchPending(in.Reason); got != c.pending {
			t.Errorf("%s: iafetchPending(%q) = %v, want %v", c.name, in.Reason, got, c.pending)
		}
	}
}

// TestMboxIASupportedNames pins the suffix rule and the reason names for -status.
func TestMboxIASupportedNames(t *testing.T) {
	for _, c := range []struct {
		name      string
		supported bool
		reason    string
	}{
		{"usenet-alt.foo.mbox", true, ""},
		{"usenet-alt.foo.mbox.gz", true, ""},
		{"usenet-alt.foo.mbox.zip", true, ""},
		{"usenet-alt.foo.mbox.7z", true, ""},
		{"usenet-alt.foo.bag", true, ""},
		{"alt.foo.usenet", true, ""},
		{"alt.foo.usenet.xz", true, ""},
		{"alt.foo.usenet.7z", true, ""},
		{"sub/dir/alt.foo.mbox.zip", true, ""},
		{"cd1.iso", false, "iso"},
		{"spool.sqsh", false, "sqsh"},
		{"spool.tar", false, "tar"},
		{"item_djvu.txt", false, "txt"},
		{"spool.ufs", false, "ufs"},
		{"disk.img", false, "img"},
		{"item_files.xml", false, "xml"},
		{"item_archive.torrent", false, "torrent"},
		{"item_jp2.zip", false, iaReasonUnsupported},
		{"item_meta.sqlite", false, iaReasonUnsupported},
		{"README", false, iaReasonUnsupported},
		{"alt.foo.mbox.zip.done", false, iaReasonUnsupported}, // already imported by an older run
		{"mbox.zip/decoy.iso", false, "iso"},                  // the base name decides, not the folder
		// openSource has no reader for these, although isInputName sees ".mbox" in them.
		{"alt.foo.mbox.bz2", false, iaReasonUnsupported},
		{"alt.foo.mbox.zst", false, iaReasonUnsupported},
		{"alt.foo.mbox.tar", false, "tar"},
		{"alt.foo.mbox.txt", false, "txt"},
		// isInputName is case-sensitive, like the directory scan it is shared with.
		{"X.MBOX.ZIP", false, iaReasonUnsupported},
		{"F.ISO", false, "iso"},
	} {
		supported, reason := iafetchSupported(c.name)
		if supported != c.supported || reason != c.reason {
			t.Errorf("iafetchSupported(%q) = %v/%q, want %v/%q", c.name, supported, reason, c.supported, c.reason)
		}
		if reason != "" && iafetchPending(reason) {
			t.Errorf("iafetchSupported(%q): format reason %q must not be pending", c.name, reason)
		}
	}
	for _, reason := range []string{iaReasonMissing, iaReasonSizeChanged, iaReasonStatError} {
		if !iafetchPending(reason) {
			t.Errorf("iafetchPending(%q) = false, want true", reason)
		}
	}
	if iafetchPending("") {
		t.Errorf(`iafetchPending("") = true, want false`)
	}
}

// TestMboxIAMissingDB checks that a DB that cannot be read carries the access hint, and
// that nothing is created: the DSN opens read-only, so SQLite must not make an empty DB
// where iafetch's was expected (a typo in -from-iafetch must not look like an empty mirror).
func TestMboxIAMissingDB(t *testing.T) {
	if _, err := iafetchInputs("", ""); err == nil {
		t.Errorf("empty path: no error")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "no-such.sqlite")
	inputs, err := iafetchInputs(dbPath, "")
	if err == nil {
		t.Fatalf("missing DB: no error, got %v", iaNames(inputs))
	}
	for _, part := range []string{dbPath, "-wal"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not name %q", err, part)
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			t.Errorf("%s was created by a read-only open", dbPath+suffix)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the read-only open left %d entries in the folder", len(entries))
	}
}

// TestMboxIAReadOnlyDSN pins that iafetchDSN really opens read-only: a write through it
// fails. Without mode=ro the mattn driver opens every "file:" DSN read-write and creates
// what is missing, so this is what keeps the importer off iafetch's DB.
func TestMboxIAReadOnlyDSN(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "iafetch.sqlite")
	writer := iaNewDB(t, dbPath)
	iaItem(t, writer, "item-a", 1, "done")
	path := filepath.Join(dir, "item-a", "a1.mbox.zip")
	iaFileRow(t, writer, "item-a", "a1.mbox.zip", iaWrite(t, path, 13), "aa11", path)

	db, err := sql.Open("sqlite3", iafetchDSN(dbPath))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatalf("read through the read-only DSN: %v", err)
	}
	if n != 1 {
		t.Fatalf("read %d files rows, want 1", n)
	}
	for _, stmt := range []string{
		`INSERT INTO files (identifier, name, size, md5, path, done_at) VALUES ('x', 'x.mbox', 1, '', 'x', '')`,
		`DELETE FROM files`,
		`CREATE TABLE sneaky (a INTEGER)`,
	} {
		if _, err := db.Exec(stmt); err == nil {
			t.Errorf("%s succeeded through the read-only DSN", stmt)
		}
	}
}

// TestMboxIAUriPathChars pins that a DB path with the URI characters '?' and '#' (and a
// space) reads the right database: the path is escaped, so SQLite does not read the rest of
// the path as its query string, which would drop mode=ro and open another file read-write.
func TestMboxIAUriPathChars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ia fetch?dir#1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(dir, "iafetch?v=2#old.sqlite")
	db := iaNewDB(t, dbPath)
	iaItem(t, db, "item-a", 1, "done")
	path := filepath.Join(dir, "item-a", "a1.mbox.zip")
	iaFileRow(t, db, "item-a", "a1.mbox.zip", iaWrite(t, path, 13), "aa11", path)

	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	inputs, err := iafetchInputs(dbPath, "")
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("got %d inputs, want 1: %v", len(inputs), iaNames(inputs))
	}
	if in := inputs[0]; in.Path != path || in.MD5 != "aa11" || !in.Supported {
		t.Errorf("input = %+v, want the supported file %q", in, path)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(after) != len(before) {
		var names []string
		for _, e := range after {
			names = append(names, e.Name())
		}
		t.Errorf("the folder holds %d entries after the read, had %d: %v", len(after), len(before), names)
	}
}

// Environment of the re-exec'd writer process (see TestMboxIAHelperWriter).
const (
	iaWriterEnvDB   = "PUGLEAF_TEST_IA_WRITER_DB"
	iaWriterEnvFile = "PUGLEAF_TEST_IA_WRITER_FILE"
)

// TestMboxIAHelperWriter is not a test of its own: the read-only tests re-exec the test
// binary with iaWriterEnvDB set, to play iafetch in another process. It creates the WAL
// database with one item and one file, says "ready" on stdout and then holds the database
// open until its stdin is closed (or it is killed), the way iafetch holds it open for the
// 30 h of a mirror download.
func TestMboxIAHelperWriter(t *testing.T) {
	dbPath := os.Getenv(iaWriterEnvDB)
	if dbPath == "" {
		t.Skip("helper process of the read-only tests (re-exec'd with " + iaWriterEnvDB + ")")
	}
	db := iaNewDB(t, dbPath)
	iaItem(t, db, "item-a", 1, "done")
	iaFileRow(t, db, "item-a", "a1.mbox.zip", 13, "aa11", os.Getenv(iaWriterEnvFile))
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin) // the parent closes stdin when it is done
}

// iaWriter is the re-exec'd writer process of a test.
type iaWriter struct {
	cmd   *exec.Cmd
	stdin io.Closer
}

// iaStartWriter re-execs the test binary as the cross-process writer of the WAL database at
// dbPath (TestMboxIAHelperWriter) and returns once it has created the DB and signalled
// readiness on stdout. The process is killed on test cleanup if it is still running.
func iaStartWriter(t *testing.T, dbPath, filePath string) *iaWriter {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMboxIAHelperWriter$", "-test.timeout=0")
	cmd.Env = append(os.Environ(), iaWriterEnvDB+"="+dbPath, iaWriterEnvFile+"="+filePath)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("writer stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("writer stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start writer: %v", err)
	}
	w := &iaWriter{cmd: cmd, stdin: stdin}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	ready := make(chan error, 1)
	go func() {
		br := bufio.NewReader(stdout)
		for {
			line, err := br.ReadString('\n')
			if strings.TrimSpace(line) == "ready" {
				ready <- nil
				return
			}
			if err != nil {
				ready <- fmt.Errorf("no ready signal: %w (last line %q)", err, line)
				return
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("writer: %v", err)
		}
	case <-time.After(testWait): // hang guard only: a working writer is ready at once
		t.Fatalf("writer did not become ready within %s", testWait)
	}
	return w
}

// stop closes the writer's stdin, which ends the process, and waits for it.
func (w *iaWriter) stop(t *testing.T) {
	t.Helper()
	if err := w.stdin.Close(); err != nil {
		t.Errorf("close writer stdin: %v", err)
	}
	if err := w.cmd.Wait(); err != nil {
		t.Errorf("writer exited with %v", err)
	}
}

// kill ends the writer with SIGKILL, which leaves a hot -wal and a stale -shm behind
// (iafetch killed, or the machine's OOM killer).
func (w *iaWriter) kill(t *testing.T) {
	t.Helper()
	if err := w.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill writer: %v", err)
	}
	_ = w.cmd.Wait() // "signal: killed"
}

// iaSums is the md5 of the database and of the WAL files that exist.
func iaSums(t *testing.T, dbPath string) map[string]string {
	t.Helper()
	sums := map[string]string{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			sums[suffix] = iaSum(t, dbPath+suffix)
		}
	}
	return sums
}

// iaCheckUnchanged compares the files of a database with an earlier iaSums. The database
// and its -wal hold iafetch's data and must come out byte for byte the same. The -shm is a
// volatile index, not data: measured here, a reader that may write it puts its read mark in
// it (the md5 changes), and one that may not (mode 0444) leaves it alone, so only its
// presence is checked.
func iaCheckUnchanged(t *testing.T, dbPath string, before map[string]string) {
	t.Helper()
	after := iaSums(t, dbPath)
	for _, suffix := range []string{"", "-wal"} {
		if before[suffix] != after[suffix] {
			t.Errorf("%s changed by the read: %q -> %q", dbPath+suffix, before[suffix], after[suffix])
		}
	}
	if _, ok := before["-shm"]; ok {
		if _, ok := after["-shm"]; !ok {
			t.Errorf("%s-shm was removed by the read", dbPath)
		}
	}
}

// iaReadOne reads the fixture of the writer process and checks the single input.
func iaReadOne(t *testing.T, dbPath, filePath string) {
	t.Helper()
	inputs, err := iafetchInputs(dbPath, "")
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("got %d inputs, want 1: %v", len(inputs), iaNames(inputs))
	}
	if in := inputs[0]; !in.Supported || in.Path != filePath || in.MD5 != "aa11" {
		t.Errorf("input = %+v, want the supported file %q", in, filePath)
	}
}

// iaSetup creates the mirror file and the DB folder of a read-only test.
func iaSetup(t *testing.T) (dbPath, filePath string) {
	t.Helper()
	root := t.TempDir()
	dbDir := filepath.Join(root, "iafetch")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	filePath = filepath.Join(root, "mirror", "item-a", "a1.mbox.zip")
	iaWrite(t, filePath, 13)
	return filepath.Join(dbDir, "iafetch.sqlite"), filePath
}

// TestMboxIAReadOnlyFolder reads a live WAL database in a folder the importer cannot write
// to, with another process writing it: the deployment case on arc1-ca, where iafetch's DB
// folder belongs to user mbox2nntp and go-pugleaf is only in its group. The read works
// (the -wal/-shm are there, so SQLite needs no new file) and leaves the files alone.
func TestMboxIAReadOnlyFolder(t *testing.T) {
	dbPath, filePath := iaSetup(t)
	writer := iaStartWriter(t, dbPath, filePath)

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err != nil {
			t.Fatalf("WAL database without %s: %v", suffix, err)
		}
	}
	dbDir := filepath.Dir(dbPath)
	if err := os.Chmod(dbDir, 0o500); err != nil { // r-x: no new file, no rename
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbDir, 0o700) }) // let t.TempDir clean up

	before := iaSums(t, dbPath)
	iaReadOne(t, dbPath, filePath)
	iaCheckUnchanged(t, dbPath, before)
	writer.stop(t)
}

// TestMboxIAReadOnlyShm is the same with the database, its -wal and its -shm readable only
// (iafetch's umask 022 gives group "archive" r--). A WAL reader normally wants the -shm
// read-write; SQLite falls back to a read-only shm mapping while the writer holds the
// database open, so the read works and changes nothing.
func TestMboxIAReadOnlyShm(t *testing.T) {
	dbPath, filePath := iaSetup(t)
	writer := iaStartWriter(t, dbPath, filePath)

	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	}
	dbDir := filepath.Dir(dbPath)
	if err := os.Chmod(dbDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbDir, 0o700) })

	before := iaSums(t, dbPath)
	iaReadOne(t, dbPath, filePath)
	iaCheckUnchanged(t, dbPath, before)
	writer.stop(t)
}

// TestMboxIAOrphanedWAL reads the database after the writer was killed: the -wal still
// holds the rows and the -shm is stale, which is the state iafetch leaves behind when it is
// killed. The read recovers the rows without changing the database or the -wal.
func TestMboxIAOrphanedWAL(t *testing.T) {
	dbPath, filePath := iaSetup(t)
	writer := iaStartWriter(t, dbPath, filePath)
	writer.kill(t)

	before := iaSums(t, dbPath)
	if _, ok := before["-wal"]; !ok {
		t.Fatalf("no %s-wal left by the killed writer", dbPath)
	}
	iaReadOne(t, dbPath, filePath)
	iaCheckUnchanged(t, dbPath, before)
}
