package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adfOptPost posts form to path as the session of cookie and returns the response plus the
// success and error flash messages the handler left for that session.
func adfOptPost(t *testing.T, cookie *http.Cookie, path string, form url.Values) (*http.Response, string, string) {
	t.Helper()
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: path, Form: form, Cookies: []*http.Cookie{cookie}})
	success, _ := GetAndClearFlash(cookie.Value, "success")
	_, failure := GetAndClearFlash(cookie.Value, "error")
	return rec.Result(), success, failure
}

// adfOptSection creates a section with a unique name and returns its ID.
func adfOptSection(t *testing.T) int {
	t.Helper()
	section := &models.Section{Name: w0Name("adfoptsec"), DisplayName: "adf opt section", CreatedAt: time.Now()}
	if err := w0DB(t).CreateSection(section); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	return section.ID
}

// adfOptAssignments returns the section IDs newsgroup is assigned to.
func adfOptAssignments(t *testing.T, newsgroup string) []int {
	t.Helper()
	sgs, err := w0DB(t).GetAllSectionGroups()
	if err != nil {
		t.Fatalf("GetAllSectionGroups: %v", err)
	}
	var ids []int
	for _, sg := range sgs {
		if sg.NewsgroupName == newsgroup {
			ids = append(ids, sg.SectionID)
		}
	}
	return ids
}

