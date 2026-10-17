// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
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
	totalCount := int(s.DB.MainDBGetNewsgroupsActiveCount())

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

	// Get API enabled status
	apiEnabledStr, _ := s.DB.GetConfigValue(config.CFG_KEY_API_ENABLED)
	apiEnabled := apiEnabledStr == "true"

	data := StatsPageData{
		TemplateData:  s.getBaseTemplateData(c, "System Statistics"),
		Groups:        topGroups, // Only top 10 most active groups
		TotalArticles: totalArticles,
		APIEnabled:    apiEnabled,
	}
	data.GroupCount = totalCount

	s.renderPage(c, http.StatusOK, data, "stats.html")
}
