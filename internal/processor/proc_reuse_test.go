package processor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

const reuseTestMsgID = "<reuse-1@example.org>"

func reuseTestHeaders() string {
	return strings.Join([]string{
		"Path: news.example.org!not-for-mail",
		"From: Alice <alice@example.org>",
		"Newsgroups: comp.lang.go,alt.test",
		"Subject: a folded",
		"\tsubject line",
		"Date: Sat, 12 Sep 2026 10:11:12 +0000",
		"Message-ID: " + reuseTestMsgID,
	}, "\n")
}

func reuseTestNoCR(t *testing.T, lines []string) {
	t.Helper()
	for i, l := range lines {
		if strings.ContainsAny(l, "\r\n") {
			t.Errorf("line %d contains CR/LF: %q", i, l)
		}
	}
}

func TestReuseStoredArticleLines(t *testing.T) {
	body := "first\n\n.dotted\n-- \nsig"
	lines, ok := storedArticleLines(reuseTestHeaders(), body)
	if !ok {
		t.Fatalf("storedArticleLines failed")
	}
	reuseTestNoCR(t, lines)
	head := strings.Split(reuseTestHeaders(), "\n")
	want := append(append(append([]string{}, head...), ""), strings.Split(body, "\n")...)
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines mismatch:\n got: %#v\nwant: %#v", lines, want)
	}
	if lines[4] != "\tsubject line" {
		t.Errorf("folded header continuation lost: %q", lines[4])
	}
	art, err := nntp.ParseLegacyArticleLines(reuseTestMsgID, lines, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if art.BodyText != body {
		t.Errorf("BodyText = %q, want %q", art.BodyText, body)
	}
	if art.HeadersJSON != reuseTestHeaders() {
		t.Errorf("HeadersJSON mismatch:\n got: %q\nwant: %q", art.HeadersJSON, reuseTestHeaders())
	}
	if !strings.Contains(art.Subject, "subject line") {
		t.Errorf("folded Subject not parsed: %q", art.Subject)
	}
}

func TestReuseStoredArticleLinesEmptyBody(t *testing.T) {
	lines, ok := storedArticleLines(reuseTestHeaders(), "")
	if !ok {
		t.Fatalf("storedArticleLines failed")
	}
	head := strings.Split(reuseTestHeaders(), "\n")
	if len(lines) != len(head)+1 || lines[len(lines)-1] != "" {
		t.Fatalf("empty body: want headers + separator only, got %#v", lines)
	}
	art, err := nntp.ParseLegacyArticleLines(reuseTestMsgID, lines, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if art.BodyText != "" {
		t.Errorf("BodyText = %q, want empty", art.BodyText)
	}
}

func TestReuseStoredArticleLinesCRLF(t *testing.T) {
	crlfHead := strings.ReplaceAll(reuseTestHeaders(), "\n", "\r\n") + "\r\n"
	lines, ok := storedArticleLines(crlfHead, "a\r\nb\r")
	if !ok {
		t.Fatalf("storedArticleLines failed")
	}
	reuseTestNoCR(t, lines)
	want := append(append(strings.Split(reuseTestHeaders(), "\n"), ""), "a", "b")
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines mismatch:\n got: %#v\nwant: %#v", lines, want)
	}
}

func TestReuseStoredArticleLinesInvalid(t *testing.T) {
	for _, h := range []string{"", "  \n ", "Subject: x\n\nFrom: y"} {
		if _, ok := storedArticleLines(h, "body"); ok {
			t.Errorf("headers %q: want not ok", h)
		}
	}
}

func TestReuseStoredArticleDisabled(t *testing.T) {
	old := ReuseCrossposts
	defer func() { ReuseCrossposts = old }()

	var nilProc *Processor
	if art := nilProc.reuseStoredArticle(reuseTestMsgID, 1, "comp.lang.go"); art != nil {
		t.Errorf("nil processor: want nil article")
	}
	for _, enabled := range []bool{true, false} {
		ReuseCrossposts = enabled
		// nil history: disabled
		proc := &Processor{}
		if art := proc.reuseStoredArticle(reuseTestMsgID, 1, "comp.lang.go"); art != nil {
			t.Errorf("nil history (flag=%v): want nil article", enabled)
		}
		// non-nil but not enabled history
		proc.History = &history.History{}
		if art := proc.reuseStoredArticle(reuseTestMsgID, 1, "comp.lang.go"); art != nil {
			t.Errorf("disabled history (flag=%v): want nil article", enabled)
		}
	}
}

func TestReuseSeenBefore(t *testing.T) {
	ids := []int64{3, 5, 3}
	if reuseSeenBefore(ids[:0], 3) || reuseSeenBefore(ids[:1], 5) || !reuseSeenBefore(ids[:2], 3) {
		t.Errorf("reuseSeenBefore wrong")
	}
}
