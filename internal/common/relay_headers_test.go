package common

// Tests of the outbound header serializers (plan nntp-client-transfer, slice
// cli-commands): ReconstructHeaders (CLI-1 parts 1-2, NC-1) and RelayHeaders
// (A-intake-6 sender half, decisions 1 and 2). No package variable is mutated:
// the X-Goo* switch is passed to relayHeaders directly.

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

const (
	cliHdrHost  = "news.test.invalid"
	cliHdrMsgID = "<r1@test.invalid>"
	cliHdrDate  = "Sat, 12 Sep 2026 10:11:12 +0000"
)

// cliHdrArticle returns an article with valid columns and the given stored header block.
func cliHdrArticle(headersJSON, path string) *models.Article {
	return &models.Article{
		MessageID:   cliHdrMsgID,
		Subject:     "hello",
		FromHeader:  "A <a@b.invalid>",
		DateString:  cliHdrDate,
		Path:        path,
		HeadersJSON: headersJSON,
	}
}

// cliHdrLines joins lines with LF like the parser stores headers_json.
func cliHdrLines(lines ...string) string {
	return strings.Join(lines, "\n")
}

// cliHdrWithPrefix returns the elements of headers that start with prefix.
func cliHdrWithPrefix(headers []string, prefix string) []string {
	var out []string
	for _, h := range headers {
		if strings.HasPrefix(h, prefix) {
			out = append(out, h)
		}
	}
	return out
}

// CLI-1 part 1 / decision 1: one Path line, local identity once, no ".TX".
func TestCliHdrReconstructSinglePath(t *testing.T) {
	host := cliHdrHost
	for _, path := range []string{"news.test.invalid!poster!not-for-mail", "poster!not-for-mail"} {
		a := cliHdrArticle("Newsgroups: a.b", path)
		headers, err := ReconstructHeaders(a, true, &host, "")
		if err != nil {
			t.Fatalf("path %q: ReconstructHeaders: %v", path, err)
		}
		got := cliHdrWithPrefix(headers, "Path:")
		want := []string{"Path: news.test.invalid!poster!not-for-mail"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("path %q: Path lines = %q, want %q (all: %q)", path, got, want, headers)
		}
		for _, h := range headers {
			if strings.Contains(h, ".TX") {
				t.Errorf("path %q: element %q contains .TX", path, h)
			}
		}
	}
	// POST (withPath false): one X-Path line by the same rule, no Path line.
	a := cliHdrArticle("Newsgroups: a.b", "poster!not-for-mail")
	headers, err := ReconstructHeaders(a, false, &host, "")
	if err != nil {
		t.Fatalf("withPath=false: %v", err)
	}
	if got := cliHdrWithPrefix(headers, "Path:"); len(got) != 0 {
		t.Errorf("withPath=false: Path lines %q", got)
	}
	if got, want := cliHdrWithPrefix(headers, "X-Path:"), []string{"X-Path: news.test.invalid!poster!not-for-mail"}; !reflect.DeepEqual(got, want) {
		t.Errorf("withPath=false: X-Path lines = %q, want %q", got, want)
	}
}

// CLI-1 part 2: the folded Newsgroups continuation is emitted once.
func TestCliHdrReconstructFoldedNewsgroups(t *testing.T) {
	host := cliHdrHost
	a := cliHdrArticle(cliHdrLines("Newsgroups: a.b,", " c.d", "X-Custom: keep"), "poster!not-for-mail")
	headers, err := ReconstructHeaders(a, true, &host, "")
	if err != nil {
		t.Fatalf("ReconstructHeaders: %v", err)
	}
	joined := strings.Join(headers, "\n")
	if n := strings.Count(joined, " c.d"); n != 1 {
		t.Errorf("continuation \" c.d\" appears %d times, want 1: %q", n, headers)
	}
	if got := cliHdrWithPrefix(headers, "X-Custom:"); len(got) != 1 {
		t.Errorf("X-Custom lines = %q, want one", got)
	}
}

