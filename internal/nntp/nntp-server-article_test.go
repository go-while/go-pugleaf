package nntp

// Article retrieval tests for the NNTP server (plan nntp-audit-tests, slice
// test-nntp-article): ARTICLE/HEAD/BODY/STAT by number, by message-id in the
// current group and in another group, without an argument, with invalid or
// missing numbers, the current article pointer, byte-exact output (CRLF and
// dot-stuffing), the local430 negative cache and the per-command delay.
//
// Every check states the RFC-correct behavior (RFC 3977). Where the current
// code differs, the result goes through knownBug with the seed id (BUG-1,
// BUG-6, BUG-8, RFC-3) or a new id (T-nntp-article-1). The harness servers run with ErrorDelay=0, so
// rateLimitOnError does not sleep on the error replies below.
// Identifiers use the art/TestArt prefixes (contract K2). Note that
// `-run TestArt` also matches the pre-existing TestArticleMatching; run these
// tests with `-run '^TestArt[A-Z]'`.

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// artCmds maps each retrieval command to its RFC 3977 §6.2 success code.
var artCmds = []struct {
	name string
	code int
}{
	{"ARTICLE", 220},
	{"HEAD", 221},
	{"BODY", 222},
	{"STAT", 223},
}

// ---------------------------------------------------------------------------
// Helpers (art prefix; the harness is read-only)

// artDefaultHeaders returns the header lines stored for a default article a of group.
func artDefaultHeaders(group string, a hArticle) []string {
	return []string{
		"Path: " + a.Path,
		"From: " + a.From,
		"Newsgroups: " + group,
		"Subject: " + a.Subject,
		"Date: " + a.DateString,
		"Message-ID: " + a.MessageID,
	}
}

// artSeed creates group "t4.<test>.<seq>" with articles 1..n whose header block
// is set explicitly (so the expected output is known), applies mutate and
// returns the group name and the inserted articles.
func artSeed(t *testing.T, test string, n int, mutate func(i int, a *hArticle)) (string, []hArticle) {
	t.Helper()
	g := hGroup(t, "t4."+test, 0, nil)
	var arts []hArticle
	for i := 1; i <= n; i++ {
		a := hDefaultArticle(g, int64(i))
		a.Headers = strings.Join(artDefaultHeaders(g, a), "\n")
		if mutate != nil {
			mutate(i, &a)
		}
		if a.Skip {
			continue
		}
		arts = append(arts, a)
	}
	if len(arts) > 0 {
		hArticleSQL(t, g, arts...)
	}
	return g, arts
}

// artFind returns the seeded article with number num.
func artFind(t *testing.T, arts []hArticle, num int64) hArticle {
	t.Helper()
	for _, a := range arts {
		if a.Num == num {
			return a
		}
	}
	t.Fatalf("no seeded article %d", num)
	return hArticle{}
}

// artHeaderLines splits a stored header block into lines.
func artHeaderLines(a hArticle) []string {
	return strings.Split(a.Headers, "\n")
}

// artBodyLines splits a stored body into lines; an empty body has no lines.
func artBodyLines(a hArticle) []string {
	if a.Body == "" {
		return nil
	}
	return strings.Split(a.Body, "\n")
}

// artExpectedLines returns the (un-stuffed) lines of the multi-line block
// that cmd should send for a (RFC 3977 §6.2.1-§6.2.3).
func artExpectedLines(cmd string, a hArticle) []string {
	switch cmd {
	case "ARTICLE":
		out := append([]string{}, artHeaderLines(a)...)
		out = append(out, "")
		return append(out, artBodyLines(a)...)
	case "HEAD":
		return artHeaderLines(a)
	case "BODY":
		return artBodyLines(a)
	}
	return nil
}

// artDo sends one command and reads the multi-line block when the reply is
// 220, 221 or 222, so the connection stays in sync whatever the server answers.
func artDo(t *testing.T, c *hClient, format string, args ...interface{}) (int, string, []string) {
	t.Helper()
	code, line := c.cmd(t, format, args...)
	var lines []string
	if code >= 220 && code <= 222 {
		lines = c.readDotLines(t)
	}
	return code, line, lines
}

// artCheckStatus checks "<wantCode> <wantNum> <wantMID> ..." (RFC 3977 §6.2:
// the first two arguments are the article number and the message-id).
func artCheckStatus(code int, line string, wantCode int, wantNum int64, wantMID string) error {
	f := strings.Fields(line)
	if code != wantCode || len(f) < 3 || f[1] != strconv.FormatInt(wantNum, 10) || f[2] != wantMID {
		return fmt.Errorf("got %q, want \"%d %d %s ...\"", line, wantCode, wantNum, wantMID)
	}
	return nil
}

