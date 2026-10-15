package nntp

// Tests for OVER, HDR, LIST OVERVIEW.FMT and LIST HEADERS (plan nntp-rfc-commands, slice
// rfc-overview, RFC-1g..j). Until wave 2 wires the dispatch, the commands are routed to the
// new handlers through ovhSession (contract K3). Identifiers start with ovh/TestOvh.

import (
	"bufio"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ovhSession serves one connection of hs.S: commands in extra go to the new
// handlers, everything else to the hardened dispatch (handleCommand).
func ovhSession(t *testing.T, hs *hServer, extra map[string]func(cc *ClientConnection, args []string) error) *hClient {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		_ = l.Close()
		if err != nil {
			return
		}
		defer conn.Close()
		cc := NewClientConnection(conn, hs.S, false)
		cc.UpdateDeadlines()
		if cc.sendWelcome() != nil {
			return
		}
		for {
			line, err := cc.textConn.ReadLine() // re-read the field: STARTTLS replaces it
			if err != nil {
				return
			}
			cc.UpdateDeadlines()
			if f := strings.Fields(line); len(f) > 0 {
				if h, ok := extra[strings.ToUpper(f[0])]; ok {
					if !cc.argCountOK(strings.ToUpper(f[0]), f[1:]) { // what the real dispatch does first
						cc.rateLimitOnError()
						_ = cc.sendResponse(501, "Syntax error: too many arguments")
						continue
					}
					if h(cc, f[1:]) != nil { // errQuit included
						return
					}
					continue
				}
			}
			if cc.handleCommand(line) != nil {
				return
			}
		}
	}()
	conn, err := net.DialTimeout("tcp", l.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c := &hClient{Conn: conn, R: bufio.NewReaderSize(conn, 1<<20), Deadline: 5 * time.Second}
	t.Cleanup(func() { _ = conn.Close(); <-done })
	if c.Greeting, err = c.readLine(); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	return c
}

// ovhExtra routes OVER, HDR and LIST OVERVIEW.FMT/HEADERS to the new handlers (the
// wave-2 dispatch in miniature); every other LIST goes to the hardened handleList.
var ovhExtra = map[string]func(cc *ClientConnection, args []string) error{
	"OVER": func(cc *ClientConnection, args []string) error { return cc.handleOver(args) },
	"HDR":  func(cc *ClientConnection, args []string) error { return cc.handleHdr(args) },
	"LIST": func(cc *ClientConnection, args []string) error {
		if len(args) > 0 {
			switch strings.ToUpper(args[0]) {
			case "OVERVIEW.FMT":
				return cc.handleListOverviewFmt(args[1:])
			case "HEADERS":
				return cc.handleListHeaders(args[1:])
			}
		}
		return cc.handleList(args)
	},
}

// ovhDial opens an ovhSession with ovhExtra.
func ovhDial(t *testing.T, hs *hServer) *hClient {
	t.Helper()
	return ovhSession(t, hs, ovhExtra)
}

// ovhSelect sends GROUP g and expects 211.
func ovhSelect(t *testing.T, c *hClient, g string) {
	t.Helper()
	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
}

// ovhExpect sends a command and fails unless the reply code is want.
func ovhExpect(t *testing.T, c *hClient, want int, format string, args ...interface{}) string {
	t.Helper()
	code, line := c.cmd(t, format, args...)
	if err := hExpectCode(code, line, want); err != nil {
		t.Fatalf("%s: %v", fmt.Sprintf(format, args...), err)
	}
	return line
}

// ovhBlock sends a command, expects the multi-line status want and returns the block.
func ovhBlock(t *testing.T, c *hClient, want int, format string, args ...interface{}) []string {
	t.Helper()
	ovhExpect(t, c, want, format, args...)
	return c.readDotLines(t)
}

// ovhOverNums checks that every line is an 8-field overview line and returns field 1 of each.
func ovhOverNums(t *testing.T, lines []string) []int64 {
	t.Helper()
	nums := make([]int64, 0, len(lines))
	for i, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) != 8 {
			t.Fatalf("overview line %d has %d fields, want 8: %q", i, len(f), l)
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			t.Fatalf("overview line %d: field 1 %q is not a number", i, f[0])
		}
		nums = append(nums, n)
	}
	return nums
}

// ovhExpectNums compares article numbers.
func ovhExpectNums(t *testing.T, what string, got []int64, want ...int64) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: numbers %v, want %v", what, got, want)
	}
}

