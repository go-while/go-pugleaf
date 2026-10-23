package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-while/go-pugleaf/internal/history"
)

func TestToolsTestRebuildProgressFile(t *testing.T) {
	dir := t.TempDir()

	last, err := readRebuildProgress(dir)
	if err != nil || last != "" {
		t.Fatalf("missing progress file: got %q, %v; want \"\", nil", last, err)
	}
	if err := removeRebuildProgress(dir); err != nil {
		t.Fatalf("remove of missing progress file: %v", err)
	}

	if err := writeRebuildProgress(dir, "comp.lang.go"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writeRebuildProgress(dir, "de.alt.test"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	last, err = readRebuildProgress(dir)
	if err != nil || last != "de.alt.test" {
		t.Fatalf("read: got %q, %v; want de.alt.test", last, err)
	}
	if _, err := os.Stat(filepath.Join(dir, rebuildProgressFileName+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("tmp file left behind: %v", err)
	}

	if err := removeRebuildProgress(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if last, _ = readRebuildProgress(dir); last != "" {
		t.Fatalf("after remove: got %q", last)
	}
}

func TestToolsTestSkipGroupForResume(t *testing.T) {
	cases := []struct {
		group, last string
		skip        bool
	}{
		{"alt.test", "", false},
		{"alt.test", "alt.test", true},
		{"alt.aaa", "alt.test", true},
		{"alt.test.sub", "alt.test", false},
		{"comp.lang.go", "alt.test", false},
	}
	for _, c := range cases {
		if got := skipGroupForResume(c.group, c.last); got != c.skip {
			t.Errorf("skipGroupForResume(%q, %q) = %t, want %t", c.group, c.last, got, c.skip)
		}
	}
}

func TestToolsTestCountGroupsInCSV(t *testing.T) {
	cases := map[string]int{
		"":        0,
		"1":       1,
		"1,2":     2,
		"12,3,44": 3,
	}
	for in, want := range cases {
		if got := countGroupsInCSV(in); got != want {
			t.Errorf("countGroupsInCSV(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestToolsTestAnalyzeHistoryDir(t *testing.T) {
	dir := t.TempDir()

	history.ENABLE_HISTORY = true
	cfg := history.DefaultConfig()
	cfg.HistoryDir = dir
	cfg.BatchTimeout = 50
	h, err := history.NewHistory(cfg, nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	h.AddArticle("<one@test>", 1)
	h.AddArticle("<two@test>", 1)
	h.AddArticle("<two@test>", 2)
	h.AddArticle("<three@test>", 1)
	h.AddArticle("<three@test>", 2)
	h.AddArticle("<three@test>", 3)
	h.AddArticle("<three@test>", 3) // idempotent
	h.AddArticle("<gone@test>", 5)
	h.RemoveArticle("<gone@test>", 5) // row deleted
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	res, err := analyzeHistoryDir(dir)
	if err != nil {
		t.Fatalf("analyzeHistoryDir: %v", err)
	}
	if res.MissingFiles != 0 || len(res.Files) != 16 {
		t.Fatalf("files: missing=%d len=%d", res.MissingFiles, len(res.Files))
	}
	if res.TotalRows != 3 {
		t.Fatalf("TotalRows = %d, want 3", res.TotalRows)
	}
	for n, want := range map[int]int64{1: 1, 2: 1, 3: 1} {
		if got := res.GroupsHistogram[n]; got != want {
			t.Errorf("histogram[%d] = %d, want %d (%v)", n, got, want, res.GroupsHistogram)
		}
	}
	if res.MaxGroups != 3 {
		t.Errorf("MaxGroups = %d, want 3", res.MaxGroups)
	}
	var tables int
	for _, fa := range res.Files {
		tables += fa.Tables
	}
	if tables != 16*256 {
		t.Errorf("tables = %d, want %d", tables, 16*256)
	}

	// a missing file is reported, not an error
	if err := os.Remove(historyFilePath(dir, 0xa)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	res, err = analyzeHistoryDir(dir)
	if err != nil {
		t.Fatalf("analyzeHistoryDir after remove: %v", err)
	}
	if res.MissingFiles != 1 || !res.Files[0xa].Missing {
		t.Fatalf("missing file not reported: %d", res.MissingFiles)
	}
}
