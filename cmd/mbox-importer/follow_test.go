package main

// Tests of wave 2 of plan mbox-import-tracking: the inputs of a run come from iafetch's
// download database (-from-iafetch), -verify-md5 checks the recorded md5 and -follow scans
// that database again and again. The fixture database is built with iafetch's own DDL
// (iaSchema and the iaNewDB/iaItem/iaFileRow helpers of iasource_test.go).

import (
	"bytes"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fwMirror is the folder of the mirror inside the test's base directory, so the recorded
// paths are relative ones that need -from-iafetch-base, as iafetch writes them when it runs
// with a relative -o.
const fwMirror = "mirror"

// fwSum is the hex md5 of content, the sum iafetch records for a file it verified.
func fwSum(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

// fwSetup creates an empty iafetch database in a fresh base directory and returns the base,
// the database path and the open writer (iafetch keeps its database open while it downloads).
func fwSetup(t *testing.T) (base, dbPath string, db *sql.DB) {
	t.Helper()
	base = t.TempDir()
	dbPath = filepath.Join(base, "iafetch.sqlite")
	return base, dbPath, iaNewDB(t, dbPath)
}

// fwPlan records a file in iafetch's database without writing it to disk: the importer finds
// it missing (pending) until fwWrite puts it there. It returns the path the importer will use.
func fwPlan(t *testing.T, db *sql.DB, base, identifier, name, content string) string {
	t.Helper()
	rel := filepath.Join(fwMirror, identifier, name)
	iaFileRow(t, db, identifier, name, int64(len(content)), fwSum(content), rel)
	return filepath.Join(base, rel)
}

// fwWrite writes content to path, creating its directories.
func fwWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fwRecord writes one file of the mirror and records it in iafetch's database as iafetch
// does: a relative path, the real size and the real md5.
func fwRecord(t *testing.T, db *sql.DB, base, identifier, name, content string) string {
	t.Helper()
	path := fwPlan(t, db, base, identifier, name, content)
	fwWrite(t, path, content)
	return path
}

// fwTrackDB opens the tracking database of a run under data for the test's own reads.
func fwTrackDB(t *testing.T, data string) *sql.DB {
	t.Helper()
	return mtRaw(t, filepath.Join(data, "cfg"))
}

// fwWantStatus checks the status and the reason of an input row.
func fwWantStatus(t *testing.T, db *sql.DB, path, status, reason string) {
	t.Helper()
	gotStatus, gotReason := mtStatusOf(t, db, path)
	if gotStatus != status || gotReason != reason {
		t.Errorf("%s: status=%q reason=%q, want %q %q", path, gotStatus, gotReason, status, reason)
	}
}

// fwWantCounts checks the input counts of a run summary.
func fwWantCounts(t *testing.T, sum *runSummary, imported, already, pending, unsupported int) {
	t.Helper()
	if sum == nil {
		t.Fatal("no run summary")
	}
	if sum.Inputs != imported || sum.AlreadyImported != already || sum.Pending != pending || sum.Unsupported != unsupported {
		t.Errorf("summary imported=%d already=%d pending=%d unsupported=%d, want %d %d %d %d",
			sum.Inputs, sum.AlreadyImported, sum.Pending, sum.Unsupported,
			imported, already, pending, unsupported)
	}
}

// TestMboxFollowIAFetchRun: a -from-iafetch run imports the files the importer can read,
// records the .tar and the OCR .txt as unsupported and a recorded file that is not on disk
// as pending; the second run imports nothing; once the missing file arrives the next run
// imports it (its pending row is claimed like any other).
func TestMboxFollowIAFetchRun(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	mtQuiet(t)
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-one", 1, "done")
	iaItem(t, db, "usenet-two", 2, "incomplete")

	alpha := fwRecord(t, db, base, "usenet-one", "alpha.mbox", miArticle(miID1, miAlpha, "a"))
	beta := fwRecord(t, db, base, "usenet-two", "beta.mbox.gz",
		string(gzipBytes(t, miArticle(miID2, miBeta, "b"))))
	tar := fwRecord(t, db, base, "usenet-one", "spool.tar", "a tar of a spool, not an mbox")
	txt := fwRecord(t, db, base, "usenet-one", "usenet-one_djvu.txt", "OCR derivative")
	// recorded by iafetch, not on disk yet (iafetch is still writing, or it was moved away)
	gammaBody := miArticle(miID3, miAlpha, "c")
	gamma := fwPlan(t, db, base, "usenet-two", "gamma.mbox", gammaBody)

	code, sum := miRun(t, "-data", data, "-from-iafetch", dbPath, "-from-iafetch-base", base)
	if code != 0 {
		t.Fatalf("first run exit %d", code)
	}
	fwWantCounts(t, sum, 2, 0, 1, 2)
	if sum.Scans != 1 {
		t.Errorf("scans = %d, want 1", sum.Scans)
	}
	miCheck(t, sum, 2, 2, 0, 0, 0, "-")
	if sum.MarkedDone != 0 || miExists(alpha+".done") || miExists(beta+".done") {
		t.Errorf("the mirror was renamed (marked-done=%d)", sum.MarkedDone)
	}

	track := fwTrackDB(t, data)
	fwWantStatus(t, track, alpha, trackDone, "")
	fwWantStatus(t, track, beta, trackDone, "")
	fwWantStatus(t, track, tar, trackUnsupported, "tar")
	fwWantStatus(t, track, txt, trackUnsupported, "txt")
	fwWantStatus(t, track, gamma, trackPending, iaReasonMissing)
	var (
		source, name, sum5 string
	)
	if err := track.QueryRow(`SELECT source, name, md5 FROM inputs WHERE path = ?`, beta).
		Scan(&source, &name, &sum5); err != nil {
		t.Fatal(err)
	}
	if source != sourceIAFetch || name != "beta.mbox.gz" || sum5 == "" {
		t.Errorf("beta row source=%q name=%q md5=%q", source, name, sum5)
	}
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1}, miBeta: {miID2}},
		[]string{miID1, miID2, miID3})

	// the second run imports nothing: both files are done, the rest is unchanged
	code, sum = miRun(t, "-data", data, "-from-iafetch", dbPath, "-from-iafetch-base", base)
	if code != 0 {
		t.Fatalf("second run exit %d", code)
	}
	fwWantCounts(t, sum, 0, 2, 1, 2)
	if sum.Total.Read != 0 {
		t.Errorf("second run read %d articles", sum.Total.Read)
	}

	// the missing file arrives: the next scan imports it
	fwWrite(t, gamma, gammaBody)
	code, sum = miRun(t, "-data", data, "-from-iafetch", dbPath, "-from-iafetch-base", base)
	if code != 0 {
		t.Fatalf("third run exit %d", code)
	}
	fwWantCounts(t, sum, 1, 2, 0, 2)
	miCheck(t, sum, 1, 1, 0, 0, 0, "-")
	fwWantStatus(t, track, gamma, trackDone, "")
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1, miID3}}, []string{miID1, miID3})
}

