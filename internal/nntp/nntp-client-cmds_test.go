package nntp

// Client command tests of plan nntp-client-transfer, slice cli-commands:
// outbound serialization (CLI-1, A-intake-6, NC-1), 411 handling in the XHDR
// stream (CLI-2), response codes (CLI-5), response limits (A-nntp-client-1),
// overview parsing (A-nntp-client-7) and :bytes (INT-5). All servers are
// cliFakeServer instances on 127.0.0.1:0; AUTHINFO back-off is disabled.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
)

// cliCmdOuter is the outer deadline of every blocking step in these tests:
// a hang guard only (cliWait), never the assertion.
var cliCmdOuter = cliWait

const cliCmdDate = "Sat, 12 Sep 2026 10:11:12 +0000"

// cliCmdRun runs f in its own goroutine and returns how long it took and its
// error. The test fails when f panics or has not returned within cliCmdOuter.
func cliCmdRun(t *testing.T, f func() error) (time.Duration, error) {
	t.Helper()
	type result struct {
		err       error
		elapsed   time.Duration
		recovered any
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		var res result
		defer func() {
			if r := recover(); r != nil {
				res.recovered = r
			}
			res.elapsed = time.Since(start)
			done <- res
		}()
		res.err = f()
	}()
	select {
	case res := <-done:
		if res.recovered != nil {
			t.Fatalf("panic after %v: %v", res.elapsed, res.recovered)
		}
		return res.elapsed, res.err
	case <-time.After(cliCmdOuter):
		t.Fatalf("still blocked after %v", cliCmdOuter)
		return 0, nil
	}
}

