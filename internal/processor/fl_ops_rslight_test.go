package processor

// Tests of the followup-leftovers slice fl-ops-tools (rslight import): the import error
// names at most legacyErrSummaryMax files plus a count, and a closed stop channel ends
// the start delay, the file loop and the article loop of a file.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlOpsRslightSummarizeErrs(t *testing.T) {
	if err := summarizeLegacyErrs(nil); err != nil {
		t.Fatalf("no errors: %v, want nil", err)
	}
	errBoom := errors.New("boom")
	var errs []error
	for i := 0; i < 12; i++ {
		errs = append(errs, fmt.Errorf("file 'f%02d': %w", i, errBoom))
	}
	err := summarizeLegacyErrs(errs)
	if !errors.Is(err, errBoom) {
		t.Fatalf("err %v does not wrap errBoom", err)
	}
	msg := err.Error()
	if lines := strings.Count(msg, "\n") + 1; lines != legacyErrSummaryMax+1 {
		t.Fatalf("summary has %d lines, want %d (first %d + count):\n%s", lines, legacyErrSummaryMax+1, legacyErrSummaryMax, msg)
	}
	if !strings.Contains(msg, "f04") || strings.Contains(msg, "f05") || !strings.Contains(msg, "and 7 more failed files") {
		t.Fatalf("summary does not list f00..f04 plus 7 more:\n%s", msg)
	}
	if got := summarizeLegacyErrs(errs[:3]).Error(); strings.Contains(got, "more failed") || strings.Count(got, "\n") != 2 {
		t.Fatalf("3 errors: %q, want the 3 errors only", got)
	}
}

func TestFlOpsRslightStartDelayStops(t *testing.T) {
	if err := waitLegacyStartDelay(0, nil); err != nil {
		t.Fatalf("no delay: %v", err)
	}
	stop := make(chan struct{})
	close(stop)
	done := make(chan error, 1)
	go func() { done <- waitLegacyStartDelay(time.Hour, stop) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrLegacyImportStopped) {
			t.Fatalf("err = %v, want ErrLegacyImportStopped", err)
		}
	case <-time.After(testWait):
		t.Fatalf("the start delay did not end within %v after stop was closed", testWait)
	}
}

func TestFlOpsRslightImportFilesStop(t *testing.T) {
	files := []string{"a", "b", "c", "d"}

	// stop closed before the start: no file is imported
	closed := make(chan struct{})
	close(closed)
	var calls atomic.Int64
	failed, err := importLegacyFiles(files, 2, closed, func(string) error { calls.Add(1); return nil })
	if calls.Load() != 0 || failed != 0 || !errors.Is(err, ErrLegacyImportStopped) {
		t.Fatalf("pre-closed stop: calls=%d failed=%d err=%v, want 0 0 ErrLegacyImportStopped", calls.Load(), failed, err)
	}

	// stop closed while the first file runs (one thread): no further file is started;
	// a failure before the stop is still reported
	stop := make(chan struct{})
	calls.Store(0)
	errBoom := errors.New("boom")
	failed, err = importLegacyFiles(files, 1, stop, func(file string) error {
		calls.Add(1)
		close(stop)
		return errBoom
	})
	if calls.Load() != 1 {
		t.Fatalf("files started after the stop: %d calls, want 1", calls.Load())
	}
	if failed != 1 || !errors.Is(err, ErrLegacyImportStopped) || !errors.Is(err, errBoom) {
		t.Fatalf("failed=%d err=%v, want 1 and both ErrLegacyImportStopped and errBoom", failed, err)
	}

	// a file that returns ErrLegacyImportStopped is not a failed file
	failed, err = importLegacyFiles([]string{"x"}, 1, nil, func(string) error {
		return fmt.Errorf("file 'x' not finished: %w", ErrLegacyImportStopped)
	})
	if failed != 0 || !errors.Is(err, ErrLegacyImportStopped) {
		t.Fatalf("stopped file: failed=%d err=%v, want 0 and ErrLegacyImportStopped", failed, err)
	}
}

func TestFlOpsRslightArticleLoopStops(t *testing.T) {
	const group = "flops.rslight.stop"
	path := filepath.Join(t.TempDir(), group+"-articles.db3")
	adfOptRslightSpool(t, path, group, []string{"a1", "a2", "a3", "a4"})

	stop := make(chan struct{})
	leg := &LegacyImporter{proc: &Processor{LegacyCounter: NewCounter()}, stop: stop}
	var seen int
	err := leg.importSQLiteArticlesWith(path, func(*LegacyArticle) error {
		seen++
		if seen == 2 {
			close(stop)
		}
		return nil
	})
	if !errors.Is(err, ErrLegacyImportStopped) {
		t.Fatalf("err = %v, want ErrLegacyImportStopped", err)
	}
	if seen != 2 || leg.imported != 2 || leg.groupsDone != 0 {
		t.Fatalf("seen=%d imported=%d groupsDone=%d, want 2 2 0 (a stopped file is not done)", seen, leg.imported, leg.groupsDone)
	}
}
