package nntp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-while/go-pugleaf/internal/config"
)

// FileCachedListNewsgroups must read the cache below the directory it is given
// (the fetcher passes <data>/cache), not below "data" relative to the cwd.
// The backend points at 127.0.0.1:1, so any attempt to dial fails the test.
func TestLo1PathsFileCachedListNewsgroups(t *testing.T) {
	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	const host = "lo1paths.invalid"
	want := []string{"alt.test", "comp.lang.go", "misc.lo1.paths"}
	body := ""
	for _, g := range want {
		body += g + "\n"
	}
	if err := os.WriteFile(filepath.Join(cacheDir, host+".list"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	pool := NewPool(&BackendConfig{
		Host:     "127.0.0.1",
		Port:     1,
		MaxConns: 1,
		Provider: &config.Provider{Name: "lo1paths", Host: host, Port: 1, MaxConns: 1},
	})
	t.Cleanup(func() {
		if err := pool.ClosePool(); err != nil {
			t.Logf("ClosePool: %v", err)
		}
	})

	got, err := pool.FileCachedListNewsgroups(cacheDir)
	if err != nil {
		t.Fatalf("FileCachedListNewsgroups(%s): %v (a dial to 127.0.0.1:1 means the cache was not used)", cacheDir, err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d groups %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("group %d: got %q, want %q", i, got[i], want[i])
		}
	}

	// Nothing may be written below a "data" directory in the cwd.
	if _, err := os.Stat("data"); err == nil {
		t.Fatalf("FileCachedListNewsgroups created ./data relative to the cwd")
	}
}

// A cache directory without the provider file must not be read from the cwd either:
// the pool then tries the server, which fails because the backend is 127.0.0.1:1.
func TestLo1PathsFileCachedListNewsgroupsMiss(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	pool := NewPool(&BackendConfig{
		Host:     "127.0.0.1",
		Port:     1,
		MaxConns: 1,
		Provider: &config.Provider{Name: "lo1paths", Host: "lo1paths-miss.invalid", Port: 1, MaxConns: 1},
	})
	t.Cleanup(func() {
		if err := pool.ClosePool(); err != nil {
			t.Logf("ClosePool: %v", err)
		}
	})

	groups, err := pool.FileCachedListNewsgroups(cacheDir)
	if err == nil {
		t.Fatalf("cache miss against 127.0.0.1:1 returned %d groups and no error", len(groups))
	}
}
