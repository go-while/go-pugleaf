package database

import (
	"reflect"
	"testing"
)

// TestThrIntakeRebuildParity (plan thread-orphan-followups, wave 2): for input that arrives in
// order, the batch writer and RebuildThreadsFromScratch write the same threads rows, the same
// thread_cache (message count, children, last child, last activity) and the same reply_count.
// The fixture mixes the cases of both slices: plain replies, two replies to one parent, a
// trimmed References, an unknown (foreign) reference, a self-reference before an older valid
// one, and a reply to an orphan root, spread over three batches.
func TestThrIntakeRebuildParity(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrparity")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	root := thrIntakeMsgID("root")
	r1 := thrIntakeMsgID("r1")
	r2 := thrIntakeMsgID("r2")
	r3 := thrIntakeMsgID("r3")
	sib := thrIntakeMsgID("sib")
	foreign := thrIntakeMsgID("foreign") // never arrives in this group
	orphan := thrIntakeMsgID("orphan")
	toOrphan := thrIntakeMsgID("to-orphan")
	trimmed := thrIntakeMsgID("trimmed")
	self := thrIntakeMsgID("self")
	root2 := thrIntakeMsgID("root2")

	thrIntakeRun(t, db, task,
		thrIntakeArticle(root, 1, nil, task),
		thrIntakeArticle(r1, 2, []string{root}, task),
		thrIntakeArticle(sib, 3, []string{root}, task),
		thrIntakeArticle(r2, 4, []string{root, r1}, task),
	)
	thrIntakeRun(t, db, task,
		thrIntakeArticle(r3, 5, []string{root, r1, r2}, task),
		thrIntakeArticle(orphan, 6, []string{foreign}, task),
		thrIntakeArticle(toOrphan, 7, []string{foreign, orphan}, task),
		thrIntakeArticle(root2, 8, nil, task),
	)
	thrIntakeRun(t, db, task,
		thrIntakeArticle(trimmed, 9, []string{root, r3}, task),
		thrIntakeArticle(self, 10, []string{root2, self}, task),
	)

	ids := []string{root, r1, r2, r3, sib, orphan, toOrphan, trimmed, self, root2}
	snapshot := func() ([]thrRow, map[int64]thrCacheRow, map[string]int) {
		counts := make(map[string]int, len(ids))
		for _, id := range ids {
			counts[id] = thrIntakeReplyCount(t, db, group, id)
		}
		cache := thrIntakeCache(t, db, group)
		for k, v := range cache {
			v.childrenOrder = nil // the order differs by design (intake: DateSent, rebuild: number)
			cache[k] = v
		}
		return thrIntakeThreads(t, db, group), cache, counts
	}
	intakeRows, intakeCache, intakeCounts := snapshot()
	if len(intakeRows) != len(ids) {
		t.Fatalf("intake wrote %d threads rows, want one per article (%d): %v", len(intakeRows), len(ids), intakeRows)
	}

	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	thrRebuildRun(t, db, group, groupDB)
	groupDB.Return()

	rebuildRows, rebuildCache, rebuildCounts := snapshot()
	if !reflect.DeepEqual(intakeRows, rebuildRows) {
		t.Errorf("threads rows differ:\nintake:  %v\nrebuild: %v", intakeRows, rebuildRows)
	}
	if !reflect.DeepEqual(intakeCache, rebuildCache) {
		t.Errorf("thread_cache differs:\nintake:  %+v\nrebuild: %+v", intakeCache, rebuildCache)
	}
	if !reflect.DeepEqual(intakeCounts, rebuildCounts) {
		t.Errorf("reply_count differs:\nintake:  %v\nrebuild: %v", intakeCounts, rebuildCounts)
	}
	if got := thrIntakeThreadsCount(t, db, group); got != 3 {
		t.Errorf("GetThreadsCount after the rebuild = %d, want 3 roots (root, orphan, root2)", got)
	}
}
