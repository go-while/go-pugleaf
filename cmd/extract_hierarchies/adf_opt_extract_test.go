package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestAdfOptExtractHierarchiesEmbedded: A-tools-18. The hierarchy list comes from
// the embedded migration, not from a file in the cwd, and no sample.db is written.
func TestAdfOptExtractHierarchiesEmbedded(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	h, err := createDatabaseAndReadHierarchies()
	if err != nil {
		t.Fatalf("createDatabaseAndReadHierarchies: %v", err)
	}
	for _, want := range []string{"comp", "alt", "sci"} {
		if !slices.Contains(h, want) {
			t.Fatalf("hierarchy %q missing from %d loaded names", want, len(h))
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cwd not empty after loading: %v", entries)
	}
}

// TestAdfOptExtractGroupAndWrite: lines go to the most specific hierarchy, and
// writeLines writes them and reports errors.
func TestAdfOptExtractGroupAndWrite(t *testing.T) {
	hier := []string{"comp", "comp.lang", "alt"}
	sort.Slice(hier, func(i, j int) bool { return len(hier[i]) > len(hier[j]) })
	in := "comp.lang.go 1 2 y\ncomp.os 1 2 y\nalt 1 2 y\nfoo.bar 1 2 y\nshort line\n\n"
	groups, unknown, total, err := groupActiveLines(strings.NewReader(in), hier)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(groups["comp.lang"]) != 1 || len(groups["comp"]) != 1 || len(groups["alt"]) != 1 || len(unknown) != 1 {
		t.Fatalf("total=%d groups=%v unknown=%v", total, groups, unknown)
	}

	out := filepath.Join(t.TempDir(), "x.active")
	if err := writeLines(out, []string{"a 1 2 y", "b 1 2 y"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "a 1 2 y\nb 1 2 y\n" {
		t.Fatalf("file = %q, %v", got, err)
	}
	if _, err := os.Stat("/dev/full"); err == nil {
		if err := writeLines("/dev/full", []string{"x"}); err == nil {
			t.Fatal("writeLines to /dev/full: want an error")
		}
	}
}
