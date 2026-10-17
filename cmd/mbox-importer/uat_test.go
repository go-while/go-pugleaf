package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/mbox2nntp/uat"
	"github.com/go-while/mbox2nntp/uat/uattest"
)

const (
	uaGroup     = "t.uat.polish" // the package name, also the first group of the Polish article
	uaOther     = "t.uat.other"  // the first group of the crosspost
	uaIDPolish  = "<pl@uat.test.invalid>"
	uaIDCross   = "<cross@uat.test.invalid>"
	uaIDEmpty   = "<empty@uat.test.invalid>"
	uaIDNoSubj  = "<nosubj@uat.test.invalid>"
	uaPolishSub = "Zażółć gęślą jaźń"
)

// uaMessage is one message as the toolkit stores it: decoded UTF-8 header fields, LF line
// endings, no Content-Type; subject "" leaves the Subject header out.
func uaMessage(msgid, newsgroups, subject, body string) string {
	var b strings.Builder
	b.WriteString("Path: a.example!not-for-mail\n")
	b.WriteString("From: Jan Kowalski <jan@example.invalid>\n")
	b.WriteString("Newsgroups: " + newsgroups + "\n")
	if subject != "" {
		b.WriteString("Subject: " + subject + "\n")
	}
	b.WriteString("Date: Tue, 24 Jun 2003 19:30:12 +0200\n")
	b.WriteString("Message-ID: " + msgid + "\n")
	b.WriteString("\n" + body)
	return b.String()
}

// uaMessages is the 4-message package: a Polish subject, a crosspost whose first group is
// not the package's, an article whose body is only empty lines, one without a Subject.
func uaMessages() []string {
	return []string{
		uaMessage(uaIDPolish, uaGroup, uaPolishSub, "Treść wiadomości: zażółć.\n\n"),
		uaMessage(uaIDCross, uaOther+","+uaGroup, "Re: crosspost", "plain ascii body\n"),
		uaMessage(uaIDEmpty, uaGroup, "empty", "\n\n\n"),
		uaMessage(uaIDNoSubj, uaGroup, "", "no subject here\n"),
	}
}

var uaIDs = []string{uaIDPolish, uaIDCross, uaIDEmpty, uaIDNoSubj}

// uaWrite writes msgs as dir/<name>.usenet (skips the test without zstd).
func uaWrite(t *testing.T, dir, name string, msgs []string) string {
	t.Helper()
	zstd := uattest.Zstd(t)
	return uattest.Write(t, zstd, dir, name, "a test group", msgs)
}

// uaNeedProg returns the path of prog or skips the test.
func uaNeedProg(t *testing.T, prog string) string {
	t.Helper()
	p, err := exec.LookPath(prog)
	if err != nil {
		t.Skipf("%s not found", prog)
	}
	return p
}

// uaXz writes src compressed with xz to dst.
func uaXz(t *testing.T, src, dst string) {
	t.Helper()
	out, err := exec.Command(uaNeedProg(t, "xz"), "-c", "--", src).Output()
	if err != nil {
		t.Fatalf("xz: %v", err)
	}
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ua7z writes a 7z archive dst holding src as the entry entry.
func ua7z(t *testing.T, src, entry, dst string) {
	t.Helper()
	prog := uaNeedProg(t, "7z")
	stage := t.TempDir()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, entry), data, 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err = filepath.Abs(dst)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(prog, "a", "-bd", "--", dst, entry)
	cmd.Dir = stage
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("7z a: %v: %s", err, out)
	}
}

// uaScript writes an executable shell script and returns its path (skips on Windows).
func uaScript(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	uaNeedProg(t, "sh")
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// uaEntries returns the number of entries of dir.
func uaEntries(t *testing.T, dir string) int {
	t.Helper()
	e, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(e)
}

// uaCaptureLog runs fn with the standard logger writing to a buffer and returns the output.
func uaCaptureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	fn()
	log.SetOutput(os.Stderr) // the buffer is read only after the logger stopped writing to it
	return buf.String()
}

func TestMboxUatSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"pl.comp.usenet":               ".usenet",
		"/a/b/pl.comp.usenet.xz":       ".usenet.xz",
		"pl.comp.usenet.7z":            ".usenet.7z",
		"pl.comp.usenet.done":          ".usenet",
		"pl.comp.usenet.xz.done":       ".usenet.xz",
		"PL.COMP.USENET.7Z":            ".usenet.7z",
		"pl.comp.usenet.gz":            "",
		"g.mbox.7z":                    "",
		"g.mbox":                       "",
		"usenet.xz":                    "",
		"/dir.usenet/g.mbox":           "",
		"pl.comp.usenet.xz.done.extra": "",
	} {
		if got := uatSuffix(in); got != want {
			t.Errorf("uatSuffix(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"/a/pl.comp.usenet.xz":   "pl.comp.usenet",
		"pl.comp.usenet.7z.done": "pl.comp.usenet",
		"copy2.usenet.7z":        "copy2.usenet",
	} {
		if got := uatUnpackedName(in); got != want {
			t.Errorf("uatUnpackedName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMboxUatInputName(t *testing.T) {
	for name, want := range map[string]bool{
		"pl.comp.usenet":         true,
		"pl.comp.usenet.xz":      true,
		"pl.comp.usenet.7z":      true,
		"pl.comp.usenet.xz.done": false,
		"pl.comp.usenet.xz.part": false,
		"pl.comp.usenet.gz":      false,
		"pl.comp.txt":            false,
	} {
		if got := isInputName(name); got != want {
			t.Errorf("isInputName(%q) = %t, want %t", name, got, want)
		}
	}
}

func TestMboxUatHasYEnc(t *testing.T) {
	for _, tc := range []struct {
		body []string
		want bool
	}{
		{nil, false},
		{[]string{"text", "=ybegin line=128 size=3 name=a.bin", "xyz", "=yend size=3"}, true},
		{[]string{"=ybegin"}, true},
		{[]string{"text", " =ybegin not at the start", "=yBegin"}, false},
	} {
		if got := hasYEnc(tc.body); got != tc.want {
			t.Errorf("hasYEnc(%q) = %t, want %t", tc.body, got, tc.want)
		}
	}
}

func TestMboxUatUnpackXz(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	content := []byte("unpacked content\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	checkOut := func(t *testing.T, dst string) {
		t.Helper()
		got, err := os.ReadFile(dst)
		if err != nil || !bytes.Equal(got, content) {
			t.Errorf("unpacked %q, %v", got, err)
		}
	}

	t.Run("xz", func(t *testing.T) {
		arc := filepath.Join(t.TempDir(), "g.usenet.xz")
		uaXz(t, src, arc)
		dst := filepath.Join(t.TempDir(), "g.usenet")
		if err := unpackXz(arc, dst, "xz", "/nonexistent/7z"); err != nil {
			t.Fatal(err)
		}
		checkOut(t, dst)
	})
	t.Run("7z fallback", func(t *testing.T) {
		arc := filepath.Join(t.TempDir(), "g.usenet.xz")
		uaXz(t, src, arc)
		uaNeedProg(t, "7z")
		dst := filepath.Join(t.TempDir(), "g.usenet")
		if err := unpackXz(arc, dst, "/nonexistent/xz", "7z"); err != nil {
			t.Fatal(err)
		}
		checkOut(t, dst)
	})
	t.Run("not xz", func(t *testing.T) {
		uaNeedProg(t, "xz")
		dst := filepath.Join(t.TempDir(), "g.usenet")
		err := unpackXz(src, dst, "xz", "7z")
		// xz names the file in its (localized) message: proves stderr is in the error
		if err == nil || !strings.Contains(err.Error(), src) {
			t.Errorf("err = %v, want xz's stderr naming %s", err, src)
		}
		if miExists(dst) {
			t.Error("the failed output was left behind")
		}
	})
	t.Run("missing programs", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "g.usenet")
		if err := unpackXz(src, dst, "/nonexistent/xz", "/nonexistent/7z"); err == nil {
			t.Error("no error without xz and 7z")
		}
		if miExists(dst) {
			t.Error("output created without a program")
		}
	})
	t.Run("fails after writing", func(t *testing.T) {
		prog := uaScript(t, "xz", "echo partial; echo uat-broken-pipe >&2; exit 3")
		dst := filepath.Join(t.TempDir(), "g.usenet")
		err := unpackXz(src, dst, prog, "/nonexistent/7z")
		if err == nil || !strings.Contains(err.Error(), "uat-broken-pipe") {
			t.Errorf("err = %v, want the stderr text", err)
		}
	})
	t.Run("empty output", func(t *testing.T) {
		prog := uaScript(t, "xz", "exit 0")
		dst := filepath.Join(t.TempDir(), "g.usenet")
		if err := unpackXz(src, dst, prog, "/nonexistent/7z"); err == nil || !strings.Contains(err.Error(), "wrote nothing") {
			t.Errorf("err = %v, want an empty-output error", err)
		}
	})
}

func TestMboxUatUnpack7z(t *testing.T) {
	uaNeedProg(t, "7z")
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	content := []byte("unpacked 7z content\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	arc := filepath.Join(dir, "g.usenet.7z")
	ua7z(t, src, "g.usenet", arc)

	t.Run("entry", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "g.usenet")
		if err := unpack7z(arc, dst, "7z"); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(dst); err != nil || !bytes.Equal(got, content) {
			t.Errorf("unpacked %q, %v", got, err)
		}
	})
	t.Run("wrong entry", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "other.usenet")
		if err := unpack7z(arc, dst, "7z"); err == nil || !strings.Contains(err.Error(), "no entry") {
			t.Errorf("err = %v, want no entry", err)
		}
		if miExists(dst) {
			t.Error("output created for a missing entry")
		}
	})
	t.Run("empty entry", func(t *testing.T) {
		empty := filepath.Join(t.TempDir(), "empty")
		if err := os.WriteFile(empty, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		earc := filepath.Join(t.TempDir(), "e.usenet.7z")
		ua7z(t, empty, "e.usenet", earc)
		dst := filepath.Join(t.TempDir(), "e.usenet")
		if err := unpack7z(earc, dst, "7z"); err == nil || !strings.Contains(err.Error(), "wrote nothing") {
			t.Errorf("err = %v, want an empty-output error", err)
		}
	})
	t.Run("not 7z", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "src")
		if err := unpack7z(src, dst, "7z"); err == nil {
			t.Error("no error for a file that is not a 7z archive")
		}
	})
	t.Run("missing program", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "g.usenet")
		if err := unpack7z(arc, dst, "/nonexistent/7z"); err == nil {
			t.Error("no error without 7z")
		}
	})
	t.Run("extract fails", func(t *testing.T) {
		// lists the entry, then fails the extraction after writing something
		prog := uaScript(t, "7z", `if [ "$1" = l ]; then printf -- '----------\nPath = g.usenet\n'; exit 0; fi
echo partial; echo uat-crc-failed >&2; exit 2`)
		dst := filepath.Join(t.TempDir(), "g.usenet")
		if err := unpack7z(arc, dst, prog); err == nil || !strings.Contains(err.Error(), "uat-crc-failed") {
			t.Errorf("err = %v, want the stderr text", err)
		}
	})
}