// TestMboxFollowVerifyMD5: with -verify-md5 a file whose md5 is not the recorded one is
// recorded as pending with the reason md5-mismatch and is not imported (and it is no input
// error: the run exits 0); without the flag the same file is imported.
func TestMboxFollowVerifyMD5(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	mtQuiet(t)
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-md5", 1, "done")

	good := fwRecord(t, db, base, "usenet-md5", "good.mbox", miArticle(miID1, miAlpha, "a"))
	// a row whose md5 does not belong to the file on disk (the size does match)
	badBody := miArticle(miID2, miBeta, "b")
	bad := filepath.Join(base, fwMirror, "usenet-md5", "bad.mbox")
	iaFileRow(t, db, "usenet-md5", "bad.mbox", int64(len(badBody)), strings.Repeat("0", 32),
		filepath.Join(fwMirror, "usenet-md5", "bad.mbox"))
	fwWrite(t, bad, badBody)

	code, sum := miRun(t, "-data", data, "-from-iafetch", dbPath, "-from-iafetch-base", base, "-verify-md5")
	if code != 0 {
		t.Fatalf("exit %d, want 0 (a mismatch is no input error)", code)
	}
	fwWantCounts(t, sum, 1, 0, 1, 0)
	track := fwTrackDB(t, data)
	fwWantStatus(t, track, good, trackDone, "")
	fwWantStatus(t, track, bad, trackPending, reasonMD5Mismatch)
	// the sum that was read is remembered, so the next scan does not read the file again,
	// and the recorded md5, size and mtime of the row are the ones the claim wrote
	var md5Seen, md5Recorded string
	var size, mtime int64
	if err := track.QueryRow(`SELECT COALESCE(md5_seen, ''), COALESCE(md5, ''), size, mtime FROM inputs WHERE path = ?`,
		bad).Scan(&md5Seen, &md5Recorded, &size, &mtime); err != nil {
		t.Fatal(err)
	}
	if md5Seen != fwSum(badBody) || md5Recorded != strings.Repeat("0", 32) ||
		size != int64(len(badBody)) || mtime == 0 {
		t.Errorf("bad row md5_seen=%q md5=%q size=%d mtime=%d", md5Seen, md5Recorded, size, mtime)
	}
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1}}, []string{miID1, miID2}, miBeta)

	// without -verify-md5 the recorded md5 is not checked and the file is imported
	code, sum = miRun(t, "-data", data, "-from-iafetch", dbPath, "-from-iafetch-base", base)
	if code != 0 {
		t.Fatalf("second run exit %d", code)
	}
	fwWantCounts(t, sum, 1, 1, 0, 0)
	fwWantStatus(t, track, bad, trackDone, "")
	miCheckGroups(t, data, map[string][]string{miBeta: {miID2}}, []string{miID2})
}

