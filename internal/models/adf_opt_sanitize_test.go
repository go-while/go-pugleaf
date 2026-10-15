package models

import (
	"encoding/base64"
	"html/template"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-while/go-pugleaf/internal/cache"
)

// adfOptLiveCache installs a fresh, enabled sanitized cache for one test and restores the globals.
func adfOptLiveCache(t *testing.T) *cache.SanitizedCache {
	t.Helper()
	sc := cache.NewSanitizedCache(100, time.Hour)
	prevCache, prevDisable := sanitizedCache, DisableSanitizedCache
	sanitizedCache, DisableSanitizedCache = sc, false
	t.Cleanup(func() {
		sanitizedCache, DisableSanitizedCache = prevCache, prevDisable
		sc.Stop()
	})
	return sc
}

// TestAdfOptSanitizeFromDisplay: From is decoded before it is cut and parsed with net/mail (A-web-6).
func TestAdfOptSanitizeFromDisplay(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{"=?UTF-8?Q?J=C3=BCrgen_M=C3=BCller?= <j@test.invalid>", "Jürgen Müller"},
		{"=?ISO-8859-1?Q?J=FCrgen?= <j@test.invalid>", "Jürgen"},
		{"=?ISO-8859-15?Q?J=FCrgen?= <j@test.invalid>", "Jürgen"},
		{"j@test.invalid (Jürgen Müller)", "Jürgen Müller"},
		{"\"Jürgen Müller\" <j@test.invalid>", "Jürgen Müller"},
		{"J\xfcrgen <j@test.invalid>", "Jürgen"},
		{"Alice Example <alice@test.invalid>", "Alice Example"},
		{"j@test.invalid", "j"},
		{"<j@test.invalid>", "j"},
		{"\"foo@bar.invalid\" <foo@bar.invalid>", "foo"},
		{"foo@bar.invalid <foo@bar.invalid>", "foo"},
		{"Müller, Jürgen <j@test.invalid>", "Müller Jürgen"},
		{"", "Anon"},
		{"=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("ü", 40))) + "?= <x@test.invalid>",
			strings.Repeat("ü", 32) + "..."},
	}
	for _, c := range cases {
		got := displayFrom(c.raw)
		if got != c.want {
			t.Errorf("displayFrom(%q)=%q, want %q", c.raw, got, c.want)
		}
		if strings.Contains(got, "@") || strings.Contains(got, "test.invalid") || strings.Contains(got, "bar.invalid") {
			t.Errorf("displayFrom(%q)=%q shows an address", c.raw, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("displayFrom(%q)=%q is not valid UTF-8", c.raw, got)
		}
		// The rendered value is the same, escaped.
		a := &Article{FromHeader: c.raw}
		if r := string(a.PrintSanitized("from")); r != template.HTMLEscapeString(got) {
			t.Errorf("PrintSanitized(from) for %q=%q, want %q", c.raw, r, got)
		}
	}
}

// TestAdfOptSanitizeSubjectRunes: subjects are decoded first and cut by runes (A-web-7).
func TestAdfOptSanitizeSubjectRunes(t *testing.T) {
	long := "Grüße " + strings.Repeat("x", 127) // 135 bytes, 133 runes
	got := displaySubject(long)
	if !utf8.ValidString(got) || strings.Contains(got, "Ã") {
		t.Fatalf("displaySubject(long)=%q: invalid or re-decoded as Latin-1", got)
	}
	if want := "Grüße " + strings.Repeat("x", 122) + "..."; got != want {
		t.Fatalf("displaySubject(long)=%q, want %q", got, want)
	}

	plain := strings.Repeat("ABC", 40) // 120 bytes -> 172-byte encoded word
	encoded := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(plain)) + "?="
	if len(encoded) != 172 {
		t.Fatalf("encoded subject is %d bytes", len(encoded))
	}
	o := &Overview{Subject: encoded}
	for _, s := range []string{string(o.PrintSanitized("subject")), o.GetCleanSubject()} {
		if s != plain {
			t.Errorf("encoded subject rendered %q, want the decoded text", s)
		}
	}
	a := &Article{Subject: "=?UTF-8?Q?Gr=C3=BC=C3=9Fe?= " + strings.Repeat("y", 200)}
	if s := a.GetCleanSubject(); !strings.HasPrefix(s, "Grüße y") || utf8.RuneCountInString(s) != 128+3 {
		t.Errorf("GetCleanSubject=%q (runes %d)", s, utf8.RuneCountInString(s))
	}
}