// ovhFindIn returns a fake processor whose history lookup finds every message-id in want
// and reports it in group g (visibleInOtherGroup re-reads it there); others miss.
func ovhFindIn(g string, want ...string) *hFakeProcessor {
	proc := &hFakeProcessor{}
	proc.findFn = func(messageID, currentGroup string) (*models.Article, error) {
		for _, id := range want {
			if id == messageID {
				group := g
				return &models.Article{MessageID: messageID, NewsgroupsPtr: []*string{&group}}, nil
			}
		}
		return nil, fmt.Errorf("ovh %s: %w", messageID, ErrArticleNotFound)
	}
	return proc
}

// ovhBulkGroup creates group <prefix>.<seq> with articles 1..n (the harness defaults)
// inserted in one transaction, then sets hide = 1 on the numbers in hidden.
func ovhBulkGroup(t *testing.T, prefix string, n int, hidden ...int64) string {
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
		for _, h := range hidden {
			if _, err := tx.Exec(`UPDATE articles SET hide = 1 WHERE article_num = ?`, h); err != nil {
				return fmt.Errorf("hide %d: %w", h, err)
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
// OVER

func TestOvhOver(t *testing.T) {
	// Articles 1, 3, 4, 5 (2 is a deleted number).
	g := hGroup(t, "rc3.over", 5, func(i int, a *hArticle) { a.Skip = i == 2 })
	empty := hGroup(t, "rc3.over-empty", 0, nil)
	hs := newTestServer(t, hServerOpts{})
	c := ovhDial(t, hs)

	// RFC 3977 §8.3.2: 412 without a selected group, for the range and the current form.
	ovhExpect(t, c, 412, "OVER 1-5")
	ovhExpect(t, c, 412, "OVER")

	// An empty group leaves the current article invalid: 420.
	ovhSelect(t, c, empty)
	ovhExpect(t, c, 420, "OVER")
	ovhExpect(t, c, 423, "OVER 1-5")

	ovhSelect(t, c, g)
	ovhExpectNums(t, "OVER (current)", ovhOverNums(t, ovhBlock(t, c, 224, "OVER")), 1)
	lines := ovhBlock(t, c, 224, "OVER 1-5")
	ovhExpectNums(t, "OVER 1-5", ovhOverNums(t, lines), 1, 3, 4, 5)
	if want := "Test article 3"; strings.Split(lines[1], "\t")[1] != want {
		t.Fatalf("OVER 1-5 line 2 subject: %q, want %q", lines[1], want)
	}
	ovhExpectNums(t, "OVER 3-", ovhOverNums(t, ovhBlock(t, c, 224, "OVER 3-")), 3, 4, 5)
	ovhExpectNums(t, "OVER 4", ovhOverNums(t, ovhBlock(t, c, 224, "OVER 4")), 4)

	ovhExpect(t, c, 423, "OVER 2")
	ovhExpect(t, c, 423, "OVER 5-3")
	ovhExpect(t, c, 423, "OVER 50-60")
	ovhExpect(t, c, 501, "OVER x")
	ovhExpect(t, c, 501, "OVER 1-x")
	ovhExpect(t, c, 501, "OVER 1 2") // the handler's own count check (no table row yet)

	// The session is still usable after all of the above.
	ovhExpectNums(t, "OVER 5", ovhOverNums(t, ovhBlock(t, c, 224, "OVER 5")), 5)
}

func TestOvhOverMsgID(t *testing.T) {
	g := hGroup(t, "rc3.overmsgid", 5, nil)
	id3 := hMessageID(g, 3)
	proc := ovhFindIn(g, id3)
	hs := newTestServer(t, hServerOpts{Proc: proc})

	t.Run("current-group", func(t *testing.T) {
		c := ovhDial(t, hs)
		ovhSelect(t, c, g)
		lines := ovhBlock(t, c, 224, "OVER %s", id3)
		ovhExpectNums(t, "OVER <id of 3>", ovhOverNums(t, lines), 3)
		if f := strings.Split(lines[0], "\t"); f[1] != "Test article 3" || f[4] != id3 {
			t.Fatalf("OVER <id of 3>: %q", lines[0])
		}
		// The message-id form never changes the current article (still 1 after GROUP).
		ovhExpectNums(t, "OVER (current)", ovhOverNums(t, ovhBlock(t, c, 224, "OVER")), 1)
	})

	t.Run("other-group", func(t *testing.T) {
		c := ovhDial(t, hs)
		// No group selected: found through the history lookup, number 0 (§8.3.2).
		lines := ovhBlock(t, c, 224, "OVER %s", id3)
		ovhExpectNums(t, "OVER <id of 3> without a group", ovhOverNums(t, lines), 0)
		if f := strings.Split(lines[0], "\t"); f[4] != id3 {
			t.Fatalf("OVER <id of 3> without a group: %q", lines[0])
		}
	})

	t.Run("unknown", func(t *testing.T) {
		c := ovhDial(t, hs)
		ovhExpect(t, c, 430, "OVER <ovh-unknown-%d@test.invalid>", hSeq.Add(1))
		ovhSelect(t, c, g)
		ovhExpect(t, c, 430, "OVER <ovh-unknown-%d@test.invalid>", hSeq.Add(1))
	})
}

// ---------------------------------------------------------------------------
// HDR

func TestOvhHdr(t *testing.T) {
	g := hGroup(t, "rc3.hdr", 5, func(i int, a *hArticle) { a.Skip = i == 2 })
	empty := hGroup(t, "rc3.hdr-empty", 0, nil)
	id4 := hMessageID(g, 4)
	hs := newTestServer(t, hServerOpts{Proc: ovhFindIn(g, id4)})
	c := ovhDial(t, hs)

	ovhExpect(t, c, 501, "HDR")
	ovhExpect(t, c, 501, "HDR Subject 1 2")
	ovhExpect(t, c, 412, "HDR Subject 1-5")
	ovhExpect(t, c, 503, "HDR :foo 3") // metadata check comes first

	// No group: the message-id form goes through the history lookup (number 0).
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR Subject %s", id4), []string{"0 Test article 4"}); err != nil {
		t.Fatal(err)
	}

	ovhSelect(t, c, empty)
	ovhExpect(t, c, 420, "HDR Subject")

	ovhSelect(t, c, g)
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR Subject 1-5"),
		[]string{"1 Test article 1", "3 Test article 3", "4 Test article 4", "5 Test article 5"}); err != nil {
		t.Fatal(err)
	}
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR subject"), []string{"1 Test article 1"}); err != nil {
		t.Fatal(err)
	}
	// Default body "body of article 3\nline two": 2 lines, 26 bytes.
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR :lines 3"), []string{"3 2"}); err != nil {
		t.Fatal(err)
	}
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR :BYTES 3-3"), []string{"3 26"}); err != nil {
		t.Fatal(err)
	}
	ovhExpect(t, c, 503, "HDR :foo 3")
	ovhExpect(t, c, 503, "HDR :foo")
	ovhExpect(t, c, 423, "HDR Subject 50-60")
	ovhExpect(t, c, 423, "HDR Subject 2")
	ovhExpect(t, c, 501, "HDR Subject x")
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR Subject %s", id4), []string{"4 Test article 4"}); err != nil {
		t.Fatal(err)
	}
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR :lines %s", id4), []string{"4 2"}); err != nil {
		t.Fatal(err)
	}
	ovhExpect(t, c, 430, "HDR Subject <ovh-unknown-%d@test.invalid>", hSeq.Add(1))
}

