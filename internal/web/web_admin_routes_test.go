package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// Tests of the fetcher routing on the admin providers tab (plan fetcher-group-routing): the
// providers.routing field and the global routing pins (/admin/routes*). Only HTTP and the
// database API are used, so the file also compiles against the wave-0 base.

// warProvider adds a provider named after prefix with the given routing and removes it (and
// every route pinned to it) at the end of the test.
func warProvider(t *testing.T, prefix, routing string, enabled bool) *models.Provider {
	t.Helper()
	db := w0DB(t)
	name := w0Name(prefix)
	if err := db.AddProvider(&models.Provider{Name: name, Host: name + ".test.invalid", Port: 119, MaxConns: 1, Priority: 900, Enabled: enabled, Routing: routing}); err != nil {
		t.Fatalf("AddProvider(%s): %v", name, err)
	}
	p, err := db.GetProviderByName(name)
	if err != nil || p == nil {
		t.Fatalf("GetProviderByName(%s) = %v, %v", name, p, err)
	}
	t.Cleanup(func() {
		warDeleteRoutesOf(t, p.ID)
		if err := db.DeleteProvider(int(p.ID)); err != nil {
			t.Errorf("DeleteProvider(%d): %v", p.ID, err)
		}
	})
	return p
}

// warRoutesOf returns the routes pinned to providerID.
func warRoutesOf(t *testing.T, providerID int64) []*models.GroupRoute {
	t.Helper()
	routes, err := w0DB(t).GetGroupRoutes()
	if err != nil {
		t.Fatalf("GetGroupRoutes: %v", err)
	}
	var out []*models.GroupRoute
	for _, r := range routes {
		if r.ProviderID == providerID {
			out = append(out, r)
		}
	}
	return out
}

// warSameRoute compares the stored fields of two routes (not created_at).
func warSameRoute(a, b *models.GroupRoute) bool {
	return a.ID == b.ID && a.Patterns == b.Patterns && a.ProviderID == b.ProviderID &&
		a.Priority == b.Priority && a.Enabled == b.Enabled && a.Notes == b.Notes
}

func warDeleteRoutesOf(t *testing.T, providerID int64) {
	t.Helper()
	for _, r := range warRoutesOf(t, providerID) {
		if err := w0DB(t).DeleteGroupRoute(r.ID); err != nil {
			t.Errorf("DeleteGroupRoute(%d): %v", r.ID, err)
		}
	}
}

// TestWarProviderRoutingCreate: the create form stores a valid routing list (normalized to
// ", " separators) and refuses '@' and invalid wildmat characters without adding a provider.
func TestWarProviderRoutingCreate(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	name := w0Name("warprov")
	form := url.Values{"name": {name}, "host": {"news.gmane.test.invalid"}, "port": {"119"},
		"routing": {"gmane.*,gwene.*\r\n!gmane.spam"}}
	resp, success, failure := adfOptPost(t, cookie, "/admin/providers", form)
	if resp.StatusCode != http.StatusSeeOther || failure != "" || !strings.Contains(success, "created successfully") {
		t.Fatalf("create: %d success=%q error=%q", resp.StatusCode, success, failure)
	}
	p, err := db.GetProviderByName(name)
	if err != nil || p == nil {
		t.Fatalf("GetProviderByName(%s) = %v, %v", name, p, err)
	}
	t.Cleanup(func() {
		if err := db.DeleteProvider(int(p.ID)); err != nil {
			t.Errorf("DeleteProvider(%d): %v", p.ID, err)
		}
	})
	if want := "gmane.*, gwene.*, !gmane.spam"; p.Routing != want {
		t.Errorf("stored routing %q, want %q", p.Routing, want)
	}

	for _, bad := range []string{"gmane.*,@alt.binaries.*", "gmane.[ab]*", "!gmane.*", "gmane.\\x"} {
		badName := w0Name("warprovbad")
		form := url.Values{"name": {badName}, "host": {"bad.test.invalid"}, "port": {"119"}, "routing": {bad}}
		_, success, failure := adfOptPost(t, cookie, "/admin/providers", form)
		if success != "" || !strings.Contains(failure, "Invalid routing") {
			t.Errorf("create with routing %q: success=%q error=%q, want an 'Invalid routing' error", bad, success, failure)
		}
		if got, err := db.GetProviderByName(badName); err != nil || got != nil {
			t.Errorf("create with routing %q added provider %+v (err %v)", bad, got, err)
			if got != nil {
				_ = db.DeleteProvider(int(got.ID))
			}
		}
	}
}

