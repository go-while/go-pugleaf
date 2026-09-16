package database

import (
	"fmt"
	"html"
	"html/template"
	"log"
	"net/url"
	"strings"
	"time"
)

// TreeViewOptions configures how the tree should be displayed
type TreeViewOptions struct {
	MaxDepth        int    `json:"max_depth"`        // Limit tree depth (0 = no limit)
	CollapseDepth   int    `json:"collapse_depth"`   // Auto-collapse nodes deeper than this
	IncludeOverview bool   `json:"include_overview"` // Include full overview data for each node
	PageSize        int    `json:"page_size"`        // For paginated tree loading
	SortBy          string `json:"sort_by"`          // "date", "author", "subject"
}

// TreeViewResponse is the JSON response for tree view API calls
type TreeViewResponse struct {
	ThreadRoot int64           `json:"thread_root"`
	Tree       *ThreadTree     `json:"tree"`
	Options    TreeViewOptions `json:"options"`
	Error      string          `json:"error,omitempty"`
	CacheHit   bool            `json:"cache_hit"`
	BuildTime  string          `json:"build_time,omitempty"`
}

// GetThreadTreeView returns a hierarchical tree view for a thread
func (db *Database) GetThreadTreeView(groupDB *GroupDB, threadRoot int64, options TreeViewOptions) (*TreeViewResponse, error) {
	startTime := time.Now()

	// Build or retrieve the thread tree
	tree, err := db.BuildThreadTree(groupDB, threadRoot)
	if err != nil {
		return &TreeViewResponse{
			ThreadRoot: threadRoot,
			Error:      fmt.Sprintf("Failed to build thread tree: %v", err),
		}, err
	}

	// Apply view options
	if options.IncludeOverview {
		if err := db.loadOverviewDataForTree(groupDB, tree); err != nil {
			log.Printf("Failed to load overview data for tree: %v", err)
			// Continue without overview data
		}
	}

	if options.MaxDepth > 0 {
		tree.limitDepth(options.MaxDepth)
	}

	buildTime := time.Since(startTime)

	return &TreeViewResponse{
		ThreadRoot: threadRoot,
		Tree:       tree,
		Options:    options,
		CacheHit:   buildTime < 10*time.Millisecond, // Heuristic for cache hit
		BuildTime:  buildTime.String(),
	}, nil
}

// loadOverviewDataForTree populates Overview data for all nodes in the tree
func (db *Database) loadOverviewDataForTree(groupDB *GroupDB, tree *ThreadTree) error {
	for articleNum, node := range tree.NodeMap {
		if node.Overview == nil {
			overview, err := db.GetOverviewByArticleNum(groupDB, articleNum)
			if err != nil {
				log.Printf("Failed to load overview for article %d: %v", articleNum, err)
				continue
			}
			node.Overview = overview
		}
	}
	return nil
}

// limitDepth truncates the tree at the specified depth
func (tree *ThreadTree) limitDepth(maxDepth int) {
	tree.limitDepthRecursive(tree.RootNode, maxDepth)
}

func (tree *ThreadTree) limitDepthRecursive(node *TreeNode, maxDepth int) {
	if node.Depth >= maxDepth {
		node.Children = nil // Cut off children beyond max depth
		return
	}

	for _, child := range node.Children {
		tree.limitDepthRecursive(child, maxDepth)
	}
}

// Example usage and testing functions

// PrintThreadTreeASCII prints a simple ASCII representation of the tree for debugging
func (tree *ThreadTree) PrintTreeASCII() {
	fmt.Printf("Thread Tree (Root: %d, Nodes: %d, Max Depth: %d)\n",
		tree.ThreadRoot, tree.TotalNodes, tree.MaxDepth)
	fmt.Println("=" + strings.Repeat("=", 60))

	if tree.RootNode != nil {
		tree.printNodeASCII(tree.RootNode, "", true)
	}
}

func (tree *ThreadTree) printNodeASCII(node *TreeNode, prefix string, isLast bool) {
	// Choose connector based on position
	connector := "├── "
	if isLast {
		connector = "└── "
	}

	// Print this node
	subject := "Unknown Subject"
	author := "Unknown Author"
	if node.Overview != nil {
		subject = node.Overview.Subject
		author = node.Overview.FromHeader
		if len(subject) > 40 {
			subject = subject[:37] + "..."
		}
		if len(author) > 20 {
			author = author[:17] + "..."
		}
	}

	fmt.Printf("%s%s[%d] %s (by %s)\n",
		prefix, connector, node.ArticleNum, subject, author)

	// Prepare prefix for children
	childPrefix := prefix
	if isLast {
		childPrefix += "    "
	} else {
		childPrefix += "│   "
	}

	// Print children
	for i, child := range node.Children {
		isLastChild := i == len(node.Children)-1
		tree.printNodeASCII(child, childPrefix, isLastChild)
	}
}

