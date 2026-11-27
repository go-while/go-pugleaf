package main

// Tests of the fetcher routing slice (plan fetcher-group-routing): connection caps, -group
// splitting, the per-run assignment against fake LIST maps, -provider selection, the list
// sync filter and the dry-run output. No database and no network.

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

func routeNames(groups []*models.Newsgroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Name)
	}
	return out
}

func routeGroups(names ...string) []*models.Newsgroup {
	out := make([]*models.Newsgroup, 0, len(names))
	for i, n := range names {
		out = append(out, &models.Newsgroup{ID: int64(i + 1), Name: n, Active: true})
	}
	return out
}

func routeList(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func routeProvider(id int64, name string, prio int, enabled bool, routing string) *models.Provider {
	return &models.Provider{ID: id, Name: name, Host: name + ".example", Port: 119, MaxConns: 4,
		Priority: prio, Enabled: enabled, Routing: routing}
}

func routeRouter(t *testing.T, providers []*models.Provider, routes []*models.GroupRoute) *nntp.GroupRouter {
	t.Helper()
	r, err := nntp.NewGroupRouter(routerProviders(providers, fetchUsable), routes)
	if err != nil {
		t.Fatalf("NewGroupRouter: %v", err)
	}
	return r
}

// TestRouteCapsUnchanged: the hardcoded connection caps keep their values and host matches.
func TestRouteCapsUnchanged(t *testing.T) {
	cases := []struct {
		host string
		max  int
		want int
	}{
		{"news.eternal-september.org", 10, 3},
		{"news.eternal-september.org", 3, 3},
		{"news.eternal-september.org", 2, 2},
		{ET_V4, 50, 3},
		{ET_V6, 50, 3},
		{"news.blueworldhosting.com", 50, 16},
		{"news.blueworldhosting.com", 16, 16},
		{"news.blueworldhosting.com", 8, 8},
		{BW_V4_Prefix, 50, 16},
		{BW_V4_Prefix + "6", 50, 16}, // prefix match, as before
		{"news.gmane.io", 50, 50},
		{"157.180.91.227", 50, 50},
	}
	for _, c := range cases {
		p := &models.Provider{Host: c.host, MaxConns: c.max}
		if got := capProviderConns(p); got != c.want {
			t.Errorf("capProviderConns(%q, %d) = %d, want %d", c.host, c.max, got, c.want)
		}
		if p.MaxConns != c.max {
			t.Errorf("capProviderConns changed p.MaxConns of %q to %d", c.host, p.MaxConns)
		}
	}
}

// TestRouteGroupSelectionSplit: -group takes several patterns separated by space, ',' or ';'.
// Before routing 'tin.*,lists.*' became the single prefix 'tin.*,lists.' and matched nothing.
func TestRouteGroupSelectionSplit(t *testing.T) {
	db := routeGroups("alt.test", "lists.go", "tin.misc", "tin.it.news", "de.test", "tinker.x")

	sel := parseGroupSelection("tin.*,lists.*")
	if sel.all || !reflect.DeepEqual(sel.prefixes, []string{"tin.", "lists."}) || len(sel.exact) != 0 {
		t.Fatalf("parse 'tin.*,lists.*' = %+v", sel)
	}
	if got := routeNames(selectNewsgroups(db, sel)); !reflect.DeepEqual(got, []string{"lists.go", "tin.misc", "tin.it.news"}) {
		t.Fatalf("select 'tin.*,lists.*' = %v", got)
	}

	sel = parseGroupSelection(" tin.* ; de.test, alt.test\tnot.in.db ;;, de.test")
	if !reflect.DeepEqual(sel.prefixes, []string{"tin."}) || !reflect.DeepEqual(sel.exact, []string{"de.test", "alt.test", "not.in.db"}) {
		t.Fatalf("parse mixed = %+v", sel)
	}
	if !sel.needsDB() {
		t.Fatalf("a prefix selection needs the database list")
	}
	// database order first, then the exact names the database lacks (bare entries)
	got := selectNewsgroups(db, sel)
	if names := routeNames(got); !reflect.DeepEqual(names, []string{"alt.test", "tin.misc", "tin.it.news", "de.test", "not.in.db"}) {
		t.Fatalf("select mixed = %v", names)
	}
	if got[len(got)-1].ID != 0 {
		t.Fatalf("a name missing in the database must stay a bare entry: %+v", got[len(got)-1])
	}

	// exact names only: no database list needed, given order kept
	sel = parseGroupSelection("b.two a.one")
	if sel.needsDB() || sel.all {
		t.Fatalf("exact names: needsDB=%t all=%t", sel.needsDB(), sel.all)
	}
	if names := routeNames(selectNewsgroups(nil, sel)); !reflect.DeepEqual(names, []string{"b.two", "a.one"}) {
		t.Fatalf("exact names = %v", names)
	}

	// 'x*' is a prefix without the dot, as before
	if names := routeNames(selectNewsgroups(db, parseGroupSelection("tin*"))); !reflect.DeepEqual(names, []string{"tin.misc", "tin.it.news", "tinker.x"}) {
		t.Fatalf("select 'tin*' = %v", names)
	}

	for _, all := range []string{"", "   ", "$all", "*", "alt.* $all", ",;"} {
		sel := parseGroupSelection(all)
		if !sel.all {
			t.Errorf("parse %q: all=false", all)
		}
		if got := selectNewsgroups(db, sel); len(got) != len(db) {
			t.Errorf("select %q: %d groups, want all %d", all, len(got), len(db))
		}
	}
}

// TestRouteActiveSelection: the -fetch-active-only selection uses the loaded flag, looks up
// bare entries and keeps a group whose lookup fails (the feeder logs it).
func TestRouteActiveSelection(t *testing.T) {
	groups := []*models.Newsgroup{
		{ID: 1, Name: "a.on", Active: true},
		{ID: 2, Name: "a.off", Active: false},
		{Name: "bare.on"},
		{Name: "bare.off"},
		{Name: "bare.broken"},
	}
	lookup := func(name string) (*models.Newsgroup, error) {
		switch name {
		case "bare.on":
			return &models.Newsgroup{Name: name, Active: true}, nil
		case "bare.off":
			return &models.Newsgroup{Name: name, Active: false}, nil
		case "a.on", "a.off":
			t.Errorf("lookup of a database-loaded group %q", name)
		}
		return nil, errors.New("no such group")
	}
	if got := routeNames(activeSelection(groups, true, lookup)); !reflect.DeepEqual(got, []string{"a.on", "bare.on", "bare.broken"}) {
		t.Fatalf("active only = %v", got)
	}
	if got := routeNames(activeSelection(groups, false, lookup)); !reflect.DeepEqual(got, []string{"a.off", "bare.off", "bare.broken"}) {
		t.Fatalf("inactive only = %v", got)
	}
}

// TestRouteAssign: routed provider first, primary fallback, pins without fallback, a disabled
// pin target skips its groups, a failed LIST carries nothing.
func TestRouteAssign(t *testing.T) {
	providers := []*models.Provider{
		routeProvider(1, "primary", 0, true, ""),
		routeProvider(2, "gmane", 1, true, "gmane.*,gwene.*"),
		routeProvider(3, "pinned", 2, true, ""),
		routeProvider(4, "dead", 3, false, ""),
		routeProvider(5, "spare", 4, true, ""), // enabled, empty routing, no pin: unused
	}
	routes := []*models.GroupRoute{
		{ID: 1, Patterns: "pin.*", ProviderID: 3, Enabled: true},
		{ID: 2, Patterns: "dead.*", ProviderID: 4, Enabled: true},
	}
	router := routeRouter(t, providers, routes)
	if got := router.Participants(); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("participants = %v, want [1 2 3]", got)
	}
	groups := routeGroups("alt.test", "gmane.test", "gmane.onlyprimary", "gwene.feed", "pin.ok", "pin.missing", "dead.test", "nolist.test")
	lists := map[int64]map[string]bool{
		1: routeList("alt.test", "gmane.test", "gmane.onlyprimary", "pin.missing", "dead.test", "gwene.feed"),
		2: routeList("gmane.test", "gwene.feed"),
		3: routeList("pin.ok"),
	}
	a := assignGroups(groups, router, lists)
	if got := routeNames(a.byProvider[1]); !reflect.DeepEqual(got, []string{"alt.test", "gmane.onlyprimary"}) {
		t.Errorf("primary got %v", got)
	}
	if got := routeNames(a.byProvider[2]); !reflect.DeepEqual(got, []string{"gmane.test", "gwene.feed"}) {
		t.Errorf("gmane got %v", got)
	}
	if got := routeNames(a.byProvider[3]); !reflect.DeepEqual(got, []string{"pin.ok"}) {
		t.Errorf("pinned got %v", got)
	}
	if len(a.byProvider[5]) != 0 || len(a.byProvider[4]) != 0 {
		t.Errorf("unused/disabled providers got groups: %v %v", routeNames(a.byProvider[5]), routeNames(a.byProvider[4]))
	}
	// pin.missing is listed by the primary but pinned to 'pinned': never fetched elsewhere;
	// dead.test is pinned to a disabled provider
	if !reflect.DeepEqual(a.pinSkipped, []string{"pin.missing", "dead.test"}) {
		t.Errorf("pinSkipped = %v", a.pinSkipped)
	}
	if !reflect.DeepEqual(a.notListed[1], []string{"nolist.test"}) || len(a.notListed) != 1 {
		t.Errorf("notListed = %v", a.notListed)
	}
	if len(a.noCandidate) != 0 {
		t.Errorf("noCandidate = %v", a.noCandidate)
	}
	if len(a.decisions) != len(groups) {
		t.Fatalf("%d decisions for %d groups", len(a.decisions), len(groups))
	}

	// the routed provider's LIST failed: its unpinned groups fall back to the primary
	delete(lists, 2)
	a = assignGroups(groups, router, lists)
	if got := routeNames(a.byProvider[1]); !reflect.DeepEqual(got, []string{"alt.test", "gmane.test", "gmane.onlyprimary", "gwene.feed"}) {
		t.Errorf("primary after gmane LIST failure got %v", got)
	}
	if len(a.byProvider[2]) != 0 {
		t.Errorf("gmane without LIST got %v", routeNames(a.byProvider[2]))
	}
}

