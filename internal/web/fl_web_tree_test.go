package web

// Tests for slice fl-web of plan followup-leftovers, thread tree items:
//   - /api/thread-tree returns the display From of visible nodes (models.DisplayFrom, the
//     helper behind PrintSanitized("fromheader") that the HTML tree uses), never the address;
//   - thread-tree.html shows no Message-ID row, author, date or "View Article" button for the
//     placeholder of a hidden root;
//   - a node whose overview does not load is filled from the article's flags: a hidden one
//     stays hidden for non-admins, a visible one links to its article page and can be hidden
//     by an admin from there (the hide endpoint works by number).
// Identifiers use the flWeb/TestFlWeb prefixes.

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

// flWebArt is one article row of a flWebSeed group.
type flWebArt struct {
	num     int64
	mid     string
	subject string
	from    string
	refs    string
	body    string
	headers string
	hide    int
}

// flWebSeed creates an active group holding arts as one thread rooted at arts[0] (every other
// article a direct reply of the root) and returns its name. A reply without refs references
// the root: BuildThreadTree attaches an orphan only when it met the root first in its map
// iteration, so orphans would make the fixture flaky.
func flWebSeed(t *testing.T, prefix string, arts []flWebArt) string {
	t.Helper()
	db := w0DB(t)
	group := w0Name(prefix)
	n := len(arts)
	if _, err := database.RetryableExec(db.GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', ?, ?, 1, ?)",
		group, n, n, database.ExtractHierarchyFromGroupName(group)); err != nil {
		t.Fatalf("insert newsgroup: %v", err)
	}
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()
	base := time.Now().Add(-time.Hour).UTC()
	const ins = `INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
			"references", bytes, lines, reply_count, path, headers_json, body_text, downloaded, spam, hide)
		 VALUES (?, ?, ?, ?, ?, 'Thu, 15 Oct 2026 10:00:00 +0000', ?, 60, 1, 0, '.POSTED!not-for-mail', ?, ?, ?, 0, ?)`
	var children []string
	for i, a := range arts {
		date := base.Add(time.Duration(i) * time.Minute).Format("2006-01-02 15:04:05")
		if i > 0 && a.refs == "" {
			a.refs = arts[0].mid
		}
		if _, err := database.RetryableExec(groupDB.DB, ins, a.num, a.mid, a.subject, a.from, date, a.refs,
			a.headers, a.body, 1, a.hide); err != nil {
			t.Fatalf("insert article %d: %v", a.num, err)
		}
		if i == 0 {
			if _, err := database.RetryableExec(groupDB.DB,
				"INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, NULL, ?, 0, 0)",
				a.num, a.num); err != nil {
				t.Fatalf("insert thread root: %v", err)
			}
			continue
		}
		children = append(children, fmt.Sprint(a.num))
		if _, err := database.RetryableExec(groupDB.DB,
			"INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, ?, ?, 1, ?)",
			arts[0].num, arts[0].num, a.num, i); err != nil {
			t.Fatalf("insert thread child %d: %v", a.num, err)
		}
	}
	last := base.Add(time.Duration(n) * time.Minute).Format("2006-01-02 15:04:05")
	if _, err := database.RetryableExec(groupDB.DB,
		"INSERT INTO thread_cache (thread_root, root_date, message_count, child_articles, last_child_number, last_activity) VALUES (?, ?, ?, ?, ?, ?)",
		arts[0].num, base.Format("2006-01-02 15:04:05"), n, strings.Join(children, ","), arts[n-1].num, last); err != nil {
		t.Fatalf("insert thread_cache: %v", err)
	}
	return group
}

// flWebMID returns a unique message-id.
func flWebMID(tag string) string {
	return fmt.Sprintf("<fl-web-%s-%d@test.invalid>", tag, w0Seq.Add(1))
}

// flWebTreeNode is the part of a /api/thread-tree node the tests read.
type flWebTreeNode struct {
	ArticleNum int64 `json:"article_num"`
	Overview   *struct {
		Subject    string `json:"subject"`
		FromHeader string `json:"from_header"`
		Hide       int    `json:"hide"`
	} `json:"overview"`
	Children []flWebTreeNode `json:"children"`
}

