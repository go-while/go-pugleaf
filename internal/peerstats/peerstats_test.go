package peerstats

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedClock is the injected Now of the tests: no test looks at the wall clock, every one
// moves this clock by hand.
type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *fixedClock { return &fixedClock{t: t} }

func (c *fixedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ---- line formats ----

// TestSyslogLine: the header syslogd puts in front of innd's and innfeed's notices.
func TestSyslogLine(t *testing.T) {
	// "Mmm _d hh:mm:ss <host> <tag>[<pid>]: <msg>"; day 9 is padded with a blank.
	got := syslogLine(time.Date(2026, 11, 9, 3, 4, 5, 0, time.UTC), "mbox-mo1", "innd", 4242, "hello")
	want := "Nov  9 03:04:05 mbox-mo1 innd[4242]: hello"
	if got != want {
		t.Errorf("syslogLine =\n%q\nwant\n%q", got, want)
	}
	got = syslogLine(time.Date(2026, 1, 23, 22, 15, 0, 0, time.UTC), "lax1", "innfeed", 7, "x")
	want = "Jan 23 22:15:00 lax1 innfeed[7]: x"
	if got != want {
		t.Errorf("syslogLine =\n%q\nwant\n%q", got, want)
	}
}

// TestInndConnectedLine against innd/rc.c:662
//
//	notice("%s connected %d streaming %s", name ? name : addr, new->fd, ...)
func TestInndConnectedLine(t *testing.T) {
	if got, want := inndConnectedLine("news-lax", 7, true), "news-lax connected 7 streaming allowed"; got != want {
		t.Errorf("connected =\n%q\nwant\n%q", got, want)
	}
	if got, want := inndConnectedLine("news-lax", 7, false), "news-lax connected 7 streaming not allowed"; got != want {
		t.Errorf("connected =\n%q\nwant\n%q", got, want)
	}
	// A name with a blank in it would shift every later field of innreport's regexps.
	if got, want := inndConnectedLine("two words", 1, true), "two_words connected 1 streaming allowed"; got != want {
		t.Errorf("connected =\n%q\nwant\n%q", got, want)
	}
	if got, want := inndConnectedLine("", 1, true), "unknown connected 1 streaming allowed"; got != want {
		t.Errorf("connected =\n%q\nwant\n%q", got, want)
	}
}

// TestInndStatsLine against innd/nc.c:275 and innd/chan.c:321,332
//
//	"%s checkpoint seconds %lu accepted %lu refused %lu rejected %lu duplicate %lu"
//	" accepted size %llu duplicate size %llu rejected size %llu"
func TestInndStatsLine(t *testing.T) {
	c := Counters{
		Offered: 11, Accepted: 3, Refused: 2, Rejected: 1, Duplicate: 4,
		AccBytes: 5000, DupBytes: 600, RejBytes: 70, Denied: 9,
	}
	want := "news-lax:7 closed seconds 42 accepted 3 refused 2 rejected 1 duplicate 4" +
		" accepted size 5000 duplicate size 600 rejected size 70"
	if got := inndStatsLine(inndChanName("news-lax", 7), "closed", 42, c); got != want {
		t.Errorf("closed =\n%q\nwant\n%q", got, want)
	}
	want = "news-lax:7 checkpoint seconds 600 accepted 3 refused 2 rejected 1 duplicate 4" +
		" accepted size 5000 duplicate size 600 rejected size 70"
	if got := inndStatsLine(inndChanName("news-lax", 7), "checkpoint", 600, c); got != want {
		t.Errorf("checkpoint =\n%q\nwant\n%q", got, want)
	}
}

// TestInnfeedQueueHist against gCalcHostBlStat, innfeed/host.c:3831, with one sample and the
// default high water mark of 10 (innfeed/innfeed.h:52).
func TestInnfeedQueueHist(t *testing.T) {
	for _, tc := range []struct {
		depth int64
		want  string
	}{
		{0, "100,0,0,0,0,0"},  // blNone
		{1, "0,100,0,0,0,0"},  // blQuartile[0]: (4*1)/10 = 0
		{3, "0,0,100,0,0,0"},  // blQuartile[1]: (4*3)/10 = 1
		{5, "0,0,0,100,0,0"},  // blQuartile[2]: (4*5)/10 = 2
		{8, "0,0,0,0,100,0"},  // blQuartile[3]: (4*8)/10 = 3
		{10, "0,0,0,0,0,100"}, // blFull: backlog >= highwater
		{99, "0,0,0,0,0,100"},
	} {
		if got := innfeedQueueHist(tc.depth); got != tc.want {
			t.Errorf("innfeedQueueHist(%d) = %q, want %q", tc.depth, got, tc.want)
		}
	}
}

// TestInnfeedStatsLine against innfeed/host.c:2890 (checkpoint) and :2929 (final)
//
//	"%s checkpoint seconds %ld offered %u accepted %u refused %u rejected %u missing %u"
//	" accsize %.0f rejsize %.0f spooled %u on_close %u unspooled %u deferred %u/%.1f"
//	" requeued %u queue %.1f/%u:%.0f,%.0f,%.0f,%.0f,%.0f,%.0f"
func TestInnfeedStatsLine(t *testing.T) {
	c := Counters{
		Offered: 10, Accepted: 7, Refused: 2, Rejected: 1, Missing: 3, Deferred: 4,
		AccBytes: 1234, RejBytes: 56,
	}
	want := "peerb checkpoint seconds 60 offered 10 accepted 7 refused 2 rejected 1 missing 3" +
		" accsize 1234 rejsize 56 spooled 0 on_close 0 unspooled 0 deferred 4/0.0" +
		" requeued 0 queue 5.0/10:0,0,0,100,0,0"
	if got := innfeedStatsLine("peerb", "checkpoint", 60, c, 5); got != want {
		t.Errorf("checkpoint =\n%q\nwant\n%q", got, want)
	}
	want = "peerb final seconds 3600 offered 10 accepted 7 refused 2 rejected 1 missing 3" +
		" accsize 1234 rejsize 56 spooled 0 on_close 0 unspooled 0 deferred 4/0.0" +
		" requeued 0 queue 0.0/10:100,0,0,0,0,0"
	if got := innfeedStatsLine("peerb", "final", 3600, c, 0); got != want {
		t.Errorf("final =\n%q\nwant\n%q", got, want)
	}
}

// TestInnfeedGlobalLine against innfeed/host.c:2005 / :2472 (per host) and :2021 ("ME").
//
//	"%s global seconds %ld offered %u accepted %u refused %u rejected %u missing %u"
//	" accsize %.0f rejsize %.0f spooled %u unspooled %u"
func TestInnfeedGlobalLine(t *testing.T) {
	c := Counters{
		Offered: 10, Accepted: 7, Refused: 2, Rejected: 1, Missing: 3,
		AccBytes: 1234, RejBytes: 56,
	}
	want := "peerb global seconds 3600 offered 10 accepted 7 refused 2 rejected 1 missing 3" +
		" accsize 1234 rejsize 56 spooled 0 unspooled 0"
	if got := innfeedGlobalLine("peerb", 3600, c); got != want {
		t.Errorf("global =\n%q\nwant\n%q", got, want)
	}
	want = "ME global seconds 3600 offered 10 accepted 7 refused 2 rejected 1 missing 3" +
		" accsize 1234 rejsize 56 spooled 0 unspooled 0"
	if got := innfeedGlobalLine("ME", 3600, c); got != want {
		t.Errorf("ME global =\n%q\nwant\n%q", got, want)
	}
}

// ---- store ----

func openTestStore(t *testing.T) (Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg", "peer-stats.sq3")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("store Close: %v", err)
		}
	})
	return st, path
}