// TestRouteAssignNoRouting: without routing and pins every group goes to the first enabled
// usable provider (the fetcher's behaviour before routing); an unusable first provider is
// passed over as before.
func TestRouteAssignNoRouting(t *testing.T) {
	noHost := routeProvider(1, "nohost", 0, true, "")
	noHost.Host = ""
	providers := []*models.Provider{
		noHost,
		routeProvider(2, "first", 1, true, ""),
		routeProvider(3, "second", 2, true, ""),
	}
	router := routeRouter(t, providers, nil)
	if got := router.Participants(); !reflect.DeepEqual(got, []int64{2}) {
		t.Fatalf("participants = %v, want [2]", got)
	}
	if !noHost.Enabled {
		t.Fatalf("routerProviders changed the caller's provider")
	}
	groups := routeGroups("a.one", "b.two", "c.missing")
	lists := map[int64]map[string]bool{2: routeList("a.one", "b.two"), 3: routeList("a.one", "b.two", "c.missing")}
	a := assignGroups(groups, router, lists)
	if got := routeNames(a.byProvider[2]); !reflect.DeepEqual(got, []string{"a.one", "b.two"}) {
		t.Fatalf("first got %v", got)
	}
	if !reflect.DeepEqual(a.notListed[2], []string{"c.missing"}) || len(a.byProvider[3]) != 0 {
		t.Fatalf("c.missing must be dropped as before, not fetched from 'second': notListed=%v second=%v", a.notListed, routeNames(a.byProvider[3]))
	}

	// only routed providers, no primary: groups no routing wants have no provider
	router = routeRouter(t, []*models.Provider{routeProvider(7, "gmane", 0, true, "gmane.*")}, nil)
	a = assignGroups(routeGroups("gmane.x", "alt.x"), router, map[int64]map[string]bool{7: routeList("gmane.x", "alt.x")})
	if got := routeNames(a.byProvider[7]); !reflect.DeepEqual(got, []string{"gmane.x"}) || !reflect.DeepEqual(a.noCandidate, []string{"alt.x"}) {
		t.Fatalf("no primary: gmane=%v noCandidate=%v", got, a.noCandidate)
	}
}

