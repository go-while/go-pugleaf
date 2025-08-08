// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// This file should contain the API endpoint functions from server.go:
var LIMIT_listGroups = 128

// Functions to be moved from server.go:
// - func (s *WebServer) listGroups(c *gin.Context) (line ~313)
//   API endpoint for "/api/v1/groups" to return JSON list of groups
// - func (s *WebServer) getGroupOverview(c *gin.Context) (line ~354)
//   API endpoint for "/api/v1/groups/:group/overview" to return group overview JSON
// - func (s *WebServer) getArticle(c *gin.Context) (line ~403)
//   API endpoint for "/api/v1/groups/:group/articles/:articleNum" to return article JSON
// - func (s *WebServer) getArticleByMessageId(c *gin.Context) (line ~428)
//   API endpoint for "/api/v1/groups/:group/message/:messageId" to return article JSON
// - func (s *WebServer) getGroupThreads(c *gin.Context) (line ~447)
//   API endpoint for "/api/v1/groups/:group/threads" to return group threads JSON
// - func (s *WebServer) getStats(c *gin.Context) (line ~466)
//   API endpoint for "/api/v1/stats" to return server statistics JSON
//
// Note: Thread tree API function (handleThreadTreeAPI) is in threadTreePage.go

func (s *WebServer) listGroups(c *gin.Context) {
	// Get pagination parameters
	page := 1
	//pageSize := 50 // Default page size

	if p := c.Query("page"); p != "" && p != "1" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	groups, totalCount, err := s.DB.GetNewsgroupsPaginated(page, LIMIT_listGroups)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	totalPages := (totalCount + LIMIT_listGroups - 1) / LIMIT_listGroups
	if totalPages == 0 {
		totalPages = 1
	}

	response := models.PaginatedResponse{
		Data:       groups,
		Page:       page,
		PageSize:   LIMIT_listGroups,
		TotalCount: totalCount,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
		HasPrev:    page > 1,
	}

	c.JSON(http.StatusOK, response)
}

func (s *WebServer) getGroupOverview(c *gin.Context) {
	groupName := c.Param("group")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccessAPI(c, groupName) {
		return // Error response already sent by checkGroupAccessAPI
	}

	// Get pagination parameters
	page := 1
	//pageSize := 50 // Default page size for articles (standardized)
	var lastArticleNum int64

	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	// Support cursor-based pagination for better performance
	if cursor := c.Query("cursor"); cursor != "" {
		if parsed, err := strconv.ParseInt(cursor, 10, 64); err == nil && parsed > 0 {
			lastArticleNum = parsed
			page = 0 // Indicate cursor-based pagination
		}
	}

	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
		return
	}
	defer groupDBs.Return(s.DB)

	// Handle page-based to cursor conversion for compatibility
	if page > 1 && lastArticleNum == 0 {
		skipCount := (page - 1) * LIMIT_listGroups
		var cursorArticleNum int64
		err = database.RetryableQueryRowScan(groupDBs.DB, `
			SELECT article_num FROM articles
			WHERE hide = 0
			ORDER BY article_num DESC
			LIMIT 1 OFFSET ?`, []interface{}{skipCount - 1}, &cursorArticleNum)
		if err != nil {
			lastArticleNum = 0
		} else {
			lastArticleNum = cursorArticleNum
		}
	}

	overviews, totalCount, hasMore, err := s.DB.GetOverviewsPaginated(groupDBs, lastArticleNum, LIMIT_listGroups)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var totalPages int
	var hasNext, hasPrev bool

	if page > 0 {
		// Page-based response
		totalPages = (totalCount + LIMIT_listGroups - 1) / LIMIT_listGroups
		if totalPages == 0 {
			totalPages = 1
		}
		hasNext = page < totalPages
		hasPrev = page > 1
	} else {
		// Cursor-based response
		totalPages = (totalCount + LIMIT_listGroups - 1) / LIMIT_listGroups
		hasNext = hasMore
		hasPrev = lastArticleNum > 0
		page = 1 // For response consistency
	}

	response := models.PaginatedResponse{
		Data:       overviews,
		Page:       page,
		PageSize:   LIMIT_listGroups,
		TotalCount: totalCount,
		TotalPages: totalPages,
		HasNext:    hasNext,
		HasPrev:    hasPrev,
	}

	c.JSON(http.StatusOK, response)
}

func (s *WebServer) getArticle(c *gin.Context) {
	groupName := c.Param("group")
	articleNumStr := c.Param("articleNum")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccessAPI(c, groupName) {
		return // Error response already sent by checkGroupAccessAPI
	}

	articleNum, err := strconv.ParseInt(articleNumStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid article number"})
		return
	}

	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
		return
	}
	defer groupDBs.Return(s.DB)
	article, err := s.DB.GetArticleByNum(groupDBs, articleNum)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Article not found"})
		return
	}

	c.JSON(http.StatusOK, article)
}