// NC-1: repeated Subject/From joined with LF only send their first line.
func TestCliHdrReconstructNoLF(t *testing.T) {
	host := cliHdrHost
	a := cliHdrArticle("Newsgroups: a.b", "poster!not-for-mail")
	a.Subject = "first\n."
	a.FromHeader = "a <a@b>\nb <b@c>"
	headers, err := ReconstructHeaders(a, true, &host, "")
	if err != nil {
		t.Fatalf("ReconstructHeaders: %v", err)
	}
	if got, want := cliHdrWithPrefix(headers, "Subject:"), []string{"Subject: first"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Subject lines = %q, want %q", got, want)
	}
	if got, want := cliHdrWithPrefix(headers, "From:"), []string{"From: a <a@b>"}; !reflect.DeepEqual(got, want) {
		t.Errorf("From lines = %q, want %q", got, want)
	}
	for _, h := range headers {
		if strings.ContainsAny(h, "\r\n") {
			t.Errorf("element %q contains CR or LF", h)
		}
	}
}

// A-intake-6: the stored block is relayed in order and byte-exact; only Path
// (replaced in place) and Xref (dropped with its continuation) change.
func TestCliHdrRelayVerbatim(t *testing.T) {
	stored := []string{
		"From: A <a@b.invalid>",
		"X-Custom:  keep  me ",
		"Newsgroups: a.b,",
		"\tc.d",
		"Path: up.invalid!poster!not-for-mail",
		"Subject: hello",
		"  world",
		"Xref: up.invalid a.b:1",
		" c.d:2",
		"Date: " + cliHdrDate,
		"Message-ID: " + cliHdrMsgID,
		"Organization: x",
	}
	a := cliHdrArticle(cliHdrLines(stored...), "up.invalid!poster!not-for-mail")
	got, err := relayHeaders(a, cliHdrHost, false)
	if err != nil {
		t.Fatalf("relayHeaders: %v", err)
	}
	want := []string{
		"From: A <a@b.invalid>",
		"X-Custom:  keep  me ",
		"Newsgroups: a.b,",
		"\tc.d",
		"Path: news.test.invalid!up.invalid!poster!not-for-mail",
		"Subject: hello",
		"  world",
		"Date: " + cliHdrDate,
		"Message-ID: " + cliHdrMsgID,
		"Organization: x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("relayed block:\n got %q\nwant %q", got, want)
	}

	// No Path header and no path column: one Path line inserted first.
	a = cliHdrArticle(cliHdrLines("From: A <a@b.invalid>", "Newsgroups: a.b", "Subject: s",
		"Date: "+cliHdrDate, "Message-ID: "+cliHdrMsgID), "")
	got, err = relayHeaders(a, cliHdrHost, false)
	if err != nil {
		t.Fatalf("relayHeaders without Path: %v", err)
	}
	if len(got) == 0 || got[0] != "Path: news.test.invalid!not-for-mail" || len(cliHdrWithPrefix(got, "Path:")) != 1 {
		t.Errorf("block without Path: %q", got)
	}

	// No local host, no Path header, no path column: "not-for-mail", never an empty Path.
	a = cliHdrArticle(cliHdrLines("From: A <a@b.invalid>", "Newsgroups: a.b", "Subject: s",
		"Date: "+cliHdrDate, "Message-ID: "+cliHdrMsgID), "")
	got, err = relayHeaders(a, "", false)
	if err != nil {
		t.Fatalf("relayHeaders without host and path: %v", err)
	}
	if len(got) == 0 || got[0] != "Path: not-for-mail" {
		t.Errorf("block without host and path: %q", got)
	}

	// A stored path that already starts with the local identity is sent unchanged.
	a = cliHdrArticle(cliHdrLines("Path: news.test.invalid!.POSTED!poster!not-for-mail", "From: A <a@b.invalid>",
		"Newsgroups: a.b", "Subject: s", "Date: "+cliHdrDate, "Message-ID: "+cliHdrMsgID),
		"news.test.invalid!.POSTED!poster!not-for-mail")
	got, err = relayHeaders(a, cliHdrHost, false)
	if err != nil {
		t.Fatalf("relayHeaders with local path: %v", err)
	}
	if got[0] != "Path: news.test.invalid!.POSTED!poster!not-for-mail" {
		t.Errorf("local path changed: %q", got[0])
	}
}

