package web

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/peerstats"
)

// Admin peering statistics page (GET /admin/peers/stats, plan peering-stats, slice web-stats):
// what peers sent us and what we sent them, per peer, per direction and per hour. The hourly
// buckets are written by the nntp-server process into data/cfg/peer-stats.sq3; this page only
// reads them, one request at a time, through the opener SetPeerStatsOpener installs (cmd/web
// wires peerstats.OpenReadOnly). Without an opener or without that file the page says that
// there are no statistics yet - a web host that runs no nntp-server is not an error.
//
// The page is the answer to "what does that peer actually send us?" before its peer row is
// enabled: a disabled peer row still counts the feed commands we refuse it, as Denied, so its
// row shows "disabled" and a Denied count instead of nothing at all.

// PeerStatsReader is what this page needs from the peer statistics store: the hourly buckets of
// a time range, and a Close for the file it opened. peerstats.OpenReadOnly returns one.
type PeerStatsReader interface {
	ReadRange(fromHour, toHour int64) ([]peerstats.Bucket, error)
	Close() error
}

// PeerStatsOpener opens the peer statistics store at path for reading.
type PeerStatsOpener func(path string) (PeerStatsReader, error)

var (
	peerStatsMu   sync.RWMutex
	openPeerStats PeerStatsOpener // nil: the page has no statistics to show
)

// SetPeerStatsOpener installs the opener the statistics page uses to read the hourly peer
// buckets; cmd/web wires peerstats.OpenReadOnly. The store is a SQLite file written by the
// nntp-server process, so the web process opens it per request, reads and closes it again.
// Without an opener the page says that there are no statistics yet.
func SetPeerStatsOpener(o PeerStatsOpener) {
	peerStatsMu.Lock()
	openPeerStats = o
	peerStatsMu.Unlock()
}

// PeerStatsRange is one entry of the range selector: how far back the page looks and how wide
// one bar of its charts is (one hour, or one day for the 30 day range).
type PeerStatsRange struct {
	Key       string // the ?range= value
	Label     string // the button text
	Hours     int64  // hours back from the end of the range
	SlotHours int64  // hours per chart bar
}

// peerStatsRanges are the ranges the page offers; the first one is the default.
var peerStatsRanges = []PeerStatsRange{
	{Key: "24h", Label: "24 hours", Hours: 24, SlotHours: 1},
	{Key: "7d", Label: "7 days", Hours: 7 * 24, SlotHours: 1},
	{Key: "30d", Label: "30 days", Hours: 30 * 24, SlotHours: 24},
}

// peerStatsRangeByKey returns the range that key names, or the default range for anything else (an
// unknown, empty or hand-edited ?range=).
func peerStatsRangeByKey(key string) PeerStatsRange {
	for _, r := range peerStatsRanges {
		if r.Key == key {
			return r
		}
	}
	return peerStatsRanges[0]
}

// Chart geometry in viewBox units. The SVG is drawn with preserveAspectRatio="none" into a
// fixed pixel height, so the bars always fill the width of the card and the page never grows
// wider than its column, whatever the number of bars.
const (
	peerStatsChartWidth  = 1000
	peerStatsChartHeight = 100
	peerStatsBarGap      = 0.18 // share of a slot left empty between two bars
)

// PeerStatsSeg is one stacked segment of a chart bar: accepted, bad (refused + rejected +
// duplicate) or denied. The coordinates are viewBox units, already formatted, and Class names
// one of the three colours the template's stylesheet defines.
type PeerStatsSeg struct {
	Y     string
	H     string
	Class string
}

// PeerStatsBar is one hour (or one day in the 30 day range) of a chart.
type PeerStatsBar struct {
	X     string
	W     string
	Segs  []PeerStatsSeg
	Title string // the tooltip: the slot's time and its numbers
}

// PeerStatsChart is the inline SVG bar chart of one direction: accepted against
// refused/rejected/duplicate against denied, per hour or per day.
type PeerStatsChart struct {
	Bars     []PeerStatsBar
	Max      int64  // the articles the tallest bar stands for (the scale top)
	MaxLabel string // Max as the axis caption shows it
	Empty    bool   // nothing happened in the whole range
	Width    int
	Height   int
}