// cliCmdDial connects to s (AuthFailureBackoff disabled, then mutate) inside cliCmdRun.
func cliCmdDial(t *testing.T, s *cliFakeServer, mutate func(*BackendConfig)) *BackendConn {
	t.Helper()
	var c *BackendConn
	_, err := cliCmdRun(t, func() error {
		var err error
		c, err = cliDial(t, s, func(cfg *BackendConfig) {
			cfg.AuthFailureBackoff = -1
			if mutate != nil {
				mutate(cfg)
			}
		})
		return err
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

// cliCmdPool builds a pool on s.Backend() (AuthFailureBackoff disabled, then
// mutate) that is closed by t.Cleanup.
func cliCmdPool(t *testing.T, s *cliFakeServer, mutate func(*BackendConfig)) *Pool {
	t.Helper()
	cfg := s.Backend()
	cfg.AuthFailureBackoff = -1
	if mutate != nil {
		mutate(cfg)
	}
	pool := NewPool(cfg)
	t.Cleanup(func() { _ = pool.ClosePool() })
	return pool
}

// cliCmdDrain reads ch until it is closed; the test fails after cliCmdOuter.
func cliCmdDrain(t *testing.T, ch <-chan HeaderLine) []HeaderLine {
	t.Helper()
	var out []HeaderLine
	timeout := time.NewTimer(cliCmdOuter)
	defer timeout.Stop()
	for {
		select {
		case h, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, h)
		case <-timeout.C:
			t.Fatalf("channel not closed within %v (got %d lines)", cliCmdOuter, len(out))
			return out
		}
	}
}

// cliCmdWaitArticles polls s.Articles() until it holds n blocks (hang guard cliCmdOuter).
func cliCmdWaitArticles(t *testing.T, s *cliFakeServer, n int) [][]string {
	t.Helper()
	deadline := time.Now().Add(cliCmdOuter)
	for {
		got := s.Articles()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("Articles() has %d blocks, want %d (lines %q)", len(got), n, s.Lines())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cliCmdSplitWire splits a raw wire block at its first empty line.
func cliCmdSplitWire(t *testing.T, wire []string) (head, body []string) {
	t.Helper()
	for i, l := range wire {
		if l == "" {
			return wire[:i], wire[i+1:]
		}
	}
	t.Fatalf("no header/body separator in %q", wire)
	return nil, nil
}

// cliCmdCount counts the lines of ls for which match is true.
func cliCmdCount(ls []string, match func(string) bool) int {
	n := 0
	for _, l := range ls {
		if match(l) {
			n++
		}
	}
	return n
}

// cliCmdPostHandler answers MODE READER and POST (340, dot block, 240); with
// first401 the first POST gets "401 MODE-READER".
func cliCmdPostHandler(first401 bool) func(fc *cliFakeConn) {
	return func(fc *cliFakeConn) {
		posts := 0
		for {
			line, err := fc.ReadLine()
			if err != nil {
				return
			}
			var werr error
			switch line {
			case "MODE READER":
				werr = fc.WriteLines("200 reader")
			case "POST":
				posts++
				if first401 && posts == 1 {
					werr = fc.WriteLines("401 MODE-READER")
					break
				}
				if werr = fc.WriteLines("340 send article"); werr != nil {
					return
				}
				if _, err := fc.ReadDotBlock(); err != nil {
					return
				}
				werr = fc.WriteLines("240 article posted")
			default:
				werr = fc.WriteLines("500 cli-cmd: unexpected " + line)
			}
			if werr != nil {
				return
			}
		}
	}
}

// cliCmdStream is a streaming connection from a pool on a cliPeerHandler
// server with a demuxer that is never started (its responses stay unread).
type cliCmdStream struct {
	s       *cliFakeServer
	conn    *BackendConn
	demuxer *ResponseDemuxer
	rr      chan *ReadRequest
	job     *CHTTJob
}

func newCliCmdStream(t *testing.T) *cliCmdStream {
	t.Helper()
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(&cliPeerOpts{})})
	pool := cliCmdPool(t, s, nil)
	var conn *BackendConn
	if _, err := cliCmdRun(t, func() error {
		var err error
		conn, err = pool.Get(MODE_STREAM_MV)
		return err
	}); err != nil {
		t.Fatalf("Get(MODE_STREAM_MV): %v", err)
	}
	errChan := make(chan struct{}, 9)
	var d *ResponseDemuxer
	if _, err := cliCmdRun(t, func() error { d = NewResponseDemuxer(conn, errChan); return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		common.SignalErrChan(errChan)
		conn.ForceCloseConn()
	})
	ng := "a.b"
	return &cliCmdStream{s: s, conn: conn, demuxer: d, rr: make(chan *ReadRequest, 4), job: &CHTTJob{Newsgroup: &ng}}
}

// send calls SendTakeThisArticleStreaming under conn.Lock() like the transfer does.
func (st *cliCmdStream) send(t *testing.T, a *models.Article) (doContinue bool, err error) {
	t.Helper()
	host := "news.local.invalid"
	if _, rerr := cliCmdRun(t, func() error {
		st.conn.Lock()
		defer st.conn.Unlock()
		_, _, err, doContinue = st.conn.SendTakeThisArticleStreaming(a, &host, "a.b", st.demuxer, st.rr, st.job, 0, 1)
		return nil
	}); rerr != nil {
		t.Fatal(rerr)
	}
	return doContinue, err
}

// cliCmdArticle returns an article with valid columns and the given stored header block.
func cliCmdArticle(msgID, headersJSON, body string) *models.Article {
	return &models.Article{
		MessageID:   msgID,
		Subject:     "subject",
		FromHeader:  "A <a@b.invalid>",
		DateString:  cliCmdDate,
		Path:        "up.invalid!poster!not-for-mail",
		HeadersJSON: headersJSON,
		BodyText:    body,
	}
}

// CLI-2: 411 to the GROUP of a streamed XHDR is ErrNewsgroupNotFound, no nil
// dereference, and the channel is closed.
func TestCliCmdXHdrStreamed411(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "GROUP .*", Reply: []string{"411 no such group"}},
	}})
	c := cliCmdDial(t, s, nil)
	ch := make(chan HeaderLine, 8)
	_, err := cliCmdRun(t, func() error { return c.XHdrStreamed("no.such.group", "Message-ID", 1, 3, ch, nil) })
	if !errors.Is(err, ErrNewsgroupNotFound) {
		t.Fatalf("XHdrStreamed: err = %v, want ErrNewsgroupNotFound", err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel delivered a value, want closed")
		}
	default:
		t.Fatal("channel not closed")
	}
}

