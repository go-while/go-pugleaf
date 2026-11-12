package nntp

// Tests for MODE STREAM and CHECK (RFC 4644 §2.3, §2.4; plan nntp-rfc-commands,
// slice rfc-stream, D5/D10). The real dispatch gets the new commands in wave 2,
// so the tests run through stmSession.

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/nntp-testkit/client"
)

// stmSession serves one connection of hs.S: commands in extra go to the new
// handlers, everything else to the hardened dispatch (handleCommand).
func stmSession(t *testing.T, hs *hServer, extra map[string]func(cc *ClientConnection, args []string) error) *client.Conn {
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
		cc.resolvePeer() // what Handle does before the greeting (plan nntp-peering)
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
	c := client.New(t, conn)
	t.Cleanup(func() { _ = conn.Close(); <-done })
	if c.Greeting, err = c.ReadLine(); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	return c
}

// stmExtra routes MODE STREAM and CHECK to the new handlers; other MODE
// variants stay with the hardened handleMode.
func stmExtra() map[string]func(cc *ClientConnection, args []string) error {
	return map[string]func(cc *ClientConnection, args []string) error{
		"MODE": func(cc *ClientConnection, args []string) error {
			if len(args) > 0 && strings.ToUpper(args[0]) == "STREAM" {
				return cc.handleModeStream(args[1:])
			}
			return cc.handleMode(args)
		},
		"CHECK": func(cc *ClientConnection, args []string) error {
			return cc.handleCheck(args)
		},
	}
}

// stmDial opens an stmSession. Whether the connection may stream is decided by the
// server's peer registry (hServerOpts.Peers), as on a real connection.
func stmDial(t *testing.T, hs *hServer) *client.Conn {
	t.Helper()
	return stmSession(t, hs, stmExtra())
}

// stmDialUser opens an stmSession and authenticates a fresh active hUser with the given
// posting right (RFC 4643 §2.3: 381, then 281). Such a user is no peer: it may POST (with
// posting) but never feed (plan nntp-peering).
func stmDialUser(t *testing.T, hs *hServer, posting bool) *client.Conn {
	t.Helper()
	c := stmSession(t, hs, stmExtra())
	user, pass := hUser(t, posting, true)
	code, line := c.Cmd("AUTHINFO USER %s", user)
	if err := client.ExpectCode(code, line, 381); err != nil {
		t.Fatalf("AUTHINFO USER: %v", err)
	}
	code, line = c.Cmd("AUTHINFO PASS %s", pass)
	if err := client.ExpectCode(code, line, 281); err != nil {
		t.Fatalf("AUTHINFO PASS: %v", err)
	}
	return c
}

// stmExpectID checks the code and that the reply's second word is id
// (RFC 4644 §2.4.2: every CHECK reply carries the message-id).
func stmExpectID(code int, line string, want int, id string) error {
	if err := client.ExpectCode(code, line, want); err != nil {
		return err
	}
	if f := strings.Fields(line); len(f) < 2 || f[1] != id {
		return fmt.Errorf("reply %q does not carry message-id %q as its second word", line, id)
	}
	return nil
}

// stmChecked returns a copy of the ids the fake processor was asked about.
func stmChecked(p *hFakeProcessor) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.checked...)
}

