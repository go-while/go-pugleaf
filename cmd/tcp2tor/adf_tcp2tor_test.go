package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// testWait is the hang guard for events a test expects (Serve returning, a connection closing, a
// forwarded dial). It only stops a hung test; a passing test never waits on it. Override with
// PUGLEAF_TEST_DEADLINE (a Go duration, e.g. 120s). Plan test-timing-hardening D3.
var testWait = testWaitFromEnv(60 * time.Second)

func testWaitFromEnv(def time.Duration) time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// adfTorWithin runs fn in a goroutine and returns its error, or fails the test when fn has not
// returned within testWait (a hang guard, not a timing assertion).
func adfTorWithin(t *testing.T, what string, fn func() error) error {
	t.Helper()
	res := make(chan error, 1) // buffered: a late fn still finishes after a failed test
	go func() { res <- fn() }()
	select {
	case err := <-res:
		return err
	case <-time.After(testWait):
		t.Fatalf("%s still running after %v", what, testWait)
		return nil
	}
}

// adfTorIsTimeout reports whether err is a timeout: context.DeadlineExceeded, an expired I/O
// deadline or any net.Error whose Timeout() is true (proxy.SOCKS5 wraps both in a *net.OpError).
func adfTorIsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// adfTorSilentProxy starts a listener on 127.0.0.1:0 that accepts connections and never writes,
// standing in for a SOCKS5 proxy that hangs during the handshake. Everything is closed in cleanup.
func adfTorSilentProxy(t *testing.T) *ProxyConfig {
	t.Helper()
	cfg, _ := adfTorSilentProxyConns(t)
	return cfg
}

// adfTorSilentProxyConns is adfTorSilentProxy that also hands every accepted connection to the
// returned channel (buffered; connections beyond its capacity are not reported), so a test can
// wait until a dialer is inside the SOCKS5 handshake and watch that connection being closed.
func adfTorSilentProxyConns(t *testing.T) (*ProxyConfig, <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu       sync.Mutex
		conns    []net.Conn
		wg       sync.WaitGroup
		accepted = make(chan net.Conn, 16)
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
			select {
			case accepted <- c:
			default:
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	addr := ln.Addr().(*net.TCPAddr)
	return &ProxyConfig{SocksHost: "127.0.0.1", SocksPort: addr.Port}, accepted
}

// The self-test against a silent proxy must end with a timeout error, not a refused or reset
// connection. testSOCKS5Connection formats its cause with %v, so the error chain is lost and the
// check is on the message: "i/o timeout" (os.ErrDeadlineExceeded) or "context deadline exceeded".
func TestAdfTorSelfTestTimeout(t *testing.T) {
	cfg := adfTorSilentProxy(t)
	start := time.Now()
	err := adfTorWithin(t, "testSOCKS5Connection (timeoutSec=1)", func() error {
		return testSOCKS5Connection(cfg, "adf-tcp2tor.invalid", "119", 1, false)
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("testSOCKS5Connection against a silent proxy returned nil error")
	}
	if msg := err.Error(); !strings.Contains(msg, "i/o timeout") && !strings.Contains(msg, "deadline exceeded") {
		t.Fatalf("testSOCKS5Connection error = %v, want a timeout", err)
	}
	// Deliberate 10x bound: a regression that ignores timeoutSec and falls back to the 30 s
	// default would still pass the error check and the testWait guard.
	if elapsed > 10*time.Second {
		t.Fatalf("testSOCKS5Connection took %v, want <= 10s (10x timeoutSec=1)", elapsed)
	}
}

func TestAdfTorDialTimeout(t *testing.T) {
	cfg := adfTorSilentProxy(t)
	timeout := 500 * time.Millisecond
	proxyAddr := net.JoinHostPort(cfg.SocksHost, strconv.Itoa(cfg.SocksPort))
	d, err := proxy.SOCKS5("tcp", proxyAddr, nil, socksForwardDialer(timeout))
	if err != nil {
		t.Fatalf("proxy.SOCKS5: %v", err)
	}
	var conn net.Conn
	start := time.Now()
	err = adfTorWithin(t, "dialThroughSOCKS (timeout 500ms)", func() error {
		var derr error
		conn, derr = dialThroughSOCKS(d, "adf-tcp2tor.invalid:119", timeout)
		return derr
	})
	elapsed := time.Since(start)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatalf("dialThroughSOCKS against a silent proxy returned nil error")
	}
	if !adfTorIsTimeout(err) {
		t.Fatalf("dialThroughSOCKS error = %v, want a timeout (context.DeadlineExceeded or net.Error.Timeout)", err)
	}
	// Deliberate 10x bound: a regression that ignores the 500ms timeout and falls back to the
	// 30 s default would still pass the error check and the testWait guard.
	if elapsed > 10*timeout {
		t.Fatalf("dialThroughSOCKS took %v, want <= %v (10x timeout %v)", elapsed, 10*timeout, timeout)
	}
}

// adfTorBlockingDialer is a proxy.Dialer without DialContext: Dial blocks until release is closed,
// then returns one end of a pipe and hands the other end to the test, which checks that the late
// connection was closed.
type adfTorBlockingDialer struct {
	release chan struct{}
	peer    chan net.Conn
}

func (d *adfTorBlockingDialer) Dial(network, addr string) (net.Conn, error) {
	<-d.release
	c1, c2 := net.Pipe()
	d.peer <- c2
	return c1, nil
}

func TestAdfTorDialTimeoutPlainDialer(t *testing.T) {
	d := &adfTorBlockingDialer{release: make(chan struct{}), peer: make(chan net.Conn, 1)}
	released := false
	t.Cleanup(func() {
		if !released {
			close(d.release)
		}
	})

	start := time.Now()
	conn, err := dialThroughSOCKS(d, "adf-tcp2tor.invalid:119", 200*time.Millisecond)
	elapsed := time.Since(start)
	if conn != nil {
		_ = conn.Close()
		t.Fatalf("dialThroughSOCKS returned a conn from a blocked dialer")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dialThroughSOCKS error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("dialThroughSOCKS took %v, want <= 2s (timeout 200ms)", elapsed)
	}

	// Let the late Dial finish: its connection must be closed, which the pipe peer sees as EOF.
	close(d.release)
	released = true
	var peer net.Conn
	select {
	case peer = <-d.peer:
	case <-time.After(testWait):
		t.Fatalf("late Dial did not complete within %v", testWait)
	}
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := peer.Read(buf)
		readErr <- err
	}()
	select {
	case err := <-readErr:
		_ = peer.Close()
		if err == nil {
			t.Fatalf("late connection was not closed: read succeeded")
		}
	case <-time.After(testWait):
		_ = peer.Close() // unblocks the reader goroutine
		<-readErr
		t.Fatalf("late connection was not closed within %v", testWait)
	}
}
