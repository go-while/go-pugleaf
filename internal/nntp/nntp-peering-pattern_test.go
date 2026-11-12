package nntp

// Tests of the peer subscription list (plan nntp-peering, slice peer-acl):
// ParsePeerPatterns and MatchNewsgroupPatterns with the INN newsfeeds(5) semantics
// (last match wins per newsgroup, "!" excludes one newsgroup, "@" poisons the article).
//
// Identifiers use the pat / TestPat prefix.

import (
	"strings"
	"testing"
)

func TestPatParse(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"*", []string{"*"}},
		{"comp.*,!comp.lang.*,@*.binaries.*", []string{"comp.*", "!comp.lang.*", "@*.binaries.*"}},
		{" comp.* , , !junk ", []string{"comp.*", "!junk"}},
		{"a\tb\nc\r,d", []string{"a", "b", "c", "d"}},
		{"!,@,,", nil}, // a bare prefix is not a pattern
	} {
		got := ParsePeerPatterns(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("ParsePeerPatterns(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPatOneGroup: one newsgroup against a pattern list; the last pattern that matches it
// decides (INN newsfeeds(5)).
func TestPatOneGroup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns string
		group    string
		accept   bool
		poisoned bool
	}{
		{"all", "*", "comp.lang.go", true, false},
		{"none", "", "comp.lang.go", false, false},
		{"no-match", "comp.*", "alt.test", false, false},
		{"exact", "comp.lang.go", "comp.lang.go", true, false},
		{"prefix-is-no-match", "comp", "comp.lang.go", false, false},
		{"star", "comp.*", "comp.lang.go", true, false},
		{"question", "comp.lang.g?", "comp.lang.go", true, false},
		{"question-one-rune-only", "comp.lang.g?", "comp.lang.goo", false, false},
		{"excluded", "comp.*,!comp.lang.*", "comp.lang.go", false, false},
		{"re-included-after-exclude", "comp.*,!comp.lang.*,comp.lang.go", "comp.lang.go", true, false},
		{"poison", "*,@*.binaries.*", "alt.binaries.cd", false, true},
		{"poison-overridden-by-a-later-pattern", "@*.binaries.*,alt.binaries.cd", "alt.binaries.cd", true, false},
		{"poison-after-an-include", "alt.*,@alt.binaries.*", "alt.binaries.cd", false, true},
		{"only-exclusions", "!*", "alt.test", false, false},
	} {
		res := MatchNewsgroupPatterns(tc.patterns, []string{tc.group})
		if res.Accept() != tc.accept || res.Poisoned != tc.poisoned {
			t.Errorf("%s: %q vs %q = %+v, want accept=%v poisoned=%v",
				tc.name, tc.patterns, tc.group, res, tc.accept, tc.poisoned)
		}
	}
}

// TestPatCrosspost: a crossposted article is wanted when one of its newsgroups is, and
// refused as soon as one of them is poisoned ("@").
func TestPatCrosspost(t *testing.T) {
	const patterns = "comp.*,alt.*,!alt.flame,@*.binaries.*"
	for _, tc := range []struct {
		name     string
		groups   []string
		wanted   []string
		poisoned bool
	}{
		{"both wanted", []string{"comp.lang.go", "alt.test"}, []string{"comp.lang.go", "alt.test"}, false},
		{"one wanted", []string{"comp.lang.go", "misc.test"}, []string{"comp.lang.go"}, false},
		{"excluded group dropped", []string{"alt.flame", "alt.test"}, []string{"alt.test"}, false},
		{"only excluded", []string{"alt.flame"}, nil, false},
		{"none wanted", []string{"misc.test"}, nil, false},
		{"poisoned wins", []string{"comp.lang.go", "alt.binaries.cd"}, nil, true},
		{"poisoned first", []string{"alt.binaries.cd", "comp.lang.go"}, nil, true},
		{"no groups", nil, nil, false},
	} {
		res := MatchNewsgroupPatterns(patterns, tc.groups)
		if res.Poisoned != tc.poisoned {
			t.Errorf("%s: poisoned=%v, want %v", tc.name, res.Poisoned, tc.poisoned)
		}
		if strings.Join(res.Wanted, "|") != strings.Join(tc.wanted, "|") {
			t.Errorf("%s: wanted %q, want %q", tc.name, res.Wanted, tc.wanted)
		}
		if res.Accept() != (!tc.poisoned && len(tc.wanted) > 0) {
			t.Errorf("%s: Accept()=%v with wanted %q poisoned=%v", tc.name, res.Accept(), res.Wanted, res.Poisoned)
		}
	}
}

// TestPatPatternReported: the result names the pattern that decided, for the log line.
func TestPatPatternReported(t *testing.T) {
	res := MatchNewsgroupPatterns("comp.*,@*.binaries.*", []string{"comp.lang.go", "alt.binaries.cd"})
	if !res.Poisoned || res.Pattern != "@*.binaries.*" {
		t.Errorf("poisoned by %+v, want the @ pattern", res)
	}
	res = MatchNewsgroupPatterns("comp.*,alt.*", []string{"alt.test", "comp.lang.go"})
	if !res.Accept() || res.Pattern != "alt.*" {
		t.Errorf("accepted by %+v, want the pattern of the first wanted group", res)
	}
}

// TestPatDefaultOutPatterns: the default out_patterns of the peers table (migration 0034)
// behave as intended. The outgoing feed uses them from wave 2 on; the semantics are tested
// here, where the matcher lives.
func TestPatDefaultOutPatterns(t *testing.T) {
	const patterns = "*,!control*,!junk"
	for _, tc := range []struct {
		group string
		want  bool
	}{
		{"comp.lang.go", true},
		{"control", false},
		{"control.cancel", false},
		{"junk", false},
		{"junk.local", true}, // "!junk" is exact: only the group itself
	} {
		if got := MatchNewsgroupPatterns(patterns, []string{tc.group}).Accept(); got != tc.want {
			t.Errorf("default out_patterns vs %q = %v, want %v", tc.group, got, tc.want)
		}
	}
}
