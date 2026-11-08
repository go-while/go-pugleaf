package feed

import (
	"strings"
	"testing"
)

// flags_test.go covers the out_flags parser and filter (plan nntp-peering, slice feed-queue):
// the newsfeeds(5) subset <N, >N, HN, GN, Nm, Nu, and the rule that anything else is a parse
// error a peer form can show.
//
// The size bounds and Nm/Nu follow INN's code, not a loose reading of the manual page: an
// article of exactly N bytes satisfies both <N and >N (innd compares with > and <), and
// Nm/Nu narrow the peer's subscription (innd/newsfeeds.c SITEparseone) instead of refusing
// the article, so a crosspost into a moderated and an unmoderated group still goes to an Nm
// peer for the moderated group (innd/art.c ARTpost).

// fAllows is Flags.Allows with the subscribed groups defaulting to the article's own
// newsgroups, which is what out_patterns "*" leaves wanted.
func fAllows(f Flags, a Snapshot) bool {
	return f.Allows(a, a.Newsgroups)
}

func TestParseFlagsEmpty(t *testing.T) {
	f, err := ParseFlags("   ")
	if err != nil {
		t.Fatalf("ParseFlags(blank) = %v, want no error", err)
	}
	if f != (Flags{}) {
		t.Errorf("ParseFlags(blank) = %+v, want the zero Flags", f)
	}
	if !fAllows(f, Snapshot{Newsgroups: []string{"alt.test"}, Bytes: 1 << 20}) {
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
	// white space separates as well, and a bare H / bare G is H1 / G1 (INN defaults both
	// counts to 1).
	f, err = ParseFlags("H  G Nm")
	if err != nil {
		t.Fatalf("ParseFlags(\"H  G Nm\"): %v", err)
	}
	if f.MaxPathHops != 1 || f.MaxGroups != 1 || !f.ModeratedOnly {
		t.Errorf("parsed %+v, want MaxPathHops 1, MaxGroups 1 and Nm", f)
	}
}

func TestParseFlagsErrors(t *testing.T) {
	for _, bad := range []string{
		"X",       // unknown letter
		"h4",      // lower case: not how newsfeeds(5) writes it
		"T4",      // feed type: not implemented here
		"Ap",      // A checks: not implemented here
		"Gx",      // not a number
		"G0",      // a count below 1 would refuse everything
		"<",       // no size
		"<0",      // zero size
		">-5",     // negative
		"N",       // neither m nor u
		"Nx",      // unknown N flag
		"Nm,Nu",   // contradiction
		"<98,>99", // no size can satisfy both
		"Ws",      // write item
		"*",       // a pattern, not a flag
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
		{99, false}, // below the ">100" minimum
		{100, true}, // exactly N satisfies both bounds (innd compares with < and >)
		{101, true},
		{999, true},
		{1000, true}, // exactly the "<1000" maximum
		{1001, false},
		{5000, false},
	} {
		if got := fAllows(f, Snapshot{Bytes: tc.bytes, Newsgroups: []string{"alt.test"}}); got != tc.want {
			t.Errorf("Allows(%d bytes) = %v, want %v", tc.bytes, got, tc.want)
		}
	}
	// "<100,>100" accepts exactly one size instead of being refused as a contradiction.
	exact, err := ParseFlags("<100,>100")
	if err != nil {
		t.Fatalf("ParseFlags(\"<100,>100\"): %v", err)
	}
	if !fAllows(exact, Snapshot{Bytes: 100, Newsgroups: []string{"alt.test"}}) {
		t.Error("<100,>100 refused an article of exactly 100 bytes")
	}
	if fAllows(exact, Snapshot{Bytes: 101, Newsgroups: []string{"alt.test"}}) {
		t.Error("<100,>100 accepted an article of 101 bytes")
	}
}

func TestFlagsAllowsGroupsAndHops(t *testing.T) {
	f, err := ParseFlags("G2,H3")
	if err != nil {
		t.Fatal(err)
	}
	two := Snapshot{Newsgroups: []string{"alt.a", "alt.b"}, Path: "a!b!c"}
	if !fAllows(f, two) {
		t.Error("G2,H3 refused 2 groups and 3 hops, want accepted")
	}
	three := Snapshot{Newsgroups: []string{"alt.a", "alt.b", "alt.c"}, Path: "a!b!c"}
	if fAllows(f, three) {
		t.Error("G2 accepted 3 newsgroups")
	}
	hops := Snapshot{Newsgroups: []string{"alt.a"}, Path: "a!b!c!d"}
	if fAllows(f, hops) {
		t.Error("H3 accepted a Path with 4 elements")
	}
	// Gcount counts the article's newsgroups, not the subset the peer subscribes to: a
	// crosspost over the limit is refused even when only one group is wanted.
	if f.Allows(three, []string{"alt.a"}) {
		t.Error("G2 accepted a 3-group crosspost because only one group was subscribed")
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

	if !fAllows(mod, moderated) || fAllows(mod, plain) {
		t.Error("Nm must accept a moderated group and refuse an unmoderated one")
	}
	if fAllows(unmod, moderated) || !fAllows(unmod, plain) {
		t.Error("Nu must refuse a moderated group and accept an unmoderated one")
	}
	// A group this server has no row for has no known status, so neither flag can decide
	// it is subscribed: both drop it.
	if fAllows(mod, unknown) {
		t.Error("Nm accepted a group of unknown status")
	}
	if fAllows(unmod, unknown) {
		t.Error("Nu accepted a group of unknown status; an unknown status is not 'unmoderated'")
	}
	// INN applies N to the per-group subscription and feeds the site if any group
	// survives: a crosspost into a moderated and an unmoderated group reaches BOTH an Nm
	// and an Nu peer, each for its own group.
	mixed := Snapshot{
		Newsgroups: []string{"alt.m", "alt.u"},
		Moderated:  map[string]bool{"alt.m": true, "alt.u": false},
	}
	if !fAllows(mod, mixed) {
		t.Error("Nm refused a crosspost that holds a moderated group: INN feeds the site for that group")
	}
	if !fAllows(unmod, mixed) {
		t.Error("Nu refused a crosspost that holds an unmoderated group: INN feeds the site for that group")
	}
	// Only the groups out_patterns subscribe to are narrowed: with just the unmoderated
	// group wanted, Nm has nothing left.
	if mod.Allows(mixed, []string{"alt.u"}) {
		t.Error("Nm accepted an article whose only wanted group is unmoderated")
	}
	if !mod.Allows(mixed, []string{"alt.m"}) {
		t.Error("Nm refused an article whose wanted group is moderated")
	}
	if fAllows(mod, Snapshot{}) || fAllows(unmod, Snapshot{}) {
		t.Error("Nm/Nu accepted an article without newsgroups; there is nothing subscribed")
	}
}

func TestFlagsModerationWanted(t *testing.T) {
	wanted := []string{"alt.m", "alt.u", "alt.unknown"}
	status := map[string]bool{"alt.m": true, "alt.u": false}

	plain, err := ParseFlags("<9999")
	if err != nil {
		t.Fatal(err)
	}
	// Without Nm/Nu nothing is narrowed, and the status map is not even consulted.
	if got := plain.ModerationWanted(wanted, nil); len(got) != 3 {
		t.Errorf("ModerationWanted without Nm/Nu = %v, want every wanted group", got)
	}
	mod, err := ParseFlags("Nm")
	if err != nil {
		t.Fatal(err)
	}
	if got := mod.ModerationWanted(wanted, status); len(got) != 1 || got[0] != "alt.m" {
		t.Errorf("Nm ModerationWanted = %v, want [alt.m]", got)
	}
	unmod, err := ParseFlags("Nu")
	if err != nil {
		t.Fatal(err)
	}
	if got := unmod.ModerationWanted(wanted, status); len(got) != 1 || got[0] != "alt.u" {
		t.Errorf("Nu ModerationWanted = %v, want [alt.u]", got)
	}
	if got := mod.ModerationWanted(wanted, nil); len(got) != 0 {
		t.Errorf("Nm ModerationWanted with no known status = %v, want nothing", got)
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
