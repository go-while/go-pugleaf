package database

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fu_threadcount_test.go covers E5 of the web-db-followups plan: the two writers that
// permanently created the thread_cache.message_count / child_articles off-by-one that E2 can
// only work around on the read side.
//
// The invariant under test is the one every other writer keeps, including the rebuild path
// (db_rescan.go: newMessageCount := 1 + len(finalChildren)):
//
//	message_count == 1 + len(child_articles)   // 1 for the root article
//
// Both writers have a "cache row missing" fallback that inserts a row with message_count = 1
// and then counted the replies from 0 instead of from that 1, so the row they wrote was one
// low -- forever, because nothing recomputes message_count outside a rescan. A thread with
// exactly one such reply reported zero replies and served no reply page at all.

// fuThreadcountBase is the fixed root date of every thread these tests build.
var fuThreadcountBase = time.Date(2024, 7, 1, 10, 0, 0, 0, time.UTC)

// fuThreadcountGroup inserts a newsgroup row with a unique name and returns that name.
func fuThreadcountGroup(t *testing.T) string {
	t.Helper()
	name := w0Name("futhreadcount")
	if _, err := RetryableExec(w0DB(t).GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 0, 0, 1, ?)",
		name, ExtractHierarchyFromGroupName(name)); err != nil {
		t.Fatalf("insert newsgroup %s: %v", name, err)
	}
	return name
}

// fuThreadcountArticles opens the group database of a fresh newsgroup and inserts a visible
// root article 1 plus nChildren visible children numbered 2..nChildren+1. It deliberately
// writes **no** thread_cache row: that missing row is what sends both writers under test into
// their initialize-and-continue fallback.
func fuThreadcountArticles(t *testing.T, nChildren int) (*Database, *GroupDB) {
	t.Helper()
	db := w0DB(t)
	group := fuThreadcountGroup(t)
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	t.Cleanup(func() { gdb.Return() })

	for i := int64(1); i <= int64(nChildren)+1; i++ {
		if _, err := db.InsertOverview(gdb, &models.Overview{
			ArticleNum: i, Subject: fmt.Sprintf("s%d", i), FromHeader: "a <a@b>",
			DateSent: fuThreadcountBase.Add(time.Duration(i) * time.Minute), DateString: "x",
			MessageID: fmt.Sprintf("<%s-%d@test.invalid>", group, i), Downloaded: 1,
		}); err != nil {
			t.Fatalf("InsertOverview %d: %v", i, err)
		}
	}
	return db, gdb
}

// fuThreadcountRow reads back the one thread_cache row of threadRoot.
func fuThreadcountRow(t *testing.T, gdb *GroupDB, threadRoot int64) (messageCount int, childArticles string) {
	t.Helper()
	if err := RetryableQueryRowScan(gdb.DB,
		"SELECT message_count, child_articles FROM thread_cache WHERE thread_root = ?",
		[]interface{}{threadRoot}, &messageCount, &childArticles); err != nil {
		t.Fatalf("read thread_cache row %d: %v", threadRoot, err)
	}
	return messageCount, childArticles
}

// fuThreadcountCheckRow asserts the invariant on the row just written, and that it lists the
// wantChildren replies the caller added.
func fuThreadcountCheckRow(t *testing.T, gdb *GroupDB, threadRoot int64, wantChildren int) {
	t.Helper()
	messageCount, childArticles := fuThreadcountRow(t, gdb, threadRoot)
	children := threadCacheReplyCount(childArticles)
	if children != wantChildren {
		t.Errorf("child_articles = %q (%d entries), want %d", childArticles, children, wantChildren)
	}
	if messageCount != 1+children {
		t.Errorf("message_count = %d for %d children (child_articles %q), want %d = 1 root + %d replies",
			messageCount, children, childArticles, 1+children, children)
	}
}

