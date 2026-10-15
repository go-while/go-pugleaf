package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// adfOptTorBackend starts a TCP server on 127.0.0.1:0 that runs handle for every connection,
// standing in for the hidden service behind the SOCKS5 proxy. Cleanup closes everything and
// waits for the handlers.
func adfOptTorBackend(t *testing.T, handle func(c *net.TCPConn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				handle(c.(*net.TCPConn))
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

// adfOptTorFakeSOCKS starts a minimal SOCKS5 server (no auth, CONNECT only) on 127.0.0.1:0 that
// ignores the requested address and connects every request to backendAddr. It forwards
// half-closes in both directions like a real Tor client.
func adfOptTorFakeSOCKS(t *testing.T, backendAddr string) *ProxyConfig {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	track := func(c net.Conn) {
		mu.Lock()
		conns = append(conns, c)
		mu.Unlock()
	}
	serve := func(c *net.TCPConn) {
		defer func() { _ = c.Close() }()
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
			return
		}
		if _, err := io.ReadFull(c, make([]byte, int(hdr[1]))); err != nil {
			return
		}
		if _, err := c.Write([]byte{5, 0}); err != nil {
			return
		}
		req := make([]byte, 4)
		if _, err := io.ReadFull(c, req); err != nil || req[1] != 1 {
			return
		}
		var addrLen int
		switch req[3] {
		case 1:
			addrLen = 4
		case 4:
			addrLen = 16
		case 3:
			l := make([]byte, 1)
			if _, err := io.ReadFull(c, l); err != nil {
				return
			}
			addrLen = int(l[0])
		default:
			return
		}
		if _, err := io.ReadFull(c, make([]byte, addrLen+2)); err != nil {
			return
		}
		bc, err := net.Dial("tcp", backendAddr)
		if err != nil {
			_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		b := bc.(*net.TCPConn)
		track(b)
		defer func() { _ = b.Close() }()
		if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.Copy(b, c)
			_ = b.CloseWrite()
		}()
		_, _ = io.Copy(c, b)
		_ = c.CloseWrite()
		<-done
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			track(c)
			wg.Add(1)
			go func() {
				defer wg.Done()
				serve(c.(*net.TCPConn))
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return &ProxyConfig{SocksHost: "127.0.0.1", SocksPort: ln.Addr().(*net.TCPAddr).Port}
}

// adfOptTorTCPDial returns a dialTarget func that connects straight to addr (no SOCKS5).
func adfOptTorTCPDial(addr string, calls chan<- struct{}) func(ctx context.Context, _ string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		if calls != nil {
			calls <- struct{}{}
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
}

// adfOptTorStart binds s on 127.0.0.1:0, runs Serve and returns the listen address and a
// channel that is closed when Serve returns. Cleanup stops the server, waits for Serve and
// fails the test when Serve returned an error.
func adfOptTorStart(t *testing.T, s *ProxyServer) (string, <-chan struct{}) {
	t.Helper()
	s.ListenAddr = "127.0.0.1:0"
	if s.ProxyConfig == nil {
		s.ProxyConfig = &ProxyConfig{SocksHost: "127.0.0.1", SocksPort: 9}
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	finished := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = s.Serve()
		close(finished)
	}()
	t.Cleanup(func() {
		s.Stop()
		select {
		case <-finished:
			if serveErr != nil {
				t.Errorf("Serve: %v", serveErr)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Serve did not return within 5s after Stop")
		}
	})
	return s.Addr().String(), finished
}

// adfOptTorExpectClosed reads from c and fails unless the peer closes it within limit.
func adfOptTorExpectClosed(t *testing.T, c net.Conn, limit time.Duration) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(limit)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, err := io.Copy(io.Discard, c)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("connection still open after %v", limit)
	}
}

func TestAdfOptTorCheckListenHost(t *testing.T) {
	cases := []struct {
		host        string
		allowPublic bool
		ok          bool
	}{
		{"127.2.3.4", false, true},
		{"127.0.0.1", false, true},
		{"localhost", false, true},
		{"LOCALHOST", false, true},
		{"::1", false, true},
		{"[::1]", false, true},
		{"0.0.0.0", false, false},
		{"", false, false},
		{"::", false, false},
		{"192.168.1.100", false, false},
		{"news.example.org", false, false},
		{"0.0.0.0", true, true},
		{"192.168.1.100", true, true},
	}
	for _, tc := range cases {
		err := checkListenHost(tc.host, tc.allowPublic)
		if (err == nil) != tc.ok {
			t.Errorf("checkListenHost(%q, %v) = %v, want ok=%v", tc.host, tc.allowPublic, err, tc.ok)
		}
	}
}

func TestAdfOptTorParseConfigSettings(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	good := write("good.cfg", strings.Join([]string{
		"# comment",
		"127.2.3.4:11119:a.onion:119 # news",
		"127.2.3.5:11120:c.onion:119 # key=foo is a comment, not a setting",
		"max_conns = 8",
		"IDLE_TIMEOUT=90s   # comment",
		"allow_public_bind = true",
		"0.0.0.0:3119:b.onion:119",
		"",
	}, "\n"))
	configs, fs, err := parseConfigFile(good, "", "127.0.0.1", 9050, "", "")
	if err != nil {
		t.Fatalf("parseConfigFile: %v", err)
	}
	if len(configs) != 3 {
		t.Fatalf("got %d entries, want 3", len(configs))
	}
	if fs.MaxConns == nil || *fs.MaxConns != 8 {
		t.Errorf("max_conns = %v, want 8", fs.MaxConns)
	}
	if fs.IdleTimeout == nil || *fs.IdleTimeout != 90*time.Second {
		t.Errorf("idle_timeout = %v, want 90s", fs.IdleTimeout)
	}
	if fs.AllowPublicBind == nil || !*fs.AllowPublicBind {
		t.Errorf("allow_public_bind = %v, want true", fs.AllowPublicBind)
	}

	// File keys override flag defaults; explicitly set flags win over the file.
	base := serverLimits{MaxConns: defaultMaxConns, IdleTimeout: defaultIdleTimeout}
	got := fs.apply(base, map[string]bool{})
	if got.MaxConns != 8 || got.IdleTimeout != 90*time.Second || !got.AllowPublicBind {
		t.Errorf("apply without explicit flags = %+v", got)
	}
	got = fs.apply(serverLimits{MaxConns: 3, IdleTimeout: time.Minute}, map[string]bool{
		"max-conns": true, "idle-timeout": true, "allow-public-bind": true,
	})
	if got.MaxConns != 3 || got.IdleTimeout != time.Minute || got.AllowPublicBind {
		t.Errorf("apply with explicit flags = %+v", got)
	}

	// Plain seconds are accepted for idle_timeout.
	secs := write("secs.cfg", "idle_timeout = 120\n127.0.0.1:1119:a.onion:119\n")
	if _, fs, err := parseConfigFile(secs, "", "127.0.0.1", 9050, "", ""); err != nil || fs.IdleTimeout == nil || *fs.IdleTimeout != 2*time.Minute {
		t.Errorf("idle_timeout = 120: fs=%v err=%v, want 2m", fs.IdleTimeout, err)
	}

	bad := map[string]string{
		"unknown":   "foo = 1\n",
		"duplicate": "max_conns = 2\nmax_conns = 3\n",
		"zero":      "max_conns = 0\n",
		"huge":      "max_conns = 100000\n",
		"idle-zero": "idle_timeout = 0s\n",
		"idle-junk": "idle_timeout = soon\n",
		"bool-junk": "allow_public_bind = maybe\n",
		"empty":     "max_conns =\n",
	}
	for name, content := range bad {
		p := write(name+".cfg", content+"127.0.0.1:1119:a.onion:119\n")
		if _, _, err := parseConfigFile(p, "", "127.0.0.1", 9050, "", ""); err == nil {
			t.Errorf("%s: parseConfigFile accepted %q", name, content)
		}
	}

	if err := (serverLimits{MaxConns: 0, IdleTimeout: time.Second}).validate(); err == nil {
		t.Errorf("validate accepted MaxConns 0")
	}
	if err := (serverLimits{MaxConns: 1, IdleTimeout: 0}).validate(); err == nil {
		t.Errorf("validate accepted IdleTimeout 0")
	}
}

func TestAdfOptTorStopBeforeListen(t *testing.T) {
	s := &ProxyServer{ListenAddr: "127.0.0.1:0", ProxyConfig: &ProxyConfig{SocksHost: "127.0.0.1", SocksPort: 9}}
	s.Stop()
	s.Stop() // second Stop must not panic
	if err := s.Listen(); !errors.Is(err, errServerStopped) {
		t.Fatalf("Listen after Stop = %v, want errServerStopped", err)
	}
	if err := s.Serve(); err != nil {
		t.Fatalf("Serve after Stop = %v, want nil", err)
	}
}

func TestAdfOptTorListenStopServe(t *testing.T) {
	s := &ProxyServer{ListenAddr: "127.0.0.1:0", ProxyConfig: &ProxyConfig{SocksHost: "127.0.0.1", SocksPort: 9}}
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := s.Listen(); err == nil {
		t.Fatalf("second Listen succeeded")
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()

	// Concurrent Stops race with Serve; none may panic.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Stop()
		}()
	}
	wg.Wait()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Serve still running 1s after Stop")
	}
	s.Stop()
}

func TestAdfOptTorMaxConns(t *testing.T) {
	backend := adfOptTorBackend(t, func(c *net.TCPConn) {
		_, _ = io.Copy(c, c) // echo until the client side ends, then close
		_ = c.Close()
	})
	calls := make(chan struct{}, 4)
	s := &ProxyServer{MaxConns: 1, IdleTimeout: 10 * time.Second, Timeout: time.Second,
		dialTarget: adfOptTorTCPDial(backend, calls)}
	addr, _ := adfOptTorStart(t, s)

	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer func() { _ = c1.Close() }()
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatalf("first connection was not forwarded")
	}

	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer func() { _ = c2.Close() }()
	adfOptTorExpectClosed(t, c2, 2*time.Second)
	select {
	case <-calls:
		t.Fatalf("second connection was forwarded despite MaxConns=1")
	default:
	}

	// The first connection still works.
	if _, err := c1.Write([]byte("ping")); err != nil {
		t.Fatalf("write c1: %v", err)
	}
	buf := make([]byte, 4)
	_ = c1.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c1, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo on c1 = %q, %v", buf, err)
	}

	// Closing it frees the slot.
	_ = c1.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c3, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial 3: %v", err)
		}
		select {
		case <-calls:
			_ = c3.Close()
			return
		case <-time.After(200 * time.Millisecond):
			_ = c3.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot was not released after the first connection closed")
		}
	}
}

