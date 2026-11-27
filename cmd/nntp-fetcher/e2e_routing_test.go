package main

// End-to-end test of the fetcher group routing (plan fetcher-group-routing, slice e2e+docs): the
// real nntp-fetcher binary (kit proc.GoBuild) against fake providers (kit fakeserver in Store
// mode), with a scratch -data root in t.TempDir().
//
//	A  primary, empty routing     127.0.0.1  alt.test, gmane.test, pin.test
//	B  routing "gmane.*,gwene.*"  127.0.0.2  gmane.test (+ gmane.new, gwene.new, alt.fromb, pin.fromb)
//	C  pin target of "pin.*"      127.0.0.3  a closed port
//
// The providers listen on different loopback addresses, not on different ports of 127.0.0.1: the
// fetcher caches each provider's LIST in <data>/cache/<host>.list, keyed by the host only.
//
// The database is seeded and changed in this process through openFetcherDB (one Database at a
// time, closed before every fetcher run). What each provider was asked for comes from the fake
// servers' command lines; what was stored from the progress database (the fetcher moves the
// progress only after the articles are committed).
//
// The fetch runs need an -nntphostname that resolves (processor.validateHostname looks it up in
// DNS, so a name like test.example is refused): PUGLEAF_E2E_HOST, else the system host name, else
// example.org. Without one the fetch steps skip; the dry runs and the list sync still run.
// Hang guards only (PUGLEAF_TEST_DEADLINE / NNTP_TEST_DEADLINE): no step asserts a duration.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	testkit "github.com/go-while/nntp-testkit"
	"github.com/go-while/nntp-testkit/fakeserver"
	"github.com/go-while/nntp-testkit/proc"
)

// routeE2EWait is the hang guard of the routing e2e test (fake servers, every fetcher run). A
// passing test never waits on it. Override with PUGLEAF_TEST_DEADLINE (a Go duration).
var routeE2EWait = routeE2EDeadline()

// routeE2EDeadline hands PUGLEAF_TEST_DEADLINE to the kit, which reads only NNTP_TEST_DEADLINE
// (an NNTP_TEST_DEADLINE set by the caller wins), and returns the kit's hang guard. A package
// variable initialiser, so it runs before any test reads testkit.Deadline().
func routeE2EDeadline() time.Duration {
	if os.Getenv(testkit.EnvDeadline) == "" {
		if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
			if err := os.Setenv(testkit.EnvDeadline, v); err != nil {
				fmt.Fprintln(os.Stderr, "[E2E] Setenv "+testkit.EnvDeadline+":", err)
			}
		}
	}
	return testkit.Deadline()
}

// TestMain removes the binaries proc.GoBuild built into its shared directory.
func TestMain(m *testing.M) {
	code := m.Run()
	proc.CleanBuilds()
	os.Exit(code)
}

const (
	routeE2EProvA = "e2e-a"
	routeE2EProvB = "e2e-b"
	routeE2EProvC = "e2e-c"
)

// routeE2EHostRe is the RFC 1123 host name form processor.validateHostname accepts.
var routeE2EHostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

// routeE2EHostname returns a host name the fetcher accepts as -nntphostname (a dotted name that
// resolves), or why there is none.
func routeE2EHostname() (string, error) {
	var cands []string
	if h := os.Getenv("PUGLEAF_E2E_HOST"); h != "" {
		cands = append(cands, h)
	} else {
		if h, err := os.Hostname(); err == nil && h != "" {
			cands = append(cands, h)
		}
		cands = append(cands, "example.org")
	}
	var why []string
	for _, h := range cands {
		l := strings.ToLower(h)
		if !routeE2EHostRe.MatchString(h) || !strings.Contains(h, ".") || l == "localhost" || l == "localhost.localdomain" || net.ParseIP(h) != nil {
			why = append(why, h+": not usable as -nntphostname")
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), routeE2EWait)
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", h)
		cancel()
		if err != nil || len(ips) == 0 {
			why = append(why, fmt.Sprintf("%s: does not resolve (%v)", h, err))
			continue
		}
		return h, nil
	}
	return "", errors.New(strings.Join(why, "; "))
}