// TestStoreAddBucketsAdds: the same bucket written twice adds up, and the peer name follows
// the latest write.
func TestStoreAddBucketsAdds(t *testing.T) {
	st, _ := openTestStore(t)
	hour := HourOf(time.Date(2026, 11, 9, 3, 30, 0, 0, time.UTC))
	b := Bucket{Peer: PeerRef{ID: 2, Name: "lax1"}, Dir: In, Hour: hour}
	b.Accepted, b.AccBytes, b.Conns = 3, 300, 1
	if err := st.AddBuckets([]Bucket{b}); err != nil {
		t.Fatalf("AddBuckets: %v", err)
	}
	b2 := Bucket{Peer: PeerRef{ID: 2, Name: "news-lax"}, Dir: In, Hour: hour}
	b2.Accepted, b2.Refused, b2.AccBytes, b2.ConnSeconds = 4, 1, 400, 17
	if err := st.AddBuckets([]Bucket{b2}); err != nil {
		t.Fatalf("AddBuckets: %v", err)
	}
	got, err := st.ReadRange(hour, hour+3600)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReadRange returned %d buckets, want 1: %+v", len(got), got)
	}
	want := Bucket{Peer: PeerRef{ID: 2, Name: "news-lax"}, Dir: In, Hour: hour}
	want.Accepted, want.Refused, want.AccBytes, want.Conns, want.ConnSeconds = 7, 1, 700, 1, 17
	if got[0] != want {
		t.Errorf("bucket = %+v, want %+v", got[0], want)
	}
	// An all-zero bucket must not create a row.
	if err := st.AddBuckets([]Bucket{{Peer: PeerRef{ID: 9, Name: "idle"}, Dir: Out, Hour: hour}}); err != nil {
		t.Fatalf("AddBuckets zero: %v", err)
	}
	got, err = st.ReadRange(hour, hour+3600)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("an all-zero bucket created a row: %+v", got)
	}
}

