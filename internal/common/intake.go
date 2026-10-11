package common

// Pure helpers for article intake (POST/IHAVE/TAKETHIS and the bulk fetch paths).
// Stdlib only: this file must not import database, nntp or processor.

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrIntakeRejected marks a permanent rejection of an article (validation or policy).
// Producers wrap it as fmt.Errorf("%w: <reason>", ErrIntakeRejected); the reason after
// the first ": " is one of a fixed set of short strings (never client input), for example
// "invalid header block", "missing mandatory header", "invalid message-id", "invalid newsgroups".
var ErrIntakeRejected = errors.New("article rejected")

// ErrIntakeTemporary marks a temporary intake failure (history or database error); the
// sender may retry the article later. Producers wrap it like ErrIntakeRejected.
var ErrIntakeTemporary = errors.New("temporary intake failure")

// maximum lengths used by the intake helpers
const (
	intakeMaxMessageIDLen = 250 // RFC 5536 §3.1.3
	intakeMaxGroupNameLen = 255 // RFC 5536 §3.1.4 (D8)
	intakeMaxHeaderLine   = 998 // RFC 5322 §2.1.1, without CRLF
)

// ValidMessageID reports whether s is a syntactically valid message-id
// (RFC 5536 §3.1.3, RFC 3977 §3.6): at most 250 octets, enclosed in "<" and ">",
// every inner octet in %x21-7E except "<" and ">", exactly one "@" outside a
// "[...]" literal, a non-empty left part and a right part that is either
// dot-atom-text or a no-fold-literal "[...]".
func ValidMessageID(s string) bool {
	if len(s) > intakeMaxMessageIDLen || len(s) < 5 || s[0] != '<' || s[len(s)-1] != '>' {
		return false
	}
	inner := s[1 : len(s)-1]
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c < 0x21 || c > 0x7E || c == '<' || c == '>' {
			return false
		}
	}
	at := strings.IndexByte(inner, '@')
	if at <= 0 || at == len(inner)-1 {
		return false
	}
	right := inner[at+1:]
	if right[0] == '[' {
		// no-fold-literal = "[" *mdtext "]"; mdtext excludes "[", "\" and "]"
		if len(right) < 2 || right[len(right)-1] != ']' {
			return false
		}
		for i := 1; i < len(right)-1; i++ {
			switch right[i] {
			case '[', ']', '\\':
				return false
			}
		}
		return true
	}
	return isDotAtomText(right)
}

// isDotAtomText reports whether s is RFC 5322 dot-atom-text: 1*atext *("." 1*atext).
func isDotAtomText(s string) bool {
	if s == "" || s[0] == '.' || s[len(s)-1] == '.' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if s[i-1] == '.' {
				return false
			}
			continue
		}
		if !isAtext(c) {
			return false
		}
	}
	return true
}