// PeerStatsRow is one peer's line of a direction table. The counters are the sums over the
// range; the strings are what the table prints.
type PeerStatsRow struct {
	PeerID   int64
	Name     string
	Unknown  bool // peer ID 0: feed commands from hosts that match no peer row
	Disabled bool // the peer row exists and is disabled (it may neither feed us nor be fed)
	Gone     bool // counters of a peer row that no longer exists
	peerstats.Counters
	AccBytesHuman string
	AcceptedPct   string // accepted of offered, or "—" when nothing was offered
	Queue         int64  // outgoing queue depth from the feed queue file
	QueueKnown    bool
}

// PeerStatsDirection is one half of the page: the totals, one row per peer and the chart.
type PeerStatsDirection struct {
	Name     string // "Incoming" / "Outgoing"
	Dir      peerstats.Dir
	Incoming bool // the template picks its tiles and columns by this, not by Dir
	Totals   peerstats.Counters
	AccHuman string
	Rows     []*PeerStatsRow
	Chart    PeerStatsChart
}

// AdminPeerStatsPageData is the template data of admin_peer_stats.html.
type AdminPeerStatsPageData struct {
	TemplateData
	Ranges    []PeerStatsRange
	Range     string // the active range key
	Available bool   // false: no store file, no opener, or it could not be read
	FromLabel string // start of the range, UTC (the hours of the store are UTC)
	ToLabel   string // end of the range, UTC
	SlotLabel string // "hour" / "day": what one bar stands for
	// The peer rows themselves, so a host whose peers are all disabled still sees that it has
	// peers at all (shown in the empty state too).
	PeersEnabled int
	PeersTotal   int
	QueueTotal   int64 // articles waiting in the outgoing feed queue, over all peers
	QueueKnown   bool  // false: no feed queue file to read the depths from
	In           PeerStatsDirection
	Out          PeerStatsDirection
}

// adminPeerStatsPage renders GET /admin/peers/stats.
func (s *WebServer) adminPeerStatsPage(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	// The page has no form and shows no flash of its own: the peer forms redirect to
	// /admin/peers, which is where their messages belong.
	rng := peerStatsRangeByKey(strings.TrimSpace(c.Query("range")))
	fromHour, toHour := peerStatsWindow(rng, time.Now())
	buckets, available := s.peerStatsBuckets(fromHour, toHour)

	// The peers and the queue depths do not come from the statistics store, so they are shown
	// whether or not there are statistics yet.
	peers := s.peerStatsPeers()
	queues := s.peerStatsQueues()

	data := AdminPeerStatsPageData{
		TemplateData: s.getBaseTemplateData(c, "Admin: Peering Statistics"),
		Ranges:       peerStatsRanges,
		Range:        rng.Key,
		Available:    available,
		// The store counts hours in UTC, so the whole page is UTC and says so.
		FromLabel:  time.Unix(fromHour, 0).UTC().Format("2006-01-02 15:04"),
		ToLabel:    time.Unix(toHour, 0).UTC().Format("2006-01-02 15:04"),
		SlotLabel:  "hour",
		PeersTotal: len(peers),
		QueueKnown: queues != nil,
	}
	if rng.SlotHours > 1 {
		data.SlotLabel = "day"
	}
	for _, p := range peers {
		if p.Enabled {
			data.PeersEnabled++
		}
	}
	for _, depth := range queues {
		data.QueueTotal += depth
	}

	if available {
		data.In = buildPeerStatsDirection("Incoming", peerstats.In, buckets, fromHour, toHour, rng, peers, nil)
		data.Out = buildPeerStatsDirection("Outgoing", peerstats.Out, buckets, fromHour, toHour, rng, peers, queues)
	}
	s.renderPage(c, http.StatusOK, data, "admin_peer_stats.html")
}

// peerStatsWindow is the [from, to) the page shows, in the Unix hours of the store. It ends at
// the end of the current hour, so the hour that is still being counted shows up (the writer
// flushes once a minute). A range whose bars are days starts at a UTC midnight instead, so one
// bar is one calendar day and today's traffic is not counted into yesterday's bar; its last bar
// is then the partial day up to now.
func peerStatsWindow(rng PeerStatsRange, now time.Time) (fromHour, toHour int64) {
	toHour = peerstats.HourOf(now) + 3600
	if rng.SlotHours <= 1 {
		return toHour - rng.Hours*3600, toHour
	}
	slot := rng.SlotHours * 3600
	// The slot the end falls into, minus the whole slots before it.
	return toHour - toHour%slot - (rng.Hours/rng.SlotHours-1)*slot, toHour
}