// TestStoreReadRangeOrder: ordered by hour, peer, dir, and bounded by [from, to).
func TestStoreReadRangeOrder(t *testing.T) {
	st, _ := openTestStore(t)
	h0 := HourOf(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	var bs []Bucket
	for _, spec := range []struct {
		id   int64
		dir  Dir
		hour int64
	}{
		{2, Out, h0 + 3600}, {2, In, h0 + 3600}, {1, In, h0 + 3600},
		{1, In, h0}, {1, In, h0 + 7200},
	} {
		b := Bucket{Peer: PeerRef{ID: spec.id, Name: fmt.Sprintf("p%d", spec.id)}, Dir: spec.dir, Hour: spec.hour}
		b.Offered = 1
		bs = append(bs, b)
	}
	if err := st.AddBuckets(bs); err != nil {
		t.Fatalf("AddBuckets: %v", err)
	}
	got, err := st.ReadRange(h0, h0+7200)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	var keys []string
	for _, b := range got {
		keys = append(keys, fmt.Sprintf("%d/%d/%s", b.Hour-h0, b.Peer.ID, b.Dir))
	}
	want := []string{"0/1/in", "3600/1/in", "3600/2/in", "3600/2/out"}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Errorf("ReadRange order = %v, want %v", keys, want)
	}
	// An empty or inverted range reads nothing.
	if got, err := st.ReadRange(h0+7200, h0); err != nil || got != nil {
		t.Errorf("ReadRange inverted = %v, %v, want nil, nil", got, err)
	}
}

// TestStorePrune drops the buckets below beforeHour and counts them.
func TestStorePrune(t *testing.T) {
	st, _ := openTestStore(t)
	h0 := HourOf(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	var bs []Bucket
	for i := int64(0); i < 5; i++ {
		b := Bucket{Peer: PeerRef{ID: 1, Name: "p1"}, Dir: In, Hour: h0 + i*3600}
		b.Accepted = 1
		bs = append(bs, b)
	}
	if err := st.AddBuckets(bs); err != nil {
		t.Fatalf("AddBuckets: %v", err)
	}
	n, err := st.Prune(h0 + 2*3600)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Errorf("Prune deleted %d rows, want 2", n)
	}
	left, err := st.ReadRange(h0, h0+100*3600)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if len(left) != 3 || left[0].Hour != h0+2*3600 {
		t.Errorf("after Prune: %d buckets, first hour %d, want 3 from %d", len(left), left[0].Hour, h0+2*3600)
	}
	if n, err := st.Prune(h0); err != nil || n != 0 {
		t.Errorf("Prune with nothing to do = %d, %v, want 0, nil", n, err)
	}
}

// TestOpenReadOnly: a missing file fails cleanly (os.ErrNotExist, the web's "no data yet")
// and is not created; an existing one is readable and not writable.
func TestOpenReadOnly(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.sq3")
	if _, err := OpenReadOnly(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly of a missing file = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly created the file: %v", err)
	}
	if _, err := OpenReadOnly(""); err == nil {
		t.Error("OpenReadOnly of an empty path succeeded")
	}

	path := filepath.Join(dir, "peer-stats.sq3")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	hour := HourOf(time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC))
	b := Bucket{Peer: PeerRef{ID: 3, Name: "peerb"}, Dir: Out, Hour: hour}
	b.Offered, b.Accepted = 2, 2
	if err := st.AddBuckets([]Bucket{b}); err != nil {
		t.Fatalf("AddBuckets: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() {
		if err := ro.Close(); err != nil {
			t.Errorf("read-only Close: %v", err)
		}
	}()
	got, err := ro.ReadRange(hour, hour+3600)
	if err != nil {
		t.Fatalf("read-only ReadRange: %v", err)
	}
	if len(got) != 1 || got[0].Accepted != 2 || got[0].Dir != Out {
		t.Errorf("read-only ReadRange = %+v", got)
	}
	if err := ro.AddBuckets([]Bucket{b}); err == nil {
		t.Error("AddBuckets on a read-only store succeeded")
	}
	if _, err := ro.Prune(hour); err == nil {
		t.Error("Prune on a read-only store succeeded")
	}
}

