package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/utils"
)

// TreeNode represents a single node in the hierarchical thread tree
type TreeNode struct {
	ArticleNum      int64            `json:"article_num"`
	ParentArticle   *int64           `json:"parent_article"` // nil for root
	Depth           int              `json:"depth"`
	ChildCount      int              `json:"child_count"`
	DescendantCount int              `json:"descendant_count"`
	TreePath        string           `json:"tree_path"` // "0.1.3.7"
	SortOrder       int              `json:"sort_order"`
	Children        []*TreeNode      `json:"children,omitempty"` // Loaded on demand
	Overview        *models.Overview `json:"overview,omitempty"` // Article data
}

// ThreadTree represents a complete thread tree structure
type ThreadTree struct {
	ThreadRoot  int64               `json:"thread_root"`
	MaxDepth    int                 `json:"max_depth"`
	TotalNodes  int                 `json:"total_nodes"`
	LeafCount   int                 `json:"leaf_count"`
	RootNode    *TreeNode           `json:"root_node"`
	NodeMap     map[int64]*TreeNode `json:"-"` // For quick lookup by article_num
	LastUpdated time.Time           `json:"last_updated"`
}

// TreeStats holds aggregated statistics for a thread tree
type TreeStats struct {
	ThreadRoot    int64     `json:"thread_root"`
	MaxDepth      int       `json:"max_depth"`
	TotalNodes    int       `json:"total_nodes"`
	LeafCount     int       `json:"leaf_count"`
	MessageCount  int       `json:"message_count"`  // Same as TotalNodes for compatibility
	TreeStructure string    `json:"tree_structure"` // JSON representation
	LastUpdated   time.Time `json:"last_updated"`
}

// GetTreeStats returns a TreeStats struct with tree statistics and MessageCount
func (tree *ThreadTree) GetTreeStats() TreeStats {
	return TreeStats{
		ThreadRoot:    tree.ThreadRoot,
		MaxDepth:      tree.MaxDepth,
		TotalNodes:    tree.TotalNodes,
		LeafCount:     tree.LeafCount,
		MessageCount:  tree.TotalNodes, // Use TotalNodes as MessageCount
		TreeStructure: "",              // Can be populated if needed
		LastUpdated:   tree.LastUpdated,
	}
}

// BuildThreadTree constructs a hierarchical tree for a given thread root
func (db *Database) BuildThreadTree(groupDB *GroupDB, threadRoot int64) (*ThreadTree, error) {
	// First check if we have a cached tree
	if tree, err := db.GetCachedTree(groupDB, threadRoot); err == nil {
		return tree, nil
	}

	// No cached tree - build from references data
	log.Printf("[TREE:BUILD] Building new tree for thread root %d", threadRoot)

	// Get all articles in this thread from thread_cache
	var childArticles string
	query := `SELECT child_articles FROM thread_cache WHERE thread_root = ?`
	err := RetryableQueryRowScan(groupDB.DB, query, []interface{}{threadRoot}, &childArticles)
	if err != nil {
		if err == sql.ErrNoRows {
			// Thread cache not yet built - fall back to single article
			log.Printf("[TREE:BUILD] No thread cache found for root %d, building minimal tree", threadRoot)
			childArticles = ""
		} else {
			return nil, fmt.Errorf("failed to get thread articles: %w", err)
		}
	}

	// Parse all article numbers in thread (root + children)
	allArticles := []int64{threadRoot} // Start with root
	if childArticles != "" {
		childNums := strings.Split(childArticles, ",")
		for _, numStr := range childNums {
			if num, err := strconv.ParseInt(numStr, 10, 64); err == nil {
				allArticles = append(allArticles, num)
			}
		}
	}

	// Get overview data for all articles to access References headers
	overviews := make(map[int64]*models.Overview)
	for _, artNum := range allArticles {
		if overview, err := db.GetOverviewByArticleNum(groupDB, artNum); err == nil {
			overviews[artNum] = overview
		}
	}

	// Batch sanitize all overviews for better performance when rendering the tree
	var overviewList []*models.Overview
	for _, overview := range overviews {
		overviewList = append(overviewList, overview)
	}
	models.BatchSanitizeOverviews(overviewList, groupDB.Newsgroup)

	tree, err := buildTreeFromOverviews(threadRoot, overviews)
	if err != nil {
		return nil, err
	}

	// Cache the tree structure
	if err := db.CacheTreeStructure(groupDB, tree); err != nil {
		log.Printf("Failed to cache tree structure: %v", err)
		// Don't fail - tree is still usable
	}

	return tree, nil
}