// TestMboxFollowFileMD5 pins the hash helper: the sum of a file, and errStopped when the
// stop channel is closed (the check between two chunks, so a 5 GB file does not hold up the
// shutdown).
func TestMboxFollowFileMD5(t *testing.T) {
	dir := t.TempDir()
	// more than one chunk, so the stop check runs more than once
	path := miWrite(t, dir, "big.bin", strings.Repeat("abcd", md5ChunkSize/2))
	sum, err := fileMD5(path, nil)
	if err != nil {
		t.Fatalf("fileMD5: %v", err)
	}
	if want := iaSum(t, path); sum != want {
		t.Errorf("fileMD5 = %q, want %q", sum, want)
	}
	stop := make(chan struct{})
	close(stop)
	if _, err := fileMD5(path, stop); !errors.Is(err, errStopped) {
		t.Errorf("fileMD5 with a closed stop channel: %v, want %v", err, errStopped)
	}
	if _, err := fileMD5(filepath.Join(dir, "gone.bin"), nil); err == nil {
		t.Error("fileMD5 of a missing file returned no error")
	}
}

// fwInjectWait replaces the wait of -follow with a channel the test drives: it reports how
// often it has been entered (so the test knows a scan is over) and then waits for a tick of
// the test or for the stop channel. Nothing in these tests waits for a clock.
func fwInjectWait(t *testing.T) (ticks chan struct{}, entered chan int) {
	t.Helper()
	ticks = make(chan struct{})
	entered = make(chan int, 8)
	waits := 0 // only the run's own goroutine touches it
	old := followWait
	followWait = func(_ time.Duration, stop <-chan struct{}) bool {
		waits++
		entered <- waits
		select {
		case <-ticks:
			return true
		case <-stop:
			return false
		}
	}
	t.Cleanup(func() { followWait = old })
	return ticks, entered
}

// fwHashHook replaces the hash function of -verify-md5 for the test.
func fwHashHook(t *testing.T, fn func(path string, stop <-chan struct{}) (string, error)) {
	t.Helper()
	old := verifyFileMD5
	verifyFileMD5 = fn
	t.Cleanup(func() { verifyFileMD5 = old })
}

// fwStart runs the importer in the background with the test's stop channel.
func fwStart(stop chan struct{}, args ...string) <-chan int {
	done := make(chan int, 1)
	go func() { done <- runWith(args, stop) }()
	return done
}

// fwAwait takes the next wait count the injected wait reports, or fails the test.
func fwAwait(t *testing.T, entered <-chan int, want int) {
	t.Helper()
	select {
	case got := <-entered:
		if got != want {
			t.Fatalf("the importer is waiting the %d. time, want the %d.", got, want)
		}
	case <-time.After(testWait):
		t.Fatalf("the importer did not reach wait %d within %v", want, testWait)
	}
}

