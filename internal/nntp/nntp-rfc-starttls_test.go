package nntp

// STARTTLS tests (plan nntp-rfc-commands, slice rfc-starttls, RFC-1n).
// Identifiers start with stls (contract K3). Commands are routed through the
// test-local stlsSession until wave 2 adds STARTTLS to the real dispatch.

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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
)

// stlsWriteCert writes a self-signed ECDSA P-256 certificate for
// 127.0.0.1/localhost and its key as PEM files into t.TempDir() (a copy of the
// generation in sessSelfSignedTLS, kept here so this file is self-contained).
func stlsWriteCert(t *testing.T) (certFile, keyFile string, der []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "t5.starttls.test.invalid"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
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
	return certFile, keyFile, der
}

// stlsNewServer is newTestServer with Config.NNTP.TLSCert/TLSKey set before
// NewNNTPServer (plain TCP, zero delays, MaxConns 100).
func stlsNewServer(t *testing.T, certFile, keyFile string, proc ArticleProcessor) *hServer {
	t.Helper()
	db := hTestDB(t)
	cfg := &config.ServerConfig{}
	cfg.NNTP.MaxConns = 100
	cfg.NNTP.TLSCert = certFile
	cfg.NNTP.TLSKey = keyFile
	hs := &hServer{}
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
	hs.Addr = l.Addr().String()
	if err := s.Serve(l, false); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { hs.stop(t) })
	return hs
}

// stlsSession serves one connection of hs.S: commands in extra go to the new
// handlers, everything else to the hardened dispatch (handleCommand).
func stlsSession(t *testing.T, hs *hServer, extra map[string]func(cc *ClientConnection, args []string) error) *hClient {
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

// stlsExtra routes STARTTLS to handleStartTLS.
var stlsExtra = map[string]func(cc *ClientConnection, args []string) error{
	"STARTTLS": func(cc *ClientConnection, args []string) error { return cc.handleStartTLS(args) },
}

// stlsUpgrade runs the client side of the TLS handshake on c and switches c to
// the TLS connection. It returns the peer's leaf certificate (DER).
func stlsUpgrade(t *testing.T, c *hClient) []byte {
	t.Helper()
	if c.R.Buffered() != 0 {
		t.Fatalf("client has %d unread bytes before the handshake", c.R.Buffered())
	}
	tc := tls.Client(c.Conn, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // test client
	_ = tc.SetDeadline(time.Now().Add(hDeadline))
	if err := tc.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	c.Conn = tc
	c.R = bufio.NewReaderSize(tc, 1<<20)
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatal("no peer certificate")
	}
	return certs[0].Raw
}

// stlsExpect fails the test unless the command answers with want.
func stlsExpect(t *testing.T, c *hClient, want int, format string, args ...interface{}) string {
	t.Helper()
	code, line := c.cmd(t, format, args...)
	if err := hExpectCode(code, line, want); err != nil {
		t.Fatalf("%q: %v", format, err)
	}
	return line
}

func TestStlsStartTLS(t *testing.T) {
	certFile, keyFile, der := stlsWriteCert(t)
	hs := stlsNewServer(t, certFile, keyFile, nil)
	g := hGroup(t, "rc5.start", 2, nil)
	user, pass := hUser(t, true, true)
	c := stlsSession(t, hs, stlsExtra)

	stlsExpect(t, c, 211, "GROUP %s", g)
	stlsExpect(t, c, 382, "STARTTLS")
	if got := stlsUpgrade(t, c); !bytes.Equal(got, der) {
		t.Fatal("peer certificate differs from the configured one")
	}
	// RFC 4642 §2.2.2: no greeting, the current group is forgotten.
	stlsExpect(t, c, 412, "STAT")
	stlsExpect(t, c, 502, "STARTTLS")
	stlsExpect(t, c, 381, "AUTHINFO USER %s", user)
	stlsExpect(t, c, 281, "AUTHINFO PASS %s", pass)
	stlsExpect(t, c, 205, "QUIT")
}

func TestStlsRefusals(t *testing.T) {
	t.Run("no-cert", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := stlsSession(t, hs, stlsExtra)
		line := stlsExpect(t, c, 502, "STARTTLS")
		if !strings.Contains(line, "not available") {
			t.Fatalf("got %q, want \"not available\"", line)
		}
	})
	t.Run("after-auth", func(t *testing.T) {
		certFile, keyFile, _ := stlsWriteCert(t)
		hs := stlsNewServer(t, certFile, keyFile, &hFakeProcessor{})
		user, pass := hUser(t, true, true)
		c := stlsSession(t, hs, stlsExtra)
		stlsExpect(t, c, 381, "AUTHINFO USER %s", user)
		stlsExpect(t, c, 281, "AUTHINFO PASS %s", pass)
		line := stlsExpect(t, c, 502, "STARTTLS")
		if !strings.Contains(line, "after authentication") {
			t.Fatalf("got %q, want \"after authentication\"", line)
		}
	})
	t.Run("args", func(t *testing.T) {
		certFile, keyFile, _ := stlsWriteCert(t)
		hs := stlsNewServer(t, certFile, keyFile, nil)
		c := stlsSession(t, hs, stlsExtra)
		stlsExpect(t, c, 501, "STARTTLS x")
		// the session is still plaintext and STARTTLS still works
		stlsExpect(t, c, 382, "STARTTLS")
		stlsUpgrade(t, c)
	})
	t.Run("cert-missing", func(t *testing.T) {
		dir := t.TempDir()
		hs := stlsNewServer(t, filepath.Join(dir, "missing.pem"), filepath.Join(dir, "missing.key"), nil)
		g := hGroup(t, "rc5.refuse", 1, nil)
		c := stlsSession(t, hs, stlsExtra)
		stlsExpect(t, c, 580, "STARTTLS")
		stlsExpect(t, c, 211, "GROUP %s", g) // same connection, still plaintext
		stlsExpect(t, c, 580, "STARTTLS")    // the load error is cached
	})
	t.Run("error-delay", func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{ErrorDelay: 300 * time.Millisecond})
		c := stlsSession(t, hs, stlsExtra)
		// Structural: the error-delay counter (incremented before the reply is sent)
		// must not move for the 502 and must move once for the 501. The 501 also
		// keeps its lower bound against the configured delay.
		delays := hs.S.ErrorDelayCount()
		stlsExpect(t, c, 502, "STARTTLS")
		if got := hs.S.ErrorDelayCount() - delays; got != 0 {
			t.Fatalf("502 reached the error delay %d times, want 0 (no error delay)", got)
		}
		start := time.Now()
		stlsExpect(t, c, 501, "STARTTLS x")
		if d := time.Since(start); d < 300*time.Millisecond {
			t.Fatalf("501 took %v, want the error delay (>= 300ms)", d)
		}
		if got := hs.S.ErrorDelayCount() - delays; got != 1 {
			t.Fatalf("502+501 reached the error delay %d times, want 1 (the 501 only)", got)
		}
	})
}