// TestStoreClosed: every method answers ErrStoreClosed after Close, and Close twice is fine.
func TestStoreClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-stats.sq3")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := st.AddBuckets([]Bucket{{Peer: PeerRef{ID: 1}, Dir: In, Hour: 3600, Counters: Counters{Offered: 1}}}); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("AddBuckets after Close = %v, want ErrStoreClosed", err)
	}
	if _, err := st.ReadRange(0, 3600); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("ReadRange after Close = %v, want ErrStoreClosed", err)
	}
	if _, err := st.Prune(3600); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("Prune after Close = %v, want ErrStoreClosed", err)
	}
}

// ---- collector ----

// TestCollectorHourRollover: events on both sides of an hour boundary land in two buckets.
func TestCollectorHourRollover(t *testing.T) {
	st, _ := openTestStore(t)
	clk := newClock(time.Date(2026, 11, 9, 10, 59, 30, 0, time.UTC))
	svc, err := New(Config{Store: st, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 4, Name: "lax1"}
	svc.Add(peer, In, Counters{Offered: 1, Accepted: 1, AccBytes: 100})
	clk.add(time.Minute) // 11:00:30
	svc.Add(peer, In, Counters{Offered: 2, Refused: 2})
	svc.Stop()

	h10 := HourOf(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC))
	got, err := st.ReadRange(h10, h10+2*3600)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(got), got)
	}
	if got[0].Hour != h10 || got[0].Accepted != 1 || got[0].AccBytes != 100 || got[0].Offered != 1 {
		t.Errorf("bucket 10:00 = %+v", got[0])
	}
	if got[1].Hour != h10+3600 || got[1].Offered != 2 || got[1].Refused != 2 || got[1].Accepted != 0 {
		t.Errorf("bucket 11:00 = %+v", got[1])
	}
	if got[0].Peer.Name != "lax1" || got[0].Dir != In {
		t.Errorf("bucket peer/dir = %+v", got[0])
	}
}

// TestCollectorStopFlushesAndIsIdempotent: Stop without Start still writes the buffered
// buckets, and a second Stop changes nothing.
func TestCollectorStopFlushesAndIsIdempotent(t *testing.T) {
	st, _ := openTestStore(t)
	clk := newClock(time.Date(2026, 11, 9, 1, 15, 0, 0, time.UTC))
	svc, err := New(Config{Store: st, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 5, Name: "peerb"}
	svc.Connected(peer, Out, "10.0.0.1:433")
	svc.Add(peer, Out, Counters{Offered: 3, Accepted: 3, AccBytes: 900})
	svc.Closed(peer, Out, Counters{Offered: 3, Accepted: 3, AccBytes: 900}, 30*time.Second)
	svc.Stop()
	svc.Stop() // idempotent

	hour := HourOf(clk.now())
	got, err := st.ReadRange(hour, hour+3600)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d buckets, want 1: %+v", len(got), got)
	}
	want := Bucket{Peer: peer, Dir: Out, Hour: hour}
	want.Offered, want.Accepted, want.AccBytes, want.Conns, want.ConnSeconds = 3, 3, 900, 1, 30
	if got[0] != want {
		t.Errorf("bucket = %+v, want %+v", got[0], want)
	}
	// A second Stop must not have written the buckets again.
	if got[0].Accepted != 3 {
		t.Errorf("accepted = %d after two Stops, want 3", got[0].Accepted)
	}
	// Start after Stop does not launch anything (Stop would hang on the goroutine).
	svc.Start()
}