// TestStmModeStream: RFC 4644 §2.3, MODE STREAM gives 203 iff streaming is
// allowed (D5), which since plan nntp-peering means "on a peer connection"; 501
// without a processor or with arguments, 480 while a peer login could still help and
// 502 for everyone else. MODE READER stays with the hardened handler.
func TestStmModeStream(t *testing.T) {
	t.Run("peer", func(t *testing.T) {
		reg, _ := hPeerIP("")
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}, Peers: reg})
		c := stmDial(t, hs)
		code, line := c.Cmd("MODE STREAM")
		if err := client.ExpectCode(code, line, 203); err != nil {
			t.Fatalf("MODE STREAM: %v", err)
		}
		code, line = c.Cmd("mode stream")
		if err := client.ExpectCode(code, line, 203); err != nil {
			t.Errorf("mode stream (lower case): %v", err)
		}
		code, line = c.Cmd("MODE STREAM extra")
		if err := client.ExpectCode(code, line, 501); err != nil {
			t.Errorf("MODE STREAM extra: %v", err)
		}
		// MODE STREAM changes no state (§2.3.2): MODE READER answers as before.
		code, line = c.Cmd("MODE READER")
		if err := client.ExpectCode(code, line, 200); err != nil {
			t.Errorf("MODE READER: %v", err)
		}
		code, line = c.Cmd("MODE")
		if err := client.ExpectCode(code, line, 501); err != nil {
			t.Errorf("MODE without argument: %v", err)
		}
	})
	t.Run("no-processor", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := stmDial(t, hs)
		code, line := c.Cmd("MODE STREAM")
		if err := client.ExpectCode(code, line, 501); err != nil {
			t.Fatalf("MODE STREAM: %v", err)
		}
		code, line = c.Cmd("MODE READER")
		if err := client.ExpectCode(code, line, 201); err != nil {
			t.Errorf("MODE READER: %v", err)
		}
	})
	t.Run("no-peers", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		c := stmDial(t, hs)
		code, line := c.Cmd("MODE STREAM")
		if err := client.ExpectCode(code, line, 502); err != nil {
			t.Fatalf("MODE STREAM without peers: %v", err)
		}
	})
	t.Run("login-pending", func(t *testing.T) {
		reg, _, _ := hPeerLogin(t, "127.0.0.1")
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}, Peers: reg})
		c := stmDial(t, hs)
		code, line := c.Cmd("MODE STREAM")
		if err := client.ExpectCode(code, line, 480); err != nil {
			t.Fatalf("MODE STREAM before the peer login: %v", err)
		}
	})
	t.Run("posting-user", func(t *testing.T) {
		// nntp_users.posting means POST and nothing else (plan nntp-peering)
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		c := stmDialUser(t, hs, true)
		code, line := c.Cmd("MODE STREAM")
		if err := client.ExpectCode(code, line, 502); err != nil {
			t.Fatalf("MODE STREAM as a posting user: %v", err)
		}
	})
	t.Run("non-posting-user", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		c := stmDialUser(t, hs, false)
		code, line := c.Cmd("MODE STREAM")
		if err := client.ExpectCode(code, line, 502); err != nil {
			t.Fatalf("MODE STREAM: %v", err)
		}
	})
}

// TestStmCheck: RFC 4644 §2.4, the CHECK gates in IHAVE's order (D5) and the
// history answers 238/438/431, each reply carrying the message-id.
func TestStmCheck(t *testing.T) {
	g := hGroupName("rc4.check")
	idPass, idDupes, idRetry, idError := hMessageID(g, 1), hMessageID(g, 2), hMessageID(g, 3), hMessageID(g, 4)
	idUnauth, idReader := hMessageID(g, 5), hMessageID(g, 6)
	results := map[string]int{
		idPass: history.CasePass, idDupes: history.CaseDupes, idRetry: history.CaseRetry, idError: history.CaseError,
	}
	checkFn := func(id string) int {
		if r, ok := results[id]; ok {
			return r
		}
		return history.CasePass
	}
	proc := &hFakeProcessor{checkFn: checkFn}
	reg, _ := hPeerIP("")
	hs := newTestServer(t, hServerOpts{Proc: proc, Peers: reg})

	// A peer login is still pending: 480, not 502 (plan nntp-peering).
	t.Run("login-pending", func(t *testing.T) {
		loginReg, _, _ := hPeerLogin(t, "127.0.0.1")
		hs0 := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{checkFn: checkFn}, Peers: loginReg})
		c := stmDial(t, hs0)
		code, line := c.Cmd("CHECK %s", idUnauth)
		if err := stmExpectID(code, line, 480, idUnauth); err != nil {
			t.Fatal(err)
		}
	})
	// A user, with or without posting rights, is no peer: 502.
	t.Run("posting-user", func(t *testing.T) {
		hs0 := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{checkFn: checkFn}})
		c := stmDialUser(t, hs0, true)
		code, line := c.Cmd("CHECK %s", idReader)
		if err := stmExpectID(code, line, 502, idReader); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no-peers", func(t *testing.T) {
		hs0 := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{checkFn: checkFn}})
		c := stmDial(t, hs0)
		code, line := c.Cmd("CHECK %s", idReader)
		if err := stmExpectID(code, line, 502, idReader); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("peer", func(t *testing.T) {
		c := stmDial(t, hs)
		for _, tc := range []struct {
			id   string
			want int
		}{
			{idPass, 238}, {idDupes, 438}, {idRetry, 431}, {idError, 431},
		} {
			code, line := c.Cmd("CHECK %s", tc.id)
			if err := stmExpectID(code, line, tc.want, tc.id); err != nil {
				t.Errorf("CHECK %s: %v", tc.id, err)
			}
		}
		// invalid message-id: 438 <arg>, no history lookup
		code, line := c.Cmd("CHECK not-an-id")
		if err := stmExpectID(code, line, 438, "not-an-id"); err != nil {
			t.Errorf("CHECK not-an-id: %v", err)
		}
		code, line = c.Cmd("CHECK")
		if err := client.ExpectCode(code, line, 501); err != nil {
			t.Errorf("CHECK without argument: %v", err)
		}
		code, line = c.Cmd("CHECK %s %s", idPass, idDupes)
		if err := client.ExpectCode(code, line, 501); err != nil {
			t.Errorf("CHECK with two arguments: %v", err)
		}
		// the connection is still in sync
		code, line = c.Cmd("CHECK %s", idPass)
		if err := stmExpectID(code, line, 238, idPass); err != nil {
			t.Errorf("CHECK after the errors: %v", err)
		}
	})
	t.Run("no-processor", func(t *testing.T) {
		hs0 := newTestServer(t, hServerOpts{Peers: reg})
		c := stmDial(t, hs0)
		id := hMessageID(g, 7)
		code, line := c.Cmd("CHECK %s", id)
		if err := stmExpectID(code, line, 500, id); err != nil {
			t.Errorf("CHECK: %v", err)
		}
		code, line = c.Cmd("CHECK")
		if err := client.ExpectCode(code, line, 500); err != nil {
			t.Errorf("CHECK without argument: %v", err)
		}
	})

	want := []string{idPass, idDupes, idRetry, idError, idPass}
	if got := stmChecked(proc); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("CheckMessageID calls = %q, want %q (only CHECKs past the gates)", got, want)
	}
}

