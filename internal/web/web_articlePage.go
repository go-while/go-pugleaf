// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// This file should contain the article page related functions from server.go:
//
// Functions to be moved from server.go:
//   - func (s *WebServer) articlePage(c *gin.Context) (line ~671)
//     Handles "/groups/:group/articles/:articleNum" route to display a specific article
//   - func (s *WebServer) articleByMessageIdPage(c *gin.Context) (line ~739)
//     Handles "/groups/:group/message/:messageId" route to display article by message ID
//
// This file will handle the individual article view functionality, displaying specific articles
// either by article number or message ID.
func (s *WebServer) articlePage(c *gin.Context) {
	groupName := c.Param("group")
	articleNumStr := c.Param("articleNum")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccess(c, groupName) {
		return // Error response already sent by checkGroupAccess
	}

	articleNum, err := strconv.ParseInt(articleNumStr, 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid article number: %v", err)
		return
	}

	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if groupDBs == nil || err != nil {
		c.String(http.StatusNotFound, "Group not found: %v", err)
		return
	}
	defer groupDBs.Return(s.DB)
	if groupDBs.NewsgroupPtr == nil {
		c.String(http.StatusInternalServerError, "Group pointer is nil for group %s", groupName)
		return
	}
	// Get the article
	article, err := s.DB.GetArticleByNum(groupDBs, articleNum)
	if err != nil {
		/* TODO: THIS DOES NOT SCALE WELL!
		// Article not found in articles table, let's check if it exists in overview
		overviews, _ := s.DB.GetOverviews(groupDBs)
		var foundInOverview bool
		for _, overview := range overviews {
			if overview.ArticleNum == articleNum {
				foundInOverview = true
				break
			}
		}

		if foundInOverview {
			c.String(http.StatusOK, "Article #%d exists in overview but full text has not been downloaded yet. Try importing full articles first.", articleNum)
		} else {
			c.String(http.StatusNotFound, "Article #%d not found in group %s. Available articles may have different numbers.", articleNum, groupName)
		}
		*/
		c.String(http.StatusNotFound, "Article #%d not found in group %s. Available articles may have different numbers.", articleNum, groupName)
		return
	}
	// Batch sanitize the article for better performance
	models.BatchSanitizeArticles([]*models.Article{article})

	// Get thread context - for now, don't show all articles (too many)
	// TODO: Implement proper threading based on References/In-Reply-To headers
	thread := []*models.Overview{}

	// Get subject for title without HTML escaping (for proper browser title display)
	subjectText := article.GetCleanSubject()

	data := ArticlePageData{
		TemplateData: s.getBaseTemplateData(c, subjectText+" - Article "+articleNumStr),
		GroupName:    groupName,
		GroupPtr:     groupDBs.NewsgroupPtr,
		ArticleNum:   articleNum,
		Article:      article,
		Thread:       thread,
		PrevArticle:  articleNum - 1,
		NextArticle:  articleNum + 1,
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/article.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

func (s *WebServer) articleByMessageIdPage(c *gin.Context) {
	groupName := c.Param("group")
	messageId := c.Param("messageId")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccess(c, groupName) {
		return // Error response already sent by checkGroupAccess
	}

	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		c.String(http.StatusNotFound, "Group not found: %v", err)
		return
	}
	defer groupDBs.Return(s.DB)
	// Get the article by message ID
	article, err := s.DB.GetArticleByMessageID(groupDBs, messageId)
	if err != nil {
		c.String(http.StatusNotFound, "Article with message ID %s not found in group %s", messageId, groupName)
		return
	}

	// Get thread context - for now, don't show all articles (too many)
	// TODO: Implement proper threading based on References/In-Reply-To headers
	thread := []*models.Overview{}

	// Batch sanitize the article for better performance
	if article != nil {
		models.BatchSanitizeArticles([]*models.Article{article})
	}

	// Get subject for title without HTML escaping (for proper browser title display)
	subjectText := article.GetCleanSubject()

	data := ArticlePageData{
		TemplateData: s.getBaseTemplateData(c, subjectText+" - Article "+strconv.FormatInt(article.ArticleNums[groupDBs.NewsgroupPtr], 10)),
		GroupName:    groupName,
		GroupPtr:     groupDBs.NewsgroupPtr,
		ArticleNum:   article.ArticleNums[groupDBs.NewsgroupPtr],
		Article:      article,
		Thread:       thread,
		PrevArticle:  article.ArticleNums[groupDBs.NewsgroupPtr] - 1,
		NextArticle:  article.ArticleNums[groupDBs.NewsgroupPtr] + 1,
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/article.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		c.String(http.StatusInternalServerError, "Template error: %v", err)
		return
	}
}