// CLI-5: a 201 greeting (no posting) is accepted.
func TestCliCmdGreeting201(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Greeting: "201 no posting"})
	c := cliCmdDial(t, s, nil)
	if c.greetingCode != 201 {
		t.Fatalf("greetingCode = %d, want 201", c.greetingCode)
	}
}

// CLI-5: 281 directly after AUTHINFO USER is success; no PASS is sent.
func TestCliCmdAuth281AfterUser(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "AUTHINFO USER u", Reply: []string{"281 authentication accepted"}},
	}})
	cliCmdDial(t, s, func(cfg *BackendConfig) { cfg.Username, cfg.Password = "u", "p" })
	for _, l := range s.Lines() {
		if strings.HasPrefix(l, "AUTHINFO PASS") {
			t.Fatalf("AUTHINFO PASS sent after 281: %q", s.Lines())
		}
	}
}

// CLI-5: 480 to GROUP is ErrAuthRequired (not ErrNewsgroupNotFound) and the
// connection is closed, not pooled.
func TestCliCmdSelectGroup480(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "MODE READER", Reply: []string{"200 reader"}},
		{Expect: "GROUP .*", Reply: []string{"480 authentication required"}},
	}})
	pool := cliCmdPool(t, s, nil)
	_, err := cliCmdRun(t, func() error { _, err := pool.SelectGroup("g.a"); return err })
	if !errors.Is(err, ErrAuthRequired) || errors.Is(err, ErrNewsgroupNotFound) {
		t.Fatalf("SelectGroup: err = %v, want ErrAuthRequired", err)
	}
	s.WaitOpen(t, 0, 2*time.Second)
	if idle := pool.Stats().IdleConnections; idle != 0 {
		t.Fatalf("IdleConnections = %d, want 0", idle)
	}
}

// CLI-5: 430/451 to STAT/HEAD/BODY reach their arms; the connection stays usable.
func TestCliCmdNotFoundCodes(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "STAT <a@x.invalid>", Reply: []string{"430 no such article"}},
		{Expect: "HEAD <a@x.invalid>", Reply: []string{"430 no such article"}},
		{Expect: "BODY <a@x.invalid>", Reply: []string{"451 removed"}},
		{Expect: "STAT <ok@x.invalid>", Reply: []string{"223 0 <ok@x.invalid>"}},
	}})
	c := cliCmdDial(t, s, nil)
	var ok bool
	if _, err := cliCmdRun(t, func() error { var err error; ok, err = c.StatArticle("<a@x.invalid>"); return err }); err != nil || ok {
		t.Fatalf("STAT 430: ok=%v err=%v, want false, nil", ok, err)
	}
	if _, err := cliCmdRun(t, func() error { _, err := c.GetHead("<a@x.invalid>"); return err }); !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("HEAD 430: err = %v, want ErrArticleNotFound", err)
	}
	if _, err := cliCmdRun(t, func() error { _, err := c.GetBody("<a@x.invalid>"); return err }); !errors.Is(err, ErrArticleRemoved) {
		t.Fatalf("BODY 451: err = %v, want ErrArticleRemoved", err)
	}
	if _, err := cliCmdRun(t, func() error { var err error; ok, err = c.StatArticle("<ok@x.invalid>"); return err }); err != nil || !ok {
		t.Fatalf("STAT 223: ok=%v err=%v, want true, nil", ok, err)
	}
}

