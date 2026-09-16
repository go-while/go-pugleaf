package database

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// w2DBPerfPlan returns the EXPLAIN QUERY PLAN details of q joined with " | ".
func w2DBPerfPlan(t *testing.T, db *sql.DB, q string, args ...interface{}) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain %q: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, " | ")
}

// w2DBPerfIndexes returns the names of all indexes in db.
func w2DBPerfIndexes(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'index'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func w2DBPerfGen[T any](c *ttlCache[T]) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.gen
}

func TestW2DBPerfTTLCacheBasics(t *testing.T) {
	var c ttlCache[[]int]
	var loads atomic.Int64
	load := func() ([]int, error) {
		n := loads.Add(1)
		return []int{int(n)}, nil
	}

	v, err := c.get(time.Hour, load)
	if err != nil || len(v) != 1 || v[0] != 1 {
		t.Fatalf("first get = %v, %v", v, err)
	}
	v, err = c.get(time.Hour, load)
	if err != nil || v[0] != 1 || loads.Load() != 1 {
		t.Fatalf("second get within TTL = %v, %v (loads=%d), want cached 1", v, err, loads.Load())
	}

	c.invalidate()
	v, _ = c.get(time.Hour, load)
	if v[0] != 2 || loads.Load() != 2 {
		t.Fatalf("get after invalidate = %v (loads=%d), want reload 2", v, loads.Load())
	}

	// expiry
	time.Sleep(5 * time.Millisecond)
	v, _ = c.get(time.Millisecond, load)
	if v[0] != 3 {
		t.Fatalf("get after TTL = %v, want reload 3", v)
	}

	// a failed load is returned but not cached
	c.invalidate()
	boom := errors.New("boom")
	if _, err := c.get(time.Hour, func() ([]int, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("failing load err = %v", err)
	}
	v, err = c.get(time.Hour, load)
	if err != nil || v[0] != 4 {
		t.Fatalf("get after failed load = %v, %v, want fresh load 4", v, err)
	}
}

// A load that started before invalidate must not store its (stale) result.
func TestW2DBPerfTTLCacheInvalidateWinsOverInflightLoad(t *testing.T) {
	var c ttlCache[string]
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan string)
	go func() {
		v, _ := c.get(time.Hour, func() (string, error) {
			close(started)
			<-release
			return "stale", nil
		})
		done <- v
	}()
	<-started
	c.invalidate()
	close(release)
	if v := <-done; v != "stale" {
		t.Fatalf("in-flight caller got %q, want its own load result", v)
	}
	v, err := c.get(time.Hour, func() (string, error) { return "fresh", nil })
	if err != nil || v != "fresh" {
		t.Fatalf("get after invalidate = %q, %v; the in-flight load stored stale data", v, err)
	}
}

func TestW2DBPerfTTLCacheConcurrent(t *testing.T) {
	var c ttlCache[[]*int]
	var wg, running sync.WaitGroup
	var gets atomic.Int64
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		running.Add(1)
		go func() {
			defer wg.Done()
			first := true
			for {
				if !first {
					gets.Add(1)
				}
				select {
				case <-stop:
					return
				default:
				}
				v, err := c.get(time.Millisecond, func() ([]*int, error) {
					n := 1
					return []*int{&n}, nil
				})
				if err != nil || len(v) != 1 || *v[0] != 1 {
					t.Errorf("get = %v, %v", v, err)
					if first {
						running.Done()
					}
					return
				}
				_ = uiCacheCopy(v)
				if first {
					first = false
					running.Done()
				}
			}
		}()
	}
	// invalidate only while every reader is inside its loop, so gets and invalidations overlap
	running.Wait()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.invalidate()
		time.Sleep(50 * time.Microsecond)
	}
	close(stop)
	wg.Wait()
	if n := gets.Load(); n < 100 {
		t.Fatalf("readers did only %d gets while invalidating", n)
	}
}

