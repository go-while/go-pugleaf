// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// This file should contain the thread tree view related functions from server.go:
//
// Functions to be moved from server.go:
//   - func (s *WebServer) threadTreePage(c *gin.Context) (line ~1596)
//     Handles "/groups/:group/tree/:threadRoot" route for thread tree view
//   - func (s *WebServer) sectionThreadTreePage(c *gin.Context) (line ~1670)
//     Handles "/:section/:group/tree/:threadRoot" route for section thread tree view
//   - func (s *WebServer) threadTreeDemoPage(c *gin.Context) (line ~1526)
//     Handles "/demo/thread-tree" route for thread tree demo functionality
//   - func (s *WebServer) handleThreadTreeAPI(c *gin.Context) (line ~1532)
//     Handles "/api/thread-tree" API endpoint for thread tree data
//
// This file will handle all thread tree view functionality, including tree rendering,
// tree API endpoints, and tree demo pages.
// handleThreadTreeAPI handles the tree view API endpoint
func (s *WebServer) handleThreadTreeAPI(c *gin.Context) {
	groupName := c.Query("group")
	threadRootStr := c.Query("thread_root")

	if groupName == "" || threadRootStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Missing required parameters: group, thread_root",
		})
		return
	}

	threadRoot, err := strconv.ParseInt(threadRootStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid thread_root parameter",
		})
		return
	}

	// Check group access (exists + active, admin bypass) before opening any group DB:
	// GetGroupDB creates the database file for unknown names.
	if !s.checkGroupAccessAPI(c, groupName) {
		return // Error response already sent by checkGroupAccessAPI
	}

	// Get group database
	groupDB, err := s.DB.GetGroupDB(groupName)
	if err != nil {
		log.Printf("[WEB]: thread-tree API: GetGroupDB(%q) failed: %v", groupName, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to build thread tree",
		})
		return
	}
	defer groupDB.Return()

	// Parse options
	options := database.TreeViewOptions{
		MaxDepth:        0,    // No limit by default
		CollapseDepth:   3,    // Auto-collapse after depth 3
		IncludeOverview: true, // Include full overview data
		PageSize:        50,   // Standard page size
		SortBy:          "date",
	}

	// Override with query parameters if provided
	if maxDepthStr := c.Query("max_depth"); maxDepthStr != "" {
		if maxDepth, err := strconv.Atoi(maxDepthStr); err == nil {
			options.MaxDepth = min(max(maxDepth, 0), 50)
		}
	}

	if includeOverviewStr := c.Query("include_overview"); includeOverviewStr == "false" {
		options.IncludeOverview = false
	}

	// Get tree view
	response, err := s.DB.GetThreadTreeView(groupDB, threadRoot, options)
	if err != nil {
		log.Printf("[WEB]: thread-tree API: group %q root %d: %v", groupName, threadRoot, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to build thread tree",
		})
		return
	}

	// Per-viewer copy (the built tree may be cached and is left untouched): nodes whose overview
	// did not load get one from the group DB (treeOverviewFiller), hidden articles (D2) lose their
	// overview for non-admins, and every remaining From is the display name the HTML tree shows
	// (models.DisplayFrom), never the raw header with its address.
	viewer := *response
	viewer.Tree = treeForViewer(response.Tree, s.treeOverviewFiller(groupDB, options.IncludeOverview), !s.isAdminRequest(c), true)
	response = &viewer

	// Return JSON response. private: the body depends on the viewer (admin or not).
	c.Header("Cache-Control", "private, max-age=60")
	c.JSON(http.StatusOK, response)
}

