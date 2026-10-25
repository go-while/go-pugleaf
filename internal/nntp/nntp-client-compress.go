package nntp

// COMPRESS DEFLATE (RFC 8054) for outbound provider connections
// (plan fetcher-compress-deflate, slice compress-conn, design points 2, 3 and 5).
//
// Layering: the deflate layer sits *under* deadlineConn
//
//	textproto.Conn -> deadlineConn (idle deadlines, response line cap, byte
//	                  counters of the raw stream)
//	               -> deflateConn  (deflates every write, inflates what the
//	                  server sends, counts the wire octets)
//	               -> the socket (plain TCP or TLS)
//
// so every hostile-peer bound of the client (MaxResponseLineBytes, the
// MaxReadLines* limits, the octet budgets) and every read deadline measures
// the *inflated* stream: a small compressed bomb is still cut by the inflated
// caps.

import (
	"bufio"
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/textproto"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/nntp/inflate"
)

const (
	// NNTPCompressActive is the server's 206: compression is on from the CRLF
	// of that line on (RFC 8054 §2.2).
	NNTPCompressActive int = 206

	// compressPendingMax bounds the inflated octets held for the reader of a
	// compressed connection: the inflater waits while that much is unread, so
	// a server that never stops writing cannot fill memory.
	compressPendingMax = 1 << 20

	// compressReadBuf is the buffer of the inflater's reader on the socket.
	compressReadBuf = 32 << 10
)

// byteCounters count the octets of one connection in both directions: the raw
// stream as the NNTP layer sees it (commands and inflated replies) and the
// wire stream as it crosses the socket. Without compression both are equal.
type byteCounters struct {
	txWire atomic.Int64
	txRaw  atomic.Int64
	rxWire atomic.Int64
	rxRaw  atomic.Int64
}

// compressRefused remembers the providers whose COMPRESS DEFLATE was refused,
// so the refusal is logged once per provider and not once per connection.
var (
	compressRefusedMu   sync.Mutex
	compressRefusedSeen = make(map[string]bool)
)

// logCompressRefused logs the refusal of provider label once (design point 2).
func logCompressRefused(label string, code int, message string) {
	compressRefusedMu.Lock()
	seen := compressRefusedSeen[label]
	compressRefusedSeen[label] = true
	compressRefusedMu.Unlock()
	if seen {
		return
	}
	log.Printf("[NNTP-CONN] provider '%s': COMPRESS DEFLATE refused (%d %s), continuing uncompressed", label, code, message)
}

// compressed reports whether this connection runs COMPRESS DEFLATE.
func (c *BackendConn) compressed() bool {
	if c.dconn == nil {
		return false
	}
	_, ok := c.dconn.Conn.(*deflateConn)
	return ok
}

// startCompress sends COMPRESS DEFLATE and switches the connection over when
// the server answers 206 (RFC 8054 §2.2). A refusal (403, 500, 501, 502, 503,
// and any other final reply) is not a connection failure: it is logged once
// per provider and the connection stays plain, so startCompress returns nil.
// Only an I/O or protocol error is returned, and then the caller aborts the
// connect. The client never sends COMPRESS twice: Connect calls this once and
// it refuses to run on an already compressed connection.
//
// The caller holds c.mux (Connect does) and must not have a response in
// flight: the compression layer starts right after the CRLF of the 206.
func (c *BackendConn) startCompress() error {
	if c.dconn == nil || c.TextConn == nil {
		return errors.New("nntp: COMPRESS DEFLATE without a connection")
	}
	if c.compressed() {
		return errors.New("nntp: COMPRESS DEFLATE is already active")
	}
	id, err := c.sendCmd("COMPRESS DEFLATE")
	if err != nil {
		return err
	}
	c.TextConn.StartResponse(id)
	code, message, err := c.TextConn.ReadCodeLine(0)
	c.TextConn.EndResponse(id)
	if err != nil && code == 0 {
		return err
	}
	if code != NNTPCompressActive {
		logCompressRefused(c.Backend.providerLabel(), code, message)
		return nil
	}
	if err := c.enableCompression(); err != nil {
		return err
	}
	log.Printf("[NNTP-CONN] provider '%s': COMPRESS DEFLATE accepted", c.Backend.providerLabel())
	return nil
}

