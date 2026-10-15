package database

import (
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// adfReadersMigration is the repair migration of slice adf-db-readers (A-db-1).
const adfReadersMigration = "migrations/0029_main_repair_null_hierarchy.sql"

// adfReadersNullGroup inserts a newsgroups row the way the old batch upsert left it:
// last_article, message_count, high_water, low_water, status and hierarchy all NULL. The
// row is removed again when the test ends, so no later test of the shared DB sees it.
func adfReadersNullGroup(t *testing.T, name string, active bool, messageCount interface{}) int64 {
	t.Helper()
	db := w0DB(t)
	if _, err := RetryableExec(db.GetMainDB(),
		`INSERT INTO newsgroups(name, description, last_article, message_count, active, high_water, low_water, status, hierarchy)
		 VALUES (?, '', NULL, ?, ?, NULL, NULL, NULL, NULL)`, name, messageCount, active); err != nil {
		t.Fatalf("insert NULL newsgroup %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := RetryableExec(db.GetMainDB(), "DELETE FROM newsgroups WHERE name = ?", name); err != nil {
			t.Errorf("cleanup newsgroup %s: %v", name, err)
		}
	})
	id := fuQueriesNewsgroupID(t, name)
	if id == 0 {
		t.Fatalf("precondition: newsgroup %s has no id", name)
	}
	return id
}

// adfReadersCheckDefaults asserts the COALESCE defaults of the NULL columns.
func adfReadersCheckDefaults(t *testing.T, where string, g *models.Newsgroup) {
	t.Helper()
	if g.LastArticle != 0 || g.MessageCount != 0 || g.HighWater != 0 || g.LowWater != 1 {
		t.Errorf("%s: last_article=%d message_count=%d high_water=%d low_water=%d; want 0, 0, 0, 1",
			where, g.LastArticle, g.MessageCount, g.HighWater, g.LowWater)
	}
	if g.Status != "y" {
		t.Errorf("%s: status = %q, want \"y\"", where, g.Status)
	}
	if g.Hierarchy != "" {
		t.Errorf("%s: hierarchy = %q, want \"\"", where, g.Hierarchy)
	}
}

// adfReadersFind returns the group called name from list, or nil.
func adfReadersFind(list []*models.Newsgroup, name string) *models.Newsgroup {
	for _, g := range list {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// adfReadersSpamCount is the count GetSpamArticles reports (limit 0: no group DB is opened).
func adfReadersSpamCount(t *testing.T) int {
	t.Helper()
	_, _, n, err := w0DB(t).GetSpamArticles(0, 0)
	if err != nil {
		t.Fatalf("GetSpamArticles: %v", err)
	}
	return n
}

// adfReadersSpamRows inserts spam rows 1..n for newsgroup id and removes whatever is left of
// them when the test ends.
func adfReadersSpamRows(t *testing.T, id int64, n int) {
	t.Helper()
	db := w0DB(t)
	for i := 1; i <= n; i++ {
		if _, err := RetryableExec(db.GetMainDB(),
			"INSERT INTO spam(newsgroup_id, article_num) VALUES (?, ?)", id, i); err != nil {
			t.Fatalf("insert spam row %d/%d: %v", id, i, err)
		}
	}
	t.Cleanup(func() {
		if _, err := RetryableExec(db.GetMainDB(), "DELETE FROM spam WHERE newsgroup_id = ?", id); err != nil {
			t.Errorf("cleanup spam rows of %d: %v", id, err)
		}
	})
}

// adfReadersSpamRowsOf counts the spam rows keyed on newsgroup id.
func adfReadersSpamRowsOf(t *testing.T, id int64) int {
	t.Helper()
	var n int
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT COUNT(*) FROM spam WHERE newsgroup_id = ?", []interface{}{id}, &n); err != nil {
		t.Fatalf("count spam rows of %d: %v", id, err)
	}
	return n
}

// TestAdfReadersNullColumns: a newsgroups row with NULL counters, status and hierarchy made
// every MainDBGet* scan fail, so the fetcher's $all, history-rebuild, expire-news and GROUP
// aborted on one such row (A-db-1); the listing readers aborted the same way (A-db-13).
func TestAdfReadersNullColumns(t *testing.T) {
	db := w0DB(t)
	name := w0Name("adfreaders")
	id := adfReadersNullGroup(t, name, true, nil)

	g, err := db.MainDBGetNewsgroup(name)
	if err != nil {
		t.Fatalf("MainDBGetNewsgroup: %v", err)
	}
	adfReadersCheckDefaults(t, "MainDBGetNewsgroup", g)

	g, err = db.MainDBGetNewsgroupByID(id)
	if err != nil {
		t.Fatalf("MainDBGetNewsgroupByID: %v", err)
	}
	if g.Name != name {
		t.Errorf("MainDBGetNewsgroupByID(%d) = %q, want %q", id, g.Name, name)
	}
	adfReadersCheckDefaults(t, "MainDBGetNewsgroupByID", g)

	all, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		t.Fatalf("MainDBGetAllNewsgroups: %v", err)
	}
	if g = adfReadersFind(all, name); g == nil {
		t.Fatalf("MainDBGetAllNewsgroups does not list %s", name)
	}
	adfReadersCheckDefaults(t, "MainDBGetAllNewsgroups", g)

	active, err := db.GetActiveNewsgroups()
	if err != nil {
		t.Fatalf("GetActiveNewsgroups: %v", err)
	}
	if g = adfReadersFind(active, name); g == nil {
		t.Fatalf("GetActiveNewsgroups does not list %s", name)
	} else if g.LastArticle != 0 || g.MessageCount != 0 {
		t.Errorf("GetActiveNewsgroups: last_article=%d message_count=%d, want 0, 0", g.LastArticle, g.MessageCount)
	}

	admin, total, err := db.GetNewsgroupsPaginatedAdmin(1, 1<<20)
	if err != nil {
		t.Fatalf("GetNewsgroupsPaginatedAdmin: %v", err)
	}
	if total < 1 {
		t.Errorf("GetNewsgroupsPaginatedAdmin total = %d", total)
	}
	if g = adfReadersFind(admin, name); g == nil {
		t.Fatalf("GetNewsgroupsPaginatedAdmin does not list %s", name)
	} else if g.Status != "y" || g.MessageCount != 0 {
		t.Errorf("GetNewsgroupsPaginatedAdmin: status=%q message_count=%d, want \"y\", 0", g.Status, g.MessageCount)
	}
}

