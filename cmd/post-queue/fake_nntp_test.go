package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-while/nntp-testkit/fakeserver"
)

// The fake NNTP server below is a copy of the one in internal/postmgr/adf_opt_postmgr_test.go
// (test helpers cannot be imported across packages). It speaks what nntp.Pool and
// postmgr.postToProvider send: greeting 200, MODE READER, POST (340, article, final code), QUIT.

// flPqFake is a loopback NNTP server that accepts MODE READER and POST and answers every posted
// article with code (240 or 441). No external network is used. In hang mode (flPqFakeHang) it
// never answers the article: it closes received once the article is read, then reads and
// discards until the connection is closed.
//
// The listener, the accept loop and the connection bookkeeping are the kit's
// (fakeserver.Server). The three commands are a fakeserver.LineHandler and not a
// fakeserver.Script, because a script step reads its dot block before it writes its reply, so
// POST's 340 would arrive after the article.
type flPqFake struct {
	srv   *fakeserver.Server
	code  int
	posts atomic.Int64 // articles received

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

// flPqFakeStart serves f on 127.0.0.1; the kit closes the listener and every connection when
// the test ends.
func flPqFakeStart(t *testing.T, f *flPqFake) *flPqFake {
	t.Helper()
	f.srv = fakeserver.New(t, fakeserver.Options{
		Greeting: "200 flpq fake ready",
		Handler:  fakeserver.LineHandler(f.line),
		Deadline: testWait,
	})
	return f
}

func (f *flPqFake) port() int { return f.srv.Port() }

// line answers one client line.
func (f *flPqFake) line(c *fakeserver.Conn, line string) (reply []string, closeConn bool) {
	switch strings.ToUpper(strings.TrimSpace(line)) {
	case "MODE READER":
		return []string{"200 reader mode"}, false
	case "POST":
		if err := c.WriteLines("340 send article"); err != nil {
			return nil, true
		}
		if _, err := c.ReadDotBlock(); err != nil {
			return nil, true
		}
		f.posts.Add(1)
		if f.hang {
			f.receivedOnce.Do(func() { close(f.received) })
			// never answer: read and discard until the client (or the kit's Close) ends it
			for {
				if _, err := c.ReadLine(); err != nil {
					return nil, true
				}
			}
		}
		return []string{fmt.Sprintf("%d flpq fake answer", f.code)}, false
	case "QUIT":
		return []string{"205 bye"}, true
	default:
		return []string{"500 unknown command"}, false
	}
}
