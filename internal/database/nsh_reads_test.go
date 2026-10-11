package database

// Tests for the NNTP reader reads of plan nntp-server-hardening (slice nntp-read): the
// hide = 0 filter, DBArtNum, watermarks, chunked article numbers and the NULL-safe active
// list. Identifiers use the nshRead/TestNSHRead prefixes.

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// nshReadGroup creates a group DB with articles 1..n and marks the numbers in hidden with
// hide = 1.
func nshReadGroup(t *testing.T, n int, hidden ...int64) (*GroupDB, string) {
	t.Helper()
	db := w0DB(t)
	group := w0Name("nshread")
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	t.Cleanup(gdb.Return)
	err = RetryableTransactionExec(gdb.DB, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references",
			                       bytes, lines, path, headers_json, body_text)
			 VALUES (?, ?, ?, 'tester@nshread.invalid', CURRENT_TIMESTAMP, 'Sat, 11 Oct 2026 10:00:00 +0000', '', 10, 1, 'not-for-mail', ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 1; i <= n; i++ {
			if _, err := stmt.Exec(i, nshReadMsgID(group, int64(i)), fmt.Sprintf("subject %d", i),
				fmt.Sprintf("Subject: subject %d", i), fmt.Sprintf("body %d", i)); err != nil {
				return err
			}
		}
		for _, h := range hidden {
			if _, err := tx.Exec(`UPDATE articles SET hide = 1 WHERE article_num = ?`, h); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed %s: %v", group, err)
	}
	return gdb, group
}

func nshReadMsgID(group string, n int64) string {
	return fmt.Sprintf("<nshread-%d.%s@test.invalid>", n, group)
}

func TestNSHReadByNumSetsDBArtNum(t *testing.T) {
	db := w0DB(t)
	gdb, group := nshReadGroup(t, 3)

	a, err := db.NNTPArticleByNum(gdb, 2)
	if err != nil {
		t.Fatalf("NNTPArticleByNum: %v", err)
	}
	if a.DBArtNum != 2 || a.MessageID != nshReadMsgID(group, 2) || a.BodyText != "body 2" || a.HeadersJSON != "Subject: subject 2" {
		t.Fatalf("NNTPArticleByNum: got num %d id %q body %q headers %q", a.DBArtNum, a.MessageID, a.BodyText, a.HeadersJSON)
	}
	if len(a.NewsgroupsPtr) != 1 || a.NewsgroupsPtr[0] == nil || *a.NewsgroupsPtr[0] != group || a.ArticleNums[gdb.NewsgroupPtr] != 2 {
		t.Fatalf("NNTPArticleByNum: NewsgroupsPtr %v ArticleNums %v", a.NewsgroupsPtr, a.ArticleNums)
	}

	a, err = db.NNTPArticleByMessageID(gdb, nshReadMsgID(group, 3))
	if err != nil {
		t.Fatalf("NNTPArticleByMessageID: %v", err)
	}
	if a.DBArtNum != 3 {
		t.Fatalf("NNTPArticleByMessageID: DBArtNum %d, want 3", a.DBArtNum)
	}

	// BUG-1 DB side: GetArticleByNum sets DBArtNum too.
	a, err = db.GetArticleByNum(gdb, 1)
	if err != nil {
		t.Fatalf("GetArticleByNum: %v", err)
	}
	if a.DBArtNum != 1 {
		t.Fatalf("GetArticleByNum: DBArtNum %d, want 1", a.DBArtNum)
	}
}

func TestNSHReadHideFiltered(t *testing.T) {
	db := w0DB(t)
	gdb, group := nshReadGroup(t, 4, 2, 4)
	hiddenID := nshReadMsgID(group, 2)

	if _, err := db.NNTPArticleByNum(gdb, 2); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("NNTPArticleByNum(hidden): err %v, want sql.ErrNoRows", err)
	}
	if _, err := db.NNTPArticleByMessageID(gdb, hiddenID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("NNTPArticleByMessageID(hidden): err %v, want sql.ErrNoRows", err)
	}
	if _, err := db.NNTPArticleMessageID(gdb, 2); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("NNTPArticleMessageID(hidden): err %v, want sql.ErrNoRows", err)
	}
	if id, err := db.NNTPArticleMessageID(gdb, 3); err != nil || id != nshReadMsgID(group, 3) {
		t.Errorf("NNTPArticleMessageID(3) = %q, %v", id, err)
	}
	// absent numbers and ids are sql.ErrNoRows as well
	if _, err := db.NNTPArticleByNum(gdb, 99); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("NNTPArticleByNum(absent): err %v, want sql.ErrNoRows", err)
	}
	if low, high, err := db.NNTPGroupWatermarks(gdb); err != nil || low != 1 || high != 3 {
		t.Errorf("NNTPGroupWatermarks = %d, %d, %v; want 1, 3 (4 is hidden)", low, high, err)
	}
	nums, err := db.NNTPArticleNumbers(gdb, 0, math.MaxInt64, 0)
	if err != nil || fmt.Sprint(nums) != "[1 3]" {
		t.Errorf("NNTPArticleNumbers = %v, %v; want [1 3]", nums, err)
	}

	// an inactive group is not listed by NNTPActiveNewsgroups
	name := w0Name("nshread.inactive")
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Description: "off", Active: false, Status: "y", LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	groups, err := db.NNTPActiveNewsgroups()
	if err != nil {
		t.Fatalf("NNTPActiveNewsgroups: %v", err)
	}
	for _, g := range groups {
		if g.Name == name {
			t.Errorf("NNTPActiveNewsgroups lists the inactive group %s", name)
		}
	}
}

