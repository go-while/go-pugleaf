package database

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fuCleanupLogs collects log output so a test can assert that a specific line really fired.
// Writes come from the goroutine under test, so every access takes the mutex.
type fuCleanupLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *fuCleanupLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// contains reports whether some single captured line holds every substring. Matching per
// line, not across the whole buffer, so an unrelated background goroutine's retry line cannot
// combine with a different line to satisfy the assertion by coincidence.
func (l *fuCleanupLogs) contains(subs ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(l.buf.String(), "\n") {
		all := true
		for _, s := range subs {
			if !strings.Contains(line, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// fuCleanupCaptureLog sends log output to a buffer for the duration of one test and restores
// the previous writer afterwards. It mutates the global logger, so no test using it may run
// with t.Parallel.
func fuCleanupCaptureLog(t *testing.T) *fuCleanupLogs {
	t.Helper()
	l := &fuCleanupLogs{}
	old := log.Writer()
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(old) })
	return l
}

// fuCleanupSection creates a section and returns it.
func fuCleanupSection(t *testing.T) *models.Section {
	t.Helper()
	section := &models.Section{
		Name:        w0Name("fucleanupsec"),
		DisplayName: "Fu cleanup",
		CreatedAt:   time.Now(),
	}
	if err := w0DB(t).CreateSection(section); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	return section
}

// fuCleanupSectionMember puts name into section as an ordinary (non-header) member.
func fuCleanupSectionMember(t *testing.T, sectionID int, name string) {
	t.Helper()
	if err := w0DB(t).CreateSectionGroup(&models.SectionGroup{
		SectionID: sectionID, NewsgroupName: name, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateSectionGroup %s: %v", name, err)
	}
}

// fuCleanupSectionGroupCount counts the section_groups rows keyed on a newsgroup name.
func fuCleanupSectionGroupCount(t *testing.T, name string) int {
	t.Helper()
	var n int
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT COUNT(*) FROM section_groups WHERE newsgroup_name = ?", []interface{}{name}, &n); err != nil {
		t.Fatalf("count section_groups of %s: %v", name, err)
	}
	return n
}

// TestFuCleanupBulkDeleteRemovesDependentRows: BulkDeleteNewsgroups used to be a bare
// `DELETE FROM newsgroups ... AND active = 0`, so bulk-deleting an inactive section member
// left its section_groups row behind - still listed in the admin section view, where it
// 404s - and its user_spam_flags rows forever. It now removes the same dependent rows as
// DeleteNewsgroup, for exactly the groups the newsgroups DELETE really removes (E9).
func TestFuCleanupBulkDeleteRemovesDependentRows(t *testing.T) {
	db := w0DB(t)
	user := fuQueriesUser(t)
	section := fuCleanupSection(t)

	gone := fuQueriesGroup(t, false)  // inactive: really deleted
	kept := fuQueriesGroup(t, true)   // active: refused by the DELETE
	other := fuQueriesGroup(t, false) // inactive, but not named in the call
	goneID := fuQueriesNewsgroupID(t, gone)
	keptID := fuQueriesNewsgroupID(t, kept)
	otherID := fuQueriesNewsgroupID(t, other)
	if goneID == 0 || keptID == 0 || otherID == 0 {
		t.Fatalf("precondition: newsgroup ids %d, %d, %d", goneID, keptID, otherID)
	}
	for _, name := range []string{gone, kept, other} {
		fuCleanupSectionMember(t, section.ID, name)
	}
	for _, id := range []int64{goneID, keptID, otherID} {
		fuQueriesFlagSpam(t, user.ID, id, 1)
		fuQueriesFlagSpam(t, user.ID, id, 2)
	}

	// An absent name in the list must be harmless, the way DeleteNewsgroup treats one.
	n, err := db.BulkDeleteNewsgroups([]string{gone, kept, w0Name("fucleanup.absent")})
	if err != nil {
		t.Fatalf("BulkDeleteNewsgroups: %v", err)
	}
	if n != 1 {
		t.Errorf("BulkDeleteNewsgroups deleted %d rows, want 1 (only the inactive group)", n)
	}

	// The inactive group and everything keyed on it is gone.
	if id := fuQueriesNewsgroupID(t, gone); id != 0 {
		t.Errorf("newsgroup %s still exists (id %d)", gone, id)
	}
	if c := fuCleanupSectionGroupCount(t, gone); c != 0 {
		t.Errorf("section_groups rows of bulk-deleted %s = %d, want 0", gone, c)
	}
	if c := fuQueriesSpamFlagCount(t, goneID); c != 0 {
		t.Errorf("user_spam_flags rows of bulk-deleted %s = %d, want 0", gone, c)
	}

	// The active group was refused, so its dependent rows must be untouched: the three
	// statements have to cover the same set, or this is where they diverge.
	if id := fuQueriesNewsgroupID(t, kept); id != keptID {
		t.Errorf("active newsgroup %s was deleted", kept)
	}
	if c := fuCleanupSectionGroupCount(t, kept); c != 1 {
		t.Errorf("section_groups rows of the active %s = %d, want 1", kept, c)
	}
	if c := fuQueriesSpamFlagCount(t, keptID); c != 2 {
		t.Errorf("user_spam_flags rows of the active %s = %d, want 2", kept, c)
	}

	// And an inactive group that was not named keeps everything too.
	if id := fuQueriesNewsgroupID(t, other); id != otherID {
		t.Errorf("unnamed newsgroup %s was deleted", other)
	}
	if c := fuCleanupSectionGroupCount(t, other); c != 1 {
		t.Errorf("section_groups rows of the unnamed %s = %d, want 1", other, c)
	}
	if c := fuQueriesSpamFlagCount(t, otherID); c != 2 {
		t.Errorf("user_spam_flags rows of the unnamed %s = %d, want 2", other, c)
	}
}

// TestFuCleanupBulkDeleteLeavesNoOrphanInTheAdminListing: the visible consequence of E9.
// The admin section listing shows every section_groups row, so an orphan left by a bulk
// delete stayed on that page and 404d when opened.
func TestFuCleanupBulkDeleteLeavesNoOrphanInTheAdminListing(t *testing.T) {
	db := w0DB(t)
	section := fuCleanupSection(t)
	gone := fuQueriesGroup(t, false)
	fuCleanupSectionMember(t, section.ID, gone)

	if n, err := db.BulkDeleteNewsgroups([]string{gone}); err != nil || n != 1 {
		t.Fatalf("BulkDeleteNewsgroups = %d, %v; want 1, nil", n, err)
	}

	// The admin listing (non-strict) is the one that keeps orphans.
	groups, err := db.GetSectionGroupsWithActivity(section.ID, "name")
	if err != nil {
		t.Fatalf("GetSectionGroupsWithActivity: %v", err)
	}
	for _, g := range groups {
		if g.NewsgroupName == gone {
			t.Fatalf("the admin listing still shows bulk-deleted %s", gone)
		}
	}
}

// TestFuCleanupUpdateUserHelpersWriteOneColumn: UpdateUserEmail and UpdateUserPassword now
// delegate to UpdateUserProfile, which is the single writer of the three user columns (E8).
// Delegating must not widen what they touch: each still writes its own column only.
func TestFuCleanupUpdateUserHelpersWriteOneColumn(t *testing.T) {
	// Unique per run: users.email is UNIQUE, so a fixed literal makes -count=2 fail.
	fuCleanupEmail := w0Name("fucleanup") + "@test.invalid"
	db := w0DB(t)
	user := fuQueriesUser(t)
	wantName := user.DisplayName

	if err := db.UpdateUserEmail(user.ID, fuCleanupEmail); err != nil {
		t.Fatalf("UpdateUserEmail: %v", err)
	}
	got, err := db.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.Email != fuCleanupEmail {
		t.Errorf("email = %q, want the updated one", got.Email)
	}
	if got.PasswordHash != user.PasswordHash {
		t.Errorf("UpdateUserEmail changed password_hash to %q", got.PasswordHash)
	}
	if got.DisplayName != wantName {
		t.Errorf("UpdateUserEmail changed display_name to %q", got.DisplayName)
	}

	if err := db.UpdateUserPassword(user.ID, "fucleanup-new-hash"); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}
	got, err = db.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.PasswordHash != "fucleanup-new-hash" {
		t.Errorf("password_hash = %q, want the updated one", got.PasswordHash)
	}
	if got.Email != fuCleanupEmail {
		t.Errorf("UpdateUserPassword changed email to %q", got.Email)
	}
	if got.DisplayName != wantName {
		t.Errorf("UpdateUserPassword changed display_name to %q", got.DisplayName)
	}
}

// TestFuCleanupUpdateUserProfileOwnsTheRuneLimit: the 64-rune display name limit used to
// live in UpdateUserDisplayName as well, an exported helper with no caller left. It is now
// in UpdateUserProfile only, which every write of that column goes through, and a rejected
// name must leave all three columns alone (E8).
func TestFuCleanupUpdateUserProfileOwnsTheRuneLimit(t *testing.T) {
	db := w0DB(t)
	user := fuQueriesUser(t)

	ok := strings.Repeat("ö", 64) // 64 runes, 128 bytes: accepted
	if err := db.UpdateUserProfile(user.ID, nil, nil, &ok); err != nil {
		t.Fatalf("UpdateUserProfile(64 runes): %v", err)
	}

	tooLong := strings.Repeat("ö", 65)
	email := "fucleanup-rejected@test.invalid"
	hash := "fucleanup-rejected-hash"
	if err := db.UpdateUserProfile(user.ID, &hash, &email, &tooLong); err == nil {
		t.Fatal("UpdateUserProfile(65 runes) = nil, want an error")
	}

	got, err := db.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.DisplayName != ok {
		t.Errorf("display_name = %q, want the 64-rune name", got.DisplayName)
	}
	// The limit is checked before the transaction, so the other two columns of the same
	// call must not have been written either.
	if got.Email == email {
		t.Error("the rejected call still wrote email")
	}
	if got.PasswordHash == hash {
		t.Error("the rejected call still wrote password_hash")
	}
}
