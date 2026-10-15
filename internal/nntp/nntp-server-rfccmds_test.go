package nntp

// Wire-level tests of the RFC 3977/4644/4642 commands through the real dispatch
// (plan nntp-rfc-commands, wave 2 slice rfc-wire: RFC-1 wire-level, RFC-1m).
// Every command goes through newTestServer/rfcNewCertServer and hs.dial, so it
// passes handleCommand, the commandMaxArgs table and the read gate exactly as a
// real client's command does. Identifiers start with rfc/TestRfc (contract K3);
// groups are rc6.<test>.<seq> (hGroup), users come from hUser.

import (
	"bufio"
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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
)

// rfcWriteCert writes a self-signed ECDSA P-256 certificate for
// 127.0.0.1/localhost and its key as PEM files into t.TempDir() (a copy of
// stlsWriteCert, kept here so this file is self-contained).
func rfcWriteCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "t6.rfcwire.test.invalid"},
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
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

// rfcServerTLS builds an implicit-TLS server config from the PEM files.
func rfcServerTLS(t *testing.T, certFile, keyFile string) *tls.Config {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

// rfcNewCertServer is newTestServer with Config.NNTP.TLSCert/TLSKey set before
// NewNNTPServer (zero delays, MaxConns 100). With tlsCfg != nil the listener is
// wrapped with tls.NewListener and served as implicit TLS (Serve(l, true)), and
// hs.dial connects with TLS. Nothing is written to the server after Serve.
func rfcNewCertServer(t *testing.T, certFile, keyFile string, tlsCfg *tls.Config, proc ArticleProcessor) *hServer {
	t.Helper()
	db := hTestDB(t)
	cfg := &config.ServerConfig{}
	cfg.NNTP.MaxConns = 100
	cfg.NNTP.TLSCert = certFile
	cfg.NNTP.TLSKey = keyFile
	hs := &hServer{TLS: tlsCfg}
	s, err := NewNNTPServer(db, cfg, &hs.wg, proc)
	if err != nil {
		t.Fatalf("NewNNTPServer: %v", err)
	}
	s.ArticleCmdDelay = 0
	s.GroupCmdDelay = 0
	s.ErrorDelay = 0
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

// rfcExpect fails the test unless the command answers with want; it returns the status line.
func rfcExpect(t *testing.T, c *hClient, want int, format string, args ...interface{}) string {
	t.Helper()
	code, line := c.cmd(t, format, args...)
	if err := hExpectCode(code, line, want); err != nil {
		t.Fatalf("%q: %v", format, err)
	}
	return line
}

// rfcCaps runs CAPABILITIES (101 + list) and returns the capability lines.
func rfcCaps(t *testing.T, c *hClient) []string {
	t.Helper()
	rfcExpect(t, c, 101, "CAPABILITIES")
	return c.readDotLines(t)
}

// rfcHasLine reports whether caps contains exactly line.
func rfcHasLine(caps []string, line string) bool {
	for _, l := range caps {
		if l == line {
			return true
		}
	}
	return false
}

// rfcHasLabel reports whether a capability line has label as its first word
// (case-insensitive, RFC 3977 §3.3.1).
func rfcHasLabel(caps []string, label string) bool {
	for _, l := range caps {
		if f := strings.Fields(l); len(f) > 0 && strings.EqualFold(f[0], label) {
			return true
		}
	}
	return false
}

// rfcAuth runs AUTHINFO USER/PASS and requires 381 then 281 (RFC 4643 §2.3).
func rfcAuth(t *testing.T, c *hClient, user, pass string) {
	t.Helper()
	rfcExpect(t, c, 381, "AUTHINFO USER %s", user)
	rfcExpect(t, c, 281, "AUTHINFO PASS %s", pass)
}

// rfcUpgrade runs the client side of the TLS handshake after a 382 and switches
// c to the TLS connection. It returns the *tls.Conn.
func rfcUpgrade(t *testing.T, c *hClient) *tls.Conn {
	t.Helper()
	return rfcUpgradeOver(t, c, c.Conn, 0)
}

// rfcUpgradeOver is rfcUpgrade with the TLS layer on raw (c.Conn or a wrapper of
// it) and an optional maximum TLS version (0: the default).
func rfcUpgradeOver(t *testing.T, c *hClient, raw net.Conn, maxVersion uint16) *tls.Conn {
	t.Helper()
	if c.R.Buffered() != 0 {
		t.Fatalf("client has %d unread bytes before the handshake", c.R.Buffered())
	}
	tc := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, MaxVersion: maxVersion}) //nolint:gosec // test client
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	c.Conn = tc
	c.R = bufio.NewReaderSize(tc, 1<<20)
	return tc
}

