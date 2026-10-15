//go:build e2e

package main

// TestE2EBinaryRFC runs the checks N50–N57 of plan nntp-rfc-commands (`## End-to-end`)
// against an externally started server, normally the one scripts/test-nntp-server.sh
// builds, seeds and starts after TestE2EBinary (N01–N11) has stored two articles in
// PUGLEAF_E2E_GROUP (N04 POST, N10 TAKETHIS):
//
//	PUGLEAF_E2E_ADDR=127.0.0.1:21119 PUGLEAF_E2E_USER=e2euser0001 PUGLEAF_E2E_PASS=e2epass0001 \
//	  PUGLEAF_E2E_GROUP=e2e.test.a PUGLEAF_E2E_STARTTLS=1 \
//	  go test -tags e2e -count=1 -v -run '^TestE2EBinaryRFC$' ./cmd/nntp-server/
//
// It prints one `PASS|FAIL N5x [rfc] <text>` line per check (the script counts
// exactly 8) plus `INFO N5x [rfc] <text>` notes, and fails the test on any FAIL.
// PUGLEAF_E2E_STARTTLS=1 means the server runs with -nntpcertfile/-nntpkeyfile, so
// STARTTLS must work (N57); otherwise STARTTLS must be refused with 502. The
// groups e2e.test.a and e2e.test.b must exist (N56).

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// e2eRfcReport prints the step lines with the [rfc] tag and fails t on FAIL.
type e2eRfcReport struct{ t *testing.T }

func (r e2eRfcReport) pass(id, text string) { fmt.Printf("PASS %s [rfc] %s\n", id, text) }

func (r e2eRfcReport) info(id, text string) { fmt.Printf("INFO %s [rfc] %s\n", id, text) }

func (r e2eRfcReport) fail(id, text, detail string) {
	fmt.Printf("FAIL %s [rfc] %s :: %s\n", id, text, detail)
	r.t.Errorf("%s %s: %s", id, text, detail)
}

func (r e2eRfcReport) check(id, text string, err error) bool {
	if err != nil {
		r.fail(id, text, err.Error())
		return false
	}
	r.pass(id, text)
	return true
}

// e2eRfcStatus returns nil when code is want and the I/O succeeded.
func e2eRfcStatus(code int, line string, err error, want int) error {
	if err != nil {
		return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
	}
	if code != want {
		return fmt.Errorf("got %q, want %d", line, want)
	}
	return nil
}

// e2eRfcBlock sends cmd, requires the status want and returns the data block.
func e2eRfcBlock(c *e2eClient, want int, cmd string) ([]string, error) {
	code, line, err := c.tryCmd("%s", cmd)
	if e := e2eRfcStatus(code, line, err, want); e != nil {
		return nil, fmt.Errorf("%s: %v", cmd, e)
	}
	lines, err := c.tryDotLines()
	if err != nil {
		return lines, fmt.Errorf("%s: %v", cmd, err)
	}
	return lines, nil
}

// e2eRfcCaps runs CAPABILITIES and returns the capability lines.
func e2eRfcCaps(c *e2eClient) ([]string, error) {
	return e2eRfcBlock(c, 101, "CAPABILITIES")
}

// e2eRfcHasLabel reports whether a capability line has label as its first word.
func e2eRfcHasLabel(caps []string, label string) bool {
	for _, l := range caps {
		if f := strings.Fields(l); len(f) > 0 && strings.EqualFold(f[0], label) {
			return true
		}
	}
	return false
}

