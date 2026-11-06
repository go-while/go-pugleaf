package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dcSpoolTree writes a small news spool tree below dir (the layout of a NetNews CD: one
// article per file, numeric names, an .XRF crosspost pointer and a file that is no article)
// and returns dir.
func dcSpoolTree(t *testing.T, dir string) string {
	t.Helper()
	files := map[string]string{
		"SPOOL/alt/test/1":         miArticle(miID1, miAlpha, "one"),
		"SPOOL/alt/test/2":         miArticle(miID2, miAlpha, "two"),
		"SPOOL/alt/test/2.XRF":     "alt/other/17\n",
		"LIB/ACTIVE":               "alt.test 0000000002 0000000001 y\n",
		"BIN/rnews":                "\x7fELF not an article\n",
		"DOUG.PRO":                 miArticle(miID3, miBeta, "mbox in a spool"),
		"news/alt/other/.overview": "1\talt.test\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// dcNeedTool returns the full path of prog or skips the test.
func dcNeedTool(t *testing.T, prog string) string {
	t.Helper()
	path, err := exec.LookPath(prog)
	if err != nil {
		t.Skipf("%s not installed", prog)
	}
	return path
}

// dcMakeISO builds an ISO 9660 image (Rock Ridge) of tree with genisoimage and returns its path.
func dcMakeISO(t *testing.T, tree, image string) string {
	t.Helper()
	prog := dcNeedTool(t, "genisoimage")
	cmd := exec.Command(prog, "-quiet", "-R", "-o", image, tree)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("genisoimage: %v\n%s", err, out)
	}
	return image
}

// dcMakeSquashfs builds a squashfs image of tree with mksquashfs and returns its path.
func dcMakeSquashfs(t *testing.T, tree, image string) string {
	t.Helper()
	prog := dcNeedTool(t, "mksquashfs")
	cmd := exec.Command(prog, tree, image, "-quiet", "-no-progress")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs: %v\n%s", err, out)
	}
	return image
}

// dcUnpackDirs lists the private unpack directories left under tmpDir.
func dcUnpackDirs(t *testing.T, tmpDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mbox-importer-") {
			left = append(left, e.Name())
		}
	}
	return left
}

// dcWaitFile waits until path exists; it only stops a hung test (testWait), a passing test
// never waits the whole time.
func dcWaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s was not created", path)
}

// TestMboxDiscInputKind pins the disc image suffixes of inputKind.
func TestMboxDiscInputKind(t *testing.T) {
	for _, c := range []struct {
		path, kind string
		disc       bool
	}{
		{"NN_1992_16.iso", kindDisc, true},
		{"CDROM_March92.cdr", kindDisc, true},
		{"utzoo.sqsh", kindDisc, true},
		{"DOUG.img", kindDisc, true},
		{"/mirror/item/F.ISO", kindDisc, true},
		{"cd1.iso.done", kindDisc, true}, // a renamed input can be imported again
		{"de.comm.foo.mbox", "mbox", false},
		{"de.comm.foo.mbox.gz", "gz", false},
		{"spool.zip", "zip", false},
		{"cd.7z", "7z", false},
		{"alt.foo.usenet.7z", "7z", false},
		{"spool.tar", "mbox", false}, // no reader by suffix: the parser rejects it
		{"isolde", "mbox", false},    // not a suffix
	} {
		if got := inputKind(c.path); got != c.kind {
			t.Errorf("inputKind(%q) = %q, want %q", c.path, got, c.kind)
		}
		if got := isDiscImage(c.path); got != c.disc {
			t.Errorf("isDiscImage(%q) = %v, want %v", c.path, got, c.disc)
		}
	}
}

