package database

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// thr_intake_test.go covers slice intake of .claude/plans/wip/thread-orphan-followups.md: the
// per-group threading rule of the batch writer (db_batch.go, batchProcessThreading ->
// batchThreadPlacements). An article whose References name no article of THIS group that is in
// a thread is a thread root here; otherwise it joins the thread of the nearest referenced
// article, and threads gets a reply row (root, parent, child, depth) with depth = parent's
// depth + 1, the same shape the rebuild writes.
//
// The tests drive the real path (processNewsgroupBatch through fuBatchRun), so the articles get
// their numbers from PHASE 1 exactly as in production. They use the batch helpers of
// fu_batch_test.go (fuBatchIsolatedDB, fuBatchRun) and adf_batch_test.go (adfBatchQueue) and
// assert on stored rows only - no wall-clock assertions anywhere.

// thrIntakeBase is a fixed, old timestamp: the articles of a fixture get base + n minutes, so
// the batch's sort order (DateSent, then article number) is data, not wall-clock.
var thrIntakeBase = time.Date(2020, 3, 1, 12, 0, 0, 0, time.UTC)

// thrIntakeMsgID returns a unique message-id for a fixture article.
func thrIntakeMsgID(name string) string {
	return fmt.Sprintf("<thr-%s-%d@test.invalid>", name, w0Seq.Add(1))
}

// thrIntakeArticle builds an article as the capture path leaves it for the batch writer: one
// entry in ArticleNums per target group, References both as the header string and as RefSlice,
// and IsThrRoot/IsReply as the processor sets them (from the header alone, see
// internal/processor/threading.go). The per-group rule must not depend on those two.
func thrIntakeArticle(messageID string, minute int, refs []string, tasks ...*BatchTasks) *models.Article {
	when := thrIntakeBase.Add(time.Duration(minute) * time.Minute)
	groups := make([]*string, 0, len(tasks))
	for _, task := range tasks {
		groups = append(groups, task.Newsgroup)
	}
	return &models.Article{
		MessageID:     messageID,
		Subject:       "thr-intake",
		FromHeader:    "thr <thr@test.invalid>",
		DateSent:      when,
		DateString:    when.Format(time.RFC1123Z),
		References:    strings.Join(refs, " "),
		RefSlice:      refs,
		ArticleNums:   make(map[*string]int64),
		NewsgroupsPtr: groups,
		IsThrRoot:     len(refs) == 0,
		IsReply:       len(refs) > 0,
	}
}

// thrIntakeRun queues arts for task's group and runs one batch for it, like the orchestrator.
func thrIntakeRun(t *testing.T, db *Database, task *BatchTasks, arts ...*models.Article) {
	t.Helper()
	adfBatchQueue(db.Batch, task, arts...)
	fuBatchRun(t, db, task, testWait, &fuBatchLogs{})
}

// thrRow is one row of a group's threads table, with -1 standing for a NULL parent_article.
type thrRow struct {
	root   int64
	parent int64
	child  int64
	depth  int
}

func (r thrRow) String() string {
	return fmt.Sprintf("{root:%d parent:%d child:%d depth:%d}", r.root, r.parent, r.child, r.depth)
}

