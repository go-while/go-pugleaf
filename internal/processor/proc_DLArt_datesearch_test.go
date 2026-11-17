package processor

// Start-by-date search (plan fetcher-date-search-holes; report of uarc-py-28cb, usenet_archiver):
// FindStartArticleByDate must never start above the first article on or after the target date.
// Before the fix, an XOVER error, a hole (no article at the probe number) or an unreadable Date
// at a probe point always moved the search up, so a date run could skip articles. The fake
// provider serves GROUP and XOVER over articles 1..dsHigh dated dsDay(n), with holes, unreadable
// dates and failing probes per test. It listens on 127.0.0.1:0 and never contacts any host; no
// wall-clock assertion (testWait is a hang guard only).

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/nntp-testkit/fakeserver"
)

const (
	dsGroup = "date.search"
	dsHigh  = 100
)

// dsDay is the Date of article n: n days after 2020-01-01.
func dsDay(n int64) time.Time {
	return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(n))
}

// dsFake is the provider of these tests.
type dsFake struct {
	high    int64               // highest article number; 0: dsHigh
	holes   map[int64]bool      // numbers with no article (cancelled, expired)
	badDate map[int64]bool      // articles whose Date does not parse
	failAt  map[int64]string    // XOVER whose range starts at this number gets this status line
	dateOf  map[int64]time.Time // a Date other than dsDay(n) (a late feed, a forged Date)
}

func (f *dsFake) last() int64 {
	if f.high > 0 {
		return f.high
	}
	return dsHigh
}

func (f *dsFake) exists(n int64) bool { return n >= 1 && n <= f.last() && !f.holes[n] }

func (f *dsFake) handle(c *fakeserver.Conn) {
	for {
		line, err := c.ReadLine()
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			_ = c.WriteLines("500 empty command")
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "QUIT":
			_ = c.WriteLines("205 bye")
			return
		case "MODE":
			_ = c.WriteLines("200 reader mode")
		case "CAPABILITIES":
			_ = c.WriteLines("101 capability list follows", "VERSION 2", "READER", "OVER", ".")
		case "GROUP":
			if len(fields) != 2 || fields[1] != dsGroup {
				_ = c.WriteLines("411 no such newsgroup")
				continue
			}
			_ = c.WriteLines(fmt.Sprintf("211 %d 1 %d %s", f.last()-int64(len(f.holes)), f.last(), dsGroup))
		case "XOVER", "OVER":
			from, to, ok := dlFakeRange(strings.Join(fields[1:], ""))
			if !ok {
				_ = c.WriteLines("501 bad range")
				continue
			}
			if to <= 0 {
				to = f.last() // open range "n-"
			}
			if st, fail := f.failAt[from]; fail {
				_ = c.WriteLines(st)
				continue
			}
			lines := []string{"224 overview follows"}
			for n := from; n <= to; n++ {
				if !f.exists(n) {
					continue
				}
				d := dsDay(n)
				if od, set := f.dateOf[n]; set {
					d = od
				}
				date := d.Format(time.RFC1123Z)
				if f.badDate[n] {
					date = "not a date"
				}
				lines = append(lines, fmt.Sprintf("%d\tsubject %d\tds@test.invalid\t%s\t<%d@ds.invalid>\t\t100\t5", n, n, date, n))
			}
			if len(lines) == 1 {
				_ = c.WriteLines("423 no articles in that range")
				continue
			}
			_ = c.WriteLines(append(lines, ".")...)
		default:
			_ = c.WriteLines("500 unknown command")
		}
	}
}

// dsFind runs FindStartArticleByDate against f for target (the search subtracts two days).
func dsFind(t *testing.T, f *dsFake, target time.Time) (int64, error) {
	t.Helper()
	srv := fakeserver.New(t, fakeserver.Options{Deadline: testWait, Handler: f.handle})
	pool := nntp.NewPool(&nntp.BackendConfig{
		Host:               srv.Host(),
		Port:               srv.Port(),
		MaxConns:           1,
		ConnectTimeout:     30 * time.Second,
		AuthFailureBackoff: -1,
		Provider:           &config.Provider{Name: "date-search-fake", Host: srv.Host(), Port: srv.Port()},
	})
	t.Cleanup(func() { _ = pool.ClosePool() })
	proc := &Processor{Pool: pool}
	gi := &nntp.GroupInfo{Name: dsGroup, Count: f.last() - int64(len(f.holes)), First: 1, Last: f.last()}
	return proc.FindStartArticleByDate(dsGroup, target, gi)
}

// dsTarget is a target date whose first article on or after it (after the search's two-day
// margin) is article want.
func dsTarget(want int64) time.Time { return dsDay(want).AddDate(0, 0, 2) }

// dsWant checks a start: never above want (the first article on the target date; above it
// articles would be skipped) and at most one probe window below it (a little more fetched).
func dsWant(t *testing.T, got int64, err error, want int64) {
	t.Helper()
	if err != nil {
		t.Fatalf("FindStartArticleByDate: %v", err)
	}
	if got > want {
		t.Fatalf("start article %d is above %d, the first article on the target date: articles %d-%d would be skipped", got, want, want, got-1)
	}
	if got < want-dateProbeWindow {
		t.Fatalf("start article %d, want %d (at most %d below it)", got, want, dateProbeWindow)
	}
}

// Baseline: no holes, the search lands on the first article of the target date.
func TestDateSearchNoHoles(t *testing.T) {
	got, err := dsFind(t, &dsFake{}, dsTarget(28))
	dsWant(t, got, err, 28)
}

