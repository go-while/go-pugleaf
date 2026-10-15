package main

// Tests of the followup-leftovers slice fl-ops-tools (tcp2tor): a failed Accept that is
// not a closed listener is retried with a growing pause (net/http style, at most 1 s)
// instead of ending the process; Stop ends the pause; -add warns about a listen host
// that the bind policy would refuse. The listener is a fake: no socket is opened.

import (
	"bytes"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// flOpsFakeListener returns transient errors for the first `transient` Accept calls
// (all of them when transient < 0), then net.ErrClosed. Close makes every later
// Accept return net.ErrClosed.
type flOpsFakeListener struct {
	mu        sync.Mutex
	transient int
	calls     int
	closed    bool
	called    chan struct{} // receives once per Accept call (non-blocking)
}

func (l *flOpsFakeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	select {
	case l.called <- struct{}{}:
	default:
	}
	if l.closed || (l.transient >= 0 && l.calls > l.transient) {
		return nil, net.ErrClosed
	}
	return nil, errors.New("accept: too many open files")
}

func (l *flOpsFakeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return nil
}

func (l *flOpsFakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func (l *flOpsFakeListener) acceptCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

func flOpsServe(t *testing.T, s *ProxyServer) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	return done
}

func flOpsWaitServe(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v, want nil", err)
		}
	case <-time.After(testWait):
		t.Fatalf("Serve did not return within %v", testWait)
	}
}

func TestFlOpsTorAcceptDelay(t *testing.T) {
	want := []time.Duration{5, 10, 20, 40, 80, 160, 320, 640}
	d := time.Duration(0)
	for _, w := range want {
		d = nextAcceptDelay(d)
		if d != w*time.Millisecond {
			t.Fatalf("delay = %v, want %v", d, w*time.Millisecond)
		}
	}
	for i := 0; i < 3; i++ {
		if d = nextAcceptDelay(d); d != acceptRetryMax {
			t.Fatalf("delay = %v, want the cap %v", d, acceptRetryMax)
		}
	}
}

func TestFlOpsTorAcceptRetriesTransientError(t *testing.T) {
	ln := &flOpsFakeListener{transient: 3, called: make(chan struct{}, 1)}
	s := &ProxyServer{ListenAddr: "fake"}
	s.listener = ln
	flOpsWaitServe(t, flOpsServe(t, s))
	if got := ln.acceptCalls(); got != 4 {
		t.Fatalf("Accept called %d times, want 4 (3 retried errors, then the closed listener)", got)
	}
}

func TestFlOpsTorStopEndsAcceptBackoff(t *testing.T) {
	ln := &flOpsFakeListener{transient: -1, called: make(chan struct{}, 1)}
	s := &ProxyServer{ListenAddr: "fake"}
	s.listener = ln
	done := flOpsServe(t, s)
	// let the backoff grow a little, then stop during a pause
	for i := 0; i < 3; i++ {
		select {
		case <-ln.called:
		case <-time.After(testWait):
			t.Fatalf("Accept was not called again within %v", testWait)
		}
	}
	s.Stop()
	flOpsWaitServe(t, done)
}

func TestFlOpsTorAddWarnsPublicHost(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cfg := filepath.Join(t.TempDir(), "tcp2tor.cfg")
	addEntryToConfigFile(cfg, "0.0.0.0", 1119, "example.onion:119", false)
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "0.0.0.0:1119:example.onion:119") {
		t.Fatalf("entry not added:\n%s", data)
	}
	if !strings.Contains(buf.String(), "WARNING") || !strings.Contains(buf.String(), "not a loopback address") {
		t.Fatalf("no bind-policy warning for 0.0.0.0:\n%s", buf.String())
	}

	buf.Reset()
	addEntryToConfigFile(cfg, "127.2.3.4", 1120, "example.onion:119", false)
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("warning for a loopback host:\n%s", buf.String())
	}
}