// routeE2EArticle is article n of group on provider prefix ("a" or "b"): a complete article as
// the fetcher's intake needs it, with a message-id that names provider and group.
func routeE2EArticle(prefix, group string, n int) fakeserver.Article {
	id := fmt.Sprintf("<%s-%s-%d@e2e-routing.invalid>", prefix, group, n)
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
	return fakeserver.Article{
		MessageID: id,
		Date:      date,
		Headers: []string{
			"Path: " + prefix + ".fake.invalid!not-for-mail",
			"From: E2E Poster <poster@e2e-routing.invalid>",
			"Newsgroups: " + group,
			fmt.Sprintf("Subject: routing e2e %s %s %d", prefix, group, n),
			"Date: " + date.Format(fakeserver.DateLayout),
			"Message-ID: " + id,
		},
		Body: []string{fmt.Sprintf("article %d of %s from provider %s", n, group, prefix)},
	}
}

// routeE2EGroup is one group of a fake provider and how many articles it holds.
type routeE2EGroup struct {
	name string
	n    int
}

// routeE2EStart starts a fake provider on addr (Store mode) and returns it with the message-ids
// of each group. A loopback address that cannot be bound skips the test.
func routeE2EStart(t *testing.T, addr, prefix string, groups []routeE2EGroup) (*fakeserver.Server, map[string][]string) {
	t.Helper()
	st := fakeserver.NewStore()
	ids := make(map[string][]string)
	for _, g := range groups {
		st.AddGroup(g.name, "routing e2e "+g.name)
		for i := 1; i <= g.n; i++ {
			a := routeE2EArticle(prefix, g.name, i)
			st.AddArticle(g.name, a)
			ids[g.name] = append(ids[g.name], a.MessageID)
		}
	}
	s, err := fakeserver.Start(fakeserver.Options{Addr: addr, Store: st, Deadline: routeE2EWait})
	if err != nil {
		t.Skipf("fake provider on %s: %v (needs the 127.0.0.0/8 loopback addresses)", addr, err)
	}
	t.Cleanup(s.Close)
	return s, ids
}

// routeE2EClosedPort returns a port of ip that nothing listens on (it was free a moment ago).
func routeE2EClosedPort(t *testing.T, ip string) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Skipf("listen on %s: %v (needs the 127.0.0.0/8 loopback addresses)", ip, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("closing the listener on %s: %v", ip, err)
	}
	return port
}

// routeE2ESelected counts the GROUP and LISTGROUP commands per group in lines.
func routeE2ESelected(lines []string) map[string]int {
	out := make(map[string]int)
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 2 && (strings.EqualFold(f[0], "GROUP") || strings.EqualFold(f[0], "LISTGROUP")) {
			out[f[1]]++
		}
	}
	return out
}

// routeE2EArticleIDs returns the distinct arguments of the ARTICLE commands in lines, sorted.
func routeE2EArticleIDs(lines []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 2 && strings.EqualFold(f[0], "ARTICLE") && !seen[f[1]] {
			seen[f[1]] = true
			out = append(out, f[1])
		}
	}
	sort.Strings(out)
	return out
}

// routeE2ESorted returns a sorted copy of ids.
func routeE2ESorted(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// routeE2EWithDB opens the main database below data in this process, runs fn and closes it again
// (fetcherShutdown). No fetcher process may run meanwhile.
func routeE2EWithDB(t *testing.T, data string, fn func(db *database.Database)) {
	t.Helper()
	prev := database.NO_CACHE_BOOT
	database.NO_CACHE_BOOT = true
	defer func() { database.NO_CACHE_BOOT = prev }()
	db, cleanup, err := openFetcherDB(data)
	if err != nil {
		t.Fatalf("openFetcherDB(%s): %v", data, err)
	}
	defer cleanup()
	fn(db)
}

// routeE2EProgress returns the fetch progress rows as "provider|group" -> last article.
func routeE2EProgress(t *testing.T, data string) map[string]int64 {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(data, "progress.db"))
	if err != nil {
		t.Fatalf("open the progress database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing the progress database: %v", err)
		}
	}()
	rows, err := db.Query(`SELECT backend_name, newsgroup_name, last_article FROM progress`)
	if err != nil {
		t.Fatalf("read the progress rows: %v", err)
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var backend, group string
		var last int64
		if err := rows.Scan(&backend, &group, &last); err != nil {
			t.Fatalf("scan a progress row: %v", err)
		}
		out[backend+"|"+group] = last
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the progress rows: %v", err)
	}
	return out
}