// TestRouteProviderFlag: -provider picks by name from GetProviders, refuses unknown, disabled
// and unused providers; -reset-progress takes any named provider, else the primary.
func TestRouteProviderFlag(t *testing.T) {
	providers := []*models.Provider{
		routeProvider(1, "primary", 0, true, ""),
		routeProvider(2, "gmane", 1, true, "gmane.*"),
		routeProvider(3, "spare", 2, true, ""),
		routeProvider(4, "off", 3, false, ""),
	}
	router := routeRouter(t, providers, nil)
	if p, err := resolveProviderFlag(providers, router, "gmane"); err != nil || p.ID != 2 {
		t.Fatalf("gmane: %v %v", p, err)
	}
	if p, err := resolveProviderFlag(providers, router, "primary"); err != nil || p.ID != 1 {
		t.Fatalf("primary: %v %v", p, err)
	}
	for _, bad := range []string{"nope", "off", "spare", "Gmane"} {
		if p, err := resolveProviderFlag(providers, router, bad); err == nil {
			t.Errorf("-provider %q accepted: %+v", bad, p)
		}
	}
	if p, err := primaryProvider(providers, router); err != nil || p.ID != 1 {
		t.Fatalf("primaryProvider: %v %v", p, err)
	}
	if p, err := resetProgressProvider(providers, router, ""); err != nil || p.ID != 1 {
		t.Fatalf("reset default: %v %v", p, err)
	}
	if p, err := resetProgressProvider(providers, router, "off"); err != nil || p.ID != 4 {
		t.Fatalf("reset -provider off: %v %v", p, err)
	}
	if _, err := resetProgressProvider(providers, router, "nope"); err == nil {
		t.Fatalf("reset -provider nope accepted")
	}
	onlyRouted := []*models.Provider{routeProvider(2, "gmane", 1, true, "gmane.*")}
	if _, err := primaryProvider(onlyRouted, routeRouter(t, onlyRouted, nil)); err == nil {
		t.Fatalf("primaryProvider without a primary must fail")
	}

	// a provider without credentials is found by name (GetProviderByName fails on NULLs)
	providers[1].Username, providers[1].Password = "", ""
	if p := findProviderByName(providers, "gmane"); p == nil || p.ID != 2 {
		t.Fatalf("findProviderByName: %+v", p)
	}
}

