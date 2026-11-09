package database

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// thr_rebuild_test.go covers slice "rebuild" of .claude/plans/wip/thread-orphan-followups.md:
// the per-group root rule in RebuildThreadsFromScratch (db_rescan.go).
//
// The rule, applied per group with the whole group in memory:
//   - pass 1: an article's parent is the nearest reference (last to first) that exists in this
//     group; no such reference means no parent;
//   - pass 2: root and depth by following the parents, with a memo and a cycle guard (the
//     article where a cycle is detected becomes a root, a self-reference too).
//
// The row shapes these tests pin down are the contract the intake side (db_batch.go, slice
// "intake" of the same plan) has to produce as well for input that arrives in order:
//
//	root:  (root_article = child_article = the article, parent_article NULL, depth 0)
//	reply: (root_article = its thread root, parent_article = its parent, child_article = the
//	        article, depth = the parent's depth + 1)
//	thread_cache: one row per root, message_count = 1 + len(child_articles), child_articles
//	        listing every descendant of the root, ascending.
//
// These tests deliberately do not drive the intake path to compare the two: intake is being
// rewritten in the other slice of this plan, so a comparison here would only describe the
// intake code this worktree still has. The shapes above are the comparison.
//
// Out-of-order input is where the two sides differ on purpose (pinned in the plan): the
// rebuild merges an orphan under the parent that arrived later, intake leaves it a root.

// thrRebuildArt is one article of a test group: its number, its message-id and its
// References header (message-ids, space separated, may name articles that do not exist).
type thrRebuildArt struct {
	num  int64
	mid  string
	refs string
	// replyCount seeds articles.reply_count with a stale value the rebuild has to correct.
	replyCount int
}

// thrRebuildBase is the fixed date the fixture dates articles from.
var thrRebuildBase = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// thrRebuildGroup creates a newsgroup of its own and inserts arts into its group database,
// in the given order and with the given article numbers. It returns the shared test database,
// the group name and its (returned on cleanup) GroupDB.
func thrRebuildGroup(t *testing.T, arts []thrRebuildArt) (*Database, string, *GroupDB) {
	t.Helper()
	db := w0DB(t)
	group := w0Name("thrrebuild")
	if _, err := RetryableExec(db.GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 0, 0, 1, ?)",
		group, ExtractHierarchyFromGroupName(group)); err != nil {
		t.Fatalf("insert newsgroup %s: %v", group, err)
	}
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	t.Cleanup(func() { gdb.Return() })

	for _, art := range arts {
		if _, err := db.InsertOverview(gdb, &models.Overview{
			ArticleNum: art.num,
			Subject:    fmt.Sprintf("thr rebuild %d", art.num),
			FromHeader: "a <a@b.invalid>",
			DateSent:   thrRebuildBase.Add(time.Duration(art.num) * time.Minute),
			DateString: "x",
			MessageID:  art.mid,
			References: art.refs,
			ReplyCount: art.replyCount,
			Downloaded: 1,
		}); err != nil {
			t.Fatalf("InsertOverview %d (%s): %v", art.num, art.mid, err)
		}
	}
	return db, group, gdb
}

// thrRebuildRun rebuilds the threads of group and fails on an error or an error in the report.
// It passes the GroupDB explicitly, the way cmd/recover-db's -reorder-by-datesent path calls
// RebuildThreadsFromScratch (on the .new database it just filled).
func thrRebuildRun(t *testing.T, db *Database, group string, gdb *GroupDB) *ThreadRebuildReport {
	t.Helper()
	report, err := db.RebuildThreadsFromScratch(group, false, gdb)
	if err != nil {
		t.Fatalf("RebuildThreadsFromScratch(%s): %v", group, err)
	}
	if report == nil {
		t.Fatalf("RebuildThreadsFromScratch(%s): nil report", group)
	}
	if len(report.Errors) > 0 {
		t.Fatalf("RebuildThreadsFromScratch(%s) reported errors: %v", group, report.Errors)
	}
	return report
}

// thrRebuildRow is one threads row. parent 0 stands for parent_article NULL (a root).
type thrRebuildRow struct {
	root   int64
	parent int64
	child  int64
	depth  int
}

func (r thrRebuildRow) String() string {
	return fmt.Sprintf("{root:%d parent:%d child:%d depth:%d}", r.root, r.parent, r.child, r.depth)
}

