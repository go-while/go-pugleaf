package nntp

// Scripted fake NNTP server and fake streaming peer for the client tests of
// plan nntp-client-transfer (wave 0). This file is read-only for later waves:
// a test that needs other server behavior writes its own Handler.
//
// Identifiers: cliFake*, cliPeer*, cliDial, cliUnstuff and the TestCliFake*
// tests (contract K2). The server listens on 127.0.0.1:0 unless a test passes
// another address; it never contacts any host.

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
)

// cliFakeOpts configures a cliFakeServer.
type cliFakeOpts struct {
	Addr       string                // listen address, default "127.0.0.1:0"
	Greeting   string                // default "200 cli-fake ready"
	NoGreeting bool                  // no greeting; without Handler: never write (greeting stall), reads are still recorded
	RawStall   bool                  // accept TCP, never write (TLS-handshake / SOCKS stall); bytes read are discarded
	TLS        *tls.Config           // non-nil: TLS server on every accepted conn (cert from cliFakeTLSConfig)
	Script     []cliFakeStep         // run sequentially on every connection after the greeting
	Handler    func(fc *cliFakeConn) // replaces Script when set; the conn is closed when it returns
	// Precedence: RawStall wins over everything (no TLS, no greeting, no Script/Handler).
	// Otherwise the greeting is written unless NoGreeting; then Handler runs when set
	// (with NoGreeting it may write its own, possibly delayed, greeting), else Script
	// (with NoGreeting and no Handler the Script is not run: lines are only recorded).
}

// cliFakeStep is one scripted exchange.
//
// A client line that does not match the current step's Expect gets
// "500 cli-fake: unexpected <line>", is recorded in Mismatches() and the same
// step waits for the next line. After the last step the connection keeps
// reading and recording lines (no replies) until EOF/error, so Open() drops
// only when the client closes. Hang steps also keep reading (after their
// Delay and Reply, when set).
type cliFakeStep struct {
	Expect     string        // regexp, anchored ^…$, matched against the client line; "" matches any line
	ReadDot    bool          // after the match, read a dot-terminated block (POST/TAKETHIS payload)
	Delay      time.Duration // before replying
	Reply      []string      // lines written with CRLF; multi-line replies include their "." line
	Hang       bool          // after the match (and ReadDot, Delay, Reply), never reply again; held until Close
	CloseAfter bool          // close the connection after the reply
}

// cliFakeServer is a scripted NNTP server for client tests.
type cliFakeServer struct {
	Host string // listen IP, e.g. "127.0.0.1"
	Port int

	t         *testing.T
	o         cliFakeOpts
	re        []*regexp.Regexp // compiled Script expectations; nil = any line
	ln        net.Listener
	done      chan struct{} // closed by Close
	wg        sync.WaitGroup
	closeOnce sync.Once

	mu         sync.Mutex
	closed     bool
	conns      map[net.Conn]struct{}
	lines      []string
	articles   [][]string
	mismatches []string
	accepted   int
	open       int
}

// cliFakeConn is one server-side connection of a cliFakeServer.
type cliFakeConn struct {
	s    *cliFakeServer
	raw  net.Conn // the accepted TCP conn
	conn net.Conn // raw, or the TLS server conn on top of it
	r    *bufio.Reader
	gone bool // guarded by s.mu: counted out of Open()
}

// newCliFakeServer listens on o.Addr, serves every connection in its own
// goroutine and registers t.Cleanup(s.Close).
func newCliFakeServer(t *testing.T, o cliFakeOpts) *cliFakeServer {
	t.Helper()
	if o.Addr == "" {
		o.Addr = "127.0.0.1:0"
	}
	if o.Greeting == "" {
		o.Greeting = "200 cli-fake ready"
	}
	s := &cliFakeServer{t: t, o: o, done: make(chan struct{}), conns: make(map[net.Conn]struct{})}
	for i, st := range o.Script {
		if st.Expect == "" {
			s.re = append(s.re, nil)
			continue
		}
		re, err := regexp.Compile("^(?:" + st.Expect + ")$")
		if err != nil {
			t.Fatalf("cli-fake: step %d: bad Expect %q: %v", i, st.Expect, err)
		}
		s.re = append(s.re, re)
	}
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		t.Fatalf("cli-fake: listen %s: %v", o.Addr, err)
	}
	s.ln = ln
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		t.Fatalf("cli-fake: unexpected listener address %v", ln.Addr())
	}
	s.Host = tcp.IP.String()
	s.Port = tcp.Port
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