// thrIntakeThreads returns the group's threads rows ordered by child_article.
func thrIntakeThreads(t *testing.T, db *Database, group string) []thrRow {
	t.Helper()
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	rows, err := RetryableQuery(groupDB.DB,
		"SELECT root_article, COALESCE(parent_article, -1), child_article, depth FROM threads ORDER BY child_article")
	if err != nil {
		t.Fatalf("select threads: %v", err)
	}
	defer rows.Close()
	var out []thrRow
	for rows.Next() {
		var r thrRow
		if err := rows.Scan(&r.root, &r.parent, &r.child, &r.depth); err != nil {
			t.Fatalf("scan threads: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("threads rows: %v", err)
	}
	return out
}

// thrIntakeRow returns the threads row of child, or false when the article is in no thread.
func thrIntakeRow(t *testing.T, db *Database, group string, child int64) (thrRow, bool) {
	t.Helper()
	for _, r := range thrIntakeThreads(t, db, group) {
		if r.child == child {
			return r, true
		}
	}
	return thrRow{}, false
}

// thrIntakeNum returns the article number message-id got in the group (0: not stored).
func thrIntakeNum(t *testing.T, db *Database, group, messageID string) int64 {
	t.Helper()
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	var num int64
	if err := RetryableQueryRowScan(groupDB.DB,
		"SELECT COALESCE((SELECT article_num FROM articles WHERE message_id = ?), 0)",
		[]interface{}{messageID}, &num); err != nil {
		t.Fatalf("select article_num of %s: %v", messageID, err)
	}
	return num
}

// thrIntakeReplyCount returns articles.reply_count of message-id in the group.
func thrIntakeReplyCount(t *testing.T, db *Database, group, messageID string) int {
	t.Helper()
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	var count int
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT reply_count FROM articles WHERE message_id = ?",
		[]interface{}{messageID}, &count); err != nil {
		t.Fatalf("select reply_count of %s: %v", messageID, err)
	}
	return count
}

// thrCacheRow is one thread_cache row: the thread's message count, its children and the two
// columns the batch writer maxes instead of overwriting.
type thrCacheRow struct {
	messageCount  int
	children      []int64
	lastChild     int64
	lastActivity  string
	childrenOrder []int64
}

// thrIntakeCache returns the group's thread_cache rows by thread root. last_activity comes back
// as the stored string, which this writer always formats as UTC "2006-01-02 15:04:05".
func thrIntakeCache(t *testing.T, db *Database, group string) map[int64]thrCacheRow {
	t.Helper()
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	rows, err := RetryableQuery(groupDB.DB,
		"SELECT thread_root, message_count, child_articles, COALESCE(last_child_number, 0), COALESCE(last_activity, '') FROM thread_cache")
	if err != nil {
		t.Fatalf("select thread_cache: %v", err)
	}
	defer rows.Close()
	out := make(map[int64]thrCacheRow)
	for rows.Next() {
		var root int64
		var count int
		var children string
		var lastChild int64
		var lastActivity string
		if err := rows.Scan(&root, &count, &children, &lastChild, &lastActivity); err != nil {
			t.Fatalf("scan thread_cache: %v", err)
		}
		entry := thrCacheRow{messageCount: count, lastChild: lastChild, lastActivity: lastActivity}
		for _, field := range strings.Split(children, ",") {
			if field == "" {
				continue
			}
			var num int64
			if _, err := fmt.Sscanf(field, "%d", &num); err != nil {
				t.Fatalf("thread_cache child_articles %q of root %d: %v", children, root, err)
			}
			entry.children = append(entry.children, num)
			entry.childrenOrder = append(entry.childrenOrder, num)
		}
		sort.Slice(entry.children, func(i, j int) bool { return entry.children[i] < entry.children[j] })
		out[root] = entry
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("thread_cache rows: %v", err)
	}
	return out
}

// thrIntakeThreadsCount reads GetThreadsCount for the group.
func thrIntakeThreadsCount(t *testing.T, db *Database, group string) int64 {
	t.Helper()
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	count, err := db.GetThreadsCount(groupDB)
	if err != nil {
		t.Fatalf("GetThreadsCount(%s): %v", group, err)
	}
	return count
}

// wantRoot asserts that num is a thread root of the group: (num, NULL, num, 0) plus its own
// thread_cache row.
func wantRoot(t *testing.T, db *Database, group string, num int64, what string) {
	t.Helper()
	row, ok := thrIntakeRow(t, db, group, num)
	if !ok {
		t.Fatalf("%s: article %d of '%s' is in no thread: %v", what, num, group, thrIntakeThreads(t, db, group))
	}
	want := thrRow{root: num, parent: -1, child: num, depth: 0}
	if row != want {
		t.Errorf("%s: threads row of article %d = %v, want %v", what, num, row, want)
	}
	if _, ok := thrIntakeCache(t, db, group)[num]; !ok {
		t.Errorf("%s: article %d is a thread root of '%s' but has no thread_cache row, so the group's thread list never shows it", what, num, group)
	}
}

// wantReply asserts that num hangs under parent in thread root with the given depth, and that
// the root's thread_cache row lists it.
func wantReply(t *testing.T, db *Database, group string, num, parent, root int64, depth int, what string) {
	t.Helper()
	row, ok := thrIntakeRow(t, db, group, num)
	if !ok {
		t.Fatalf("%s: article %d of '%s' is in no thread: %v", what, num, group, thrIntakeThreads(t, db, group))
	}
	want := thrRow{root: root, parent: parent, child: num, depth: depth}
	if row != want {
		t.Errorf("%s: threads row of article %d = %v, want %v", what, num, row, want)
	}
	entry, ok := thrIntakeCache(t, db, group)[root]
	if !ok {
		t.Fatalf("%s: thread root %d of '%s' has no thread_cache row", what, root, group)
	}
	found := false
	for _, child := range entry.children {
		if child == num {
			found = true
		}
	}
	if !found {
		t.Errorf("%s: thread_cache of root %d lists children %v, article %d is missing", what, root, entry.children, num)
	}
}

// TestThrIntakeUnknownReferenceIsRoot: a follow-up whose References name a message-id this
// group never carried starts a thread here (the reported bug: it used to be stored in no
// thread at all, so the group's thread list never showed it).
func TestThrIntakeUnknownReferenceIsRoot(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.unknown")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	followup := thrIntakeMsgID("followup")
	thrIntakeRun(t, db, task, thrIntakeArticle(followup, 1, []string{"<thr-absent-root@elsewhere.invalid>"}, task))

	num := thrIntakeNum(t, db, group, followup)
	if num == 0 {
		t.Fatalf("the follow-up was not stored in '%s'", group)
	}
	wantRoot(t, db, group, num, "unknown reference")
	if got := thrIntakeThreadsCount(t, db, group); got != 1 {
		t.Errorf("GetThreadsCount = %d, want 1", got)
	}
}

// TestThrIntakeCrosspostParentOnlyInOneGroup: the reported case. A follow-up crossposted to A
// and B whose parent is only in A is a reply in A and a thread root in B.
func TestThrIntakeCrosspostParentOnlyInOneGroup(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	groupA, groupB := w0Name("thrintake.cross.a"), w0Name("thrintake.cross.b")
	taskA := db.Batch.GetOrCreateTasksMapKey(groupA)
	taskB := db.Batch.GetOrCreateTasksMapKey(groupB)

	root := thrIntakeMsgID("root")
	thrIntakeRun(t, db, taskA, thrIntakeArticle(root, 1, nil, taskA))

	// One article, two groups: the batch writer runs once per group and the per-group rule
	// must answer differently for each.
	followup := thrIntakeMsgID("followup")
	crosspost := thrIntakeArticle(followup, 2, []string{root}, taskA, taskB)
	thrIntakeRun(t, db, taskA, crosspost)
	thrIntakeRun(t, db, taskB, crosspost)

	rootNum := thrIntakeNum(t, db, groupA, root)
	inA := thrIntakeNum(t, db, groupA, followup)
	inB := thrIntakeNum(t, db, groupB, followup)
	if rootNum == 0 || inA == 0 || inB == 0 {
		t.Fatalf("articles not stored: root in A = %d, follow-up in A = %d, in B = %d", rootNum, inA, inB)
	}
	wantRoot(t, db, groupA, rootNum, "crosspost: the root in A")
	wantReply(t, db, groupA, inA, rootNum, rootNum, 1, "crosspost: the follow-up in A")
	wantRoot(t, db, groupB, inB, "crosspost: the follow-up in B, whose thread lives in A")
	if got := thrIntakeThreadsCount(t, db, groupB); got != 1 {
		t.Errorf("GetThreadsCount in B = %d, want 1 (the follow-up is B's only thread)", got)
	}
	if got := thrIntakeThreadsCount(t, db, groupA); got != 1 {
		t.Errorf("GetThreadsCount in A = %d, want 1 (one thread of two articles)", got)
	}
	if got := thrIntakeReplyCount(t, db, groupA, root); got != 1 {
		t.Errorf("reply_count of the root in A = %d, want 1", got)
	}
}

// TestThrIntakeParentAfterReplyInSameBatch: the batch is sorted by DateSent, so a parent that
// sits behind its reply in the batch slice is still placed first and the reply attaches.
func TestThrIntakeParentAfterReplyInSameBatch(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.unsorted")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	parent := thrIntakeMsgID("parent")
	reply := thrIntakeMsgID("reply")
	// Reply first in the slice, parent older by one minute.
	thrIntakeRun(t, db, task,
		thrIntakeArticle(reply, 2, []string{parent}, task),
		thrIntakeArticle(parent, 1, nil, task),
	)

	parentNum := thrIntakeNum(t, db, group, parent)
	replyNum := thrIntakeNum(t, db, group, reply)
	if parentNum == 0 || replyNum == 0 {
		t.Fatalf("articles not stored: parent = %d, reply = %d", parentNum, replyNum)
	}
	wantRoot(t, db, group, parentNum, "reply first in the slice: the parent")
	wantReply(t, db, group, replyNum, parentNum, parentNum, 1, "reply first in the slice: the reply")
	if got := thrIntakeThreadsCount(t, db, group); got != 1 {
		t.Errorf("GetThreadsCount = %d, want 1", got)
	}
}

// TestThrIntakeOrphanChainOneBatch: reply -> orphan -> reply in a single batch. The orphan's
// own parent is not in the group, so it is a root; the reply to the orphan joins it and the
// reply to that reply sits at depth 2.
func TestThrIntakeOrphanChainOneBatch(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.chain1")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	foreign := "<thr-foreign-root@elsewhere.invalid>"
	orphan := thrIntakeMsgID("orphan")
	r1 := thrIntakeMsgID("r1")
	r2 := thrIntakeMsgID("r2")
	thrIntakeRun(t, db, task,
		thrIntakeArticle(orphan, 1, []string{foreign}, task),
		thrIntakeArticle(r1, 2, []string{foreign, orphan}, task),
		thrIntakeArticle(r2, 3, []string{foreign, orphan, r1}, task),
	)

	orphanNum := thrIntakeNum(t, db, group, orphan)
	r1Num := thrIntakeNum(t, db, group, r1)
	r2Num := thrIntakeNum(t, db, group, r2)
	wantRoot(t, db, group, orphanNum, "one batch: the orphan")
	wantReply(t, db, group, r1Num, orphanNum, orphanNum, 1, "one batch: the reply to the orphan")
	wantReply(t, db, group, r2Num, r1Num, orphanNum, 2, "one batch: the reply to the reply")
	if got := thrIntakeThreadsCount(t, db, group); got != 1 {
		t.Errorf("GetThreadsCount = %d, want 1: the chain is one thread", got)
	}
	cache := thrIntakeCache(t, db, group)[orphanNum]
	if cache.messageCount != 3 {
		t.Errorf("thread_cache message_count of root %d = %d, want 3", orphanNum, cache.messageCount)
	}
	if len(cache.children) != 2 || cache.children[0] != r1Num || cache.children[1] != r2Num {
		t.Errorf("thread_cache children of root %d = %v, want [%d %d]", orphanNum, cache.children, r1Num, r2Num)
	}
}

// TestThrIntakeOrphanChainTwoBatches: the same chain across batches. The orphan is placed in
// the first batch and found again in the group database by the second one, which also resolves
// its own reply from the batch map.
func TestThrIntakeOrphanChainTwoBatches(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.chain2")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	foreign := "<thr-foreign-root@elsewhere.invalid>"
	orphan := thrIntakeMsgID("orphan")
	r1 := thrIntakeMsgID("r1")
	r2 := thrIntakeMsgID("r2")
	thrIntakeRun(t, db, task, thrIntakeArticle(orphan, 1, []string{foreign}, task))
	thrIntakeRun(t, db, task,
		thrIntakeArticle(r1, 2, []string{foreign, orphan}, task),
		thrIntakeArticle(r2, 3, []string{foreign, orphan, r1}, task),
	)

	orphanNum := thrIntakeNum(t, db, group, orphan)
	r1Num := thrIntakeNum(t, db, group, r1)
	r2Num := thrIntakeNum(t, db, group, r2)
	wantRoot(t, db, group, orphanNum, "two batches: the orphan")
	wantReply(t, db, group, r1Num, orphanNum, orphanNum, 1, "two batches: the reply to the orphan (parent from the group database)")
	wantReply(t, db, group, r2Num, r1Num, orphanNum, 2, "two batches: the reply to the reply (parent from the batch map)")
	if got := thrIntakeThreadsCount(t, db, group); got != 1 {
		t.Errorf("GetThreadsCount = %d, want 1: the chain is one thread", got)
	}
	if got := thrIntakeReplyCount(t, db, group, orphan); got != 1 {
		t.Errorf("reply_count of the orphan = %d, want 1", got)
	}
	if got := thrIntakeReplyCount(t, db, group, r1); got != 1 {
		t.Errorf("reply_count of the first reply = %d, want 1", got)
	}
}

// TestThrIntakeTrimmedReferences: References trimmed to [root, r3] (RFC 5537 3.4.4) attach to
// r3, the nearest reference present, and inherit r3's thread root with depth r3 + 1. The old
// root-only lookup found the root instead and lost the position in the tree; a chain whose
// local hits are only replies got no thread row at all.
func TestThrIntakeTrimmedReferences(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.trimmed")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	root := thrIntakeMsgID("root")
	r1 := thrIntakeMsgID("r1")
	r2 := thrIntakeMsgID("r2")
	r3 := thrIntakeMsgID("r3")
	thrIntakeRun(t, db, task,
		thrIntakeArticle(root, 1, nil, task),
		thrIntakeArticle(r1, 2, []string{root}, task),
		thrIntakeArticle(r2, 3, []string{root, r1}, task),
		thrIntakeArticle(r3, 4, []string{root, r1, r2}, task),
	)
	trimmed := thrIntakeMsgID("trimmed")
	thrIntakeRun(t, db, task, thrIntakeArticle(trimmed, 5, []string{root, r3}, task))

	rootNum := thrIntakeNum(t, db, group, root)
	r3Num := thrIntakeNum(t, db, group, r3)
	trimmedNum := thrIntakeNum(t, db, group, trimmed)
	r3Row, ok := thrIntakeRow(t, db, group, r3Num)
	if !ok {
		t.Fatalf("r3 is in no thread: %v", thrIntakeThreads(t, db, group))
	}
	if r3Row.depth != 3 {
		t.Fatalf("depth of r3 = %d, want 3 (depth is the parent's + 1, not the reference position)", r3Row.depth)
	}
	wantReply(t, db, group, trimmedNum, r3Num, rootNum, r3Row.depth+1, "trimmed References")
	if got := thrIntakeThreadsCount(t, db, group); got != 1 {
		t.Errorf("GetThreadsCount = %d, want 1", got)
	}
}

// TestThrIntakeTwoRepliesOneParent: two replies to one parent in one batch bump the parent's
// reply_count by 2. Keyed by *string into each reply's RefSlice the map held one entry per
// reply, so the CASE had two arms for the same message-id and only the first applied (+1).
func TestThrIntakeTwoRepliesOneParent(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.replycount")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	parent := thrIntakeMsgID("parent")
	a := thrIntakeMsgID("a")
	b := thrIntakeMsgID("b")
	thrIntakeRun(t, db, task,
		thrIntakeArticle(parent, 1, nil, task),
		thrIntakeArticle(a, 2, []string{parent}, task),
		thrIntakeArticle(b, 3, []string{parent}, task),
	)

	parentNum := thrIntakeNum(t, db, group, parent)
	if got := thrIntakeReplyCount(t, db, group, parent); got != 2 {
		t.Errorf("reply_count of the parent = %d, want 2", got)
	}
	wantReply(t, db, group, thrIntakeNum(t, db, group, a), parentNum, parentNum, 1, "two replies: the first")
	wantReply(t, db, group, thrIntakeNum(t, db, group, b), parentNum, parentNum, 1, "two replies: the second")

	// A reference this group does not carry bumps nothing: reply_count counts the direct
	// replies stored HERE.
	foreign := thrIntakeMsgID("foreign-child")
	thrIntakeRun(t, db, task, thrIntakeArticle(foreign, 4, []string{"<thr-absent-parent@elsewhere.invalid>"}, task))
	if got := thrIntakeReplyCount(t, db, group, parent); got != 2 {
		t.Errorf("reply_count of the parent after an unrelated orphan = %d, want 2", got)
	}
}

// TestThrIntakeReplyBeforeParentStaysRoot: a reply stored before its parent is a root, and
// stays one when the parent arrives in a later batch - no re-parenting at intake (pinned;
// recover-db -rebuild-threads merges the two).
func TestThrIntakeReplyBeforeParentStaysRoot(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.outoforder")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	parent := thrIntakeMsgID("parent")
	reply := thrIntakeMsgID("reply")
	// The reply arrives first, although its DateSent is the later one.
	thrIntakeRun(t, db, task, thrIntakeArticle(reply, 2, []string{parent}, task))
	thrIntakeRun(t, db, task, thrIntakeArticle(parent, 1, nil, task))

	replyNum := thrIntakeNum(t, db, group, reply)
	parentNum := thrIntakeNum(t, db, group, parent)
	wantRoot(t, db, group, replyNum, "out of order: the reply that arrived first")
	wantRoot(t, db, group, parentNum, "out of order: the parent that arrived second")
	if got := thrIntakeThreadsCount(t, db, group); got != 2 {
		t.Errorf("GetThreadsCount = %d, want 2: both are roots until a rebuild merges them", got)
	}
	if got := thrIntakeReplyCount(t, db, group, parent); got != 0 {
		t.Errorf("reply_count of the parent = %d, want 0: the reply is not attached to it", got)
	}
}

// TestThrIntakeThreadsCountCountsRoots: GetThreadsCount counts thread roots, not threads rows.
// It feeds the stats total, which jumped after a rebuild while intake wrote no reply rows.
func TestThrIntakeThreadsCountCountsRoots(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.count")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	rootA := thrIntakeMsgID("roota")
	rootB := thrIntakeMsgID("rootb")
	thrIntakeRun(t, db, task,
		thrIntakeArticle(rootA, 1, nil, task),
		thrIntakeArticle(rootB, 2, nil, task),
		thrIntakeArticle(thrIntakeMsgID("a1"), 3, []string{rootA}, task),
		thrIntakeArticle(thrIntakeMsgID("a2"), 4, []string{rootA}, task),
		thrIntakeArticle(thrIntakeMsgID("b1"), 5, []string{rootB}, task),
	)

	rows := thrIntakeThreads(t, db, group)
	if len(rows) != 5 {
		t.Fatalf("threads rows = %d, want 5 (one per threaded article): %v", len(rows), rows)
	}
	if got := thrIntakeThreadsCount(t, db, group); got != 2 {
		t.Errorf("GetThreadsCount = %d, want 2 (two roots among %d rows)", got, len(rows))
	}
}

// TestThrIntakeDisableThreadingWritesNothing: with DBConfig.DisableThreading (nntp-server
// -disable-webserver-post-threads) the batch commits the articles and writes no threading row
// at all - not for roots, not for replies, and no thread_cache or reply_count either.
func TestThrIntakeDisableThreadingWritesNothing(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	db.dbconfig.DisableThreading = true
	group := w0Name("thrintake.disabled")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	parent := thrIntakeMsgID("parent")
	thrIntakeRun(t, db, task,
		thrIntakeArticle(parent, 1, nil, task),
		thrIntakeArticle(thrIntakeMsgID("reply"), 2, []string{parent}, task),
		thrIntakeArticle(thrIntakeMsgID("orphan"), 3, []string{"<thr-absent@elsewhere.invalid>"}, task),
	)

	if rows := thrIntakeThreads(t, db, group); len(rows) != 0 {
		t.Errorf("DisableThreading: threads rows = %v, want none", rows)
	}
	if cache := thrIntakeCache(t, db, group); len(cache) != 0 {
		t.Errorf("DisableThreading: thread_cache rows = %v, want none", cache)
	}
	if got := thrIntakeReplyCount(t, db, group, parent); got != 0 {
		t.Errorf("DisableThreading: reply_count of the parent = %d, want 0", got)
	}
	if got := thrIntakeThreadsCount(t, db, group); got != 0 {
		t.Errorf("DisableThreading: GetThreadsCount = %d, want 0", got)
	}
	var stored int
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", nil, &stored); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if stored != 3 {
		t.Errorf("DisableThreading: articles stored = %d, want 3", stored)
	}
}