// rfcRecordingConn records every byte read from the wrapped connection.
type rfcRecordingConn struct {
	net.Conn
	got []byte
}

func (r *rfcRecordingConn) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	r.got = append(r.got, p[:n]...)
	return n, err
}

// rfcTLSRecordTypes splits a raw TLS byte stream into records and returns their
// content types (RFC 5246 §6.2.1: type, 2-byte version, 2-byte length). An
// incomplete last record is an error.
func rfcTLSRecordTypes(b []byte) ([]byte, error) {
	var types []byte
	for len(b) > 0 {
		if len(b) < 5 {
			return types, fmt.Errorf("truncated record header (%d bytes)", len(b))
		}
		n := int(b[3])<<8 | int(b[4])
		if len(b) < 5+n {
			return types, fmt.Errorf("truncated record: type %d, %d of %d bytes", b[0], len(b)-5, n)
		}
		types = append(types, b[0])
		b = b[5+n:]
	}
	return types, nil
}

// rfcDateRe is the DATE reply of RFC 3977 §7.1.2: "111 yyyymmddhhmmss".
var rfcDateRe = regexp.MustCompile(`^111 [0-9]{14}$`)

// rfcListOverviewFmt is the LIST OVERVIEW.FMT block (plan D2).
var rfcListOverviewFmt = []string{"Subject:", "From:", "Date:", "Message-ID:", "References:", ":bytes", ":lines"}

// rfcListHeaders is the LIST HEADERS block (plan D2).
var rfcListHeaders = []string{":", ":bytes", ":lines"}