// thrRebuildThreads reads every threads row, ordered by child_article.
func thrRebuildThreads(t *testing.T, gdb *GroupDB) []thrRebuildRow {
	t.Helper()
	rows, err := RetryableQuery(gdb.DB,
		"SELECT root_article, parent_article, child_article, depth FROM threads ORDER BY child_article")
	if err != nil {
		t.Fatalf("select threads: %v", err)
	}
	defer rows.Close()
	var out []thrRebuildRow
	for rows.Next() {
		var row thrRebuildRow
		var parent sql.NullInt64
		if err := rows.Scan(&row.root, &parent, &row.child, &row.depth); err != nil {
			t.Fatalf("scan threads row: %v", err)
		}
		row.parent = parent.Int64 // NULL -> 0
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate threads: %v", err)
	}
	return out
}

// thrRebuildWantThreads compares the threads rows of gdb with want (both by child_article).
func thrRebuildWantThreads(t *testing.T, gdb *GroupDB, want []thrRebuildRow) {
	t.Helper()
	got := thrRebuildThreads(t, gdb)
	sort.Slice(want, func(i, j int) bool { return want[i].child < want[j].child })
	if len(got) != len(want) {
		t.Fatalf("threads rows = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("threads row %d = %v, want %v (all rows: %v)", i, got[i], want[i], got)
		}
	}
}

// thrRebuildCacheRow is one thread_cache row as these tests compare them.
type thrRebuildCacheRow struct {
	messageCount int
	children     string
	lastChild    int64
	lastActivity time.Time
}

// thrRebuildCache reads the thread_cache rows of gdb by thread root.
func thrRebuildCache(t *testing.T, gdb *GroupDB) map[int64]thrRebuildCacheRow {
	t.Helper()
	rows, err := RetryableQuery(gdb.DB,
		"SELECT thread_root, message_count, child_articles, last_child_number, last_activity FROM thread_cache")
	if err != nil {
		t.Fatalf("select thread_cache: %v", err)
	}
	defer rows.Close()
	out := make(map[int64]thrRebuildCacheRow)
	for rows.Next() {
		var root int64
		var row thrRebuildCacheRow
		var activity interface{}
		if err := rows.Scan(&root, &row.messageCount, &row.children, &row.lastChild, &activity); err != nil {
			t.Fatalf("scan thread_cache row: %v", err)
		}
		row.lastActivity, _ = threadRebuildDate(activity)
		out[root] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate thread_cache: %v", err)
	}
	return out
}

