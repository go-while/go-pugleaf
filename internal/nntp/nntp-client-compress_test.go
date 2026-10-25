package nntp

// COMPRESS DEFLATE (RFC 8054) on provider connections: plan
// fetcher-compress-deflate, slice compress-conn, design points 2, 3 and 5.
//
// Pinned here:
//   - against a kit Store server with StoreOptions.Compress the client sends
//     COMPRESS DEFLATE once, switches on the 206 and serves GROUP, ARTICLE,
//     XHDR and XOVER over the compressed stream; the wire counters stay below
//     the raw ones;
//   - a refusal (Faults.Override "COMPRESS" -> 403, or a server without the
//     command -> 500) keeps the connection plain and working and is logged
//     once per provider;
//   - StoreOptions.CompressOutputOnly (a server that deflates its output but
//     reads its input plain) ends in an error and a closed connection, never
//     in a hang;
//   - a Pool with Compress serves articles, and the server-side connection cap
//     is still reported as the "502" greeting;
//   - the hostile-peer bounds measure the *inflated* stream: a compressed
//     endless reply still ends in ErrResponseTooLarge and a compressed long
//     line in ErrResponseLineTooLong;
//   - the decoder passes bytes on at a zlib Z_PARTIAL_FLUSH (an empty fixed
//     block), with no further input;
//   - Close and ForceCloseConn end the inflater goroutine (no leak).
//
// There is no wall-clock assertion: cliWait (PUGLEAF_TEST_DEADLINE) and the
// kit's Options.Deadline are hang guards, and the ReadTimeout of the broken
// server test is a bound on a server that answers nothing, not a measurement.

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const cliCompGroup = "compress.test"

// cliCompMsgID is the message-id of the n-th stored article.
func cliCompMsgID(n int) string { return fmt.Sprintf("<comp%d@test.invalid>", n) }

// cliCompStore returns a store with cliCompGroup and n articles whose bodies
// are highly compressible (so the wire counters stay well below the raw ones).
func cliCompStore(n, bodyLines int) *cliFakeStore {
	st := cliFakeNewStore()
	st.AddGroup(cliCompGroup, "compress test group")
	body := make([]string, bodyLines)
	for i := range body {
		body[i] = strings.Repeat("compressible text ", 4)
	}
	for i := 1; i <= n; i++ {
		st.AddArticle(cliCompGroup, cliFakeArticle{
			MessageID: cliCompMsgID(i),
			Headers: []string{
				"From: compressor <c@test.invalid>",
				fmt.Sprintf("Subject: compress %d", i),
			},
			Body: body,
		})
	}
	return st
}

// cliCompServer starts a kit Store server that offers COMPRESS DEFLATE.
func cliCompServer(t *testing.T, n int, f cliFakeFaults) *cliFakeServer {
	t.Helper()
	return cliStoreServer(t, cliCompStore(n, 20), cliFakeStoreOpts{Compress: true}, f)
}

// cliCompDial connects with BackendConfig.Compress set, after mutate.
func cliCompDial(t *testing.T, s *cliFakeServer, mutate func(*BackendConfig)) *BackendConn {
	t.Helper()
	return cliCmdDial(t, s, func(cfg *BackendConfig) {
		cfg.Compress = true
		if mutate != nil {
			mutate(cfg)
		}
	})
}

// cliCompLayer returns the deflate layer of a compressed connection.
func cliCompLayer(t *testing.T, c *BackendConn) *deflateConn {
	t.Helper()
	if c.dconn == nil {
		t.Fatal("connection has no deadlineConn")
	}
	d, ok := c.dconn.Conn.(*deflateConn)
	if !ok {
		t.Fatalf("the connection is not compressed: %T under the deadline wrapper", c.dconn.Conn)
	}
	return d
}

// cliCompLogBuf collects log output while a test runs.
type cliCompLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *cliCompLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *cliCompLogBuf) count(sub string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), sub)
}

