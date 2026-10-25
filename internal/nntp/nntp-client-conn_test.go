package nntp

// Client connection tests of plan nntp-client-transfer, slice cli-conn:
// deadlines and socket leaks (CLI-3), demuxer start after AUTHINFO (CLI-4),
// idempotent force-close and pool accounting (A-nntp-client-11), pool wait
// and idle expiry (A-nntp-client-4) and the nil-Provider guard (A-tools-3).
// All servers are cliFakeServer instances on 127.0.0.1:0.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
)

// cliConnOuter is the outer deadline of every blocking step in these tests:
// a hang guard only (cliWait), never the assertion.
var cliConnOuter = cliWait

// cliConnIsTimeout reports whether err is a timeout: a net.Error with
// Timeout() (read/write deadline, dial timeout, SOCKS OpError) or
// context.DeadlineExceeded (TLS HandshakeContext, GetContext).
func cliConnIsTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

type cliConnResult struct {
	err       error
	elapsed   time.Duration
	recovered any
}

// cliConnRun runs f in its own goroutine and returns how long it took and its
// error. The test fails when f panics or has not returned within cliConnOuter.
func cliConnRun(t *testing.T, f func() error) (time.Duration, error) {
	t.Helper()
	done := make(chan cliConnResult, 1)
	start := time.Now()
	go func() {
		var res cliConnResult
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
	case <-time.After(cliConnOuter):
		t.Fatalf("still blocked after %v", cliConnOuter)
		return 0, nil
	}
}

// cliConnConnect dials cfg with NewConn+Connect inside cliConnRun; a
// successful connection is closed by t.Cleanup.
func cliConnConnect(t *testing.T, cfg *BackendConfig) (*BackendConn, time.Duration, error) {
	t.Helper()
	c := NewConn(cfg)
	elapsed, err := cliConnRun(t, c.Connect)
	if err == nil {
		t.Cleanup(func() { _ = c.CloseFromPoolOnly() })
	}
	return c, elapsed, err
}

// cliConnPool builds a pool on cfg that is closed by t.Cleanup.
func cliConnPool(t *testing.T, cfg *BackendConfig) *Pool {
	t.Helper()
	pool := NewPool(cfg)
	t.Cleanup(func() { _ = pool.ClosePool() })
	return pool
}

// cliConnGet runs pool.Get inside cliConnRun.
func cliConnGet(t *testing.T, pool *Pool, mode int) (*BackendConn, time.Duration, error) {
	t.Helper()
	var conn *BackendConn
	elapsed, err := cliConnRun(t, func() error {
		var err error
		conn, err = pool.Get(mode)
		return err
	})
	return conn, elapsed, err
}

// CLI-3: a server that accepts but never greets must not hang Connect, and the
// socket must be closed.
func TestCliConnGreetingTimeout(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{NoGreeting: true})
	cfg := cliBackend(s)
	cfg.ConnectTimeout = 300 * time.Millisecond
	_, elapsed, err := cliConnConnect(t, cfg)
	if err == nil {
		t.Fatal("Connect succeeded without a greeting")
	}
	// the greeting read is bounded by ConnectTimeout: a timeout error, not a
	// hang (cliConnOuter) and not some other failure
	if !cliConnIsTimeout(err) {
		t.Fatalf("Connect error %v (after %v), want a timeout (ConnectTimeout 300ms)", err, elapsed)
	}
	// loose bound, deliberately 50x the 300 ms: catches a fallback to
	// config.DefaultConnectTimeout (30 s) instead of the configured value
	if elapsed >= config.DefaultConnectTimeout/2 {
		t.Fatalf("Connect took %v: ConnectTimeout 300ms ignored (default %v?)", elapsed, config.DefaultConnectTimeout)
	}
	s.WaitOpen(t, 0, 2*time.Second)
}

// CLI-3: a non-2xx greeting returns an error and closes the socket.
func TestCliConnBadGreetingClosesSocket(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Greeting: "400 go away"})
	_, _, err := cliConnConnect(t, cliBackend(s))
	if err == nil {
		t.Fatal("Connect accepted a 400 greeting")
	}
	s.WaitOpen(t, 0, 2*time.Second)
}

