package nntp

// Session tests for the NNTP server (plan nntp-audit-tests, slice test-nntp-session):
// greeting, CAPABILITIES, MODE, HELP, QUIT, command parsing, pipelining, line
// limits, stats growth, idle timeout, MaxConns, Stop, TLS, Local430 and
// ServerStats units, and a command-line fuzz target.
//
// Every check states the RFC-correct behavior. Where the current code differs,
// the result goes through knownBug with the seed id or the audit id (A-<slug>-<n>).
// Identifiers use the sess/TestSess/FuzzSess prefixes (contract K2).

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
)

// sessKnownCommands is the dispatch table of handleCommand (nntp-server-cliconns.go).
var sessKnownCommands = []string{
	"CAPABILITIES", "MODE", "AUTHINFO", "QUIT", "HELP", "LIST", "GROUP", "LISTGROUP",
	"STAT", "HEAD", "BODY", "ARTICLE", "XOVER", "XHDR", "POST", "IHAVE", "TAKETHIS",
}

// ---------------------------------------------------------------------------
// Helpers (sess prefix; the harness is read-only)

// sessIsChild reports whether this is the runIsolated child of t.
func sessIsChild(t *testing.T) bool {
	return os.Getenv("PUGLEAF_ISOLATED") == t.Name()
}

// sessIsTimeout reports whether err is an I/O deadline error.
func sessIsTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// sessExpectClosed returns nil when the server closes the connection within d
// (any lines it sends first, such as a 400, are allowed), or an error when the
// connection is still open after d.
func sessExpectClosed(c *hClient, d time.Duration) error {
	end := time.Now().Add(d)
	var lines []string
	for {
		left := time.Until(end)
		if left <= 0 {
			return fmt.Errorf("connection still open after %s (lines %q)", d, lines)
		}
		c.Deadline = left
		line, err := c.readLine()
		if err != nil {
			if sessIsTimeout(err) {
				return fmt.Errorf("connection still open after %s (lines %q)", d, lines)
			}
			return nil // EOF or reset: closed
		}
		lines = append(lines, line)
		if len(lines) > 16 {
			return fmt.Errorf("server keeps sending instead of closing: %q", lines)
		}
	}
}

// sessUser inserts an NNTP user named t1user<seq> (>= 10 chars, K3).
func sessUser(t *testing.T, posting bool) (user, pass string) {
	t.Helper()
	db := hTestDB(t)
	user = fmt.Sprintf("t1user%04d", hSeq.Add(1))
	pass = "pw-" + user
	if err := db.InsertNNTPUser(&models.NNTPUser{
		Username: user, Password: pass, MaxConns: 10, Posting: posting, WebUserID: 0, IsActive: true,
	}); err != nil {
		t.Fatalf("InsertNNTPUser %s: %v", user, err)
	}
	return user, pass
}

// sessAuth runs AUTHINFO USER/PASS and requires 381 then 281 (RFC 4643 §2.3).
func sessAuth(t *testing.T, c *hClient, user, pass string) {
	t.Helper()
	code, line := c.cmd(t, "AUTHINFO USER %s", user)
	if err := hExpectCode(code, line, 381); err != nil {
		t.Fatalf("AUTHINFO USER: %v", err)
	}
	code, line = c.cmd(t, "AUTHINFO PASS %s", pass)
	if err := hExpectCode(code, line, 281); err != nil {
		t.Fatalf("AUTHINFO PASS: %v", err)
	}
}

// sessCaps runs CAPABILITIES and returns the capability lines (RFC 3977 §5.2.2: 101 + list).
func sessCaps(t *testing.T, c *hClient) []string {
	t.Helper()
	code, line := c.cmd(t, "CAPABILITIES")
	if err := hExpectCode(code, line, 101); err != nil {
		t.Fatalf("CAPABILITIES: %v", err)
	}
	return c.readDotLines(t)
}

// sessHasCap reports whether a capability line starts with label (case-insensitive).
func sessHasCap(caps []string, label string) bool {
	for _, l := range caps {
		f := strings.Fields(l)
		if len(f) > 0 && strings.EqualFold(f[0], label) {
			return true
		}
	}
	return false
}

// sessSelfSignedTLS builds an in-memory self-signed ECDSA P-256 server config.
func sessSelfSignedTLS(t *testing.T) (*tls.Config, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "t1.session.test.invalid"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}, der
}

// ---------------------------------------------------------------------------
// Greeting