// e2eRfcUpgrade runs the client side of the TLS handshake after a 382 and
// switches c to the TLS connection.
func e2eRfcUpgrade(c *e2eClient) error {
	if n := c.R.Buffered(); n != 0 {
		return fmt.Errorf("%d unread bytes before the handshake", n)
	}
	tc := tls.Client(c.Conn, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // test client, self-signed scratch certificate
	_ = tc.SetDeadline(time.Now().Add(c.Deadline))
	if err := tc.Handshake(); err != nil {
		return fmt.Errorf("TLS handshake: %w", err)
	}
	c.Conn = tc
	c.R = bufio.NewReaderSize(tc, 1<<16)
	return nil
}

func TestE2EBinaryRFC(t *testing.T) {
	addr := os.Getenv("PUGLEAF_E2E_ADDR")
	if addr == "" {
		t.Skip("PUGLEAF_E2E_ADDR not set (run scripts/test-nntp-server.sh)")
	}
	user := e2eEnv("PUGLEAF_E2E_USER", "e2euser0001")
	pass := e2eEnv("PUGLEAF_E2E_PASS", "e2epass0001")
	group := e2eEnv("PUGLEAF_E2E_GROUP", "e2e.test.a")
	withTLS := os.Getenv("PUGLEAF_E2E_STARTTLS") == "1"
	r := e2eRfcReport{t: t}
	freshID := fmt.Sprintf("<e2e-n55-%d@test.invalid>", time.Now().UnixNano())

	c, err := e2eDialAddr(addr, testWait)
	if err != nil {
		r.fail("N50", "connect", err.Error())
		t.FailNow()
	}
	defer c.close()

	// N50: DATE → 111 yyyymmddhhmmss within 5 minutes of UTC now (RFC 3977 §7.1)
	r.check("N50", "DATE 111 within 5 min of UTC", func() error {
		code, line, err := c.tryCmd("DATE")
		if e := e2eRfcStatus(code, line, err, 111); e != nil {
			return e
		}
		f := strings.Fields(line)
		if len(f) != 2 || len(f[1]) != 14 {
			return fmt.Errorf("got %q, want 111 yyyymmddhhmmss", line)
		}
		ts, err := time.Parse("20060102150405", f[1])
		if err != nil {
			return fmt.Errorf("got %q: %v", line, err)
		}
		if d := time.Since(ts); d > 5*time.Minute || d < -5*time.Minute {
			return fmt.Errorf("DATE %s is %s away from UTC now", f[1], d.Round(time.Second))
		}
		return nil
	}())

	// N51: NEXT/LAST walk the group (RFC 3977 §6.1.3, §6.1.4)
	r.check("N51", "NEXT/LAST walk "+group, func() error {
		// N10's TAKETHIS commits asynchronously: wait up to testWait for count >= 2
		var count, low, high int64
		var last string
		perr := e2eWaitFor(testWait, func() error {
			code, line, err := c.tryCmd("GROUP %s", group)
			if err != nil {
				return err
			}
			last = line
			f := strings.Fields(line)
			if code != 211 || len(f) < 5 {
				return fmt.Errorf("GROUP %s: %q", group, line)
			}
			count, _ = strconv.ParseInt(f[1], 10, 64)
			low, _ = strconv.ParseInt(f[2], 10, 64)
			high, _ = strconv.ParseInt(f[3], 10, 64)
			if count < 2 {
				return fmt.Errorf("GROUP %s: %q, want count >= 2", group, line)
			}
			return nil
		})
		if count < 1 {
			return fmt.Errorf("no article in %s: %v (last %q)", group, perr, last)
		}
		if perr != nil {
			r.info("N51", fmt.Sprintf("count stayed %d after %v (%q): walking one article", count, testWait, last))
		}
		code, line, err := c.tryCmd("LAST")
		if e := e2eRfcStatus(code, line, err, 422); e != nil {
			return fmt.Errorf("LAST at the first article: %v", e)
		}
		prev := low
		got := int64(0)
		for i := int64(0); i <= count+1; i++ {
			code, line, err := c.tryCmd("NEXT")
			if err != nil {
				return fmt.Errorf("NEXT: %v", err)
			}
			if code == 421 {
				break
			}
			if code != 223 {
				return fmt.Errorf("NEXT: got %q, want 223 or 421", line)
			}
			f := strings.Fields(line)
			if len(f) < 3 {
				return fmt.Errorf("NEXT: %q, want 223 n <message-id>", line)
			}
			n, perr := strconv.ParseInt(f[1], 10, 64)
			if perr != nil || n <= prev || n > high || !strings.HasPrefix(f[2], "<") {
				return fmt.Errorf("NEXT: %q after %d (low %d, high %d)", line, prev, low, high)
			}
			prev = n
			got++
		}
		if got != count-1 {
			return fmt.Errorf("NEXT gave %d articles, want %d (count %d)", got, count-1, count)
		}
		want := 422
		if count >= 2 {
			want = 223
		}
		code, line, err = c.tryCmd("LAST")
		if e := e2eRfcStatus(code, line, err, want); e != nil {
			return fmt.Errorf("LAST at the last article: %v", e)
		}
		return nil
	}())

	// N52: LIST OVERVIEW.FMT → 215 and the 7 fixed lines (RFC 3977 §8.4)
	r.check("N52", "LIST OVERVIEW.FMT 215, 7 fields", func() error {
		lines, err := e2eRfcBlock(c, 215, "LIST OVERVIEW.FMT")
		if err != nil {
			return err
		}
		return e2eExpectLines(lines, []string{"Subject:", "From:", "Date:", "Message-ID:", "References:", ":bytes", ":lines"})
	}())

	// N53: OVER 1- → 224 with numbered lines; OVER 999999999- → 423 (RFC 3977 §8.3)
	r.check("N53", "OVER 1- 224, OVER 999999999- 423", func() error {
		lines, err := e2eRfcBlock(c, 224, "OVER 1-")
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return fmt.Errorf("OVER 1-: empty block")
		}
		for _, l := range lines {
			f := strings.SplitN(l, "\t", 2)
			if _, err := strconv.ParseInt(f[0], 10, 64); err != nil || len(f) != 2 {
				return fmt.Errorf("OVER 1-: line %q does not start with a number and TAB", l)
			}
		}
		code, line, err := c.tryCmd("OVER 999999999-")
		return e2eRfcStatus(code, line, err, 423)
	}())

	// N54: HDR Subject 1 → 225 "1 …"; LIST HEADERS → 215 with :bytes (RFC 3977 §8.5, §8.6)
	r.check("N54", "HDR Subject 1 225, LIST HEADERS 215", func() error {
		lines, err := e2eRfcBlock(c, 225, "HDR Subject 1")
		if err != nil {
			return err
		}
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "1 ") {
			return fmt.Errorf("HDR Subject 1: lines %q, want one line \"1 <subject>\"", lines)
		}
		lines, err = e2eRfcBlock(c, 215, "LIST HEADERS")
		if err != nil {
			return err
		}
		for _, l := range lines {
			if l == ":bytes" {
				return nil
			}
		}
		return fmt.Errorf("LIST HEADERS: lines %q lack :bytes", lines)
	}())

	// N55: MODE STREAM 203, CHECK fresh id 238, CHECK stored id 438 (RFC 4644 §2.3, §2.4)
	r.check("N55", "MODE STREAM 203, CHECK 238/438", func() error {
		code, line, err := c.tryCmd("MODE STREAM")
		if e := e2eRfcStatus(code, line, err, 203); e != nil {
			return fmt.Errorf("MODE STREAM: %v", e)
		}
		code, line, err = c.tryAuth(user, pass)
		if e := e2eRfcStatus(code, line, err, 281); e != nil {
			return fmt.Errorf("AUTHINFO: %v", e)
		}
		code, line, err = c.tryCmd("CHECK %s", freshID)
		if e := e2eRfcStatus(code, line, err, 238); e != nil {
			return fmt.Errorf("CHECK fresh id: %v", e)
		}
		if f := strings.Fields(line); len(f) < 2 || f[1] != freshID {
			return fmt.Errorf("CHECK fresh id: %q does not carry %s", line, freshID)
		}
		lines, err := e2eRfcBlock(c, 225, "HDR Message-ID 1")
		if err != nil {
			return err
		}
		f := []string{}
		if len(lines) == 1 {
			f = strings.Fields(lines[0])
		}
		if len(f) != 2 || f[0] != "1" || !strings.HasPrefix(f[1], "<") {
			return fmt.Errorf("HDR Message-ID 1: lines %q", lines)
		}
		stored := f[1]
		code, line, err = c.tryCmd("CHECK %s", stored)
		if e := e2eRfcStatus(code, line, err, 438); e != nil {
			return fmt.Errorf("CHECK stored id: %v", e)
		}
		if f := strings.Fields(line); len(f) < 2 || f[1] != stored {
			return fmt.Errorf("CHECK stored id: %q does not carry %s", line, stored)
		}
		return nil
	}())

	// N56: NEWGROUPS lists the group in LIST ACTIVE format; LIST ACTIVE wildmat (RFC 3977 §7.3, §7.6.3, §4)
	r.check("N56", "NEWGROUPS lists "+group+", LIST ACTIVE wildmat", func() error {
		since := time.Now().UTC().Add(-48 * time.Hour).Format("20060102 150405")
		ng, err := e2eRfcBlock(c, 231, "NEWGROUPS "+since+" GMT")
		if err != nil {
			return err
		}
		ngLine := ""
		for _, l := range ng {
			if strings.HasPrefix(l, group+" ") {
				ngLine = l
			}
		}
		if ngLine == "" {
			return fmt.Errorf("NEWGROUPS %s GMT: %s missing from %q", since, group, ng)
		}
		la, err := e2eRfcBlock(c, 215, "LIST ACTIVE "+group)
		if err != nil {
			return err
		}
		if len(la) != 1 || la[0] != ngLine {
			return fmt.Errorf("LIST ACTIVE %s: %q, want exactly the NEWGROUPS line %q", group, la, ngLine)
		}
		wm, err := e2eRfcBlock(c, 215, "LIST ACTIVE e2e.test.*,!*.b")
		if err != nil {
			return err
		}
		hasA, hasB := false, false
		for _, l := range wm {
			hasA = hasA || strings.HasPrefix(l, "e2e.test.a ")
			hasB = hasB || strings.HasPrefix(l, "e2e.test.b ")
		}
		if !hasA || hasB {
			return fmt.Errorf("LIST ACTIVE e2e.test.*,!*.b: %q, want e2e.test.a and not e2e.test.b", wm)
		}
		return nil
	}())

	// The main connection ends here; N57 needs a fresh, unauthenticated one.
	if code, line, err := c.tryCmd("QUIT"); err != nil || code != 205 {
		r.info("N56", fmt.Sprintf("QUIT after the main steps: code=%d line=%q err=%v", code, line, err))
	}
	c.close()

	// N57: STARTTLS (RFC 4642 §2.2)
	if withTLS {
		r.check("N57", "STARTTLS 382, handshake, not listed over TLS, second 502", func() error {
			s, err := e2eDialAddr(addr, testWait)
			if err != nil {
				return err
			}
			defer s.close()
			caps, err := e2eRfcCaps(s)
			if err != nil {
				return err
			}
			if !e2eRfcHasLabel(caps, "STARTTLS") {
				return fmt.Errorf("STARTTLS not listed before TLS: %q", caps)
			}
			code, line, err := s.tryCmd("STARTTLS")
			if e := e2eRfcStatus(code, line, err, 382); e != nil {
				return fmt.Errorf("STARTTLS: %v", e)
			}
			if err := e2eRfcUpgrade(s); err != nil {
				return err
			}
			caps, err = e2eRfcCaps(s)
			if err != nil {
				return fmt.Errorf("over TLS: %v", err)
			}
			if e2eRfcHasLabel(caps, "STARTTLS") {
				return fmt.Errorf("STARTTLS still listed over TLS: %q", caps)
			}
			code, line, err = s.tryCmd("STARTTLS")
			if e := e2eRfcStatus(code, line, err, 502); e != nil {
				return fmt.Errorf("second STARTTLS: %v", e)
			}
			code, line, err = s.tryCmd("QUIT")
			return e2eRfcStatus(code, line, err, 205)
		}())
	} else {
		r.check("N57", "STARTTLS not listed and 502 without a certificate", func() error {
			s, err := e2eDialAddr(addr, testWait)
			if err != nil {
				return err
			}
			defer s.close()
			caps, err := e2eRfcCaps(s)
			if err != nil {
				return err
			}
			if e2eRfcHasLabel(caps, "STARTTLS") {
				return fmt.Errorf("STARTTLS listed without a certificate: %q", caps)
			}
			code, line, err := s.tryCmd("STARTTLS")
			if e := e2eRfcStatus(code, line, err, 502); e != nil {
				return fmt.Errorf("STARTTLS: %v", e)
			}
			code, line, err = s.tryCmd("QUIT")
			return e2eRfcStatus(code, line, err, 205)
		}())
		r.info("N57", "no certificate configured")
	}
}
