package processor

// Tests for the article-layer intake helpers (plan nntp-intake-injection, S3). No database:
// ProcessArticle itself is covered end to end in cmd/nntp-server (TestE2E*).

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
)

// iprReason returns the K4 reason of an intake error (the text after the first ": ").
func iprReason(err error) string {
	if err == nil {
		return ""
	}
	if _, after, ok := strings.Cut(err.Error(), ": "); ok {
		return after
	}
	return err.Error()
}

// iprHead returns a complete strict-valid header block plus extra lines.
func iprHead(msgid string, extra ...string) []string {
	h := []string{
		"Path: peer.example!not-for-mail",
		"From: Tester <tester@test.invalid>",
		"Newsgroups: t7.ipr.a,t7.ipr.b",
		"Subject: intake test",
		"Date: Sun, 11 Oct 2026 12:00:00 +0000",
		"Message-ID: " + msgid,
	}
	return append(h, extra...)
}

func TestIntakeProcParseNNTPDateStrict(t *testing.T) {
	fallback := time.Date(1990, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in     string
		ok     bool
		strict time.Time // checked when ok
		lax    time.Time // ParseNNTPDate result (K1 regression guard)
	}{
		{"Sun, 11 Oct 2026 12:00:00 +0000", true, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)},
		{"Sun, 11 Oct 2026 14:00:00 +0200 (CEST)", true, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)},
		{"Mon, 2 Jan 2006 15:04:05 EST", true, time.Date(2006, 1, 2, 20, 4, 5, 0, time.UTC), time.Date(2006, 1, 2, 20, 4, 5, 0, time.UTC)},
		// brute force with year and month found: ok
		{"sometime 7 March 1999 at 10:20", true, time.Date(1999, 3, 7, 10, 20, 0, 0, time.UTC), time.Date(1999, 3, 7, 10, 20, 0, 0, time.UTC)},
		// defaulted year/month: !ok, the lenient parser keeps its old fallback
		{"garbage", false, time.Time{}, fallback},
		{"Tuesday 7pm", false, time.Time{}, fallback},
		{"March", false, time.Time{}, time.Date(1990, 3, 1, 12, 0, 0, 0, time.UTC)},
		{"", false, time.Time{}, time.Time{}},
	} {
		got, ok := ParseNNTPDateStrict(tc.in)
		if ok != tc.ok {
			t.Errorf("ParseNNTPDateStrict(%q) ok=%v, want %v (t=%v)", tc.in, ok, tc.ok, got)
		}
		if ok && !got.Equal(tc.strict) {
			t.Errorf("ParseNNTPDateStrict(%q) = %v, want %v", tc.in, got, tc.strict)
		}
		if lax := ParseNNTPDate(tc.in); !lax.Equal(tc.lax) {
			t.Errorf("ParseNNTPDate(%q) = %v, want %v (unchanged, K1)", tc.in, lax, tc.lax)
		}
		if bf, bfok := bruteForceDateParseOK(tc.in); !bf.Equal(bruteForceDateParse(tc.in)) || (bfok && bf.IsZero()) {
			t.Errorf("bruteForceDateParseOK(%q) = %v, %v; bruteForceDateParse = %v", tc.in, bf, bfok, bruteForceDateParse(tc.in))
		}
	}
	// beyond the brute-force parser's internal 25 h bound: zero, not ok
	far := time.Now().UTC().AddDate(0, 0, 3)
	if got, ok := bruteForceDateParseOK("on " + far.Format("2 January 2006")); ok || !got.IsZero() {
		t.Errorf("bruteForceDateParseOK(3 days ahead) = %v, %v; want zero, false", got, ok)
	}
}

func TestIntakeProcFutureMargin(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	f := func(d time.Duration) string { return now.Add(d).Format(time.RFC1123Z) }
	for _, tc := range []struct {
		name, date, injection, reason string
	}{
		{"now", f(0), "", ""},
		{"23h", f(23 * time.Hour), "", ""},
		{"24h-boundary", f(MaxFutureDate), "", ""},
		{"24h+1s", f(MaxFutureDate + time.Second), "", "date in the future"},
		{"24h30m", f(24*time.Hour + 30*time.Minute), "", "date in the future"},
		{"old-date", f(-30 * 24 * time.Hour), "", ""},
		{"garbage", "garbage", "", "invalid date"},
		{"empty", "", "", "invalid date"},
		// the Injection-Date is measured when present and parseable
		{"injection-future", f(0), f(25 * time.Hour), "date in the future"},
		{"injection-ok-date-future", f(48 * time.Hour), f(0), ""},
		{"injection-unparseable", f(0), "garbage", ""},
	} {
		got, err := checkIntakeDates(tc.date, tc.injection, now)
		if tc.reason == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			} else if want := ParseNNTPDate(tc.date); !got.Equal(want) {
				t.Errorf("%s: date %v, want %v", tc.name, got, want)
			}
			continue
		}
		if !errors.Is(err, common.ErrIntakeRejected) || iprReason(err) != tc.reason {
			t.Errorf("%s: err %v, want rejected %q", tc.name, err, tc.reason)
		}
	}
	if MaxFutureDate != 24*time.Hour {
		t.Errorf("MaxFutureDate = %v, want 24h (RFC 5537 §3.7)", MaxFutureDate)
	}
}

