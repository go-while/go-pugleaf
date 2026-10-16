package history

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"
)

// Plan xref-batched-lookup: one LookupLocations per id vs one LookupLocationsBatch for the
// 1000 ids of an OVER range. Benchmarks only (no timing asserts anywhere):
//
//	go test -run xxx -bench 'LookupLocations' -benchmem ./internal/history/

const (
	benchXrefRows    = 100000 // rows in the scratch index
	benchXrefLookups = 1000   // ids looked up per op (one OVER range)
)

// benchXrefHistory fills a scratch index in b.TempDir() with benchXrefRows numbered rows
// (every 4th one crossposted to a second group) and returns it with the ids of one lookup
// range: benchXrefLookups ids, 10% of them absent from the index.
func benchXrefHistory(b *testing.B) (*History, []string) {
	b.Helper()
	old := ENABLE_HISTORY
	ENABLE_HISTORY = true
	b.Cleanup(func() { ENABLE_HISTORY = old })

	cfg := histTestConfig(filepath.Join(b.TempDir(), "history"))
	cfg.BatchSize = 10000
	h, err := NewHistory(cfg, nil)
	if err != nil {
		b.Fatalf("NewHistory: %v", err)
	}
	b.Cleanup(func() { h.Close() })

	rnd := rand.New(rand.NewPCG(1, 2))
	all := make([]string, benchXrefRows)
	for i := range all {
		id := fmt.Sprintf("<%016x.%d@bench.example>", rnd.Uint64(), i)
		all[i] = id
		h.AddArticleAt(id, int64(i%50+1), int64(i+1))
		if i%4 == 0 {
			h.AddArticleAt(id, int64(i%50+51), int64(i/4+1))
		}
	}
	deadline := time.Now().Add(testWait)
	for !h.CheckNoMoreWorkInHistory() {
		if time.Now().After(deadline) {
			b.Fatalf("history still has pending work: %d", h.pending.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	ids := make([]string, 0, benchXrefLookups)
	start := rnd.IntN(benchXrefRows - benchXrefLookups)
	for i := 0; i < benchXrefLookups; i++ {
		if i%10 == 9 {
			ids = append(ids, fmt.Sprintf("<absent.%d@bench.example>", i))
			continue
		}
		ids = append(ids, all[start+i])
	}
	return h, ids
}

func BenchmarkLookupLocations(b *testing.B) {
	h, ids := benchXrefHistory(b)
	b.ReportAllocs()
	for b.Loop() {
		for _, id := range ids {
			if _, err := h.LookupLocations(id); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkLookupLocationsBatch(b *testing.B) {
	h, ids := benchXrefHistory(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.LookupLocationsBatch(ids); err != nil {
			b.Fatal(err)
		}
	}
}
