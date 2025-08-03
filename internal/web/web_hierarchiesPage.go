// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"net/http"
	"strconv"

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