func (s *cliFakeServer) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return // listener closed by Close
			default:
			}
			time.Sleep(10 * time.Millisecond) // transient accept error: retry
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.accepted++
		s.open++
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(c)
	}
}

func (s *cliFakeServer) serve(raw net.Conn) {
	defer s.wg.Done()
	fc := &cliFakeConn{s: s, raw: raw, conn: raw}
	defer fc.finish()
	if s.o.RawStall {
		buf := make([]byte, 4096)
		for {
			if _, err := raw.Read(buf); err != nil {
				return
			}
		}
	}
	if s.o.TLS != nil {
		fc.conn = tls.Server(raw, s.o.TLS)
	}
	fc.r = bufio.NewReader(fc.conn)
	if !s.o.NoGreeting {
		if err := fc.WriteLines(s.o.Greeting); err != nil {
			return
		}
	} else if s.o.Handler == nil {
		fc.drain()
		return
	}
	if s.o.Handler != nil {
		s.o.Handler(fc)
		return
	}
	s.runScript(fc)
}

func (s *cliFakeServer) runScript(fc *cliFakeConn) {
	for i := 0; i < len(s.o.Script); {
		st := s.o.Script[i]
		line, err := fc.ReadLine()
		if err != nil {
			return
		}
		if re := s.re[i]; re != nil && !re.MatchString(line) {
			s.mu.Lock()
			s.mismatches = append(s.mismatches, line)
			s.mu.Unlock()
			if err := fc.WriteLines("500 cli-fake: unexpected " + line); err != nil {
				return
			}
			continue
		}
		if st.ReadDot {
			if _, err := fc.ReadDotBlock(); err != nil {
				return
			}
		}
		if st.Delay > 0 && !fc.sleep(st.Delay) {
			return
		}
		if len(st.Reply) > 0 {
			if err := fc.WriteLines(st.Reply...); err != nil {
				return
			}
		}
		if st.Hang {
			fc.drain()
			return
		}
		if st.CloseAfter {
			return
		}
		i++
	}
	fc.drain()
}

// cliFakeTLSConfig returns a server config with a fresh self-signed ECDSA
// P-256 certificate for 127.0.0.1/localhost (valid ±1h) and a pool that trusts it.
func cliFakeTLSConfig(t *testing.T) (server *tls.Config, clientRoots *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("cli-fake: ecdsa key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("cli-fake: serial: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "cli-fake"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cli-fake: certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("cli-fake: parse certificate: %v", err)
	}
	clientRoots = x509.NewCertPool()
	clientRoots.AddCert(cert)
	server = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}},
		MinVersion:   tls.VersionTLS12,
	}
	return server, clientRoots
}

// Backend returns a fresh client config for this server.
func (s *cliFakeServer) Backend() *BackendConfig {
	return &BackendConfig{
		Host:           s.Host,
		Port:           s.Port,
		MaxConns:       2,
		ConnectTimeout: 2 * time.Second,
		Provider:       &config.Provider{Name: "cli-fake", Host: s.Host, Port: s.Port},
	}
}

// Lines returns every client line (CRLF stripped) in arrival order, all connections.
// Payload lines read by ReadDotBlock are not included (see Articles).
func (s *cliFakeServer) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// Articles returns every dot block as raw wire lines (still dot-stuffed, without
// the "." terminator), in arrival order.
func (s *cliFakeServer) Articles() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.articles))
	for i, a := range s.articles {
		out[i] = append([]string{}, a...)
	}
	return out
}

