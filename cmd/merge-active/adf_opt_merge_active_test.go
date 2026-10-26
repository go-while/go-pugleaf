package main

import (
	"os"
	"path/filepath"
	"testing"
)

func adfOptMergeWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAdfOptMergeActiveCollectExcludesOutput: A-tools-18. The active.* glob must not
// pick up the primary file or a previous active.merged.
func TestAdfOptMergeActiveCollectExcludesOutput(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"active.isc", mergedActiveName, "a.active", "active.2025", "notes.txt"} {
		adfOptMergeWrite(t, filepath.Join(dir, name), "x 1 1 y\n")
	}
	got, err := collectActiveFiles(dir, "active.isc")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a.active"), filepath.Join(dir, "active.2025")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("collectActiveFiles = %v, want %v", got, want)
	}
}

// TestAdfOptMergeActiveSortedOutput: the merged file is sorted by group name.
func TestAdfOptMergeActiveSortedOutput(t *testing.T) {
	m := ActiveMap{
		"comp.os":  {GroupName: "comp.os", HighWater: "2", LowWater: "1", Status: "y"},
		"alt.test": {GroupName: "alt.test", HighWater: "5", LowWater: "1", Status: "n"},
		"bit.list": {GroupName: "bit.list", HighWater: "9", LowWater: "3", Status: "m"},
	}
	out := filepath.Join(t.TempDir(), mergedActiveName)
	if err := writeMergedActiveFile(out, m); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "alt.test 5 1 n\nbit.list 9 3 m\ncomp.os 2 1 y\n"
	if string(got) != want {
		t.Fatalf("merged file = %q, want %q", got, want)
	}
	if _, err := os.Stat("/dev/full"); err == nil {
		if err := writeActiveMap("/dev/full", m, "test"); err == nil {
			t.Fatal("writeActiveMap to /dev/full: want an error")
		}
	}
}

// TestAdfOptMergeActiveOverviewSkipsMissing: a missing overview file is skipped,
// not fatal for the whole run.
func TestAdfOptMergeActiveOverviewSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	ovDir := filepath.Join(dir, "ov")
	if err := os.MkdirAll(ovDir, 0o755); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, "local-mode.active")
	adfOptMergeWrite(t, active, "have.group 3 1 y\nmissing.group 3 1 y\n")
	adfOptMergeWrite(t, overviewFilePath(ovDir, "have.group"),
		"header\n1\tsubj\tfrom\tdate\t<a@b>\t\t10\t1\n")
	if err := processOverviewFiles(active, ovDir); err != nil {
		t.Fatalf("processOverviewFiles: %v", err)
	}
}
