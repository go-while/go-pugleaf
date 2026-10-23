package models

// Tests for slice fl-web of plan followup-leftovers:
//   - DecodeBodyText (exported wrapper of decodeBodyText) and DisplayFrom;
//   - a malformed quoted-printable sequence leaves only its own line encoded, not the body;
//   - the sanitized cache's cachedSize matches its entries under concurrent writes and
//     evictions, and Clear resets it.

import (
	"fmt"
	"html/template"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/cache"
)

const flWebQPHeaders = "Subject: t\nContent-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: quoted-printable"

func TestFlWebDecodeBodyTextExported(t *testing.T) {
	cases := []struct{ body, headers string }{
		{"Gr=C3=BC=C3=9Fe &amp; =3D", flWebQPHeaders},
		{"plain &lt;b&gt; =41", ""},
		{"Gr\xfc\xdfe", "Content-Type: text/plain; charset=ISO-8859-1"},
	}
	for _, c := range cases {
		if got, want := DecodeBodyText(c.body, c.headers), decodeBodyText(c.body, c.headers); got != want {
			t.Errorf("DecodeBodyText(%q) = %q, want %q", c.body, got, want)
		}
	}
	if got := DecodeBodyText("Gr=C3=BC=C3=9Fe &amp; =3D", flWebQPHeaders); got != "Grüße &amp; =" {
		t.Errorf("DecodeBodyText = %q, want entities kept as text", got)
	}
	for raw, want := range map[string]string{
		"visible <visible@test.invalid>":   "visible",
		"user@example.invalid (Real Name)": "Real Name",
		"<only@example.invalid>":           "only",
		"":                                 "Anon",
	} {
		if got := DisplayFrom(raw); got != want {
			t.Errorf("DisplayFrom(%q) = %q, want %q", raw, got, want)
		}
		if got := DisplayFrom(raw); got != displayFrom(raw) {
			t.Errorf("DisplayFrom(%q) differs from displayFrom", raw)
		}
	}
}

// TestFlWebQPFallbackPerLine: one line the decoder rejects (a control byte, a lone "=" at the
// end) stays as it is; every other line is decoded, soft line breaks included.
func TestFlWebQPFallbackPerLine(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"control byte line", "Gr=C3=BC=C3=9Fe\nbad \x01 =41\nsoft=\nbreak =3D", "Grüße\nbad \x01 =41\nsoftbreak ="},
		{"lone = as the last line", "first =41\n=", "first A\n="},
		{"crlf soft break", "a=\r\nb\r\nbad \x02\r\n=41", "ab\r\nbad \x02\r\nA"},
		{"all good", "x =41 y\nsoft=\nbreak", "x A y\nsoftbreak"},
	}
	for _, c := range cases {
		if got := decodeBodyText(c.body, flWebQPHeaders); got != c.want {
			t.Errorf("%s: decodeBodyText = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestFlWebSanitizedCacheSize: concurrent single and batch writes with evictions (a bound far
// below the key space) leave cachedSize equal to the sum of the entries; Clear resets it.
func TestFlWebSanitizedCacheSize(t *testing.T) {
	sc := cache.NewSanitizedCache(16, time.Hour)
	defer sc.Stop()
	const keys, workers, rounds = 64, 8, 300
	key := func(i int) string { return fmt.Sprintf("g\x00<k%d@test>", i%keys) }
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				val := template.HTML(strings.Repeat("v", (w*rounds+r)%37))
				if r%2 == 0 {
					sc.SetField(key(w*7+r), "subject", val)
					continue
				}
				batch := map[string]map[string]template.HTML{
					key(w + r):     {"subject": val, "from": val + "f"},
					key(w + r + 1): {"body": val + "bb"},
				}
				sc.BatchSetArticles(batch)
			}
		}(w)
	}
	wg.Wait()

	var sum int64
	for i := 0; i < keys; i++ {
		if a, ok := sc.GetArticle(key(i)); ok {
			sum += a.Size
		}
	}
	if got := sc.GetCachedSize(); got != sum {
		t.Fatalf("cachedSize = %d, sum of the cached entries = %d", got, sum)
	}
	if n := sc.Stats()["entries"].(int); n > 16 {
		t.Fatalf("%d entries, want at most 16", n)
	}

	sc.Clear()
	if got := sc.GetCachedSize(); got != 0 {
		t.Fatalf("cachedSize after Clear = %d, want 0", got)
	}
	sc.SetField(key(1), "subject", "abc")
	if got := sc.GetCachedSize(); got != 3 {
		t.Fatalf("cachedSize after Clear and one 3-byte write = %d, want 3", got)
	}
	sc.Stop() // Stop is idempotent per instance (the deferred Stop runs too)
}