// Mismatches returns the client lines that did not match the current step's Expect.
func (s *cliFakeServer) Mismatches() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.mismatches...)
}

// Accepted returns the number of accepted connections.
func (s *cliFakeServer) Accepted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// Open returns the connections whose server-side read has not seen EOF/error
// (and that the server has not closed itself).
func (s *cliFakeServer) Open() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open
}

// WaitOpen polls Open every 10 ms until it equals n; t.Fatalf after d.
func (s *cliFakeServer) WaitOpen(t *testing.T, n int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		got := s.Open()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cli-fake: Open() = %d, want %d within %v", got, n, d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Close closes the listener and every connection and waits up to 5 s for the
// accept and handler goroutines. Safe to call more than once.
func (s *cliFakeServer) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.done)
		_ = s.ln.Close()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		waited := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			s.t.Errorf("cli-fake: handler goroutines still running 5s after Close")
		}
	})
}

// ReadLine reads one client line (CRLF stripped) and records it in Lines().
func (fc *cliFakeConn) ReadLine() (string, error) {
	line, err := fc.readRaw()
	if err != nil {
		return "", err
	}
	fc.s.mu.Lock()
	fc.s.lines = append(fc.s.lines, line)
	fc.s.mu.Unlock()
	return line, nil
}

// WriteLines writes each line followed by CRLF in one Write.
func (fc *cliFakeConn) WriteLines(lines ...string) error {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	_, err := io.WriteString(fc.conn, b.String())
	return err
}

// ReadDotBlock reads lines until "." and records them (still dot-stuffed,
// without the terminator) in Articles(). A cut-off block is not recorded.
func (fc *cliFakeConn) ReadDotBlock() ([]string, error) {
	block := []string{}
	for {
		line, err := fc.readRaw()
		if err != nil {
			return block, err
		}
		if line == "." {
			break
		}
		block = append(block, line)
	}
	fc.s.mu.Lock()
	fc.s.articles = append(fc.s.articles, append([]string{}, block...))
	fc.s.mu.Unlock()
	return block, nil
}

// Raw returns the connection the server reads and writes (the TLS conn when TLS is set).
func (fc *cliFakeConn) Raw() net.Conn { return fc.conn }

// readRaw reads one line without recording it; a read error counts the conn out of Open().
func (fc *cliFakeConn) readRaw() (string, error) {
	line, err := fc.r.ReadString('\n')
	if err != nil {
		fc.markGone()
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r"), nil
}

// drain reads and records lines until EOF/error.
func (fc *cliFakeConn) drain() {
	for {
		if _, err := fc.ReadLine(); err != nil {
			return
		}
	}
}

// sleep waits d or until the server is closed; false when closed.
func (fc *cliFakeConn) sleep(d time.Duration) bool {
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
		return true
	case <-fc.s.done:
		return false
	}
}

func (fc *cliFakeConn) markGone() {
	fc.s.mu.Lock()
	if !fc.gone {
		fc.gone = true
		fc.s.open--
	}
	fc.s.mu.Unlock()
}

func (fc *cliFakeConn) finish() {
	fc.markGone()
	_ = fc.conn.Close()
	if fc.conn != fc.raw {
		_ = fc.raw.Close()
	}
	fc.s.mu.Lock()
	delete(fc.s.conns, fc.raw)
	fc.s.mu.Unlock()
}

// cliDial connects a BackendConn to s with s.Backend() after mutate. On
// success the connection is closed by t.Cleanup.
func cliDial(t *testing.T, s *cliFakeServer, mutate func(*BackendConfig)) (*BackendConn, error) {
	t.Helper()
	cfg := s.Backend()
	if mutate != nil {
		mutate(cfg)
	}
	c := NewConn(cfg)
	if err := c.Connect(); err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = c.CloseFromPoolOnly() })
	return c, nil
}

