package peerstats

// Tests of the INN-style article log (plan inn-reject-log, artlog.go): the line format against
// innd's ARTlog (innd/art.c:304), the message-id and text rules, and the file handling (queue,
// rotation, Stop). No test looks at the wall clock: the times are pinned.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// innArtLine is innd's fprintf of innd/art.c:304 rebuilt from its parts: "%.15s" of
// ctime(&t) + 4 (time.ANSIC is ctime's layout without the newline), ".%03d" of the
// milliseconds, the code character, Feedsite, Message-ID and text.
func innArtLine(t time.Time, code byte, feedsite, id, text string) string {
	return fmt.Sprintf("%.15s.%03d %c %s %s %s", t.Format(time.ANSIC)[4:], t.Nanosecond()/1e6, code, feedsite, id, text)
}

// readArtLog returns the lines of the article log file.
func readArtLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(data)
	if s == "" {
		return nil
	}
	if !strings.HasSuffix(s, "\n") {
		t.Fatalf("article log does not end with a newline: %q", s)
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// TestArtRejectLine against innd/art.c:304, byte for byte.
func TestArtRejectLine(t *testing.T) {
	// A single-digit day: ctime pads it with a space ("Nov  5").
	at := time.Date(2026, 11, 5, 15, 17, 19, 123987654, time.UTC)
	got := artRejectLine(at, "web-lax1", "<a1@example.invalid>", 437, "Rejected (path loop)")
	want := "Nov  5 15:17:19.123 - web-lax1 <a1@example.invalid> 437 Rejected (path loop)"
	if got != want {
		t.Errorf("line =\n%q, want\n%q", got, want)
	}
	if inn := innArtLine(at, '-', "web-lax1", "<a1@example.invalid>", "437 Rejected (path loop)"); got != inn {
		t.Errorf("line =\n%q, innd's format gives\n%q", got, inn)
	}
	// A two-digit day and 0 ms.
	at = time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	if got, want := artRejectLine(at, "p", "<b@c>", 439, "Not wanted"), "Nov 20 01:02:03.000 - p <b@c> 439 Not wanted"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

// TestArtRejectLineFields: the message-id, peer and text rules of artlog.go.
func TestArtRejectLineFields(t *testing.T) {
	at := time.Date(2026, 11, 20, 15, 17, 19, 5e6, time.UTC)
	const ts = "Nov 20 15:17:19.005 - "
	long := strings.Repeat("x", artTextMax+40)
	// A multi-byte rune across the cap is not cut in half.
	runeCut := strings.Repeat("a", artTextMax-1) + "é" + "tail"
	for _, tc := range []struct {
		name       string
		peer, id   string
		code       int
		text, want string
	}{
		{"takethis id dropped", "p1", "<a@b>", 439, "<a@b> Rejected (control messages not accepted)",
			"p1 <a@b> 439 Rejected (control messages not accepted)"},
		{"another id stays", "p1", "<a@b>", 439, "<x@y> Rejected", "p1 <a@b> 439 <x@y> Rejected"},
		{"no id", "p1", "", 439, "Rejected (invalid message-id)", "p1 (null) 439 Rejected (invalid message-id)"},
		{"invalid id", "p1", "not-an-id", 439, "Rejected (invalid message-id)",
			"p1 (null) 439 Rejected (invalid message-id)"},
		// An invalid id is never put in front of a reply, so it is not stripped either.
		{"invalid id in front stays", "p1", "not-an-id", 439, "not-an-id Rejected",
			"p1 (null) 439 not-an-id Rejected"},
		// An invalid id equal to the first word of the text is no id in front of it: only a
		// valid id is ever put in front of a reply (TAKETHIS Rejected, round-0 F2).
		{"invalid id equal to the first word", "p1", "Rejected", 439, "Rejected (invalid message-id)",
			"p1 (null) 439 Rejected (invalid message-id)"},
		{"id with a control char", "p1", "<a\x01@b>", 437, "Rejected (invalid article)",
			"p1 (null) 437 Rejected (invalid article)"},
		{"control chars", "p1", "<a@b>", 437, "Rejected\r\n(bad)\x00\tx\x7f", "p1 <a@b> 437 Rejected  (bad)  x"},
		{"empty text", "p1", "<a@b>", 437, "", "p1 <a@b> 437"},
		{"capped", "p1", "<a@b>", 437, long, "p1 <a@b> 437 " + long[:artTextMax]},
		{"capped at a rune", "p1", "<a@b>", 437, runeCut, "p1 <a@b> 437 " + strings.Repeat("a", artTextMax-1)},
		{"capped after a space", "p1", "<a@b>", 437, strings.Repeat("a", artTextMax-2) + "  tail",
			"p1 <a@b> 437 " + strings.Repeat("a", artTextMax-2)},
		{"peer with spaces", " web lax1 ", "<a@b>", 437, "Rejected", "web_lax1 <a@b> 437 Rejected"},
		{"no peer name", "", "<a@b>", 437, "Rejected", "unknown <a@b> 437 Rejected"},
	} {
		if got, want := artRejectLine(at, tc.peer, tc.id, tc.code, tc.text), ts+tc.want; got != want {
			t.Errorf("%s: line =\n%q, want\n%q", tc.name, got, want)
		}
	}
}

// TestArtLogOnlyRejects: Article writes 437 and 439 only, asynchronously, and the INN log
// next to it gets none of these lines.
func TestArtLogOnlyRejects(t *testing.T) {
	dir := t.TempDir()
	artPath := filepath.Join(dir, "news")
	innPath := filepath.Join(dir, "news.notice")
	clk := newClock(time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC))
	svc, err := New(Config{INNLogPath: innPath, ArtLogPath: artPath, Host: "h", Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 3, Name: "web-lax1"}
	for _, code := range []int{235, 239, 435, 436, 438, 431, 400, 441} {
		svc.Article(peer, "<n@b>", code, "not a reject")
	}
	svc.Article(peer, "<r1@b>", 437, "Rejected (path loop)")
	svc.Article(peer, "<r2@b>", 439, "<r2@b> Rejected (duplicate)")
	if lines := readArtLog(t, artPath); len(lines) != 0 {
		t.Errorf("Article wrote %d lines to the file itself: %q", len(lines), lines)
	}
	svc.mu.Lock()
	queued := len(svc.artq)
	svc.mu.Unlock()
	if queued != 2 {
		t.Errorf("queued %d lines, want 2", queued)
	}
	svc.Stop()
	want := []string{
		"Nov 20 15:00:00.000 - web-lax1 <r1@b> 437 Rejected (path loop)",
		"Nov 20 15:00:00.000 - web-lax1 <r2@b> 439 Rejected (duplicate)",
	}
	if got := readArtLog(t, artPath); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("article log =\n%q, want\n%q", got, want)
	}
	if msgs := readLog(t, innPath); len(msgs) != 0 {
		t.Errorf("the inn log got %d lines from Article: %q", len(msgs), msgs)
	}
}

// TestArtLogOff: without ArtLogPath, Article queues nothing (and Nop takes it as well).
func TestArtLogOff(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "news.notice")
	svc, err := New(Config{INNLogPath: logPath, Host: "h"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.Article(PeerRef{ID: 1, Name: "p"}, "<a@b>", 437, "Rejected")
	svc.mu.Lock()
	queued := len(svc.artq)
	svc.mu.Unlock()
	if queued != 0 {
		t.Errorf("queued %d article lines without an article log", queued)
	}
	svc.Stop()
	if msgs := readLog(t, logPath); len(msgs) != 0 {
		t.Errorf("the inn log got %d lines: %q", len(msgs), msgs)
	}
	Nop.Article(PeerRef{}, "", 437, "")
}

// TestArtLogQueueBound: a full queue drops lines and counts them instead of growing.
func TestArtLogQueueBound(t *testing.T) {
	artPath := filepath.Join(t.TempDir(), "news")
	clk := newClock(time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 1, Name: "p1"}
	for i := 0; i < maxArtQueue+10; i++ {
		svc.Article(peer, fmt.Sprintf("<%d@b>", i), 439, "Not wanted")
	}
	svc.mu.Lock()
	queued, dropped := len(svc.artq), svc.artDropped
	svc.mu.Unlock()
	if queued != maxArtQueue || dropped != 10 {
		t.Errorf("queued %d, dropped %d; want %d and 10", queued, dropped, maxArtQueue)
	}
	svc.drainArtLog()
	svc.mu.Lock()
	queued, dropped = len(svc.artq), svc.artDropped
	svc.mu.Unlock()
	if queued != 0 || dropped != 0 {
		t.Errorf("after the drain: queued %d, dropped %d; want 0 and 0", queued, dropped)
	}
	if n := len(readArtLog(t, artPath)); n != maxArtQueue {
		t.Errorf("file has %d lines, want %d", n, maxArtQueue)
	}
	svc.Stop()
}

// TestArtLogReopenAfterRename: a file logrotate moved away is reopened at its path before the
// next lines are written, by the drain and by the checkpoint tick.
func TestArtLogReopenAfterRename(t *testing.T) {
	dir := t.TempDir()
	artPath := filepath.Join(dir, "news")
	clk := newClock(time.Date(2026, 11, 20, 17, 0, 0, 0, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 1, Name: "p1"}
	svc.Article(peer, "<1@b>", 437, "one")
	svc.drainArtLog()
	if err := os.Rename(artPath, artPath+".1"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	svc.Article(peer, "<2@b>", 437, "two")
	svc.drainArtLog()
	if err := os.Rename(artPath, artPath+".2"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	svc.checkpointTick() // reopens with nothing queued
	if _, err := os.Stat(artPath); err != nil {
		t.Errorf("the checkpoint tick did not reopen the file: %v", err)
	}
	svc.Article(peer, "<3@b>", 437, "three")
	svc.Stop()
	for _, f := range []struct{ path, id string }{{artPath + ".1", "<1@b>"}, {artPath + ".2", "<2@b>"}, {artPath, "<3@b>"}} {
		lines := readArtLog(t, f.path)
		if len(lines) != 1 || !strings.Contains(lines[0], " "+f.id+" ") {
			t.Errorf("%s = %q, want the one line of %s", f.path, lines, f.id)
		}
	}
}

// TestArtLogAfterStop: Stop writes what was queued (without Start); a late Article neither
// writes nor reopens the closed file.
func TestArtLogAfterStop(t *testing.T) {
	artPath := filepath.Join(t.TempDir(), "news")
	clk := newClock(time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 1, Name: "p1"}
	svc.Article(peer, "<1@b>", 437, "before")
	svc.Stop()
	if n := len(readArtLog(t, artPath)); n != 1 {
		t.Fatalf("Stop wrote %d lines, want 1", n)
	}
	svc.Article(peer, "<2@b>", 437, "after")
	svc.Article(peer, "<3@b>", 439, "after")
	svc.drainArtLog()
	svc.artlog.reopenIfRotated()
	svc.Stop()
	if n := len(readArtLog(t, artPath)); n != 1 {
		t.Errorf("Article after Stop wrote %d extra lines", n-1)
	}
	svc.mu.Lock()
	after, queued := svc.artAfterStop, len(svc.artq)
	svc.mu.Unlock()
	if after != 2 || queued != 0 {
		t.Errorf("artAfterStop = %d, queued = %d; want 2 and 0", after, queued)
	}
	svc.artlog.mu.Lock()
	closed, f := svc.artlog.closed, svc.artlog.f
	svc.artlog.mu.Unlock()
	if !closed || f != nil {
		t.Errorf("the article log was reopened after Close: closed=%v f=%v", closed, f)
	}
}

// TestArtLogConcurrent: Article from many goroutines with drains interleaved: every producer
// hands a drain request to the drainer goroutine after each block of lines and waits until the
// drain is done, while the other producers may go on queueing (whether a drain and an Article
// actually run at the same instant is up to the scheduler; TestArtLogArticleDuringDrain forces
// that case). Every id is in the file exactly once, none lost or doubled (go test -race).
func TestArtLogConcurrent(t *testing.T) {
	artPath := filepath.Join(t.TempDir(), "news")
	clk := newClock(time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const workers, per, block = 8, 200, 25
	drainReq := make(chan chan struct{})
	drainerDone := make(chan struct{})
	drains := 0
	go func() {
		defer close(drainerDone)
		for done := range drainReq {
			svc.drainArtLog()
			drains++
			close(done)
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			peer := PeerRef{ID: int64(w), Name: fmt.Sprintf("p%d", w)}
			for i := 0; i < per; i++ {
				svc.Article(peer, fmt.Sprintf("<%d.%d@b>", w, i), 439, "Not wanted")
				if (i+1)%block == 0 {
					done := make(chan struct{})
					drainReq <- done
					<-done
				}
			}
		}(w)
	}
	wg.Wait()
	close(drainReq)
	<-drainerDone
	svc.Stop() // writes the tail, if any
	if want := workers * per / block; drains != want {
		t.Errorf("%d drains ran, want %d", drains, want)
	}
	seen := make(map[string]int)
	for _, l := range readArtLog(t, artPath) {
		if !strings.HasPrefix(l, "Nov 20 19:00:00.000 - p") || !strings.HasSuffix(l, " 439 Not wanted") {
			t.Fatalf("broken line %q", l)
		}
		seen[strings.Fields(l)[5]]++ // Nov, 20, time, -, peer, id, ...
	}
	for w := 0; w < workers; w++ {
		for i := 0; i < per; i++ {
			id := fmt.Sprintf("<%d.%d@b>", w, i)
			if n := seen[id]; n != 1 {
				t.Errorf("%s is %d times in the file, want once", id, n)
			}
			delete(seen, id)
		}
	}
	if len(seen) != 0 {
		t.Errorf("unexpected ids in the file: %v", seen)
	}
}

// TestArtLogArticleDuringDrain: Article never waits for a drain that is writing. The test holds
// the log file's mutex, so a drain that took the queue blocks before it writes; while it is
// blocked there the producers queue more lines, which must neither block nor get lost: after
// the file is released, the first batch and the later lines are each in the file exactly once.
func TestArtLogArticleDuringDrain(t *testing.T) {
	artPath := filepath.Join(t.TempDir(), "news")
	clk := newClock(time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	peer := PeerRef{ID: 1, Name: "p1"}
	const first, later, workers = 50, 40, 4
	for i := 0; i < first; i++ {
		svc.Article(peer, fmt.Sprintf("<a%d@b>", i), 439, "Not wanted")
	}
	svc.artlog.mu.Lock() // the drain below takes the queue, then waits here before it writes
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		svc.drainArtLog()
	}()
	// Wait (no clock: a hang guard is the test timeout) until the drain has taken the queue.
	for {
		svc.mu.Lock()
		n := len(svc.artq)
		svc.mu.Unlock()
		if n == 0 {
			break
		}
		runtime.Gosched()
	}
	// The drain holds its batch and waits for the file: Article must go on regardless.
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < later/workers; i++ {
				svc.Article(peer, fmt.Sprintf("<l%d.%d@b>", w, i), 439, "Not wanted")
			}
		}(w)
	}
	wg.Wait() // would hang if Article waited for the blocked drain
	svc.mu.Lock()
	queued := len(svc.artq)
	svc.mu.Unlock()
	if queued != later {
		t.Errorf("queued %d lines while the drain was blocked, want %d", queued, later)
	}
	select {
	case <-drained:
		t.Error("the drain finished while the file was held")
	default:
	}
	svc.artlog.mu.Unlock()
	<-drained
	svc.Stop() // writes the later lines
	seen := make(map[string]int)
	for _, id := range strings.Fields(artIDs(t, artPath)) {
		seen[id]++
	}
	want := make([]string, 0, first+later)
	for i := 0; i < first; i++ {
		want = append(want, fmt.Sprintf("<a%d@b>", i))
	}
	for w := 0; w < workers; w++ {
		for i := 0; i < later/workers; i++ {
			want = append(want, fmt.Sprintf("<l%d.%d@b>", w, i))
		}
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Errorf("%s is %d times in the file, want once", id, seen[id])
		}
		delete(seen, id)
	}
	if len(seen) != 0 {
		t.Errorf("unexpected ids in the file: %v", seen)
	}
	// The first batch was written before the later lines (one drain, then Stop).
	if ids := strings.Fields(artIDs(t, artPath)); len(ids) != first+later || ids[first-1] != fmt.Sprintf("<a%d@b>", first-1) {
		t.Errorf("the first batch is not in front of the later lines: %v", ids)
	}
}

// artWriteThenDrain queues one line for id and drains it.
func artWriteThenDrain(svc *Service, id string) {
	svc.Article(PeerRef{ID: 1, Name: "p1"}, id, 437, "Rejected")
	svc.drainArtLog()
}

// artIDs returns the message-ids of the lines of the article log at path, space separated.
func artIDs(t *testing.T, path string) string {
	t.Helper()
	var ids []string
	for _, l := range readArtLog(t, path) {
		ids = append(ids, strings.Fields(l)[5])
	}
	return strings.Join(ids, " ")
}

// TestArtLogCopytruncate: logrotate's copytruncate empties the file in place; the file is
// O_APPEND, so the next lines start at the beginning of the empty file and nothing is lost.
func TestArtLogCopytruncate(t *testing.T) {
	artPath := filepath.Join(t.TempDir(), "news")
	// a pinned clock, in another month and with a padded day, so the test never depends on
	// the real date
	clk := newClock(time.Date(2027, 3, 5, 4, 5, 6, 0, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	artWriteThenDrain(svc, "<1@b>")
	artWriteThenDrain(svc, "<2@b>")
	if err := os.Truncate(artPath, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	artWriteThenDrain(svc, "<3@b>")
	svc.checkpointTick() // the same file: no reopen
	artWriteThenDrain(svc, "<4@b>")
	svc.Stop()
	data, err := os.ReadFile(artPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(string(data), "Mar  5 04:05:06.000 - ") || strings.ContainsRune(string(data), 0) {
		t.Errorf("the file after the truncate does not start with a line (a hole?): %q", data)
	}
	if got, want := artIDs(t, artPath), "<3@b> <4@b>"; got != want {
		t.Errorf("ids after the truncate = %q, want %q", got, want)
	}
}

// TestArtLogUnlinked: a file removed under the open log is recreated at its path by the next
// drain that has lines.
func TestArtLogUnlinked(t *testing.T) {
	artPath := filepath.Join(t.TempDir(), "news")
	clk := newClock(time.Date(2027, 1, 9, 23, 59, 59, 999e6, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	artWriteThenDrain(svc, "<1@b>")
	if err := os.Remove(artPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	artWriteThenDrain(svc, "<2@b>")
	svc.Stop()
	if got, want := artIDs(t, artPath), "<2@b>"; got != want {
		t.Errorf("ids in the recreated file = %q, want %q", got, want)
	}
}

// syncBuffer is a bytes.Buffer safe for the logger's concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// captureLog sends the standard logger to a buffer for the rest of the test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf
}

// TestArtLogDirectoryGone: while the directory of the file is missing, a drain cannot reopen
// it: that batch is dropped with a [PEERSTATS] error naming the article log, and writing goes
// on once the directory is back. No message calls the article log the inn log.
func TestArtLogDirectoryGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "log")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	artPath := filepath.Join(dir, "news")
	clk := newClock(time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logged := captureLog(t)
	artWriteThenDrain(svc, "<1@b>")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove the directory: %v", err)
	}
	artWriteThenDrain(svc, "<2@b>") // dropped: the file cannot be opened
	if out := logged.String(); !strings.Contains(out, "[PEERSTATS] peerstats article log "+artPath) {
		t.Errorf("no [PEERSTATS] error naming the article log for the dropped batch; log:\n%s", out)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir again: %v", err)
	}
	artWriteThenDrain(svc, "<3@b>")
	svc.Stop()
	if got, want := artIDs(t, artPath), "<3@b>"; got != want {
		t.Errorf("ids after the directory came back = %q, want %q", got, want)
	}
	if out := logged.String(); !strings.Contains(out, "[PEERSTATS] reopened article log "+artPath) || strings.Contains(out, "inn log") {
		t.Errorf("the messages do not name the article log; log:\n%s", out)
	}
}

// openFDsTo counts the descriptors of this process open on the file at path, -1 when the
// platform has no /proc/self/fd.
func openFDsTo(t *testing.T, path string) int {
	t.Helper()
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks %s: %v", path, err)
	}
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range ents {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && target == want {
			n++
		}
	}
	return n
}

// TestArtLogSameFileAsINNLog: New refuses an article log that is the INN log under another
// name (a symlink here; a hard link or another spelling of the path is the same file too), so
// innreport never sees article lines, and it closes what it had opened.
func TestArtLogSameFileAsINNLog(t *testing.T) {
	dir := t.TempDir()
	innPath := filepath.Join(dir, "news.notice")
	link := filepath.Join(dir, "news")
	if err := os.WriteFile(innPath, nil, 0o644); err != nil {
		t.Fatalf("create %s: %v", innPath, err)
	}
	if err := os.Symlink(innPath, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	for _, art := range []string{link, innPath, filepath.Join(dir, ".", "news.notice")} {
		if svc, err := New(Config{INNLogPath: innPath, ArtLogPath: art}); err == nil {
			svc.Stop()
			t.Errorf("New with -inn-artlog %s, the inn log %s: no error", art, innPath)
		}
	}
	// The refused New closed both files again.
	switch n := openFDsTo(t, innPath); {
	case n < 0:
		t.Log("no /proc/self/fd: descriptor check skipped")
	case n > 0:
		t.Errorf("%d descriptors still open on %s after the refused New", n, innPath)
	}
	// Two different files are fine.
	svc, err := New(Config{INNLogPath: innPath, ArtLogPath: filepath.Join(dir, "other")})
	if err != nil {
		t.Fatalf("New with two files: %v", err)
	}
	svc.Stop()
}

// TestArtLogMissingDir: New fails when the article log cannot be opened.
func TestArtLogMissingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-such-dir", "news")
	if _, err := New(Config{ArtLogPath: path}); err == nil {
		t.Error("New with an unopenable article log path succeeded")
	}
	innPath := filepath.Join(dir, "news.notice")
	if _, err := New(Config{INNLogPath: innPath, ArtLogPath: path}); err == nil {
		t.Error("New with an unopenable article log path and an inn log succeeded")
	}
	// The inn log New had opened first is closed again: no descriptor leaks.
	switch n := openFDsTo(t, innPath); {
	case n < 0:
		t.Log("no /proc/self/fd: descriptor check skipped")
	case n > 0:
		t.Errorf("%d descriptors still open on %s after the failed New", n, innPath)
	}
}
