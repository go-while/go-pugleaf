package nntp

import (
	"errors"
	"strings"
	"testing"
)

// TestCliTxNoStreamPeer: the transfer client meets a transit peer without
// streaming (kit PeerOptions.NoStream: innd with streaming turned off, MODE
// STREAM answers 501, CHECK and TAKETHIS are unknown commands and the article
// lines after a TAKETHIS would be read as commands).
//
// go-pugleaf's transfer path is CHECK/TAKETHIS only (cmd/nntp-transfer, no
// IHAVE fallback), so the pinned behaviour is: Pool.Get(MODE_STREAM_MV) fails
// with the 501 in the error, the connection is closed and not counted as open,
// and the peer never sees CHECK, TAKETHIS, IHAVE or POST. cmd/nntp-transfer's
// BootConnWorkers then retries the Get with a backoff of up to one minute for
// as long as the tool runs; that it never gives up on such a peer is a
// decision, not covered here.
func TestCliTxNoStreamPeer(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(&cliPeerOpts{
		NoStream: true,
		Have:     make(map[string]bool),
	})})
	pool := cliSessPool(t, s, nil)

	conn, err := pool.Get(MODE_STREAM_MV)
	if err == nil {
		conn.ForceCloseConn()
		t.Fatal("Pool.Get(MODE_STREAM_MV) succeeded against a peer without streaming")
	}
	if !errors.Is(err, ErrStreamingUnsupported) || !strings.Contains(err.Error(), "501") {
		t.Fatalf("Pool.Get error = %q, want ErrStreamingUnsupported with the peer's 501 in it", err)
	}
	cliSessWaitStats(t, pool, "the failed connection closed and not counted", func(st PoolStats) bool {
		return st.TotalCreated >= 1 && st.TotalCreated == st.TotalClosed &&
			st.ActiveConnections == 0 && st.IdleConnections == 0
	})

	// a second attempt behaves the same: no retry loop inside Get, no leaked slot
	if conn, err = pool.Get(MODE_STREAM_MV); err == nil {
		conn.ForceCloseConn()
		t.Fatal("second Pool.Get(MODE_STREAM_MV) succeeded against a peer without streaming")
	}
	cliSessWaitStats(t, pool, "both failed connections closed", func(st PoolStats) bool {
		return st.TotalCreated >= 2 && st.TotalCreated == st.TotalClosed && st.ActiveConnections == 0
	})

	sawModeStream := false
	for _, line := range s.Lines() {
		verb := strings.ToUpper(strings.Fields(line + " ")[0])
		switch verb {
		case "CHECK", "TAKETHIS", "IHAVE", "POST":
			t.Fatalf("client sent %q to a peer without streaming after MODE STREAM failed", line)
		case "MODE":
			if strings.EqualFold(strings.TrimSpace(line), "MODE STREAM") {
				sawModeStream = true
			}
		}
	}
	if !sawModeStream {
		t.Fatalf("peer never saw MODE STREAM; lines: %q", s.Lines())
	}
}

// TestCliTxNoStreamConn: the same 501 on a bare BackendConn. SwitchToModeStream
// reports it and leaves the connection in neither mode, so a later
// Pool.Get(MODE_STREAM_MV) on it would try again instead of streaming blindly.
func TestCliTxNoStreamConn(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(&cliPeerOpts{
		NoStream: true,
		Have:     make(map[string]bool),
	})})
	c, err := cliDial(t, s, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := c.SwitchToModeStream(); !errors.Is(err, ErrStreamingUnsupported) || !strings.Contains(err.Error(), "501") {
		t.Fatalf("SwitchToModeStream error = %v, want ErrStreamingUnsupported with the peer's 501", err)
	}
	if c.ModeStream || c.ModeReader {
		t.Fatalf("after a refused MODE STREAM: ModeStream=%v ModeReader=%v, want neither", c.ModeStream, c.ModeReader)
	}
}
