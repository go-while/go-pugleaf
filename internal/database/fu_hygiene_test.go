package database

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fuHygieneGroup inserts a newsgroup row with a unique name and returns that name.
func fuHygieneGroup(t *testing.T) string {
	t.Helper()
	name := w0Name("fuhygiene")
	if _, err := RetryableExec(w0DB(t).GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 0, 0, 1, ?)",
		name, ExtractHierarchyFromGroupName(name)); err != nil {
		t.Fatalf("insert newsgroup %s: %v", name, err)
	}
	return name
}

// fuHygieneThread seeds a fresh group DB with a visible root article 1 and nChildren visible
// children numbered 2..nChildren+1, plus the one thread_cache row for that thread. The row's
// message_count is whatever the caller passes: a message_count that disagrees with
// child_articles is exactly the state E2 is about.
func fuHygieneThread(t *testing.T, nChildren int, messageCount int) (*Database, *GroupDB) {
	t.Helper()
	db := w0DB(t)
	group := fuHygieneGroup(t)
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	t.Cleanup(func() { gdb.Return() })

	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	last := int64(nChildren) + 1
	var children []string
	for i := int64(1); i <= last; i++ {
		if _, err := db.InsertOverview(gdb, &models.Overview{
			ArticleNum: i, Subject: fmt.Sprintf("s%d", i), FromHeader: "a <a@b>",
			DateSent: base.Add(time.Duration(i) * time.Minute), DateString: "x",
			MessageID: fmt.Sprintf("<%s-%d@test.invalid>", group, i), Downloaded: 1,
		}); err != nil {
			t.Fatalf("InsertOverview %d: %v", i, err)
		}
		if i > 1 {
			children = append(children, strconv.FormatInt(i, 10))
		}
	}
	if _, err := RetryableExec(gdb.DB, `INSERT INTO thread_cache
		(thread_root, root_date, message_count, child_articles, last_child_number, last_activity)
		VALUES (1, ?, ?, ?, ?, ?)`,
		base, messageCount, strings.Join(children, ","), last, base); err != nil {
		t.Fatalf("seed thread_cache: %v", err)
	}
	return db, gdb
}

// TestFuHygieneThreadRepliesCountFromChildArticles: the reply count GetCachedThreadReplies
// returns must describe the list it actually paginates (child_articles), not thread_cache's
// message_count. Every page the returned count promises has to carry rows, and the page past
// it has to be empty — otherwise the caller's totalPages links a page that renders empty (E2).
func TestFuHygieneThreadRepliesCountFromChildArticles(t *testing.T) {
	const pageSize = 2
	for _, tc := range []struct {
		name         string
		children     int
		messageCount int
	}{
		// message_count too low: the old code returned 0 replies and no page at all.
		{"message_count below child_articles", 5, 1},
		// message_count too high: the old code promised 49 pages, 47 of them empty.
		{"message_count above child_articles", 5, 99},
		{"message_count consistent", 5, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, gdb := fuHygieneThread(t, tc.children, tc.messageCount)

			wantPages := (tc.children + pageSize - 1) / pageSize
			seen := 0
			for page := 1; page <= wantPages; page++ {
				replies, total, err := db.GetCachedThreadReplies(gdb, 1, page, pageSize)
				if err != nil {
					t.Fatalf("GetCachedThreadReplies(page=%d): %v", page, err)
				}
				if total != tc.children {
					t.Errorf("page %d: total replies = %d, want %d (one per child_articles entry)", page, total, tc.children)
				}
				if len(replies) == 0 {
					t.Errorf("page %d of the %d pages the count promises is empty", page, wantPages)
				}
				seen += len(replies)
			}
			if seen != tc.children {
				t.Errorf("the %d pages returned %d replies in total, want %d", wantPages, seen, tc.children)
			}

			// One past the last page the count promises: empty, but still the same count.
			replies, total, err := db.GetCachedThreadReplies(gdb, 1, wantPages+1, pageSize)
			if err != nil {
				t.Fatalf("GetCachedThreadReplies(page=%d): %v", wantPages+1, err)
			}
			if len(replies) != 0 {
				t.Errorf("page %d: %d replies, want 0", wantPages+1, len(replies))
			}
			if total != tc.children {
				t.Errorf("page %d: total replies = %d, want %d", wantPages+1, total, tc.children)
			}
		})
	}
}

// TestFuHygieneThreadRepliesEmptyChildArticles: a thread_cache row with no children reports
// no replies even when its message_count claims otherwise, and must not error.
func TestFuHygieneThreadRepliesEmptyChildArticles(t *testing.T) {
	db, gdb := fuHygieneThread(t, 0, 7)
	replies, total, err := db.GetCachedThreadReplies(gdb, 1, 1, 25)
	if err != nil {
		t.Fatalf("GetCachedThreadReplies: %v", err)
	}
	if len(replies) != 0 || total != 0 {
		t.Fatalf("%d replies, total %d; want 0 and 0", len(replies), total)
	}
}
