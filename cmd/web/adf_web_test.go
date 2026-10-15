package main

// Tests of the audit-delta-fixes slice adf-web-cmd (AUDIT-2026-10 A-ops-4, OPS-4b,
// OPS-1c): -disable-active (D14), the legacy -useshorthashlen resolution and the stop
// path of the cmd/web background loops. One scratch database below os.TempDir() is
// opened for the whole test binary; ./data is never used.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

var adfWebCmdDB *database.Database

// adfWebCmdGroups are the 4 groups every -disable-active test starts with, all active.
var adfWebCmdGroups = []string{"adf.webcmd.a", "adf.webcmd.b", "adf.webcmd.c", "adf.webcmd.d"}

func TestMain(m *testing.M) {
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		fmt.Println("cmd/web: skipped: OpenDatabase refuses to run with euid < 1000")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "pugleaf-adf-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	database.NO_CACHE_BOOT = true
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "OpenDatabase:", err)
		_ = os.RemoveAll(dir)
		os.Exit(2)
	}
	adfWebCmdDB = db

	code := m.Run()

	if err := teardownDB(db); err != nil {
		fmt.Fprintln(os.Stderr, "teardownDB:", err)
		if code == 0 {
			code = 1
		}
	}
	if code == 0 {
		_ = os.RemoveAll(dir)
	} else {
		fmt.Fprintln(os.Stderr, "test data kept in", dir)
	}
	os.Exit(code)
}

// adfWebCmdActive returns the active flag of every group in the main DB.
func adfWebCmdActive(t *testing.T) map[string]bool {
	t.Helper()
	groups, err := adfWebCmdDB.MainDBGetAllNewsgroups()
	if err != nil {
		t.Fatalf("MainDBGetAllNewsgroups: %v", err)
	}
	out := make(map[string]bool, len(groups))
	for _, g := range groups {
		out[g.Name] = g.Active
	}
	return out
}

// adfWebCmdSeed makes sure the 4 test groups exist and are all active.
func adfWebCmdSeed(t *testing.T) {
	t.Helper()
	have := adfWebCmdActive(t)
	for _, name := range adfWebCmdGroups {
		if _, ok := have[name]; !ok {
			if err := adfWebCmdDB.InsertNewsgroup(&models.Newsgroup{Name: name, Active: true}); err != nil {
				t.Fatalf("InsertNewsgroup %s: %v", name, err)
			}
			continue
		}
		if err := adfWebCmdDB.UpdateNewsgroupActive(name, true); err != nil {
			t.Fatalf("UpdateNewsgroupActive %s: %v", name, err)
		}
	}
	active := adfWebCmdActive(t)
	for _, name := range adfWebCmdGroups {
		if !active[name] {
			t.Fatalf("seed: %s is not active", name)
		}
	}
}

// adfWebCmdWriteFile writes content to a new file below t.TempDir().
func adfWebCmdWriteFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "active")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// adfWebCmdWantActive checks the active flag of each of the 4 test groups.
func adfWebCmdWantActive(t *testing.T, want map[string]bool) {
	t.Helper()
	got := adfWebCmdActive(t)
	for _, name := range adfWebCmdGroups {
		if got[name] != want[name] {
			t.Errorf("%s: active=%t, want %t", name, got[name], want[name])
		}
	}
}

var adfWebCmdAllActive = map[string]bool{
	"adf.webcmd.a": true, "adf.webcmd.b": true, "adf.webcmd.c": true, "adf.webcmd.d": true,
}

func TestAdfWebCmdDisableActiveEmptyFile(t *testing.T) {
	adfWebCmdSeed(t)
	path := adfWebCmdWriteFile(t, "# newsgroups header only\n\n   \n# another comment\n")
	err := disableNewsgroupsNotInActiveFile(adfWebCmdDB, path, disableActiveOpts{})
	if err == nil {
		t.Fatal("a file without group names was accepted, want an error")
	}
	adfWebCmdWantActive(t, adfWebCmdAllActive)
}

