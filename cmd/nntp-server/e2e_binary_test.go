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
	"net/mail"
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

// TestE2EBinaryIntake runs the checks N40 and N42–N49 of plan nntp-intake-injection
// (`## End-to-end`) against the externally started server; N41 is a shell check of the
// script. Extra env: PUGLEAF_E2E_HOST (the server's -nntphostname, default example.org).
// The groups e2e.intake.gen (status y) and e2e.intake.mod (status m) must exist and
// e2e.intake.gen must be empty; e2e.nosuch.group must not exist.
//
//	PUGLEAF_E2E_ADDR=127.0.0.1:21119 PUGLEAF_E2E_HOST=example.org \
//	  go test -tags e2e -count=1 -v -run '^TestE2EBinaryIntake$' ./cmd/nntp-server/
func TestE2EBinaryIntake(t *testing.T) {
	addr := os.Getenv("PUGLEAF_E2E_ADDR")
	if addr == "" {
		t.Skip("PUGLEAF_E2E_ADDR not set (run scripts/test-nntp-server.sh)")
	}
	user := e2eEnv("PUGLEAF_E2E_USER", "e2euser0001")
	pass := e2eEnv("PUGLEAF_E2E_PASS", "e2epass0001")
	host := e2eEnv("PUGLEAF_E2E_HOST", "example.org")
	const gen, mod, nosuch = "e2e.intake.gen", "e2e.intake.mod", "e2e.nosuch.group"
	r := e2eReport{t: t}
	stamp := time.Now().UnixNano()
	newID := func(n string) string { return fmt.Sprintf("<e2e-%s-%d@test.invalid>", n, stamp) }
	statusIs := func(code int, line string, err error, want ...int) error {
		if err != nil {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		for _, w := range want {
			if code == w {
				return nil
			}
		}
		return fmt.Errorf("got %q, want %v", line, want)
	}
	modeReader := func(c *e2eClient) error {
		code, line, err := c.tryCmd("MODE READER")
		if e := statusIs(code, line, err, 200, 201); e != nil {
			return fmt.Errorf("MODE READER on the same connection: %v", e)
		}
		return nil
	}
	big := make([]string, 0, 1024)
	for i := 0; i < 1024; i++ { // about 66 KiB, over the default MaxArtSize of 32 KiB
		big = append(big, fmt.Sprintf("%04d %s", i, strings.Repeat("z", 59)))
	}

	c, err := e2eDialAddr(addr, 15*time.Second)
	if err != nil {
		r.fail("N40", "connect", err.Error())
		t.FailNow()
	}
	defer c.close()
	if code, line, err := c.tryAuth(user, pass); err != nil || code != 281 {
		r.fail("N40", "AUTHINFO 281", fmt.Sprintf("code=%d line=%q err=%v", code, line, err))
		t.FailNow()
	}

	// N40: POST to a group that does not exist → 441 (RFC 5537 §3.5: no group is created)
	r.check("N40", "POST to "+nosuch+" 441", func() error {
		code, line, err := c.tryPost(e2eWire(e2eHead(newID("n40"), nosuch, "E2E N40 unknown group", e2eNow()), []string{"n40"}))
		return statusIs(code, line, err, 441)
	}())

	// N42: POST without Message-ID and Date → 240; the injecting agent generated both,
	// plus Injection-Date and Injection-Info (RFC 5537 §3.5 steps 5/6/10/11)
	var head42 []string
	r.check("N42", "POST without Message-ID/Date 240, injected headers", func() error {
		code, line, err := c.tryPost(e2eWire(e2eHead("", gen, "E2E N42 generated headers", ""), []string{"n42"}))
		if e := statusIs(code, line, err, 240); e != nil {
			return e
		}
		var last string
		if err := e2eWaitFor(20*time.Second, func() error {
			code, line, err := c.tryCmd("GROUP %s", gen)
			if err != nil {
				return err
			}
			last = line
			if code != 211 || !strings.HasPrefix(line, "211 1 ") {
				return fmt.Errorf("got %q", line)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("GROUP %s: %v (last %q)", gen, err, last)
		}
		code, line, err = c.tryCmd("HEAD 1")
		if e := statusIs(code, line, err, 221); e != nil {
			return fmt.Errorf("HEAD 1: %v", e)
		}
		hdrs, err := c.tryDotLines()
		if err != nil {
			return err
		}
		head42 = hdrs
		if v := e2eHeaderValue(hdrs, "Message-ID"); !strings.HasPrefix(v, "<") || !strings.HasSuffix(v, "@"+host+">") {
			return fmt.Errorf("Message-ID %q is not a generated <…@%s>", v, host)
		}
		if _, err := mail.ParseDate(e2eHeaderValue(hdrs, "Date")); err != nil {
			return fmt.Errorf("Date %q: %v", e2eHeaderValue(hdrs, "Date"), err)
		}
		if _, err := mail.ParseDate(e2eHeaderValue(hdrs, "Injection-Date")); err != nil {
			return fmt.Errorf("Injection-Date %q: %v", e2eHeaderValue(hdrs, "Injection-Date"), err)
		}
		if v := e2eHeaderValue(hdrs, "Injection-Info"); !strings.HasPrefix(v, host) {
			return fmt.Errorf("Injection-Info %q does not start with %q", v, host)
		}
		return nil
	}())

	// N43: the served Path starts with "<identity>!.POSTED!" (RFC 5537 §3.5 step 9)
	r.check("N43", "Path starts with "+host+"!.POSTED!", func() error {
		if head42 == nil {
			return fmt.Errorf("no HEAD from N42")
		}
		if p := e2eHeaderValue(head42, "Path"); !strings.HasPrefix(p, host+"!.POSTED!") {
			return fmt.Errorf("Path %q", p)
		}
		return nil
	}())

	// N44: IHAVE of an article with two Subject headers → 335 then 437
	r.check("N44", "IHAVE two Subject headers 335 then 437", func() error {
		id := newID("n44")
		code, line, err := c.tryCmd("IHAVE %s", id)
		if e := statusIs(code, line, err, 335); e != nil {
			return fmt.Errorf("IHAVE: %v", e)
		}
		head := append(e2eHead(id, gen, "E2E N44 first subject", e2eNow()), "Subject: E2E N44 second subject")
		if err := c.send(e2eWire(head, []string{"n44"})); err != nil {
			return err
		}
		code, line, err = c.readStatus()
		return statusIs(code, line, err, 437)
	}())

	// N45: POST of a 64 KiB article → 441, the connection stays usable
	r.check("N45", "POST 64 KiB 441, then MODE READER", func() error {
		code, line, err := c.tryPost(e2eWire(e2eHead(newID("n45"), gen, "E2E N45 big", e2eNow()), big))
		if e := statusIs(code, line, err, 441); e != nil {
			return e
		}
		return modeReader(c)
	}())

	// N46: pipelined TAKETHIS of a 64 KiB article and a small one → 439 <id1>, 239 <id2>
	r.check("N46", "TAKETHIS 64 KiB 439 then small 239", func() error {
		id1, id2 := newID("n46a"), newID("n46b")
		stream := "TAKETHIS " + id1 + "\r\n" + e2eWire(e2eHead(id1, gen, "E2E N46 big", e2eNow()), big) +
			"TAKETHIS " + id2 + "\r\n" + e2eWire(e2eHead(id2, gen, "E2E N46 small", e2eNow()), []string{"n46"})
		if err := c.send(stream); err != nil {
			return err
		}
		for _, w := range []struct {
			code int
			id   string
		}{{439, id1}, {239, id2}} {
			code, line, err := c.readStatus()
			if e := statusIs(code, line, err, w.code); e != nil {
				return e
			}
			if f := strings.Fields(line); len(f) < 2 || f[1] != w.id {
				return fmt.Errorf("reply %q does not carry %s", line, w.id)
			}
		}
		return nil
	}())

	// N47: IHAVE with a malformed message-id → 501 (RFC 3977 §3.2.1)
	r.check("N47", "IHAVE not-a-message-id 501", func() error {
		code, line, err := c.tryCmd("IHAVE not-a-message-id")
		return statusIs(code, line, err, 501)
	}())

	// N48: POST to a moderated group without Approved → 441 (RFC 5537 §3.7 step 5)
	r.check("N48", "POST to "+mod+" without Approved 441", func() error {
		code, line, err := c.tryPost(e2eWire(e2eHead(newID("n48"), mod, "E2E N48 moderated", e2eNow()), []string{"n48"}))
		return statusIs(code, line, err, 441)
	}())

	// N49: POST with a header line ".." (un-stuffed ".") → 441, the connection stays usable
	r.check("N49", "POST with a \"..\" header line 441, then MODE READER", func() error {
		head := e2eHead(newID("n49"), gen, "E2E N49 dot header", e2eNow())
		head = append(head[:2], append([]string{"."}, head[2:]...)...) // sent as ".." by e2eWire
		code, line, err := c.tryPost(e2eWire(head, []string{"n49"}))
		if e := statusIs(code, line, err, 441); e != nil {
			return e
		}
		return modeReader(c)
	}())
}
