package web

// Tests for slice adf-web-hide (A-web-1, A-web-11, A-web-15; decisions D2, D3): hidden
// articles are 404 for non-admins on every article route, the tree page and /api/thread-tree
// redact them, /api/thread-tree follows the API setting and is privately cacheable, and the
// article page shows the counters from the group DB. Identifiers use the adfHide/TestAdfHide
// prefixes.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

const (
	adfHideSecretSubject = "adf hide secret subject"
	adfHideSecretBody    = "adf hide secret body"
	adfHideSecretFrom    = "secret-poster@test.invalid"
	adfHideVisibleSubj   = "Re: adf hide visible reply"
	adfHideVisibleBody   = "adf hide visible body"
)

// adfHideFixture is one active group in its own section: article 1 is hidden (spam 1,
// hide 1) and is the root of a thread whose reply, article 2, is visible.
type adfHideFixture struct {
	group, section string
	hiddenMID      string
	visibleMID     string
}

func adfHideSeed(t *testing.T) adfHideFixture {
	t.Helper()
	db := w0DB(t)
	f := adfHideFixture{
		group:      w0Name("adfhide"),
		section:    fmt.Sprintf("adfhidesec%d", w0Seq.Add(1)),
		hiddenMID:  fmt.Sprintf("<adf-web-hide-%d@test.invalid>", w0Seq.Add(1)),
		visibleMID: fmt.Sprintf("<adf-web-hide-%d@test.invalid>", w0Seq.Add(1)),
	}
	if _, err := database.RetryableExec(db.GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 2, 2, 1, ?)",
		f.group, database.ExtractHierarchyFromGroupName(f.group)); err != nil {
		t.Fatalf("insert newsgroup: %v", err)
	}
	section := &models.Section{Name: f.section, DisplayName: "ADF hide", CreatedAt: time.Now()}
	if err := db.CreateSection(section); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	if err := db.CreateSectionGroup(&models.SectionGroup{SectionID: section.ID, NewsgroupName: f.group, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateSectionGroup: %v", err)
	}
	w0Srv.loadSectionsCache()

	groupDB, err := db.GetGroupDB(f.group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()
	now := time.Now().Add(-time.Hour).UTC()
	d1, d2 := now.Format("2006-01-02 15:04:05"), now.Add(time.Minute).Format("2006-01-02 15:04:05")
	const insArticle = `INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
			"references", bytes, lines, reply_count, path, headers_json, body_text, downloaded, spam, hide)
		 VALUES (?, ?, ?, ?, ?, 'Thu, 15 Oct 2026 10:00:00 +0000', ?, 60, 1, 0, '.POSTED!not-for-mail', '', ?, 1, ?, ?)`
	stmts := []struct {
		q    string
		args []any
	}{
		{insArticle, []any{1, f.hiddenMID, adfHideSecretSubject, "Secret <" + adfHideSecretFrom + ">", d1, "", adfHideSecretBody, 1, 1}},
		{insArticle, []any{2, f.visibleMID, adfHideVisibleSubj, "visible <visible@test.invalid>", d2, f.hiddenMID, adfHideVisibleBody, 0, 0}},
		{"INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (1, NULL, 1, 0, 0)", nil},
		{"INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (1, 1, 2, 1, 1)", nil},
		{"INSERT INTO thread_cache (thread_root, root_date, message_count, child_articles, last_child_number, last_activity) VALUES (1, ?, 2, '2', 2, ?)",
			[]any{d1, d2}},
	}
	for _, st := range stmts {
		if _, err := database.RetryableExec(groupDB.DB, st.q, st.args...); err != nil {
			t.Fatalf("%s: %v", st.q, err)
		}
	}
	return f
}

// adfHideNoSecret fails when the response body carries data of the hidden article.
func adfHideNoSecret(t *testing.T, what string, body string) {
	t.Helper()
	for _, s := range []string{adfHideSecretSubject, adfHideSecretBody, adfHideSecretFrom} {
		if strings.Contains(body, s) {
			t.Fatalf("%s: body contains %q: %.800s", what, s, body)
		}
	}
}

func (f adfHideFixture) articlePaths() map[string]string {
	mid := url.PathEscape(f.hiddenMID)
	return map[string]string{
		"by number":             "/groups/" + f.group + "/articles/1",
		"by message-id":         "/groups/" + f.group + "/message/" + mid,
		"section by number":     "/" + f.section + "/" + f.group + "/articles/1",
		"section by message-id": "/" + f.section + "/" + f.group + "/message/" + mid,
		"preview":               "/groups/" + f.group + "/articles/1/preview",
	}
}

func TestAdfHideArticleRoutesAnonymousAndUser(t *testing.T) {
	f := adfHideSeed(t)
	_, userCookie := w0NewUser(t, false)
	for name, path := range f.articlePaths() {
		for _, who := range []struct {
			label   string
			cookies []*http.Cookie
		}{{"anonymous", nil}, {"user", []*http.Cookie{userCookie}}} {
			rec := w0Do(t, w0Req{Path: path, Cookies: who.cookies})
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s (%s) = %d, want 404", who.label, name, path, rec.Code)
			}
			adfHideNoSecret(t, who.label+" "+name, rec.Body.String())
		}
	}
	// The API variant of the preview (API enabled) answers the same 404.
	lo1CoreSetAPIEnabled(t, "true")
	rec := w0Do(t, w0Req{Path: "/api/v1/groups/" + f.group + "/articles/1/preview"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Article not found") {
		t.Fatalf("API preview of hidden article = %d %s, want 404 Article not found", rec.Code, rec.Body.String())
	}
	// The visible reply stays readable.
	if rec := w0Do(t, w0Req{Path: "/groups/" + f.group + "/articles/2"}); rec.Code != http.StatusOK {
		t.Fatalf("visible article = %d, want 200", rec.Code)
	}
}

func TestAdfHideArticleRoutesAdmin(t *testing.T) {
	f := adfHideSeed(t)
	_, adminCookie := w0NewUser(t, true)
	for name, path := range f.articlePaths() {
		rec := w0Do(t, w0Req{Path: path, Cookies: []*http.Cookie{adminCookie}})
		if rec.Code != http.StatusOK {
			t.Errorf("admin %s (%s) = %d, want 200", name, path, rec.Code)
		}
	}
	rec := w0Do(t, w0Req{Path: "/groups/" + f.group + "/articles/1", Cookies: []*http.Cookie{adminCookie}})
	body := rec.Body.String()
	if !strings.Contains(body, "👁️ 1") || !strings.Contains(body, "🚩 1") {
		t.Fatalf("admin article page lacks the counters 🚩 1 / 👁️ 1: %.2000s", body)
	}
	rec = w0Do(t, w0Req{Path: "/groups/" + f.group + "/message/" + url.PathEscape(f.hiddenMID), Cookies: []*http.Cookie{adminCookie}})
	if !strings.Contains(rec.Body.String(), "👁️ 1") {
		t.Fatal("admin article-by-message-id page lacks the hide counter 👁️ 1")
	}
}

func TestAdfHideTreePage(t *testing.T) {
	f := adfHideSeed(t)
	for _, path := range []string{"/groups/" + f.group + "/tree/1", "/" + f.section + "/" + f.group + "/tree/1"} {
		rec := w0Do(t, w0Req{Path: path})
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("anonymous %s = %d, want 200: %.500s", path, rec.Code, body)
		}
		if !strings.Contains(body, "[hidden article]") {
			t.Fatalf("anonymous %s lacks [hidden article]: %.2000s", path, body)
		}
		adfHideNoSecret(t, "anonymous "+path, body)
		if !strings.Contains(body, "adf hide visible reply") {
			t.Fatalf("anonymous %s lacks the visible reply", path)
		}
	}
	_, adminCookie := w0NewUser(t, true)
	rec := w0Do(t, w0Req{Path: "/groups/" + f.group + "/tree/1", Cookies: []*http.Cookie{adminCookie}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), adfHideSecretSubject) ||
		strings.Contains(rec.Body.String(), "[hidden article]") {
		t.Fatalf("admin tree page = %d, want 200 with the hidden subject and no [hidden article]", rec.Code)
	}
}