// cliUnstuff removes the dot-stuffing of raw wire lines (".." prefix -> ".").
func cliUnstuff(wire []string) []string {
	out := make([]string, len(wire))
	for i, l := range wire {
		if strings.HasPrefix(l, "..") {
			l = l[1:]
		}
		out[i] = l
	}
	return out
}

// cliPeerOpts configures cliPeerHandler, a fake streaming (RFC 4644) peer.
type cliPeerOpts struct {
	User, Pass      string          // non-empty: AUTHINFO required; CHECK/TAKETHIS before auth → 480
	Have            map[string]bool // ids the peer holds; guarded by the peer's mutex; TAKETHIS 239 adds to it
	Defer431Once    map[string]bool // CHECK of these ids → 431 the first time only
	StallModeStream time.Duration   // sleep before the first connection's "203" reply
	OnArticle       func(msgID string, wire []string)
}

// cliPeerHandler returns a Handler that answers like a streaming peer:
// CAPABILITIES, MODE STREAM/READER, AUTHINFO USER/PASS, CHECK, TAKETHIS, QUIT;
// anything else gets 500. Have is guarded by a mutex inside the handler: read
// it from a test only after the server is closed. OnArticle runs before the
// 239 reply is written, on the connection's goroutine.
func cliPeerHandler(o *cliPeerOpts) func(fc *cliFakeConn) {
	var mu sync.Mutex
	if o.Have == nil {
		o.Have = make(map[string]bool)
	}
	stallPending := o.StallModeStream > 0
	deferred := make(map[string]bool) // ids already answered 431 once
	needAuth := o.User != "" || o.Pass != ""
	return func(fc *cliFakeConn) {
		authed := !needAuth
		userOK := false
		for {
			line, err := fc.ReadLine()
			if err != nil {
				return
			}
			verb, arg := line, ""
			if i := strings.IndexByte(line, ' '); i >= 0 {
				verb, arg = line[:i], line[i+1:]
			}
			var reply []string
			switch strings.ToUpper(verb) {
			case "CAPABILITIES":
				reply = []string{"101 capability list follows", "VERSION 2", "READER", "STREAMING", "."}
			case "MODE":
				switch strings.ToUpper(arg) {
				case "STREAM":
					mu.Lock()
					stall := stallPending
					stallPending = false
					mu.Unlock()
					if stall && !fc.sleep(o.StallModeStream) {
						return
					}
					reply = []string{"203 streaming ok"}
				case "READER":
					reply = []string{"200 reader"}
				default:
					reply = []string{"500 cli-peer: unknown command"}
				}
			case "AUTHINFO":
				sub, val := arg, ""
				if i := strings.IndexByte(arg, ' '); i >= 0 {
					sub, val = arg[:i], arg[i+1:]
				}
				switch strings.ToUpper(sub) {
				case "USER":
					userOK = o.User == "" || val == o.User
					if userOK {
						reply = []string{"381 password required"}
					} else {
						reply = []string{"481 authentication failed"}
					}
				case "PASS":
					if userOK && (o.Pass == "" || val == o.Pass) {
						authed = true
						reply = []string{"281 authentication accepted"}
					} else {
						reply = []string{"481 authentication failed"}
					}
				default:
					reply = []string{"500 cli-peer: unknown command"}
				}
			case "CHECK":
				if !authed {
					reply = []string{"480 authentication required"}
					break
				}
				mu.Lock()
				switch {
				case o.Have[arg]:
					reply = []string{"438 " + arg}
				case o.Defer431Once[arg] && !deferred[arg]:
					deferred[arg] = true
					reply = []string{"431 " + arg}
				default:
					reply = []string{"238 " + arg}
				}
				mu.Unlock()
			case "TAKETHIS":
				wire, err := fc.ReadDotBlock()
				if err != nil {
					return
				}
				if !authed {
					reply = []string{"480 authentication required"}
					break
				}
				mu.Lock()
				held := o.Have[arg]
				o.Have[arg] = true
				mu.Unlock()
				if held {
					reply = []string{"439 " + arg}
					break
				}
				if o.OnArticle != nil {
					o.OnArticle(arg, wire)
				}
				reply = []string{"239 " + arg}
			case "QUIT":
				_ = fc.WriteLines("205 bye")
				return
			default:
				reply = []string{"500 cli-peer: unknown command"}
			}
			if err := fc.WriteLines(reply...); err != nil {
				return
			}
		}
	}
}