// adfOptGroupWithArticles inserts the active newsgroup group and one article per
// message-id/date pair into its group DB (article numbers 1..n, subject "adf opt subject <n>").
func adfOptGroupWithArticles(t *testing.T, group string, msgIDs []string, dates []time.Time) string {
	t.Helper()
	db := w0DB(t)
	if _, err := database.RetryableExec(db.GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', ?, ?, 1, ?)",
		group, len(msgIDs), len(msgIDs), database.ExtractHierarchyFromGroupName(group)); err != nil {
		t.Fatalf("insert newsgroup: %v", err)
	}
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()
	for i, mid := range msgIDs {
		date := dates[i].UTC().Format("2006-01-02 15:04:05")
		if _, err := database.RetryableExec(groupDB.DB,
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
			"references", bytes, lines, reply_count, path, headers_json, body_text, downloaded)
			VALUES (?, ?, ?, 'poster <poster@test.invalid>', ?, '', '', 60, 2, 0, '.POSTED!not-for-mail', '', 'body', 1)`,
			i+1, mid, fmt.Sprintf("adf opt subject %d", i+1), date); err != nil {
			t.Fatalf("insert article %d: %v", i+1, err)
		}
	}
	return group
}

// TestAdfOptAIChatNoInnerHTML (A-web-12): the chat page renders model and user text only as
// text nodes; the only innerHTML assignments left clear the message list.
func TestAdfOptAIChatNoInnerHTML(t *testing.T) {
	postKey := strings.ReplaceAll(w0Name("adfoptai"), ".", "_")
	model, err := w0DB(t).CreateAIModel(postKey, "adf-opt-ollama", "Adf Opt Model "+postKey, "test model", true, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w0DB(t).DeleteAIModel(model.ID); err != nil {
			t.Errorf("DeleteAIModel(%d): %v", model.ID, err)
		}
	})
	user, cookie := w0NewUser(t, false)
	const payload = `<img src=x onerror=alert(1)>`
	chatCacheMux.Lock()
	chatHistoryCache[chatHistoryKey(user.ID, postKey)] = &chatEntry{
		msgs:     []ChatMessage{{Role: "user", Content: payload}, {Role: "assistant", Content: payload + "<br>x"}},
		lastUsed: time.Now(),
	}
	chatCacheMux.Unlock()
	t.Cleanup(func() {
		chatCacheMux.Lock()
		delete(chatHistoryCache, chatHistoryKey(user.ID, postKey))
		chatCacheMux.Unlock()
	})

	rec := w0Do(t, w0Req{Path: "/aichat", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /aichat = %d, want 200", rec.Code)
	}
	page := rec.Body.String()
	if strings.Contains(page, payload) {
		t.Fatalf("chat page contains the unescaped history payload %q", payload)
	}

	section := func(startMark, endMark string) string {
		t.Helper()
		start := strings.Index(page, startMark)
		if start < 0 {
			t.Fatalf("%q not found in the chat page", startMark)
		}
		end := strings.Index(page[start:], endMark)
		if end < 0 {
			t.Fatalf("%q not found after %q", endMark, startMark)
		}
		return page[start : start+end]
	}
	appendMsg := section("function appendMessage(role, content) {", "function clearChatMessages() {")
	// html/template strips JS comments, so the end marks are code.
	helpers := section("function appendChatText(", "fixPreRenderedHTML();")
	for name, code := range map[string]string{"appendMessage": appendMsg, "helpers": helpers} {
		if strings.Contains(code, "innerHTML") {
			t.Errorf("%s uses innerHTML:\n%s", name, code)
		}
	}
	for _, want := range []string{"textContent", "appendAssistantText(", "appendChatText("} {
		if !strings.Contains(appendMsg, want) {
			t.Errorf("appendMessage does not contain %q:\n%s", want, appendMsg)
		}
	}
	for _, want := range []string{"createTextNode(", "thinking-block collapsed", "addEventListener('click'"} {
		if !strings.Contains(helpers, want) {
			t.Errorf("chat text helpers do not contain %q:\n%s", want, helpers)
		}
	}
	re := regexp.MustCompile(`innerHTML\s*=\s*([^\n]*)`)
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		if strings.TrimSpace(m[1]) != "'';" {
			t.Errorf("chat page assigns non-empty markup to innerHTML: %s", strings.TrimSpace(m[0]))
		}
	}
	for _, bad := range []string{"${processedContent}", "${content}"} {
		if strings.Contains(page, bad) {
			t.Errorf("chat page still interpolates %q", bad)
		}
	}
}

// TestAdfOptProviderCreateExists (A-web-14): a second create with the same name reports
// "already exists" and leaves one row.
func TestAdfOptProviderCreateExists(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfoptprov")
	form := url.Values{"name": {name}, "host": {"news.test.invalid"}, "port": {"119"}}

	resp, success, failure := adfOptPost(t, cookie, "/admin/providers", form)
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(success, "created successfully") || failure != "" {
		t.Fatalf("first create: %d success=%q error=%q", resp.StatusCode, success, failure)
	}
	p, err := db.GetProviderByName(name)
	if err != nil || p == nil {
		t.Fatalf("GetProviderByName(%s) = %v, %v", name, p, err)
	}
	t.Cleanup(func() {
		if err := db.DeleteProvider(int(p.ID)); err != nil {
			t.Errorf("DeleteProvider(%d): %v", p.ID, err)
		}
	})

	resp, success, failure = adfOptPost(t, cookie, "/admin/providers", form)
	if resp.StatusCode != http.StatusSeeOther || success != "" || !strings.Contains(failure, "already exists") {
		t.Fatalf("second create: %d success=%q error=%q, want an 'already exists' error", resp.StatusCode, success, failure)
	}
	providers, err := db.GetProviders()
	if err != nil {
		t.Fatalf("GetProviders: %v", err)
	}
	n := 0
	for _, pr := range providers {
		if pr.Name == name {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d providers named %s, want 1", n, name)
	}
}

// TestAdfOptProviderUpdate (A-web-14): an unknown id is an error, a rename is refused, and an
// update without the (read-only) name field succeeds.
func TestAdfOptProviderUpdate(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfoptprovupd")
	if err := db.AddProvider(&models.Provider{Name: name, Host: "old.test.invalid", Port: 119, MaxConns: 1, Priority: 1}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	p, err := db.GetProviderByName(name)
	if err != nil || p == nil {
		t.Fatalf("GetProviderByName(%s) = %v, %v", name, p, err)
	}
	t.Cleanup(func() {
		if err := db.DeleteProvider(int(p.ID)); err != nil {
			t.Errorf("DeleteProvider(%d): %v", p.ID, err)
		}
	})
	id := fmt.Sprint(p.ID)

	_, success, failure := adfOptPost(t, cookie, "/admin/providers/update",
		url.Values{"id": {"987654321"}, "name": {name}, "host": {"x.test.invalid"}})
	if success != "" || !strings.Contains(failure, "not found") {
		t.Errorf("update of an unknown id: success=%q error=%q, want a 'not found' error", success, failure)
	}

	_, success, failure = adfOptPost(t, cookie, "/admin/providers/update",
		url.Values{"id": {id}, "name": {name + "x"}, "host": {"renamed.test.invalid"}})
	if success != "" || !strings.Contains(failure, "cannot be changed") {
		t.Errorf("rename: success=%q error=%q, want a 'cannot be changed' error", success, failure)
	}
	if got, err := db.GetProviderByID(p.ID); err != nil || got == nil || got.Host != "old.test.invalid" {
		t.Fatalf("after the refused rename: %+v, %v; want host unchanged", got, err)
	}

	_, success, failure = adfOptPost(t, cookie, "/admin/providers/update",
		url.Values{"id": {id}, "host": {"new.test.invalid"}})
	if failure != "" || !strings.Contains(success, "updated successfully") {
		t.Errorf("update without name: success=%q error=%q", success, failure)
	}
	got, err := db.GetProviderByID(p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetProviderByID(%d) = %v, %v", p.ID, got, err)
	}
	if got.Host != "new.test.invalid" || got.Name != name {
		t.Errorf("after update: name=%q host=%q, want %q / new.test.invalid", got.Name, got.Host, name)
	}
}

// TestAdfOptNewsgroupCreateValidation (A-web-16): invalid names are refused, an existing
// name is reported as such.
func TestAdfOptNewsgroupCreateValidation(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)

	for _, bad := range []string{"bad name<x>", "a..b", ".a", "a.", `x"y`} {
		_, success, failure := adfOptPost(t, cookie, "/admin/newsgroups", url.Values{"name": {bad}})
		if success != "" || !strings.Contains(failure, "Invalid newsgroup name") {
			t.Errorf("create %q: success=%q error=%q, want an 'Invalid newsgroup name' error", bad, success, failure)
		}
		if _, err := db.MainDBGetNewsgroup(bad); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("create %q left a row (lookup err %v)", bad, err)
		}
	}

	name := w0Name("adfoptng")
	_, success, failure := adfOptPost(t, cookie, "/admin/newsgroups", url.Values{"name": {name}})
	if failure != "" || !strings.Contains(success, "created successfully") {
		t.Fatalf("create %s: success=%q error=%q", name, success, failure)
	}
	_, success, failure = adfOptPost(t, cookie, "/admin/newsgroups", url.Values{"name": {name}})
	if success != "" || !strings.Contains(failure, "already exists") {
		t.Errorf("second create %s: success=%q error=%q, want 'already exists'", name, success, failure)
	}
}