// cliCompCaptureLog sends the standard logger into a buffer for this test and
// clears the per-provider dedupe of the refusal line, so the count is this
// test's own.
func cliCompCaptureLog(t *testing.T) *cliCompLogBuf {
	t.Helper()
	compressRefusedMu.Lock()
	compressRefusedSeen = make(map[string]bool)
	compressRefusedMu.Unlock()

	b := &cliCompLogBuf{}
	old := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(old) })
	return b
}

// TestCliCompressRoundTrip: GROUP, ARTICLE, XHDR and XOVER over a compressed
// connection; COMPRESS goes out once and the wire counters stay below the raw
// ones.
func TestCliCompressRoundTrip(t *testing.T) {
	const articles = 6
	s := cliCompServer(t, articles, cliFakeFaults{})
	c := cliCompDial(t, s, nil)
	if !c.compressed() {
		t.Fatal("the connection is not compressed after COMPRESS DEFLATE")
	}

	var gi *GroupInfo
	if _, err := cliCmdRun(t, func() error {
		var e error
		gi, _, e = c.SelectGroup(cliCompGroup)
		return e
	}); err != nil {
		t.Fatalf("SelectGroup: %v", err)
	}
	if gi == nil || gi.Count != articles {
		t.Fatalf("GroupInfo = %+v, want %d articles", gi, articles)
	}

	msgID := cliCompMsgID(2)
	var gotID string
	if _, err := cliCmdRun(t, func() error {
		a, e := c.GetArticle(&msgID, true)
		if e != nil {
			return e
		}
		gotID = a.MessageID
		return nil
	}); err != nil {
		t.Fatalf("GetArticle: %v", err)
	}
	if gotID != msgID {
		t.Fatalf("GetArticle returned %q, want %q", gotID, msgID)
	}

	var over []OverviewLine
	if _, err := cliCmdRun(t, func() error {
		var e error
		over, e = c.XOver(cliCompGroup, 1, articles, false)
		return e
	}); err != nil {
		t.Fatalf("XOver: %v", err)
	}
	if len(over) != articles {
		t.Fatalf("XOver returned %d lines, want %d", len(over), articles)
	}

	var groups []GroupInfo
	if _, err := cliCmdRun(t, func() error {
		var e error
		groups, e = c.ListGroups()
		return e
	}); err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != cliCompGroup {
		t.Fatalf("ListGroups = %+v, want the one group %q", groups, cliCompGroup)
	}

	if n := s.Count("COMPRESS"); n != 1 {
		t.Fatalf("the server saw COMPRESS %d times, want exactly 1", n)
	}
	if c.RxBytesRaw() <= 0 || c.RxBytesWire() <= 0 || c.TxBytesRaw() <= 0 || c.TxBytesWire() <= 0 {
		t.Fatalf("counters tx %d/%d rx %d/%d (raw/wire), want all above zero",
			c.TxBytesRaw(), c.TxBytesWire(), c.RxBytesRaw(), c.RxBytesWire())
	}
	if c.RxBytesWire() >= c.RxBytesRaw() {
		t.Fatalf("rx wire %d >= rx raw %d: the replies were not compressed", c.RxBytesWire(), c.RxBytesRaw())
	}
}