// CLI-5: "401 MODE-READER" triggers MODE READER and a second POST.
func TestCliCmdPost401ModeReader(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliCmdPostHandler(true)})
	c := cliCmdDial(t, s, nil)
	a := cliCmdArticle("<p401@test.invalid>", "Newsgroups: a.b", "body\n")
	var code int
	if _, err := cliCmdRun(t, func() error { var err error; code, err = c.PostArticle(a); return err }); err != nil || code != 240 {
		t.Fatalf("PostArticle: code=%d err=%v, want 240", code, err)
	}
	if n := cliCmdCount(s.Lines(), func(l string) bool { return l == "MODE READER" }); n != 1 {
		t.Fatalf("MODE READER sent %d times: %q", n, s.Lines())
	}
}

// CLI-1, A-intake-6: TAKETHIS relays the stored block: one Path line, no Xref,
// the continuation once, header and body lines dot-stuffed, final empty line kept.
func TestCliCmdTakeThisWire(t *testing.T) {
	st := newCliCmdStream(t)
	hdr := strings.Join([]string{
		"Path: up.invalid!poster!not-for-mail",
		"From: A <a@b.invalid>",
		"Newsgroups: a.b,",
		" c.d",
		"Subject: tt",
		"Date: " + cliCmdDate,
		"Message-ID: <tt1@test.invalid>",
		"Xref: up.invalid a.b:1",
		".X-Dot: y",
	}, "\n")
	a := cliCmdArticle("<tt1@test.invalid>", hdr, "a\n.b\n")
	if doContinue, err := st.send(t, a); err != nil || !doContinue {
		t.Fatalf("SendTakeThisArticleStreaming: err=%v doContinue=%v", err, doContinue)
	}
	head, body := cliCmdSplitWire(t, cliCmdWaitArticles(t, st.s, 1)[0])
	if n := cliCmdCount(head, func(l string) bool { return strings.HasPrefix(l, "Path:") }); n != 1 {
		t.Errorf("%d Path lines in %q", n, head)
	}
	if got := head[0]; got != "Path: news.local.invalid!up.invalid!poster!not-for-mail" {
		t.Errorf("Path line = %q", got)
	}
	if n := cliCmdCount(head, func(l string) bool { return strings.HasPrefix(strings.ToLower(l), "xref") }); n != 0 {
		t.Errorf("Xref sent: %q", head)
	}
	if n := cliCmdCount(head, func(l string) bool { return l == " c.d" }); n != 1 {
		t.Errorf("continuation sent %d times: %q", n, head)
	}
	if n := cliCmdCount(head, func(l string) bool { return l == "..X-Dot: y" }); n != 1 {
		t.Errorf("header line .X-Dot not dot-stuffed: %q", head)
	}
	if want := []string{"a", "..b", ""}; strings.Join(body, "\x01") != strings.Join(want, "\x01") || len(body) != len(want) {
		t.Errorf("body = %q, want %q", body, want)
	}
}

// A-intake-6: an unsafe stored block is skipped before anything is written.
func TestCliCmdTakeThisInvalidSkipped(t *testing.T) {
	st := newCliCmdStream(t)
	bad := strings.Join([]string{"Path: p!not-for-mail", "From: A <a@b.invalid>", "Newsgroups: a.b",
		"Subject: one", "Subject: two", "Date: " + cliCmdDate, "Message-ID: <bad@test.invalid>"}, "\n")
	doContinue, err := st.send(t, cliCmdArticle("<bad@test.invalid>", bad, "x\n"))
	if !errors.Is(err, common.ErrRelayHeadersInvalid) || !doContinue {
		t.Fatalf("invalid block: err=%v doContinue=%v, want ErrRelayHeadersInvalid and true", err, doContinue)
	}
	// A valid article afterwards is the first and only TAKETHIS on the wire.
	good := strings.Join([]string{"Path: p!not-for-mail", "From: A <a@b.invalid>", "Newsgroups: a.b",
		"Subject: one", "Date: " + cliCmdDate, "Message-ID: <good@test.invalid>"}, "\n")
	if _, err := st.send(t, cliCmdArticle("<good@test.invalid>", good, "x\n")); err != nil {
		t.Fatalf("valid article: %v", err)
	}
	cliCmdWaitArticles(t, st.s, 1)
	var tt []string
	for _, l := range st.s.Lines() {
		if strings.HasPrefix(l, "TAKETHIS") {
			tt = append(tt, l)
		}
	}
	if len(tt) != 1 || tt[0] != "TAKETHIS <good@test.invalid>" {
		t.Fatalf("TAKETHIS lines = %q, want only the valid article", tt)
	}
}