// TestFuThreadcountUpdateThreadCacheFallbackCountsFromOne drives UpdateThreadCache's "cache
// row missing" fallback (thread_cache.go): the first reply of a thread whose root was
// processed without initializing the cache. The fallback calls InitializeThreadCache, which
// inserts message_count = 1, so the reply it then adds must make the row read 2 -- it used to
// read 1, which GetCachedThreadReplies reported as zero replies (no reply page at all, the
// thread's articles unreachable from the web).
func TestFuThreadcountUpdateThreadCacheFallbackCountsFromOne(t *testing.T) {
	for _, replies := range []int{1, 2, 5} {
		t.Run(fmt.Sprintf("%d_replies", replies), func(t *testing.T) {
			db, gdb := fuThreadcountArticles(t, replies)

			// Nothing seeded thread_cache, so the first UpdateThreadCache takes the fallback
			// and every later one takes the normal select-hit path.
			for child := int64(2); child <= int64(replies)+1; child++ {
				childDate := fuThreadcountBase.Add(time.Duration(child) * time.Minute)
				if err := db.UpdateThreadCache(gdb, 1, child, childDate); err != nil {
					t.Fatalf("UpdateThreadCache(child=%d): %v", child, err)
				}
				// The invariant has to hold after every single reply, not just at the end.
				fuThreadcountCheckRow(t, gdb, 1, int(child)-1)
			}

			// And the read path has to see exactly those replies.
			got, total, err := db.GetCachedThreadReplies(gdb, 1, 1, 50)
			if err != nil {
				t.Fatalf("GetCachedThreadReplies: %v", err)
			}
			if total != replies || len(got) != replies {
				t.Errorf("GetCachedThreadReplies: %d replies, total %d; want %d and %d",
					len(got), total, replies, replies)
			}
		})
	}
}

// TestFuThreadcountBatchUpdateFallbackCountsFromOne drives the same fallback in
// batchUpdateThreadCache (db_batch.go), where a select-miss inserts message_count = 1 and then
// applied all of the batch's accumulated updates on top of 0. With a batch of n replies the
// row used to read n instead of n + 1.
func TestFuThreadcountBatchUpdateFallbackCountsFromOne(t *testing.T) {
	for _, replies := range []int{1, 2, 5} {
		t.Run(fmt.Sprintf("%d_replies", replies), func(t *testing.T) {
			db, gdb := fuThreadcountArticles(t, replies)
			sq := db.Batch
			if sq == nil {
				t.Fatal("db.Batch is nil")
			}

			updates := make([]threadCacheUpdateData, 0, replies)
			for child := int64(2); child <= int64(replies)+1; child++ {
				updates = append(updates, threadCacheUpdateData{
					childArticleNum: child,
					childDate:       fuThreadcountBase.Add(time.Duration(child) * time.Minute),
				})
			}
			if err := sq.batchUpdateThreadCache(gdb, map[int64][]threadCacheUpdateData{1: updates}); err != nil {
				t.Fatalf("batchUpdateThreadCache: %v", err)
			}
			fuThreadcountCheckRow(t, gdb, 1, replies)

			// A second batch for the same thread takes the select-hit path and must keep
			// counting from the row it finds.
			next := int64(replies) + 2
			if _, err := db.InsertOverview(gdb, &models.Overview{
				ArticleNum: next, Subject: "s", FromHeader: "a <a@b>",
				DateSent: fuThreadcountBase.Add(time.Duration(next) * time.Minute), DateString: "x",
				MessageID: fmt.Sprintf("<%s-%d@test.invalid>", gdb.Newsgroup, next), Downloaded: 1,
			}); err != nil {
				t.Fatalf("InsertOverview %d: %v", next, err)
			}
			if err := sq.batchUpdateThreadCache(gdb, map[int64][]threadCacheUpdateData{
				1: {{childArticleNum: next, childDate: fuThreadcountBase.Add(time.Duration(next) * time.Minute)}},
			}); err != nil {
				t.Fatalf("batchUpdateThreadCache (second batch): %v", err)
			}
			fuThreadcountCheckRow(t, gdb, 1, replies+1)
		})
	}
}

// fuThreadcountMem returns an isolated memory thread cache. NewMemCachedThreads would start a
// CleanCron goroutine that never stops, and the shared test database's own cache is read by
// background goroutines, so these tests bring their own.
func fuThreadcountMem() *MemCachedThreads {
	return &MemCachedThreads{Groups: make(map[string]*MemGroupThreadCache, 4)}
}

