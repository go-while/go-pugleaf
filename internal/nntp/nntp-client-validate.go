package nntp

// Argument validation for outbound NNTP commands (A-nntp-client-3).
//
// Message-ids and group names reach the client from remote XOVER/XHDR data
// and the local database. A bare CR survives textproto's ReadLine and a space
// splits a command into extra arguments, so every command argument is checked
// before it is written: invalid arguments are rejected, never sanitized.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-while/go-pugleaf/internal/common"
)

// ErrInvalidCommandArg is returned when a command argument or the formatted
// command line is not allowed on the wire. Nothing has been written to the
// connection when it is returned, so the connection is still usable.
var ErrInvalidCommandArg = errors.New("nntp: invalid command argument")

// maxClientCommandLine is the longest command line the client sends, without
// CRLF (RFC 3977 §3.1: 512 octets including CRLF).
const maxClientCommandLine = 510

// validMessageIDArg reports whether id may be sent as a message-id argument
// (common.ValidMessageID: RFC 5536 msg-id, at most 250 octets).
func validMessageIDArg(id string) error {
	if !common.ValidMessageID(id) {
		return fmt.Errorf("%w: message-id %q", ErrInvalidCommandArg, id)
	}
	return nil
}

// validGroupArg reports whether name may be sent as a newsgroup argument:
// 1..497 octets, no control character, space, DEL or ','.
func validGroupArg(name string) error {
	if name == "" || len(name) > 497 {
		return fmt.Errorf("%w: newsgroup name length %d", ErrInvalidCommandArg, len(name))
	}
	for i := 0; i < len(name); i++ {
		if b := name[i]; b <= 0x20 || b == 0x7f || b == ',' {
			return fmt.Errorf("%w: newsgroup name %q", ErrInvalidCommandArg, name)
		}
	}
	return nil
}

// validTokenArg reports whether s may be sent as a single command argument:
// non-empty, no CR, LF or NUL, and no SP/HT unless allowSpace. The value is
// not part of the error (it may be a password).
func validTokenArg(s string, allowSpace bool) error {
	if s == "" {
		return fmt.Errorf("%w: empty argument", ErrInvalidCommandArg)
	}
	bad := "\r\n\x00"
	if !allowSpace {
		bad += " \t"
	}
	if strings.ContainsAny(s, bad) {
		return fmt.Errorf("%w: argument contains a forbidden character", ErrInvalidCommandArg)
	}
	return nil
}

// sendCmd formats one command line and sends it with c.TextConn.Cmd. A line
// that contains CR, LF or NUL or is longer than maxClientCommandLine octets is
// refused with ErrInvalidCommandArg and nothing is written (no textproto id is
// consumed). The error never contains the line (AUTHINFO carries secrets).
func (c *BackendConn) sendCmd(format string, args ...any) (uint, error) {
	line := fmt.Sprintf(format, args...)
	verb, _, _ := strings.Cut(format, " ")
	if strings.ContainsAny(line, "\r\n\x00") {
		return 0, fmt.Errorf("%w: %s command line contains CR, LF or NUL", ErrInvalidCommandArg, verb)
	}
	if len(line) > maxClientCommandLine {
		return 0, fmt.Errorf("%w: %s command line is %d octets (max %d)", ErrInvalidCommandArg, verb, len(line), maxClientCommandLine)
	}
	return c.TextConn.Cmd("%s", line)
}
