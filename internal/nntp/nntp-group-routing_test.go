package nntp

// Tests of the fetcher group router (plan fetcher-group-routing, wave 0): ValidateRoutingPatterns
// and GroupRouter precedence (pins, provider routing, primary fallback).
//
// Identifiers use the grt / TestGrt prefix.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

func grtProv(id int64, prio int, enabled bool, routing string) *models.Provider {
	return &models.Provider{ID: id, Name: "p" + string(rune('0'+id)), Priority: prio, Enabled: enabled, Routing: routing}
}

func grtPin(id, provider int64, prio int, patterns string) *models.GroupRoute {
	return &models.GroupRoute{ID: id, ProviderID: provider, Priority: prio, Enabled: true, Patterns: patterns}
}

func grtRouter(t *testing.T, providers []*models.Provider, routes []*models.GroupRoute) *GroupRouter {
	t.Helper()
	r, err := NewGroupRouter(providers, routes)
	if err != nil {
		t.Fatalf("NewGroupRouter: %v", err)
	}
	return r
}

func grtWant(t *testing.T, r *GroupRouter, group string, want []int64, wantPinned bool) {
	t.Helper()
	ids, pinned := r.Candidates(group)
	if len(ids) == 0 && len(want) == 0 {
		ids, want = nil, nil
	}
	if !reflect.DeepEqual(ids, want) || pinned != wantPinned {
		t.Errorf("Candidates(%q) = %v pinned=%v, want %v pinned=%v", group, ids, pinned, want, wantPinned)
	}
}

func TestGrtValidate(t *testing.T) {
	for _, tc := range []struct {
		in    string
		ok    bool
		match string // part of the error text
	}{
		{"", true, ""},
		{"  \t\n", true, ""},
		{"gmane.*,gwene.*", true, ""},
		{"gmane.* gwene.*", true, ""},
		{"tin.*,!tin.it.*", true, ""},
		{"digitalmars.*, D, D.gnu", true, ""},
		{"!tin.it.*,tin.*", true, ""},
		{"comp.lang.?", true, ""},
		{"@alt.*", false, "'@'"},
		{"alt.*,@alt.binaries.*", false, "'@'"},
		{"alt.*,!@x", false, "'@'"},
		{"!alt.*", false, "only '!'"},
		{"!alt.*,!comp.*", false, "only '!'"},
		{"alt.*,!", false, "empty pattern"},
		{"alt.*,!!comp.*", false, "invalid wildmat"},
		{"alt.[ab]*", false, "invalid wildmat"},
		{"alt\\x", false, "invalid wildmat"},
		{"alt.\x01", false, "invalid wildmat"},
		{"alt.\xff", false, "invalid wildmat"},
		{"a!b", false, "invalid wildmat"},
	} {
		err := ValidateRoutingPatterns(tc.in)
		if tc.ok && err != nil {
			t.Errorf("ValidateRoutingPatterns(%q) = %v, want nil", tc.in, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("ValidateRoutingPatterns(%q) = nil, want error", tc.in)
			} else if !strings.Contains(err.Error(), tc.match) {
				t.Errorf("ValidateRoutingPatterns(%q) = %v, want text %q", tc.in, err, tc.match)
			}
		}
	}
}

// TestGrtNoRouting: without routing and pins every group goes to the first enabled provider
// (priority order), as before routing existed; the other enabled providers stay unused.
func TestGrtNoRouting(t *testing.T) {
	r := grtRouter(t, []*models.Provider{
		grtProv(1, 0, false, ""),
		grtProv(2, 5, true, ""),
		grtProv(3, 9, true, ""),
	}, nil)
	for _, g := range []string{"alt.test", "gmane.test", "comp.lang.go"} {
		grtWant(t, r, g, []int64{2}, false)
	}
	if got := r.Participants(); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("Participants = %v, want [2]", got)
	}
	if id, ok := r.Primary(); !ok || id != 2 {
		t.Errorf("Primary = %d,%v want 2,true", id, ok)
	}
	if !r.Allowed(2, "alt.test") || r.Allowed(3, "alt.test") || r.Allowed(1, "alt.test") {
		t.Error("Allowed: only the primary may serve unrouted groups")
	}
}

