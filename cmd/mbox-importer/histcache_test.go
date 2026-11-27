package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// Tests of -history-cache-mb and -history-mmap-mb (plan history-cache-knobs): the flags reach
// processor.HistoryPageCacheMB / HistoryMmapMB, which NewProcessorE passes to the history
// config, and the history open logs the effective values.

func TestMboxCmdHistoryCacheFlagsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"-history-cache-mb", "-1", "x.mbox"},
		{"-history-mmap-mb", "-1", "x.mbox"},
	} {
		if code, _ := miRun(t, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
}

func TestMboxCmdHistoryCacheFlags(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	oldCache, oldMmap := processor.HistoryPageCacheMB, processor.HistoryMmapMB
	t.Cleanup(func() { processor.HistoryPageCacheMB, processor.HistoryMmapMB = oldCache, oldMmap })

	// the test processor (no DNS check) takes the two values from the processor package the
	// way NewProcessorE does, and records what it saw
	var gotCache, gotMmap int
	newProcessor = func(db *database.Database, hashLen int) (*processor.Processor, error) {
		gotCache, gotMmap = processor.HistoryPageCacheMB, processor.HistoryMmapMB
		cfg := history.DefaultConfig()
		cfg.HistoryDir = filepath.Join(db.GetDataDir(), "history")
		cfg.BatchTimeout = 50 // ms
		cfg.MaxConnections = 4
		cfg.UseShortHashLen = hashLen
		cfg.PageCacheMB = processor.HistoryPageCacheMB
		cfg.MmapMB = processor.HistoryMmapMB
		hist, err := history.NewHistory(cfg, db.WG)
		if err != nil {
			return nil, err
		}
		proc := &processor.Processor{
			DB:            db,
			History:       hist,
			MsgIdCache:    history.NewMsgIdItemCache(),
			ThreadCounter: processor.NewCounter(),
			LegacyCounter: processor.NewCounter(),
		}
		db.Batch.SetProcessor(proc)
		hist.SetDatabaseWorkChecker(db.Batch)
		return proc, nil
	}

	// the cases run in order: a run without the flags after one with them is back at 0
	for _, tc := range []struct {
		name               string
		flags              []string
		wantCache, wantMap int
		wantLog            []string
	}{
		{"set", []string{"-history-cache-mb", "64", "-history-mmap-mb", "256"}, 64, 256, []string{
			"[HISTORY] page cache 64 MiB per connection (_cache_size=-65536, max 4 connections per file), mmap 256 MiB per file (mmap_size=268435456)",
		}},
		{"default", nil, 0, 0, []string{
			"[HISTORY] page cache 8 MiB per connection (_cache_size=-8000, max 4 connections per file), mmap 16 MiB per file (mmap_size=16777216)",
		}},
		{"clamped", []string{"-history-cache-mb", "99999", "-history-mmap-mb", "99999"}, 99999, 99999, []string{
			"[HISTORY] WARN: PageCacheMB 99999 is above the cap, using 4096",
			"[HISTORY] WARN: MmapMB 99999 is above the cap, using 2047",
			"[HISTORY] page cache 4096 MiB per connection (_cache_size=-4194304, max 4 connections per file), mmap 2047 MiB per file (mmap_size=2146435072)",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "data")
			in := miWrite(t, t.TempDir(), "g.mbox", miFixture())
			gotCache, gotMmap = -1, -1

			var buf bytes.Buffer
			log.SetOutput(&buf)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			args := append(append([]string{"-data", data, "-trackdb", "off"}, tc.flags...), in)
			code, sum := miRun(t, args...)
			log.SetOutput(os.Stderr) // the buffer is read only after the logger stopped writing to it

			if code != 0 {
				t.Fatalf("exit %d\n%s", code, buf.String())
			}
			miCheck(t, sum, 3, 2, 0, 0, 0, "no-subject=1")
			if gotCache != tc.wantCache || gotMmap != tc.wantMap {
				t.Errorf("processor saw HistoryPageCacheMB=%d HistoryMmapMB=%d, want %d/%d", gotCache, gotMmap, tc.wantCache, tc.wantMap)
			}
			out := buf.String()
			for _, want := range tc.wantLog {
				if !strings.Contains(out, want) {
					t.Errorf("log lacks %q\n%s", want, out)
				}
			}
		})
	}
}
