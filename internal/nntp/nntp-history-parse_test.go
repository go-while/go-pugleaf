package nntp

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func nntpHistTestHeadLines() []string {
	return []string{
		"Path: news.example.org!feeder.example.net!not-for-mail",
		"From: Alice Example <alice@example.org>",
		"Newsgroups: comp.lang.go, alt.test",
		"Subject: Re: a folded",
		"\tsubject line",
		"Date: Sat, 12 Sep 2026 10:11:12 +0000",
		"Message-ID: <reply-2@example.org>",
		"References: <root-1@example.org> <child-1@example.org>",
		"Xref: news.example.org comp.lang.go:123 alt.test:456",
	}
}

func nntpHistTestBodyLines() []string {
	return []string{
		"First body line.",
		"",
		".a line that was dot-stuffed on the wire",
		"> quoted text",
		"-- ",
		"sig",
	}
}

func nntpHistTestJoin(head, body []string, withBody bool) []string {
	lines := append([]string{}, head...)
	lines = append(lines, "")
	if withBody {
		lines = append(lines, body...)
	}
	return lines
}

func nntpHistTestCompare(t *testing.T, a, b interface{}, field string) {
	t.Helper()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%s mismatch after round-trip:\n first: %#v\nsecond: %#v", field, a, b)
	}
}

func nntpHistTestRoundTrip(t *testing.T, lines []string) {
	t.Helper()
	msgID := "<reply-2@example.org>"
	first, err := ParseLegacyArticleLines(msgID, lines, true)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	rebuilt := strings.Split(first.HeadersJSON, "\n")
	rebuilt = append(rebuilt, "")
	if first.BodyText != "" {
		rebuilt = append(rebuilt, strings.Split(first.BodyText, "\n")...)
	}
	second, err := ParseLegacyArticleLines(msgID, rebuilt, true)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	nntpHistTestCompare(t, first.Subject, second.Subject, "Subject")
	nntpHistTestCompare(t, first.FromHeader, second.FromHeader, "FromHeader")
	nntpHistTestCompare(t, first.DateString, second.DateString, "DateString")
	nntpHistTestCompare(t, first.References, second.References, "References")
	nntpHistTestCompare(t, first.RefSlice, second.RefSlice, "RefSlice")
	nntpHistTestCompare(t, first.Path, second.Path, "Path")
	nntpHistTestCompare(t, first.Headers["newsgroups"], second.Headers["newsgroups"], "Headers[newsgroups]")
	nntpHistTestCompare(t, first.Bytes, second.Bytes, "Bytes")
	nntpHistTestCompare(t, first.Lines, second.Lines, "Lines")
	nntpHistTestCompare(t, first.HeadersJSON, second.HeadersJSON, "HeadersJSON")
	nntpHistTestCompare(t, first.BodyText, second.BodyText, "BodyText")
}

func TestNNTPHistParseLegacyRoundTrip(t *testing.T) {
	head, body := nntpHistTestHeadLines(), nntpHistTestBodyLines()
	lines := nntpHistTestJoin(head, body, true)

	a, err := ParseLegacyArticleLines("<reply-2@example.org>", lines, true)
	if err != nil {
		t.Fatal(err)
	}
	// sanity checks on the first parse
	if a.Subject != "Re: a folded subject line" {
		t.Errorf("Subject = %q", a.Subject)
	}
	if want := []string{"<root-1@example.org>", "<child-1@example.org>"}; !reflect.DeepEqual(a.RefSlice, want) {
		t.Errorf("RefSlice = %#v, want %#v", a.RefSlice, want)
	}
	if a.Lines != len(body) {
		t.Errorf("Lines = %d, want %d", a.Lines, len(body))
	}
	if a.HeadersJSON != strings.Join(head, "\n") {
		t.Errorf("HeadersJSON does not preserve raw header lines: %q", a.HeadersJSON)
	}
	nntpHistTestRoundTrip(t, lines)
}

func TestNNTPHistParseLegacyRoundTripNoBody(t *testing.T) {
	lines := nntpHistTestJoin(nntpHistTestHeadLines(), nil, false)
	a, err := ParseLegacyArticleLines("<reply-2@example.org>", lines, true)
	if err != nil {
		t.Fatal(err)
	}
	if a.BodyText != "" || a.Lines != 0 || a.Bytes != 0 {
		t.Errorf("no-body article: BodyText=%q Lines=%d Bytes=%d", a.BodyText, a.Lines, a.Bytes)
	}
	nntpHistTestRoundTrip(t, lines)
}