// cliFakeTextDial opens a raw textproto client to s with a 5 s I/O deadline
// (self-tests only).
func cliFakeTextDial(t *testing.T, s *cliFakeServer) (*textproto.Conn, net.Conn) {
	t.Helper()
	nc, err := net.DialTimeout("tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial cli-fake: %v", err)
	}
	_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
	tc := textproto.NewConn(nc)
	t.Cleanup(func() { _ = tc.Close() })
	return tc, nc
}

// cliFakeExpect sends cmd (when non-empty) and checks the reply code.
func cliFakeExpect(t *testing.T, tc *textproto.Conn, cmd string, want int) string {
	t.Helper()
	if cmd != "" {
		if err := tc.PrintfLine("%s", cmd); err != nil {
			t.Fatalf("send %q: %v", cmd, err)
		}
	}
	code, msg, err := tc.ReadCodeLine(want)
	if err != nil {
		t.Fatalf("%q: got %d %q, want %d: %v", cmd, code, msg, want, err)
	}
	return msg
}

// cliFakeWaitLines polls s.Lines() until it equals want (2 s).
func cliFakeWaitLines(t *testing.T, s *cliFakeServer, want []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := s.Lines()
		if reflect.DeepEqual(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Lines() = %q, want %q", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCliFakeScript(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "HELLO .*", Reply: []string{"280 hi"}},
		{Expect: "POST", ReadDot: true, Delay: 20 * time.Millisecond, Reply: []string{"240 ok"}},
	}})
	tc, _ := cliFakeTextDial(t, s)
	if msg := cliFakeExpect(t, tc, "", 200); msg != "cli-fake ready" {
		t.Fatalf("greeting text %q", msg)
	}
	cliFakeExpect(t, tc, "NOPE", 500)
	cliFakeExpect(t, tc, "HELLO world", 280)
	if err := tc.PrintfLine("POST"); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"a", "..b", "", "."} {
		if err := tc.PrintfLine("%s", l); err != nil {
			t.Fatal(err)
		}
	}
	cliFakeExpect(t, tc, "", 240)
	// after the last step: recorded, never answered
	if err := tc.PrintfLine("AFTER"); err != nil {
		t.Fatal(err)
	}
	cliFakeWaitLines(t, s, []string{"NOPE", "HELLO world", "POST", "AFTER"})
	if got := s.Mismatches(); !reflect.DeepEqual(got, []string{"NOPE"}) {
		t.Errorf("Mismatches() = %q", got)
	}
	arts := s.Articles()
	if len(arts) != 1 || !reflect.DeepEqual(arts[0], []string{"a", "..b", ""}) {
		t.Fatalf("Articles() = %q", arts)
	}
	if got := cliUnstuff(arts[0]); !reflect.DeepEqual(got, []string{"a", ".b", ""}) {
		t.Errorf("cliUnstuff = %q", got)
	}

	// CloseAfter: the server closes after the reply
	c := newCliFakeServer(t, cliFakeOpts{Greeting: "201 no posting", Script: []cliFakeStep{
		{Expect: "QUIT", Reply: []string{"205 bye"}, CloseAfter: true},
	}})
	tc2, _ := cliFakeTextDial(t, c)
	cliFakeExpect(t, tc2, "", 201)
	cliFakeExpect(t, tc2, "QUIT", 205)
	if l, err := tc2.ReadLine(); err == nil {
		t.Errorf("read after CloseAfter: %q, want EOF", l)
	}
	c.WaitOpen(t, 0, 2*time.Second)

	// Reply then Hang: the reply arrives, nothing follows, lines are still recorded
	hs := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "ARTICLE .*", Reply: []string{"220 0 <a@b>", "Path: x"}, Hang: true},
	}})
	tch, nch := cliFakeTextDial(t, hs)
	cliFakeExpect(t, tch, "", 200)
	if msg := cliFakeExpect(t, tch, "ARTICLE <a@b>", 220); msg != "0 <a@b>" {
		t.Errorf("220 text %q", msg)
	}
	if l, err := tch.ReadLine(); err != nil || l != "Path: x" {
		t.Fatalf("second reply line %q %v", l, err)
	}
	if err := tch.PrintfLine("STAT <a@b>"); err != nil {
		t.Fatal(err)
	}
	cliFakeWaitLines(t, hs, []string{"ARTICLE <a@b>", "STAT <a@b>"})
	_ = nch.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if l, err := tch.ReadLine(); err == nil {
		t.Errorf("Hang step wrote %q after its reply", l)
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Errorf("Hang step: read error %v, want timeout", err)
	}
	hs.WaitOpen(t, 1, 2*time.Second)

	// NoGreeting + Handler: the Handler writes its own greeting
	ng := newCliFakeServer(t, cliFakeOpts{NoGreeting: true, Handler: func(fc *cliFakeConn) {
		if !fc.sleep(20 * time.Millisecond) {
			return
		}
		if err := fc.WriteLines("201 own greeting"); err != nil {
			return
		}
		fc.drain()
	}})
	tcn, _ := cliFakeTextDial(t, ng)
	if msg := cliFakeExpect(t, tcn, "", 201); msg != "own greeting" {
		t.Errorf("handler greeting %q", msg)
	}

	// TLS with the self-signed config; the client trusts clientRoots
	srvCfg, roots := cliFakeTLSConfig(t)
	ts := newCliFakeServer(t, cliFakeOpts{TLS: srvCfg, Script: []cliFakeStep{{Expect: "PING", Reply: []string{"200 pong"}}}})
	d := &net.Dialer{Timeout: 2 * time.Second}
	tlsConn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(ts.Host, strconv.Itoa(ts.Port)),
		&tls.Config{RootCAs: roots, ServerName: ts.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
	tc3 := textproto.NewConn(tlsConn)
	defer tc3.Close()
	cliFakeExpect(t, tc3, "", 200)
	cliFakeExpect(t, tc3, "PING", 200)
}