// TestCollectorStartStop: the goroutine flushes on its tick and stops on Stop.
func TestCollectorStartStop(t *testing.T) {
	st, _ := openTestStore(t)
	clk := newClock(time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC))
	svc, err := New(Config{
		Store:           st,
		FlushEvery:      time.Millisecond,
		CheckpointEvery: time.Millisecond,
		Now:             clk.now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.Start()
	svc.Start() // idempotent
	peer := PeerRef{ID: 6, Name: "p6"}
	for i := 0; i < 50; i++ {
		svc.Add(peer, In, Counters{Offered: 1, Accepted: 1, AccBytes: 10})
	}
	svc.Stop()
	hour := HourOf(clk.now())
	got, err := st.ReadRange(hour, hour+3600)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if len(got) != 1 || got[0].Accepted != 50 || got[0].AccBytes != 500 {
		t.Errorf("after Start/Stop: %+v, want accepted 50 acc_bytes 500", got)
	}
}

// errStore fails the first AddBuckets and then records what it gets.
type errStore struct {
	mu    sync.Mutex
	fails int
	got   []Bucket
}

func (s *errStore) AddBuckets(b []Bucket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails > 0 {
		s.fails--
		return errors.New("store is busy")
	}
	s.got = append(s.got, b...)
	return nil
}

func (s *errStore) ReadRange(int64, int64) ([]Bucket, error) { return nil, nil }
func (s *errStore) Prune(int64) (int64, error)               { return 0, nil }
func (s *errStore) Close() error                             { return nil }

// TestCollectorFlushKeepsBucketsOnError: a failed flush keeps the buckets and merges them
// with what arrived meanwhile, so nothing is lost.
func TestCollectorFlushKeepsBucketsOnError(t *testing.T) {
	st := &errStore{fails: 1}
	clk := newClock(time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC))
	svc, err := New(Config{Store: st, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 7, Name: "p7"}
	svc.Add(peer, In, Counters{Accepted: 2})
	svc.flush() // fails, keeps the bucket
	st.mu.Lock()
	n := len(st.got)
	st.mu.Unlock()
	if n != 0 {
		t.Fatalf("the failed flush stored %d buckets", n)
	}
	svc.Add(peer, In, Counters{Accepted: 3})
	svc.Stop() // flushes
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.got) != 1 {
		t.Fatalf("stored %d buckets, want 1: %+v", len(st.got), st.got)
	}
	if st.got[0].Accepted != 5 {
		t.Errorf("accepted = %d, want 5 (2 kept + 3 new)", st.got[0].Accepted)
	}
}

// TestCollectorWithoutStore: a collector with no store only logs and never panics.
func TestCollectorWithoutStore(t *testing.T) {
	clk := newClock(time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC))
	svc, err := New(Config{Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.Add(PeerRef{ID: 8, Name: "p8"}, In, Counters{Accepted: 1})
	svc.SetQueue(PeerRef{ID: 8, Name: "p8"}, 4)
	if got := svc.Queue(8); got != 4 {
		t.Errorf("Queue = %d, want 4", got)
	}
	svc.prune() // no store: no-op
	svc.Stop()
}

// ---- the inn log file ----

// readLog returns the message part (after "prog[pid]: ") of each line of the log file.
func readLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var msgs []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		i := strings.Index(line, "]: ")
		if i < 0 {
			t.Fatalf("log line without a syslog header: %q", line)
		}
		msgs = append(msgs, line[i+3:])
	}
	return msgs
}