// buildTreeFromOverviews builds the thread tree of threadRoot from the overviews of its
// articles (keyed by article number). The result does not depend on map iteration order:
// articles are processed by ascending number, a message-id that occurs twice resolves to the
// lowest article number, and children are ordered by date with the article number as
// tiebreak. The parent of an article is the article named by its last reference; a missing
// parent, a self-reference or a reference that would close a cycle attaches it to the root.
func buildTreeFromOverviews(threadRoot int64, ov map[int64]*models.Overview) (*ThreadTree, error) {
	rootOv, ok := ov[threadRoot]
	if !ok {
		return nil, fmt.Errorf("root article %d not found in overviews (may be missing from DB)", threadRoot)
	}

	nums := make([]int64, 0, len(ov))
	for artNum := range ov {
		nums = append(nums, artNum)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })

	tree := &ThreadTree{
		ThreadRoot:  threadRoot,
		NodeMap:     make(map[int64]*TreeNode, len(ov)),
		LastUpdated: time.Now(),
	}
	root := &TreeNode{ArticleNum: threadRoot, Overview: rootOv, Children: make([]*TreeNode, 0)}
	tree.RootNode = root
	tree.NodeMap[threadRoot] = root

	// Create nodes and the message-id index (ascending: the first article with a message-id wins).
	byMsgID := make(map[string]int64, len(ov))
	for _, artNum := range nums {
		overview := ov[artNum]
		if artNum != threadRoot {
			tree.NodeMap[artNum] = &TreeNode{ArticleNum: artNum, Overview: overview, Children: make([]*TreeNode, 0)}
		}
		if overview == nil || overview.MessageID == "" {
			continue
		}
		if _, dup := byMsgID[overview.MessageID]; !dup {
			byMsgID[overview.MessageID] = artNum
		}
	}

	// Resolve parents. parentOf only holds accepted links, so it never contains a cycle and
	// every chain ends at the root.
	parentOf := make(map[int64]int64, len(ov))
	closesCycle := func(artNum, parent int64) bool {
		for steps := 0; steps <= len(ov); steps++ {
			if parent == artNum {
				return true
			}
			next, ok := parentOf[parent]
			if !ok {
				return false
			}
			parent = next
		}
		return true // guard: a chain longer than the thread is treated as a cycle
	}
	for _, artNum := range nums {
		if artNum == threadRoot {
			continue
		}
		parent := threadRoot
		if overview := ov[artNum]; overview != nil {
			if refs := utils.ParseReferences(overview.References); len(refs) > 0 {
				parentMessageID := refs[len(refs)-1] // Last reference is immediate parent
				if p, found := byMsgID[parentMessageID]; !found {
					log.Printf("[TREE:CROSSPOST] Article %d references parent %s not found in current group (cross-post?)",
						artNum, parentMessageID)
				} else if p != artNum && !closesCycle(artNum, p) {
					parent = p
				}
			}
		}
		parentOf[artNum] = parent
	}
	for _, artNum := range nums {
		if artNum == threadRoot {
			continue
		}
		node := tree.NodeMap[artNum]
		parentNum := parentOf[artNum]
		node.ParentArticle = &parentNum
		parentNode := tree.NodeMap[parentNum]
		parentNode.Children = append(parentNode.Children, node)
	}

	// Depths from the root; anything not reached (not expected) is attached to the root.
	visited := make(map[*TreeNode]bool, len(tree.NodeMap))
	var setDepth func(node *TreeNode, depth int)
	setDepth = func(node *TreeNode, depth int) {
		visited[node] = true
		node.Depth = depth
		for _, child := range node.Children {
			if !visited[child] {
				setDepth(child, depth+1)
			}
		}
	}
	setDepth(root, 0)
	for _, artNum := range nums {
		node := tree.NodeMap[artNum]
		if visited[node] {
			continue
		}
		log.Printf("[TREE:BUILD] Article %d not reachable from root %d, attaching it to the root", artNum, threadRoot)
		if node.ParentArticle != nil {
			if old := tree.NodeMap[*node.ParentArticle]; old != nil {
				for i, child := range old.Children {
					if child == node {
						old.Children = append(old.Children[:i], old.Children[i+1:]...)
						break
					}
				}
			}
		}
		rootNum := threadRoot
		node.ParentArticle = &rootNum
		root.Children = append(root.Children, node)
		setDepth(node, 1)
	}

	// Calculate tree statistics and paths
	tree.calculateTreeStats()
	tree.assignSortOrder()
	return tree, nil
}