// A hole at the first probe point (50) with the target below it.
func TestDateSearchHoleAtProbeTargetBelow(t *testing.T) {
	holes := map[int64]bool{}
	for n := int64(40); n <= 60; n++ {
		holes[n] = true
	}
	got, err := dsFind(t, &dsFake{holes: holes}, dsTarget(28))
	dsWant(t, got, err, 28)
}

// The same hole with the target above it.
func TestDateSearchHoleAtProbeTargetAbove(t *testing.T) {
	holes := map[int64]bool{}
	for n := int64(40); n <= 60; n++ {
		holes[n] = true
	}
	got, err := dsFind(t, &dsFake{holes: holes}, dsTarget(80))
	dsWant(t, got, err, 80)
}

// An unreadable Date at the first probe point, target below it.
func TestDateSearchBadDateAtProbe(t *testing.T) {
	got, err := dsFind(t, &dsFake{badDate: map[int64]bool{50: true}}, dsTarget(28))
	dsWant(t, got, err, 28)
}

// A whole probe window without articles: the search must look lower, not higher.
func TestDateSearchEmptyWindowMovesDown(t *testing.T) {
	holes := map[int64]bool{}
	for n := int64(45); n <= 99; n++ {
		holes[n] = true
	}
	got, err := dsFind(t, &dsFake{holes: holes}, dsTarget(28))
	dsWant(t, got, err, 28)
}

// A transient XOVER failure at a probe point ends the search with an error (no start is
// guessed, so the caller writes no progress).
func TestDateSearchProbeErrorReturnsError(t *testing.T) {
	got, err := dsFind(t, &dsFake{failAt: map[int64]string{50: "503 temporary failure"}}, dsTarget(28))
	if err == nil {
		t.Fatalf("FindStartArticleByDate after a 503 at a probe: start %d and no error, want an error", got)
	}
}

// A failure on a later probe (the second, from 25) is an error too.
func TestDateSearchLaterProbeErrorReturnsError(t *testing.T) {
	got, err := dsFind(t, &dsFake{failAt: map[int64]string{25: "400 service temporarily unavailable"}}, dsTarget(28))
	if err == nil {
		t.Fatalf("FindStartArticleByDate after a 400 on the second probe: start %d and no error, want an error", got)
	}
}

// The newest article carries an old Date (a late feed): the search must not return Last.
func TestDateSearchMisdatedLastArticle(t *testing.T) {
	f := &dsFake{dateOf: map[int64]time.Time{dsHigh: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)}}
	got, err := dsFind(t, f, dsTarget(28))
	dsWant(t, got, err, 28)
}

// One article with an old Date at a probe point does not move the search up.
func TestDateSearchMisdatedArticleAtProbe(t *testing.T) {
	f := &dsFake{dateOf: map[int64]time.Time{
		37: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
		50: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
	}}
	got, err := dsFind(t, f, dsTarget(28))
	dsWant(t, got, err, 28)
}

// The first article is dated exactly at the target: it is the start, not skipped.
func TestDateSearchFirstArticleAtTarget(t *testing.T) {
	got, err := dsFind(t, &dsFake{}, dsTarget(1))
	dsWant(t, got, err, 1)
}

// A stale low water mark: GROUP says 1, the articles start at 81. The search must reach the
// live range instead of starting in the empty numbers.
func TestDateSearchStaleLowWater(t *testing.T) {
	holes := map[int64]bool{}
	for n := int64(1); n <= 80; n++ {
		holes[n] = true
	}
	got, err := dsFind(t, &dsFake{holes: holes}, dsTarget(90))
	dsWant(t, got, err, 90)
}

// A wide hole right below the start: the start is the first article after the hole, not the
// first number of the hole.
func TestDateSearchWideHoleBeforeStart(t *testing.T) {
	holes := map[int64]bool{}
	for n := int64(30); n <= 98; n++ {
		holes[n] = true
	}
	got, err := dsFind(t, &dsFake{holes: holes}, dsTarget(99))
	dsWant(t, got, err, 99)
}

// A single article with an old Date in a sparse window (holes around it) does not move the
// lower bound: too few readable Dates to decide.
func TestDateSearchMisdatedArticleInSparseWindow(t *testing.T) {
	holes := map[int64]bool{}
	for n := int64(41); n <= 99; n++ {
		if n != 70 {
			holes[n] = true
		}
	}
	f := &dsFake{holes: holes, dateOf: map[int64]time.Time{70: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)}}
	got, err := dsFind(t, f, dsTarget(28))
	dsWant(t, got, err, 28)
}

// Two articles in a row with an old Date (a stuck clock, a late batch) at a probe point do not
// move the search up: the median of the window decides.
func TestDateSearchMisdatedRunAtProbe(t *testing.T) {
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &dsFake{dateOf: map[int64]time.Time{50: old, 51: old, 52: old}}
	got, err := dsFind(t, f, dsTarget(28))
	dsWant(t, got, err, 28)
}

// One article with a future Date below the start does not pull the start far down.
func TestDateSearchFutureDatedArticle(t *testing.T) {
	f := &dsFake{dateOf: map[int64]time.Time{65: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}}
	got, err := dsFind(t, f, dsTarget(80))
	dsWant(t, got, err, 80)
}

// A stale low water mark far below the articles (a hole wider than every probe window): the
// search must still reach the live range.
func TestDateSearchVeryStaleLowWater(t *testing.T) {
	const high = 20000
	holes := map[int64]bool{}
	for n := int64(1); n <= 15000; n++ {
		holes[n] = true
	}
	got, err := dsFind(t, &dsFake{high: high, holes: holes}, dsTarget(18000))
	dsWant(t, got, err, 18000)
}