// fwTick lets the waiting scan loop run its next scan.
func fwTick(t *testing.T, ticks chan struct{}) {
	t.Helper()
	select {
	case ticks <- struct{}{}:
	case <-time.After(testWait):
		t.Fatalf("the importer did not take the tick within %v", testWait)
	}
}

// fwWaitCode takes the exit code of the background run.
func fwWaitCode(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(testWait):
		t.Fatalf("runWith did not return within %v", testWait)
		return -1
	}
}

// TestMboxFollowScanLoop: -follow imports the inputs of a scan, waits, scans again and
// imports a files row that was inserted meanwhile; the stop signal while it waits ends the
// run with exit code 0. The wait is injected, so the test never waits for a clock.
func TestMboxFollowScanLoop(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	mtQuiet(t)
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-follow", 1, "incomplete")
	fwRecord(t, db, base, "usenet-follow", "alpha.mbox", miArticle(miID1, miAlpha, "a"))

	ticks, entered := fwInjectWait(t)
	stop := make(chan struct{})
	done := fwStart(stop, "-data", data, "-from-iafetch", dbPath,
		"-from-iafetch-base", base, "-follow", "1h")

	fwAwait(t, entered, 1) // the first scan is done and the run is waiting
	beta := fwRecord(t, db, base, "usenet-follow", "beta.mbox", miArticle(miID2, miBeta, "b"))
	fwTick(t, ticks)       // the run scans again
	fwAwait(t, entered, 2) // the second scan is done
	close(stop)            // stopped while waiting: no error

	code := fwWaitCode(t, done)
	if code != 0 {
		t.Fatalf("exit %d, want 0 (stopped while waiting)", code)
	}
	sum := lastRun
	if sum == nil || sum.Scans != 2 {
		t.Fatalf("summary %+v, want 2 scans", sum)
	}
	if sum.Stopped {
		t.Error("the run counts as stopped although the stop came while it was waiting")
	}
	fwWantCounts(t, sum, 2, 1, 0, 0) // alpha, then beta; alpha is already imported in scan 2
	miCheck(t, sum, 2, 2, 0, 0, 0, "-")
	track := fwTrackDB(t, data)
	fwWantStatus(t, track, beta, trackDone, "")
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1}, miBeta: {miID2}},
		[]string{miID1, miID2})
}

// TestMboxFollowStopInsideInput: a signal while an input is being imported ends the -follow
// loop at once (no further scan) and the run exits 130, not 0.
func TestMboxFollowStopInsideInput(t *testing.T) {
	mtQuiet(t)
	base, dbPath, db := fwSetup(t)
	iaItem(t, db, "usenet-stop", 1, "done")
	fwRecord(t, db, base, "usenet-stop", "alpha.mbox", miFixture())
	fwRecord(t, db, base, "usenet-stop", "beta.mbox", miFixture())
	planned, err := iafetchInputs(dbPath, base)
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}
	if len(planned) != 2 {
		t.Fatalf("%d planned inputs, want 2", len(planned))
	}

	followWaitCalled := false
	old := followWait
	followWait = func(time.Duration, <-chan struct{}) bool {
		followWaitCalled = true
		return false
	}
	t.Cleanup(func() { followWait = old })

	stop := make(chan struct{})
	sink := &stopSink{countingSink: newCountingSink(), stop: stop} // closes stop on its first article
	scan := func() ([]plannedInput, error) { return iafetchInputs(dbPath, base) }
	res := importScans(planned, scan, &runOptions{follow: time.Hour}, sink, stop, nil)

	if !res.Stopped || res.exitCode() != 130 {
		t.Errorf("stopped=%t exit=%d, want true 130", res.Stopped, res.exitCode())
	}
	if res.Scans != 1 {
		t.Errorf("scans = %d, want 1", res.Scans)
	}
	if followWaitCalled {
		t.Error("the scan loop waited for another scan although an input was stopped")
	}
}

