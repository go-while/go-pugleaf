package main

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
)

// fixtureMbox holds two articles with the archive.org separator. The first has a folded
// Newsgroups header with three groups, a "Key:value" and a "Key :value" line, an empty
// header, a tab, a stray "From " line and a folded Xref header.
const fixtureMbox = "From nobody Thu Dec  8 13:43:59 2016\n" +
	"Path: a.example!b.example!not-for-mail\n" +
	"From: Alice <alice@example.invalid>\n" +
	"Newsgroups: de.test.one, de.test.two,\n" +
	" de.test.three\n" +
	"Subject: first article\n" +
	"Date: Tue, 24 Jun 2003 19:30:12 +0200\n" +
	"Lines:2\n" +
	"Message-ID: <first@example.invalid>\n" +
	"Organization :Example Org\n" +
	"Keywords:\n" +
	"From someone@example.invalid Mon Jun 30 23:33:23 2003\n" +
	"X-Trace: t\t1 2\n" +
	"Xref: news.example de.test.one:1\n" +
	" de.test.two:2 de.test.three:3\n" +
	"\n" +
	"Body line one\n" +
	">From the archive\n" +
	"\n" +
	"From nobody Thu Dec  8 13:43:59 2016\n" +
	"Path: c.example!not-for-mail\n" +
	"From: Bob <bob@example.invalid>\n" +
	"Newsgroups: de.test.two\n" +
	"Subject: Re: first article\n" +
	"Date: Wed, 25 Jun 2003 08:00:00 +0200\n" +
	"References: <first@example.invalid>\n" +
	"Message-ID: <second@example.invalid>\n" +
	"\n" +
	"reply body\n" +
	"\n" +
	"\n"

var fixtureWant = []struct {
	msgid string
	group string
	head  []string
	body  []string
}{
	{
		msgid: "<first@example.invalid>",
		group: "de.test.one",
		head: []string{
			"Path: a.example!b.example!not-for-mail",
			"From: Alice <alice@example.invalid>",
			"Newsgroups: de.test.one, de.test.two,",
			" de.test.three",
			"Subject: first article",
			"Date: Tue, 24 Jun 2003 19:30:12 +0200",
			"Lines: 2",
			"Message-ID: <first@example.invalid>",
			"Organization: Example Org",
			"X-Trace: t 1 2",
		},
		body: []string{"Body line one", ">From the archive"},
	},
	{
		msgid: "<second@example.invalid>",
		group: "de.test.two",
		head: []string{
			"Path: c.example!not-for-mail",
			"From: Bob <bob@example.invalid>",
			"Newsgroups: de.test.two",
			"Subject: Re: first article",
			"Date: Wed, 25 Jun 2003 08:00:00 +0200",
			"References: <first@example.invalid>",
			"Message-ID: <second@example.invalid>",
		},
		body: []string{"reply body"},
	},
}

// TestMboxArtRewriteFixture parses the fixture through openSource and checks the
// rewritten heads byte for byte, and that the processor's bulk header check keeps them.
func TestMboxArtRewriteFixture(t *testing.T) {
	path := writeFile(t, "fixture.mbox", fixtureMbox)
	src, err := openSource(path, "7z")
	if err != nil {
		t.Fatalf("openSource: %v", err)
	}
	defer func() { _ = src.Close() }()
	msgs := readAllMessages(t, src)
	if len(msgs) != len(fixtureWant) {
		t.Fatalf("got %d messages, want %d", len(msgs), len(fixtureWant))
	}
	for i, want := range fixtureWant {
		m := msgs[i]
		msgid, reason := messageIDFromHeaders(m.head, false)
		if reason != skipNone || msgid != want.msgid {
			t.Fatalf("message %d: msgid %q reason %v, want %q", i, msgid, reason, want.msgid)
		}
		head, reason := rewriteHead(msgid, m.head)
		if reason != skipNone {
			t.Fatalf("message %d: rewriteHead reason %v", i, reason)
		}
		if !reflect.DeepEqual(head, want.head) {
			t.Errorf("message %d head:\n got %q\nwant %q", i, head, want.head)
		}
		if !reflect.DeepEqual(m.body, want.body) {
			t.Errorf("message %d body:\n got %q\nwant %q", i, m.body, want.body)
		}
		group, reason := firstNewsgroup(head)
		if reason != skipNone || group != want.group {
			t.Errorf("message %d: group %q reason %v, want %q", i, group, reason, want.group)
		}
		normalized, err := common.CheckHeaderBlock(head, common.HeaderBlockFetch)
		if err != nil {
			t.Fatalf("message %d: CheckHeaderBlock: %v", i, err)
		}
		if !reflect.DeepEqual(normalized, head) {
			t.Errorf("message %d: CheckHeaderBlock changed the head:\n got %q\nwant %q", i, normalized, head)
		}
		if got := common.HeaderValue(normalized, "Message-ID"); got != msgid {
			t.Errorf("message %d: Message-ID header %q, want %q", i, got, msgid)
		}
	}
	if src.Unread() {
		t.Error("Unread after a full read")
	}
}