// GetCachedTree retrieves a pre-computed tree from the cache
func (db *Database) GetCachedTree(groupDB *GroupDB, threadRoot int64) (*ThreadTree, error) {
	// Check if tree cache exists and is recent
	var lastUpdated time.Time
	query := `SELECT last_updated FROM tree_stats WHERE thread_root = ?`
	err := RetryableQueryRowScan(groupDB.DB, query, []interface{}{threadRoot}, &lastUpdated)
	if err != nil {
		return nil, fmt.Errorf("no cached tree found: %w", err)
	}

	// Check if cache is stale (older than 5 minutes)
	if time.Since(lastUpdated) > 5*time.Minute {
		return nil, fmt.Errorf("cached tree is stale")
	}

	// Load tree structure from cache
	rows, err := RetryableQuery(groupDB.DB, `
		SELECT article_num, parent_article, depth, child_count, descendant_count,
		       tree_path, sort_order
		FROM cached_trees
		WHERE thread_root = ?
		ORDER BY sort_order`, threadRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to query cached tree: %w", err)
	}
	defer rows.Close()

	tree := &ThreadTree{
		ThreadRoot:  threadRoot,
		NodeMap:     make(map[int64]*TreeNode),
		LastUpdated: lastUpdated,
	}

	// Load all nodes (rows come in sort_order; ordered keeps it for the children lists)
	var ordered []*TreeNode
	for rows.Next() {
		node := &TreeNode{
			Children: make([]*TreeNode, 0),
		}
		var parentArticle sql.NullInt64

		err := rows.Scan(
			&node.ArticleNum,
			&parentArticle,
			&node.Depth,
			&node.ChildCount,
			&node.DescendantCount,
			&node.TreePath,
			&node.SortOrder,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan tree node: %w", err)
		}

		if parentArticle.Valid {
			node.ParentArticle = &parentArticle.Int64
		}

		tree.NodeMap[node.ArticleNum] = node
		ordered = append(ordered, node)

		if node.ArticleNum == threadRoot {
			tree.RootNode = node
		}
	}

	// Build parent-child relationships in sort_order, so children keep their cached order
	for _, node := range ordered {
		if node.ParentArticle != nil && *node.ParentArticle != node.ArticleNum {
			if parentNode, exists := tree.NodeMap[*node.ParentArticle]; exists {
				parentNode.Children = append(parentNode.Children, node)
			}
		}
	}

	// Load tree stats
	statsQuery := `SELECT max_depth, total_nodes, leaf_count FROM tree_stats WHERE thread_root = ?`
	err = RetryableQueryRowScan(groupDB.DB, statsQuery, []interface{}{threadRoot},
		&tree.MaxDepth, &tree.TotalNodes, &tree.LeafCount)
	if err != nil {
		log.Printf("Failed to load tree stats: %v", err)
		// Calculate on the fly
		tree.calculateTreeStats()
	}

	log.Printf("[TREE:CACHE:HIT] Loaded cached tree for root %d (nodes: %d, depth: %d)",
		threadRoot, tree.TotalNodes, tree.MaxDepth)

	return tree, nil
}

