package nntp

import (
	"bufio"
	"net"
	"sync"
	"testing"

	"github.com/go-while/go-pugleaf/internal/config"
)

// Plan xref-leftovers item 3: Config.NNTP.Addr ("" = all interfaces) selects the IP
// the Start listeners bind to.

func TestBindListenAddr(t *testing.T) {
	for _, tc := range []struct {
		addr string
		port int
		want string
	}{
		{"", 1119, ":1119"},
		{"127.0.0.1", 1119, "127.0.0.1:1119"},
		{"::1", 1119, "[::1]:1119"},
		{"2001:db8::7", 563, "[2001:db8::7]:563"},
	} {
		if got := listenAddr(tc.addr, tc.port); got != tc.want {
			t.Errorf("listenAddr(%q, %d) = %q, want %q", tc.addr, tc.port, got, tc.want)
		}
	}
}

// bindFreePort returns a port that was free on ip a moment ago (listener closed
// again); ok is false when ip cannot be bound at all (no IPv6 loopback).
func bindFreePort(t *testing.T, ip string) (port int, ok bool) {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return 0, false
	}
	port = l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return port, true
}

func TestBindStartOnAddr(t *testing.T) {
	db := hTestDB(t)
	for _, ip := range []string{"127.0.0.1", "::1"} {
		t.Run(ip, func(t *testing.T) {
			port, ok := bindFreePort(t, ip)
			if !ok {
				t.Skipf("no loopback listener on %s", ip)
			}
			addr := listenAddr(ip, port)
			var wg sync.WaitGroup
			cfg := &config.ServerConfig{}
			cfg.NNTP.MaxConns = 10
			cfg.NNTP.Addr = ip
			cfg.NNTP.Port = port
			s, err := NewNNTPServer(db, cfg, &wg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Start(); err != nil {
				t.Fatalf("Start on %s: %v", addr, err)
			}
			stopped := false
			t.Cleanup(func() {
				if !stopped {
					_ = s.Stop()
					sessWaitWG(t, &wg)
				}
			})

			s.mu.Lock()
			bound, _ := s.Listener.Addr().(*net.TCPAddr)
			s.mu.Unlock()
			if bound == nil || !bound.IP.Equal(net.ParseIP(ip)) || bound.Port != port {
				t.Fatalf("listener bound to %v, want %s", bound, addr)
			}

			conn, err := net.DialTimeout("tcp", addr, hDeadline)
			if err != nil {
				t.Fatalf("dial %s: %v", addr, err)
			}
			c := &hClient{Conn: conn, R: bufio.NewReader(conn), Deadline: hDeadline}
			greeting, err := c.readLine()
			c.close()
			if err != nil {
				t.Fatalf("greeting: %v", err)
			}
			if code, perr := hParseCode(greeting); perr != nil || code != 201 {
				t.Fatalf("greeting %q, want 201 (read-only server)", greeting)
			}

			if err := s.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			stopped = true
			sessWaitWG(t, &wg)
			// Stop released the address: it can be bound again
			l, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatalf("%s still bound after Stop: %v", addr, err)
			}
			_ = l.Close()
		})
	}
}
