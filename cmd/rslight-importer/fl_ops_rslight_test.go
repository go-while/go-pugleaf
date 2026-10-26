//go:build unix

package main

// Tests of the followup-leftovers slice fl-ops-tools (rslight-importer): a SIGINT
// closes the stop channel that the reset countdown and the article import watch (run
// then returns through shutdown()), and a stopped run exits 130.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/processor"
)

// testWait is the hang guard of this package's tests (plan test-timing-hardening D3):
// a passing test never waits on it. Override with PUGLEAF_TEST_DEADLINE.
var testWait = func() time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}()

func TestFlOpsRslightSignalClosesStop(t *testing.T) {
	stop, release := stopOnSignal()
	defer release()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-stop:
	case <-time.After(testWait):
		t.Fatalf("stop was not closed within %v after SIGINT", testWait)
	}
	release()
	release() // idempotent
}

func TestFlOpsRslightImportExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{errors.New("boom"), 1},
		{errResetCancelled, 130},
		{fmt.Errorf("failed to import SQLite databases: %w", fmt.Errorf("before the article import: %w", processor.ErrLegacyImportStopped)), 130},
	}
	for _, c := range cases {
		if got := importExitCode(c.err); got != c.want {
			t.Errorf("importExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
