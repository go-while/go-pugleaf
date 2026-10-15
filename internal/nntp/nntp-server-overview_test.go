package nntp

// Overview tests for the NNTP server (plan nntp-audit-tests, slice test-nntp-overview):
// XOVER ranges and response codes, the overview line format, sanitizing of
// TAB/CR/LF in overview and header values, ranges past 1000 articles, XHDR
// (supported, unsupported, absent headers and the message-id form), the
// SEC-1 spin, and the RFC 3977 OVER/HDR commands.
//
// Every check states the RFC-correct behavior. Where the current code differs,
// the result goes through knownBug with the seed id. Identifiers use the
// ovr/TestOvr prefixes (contract K2); groups are named t5.<test>.<seq> (K3).

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
)

// ---------------------------------------------------------------------------
// Helpers (ovr prefix; waits use the harness hang guard hDeadline)

// ovrIsChild reports whether this is the runIsolated child of t.
func ovrIsChild(t *testing.T) bool {
	return os.Getenv("PUGLEAF_ISOLATED") == t.Name()
}

// ovrMultiLine reports whether code starts a dot-terminated block for the
// commands tested here (215 LIST, 221 XHDR, 224 XOVER/OVER, 225 HDR).
func ovrMultiLine(code int) bool {
	switch code {
	case 215, 221, 224, 225:
		return true
	}
	return false
}

// ovrCmd sends one command, fails the test on I/O errors and returns the status
// and, for a multi-line status, the un-stuffed block. The connection stays in
// sync whatever the code is.
func ovrCmd(t *testing.T, c *hClient, format string, args ...interface{}) (int, string, []string) {
	t.Helper()
	code, line := c.cmd(t, format, args...)
	if !ovrMultiLine(code) {
		return code, line, nil
	}
	return code, line, c.readDotLines(t)
}

// ovrTryDotLines reads a dot-terminated block without failing the test.
func ovrTryDotLines(c *hClient) ([]string, error) {
	var out []string
	for {
		line, err := c.readLine()
		if err != nil {
			return out, fmt.Errorf("no dot terminator: %v after %d lines", err, len(out))
		}
		if line == "." {
			return out, nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		out = append(out, line)
	}
}

// ovrSelect sends GROUP g and fails unless the server answers 211.
func ovrSelect(t *testing.T, c *hClient, g string) {
	t.Helper()
	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatalf("GROUP %s: %v", g, err)
	}
}

// ovrNums returns the leading article number (the text before the first TAB
// or space) of each line.
func ovrNums(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if i := strings.IndexAny(l, "\t "); i >= 0 {
			l = l[:i]
		}
		out = append(out, l)
	}
	return out
}

// ovrLastNum returns the article number of the last line, or "" without lines.
func ovrLastNum(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return ovrNums(lines[len(lines)-1:])[0]
}

// ovrAbsentLines checks one absent-value line per article number in nums:
// "n", "n " or "n (none)" (RFC 3977 §8.5.2; "(none)" is the INN style).
func ovrAbsentLines(lines []string, nums ...string) error {
	if len(lines) != len(nums) {
		return fmt.Errorf("got %d lines, want %d: got=%q", len(lines), len(nums), lines)
	}
	for i, n := range nums {
		if l := lines[i]; l != n && l != n+" " && l != n+" (none)" {
			return fmt.Errorf("line %d: got %q, want %q, %q or %q", i, l, n, n+" ", n+" (none)")
		}
	}
	return nil
}

// ovrRawLines reads a dot-terminated block byte-exact and splits it into lines
// on CRLF. It returns an error when the block contains a bare CR or LF, which
// a client reading lines sees as extra (desynchronizing) lines.
func ovrRawLines(t *testing.T, c *hClient) ([]string, error) {
	t.Helper()
	raw := c.rawUntilDot(t)
	body := bytes.TrimSuffix(raw, []byte(".\r\n"))
	if len(body) == 0 {
		return nil, nil
	}
	if !bytes.HasSuffix(body, []byte("\r\n")) {
		return nil, fmt.Errorf("block does not end with CRLF before the terminator: %q", raw)
	}
	lines := strings.Split(string(body[:len(body)-2]), "\r\n")
	for i, l := range lines {
		if strings.ContainsAny(l, "\r\n") {
			return lines, fmt.Errorf("line %d contains a bare CR or LF: %q", i, l)
		}
	}
	return lines, nil
}

