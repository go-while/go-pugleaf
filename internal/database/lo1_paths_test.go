package database

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// lo1PathsFreshDB returns an isolated *Database with its own main DB and data root.
// ResetAllNewsgroupData touches every newsgroup, so it must never run against the
// shared w0DB (see K3); the pattern follows TestW1SQLiteShutdownDuringInit.
func lo1PathsFreshDB(t *testing.T) *Database {
	t.Helper()
	shared := w0DB(t)
	cfg := *shared.dbconfig
	cfg.DataDir = t.TempDir()
	cfg.BackupDir = filepath.Join(cfg.DataDir, "backups")
	fresh := &Database{
		dbconfig: &cfg,
		groupDB:  make(map[string]*GroupDB),
		WG:       &sync.WaitGroup{},
		Batch:    shared.Batch,
	}
	if err := fresh.initMainDB(); err != nil {
		t.Fatalf("initMainDB: %v", err)
	}
	if err := fresh.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() {
		if err := fresh.Shutdown(); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	})
	return fresh
}

// lo1PathsSeedArticle creates the group DB (if needed) and inserts one article row.
func lo1PathsSeedArticle(t *testing.T, db *Database, group string, num int64) {
	t.Helper()
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer gdb.Return()
	if _, err := gdb.DB.Exec(
		`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, path, headers_json, body_text)
		 VALUES (?, ?, 'lo1paths', 'tester@lo1.invalid', CURRENT_TIMESTAMP, '', '', 1, 1, 'not-for-mail', '', 'body')`,
		num, "<lo1paths."+group+"."+SanitizeGroupName(group)+"@lo1.invalid>",
	); err != nil {
		t.Fatalf("insert article into %s: %v", group, err)
	}
	if _, err := gdb.DB.Exec(`INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, NULL, ?, 0, 0)`, num, num); err != nil {
		t.Fatalf("insert thread into %s: %v", group, err)
	}
}

func lo1PathsCount(t *testing.T, db *Database, group string, table string) int64 {
	t.Helper()
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer gdb.Return()
	var n int64
	if err := gdb.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s in %s: %v", table, group, err)
	}
	return n
}

// ResetAllNewsgroupData must find the group databases in the real layout
// (<data>/db/<md5>/<sanitized>.db), and it must not create a database for a
// newsgroup that has none.
func TestLo1PathsResetAllUsesLayout(t *testing.T) {
	db := lo1PathsFreshDB(t)

	withDB1 := w0Name("lo1paths.reset.a")
	withDB2 := w0Name("lo1paths.reset.b")
	noDB := w0Name("lo1paths.reset.none")
	for _, name := range []string{withDB1, withDB2, noDB} {
		if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Active: true, Status: "y"}); err != nil {
			t.Fatalf("InsertNewsgroup(%s): %v", name, err)
		}
	}

	lo1PathsSeedArticle(t, db, withDB1, 1)
	lo1PathsSeedArticle(t, db, withDB2, 2)

	// Non-zero counters that the reset must clear.
	if _, err := db.mainDB.Exec(`UPDATE newsgroups SET message_count = 7, last_article = 7, high_water = 7, low_water = 3`); err != nil {
		t.Fatal(err)
	}

	// The third group has no database file.
	if db.groupDBFileExists(noDB) {
		t.Fatalf("group DB file for %s exists before the reset", noDB)
	}
	noDBdir := filepath.Join(db.GetDataDir(), "db", MD5Hash(noDB))

	if err := db.ResetAllNewsgroupData(); err != nil {
		t.Fatalf("ResetAllNewsgroupData: %v", err)
	}

	for _, name := range []string{withDB1, withDB2} {
		if n := lo1PathsCount(t, db, name, "articles"); n != 0 {
			t.Fatalf("%s: %d articles after the reset, want 0", name, n)
		}
		if n := lo1PathsCount(t, db, name, "threads"); n != 0 {
			t.Fatalf("%s: %d threads after the reset, want 0", name, n)
		}
	}

	var counters int
	if err := db.mainDB.QueryRow(
		`SELECT COUNT(*) FROM newsgroups WHERE message_count != 0 OR last_article != 0 OR high_water != 0 OR low_water != 1`,
	).Scan(&counters); err != nil {
		t.Fatal(err)
	}
	if counters != 0 {
		t.Fatalf("%d newsgroups still have non-reset counters", counters)
	}

	if db.groupDBFileExists(noDB) {
		t.Fatalf("ResetAllNewsgroupData created a group DB for %s, which had none", noDB)
	}
	if _, err := os.Stat(noDBdir); err == nil {
		t.Fatalf("ResetAllNewsgroupData created the directory %s", noDBdir)
	}
	if _, err := os.Stat(filepath.Join(db.GetDataDir(), "groups")); err == nil {
		t.Fatalf("a <data>/groups directory was created; the layout is <data>/db/<md5>/")
	}
}

