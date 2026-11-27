package database

// Tests of migration 0039 (plan fetcher-group-routing, wave 0): providers.routing in every
// provider query, group_routes CRUD/toggle/validation and the DeleteProvider guard.
//
// Identifiers use the grt / TestGrt prefix.

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// grtAddProvider inserts a provider with the given routing and returns the stored row; the row
// is removed at the end of the test (after the routes of the test, Cleanup is LIFO).
func grtAddProvider(t *testing.T, db *Database, routing string) *models.Provider {
	t.Helper()
	name := w0Name("routing")
	p := &models.Provider{Name: name, Host: "news.example.invalid", Port: 119, MaxConns: 1, Enabled: true, Priority: 50, Routing: routing}
	if err := db.AddProvider(p); err != nil {
		t.Fatalf("AddProvider %s: %v", name, err)
	}
	stored, err := db.GetProviderByName(name)
	if err != nil || stored == nil {
		t.Fatalf("GetProviderByName %s: %v %v", name, stored, err)
	}
	t.Cleanup(func() {
		if err := db.DeleteProvider(int(stored.ID)); err != nil {
			t.Errorf("DeleteProvider %d: %v", stored.ID, err)
		}
	})
	return stored
}

// grtRoutingEverywhere returns the routing of id as GetProviderByID, GetProviderByName and
// GetProviders read it.
func grtRoutingEverywhere(t *testing.T, db *Database, p *models.Provider) []string {
	t.Helper()
	byID, err := db.GetProviderByID(p.ID)
	if err != nil || byID == nil {
		t.Fatalf("GetProviderByID %d: %v %v", p.ID, byID, err)
	}
	byName, err := db.GetProviderByName(p.Name)
	if err != nil || byName == nil {
		t.Fatalf("GetProviderByName %s: %v %v", p.Name, byName, err)
	}
	all, err := db.GetProviders()
	if err != nil {
		t.Fatalf("GetProviders: %v", err)
	}
	inList := "<missing>"
	for _, q := range all {
		if q.ID == p.ID {
			inList = q.Routing
		}
	}
	return []string{byID.Routing, byName.Routing, inList}
}

func grtAllEqual(got []string, want string) bool {
	for _, g := range got {
		if g != want {
			return false
		}
	}
	return true
}

// TestGrtProviderRoutingRoundTrip: routing is written by AddProvider and SetProvider and read by
// every provider getter; a provider added without it reads an empty routing.
func TestGrtProviderRoutingRoundTrip(t *testing.T) {
	db := w0DB(t)
	plain := grtAddProvider(t, db, "")
	if got := grtRoutingEverywhere(t, db, plain); !grtAllEqual(got, "") {
		t.Errorf("routing of a provider added without it = %q, want ''", got)
	}

	routed := grtAddProvider(t, db, "gmane.*,gwene.*")
	if got := grtRoutingEverywhere(t, db, routed); !grtAllEqual(got, "gmane.*,gwene.*") {
		t.Errorf("routing after AddProvider = %q", got)
	}
	routed.Routing = "tin.*,!tin.it.*"
	if err := db.SetProvider(routed); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	if got := grtRoutingEverywhere(t, db, routed); !grtAllEqual(got, "tin.*,!tin.it.*") {
		t.Errorf("routing after SetProvider = %q", got)
	}
	routed.Routing = ""
	if err := db.SetProvider(routed); err != nil {
		t.Fatalf("SetProvider (clear): %v", err)
	}
	if got := grtRoutingEverywhere(t, db, routed); !grtAllEqual(got, "") {
		t.Errorf("routing after clearing = %q", got)
	}
}