func TestMboxArtSkipReasonStrings(t *testing.T) {
	seen := map[string]skipReason{}
	for r := skipNone; r <= skipBadGroup; r++ {
		s := r.String()
		if s == "unknown" || s == "" {
			t.Errorf("reason %d has no name", r)
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("reasons %d and %d share the name %q", prev, r, s)
		}
		seen[s] = r
	}
	if got := skipReason(99).String(); got != "unknown" {
		t.Errorf("skipReason(99) = %q", got)
	}
}

func TestMboxArtSkipReasons(t *testing.T) {
	base := func(extra ...string) []string {
		h := []string{
			"From: a@example.invalid",
			"Newsgroups: de.test.one",
			"Subject: s",
			"Date: Tue, 24 Jun 2003 19:30:12 +0200",
			"Message-ID: <x@example.invalid>",
		}
		return append(h, extra...)
	}
	without := func(name string) []string {
		var out []string
		for _, l := range base() {
			if !strings.HasPrefix(l, name+":") {
				out = append(out, l)
			}
		}
		return out
	}

	if _, r := messageIDFromHeaders(without("Message-ID"), false); r != skipNoMessageID {
		t.Errorf("no Message-ID: %v", r)
	}
	if _, r := messageIDFromHeaders(append(without("Message-ID"), "Message-ID:"), false); r != skipNoMessageID {
		t.Errorf("empty Message-ID: %v", r)
	}
	if _, r := messageIDFromHeaders(append(without("Message-ID"), "Message-ID: no-brackets@x"), false); r != skipBadMessageID {
		t.Errorf("bad Message-ID: %v", r)
	}
	if _, r := rewriteHead("<x@example.invalid>", without("Subject")); r != skipNoSubject {
		t.Errorf("no Subject: %v", r)
	}
	if _, r := rewriteHead("<x@example.invalid>", append(without("Subject"), "Subject:")); r != skipNoSubject {
		t.Errorf("empty Subject: %v", r)
	}
	if _, r := rewriteHead("<x@example.invalid>", append(without("Subject"), "Subject:", " ")); r != skipNoSubject {
		t.Errorf("Subject with a blank continuation: %v", r)
	}
	if _, r := rewriteHead("<x@example.invalid>", append(without("Subject"), "Subject:", "\t", " \x00")); r != skipNoSubject {
		t.Errorf("Subject with blank continuations: %v", r)
	}
	if _, r := rewriteHead("<x@example.invalid>", without("Date")); r != skipNoDate {
		t.Errorf("no Date: %v", r)
	}
	if _, r := rewriteHead("<x@example.invalid>", append(without("Date"), "Date:", " ")); r != skipNoDate {
		t.Errorf("Date with a blank continuation: %v", r)
	}
	if got, r := rewriteHead("<x@example.invalid>", append(without("Subject"), "Subject:", " ", " real")); r != skipNone ||
		!reflect.DeepEqual(got[len(got)-2:], []string{"Subject:", " real"}) {
		t.Errorf("Subject with a blank and a real continuation: %q %v", got, r)
	}
	many := base()
	for len(many) <= maxHeaderLines {
		many = append(many, "X-Filler: f")
	}
	if _, r := rewriteHead("<x@example.invalid>", many); r != skipTooManyHeaders {
		t.Errorf("%d header lines: %v", len(many), r)
	}
	if _, r := rewriteHead("<x@example.invalid>", many[:maxHeaderLines]); r != skipNone {
		t.Errorf("%d header lines: %v", maxHeaderLines, r)
	}
	if _, r := firstNewsgroup(without("Newsgroups")); r != skipNoNewsgroups {
		t.Errorf("no Newsgroups: %v", r)
	}
	if _, r := firstNewsgroup(append(without("Newsgroups"), "Newsgroups: , ;")); r != skipNoNewsgroups {
		t.Errorf("empty Newsgroups: %v", r)
	}
	if _, r := firstNewsgroup(append(without("Newsgroups"), "Newsgroups: bad/group!name,de.test.one")); r != skipBadGroup {
		t.Errorf("bad first group: %v", r)
	}
	if body, bin := readBody(strings.NewReader("text\n=ybegin line=128 size=3 name=a.bin\nabc\n=yend\n"), false); !bin || body != nil {
		t.Errorf("binary: isBinary=%v body=%q", bin, body)
	}
	if body, _ := readBody(strings.NewReader("\n\r\n\n"), false); len(body) != 0 {
		t.Errorf("empty body (no-body): %q", body)
	}
	if _, r := rewriteHead("<x@example.invalid>", base()); r != skipNone {
		t.Errorf("complete head: %v", r)
	}
}