// TestAdfOptNewsgroupUpdateUnknown (A-web-16): updating a name without a row is an error, and
// the redirect keeps '&' and '#' of the search term.
func TestAdfOptNewsgroupUpdateUnknown(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	missing := w0Name("adfoptmissing")
	_, success, failure := adfOptPost(t, cookie, "/admin/newsgroups/update",
		url.Values{"name": {missing}, "description": {"x"}})
	if success != "" || !strings.Contains(failure, "not found") {
		t.Errorf("update of %s: success=%q error=%q, want a 'not found' error", missing, success, failure)
	}
	if _, err := db.MainDBGetNewsgroup(missing); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("update of %s created a row (lookup err %v)", missing, err)
	}

	group := w0NewGroup(t, true)
	const search = "a&b#c d"
	resp, success, failure := adfOptPost(t, cookie, "/admin/newsgroups/update", url.Values{
		"name": {group}, "description": {"adf opt desc"}, "active": {"on"},
		"search": {search}, "search_description": {"on"}, "ng_page": {"2"},
	})
	if failure != "" || !strings.Contains(success, "updated successfully") {
		t.Fatalf("update of %s: success=%q error=%q", group, success, failure)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location %q: %v", resp.Header.Get("Location"), err)
	}
	q := loc.Query()
	if loc.Path != "/admin" || loc.Fragment != "" || q.Get("tab") != "newsgroups" || q.Get("search") != search ||
		q.Get("search_description") != "on" || q.Get("ng_page") != "2" {
		t.Errorf("Location %q: path=%q fragment=%q query=%v, want tab, search %q, search_description and ng_page",
			resp.Header.Get("Location"), loc.Path, loc.Fragment, q, search)
	}
	ng, err := db.MainDBGetNewsgroup(group)
	if err != nil || ng.Description != "adf opt desc" {
		t.Errorf("after update: %+v, %v", ng, err)
	}
}