// TestMboxDiscSpoolInput pins the dispatch: a disc image is always a news spool, a
// directory or a zip only with -spool.
func TestMboxDiscSpoolInput(t *testing.T) {
	dir := t.TempDir()
	tree := dcSpoolTree(t, filepath.Join(dir, "tree"))
	zipPath := writeZip(t, "spool.zip", map[string]string{"x": "y"}, []string{"x"})
	mbox := writeFile(t, "group.mbox", fixtureMbox)
	image := filepath.Join(dir, "cd.iso") // not created: the name decides

	for _, c := range []struct {
		path       string
		spool      bool
		want       spoolKind
		wantString string
	}{
		{image, false, spoolDisc, "disc image without -spool"},
		{image, true, spoolDisc, "disc image with -spool"},
		{tree, false, spoolNone, "directory without -spool"},
		{tree, true, spoolDir, "directory with -spool"},
		{zipPath, false, spoolNone, "zip without -spool"},
		{zipPath, true, spoolZip, "zip with -spool"},
		{mbox, false, spoolNone, "mbox without -spool"},
		{mbox, true, spoolNone, "mbox with -spool"},
		{filepath.Join(dir, "missing-dir"), true, spoolNone, "missing path with -spool"},
	} {
		if got := spoolInput(c.path, &runOptions{spool: c.spool}); got != c.want {
			t.Errorf("%s: spoolInput = %d, want %d", c.wantString, got, c.want)
		}
	}

	// openSource refuses a disc image: it is read as a spool, not as an mbox
	iso := filepath.Join(dir, "real.iso")
	if err := os.WriteFile(iso, []byte("not opened"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := openSource(iso, "7z")
	if err == nil {
		_ = src.Close()
		t.Fatal("openSource of a disc image: no error")
	}
	if !strings.Contains(err.Error(), "news spool") {
		t.Errorf("openSource of a disc image: %v", err)
	}
}

// TestMboxDiscSpoolDirSizes pins that a spool directory is tracked with size 0 (a directory
// has no size of its own) and keeps its modification time.
func TestMboxDiscSpoolDirSizes(t *testing.T) {
	dir := t.TempDir()
	tree := dcSpoolTree(t, filepath.Join(dir, "tree"))
	mbox := writeFile(t, "group.mbox", fixtureMbox)

	planned := plannedFromPaths([]string{tree, mbox})
	spoolDirSizes(planned)
	if planned[0].Size != 0 || planned[0].Mtime == 0 {
		t.Errorf("directory input: size=%d mtime=%d, want 0 and a time", planned[0].Size, planned[0].Mtime)
	}
	if planned[1].Size != int64(len(fixtureMbox)) {
		t.Errorf("file input: size=%d, want %d", planned[1].Size, len(fixtureMbox))
	}
}

// TestMboxDiscUnpack unpacks an ISO and a squashfs image with the 7z program and checks the
// tree that comes out, then that openSpool's release removes the unpack directory again
// (the success path: the walk itself is readSpool's, see spool.go).
func TestMboxDiscUnpack(t *testing.T) {
	need7z(t)
	dir := t.TempDir()
	tree := dcSpoolTree(t, filepath.Join(dir, "tree"))

	for _, c := range []struct {
		name  string
		build func() string
	}{
		{"iso", func() string { return dcMakeISO(t, tree, filepath.Join(dir, "spool.iso")) }},
		{"sqsh", func() string { return dcMakeSquashfs(t, tree, filepath.Join(dir, "spool.sqsh")) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			image := c.build()
			tmp := t.TempDir()
			opts := &runOptions{sevenZip: "7z", tmpDir: tmp}
			fsys, release, err := openSpool(image, spoolDisc, opts, nil)
			if err != nil {
				t.Fatalf("openSpool: %v", err)
			}
			// the unpacked tree is below the private directory and readable through fsys
			for _, name := range []string{"SPOOL/alt/test/1", "SPOOL/alt/test/2.XRF", "LIB/ACTIVE"} {
				b, err := fs.ReadFile(fsys, name)
				if err != nil {
					t.Errorf("%s: %v", name, err)
					continue
				}
				if len(b) == 0 {
					t.Errorf("%s: empty", name)
				}
			}
			if b, err := fs.ReadFile(fsys, "SPOOL/alt/test/1"); err != nil || string(b) != miArticle(miID1, miAlpha, "one") {
				t.Errorf("article 1 content: %v %q", err, b)
			}
			if dirs := dcUnpackDirs(t, tmp); len(dirs) != 1 {
				t.Errorf("unpack directories %v, want exactly one", dirs)
			}
			release()
			if dirs := dcUnpackDirs(t, tmp); len(dirs) != 0 {
				t.Errorf("unpack directories after release: %v, want none", dirs)
			}
		})
	}
}

// TestMboxDiscUnpackErrors: a corrupt image and a missing 7z program are input errors and
// leave no unpack directory behind.
func TestMboxDiscUnpackErrors(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.iso")
	if err := os.WriteFile(corrupt, []byte(strings.Repeat("not an image\n", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	need7z(t)
	tmp := t.TempDir()
	opts := &runOptions{sevenZip: "7z", tmpDir: tmp}
	if fsys, release, err := openSpool(corrupt, spoolDisc, opts, nil); err == nil {
		release()
		t.Errorf("corrupt image: no error (fsys %v)", fsys)
	} else if errors.Is(err, errStopped) {
		t.Errorf("corrupt image: %v, want an input error", err)
	}
	if dirs := dcUnpackDirs(t, tmp); len(dirs) != 0 {
		t.Errorf("unpack directories after a failed unpack: %v, want none", dirs)
	}

	// no 7z program: nothing is unpacked, nothing is left
	tmp = t.TempDir()
	missing := &runOptions{sevenZip: filepath.Join(dir, "no-such-7z"), tmpDir: tmp}
	_, _, err := openSpool(corrupt, spoolDisc, missing, nil)
	if err == nil || !strings.Contains(err.Error(), "7z program") {
		t.Errorf("missing 7z: %v", err)
	}
	if dirs := dcUnpackDirs(t, tmp); len(dirs) != 0 {
		t.Errorf("unpack directories after a missing 7z: %v, want none", dirs)
	}
}

// TestMboxDiscUnpackStopped: the shutdown signal while 7z runs kills it and ends the input
// as stopped, and the unpack directory is removed. The 7z program is replaced by a script
// that reports it started and then sleeps, so the test does not depend on the size of an image.
func TestMboxDiscUnpackStopped(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	finished := filepath.Join(dir, "finished")
	fake := filepath.Join(dir, "fake-7z")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\nsleep 30\ntouch %q\n", started, finished)
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil { // #nosec G306 -- a test program
		t.Fatal(err)
	}
	image := filepath.Join(dir, "slow.iso")
	if err := os.WriteFile(image, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	opts := &runOptions{sevenZip: fake, tmpDir: tmp, markDone: true}
	stop := make(chan struct{})
	type result struct {
		c              *counters
		inputErr, stop bool
	}
	res := make(chan result, 1)
	go func() {
		c, inputErr, stopped := importSpool(image, spoolDisc, opts, newCountingSink(), stop)
		res <- result{c, inputErr, stopped}
	}()
	dcWaitFile(t, started) // the unpack is running
	close(stop)
	got := <-res

	if !got.stop || got.inputErr {
		t.Errorf("stopped=%v inputErr=%v, want true/false", got.stop, got.inputErr)
	}
	if got.c != nil {
		t.Errorf("counters %v, want nil: nothing was read", got.c)
	}
	if _, err := os.Stat(finished); err == nil {
		t.Error("the unpack program ran to its end: it was not killed")
	}
	if dirs := dcUnpackDirs(t, tmp); len(dirs) != 0 {
		t.Errorf("unpack directories after the stop: %v, want none", dirs)
	}
	if miExists(image + ".done") {
		t.Error("a stopped disc image was renamed")
	}

	// a stop that is already closed starts nothing at all
	closed := make(chan struct{})
	close(closed)
	if err := unpackDisc(image, tmp, fake, closed); !errors.Is(err, errStopped) {
		t.Errorf("unpackDisc with a closed stop: %v, want errStopped", err)
	}
	if _, err := os.Stat(finished); err == nil {
		t.Error("the unpack program ran although the stop was closed")
	}
}

// TestMboxDiscImportDispatch runs a disc image, a spool directory and a zip spool through
// the import loop. readSpool is still the wave-0 stub (errSpoolNotImplemented), so every
// input ends as an input error here; what this pins is the dispatch, the cleanup of the
// unpack directory and that no spool input is renamed on a failed read.
func TestMboxDiscImportDispatch(t *testing.T) {
	need7z(t)
	dir := t.TempDir()
	tree := dcSpoolTree(t, filepath.Join(dir, "tree"))
	image := dcMakeISO(t, tree, filepath.Join(dir, "spool.iso"))
	zipPath := writeZip(t, "spool.zip", map[string]string{"SPOOL/alt/test/1": miArticle(miID1, miAlpha, "one")},
		[]string{"SPOOL/alt/test/1"})
	tmp := t.TempDir()

	for _, c := range []struct {
		name  string
		path  string
		spool bool
	}{
		{"disc image", image, false},
		{"spool directory", tree, true},
		{"spool zip", zipPath, true},
	} {
		opts := &runOptions{sevenZip: "7z", tmpDir: tmp, spool: c.spool, markDone: true}
		res := importInputs([]string{c.path}, opts, newCountingSink(), nil)
		if res.Inputs != 1 || res.InputErrors != 1 {
			t.Errorf("%s: inputs=%d input-errors=%d, want 1/1 (readSpool is still a stub)",
				c.name, res.Inputs, res.InputErrors)
		}
		if len(res.PerInput) != 1 || res.PerInput[0].File != c.path {
			t.Errorf("%s: per-input counters %v", c.name, res.PerInput)
		}
		if res.Stopped || res.MarkedDone != 0 || miExists(c.path+".done") {
			t.Errorf("%s: stopped=%v marked-done=%d: an input that failed must not be renamed",
				c.name, res.Stopped, res.MarkedDone)
		}
		if dirs := dcUnpackDirs(t, tmp); len(dirs) != 0 {
			t.Errorf("%s: unpack directories left: %v", c.name, dirs)
		}
	}

	// a spool directory is never renamed, not even when the import went through: the
	// mark-done switch takes that branch before it looks at the counters
	if miExists(tree + ".done") {
		t.Error("the spool directory was renamed")
	}
}