// TestAdfReadersRepairMigration: migration 0029 is embedded, ran when the test DB was
// opened, and sets hierarchy to the first label of the name (the whole name without a dot)
// and the NULL counters to 0, leaving rows that already have a hierarchy alone.
func TestAdfReadersRepairMigration(t *testing.T) {
	db := w0DB(t)
	var applied int
	if err := RetryableQueryRowScan(db.GetMainDB(),
		"SELECT COUNT(*) FROM schema_migrations WHERE filename = ?",
		[]interface{}{strings.TrimPrefix(adfReadersMigration, "migrations/")}, &applied); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if applied != 1 {
		t.Errorf("migration %s recorded %d times, want 1", adfReadersMigration, applied)
	}

	dotted := w0Name("adfreadersrepair")
	plain := strings.ReplaceAll(w0Name("adfreadersrepair"), ".", "")
	kept := w0Name("adfreadersrepair")
	adfReadersNullGroup(t, dotted, true, nil)
	adfReadersNullGroup(t, plain, false, nil)
	adfReadersNullGroup(t, kept, true, nil)
	if _, err := RetryableExec(db.GetMainDB(),
		"UPDATE newsgroups SET hierarchy = 'custom' WHERE name = ?", kept); err != nil {
		t.Fatalf("set hierarchy of %s: %v", kept, err)
	}

	sqlText, err := EmbeddedMigrationsFS.ReadFile(adfReadersMigration)
	if err != nil {
		t.Fatalf("read embedded %s: %v", adfReadersMigration, err)
	}
	if _, err := RetryableExec(db.GetMainDB(), string(sqlText)); err != nil {
		t.Fatalf("run %s: %v", adfReadersMigration, err)
	}

	for _, c := range []struct{ name, hierarchy string }{
		{dotted, "adfreadersrepair"},
		{plain, plain},
		{kept, "custom"},
	} {
		var hierarchy string
		var lastArticle, messageCount int64
		if err := RetryableQueryRowScan(db.GetMainDB(),
			"SELECT hierarchy, last_article, message_count FROM newsgroups WHERE name = ?",
			[]interface{}{c.name}, &hierarchy, &lastArticle, &messageCount); err != nil {
			t.Fatalf("read repaired %s: %v", c.name, err)
		}
		if hierarchy != c.hierarchy || lastArticle != 0 || messageCount != 0 {
			t.Errorf("%s after repair: hierarchy=%q last_article=%d message_count=%d; want %q, 0, 0",
				c.name, hierarchy, lastArticle, messageCount, c.hierarchy)
		}
	}
}

