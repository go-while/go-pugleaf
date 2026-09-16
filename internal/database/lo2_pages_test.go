package database

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// lo2PagesGroup inserts a newsgroup row with a unique name and returns it.
func lo2PagesGroup(t *testing.T, active bool) string {
	t.Helper()
	name := w0Name("lo2pages")
	if _, err := RetryableExec(w0DB(t).GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 0, 0, ?, ?)",
		name, active, ExtractHierarchyFromGroupName(name)); err != nil {
		t.Fatalf("insert newsgroup %s: %v", name, err)
	}
	return name
}

// lo2PagesSectionGroupCount counts the section_groups rows of a newsgroup.
func lo2PagesSectionGroupCount(t *testing.T, name string) int {
	t.Helper()
	var n int
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT COUNT(*) FROM section_groups WHERE newsgroup_name = ?", []interface{}{name}, &n); err != nil {
		t.Fatalf("count section_groups of %s: %v", name, err)
	}
	return n
}

// lo2PagesNewsgroupExists reports whether the newsgroups row is still there.
func lo2PagesNewsgroupExists(t *testing.T, name string) bool {
	t.Helper()
	var n int
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT COUNT(*) FROM newsgroups WHERE name = ?", []interface{}{name}, &n); err != nil {
		t.Fatalf("count newsgroups of %s: %v", name, err)
	}
	return n > 0
}

// TestLo2PagesThreadRepliesHugePage: a page number far beyond the last page must return an
// empty page instead of overflowing (page-1)*pageSize into a negative slice index (F15).
func TestLo2PagesThreadRepliesHugePage(t *testing.T) {
	db := w0DB(t)
	group := lo2PagesGroup(t, true)
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer gdb.Return()

	base := time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)
	for i := int64(1); i <= 4; i++ {
		if _, err := db.InsertOverview(gdb, &models.Overview{
			ArticleNum: i, Subject: fmt.Sprintf("s%d", i), FromHeader: "a <a@b>",
			DateSent: base.Add(time.Duration(i) * time.Minute), DateString: "x",
			MessageID: fmt.Sprintf("<lo2pages-%d@test.invalid>", i), Downloaded: 1,
		}); err != nil {
			t.Fatalf("InsertOverview %d: %v", i, err)
		}
	}
	// Root 1 with the three children 2,3,4.
	if _, err := RetryableExec(gdb.DB, `INSERT INTO thread_cache
		(thread_root, root_date, message_count, child_articles, last_child_number, last_activity)
		VALUES (1, ?, 4, '2,3,4', 4, ?)`, base, base); err != nil {
		t.Fatalf("seed thread_cache: %v", err)
	}

	// Page 1 still works and returns all three children.
	replies, total, err := db.GetCachedThreadReplies(gdb, 1, 1, 25)
	if err != nil {
		t.Fatalf("GetCachedThreadReplies(page=1): %v", err)
	}
	if total != 3 || len(replies) != 3 {
		t.Fatalf("page 1: %d replies, total %d; want 3 and 3", len(replies), total)
	}

	for _, page := range []int{1 << 60, 1<<62 + 1, 1000000, 2, 0, -5} {
		replies, total, err := db.GetCachedThreadReplies(gdb, 1, page, 25)
		if err != nil {
			t.Fatalf("GetCachedThreadReplies(page=%d): %v", page, err)
		}
		if total != 3 {
			t.Errorf("page %d: total replies = %d, want 3", page, total)
		}
		switch page {
		case 0, -5:
			// Both clamp to page 1.
			if len(replies) != 3 {
				t.Errorf("page %d: %d replies, want 3 (clamped to page 1)", page, len(replies))
			}
		default:
			if len(replies) != 0 {
				t.Errorf("page %d: %d replies, want 0", page, len(replies))
			}
		}
	}

	// A pageSize of 0 must not divide by zero either.
	if replies, total, err := db.GetCachedThreadReplies(gdb, 1, 1, 0); err != nil || len(replies) != 0 || total != 3 {
		t.Fatalf("pageSize 0: %d replies, total %d, err %v; want 0, 3, nil", len(replies), total, err)
	}
}

// TestLo2PagesDeleteNewsgroupRemovesSectionGroups: deleting an inactive newsgroup also drops
// its section memberships; an active newsgroup is not deleted and keeps them (F10).
func TestLo2PagesDeleteNewsgroupRemovesSectionGroups(t *testing.T) {
	db := w0DB(t)
	section := &models.Section{
		Name:        w0Name("lo2pagessec"),
		DisplayName: "Lo2 pages",
		CreatedAt:   time.Now(),
	}
	if err := db.CreateSection(section); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}

	gone := lo2PagesGroup(t, false)
	kept := lo2PagesGroup(t, true)
	for _, name := range []string{gone, kept} {
		if err := db.CreateSectionGroup(&models.SectionGroup{
			SectionID: section.ID, NewsgroupName: name, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("CreateSectionGroup %s: %v", name, err)
		}
	}

	if err := db.DeleteNewsgroup(gone); err != nil {
		t.Fatalf("DeleteNewsgroup(%s): %v", gone, err)
	}
	if lo2PagesNewsgroupExists(t, gone) {
		t.Errorf("inactive newsgroup %s still exists after DeleteNewsgroup", gone)
	}
	if n := lo2PagesSectionGroupCount(t, gone); n != 0 {
		t.Errorf("section_groups rows of deleted %s = %d, want 0", gone, n)
	}

	// An active group is refused by the DELETE (active = 0 guard); nothing may change.
	if err := db.DeleteNewsgroup(kept); err != nil {
		t.Fatalf("DeleteNewsgroup(%s): %v", kept, err)
	}
	if !lo2PagesNewsgroupExists(t, kept) {
		t.Errorf("active newsgroup %s was deleted", kept)
	}
	if n := lo2PagesSectionGroupCount(t, kept); n != 1 {
		t.Errorf("section_groups rows of active %s = %d, want 1", kept, n)
	}
}

// TestLo2PagesDisplayNameRuneLimit: the limit counts characters, not bytes, so it matches
// the web validation and a multi-byte name of 64 characters can be stored (F6).
func TestLo2PagesDisplayNameRuneLimit(t *testing.T) {
	db := w0DB(t)
	name := w0Name("lo2pages_user")
	if err := db.InsertUser(&models.User{
		Username: name, Email: name + "@test.invalid", PasswordHash: "x", DisplayName: name,
	}); err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	u, err := db.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}

	ok := strings.Repeat("ä", 64) // 64 runes, 128 bytes
	if err := db.UpdateUserDisplayName(u.ID, ok); err != nil {
		t.Fatalf("UpdateUserDisplayName(64 runes): %v", err)
	}
	got, err := db.GetUserByID(u.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.DisplayName != ok {
		t.Fatalf("stored display name = %q, want the 64-rune name", got.DisplayName)
	}

	tooLong := strings.Repeat("ä", 65)
	if err := db.UpdateUserDisplayName(u.ID, tooLong); err == nil {
		t.Fatal("UpdateUserDisplayName(65 runes) = nil, want an error")
	}
	got, err = db.GetUserByID(u.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.DisplayName != ok {
		t.Fatalf("display name changed to %q after the rejected update", got.DisplayName)
	}
}