// TestGrtPrimaryInputOrder: equal priorities keep the GetProviders order.
func TestGrtPrimaryInputOrder(t *testing.T) {
	r := grtRouter(t, []*models.Provider{grtProv(7, 1, true, ""), grtProv(3, 1, true, "")}, nil)
	if id, _ := r.Primary(); id != 7 {
		t.Errorf("Primary = %d, want 7 (first of equal priority)", id)
	}
	// unsorted input is sorted by priority
	r = grtRouter(t, []*models.Provider{grtProv(4, 9, true, ""), grtProv(5, 2, true, "")}, nil)
	if id, _ := r.Primary(); id != 5 {
		t.Errorf("Primary = %d, want 5 (lowest priority value)", id)
	}
}

// TestGrtPrimaryIsFirstWithEmptyRouting: a provider with routing is never the primary, even when
// it has the best priority.
func TestGrtPrimaryIsFirstWithEmptyRouting(t *testing.T) {
	r := grtRouter(t, []*models.Provider{
		grtProv(1, 0, true, "gmane.*,gwene.*"),
		grtProv(2, 1, false, ""),
		grtProv(3, 2, true, ""),
	}, nil)
	if id, ok := r.Primary(); !ok || id != 3 {
		t.Errorf("Primary = %d,%v want 3", id, ok)
	}
}

// TestGrtRoutedBeforePrimary: a routed provider comes first for its groups, the primary is the
// fallback; other groups go to the primary only.
func TestGrtRoutedBeforePrimary(t *testing.T) {
	r := grtRouter(t, []*models.Provider{
		grtProv(1, 0, true, ""),
		grtProv(2, 5, true, "gmane.*,gwene.*"),
		grtProv(3, 6, true, "tin.*,!tin.it.*"),
		grtProv(4, 7, true, "gmane.comp.*"),
	}, nil)
	grtWant(t, r, "alt.test", []int64{1}, false)
	grtWant(t, r, "gmane.test", []int64{2, 1}, false)
	grtWant(t, r, "gmane.comp.x", []int64{2, 4, 1}, false)
	grtWant(t, r, "gwene.a.b", []int64{2, 1}, false)
	grtWant(t, r, "tin.misc", []int64{3, 1}, false)
	grtWant(t, r, "tin.it.annunci", []int64{1}, false)
	if got := r.Participants(); !reflect.DeepEqual(got, []int64{1, 2, 3, 4}) {
		t.Errorf("Participants = %v, want [1 2 3 4]", got)
	}
	if !r.Allowed(2, "gmane.test") || r.Allowed(2, "alt.test") || !r.Allowed(1, "gmane.test") {
		t.Error("Allowed: routed provider only for its groups, primary as fallback for all")
	}
	if r.Allowed(3, "tin.it.annunci") {
		t.Error("Allowed(3, tin.it.annunci): '!' exclusion ignored")
	}
}

// TestGrtLastMatchWins: the last matching pattern decides, in both directions.
func TestGrtLastMatchWins(t *testing.T) {
	r := grtRouter(t, []*models.Provider{
		grtProv(1, 0, true, ""),
		grtProv(2, 1, true, "gmane.*,!gmane.spam.*,gmane.spam.ok"),
	}, nil)
	grtWant(t, r, "gmane.x", []int64{2, 1}, false)
	grtWant(t, r, "gmane.spam.bad", []int64{1}, false)
	grtWant(t, r, "gmane.spam.ok", []int64{2, 1}, false)
	r = grtRouter(t, []*models.Provider{
		grtProv(1, 0, true, ""),
		grtProv(2, 1, true, "!gmane.spam.*,gmane.*"),
	}, nil)
	grtWant(t, r, "gmane.spam.bad", []int64{2, 1}, false)
}