// thrRebuildWantCache compares the thread_cache rows of gdb with want (root -> children,
// ascending and comma separated). message_count must be 1 + the number of children, and
// last_child_number / last_activity must describe the highest article of the thread (root or
// child): the fixture dates article n at thrRebuildBase + n minutes, so the newest date in a
// thread is the one of its highest article number.
func thrRebuildWantCache(t *testing.T, gdb *GroupDB, want map[int64]string) {
	t.Helper()
	got := thrRebuildCache(t, gdb)
	if len(got) != len(want) {
		t.Fatalf("thread_cache has %d rows (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for root, children := range want {
		row, ok := got[root]
		if !ok {
			t.Fatalf("thread_cache has no row for root %d (rows: %v)", root, got)
		}
		if row.children != children {
			t.Errorf("thread_cache root %d: child_articles = %q, want %q", root, row.children, children)
		}
		wantCount := 1
		wantLast := root
		if children != "" {
			fields := strings.Split(children, ",")
			wantCount = 1 + len(fields)
			for _, field := range fields {
				num, err := strconv.ParseInt(field, 10, 64)
				if err != nil {
					t.Fatalf("thread_cache root %d: child_articles = %q: %v", root, children, err)
				}
				if num > wantLast {
					wantLast = num
				}
			}
		}
		if row.messageCount != wantCount {
			t.Errorf("thread_cache root %d: message_count = %d, want %d (1 root + %q)",
				root, row.messageCount, wantCount, children)
		}
		if row.lastChild != wantLast {
			t.Errorf("thread_cache root %d: last_child_number = %d, want %d (the highest article of %d,%s)",
				root, row.lastChild, wantLast, root, children)
		}
		wantActivity := thrRebuildBase.Add(time.Duration(wantLast) * time.Minute)
		if !row.lastActivity.Equal(wantActivity) {
			t.Errorf("thread_cache root %d: last_activity = %s, want %s (the date of article %d)",
				root, row.lastActivity.UTC(), wantActivity.UTC(), wantLast)
		}
	}
}

// thrRebuildReplyCounts reads articles.reply_count by article number.
func thrRebuildReplyCounts(t *testing.T, gdb *GroupDB) map[int64]int64 {
	t.Helper()
	rows, err := RetryableQuery(gdb.DB, "SELECT article_num, reply_count FROM articles")
	if err != nil {
		t.Fatalf("select reply_count: %v", err)
	}
	defer rows.Close()
	out := make(map[int64]int64)
	for rows.Next() {
		var num, count int64
		if err := rows.Scan(&num, &count); err != nil {
			t.Fatalf("scan reply_count: %v", err)
		}
		out[num] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate reply_count: %v", err)
	}
	return out
}

// thrRebuildWantReplyCounts checks articles.reply_count against want; an article missing from
// want must have 0 replies.
func thrRebuildWantReplyCounts(t *testing.T, gdb *GroupDB, want map[int64]int64) {
	t.Helper()
	got := thrRebuildReplyCounts(t, gdb)
	for num, count := range got {
		if count != want[num] {
			t.Errorf("article %d: reply_count = %d, want %d (all: %v)", num, count, want[num], got)
		}
	}
	for num, count := range want {
		if _, ok := got[num]; !ok {
			t.Errorf("article %d missing from articles, want reply_count %d", num, count)
		}
	}
}

// TestThrRebuildUnknownReferenceIsRoot: a follow-up whose References name no article of this
// group is a thread root here, with References or without. This is the reported case (a
// follow-up filed only into group B while its thread lives in group A).
func TestThrRebuildUnknownReferenceIsRoot(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<plain@thr>", refs: ""},
		{num: 2, mid: "<orphan@thr>", refs: "<elsewhere@other.group>"},
		{num: 3, mid: "<orphan2@thr>", refs: "<root@other.group> <mid@other.group>"},
	})
	thrRebuildRun(t, db, group, gdb)

	thrRebuildWantThreads(t, gdb, []thrRebuildRow{
		{root: 1, parent: 0, child: 1, depth: 0},
		{root: 2, parent: 0, child: 2, depth: 0},
		{root: 3, parent: 0, child: 3, depth: 0},
	})
	thrRebuildWantCache(t, gdb, map[int64]string{1: "", 2: "", 3: ""})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{})
}

// TestThrRebuildParentAndReplies: a root with two direct replies, plus a second independent
// thread. Two replies to the same parent give reply_count 2 (the intake bug this plan fixes
// counted them as 1).
func TestThrRebuildParentAndReplies(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<r1@thr>", refs: ""},
		{num: 2, mid: "<a2@thr>", refs: "<r1@thr>"},
		{num: 3, mid: "<a3@thr>", refs: "<r1@thr>"},
		{num: 4, mid: "<r4@thr>", refs: ""},
		{num: 5, mid: "<a5@thr>", refs: "<r4@thr>"},
	})
	thrRebuildRun(t, db, group, gdb)

	thrRebuildWantThreads(t, gdb, []thrRebuildRow{
		{root: 1, parent: 0, child: 1, depth: 0},
		{root: 1, parent: 1, child: 2, depth: 1},
		{root: 1, parent: 1, child: 3, depth: 1},
		{root: 4, parent: 0, child: 4, depth: 0},
		{root: 4, parent: 4, child: 5, depth: 1},
	})
	thrRebuildWantCache(t, gdb, map[int64]string{1: "2,3", 4: "5"})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{1: 2, 4: 1})
}

// TestThrRebuildTrimmedReferences: References trimmed to [root, r3] (RFC 5537 3.4.4) attach
// the article to r3 - the nearest reference that exists here - at r3's depth + 1, not at the
// position the reference has in the header.
func TestThrRebuildTrimmedReferences(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<root@thr>", refs: ""},
		{num: 2, mid: "<r2@thr>", refs: "<root@thr>"},
		{num: 3, mid: "<r3@thr>", refs: "<root@thr> <r2@thr>"},
		{num: 4, mid: "<r4@thr>", refs: "<root@thr> <r3@thr>"},
	})
	thrRebuildRun(t, db, group, gdb)

	thrRebuildWantThreads(t, gdb, []thrRebuildRow{
		{root: 1, parent: 0, child: 1, depth: 0},
		{root: 1, parent: 1, child: 2, depth: 1},
		{root: 1, parent: 2, child: 3, depth: 2},
		{root: 1, parent: 3, child: 4, depth: 3},
	})
	thrRebuildWantCache(t, gdb, map[int64]string{1: "2,3,4"})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{1: 1, 2: 1, 3: 1})
}

