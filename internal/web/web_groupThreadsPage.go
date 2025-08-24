package web

import (
	"html/template"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

const Threads_perPage int64 = 50 // Static page size for optimal caching

func (s *WebServer) groupThreadsPage(c *gin.Context) {
	groupName := c.Param("group")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccess(c, groupName) {
		return // Error response already sent by checkGroupAccess
	}

	// Get pagination parameters
	pageStr := c.DefaultQuery("page", "1")
	page, err := strconv.ParseInt(pageStr, 10, 64)
	if err != nil || page < 1 {
		page = 1
	}

	// Get group database connections
	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		log.Printf("Failed to get group databases for %s: %v", groupName, err)
		s.renderError(c, http.StatusInternalServerError, "Database error", err.Error())
		return
	}
	defer groupDBs.Return(s.DB)

	// Use cached thread data for fast performance
	forumThreads, totalThreads, err := s.DB.GetCachedThreads(groupDBs, page, Threads_perPage)
	if err != nil {
		log.Printf("Failed to get cached threads for %s: %v", groupName, err)
		s.renderError(c, http.StatusInternalServerError, "Failed to load threads", err.Error())
		return
	}

	// Batch sanitize all root articles for better performance
	var rootOverviews []*models.Overview
	for _, ft := range forumThreads {
		if ft.RootArticle != nil {
			// Initialize ArticleNums map if nil
			if ft.RootArticle.ArticleNums == nil {
				ft.RootArticle.ArticleNums = make(map[*string]int64)
			}
			// Store the article number for this newsgroup
			ft.RootArticle.ArticleNums[groupDBs.NewsgroupPtr] = ft.RootArticle.ArticleNum
			rootOverviews = append(rootOverviews, ft.RootArticle)
		}
	}
	models.BatchSanitizeOverviews(rootOverviews)

	// Calculate pagination info
	totalPages := (totalThreads + Threads_perPage - 1) / Threads_perPage
	if page > totalPages && totalPages > 0 {
		page = totalPages
	}

	// Calculate total messages across all threads
	totalMessages := 0
	for _, ft := range forumThreads {
		totalMessages += ft.MessageCount
	}

	// Get base template data with authentication context
	baseData := s.getBaseTemplateData(c, groupName+" - Threads")

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
		"GroupPtr":            groupDBs.NewsgroupPtr,
		"ForumThreads":        forumThreads,
		"TotalThreads":        totalThreads,
		"TotalMessages":       totalMessages,
		"CurrentPage":         page,
		"TotalPages":          totalPages,
		"PerPage":             Threads_perPage,
		"HasPrevPage":         page > 1,
		"HasNextPage":         page < totalPages,
		"PrevPage":            page - 1,
		"NextPage":            page + 1,
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/threads.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}