// TestGrtNoPrimary: only routed providers: groups they do not want have no candidate.
func TestGrtNoPrimary(t *testing.T) {
	r := grtRouter(t, []*models.Provider{grtProv(1, 0, true, "gmane.*"), grtProv(2, 1, false, "")}, nil)
	grtWant(t, r, "gmane.test", []int64{1}, false)
	grtWant(t, r, "alt.test", nil, false)
	if _, ok := r.Primary(); ok {
		t.Error("Primary: ok without an enabled provider with empty routing")
	}
	r = grtRouter(t, nil, nil)
	grtWant(t, r, "alt.test", nil, false)
	if len(r.Participants()) != 0 {
		t.Errorf("Participants without providers = %v", r.Participants())
	}
}

// TestGrtPinBeatsRouting: a pin sends its groups only to the pinned provider, also when that
// provider's own routing does not want them, and the primary and routed providers lose them.
func TestGrtPinBeatsRouting(t *testing.T) {
	r := grtRouter(t, []*models.Provider{
		grtProv(1, 0, true, ""),
		grtProv(2, 5, true, "gmane.*"),
		grtProv(3, 6, true, "digitalmars.*"),
	}, []*models.GroupRoute{grtPin(10, 3, 0, "pin.*,gmane.pinned.*")})
	grtWant(t, r, "pin.test", []int64{3}, true)
	grtWant(t, r, "gmane.pinned.x", []int64{3}, true)
	grtWant(t, r, "gmane.other", []int64{2, 1}, false)
	grtWant(t, r, "digitalmars.D", []int64{3, 1}, false)
	if r.Allowed(1, "pin.test") || r.Allowed(2, "gmane.pinned.x") || !r.Allowed(3, "pin.test") {
		t.Error("Allowed: pinned groups only for the pinned provider")
	}
}

// TestGrtPinTargetParticipates: an enabled provider with empty routing that is not the primary
// takes part only through a pin.
func TestGrtPinTargetParticipates(t *testing.T) {
	provs := []*models.Provider{grtProv(1, 0, true, ""), grtProv(2, 3, true, ""), grtProv(3, 4, true, "")}
	r := grtRouter(t, provs, []*models.GroupRoute{grtPin(1, 3, 0, "pin.*")})
	grtWant(t, r, "pin.a", []int64{3}, true)
	grtWant(t, r, "alt.a", []int64{1}, false)
	if got := r.Participants(); !reflect.DeepEqual(got, []int64{1, 3}) {
		t.Errorf("Participants = %v, want [1 3]", got)
	}
}

// TestGrtSeveralPins: several pins for one group: (route priority, provider priority) order,
// each provider once; non-matching pins do not add their providers.
func TestGrtSeveralPins(t *testing.T) {
	provs := []*models.Provider{
		grtProv(1, 0, true, ""),
		grtProv(2, 9, true, ""),
		grtProv(3, 4, true, ""),
		grtProv(4, 2, true, ""),
		grtProv(5, 1, true, ""),
	}
	routes := []*models.GroupRoute{
		grtPin(1, 2, 0, "pin.*"),       // route prio 0, provider prio 9
		grtPin(2, 3, 0, "pin.*"),       // route prio 0, provider prio 4
		grtPin(3, 4, 1, "pin.a.*"),     // route prio 1
		grtPin(4, 3, 2, "pin.*"),       // provider 3 again: once
		grtPin(5, 5, 0, "other.*"),     // does not want pin.*
		grtPin(6, 4, -1, "pin.b.only"), // route prio -1, only pin.b.only
	}
	r := grtRouter(t, provs, routes)
	grtWant(t, r, "pin.x", []int64{3, 2}, true)
	grtWant(t, r, "pin.a.x", []int64{3, 2, 4}, true)
	grtWant(t, r, "pin.b.only", []int64{4, 3, 2}, true)
	grtWant(t, r, "other.x", []int64{5}, true)
	if got := r.Participants(); !reflect.DeepEqual(got, []int64{1, 5, 4, 3, 2}) {
		t.Errorf("Participants = %v, want [1 5 4 3 2] (priority order)", got)
	}
}