// TestThrRebuildReplyChainToOrphan: a chain whose first article is an orphan root (its
// References name another group's article) keeps the orphan as the root of the whole chain.
func TestThrRebuildReplyChainToOrphan(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<f1@thr>", refs: "<gone@other.group>"},
		{num: 2, mid: "<f2@thr>", refs: "<gone@other.group> <f1@thr>"},
		{num: 3, mid: "<f3@thr>", refs: "<gone@other.group> <f1@thr> <f2@thr>"},
	})
	thrRebuildRun(t, db, group, gdb)

	thrRebuildWantThreads(t, gdb, []thrRebuildRow{
		{root: 1, parent: 0, child: 1, depth: 0},
		{root: 1, parent: 1, child: 2, depth: 1},
		{root: 1, parent: 2, child: 3, depth: 2},
	})
	thrRebuildWantCache(t, gdb, map[int64]string{1: "2,3"})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{1: 1, 2: 1})
}

// TestThrRebuildOutOfOrderMergesOrphan: the reply was filed before its parent (an archive or
// mbox import in any order), so the reply has the lower article number. The rebuild merges it
// under its parent - this is where it goes further than intake, which leaves both as roots
// (pinned in the plan).
func TestThrRebuildOutOfOrderMergesOrphan(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<reply@thr>", refs: "<parent@thr>"},
		{num: 2, mid: "<parent@thr>", refs: ""},
		{num: 3, mid: "<reply2@thr>", refs: "<parent@thr> <reply@thr>"},
	})
	thrRebuildRun(t, db, group, gdb)

	thrRebuildWantThreads(t, gdb, []thrRebuildRow{
		{root: 2, parent: 2, child: 1, depth: 1},
		{root: 2, parent: 0, child: 2, depth: 0},
		{root: 2, parent: 1, child: 3, depth: 2},
	})
	// The root's children are listed ascending even though child 1 was written before the
	// root existed: rebuildThreadRoots inserts every root before any child is appended.
	thrRebuildWantCache(t, gdb, map[int64]string{2: "1,3"})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{1: 1, 2: 1})
}

// TestThrRebuildReferenceCycle: A references B and B references A. The cycle is broken where
// it is detected, so exactly one of them is a root and the other its reply - and the rebuild
// returns instead of walking the cycle for ever.
func TestThrRebuildReferenceCycle(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<a@thr>", refs: "<b@thr>"},
		{num: 2, mid: "<b@thr>", refs: "<a@thr>"},
	})
	thrRebuildRun(t, db, group, gdb)

	rows := thrRebuildThreads(t, gdb)
	if len(rows) != 2 {
		t.Fatalf("threads rows = %v, want one row per article", rows)
	}
	var roots, replies []thrRebuildRow
	for _, row := range rows {
		if row.parent == 0 {
			roots = append(roots, row)
		} else {
			replies = append(replies, row)
		}
	}
	if len(roots) != 1 || len(replies) != 1 {
		t.Fatalf("threads rows = %v, want exactly one root and one reply", rows)
	}
	root, reply := roots[0], replies[0]
	if root.root != root.child || root.depth != 0 {
		t.Errorf("root row = %v, want root_article = child_article with depth 0", root)
	}
	if reply.parent != root.child || reply.root != root.child || reply.depth != 1 {
		t.Errorf("reply row = %v, want parent and root %d at depth 1", reply, root.child)
	}
	thrRebuildWantCache(t, gdb, map[int64]string{root.child: fmt.Sprintf("%d", reply.child)})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{root.child: 1})
}

// TestThrRebuildThreeCycleKeepsEveryArticleThreaded: a 3-cycle (A -> B -> C -> A) plus a
// reply hanging off it. Every article ends up in exactly one threads row.
func TestThrRebuildThreeCycleKeepsEveryArticleThreaded(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<c1@thr>", refs: "<c2@thr>"},
		{num: 2, mid: "<c2@thr>", refs: "<c3@thr>"},
		{num: 3, mid: "<c3@thr>", refs: "<c1@thr>"},
		{num: 4, mid: "<c4@thr>", refs: "<c3@thr>"},
	})
	thrRebuildRun(t, db, group, gdb)

	rows := thrRebuildThreads(t, gdb)
	if len(rows) != 4 {
		t.Fatalf("threads rows = %v, want one row per article", rows)
	}
	seen := make(map[int64]bool, len(rows))
	roots := 0
	for _, row := range rows {
		if seen[row.child] {
			t.Fatalf("article %d has more than one threads row (%v)", row.child, rows)
		}
		seen[row.child] = true
		if row.parent == 0 {
			roots++
			if row.root != row.child || row.depth != 0 {
				t.Errorf("root row = %v, want root_article = child_article with depth 0", row)
			}
			continue
		}
		if row.depth < 1 {
			t.Errorf("reply row = %v, want depth >= 1", row)
		}
	}
	if roots != 1 {
		t.Fatalf("threads rows = %v, want exactly one root", rows)
	}
}