// peerStatsBuckets reads the hourly buckets of [fromHour, toHour). The second result is false
// when there is nothing to read: no opener wired, no store file on this host, or a store that
// could not be opened or read (logged, never an error page) - the page then says that there
// are no statistics yet instead of showing zeroes as if they were counted.
func (s *WebServer) peerStatsBuckets(fromHour, toHour int64) ([]peerstats.Bucket, bool) {
	peerStatsMu.RLock()
	open := openPeerStats
	peerStatsMu.RUnlock()
	if open == nil || s.DB == nil {
		return nil, false
	}
	path := peerstats.Path(s.DB.GetDataDir())
	if _, err := os.Stat(path); err != nil {
		return nil, false // no nntp-server has written statistics on this host
	}
	st, err := open(path)
	if err != nil {
		log.Printf("[WEB]: Failed to open the peer statistics %s: %v", path, err)
		return nil, false
	}
	if st == nil {
		return nil, false
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Printf("[WEB]: Failed to close the peer statistics %s: %v", path, err)
		}
	}()
	buckets, err := st.ReadRange(fromHour, toHour)
	if err != nil {
		log.Printf("[WEB]: Failed to read the peer statistics from %s: %v", path, err)
		return nil, false
	}
	return buckets, true
}

// peerStatsPeers returns the configured peers by ID, for the "disabled" badge and the current
// name of a peer. A failure is logged and the page renders without the badges.
func (s *WebServer) peerStatsPeers() map[int64]*models.Peer {
	if s.DB == nil {
		return nil
	}
	peers, err := s.DB.GetPeers()
	if err != nil {
		log.Printf("[WEB]: Failed to load the peers for the statistics page: %v", err)
		return nil
	}
	byID := make(map[int64]*models.Peer, len(peers))
	for _, p := range peers {
		byID[p.ID] = p
	}
	return byID
}

// peerStatsQueues returns the outgoing queue depth per peer ID, from the same feed queue file
// the peers page reads, or nil when there are no numbers.
func (s *WebServer) peerStatsQueues() map[int64]int64 {
	stats := s.feedStats()
	if stats == nil {
		return nil
	}
	depth := make(map[int64]int64, len(stats))
	for id, st := range stats {
		depth[id] = st.QueueDepth
	}
	return depth
}

// buildPeerStatsDirection sums the buckets of one direction into the totals, one row per peer
// and the chart bars. peers names the configured peers (for the badge and the current name),
// queues the outgoing queue depths (nil for the incoming direction).
func buildPeerStatsDirection(name string, d peerstats.Dir, buckets []peerstats.Bucket, fromHour, toHour int64, rng PeerStatsRange, peers map[int64]*models.Peer, queues map[int64]int64) PeerStatsDirection {
	slot := rng.SlotHours * 3600
	slots := int((toHour - fromHour + slot - 1) / slot)
	if slots < 1 {
		slots = 1
	}
	perSlot := make([]peerstats.Counters, slots)
	rows := make(map[int64]*PeerStatsRow)
	var totals peerstats.Counters

	for _, b := range buckets {
		if b.Dir != d || b.Hour < fromHour || b.Hour >= toHour {
			continue
		}
		totals.Add(b.Counters)
		if i := int((b.Hour - fromHour) / slot); i >= 0 && i < slots {
			perSlot[i].Add(b.Counters)
		}
		row := rows[b.Peer.ID]
		if row == nil {
			row = newPeerStatsRow(b.Peer, peers, queues)
			rows[b.Peer.ID] = row
		}
		row.Counters.Add(b.Counters)
	}

	out := PeerStatsDirection{
		Name:     name,
		Dir:      d,
		Incoming: d == peerstats.In,
		Totals:   totals,
		AccHuman: peerStatsBytes(totals.AccBytes),
		Rows:     make([]*PeerStatsRow, 0, len(rows)),
		Chart:    peerStatsBuildChart(perSlot, fromHour, slot),
	}
	for _, row := range rows {
		row.AccBytesHuman = peerStatsBytes(row.AccBytes)
		row.AcceptedPct = peerStatsPercent(row.Accepted, row.Offered)
		out.Rows = append(out.Rows, row)
	}
	sortPeerStatsRows(out.Rows)
	return out
}

