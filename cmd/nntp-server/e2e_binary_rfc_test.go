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
	"crypto/tls"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/nntp-testkit/client"
	"github.com/go-while/nntp-testkit/conformance"
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
func e2eRfcBlock(c *client.Conn, want int, cmd string) ([]string, error) {
	code, line, err := c.TryCmd("%s", cmd)
	if e := e2eRfcStatus(code, line, err, want); e != nil {
		return nil, fmt.Errorf("%s: %v", cmd, e)
	}
	lines, err := c.TryReadDotLines()
	if err != nil {
		return lines, fmt.Errorf("%s: %v", cmd, err)
	}
	return lines, nil
}

// e2eRfcCaps runs CAPABILITIES and returns the capability lines and the parsed labels
// (kit client.ParseCapabilities, which also checks the block's format).
func e2eRfcCaps(c *client.Conn) ([]string, map[string][]string, error) {
	lines, err := e2eRfcBlock(c, 101, "CAPABILITIES")
	if err != nil {
		return lines, nil, err
	}
	m, err := client.ParseCapabilities(lines)
	return lines, m, err
}

// e2eRfcConf is the outcome of the kit's conformance run: the results by check name, or the
// error that kept the run from starting. Every N5x step requires the checks that cover it on
// top of its own, fixture-specific assertions, so a run that did not start fails the steps
// instead of letting them pass with no kit check behind them.
type e2eRfcConf struct {
	res map[string]conformance.Result
	err error
}

// e2eRfcConformance runs the kit's black-box protocol checks (package conformance) once against
// addr, before the steps below.
//
// postGroup is a group of its own, seeded by e2eRfcSeed: the checks that read an article skip
// themselves in an empty group, and the ones that post, feed or cancel must not move the
// article numbers of PUGLEAF_E2E_GROUP, which N51-N56 assert. NEWNEWS answers 500 by decision
// (plan nntp-rfc-commands), so that check is left out. Results no step requires are printed as
// INFO lines: the shell driver counts only the PASS/FAIL N5x lines.
func e2eRfcConformance(r e2eRfcReport, addr, user, pass, postGroup string) e2eRfcConf {
	if err := e2eRfcSeed(addr, user, pass, postGroup); err != nil {
		return e2eRfcConf{err: fmt.Errorf("seeding %s for the conformance run: %w", postGroup, err)}
	}
	rep, err := conformance.Check(conformance.Options{
		Addr: addr, User: user, Pass: pass, PostGroup: postGroup,
		Skip:     []string{"newnews"}, // 500 by decision
		Deadline: testWait,
	})
	if err != nil {
		return e2eRfcConf{err: err}
	}
	for _, res := range rep.Results {
		if res.Status != conformance.Pass {
			r.info("N50", fmt.Sprintf("conformance %s (%s): %v: %s", res.Name, res.RFC, res.Status, res.Detail))
		}
	}
	return e2eRfcConf{res: rep.ByName()}
}

// e2eRfcSeedArticles is how many articles e2eRfcSeed posts: NEXT/LAST and the range forms of
// the conformance checks want more than one article in the group.
const e2eRfcSeedArticles = 2

// e2eRfcSeed posts e2eRfcSeedArticles articles into group, so the conformance checks that read
// an article have ones to read there. The commit is asynchronous, so it waits until GROUP
// counts them.
func e2eRfcSeed(addr, user, pass, group string) error {
	c, err := e2eDialAddr(addr, testWait)
	if err != nil {
		return err
	}
	defer c.Close()
	if code, line, err := c.TryLogin(user, pass); err != nil || code != 281 {
		return fmt.Errorf("AUTHINFO: code=%d line=%q err=%v", code, line, err)
	}
	for i := 0; i < e2eRfcSeedArticles; i++ {
		id := fmt.Sprintf("<e2e-conf-seed-%d-%d@test.invalid>", time.Now().UnixNano(), i)
		art := e2eArticle(e2eHead(id, group, "E2E conformance seed", e2eNow()), []string{"seed"})
		if code, line, err := e2ePost(c, art); err != nil || code != 240 {
			return fmt.Errorf("POST into %s: code=%d line=%q err=%v, want 240", group, code, line, err)
		}
	}
	return e2eWaitFor(testWait, func() error {
		code, line, err := c.TryCmd("GROUP %s", group)
		if err != nil {
			return err
		}
		f := strings.Fields(line)
		if code != 211 || len(f) < 5 {
			return fmt.Errorf("GROUP %s: %q", group, line)
		}
		if n, perr := strconv.ParseInt(f[1], 10, 64); perr != nil || n < e2eRfcSeedArticles {
			return fmt.Errorf("GROUP %s: %q, want at least %d articles", group, line, e2eRfcSeedArticles)
		}
		return nil
	})
}