func TestAdfOptTorIdleTimeout(t *testing.T) {
	backendClosed := make(chan struct{}, 1)
	backend := adfOptTorBackend(t, func(c *net.TCPConn) {
		_, _ = io.Copy(io.Discard, c) // silent; returns when tcp2tor closes the connection
		backendClosed <- struct{}{}
	})
	s := &ProxyServer{IdleTimeout: 300 * time.Millisecond, Timeout: time.Second,
		dialTarget: adfOptTorTCPDial(backend, nil)}
	addr, _ := adfOptTorStart(t, s)

	// start is taken before the dial, so every deadline the handler sets is later than start
	// and the close cannot come earlier than one idle timeout after it.
	start := time.Now()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	adfOptTorExpectClosed(t, c, 3*time.Second)
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("idle connection closed after %v, before the 300ms idle timeout", elapsed)
	}
	select {
	case <-backendClosed:
	case <-time.After(2 * time.Second):
		t.Fatalf("target side was not closed after the idle timeout")
	}
}

// One-way traffic keeps the connection alive: the silent direction must not time out while the
// other one moves data. Chunks come every 100 ms (10x margin to the 1 s idle timeout) for 3 s,
// so the silent direction's read deadline fires and is retried several times.
func TestAdfOptTorIdleOneWayTraffic(t *testing.T) {
	const chunks = 30
	retriesBefore := idleRetries.Load()
	backend := adfOptTorBackend(t, func(c *net.TCPConn) {
		for i := 0; i < chunks; i++ {
			if _, err := c.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = c.CloseWrite()
		_, _ = io.Copy(io.Discard, c)
	})
	s := &ProxyServer{IdleTimeout: time.Second, Timeout: time.Second,
		dialTarget: adfOptTorTCPDial(backend, nil)}
	addr, _ := adfOptTorStart(t, s)

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second)) // hang guard: 10x the ~3 s stream
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != chunks {
		t.Fatalf("got %d bytes, want %d (connection closed while the other direction was active)", len(got), chunks)
	}
	if idleRetries.Load()-retriesBefore < 1 {
		t.Fatalf("the silent direction's idle deadline never fired and was retried")
	}
}

