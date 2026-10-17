package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
)

// adfXSSBody is a body a Usenet poster controls: raw markup plus entity-encoded markup. Since
// DecodeBodyText (plan followup-leftovers) a text/plain body keeps its entities as text.
const adfXSSBody = "hello <img src=x onerror=alert(1)>\n&lt;svg onload=alert(2)&gt;"

// adfXSSWantCSP is the policy of decision D1 (audit-delta-fixes plan).
const adfXSSWantCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://fonts.googleapis.com; " +
	"font-src 'self' data: https://cdn.jsdelivr.net https://fonts.gstatic.com; img-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"

// adfXSSNewGroup creates an active group w0Name("adfxss") holding article 1 (body adfXSSBody)
// as a one-article thread, with the threads/thread_cache rows the group and tree pages read.
func adfXSSNewGroup(t *testing.T) string {
	t.Helper()
	db := w0DB(t)
	group := w0Name("adfxss")
	if _, err := database.RetryableExec(db.GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 1, 1, 1, ?)",
		group, database.ExtractHierarchyFromGroupName(group)); err != nil {
		t.Fatalf("insert newsgroup: %v", err)
	}
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()
	now := time.Now().Add(-time.Hour).UTC().Format("2006-01-02 15:04:05")
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
			"references", bytes, lines, reply_count, path, headers_json, body_text, downloaded)
		 VALUES (1, ?, 'adf xss root', 'poster <poster@test.invalid>', ?, '', '', 60, 2, 0, '.POSTED!not-for-mail', '', ?, 1)`,
			[]any{fmt.Sprintf("<adf-xss-%d@test.invalid>", w0Seq.Add(1)), now, adfXSSBody}},
		{"INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (1, NULL, 1, 0, 0)", nil},
		{"INSERT INTO thread_cache (thread_root, root_date, message_count, child_articles, last_child_number, last_activity) VALUES (1, ?, 1, '', 1, ?)",
			[]any{now, now}},
	}
	for _, st := range stmts {
		if _, err := database.RetryableExec(groupDB.DB, st.q, st.args...); err != nil {
			t.Fatalf("%s: %v", st.q, err)
		}
	}
	return group
}

// TestAdfXSSPreviewIsPlainJSON: the preview route answers JSON whose body is decoded plain text
// (markup included as text). The server does not HTML-escape it; the protection is that the
// browser inserts it with textContent (TestAdfXSSThreadTreeJSUsesTextContent).
func TestAdfXSSPreviewIsPlainJSON(t *testing.T) {
	group := adfXSSNewGroup(t)
	rec := w0Do(t, w0Req{Path: "/groups/" + group + "/articles/1/preview"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET preview = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var resp struct {
		Group      string `json:"group"`
		ArticleNum int64  `json:"article_num"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if resp.Group != group || resp.ArticleNum != 1 {
		t.Fatalf("group/article_num = %q/%d, want %q/1", resp.Group, resp.ArticleNum, group)
	}
	for _, want := range []string{"&lt;svg onload=alert(2)&gt;", "<img src=x onerror=alert(1)>"} {
		if !strings.Contains(resp.Body, want) {
			t.Fatalf("preview body %q does not contain %q as text", resp.Body, want)
		}
	}
}

// TestAdfXSSThreadTreeJSUsesTextContent: the served thread-tree.js builds the preview with
// textContent and never puts an article field into innerHTML.
func TestAdfXSSThreadTreeJSUsesTextContent(t *testing.T) {
	rec := w0Do(t, w0Req{Path: "/static/js/thread-tree.js"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/js/thread-tree.js = %d, want 200", rec.Code)
	}
	js := rec.Body.String()

	const startMark = "renderArticlePreview(contentDiv, article) {"
	const endMark = "formatPreviewText(text) {"
	start := strings.Index(js, startMark)
	end := strings.Index(js, endMark)
	if start < 0 || end < 0 || end < start {
		t.Fatalf("renderArticlePreview (%d) / formatPreviewText (%d) definitions not found in order", start, end)
	}
	render := js[start:end]
	for _, want := range []string{"textContent", "renderPreviewNode(", "encodeURIComponent(", "pre-wrap"} {
		if !strings.Contains(render, want) {
			t.Fatalf("renderArticlePreview/renderPreviewNode does not contain %q:\n%s", want, render)
		}
	}
	if strings.Contains(render, "innerHTML") {
		t.Fatalf("renderArticlePreview/renderPreviewNode uses innerHTML:\n%s", render)
	}
	for _, bad := range []string{"${cleanPreview}", "${article.body", "${article.group", "${article."} {
		if strings.Contains(js, bad) {
			t.Fatalf("thread-tree.js still contains %q", bad)
		}
	}
	re := regexp.MustCompile(`innerHTML\s*=.*article\.`)
	for i, line := range strings.Split(js, "\n") {
		if re.MatchString(line) {
			t.Fatalf("thread-tree.js line %d puts an article field into innerHTML: %s", i+1, strings.TrimSpace(line))
		}
	}
}

// TestAdfXSSCSPHeader: pages and the preview JSON carry the D1 Content-Security-Policy, and the
// pages that load thread-tree.js still render.
func TestAdfXSSCSPHeader(t *testing.T) {
	group := adfXSSNewGroup(t)
	for _, path := range []string{
		"/",
		"/groups/" + group,
		"/groups/" + group + "/tree/1",
		"/groups/" + group + "/articles/1/preview",
	} {
		rec := w0Do(t, w0Req{Path: path})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200; body %.500s", path, rec.Code, rec.Body.String())
		}
		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "connect-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Fatalf("GET %s: Content-Security-Policy %q lacks %q", path, csp, want)
			}
		}
		if csp != adfXSSWantCSP {
			t.Fatalf("GET %s: Content-Security-Policy\n got %q\nwant %q", path, csp, adfXSSWantCSP)
		}
	}
	// The tree page renders the node with its preview container (thread-tree.js hooks into it).
	rec := w0Do(t, w0Req{Path: "/groups/" + group + "/tree/1"})
	if body := rec.Body.String(); !strings.Contains(body, "thread-tree") || !strings.Contains(body, "adf xss root") {
		t.Fatalf("tree page not rendered: %.500s", body)
	}
}
