package database

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// fl_nntpdb_hierarchy_test.go: the hierarchy cache rebuild lock is per top level (a rebuild
// of one top level, whose RetryableQuery may wait out a busy main database, does not hold up
// the readers of another), and a stats update for a group that is not cached marks its top
// level dirty (plan followup-leftovers, slice fl-nntp-db).

// TestFlNntpdbHierarchyRebuildLockPerTop: while the rebuild of top level A is held in its
// hook, a reader of the dirty top level B rebuilds B and returns; a second reader of A waits
// for the running rebuild and runs no query of its own.
func TestFlNntpdbHierarchyRebuildLockPerTop(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	topA := strings.ReplaceAll(w0Name("flnntpdbhiera"), ".", "")
	topB := strings.ReplaceAll(w0Name("flnntpdbhierb"), ".", "")
	adfOptHierInsert(t, db, topA+".g", 1)
	adfOptHierInsert(t, db, topB+".g", 1)

	hc := NewHierarchyCache()
	if err := hc.buildHierarchyTreeBulk(db); err != nil {
		t.Fatalf("buildHierarchyTreeBulk: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := map[string]int{}
	hc.mu.Lock()
	hc.testRebuildHook = func(top string) {
		mu.Lock()
		calls[top]++
		first := top == topA && calls[top] == 1
		mu.Unlock()
		if first {
			close(entered)
			select {
			case <-release:
			case <-time.After(testWait):
			}
		}
	}
	hc.mu.Unlock()
	hc.InvalidateHierarchy(topA)
	hc.InvalidateHierarchy(topB)

	readA := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			_, _, err := hc.GetDirectGroupsAtLevel(db, topA, "name", 1, 10)
			done <- err
		}()
		return done
	}
	doneA1 := readA()
	select {
	case <-entered:
	case <-time.After(testWait):
		t.Fatalf("the rebuild of %s never reached its hook", topA)
	}
	doneA2 := readA() // waits for the running rebuild of topA

	doneB := make(chan error, 1)
	go func() {
		groups, _, err := hc.GetDirectGroupsAtLevel(db, topB, "name", 1, 10)
		if err == nil && (len(groups) != 1 || groups[0].Name != topB+".g") {
			err = errFlNntpdbHier(adfOptHierNames(groups))
		}
		doneB <- err
	}()
	select {
	case err := <-doneB:
		if err != nil {
			t.Errorf("reader of %s: %v", topB, err)
		}
	case <-time.After(testWait):
		close(release)
		t.Fatalf("the reader of %s waited for the rebuild of %s: the rebuild lock is not per top level", topB, topA)
	}
	close(release)
	for i, done := range []<-chan error{doneA1, doneA2} {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("reader %d of %s: %v", i+1, topA, err)
			}
		case <-time.After(testWait):
			t.Fatalf("reader %d of %s did not return after the release", i+1, topA)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls[topA] != 1 || calls[topB] != 1 {
		t.Errorf("rebuild hook calls %v, want one per top level (the second reader of %s must not rebuild again)", calls, topA)
	}
}

type errFlNntpdbHier []string

func (e errFlNntpdbHier) Error() string { return "unexpected groups " + strings.Join(e, ",") }

// TestFlNntpdbHierarchyStatsMissingGroupMarks: a batch for a group the cache does not list
// (its first articles: the tree lists only groups with messages) marks the top level dirty,
// so the next reader rebuilds it and lists the group; a stats update without new articles
// marks nothing.
func TestFlNntpdbHierarchyStatsMissingGroupMarks(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	top := strings.ReplaceAll(w0Name("flnntpdbhiermiss"), ".", "")
	listed := top + ".listed"
	fresh := top + ".fresh"
	adfOptHierInsert(t, db, listed, 2)
	adfOptHierInsert(t, db, fresh, 0)

	hc := NewHierarchyCache()
	if err := hc.buildHierarchyTreeBulk(db); err != nil {
		t.Fatalf("buildHierarchyTreeBulk: %v", err)
	}
	if groups := adfOptHierDirect(t, hc, top, "name"); strings.Join(adfOptHierNames(groups), ",") != listed {
		t.Fatalf("after the build: %v, want only %s", adfOptHierNames(groups), listed)
	}

	hc.UpdateNewsgroupStats(fresh, 0, 0, time.Time{})
	if adfOptHierDirty(hc, top) {
		t.Fatalf("%s dirty after a stats update without articles", top)
	}
	hc.UpdateNewsgroupStats(listed, 1, 3, time.Now())
	if adfOptHierDirty(hc, top) {
		t.Fatalf("%s dirty after a stats update of a cached group", top)
	}

	// the batch writer stored the first articles of fresh: row first, then the cache
	if _, err := RetryableExec(db.GetMainDB(), "UPDATE newsgroups SET message_count = 3, last_article = 3 WHERE name = ?", fresh); err != nil {
		t.Fatalf("update %s: %v", fresh, err)
	}
	hc.UpdateNewsgroupStats(fresh, 3, 3, time.Now())
	if !adfOptHierDirty(hc, top) {
		t.Fatalf("%s not dirty after the first articles of the uncached group %s", top, fresh)
	}
	groups, total, err := hc.GetDirectGroupsAtLevel(db, top, "name", 1, 10)
	if err != nil || total != 2 || strings.Join(adfOptHierNames(groups), ",") != fresh+","+listed {
		t.Fatalf("after the rebuild: %v total %d err %v, want %s,%s", adfOptHierNames(groups), total, err, fresh, listed)
	}
	if adfOptHierDirty(hc, top) {
		t.Errorf("%s still dirty after the rebuild", top)
	}
}