// GetThreadTreeHTML generates HTML representation of the tree (for web display)
func (tree *ThreadTree) GetThreadTreeHTML(groupName string) template.HTML {
	if tree.RootNode == nil {
		return template.HTML("<p>No tree data available</p>")
	}

	escGroup := html.EscapeString(groupName)
	out := fmt.Sprintf(`
<div class="thread-tree" data-thread-root="%d" data-group-name="%s">
	<div class="tree-stats">
		<span class="node-count">%d nodes</span>
		<span class="max-depth">max depth: %d</span>
		<span class="leaf-count">%d leaves</span>
	</div>
	<ul class="tree-root">
`, tree.ThreadRoot, escGroup, tree.TotalNodes, tree.MaxDepth, tree.LeafCount)

	out += tree.getNodeHTML(tree.RootNode, groupName)
	out += `
	</ul>
</div>`

	return template.HTML(out)
}

func (tree *ThreadTree) getNodeHTML(node *TreeNode, groupName string) string {
	subject := "Unknown Subject"
	author := "Unknown Author"
	dateStr := "Unknown Date"

	if node.Overview != nil {
		subject = string(node.Overview.PrintSanitized("subject", groupName))
		author = string(node.Overview.PrintSanitized("fromheader", groupName))
		dateStr = node.Overview.DateSent.Format("2006-01-02 15:04")
	}

	hasChildren := len(node.Children) > 0
	expandClass := ""
	if hasChildren {
		expandClass = " expandable"
	}

	escGroup := html.EscapeString(groupName)
	hrefGroup := html.EscapeString(url.PathEscape(groupName))

	var out string

	// Use details/summary for graceful degradation without JavaScript
	if hasChildren {
		out = fmt.Sprintf(`
<li class="tree-node%s" data-article-num="%d" data-depth="%d">
	<div class="node-content">
		<details open class="tree-details" style="display: inline;">
			<summary class="node-toggle" style="display: inline; list-style: none;">
				<span class="toggle-icon"></span>
			</summary>
		</details>
		<div class="node-link" data-article-num="%d" data-group="%s">
			<span class="node-subject">%s</span>
			<span class="node-author">by %s</span>
			<span class="node-date">%s</span>
			<span class="node-actions">
				<a href="/groups/%s/articles/%d" class="btn-view" title="View Article">👁</a>
				<button class="btn-preview" title="Preview" data-article="%d">📄</button>
			</span>
		</div>
	</div>
	<div class="article-preview" id="preview-%d" style="display: none;">
		<div class="preview-content">
			<div class="preview-loading">Loading...</div>
		</div>
	</div>`,
			expandClass, node.ArticleNum, node.Depth, node.ArticleNum, escGroup, subject, author, dateStr, hrefGroup, node.ArticleNum, node.ArticleNum, node.ArticleNum)

		out += `<ul class="tree-children">`
		for _, child := range node.Children {
			out += tree.getNodeHTML(child, groupName)
		}
		out += `</ul>`
	} else {
		out = fmt.Sprintf(`
<li class="tree-node%s" data-article-num="%d" data-depth="%d">
	<div class="node-content">
		<span class="node-toggle"></span>
		<div class="node-link" data-article-num="%d" data-group="%s">
			<span class="node-subject">%s</span>
			<span class="node-author">by %s</span>
			<span class="node-date">%s</span>
			<span class="node-actions">
				<a href="/groups/%s/articles/%d" class="btn-view" title="View Article">👁</a>
				<button class="btn-preview" title="Preview" data-article="%d">📄</button>
			</span>
		</div>
	</div>
	<div class="article-preview" id="preview-%d" style="display: none;">
		<div class="preview-content">
			<div class="preview-loading">Loading...</div>
		</div>
	</div>`,
			expandClass, node.ArticleNum, node.Depth, node.ArticleNum, escGroup, subject, author, dateStr, hrefGroup, node.ArticleNum, node.ArticleNum, node.ArticleNum)
	}

	out += `</li>`
	return out
}