// CLI-3: a failed AUTHINFO closes the socket, before the back-off sleep.
func TestCliConnAuthFailureClosesSocket(t *testing.T) {
	script := []cliFakeStep{
		{Expect: "AUTHINFO USER .*", Reply: []string{"381 password required"}},
		{Expect: "AUTHINFO PASS .*", Reply: []string{"481 authentication failed"}},
	}
	t.Run("no-backoff", func(t *testing.T) {
		s := newCliFakeServer(t, cliFakeOpts{Script: script})
		cfg := cliBackend(s)
		cfg.Username, cfg.Password = "u", "wrong"
		cfg.AuthFailureBackoff = -1
		_, elapsed, err := cliConnConnect(t, cfg)
		if err == nil {
			t.Fatal("Connect succeeded after 481")
		}
		if !errors.Is(err, ErrAuthRejected) {
			t.Fatalf("Connect error %v, want ErrAuthRejected", err)
		}
		// -1 disables the back-off: Connect must not have slept the default
		// one (15 s; a passing Connect takes milliseconds, >1000x margin).
		if elapsed >= DefaultAuthFailureBackoff {
			t.Fatalf("Connect took %v with AuthFailureBackoff -1: it slept the default back-off %v", elapsed, DefaultAuthFailureBackoff)
		}
		s.WaitOpen(t, 0, 2*time.Second)
	})
	t.Run("backoff-after-close", func(t *testing.T) {
		const backoff = 3 * time.Second
		s := newCliFakeServer(t, cliFakeOpts{Script: script})
		cfg := cliBackend(s)
		cfg.Username, cfg.Password = "u", "wrong"
		cfg.AuthFailureBackoff = backoff
		c := NewConn(cfg)
		done := make(chan error, 1)
		start := time.Now()
		go func() { done <- c.Connect() }()
		// the socket is gone while Connect still sleeps
		s.WaitLines(t, []string{"AUTHINFO USER u", "AUTHINFO PASS wrong"})
		s.WaitOpen(t, 0, 2*time.Second)
		// deliberate short negative window: the socket closed while Connect
		// still sleeps; the 3 s back-off leaves room for a loaded machine
		select {
		case err := <-done:
			t.Fatalf("Connect returned before the back-off ended (after %v): %v", time.Since(start), err)
		default:
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Connect succeeded after 481")
			}
			if el := time.Since(start); el < backoff {
				t.Fatalf("Connect returned after %v, want >= %v", el, backoff)
			}
		case <-time.After(cliConnOuter):
			t.Fatalf("Connect still blocked after %v", cliConnOuter)
		}
	})
}

// CLI-3: the TLS handshake is bounded by ConnectTimeout.
func TestCliConnTLSHandshakeTimeout(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{RawStall: true})
	cfg := cliBackend(s)
	cfg.SSL = true
	cfg.ConnectTimeout = 300 * time.Millisecond
	_, elapsed, err := cliConnConnect(t, cfg)
	if err == nil {
		t.Fatal("Connect succeeded against a stalled TLS server")
	}
	if !strings.Contains(err.Error(), "TLS handshake failed") {
		t.Errorf("error %q does not mention the TLS handshake", err)
	}
	// the handshake is bounded by ConnectTimeout: a timeout error, not a hang
	if !cliConnIsTimeout(err) {
		t.Errorf("Connect error %v (after %v), want a timeout (ConnectTimeout 300ms)", err, elapsed)
	}
	// loose bound, deliberately 50x the 300 ms: catches a fallback to
	// config.DefaultConnectTimeout (30 s) instead of the configured value
	if elapsed >= config.DefaultConnectTimeout/2 {
		t.Fatalf("Connect took %v: ConnectTimeout 300ms ignored (default %v?)", elapsed, config.DefaultConnectTimeout)
	}
	s.WaitOpen(t, 0, 2*time.Second)
}

// A self-signed server certificate is rejected (no production hook for custom roots).
func TestCliConnTLSUnknownCA(t *testing.T) {
	serverTLS, _ := cliFakeTLSConfig(t)
	s := newCliFakeServer(t, cliFakeOpts{TLS: serverTLS})
	cfg := cliBackend(s) // ConnectTimeout 30 s: the rejection must not depend on it
	cfg.SSL = true
	_, _, err := cliConnConnect(t, cfg)
	if err == nil {
		t.Fatal("Connect accepted a certificate from an unknown CA")
	}
	if !strings.Contains(err.Error(), "TLS handshake failed") {
		t.Fatalf("error %q, want it to contain %q", err, "TLS handshake failed")
	}
	if cliConnIsTimeout(err) {
		t.Fatalf("error %v is a timeout, want a certificate rejection", err)
	}
}

