package history

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// Tests of the history SQLite memory knobs (plan history-cache-knobs): HistoryConfig.PageCacheMB
// and MmapMB reach every pooled connection of every database file, and 0 keeps the values
// that were fixed before (_cache_size=-8000, PRAGMA mmap_size = 16777216).

// hcOldDSNParams is the fixed DSN of the history pools before PageCacheMB existed
const hcOldDSNParams = "?_journal_mode=WAL&_synchronous=OFF&_busy_timeout=30000&_txlock=immediate&_cache_size=-8000"

// hcShards are the database files whose connections the pool tests inspect
var hcShards = []int{0x0, 0x7, 0xf}

// hcConnsPerShard connections are held at the same time per file, so database/sql must open
// that many distinct connections (the test pools allow 4)
const hcConnsPerShard = 3

// hcNewHistory opens an enabled history with cfg in a temp dir; closed via t.Cleanup
func hcNewHistory(t *testing.T, cfg *HistoryConfig) *History {
	t.Helper()
	histTestEnable(t, true)
	h, err := NewHistory(cfg, nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// hcCheckPools holds hcConnsPerShard connections of each file in hcShards at once and checks
// PRAGMA cache_size and PRAGMA mmap_size on every one of them
func hcCheckPools(t *testing.T, s *SQLite3ShardedDB, wantCache, wantMmap int64) {
	t.Helper()
	ctx := context.Background()
	for _, idx := range hcShards {
		hcCheckShard(t, ctx, s, idx, wantCache, wantMmap)
	}
}

// hcCheckShard is hcCheckPools for one file. Every connection is released by a defer
// registered right after it was acquired, so a failing check (t.Fatalf) never leaves one
// checked out, and all are back in the pool when it returns (a later check of the same
// pool needs them again).
func hcCheckShard(t *testing.T, ctx context.Context, s *SQLite3ShardedDB, idx int, wantCache, wantMmap int64) {
	t.Helper()
	db, err := s.GetShardedDB(idx, false)
	if err != nil {
		t.Fatalf("GetShardedDB(%d): %v", idx, err)
	}
	conns := make([]*sql.Conn, 0, hcConnsPerShard)
	for i := 0; i < hcConnsPerShard; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("shard %x conn %d: %v", idx, i, err)
		}
		defer func() {
			if err := c.Close(); err != nil {
				t.Errorf("shard %x conn %d: close: %v", idx, i, err)
			}
		}()
		conns = append(conns, c)
	}
	for i, c := range conns {
		var cache, mmap int64
		if err := c.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&cache); err != nil {
			t.Fatalf("shard %x conn %d: PRAGMA cache_size: %v", idx, i, err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA mmap_size").Scan(&mmap); err != nil {
			t.Fatalf("shard %x conn %d: PRAGMA mmap_size: %v", idx, i, err)
		}
		if cache != wantCache {
			t.Errorf("shard %x conn %d: cache_size = %d, want %d", idx, i, cache, wantCache)
		}
		if mmap != wantMmap {
			t.Errorf("shard %x conn %d: mmap_size = %d, want %d", idx, i, mmap, wantMmap)
		}
	}
}

// TestHistoryCacheDefaultsUnchanged: a config without the knobs gives the old DSN and the old
// values on every connection.
func TestHistoryCacheDefaultsUnchanged(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if cfg.PageCacheMB != 0 || cfg.MmapMB != 0 {
		t.Fatalf("DefaultConfig PageCacheMB=%d MmapMB=%d, want 0/0", cfg.PageCacheMB, cfg.MmapMB)
	}
	if got := historyDSNParams(cfg.pageCacheKiB()); got != hcOldDSNParams {
		t.Errorf("default DSN params\n got %s\nwant %s", got, hcOldDSNParams)
	}
	if got := cfg.mmapBytes(); got != 16777216 {
		t.Errorf("default mmap bytes = %d, want 16777216", got)
	}

	h := hcNewHistory(t, histTestConfig(filepath.Join(t.TempDir(), "history")))
	hcCheckPools(t, h.db, -8000, 16777216)
}