func (s *WebServer) getArticleByMessageId(c *gin.Context) {
	groupName := c.Param("group")
	messageId := c.Param("messageId")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccessAPI(c, groupName) {
		return // Error response already sent by checkGroupAccessAPI
	}

	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
		return
	}
	defer groupDBs.Return(s.DB)
	article, err := s.DB.GetArticleByMessageID(groupDBs, messageId)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Article not found"})
		return
	}

	c.JSON(http.StatusOK, article)
}

func (s *WebServer) getGroupThreads(c *gin.Context) {
	groupName := c.Param("group")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccessAPI(c, groupName) {
		return // Error response already sent by checkGroupAccessAPI
	}

	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
		return
	}
	defer groupDBs.Return(s.DB)
	threads, err := s.DB.GetThreads(groupDBs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, threads)
}

// getStats returns JSON statistics data for the API
func (s *WebServer) getStats(c *gin.Context) {
	groups, err := s.DB.GetActiveNewsgroups()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get statistics"})
		return
	}

	// Calculate total articles and other stats
	var totalArticles int64 = 0
	totalThreads := 0
	activeGroups := 0
	var oldestArticle, newestArticle time.Time
	topGroups := make([]gin.H, 0)

	for _, group := range groups {
		if group.MessageCount > 0 {
			activeGroups++
			totalArticles += group.MessageCount

			// Add to top groups (for now, all groups - could be limited/sorted later)
			topGroups = append(topGroups, gin.H{
				"name":          group.Name,
				"article_count": int(group.MessageCount), // Convert to int for consistency
				"total_size":    0,                       // Could calculate actual size if needed
			})
		}

		// Update oldest/newest article dates (simplified - could get actual dates from DB)
		if !group.CreatedAt.IsZero() {
			if oldestArticle.IsZero() || group.CreatedAt.Before(oldestArticle) {
				oldestArticle = group.CreatedAt
			}
			if newestArticle.IsZero() || group.CreatedAt.After(newestArticle) {
				newestArticle = group.CreatedAt
			}
		}
	}

	// Sort top groups by article count (descending)
	sort.Slice(topGroups, func(i, j int) bool {
		return topGroups[i]["article_count"].(int) > topGroups[j]["article_count"].(int)
	})

	// Limit to top 10
	if len(topGroups) > 10 {
		topGroups = topGroups[:10]
	}

	stats := gin.H{
		"total_groups":    len(groups),
		"active_groups":   activeGroups,
		"total_articles":  int(totalArticles), // Convert to int for JSON
		"total_threads":   totalThreads,       // Could calculate actual thread count if needed
		"total_size":      0,                  // Could calculate actual size if needed
		"top_groups":      topGroups,
		"last_update":     time.Now().Format(time.RFC3339),
		"backend_version": config.AppVersion, // Use the server's version
		"uptime":          "Running",         // Could track actual uptime
	}

	if !oldestArticle.IsZero() {
		stats["oldest_article"] = oldestArticle.Format(time.RFC3339)
	}
	if !newestArticle.IsZero() {
		stats["newest_article"] = newestArticle.Format(time.RFC3339)
	}

	c.JSON(http.StatusOK, stats)
}

// getArticlePreview handles public article preview requests (no auth required)
func (s *WebServer) getArticlePreview(c *gin.Context) {
	groupName := c.Param("group")
	articleNumStr := c.Param("articleNum")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccessAPI(c, groupName) {
		return // Error response already sent by checkGroupAccessAPI
	}

	articleNum, err := strconv.ParseInt(articleNumStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid article number"})
		return
	}

	// Get group database
	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
		return
	}
	defer groupDBs.Return(s.DB)

	// Get article overview for basic info
	overview, err := s.DB.GetOverviewByArticleNum(groupDBs, articleNum)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Article not found"})
		return
	}

	// Get article content (limited for preview)
	article, err := s.DB.GetArticleByNum(groupDBs, articleNum)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Article content not found"})
		return
	}

	// Create preview response with limited content
	// Use ConvertToUTF8 to decode the text properly but without HTML escaping (since JS will handle HTML context)
	// This is the same decoding used by PrintSanitized but without the html.EscapeString step
	fullBodyDecoded := models.ConvertToUTF8(article.BodyText)
	bodyPreview := ""
	if len(fullBodyDecoded) > 500 {
		bodyPreview = fullBodyDecoded[:500] + "..."
	} else {
		bodyPreview = fullBodyDecoded
	}

	response := gin.H{
		"article_num": articleNum,
		"group":       groupName,
		"subject":     string(overview.PrintSanitized("subject", groupName)),
		"from":        string(overview.PrintSanitized("fromheader", groupName)),
		"date":        overview.DateSent.Format("2006-01-02 15:04:05"),
		"lines":       overview.Lines,
		"body":        bodyPreview,
	}

	c.JSON(http.StatusOK, response)
}
