package main

// Tests of the audit-delta-fixes slice adf-opt-ops-tools (A-ops-8): the -group names
// (one name or a comma list) are looked up in the main database, so an unknown name
// never creates a group database. The database lives below t.TempDir(); ./data is never
// used, and recoverExit (it ends the process) is never called.

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adfOptOpenDB opens a scratch main database below t.TempDir() and tears it down at the
// end of the test (close StopChan, WG.Wait, Shutdown).
func adfOptOpenDB(t *testing.T) *database.Database {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		t.Skip("OpenDatabase refuses to run with euid < 1000")
	}
	dir := t.TempDir()
	database.NO_CACHE_BOOT = true
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() {
		close(db.StopChan)
		db.WG.Wait()
		if err := db.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return db
}

func TestAdfOptRecoverSplitGroupNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{in: "a.b", want: []string{"a.b"}},
		{in: "a.b,c.d", want: []string{"a.b", "c.d"}},
		{in: " a.b , ,c.d,", want: []string{"a.b", "c.d"}},
		{in: ",", want: nil},
	}
	for _, tc := range cases {
		if got := splitGroupNames(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("splitGroupNames(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAdfOptRecoverResolveNamedGroups(t *testing.T) {
	db := adfOptOpenDB(t)
	for _, name := range []string{"adf.opt.recover.a", "adf.opt.recover.b"} {
		if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Active: true, MaxArticles: 4}); err != nil {
			t.Fatalf("InsertNewsgroup %s: %v", name, err)
		}
	}
	found, unknown, err := resolveNamedGroups(db, splitGroupNames("adf.opt.recover.a, adf.opt.recover.tpyo,adf.opt.recover.b"))
	if err != nil {
		t.Fatalf("resolveNamedGroups: %v", err)
	}
	if len(found) != 2 || found[0].Name != "adf.opt.recover.a" || found[1].Name != "adf.opt.recover.b" {
		t.Fatalf("found %v, want adf.opt.recover.a and adf.opt.recover.b", found)
	}
	for _, g := range found {
		if g.ID == 0 || g.MaxArticles != 4 {
			t.Fatalf("found %+v, want the main-DB row (ID set, MaxArticles 4)", *g)
		}
	}
	if !reflect.DeepEqual(unknown, []string{"adf.opt.recover.tpyo"}) {
		t.Fatalf("unknown %v, want [adf.opt.recover.tpyo]", unknown)
	}
	entries, err := os.ReadDir(filepath.Join(db.GetDataDir(), "db"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("group database directories were created: %v", entries)
	}
}