// routeE2ETail returns the last n lines of s.
func routeE2ETail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// routeE2ELines returns the lines of out that start with prefix (the dry-run ROUTE and SKIP
// lines go to stdout, the log to stderr; proc.Run returns both).
func routeE2ELines(out, prefix string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			got = append(got, strings.TrimRight(l, "\r"))
		}
	}
	sort.Strings(got)
	return got
}

// routeE2EEnv is the state the steps of TestE2ERoutingFetcher share.
type routeE2EEnv struct {
	bin, data, run string
	a, b           *fakeserver.Server
	idsA, idsB     map[string][]string
	provC          int64 // provider id of C
	routeID        int64 // the pin.* route
}

// fetcher runs the fetcher with -data and args and fails the test unless it exits with want.
func (e *routeE2EEnv) fetcher(t *testing.T, want int, args ...string) string {
	t.Helper()
	out, code := proc.Run(t, proc.RunOptions{
		Path:    e.bin,
		Args:    append([]string{"-data", e.data}, args...),
		Dir:     e.run,
		Timeout: routeE2EWait,
	})
	if code != want {
		t.Fatalf("nntp-fetcher %s: exit %d, want %d\n%s", strings.Join(args, " "), code, want, routeE2ETail(out, 60))
	}
	return out
}

// wantLines fails unless the lines of out that start with prefix are want (any order).
func routeE2EWantLines(t *testing.T, out, prefix string, want ...string) {
	t.Helper()
	got := routeE2ELines(out, prefix)
	want = routeE2ESorted(want)
	if !slices.Equal(got, want) {
		t.Errorf("%q lines:\n%s\nwant:\n%s\noutput tail:\n%s", prefix, strings.Join(got, "\n"), strings.Join(want, "\n"), routeE2ETail(out, 40))
	}
}

// routeE2EContains fails unless out contains every s.
func routeE2EContains(t *testing.T, out string, s ...string) {
	t.Helper()
	for _, x := range s {
		if !strings.Contains(out, x) {
			t.Errorf("output lacks %q\noutput tail:\n%s", x, routeE2ETail(out, 40))
		}
	}
}

// routeE2ELacks fails when out contains any s.
func routeE2ELacks(t *testing.T, out string, s ...string) {
	t.Helper()
	for _, x := range s {
		if strings.Contains(out, x) {
			t.Errorf("output contains %q\noutput tail:\n%s", x, routeE2ETail(out, 40))
		}
	}
}

