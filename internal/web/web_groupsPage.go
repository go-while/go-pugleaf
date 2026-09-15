// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// This file should contain the groups listing page related functions from server.go:
//
// Functions to be moved from server.go:
// - func (s *WebServer) groupsPage(c *gin.Context) (line ~553)
//   Handles "/groups" route to display all available newsgroups
//
// This file will handle the groups listing functionality, showing all available newsgroups.

func (s *WebServer) groupsPage(c *gin.Context) {
	// Get pagination parameters
	page := 1
	pageSize := 50 // Static page size for groups (cache efficiency)

	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	// Try to get from cache first
	groups, totalCount, fromCache := models.GetCachedNewsgroups(page, pageSize)
	var err error

	if !fromCache {
		// Cache miss - fetch from database
		groups, totalCount, err = s.DB.GetNewsgroupsPaginated(page, pageSize)
		if err != nil {
			s.renderPage(c, http.StatusOK, gin.H{"Error": err.Error()}, "error.html")
			return
		}

		// Store in cache for future requests
		models.SetCachedNewsgroups(page, pageSize, groups, totalCount)
	}

	pagination := models.NewPaginationInfo(page, pageSize, totalCount)

	data := GroupsPageData{
		TemplateData: s.getBaseTemplateData(c, "Newsgroups"),
		Groups:       groups,
		Pagination:   pagination,
	}
	data.GroupCount = totalCount

	s.renderPage(c, http.StatusOK, data, "groups.html", "pagination.html")
}