// TestAdfOptNewsgroupAssignSection (A-web-16): no assignment for a group without a row, and a
// reassignment removes every earlier assignment.
func TestAdfOptNewsgroupAssignSection(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	s1, s2, s3 := adfOptSection(t), adfOptSection(t), adfOptSection(t)

	missing := w0Name("adfoptorphan")
	_, success, failure := adfOptPost(t, cookie, "/admin/newsgroups/assign-section",
		url.Values{"newsgroup_name": {missing}, "section_id": {fmt.Sprint(s1)}})
	if success != "" || !strings.Contains(failure, "not found") {
		t.Errorf("assign %s: success=%q error=%q, want a 'not found' error", missing, success, failure)
	}
	if ids := adfOptAssignments(t, missing); len(ids) != 0 {
		t.Errorf("assign of a missing group created section_groups rows for sections %v", ids)
	}

	group := w0NewGroup(t, true)
	for _, sid := range []int{s1, s2} {
		if err := db.CreateSectionGroup(&models.SectionGroup{SectionID: sid, NewsgroupName: group, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("CreateSectionGroup(%d, %s): %v", sid, group, err)
		}
	}
	_, success, failure = adfOptPost(t, cookie, "/admin/newsgroups/assign-section",
		url.Values{"newsgroup_name": {group}, "section_id": {fmt.Sprint(s3)}})
	if failure != "" || !strings.Contains(success, "assigned") {
		t.Fatalf("assign %s: success=%q error=%q", group, success, failure)
	}
	if ids := adfOptAssignments(t, group); len(ids) != 1 || ids[0] != s3 {
		t.Errorf("assignments of %s = %v, want only [%d]", group, ids, s3)
	}

	_, success, failure = adfOptPost(t, cookie, "/admin/newsgroups/assign-section",
		url.Values{"newsgroup_name": {group}, "section_id": {"0"}})
	if failure != "" || !strings.Contains(success, "unassigned") {
		t.Fatalf("unassign %s: success=%q error=%q", group, success, failure)
	}
	if ids := adfOptAssignments(t, group); len(ids) != 0 {
		t.Errorf("assignments of %s after unassign = %v, want none", group, ids)
	}
}

// TestAdfOptHideFuturePosts (A-web-16): the future-article query (now RetryableQuery with
// rows.Err) finds only the article dated beyond the 48 h cutoff.
func TestAdfOptHideFuturePosts(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	seq := w0Seq.Add(1)
	group := adfOptGroupWithArticles(t, w0Name("adfoptfuture"),
		[]string{fmt.Sprintf("<adf-opt-past-%d@test.invalid>", seq), fmt.Sprintf("<adf-opt-future-%d@test.invalid>", seq)},
		[]time.Time{time.Now().Add(-time.Hour), time.Now().Add(96 * time.Hour)})

	_, success, failure := adfOptPost(t, cookie, "/admin/newsgroups/hide-future-posts", url.Values{"newsgroup_name": {group}})
	if failure != "" || !strings.Contains(success, "processed 1 future-dated") {
		t.Fatalf("hide-future-posts %s: success=%q error=%q, want 1 processed", group, success, failure)
	}
	groupDB, err := w0DB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()
	for num, want := range map[int64]int{1: 0, 2: 1} {
		var hide int
		if err := database.RetryableQueryRowScan(groupDB.DB, "SELECT hide FROM articles WHERE article_num = ?", []any{num}, &hide); err != nil {
			t.Fatalf("read hide of %d: %v", num, err)
		}
		if hide != want {
			t.Errorf("article %d hide = %d, want %d", num, hide, want)
		}
	}
}

// TestAdfOptGetDataEscapes (A-web-18): parent_URL and references_URLs path-escape group and
// the last reference, and the message-id survives the round trip.
func TestAdfOptGetDataEscapes(t *testing.T) {
	const parent = "<a/b?c#d%e@x>"
	a := &models.Article{References: "<first@x>  " + parent}
	for _, what := range []string{"parent_URL", "references_URLs"} {
		got := a.GetData(what, "alt.test")
		const prefix = "/groups/alt.test/message/"
		if !strings.HasPrefix(got, prefix) {
			t.Fatalf("GetData(%s) = %q, want prefix %q", what, got, prefix)
		}
		seg := strings.TrimPrefix(got, prefix)
		if strings.ContainsAny(seg, "/?#") {
			t.Errorf("GetData(%s) = %q: message-id segment not escaped", what, got)
		}
		if dec, err := url.PathUnescape(seg); err != nil || dec != parent {
			t.Errorf("GetData(%s) segment %q unescapes to %q (%v), want %q", what, seg, dec, err, parent)
		}
	}
	if got := (&models.Article{}).GetData("parent_URL", "g"); got != "" {
		t.Errorf("GetData without references = %q, want empty", got)
	}
	var nilArt *models.Article
	if got := nilArt.GetData("parent_URL", "g"); got != "" {
		t.Errorf("nil GetData = %q, want empty", got)
	}
}

// TestAdfOptRawPathParams (A-web-18): the router's param handling (UseRawPath, gin's
// UnescapePathValues off, unescapeRawPathParams) gives the same params as gin's defaults for
// canonical paths, and keeps '+' (which url.QueryUnescape would turn into a space) for paths
// gin matches on URL.RawPath.
func TestAdfOptRawPathParams(t *testing.T) {
	newEngine := func(raw bool) *gin.Engine {
		e := gin.New()
		if raw {
			e.UseRawPath = true
			e.UnescapePathValues = false
			e.Use(unescapeRawPathParams())
		}
		e.GET("/g/:group/m/:id", func(c *gin.Context) {
			c.String(http.StatusOK, "%s|%s", c.Param("group"), c.Param("id"))
		})
		e.GET("/s/*filepath", func(c *gin.Context) {
			c.String(http.StatusOK, "%s", c.Param("filepath"))
		})
		return e
	}
	serve := func(e *gin.Engine, target string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			return fmt.Sprintf("status %d", rec.Code)
		}
		return rec.Body.String()
	}
	oldEngine, rawEngine := newEngine(false), newEngine(true)

	// Canonical escaping: URL.RawPath stays empty, both routers see identical params.
	for _, target := range []string{
		"/g/comp.lang.c++/m/%3Cab+c@x%3E",
		"/g/a.b/m/%3Cx@y%3E",
		"/g/a.b/m/%3Ca%20b%25c%3Fd%23e@x%3E",
		"/g/a_b-c/m/plain",
		"/s/js/thread-tree.js",
		"/s/a%20b.css",
	} {
		if got, want := serve(rawEngine, target), serve(oldEngine, target); got != want {
			t.Errorf("GET %s: raw-path router %q, default router %q", target, got, want)
		}
	}

	// Non-canonical escaping: gin matches on URL.RawPath, '+' and ',' must survive.
	for target, want := range map[string]string{
		"/g/comp.lang.c++/m/%3Ca%2Fb,c+d@x%3E":     "comp.lang.c++|<a/b,c+d@x>",
		"/g/comp.lang.c%2B%2B/m/%3Ca%2Bb%2Fc@x%3E": "comp.lang.c++|<a+b/c@x>",
		"/g/comp.lang.c++/m/<ab+c@x>":              "comp.lang.c++|<ab+c@x>",
		"/g/a.b/m/%3ca%2cb@x%3e":                   "a.b|<a,b@x>",
		"/g/a.b/m/%3Ca%2Cb+c@x%3E":                 "a.b|<a,b+c@x>",
		"/s/a%2Fb+c.css":                           "/a/b+c.css",
	} {
		if got := serve(rawEngine, target); got != want {
			t.Errorf("GET %s = %q, want %q", target, got, want)
		}
	}
}