// TestThrIntakePlacementIsOrderIndependent: a parent whose DateSent is LATER than its own
// reply's (a poster's clock behind, or a date an importer could not read) is still the reply's
// parent, although the batch is written oldest first and the parent is therefore written after
// its child. Placement looks at the whole batch at once; the sort only decides row order.
func TestThrIntakePlacementIsOrderIndependent(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.clockskew")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	parent := thrIntakeMsgID("parent")
	reply := thrIntakeMsgID("reply")
	// Parent first in the slice, so it gets the lower article number, but its Date is two
	// minutes AFTER the reply's: sorted oldest first, the reply is written first.
	thrIntakeRun(t, db, task,
		thrIntakeArticle(parent, 5, nil, task),
		thrIntakeArticle(reply, 3, []string{parent}, task),
	)

	parentNum := thrIntakeNum(t, db, group, parent)
	replyNum := thrIntakeNum(t, db, group, reply)
	if parentNum == 0 || replyNum == 0 {
		t.Fatalf("articles not stored: parent = %d, reply = %d", parentNum, replyNum)
	}
	wantRoot(t, db, group, parentNum, "clock skew: the parent with the later Date")
	wantReply(t, db, group, replyNum, parentNum, parentNum, 1, "clock skew: the reply with the earlier Date")
	if got := thrIntakeThreadsCount(t, db, group); got != 1 {
		t.Errorf("GetThreadsCount = %d, want 1: the reply must not become a second root (the rebuild puts it under the parent, and both must agree)", got)
	}
	if got := thrIntakeReplyCount(t, db, group, parent); got != 1 {
		t.Errorf("reply_count of the parent = %d, want 1", got)
	}
}

