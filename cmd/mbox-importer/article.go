package main

import (
	"bytes"
	"io"
	"strings"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// skipReason says why an article of the input was not handed to the processor.
type skipReason int

const (
	skipNone skipReason = iota
	skipNoMessageID
	skipBadMessageID
	skipNoBody
	skipBinary
	skipNoDate
	skipNoSubject
	skipTooManyHeaders
	skipNoNewsgroups
	skipBadGroup
	skipTooBig
	skipLast = skipTooBig // the last reason, for loops over all of them
)

// maxHeaderLines is the most header lines (continuations included) an article may have.
const maxHeaderLines = 256

// defaultMaxArticleSize is the default of -max-article-size (mbox2nntp's cap: text
// articles are mostly under 16 KB, servers rarely take one over 256 KB).
const defaultMaxArticleSize = 1 << 20

// bodyChunkSize is the read size of readBody; longer lines are joined from several reads.
const bodyChunkSize = 64 * 1024

func (r skipReason) String() string {
	switch r {
	case skipNone:
		return "none"
	case skipNoMessageID:
		return "no-msgid"
	case skipBadMessageID:
		return "bad-msgid"
	case skipNoBody:
		return "no-body"
	case skipBinary:
		return "binary"
	case skipNoDate:
		return "no-date"
	case skipNoSubject:
		return "no-subject"
	case skipTooManyHeaders:
		return "too-many-headers"
	case skipNoNewsgroups:
		return "no-newsgroups"
	case skipBadGroup:
		return "bad-group"
	case skipTooBig:
		return "too-big"
	}
	return "unknown"
}

// isspace is the mbox library's whitespace test: every byte below '!' (a header line
// starting with one continues the previous header).
func isspace(b byte) bool {
	return b < 33
}

// readBody reads a message body into lines without line endings; isBinary is set (and the
// body left out) when keepBinary is false and a line starts with "=ybegin". tooBig is set
// (and the body left out) when the body holds more than maxBytes bytes (maxBytes < 0: no
// limit), so a huge article never sits in memory whole. It reads r in 64 KiB chunks, joins
// a line split over several reads, strips CR and LF at the line end and removes trailing
// empty lines. r is always read to its end.
func readBody(r io.Reader, keepBinary bool, maxBytes int) (body []string, isBinary, tooBig bool) {
	buffer := make([]byte, bodyChunkSize)
	var part []byte // the start of a line not yet ended by '\n'
	size := 0       // body bytes read so far, line endings included
	addLine := func(line string) {
		if !keepBinary && !isBinary && strings.HasPrefix(line, "=ybegin") {
			isBinary = true
			body = nil
		}
		if !isBinary && !tooBig {
			body = append(body, strings.TrimRight(line, "\r\n"))
		}
	}
	for {
		n, err := r.Read(buffer)
		chunk := buffer[:n]
		if size += n; maxBytes >= 0 && size > maxBytes && !tooBig {
			tooBig = true
			body, part = nil, nil
		}
		for len(chunk) > 0 && !tooBig {
			i := bytes.IndexByte(chunk, '\n')
			if i < 0 {
				part = append(part, chunk...)
				break
			}
			if len(part) > 0 {
				part = append(part, chunk[:i+1]...)
				addLine(string(part))
				part = part[:0]
			} else {
				addLine(string(chunk[:i+1]))
			}
			chunk = chunk[i+1:]
		}
		if err != nil {
			// io.EOF or a read error: a last line without '\n' is kept
			if len(part) > 0 {
				addLine(string(part))
			}
			break
		}
	}
	return removeFinalEmptyLines(body), isBinary, tooBig
}

// removeFinalEmptyLines drops empty lines at the end of body (mbox2nntp's
// body_remove_finalemptylines).
func removeFinalEmptyLines(body []string) []string {
	for len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	return body
}

// headerField returns the value of the first header named name (case-insensitive, the
// name may carry spaces before the colon) with its continuation lines joined as they
// are (mbox2nntp's ParseHeaderKeys), trimmed; found is false when there is no such header.
func headerField(head []string, name string) (value string, found bool) {
	for i, line := range head {
		if line == "" || isspace(line[0]) {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || !strings.EqualFold(strings.TrimSpace(line[:colon]), name) {
			continue
		}
		var b strings.Builder
		b.WriteString(line[colon+1:])
		for _, cont := range head[i+1:] {
			if cont == "" || !isspace(cont[0]) {
				break
			}
			b.WriteString(cont)
		}
		return strings.TrimSpace(b.String()), true
	}
	return "", false
}

// repairMessageID applies mbox2nntp's lax repairs (GetMessageID): a ">#1/1" suffix is
// cut at the last '#'; a "<...>" followed by "?(", " (", "(UMass-" or "-(UMass-" and
// ending in ')' is cut after the first '>'.
func repairMessageID(msgid string) string {
	if strings.HasSuffix(msgid, ">#1/1") {
		if i := strings.LastIndex(msgid, "#"); i > 0 {
			return msgid[:i]
		}
		return msgid
	}
	if strings.HasPrefix(msgid, "<") && strings.HasSuffix(msgid, ")") &&
		(strings.Contains(msgid, ">?(") || strings.Contains(msgid, "> (") ||
			strings.Contains(msgid, ">(UMass-") || strings.Contains(msgid, ">-(UMass-")) {
		return msgid[:strings.IndexByte(msgid, '>')+1]
	}
	return msgid
}

// firstMessageID cuts a field holding several IDs, "<a@b> <c@d>" or "<a@b><c@d>", to the
// first (mbox2nntp's lax GetMessageID, v0.3.2).
func firstMessageID(msgid string) string {
	if strings.HasPrefix(msgid, "<") {
		if j := strings.IndexByte(msgid, '>'); j > 0 && strings.HasPrefix(strings.TrimLeft(msgid[j+1:], " \t"), "<") {
			return msgid[:j+1]
		}
	}
	return msgid
}

// mboxrdFormatField names the header field mbox2nntp's exporters (uatexport, ncexport,
// v0.3.0+) end each article's header with: the article is quoted mboxrd, header and body.
const mboxrdFormatField = "X-Mbox2nntp-Format"

// isMboxrdQuoted reports whether head holds "X-Mbox2nntp-Format: mboxrd" (mbox2nntp's
// mboxout.IsQuoted). Old archive mboxes are not quoted: in them a ">From " line is part
// of the article as posted, so only an article with this field is unquoted.
func isMboxrdQuoted(head []string) bool {
	for _, l := range head {
		if name, value, ok := strings.Cut(l, ":"); ok &&
			strings.EqualFold(strings.TrimSpace(name), mboxrdFormatField) &&
			strings.EqualFold(strings.TrimSpace(value), "mboxrd") {
			return true
		}
	}
	return false
}

// mboxrdUnquote undoes the exporters' quote of one line: a '>' before a line that, with
// any number of '>' taken off, starts "From " or "#! rnews " (mbox2nntp's mboxout.Unquote).
func mboxrdUnquote(l string) string {
	if strings.HasPrefix(l, ">") {
		t := strings.TrimLeft(l[1:], ">")
		if strings.HasPrefix(t, "From ") || strings.HasPrefix(t, "#! rnews ") {
			return l[1:]
		}
	}
	return l
}

// laxMessageID reports whether msgid starts with '<', ends with '>' and holds no
// whitespace or control byte (mbox2nntp's -lax-msgid default, without spaces).
func laxMessageID(msgid string) bool {
	if len(msgid) < 2 || msgid[0] != '<' || msgid[len(msgid)-1] != '>' {
		return false
	}
	for i := 0; i < len(msgid); i++ {
		if isspace(msgid[i]) || msgid[i] == 0x7f {
			return false
		}
	}
	return true
}

// messageIDFromHeaders finds the article's message-id (lax repairs as in mbox2nntp; strict
// requires common.ValidMessageID and applies no repair). An early B-news article without a
// Message-ID field but with "Article-I.D.: site.n" gets <bnews.site.n> (bnewsMessageID);
// that form has no "@", so -strict-msgid refuses it as bad-msgid.
func messageIDFromHeaders(head []string, strict bool) (msgid string, reason skipReason) {
	value, found := headerField(head, "Message-ID")
	if !found || value == "" {
		if id, ok := bnewsMessageID(head); ok {
			if strict && !common.ValidMessageID(id) {
				return "", skipBadMessageID
			}
			return id, skipNone
		}
		return "", skipNoMessageID
	}
	if strict {
		if !common.ValidMessageID(value) {
			return "", skipBadMessageID
		}
		return value, skipNone
	}
	value = firstMessageID(value)
	value = repairMessageID(value)
	if !laxMessageID(value) {
		return "", skipBadMessageID
	}
	return value, skipNone
}

// cleanHeadline turns NUL, backspace, tab, CR and LF into spaces (mbox2nntp's
// cleanHeadline, plus CR: the processor rejects a header block holding one).
func cleanHeadline(line string) string {
	if strings.ContainsAny(line, "\x00\010\t\r\n") {
		line = strings.NewReplacer("\x00", " ", "\010", " ", "\t", " ", "\r", " ", "\n", " ").Replace(line)
	}
	return line
}

// isBadDateLine reports whether line starts like a date ("Thu, "), which some
// Google-made headers carry as a line of its own.
func isBadDateLine(line string) bool {
	return len(line) >= 5 &&
		line[0] >= 'A' && line[0] <= 'Z' &&
		line[1] >= 'a' && line[1] <= 'z' &&
		line[2] >= 'a' && line[2] <= 'z' &&
		line[3] == ',' && line[4] == ' '
}

// validFieldName reports whether every byte of name is RFC 5322 ftext (%x21-39 / %x3B-7E),
// the processor's header-block rule for a field name.
func validFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x21 || c > 0x7e || c == ':' {
			return false
		}
	}
	return true
}