// fuThreadcountListingReplies refreshes an isolated memory cache from the group database and
// returns the reply count the group listing would render for threadRoot.
func fuThreadcountListingReplies(t *testing.T, db *Database, gdb *GroupDB, threadRoot int64) int {
	t.Helper()
	mem := fuThreadcountMem()
	if err := mem.RefreshThreadCache(db, gdb, gdb.Newsgroup, 1, 25); err != nil {
		t.Fatalf("RefreshThreadCache: %v", err)
	}
	threads, _, hit := mem.GetCachedThreadsFromMemory(db, gdb, gdb.Newsgroup, 1, 25)
	if !hit {
		t.Fatal("GetCachedThreadsFromMemory: cache miss right after a refresh")
	}
	for _, th := range threads {
		if th.RootArticle != nil && th.RootArticle.ArticleNum == threadRoot {
			return th.MessageCount
		}
	}
	t.Fatalf("thread root %d not in the %d listed threads", threadRoot, len(threads))
	return 0
}

// TestFuThreadcountListingAgreesWithThreadPage: the reply count the group listing renders
// (GetCachedThreadsFromMemory) and the one the thread page paginates
// (GetCachedThreadReplies) must be the same number. The listing used to report
// message_count - 1, so on any row whose message_count was stale -- which is every row the two
// fallbacks ever wrote, and still every such row already on disk until a rescan -- it promised
// a different number of replies than the page served.
func TestFuThreadcountListingAgreesWithThreadPage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replies  int
		seedRow  bool // seed the row directly instead of going through UpdateThreadCache
		rowCount int  // message_count to seed with (seedRow only)
	}{
		{name: "fallback_created_one_reply", replies: 1},
		{name: "fallback_created_three_replies", replies: 3},
		// Rows already on disk: a stale message_count, low or high, must not make the two
		// ends disagree.
		{name: "stale_message_count_too_low", replies: 3, seedRow: true, rowCount: 1},
		{name: "stale_message_count_too_high", replies: 3, seedRow: true, rowCount: 99},
		{name: "consistent_message_count", replies: 3, seedRow: true, rowCount: 4},
		{name: "no_replies", replies: 0, seedRow: true, rowCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, gdb := fuThreadcountArticles(t, tc.replies)
			last := int64(tc.replies) + 1

			if tc.seedRow {
				var children []string
				for child := int64(2); child <= last; child++ {
					children = append(children, strconv.FormatInt(child, 10))
				}
				if _, err := RetryableExec(gdb.DB, `INSERT INTO thread_cache
					(thread_root, root_date, message_count, child_articles, last_child_number, last_activity)
					VALUES (1, ?, ?, ?, ?, ?)`,
					fuThreadcountBase, tc.rowCount, strings.Join(children, ","), last, fuThreadcountBase); err != nil {
					t.Fatalf("seed thread_cache: %v", err)
				}
			} else {
				for child := int64(2); child <= last; child++ {
					if err := db.UpdateThreadCache(gdb, 1, child,
						fuThreadcountBase.Add(time.Duration(child)*time.Minute)); err != nil {
						t.Fatalf("UpdateThreadCache(child=%d): %v", child, err)
					}
				}
			}

			_, pageReplies, err := db.GetCachedThreadReplies(gdb, 1, 1, 50)
			if err != nil {
				t.Fatalf("GetCachedThreadReplies: %v", err)
			}
			listingReplies := fuThreadcountListingReplies(t, db, gdb, 1)

			if listingReplies != pageReplies {
				t.Errorf("group listing says %d replies, thread page serves %d: the two disagree",
					listingReplies, pageReplies)
			}
			if pageReplies != tc.replies {
				t.Errorf("thread page serves %d replies, want %d", pageReplies, tc.replies)
			}
		})
	}
}

// TestFuThreadcountReplyCountHelper pins threadCacheReplyCount against the expression
// GetCachedThreadReplies derives its own count from, so the listing and the page cannot drift
// apart through two different parses of the same column.
func TestFuThreadcountReplyCountHelper(t *testing.T) {
	for _, in := range []string{"", "2", "2,3", "2,3,4", "10,11,12,13"} {
		childNums := strings.Split(in, ",")
		if len(childNums) == 1 && childNums[0] == "" {
			childNums = nil
		}
		if got, want := threadCacheReplyCount(in), len(childNums); got != want {
			t.Errorf("threadCacheReplyCount(%q) = %d, want %d", in, got, want)
		}
	}
}
