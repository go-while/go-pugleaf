package processor

import (
	"reflect"
	"strings"
	"testing"
	"time"

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

// TestXrefLocationsDisabled: no history or a disabled one gives nil, nil (no lookups).
func TestXrefLocationsDisabled(t *testing.T) {
	var nilProc *Processor
	for name, proc := range map[string]*Processor{
		"nil processor":    nilProc,
		"nil history":      {},
		"disabled history": {History: &history.History{}},
	} {
		locs, err := proc.XrefLocations(reuseTestMsgID)
		if locs != nil || err != nil {
			t.Errorf("%s: got %v, %v; want nil, nil", name, locs, err)
		}
	}
}

// TestXrefResolve: the location filter of XrefLocations, with a fake name lookup and an
// explicit clock (no wall-clock waits): number 0 is skipped without a lookup, unknown IDs
// are skipped and looked up only once within xrefMissTTL, known groups give name+number.
func TestXrefResolve(t *testing.T) {
	names := map[int64]string{1: "comp.lang.go", 3: "alt.test"}
	queries := map[int64]int{}
	lookup := func(id int64) (string, bool) {
		queries[id]++
		n, ok := names[id]
		return n, ok
	}
	locs := []history.Location{
		{GroupID: 1, ArticleNum: 5},
		{GroupID: 2, ArticleNum: 0},  // number unknown: skipped, never looked up
		{GroupID: 99, ArticleNum: 4}, // group deleted from the main DB
		{GroupID: 3, ArticleNum: 7},
	}
	want := []nntp.XrefLocation{{Group: "comp.lang.go", ArticleNum: 5}, {Group: "alt.test", ArticleNum: 7}}
	proc := &Processor{}
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	for i, now := range []time.Time{t0, t0.Add(time.Minute), t0.Add(xrefMissTTL)} {
		if got := proc.xrefResolve(locs, lookup, now); !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d: got %v, want %v", i, got, want)
		}
	}
	if queries[2] != 0 {
		t.Errorf("number-0 location looked up %d times", queries[2])
	}
	if queries[99] != 1 {
		t.Errorf("unknown ID looked up %d times within the TTL, want 1", queries[99])
	}
	if queries[1] != 3 || queries[3] != 3 {
		t.Errorf("known IDs looked up %d/%d times, want 3 each (the ID cache caches them)", queries[1], queries[3])
	}
	// past the TTL the unknown ID is queried again (and remembered again)
	proc.xrefResolve(locs, lookup, t0.Add(xrefMissTTL+time.Second))
	if queries[99] != 2 {
		t.Errorf("unknown ID after the TTL: %d lookups, want 2", queries[99])
	}

	// bounded: the negative cache never holds more than xrefMissMax entries
	var many []history.Location
	for id := int64(1000); id < 1000+2*xrefMissMax; id++ {
		many = append(many, history.Location{GroupID: id, ArticleNum: 1})
	}
	proc.xrefResolve(many, lookup, t0)
	proc.xrefMiss.mu.Lock()
	n := len(proc.xrefMiss.m)
	proc.xrefMiss.mu.Unlock()
	if n == 0 || n > xrefMissMax {
		t.Errorf("negative cache holds %d entries, want 1..%d", n, xrefMissMax)
	}
}

func TestReuseSeenBefore(t *testing.T) {
	ids := []int64{3, 5, 3}
	if reuseSeenBefore(ids[:0], 3) || reuseSeenBefore(ids[:1], 5) || !reuseSeenBefore(ids[:2], 3) {
		t.Errorf("reuseSeenBefore wrong")
	}
}