// TestAdfReadersHierarchyReaders: the hierarchy readers dropped a row whose hierarchy or
// message_count was NULL (scan error, `continue`, no log of the cause) (A-db-11).
func TestAdfReadersHierarchyReaders(t *testing.T) {
	db := w0DB(t)

	// GetNewsgroupsByHierarchy, sub-hierarchy path (name LIKE): the NULL row is listed.
	parent := w0Name("adfreadershier")
	child := parent + ".sub"
	adfReadersNullGroup(t, child, true, nil)
	groups, total, err := db.GetNewsgroupsByHierarchy(parent, 1, 10, "name")
	if err != nil {
		t.Fatalf("GetNewsgroupsByHierarchy: %v", err)
	}
	if total != 1 || len(groups) != 1 || groups[0].Name != child {
		t.Fatalf("GetNewsgroupsByHierarchy(%s) = %d groups, total %d; want only %s", parent, len(groups), total, child)
	}
	if groups[0].Hierarchy != "" || groups[0].MessageCount != 0 || groups[0].LastArticle != 0 {
		t.Errorf("GetNewsgroupsByHierarchy: hierarchy=%q message_count=%d last_article=%d; want \"\", 0, 0",
			groups[0].Hierarchy, groups[0].MessageCount, groups[0].LastArticle)
	}

	// buildHierarchyTreeBulk (fresh cache, nothing else touches it): a group with a NULL
	// hierarchy but messages is placed in the tree.
	treeName := w0Name("adfreaderstree")
	adfReadersNullGroup(t, treeName, true, 3)
	hc := NewHierarchyCache()
	if err := hc.buildHierarchyTreeBulk(db); err != nil {
		t.Fatalf("buildHierarchyTreeBulk: %v", err)
	}
	top := treeName[:strings.IndexByte(treeName, '.')]
	hc.mu.RLock()
	found := adfReadersFind(hc.directGroupsCache[top+":name"], treeName)
	hc.mu.RUnlock()
	if found == nil {
		t.Fatalf("buildHierarchyTreeBulk did not place %s under %s", treeName, top)
	}
	if found.MessageCount != 3 || found.Hierarchy != "" {
		t.Errorf("tree entry %s: message_count=%d hierarchy=%q; want 3, \"\"", treeName, found.MessageCount, found.Hierarchy)
	}

	// getHierarchiesPaginatedDirect: a hierarchy with a NULL description is listed with "".
	hname := strings.ReplaceAll(w0Name("adfreadershdir"), ".", "")
	if _, err := RetryableExec(db.GetMainDB(),
		"INSERT INTO hierarchies(name, description, group_count) VALUES (?, NULL, 1)", hname); err != nil {
		t.Fatalf("insert hierarchy %s: %v", hname, err)
	}
	t.Cleanup(func() {
		if _, err := RetryableExec(db.GetMainDB(), "DELETE FROM hierarchies WHERE name = ?", hname); err != nil {
			t.Errorf("cleanup hierarchy %s: %v", hname, err)
		}
	})
	member := hname + ".group"
	adfReadersNullGroup(t, member, true, 5)
	if _, err := RetryableExec(db.GetMainDB(),
		"UPDATE newsgroups SET hierarchy = ? WHERE name = ?", hname, member); err != nil {
		t.Fatalf("set hierarchy of %s: %v", member, err)
	}
	// Every sort order still runs: two of them ORDER BY a column the DISTINCT select list
	// now only holds inside COALESCE.
	for _, sortBy := range []string{"newest", "groups"} {
		if _, _, err := db.getHierarchiesPaginatedDirect(1, 10, sortBy); err != nil {
			t.Fatalf("getHierarchiesPaginatedDirect(sort %s): %v", sortBy, err)
		}
	}
	hierarchies, _, err := db.getHierarchiesPaginatedDirect(1, 1<<20, "name")
	if err != nil {
		t.Fatalf("getHierarchiesPaginatedDirect: %v", err)
	}
	var got *models.Hierarchy
	for _, h := range hierarchies {
		if h.Name == hname {
			got = h
		}
	}
	if got == nil {
		t.Fatalf("getHierarchiesPaginatedDirect does not list %s", hname)
	}
	if got.Description != "" || got.GroupCount != 1 {
		t.Errorf("hierarchy %s: description=%q group_count=%d; want \"\", 1", hname, got.Description, got.GroupCount)
	}
}

