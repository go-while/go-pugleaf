package database

// Tests for the NEXT/LAST reads of plan nntp-rfc-commands (slice rfc-reader):
// NNTPNextArticle/NNTPPrevArticle skip hidden articles and give sql.ErrNoRows at the
// ends. Identifiers use the rdr/TestRdr prefixes.

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func rdrMsgID(group string, n int64) string {
	return fmt.Sprintf("<rdr-%d.%s@test.invalid>", n, group)
}

// rdrGroup creates a group DB with the visible articles 10, 20, 30 and the hidden 25.
func rdrGroup(t *testing.T) (*GroupDB, string) {
	t.Helper()
	db := w0DB(t)
	group := w0Name("rdr.next")
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	t.Cleanup(gdb.Return)
	err = RetryableTransactionExec(gdb.DB, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references",
			                       bytes, lines, path, headers_json, body_text, hide)
			 VALUES (?, ?, ?, 'tester@rdr.invalid', CURRENT_TIMESTAMP, 'Thu, 15 Oct 2026 10:00:00 +0000', '', 10, 1, 'not-for-mail', ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, a := range []struct{ num, hide int64 }{{10, 0}, {20, 0}, {25, 1}, {30, 0}} {
			if _, err := stmt.Exec(a.num, rdrMsgID(group, a.num), fmt.Sprintf("subject %d", a.num),
				fmt.Sprintf("Subject: subject %d", a.num), fmt.Sprintf("body %d", a.num), a.hide); err != nil {
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

func TestRdrNextPrevArticle(t *testing.T) {
	db := w0DB(t)
	gdb, group := rdrGroup(t)

	cases := []struct {
		name string
		next bool
		cur  int64
		want int64 // 0 = sql.ErrNoRows
	}{
		{"next of 10", true, 10, 20},
		{"next of 20 skips hidden 25", true, 20, 30},
		{"next of 30", true, 30, 0},
		{"prev of 10", false, 10, 0},
		{"prev of 30 skips hidden 25", false, 30, 20},
		{"next of 0", true, 0, 10},
	}
	for _, tc := range cases {
		var num int64
		var id string
		var err error
		if tc.next {
			num, id, err = db.NNTPNextArticle(gdb, tc.cur)
		} else {
			num, id, err = db.NNTPPrevArticle(gdb, tc.cur)
		}
		if tc.want == 0 {
			if !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("%s: got (%d, %q, %v), want sql.ErrNoRows", tc.name, num, id, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if num != tc.want || id != rdrMsgID(group, tc.want) {
			t.Errorf("%s: got (%d, %q), want (%d, %q)", tc.name, num, id, tc.want, rdrMsgID(group, tc.want))
		}
	}
}