// TestCliCompressRefused: a refusal keeps the connection plain and working and
// is logged once per provider, not once per connection.
func TestCliCompressRefused(t *testing.T) {
	cases := []struct {
		name  string
		opts  cliFakeStoreOpts
		fault cliFakeFaults
	}{
		{
			name:  "403",
			opts:  cliFakeStoreOpts{Compress: true},
			fault: cliFakeFaults{Override: map[string][]string{"COMPRESS": {"403 Unable to activate compression"}}},
		},
		{
			// A server without the command: the kit answers 500.
			name: "500 unknown command",
			opts: cliFakeStoreOpts{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := cliCompCaptureLog(t)
			s := cliStoreServer(t, cliCompStore(3, 4), tc.opts, tc.fault)

			for i := 0; i < 2; i++ {
				c := cliCompDial(t, s, nil)
				if c.compressed() {
					t.Fatalf("connection %d is compressed although COMPRESS was refused", i)
				}
				msgID := cliCompMsgID(1)
				if _, err := cliCmdRun(t, func() error {
					_, _, e := c.SelectGroup(cliCompGroup)
					return e
				}); err != nil {
					t.Fatalf("SelectGroup on connection %d: %v", i, err)
				}
				if _, err := cliCmdRun(t, func() error {
					_, e := c.GetArticle(&msgID, true)
					return e
				}); err != nil {
					t.Fatalf("GetArticle on connection %d: %v", i, err)
				}
				if c.RxBytesWire() != c.RxBytesRaw() || c.TxBytesWire() != c.TxBytesRaw() {
					t.Fatalf("connection %d: counters tx %d/%d rx %d/%d, want wire == raw on a plain connection",
						i, c.TxBytesRaw(), c.TxBytesWire(), c.RxBytesRaw(), c.RxBytesWire())
				}
				_ = c.CloseFromPoolOnly()
			}

			if n := logs.count("COMPRESS DEFLATE refused"); n != 1 {
				t.Fatalf("the refusal was logged %d times for two connections, want 1", n)
			}
		})
	}
}

// TestCliCompressOutputOnly: a server that deflates its output but reads its
// input plain (StoreOptions.CompressOutputOnly) is broken; the client's
// compressed command is not understood, and the command ends in an error and a
// closed pool connection instead of a hang. The ReadTimeout bounds a server
// that answers nothing at all; the assertion is the error, not its timing.
func TestCliCompressOutputOnly(t *testing.T) {
	s := cliStoreServer(t, cliCompStore(3, 4), cliFakeStoreOpts{CompressOutputOnly: true}, cliFakeFaults{})
	pool := cliCmdPool(t, s, func(cfg *BackendConfig) {
		cfg.Compress = true
		cfg.IdleExpire = -1
		cfg.ReadTimeout = 2 * time.Second
	})
	msgID := cliCompMsgID(1)
	_, err := cliCmdRun(t, func() error { _, e := pool.GetArticle(&msgID, true); return e })
	if err == nil {
		t.Fatal("GetArticle succeeded against a server that never inflates the client's commands")
	}
	s.WaitOpen(t, 0, cliWait) // the broken connection is closed, not parked
	cliSessWaitStats(t, pool, "no idle connection after the broken COMPRESS",
		func(st PoolStats) bool { return st.IdleConnections == 0 })
}

// TestCliCompressPool: a Pool with Compress serves articles over compressed
// connections.
func TestCliCompressPool(t *testing.T) {
	const articles = 5
	s := cliCompServer(t, articles, cliFakeFaults{})
	pool := cliCmdPool(t, s, func(cfg *BackendConfig) {
		cfg.Compress = true
		cfg.IdleExpire = -1
	})
	for i := 1; i <= articles; i++ {
		id := cliCompMsgID(i)
		var got string
		if _, err := cliCmdRun(t, func() error {
			a, e := pool.GetArticle(&id, true)
			if e != nil {
				return e
			}
			got = a.MessageID
			return nil
		}); err != nil {
			t.Fatalf("pool.GetArticle %s: %v", id, err)
		}
		if got != id {
			t.Fatalf("pool.GetArticle returned %q, want %q", got, id)
		}
	}
	if n := s.Count("COMPRESS"); n != s.Accepted() {
		t.Fatalf("COMPRESS seen %d times on %d connections, want one per connection", n, s.Accepted())
	}
}

