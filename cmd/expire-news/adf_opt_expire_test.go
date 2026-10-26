package main

// Tests of the audit-delta-fixes slice adf-opt-ops-tools (A-ops-6): the single-group
// path of getNewsgroupsToExpire returns the group's expiry_days and max_articles.
// The database lives below t.TempDir(); ./data is never used. shutdown() is guarded
// by a package-level sync.Once and must not be called here: the test tears the
// database down itself.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adfOptExpireDB opens a scratch main database and tears it down at the end of the test.
func adfOptExpireDB(t *testing.T) *database.Database {
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

func TestAdfOptExpireSingleGroupSettings(t *testing.T) {
	db := adfOptExpireDB(t)
	for _, g := range []*models.Newsgroup{
		{Name: "adf.opt.expire.on", Active: true, ExpiryDays: 7, MaxArticles: 5},
		{Name: "adf.opt.expire.off", Active: false, ExpiryDays: 3, MaxArticles: 2},
	} {
		if err := db.InsertNewsgroup(g); err != nil {
			t.Fatalf("InsertNewsgroup %s: %v", g.Name, err)
		}
	}

	got, err := getNewsgroupsToExpire(db, "adf.opt.expire.on")
	if err != nil {
		t.Fatalf("getNewsgroupsToExpire: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d groups, want 1", len(got))
	}
	if got[0].Name != "adf.opt.expire.on" || got[0].ExpiryDays != 7 || got[0].MaxArticles != 5 || got[0].ID == 0 {
		t.Fatalf("got %+v, want name adf.opt.expire.on, ExpiryDays 7, MaxArticles 5, ID set", *got[0])
	}

	// the wildcard path returns the same settings
	wild, err := getNewsgroupsToExpire(db, "adf.opt.expire.*")
	if err != nil {
		t.Fatalf("getNewsgroupsToExpire wildcard: %v", err)
	}
	if len(wild) != 1 || wild[0].ExpiryDays != 7 || wild[0].MaxArticles != 5 {
		t.Fatalf("wildcard got %v, want only adf.opt.expire.on with 7/5", wild)
	}

	if _, err := getNewsgroupsToExpire(db, "adf.opt.expire.off"); err == nil {
		t.Fatalf("inactive group: want error")
	}
	if _, err := getNewsgroupsToExpire(db, "adf.opt.expire.missing"); err == nil {
		t.Fatalf("missing group: want error")
	}
	// the lookups never open (and so never create) a group database
	entries, err := os.ReadDir(filepath.Join(db.GetDataDir(), "db"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("group database directories were created: %v", entries)
	}
}
