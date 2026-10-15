package database

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// adfOptHierSkipLowEUID skips a test that needs the shared database: OpenDatabase refuses
// to run with euid < 1000.
func adfOptHierSkipLowEUID(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		if euid := os.Geteuid(); euid >= 0 && euid < 1000 {
			t.Skipf("skipping: OpenDatabase refuses euid %d (< 1000)", euid)
		}
	}
}

// adfOptHierFill builds a fresh cache (no database) from hand-made groups.
func adfOptHierFill(groups ...*models.Newsgroup) *HierarchyCache {
	hc := NewHierarchyCache()
	tree := hc.buildTree(groups)
	hc.mu.Lock()
	hc.installTreeLocked(tree)
	hc.mu.Unlock()
	return hc
}

func adfOptHierGroup(name string, messages, lastArticle int64, updated time.Time) *models.Newsgroup {
	return &models.Newsgroup{Name: name, Hierarchy: ExtractHierarchyFromGroupName(name), Active: true,
		MessageCount: messages, LastArticle: lastArticle, UpdatedAt: updated}
}

func adfOptHierNames(groups []*models.Newsgroup) []string {
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	return names
}

// TestAdfOptHierarchyPageWindow: the one window helper never returns a negative or
// overflowing window (A-db-10).
func TestAdfOptHierarchyPageWindow(t *testing.T) {
	huge := math.MaxInt/10 + 1
	cases := []struct {
		n, page, size, start, end int
	}{
		{0, 1, 10, 0, 0},
		{5, 1, 0, 0, 0},
		{5, 1, -3, 0, 0},
		{5, 0, 10, 0, 5},
		{5, -7, 2, 0, 2},
		{5, 1, 2, 0, 2},
		{5, 2, 2, 2, 4},
		{5, 3, 2, 4, 5},
		{5, 4, 2, 5, 5},
		{4, 2, 2, 2, 4},
		{4, 3, 2, 4, 4},
		{5, huge, 10, 5, 5},
		{5, math.MaxInt, math.MaxInt, 5, 5},
		{5, 1, math.MaxInt, 0, 5},
		{25, 4611686018427387905, 10, 25, 25},
	}
	for _, c := range cases {
		start, end := pageWindow(c.n, c.page, c.size)
		if start != c.start || end != c.end {
			t.Errorf("pageWindow(%d, %d, %d) = %d, %d; want %d, %d", c.n, c.page, c.size, start, end, c.start, c.end)
		}
	}
}