// A-intake-6, INT-7: a web-post style block falls back to ReconstructHeaders.
func TestCliCmdTakeThisIncompleteFallback(t *testing.T) {
	st := newCliCmdStream(t)
	web := strings.Join([]string{"MIME-Version: 1.0", "Content-Type: text/plain; charset=UTF-8",
		"From: A <a@b.invalid>", "Newsgroups: a.b"}, "\n")
	a := cliCmdArticle("<web1@test.invalid>", web, "hello\n")
	a.Path = "web.invalid!not-for-mail"
	if _, err := st.send(t, a); err != nil {
		t.Fatalf("SendTakeThisArticleStreaming: %v", err)
	}
	head, _ := cliCmdSplitWire(t, cliCmdWaitArticles(t, st.s, 1)[0])
	for _, want := range []string{"Path: news.local.invalid!web.invalid!not-for-mail", "Date: " + cliCmdDate,
		"Message-ID: <web1@test.invalid>", "Subject: subject", "MIME-Version: 1.0"} {
		if n := cliCmdCount(head, func(l string) bool { return l == want }); n != 1 {
			t.Errorf("line %q appears %d times in %q", want, n, head)
		}
	}
	if n := cliCmdCount(head, func(l string) bool { return strings.HasPrefix(l, "Path:") }); n != 1 {
		t.Errorf("%d Path lines in %q", n, head)
	}
}

// CLI-2: a cut-off XHDR stream (read timeout, over-long line) is an error: the
// channel closes, the connection is closed and nothing returns to the pool.
func TestCliCmdXHdrStreamedReadError(t *testing.T) {
	run := func(t *testing.T, second string, mutate func(*BackendConfig)) {
		s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
			{Expect: "MODE READER", Reply: []string{"200 reader"}},
			{Expect: "GROUP .*", Reply: []string{"211 3 1 3 g"}},
			{Expect: "XHDR .*", Reply: []string{"221 ok", second}, Hang: true},
		}})
		pool := cliCmdPool(t, s, mutate)
		ch := make(chan HeaderLine, 16)
		if _, err := cliCmdRun(t, func() error { return pool.XHdrStreamed("g", "Message-ID", 1, 3, ch, nil) }); err != nil {
			t.Fatalf("pool.XHdrStreamed: %v", err)
		}
		cliCmdDrain(t, ch)
		s.WaitOpen(t, 0, 2*time.Second)
		if idle := pool.Stats().IdleConnections; idle != 0 {
			t.Fatalf("IdleConnections = %d, want 0", idle)
		}
	}
	t.Run("read timeout", func(t *testing.T) {
		run(t, "1 a", func(cfg *BackendConfig) { cfg.ReadTimeout = 300 * time.Millisecond })
	})
	t.Run("line cap", func(t *testing.T) {
		run(t, "1 "+strings.Repeat("x", 4096), func(cfg *BackendConfig) { cfg.MaxResponseLineBytes = 512 })
	})
}

