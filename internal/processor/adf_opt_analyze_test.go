package processor

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/nntp"
)

const adfOptDate = "Mon, 02 Jan 2006 15:04:05 +0000"

// adfOptCacheLine returns one cached overview line in the format analyzeFromRemote writes.
func adfOptCacheLine(num int64, msgID, refs string, bytes int) string {
	return strings.Join([]string{
		strconv.FormatInt(num, 10), "subj", "from@example.invalid", adfOptDate, msgID, refs, strconv.Itoa(bytes), "3",
	}, "\t")
}

// adfOptProc returns a Processor with a Pool literal (never dialed) and a cache root.
func adfOptProc(t *testing.T, host string) (*Processor, string) {
	t.Helper()
	root := t.TempDir()
	return &Processor{
		Pool:           &nntp.Pool{Backend: &nntp.BackendConfig{Host: host}},
		AnalyzeDataDir: root,
	}, root
}

func adfOptListTmp(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

// A-tools-7: the cache root comes from AnalyzeDataDir, else DB; never "data" in the cwd.
func TestAdfOptAnalyzeCachePathFromDataDir(t *testing.T) {
	root := t.TempDir()
	proc := &Processor{AnalyzeDataDir: root}
	got, err := proc.getCacheFilePath("prov", "alt.test")
	if err != nil {
		t.Fatalf("getCacheFilePath: %v", err)
	}
	if want := analyzeCachePath(root, "prov", "alt.test"); got != want {
		t.Fatalf("getCacheFilePath = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Fatalf("cache path %q is not below %q", got, root)
	}
}

// A-tools-8: one cache key derived from Backend.Host; unusable hosts are errors.
func TestAdfOptAnalyzeProviderName(t *testing.T) {
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		{"h.example:119", "h.example", true},
		{"h.example", "h.example", true},
		{"../x", "x", true},
		{"", "", false},
		{"..", "", false},
		{".", "", false},
		{"/", "", false},
		{":119", "", false},
	}
	for _, c := range cases {
		proc := &Processor{Pool: &nntp.Pool{Backend: &nntp.BackendConfig{Host: c.host}}}
		got, err := proc.analyzeProviderName()
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("host %q: got %q, %v; want %q", c.host, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("host %q: got %q, want error", c.host, got)
		}
	}
}

// A-tools-8: with a nil Pool or Backend every cache function returns an error, none panics.
func TestAdfOptAnalyzeNilPoolNoPanic(t *testing.T) {
	root := t.TempDir()
	for name, proc := range map[string]*Processor{
		"nil pool":    {AnalyzeDataDir: root},
		"nil backend": {Pool: &nntp.Pool{}, AnalyzeDataDir: root},
	} {
		if err := proc.ClearCache("alt.test"); err == nil {
			t.Errorf("%s: ClearCache: want error", name)
		}
		if _, err := proc.GetCachedMessageIDs("alt.test", 1, 10); err == nil {
			t.Errorf("%s: GetCachedMessageIDs: want error", name)
		}
		if _, err := proc.GetCacheStats("alt.test"); err == nil {
			t.Errorf("%s: GetCacheStats: want error", name)
		}
		if err := proc.ValidateCacheIntegrity("alt.test"); err == nil {
			t.Errorf("%s: ValidateCacheIntegrity: want error", name)
		}
		if _, err := proc.FindArticlesByDateRange("alt.test", time.Time{}, time.Now()); err == nil {
			t.Errorf("%s: FindArticlesByDateRange: want error", name)
		}
		if _, err := proc.GetArticleCountByDateRange("alt.test", time.Time{}, time.Now()); err == nil {
			t.Errorf("%s: GetArticleCountByDateRange: want error", name)
		}
		if _, err := proc.AnalyzeMode("alt.test", false, 0, "", "", "", false, true, false, root); err == nil {
			t.Errorf("%s: AnalyzeMode -clear-cache: want error", name)
		}
	}
}

// A-tools-8: a cache written where AnalyzeGroup writes it is found by every other cache function.
func TestAdfOptAnalyzeCacheKeyShared(t *testing.T) {
	proc, root := adfOptProc(t, "h.example:119")
	cacheFile := analyzeCachePath(root, "h.example", "alt.test")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o750); err != nil {
		t.Fatal(err)
	}
	content := adfOptCacheLine(1, "<a@x>", "", 100) + "\n" + adfOptCacheLine(2, "<b@x>", "<a@x>", 5000) + "\n"
	if err := os.WriteFile(cacheFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	stats, err := proc.GetCacheStats("alt.test")
	if err != nil {
		t.Fatalf("GetCacheStats: %v", err)
	}
	if stats.CachedArticles != 2 || stats.TotalBytes != 5100 || stats.ProviderName != "h.example" {
		t.Fatalf("GetCacheStats = %d articles, %d bytes, provider %q", stats.CachedArticles, stats.TotalBytes, stats.ProviderName)
	}
	if err := proc.ValidateCacheIntegrity("alt.test"); err != nil {
		t.Fatalf("ValidateCacheIntegrity: %v", err)
	}
	ids, err := proc.GetCachedMessageIDs("alt.test", 1, 2)
	if err != nil || len(ids) != 2 || ids[1].Value != "<b@x>" {
		t.Fatalf("GetCachedMessageIDs = %v, %v", ids, err)
	}
	from := time.Date(2006, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2006, 1, 3, 0, 0, 0, 0, time.UTC)
	dr, err := proc.FindArticlesByDateRange("alt.test", from, to)
	if err != nil || dr.ArticleCount != 2 || dr.FirstArticleNum != 1 || dr.LastArticleNum != 2 {
		t.Fatalf("FindArticlesByDateRange = %+v, %v", dr, err)
	}
	n, err := proc.GetArticleCountByDateRange("alt.test", from, to)
	if err != nil || n != 2 {
		t.Fatalf("GetArticleCountByDateRange = %d, %v", n, err)
	}

	if err := proc.ClearCache("alt.test"); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Fatalf("cache file still there after ClearCache: %v", err)
	}
	if err := proc.ClearCache("alt.test"); err != nil {
		t.Fatalf("ClearCache without a cache: %v", err)
	}
}