// TestCliCompressMaxConns: the server-side connection cap is still reported as
// the "502 Too many connections" greeting when Compress is on (COMPRESS comes
// after the greeting, so the refusal path is unchanged).
func TestCliCompressMaxConns(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{
		Store:     cliCompStore(2, 4),
		StoreOpts: cliFakeStoreOpts{Compress: true},
		MaxConns:  1,
	})
	pool := cliCmdPool(t, s, func(cfg *BackendConfig) {
		cfg.Compress = true
		cfg.IdleExpire = -1
	})
	var c1 *BackendConn
	if _, err := cliCmdRun(t, func() error {
		var e error
		c1, e = pool.Get(MODE_READER_MV)
		return e
	}); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if !c1.compressed() {
		t.Fatal("the first connection is not compressed")
	}
	_, err := cliCmdRun(t, func() error {
		c, e := pool.Get(MODE_READER_MV)
		if c != nil {
			_ = pool.Put(c)
		}
		return e
	})
	if err == nil {
		t.Fatal("the second Get succeeded although the server allows one connection")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("second Get: err = %v, want the 502 greeting in it", err)
	}
	_ = pool.Put(c1)
}

// TestCliCompressInflatedBounds: the hostile-peer bounds measure the inflated
// stream, so a compressed endless reply still hits the octet budget and a
// compressed over-long line still hits the response line cap, although both
// are a few hundred bytes on the wire.
func TestCliCompressInflatedBounds(t *testing.T) {
	msgID := cliCompMsgID(1)

	t.Run("endless", func(t *testing.T) {
		s := cliCompServer(t, 3, cliFakeFaults{Endless: map[string]bool{"ARTICLE": true}})
		c := cliCompDial(t, s, func(cfg *BackendConfig) {
			cfg.MaxArticleResponseBytes = 8 << 10
		})
		_, err := cliCmdRun(t, func() error { _, e := c.GetArticle(&msgID, true); return e })
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("GetArticle on an endless compressed reply: err = %v, want ErrResponseTooLarge", err)
		}
	})

	// A long but very compressible article: the octet budget fires on the
	// inflated octets although far fewer crossed the wire.
	t.Run("octet budget", func(t *testing.T) {
		st := cliCompStore(1, 4000) // ~300 KB inflated, a few KB on the wire
		s := cliStoreServer(t, st, cliFakeStoreOpts{Compress: true}, cliFakeFaults{})
		c := cliCompDial(t, s, func(cfg *BackendConfig) {
			cfg.MaxArticleResponseBytes = 16 << 10
		})
		_, err := cliCmdRun(t, func() error { _, e := c.GetArticle(&msgID, true); return e })
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("GetArticle over the octet budget: err = %v, want ErrResponseTooLarge", err)
		}
		if c.RxBytesWire() >= c.RxBytesRaw() {
			t.Fatalf("rx wire %d >= rx raw %d: the budget did not measure the inflated stream",
				c.RxBytesWire(), c.RxBytesRaw())
		}
	})

	t.Run("long line", func(t *testing.T) {
		s := cliCompServer(t, 3, cliFakeFaults{LongLine: map[string]int{"ARTICLE": 4096}})
		c := cliCompDial(t, s, func(cfg *BackendConfig) {
			cfg.MaxResponseLineBytes = 512
		})
		_, err := cliCmdRun(t, func() error { _, e := c.GetArticle(&msgID, true); return e })
		if !errors.Is(err, ErrResponseLineTooLong) {
			t.Fatalf("GetArticle on a compressed over-long line: err = %v, want ErrResponseLineTooLong", err)
		}
		if c.RxBytesWire() >= c.RxBytesRaw() {
			t.Fatalf("rx wire %d >= rx raw %d: the line cap did not measure the inflated stream",
				c.RxBytesWire(), c.RxBytesRaw())
		}
	})
}