// require returns an error unless the run started and every named check passed.
func (cf e2eRfcConf) require(names ...string) error {
	if cf.err != nil {
		return fmt.Errorf("the kit's conformance run did not start: %v", cf.err)
	}
	for _, n := range names {
		got, ok := cf.res[n]
		if !ok {
			return fmt.Errorf("conformance check %s did not run", n)
		}
		if got.Status != conformance.Pass {
			return fmt.Errorf("conformance %s (%s): %v: %s", n, got.RFC, got.Status, got.Detail)
		}
	}
	return nil
}

// e2eRfcClientTLS is the client side of the STARTTLS handshake: the server's scratch
// certificate is self-signed, so it is not verified.
func e2eRfcClientTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // test client, self-signed scratch certificate
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
	// The kit's checks run first, on their own connections and in a group of their own, so
	// the articles they post cannot move what the steps below read.
	conf := e2eRfcConformance(r, addr, user, pass, e2eEnv("PUGLEAF_E2E_GROUP2", "e2e.test.b"))

	c, err := e2eDialAddr(addr, testWait)
	if err != nil {
		r.fail("N50", "connect", err.Error())
		t.FailNow()
	}
	defer c.Close()

	// N50: DATE → 111 yyyymmddhhmmss within 5 minutes of UTC now (RFC 3977 §7.1)
	r.check("N50", "DATE 111 within 5 min of UTC", func() error {
		code, line, err := c.TryCmd("DATE")
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
		return conf.require("greeting", "capabilities", "date", "help", "quit-eof",
			"unknown-command", "bad-arguments")
	}())

	// N51: NEXT/LAST walk the group (RFC 3977 §6.1.3, §6.1.4)
	r.check("N51", "NEXT/LAST walk "+group, func() error {
		// N10's TAKETHIS commits asynchronously: wait for count >= 2. Deliberate soft window,
		// not a hang guard: when it expires with count 1 the check only notes it (INFO) and
		// walks one article, so it must not take the full testWait.
		const n51SoftWait = 10 * time.Second
		var count, low, high int64
		var last string
		perr := e2eWaitFor(n51SoftWait, func() error {
			code, line, err := c.TryCmd("GROUP %s", group)
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
			r.info("N51", fmt.Sprintf("count stayed %d after %v (%q): walking one article", count, n51SoftWait, last))
		}
		code, line, err := c.TryCmd("LAST")
		if e := e2eRfcStatus(code, line, err, 422); e != nil {
			return fmt.Errorf("LAST at the first article: %v", e)
		}
		prev := low
		got := int64(0)
		for i := int64(0); i <= count+1; i++ {
			code, line, err := c.TryCmd("NEXT")
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
		code, line, err = c.TryCmd("LAST")
		if e := e2eRfcStatus(code, line, err, want); e != nil {
			return fmt.Errorf("LAST at the last article: %v", e)
		}
		return conf.require("group", "listgroup", "listgroup-range", "next-last",
			"article", "head", "body", "stat")
	}())

	// N52: LIST OVERVIEW.FMT → 215 and the fixed lines (RFC 3977 §8.4): the seven mandatory
	// fields plus "Xref:full", which the server appends in -xref partial and full
	// (nntp-cmd-over-hdr.go; the in-process test pins the same block in
	// internal/nntp/nntp-server-rfccmds_test.go rfcListOverviewFmt).
	r.check("N52", "LIST OVERVIEW.FMT 215, 7 fields + Xref:full", func() error {
		lines, err := e2eRfcBlock(c, 215, "LIST OVERVIEW.FMT")
		if err != nil {
			return err
		}
		if err := e2eExpectLines(lines, []string{"Subject:", "From:", "Date:", "Message-ID:", "References:", ":bytes", ":lines", "Xref:full"}); err != nil {
			return err
		}
		return conf.require("list-active", "list-newsgroups", "list-overview-fmt",
			"list-unknown-keyword")
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
		code, line, err := c.TryCmd("OVER 999999999-")
		if e := e2eRfcStatus(code, line, err, 423); e != nil {
			return e
		}
		return conf.require("over", "over-errors")
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
		found := false
		for _, l := range lines {
			found = found || l == ":bytes"
		}
		if !found {
			return fmt.Errorf("LIST HEADERS: lines %q lack :bytes", lines)
		}
		return conf.require("hdr", "list-headers")
	}())

	// N55: MODE STREAM 203, CHECK fresh id 238, CHECK stored id 438 (RFC 4644 §2.3, §2.4)
	r.check("N55", "MODE STREAM 203, CHECK 238/438", func() error {
		code, line, err := c.TryCmd("MODE STREAM")
		if e := e2eRfcStatus(code, line, err, 203); e != nil {
			return fmt.Errorf("MODE STREAM: %v", e)
		}
		code, line, err = c.TryLogin(user, pass)
		if e := e2eRfcStatus(code, line, err, 281); e != nil {
			return fmt.Errorf("AUTHINFO: %v", e)
		}
		code, line, err = c.TryCmd("CHECK %s", freshID)
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
		code, line, err = c.TryCmd("CHECK %s", stored)
		if e := e2eRfcStatus(code, line, err, 438); e != nil {
			return fmt.Errorf("CHECK stored id: %v", e)
		}
		if f := strings.Fields(line); len(f) < 2 || f[1] != stored {
			return fmt.Errorf("CHECK stored id: %q does not carry %s", line, stored)
		}
		// check-have and ihave-have are left out: this server commits asynchronously and
		// answers 431/436 ("retry later") for an article that is still being committed, which
		// the kit reports as a deferral (Skip), not as a pass.
		return conf.require("mode-stream", "mode-stream-capability", "check-new",
			"check-pipelined", "takethis", "takethis-duplicate")
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
		return conf.require("newgroups")
	}())

	// The main connection ends here; N57 needs a fresh, unauthenticated one.
	if code, line, err := c.TryCmd("QUIT"); err != nil || code != 205 {
		r.info("N56", fmt.Sprintf("QUIT after the main steps: code=%d line=%q err=%v", code, line, err))
	}
	c.Close()

	// N57: STARTTLS (RFC 4642 §2.2)
	if withTLS {
		r.check("N57", "STARTTLS 382, handshake, not listed over TLS, second 502", func() error {
			s, err := e2eDialAddr(addr, testWait)
			if err != nil {
				return err
			}
			defer s.Close()
			caps, labels, err := e2eRfcCaps(s)
			if err != nil {
				return err
			}
			if _, listed := labels["STARTTLS"]; !listed {
				return fmt.Errorf("STARTTLS not listed before TLS: %q", caps)
			}
			// the kit does the STARTTLS, the 382, the "no plaintext behind it" check
			// (CVE-2011-0411) and the handshake
			tc, err := s.TryStartTLS(e2eRfcClientTLS())
			if err != nil {
				return err
			}
			defer tc.Close()
			caps, labels, err = e2eRfcCaps(tc)
			if err != nil {
				return fmt.Errorf("over TLS: %v", err)
			}
			if _, listed := labels["STARTTLS"]; listed {
				return fmt.Errorf("STARTTLS still listed over TLS: %q", caps)
			}
			code, line, err := tc.TryCmd("STARTTLS")
			if e := e2eRfcStatus(code, line, err, 502); e != nil {
				return fmt.Errorf("second STARTTLS: %v", e)
			}
			code, line, err = tc.TryCmd("QUIT")
			if e := e2eRfcStatus(code, line, err, 205); e != nil {
				return e
			}
			return conf.require("starttls", "starttls-twice", "starttls-pipelined")
		}())
	} else {
		r.check("N57", "STARTTLS not listed and 502 without a certificate", func() error {
			s, err := e2eDialAddr(addr, testWait)
			if err != nil {
				return err
			}
			defer s.Close()
			caps, labels, err := e2eRfcCaps(s)
			if err != nil {
				return err
			}
			if _, listed := labels["STARTTLS"]; listed {
				return fmt.Errorf("STARTTLS listed without a certificate: %q", caps)
			}
			code, line, err := s.TryCmd("STARTTLS")
			if e := e2eRfcStatus(code, line, err, 502); e != nil {
				return fmt.Errorf("STARTTLS: %v", e)
			}
			code, line, err = s.TryCmd("QUIT")
			return e2eRfcStatus(code, line, err, 205)
		}())
		r.info("N57", "no certificate configured")
	}
}
