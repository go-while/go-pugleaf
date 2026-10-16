package nntp

// NNTP server group and listing tests (plan nntp-audit-tests, slice test-nntp-group).
//
// Covers GROUP (RFC 3977 §6.1.1), LISTGROUP (§6.1.2), LIST ACTIVE / NEWSGROUPS /
// OVERVIEW.FMT (§7.6, §8.4) and the READER commands DATE (§7.1), NEXT/LAST
// (§6.1.3, §6.1.4), NEWGROUPS (§7.3) and NEWNEWS (§7.4; not implemented, not
// advertised). Every assertion states the RFC-correct behavior; where the
// server differs today the result of that check goes to knownBug with the seed
// id (BUG-4, SEC-2, SEC-7) or a new T-nntp-group-<n> id.
//
// Identifiers use the grp/TestGrp prefix (contract K2). Groups are named
// t3.<test>.<seq> (hGroup appends the sequence number).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// grpSkip12 leaves articles 1 and 2 out ("deleted"), so 1..5 gives 3, 4, 5.
func grpSkip12(i int, a *hArticle) {
	if i <= 2 {
		a.Skip = true
	}
}

// grpParse211 parses a "211 count low high group" status line (RFC 3977 §6.1.1.2).
func grpParse211(line string) (count, low, high int64, group string, err error) {
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "211" {
		return 0, 0, 0, "", fmt.Errorf("status line %q is not '211 count low high group'", line)
	}
	nums := make([]int64, 3)
	for i := range nums {
		n, perr := strconv.ParseInt(f[i+1], 10, 64)
		if perr != nil {
			return 0, 0, 0, "", fmt.Errorf("status line %q is not '211 count low high group': field %d %q", line, i+1, f[i+1])
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], f[4], nil
}

// grpExpect211 checks that line is exactly "211 count low high group".
func grpExpect211(line string, count, low, high int64, group string) error {
	c, l, h, g, err := grpParse211(line)
	if err != nil {
		return err
	}
	if c != count || l != low || h != high || g != group {
		return fmt.Errorf("got %q, want \"211 %d %d %d %s\"", line, count, low, high, group)
	}
	return nil
}

// grpReadIfMulti reads the dot-terminated block when code is one of the
// multi-line codes, so the connection stays in sync whatever the server sent.
func grpReadIfMulti(t *testing.T, c *hClient, code int, multi ...int) []string {
	t.Helper()
	for _, m := range multi {
		if code == m {
			return c.readDotLines(t)
		}
	}
	return nil
}

// grpNumbers converts LISTGROUP body lines to article numbers.
func grpNumbers(lines []string) ([]int64, error) {
	out := make([]int64, 0, len(lines))
	for _, l := range lines {
		n, err := strconv.ParseInt(strings.TrimSpace(l), 10, 64)
		if err != nil {
			return out, fmt.Errorf("LISTGROUP line %q is not an article number", l)
		}
		out = append(out, n)
	}
	return out, nil
}

// grpExpectNums compares article number lists.
func grpExpectNums(got, want []int64) error {
	if len(got) != len(want) {
		return fmt.Errorf("got numbers %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("got numbers %v, want %v", got, want)
		}
	}
	return nil
}

// grpFindLine returns the first line whose first field is name.
func grpFindLine(lines []string, name string) (string, bool) {
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 0 && f[0] == name {
			return l, true
		}
	}
	return "", false
}