// threadTreePage displays a thread in tree view format
func (s *WebServer) threadTreePage(c *gin.Context) {
	groupName := c.Param("group")
	threadRootStr := c.Param("threadRoot")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccess(c, groupName) {
		return // Error response already sent by checkGroupAccess
	}

	threadRoot, err := strconv.ParseInt(threadRootStr, 10, 64)
	if err != nil {
		s.renderError(c, http.StatusBadRequest, "Invalid Thread", "Invalid thread root parameter")
		return
	}

	// Get group database
	groupDB, err := s.DB.GetGroupDB(groupName)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Group Not Found", "Group not found: "+groupName)
		return
	}
	defer groupDB.Return()

	// Get tree view
	options := database.TreeViewOptions{
		MaxDepth:        0,    // No limit
		CollapseDepth:   3,    // Auto-collapse after depth 3
		IncludeOverview: true, // Include full overview data
		SortBy:          "date",
	}

	treeResponse, err := s.DB.GetThreadTreeView(groupDB, threadRoot, options)
	if err != nil {
		log.Printf("[WEB]: threadTreePage: GetThreadTreeView(%s/%d): %v", groupName, threadRoot, err)
		s.renderError(c, http.StatusInternalServerError, "Tree Error", publicErrorDetail)
		return
	}
	// Nodes whose overview did not load are filled from the group DB, so a hidden one renders
	// as "[hidden article]" for non-admins (the built tree itself is not modified).
	tree := treeForViewer(treeResponse.Tree, s.treeOverviewFiller(groupDB, true), false, false)

	// Get root article for title
	rootOverview, err := s.DB.GetOverviewByArticleNum(groupDB, threadRoot)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Article Not Found", "Root article not found")
		return
	}

	// A hidden root (D2) shows neither subject nor author to non-admins.
	showHidden := s.isAdminRequest(c)
	rootHidden := !showHidden && rootOverview.Hide > 0
	if rootHidden {
		rootOverview = &models.Overview{ArticleNum: rootOverview.ArticleNum, Subject: "[hidden article]", Hide: rootOverview.Hide}
	}

	// Get base template data with authentication context
	baseData := s.getBaseTemplateData(c, rootOverview.GetCleanSubject()+" - Thread Tree")

	data := gin.H{
		"Title":               baseData.Title,
		"CurrentTime":         baseData.CurrentTime,
		"Port":                baseData.Port,
		"NNTPtcpPort":         baseData.NNTPtcpPort,
		"NNTPtlsPort":         baseData.NNTPtlsPort,
		"GroupCount":          baseData.GroupCount,
		"User":                baseData.User,
		"IsAdmin":             baseData.IsAdmin,
		"AppVersion":          baseData.AppVersion,
		"RegistrationEnabled": baseData.RegistrationEnabled,
		"SiteNews":            baseData.SiteNews,
		"AvailableSections":   baseData.AvailableSections,
		"AvailableAIModels":   baseData.AvailableAIModels,
		"GroupName":           groupName,
		"ThreadRoot":          rootOverview,
		"RootHidden":          rootHidden, // placeholder root: no Message-ID, author, date or article link
		"TreeHTML":            tree.GetThreadTreeHTMLFor(groupName, showHidden),
		"TreeStats":           tree.GetTreeStats(),
		"BuildTime":           treeResponse.BuildTime,
		"CacheHit":            treeResponse.CacheHit,
	}

	s.renderPage(c, http.StatusOK, data, "thread-tree.html")
}

// sectionThreadTreePage displays a thread in tree view format within a section
func (s *WebServer) sectionThreadTreePage(c *gin.Context) {
	section := c.Param("section")
	// The group parameter is the full group name, like in the other section routes
	groupName := c.Param("group")
	threadRootStr := c.Param("threadRoot")

	// Unknown sections are rejected by sectionValidationMiddleware.
	// Section + group membership check before opening any group DB
	if _, ok := s.sectionGroupAllowed(c, section, groupName); !ok {
		return // Error response already sent by sectionGroupAllowed
	}

	threadRoot, err := strconv.ParseInt(threadRootStr, 10, 64)
	if err != nil {
		s.renderError(c, http.StatusBadRequest, "Invalid Thread", "Invalid thread root parameter")
		return
	}

	// Get group database
	groupDB, err := s.DB.GetGroupDB(groupName)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Group Not Found", "Group not found: "+groupName)
		return
	}
	defer groupDB.Return()

	// Get tree view
	options := database.TreeViewOptions{
		MaxDepth:        0,    // No limit
		CollapseDepth:   3,    // Auto-collapse after depth 3
		IncludeOverview: true, // Include full overview data
		SortBy:          "date",
	}

	treeResponse, err := s.DB.GetThreadTreeView(groupDB, threadRoot, options)
	if err != nil {
		log.Printf("[WEB]: sectionThreadTreePage: GetThreadTreeView(%s/%s/%d): %v", section, groupName, threadRoot, err)
		s.renderError(c, http.StatusInternalServerError, "Tree Error", publicErrorDetail)
		return
	}
	// Nodes whose overview did not load are filled from the group DB (see threadTreePage).
	tree := treeForViewer(treeResponse.Tree, s.treeOverviewFiller(groupDB, true), false, false)

	// Get root article for title
	rootOverview, err := s.DB.GetOverviewByArticleNum(groupDB, threadRoot)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Article Not Found", "Root article not found")
		return
	}

	// A hidden root (D2) shows neither subject nor author to non-admins.
	showHidden := s.isAdminRequest(c)
	rootHidden := !showHidden && rootOverview.Hide > 0
	if rootHidden {
		rootOverview = &models.Overview{ArticleNum: rootOverview.ArticleNum, Subject: "[hidden article]", Hide: rootOverview.Hide}
	}

	// Get base template data with authentication context
	baseData := s.getBaseTemplateData(c, rootOverview.GetCleanSubject()+" - Thread Tree")

	data := gin.H{
		"Title":               baseData.Title,
		"CurrentTime":         baseData.CurrentTime,
		"Port":                baseData.Port,
		"NNTPtcpPort":         baseData.NNTPtcpPort,
		"NNTPtlsPort":         baseData.NNTPtlsPort,
		"GroupCount":          baseData.GroupCount,
		"User":                baseData.User,
		"IsAdmin":             baseData.IsAdmin,
		"AppVersion":          baseData.AppVersion,
		"RegistrationEnabled": baseData.RegistrationEnabled,
		"SiteNews":            baseData.SiteNews,
		"AvailableSections":   baseData.AvailableSections,
		"AvailableAIModels":   baseData.AvailableAIModels,
		"Section":             section,
		"Group":               groupName,
		"GroupName":           groupName,
		"ThreadRoot":          rootOverview,
		"RootHidden":          rootHidden, // placeholder root: no Message-ID, author, date or article link
		"TreeHTML":            tree.GetThreadTreeHTMLFor(groupName, showHidden),
		"TreeStats":           tree.GetTreeStats(),
		"BuildTime":           treeResponse.BuildTime,
		"CacheHit":            treeResponse.CacheHit,
	}

	s.renderPage(c, http.StatusOK, data, "thread-tree.html")
}