// TestAdfOptHierarchyReadersHugePage: the three readers return an empty page for a page
// that used to wrap negative, and page 1 for page 0, without a panic (A-db-10).
func TestAdfOptHierarchyReadersHugePage(t *testing.T) {
	now := time.Now().UTC()
	hc := adfOptHierFill(
		adfOptHierGroup("hp.a.one", 1, 1, now),
		adfOptHierGroup("hp.a.two", 2, 2, now),
		adfOptHierGroup("hp.b", 3, 3, now),
	)
	hc.mu.Lock()
	hc.hierarchies = []*models.Hierarchy{{Name: "hp", GroupCount: 3}, {Name: "hq", GroupCount: 1}}
	hc.mu.Unlock()

	for _, page := range []int{math.MaxInt/10 + 1, 4611686018427387905, math.MaxInt} {
		hs, total, err := hc.GetHierarchiesPaginated(nil, page, 10, "name")
		if err != nil || len(hs) != 0 || total != 2 {
			t.Errorf("GetHierarchiesPaginated(page %d) = %d items, total %d, err %v; want 0, 2, nil", page, len(hs), total, err)
		}
		subs, _, err := hc.GetHierarchySubLevels(nil, "hp", page, 10)
		if err != nil || len(subs) != 0 {
			t.Errorf("GetHierarchySubLevels(page %d) = %v, err %v; want empty", page, subs, err)
		}
		groups, total, err := hc.GetDirectGroupsAtLevel(nil, "hp.a", "name", page, 10)
		if err != nil || len(groups) != 0 || total != 2 {
			t.Errorf("GetDirectGroupsAtLevel(page %d) = %v, total %d, err %v; want empty, 2", page, adfOptHierNames(groups), total, err)
		}
	}

	// page 0 is page 1; pageSize 0 is an empty page.
	if hs, _, _ := hc.GetHierarchiesPaginated(nil, 0, 10, "name"); len(hs) != 2 {
		t.Errorf("GetHierarchiesPaginated(page 0) = %d items; want 2", len(hs))
	}
	if subs, _, _ := hc.GetHierarchySubLevels(nil, "hp", 0, 10); len(subs) != 1 || subs["a"] != 2 {
		t.Errorf("GetHierarchySubLevels(page 0) = %v; want map[a:2]", subs)
	}
	if groups, _, _ := hc.GetDirectGroupsAtLevel(nil, "hp.a", "name", 0, 10); len(groups) != 2 {
		t.Errorf("GetDirectGroupsAtLevel(page 0) = %v; want 2 groups", adfOptHierNames(groups))
	}
	for _, size := range []int{0, -1} {
		if hs, _, _ := hc.GetHierarchiesPaginated(nil, 1, size, "name"); len(hs) != 0 {
			t.Errorf("GetHierarchiesPaginated(pageSize %d) = %d items; want 0", size, len(hs))
		}
		if subs, _, _ := hc.GetHierarchySubLevels(nil, "hp", 1, size); len(subs) != 0 {
			t.Errorf("GetHierarchySubLevels(pageSize %d) = %v; want empty", size, subs)
		}
		if groups, _, _ := hc.GetDirectGroupsAtLevel(nil, "hp.a", "name", 1, size); len(groups) != 0 {
			t.Errorf("GetDirectGroupsAtLevel(pageSize %d) = %v; want empty", size, adfOptHierNames(groups))
		}
	}
}

