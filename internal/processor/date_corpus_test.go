package processor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestDateCorpus is a comparison tool, not an assertion: with PUGLEAF_DATE_CORPUS set to a
// directory of TSV files ("count<TAB>shape<TAB>example" per line, e.g. one example of every
// Date header shape found in an archive, digits replaced by 9 in the shape) it parses every
// example with ParseNNTPDateStrict and writes "count<TAB>example<TAB>result<TAB>ok" lines,
// sorted, to PUGLEAF_DATE_CORPUS_OUT (default: standard output). Run it on two revisions and
// diff the outputs to see what a parser change does to real data. Skipped otherwise.
func TestDateCorpus(t *testing.T) {
	dir := os.Getenv("PUGLEAF_DATE_CORPUS")
	if dir == "" {
		t.Skip("PUGLEAF_DATE_CORPUS not set")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.tsv"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no *.tsv in %s (%v)", dir, err)
	}
	var lines []string
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			p := strings.SplitN(sc.Text(), "\t", 3)
			if len(p) != 3 {
				continue
			}
			n, _ := strconv.Atoi(p[0])
			ex := strings.TrimSpace(p[2])
			got, ok := ParseNNTPDateStrict(ex)
			lines = append(lines, fmt.Sprintf("%d\t%s\t%s\t%v", n, ex, got.UTC().Format("2006-01-02 15:04:05"), ok))
		}
		fh.Close()
	}
	sort.Strings(lines)
	out := os.Stdout
	if name := os.Getenv("PUGLEAF_DATE_CORPUS_OUT"); name != "" {
		fh, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		defer fh.Close()
		out = fh
	}
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	t.Logf("%d examples from %d files", len(lines), len(files))
}