// TestThrRebuildSelfReference: an article referencing itself is a root, not its own child.
func TestThrRebuildSelfReference(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<self@thr>", refs: "<self@thr>"},
		{num: 2, mid: "<kid@thr>", refs: "<self@thr>"},
	})
	thrRebuildRun(t, db, group, gdb)

	thrRebuildWantThreads(t, gdb, []thrRebuildRow{
		{root: 1, parent: 0, child: 1, depth: 0},
		{root: 1, parent: 1, child: 2, depth: 1},
	})
	thrRebuildWantCache(t, gdb, map[int64]string{1: "2"})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{1: 1})
}

// TestThrRebuildReplyCountRecomputed: reply_count is the number of direct children in this
// group, whatever the column held before the rebuild (nothing else recomputes it, so a
// wrong value stays until a rebuild).
func TestThrRebuildReplyCountRecomputed(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<rc1@thr>", refs: "", replyCount: 1},  // really has 2 replies
		{num: 2, mid: "<rc2@thr>", refs: "<rc1@thr>"},        // really has 1 reply
		{num: 3, mid: "<rc3@thr>", refs: "<rc1@thr>"},        //
		{num: 4, mid: "<rc4@thr>", refs: "<rc2@thr>"},        //
		{num: 5, mid: "<rc5@thr>", refs: "", replyCount: 99}, // really has none
	})
	thrRebuildRun(t, db, group, gdb)

	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{1: 2, 2: 1})
	thrRebuildWantCache(t, gdb, map[int64]string{1: "2,3,4", 5: ""})
}

// TestThrRebuildIsRepeatable: a second rebuild of the same group writes the same rows (it
// clears the threads and cache tables first, so nothing accumulates).
func TestThrRebuildIsRepeatable(t *testing.T) {
	arts := []thrRebuildArt{
		{num: 1, mid: "<rep1@thr>", refs: ""},
		{num: 2, mid: "<rep2@thr>", refs: "<rep1@thr>"},
		{num: 3, mid: "<rep3@thr>", refs: "<rep2@thr>"},
		{num: 4, mid: "<rep4@thr>", refs: "<nowhere@other.group>"},
	}
	db, group, gdb := thrRebuildGroup(t, arts)
	first := thrRebuildRun(t, db, group, gdb)
	wantThreads := thrRebuildThreads(t, gdb)
	wantCache := thrRebuildCache(t, gdb)
	wantCounts := thrRebuildReplyCounts(t, gdb)

	if first.TotalArticles != int64(len(arts)) {
		t.Errorf("report.TotalArticles = %d, want %d", first.TotalArticles, len(arts))
	}
	if first.ThreadsRebuilt != 2 { // the two roots: 1 and the orphan 4
		t.Errorf("report.ThreadsRebuilt = %d, want 2", first.ThreadsRebuilt)
	}

	second := thrRebuildRun(t, db, group, gdb)
	if second.ThreadsDeleted != int64(len(arts)) {
		t.Errorf("second run report.ThreadsDeleted = %d, want %d (one row per article)",
			second.ThreadsDeleted, len(arts))
	}
	thrRebuildWantThreads(t, gdb, wantThreads)
	if got := thrRebuildCache(t, gdb); fmt.Sprint(got) != fmt.Sprint(wantCache) {
		t.Errorf("thread_cache after the second rebuild = %v, want %v", got, wantCache)
	}
	if got := thrRebuildReplyCounts(t, gdb); fmt.Sprint(got) != fmt.Sprint(wantCounts) {
		t.Errorf("reply counts after the second rebuild = %v, want %v", got, wantCounts)
	}
}