// TestWarProviderRoutingUpdate: SetProvider writes every field, so the update keeps the stored
// routing when the field is not posted, changes it when posted, clears it only when posted
// empty, and refuses an invalid list without touching the row.
func TestWarProviderRoutingUpdate(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	p := warProvider(t, "warprovupd", "gmane.*, gwene.*", false)
	id := fmt.Sprint(p.ID)
	routing := func() string {
		t.Helper()
		got, err := db.GetProviderByID(p.ID)
		if err != nil || got == nil {
			t.Fatalf("GetProviderByID(%d) = %v, %v", p.ID, got, err)
		}
		return got.Routing
	}

	// The edit form posts the field with the stored value: unchanged.
	_, success, failure := adfOptPost(t, cookie, "/admin/providers/update",
		url.Values{"id": {id}, "host": {p.Host}, "routing": {"gmane.*, gwene.*"}})
	if failure != "" || success == "" {
		t.Fatalf("update with routing: success=%q error=%q", success, failure)
	}
	if got := routing(); got != "gmane.*, gwene.*" {
		t.Errorf("after an update posting the same routing: %q", got)
	}

	// A client that does not send the field must not clear it.
	_, success, failure = adfOptPost(t, cookie, "/admin/providers/update",
		url.Values{"id": {id}, "host": {p.Host}})
	if failure != "" || success == "" {
		t.Fatalf("update without routing: success=%q error=%q", success, failure)
	}
	if got := routing(); got != "gmane.*, gwene.*" {
		t.Errorf("an update without the routing field changed routing to %q, want it kept", got)
	}

	// Changed.
	_, success, failure = adfOptPost(t, cookie, "/admin/providers/update",
		url.Values{"id": {id}, "host": {p.Host}, "routing": {"tin.* !tin.it.* ka.* lists.*"}})
	if failure != "" || success == "" {
		t.Fatalf("update with new routing: success=%q error=%q", success, failure)
	}
	if got := routing(); got != "tin.*, !tin.it.*, ka.*, lists.*" {
		t.Errorf("after the routing change: %q", got)
	}

	// Invalid: refused, unchanged.
	_, success, failure = adfOptPost(t, cookie, "/admin/providers/update",
		url.Values{"id": {id}, "host": {"changed.test.invalid"}, "routing": {"tin.*,@tin.it.*"}})
	if success != "" || !strings.Contains(failure, "Invalid routing") {
		t.Errorf("update with '@': success=%q error=%q, want an 'Invalid routing' error", success, failure)
	}
	if got, err := db.GetProviderByID(p.ID); err != nil || got == nil || got.Host != p.Host || got.Routing != "tin.*, !tin.it.*, ka.*, lists.*" {
		t.Errorf("the refused update changed the provider: %+v, %v", got, err)
	}

	// Posted empty: cleared.
	_, success, failure = adfOptPost(t, cookie, "/admin/providers/update",
		url.Values{"id": {id}, "host": {p.Host}, "routing": {""}})
	if failure != "" || success == "" {
		t.Fatalf("update with empty routing: success=%q error=%q", success, failure)
	}
	if got := routing(); got != "" {
		t.Errorf("after posting an empty routing field: %q, want empty", got)
	}
}

