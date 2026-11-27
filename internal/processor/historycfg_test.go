package processor

import (
	"path/filepath"
	"testing"

	"github.com/go-while/go-pugleaf/internal/history"
)

// TestHistoryConfigForCacheKnobs: the history configuration NewProcessorE opens the index
// with carries HistoryPageCacheMB / HistoryMmapMB (plan history-cache-knobs) and keeps the
// hardcoded values.
func TestHistoryConfigForCacheKnobs(t *testing.T) {
	oldCache, oldMmap := HistoryPageCacheMB, HistoryMmapMB
	t.Cleanup(func() { HistoryPageCacheMB, HistoryMmapMB = oldCache, oldMmap })

	for _, tc := range []struct {
		cache, mmap int
	}{
		{0, 0},
		{64, 256},
		{1, 2047},
	} {
		HistoryPageCacheMB, HistoryMmapMB = tc.cache, tc.mmap
		cfg := HistoryConfigFor("/srv/pugleaf-data", 5)
		if cfg.PageCacheMB != tc.cache || cfg.MmapMB != tc.mmap {
			t.Errorf("globals %d/%d: config PageCacheMB=%d MmapMB=%d", tc.cache, tc.mmap, cfg.PageCacheMB, cfg.MmapMB)
		}
		want := history.HistoryConfig{
			HistoryDir:      filepath.Join("/srv/pugleaf-data", "history"),
			CacheExpires:    60,
			CachePurge:      15,
			ShardMode:       history.SHARD_16_256,
			MaxConnections:  8,
			UseShortHashLen: 5,
			PageCacheMB:     tc.cache,
			MmapMB:          tc.mmap,
		}
		if *cfg != want {
			t.Errorf("globals %d/%d: config %+v, want %+v", tc.cache, tc.mmap, *cfg, want)
		}
	}
}
