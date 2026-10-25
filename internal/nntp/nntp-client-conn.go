package nntp

// Socket wrapper for outbound NNTP connections (plan nntp-client-transfer, CLI-3).

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// ErrResponseLineTooLong is returned by a read when the server sent a line
// longer than BackendConfig.MaxResponseLineBytes (A-nntp-client-1). The
// connection is unusable afterwards and must be force-closed.
var ErrResponseLineTooLong = errors.New("nntp: response line too long")

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
//
// Read also enforces the response line cap (setMaxLine): a line (the bytes
// before its '\n') longer than the cap fails the read with
// ErrResponseLineTooLong, and every later Read fails with the same error.
type deadlineConn struct {
	net.Conn
	readTimeout  atomic.Int64 // nanoseconds; <= 0: no read deadline
	writeTimeout atomic.Int64 // nanoseconds; <= 0: no write deadline
	maxLine      atomic.Int64 // response line cap in bytes; <= 0: no cap

	// ctr counts the octets of both directions (nil: no accounting). What
	// passes through deadlineConn is the raw stream; the wire octets are
	// counted here as well unless the layer underneath does it (belowCounts,
	// set by swapConn for the COMPRESS DEFLATE layer).
	ctr         *byteCounters
	belowCounts atomic.Bool

	// run and lineErr are used by Read only (one reader at a time: textproto).
	run     int64 // bytes since the last '\n' seen in earlier reads
	lineErr error // sticky ErrResponseLineTooLong once the cap was exceeded
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

// setMaxLine sets the response line cap (bytes before the '\n'); n <= 0
// disables it. Call it before the first Read (Connect holds c.mux).
func (dc *deadlineConn) setMaxLine(n int) {
	dc.maxLine.Store(int64(n))
}

// setCounters attaches the octet counters of the connection; nil disables the
// accounting. Call it before the first Read or Write.
func (dc *deadlineConn) setCounters(ctr *byteCounters) {
	dc.ctr = ctr
}

// swapConn puts another conn underneath: the COMPRESS DEFLATE layer over the
// socket (nntp-client-compress.go). The deadlines and the line cap keep
// working on what that layer hands up, which is the inflated stream, and the
// line-cap state is reset because the octets counted so far belong to the
// plain stream before the 206. From now on the layer underneath counts the
// wire octets. Call it while no Read or Write is in progress (Connect holds
// c.mux and nothing is in flight).
func (dc *deadlineConn) swapConn(c net.Conn) {
	dc.Conn = c
	dc.run, dc.lineErr = 0, nil
	dc.belowCounts.Store(true)
}

// count adds n octets of one direction to the counters: always to the raw
// stream, and to the wire stream unless the layer underneath counts it.
func (dc *deadlineConn) count(raw, wire *atomic.Int64, n int) {
	if dc.ctr == nil || n <= 0 {
		return
	}
	raw.Add(int64(n))
	if !dc.belowCounts.Load() {
		wire.Add(int64(n))
	}
}

// Read arms the read deadline, reads from the socket and checks the line cap.
func (dc *deadlineConn) Read(p []byte) (int, error) {
	if dc.lineErr != nil {
		return 0, dc.lineErr
	}
	if t := time.Duration(dc.readTimeout.Load()); t > 0 {
		if err := dc.Conn.SetReadDeadline(time.Now().Add(t)); err != nil {
			return 0, err
		}
	}
	n, err := dc.Conn.Read(p)
	if dc.ctr != nil {
		dc.count(&dc.ctr.rxRaw, &dc.ctr.rxWire, n)
	}
	if limit := dc.maxLine.Load(); limit > 0 && n > 0 {
		// Walk every '\n' in p[:n]: the first segment continues the line of
		// earlier reads (run), each following one is measured between two
		// newlines, and the tail after the last newline becomes the new run.
		run := dc.run
		start := 0 // offset in p of the segment being measured
		for {
			i := bytes.IndexByte(p[start:n], '\n')
			if i < 0 {
				run += int64(n - start)
				break
			}
			if run+int64(i) > limit {
				run += int64(i)
				break
			}
			run = 0
			start += i + 1
		}
		dc.run = run
		if run > limit {
			// Hand on only the complete lines before the long one, so the
			// reader can never return it; it fails on that line, and every
			// later Read fails with the same error.
			dc.lineErr = fmt.Errorf("%w: over %d bytes", ErrResponseLineTooLong, limit)
			return start, dc.lineErr
		}
	}
	return n, err
}

// Write arms the write deadline, then writes to the socket.
func (dc *deadlineConn) Write(p []byte) (int, error) {
	if t := time.Duration(dc.writeTimeout.Load()); t > 0 {
		if err := dc.Conn.SetWriteDeadline(time.Now().Add(t)); err != nil {
			return 0, err
		}
	}
	n, err := dc.Conn.Write(p)
	if dc.ctr != nil {
		dc.count(&dc.ctr.txRaw, &dc.ctr.txWire, n)
	}
	return n, err
}
