package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAdfOptMergeDescriptionsNewestYearWins: A-tools-18. The latest file wins over
// every year file, and among year files the newest year wins.
func TestAdfOptMergeDescriptionsNewestYearWins(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"newsgroups.isc.latest":    "a.latest\tfrom latest\n",
		"newsgroups.isc.year.2019": "a.latest\told\ndup.group\tfrom 2019\nonly.old\tfrom 2019\n",
		"newsgroups.isc.year.2023": "dup.group\tfrom 2023\n",
		"newsgroups.isc.year.2021": "dup.group\tfrom 2021\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := mergeDescriptionFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.latest": "from latest", "dup.group": "from 2023", "only.old": "from 2019"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

// TestAdfOptMergeDescriptionsWriteErrors: writeDescriptions reports a write/flush
// error instead of returning success, and writes sorted output.
func TestAdfOptMergeDescriptionsWriteErrors(t *testing.T) {
	out := filepath.Join(t.TempDir(), "newsgroups.descriptions")
	if err := writeDescriptions(out, map[string]string{"b.g": "B", "a.g": "A"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "a.g\tA\nb.g\tB\n" {
		t.Fatalf("output = %q, %v", got, err)
	}
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available")
	}
	if err := writeDescriptions("/dev/full", map[string]string{"a.g": "A"}); err == nil {
		t.Fatal("writeDescriptions to /dev/full: want an error")
	}
}