// TestMboxFollowStopDuringScan: the signal arrives while the scan after the wait is running.
// No input of that scan is started (nothing was in progress), so the run ends with 0 and the
// new file stays for the next run instead of being counted as a stopped import.
func TestMboxFollowStopDuringScan(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	mtQuiet(t)
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-race", 1, "incomplete")
	fwRecord(t, db, base, "usenet-race", "alpha.mbox", miArticle(miID1, miAlpha, "a"))

	// a wait that does not look at the stop channel: the stop is closed before the tick, so
	// the run scans once more and has to notice it before it imports anything
	ticks := make(chan struct{})
	entered := make(chan int, 4)
	waits := 0
	old := followWait
	followWait = func(time.Duration, <-chan struct{}) bool {
		waits++
		entered <- waits
		<-ticks
		return true
	}
	t.Cleanup(func() { followWait = old })

	stop := make(chan struct{})
	done := fwStart(stop, "-data", data, "-from-iafetch", dbPath,
		"-from-iafetch-base", base, "-follow", "1h")
	fwAwait(t, entered, 1)
	beta := fwRecord(t, db, base, "usenet-race", "beta.mbox", miArticle(miID2, miBeta, "b"))
	close(stop)      // the signal comes before the scan
	fwTick(t, ticks) // the run scans and must stop instead of importing

	if code := fwWaitCode(t, done); code != 0 {
		t.Fatalf("exit %d, want 0 (nothing was in progress)", code)
	}
	sum := lastRun
	if sum == nil || sum.Scans != 1 || sum.Stopped {
		t.Fatalf("summary %+v, want 1 scan and not stopped", sum)
	}
	// beta was never claimed: no row, or a row that is not done
	var status string
	err := fwTrackDB(t, data).QueryRow(`SELECT status FROM inputs WHERE path = ?`, beta).Scan(&status)
	if err == nil && status != trackPending {
		t.Errorf("beta row status = %q, want none or %q", status, trackPending)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("beta row: %v", err)
	}
}

// TestMboxFollowEmptyDatabase: -follow against an iafetch database without a files row yet
// still opens the tracking database and imports what shows up in a later scan.
func TestMboxFollowEmptyDatabase(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	mtQuiet(t)
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-late", 1, "incomplete") // the item is known, no file is done yet

	ticks, entered := fwInjectWait(t)
	stop := make(chan struct{})
	done := fwStart(stop, "-data", data, "-from-iafetch", dbPath,
		"-from-iafetch-base", base, "-follow", "1h")

	fwAwait(t, entered, 1) // the first scan found nothing
	trackPath := filepath.Join(data, "cfg", trackDBName)
	if !miExists(trackPath) {
		t.Errorf("no tracking database at %s although -follow keeps the run going", trackPath)
	}
	late := fwRecord(t, db, base, "usenet-late", "alpha.mbox", miArticle(miID1, miAlpha, "a"))
	fwTick(t, ticks)
	fwAwait(t, entered, 2)
	close(stop)

	if code := fwWaitCode(t, done); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	sum := lastRun
	if sum == nil || sum.Scans != 2 {
		t.Fatalf("summary %+v, want 2 scans", sum)
	}
	fwWantCounts(t, sum, 1, 0, 0, 0)
	fwWantStatus(t, fwTrackDB(t, data), late, trackDone, "")
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1}}, []string{miID1})
}

