package database

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// hcntGroup is an active newsgroup of top level top, with messages articles.
func hcntGroup(top, leaf string, messages int64, active bool) *models.Newsgroup {
	return &models.Newsgroup{Name: top + "." + leaf, Active: active, MessageCount: messages, LastArticle: messages, LowWater: 1, Status: "y"}
}

// hcntCount reads the hierarchies row of top; -1 when there is none.
func hcntCount(t *testing.T, db *Database, top string) int {
	t.Helper()
	var n int
	err := RetryableQueryRowScan(db.GetMainDB(), "SELECT group_count FROM hierarchies WHERE name = ?", []interface{}{top}, &n)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return -1
		}
		t.Fatalf("hierarchies row %s: %v", top, err)
	}
	return n
}

func hcntCleanup(t *testing.T, db *Database, top string) {
	t.Cleanup(func() {
		for _, q := range []string{"DELETE FROM newsgroups WHERE hierarchy = ?", "DELETE FROM hierarchies WHERE name = ?"} {
			if _, err := RetryableExec(db.GetMainDB(), q, top); err != nil {
				t.Errorf("cleanup %s: %v", top, err)
			}
		}
		db.HierarchyCache.refreshHierarchyRow(db, top)
	})
}

// TestHierarchyCountsMaintained: the hierarchies table, which the Usenet page lists, is
// kept by the group writers themselves: InsertNewsgroup counts an active group in (O(1),
// creating the row for a new top level), an inactive group is not counted, and the active
// toggles recount the top level. Until 2026-10-23 only the admin button wrote this table.
func TestHierarchyCountsMaintained(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	top := strings.ReplaceAll(w0Name("hcnt"), ".", "")
	hcntCleanup(t, db, top)

	if got := hcntCount(t, db, top); got != -1 {
		t.Fatalf("fresh top level %s already has a hierarchies row (%d)", top, got)
	}
	if err := db.InsertNewsgroup(hcntGroup(top, "a", 5, true)); err != nil {
		t.Fatal(err)
	}
	if got := hcntCount(t, db, top); got != 1 {
		t.Errorf("after first active group: group_count %d, want 1", got)
	}
	if err := db.InsertNewsgroup(hcntGroup(top, "b", 0, true)); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertNewsgroup(hcntGroup(top, "c", 0, false)); err != nil {
		t.Fatal(err)
	}
	if got := hcntCount(t, db, top); got != 2 {
		t.Errorf("after two active and one inactive group: group_count %d, want 2", got)
	}

	// deactivate one, activate the inactive one, bulk-deactivate all: real recounts
	if err := db.UpdateNewsgroupActive(top+".b", false); err != nil {
		t.Fatal(err)
	}
	if got := hcntCount(t, db, top); got != 1 {
		t.Errorf("after deactivating b: group_count %d, want 1", got)
	}
	if err := db.UpdateNewsgroupActive(top+".c", true); err != nil {
		t.Fatal(err)
	}
	if got := hcntCount(t, db, top); got != 2 {
		t.Errorf("after activating c: group_count %d, want 2", got)
	}
	if n, err := db.BulkUpdateNewsgroupActive([]string{top + ".a", top + ".c"}, false); err != nil || n != 2 {
		t.Fatalf("bulk deactivate: n=%d err=%v", n, err)
	}
	if got := hcntCount(t, db, top); got != 0 {
		t.Errorf("after bulk deactivation: group_count %d, want 0", got)
	}

	// a duplicate insert fails and leaves the count alone
	if err := db.InsertNewsgroup(hcntGroup(top, "a", 0, true)); err == nil {
		t.Error("duplicate InsertNewsgroup succeeded")
	}
	if got := hcntCount(t, db, top); got != 0 {
		t.Errorf("after failed duplicate insert: group_count %d, want 0", got)
	}
}

// TestHierarchyCountsListedRow: the cached top-level list follows the database without a
// restart. A new hierarchy appears once it counts an active group with articles (the same
// filter as the uncached query), stays while that holds and disappears when its groups
// are deactivated. hasHierarchy is what the batch writer checks after a commit.
func TestHierarchyCountsListedRow(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	hc := db.HierarchyCache
	top := strings.ReplaceAll(w0Name("hcntlist"), ".", "")
	hcntCleanup(t, db, top)

	listed := func() (bool, int) {
		hs, total, err := hc.GetHierarchiesPaginated(db, 1, 100000, "name")
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hs {
			if h.Name == top {
				return true, h.GroupCount
			}
		}
		_ = total
		return false, 0
	}

	// a group without articles: the row counts it, the page does not list the hierarchy
	if err := db.InsertNewsgroup(hcntGroup(top, "empty", 0, true)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := listed(); ok || hc.hasHierarchy(top) {
		t.Errorf("hierarchy with an empty group is listed")
	}

	// the first group with articles lists it (the batch writer refreshes a missing top level)
	if err := db.InsertNewsgroup(hcntGroup(top, "full", 3, true)); err != nil {
		t.Fatal(err)
	}
	if ok, n := listed(); !ok || n != 2 {
		t.Errorf("hierarchy with a filled group: listed=%v count=%d, want true, 2", ok, n)
	}
	if !hc.hasHierarchy(top) {
		t.Error("hasHierarchy false after the hierarchy was listed")
	}

	// a stale entry is corrected, not duplicated, by a second refresh
	hc.refreshHierarchyRow(db, top)
	hs, _, err := hc.GetHierarchiesPaginated(db, 1, 100000, "name")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, h := range hs {
		if h.Name == top {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("hierarchy listed %d times, want once", seen)
	}

	// deactivating the filled group drops the hierarchy from the list again
	if err := db.UpdateNewsgroupActive(top+".full", false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := listed(); ok || hc.hasHierarchy(top) {
		t.Error("hierarchy still listed after its only filled group was deactivated")
	}
	if got := hcntCount(t, db, top); got != 1 {
		t.Errorf("group_count %d after deactivation, want 1", got)
	}
}

// TestHierarchyCountsSeedCheck: on a database whose hierarchies table lists something the
// warm-up check does nothing (no full sync on a normal start).
func TestHierarchyCountsSeedCheck(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	top := strings.ReplaceAll(w0Name("hcntseed"), ".", "")
	hcntCleanup(t, db, top)
	if err := db.InsertNewsgroup(hcntGroup(top, "a", 1, true)); err != nil {
		t.Fatal(err)
	}
	if ran, err := db.seedHierarchiesIfEmpty(); err != nil || ran {
		t.Errorf("seedHierarchiesIfEmpty on a filled table: ran=%v err=%v, want false, nil", ran, err)
	}
}