// TestAdfOptSanitizeBodyDecoding: the body is decoded by its stored CTE and charset only (A-web-8).
func TestAdfOptSanitizeBodyDecoding(t *testing.T) {
	const qpHeaders = "Subject: t\nContent-Type: text/plain;\n charset=utf-8\nContent-Transfer-Encoding: quoted-printable"
	cases := []struct {
		name, body, headers, want string
	}{
		{"plain =41", "x =41 y", "", "x =41 y"},
		{"plain soft break", "soft=\nbreak", "Content-Transfer-Encoding: 8bit", "soft=\nbreak"},
		{"plain encoded word", "see =?UTF-8?Q?x?= here", "", "see =?UTF-8?Q?x?= here"},
		{"plain entity", "a &lt;b&gt; c", "", "a &lt;b&gt; c"},
		{"qp =41", "x =41 y", qpHeaders, "x A y"},
		{"qp soft break", "soft=\nbreak", qpHeaders, "softbreak"},
		{"qp utf8", "Gr=C3=BC=C3=9Fe", qpHeaders, "Grüße"},
		{"mixed invalid", "ok utf8 ü plus latin1 \xfc", "", "ok utf8 ü plus latin1 ü"},
		{"latin1 charset", "Gr\xfc\xdfe", "Content-Type: text/plain; charset=ISO-8859-1", "Grüße"},
		{"latin1 qp", "Gr=FC=DFe", "Content-Type: text/plain; charset=iso-8859-15\nContent-Transfer-Encoding: Quoted-Printable", "Grüße"},
		{"mislabelled utf8", "Grüße", "Content-Type: text/plain; charset=iso-8859-1", "Grüße"},
		{"base64", base64.StdEncoding.EncodeToString([]byte("hello\nwörld"))[:8] + "\n" +
			base64.StdEncoding.EncodeToString([]byte("hello\nwörld"))[8:], "Content-Transfer-Encoding: base64", "hello\nwörld"},
		{"multipart keeps qp text", "x =41 y", "Content-Type: multipart/mixed; boundary=b\nContent-Transfer-Encoding: quoted-printable", "x =41 y"},
	}
	for _, c := range cases {
		if got := decodeBodyText(c.body, c.headers); got != c.want {
			t.Errorf("%s: decodeBodyText=%q, want %q", c.name, got, c.want)
		}
	}

	// Through PrintSanitized, the headers come from HeadersJSON.
	a := &Article{BodyText: "x =41 y\nsoft=\nbreak", HeadersJSON: qpHeaders}
	if got := string(a.PrintSanitized("bodytext")); got != "x A y\nsoftbreak" {
		t.Errorf("PrintSanitized(bodytext) with QP CTE=%q", got)
	}
	a = &Article{BodyText: "x =41 y\nsoft=\nbreak"}
	if got := string(a.PrintSanitized("bodytext")); got != "x =41 y\nsoft=\nbreak" {
		t.Errorf("PrintSanitized(bodytext) without CTE=%q", got)
	}

	// Headers: per-sequence repair, no QP pass.
	if got := decodeHeaderText("ok ü and \xfc a=41"); got != "ok ü and ü a=41" {
		t.Errorf("decodeHeaderText=%q", got)
	}
	// ConvertToUTF8 keeps its behaviour for its outside callers.
	if got := ConvertToUTF8("&lt;svg&gt; x =41"); got != "<svg> x A" {
		t.Errorf("ConvertToUTF8 changed: %q", got)
	}
}