// TestAdfOptHierarchyUpdateStats: UpdateNewsgroupStats leaves GroupCount alone, takes the
// dates from the batch date (never from an article number), finds a 3-part group under its
// direct parent, adds the batch once and re-sorts "activity" by UpdatedAt (A-db-7).
func TestAdfOptHierarchyUpdateStats(t *testing.T) {
	loaded := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	hc := adfOptHierFill(
		// "one" has the highest article number but the oldest date: activity is by date.
		adfOptHierGroup("zz.a.one", 10, 900, old.Add(time.Hour)),
		adfOptHierGroup("zz.a.two", 5, 10, old),
		adfOptHierGroup("zz.b", 7, 3, old),
	)
	zz := &models.Hierarchy{Name: "zz", GroupCount: 3, LastUpdated: loaded}
	yy := &models.Hierarchy{Name: "yy", GroupCount: 3}
	hc.mu.Lock()
	hc.hierarchies = []*models.Hierarchy{zz, yy}
	hc.hierarchiesMap["zz"] = zz
	hc.hierarchiesMap["yy"] = yy
	hc.mu.Unlock()

	if got := adfOptHierNames(adfOptHierDirect(t, hc, "zz.a", "activity")); strings.Join(got, ",") != "zz.a.one,zz.a.two" {
		t.Fatalf("activity order at build = %v; want zz.a.one first (newer UpdatedAt)", got)
	}

	// The audit's call: a zero batch date must not move any date (yy stayed at 1970 before).
	hc.UpdateNewsgroupStats("yy.x", 1000, 42, time.Time{})
	hc.UpdateNewsgroupStats("zz.a.two", 1000, 42, time.Time{})
	hc.mu.RLock()
	if zz.GroupCount != 3 || !zz.LastUpdated.Equal(loaded) || yy.GroupCount != 3 || !yy.LastUpdated.IsZero() {
		t.Errorf("zero date: zz GroupCount=%d LastUpdated=%v, yy GroupCount=%d LastUpdated=%v; want 3, %v, 3, zero",
			zz.GroupCount, zz.LastUpdated, yy.GroupCount, yy.LastUpdated, loaded)
	}
	hc.mu.RUnlock()

	latest := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	hc.UpdateNewsgroupStats("zz.a.two", 3, 50, latest)
	hc.mu.RLock()
	if zz.GroupCount != 3 || !zz.LastUpdated.Equal(latest) {
		t.Errorf("zz after batch: GroupCount=%d LastUpdated=%v; want 3, %v", zz.GroupCount, zz.LastUpdated, latest)
	}
	hc.mu.RUnlock()

	for _, sortBy := range hierarchySortOrders {
		var two *models.Newsgroup
		for _, g := range adfOptHierDirect(t, hc, "zz.a", sortBy) {
			if g.Name == "zz.a.two" {
				two = g
			}
		}
		if two == nil {
			t.Fatalf("zz.a.two missing from zz.a:%s", sortBy)
		}
		// 5 + 1000 + 3: each batch added once, not once per sort order.
		if two.MessageCount != 1008 || two.LastArticle != 50 || !two.UpdatedAt.Equal(latest) {
			t.Errorf("zz.a.two in %s: MessageCount=%d LastArticle=%d UpdatedAt=%v; want 1008, 50, %v",
				sortBy, two.MessageCount, two.LastArticle, two.UpdatedAt, latest)
		}
	}
	if got := adfOptHierNames(adfOptHierDirect(t, hc, "zz.a", "activity")); strings.Join(got, ",") != "zz.a.two,zz.a.one" {
		t.Errorf("activity order after batch = %v; want zz.a.two first", got)
	}
	if got := adfOptHierNames(adfOptHierDirect(t, hc, "zz.a", "groups")); strings.Join(got, ",") != "zz.a.two,zz.a.one" {
		t.Errorf("groups order after batch = %v; want zz.a.two first", got)
	}

	// An older batch date never moves a date backwards.
	hc.UpdateNewsgroupStats("zz.a.two", 1, 1, old)
	hc.mu.RLock()
	if !zz.LastUpdated.Equal(latest) {
		t.Errorf("older date moved zz.LastUpdated to %v", zz.LastUpdated)
	}
	hc.mu.RUnlock()
	for _, g := range adfOptHierDirect(t, hc, "zz.a", "name") {
		if g.Name == "zz.a.two" && (!g.UpdatedAt.Equal(latest) || g.LastArticle != 50) {
			t.Errorf("older batch: zz.a.two UpdatedAt=%v LastArticle=%d; want %v, 50", g.UpdatedAt, g.LastArticle, latest)
		}
	}

	// UpdateNewsgroupActiveStatus finds a 3-part group under its direct parent too.
	hc.UpdateNewsgroupActiveStatus("zz.a.one", false)
	for _, sortBy := range hierarchySortOrders {
		for _, g := range adfOptHierDirect(t, hc, "zz.a", sortBy) {
			if g.Name == "zz.a.one" && g.Active {
				t.Errorf("zz.a.one still active in zz.a:%s", sortBy)
			}
		}
	}
}

func adfOptHierDirect(t *testing.T, hc *HierarchyCache, prefix, sortBy string) []*models.Newsgroup {
	t.Helper()
	groups, _, err := hc.GetDirectGroupsAtLevel(nil, prefix, sortBy, 1, 100)
	if err != nil {
		t.Fatalf("GetDirectGroupsAtLevel(%s, %s): %v", prefix, sortBy, err)
	}
	return groups
}

// adfOptHierInsert inserts an active newsgroup row straight into the shared main DB (not
// through InsertNewsgroup, so the shared db.HierarchyCache is not touched).
func adfOptHierInsert(t *testing.T, db *Database, name string, messages int) {
	t.Helper()
	if _, err := RetryableExec(db.GetMainDB(),
		`INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy, updated_at)
		 VALUES (?, '', ?, ?, 1, ?, ?)`,
		name, messages, messages, ExtractHierarchyFromGroupName(name), time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("insert newsgroup %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := RetryableExec(db.GetMainDB(), "DELETE FROM newsgroups WHERE name = ?", name); err != nil {
			t.Errorf("cleanup newsgroup %s: %v", name, err)
		}
	})
}

