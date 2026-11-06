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
// records the .iso and the OCR .txt as unsupported and a recorded file that is not on disk
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
	iso := fwRecord(t, db, base, "usenet-one", "cd1.iso", "a CD image, not an mbox")
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
	fwWantStatus(t, track, iso, trackUnsupported, "iso")
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

// fwAwait takes the next round number the injected wait reports, or fails the test.
func fwAwait(t *testing.T, entered <-chan int, want int) {
	t.Helper()
	select {
	case got := <-entered:
		if got != want {
			t.Fatalf("the importer waits before scan %d, want %d", got+1, want+1)
		}
	case <-time.After(testWait):
		t.Fatalf("scan %d did not finish within %v", want, testWait)
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

	// the injected wait: it reports the number of scans that are done and then waits for a
	// tick of the test or for the stop channel
	ticks := make(chan struct{})
	entered := make(chan int, 4)
	rounds := 0
	old := followWait
	followWait = func(_ time.Duration, stop <-chan struct{}) bool {
		rounds++
		entered <- rounds
		select {
		case <-ticks:
			return true
		case <-stop:
			return false
		}
	}
	defer func() { followWait = old }()

	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- runWith([]string{"-data", data, "-from-iafetch", dbPath,
			"-from-iafetch-base", base, "-follow", "1h"}, stop)
	}()

	fwAwait(t, entered, 1) // the first scan is done and the run is waiting
	beta := fwRecord(t, db, base, "usenet-follow", "beta.mbox", miArticle(miID2, miBeta, "b"))
	select {
	case ticks <- struct{}{}: // the run scans again
	case <-time.After(testWait):
		t.Fatalf("the importer did not take the tick within %v", testWait)
	}
	fwAwait(t, entered, 2) // the second scan is done
	close(stop)            // stopped while waiting: no error

	var code int
	select {
	case code = <-done:
	case <-time.After(testWait):
		t.Fatalf("runWith did not return within %v after the stop", testWait)
	}
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

// TestMboxFollowDryRun: a dry run with -from-iafetch scans once, prints the planned counts
// per reason and opens no database at all (no <data>/cfg, so no tracking database either).
func TestMboxFollowDryRun(t *testing.T) {
	base, dbPath, db := fwSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	iaItem(t, db, "usenet-dry", 1, "done")
	fwRecord(t, db, base, "usenet-dry", "alpha.mbox", miArticle(miID1, miAlpha, "a"))
	fwRecord(t, db, base, "usenet-dry", "beta.mbox", miArticle(miID2, miBeta, "b"))
	fwRecord(t, db, base, "usenet-dry", "cd1.iso", "a CD image")
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
		"[MBOX-IMPORT] scan 1: 5 planned input(s): supported=2 pending=1 unsupported=2",
		"[MBOX-IMPORT] scan 1: pending missing=1",
		"[MBOX-IMPORT] scan 1: unsupported iso=1 txt=1",
		"[MBOX-IMPORT] scan 1 done: imported=2 already-imported=0 pending=1 unsupported=2",
		"is ignored in a dry run",
		fmt.Sprintf("[MBOX-IMPORT] dry-run group=%q articles=1", miAlpha),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
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
