//go:build e2e

package main

// TestE2EBinary runs the client steps N01–N11 of the plan's `## End-to-end`
// (nntp-audit-tests) against an externally started server, normally the one
// scripts/test-nntp-server.sh builds, seeds and starts:
//
//	PUGLEAF_E2E_ADDR=127.0.0.1:21119 PUGLEAF_E2E_USER=e2euser0001 PUGLEAF_E2E_PASS=e2epass0001 \
//	  go test -tags e2e -count=1 -v -run '^TestE2EBinary$' ./cmd/nntp-server/
//
// It prints one `PASS|FAIL|INFO N<nn> [nntp] <text>` line per step (the script
// counts them) and fails the test on any FAIL. PUGLEAF_E2E_GROUP (default
// e2e.test.a) must exist, be active and be empty.

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// e2eReport prints the step lines and fails t on FAIL.
type e2eReport struct{ t *testing.T }

func (r e2eReport) pass(id, text string) { fmt.Printf("PASS %s [nntp] %s\n", id, text) }

func (r e2eReport) fail(id, text, detail string) {
	fmt.Printf("FAIL %s [nntp] %s :: %s\n", id, text, detail)
	r.t.Errorf("%s %s: %s", id, text, detail)
}

func (r e2eReport) check(id, text string, err error) bool {
	if err != nil {
		r.fail(id, text, err.Error())
		return false
	}
	r.pass(id, text)
	return true
}

func e2eEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func TestE2EBinary(t *testing.T) {
	addr := os.Getenv("PUGLEAF_E2E_ADDR")
	if addr == "" {
		t.Skip("PUGLEAF_E2E_ADDR not set (run scripts/test-nntp-server.sh)")
	}
	user := e2eEnv("PUGLEAF_E2E_USER", "e2euser0001")
	pass := e2eEnv("PUGLEAF_E2E_PASS", "e2epass0001")
	group := e2eEnv("PUGLEAF_E2E_GROUP", "e2e.test.a")
	r := e2eReport{t: t}
	stamp := time.Now().UnixNano()
	id := fmt.Sprintf("<e2e-n04-%d@test.invalid>", stamp)
	id10 := fmt.Sprintf("<e2e-n10-%d@test.invalid>", stamp)
	body := []string{"e2e binary body", ".dotted line", "", "end"}

	// N01: greeting 200 (RFC 3977 §5.1.1, posting allowed)
	c, err := e2eDialAddr(addr, 15*time.Second)
	if err != nil {
		r.fail("N01", "greeting 200", err.Error())
		t.FailNow()
	}
	defer c.close()
	if code, _ := e2eParseCode(c.Greeting); code == 200 {
		r.pass("N01", "greeting 200")
	} else {
		r.fail("N01", "greeting 200", fmt.Sprintf("%q", c.Greeting))
	}

	// N02: CAPABILITIES starts with VERSION 2 (RFC 3977 §5.2.2)
	r.check("N02", "CAPABILITIES starts with VERSION 2", func() error {
		code, line, err := c.tryCmd("CAPABILITIES")
		if err != nil || code != 101 {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		caps, err := c.tryDotLines()
		if err != nil {
			return err
		}
		if len(caps) == 0 || caps[0] != "VERSION 2" {
			return fmt.Errorf("lines %q", caps)
		}
		return nil
	}())

	// N03: AUTHINFO USER/PASS → 281 (RFC 4643 §2.3)
	r.check("N03", "AUTHINFO 281", func() error {
		code, line, err := c.tryAuth(user, pass)
		if err != nil || code != 281 {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		return nil
	}())

	// N04: POST with all headers → 240 (RFC 3977 §6.3.1)
	r.check("N04", "POST 240", func() error {
		code, line, err := c.tryPost(e2eWire(e2eHead(id, group, "E2E binary post", e2eNow()), body))
		if err != nil || code != 240 {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		return nil
	}())

	// N05: within 20 s GROUP reports the article: 211 1 1 1 (async commit)
	start := time.Now()
	r.check("N05", "GROUP "+group+" 211 1 1 1 within 20s", func() error {
		var last string
		err := e2eWaitFor(20*time.Second, func() error {
			code, line, err := c.tryCmd("GROUP %s", group)
			if err != nil {
				return err
			}
			last = line
			if code != 211 || !strings.HasPrefix(line, "211 1 1 1 ") {
				return fmt.Errorf("got %q", line)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("%v (last %q)", err, last)
		}
		return nil
	}())
	fmt.Printf("INFO N05 [nntp] commit visible after %s\n", time.Since(start).Round(10*time.Millisecond))

	// N06: XOVER 1- → exactly 1 overview line (RFC 2980 §2.8)
	r.check("N06", "XOVER 1- one line", func() error {
		code, line, err := c.tryCmd("XOVER 1-")
		if err != nil || code != 224 {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		lines, err := c.tryDotLines()
		if err != nil {
			return err
		}
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "1\t") || !strings.Contains(lines[0], id) {
			return fmt.Errorf("lines %q", lines)
		}
		return nil
	}())

	// N07: ARTICLE 1 → 220 1 <id> and the posted body (RFC 3977 §6.2.1)
	r.check("N07", "ARTICLE 1 body matches", func() error {
		code, line, err := c.tryCmd("ARTICLE 1")
		if err != nil || code != 220 || !strings.HasPrefix(line, "220 1 "+id) {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		lines, err := c.tryDotLines()
		if err != nil {
			return err
		}
		_, got := e2eSplitArticle(lines)
		return e2eExpectLines(got, body)
	}())

	// N08: STAT <id> → 223 (RFC 3977 §6.2.4)
	r.check("N08", "STAT <id> 223", func() error {
		code, line, err := c.tryCmd("STAT %s", id)
		if err != nil || code != 223 || !strings.Contains(line, id) {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		return nil
	}())

	// N09: IHAVE of the stored id → 435 (RFC 3977 §6.3.2.2); 436 while still in flight
	r.check("N09", "IHAVE same id 435", func() error {
		var last string
		var stop error // a definite wrong answer ends the polling
		err := e2eWaitFor(20*time.Second, func() error {
			code, line, err := c.tryCmd("IHAVE %s", id)
			if err != nil {
				stop = err
				return nil
			}
			last = line
			switch code {
			case 435:
				return nil
			case 436:
				return fmt.Errorf("got %q", line)
			case 335:
				// wanted again: send the article to keep the stream in sync, then fail
				_ = c.send(e2eWire(e2eHead(id, group, "E2E binary post", e2eNow()), body))
				_, l2, _ := c.readStatus()
				stop = fmt.Errorf("got %q (then %q) for a stored article", line, l2)
				return nil
			}
			stop = fmt.Errorf("got %q", line)
			return nil
		})
		if stop != nil {
			return stop
		}
		if err != nil {
			return fmt.Errorf("%v (last %q)", err, last)
		}
		return nil
	}())

	// N10: TAKETHIS a new id → 239 <id> (RFC 4644 §2.5)
	r.check("N10", "TAKETHIS new id 239", func() error {
		if err := c.send("TAKETHIS " + id10 + "\r\n" + e2eWire(e2eHead(id10, group, "E2E binary takethis", e2eNow()), []string{"takethis"})); err != nil {
			return err
		}
		code, line, err := c.readStatus()
		if err != nil || code != 239 || !strings.HasPrefix(line, "239 "+id10) {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		return nil
	}())

	// N11: QUIT → 205 (RFC 3977 §5.4)
	r.check("N11", "QUIT 205", func() error {
		code, line, err := c.tryCmd("QUIT")
		if err != nil || code != 205 {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		return nil
	}())
}