// TestAdfOptSanitizeEscapedNotStripped: text is escaped, not filtered (A-web-9).
func TestAdfOptSanitizeEscapedNotStripped(t *testing.T) {
	const body = "click onclick='go()' here and <script>x</script> end javascript:alert(1)"
	const want = "click onclick=&#39;go()&#39; here and &lt;script&gt;x&lt;/script&gt; end javascript:alert(1)"
	a := &Article{MessageID: "<adf-opt-esc@test.invalid>", BodyText: body, Subject: body}
	if got := string(a.PrintSanitized("bodytext")); got != want {
		t.Errorf("PrintSanitized(bodytext)=%q, want %q", got, want)
	}
	if got := string(a.PrintSanitized("subject")); got != want {
		t.Errorf("PrintSanitized(subject)=%q, want %q", got, want)
	}
	if got := a.GetCleanSubject(); got != body {
		t.Errorf("GetCleanSubject=%q, want the unfiltered subject", got)
	}
}

// TestAdfOptSanitizeOverviewNoWriteBack: PrintSanitized and BatchSanitizeOverviews only read the Overview (A-web-9).
func TestAdfOptSanitizeOverviewNoWriteBack(t *testing.T) {
	adfOptLiveCache(t)
	orig := Overview{
		ArticleNum: 7,
		Subject:    "=?UTF-8?Q?Gr=C3=BC=C3=9Fe?= <script>x</script>",
		FromHeader: "=?UTF-8?Q?J=C3=BCrgen?= <j@test.invalid>",
		DateString: "Thu, 15 Oct 2026 06:00:00 +0000",
		DateSent:   time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC),
		MessageID:  "<adf-opt-ov@test.invalid>",
		References: "<adf-opt-ref@test.invalid>",
	}
	o := orig
	for _, f := range []string{"subject", "fromheader", "date_string", "references", "messageid"} {
		_ = o.PrintSanitized(f)
		_ = o.PrintSanitized(f, "adf.opt.group")
	}
	o2 := orig
	BatchSanitizeOverviews([]*Overview{&o2}, "adf.opt.group2")
	for _, got := range []Overview{o, o2} {
		if got.Subject != orig.Subject || got.FromHeader != orig.FromHeader || got.DateString != orig.DateString ||
			got.References != orig.References || got.Sanitized {
			t.Errorf("overview modified: %+v", got)
		}
	}
	if s := string(o.PrintSanitized("subject", "adf.opt.group")); s != "Grüße &lt;script&gt;x&lt;/script&gt;" {
		t.Errorf("overview subject=%q", s)
	}
	if s := string(o.PrintSanitized("from", "adf.opt.group2")); s != "Jürgen" {
		t.Errorf("overview from (batch prefill)=%q", s)
	}
}