// CLI-1 parts 2/3, NC-1: POST sends the folded Newsgroups once, the first
// Subject line only and the body byte-exact.
func TestCliCmdPostWire(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliCmdPostHandler(false)})
	c := cliCmdDial(t, s, nil)
	a := cliCmdArticle("<pw@test.invalid>", "Newsgroups: a.b,\n c.d", "x\n")
	a.Subject = "s\nx"
	var code int
	if _, err := cliCmdRun(t, func() error { var err error; code, err = c.PostArticle(a); return err }); err != nil || code != 240 {
		t.Fatalf("PostArticle: code=%d err=%v, want 240", code, err)
	}
	head, body := cliCmdSplitWire(t, cliCmdWaitArticles(t, s, 1)[0])
	if n := cliCmdCount(head, func(l string) bool { return strings.Contains(l, "c.d") }); n != 1 {
		t.Errorf("c.d appears in %d header lines: %q", n, head)
	}
	if n := cliCmdCount(head, func(l string) bool { return strings.HasPrefix(l, "Subject:") }); n != 1 ||
		cliCmdCount(head, func(l string) bool { return l == "Subject: s" }) != 1 {
		t.Errorf("Subject lines wrong: %q", head)
	}
	if len(body) != 2 || body[0] != "x" || body[1] != "" {
		t.Errorf("body = %q, want [\"x\" \"\"]", body)
	}
}

// A-nntp-client-1: an ARTICLE response over MaxArticleResponseBytes fails.
func TestCliCmdArticleBudget(t *testing.T) {
	reply := []string{"220 1 <big@test.invalid>", "Path: x!not-for-mail", "Message-ID: <big@test.invalid>", ""}
	for i := 0; i < 64; i++ {
		reply = append(reply, strings.Repeat("b", 64))
	}
	reply = append(reply, ".")
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{{Expect: "ARTICLE .*", Reply: reply}}})
	c := cliCmdDial(t, s, func(cfg *BackendConfig) { cfg.MaxArticleResponseBytes = 1024 })
	msgID := "<big@test.invalid>"
	if _, err := cliCmdRun(t, func() error { _, err := c.GetArticle(&msgID, true); return err }); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("GetArticle: err = %v, want ErrResponseTooLarge", err)
	}
}

// A-nntp-client-1: a response line over MaxResponseLineBytes fails.
func TestCliCmdLineCap(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "GROUP .*", Reply: []string{"211 3 1 3 g"}},
		{Expect: "XOVER .*", Reply: []string{"224 overview follows", "1\t" + strings.Repeat("s", 4096), "."}},
	}})
	c := cliCmdDial(t, s, func(cfg *BackendConfig) { cfg.MaxResponseLineBytes = 512 })
	if _, err := cliCmdRun(t, func() error { _, err := c.XOver("g", 1, 3, false); return err }); !errors.Is(err, ErrResponseLineTooLong) {
		t.Fatalf("XOver: err = %v, want ErrResponseLineTooLong", err)
	}
}

// A-nntp-client-1: an XHDR stream may not return more lines than requested.
func TestCliCmdXHdrStreamedLineCap(t *testing.T) {
	reply := []string{"221 headers follow"}
	for i := 1; i <= 10; i++ {
		reply = append(reply, strings.Repeat("1", i)+" v") // distinct positive numbers
	}
	reply = append(reply, ".")
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "GROUP .*", Reply: []string{"211 10 1 10 g"}},
		{Expect: "XHDR .*", Reply: reply},
	}})
	c := cliCmdDial(t, s, nil)
	ch := make(chan HeaderLine, 16)
	if _, err := cliCmdRun(t, func() error { return c.XHdrStreamed("g", "Subject", 1, 3, ch, nil) }); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("XHdrStreamed: err = %v, want ErrResponseTooLarge", err)
	}
	cliCmdDrain(t, ch)
}