// TestRouteListSyncFilter: the list sync adds only groups the chosen provider may serve.
func TestRouteListSyncFilter(t *testing.T) {
	providers := []*models.Provider{
		routeProvider(1, "primary", 0, true, ""),
		routeProvider(2, "gmane", 1, true, "gmane.*,!gmane.spam.*"),
		routeProvider(3, "pinned", 2, true, ""),
	}
	routes := []*models.GroupRoute{{ID: 1, Patterns: "pin.*", ProviderID: 3, Enabled: true}}
	router, err := nntp.NewGroupRouter(routerProviders(providers, listUsable), routes)
	if err != nil {
		t.Fatalf("NewGroupRouter: %v", err)
	}
	remote := []nntp.GroupInfo{{Name: "gmane.a", Count: 1}, {Name: "gmane.spam.x", Count: 2}, {Name: "alt.b", Count: 4}, {Name: "pin.c", Count: 8}}

	kept, dropped := filterAllowedGroups(remote, router, 2)
	if dropped != 3 || len(kept) != 1 || kept[0].Name != "gmane.a" {
		t.Fatalf("gmane: kept %v dropped %d", kept, dropped)
	}
	n, skipped, msgs, err := addMissingGroups(nil, kept, map[string]bool{}, true, "", nil, false, func() bool { return false })
	if err != nil || n != 1 || skipped != 0 || msgs != 1 {
		t.Fatalf("gmane addMissingGroups: new=%d skipped=%d messages=%d err=%v", n, skipped, msgs, err)
	}

	// the primary may serve everything but the pinned groups
	kept, dropped = filterAllowedGroups(remote, router, 1)
	var names []string
	for _, g := range kept {
		names = append(names, g.Name)
	}
	if dropped != 1 || !reflect.DeepEqual(names, []string{"gmane.a", "gmane.spam.x", "alt.b"}) {
		t.Fatalf("primary: kept %v dropped %d", names, dropped)
	}
	kept, _ = filterAllowedGroups(remote, router, 3)
	if len(kept) != 1 || kept[0].Name != "pin.c" {
		t.Fatalf("pinned: kept %v", kept)
	}
}

