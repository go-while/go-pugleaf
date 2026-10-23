// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-while/go-pugleaf/internal/models"
)

// This file should contain the article page related functions from server.go:
//
// Functions to be moved from server.go:
//   - func (s *WebServer) articlePage(c *Context) (line ~671)
//     Handles "/groups/:group/articles/:articleNum" route to display a specific article
//   - func (s *WebServer) articleByMessageIdPage(c *Context) (line ~739)
//     Handles "/groups/:group/message/:messageId" route to display article by message ID
//
// This file will handle the individual article view functionality, displaying specific articles
// either by article number or message ID.
func (s *WebServer) articlePage(c *Context) {
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

	groupDB, err := s.DB.GetGroupDB(groupName)
	if groupDB == nil || err != nil {
		c.String(http.StatusNotFound, "Group not found: %v", err)
		return
	}
	defer groupDB.Return()
	if groupDB.NewsgroupPtr == nil {
		c.String(http.StatusInternalServerError, "Group pointer is nil for group %s", groupName)
		return
	}
	// Get the article
	article, err := s.DB.GetArticleByNum(groupDB, articleNum)
	if err != nil {
		/* TODO: THIS DOES NOT SCALE WELL!
		// Article not found in articles table, let's check if it exists in overview
		overviews, _ := s.DB.GetOverviews(groupDB)
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
	// Flags straight from the group DB (never from the cached article): hidden = not found for non-admins.
	spam, hide, err := s.DB.GetArticleFlags(groupDB, articleNum)
	if err != nil || s.articleHiddenFor(c, hide) {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[WEB]: articlePage: GetArticleFlags(%s/%d): %v", groupName, articleNum, err)
		}
		c.String(http.StatusNotFound, "Article #%d not found in group %s. Available articles may have different numbers.", articleNum, groupName)
		return
	}
	// Batch sanitize the article for better performance
	models.BatchSanitizeArticles([]*models.Article{article}, groupName)

	// Get thread context - for now, don't show all articles (too many)
	// TODO: Implement proper threading based on References/In-Reply-To headers
	thread := []*models.Overview{}

	// Get subject for title without HTML escaping (for proper browser title display)
	subjectText := article.GetCleanSubject()

	data := ArticlePageData{
		TemplateData: s.getBaseTemplateData(c, subjectText+" - Article "+articleNumStr),
		GroupName:    groupName,
		GroupPtr:     groupDB.NewsgroupPtr,
		ArticleNum:   articleNum,
		Article:      article,
		Thread:       thread,
		PrevArticle:  articleNum - 1,
		NextArticle:  articleNum + 1,
		ArticleSpam:  spam,
		ArticleHide:  hide,
	}

	s.renderPage(c, http.StatusOK, data, "article.html")
}

func (s *WebServer) articleByMessageIdPage(c *Context) {
	groupName := c.Param("group")
	messageId := c.Param("messageId")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccess(c, groupName) {
		return // Error response already sent by checkGroupAccess
	}

	groupDB, err := s.DB.GetGroupDB(groupName)
	if err != nil {
		c.String(http.StatusNotFound, "Group not found: %v", err)
		return
	}
	defer groupDB.Return()
	// Get the article by message ID
	article, err := s.DB.GetArticleByMessageID(groupDB, messageId)
	if err != nil {
		c.String(http.StatusNotFound, "Article with message ID %s not found in group %s", messageId, groupName)
		return
	}
	// Flags straight from the group DB (never from the cached article): hidden = not found for non-admins.
	_, spam, hide, err := s.DB.GetArticleFlagsByMessageID(groupDB, messageId)
	if err != nil || s.articleHiddenFor(c, hide) {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[WEB]: articleByMessageIdPage: GetArticleFlagsByMessageID(%s): %v", groupName, err)
		}
		c.String(http.StatusNotFound, "Article with message ID %s not found in group %s", messageId, groupName)
		return
	}

	// Get thread context - for now, don't show all articles (too many)
	// TODO: Implement proper threading based on References/In-Reply-To headers
	thread := []*models.Overview{}

	// Batch sanitize the article for better performance
	if article != nil {
		models.BatchSanitizeArticles([]*models.Article{article}, groupName)
	}

	// Get subject for title without HTML escaping (for proper browser title display)
	subjectText := article.GetCleanSubject()

	data := ArticlePageData{
		TemplateData: s.getBaseTemplateData(c, subjectText+" - Article "+strconv.FormatInt(article.ArticleNums[groupDB.NewsgroupPtr], 10)),
		GroupName:    groupName,
		GroupPtr:     groupDB.NewsgroupPtr,
		ArticleNum:   article.ArticleNums[groupDB.NewsgroupPtr],
		Article:      article,
		Thread:       thread,
		PrevArticle:  article.ArticleNums[groupDB.NewsgroupPtr] - 1,
		NextArticle:  article.ArticleNums[groupDB.NewsgroupPtr] + 1,
		ArticleSpam:  spam,
		ArticleHide:  hide,
	}

	s.renderPage(c, http.StatusOK, data, "article.html")
}

// articleHiddenFor reports whether an article with this hide counter is invisible to the
// requester: hidden articles (hide > 0) exist only for admins (D2). Callers read hide with
// GetArticleFlags/GetArticleFlagsByMessageID, never from a cached article.
func (s *WebServer) articleHiddenFor(c *Context, hide int) bool {
	return hide > 0 && !s.isAdminRequest(c)
}
