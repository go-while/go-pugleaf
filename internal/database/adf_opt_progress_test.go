package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAdfOptProgressInitSchemaErrorReturns: A-tools-6. When the progress DB file
// cannot be opened (here a directory sits where the file should be), NewProgressDB
// must return an error instead of retrying the schema forever and silently.
func TestAdfOptProgressInitSchemaErrorReturns(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "progress.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	type result struct {
		p   *ProgressDB
		err error
	}
	done := make(chan result, 1)
	go func() {
		p, err := NewProgressDB(dir)
		done <- result{p, err}
	}()
	select {
	case r := <-done:
		if r.err == nil {
			_ = r.p.Close()
			t.Fatal("NewProgressDB succeeded with a directory in place of progress.db")
		}
	case <-time.After(testWait):
		t.Fatalf("NewProgressDB still blocked after %v (schema retried forever)", testWait)
	}
}

// TestAdfOptProgressInitSchemaOK: the normal path still creates the schema and works.
func TestAdfOptProgressInitSchemaOK(t *testing.T) {
	p, err := NewProgressDB(t.TempDir())
	if err != nil {
		t.Fatalf("NewProgressDB: %v", err)
	}
	defer func() { _ = p.Close() }()
	if err := p.UpdateProgress("adfOptProgBackend", "adf.opt.prog", 42); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	got, err := p.GetLastArticle("adfOptProgBackend", "adf.opt.prog")
	if err != nil || got != 42 {
		t.Fatalf("GetLastArticle = %d, %v; want 42, nil", got, err)
	}
}