func TestRfcDispatch(t *testing.T) {
	g := hGroup(t, "rc6.dispatch", 2, nil)
	proc := newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}})
	plain := newTestServer(t, hServerOpts{}) // no processor, no certificate

	t.Run("DATE", func(t *testing.T) {
		line := rfcExpect(t, plain.dial(t), 111, "DATE")
		if !rfcDateRe.MatchString(line) {
			t.Fatalf("DATE reply %q, want 111 yyyymmddhhmmss", line)
		}
	})
	t.Run("DATE-args", func(t *testing.T) {
		// commandMaxArgs["DATE"] = 0: the table answers before the handler
		line := rfcExpect(t, plain.dial(t), 501, "DATE x")
		if !strings.Contains(line, "too many arguments") {
			t.Fatalf("got %q, want the table's 501 (too many arguments)", line)
		}
	})
	t.Run("NEXT", func(t *testing.T) {
		rfcExpect(t, plain.dial(t), 412, "NEXT")
	})
	t.Run("LAST", func(t *testing.T) {
		rfcExpect(t, plain.dial(t), 412, "LAST")
	})
	t.Run("NEWGROUPS", func(t *testing.T) {
		c := plain.dial(t)
		rfcExpect(t, c, 231, "NEWGROUPS 20991231 235959 GMT")
		if lines := c.readDotLines(t); len(lines) != 0 {
			t.Fatalf("NEWGROUPS in the future listed %q, want an empty block", lines)
		}
	})
	t.Run("LIST-ACTIVE-wildmat", func(t *testing.T) {
		c := plain.dial(t)
		rfcExpect(t, c, 215, "LIST ACTIVE rc6.none.*")
		if lines := c.readDotLines(t); len(lines) != 0 {
			t.Fatalf("LIST ACTIVE rc6.none.* listed %q, want an empty block", lines)
		}
		rfcExpect(t, c, 215, "LIST ACTIVE %s", g)
		if lines := c.readDotLines(t); len(lines) != 1 || !strings.HasPrefix(lines[0], g+" ") {
			t.Fatalf("LIST ACTIVE %s listed %q, want exactly that group", g, lines)
		}
	})
	t.Run("LIST-ACTIVE-plain", func(t *testing.T) {
		// without a wildmat the hardened handleList answers with every active group
		c := plain.dial(t)
		rfcExpect(t, c, 215, "LIST ACTIVE")
		lines := c.readDotLines(t)
		found := false
		for _, l := range lines {
			found = found || strings.HasPrefix(l, g+" ")
		}
		if !found {
			t.Fatalf("plain LIST ACTIVE lacks %s (%d lines)", g, len(lines))
		}
	})
	t.Run("LIST-OVERVIEW.FMT", func(t *testing.T) {
		c := plain.dial(t)
		rfcExpect(t, c, 215, "LIST OVERVIEW.FMT")
		if err := hExpectLines(c.readDotLines(t), rfcListOverviewFmt); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("LIST-HEADERS", func(t *testing.T) {
		c := plain.dial(t)
		rfcExpect(t, c, 215, "LIST HEADERS")
		if err := hExpectLines(c.readDotLines(t), rfcListHeaders); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("OVER", func(t *testing.T) {
		rfcExpect(t, plain.dial(t), 412, "OVER")
	})
	t.Run("HDR", func(t *testing.T) {
		rfcExpect(t, plain.dial(t), 412, "HDR Subject")
	})
	t.Run("MODE-STREAM", func(t *testing.T) {
		line := rfcExpect(t, proc.dial(t), 203, "MODE STREAM")
		if !strings.Contains(line, "Streaming permitted") {
			t.Fatalf("got %q, want handleModeStream's 203", line)
		}
	})
	t.Run("MODE-READER", func(t *testing.T) {
		// the hardened handleMode still answers MODE READER
		line := rfcExpect(t, proc.dial(t), 200, "MODE READER")
		if !strings.Contains(line, "Reader mode") {
			t.Fatalf("got %q, want handleMode's reader reply", line)
		}
		rfcExpect(t, plain.dial(t), 201, "MODE READER")
	})
	t.Run("CHECK", func(t *testing.T) {
		line := rfcExpect(t, proc.dial(t), 480, "CHECK <x@y>")
		if !strings.HasPrefix(line, "480 <x@y> ") {
			t.Fatalf("got %q, want 480 carrying <x@y>", line)
		}
	})
	t.Run("CHECK-args", func(t *testing.T) {
		// commandMaxArgs["CHECK"] = 1: the table's 501 comes before the handler's
		// no-processor 500
		line := rfcExpect(t, plain.dial(t), 501, "CHECK a b")
		if !strings.Contains(line, "too many arguments") {
			t.Fatalf("got %q, want the table's 501 (too many arguments)", line)
		}
		rfcExpect(t, plain.dial(t), 500, "CHECK <x@y>") // and the handler answers 500 there
	})
	t.Run("STARTTLS", func(t *testing.T) {
		line := rfcExpect(t, plain.dial(t), 502, "STARTTLS")
		if !strings.Contains(line, "not available") {
			t.Fatalf("got %q, want handleStartTLS's 502 (not available)", line)
		}
	})
	t.Run("NEWNEWS", func(t *testing.T) {
		// decision D3: NEWNEWS is not implemented and stays unknown (RFC-1m)
		rfcExpect(t, proc.dial(t), 500, "NEWNEWS rc6.* 20000101 000000 GMT")
		rfcExpect(t, plain.dial(t), 500, "NEWNEWS rc6.* 20000101 000000 GMT")
	})
}

// rfcCapLIST is the extended LIST capability line of the plan.
const rfcCapLIST = "LIST ACTIVE NEWSGROUPS OVERVIEW.FMT HEADERS"

func TestRfcCapabilities(t *testing.T) {
	certFile, keyFile := rfcWriteCert(t)
	tlsCfg := rfcServerTLS(t, certFile, keyFile)
	postUser, postPass := hUser(t, true, true)
	readUser, readPass := hUser(t, false, true)

	base := []string{rfcCapLIST, "OVER MSGID", "HDR"}
	cases := []struct {
		name    string
		session func(t *testing.T) *hClient // returns the client in the state of the row
		present []string
		never   []string
	}{
		{
			name: "plaintext-before-auth-processor-cert",
			session: func(t *testing.T) *hClient {
				return rfcNewCertServer(t, certFile, keyFile, nil, &hFakeProcessor{}).dial(t)
			},
			present: append(append([]string{}, base...), "STREAMING", "STARTTLS"),
			never:   []string{"NEWNEWS", "TAKETHIS"},
		},
		{
			name: "plaintext-before-auth-no-cert",
			session: func(t *testing.T) *hClient {
				return newTestServer(t, hServerOpts{Proc: &hFakeProcessor{}}).dial(t)
			},
			present: append(append([]string{}, base...), "STREAMING"),
			never:   []string{"STARTTLS"},
		},
		{
			name: "after-auth-posting-user",
			session: func(t *testing.T) *hClient {
				c := rfcNewCertServer(t, certFile, keyFile, nil, &hFakeProcessor{}).dial(t)
				rfcAuth(t, c, postUser, postPass)
				return c
			},
			present: append(append([]string{}, base...), "STREAMING"),
			never:   []string{"STARTTLS"},
		},
		{
			name: "after-auth-user-without-posting",
			session: func(t *testing.T) *hClient {
				c := rfcNewCertServer(t, certFile, keyFile, nil, &hFakeProcessor{}).dial(t)
				rfcAuth(t, c, readUser, readPass)
				return c
			},
			present: base,
			never:   []string{"STREAMING", "STARTTLS"},
		},
		{
			name: "no-processor",
			session: func(t *testing.T) *hClient {
				return rfcNewCertServer(t, certFile, keyFile, nil, nil).dial(t)
			},
			present: append(append([]string{}, base...), "STARTTLS"),
			never:   []string{"STREAMING"},
		},
		{
			name: "tls-port",
			session: func(t *testing.T) *hClient {
				return rfcNewCertServer(t, certFile, keyFile, tlsCfg, &hFakeProcessor{}).dial(t)
			},
			present: append(append([]string{}, base...), "STREAMING"),
			never:   []string{"STARTTLS"},
		},
		{
			name: "after-starttls",
			session: func(t *testing.T) *hClient {
				c := rfcNewCertServer(t, certFile, keyFile, nil, &hFakeProcessor{}).dial(t)
				rfcExpect(t, c, 382, "STARTTLS")
				rfcUpgrade(t, c)
				return c
			},
			present: append(append([]string{}, base...), "STREAMING"),
			never:   []string{"STARTTLS"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := rfcCaps(t, tc.session(t))
			// RFC 3977 §5.2.2: VERSION first; READER unconditional (plan D9)
			if len(caps) < 2 || caps[0] != "VERSION 2" || caps[1] != "READER" {
				t.Fatalf("capabilities %q: want VERSION 2, READER first", caps)
			}
			for _, p := range tc.present {
				if !rfcHasLine(caps, p) {
					t.Errorf("capability %q missing from %q", p, caps)
				}
			}
			for _, n := range append([]string{"NEWNEWS"}, tc.never...) {
				if rfcHasLabel(caps, n) {
					t.Errorf("capability %q listed in %q", n, caps)
				}
			}
		})
	}
}

func TestRfcStreamingConsistent(t *testing.T) {
	postUser, postPass := hUser(t, true, true)
	readUser, readPass := hUser(t, false, true)
	for _, tc := range []struct {
		name      string
		proc      bool
		user      string
		pass      string
		streaming bool
	}{
		{"processor-unauthenticated", true, "", "", true},
		{"posting-user", true, postUser, postPass, true},
		{"user-without-posting", true, readUser, readPass, false},
		{"no-processor", false, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := hServerOpts{}
			if tc.proc {
				opts.Proc = &hFakeProcessor{}
			}
			c := newTestServer(t, opts).dial(t)
			if tc.user != "" {
				rfcAuth(t, c, tc.user, tc.pass)
			}
			caps := rfcCaps(t, c)
			code, line := c.cmd(t, "MODE STREAM")
			// RFC 4644 §2.2 / plan D5: STREAMING listed iff MODE STREAM gives 203
			has := rfcHasLine(caps, "STREAMING")
			if has != (code == 203) {
				t.Fatalf("STREAMING listed=%v but MODE STREAM answered %q", has, line)
			}
			if has != tc.streaming {
				t.Fatalf("STREAMING listed=%v, want %v (MODE STREAM %q)", has, tc.streaming, line)
			}
		})
	}
}

func TestRfcStartTLSWire(t *testing.T) {
	certFile, keyFile := rfcWriteCert(t)

	t.Run("plaintext-upgrade", func(t *testing.T) {
		hs := rfcNewCertServer(t, certFile, keyFile, nil, nil)
		user, pass := hUser(t, true, true)
		c := hs.dial(t)
		if caps := rfcCaps(t, c); !rfcHasLine(caps, "STARTTLS") {
			t.Fatalf("STARTTLS not listed before auth: %q", caps)
		}
		rfcExpect(t, c, 382, "STARTTLS")
		rfcUpgrade(t, c)
		caps := rfcCaps(t, c)
		if rfcHasLabel(caps, "STARTTLS") {
			t.Fatalf("STARTTLS still listed over TLS: %q", caps)
		}
		if !rfcHasLine(caps, "AUTHINFO USER") {
			t.Fatalf("AUTHINFO USER not listed over TLS: %q", caps)
		}
		line := rfcExpect(t, c, 502, "STARTTLS")
		if !strings.Contains(line, "active TLS layer") {
			t.Fatalf("second STARTTLS: got %q, want 502 (active TLS layer)", line)
		}
		rfcAuth(t, c, user, pass)
	})
	t.Run("implicit-tls", func(t *testing.T) {
		hs := rfcNewCertServer(t, certFile, keyFile, rfcServerTLS(t, certFile, keyFile), nil)
		c := hs.dial(t)
		if caps := rfcCaps(t, c); rfcHasLabel(caps, "STARTTLS") {
			t.Fatalf("STARTTLS listed on the TLS port: %q", caps)
		}
		rfcExpect(t, c, 502, "STARTTLS")
	})
	t.Run("quit-close-notify", func(t *testing.T) {
		// T-rfc-commands-2 (fixed in step A): after STARTTLS the server closes the
		// TLS layer on QUIT (close_notify) before the TCP connection. crypto/tls
		// reports io.EOF for a bare TCP close at a record boundary too, so the
		// client runs TLS 1.2, where an alert keeps its record type 21 in the clear,
		// and the raw stream after the 205 must hold that alert record.
		hs := rfcNewCertServer(t, certFile, keyFile, nil, nil)
		c := hs.dial(t)
		rfcExpect(t, c, 382, "STARTTLS")
		raw := &rfcRecordingConn{Conn: c.Conn}
		tc := rfcUpgradeOver(t, c, raw, tls.VersionTLS12)
		rfcExpect(t, c, 205, "QUIT")
		if n := c.R.Buffered(); n != 0 {
			t.Fatalf("%d unexpected bytes after 205", n)
		}
		_ = tc.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 64)
		if n, err := tc.Read(buf); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("after QUIT: read %d bytes %q, err %v; want 0 and io.EOF", n, buf[:n], err)
		}
		types, err := rfcTLSRecordTypes(raw.got)
		if err != nil {
			t.Fatalf("raw TLS stream: %v (types so far %v)", err, types)
		}
		const alert = 21 // RFC 5246 §6.2.1 ContentType alert
		if len(types) == 0 || types[len(types)-1] != alert {
			t.Fatalf("TLS connection ended without close_notify: record types %v, want an alert (21) last", types)
		}
	})
}

