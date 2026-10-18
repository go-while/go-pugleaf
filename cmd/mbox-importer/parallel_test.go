package main

import (
	"path/filepath"
	"testing"
)

// TestMboxCmdParallelFlags: -parallel, -max-batch and -max-queue outside their ranges are
// command-line errors.
func TestMboxCmdParallelFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"parallel 0", []string{"-parallel", "0", "x.mbox"}},
		{"parallel 33", []string{"-parallel", "33", "x.mbox"}},
		{"max-batch 99", []string{"-max-batch", "99", "x.mbox"}},
		{"max-batch 1001", []string{"-max-batch", "1001", "x.mbox"}},
		{"max-queue 15", []string{"-max-queue", "15", "x.mbox"}},
		{"max-queue 100001", []string{"-max-queue", "100001", "x.mbox"}},
		{"max-queue -1", []string{"-max-queue", "-1", "x.mbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := miRun(t, tc.args...); code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
		})
	}
}

// TestMboxCmdBatchQueueSize: an explicit -max-queue is taken as is; 0 gives twice
// parallel*max-batch, at least the database default (1280) and at most the batch writer's limit.
func TestMboxCmdBatchQueueSize(t *testing.T) {
	for _, tc := range []struct {
		parallel, maxBatch, maxQueue, want int
	}{
		{1, 1000, 0, 2000},
		{1, 100, 0, 1280},
		{4, 1000, 0, 8000},
		{4, 200, 0, 1600},
		{32, 1000, 0, 64000},
		{32, 1000, 16, 16},
		{1, 100, 100000, 100000},
		{4, 1000, 1280, 1280},
	} {
		if got := batchQueueSize(tc.parallel, tc.maxBatch, tc.maxQueue); got != tc.want {
			t.Errorf("batchQueueSize(%d, %d, %d) = %d, want %d", tc.parallel, tc.maxBatch, tc.maxQueue, got, tc.want)
		}
	}
}

// TestMboxCmdParallelFakeSink: with more workers than needed every input is imported once,
// the summary keeps the command-line order, and the totals are the sum of the inputs.
func TestMboxCmdParallelFakeSink(t *testing.T) {
	dir := t.TempDir()
	const idB = "<par-b@mbox.test.invalid>"
	const idC = "<par-c@mbox.test.invalid>"
	files := []string{
		miWrite(t, dir, "a.mbox", miArticle(miID1, miAlpha, "a")),
		miWrite(t, dir, "b.mbox", miArticle(idB, miAlpha, "b")+miArticle(miID2, miBeta, "b2")),
		miWrite(t, dir, "c.mbox", miArticle(idC, miBeta, "c")),
	}
	sink := &fakeSink{results: map[string]fakeResult{
		miID1: {stored: true}, idB: {stored: true}, miID2: {present: true}, idC: {stored: true},
	}}
	res := importInputs(files, &runOptions{markDone: true, parallel: 8}, sink, nil)
	if res.Inputs != 3 || res.InputErrors != 0 || res.Stopped || res.exitCode() != 0 {
		t.Fatalf("inputs=%d input-errors=%d stopped=%t exit=%d", res.Inputs, res.InputErrors, res.Stopped, res.exitCode())
	}
	if c := res.Total; c.Read != 4 || c.Stored != 3 || c.Present != 1 || c.Errors != 0 {
		t.Errorf("totals %+v", c)
	}
	if len(res.PerInput) != 3 {
		t.Fatalf("per-input entries: %d, want 3", len(res.PerInput))
	}
	for i, c := range res.PerInput {
		if c.File != files[i] {
			t.Errorf("PerInput[%d] = %q, want %q (command-line order)", i, c.File, files[i])
		}
		if !c.MarkedDone || !miExists(files[i]+".done") {
			t.Errorf("%s not marked done", filepath.Base(files[i]))
		}
	}
	if res.MarkedDone != 3 {
		t.Errorf("marked-done=%d, want 3", res.MarkedDone)
	}
}

// TestMboxCmdParallelStop: a stop while two inputs are open finishes their current article,
// starts no further input, and renames nothing.
func TestMboxCmdParallelStop(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		miWrite(t, dir, "a.mbox", miFixture()),
		miWrite(t, dir, "b.mbox", miFixture()),
		miWrite(t, dir, "c.mbox", miFixture()),
		miWrite(t, dir, "d.mbox", miFixture()),
	}
	stop := make(chan struct{})
	sink := &stopSink{countingSink: newCountingSink(), stop: stop}
	res := importInputs(files, &runOptions{markDone: true, parallel: 2}, sink, stop)
	if !res.Stopped || res.exitCode() != 130 {
		t.Errorf("stopped=%t exit=%d, want a stopped run (130)", res.Stopped, res.exitCode())
	}
	// Two workers: at most two inputs had started when the first article closed stop. The
	// input that closed it read one article; the other read one or none (the stop is checked
	// before every article), and no third input started.
	if res.Inputs < 1 || res.Inputs > 2 || res.Total.Read < 1 || res.Total.Read > res.Inputs {
		t.Errorf("inputs=%d read=%d, want 1-2 inputs reading at most one article each", res.Inputs, res.Total.Read)
	}
	for _, f := range files {
		if miExists(f + ".done") {
			t.Errorf("%s renamed after the stop", filepath.Base(f))
		}
	}
}

