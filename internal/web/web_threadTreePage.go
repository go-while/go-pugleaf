// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
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

	// Return JSON response
	c.Header("Cache-Control", "public, max-age=300") // Cache for 5 minutes
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
		s.renderError(c, http.StatusInternalServerError, "Tree Error", "Failed to build thread tree: "+err.Error())
		return
	}

	// Get root article for title
	rootOverview, err := s.DB.GetOverviewByArticleNum(groupDB, threadRoot)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Article Not Found", "Root article not found")
		return
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
		"TreeHTML":            treeResponse.Tree.GetThreadTreeHTML(groupName),
		"TreeStats":           treeResponse.Tree.GetTreeStats(),
		"BuildTime":           treeResponse.BuildTime,
		"CacheHit":            treeResponse.CacheHit,
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/thread-tree.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
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
		s.renderError(c, http.StatusInternalServerError, "Tree Error", "Failed to build thread tree: "+err.Error())
		return
	}

	// Get root article for title
	rootOverview, err := s.DB.GetOverviewByArticleNum(groupDB, threadRoot)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Article Not Found", "Root article not found")
		return
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
		"TreeHTML":            treeResponse.Tree.GetThreadTreeHTML(groupName),
		"TreeStats":           treeResponse.Tree.GetTreeStats(),
		"BuildTime":           treeResponse.BuildTime,
		"CacheHit":            treeResponse.CacheHit,
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/thread-tree.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

// threadTreeDemoPage serves the tree view demo page
func (s *WebServer) threadTreeDemoPage(c *gin.Context) {
	// Serve the static demo HTML file
	c.File("./web/templates/thread-tree-demo.html")
}
