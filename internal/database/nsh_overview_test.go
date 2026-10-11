package database

// Tests for the NNTP overview reads of plan nntp-server-hardening (slice nntp-overview):
// chunked range reads with the hide = 0 filter, header values from headers_json and the
// message-id header lookup. Identifiers use the nshOvr/TestNSHOvr prefixes.

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"testing"
)

// nshOvrGroup creates a group DB with articles 1..n (headers_json holds Subject, X-Seq and,
// for article 1, a folded X-Fold header) and marks the numbers in hidden with hide = 1.
func nshOvrGroup(t *testing.T, n int, hidden ...int64) (*GroupDB, string) {
	t.Helper()
	db := w0DB(t)
	group := w0Name("nshovr")
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	t.Cleanup(gdb.Return)
	err = RetryableTransactionExec(gdb.DB, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references",
			                       bytes, lines, path, headers_json, body_text)
			 VALUES (?, ?, ?, 'tester@nshovr.invalid', CURRENT_TIMESTAMP, 'Sat, 11 Oct 2026 10:00:00 +0000', '', ?, 2, 'not-for-mail', ?, 'body')`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 1; i <= n; i++ {
			// raw upstream Path and Xref lines, as the fetcher stores them
			headers := fmt.Sprintf("Path: upstream.example!raw\nSubject: subject %d\nX-Seq: %d\nXref: upstream.example %s:%d", i, i, group, 9000+i)
			if i == 1 {
				headers += "\nX-Fold: first\n\tsecond\n  third\nX-Seq: duplicate"
			}
			if _, err := stmt.Exec(i, nshOvrMsgID(group, int64(i)), fmt.Sprintf("subject %d", i), 100+i, headers); err != nil {
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

func nshOvrMsgID(group string, n int64) string {
	return fmt.Sprintf("<nshovr-%d.%s@test.invalid>", n, group)
}

func TestNSHOvrHeaderValueFromJSON(t *testing.T) {
	headers := "Path: a!b\r\nSubject: Hello\n  World\nsubject: second\nX-Empty:\nX-Fold: one\n\ttwo\n three\nFrom: x@y"
	for _, tc := range []struct{ field, want string }{
		{"Subject", "Hello World"}, // first occurrence, unfolded
		{"SUBJECT", "Hello World"}, // case-insensitive
		{"path", "a!b"},            // CR stripped
		{"x-fold", "one two three"},
		{"X-Empty", ""},
		{"X-Absent", ""},
		{"From", "x@y"},
		{"", ""},
	} {
		if got := HeaderValueFromJSON(headers, tc.field); got != tc.want {
			t.Errorf("HeaderValueFromJSON(%q) = %q, want %q", tc.field, got, tc.want)
		}
	}
	if got := HeaderValueFromJSON("", "Subject"); got != "" {
		t.Errorf("empty headers: got %q", got)
	}
}

func TestNSHOvrRangeChunks(t *testing.T) {
	db := w0DB(t)
	gdb, _ := nshOvrGroup(t, 2500, 10, 1500)

	// Overview chunks: 1000/1000/498 visible rows (10 and 1500 are hidden), in order.
	var got []int64
	start := int64(1)
	var sizes []int
	for {
		chunk, err := db.GetOverviewsRange(gdb, start, math.MaxInt64, OverviewRangeChunk)
		if err != nil {
			t.Fatalf("GetOverviewsRange(%d-): %v", start, err)
		}
		sizes = append(sizes, len(chunk))
		for _, o := range chunk {
			got = append(got, o.ArticleNum)
		}
		if len(chunk) < OverviewRangeChunk {
			break
		}
		start = chunk[len(chunk)-1].ArticleNum + 1
	}
	if fmt.Sprint(sizes) != "[1000 1000 498]" {
		t.Fatalf("chunk sizes %v, want [1000 1000 498]", sizes)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("not in order at %d: %d after %d", i, got[i], got[i-1])
		}
	}
	for _, n := range got {
		if n == 10 || n == 1500 {
			t.Fatalf("hidden article %d returned", n)
		}
	}

	// limit <= 0 means OverviewRangeChunk.
	if chunk, err := db.GetOverviewsRange(gdb, 1, math.MaxInt64, 0); err != nil || len(chunk) != OverviewRangeChunk {
		t.Fatalf("GetOverviewsRange limit 0: %d rows, %v", len(chunk), err)
	}
	// A reversed range is an error, an empty one no rows.
	if _, err := db.GetOverviewsRange(gdb, 5, 4, 10); err == nil {
		t.Fatal("GetOverviewsRange(5, 4): want an error")
	}
	if chunk, err := db.GetOverviewsRange(gdb, 3000, math.MaxInt64, 10); err != nil || len(chunk) != 0 {
		t.Fatalf("GetOverviewsRange(3000-): %d rows, %v", len(chunk), err)
	}

	// Header chunks: same shape for a column field and for a headers_json field.
	for _, field := range []string{"subject", "X-Seq", "bytes"} {
		var nums []int64
		start := int64(1)
		var sizes []int
		for {
			chunk, err := db.GetHeaderFieldRange(gdb, field, start, math.MaxInt64, OverviewRangeChunk)
			if err != nil {
				t.Fatalf("GetHeaderFieldRange(%s, %d-): %v", field, start, err)
			}
			sizes = append(sizes, len(chunk))
			for _, hv := range chunk {
				nums = append(nums, hv.ArticleNum)
				var want string
				switch field {
				case "subject":
					want = fmt.Sprintf("subject %d", hv.ArticleNum)
				case "X-Seq":
					want = fmt.Sprint(hv.ArticleNum) // first occurrence for article 1
				case "bytes":
					want = fmt.Sprint(100 + hv.ArticleNum)
				}
				if hv.Value != want {
					t.Fatalf("%s of %d = %q, want %q", field, hv.ArticleNum, hv.Value, want)
				}
			}
			if len(chunk) < OverviewRangeChunk {
				break
			}
			start = chunk[len(chunk)-1].ArticleNum + 1
		}
		if fmt.Sprint(sizes) != "[1000 1000 498]" {
			t.Fatalf("%s chunk sizes %v, want [1000 1000 498]", field, sizes)
		}
		for _, n := range nums {
			if n == 10 || n == 1500 {
				t.Fatalf("%s: hidden article %d returned", field, n)
			}
		}
	}

	// An absent header gives one element per visible row with an empty value.
	chunk, err := db.GetHeaderFieldRange(gdb, "X-Absent", 1, 12, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk) != 11 {
		t.Fatalf("X-Absent 1-12: %d rows, want 11", len(chunk))
	}
	for _, hv := range chunk {
		if hv.Value != "" {
			t.Fatalf("X-Absent of %d = %q, want empty", hv.ArticleNum, hv.Value)
		}
	}
	if chunk, err := db.GetHeaderFieldRange(gdb, "x-fold", 1, 1, 0); err != nil || len(chunk) != 1 || chunk[0].Value != "first second third" {
		t.Fatalf("x-fold of 1: %+v, %v", chunk, err)
	}

	// Xref: one empty value per visible row; Path: the path column, not the raw line.
	for _, tc := range []struct{ field, want string }{{"Xref", ""}, {"XREF", ""}, {"Path", "not-for-mail"}, {"path", "not-for-mail"}} {
		chunk, err := db.GetHeaderFieldRange(gdb, tc.field, 1, 12, 0)
		if err != nil || len(chunk) != 11 {
			t.Fatalf("%s 1-12: %d rows, %v; want 11", tc.field, len(chunk), err)
		}
		for _, hv := range chunk {
			if hv.Value != tc.want {
				t.Fatalf("%s of %d = %q, want %q", tc.field, hv.ArticleNum, hv.Value, tc.want)
			}
		}
	}
}

func TestNSHOvrRangeNulls(t *testing.T) {
	db := w0DB(t)
	gdb, _ := nshOvrGroup(t, 1)
	// Every nullable overview column NULL: the row must still be returned.
	if _, err := RetryableExec(gdb.DB,
		`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, headers_json)
		 VALUES (2, '<nshovr-null@test.invalid>', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`); err != nil {
		t.Fatalf("insert NULL row: %v", err)
	}
	chunk, err := db.GetOverviewsRange(gdb, 1, math.MaxInt64, 0)
	if err != nil {
		t.Fatalf("GetOverviewsRange with a NULL row: %v", err)
	}
	if len(chunk) != 2 || chunk[1].ArticleNum != 2 || chunk[1].Subject != "" || chunk[1].Bytes != 0 || !chunk[1].DateSent.IsZero() {
		t.Fatalf("NULL row: got %d rows, last %+v", len(chunk), chunk[len(chunk)-1])
	}
	hv, err := db.GetHeaderFieldRange(gdb, "X-Seq", 2, 2, 0)
	if err != nil || len(hv) != 1 || hv[0].Value != "" {
		t.Fatalf("X-Seq of the NULL row: %+v, %v", hv, err)
	}
}

func TestNSHOvrHeaderByMessageID(t *testing.T) {
	db := w0DB(t)
	gdb, group := nshOvrGroup(t, 3, 2)

	num, value, err := db.NNTPHeaderByMessageID(gdb, "Subject", nshOvrMsgID(group, 3))
	if err != nil || num != 3 || value != "subject 3" {
		t.Fatalf("Subject of 3: %d %q %v", num, value, err)
	}
	num, value, err = db.NNTPHeaderByMessageID(gdb, "x-fold", nshOvrMsgID(group, 1))
	if err != nil || num != 1 || value != "first second third" {
		t.Fatalf("X-Fold of 1: %d %q %v", num, value, err)
	}
	num, value, err = db.NNTPHeaderByMessageID(gdb, "Lines", nshOvrMsgID(group, 1))
	if err != nil || num != 1 || value != "2" {
		t.Fatalf("Lines of 1: %d %q %v", num, value, err)
	}
	// Xref is never served (it holds the upstream numbers), Path comes from the path column.
	for _, f := range []string{"Xref", "XREF", "xref"} {
		num, value, err = db.NNTPHeaderByMessageID(gdb, f, nshOvrMsgID(group, 1))
		if err != nil || num != 1 || value != "" {
			t.Fatalf("%s of 1: %d %q %v, want empty", f, num, value, err)
		}
	}
	for _, f := range []string{"Path", "PATH"} {
		num, value, err = db.NNTPHeaderByMessageID(gdb, f, nshOvrMsgID(group, 1))
		if err != nil || num != 1 || value != "not-for-mail" {
			t.Fatalf("%s of 1: %d %q %v, want the path column", f, num, value, err)
		}
	}
	num, value, err = db.NNTPHeaderByMessageID(gdb, "X-Absent", nshOvrMsgID(group, 1))
	if err != nil || num != 1 || value != "" {
		t.Fatalf("X-Absent of 1: %d %q %v", num, value, err)
	}
	if _, _, err := db.NNTPHeaderByMessageID(gdb, "Subject", nshOvrMsgID(group, 2)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("hidden article: err %v, want sql.ErrNoRows", err)
	}
	if _, _, err := db.NNTPHeaderByMessageID(gdb, "X-Seq", nshOvrMsgID(group, 2)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("hidden article (headers_json field): err %v, want sql.ErrNoRows", err)
	}
	if _, _, err := db.NNTPHeaderByMessageID(gdb, "Subject", "<absent@test.invalid>"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absent article: err %v, want sql.ErrNoRows", err)
	}
}
