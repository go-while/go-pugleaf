// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// This file should contain the thread-related page functions from server.go:
//
// Functions to be moved from server.go:
//   - func (s *WebServer) singleThreadPage(c *gin.Context) (line ~1394)
//     Handles "/groups/:group/thread/:threadRoot" route to display a single thread flat view
//
// This file will handle thread-specific page functionality for single thread flat views.
// Note: Tree-related functions are in threadTreePage.go
const ThreadMessages_perPage int = 50 // Static page size for optimal caching

// singleThreadPage displays all messages in a single thread as a flat list with pagination
func (s *WebServer) singleThreadPage(c *gin.Context) {
	groupName := c.Param("group")
	threadRootStr := c.Param("threadRoot")

	threadRoot, err := strconv.ParseInt(threadRootStr, 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid thread root: %v", err)
		return
	}

	// Get pagination parameters
	pageStr := c.DefaultQuery("page", "1")
	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 1 {
		page = 1
	}

	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		c.String(http.StatusNotFound, "Group not found: %v", err)
		return
	}
	defer groupDBs.Return(s.DB)

	// Get the thread root overview first
	rootOverview, err := s.DB.GetOverviewByArticleNum(groupDBs, threadRoot)
	if err != nil {
		c.String(http.StatusNotFound, "Thread root article %d not found: %v", threadRoot, err)
		return
	}

	// Check if thread root is hidden
	if rootOverview.Hide != 0 {
		c.String(http.StatusNotFound, "Thread root article %d is hidden", threadRoot)
		return
	}

	// Use cached thread replies with pagination
	threadReplies, totalReplies, err := s.DB.GetCachedThreadReplies(groupDBs, threadRoot, page, ThreadMessages_perPage)
	if err != nil {
		log.Printf("Failed to get cached thread replies for %s/%d: %v", groupName, threadRoot, err)
		s.renderError(c, http.StatusInternalServerError, "Failed to load thread replies", err.Error())
		return
	}

	// Total messages = root + replies
	totalMessages := totalReplies + 1
	totalPages := (totalMessages + ThreadMessages_perPage - 1) / ThreadMessages_perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	// Prepare overviews for this page (root + current page of replies)
	var pageOverviews []*models.Overview

	// On page 1, include the root article first
	if page == 1 {
		pageOverviews = append(pageOverviews, rootOverview)
	}

	// Add the replies for this page
	pageOverviews = append(pageOverviews, threadReplies...)

	// Load full articles for the current page only
	var threadMessages []*models.Article
	for _, overview := range pageOverviews {
		// Skip hidden articles as an extra safety check
		if overview.Hide != 0 {
			log.Printf("Warning: Skipping hidden article %d in thread %d", overview.ArticleNum, threadRoot)
			continue
		}

		article, err := s.DB.GetArticleByNum(groupDBs, overview.ArticleNum)
		if err != nil {
			log.Printf("Warning: Could not load article %d: %v", overview.ArticleNum, err)
			// If we can't load the full article, we could fall back to overview data
			// but for now let's skip missing articles
			continue
		}
		threadMessages = append(threadMessages, article)
	}

	// Batch sanitize all thread articles for better performance
	models.BatchSanitizeArticles(threadMessages)

	// Get base template data with authentication context
	baseData := s.getBaseTemplateData(c, rootOverview.GetCleanSubject()+" - Thread")

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
		"ThreadMessages":      threadMessages,
		"MessageCount":        totalMessages,
		"CurrentPage":         page,
		"TotalPages":          totalPages,
		"PerPage":             ThreadMessages_perPage,
		"HasPrevPage":         page > 1,
		"HasNextPage":         page < totalPages,
		"PrevPage":            page - 1,
		"NextPage":            page + 1,
		"ThreadRootNum":       threadRoot, // For pagination URLs
	}

	// Load template
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/thread.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}
