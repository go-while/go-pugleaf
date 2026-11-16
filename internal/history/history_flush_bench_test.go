package history

import (
	"fmt"
	"testing"
)

// BenchmarkHistoryFlush10k measures one flush of 10,000 new message-IDs spread over all files
// and tables (the importer's batch size). Compare against the base commit to see the prepare cost.
func BenchmarkHistoryFlush10k(b *testing.B) {
	old := ENABLE_HISTORY
	ENABLE_HISTORY = true
	b.Cleanup(func() { ENABLE_HISTORY = old })
	cfg := histTestConfig(b.TempDir() + "/history")
	cfg.BatchTimeout = 3600000 // the benchmark flushes itself
	cfg.BatchSize = 100000
	h, err := NewHistory(cfg, nil)
	if err != nil {
		b.Fatalf("NewHistory: %v", err)
	}
	b.Cleanup(func() { h.Close() })

	// Never enqueue ops here: flushBatch runs on this goroutine, outside the writer goroutine,
	// which is safe only while the writer has nothing to flush.
	const per = 10000
	batch := make([]historyOp, per)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for j := range batch {
			batch[j] = historyOp{op: opAdd, messageID: fmt.Sprintf("<b%d.%d@bench>", i, j), groupID: 1, articleNum: int64(j + 1)}
		}
		h.pending.Add(per) // flushBatch subtracts what it wrote
		b.StartTimer()
		h.flushBatch(batch)
	}
}