func TestOvhHdrAnyHeader(t *testing.T) {
	g := hGroup(t, "rc3.hdrany", 2, func(i int, a *hArticle) {
		if i == 1 {
			a.Headers = strings.Join([]string{
				"Path: " + a.Path,
				"From: " + a.From,
				"Subject: " + a.Subject,
				"Date: " + a.DateString,
				"Message-ID: " + a.MessageID,
				"X-Ovh-Test: v1",
				"Xref: upstream.invalid rc3.hdrany:77",
			}, "\n")
		}
	})
	id1, id2 := hMessageID(g, 1), hMessageID(g, 2)
	hs := newTestServer(t, hServerOpts{})
	c := ovhDial(t, hs)
	ovhSelect(t, c, g)

	if err := hExpectLines(ovhBlock(t, c, 225, "HDR X-Ovh-Test 1"), []string{"1 v1"}); err != nil {
		t.Fatal(err)
	}
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR x-ovh-test %s", id1), []string{"1 v1"}); err != nil {
		t.Fatal(err)
	}
	// Absent header: "n" (or "n ", both allowed by RFC 3977 §8.5.2).
	absent := func(lines []string, want string) {
		t.Helper()
		if len(lines) != 1 || strings.TrimRight(lines[0], " ") != want {
			t.Fatalf("absent header: %q, want [%q]", lines, want)
		}
	}
	absent(ovhBlock(t, c, 225, "HDR X-Ovh-Test 2"), "2")
	absent(ovhBlock(t, c, 225, "HDR X-Ovh-Test %s", id2), "2")
	// The upstream Xref line in headers_json is never served, in either form (as XHDR).
	absent(ovhBlock(t, c, 225, "HDR Xref 1"), "1")
	absent(ovhBlock(t, c, 225, "HDR Xref %s", id1), "1")
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR X-Ovh-Test 1-2"), []string{"1 v1", "2"}); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// LIST OVERVIEW.FMT / LIST HEADERS

