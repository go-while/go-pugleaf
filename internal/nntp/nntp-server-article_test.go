package nntp

// Article retrieval tests for the NNTP server (plan nntp-audit-tests, slice
// test-nntp-article): ARTICLE/HEAD/BODY/STAT by number, by message-id in the
// current group and in another group, without an argument, with invalid or
// missing numbers, the current article pointer, byte-exact output (CRLF and
// dot-stuffing), the local430 negative cache and the per-command delay.
//
// Every check states the RFC-correct behavior (RFC 3977). Where the current
// code differs, the result goes through knownBug with the seed id (BUG-1,
// BUG-6, BUG-8, RFC-3). The harness servers run with ErrorDelay=0, so
// rateLimitOnError does not sleep on the error replies below.
// Identifiers use the art/TestArt prefixes (contract K2).

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

	proc := &hFakeProcessor{}
	proc.findFn = func(messageID, currentGroup string) (*models.Article, error) {
		db := hTestDB(t)
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
		// STAT n sets the current article (RFC 3977 §6.2.4.2).
		if err := artCheckRetrieval(t, c, "STAT", "1", 223, 1, a1); err != nil {
			t.Fatal(err)
		}
		// RFC 3977 §6.2.1-§6.2.4: without an argument the current article is used.
		var errs []error
		for _, cmd := range artCmds {
			code, line, lines := artDo(t, c, "%s", cmd.name)
			err := artCheckStatus(code, line, cmd.code, 1, a1.MessageID)
			if err == nil && cmd.name != "STAT" {
				err = hExpectLines(lines, artExpectedLines(cmd.name, a1))
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("bare %s: %w", cmd.name, err))
			}
		}
		knownBug(t, "RFC-3", errors.Join(errs...))
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
		knownBug(t, "RFC-3", errors.Join(errs...))
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
		knownBug(t, "RFC-3", errors.Join(errs...))
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
		{"0", "RFC-3"},
		{"-1", "RFC-3"},
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
	// article number to n, so a following bare STAT reports article n.
	// Today GetArticleByNum never sets DBArtNum, so the current article becomes
	// 0 (BUG-1); the bare STAT also hits RFC-3 (501) first.
	for _, cmd := range artCmds[:3] { // ARTICLE, HEAD, BODY
		t.Run("after-"+cmd.name, func(t *testing.T) {
			c := hs.dial(t)
			artSelect(t, c, g)
			if err := artCheckRetrieval(t, c, cmd.name, "2", cmd.code, 2, a2); err != nil {
				t.Fatal(err)
			}
			code, line, _ := artDo(t, c, "STAT")
			knownBug(t, "BUG-1", artCheckStatus(code, line, 223, 2, a2.MessageID))
		})
	}
	t.Run("after-STAT", func(t *testing.T) {
		c := hs.dial(t)
		artSelect(t, c, g)
		// RFC 3977 §6.2.4.2: STAT n sets the current article as well. The STAT
		// path does set it today; only the bare form fails (RFC-3).
		if err := artCheckRetrieval(t, c, "STAT", "2", 223, 2, a2); err != nil {
			t.Fatal(err)
		}
		code, line, _ := artDo(t, c, "STAT")
		knownBug(t, "RFC-3", artCheckStatus(code, line, 223, 2, a2.MessageID))
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
		code, line, _ := artDo(t, c, "STAT")
		// The code keeps the pointer (isMessageID check), but the bare STAT that
		// would show it answers 501 (RFC-3).
		knownBug(t, "RFC-3", artCheckStatus(code, line, 223, 2, a2.MessageID))
	})
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
			// Stored as lax intake keeps it: a folded line that starts with ".".
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
		knownBug(t, "BUG-6", errors.Join(errs...))
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
		knownBug(t, "BUG-6", errors.Join(errs...))
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
		knownBug(t, "BUG-6", errors.Join(errs...))
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
		knownBug(t, "BUG-6", err)
	})
}

// ---------------------------------------------------------------------------
// local430

func TestArtLocal430(t *testing.T) {
	// The group the article arrives in later; no group is selected by the
	// client, so every lookup goes through local430 and the processor.
	g, _ := artSeed(t, "local430", 0, nil)
	a := hDefaultArticle(g, 1)
	a.Headers = strings.Join(artDefaultHeaders(g, a), "\n")

	var arrived atomic.Bool
	proc := &hFakeProcessor{}
	proc.findFn = func(messageID, currentGroup string) (*models.Article, error) {
		if !arrived.Load() {
			return nil, fmt.Errorf("artLocal430 %s: %w", messageID, ErrArticleNotFound)
		}
		db := hTestDB(t)
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
	n1 := proc.findCount()
	if n1 != 1 {
		t.Fatalf("first STAT: findCount %d, want 1", n1)
	}
	// A definite miss is remembered: the second STAT is answered from local430.
	code, line = c.cmd(t, "STAT %s", a.MessageID)
	if err := hExpectCode(code, line, 430); err != nil {
		t.Fatalf("second STAT: %v", err)
	}
	if n := proc.findCount(); n != n1 {
		t.Fatalf("second STAT: findCount %d, want %d (no second processor call)", n, n1)
	}

	// The article arrives and the processor (history) now finds it. A 430 for an
	// existing article is wrong (RFC 3977 §6.2.4.2), so the cached miss must not
	// outlive the arrival by more than a moment: STAT → 223 within 1 s. Today the
	// entry is only dropped by CronLocal430 after 60-75 s (BUG-8).
	hArticleSQL(t, g, a)
	arrived.Store(true)
	var err error
	deadline := time.Now().Add(time.Second)
	for {
		code, line = c.cmd(t, "STAT %s", a.MessageID)
		// RFC 3977 §6.2.1.2: no group selected → number 0.
		err = artCheckStatus(code, line, 223, 0, a.MessageID)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	knownBug(t, "BUG-8", err)
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
