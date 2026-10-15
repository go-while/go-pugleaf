package database

// Tests of the audit-delta-fixes slice adf-opt-web-maint (A-ops-11): RsyncDIR passes
// "--" before the paths and prefixes relative paths with "./", so a path can never
// become an rsync option or an ssh remote. No database is used.

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAdfOptRsyncArgs(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		remove   bool
		want     []string
	}{
		{"option-like source", "-foo", "new/db", false,
			[]string{"-va", "--progress", "--", "./-foo", "./new/db"}},
		{"remote-like target", "data/db/abc", "host:x/db", false,
			[]string{"-va", "--progress", "--", "./data/db/abc", "./host:x/db"}},
		{"absolute paths, remove source", "/srv/a", "/srv/b", true,
			[]string{"-va", "--progress", "--remove-source-files", "--", "/srv/a", "/srv/b"}},
		{"already dot-slash and parent", "./data/db/x", "../new", false,
			[]string{"-va", "--progress", "--", "./data/db/x", "./../new"}},
		{"long option source", "--delete", "-e sh", true,
			[]string{"-va", "--progress", "--remove-source-files", "--", "./--delete", "./-e sh"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rsyncArgs(tc.old, tc.new, tc.remove)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rsyncArgs(%q, %q, %t) = %q, want %q", tc.old, tc.new, tc.remove, got, tc.want)
			}
			// general shape: options, then "--", then exactly 2 local paths
			sep := -1
			for i, a := range got {
				if a == "--" {
					sep = i
					break
				}
				if !strings.HasPrefix(a, "-") {
					t.Errorf("argument %q before \"--\" is not an option", a)
				}
			}
			if sep < 0 || len(got)-sep-1 != 2 {
				t.Fatalf("want \"--\" followed by 2 paths, got %q", got)
			}
			for _, p := range got[sep+1:] {
				if strings.HasPrefix(p, "-") {
					t.Errorf("path %q starts with '-'", p)
				}
				slash := strings.Index(p, "/")
				if colon := strings.Index(p, ":"); colon >= 0 && (slash < 0 || colon < slash) {
					t.Errorf("path %q has a ':' before the first '/': rsync would read it as a remote", p)
				}
			}
		})
	}
}

func TestAdfOptRsyncEmptyPath(t *testing.T) {
	if err := RsyncDIR("", t.TempDir(), false); err == nil {
		t.Error("RsyncDIR with an empty source was accepted")
	}
	if err := RsyncDIR(t.TempDir(), "", false); err == nil {
		t.Error("RsyncDIR with an empty target was accepted")
	}
}

// TestAdfOptRsyncDirReal runs the real rsync (skipped when it is not installed) on a
// source directory whose name looks like an option: the "--" separator must be
// accepted and the directory copied.
func TestAdfOptRsyncDirReal(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	root := t.TempDir()
	src := filepath.Join(root, "-src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "g.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "dst")
	if err := RsyncDIR(src, dst, true); err != nil {
		t.Fatalf("RsyncDIR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "-src", "g.db")); err != nil {
		t.Fatalf("copied file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "g.db")); !os.IsNotExist(err) {
		t.Fatalf("source file still present after --remove-source-files (err=%v)", err)
	}
}

// TestAdfOptRsyncDirRelative runs the real rsync (skipped when it is not installed) on
// relative paths that look like an option and a remote: the "./" prefix must keep
// both local.
func TestAdfOptRsyncDirRelative(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("-src", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("-src", "g.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RsyncDIR("-src", "host:dst", true); err != nil {
		t.Fatalf("RsyncDIR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "host:dst", "-src", "g.db")); err != nil {
		t.Fatalf("copied file missing in the local directory 'host:dst': %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "-src", "g.db")); !os.IsNotExist(err) {
		t.Fatalf("source file still present after --remove-source-files (err=%v)", err)
	}
}