func TestSessGreeting(t *testing.T) {
	t.Run("with-processor", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		c := hs.dial(t)
		code, err := hParseCode(c.Greeting)
		if err != nil {
			t.Fatal(err)
		}
		// RFC 3977 §5.1.1: 200 "service available, posting allowed".
		if err := hExpectCode(code, c.Greeting, 200); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("read-only", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		code, err := hParseCode(c.Greeting)
		if err != nil {
			t.Fatal(err)
		}
		// RFC 3977 §5.1.1: without a processor nothing can be posted, so the
		// greeting is 201 "service available, posting prohibited".
		if err := hExpectCode(code, c.Greeting, 201); err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// CAPABILITIES

func TestSessCapabilities(t *testing.T) {
	t.Run("version-first", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		caps := sessCaps(t, hs.dial(t))
		// RFC 3977 §3.3.2/§5.2.2: VERSION is mandatory and the first line; "VERSION 2" for RFC 3977.
		if len(caps) == 0 || caps[0] != "VERSION 2" {
			t.Fatalf("capabilities %q: first line must be VERSION 2", caps)
		}
		// RFC 3977 §3.3.2: a reading server advertises READER.
		if !sessHasCap(caps, "READER") {
			t.Fatalf("capabilities %q: READER missing", caps)
		}
	})
	t.Run("reader-without-mode-reader", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		caps := sessCaps(t, hs.dial(t))
		// RFC 3977 §3.4.2/§5.3: MODE-READER is advertised only by a mode-switching
		// server that is not in reader mode yet; never together with READER.
		var err error
		if sessHasCap(caps, "READER") && sessHasCap(caps, "MODE-READER") {
			err = fmt.Errorf("READER and MODE-READER both advertised: %q", caps)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no-nonstandard-labels", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		caps := sessCaps(t, hs.dial(t))
		// RFC 3977 §3.3.3: labels beginning with "X" are reserved for private
		// extensions, so XOVER/XHDR are legal (only logged as a policy note; the
		// standard capabilities are OVER/HDR, §8). Any other label must be
		// registered; TAKETHIS is not one: RFC 4644 §2.1 advertises it via STREAMING.
		for _, l := range []string{"XOVER", "XHDR"} {
			if sessHasCap(caps, l) {
				t.Logf("policy: private label %s advertised", l)
			}
		}
		var err error
		if sessHasCap(caps, "TAKETHIS") {
			err = fmt.Errorf("unregistered label TAKETHIS (no X prefix) in %q", caps)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("read-only-no-post-ihave", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		caps := sessCaps(t, hs.dial(t))
		// RFC 3977 §3.3.2: POST and IHAVE are advertised only when the server
		// accepts them; RFC 4644 §2.1 likewise for STREAMING.
		for _, l := range []string{"POST", "IHAVE", "STREAMING"} {
			if sessHasCap(caps, l) {
				t.Errorf("read-only server advertises %s: %q", l, caps)
			}
		}
	})
	t.Run("processor-ihave", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		caps := sessCaps(t, hs.dial(t))
		// RFC 3977 §3.3.2: IHAVE is advertised when the server supports IHAVE.
		if !sessHasCap(caps, "IHAVE") {
			t.Fatalf("server with processor lacks IHAVE: %q", caps)
		}
	})
	t.Run("processor-streaming-consistent", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		c := hs.dial(t)
		caps := sessCaps(t, c)
		code, line := c.cmd(t, "MODE STREAM")
		// RFC 4644 §2.2: STREAMING may be advertised only when MODE STREAM, CHECK
		// and TAKETHIS are implemented: STREAMING is listed iff MODE STREAM gives 203.
		if has := sessHasCap(caps, "STREAMING"); has != (code == 203) {
			t.Fatalf("STREAMING advertised=%v but MODE STREAM answered %q", has, line)
		}
	})
	t.Run("processor-streaming-complete", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		code, line := hs.dial(t).cmd(t, "MODE STREAM")
		// RFC 4644 §2.2/§2.3: TAKETHIS is dispatched, so the rest of streaming is
		// expected too (MODE STREAM → 203, CHECK, the STREAMING capability).
		var err error
		if code != 203 {
			err = fmt.Errorf("TAKETHIS is accepted but MODE STREAM answered %q (want 203)", line)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("post-allowed-user", func(t *testing.T) {
		user, pass := sessUser(t, true)
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		c := hs.dial(t)
		sessAuth(t, c, user, pass)
		caps := sessCaps(t, c)
		// RFC 3977 §3.3.2: POST is advertised when posting is permitted.
		if !sessHasCap(caps, "POST") {
			t.Fatalf("posting user: POST missing from %q", caps)
		}
	})
	t.Run("post-not-allowed-user", func(t *testing.T) {
		user, pass := sessUser(t, false)
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		c := hs.dial(t)
		sessAuth(t, c, user, pass)
		caps := sessCaps(t, c)
		// RFC 3977 §3.3.1: a capability is listed only in the state where it
		// applies; this user may not post, so POST is not listed.
		var err error
		if sessHasCap(caps, "POST") {
			err = fmt.Errorf("user without posting permission sees POST: %q", caps)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("authinfo-after-auth", func(t *testing.T) {
		user, pass := sessUser(t, true)
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		c := hs.dial(t)
		sessAuth(t, c, user, pass)
		caps := sessCaps(t, c)
		// RFC 4643 §2.2: after successful authentication the server MUST NOT
		// advertise the AUTHINFO capability any more.
		var err error
		if sessHasCap(caps, "AUTHINFO") {
			err = fmt.Errorf("AUTHINFO still advertised after 281: %q", caps)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// MODE, HELP, QUIT

func TestSessMode(t *testing.T) {
	t.Run("reader-with-processor", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		code, line := hs.dial(t).cmd(t, "MODE READER")
		// RFC 3977 §5.3.2: 200 (posting allowed) or 201 (posting prohibited).
		if err := hExpectCode(code, line, 200, 201); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("reader-read-only", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		code, line := hs.dial(t).cmd(t, "MODE READER")
		// RFC 3977 §5.3.2: posting is not possible without a processor → 201.
		if err := hExpectCode(code, line, 201); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no-argument", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		code, line := hs.dial(t).cmd(t, "MODE")
		// RFC 3977 §3.2.1: 501 syntax error in the arguments.
		if err := hExpectCode(code, line, 501); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("stream", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
		code, line := hs.dial(t).cmd(t, "MODE STREAM")
		// RFC 4644 §2.3: 203 streaming permitted; a server without streaming
		// answers the unknown MODE variant with 501 (RFC 3977 §3.2.1).
		if err := hExpectCode(code, line, 203, 501); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown-variant", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		code, line := hs.dial(t).cmd(t, "MODE FOO")
		// RFC 3977 §3.2.1: an unknown variant of a known command is a syntax error → 501.
		if err := hExpectCode(code, line, 501); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSessHelp(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "HELP")
	// RFC 3977 §7.2: 100 followed by a dot-terminated multi-line help text.
	if err := hExpectCode(code, line, 100); err != nil {
		t.Fatal(err)
	}
	if body := c.readDotLines(t); len(body) == 0 {
		t.Fatal("HELP: empty help text")
	}
	// The connection stays usable after the block.
	code, line = c.cmd(t, "MODE READER")
	if err := hExpectCode(code, line, 200, 201); err != nil {
		t.Fatalf("after HELP: %v", err)
	}
}

func TestSessQuit(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "QUIT")
	// RFC 3977 §5.4: 205 and the server closes the connection.
	if err := hExpectCode(code, line, 205); err != nil {
		t.Fatal(err)
	}
	if err := sessExpectClosed(c, time.Second); err != nil {
		t.Fatalf("after QUIT: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Command parsing

func TestSessUnknownCommands(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)

	// RFC 3977 §3.2.1: an unknown command gets 500.
	code, line := c.cmd(t, "foo")
	if err := hExpectCode(code, line, 500); err != nil {
		t.Errorf("unknown command: %v", err)
	}
	// RFC 3977 §3.1/§3.2.1: an empty line is no command; 500 or 501.
	code, line = c.cmd(t, "")
	if err := hExpectCode(code, line, 500, 501); err != nil {
		t.Errorf("empty line: %v", err)
	}
	code, line = c.cmd(t, "   \t ")
	if err := hExpectCode(code, line, 500, 501); err != nil {
		t.Errorf("whitespace-only line: %v", err)
	}
	// RFC 3977 §3.1: commands are case-insensitive.
	code, line = c.cmd(t, "capabilities")
	if err := hExpectCode(code, line, 101); err != nil {
		t.Errorf("lowercase capabilities: %v", err)
	} else {
		c.readDotLines(t)
	}
	code, line = c.cmd(t, "mOdE rEaDeR")
	if err := hExpectCode(code, line, 200, 201); err != nil {
		t.Errorf("mixed-case MODE READER: %v", err)
	}
	code, line = c.cmd(t, "quit")
	if err := hExpectCode(code, line, 205); err != nil {
		t.Fatalf("lowercase quit: %v", err)
	}
	if err := sessExpectClosed(c, time.Second); err != nil {
		t.Fatalf("after quit: %v", err)
	}
}

func TestSessPipelining(t *testing.T) {
	g := hGroup(t, "t1.pipelining", 2, nil)
	hs := newTestServer(t, hServerOpts{})

	t.Run("existing-group", func(t *testing.T) {
		c := hs.dial(t)
		// RFC 3977 §3.5: a client may send several commands without waiting; the
		// server answers them in order.
		if err := c.send("GROUP " + g + CRLF + "STAT 1" + CRLF + "QUIT" + CRLF); err != nil {
			t.Fatal(err)
		}
		code, line, err := c.readStatus()
		if err != nil {
			t.Fatalf("reply 1: %v (%q)", err, line)
		}
		// RFC 3977 §6.1.1: 211 number low high group.
		if f := strings.Fields(line); code != 211 || len(f) < 5 || f[1] != "2" || f[4] != g {
			t.Fatalf("reply 1 %q: want 211 2 <low> <high> %s", line, g)
		}
		code, line, err = c.readStatus()
		if err != nil {
			t.Fatalf("reply 2: %v (%q)", err, line)
		}
		// RFC 3977 §6.2.4: 223 n message-id.
		if f := strings.Fields(line); code != 223 || len(f) < 3 || f[1] != "1" || f[2] != hMessageID(g, 1) {
			t.Fatalf("reply 2 %q: want 223 1 %s", line, hMessageID(g, 1))
		}
		code, line, err = c.readStatus()
		if err != nil {
			t.Fatalf("reply 3: %v (%q)", err, line)
		}
		if err := hExpectCode(code, line, 205); err != nil {
			t.Fatalf("reply 3: %v", err)
		}
		if err := sessExpectClosed(c, time.Second); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing-group", func(t *testing.T) {
		c := hs.dial(t)
		missing := hGroupName("t1.pipelining.missing")
		if err := c.send("GROUP " + missing + CRLF + "STAT 1" + CRLF + "QUIT" + CRLF); err != nil {
			t.Fatal(err)
		}
		// RFC 3977 §6.1.1: 411 no such group; §6.2.4: 412 no group selected; §5.4: 205.
		for i, want := range []int{411, 412, 205} {
			code, line, err := c.readStatus()
			if err != nil {
				t.Fatalf("reply %d: %v (%q)", i+1, err, line)
			}
			if err := hExpectCode(code, line, want); err != nil {
				t.Fatalf("reply %d: %v", i+1, err)
			}
		}
	})
	t.Run("quit-then-command", func(t *testing.T) {
		// Isolated: before the A-nntp-sec-1 fix the buffered command after QUIT
		// reached UpdateDeadlines with a nil conn and the panic killed the process.
		err := runIsolated(t, 60*time.Second, func(t *testing.T) {
			hs := newTestServer(t, hServerOpts{})
			c := hs.dial(t)
			c.Deadline = 2 * time.Second
			if err := c.send("QUIT" + CRLF + "HELP" + CRLF); err != nil {
				t.Fatal(err)
			}
			// RFC 3977 §5.4: QUIT gets 205 and the server closes the connection;
			// the pipelined HELP after it is never answered.
			code, line, err := c.readStatus()
			if err != nil {
				t.Fatalf("QUIT reply: %v (%q)", err, line)
			}
			if err := hExpectCode(code, line, 205); err != nil {
				t.Fatal(err)
			}
			if err := sessExpectClosed(c, time.Second); err != nil {
				t.Fatalf("after 205: %v", err)
			}
			// The server must survive: a new client still gets a greeting.
			c2, err := hs.tryDial(2 * time.Second)
			if err != nil {
				t.Fatalf("server gone after pipelined QUIT: %v", err)
			}
			c2.close()
		})
		if sessIsChild(t) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// Line limits (SEC-3)

func TestSessLineLimit(t *testing.T) {
	t.Run("over-512-octets", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		long := "GROUP " + strings.Repeat("a", 600) // 608 octets with CRLF
		code, line, err := c.tryCmd("%s", long)
		// RFC 3977 §3.1: command lines MUST NOT exceed 512 octets including CRLF.
		// The server rejects a longer line with 501 or closes the connection.
		var res error
		switch {
		case err == nil:
			res = hExpectCode(code, line, 501)
		case sessIsTimeout(err):
			// no reply but the socket is still open: neither 501 nor a close
			res = fmt.Errorf("no reply and no close: %v", err)
		}
		if res != nil {
			t.Fatal(res)
		}
	})
	t.Run("1MiB-without-LF", func(t *testing.T) {
		err := runIsolated(t, 60*time.Second, func(t *testing.T) {
			hs := newTestServer(t, hServerOpts{})
			c := hs.dial(t)
			payload := bytes.Repeat([]byte("a"), 1<<20)
			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)
			if err := c.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			_, werr := c.Conn.Write(payload)
			time.Sleep(50 * time.Millisecond) // let the server drain the socket
			runtime.GC()
			runtime.ReadMemStats(&m1)
			runtime.KeepAlive(payload)
			delta := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
			t.Logf("heap delta %d bytes, write err %v", delta, werr)

			var problems []string
			// The 16 MiB bound only guards against a blow-up: buffering a 1 MiB
			// line costs about 1 MiB, so this passes today. SEC-3 itself is
			// detected by the missing close below.
			if delta >= 16<<20 {
				problems = append(problems, fmt.Sprintf("heap grew by %d bytes (want < 16 MiB)", delta))
			}
			// RFC 3977 §3.1 (512-octet command lines): the server stops reading an
			// overlong line and closes the connection.
			switch {
			case werr != nil && sessIsTimeout(werr):
				problems = append(problems, "server stopped reading but kept the connection open")
			case werr != nil:
				// broken pipe / reset: already closed
			default:
				if err := sessExpectClosed(c, 2*time.Second); err != nil {
					problems = append(problems, "1 MiB line without LF: "+err.Error())
				}
			}
			if len(problems) > 0 {
				t.Fatal(strings.Join(problems, "; "))
			}
		})
		if sessIsChild(t) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------------------
// SEC-4: stats map growth

func TestSessStatsGrowth(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	c.Deadline = 30 * time.Second

	const n = 10000
	var buf strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&buf, "SESSJUNK%05d%s", i, CRLF)
	}

	done := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			code, line, err := c.readStatus()
			if err != nil {
				done <- fmt.Errorf("reply %d: %v (%q)", i, err, line)
				return
			}
			// RFC 3977 §3.2.1: unknown command → 500.
			if code != 500 {
				done <- fmt.Errorf("reply %d: %q, want 500", i, line)
				return
			}
		}
		done <- nil
	}()
	if err := c.send(buf.String()); err != nil {
		c.close()
		<-done
		t.Fatalf("send: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// SEC-4: per-command stats only track known commands (plus at most one bucket
	// for unknown ones), so junk input can't grow server memory without bound.
	counts := hs.S.Stats.GetAllCommandCounts()
	limit := len(sessKnownCommands) + 1
	var err error
	if len(counts) > limit {
		err = fmt.Errorf("stats map has %d entries after %d unique junk commands, want <= %d", len(counts), n, limit)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Idle timeout (BUG-9)

func TestSessIdleTimeout(t *testing.T) {
	t.Run("closes-idle-client", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{IdleTimeout: 200 * time.Millisecond})
		start := time.Now()
		c := hs.dial(t)
		// RFC 3977 §3.1: a server MAY have an inactivity timer; with 200 ms
		// configured, an idle client is disconnected.
		if err := sessExpectClosed(c, 3*time.Second); err != nil {
			t.Fatal(err)
		}
		if took := time.Since(start); took < 150*time.Millisecond {
			t.Fatalf("closed after %s, before the 200 ms idle timeout", took)
		}
	})
	t.Run("activity-resets-timer", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{IdleTimeout: 500 * time.Millisecond})
		c := hs.dial(t)
		// The timer measures inactivity: commands every 50 ms keep the
		// connection open past 500 ms (12 × 50 ms plus round trips), with room
		// for a stall of a few hundred ms on a loaded host.
		for i := 0; i < 12; i++ {
			time.Sleep(50 * time.Millisecond)
			code, line := c.cmd(t, "MODE READER")
			if err := hExpectCode(code, line, 200, 201); err != nil {
				t.Fatalf("command %d: %v", i, err)
			}
		}
	})
	t.Run("default-at-least-3min", func(t *testing.T) {
		db := hTestDB(t)
		var wg sync.WaitGroup
		cfg := &config.ServerConfig{}
		cfg.NNTP.MaxConns = 100
		s, err := NewNNTPServer(db, cfg, &wg, nil)
		if err != nil {
			t.Fatal(err)
		}
		// RFC 3977 §3.1: an inactivity timer SHOULD be at least three minutes.
		var res error
		if DefaultNNTPcliconnTimeout < 3*time.Minute || s.IdleTimeout < 3*time.Minute {
			res = fmt.Errorf("default idle timeout %s (server %s), want >= 3m", DefaultNNTPcliconnTimeout, s.IdleTimeout)
		}
		if res != nil {
			t.Fatal(res)
		}
	})
}

// ---------------------------------------------------------------------------
// MaxConns

func TestSessMaxConns(t *testing.T) {
	hs := newTestServer(t, hServerOpts{MaxConns: 2})
	c1 := hs.dial(t)
	c2 := hs.dial(t)
	for i, c := range []*hClient{c1, c2} {
		if code, err := hParseCode(c.Greeting); err != nil || (code != 200 && code != 201) {
			t.Fatalf("client %d greeting %q", i+1, c.Greeting)
		}
	}

	// The third connection is over the limit: it must not get a service greeting.
	c3, err := hs.tryDial(2 * time.Second)
	if err == nil {
		if code, perr := hParseCode(c3.Greeting); perr == nil && (code == 200 || code == 201) {
			t.Fatalf("third connection accepted with %q despite MaxConns=2", c3.Greeting)
		}
	}

	t.Run("slot-freed", func(t *testing.T) {
		c1.close()
		// After a client leaves, a new connection gets in (poll: ConnectionEnded runs async).
		end := time.Now().Add(3 * time.Second)
		for {
			c, err := hs.tryDial(time.Second)
			if err == nil {
				if code, perr := hParseCode(c.Greeting); perr == nil && (code == 200 || code == 201) {
					c.close()
					return
				}
				c.close()
			}
			if time.Now().After(end) {
				t.Fatalf("no connection accepted within 3s after a client left (last err %v)", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("rejection-status", func(t *testing.T) {
		// RFC 3977 §5.1.2: a server that refuses the client sends a 400 (service
		// temporarily unavailable) or 502 (service permanently unavailable)
		// greeting and then closes; not a silent close.
		var res error
		if err != nil {
			res = fmt.Errorf("third connection closed without a status line: %v", err)
		} else {
			code, perr := hParseCode(c3.Greeting)
			if perr != nil {
				res = perr
			} else {
				res = hExpectCode(code, c3.Greeting, 400, 502)
			}
		}
		if res != nil {
			t.Fatal(res)
		}
	})
}

// ---------------------------------------------------------------------------
// Stop (BUG-7)

func TestSessStop(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	if code, line := c.cmd(t, "MODE READER"); code != 200 && code != 201 {
		t.Fatalf("MODE READER: %q", line)
	}

	start := time.Now()
	if err := hs.S.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Stop took %s", took)
	}
	if hs.S.IsRunning() {
		t.Fatal("IsRunning() true after Stop")
	}

	// The listener is closed: new connections fail within 2 s.
	end := time.Now().Add(2 * time.Second)
	for {
		nc, err := hs.tryDial(500 * time.Millisecond)
		if err != nil {
			break
		}
		nc.close()
		if time.Now().After(end) {
			t.Fatal("listener still accepts connections 2s after Stop")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// BUG-7: Stop also ends live sessions within 2 s (optionally after a 400).
	if err := sessExpectClosed(c, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// TLS

func TestSessTLS(t *testing.T) {
	cfg, der := sessSelfSignedTLS(t)
	hs := newTestServer(t, hServerOpts{TLS: cfg})
	c := hs.dial(t)

	tc, ok := c.Conn.(*tls.Conn)
	if !ok {
		t.Fatalf("client conn is %T, want *tls.Conn", c.Conn)
	}
	st := tc.ConnectionState()
	if !st.HandshakeComplete {
		t.Fatal("TLS handshake not complete after greeting")
	}
	if len(st.PeerCertificates) == 0 || !bytes.Equal(st.PeerCertificates[0].Raw, der) {
		t.Fatal("server did not present the in-memory certificate")
	}
	// RFC 3977 §5.1.1 (and RFC 4642 for implicit TLS): the same 200/201 greeting.
	code, err := hParseCode(c.Greeting)
	if err != nil {
		t.Fatal(err)
	}
	if err := hExpectCode(code, c.Greeting, 200, 201); err != nil {
		t.Fatal(err)
	}
	caps := sessCaps(t, c)
	if len(caps) == 0 || caps[0] != "VERSION 2" {
		t.Fatalf("CAPABILITIES over TLS: %q", caps)
	}
	code, line := c.cmd(t, "QUIT")
	if err := hExpectCode(code, line, 205); err != nil {
		t.Fatal(err)
	}
	if err := sessExpectClosed(c, time.Second); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Units: Local430 and ServerStats

func TestSessLocal430(t *testing.T) {
	lc := &Local430{cache: make(map[string]time.Time)} // ttl 0 → DefaultLocal430TTL
	const id = "<t1-local430-1@test.invalid>"
	if lc.Check(id) {
		t.Fatal("empty cache reports a hit")
	}
	if !lc.Add(id) {
		t.Fatal("Add returned false")
	}
	if !lc.Check(id) {
		t.Fatal("Check misses an added id")
	}
	lc.Cleanup()
	if !lc.Check(id) {
		t.Fatal("Cleanup removed a fresh entry")
	}

	// Entries older than the TTL are removed by Cleanup, younger ones stay.
	const oldID, youngID = "<t1-local430-old@test.invalid>", "<t1-local430-young@test.invalid>"
	lc.mu.Lock()
	lc.cache[oldID] = time.Now().Add(-2 * DefaultLocal430TTL)
	lc.cache[youngID] = time.Now()
	lc.mu.Unlock()
	lc.Cleanup()
	lc.mu.RLock()
	_, oldKept := lc.cache[oldID]
	lc.mu.RUnlock()
	if oldKept {
		t.Error("Cleanup kept an entry older than the TTL")
	}
	if !lc.Check(youngID) || !lc.Check(id) {
		t.Error("Cleanup removed entries younger than the TTL")
	}

	// Check itself enforces the TTL (no wait for the cleanup) and drops the entry.
	const staleID = "<t1-local430-stale@test.invalid>"
	lc.mu.Lock()
	lc.cache[staleID] = time.Now().Add(-2 * DefaultLocal430TTL)
	lc.mu.Unlock()
	if lc.Check(staleID) {
		t.Error("Check reports an entry older than the TTL")
	}
	lc.mu.RLock()
	_, staleKept := lc.cache[staleID]
	lc.mu.RUnlock()
	if staleKept {
		t.Error("Check kept an expired entry")
	}

	// A per-cache ttl is honoured.
	short := &Local430{cache: make(map[string]time.Time), ttl: time.Millisecond}
	short.mu.Lock()
	short.cache[id] = time.Now().Add(-10 * time.Millisecond)
	short.mu.Unlock()
	if short.Check(id) {
		t.Error("ttl 1ms: a 10ms old entry is still a hit")
	}

	// Remove forgets an id; removing an absent id is a no-op.
	lc.Remove(id)
	if lc.Check(id) {
		t.Error("Check hits a removed id")
	}
	lc.Remove("<t1-local430-never@test.invalid>")

	// Message-ids over 250 octets are never cached.
	long := "<" + strings.Repeat("x", 249) + ">" // 251 octets
	if lc.Add(long) || lc.Check(long) {
		t.Error("a 251-octet message-id was cached")
	}
	max250 := "<" + strings.Repeat("y", 248) + ">" // 250 octets
	if !lc.Add(max250) || !lc.Check(max250) {
		t.Error("a 250-octet message-id was not cached")
	}

	// The cache stops growing at local430MaxEntries.
	full := &Local430{cache: make(map[string]time.Time, local430MaxEntries)}
	now := time.Now()
	for i := 0; i < local430MaxEntries; i++ {
		full.cache[fmt.Sprintf("<t1-full-%d@test.invalid>", i)] = now
	}
	if full.Add("<t1-full-extra@test.invalid>") {
		t.Error("Add accepted a new id into a full cache")
	}
	if !full.Add("<t1-full-0@test.invalid>") {
		t.Error("Add refused to refresh an id already in a full cache")
	}

	// Concurrent use is safe (run with -race).
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				mid := fmt.Sprintf("<t1-local430-%d-%d@test.invalid>", g, i)
				lc.Add(mid)
				_ = lc.Check(mid)
				if i%50 == 0 {
					lc.Cleanup()
				}
			}
		}(g)
	}
	wg.Wait()
	if !lc.Check("<t1-local430-7-199@test.invalid>") {
		t.Error("concurrently added id missing")
	}
}

func TestSessServerStats(t *testing.T) {
	t.Run("unit", func(t *testing.T) {
		s := NewServerStats()
		s.ConnectionStarted()
		s.ConnectionStarted()
		s.ConnectionStarted()
		s.ConnectionEnded()
		if got := s.GetActiveConnections(); got != 2 {
			t.Errorf("active %d, want 2", got)
		}
		if got := s.GetTotalConnections(); got != 3 {
			t.Errorf("total %d, want 3", got)
		}
		s.CommandExecuted("GROUP")
		s.CommandExecuted("GROUP")
		s.CommandExecuted("STAT")
		if got := s.GetCommandCount("GROUP"); got != 2 {
			t.Errorf("GROUP count %d, want 2", got)
		}
		if got := s.GetCommandCount("NOPE"); got != 0 {
			t.Errorf("unknown count %d, want 0", got)
		}
		all := s.GetAllCommandCounts()
		if len(all) != 2 || all["GROUP"] != 2 || all["STAT"] != 1 {
			t.Errorf("GetAllCommandCounts %v", all)
		}
		all["GROUP"] = 99 // a copy: must not change the stats
		if got := s.GetCommandCount("GROUP"); got != 2 {
			t.Errorf("GetAllCommandCounts returned the live map (GROUP now %d)", got)
		}
		s.AuthSuccess()
		s.AuthFailure()
		s.AuthFailure()
		if ok, bad := s.GetAuthStats(); ok != 1 || bad != 2 {
			t.Errorf("auth stats %d/%d, want 1/2", ok, bad)
		}
		if s.GetUptime() <= 0 {
			t.Error("uptime not positive")
		}
		s.Reset()
		if s.GetTotalConnections() != 0 || len(s.GetAllCommandCounts()) != 0 {
			t.Error("Reset kept totals or command counts")
		}
		if ok, bad := s.GetAuthStats(); ok != 0 || bad != 0 {
			t.Error("Reset kept auth stats")
		}
		// Live connections are still live after a reset.
		if got := s.GetActiveConnections(); got != 2 {
			t.Errorf("Reset changed active connections to %d", got)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		s := NewServerStats()
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 500; i++ {
					s.ConnectionStarted()
					s.CommandExecuted("MODE")
					_ = s.GetAllCommandCounts()
					s.ConnectionEnded()
				}
			}()
		}
		wg.Wait()
		if s.GetActiveConnections() != 0 || s.GetTotalConnections() != 4000 || s.GetCommandCount("MODE") != 4000 {
			t.Errorf("active %d total %d MODE %d", s.GetActiveConnections(), s.GetTotalConnections(), s.GetCommandCount("MODE"))
		}
	})
	t.Run("server", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		if got := hs.S.Stats.GetActiveConnections(); got != 1 {
			t.Errorf("active connections %d with one client, want 1", got)
		}
		sessCaps(t, c)
		if got := hs.S.Stats.GetCommandCount("CAPABILITIES"); got != 1 {
			t.Errorf("CAPABILITIES count %d, want 1", got)
		}
		code, line := c.cmd(t, "AUTHINFO USER t1user9%04d", hSeq.Add(1))
		if err := hExpectCode(code, line, 381); err != nil {
			t.Fatal(err)
		}
		code, line = c.cmd(t, "AUTHINFO PASS wrong")
		// RFC 4643 §2.3.2: 481 for rejected credentials.
		if err := hExpectCode(code, line, 481); err != nil {
			t.Fatal(err)
		}
		if _, bad := hs.S.Stats.GetAuthStats(); bad != 1 {
			t.Errorf("auth failures %d, want 1", bad)
		}
		c.close()
		end := time.Now().Add(2 * time.Second)
		for hs.S.Stats.GetActiveConnections() != 0 {
			if time.Now().After(end) {
				t.Fatalf("active connections %d 2s after the client left", hs.S.Stats.GetActiveConnections())
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got := hs.S.Stats.GetTotalConnections(); got != 1 {
			t.Errorf("total connections %d, want 1", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Fuzz: arbitrary command lines

// sessFuzzSkip lists verbs whose single line isn't self-contained: TAKETHIS
// reads an article after the line, LISTGROUP <name> creates group DBs (SEC-2,
// covered by the group tests).
var sessFuzzSkip = map[string]bool{"TAKETHIS": true, "LISTGROUP": true}

// sessFuzzMultiCodes are the status codes followed by a dot-terminated block
// (RFC 3977 §3.4.1: HELP 100, CAPABILITIES 101, LIST 215, ARTICLE 220,
// HEAD 221, BODY 222, OVER/XOVER 224, HDR/XHDR 225, NEWGROUPS 231; XHDR uses 221 too).
var sessFuzzMultiCodes = map[int]bool{100: true, 101: true, 215: true, 220: true, 221: true, 222: true, 224: true, 225: true, 231: true}

// sessFuzzServer starts a read-only server for a fuzz target (newTestServer
// needs a *testing.T); the cleanup stops it and waits at most 10 s.
func sessFuzzServer(f *testing.F) *hServer {
	f.Helper()
	if hDB == nil {
		f.Skipf("no test database: %s", hDBSkipReason)
	}
	cfg := &config.ServerConfig{}
	cfg.NNTP.MaxConns = 100
	hs := &hServer{}
	s, err := NewNNTPServer(hDB, cfg, &hs.wg, nil)
	if err != nil {
		f.Fatalf("NewNNTPServer: %v", err)
	}
	s.ArticleCmdDelay = 0
	s.GroupCmdDelay = 0
	s.ErrorDelay = 0
	hs.S = s
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.Fatalf("listen: %v", err)
	}
	hs.Addr = l.Addr().String()
	if err := s.Serve(l, false); err != nil {
		f.Fatalf("Serve: %v", err)
	}
	f.Cleanup(func() {
		_ = s.Stop()
		done := make(chan struct{})
		go func() { hs.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			f.Errorf("fuzz server goroutines did not finish within 10s after Stop")
		}
	})
	return hs
}

// sessDialRaw connects without tracking (fuzz inputs close their own conns).
func sessDialRaw(addr string, d time.Duration) (*hClient, error) {
	conn, err := net.DialTimeout("tcp", addr, d)
	if err != nil {
		return nil, err
	}
	c := &hClient{Conn: conn, R: bufio.NewReader(conn), Deadline: d}
	g, err := c.readLine()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("greeting: %w", err)
	}
	c.Greeting = g
	return c, nil
}

func FuzzSessCommandLine(f *testing.F) {
	for _, s := range []string{
		"", " ", "\t", "foo", "quit", "QUIT now", "MODE", "MODE READER", "mode stream", "MODE FOO",
		"GROUP", "GROUP t1.fuzz.none", "STAT", "STAT 1", "STAT <a@b>", "STAT <>", "HEAD 0", "BODY -1",
		"ARTICLE 9223372036854775807", "XOVER 1-9223372036854775807", "XHDR subject 1-9223372036854775807",
		"AUTHINFO", "AUTHINFO USER x", "AUTHINFO PASS y", "AUTHINFO SASL PLAIN", "POST", "IHAVE <x@y>",
		"CAPABILITIES", "HELP", "LIST", "LIST NEWSGROUPS", "LIST OVERVIEW.FMT", "DATE", "NEXT", "LAST",
		"\x00", "\xff\xfe", "ÄÖÜ äöü", "GROUP " + strings.Repeat("x", 600), strings.Repeat("A", 1000),
	} {
		f.Add(s)
	}
	hs := sessFuzzServer(f)
	f.Fuzz(func(t *testing.T, line string) {
		if strings.ContainsAny(line, "\r\n") || len(line) > 4094 {
			t.Skip("not a single command line")
		}
		verb := ""
		if fl := strings.Fields(line); len(fl) > 0 {
			verb = strings.ToUpper(fl[0])
		}
		if sessFuzzSkip[verb] {
			t.Skip("verb reads more than one line or creates group DBs")
		}
		c, err := sessDialRaw(hs.Addr, 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.close()

		// RFC 3977 §3.1/§3.2: every command line gets exactly one status line
		// (plus a dot-terminated block for multi-line responses).
		code, status, err := c.tryCmd("%s", line)
		if err != nil {
			t.Fatalf("%q: no status line: %v (%q)", line, err, status)
		}
		if sessFuzzMultiCodes[code] {
			c.readDotLines(t)
		}
		if verb == "QUIT" {
			// RFC 3977 §5.4: 205, then the connection closes.
			if err := hExpectCode(code, status, 205); err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			if err := sessExpectClosed(c, time.Second); err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			return
		}
		// HELP's 100 can't be an extra line of a single-line reply, so if the next
		// status line is 100 the fuzzed command produced exactly one reply.
		code, status, err = c.tryCmd("HELP")
		if err != nil {
			t.Fatalf("%q: connection unusable afterwards: %v (%q)", line, err, status)
		}
		if code != 100 {
			t.Fatalf("%q: extra line %q before the HELP reply", line, status)
		}
		c.readDotLines(t)
	})
}

// ---------------------------------------------------------------------------
// Own servers, pipes and ports (plan nntp-server-hardening, slice nntp-session)

// sessOwnServer builds a server like newTestServer but lets set change fields
// (ReadRequiresAuth, PlaintextAuth, EnforceUserMaxConns, ...) before Serve, so
// nothing races with a running server. tlsCfg != nil serves TLS.
func sessOwnServer(t *testing.T, proc ArticleProcessor, tlsCfg *tls.Config, set func(s *NNTPServer)) *hServer {
	t.Helper()
	db := hTestDB(t)
	cfg := &config.ServerConfig{}
	cfg.NNTP.MaxConns = 100
	hs := &hServer{TLS: tlsCfg}
	s, err := NewNNTPServer(db, cfg, &hs.wg, proc)
	if err != nil {
		t.Fatalf("NewNNTPServer: %v", err)
	}
	s.ArticleCmdDelay = 0
	s.GroupCmdDelay = 0
	s.ErrorDelay = 0
	if set != nil {
		set(s)
	}
	hs.S = s
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	isTLS := false
	if tlsCfg != nil {
		l = tls.NewListener(l, tlsCfg)
		isTLS = true
	}
	hs.Addr = l.Addr().String()
	if err := s.Serve(l, isTLS); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { hs.stop(t) })
	return hs
}

// sessPipeConn returns a ClientConnection on one end of a net.Pipe (server with
// default fields) and the other end for the test to write to.
func sessPipeConn(t *testing.T) (*ClientConnection, net.Conn) {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	c := NewClientConnection(serverEnd, &NNTPServer{}, false)
	t.Cleanup(func() {
		_ = serverEnd.Close()
		_ = clientEnd.Close()
	})
	return c, clientEnd
}

// sessFreePort returns a TCP port that was free a moment ago.
func sessFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return port
}

// sessWaitWG waits at most 10 s for wg (server goroutines after Stop).
func sessWaitWG(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Errorf("server goroutines did not finish within 10s after Stop")
	}
}

// ---------------------------------------------------------------------------
// Argument counts and echo (A-nntp-rfc-5, A-nntp-rfc-6)

func TestSessCommandArgs(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	// RFC 3977 §3.2.1: surplus arguments are a syntax error → 501. The table
	// rejects them before the handler, so no group is needed for XOVER/LISTGROUP.
	for _, line := range []string{"GROUP a b", "MODE READER x", "HELP x", "XOVER 1-2 x", "LISTGROUP g 1- x"} {
		code, reply := c.cmd(t, "%s", line)
		if err := hExpectCode(code, reply, 501); err != nil {
			t.Errorf("%q: %v", line, err)
		}
	}
	// Decision 13: QUIT ignores surplus arguments.
	code, reply := c.cmd(t, "QUIT now")
	if err := hExpectCode(code, reply, 205); err != nil {
		t.Fatalf("QUIT now: %v", err)
	}
	if err := sessExpectClosed(c, time.Second); err != nil {
		t.Fatalf("after QUIT now: %v", err)
	}
}

func TestSessNoEcho(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	junk := strings.Repeat("A", 400)
	code, line := c.cmd(t, "XYZZY%s", junk)
	if err := hExpectCode(code, line, 500); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, "XYZZY") || strings.Contains(line, "AAAAAAAA") {
		t.Errorf("500 reply echoes the command: %q", line)
	}
	code, line = c.cmd(t, "MODE FOO")
	if err := hExpectCode(code, line, 501); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, "FOO") {
		t.Errorf("MODE reply echoes the variant: %q", line)
	}
	code, line = c.cmd(t, "AUTHINFO FOOBAR x")
	if err := hExpectCode(code, line, 501); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, "FOOBAR") {
		t.Errorf("AUTHINFO reply echoes the subcommand: %q", line)
	}
	name := fmt.Sprintf("t1user%04dnoecho", hSeq.Add(1))
	code, line = c.cmd(t, "AUTHINFO USER %s", name)
	if err := hExpectCode(code, line, 381); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, name) {
		t.Errorf("381 reply echoes the username: %q", line)
	}
}

// ---------------------------------------------------------------------------
// readArticleBlock (SEC-3 article reader, wired by nntp-intake-injection)

func TestSessReadArticleBlock(t *testing.T) {
	// write sends data from the client end in the background (net.Pipe is
	// synchronous); the pipe cleanup unblocks a writer the reader abandoned.
	write := func(conn net.Conn, data string) {
		go func() { _, _ = io.WriteString(conn, data) }()
	}

	t.Run("normal", func(t *testing.T) {
		c, w := sessPipeConn(t)
		write(w, "Subject: a\r\nFrom: b\r\n\r\nbody1\r\n..dot\r\n\r\nlast\r\n.\r\nNEXT\r\n")
		head, body, err := c.readArticleBlock(0)
		if err != nil {
			t.Fatal(err)
		}
		if err := hExpectLines(head, []string{"Subject: a", "From: b"}); err != nil {
			t.Errorf("head: %v", err)
		}
		if err := hExpectLines(body, []string{"body1", ".dot", "", "last"}); err != nil {
			t.Errorf("body: %v", err)
		}
		if line, err := c.readCommandLine(); err != nil || line != "NEXT" {
			t.Errorf("next line %q, %v; want NEXT", line, err)
		}
	})
	t.Run("empty-body", func(t *testing.T) {
		c, w := sessPipeConn(t)
		write(w, "Subject: a\r\n\r\n.\r\n")
		head, body, err := c.readArticleBlock(0)
		if err != nil {
			t.Fatal(err)
		}
		if len(head) != 1 || len(body) != 0 {
			t.Errorf("head %q body %q, want 1 head line and no body", head, body)
		}
	})
	t.Run("long-line-inside-limit", func(t *testing.T) {
		c, w := sessPipeConn(t)
		long := strings.Repeat("x", 6000)
		write(w, "Subject: a\r\n\r\n"+long+"\r\n.\r\n")
		_, body, err := c.readArticleBlock(0)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body[0] != long {
			t.Errorf("body: %d lines; want one 6000-byte line", len(body))
		}
	})
	t.Run("over-limit", func(t *testing.T) {
		c, w := sessPipeConn(t)
		write(w, "Subject: a\r\n\r\n"+strings.Repeat("y", 300)+"\r\n"+strings.Repeat("z", 5000)+"\r\n.\r\nAFTER\r\n")
		head, body, err := c.readArticleBlock(100)
		if !errors.Is(err, ErrArticleTooLarge) {
			t.Fatalf("err %v, want ErrArticleTooLarge", err)
		}
		if head != nil || body != nil {
			t.Errorf("oversized article returned lines: %d head, %d body", len(head), len(body))
		}
		// the article was drained: the connection reads the next command
		if line, err := c.readCommandLine(); err != nil || line != "AFTER" {
			t.Errorf("next line %q, %v; want AFTER", line, err)
		}
	})
	t.Run("head-line-cap", func(t *testing.T) {
		c, w := sessPipeConn(t)
		var b strings.Builder
		for i := 0; i < 1100; i++ {
			fmt.Fprintf(&b, "X-H%d: v\r\n", i)
		}
		b.WriteString("\r\nbody\r\n.\r\nAFTER\r\n")
		write(w, b.String())
		if _, _, err := c.readArticleBlock(0); !errors.Is(err, ErrArticleTooLarge) {
			t.Fatalf("err %v, want ErrArticleTooLarge for 1100 head lines", err)
		}
		if line, err := c.readCommandLine(); err != nil || line != "AFTER" {
			t.Errorf("next line %q, %v; want AFTER", line, err)
		}
	})
	t.Run("drain-limit", func(t *testing.T) {
		c, w := sessPipeConn(t)
		bound := maxArticleDrainBytes(1000)
		line := strings.Repeat("d", 1000) + "\r\n"
		write(w, strings.Repeat(line, int(bound)/len(line)+16)) // no terminator
		if _, _, err := c.readArticleBlock(1000); !errors.Is(err, ErrArticleDrainLimit) {
			t.Fatalf("err %v, want ErrArticleDrainLimit", err)
		}
	})
	t.Run("drain-bound", func(t *testing.T) {
		for _, tc := range []struct{ limit, want int64 }{
			{1000, 1 << 20}, {1 << 20, 4 << 20}, {32 << 20, 64 << 20}, {1 << 40, 64 << 20},
		} {
			if got := maxArticleDrainBytes(tc.limit); got != tc.want {
				t.Errorf("maxArticleDrainBytes(%d) = %d, want %d", tc.limit, got, tc.want)
			}
		}
		if got := (&NNTPServer{}).MaxArticleBytes(); got != DefaultMaxArticleBytes {
			t.Errorf("MaxArticleBytes without config = %d, want %d", got, DefaultMaxArticleBytes)
		}
		cfg := &config.ServerConfig{}
		cfg.NNTP.MaxArtSize = 32768
		if got := (&NNTPServer{Config: cfg}).MaxArticleBytes(); got != 32768 {
			t.Errorf("MaxArticleBytes with MaxArtSize 32768 = %d", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Start validation and TLS (BUG-7, A-nntp-rfc-12, A-nntp-sec-5)

func TestSessStartErrors(t *testing.T) {
	db := hTestDB(t)
	t.Run("no-ports", func(t *testing.T) {
		var wg sync.WaitGroup
		cfg := &config.ServerConfig{}
		cfg.NNTP.MaxConns = 10
		s, err := NewNNTPServer(db, cfg, &wg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Start(); err == nil {
			_ = s.Stop()
			t.Fatal("Start without any port succeeded")
		}
		if s.IsRunning() {
			t.Fatal("IsRunning() after a failed Start")
		}
	})
	for _, tc := range []struct{ name, cert, key string }{
		{"tls-without-cert", "", ""},
		{"tls-missing-cert-file", "missing-cert.pem", "missing-key.pem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wg sync.WaitGroup
			dir := t.TempDir()
			cfg := &config.ServerConfig{}
			cfg.NNTP.MaxConns = 10
			cfg.NNTP.Port = sessFreePort(t)
			cfg.NNTP.TLSPort = sessFreePort(t)
			if tc.cert != "" {
				cfg.NNTP.TLSCert = filepath.Join(dir, tc.cert)
				cfg.NNTP.TLSKey = filepath.Join(dir, tc.key)
			}
			s, err := NewNNTPServer(db, cfg, &wg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Start(); err == nil {
				_ = s.Stop()
				t.Fatal("Start with a TLS port but no usable certificate succeeded")
			}
			if s.IsRunning() {
				t.Fatal("IsRunning() after a failed Start")
			}
			// the plain port was never bound
			l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.NNTP.Port))
			if err != nil {
				t.Fatalf("plain port %d still bound after a failed Start: %v", cfg.NNTP.Port, err)
			}
			_ = l.Close()
			sessWaitWG(t, &wg)
		})
	}
}

func TestSessTLSMinVersion(t *testing.T) {
	db := hTestDB(t)
	srvCfg, der := sessSelfSignedTLS(t)
	key, ok := srvCfg.Certificates[0].PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("private key is %T", srvCfg.Certificates[0].PrivateKey)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	cfg := &config.ServerConfig{}
	cfg.NNTP.MaxConns = 10
	cfg.NNTP.TLSPort = sessFreePort(t)
	cfg.NNTP.TLSCert = certFile
	cfg.NNTP.TLSKey = keyFile
	s, err := NewNNTPServer(db, cfg, &wg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Stop()
		sessWaitWG(t, &wg)
	})
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.NNTP.TLSPort)
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	// A TLS 1.1 client must fail the handshake (MinVersion TLS 1.2).
	old := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11} //nolint:gosec // test client
	if conn, err := tls.DialWithDialer(dialer, "tcp", addr, old); err == nil {
		_ = conn.Close()
		t.Fatal("TLS 1.1 handshake succeeded, want MinVersion TLS 1.2")
	}

	// A TLS 1.2+ client gets the greeting.
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // test client
	if err != nil {
		t.Fatalf("TLS 1.2+ dial: %v", err)
	}
	defer conn.Close()
	c := &hClient{Conn: conn, R: bufio.NewReader(conn), Deadline: 5 * time.Second}
	greeting, err := c.readLine()
	if err != nil {
		t.Fatalf("greeting: %v", err)
	}
	if code, perr := hParseCode(greeting); perr != nil || code != 201 {
		t.Fatalf("greeting %q, want 201 (read-only server)", greeting)
	}
}

// ---------------------------------------------------------------------------
// Access policy (decisions 1 and 4)

func TestSessReadRequiresAuth(t *testing.T) {
	user, pass := sessUser(t, false)
	hs := sessOwnServer(t, nil, nil, func(s *NNTPServer) { s.ReadRequiresAuth = true })
	c := hs.dial(t)
	missing := hGroupName("t1.readauth.none")
	// RFC 3977 §3.2.1 / RFC 4643 §2.1: 480 before authentication.
	for _, line := range []string{"GROUP " + missing, "LIST", "XOVER", "STAT 1"} {
		code, reply := c.cmd(t, "%s", line)
		if err := hExpectCode(code, reply, 480); err != nil {
			t.Errorf("%q before AUTHINFO: %v", line, err)
		}
	}
	// Session commands stay available.
	sessCaps(t, c)
	if code, reply := c.cmd(t, "MODE READER"); code != 201 {
		t.Errorf("MODE READER before AUTHINFO: %q", reply)
	}
	sessAuth(t, c, user, pass)
	code, reply := c.cmd(t, "GROUP %s", missing)
	if err := hExpectCode(code, reply, 211, 411); err != nil {
		t.Fatalf("GROUP after AUTHINFO: %v", err)
	}
}

func TestSessPlaintextPolicy(t *testing.T) {
	t.Run("refuse", func(t *testing.T) {
		hs := sessOwnServer(t, nil, nil, func(s *NNTPServer) { s.PlaintextAuth = PlaintextAuthRefuse })
		c := hs.dial(t)
		if caps := sessCaps(t, c); sessHasCap(caps, "AUTHINFO") {
			t.Errorf("refuse: AUTHINFO advertised on plaintext: %q", caps)
		}
		// RFC 4643 §2.3.1 / RFC 3977 §3.2.1: 483 encryption required.
		code, line := c.cmd(t, "AUTHINFO USER t1user-refuse")
		if err := hExpectCode(code, line, 483); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("accept", func(t *testing.T) {
		hs := sessOwnServer(t, nil, nil, nil)
		c := hs.dial(t)
		if caps := sessCaps(t, c); sessHasCap(caps, "AUTHINFO") {
			t.Errorf("accept: AUTHINFO advertised on plaintext: %q", caps)
		}
		code, line := c.cmd(t, "AUTHINFO USER t1user-accept")
		if err := hExpectCode(code, line, 381); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("advertise", func(t *testing.T) {
		hs := sessOwnServer(t, nil, nil, func(s *NNTPServer) { s.PlaintextAuth = PlaintextAuthAdvertise })
		if caps := sessCaps(t, hs.dial(t)); !sessHasCap(caps, "AUTHINFO") {
			t.Errorf("advertise: AUTHINFO missing on plaintext: %q", caps)
		}
	})
	t.Run("tls", func(t *testing.T) {
		user, pass := sessUser(t, true)
		cfg, _ := sessSelfSignedTLS(t)
		hs := sessOwnServer(t, nil, cfg, func(s *NNTPServer) { s.PlaintextAuth = PlaintextAuthRefuse })
		c := hs.dial(t)
		if caps := sessCaps(t, c); !sessHasCap(caps, "AUTHINFO") {
			t.Errorf("TLS: AUTHINFO missing before login: %q", caps)
		}
		sessAuth(t, c, user, pass)
		// RFC 4643 §2.2: not advertised after a successful login.
		if caps := sessCaps(t, c); sessHasCap(caps, "AUTHINFO") {
			t.Errorf("TLS: AUTHINFO still advertised after 281: %q", caps)
		}
	})
	t.Run("parse", func(t *testing.T) {
		for in, want := range map[string]PlaintextAuthPolicy{
			"accept": PlaintextAuthAccept, "ADVERTISE": PlaintextAuthAdvertise, " refuse ": PlaintextAuthRefuse,
		} {
			if got, err := ParsePlaintextAuthPolicy(in); err != nil || got != want {
				t.Errorf("ParsePlaintextAuthPolicy(%q) = %v, %v; want %v", in, got, err, want)
			}
		}
		if _, err := ParsePlaintextAuthPolicy("maybe"); err == nil {
			t.Error("ParsePlaintextAuthPolicy(maybe) succeeded")
		}
	})
}