// TestMboxFollowScanFails: a later scan that fails (iafetch's database is not readable for a
// moment) is logged and tried again at the next tick; it is no scan of its own and ends
// nothing.
func TestMboxFollowScanFails(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-gone", 1, "incomplete")
	fwRecord(t, db, base, "usenet-gone", "alpha.mbox", miArticle(miID1, miAlpha, "a"))

	ticks, entered := fwInjectWait(t)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	stop := make(chan struct{})
	done := fwStart(stop, "-data", data, "-from-iafetch", dbPath,
		"-from-iafetch-base", base, "-follow", "1h")

	fwAwait(t, entered, 1)
	// the database is moved away: the next scan cannot read it
	away := dbPath + ".away"
	if err := os.Rename(dbPath, away); err != nil {
		t.Fatalf("rename %s: %v", dbPath, err)
	}
	fwTick(t, ticks)
	fwAwait(t, entered, 2) // the failed scan led straight back to the wait
	if err := os.Rename(away, dbPath); err != nil {
		t.Fatalf("rename %s back: %v", away, err)
	}
	beta := fwRecord(t, db, base, "usenet-gone", "beta.mbox", miArticle(miID2, miBeta, "b"))
	fwTick(t, ticks)
	fwAwait(t, entered, 3) // the scan after the failure worked
	close(stop)

	code := fwWaitCode(t, done)
	log.SetOutput(os.Stderr) // read the buffer only after the run stopped writing to it
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	sum := lastRun
	if sum == nil || sum.Scans != 2 {
		t.Fatalf("summary %+v, want 2 scans (the failed scan is none)", sum)
	}
	fwWantCounts(t, sum, 2, 1, 0, 0) // alpha in scan 1, beta in scan 2, alpha already imported
	fwWantStatus(t, fwTrackDB(t, data), beta, trackDone, "")
	out := buf.String()
	if !strings.Contains(out, "[MBOX-IMPORT] -follow: scan failed after 1 scan(s)") {
		t.Errorf("log lacks the failed scan\n%s", out)
	}
	if strings.Contains(out, "scan 3:") {
		t.Errorf("the failed scan got a scan number of its own\n%s", out)
	}
}

// TestMboxFollowStopWhileHashing: the signal during the md5 check of -verify-md5 leaves the
// input "stopped" in the tracking database and is not a stopped import (nothing of the input
// was read, so the run exits 0).
func TestMboxFollowStopWhileHashing(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	base, dbPath, db := fwSetup(t)
	iaItem(t, db, "usenet-hash", 1, "done")
	path := fwRecord(t, db, base, "usenet-hash", "alpha.mbox", miArticle(miID1, miAlpha, "a"))
	planned, err := iafetchInputs(dbPath, base)
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}

	stop := make(chan struct{})
	fwHashHook(t, func(string, <-chan struct{}) (string, error) {
		close(stop) // the signal arrives while the file is being hashed
		return "", errStopped
	})
	tr := mtOpen(t, dir, false, false)
	res := importPlanned(planned, &runOptions{tracked: true, verifyMD5: true}, newCountingSink(), stop, tr)
	tr.close(res, res.exitCode())

	if res.Stopped || res.exitCode() != 0 {
		t.Errorf("stopped=%t exit=%d, want false 0", res.Stopped, res.exitCode())
	}
	if res.HashStopped != 1 || res.Inputs != 0 {
		t.Errorf("md5-stopped=%d inputs=%d, want 1 0", res.HashStopped, res.Inputs)
	}
	fwWantStatus(t, mtRaw(t, dir), path, trackStopped, "shutdown signal")
}

// TestMboxFollowKnownMismatch: a file whose md5 did not match is not hashed again while it
// is unchanged (its sum is remembered in md5_seen); a changed mtime makes it hash again.
func TestMboxFollowKnownMismatch(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	base, dbPath, db := fwSetup(t)
	iaItem(t, db, "usenet-bad", 1, "done")
	body := miArticle(miID1, miAlpha, "a")
	path := filepath.Join(base, fwMirror, "usenet-bad", "bad.mbox")
	recorded := strings.Repeat("0", 32) // not the md5 of the file on disk
	iaFileRow(t, db, "usenet-bad", "bad.mbox", int64(len(body)), recorded,
		filepath.Join(fwMirror, "usenet-bad", "bad.mbox"))
	fwWrite(t, path, body)

	hashes := 0 // written by the import worker, read between the runs (after its wait group)
	seen := strings.Repeat("a", 32)
	fwHashHook(t, func(string, <-chan struct{}) (string, error) {
		hashes++
		return seen, nil
	})
	tr := mtOpen(t, dir, false, false)
	opts := &runOptions{tracked: true, verifyMD5: true}
	planned, err := iafetchInputs(dbPath, base)
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}

	res := importPlanned(planned, opts, newCountingSink(), nil, tr)
	if res.Pending != 1 || res.Inputs != 0 || hashes != 1 {
		t.Fatalf("first scan: pending=%d inputs=%d hashes=%d, want 1 0 1", res.Pending, res.Inputs, hashes)
	}
	raw := mtRaw(t, dir)
	fwWantStatus(t, raw, path, trackPending, reasonMD5Mismatch)
	var md5Seen string
	if err := raw.QueryRow(`SELECT COALESCE(md5_seen, '') FROM inputs WHERE path = ?`, path).Scan(&md5Seen); err != nil {
		t.Fatal(err)
	}
	if md5Seen != seen {
		t.Errorf("md5_seen = %q, want %q", md5Seen, seen)
	}

	// the same file again: the sum is known, so it is not read a second time
	res = importPlanned(planned, opts, newCountingSink(), nil, tr)
	if res.Pending != 1 || hashes != 1 {
		t.Errorf("second scan: pending=%d hashes=%d, want 1 1", res.Pending, hashes)
	}
	fwWantStatus(t, raw, path, trackPending, reasonMD5Mismatch)

	// the file changed (another mtime): it is hashed again
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	changed, err := iafetchInputs(dbPath, base)
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}
	res = importPlanned(changed, opts, newCountingSink(), nil, tr)
	if res.Pending != 1 || hashes != 2 {
		t.Errorf("third scan: pending=%d hashes=%d, want 1 2", res.Pending, hashes)
	}
	tr.close(res, 0)
}