func TestMboxArtFirstNewsgroupFolded(t *testing.T) {
	cases := []struct {
		head []string
		want string
	}{
		{[]string{"Newsgroups:", " de.test.one,", "\tde.test.two"}, "de.test.one"},
		{[]string{"newsgroups :  de.test.one;de.test.two"}, "de.test.one"},
		{[]string{"Subject: s", "Newsgroups: de.test.one:de.test.two", " de.test.three"}, "de.test.one"},
		{[]string{"X-Newsgroups: other.group", "Newsgroups: , de.test.two"}, "de.test.two"},
	}
	for _, tc := range cases {
		got, r := firstNewsgroup(tc.head)
		if r != skipNone || got != tc.want {
			t.Errorf("%q: got %q %v, want %q", tc.head, got, r, tc.want)
		}
	}
}

func TestMboxArtMessageIDLaxStrict(t *testing.T) {
	cases := []struct {
		header       []string
		lax, strict  string
		laxR, strctR skipReason
	}{
		{[]string{"Message-ID: <a@b.example>"}, "<a@b.example>", "<a@b.example>", skipNone, skipNone},
		{[]string{"message-id:<a@b.example>  "}, "<a@b.example>", "<a@b.example>", skipNone, skipNone},
		{[]string{"Message-ID:", " <a@b.example>"}, "<a@b.example>", "<a@b.example>", skipNone, skipNone},
		{[]string{"Message-ID: <a@b.example>#1/1"}, "<a@b.example>", "", skipNone, skipBadMessageID},
		{[]string{"Message-ID: <a@b.example> (comment)"}, "<a@b.example>", "", skipNone, skipBadMessageID},
		{[]string{"Message-ID: <a@b.example>?(x)"}, "<a@b.example>", "", skipNone, skipBadMessageID},
		{[]string{"Message-ID: <a@b.example>(UMass-Amherst)"}, "<a@b.example>", "", skipNone, skipBadMessageID},
		{[]string{"Message-ID: <a@b.example>-(UMass-Amherst)"}, "<a@b.example>", "", skipNone, skipBadMessageID},
		// lax accepts what has brackets and no whitespace; strict wants RFC 5536
		{[]string{"Message-ID: <no-at-sign>"}, "<no-at-sign>", "", skipNone, skipBadMessageID},
		{[]string{"Message-ID: <a b@c.example>"}, "", "", skipBadMessageID, skipBadMessageID},
		{[]string{"Message-ID: <a@", " b.example>"}, "", "", skipBadMessageID, skipBadMessageID},
		{[]string{"Message-ID: a@b.example"}, "", "", skipBadMessageID, skipBadMessageID},
		{[]string{"Subject: <a@b.example>"}, "", "", skipNoMessageID, skipNoMessageID},
	}
	for _, tc := range cases {
		got, r := messageIDFromHeaders(tc.header, false)
		if got != tc.lax || r != tc.laxR {
			t.Errorf("lax %q: got %q %v, want %q %v", tc.header, got, r, tc.lax, tc.laxR)
		}
		got, r = messageIDFromHeaders(tc.header, true)
		if got != tc.strict || r != tc.strctR {
			t.Errorf("strict %q: got %q %v, want %q %v", tc.header, got, r, tc.strict, tc.strctR)
		}
	}
}

