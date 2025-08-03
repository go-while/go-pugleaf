// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"
)

// This file should contain the statistics page related functions from server.go:
//
// Functions to be moved from server.go:
//   - func (s *WebServer) statsPage(c *gin.Context) (line ~857)
//     Handles "/stats" route to display server statistics
//
// This file will handle the statistics page functionality, showing server and NNTP statistics.
func (s *WebServer) statsPage(c *gin.Context) {
	// Get total count without loading all groups
	totalCount := int(s.DB.MainDBGetNewsgroupsCount())

	// Get top 10 most active groups ordered by message count
	topGroups, _ := s.DB.GetTopGroupsByMessageCount(10)

	// Calculate total articles across all groups
	allGroups, err := s.DB.GetActiveNewsgroups()
	var totalArticles int64 = 0
	if err == nil {
		for _, group := range allGroups {
			if group.MessageCount > 0 {
				totalArticles += group.MessageCount
			}
		}
	}

	data := StatsPageData{
		TemplateData:  s.getBaseTemplateData(c, "System Statistics"),
		Groups:        topGroups, // Only top 10 most active groups
		TotalArticles: totalArticles,
	}
	data.GroupCount = totalCount

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/stats.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}