func TestCliFakeOpenCount(t *testing.T) {
	// cliDial + BackendConn close observed by the server
	s := newCliFakeServer(t, cliFakeOpts{})
	c, err := cliDial(t, s, nil)
	if err != nil {
		t.Fatalf("cliDial: %v", err)
	}
	s.WaitOpen(t, 1, 2*time.Second)
	if err := c.CloseFromPoolOnly(); err != nil {
		t.Fatalf("CloseFromPoolOnly: %v", err)
	}
	s.WaitOpen(t, 0, 2*time.Second)
	if n := s.Accepted(); n != 1 {
		t.Errorf("Accepted() = %d, want 1", n)
	}

	// RawStall: no greeting, client close still observed
	r := newCliFakeServer(t, cliFakeOpts{RawStall: true})
	nc, err := net.DialTimeout("tcp", net.JoinHostPort(r.Host, strconv.Itoa(r.Port)), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nc.Write([]byte("hello\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = nc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if n, err := nc.Read(make([]byte, 16)); err == nil || n != 0 {
		t.Errorf("RawStall wrote %d bytes (err %v)", n, err)
	}
	r.WaitOpen(t, 1, 2*time.Second)
	_ = nc.Close()
	r.WaitOpen(t, 0, 2*time.Second)
	if got := r.Lines(); len(got) != 0 {
		t.Errorf("RawStall recorded lines %q", got)
	}

	// NoGreeting records lines; Hang holds the conn until Close
	g := newCliFakeServer(t, cliFakeOpts{NoGreeting: true})
	tcg, _ := cliFakeTextDial(t, g)
	if err := tcg.PrintfLine("CAPABILITIES"); err != nil {
		t.Fatal(err)
	}
	cliFakeWaitLines(t, g, []string{"CAPABILITIES"})
	g.WaitOpen(t, 1, 2*time.Second)

	h := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{{Expect: "ARTICLE .*", Hang: true}}})
	tch, _ := cliFakeTextDial(t, h)
	cliFakeExpect(t, tch, "", 200)
	if err := tch.PrintfLine("ARTICLE <a@b>"); err != nil {
		t.Fatal(err)
	}
	if err := tch.PrintfLine("STAT <a@b>"); err != nil {
		t.Fatal(err)
	}
	cliFakeWaitLines(t, h, []string{"ARTICLE <a@b>", "STAT <a@b>"})
	h.WaitOpen(t, 1, 2*time.Second)
	h.Close()
	if n := h.Open(); n != 0 {
		t.Errorf("Open() after Close = %d", n)
	}
	if l, err := tch.ReadLine(); err == nil {
		t.Errorf("read after Close: %q, want EOF", l)
	}
}

