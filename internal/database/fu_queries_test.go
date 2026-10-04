package database

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fuQueriesGroup inserts a newsgroup row with a unique name and returns the name.
func fuQueriesGroup(t *testing.T, active bool) string {
	t.Helper()
	name := w0Name("fuqueries")
	if _, err := RetryableExec(w0DB(t).GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 0, 0, ?, ?)",
		name, active, ExtractHierarchyFromGroupName(name)); err != nil {
		t.Fatalf("insert newsgroup %s: %v", name, err)
	}
	return name
}

// fuQueriesNewsgroupID returns newsgroups.id of name, or 0 when the row is gone.
func fuQueriesNewsgroupID(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	err := RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT id FROM newsgroups WHERE name = ?", []interface{}{name}, &id)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatalf("newsgroup id of %s: %v", name, err)
	}
	return id
}

// fuQueriesUser inserts a user row (user_spam_flags.user_id has a foreign key on users.id).
func fuQueriesUser(t *testing.T) *models.User {
	t.Helper()
	db := w0DB(t)
	name := w0Name("fuqueries_user")
	if err := db.InsertUser(&models.User{
		Username: name, Email: name + "@test.invalid", PasswordHash: "x", DisplayName: name,
	}); err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	u, err := db.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	return u
}

// fuQueriesFlagSpam writes one user_spam_flags row for (user, newsgroup id, article).
func fuQueriesFlagSpam(t *testing.T, userID, newsgroupID, articleNum int64) {
	t.Helper()
	if _, err := RetryableExec(w0DB(t).GetMainDB(),
		"INSERT INTO user_spam_flags (user_id, newsgroup_id, article_num) VALUES (?, ?, ?)",
		userID, newsgroupID, articleNum); err != nil {
		t.Fatalf("insert user_spam_flags(%d,%d,%d): %v", userID, newsgroupID, articleNum, err)
	}
}

// fuQueriesSpamFlagCount counts the user_spam_flags rows keyed on a newsgroup id.
func fuQueriesSpamFlagCount(t *testing.T, newsgroupID int64) int {
	t.Helper()
	var n int
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT COUNT(*) FROM user_spam_flags WHERE newsgroup_id = ?", []interface{}{newsgroupID}, &n); err != nil {
		t.Fatalf("count user_spam_flags of %d: %v", newsgroupID, err)
	}
	return n
}

// TestFuQueriesDeleteNewsgroupReportsDeletion: query_DeleteNewsgroup only matches
// `active = 0`, so a nil error does not mean a row went away. DeleteNewsgroup reports it
// (B3) and the admin handler needs that to stop claiming a delete that did not happen.
func TestFuQueriesDeleteNewsgroupReportsDeletion(t *testing.T) {
	db := w0DB(t)

	inactive := fuQueriesGroup(t, false)
	deleted, err := db.DeleteNewsgroup(inactive)
	if err != nil {
		t.Fatalf("DeleteNewsgroup(%s): %v", inactive, err)
	}
	if !deleted {
		t.Errorf("DeleteNewsgroup(%s) = false, want true for an inactive group", inactive)
	}
	if id := fuQueriesNewsgroupID(t, inactive); id != 0 {
		t.Errorf("newsgroup %s still exists (id %d) after the delete", inactive, id)
	}

	active := fuQueriesGroup(t, true)
	deleted, err = db.DeleteNewsgroup(active)
	if err != nil {
		t.Fatalf("DeleteNewsgroup(%s): %v", active, err)
	}
	if deleted {
		t.Errorf("DeleteNewsgroup(%s) = true, want false while the group is active", active)
	}
	if id := fuQueriesNewsgroupID(t, active); id == 0 {
		t.Errorf("active newsgroup %s was deleted", active)
	}

	// An unknown name is not an error either, and it deleted nothing.
	deleted, err = db.DeleteNewsgroup(w0Name("fuqueries.absent"))
	if err != nil {
		t.Fatalf("DeleteNewsgroup(absent): %v", err)
	}
	if deleted {
		t.Error("DeleteNewsgroup(absent) = true, want false")
	}
}