// TestWarGroupRoutesCRUD: add, edit, toggle and delete a global route through the admin
// routes; bad patterns, a missing provider and unknown ids are refused with a message.
func TestWarGroupRoutesCRUD(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	p := warProvider(t, "warroute", "", true)
	p2 := warProvider(t, "warroute2", "", false)
	pid := fmt.Sprint(p.ID)

	resp, success, failure := adfOptPost(t, cookie, "/admin/routes",
		url.Values{"patterns": {"pin.*,\n!pin.local"}, "provider_id": {pid}, "priority": {"3"}, "enabled": {"on"}, "notes": {" pinned <b>here</b> "}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin?tab=providers" || failure != "" || success == "" {
		t.Fatalf("add route: %d %q success=%q error=%q", resp.StatusCode, resp.Header.Get("Location"), success, failure)
	}
	routes := warRoutesOf(t, p.ID)
	if len(routes) != 1 {
		t.Fatalf("%d routes for provider %d after the add, want 1", len(routes), p.ID)
	}
	r := routes[0]
	if r.Patterns != "pin.*, !pin.local" || r.Priority != 3 || !r.Enabled || r.Notes != "pinned <b>here</b>" {
		t.Errorf("stored route %+v", r)
	}
	rid := fmt.Sprint(r.ID)

	// Refused adds: nothing stored.
	refused := []struct {
		form url.Values
		want string
	}{
		{url.Values{"patterns": {"pin.*,@alt.*"}, "provider_id": {pid}}, "Invalid route patterns"},
		{url.Values{"patterns": {"pin.[x]"}, "provider_id": {pid}}, "Invalid route patterns"},
		{url.Values{"patterns": {"!pin.*"}, "provider_id": {pid}}, "Invalid route patterns"},
		{url.Values{"patterns": {"  "}, "provider_id": {pid}}, "Invalid route"},
		{url.Values{"patterns": {"pin.*"}, "provider_id": {""}}, "Select a provider"},
		{url.Values{"patterns": {"pin.*"}, "provider_id": {"987654321"}}, "Provider not found"},
		{url.Values{"patterns": {"pin.*"}, "provider_id": {pid}, "priority": {"-1"}}, "Invalid route priority"},
		{url.Values{"patterns": {"pin.*"}, "provider_id": {pid}, "notes": {strings.Repeat("n", database.MaxGroupRouteNotes+1)}}, "notes"},
	}
	for _, tc := range refused {
		_, success, failure := adfOptPost(t, cookie, "/admin/routes", tc.form)
		if success != "" || !strings.Contains(failure, tc.want) {
			t.Errorf("add %v: success=%q error=%q, want %q", tc.form, success, failure, tc.want)
		}
	}
	if n := len(warRoutesOf(t, p.ID)); n != 1 {
		t.Errorf("%d routes after the refused adds, want 1", n)
	}

	// Edit: every field, moved to the second provider; the unticked box disables it.
	_, success, failure = adfOptPost(t, cookie, "/admin/routes/update",
		url.Values{"id": {rid}, "patterns": {"pin.* other.*"}, "provider_id": {fmt.Sprint(p2.ID)}, "priority": {"7"}, "notes": {"moved"}})
	if failure != "" || success == "" {
		t.Fatalf("edit route: success=%q error=%q", success, failure)
	}
	got, err := w0DB(t).GetGroupRouteByID(r.ID)
	if err != nil {
		t.Fatalf("GetGroupRouteByID(%d): %v", r.ID, err)
	}
	if got.Patterns != "pin.*, other.*" || got.ProviderID != p2.ID || got.Priority != 7 || got.Enabled || got.Notes != "moved" {
		t.Errorf("after the edit: %+v", got)
	}

	// Edit with a bad pattern: refused, unchanged.
	_, success, failure = adfOptPost(t, cookie, "/admin/routes/update",
		url.Values{"id": {rid}, "patterns": {"pin.*,@x"}, "provider_id": {pid}, "priority": {"1"}, "enabled": {"on"}})
	if success != "" || !strings.Contains(failure, "Invalid route patterns") {
		t.Errorf("edit with '@': success=%q error=%q", success, failure)
	}
	if again, err := w0DB(t).GetGroupRouteByID(r.ID); err != nil || !warSameRoute(again, got) {
		t.Errorf("the refused edit changed the route: %+v (err %v), want %+v", again, err, got)
	}

	// Toggle twice.
	for i, want := range []bool{true, false} {
		if _, success, failure := adfOptPost(t, cookie, "/admin/routes/toggle", url.Values{"id": {rid}}); failure != "" || success == "" {
			t.Fatalf("toggle #%d: success=%q error=%q", i, success, failure)
		}
		got, err := w0DB(t).GetGroupRouteByID(r.ID)
		if err != nil || got.Enabled != want {
			t.Errorf("after toggle #%d: %+v, %v; want enabled=%v", i, got, err, want)
		}
	}

	// Unknown and invalid ids.
	for _, path := range []string{"/admin/routes/update", "/admin/routes/delete", "/admin/routes/toggle"} {
		_, success, failure := adfOptPost(t, cookie, path, url.Values{"id": {"987654321"}, "patterns": {"pin.*"}, "provider_id": {pid}})
		if success != "" || !strings.Contains(failure, "not found") {
			t.Errorf("%s with an unknown id: success=%q error=%q, want 'not found'", path, success, failure)
		}
		_, success, failure = adfOptPost(t, cookie, path, url.Values{"id": {"x"}})
		if success != "" || !strings.Contains(failure, "Invalid route ID") {
			t.Errorf("%s with an invalid id: success=%q error=%q", path, success, failure)
		}
	}

	// Delete.
	if _, success, failure := adfOptPost(t, cookie, "/admin/routes/delete", url.Values{"id": {rid}}); failure != "" || success == "" {
		t.Fatalf("delete route: success=%q error=%q", success, failure)
	}
	if n := len(warRoutesOf(t, p.ID)) + len(warRoutesOf(t, p2.ID)); n != 0 {
		t.Errorf("%d routes left after the delete", n)
	}
}

// TestWarProviderDeletePinned: a provider that a global route pins groups to is not deleted;
// the admin gets a message that says why. Without the route the delete works.
func TestWarProviderDeletePinned(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	name := w0Name("warprovpin")
	if err := db.AddProvider(&models.Provider{Name: name, Host: "pin.test.invalid", Port: 119, MaxConns: 1, Priority: 900}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	p, err := db.GetProviderByName(name)
	if err != nil || p == nil {
		t.Fatalf("GetProviderByName(%s) = %v, %v", name, p, err)
	}
	t.Cleanup(func() {
		warDeleteRoutesOf(t, p.ID)
		if got, _ := db.GetProviderByID(p.ID); got != nil {
			if err := db.DeleteProvider(int(p.ID)); err != nil {
				t.Errorf("DeleteProvider(%d): %v", p.ID, err)
			}
		}
	})
	if _, err := db.InsertGroupRoute(&models.GroupRoute{Patterns: "pin.*", ProviderID: p.ID, Enabled: true}); err != nil {
		t.Fatalf("InsertGroupRoute: %v", err)
	}

	_, success, failure := adfOptPost(t, cookie, "/admin/providers/delete", url.Values{"id": {fmt.Sprint(p.ID)}})
	if success != "" || !strings.Contains(failure, "global routing") {
		t.Errorf("delete of a pinned provider: success=%q error=%q, want a message about global routing", success, failure)
	}
	if got, err := db.GetProviderByID(p.ID); err != nil || got == nil {
		t.Fatalf("the pinned provider is gone: %v, %v", got, err)
	}
	if err := db.DeleteProvider(int(p.ID)); !errors.Is(err, database.ErrProviderHasRoutes) {
		t.Errorf("DeleteProvider of a pinned provider = %v, want ErrProviderHasRoutes", err)
	}

	warDeleteRoutesOf(t, p.ID)
	_, success, failure = adfOptPost(t, cookie, "/admin/providers/delete", url.Values{"id": {fmt.Sprint(p.ID)}})
	if failure != "" || !strings.Contains(success, "deleted") {
		t.Errorf("delete without routes: success=%q error=%q", success, failure)
	}
	if got, err := db.GetProviderByID(p.ID); err != nil || got != nil {
		t.Errorf("provider still there after the delete: %+v, %v", got, err)
	}
}

// TestWarGroupRoutesAccess: the route handlers need an admin session like the provider
// handlers (plain user -> /profile, anonymous -> /login), and the CSRF layer rejects a
// cross-site POST before the handler. No refused request changes the routes.
func TestWarGroupRoutesAccess(t *testing.T) {
	db := w0DB(t)
	_, admin := w0NewUser(t, true)
	_, plain := w0NewUser(t, false)
	p := warProvider(t, "warrouteacc", "", true)
	if _, err := db.InsertGroupRoute(&models.GroupRoute{Patterns: "acc.*", ProviderID: p.ID, Priority: 1, Enabled: true, Notes: "keep"}); err != nil {
		t.Fatalf("InsertGroupRoute: %v", err)
	}
	before := warRoutesOf(t, p.ID)
	if len(before) != 1 {
		t.Fatalf("%d routes, want 1", len(before))
	}
	rid, pid := fmt.Sprint(before[0].ID), fmt.Sprint(p.ID)
	posts := []struct {
		path string
		form url.Values
	}{
		{"/admin/routes", url.Values{"patterns": {"evil.*"}, "provider_id": {pid}, "enabled": {"on"}}},
		{"/admin/routes/update", url.Values{"id": {rid}, "patterns": {"evil.*"}, "provider_id": {pid}}},
		{"/admin/routes/delete", url.Values{"id": {rid}}},
		{"/admin/routes/toggle", url.Values{"id": {rid}}},
	}
	for _, tc := range posts {
		rec := w0Do(t, w0Req{Method: http.MethodPost, Path: tc.path, Form: tc.form, Cookies: []*http.Cookie{plain}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/profile" {
			t.Errorf("POST %s as a plain user = %d %q, want 303 /profile", tc.path, rec.Code, rec.Header().Get("Location"))
		}
		GetAndClearFlash(plain.Value, "error")

		rec = w0Do(t, w0Req{Method: http.MethodPost, Path: tc.path, Form: tc.form})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("POST %s anonymously = %d %q, want 303 /login", tc.path, rec.Code, rec.Header().Get("Location"))
		}

		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example")
		req.AddCookie(admin)
		crec := httptest.NewRecorder()
		w0Srv.rootHandler().ServeHTTP(crec, req)
		if crec.Code != http.StatusForbidden {
			t.Errorf("cross-site POST %s as admin = %d, want 403", tc.path, crec.Code)
		}
	}
	after := warRoutesOf(t, p.ID)
	if len(after) != 1 || !warSameRoute(after[0], before[0]) {
		t.Errorf("refused requests changed the routes: %+v, want %+v", after, before)
	}
}

// TestWarProvidersTabRenders: the providers tab shows the routing column, the routing field in
// the edit and create forms, the global routing card with its route and the newsdeef preset
// menu including the tin.it.* note; route values are escaped.
func TestWarProvidersTabRenders(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	p := warProvider(t, "warprovtab", "gmane.*, gwene.*", true)
	if _, err := db.InsertGroupRoute(&models.GroupRoute{Patterns: "tabpin.*", ProviderID: p.ID, Priority: 2, Enabled: true, Notes: "<script>x</script>"}); err != nil {
		t.Fatalf("InsertGroupRoute: %v", err)
	}
	rec := w0Do(t, w0Req{Path: "/admin?tab=providers", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin?tab=providers = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<code>gmane.*, gwene.*</code>`,
		`name="routing" rows="2"`,
		`id="provider_routing" name="routing"`,
		`>gmane.*, gwene.*</textarea>`,
		`Global routing`,
		`action="/admin/routes?tab=providers"`,
		`action="/admin/routes/update?tab=providers"`,
		`action="/admin/routes/delete?tab=providers"`,
		`action="/admin/routes/toggle?tab=providers"`,
		`<code>tabpin.*</code>`,
		`&lt;script&gt;x&lt;/script&gt;`,
		`id="provider_preset"`,
		`value="news.gmane.io" data-name="news.gmane.io" data-routing="gmane.*, gwene.*"`,
		`data-routing="digitalmars.*, D, D.gnu"`,
		`tin.it.*`,
		`the primary is the first enabled provider`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("providers tab lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>x</script>") {
		t.Error("route notes rendered unescaped")
	}
	if strings.Contains(body, "will only fetch from first enabled provider") {
		t.Error("providers tab still shows the old fetcher text")
	}
	// The pinned provider's option is selected in the route edit form.
	if !strings.Contains(body, fmt.Sprintf(`<option value="%d" selected>%s</option>`, p.ID, p.Name)) {
		t.Errorf("route edit form does not select provider %d", p.ID)
	}
}

// TestWarRoutingBacklogNotice: saving a provider's routing or a global route tells how many
// existing groups with articles (message_count > 0) the patterns want, matched as the fetcher's
// router does, and that their older articles on that provider are not fetched automatically.
func TestWarRoutingBacklogNotice(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	base := w0Name("warbl") // unique prefix: other tests' groups never match
	for name, count := range map[string]int64{
		base + ".a": 5, base + ".b": 1, base + ".skip": 3, base + ".empty": 0, w0Name("warblother") + ".a": 9,
	} {
		if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, MessageCount: count, Status: "y", LowWater: 1}); err != nil {
			t.Fatalf("InsertNewsgroup(%s): %v", name, err)
		}
		t.Cleanup(func() {
			if _, err := db.DeleteNewsgroup(name); err != nil {
				t.Errorf("DeleteNewsgroup(%s): %v", name, err)
			}
		})
	}
	notice := func(n int, provider string) string {
		return fmt.Sprintf(" Note: the patterns match %d existing groups that already have articles. Older articles of these groups on provider '%s' are not fetched automatically (use pugleaf-fetcher -download-start-date YYYY-MM-DD -group <groups> -provider '%s').", n, provider, provider)
	}

	// the count alone: last match wins, empty groups and empty patterns do not count
	groups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		t.Fatalf("MainDBGetAllNewsgroups: %v", err)
	}
	for patterns, want := range map[string]int{
		base + ".*":                       3,
		base + ".*, !" + base + ".skip":   2,
		base + ".a " + base + ".empty":    1,
		"!" + base + ".a, " + base + ".*": 3,
		"":                                0,
	} {
		if got := routingBacklogCount(groups, patterns); got != want {
			t.Errorf("routingBacklogCount(%q) = %d, want %d", patterns, got, want)
		}
	}

	// provider create: with matching routing the notice, without a match none
	name := w0Name("warblprov")
	_, success, failure := adfOptPost(t, cookie, "/admin/providers", url.Values{"name": {name}, "host": {"bl.test.invalid"}, "port": {"119"},
		"routing": {base + ".*, !" + base + ".skip"}})
	if failure != "" || success != "Provider created successfully"+notice(2, name) {
		t.Fatalf("create with routing: success=%q error=%q", success, failure)
	}
	p, err := db.GetProviderByName(name)
	if err != nil || p == nil {
		t.Fatalf("GetProviderByName(%s) = %v, %v", name, p, err)
	}
	t.Cleanup(func() {
		warDeleteRoutesOf(t, p.ID)
		if err := db.DeleteProvider(int(p.ID)); err != nil {
			t.Errorf("DeleteProvider(%d): %v", p.ID, err)
		}
	})
	quiet := w0Name("warblquiet")
	_, success, failure = adfOptPost(t, cookie, "/admin/providers", url.Values{"name": {quiet}, "host": {"blq.test.invalid"}, "port": {"119"},
		"routing": {base + ".empty"}})
	if failure != "" || success != "Provider created successfully" {
		t.Errorf("create with routing matching only an empty group: success=%q error=%q", success, failure)
	}
	if q, err := db.GetProviderByName(quiet); err == nil && q != nil {
		if err := db.DeleteProvider(int(q.ID)); err != nil {
			t.Errorf("DeleteProvider(%d): %v", q.ID, err)
		}
	}

	// provider update: unchanged routing no notice, changed routing the new count
	id := fmt.Sprint(p.ID)
	_, success, failure = adfOptPost(t, cookie, "/admin/providers/update", url.Values{"id": {id}, "host": {p.Host}, "routing": {p.Routing}})
	if failure != "" || success != "Provider updated successfully" {
		t.Errorf("update with the same routing: success=%q error=%q", success, failure)
	}
	_, success, failure = adfOptPost(t, cookie, "/admin/providers/update", url.Values{"id": {id}, "host": {p.Host}, "routing": {base + ".a"}})
	if failure != "" || success != "Provider updated successfully"+notice(1, name) {
		t.Errorf("update with new routing: success=%q error=%q", success, failure)
	}

	// global route add and edit
	_, success, failure = adfOptPost(t, cookie, "/admin/routes", url.Values{"patterns": {base + ".*"}, "provider_id": {id}, "enabled": {"on"}})
	if failure != "" || success != "Route added: "+base+".*"+notice(3, name) {
		t.Fatalf("add route: success=%q error=%q", success, failure)
	}
	routes := warRoutesOf(t, p.ID)
	if len(routes) != 1 {
		t.Fatalf("%d routes for provider %d, want 1", len(routes), p.ID)
	}
	rid := fmt.Sprint(routes[0].ID)
	_, success, failure = adfOptPost(t, cookie, "/admin/routes/update", url.Values{"id": {rid}, "patterns": {base + ".b"}, "provider_id": {id}, "enabled": {"on"}})
	if failure != "" || success != "Route updated: "+base+".b"+notice(1, name) {
		t.Errorf("edit route: success=%q error=%q", success, failure)
	}
	_, success, failure = adfOptPost(t, cookie, "/admin/routes/update", url.Values{"id": {rid}, "patterns": {base + ".empty"}, "provider_id": {id}, "enabled": {"on"}})
	if failure != "" || success != "Route updated: "+base+".empty" {
		t.Errorf("edit route to an empty group: success=%q error=%q", success, failure)
	}
}