// X-Goo* dropping follows the argument; a non-RFC Date is repaired by
// outboundDate, an RFC Date is kept verbatim.
func TestCliHdrRelayGoogleAndDate(t *testing.T) {
	block := func(date string) string {
		return cliHdrLines("From: A <a@b.invalid>", "Newsgroups: a.b", "Subject: s", "X-Google-Foo: bar",
			"Date: "+date, "Message-ID: "+cliHdrMsgID)
	}
	a := cliHdrArticle(block(cliHdrDate), "p!not-for-mail")
	got, err := relayHeaders(a, cliHdrHost, true)
	if err != nil {
		t.Fatalf("relayHeaders(ignoreGoogle=true): %v", err)
	}
	if g := cliHdrWithPrefix(got, "X-Google-Foo:"); len(g) != 0 {
		t.Errorf("ignoreGoogle=true kept %q", g)
	}
	got, err = relayHeaders(a, cliHdrHost, false)
	if err != nil {
		t.Fatalf("relayHeaders(ignoreGoogle=false): %v", err)
	}
	if g := cliHdrWithPrefix(got, "X-Google-Foo:"); len(g) != 1 {
		t.Errorf("ignoreGoogle=false: X-Google-Foo lines %q, want one", g)
	}

	// Non-RFC stored Date with a valid DateSent: replaced by outboundDate's value.
	sent := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	a = cliHdrArticle(block("Tuesday 7pm"), "p!not-for-mail")
	a.DateString = "Tuesday 7pm"
	a.DateSent = sent
	ref := cliHdrArticle(block("Tuesday 7pm"), "p!not-for-mail")
	ref.DateString = "Tuesday 7pm"
	ref.DateSent = sent
	wantDate, err := outboundDate(ref)
	if err != nil {
		t.Fatalf("outboundDate: %v", err)
	}
	got, err = relayHeaders(a, cliHdrHost, false)
	if err != nil {
		t.Fatalf("relayHeaders(non-RFC date): %v", err)
	}
	if g, w := cliHdrWithPrefix(got, "Date:"), []string{"Date: " + wantDate}; !reflect.DeepEqual(g, w) {
		t.Errorf("non-RFC Date: got %q, want %q", g, w)
	}

	// RFC stored Date: kept verbatim even when the column differs.
	a = cliHdrArticle(block(cliHdrDate), "p!not-for-mail")
	a.DateString = "Mon, 01 Jan 2024 00:00:00 +0000"
	got, err = relayHeaders(a, cliHdrHost, false)
	if err != nil {
		t.Fatalf("relayHeaders(RFC date): %v", err)
	}
	if g, w := cliHdrWithPrefix(got, "Date:"), []string{"Date: " + cliHdrDate}; !reflect.DeepEqual(g, w) {
		t.Errorf("RFC Date: got %q, want %q", g, w)
	}

	// RFC 5322 dates outside isRFCdate's layouts are relayed byte-exact, also
	// when the columns could not repair them (zero DateSent, no Date-Received).
	for _, date := range []string{
		"Thu, 01 Jan 2026 00:00:00 +0000 (UTC)",
		"01 Jan 2026 00:00:00 GMT",
		"1 Jan 2026 00:00:00 +0000",
		"Thu, 01 Jan 2026 00:00 +0000",
	} {
		for _, zeroSent := range []bool{false, true} {
			a = cliHdrArticle(block(date), "p!not-for-mail")
			a.DateString = date
			if !zeroSent {
				a.DateSent = sent
			}
			got, err = relayHeaders(a, cliHdrHost, false)
			if err != nil {
				t.Errorf("relayHeaders(%q, zeroSent=%v): %v", date, zeroSent, err)
				continue
			}
			if g, w := cliHdrWithPrefix(got, "Date:"), []string{"Date: " + date}; !reflect.DeepEqual(g, w) {
				t.Errorf("Date %q (zeroSent=%v): got %q, want %q", date, zeroSent, g, w)
			}
		}
	}
}