// A newsgroup list without any group DB file leaves the group databases alone
// and still resets the main counters.
func TestLo1PathsResetAllWithoutGroupDBs(t *testing.T) {
	db := lo1PathsFreshDB(t)

	name := w0Name("lo1paths.empty")
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Active: true, Status: "y"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.mainDB.Exec(`UPDATE newsgroups SET message_count = 5, last_article = 5`); err != nil {
		t.Fatal(err)
	}

	if err := db.ResetAllNewsgroupData(); err != nil {
		t.Fatalf("ResetAllNewsgroupData: %v", err)
	}

	var mc, la int64
	if err := db.mainDB.QueryRow(`SELECT message_count, last_article FROM newsgroups WHERE name = ?`, name).Scan(&mc, &la); err != nil {
		t.Fatal(err)
	}
	if mc != 0 || la != 0 {
		t.Fatalf("counters after reset: message_count=%d last_article=%d, want 0 and 0", mc, la)
	}
	if db.groupDBFileExists(name) {
		t.Fatalf("a group DB was created for %s", name)
	}
}

// A6: SanitizeGroupName maps "a.b" and "a_b" to the same file name, but the
// directory is MD5Hash of the real name, so the two databases never collide.
func TestLo1PathsSanitizedNamesDoNotCollide(t *testing.T) {
	db := w0DB(t)
	base := w0Name("lo1paths.collide")
	dotted := base + ".x.y"
	scored := base + ".x_y"

	if SanitizeGroupName(dotted) != SanitizeGroupName(scored) {
		t.Fatalf("precondition: %q and %q sanitize differently (%q vs %q)",
			dotted, scored, SanitizeGroupName(dotted), SanitizeGroupName(scored))
	}
	if MD5Hash(dotted) == MD5Hash(scored) {
		t.Fatalf("MD5Hash collision for %q and %q", dotted, scored)
	}

	gdb1, err := db.GetGroupDB(dotted)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", dotted, err)
	}
	defer gdb1.Return()
	gdb2, err := db.GetGroupDB(scored)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", scored, err)
	}
	defer gdb2.Return()

	if _, err := gdb1.DB.Exec(
		`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, path, headers_json, body_text)
		 VALUES (1, '<lo1paths.collide.1@lo1.invalid>', 'dotted', 'a@b', CURRENT_TIMESTAMP, '', '', 1, 1, 'not-for-mail', '', 'b')`,
	); err != nil {
		t.Fatalf("insert into %s: %v", dotted, err)
	}

	// The second database must be independent: the row above must not be visible.
	var n int64
	if err := gdb2.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%s sees %d articles of %s: the two group DBs share a file", scored, n, dotted)
	}

	dir1 := filepath.Join(db.GetDataDir(), "db", MD5Hash(dotted))
	dir2 := filepath.Join(db.GetDataDir(), "db", MD5Hash(scored))
	if dir1 == dir2 {
		t.Fatalf("both groups use the directory %s", dir1)
	}
	for _, f := range []string{
		filepath.Join(dir1, SanitizeGroupName(dotted)+".db"),
		filepath.Join(dir2, SanitizeGroupName(scored)+".db"),
	} {
		if !FileExists(f) {
			t.Fatalf("group DB file %s does not exist", f)
		}
	}
}