// CLI-3: a server that stops in the middle of a response trips ReadTimeout.
func TestCliConnReadIdleTimeout(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "ARTICLE .*", Reply: []string{"220 0 <a@b>", "Path: x"}, Hang: true},
	}})
	cfg := cliBackend(s)
	cfg.ReadTimeout = 300 * time.Millisecond
	c, _, err := cliConnConnect(t, cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	msgID := "<a@b>"
	elapsed, err := cliConnRun(t, func() error {
		_, err := c.GetArticle(&msgID, false)
		return err
	})
	if err == nil {
		t.Fatal("GetArticle succeeded on a truncated response")
	}
	// bounded by ReadTimeout: a timeout error, not a hang (cliConnOuter)
	if !cliConnIsTimeout(err) {
		t.Fatalf("GetArticle error %v (after %v), want a timeout (ReadTimeout 300ms)", err, elapsed)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("GetArticle failed after %v (%v): before the read timeout, wrong failure", elapsed, err)
	}
}

// CLI-4: the first CHECK registered with a demuxer is answered whatever ran
// on the connection before (AUTHINFO USER/PASS and MODE STREAM shift the
// textproto ids by two).
func TestCliConnDemuxerAfterAuth(t *testing.T) {
	run := func(t *testing.T, user, pass string) {
		s := newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(&cliPeerOpts{User: user, Pass: pass})})
		cfg := cliBackend(s)
		cfg.Username, cfg.Password = user, pass
		cfg.AuthFailureBackoff = -1
		pool := cliConnPool(t, cfg)
		conn, _, err := cliConnGet(t, pool, MODE_STREAM_MV)
		if err != nil {
			t.Fatalf("Get(MODE_STREAM_MV): %v", err)
		}
		errChan := make(chan struct{}, 9)
		var d *ResponseDemuxer
		if _, err := cliConnRun(t, func() error { d = NewResponseDemuxer(conn, errChan); return nil }); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			common.SignalErrChan(errChan)
			conn.ForceCloseConn()
		})
		d.Start()

		const msgID = "<cli4@test.invalid>"
		conn.Lock()
		id, err := conn.TextConn.Cmd("CHECK %s", msgID)
		if err == nil {
			d.RegisterCommand(id, TYPE_CHECK)
		}
		conn.Unlock()
		if err != nil {
			t.Fatalf("send CHECK: %v", err)
		}
		select {
		case r := <-d.GetCheckResponseChan():
			if r.Code != 238 || r.CmdID != id {
				t.Fatalf("CHECK response: id=%d code=%d line=%q err=%v, want id=%d code=238", r.CmdID, r.Code, r.Line, r.Err, id)
			}
		case <-time.After(cliConnOuter):
			t.Fatalf("no CHECK response within %v (cmd id %d): demuxer stuck in its backlog", cliConnOuter, id)
		}
		want := []string{"MODE STREAM", "CHECK " + msgID}
		if user != "" {
			want = append([]string{"AUTHINFO USER " + user, "AUTHINFO PASS " + pass}, want...)
		}
		s.WaitLines(t, want)
	}
	t.Run("authenticated", func(t *testing.T) { run(t, "u", "p") })
	t.Run("unauthenticated", func(t *testing.T) { run(t, "", "") })
}