// grpNewGroup inserts a newsgroups row without articles (an empty group with
// LowWater 1, HighWater 0) and returns its unique name.
func grpNewGroup(t *testing.T, prefix string, active bool) string {
	t.Helper()
	db := hTestDB(t)
	name := hGroupName(prefix)
	if err := db.InsertNewsgroup(&models.Newsgroup{
		Name: name, Description: "grp group " + name, Active: active,
		Status: "y", LowWater: 1, Hierarchy: "t3",
	}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	return name
}

// grpSeedBulk inserts articles 1..n into group in one transaction. Subject and
// References are padded to pad bytes each, so a handler that loads whole
// overview rows allocates far more than one that reads only the numbers.
// The newsgroups row is updated like hArticleSQL does.
func grpSeedBulk(t *testing.T, group string, n, pad int) {
	t.Helper()
	db := hTestDB(t)
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	tx, err := gdb.DB.Begin()
	if err != nil {
		t.Fatalf("begin %s: %v", group, err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references",
		                       bytes, lines, reply_count, path, headers_json, body_text, downloaded, imported_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, 1, CURRENT_TIMESTAMP)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare %s: %v", group, err)
	}
	subjPad := strings.Repeat("s", pad)
	refPad := strings.Repeat("r", pad)
	for i := 1; i <= n; i++ {
		a := hDefaultArticle(group, int64(i))
		headers := "Message-ID: " + a.MessageID + "\nNewsgroups: " + group
		if _, err := stmt.Exec(a.Num, a.MessageID, a.Subject+" "+subjPad, a.From,
			a.DateSent.UTC().Format("2006-01-02 15:04:05"), a.DateString, refPad,
			len(a.Body), 2, a.Path, headers, a.Body); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("insert article %d into %s: %v", i, group, err)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("close stmt %s: %v", group, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit %s: %v", group, err)
	}
	if _, err := db.GetMainDB().Exec(
		`UPDATE newsgroups SET last_article = ?, message_count = ?, high_water = ?, low_water = 1, updated_at = CURRENT_TIMESTAMP WHERE name = ?`,
		n, n, n, group,
	); err != nil {
		t.Fatalf("update newsgroups %s: %v", group, err)
	}
}

// grpDBEntries returns the paths under <DataDir>/db (two levels: the hash
// directories and the group files in them).
func grpDBEntries(t *testing.T) map[string]bool {
	t.Helper()
	root := filepath.Join(hDataDir(t), "db")
	out := make(map[string]bool)
	for _, pat := range []string{filepath.Join(root, "*"), filepath.Join(root, "*", "*")} {
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		for _, p := range m {
			out[p] = true
		}
	}
	return out
}

// TestGrpGroupSeeded: GROUP on a group holding articles 3, 4 and 5 (1 and 2
// deleted).
func TestGrpGroupSeeded(t *testing.T) {
	g := hGroup(t, "t3.groupseeded", 5, grpSkip12)
	hs := newTestServer(t, hServerOpts{})

	// RFC 3977 §6.1.1.2: "211 number low high group", where low and high are
	// the lowest and highest article numbers in the group, here 3 and 5.
	t.Run("reply", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "GROUP %s", g)
		if err := hExpectCode(code, line, 211); err != nil {
			t.Fatal(err)
		}
		count, low, high, name, err := grpParse211(line)
		if err != nil {
			t.Fatal(err)
		}
		if count != 3 || high != 5 || name != g {
			t.Fatalf("GROUP: got %q, want \"211 3 3 5 %s\"", line, g)
		}
		if low != 3 {
			err = fmt.Errorf("GROUP: got %q, want low mark 3 (\"211 3 3 5 %s\")", line, g)
		}
		if err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §6.1.1.2: a successful GROUP sets the current article number to
	// the low water mark, so a bare STAT (§6.2.4, current article form) gives
	// "223 3 <id>". Also depends on RFC-3 (STAT without argument → 501 today).
	t.Run("current-article", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "GROUP %s", g)
		if err := hExpectCode(code, line, 211); err != nil {
			t.Fatal(err)
		}
		code, line = c.cmd(t, "STAT")
		err := hExpectCode(code, line, 223)
		if err == nil {
			want := fmt.Sprintf("223 3 %s", hMessageID(g, 3))
			if !strings.HasPrefix(line, want) {
				err = fmt.Errorf("bare STAT after GROUP: got %q, want prefix %q (also needs RFC-3)", line, want)
			}
		} else {
			err = fmt.Errorf("bare STAT after GROUP: %w (also needs RFC-3)", err)
		}
		if err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §6.1.1.2: 411 for an unknown group, and "the currently selected
	// newsgroup and current article number MUST NOT be changed": STAT 3 still
	// works in the old group.
	t.Run("unknown-keeps-current", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "GROUP %s", g)
		if err := hExpectCode(code, line, 211); err != nil {
			t.Fatal(err)
		}
		unknown := hGroupName("t3.groupunknown")
		code, line = c.cmd(t, "GROUP %s", unknown)
		if err := hExpectCode(code, line, 411); err != nil {
			t.Fatalf("GROUP %s: %v", unknown, err)
		}
		code, line = c.cmd(t, "STAT 3")
		if err := hExpectCode(code, line, 223); err != nil {
			t.Fatalf("STAT 3 after a failed GROUP: %v", err)
		}
		if f := strings.Fields(line); len(f) < 3 || f[1] != "3" || f[2] != hMessageID(g, 3) {
			t.Fatalf("STAT 3 after a failed GROUP: got %q, want \"223 3 %s\"", line, hMessageID(g, 3))
		}
	})
}

// TestGrpGroupInactive: a group the server does not carry (newsgroups.active=0)
// is answered with 411 (RFC 3977 §6.1.1.2).
func TestGrpGroupInactive(t *testing.T) {
	g := grpNewGroup(t, "t3.groupinactive", false)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 411); err != nil {
		t.Fatal(err)
	}
}

// TestGrpGroupEmpty: RFC 3977 §6.1.1.2 allows three forms for an empty group
// (count 0 with high = low-1, all three numbers zero, or high >= low) and
// recommends the first, with low = the number the next article gets. The server
// uses that form: a never-used group gives "211 0 1 0". There is no current
// article (420 for STAT/NEXT/LAST without a number), and LISTGROUP gives the same
// status line and an empty list.
func TestGrpGroupEmpty(t *testing.T) {
	g := hGroup(t, "t3.groupempty", 0, nil)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	if err := grpExpect211(line, 0, 1, 0, g); err != nil {
		t.Fatalf("empty group: %v", err)
	}
	grpExpectNoCurrent(t, c)
	grpExpectEmptyListGroup(t, c, g, 0, 1, 0)
}