// adfHideTreeJSON is the part of the /api/thread-tree response the tests read.
type adfHideTreeJSON struct {
	Tree struct {
		RootNode struct {
			ArticleNum int64           `json:"article_num"`
			Overview   json.RawMessage `json:"overview"`
			Children   []struct {
				ArticleNum int64           `json:"article_num"`
				Overview   json.RawMessage `json:"overview"`
			} `json:"children"`
		} `json:"root_node"`
	} `json:"tree"`
}

func TestAdfHideThreadTreeAPI(t *testing.T) {
	f := adfHideSeed(t)
	path := "/api/thread-tree?group=" + url.QueryEscape(f.group) + "&thread_root=1"

	lo1CoreSetAPIEnabled(t, "false")
	if rec := w0Do(t, w0Req{Path: path}); rec.Code == http.StatusOK {
		t.Fatalf("API disabled: /api/thread-tree = 200, want refused")
	}

	lo1CoreSetAPIEnabled(t, "true")
	rec := w0Do(t, w0Req{Path: path})
	if rec.Code != http.StatusOK {
		t.Fatalf("API enabled: /api/thread-tree = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "private") {
		t.Fatalf("Cache-Control = %q, want private", cc)
	}
	adfHideNoSecret(t, "anonymous /api/thread-tree", rec.Body.String())
	var anon adfHideTreeJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &anon); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	root := anon.Tree.RootNode
	if root.ArticleNum != 1 || len(root.Overview) != 0 {
		t.Fatalf("anonymous: root node %d has overview %s, want none", root.ArticleNum, root.Overview)
	}
	if len(root.Children) != 1 || root.Children[0].ArticleNum != 2 || len(root.Children[0].Overview) == 0 {
		t.Fatalf("anonymous: visible reply missing or without overview: %s", rec.Body.String())
	}

	_, adminCookie := w0NewUser(t, true)
	rec = w0Do(t, w0Req{Path: path, Cookies: []*http.Cookie{adminCookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin /api/thread-tree = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "private") {
		t.Fatalf("admin Cache-Control = %q, want private", cc)
	}
	var admin adfHideTreeJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &admin); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(admin.Tree.RootNode.Overview) == 0 {
		t.Fatalf("admin: hidden root lost its overview: %s", rec.Body.String())
	}
}

func TestAdfHideTokenAPI(t *testing.T) {
	f := adfHideSeed(t)
	lo1CoreSetAPIEnabled(t, "true")
	_, token, err := w0DB(t).CreateAPIToken("adfhide-token", 0, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	hdr := map[string]string{APIAuthHeader: token}
	for _, path := range []string{
		"/api/v1/groups/" + f.group + "/articles/1",
		"/api/v1/groups/" + f.group + "/message/" + url.PathEscape(f.hiddenMID),
	} {
		rec := w0Do(t, w0Req{Path: path, Header: hdr})
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Article not found") {
			t.Errorf("token %s = %d %s, want 404 Article not found", path, rec.Code, rec.Body.String())
		}
		adfHideNoSecret(t, "token "+path, rec.Body.String())
	}
	if rec := w0Do(t, w0Req{Path: "/api/v1/groups/" + f.group + "/articles/2", Header: hdr}); rec.Code != http.StatusOK {
		t.Fatalf("token visible article = %d, want 200", rec.Code)
	}
}

func TestAdfHideReplyPrefill(t *testing.T) {
	f := adfHideSeed(t)
	_, cookie := w0NewUser(t, false)
	rec := w0Do(t, w0Req{
		Method:  http.MethodPost,
		Path:    "/SitePost",
		Form:    url.Values{"newsgroup": {f.group}, "reply_to": {"1"}, "message_id": {f.hiddenMID}},
		Cookies: []*http.Cookie{cookie},
	})
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("SitePost reply to hidden = %d, want 200", rec.Code)
	}
	adfHideNoSecret(t, "reply prefill", body)
	if !strings.Contains(body, "Article not found") {
		t.Fatalf("reply prefill to a hidden article lacks the error: %.2000s", body)
	}

	// A visible article is still quoted.
	rec = w0Do(t, w0Req{
		Method:  http.MethodPost,
		Path:    "/SitePost",
		Form:    url.Values{"newsgroup": {f.group}, "reply_to": {"2"}, "message_id": {f.visibleMID}},
		Cookies: []*http.Cookie{cookie},
	})
	if !strings.Contains(rec.Body.String(), "&gt; "+adfHideVisibleBody) {
		t.Fatalf("reply prefill to a visible article lacks the quote: %.2000s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Article not found") {
		t.Fatal("reply prefill to a visible article shows Article not found")
	}
}

func TestAdfHideVisibleSpamCounter(t *testing.T) {
	f := adfHideSeed(t)
	path := "/groups/" + f.group + "/articles/2"
	// The counter buttons are shown to logged-in users only.
	_, cookie := w0NewUser(t, false)
	ck := []*http.Cookie{cookie}
	rec := w0Do(t, w0Req{Path: path, Cookies: ck})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "🚩 0") {
		t.Fatalf("visible article before the flag = %d, want 200 with 🚩 0", rec.Code)
	}
	if err := w0DB(t).IncrementArticleSpam(f.group, 2); err != nil {
		t.Fatalf("IncrementArticleSpam: %v", err)
	}
	// The article itself is cached by now; the counter must still come from the DB.
	rec = w0Do(t, w0Req{Path: path, Cookies: ck})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "🚩 1") {
		t.Fatalf("visible article after IncrementArticleSpam = %d, want 200 with 🚩 1", rec.Code)
	}
	rec = w0Do(t, w0Req{Path: "/groups/" + f.group + "/message/" + url.PathEscape(f.visibleMID), Cookies: ck})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "🚩 1") {
		t.Fatalf("visible article by message-id = %d, want 200 with 🚩 1", rec.Code)
	}
}

// TestAdfHideThreadTreeAPIGroupChecks: behind requireAPIEnabled the group checks of
// /api/thread-tree still hold (unknown and inactive groups are 404, no group DB file is created).
func TestAdfHideThreadTreeAPIGroupChecks(t *testing.T) {
	lo1CoreSetAPIEnabled(t, "true")
	unknown := w0Name("adfhide.unknown")
	if rec := w0Do(t, w0Req{Path: "/api/thread-tree?group=" + url.QueryEscape(unknown) + "&thread_root=1"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown group: status %d, want 404", rec.Code)
	}
	w1APINoGroupFile(t, unknown)
	inactive := w0NewGroup(t, false)
	if rec := w0Do(t, w0Req{Path: "/api/thread-tree?group=" + url.QueryEscape(inactive) + "&thread_root=1"}); rec.Code != http.StatusNotFound {
		t.Fatalf("inactive group: status %d, want 404", rec.Code)
	}
	w1APINoGroupFile(t, inactive)
}