// TestFuQueriesDeleteNewsgroupRemovesUserSpamFlags: user_spam_flags is keyed on
// newsgroup_id and migration 0007 puts a foreign key on user_id only, so nothing cascades
// when the newsgroup goes. The rows must be deleted in the same transaction (A4), and only
// for a group that was really deleted.
func TestFuQueriesDeleteNewsgroupRemovesUserSpamFlags(t *testing.T) {
	db := w0DB(t)
	user := fuQueriesUser(t)

	gone := fuQueriesGroup(t, false)
	kept := fuQueriesGroup(t, true)
	goneID := fuQueriesNewsgroupID(t, gone)
	keptID := fuQueriesNewsgroupID(t, kept)
	if goneID == 0 || keptID == 0 {
		t.Fatalf("precondition: newsgroup ids %d and %d", goneID, keptID)
	}
	for _, num := range []int64{1, 2} {
		fuQueriesFlagSpam(t, user.ID, goneID, num)
		fuQueriesFlagSpam(t, user.ID, keptID, num)
	}

	deleted, err := db.DeleteNewsgroup(gone)
	if err != nil || !deleted {
		t.Fatalf("DeleteNewsgroup(%s) = %v, %v; want true, nil", gone, deleted, err)
	}
	if n := fuQueriesSpamFlagCount(t, goneID); n != 0 {
		t.Errorf("user_spam_flags rows of deleted newsgroup %d = %d, want 0", goneID, n)
	}
	if n := fuQueriesSpamFlagCount(t, keptID); n != 2 {
		t.Errorf("user_spam_flags rows of %d = %d, want 2 (another group's flags)", keptID, n)
	}

	// The active group is refused by the DELETE, so its flags must survive untouched.
	deleted, err = db.DeleteNewsgroup(kept)
	if err != nil {
		t.Fatalf("DeleteNewsgroup(%s): %v", kept, err)
	}
	if deleted {
		t.Fatalf("DeleteNewsgroup(%s) = true, want false while active", kept)
	}
	if n := fuQueriesSpamFlagCount(t, keptID); n != 2 {
		t.Errorf("user_spam_flags rows of the active %d = %d, want 2", keptID, n)
	}
}

// TestFuQueriesSectionGroupsStrictListing: the strict listing drops the members a visitor
// cannot open - an inactive newsgroup and a section_groups row orphaned by a delete - while
// keeping the active ones and the category headers, which have no newsgroup of their own.
// The non-strict listing keeps everything, for the admin view (E1).
func TestFuQueriesSectionGroupsStrictListing(t *testing.T) {
	db := w0DB(t)
	section := &models.Section{
		Name:        w0Name("fuqueriessec"),
		DisplayName: "Fu queries",
		CreatedAt:   time.Now(),
	}
	if err := db.CreateSection(section); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}

	activeGroup := fuQueriesGroup(t, true)
	inactiveGroup := fuQueriesGroup(t, false)
	orphan := w0Name("fuqueries.orphan") // no newsgroups row at all
	header := w0Name("fuqueries.header") // category header, never a newsgroup

	for _, m := range []struct {
		name     string
		isHeader bool
	}{
		{activeGroup, false},
		{inactiveGroup, false},
		{orphan, false},
		{header, true},
	} {
		if err := db.CreateSectionGroup(&models.SectionGroup{
			SectionID: section.ID, NewsgroupName: m.name, IsCategoryHeader: m.isHeader,
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("CreateSectionGroup %s: %v", m.name, err)
		}
	}

	names := func(groups []*models.SectionGroup) map[string]bool {
		out := make(map[string]bool, len(groups))
		for _, g := range groups {
			out[g.NewsgroupName] = true
		}
		return out
	}

	// Both sort orders, because the two queries share the ORDER BY %s and the "activity"
	// one sorts on the joined newsgroups table.
	for _, sortBy := range []string{"activity", "name"} {
		all, err := db.GetSectionGroupsWithActivity(section.ID, sortBy)
		if err != nil {
			t.Fatalf("GetSectionGroupsWithActivity(%s): %v", sortBy, err)
		}
		got := names(all)
		for _, want := range []string{activeGroup, inactiveGroup, orphan, header} {
			if !got[want] {
				t.Errorf("sort=%s: the full listing is missing %s", sortBy, want)
			}
		}

		strict, err := db.GetSectionGroupsWithActivityStrict(section.ID, sortBy)
		if err != nil {
			t.Fatalf("GetSectionGroupsWithActivityStrict(%s): %v", sortBy, err)
		}
		got = names(strict)
		if !got[activeGroup] {
			t.Errorf("sort=%s: the strict listing dropped the active group %s", sortBy, activeGroup)
		}
		if !got[header] {
			t.Errorf("sort=%s: the strict listing dropped the category header %s", sortBy, header)
		}
		if got[inactiveGroup] {
			t.Errorf("sort=%s: the strict listing kept the inactive group %s", sortBy, inactiveGroup)
		}
		if got[orphan] {
			t.Errorf("sort=%s: the strict listing kept the orphan row %s", sortBy, orphan)
		}
		if len(strict) != 2 {
			t.Errorf("sort=%s: strict listing has %d rows, want 2", sortBy, len(strict))
		}
	}

	// A group deleted through DeleteNewsgroup loses its section_groups row, so it leaves
	// neither listing - the orphans are only the rows from before that fix.
	if deleted, err := db.DeleteNewsgroup(inactiveGroup); err != nil || !deleted {
		t.Fatalf("DeleteNewsgroup(%s) = %v, %v; want true, nil", inactiveGroup, deleted, err)
	}
	all, err := db.GetSectionGroupsWithActivity(section.ID, "name")
	if err != nil {
		t.Fatalf("GetSectionGroupsWithActivity after the delete: %v", err)
	}
	if names(all)[inactiveGroup] {
		t.Errorf("%s is still listed after DeleteNewsgroup removed its section_groups row", inactiveGroup)
	}
}

