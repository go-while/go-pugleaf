package main

// Tests of the audit-delta-fixes slice adf-opt-web-maint (A-ops-11, A-ops-12): the
// one-shot maintenance predicate and dispatch (no hostname, processor or progress DB),
// the -force requirement of -rsync-remove-source, and the per-group WAL checkpoint
// before a copy (busy group refused). They use the shared scratch database adfWebCmdDB
// of adf_web_test.go (TestMain there) with their own adf.optweb.* groups.

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// adfOptWebSkipDB skips a database test where OpenDatabase refuses to run.
func adfOptWebSkipDB(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		t.Skip("OpenDatabase refuses to run with euid < 1000")
	}
	if adfWebCmdDB == nil {
		t.Skip("no shared test database")
	}
}

// adfOptWebResetFlags clears every flag variable maintenanceRequested and
// runMaintenance read and restores their values when the test ends.
func adfOptWebResetFlags(t *testing.T) {
	t.Helper()
	saved := struct {
		hideFuture, findOrphan, repair, writeOnly, rsyncRemove, rsyncF, dryRun, createMissing, reloadDesc bool
		nga                                                                                               int
		writeActive, rsyncDir, compare, disable, expiry, importActive, importDesc                         string
		compareMin                                                                                        int64
	}{
		updateNewsgroupsHideFuture, findOrphanDBs, repairWatermarks, writeActiveOnly, rsyncRemoveSource, rsyncForce, disableActiveDryRun, importCreateMissing, forceReloadDesc,
		updateNGAConcurrent,
		writeActiveFile, rsyncInactiveGroups, compareActiveFile, disableActiveFile, updateNewsgroupsExpiryFile, importActiveFile, importDescFile,
		compareActiveMinArticles,
	}
	t.Cleanup(func() {
		updateNewsgroupsHideFuture, findOrphanDBs, repairWatermarks = saved.hideFuture, saved.findOrphan, saved.repair
		writeActiveOnly, rsyncRemoveSource, rsyncForce = saved.writeOnly, saved.rsyncRemove, saved.rsyncF
		disableActiveDryRun, importCreateMissing, forceReloadDesc = saved.dryRun, saved.createMissing, saved.reloadDesc
		updateNGAConcurrent = saved.nga
		writeActiveFile, rsyncInactiveGroups, compareActiveFile = saved.writeActive, saved.rsyncDir, saved.compare
		disableActiveFile, updateNewsgroupsExpiryFile = saved.disable, saved.expiry
		importActiveFile, importDescFile = saved.importActive, saved.importDesc
		compareActiveMinArticles = saved.compareMin
	})
	updateNewsgroupsHideFuture, findOrphanDBs, repairWatermarks = false, false, false
	writeActiveOnly, rsyncRemoveSource, rsyncForce = true, false, false
	disableActiveDryRun, importCreateMissing, forceReloadDesc = false, false, false
	updateNGAConcurrent = 0
	writeActiveFile, rsyncInactiveGroups, compareActiveFile = "", "", ""
	disableActiveFile, updateNewsgroupsExpiryFile = "", ""
	importActiveFile, importDescFile = "", ""
	compareActiveMinArticles = 0
}

