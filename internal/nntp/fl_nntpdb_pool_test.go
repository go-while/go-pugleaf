package nntp

// Pool tests of plan followup-leftovers, slice fl-nntp-db: XHdrStreamedWait returns the
// stream's error (a cut-off XHDR stream is not a complete result) and ClosePool frees every
// slot before it reports (no "positive count" after a clean run). cliFakeServer on 127.0.0.1:0.

import (
	"strings"
	"testing"
	"time"
)

// flNntpdbXHdrScript answers MODE READER, GROUP and an XHDR with the given reply lines.
func flNntpdbXHdrScript(xhdrReply []string, hang bool) []cliFakeStep {
	return []cliFakeStep{
		{Expect: "MODE READER", Reply: []string{"200 reader"}},
		{Expect: "GROUP .*", Reply: []string{"211 3 1 3 g"}},
		{Expect: "XHDR .*", Reply: xhdrReply, Hang: hang},
	}
}

// flNntpdbStream runs pool.XHdrStreamedWait with a reader goroutine and returns the lines and
// the error. Both must arrive within cliCmdOuter.
func flNntpdbStream(t *testing.T, pool *Pool) ([]HeaderLine, error) {
	t.Helper()
	ch := make(chan HeaderLine, 1) // smaller than the reply: the stream must not need a big buffer
	lines := make(chan []HeaderLine, 1)
	go func() {
		var got []HeaderLine
		for h := range ch {
			got = append(got, h)
		}
		lines <- got
	}()
	_, err := cliCmdRun(t, func() error { return pool.XHdrStreamedWait("g", "Message-ID", 1, 3, ch, nil) })
	select {
	case got := <-lines:
		return got, err
	case <-time.After(cliCmdOuter):
		t.Fatalf("xhdr channel not closed within %v after XHdrStreamedWait returned", cliCmdOuter)
		return nil, err
	}
}

// TestFlNntpdbXHdrStreamedWaitComplete: a complete stream returns nil after every line was
// delivered, and the connection goes back to the pool.
func TestFlNntpdbXHdrStreamedWaitComplete(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: flNntpdbXHdrScript(
		[]string{"221 ok", "1 <a@test.invalid>", "2 <b@test.invalid>", "3 <c@test.invalid>", "."}, false)})
	pool := cliCmdPool(t, s, nil)
	got, err := flNntpdbStream(t, pool)
	if err != nil {
		t.Fatalf("XHdrStreamedWait: %v", err)
	}
	if len(got) != 3 || got[2].Value != "<c@test.invalid>" {
		t.Fatalf("lines %+v, want 3 ending with <c@test.invalid>", got)
	}
	if idle := pool.Stats().IdleConnections; idle != 1 {
		t.Errorf("IdleConnections = %d, want 1 (a complete stream returns the connection)", idle)
	}
}

// TestFlNntpdbXHdrStreamedWaitCutOff: a stream cut off before its terminating dot returns the
// error (the caller must not treat the range as complete) and the connection is closed.
func TestFlNntpdbXHdrStreamedWaitCutOff(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: flNntpdbXHdrScript(
		[]string{"221 ok", "1 <a@test.invalid>"}, true)})
	pool := cliCmdPool(t, s, func(cfg *BackendConfig) { cfg.ReadTimeout = 300 * time.Millisecond })
	got, err := flNntpdbStream(t, pool)
	if err == nil {
		t.Fatalf("XHdrStreamedWait returned nil for a cut-off stream (%d lines)", len(got))
	}
	if !strings.Contains(err.Error(), "XHDR stream") {
		t.Errorf("error %v, want the stream read error", err)
	}
	if len(got) != 1 {
		t.Errorf("lines %+v, want the 1 line before the cut", got)
	}
	s.WaitOpen(t, 0, cliWait)
	if idle := pool.Stats().IdleConnections; idle != 0 {
		t.Errorf("IdleConnections = %d, want 0 (the cut-off connection is closed)", idle)
	}
}

// TestFlNntpdbClosePoolCountsEveryConn: after a clean run - one connection back in the pool,
// one handed back with ForceCloseConn (asynchronous) just before - ClosePool returns with no
// active connection left, so it logs no "positive count" warning.
func TestFlNntpdbClosePoolCountsEveryConn(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "MODE READER", Reply: []string{"200 reader"}},
	}})
	pool := cliCmdPool(t, s, nil)
	a, _, err := cliConnGet(t, pool, MODE_READER_MV)
	if err != nil {
		t.Fatalf("Get a: %v", err)
	}
	b, _, err := cliConnGet(t, pool, MODE_READER_MV)
	if err != nil {
		t.Fatalf("Get b: %v", err)
	}
	if st := pool.Stats(); st.ActiveConnections != 2 {
		t.Fatalf("ActiveConnections = %d, want 2", st.ActiveConnections)
	}
	if err := pool.Put(a); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	b.ForceCloseConn()
	if _, err := cliCmdRun(t, pool.ClosePool); err != nil {
		t.Fatalf("ClosePool: %v", err)
	}
	st := pool.Stats()
	if st.ActiveConnections != 0 {
		t.Errorf("ActiveConnections = %d right after ClosePool, want 0 (the warning would fire)", st.ActiveConnections)
	}
	if st.TotalCreated != 2 || st.TotalClosed != 2 {
		t.Errorf("created %d closed %d, want 2 and 2", st.TotalCreated, st.TotalClosed)
	}
	s.WaitOpen(t, 0, cliWait)
}