// TestMboxFollowClaimFirst: -verify-md5 hashes only an input that is really going to be
// imported, so an input the tracking database knows as done is not read again.
func TestMboxFollowClaimFirst(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	base, dbPath, db := fwSetup(t)
	iaItem(t, db, "usenet-done", 1, "done")
	fwRecord(t, db, base, "usenet-done", "alpha.mbox", miArticle(miID1, miAlpha, "a"))
	planned, err := iafetchInputs(dbPath, base)
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}

	tr := mtOpen(t, dir, false, false)
	res := importPlanned(planned, &runOptions{tracked: true}, newCountingSink(), nil, tr)
	if res.Inputs != 1 {
		t.Fatalf("first run: inputs=%d, want 1", res.Inputs)
	}
	hashes := 0
	fwHashHook(t, func(path string, _ <-chan struct{}) (string, error) {
		hashes++
		return "", fmt.Errorf("%s must not be hashed: its row is done", path)
	})
	res = importPlanned(planned, &runOptions{tracked: true, verifyMD5: true}, newCountingSink(), nil, tr)
	if res.AlreadyImported != 1 || res.Inputs != 0 || hashes != 0 {
		t.Errorf("second run: already=%d inputs=%d hashes=%d, want 1 0 0", res.AlreadyImported, res.Inputs, hashes)
	}
	tr.close(res, 0)
}

// TestMboxFollowVerifyNoMD5: -verify-md5 on a row iafetch recorded without an md5 checks
// nothing, logs one line for that input and imports it.
func TestMboxFollowVerifyNoMD5(t *testing.T) {
	base, dbPath, db := fwSetup(t)
	iaItem(t, db, "usenet-nomd5", 1, "done")
	body := miArticle(miID1, miAlpha, "a")
	path := filepath.Join(base, fwMirror, "usenet-nomd5", "alpha.mbox")
	iaFileRow(t, db, "usenet-nomd5", "alpha.mbox", int64(len(body)), "",
		filepath.Join(fwMirror, "usenet-nomd5", "alpha.mbox"))
	fwWrite(t, path, body)
	planned, err := iafetchInputs(dbPath, base)
	if err != nil {
		t.Fatalf("iafetchInputs: %v", err)
	}

	hashes := 0
	fwHashHook(t, func(string, <-chan struct{}) (string, error) {
		hashes++
		return "", nil
	})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	res := importPlanned(planned, &runOptions{verifyMD5: true}, newCountingSink(), nil, nil)
	log.SetOutput(os.Stderr)

	if res.Inputs != 1 || res.Pending != 0 || hashes != 0 {
		t.Errorf("inputs=%d pending=%d hashes=%d, want 1 0 0", res.Inputs, res.Pending, hashes)
	}
	want := fmt.Sprintf("[MBOX-IMPORT] file=%q -verify-md5: the source recorded no md5: not checked", path)
	if out := buf.String(); !strings.Contains(out, want) {
		t.Errorf("log lacks %q\n%s", want, out)
	}
}