// isAtext reports whether c is an RFC 5322 atext octet.
func isAtext(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-/=?^_`{|}~", c) >= 0
}

// ValidGroupNameSyntax reports whether name follows the RFC 5536 §3.1.4 grammar
// (decision D8): component *("." component), component = 1*(ALPHA / DIGIT / "+" / "-" / "_"),
// at most 255 octets. Upper case is syntactically valid.
func ValidGroupNameSyntax(name string) bool {
	if name == "" || len(name) > intakeMaxGroupNameLen {
		return false
	}
	compLen := 0
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '.':
			if compLen == 0 {
				return false
			}
			compLen = 0
			continue
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '+', c == '-', c == '_':
			compLen++
		default:
			return false
		}
	}
	return compLen > 0
}

// IsReservedGroupName reports whether name is reserved (decision D8, compared lower-case):
// first component "example", "to" or "control"; whole name "junk" or "poster";
// any component "all" or "ctl".
func IsReservedGroupName(name string) bool {
	lname := strings.ToLower(name)
	switch lname {
	case "junk", "poster":
		return true
	}
	comps := strings.Split(lname, ".")
	switch comps[0] {
	case "example", "to", "control":
		return true
	}
	for _, comp := range comps {
		if comp == "all" || comp == "ctl" {
			return true
		}
	}
	return false
}

// ParseNewsgroups splits a Newsgroups header value on ",", trims SP/TAB around each
// element, skips empty elements and removes duplicates (keeping the first occurrence).
// It returns an error wrapping ErrIntakeRejected ("invalid newsgroups") on the first name
// that fails ValidGroupNameSyntax and when no name is left.
func ParseNewsgroups(value string) ([]string, error) {
	var names []string
	seen := make(map[string]struct{})
	for _, elem := range strings.Split(value, ",") {
		name := strings.Trim(elem, " \t")
		if name == "" {
			continue
		}
		if !ValidGroupNameSyntax(name) {
			return nil, fmt.Errorf("%w: invalid newsgroups", ErrIntakeRejected)
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%w: invalid newsgroups", ErrIntakeRejected)
	}
	return names, nil
}

// HeaderBlockMode selects the rules of CheckHeaderBlock.
type HeaderBlockMode int

const (
	// HeaderBlockStrict is used for NNTP intake (POST/IHAVE/TAKETHIS): every defect rejects.
	// It is the zero value; any mode other than HeaderBlockFetch is treated as strict.
	HeaderBlockStrict HeaderBlockMode = iota
	// HeaderBlockFetch is used for fetched/imported articles: NUL/CR/LF and duplicate core
	// headers reject, malformed lines are dropped (normalized).
	HeaderBlockFetch
)

// headers that must not appear twice in strict mode (lower-case names)
var intakeStrictUniqueHeaders = map[string]struct{}{
	"date": {}, "from": {}, "message-id": {}, "newsgroups": {}, "path": {}, "subject": {},
	"references": {}, "injection-date": {}, "injection-info": {}, "xref": {}, "control": {},
	"supersedes": {}, "approved": {},
}

// headers that must not appear twice in fetch mode (lower-case names)
var intakeFetchUniqueHeaders = map[string]struct{}{
	"date": {}, "from": {}, "message-id": {}, "newsgroups": {}, "path": {}, "subject": {},
}

// CheckHeaderBlock validates an article header block. The lines are un-stuffed, without
// CRLF and without the empty separator line. Rules:
//  1. both modes: a line containing NUL, CR or LF rejects. Strict mode also rejects every
//     other C0 control except TAB; fetch mode lets them pass (ESC in ISO-2022-JP headers).
//  2. a line starting with SP/TAB without a preceding field line (first line, or after a
//     dropped line): strict rejects, fetch drops it.
//  3. a non-continuation line without ":", with an empty field name, or with a name octet
//     outside %x21-39 / %x3B-7E (this covers a lone "."): strict rejects, fetch drops it
//     and its continuation lines.
//  4. a second copy (case-insensitive name) of Date, From, Message-ID, Newsgroups, Path,
//     Subject rejects in both modes; strict mode also rejects a second References,
//     Injection-Date, Injection-Info, Xref, Control, Supersedes or Approved.
//  5. strict only: a line longer than 998 octets rejects.
//
// Errors wrap ErrIntakeRejected with the reason "invalid header block". The input slice is
// returned unchanged when nothing was dropped, otherwise a new slice; the input is never modified.
func CheckHeaderBlock(lines []string, mode HeaderBlockMode) ([]string, error) {
	strict := mode != HeaderBlockFetch
	unique := intakeStrictUniqueHeaders
	if !strict {
		unique = intakeFetchUniqueHeaders
	}
	reject := func() ([]string, error) {
		return nil, fmt.Errorf("%w: invalid header block", ErrIntakeRejected)
	}
	var out []string // nil until the first dropped line
	dropped := false
	drop := func(i int) {
		if !dropped {
			dropped = true
			out = make([]string, 0, len(lines)-1)
			out = append(out, lines[:i]...)
		}
	}
	keep := func(line string) {
		if dropped {
			out = append(out, line)
		}
	}
	seen := make(map[string]struct{})
	havePrevField := false
	for i, line := range lines {
		// rule 1
		for j := 0; j < len(line); j++ {
			c := line[j]
			if c == 0 || c == '\r' || c == '\n' {
				return reject()
			}
			if strict && c < 0x20 && c != '\t' {
				return reject()
			}
		}
		// rule 5
		if strict && len(line) > intakeMaxHeaderLine {
			return reject()
		}
		// rule 2
		if line != "" && (line[0] == ' ' || line[0] == '\t') {
			if !havePrevField {
				if strict {
					return reject()
				}
				drop(i)
				continue
			}
			keep(line)
			continue
		}
		// rule 3
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || !validFieldName(line[:colon]) {
			if strict {
				return reject()
			}
			drop(i)
			havePrevField = false
			continue
		}
		// rule 4
		lname := strings.ToLower(line[:colon])
		if _, ok := unique[lname]; ok {
			if _, dup := seen[lname]; dup {
				return reject()
			}
			seen[lname] = struct{}{}
		}
		havePrevField = true
		keep(line)
	}
	if !dropped {
		return lines, nil
	}
	return out, nil
}

// validFieldName reports whether every octet of name is RFC 5322 ftext (%x21-39 / %x3B-7E).
func validFieldName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x21 || c > 0x7E || c == ':' {
			return false
		}
	}
	return name != ""
}

// isContinuationLine reports whether line continues the previous header field.
func isContinuationLine(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

// headerFieldMatches reports whether line is a field line (not a continuation) named name
// (case-insensitive, surrounding whitespace of the name ignored) and returns the colon index.
func headerFieldMatches(line, name string) (int, bool) {
	if line == "" || isContinuationLine(line) {
		return -1, false
	}
	colon := strings.IndexByte(line, ':')
	if colon == -1 || !strings.EqualFold(strings.TrimSpace(line[:colon]), name) {
		return -1, false
	}
	return colon, true
}

// unfoldHeaderAt returns the value of the field line at index i (colon at colon), joined
// with its continuation lines by single spaces and trimmed.
func unfoldHeaderAt(lines []string, i, colon int) string {
	value := strings.TrimSpace(lines[i][colon+1:])
	for _, cont := range lines[i+1:] {
		if !isContinuationLine(cont) {
			break
		}
		if part := strings.TrimSpace(cont); part != "" {
			if value != "" {
				value += " "
			}
			value += part
		}
	}
	return value
}

// HeaderValue returns the value of the first field named name (case-insensitive),
// unfolded with single spaces and trimmed; "" when absent.
func HeaderValue(lines []string, name string) string {
	for i, line := range lines {
		if colon, ok := headerFieldMatches(line, name); ok {
			return unfoldHeaderAt(lines, i, colon)
		}
	}
	return ""
}

// HeaderValues returns the unfolded values of every field named name, in order.
func HeaderValues(lines []string, name string) []string {
	var values []string
	for i, line := range lines {
		if colon, ok := headerFieldMatches(line, name); ok {
			values = append(values, unfoldHeaderAt(lines, i, colon))
		}
	}
	return values
}

// ReplaceHeader replaces the first field named name, including its continuation lines,
// with one "Name: value" line, keeping the spelling of the existing field name. When the
// field is absent, "name: value" is inserted at index 0. It returns a new slice; the
// input is not modified.
func ReplaceHeader(lines []string, name, value string) []string {
	for i, line := range lines {
		colon, ok := headerFieldMatches(line, name)
		if !ok {
			continue
		}
		end := i + 1
		for end < len(lines) && isContinuationLine(lines[end]) {
			end++
		}
		out := make([]string, 0, len(lines)-(end-i)+1)
		out = append(out, lines[:i]...)
		out = append(out, strings.TrimSpace(line[:colon])+": "+value)
		out = append(out, lines[end:]...)
		return out
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, name+": "+value)
	out = append(out, lines...)
	return out
}

// RemoveHeaders drops every field named name together with its continuation lines.
// It returns the input unchanged when no such field exists, otherwise a new slice.
func RemoveHeaders(lines []string, name string) []string {
	var out []string
	removed := false
	skipping := false
	for i, line := range lines {
		if skipping && isContinuationLine(line) {
			continue
		}
		skipping = false
		if _, ok := headerFieldMatches(line, name); ok {
			if !removed {
				removed = true
				out = make([]string, 0, len(lines)-1)
				out = append(out, lines[:i]...)
			}
			skipping = true
			continue
		}
		if removed {
			out = append(out, line)
		}
	}
	if !removed {
		return lines
	}
	return out
}

// WireArticleSize returns the size of the article on the wire without dot-stuffing
// (RFC 3977 §8.1.1 ":bytes"): every head and body line plus CRLF, plus the CRLF of the
// separator line.
func WireArticleSize(head, body []string) int {
	size := 2
	for _, l := range head {
		size += len(l) + 2
	}
	for _, l := range body {
		size += len(l) + 2
	}
	return size
}

// intakeMsgIDEncoding is RFC 4648 base32 with the lower-case alphabet and no padding.
var intakeMsgIDEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// GenerateMessageID returns a new message-id "<" + base32(16 random octets) + "@" + identity + ">"
// (decision D4). It fails when identity is empty, the random source fails or the result is
// not a valid message-id (for example an identity containing spaces).
func GenerateMessageID(identity string) (string, error) {
	if identity == "" {
		return "", errors.New("generate message-id: empty identity")
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate message-id: %w", err)
	}
	id := "<" + intakeMsgIDEncoding.EncodeToString(buf[:]) + "@" + identity + ">"
	if !ValidMessageID(id) {
		return "", fmt.Errorf("generate message-id: invalid identity %q", identity)
	}
	return id, nil
}

// FormatNewsDate formats t as an RFC 5322 date in UTC ("Mon, 02 Jan 2006 15:04:05 +0000").
func FormatNewsDate(t time.Time) string {
	return t.UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")
}
