package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// buildNewsgroupAdminRedirectURL builds a redirect URL preserving search and pagination state
func buildNewsgroupAdminRedirectURL(c *gin.Context) string {
	redirectURL := "/admin?tab=newsgroups"
	if search := c.PostForm("search"); search != "" {
		redirectURL += "&search=" + search
	}
	if page := c.PostForm("ng_page"); page != "" {
		redirectURL += "&ng_page=" + page
	}
	return redirectURL
}

// adminCreateNewsgroup handles newsgroup creation
func (s *WebServer) adminCreateNewsgroup(c *gin.Context) {
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

	// Get form data
	name := strings.TrimSpace(c.PostForm("name"))
	description := strings.TrimSpace(c.PostForm("description"))
	expiryDaysStr := strings.TrimSpace(c.PostForm("expiry_days"))
	maxArticlesStr := strings.TrimSpace(c.PostForm("max_articles"))
	activeStr := c.PostForm("active")

	// Validate input
	if name == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Parse expiry days
	expiryDays := 0
	if expiryDaysStr != "" {
		expiryDays, err = strconv.Atoi(expiryDaysStr)
		if err != nil || expiryDays < 0 {
			session.SetError("Invalid expiry days")
			c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
			return
		}
	}

	// Parse max articles
	maxArticles := 0
	if maxArticlesStr != "" {
		maxArticles, err = strconv.Atoi(maxArticlesStr)
		if err != nil || maxArticles < 0 {
			session.SetError("Invalid max articles")
			c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
			return
		}
	}

	// Parse active status
	active := activeStr == "on" || activeStr == "true"

	// Check if newsgroup already exists
	_, err = s.DB.MainDBGetNewsgroup(name)
	if err == nil {
		session.SetError("Newsgroup already exists")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Create newsgroup
	newsgroup := &models.Newsgroup{
		Name:         name,
		Description:  description,
		Active:       active,
		ExpiryDays:   expiryDays,
		MaxArticles:  maxArticles,
		LastArticle:  0,
		MessageCount: 0,
		CreatedAt:    time.Now(),
		// Note: UpdatedAt will be set only when articles are processed via batch
	}

	err = s.DB.InsertNewsgroup(newsgroup)
	if err != nil {
		session.SetError("Failed to create newsgroup")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	session.SetSuccess("Newsgroup created successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
}

// adminUpdateNewsgroup handles newsgroup updates
func (s *WebServer) adminUpdateNewsgroup(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Get form data
	name := strings.TrimSpace(c.PostForm("name"))
	description := strings.TrimSpace(c.PostForm("description"))
	expiryDaysStr := strings.TrimSpace(c.PostForm("expiry_days"))
	maxArticlesStr := strings.TrimSpace(c.PostForm("max_articles"))
	activeStr := c.PostForm("active")

	// Validate input
	if name == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Parse expiry days
	expiryDays := 0
	if expiryDaysStr != "" {
		expiryDays, err = strconv.Atoi(expiryDaysStr)
		if err != nil || expiryDays < 0 {
			session.SetError("Invalid expiry days")
			c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
			return
		}
	}

	// Parse max articles
	maxArticles := 0
	if maxArticlesStr != "" {
		maxArticles, err = strconv.Atoi(maxArticlesStr)
		if err != nil || maxArticles < 0 {
			session.SetError("Invalid max articles")
			c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
			return
		}
	}

	// Parse active status
	active := activeStr == "on" || activeStr == "true"

	// Update newsgroup fields
	err = s.DB.UpdateNewsgroupDescription(name, description)
	if err != nil {
		session.SetError("Failed to update newsgroup description")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	err = s.DB.UpdateNewsgroupExpiry(name, expiryDays)
	if err != nil {
		session.SetError("Failed to update newsgroup expiry")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	err = s.DB.UpdateNewsgroupMaxArticles(name, maxArticles)
	if err != nil {
		session.SetError("Failed to update newsgroup max articles")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	err = s.DB.UpdateNewsgroupActive(name, active)
	if err != nil {
		session.SetError("Failed to update newsgroup status")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	session.SetSuccess("Newsgroup updated successfully")
	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}

// adminDeleteNewsgroup handles newsgroup deletion
func (s *WebServer) adminDeleteNewsgroup(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Get newsgroup name
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		session.SetError("Invalid newsgroup name")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Delete newsgroup
	err = s.DB.DeleteNewsgroup(name)
	if err != nil {
		session.SetError("Failed to delete newsgroup")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	session.SetSuccess("Newsgroup deleted successfully")
	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}

// adminAssignNewsgroupSection handles section assignment for newsgroups
func (s *WebServer) adminAssignNewsgroupSection(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	newsgroupName := strings.TrimSpace(c.PostForm("newsgroup_name"))
	sectionIDStr := strings.TrimSpace(c.PostForm("section_id"))

	// Validate inputs
	if newsgroupName == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Parse section ID (0 means unassign)
	sectionID, err := strconv.Atoi(sectionIDStr)
	if err != nil {
		session.SetError("Invalid section ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// First, remove any existing assignment for this newsgroup
	// Find existing section group assignment
	sectionGroups, err := s.DB.GetAllSectionGroups()
	if err != nil {
		session.SetError("Failed to load section assignments")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Remove existing assignment if any
	for _, sg := range sectionGroups {
		if sg.NewsgroupName == newsgroupName {
			err = s.DB.DeleteSectionGroup(sg.ID)
			if err != nil {
				session.SetError("Failed to remove existing section assignment")
				c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
				return
			}
			break
		}
	}

	// If sectionID is 0, we're unassigning (already done above)
	if sectionID == 0 {
		session.SetSuccess("Newsgroup unassigned from section")
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Get newsgroup description for the assignment
	newsgroup, err := s.DB.GetNewsgroupByName(newsgroupName)
	var groupDescription string
	if err == nil && newsgroup != nil {
		groupDescription = newsgroup.Description
	}

	// Create new assignment
	sectionGroup := &models.SectionGroup{
		SectionID:        sectionID,
		NewsgroupName:    newsgroupName,
		GroupDescription: groupDescription,
		SortOrder:        0,
		IsCategoryHeader: false,
		CreatedAt:        time.Now(),
	}

	err = s.DB.CreateSectionGroup(sectionGroup)
	if err != nil {
		session.SetError("Failed to assign newsgroup to section")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	session.SetSuccess("Newsgroup assigned to section successfully")
	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}

// adminToggleNewsgroup handles toggling the active status of a newsgroup
func (s *WebServer) adminToggleNewsgroup(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Get newsgroup name from form
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Get current newsgroup to check its current status
	newsgroup, err := s.DB.MainDBGetNewsgroup(name)
	if err != nil {
		session.SetError("Newsgroup not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Toggle the active status
	newActiveStatus := !newsgroup.Active

	// Update the newsgroup active status
	err = s.DB.UpdateNewsgroupActive(name, newActiveStatus)
	if err != nil {
		session.SetError("Failed to toggle newsgroup status")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Set success message based on new status
	statusText := "activated"
	if !newActiveStatus {
		statusText = "deactivated"
	}
	session.SetSuccess("Newsgroup '" + name + "' has been " + statusText)
	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}

// adminBulkEnableNewsgroups handles bulk enabling of newsgroups
func (s *WebServer) adminBulkEnableNewsgroups(c *gin.Context) {
	s.handleBulkNewsgroupAction(c, true, "enable")
}

// adminBulkDisableNewsgroups handles bulk disabling of newsgroups
func (s *WebServer) adminBulkDisableNewsgroups(c *gin.Context) {
	s.handleBulkNewsgroupAction(c, false, "disable")
}

// adminBulkDeleteNewsgroups handles bulk deletion of inactive newsgroups
func (s *WebServer) adminBulkDeleteNewsgroups(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Get newsgroup names from form array
	newsgroups := c.PostFormArray("newsgroups")
	if len(newsgroups) == 0 {
		session.SetError("No newsgroups selected for deletion")
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Bulk delete newsgroups (only inactive ones will be deleted)
	rowsAffected, err := s.DB.BulkDeleteNewsgroups(newsgroups)
	if err != nil {
		session.SetError("Failed to delete newsgroups: " + err.Error())
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Set success message
	if rowsAffected == 0 {
		session.SetError("No newsgroups were deleted. Only inactive newsgroups can be deleted.")
	} else {
		session.SetSuccess(fmt.Sprintf("Successfully deleted %d newsgroups", rowsAffected))
	}
	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}

// handleBulkNewsgroupAction is a helper function for bulk enable/disable operations
func (s *WebServer) handleBulkNewsgroupAction(c *gin.Context, activeStatus bool, actionName string) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Get newsgroup names from form array
	newsgroups := c.PostFormArray("newsgroups")
	if len(newsgroups) == 0 {
		session.SetError("No newsgroups selected for " + actionName)
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Bulk update newsgroups
	rowsAffected, err := s.DB.BulkUpdateNewsgroupActive(newsgroups, activeStatus)
	if err != nil {
		session.SetError("Failed to " + actionName + " newsgroups: " + err.Error())
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Set success message
	actionPastTense := "enabled"
	if !activeStatus {
		actionPastTense = "disabled"
	}
	session.SetSuccess(fmt.Sprintf("Successfully %s %d newsgroups", actionPastTense, rowsAffected))
	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}

// adminMigrateNewsgroupActivity handles migrating activity timestamp for a specific newsgroup
func (s *WebServer) adminMigrateNewsgroupActivity(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Get newsgroup name from form
	name := strings.TrimSpace(c.PostForm("newsgroup_name"))
	if name == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Check if newsgroup exists
	newsgroup, err := s.DB.MainDBGetNewsgroup(name)
	if err != nil {
		session.SetError("Newsgroup not found: " + name)
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Get the group database for this newsgroup
	groupDBs, err := s.DB.GetGroupDBs(name)
	if err != nil {
		session.SetError("Failed to access newsgroup database: " + err.Error())
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Query the latest article date from visible articles only
	var latestDate sql.NullString
	err = groupDBs.DB.QueryRow("SELECT MAX(date_sent) FROM articles WHERE hide = 0").Scan(&latestDate)
	groupDBs.Return(s.DB) // Always return the database connection

	if err != nil {
		session.SetError("Failed to query latest article for " + name + ": " + err.Error())
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Update newsgroup timestamp if we found a latest date
	if latestDate.Valid {
		_, err = s.DB.GetMainDB().Exec("UPDATE newsgroups SET updated_at = ? WHERE id = ?", latestDate.String, newsgroup.ID)
		if err != nil {
			session.SetError("Failed to update newsgroup activity timestamp: " + err.Error())
			c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
			return
		}
		session.SetSuccess("Successfully updated activity timestamp for newsgroup: " + name)
	} else {
		session.SetError("No visible articles found in newsgroup: " + name)
	}

	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}
