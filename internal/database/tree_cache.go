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
func (db *Database) BuildThreadTree(groupDBs *GroupDBs, threadRoot int64) (*ThreadTree, error) {
	// First check if we have a cached tree
	if tree, err := db.GetCachedTree(groupDBs, threadRoot); err == nil {
		return tree, nil
	}

	// No cached tree - build from references data
	log.Printf("[TREE:BUILD] Building new tree for thread root %d", threadRoot)

	// Get all articles in this thread from thread_cache
	var childArticles string
	query := `SELECT child_articles FROM thread_cache WHERE thread_root = ?`
	err := groupDBs.DB.QueryRow(query, threadRoot).Scan(&childArticles)
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
		if overview, err := db.GetOverviewByArticleNum(groupDBs, artNum); err == nil {
			overviews[artNum] = overview
		}
	}

	// Batch sanitize all overviews for better performance when rendering the tree
	var overviewList []*models.Overview
	for _, overview := range overviews {
		overviewList = append(overviewList, overview)
	}
	models.BatchSanitizeOverviews(overviewList)

	// Ensure root article exists in overviews
	if _, ok := overviews[threadRoot]; !ok {
		return nil, fmt.Errorf("root article %d not found in overviews (may be missing from DB)", threadRoot)
	}

	// Build parent-child relationships from References headers
	tree := &ThreadTree{
		ThreadRoot:  threadRoot,
		NodeMap:     make(map[int64]*TreeNode),
		LastUpdated: time.Now(),
	}

	// Create nodes for all articles
	for artNum, overview := range overviews {
		node := &TreeNode{
			ArticleNum: artNum,
			Overview:   overview,
			Children:   make([]*TreeNode, 0),
		}
		tree.NodeMap[artNum] = node
	}

	// Build parent-child relationships using References
	for artNum, overview := range overviews {
		node := tree.NodeMap[artNum]

		if artNum == threadRoot {
			// This is the root node
			node.Depth = 0
			node.ParentArticle = nil
			tree.RootNode = node
		} else {
			// Find parent from references
			refs := utils.ParseReferences(overview.References)
			if len(refs) > 0 {
				parentMessageID := refs[len(refs)-1] // Last reference is immediate parent

				// Find parent article number
				var parentFound bool
				for parentNum, parentOverview := range overviews {
					if parentOverview.MessageID == parentMessageID {
						node.ParentArticle = &parentNum
						parentNode := tree.NodeMap[parentNum]
						parentNode.Children = append(parentNode.Children, node)
						node.Depth = parentNode.Depth + 1
						parentFound = true
						break
					}
				}

				// Debug: Log cross-posting issues
				if !parentFound {
					log.Printf("[TREE:CROSSPOST] Article %d references parent %s not found in current group (cross-post?)",
						artNum, parentMessageID)
				}
			}

			// If no parent found, attach to root
			if node.ParentArticle == nil {
				// Safety check: ensure root node exists
				if tree.RootNode == nil {
					return nil, fmt.Errorf("root node is nil when trying to attach orphaned node %d", artNum)
				}
				node.ParentArticle = &threadRoot
				tree.RootNode.Children = append(tree.RootNode.Children, node)
				node.Depth = 1
			}
		}
	}

	// Calculate tree statistics and paths
	tree.calculateTreeStats()
	tree.assignSortOrder()

	// Cache the tree structure
	if err := db.CacheTreeStructure(groupDBs, tree); err != nil {
		log.Printf("Failed to cache tree structure: %v", err)
		// Don't fail - tree is still usable
	}

	return tree, nil
}

// GetCachedTree retrieves a pre-computed tree from the cache
func (db *Database) GetCachedTree(groupDBs *GroupDBs, threadRoot int64) (*ThreadTree, error) {
	// Check if tree cache exists and is recent
	var lastUpdated time.Time
	query := `SELECT last_updated FROM tree_stats WHERE thread_root = ?`
	err := groupDBs.DB.QueryRow(query, threadRoot).Scan(&lastUpdated)
	if err != nil {
		return nil, fmt.Errorf("no cached tree found: %w", err)
	}

	// Check if cache is stale (older than 5 minutes)
	if time.Since(lastUpdated) > 5*time.Minute {
		return nil, fmt.Errorf("cached tree is stale")
	}

	// Load tree structure from cache
	rows, err := groupDBs.DB.Query(`
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

	// Load all nodes
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

		if node.ArticleNum == threadRoot {
			tree.RootNode = node
		}
	}

	// Build parent-child relationships
	for _, node := range tree.NodeMap {
		if node.ParentArticle != nil {
			if parentNode, exists := tree.NodeMap[*node.ParentArticle]; exists {
				parentNode.Children = append(parentNode.Children, node)
			}
		}
	}

	// Load tree stats
	statsQuery := `SELECT max_depth, total_nodes, leaf_count FROM tree_stats WHERE thread_root = ?`
	err = groupDBs.DB.QueryRow(statsQuery, threadRoot).Scan(
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
func (db *Database) CacheTreeStructure(groupDBs *GroupDBs, tree *ThreadTree) error {
	// Start transaction for atomicity
	tx, err := groupDBs.DB.Begin()
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
func (db *Database) InvalidateTreeCache(groupDBs *GroupDBs, threadRoot int64) error {
	_, err := groupDBs.DB.Exec(`DELETE FROM cached_trees WHERE thread_root = ?`, threadRoot)
	if err != nil {
		return fmt.Errorf("failed to invalidate tree cache: %w", err)
	}

	_, err = groupDBs.DB.Exec(`DELETE FROM tree_stats WHERE thread_root = ?`, threadRoot)
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

func (tree *ThreadTree) countDescendants(node *TreeNode) int {
	count := 0
	for _, child := range node.Children {
		count += 1 + tree.countDescendants(child)
	}
	return count
}

func (tree *ThreadTree) assignSortOrder() {
	sortOrder := 0
	tree.assignSortOrderRecursive(tree.RootNode, &sortOrder, "0")
}

func (tree *ThreadTree) assignSortOrderRecursive(node *TreeNode, sortOrder *int, pathPrefix string) {
	node.SortOrder = *sortOrder
	node.TreePath = pathPrefix
	*sortOrder++

	// Sort children by date for consistent ordering
	// This ensures deterministic tree structure between page loads
	sort.Slice(node.Children, func(i, j int) bool {
		// Sort by date (ascending order - oldest first)
		if node.Children[i].Overview != nil && node.Children[j].Overview != nil {
			return node.Children[i].Overview.DateSent.Before(node.Children[j].Overview.DateSent)
		}
		// Fallback to article number if dates are missing
		return node.Children[i].ArticleNum < node.Children[j].ArticleNum
	})

	for i, child := range node.Children {
		childPath := fmt.Sprintf("%s.%d", pathPrefix, i)
		tree.assignSortOrderRecursive(child, sortOrder, childPath)
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
