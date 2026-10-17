package nntp

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// Tests for slice rfc-lists of plan nntp-rfc-commands (prefix lst): LIST ACTIVE /
// LIST NEWSGROUPS with a wildmat and NEWGROUPS, through lstSession (the real
// dispatch is wired in wave 2).

// lstSession serves one connection of hs.S: commands in extra go to the new
// handlers, everything else to the hardened dispatch (handleCommand).
func lstSession(t *testing.T, hs *hServer, extra map[string]func(cc *ClientConnection, args []string) error) *hClient {
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

// lstExtra routes NEWGROUPS to handleNewGroups and LIST ACTIVE|NEWSGROUPS <wildmat>
// to handleListWildmat; every other LIST form goes to the hardened handleList.
func lstExtra() map[string]func(cc *ClientConnection, args []string) error {
	return map[string]func(cc *ClientConnection, args []string) error{
		"NEWGROUPS": func(cc *ClientConnection, args []string) error { return cc.handleNewGroups(args) },
		"LIST": func(cc *ClientConnection, args []string) error {
			if len(args) == 2 {
				if kw := strings.ToUpper(args[0]); kw == "ACTIVE" || kw == "NEWSGROUPS" {
					return cc.handleListWildmat(kw, args[1:])
				}
			}
			return cc.handleList(args)
		},
	}
}

// lstCmd sends a command and returns the status code, the status line and, when the
// code is want (a multi-line reply), the data block.
func lstCmd(t *testing.T, c *hClient, want int, format string, args ...interface{}) (int, string, []string) {
	t.Helper()
	code, line := c.cmd(t, format, args...)
	if code != want {
		return code, line, nil
	}
	return code, line, c.readDotLines(t)
}

// lstMustList runs a command that must answer want with a data block.
func lstMustList(t *testing.T, c *hClient, want int, format string, args ...interface{}) []string {
	t.Helper()
	code, line, lines := lstCmd(t, c, want, format, args...)
	if code != want {
		t.Fatalf("%s: got %q, want %d", fmt.Sprintf(format, args...), line, want)
	}
	return lines
}

// lstLineOf returns the line of group name in lines ("" when absent).
func lstLineOf(lines []string, name string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, name+" ") {
			return l
		}
	}
	return ""
}