func TestNSHReadWatermarks(t *testing.T) {
	db := w0DB(t)
	gdb, _ := nshReadGroup(t, 0)
	if low, high, err := db.NNTPGroupWatermarks(gdb); err != nil || low != 0 || high != 0 {
		t.Fatalf("empty group: NNTPGroupWatermarks = %d, %d, %v; want 0, 0", low, high, err)
	}
	gdb, _ = nshReadGroup(t, 5, 1, 2, 3, 4, 5)
	if low, high, err := db.NNTPGroupWatermarks(gdb); err != nil || low != 0 || high != 0 {
		t.Fatalf("all hidden: NNTPGroupWatermarks = %d, %d, %v; want 0, 0", low, high, err)
	}
	gdb, _ = nshReadGroup(t, 5, 1)
	if low, high, err := db.NNTPGroupWatermarks(gdb); err != nil || low != 2 || high != 5 {
		t.Fatalf("NNTPGroupWatermarks = %d, %d, %v; want 2, 5", low, high, err)
	}
}

func TestNSHReadNumbersChunks(t *testing.T) {
	db := w0DB(t)
	gdb, _ := nshReadGroup(t, 25, 7)
	var got []int64
	first := int64(1)
	calls := 0
	for {
		calls++
		nums, err := db.NNTPArticleNumbers(gdb, first, math.MaxInt64, 10)
		if err != nil {
			t.Fatalf("NNTPArticleNumbers(%d-): %v", first, err)
		}
		got = append(got, nums...)
		if len(nums) < 10 {
			break
		}
		first = nums[len(nums)-1] + 1
	}
	if len(got) != 24 || calls != 3 {
		t.Fatalf("chunks: %d numbers in %d calls, want 24 in 3", len(got), calls)
	}
	prev := int64(0)
	for _, n := range got {
		if n <= prev || n == 7 {
			t.Fatalf("chunks: numbers %v not ascending or contain the hidden 7", got)
		}
		prev = n
	}
	// a bounded and a reversed range
	if nums, err := db.NNTPArticleNumbers(gdb, 5, 9, 0); err != nil || fmt.Sprint(nums) != "[5 6 8 9]" {
		t.Fatalf("NNTPArticleNumbers(5-9) = %v, %v; want [5 6 8 9]", nums, err)
	}
	if nums, err := db.NNTPArticleNumbers(gdb, 9, 5, 0); err != nil || len(nums) != 0 {
		t.Fatalf("NNTPArticleNumbers(9-5) = %v, %v; want none", nums, err)
	}
}

func TestNSHReadActiveNullSafe(t *testing.T) {
	db := w0DB(t)
	name := w0Name("nshread.null")
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Description: "null row", Active: true, Status: "m", LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	if _, err := RetryableExec(db.GetMainDB(),
		`UPDATE newsgroups SET status = NULL, low_water = NULL, last_article = NULL WHERE name = ?`, name); err != nil {
		t.Fatalf("null columns of %s: %v", name, err)
	}
	groups, err := db.NNTPActiveNewsgroups()
	if err != nil {
		t.Fatalf("NNTPActiveNewsgroups: %v", err)
	}
	var found *models.Newsgroup
	prev := ""
	for _, g := range groups {
		if g.Name < prev {
			t.Fatalf("NNTPActiveNewsgroups not ordered by name: %q after %q", g.Name, prev)
		}
		prev = g.Name
		if g.Name == name {
			found = g
		}
	}
	if found == nil {
		t.Fatalf("NNTPActiveNewsgroups does not list %s", name)
	}
	if found.Status != "y" || found.LowWater != 0 || found.LastArticle != 0 || found.Description != "null row" || !found.Active {
		t.Fatalf("NULL row: got %+v", *found)
	}
}
