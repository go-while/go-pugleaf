package processor

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-while/go-pugleaf/internal/nntp"
	_ "github.com/mattn/go-sqlite3"
)

// A-tools-15: the spool article text is wireformat (CRLF or LF, dot-stuffed, final ".").
func TestAdfOptRslightParseArticleText(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		body  string
		lines int // body lines
	}{
		{"LF dot-stuffed", "Subject: s\nFrom: a@b\n\nline1\n..dotline\n.\n", "line1\n.dotline", 2},
		{"CRLF wireformat", "Subject: s\r\nFrom: a@b\r\n\r\nline1\r\n..dotline\r\n.\r\n", "line1\n.dotline", 2},
		{"LF no terminator", "Subject: s\nFrom: a@b\n\nline1\n..dotline\n", "line1\n.dotline", 2},
		{"CRLF no final line end", "Subject: s\r\nFrom: a@b\r\n\r\nline1\r\n..dotline", "line1\n.dotline", 2},
		{"terminator without line end", "Subject: s\n\nline1\n.", "line1", 1},
		{"stuffed dot line last", "Subject: s\n\nline1\n..\n.\n", "line1\n.", 2},
		{"trailing empty body line kept", "Subject: s\n\nline1\n\n.\n", "line1\n", 2},
		{"empty body with terminator", "Subject: s\n\n.\n", "", 0},
		{"empty body no terminator", "Subject: s\n\n", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, _ := parseArticleTextToSlice(tc.text)
			for i, l := range lines {
				if strings.Contains(l, "\r") {
					t.Fatalf("line %d %q keeps a CR", i, l)
				}
			}
			art, err := nntp.ParseLegacyArticleLines("<adf-opt-rslight@test>", lines, true)
			if err != nil {
				t.Fatalf("ParseLegacyArticleLines(%q): %v", lines, err)
			}
			if art.Subject != "s" {
				t.Errorf("subject = %q, want %q", art.Subject, "s")
			}
			if art.BodyText != tc.body {
				t.Errorf("body = %q, want %q (lines %q)", art.BodyText, tc.body, lines)
			}
			if art.Lines != tc.lines {
				t.Errorf("Lines = %d, want %d", art.Lines, tc.lines)
			}
		})
	}
}

// A-tools-16: an empty article is classified as empty, before the processor is touched.
func TestAdfOptRslightEmptyArticle(t *testing.T) {
	leg := &LegacyImporter{}
	for _, text := range []string{"", "\n", "\r\n", "  \r\n"} {
		err := leg.importLegacyArticle(&LegacyArticle{MsgID: "<e@test>", Article: text})
		if classifyLegacyImportErr(err) != legacySkippedEmpty {
			t.Errorf("article %q: err %v, want the empty-article error", text, err)
		}
	}
}