func adfOptHierDelete(t *testing.T, db *Database, name string) {
	t.Helper()
	if _, err := RetryableExec(db.GetMainDB(), "DELETE FROM newsgroups WHERE name = ?", name); err != nil {
		t.Fatalf("delete newsgroup %s: %v", name, err)
	}
}

// TestAdfOptHierarchyInvalidateRebuild: InvalidateHierarchy no longer blanks a top level
// (and is not throttled); the next reader rebuilds the top level and all its sub-levels
// from the database, stale sub-levels disappear and other top levels are untouched (A-db-8).
func TestAdfOptHierarchyInvalidateRebuild(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	top := strings.ReplaceAll(w0Name("adfopthier"), ".", "")
	// A sibling top level whose name starts with top: the range query must not take it.
	sibling := top + "z"

	adfOptHierInsert(t, db, top+".a", 2)
	adfOptHierInsert(t, db, top+".b", 3)
	adfOptHierInsert(t, db, top+".y.old", 4)
	adfOptHierInsert(t, db, sibling+".s", 1)

	hc := NewHierarchyCache()
	if err := hc.buildHierarchyTreeBulk(db); err != nil {
		t.Fatalf("buildHierarchyTreeBulk: %v", err)
	}
	groups, total, err := hc.GetDirectGroupsAtLevel(db, top, "name", 1, 50)
	if err != nil || total != 2 || strings.Join(adfOptHierNames(groups), ",") != top+".a,"+top+".b" {
		t.Fatalf("after build: %v total %d err %v; want %s.a, %s.b", adfOptHierNames(groups), total, err, top, top)
	}

	// Without an invalidation the cache is served as it is.
	adfOptHierInsert(t, db, top+".c", 1)
	adfOptHierInsert(t, db, top+".x.deep", 5)
	adfOptHierInsert(t, db, top+".empty", 0) // message_count 0: never listed
	adfOptHierDelete(t, db, top+".y.old")
	if groups, _, _ := hc.GetDirectGroupsAtLevel(db, top, "name", 1, 50); len(groups) != 2 {
		t.Fatalf("not invalidated: %v; want the 2 cached groups", adfOptHierNames(groups))
	}

	// Two invalidations in a row (the second used to be throttled or to blank the page).
	hc.InvalidateHierarchy(top)
	hc.InvalidateHierarchy(top)
	if _, _, wait := hc.GetInvalidationStatus(); wait != 0 {
		t.Errorf("GetInvalidationStatus wait = %v; want 0 (no throttle)", wait)
	}

	groups, total, err = hc.GetDirectGroupsAtLevel(db, top, "name", 1, 50)
	want := top + ".a," + top + ".b," + top + ".c"
	if err != nil || total != 3 || strings.Join(adfOptHierNames(groups), ",") != want {
		t.Fatalf("after invalidate: %v total %d err %v; want %s", adfOptHierNames(groups), total, err, want)
	}
	subs, subTotal, err := hc.GetHierarchySubLevels(db, top, 1, 50)
	if err != nil || subTotal != 1 || len(subs) != 1 || subs["x"] != 1 {
		t.Errorf("sub-levels of %s = %v total %d err %v; want map[x:1] (y is gone)", top, subs, subTotal, err)
	}
	deep, deepTotal, err := hc.GetDirectGroupsAtLevel(db, top+".x", "activity", 1, 50)
	if err != nil || deepTotal != 1 || len(deep) != 1 || deep[0].Name != top+".x.deep" || deep[0].MessageCount != 5 {
		t.Errorf("%s.x = %v total %d err %v; want %s.x.deep with 5 messages", top, adfOptHierNames(deep), deepTotal, err, top)
	}
	hc.mu.RLock()
	for _, sortBy := range hierarchySortOrders {
		if _, ok := hc.directGroupsCache[top+".y:"+sortBy]; ok {
			t.Errorf("stale key %s.y:%s survived the rebuild", top, sortBy)
		}
	}
	if _, ok := hc.directGroupsCache[sibling+":name"]; !ok {
		t.Errorf("sibling top level %s lost its entries", sibling)
	}
	dirty := hc.isDirtyLocked(top)
	hc.mu.RUnlock()
	if dirty {
		t.Errorf("%s still dirty after the rebuild", top)
	}

	// A sub-level reader rebuilds too, and a delete is seen.
	adfOptHierDelete(t, db, top+".b")
	hc.InvalidateHierarchy(top + ".b")
	if subs, _, _ := hc.GetHierarchySubLevels(db, top, 1, 50); len(subs) != 1 {
		t.Errorf("sub-levels after delete = %v; want map[x:1]", subs)
	}
	if groups, _, _ := hc.GetDirectGroupsAtLevel(db, top, "groups", 1, 50); strings.Join(adfOptHierNames(groups), ",") != top+".a,"+top+".c" {
		t.Errorf("after delete: %v; want %s.a, %s.c (by message count)", adfOptHierNames(groups), top, top)
	}

	// A full build clears the marks it covers; a mark after it is dirty again.
	hc.InvalidateHierarchy(top)
	if err := hc.buildHierarchyTreeBulk(db); err != nil {
		t.Fatalf("buildHierarchyTreeBulk: %v", err)
	}
	hc.mu.RLock()
	dirty = hc.isDirtyLocked(top)
	hc.mu.RUnlock()
	if dirty {
		t.Errorf("%s dirty after a full build", top)
	}
	hc.InvalidateHierarchy(top)
	hc.mu.RLock()
	dirty = hc.isDirtyLocked(top)
	hc.mu.RUnlock()
	if !dirty {
		t.Errorf("%s not dirty after a mark following the full build", top)
	}

	// Without a database a dirty top level is served from the cache.
	if groups, _, _ := hc.GetDirectGroupsAtLevel(nil, top, "name", 1, 50); len(groups) != 2 {
		t.Errorf("db nil: %v; want the 2 cached groups", adfOptHierNames(groups))
	}
}

