package main

import (
	"io"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// The fake NNTP server below is a copy of the one in internal/postmgr/adf_opt_postmgr_test.go
// (test helpers cannot be imported across packages). It speaks what nntp.Pool and
// postmgr.postToProvider send: greeting 200, MODE READER, POST (340, article, final code), QUIT.

// flPqFake is a loopback NNTP server that accepts MODE READER and POST and answers every posted
// article with code (240 or 441). No external network is used. In hang mode (flPqFakeHang) it
// never answers the article: it closes received once the article is read, then blocks until the
// connection is closed.
type flPqFake struct {
	ln    net.Listener
	code  int
	posts atomic.Int64 // articles received
	mu    sync.Mutex
	conns []net.Conn
	wg    sync.WaitGroup

	hang         bool
	received     chan struct{} // hang mode: closed when the first article was read
	receivedOnce sync.Once
}

// flPqFakeNNTP starts a flPqFake on 127.0.0.1; it stops when the test ends.
func flPqFakeNNTP(t *testing.T, code int) *flPqFake {
	t.Helper()
	return flPqFakeStart(t, &flPqFake{code: code})
}

// flPqFakeHang starts a flPqFake in hang mode: it answers 340 to POST, reads the article, closes
// received and then never answers, until the client closes the connection.
func flPqFakeHang(t *testing.T) *flPqFake {
	t.Helper()
	return flPqFakeStart(t, &flPqFake{hang: true, received: make(chan struct{})})
}

// flPqFakeStart listens on 127.0.0.1 and serves f; it stops when the test ends.
func flPqFakeStart(t *testing.T, f *flPqFake) *flPqFake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.ln = ln
	f.wg.Add(1)
	go f.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		for _, c := range f.conns {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *flPqFake) port() int {
	return f.ln.Addr().(*net.TCPAddr).Port
}

func (f *flPqFake) serve() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		f.wg.Add(1)
		go f.handle(c)
	}
}

func (f *flPqFake) handle(c net.Conn) {
	defer f.wg.Done()
	defer c.Close()
	tp := textproto.NewConn(c)
	if err := tp.PrintfLine("200 flpq fake ready"); err != nil {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		switch strings.ToUpper(strings.TrimSpace(line)) {
		case "MODE READER":
			err = tp.PrintfLine("200 reader mode")
		case "POST":
			if err = tp.PrintfLine("340 send article"); err != nil {
				return
			}
			if _, err = tp.ReadDotLines(); err != nil {
				return
			}
			f.posts.Add(1)
			if f.hang {
				f.receivedOnce.Do(func() { close(f.received) })
				// never answer: block until the client (or the test cleanup) closes the conn
				_, _ = io.Copy(io.Discard, c)
				return
			}
			err = tp.PrintfLine("%d flpq fake answer", f.code)
		case "QUIT":
			_ = tp.PrintfLine("205 bye")
			return
		default:
			err = tp.PrintfLine("500 unknown command")
		}
		if err != nil {
			return
		}
	}
}

// flPqClosedPort returns a loopback port nothing listens on (connection refused).
func flPqClosedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