// enableCompression puts the deflate layer under the deadline wrapper and
// gives the connection a fresh textproto on top of it. The octets the
// textproto reader buffered after the 206 line are already part of the
// compressed stream (RFC 8054 §2.2.2), so they are the first input of the
// inflater. The caller holds c.mux and nothing is in flight.
func (c *BackendConn) enableCompression() error {
	var prefix []byte
	if n := c.TextConn.R.Buffered(); n > 0 {
		b, err := c.TextConn.R.Peek(n)
		if err != nil {
			return fmt.Errorf("nntp: COMPRESS DEFLATE: %w", err)
		}
		prefix = append([]byte(nil), b...)
		// Those octets were counted as raw *and* wire input while the
		// connection was still plain; they are compressed payload, so only
		// the wire count is right.
		c.counters.rxRaw.Add(-int64(len(prefix)))
	}
	d, err := newDeflateConn(c.dconn.Conn, prefix, &c.counters, c.ForceCloseConn)
	if err != nil {
		return fmt.Errorf("nntp: COMPRESS DEFLATE: %w", err)
	}
	c.dconn.swapConn(d)
	c.TextConn = textproto.NewConn(c.dconn)
	return nil
}

// deflateConn is the RFC 8054 compression layer of a client connection.
//
// Writes are deflated (raw DEFLATE, RFC 1951, no zlib header) and sync-flushed
// per Write, and the flushed octets go to the socket in one write, so the
// server can inflate a whole command at once.
//
// Reads come from an inflater goroutine running inflate.Run over the socket:
// it passes every decoded octet on as soon as the octets that carry it have
// arrived, whatever flush the server uses (a sync or full flush, or zlib's
// Z_PARTIAL_FLUSH, which INN's nnrpd sends and which compress/flate's reader
// would hold back). The decoded octets wait in a bounded buffer
// (compressPendingMax); the inflater blocks while it is full. The socket is
// read without a deadline: the read deadline of the connection applies to the
// inflated stream instead.
//
// Close is idempotent, closes the socket and ends the inflater goroutine.
// Corrupt input is a read error wrapping flate.CorruptInputError, and the
// connection is force-closed (RFC 8054 §2.2.2).
type deflateConn struct {
	raw       net.Conn
	ctr       *byteCounters
	onCorrupt func() // force-close hook, called once on corrupt input

	wmu  sync.Mutex // serialises Write: the flate.Writer is not concurrency safe
	zw   *flate.Writer
	wbuf bytes.Buffer // the deflated octets of one Write, written to raw in one go

	mu       sync.Mutex
	room     *sync.Cond // the inflater waits on it while buf is full
	buf      []byte     // inflated octets not read yet
	err      error      // the end of the inflated stream, returned once buf is empty
	closed   bool
	deadline time.Time     // read deadline of the inflated stream
	wake     chan struct{} // capacity 1: data, an error, Close or a new deadline
	done     chan struct{} // closed when the inflater goroutine has returned
}

// countingReader adds every octet it reads to n.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n.Add(int64(n))
	}
	return n, err
}

