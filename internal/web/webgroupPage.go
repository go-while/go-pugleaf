// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

var LIMIT_groupPage = 128

// This file should contain the individual group page related functions from server.go:
//
// Functions to be moved from server.go:
//   - func (s *WebServer) groupPage(c *gin.Context) (line ~598)
//     Handles "/groups/:group" route to display articles in a specific newsgroup
//
// This file will handle the individual group view functionality, showing articles in a specific newsgroup.
func (s *WebServer) groupPage(c *gin.Context) {
	groupName := c.Param("group")

	// Check if user can access this group (active status + admin bypass)
	if !s.checkGroupAccess(c, groupName) {
		return // Error response already sent by checkGroupAccess
	}

	// Get pagination parameters
	page := 1
	var lastArticleNum int64

	if p := c.Query("page"); p != "" && p != "1" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 1 {
			page = parsed
		}
	}
	// Check for cursor parameter (more efficient than page-based)
	if cursor := c.Query("cursor"); cursor != "" {
		if parsed, err := strconv.ParseInt(cursor, 10, 64); err == nil && parsed > 0 {
			lastArticleNum = parsed
			page = 0 // Indicate cursor-based pagination
		}
	}

	// Try to get group overview with pagination
	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		// Handle error case - groupDBs is nil, so don't try to return it
		data := GroupPageData{
			TemplateData: s.getBaseTemplateData(c, groupName),
			GroupName:    groupName,
			Articles:     nil,
			Pagination:   nil,
		}
		// Load template individually to avoid conflicts
		tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/group.html", "web/templates/pagination.html"))
		c.Header("Content-Type", "text/html")
		err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
		if err != nil {
			s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		}
		return
	}
	defer groupDBs.Return(s.DB) // Only defer if groupDBs is not nil

	var articles []*models.Overview
	var totalCount int
	var pagination *models.PaginationInfo
	var hasMore bool

	// Use cursor-based pagination for better performance
	if page > 1 && lastArticleNum == 0 {
		// For page-based requests beyond page 1, we need to calculate the cursor
		// This is less efficient but maintains URL compatibility
		// For optimal performance, clients should use cursor parameter
		skipCount := (page - 1) * LIMIT_groupPage
		if skipCount > 1000 { // Warn for very large offsets
			log.Printf("Warning: Large page offset (%d) requested for group %s, consider using cursor parameter", skipCount, groupName)
		}

		// Get the article_num at the skip position by querying with OFFSET once
		var cursorArticleNum int64
		err = groupDBs.DB.QueryRow(`
			SELECT article_num FROM articles
			WHERE hide = 0
			ORDER BY article_num DESC
			LIMIT 1 OFFSET ?`, skipCount-1).Scan(&cursorArticleNum)
		if err != nil {
			lastArticleNum = 0 // Fall back to first page
		} else {
			lastArticleNum = cursorArticleNum
		}
	}

	articles, totalCount, hasMore, err = s.DB.GetOverviewsPaginated(groupDBs, lastArticleNum, LIMIT_groupPage)
	if err == nil {
		if page > 0 {
			// Page-based pagination info
			pagination = models.NewPaginationInfo(page, LIMIT_groupPage, totalCount)
		} else {
			// Cursor-based pagination - create a simple pagination info
			pagination = &models.PaginationInfo{
				CurrentPage: 1,
				PageSize:    LIMIT_groupPage,
				TotalCount:  totalCount,
				TotalPages:  (totalCount + LIMIT_groupPage - 1) / LIMIT_groupPage,
				HasNext:     hasMore,
				HasPrev:     lastArticleNum > 0,
			}
		}
	}
	data := GroupPageData{
		TemplateData: s.getBaseTemplateData(c, groupName),
		GroupName:    groupName,
		Articles:     articles,
		Pagination:   pagination,
	}

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/group.html", "web/templates/pagination.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