func TestIntakeProcGroupDecision(t *testing.T) {
	for _, tc := range []struct {
		active    bool
		status    string
		posted    bool
		keep, mod bool
	}{
		{true, "y", false, true, false},
		{true, "y", true, true, false},
		{true, "m", false, true, true},
		{true, "m", true, true, true},
		{true, "n", false, true, false}, // feeds are accepted
		{true, "n", true, false, false}, // POST: posting not permitted
		{true, "x", false, false, false},
		{true, "x", true, false, false},
		{true, "j", false, false, false},
		{true, "=other.group", false, false, false},
		{true, "", false, false, false},
		{false, "y", false, false, false},
		{false, "m", true, false, false},
		{false, "n", false, false, false},
	} {
		keep, mod := intakeGroupDecision(database.IntakeGroup{Name: "t7.ipr.g", Active: tc.active, Status: tc.status}, tc.posted)
		if keep != tc.keep || mod != tc.mod {
			t.Errorf("active=%v status=%q posted=%v: keep=%v moderated=%v, want %v %v",
				tc.active, tc.status, tc.posted, keep, mod, tc.keep, tc.mod)
		}
	}
}

func TestIntakeProcSyncPathAndXref(t *testing.T) {
	in := []string{
		"Path: a!b",
		" !c",
		"Xref: other.example g:1",
		"Subject: s",
		"XREF: second g:2",
		"\tfolded",
		"From: f",
	}
	snapshot := append([]string(nil), in...)
	stripped := stripXrefLines(in)
	want := []string{"Path: a!b", " !c", "Subject: s", "From: f"}
	if strings.Join(stripped, "|") != strings.Join(want, "|") {
		t.Fatalf("stripXrefLines = %q, want %q", stripped, want)
	}
	synced := syncStoredPath(stripped, "news.local!a!b!c")
	want = []string{"Path: news.local!a!b!c", "Subject: s", "From: f"}
	if strings.Join(synced, "|") != strings.Join(want, "|") {
		t.Fatalf("syncStoredPath = %q, want %q", synced, want)
	}
	if common.HeaderValue(synced, "Path") != "news.local!a!b!c" {
		t.Fatalf("Path value %q", common.HeaderValue(synced, "Path"))
	}
	// a block without a Path line (web posts) stays as it is
	web := []string{"From: w", "Newsgroups: a.b", "X-pugleaf-Trace: host;", " nonce=\"1\";"}
	if got := syncStoredPath(web, "x!y"); strings.Join(got, "|") != strings.Join(web, "|") {
		t.Fatalf("syncStoredPath without Path = %q, want unchanged", got)
	}
	// a "Path" substring in another header or a continuation line is not a Path line
	other := []string{"X-Path: q", " Path: r", "Subject: s"}
	if got := syncStoredPath(other, "x!y"); strings.Join(got, "|") != strings.Join(other, "|") {
		t.Fatalf("syncStoredPath with X-Path = %q, want unchanged", got)
	}
	if strings.Join(in, "|") != strings.Join(snapshot, "|") {
		t.Fatalf("input modified: %q", in)
	}
}

func TestIntakeProcValidateHeaders(t *testing.T) {
	const id = "<t7-ipr-1@test.invalid>"
	for _, tc := range []struct {
		name     string
		lines    []string
		msgid    string
		reason   string
		approved bool
	}{
		{"valid", iprHead(id), id, "", false},
		{"approved", iprHead(id, "Approved: moderator@test.invalid"), id, "", true},
		{"supersedes", iprHead(id, "Supersedes: <old@test.invalid>"), id, "", false},
		{"two-subjects", iprHead(id, "Subject: again"), id, "invalid header block", false},
		{"colon-less", iprHead(id, "no colon here"), id, "invalid header block", false},
		{"dot-line", iprHead(id, "."), id, "invalid header block", false},
		{"nul", iprHead(id, "X-Bad: a\x00b"), id, "invalid header block", false},
		{"no-path", iprHead(id)[1:], id, "missing mandatory header", false},
		{"empty-subject", append(iprHead(id)[:3:3], "Subject:", "Date: Sun, 11 Oct 2026 12:00:00 +0000", "Message-ID: "+id), id, "missing mandatory header", false},
		{"no-message-id", iprHead(id)[:5], id, "missing mandatory header", false},
		{"nil-block", nil, id, "missing mandatory header", false},
		{"id-mismatch", iprHead(id), "<t7-ipr-2@test.invalid>", "invalid message-id", false},
		{"id-syntax", iprHead("no-brackets@x"), "no-brackets@x", "invalid message-id", false},
		{"id-too-long", iprHead("<" + strings.Repeat("a", 240) + "@test.invalid>"), "<" + strings.Repeat("a", 240) + "@test.invalid>", "invalid message-id", false},
		{"control", iprHead(id, "Control: cancel <x@y>"), id, "control messages not accepted", false},
	} {
		normalized, approved, err := validateIntakeHeaders(tc.lines, tc.msgid)
		if tc.reason == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
				continue
			}
			if approved != tc.approved {
				t.Errorf("%s: approved=%v, want %v", tc.name, approved, tc.approved)
			}
			if strings.Join(normalized, "|") != strings.Join(tc.lines, "|") {
				t.Errorf("%s: normalized %q differs from the input", tc.name, normalized)
			}
			continue
		}
		if !errors.Is(err, common.ErrIntakeRejected) || iprReason(err) != tc.reason {
			t.Errorf("%s: err %v, want rejected %q", tc.name, err, tc.reason)
		}
	}
}