// TestAdfOptMessageIDRouteRawPath (A-web-18): message-ids with '/', '?', '#', '%', ',' and
// '+' in a group named like comp.lang.c++ resolve through the GetData link, a link with
// literal '+', and the literal-bracket form, on the web page and on the API route; routes
// with plain params still match.
func TestAdfOptMessageIDRouteRawPath(t *testing.T) {
	w1APIEnable(t)
	_, token, err := w0DB(t).CreateAPIToken("adfopt-rawpath", 0, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	seq := w0Seq.Add(1)
	ids := []string{
		fmt.Sprintf("<adf/opt?x#y%%z-%d@test.invalid>", seq),
		fmt.Sprintf("<a/b,c+d-%d@test.invalid>", seq),
		fmt.Sprintf("<ab+c-%d@test.invalid>", seq),
	}
	now := time.Now().Add(-time.Hour)
	group := adfOptGroupWithArticles(t, fmt.Sprintf("adfopt%d.lang.c++", seq), ids, []time.Time{now, now, now})

	for i, mid := range ids {
		subject := fmt.Sprintf("adf opt subject %d", i+1)
		segs := map[string]string{
			// GetData: everything escaped, '+' as %2B.
			"GetData": strings.TrimPrefix((&models.Article{References: mid}).GetData("parent_URL", group), "/groups/"),
			// Literal '+' in group and id; an escaped '/' makes the path non-canonical.
			"literal-plus": group + "/message/" + url.PathEscape(mid),
		}
		if !strings.ContainsAny(mid, "/?#%") {
			segs["literal-brackets"] = group + "/message/" + mid
		}
		for form, seg := range segs {
			rec := w0Do(t, w0Req{Path: "/groups/" + seg})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), subject) {
				t.Errorf("%s: GET /groups/%s = %d, want 200 with %q (body %.300q)", form, seg, rec.Code, subject, rec.Body.String())
			}
			rec = w0Do(t, w0Req{Path: "/api/v1/groups/" + seg, Header: map[string]string{APIAuthHeader: token}})
			var art struct {
				MessageID string `json:"message_id"`
			}
			if rec.Code != http.StatusOK {
				t.Errorf("%s: GET /api/v1/groups/%s = %d %.300q, want 200", form, seg, rec.Code, rec.Body.String())
			} else if err := json.Unmarshal(rec.Body.Bytes(), &art); err != nil || art.MessageID != mid {
				t.Errorf("%s: GET /api/v1/groups/%s message_id = %q (%v), want %q", form, seg, art.MessageID, err, mid)
			}
		}
	}

	for _, path := range []string{"/groups/" + group, "/groups/" + group + "/articles/1", "/ping"} {
		if rec := w0Do(t, w0Req{Path: path}); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
	if rec := w0Do(t, w0Req{Path: "/groups/" + group + "/message/" + url.PathEscape("<adf-opt-none@test.invalid>")}); rec.Code == http.StatusOK {
		t.Errorf("GET of an unknown message-id = 200, want an error status")
	}
}

// adfOptCacheGoroutines counts the cleanup goroutines of the sanitized and newsgroup caches.
func adfOptCacheGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	s := string(buf)
	return strings.Count(s, "(*SanitizedCache).cleanupLoop") + strings.Count(s, "(*NewsgroupCache).cleanup(")
}

// TestAdfOptShutdownStopsCaches (A-web-19): Shutdown stops the global caches' cleanup
// goroutines, and repeated Shutdown calls (one server, several servers) do not panic.
func TestAdfOptShutdownStopsCaches(t *testing.T) {
	if models.GetSanitizedCache() == nil || models.GetNewsgroupCache() == nil {
		t.Fatal("test main did not initialise the global caches")
	}
	srv1 := lo1CoreNewServer(t)
	srv2 := lo1CoreNewServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), testShutdownWait())
	defer cancel()
	for i, srv := range []*WebServer{srv1, srv1, srv2} {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown #%d: %v", i+1, err)
		}
	}
	deadline := time.Now().Add(testWait)
	for adfOptCacheGoroutines() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d cache cleanup goroutine(s) still running after Shutdown", adfOptCacheGoroutines())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
