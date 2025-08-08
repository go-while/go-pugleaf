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

// This file should contain the sections page related functions from server.go:
var LIMIT_sectionPage = 128
var LIMIT_sectionGroupPage = 128

// Functions to be moved from server.go:
//   - func (s *WebServer) sectionsPage(c *gin.Context) (line ~893)
//     Handles "/sections" route to display all available sections
//   - func (s *WebServer) sectionPage(c *gin.Context) (line ~932)
//     Handles "/:section" route to display groups in a specific section
//   - func (s *WebServer) sectionGroupPage(c *gin.Context) (line ~1027)
//     Handles "/:section/:group/" route to display articles in a section's group
//   - func (s *WebServer) sectionArticlePage(c *gin.Context) (line ~1123)
//     Handles "/:section/:group/articles/:articleNum" route for section articles
//   - func (s *WebServer) sectionArticleByMessageIdPage(c *gin.Context) (line ~1204)
//     Handles "/:section/:group/message/:messageId" route for section articles by message ID
//
// This file will handle all section-related functionality, including section listing,
// section group views, and section article views.

// Section-based handlers (legacy compatibility)

func (s *WebServer) sectionsPage(c *gin.Context) {
	// Get all sections
	sections, err := s.DB.GetSections()
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Database Error",
			"Could not retrieve sections: "+err.Error())
		return
	}

	data := struct {
		TemplateData
		Sections []*models.Section
	}{
		TemplateData: s.getBaseTemplateData(c, "All Sections"),
		Sections:     sections,
	}

	// Load template individually
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/sections.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		log.Printf("Error rendering sections template: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

// sectionPage handles /:section/ - shows groups in a section
func (s *WebServer) sectionPage(c *gin.Context) {
	sectionName := c.Param("section")

	// Check for reserved paths that should not be treated as sections
	reservedPaths := []string{"groups", "search", "stats", "help", "api", "static", "sections"}
	for _, reserved := range reservedPaths {
		if sectionName == reserved {
			// This should have been handled by a more specific route
			// If we got here, it means the route order is wrong
			c.Redirect(http.StatusMovedPermanently, "/"+reserved)
			return
		}
	}

	// Get section info
	section, err := s.DB.GetSectionByName(sectionName)
	if err != nil {
		// Section doesn't exist - return 404
		s.renderError(c, http.StatusNotFound, "Section not found",
			"The section '"+sectionName+"' does not exist.")
		return
	}

	// Get pagination parameters
	page := 1

	if p := c.Query("page"); p != "" && p != "1" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 1 {
			page = parsed
		}
	}

	// Get groups for this section (paginated)
	allGroups, err := s.DB.GetSectionGroups(section.ID)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Database Error", err.Error())
		return
	}

	// Calculate pagination
	totalCount := len(allGroups)
	start := (page - 1) * LIMIT_sectionPage
	end := start + LIMIT_sectionPage

	if start > totalCount {
		start = totalCount
	}
	if end > totalCount {
		end = totalCount
	}

	groups := allGroups[start:end]
	pagination := models.NewPaginationInfo(page, LIMIT_sectionPage, totalCount)

	// Get all sections for navigation
	sections, err := s.DB.GetHeaderSections()
	if err != nil {
		sections = []*models.Section{} // Fallback to empty if error
	}

	data := SectionPageData{
		TemplateData:      s.getBaseTemplateData(c, section.DisplayName+" Groups"),
		Section:           section,
		Groups:            groups,
		Pagination:        pagination,
		TotalGroups:       totalCount,
		AvailableSections: sections,
	}

	// Load template individually to include pagination support
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/section.html", "web/templates/pagination.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		log.Printf("Error rendering section template: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
	}
}

