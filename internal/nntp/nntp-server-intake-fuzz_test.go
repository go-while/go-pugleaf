package nntp

// Fuzz test of the server's article parsing path (plan nntp-audit-tests, slice
// test-nntp-intake-e2e). POST, IHAVE and TAKETHIS all go through
// readArticleBlock (wire → dot-unstuffed head and body lines) and then
// parseIncomingArticleLines; this fuzzes the second step with the line shapes
// the first one can produce. Plain `go test` runs only the seed corpus.
//
// Identifiers use the prefix e2e / FuzzE2E (contract K2).

import (
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
)

// e2eFuzzHeadLines splits a fuzzed head into lines the way readArticleBlock
// would deliver them: no CRLF, and no empty line (an empty line ends the head).
func e2eFuzzHeadLines(head string) []string {
	var out []string
	for _, l := range strings.Split(head, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// e2eFuzzBodyLines splits a fuzzed body into lines (empty lines are kept, an
// empty input is an empty body).
func e2eFuzzBodyLines(body string) []string {
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

func FuzzE2EParseIncomingArticleLines(f *testing.F) {
	seeds := []struct{ head, body string }{
		{"Path: a!b\nFrom: x <x@test.invalid>\nNewsgroups: t6.fuzz.a\nSubject: s\nDate: Sat, 11 Oct 2026 10:00:00 +0000\nMessage-ID: <t6-fuzz-1@test.invalid>", "body\nline two"},
		{"Newsgroups: t6.fuzz.a, t6.fuzz.b,,\nMessage-ID: <t6-fuzz-2@test.invalid>", ""},
		{"newsgroups:t6.fuzz.a\nmessage-id:   <t6-fuzz-3@test.invalid>   ", "."},
		{"Message-ID:\n <t6-fuzz-4@test.invalid>\nNewsgroups: t6.fuzz.a", "..\n.\n"},
		{"Subject: first\nSubject: .\n.\nNewsgroups: t6.fuzz.a\nMessage-ID: <p@test.invalid>", "x"}, // A-nntp-rfc-2
		{"Newsgroups: \t\nMessage-ID: <t6-fuzz-5@test.invalid>", "no groups"},
		{"Xref: other.host t6.fuzz.a:1\nNewsgroups: t6.fuzz.a\nMessage-ID: <t6-fuzz-6@test.invalid>\nMessage-ID: <second@test.invalid>", "\n\n"},
		{"\tleading continuation\n: empty name\nNo colon at all\nNewsgroups: t6.fuzz.a", "\r\n\r"},
		{"", ""},
		{"Newsgroups: t6.fuzz.a\n References: <a@b>\n\tmore", strings.Repeat("y", 1000)},
		{"Newsgroups: a.b c.d\nMessage-ID: <t7-fuzz-1@test.invalid>", "space separated"},
		{"Newsgroups: ai..dots.\nMessage-ID: <t7-fuzz-2@test.invalid>", "empty components"},
	}
	for _, s := range seeds {
		f.Add(s.head, s.body)
	}
	// RFC 5536 §3.1.4 (D8): names are comma separated and every component is non-empty
	for _, head := range []string{"Newsgroups: a.b c.d", "Newsgroups: ai..dots."} {
		if _, groups, err := parseIncomingArticleLines(e2eFuzzHeadLines(head), nil); err == nil {
			f.Fatalf("%q parsed to groups %q, want an error", head, groups)
		}
	}
	f.Fuzz(func(t *testing.T, head, body string) {
		headLines := e2eFuzzHeadLines(head)
		bodyLines := e2eFuzzBodyLines(body)
		article, groups, err := parseIncomingArticleLines(headLines, bodyLines)
		if err != nil {
			if article != nil || groups != nil {
				t.Fatalf("error %v with a non-nil result", err)
			}
			return
		}
		if article == nil {
			t.Fatal("nil article without an error")
		}
		// the parsed Message-ID is exactly what extractHeaderValue finds
		if want := extractHeaderValue(headLines, "message-id"); article.MessageID != want {
			t.Fatalf("MessageID %q, want extractHeaderValue %q", article.MessageID, want)
		}
		if len(groups) == 0 {
			t.Fatal("no error but no newsgroups")
		}
		for _, g := range groups {
			if g == "" || g != strings.TrimSpace(g) || strings.Contains(g, ",") || !common.ValidGroupNameSyntax(g) {
				t.Fatalf("bad newsgroup %q in %q", g, groups)
			}
		}
		if len(article.NNTPhead) != len(headLines) || len(article.NNTPbody) != len(bodyLines) {
			t.Fatalf("NNTPhead/NNTPbody have %d/%d lines, want %d/%d",
				len(article.NNTPhead), len(article.NNTPbody), len(headLines), len(bodyLines))
		}
		if want := strings.Join(bodyLines, "\n"); article.BodyText != want {
			t.Fatalf("BodyText %q, want %q", article.BodyText, want)
		}
		if article.Lines != len(bodyLines) {
			t.Fatalf("Lines %d, want %d", article.Lines, len(bodyLines))
		}
	})
}
