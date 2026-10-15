package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fuTreeArt is one article of an in-memory thread: number, message-id and References.
type fuTreeArt struct {
	num  int64
	mid  string
	refs string
}

// fuTreeWant is the expected placement of one node; parent 0 means the root (no parent).
type fuTreeWant struct {
	parent int64
	depth  int
	path   string
}

// fuTreeOverviews builds a fresh overview map (fresh map: a new iteration order every call).
// Every article has the same date, so children are ordered by article number.
func fuTreeOverviews(arts []fuTreeArt) map[int64]*models.Overview {
	date := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	ov := make(map[int64]*models.Overview, len(arts))
	for _, a := range arts {
		ov[a.num] = &models.Overview{ArticleNum: a.num, MessageID: a.mid, References: a.refs, DateSent: date,
			Subject: fmt.Sprintf("fu tree %d", a.num)}
	}
	return ov
}

// fuTreeCheck builds the tree 50 times and checks every node, the stats and that the
// TreePaths are the same in every iteration.
func fuTreeCheck(t *testing.T, root int64, arts []fuTreeArt, want map[int64]fuTreeWant, maxDepth, leaves int) {
	t.Helper()
	var first map[int64]string
	for iter := 0; iter < 50; iter++ {
		tree, err := buildTreeFromOverviews(root, fuTreeOverviews(arts))
		if err != nil {
			t.Fatalf("iter %d: buildTreeFromOverviews: %v", iter, err)
		}
		if tree.RootNode == nil || tree.RootNode.ArticleNum != root {
			t.Fatalf("iter %d: RootNode = %+v, want article %d", iter, tree.RootNode, root)
		}
		if tree.TotalNodes != len(want) || len(tree.NodeMap) != len(want) {
			t.Fatalf("iter %d: TotalNodes %d, NodeMap %d, want %d", iter, tree.TotalNodes, len(tree.NodeMap), len(want))
		}
		if tree.MaxDepth != maxDepth || tree.LeafCount != leaves {
			t.Fatalf("iter %d: MaxDepth %d LeafCount %d, want %d %d", iter, tree.MaxDepth, tree.LeafCount, maxDepth, leaves)
		}
		paths := make(map[int64]string, len(want))
		for num, w := range want {
			node := tree.NodeMap[num]
			if node == nil {
				t.Fatalf("iter %d: node %d missing", iter, num)
			}
			var parent int64
			if node.ParentArticle != nil {
				parent = *node.ParentArticle
				if p := tree.NodeMap[parent]; p == nil || !fuTreeHasChild(p, node) {
					t.Fatalf("iter %d: node %d not in the children of its parent %d", iter, num, parent)
				}
			}
			if parent != w.parent || node.Depth != w.depth || node.TreePath != w.path {
				t.Fatalf("iter %d: node %d parent %d depth %d path %q, want %d %d %q",
					iter, num, parent, node.Depth, node.TreePath, w.parent, w.depth, w.path)
			}
			paths[num] = node.TreePath
		}
		if first == nil {
			first = paths
			continue
		}
		for num, p := range paths {
			if first[num] != p {
				t.Fatalf("iter %d: node %d TreePath %q differs from first build %q", iter, num, p, first[num])
			}
		}
	}
}

func fuTreeHasChild(parent, child *TreeNode) bool {
	for _, c := range parent.Children {
		if c == child {
			return true
		}
	}
	return false
}

// TestFuTreeBuildDeterministic: replies met before their parent, an unknown reference, a
// self-reference, 2- and 3-cycles, a reply without references and a duplicate message-id.
func TestFuTreeBuildDeterministic(t *testing.T) {
	arts := []fuTreeArt{
		{10, "<r10@fu>", ""},
		{3, "<m3@fu>", "<r10@fu> <m7@fu>"}, // replies to 7, which has a higher number
		{7, "<m7@fu>", "<r10@fu>"},
		{12, "<m12@fu>", "<r10@fu> <unknown@fu>"}, // parent not in the thread
		{14, "<m14@fu>", "<m14@fu>"},              // self-reference
		{20, "<m20@fu>", "<m21@fu>"},              // 2-cycle 20 <-> 21
		{21, "<m21@fu>", "<m20@fu>"},
		{30, "<dup@fu>", "<r10@fu>"}, // duplicate message-id: 30 wins
		{31, "<dup@fu>", "<r10@fu>"},
		{32, "<m32@fu>", "<r10@fu> <dup@fu>"},
		{35, "<m35@fu>", ""},         // no references
		{40, "<m40@fu>", "<m41@fu>"}, // 3-cycle 40 -> 41 -> 42 -> 40
		{41, "<m41@fu>", "<m42@fu>"},
		{42, "<m42@fu>", "<m40@fu>"},
	}
	// 20 takes 21 (not yet placed); 21 -> 20 would close the cycle, so 21 goes to the root.
	// 40 -> 41 and 41 -> 42 are accepted; 42 -> 40 closes the cycle: 42 goes to the root.
	want := map[int64]fuTreeWant{
		10: {0, 0, "0"},
		7:  {10, 1, "0.0"},
		3:  {7, 2, "0.0.0"},
		12: {10, 1, "0.1"},
		14: {10, 1, "0.2"},
		21: {10, 1, "0.3"},
		20: {21, 2, "0.3.0"},
		30: {10, 1, "0.4"},
		32: {30, 2, "0.4.0"},
		31: {10, 1, "0.5"},
		35: {10, 1, "0.6"},
		42: {10, 1, "0.7"},
		41: {42, 2, "0.7.0"},
		40: {41, 3, "0.7.0.0"},
	}
	// Leaves: 3, 12, 14, 20, 32, 31, 35, 40.
	fuTreeCheck(t, 10, arts, want, 3, 8)
}