// TestThrIntakeReferenceCycle: two articles of one batch that reference each other (and an
// article that references itself) must not hang the placement walk. The article where the cycle
// is detected becomes a thread root, as the rebuild does.
func TestThrIntakeReferenceCycle(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.cycle")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	a := thrIntakeMsgID("cycle-a")
	b := thrIntakeMsgID("cycle-b")
	// A references B and B references A, both in the same batch. A is the older one, so the
	// walk starts at A and detects the cycle there.
	thrIntakeRun(t, db, task,
		thrIntakeArticle(a, 1, []string{b}, task),
		thrIntakeArticle(b, 2, []string{a}, task),
	)

	aNum := thrIntakeNum(t, db, group, a)
	bNum := thrIntakeNum(t, db, group, b)
	rows := thrIntakeThreads(t, db, group)
	if len(rows) != 2 {
		t.Fatalf("threads rows = %v, want one per article", rows)
	}
	if got := thrIntakeThreadsCount(t, db, group); got != 1 {
		t.Errorf("GetThreadsCount = %d, want 1: a cycle must leave exactly one root, not two and not none", got)
	}
	wantRoot(t, db, group, aNum, "cycle: the article the cycle was detected at")
	wantReply(t, db, group, bNum, aNum, aNum, 1, "cycle: the other article")

	// A self-reference is no parent either.
	selfMsgID := thrIntakeMsgID("self")
	self := thrIntakeArticle(selfMsgID, 3, nil, task)
	self.RefSlice = []string{selfMsgID}
	self.References = selfMsgID
	thrIntakeRun(t, db, task, self)
	wantRoot(t, db, group, thrIntakeNum(t, db, group, selfMsgID), "self-reference")
	if got := thrIntakeThreadsCount(t, db, group); got != 2 {
		t.Errorf("GetThreadsCount = %d, want 2 after the self-referencing article", got)
	}
}