// A-nntp-client-1: the SOCKS negotiation is bounded by ConnectTimeout.
func TestCliCmdSOCKSTimeout(t *testing.T) {
	proxy := newCliFakeServer(t, cliFakeOpts{RawStall: true})
	cfg := &BackendConfig{
		Host:               "127.0.0.1",
		Port:               119,
		MaxConns:           1,
		ConnectTimeout:     300 * time.Millisecond,
		AuthFailureBackoff: -1,
		ProxyEnabled:       true,
		ProxyType:          "socks5",
		ProxyHost:          proxy.Host,
		ProxyPort:          proxy.Port,
	}
	c := NewConn(cfg)
	elapsed, err := cliCmdRun(t, c.Connect)
	if err == nil {
		_ = c.CloseFromPoolOnly()
		t.Fatal("Connect through a stalled SOCKS proxy succeeded")
	}
	// the SOCKS negotiation is bounded by ConnectTimeout: a timeout error
	// (x/net/proxy wraps it in a *net.OpError), not a hang (cliCmdOuter)
	if !cliConnIsTimeout(err) {
		t.Fatalf("Connect error %v (after %v), want a timeout (ConnectTimeout 300ms)", err, elapsed)
	}
	// loose bound, deliberately 50x the 300 ms: catches a fallback to
	// config.DefaultConnectTimeout (30 s) instead of the configured value
	if elapsed >= config.DefaultConnectTimeout/2 {
		t.Fatalf("Connect took %v: ConnectTimeout 300ms ignored (default %v?)", elapsed, config.DefaultConnectTimeout)
	}
}

// A-nntp-client-7: overview/XHDR lines with a non-numeric or non-positive
// article number are dropped.
func TestCliCmdOverviewMalformed(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "GROUP .*", Reply: []string{"211 5 1 5 g"}},
		{Expect: "XOVER .*", Reply: []string{"224 overview follows",
			"abc\ts\tf\t" + cliCmdDate + "\t<m1@x.invalid>\t\t10\t1",
			"0\ts\tf\t" + cliCmdDate + "\t<m0@x.invalid>\t\t10\t1",
			"5\ts\tf\t" + cliCmdDate + "\t<m5@x.invalid>\t\t10\t1",
			"."}},
		{Expect: "GROUP .*", Reply: []string{"211 5 1 5 g"}},
		{Expect: "XHDR .*", Reply: []string{"221 headers follow", "0 x", "2 y", "."}},
	}})
	c := cliCmdDial(t, s, nil)
	var ov []OverviewLine
	if _, err := cliCmdRun(t, func() error { var err error; ov, err = c.XOver("g", 1, 5, false); return err }); err != nil {
		t.Fatalf("XOver: %v", err)
	}
	if len(ov) != 1 || ov[0].ArticleNum != 5 || ov[0].MessageID != "<m5@x.invalid>" {
		t.Fatalf("XOver lines = %+v, want only article 5", ov)
	}
	ch := make(chan HeaderLine, 16)
	if _, err := cliCmdRun(t, func() error { return c.XHdrStreamed("g", "Subject", 1, 3, ch, nil) }); err != nil {
		t.Fatalf("XHdrStreamed: %v", err)
	}
	got := cliCmdDrain(t, ch)
	if len(got) != 1 || got[0].ArticleNum != 2 || got[0].Value != "y" {
		t.Fatalf("XHDR stream = %+v, want only {2 y}", got)
	}
}

// INT-5: Bytes is the CRLF wire size of the whole article, also without body.
func TestCliCmdBytesCRLF(t *testing.T) {
	head := []string{"Path: x!not-for-mail", "From: A <a@b.invalid>", "Newsgroups: a.b", "Subject: s",
		"Date: " + cliCmdDate, "Message-ID: <bytes@test.invalid>"}
	body := []string{"line one", ".dot", ""}
	size := func(ls []string) int {
		n := 0
		for _, l := range ls {
			n += len(l) + 2
		}
		return n
	}
	lines := append(append(append([]string{}, head...), ""), body...)
	a, err := ParseLegacyArticleLines("<bytes@test.invalid>", lines, true)
	if err != nil {
		t.Fatalf("ParseLegacyArticleLines: %v", err)
	}
	if want := size(head) + 2 + size(body); a.Bytes != want {
		t.Errorf("Bytes = %d, want %d", a.Bytes, want)
	}
	a, err = ParseLegacyArticleLines("<bytes@test.invalid>", append(append([]string{}, head...), ""), true)
	if err != nil {
		t.Fatalf("ParseLegacyArticleLines (no body): %v", err)
	}
	if want := size(head) + 2; a.Bytes != want {
		t.Errorf("no body: Bytes = %d, want %d", a.Bytes, want)
	}
}