// After the client half-closes, a reply that streams for longer than the idle timeout (but
// never pauses for it) must arrive completely: no total drain limit may cut it off.
func TestAdfOptTorHalfCloseLongReply(t *testing.T) {
	const chunks = 30 // 3 s of reply, 100 ms apart, idle timeout 1 s
	backend := adfOptTorBackend(t, func(c *net.TCPConn) {
		if _, err := io.ReadAll(c); err != nil { // until the client's half-close arrives
			return
		}
		for i := 0; i < chunks; i++ {
			if _, err := c.Write([]byte("r")); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = c.CloseWrite()
	})
	s := &ProxyServer{IdleTimeout: time.Second, Timeout: time.Second,
		dialTarget: adfOptTorTCPDial(backend, nil)}
	addr, _ := adfOptTorStart(t, s)

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("ARTICLE 1\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second)) // hang guard: 10x the ~3 s stream
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read reply: %v (got %d bytes)", err, len(got))
	}
	if len(got) != chunks {
		t.Fatalf("got %d of %d reply bytes after half-close", len(got), chunks)
	}
}

// adfOptTorHalfCloseCheck sends a request, half-closes the client side and expects the full
// reply that the backend writes only after it has seen EOF.
func adfOptTorHalfCloseCheck(t *testing.T, addr string, reply []byte) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("QUIT\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read reply: %v (got %d bytes)", err, len(got))
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("reply = %d bytes, want %d bytes (lost after half-close)", len(got), len(reply))
	}
}

