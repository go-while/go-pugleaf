package database

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// badBotsNew are the patterns migration 0038 appends.
var badBotsNew = []string{"anticlone", "censys", "claudebot", "infrawatch", "twitterbot"}

// badBotsCount counts each pattern of the comma list.
func badBotsCount(list string) map[string]int {
	n := make(map[string]int)
	for _, p := range strings.Split(list, ",") {
		n[strings.ToLower(strings.TrimSpace(p))]++
	}
	return n
}

// TestBadBotsMigration0038: a fresh main DB gets the 0023 list plus the five new patterns, each
// once; running the 0038 SQL on a list that already holds some of them (an admin added them by
// hand, any case or spacing) or on an empty list adds only the missing ones.
func TestBadBotsMigration0038(t *testing.T) {
	shared := w0DB(t)
	mainDB, err := sql.Open(driverNameMain, filepath.Join(t.TempDir(), "main.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	fresh := &Database{mainDB: mainDB, dbconfig: shared.dbconfig}
	if err := fresh.migrateMainDB(); err != nil {
		t.Fatalf("main migrations: %v", err)
	}
	var list string
	if err := mainDB.QueryRow(`SELECT value FROM config WHERE key = 'BadBots'`).Scan(&list); err != nil {
		t.Fatalf("read BadBots: %v", err)
	}
	n := badBotsCount(list)
	for _, p := range append([]string{"acunetix", "census", "wget"}, badBotsNew...) {
		if n[p] != 1 {
			t.Errorf("fresh BadBots holds %q %d times, want once: %q", p, n[p], list)
		}
	}

	sqlText, err := EmbeddedMigrationsFS.ReadFile("migrations/0038_main_bad_bots_2026_11.sql")
	if err != nil {
		t.Fatalf("read migration 0038: %v", err)
	}
	for _, tc := range []struct{ name, before string }{
		{"some by hand", "curl, ClaudeBot,wget,censys"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mainDB.Exec(`UPDATE config SET value = ? WHERE key = 'BadBots'`, tc.before); err != nil {
				t.Fatal(err)
			}
			if _, err := mainDB.Exec(string(sqlText)); err != nil {
				t.Fatalf("run 0038: %v", err)
			}
			var after string
			if err := mainDB.QueryRow(`SELECT value FROM config WHERE key = 'BadBots'`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			n := badBotsCount(after)
			for _, p := range badBotsNew {
				if n[p] != 1 {
					t.Errorf("after 0038 %q is there %d times, want once: %q", p, n[p], after)
				}
			}
			if n[""] != 0 {
				t.Errorf("after 0038 the list has an empty entry: %q", after)
			}
		})
	}
}