// TestMboxFollowDryRun: a dry run with -from-iafetch scans once, prints the planned counts
// per reason and opens no database at all (no <data>/cfg, so no tracking database either).
func TestMboxFollowDryRun(t *testing.T) {
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-dry", 1, "done")
	fwRecord(t, db, base, "usenet-dry", "alpha.mbox", miArticle(miID1, miAlpha, "a"))
	fwRecord(t, db, base, "usenet-dry", "beta.mbox", miArticle(miID2, miBeta, "b"))
	fwRecord(t, db, base, "usenet-dry", "spool.tar", "a tar of a spool")
	fwRecord(t, db, base, "usenet-dry", "usenet-dry_djvu.txt", "OCR")
	fwPlan(t, db, base, "usenet-dry", "gamma.mbox", miArticle(miID3, miAlpha, "c"))

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, sum := miRun(t, "-data", data, "-dry-run", "-follow", "1h",
		"-from-iafetch", dbPath, "-from-iafetch-base", base)
	log.SetOutput(os.Stderr) // the buffer is read only after the logger stopped writing to it

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if miExists(data) {
		t.Errorf("the dry run created %s", data)
	}
	fwWantCounts(t, sum, 2, 0, 1, 2)
	if sum.Scans != 1 {
		t.Errorf("scans = %d, want 1 (-follow is ignored in a dry run)", sum.Scans)
	}
	out := buf.String()
	for _, want := range []string{
		// the scan stats nothing: the missing file counts as supported until a worker
		// reaches it, and then as pending in the totals
		"[MBOX-IMPORT] scan 1: 5 planned input(s): supported=3 pending=0 unsupported=2",
		"[MBOX-IMPORT] scan 1: unsupported tar=1 txt=1",
		"[MBOX-IMPORT] scan 1 done: imported=2 already-imported=0 pending=1 unsupported=2",
		"is ignored in a dry run",
		fmt.Sprintf("[MBOX-IMPORT] dry-run group=%q articles=1", miAlpha),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
	}

	// a dry run needs no tracking database, so -trackdb off is allowed with -from-iafetch
	log.SetOutput(&buf)
	code, sum = miRun(t, "-data", data, "-dry-run", "-trackdb", trackDBOff,
		"-from-iafetch", dbPath, "-from-iafetch-base", base)
	log.SetOutput(os.Stderr)
	if code != 0 {
		t.Errorf("-dry-run -trackdb off: exit %d, want 0", code)
	}
	fwWantCounts(t, sum, 2, 0, 1, 2)
	if miExists(data) {
		t.Errorf("the dry run created %s", data)
	}
}

// TestMboxFollowFlagErrors: the command lines -from-iafetch refuses (exit code 2), none of
// which creates a data directory.
func TestMboxFollowFlagErrors(t *testing.T) {
	mtQuiet(t)
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-one", 1, "done")
	// a relative path: it needs -from-iafetch-base
	fwPlan(t, db, base, "usenet-one", "alpha.mbox", miArticle(miID1, miAlpha, "a"))

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"positional input", []string{"-from-iafetch", dbPath, "-from-iafetch-base", base, "x.mbox"}},
		{"base without iafetch", []string{"-from-iafetch-base", base, "x.mbox"}},
		{"verify-md5 without iafetch", []string{"-verify-md5", "x.mbox"}},
		{"follow without iafetch", []string{"-follow", "5s", "x.mbox"}},
		{"trackdb off", []string{"-from-iafetch", dbPath, "-from-iafetch-base", base, "-trackdb", trackDBOff}},
		{"negative follow", []string{"-from-iafetch", dbPath, "-from-iafetch-base", base, "-follow", "-5s"}},
		{"relative path without base", []string{"-from-iafetch", dbPath}},
		{"missing database", []string{"-from-iafetch", filepath.Join(base, "nope.sqlite")}},
		{"no input at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := miRun(t, append([]string{"-data", data}, tc.args...)...); code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
		})
	}
	if miExists(data) {
		t.Errorf("a refused command line created %s", data)
	}
}
