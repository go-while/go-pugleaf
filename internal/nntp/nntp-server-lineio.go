package nntp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultMaxCommandLineBytes is the longest accepted command line including
	// CRLF (RFC 3977 §3.1). A longer line that ends within commandLineHardLimit
	// gets 501 and the session continues.
	DefaultMaxCommandLineBytes = 512
	// commandLineHardLimit is the size of the bufio.Reader of textproto.NewConn:
	// a line without LF within it gets 400 and the connection is closed.
	commandLineHardLimit = 4096
	// DefaultMaxArticleBytes is the article size limit when Config.NNTP.MaxArtSize <= 0.
	DefaultMaxArticleBytes int64 = 1 << 20
)

var (
	errQuit            = errors.New("client quit")
	errCommandTooLong  = errors.New("command line too long")
	errCommandOverflow = errors.New("command line without line end within the read buffer")

	// ErrArticleTooLarge: the article exceeded the byte limit or the line caps; it
	// was read up to its terminator and discarded, the connection is in sync.
	ErrArticleTooLarge = errors.New("article too large")
	// ErrArticleDrainLimit: the article exceeded the drain bound before its
	// terminator; the connection is not in sync and must be closed.
	ErrArticleDrainLimit = errors.New("article too large to drain")
)

// maxArticleDrainBytes is how many bytes readArticleBlock reads (and discards)
// for an oversized article before giving up: max(4×limit, 1 MiB), at most 64 MiB.
func maxArticleDrainBytes(limit int64) int64 {
	const floor, ceiling = 1 << 20, 64 << 20
	if limit > ceiling/4 {
		return ceiling
	}
	d := 4 * limit
	if d < floor {
		d = floor
	}
	return d
}

// MaxArticleBytes is the article size limit of this server: Config.NNTP.MaxArtSize,
// or DefaultMaxArticleBytes when that is <= 0 or there is no config.
func (s *NNTPServer) MaxArticleBytes() int64 {
	if s != nil && s.Config != nil && s.Config.NNTP.MaxArtSize > 0 {
		return int64(s.Config.NNTP.MaxArtSize)
	}
	return DefaultMaxArticleBytes
}

// readCommandLine reads one command line without its CR/LF. It returns
// errCommandTooLong for a line over DefaultMaxCommandLineBytes (the line is
// consumed) and errCommandOverflow when no LF arrives within the read buffer.
func (c *ClientConnection) readCommandLine() (string, error) {
	raw, err := c.textConn.R.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return "", errCommandOverflow
		}
		return "", err
	}
	if len(raw) > commandLineHardLimit {
		return "", errCommandOverflow // defensive: the reader is never larger
	}
	if len(raw) > DefaultMaxCommandLineBytes {
		return "", errCommandTooLong
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	raw = bytes.TrimSuffix(raw, []byte("\r"))
	return string(raw), nil
}

// readArticleBlock reads a dot-terminated article (RFC 3977 §3.1.1), undoes the
// dot-stuffing and splits it at the first empty line into head and body lines.
// Every byte including CRLF counts against maxBytes (<= 0: DefaultMaxArticleBytes);
// 1024 head lines and 16384 lines are a secondary bound. An article over either
// bound is read up to its terminator and discarded (ErrArticleTooLarge, the
// connection stays usable); past maxArticleDrainBytes(maxBytes) it stops at once
// with ErrArticleDrainLimit. The read deadline is refreshed before every line, so
// a slow but steady upload is not cut off.
func (c *ClientConnection) readArticleBlock(maxBytes int64) (headLines, bodyLines []string, err error) {
	const maxHead, maxLines = 1024, 16384
	if maxBytes <= 0 {
		maxBytes = DefaultMaxArticleBytes
	}
	drainLimit := maxArticleDrainBytes(maxBytes)
	var total int64
	tooLarge := false
	inHeaders := true
	headCount, lineCount := 0, 0

	for {
		if c.conn != nil {
			_ = c.conn.SetReadDeadline(time.Now().Add(c.ioTimeout()))
		}
		var raw, last []byte
		long := false // the line spanned more than one buffer fill
		for {
			chunk, rerr := c.textConn.R.ReadSlice('\n')
			total += int64(len(chunk))
			if total > drainLimit {
				return nil, nil, ErrArticleDrainLimit
			}
			if rerr == nil {
				last = chunk
				break
			}
			if !errors.Is(rerr, bufio.ErrBufferFull) {
				return nil, nil, fmt.Errorf("failed to read article line: %w", rerr)
			}
			long = true
			if total > maxBytes {
				tooLarge = true
			}
			if tooLarge {
				raw = nil
			} else {
				raw = append(raw, chunk...)
			}
		}

		// The terminator itself does not count against maxBytes.
		if !long && (bytes.Equal(last, []byte(".\r\n")) || bytes.Equal(last, []byte(".\n"))) {
			break
		}
		if total > maxBytes {
			tooLarge = true
		}
		if tooLarge {
			headLines, bodyLines = nil, nil
			continue
		}

		raw = append(raw, last...)
		line := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		if inHeaders {
			if line == "" {
				inHeaders = false
			} else {
				headLines = append(headLines, line)
				headCount++
			}
		} else {
			bodyLines = append(bodyLines, line)
		}
		lineCount++
		if headCount > maxHead || lineCount > maxLines {
			tooLarge = true
			headLines, bodyLines = nil, nil
		}
	}
	if tooLarge {
		return nil, nil, ErrArticleTooLarge
	}
	return headLines, bodyLines, nil
}
