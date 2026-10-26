package database

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// w1APIUser inserts a real user row (user_spam_flags.user_id references users).
func w1APIUser(t *testing.T) *models.User {
	t.Helper()
	db := w0DB(t)
	name := fmt.Sprintf("w1api_user_%d", w0Seq.Add(1))
	if err := db.InsertUser(&models.User{Username: name, Email: name + "@test.invalid", PasswordHash: "x", DisplayName: name}); err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	u, err := db.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	return u
}

// w1APIGroup inserts an active newsgroup row and returns its name.
func w1APIGroup(t *testing.T) string {
	t.Helper()
	name := w0Name("w1api.grp")
	_, err := RetryableExec(w0DB(t).GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 0, 0, 1, ?)",
		name, ExtractHierarchyFromGroupName(name))
	if err != nil {
		t.Fatalf("insert newsgroup: %v", err)
	}
	return name
}

func TestW1APIFlagArticleSpamByUserConcurrent(t *testing.T) {
	db := w0DB(t)
	user := w1APIUser(t)
	group := w1APIGroup(t)

	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	_, err = db.InsertOverview(groupDB, &models.Overview{
		ArticleNum: 1, Subject: "spam", FromHeader: "a <a@b>", DateSent: time.Now(),
		DateString: "x", MessageID: "<w1api-spam-1@test.invalid>", Downloaded: 1,
	})
	groupDB.Return()
	if err != nil {
		t.Fatalf("InsertOverview: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	trues := 0
	var errs []error
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := db.FlagArticleSpamByUser(user.ID, group, 1)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				trues++
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("FlagArticleSpamByUser errors: %v", errs)
	}
	if trues != 1 {
		t.Fatalf("new flags = %d, want 1", trues)
	}

	groupDB, err = db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()
	var spam int
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT spam FROM articles WHERE article_num = 1", nil, &spam); err != nil {
		t.Fatalf("select spam: %v", err)
	}
	if spam != 1 {
		t.Fatalf("spam = %d, want 1", spam)
	}
	var flags, spamRows int
	if err := RetryableQueryRowScan(db.GetMainDB(),
		"SELECT (SELECT COUNT(*) FROM user_spam_flags WHERE user_id = ? AND article_num = 1 AND newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?)), "+
			"(SELECT COUNT(*) FROM spam WHERE article_num = 1 AND newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?))",
		[]interface{}{user.ID, group, group}, &flags, &spamRows); err != nil {
		t.Fatalf("count flags: %v", err)
	}
	if flags != 1 || spamRows != 1 {
		t.Fatalf("user_spam_flags rows = %d, spam rows = %d; want 1 and 1", flags, spamRows)
	}

	// A second user counts once more.
	other := w1APIUser(t)
	if ok, err := db.FlagArticleSpamByUser(other.ID, group, 1); err != nil || !ok {
		t.Fatalf("second user flag = %v, %v; want true, nil", ok, err)
	}

	// Unknown article: no flag row, ErrArticleNotFound.
	if ok, err := db.FlagArticleSpamByUser(user.ID, group, 999); !errors.Is(err, ErrArticleNotFound) || ok {
		t.Fatalf("missing article = %v, %v; want false, ErrArticleNotFound", ok, err)
	}
	// Unknown group: error before any group DB is opened.
	if _, err := db.FlagArticleSpamByUser(user.ID, w0Name("w1api.nosuch"), 1); err == nil {
		t.Fatal("unknown group: want error")
	}
}

func TestW1APITryReserveWebPostConcurrent(t *testing.T) {
	db := w0DB(t)
	user := w1APIUser(t)
	now := time.Now().Unix()

	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	trues := 0
	var errs []error
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := db.TryReserveWebPost(user.ID, now, 42)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				trues++
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("TryReserveWebPost errors: %v", errs)
	}
	if trues != 1 {
		t.Fatalf("reservations = %d, want 1", trues)
	}

	var postCount, last int64
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT post_count, lastpost_unix FROM users WHERE id = ?", []interface{}{user.ID}, &postCount, &last); err != nil {
		t.Fatalf("select user: %v", err)
	}
	if postCount != 1 || last != now {
		t.Fatalf("post_count=%d lastpost_unix=%d, want 1 and %d", postCount, last, now)
	}

	// Still inside the back-off, then after it.
	if ok, err := db.TryReserveWebPost(user.ID, now+41, 42); err != nil || ok {
		t.Fatalf("inside back-off = %v, %v; want false, nil", ok, err)
	}
	if ok, err := db.TryReserveWebPost(user.ID, now+42, 42); err != nil || !ok {
		t.Fatalf("after back-off = %v, %v; want true, nil", ok, err)
	}
}

func TestW1APIGetThreadsPaged(t *testing.T) {
	db := w0DB(t)
	group := w1APIGroup(t)
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()

	for i := int64(1); i <= 5; i++ {
		if _, err := RetryableExec(groupDB.DB,
			"INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (1, NULL, ?, 0, ?)", i, i); err != nil {
			t.Fatalf("insert thread: %v", err)
		}
	}

	cases := []struct {
		limit, offset int
		want          []int64 // child_article values
	}{
		{2, 0, []int64{1, 2}},
		{2, 2, []int64{3, 4}},
		{2, 4, []int64{5}},
		{10, 0, []int64{1, 2, 3, 4, 5}},
		{3, 10, nil},
	}
	for _, tc := range cases {
		got, err := db.GetThreadsPaged(groupDB, tc.limit, tc.offset)
		if err != nil {
			t.Fatalf("GetThreadsPaged(%d,%d): %v", tc.limit, tc.offset, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("GetThreadsPaged(%d,%d) len = %d, want %d", tc.limit, tc.offset, len(got), len(tc.want))
		}
		for i, th := range got {
			if th.ChildArticle != tc.want[i] || th.ParentArticle != nil {
				t.Fatalf("GetThreadsPaged(%d,%d)[%d] = child %d parent %v, want child %d parent nil",
					tc.limit, tc.offset, i, th.ChildArticle, th.ParentArticle, tc.want[i])
			}
		}
	}
}

func TestW1APIValidateAPITokenExpired(t *testing.T) {
	db := w0DB(t)
	past := time.Now().Add(-time.Hour)
	_, plain, err := db.CreateAPIToken("w1api", 0, &past)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if tok, err := db.ValidateAPIToken(plain); !errors.Is(err, ErrAPITokenExpired) || tok != nil {
		t.Fatalf("expired token = %v, %v; want nil, ErrAPITokenExpired", tok, err)
	}
	_, plain, err = db.CreateAPIToken("w1api", 0, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if tok, err := db.ValidateAPIToken(plain); err != nil || tok == nil {
		t.Fatalf("valid token = %v, %v; want token, nil", tok, err)
	}
}