// artCheckRetrieval runs "<cmd> <arg>" and checks the status line and the
// block against article a reported as number wantNum.
func artCheckRetrieval(t *testing.T, c *hClient, cmd, arg string, wantCode int, wantNum int64, a hArticle) error {
	t.Helper()
	code, line, lines := artDo(t, c, "%s %s", cmd, arg)
	if err := artCheckStatus(code, line, wantCode, wantNum, a.MessageID); err != nil {
		return fmt.Errorf("%s %s: %w", cmd, arg, err)
	}
	if cmd == "STAT" {
		return nil
	}
	if err := hExpectLines(lines, artExpectedLines(cmd, a)); err != nil {
		return fmt.Errorf("%s %s block: %w", cmd, arg, err)
	}
	return nil
}

// artRawResponse sends one command and returns the raw status line and, for
// 220/221/222, the raw block up to and including the terminator.
func artRawResponse(t *testing.T, c *hClient, cmd string) (status, block []byte) {
	t.Helper()
	if err := c.send(cmd + CRLF); err != nil {
		t.Fatalf("send %q: %v", cmd, err)
	}
	c.bump()
	status, err := c.R.ReadBytes('\n')
	if err != nil {
		t.Fatalf("%s: status line: %v (%q)", cmd, err, status)
	}
	code, err := hParseCode(strings.TrimRight(string(status), "\r\n"))
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	if code >= 220 && code <= 222 {
		block = c.rawUntilDot(t)
	}
	return status, block
}

// artCRLF joins lines, each terminated by CRLF.
func artCRLF(lines ...string) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString(CRLF)
	}
	return b.Bytes()
}

// artCheckCRLF returns an error unless every line in raw ends in CRLF
// (RFC 3977 §3.1: lines are terminated by CRLF; no bare LF or CR).
func artCheckCRLF(what string, raw []byte) error {
	if !bytes.HasSuffix(raw, []byte(CRLF)) {
		return fmt.Errorf("%s: does not end in CRLF: %q", what, raw)
	}
	for i, b := range raw {
		if b == '\n' && (i == 0 || raw[i-1] != '\r') {
			return fmt.Errorf("%s: bare LF at offset %d: %q", what, i, raw)
		}
		if b == '\r' && (i+1 >= len(raw) || raw[i+1] != '\n') {
			return fmt.Errorf("%s: bare CR at offset %d: %q", what, i, raw)
		}
	}
	return nil
}

// artExpectRaw compares raw bytes.
func artExpectRaw(what string, got, want []byte) error {
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
	return nil
}

// artSetHeadersJSON overwrites headers_json of article num in group (hArticleSQL
// regenerates an empty header block, so an empty one is set afterwards).
func artSetHeadersJSON(t *testing.T, group string, num int64, headers string) {
	t.Helper()
	gdb, err := hTestDB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	if _, err := database.RetryableExec(gdb.DB, `UPDATE articles SET headers_json = ? WHERE article_num = ?`, headers, num); err != nil {
		t.Fatalf("update headers_json of %d in %s: %v", num, group, err)
	}
}

// artSelect sends GROUP g and fails unless it answers 211 (RFC 3977 §6.1.1).
func artSelect(t *testing.T, c *hClient, g string) {
	t.Helper()
	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatalf("GROUP %s: %v", g, err)
	}
}

// ---------------------------------------------------------------------------
// By number, by message-id

func TestArtByNumber(t *testing.T) {
	g, arts := artSeed(t, "bynumber", 3, nil)
	hs := newTestServer(t, hServerOpts{})
	a2 := artFind(t, arts, 2)

	for _, cmd := range artCmds {
		t.Run(cmd.name, func(t *testing.T) {
			c := hs.dial(t)
			artSelect(t, c, g)
			// RFC 3977 §6.2.1-§6.2.4: "<cmd> n" answers 220/221/222/223 "n <message-id>";
			// ARTICLE sends headers, a blank line and the body, HEAD the headers,
			// BODY the body, STAT nothing.
			if err := artCheckRetrieval(t, c, cmd.name, "2", cmd.code, 2, a2); err != nil {
				t.Fatal(err)
			}
			// The connection is still in sync: the next reply is the next STAT.
			if err := artCheckRetrieval(t, c, "STAT", "1", 223, 1, artFind(t, arts, 1)); err != nil {
				t.Fatalf("after %s: %v", cmd.name, err)
			}
		})
	}
}

