package main

// Tests of the audit-delta-fixes slice adf-opt-ops-tools (A-ops-8): a named -group is
// looked up in the main database, so an unknown name never creates a group database.
// The database lives below t.TempDir(); ./data is never used.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adfOptOpenDB opens a scratch main database below t.TempDir() and tears it down at the
// end of the test (close StopChan, WG.Wait, Shutdown). ftaExit is never called: it ends
// the process.
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

func TestAdfOptFtaResolveNamedGroups(t *testing.T) {
	db := adfOptOpenDB(t)
	known := "adf.opt.fta.known"
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: known, Active: true}); err != nil {
		t.Fatalf("InsertNewsgroup: %v", err)
	}
	found, unknown, err := resolveNamedGroups(db, []string{known, "adf.opt.fta.tpyo"})
	if err != nil {
		t.Fatalf("resolveNamedGroups: %v", err)
	}
	if len(found) != 1 || found[0].Name != known || found[0].ID == 0 {
		t.Fatalf("found %v, want only %s with its ID", found, known)
	}
	if len(unknown) != 1 || unknown[0] != "adf.opt.fta.tpyo" {
		t.Fatalf("unknown %v, want [adf.opt.fta.tpyo]", unknown)
	}
	entries, err := os.ReadDir(filepath.Join(db.GetDataDir(), "db"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("group database directories were created: %v", entries)
	}
}