// sectionGroupPage handles /:section/:group/ - shows articles in a group within a section
func (s *WebServer) sectionGroupPage(c *gin.Context) {
	sectionName := c.Param("section")
	groupName := c.Param("group")

	// Get section info
	section, err := s.DB.GetSectionByName(sectionName)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Section not found",
			"The section '"+sectionName+"' does not exist.")
		return
	}

	// Verify group exists in this section
	sectionGroups, err := s.DB.GetSectionGroupsByName(groupName)
	if err != nil || len(sectionGroups) == 0 {
		s.renderError(c, http.StatusNotFound, "Group not found",
			"The group '"+groupName+"' does not exist or is not in section '"+sectionName+"'.")
		return
	}

	// Check if this group belongs to the specified section
	groupInSection := false
	for _, sg := range sectionGroups {
		if sg.SectionID == section.ID {
			groupInSection = true
			break
		}
	}

	if !groupInSection {
		s.renderError(c, http.StatusNotFound, "Group not in section",
			"The group '"+groupName+"' is not in section '"+sectionName+"'.")
		return
	}

	// Get group database
	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Group database not found",
			"The group '"+groupName+"' database does not exist. Try importing data first.")
		return
	}
	defer groupDBs.Return(s.DB)
	// Get pagination parameters
	page := 1
	var lastArticleNum int64

	if p := c.Query("page"); p != "" && p != "1" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 1 {
			page = parsed
		}
	}

	// Support cursor-based pagination
	if cursor := c.Query("cursor"); cursor != "" {
		if parsed, err := strconv.ParseInt(cursor, 10, 64); err == nil && parsed > 0 {
			lastArticleNum = parsed
			page = 0 // Indicate cursor-based pagination
		}
	}

	// Handle page-based to cursor conversion for compatibility
	if page > 1 && lastArticleNum == 0 {
		skipCount := (page - 1) * LIMIT_sectionGroupPage
		var cursorArticleNum int64
		err = groupDBs.DB.QueryRow(`
			SELECT article_num FROM articles
			WHERE hide = 0
			ORDER BY article_num DESC
			LIMIT 1 OFFSET ?`, skipCount-1).Scan(&cursorArticleNum)
		if err != nil {
			lastArticleNum = 0
		} else {
			lastArticleNum = cursorArticleNum
		}
	}

	// Get articles (overview data) for this group with pagination
	articles, totalCount, hasMore, err := s.DB.GetOverviewsPaginated(groupDBs, lastArticleNum, LIMIT_sectionGroupPage)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Database Error", err.Error())
		return
	}

	var pagination *models.PaginationInfo
	if page > 0 {
		// Page-based pagination
		pagination = models.NewPaginationInfo(page, LIMIT_sectionGroupPage, totalCount)
	} else {
		// Cursor-based pagination
		pagination = &models.PaginationInfo{
			CurrentPage: 1,
			PageSize:    LIMIT_sectionGroupPage,
			TotalCount:  totalCount,
			TotalPages:  (totalCount + LIMIT_sectionGroupPage - 1) / LIMIT_sectionGroupPage,
			HasNext:     hasMore,
			HasPrev:     lastArticleNum > 0,
		}
	}

	data := SectionGroupPageData{
		TemplateData: s.getBaseTemplateData(c, section.DisplayName+" - "+groupName),
		Section:      section,
		GroupName:    groupName,
		Articles:     articles,
		Pagination:   pagination,
		GroupExists:  true,
	}

	// Load template individually to include pagination support
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/section-group.html", "web/templates/pagination.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		log.Printf("Error rendering section-group template: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
	}
}