func TestE2ERoutingFetcher(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs the fetcher binary")
	}
	if euid := os.Geteuid(); euid >= 0 && euid < 1000 {
		t.Skipf("skipping: OpenDatabase refuses euid %d (< 1000)", euid)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go tool not found: %v", err)
	}

	tmp := t.TempDir()
	e := &routeE2EEnv{
		data: filepath.Join(tmp, "data-test-e2e-routing"),
		run:  filepath.Join(tmp, "run"),
	}
	for _, d := range []string{e.data, e.run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	e.a, e.idsA = routeE2EStart(t, "127.0.0.1:0", "a", []routeE2EGroup{{"alt.test", 3}, {"gmane.test", 2}, {"pin.test", 2}})
	e.b, e.idsB = routeE2EStart(t, "127.0.0.2:0", "b", []routeE2EGroup{
		{"gmane.test", 2}, {"gmane.new", 1}, {"gwene.new", 1}, {"alt.fromb", 1}, {"pin.fromb", 1},
	})
	portC := routeE2EClosedPort(t, "127.0.0.3")
	e.bin = proc.GoBuild(t, "", "github.com/go-while/go-pugleaf/cmd/nntp-fetcher", "-X main.appVersion=e2e-test")

	// Seed: only A, B and C enabled (the default providers are disabled), three active groups,
	// one global route pin.* -> C.
	routeE2EWithDB(t, e.data, func(db *database.Database) {
		existing, err := db.GetProviders()
		if err != nil {
			t.Fatalf("GetProviders: %v", err)
		}
		for _, p := range existing {
			if p.Enabled {
				p.Enabled = false
				if err := db.SetProvider(p); err != nil {
					t.Fatalf("disable provider %s: %v", p.Name, err)
				}
			}
		}
		add := func(name, host string, port, prio int, routing string) int64 {
			if err := db.AddProvider(&models.Provider{
				Name: name, Grp: "e2e", Host: host, Port: port, SSL: false, MaxConns: 2,
				Enabled: true, Priority: prio, Routing: routing,
			}); err != nil {
				t.Fatalf("AddProvider %s: %v", name, err)
			}
			p, err := db.GetProviderByName(name)
			if err != nil || p == nil {
				t.Fatalf("GetProviderByName %s: %v", name, err)
			}
			return p.ID
		}
		add(routeE2EProvA, "127.0.0.1", e.a.Port(), 1, "")
		add(routeE2EProvB, "127.0.0.2", e.b.Port(), 2, "gmane.*,gwene.*")
		e.provC = add(routeE2EProvC, "127.0.0.3", portC, 3, "")
		for _, g := range []string{"alt.test", "gmane.test", "pin.test"} {
			if err := db.InsertNewsgroup(&models.Newsgroup{Name: g, Active: true, Status: "y", LowWater: 1}); err != nil {
				t.Fatalf("InsertNewsgroup %s: %v", g, err)
			}
		}
		id, err := db.InsertGroupRoute(&models.GroupRoute{Patterns: "pin.*", ProviderID: e.provC, Enabled: true, Notes: "e2e pin to a closed port"})
		if err != nil {
			t.Fatalf("InsertGroupRoute: %v", err)
		}
		e.routeID = id
	})
	t.Logf("providers: A %s, B %s (routing gmane.*,gwene.*), C 127.0.0.3:%d (closed, pin.*)", e.a.Addr(), e.b.Addr(), portC)

	if !t.Run("dry-run", func(t *testing.T) {
		out := e.fetcher(t, 0, "-routing-dry-run")
		routeE2EWantLines(t, out, "ROUTE ", "ROUTE alt.test -> "+routeE2EProvA, "ROUTE gmane.test -> "+routeE2EProvB)
		routeE2EWantLines(t, out, "SKIP ", "SKIP pin.test pinned: no pinned provider lists it")
		routeE2EContains(t, out,
			"[FETCHER-ROUTE] dry run: 2 groups routed, 1 skipped, no articles fetched",
			"[FETCHER-ROUTE]: provider '"+routeE2EProvA+"': 1 groups",
			"[FETCHER-ROUTE]: provider '"+routeE2EProvB+"': 1 groups",
			"[FETCHER-ROUTE]: provider '"+routeE2EProvC+"': failed to fetch newsgroup list",
			"[FETCHER-ROUTE]: 1 pinned groups skipped (no pinned provider is usable and lists them): pin.test",
		)
		for name, s := range map[string]*fakeserver.Server{"A": e.a, "B": e.b} {
			if n := s.Count("LIST"); n < 1 {
				t.Errorf("provider %s got %d LIST commands, want at least 1", name, n)
			}
			if n := s.Count("ARTICLE"); n != 0 {
				t.Errorf("dry run: provider %s got %d ARTICLE commands, want 0", name, n)
			}
			if sel := routeE2ESelected(s.Lines()); len(sel) != 0 {
				t.Errorf("dry run: provider %s got GROUP commands: %v", name, sel)
			}
		}
	}) {
		return
	}

	if !t.Run("group-patterns", func(t *testing.T) {
		// -group takes several patterns: 'alt.*,gmane.*' was one prefix "alt.*,gmane." before
		out := e.fetcher(t, 0, "-routing-dry-run", "-group", "alt.*,gmane.*")
		routeE2EContains(t, out,
			"-group selection: 2 pattern(s): alt.*, gmane.*",
			"-group selected 2 newsgroups",
			"[FETCHER-ROUTE] dry run: 2 groups routed, 0 skipped, no articles fetched",
		)
		routeE2EWantLines(t, out, "ROUTE ", "ROUTE alt.test -> "+routeE2EProvA, "ROUTE gmane.test -> "+routeE2EProvB)
		routeE2EWantLines(t, out, "SKIP ")

		// -provider: only that provider's ROUTE lines (the skips are always listed)
		out = e.fetcher(t, 0, "-routing-dry-run", "-provider", routeE2EProvB)
		routeE2EWantLines(t, out, "ROUTE ", "ROUTE gmane.test -> "+routeE2EProvB)
		routeE2EWantLines(t, out, "SKIP ", "SKIP pin.test pinned: no pinned provider lists it")
		routeE2EContains(t, out, "[FETCHER-ROUTE] dry run: 1 groups routed, 1 skipped, no articles fetched")
		if n := e.a.Count("ARTICLE") + e.b.Count("ARTICLE"); n != 0 {
			t.Errorf("dry runs sent %d ARTICLE commands, want 0", n)
		}
	}) {
		return
	}

	if !t.Run("list-sync", func(t *testing.T) {
		// B's LIST: gmane.test (in the database), gmane.new, gwene.new (its routing wants them),
		// alt.fromb (B is not the primary) and pin.fromb (pinned to C): only the two new groups
		// B may serve are added
		acceptedA := e.a.Accepted()
		listB := e.b.Count("LIST")
		out := e.fetcher(t, 0, "-fetch-newsgroups-from-remote", "$all", "-fetch-newsgroups-force", "-provider", routeE2EProvB)
		routeE2EContains(t, out,
			"UpdateNewsgroupList: Using provider: "+routeE2EProvB,
			"[FETCHER-ROUTE]: UpdateNewsgroupList: 2 of 5 remote groups skipped: the routing does not let provider '"+routeE2EProvB+"' serve them",
			"(Added new) newsgroup: 'gmane.new'",
			"(Added new) newsgroup: 'gwene.new'",
		)
		routeE2ELacks(t, out, "'alt.fromb'", "'pin.fromb'")
		if n := e.b.Count("LIST"); n <= listB {
			t.Errorf("provider B got no LIST for the list sync (LIST count %d before and after)", n)
		}
		if n := e.a.Accepted(); n != acceptedA {
			t.Errorf("list sync with -provider %s connected to A (%d connections before, %d after)", routeE2EProvB, acceptedA, n)
		}
		routeE2EWithDB(t, e.data, func(db *database.Database) {
			groups, err := db.MainDBGetAllNewsgroups()
			if err != nil {
				t.Fatalf("MainDBGetAllNewsgroups: %v", err)
			}
			got := make(map[string]bool)
			for _, g := range groups {
				got[g.Name] = g.Active
			}
			for _, name := range []string{"gmane.new", "gwene.new"} {
				active, ok := got[name]
				if !ok {
					t.Errorf("list sync did not add %s", name)
				} else if active {
					t.Errorf("list sync added %s as active, want inactive", name)
				}
			}
			for _, name := range []string{"alt.fromb", "pin.fromb"} {
				if _, ok := got[name]; ok {
					t.Errorf("list sync added %s: provider %s may not serve it", name, routeE2EProvB)
				}
			}
		})
	}) {
		return
	}

	host, hostErr := routeE2EHostname()
	if !t.Run("fetch", func(t *testing.T) {
		if hostErr != nil {
			t.Skipf("no usable -nntphostname (set PUGLEAF_E2E_HOST): %v", hostErr)
		}
		out := e.fetcher(t, 0, "-nntphostname", host)
		routeE2EContains(t, out,
			"[FETCHER-ROUTE]: provider '"+routeE2EProvA+"': 1 groups",
			"[FETCHER-ROUTE]: provider '"+routeE2EProvB+"': 1 groups",
			"[FETCHER-ROUTE]: 1 pinned groups skipped (no pinned provider is usable and lists them): pin.test",
			"[FETCHER-ROUTE]: pass 1/2: provider '"+routeE2EProvA+"' with 1 groups",
			"[FETCHER-ROUTE]: pass 2/2: provider '"+routeE2EProvB+"' with 1 groups",
		)

		linesA, linesB := e.a.Lines(), e.b.Lines()
		selA, selB := routeE2ESelected(linesA), routeE2ESelected(linesB)
		if selA["alt.test"] == 0 {
			t.Errorf("A never got GROUP alt.test (A selected %v)", selA)
		}
		for _, g := range []string{"gmane.test", "pin.test"} {
			if selA[g] != 0 {
				t.Errorf("A got GROUP %s %d times: the routing gives it to another provider", g, selA[g])
			}
		}
		if selB["gmane.test"] == 0 {
			t.Errorf("B never got GROUP gmane.test (B selected %v)", selB)
		}
		for g := range selB {
			if g != "gmane.test" {
				t.Errorf("B got GROUP %s: only gmane.test is routed to it", g)
			}
		}
		if got, want := routeE2EArticleIDs(linesA), routeE2ESorted(e.idsA["alt.test"]); !slices.Equal(got, want) {
			t.Errorf("A was asked for the articles\n%v\nwant only alt.test's\n%v", got, want)
		}
		if got, want := routeE2EArticleIDs(linesB), routeE2ESorted(e.idsB["gmane.test"]); !slices.Equal(got, want) {
			t.Errorf("B was asked for the articles\n%v\nwant only gmane.test's\n%v", got, want)
		}

		// the progress moves only after the articles are committed
		prog := routeE2EProgress(t, e.data)
		if got := prog[routeE2EProvA+"|alt.test"]; got != 3 {
			t.Errorf("progress (%s, alt.test) = %d, want 3 (progress rows %v)", routeE2EProvA, got, prog)
		}
		if got := prog[routeE2EProvB+"|gmane.test"]; got != 2 {
			t.Errorf("progress (%s, gmane.test) = %d, want 2 (progress rows %v)", routeE2EProvB, got, prog)
		}
		for k := range prog {
			if strings.HasSuffix(k, "|pin.test") || k == routeE2EProvA+"|gmane.test" || k == routeE2EProvB+"|alt.test" {
				t.Errorf("unexpected progress row %s (rows %v)", k, prog)
			}
		}
	}) {
		return
	}

	t.Run("routing-cleared", func(t *testing.T) {
		if hostErr != nil {
			t.Skipf("no usable -nntphostname (set PUGLEAF_E2E_HOST): %v", hostErr)
		}
		// no provider routing and no global route: the fetcher behaves as before routing, every
		// group comes from the primary A and B and C are not contacted
		routeE2EWithDB(t, e.data, func(db *database.Database) {
			providers, err := db.GetProviders()
			if err != nil {
				t.Fatalf("GetProviders: %v", err)
			}
			for _, p := range providers {
				if p.Routing != "" {
					p.Routing = ""
					if err := db.SetProvider(p); err != nil {
						t.Fatalf("clear the routing of %s: %v", p.Name, err)
					}
				}
			}
			if err := db.DeleteGroupRoute(e.routeID); err != nil {
				t.Fatalf("DeleteGroupRoute: %v", err)
			}
		})
		// A has gmane.test 1-2 too; mark it as fetched from A so the run selects the group on A
		// without the date-based download of a group that moved provider (not driven here)
		out := e.fetcher(t, 0, "-group", "gmane.test", "-reset-progress", "2", "-provider", routeE2EProvA)
		routeE2EContains(t, out, "on provider '"+routeE2EProvA+"'")

		acceptedB := e.b.Accepted()
		before := len(e.a.Lines())
		out = e.fetcher(t, 0)
		routeE2ELacks(t, out,
			"Provider '"+routeE2EProvB+"'",
			"Provider '"+routeE2EProvC+"'",
			"providers take part",
			"[FETCHER-ROUTE]: pass ",
			"pinned groups skipped",
		)
		routeE2EContains(t, out, "[FETCHER]: Provider '"+routeE2EProvA+"'")
		if n := e.b.Accepted(); n != acceptedB {
			t.Errorf("B was contacted without routing (%d connections before, %d after)", acceptedB, n)
		}
		linesA := e.a.Lines()[before:]
		selA := routeE2ESelected(linesA)
		for _, g := range []string{"alt.test", "gmane.test", "pin.test"} {
			if selA[g] == 0 {
				t.Errorf("A never got GROUP %s without routing (A selected %v)", g, selA)
			}
		}
		if got, want := routeE2EArticleIDs(linesA), routeE2ESorted(e.idsA["pin.test"]); !slices.Equal(got, want) {
			t.Errorf("A was asked for the articles\n%v\nwant pin.test's (alt.test and gmane.test are up to date)\n%v", got, want)
		}
		prog := routeE2EProgress(t, e.data)
		if got := prog[routeE2EProvA+"|pin.test"]; got != 2 {
			t.Errorf("progress (%s, pin.test) = %d, want 2 (progress rows %v)", routeE2EProvA, got, prog)
		}
		if got := prog[routeE2EProvA+"|gmane.test"]; got != 2 {
			t.Errorf("progress (%s, gmane.test) = %d, want 2 from -reset-progress (progress rows %v)", routeE2EProvA, got, prog)
		}
	})
}