// CacheTreeStructure saves a computed tree to the cache
func (db *Database) CacheTreeStructure(groupDB *GroupDB, tree *ThreadTree) error {
	// Start transaction for atomicity
	tx, err := groupDB.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	// Ensure thread_cache entry exists for foreign key constraint
	// Check if thread_cache entry exists
	var exists int
	err = tx.QueryRow(`SELECT 1 FROM thread_cache WHERE thread_root = ?`, tree.ThreadRoot).Scan(&exists)
	if err == sql.ErrNoRows {
		// Create minimal thread_cache entry for single-message threads
		childArticles := ""
		if tree.TotalNodes > 1 {
			// Build comma-separated list of child article numbers
			var children []string
			for _, node := range tree.NodeMap {
				if node.ArticleNum != tree.ThreadRoot {
					children = append(children, fmt.Sprintf("%d", node.ArticleNum))
				}
			}
			childArticles = strings.Join(children, ",")
		}

		_, err = tx.Exec(`
			INSERT INTO thread_cache (
				thread_root, message_count, last_activity, child_articles
			) VALUES (?, ?, ?, ?)`,
			tree.ThreadRoot, tree.TotalNodes, time.Now(), childArticles)
		if err != nil {
			return fmt.Errorf("failed to create thread_cache entry: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to check thread_cache: %w", err)
	}

	// Clear existing cache for this thread
	_, err = tx.Exec(`DELETE FROM cached_trees WHERE thread_root = ?`, tree.ThreadRoot)
	if err != nil {
		return fmt.Errorf("failed to clear old cache: %w", err)
	}

	// Insert all nodes
	insertStmt, err := tx.Prepare(`
		INSERT INTO cached_trees (
			thread_root, article_num, parent_article, depth, child_count,
			descendant_count, tree_path, sort_order
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer insertStmt.Close()

	for _, node := range tree.NodeMap {
		var parentArticle interface{}
		if node.ParentArticle != nil {
			parentArticle = *node.ParentArticle
		} else {
			parentArticle = nil
		}

		_, err = insertStmt.Exec(
			tree.ThreadRoot,
			node.ArticleNum,
			parentArticle,
			node.Depth,
			node.ChildCount,
			node.DescendantCount,
			node.TreePath,
			node.SortOrder,
		)
		if err != nil {
			return fmt.Errorf("failed to insert tree node %d: %w", node.ArticleNum, err)
		}
	}

	// Update tree stats (trigger will handle this automatically)
	// But we can also update manually for immediate consistency
	_, err = tx.Exec(`
		INSERT OR REPLACE INTO tree_stats (
			thread_root, max_depth, total_nodes, leaf_count, last_updated
		) VALUES (?, ?, ?, ?, ?)`,
		tree.ThreadRoot, tree.MaxDepth, tree.TotalNodes, tree.LeafCount, time.Now())
	if err != nil {
		return fmt.Errorf("failed to update tree stats: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit tree cache: %w", err)
	}
	/*
		log.Printf("[TREE:CACHE:SAVED] Cached tree for root %d (nodes: %d, depth: %d)",
			tree.ThreadRoot, tree.TotalNodes, tree.MaxDepth)
	*/
	return nil
}

// InvalidateTreeCache removes cached tree data when thread structure changes
func (db *Database) InvalidateTreeCache(groupDB *GroupDB, threadRoot int64) error {
	_, err := RetryableExec(groupDB.DB, `DELETE FROM cached_trees WHERE thread_root = ?`, threadRoot)
	if err != nil {
		return fmt.Errorf("failed to invalidate tree cache: %w", err)
	}

	_, err = RetryableExec(groupDB.DB, `DELETE FROM tree_stats WHERE thread_root = ?`, threadRoot)
	if err != nil {
		return fmt.Errorf("failed to invalidate tree stats: %w", err)
	}

	//log.Printf("[TREE:CACHE:INVALIDATED] Cleared tree cache for root %d", threadRoot)
	return nil
}

// Helper methods for ThreadTree

func (tree *ThreadTree) calculateTreeStats() {
	tree.MaxDepth = 0
	tree.TotalNodes = len(tree.NodeMap)
	tree.LeafCount = 0

	for _, node := range tree.NodeMap {
		// Update max depth
		if node.Depth > tree.MaxDepth {
			tree.MaxDepth = node.Depth
		}

		// Count children and descendants
		node.ChildCount = len(node.Children)
		node.DescendantCount = tree.countDescendants(node)

		// Count leaf nodes (no children)
		if node.ChildCount == 0 {
			tree.LeafCount++
		}
	}
}

// countDescendants returns the number of distinct nodes below node. It terminates on any
// input: a node met twice (a cycle or a shared child) is counted once.
func (tree *ThreadTree) countDescendants(node *TreeNode) int {
	if node == nil {
		return 0
	}
	seen := map[*TreeNode]bool{node: true}
	stack := append([]*TreeNode(nil), node.Children...)
	count := 0
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil || seen[n] {
			continue
		}
		seen[n] = true
		count++
		stack = append(stack, n.Children...)
	}
	return count
}

func (tree *ThreadTree) assignSortOrder() {
	if tree.RootNode == nil {
		return
	}
	sortOrder := 0
	tree.assignSortOrderRecursive(tree.RootNode, &sortOrder, "0", make(map[*TreeNode]bool, len(tree.NodeMap)))
}

func (tree *ThreadTree) assignSortOrderRecursive(node *TreeNode, sortOrder *int, pathPrefix string, visited map[*TreeNode]bool) {
	visited[node] = true
	node.SortOrder = *sortOrder
	node.TreePath = pathPrefix
	*sortOrder++

	// Sort children by date for consistent ordering
	// This ensures deterministic tree structure between page loads
	sort.SliceStable(node.Children, func(i, j int) bool {
		a, b := node.Children[i], node.Children[j]
		// Sort by date (ascending order - oldest first)
		if a.Overview != nil && b.Overview != nil && !a.Overview.DateSent.Equal(b.Overview.DateSent) {
			return a.Overview.DateSent.Before(b.Overview.DateSent)
		}
		// Same date or missing dates: article number
		return a.ArticleNum < b.ArticleNum
	})

	for i, child := range node.Children {
		if child == nil || visited[child] {
			continue // a cycle or a shared child: each node gets one path
		}
		childPath := fmt.Sprintf("%s.%d", pathPrefix, i)
		tree.assignSortOrderRecursive(child, sortOrder, childPath, visited)
	}
}

// GetTreeStructureJSON returns a JSON representation of the tree structure
func (tree *ThreadTree) GetTreeStructureJSON() (string, error) {
	treeData := map[string]interface{}{
		"thread_root": tree.ThreadRoot,
		"max_depth":   tree.MaxDepth,
		"total_nodes": tree.TotalNodes,
		"leaf_count":  tree.LeafCount,
		"root_node":   tree.RootNode,
		"updated":     tree.LastUpdated,
	}

	jsonData, err := json.Marshal(treeData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal tree to JSON: %w", err)
	}

	return string(jsonData), nil
}
