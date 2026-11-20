package peerstats

// Tests of the INN-style article log (plan inn-reject-log, artlog.go): the line format against
// innd's ARTlog (innd/art.c:304), the message-id and text rules, and the file handling (queue,
// rotation, Stop). No test looks at the wall clock: the times are pinned.

import (
	"fmt"
	"os"
	"path/filepath"
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
		{"invalid id", "p1", "not-an-id", 439, "not-an-id Rejected (invalid message-id)",
			"p1 (null) 439 Rejected (invalid message-id)"},
		{"id with a control char", "p1", "<a\x01@b>", 437, "Rejected (invalid article)",
			"p1 (null) 437 Rejected (invalid article)"},
		{"control chars", "p1", "<a@b>", 437, "Rejected\r\n(bad)\x00\tx\x7f", "p1 <a@b> 437 Rejected  (bad)  x"},
		{"empty text", "p1", "<a@b>", 437, "", "p1 <a@b> 437"},
		{"capped", "p1", "<a@b>", 437, long, "p1 <a@b> 437 " + long[:artTextMax]},
		{"capped at a rune", "p1", "<a@b>", 437, runeCut, "p1 <a@b> 437 " + strings.Repeat("a", artTextMax-1)},
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

// TestArtLogConcurrent: Article from many goroutines while the background goroutine drains;
// every line arrives whole (go test -race).
func TestArtLogConcurrent(t *testing.T) {
	artPath := filepath.Join(t.TempDir(), "news")
	clk := newClock(time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC))
	svc, err := New(Config{ArtLogPath: artPath, Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.Start()
	const workers, per = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			peer := PeerRef{ID: int64(w), Name: fmt.Sprintf("p%d", w)}
			for i := 0; i < per; i++ {
				svc.Article(peer, fmt.Sprintf("<%d.%d@b>", w, i), 439, "Not wanted")
			}
		}(w)
	}
	wg.Wait()
	svc.Stop()
	lines := readArtLog(t, artPath)
	if len(lines) != workers*per {
		t.Fatalf("file has %d lines, want %d", len(lines), workers*per)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "Nov 20 19:00:00.000 - p") || !strings.HasSuffix(l, " 439 Not wanted") {
			t.Fatalf("broken line %q", l)
		}
	}
}

// TestArtLogMissingDir: New fails when the article log cannot be opened.
func TestArtLogMissingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-such-dir", "news")
	if _, err := New(Config{ArtLogPath: path}); err == nil {
		t.Error("New with an unopenable article log path succeeded")
	}
	if _, err := New(Config{INNLogPath: filepath.Join(dir, "news.notice"), ArtLogPath: path}); err == nil {
		t.Error("New with an unopenable article log path and an inn log succeeded")
	}
}