// TestRouteDryRunOutput: -routing-dry-run prints one ROUTE/SKIP line per group and a summary.
func TestRouteDryRunOutput(t *testing.T) {
	providers := []*models.Provider{
		routeProvider(1, "A", 0, true, ""),
		routeProvider(2, "B", 1, true, "gmane.*"),
		routeProvider(3, "C", 2, true, ""),
	}
	routes := []*models.GroupRoute{{ID: 1, Patterns: "pin.*", ProviderID: 3, Enabled: true}}
	router := routeRouter(t, providers, routes)
	lists := map[int64]map[string]bool{
		1: routeList("alt.test", "gmane.test", "pin.test"),
		2: routeList("gmane.test"),
	} // C's LIST failed
	a := assignGroups(routeGroups("alt.test", "gmane.test", "pin.test", "gone.test"), router, lists)
	names := map[int64]string{1: "A", 2: "B", 3: "C"}

	var buf bytes.Buffer
	if err := writeRouteDryRun(&buf, a, names, 0); err != nil {
		t.Fatalf("writeRouteDryRun: %v", err)
	}
	want := "ROUTE alt.test -> A\n" +
		"ROUTE gmane.test -> B\n" +
		"SKIP pin.test pinned: no pinned provider lists it\n" +
		"SKIP gone.test not in the LIST of A\n" +
		"[FETCHER-ROUTE] dry run: 2 groups routed, 2 skipped, no articles fetched\n"
	if buf.String() != want {
		t.Fatalf("dry run output:\n%s\nwant:\n%s", buf.String(), want)
	}

	buf.Reset()
	if err := writeRouteDryRun(&buf, a, names, 2); err != nil {
		t.Fatalf("writeRouteDryRun only B: %v", err)
	}
	if out := buf.String(); strings.Contains(out, "-> A") || !strings.Contains(out, "ROUTE gmane.test -> B\n") ||
		!strings.Contains(out, "1 groups routed, 2 skipped") {
		t.Fatalf("dry run -provider B:\n%s", out)
	}
}

// TestRouteFirstNames: skip logs list at most routeLogFirst names.
func TestRouteFirstNames(t *testing.T) {
	var many []string
	for i := 0; i < routeLogFirst+5; i++ {
		many = append(many, "g"+strings.Repeat("x", i))
	}
	if got := firstNames(many); strings.Count(got, ",") != routeLogFirst || !strings.HasSuffix(got, ", ...") {
		t.Fatalf("firstNames(%d) = %q", len(many), got)
	}
	if got := firstNames([]string{"a", "b"}); got != "a, b" {
		t.Fatalf("firstNames(2) = %q", got)
	}
}