// TestCliCompressXHdr: XHDR over a compressed connection. The kit's Store
// handler has no XHDR, so this runs against a scripted server whose COMPRESS
// step switches the connection (fakeserver.Step.Compress).
func TestCliCompressXHdr(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "COMPRESS DEFLATE", Reply: []string{"206 Compression active"}, Compress: true},
		{Expect: "GROUP .*", Reply: []string{"211 3 1 3 " + cliCompGroup}},
		{Expect: "XHDR .*", Reply: []string{"221 headers follow",
			"1 " + strings.Repeat("subject one ", 32),
			"2 " + strings.Repeat("subject two ", 32),
			"3 " + strings.Repeat("subject three ", 32), "."}},
	}})
	c := cliCompDial(t, s, nil)
	if !c.compressed() {
		t.Fatal("the connection is not compressed after the scripted 206")
	}
	var hdrs []HeaderLine
	if _, err := cliCmdRun(t, func() error {
		var e error
		hdrs, e = c.XHdr(cliCompGroup, "Subject", 1, 3)
		return e
	}); err != nil {
		t.Fatalf("XHdr: %v", err)
	}
	if len(hdrs) != 3 {
		t.Fatalf("XHdr returned %d lines, want 3", len(hdrs))
	}
	if !strings.HasPrefix(hdrs[0].Value, "subject one ") || hdrs[2].ArticleNum != 3 {
		t.Fatalf("XHdr lines = %+v", hdrs)
	}
	if c.RxBytesWire() >= c.RxBytesRaw() {
		t.Fatalf("rx wire %d >= rx raw %d: the XHDR reply was not compressed", c.RxBytesWire(), c.RxBytesRaw())
	}
}

// TestCliCompressClose: Close and ForceCloseConn end the inflater goroutine,
// and Close is idempotent.
func TestCliCompressClose(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		s := cliCompServer(t, 2, cliFakeFaults{})
		c := cliCompDial(t, s, nil)
		d := cliCompLayer(t, c)
		if err := c.CloseFromPoolOnly(); err != nil {
			t.Fatalf("CloseFromPoolOnly: %v", err)
		}
		cliCompWaitInflater(t, d)
		if err := d.Close(); err != nil {
			t.Fatalf("second Close: %v, want nil (idempotent)", err)
		}
	})

	t.Run("force close", func(t *testing.T) {
		s := cliCompServer(t, 2, cliFakeFaults{})
		c := cliCompDial(t, s, nil)
		d := cliCompLayer(t, c)
		c.ForceCloseConn()
		c.ForceCloseConn() // idempotent
		cliCompWaitInflater(t, d)
	})
}

// cliCompWaitInflater fails unless the inflater goroutine of d has returned
// within the hang guard.
func cliCompWaitInflater(t *testing.T, d *deflateConn) {
	t.Helper()
	select {
	case <-d.done:
	case <-time.After(cliWait):
		t.Fatalf("the inflater goroutine is still running %v after the close", cliWait)
	}
}

