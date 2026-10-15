package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// adfTorSilentProxy starts a listener on 127.0.0.1:0 that accepts connections and never writes,
// standing in for a SOCKS5 proxy that hangs during the handshake. Everything is closed in cleanup.
func adfTorSilentProxy(t *testing.T) *ProxyConfig {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
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
	return &ProxyConfig{SocksHost: "127.0.0.1", SocksPort: addr.Port}
}

func TestAdfTorSelfTestTimeout(t *testing.T) {
	cfg := adfTorSilentProxy(t)
	start := time.Now()
	err := testSOCKS5Connection(cfg, "adf-tcp2tor.invalid", "119", 1, false)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("testSOCKS5Connection against a silent proxy returned nil error")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("testSOCKS5Connection took %v, want <= 3s (timeoutSec=1)", elapsed)
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
	start := time.Now()
	conn, err := dialThroughSOCKS(d, "adf-tcp2tor.invalid:119", timeout)
	elapsed := time.Since(start)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatalf("dialThroughSOCKS against a silent proxy returned nil error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("dialThroughSOCKS took %v, want <= 2s (timeout 500ms)", elapsed)
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
	case <-time.After(2 * time.Second):
		t.Fatalf("late Dial did not complete")
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
	case <-time.After(2 * time.Second):
		_ = peer.Close() // unblocks the reader goroutine
		<-readErr
		t.Fatalf("late connection was not closed within 2s")
	}
}