// TestHistoryCacheCustomValues: custom values reach several pooled connections of several
// files; the mmap cap is below SQLite's own limit, so SQLite keeps it as given.
func TestHistoryCacheCustomValues(t *testing.T) {
	for _, tc := range []struct {
		name            string
		cacheMB, mmapMB int
		wantCache       int64
		wantMmap        int64
	}{
		{"64-256", 64, 256, -64 * 1024, 256 << 20},
		{"1-1", 1, 1, -1024, 1 << 20},
		{"caps", MaxPageCacheMB, MaxMmapMB, -int64(MaxPageCacheMB) * 1024, int64(MaxMmapMB) << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := histTestConfig(filepath.Join(t.TempDir(), "history"))
			cfg.PageCacheMB = tc.cacheMB
			cfg.MmapMB = tc.mmapMB
			h := hcNewHistory(t, cfg)
			hcCheckPools(t, h.db, tc.wantCache, tc.wantMmap)
		})
	}
}

// TestHistoryCachePoolsIndependent: two pools with different settings open at the same time
// keep their own values (nothing is shared through the registered driver).
func TestHistoryCachePoolsIndependent(t *testing.T) {
	a, err := NewSQLite3ShardedDB(&ShardConfig{BaseDir: t.TempDir(), MaxOpenPerDB: 4, CacheKiB: 2048, MmapBytes: 32 << 20}, false)
	if err != nil {
		t.Fatalf("pool a: %v", err)
	}
	defer a.Close()
	b, err := NewSQLite3ShardedDB(&ShardConfig{BaseDir: t.TempDir(), MaxOpenPerDB: 4}, false)
	if err != nil {
		t.Fatalf("pool b: %v", err)
	}
	defer b.Close()
	hcCheckPools(t, a, -2048, 32<<20)
	hcCheckPools(t, b, -8000, 16777216)
	hcCheckPools(t, a, -2048, 32<<20)
}

// TestHistoryCacheClamp: ValidateConfig clamps the knobs to 0..cap, and a ShardConfig above
// the caps is clamped as well.
func TestHistoryCacheClamp(t *testing.T) {
	for _, tc := range []struct {
		cacheMB, mmapMB         int
		wantCacheMB, wantMmapMB int
	}{
		{0, 0, 0, 0},
		{-5, -1, 0, 0},
		{MaxPageCacheMB, MaxMmapMB, MaxPageCacheMB, MaxMmapMB},
		{MaxPageCacheMB + 1, MaxMmapMB + 1, MaxPageCacheMB, MaxMmapMB},
		{1 << 30, 1 << 30, MaxPageCacheMB, MaxMmapMB},
	} {
		cfg := DefaultConfig()
		cfg.PageCacheMB = tc.cacheMB
		cfg.MmapMB = tc.mmapMB
		if err := cfg.ValidateConfig(); err != nil {
			t.Fatalf("ValidateConfig: %v", err)
		}
		if cfg.PageCacheMB != tc.wantCacheMB || cfg.MmapMB != tc.wantMmapMB {
			t.Errorf("in %d/%d: got PageCacheMB=%d MmapMB=%d, want %d/%d",
				tc.cacheMB, tc.mmapMB, cfg.PageCacheMB, cfg.MmapMB, tc.wantCacheMB, tc.wantMmapMB)
		}
	}

	// unvalidated values: the effective getters cap too
	cfg := &HistoryConfig{PageCacheMB: 1 << 30, MmapMB: 1 << 30}
	if got, want := cfg.pageCacheKiB(), MaxPageCacheMB*1024; got != want {
		t.Errorf("pageCacheKiB = %d, want %d", got, want)
	}
	if got, want := cfg.mmapBytes(), int64(MaxMmapMB)<<20; got != want {
		t.Errorf("mmapBytes = %d, want %d", got, want)
	}

	sc := &ShardConfig{BaseDir: t.TempDir(), MaxOpenPerDB: 4, CacheKiB: 1 << 30, MmapBytes: 1 << 50}
	s, err := NewSQLite3ShardedDB(sc, false)
	if err != nil {
		t.Fatalf("NewSQLite3ShardedDB: %v", err)
	}
	defer s.Close()
	if sc.CacheKiB != MaxPageCacheMB*1024 || sc.MmapBytes != int64(MaxMmapMB)<<20 {
		t.Errorf("ShardConfig clamped to CacheKiB=%d MmapBytes=%d", sc.CacheKiB, sc.MmapBytes)
	}
	hcCheckPools(t, s, -int64(MaxPageCacheMB)*1024, int64(MaxMmapMB)<<20)
}