// flWebTreeAPI fetches /api/thread-tree of group/root (API enabled) and returns the root node.
func flWebTreeAPI(t *testing.T, group string, root int64, cookies []*http.Cookie) (flWebTreeNode, string) {
	t.Helper()
	path := "/api/thread-tree?group=" + url.QueryEscape(group) + "&thread_root=" + fmt.Sprint(root)
	rec := w0Do(t, w0Req{Path: path, Cookies: cookies})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	var resp struct {
		Tree struct {
			RootNode flWebTreeNode `json:"root_node"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return resp.Tree.RootNode, rec.Body.String()
}

// flWebChild returns the child num of node, or fails.
func flWebChild(t *testing.T, node flWebTreeNode, num int64) flWebTreeNode {
	t.Helper()
	for _, c := range node.Children {
		if c.ArticleNum == num {
			return c
		}
	}
	t.Fatalf("node %d has no child %d", node.ArticleNum, num)
	return flWebTreeNode{}
}

// TestFlWebTreeAPIDisplayFrom: visible nodes carry the display From (the one the HTML tree
// shows), for anonymous viewers and admins alike; no address reaches the JSON.
func TestFlWebTreeAPIDisplayFrom(t *testing.T) {
	f := adfHideSeed(t)
	lo1CoreSetAPIEnabled(t, "true")
	const rawVisibleFrom = "visible <visible@test.invalid>"
	want := models.DisplayFrom(rawVisibleFrom)
	if want != "visible" {
		t.Fatalf("DisplayFrom(%q) = %q, want visible", rawVisibleFrom, want)
	}
	// The HTML tree prints the same text for this From.
	if got := string((&models.Overview{FromHeader: rawVisibleFrom}).PrintSanitized("fromheader")); got != want {
		t.Fatalf("PrintSanitized(fromheader) = %q, DisplayFrom = %q: the API would differ from the HTML tree", got, want)
	}

	root, body := flWebTreeAPI(t, f.group, 1, nil)
	if strings.Contains(body, "@test.invalid>") || strings.Contains(body, "visible@") {
		t.Fatalf("anonymous /api/thread-tree carries an address: %s", body)
	}
	child := flWebChild(t, root, 2)
	if child.Overview == nil || child.Overview.FromHeader != want {
		t.Fatalf("anonymous: visible reply from_header = %+v, want %q", child.Overview, want)
	}

	_, adminCookie := w0NewUser(t, true)
	root, body = flWebTreeAPI(t, f.group, 1, []*http.Cookie{adminCookie})
	if strings.Contains(body, adfHideSecretFrom) || strings.Contains(body, "visible@") {
		t.Fatalf("admin /api/thread-tree carries an address: %s", body)
	}
	if root.Overview == nil || root.Overview.FromHeader != models.DisplayFrom("Secret <"+adfHideSecretFrom+">") {
		t.Fatalf("admin: hidden root from_header = %+v, want the display name", root.Overview)
	}
	if child := flWebChild(t, root, 2); child.Overview == nil || child.Overview.FromHeader != want {
		t.Fatalf("admin: visible reply from_header = %+v, want %q", child.Overview, want)
	}
}

// TestFlWebTreeHiddenRootPlaceholder: the root card of a hidden root shows no Message-ID row,
// author, date or "View Article" button to non-admins; admins still get all of them.
func TestFlWebTreeHiddenRootPlaceholder(t *testing.T) {
	f := adfHideSeed(t)
	const viewButton = `</i>View Article`
	articleLink := `href="/groups/` + f.group + `/articles/1"`
	for _, path := range []string{"/groups/" + f.group + "/tree/1", "/" + f.section + "/" + f.group + "/tree/1"} {
		rec := w0Do(t, w0Req{Path: path})
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "[hidden article]") {
			t.Fatalf("anonymous %s = %d, want 200 with the placeholder: %.500s", path, rec.Code, body)
		}
		for _, bad := range []string{"Message-ID:", viewButton, articleLink, "<strong>From:</strong>", "<strong>Date:</strong>"} {
			if strings.Contains(body, bad) {
				t.Errorf("anonymous %s shows %q for the hidden root placeholder", path, bad)
			}
		}
	}

	_, adminCookie := w0NewUser(t, true)
	rec := w0Do(t, w0Req{Path: "/groups/" + f.group + "/tree/1", Cookies: []*http.Cookie{adminCookie}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("admin tree page = %d", rec.Code)
	}
	for _, want := range []string{"Message-ID:", f.hiddenMID[1 : len(f.hiddenMID)-1], viewButton, articleLink, "<strong>From:</strong>"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin tree page lacks %q for the hidden root", want)
		}
	}

	// A visible root keeps its card.
	group := flWebSeed(t, "flwebroot", []flWebArt{{num: 1, mid: flWebMID("root"), subject: "fl web visible root", from: "root <r@test.invalid>", body: "x"}})
	rec = w0Do(t, w0Req{Path: "/groups/" + group + "/tree/1"})
	body = rec.Body.String()
	for _, want := range []string{"Message-ID:", viewButton, `href="/groups/` + group + `/articles/1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("anonymous tree page of a visible root lacks %q", want)
		}
	}
}

