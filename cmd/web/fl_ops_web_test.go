package main

// Tests of the followup-leftovers slice fl-ops-tools (cmd/web): a failed
// UpdateNewsgroupActive makes -disable-active fail (exit 1, not 0), the progress
// database registered for the server mode is closed by the teardown path, and the
// signal exit codes. They use the shared scratch database adfWebCmdDB (TestMain in
// adf_web_test.go); ./data is never used.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
)

func TestFlOpsWebDisableActiveUpdateFailure(t *testing.T) {
	adfOptWebSkipDB(t)
	adfWebCmdSeed(t)
	mainDB := adfWebCmdDB.GetMainDB()
	if _, err := mainDB.Exec(`CREATE TRIGGER fl_ops_web_fail_disable BEFORE UPDATE OF active ON newsgroups
		WHEN OLD.name = 'adf.webcmd.d' AND NEW.active = 0 BEGIN SELECT RAISE(ABORT, 'fl-ops forced failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := mainDB.Exec("DROP TRIGGER IF EXISTS fl_ops_web_fail_disable"); err != nil {
			t.Errorf("drop trigger: %v", err)
		}
	})
	path := adfWebCmdWriteFile(t, adfWebCmdOneOfFour)
	if err := disableNewsgroupsNotInActiveFile(adfWebCmdDB, path, disableActiveOpts{}); err == nil {
		t.Fatal("a failed UpdateNewsgroupActive was reported as success, want an error (exit 1)")
	}
	// the other groups were still processed
	adfWebCmdWantActive(t, map[string]bool{"adf.webcmd.b": true, "adf.webcmd.d": true})
}

func TestFlOpsWebProgressDBClosedByTeardownHelper(t *testing.T) {
	p, err := database.NewProgressDB(filepath.Join(t.TempDir(), "progress.db"))
	if err != nil {
		t.Fatalf("NewProgressDB: %v", err)
	}
	setWebProgressDB(p)
	closeWebProgressDB()
	webProgressDBMu.Lock()
	left := webProgressDB
	webProgressDBMu.Unlock()
	if left != nil {
		t.Fatal("closeWebProgressDB left the progress database registered")
	}
	if _, err := p.GetLastArticle("fl-ops-backend", "fl.ops.web"); err == nil {
		t.Fatal("progress database still usable after closeWebProgressDB, want it closed")
	}
	closeWebProgressDB() // a second call (several exit paths) is a no-op
}

func TestFlOpsWebSignalExitCode(t *testing.T) {
	if got := signalExitCode(os.Interrupt); got != 130 {
		t.Fatalf("signalExitCode(SIGINT) = %d, want 130", got)
	}
	if got := signalExitCode(syscall.SIGTERM); got != 143 {
		t.Fatalf("signalExitCode(SIGTERM) = %d, want 143", got)
	}
}

// TestFlOpsWebBootSignalWatch: a signal during the boot is handed to main (the server
// is then never started); after endBootWatch the watcher no longer reads sigChan, so a
// later signal stays there for the select at the end of main. The second-signal path
// (fatalAfterDB) ends the process and is not run here.
func TestFlOpsWebBootSignalWatch(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	bootSig, endBootWatch := watchBootSignals(nil, sigChan)
	sigChan <- os.Interrupt
	select {
	case sig := <-bootSig:
		if sig != os.Interrupt {
			t.Fatalf("boot signal = %v, want interrupt", sig)
		}
	case <-time.After(testWait):
		t.Fatalf("the boot signal was not passed on within %v", testWait)
	}
	endBootWatch()
	sigChan <- syscall.SIGTERM
	select {
	case sig := <-sigChan:
		if sig != syscall.SIGTERM {
			t.Fatalf("signal after the boot = %v, want SIGTERM", sig)
		}
	default:
		t.Fatal("the watcher read a signal after endBootWatch")
	}

	// no signal during the boot: bootSig stays empty
	quiet := make(chan os.Signal, 1)
	bootSig, endBootWatch = watchBootSignals(nil, quiet)
	endBootWatch()
	select {
	case sig := <-bootSig:
		t.Fatalf("boot signal %v without a signal", sig)
	default:
	}
}