// lstNames returns the first word of every line.
func lstNames(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// TestLstListWildmat: RFC 3977 §7.6.3/§7.6.6 with §4 wildmats: only matching groups
// are listed, "!" excludes, an invalid wildmat is 501 (delayed); 215 is not delayed.
func TestLstListWildmat(t *testing.T) {
	tok := strings.ReplaceAll(hGroupName("tok"), ".", "")
	// hGroup appends ".<seq>", so the names are rc2.lw.<tok>.a.<n> etc.
	a := hGroup(t, "rc2.lw."+tok+".a", 0, nil)
	b := hGroup(t, "rc2.lw."+tok+".b", 0, nil)
	o := hGroup(t, "rc2.other."+tok, 0, nil)

	hs := newTestServer(t, hServerOpts{})
	c := lstSession(t, hs, lstExtra())

	lines := lstMustList(t, c, 215, "LIST ACTIVE rc2.lw.%s.*", tok)
	if err := hExpectLines(lstNames(lines), []string{a, b}); err != nil {
		t.Errorf("LIST ACTIVE rc2.lw.%s.*: %v", tok, err)
	}
	if lstLineOf(lines, o) != "" {
		t.Errorf("LIST ACTIVE rc2.lw.%s.* lists %s", tok, o)
	}

	lines = lstMustList(t, c, 215, "list active rc2.lw.%s.*", tok)
	if err := hExpectLines(lstNames(lines), []string{a, b}); err != nil {
		t.Errorf("lower-case keyword: %v", err)
	}

	lines = lstMustList(t, c, 215, "LIST NEWSGROUPS %s", a)
	if err := hExpectLines(lines, []string{a + " harness group " + a}); err != nil {
		t.Errorf("LIST NEWSGROUPS %s: %v", a, err)
	}

	lines = lstMustList(t, c, 215, "LIST ACTIVE rc2.lw.%s.*,!*.b.*", tok)
	if err := hExpectLines(lstNames(lines), []string{a}); err != nil {
		t.Errorf("LIST ACTIVE rc2.lw.%s.*,!*.b.*: %v", tok, err)
	}

	lines = lstMustList(t, c, 215, "LIST NEWSGROUPS rc2.*.%s*,!rc2.lw.*", tok)
	if err := hExpectLines(lstNames(lines), []string{o}); err != nil {
		t.Errorf("LIST NEWSGROUPS rc2.*.%s*,!rc2.lw.*: %v", tok, err)
	}

	lines = lstMustList(t, c, 215, "LIST ACTIVE nomatch.%s.*", tok)
	if len(lines) != 0 {
		t.Errorf("LIST ACTIVE nomatch.%s.*: got %q, want an empty list", tok, lines)
	}

	for _, bad := range []string{"LIST ACTIVE a,,b", "LIST NEWSGROUPS [ab]", "LIST ACTIVE a,", "LIST NEWSGROUPS !"} {
		code, line, _ := lstCmd(t, c, 215, "%s", bad)
		if err := hExpectCode(code, line, 501); err != nil {
			t.Errorf("%s: %v", bad, err)
		}
	}

	// The session still works after the errors.
	lines = lstMustList(t, c, 215, "LIST ACTIVE %s", b)
	if err := hExpectLines(lstNames(lines), []string{b}); err != nil {
		t.Errorf("LIST ACTIVE %s: %v", b, err)
	}

	// D10: 501 is delayed, 215 is not. Structural: the error-delay counter
	// (incremented before the reply is sent) does not move for the 215 and moves
	// once for the 501; the 501 keeps its lower bound against the configured delay.
	const delay = 300 * time.Millisecond
	hd := newTestServer(t, hServerOpts{ErrorDelay: delay})
	cd := lstSession(t, hd, lstExtra())
	delays := hd.S.ErrorDelayCount()
	lstMustList(t, cd, 215, "LIST ACTIVE rc2.lw.%s.*", tok)
	if n := hd.S.ErrorDelayCount() - delays; n != 0 {
		t.Errorf("LIST ACTIVE <wildmat> (215) reached the error delay %d times, want 0", n)
	}
	delays = hd.S.ErrorDelayCount()
	start := time.Now()
	code, line, _ := lstCmd(t, cd, 215, "LIST ACTIVE a,,b")
	if err := hExpectCode(code, line, 501); err != nil {
		t.Error(err)
	}
	if d := time.Since(start); d < delay {
		t.Errorf("LIST ACTIVE a,,b (501) took %s, want at least %s", d, delay)
	}
	if n := hd.S.ErrorDelayCount() - delays; n != 1 {
		t.Errorf("LIST ACTIVE a,,b (501) reached the error delay %d times, want 1", n)
	}
}

// TestLstMatchesList: a group's line in LIST ACTIVE/NEWSGROUPS <wildmat> equals its
// line in the plain list, and "*" gives exactly the plain list, with and without a
// processor (status y vs n, decision 14).
func TestLstMatchesList(t *testing.T) {
	g := hGroup(t, "rc2.ml", 3, nil)
	gt := hGroup(t, "rc2.mltab", 0, nil)
	if _, err := hTestDB(t).GetMainDB().Exec(`UPDATE newsgroups SET description = ? WHERE name = ?`,
		"tab\there\rcr\nlf", gt); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		opts   hServerOpts
		status string
	}{
		{"no-processor", hServerOpts{}, "n"},
		{"processor", hServerOpts{Proc: &hFakeProcessor{}}, "y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hs := newTestServer(t, tc.opts)
			c := lstSession(t, hs, lstExtra())

			plain := lstMustList(t, c, 215, "LIST ACTIVE")
			want := fmt.Sprintf("%s 3 1 %s", g, tc.status)
			if got := lstLineOf(plain, g); got != want {
				t.Errorf("plain LIST ACTIVE line of %s: %q, want %q", g, got, want)
			}
			one := lstMustList(t, c, 215, "LIST ACTIVE %s", g)
			if err := hExpectLines(one, []string{lstLineOf(plain, g)}); err != nil {
				t.Errorf("LIST ACTIVE %s: %v", g, err)
			}
			star := lstMustList(t, c, 215, "LIST ACTIVE *")
			if err := hExpectLines(star, plain); err != nil {
				t.Errorf("LIST ACTIVE * vs LIST ACTIVE: %v", err)
			}

			plainNG := lstMustList(t, c, 215, "LIST NEWSGROUPS")
			for _, name := range []string{g, gt} {
				one := lstMustList(t, c, 215, "LIST NEWSGROUPS %s", name)
				if err := hExpectLines(one, []string{lstLineOf(plainNG, name)}); err != nil {
					t.Errorf("LIST NEWSGROUPS %s: %v", name, err)
				}
			}
			if got, want := lstLineOf(plainNG, gt), gt+" tab here cr lf"; got != want {
				t.Errorf("LIST NEWSGROUPS line of %s: %q, want %q", gt, got, want)
			}
			starNG := lstMustList(t, c, 215, "LIST NEWSGROUPS *")
			if err := hExpectLines(starNG, plainNG); err != nil {
				t.Errorf("LIST NEWSGROUPS * vs LIST NEWSGROUPS: %v", err)
			}
		})
	}
}

