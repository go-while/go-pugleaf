package common

// Outbound header serializer for relaying stored articles (TAKETHIS) without
// rewriting them: RFC 5537 §3.6, relaying agents MUST NOT alter, delete or
// rearrange any part of an article except Path and Xref (plan
// nntp-client-transfer, A-intake-6 sender half and CLI-1, decisions 1 and 2).

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-while/go-pugleaf/internal/models"
)

// ErrRelayHeadersInvalid marks a stored header block that must not be sent at
// all (unsafe: CR/LF/NUL inside a line, a duplicated mandatory field, a
// Message-ID that differs from the article's or is invalid). The caller skips
// the article; it is never repaired.
var ErrRelayHeadersInvalid = errors.New("stored header block not relayable")

// ErrRelayHeadersIncomplete marks a stored header block that lacks a mandatory
// field or has a malformed field line. The caller falls back to
// ReconstructHeaders, which fills the fields from the article's columns.
var ErrRelayHeadersIncomplete = errors.New("stored header block incomplete")

// EnsureLocalPathPrefix returns path with the local identity localHost as its
// first element: unchanged when it already starts with it (the intake prefixes
// stored paths), else "<localHost>!" is prepended once. An empty path becomes
// "<localHost>!not-for-mail"; an empty localHost leaves path unchanged.
func EnsureLocalPathPrefix(path, localHost string) string {
	if localHost == "" {
		return path
	}
	if path == "" {
		return localHost + "!not-for-mail"
	}
	if path == localHost || strings.HasPrefix(path, localHost+"!") {
		return path
	}
	return localHost + "!" + path
}

// RelayHeaders returns the stored header block of article (HeadersJSON) for
// relaying: the lines in stored order and unchanged, except that the Path
// field is replaced in place by one "Path: " + EnsureLocalPathPrefix line
// (inserted first when absent), Xref fields are dropped, X-Goo* fields are
// dropped when IgnoreGoogleHeaders is set, and a Date value that is not an RFC
// date is replaced by outboundDate. Errors wrap ErrRelayHeadersInvalid (skip
// the article) or ErrRelayHeadersIncomplete (fall back to ReconstructHeaders);
// unsafe checks run first.
func RelayHeaders(article *models.Article, localHost string) ([]string, error) {
	return relayHeaders(article, localHost, IgnoreGoogleHeaders)
}

// relayHeaders is RelayHeaders with the X-Goo* switch as an argument (tests
// use it, so the package flag is never mutated).
func relayHeaders(article *models.Article, localHost string, ignoreGoogle bool) ([]string, error) {
	// relayField is one header field of the stored block: the field line and
	// its continuation lines, and its lower-case name ("" when malformed).
	type relayField struct {
		lines []string
		name  string // lower-case field name; "" for a malformed field line
		colon int    // index of ':' in lines[0]
	}
	// value returns the unfolded field value (CRLF removed), trimmed.
	value := func(f *relayField) string {
		if f.name == "" {
			return ""
		}
		var b strings.Builder
		b.WriteString(f.lines[0][f.colon+1:])
		for _, l := range f.lines[1:] {
			b.WriteString(l)
		}
		return strings.TrimSpace(b.String())
	}

	raw := strings.Split(article.HeadersJSON, "\n")
	for len(raw) > 0 && strings.TrimSuffix(raw[len(raw)-1], "\r") == "" {
		raw = raw[:len(raw)-1] // trailing empty lines are not part of the block
	}

	// Pass 1: unsafe bytes, grouping into fields; malformed lines are noted.
	var fields []*relayField
	incomplete := ""
	for i, l := range raw {
		l = strings.TrimSuffix(l, "\r")
		if strings.ContainsAny(l, "\r\n\x00") {
			return nil, fmt.Errorf("%w: CR, LF or NUL inside header line %d", ErrRelayHeadersInvalid, i+1)
		}
		if l != "" && (l[0] == ' ' || l[0] == '\t') {
			if len(fields) == 0 {
				if incomplete == "" {
					incomplete = "first line is a continuation"
				}
				fields = append(fields, &relayField{})
			}
			f := fields[len(fields)-1]
			f.lines = append(f.lines, l)
			continue
		}
		f := &relayField{lines: []string{l}}
		fields = append(fields, f)
		colon := strings.IndexByte(l, ':')
		if colon < 0 {
			if incomplete == "" {
				incomplete = fmt.Sprintf("header line %d has no colon", i+1)
			}
			continue
		}
		name := l[:colon]
		bad := name == ""
		for j := 0; j < len(name) && !bad; j++ {
			if name[j] <= 0x20 || name[j] == 0x7f {
				bad = true
			}
		}
		if bad {
			if incomplete == "" {
				incomplete = fmt.Sprintf("header line %d has an invalid field name", i+1)
			}
			continue
		}
		f.name = strings.ToLower(name)
		f.colon = colon
	}

	// Pass 2: unsafe field content (before any completeness check).
	count := make(map[string]int)
	var pathField, dateField *relayField
	for _, f := range fields {
		switch f.name {
		case "date", "from", "message-id", "newsgroups", "subject", "path":
			count[f.name]++
			if count[f.name] > 1 {
				return nil, fmt.Errorf("%w: %s present more than once", ErrRelayHeadersInvalid, f.name)
			}
		}
		switch f.name {
		case "message-id":
			id := value(f)
			if id != strings.TrimSpace(article.MessageID) {
				return nil, fmt.Errorf("%w: Message-ID %q differs from the article's %q", ErrRelayHeadersInvalid, id, article.MessageID)
			}
			if !ValidMessageID(id) {
				return nil, fmt.Errorf("%w: invalid Message-ID %q", ErrRelayHeadersInvalid, id)
			}
		case "path":
			pathField = f
		case "date":
			dateField = f
		}
	}

	// Pass 3: completeness (Path is inserted when absent).
	if incomplete != "" {
		return nil, fmt.Errorf("%w: %s", ErrRelayHeadersIncomplete, incomplete)
	}
	for _, name := range []string{"date", "from", "message-id", "newsgroups", "subject"} {
		if count[name] == 0 {
			return nil, fmt.Errorf("%w: missing %s", ErrRelayHeadersIncomplete, name)
		}
	}

	// Pass 4: build the outbound block.
	pathValue := article.Path
	if pathValue == "" && pathField != nil {
		pathValue = value(pathField)
	}
	pathLine := "Path: " + EnsureLocalPathPrefix(pathValue, localHost)
	var dateLine string
	if !isRFCdate(value(dateField)) {
		d, err := outboundDate(article)
		if err != nil {
			return nil, err
		}
		dateLine = "Date: " + d
	}

	out := make([]string, 0, len(raw)+1)
	if pathField == nil {
		out = append(out, pathLine)
	}
	for _, f := range fields {
		switch {
		case f == pathField:
			out = append(out, pathLine)
		case f.name == "xref":
			// dropped (RFC 5537 §3.6 allows it; Xref is local to the sender)
		case ignoreGoogle && strings.HasPrefix(f.name, "x-goo"):
			// dropped (-ignore-google-headers)
		case f == dateField && dateLine != "":
			out = append(out, dateLine)
		default:
			out = append(out, f.lines...)
		}
	}
	return out, nil
}