// TestGrtDisabledPinTarget: a pin to a disabled (or unknown) provider gives no candidate and no
// fallback; the pinned flag tells the fetcher to skip the group.
func TestGrtDisabledPinTarget(t *testing.T) {
	provs := []*models.Provider{grtProv(1, 0, true, ""), grtProv(2, 1, false, ""), grtProv(3, 2, true, "pin.*")}
	r := grtRouter(t, provs, []*models.GroupRoute{grtPin(1, 2, 0, "pin.*"), grtPin(2, 99, 0, "gone.*")})
	grtWant(t, r, "pin.x", nil, true)
	grtWant(t, r, "gone.x", nil, true)
	grtWant(t, r, "alt.x", []int64{1}, false)
	if r.Allowed(1, "pin.x") || r.Allowed(3, "pin.x") || r.Allowed(2, "pin.x") {
		t.Error("Allowed: a group pinned to a disabled provider has no provider")
	}
	if got := r.Participants(); !reflect.DeepEqual(got, []int64{1, 3}) {
		t.Errorf("Participants = %v, want [1 3] (disabled pin target left out)", got)
	}

	// With a second, enabled pin target the disabled one is only dropped.
	r = grtRouter(t, provs, []*models.GroupRoute{grtPin(1, 2, 0, "pin.*"), grtPin(2, 1, 5, "pin.*")})
	grtWant(t, r, "pin.x", []int64{1}, true)
}

// TestGrtDisabledRoute: a disabled pin is ignored.
func TestGrtDisabledRoute(t *testing.T) {
	pin := grtPin(1, 2, 0, "alt.*")
	pin.Enabled = false
	r := grtRouter(t, []*models.Provider{grtProv(1, 0, true, ""), grtProv(2, 1, true, "")}, []*models.GroupRoute{pin})
	grtWant(t, r, "alt.x", []int64{1}, false)
	if got := r.Participants(); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("Participants = %v, want [1]", got)
	}
}

// TestGrtInvalidPatterns: invalid routing of an enabled provider or an enabled pin refuses the
// router; a disabled provider's or pin's routing is not looked at.
func TestGrtInvalidPatterns(t *testing.T) {
	if _, err := NewGroupRouter([]*models.Provider{grtProv(1, 0, true, "@alt.*")}, nil); err == nil {
		t.Error("NewGroupRouter accepted '@' in an enabled provider's routing")
	}
	if _, err := NewGroupRouter([]*models.Provider{grtProv(1, 0, true, "!alt.*")}, nil); err == nil {
		t.Error("NewGroupRouter accepted an only-'!' routing")
	}
	if _, err := NewGroupRouter([]*models.Provider{grtProv(1, 0, true, "")}, []*models.GroupRoute{grtPin(1, 1, 0, "@x.*")}); err == nil {
		t.Error("NewGroupRouter accepted '@' in an enabled pin")
	}
	if _, err := NewGroupRouter([]*models.Provider{grtProv(1, 0, true, "")}, []*models.GroupRoute{grtPin(1, 1, 0, " ")}); err == nil {
		t.Error("NewGroupRouter accepted an enabled pin with empty patterns")
	}
	off := grtPin(1, 1, 0, "@x.*")
	off.Enabled = false
	if _, err := NewGroupRouter([]*models.Provider{grtProv(1, 0, true, ""), grtProv(2, 1, false, "@bad")}, []*models.GroupRoute{off, nil}); err != nil {
		t.Errorf("NewGroupRouter refused disabled entries: %v", err)
	}
}

// TestGrtNilRouter: the methods of a nil router answer "nothing".
func TestGrtNilRouter(t *testing.T) {
	var r *GroupRouter
	if ids, pinned := r.Candidates("alt.x"); ids != nil || pinned {
		t.Error("nil router: Candidates not empty")
	}
	if r.Allowed(1, "alt.x") || r.Participants() != nil {
		t.Error("nil router: Allowed/Participants not empty")
	}
	if _, ok := r.Primary(); ok {
		t.Error("nil router: Primary ok")
	}
}
