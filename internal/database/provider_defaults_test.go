package database

import (
	"testing"
)

// TestProviderDefaultsFresh: a fresh main DB gets the corrected default providers: no
// "pugleaf Archive" (81-171-22-215.pugleaf.net is gone), the BlueWorldHosting archive on
// archive.usenet.blueworldhosting.com plus the public news.blueworldhosting.com, and the
// posting flag of the tcpreset entries (LoadDefaultProviders used to drop it).
func TestProviderDefaultsFresh(t *testing.T) {
	db := w0DB(t)
	if p, _ := db.GetProviderByName("pugleaf Archive"); p != nil {
		t.Errorf("default provider pugleaf Archive (%s) still inserted", p.Host)
	}
	want := map[string]string{
		"BlueWorldHosting Archive": "archive.usenet.blueworldhosting.com",
		"BlueWorldHosting":         "news.blueworldhosting.com",
	}
	for name, host := range want {
		p, err := db.GetProviderByName(name)
		if err != nil || p == nil {
			t.Errorf("default provider %q missing: %v", name, err)
			continue
		}
		if p.Host != host {
			t.Errorf("provider %q host = %q, want %q", name, p.Host, host)
		}
	}
	for _, name := range []string{"news.tcpreset.net - Germany", "TOR: tcpreset"} {
		p, err := db.GetProviderByName(name)
		if err != nil || p == nil {
			t.Errorf("default provider %q missing: %v", name, err)
			continue
		}
		if !p.Posting {
			t.Errorf("provider %q posting = false, want true", name)
		}
	}
}

// TestProviderDefaultsMigration0035: the migration corrects the rows an older version wrote and
// leaves a provider the admin changed alone.
func TestProviderDefaultsMigration0035(t *testing.T) {
	db := w0DB(t)
	sqlText, err := EmbeddedMigrationsFS.ReadFile("migrations/0035_main_provider_defaults.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Restore the old rows (and an admin-changed one that must survive).
	for _, stmt := range []string{
		`INSERT OR IGNORE INTO providers (name, grp, host, port, ssl, max_conns, enabled, priority) VALUES ('pugleaf Archive', 'Archive', '81-171-22-215.pugleaf.net', 563, 1, 50, 0, 98)`,
		`UPDATE providers SET host = 'news.blueworldhosting.com' WHERE name = 'BlueWorldHosting Archive'`,
		`UPDATE providers SET posting = 0 WHERE name IN ('news.tcpreset.net - Germany', 'TOR: tcpreset')`,
		`UPDATE providers SET host = 'news.tcpreset.example', posting = 0 WHERE name = 'news.tcpreset.net - Germany'`,
	} {
		if _, err := db.mainDB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.mainDB.Exec(`UPDATE providers SET host = 'news.tcpreset.net', posting = 1 WHERE name = 'news.tcpreset.net - Germany'`)
	})
	if _, err := db.mainDB.Exec(string(sqlText)); err != nil {
		t.Fatalf("migration 0035: %v", err)
	}
	if p, _ := db.GetProviderByName("pugleaf Archive"); p != nil {
		t.Error("migration 0035 kept pugleaf Archive")
	}
	if p, _ := db.GetProviderByName("BlueWorldHosting Archive"); p == nil || p.Host != "archive.usenet.blueworldhosting.com" {
		t.Errorf("migration 0035: BlueWorldHosting Archive = %+v", p)
	}
	if p, _ := db.GetProviderByName("TOR: tcpreset"); p == nil || !p.Posting {
		t.Errorf("migration 0035: TOR: tcpreset posting not set: %+v", p)
	}
	if p, _ := db.GetProviderByName("news.tcpreset.net - Germany"); p == nil || p.Posting {
		t.Errorf("migration 0035 changed a provider the admin edited (other host): %+v", p)
	}
}
