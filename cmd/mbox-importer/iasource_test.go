package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// iaNewDB creates an iafetch DB at path in WAL mode and returns the open writer: keeping it
// open is what iafetch does while it downloads, and it keeps the -wal/-shm files on disk,
// so the reader is exercised against a live WAL database.
func iaNewDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=10000")
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
func iaFileRow(t *testing.T, db *sql.DB, identifier, name string, size int64, md5, path string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO files (identifier, name, size, md5, path, done_at)
		VALUES (?, ?, ?, ?, ?, '2026-11-06T00:00:00Z')`, identifier, name, size, md5, path); err != nil {
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
	// 4. iafetch had no size for the file (0): the disk size is not compared.
	nosize := filepath.Join(dir, "item-a", "a4.mbox.zip")
	iaWrite(t, nosize, 9)
	iaFileRow(t, db, "item-a", "a4.mbox.zip", 0, "aa44", nosize)
	// 5. an unsupported file that is missing keeps its suffix reason.
	iaFileRow(t, db, "item-a", "a5.iso", 100, "aa55", filepath.Join(dir, "item-a", "a5.iso"))

	inputs, err := iafetchInputs(dbPath, "")
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}
	for _, c := range []struct {
		name      string
		supported bool
		reason    string
		mtime     bool // true: a modification time is expected
	}{
		{"a1.mbox.zip", false, iaReasonMissing, false},
		{"a2.mbox.gz", false, iaReasonSizeChanged, true},
		{"a3.mbox.zip", true, "", true},
		{"a4.mbox.zip", true, "", true},
		{"a5.iso", false, "iso", false},
	} {
		in := iaFind(t, inputs, "item-a", c.name)
		if in.Supported != c.supported || in.Reason != c.reason {
			t.Errorf("%s: supported=%v reason=%q, want %v/%q", c.name, in.Supported, in.Reason, c.supported, c.reason)
		}
		if (in.Mtime != 0) != c.mtime {
			t.Errorf("%s: Mtime = %d, want set=%v", c.name, in.Mtime, c.mtime)
		}
		if in.Size != 100 && in.Size != 42 && in.Size != 0 {
			t.Errorf("%s: Size = %d, want the recorded size", c.name, in.Size)
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
	} {
		supported, reason := iafetchSupported(c.name)
		if supported != c.supported || reason != c.reason {
			t.Errorf("iafetchSupported(%q) = %v/%q, want %v/%q", c.name, supported, reason, c.supported, c.reason)
		}
	}
}

// TestMboxIAMissingDB checks that a DB that cannot be read carries the access hint.
func TestMboxIAMissingDB(t *testing.T) {
	if _, err := iafetchInputs("", ""); err == nil {
		t.Errorf("empty path: no error")
	}
	dbPath := filepath.Join(t.TempDir(), "no-such.sqlite")
	inputs, err := iafetchInputs(dbPath, "")
	if err == nil {
		t.Fatalf("missing DB: no error, got %v", iaNames(inputs))
	}
	for _, part := range []string{dbPath, "-wal"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not name %q", err, part)
		}
	}
}

// TestMboxIAReadOnlyFolder pins what SQLite does with a live WAL database in a folder the
// reader cannot write to (iafetch's /tank0/archive.org/.iafetch on arc1-ca): either the
// read works, or the error carries the access hint. The -wal/-shm files are present
// (the writer is still open), which is what makes the read possible at all.
func TestMboxIAReadOnlyFolder(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "iafetch")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(dbDir, "iafetch.sqlite")
	db := iaNewDB(t, dbPath)
	iaItem(t, db, "item-a", 1, "done")
	path := filepath.Join(root, "mirror", "item-a", "a1.mbox.zip")
	iaFileRow(t, db, "item-a", "a1.mbox.zip", iaWrite(t, path, 13), "aa11", path)

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err != nil {
			t.Fatalf("WAL database without %s: %v", suffix, err)
		}
	}
	if err := os.Chmod(dbDir, 0o500); err != nil { // r-x: no new files, no rename
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbDir, 0o700) }) // let t.TempDir clean up

	inputs, err := iafetchInputs(dbPath, "")
	if err != nil {
		if !strings.Contains(err.Error(), "-wal/-shm") {
			t.Errorf("error without the access hint: %v", err)
		}
		t.Logf("read-only folder: refused with %v", err)
		return
	}
	t.Logf("read-only folder: read %d inputs", len(inputs))
	if len(inputs) != 1 {
		t.Fatalf("got %d inputs, want 1: %v", len(inputs), iaNames(inputs))
	}
	if in := inputs[0]; !in.Supported || in.Path != path || in.MD5 != "aa11" {
		t.Errorf("input = %+v, want the supported file %q", in, path)
	}
}