// TestAdfReadersBulkDeleteSpam: BulkDeleteNewsgroups removes the spam rows of the groups it
// deletes and only those (FU:E15); the count comes from the newsgroups delete (FU:E13).
func TestAdfReadersBulkDeleteSpam(t *testing.T) {
	db := w0DB(t)
	gone := fuQueriesGroup(t, false)
	kept := fuQueriesGroup(t, true)
	goneID := fuQueriesNewsgroupID(t, gone)
	keptID := fuQueriesNewsgroupID(t, kept)

	base := adfReadersSpamCount(t)
	adfReadersSpamRows(t, goneID, 3)
	adfReadersSpamRows(t, keptID, 2)
	if c := adfReadersSpamCount(t); c != base+5 {
		t.Fatalf("spam count before delete = %d, want %d", c, base+5)
	}

	n, err := db.BulkDeleteNewsgroups([]string{gone, kept, w0Name("adfreaders.absent")})
	if err != nil {
		t.Fatalf("BulkDeleteNewsgroups: %v", err)
	}
	if n != 1 {
		t.Errorf("BulkDeleteNewsgroups deleted %d rows, want 1 (only the inactive group)", n)
	}
	if c := adfReadersSpamRowsOf(t, goneID); c != 0 {
		t.Errorf("spam rows of bulk-deleted %s = %d, want 0", gone, c)
	}
	if c := adfReadersSpamRowsOf(t, keptID); c != 2 {
		t.Errorf("spam rows of the active %s = %d, want 2", kept, c)
	}
	if c := adfReadersSpamCount(t); c != base+2 {
		t.Errorf("spam count after delete = %d, want %d", c, base+2)
	}
}

// TestAdfReadersDeleteNewsgroupSpam: the same for the single-group DeleteNewsgroup.
func TestAdfReadersDeleteNewsgroupSpam(t *testing.T) {
	db := w0DB(t)
	gone := fuQueriesGroup(t, false)
	goneID := fuQueriesNewsgroupID(t, gone)

	base := adfReadersSpamCount(t)
	adfReadersSpamRows(t, goneID, 3)
	if c := adfReadersSpamCount(t); c != base+3 {
		t.Fatalf("spam count before delete = %d, want %d", c, base+3)
	}

	deleted, err := db.DeleteNewsgroup(gone)
	if err != nil || !deleted {
		t.Fatalf("DeleteNewsgroup = %v, %v; want true, nil", deleted, err)
	}
	if c := adfReadersSpamRowsOf(t, goneID); c != 0 {
		t.Errorf("spam rows of deleted %s = %d, want 0", gone, c)
	}
	if c := adfReadersSpamCount(t); c != base {
		t.Errorf("spam count after delete = %d, want %d", c, base)
	}

	// An active group is not deleted, and neither are its spam rows.
	active := fuQueriesGroup(t, true)
	activeID := fuQueriesNewsgroupID(t, active)
	adfReadersSpamRows(t, activeID, 2)
	if deleted, err := db.DeleteNewsgroup(active); err != nil || deleted {
		t.Fatalf("DeleteNewsgroup(active) = %v, %v; want false, nil", deleted, err)
	}
	if c := adfReadersSpamRowsOf(t, activeID); c != 2 {
		t.Errorf("spam rows of the active %s = %d, want 2", active, c)
	}
}

// TestAdfReadersSpamCountIgnoresOrphans: spam rows of a group that no longer exists (left
// by deletes before this fix) do not inflate the admin count past the listable rows.
func TestAdfReadersSpamCountIgnoresOrphans(t *testing.T) {
	db := w0DB(t)
	var maxID int64
	if err := RetryableQueryRowScan(db.GetMainDB(),
		"SELECT COALESCE(MAX(id), 0) FROM newsgroups", nil, &maxID); err != nil {
		t.Fatalf("max newsgroup id: %v", err)
	}
	base := adfReadersSpamCount(t)
	adfReadersSpamRows(t, maxID+1000000, 4)
	if c := adfReadersSpamCount(t); c != base {
		t.Errorf("spam count with 4 orphaned rows = %d, want %d", c, base)
	}
}
