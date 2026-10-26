package database

// Tests for slice adf-web-hide (A-web-1, A-web-15): GetArticleFlags,
// GetArticleFlagsByMessageID and the hidden-node handling of the tree (RedactedCopy,
// GetThreadTreeHTMLFor). Identifiers use the adfFlags/TestAdfFlags prefixes.

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// adfFlagsGroup creates a group DB with article 1 (spam 2, hide 1) and article 2 (no flags).
func adfFlagsGroup(t *testing.T) *GroupDB {
	t.Helper()
	db := w0DB(t)
	group := w0Name("adfflags")
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	t.Cleanup(gdb.Return)
	for _, row := range []struct {
		num        int64
		spam, hide int
	}{{1, 2, 1}, {2, 0, 0}} {
		if _, err := RetryableExec(gdb.DB,
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references",
			                       bytes, lines, path, headers_json, body_text, spam, hide)
			 VALUES (?, ?, 'adf flags', 'tester@test.invalid', CURRENT_TIMESTAMP, 'Thu, 15 Oct 2026 10:00:00 +0000', '',
			         10, 1, 'not-for-mail', 'Subject: adf flags', 'body', ?, ?)`,
			row.num, adfFlagsMsgID(row.num), row.spam, row.hide); err != nil {
			t.Fatalf("seed article %d: %v", row.num, err)
		}
	}
	return gdb
}

func adfFlagsMsgID(n int64) string {
	if n == 1 {
		return "<adf-web-hide-1@test.invalid>"
	}
	return "<adf-web-hide-2@test.invalid>"
}

func TestAdfFlagsByNumber(t *testing.T) {
	db := w0DB(t)
	gdb := adfFlagsGroup(t)

	spam, hide, err := db.GetArticleFlags(gdb, 1)
	if err != nil || spam != 2 || hide != 1 {
		t.Fatalf("GetArticleFlags(1) = %d, %d, %v; want 2, 1, nil", spam, hide, err)
	}
	spam, hide, err = db.GetArticleFlags(gdb, 2)
	if err != nil || spam != 0 || hide != 0 {
		t.Fatalf("GetArticleFlags(2) = %d, %d, %v; want 0, 0, nil", spam, hide, err)
	}
	if _, _, err := db.GetArticleFlags(gdb, 99); err != sql.ErrNoRows {
		t.Fatalf("GetArticleFlags(99) err = %v, want sql.ErrNoRows (unwrapped)", err)
	}
}

func TestAdfFlagsByMessageID(t *testing.T) {
	db := w0DB(t)
	gdb := adfFlagsGroup(t)

	num, spam, hide, err := db.GetArticleFlagsByMessageID(gdb, adfFlagsMsgID(1))
	if err != nil || num != 1 || spam != 2 || hide != 1 {
		t.Fatalf("GetArticleFlagsByMessageID(1) = %d, %d, %d, %v; want 1, 2, 1, nil", num, spam, hide, err)
	}
	num, spam, hide, err = db.GetArticleFlagsByMessageID(gdb, adfFlagsMsgID(2))
	if err != nil || num != 2 || spam != 0 || hide != 0 {
		t.Fatalf("GetArticleFlagsByMessageID(2) = %d, %d, %d, %v; want 2, 0, 0, nil", num, spam, hide, err)
	}
	if _, _, _, err := db.GetArticleFlagsByMessageID(gdb, "<adf-web-hide-missing@test.invalid>"); err != sql.ErrNoRows {
		t.Fatalf("missing message-id err = %v, want sql.ErrNoRows (unwrapped)", err)
	}
}

// adfFlagsTree is root 1 (hidden) -> child 2 (visible) -> grandchild 3 (hidden).
func adfFlagsTree() *ThreadTree {
	parent1, parent2 := int64(1), int64(2)
	n3 := &TreeNode{ArticleNum: 3, ParentArticle: &parent2, Depth: 2,
		Overview: &models.Overview{ArticleNum: 3, Subject: "adf grandchild secret", FromHeader: "c <c@test.invalid>", Hide: 1}}
	n2 := &TreeNode{ArticleNum: 2, ParentArticle: &parent1, Depth: 1, Children: []*TreeNode{n3},
		Overview: &models.Overview{ArticleNum: 2, Subject: "adf visible child", FromHeader: "b <b@test.invalid>"}}
	n1 := &TreeNode{ArticleNum: 1, Depth: 0, Children: []*TreeNode{n2},
		Overview: &models.Overview{ArticleNum: 1, Subject: "adf root secret", FromHeader: "a <a@test.invalid>", Hide: 1}}
	return &ThreadTree{ThreadRoot: 1, MaxDepth: 2, TotalNodes: 3, LeafCount: 1, RootNode: n1,
		NodeMap: map[int64]*TreeNode{1: n1, 2: n2, 3: n3}}
}

func TestAdfFlagsRedactedCopy(t *testing.T) {
	tree := adfFlagsTree()
	red := tree.RedactedCopy()

	// The original is untouched.
	for num, node := range tree.NodeMap {
		if node.Overview == nil {
			t.Fatalf("original node %d lost its overview", num)
		}
	}
	if red.RootNode == tree.RootNode || red.NodeMap[2] == tree.NodeMap[2] {
		t.Fatal("RedactedCopy shares nodes with the original")
	}
	if red.RootNode.Overview != nil || red.NodeMap[3].Overview != nil {
		t.Fatal("hidden nodes keep their overview in the copy")
	}
	if red.NodeMap[2].Overview == nil || red.NodeMap[2].Overview.Subject != "adf visible child" {
		t.Fatal("visible node lost its overview in the copy")
	}
	// NodeMap and the tree links point at the same copied nodes.
	if red.RootNode != red.NodeMap[1] || red.RootNode.Children[0] != red.NodeMap[2] ||
		red.NodeMap[2].Children[0] != red.NodeMap[3] {
		t.Fatal("copied NodeMap and tree links disagree")
	}
	if red.NodeMap[2].ParentArticle == tree.NodeMap[2].ParentArticle || *red.NodeMap[2].ParentArticle != 1 {
		t.Fatal("ParentArticle not deep-copied")
	}
	out, err := json.Marshal(red)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret") {
		t.Fatalf("redacted JSON contains hidden data: %s", out)
	}
	if !strings.Contains(string(out), "adf visible child") {
		t.Fatalf("redacted JSON lacks the visible node: %s", out)
	}
	if (*ThreadTree)(nil).RedactedCopy() != nil {
		t.Fatal("RedactedCopy of nil tree is not nil")
	}
}

func TestAdfFlagsTreeHTMLHidden(t *testing.T) {
	tree := adfFlagsTree()

	anon := string(tree.GetThreadTreeHTMLFor("adf.flags", false))
	if strings.Contains(anon, "secret") || strings.Contains(anon, "a@test.invalid") {
		t.Fatalf("hidden subject/author in non-admin HTML:\n%s", anon)
	}
	if n := strings.Count(anon, "[hidden article]"); n != 2 {
		t.Fatalf("[hidden article] count = %d, want 2:\n%s", n, anon)
	}
	if strings.Contains(anon, "/articles/1\"") || strings.Contains(anon, "/articles/3\"") {
		t.Fatalf("link to a hidden article in non-admin HTML:\n%s", anon)
	}
	if !strings.Contains(anon, "adf visible child") || !strings.Contains(anon, `href="/groups/adf.flags/articles/2"`) {
		t.Fatalf("visible child missing in non-admin HTML:\n%s", anon)
	}

	admin := string(tree.GetThreadTreeHTMLFor("adf.flags", true))
	if strings.Contains(admin, "[hidden article]") || !strings.Contains(admin, "adf root secret") {
		t.Fatalf("admin HTML does not show the hidden articles:\n%s", admin)
	}
	if got := string(tree.GetThreadTreeHTML("adf.flags")); got != admin {
		t.Fatal("GetThreadTreeHTML differs from GetThreadTreeHTMLFor(group, true)")
	}
	for _, node := range tree.NodeMap {
		if node.Overview == nil || node.Overview.Hide == 0 && node.ArticleNum != 2 {
			t.Fatal("rendering changed the tree")
		}
	}
}