// newDeflateConn wraps raw and starts the inflater goroutine. prefix holds the
// octets of the compressed stream that were read from the socket already (the
// buffer of the reader that read the 206); ctr and onCorrupt may be nil.
func newDeflateConn(raw net.Conn, prefix []byte, ctr *byteCounters, onCorrupt func()) (*deflateConn, error) {
	// The socket itself is read without a deadline from now on; the read
	// deadline of the connection bounds the inflated stream (SetReadDeadline).
	if err := raw.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	d := &deflateConn{
		raw:       raw,
		ctr:       ctr,
		onCorrupt: onCorrupt,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	zw, err := flate.NewWriter(&d.wbuf, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	d.zw = zw
	d.room = sync.NewCond(&d.mu)

	var src io.Reader = raw
	if ctr != nil {
		src = &countingReader{r: raw, n: &ctr.rxWire}
	}
	if len(prefix) > 0 {
		src = io.MultiReader(bytes.NewReader(prefix), src)
	}
	go d.runInflate(bufio.NewReaderSize(src, compressReadBuf))
	return d, nil
}

// notify wakes a Read that waits.
func (d *deflateConn) notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// runInflate decodes the server's stream into buf until it ends. Corrupt input
// closes the connection (RFC 8054 §2.2.2) after the error was stored, so the
// reader still sees the corrupt-input error and not net.ErrClosed.
func (d *deflateConn) runInflate(src *bufio.Reader) {
	defer close(d.done)
	err := inflate.Run(src, d.put)
	var ce flate.CorruptInputError
	corrupt := errors.As(err, &ce)
	if corrupt {
		err = fmt.Errorf("nntp: compressed stream from the server: %w", err)
	}
	d.mu.Lock()
	if d.err == nil {
		d.err = err
	}
	d.mu.Unlock()
	d.notify()
	if corrupt {
		_ = d.Close()
		if d.onCorrupt != nil {
			d.onCorrupt()
		}
	}
}

// put appends inflated octets to buf; it waits while buf is full and fails
// once the connection is closed, which ends the inflater goroutine.
func (d *deflateConn) put(b []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for len(d.buf) >= compressPendingMax && !d.closed {
		d.room.Wait()
	}
	if d.closed {
		return net.ErrClosed
	}
	d.buf = append(d.buf, b...)
	d.notify()
	return nil
}

// Read returns inflated octets. It waits until some arrive, the stream ends or
// the read deadline passes (a net.Error whose Timeout is true).
func (d *deflateConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		d.mu.Lock()
		switch {
		case len(d.buf) > 0:
			n := copy(p, d.buf)
			d.buf = d.buf[n:]
			d.room.Signal()
			d.mu.Unlock()
			return n, nil
		case d.err != nil:
			err := d.err
			d.mu.Unlock()
			return 0, err
		case d.closed:
			d.mu.Unlock()
			return 0, net.ErrClosed
		}
		dl := d.deadline
		d.mu.Unlock()
		if dl.IsZero() {
			<-d.wake
			continue
		}
		wait := time.Until(dl)
		if wait <= 0 {
			return 0, d.timeoutErr()
		}
		tm := time.NewTimer(wait)
		select {
		case <-d.wake:
		case <-tm.C:
		}
		tm.Stop()
	}
}

// timeoutErr is the read timeout of the inflated stream: a net.Error whose
// Timeout reports true, like the socket's own deadline error.
func (d *deflateConn) timeoutErr() error {
	return &net.OpError{
		Op:     "read",
		Net:    d.raw.LocalAddr().Network(),
		Source: d.raw.LocalAddr(),
		Addr:   d.raw.RemoteAddr(),
		Err:    os.ErrDeadlineExceeded,
	}
}

// Write deflates p, flushes the compressor (sync flush) and sends the result
// in one socket write, so the server can inflate all of p at once.
func (d *deflateConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	d.wbuf.Reset()
	if _, err := d.zw.Write(p); err != nil {
		return 0, err
	}
	if err := d.zw.Flush(); err != nil {
		return 0, err
	}
	if d.wbuf.Len() == 0 {
		return len(p), nil
	}
	n, err := d.raw.Write(d.wbuf.Bytes())
	if d.ctr != nil && n > 0 {
		d.ctr.txWire.Add(int64(n))
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close closes the socket, which ends the inflater goroutine. It is
// idempotent: later calls do nothing and return nil.
func (d *deflateConn) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.room.Broadcast()
	d.mu.Unlock()
	d.notify()
	return d.raw.Close()
}

func (d *deflateConn) LocalAddr() net.Addr  { return d.raw.LocalAddr() }
func (d *deflateConn) RemoteAddr() net.Addr { return d.raw.RemoteAddr() }

// SetDeadline sets the read deadline of the inflated stream and the socket's
// write deadline.
func (d *deflateConn) SetDeadline(t time.Time) error {
	_ = d.SetReadDeadline(t)
	return d.raw.SetWriteDeadline(t)
}

// SetReadDeadline bounds Read of the inflated stream; the socket itself is
// read without a deadline.
func (d *deflateConn) SetReadDeadline(t time.Time) error {
	d.mu.Lock()
	d.deadline = t
	d.mu.Unlock()
	d.notify()
	return nil
}

// SetWriteDeadline passes the deadline to the socket.
func (d *deflateConn) SetWriteDeadline(t time.Time) error { return d.raw.SetWriteDeadline(t) }