// TestAdfOptHierarchyConcurrentReaders: readers get copies, so UpdateNewsgroupStats and
// UpdateNewsgroupActiveStatus (in-place updates and re-sorts) do not race with them, and a
// page never lists a group twice (A-db-14). Meant for go test -race.
func TestAdfOptHierarchyConcurrentReaders(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var groups []*models.Newsgroup
	for i := 0; i < 20; i++ {
		groups = append(groups, adfOptHierGroup(fmt.Sprintf("rc.a.g%02d", i), int64(i), int64(i), base.Add(time.Duration(i)*time.Minute)))
	}
	hc := adfOptHierFill(groups...)

	const rounds = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			name := fmt.Sprintf("rc.a.g%02d", i%20)
			hc.UpdateNewsgroupStats(name, i%7, int64(1000+i), base.Add(time.Duration(100+i)*time.Minute))
			hc.UpdateNewsgroupActiveStatus(name, i%2 == 0)
		}
	}()
	errs := make(chan string, 4)
	for r := 0; r < 4; r++ {
		sortBy := []string{"activity", "groups"}[r%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				page, _, err := hc.GetDirectGroupsAtLevel(nil, "rc.a", sortBy, 1+i%2, 10)
				if err != nil {
					errs <- err.Error()
					return
				}
				seen := make(map[string]bool, len(page))
				for _, g := range page {
					_ = fmt.Sprint(g.Active, g.MessageCount, g.LastArticle, g.UpdatedAt)
					if seen[g.Name] {
						errs <- fmt.Sprintf("%s page lists %s twice", sortBy, g.Name)
						return
					}
					seen[g.Name] = true
				}
				// Mutating a returned copy must not reach the cache.
				for _, g := range page {
					g.MessageCount = -1
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	for _, g := range adfOptHierDirect(t, hc, "rc.a", "name") {
		if g.MessageCount < 0 {
			t.Errorf("a reader's copy changed the cached %s", g.Name)
		}
	}
}
