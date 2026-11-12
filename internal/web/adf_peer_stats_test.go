package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/peerstats"
)

// Tests of the admin peering statistics page (/admin/peers/stats, plan peering-stats, slice
// web-stats). The page reads the hourly buckets through the opener SetPeerStatsOpener installs;
// the real opener is peerstats.OpenReadOnly, these tests use a fake reader.

// adfPeerStatsReader is a fake PeerStatsReader: it hands out buckets whatever range is asked
// for (so the handler's own range filter is what the range tests check) and records the calls.
type adfPeerStatsReader struct {
	buckets []peerstats.Bucket
	err     error
	paths   []string
	ranges  [][2]int64
	closed  int
}

func (r *adfPeerStatsReader) ReadRange(fromHour, toHour int64) ([]peerstats.Bucket, error) {
	r.ranges = append(r.ranges, [2]int64{fromHour, toHour})
	if r.err != nil {
		return nil, r.err
	}
	return r.buckets, nil
}

func (r *adfPeerStatsReader) Close() error {
	r.closed++
	return nil
}

// adfPeerStatsFile creates the store file the page looks for and removes it afterwards (its
// content is never read: the fake opener stands for peerstats.OpenReadOnly).
func adfPeerStatsFile(t *testing.T) {
	t.Helper()
	path := peerstats.Path(w0DB(t).GetDataDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

// adfPeerStatsWire installs r as the only opener and records the paths it was called with.
func adfPeerStatsWire(t *testing.T, r *adfPeerStatsReader) {
	t.Helper()
	SetPeerStatsOpener(func(path string) (PeerStatsReader, error) {
		r.paths = append(r.paths, path)
		return r, nil
	})
	t.Cleanup(func() { SetPeerStatsOpener(nil) })
}

// adfPeerStatsBody renders the page as an admin and returns the body with its white space
// collapsed, so assertions do not depend on the template's indentation.
func adfPeerStatsBody(t *testing.T, cookie *http.Cookie, query string) string {
	t.Helper()
	rec := w0Do(t, w0Req{Path: "/admin/peers/stats" + query, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/peers/stats%s = %d, want 200", query, rec.Code)
	}
	return adfSqueeze(rec.Body.String())
}

// adfPeerStatsHalves splits the rendered page into its incoming and its outgoing part.
func adfPeerStatsHalves(t *testing.T, body string) (in, out string) {
	t.Helper()
	i := strings.Index(body, `<h6 class="mb-0">Incoming</h6>`)
	o := strings.Index(body, `<h6 class="mb-0">Outgoing</h6>`)
	if i < 0 || o <= i {
		t.Fatalf("page has no Incoming/Outgoing sections (in=%d out=%d)", i, o)
	}
	return body[i:o], body[o:]
}

// adfPeerStatsRow returns the table row of half that names peer.
func adfPeerStatsRow(t *testing.T, half, peer string) string {
	t.Helper()
	for _, row := range strings.Split(half, "<tr>") {
		if strings.Contains(row, `ps-name"> `+peer+" ") {
			return row
		}
	}
	t.Fatalf("no table row for peer %q in:\n%s", peer, half)
	return ""
}

// adfPeerStatsTile is the tile the page prints for one total.
func adfPeerStatsTile(n int64, label string) string {
	return fmt.Sprintf(`<span class="fw-bold">%d</span> <small class="text-muted">%s</small>`, n, label)
}

// adfPeerStatsPeersTile is the peers tile of the configured peer rows: enabled of total.
func adfPeerStatsPeersTile(t *testing.T) string {
	t.Helper()
	peers, err := w0DB(t).GetPeers()
	if err != nil {
		t.Fatalf("GetPeers: %v", err)
	}
	enabled := 0
	for _, p := range peers {
		if p.Enabled {
			enabled++
		}
	}
	return fmt.Sprintf(`<span class="fw-bold">%d / %d</span> <small class="text-muted">peers enabled</small>`, enabled, len(peers))
}

// adfPeerStatsHour is the start of an hour n hours before the current one.
func adfPeerStatsHour(n int64) int64 { return peerstats.HourOf(time.Now()) - n*3600 }

// adfPeerStatsPeer creates a peer row through the database (not the form) and returns it.
func adfPeerStatsPeer(t *testing.T, enabled bool) *models.Peer {
	t.Helper()
	name := w0Name("adfpstat")
	p := &models.Peer{Name: name, PathToken: name, Hosts: "192.0.2.9", Enabled: enabled}
	id, err := w0DB(t).InsertPeer(p, "")
	if err != nil {
		t.Fatalf("InsertPeer %s: %v", name, err)
	}
	got, err := w0DB(t).GetPeerByID(id)
	if err != nil {
		t.Fatalf("GetPeerByID(%d): %v", id, err)
	}
	t.Cleanup(func() { _ = w0DB(t).DeletePeer(id) })
	return got
}

// TestAdfPeerStatsNoStore: without an opener and without the store file the page is a 200 that
// says there are no statistics yet, and the opener is not even called when the file is missing.
func TestAdfPeerStatsNoStore(t *testing.T) {
	_, cookie := w0NewUser(t, true)

	// A disabled peer row and nothing else: the page must still say that this host has a peer.
	adfPeerStatsPeer(t, false)

	SetPeerStatsOpener(nil)
	body := adfPeerStatsBody(t, cookie, "")
	if !strings.Contains(body, "No statistics yet:") {
		t.Error("page without an opener does not say that there are no statistics yet")
	}
	if strings.Contains(body, `<h6 class="mb-0">Incoming</h6>`) {
		t.Error("page without an opener shows the direction tables")
	}
	// The peers tile and the queue tile do not come from the statistics store, so the empty
	// state shows them: "0 enabled / 1" for a host whose only peer row is disabled.
	if want := adfPeerStatsPeersTile(t); !strings.Contains(body, want) {
		t.Errorf("the empty state lacks the peers tile %q", want)
	}
	if want := `<span class="fw-bold">n/a</span> <small class="text-muted">outgoing queue</small>`; !strings.Contains(body, want) {
		t.Error("the empty state does not show the outgoing queue as n/a without a feed queue file")
	}

	// An opener is wired but the store file does not exist: still nothing, and the opener is
	// not called (a web host that runs no nntp-server must not even try).
	r := &adfPeerStatsReader{}
	adfPeerStatsWire(t, r)
	body = adfPeerStatsBody(t, cookie, "")
	if len(r.paths) != 0 {
		t.Errorf("the store was opened although the file does not exist: %v", r.paths)
	}
	if !strings.Contains(body, "No statistics yet:") {
		t.Error("page does not say that there are no statistics yet when the store file is missing")
	}
}

// TestAdfPeerStatsReadError: a store that cannot be read is not an error page either.
func TestAdfPeerStatsReadError(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	adfPeerStatsFile(t)
	r := &adfPeerStatsReader{err: errors.New("peer-stats.sq3 is locked")}
	adfPeerStatsWire(t, r)

	body := adfPeerStatsBody(t, cookie, "")
	if !strings.Contains(body, "No statistics yet:") {
		t.Error("page does not fall back to the empty state on a read error")
	}
	if r.closed != 1 {
		t.Errorf("the store was closed %d time(s), want 1", r.closed)
	}
}

// TestAdfPeerStatsPage: the page sums the buckets of the range into the tiles and one row per
// peer, marks a disabled peer row and the unknown hosts, and closes the store again.
func TestAdfPeerStatsPage(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	adfPeerStatsFile(t)
	enabled := adfPeerStatsPeer(t, true)
	disabled := adfPeerStatsPeer(t, false)

	h0, h1 := adfPeerStatsHour(0), adfPeerStatsHour(1)
	ref := func(p *models.Peer) peerstats.PeerRef { return peerstats.PeerRef{ID: p.ID, Name: p.Name} }
	r := &adfPeerStatsReader{buckets: []peerstats.Bucket{
		// The enabled peer feeds us over two hours; the sums are what the page shows.
		{Peer: ref(enabled), Dir: peerstats.In, Hour: h1, Counters: peerstats.Counters{
			Offered: 100, Accepted: 60, Refused: 20, Rejected: 5, Duplicate: 4, Deferred: 2, AccBytes: 600000, Conns: 1,
		}},
		{Peer: ref(enabled), Dir: peerstats.In, Hour: h0, Counters: peerstats.Counters{
			Offered: 100, Accepted: 61, Refused: 21, Rejected: 6, Duplicate: 5, Deferred: 3, AccBytes: 648576, Conns: 2,
		}},
		// ... and we feed it.
		{Peer: ref(enabled), Dir: peerstats.Out, Hour: h0, Counters: peerstats.Counters{
			Offered: 90, Accepted: 70, Refused: 11, Rejected: 3, Deferred: 4, Missing: 2, AccBytes: 1024, Conns: 1,
		}},
		// The disabled peer row only has denied attempts: that is what the admin wants to see
		// before enabling it.
		{Peer: ref(disabled), Dir: peerstats.In, Hour: h0, Counters: peerstats.Counters{Denied: 37, Conns: 6}},
		// Feed commands from a host that matches no peer row.
		{Peer: peerstats.UnknownPeer, Dir: peerstats.In, Hour: h0, Counters: peerstats.Counters{Denied: 9, Conns: 3}},
	}}
	adfPeerStatsWire(t, r)

	body := adfPeerStatsBody(t, cookie, "")
	if len(r.paths) != 1 || r.paths[0] != peerstats.Path(w0DB(t).GetDataDir()) {
		t.Errorf("opened %v, want [%s]", r.paths, peerstats.Path(w0DB(t).GetDataDir()))
	}
	if r.closed != 1 {
		t.Errorf("the store was closed %d time(s), want 1", r.closed)
	}

	// Incoming tiles: the sums over both hours and all peers, denied included.
	for _, want := range []string{
		adfPeerStatsTile(121, "accepted"),
		adfPeerStatsTile(41, "refused"),
		adfPeerStatsTile(11, "rejected"),
		adfPeerStatsTile(9, "duplicate"),
		// The denied tile is the one the page highlights.
		`<span class="fw-bold text-danger">46</span> <small class="text-muted">denied</small>`,
		`<span class="fw-bold">1.2 MB</span> <small class="text-muted">accepted bytes</small>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks the incoming tile %q", want)
		}
	}
	// Outgoing tiles.
	for _, want := range []string{
		adfPeerStatsTile(90, "offered"),
		adfPeerStatsTile(70, "accepted"),
		adfPeerStatsTile(4, "deferred"),
		adfPeerStatsTile(2, "missing"),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks the outgoing tile %q", want)
		}
	}

	in, out := adfPeerStatsHalves(t, body)

	// The enabled peer's incoming row carries the sums and the accepted share of offered.
	row := adfPeerStatsRow(t, in, enabled.Name)
	for _, want := range []string{"<td>3</td>", "<td>200</td>", "<td>121</td>", "<td>41</td>",
		"<td>11</td>", "<td>9</td>", "<td>5</td>", "1.2 MB", "60.5%"} {
		if !strings.Contains(row, want) {
			t.Errorf("incoming row of %s lacks %q: %s", enabled.Name, want, row)
		}
	}
	if strings.Contains(row, ">disabled<") {
		t.Errorf("incoming row of the enabled peer %s has the disabled badge", enabled.Name)
	}

	// The disabled peer row is marked and shows its denied attempts.
	row = adfPeerStatsRow(t, in, disabled.Name)
	if !strings.Contains(row, ">disabled<") {
		t.Errorf("incoming row of %s has no disabled badge: %s", disabled.Name, row)
	}
	if !strings.Contains(row, ">37<") {
		t.Errorf("incoming row of %s does not show its 37 denied attempts: %s", disabled.Name, row)
	}

	// The unknown hosts are a row of their own, marked and last.
	row = adfPeerStatsRow(t, in, peerstats.UnknownPeer.Name)
	if !strings.Contains(row, ">unknown hosts<") || !strings.Contains(row, ">9<") {
		t.Errorf("row of the unknown hosts is wrong: %s", row)
	}
	if iu, id := strings.Index(in, `ps-name"> `+peerstats.UnknownPeer.Name+" "), strings.Index(in, `ps-name"> `+disabled.Name+" "); iu < id {
		t.Errorf("the unknown hosts (%d) are listed before the peer rows (%d)", iu, id)
	}

	// Outgoing: only the peer we feed, with its missing count and n/a for the queue depth
	// (no feed queue file in this test).
	row = adfPeerStatsRow(t, out, enabled.Name)
	for _, want := range []string{"<td>90</td>", "<td>70</td>", "<td>2</td>", "n/a", "77.8%"} {
		if !strings.Contains(row, want) {
			t.Errorf("outgoing row of %s lacks %q: %s", enabled.Name, want, row)
		}
	}
	if strings.Contains(out, `ps-name"> `+peerstats.UnknownPeer.Name+" ") {
		t.Error("the outgoing table lists the unknown hosts, which only the incoming side has")
	}

	// The chart of the current hour carries the numbers of that slot in its tooltip.
	want := "accepted 61, refused 21, rejected 6, duplicate 5, denied 46"
	if !strings.Contains(body, want) {
		t.Errorf("no chart bar with the tooltip %q", want)
	}
	if !strings.Contains(in, `<svg class="ps-chart"`) || !strings.Contains(out, `<svg class="ps-chart"`) {
		t.Error("a direction has no inline SVG chart")
	}
	// The charts are server-generated SVG: the statistics themselves bring no script and no
	// external asset along (in ends where out begins, so this is page content only).
	if strings.Contains(in, "<script") || strings.Contains(in, `src="//`) || strings.Contains(in, "http") {
		t.Error("the incoming section loads a script or an external asset")
	}
}

// TestAdfPeerStatsRanges: ?range= picks how far back the page looks, anything unknown falls
// back to 24 hours, and the counters of the hours in range are summed.
func TestAdfPeerStatsRanges(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	adfPeerStatsFile(t)
	peer := adfPeerStatsPeer(t, true)
	ref := peerstats.PeerRef{ID: peer.ID, Name: peer.Name}

	r := &adfPeerStatsReader{buckets: []peerstats.Bucket{
		{Peer: ref, Dir: peerstats.In, Hour: adfPeerStatsHour(0), Counters: peerstats.Counters{Offered: 5, Accepted: 5}},
		{Peer: ref, Dir: peerstats.In, Hour: adfPeerStatsHour(40), Counters: peerstats.Counters{Offered: 700, Accepted: 700}},
		{Peer: ref, Dir: peerstats.In, Hour: adfPeerStatsHour(20 * 24), Counters: peerstats.Counters{Offered: 9000, Accepted: 9000}},
	}}
	adfPeerStatsWire(t, r)

	cases := []struct {
		query    string
		hours    int64 // the hours the handler must ask the store for (0: the day-aligned range)
		days     bool  // the bars are days: the range starts at a UTC midnight
		accepted int64
		slot     string
		label    string
	}{
		{query: "", hours: 24, accepted: 5, slot: "hour", label: "24 hours"},
		{query: "?range=24h", hours: 24, accepted: 5, slot: "hour", label: "24 hours"},
		{query: "?range=7d", hours: 7 * 24, accepted: 705, slot: "hour", label: "7 days"},
		{query: "?range=30d", days: true, accepted: 9705, slot: "day", label: "30 days"},
		{query: "?range=../../etc/passwd", hours: 24, accepted: 5, slot: "hour", label: "24 hours"},
		{query: "?range=99y", hours: 24, accepted: 5, slot: "hour", label: "24 hours"},
	}
	for _, tc := range cases {
		r.ranges = nil
		// The end of the range is the end of the hour the request falls into, which may be the
		// next one when the hour turns between here and the handler.
		endBefore := peerstats.HourOf(time.Now()) + 3600
		body := adfPeerStatsBody(t, cookie, tc.query)
		if len(r.ranges) != 1 {
			t.Fatalf("%q: ReadRange called %d time(s), want 1", tc.query, len(r.ranges))
		}
		got := r.ranges[0]
		switch {
		case tc.days:
			// A day range starts at a UTC midnight, so one bar is one calendar day; its last
			// bar is the partial day up to now, which makes it 29 to 30 days long.
			if got[0]%86400 != 0 {
				t.Errorf("%q: range starts at %d, which is no UTC midnight", tc.query, got[0])
			}
			if h := (got[1] - got[0]) / 3600; h < 29*24 || h > 30*24 {
				t.Errorf("%q: read %d hours, want 29 to 30 days", tc.query, h)
			}
		default:
			if h := (got[1] - got[0]) / 3600; h != tc.hours {
				t.Errorf("%q: read %d hours, want %d", tc.query, h, tc.hours)
			}
		}
		// The range ends at the end of the current hour, so the hour still being counted shows.
		if got[1] != endBefore && got[1] != endBefore+3600 {
			t.Errorf("%q: range ends at %d, want %d (the current hour + 1)", tc.query, got[1], endBefore)
		}
		if want := adfPeerStatsTile(tc.accepted, "accepted"); !strings.Contains(body, want) {
			t.Errorf("%q: page lacks the tile %q", tc.query, want)
		}
		if want := "one bar = one " + tc.slot; !strings.Contains(body, want) {
			t.Errorf("%q: page does not say %q", tc.query, want)
		}
		// The active range button is the one the handler chose.
		if want := fmt.Sprintf(`class="btn btn-sm btn-primary">%s</a>`, tc.label); !strings.Contains(body, want) {
			t.Errorf("%q: %s is not the active range button", tc.query, tc.label)
		}
	}
}

// TestAdfPeerStatsEscaping: a peer name is admin-entered, but the page escapes it like any
// other value, and a bucket of a peer row that is gone is marked instead of being dropped.
func TestAdfPeerStatsEscaping(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	adfPeerStatsFile(t)
	evil := `<script>alert("peer")</script>`
	r := &adfPeerStatsReader{buckets: []peerstats.Bucket{
		{Peer: peerstats.PeerRef{ID: 999999, Name: evil}, Dir: peerstats.In,
			Hour: adfPeerStatsHour(0), Counters: peerstats.Counters{Offered: 2, Denied: 2}},
	}}
	adfPeerStatsWire(t, r)

	body := adfPeerStatsBody(t, cookie, "")
	if strings.Contains(body, evil) {
		t.Error("the page renders a peer name unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("the page does not show the escaped peer name")
	}
	if !strings.Contains(body, ">removed<") {
		t.Error("a bucket of a peer row that no longer exists is not marked as removed")
	}
}

// TestAdfPeerStatsLinkedFromPeers: the peers page links to the statistics.
func TestAdfPeerStatsLinkedFromPeers(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	body := adfPeersBody(t, cookie)
	if !strings.Contains(body, `href="/admin/peers/stats"`) {
		t.Error("the peers page does not link to /admin/peers/stats")
	}
}

// TestAdfPeerStatsNonAdminRefused: the page is admin-only, like every other admin route.
func TestAdfPeerStatsNonAdminRefused(t *testing.T) {
	adfPeerStatsFile(t)
	r := &adfPeerStatsReader{}
	adfPeerStatsWire(t, r)

	_, plain := w0NewUser(t, false)
	rec := w0Do(t, w0Req{Path: "/admin/peers/stats", Cookies: []*http.Cookie{plain}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/profile" {
		t.Errorf("GET /admin/peers/stats as a plain user = %d %q, want 303 /profile", rec.Code, rec.Header().Get("Location"))
	}
	if _, failure := GetAndClearFlash(plain.Value, "error"); failure != "Access denied" {
		t.Errorf("error flash %q, want \"Access denied\"", failure)
	}

	rec = w0Do(t, w0Req{Path: "/admin/peers/stats"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET /admin/peers/stats anonymously = %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}

	// A POST to the page is answered by the router, not by the handler.
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/peers/stats", Form: url.Values{"x": {"1"}}})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /admin/peers/stats = %d, want 405", rec.Code)
	}
	if len(r.paths) != 0 {
		t.Errorf("a non-admin request opened the store: %v", r.paths)
	}
}

// TestAdfPeerStatsAggregate: the aggregation of the buckets into rows, totals and chart bars,
// without the HTTP layer. It is what the tables and the charts are built from: the sums per
// peer, one bar per slot, the unknown hosts last and the scale of the chart.
func TestAdfPeerStatsAggregate(t *testing.T) {
	from := peerstats.HourOf(time.Now()) - 23*3600
	to := from + 24*3600
	rng := peerStatsRangeByKey("24h")
	peers := map[int64]*models.Peer{
		1: {ID: 1, Name: "news-ok", Enabled: true},
		2: {ID: 2, Name: "news-lax", Enabled: false},
	}
	buckets := []peerstats.Bucket{
		{Peer: peerstats.PeerRef{ID: 1, Name: "old-name"}, Dir: peerstats.In, Hour: from, Counters: peerstats.Counters{Offered: 10, Accepted: 6, Refused: 4, AccBytes: 100, Conns: 1}},
		{Peer: peerstats.PeerRef{ID: 1, Name: "old-name"}, Dir: peerstats.In, Hour: from + 3600, Counters: peerstats.Counters{Offered: 10, Accepted: 4, Rejected: 6, AccBytes: 100, Conns: 1}},
		{Peer: peerstats.PeerRef{ID: 2, Name: "news-lax"}, Dir: peerstats.In, Hour: from + 3600, Counters: peerstats.Counters{Denied: 37, Conns: 5}},
		{Peer: peerstats.UnknownPeer, Dir: peerstats.In, Hour: from, Counters: peerstats.Counters{Denied: 1}},
		// Out of range and the other direction: both must be left out.
		{Peer: peerstats.PeerRef{ID: 1, Name: "news-ok"}, Dir: peerstats.In, Hour: from - 3600, Counters: peerstats.Counters{Accepted: 1000}},
		{Peer: peerstats.PeerRef{ID: 1, Name: "news-ok"}, Dir: peerstats.In, Hour: to, Counters: peerstats.Counters{Accepted: 2000}},
		{Peer: peerstats.PeerRef{ID: 1, Name: "news-ok"}, Dir: peerstats.Out, Hour: from, Counters: peerstats.Counters{Accepted: 3000}},
	}
	queues := map[int64]int64{1: 42}

	in := buildPeerStatsDirection("Incoming", peerstats.In, buckets, from, to, rng, peers, nil)
	if in.Totals.Accepted != 10 || in.Totals.Refused != 4 || in.Totals.Rejected != 6 || in.Totals.Denied != 38 {
		t.Errorf("incoming totals = %+v", in.Totals)
	}
	if in.AccHuman != "200 B" {
		t.Errorf("accepted bytes = %q, want \"200 B\"", in.AccHuman)
	}
	if len(in.Rows) != 3 {
		t.Fatalf("incoming rows = %d, want 3", len(in.Rows))
	}
	// Sorted by name with the unknown hosts last.
	if in.Rows[0].Name != "news-lax" || in.Rows[1].Name != "news-ok" || !in.Rows[2].Unknown {
		t.Errorf("row order = %q, %q, unknown=%v", in.Rows[0].Name, in.Rows[1].Name, in.Rows[2].Unknown)
	}
	// The current peer name wins over the one the bucket was written with.
	ok := in.Rows[1]
	if ok.Disabled || ok.Gone || ok.Offered != 20 || ok.Accepted != 10 || ok.Conns != 2 || ok.AcceptedPct != "50.0%" {
		t.Errorf("row of news-ok = %+v", ok)
	}
	lax := in.Rows[0]
	if !lax.Disabled || lax.Denied != 37 || lax.AcceptedPct != "—" {
		t.Errorf("row of news-lax = %+v", lax)
	}
	if in.Rows[2].QueueKnown {
		t.Error("the incoming table must not show a queue depth")
	}

	// One bar per hour; the tallest bar sets the scale.
	if len(in.Chart.Bars) != 24 || in.Chart.Empty {
		t.Fatalf("chart has %d bars, empty=%v", len(in.Chart.Bars), in.Chart.Empty)
	}
	if in.Chart.Max != 47 { // hour 2: 4 accepted + 6 rejected + 37 denied
		t.Errorf("chart max = %d, want 47", in.Chart.Max)
	}
	if n := len(in.Chart.Bars[1].Segs); n != 3 {
		t.Errorf("the second bar has %d segments, want 3 (accepted, bad, denied)", n)
	}
	if n := len(in.Chart.Bars[5].Segs); n != 0 {
		t.Errorf("an hour without traffic has %d segments, want 0", n)
	}

	// The outgoing direction takes its own buckets and the queue depth of the feed queue.
	out := buildPeerStatsDirection("Outgoing", peerstats.Out, buckets, from, to, rng, peers, queues)
	if out.Totals.Accepted != 3000 || len(out.Rows) != 1 {
		t.Fatalf("outgoing totals = %+v, rows = %d", out.Totals, len(out.Rows))
	}
	if !out.Rows[0].QueueKnown || out.Rows[0].Queue != 42 {
		t.Errorf("outgoing queue depth = %d (known=%v), want 42", out.Rows[0].Queue, out.Rows[0].QueueKnown)
	}

	// 30 days: one bar per day, the hours of a day summed into it.
	wide := peerStatsRangeByKey("30d")
	from30 := to - wide.Hours*3600
	day := buildPeerStatsDirection("Incoming", peerstats.In, buckets, from30, to, wide, peers, nil)
	if len(day.Chart.Bars) != 30 {
		t.Errorf("30 day chart has %d bars, want 30", len(day.Chart.Bars))
	}
	// The wider range also takes in the hour just before the 24 hour window.
	if day.Totals.Accepted != 1010 {
		t.Errorf("30 day totals accepted = %d, want 1010", day.Totals.Accepted)
	}

	// Nothing at all: an empty chart and no rows, not a chart with a zero scale.
	empty := buildPeerStatsDirection("Incoming", peerstats.In, nil, from, to, rng, peers, nil)
	if !empty.Chart.Empty || len(empty.Rows) != 0 {
		t.Errorf("empty direction: chart empty=%v, rows=%d", empty.Chart.Empty, len(empty.Rows))
	}
}

// TestAdfPeerStatsWindow: the window the page reads. An hourly range ends at the end of the
// current hour; a range whose bars are days starts at a UTC midnight, so a day bar is one
// calendar day and the traffic of today is not counted into yesterday's bar.
func TestAdfPeerStatsWindow(t *testing.T) {
	// A fixed instant, and the same instant in another zone: the window must not depend on the
	// zone of the web process, only on the UTC hours of the store.
	now := time.Date(2026, 11, 9, 14, 37, 12, 0, time.UTC)
	elsewhere := now.In(time.FixedZone("UTC-9", -9*3600))

	cases := []struct {
		key        string
		from, to   time.Time
		wantAnchor bool // the start must be a UTC midnight
	}{
		{key: "24h", from: time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)},
		{key: "7d", from: time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)},
		{key: "30d", from: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC), wantAnchor: true},
	}
	for _, tc := range cases {
		for _, in := range []time.Time{now, elsewhere} {
			from, to := peerStatsWindow(peerStatsRangeByKey(tc.key), in)
			if from != tc.from.Unix() || to != tc.to.Unix() {
				t.Errorf("%s window (now %s) = %s .. %s, want %s .. %s", tc.key, in,
					time.Unix(from, 0).UTC(), time.Unix(to, 0).UTC(), tc.from, tc.to)
			}
			if tc.wantAnchor && from%86400 != 0 {
				t.Errorf("%s window starts at %s, which is no UTC midnight", tc.key, time.Unix(from, 0).UTC())
			}
		}
	}

	// A day range that ends exactly at a UTC midnight is 30 whole days, not 31 bars.
	rng := peerStatsRangeByKey("30d")
	from, to := peerStatsWindow(rng, time.Date(2026, 11, 9, 23, 30, 0, 0, time.UTC))
	if to != time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Unix() || (to-from)/86400 != 29 {
		t.Errorf("30d window at a midnight = %s .. %s", time.Unix(from, 0).UTC(), time.Unix(to, 0).UTC())
	}
}

// TestAdfPeerStatsChartGeometry: every segment stays inside the viewBox, also when the minimum
// height of a tiny segment is added on the tallest bar, and a day bar says which day it counts.
func TestAdfPeerStatsChartGeometry(t *testing.T) {
	// 1000 accepted and 1 refused: the refused segment is bumped to its minimum height, which
	// would push it above the chart without the clamp.
	chart := peerStatsBuildChart([]peerstats.Counters{{Accepted: 1000, Refused: 1}}, peerstats.HourOf(time.Now()), 3600)
	if chart.Empty || len(chart.Bars) != 1 {
		t.Fatalf("chart empty=%v with %d bars", chart.Empty, len(chart.Bars))
	}
	for i, seg := range chart.Bars[0].Segs {
		y, err := strconv.ParseFloat(seg.Y, 64)
		if err != nil {
			t.Fatalf("segment %d has the unparsable y %q: %v", i, seg.Y, err)
		}
		h, err := strconv.ParseFloat(seg.H, 64)
		if err != nil {
			t.Fatalf("segment %d has the unparsable height %q: %v", i, seg.H, err)
		}
		if y < 0 || h <= 0 || y+h > peerStatsChartHeight+0.001 {
			t.Errorf("segment %d is outside the viewBox: y=%v h=%v (height %d)", i, y, h, peerStatsChartHeight)
		}
	}

	// A day bar is titled with its UTC day, an hour bar with its UTC hour.
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC).Unix()
	if got := peerStatsSlotTitle(day, 86400, peerstats.Counters{Accepted: 1}); !strings.HasPrefix(got, "2026-10-11 UTC: ") {
		t.Errorf("day bar title = %q", got)
	}
	if got := peerStatsSlotTitle(day+16*3600, 3600, peerstats.Counters{Accepted: 1}); !strings.HasPrefix(got, "2026-10-11 16:00 UTC: ") {
		t.Errorf("hour bar title = %q", got)
	}
}