// hasNonBlankContinuation reports whether the header line head[i] has a continuation
// line that is not blank once cleaned.
func hasNonBlankContinuation(head []string, i int) bool {
	for _, cont := range head[i+1:] {
		if cont == "" || !isspace(cont[0]) {
			return false
		}
		if strings.TrimSpace(cleanHeadline(cont)) != "" {
			return true
		}
	}
	return false
}

// rewriteHead turns the stored header lines into the ones handed to the processor
// (mbox2nntp's rewriteHead without the Path prefix: the processor prefixes the local
// hostname). Per line: NUL, backspace, tab and CR become spaces; Xref, Expires,
// X-Mbox2nntp-Format and stray
// "From " lines are dropped with their continuations, so are lines without a colon (or
// with a field name that is not RFC 5322 ftext) and orphan or blank continuations;
// a header with an empty value and no continuation is dropped; "Key:value" and
// "Key :value" become "Key: value"; every Message-ID header is replaced by one
// "Message-ID: <msgid>" line at the position of the first (appended when there is none).
// More than maxHeaderLines lines or no Subject skip the article; an article without a Date
// header gets one from a B-news date field when it has a readable one (fallbackDateLine),
// else it is skipped as no-date.
func rewriteHead(msgid string, head []string) (newhead []string, reason skipReason) {
	if len(head) > maxHeaderLines {
		return nil, skipTooManyHeaders
	}
	hasSubject, hasDate, hasMsgID := false, false, false
	dropping := false  // drop the continuation lines of the current header
	haveField := false // a field line was kept, a continuation may follow it
	newhead = make([]string, 0, len(head)+1)

	for i, raw := range head {
		if raw == "" {
			continue
		}
		line := cleanHeadline(raw)
		spacedNext := i+1 < len(head) && head[i+1] != "" && isspace(head[i+1][0])
		spacedPrev := i > 0 && head[i-1] != "" && isspace(head[i-1][0])

		// a line "Thu, ... datestring" of its own: some Google headers are that broken
		if isBadDateLine(line) && spacedPrev {
			if !spacedNext {
				continue
			}
			line = " " + line
		}

		if isspace(line[0]) {
			if dropping || !haveField || strings.TrimSpace(line) == "" {
				continue
			}
			if line[0] != ' ' {
				// another control byte: the processor only folds lines starting with SP/TAB
				line = " " + line[1:]
			}
			newhead = append(newhead, line)
			continue
		}

		// a field line (or junk): decide for it and its continuations
		dropping, haveField = true, false
		if strings.HasPrefix(line, "From ") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		if !validFieldName(key) {
			continue
		}
		dat := strings.TrimSpace(line[colon+1:])
		if dat == "" && !hasNonBlankContinuation(head, i) {
			continue
		}
		switch strings.ToLower(key) {
		case "xref", "expires", "x-mbox2nntp-format":
			continue
		case "message-id":
			if !hasMsgID {
				newhead = append(newhead, "Message-ID: "+msgid)
				hasMsgID = true
			}
			continue
		case "subject":
			hasSubject = true
		case "date":
			hasDate = true
		}
		if dat == "" {
			line = key + ":"
		} else if key != line[:colon] || !strings.HasPrefix(line[colon+1:], " ") {
			line = key + ": " + dat // repair the colon-space
		}
		dropping, haveField = false, true
		newhead = append(newhead, line)
	}

	if !hasSubject {
		return nil, skipNoSubject
	}
	if !hasDate {
		line, ok := fallbackDateLine(head)
		if !ok {
			return nil, skipNoDate
		}
		newhead = append(newhead, line)
	}
	if !hasMsgID {
		newhead = append(newhead, "Message-ID: "+msgid)
	}
	return newhead, skipNone
}