// sectionArticlePage handles /:section/:group/articles/:articleNum - shows a specific article
func (s *WebServer) sectionArticlePage(c *gin.Context) {
	sectionName := c.Param("section")
	groupName := c.Param("group")
	articleNumStr := c.Param("articleNum")

	// Parse article number
	articleNum, err := strconv.ParseInt(articleNumStr, 10, 64)
	if err != nil {
		s.renderError(c, http.StatusBadRequest, "Invalid Article Number",
			"The article number must be a valid integer.")
		return
	}

	// Get section info
	section, err := s.DB.GetSectionByName(sectionName)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Section not found",
			"The section '"+sectionName+"' does not exist.")
		return
	}

	// Verify group exists in this section (same as sectionGroupPage)
	sectionGroups, err := s.DB.GetSectionGroupsByName(groupName)
	if err != nil || len(sectionGroups) == 0 {
		s.renderError(c, http.StatusNotFound, "Group not found",
			"The group '"+groupName+"' does not exist or is not in section '"+sectionName+"'.")
		return
	}

	groupInSection := false
	for _, sg := range sectionGroups {
		if sg.SectionID == section.ID {
			groupInSection = true
			break
		}
	}

	if !groupInSection {
		s.renderError(c, http.StatusNotFound, "Group not in section",
			"The group '"+groupName+"' is not in section '"+sectionName+"'.")
		return
	}

	// Get group database
	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Group database not found",
			"The group '"+groupName+"' database does not exist.")
		return
	}
	defer groupDBs.Return(s.DB)
	// Get the article
	article, err := s.DB.GetArticleByNum(groupDBs, articleNum)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Article not found",
			"Article "+articleNumStr+" not found in group '"+groupName+"'. It may not have been imported yet.")
		return
	}

	// Get subject for title without HTML escaping (for proper browser title display)
	subjectText := article.GetCleanSubject()

	data := SectionArticlePageData{
		TemplateData: s.getBaseTemplateData(c, section.DisplayName+" - "+groupName+" - "+subjectText),
		Section:      section,
		GroupName:    groupName,
		ArticleNum:   articleNum,
		Article:      article,
		Thread:       []*models.Overview{}, // TODO: Implement threading
		PrevArticle:  0,                    // TODO: Implement navigation
		NextArticle:  0,                    // TODO: Implement navigation
	}

	s.renderTemplate(c, "section-article.html", data)
}

func (s *WebServer) sectionArticleByMessageIdPage(c *gin.Context) {
	sectionName := c.Param("section")
	groupName := c.Param("group")
	messageId := c.Param("messageId")

	// Get section info
	section, err := s.DB.GetSectionByName(sectionName)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Section not found",
			"The section '"+sectionName+"' does not exist.")
		return
	}

	// Verify group exists in this section (same as sectionGroupPage)
	sectionGroups, err := s.DB.GetSectionGroupsByName(groupName)
	if err != nil || len(sectionGroups) == 0 {
		s.renderError(c, http.StatusNotFound, "Group not found",
			"The group '"+groupName+"' does not exist or is not in section '"+sectionName+"'.")
		return
	}

	groupInSection := false
	for _, sg := range sectionGroups {
		if sg.SectionID == section.ID {
			groupInSection = true
			break
		}
	}

	if !groupInSection {
		s.renderError(c, http.StatusNotFound, "Group not in section",
			"The group '"+groupName+"' is not in section '"+sectionName+"'.")
		return
	}

	// Get group database
	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Group database not found",
			"The group '"+groupName+"' database does not exist.")
		return
	}
	defer groupDBs.Return(s.DB)
	// Get the article by message ID
	article, err := s.DB.GetArticleByMessageID(groupDBs, messageId)
	if err != nil {
		s.renderError(c, http.StatusNotFound, "Article not found",
			"Article with message ID '"+messageId+"' not found in group '"+groupName+"'. It may not have been imported yet.")
		return
	}

	// Get subject for title without HTML escaping (for proper browser title display)
	subjectText := article.GetCleanSubject()

	data := SectionArticlePageData{
		TemplateData: s.getBaseTemplateData(c, section.DisplayName+" - "+groupName+" - "+subjectText),
		Section:      section,
		GroupName:    groupName,
		ArticleNum:   article.ArticleNum,
		Article:      article,
		Thread:       []*models.Overview{}, // TODO: Implement threading
		PrevArticle:  0,                    // TODO: Implement navigation
		NextArticle:  0,                    // TODO: Implement navigation
	}

	s.renderTemplate(c, "section-article.html", data)
}
