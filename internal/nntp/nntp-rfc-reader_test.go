package nntp

// Tests for DATE, NEXT and LAST (plan nntp-rfc-commands, slice rfc-reader; RFC-1a/b/c).
// Until the wave-2 dispatch routes these words, the tests go through rdrSession.
// Identifiers use the rdr/TestRdr prefixes; groups are named rc1.<test>.

import (
	"bufio"
	"fmt"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

// rdrSession serves one connection of hs.S: commands in extra go to the new
// handlers, everything else to the hardened dispatch (handleCommand).
func rdrSession(t *testing.T, hs *hServer, extra map[string]func(cc *ClientConnection, args []string) error) *hClient {
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
	conn, err := net.DialTimeout("tcp", l.Addr().String(), hDeadline)
	if err != nil {
		t.Fatal(err)
	}
	c := &hClient{Conn: conn, R: bufio.NewReaderSize(conn, 1<<20), Deadline: hDeadline}
	t.Cleanup(func() { _ = conn.Close(); <-done })
	if c.Greeting, err = c.readLine(); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	return c
}

// rdrExtra routes DATE, NEXT and LAST to the handlers of this slice (never mutated).
var rdrExtra = map[string]func(cc *ClientConnection, args []string) error{
	"DATE": (*ClientConnection).handleDate,
	"NEXT": (*ClientConnection).handleNext,
	"LAST": (*ClientConnection).handleLast,
}

// rdrExpect sends cmd and checks the reply code; for 223 the line must start with
// "223 <num> <message-id of num in group>".
func rdrExpect(t *testing.T, c *hClient, group, cmd string, code int, num int64) {
	t.Helper()
	got, line := c.cmd(t, "%s", cmd)
	if got != code {
		t.Errorf("%s: got %q, want %d", cmd, line, code)
		return
	}
	if code == 223 {
		want := fmt.Sprintf("223 %d %s", num, hMessageID(group, num))
		if !strings.HasPrefix(line, want) {
			t.Errorf("%s: got %q, want prefix %q", cmd, line, want)
		}
	}
}

// rdrGroupSkip seeds rc1.<name> with articles 3, 4 and 6 (1, 2 and 5 deleted).
func rdrGroupSkip(t *testing.T, name string) string {
	t.Helper()
	return hGroup(t, "rc1."+name, 6, func(i int, a *hArticle) {
		if i <= 2 || i == 5 {
			a.Skip = true
		}
	})
}

// rdrSelect sends GROUP and requires 211.
func rdrSelect(t *testing.T, c *hClient, group string) {
	t.Helper()
	code, line := c.cmd(t, "GROUP %s", group)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
}

// TestRdrDate: RFC 3977 §7.1, "111 yyyymmddhhmmss" in UTC; arguments → 501.
func TestRdrDate(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := rdrSession(t, hs, rdrExtra)

	code, line := c.cmd(t, "DATE")
	if err := hExpectCode(code, line, 111); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^111 (\d{14})$`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("DATE: got %q, want \"111 yyyymmddhhmmss\"", line)
	}
	ts, err := time.Parse("20060102150405", m[1])
	if err != nil {
		t.Fatalf("DATE: %q: %v", line, err)
	}
	if d := time.Since(ts); d > 5*time.Minute || d < -5*time.Minute {
		t.Fatalf("DATE: %q is %s away from UTC now", line, d)
	}

	code, line = c.cmd(t, "DATE x")
	if err := hExpectCode(code, line, 501); err != nil {
		t.Fatal(err)
	}
}

// TestRdrNextLast: RFC 3977 §6.1.3/§6.1.4 replies and the current-article bookkeeping.
func TestRdrNextLast(t *testing.T) {
	g := rdrGroupSkip(t, "nextlast")
	empty := hGroup(t, "rc1.empty", 0, nil)
	hs := newTestServer(t, hServerOpts{})

	t.Run("no-group", func(t *testing.T) {
		c := rdrSession(t, hs, rdrExtra)
		rdrExpect(t, c, g, "NEXT", 412, 0)
		rdrExpect(t, c, g, "LAST", 412, 0)
	})

	t.Run("walk", func(t *testing.T) {
		c := rdrSession(t, hs, rdrExtra)
		rdrSelect(t, c, g)                 // current = 3 (low mark)
		rdrExpect(t, c, g, "LAST", 422, 0) // at the first article
		rdrExpect(t, c, g, "STAT", 223, 3) // 422 left it unchanged
		rdrExpect(t, c, g, "NEXT", 223, 4)
		rdrExpect(t, c, g, "NEXT", 223, 6) // skips the deleted 5
		rdrExpect(t, c, g, "NEXT", 421, 0) // at the last article
		rdrExpect(t, c, g, "STAT", 223, 6) // 421 left it unchanged
		rdrExpect(t, c, g, "LAST", 223, 4)
		rdrExpect(t, c, g, "STAT", 223, 4) // bare STAT uses the moved current article (K-H3)
		rdrExpect(t, c, g, "NEXT x", 501, 0)
		rdrExpect(t, c, g, "LAST x", 501, 0)
		rdrExpect(t, c, g, "STAT", 223, 4) // 501 left it unchanged
	})

	t.Run("empty-group", func(t *testing.T) {
		c := rdrSession(t, hs, rdrExtra)
		rdrSelect(t, c, empty)
		rdrExpect(t, c, empty, "NEXT", 420, 0)
		rdrExpect(t, c, empty, "LAST", 420, 0)
	})

	t.Run("article-msgid-keeps-current", func(t *testing.T) {
		c := rdrSession(t, hs, rdrExtra)
		rdrSelect(t, c, g)
		rdrExpect(t, c, g, "STAT 3", 223, 3)
		code, line := c.cmd(t, "ARTICLE %s", hMessageID(g, 6))
		if err := hExpectCode(code, line, 220); err != nil {
			t.Fatal(err)
		}
		_ = c.readDotLines(t)
		rdrExpect(t, c, g, "NEXT", 223, 4) // §6.2.1.2: the message-id form does not move it
	})
}

// TestRdrNextLastDelays: D10, 421/422 end a walk without the error delay; 412 has it.
// Structural: the error-delay counter (incremented before the reply is sent) moves
// once for the 412 and not at all for 422/223/421; the 412 keeps its lower bound.
func TestRdrNextLastDelays(t *testing.T) {
	const delay = 300 * time.Millisecond
	g := hGroup(t, "rc1.delays", 2, nil)
	hs := newTestServer(t, hServerOpts{ErrorDelay: delay})
	c := rdrSession(t, hs, rdrExtra)

	// run returns the reply time and how often the reply reached the error delay.
	run := func(cmd string, code int, num int64) (time.Duration, int64) {
		t.Helper()
		before := hs.S.ErrorDelayCount()
		start := time.Now()
		rdrExpect(t, c, g, cmd, code, num)
		return time.Since(start), hs.S.ErrorDelayCount() - before
	}

	d, n := run("NEXT", 412, 0)
	if d < delay {
		t.Errorf("NEXT without a group (412) answered after %s, want at least %s", d, delay)
	}
	if n != 1 {
		t.Errorf("NEXT without a group (412) reached the error delay %d times, want 1", n)
	}
	rdrSelect(t, c, g) // current = 1
	if _, n := run("LAST", 422, 0); n != 0 {
		t.Errorf("LAST at the first article (422) reached the error delay %d times, want 0", n)
	}
	if _, n := run("NEXT", 223, 2); n != 0 {
		t.Errorf("NEXT to article 2 (223) reached the error delay %d times, want 0", n)
	}
	if _, n := run("NEXT", 421, 0); n != 0 {
		t.Errorf("NEXT at the last article (421) reached the error delay %d times, want 0", n)
	}
}

// TestRdrNextLastHidden: K-H10, NEXT/LAST skip articles with hide > 0.
func TestRdrNextLastHidden(t *testing.T) {
	g := rdrGroupSkip(t, "hidden")
	gdb, err := hTestDB(t).GetGroupDB(g)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", g, err)
	}
	_, err = gdb.DB.Exec(`UPDATE articles SET hide = 1 WHERE article_num = 4`)
	gdb.Return()
	if err != nil {
		t.Fatalf("hide 4: %v", err)
	}

	hs := newTestServer(t, hServerOpts{})
	c := rdrSession(t, hs, rdrExtra)
	rdrSelect(t, c, g)                 // current = 3
	rdrExpect(t, c, g, "NEXT", 223, 6) // skips the hidden 4
	rdrExpect(t, c, g, "LAST", 223, 3) // and back
}