func (s *WebServer) incrementSpam(c *gin.Context) {
	// Check authentication - any logged-in user can flag spam
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	user, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil {
		session.SetError("User not found")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	group := c.Param("group")
	articleNumStr := c.Param("articleNum")

	articleNum, err := strconv.ParseInt(articleNumStr, 10, 64)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/groups/"+group)
		return
	}

	// Check if user has already flagged this article
	alreadyFlagged, err := s.DB.HasUserFlaggedSpam(user.ID, group, articleNum)
	if err != nil {
		log.Printf("Error checking user spam flag: %v", err)
		// Continue anyway - don't block the user
	} else if alreadyFlagged {
		log.Printf("DEBUG: User %d already flagged article %d in group %s", user.ID, articleNum, group)
		// Redirect without incrementing
		referer := c.GetHeader("Referer")
		if strings.Contains(referer, "/admin") {
			c.Redirect(http.StatusSeeOther, "/admin?tab=spam")
		} else if strings.Contains(referer, "/threads") {
			c.Redirect(http.StatusSeeOther, "/groups/"+group+"/threads")
		} else {
			c.Redirect(http.StatusSeeOther, "/groups/"+group)
		}
		return
	}

	// Update spam counter in database
	log.Printf("DEBUG: Incrementing spam for group=%s, articleNum=%d", group, articleNum)
	err = s.DB.IncrementArticleSpam(group, articleNum)
	if err != nil {
		log.Printf("Error incrementing spam count: %v", err)
	} else {
		log.Printf("DEBUG: Successfully incremented spam count for article %d", articleNum)

		// Record that this user has flagged this article
		err = s.DB.RecordUserSpamFlag(user.ID, group, articleNum)
		if err != nil {
			log.Printf("Error recording user spam flag: %v", err)
			// Don't fail the operation if we can't record the flag
		}
	}

	// Check referer to determine redirect location and preserve pagination
	referer := c.GetHeader("Referer")
	redirectURL := "/groups/" + group // Default fallback

	if referer != "" {
		// Parse referer to preserve query parameters (like page number)
		if parsedURL, err := url.Parse(referer); err == nil {
			if strings.Contains(referer, "/admin") {
				redirectURL = "/admin?tab=spam"
			} else if strings.Contains(referer, "/threads") {
				// Preserve pagination for threads view
				redirectURL = "/groups/" + group + "/threads"
				if parsedURL.RawQuery != "" {
					redirectURL += "?" + parsedURL.RawQuery
				}
				// Add anchor for the specific thread (threads use thread- prefix)
				redirectURL += "#thread-" + strconv.FormatInt(articleNum, 10)
			} else {
				// Preserve pagination for group view
				redirectURL = "/groups/" + group
				if parsedURL.RawQuery != "" {
					redirectURL += "?" + parsedURL.RawQuery
				}
				// Add anchor for the specific article (group view uses article- prefix)
				redirectURL += "#article-" + strconv.FormatInt(articleNum, 10)
			}
		} else {
			log.Printf("Error parsing referer URL: %v", err)
		}
	}

	c.Redirect(http.StatusSeeOther, redirectURL)
}

func (s *WebServer) incrementHide(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	group := c.Param("group")
	articleNumStr := c.Param("articleNum")

	articleNum, err := strconv.ParseInt(articleNumStr, 10, 64)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/groups/"+group)
		return
	}

	// Update hide counter in database
	err = s.DB.IncrementArticleHide(group, articleNum)
	if err != nil {
		log.Printf("Error incrementing hide count: %v", err)
	}

	// Check referer to determine redirect location and preserve pagination
	referer := c.GetHeader("Referer")
	redirectURL := "/groups/" + group // Default fallback

	if referer != "" {
		// Parse referer to preserve query parameters (like page number)
		if parsedURL, err := url.Parse(referer); err == nil {
			if strings.Contains(referer, "/admin") {
				redirectURL = "/admin?tab=spam"
			} else if strings.Contains(referer, "/threads") {
				// Preserve pagination for threads view
				redirectURL = "/groups/" + group + "/threads"
				if parsedURL.RawQuery != "" {
					redirectURL += "?" + parsedURL.RawQuery
				}
				// Add anchor for the specific thread (threads use thread- prefix)
				redirectURL += "#thread-" + strconv.FormatInt(articleNum, 10)
			} else {
				// Preserve pagination for group view
				redirectURL = "/groups/" + group
				if parsedURL.RawQuery != "" {
					redirectURL += "?" + parsedURL.RawQuery
				}
				// Add anchor for the specific article (group view uses article- prefix)
				redirectURL += "#article-" + strconv.FormatInt(articleNum, 10)
			}
		} else {
			log.Printf("Error parsing referer URL: %v", err)
		}
	}

	c.Redirect(http.StatusSeeOther, redirectURL)
}
