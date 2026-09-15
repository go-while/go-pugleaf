package database

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// w0TestDB is shared by every test of this package: OpenDatabase may run only once per
// process (INIT guard) and it starts background goroutines, so tests must not mutate
// package globals those goroutines read (see K3 in .claude/plans/*/web-sqlite-hardening.md).
var w0TestDB *Database
var w0Seq atomic.Int64

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pugleaf-dbtest-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg := DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := OpenDatabase(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "OpenDatabase:", err)
		os.Exit(2)
	}
	w0TestDB = db
	code := m.Run()
	if code == 0 {
		_ = os.RemoveAll(dir)
	} else {
		fmt.Fprintln(os.Stderr, "test data kept in", dir)
	}
	os.Exit(code)
}

// w0DB returns the shared test database (data root is a temp dir).
func w0DB(t *testing.T) *Database {
	t.Helper()
	if w0TestDB == nil {
		t.Fatal("test DB not open")
	}
	return w0TestDB
}

// w0Name returns a unique name like "<prefix>.t42" (valid as a newsgroup name).
func w0Name(prefix string) string {
	return fmt.Sprintf("%s.t%d", prefix, w0Seq.Add(1))
}

func TestW0Harness(t *testing.T) {
	if _, err := w0DB(t).GetConfigValue("registration_enabled"); err != nil {
		t.Fatal(err)
	}
}
