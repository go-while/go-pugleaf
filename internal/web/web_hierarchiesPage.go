// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// This file handles the hierarchies listing page

var LIMIT_GROUPS_IN_HIERARCHY_TREE = 128
var LIMIT_hierarchyGroupsPage = 256
var LIMIT_hierarchyTreePage = 384
var LIMIT_hierarchiesPage = 512

// Shows all available hierarchies with group counts and navigation
func (s *WebServer) hierarchiesPage(c *gin.Context) {
	// Get pagination parameters
	page := 1

	if p := c.Query("page"); p != "" && p != "1" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 1 {
			page = parsed
		}
	}

	// Get sort parameter
	sortBy := c.Query("sort")
	if sortBy == "" {
		sortBy = "activity" // Default to last activity sorting
	}

	hierarchies, totalCount, err := s.DB.GetHierarchiesPaginated(page, LIMIT_hierarchiesPage, sortBy)
	if err != nil {
		// Load error template individually
		tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/error.html"))
		c.Header("Content-Type", "text/html")
		tmpl.ExecuteTemplate(c.Writer, "base.html", gin.H{"Error": err.Error()})
		return
	}

	pagination := models.NewPaginationInfo(page, LIMIT_hierarchiesPage, totalCount)

	data := HierarchiesPageData{
		TemplateData: s.getBaseTemplateData(c, "Hierarchies"),
		Hierarchies:  hierarchies,
		Pagination:   pagination,
		SortBy:       sortBy,
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/hierarchies.html", "web/templates/pagination.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

// hierarchyGroupsPage displays all newsgroups within a specific hierarchy
func (s *WebServer) hierarchyGroupsPage(c *gin.Context) {
	hierarchyName := c.Param("hierarchy")
	if hierarchyName == "" {
		s.renderError(c, http.StatusBadRequest, "Missing hierarchy", "Hierarchy name is required")
		return
	}

	// Get pagination parameters
	page := 1

	if p := c.Query("page"); p != "" && p != "1" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 1 {
			page = parsed
		}
	}

	// Get sort parameter
	sortBy := c.Query("sort")
	if sortBy == "" {
		sortBy = "activity" // Default to last activity sorting
	}

	groups, totalCount, err := s.DB.GetNewsgroupsByHierarchy(hierarchyName, page, LIMIT_hierarchyGroupsPage, sortBy)
	if err != nil {
		// Load error template individually
		tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/error.html"))
		c.Header("Content-Type", "text/html")
		tmpl.ExecuteTemplate(c.Writer, "base.html", gin.H{"Error": err.Error()})
		return
	}

	pagination := models.NewPaginationInfo(page, LIMIT_hierarchyGroupsPage, totalCount)

	data := HierarchyGroupsPageData{
		TemplateData:  s.getBaseTemplateData(c, "Hierarchy: "+hierarchyName),
		HierarchyName: hierarchyName,
		Groups:        groups,
		Pagination:    pagination,
		SortBy:        sortBy,
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/hierarchy_groups.html", "web/templates/pagination.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

// adminUpdateHierarchies updates the hierarchy counts and structure
func (s *WebServer) adminUpdateHierarchies(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	// Update hierarchy counts
	err = s.DB.UpdateHierarchyCounts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update hierarchies: " + err.Error()})
		return
	}

	// Refresh hierarchy cache after update
	if s.DB.HierarchyCache != nil {
		s.DB.HierarchyCache.InvalidateAll()
		go s.DB.HierarchyCache.WarmCache(s.DB) // Warm cache in background
	}

	// Return success response as JSON
	c.JSON(http.StatusOK, gin.H{"message": "Hierarchies updated and cache refreshed successfully"})
}

// hierarchyTreePage displays a hierarchical tree view for browsing newsgroups
func (s *WebServer) hierarchyTreePage(c *gin.Context) {
	hierarchyName := c.Param("hierarchy")

	// Build path from individual level parameters
	var pathParts []string
	if level1 := c.Param("level1"); level1 != "" {
		pathParts = append(pathParts, level1)
		if level2 := c.Param("level2"); level2 != "" {
			pathParts = append(pathParts, level2)
			if level3 := c.Param("level3"); level3 != "" {
				pathParts = append(pathParts, level3)
			}
		}
	}

	path := strings.Join(pathParts, "/")

	// Build the full path for this level
	currentPath := hierarchyName
	if path != "" {
		currentPath = hierarchyName + "." + strings.ReplaceAll(path, "/", ".")
	}

	// RelativePath is the path without the hierarchy prefix (for URL construction)
	relativePath := path

	// Build breadcrumbs
	breadcrumbs := []HierarchyBreadcrumb{
		{Name: hierarchyName, Path: "/hierarchy/" + hierarchyName, IsLast: path == ""},
	}

	if path != "" {
		parts := strings.Split(path, "/")
		partialPath := hierarchyName
		for i, part := range parts {
			partialPath += "." + part
			pathParts := parts[:i+1]
			isLast := i == len(parts)-1
			breadcrumbs = append(breadcrumbs, HierarchyBreadcrumb{
				Name:   part,
				Path:   "/hierarchy/" + hierarchyName + "/" + strings.Join(pathParts, "/"),
				IsLast: isLast,
			})
		}
	}

	// Get parent path for "Up" navigation
	parentPath := ""
	if path != "" {
		parts := strings.Split(path, "/")
		if len(parts) > 1 {
			parentPath = "/hierarchy/" + hierarchyName + "/" + strings.Join(parts[:len(parts)-1], "/")
		} else {
			parentPath = "/hierarchy/" + hierarchyName
		}
	} else if hierarchyName != "" {
		parentPath = "/hierarchies"
	}

	// Get sort parameter
	sortBy := c.Query("sort")
	if sortBy == "" {
		sortBy = "activity" // Default to last activity sorting
	}

	// Get pagination parameters
	page := 1

	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	// Get sub-hierarchies and groups for this level
	subHierarchies, groups, totalSubHierarchies, totalGroups, err := s.getHierarchyLevel(currentPath, sortBy, page, LIMIT_hierarchyTreePage)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Database error", err.Error())
		return
	}

	// Calculate pagination based on sub-hierarchies (primary content)
	// Groups are shown as a preview/summary alongside sub-hierarchies
	var pagination *models.PaginationInfo

	if totalSubHierarchies > LIMIT_hierarchyTreePage {
		// We have more sub-hierarchies than can fit on one page
		pagination = models.NewPaginationInfo(page, LIMIT_hierarchyTreePage, totalSubHierarchies)
	}

	// Calculate if we're at maximum depth (level 3)
	currentDepth := len(pathParts)
	atMaxDepth := currentDepth >= 3

	data := HierarchyTreePageData{
		TemplateData:   s.getBaseTemplateData(c, "Hierarchy: "+currentPath),
		HierarchyName:  hierarchyName,
		CurrentPath:    currentPath,
		RelativePath:   relativePath,
		ParentPath:     parentPath,
		Breadcrumbs:    breadcrumbs,
		SubHierarchies: subHierarchies,
		Groups:         groups,
		TotalSubItems:  totalSubHierarchies,
		TotalGroups:    totalGroups,
		ShowingGroups:  totalGroups > 0,
		SortBy:         sortBy,
		Pagination:     pagination,
		AtMaxDepth:     atMaxDepth,
	}

	// Load template
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/hierarchy_tree.html", "web/templates/pagination.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

// getHierarchyLevel returns sub-hierarchies and groups for a given hierarchy level with pagination
func (s *WebServer) getHierarchyLevel(currentPath string, sortBy string, page int, pageSize int) ([]HierarchyNode, []*models.Newsgroup, int, int, error) {
	// Get sub-hierarchies with pagination
	subHierarchyMap, totalSubHierarchies, err := s.DB.GetHierarchySubLevels(currentPath, page, pageSize)
	if err != nil {
		return nil, nil, 0, 0, err
	}

	// Convert to HierarchyNode slice
	var subHierarchies []HierarchyNode
	for name, count := range subHierarchyMap {
		fullSubPath := currentPath + "." + name

		// Check if this sub-hierarchy has direct groups (limit check to avoid loading all)
		_, totalCount, _ := s.DB.GetDirectGroupsAtLevel(fullSubPath, "name", 1, 1)
		hasGroups := totalCount > 0

		subHierarchies = append(subHierarchies, HierarchyNode{
			Name:       name,
			FullPath:   fullSubPath,
			GroupCount: count,
			HasGroups:  hasGroups,
		})
	}

	// Sort sub-hierarchies alphabetically by name
	sort.Slice(subHierarchies, func(i, j int) bool {
		return subHierarchies[i].Name < subHierarchies[j].Name
	})

	// Get direct groups at this level - always get them for display
	var directGroups []*models.Newsgroup
	var totalGroups int

	// Always get direct groups at this level, but limit to avoid overwhelming the page
	// Show first N groups or nothing if more groups than pagesize limit
	directGroups, totalGroups, err = s.DB.GetDirectGroupsAtLevel(currentPath, sortBy, 1, LIMIT_GROUPS_IN_HIERARCHY_TREE)
	if err != nil {
		return nil, nil, 0, 0, err
	}

	return subHierarchies, directGroups, totalSubHierarchies, totalGroups, nil
}