func TestCliFakePeerStream(t *testing.T) {
	var mu sync.Mutex
	var got []string
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(&cliPeerOpts{
		User:         "u",
		Pass:         "p",
		Defer431Once: map[string]bool{"<d@test.invalid>": true},
		OnArticle: func(id string, wire []string) {
			mu.Lock()
			got = append(got, id)
			mu.Unlock()
		},
	})})
	tc, _ := cliFakeTextDial(t, s)
	cliFakeExpect(t, tc, "", 200)
	cliFakeExpect(t, tc, "CHECK <x@test.invalid>", 480)
	cliFakeExpect(t, tc, "CAPABILITIES", 101)
	caps, err := tc.ReadDotLines()
	if err != nil || !reflect.DeepEqual(caps, []string{"VERSION 2", "READER", "STREAMING"}) {
		t.Fatalf("capabilities %q %v", caps, err)
	}
	cliFakeExpect(t, tc, "AUTHINFO USER u", 381)
	cliFakeExpect(t, tc, "AUTHINFO PASS p", 281)
	cliFakeExpect(t, tc, "MODE STREAM", 203)
	if msg := cliFakeExpect(t, tc, "CHECK <x@test.invalid>", 238); msg != "<x@test.invalid>" {
		t.Errorf("238 text %q", msg)
	}
	cliFakeExpect(t, tc, "CHECK <d@test.invalid>", 431)
	cliFakeExpect(t, tc, "CHECK <d@test.invalid>", 238)
	payload := []string{"Message-ID: <x@test.invalid>", "", "..dot", "body", ""}
	if err := tc.PrintfLine("TAKETHIS <x@test.invalid>"); err != nil {
		t.Fatal(err)
	}
	for _, l := range append(payload, ".") {
		if err := tc.PrintfLine("%s", l); err != nil {
			t.Fatal(err)
		}
	}
	cliFakeExpect(t, tc, "", 239)
	cliFakeExpect(t, tc, "CHECK <x@test.invalid>", 438)
	if err := tc.PrintfLine("TAKETHIS <x@test.invalid>"); err != nil {
		t.Fatal(err)
	}
	if err := tc.PrintfLine("."); err != nil {
		t.Fatal(err)
	}
	cliFakeExpect(t, tc, "", 439)
	cliFakeExpect(t, tc, "XYZZY", 500)
	cliFakeExpect(t, tc, "QUIT", 205)
	s.WaitOpen(t, 0, 2*time.Second)

	arts := s.Articles()
	if len(arts) != 2 || !reflect.DeepEqual(arts[0], payload) || len(arts[1]) != 0 {
		t.Fatalf("Articles() = %q", arts)
	}
	if body := cliUnstuff(arts[0]); body[2] != ".dot" {
		t.Errorf("unstuffed %q", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, []string{"<x@test.invalid>"}) {
		t.Errorf("OnArticle ids %q", got)
	}
	if n := len(s.Mismatches()); n != 0 {
		t.Errorf("Mismatches() = %d", n)
	}
}