// TestMboxCmdParallelDirectory: a directory imported with -parallel 3 files every article,
// marks every input done and lists every input once; -max-batch is accepted.
func TestMboxCmdParallelDirectory(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	dir := t.TempDir()
	const id3 = "<par-dir-3@mbox.test.invalid>"
	const id4 = "<par-dir-4@mbox.test.invalid>"
	a := miWrite(t, dir, "a.mbox", miArticle(miID1, miAlpha, "a"))
	b := miWrite(t, dir, "b.mbox", miArticle(miID2, miBeta, "b"))
	c := miWrite(t, dir, "c.mbox", miArticle(id3, miAlpha, "c"))
	d := miWrite(t, dir, "d.mbox", miArticle(id4, miBeta, "d"))

	code, sum := miRun(t, "-data", data, "-parallel", "3", "-max-batch", "100", dir)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 4, 4, 0, 0, 0, "-")
	if sum.Inputs != 4 || sum.MarkedDone != 4 || len(sum.PerInput) != 4 {
		t.Errorf("inputs=%d marked=%d per-input=%d", sum.Inputs, sum.MarkedDone, len(sum.PerInput))
	}
	for _, f := range []string{a, b, c, d} {
		if !miExists(f + ".done") {
			t.Errorf("%s not marked done", filepath.Base(f))
		}
	}
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1, id3}, miBeta: {miID2, id4}}, []string{miID1, miID2, id3, id4})

	// A second run finds everything present and has nothing left to import.
	code, sum = miRun(t, "-data", data, "-parallel", "3", dir)
	if code != 0 || sum.Inputs != 0 {
		t.Errorf("re-run: exit %d inputs=%d, want 0 and 0 (all inputs are .done)", code, sum.Inputs)
	}
}

// TestMboxCmdGroupFromFileName: the file-name rule of -group-from-name.
func TestMboxCmdGroupFromFileName(t *testing.T) {
	for _, tc := range []struct {
		path, want string
	}{
		{"de.comm.internet.commerce.mbox.gz", "de.comm.internet.commerce"},
		{"/x/y/alt.test.mbox", "alt.test"},
		{"alt.test.mbox.done", "alt.test"},
		{"alt.test.bag", "alt.test"},
		{"pl.comp.lang.c.usenet.xz", "pl.comp.lang.c"},
		{"pl.comp.lang.c.usenet.7z.done", "pl.comp.lang.c"},
		{"alt.test.MBOX.ZIP", "alt.test"},
		{".mbox", ""},
		{"bad name!.mbox", ""},
		{"nosuffix", ""},
	} {
		got, err := groupFromFileName(tc.path)
		if tc.want == "" {
			if err == nil {
				t.Errorf("%q: got %q, want an error", tc.path, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
}

// TestMboxCmdGroupFromName: -group-from-name files each input into the group named by its
// file, whatever the Newsgroups header says; a file with an invalid name is an input error
// and stays unrenamed; -group and -group-from-name together are a command-line error.
func TestMboxCmdGroupFromName(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	dir := t.TempDir()
	const one, two = "t.fromname.one", "t.fromname.two"
	a := miWrite(t, dir, one+".mbox", miArticle(miID1, miAlpha, "a"))
	b := miWrite(t, dir, two+".bag", miArticle(miID2, miAlpha, "b"))
	bad := miWrite(t, dir, "bad name!.mbox", miArticle(miID3, miAlpha, "c"))

	if code, _ := miRun(t, "-data", data, "-group", one, "-group-from-name", a); code != 2 {
		t.Errorf("-group with -group-from-name: exit %d, want 2", code)
	}

	code, sum := miRun(t, "-data", data, "-group-from-name", "-parallel", "2", dir)
	if code != 1 || sum.InputErrors != 1 || sum.Inputs != 3 || sum.MarkedDone != 2 {
		t.Fatalf("exit %d inputs=%d input-errors=%d marked=%d; want 1, 3, 1, 2", code, sum.Inputs, sum.InputErrors, sum.MarkedDone)
	}
	miCheck(t, sum, 2, 2, 0, 0, 0, "-")
	if !miExists(a+".done") || !miExists(b+".done") || miExists(bad+".done") {
		t.Errorf("renamed: a=%t b=%t bad=%t", miExists(a+".done"), miExists(b+".done"), miExists(bad+".done"))
	}
	miCheckGroups(t, data, map[string][]string{one: {miID1}, two: {miID2}}, []string{miID1, miID2}, miAlpha)
}