func TestMboxUatDryRun(t *testing.T) {
	in := uaWrite(t, t.TempDir(), uaGroup, uaMessages())
	data := filepath.Join(t.TempDir(), "data")
	tmp := t.TempDir()

	var code int
	var sum *runSummary
	out := uaCaptureLog(t, func() { code, sum = miRun(t, "-data", data, "-tmpdir", tmp, "-dry-run", in) })
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	miCheck(t, sum, 4, 2, 0, 0, 0, "no-body=1,no-subject=1")
	if miExists(data) {
		t.Errorf("dry run created %s", data)
	}
	if !miExists(in) || miExists(in+".done") {
		t.Error("dry run renamed the input")
	}
	if n := uaEntries(t, tmp); n != 0 {
		t.Errorf("tmpdir has %d entries", n)
	}
	for _, want := range []string{
		fmt.Sprintf("[MBOX-IMPORT] uat file=%q group=%q version=3 messages=4", in, uaGroup),
		fmt.Sprintf("[MBOX-IMPORT] dry-run group=%q articles=1", uaGroup),
		fmt.Sprintf("[MBOX-IMPORT] dry-run group=%q articles=1", uaOther),
		fmt.Sprintf("[MBOX-IMPORT] file=%q read=4 stored=2 present=0 skipped=2 (no-body=1,no-subject=1) rejected=0 errors=0 parser-skipped=0 near-misses=0 stopped-early=false unread=false marked-done=false", in),
		fmt.Sprintf("[MBOX-IMPORT] uat file=%q re-encoded-fields=1 added-mime-version=4 raw-fields=-", in),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
	}
}