// chunkReader returns at most n bytes per Read.
type chunkReader struct {
	data []byte
	n    int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	k := c.n
	if k > len(p) {
		k = len(p)
	}
	if k > len(c.data) {
		k = len(c.data)
	}
	copy(p, c.data[:k])
	c.data = c.data[k:]
	return k, nil
}

func TestMboxArtReadBody(t *testing.T) {
	long := strings.Repeat("x", 3*bodyChunkSize+17)
	input := "first\r\n" + long + "\n" + "=ybegin is text here\n" + "\n" + "last without newline"
	want := []string{"first", long, "=ybegin is text here", "", "last without newline"}

	for _, tc := range []struct {
		name string
		r    io.Reader
	}{
		{"one read", strings.NewReader(input)},
		{"small reads", &chunkReader{data: []byte(input), n: 7}},
		{"chunk-sized reads", &chunkReader{data: []byte(input), n: bodyChunkSize}},
	} {
		body, bin := readBody(tc.r, true)
		if bin {
			t.Errorf("%s: isBinary with keepBinary", tc.name)
		}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("%s: got %d lines, want %d", tc.name, len(body), len(want))
			for i := range body {
				if i < len(want) && body[i] != want[i] {
					t.Errorf("%s: line %d differs (len %d, want len %d)", tc.name, i, len(body[i]), len(want[i]))
				}
			}
		}
	}

	r := &chunkReader{data: []byte(input), n: 1000}
	body, bin := readBody(r, false)
	if !bin || body != nil {
		t.Errorf("=ybegin without keepBinary: isBinary=%v body=%d lines", bin, len(body))
	}
	if len(r.data) != 0 {
		t.Errorf("readBody left %d bytes unread", len(r.data))
	}

	body, _ = readBody(strings.NewReader("a\r\n\r\nb\r\n\r\n\n\r\n"), false)
	if !reflect.DeepEqual(body, []string{"a", "", "b"}) {
		t.Errorf("CRLF and final empty lines: %q", body)
	}
}