// Unsafe blocks wrap ErrRelayHeadersInvalid, also when they are incomplete too.
func TestCliHdrRelayInvalid(t *testing.T) {
	base := []string{"Path: p!not-for-mail", "From: A <a@b.invalid>", "Newsgroups: a.b", "Subject: s",
		"Date: " + cliHdrDate, "Message-ID: " + cliHdrMsgID}
	with := func(extra ...string) string { return cliHdrLines(append(append([]string{}, base...), extra...)...) }
	cases := []struct {
		name, block, msgID string
	}{
		{"two Subject lines", with("Subject: again"), cliHdrMsgID},
		{"two Path lines", with("path: q!not-for-mail"), cliHdrMsgID},
		{"Message-ID differs", with(), "<other@test.invalid>"},
		{"invalid Message-ID", cliHdrLines("From: A <a@b.invalid>", "Newsgroups: a.b", "Subject: s",
			"Date: "+cliHdrDate, "Message-ID: <no-at-sign>"), "<no-at-sign>"},
		{"bare CR", with("X-Bad: a\rb"), cliHdrMsgID},
		{"NUL", with("X-Bad: a\x00b"), cliHdrMsgID},
		{"duplicate and incomplete", cliHdrLines("Subject: s", "Subject: t", "From: A <a@b.invalid>"), cliHdrMsgID},
	}
	for _, tc := range cases {
		a := cliHdrArticle(tc.block, "p!not-for-mail")
		a.MessageID = tc.msgID
		_, err := relayHeaders(a, cliHdrHost, false)
		if !errors.Is(err, ErrRelayHeadersInvalid) {
			t.Errorf("%s: err = %v, want ErrRelayHeadersInvalid", tc.name, err)
		}
		if errors.Is(err, ErrRelayHeadersIncomplete) {
			t.Errorf("%s: err %v also wraps ErrRelayHeadersIncomplete", tc.name, err)
		}
	}
}

// Incomplete or malformed blocks wrap ErrRelayHeadersIncomplete (the caller
// falls back to ReconstructHeaders).
func TestCliHdrRelayIncomplete(t *testing.T) {
	full := []string{"From: A <a@b.invalid>", "Newsgroups: a.b", "Subject: s", "Date: " + cliHdrDate,
		"Message-ID: " + cliHdrMsgID}
	cases := []struct {
		name  string
		block string
	}{
		{"web post", cliHdrLines("MIME-Version: 1.0", "Content-Type: text/plain; charset=UTF-8",
			"From: A <a@b.invalid>", "Newsgroups: a.b")},
		{"missing Date", cliHdrLines(full[0], full[1], full[2], full[4])},
		{"colon-less line", cliHdrLines(append([]string{"Bogus line"}, full...)...)},
		{"first line continuation", cliHdrLines(append([]string{" continued"}, full...)...)},
		{"space in field name", cliHdrLines(append([]string{"X Bad: v"}, full...)...)},
	}
	for _, tc := range cases {
		a := cliHdrArticle(tc.block, "p!not-for-mail")
		_, err := relayHeaders(a, cliHdrHost, false)
		if !errors.Is(err, ErrRelayHeadersIncomplete) {
			t.Errorf("%s: err = %v, want ErrRelayHeadersIncomplete", tc.name, err)
		}
	}
	// The complete block relays.
	if _, err := relayHeaders(cliHdrArticle(cliHdrLines(full...), "p!not-for-mail"), cliHdrHost, false); err != nil {
		t.Errorf("complete block: %v", err)
	}
}

func TestCliHdrEnsureLocalPathPrefix(t *testing.T) {
	cases := []struct{ path, host, want string }{
		{"a!b", "", "a!b"},
		{"", "h.example", "h.example!not-for-mail"},
		{"h.example", "h.example", "h.example"},
		{"h.example!x!not-for-mail", "h.example", "h.example!x!not-for-mail"},
		{"h.examplex!y", "h.example", "h.example!h.examplex!y"},
		{"x!h.example!y", "h.example", "h.example!x!h.example!y"},
		{"example.org!.POSTED!poster!not-for-mail", "example.org", "example.org!.POSTED!poster!not-for-mail"},
	}
	for _, tc := range cases {
		if got := EnsureLocalPathPrefix(tc.path, tc.host); got != tc.want {
			t.Errorf("EnsureLocalPathPrefix(%q, %q) = %q, want %q", tc.path, tc.host, got, tc.want)
		}
	}
}
