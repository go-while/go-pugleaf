package common

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// icTestMsgID returns a message-id of exactly n octets: "<" + a... + "@x>".
func icTestMsgID(n int) string {
	return "<" + strings.Repeat("a", n-4) + "@x>"
}

// icTestIsRejected reports whether err wraps ErrIntakeRejected with the given reason.
func icTestIsRejected(err error, reason string) bool {
	return errors.Is(err, ErrIntakeRejected) && strings.HasSuffix(err.Error(), ": "+reason)
}

// icTestFetchUnique are the header names fetch mode must never return twice.
var icTestFetchUnique = []string{"date", "from", "message-id", "newsgroups", "path", "subject"}

// icTestDupFree reports whether lines contain at most one field line of each name in names.
func icTestDupFree(lines []string, names []string) bool {
	seen := make(map[string]int)
	for _, l := range lines {
		if isContinuationLine(l) {
			continue
		}
		if c := strings.IndexByte(l, ':'); c > 0 {
			seen[strings.ToLower(l[:c])]++
		}
	}
	for _, n := range names {
		if seen[n] > 1 {
			return false
		}
	}
	return true
}

// icTestIsSubsequence reports whether sub is a subsequence of full.
func icTestIsSubsequence(sub, full []string) bool {
	j := 0
	for _, l := range full {
		if j < len(sub) && sub[j] == l {
			j++
		}
	}
	return j == len(sub)
}