// TestCollectorINNLogLines: the lines the collector writes for one incoming and one outgoing
// peer, in order, including the checkpoint and the final lines.
func TestCollectorINNLogLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "news.notice")
	clk := newClock(time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC))
	svc, err := New(Config{INNLogPath: logPath, Host: "mbox-mo1", Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	in := PeerRef{ID: 1, Name: "lax1"}
	out := PeerRef{ID: 2, Name: "news-lax"}
	svc.Connected(in, In, "10.0.0.9:51000")
	svc.Connected(out, Out, "news-lax.pugleaf.net:433")
	svc.SetQueue(out, 3)
	svc.Add(in, In, Counters{Offered: 2, Accepted: 1, Refused: 1, AccBytes: 1000})
	svc.Add(out, Out, Counters{Offered: 2, Accepted: 2, AccBytes: 2000})
	clk.add(10 * time.Minute)
	svc.checkpointLines()
	svc.Closed(in, In, Counters{Offered: 2, Accepted: 1, Refused: 1, AccBytes: 1000}, 600*time.Second)
	svc.Stop()

	want := []string{
		"lax1 connected 1 streaming allowed",
		"news-lax:2 connected",
		"lax1:1 checkpoint seconds 600 accepted 1 refused 1 rejected 0 duplicate 0" +
			" accepted size 1000 duplicate size 0 rejected size 0",
		"news-lax checkpoint seconds 600 offered 2 accepted 2 refused 0 rejected 0 missing 0" +
			" accsize 2000 rejsize 0 spooled 0 on_close 0 unspooled 0 deferred 0/0.0" +
			" requeued 0 queue 3.0/10:0,0,100,0,0,0",
		"lax1:1 closed seconds 600 accepted 1 refused 1 rejected 0 duplicate 0" +
			" accepted size 1000 duplicate size 0 rejected size 0",
		// Stop: the totals since the first event of each peer and direction.
		"lax1:0 checkpoint seconds 600 accepted 1 refused 1 rejected 0 duplicate 0" +
			" accepted size 1000 duplicate size 0 rejected size 0",
		"news-lax global seconds 600 offered 2 accepted 2 refused 0 rejected 0 missing 0" +
			" accsize 2000 rejsize 0 spooled 0 unspooled 0",
		"ME global seconds 600 offered 2 accepted 2 refused 0 rejected 0 missing 0" +
			" accsize 2000 rejsize 0 spooled 0 unspooled 0",
	}
	got := readLog(t, logPath)
	if len(got) != len(want) {
		t.Fatalf("log has %d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d =\n%q\nwant\n%q", i, got[i], want[i])
		}
	}
	// The syslog header carries the configured host and this process' pid.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	head := fmt.Sprintf("Nov  9 07:00:00 mbox-mo1 innd[%d]: ", os.Getpid())
	if !strings.HasPrefix(string(data), head) {
		t.Errorf("first line does not start with %q:\n%s", head, string(data))
	}
}

// TestCollectorUnknownPeerLabel: a feed command from a host that matches no peer row is
// logged under its address, the way innd logs a channel it has no name for (innd/rc.c:662).
func TestCollectorUnknownPeerLabel(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "news.notice")
	clk := newClock(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC))
	svc, err := New(Config{INNLogPath: logPath, Host: "mbox-mo1", Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.Connected(UnknownPeer, In, "203.0.113.7:40000")
	svc.Add(UnknownPeer, In, Counters{Offered: 1, Denied: 1})
	svc.Stop()
	got := readLog(t, logPath)
	if len(got) == 0 || !strings.HasPrefix(got[0], "203.0.113.7:40000 connected 1 ") {
		t.Errorf("unknown peer line = %v", got)
	}
}

// TestCollectorCheckpointOnlyWithActivity: a peer with nothing new gets no checkpoint line.
func TestCollectorCheckpointOnlyWithActivity(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "news.notice")
	clk := newClock(time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC))
	svc, err := New(Config{INNLogPath: logPath, Host: "h", Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 1, Name: "p1"}
	svc.Add(peer, In, Counters{Accepted: 1})
	clk.add(time.Minute)
	svc.checkpointLines()
	first := len(readLog(t, logPath))
	clk.add(time.Minute)
	svc.checkpointLines() // nothing happened since: no line
	if n := len(readLog(t, logPath)); n != first {
		t.Errorf("an idle checkpoint wrote %d extra lines", n-first)
	}
	svc.Stop()
}

// TestINNLogReopenAfterRename: logrotate's rename is noticed on the checkpoint tick and the
// log file is reopened at its old path.
func TestINNLogReopenAfterRename(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "news.notice")
	clk := newClock(time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC))
	svc, err := New(Config{INNLogPath: logPath, Host: "h", Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.Connected(PeerRef{ID: 1, Name: "p1"}, In, "10.0.0.1:119")
	rotated := logPath + ".1"
	if err := os.Rename(logPath, rotated); err != nil {
		t.Fatalf("rename: %v", err)
	}
	svc.innlog.reopenIfRotated()
	svc.Connected(PeerRef{ID: 1, Name: "p1"}, In, "10.0.0.1:119")
	svc.Stop()

	old := readLog(t, rotated)
	if len(old) != 1 || !strings.HasPrefix(old[0], "p1 connected 1 ") {
		t.Errorf("rotated file = %v", old)
	}
	fresh := readLog(t, logPath)
	if len(fresh) == 0 || !strings.HasPrefix(fresh[0], "p1 connected 2 ") {
		t.Errorf("reopened file = %v", fresh)
	}
	// Reopening when nothing moved keeps the same file and loses nothing.
	before := len(fresh)
	svc.innlog.reopenIfRotated()
	if n := len(readLog(t, logPath)); n != before {
		t.Errorf("a no-op reopen changed the file: %d lines, want %d", n, before)
	}
}

// TestINNLogMissingDir: New fails when the log file cannot be opened.
func TestINNLogMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "news.notice")
	if _, err := New(Config{INNLogPath: path}); err == nil {
		t.Error("New with an unopenable log path succeeded")
	}
}

