// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// This file handles the hierarchies listing page
// Shows all available Usenet hierarchies with group counts and navigation

func (s *WebServer) hierarchiesPage(c *gin.Context) {
	// Get pagination parameters
	page := 1
	pageSize := 50 // Default page size for hierarchies

	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	if ps := c.Query("page_size"); ps != "" {
		if parsed, err := strconv.Atoi(ps); err == nil && parsed > 0 && parsed <= 200 {
			pageSize = parsed
		}
	}

	// Get sort parameter
	sortBy := c.Query("sort")
	if sortBy == "" {
		sortBy = "activity" // Default to last activity sorting
	}

	hierarchies, totalCount, err := s.DB.GetHierarchiesPaginated(page, pageSize, sortBy)
	if err != nil {
		// Load error template individually
		tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/error.html"))
		c.Header("Content-Type", "text/html")
		tmpl.ExecuteTemplate(c.Writer, "base.html", gin.H{"Error": err.Error()})
		return
	}

	pagination := models.NewPaginationInfo(page, pageSize, totalCount)

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
	pageSize := 50 // Default page size for groups

	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	if ps := c.Query("page_size"); ps != "" {
		if parsed, err := strconv.Atoi(ps); err == nil && parsed > 0 && parsed <= 200 {
			pageSize = parsed
		}
	}

	// Get sort parameter
	sortBy := c.Query("sort")
	if sortBy == "" {
		sortBy = "activity" // Default to last activity sorting
	}

	groups, totalCount, err := s.DB.GetNewsgroupsByHierarchy(hierarchyName, page, pageSize, sortBy)
	if err != nil {
		// Load error template individually
		tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/error.html"))
		c.Header("Content-Type", "text/html")
		tmpl.ExecuteTemplate(c.Writer, "base.html", gin.H{"Error": err.Error()})
		return
	}

	pagination := models.NewPaginationInfo(page, pageSize, totalCount)

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

	// Return success response as JSON
	c.JSON(http.StatusOK, gin.H{"message": "Hierarchies updated successfully"})
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

	// Get sub-hierarchies and groups for this level
	subHierarchies, groups, err := s.getHierarchyLevel(currentPath)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Database error", err.Error())
		return
	}

	data := HierarchyTreePageData{
		TemplateData:   s.getBaseTemplateData(c, "Hierarchy: "+currentPath),
		HierarchyName:  hierarchyName,
		CurrentPath:    currentPath,
		RelativePath:   relativePath,
		ParentPath:     parentPath,
		Breadcrumbs:    breadcrumbs,
		SubHierarchies: subHierarchies,
		Groups:         groups,
		TotalSubItems:  len(subHierarchies),
		TotalGroups:    len(groups),
		ShowingGroups:  len(groups) > 0,
	}

	// Load template
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/hierarchy_tree.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

// getHierarchyLevel returns sub-hierarchies and groups for a given hierarchy level
func (s *WebServer) getHierarchyLevel(currentPath string) ([]HierarchyNode, []*models.Newsgroup, error) {
	// Get all newsgroups that start with the current path
	allGroups, err := s.DB.GetNewsgroupsByPrefix(currentPath + ".")
	if err != nil {
		return nil, nil, err
	}

	// Organize groups into sub-hierarchies and direct groups
	subHierarchyMap := make(map[string]*HierarchyNode)
	var directGroups []*models.Newsgroup

	for _, group := range allGroups {
		// Remove the current path prefix
		remainder := strings.TrimPrefix(group.Name, currentPath+".")

		// If there's a dot in the remainder, it's a sub-hierarchy
		if dotIndex := strings.Index(remainder, "."); dotIndex > 0 {
			subHierarchyName := remainder[:dotIndex]
			fullSubPath := currentPath + "." + subHierarchyName

			if node, exists := subHierarchyMap[subHierarchyName]; exists {
				node.GroupCount++
			} else {
				subHierarchyMap[subHierarchyName] = &HierarchyNode{
					Name:       subHierarchyName,
					FullPath:   fullSubPath,
					GroupCount: 1,
					HasGroups:  false,
				}
			}
		} else {
			// This is a direct group at this level
			directGroups = append(directGroups, group)
		}
	}

	// Convert map to slice for sub-hierarchies
	var subHierarchies []HierarchyNode
	for _, node := range subHierarchyMap {
		// Check if this sub-hierarchy has direct groups
		directGroupsInSub, _ := s.DB.GetNewsgroupsByExactPrefix(node.FullPath)
		node.HasGroups = len(directGroupsInSub) > 0
		subHierarchies = append(subHierarchies, *node)
	}

	return subHierarchies, directGroups, nil
}
