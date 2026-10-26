package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/processor"
)

// A-tools-11: names from an active file that are not newsgroup names are skipped.
func TestAdfOptAnalyzeActiveFileValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active")
	content := strings.Join([]string{
		"# comment",
		"alt.test 10 1 y",
		"../../../tmp/x 1 1 y",
		"a/b 1 1 y",
		"a..b 1 1 y",
		".hidden 1 1 y",
		"short 1",
		"de.rec.film 5 1 m",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	groups, err := readGroupsFromActiveFile(path)
	if err != nil {
		t.Fatalf("readGroupsFromActiveFile: %v", err)
	}
	if len(groups) != 2 || groups[0] != "alt.test" || groups[1] != "de.rec.film" {
		t.Fatalf("groups = %q, want [alt.test de.rec.film]", groups)
	}

	bad := filepath.Join(t.TempDir(), "active.bad")
	if err := os.WriteFile(bad, []byte("../x 1 1 y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if groups, err := readGroupsFromActiveFile(bad); err == nil {
		t.Fatalf("active file with only invalid names: want error, got %q", groups)
	}
}

// A-tools-11: the output directory defaults below -data and -out overrides it.
func TestAdfOptAnalyzeResolveOutDir(t *testing.T) {
	if got, want := resolveOutDir("", "/srv/data-test"), filepath.Join("/srv/data-test", defaultOutSubdir); got != want {
		t.Fatalf("resolveOutDir default = %q, want %q", got, want)
	}
	if got := resolveOutDir("/x/out", "/srv/data-test"); got != "/x/out" {
		t.Fatalf("resolveOutDir with -out = %q", got)
	}
}

// A-tools-11: the groups-file summary stays inside the output directory.
func TestAdfOptAnalyzeSummaryFileName(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	for _, p := range []string{"active", "/etc/active", "../../x", "..", "a\\b", "./x/../../y"} {
		got := summaryFileName(outDir, p)
		if filepath.Dir(got) != outDir {
			t.Errorf("summaryFileName(%q) = %q, not directly in %q", p, got, outDir)
		}
		if !strings.HasSuffix(got, ".summary.txt") {
			t.Errorf("summaryFileName(%q) = %q, missing suffix", p, got)
		}
	}
}

// writeSummaryToFile creates the output directory and writes the summary there.
func TestAdfOptAnalyzeWriteSummary(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "nested", "out")
	file := summaryFileName(outDir, "../active")
	if err := writeSummaryToFile(file, []string{"alt.test"}, 1, 0, 0, 0, &processor.ArticleSizeStats{}, nil, nil); err != nil {
		t.Fatalf("writeSummaryToFile: %v", err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Total groups: 1") {
		t.Fatalf("summary content: %q", b)
	}
}
