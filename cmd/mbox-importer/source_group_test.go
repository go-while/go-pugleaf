package main

// Tests of targetGroup: an article goes to the group of the input it came from (the file
// name, a UAT package, a spool directory) when it lists that group, not to the first group
// of its Newsgroups header. Before, every copy of a crosspost went to the first group: an
// archive's article "Newsgroups: rec.boat,ca.forsale" made a one-article group rec.boat and
// ca.forsale never got it.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceGroupTargetGroup(t *testing.T) {
	head := func(ng string) []string { return []string{"Newsgroups: " + ng, "Subject: x"} }
	for _, c := range []struct {
		name       string
		newsgroups string
		opts       runOptions
		want       string
		reason     skipReason
	}{
		{"no source: first group", "a.one,a.two", runOptions{}, "a.one", skipNone},
		{"file group listed second", "a.typo,a.two", runOptions{sourceGroup: "a.two"}, "a.two", skipNone},
		{"file group in another case", "A.Two,a.one", runOptions{sourceGroup: "a.two"}, "a.two", skipNone},
		{"file group not listed: first group", "a.one,a.two", runOptions{sourceGroup: "b.other"}, "a.one", skipNone},
		{"hierarchy dump: first group below it", "x.y,free.pt.foo,free.pt.bar", runOptions{sourceGroup: "free.pt"}, "free.pt.foo", skipNone},
		{"prefix needs a dot", "free.ptx,free.pt.foo", runOptions{sourceGroup: "free.pt"}, "free.pt.foo", skipNone},
		{"spool directory", "alt.typo,alt.test", runOptions{sourceDir: "SPOOL/alt/test"}, "alt.test", skipNone},
		{"spool directory upper case (ISO 9660)", "alt.typo,alt.test", runOptions{sourceDir: "SPOOL/ALT/TEST"}, "alt.test", skipNone},
		{"spool directory: longest match", "test,alt.test", runOptions{sourceDir: "news/alt/test"}, "alt.test", skipNone},
		{"spool directory is a suffix by components", "alt.typo,t.test", runOptions{sourceDir: "SPOOL/alt/test"}, "alt.typo", skipNone},
		{"spool directory beats the file group", "a.one,a.two,alt.test", runOptions{sourceDir: "SPOOL/alt/test", sourceGroup: "a.two"}, "alt.test", skipNone},
		{"spool root: file group", "a.one,a.two", runOptions{sourceDir: ".", sourceGroup: "a.two"}, "a.two", skipNone},
		{"forced group wins", "a.one,a.two", runOptions{group: "f.orced", sourceGroup: "a.two"}, "f.orced", skipNone},
		{"bad first group, source listed", "bad/group,a.two", runOptions{sourceGroup: "a.two"}, "a.two", skipNone},
		{"bad first group, no source", "bad/group,a.two", runOptions{}, "", skipBadGroup},
		{"no Newsgroups", "", runOptions{sourceGroup: "a.two"}, "", skipNoNewsgroups},
		{"no Newsgroups, forced", "", runOptions{group: "f.orced"}, "", skipNoNewsgroups},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := head(c.newsgroups)
			if c.newsgroups == "" {
				h = []string{"Subject: x"}
			}
			got, reason := targetGroup(h, &c.opts)
			if got != c.want || reason != c.reason {
				t.Errorf("targetGroup = %q, %v; want %q, %v", got, reason, c.want, c.reason)
			}
		})
	}
}

// TestSourceGroupMboxCrosspost: one crosspost in two per-group archives whose header names
// a third group first. Each archive files it into its own group; the first group is never
// created. An article that does not list its archive's group goes to its first group.
func TestSourceGroupMboxCrosspost(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	const typo = "t.mbox.typo"
	const gamma = "t.mbox.gamma"
	cross := typo + "," + miAlpha + "," + miBeta
	dir := t.TempDir()
	alpha := miWrite(t, dir, miAlpha+".mbox", miArticle(miID1, cross, "crosspost")+miArticle(miID3, gamma, "elsewhere"))
	beta := miWrite(t, dir, miBeta+".mbox", miArticle(miID1, cross, "crosspost")+miArticle(miID2, miBeta, "own"))
	data := filepath.Join(t.TempDir(), "data")

	code, sum := miRun(t, "-data", data, alpha, beta)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 4, 4, 0, 0, 0, "-")
	miCheckGroups(t, data, map[string][]string{
		miAlpha: {miID1},
		miBeta:  {miID1, miID2},
		gamma:   {miID3},
	}, []string{miID1, miID2, miID3}, typo)
}

// TestSourceGroupSpoolDir: an article file in a spool directory goes to that directory's
// group when it lists it, also when another group comes first.
func TestSourceGroupSpoolDir(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	const typo = "t.mbox.typo"
	tree := t.TempDir()
	for name, content := range map[string]string{
		"SPOOL/t/mbox/beta/1":  miArticle(miID1, typo+","+miBeta, "crosspost"),
		"SPOOL/t/mbox/alpha/1": miArticle(miID2, miAlpha, "own"),
	} {
		p := filepath.Join(tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(t.TempDir(), "data")

	code, sum := miRun(t, "-data", data, "-spool", tree)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 2, 2, 0, 0, 0, "-")
	miCheckGroups(t, data, map[string][]string{
		miAlpha: {miID2},
		miBeta:  {miID1},
	}, []string{miID1, miID2}, typo)
}
