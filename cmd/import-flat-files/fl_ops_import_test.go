package main

// Tests of the followup-leftovers slice fl-ops-tools (import-flat-files): -dry-run
// scans and counts but opens (and creates) no database file, also with -update.

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestFlOpsImportDryRunOpensNoDatabase(t *testing.T) {
	for _, upd := range []bool{false, true} {
		out := adfOptImportReset(t)
		*dryRun, *update = true, upd
		src := t.TempDir()
		headRoot, bodyRoot := filepath.Join(src, "head"), filepath.Join(src, "body")
		adfOptImportWritePair(t, headRoot, bodyRoot, "0/0/1", "abc0123456789abcdef0123456789abcdef", "Subject: a\n", "a\n")
		adfOptImportWritePair(t, headRoot, bodyRoot, "0/1/2", "def0123456789abcdef0123456789abcdef", "Subject: b\n", "b\n")

		dm := adfOptImportRun(t, headRoot, bodyRoot)

		if p, e := atomic.LoadInt64(&totalProcessed), atomic.LoadInt64(&totalErrors); p != 2 || e != 0 {
			t.Fatalf("update=%t: processed/errors = %d/%d, want 2/0", upd, p, e)
		}
		entries, err := os.ReadDir(out)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("update=%t: the dry run created %d entries in the -db directory: %v", upd, len(entries), entries)
		}
		dm.mutex.RLock()
		opened := len(dm.dbConnections)
		dm.mutex.RUnlock()
		if opened != 0 {
			t.Fatalf("update=%t: %d databases opened in a dry run", upd, opened)
		}
		if _, err := dm.GetDB("00"); !errors.Is(err, errDryRunNoDB) {
			t.Fatalf("GetDB in a dry run: err = %v, want errDryRunNoDB", err)
		}
	}
}