// TestFlWebTreeOverviewUnavailable: the tree of articles 1-3 is built and cached, then the
// overview rows of 2 (hidden) and 3 (visible) stop loading (a "downloaded" value that does not
// scan; the article row and its flags stay readable). The cached tree still has both nodes.
// Before the fix both rendered "Unknown Subject" with an article link for everybody, so the
// hidden one was not hidden and marking one hidden changed nothing in the tree.
func TestFlWebTreeOverviewUnavailable(t *testing.T) {
	group := flWebSeed(t, "flwebunavail", []flWebArt{
		{num: 1, mid: flWebMID("root"), subject: "fl web unavail root", from: "root <r@test.invalid>", body: "root"},
		{num: 2, mid: flWebMID("hidden"), subject: "fl web unavail hidden secret", from: "h <h@test.invalid>", body: "h", hide: 1},
		{num: 3, mid: flWebMID("visible"), subject: "fl web unavail visible", from: "v <v@test.invalid>", body: "v"},
	})
	treePath := "/groups/" + group + "/tree/1"
	if rec := w0Do(t, w0Req{Path: treePath}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "3 nodes") {
		t.Fatalf("warm-up tree = %d, want 200 with 3 nodes", rec.Code)
	}
	groupDB, err := w0DB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	if _, err := database.RetryableExec(groupDB.DB, "UPDATE articles SET downloaded = 'broken' WHERE article_num IN (2, 3)"); err != nil {
		groupDB.Return()
		t.Fatalf("break overviews: %v", err)
	}
	_, ovErr := w0DB(t).GetOverviewByArticleNum(groupDB, 2)
	groupDB.Return()
	if ovErr == nil {
		t.Fatal("fixture: the overview of article 2 loads, the test would not cover the failure path")
	}

	link := func(n int) string { return fmt.Sprintf(`href="/groups/%s/articles/%d"`, group, n) }
	hiddenNode := func(n int) string { return fmt.Sprintf(`tree-node-hidden" data-article-num="%d"`, n) }

	rec := w0Do(t, w0Req{Path: treePath})
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous tree = %d", rec.Code)
	}
	if !strings.Contains(body, hiddenNode(2)) || strings.Contains(body, link(2)) {
		t.Errorf("anonymous: hidden article 2 without overview is not rendered hidden (or still linked)")
	}
	if strings.Contains(body, "fl web unavail hidden secret") || strings.Contains(body, "Unknown Subject") {
		t.Errorf("anonymous tree shows the hidden subject or an Unknown Subject node")
	}
	if !strings.Contains(body, treeOverviewUnavailable) || !strings.Contains(body, link(3)) {
		t.Errorf("anonymous: visible article 3 without overview lacks the placeholder or its link")
	}

	// The API: no overview for the hidden node, the placeholder for the visible one.
	lo1CoreSetAPIEnabled(t, "true")
	root, _ := flWebTreeAPI(t, group, 1, nil)
	if n := flWebChild(t, root, 2); n.Overview != nil {
		t.Errorf("anonymous API: hidden node 2 has an overview %+v", n.Overview)
	}
	if n := flWebChild(t, root, 3); n.Overview == nil || n.Overview.Subject != treeOverviewUnavailable {
		t.Errorf("anonymous API: node 3 overview = %+v, want the placeholder", n.Overview)
	}

	// Admins see both placeholders with links; the visible one's article page works, so it
	// can be hidden there.
	_, adminCookie := w0NewUser(t, true)
	admin := []*http.Cookie{adminCookie}
	rec = w0Do(t, w0Req{Path: treePath, Cookies: admin})
	body = rec.Body.String()
	if strings.Contains(body, "[hidden article]") || !strings.Contains(body, link(2)) || !strings.Contains(body, link(3)) {
		t.Errorf("admin tree: want both nodes as linked placeholders: %.3000s", body)
	}
	if rec := w0Do(t, w0Req{Path: "/groups/" + group + "/articles/3", Cookies: admin}); rec.Code != http.StatusOK {
		t.Fatalf("admin article page of node 3 = %d, want 200", rec.Code)
	}
	// IncrementArticleHide only hides an article flagged as spam: flag it first, as an admin
	// does on the article page.
	for _, action := range []string{"spam", "hide"} {
		rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/groups/" + group + "/articles/3/" + action, Cookies: admin})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("admin %s of article 3 = %d, want 303", action, rec.Code)
		}
	}
	body = w0Do(t, w0Req{Path: treePath}).Body.String()
	if !strings.Contains(body, hiddenNode(3)) || strings.Contains(body, link(3)) {
		t.Errorf("anonymous: article 3 hidden by the admin is still shown as visible")
	}
}