func TestAdfOptRslightClassify(t *testing.T) {
	cases := []struct {
		err  error
		want legacyArticleOutcome
	}{
		{nil, legacyImported},
		{error_empty_article, legacySkippedEmpty},
		{fmt.Errorf("article '<x>': %w", errLegacyDuplicate), legacyDuplicate},
		{errors.New("failed to process article"), legacyFailed},
	}
	for _, tc := range cases {
		if got := classifyLegacyImportErr(tc.err); got != tc.want {
			t.Errorf("classify(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// A-tools-16: every worker has its own error; failures are aggregated and returned.
func TestAdfOptRslightImportFilesErrors(t *testing.T) {
	errBoom := errors.New("boom")
	for _, threads := range []int{0, 1, 4, 64} {
		t.Run(fmt.Sprintf("threads=%d", threads), func(t *testing.T) {
			var files []string
			for i := 0; i < 40; i++ {
				files = append(files, fmt.Sprintf("g%02d-articles.db3", i))
			}
			var calls, running, maxRunning atomic.Int64
			failed, err := importLegacyFiles(files, threads, func(file string) error {
				calls.Add(1)
				n := running.Add(1)
				defer running.Add(-1)
				for {
					m := maxRunning.Load()
					if n <= m || maxRunning.CompareAndSwap(m, n) {
						break
					}
				}
				var i int
				if _, err := fmt.Sscanf(file, "g%02d", &i); err != nil {
					return err
				}
				if i%3 == 0 {
					return errBoom
				}
				return nil
			})
			if calls.Load() != int64(len(files)) {
				t.Fatalf("importFn called %d times, want %d", calls.Load(), len(files))
			}
			if failed != 14 {
				t.Errorf("failed = %d, want 14", failed)
			}
			if !errors.Is(err, errBoom) {
				t.Errorf("err = %v, want it to wrap errBoom", err)
			}
			if err != nil && !strings.Contains(err.Error(), "g39-articles.db3") {
				t.Errorf("err does not name a failed file: %v", err)
			}
			limit := int64(threads)
			if limit < 1 {
				limit = 1
			}
			if maxRunning.Load() > limit {
				t.Errorf("%d files ran at once, limit %d", maxRunning.Load(), limit)
			}
		})
	}
	failed, err := importLegacyFiles([]string{"a", "b"}, 2, func(string) error { return nil })
	if failed != 0 || err != nil {
		t.Errorf("all ok: failed=%d err=%v, want 0 and nil", failed, err)
	}
}

// adfOptRslightSpool writes a legacy RockSolid Light articles database with the given articles.
func adfOptRslightSpool(t *testing.T, path, group string, articles []string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE articles (id INTEGER PRIMARY KEY AUTOINCREMENT, newsgroup TEXT, number TEXT, msgid TEXT, date TEXT, name TEXT, subject TEXT, article TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i, a := range articles {
		if _, err := db.Exec(`INSERT INTO articles (newsgroup, msgid, article) VALUES (?, ?, ?)`, group, fmt.Sprintf("<%d@adf-opt-rslight>", i), a); err != nil {
			t.Fatal(err)
		}
	}
}

// A-tools-16: failed, empty and duplicate articles go to their own counters; groupsDone counts once.
func TestAdfOptRslightSQLiteCounters(t *testing.T) {
	const group = "adfopt.rslight.counters"
	path := filepath.Join(t.TempDir(), group+"-articles.db3")
	adfOptRslightSpool(t, path, group, []string{"ok", "empty", "dup", "fail", "ok", "dup"})

	leg := &LegacyImporter{proc: &Processor{LegacyCounter: NewCounter()}}
	err := leg.importSQLiteArticlesWith(path, func(a *LegacyArticle) error {
		if a.Newsgroup != group {
			return fmt.Errorf("newsgroup %q, want %q", a.Newsgroup, group)
		}
		switch a.Article {
		case "ok":
			return nil
		case "empty":
			return error_empty_article
		case "dup":
			return fmt.Errorf("article '%s': %w", a.MsgID, errLegacyDuplicate)
		default:
			return errors.New("process failed")
		}
	})
	if err != nil {
		t.Fatalf("importSQLiteArticlesWith: %v", err)
	}
	if leg.imported != 2 || leg.skipped != 1 || leg.skippedEmpty != 1 || leg.duplicates != 2 {
		t.Errorf("imported=%d failed=%d empty=%d dupes=%d, want 2 1 1 2", leg.imported, leg.skipped, leg.skippedEmpty, leg.duplicates)
	}
	if leg.groupsDone != 1 {
		t.Errorf("groupsDone = %d, want 1", leg.groupsDone)
	}
	if got := leg.proc.LegacyCounter.GetReset("articles:" + group); got != 2 {
		t.Errorf("legacy counter = %d, want 2", got)
	}

	if err := leg.importSQLiteArticlesWith(filepath.Join(t.TempDir(), "missing-articles.db3"), func(*LegacyArticle) error { return nil }); err == nil {
		t.Errorf("missing database: err = nil, want an error")
	}
}

// A-tools-16: ImportAllSQLiteDatabases reports an error when nothing could be imported.
func TestAdfOptRslightImportAllNothingToImport(t *testing.T) {
	saved := LegacyImportStartDelay
	LegacyImportStartDelay = 0
	t.Cleanup(func() { LegacyImportStartDelay = saved })

	leg := &LegacyImporter{}
	if err := leg.ImportAllSQLiteDatabases(t.TempDir(), 2); err == nil {
		t.Errorf("empty spool: err = nil, want an error")
	}

	dir := t.TempDir()
	// a space in the name fails isValidDatabaseFilename, so the file is skipped before any DB access
	if err := os.WriteFile(filepath.Join(dir, "bad name-articles.db3"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := leg.ImportAllSQLiteDatabases(dir, 2)
	if err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("all files skipped: err = %v, want a skipped error", err)
	}
}
