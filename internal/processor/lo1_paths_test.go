package processor

import (
	"path/filepath"
	"strings"
	"testing"
)

// The overview cache must live below the -data root, and the file name must not
// change (sha256 of the sanitized group name + ".overview").
func TestLo1PathsAnalyzeCachePath(t *testing.T) {
	got := analyzeCachePath("/x/data", "prov", "a.b")
	wantDir := filepath.Join("/x/data", "cache", "prov")
	if filepath.Dir(got) != wantDir {
		t.Fatalf("dir of %q = %q, want %q", got, filepath.Dir(got), wantDir)
	}
	base := filepath.Base(got)
	if !strings.HasSuffix(base, ".overview") {
		t.Fatalf("file name %q does not end in .overview", base)
	}
	if want := sha256hashFromString("a.b") + ".overview"; base != want {
		t.Fatalf("file name = %q, want %q", base, want)
	}
	if strings.HasPrefix(got, "data"+string(filepath.Separator)) {
		t.Fatalf("path %q is relative to the cwd", got)
	}
}

// The path separators of a group name are replaced, and different data roots and
// providers give different files while the same input gives the same file.
func TestLo1PathsAnalyzeCachePathSeparation(t *testing.T) {
	a := analyzeCachePath("/x/data", "prov", "a.b")
	b := analyzeCachePath("/y/data", "prov", "a.b")
	c := analyzeCachePath("/x/data", "other", "a.b")
	d := analyzeCachePath("/x/data", "prov", "a.c")
	if a == b || a == c || a == d {
		t.Fatalf("paths are not distinct: %q %q %q %q", a, b, c, d)
	}
	if a != analyzeCachePath("/x/data", "prov", "a.b") {
		t.Fatal("analyzeCachePath is not deterministic")
	}
	slashed := analyzeCachePath("/x/data", "prov", "a/b")
	if strings.Contains(filepath.Base(slashed), "/") {
		t.Fatalf("group separator leaked into the file name: %q", slashed)
	}
}

// With no DB attached the helper falls back to "data", which keeps the old
// behaviour for a Processor built without a database.
func TestLo1PathsGetCacheFilePathNilDB(t *testing.T) {
	proc := &Processor{}
	got := proc.getCacheFilePath("prov", "a.b")
	if want := analyzeCachePath("data", "prov", "a.b"); got != want {
		t.Fatalf("getCacheFilePath with nil DB = %q, want %q", got, want)
	}
}