func TestArtByMessageIDCurrentGroup(t *testing.T) {
	// Articles 5..7 only, so the reported number cannot be a list index.
	g, arts := artSeed(t, "bymsgid", 7, func(i int, a *hArticle) { a.Skip = i < 5 })
	hs := newTestServer(t, hServerOpts{})
	a6 := artFind(t, arts, 6)

	for _, cmd := range artCmds {
		t.Run(cmd.name, func(t *testing.T) {
			c := hs.dial(t)
			artSelect(t, c, g)
			// RFC 3977 §6.2.1.2: for the message-id form, n is the article's number
			// in the currently selected group when it is in that group.
			if err := artCheckRetrieval(t, c, cmd.name, a6.MessageID, cmd.code, 6, a6); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestArtMessageIDOtherGroup(t *testing.T) {
	gA, _ := artSeed(t, "othergrp-a", 1, nil)
	// The article lives in group B as number 7: replying 7 would leak B's
	// numbering into group A.
	gB, artsB := artSeed(t, "othergrp-b", 7, func(i int, a *hArticle) { a.Skip = i < 7 })
	b7 := artFind(t, artsB, 7)

	// db is taken here: findFn runs on the server goroutine, where hTestDB's
	// t.Skipf must not be called.
	db := hTestDB(t)
	proc := &hFakeProcessor{}
	proc.findFn = func(messageID, currentGroup string) (*models.Article, error) {
		gdb, err := db.GetGroupDB(gB)
		if err != nil {
			return nil, err
		}
		defer gdb.Return()
		a, err := db.GetArticleByMessageID(gdb, messageID)
		if err != nil {
			return nil, fmt.Errorf("artOtherGroup %s: %w", messageID, ErrArticleNotFound)
		}
		return a, nil
	}
	hs := newTestServer(t, hServerOpts{Proc: proc})

	for _, cmd := range artCmds {
		t.Run("other-group-selected/"+cmd.name, func(t *testing.T) {
			c := hs.dial(t)
			artSelect(t, c, gA)
			before := proc.findCount()
			// RFC 3977 §6.2.1.2: an article selected by message-id that is not in
			// the current group is reported with article number 0.
			if err := artCheckRetrieval(t, c, cmd.name, b7.MessageID, cmd.code, 0, b7); err != nil {
				t.Fatal(err)
			}
			if proc.findCount() <= before {
				t.Fatalf("processor lookup not called (findCount %d)", proc.findCount())
			}
		})
	}
	t.Run("no-group-selected", func(t *testing.T) {
		c := hs.dial(t)
		// RFC 3977 §6.2.1.2: without a selected group the number is 0 as well.
		if err := artCheckRetrieval(t, c, "STAT", b7.MessageID, 223, 0, b7); err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// No argument, invalid and missing numbers

func TestArtNoArgument(t *testing.T) {
	g, arts := artSeed(t, "noarg", 2, nil)
	gEmpty, _ := artSeed(t, "noarg-empty", 0, nil)
	hs := newTestServer(t, hServerOpts{})
	a1 := artFind(t, arts, 1)

	t.Run("current-article", func(t *testing.T) {
		c := hs.dial(t)
		artSelect(t, c, g)
		// RFC 3977 §6.2.1-§6.2.4: without an argument the current article is used.
		var errs []error
		for _, cmd := range artCmds {
			// STAT n sets the current article (RFC 3977 §6.2.4.2). It is set again
			// before each bare command, so an RFC-3 fix is not hidden by BUG-1
			// (a bare ARTICLE/HEAD/BODY resetting the current article to 0).
			if err := artCheckRetrieval(t, c, "STAT", "1", 223, 1, a1); err != nil {
				t.Fatal(err)
			}
			code, line, lines := artDo(t, c, "%s", cmd.name)
			err := artCheckStatus(code, line, cmd.code, 1, a1.MessageID)
			if err == nil && cmd.name != "STAT" {
				err = hExpectLines(lines, artExpectedLines(cmd.name, a1))
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("bare %s: %w", cmd.name, err))
			}
		}
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("invalid-current-article-420", func(t *testing.T) {
		c := hs.dial(t)
		// An empty group has no valid current article.
		artSelect(t, c, gEmpty)
		var errs []error
		for _, cmd := range artCmds {
			code, line, _ := artDo(t, c, "%s", cmd.name)
			// RFC 3977 §6.2.1.2: 420 current article number is invalid.
			if err := hExpectCode(code, line, 420); err != nil {
				errs = append(errs, fmt.Errorf("bare %s: %w", cmd.name, err))
			}
		}
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no-group-412", func(t *testing.T) {
		c := hs.dial(t)
		var errs []error
		for _, cmd := range artCmds {
			code, line, _ := artDo(t, c, "%s", cmd.name)
			// RFC 3977 §6.2.1.2: 412 no newsgroup selected (the current-article
			// form needs a group).
			if err := hExpectCode(code, line, 412); err != nil {
				errs = append(errs, fmt.Errorf("bare %s: %w", cmd.name, err))
			}
		}
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	})
}

func TestArtInvalidNumbers(t *testing.T) {
	g, arts := artSeed(t, "invalidnum", 2, nil)
	hs := newTestServer(t, hServerOpts{})
	cases := []struct {
		arg string
		bug string // seed id when the current code differs; "" when already correct
	}{
		// "0" matches article-number (1*16DIGIT) but no article 0 exists: 423.
		// "-1" is not an article-number: 501. Either reply is accepted for both.
		{"0", ""},
		{"-1", ""},
		// 20 digits exceeds 1*16DIGIT and int64: 501 (423 accepted).
		{"99999999999999999999", ""},
	}
	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			c := hs.dial(t)
			artSelect(t, c, g)
			var errs []error
			for _, cmd := range artCmds {
				code, line, _ := artDo(t, c, "%s %s", cmd.name, tc.arg)
				// RFC 3977 §6.2.1.2 (423 no article with that number) and §3.2.1
				// (501 syntax error in arguments).
				if err := hExpectCode(code, line, 423, 501); err != nil {
					errs = append(errs, fmt.Errorf("%s %s: %w", cmd.name, tc.arg, err))
				}
			}
			// The connection is still usable afterwards.
			if err := artCheckRetrieval(t, c, "STAT", "1", 223, 1, artFind(t, arts, 1)); err != nil {
				errs = append(errs, fmt.Errorf("after invalid numbers: %w", err))
			}
			err := errors.Join(errs...)
			if tc.bug != "" {
				knownBug(t, tc.bug, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestArtNotFound(t *testing.T) {
	// Articles 1 and 3; 2 is a deleted number.
	g, _ := artSeed(t, "notfound", 3, func(i int, a *hArticle) { a.Skip = i == 2 })
	missingMID := hMessageID(g, 99)

	t.Run("missing-number-423", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		artSelect(t, c, g)
		for _, arg := range []string{"2", "99"} {
			for _, cmd := range artCmds {
				code, line, _ := artDo(t, c, "%s %s", cmd.name, arg)
				// RFC 3977 §6.2.1.2: 423 no article with that number.
				if err := hExpectCode(code, line, 423); err != nil {
					t.Errorf("%s %s: %v", cmd.name, arg, err)
				}
			}
		}
	})
	t.Run("missing-message-id-430", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		for _, selected := range []bool{false, true} {
			if selected {
				artSelect(t, c, g)
			}
			for _, cmd := range artCmds {
				code, line, _ := artDo(t, c, "%s %s", cmd.name, missingMID)
				// RFC 3977 §6.2.1.2: 430 no article with that message-id.
				if err := hExpectCode(code, line, 430); err != nil {
					t.Errorf("%s %s (group selected %v): %v", cmd.name, missingMID, selected, err)
				}
			}
		}
	})
	t.Run("missing-message-id-430-processor", func(t *testing.T) {
		proc := &hFakeProcessor{findErr: fmt.Errorf("artNotFound: %w", ErrArticleNotFound)}
		hs := newTestServer(t, hServerOpts{Proc: proc})
		c := hs.dial(t)
		artSelect(t, c, g)
		for _, cmd := range artCmds {
			code, line, _ := artDo(t, c, "%s %s", cmd.name, missingMID)
			// RFC 3977 §6.2.1.2: 430 no article with that message-id.
			if err := hExpectCode(code, line, 430); err != nil {
				t.Errorf("%s %s: %v", cmd.name, missingMID, err)
			}
		}
	})
	t.Run("no-group-412", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		for _, cmd := range artCmds {
			code, line, _ := artDo(t, c, "%s 1", cmd.name)
			// RFC 3977 §6.2.1.2: the number form without a selected group → 412.
			if err := hExpectCode(code, line, 412); err != nil {
				t.Errorf("%s 1: %v", cmd.name, err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Current article

func TestArtCurrentArticle(t *testing.T) {
	g, arts := artSeed(t, "current", 3, nil)
	hs := newTestServer(t, hServerOpts{})
	a2 := artFind(t, arts, 2)
	a3 := artFind(t, arts, 3)

	// RFC 3977 §6.2.1.2: a successful ARTICLE/HEAD/BODY n sets the current
	// article number to n. A bare STAT would show it but is masked by RFC-3
	// (501), so the pointer is read with a bare XOVER, which uses the current
	// article (RFC 2980 §2.8, RFC 3977 §8.3.2: 224 and the overview line of n;
	// 420 when the current article is invalid). Today GetArticleByNum never sets
	// DBArtNum, so the current article becomes 0 and XOVER answers 420 (BUG-1).
	for _, cmd := range artCmds[:3] { // ARTICLE, HEAD, BODY
		t.Run("after-"+cmd.name, func(t *testing.T) {
			c := hs.dial(t)
			artSelect(t, c, g)
			if err := artCheckRetrieval(t, c, cmd.name, "2", cmd.code, 2, a2); err != nil {
				t.Fatal(err)
			}
			if err := artCheckBareXover(t, c, 2); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("after-STAT", func(t *testing.T) {
		c := hs.dial(t)
		artSelect(t, c, g)
		// RFC 3977 §6.2.4.2: STAT n sets the current article as well. The STAT
		// path does set it today (control for BUG-1: bare XOVER works).
		if err := artCheckRetrieval(t, c, "STAT", "2", 223, 2, a2); err != nil {
			t.Fatal(err)
		}
		if err := artCheckBareXover(t, c, 2); err != nil {
			t.Fatal(err)
		}
		// RFC 3977 §6.2.4: a bare STAT reports the current article; today 501 (RFC-3).
		code, line, _ := artDo(t, c, "STAT")
		if err := artCheckStatus(code, line, 223, 2, a2.MessageID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("message-id-lookup-unchanged", func(t *testing.T) {
		c := hs.dial(t)
		artSelect(t, c, g)
		if err := artCheckRetrieval(t, c, "STAT", "2", 223, 2, a2); err != nil {
			t.Fatal(err)
		}
		// RFC 3977 §6.2.1.2: the message-id form MUST NOT change the current
		// article number.
		if err := artCheckRetrieval(t, c, "ARTICLE", a3.MessageID, 220, 3, a3); err != nil {
			t.Fatal(err)
		}
		if err := artCheckBareXover(t, c, 2); err != nil {
			t.Fatal(err)
		}
		// The same through a bare STAT, which answers 501 today (RFC-3).
		code, line, _ := artDo(t, c, "STAT")
		if err := artCheckStatus(code, line, 223, 2, a2.MessageID); err != nil {
			t.Fatal(err)
		}
	})
}

// artCheckBareXover sends a bare XOVER and checks that it answers 224 with one
// overview line for article wantNum (RFC 2980 §2.8, RFC 3977 §8.3.2).
func artCheckBareXover(t *testing.T, c *hClient, wantNum int64) error {
	t.Helper()
	code, line := c.cmd(t, "XOVER")
	if code != 224 {
		return fmt.Errorf("bare XOVER: got %q, want 224 and the overview of %d", line, wantNum)
	}
	lines := c.readDotLines(t)
	if len(lines) != 1 || strings.SplitN(lines[0], "\t", 2)[0] != strconv.FormatInt(wantNum, 10) {
		return fmt.Errorf("bare XOVER: got lines %q, want one overview line of %d", lines, wantNum)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Byte-exact output

func TestArtByteExact(t *testing.T) {
	t.Run("crlf-and-body-dot-stuffing", func(t *testing.T) {
		g, arts := artSeed(t, "bytes-body", 1, func(_ int, a *hArticle) {
			a.Body = ".dot\n.\nplain\n..two\n"
		})
		a := arts[0]
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		artSelect(t, c, g)

		// RFC 3977 §3.1.1: every line of a multi-line block ends in CRLF; a line
		// starting with "." is sent with one more "." ("." → "..", ".dot" → "..dot");
		// the block ends with ".\r\n". The stored body ends in LF, i.e. its last
		// line is empty.
		wantBody := artCRLF("..dot", "..", "plain", "...two", "")
		wantArticle := append(artCRLF(artHeaderLines(a)...), CRLF...)
		wantArticle = append(wantArticle, wantBody...)
		wantArticle = append(wantArticle, ".\r\n"...)

		status, block := artRawResponse(t, c, "ARTICLE 1")
		if err := artCheckCRLF("ARTICLE status", status); err != nil {
			t.Error(err)
		}
		if err := artCheckCRLF("ARTICLE block", block); err != nil {
			t.Error(err)
		}
		if err := artExpectRaw("ARTICLE block", block, wantArticle); err != nil {
			t.Error(err)
		}

		status, block = artRawResponse(t, c, "BODY 1")
		if err := artCheckCRLF("BODY status", status); err != nil {
			t.Error(err)
		}
		if err := artExpectRaw("BODY block", block, append(wantBody, ".\r\n"...)); err != nil {
			t.Error(err)
		}

		status, block = artRawResponse(t, c, "HEAD 1")
		if err := artCheckCRLF("HEAD status", status); err != nil {
			t.Error(err)
		}
		if err := artExpectRaw("HEAD block", block, append(artCRLF(artHeaderLines(a)...), ".\r\n"...)); err != nil {
			t.Error(err)
		}

		status, _ = artRawResponse(t, c, "STAT 1")
		if err := artCheckCRLF("STAT status", status); err != nil {
			t.Error(err)
		}
	})

	// A header line starting with "." must be dot-stuffed like any other line
	// of a multi-line block (RFC 3977 §3.1.1). Header lines are sent unstuffed
	// today (BUG-6).
	t.Run("header-dot-continuation", func(t *testing.T) {
		g, arts := artSeed(t, "bytes-hdrdot", 1, func(_ int, a *hArticle) {
			// A stored header line starting with "." (lax intake).
			a.Headers += "\nX-Folded: first\n.second part"
		})
		a := arts[0]
		hs := newTestServer(t, hServerOpts{})
		wantHead := artCRLF(append(artDefaultHeaders(g, a), "X-Folded: first", "..second part")...)
		var errs []error

		c := hs.dial(t)
		artSelect(t, c, g)
		_, block := artRawResponse(t, c, "HEAD 1")
		errs = append(errs, artExpectRaw("HEAD block", block, append(append([]byte{}, wantHead...), ".\r\n"...)))

		_, block = artRawResponse(t, c, "ARTICLE 1")
		wantArticle := append(append([]byte{}, wantHead...), CRLF...)
		wantArticle = append(wantArticle, artCRLF(artBodyLines(a)...)...)
		errs = append(errs, artExpectRaw("ARTICLE block", block, append(wantArticle, ".\r\n"...)))
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	})
	// A stored header line "." (a dot-stuffed ".." unstuffed on intake) is sent
	// as "." and ends HEAD/ARTICLE early (audit A-nntp-rfc-2, part of BUG-6).
	t.Run("header-lone-dot", func(t *testing.T) {
		g, arts := artSeed(t, "bytes-hdrlone", 1, func(_ int, a *hArticle) {
			a.Headers += "\nX-Folded: first\n."
		})
		a := arts[0]
		hs := newTestServer(t, hServerOpts{})
		wantHead := artCRLF(append(artDefaultHeaders(g, a), "X-Folded: first", "..")...)
		var errs []error

		// Separate connections: an early "." leaves the rest of the block unread.
		c := hs.dial(t)
		artSelect(t, c, g)
		_, block := artRawResponse(t, c, "HEAD 1")
		errs = append(errs, artExpectRaw("HEAD block", block, append(append([]byte{}, wantHead...), ".\r\n"...)))
		c.close()

		c = hs.dial(t)
		artSelect(t, c, g)
		_, block = artRawResponse(t, c, "ARTICLE 1")
		wantArticle := append(append([]byte{}, wantHead...), CRLF...)
		wantArticle = append(wantArticle, artCRLF(artBodyLines(a)...)...)
		errs = append(errs, artExpectRaw("ARTICLE block", block, append(wantArticle, ".\r\n"...)))
		c.close()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	})
	// An empty body has zero lines: ARTICLE ends with the headers' blank line
	// followed directly by ".\r\n", and BODY is just ".\r\n" (RFC 3977 §3.1.1,
	// §6.2.1). Today an extra empty line is sent (BUG-6).
	t.Run("empty-body", func(t *testing.T) {
		g, arts := artSeed(t, "bytes-emptybody", 1, func(_ int, a *hArticle) { a.Body = "" })
		a := arts[0]
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		artSelect(t, c, g)
		var errs []error
		_, block := artRawResponse(t, c, "ARTICLE 1")
		want := append(artCRLF(artHeaderLines(a)...), "\r\n.\r\n"...)
		errs = append(errs, artExpectRaw("ARTICLE block", block, want))
		_, block = artRawResponse(t, c, "BODY 1")
		errs = append(errs, artExpectRaw("BODY block", block, []byte(".\r\n")))
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	})
	// HEAD carries header lines only; an empty line inside it would read as the
	// header/body separator (RFC 3977 §6.2.2, RFC 5322 §2.1). An empty
	// headers_json is sent as one empty line today (BUG-6).
	t.Run("empty-headers", func(t *testing.T) {
		g, _ := artSeed(t, "bytes-emptyhdr", 1, nil)
		artSetHeadersJSON(t, g, 1, "")
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		artSelect(t, c, g)
		_, block := artRawResponse(t, c, "HEAD 1")
		var err error
		for i, l := range bytes.SplitAfter(block, []byte(CRLF)) {
			if bytes.Equal(l, []byte(CRLF)) {
				err = fmt.Errorf("HEAD block has an empty line (line %d): %q", i, block)
				break
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// local430

// artLocal430Env is one local430 scenario: a group the article arrives in
// later, the article, a fake processor whose findFn misses until arrived is
// set, the server and a reader connection with no group selected (so every
// lookup goes through local430 and the processor).
type artLocal430Env struct {
	g       string
	a       hArticle
	proc    *hFakeProcessor
	arrived *atomic.Bool
	hs      *hServer
	c       *hClient
}

// artLocal430Miss builds the scenario and checks the two misses: the first
// STAT asks the processor (430), the second is answered from local430 without
// a second processor call.
func artLocal430Miss(t *testing.T, test string) *artLocal430Env {
	t.Helper()
	g, _ := artSeed(t, test, 0, nil)
	a := hDefaultArticle(g, 1)
	a.Headers = strings.Join(artDefaultHeaders(g, a), "\n")

	// db is taken here: findFn runs on the server goroutine, where hTestDB's
	// t.Skipf must not be called.
	db := hTestDB(t)
	arrived := &atomic.Bool{}
	// IHAVE wants the article (335) and the transfer is accepted (235).
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	proc.findFn = func(messageID, currentGroup string) (*models.Article, error) {
		if !arrived.Load() {
			return nil, fmt.Errorf("artLocal430 %s: %w", messageID, ErrArticleNotFound)
		}
		gdb, err := db.GetGroupDB(g)
		if err != nil {
			return nil, err
		}
		defer gdb.Return()
		art, err := db.GetArticleByMessageID(gdb, messageID)
		if err != nil {
			return nil, fmt.Errorf("artLocal430 %s: %v: %w", messageID, err, ErrArticleNotFound)
		}
		return art, nil
	}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	c := hs.dial(t)

	// RFC 3977 §6.2.4.2: 430 no article with that message-id.
	code, line := c.cmd(t, "STAT %s", a.MessageID)
	if err := hExpectCode(code, line, 430); err != nil {
		t.Fatalf("first STAT: %v", err)
	}
	if n := proc.findCount(); n != 1 {
		t.Fatalf("first STAT: findCount %d, want 1", n)
	}
	// A definite miss is remembered: the second STAT is answered from local430.
	code, line = c.cmd(t, "STAT %s", a.MessageID)
	if err := hExpectCode(code, line, 430); err != nil {
		t.Fatalf("second STAT: %v", err)
	}
	if n := proc.findCount(); n != 1 {
		t.Fatalf("second STAT: findCount %d, want 1 (no second processor call)", n)
	}
	return &artLocal430Env{g: g, a: a, proc: proc, arrived: arrived, hs: hs, c: c}
}

// artPollStat223 polls STAT <mid> on c until it answers "223 0 <mid>" or 1 s
// has passed, and returns the result of the last check.
func artPollStat223(t *testing.T, c *hClient, a hArticle) error {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		code, line := c.cmd(t, "STAT %s", a.MessageID)
		// RFC 3977 §6.2.4.2: 223 n message-id; §6.2.1.2: no group selected → n is 0.
		err := artCheckStatus(code, line, 223, 0, a.MessageID)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestArtLocal430(t *testing.T) {
	// A 430 for an existing article is wrong (RFC 3977 §6.2.4.2), so a cached
	// miss must not outlive the article's arrival by more than a moment:
	// STAT → 223 within 1 s.

	// The article arrives through this server (IHAVE answered 235). The server
	// knows the message-id was accepted, but local430 is never invalidated: the
	// entry is only dropped by CronLocal430 after 60-75 s (BUG-8).
	t.Run("ihave-arrival", func(t *testing.T) {
		env := artLocal430Miss(t, "local430-ihave")

		// A peer with transfer permission delivers the article.
		user, pass := hUser(t, true, true)
		peer := env.hs.dial(t)
		code, line := peer.cmd(t, "AUTHINFO USER %s", user)
		if err := hExpectCode(code, line, 381); err != nil {
			t.Fatalf("AUTHINFO USER: %v", err)
		}
		code, line = peer.cmd(t, "AUTHINFO PASS %s", pass)
		if err := hExpectCode(code, line, 281); err != nil {
			t.Fatalf("AUTHINFO PASS: %v", err)
		}
		// RFC 3977 §6.3.2.2: IHAVE of a wanted article → 335, then 235 after transfer.
		code, line = peer.cmd(t, "IHAVE %s", env.a.MessageID)
		if err := hExpectCode(code, line, 335); err != nil {
			t.Fatalf("IHAVE: %v", err)
		}
		raw := string(artCRLF(artHeaderLines(env.a)...)) + CRLF + string(artCRLF(artBodyLines(env.a)...)) + ".\r\n"
		if err := peer.send(raw); err != nil {
			t.Fatalf("send article: %v", err)
		}
		code, line, err := peer.readStatus()
		if err != nil {
			t.Fatalf("IHAVE transfer reply: %v (%q)", err, line)
		}
		if err := hExpectCode(code, line, 235); err != nil {
			t.Fatalf("IHAVE transfer: %v", err)
		}
		// What the real processor does on CasePass: the article is stored and the
		// history finds it.
		hArticleSQL(t, env.g, env.a)
		env.arrived.Store(true)

		knownBug(t, "BUG-8", artPollStat223(t, env.c, env.a))
	})

	// T-nntp-article-1 (low): local430 has no coherence with intake outside
	// this process. When nntp-fetcher (another process) stores the article in
	// the shared DB and history, this server is never told, so a message-id that
	// missed once stays 430 for up to ~75 s after the fetcher stored it. Here the
	// article "arrives" by raw SQL and the processor finds it right away.
	t.Run("external-arrival", func(t *testing.T) {
		env := artLocal430Miss(t, "local430-external")
		hArticleSQL(t, env.g, env.a)
		env.arrived.Store(true)
		knownBug(t, "T-nntp-article-1", artPollStat223(t, env.c, env.a))
	})
}

// ---------------------------------------------------------------------------
// Delay

func TestArtNoDelay(t *testing.T) {
	// With the default ArticleCmdDelay (DefaultArticleCmdDelay = 200 ms) every
	// ARTICLE/HEAD/BODY/STAT sleeps before answering, so these 50 STATs would
	// take at least 10 s and one connection gets about 5 articles/s (BUG-3).
	// The harness sets ArticleCmdDelay=0; this checks that zero means no sleep.
	g, arts := artSeed(t, "nodelay", 1, nil)
	hs := newTestServer(t, hServerOpts{})
	if hs.S.ArticleCmdDelay != 0 {
		t.Fatalf("harness ArticleCmdDelay = %s, want 0", hs.S.ArticleCmdDelay)
	}
	c := hs.dial(t)
	artSelect(t, c, g)
	a1 := artFind(t, arts, 1)
	start := time.Now()
	for i := 0; i < 50; i++ {
		code, line := c.cmd(t, "STAT 1")
		// RFC 3977 §6.2.4.2: 223 n message-id.
		if err := artCheckStatus(code, line, 223, 1, a1.MessageID); err != nil {
			t.Fatalf("STAT %d: %v", i, err)
		}
	}
	if took := time.Since(start); took >= time.Second {
		t.Fatalf("50 STATs took %s with ArticleCmdDelay=0, want < 1s", took)
	}
}

// ---------------------------------------------------------------------------
// Hidden articles and 430 texts (plan nntp-server-hardening, slice nntp-read)

// artHide sets hide = 1 on article num of group (decision 2: never served over NNTP).
func artHide(t *testing.T, group string, num int64) {
	t.Helper()
	gdb, err := hTestDB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	if _, err := database.RetryableExec(gdb.DB, `UPDATE articles SET hide = 1 WHERE article_num = ?`, num); err != nil {
		t.Fatalf("hide article %d in %s: %v", num, group, err)
	}
}

func TestArtHidden(t *testing.T) {
	g, arts := artSeed(t, "hidden", 3, nil)
	artHide(t, g, 2)
	a2 := artFind(t, arts, 2)
	a3 := artFind(t, arts, 3)

	t.Run("number-423", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		artSelect(t, c, g)
		for _, cmd := range artCmds {
			code, line, _ := artDo(t, c, "%s 2", cmd.name)
			// A hidden article answers as if it did not exist: 423 (RFC 3977 §6.2.1.2).
			if err := hExpectCode(code, line, 423); err != nil {
				t.Errorf("%s 2: %v", cmd.name, err)
			}
		}
		// The visible neighbour is still served.
		if err := artCheckRetrieval(t, c, "ARTICLE", "3", 220, 3, a3); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("message-id-current-group-430", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		artSelect(t, c, g)
		for _, cmd := range artCmds {
			code, line, _ := artDo(t, c, "%s %s", cmd.name, a2.MessageID)
			if err := hExpectCode(code, line, 430); err != nil {
				t.Errorf("%s %s: %v", cmd.name, a2.MessageID, err)
			}
		}
	})
	t.Run("message-id-processor-430", func(t *testing.T) {
		gA, _ := artSeed(t, "hidden-a", 1, nil)
		// db is taken here: findFn runs on the server goroutine.
		db := hTestDB(t)
		proc := &hFakeProcessor{}
		proc.findFn = func(messageID, currentGroup string) (*models.Article, error) {
			gdb, err := db.GetGroupDB(g)
			if err != nil {
				return nil, err
			}
			defer gdb.Return()
			// GetArticleByMessageID does not filter hide: the processor finds the hidden copy.
			a, err := db.GetArticleByMessageID(gdb, messageID)
			if err != nil {
				return nil, fmt.Errorf("artHidden %s: %w", messageID, ErrArticleNotFound)
			}
			return a, nil
		}
		hs := newTestServer(t, hServerOpts{Proc: proc})
		for _, selected := range []bool{true, false} {
			c := hs.dial(t)
			if selected {
				artSelect(t, c, gA)
			}
			before := proc.findCount()
			code, line := c.cmd(t, "STAT %s", a2.MessageID)
			if err := hExpectCode(code, line, 430); err != nil {
				t.Errorf("STAT %s (group %s selected %v): %v", a2.MessageID, gA, selected, err)
			}
			if proc.findCount() <= before {
				t.Errorf("STAT %s (selected %v): processor lookup not called", a2.MessageID, selected)
			}
			// The visible article of the same group is found through the processor.
			if err := artCheckRetrieval(t, c, "STAT", a3.MessageID, 223, 0, a3); err != nil {
				t.Errorf("selected %v: %v", selected, err)
			}
		}
	})
}

func TestArtNotFoundTexts(t *testing.T) {
	g, _ := artSeed(t, "notfoundtexts", 1, nil)
	missing := hMessageID(g, 99)
	var lines []string

	// no processor: only the current group is searched
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	artSelect(t, c, g)
	for _, cmd := range artCmds {
		code, line, _ := artDo(t, c, "%s %s", cmd.name, missing)
		if err := hExpectCode(code, line, 430); err != nil {
			t.Fatalf("%s %s: %v", cmd.name, missing, err)
		}
		lines = append(lines, line)
	}

	// processor miss, then the same id answered from local430
	proc := &hFakeProcessor{findErr: fmt.Errorf("artNotFoundTexts: %w", ErrArticleNotFound)}
	hsP := newTestServer(t, hServerOpts{Proc: proc})
	cP := hsP.dial(t)
	for i := 0; i < 2; i++ {
		code, line := cP.cmd(t, "STAT %s", missing)
		if err := hExpectCode(code, line, 430); err != nil {
			t.Fatalf("STAT %s round %d: %v", missing, i, err)
		}
		lines = append(lines, line)
	}
	if n := proc.findCount(); n != 1 {
		t.Fatalf("findCount %d, want 1 (second miss from local430)", n)
	}

	for _, line := range lines {
		for _, bad := range []string{"Cache says no", "NotF0", "NotF1"} {
			if strings.Contains(line, bad) {
				t.Errorf("430 reply %q contains the debug text %q", line, bad)
			}
		}
	}
}