// TestCliCompressNoGoroutineLeak: opening and closing compressed connections
// leaves no inflater behind; runtime.NumGoroutine settles back.
func TestCliCompressNoGoroutineLeak(t *testing.T) {
	s := cliCompServer(t, 2, cliFakeFaults{})
	msgID := cliCompMsgID(1)
	open := func() {
		c := cliCompDial(t, s, nil)
		d := cliCompLayer(t, c)
		if _, err := cliCmdRun(t, func() error { _, e := c.GetArticle(&msgID, true); return e }); err != nil {
			t.Fatalf("GetArticle: %v", err)
		}
		_ = c.CloseFromPoolOnly()
		cliCompWaitInflater(t, d)
	}
	open() // warm up: the kit's per-connection goroutines of the first dial
	s.WaitOpen(t, 0, cliWait)
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		open()
	}
	s.WaitOpen(t, 0, cliWait)
	deadline := time.Now().Add(cliWait)
	for {
		runtime.Gosched()
		now := runtime.NumGoroutine()
		if now <= before+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d after 5 compressed connections, was %d before (within %v)", now, before, cliWait)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// cliCompBits writes raw DEFLATE bits, the first one lowest; Huffman codes are
// written most significant bit first (as in internal/nntp/inflate).
type cliCompBits struct {
	out  []byte
	bits uint32
	nb   uint
}

func (w *cliCompBits) put(v uint32, n uint) {
	w.bits |= v << w.nb
	w.nb += n
	for w.nb >= 8 {
		w.out = append(w.out, byte(w.bits))
		w.bits >>= 8
		w.nb -= 8
	}
}

// code writes an n-bit Huffman code, most significant bit first.
func (w *cliCompBits) code(v uint32, n uint) {
	var r uint32
	for i := uint(0); i < n; i++ {
		r = r<<1 | (v>>i)&1
	}
	w.put(r, n)
}

// lit writes s as fixed-code literals (every byte is below 144).
func (w *cliCompBits) lit(s string) {
	for _, c := range []byte(s) {
		w.code(0x30+uint32(c), 8)
	}
}

func (w *cliCompBits) eob() { w.code(0, 7) }

// TestCliCompressPartialFlush: a reply followed by zlib's Z_PARTIAL_FLUSH (an
// empty fixed block, what INN's nnrpd sends) is readable at once, although the
// last bits of that block are not even sent and no further input arrives.
// compress/flate's own reader would hold the reply back here.
func TestCliCompressPartialFlush(t *testing.T) {
	const reply = "211 6 1 6 compress.test\r\n"
	var w cliCompBits
	w.put(0, 1) // not the final block
	w.put(1, 2) // fixed Huffman codes
	w.lit(reply)
	w.eob()
	w.put(0, 1) // the empty fixed block of a partial flush...
	w.put(1, 2)
	w.eob() // ...whose last bits stay in the partial byte and are not sent

	cli, srv := net.Pipe()
	t.Cleanup(func() { _ = srv.Close() })
	d, err := newDeflateConn(cli, nil, nil, nil)
	if err != nil {
		t.Fatalf("newDeflateConn: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	stream := w.out
	werr := make(chan error, 1)
	go func() {
		_, err := srv.Write(stream)
		werr <- err
		// nothing else is sent: the reply must be readable from these bytes
	}()

	got := make(chan string, 1)
	rerr := make(chan error, 1)
	go func() {
		buf := make([]byte, len(reply))
		n, err := io.ReadFull(d, buf)
		if err != nil {
			rerr <- err
			return
		}
		got <- string(buf[:n])
	}()
	select {
	case line := <-got:
		if line != reply {
			t.Fatalf("Read = %q, want %q", line, reply)
		}
	case err := <-rerr:
		t.Fatalf("Read before more input arrived: %v", err)
	case <-time.After(cliWait):
		t.Fatalf("the partially flushed reply was not readable within %v", cliWait)
	}
	if err := <-werr; err != nil {
		t.Fatalf("writing the hand-made stream: %v", err)
	}
}

// TestCliCompressCorruptInput: plain text where the compressed stream should
// be is a read error wrapping flate.CorruptInputError, and the force-close
// hook runs.
func TestCliCompressCorruptInput(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { _ = srv.Close() })
	forced := make(chan struct{})
	var once sync.Once
	d, err := newDeflateConn(cli, nil, nil, func() { once.Do(func() { close(forced) }) })
	if err != nil {
		t.Fatalf("newDeflateConn: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	go func() { _, _ = srv.Write([]byte("\xff\xff\xff\xff430 No such article\r\n")) }()

	rerr := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		_, err := d.Read(buf)
		rerr <- err
	}()
	select {
	case err := <-rerr:
		var ce flate.CorruptInputError
		if !errors.As(err, &ce) {
			t.Fatalf("Read on a corrupt stream: err = %v, want a flate.CorruptInputError", err)
		}
	case <-time.After(cliWait):
		t.Fatalf("no read error within %v on a corrupt compressed stream", cliWait)
	}
	select {
	case <-forced:
	case <-time.After(cliWait):
		t.Fatalf("the force-close hook did not run within %v", cliWait)
	}
	cliCompWaitInflater(t, d)
}