func TestW2DBPerfSiteNewsCacheInvalidation(t *testing.T) {
	db := w0DB(t)
	if _, err := db.GetVisibleSiteNews(); err != nil { // prime the cache
		t.Fatal(err)
	}
	subject := w0Name("w2dbperf.news")
	news := &models.SiteNews{Subject: subject, Content: "c", DatePublished: time.Now(), IsVisible: true}
	if err := db.CreateSiteNews(news); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DeleteSiteNews(news.ID) })

	has := func() bool {
		t.Helper()
		list, err := db.GetVisibleSiteNews()
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range list {
			if n.ID == news.ID {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Fatal("created visible news missing right after CreateSiteNews")
	}

	// callers get a new slice: clobbering it must not affect the cache
	list, _ := db.GetVisibleSiteNews()
	for i := range list {
		list[i] = nil
	}
	if !has() {
		t.Fatal("modifying a returned slice changed the cached slice")
	}

	if err := db.ToggleSiteNewsVisibility(news.ID); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("hidden news still listed after ToggleSiteNewsVisibility")
	}
	news.IsVisible = true
	if err := db.UpdateSiteNews(news); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Fatal("news missing after UpdateSiteNews made it visible")
	}
	if err := db.DeleteSiteNews(news.ID); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("deleted news still listed")
	}
}

func TestW2DBPerfHeaderSectionsCacheInvalidation(t *testing.T) {
	db := w0DB(t)
	if _, err := db.GetHeaderSections(); err != nil {
		t.Fatal(err)
	}
	has := func(name string) bool {
		t.Helper()
		list, err := db.GetHeaderSections()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			if s.Name == name {
				return true
			}
		}
		return false
	}

	sec := &models.Section{Name: strings.ReplaceAll(w0Name("w2dbperfsec"), ".", ""), DisplayName: "w2", ShowInHeader: true}
	if err := db.CreateSection(sec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DeleteSection(sec.ID) })
	if !has(sec.Name) {
		t.Fatal("CreateSection: section missing from header sections")
	}

	sec.ShowInHeader = false
	if err := db.UpdateSection(sec); err != nil {
		t.Fatal(err)
	}
	if has(sec.Name) {
		t.Fatal("UpdateSection(show_in_header=false): still listed")
	}

	gen := w2DBPerfGen(&uiCacheHeaderSections)
	sg := &models.SectionGroup{SectionID: sec.ID, NewsgroupName: w0Name("w2dbperf.sg"), CreatedAt: time.Now()}
	if err := db.CreateSectionGroup(sg); err != nil {
		t.Fatal(err)
	}
	if w2DBPerfGen(&uiCacheHeaderSections) == gen {
		t.Fatal("CreateSectionGroup did not invalidate the header sections cache")
	}
	gen = w2DBPerfGen(&uiCacheHeaderSections)
	if err := db.DeleteSectionGroup(sg.ID); err != nil {
		t.Fatal(err)
	}
	if w2DBPerfGen(&uiCacheHeaderSections) == gen {
		t.Fatal("DeleteSectionGroup did not invalidate the header sections cache")
	}

	ins := &models.Section{Name: strings.ReplaceAll(w0Name("w2dbperfins"), ".", ""), DisplayName: "w2", ShowInHeader: true}
	if err := db.InsertSection(ins); err != nil {
		t.Fatal(err)
	}
	if !has(ins.Name) {
		t.Fatal("InsertSection: section missing from header sections")
	}
	got, err := db.GetSectionByName(ins.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSection(got.ID); err != nil {
		t.Fatal(err)
	}
	if has(ins.Name) {
		t.Fatal("DeleteSection: section still listed")
	}
}

func TestW2DBPerfAIModelsCacheInvalidation(t *testing.T) {
	db := w0DB(t)
	if _, err := db.GetActiveAIModels(); err != nil {
		t.Fatal(err)
	}
	has := func(id int) bool {
		t.Helper()
		list, err := db.GetActiveAIModels()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range list {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	key := strings.ReplaceAll(w0Name("w2dbperf_model"), ".", "_")
	m, err := db.CreateAIModel(key, "ollama", "W2", "d", true, false, 99)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DeleteAIModel(m.ID) })
	if !has(m.ID) {
		t.Fatal("CreateAIModel: model missing from active models")
	}
	if err := db.UpdateAIModel(m.ID, "ollama", "W2", "d", false, false, 99); err != nil {
		t.Fatal(err)
	}
	if has(m.ID) {
		t.Fatal("UpdateAIModel(inactive): still listed")
	}
	if err := db.UpdateAIModel(m.ID, "ollama", "W2", "d", true, false, 99); err != nil {
		t.Fatal(err)
	}
	if !has(m.ID) {
		t.Fatal("UpdateAIModel(active): missing")
	}
	gen := w2DBPerfGen(&uiCacheActiveAIModels)
	if err := db.SetDefaultAIModel(m.ID); err != nil {
		t.Fatal(err)
	}
	if w2DBPerfGen(&uiCacheActiveAIModels) == gen {
		t.Fatal("SetDefaultAIModel did not invalidate the active AI models cache")
	}
	list, _ := db.GetActiveAIModels()
	for _, x := range list {
		if x.ID == m.ID && !x.IsDefault {
			t.Fatal("SetDefaultAIModel: cached model not default")
		}
	}
	if err := db.DeleteAIModel(m.ID); err != nil {
		t.Fatal(err)
	}
	if has(m.ID) {
		t.Fatal("DeleteAIModel: still listed")
	}
}