// TestGrpGroupEmptyNextNumFails: when the next-number query of an empty group fails
// (no article_num_seq table), GROUP answers 403, nothing is selected (a bare LISTGROUP
// answers 412) and no group DB stays held (the server stops cleanly).
func TestGrpGroupEmptyNextNumFails(t *testing.T) {
	g := hGroup(t, "t3.groupemptyfail", 0, nil)
	db := hTestDB(t)
	gdb, err := db.GetGroupDB(g)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", g, err)
	}
	if _, err := database.RetryableExec(gdb.DB, `DROP TABLE article_num_seq`); err != nil {
		gdb.Return()
		t.Fatalf("drop article_num_seq of %s: %v", g, err)
	}
	gdb.Return()
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 403); err != nil {
		t.Fatal(err)
	}
	code, line = c.cmd(t, "LISTGROUP")
	if err := hExpectCode(code, line, 412); err != nil {
		t.Fatalf("after the failed GROUP: %v", err)
	}
	c.close()
	hs.stop(t)
}

// grpExpectNoCurrent checks that STAT, NEXT and LAST without an argument answer 420
// (no current article, RFC 3977 §6.1.1.2, §6.1.3, §6.1.4, §6.2.4).
func grpExpectNoCurrent(t *testing.T, c *hClient) {
	t.Helper()
	for _, cmd := range []string{"STAT", "NEXT", "LAST"} {
		code, line := c.cmd(t, "%s", cmd)
		if err := hExpectCode(code, line, 420); err != nil {
			t.Fatalf("%s without a current article: %v", cmd, err)
		}
	}
}

// grpExpectEmptyListGroup checks that "LISTGROUP group" answers exactly
// "211 count low high group" followed by an empty list.
func grpExpectEmptyListGroup(t *testing.T, c *hClient, group string, count, low, high int64) {
	t.Helper()
	code, line := c.cmd(t, "LISTGROUP %s", group)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	lines := c.readDotLines(t)
	if err := grpExpect211(line, count, low, high, group); err != nil {
		t.Fatalf("LISTGROUP %s: %v", group, err)
	}
	if len(lines) != 0 {
		t.Fatalf("LISTGROUP %s: got lines %q, want none", group, lines)
	}
}

// grpEmptyOut deletes every article of group (an expiry of all of them) and sets the
// newsgroups row's message_count to 0; article_num_seq keeps the highest number used.
func grpEmptyOut(t *testing.T, group string) {
	t.Helper()
	db := hTestDB(t)
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	if _, err := database.RetryableExec(gdb.DB, `DELETE FROM articles`); err != nil {
		t.Fatalf("delete articles of %s: %v", group, err)
	}
	if _, err := database.RetryableExec(db.GetMainDB(),
		`UPDATE newsgroups SET message_count = 0 WHERE name = ?`, group); err != nil {
		t.Fatalf("zero message_count of %s: %v", group, err)
	}
}

// TestGrpGroupEmptied: a group whose articles 1..5 were all deleted answers
// "211 0 6 5" (RFC 3977 §6.1.1.2: low = high+1, so a reader keeps its place);
// the old numbers are gone (423), and the next stored article gets number 6.
func TestGrpGroupEmptied(t *testing.T) {
	g := hGroup(t, "t3.groupemptied", 5, nil)
	grpEmptyOut(t, g)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)

	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	if err := grpExpect211(line, 0, 6, 5, g); err != nil {
		t.Fatalf("emptied group: %v", err)
	}
	grpExpectNoCurrent(t, c)
	code, line = c.cmd(t, "ARTICLE 1")
	if err := hExpectCode(code, line, 423); err != nil {
		t.Fatalf("ARTICLE 1 in the emptied group: %v", err)
	}
	grpExpectEmptyListGroup(t, c, g, 0, 6, 5)

	// A range over the old numbers lists nothing either.
	code, line = c.cmd(t, "LISTGROUP %s 1-10", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	if lines := c.readDotLines(t); len(lines) != 0 {
		t.Fatalf("LISTGROUP %s 1-10: got lines %q, want none", g, lines)
	}

	// The next stored article gets the announced low mark.
	gdb, err := hTestDB(t).GetGroupDB(g)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", g, err)
	}
	next, err := hTestDB(t).NNTPGroupNextArticleNum(gdb)
	gdb.Return()
	if err != nil {
		t.Fatalf("NNTPGroupNextArticleNum %s: %v", g, err)
	}
	if next != 6 {
		t.Fatalf("next article number of %s: got %d, want 6", g, next)
	}
	hArticleSQL(t, g, hDefaultArticle(g, next))
	code, line = c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	if err := grpExpect211(line, 1, 6, 6, g); err != nil {
		t.Fatalf("GROUP after a new article: %v", err)
	}
	code, line = c.cmd(t, "STAT")
	if !strings.HasPrefix(line, fmt.Sprintf("223 6 %s", hMessageID(g, 6))) {
		t.Fatalf("bare STAT after GROUP: got %q (code %d), want 223 6", line, code)
	}
}