// TestLstNewGroups: RFC 3977 §7.3, NEWGROUPS lists the groups created since the
// given time in the LIST ACTIVE format; future → empty 231; bad syntax/date → 501
// (delayed); 231 is not delayed.
func TestLstNewGroups(t *testing.T) {
	g := hGroup(t, "rc2.ng", 2, nil)
	since := time.Now().Add(-48 * time.Hour)
	sinceUTC := since.UTC()

	for _, tc := range []struct {
		name   string
		opts   hServerOpts
		status string
	}{
		{"no-processor", hServerOpts{}, "n"},
		{"processor", hServerOpts{Proc: &hFakeProcessor{}}, "y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hs := newTestServer(t, tc.opts)
			c := lstSession(t, hs, lstExtra())

			activeLn := lstLineOf(lstMustList(t, c, 215, "LIST ACTIVE"), g)
			want := fmt.Sprintf("%s 2 1 %s", g, tc.status)
			if activeLn != want {
				t.Fatalf("LIST ACTIVE line of %s: %q, want %q", g, activeLn, want)
			}

			for _, cmd := range []string{
				"NEWGROUPS " + sinceUTC.Format("20060102 150405") + " GMT",
				"NEWGROUPS " + sinceUTC.Format("20060102 150405") + " gmt",
				"NEWGROUPS " + sinceUTC.Format("060102 150405") + " GMT",
				"NEWGROUPS " + since.Local().Format("20060102 150405"), // local time without GMT
			} {
				lines := lstMustList(t, c, 231, "%s", cmd)
				if got := lstLineOf(lines, g); got != activeLn {
					t.Errorf("%s: line of %s %q, want the LIST ACTIVE line %q", cmd, g, got, activeLn)
				}
			}

			if lines := lstMustList(t, c, 231, "NEWGROUPS 20991231 235959 GMT"); len(lines) != 0 {
				t.Errorf("NEWGROUPS (future): got %q, want an empty list", lines)
			}

			for _, bad := range []string{
				"NEWGROUPS 20261301 000000 GMT",
				"NEWGROUPS 20260101 000000 UTC",
				"NEWGROUPS 20260101",
				"NEWGROUPS",
				"NEWGROUPS 20260101 000000 GMT x",
				"NEWGROUPS 2026010 000000 GMT",
				"NEWGROUPS 20260231 000000 GMT",
				"NEWGROUPS 20260101 240000 GMT",
			} {
				code, line, _ := lstCmd(t, c, 231, "%s", bad)
				if err := hExpectCode(code, line, 501); err != nil {
					t.Errorf("%s: %v", bad, err)
				}
			}
		})
	}

	// D10: 501 is delayed, 231 is not. Structural: the error-delay counter
	// (incremented before the reply is sent) does not move for the 231 and moves
	// once per 501; the 501s keep their lower bound against the configured delay.
	const delay = 300 * time.Millisecond
	hd := newTestServer(t, hServerOpts{ErrorDelay: delay})
	cd := lstSession(t, hd, lstExtra())
	delays := hd.S.ErrorDelayCount()
	lstMustList(t, cd, 231, "NEWGROUPS 20991231 235959 GMT")
	if n := hd.S.ErrorDelayCount() - delays; n != 0 {
		t.Errorf("NEWGROUPS (231) reached the error delay %d times, want 0", n)
	}
	for _, bad := range []string{"NEWGROUPS 20260101", "NEWGROUPS 20261301 000000 GMT"} {
		delays = hd.S.ErrorDelayCount()
		start := time.Now()
		code, line, _ := lstCmd(t, cd, 231, "%s", bad)
		if err := hExpectCode(code, line, 501); err != nil {
			t.Error(err)
		}
		if d := time.Since(start); d < delay {
			t.Errorf("%s (501) took %s, want at least %s", bad, d, delay)
		}
		if n := hd.S.ErrorDelayCount() - delays; n != 1 {
			t.Errorf("%s (501) reached the error delay %d times, want 1", bad, n)
		}
	}
}