func adfOptTorReplyAfterEOF(reply []byte, gotReq chan<- string) func(c *net.TCPConn) {
	return func(c *net.TCPConn) {
		req, err := io.ReadAll(c) // until the client's half-close arrives
		if err != nil {
			return
		}
		gotReq <- string(req)
		time.Sleep(100 * time.Millisecond) // reply arrives well after the client's EOF
		_, _ = c.Write(reply)
		_ = c.CloseWrite()
	}
}

func TestAdfOptTorHalfClose(t *testing.T) {
	reply := append([]byte("205 bye\r\n"), bytes.Repeat([]byte("z"), 256*1024)...)
	gotReq := make(chan string, 1)
	backend := adfOptTorBackend(t, adfOptTorReplyAfterEOF(reply, gotReq))
	s := &ProxyServer{IdleTimeout: 5 * time.Second, Timeout: time.Second,
		dialTarget: adfOptTorTCPDial(backend, nil)}
	addr, _ := adfOptTorStart(t, s)

	adfOptTorHalfCloseCheck(t, addr, reply)
	if req := <-gotReq; req != "QUIT\r\n" {
		t.Fatalf("backend got %q", req)
	}
}

// Same as TestAdfOptTorHalfClose, but through the real proxy.SOCKS5 dial path.
func TestAdfOptTorHalfCloseThroughSOCKS(t *testing.T) {
	reply := append([]byte("205 bye\r\n"), bytes.Repeat([]byte("y"), 128*1024)...)
	gotReq := make(chan string, 1)
	backend := adfOptTorBackend(t, adfOptTorReplyAfterEOF(reply, gotReq))
	s := &ProxyServer{
		TargetHost:  "adf-opt-tcp2tor.invalid",
		TargetPort:  "119",
		ProxyConfig: adfOptTorFakeSOCKS(t, backend),
		IdleTimeout: 5 * time.Second,
		Timeout:     2 * time.Second,
	}
	addr, _ := adfOptTorStart(t, s)

	adfOptTorHalfCloseCheck(t, addr, reply)
	if req := <-gotReq; req != "QUIT\r\n" {
		t.Fatalf("backend got %q", req)
	}
}

// Stop aborts a SOCKS5 dial in progress instead of waiting for -timeout.
func TestAdfOptTorStopDuringDial(t *testing.T) {
	s := &ProxyServer{
		TargetHost:  "adf-opt-tcp2tor.invalid",
		TargetPort:  "119",
		ProxyConfig: adfTorSilentProxy(t),
		Timeout:     30 * time.Second,
	}
	addr, done := adfOptTorStart(t, s)

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	time.Sleep(200 * time.Millisecond) // let the handler reach the SOCKS5 handshake

	start := time.Now()
	s.Stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Serve still running 3s after Stop with a dial in progress")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Stop took %v", elapsed)
	}
	adfOptTorExpectClosed(t, c, 2*time.Second)
}

// Stop closes forwarded connections and Serve waits for their handlers.
func TestAdfOptTorStopClosesLiveConns(t *testing.T) {
	backend := adfOptTorBackend(t, func(c *net.TCPConn) {
		_, _ = io.Copy(c, c)
	})
	s := &ProxyServer{IdleTimeout: time.Minute, Timeout: time.Second,
		dialTarget: adfOptTorTCPDial(backend, nil)}
	addr, done := adfOptTorStart(t, s)

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 2)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("echo: %v", err)
	}

	s.Stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Serve still running 3s after Stop with a live connection")
	}
	adfOptTorExpectClosed(t, c, 2*time.Second)
}
