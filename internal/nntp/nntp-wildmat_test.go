package nntp

import (
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestLstWildmatRFCExamples: the examples of RFC 3977 §4.2 and §4.4.
func TestLstWildmatRFCExamples(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		// §4.2: the rightmost matching pattern decides.
		{"a*,!*b,*c*", "aaa", true},
		{"a*,!*b,*c*", "abb", false},
		{"a*,!*b,*c*", "ccb", true},
		{"a*,!*b,*c*", "xxx", false},
		// §4.4: exact patterns.
		{"abc", "abc", true},
		{"abc", "abcd", false},
		{"abc", "ab", false},
		{"abc", "ABC", false},
		{"abc,def", "abc", true},
		{"abc,def", "def", true},
		{"abc,def", "abcdef", false},
		// One character is one rune, not one octet: "£" is 2 octets in UTF-8.
		{"£", "£", true},
		{"?", "£", true},
		{"??", "£", false},
		{"a?c", "a£c", true},
		// §4.4 wildcard examples.
		{"a*", "abc", true},
		{"a*", "a", true},
		{"a*", "ba", false},
		{"a*b", "ab", true},
		{"a*b", "axyzb", true},
		{"a*b", "abc", false},
		{"a*b", "a", false},
		{"a*,*b", "ac", true},
		{"a*,*b", "cb", true},
		{"a*,*b", "cc", false},
		{"a*,!*b", "ac", true},
		{"a*,!*b", "ab", false},
		{"a*,!*b", "cb", false},
		{"a*,!*b,c*", "ac", true},
		{"a*,!*b,c*", "ab", false},
		{"a*,!*b,c*", "cb", true},
		{"a*,!*b,c*", "xb", false},
		{"a*,c*,!*b", "cb", false},
		{"a*,c*,!*b", "ca", true},
		{"?a*", "ba", true},
		{"?a*", "abc", false},
		{"a??", "abc", true},
		{"a??", "ab", false},
		{"a??", "abcd", false},
		// "*" matches the empty string; patterns are anchored.
		{"*", "", true},
		{"*", "anything.at.all", true},
		{"t3.*", "t3.x.y", true},
		{"t3.*", "t3.", true},
		{"t3.*", "t3", false},
		{"t3.*", "xt3.x", false},
		{"*.b", "a.b", true},
		{"*.b", "a.b.c", false},
		{"**a**", "xay", true},
		{"*a*b*c*", "aXbYc", true},
		{"*a*b*c*", "aXcYb", false},
	}
	for _, tc := range cases {
		if !wildmatValid(tc.pattern) {
			t.Errorf("wildmatValid(%q) = false, want true", tc.pattern)
			continue
		}
		if got := wildmatMatch(tc.pattern, tc.name); got != tc.want {
			t.Errorf("wildmatMatch(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

// TestLstWildmatInvalid: empty patterns, the excluded characters, controls and
// invalid UTF-8 make a wildmat invalid; a leading "!" on the first pattern is a
// lenient extension (D8) that can only exclude.
func TestLstWildmatInvalid(t *testing.T) {
	for _, p := range []string{
		"", "a,,b", "a,", ",a", "!", "!!a", "a,!", "a!b", `a\b`, "[ab]", "a]", "a[",
		"\xff", "a\xc3", "a\tb", "a\x00", "a\x7f", "a\r", "a\nb",
	} {
		if wildmatValid(p) {
			t.Errorf("wildmatValid(%q) = true, want false", p)
		}
		if wildmatMatch(p, "a") || wildmatMatch(p, "ab") {
			t.Errorf("wildmatMatch(%q, …) = true for an invalid wildmat", p)
		}
		if _, _, ok := wildmatSplit(p); ok {
			t.Errorf("wildmatSplit(%q) ok", p)
		}
	}
	if !wildmatValid("!a*") {
		t.Fatal(`wildmatValid("!a*") = false, want true (lenient leading "!")`)
	}
	for _, name := range []string{"a", "abc", "xyz", ""} {
		if wildmatMatch("!a*", name) {
			t.Errorf(`wildmatMatch("!a*", %q) = true, want false`, name)
		}
	}
	pats, neg, ok := wildmatSplit("!a*,b?,!c")
	if !ok || strings.Join(pats, "|") != "a*|b?|c" || len(neg) != 3 || !neg[0] || neg[1] || !neg[2] {
		t.Errorf(`wildmatSplit("!a*,b?,!c") = %q %v %v`, pats, neg, ok)
	}
}

// lstWildmatRef is the regexp reference for wildmatMatch: each pattern becomes an
// anchored regexp ("*" → ".*", "?" → "."), the rightmost match decides.
func lstWildmatRef(t *testing.T, pattern, name string) bool {
	pats, neg, ok := wildmatSplit(pattern)
	if !ok {
		t.Fatalf("lstWildmatRef: invalid wildmat %q", pattern)
	}
	for i := len(pats) - 1; i >= 0; i-- {
		q := regexp.QuoteMeta(pats[i])
		q = strings.ReplaceAll(q, `\*`, ".*")
		q = strings.ReplaceAll(q, `\?`, ".")
		if regexp.MustCompile("(?s)^" + q + "$").MatchString(name) {
			return !neg[i]
		}
	}
	return false
}

// FuzzLstWildmat: for valid wildmats wildmatMatch equals the regexp reference; it
// never panics and stays fast on hostile 512-byte inputs. Under plain `go test` only
// the seed corpus below runs.
func FuzzLstWildmat(f *testing.F) {
	seeds := []struct{ pattern, name string }{
		{"a*,!*b,*c*", "aaa"},
		{"a*,!*b,*c*", "abb"},
		{"a*,!*b,*c*", "ccb"},
		{"£", "£"},
		{"?", "£"},
		{"?£*", "x£yz"},
		{"*", ""},
		{"t3.*", "t3.x.y"},
		{"!a*", "abc"},
		{"rc2.*,!*.b.*", "rc2.x.b.7"},
		{"a.b+c(d)|e^$", "a.b+c(d)|e^$"},
		{"*.*.*", "a..b"},
		{"a,,b", "a"},
		{"[ab]", "a"},
		{"\xff", "\xff"},
		{strings.Repeat("*a", 200) + "b", strings.Repeat("a", 512)},
		{strings.Repeat("?", 256), strings.Repeat("£", 256)},
		{strings.Repeat("a*", 256), strings.Repeat("ab", 256)},
	}
	for _, s := range seeds {
		f.Add(s.pattern, s.name)
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		start := time.Now()
		got := wildmatMatch(pattern, name)
		valid := wildmatValid(pattern)
		if d := time.Since(start); len(pattern) <= 512 && len(name) <= 512 && d > time.Second {
			t.Fatalf("wildmatMatch(%d bytes, %d bytes) took %s", len(pattern), len(name), d)
		}
		if !valid {
			if got {
				t.Fatalf("wildmatMatch(%q, %q) = true for an invalid wildmat", pattern, name)
			}
			return
		}
		if !utf8.ValidString(name) {
			return // group names are UTF-8; the reference would need its own invalid-byte rules
		}
		if want := lstWildmatRef(t, pattern, name); got != want {
			t.Fatalf("wildmatMatch(%q, %q) = %v, regexp reference %v", pattern, name, got, want)
		}
	})
}