// TestConfigDefaults: the unset fields get the documented defaults.
func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.FlushEvery != DefaultFlushEvery || c.CheckpointEvery != DefaultCheckpointEvery ||
		c.Retention != DefaultRetention {
		t.Errorf("defaults = %v/%v/%v, want %v/%v/%v",
			c.FlushEvery, c.CheckpointEvery, c.Retention,
			DefaultFlushEvery, DefaultCheckpointEvery, DefaultRetention)
	}
	if c.Now == nil {
		t.Error("Now default is nil")
	}
	if c.Host == "" {
		t.Error("Host default is empty")
	}
}

// TestServicePrune: the background prune uses HourOf(now - retention).
func TestServicePrune(t *testing.T) {
	st, _ := openTestStore(t)
	now := time.Date(2026, 11, 9, 12, 30, 0, 0, time.UTC)
	clk := newClock(now)
	svc, err := New(Config{Store: st, Retention: 2 * time.Hour, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := HourOf(now)
	var bs []Bucket
	for i := int64(-4); i <= 0; i++ {
		b := Bucket{Peer: PeerRef{ID: 1, Name: "p1"}, Dir: In, Hour: h + i*3600}
		b.Accepted = 1
		bs = append(bs, b)
	}
	if err := st.AddBuckets(bs); err != nil {
		t.Fatalf("AddBuckets: %v", err)
	}
	svc.prune()
	svc.Stop()
	left, err := st.ReadRange(h-10*3600, h+3600)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	// Retention 2h at 12:30 keeps the buckets from hour 10:00 on.
	if len(left) != 3 || left[0].Hour != h-2*3600 {
		t.Errorf("after prune: %d buckets from %d, want 3 from %d", len(left), left[0].Hour, h-2*3600)
	}
}

// TestCollectorConcurrentEvents: the event methods are called from every connection
// goroutine while the background goroutine flushes; this is here for the race detector.
func TestCollectorConcurrentEvents(t *testing.T) {
	st, _ := openTestStore(t)
	clk := newClock(time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC))
	logPath := filepath.Join(t.TempDir(), "news.notice")
	svc, err := New(Config{
		Store:           st,
		INNLogPath:      logPath,
		Host:            "h",
		FlushEvery:      time.Millisecond,
		CheckpointEvery: time.Millisecond,
		Now:             clk.now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.Start()
	const workers, each = 8, 100
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			peer := PeerRef{ID: int64(w%3) + 1, Name: fmt.Sprintf("p%d", w%3+1)}
			dir := In
			if w%2 == 1 {
				dir = Out
			}
			svc.Connected(peer, dir, "10.0.0.1:119")
			for i := 0; i < each; i++ {
				svc.Add(peer, dir, Counters{Offered: 1, Accepted: 1, AccBytes: 7})
				svc.SetQueue(peer, int64(i%12))
			}
			svc.Closed(peer, dir, Counters{Offered: each, Accepted: each}, time.Second)
		}(w)
	}
	wg.Wait()
	svc.Stop()
	hour := HourOf(clk.now())
	got, err := st.ReadRange(hour, hour+3600)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	var accepted, conns int64
	for _, b := range got {
		accepted += b.Accepted
		conns += b.Conns
	}
	if accepted != workers*each {
		t.Errorf("accepted = %d, want %d", accepted, workers*each)
	}
	if conns != workers {
		t.Errorf("conns = %d, want %d", conns, workers)
	}
}

// TestDirRoundTrip: the dir column survives a store round trip.
func TestDirRoundTrip(t *testing.T) {
	for _, d := range []Dir{In, Out} {
		if got := dirFromString(d.String()); got != d {
			t.Errorf("dirFromString(%q) = %v, want %v", d.String(), got, d)
		}
	}
	if got := dirFromString("nonsense"); got != 0 {
		t.Errorf("dirFromString of an unknown value = %v, want 0", got)
	}
}
