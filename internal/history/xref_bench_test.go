package history

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"path/filepath"
	"testing"
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
// range: benchXrefLookups ids of consecutive rows, every 10th one absent from the index.
// The rows are written directly (one transaction per shard database), not through the
// writer queue, to keep the setup short.
func benchXrefHistory(b *testing.B) (*History, []string) {
	b.Helper()
	oldEnable := ENABLE_HISTORY
	ENABLE_HISTORY = true
	oldLog := log.Writer()
	log.SetOutput(io.Discard) // keep the [HISTORY] open/close lines out of the benchmark output
	b.Cleanup(func() {
		ENABLE_HISTORY = oldEnable
		log.SetOutput(oldLog)
	})

	h, err := NewHistory(histTestConfig(filepath.Join(b.TempDir(), "history")), nil)
	if err != nil {
		b.Fatalf("NewHistory: %v", err)
	}
	b.Cleanup(func() { h.Close() })

	rnd := rand.New(rand.NewPCG(1, 2))
	all := make([]string, benchXrefRows)
	perDB := make(map[int]map[string][][2]string) // dbIndex -> table -> (message_id, newsgroups)
	for i := range all {
		id := fmt.Sprintf("<%016x.%d@bench.example>", rnd.Uint64(), i)
		all[i] = id
		newsgroups := historyToken(int64(i%50+1), int64(i+1))
		if i%4 == 0 {
			newsgroups += "," + historyToken(int64(i%50+51), int64(i/4+1))
		}
		dbIndex, tableName, err := h.routeHash(id)
		if err != nil {
			b.Fatal(err)
		}
		if perDB[dbIndex] == nil {
			perDB[dbIndex] = make(map[string][][2]string)
		}
		perDB[dbIndex][tableName] = append(perDB[dbIndex][tableName], [2]string{id, newsgroups})
	}
	for dbIndex, tables := range perDB {
		db, err := h.db.GetShardedDB(dbIndex, true)
		if err != nil {
			b.Fatal(err)
		}
		if err := benchXrefFillDB(db, tables); err != nil {
			b.Fatalf("fill hashdb_%x: %v", dbIndex, err)
		}
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

// benchXrefFillDB inserts the rows of one shard database in one transaction
func benchXrefFillDB(db *sql.DB, tables map[string][][2]string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for tableName, rows := range tables {
		stmt, err := tx.Prepare("INSERT INTO " + tableName + "(message_id,newsgroups) VALUES(?,?)")
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, r := range rows {
			if _, err := stmt.Exec(r[0], r[1]); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		if err := stmt.Close(); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
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
		got, err := h.LookupLocationsBatch(ids)
		if err != nil {
			b.Fatal(err)
		}
		if len(got) != benchXrefLookups-benchXrefLookups/10 {
			b.Fatalf("batch found %d ids, want %d", len(got), benchXrefLookups-benchXrefLookups/10)
		}
	}
}