// TestFuTreeBuildOrphanBeforeRoot: an orphan with a lower number than the root. The old
// map-order builder failed with "root node is nil when trying to attach orphaned node"
// whenever it met the orphan before the root.
func TestFuTreeBuildOrphanBeforeRoot(t *testing.T) {
	arts := []fuTreeArt{
		{5, "<m5@fu>", "<gone@fu>"},
		{6, "<m6@fu>", "<m5@fu>"},
		{50, "<r50@fu>", ""},
	}
	want := map[int64]fuTreeWant{
		50: {0, 0, "0"},
		5:  {50, 1, "0.0"},
		6:  {5, 2, "0.0.0"},
	}
	fuTreeCheck(t, 50, arts, want, 2, 1)
}

// TestFuTreeBuildChildOrderByDate: children are ordered by date, equal dates by number.
func TestFuTreeBuildChildOrderByDate(t *testing.T) {
	arts := []fuTreeArt{
		{1, "<r1@fu>", ""},
		{2, "<m2@fu>", "<r1@fu>"},
		{3, "<m3@fu>", "<r1@fu>"},
		{4, "<m4@fu>", "<r1@fu>"},
	}
	for iter := 0; iter < 50; iter++ {
		ov := fuTreeOverviews(arts)
		ov[2].DateSent = ov[2].DateSent.Add(time.Hour) // newest: last
		tree, err := buildTreeFromOverviews(1, ov)
		if err != nil {
			t.Fatal(err)
		}
		p3, p4, p2 := tree.NodeMap[3].TreePath, tree.NodeMap[4].TreePath, tree.NodeMap[2].TreePath
		if p3 != "0.0" || p4 != "0.1" || p2 != "0.2" {
			t.Fatalf("iter %d: paths 3=%q 4=%q 2=%q, want 0.0 0.1 0.2", iter, p3, p4, p2)
		}
	}
}

func TestFuTreeBuildMissingRoot(t *testing.T) {
	if _, err := buildTreeFromOverviews(99, fuTreeOverviews([]fuTreeArt{{1, "<m1@fu>", ""}})); err == nil {
		t.Fatal("no error for a root that is not in the overviews")
	}
}

// TestFuTreeStatsTerminateOnCycles: hand-made trees with cyclic Children links (as a broken
// cache could produce) must not hang calculateTreeStats or assignSortOrder.
func TestFuTreeStatsTerminateOnCycles(t *testing.T) {
	a := &TreeNode{ArticleNum: 1}
	b := &TreeNode{ArticleNum: 2}
	c := &TreeNode{ArticleNum: 3}
	a.Children = []*TreeNode{b, a} // self-loop
	b.Children = []*TreeNode{c}
	c.Children = []*TreeNode{a, b} // back edges
	tree := &ThreadTree{ThreadRoot: 1, RootNode: a, NodeMap: map[int64]*TreeNode{1: a, 2: b, 3: c}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		tree.calculateTreeStats()
		tree.assignSortOrder()
	}()
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("calculateTreeStats/assignSortOrder did not terminate on a cyclic tree")
	}
	if a.DescendantCount != 2 || b.DescendantCount != 2 || c.DescendantCount != 2 {
		t.Fatalf("descendants a=%d b=%d c=%d, want 2 each", a.DescendantCount, b.DescendantCount, c.DescendantCount)
	}
	if a.TreePath != "0" || b.TreePath == "" || c.TreePath == "" {
		t.Fatalf("paths a=%q b=%q c=%q", a.TreePath, b.TreePath, c.TreePath)
	}
	(&ThreadTree{}).assignSortOrder() // nil RootNode: no panic
}