// uaExpectedSubject is the Subject buildArticle gives the Polish article after uat.Fix.
func uaExpectedSubject(t *testing.T) string {
	t.Helper()
	head, body, _ := uat.Fix([]byte(uaMessages()[0]))
	newHead, reason := rewriteHead(uaIDPolish, head)
	if reason != skipNone {
		t.Fatalf("rewriteHead: %v", reason)
	}
	art, err := buildArticle(uaIDPolish, newHead, removeFinalEmptyLines(body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(art.Subject, "=?") || strings.Contains(art.Subject, "ż") {
		t.Fatalf("Subject %q is not an encoded-word", art.Subject)
	}
	return art.Subject
}

// TestMboxUatImport: a real run files the articles into their first groups and renames the
// package, a rerun finds them present, and .usenet.xz / .usenet.7z copies of the package are
// unpacked into -tmpdir (emptied again), found present and renamed themselves.
func TestMboxUatImport(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	dir := t.TempDir()
	in := uaWrite(t, dir, uaGroup, uaMessages())
	data := filepath.Join(t.TempDir(), "data")
	tmp := t.TempDir()
	wantSubject := uaExpectedSubject(t)

	var code int
	var sum *runSummary
	out := uaCaptureLog(t, func() { code, sum = miRun(t, "-data", data, "-tmpdir", tmp, in) })
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	miCheck(t, sum, 4, 2, 0, 0, 0, "no-body=1,no-subject=1")
	if miExists(in) || !miExists(in+".done") || sum.MarkedDone != 1 {
		t.Fatalf("package not renamed to .done (marked %d)", sum.MarkedDone)
	}
	if !strings.Contains(out, fmt.Sprintf("[MBOX-IMPORT] uat file=%q re-encoded-fields=1 added-mime-version=4 raw-fields=-", in)) {
		t.Errorf("log lacks the uat summary line\n%s", out)
	}
	miWithDB(t, data, func(db *database.Database) {
		// miCheckGroups inline: one database open less
		want := map[string]string{uaIDPolish: uaGroup, uaIDCross: uaOther}
		for _, group := range []string{uaGroup, uaOther} {
			if _, err := db.MainDBGetNewsgroup(group); err != nil {
				t.Errorf("group %s: %v", group, err)
				continue
			}
			for _, id := range uaIDs {
				if got := miStoredIn(t, db, group, id); got != (want[id] == group) {
					t.Errorf("%s in %s = %t", id, group, got)
				}
			}
		}
		g, err := db.GetGroupDB(uaGroup)
		if err != nil {
			t.Fatalf("GetGroupDB: %v", err)
		}
		defer g.Return()
		a, err := db.GetArticleByMessageID(g, uaIDPolish)
		if err != nil {
			t.Fatalf("GetArticleByMessageID: %v", err)
		}
		if a.Subject != wantSubject {
			t.Errorf("stored Subject %q, want %q", a.Subject, wantSubject)
		}
		for _, want := range []string{"MIME-Version: 1.0", "Content-Type: text/plain; charset=UTF-8", "Content-Transfer-Encoding: 8bit"} {
			if !strings.Contains(a.HeadersJSON, want) {
				t.Errorf("stored headers lack %q:\n%s", want, a.HeadersJSON)
			}
		}
	})
	miCheckHistory(t, data, uaIDPolish, uaIDCross)

	// the renamed package again: everything present, the name is kept
	code, sum = miRun(t, "-data", data, "-tmpdir", tmp, in+".done")
	if code != 0 {
		t.Fatalf("rerun exit %d", code)
	}
	miCheck(t, sum, 4, 0, 2, 0, 0, "no-body=1,no-subject=1")
	if !miExists(in+".done") || miExists(in+".done.done") || sum.MarkedDone != 0 {
		t.Errorf("rerun renamed the .done package (marked %d)", sum.MarkedDone)
	}

	t.Run("xz and 7z", func(t *testing.T) {
		uaNeedProg(t, "xz")
		uaNeedProg(t, "7z")
		xzIn := filepath.Join(dir, "copy1.usenet.xz")
		uaXz(t, in+".done", xzIn)
		sevenIn := filepath.Join(dir, "copy2.usenet.7z")
		ua7z(t, in+".done", "copy2.usenet", sevenIn)

		code, sum := miRun(t, "-data", data, "-tmpdir", tmp, xzIn, sevenIn)
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if len(sum.PerInput) != 2 || sum.MarkedDone != 2 {
			t.Fatalf("inputs=%d marked=%d", len(sum.PerInput), sum.MarkedDone)
		}
		for _, c := range sum.PerInput {
			if c.Read != 4 || c.Stored != 0 || c.Present != 2 || c.skippedDetail() != "no-body=1,no-subject=1" {
				t.Errorf("%s: read=%d stored=%d present=%d skipped=%s", c.File, c.Read, c.Stored, c.Present, c.skippedDetail())
			}
		}
		for _, p := range []string{xzIn, sevenIn} {
			if miExists(p) || !miExists(p+".done") {
				t.Errorf("%s not renamed to .done", p)
			}
		}
		if n := uaEntries(t, tmp); n != 0 {
			t.Errorf("tmpdir has %d entries after the run", n)
		}
	})
}

// TestMboxUatForcedGroup: -group files everything importable into one group; a yEnc
// article is skipped as binary.
func TestMboxUatForcedGroup(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	const forced = "forced.uat"
	const idBin = "<bin@uat.test.invalid>"
	msgs := append(uaMessages(), uaMessage(idBin, uaGroup, "binary", "=ybegin line=128 size=3 name=a.bin\nxyz\n=yend size=3\n"))
	in := uaWrite(t, t.TempDir(), uaGroup, msgs)
	data := filepath.Join(t.TempDir(), "data")

	code, sum := miRun(t, "-data", data, "-tmpdir", t.TempDir(), "-group", forced, in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 5, 2, 0, 0, 0, "no-body=1,binary=1,no-subject=1")
	miCheckGroups(t, data, map[string][]string{forced: {uaIDPolish, uaIDCross}}, append(uaIDs, idBin), uaGroup, uaOther)
}

// TestMboxUatMissingZstd: without the zstd program the input is an error and nothing is
// unpacked (a dry run: importUAT runs the same way without a database).
func TestMboxUatMissingZstd(t *testing.T) {
	uaNeedProg(t, "xz")
	dir := t.TempDir()
	in := uaWrite(t, dir, uaGroup, uaMessages())
	xzIn := filepath.Join(dir, "copy1.usenet.xz")
	uaXz(t, in, xzIn)
	tmp := t.TempDir()

	code, sum := miRun(t, "-dry-run", "-tmpdir", tmp, "-zstd", "/nonexistent/zstd", xzIn)
	if code != 1 || sum == nil || sum.InputErrors != 1 || sum.Total.Read != 0 {
		t.Fatalf("exit %d summary %+v", code, sum)
	}
	if !miExists(xzIn) || miExists(xzIn+".done") {
		t.Error("input renamed")
	}
	if n := uaEntries(t, tmp); n != 0 {
		t.Errorf("tmpdir has %d entries", n)
	}
}

// uaInput is the package as .usenet.xz when xz is installed (to exercise the unpack
// directory), else the plain .usenet.
func uaInput(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	in := uaWrite(t, dir, uaGroup, uaMessages())
	if _, err := exec.LookPath("xz"); err != nil {
		return in
	}
	xzIn := filepath.Join(dir, "copy1.usenet.xz")
	uaXz(t, in, xzIn)
	return xzIn
}

// TestMboxUatStopped: a stop closed before the package's first message reads nothing, a
// stop during the package finishes the current article; neither renames the archive and
// the unpack directory is removed.
func TestMboxUatStopped(t *testing.T) {
	t.Run("pre-closed", func(t *testing.T) {
		in := uaInput(t)
		tmp := t.TempDir()
		stop := make(chan struct{})
		close(stop)
		c, inputErr, stopped := importUAT(in, &runOptions{zstd: "zstd", xz: "xz", sevenZip: "7z", tmpDir: tmp, markDone: true}, newCountingSink(), stop)
		res := &runSummary{Stopped: stopped, Total: newCounters("")}
		if c == nil || !stopped || inputErr || c.Read != 0 || res.exitCode() != 130 {
			t.Fatalf("counters %+v input-error=%t stopped=%t", c, inputErr, stopped)
		}
		if !miExists(in) || miExists(in+".done") {
			t.Error("input renamed after the stop")
		}
		if n := uaEntries(t, tmp); n != 0 {
			t.Errorf("tmpdir has %d entries", n)
		}
	})
	t.Run("during", func(t *testing.T) {
		in := uaInput(t)
		tmp := t.TempDir()
		stop := make(chan struct{})
		sink := &stopSink{countingSink: newCountingSink(), stop: stop}
		res := importInputs([]string{in}, &runOptions{zstd: "zstd", xz: "xz", sevenZip: "7z", tmpDir: tmp, markDone: true}, sink, stop)
		if !res.Stopped || res.exitCode() != 130 || res.Total.Read != 1 || res.Total.Stored != 1 {
			t.Errorf("stopped=%t exit=%d read=%d stored=%d", res.Stopped, res.exitCode(), res.Total.Read, res.Total.Stored)
		}
		if !miExists(in) || miExists(in+".done") {
			t.Error("input renamed after the stop")
		}
		if n := uaEntries(t, tmp); n != 0 {
			t.Errorf("tmpdir has %d entries", n)
		}
	})
}

// TestMboxUatKilledBySignal: an unpack program or zstd failing while stop is closed (Ctrl-C at
// a terminal kills them too, same process group) ends the input as stopped, not as an input error.
func TestMboxUatKilledBySignal(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	failing := uaScript(t, "prog", "cat >/dev/null; echo uat-killed >&2; exit 1")
	t.Run("zstd", func(t *testing.T) {
		in := uaWrite(t, t.TempDir(), uaGroup, uaMessages())
		c, inputErr, stopped := importUAT(in, &runOptions{zstd: failing, tmpDir: t.TempDir(), markDone: true}, newCountingSink(), stop)
		res := &runSummary{Stopped: stopped, Total: newCounters("")}
		if inputErr {
			res.InputErrors++
		}
		if c == nil || !stopped || inputErr || c.StoppedEarly || res.exitCode() != 130 {
			t.Fatalf("counters %+v input-error=%t stopped=%t exit=%d", c, inputErr, stopped, res.exitCode())
		}
		if !miExists(in) || miExists(in+".done") {
			t.Error("input renamed")
		}
	})
	t.Run("unpack", func(t *testing.T) {
		in := uaWrite(t, t.TempDir(), uaGroup, uaMessages())
		xzIn := in + ".xz" // the content does not matter: the fake xz fails
		if err := os.Rename(in, xzIn); err != nil {
			t.Fatal(err)
		}
		tmp := t.TempDir()
		c, inputErr, stopped := importUAT(xzIn, &runOptions{zstd: "zstd", xz: failing, sevenZip: "/nonexistent/7z", tmpDir: tmp, markDone: true}, newCountingSink(), stop)
		if c != nil || !stopped || inputErr {
			t.Fatalf("counters %+v input-error=%t stopped=%t", c, inputErr, stopped)
		}
		if !miExists(xzIn) || miExists(xzIn+".done") {
			t.Error("input renamed")
		}
		if n := uaEntries(t, tmp); n != 0 {
			t.Errorf("tmpdir has %d entries", n)
		}
	})
}

// TestMboxUatZstdFailure: a failing zstd is an input error that blocks the rename.
func TestMboxUatZstdFailure(t *testing.T) {
	in := uaWrite(t, t.TempDir(), uaGroup, uaMessages())
	zstd := uaScript(t, "zstd", "cat >/dev/null; echo uat-zstd-broken >&2; exit 3")
	res := importInputs([]string{in}, &runOptions{zstd: zstd, tmpDir: t.TempDir(), markDone: true}, newCountingSink(), nil)
	if res.InputErrors != 1 || res.exitCode() != 1 || len(res.PerInput) != 1 || !res.PerInput[0].StoppedEarly {
		t.Fatalf("input-errors=%d exit=%d per-input=%+v", res.InputErrors, res.exitCode(), res.PerInput)
	}
	if !miExists(in) || miExists(in+".done") {
		t.Error("input renamed after a zstd failure")
	}
}

// TestMboxUatWriteFixture writes a 3-message package to $MBOX_IMPORTER_UAT_FIXTURE for the
// end-to-end check (group t.uat.fixture: a Polish subject, a crosspost whose first group is
// t.uat.other, a plain article). Skipped unless the variable is set.
func TestMboxUatWriteFixture(t *testing.T) {
	dst := os.Getenv("MBOX_IMPORTER_UAT_FIXTURE")
	if dst == "" {
		t.Skip("MBOX_IMPORTER_UAT_FIXTURE not set")
	}
	const group = "t.uat.fixture"
	p := uaWrite(t, t.TempDir(), group, []string{
		uaMessage("<fixture-pl@uat.test.invalid>", group, uaPolishSub, "Treść wiadomości: zażółć.\n"),
		uaMessage("<fixture-cross@uat.test.invalid>", uaOther+","+group, "Re: crosspost", "crossposted body\n"),
		uaMessage("<fixture-plain@uat.test.invalid>", group, "plain", "plain body\n"),
	})
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", dst)
}