// A-nntp-client-11: ForceCloseConn three times closes once and frees exactly one slot.
// The kit's transit peer answers MODE READER with 502 since v0.8.0 (innd with reader access denied), so the
// pool tests below fetch streaming connections: the mode is irrelevant to what they pin.
func TestCliConnForceCloseIdempotent(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(&cliPeerOpts{})})
	cfg := cliBackend(s)
	cfg.MaxConns = 2
	cfg.PoolWaitTimeout = 200 * time.Millisecond
	pool := cliConnPool(t, cfg)
	conn, _, err := cliConnGet(t, pool, MODE_STREAM_MV)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	conn.ForceCloseConn()
	conn.ForceCloseConn()
	conn.ForceCloseConn()

	deadline := time.Now().Add(cliConnOuter)
	for {
		active := pool.Stats().ActiveConnections
		if active < 0 {
			t.Fatalf("ActiveConnections = %d: the slot was freed more than once", active)
		}
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ActiveConnections = %d, want 0 within %v", active, cliConnOuter)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// deliberate short negative window: the count must stay at 0 for 200 ms
	steady := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(steady) {
		if active := pool.Stats().ActiveConnections; active != 0 {
			t.Fatalf("ActiveConnections = %d after settling, want 0", active)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := pool.Stats(); st.TotalClosed != 1 {
		t.Fatalf("TotalClosed = %d, want 1", st.TotalClosed)
	}
	s.WaitOpen(t, 0, 2*time.Second)

	c1, _, err := cliConnGet(t, pool, MODE_STREAM_MV)
	if err != nil {
		t.Fatalf("Get #1 after force-close: %v", err)
	}
	c2, _, err := cliConnGet(t, pool, MODE_STREAM_MV)
	if err != nil {
		t.Fatalf("Get #2 after force-close: %v", err)
	}
	c3, elapsed, err := cliConnGet(t, pool, MODE_STREAM_MV)
	if err == nil {
		_ = pool.Put(c3)
		t.Fatalf("Get #3 succeeded with MaxConns 2 and both conns held (stats %+v)", pool.Stats())
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Get #3 error %q, want a pool timeout", err)
	}
	// lower bound only: it waited for PoolWaitTimeout; the upper end is the
	// "timeout" error above plus the cliConnOuter hang guard
	if elapsed < 150*time.Millisecond {
		t.Errorf("Get #3 gave up after %v, want >= 150ms (PoolWaitTimeout 200ms)", elapsed)
	}
	_ = pool.Put(c1)
	_ = pool.Put(c2)
}

// A-nntp-client-4: GetContext stops waiting for a slot when its context ends.
func TestCliConnGetContextCancel(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(&cliPeerOpts{})})
	cfg := cliBackend(s)
	cfg.MaxConns = 1
	pool := cliConnPool(t, cfg)
	held, _, err := cliConnGet(t, pool, MODE_STREAM_MV)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = cliConnRun(t, func() error {
		c, err := pool.GetContext(ctx, MODE_STREAM_MV)
		if err == nil {
			_ = pool.Put(c)
			return fmt.Errorf("GetContext returned a connection with MaxConns 1 held")
		}
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetContext error %v, want context.DeadlineExceeded", err)
	}
	_ = pool.Put(held)
}

// A-nntp-client-4: an idle pooled connection older than IdleExpire is replaced.
func TestCliConnIdleExpire(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(&cliPeerOpts{})})
	cfg := cliBackend(s)
	cfg.IdleExpire = 100 * time.Millisecond
	pool := cliConnPool(t, cfg)
	c, _, err := cliConnGet(t, pool, MODE_STREAM_MV)
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	if err := pool.Put(c); err != nil {
		t.Fatalf("Put: %v", err)
	}
	time.Sleep(250 * time.Millisecond) // the idle expiry under test
	c2, _, err := cliConnGet(t, pool, MODE_STREAM_MV)
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	st := pool.Stats()
	if st.TotalCreated != 2 || st.TotalClosed != 1 || st.ActiveConnections != 1 {
		t.Fatalf("stats %+v, want TotalCreated 2, TotalClosed 1, ActiveConnections 1", st)
	}
	_ = pool.Put(c2)
}

// A-tools-3: a BackendConfig without Provider must not panic on error paths.
func TestCliConnNilProvider(t *testing.T) {
	dead := newCliFakeServer(t, cliFakeOpts{})
	port := dead.Port()
	dead.Close() // the port is now closed: connects are refused

	cfg := &BackendConfig{Host: "127.0.0.1", Port: port, ConnectTimeout: 300 * time.Millisecond, MaxConns: 1}
	pool := cliConnPool(t, cfg)
	if _, _, err := cliConnGet(t, pool, MODE_READER_MV); err == nil {
		t.Fatal("Get succeeded against a closed port")
	}
	if st := pool.Stats(); st.ActiveConnections != 0 {
		t.Fatalf("ActiveConnections = %d after a failed connect, want 0", st.ActiveConnections)
	}
	cacheDir := t.TempDir()
	if _, err := cliConnRun(t, func() error {
		_, err := pool.FileCachedListNewsgroups(cacheDir)
		return err
	}); err == nil {
		t.Fatal("FileCachedListNewsgroups succeeded against a closed port")
	}

	onion := &BackendConfig{Host: "x.onion", Port: 119, ConnectTimeout: 300 * time.Millisecond, MaxConns: 1}
	_, _, err := cliConnConnect(t, onion)
	if err == nil {
		t.Fatal("Connect to x.onion without proxy succeeded")
	}
	if !strings.Contains(err.Error(), "x.onion:119") {
		t.Errorf("error %q does not name the backend", err)
	}
}