func TestMboxArtRewriteRules(t *testing.T) {
	msgid := "<x@example.invalid>"
	head := []string{
		" orphan continuation",
		"Thu, 5 Aug 2004 21:50:04 EDT", // not after a continuation: kept, repaired, invalid name dropped
		"From: a@example.invalid",
		"Message-ID: <other@example.invalid>",
		" folded-msgid-tail",
		"Subject :spaced key",
		"X-Folded: one",
		" Thu, 5 Aug 2004 21:50:04 EDT",
		"Thu, 5 Aug 2004 21:50:04 EDT", // after a continuation, before a field: dropped
		"X-Two: a",
		" b",
		"Thu, 5 Aug 2004 21:50:04 EDT", // between continuations: becomes one
		" c",
		"Date: Tue, 24 Jun 2003 19:30:12 +0200",
		"Expires: Fri, 1 Jan 2010 00:00:00 GMT",
		" expires-tail",
		"no colon here",
		" no-colon tail",
		"X-Nul: a\x00b\bc\rd",
		"X-Empty:",
		"X-Empty-Folded:",
		" value",
		"x-blank: x",
		" \t ",
		"Message-ID: <x@example.invalid>",
		"X-Last: ok",
	}
	want := []string{
		"From: a@example.invalid",
		"Message-ID: <x@example.invalid>",
		"Subject: spaced key",
		"X-Folded: one",
		" Thu, 5 Aug 2004 21:50:04 EDT",
		"X-Two: a",
		" b",
		" Thu, 5 Aug 2004 21:50:04 EDT",
		" c",
		"Date: Tue, 24 Jun 2003 19:30:12 +0200",
		"X-Nul: a b c d",
		"X-Empty-Folded:",
		" value",
		"x-blank: x",
		"X-Last: ok",
	}
	got, r := rewriteHead(msgid, head)
	if r != skipNone {
		t.Fatalf("reason %v", r)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rewrite:\n got %q\nwant %q", got, want)
	}
	if _, err := common.CheckHeaderBlock(got, common.HeaderBlockFetch); err != nil {
		t.Errorf("CheckHeaderBlock: %v", err)
	}

	// a continuation starting with another control byte is folded with a space, so the
	// processor's header check and ParseHeaders see the same lines
	got, r = rewriteHead(msgid, []string{"From: a", "\fSubject: evil", "Subject: real", "Date: d", "\vx"})
	want = []string{"From: a", " Subject: evil", "Subject: real", "Date: d", " x", "Message-ID: " + msgid}
	if r != skipNone || !reflect.DeepEqual(got, want) {
		t.Fatalf("control-byte continuation:\n got %q %v\nwant %q", got, r, want)
	}
	normalized, err := common.CheckHeaderBlock(got, common.HeaderBlockFetch)
	if err != nil || !reflect.DeepEqual(normalized, got) {
		t.Errorf("CheckHeaderBlock: %q %v", normalized, err)
	}
	art, err := buildArticle(msgid, got, []string{"body"})
	if err != nil || art.Subject != "real" {
		t.Errorf("buildArticle: Subject %q %v", art.Subject, err)
	}

	// a lost Message-ID header is appended; Path is kept as stored
	got, r = rewriteHead(msgid, []string{"Path: x!y", "Subject: s", "Date: d"})
	if r != skipNone || !reflect.DeepEqual(got, []string{"Path: x!y", "Subject: s", "Date: d", "Message-ID: " + msgid}) {
		t.Errorf("no Message-ID header: %q %v", got, r)
	}
}

func TestMboxArtBuildArticle(t *testing.T) {
	head := []string{
		"Path: c.example!not-for-mail",
		"From: Bob <bob@example.invalid>",
		"Newsgroups: de.test.two",
		"Subject: Re: first",
		" article",
		"Date: Wed, 25 Jun 2003 08:00:00 +0200",
		"References: <first@example.invalid>",
		" <second@example.invalid>",
		"Message-ID: <third@example.invalid>",
	}
	body := []string{"line one", "", "line three"}
	art, err := buildArticle("<third@example.invalid>", head, body)
	if err != nil {
		t.Fatalf("buildArticle: %v", err)
	}
	if art.MessageID != "<third@example.invalid>" {
		t.Errorf("MessageID %q", art.MessageID)
	}
	if art.Subject != "Re: first article" {
		t.Errorf("Subject %q", art.Subject)
	}
	if art.Path != "c.example!not-for-mail" {
		t.Errorf("Path %q", art.Path)
	}
	if !reflect.DeepEqual(art.RefSlice, []string{"<first@example.invalid>", "<second@example.invalid>"}) {
		t.Errorf("RefSlice %q", art.RefSlice)
	}
	if want := common.WireArticleSize(head, body); art.Bytes != want {
		t.Errorf("Bytes %d, want %d", art.Bytes, want)
	}
	if art.Lines != 3 || art.BodyText != "line one\n\nline three" {
		t.Errorf("Lines %d BodyText %q", art.Lines, art.BodyText)
	}
	if art.HeadersJSON != strings.Join(head, "\n") {
		t.Errorf("HeadersJSON %q", art.HeadersJSON)
	}
	if art.DateString != "Wed, 25 Jun 2003 08:00:00 +0200" || art.FromHeader != "Bob <bob@example.invalid>" {
		t.Errorf("DateString %q FromHeader %q", art.DateString, art.FromHeader)
	}
	// the caller's slices are not changed
	if len(head) != 9 || head[8] != "Message-ID: <third@example.invalid>" {
		t.Errorf("head changed: %q", head)
	}
}