// TestThrIntakeCacheActivityIsMaxed: a backfilled reply - older and, within its batch, not the
// last one written - must not move a thread down the group listing (which orders by
// last_activity DESC) nor lower last_child_number. Both columns are the maximum over the stored
// row and the batch, which is what the rebuild writes too.
func TestThrIntakeCacheActivityIsMaxed(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.maxed")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	root := thrIntakeMsgID("root")
	newest := thrIntakeMsgID("newest")
	thrIntakeRun(t, db, task,
		thrIntakeArticle(root, 0, nil, task),               // article 1, 12:00
		thrIntakeArticle(newest, 60, []string{root}, task), // article 2, 13:00
	)
	rootNum := thrIntakeNum(t, db, group, root)
	before := thrIntakeCache(t, db, group)[rootNum]
	if before.lastActivity != "2020-03-01 13:00:00" || before.lastChild != 2 {
		t.Fatalf("thread_cache after the first batch: last_activity %q, last_child_number %d, want \"2020-03-01 13:00:00\" and 2",
			before.lastActivity, before.lastChild)
	}

	// Two older replies, the higher-numbered one written FIRST (it is the older of the two), so
	// the batch's last written child (3) is not its highest (4) and both are older than 13:00.
	backfillA := thrIntakeMsgID("backfill-a") // article 3, 12:30
	backfillB := thrIntakeMsgID("backfill-b") // article 4, 12:20
	thrIntakeRun(t, db, task,
		thrIntakeArticle(backfillA, 30, []string{root}, task),
		thrIntakeArticle(backfillB, 20, []string{root}, task),
	)

	after := thrIntakeCache(t, db, group)[rootNum]
	if after.lastActivity != "2020-03-01 13:00:00" {
		t.Errorf("thread_cache last_activity = %q, want \"2020-03-01 13:00:00\": a backfilled older reply moved the thread", after.lastActivity)
	}
	if after.lastChild != 4 {
		t.Errorf("thread_cache last_child_number = %d, want 4 (the highest number in the thread, not the last one written: %v)", after.lastChild, after.childrenOrder)
	}
	if after.messageCount != 4 {
		t.Errorf("thread_cache message_count = %d, want 4", after.messageCount)
	}
	if len(after.children) != 3 || after.children[0] != 2 || after.children[1] != 3 || after.children[2] != 4 {
		t.Errorf("thread_cache children = %v, want [2 3 4]", after.children)
	}
	// The premise of the last_child_number check: this batch appended 4 before 3.
	if len(after.childrenOrder) == 3 && after.childrenOrder[2] != 3 {
		t.Logf("note: child_articles order is %v, so the clobbering this test pins is no longer reproducible this way", after.childrenOrder)
	}
}

// TestThrIntakeThreadsCountCountsLegacyZeroParent: cmd/fix-references writes an orphan root
// with parent_article = 0 instead of NULL, and such a row must still count as a thread.
func TestThrIntakeThreadsCountCountsLegacyZeroParent(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	group := w0Name("thrintake.zeroparent")
	task := db.Batch.GetOrCreateTasksMapKey(group)

	thrIntakeRun(t, db, task, thrIntakeArticle(thrIntakeMsgID("root"), 1, nil, task))
	if got := thrIntakeThreadsCount(t, db, group); got != 1 {
		t.Fatalf("GetThreadsCount = %d, want 1", got)
	}
	// A root the way cmd/fix-references leaves it.
	adfBatchExec(t, db, group,
		"INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, 0, ?, 0, 0)", 42, 42)
	if got := thrIntakeThreadsCount(t, db, group); got != 2 {
		t.Errorf("GetThreadsCount = %d, want 2: a root with parent_article 0 is a thread as well", got)
	}
}