// bnewsDateFields are the date fields a Date line is taken from when the article has none:
// the "Posted:" and "Date-Received:" of the B-news era (the utzoo tapes carry articles with
// only these). The processor has the same fallback (its fallbackDateHeaders), but it reads
// the article's header map, which keeps none of these fields, so the importer writes the
// Date into the stored header block instead.
var bnewsDateFields = []string{"Posted", "Date-Received"}

// minArchiveYear is the first year an archive article can really carry (Usenet started in
// 1980; the processor's minArchiveDate). An earlier fallback date is a broken clock, mostly
// ctime(-1), and is not written into the header block.
const minArchiveYear = 1980

// fallbackDateLine returns the "Date: ..." line of an article that has no Date header,
// built from the first readable B-news date field, as RFC 5322 (a ctime date or any
// spelling the processor's lenient parser knows); ok is false when there is none.
func fallbackDateLine(head []string) (line string, ok bool) {
	for _, name := range bnewsDateFields {
		value, found := headerField(head, name)
		if !found || value == "" {
			continue
		}
		t, parsed := processor.ParseNNTPDateStrict(cleanHeadline(value))
		if !parsed || t.Year() < minArchiveYear {
			continue
		}
		return "Date: " + t.Format(rfc5322DateLayout), true
	}
	return "", false
}

// firstNewsgroup returns the first group of the Newsgroups header (folded lines joined,
// split on [,;:\s]+ like the processor).
func firstNewsgroup(head []string) (group string, reason skipReason) {
	value, _ := headerField(head, "Newsgroups")
	for _, g := range common.SeparatorRegex.Split(value, -1) {
		if g = strings.TrimSpace(g); g == "" {
			continue
		}
		if !common.IsValidGroupName(g) {
			return "", skipBadGroup
		}
		return g, skipNone
	}
	return "", skipNoNewsgroups
}

// buildArticle builds the processor's article from the rewritten head and the body.
func buildArticle(msgid string, head, body []string) (*models.Article, error) {
	lines := make([]string, 0, len(head)+1+len(body))
	lines = append(lines, head...)
	lines = append(lines, "")
	lines = append(lines, body...)
	return nntp.ParseLegacyArticleLines(msgid, lines, true)
}