// TestGrtMigration0039Existing: on a main DB from before 0039 (providers rows, no routing column,
// no group_routes) the migration applies and the existing rows read an empty routing.
func TestGrtMigration0039Existing(t *testing.T) {
	shared := w0DB(t)
	mainDB, err := sql.Open(driverNameMain, filepath.Join(t.TempDir(), "main.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	old := &Database{mainDB: mainDB, dbconfig: shared.dbconfig}
	if err := old.migrateMainDB(); err != nil {
		t.Fatalf("main migrations: %v", err)
	}
	// Turn it back into a pre-0039 database.
	for _, stmt := range []string{
		`DROP TABLE group_routes`,
		`ALTER TABLE providers DROP COLUMN routing`,
		`DELETE FROM schema_migrations WHERE filename = '0039_main_fetcher_routing.sql'`,
		`INSERT INTO providers (name, grp, host, port, ssl, max_conns, enabled, priority) VALUES ('old.provider', 'x', 'news.example.invalid', 119, 0, 2, 1, 1)`,
	} {
		if _, err := mainDB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := old.migrateMainDB(); err != nil {
		t.Fatalf("migration 0039 on an existing DB: %v", err)
	}
	p, err := old.GetProviderByName("old.provider")
	if err != nil || p == nil {
		t.Fatalf("GetProviderByName after 0039: %v %v", p, err)
	}
	if p.Routing != "" {
		t.Errorf("existing provider routing = %q, want ''", p.Routing)
	}
	var n int
	if err := mainDB.QueryRow(`SELECT COUNT(*) FROM group_routes`).Scan(&n); err != nil || n != 0 {
		t.Errorf("group_routes after 0039: %d rows, %v", n, err)
	}
	if _, err := old.InsertGroupRoute(&models.GroupRoute{Patterns: "pin.*", ProviderID: p.ID, Enabled: true}); err != nil {
		t.Errorf("InsertGroupRoute after 0039: %v", err)
	}
}

// TestGrtGroupRouteCRUD: insert, read, update, toggle and delete a route.
func TestGrtGroupRouteCRUD(t *testing.T) {
	db := w0DB(t)
	p1 := grtAddProvider(t, db, "")
	p2 := grtAddProvider(t, db, "gmane.*")

	r := &models.GroupRoute{Patterns: "  pin.*, !pin.local.*  ", ProviderID: p1.ID, Priority: 3, Enabled: true, Notes: " first "}
	id, err := db.InsertGroupRoute(r)
	if err != nil {
		t.Fatalf("InsertGroupRoute: %v", err)
	}
	t.Cleanup(func() { _ = db.DeleteGroupRoute(id) })
	if id <= 0 || r.ID != id {
		t.Fatalf("InsertGroupRoute id = %d, r.ID = %d", id, r.ID)
	}
	got, err := db.GetGroupRouteByID(id)
	if err != nil {
		t.Fatalf("GetGroupRouteByID: %v", err)
	}
	if got.Patterns != "pin.*, !pin.local.*" || got.ProviderID != p1.ID || got.Priority != 3 || !got.Enabled || got.Notes != "first" {
		t.Errorf("stored route = %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("stored route has no created_at")
	}

	got.Patterns = "pin.*"
	got.ProviderID = p2.ID
	got.Priority = -1
	got.Notes = "moved"
	if err := db.UpdateGroupRoute(got); err != nil {
		t.Fatalf("UpdateGroupRoute: %v", err)
	}
	after, err := db.GetGroupRouteByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Patterns != "pin.*" || after.ProviderID != p2.ID || after.Priority != -1 || after.Notes != "moved" || !after.Enabled {
		t.Errorf("route after update = %+v", after)
	}

	grtInEnabled := func() bool {
		t.Helper()
		rs, err := db.GetEnabledGroupRoutes()
		if err != nil {
			t.Fatalf("GetEnabledGroupRoutes: %v", err)
		}
		for _, x := range rs {
			if x.ID == id {
				return true
			}
		}
		return false
	}
	if !grtInEnabled() {
		t.Error("enabled route missing from GetEnabledGroupRoutes")
	}
	if err := db.ToggleGroupRoute(id); err != nil {
		t.Fatalf("ToggleGroupRoute: %v", err)
	}
	if x, err := db.GetGroupRouteByID(id); err != nil || x.Enabled {
		t.Errorf("after toggle: %+v %v, want disabled", x, err)
	}
	if grtInEnabled() {
		t.Error("disabled route listed by GetEnabledGroupRoutes")
	}
	all, err := db.GetGroupRoutes()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range all {
		found = found || x.ID == id
	}
	if !found {
		t.Error("disabled route missing from GetGroupRoutes")
	}
	if err := db.ToggleGroupRoute(id); err != nil {
		t.Fatalf("ToggleGroupRoute (back): %v", err)
	}
	if !grtInEnabled() {
		t.Error("route not enabled again after the second toggle")
	}

	if err := db.DeleteGroupRoute(id); err != nil {
		t.Fatalf("DeleteGroupRoute: %v", err)
	}
	if _, err := db.GetGroupRouteByID(id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetGroupRouteByID after delete: %v, want sql.ErrNoRows", err)
	}
	for name, err := range map[string]error{
		"delete": db.DeleteGroupRoute(id),
		"toggle": db.ToggleGroupRoute(id),
		"update": db.UpdateGroupRoute(after),
	} {
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("%s of a missing route: %v, want sql.ErrNoRows", name, err)
		}
	}
}

// TestGrtGroupRouteOrder: GetGroupRoutes orders by priority, then id.
func TestGrtGroupRouteOrder(t *testing.T) {
	db := w0DB(t)
	p := grtAddProvider(t, db, "")
	var ids []int64
	for _, prio := range []int{1000005, 1000001, 1000005} {
		id, err := db.InsertGroupRoute(&models.GroupRoute{Patterns: "order.*", ProviderID: p.ID, Priority: prio, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		t.Cleanup(func() { _ = db.DeleteGroupRoute(id) })
	}
	all, err := db.GetGroupRoutes()
	if err != nil {
		t.Fatal(err)
	}
	var mine []int64
	for _, r := range all {
		if r.ProviderID == p.ID {
			mine = append(mine, r.ID)
		}
	}
	want := []int64{ids[1], ids[0], ids[2]}
	if len(mine) != 3 || mine[0] != want[0] || mine[1] != want[1] || mine[2] != want[2] {
		t.Errorf("route order = %v, want %v", mine, want)
	}
}

// TestGrtGroupRouteValidation: invalid routes are refused and nothing is written.
func TestGrtGroupRouteValidation(t *testing.T) {
	db := w0DB(t)
	p := grtAddProvider(t, db, "")
	for _, tc := range []struct {
		name  string
		r     *models.GroupRoute
		match string
	}{
		{"empty patterns", &models.GroupRoute{Patterns: "  ", ProviderID: p.ID}, "patterns are empty"},
		{"no provider", &models.GroupRoute{Patterns: "pin.*"}, "no provider"},
		{"unknown provider", &models.GroupRoute{Patterns: "pin.*", ProviderID: 1 << 40}, "does not exist"},
		{"long notes", &models.GroupRoute{Patterns: "pin.*", ProviderID: p.ID, Notes: strings.Repeat("x", MaxGroupRouteNotes+1)}, "notes"},
	} {
		if _, err := db.InsertGroupRoute(tc.r); err == nil || !strings.Contains(err.Error(), tc.match) {
			t.Errorf("%s: InsertGroupRoute = %v, want error with %q", tc.name, err, tc.match)
		}
	}
	if err := ValidateGroupRoute(nil); err == nil {
		t.Error("ValidateGroupRoute(nil) = nil")
	}
	if n, err := db.CountGroupRoutesForProvider(p.ID); err != nil || n != 0 {
		t.Errorf("routes after refused inserts = %d, %v", n, err)
	}

	// Update refuses the same.
	r := &models.GroupRoute{Patterns: "pin.*", ProviderID: p.ID, Enabled: true}
	id, err := db.InsertGroupRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DeleteGroupRoute(id) })
	bad := *r
	bad.ProviderID = 1 << 40
	if err := db.UpdateGroupRoute(&bad); err == nil {
		t.Error("UpdateGroupRoute to an unknown provider accepted")
	}
	if x, err := db.GetGroupRouteByID(id); err != nil || x.ProviderID != p.ID {
		t.Errorf("route after refused update = %+v, %v", x, err)
	}
}

// TestGrtDeleteProviderGuard: a provider that routes reference (enabled or not) cannot be deleted;
// once the routes are gone it can.
func TestGrtDeleteProviderGuard(t *testing.T) {
	db := w0DB(t)
	p := grtAddProvider(t, db, "")
	r := &models.GroupRoute{Patterns: "pin.*", ProviderID: p.ID, Enabled: false}
	id, err := db.InsertGroupRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DeleteGroupRoute(id) })
	if n, err := db.CountGroupRoutesForProvider(p.ID); err != nil || n != 1 {
		t.Fatalf("CountGroupRoutesForProvider = %d, %v; want 1", n, err)
	}
	err = db.DeleteProvider(int(p.ID))
	if !errors.Is(err, ErrProviderHasRoutes) {
		t.Fatalf("DeleteProvider with a route = %v, want ErrProviderHasRoutes", err)
	}
	if still, err := db.GetProviderByID(p.ID); err != nil || still == nil {
		t.Fatalf("provider gone after refused delete: %v %v", still, err)
	}
	if err := db.DeleteGroupRoute(id); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProvider(int(p.ID)); err != nil {
		t.Fatalf("DeleteProvider without routes: %v", err)
	}
	if gone, err := db.GetProviderByID(p.ID); err != nil || gone != nil {
		t.Errorf("provider after delete = %v, %v", gone, err)
	}
}