// newPeerStatsRow starts the row of one peer: its name as it is configured now (the name the
// bucket was written with when the peer row is gone) and its state badge.
func newPeerStatsRow(ref peerstats.PeerRef, peers map[int64]*models.Peer, queues map[int64]int64) *PeerStatsRow {
	row := &PeerStatsRow{PeerID: ref.ID, Name: ref.Name}
	if ref.ID == 0 {
		row.Unknown = true
		if row.Name == "" {
			row.Name = peerstats.UnknownPeer.Name
		}
		return row
	}
	if p, ok := peers[ref.ID]; ok {
		if p.Name != "" {
			row.Name = p.Name
		}
		row.Disabled = !p.Enabled
	} else if peers != nil {
		row.Gone = true
	}
	if row.Name == "" {
		row.Name = "peer " + strconv.FormatInt(ref.ID, 10)
	}
	if depth, ok := queues[ref.ID]; ok {
		row.Queue = depth
		row.QueueKnown = true
	}
	return row
}

// sortPeerStatsRows orders the table: the configured peers by name, the unknown hosts last.
func sortPeerStatsRows(rows []*PeerStatsRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Unknown != b.Unknown {
			return b.Unknown
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		return a.PeerID < b.PeerID
	})
}

// peerStatsBuildChart turns the per-slot counters into stacked bars: accepted at the bottom,
// refused + rejected + duplicate above it, denied on top. Every bar is scaled to the tallest
// one, so the chart shows the shape of the traffic, and its tooltip carries the numbers.
func peerStatsBuildChart(perSlot []peerstats.Counters, fromHour, slot int64) PeerStatsChart {
	chart := PeerStatsChart{
		Width:  peerStatsChartWidth,
		Height: peerStatsChartHeight,
		Bars:   make([]PeerStatsBar, 0, len(perSlot)),
	}
	var max int64
	for _, c := range perSlot {
		if t := peerStatsBarTotal(c); t > max {
			max = t
		}
	}
	chart.Max = max
	chart.MaxLabel = strconv.FormatInt(max, 10)
	if max == 0 {
		chart.Empty = true
		return chart
	}

	step := float64(peerStatsChartWidth) / float64(len(perSlot))
	width := step * (1 - peerStatsBarGap)
	if width < 0.5 {
		width = step // too many bars for a gap: let them touch
	}
	for i, c := range perSlot {
		bar := PeerStatsBar{
			X:     peerStatsNum(float64(i) * step),
			W:     peerStatsNum(width),
			Title: peerStatsSlotTitle(fromHour+int64(i)*slot, slot, c),
		}
		// Stack from the bottom of the viewBox upwards.
		y := float64(peerStatsChartHeight)
		for _, seg := range []struct {
			n     int64
			class string
		}{
			{c.Accepted, "ps-acc"},
			{c.Refused + c.Rejected + c.Duplicate, "ps-bad"},
			{c.Denied, "ps-den"},
		} {
			if seg.n <= 0 {
				continue
			}
			h := float64(seg.n) / float64(max) * float64(peerStatsChartHeight)
			if h < 0.7 {
				h = 0.7 // a single article stays visible
			}
			y -= h
			if y < 0 {
				// The tallest bar fills the chart: the minimum height above must not push a
				// segment out of the viewBox, so it is cut off at the top instead.
				h += y
				y = 0
			}
			if h <= 0 {
				continue
			}
			bar.Segs = append(bar.Segs, PeerStatsSeg{Y: peerStatsNum(y), H: peerStatsNum(h), Class: seg.class})
		}
		chart.Bars = append(chart.Bars, bar)
	}
	return chart
}

// peerStatsBarTotal is the height one bar stands for: the three stacked series.
func peerStatsBarTotal(c peerstats.Counters) int64 {
	return c.Accepted + c.Refused + c.Rejected + c.Duplicate + c.Denied
}

// peerStatsSlotTitle is the tooltip of one bar: the hour (or the day) it counts, in UTC like
// every other time on the page and like the hours of the store itself.
func peerStatsSlotTitle(start, slot int64, c peerstats.Counters) string {
	t := time.Unix(start, 0).UTC()
	when := t.Format("2006-01-02 15:04") + " UTC"
	if slot >= 86400 {
		when = t.Format("2006-01-02") + " UTC"
	}
	return fmt.Sprintf("%s: accepted %d, refused %d, rejected %d, duplicate %d, denied %d",
		when, c.Accepted, c.Refused, c.Rejected, c.Duplicate, c.Denied)
}

// peerStatsNum formats a viewBox coordinate.
func peerStatsNum(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// peerStatsPercent is n of total as the table prints it.
func peerStatsPercent(n, total int64) string {
	if total <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", float64(n)/float64(total)*100)
}

// peerStatsBytes formats a byte count for the table (same scale as the analyzer's output).
func peerStatsBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
	}
}