// threadTreeDemoPage serves the tree view demo page
func (s *WebServer) threadTreeDemoPage(c *gin.Context) {
	// Serve the static demo HTML file
	c.File("./web/templates/thread-tree-demo.html")
}

// Subject of the placeholder overview treeOverviewFiller gives a node whose article row exists
// but whose overview could not be read.
const treeOverviewUnavailable = "[overview not available]"

// treeOverviewFiller returns the function treeForViewer uses for a node without an overview
// (GetThreadTreeView logs and skips an overview it cannot load; the HTML tree then shows
// "Unknown Subject" and cannot tell a hidden article from a visible one). It reads the overview
// once more; when that fails for another reason than a missing row, it reads the spam and hide
// counters (GetArticleFlags, as the article routes do) and returns a placeholder that carries
// them. So a hidden article stays hidden for non-admins even when its overview does not load,
// and a visible placeholder links to an article page that exists, where an admin can hide it
// as usual. A missing row (expired or never stored) returns nil: the node keeps "Unknown
// Subject" and its link answers the article page's "Article #N not found in group" message.
// With enabled false (the API's include_overview=false) the filler is nil.
func (s *WebServer) treeOverviewFiller(groupDB *database.GroupDB, enabled bool) func(articleNum int64) *models.Overview {
	if !enabled || groupDB == nil {
		return nil
	}
	return func(articleNum int64) *models.Overview {
		ov, err := s.DB.GetOverviewByArticleNum(groupDB, articleNum)
		if err == nil {
			return ov
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		spam, hide, ferr := s.DB.GetArticleFlags(groupDB, articleNum)
		if ferr != nil {
			if !errors.Is(ferr, sql.ErrNoRows) {
				log.Printf("[WEB]: thread tree: overview and flags of article %d unavailable: %v / %v", articleNum, err, ferr)
			}
			return nil
		}
		return &models.Overview{ArticleNum: articleNum, Subject: treeOverviewUnavailable, Spam: spam, Hide: hide}
	}
}

// treeForViewer returns a deep copy of tree (nodes and NodeMap) for one response; tree may be
// cached and shared between requests and is never modified. A node without an overview gets
// fill(articleNum) when fill is set. With redact, a node whose article is hidden (Hide > 0) has
// no overview (as ThreadTree.RedactedCopy). With displayFrom, every remaining overview is a copy
// whose FromHeader is models.DisplayFrom of the raw header: the author name the HTML tree shows,
// without the address.
func treeForViewer(tree *database.ThreadTree, fill func(articleNum int64) *models.Overview, redact, displayFrom bool) *database.ThreadTree {
	if tree == nil {
		return nil
	}
	cp := *tree
	copies := make(map[*database.TreeNode]*database.TreeNode, len(tree.NodeMap)+1)
	var copyNode func(node *database.TreeNode) *database.TreeNode
	copyNode = func(node *database.TreeNode) *database.TreeNode {
		if node == nil {
			return nil
		}
		if done, ok := copies[node]; ok {
			return done
		}
		n := *node
		copies[node] = &n
		if node.ParentArticle != nil {
			parent := *node.ParentArticle
			n.ParentArticle = &parent
		}
		if n.Overview == nil && fill != nil {
			n.Overview = fill(node.ArticleNum)
		}
		if redact && n.Overview != nil && n.Overview.Hide > 0 {
			n.Overview = nil
		}
		if displayFrom && n.Overview != nil {
			ov := *n.Overview
			ov.FromHeader = models.DisplayFrom(ov.FromHeader)
			n.Overview = &ov
		}
		if node.Children != nil {
			n.Children = make([]*database.TreeNode, len(node.Children))
			for i, child := range node.Children {
				n.Children[i] = copyNode(child)
			}
		}
		return &n
	}
	cp.RootNode = copyNode(tree.RootNode)
	if tree.NodeMap != nil {
		cp.NodeMap = make(map[int64]*database.TreeNode, len(tree.NodeMap))
		for num, node := range tree.NodeMap {
			cp.NodeMap[num] = copyNode(node)
		}
	}
	return &cp
}