func TestIntakeProcCaptureLimiter(t *testing.T) {
	never := func() bool { return false }
	l := newCaptureLimiter(2)
	if !l.acquire(true, never) || !l.acquire(true, never) {
		t.Fatal("the first two acquires must succeed")
	}
	if n := len(l.slots); n != 2 {
		t.Fatalf("%d slots taken, want 2", n)
	}

	// a third acquire(true) blocks until release
	got := make(chan bool, 1)
	go func() { got <- l.acquire(true, never) }()
	select {
	case ok := <-got:
		t.Fatalf("third acquire returned %v on a full limiter", ok)
	case <-time.After(50 * time.Millisecond): // deliberate negative window (cannot fail on a slow machine)
	}
	l.release()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("third acquire returned false after release")
		}
	case <-time.After(testWait):
		t.Fatal("third acquire still blocked after release")
	}

	// full again: once stop reports true, acquire(true) gives up on its next ticker poll.
	// Structural: stopFn counts its calls; no duration is asserted.
	var stop atomic.Bool
	var pollsBefore, pollsAfter atomic.Int64 // stopFn calls that saw stop false / true
	stopFn := func() bool {
		if stop.Load() {
			pollsAfter.Add(1)
			return true
		}
		pollsBefore.Add(1)
		return false
	}
	go func() { got <- l.acquire(true, stopFn) }()
	// wait for the first poll, so the poll that sees stop can only come from a ticker tick
	deadline := time.Now().Add(testWait)
	for pollsBefore.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("acquire(true) on a full limiter never polled stop")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case ok := <-got:
		t.Fatalf("acquire returned %v before stop", ok)
	case <-time.After(30 * time.Millisecond): // deliberate negative window (cannot fail on a slow machine)
	}
	stop.Store(true)
	start := time.Now()
	select {
	case ok := <-got:
		if ok {
			t.Fatal("acquire returned true on a full limiter after stop")
		}
	case <-time.After(testWait):
		t.Fatal("acquire still blocked after stop")
	}
	// 1 s is 10x the 100 ms tick (rule 1): catches a ticker regression the poll count cannot
	if d := time.Since(start); d > time.Second {
		t.Fatalf("acquire took %v after stop, want <= 1s (10x the 100 ms tick)", d)
	}
	if n := pollsAfter.Load(); n != 1 {
		t.Fatalf("acquire polled stop %d times after it reported true, want 1 (give up on the first tick)", n)
	}

	// wait=false never blocks (it never enters the polling loop) and counts the overflow;
	// run in a goroutine so a regression that blocks fails at testWait instead of hanging
	var falsePolls atomic.Int64
	go func() { got <- l.acquire(false, func() bool { falsePolls.Add(1); return false }) }()
	select {
	case ok := <-got:
		if ok {
			t.Fatal("acquire(false) took a slot on a full limiter")
		}
	case <-time.After(testWait):
		t.Fatal("acquire(false) blocked on a full limiter")
	}
	if n := falsePolls.Load(); n != 0 {
		t.Fatalf("acquire(false) polled stop %d times, want 0 (it must not wait)", n)
	}
	if n := l.overflow.Load(); n != 1 {
		t.Fatalf("overflow = %d, want 1", n)
	}
	if n := len(l.slots); n != 2 {
		t.Fatalf("%d slots taken, want 2", n)
	}

	// release frees slots; a release without a slot is a no-op
	l.release()
	l.release()
	l.release()
	if n := len(l.slots); n != 0 {
		t.Fatalf("%d slots taken after releases, want 0", n)
	}
	if !l.acquire(false, nil) {
		t.Fatal("acquire(false) on an empty limiter must take a slot")
	}

	for _, n := range []int{0, -5, 1} {
		if c := cap(newCaptureLimiter(n).slots); c != 1 {
			t.Fatalf("newCaptureLimiter(%d) has %d slots, want 1", n, c)
		}
	}
}