// TestGrpGroupAllHidden: a group whose articles 1..5 are all hidden has no visible
// article and answers like an emptied group, "211 0 6 5", with an empty LISTGROUP.
func TestGrpGroupAllHidden(t *testing.T) {
	g := hGroup(t, "t3.groupallhidden", 5, nil)
	grpHide(t, g, 1, 2, 3, 4, 5)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)

	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	if err := grpExpect211(line, 0, 6, 5, g); err != nil {
		t.Fatalf("all-hidden group: %v", err)
	}
	grpExpectNoCurrent(t, c)
	grpExpectEmptyListGroup(t, c, g, 0, 6, 5)
}

// TestGrpGroupNoArg: GROUP without a group name is a syntax error, 501
// (RFC 3977 §3.2.1, §6.1.1.1).
func TestGrpGroupNoArg(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "GROUP")
	if err := hExpectCode(code, line, 501); err != nil {
		t.Fatal(err)
	}
}

// TestGrpListGroup covers LISTGROUP replies, ranges and group selection.
func TestGrpListGroup(t *testing.T) {
	g := hGroup(t, "t3.listgroup", 5, grpSkip12)
	hs := newTestServer(t, hServerOpts{})

	// RFC 3977 §6.1.2.2: "211 number low high group" as for GROUP, followed by
	// the article numbers in the group in ascending order.
	t.Run("reply-and-numbers", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "LISTGROUP %s", g)
		if err := hExpectCode(code, line, 211); err != nil {
			t.Fatal(err)
		}
		lines := c.readDotLines(t)
		nums, nerr := grpNumbers(lines)
		if nerr == nil {
			nerr = grpExpectNums(nums, []int64{3, 4, 5})
		}
		if nerr != nil {
			t.Fatalf("LISTGROUP body: %v", nerr)
		}
		if err := grpExpect211(line, 3, 3, 5, g); err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §6.1.2.2: with a range only the numbers in that range are
	// listed; "4-" means 4 to the end, here 4 and 5.
	t.Run("range", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "LISTGROUP %s 4-", g)
		if err := hExpectCode(code, line, 211); err != nil {
			t.Fatal(err)
		}
		nums, err := grpNumbers(c.readDotLines(t))
		if err == nil {
			err = grpExpectNums(nums, []int64{4, 5})
		}
		if err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §6.1.2.2: a successful LISTGROUP selects the group and sets the
	// current article like GROUP, so a following STAT 3 gives 223 (not 412).
	t.Run("selects-group", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "LISTGROUP %s", g)
		if err := hExpectCode(code, line, 211); err != nil {
			t.Fatal(err)
		}
		c.readDotLines(t)
		code, line = c.cmd(t, "STAT 3")
		if err := hExpectCode(code, line, 223); err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §6.1.2.2: LISTGROUP without a group and with no group selected → 412.
	t.Run("no-group", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "LISTGROUP")
		grpReadIfMulti(t, c, code, 211)
		if err := hExpectCode(code, line, 412); err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §6.1.2.2: a group the server does not carry gives 411, the same
	// answer GROUP gives for it (TestGrpGroupInactive). LISTGROUP never looks
	// at newsgroups.active (T-nntp-group-1).
	t.Run("inactive", func(t *testing.T) {
		inactive := grpNewGroup(t, "t3.listgroupinactive", false)
		c := hs.dial(t)
		code, line := c.cmd(t, "LISTGROUP %s", inactive)
		grpReadIfMulti(t, c, code, 211)
		if err := hExpectCode(code, line, 411); err != nil {
			t.Fatal(err)
		}
	})
}