func TestIntakeCommonValidMessageID(t *testing.T) {
	if len(icTestMsgID(250)) != 250 || len(icTestMsgID(251)) != 251 {
		t.Fatal("icTestMsgID builds the wrong length")
	}
	cases := []struct {
		id   string
		want bool
	}{
		{"<abc@example.org>", true},
		{"<a.b.c$123@news.example.org>", true},
		{"<k5q3abcdefghijklmnopqrstuv@host>", true},
		{"<x@[127.0.0.1]>", true},
		{"<x@[a@b]>", true},
		{"<a..b@example.org>", true}, // left part only needs printable octets
		{icTestMsgID(250), true},
		{icTestMsgID(251), false},
		{"abc@example.org", false},  // no brackets
		{"<abc@example.org", false}, // no closing bracket
		{"abc@example.org>", false}, // no opening bracket
		{"<a@b@example.org>", false},
		{"<abc@exa mple.org>", false},
		{"<abc\t@example.org>", false},
		{"<abcexample.org>", false},
		{"<@example.org>", false},
		{"<abc@>", false},
		{"<>", false},
		{"", false},
		{"<a<b@example.org>", false},
		{"<a>b@example.org>", false},
		{"<abc@.example.org>", false},
		{"<abc@example.org.>", false},
		{"<abc@example..org>", false},
		{"<abc@exa(mple).org>", false},
		{"<x@[a[b]>", false},
		{"<x@[a\\b]>", false},
		{"<x@[abc>", false},
		{"<a@[a\\]b]>", true},
		{"<a@[a\\x]>", false},
		{"<a@[a\\\\]>", true},
		{"<a@[a\\[b]>", true},
		{"<a@[a\\]>", false},
		{"<a@[\\]>", false},
		{"<abc@ex\x7fample.org>", false},
		{"<abc@ex\x80ample.org>", false},
	}
	for _, c := range cases {
		if got := ValidMessageID(c.id); got != c.want {
			t.Errorf("ValidMessageID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestIntakeCommonGroupNames(t *testing.T) {
	name255 := strings.Repeat("a", 127) + "." + strings.Repeat("b", 127)
	if len(name255) != 255 {
		t.Fatalf("name255 has %d octets", len(name255))
	}
	syntax := []struct {
		name string
		want bool
	}{
		{"comp.lang.go", true},
		{"Comp.Lang.Go", true},
		{"c++", true},
		{"alt.binaries.x_y-z+1", true},
		{"junk", true},
		{name255, true},
		{name255 + "c", false},
		{"ai..dots.", false},
		{"a..b", false},
		{".a", false},
		{"a.", false},
		{"a b", false},
		{"&x", false},
		{"a,b", false},
		{"a;b", false},
		{"a:b", false},
		{"", false},
		{"grp\xc3\xa4", false},
	}
	for _, c := range syntax {
		if got := ValidGroupNameSyntax(c.name); got != c.want {
			t.Errorf("ValidGroupNameSyntax(%q) = %v, want %v", c.name, got, c.want)
		}
	}
	reserved := []struct {
		name string
		want bool
	}{
		{"comp.lang.go", false},
		{"junk", true},
		{"JUNK", true},
		{"junk.foo", false},
		{"poster", true},
		{"control", true},
		{"control.cancel", true},
		{"example.foo", true},
		{"Example.Foo", true},
		{"to.bob", true},
		{"alt.all.x", true},
		{"all", true},
		{"alt.ctl", true},
		{"alt.toast", false},
		{"alt.example", false},
		{"alt.control", false},
		{"alt.allx", false},
	}
	for _, c := range reserved {
		if got := IsReservedGroupName(c.name); got != c.want {
			t.Errorf("IsReservedGroupName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIntakeCommonParseNewsgroups(t *testing.T) {
	cases := []struct {
		value   string
		want    []string
		wantErr bool
	}{
		{"a.b, c.d", []string{"a.b", "c.d"}, false},
		{"a.b,c.d", []string{"a.b", "c.d"}, false},
		{" \ta.b\t,\tc.d ", []string{"a.b", "c.d"}, false},
		{"a.b,,", []string{"a.b"}, false},
		{",a.b,,c.d,", []string{"a.b", "c.d"}, false},
		{"a.b,c.d,a.b", []string{"a.b", "c.d"}, false},
		{"A.b,a.b", []string{"A.b", "a.b"}, false},
		{"a.b c.d", nil, true},
		{"a.b;c.d", nil, true},
		{"a.b:c.d", nil, true},
		{"a.b,ai..dots.", nil, true},
		{"", nil, true},
		{" , ,", nil, true},
	}
	for _, c := range cases {
		got, err := ParseNewsgroups(c.value)
		if c.wantErr {
			if !icTestIsRejected(err, "invalid newsgroups") {
				t.Errorf("ParseNewsgroups(%q) err = %v, want ErrIntakeRejected invalid newsgroups", c.value, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseNewsgroups(%q) = %q, %v; want %q", c.value, got, err, c.want)
		}
	}
}

func TestIntakeCommonCheckHeaderBlock(t *testing.T) {
	base := []string{
		"Path: a!b",
		"From: x@example.org",
		"Newsgroups: a.b",
		"Subject: test",
		"Date: Sun, 11 Oct 2026 10:00:00 +0000",
		"Message-ID: <t@example.org>",
	}
	with := func(extra ...string) []string {
		out := append([]string(nil), base...)
		return append(out, extra...)
	}
	auditRepro := []string{
		"From: a@example.org",
		"From: b@example.org",
		"Newsgroups: a.b",
		"Newsgroups: c.d",
		"Subject: one",
		"Subject: two",
		"Message-ID: <r1@example.org>",
		"Message-ID: <r2@example.org>",
		".",
		"no colon here",
	}
	isoSubject := "Subject: \x1b$B%F%9%H\x1b(B"
	cases := []struct {
		name      string
		mode      HeaderBlockMode
		in        []string
		want      []string // nil with wantErr false: same as in
		wantErr   bool
		wantSame  bool // output must be the input slice itself
		wantFresh bool // output must not share the input backing array
	}{
		{name: "strict-valid", mode: HeaderBlockStrict, in: base, wantSame: true},
		{name: "fetch-valid", mode: HeaderBlockFetch, in: base, wantSame: true},
		{name: "strict-folded", mode: HeaderBlockStrict, in: with("References: <a@b>", "\t<c@d>", " <e@f>"), wantSame: true},
		{name: "empty", mode: HeaderBlockStrict, in: []string{}, wantSame: true},
		// rule 1
		{name: "strict-nul", mode: HeaderBlockStrict, in: with("X-A: a\x00b"), wantErr: true},
		{name: "fetch-nul", mode: HeaderBlockFetch, in: with("X-A: a\x00b"), wantErr: true},
		{name: "strict-cr", mode: HeaderBlockStrict, in: with("X-A: a\rb"), wantErr: true},
		{name: "fetch-cr", mode: HeaderBlockFetch, in: with("X-A: a\rb"), wantErr: true},
		{name: "strict-lf", mode: HeaderBlockStrict, in: with("X-A: a\nb"), wantErr: true},
		{name: "fetch-lf", mode: HeaderBlockFetch, in: with("X-A: a\nb"), wantErr: true},
		{name: "fetch-lf-in-dropped-line", mode: HeaderBlockFetch, in: with("no colon\n"), wantErr: true},
		{name: "strict-esc", mode: HeaderBlockStrict, in: with("X-A: a\x1bb"), wantErr: true},
		{name: "strict-iso2022jp", mode: HeaderBlockStrict, in: []string{isoSubject}, wantErr: true},
		{name: "fetch-iso2022jp", mode: HeaderBlockFetch, in: []string{"From: x@example.org", isoSubject}, wantSame: true},
		{name: "strict-tab-in-value", mode: HeaderBlockStrict, in: with("X-A: a\tb"), wantSame: true},
		{name: "strict-del-passes", mode: HeaderBlockStrict, in: with("X-A: a\x7fb"), wantSame: true},
		// rule 2
		{name: "strict-leading-continuation", mode: HeaderBlockStrict, in: append([]string{" lead"}, base...), wantErr: true},
		{name: "fetch-leading-continuation", mode: HeaderBlockFetch, in: append([]string{" lead", "\tlead2"}, base...), want: base, wantFresh: true},
		{name: "strict-wsp-only-continuation", mode: HeaderBlockStrict, in: with("X-A: a", "  ", " more"), wantErr: true},
		{name: "strict-tab-only-continuation", mode: HeaderBlockStrict, in: with("X-A: a", "\t"), wantErr: true},
		{name: "fetch-wsp-only-continuation", mode: HeaderBlockFetch,
			in:   []string{"Subject: x", "  ", " more", "\t \t", "From: f"},
			want: []string{"Subject: x", " more", "From: f"}, wantFresh: true},
		// rule 3
		{name: "strict-colonless", mode: HeaderBlockStrict, in: with("no colon here"), wantErr: true},
		{name: "fetch-colonless", mode: HeaderBlockFetch, in: with("no colon here", " cont of dropped"), want: base, wantFresh: true},
		{name: "strict-dot-line", mode: HeaderBlockStrict, in: with("."), wantErr: true},
		{name: "fetch-dot-line", mode: HeaderBlockFetch, in: with("."), want: base, wantFresh: true},
		{name: "strict-empty-name", mode: HeaderBlockStrict, in: with(": value"), wantErr: true},
		{name: "fetch-empty-name", mode: HeaderBlockFetch, in: with(": value"), want: base, wantFresh: true},
		{name: "strict-space-in-name", mode: HeaderBlockStrict, in: with("Bad Name: v"), wantErr: true},
		{name: "fetch-space-before-colon", mode: HeaderBlockFetch, in: with("Subject2 : v"), want: base, wantFresh: true},
		{name: "strict-empty-line", mode: HeaderBlockStrict, in: with(""), wantErr: true},
		{name: "fetch-empty-line", mode: HeaderBlockFetch, in: with(""), want: base, wantFresh: true},
		{name: "fetch-drop-middle", mode: HeaderBlockFetch,
			in:   []string{"From: x", "junk line", "\tjunk cont", "Subject: s", " folded", "..", "Date: d"},
			want: []string{"From: x", "Subject: s", " folded", "Date: d"}, wantFresh: true},
		// rule 4
		{name: "strict-dup-subject", mode: HeaderBlockStrict, in: with("subject: again"), wantErr: true},
		{name: "fetch-dup-subject", mode: HeaderBlockFetch, in: with("SUBJECT: again"), wantErr: true},
		{name: "fetch-dup-path", mode: HeaderBlockFetch, in: with("Path: c!d"), wantErr: true},
		{name: "strict-dup-references", mode: HeaderBlockStrict, in: with("References: <a@b>", "References: <c@d>"), wantErr: true},
		{name: "fetch-dup-references", mode: HeaderBlockFetch, in: with("References: <a@b>", "References: <c@d>"), wantSame: true},
		{name: "strict-dup-xref", mode: HeaderBlockStrict, in: with("Xref: h a:1", "Xref: h b:2"), wantErr: true},
		{name: "strict-dup-approved", mode: HeaderBlockStrict, in: with("Approved: a", "approved: b"), wantErr: true},
		{name: "strict-dup-control", mode: HeaderBlockStrict, in: with("Control: cancel <a@b>", "Control: cancel <c@d>"), wantErr: true},
		{name: "strict-dup-supersedes", mode: HeaderBlockStrict, in: with("Supersedes: <a@b>", "Supersedes: <c@d>"), wantErr: true},
		{name: "strict-dup-injection", mode: HeaderBlockStrict, in: with("Injection-Info: h", "Injection-Info: i"), wantErr: true},
		{name: "strict-dup-injection-date", mode: HeaderBlockStrict, in: with("Injection-Date: d", "Injection-Date: e"), wantErr: true},
		{name: "strict-dup-organization", mode: HeaderBlockStrict, in: with("Organization: a", "Organization: b"), wantSame: true},
		{name: "fetch-dup-user-agent", mode: HeaderBlockFetch, in: with("User-Agent: a", "User-Agent: b"), wantSame: true},
		{name: "strict-audit-repro", mode: HeaderBlockStrict, in: auditRepro, wantErr: true},
		{name: "fetch-audit-repro", mode: HeaderBlockFetch, in: auditRepro, wantErr: true},
		// rule 5
		{name: "strict-998", mode: HeaderBlockStrict, in: with("X-Long: " + strings.Repeat("a", 990)), wantSame: true},
		{name: "strict-999", mode: HeaderBlockStrict, in: with("X-Long: " + strings.Repeat("a", 991)), wantErr: true},
		{name: "fetch-999", mode: HeaderBlockFetch, in: with("X-Long: " + strings.Repeat("a", 991)), wantSame: true},
		// unknown mode values behave like strict
		{name: "unknown-mode-strict", mode: HeaderBlockMode(42), in: with("no colon"), wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snapshot := append([]string(nil), c.in...)
			got, err := CheckHeaderBlock(c.in, c.mode)
			if !slices.Equal(c.in, snapshot) {
				t.Fatalf("input modified: %q", c.in)
			}
			if c.wantErr {
				if !icTestIsRejected(err, "invalid header block") {
					t.Fatalf("err = %v, want ErrIntakeRejected invalid header block", err)
				}
				if got != nil {
					t.Fatalf("got %q with error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			want := c.want
			if want == nil {
				want = c.in
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %q, want %q", got, want)
			}
			if c.wantSame && len(c.in) > 0 && &got[0] != &c.in[0] {
				t.Fatal("expected the input slice back unchanged")
			}
			if c.wantFresh && len(got) > 0 && &got[0] == &c.in[0] {
				t.Fatal("expected a new slice after dropping lines")
			}
		})
	}
}

func TestIntakeCommonHeaderValue(t *testing.T) {
	lines := []string{
		"Path: a!b",
		"Subject: first",
		"  part two ",
		"\t",
		"\tpart three",
		"subject: second",
		"X-Empty:",
		"  only continuation ",
		"From : spaced@example.org",
		" Subject: continuation not a field",
		"Organization: org",
	}
	if got := HeaderValue(lines, "SUBJECT"); got != "first part two part three" {
		t.Errorf("HeaderValue Subject = %q", got)
	}
	if got := HeaderValue(lines, "x-empty"); got != "only continuation" {
		t.Errorf("HeaderValue X-Empty = %q", got)
	}
	if got := HeaderValue(lines, "From"); got != "spaced@example.org Subject: continuation not a field" {
		t.Errorf("HeaderValue From = %q", got)
	}
	if got := HeaderValue(lines, "Newsgroups"); got != "" {
		t.Errorf("HeaderValue Newsgroups = %q", got)
	}
	if got := HeaderValue(nil, "Subject"); got != "" {
		t.Errorf("HeaderValue(nil) = %q", got)
	}
	if got := HeaderValues(lines, "subject"); !reflect.DeepEqual(got, []string{"first part two part three", "second"}) {
		t.Errorf("HeaderValues Subject = %q", got)
	}
	if got := HeaderValues(lines, "Xref"); got != nil {
		t.Errorf("HeaderValues Xref = %q", got)
	}
}

func TestIntakeCommonReplaceRemoveHeaders(t *testing.T) {
	lines := []string{
		"PATH: old!path",
		" continued!path",
		"Xref: host a.b:1",
		"\tc.d:2",
		"Subject: s",
		"xref: host e.f:3",
		"From: f",
	}
	snapshot := append([]string(nil), lines...)

	got := ReplaceHeader(lines, "Path", "me!.POSTED!old")
	want := []string{"PATH: me!.POSTED!old", "Xref: host a.b:1", "\tc.d:2", "Subject: s", "xref: host e.f:3", "From: f"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReplaceHeader existing = %q, want %q", got, want)
	}
	got = ReplaceHeader(lines, "Message-ID", "<a@b>")
	if len(got) != len(lines)+1 || got[0] != "Message-ID: <a@b>" || !reflect.DeepEqual(got[1:], lines) {
		t.Errorf("ReplaceHeader absent = %q", got)
	}
	got = ReplaceHeader([]string{"From: f", "Subject: last", " fold"}, "subject", "new")
	if !reflect.DeepEqual(got, []string{"From: f", "Subject: new"}) {
		t.Errorf("ReplaceHeader last field = %q", got)
	}
	got = ReplaceHeader(nil, "Path", "x")
	if !reflect.DeepEqual(got, []string{"Path: x"}) {
		t.Errorf("ReplaceHeader(nil) = %q", got)
	}

	got = RemoveHeaders(lines, "XREF")
	want = []string{"PATH: old!path", " continued!path", "Subject: s", "From: f"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RemoveHeaders = %q, want %q", got, want)
	}
	got = RemoveHeaders(lines, "Organization")
	if len(got) != len(lines) || &got[0] != &lines[0] {
		t.Errorf("RemoveHeaders absent should return the input, got %q", got)
	}
	got = RemoveHeaders([]string{"Xref: a", " b"}, "xref")
	if len(got) != 0 {
		t.Errorf("RemoveHeaders all = %q", got)
	}
	if !slices.Equal(lines, snapshot) {
		t.Fatalf("input modified: %q", lines)
	}
}

func TestIntakeCommonWireArticleSize(t *testing.T) {
	if got := WireArticleSize(nil, nil); got != 2 {
		t.Errorf("WireArticleSize(nil, nil) = %d, want 2", got)
	}
	if got := WireArticleSize([]string{}, []string{}); got != 2 {
		t.Errorf("WireArticleSize([], []) = %d, want 2", got)
	}
	if got := WireArticleSize([]string{"A: b"}, []string{"x"}); got != 6+2+3 {
		t.Errorf("WireArticleSize = %d, want 11", got)
	}
	// dot-stuffing is not counted: a body line "." counts as 1 octet + CRLF
	if got := WireArticleSize([]string{"A: b"}, []string{".", ""}); got != 6+2+3+2 {
		t.Errorf("WireArticleSize with dot line = %d, want 13", got)
	}
}

func TestIntakeCommonGenerateMessageID(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 50; i++ {
		id, err := GenerateMessageID("news.example.org")
		if err != nil {
			t.Fatal(err)
		}
		if !ValidMessageID(id) {
			t.Fatalf("generated id %q is not valid", id)
		}
		if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@news.example.org>") {
			t.Fatalf("generated id %q has the wrong shape", id)
		}
		left := strings.TrimPrefix(strings.TrimSuffix(id, "@news.example.org>"), "<")
		if len(left) != 26 {
			t.Fatalf("left part %q has %d chars, want 26", left, len(left))
		}
		for _, c := range left {
			if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
				t.Fatalf("left part %q has char %q outside the lower-case base32 alphabet", left, c)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate generated id %q", id)
		}
		seen[id] = struct{}{}
	}
	for _, bad := range []string{"", "bad host", "a@b", "x>y"} {
		if id, err := GenerateMessageID(bad); err == nil {
			t.Errorf("GenerateMessageID(%q) = %q, want error", bad, id)
		}
	}
}

func TestIntakeCommonFormatNewsDate(t *testing.T) {
	loc := time.FixedZone("X", 2*3600)
	tm := time.Date(2026, 10, 11, 12, 34, 56, 0, loc)
	if got := FormatNewsDate(tm); got != "Sun, 11 Oct 2026 10:34:56 +0000" {
		t.Errorf("FormatNewsDate = %q", got)
	}
}

func TestIntakeCommonErrors(t *testing.T) {
	if errors.Is(ErrIntakeRejected, ErrIntakeTemporary) || errors.Is(ErrIntakeTemporary, ErrIntakeRejected) {
		t.Fatal("ErrIntakeRejected and ErrIntakeTemporary must be distinct")
	}
	_, err := CheckHeaderBlock([]string{"."}, HeaderBlockStrict)
	if errors.Is(err, ErrIntakeTemporary) || err.Error() != "article rejected: invalid header block" {
		t.Fatalf("CheckHeaderBlock error = %v", err)
	}
}

func FuzzIntakeCommonCheckHeaderBlock(f *testing.F) {
	// lines are separated by CRLF so that a lone CR or LF can appear inside a line
	seeds := []string{
		"Path: a!b\r\nFrom: x@y\r\nNewsgroups: a.b\r\nSubject: s\r\nDate: d\r\nMessage-ID: <a@b>",
		"From: a\r\nFrom: b\r\nNewsgroups: a.b\r\nNewsgroups: c.d\r\nSubject: 1\r\nSubject: 2\r\n.\r\nno colon",
		".\r\n..\r\nSubject: s",
		" leading\r\nSubject: s\r\n\tfold",
		"Subject: \x1b$B%F%9%H\x1b(B",
		"X-A: a\x00b",
		"X-A: a\rb\r\nX-B: a\nb",
		"References: <a@b>\r\nReferences: <c@d>\r\nOrganization: o\r\nOrganization: p",
		"X-Long: " + strings.Repeat("a", 991),
		": empty\r\nBad Name: v\r\nok: v",
		"Subject: x\r\n  \r\n",
		"Subject: x\r\n \t\r\n folded\r\nFrom: f",
		"",
	}
	for _, s := range seeds {
		f.Add(s, true)
		f.Add(s, false)
	}
	f.Fuzz(func(t *testing.T, data string, strict bool) {
		lines := strings.Split(data, "\r\n")
		snapshot := append([]string(nil), lines...)
		mode := HeaderBlockFetch
		if strict {
			mode = HeaderBlockStrict
		}
		got, err := CheckHeaderBlock(lines, mode)
		if !slices.Equal(lines, snapshot) {
			t.Fatal("input modified")
		}
		if err != nil {
			if !errors.Is(err, ErrIntakeRejected) || got != nil {
				t.Fatalf("bad error result %q, %v", got, err)
			}
			return
		}
		if !icTestDupFree(got, icTestFetchUnique) {
			t.Fatalf("duplicate core header in %q", got)
		}
		for _, l := range got {
			if l != "" && strings.Trim(l, " \t") == "" {
				t.Fatalf("success with a whitespace-only line in %q", got)
			}
		}
		if strict {
			if !reflect.DeepEqual(got, lines) {
				t.Fatalf("strict mode changed the block: %q", got)
			}
			for _, l := range got {
				if l == "." {
					t.Fatal("strict success with a lone dot line")
				}
				for i := 0; i < len(l); i++ {
					if l[i] < 0x20 && l[i] != '\t' {
						t.Fatalf("strict success with control octet %#x", l[i])
					}
				}
			}
			return
		}
		for _, l := range got {
			if strings.ContainsAny(l, "\x00\r\n") {
				t.Fatalf("fetch success with NUL/CR/LF in %q", l)
			}
		}
		if !icTestIsSubsequence(got, lines) {
			t.Fatalf("fetch output %q is not a subsequence of %q", got, lines)
		}
		// the normalized block must pass fetch mode unchanged
		again, err := CheckHeaderBlock(got, HeaderBlockFetch)
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("fetch output not stable: %q, %v", again, err)
		}
	})
}

func FuzzIntakeCommonParseNewsgroups(f *testing.F) {
	for _, s := range []string{
		"a.b, c.d", "a.b,,", "a.b c.d", "a.b;c.d", "a.b,a.b", "ai..dots.", "", ",", "comp.lang.go,alt.test",
		"\ta.b\t", "c++,alt.x_y-z", strings.Repeat("a", 256),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		names, err := ParseNewsgroups(value)
		if err != nil {
			if !errors.Is(err, ErrIntakeRejected) || names != nil {
				t.Fatalf("bad error result %q, %v", names, err)
			}
			return
		}
		if len(names) == 0 {
			t.Fatal("success with an empty list")
		}
		seen := make(map[string]struct{})
		for _, n := range names {
			if !ValidGroupNameSyntax(n) || strings.Contains(n, ",") {
				t.Fatalf("invalid name %q returned", n)
			}
			if _, dup := seen[n]; dup {
				t.Fatalf("duplicate name %q returned", n)
			}
			seen[n] = struct{}{}
		}
	})
}