// TestFlWebTreeForViewerLeavesSource: the per-viewer copy never changes the (cached) tree.
func TestFlWebTreeForViewerLeavesSource(t *testing.T) {
	parent := int64(1)
	rootOv := &models.Overview{ArticleNum: 1, Subject: "s", FromHeader: "A <a@b.invalid>", Hide: 1}
	childOv := &models.Overview{ArticleNum: 2, Subject: "c", FromHeader: "B <b@b.invalid>"}
	child := &database.TreeNode{ArticleNum: 2, ParentArticle: &parent, Depth: 1, Overview: childOv}
	missing := &database.TreeNode{ArticleNum: 3, ParentArticle: &parent, Depth: 1}
	root := &database.TreeNode{ArticleNum: 1, Overview: rootOv, Children: []*database.TreeNode{child, missing}}
	tree := &database.ThreadTree{ThreadRoot: 1, RootNode: root, NodeMap: map[int64]*database.TreeNode{1: root, 2: child, 3: missing}}

	fill := func(num int64) *models.Overview { return &models.Overview{ArticleNum: num, Subject: "filled", Hide: 1} }
	cp := treeForViewer(tree, fill, true, true)
	if cp.RootNode.Overview != nil || cp.NodeMap[3].Overview != nil {
		t.Errorf("redacted copy keeps a hidden overview")
	}
	if got := cp.NodeMap[2].Overview.FromHeader; got != "B" {
		t.Errorf("copy From = %q, want B", got)
	}
	if cp.RootNode.Children[0] != cp.NodeMap[2] {
		t.Errorf("copy: Children and NodeMap point to different nodes")
	}
	if root.Overview != rootOv || childOv.FromHeader != "B <b@b.invalid>" || missing.Overview != nil || tree.NodeMap[2] != child {
		t.Errorf("treeForViewer modified the source tree")
	}
	// Admin HTML path: no redaction, no From rewrite, the filled overview is kept.
	cp = treeForViewer(tree, fill, false, false)
	if cp.RootNode.Overview != rootOv || cp.NodeMap[3].Overview == nil || cp.NodeMap[3].Overview.Subject != "filled" {
		t.Errorf("unredacted copy: root %+v, filled %+v", cp.RootNode.Overview, cp.NodeMap[3].Overview)
	}
	if treeForViewer(nil, fill, true, true) != nil {
		t.Errorf("treeForViewer(nil) != nil")
	}
}
