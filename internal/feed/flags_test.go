package feed

import (
	"strings"
	"testing"
)

// flags_test.go covers the out_flags parser and filter (plan nntp-peering, slice feed-queue):
// the newsfeeds(5) subset <N, >N, HN, GN, Nm, Nu, and the rule that anything else is a parse
// error a peer form can show.

func TestParseFlagsEmpty(t *testing.T) {
	f, err := ParseFlags("   ")
	if err != nil {
		t.Fatalf("ParseFlags(blank) = %v, want no error", err)
	}
	if f != (Flags{}) {
		t.Errorf("ParseFlags(blank) = %+v, want the zero Flags", f)
	}
	if !f.Allows(Snapshot{Newsgroups: []string{"alt.test"}, Bytes: 1 << 20}) {
		t.Error("the zero Flags refused an article; an empty out_flags accepts every article")
	}
}

func TestParseFlagsSubset(t *testing.T) {
	f, err := ParseFlags("<64000,>100,H8,G5,Nu")
	if err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if f.MaxBytes != 64000 || f.MinBytes != 100 || f.MaxPathHops != 8 || f.MaxGroups != 5 {
		t.Errorf("parsed %+v, want MaxBytes 64000, MinBytes 100, MaxPathHops 8, MaxGroups 5", f)
	}
	if f.ModeratedOnly || !f.UnmoderatedOnly {
		t.Errorf("parsed %+v, want Nu only", f)
	}
	if f.Raw != "<64000,>100,H8,G5,Nu" {
		t.Errorf("Raw = %q, want the field as given", f.Raw)
	}
	// white space separates as well, and a bare H is H1
	f, err = ParseFlags("H  Nm")
	if err != nil {
		t.Fatalf("ParseFlags(\"H  Nm\"): %v", err)
	}
	if f.MaxPathHops != 1 || !f.ModeratedOnly {
		t.Errorf("parsed %+v, want MaxPathHops 1 and Nm", f)
	}
}

func TestParseFlagsErrors(t *testing.T) {
	for _, bad := range []string{
		"X",        // unknown letter
		"h4",       // lower case: not how newsfeeds(5) writes it
		"T4",       // feed type: not implemented here
		"Ap",       // A checks: not implemented here
		"G",        // no count
		"Gx",       // not a number
		"G0",       // a count below 1 would refuse everything
		"<",        // no size
		"<0",       // zero size
		">-5",      // negative
		"N",        // neither m nor u
		"Nx",       // unknown N flag
		"Nm,Nu",    // contradiction
		"<100,>99", // no size can satisfy both
		"Ws",       // write item
		"*",        // a pattern, not a flag
	} {
		f, err := ParseFlags(bad)
		if err == nil {
			t.Errorf("ParseFlags(%q) = %+v, nil; want an error", bad, f)
			continue
		}
		if !strings.Contains(err.Error(), "out_flags") {
			t.Errorf("ParseFlags(%q) error %q does not name out_flags", bad, err)
		}
		if f.Raw != strings.TrimSpace(bad) {
			t.Errorf("ParseFlags(%q) kept Raw %q; the raw field is reported back for the admin form", bad, f.Raw)
		}
	}
}

func TestFlagsAllowsSize(t *testing.T) {
	f, err := ParseFlags("<1000,>100")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		bytes int
		want  bool
	}{
		{100, false}, // ">100" is strictly larger
		{101, true},
		{999, true},
		{1000, false}, // "<1000" is strictly smaller
		{5000, false},
	} {
		if got := f.Allows(Snapshot{Bytes: tc.bytes, Newsgroups: []string{"alt.test"}}); got != tc.want {
			t.Errorf("Allows(%d bytes) = %v, want %v", tc.bytes, got, tc.want)
		}
	}
}

func TestFlagsAllowsGroupsAndHops(t *testing.T) {
	f, err := ParseFlags("G2,H3")
	if err != nil {
		t.Fatal(err)
	}
	two := Snapshot{Newsgroups: []string{"alt.a", "alt.b"}, Path: "a!b!c"}
	if !f.Allows(two) {
		t.Error("G2,H3 refused 2 groups and 3 hops, want accepted")
	}
	three := Snapshot{Newsgroups: []string{"alt.a", "alt.b", "alt.c"}, Path: "a!b!c"}
	if f.Allows(three) {
		t.Error("G2 accepted 3 newsgroups")
	}
	hops := Snapshot{Newsgroups: []string{"alt.a"}, Path: "a!b!c!d"}
	if f.Allows(hops) {
		t.Error("H3 accepted a Path with 4 elements")
	}
}

func TestFlagsAllowsModeration(t *testing.T) {
	mod, err := ParseFlags("Nm")
	if err != nil {
		t.Fatal(err)
	}
	unmod, err := ParseFlags("Nu")
	if err != nil {
		t.Fatal(err)
	}
	if !mod.NeedsModeration() || !unmod.NeedsModeration() {
		t.Fatal("Nm/Nu do not report NeedsModeration")
	}
	moderated := Snapshot{Newsgroups: []string{"alt.m"}, Moderated: map[string]bool{"alt.m": true}}
	plain := Snapshot{Newsgroups: []string{"alt.u"}, Moderated: map[string]bool{"alt.u": false}}
	unknown := Snapshot{Newsgroups: []string{"alt.unknown"}}
	mixed := Snapshot{Newsgroups: []string{"alt.m", "alt.u"}, Moderated: map[string]bool{"alt.m": true}}

	if !mod.Allows(moderated) || mod.Allows(plain) {
		t.Error("Nm must accept a moderated group and refuse an unmoderated one")
	}
	if unmod.Allows(moderated) || !unmod.Allows(plain) {
		t.Error("Nu must refuse a moderated group and accept an unmoderated one")
	}
	if mod.Allows(unknown) {
		t.Error("Nm accepted a group of unknown status; an unknown status counts as unmoderated")
	}
	if !unmod.Allows(unknown) {
		t.Error("Nu refused a group of unknown status; an unknown status counts as unmoderated")
	}
	if mod.Allows(mixed) || unmod.Allows(mixed) {
		t.Error("Nm/Nu must refuse an article crossposted to a moderated and an unmoderated group")
	}
	if mod.Allows(Snapshot{}) || unmod.Allows(Snapshot{}) {
		t.Error("Nm/Nu accepted an article without newsgroups; there is nothing to decide on")
	}
}

func TestPathHops(t *testing.T) {
	for _, tc := range []struct {
		path string
		want int
	}{
		{"", 0},
		{"ours.invalid", 1},
		{"ours.invalid!peer.invalid!not-for-mail", 3},
		{"ours.invalid!.POSTED!not-for-mail", 3},
		{"a!!b!", 2},
	} {
		if got := PathHops(tc.path); got != tc.want {
			t.Errorf("PathHops(%q) = %d, want %d", tc.path, got, tc.want)
		}
	}
}