func TestAdfOptWebMaintenanceRequested(t *testing.T) {
	cases := []struct {
		name string
		set  func()
		want bool
	}{
		{"server (no mode)", func() {}, false},
		{"descriptions-only import", func() { importDescFile = "x"; importCreateMissing = true }, false},
		{"overwrite descriptions", func() { forceReloadDesc = true }, false},
		{"dry-run alone", func() { disableActiveDryRun = true }, false},
		{"remove-source alone", func() { rsyncRemoveSource = true; rsyncForce = true }, false},
		{"hide future posts", func() { updateNewsgroupsHideFuture = true }, true},
		{"newsgroup activity", func() { updateNGAConcurrent = 4 }, true},
		{"write active", func() { writeActiveFile = "x" }, true},
		{"rsync", func() { rsyncInactiveGroups = "x" }, true},
		{"compare active", func() { compareActiveFile = "x" }, true},
		{"disable active", func() { disableActiveFile = "x" }, true},
		{"find orphan dbs", func() { findOrphanDBs = true }, true},
		{"expiry file", func() { updateNewsgroupsExpiryFile = "x" }, true},
		{"import active", func() { importActiveFile = "x" }, true},
		{"import active and descriptions", func() { importActiveFile = "x"; importDescFile = "y" }, true},
		{"repair watermarks", func() { repairWatermarks = true }, true},
		{"descriptions and repair", func() { importDescFile = "y"; repairWatermarks = true }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adfOptWebResetFlags(t)
			tc.set()
			if got := maintenanceRequested(); got != tc.want {
				t.Fatalf("maintenanceRequested() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAdfOptWebRsyncFlags(t *testing.T) {
	cases := []struct {
		dir           string
		remove, force bool
		wantErr       bool
	}{
		{"", false, false, false},
		{"", true, false, false}, // no rsync requested: -rsync-remove-source alone does nothing
		{"/new", false, false, false},
		{"/new", true, false, true},
		{"/new", true, true, false},
		{"/new", false, true, false},
	}
	for _, tc := range cases {
		err := rsyncFlagsError(tc.dir, tc.remove, tc.force)
		if (err != nil) != tc.wantErr {
			t.Errorf("rsyncFlagsError(%q, remove=%t, force=%t) = %v, want error %t", tc.dir, tc.remove, tc.force, err, tc.wantErr)
		}
	}
}

// TestAdfOptWebRunMaintenanceWriteActive: a one-shot mode runs on the database alone,
// with no NNTP hostname set (no processor.SetHostname, no DNS), and returns its exit code.
func TestAdfOptWebRunMaintenanceWriteActive(t *testing.T) {
	adfOptWebSkipDB(t)
	if processor.LocalNNTPHostname != "" {
		t.Fatalf("processor.LocalNNTPHostname = %q, want empty for this test", processor.LocalNNTPHostname)
	}
	adfOptWebResetFlags(t)
	const group = "adf.optweb.writeactive"
	adfOptWebGroup(t, group, true)

	writeActiveFile = filepath.Join(t.TempDir(), "active")
	if code := runMaintenance(adfWebCmdDB); code != 0 {
		t.Fatalf("runMaintenance(-write-active-file) = %d, want 0", code)
	}
	data, err := os.ReadFile(writeActiveFile)
	if err != nil {
		t.Fatalf("read active file: %v", err)
	}
	if !strings.Contains(string(data), group+" ") {
		t.Fatalf("active file does not list %s:\n%s", group, data)
	}

	writeActiveFile = filepath.Join(t.TempDir(), "missing-dir", "active")
	if code := runMaintenance(adfWebCmdDB); code != 1 {
		t.Fatalf("runMaintenance(-write-active-file into a missing dir) = %d, want 1", code)
	}
}

// TestAdfOptWebRunMaintenanceRsyncNeedsForce: -rsync-remove-source without -force
// fails before anything is copied.
func TestAdfOptWebRunMaintenanceRsyncNeedsForce(t *testing.T) {
	adfOptWebSkipDB(t)
	adfOptWebResetFlags(t)
	target := filepath.Join(t.TempDir(), "new")
	rsyncInactiveGroups = target
	rsyncRemoveSource = true
	if code := runMaintenance(adfWebCmdDB); code != 1 {
		t.Fatalf("runMaintenance(-rsync-remove-source without -force) = %d, want 1", code)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target %s was created (err=%v), want nothing done", target, err)
	}
}

// adfOptWebGroup makes sure group exists in the main DB with the given active flag.
func adfOptWebGroup(t *testing.T, group string, active bool) {
	t.Helper()
	if _, err := adfWebCmdDB.MainDBGetNewsgroup(group); err != nil {
		if err := adfWebCmdDB.InsertNewsgroup(&models.Newsgroup{Name: group, Active: active}); err != nil {
			t.Fatalf("InsertNewsgroup %s: %v", group, err)
		}
	}
	if err := adfWebCmdDB.UpdateNewsgroupActive(group, active); err != nil {
		t.Fatalf("UpdateNewsgroupActive %s: %v", group, err)
	}
}

// adfOptWebGroupFile creates the group database (if needed), closes it again and
// returns the path of its .db file.
func adfOptWebGroupFile(t *testing.T, group string) string {
	t.Helper()
	g, err := adfWebCmdDB.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	if err := adfWebCmdDB.ForceCloseGroupDB(g); err != nil {
		t.Fatalf("ForceCloseGroupDB %s: %v", group, err)
	}
	return filepath.Join(adfWebCmdDB.GetDataDir(), "db", database.MD5Hash(group), database.SanitizeGroupName(group)+".db")
}

// TestAdfOptWebCheckpointBusy: the checkpoint before a copy succeeds on an idle group
// and empties the WAL; a read transaction on another connection (as another process
// would hold) makes it report busy, and the group is refused.
func TestAdfOptWebCheckpointBusy(t *testing.T) {
	adfOptWebSkipDB(t)
	const group = "adf.optweb.busy"
	file := adfOptWebGroupFile(t, group)

	if err := checkpointGroupForCopy(adfWebCmdDB, group, file, 0); err != nil {
		t.Fatalf("checkpoint of an idle group: %v", err)
	}
	if st, err := os.Stat(file + "-wal"); err == nil && st.Size() != 0 {
		t.Fatalf("WAL has %d bytes after checkpoint and close, want 0 or no file", st.Size())
	}

	// a second connection, as another process would open it: write (WAL frames), then
	// keep a read transaction open on that snapshot
	other, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer other.Close()
	other.SetMaxOpenConns(2)
	for _, q := range []string{
		"PRAGMA journal_mode = WAL",
		"CREATE TABLE IF NOT EXISTS adf_opt_busy (x INTEGER)",
		"INSERT INTO adf_opt_busy (x) VALUES (1)",
	} {
		if _, err := other.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	tx, err := other.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM adf_opt_busy").Scan(&n); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read: %v", err)
	}

	start := time.Now()
	err = checkpointGroupForCopy(adfWebCmdDB, group, file, 200*time.Millisecond)
	if !errors.Is(err, errGroupDBBusy) {
		_ = tx.Rollback()
		t.Fatalf("checkpoint with an open reader elsewhere = %v, want errGroupDBBusy", err)
	}
	if !strings.Contains(err.Error(), "wal_checkpoint(TRUNCATE) blocked") {
		t.Errorf("refusal %q does not come from the busy checkpoint", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("busy checkpoint took %v, want about the 200ms busy wait", d)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("close other: %v", err)
	}
	if err := checkpointGroupForCopy(adfWebCmdDB, group, file, 200*time.Millisecond); err != nil {
		t.Fatalf("checkpoint after the reader ended: %v", err)
	}
}

// TestAdfOptWebRsyncInactive copies, then moves (with -force) the database of an
// inactive group with the real rsync (skipped when it is not installed).
func TestAdfOptWebRsyncInactive(t *testing.T) {
	adfOptWebSkipDB(t)
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	const group = "adf.optweb.inactive"
	adfOptWebGroup(t, group, false)
	file := adfOptWebGroupFile(t, group)
	hash := database.MD5Hash(group)
	name := database.SanitizeGroupName(group) + ".db"

	copyDir := filepath.Join(t.TempDir(), "copy")
	if err := rsyncInactiveGroupsToDir(adfWebCmdDB, copyDir, false, false); err != nil {
		t.Fatalf("rsync copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(copyDir, "db", hash, name)); err != nil {
		t.Fatalf("copied database missing: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("source database gone after a copy without -rsync-remove-source: %v", err)
	}

	moveDir := filepath.Join(t.TempDir(), "move")
	if err := rsyncInactiveGroupsToDir(adfWebCmdDB, moveDir, true, false); err == nil {
		t.Fatal("-rsync-remove-source without -force was accepted")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("source database gone after the refused move: %v", err)
	}
	if err := rsyncInactiveGroupsToDir(adfWebCmdDB, moveDir, true, true); err != nil {
		t.Fatalf("rsync move with -force: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moveDir, "db", hash, name)); err != nil {
		t.Fatalf("moved database missing: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("source database still present after the move (err=%v)", err)
	}
}

// TestAdfOptWebCheckpointOpenElsewhere: an idle handle on the group database in
// another connection (as an idle web or nntp-server would hold) does not block the
// checkpoint, but the WAL survives the close, so the group is refused; once that
// handle is closed the group is accepted.
func TestAdfOptWebCheckpointOpenElsewhere(t *testing.T) {
	adfOptWebSkipDB(t)
	const group = "adf.optweb.openelsewhere"
	file := adfOptWebGroupFile(t, group)

	other, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer other.Close()
	var n int
	if err := other.QueryRow("SELECT COUNT(*) FROM sqlite_master").Scan(&n); err != nil {
		t.Fatalf("read through the second handle: %v", err)
	}

	err = checkpointGroupForCopy(adfWebCmdDB, group, file, 200*time.Millisecond)
	if !errors.Is(err, errGroupDBBusy) {
		t.Fatalf("checkpoint with an idle handle elsewhere = %v, want errGroupDBBusy", err)
	}
	if !strings.Contains(err.Error(), "-wal still exists") {
		t.Errorf("refusal %q does not come from the leftover WAL", err)
	}

	if err := other.Close(); err != nil {
		t.Fatalf("close other: %v", err)
	}
	if err := checkpointGroupForCopy(adfWebCmdDB, group, file, 200*time.Millisecond); err != nil {
		t.Fatalf("checkpoint after the second handle was closed: %v", err)
	}
}