func TestOvhOverviewFmt(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := ovhDial(t, hs)
	want := []string{"Subject:", "From:", "Date:", "Message-ID:", "References:", ":bytes", ":lines"}
	if err := hExpectLines(ovhBlock(t, c, 215, "LIST OVERVIEW.FMT"), want); err != nil {
		t.Fatal(err)
	}
	if err := hExpectLines(ovhBlock(t, c, 215, "list overview.fmt"), want); err != nil {
		t.Fatal(err)
	}
	ovhExpect(t, c, 501, "LIST OVERVIEW.FMT x")
	// Other keywords still reach the hardened handleList.
	ovhBlock(t, c, 215, "LIST ACTIVE")
}

func TestOvhListHeaders(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := ovhDial(t, hs)
	set := func(lines []string) string {
		m := map[string]bool{}
		for _, l := range lines {
			m[l] = true
		}
		if len(m) != len(lines) || len(m) != 3 || !m[":"] || !m[":bytes"] || !m[":lines"] {
			return fmt.Sprintf("bad set %q", lines)
		}
		return ""
	}
	for _, cmd := range []string{"LIST HEADERS", "LIST HEADERS MSGID", "LIST HEADERS RANGE", "LIST headers msgid"} {
		if msg := set(ovhBlock(t, c, 215, "%s", cmd)); msg != "" {
			t.Fatalf("%s: %s", cmd, msg)
		}
	}
	ovhExpect(t, c, 501, "LIST HEADERS FOO")
	ovhExpect(t, c, 501, "LIST HEADERS MSGID RANGE") // too many words (LIST max 2)
}

// ---------------------------------------------------------------------------
// Large ranges: chunked reads

func TestOvhLargeRange(t *testing.T) {
	const n = 2500
	g := ovhBulkGroup(t, "rc3.large", n, 500, 1500)
	hs := newTestServer(t, hServerOpts{})
	c := ovhDial(t, hs)
	ovhSelect(t, c, g)

	check := func(what string, nums []int64) {
		t.Helper()
		if len(nums) != n-2 {
			t.Fatalf("%s: %d lines, want %d", what, len(nums), n-2)
		}
		prev := int64(0)
		for _, x := range nums {
			if x <= prev || x == 500 || x == 1500 {
				t.Fatalf("%s: number %d after %d (out of order or hidden)", what, x, prev)
			}
			prev = x
		}
		if prev != n {
			t.Fatalf("%s: last number %d, want %d", what, prev, n)
		}
	}

	start := time.Now()
	check("OVER 1-", ovhOverNums(t, ovhBlock(t, c, 224, "OVER 1-")))
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("OVER 1- took %s", d)
	}

	start = time.Now()
	lines := ovhBlock(t, c, 225, "HDR Subject 1-")
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("HDR Subject 1- took %s", d)
	}
	nums := make([]int64, 0, len(lines))
	for i, l := range lines {
		num, subj, _ := strings.Cut(l, " ")
		x, err := strconv.ParseInt(num, 10, 64)
		if err != nil || subj != fmt.Sprintf("Test article %d", x) {
			t.Fatalf("HDR line %d: %q", i, l)
		}
		nums = append(nums, x)
	}
	check("HDR Subject 1-", nums)

	// An open-ended huge range stops at the last row (no walk over unused numbers).
	start = time.Now()
	ovhExpectNums(t, "OVER 2499-max", ovhOverNums(t, ovhBlock(t, c, 224, "OVER 2499-9223372036854775807")), 2499, 2500)
	if err := hExpectLines(ovhBlock(t, c, 225, "HDR Subject 2500-"), []string{"2500 Test article 2500"}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("open ranges took %s", d)
	}
	ovhExpect(t, c, 423, "OVER 2501-9223372036854775807")
}