// A-tools-9: analyzeFromCache takes lines over 64 KiB and reports lines over the cap.
func TestAdfOptAnalyzeFromCacheLongLines(t *testing.T) {
	proc := &Processor{}
	dir := t.TempDir()

	long := filepath.Join(dir, "long.overview")
	refs := strings.Repeat("<r@x> ", 20000) // ~120 KiB, over bufio.Scanner's 64 KiB default
	content := adfOptCacheLine(1, "<a@x>", refs, 10) + "\n" + adfOptCacheLine(2, "<b@x>", "", 20) + "\n"
	if err := os.WriteFile(long, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := proc.analyzeFromCache(long, &GroupAnalysis{GroupName: "alt.test"}, &AnalyzeOptions{})
	if err != nil {
		t.Fatalf("analyzeFromCache with a 120 KiB line: %v", err)
	}
	if got.CachedArticles != 2 || got.TotalBytes != 30 {
		t.Fatalf("analyzeFromCache = %d articles, %d bytes; want 2, 30", got.CachedArticles, got.TotalBytes)
	}

	tooLong := filepath.Join(dir, "toolong.overview")
	huge := strings.Repeat("x", analyzeCacheMaxLine+1)
	content = adfOptCacheLine(1, "<a@x>", "", 10) + "\n" + adfOptCacheLine(2, "<b@x>", huge, 20) + "\n"
	if err := os.WriteFile(tooLong, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := proc.analyzeFromCache(tooLong, &GroupAnalysis{GroupName: "alt.test"}, &AnalyzeOptions{}); err == nil {
		t.Fatalf("analyzeFromCache with a line over the cap: want error, got %d articles", got.CachedArticles)
	}
}

// A-tools-9: the batch loop covers the range exactly and terminates at MaxInt64.
func TestAdfOptAnalyzeBatchRanges(t *testing.T) {
	collect := func(first, last, size int64) [][2]int64 {
		var got [][2]int64
		err := forEachAnalyzeBatch(first, last, size, func(s, e int64) error {
			got = append(got, [2]int64{s, e})
			if len(got) > 100 {
				t.Fatalf("batch loop does not terminate: %v", got[:5])
			}
			return nil
		})
		if err != nil {
			t.Fatalf("forEachAnalyzeBatch(%d,%d,%d): %v", first, last, size, err)
		}
		return got
	}
	if got := collect(1, 0, 10); len(got) != 0 {
		t.Fatalf("empty range gave batches %v", got)
	}
	if got := collect(1, 10, 10); len(got) != 1 || got[0] != [2]int64{1, 10} {
		t.Fatalf("1..10 = %v", got)
	}
	if got := collect(1, 25, 10); len(got) != 3 || got[2] != [2]int64{21, 25} {
		t.Fatalf("1..25 = %v", got)
	}
	got := collect(math.MaxInt64-24, math.MaxInt64, 10)
	if len(got) != 3 || got[0][0] != math.MaxInt64-24 || got[2][1] != math.MaxInt64 {
		t.Fatalf("range ending at MaxInt64 = %v", got)
	}
	if got := collect(math.MaxInt64, math.MaxInt64, AnalyzeBatchSize); len(got) != 1 {
		t.Fatalf("single article at MaxInt64 = %v", got)
	}

	calls := 0
	if err := forEachAnalyzeBatch(1, 100, 10, func(s, e int64) error { calls++; return errAnalyzeStop }); err != nil || calls != 1 {
		t.Fatalf("errAnalyzeStop: err=%v calls=%d", err, calls)
	}
	boom := errors.New("boom")
	if err := forEachAnalyzeBatch(1, 100, 10, func(s, e int64) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("batch error = %v, want boom", err)
	}
	if err := forEachAnalyzeBatch(1, 100, 0, func(s, e int64) error { return nil }); err == nil {
		t.Fatal("batch size 0: want error")
	}
}

// A-tools-9: GROUP ranges with negative or absurd numbers are rejected; empty groups pass.
func TestAdfOptAnalyzeValidateRange(t *testing.T) {
	for _, c := range []struct {
		first, last int64
		ok          bool
	}{
		{1, 100, true},
		{1, 0, true}, // empty group
		{0, 0, true},
		{-1, 10, false},
		{1, -1, false},
		{0, math.MaxInt64, false},
		{1, 1 + maxAnalyzeRange, false},
	} {
		err := validateAnalyzeRange(c.first, c.last)
		if (err == nil) != c.ok {
			t.Errorf("validateAnalyzeRange(%d,%d) = %v, want ok=%v", c.first, c.last, err, c.ok)
		}
	}
}

// A-tools-9: the cache writer replaces the cache only on Commit and leaves no temp file.
func TestAdfOptAnalyzeCacheWriter(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "cache", "prov", "g.overview")

	cw, err := newAnalyzeCacheWriter(final)
	if err != nil {
		t.Fatalf("newAnalyzeCacheWriter: %v", err)
	}
	if err := cw.WriteLine("one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("final file exists before Commit: %v", err)
	}
	if err := cw.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	cw.Abort() // no-op after Commit
	if b, err := os.ReadFile(final); err != nil || string(b) != "one\n" {
		t.Fatalf("after Commit: %q, %v", b, err)
	}

	// Abort keeps the old cache.
	cw, err = newAnalyzeCacheWriter(final)
	if err != nil {
		t.Fatal(err)
	}
	if err := cw.WriteLine("partial"); err != nil {
		t.Fatal(err)
	}
	cw.Abort()
	if b, err := os.ReadFile(final); err != nil || string(b) != "one\n" {
		t.Fatalf("after Abort: %q, %v", b, err)
	}
	if tmp := adfOptListTmp(t, filepath.Dir(final)); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}

	// A failing rename (target is a non-empty directory) returns an error and cleans up.
	blocked := filepath.Join(dir, "blocked.overview")
	if err := os.MkdirAll(filepath.Join(blocked, "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	cw, err = newAnalyzeCacheWriter(blocked)
	if err != nil {
		t.Fatal(err)
	}
	if err := cw.WriteLine("x"); err != nil {
		t.Fatal(err)
	}
	if err := cw.Commit(); err == nil {
		t.Fatal("Commit over a directory: want error")
	}
	if tmp := adfOptListTmp(t, dir); len(tmp) != 0 {
		t.Fatalf("temp files left after failed Commit: %v", tmp)
	}
}

// A-tools-9: a failed XOVER batch fails the analysis and the old cache stays untouched.
// The pool is closed, so XOver fails before any dial.
func TestAdfOptAnalyzeRemoteBatchErrorKeepsCache(t *testing.T) {
	root := t.TempDir()
	pool := nntp.NewPool(&nntp.BackendConfig{Host: "h.example", Port: 119, MaxConns: 1})
	if err := pool.ClosePool(); err != nil {
		t.Fatalf("ClosePool: %v", err)
	}
	proc := &Processor{Pool: pool, AnalyzeDataDir: root}
	_, cacheFile, err := proc.analyzeCacheFile("alt.test")
	if err != nil {
		t.Fatal(err)
	}
	old := adfOptCacheLine(1, "<a@x>", "", 10) + "\n"
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, opts := range []*AnalyzeOptions{{ForceRefresh: true}, {ForceRefresh: true, MaxArticles: 5}} {
		analysis := &GroupAnalysis{GroupName: "alt.test", ProviderName: "h.example", SizeStats: &ArticleSizeStats{}}
		got, err := proc.analyzeFromRemote(&nntp.GroupInfo{First: 1, Last: 5}, cacheFile, analysis, opts)
		if err == nil {
			t.Fatalf("analyzeFromRemote with a failing batch (max %d): want error, got %+v", opts.MaxArticles, got)
		}
		if b, rerr := os.ReadFile(cacheFile); rerr != nil || string(b) != old {
			t.Fatalf("cache changed after a failed run: %q, %v", b, rerr)
		}
		if tmp := adfOptListTmp(t, filepath.Dir(cacheFile)); len(tmp) != 0 {
			t.Fatalf("temp files left: %v", tmp)
		}
	}
}

// A-tools-10: JSON and CSV exports are well-formed for names with quotes and commas.
func TestAdfOptAnalyzeExports(t *testing.T) {
	a := &GroupAnalysis{
		GroupName:      `a"b,c`,
		ProviderName:   "p\\q",
		TotalArticles:  3,
		TotalBytes:     42,
		CacheExists:    true,
		CachedArticles: 3,
		AnalyzedAt:     time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC),
	}
	data, err := a.ExportAnalysisToJSON()
	if err != nil {
		t.Fatalf("ExportAnalysisToJSON: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("JSON export is not valid JSON:\n%s", data)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back["group_name"] != a.GroupName || back["provider_name"] != a.ProviderName ||
		back["cached_articles"] != float64(3) || back["cache_exists"] != true ||
		back["analyzed_at"] != "2024-05-06T07:08:09Z" {
		t.Fatalf("JSON round trip: %v", back)
	}
	if len(back) != 11 {
		t.Fatalf("JSON has %d keys, want 11: %v", len(back), back)
	}

	rec, err := csv.NewReader(strings.NewReader(a.ExportAnalysisToCSV())).ReadAll()
	if err != nil {
		t.Fatalf("CSV export does not parse: %v", err)
	}
	if len(rec) != 1 || len(rec[0]) != 11 || rec[0][0] != a.GroupName || rec[0][1] != a.ProviderName || rec[0][10] != "3" {
		t.Fatalf("CSV export = %q", rec)
	}
	hdr, err := csv.NewReader(strings.NewReader(GetAnalysisCSVHeader())).ReadAll()
	if err != nil || len(hdr) != 1 || len(hdr[0]) != 11 || hdr[0][0] != "group_name" {
		t.Fatalf("CSV header = %q, %v", hdr, err)
	}
}

// A-tools-11: exports go to outDir only; names that are not newsgroup names are not
// exported (warning), but the result is still printed without an error.
func TestAdfOptAnalyzePrintResultsOutDir(t *testing.T) {
	base := t.TempDir()
	outDir := filepath.Join(base, "nested", "out")

	for _, name := range []string{"../../escape", "a/b", "..", "a..b", ""} {
		for _, format := range []string{"json", "csv"} {
			err := printAnalysisResults(&GroupAnalysis{GroupName: name}, format, outDir)
			if err != nil {
				t.Errorf("printAnalysisResults(%q, %s): want export skipped without error, got %v", name, format, err)
			}
		}
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatalf("outDir was created for skipped names: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(base, "*.json")); len(m) != 0 {
		t.Fatalf("files written outside outDir: %v", m)
	}

	a := &GroupAnalysis{GroupName: "de.rec.test", ProviderName: "p", SizeStats: &ArticleSizeStats{}}
	if err := printAnalysisResults(a, "json", outDir); err != nil {
		t.Fatalf("json export: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "de.rec.test.json"))
	if err != nil || !json.Valid(data) {
		t.Fatalf("json file: valid=%v err=%v", json.Valid(data), err)
	}
	if info, err := os.Stat(outDir); err != nil || info.Mode().Perm()&^0o750 != 0 {
		t.Fatalf("outDir mode: %v, %v", info, err)
	}

	for i := 0; i < 2; i++ {
		if err := printAnalysisResults(a, "csv", outDir); err != nil {
			t.Fatalf("csv export: %v", err)
		}
	}
	f, err := os.Open(filepath.Join(outDir, "de.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][0] != "group_name" || rows[2][0] != "de.rec.test" {
		t.Fatalf("csv file rows = %q, %v", rows, err)
	}

	// No export: nothing is created.
	other := filepath.Join(base, "none")
	if err := printAnalysisResults(a, "", other); err != nil {
		t.Fatalf("plain print: %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("outDir created without an export: %v", err)
	}
}

// A stale temp file of the same cache file (from a killed run) is removed when a new
// writer starts; temp files of other cache files stay.
func TestAdfOptAnalyzeCacheWriterStaleTmp(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "g.overview")
	stale := final + ".12345.tmp"
	other := filepath.Join(dir, "h.overview.999.tmp")
	for _, f := range []string{stale, other} {
		if err := os.WriteFile(f, []byte("partial\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cw, err := newAnalyzeCacheWriter(final)
	if err != nil {
		t.Fatalf("newAnalyzeCacheWriter: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("temp file of another cache was removed: %v", err)
	}
	if err := cw.WriteLine("x"); err != nil {
		t.Fatal(err)
	}
	if err := cw.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if tmp := adfOptListTmp(t, dir); len(tmp) != 1 || tmp[0] != other {
		t.Fatalf("temp files after Commit = %v, want only %s", tmp, other)
	}
}

// A 423 reply (nntp.ErrNoArticlesInRange) for one batch is an empty batch: the analysis
// goes on and commits the cache. Any other batch error still fails it.
func TestAdfOptAnalyzeRemoteNoArticlesInRange(t *testing.T) {
	saved := analyzeXOver
	t.Cleanup(func() { analyzeXOver = saved })

	first, last := int64(1), int64(AnalyzeBatchSize+3) // two batches
	var calls [][2]int64
	analyzeXOver = func(_ *nntp.Pool, group string, start, end int64, _ bool) ([]nntp.OverviewLine, error) {
		calls = append(calls, [2]int64{start, end})
		if start == first {
			// same wrapping as BackendConn.XOver for a 423 reply
			return nil, fmt.Errorf("failed to read XOVER response: %w: %w",
				&textproto.Error{Code: 423, Msg: "No articles in that range"}, nntp.ErrNoArticlesInRange)
		}
		return []nntp.OverviewLine{
			{ArticleNum: end - 1, Subject: "s1", From: "f", Date: adfOptDate, MessageID: "<1@x>", Bytes: 100, Lines: 2},
			{ArticleNum: end, Subject: "s2", From: "f", Date: adfOptDate, MessageID: "<2@x>", References: "<1@x>", Bytes: 200, Lines: 3},
		}, nil
	}

	proc, _ := adfOptProc(t, "h.example")
	_, cacheFile, err := proc.analyzeCacheFile("alt.test")
	if err != nil {
		t.Fatal(err)
	}
	analysis := &GroupAnalysis{GroupName: "alt.test", ProviderName: "h.example", SizeStats: &ArticleSizeStats{}}
	got, err := proc.analyzeFromRemote(&nntp.GroupInfo{First: first, Last: last}, cacheFile, analysis, &AnalyzeOptions{})
	if err != nil {
		t.Fatalf("analyzeFromRemote with a 423 batch: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("XOver calls = %v, want 2 batches", calls)
	}
	if got.CachedArticles != 2 || got.TotalBytes != 300 || !got.CacheExists {
		t.Fatalf("analysis = %d articles, %d bytes, cache=%v", got.CachedArticles, got.TotalBytes, got.CacheExists)
	}
	stats, err := proc.GetCacheStats("alt.test")
	if err != nil || stats.CachedArticles != 2 {
		t.Fatalf("committed cache: %+v, %v", stats, err)
	}

	// Any other error still fails the analysis and leaves the committed cache alone.
	before, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatal(err)
	}
	analyzeXOver = func(_ *nntp.Pool, _ string, _, _ int64, _ bool) ([]nntp.OverviewLine, error) {
		return nil, fmt.Errorf("failed to read XOVER response: %w", &textproto.Error{Code: 503, Msg: "timeout"})
	}
	analysis = &GroupAnalysis{GroupName: "alt.test", ProviderName: "h.example", SizeStats: &ArticleSizeStats{}}
	if _, err := proc.analyzeFromRemote(&nntp.GroupInfo{First: first, Last: last}, cacheFile, analysis, &AnalyzeOptions{}); err == nil {
		t.Fatal("analyzeFromRemote with a 503 batch: want error")
	}
	if after, err := os.ReadFile(cacheFile); err != nil || string(after) != string(before) {
		t.Fatalf("cache changed after a failed run: %v", err)
	}
}
