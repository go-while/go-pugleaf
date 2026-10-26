package nntp

import (
	"strings"
	"time"
)

// dataBlockFlushLines is the number of data-block lines buffered before a flush.
const dataBlockFlushLines = 256

// dataBlockWriter writes one multi-line data block (RFC 3977 §3.1.1) through the
// connection's single buffered writer (c.textConn.W).
type dataBlockWriter struct {
	c *ClientConnection
	n int // lines since the last flush
}

// newDataBlock starts a data block; the status line must already have been sent.
func (c *ClientConnection) newDataBlock() *dataBlockWriter { return &dataBlockWriter{c: c} }

// cleanDataLine strips one trailing CR and replaces every other CR, LF and NUL by a space,
// so that a stored line can never end the block or split into two lines.
func cleanDataLine(s string) string {
	s = strings.TrimSuffix(s, "\r")
	if !strings.ContainsAny(s, "\r\n\x00") {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', 0:
			return ' '
		}
		return r
	}, s)
}

// Line writes cleanDataLine(s), dot-stuffed (a leading "." becomes ".."), plus CRLF.
// It flushes (after setWriteDeadline) every dataBlockFlushLines lines.
func (d *dataBlockWriter) Line(s string) error {
	s = cleanDataLine(s)
	w := d.c.textConn.W
	if strings.HasPrefix(s, ".") {
		if err := w.WriteByte('.'); err != nil {
			return err
		}
	}
	if _, err := w.WriteString(s); err != nil {
		return err
	}
	if _, err := w.WriteString("\r\n"); err != nil {
		return err
	}
	d.n++
	if d.n >= dataBlockFlushLines {
		d.n = 0
		d.c.setWriteDeadline()
		return w.Flush()
	}
	return nil
}

// Close writes the terminating ".\r\n" and flushes.
func (d *dataBlockWriter) Close() error {
	w := d.c.textConn.W
	if _, err := w.WriteString(".\r\n"); err != nil {
		return err
	}
	d.c.setWriteDeadline()
	return w.Flush()
}

// sendDataBlock sends "code msg" and lines as one block (CAPABILITIES, HELP, short lists).
func (c *ClientConnection) sendDataBlock(code int, msg string, lines []string) error {
	if err := c.sendResponse(code, msg); err != nil {
		return err
	}
	d := c.newDataBlock()
	for _, line := range lines {
		if err := d.Line(line); err != nil {
			return err
		}
	}
	return d.Close()
}

// ioTimeout is the server's IdleTimeout, or DefaultNNTPcliconnTimeout when it is <= 0
// or the connection has no server.
func (c *ClientConnection) ioTimeout() time.Duration {
	if c.server != nil && c.server.IdleTimeout > 0 {
		return c.server.IdleTimeout
	}
	return DefaultNNTPcliconnTimeout
}

// setWriteDeadline sets the connection's write deadline to now+ioTimeout(); no-op when
// there is no connection.
func (c *ClientConnection) setWriteDeadline() {
	if c.conn == nil {
		return
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.ioTimeout()))
}
