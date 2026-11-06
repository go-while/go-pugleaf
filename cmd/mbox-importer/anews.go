package main

// A-news batches and early B-news Message-IDs (plan mbox-import-discs). The oldest utzoo
// files (1981-83) hold A-news articles: "A<site>.<n>", newsgroups, path, ctime date, title,
// body, one after the other. They get Google Groups' Message-ID forms, so the same article
// from another archive is a duplicate: <anews.A<site>.<n>> and, for a B-news article with
// only "Article-I.D.: site.n", <bnews.site.n>.

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// anewsHeaderLines is the length of an A-news header block: id, newsgroups, path, date,
// title. An article starts only where all five lines are there (anewsHeader).
const anewsHeaderLines = 5

// anewsIDRe matches the first line of an A-news article: "A" + site + "." + number
// ("Aalice.154", "Awatmath.1472").
var anewsIDRe = regexp.MustCompile(`^A[A-Za-z0-9_.-]+\.[0-9]+$`)

// bnewsArticleIDRe matches the "site.number" value of an early B-news "Article-I.D."
// field ("decvax.123", "watmath.1472").
var bnewsArticleIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]*\.[0-9]+$`)

// bnewsArticleIDFields are the spellings of the B-news article-id field (headerField
// compares the name case-insensitively).
var bnewsArticleIDFields = []string{"Article-I.D.", "Article-ID"}

// ctimeLayouts are the spellings of the A-news date line (and of a B-news "Posted:"):
// ctime(3) as the 1981-83 sites wrote it, with or without a zone name. time.Parse reads a
// zone name it does not know as +0000, which is what these articles are taken to be.
var ctimeLayouts = []string{
	"Mon Jan _2 15:04:05 2006",
	"Mon Jan _2 15:04:05 MST 2006",
	"Mon Jan _2 15:04:05 2006 MST",
}

// rfc5322DateLayout is the Date header the A-news header blocks (and a Date taken from a
// B-news fallback field, see fallbackDateLine) are written with.
const rfc5322DateLayout = "Mon, 02 Jan 2006 15:04:05 -0700"

// parseCtime parses a ctime(3) date line, runs of whitespace included.
func parseCtime(line string) (time.Time, bool) {
	line = strings.Join(strings.Fields(line), " ")
	for _, layout := range ctimeLayouts {
		if t, err := time.Parse(layout, line); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// anewsReader reads the lines of a batch with a look-ahead window, so a line can be looked
// at before it is known whether it starts an article or belongs to the previous body. A CR
// before the LF is dropped; err holds the first read error that was not io.EOF.
type anewsReader struct {
	br  *bufio.Reader
	win []string // lines read but not consumed
	eof bool
	err error
}

// fill reads until the window holds n lines or the input ends.
func (a *anewsReader) fill(n int) {
	for len(a.win) < n && !a.eof && a.err == nil {
		line, err := a.br.ReadString('\n')
		if err != nil {
			a.eof = true
			if err != io.EOF {
				a.err = err
			}
			if line == "" {
				break
			}
		}
		a.win = append(a.win, strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	}
}

// peek returns the i-th line that was not consumed yet; ok is false past the end.
func (a *anewsReader) peek(i int) (line string, ok bool) {
	a.fill(i + 1)
	if i < len(a.win) {
		return a.win[i], true
	}
	return "", false
}

// advance consumes the next n lines (fewer at the end of the input).
func (a *anewsReader) advance(n int) {
	a.win = a.win[min(n, len(a.win)):]
}

// anewsHeader builds the RFC 5322 header block of the A-news article that starts at the
// next unread line, or reports that none starts there: the first of the five lines must be
// an A-news id and the fourth a ctime date (a line that only looks like an id belongs to
// the body). The lines are not consumed.
func anewsHeader(a *anewsReader) (head []string, ok bool) {
	block := make([]string, 0, anewsHeaderLines)
	for i := 0; i < anewsHeaderLines; i++ {
		line, have := a.peek(i)
		if !have {
			return nil, false
		}
		block = append(block, line)
	}
	if !anewsIDRe.MatchString(block[0]) {
		return nil, false
	}
	date, ok := parseCtime(block[3])
	if !ok {
		return nil, false
	}
	head = make([]string, 0, anewsHeaderLines+1)
	if path := strings.TrimSpace(block[2]); path != "" {
		head = append(head, "Path: "+path)
		if from := anewsFrom(path); from != "" {
			head = append(head, "From: "+from)
		}
	}
	return append(head,
		"Newsgroups: "+strings.TrimSpace(block[1]),
		"Subject: "+strings.TrimSpace(block[4]),
		"Date: "+date.Format(rfc5322DateLayout),
		"Message-ID: <anews."+block[0]+">",
	), true
}

// anewsFrom is the From header of an A-news article: the last two elements of the bang
// path, "alice!ark" of "utzoo!decvax!ucbvax!mhtsa!research!alice!ark" (A-news carried no
// author field of its own). A path with one element is used as it is; "" when there is none.
func anewsFrom(path string) string {
	var parts []string
	for _, p := range strings.Split(path, "!") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "!")
}

// parseANews reads an A-news batch from r and calls emit once per article with an RFC 5322
// header block (Path, From, Newsgroups, Subject, Date, Message-ID) and the body. The body
// runs to the next header block, so the lines of a block that is broken (no readable ctime
// date) stay with the previous article; text before the first block is dropped. An error is
// returned for a read error, and for a batch that holds no article at all (the dropped line
// count is in its message); emit may have been called before either.
func parseANews(r io.Reader, emit func(head []string, body io.Reader)) error {
	a := &anewsReader{br: bufio.NewReader(r)}
	var (
		head    []string // header block of the article being read, nil before the first
		body    []string
		emitted int
		dropped int // non-blank lines before the first article
	)
	flush := func() {
		if head == nil {
			return
		}
		var b strings.Builder
		for _, l := range body {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		emit(head, strings.NewReader(b.String()))
		emitted++
		head, body = nil, nil
	}
	for {
		if next, ok := anewsHeader(a); ok {
			flush()
			head = next
			a.advance(anewsHeaderLines)
			continue
		}
		line, ok := a.peek(0)
		if !ok {
			break
		}
		a.advance(1)
		switch {
		case head != nil:
			body = append(body, line)
		case strings.TrimSpace(line) != "":
			dropped++
		}
	}
	flush()
	if a.err != nil {
		return fmt.Errorf("read A-news batch: %w", a.err)
	}
	if emitted == 0 {
		return fmt.Errorf("no A-news article in the batch (%d lines dropped)", dropped)
	}
	return nil
}

// bnewsMessageID returns <bnews.ID> for a header block that has an "Article-I.D.: ID" field
// and no Message-ID field, the Message-ID Google Groups gave those articles. The whole value
// must be the "site.number" form B-news wrote; anything else (a folded value, a comment, an
// id that is already a message-id in angle brackets) is left to the no-msgid rule.
func bnewsMessageID(head []string) (string, bool) {
	if value, found := headerField(head, "Message-ID"); found && value != "" {
		return "", false
	}
	for _, name := range bnewsArticleIDFields {
		value, found := headerField(head, name)
		if !found {
			continue
		}
		if bnewsArticleIDRe.MatchString(value) {
			return "<bnews." + value + ">", true
		}
	}
	return "", false
}