// TestStmPipelined: RFC 4644 §2.1, pipelined CHECKs are answered in order,
// each reply carrying its id; 438 is not delayed (D10): the error-delay
// counter must not move across the 50 replies (structural, no wall clock).
func TestStmPipelined(t *testing.T) {
	const n = 50
	proc := &hFakeProcessor{checkFn: func(string) int { return history.CaseDupes }}
	reg, _ := hPeerIP("")
	hs := newTestServer(t, hServerOpts{Proc: proc, Peers: reg, ErrorDelay: 200 * time.Millisecond})
	c := stmDial(t, hs)

	g := hGroupName("rc4.pipe")
	ids := make([]string, n)
	var b strings.Builder
	for i := range ids {
		ids[i] = hMessageID(g, int64(i+1))
		b.WriteString("CHECK " + ids[i] + "\r\n")
	}
	delays := hs.S.ErrorDelayCount()
	if err := c.Send(b.String()); err != nil {
		t.Fatalf("send: %v", err)
	}
	for i, id := range ids {
		code, line, err := c.ReadStatus()
		if err != nil {
			t.Fatalf("reply %d: %v (%q)", i, err, line)
		}
		if err := stmExpectID(code, line, 438, id); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
	}
	if got := hs.S.ErrorDelayCount() - delays; got != 0 {
		t.Errorf("%d pipelined 438 replies reached the error delay %d times, want 0 (no error delay on 438)", n, got)
	}
	if got := stmChecked(proc); len(got) != n {
		t.Errorf("CheckMessageID called %d times, want %d", len(got), n)
	}
}

// TestStmCheckThenTakethis: a CHECK that gave 238 leaves no state that breaks
// the TAKETHIS of the same id (RFC 4644 §2.4, §2.5).
func TestStmCheckThenTakethis(t *testing.T) {
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	reg, _ := hPeerIP("")
	hs := newTestServer(t, hServerOpts{Proc: proc, Peers: reg})
	c := stmDial(t, hs)

	g := hGroupName("rc4.take")
	id := hMessageID(g, 1)
	code, line := c.Cmd("CHECK %s", id)
	if err := stmExpectID(code, line, 238, id); err != nil {
		t.Fatalf("CHECK: %v", err)
	}
	article := "Path: peer.stm.invalid!not-for-mail\r\n" +
		"From: Tester <tester@test.invalid>\r\n" +
		"Newsgroups: " + g + "\r\n" +
		"Subject: stm\r\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: " + id + "\r\n" +
		"\r\n" +
		"body\r\n" +
		".\r\n"
	if err := c.Send("TAKETHIS " + id + "\r\n" + article); err != nil {
		t.Fatalf("send TAKETHIS: %v", err)
	}
	code, line, err := c.ReadStatus()
	if err != nil {
		t.Fatalf("TAKETHIS reply: %v (%q)", err, line)
	}
	if err := stmExpectID(code, line, 239, id); err != nil {
		t.Fatalf("TAKETHIS: %v", err)
	}
	if arts := proc.processedArticles(); len(arts) != 1 || arts[0].MessageID != id {
		t.Errorf("processed %d articles, want exactly %s", len(arts), id)
	}
}