// TestAdfOptSanitizeCacheKeyPerGroup: the cache is keyed by group and message-id (A-web-10).
func TestAdfOptSanitizeCacheKeyPerGroup(t *testing.T) {
	sc := adfOptLiveCache(t)
	const mid = "<adf-opt-dup@test.invalid>"
	a := &Article{MessageID: mid, Subject: "legit subject in group A"}
	b := &Article{MessageID: mid, Subject: "attacker subject in group B"}
	if got := string(a.PrintSanitized("subject", "group.a")); got != "legit subject in group A" {
		t.Fatalf("A=%q", got)
	}
	if got := string(b.PrintSanitized("subject", "group.b")); got != "attacker subject in group B" {
		t.Fatalf("B=%q, want its own subject", got)
	}
	// Cached per group: a different Article object in group A is served A's cached value.
	other := &Article{MessageID: mid, Subject: "changed"}
	if got := string(other.PrintSanitized("subject", "group.a")); got != "legit subject in group A" {
		t.Errorf("group A cache hit=%q", got)
	}
	// Without a group nothing is cached.
	before := sc.Stats()["entries"]
	if got := string(other.PrintSanitized("subject")); got != "changed" {
		t.Errorf("no-group render=%q", got)
	}
	if after := sc.Stats()["entries"]; after != before {
		t.Errorf("render without group added cache entries: %v -> %v", before, after)
	}

	// The batch prefill lands under the key PrintSanitized reads.
	c := &Article{MessageID: "<adf-opt-batch@test.invalid>", Subject: "batch subject",
		DateSent: time.Date(2026, 10, 15, 6, 7, 0, 0, time.UTC), FromHeader: "Bob <b@test.invalid>"}
	BatchSanitizeArticles([]*Article{c}, "group.c")
	c2 := &Article{MessageID: c.MessageID, Subject: "not from the batch"}
	if got := string(c2.PrintSanitized("subject", "group.c")); got != "batch subject" {
		t.Errorf("batch prefill not read by PrintSanitized: %q", got)
	}
	// The batch date equals the PrintSanitized date.
	fresh := &Article{DateSent: c.DateSent}
	want := string(fresh.PrintSanitized("date_string"))
	for _, f := range []string{"date", "date_string", "datestring"} {
		if got := string(c2.PrintSanitized(f, "group.c")); got != want {
			t.Errorf("batch %s=%q, PrintSanitized=%q", f, got, want)
		}
	}
	// Aliases share one slot.
	if v1, v2 := c2.PrintSanitized("from", "group.c"), c2.PrintSanitized("fromheader", "group.c"); v1 != "Bob" || v2 != "Bob" {
		t.Errorf("from=%q fromheader=%q", v1, v2)
	}
}

// TestAdfOptSanitizeCacheFieldNames: one alias map; unknown fields are neither stored nor counted (A-web-10).
func TestAdfOptSanitizeCacheFieldNames(t *testing.T) {
	sc := cache.NewSanitizedCache(100, time.Hour)
	t.Cleanup(sc.Stop)
	const key = "g\x00<adf-opt-fields@test.invalid>"

	sc.SetField(key, "bogus", "12345")
	if _, ok := sc.GetArticle(key); ok {
		t.Fatal("unknown field created an entry")
	}
	if got := sc.GetCachedSize(); got != 0 {
		t.Fatalf("unknown field counted: size=%d", got)
	}

	sc.SetField(key, "from", "Bob")
	for _, f := range []string{"from", "fromheader", "from_header", "author", "FromHeader"} {
		if v, ok := sc.GetField(key, f); !ok || v != "Bob" {
			t.Errorf("GetField(%s) ok=%v v=%q", f, ok, v)
		}
	}
	sc.SetField(key, "message_id", "m")
	if v, ok := sc.GetField(key, "messageid"); !ok || v != "m" {
		t.Errorf("message_id/messageid: ok=%v v=%q", ok, v)
	}
	sc.SetArticle(key, map[string]template.HTML{"body_text": "bb", "unknown": "zzzzzz"})
	if v, ok := sc.GetField(key, "bodytext"); !ok || v != "bb" {
		t.Errorf("body_text/bodytext: ok=%v v=%q", ok, v)
	}
	// Overwriting through another alias replaces the slot without double counting.
	sc.BatchSetArticles(map[string]map[string]template.HTML{key: {"author": "Alice", "nope": "123"}})
	if got, want := sc.GetCachedSize(), int64(len("Alice")+len("m")+len("bb")); got != want {
		t.Errorf("cached size=%d, want %d", got, want)
	}
	for _, f := range []string{"subject", "from", "body", "date", "date_since", "references", "messageid", "path", "headers_json"} {
		if c, ok := cache.CanonicalField(f); !ok || c != f {
			t.Errorf("CanonicalField(%q)=%q,%v; batch field names must be canonical", f, c, ok)
		}
	}
	for _, f := range append(append([]string{}, batchArticleFields...), batchOverviewFields...) {
		if c, ok := cache.CanonicalField(f); !ok || c != f {
			t.Errorf("batch field %q is not canonical (%q)", f, c)
		}
	}
}