func TestRfcReaderHonest(t *testing.T) {
	g := hGroup(t, "rc6.honest", 3, nil)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	if caps := rfcCaps(t, c); !rfcHasLine(caps, "READER") {
		t.Fatalf("READER not listed: %q", caps)
	}
	// RFC 3977 §3.3.3: a server listing READER MUST implement DATE, NEXT, LAST
	// and NEWGROUPS. None of them may answer 500 (not recognized).
	nonUnknown := func(cmd string) (int, string) {
		t.Helper()
		code, line := c.cmd(t, "%s", cmd)
		if code == 500 {
			t.Fatalf("%s answered %q although READER is listed", strings.Fields(cmd)[0], line)
		}
		return code, line
	}
	if code, line := nonUnknown("DATE"); code != 111 {
		t.Fatalf("DATE: %q, want 111", line)
	}
	if code, line := nonUnknown("NEXT"); code != 412 {
		t.Fatalf("NEXT without a group: %q, want 412", line)
	}
	if code, line := nonUnknown("LAST"); code != 412 {
		t.Fatalf("LAST without a group: %q, want 412", line)
	}
	rfcExpect(t, c, 211, "GROUP %s", g)
	if code, line := nonUnknown("NEXT"); code != 223 || !strings.HasPrefix(line, "223 2 "+hMessageID(g, 2)) {
		t.Fatalf("NEXT: %q, want 223 2 %s", line, hMessageID(g, 2))
	}
	if code, line := nonUnknown("LAST"); code != 223 || !strings.HasPrefix(line, "223 1 "+hMessageID(g, 1)) {
		t.Fatalf("LAST: %q, want 223 1 %s", line, hMessageID(g, 1))
	}
	if code, line := nonUnknown("NEWGROUPS 20000101 000000 GMT"); code != 231 {
		t.Fatalf("NEWGROUPS: %q, want 231", line)
	}
	lines := c.readDotLines(t)
	found := false
	for _, l := range lines {
		found = found || strings.HasPrefix(l, g+" ")
	}
	if !found {
		t.Fatalf("NEWGROUPS since 2000 lacks %s (%d lines)", g, len(lines))
	}
}