// TestThrRebuildUnreadableDateSent: an article whose date_sent is NULL, or text no date
// format matches, still ends up threaded. Reading such a row used to abort the group's
// rebuild - after the thread tables had been cleared, so the group was left with no threads
// at all. The unreadable date becomes the zero time: the thread sorts last in a listing
// ordered by last_activity instead of hiding its articles.
func TestThrRebuildUnreadableDateSent(t *testing.T) {
	db, group, gdb := thrRebuildGroup(t, []thrRebuildArt{
		{num: 1, mid: "<nd1@thr>", refs: ""},
		{num: 2, mid: "<nd2@thr>", refs: "<nd1@thr>"},
		{num: 3, mid: "<nd3@thr>", refs: ""},
	})
	if _, err := RetryableExec(gdb.DB, "UPDATE articles SET date_sent = NULL WHERE article_num = 2"); err != nil {
		t.Fatalf("null the date of article 2: %v", err)
	}
	if _, err := RetryableExec(gdb.DB, "UPDATE articles SET date_sent = 'not-a-date' WHERE article_num = 3"); err != nil {
		t.Fatalf("break the date of article 3: %v", err)
	}
	thrRebuildRun(t, db, group, gdb)

	thrRebuildWantThreads(t, gdb, []thrRebuildRow{
		{root: 1, parent: 0, child: 1, depth: 0},
		{root: 1, parent: 1, child: 2, depth: 1},
		{root: 3, parent: 0, child: 3, depth: 0},
	})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{1: 1})

	cache := thrRebuildCache(t, gdb)
	if len(cache) != 2 {
		t.Fatalf("thread_cache = %v, want one row per root (1 and 3)", cache)
	}
	// Root 1 keeps its own date: its only child has no readable one.
	row := cache[1]
	if row.children != "2" || row.lastChild != 2 {
		t.Errorf("thread_cache root 1 = %+v, want child_articles \"2\" and last_child_number 2", row)
	}
	if want := thrRebuildBase.Add(time.Minute); !row.lastActivity.Equal(want) {
		t.Errorf("thread_cache root 1: last_activity = %s, want the root's own date %s",
			row.lastActivity.UTC(), want.UTC())
	}
	// Root 3's own date is unreadable: the row exists, with the zero date.
	row = cache[3]
	if row.children != "" || row.messageCount != 1 {
		t.Errorf("thread_cache root 3 = %+v, want no children and message_count 1", row)
	}
	if !row.lastActivity.IsZero() {
		t.Errorf("thread_cache root 3: last_activity = %s, want the zero date", row.lastActivity.UTC())
	}
}

// TestThrRebuildAcrossChunks: more articles than one rebuild chunk, so every pass pages, a
// root's children are spread over several chunks and a reply lands in a chunk before the one
// holding its root. RescanBatchSize is a package global the rebuild reads, so this test may
// not run in parallel with another one.
func TestThrRebuildAcrossChunks(t *testing.T) {
	const chunk = 4
	// 1 is the root of 2..9 (children in every chunk); 11 is a reply to the later root 12.
	arts := []thrRebuildArt{{num: 1, mid: "<big1@thr>", refs: ""}}
	for num := int64(2); num <= 9; num++ {
		arts = append(arts, thrRebuildArt{num: num, mid: fmt.Sprintf("<big%d@thr>", num), refs: "<big1@thr>"})
	}
	arts = append(arts,
		thrRebuildArt{num: 11, mid: "<late-reply@thr>", refs: "<late-root@thr>"},
		thrRebuildArt{num: 12, mid: "<late-root@thr>", refs: ""},
	)
	db, group, gdb := thrRebuildGroup(t, arts)

	old := RescanBatchSize
	RescanBatchSize = chunk
	t.Cleanup(func() { RescanBatchSize = old })

	thrRebuildRun(t, db, group, gdb)

	want := []thrRebuildRow{
		{root: 1, parent: 0, child: 1, depth: 0},
		{root: 12, parent: 12, child: 11, depth: 1},
		{root: 12, parent: 0, child: 12, depth: 0},
	}
	for num := int64(2); num <= 9; num++ {
		want = append(want, thrRebuildRow{root: 1, parent: 1, child: num, depth: 1})
	}
	thrRebuildWantThreads(t, gdb, want)
	thrRebuildWantCache(t, gdb, map[int64]string{1: "2,3,4,5,6,7,8,9", 12: "11"})
	thrRebuildWantReplyCounts(t, gdb, map[int64]int64{1: 8, 12: 1})
}