// TestLstParseNNTPDateTime: RFC 3977 §7.3.2 date/time parsing incl. the century
// rule for 2-digit years, the leap second and invalid calendar dates.
func TestLstParseNNTPDateTime(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	utc := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}
	ok := []struct {
		date, clock string
		now         time.Time
		want        time.Time
	}{
		{"261011", "120000", now, utc(2026, 10, 11, 12, 0, 0)},
		{"270101", "000000", now, utc(1927, 1, 1, 0, 0, 0)},
		{"991231", "235959", now, utc(1999, 12, 31, 23, 59, 59)},
		{"000101", "000000", now, utc(2000, 1, 1, 0, 0, 0)},
		{"251231", "010203", now, utc(2025, 12, 31, 1, 2, 3)},
		{"991231", "000000", utc(2101, 1, 1, 0, 0, 0), utc(2099, 12, 31, 0, 0, 0)},
		{"000101", "000000", utc(2101, 1, 1, 0, 0, 0), utc(2100, 1, 1, 0, 0, 0)},
		{"20261011", "235960", now, utc(2026, 10, 11, 23, 59, 59)},
		{"20240229", "000000", now, utc(2024, 2, 29, 0, 0, 0)},
		{"19000101", "000000", now, utc(1900, 1, 1, 0, 0, 0)},
		{"99991231", "235959", now, utc(9999, 12, 31, 23, 59, 59)},
	}
	for _, tc := range ok {
		got, err := parseNNTPDateTime(tc.date, tc.clock, true, tc.now)
		if err != nil {
			t.Errorf("parseNNTPDateTime(%q, %q, GMT, %s): %v", tc.date, tc.clock, tc.now, err)
			continue
		}
		if !got.Equal(tc.want) || got.Location() != time.UTC {
			t.Errorf("parseNNTPDateTime(%q, %q, GMT, %s) = %s, want %s", tc.date, tc.clock, tc.now, got, tc.want)
		}
	}

	bad := []struct{ date, clock string }{
		{"20260230", "000000"}, {"20260231", "000000"}, {"20230229", "000000"}, {"260431", "000000"},
		{"20261301", "000000"}, {"20260001", "000000"}, {"20260100", "000000"}, {"20260132", "000000"},
		{"18991231", "235959"}, {"00000101", "000000"},
		{"20261011", "240000"}, {"20261011", "236000"}, {"20261011", "235961"},
		{"2026101", "000000"}, {"202610111", "000000"}, {"26101", "000000"}, {"", "000000"},
		{"2026-1-1", "000000"}, {"26101a", "000000"}, {"+26101", "000000"}, {"２０２６１０", "000000"},
		{"20261011", "12000"}, {"20261011", "1200000"}, {"20261011", "12000a"}, {"20261011", "-12000"}, {"20261011", ""},
	}
	for _, tc := range bad {
		if got, err := parseNNTPDateTime(tc.date, tc.clock, true, now); err == nil {
			t.Errorf("parseNNTPDateTime(%q, %q) = %s, want an error", tc.date, tc.clock, got)
		}
	}

	// Without GMT the time is in the server's local zone (§7.3.2).
	got, err := parseNNTPDateTime("20261011", "120000", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local); !got.Equal(want) || got.Location() != time.Local {
		t.Errorf("parseNNTPDateTime local = %s (%s), want %s (Local)", got, got.Location(), want)
	}
}
