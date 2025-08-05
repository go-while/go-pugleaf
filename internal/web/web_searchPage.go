// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// This file should contain the search page related functions from server.go:
//
// Functions to be moved from server.go:
//   - func (s *WebServer) searchPage(c *gin.Context) (line ~784)
//     Handles "/search" route for searching articles and groups
//
// This file will handle the search functionality, allowing users to search for articles
// and groups across the NNTP server.
func (s *WebServer) searchPage(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	searchType := c.DefaultQuery("searchType", "all")

	// Parse pagination parameters
	page := 1
	pageSize := 50 // Results per page
	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	// Use getBaseTemplateData to include user authentication context
	baseData := s.getBaseTemplateData(c, "Search - go-pugleaf")

	data := SearchPageData{
		TemplateData: baseData,
		Query:        query,
		SearchType:   searchType,
		HasResults:   false,
		ResultCount:  0,
	}

	// If there's a search query, perform the search
	if query != "" {
		switch searchType {
		case "groups":
			// Search in group names only with pagination
			offset := (page - 1) * pageSize
			groups, err := s.DB.SearchNewsgroups(query, pageSize, offset)
			if err != nil {
				log.Printf("Error searching groups: %v", err)
				s.renderError(c, http.StatusInternalServerError, "Search Error", err.Error())
				return
			}

			// Get total count for pagination
			totalCount, err := s.DB.CountSearchNewsgroups(query)
			if err != nil {
				log.Printf("Error counting search results: %v", err)
				totalCount = len(groups) // Fallback to current results count
			}

			// Create pagination info
			pagination := models.NewPaginationInfo(page, pageSize, totalCount)

			data.Results = groups
			data.ResultCount = totalCount
			data.HasResults = len(groups) > 0
			data.Pagination = pagination
			data.Title = template.HTML("Search Results: " + query)

		case "subjects":
			// TODO: Implement article subject search
			data.Results = []*models.Overview{}
			data.ResultCount = 0
			data.HasResults = false

		case "authors":
			// TODO: Implement author search
			data.Results = []*models.Overview{}
			data.ResultCount = 0
			data.HasResults = false

		case "all":
		default:
			// TODO: Implement combined search (groups + articles)
			offset := (page - 1) * pageSize
			groups, err := s.DB.SearchNewsgroups(query, pageSize, offset)
			if err != nil {
				log.Printf("Error searching: %v", err)
				s.renderError(c, http.StatusInternalServerError, "Search Error", err.Error())
				return
			}

			// Get total count for pagination
			totalCount, err := s.DB.CountSearchNewsgroups(query)
			if err != nil {
				log.Printf("Error counting search results: %v", err)
				totalCount = len(groups) // Fallback to current results count
			}

			// Create pagination info
			pagination := models.NewPaginationInfo(page, pageSize, totalCount)

			data.Results = groups
			data.ResultCount = totalCount
			data.HasResults = len(groups) > 0
			data.Pagination = pagination
			data.Title = template.HTML("Search Results: " + query)
		}
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/search.html"))
	c.Header("Content-Type", "text/html")
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}
