package nntp

// Socket wrapper for outbound NNTP connections (plan nntp-client-transfer, CLI-3).

import (
	"net"
	"sync/atomic"
	"time"
)

// deadlineConn wraps the client socket (plain TCP or TLS) and arms an idle
// deadline before every Read and Write: each call must make progress within
// the configured timeout, otherwise it fails with a timeout error and the
// caller force-closes the connection. A timeout <= 0 means no deadline.
//
// Connect creates it with ConnectTimeout for both directions (bounding the
// greeting and AUTHINFO) and switches to ReadTimeout/WriteTimeout with
// setTimeouts once the connection is ready. BackendConn.conn and the
// textproto.Conn both sit on top of it, so the TAKETHIS/POST bufio writers
// get the write deadline as well.
type deadlineConn struct {
	net.Conn
	readTimeout  atomic.Int64 // nanoseconds; <= 0: no read deadline
	writeTimeout atomic.Int64 // nanoseconds; <= 0: no write deadline
}

// newDeadlineConn wraps c with the given read and write idle timeouts.
func newDeadlineConn(c net.Conn, rd, wr time.Duration) *deadlineConn {
	dc := &deadlineConn{Conn: c}
	dc.readTimeout.Store(int64(rd))
	dc.writeTimeout.Store(int64(wr))
	return dc
}

// setTimeouts replaces both idle timeouts. A value <= 0 also clears a deadline
// armed earlier (e.g. the ConnectTimeout one), so it cannot fire later.
// Call it while no Read or Write is in progress (Connect holds c.mux).
func (dc *deadlineConn) setTimeouts(rd, wr time.Duration) {
	dc.readTimeout.Store(int64(rd))
	dc.writeTimeout.Store(int64(wr))
	if rd <= 0 {
		_ = dc.Conn.SetReadDeadline(time.Time{})
	}
	if wr <= 0 {
		_ = dc.Conn.SetWriteDeadline(time.Time{})
	}
}

// Read arms the read deadline, then reads from the socket.
func (dc *deadlineConn) Read(p []byte) (int, error) {
	if t := time.Duration(dc.readTimeout.Load()); t > 0 {
		if err := dc.Conn.SetReadDeadline(time.Now().Add(t)); err != nil {
			return 0, err
		}
	}
	n, err := dc.Conn.Read(p)
	// The response line-length cap (wave 2, MaxResponseLineBytes) goes here:
	// scan p[:n] for '\n' and return ErrResponseLineTooLong for a segment over the cap.
	return n, err
}

// Write arms the write deadline, then writes to the socket.
func (dc *deadlineConn) Write(p []byte) (int, error) {
	if t := time.Duration(dc.writeTimeout.Load()); t > 0 {
		if err := dc.Conn.SetWriteDeadline(time.Now().Add(t)); err != nil {
			return 0, err
		}
	}
	return dc.Conn.Write(p)
}