func TestNNTPHistParseIncomingArticleLines(t *testing.T) {
	head := nntpHistTestHeadLines()
	body := nntpHistTestBodyLines() // already dot-unstuffed by readArticleLines

	article, newsgroups, err := parseIncomingArticleLines(head, body)
	if err != nil {
		t.Fatalf("parseIncomingArticleLines: %v", err)
	}
	if want := []string{"comp.lang.go", "alt.test"}; !reflect.DeepEqual(newsgroups, want) {
		t.Errorf("newsgroups = %#v, want %#v", newsgroups, want)
	}
	if article.MessageID != "<reply-2@example.org>" {
		t.Errorf("MessageID = %q", article.MessageID)
	}
	if article.HeadersJSON != strings.Join(head, "\n") {
		t.Errorf("HeadersJSON = %q", article.HeadersJSON)
	}
	if article.Subject != "Re: a folded subject line" {
		t.Errorf("Subject = %q", article.Subject)
	}
	if article.FromHeader != "Alice Example <alice@example.org>" {
		t.Errorf("FromHeader = %q", article.FromHeader)
	}
	if article.Path == "" || article.DateString == "" {
		t.Errorf("Path=%q DateString=%q", article.Path, article.DateString)
	}
	if want := []string{"<root-1@example.org>", "<child-1@example.org>"}; !reflect.DeepEqual(article.RefSlice, want) {
		t.Errorf("RefSlice = %#v, want %#v", article.RefSlice, want)
	}
	if article.BodyText != strings.Join(body, "\n") {
		t.Errorf("BodyText = %q", article.BodyText)
	}
	if !strings.Contains(article.BodyText, "\n.a line that was dot-stuffed") {
		t.Errorf("dot-unstuffed body line not preserved: %q", article.BodyText)
	}
	if article.Lines != len(body) {
		t.Errorf("Lines = %d, want %d", article.Lines, len(body))
	}
	if !reflect.DeepEqual(article.NNTPhead, head) || !reflect.DeepEqual(article.NNTPbody, body) {
		t.Errorf("NNTPhead/NNTPbody not preserved")
	}

	// no body
	article, newsgroups, err = parseIncomingArticleLines(head, nil)
	if err != nil {
		t.Fatalf("no body: %v", err)
	}
	if len(newsgroups) != 2 || article.BodyText != "" || article.MessageID == "" {
		t.Errorf("no body: newsgroups=%v BodyText=%q MessageID=%q", newsgroups, article.BodyText, article.MessageID)
	}
}

func TestNNTPHistParseIncomingArticleLinesErrors(t *testing.T) {
	head := []string{
		"From: bob@example.org",
		"Subject: no groups",
		"Newsgroups: , ,",
	}
	if _, _, err := parseIncomingArticleLines(head, []string{"body"}); err == nil {
		t.Error("expected error for empty Newsgroups header")
	}
	if _, _, err := parseIncomingArticleLines(nil, []string{"body"}); err == nil {
		t.Error("expected error for article without headers")
	}
}

func TestNNTPHistExtractHeaderValue(t *testing.T) {
	head := []string{
		"Subject: x",
		"message-id:",
		"   <folded@example.org>  ",
		"X-Other: y",
	}
	if got := extractHeaderValue(head, "Message-ID"); got != "<folded@example.org>" {
		t.Errorf("folded Message-ID = %q", got)
	}
	if got := extractHeaderValue([]string{"MESSAGE-ID:  <a@b>  "}, "message-id"); got != "<a@b>" {
		t.Errorf("Message-ID = %q", got)
	}
	if got := extractHeaderValue(head, "References"); got != "" {
		t.Errorf("missing header = %q", got)
	}
}

func TestNNTPHistLocal430StringKeys(t *testing.T) {
	lc := &Local430{cache: make(map[string]time.Time)}
	if lc.Check("<a@example.org>") {
		t.Fatal("empty cache reports hit")
	}
	if !lc.Add("<a@example.org>") {
		t.Fatal("Add returned false")
	}
	if !lc.Check("<a@example.org>") {
		t.Fatal("Check misses added id")
	}
	if lc.Check("<b@example.org>") {
		t.Fatal("Check hits id that was never added")
	}
	// distinct string values with equal content must hit the same entry
	id := strings.Join([]string{"<a", "example.org>"}, "@")
	if !lc.Check(id) {
		t.Fatal("Check must match by string value")
	}

	lc.Add("<old@example.org>")
	lc.mu.Lock()
	lc.cache["<old@example.org>"] = time.Now().Add(-2 * time.Minute)
	lc.mu.Unlock()
	lc.Cleanup()
	if lc.Check("<old@example.org>") {
		t.Error("Cleanup did not remove expired entry")
	}
	if !lc.Check("<a@example.org>") {
		t.Error("Cleanup removed fresh entry")
	}
}