// fuQueriesBusyDB returns an isolated Database whose main DB has busy_timeout = 0, plus a
// second pool on the same file to hold the write lock with. With busy_timeout = 0 a write
// that waits for another writer fails at once instead of blocking for 30s, which is what
// makes the retry in ResetAllNewsgroupData observable in a test.
//
// ResetAllNewsgroupData touches every newsgroup row, so per K3 it never runs against the
// shared w0DB.
func fuQueriesBusyDB(t *testing.T) (*Database, *sql.DB) {
	t.Helper()
	shared := w0DB(t)
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "fuqueries-main.sq3") +
		"?_busy_timeout=0&_journal_mode=WAL&_foreign_keys=on"

	pool, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open main DB: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	cfg := *shared.dbconfig
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	fresh := &Database{mainDB: pool, dbconfig: &cfg, groupDB: make(map[string]*GroupDB)}
	if err := fresh.migrateMainDB(); err != nil {
		t.Fatalf("main migrations: %v", err)
	}

	blocker, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open blocker: %v", err)
	}
	t.Cleanup(func() { blocker.Close() })
	return fresh, blocker
}

// TestFuQueriesResetAllWaitsForABusyMainDB: step 1 of ResetAllNewsgroupData is a mass
// counter UPDATE. It used a bare db.mainDB.Exec, which gives up on the first SQLITE_BUSY;
// routed through RetryableExec it waits for the other writer instead (A2).
func TestFuQueriesResetAllWaitsForABusyMainDB(t *testing.T) {
	db, blocker := fuQueriesBusyDB(t)

	group := w0Name("fuqueries.reset")
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: group, Active: true, Status: "y"}); err != nil {
		t.Fatalf("InsertNewsgroup: %v", err)
	}
	if _, err := db.mainDB.Exec(`UPDATE newsgroups SET message_count = 7, last_article = 7, high_water = 7, low_water = 3`); err != nil {
		t.Fatalf("seed counters: %v", err)
	}

	// Take the write lock of the main DB on another connection.
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("blocker Begin: %v", err)
	}
	rolledBack := false
	defer func() {
		if !rolledBack {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Exec(`UPDATE newsgroups SET description = 'blocked' WHERE name = ?`, group); err != nil {
		t.Fatalf("blocker write: %v", err)
	}

	// Precondition: with busy_timeout = 0 an un-retried write really does fail now. This is
	// exactly what step 1 did before A2, so the assertion below has something to prove.
	if _, err := db.mainDB.Exec(`UPDATE newsgroups SET message_count = 0`); err == nil {
		t.Fatal("precondition: a bare Exec succeeded while another connection held the write lock")
	} else if !isRetryableError(err) {
		t.Fatalf("precondition: bare Exec failed with %v, want a busy/locked error", err)
	}

	done := make(chan error, 1)
	go func() { done <- db.ResetAllNewsgroupData() }()

	// It must still be retrying, not back with an error.
	select {
	case err := <-done:
		t.Fatalf("ResetAllNewsgroupData returned %v while the main DB write lock was held", err)
	case <-time.After(150 * time.Millisecond):
	}

	rolledBack = true
	if err := tx.Rollback(); err != nil {
		t.Fatalf("blocker Rollback: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ResetAllNewsgroupData: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ResetAllNewsgroupData did not return after the write lock was released")
	}

	var left int
	if err := RetryableQueryRowScan(db.mainDB,
		`SELECT COUNT(*) FROM newsgroups WHERE message_count != 0 OR last_article != 0 OR high_water != 0 OR low_water != 1`,
		nil, &left); err != nil {
		t.Fatalf("count counters: %v", err)
	}
	if left != 0 {
		t.Errorf("%d newsgroups still have non-reset counters", left)
	}
}