func TestStlsPipelinedDiscarded(t *testing.T) {
	certFile, keyFile, _ := stlsWriteCert(t)
	hs := stlsNewServer(t, certFile, keyFile, nil)
	g := hGroup(t, "rc5.pipe", 1, nil)
	c := stlsSession(t, hs, stlsExtra)

	if err := c.send("STARTTLS\r\nGROUP " + g + "\r\n"); err != nil {
		t.Fatal(err)
	}
	code, line, err := c.readStatus()
	if err != nil {
		t.Fatalf("STARTTLS reply: %v (%q)", err, line)
	}
	if err := hExpectCode(code, line, 382); err != nil {
		t.Fatal(err)
	}
	stlsUpgrade(t, c)
	// The pipelined GROUP must not have run: the first reply over TLS is 101.
	stlsExpect(t, c, 101, "CAPABILITIES")
	_ = c.readDotLines(t)
	stlsExpect(t, c, 412, "STAT") // and no group was selected
}

func TestStlsHandshakeFailure(t *testing.T) {
	certFile, keyFile, _ := stlsWriteCert(t)
	hs := stlsNewServer(t, certFile, keyFile, nil)
	c := stlsSession(t, hs, stlsExtra)

	stlsExpect(t, c, 382, "STARTTLS")
	if err := c.send("GARBAGE\r\n"); err != nil {
		t.Fatal(err)
	}
	// The server closes the connection without a reply; a TLS alert record may
	// arrive first, then EOF or reset.
	_ = c.Conn.SetReadDeadline(time.Now().Add(hDeadline))
	buf := make([]byte, 512)
	for {
		_, err := c.R.Read(buf)
		if err == nil {
			continue
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("server did not close the connection within %s", hDeadline)
		}
		break
	}
	// The server still accepts new connections.
	nc := hs.dial(t)
	if code, err := hParseCode(nc.Greeting); err != nil || (code != 200 && code != 201) {
		t.Fatalf("greeting after a failed handshake: %q (%v)", nc.Greeting, err)
	}
}

func TestStlsAvailable(t *testing.T) {
	certFile, keyFile, _ := stlsWriteCert(t)
	withCert := stlsNewServer(t, certFile, keyFile, nil)
	noCert := newTestServer(t, hServerOpts{})
	for _, tc := range []struct {
		name          string
		cert, isTLS   bool
		authenticated bool
		want          bool
	}{
		{"cert-plain", true, false, false, true},
		{"cert-plain-auth", true, false, true, false},
		{"cert-tls", true, true, false, false},
		{"cert-tls-auth", true, true, true, false},
		{"nocert-plain", false, false, false, false},
		{"nocert-plain-auth", false, false, true, false},
		{"nocert-tls", false, true, false, false},
		{"nocert-tls-auth", false, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
			hs := noCert
			if tc.cert {
				hs = withCert
			}
			cc := NewClientConnection(a, hs.S, tc.isTLS)
			cc.authenticated = tc.authenticated
			if got := cc.starttlsAvailable(); got != tc.want {
				t.Fatalf("starttlsAvailable() = %v, want %v", got, tc.want)
			}
		})
	}
}
