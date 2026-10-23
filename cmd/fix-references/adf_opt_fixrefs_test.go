package main

// Tests of the audit-delta-fixes slice adf-opt-ops-tools (A-ops-7, A-ops-8): usage and
// dry-run texts, and the main-DB lookup of a named -group. The database lives below
// t.TempDir(); ./data is never used.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adfOptOpenDB opens a scratch main database below t.TempDir() and tears it down at the
// end of the test (close StopChan, WG.Wait, Shutdown). fixRefsExit is never called: it
// ends the process.
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

func TestAdfOptFixRefsUsageText(t *testing.T) {
	usage, example := usageText("fix-references")
	for _, s := range []string{usage, example} {
		if strings.Contains(s, "-db ") {
			t.Fatalf("text names the undefined -db flag: %q", s)
		}
		if !strings.Contains(s, "-data ") {
			t.Fatalf("text does not name -data: %q", s)
		}
		if !strings.Contains(s, "-group ") {
			t.Fatalf("text does not name -group: %q", s)
		}
	}
}

func TestAdfOptFixRefsDryRunHint(t *testing.T) {
	hint := dryRunHint()
	if !strings.Contains(hint, "-dry-run=false") {
		t.Fatalf("hint %q does not say -dry-run=false", hint)
	}
	if strings.Contains(hint, "without -dry-run") {
		t.Fatalf("hint %q still says without -dry-run (it defaults to true)", hint)
	}
}

func TestAdfOptFixRefsAppVersionVar(t *testing.T) {
	// build_fix-references.sh passes -X main.appVersion: the variable must exist and
	// default to the "-unset-" marker of the other tools.
	if appVersion != "-unset-" {
		t.Fatalf("appVersion = %q, want -unset- in a test build", appVersion)
	}
}

func TestAdfOptFixRefsResolveNamedGroups(t *testing.T) {
	db := adfOptOpenDB(t)
	known := "adf.opt.fixrefs.known"
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: known, Active: true, ExpiryDays: 9}); err != nil {
		t.Fatalf("InsertNewsgroup: %v", err)
	}
	found, unknown, err := resolveNamedGroups(db, []string{known, "adf.opt.fixrefs.tpyo"})
	if err != nil {
		t.Fatalf("resolveNamedGroups: %v", err)
	}
	if len(found) != 1 || found[0].Name != known || found[0].ID == 0 || found[0].ExpiryDays != 9 {
		t.Fatalf("found %v, want only %s with ID and ExpiryDays 9", found, known)
	}
	if len(unknown) != 1 || unknown[0] != "adf.opt.fixrefs.tpyo" {
		t.Fatalf("unknown %v, want [adf.opt.fixrefs.tpyo]", unknown)
	}
	entries, err := os.ReadDir(filepath.Join(db.GetDataDir(), "db"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("group database directories were created: %v", entries)
	}
}