// TestGrpListGroupUnknownNoFile: RFC 3977 §6.1.2.2, LISTGROUP for a group that
// does not exist gives 411. It must not create a group database (SEC-2): no
// new directory or file under <DataDir>/db and no newsgroups row.
func TestGrpListGroupUnknownNoFile(t *testing.T) {
	db := hTestDB(t)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	unknown := hGroupName("t3.listgroupunknown")
	sanitized := database.SanitizeGroupName(unknown)

	before := grpDBEntries(t)
	code, line := c.cmd(t, "LISTGROUP %s", unknown)
	grpReadIfMulti(t, c, code, 211)
	after := grpDBEntries(t)

	var errs []error
	if err := hExpectCode(code, line, 411); err != nil {
		errs = append(errs, fmt.Errorf("LISTGROUP %s: %w", unknown, err))
	}
	var created []string
	for p := range after {
		if before[p] {
			continue
		}
		fi, err := os.Stat(p)
		if strings.Contains(filepath.Base(p), sanitized) || (err == nil && fi.IsDir()) {
			created = append(created, p)
		}
	}
	if len(created) > 0 {
		errs = append(errs, fmt.Errorf("LISTGROUP %s created %v", unknown, created))
	}
	if _, err := db.MainDBGetNewsgroup(unknown); err == nil {
		t.Fatalf("LISTGROUP %s created a newsgroups row", unknown)
	}
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

// grpListGroupAllocCeiling bounds what one LISTGROUP of grpMemRows rows may
// allocate (process-wide TotalAlloc delta, client reading included). Reading
// only the article numbers stays far below it; loading every overview row
// (2 KiB of subject and references each) goes far above.
const (
	grpMemRows               = 20000
	grpMemPad                = 1024
	grpListGroupAllocCeiling = 16 << 20
	grpMemMarker             = "grp-mem-ceiling"
)

// grpCountListLines reads a LISTGROUP body without allocating per line and
// returns the number of lines and whether they were strictly ascending.
func grpCountListLines(c *hClient, deadline time.Duration) (int, bool, error) {
	if err := c.Conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		return 0, false, err
	}
	n, sorted := 0, true
	var prev int64 = -1
	for {
		b, err := c.R.ReadSlice('\n')
		if err != nil {
			return n, sorted, fmt.Errorf("after %d lines: %w", n, err)
		}
		if string(b) == ".\r\n" || string(b) == ".\n" {
			return n, sorted, nil
		}
		var v int64
		for _, ch := range b {
			if ch >= '0' && ch <= '9' {
				v = v*10 + int64(ch-'0')
			}
		}
		if v <= prev {
			sorted = false
		}
		prev = v
		n++
	}
}

