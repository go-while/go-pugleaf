package main

// Tests for the mbox2nntp v0.3.2 sync: -max-article-size (too-big), mboxrd unquoting of
// articles written by mbox2nntp's exporters, the lax cut of a field holding several
// Message-IDs, and the RFC 4155 separator line of go-lib-mbox2nntp 73e35b8.

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/mbox2nntp/uat"
)

// captureSink builds every article and keeps it by message-id.
type captureSink struct {
	mu   sync.Mutex
	arts map[string]*models.Article
}

func newCaptureSink() *captureSink { return &captureSink{arts: make(map[string]*models.Article)} }

func (s *captureSink) Handle(msgid string, art func() (*models.Article, error), _ string) (bool, bool, error) {
	a, err := art()
	if err != nil {
		return false, false, fmt.Errorf("%w: %v", errRejected, err)
	}
	s.mu.Lock()
	s.arts[msgid] = a
	s.mu.Unlock()
	return true, false, nil
}

func (s *captureSink) beginInput() inputSink { return s }

func (s *captureSink) endInput(time.Duration) error { return nil }

// syArticle is one mbox article with the given extra header lines and body.
func syArticle(msgid string, extraHead []string, body string) string {
	var b strings.Builder
	b.WriteString("From nobody Thu Dec  8 12:55:04 2016\n")
	b.WriteString("From: a@example.invalid\n")
	b.WriteString("Newsgroups: de.test.sync\n")
	b.WriteString("Subject: sync test\n")
	b.WriteString("Date: Thu, 08 Dec 2016 12:55:04 +0000\n")
	b.WriteString("Message-ID: " + msgid + "\n")
	for _, l := range extraHead {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n" + body + "\n")
	return b.String()
}

// syReader counts how much of it was read.
type syReader struct {
	r    io.Reader
	read int
}

func (s *syReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.read += n
	return n, err
}

func TestMboxSyncReadBodyLimit(t *testing.T) {
	body := strings.Repeat("0123456789\n", 10) // 110 bytes
	for _, tc := range []struct {
		max     int
		tooBig  bool
		wantLen int
	}{
		{-1, false, 10},
		{110, false, 10},
		{109, true, 0},
		{0, true, 0},
	} {
		r := &syReader{r: strings.NewReader(body)}
		got, _, tooBig := readBody(r, false, tc.max)
		if tooBig != tc.tooBig || len(got) != tc.wantLen {
			t.Errorf("max=%d: tooBig=%t lines=%d, want %t %d", tc.max, tooBig, len(got), tc.tooBig, tc.wantLen)
		}
		if r.read != len(body) {
			t.Errorf("max=%d: read %d of %d bytes: the body must be drained", tc.max, r.read, len(body))
		}
	}
	// one line longer than the limit, no newline at all
	if got, _, tooBig := readBody(strings.NewReader(strings.Repeat("x", 3*bodyChunkSize)), false, bodyChunkSize); !tooBig || got != nil {
		t.Errorf("long line: tooBig=%t lines=%d", tooBig, len(got))
	}
}

func TestMboxSyncFirstMessageID(t *testing.T) {
	cases := []struct{ in, lax, strictReason string }{
		{"<a@b.example> <c@d.example>", "<a@b.example>", "bad-msgid"},
		{"<a@b.example><c@d.example>", "<a@b.example>", "bad-msgid"},
		{"<a@b.example>\t<c@d.example>", "<a@b.example>", "bad-msgid"},
		{"<a@b.example>", "<a@b.example>", "none"},
		{"<a@b.example> (comment)", "<a@b.example>", "bad-msgid"}, // the old repair, unchanged
	}
	for _, tc := range cases {
		head := []string{"Message-ID: " + tc.in}
		if got, r := messageIDFromHeaders(head, false); got != tc.lax || r != skipNone {
			t.Errorf("lax %q: %q %v, want %q", tc.in, got, r, tc.lax)
		}
		if _, r := messageIDFromHeaders(head, true); r.String() != tc.strictReason {
			t.Errorf("strict %q: %v, want %s", tc.in, r, tc.strictReason)
		}
	}
}

func TestMboxSyncMboxrdHelpers(t *testing.T) {
	if !isMboxrdQuoted([]string{"Subject: x", "x-mbox2nntp-format :  MBOXRD "}) {
		t.Error("format field not recognised")
	}
	if isMboxrdQuoted([]string{"Subject: x", "X-Mbox2nntp-Format: mboxo"}) {
		t.Error("another format taken for mboxrd")
	}
	for in, want := range map[string]string{
		">From here":        "From here",
		">>From here":       ">From here",
		">#! rnews 12":      "#! rnews 12",
		">>#! rnews 12":     ">#! rnews 12",
		">quoted text":      ">quoted text",
		"From here":         "From here",
		">":                 ">",
		"> From with space": "> From with space",
	} {
		if got := mboxrdUnquote(in); got != want {
			t.Errorf("unquote %q = %q, want %q", in, got, want)
		}
	}
	head := []string{"From: a@example.invalid", "Subject: s", "Date: Thu, 08 Dec 2016 12:55:04 +0000",
		"X-Mbox2nntp-Format: mboxrd", " continued", "Newsgroups: de.test.sync"}
	out, r := rewriteHead("<a@b.example>", head)
	if r != skipNone {
		t.Fatalf("rewriteHead: %v", r)
	}
	for _, l := range out {
		if strings.Contains(strings.ToLower(l), "mbox2nntp") || l == " continued" {
			t.Errorf("format field or its continuation kept: %q", out)
		}
	}
}

// TestMboxSyncImport runs an mbox through importInputs: the quoted article is unquoted in
// header and body, an unquoted one keeps its ">From " line, a too-big article is skipped
// and counted, and a message after an RFC 4155 separator line is read on its own.
func TestMboxSyncImport(t *testing.T) {
	big := strings.Repeat("big body line\n", 200) // 2800 bytes
	content := syArticle("<quoted@sync.example>", []string{">From-Like: header", "X-Mbox2nntp-Format: mboxrd"},
		"first\n>From the quoted body\n>>From twice\n>not a from line") +
		syArticle("<plain@sync.example>", nil, "first\n>From kept as posted") +
		syArticle("<big@sync.example>", nil, big) +
		strings.Replace(syArticle("<rfc4155@sync.example>", nil, "after the RFC 4155 line"),
			"From nobody Thu Dec  8 12:55:04 2016", "From user@example.com Thu Dec  8 12:55:04 2016", 1)
	in := writeFile(t, "de.test.sync.mbox", content)

	sink := newCaptureSink()
	res := importInputs([]string{in}, &runOptions{maxArticleSize: 1024}, sink, make(chan struct{}))
	if res.Total.Read != 4 || res.Total.Stored != 3 || res.Total.Skipped[skipTooBig] != 1 {
		t.Fatalf("read=%d stored=%d skipped=%s", res.Total.Read, res.Total.Stored, res.Total.skippedDetail())
	}
	if d := res.Total.skippedDetail(); d != "too-big=1" {
		t.Errorf("skipped detail %q", d)
	}

	q := sink.arts["<quoted@sync.example>"]
	if q == nil {
		t.Fatal("quoted article not stored")
	}
	if want := "first\nFrom the quoted body\n>From twice\n>not a from line"; q.BodyText != want {
		t.Errorf("quoted body %q, want %q", q.BodyText, want)
	}
	for k := range q.Headers {
		if strings.EqualFold(k, mboxrdFormatField) {
			t.Errorf("format field stored: %v", q.Headers)
		}
	}
	if p := sink.arts["<plain@sync.example>"]; p == nil || p.BodyText != "first\n>From kept as posted" {
		t.Errorf("unquoted article changed: %+v", p)
	}
	if a := sink.arts["<rfc4155@sync.example>"]; a == nil || a.BodyText != "after the RFC 4155 line" {
		t.Errorf("message after the RFC 4155 separator: %+v", a)
	}

	// 0 = no limit: the big article is stored
	in = writeFile(t, "de.test.sync.big.mbox", syArticle("<big@sync.example>", nil, big))
	sink = newCaptureSink()
	res = importInputs([]string{in}, &runOptions{}, sink, make(chan struct{}))
	if res.Total.Stored != 1 || res.Total.Skipped[skipTooBig] != 0 {
		t.Errorf("no limit: stored=%d skipped=%s", res.Total.Stored, res.Total.skippedDetail())
	}
}

// TestMboxSyncUATTooBig: a UAT message over uat.MaxMessageSize is read past by the reader
// and counted as read and too-big.
func TestMboxSyncUATTooBig(t *testing.T) {
	dir := t.TempDir()
	msgs := []string{
		uaMessage(uaIDPolish, uaGroup, uaPolishSub, "small\n"),
		uaMessage(uaIDCross, uaGroup, "big one", strings.Repeat("big body line\n", 200)),
	}
	in := uaWrite(t, dir, uaGroup, msgs)
	old := uat.MaxMessageSize
	uat.MaxMessageSize = 1024
	t.Cleanup(func() { uat.MaxMessageSize = old })

	sink := newCountingSink()
	res := importInputs([]string{in}, &runOptions{zstd: "zstd"}, sink, make(chan struct{}))
	if res.Total.Read != 2 || res.Total.Stored != 1 || res.Total.Skipped[skipTooBig] != 1 {
		t.Errorf("read=%d stored=%d skipped=%s", res.Total.Read, res.Total.Stored, res.Total.skippedDetail())
	}
}