func TestW2DBPerfEscapeLike(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"comp.lang", "comp.lang"},
		{"%", `\%`},
		{"_", `\_`},
		{`\`, `\\`},
		{`a%b_c\d`, `a\%b\_c\\d`},
		{`%%__`, `\%\%\_\_`},
		{"äö%", `äö\%`},
	} {
		if got := escapeLike(tc.in); got != tc.want {
			t.Errorf("escapeLike(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestW2DBPerfSearchLiteralWildcards(t *testing.T) {
	db := w0DB(t)
	suffix := strings.TrimPrefix(w0Name(""), ".")
	pct := "%w2dbperf." + suffix
	under := "w2dbperf_x." + suffix
	plain := "w2dbperfAx." + suffix
	for _, name := range []string{pct, under, plain} {
		if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Active: true, Status: "y", Description: "w2dbperf " + suffix}); err != nil {
			t.Fatal(err)
		}
	}

	names := func(term string, admin, desc bool) []string {
		t.Helper()
		groups, err := db.SearchNewsgroupsWithOptions(term, 10000, 0, admin, desc)
		if err != nil {
			t.Fatal(err)
		}
		count, err := db.CountSearchNewsgroupsWithOptions(term, desc, admin)
		if err != nil {
			t.Fatal(err)
		}
		if count != len(groups) {
			t.Fatalf("search %q admin=%v desc=%v: count %d != %d results", term, admin, desc, count, len(groups))
		}
		out := make([]string, 0, len(groups))
		for _, g := range groups {
			out = append(out, g.Name)
		}
		return out
	}
	contains := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}

	for _, admin := range []bool{false, true} {
		for _, desc := range []bool{false, true} {
			got := names("%", admin, desc)
			if !contains(got, pct) {
				t.Errorf("admin=%v desc=%v: %q not found for term %%", admin, desc, pct)
			}
			for _, n := range got {
				if !strings.HasPrefix(n, "%") {
					t.Errorf("admin=%v desc=%v: term %% matched %q", admin, desc, n)
				}
			}

			got = names("w2dbperf_", admin, desc)
			if !contains(got, under) || contains(got, plain) {
				t.Errorf("admin=%v desc=%v: term w2dbperf_ = %v, want %q and not %q", admin, desc, got, under, plain)
			}

			// still case-insensitive
			if got = names("W2DBPERFAX", admin, desc); !contains(got, plain) {
				t.Errorf("admin=%v desc=%v: upper-case term did not find %q", admin, desc, plain)
			}
		}
	}
}

func TestW2DBPerfSearchUsesNocaseIndex(t *testing.T) {
	m := w0DB(t).GetMainDB()
	idx := w2DBPerfIndexes(t, m)
	if !idx["idx_newsgroups_name_nocase"] {
		t.Fatal("idx_newsgroups_name_nocase missing after migration 0027")
	}
	if idx["idx_name"] {
		t.Fatal("idx_name still present after migration 0027")
	}
	pattern := escapeLike("comp.l_ng%") + "%"
	for name, tc := range map[string]struct {
		q    string
		args []interface{}
	}{
		"search":      {query_SearchNewsgroupsNameOnly, []interface{}{pattern, 10, 0}},
		"searchAdmin": {query_SearchNewsgroupsAdminNameOnly, []interface{}{pattern, 10, 0}},
		"count":       {query_CountSearchNewsgroupsNameOnly, []interface{}{pattern}},
		"countAdmin":  {query_CountSearchNewsgroupsAdminNameOnly, []interface{}{pattern}},
	} {
		plan := w2DBPerfPlan(t, m, tc.q, tc.args...)
		t.Logf("%s: %s", name, plan)
		if !strings.Contains(plan, "idx_newsgroups_name_nocase") {
			t.Errorf("%s: plan %q does not use idx_newsgroups_name_nocase", name, plan)
		}
	}
	for name, q := range map[string]string{"searchDesc": query_SearchNewsgroupsWithDesc, "countDesc": query_CountSearchNewsgroupsWithDesc} {
		args := []interface{}{pattern, pattern, 10, 0}[:strings.Count(q, "?")]
		t.Logf("%s: %s", name, w2DBPerfPlan(t, m, q, args...))
	}
}

func TestW2DBPerfGroupDBDropsRedundantIndexes(t *testing.T) {
	db := w0DB(t)
	gdb, err := db.GetGroupDB(w0Name("w2dbperf.grp"))
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Return()

	var applied int
	if err := gdb.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE filename = '0009_single_drop_redundant_indexes.sql'`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("0009_single_drop_redundant_indexes.sql recorded %d times, want 1", applied)
	}
	idx := w2DBPerfIndexes(t, gdb.DB)
	for _, dropped := range []string{"idx_articles_message_id", "idx_articles_hide", "idx_articles_spam"} {
		if idx[dropped] {
			t.Errorf("%s still present", dropped)
		}
	}
	for _, kept := range []string{"idx_articles_hide_article_num", "idx_articles_spam_hide", "idx_articles_article_num_spam", "idx_articles_hide_date"} {
		if !idx[kept] {
			t.Errorf("%s missing", kept)
		}
	}

	// None of the article queries that used the dropped indexes may fall back to a full scan.
	qs := []struct {
		name string
		q    string
		args []interface{}
	}{
		{"GetOverviewsPaginated1", query_GetOverviewsPaginated1, []interface{}{100, 10}},
		{"GetOverviewsPaginated2", query_GetOverviewsPaginated2, []interface{}{10}},
		{"GetOverviewsPaginated3", query_GetOverviewsPaginated3, []interface{}{100}},
		{"pageOffsetCursor", `SELECT article_num FROM articles WHERE hide = 0 ORDER BY article_num DESC LIMIT 1 OFFSET ?`, []interface{}{10}},
		{"GetLastArticleDate", query_GetLastArticleDate, nil},
		{"IncrementArticleSpam", "UPDATE articles SET spam = spam + 1 WHERE article_num = ?", []interface{}{1}},
		{"IncrementArticleHide", "UPDATE articles SET hide = 1 WHERE article_num = ? AND spam > 0", []interface{}{1}},
		{"UnHideArticle", "UPDATE articles SET hide = 0 WHERE article_num = ?", []interface{}{1}},
		{"DecrementArticleSpam", "UPDATE articles SET spam = spam - 1 WHERE article_num = ? AND spam > 0", []interface{}{1}},
		{"spamFixup(cmd/web)", "UPDATE articles SET spam = 1 WHERE spam = 0 AND hide = 1", nil},
		// Built from the live definition, so a revert of the "+hide" plan fix in
		// thread_cache.go is caught here instead of silently passing against a stale copy.
		{"threadCacheChildren", threadChildrenQuery("?,?,?"), []interface{}{1, 2, 3}},
		{"processOverviewBatch2", query_processOverviewBatch2 + "?,?)", []interface{}{"<a@b>", "<c@d>"}},
		{"batchUpdateReplyCounts", "UPDATE articles SET reply_count = CASE WHEN message_id = ? THEN reply_count + ? END WHERE message_id IN (?)", []interface{}{"<a@b>", 1, "<a@b>"}},
		{"threadRootByMessageID", `SELECT root_article FROM threads WHERE root_article = (SELECT article_num FROM articles WHERE message_id = ? LIMIT 1) LIMIT 1`, []interface{}{"<a@b>"}},
	}
	for _, tc := range qs {
		plan := w2DBPerfPlan(t, gdb.DB, tc.q, tc.args...)
		t.Logf("%s: %s", tc.name, plan)
		if strings.Contains(plan, "SCAN articles") {
			t.Errorf("%s: full scan: %s", tc.name, plan)
		}
	}
}