// TestGrpListGroupMemory: LISTGROUP of a 20k-article group must not load every
// overview row into memory (SEC-7; RFC 3977 §6.1.2 needs only the numbers).
// It runs in an isolated child so the allocation counter sees nothing else.
func TestGrpListGroupMemory(t *testing.T) {
	err := runIsolated(t, 3*hDeadline, func(t *testing.T) {
		g := hGroup(t, "t3.listgroupmem", 0, nil)
		grpSeedBulk(t, g, grpMemRows, grpMemPad)
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)

		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		code, line, err := c.tryCmd("LISTGROUP %s", g)
		if err != nil {
			t.Fatalf("LISTGROUP: %v", err)
		}
		if code != 211 {
			t.Fatalf("LISTGROUP: got %q, want 211", line)
		}
		n, sorted, err := grpCountListLines(c, hDeadline)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatalf("LISTGROUP body: %v", err)
		}
		if n != grpMemRows || !sorted {
			t.Fatalf("LISTGROUP body: %d lines (sorted=%v), want %d ascending", n, sorted, grpMemRows)
		}
		alloc := after.TotalAlloc - before.TotalAlloc
		t.Logf("LISTGROUP of %d rows allocated %.1f MiB", n, float64(alloc)/(1<<20))
		if alloc > grpListGroupAllocCeiling {
			t.Fatalf("%s: LISTGROUP of %d rows allocated %.1f MiB, ceiling %d MiB",
				grpMemMarker, n, float64(alloc)/(1<<20), grpListGroupAllocCeiling>>20)
		}
	})
	if os.Getenv("PUGLEAF_ISOLATED") == t.Name() {
		return // child
	}
	if err != nil && !strings.Contains(err.Error(), grpMemMarker) {
		t.Fatalf("isolated LISTGROUP run failed for another reason: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestGrpListActive: RFC 3977 §7.6.3, LIST ACTIVE lines are "name high low
// status" with the real low water mark and status "y" when posting to the group
// is permitted (the server has a processor and the group's status is y).
// Groups the server does not carry are not listed.
func TestGrpListActive(t *testing.T) {
	g := hGroup(t, "t3.listactive", 5, grpSkip12)
	inactive := grpNewGroup(t, "t3.listactiveoff", false)
	hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
	c := hs.dial(t)
	code, line := c.cmd(t, "LIST ACTIVE")
	if err := hExpectCode(code, line, 215); err != nil {
		t.Fatal(err)
	}
	lines := c.readDotLines(t)
	if l, ok := grpFindLine(lines, inactive); ok {
		t.Fatalf("LIST ACTIVE lists the inactive group: %q", l)
	}
	got, ok := grpFindLine(lines, g)
	if !ok {
		t.Fatalf("LIST ACTIVE does not list %s", g)
	}
	want := fmt.Sprintf("%s 5 3 y", g)
	f := strings.Fields(got)
	if len(f) != 4 || f[1] != "5" {
		t.Fatalf("LIST ACTIVE line %q, want %q", got, want)
	}
	var err error
	if f[2] != "3" || f[3] != "y" {
		err = fmt.Errorf("LIST ACTIVE line %q, want %q (low mark and status)", got, want)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestGrpListWildmat: RFC 3977 §7.6.3 and §7.6.6, LIST ACTIVE and LIST
// NEWSGROUPS take a wildmat (§4) and list only matching groups (RFC-1).
func TestGrpListWildmat(t *testing.T) {
	tok := hSeq.Add(1)
	a := hGroup(t, fmt.Sprintf("t3.listwild.%d.a", tok), 1, nil)
	b := hGroup(t, fmt.Sprintf("t3.listwild.%d.b", tok), 1, nil)
	pattern := fmt.Sprintf("t3.listwild.%d.a.*", tok)
	t3re := regexp.MustCompile(`^t3\.`)
	hs := newTestServer(t, hServerOpts{})

	for _, kw := range []string{"ACTIVE", "NEWSGROUPS"} {
		t.Run(strings.ToLower(kw), func(t *testing.T) {
			c := hs.dial(t)
			var errs []error

			// "t3.*" lists a and b and only t3.* groups.
			code, line := c.cmd(t, "LIST %s t3.*", kw)
			if err := hExpectCode(code, line, 215); err != nil {
				t.Fatal(err)
			}
			lines := c.readDotLines(t)
			for _, want := range []string{a, b} {
				if _, ok := grpFindLine(lines, want); !ok {
					t.Fatalf("LIST %s t3.* does not list %s", kw, want)
				}
			}
			for _, l := range lines {
				if !t3re.MatchString(l) {
					errs = append(errs, fmt.Errorf("LIST %s t3.* lists %q", kw, l))
					break
				}
			}

			// The narrower pattern lists exactly a.
			code, line = c.cmd(t, "LIST %s %s", kw, pattern)
			if err := hExpectCode(code, line, 215); err != nil {
				t.Fatal(err)
			}
			lines = c.readDotLines(t)
			if len(lines) != 1 || !strings.HasPrefix(lines[0], a+" ") {
				errs = append(errs, fmt.Errorf("LIST %s %s: got %q, want only %s", kw, pattern, lines, a))
			}
			if kw == "NEWSGROUPS" && len(lines) == 1 {
				// §7.6.6: "name<whitespace>description".
				if desc := strings.Join(strings.Fields(lines[0])[1:], " "); desc != "harness group "+a {
					errs = append(errs, fmt.Errorf("LIST NEWSGROUPS line %q: description %q", lines[0], desc))
				}
			}
			if err := errors.Join(errs...); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestGrpListUnknownKeyword: RFC 3977 §7.6.1, an unknown LIST keyword is a
// syntax error, 501.
func TestGrpListUnknownKeyword(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "LIST FOO")
	grpReadIfMulti(t, c, code, 215)
	if err := hExpectCode(code, line, 501); err != nil {
		t.Fatal(err)
	}
}

// TestGrpListOverviewFmt: RFC 3977 §8.4, LIST OVERVIEW.FMT gives 215 and the
// overview fields in order: Subject:, From:, Date:, Message-ID:, References:,
// then :bytes and :lines (§8.4.2 allows Bytes: and Lines: instead) (RFC-1).
func TestGrpListOverviewFmt(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "LIST OVERVIEW.FMT")
	lines := grpReadIfMulti(t, c, code, 215)
	err := hExpectCode(code, line, 215)
	if err == nil {
		want := []string{"subject:", "from:", "date:", "message-id:", "references:", ":bytes", ":lines"}
		alt := map[string]string{":bytes": "bytes:", ":lines": "lines:"}
		if len(lines) < len(want) {
			err = fmt.Errorf("OVERVIEW.FMT has %d lines, want at least %d: %q", len(lines), len(want), lines)
		} else {
			for i, w := range want {
				got := strings.ToLower(strings.TrimSpace(lines[i]))
				if got != w && got != alt[w] {
					err = fmt.Errorf("OVERVIEW.FMT line %d: got %q, want %q", i, lines[i], w)
					break
				}
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestGrpDate: RFC 3977 §7.1, DATE gives "111 yyyymmddhhmmss" in UTC (RFC-1).
func TestGrpDate(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "DATE")
	err := hExpectCode(code, line, 111)
	if err == nil {
		m := regexp.MustCompile(`^111 (\d{14})(\s|$)`).FindStringSubmatch(line)
		if m == nil {
			err = fmt.Errorf("DATE: got %q, want \"111 yyyymmddhhmmss\"", line)
		} else if ts, perr := time.Parse("20060102150405", m[1]); perr != nil {
			err = fmt.Errorf("DATE: %q: %v", line, perr)
		} else if d := time.Since(ts); d > 5*time.Minute || d < -5*time.Minute {
			err = fmt.Errorf("DATE: %q is %s away from UTC now", line, d)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestGrpNextLast: RFC 3977 §6.1.3 (LAST) and §6.1.4 (NEXT) move the current
// article: 223 n <id>, 422 no previous / 421 no next article, 412 no group,
// 420 no current article (RFC-1). The walk also relies on GROUP setting the
// current article to the low mark (BUG-4).
func TestGrpNextLast(t *testing.T) {
	// articles 3, 4 and 6 (1, 2 and 5 deleted)
	g := hGroup(t, "t3.nextlast", 6, func(i int, a *hArticle) {
		if i <= 2 || i == 5 {
			a.Skip = true
		}
	})
	empty := hGroup(t, "t3.nextlastempty", 0, nil)
	hs := newTestServer(t, hServerOpts{})

	expect := func(t *testing.T, c *hClient, cmd string, code int, num int64) error {
		t.Helper()
		got, line := c.cmd(t, "%s", cmd)
		if got != code {
			return fmt.Errorf("%s: got %q, want %d", cmd, line, code)
		}
		if code == 223 {
			want := fmt.Sprintf("223 %d %s", num, hMessageID(g, num))
			if !strings.HasPrefix(line, want) {
				return fmt.Errorf("%s: got %q, want prefix %q", cmd, line, want)
			}
		}
		return nil
	}

	t.Run("no-group", func(t *testing.T) {
		c := hs.dial(t)
		if err := errors.Join(expect(t, c, "NEXT", 412, 0), expect(t, c, "LAST", 412, 0)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("walk", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "GROUP %s", g)
		if err := hExpectCode(code, line, 211); err != nil {
			t.Fatal(err)
		}
		err := errors.Join(
			expect(t, c, "LAST", 422, 0), // at the first article
			expect(t, c, "NEXT", 223, 4),
			expect(t, c, "NEXT", 223, 6), // skips the deleted 5
			expect(t, c, "NEXT", 421, 0), // at the last article
			expect(t, c, "LAST", 223, 4),
			expect(t, c, "STAT", 223, 4), // the current article moved (§6.2.4)
		)
		if err != nil {
			err = fmt.Errorf("%w\n(also needs RFC-3: bare STAT)", err)
		}
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty-group", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "GROUP %s", empty)
		if err := hExpectCode(code, line, 211); err != nil {
			t.Fatal(err)
		}
		// §6.1.1.2: in an empty group the current article number is invalid → 420.
		if err := errors.Join(expect(t, c, "NEXT", 420, 0), expect(t, c, "LAST", 420, 0)); err != nil {
			t.Fatal(err)
		}
	})
}

// TestGrpNewGroups: RFC 3977 §7.3, "NEWGROUPS date time GMT" gives 231 and the
// groups created since then in LIST ACTIVE format; a date in the future gives
// an empty list (RFC-1).
func TestGrpNewGroups(t *testing.T) {
	g := hGroup(t, "t3.newgroups", 2, nil)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	var errs []error

	since := time.Now().UTC().Add(-48 * time.Hour)
	code, line := c.cmd(t, "NEWGROUPS %s GMT", since.Format("20060102 150405"))
	lines := grpReadIfMulti(t, c, code, 231)
	if err := hExpectCode(code, line, 231); err != nil {
		errs = append(errs, fmt.Errorf("NEWGROUPS (2 days ago): %w", err))
	} else if l, ok := grpFindLine(lines, g); !ok {
		errs = append(errs, fmt.Errorf("NEWGROUPS (2 days ago) does not list %s", g))
	} else if f := strings.Fields(l); len(f) != 4 || f[1] != "2" || f[2] != "1" {
		errs = append(errs, fmt.Errorf("NEWGROUPS line %q, want \"%s 2 1 <status>\"", l, g))
	}

	code, line = c.cmd(t, "NEWGROUPS 20991231 235959 GMT")
	lines = grpReadIfMulti(t, c, code, 231)
	if err := hExpectCode(code, line, 231); err != nil {
		errs = append(errs, fmt.Errorf("NEWGROUPS (future): %w", err))
	} else if len(lines) != 0 {
		errs = append(errs, fmt.Errorf("NEWGROUPS (future) lists %q, want nothing", lines))
	}
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

// TestGrpNewNews: RFC 3977 §7.4 and §3.3.2, NEWNEWS gives 230 plus a list
// when the NEWNEWS capability is advertised; a server that does not advertise
// it answers 500 (unknown command).
func TestGrpNewNews(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "CAPABILITIES")
	if err := hExpectCode(code, line, 101); err != nil {
		t.Fatal(err)
	}
	advertised := false
	for _, l := range c.readDotLines(t) {
		if f := strings.Fields(l); len(f) > 0 && strings.EqualFold(f[0], "NEWNEWS") {
			advertised = true
		}
	}
	code, line = c.cmd(t, "NEWNEWS t3.* 20000101 000000 GMT")
	grpReadIfMulti(t, c, code, 230)
	switch {
	case code == 230 && !advertised:
		t.Fatalf("NEWNEWS answered %q but CAPABILITIES does not list NEWNEWS (§3.3.2)", line)
	case code == 500 && advertised:
		t.Fatalf("CAPABILITIES lists NEWNEWS but NEWNEWS answered %q", line)
	case code != 230 && code != 500:
		t.Fatalf("NEWNEWS: got %q, want 230 or 500", line)
	}
}

// ---------------------------------------------------------------------------
// Plan nntp-server-hardening, slice nntp-read

// grpHide sets hide = 1 on the given article numbers of group (decision 2).
func grpHide(t *testing.T, group string, nums ...int64) {
	t.Helper()
	gdb, err := hTestDB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	for _, n := range nums {
		if _, err := database.RetryableExec(gdb.DB, `UPDATE articles SET hide = 1 WHERE article_num = ?`, n); err != nil {
			t.Fatalf("hide article %d in %s: %v", n, group, err)
		}
	}
}

// TestGrpHiddenNotListed: hidden articles (hide > 0) are absent from LISTGROUP and from
// GROUP's marks; GROUP's count is an estimate that only has to cover the visible
// articles (decision 14, RFC 3977 §6.1.1.2).
func TestGrpHiddenNotListed(t *testing.T) {
	g := hGroup(t, "t3.hidden", 5, nil)
	grpHide(t, g, 1, 3, 5)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)

	code, line := c.cmd(t, "LISTGROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	nums, err := grpNumbers(c.readDotLines(t))
	if err == nil {
		err = grpExpectNums(nums, []int64{2, 4})
	}
	if err != nil {
		t.Fatalf("LISTGROUP body: %v", err)
	}

	code, line = c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	count, low, high, name, err := grpParse211(line)
	if err != nil {
		t.Fatal(err)
	}
	if low != 2 || high != 4 || name != g || count < 2 || count > high-low+1 {
		t.Fatalf("GROUP: got %q, want \"211 <2..3> 2 4 %s\"", line, g)
	}
	// The current article is the low mark, a visible article.
	code, line = c.cmd(t, "STAT")
	if !strings.HasPrefix(line, fmt.Sprintf("223 2 %s", hMessageID(g, 2))) {
		t.Fatalf("bare STAT after GROUP: got %q (code %d), want 223 2", line, code)
	}

	// A stale message_count of 0 must not give "211 0 2 4": a non-empty group reports a
	// count of at least the visible articles (RFC 3977 §6.1.1.2), here high-low+1 = 3.
	if _, err := database.RetryableExec(hTestDB(t).GetMainDB(),
		`UPDATE newsgroups SET message_count = 0 WHERE name = ?`, g); err != nil {
		t.Fatalf("zero message_count of %s: %v", g, err)
	}
	code, line = c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	if err := grpExpect211(line, 3, 2, 4, g); err != nil {
		t.Fatalf("GROUP with message_count 0: %v", err)
	}
}

// TestGrpListGroupRangeEmpty: a reversed range is not a syntax error; LISTGROUP answers
// 211 with an empty list and still selects the group (RFC 3977 §6.1.2.2).
func TestGrpListGroupRangeEmpty(t *testing.T) {
	g := hGroup(t, "t3.listgrouprangeempty", 5, nil)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)

	code, line := c.cmd(t, "LISTGROUP %s 9-3", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	if lines := c.readDotLines(t); len(lines) != 0 {
		t.Fatalf("LISTGROUP %s 9-3: got lines %q, want none", g, lines)
	}
	if err := grpExpect211(line, 5, 1, 5, g); err != nil {
		t.Fatal(err)
	}
	// A range beyond the group is empty as well.
	code, line = c.cmd(t, "LISTGROUP %s 100-", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	if lines := c.readDotLines(t); len(lines) != 0 {
		t.Fatalf("LISTGROUP %s 100-: got lines %q, want none", g, lines)
	}
	// A bad range is a syntax error.
	code, line = c.cmd(t, "LISTGROUP %s x-5", g)
	grpReadIfMulti(t, c, code, 211)
	if err := hExpectCode(code, line, 501); err != nil {
		t.Fatal(err)
	}
	code, line = c.cmd(t, "STAT 3")
	if err := hExpectCode(code, line, 223); err != nil {
		t.Fatalf("STAT 3 after LISTGROUP with an empty range: %v", err)
	}
}

// TestGrpListNewsgroupsSanitized: a description with TAB, CR or LF is sent with spaces,
// one line per group (RFC 3977 §7.6.6).
func TestGrpListNewsgroupsSanitized(t *testing.T) {
	db := hTestDB(t)
	name := hGroupName("t3.listnewsgroupssan")
	if err := db.InsertNewsgroup(&models.Newsgroup{
		Name: name, Description: "one\ttwo\rthree\nfour", Active: true,
		Status: "y", LowWater: 1, Hierarchy: "t3",
	}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "LIST NEWSGROUPS")
	if err := hExpectCode(code, line, 215); err != nil {
		t.Fatal(err)
	}
	var mine []string
	for _, l := range c.readDotLines(t) {
		if l == "four" || l == "three" || strings.HasPrefix(l, name+" ") {
			mine = append(mine, l)
		}
	}
	want := name + " one two three four"
	if len(mine) != 1 || mine[0] != want {
		t.Fatalf("LIST NEWSGROUPS lines for %s: %q, want [%q]", name, mine, want)
	}
}