// adfWebCmdOneOfFour is an active file (4-field format) that lists 1 of the 4 groups.
const adfWebCmdOneOfFour = "# active file\nadf.webcmd.b 0000000010 0000000001 y\n"

func TestAdfWebCmdDisableActiveOneOfFour(t *testing.T) {
	adfWebCmdSeed(t)
	path := adfWebCmdWriteFile(t, adfWebCmdOneOfFour)
	if err := disableNewsgroupsNotInActiveFile(adfWebCmdDB, path, disableActiveOpts{}); err != nil {
		t.Fatalf("disableNewsgroupsNotInActiveFile: %v", err)
	}
	adfWebCmdWantActive(t, map[string]bool{"adf.webcmd.b": true})
}

func TestAdfWebCmdDisableActiveDryRun(t *testing.T) {
	adfWebCmdSeed(t)
	path := adfWebCmdWriteFile(t, adfWebCmdOneOfFour)
	if err := disableNewsgroupsNotInActiveFile(adfWebCmdDB, path, disableActiveOpts{DryRun: true}); err != nil {
		t.Fatalf("disableNewsgroupsNotInActiveFile dry-run: %v", err)
	}
	adfWebCmdWantActive(t, adfWebCmdAllActive)
}

func TestAdfWebCmdPlainList(t *testing.T) {
	adfWebCmdSeed(t)
	path := adfWebCmdWriteFile(t, "adf.webcmd.a\n\n# keep c too\n  adf.webcmd.c  \n")
	if err := disableNewsgroupsNotInActiveFile(adfWebCmdDB, path, disableActiveOpts{}); err != nil {
		t.Fatalf("disableNewsgroupsNotInActiveFile: %v", err)
	}
	adfWebCmdWantActive(t, map[string]bool{"adf.webcmd.a": true, "adf.webcmd.c": true})
}

// TestAdfWebCmdShortHashLen: a first value outside 2..7 is stored as 7 instead of
// aborting (OPS-4b); afterwards the locked stored value wins over the CLI value.
func TestAdfWebCmdShortHashLen(t *testing.T) {
	got, err := resolveUseShortHashLen(adfWebCmdDB, 9)
	if err != nil {
		t.Fatalf("resolveUseShortHashLen(9): %v", err)
	}
	if got != 7 {
		t.Fatalf("resolveUseShortHashLen(9) = %d, want 7", got)
	}
	stored, locked, err := adfWebCmdDB.GetHistoryUseShortHashLen(0)
	if err != nil {
		t.Fatalf("GetHistoryUseShortHashLen: %v", err)
	}
	if stored != 7 || !locked {
		t.Fatalf("stored=%d locked=%t, want 7 true", stored, locked)
	}
	got, err = resolveUseShortHashLen(adfWebCmdDB, 3)
	if err != nil {
		t.Fatalf("resolveUseShortHashLen(3): %v", err)
	}
	if got != 7 {
		t.Fatalf("resolveUseShortHashLen(3) with locked 7 = %d, want 7", got)
	}
}

// TestAdfWebCmdBackgroundLoopsStop: startHierarchyUpdater and monitorUpdateFile
// return once their stop channel is closed (OPS-1c).
func TestAdfWebCmdBackgroundLoopsStop(t *testing.T) {
	stop := make(chan struct{})
	hierDone := make(chan struct{})
	monDone := make(chan struct{})
	go func() {
		defer close(hierDone)
		startHierarchyUpdater(adfWebCmdDB, stop)
	}()
	go func() {
		defer close(monDone)
		monitorUpdateFile(make(chan bool, 1), stop)
	}()
	close(stop)
	deadline := time.After(10 * time.Second)
	for name, done := range map[string]chan struct{}{"startHierarchyUpdater": hierDone, "monitorUpdateFile": monDone} {
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("%s did not return within 10s after stop was closed", name)
		}
	}
}