// ovrBulkGroup creates group t5.<name>.<seq> with articles 1..n (the harness
// defaults) inserted in one transaction, which keeps 1500 articles fast.
func ovrBulkGroup(t *testing.T, prefix string, n int) string {
	t.Helper()
	db := hTestDB(t)
	g := hGroup(t, prefix, 0, nil)
	gdb, err := db.GetGroupDB(g)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", g, err)
	}
	defer gdb.Return()
	err = database.RetryableTransactionExec(gdb.DB, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references",
			                       bytes, lines, reply_count, path, headers_json, body_text, downloaded, imported_at)
			 VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, 0, ?, ?, ?, 1, CURRENT_TIMESTAMP)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 1; i <= n; i++ {
			a := hDefaultArticle(g, int64(i))
			headers := strings.Join([]string{
				"Path: " + a.Path,
				"From: " + a.From,
				"Newsgroups: " + g,
				"Subject: " + a.Subject,
				"Date: " + a.DateString,
				"Message-ID: " + a.MessageID,
			}, "\n")
			if _, err := stmt.Exec(a.Num, a.MessageID, a.Subject, a.From,
				a.DateSent.UTC().Format("2006-01-02 15:04:05"), a.DateString,
				len(a.Body), strings.Count(a.Body, "\n")+1, a.Path, headers, a.Body); err != nil {
				return fmt.Errorf("article %d: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("bulk insert into %s: %v", g, err)
	}
	hArticleSQL(t, g) // no articles: updates the newsgroups row from the table
	return g
}

// ---------------------------------------------------------------------------
// XOVER ranges and response codes

func TestOvrXoverRanges(t *testing.T) {
	// Articles 1, 2, 4, 5 (3 is a deleted number).
	g := hGroup(t, "t5.ranges", 5, func(i int, a *hArticle) { a.Skip = i == 3 })
	empty := hGroup(t, "t5.ranges-empty", 0, nil)
	hs := newTestServer(t, hServerOpts{})

	t.Run("no-group", func(t *testing.T) {
		c := hs.dial(t)
		defer c.close()
		// RFC 3977 §8.3.2 / RFC 2980 §2.8: 412 when no newsgroup is selected.
		code, line, _ := ovrCmd(t, c, "XOVER 1-5")
		if err := hExpectCode(code, line, 412); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no-current-article", func(t *testing.T) {
		c := hs.dial(t)
		defer c.close()
		// GROUP on an empty group leaves the current article number invalid
		// (RFC 3977 §6.1.1.2); a bare XOVER then gets 420 (RFC 3977 §8.3.2).
		ovrSelect(t, c, empty)
		code, line, _ := ovrCmd(t, c, "XOVER")
		if err := hExpectCode(code, line, 420); err != nil {
			t.Fatal(err)
		}
	})

	c := hs.dial(t)
	ovrSelect(t, c, g)

	matching := []struct {
		name, arg string
		want      []string
	}{
		// RFC 3977 §8.3.2 / RFC 2980 §2.8: 224 and one line per existing article
		// in the range, in order; deleted numbers are left out.
		{"n", "2", []string{"2"}},
		{"n-", "2-", []string{"2", "4", "5"}},
		{"n-m", "2-4", []string{"2", "4"}},
		{"all", "1-5", []string{"1", "2", "4", "5"}},
	}
	for _, tc := range matching {
		t.Run(tc.name, func(t *testing.T) {
			code, line, lines := ovrCmd(t, c, "XOVER %s", tc.arg)
			if err := hExpectCode(code, line, 224); err != nil {
				t.Fatalf("XOVER %s: %v", tc.arg, err)
			}
			if err := hExpectLines(ovrNums(lines), tc.want); err != nil {
				t.Fatalf("XOVER %s article numbers: %v", tc.arg, err)
			}
		})
	}

	noArticles := []struct {
		name, arg string
	}{
		{"deleted-number", "3"},
		{"past-high", "100-200"},
		// RFC 3977 §8.3.2 and §6.1.2.2: a range whose end is less than its
		// start contains no articles; it is not a syntax error.
		{"reversed", "4-2"},
	}
	for _, tc := range noArticles {
		t.Run(tc.name, func(t *testing.T) {
			// RFC 3977 §8.3.2: 423 "No articles in that range"; RFC 2980 §2.8: 420.
			code, line, lines := ovrCmd(t, c, "XOVER %s", tc.arg)
			err := hExpectCode(code, line, 423, 420)
			if err != nil {
				err = fmt.Errorf("XOVER %s: %w (block %q)", tc.arg, err, lines)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("garbage", func(t *testing.T) {
		// RFC 3977 §3.2.1: a syntactically invalid argument gets 501.
		for _, arg := range []string{"abc", "1-2-3", "x-5", "5-y", "-"} {
			code, line, _ := ovrCmd(t, c, "XOVER %s", arg)
			if err := hExpectCode(code, line, 501); err != nil {
				t.Errorf("XOVER %s: %v", arg, err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// XOVER line format

func TestOvrLineFormat(t *testing.T) {
	refs := "<parent-1@test.invalid> <parent-2@test.invalid>"
	g := hGroup(t, "t5.format", 1, func(i int, a *hArticle) { a.References = refs })
	art := hDefaultArticle(g, 1)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	ovrSelect(t, c, g)

	code, line, lines := ovrCmd(t, c, "XOVER 1")
	if err := hExpectCode(code, line, 224); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("XOVER 1: got %d lines, want 1: %q", len(lines), lines)
	}
	fields := strings.Split(lines[0], "\t")

	t.Run("mandatory-fields", func(t *testing.T) {
		// RFC 3977 §8.3.2 / RFC 2980 §2.8: number, Subject, From, Date,
		// Message-ID, References, :bytes, :lines, in this order.
		if len(fields) < 8 {
			t.Fatalf("got %d fields, want at least 8: %q", len(fields), lines[0])
		}
		want := []string{"1", art.Subject, art.From, art.DateString, art.MessageID, refs}
		if err := hExpectLines(fields[:6], want); err != nil {
			t.Fatalf("fields 1-6: %v", err)
		}
		// :bytes is a decimal count (its value, the whole article size, is INT-5).
		if n, err := strconv.ParseInt(fields[6], 10, 64); err != nil || n <= 0 {
			t.Errorf(":bytes field %q: want a positive decimal number", fields[6])
		}
		// :lines is the number of body lines (RFC 3977 §8.1.1): the body has 2.
		if fields[7] != "2" {
			t.Errorf(":lines field %q, want 2", fields[7])
		}
	})

	t.Run("no-trailing-field", func(t *testing.T) {
		// RFC 3977 §8.4: LIST OVERVIEW.FMT lists the fields after the 7
		// mandatory ones; only Xref:full there allows a 9th field, and trailing
		// empty fields may be omitted (§8.3.2), so then 8 or 9 fields are fine.
		xrefListed := false
		code, _, fmtLines := ovrCmd(t, c, "LIST OVERVIEW.FMT")
		if code == 215 {
			for _, l := range fmtLines {
				if strings.EqualFold(l, "Xref:full") {
					xrefListed = true
				}
			}
		}
		var err error
		if len(fields) != 8 && (!xrefListed || len(fields) != 9) {
			want := "8"
			if xrefListed {
				want = "8 or 9"
			}
			err = fmt.Errorf("got %d fields, want %s (Xref listed in OVERVIEW.FMT: %v): %q",
				len(fields), want, xrefListed, lines[0])
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// Sanitizing of TAB/CR/LF

func TestOvrSanitizing(t *testing.T) {
	// The DB stores repeated headers joined with LF (common/headers.go), and a
	// value may hold a TAB or a stray CR. Article 1 has two Subject lines;
	// article 2 has a TAB in Subject, a CR in From and an LF in References.
	g := hGroup(t, "t5.sanitize", 2, func(i int, a *hArticle) {
		switch i {
		case 1:
			a.Subject = "Line1\nLine2"
			a.Headers = strings.Join([]string{
				"Path: " + a.Path,
				"From: " + a.From,
				"Subject: Line1",
				"Subject: Line2",
				"Date: " + a.DateString,
				"Message-ID: " + a.MessageID,
			}, "\n")
		case 2:
			a.Subject = "Tab\there"
			a.From = "Carriage\rReturn <cr@test.invalid>"
			a.References = "<r1@test.invalid>\n<r2@test.invalid>"
		}
	})
	a1 := hDefaultArticle(g, 1)
	// RFC 3977 §8.3.2: CRLF pairs are removed, then each remaining TAB, CR and
	// LF is replaced by a single space.
	type want struct{ subject, from, refs string }
	wants := map[string]want{
		"1": {"Line1 Line2", a1.From, ""},
		"2": {"Tab here", "Carriage Return <cr@test.invalid>", "<r1@test.invalid> <r2@test.invalid>"},
	}
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	ovrSelect(t, c, g)

	// A bare LF in a value (A-nntp-rfc-2) splits one overview or header line
	// into two, so a client reading lines is desynchronized.
	t.Run("XOVER", func(t *testing.T) {
		code, line := c.cmd(t, "XOVER 1-2")
		if err := hExpectCode(code, line, 224); err != nil {
			t.Fatal(err)
		}
		lines, err := ovrRawLines(t, c)
		if err == nil && len(lines) != 2 {
			err = fmt.Errorf("got %d lines, want 2: %q", len(lines), lines)
		}
		if err == nil {
			for _, l := range lines {
				f := strings.Split(l, "\t")
				if len(f) < 8 {
					err = fmt.Errorf("line %q: %d fields, want at least 8", l, len(f))
					break
				}
				w, ok := wants[f[0]]
				if !ok {
					err = fmt.Errorf("line %q: unexpected article number", l)
					break
				}
				if f[1] != w.subject || f[2] != w.from || f[5] != w.refs {
					err = fmt.Errorf("line %q: subject/from/references = %q/%q/%q, want %q/%q/%q",
						l, f[1], f[2], f[5], w.subject, w.from, w.refs)
					break
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("XHDR", func(t *testing.T) {
		// RFC 2980 §2.6, with the RFC 3977 §8.5.2 rules for header values: the
		// value is unfolded and TAB/CR/LF become spaces. For a repeated header
		// §8.5.2 returns only the first occurrence, so article 1's Subject may
		// be "Line1" or the joined "Line1 Line2".
		var problems []string
		check := func(field string, value func(n string, w want) []string) {
			code, line := c.cmd(t, "XHDR %s 1-2", field)
			if err := hExpectCode(code, line, 221); err != nil {
				t.Fatalf("XHDR %s: %v", field, err)
			}
			lines, err := ovrRawLines(t, c)
			if err != nil {
				problems = append(problems, fmt.Sprintf("XHDR %s: %v", field, err))
				return
			}
			// Allowed lines per article; absent values are checked in TestOvrXhdr.
			var exp [][]string
			for _, n := range []string{"1", "2"} {
				var alts []string
				for _, v := range value(n, wants[n]) {
					if v != "" {
						alts = append(alts, n+" "+v)
					}
				}
				if len(alts) > 0 {
					exp = append(exp, alts)
				}
			}
			var got []string
			for _, l := range lines {
				// Skip absent-value lines: "n", "n " or "n (none)".
				if strings.Contains(l, " ") && !strings.HasSuffix(l, " ") && !strings.HasSuffix(l, " (none)") {
					got = append(got, l)
				}
			}
			if len(got) != len(exp) {
				problems = append(problems, fmt.Sprintf("XHDR %s: got %d value lines, want %d: got=%q want=%q",
					field, len(got), len(exp), got, exp))
				return
			}
			for i, l := range got {
				ok := false
				for _, alt := range exp[i] {
					ok = ok || l == alt
				}
				if !ok {
					problems = append(problems, fmt.Sprintf("XHDR %s: line %d: got %q, want one of %q", field, i, l, exp[i]))
				}
			}
		}
		check("subject", func(n string, w want) []string {
			if n == "1" {
				return []string{"Line1", w.subject}
			}
			return []string{w.subject}
		})
		check("from", func(n string, w want) []string { return []string{w.from} })
		check("references", func(n string, w want) []string { return []string{w.refs} })
		var err error
		if len(problems) > 0 {
			err = errors.New(strings.Join(problems, "; "))
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// Ranges past 1000 articles

func TestOvrFullRange(t *testing.T) {
	const n = 1500
	g := ovrBulkGroup(t, "t5.fullrange", n)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	ovrSelect(t, c, g)

	// RFC 3977 §8.3.2 / RFC 2980 §2.8: one line per article in the range; a
	// server may not silently drop part of it.
	t.Run("XOVER", func(t *testing.T) {
		code, line, lines := ovrCmd(t, c, "XOVER 1-%d", n)
		if err := hExpectCode(code, line, 224); err != nil {
			t.Fatal(err)
		}
		var err error
		if len(lines) != n {
			err = fmt.Errorf("XOVER 1-%d: got %d lines, want %d (last %q)", n, len(lines), n, ovrLastNum(lines))
		}
		if err != nil {
			t.Fatal(err)
		}
	})

	// RFC 2980 §2.6: one line per article in the range.
	t.Run("XHDR", func(t *testing.T) {
		code, line, lines := ovrCmd(t, c, "XHDR subject 1-%d", n)
		if err := hExpectCode(code, line, 221); err != nil {
			t.Fatal(err)
		}
		var err error
		if len(lines) != n {
			err = fmt.Errorf("XHDR subject 1-%d: got %d lines, want %d (last %q)", n, len(lines), n, ovrLastNum(lines))
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// XHDR

func TestOvrXhdr(t *testing.T) {
	// Articles 1, 2, 4 (3 is a deleted number); none has References.
	g := hGroup(t, "t5.xhdr", 4, func(i int, a *hArticle) { a.Skip = i == 3 })
	a2 := hDefaultArticle(g, 2)
	path := a2.Path
	hs := newTestServer(t, hServerOpts{})

	t.Run("no-group", func(t *testing.T) {
		cc := hs.dial(t)
		defer cc.close()
		// RFC 2980 §2.6: 412 when no newsgroup is selected (range form).
		code, line, _ := ovrCmd(t, cc, "XHDR subject 1-4")
		if err := hExpectCode(code, line, 412); err != nil {
			t.Fatal(err)
		}
	})

	c := hs.dial(t)
	ovrSelect(t, c, g)

	t.Run("range", func(t *testing.T) {
		// RFC 2980 §2.6: 221, then "<number> <value>" for each article in the range.
		code, line, lines := ovrCmd(t, c, "XHDR subject 1-4")
		if err := hExpectCode(code, line, 221); err != nil {
			t.Fatal(err)
		}
		want := []string{"1 Test article 1", "2 Test article 2", "4 Test article 4"}
		if err := hExpectLines(lines, want); err != nil {
			t.Fatal(err)
		}
	})

	// RFC 2980 §2.6: XHDR works for any header of the article, not only the
	// overview fields. RFC 3977 §8.5.2 also allows 503 for a header the server
	// does not serve; an empty 221 is wrong either way.
	for _, tc := range []struct{ header, value string }{
		{"Newsgroups", g},
		{"Path", path},
	} {
		t.Run("present-"+tc.header, func(t *testing.T) {
			code, line, lines := ovrCmd(t, c, "XHDR %s 1-2", tc.header)
			var err error
			if code != 503 {
				err = hExpectCode(code, line, 221, 503)
				if err == nil {
					err = hExpectLines(lines, []string{"1 " + tc.value, "2 " + tc.value})
				}
			}
			if err != nil {
				err = fmt.Errorf("XHDR %s 1-2: %w", tc.header, err)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	// RFC 3977 §8.5.2: one line per existing article in the range; when the
	// article lacks the header the value is empty (the space after the number
	// MAY be omitted). "n (none)" (INN style) is accepted too.
	t.Run("absent-References", func(t *testing.T) {
		// References is an overview field the server serves: correct today.
		code, line, lines := ovrCmd(t, c, "XHDR References 1-2")
		if err := hExpectCode(code, line, 221); err != nil {
			t.Fatal(err)
		}
		if err := ovrAbsentLines(lines, "1", "2"); err != nil {
			t.Fatalf("XHDR References 1-2: %v", err)
		}
	})
	t.Run("absent-X-Ovr-Absent", func(t *testing.T) {
		code, line, lines := ovrCmd(t, c, "XHDR X-Ovr-Absent 1-2")
		err := hExpectCode(code, line, 221)
		if err == nil {
			err = ovrAbsentLines(lines, "1", "2")
		}
		if err != nil {
			err = fmt.Errorf("XHDR X-Ovr-Absent 1-2: %w", err)
		}
		if err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §8.5.2: 423 "No articles in that range" (420 in RFC 2980 terms)
	// for an empty range and for a reversed one (§6.1.2.2: no articles).
	for _, tc := range []struct{ name, arg string }{
		{"past-high", "100-200"},
		{"reversed", "4-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, line, lines := ovrCmd(t, c, "XHDR subject %s", tc.arg)
			err := hExpectCode(code, line, 423, 420)
			if err != nil {
				err = fmt.Errorf("XHDR subject %s: %w (block %q)", tc.arg, err, lines)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("message-id", func(t *testing.T) {
		// RFC 2980 §2.6: with a message-id argument the line starts with that
		// message-id instead of an article number.
		code, line, lines := ovrCmd(t, c, "XHDR subject %s", a2.MessageID)
		err := hExpectCode(code, line, 221)
		if err == nil {
			err = hExpectLines(lines, []string{a2.MessageID + " " + a2.Subject})
		}
		if err != nil {
			err = fmt.Errorf("XHDR subject %s: %w", a2.MessageID, err)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// SEC-1: XHDR/XOVER with a huge range

func TestOvrHugeRange(t *testing.T) {
	// 4*hDeadline: in the regression case the child's own hDeadline guards add
	// up to about 3*hDeadline, and its errors must surface instead of "timed out".
	err := runIsolated(t, 4*hDeadline, func(t *testing.T) {
		g := hGroup(t, "t5.hugerange", 3, nil)
		hs := newTestServer(t, hServerOpts{})
		want := []string{"1", "2", "3"}
		// RFC 2980 §2.6 / §2.8: the response for a range is the existing
		// articles in it, then the terminator; a huge range end must not make
		// the server walk every number. The assertion is the reply: its code and
		// exactly the 3 existing articles. A server that walks the range never
		// sends the "." and trips the hDeadline hang guard (not a timing bound).
		for _, tc := range []struct {
			cmd  string
			code int
		}{
			{"XHDR subject 1-9223372036854775807", 221},
			{"XOVER 1-9223372036854775807", 224},
		} {
			cmd := tc.cmd
			c := hs.dial(t)
			ovrSelect(t, c, g)
			code, line, err := c.tryCmd("%s", cmd)
			if err != nil {
				t.Errorf("%s: %v (%q)", cmd, err, line)
				continue
			}
			if code != tc.code {
				t.Errorf("%s: got %q, want %d", cmd, line, tc.code)
				continue
			}
			lines, err := ovrTryDotLines(c)
			if err != nil {
				t.Errorf("%s: %v", cmd, err)
				continue
			}
			if err := hExpectLines(ovrNums(lines), want); err != nil {
				t.Errorf("%s: %v", cmd, err)
			}
			c.close()
		}
	})
	if ovrIsChild(t) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// OVER/HDR (RFC 3977)

func TestOvrOverHdr(t *testing.T) {
	g := hGroup(t, "t5.overhdr", 2, nil)
	a1 := hDefaultArticle(g, 1)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	ovrSelect(t, c, g)

	t.Run("OVER", func(t *testing.T) {
		// RFC 3977 §8.3: OVER <range> → 224 and one overview line per article.
		code, line, lines := ovrCmd(t, c, "OVER 1-2")
		err := hExpectCode(code, line, 224)
		if err == nil {
			err = hExpectLines(ovrNums(lines), []string{"1", "2"})
		}
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("HDR", func(t *testing.T) {
		// RFC 3977 §8.5: HDR <field> <range> → 225 and "<number> <value>" lines.
		code, line, lines := ovrCmd(t, c, "HDR Subject 1")
		err := hExpectCode(code, line, 225)
		if err == nil {
			err = hExpectLines(lines, []string{"1 " + a1.Subject})
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// Range parser (plan nntp-server-hardening, D2)

func TestOvrRangeParse(t *testing.T) {
	c := &ClientConnection{}
	ok := []struct {
		arg   string
		want  articleRange
		empty bool
	}{
		{"5", articleRange{First: 5, Last: 5}, false},
		{"5-", articleRange{First: 5, Last: math.MaxInt64, Open: true}, false},
		{"5-9", articleRange{First: 5, Last: 9}, false},
		{"9-5", articleRange{First: 9, Last: 5}, true},
		{"0-", articleRange{First: 0, Last: math.MaxInt64, Open: true}, false},
		{"0", articleRange{First: 0, Last: 0}, false},
		{"9999999999999999", articleRange{First: 9999999999999999, Last: 9999999999999999}, false},
		{"1-9999999999999999", articleRange{First: 1, Last: 9999999999999999}, false},
		// 17+ digits up to math.MaxInt64 are accepted (deviation from the 16-digit
		// RFC 3977 §9.8 limit: TestOvrHugeRange and the N22 probe use this end).
		{"12345678901234567", articleRange{First: 12345678901234567, Last: 12345678901234567}, false},
		{"1-9223372036854775807", articleRange{First: 1, Last: math.MaxInt64}, false},
		{"0009", articleRange{First: 9, Last: 9}, false},
	}
	for _, tc := range ok {
		got, err := c.parseRangeArg(tc.arg)
		if err != nil {
			t.Errorf("parseRangeArg(%q): unexpected error %v", tc.arg, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseRangeArg(%q) = %+v, want %+v", tc.arg, got, tc.want)
		}
		if got.Empty() != tc.empty {
			t.Errorf("parseRangeArg(%q).Empty() = %v, want %v", tc.arg, got.Empty(), tc.empty)
		}
	}
	for _, arg := range []string{
		"", "-", "x-5", "5-y", "1-2-3", "+1", "-5", " 5", "5 ", "1--2",
		"9223372036854775808", "1-9223372036854775808", "12345678901234567890", "00000000000000000001",
	} {
		if got, err := c.parseRangeArg(arg); !errors.Is(err, errRangeSyntax) {
			t.Errorf("parseRangeArg(%q) = %+v, %v; want errRangeSyntax", arg, got, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Hidden articles (decision 2: hide > 0 is never served over NNTP)

func TestOvrHidden(t *testing.T) {
	g := hGroup(t, "t5.hidden", 3, nil)
	hidden := hDefaultArticle(g, 2)
	gdb, err := hTestDB(t).GetGroupDB(g)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", g, err)
	}
	_, err = database.RetryableExec(gdb.DB, `UPDATE articles SET hide = 1 WHERE article_num = 2`)
	gdb.Return()
	if err != nil {
		t.Fatalf("hide article 2: %v", err)
	}

	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	ovrSelect(t, c, g)

	t.Run("XOVER", func(t *testing.T) {
		code, line, lines := ovrCmd(t, c, "XOVER 1-")
		if err := hExpectCode(code, line, 224); err != nil {
			t.Fatal(err)
		}
		if err := hExpectLines(ovrNums(lines), []string{"1", "3"}); err != nil {
			t.Fatalf("XOVER 1-: %v", err)
		}
	})
	t.Run("XOVER-only-hidden", func(t *testing.T) {
		code, line, _ := ovrCmd(t, c, "XOVER 2")
		if err := hExpectCode(code, line, 423); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("XHDR", func(t *testing.T) {
		code, line, lines := ovrCmd(t, c, "XHDR subject 1-")
		if err := hExpectCode(code, line, 221); err != nil {
			t.Fatal(err)
		}
		if err := hExpectLines(ovrNums(lines), []string{"1", "3"}); err != nil {
			t.Fatalf("XHDR subject 1-: %v", err)
		}
	})
	t.Run("XHDR-message-id", func(t *testing.T) {
		code, line, _ := ovrCmd(t, c, "XHDR subject %s", hidden.MessageID)
		if err := hExpectCode(code, line, 430); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("XHDR-status-no-echo", func(t *testing.T) {
		// A-nntp-rfc-6: the status line echoes at most 64 bytes of the field name.
		long := strings.Repeat("X", 300)
		code, line, _ := ovrCmd(t, c, "XHDR %s 1-", long)
		if err := hExpectCode(code, line, 221); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, strings.Repeat("X", 65)) {
			t.Fatalf("status line echoes more than 64 bytes of the field: %q", line)
		}
	})
}

// ---------------------------------------------------------------------------
// XHDR Xref/Path: consistent with HEAD/ARTICLE (common.ReconstructHeaders drops the
// stored Xref and rebuilds Path from the path column), never the raw upstream lines.

func TestOvrXhdrXrefPath(t *testing.T) {
	g := hGroup(t, "t5.xrefpath", 2, func(i int, a *hArticle) {
		a.Headers = strings.Join([]string{
			"Path: upstream.example!raw-path",
			"From: " + a.From,
			"Newsgroups: other.group",
			"Subject: " + a.Subject,
			"Date: " + a.DateString,
			"Message-ID: " + a.MessageID,
			fmt.Sprintf("Xref: upstream.example other.group:%d", 9000+i),
		}, "\n")
	})
	a1 := hDefaultArticle(g, 1)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	ovrSelect(t, c, g)

	for _, field := range []string{"Xref", "XREF"} {
		code, line, lines := ovrCmd(t, c, "XHDR %s 1-2", field)
		if err := hExpectCode(code, line, 221); err != nil {
			t.Fatalf("XHDR %s: %v", field, err)
		}
		if err := hExpectLines(lines, []string{"1", "2"}); err != nil {
			t.Fatalf("XHDR %s 1-2: %v", field, err)
		}
	}
	code, line, lines := ovrCmd(t, c, "XHDR Xref %s", a1.MessageID)
	if err := hExpectCode(code, line, 221); err != nil {
		t.Fatalf("XHDR Xref <id>: %v", err)
	}
	if err := hExpectLines(lines, []string{a1.MessageID}); err != nil {
		t.Fatalf("XHDR Xref <id>: %v", err)
	}

	// Path: the stored path column (a.Path), not the raw upstream line in headers_json.
	code, line, lines = ovrCmd(t, c, "XHDR path 1-2")
	if err := hExpectCode(code, line, 221); err != nil {
		t.Fatalf("XHDR path: %v", err)
	}
	if err := hExpectLines(lines, []string{"1 " + a1.Path, "2 " + a1.Path}); err != nil {
		t.Fatalf("XHDR path 1-2: %v", err)
	}
	code, line, lines = ovrCmd(t, c, "XHDR Path %s", a1.MessageID)
	if err := hExpectCode(code, line, 221); err != nil {
		t.Fatalf("XHDR Path <id>: %v", err)
	}
	if err := hExpectLines(lines, []string{a1.MessageID + " " + a1.Path}); err != nil {
		t.Fatalf("XHDR Path <id>: %v", err)
	}
}

// XHDR <field> <message-id> with a processor: a definite miss is 430, a lookup fault
// (history DB) is 403 (decision 15).
func TestOvrXhdrLookupFault(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"miss", fmt.Errorf("message-id: %w", ErrArticleNotFound), 430},
		{"fault", errors.New("history lookup failed: disk I/O error"), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{findErr: tc.err}})
			c := hs.dial(t)
			defer c.close()
			code, line, _ := ovrCmd(t, c, "XHDR subject <ovr-fault@test.invalid>")
			if err := hExpectCode(code, line, tc.want); err != nil {
				t.Fatal(err)
			}
		})
	}
}
