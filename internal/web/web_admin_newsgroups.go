package web

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// buildNewsgroupAdminRedirectURL builds a redirect URL preserving search and pagination state
func buildNewsgroupAdminRedirectURL(c *gin.Context) string {
	redirectURL := "/admin?tab=newsgroups"
	if search := c.PostForm("search"); search != "" {
		redirectURL += "&search=" + search
	}
	if searchDesc := c.PostForm("search_description"); searchDesc == "on" {
		redirectURL += "&search_description=on"
	}
	if page := c.PostForm("ng_page"); page != "" {
		redirectURL += "&ng_page=" + page
	}
	return redirectURL
}

// adminCreateNewsgroup handles newsgroup creation
func (s *WebServer) adminCreateNewsgroup(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get form data
	name := strings.TrimSpace(c.PostForm("name"))
	description := strings.TrimSpace(c.PostForm("description"))
	expiryDaysStr := strings.TrimSpace(c.PostForm("expiry_days"))
	maxArticlesStr := strings.TrimSpace(c.PostForm("max_articles"))
	maxArtSizeStr := strings.TrimSpace(c.PostForm("max_art_size"))
	activeStr := c.PostForm("active")

	// Validate input
	if name == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Parse expiry days
	var err error
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

	// Parse max art size
	maxArtSize := 0
	if maxArtSizeStr != "" {
		maxArtSize, err = strconv.Atoi(maxArtSizeStr)
		if err != nil || maxArtSize < 0 {
			session.SetError("Invalid max article size")
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
		MaxArtSize:   maxArtSize,
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
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get form data
	var err error
	name := strings.TrimSpace(c.PostForm("name"))
	description := strings.TrimSpace(c.PostForm("description"))
	expiryDaysStr := strings.TrimSpace(c.PostForm("expiry_days"))
	maxArticlesStr := strings.TrimSpace(c.PostForm("max_articles"))
	maxArtSizeStr := strings.TrimSpace(c.PostForm("max_art_size"))
	activeStr := c.PostForm("active")
	status := strings.TrimSpace(c.PostForm("status"))

	// Validate input
	if name == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Validate status field
	if status != "" {
		validStatuses := []string{"y", "m", "n", "j", "x"}
		isValid := false
		for _, validStatus := range validStatuses {
			if status == validStatus {
				isValid = true
				break
			}
		}
		// Check if it's a redirect status (starts with =)
		if !isValid && !strings.HasPrefix(status, "=") {
			session.SetError("Invalid status value. Must be y, m, n, j, x, or =group.name")
			c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
			return
		}
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

	// Parse max art size
	maxArtSize := 0
	if maxArtSizeStr != "" {
		maxArtSize, err = strconv.Atoi(maxArtSizeStr)
		if err != nil || maxArtSize < 0 {
			session.SetError("Invalid max article size")
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

	err = s.DB.UpdateNewsgroupMaxArtSize(name, maxArtSize)
	if err != nil {
		session.SetError("Failed to update newsgroup max article size")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	err = s.DB.UpdateNewsgroupActive(name, active)
	if err != nil {
		session.SetError("Failed to update newsgroup status")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Update status if provided
	if status != "" {
		err = s.DB.UpdateNewsgroupStatus(name, status)
		if err != nil {
			session.SetError("Failed to update newsgroup NNTP status")
			c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
			return
		}
	}

	session.SetSuccess("Newsgroup updated successfully")
	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}

// adminDeleteNewsgroup handles newsgroup deletion
func (s *WebServer) adminDeleteNewsgroup(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get newsgroup name
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		session.SetError("Invalid newsgroup name")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Delete newsgroup
	err := s.DB.DeleteNewsgroup(name)
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
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

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
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

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
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

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
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

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
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

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
	err = database.RetryableQueryRowScan(groupDBs.DB, "SELECT MAX(date_sent) FROM articles WHERE hide = 0", nil, &latestDate)
	groupDBs.Return(s.DB) // Always return the database connection

	if err != nil {
		session.SetError("Failed to query latest article for " + name + ": " + err.Error())
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Update newsgroup timestamp if we found a latest date
	if latestDate.Valid {
		_, err = database.RetryableExec(s.DB.GetMainDB(), "UPDATE newsgroups SET updated_at = ? WHERE id = ?", latestDate.String, newsgroup.ID)
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

// adminFixThreadActivity handles fixing thread activity timestamps for a specific newsgroup
func (s *WebServer) adminFixThreadActivity(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get newsgroup name from form
	name := strings.TrimSpace(c.PostForm("newsgroup_name"))
	if name == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Check if newsgroup exists
	_, err := s.DB.MainDBGetNewsgroup(name)
	if err != nil {
		session.SetError("Newsgroup not found: " + name)
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Call the fix thread activity function (same logic as cmd/fix-thread-activity)
	if err := s.fixGroupThreadActivity(name); err != nil {
		session.SetError("Failed to fix thread activity for " + name + ": " + err.Error())
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	session.SetSuccess("Successfully fixed thread activity timestamps for newsgroup: " + name)
	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}

const query_fixGroupThreadActivity1 = "SELECT thread_root, child_articles, last_activity FROM thread_cache"
const query_fixGroupThreadActivity2 = "SELECT date_sent FROM articles WHERE article_num = ? AND hide = 0"
const query_fixGroupThreadActivity3 = "UPDATE thread_cache SET last_activity = ? WHERE thread_root = ?"

// fixGroupThreadActivity implements the same logic as cmd/fix-thread-activity for a single group
func (s *WebServer) fixGroupThreadActivity(groupName string) error {
	groupDBs, err := s.DB.GetGroupDBs(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group DB: %w", err)
	}
	defer groupDBs.Return(s.DB)

	rows, err := database.RetryableQuery(groupDBs.DB, query_fixGroupThreadActivity1)
	if err != nil {
		return fmt.Errorf("failed to query thread cache: %w", err)
	}
	defer rows.Close()

	type threadInfo struct {
		root          int64
		childArticles string
		lastActivity  time.Time
	}
	updatedCount := 0
	for rows.Next() {
		var thread threadInfo
		var lastActivityStr sql.NullString
		if err := rows.Scan(&thread.root, &thread.childArticles, &lastActivityStr); err != nil {
			return fmt.Errorf("failed to scan thread in '%s': %w", groupName, err)
		}
		if lastActivityStr.Valid {
			// Try SQLite format first, then RFC3339 format (same as recover-db/main.go)
			if parsed, err := time.Parse(time.RFC3339, lastActivityStr.String); err == nil {
				thread.lastActivity = parsed
			} else if parsed, err := time.Parse("2006-01-02 15:04:05", lastActivityStr.String); err == nil {
				thread.lastActivity = parsed
			}
		} else {
			log.Printf("Thread %d in '%s': last_activity is NULL", thread.root, groupName)
		}
		// Build list of all articles in this thread
		articleNums := []int64{thread.root}
		if thread.childArticles != "" {
			parts := strings.Split(thread.childArticles, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" {
					if num, err := strconv.ParseInt(part, 10, 64); err == nil {
						articleNums = append(articleNums, num)
					}
				}
			}
		}

		// Find the actual maximum date_sent among all articles in this thread
		maxDate := time.Time{}
		found := false

		for _, articleNum := range articleNums {
			// Debug logging for specific article in de.admin.mail
			//if groupName == "de.admin.mail" && articleNum == 1598 {
			//	log.Printf("DEBUG: Processing article %d in newsgroup %s, thread %d", articleNum, groupName, thread.root)
			//}

			var dateSent time.Time
			var dateStr sql.NullString
			err := database.RetryableQueryRowScan(groupDBs.DB, query_fixGroupThreadActivity2, []interface{}{articleNum}, &dateStr)

			if err != nil || !dateStr.Valid {
				log.Printf("Skipping article %d in thread %d: no valid date_sent\n", articleNum, thread.root)
				continue
			}

			//if groupName == "de.admin.mail" && articleNum == 1598 {
			//	log.Printf("DEBUG: Article %d in %s - Raw date_sent string: '%s'", articleNum, groupName, dateStr.String)
			//}

			// Try SQLite format first, then RFC3339 format (same as recover-db/main.go)
			var parseErr error
			dateSent, parseErr = time.Parse("2006-01-02 15:04:05", dateStr.String)
			if parseErr != nil {
				// Try RFC3339 format as fallback
				dateSent, parseErr = time.Parse(time.RFC3339, dateStr.String)
				if parseErr != nil {
					//if groupName == "de.admin.mail" && articleNum == 1598 {
					//	log.Printf("DEBUG: Article %d in %s - Failed to parse date_sent '%s' with both SQLite and RFC3339 formats: %v", articleNum, groupName, dateStr.String, parseErr)
					//}
					continue
				} else {
					//if groupName == "de.admin.mail" && articleNum == 1598 {
					//	log.Printf("DEBUG: Article %d in %s - Parsed date_sent with RFC3339 format: %v", articleNum, groupName, dateSent)
					//}
				}
			} else {
				//if groupName == "de.admin.mail" && articleNum == 1598 {
				//	log.Printf("DEBUG: Article %d in %s - Parsed date_sent with SQLite format: %v", articleNum, groupName, dateSent)
				//}
			}

			if !found || dateSent.After(maxDate) {
				maxDate = dateSent
				found = true
				//if groupName == "de.admin.mail" && articleNum == 1598 {
				//	log.Printf("DEBUG: Article %d in %s - Set as new maxDate: %v", articleNum, groupName, maxDate)
				//}
			}
		}

		// Update thread cache if we found a valid date
		if found && !maxDate.Equal(thread.lastActivity) {
			// Format as UTC string to avoid timezone encoding issues
			utcTimeStr := maxDate.UTC().Format("2006-01-02 15:04:05")

			_, err := database.RetryableExec(groupDBs.DB, query_fixGroupThreadActivity3, utcTimeStr, thread.root)

			if err != nil {
				log.Print("Failed to update thread activity for thread ", thread.root, ": ", err)
				continue // Skip threads that fail to update
			}
			log.Printf("Updated thread %d last_activity to %s\n", thread.root, utcTimeStr)
			updatedCount++
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating threads: %w", err)
	}

	return nil
}

// adminHideFuturePosts handles hiding future-dated posts for a specific newsgroup using the spam system
func (s *WebServer) adminHideFuturePosts(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get newsgroup name from form
	name := strings.TrimSpace(c.PostForm("newsgroup_name"))
	if name == "" {
		session.SetError("Newsgroup name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=newsgroups")
		return
	}

	// Check if newsgroup exists
	_, err := s.DB.MainDBGetNewsgroup(name)
	if err != nil {
		session.SetError("Newsgroup not found: " + name)
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Calculate the cutoff time (current time + 48 hours)
	cutoffTime := time.Now().Add(48 * time.Hour)

	// Get the group database for this newsgroup
	groupDBs, err := s.DB.GetGroupDBs(name)
	if err != nil {
		session.SetError("Failed to access newsgroup database: " + err.Error())
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Find articles that are posted more than 48 hours in the future and not already hidden
	articleRows, err := groupDBs.DB.Query("SELECT article_num FROM articles WHERE date_sent > ? AND hide = 0", cutoffTime.Format("2006-01-02 15:04:05"))
	if err != nil {
		groupDBs.Return(s.DB)
		session.SetError("Failed to query future articles: " + err.Error())
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	var futureArticles []int64
	for articleRows.Next() {
		var articleNum int64
		if err := articleRows.Scan(&articleNum); err != nil {
			continue // Skip problematic articles
		}
		futureArticles = append(futureArticles, articleNum)
	}
	articleRows.Close()
	groupDBs.Return(s.DB)

	if len(futureArticles) == 0 {
		session.SetSuccess("No future-dated articles found in newsgroup: " + name)
		c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
		return
	}

	// Process each future article using the proper spam increment system
	processedCount := 0
	for _, articleNum := range futureArticles {
		// Use the proper spam increment function which:
		// 1. Increments spam counter (spam = spam + 1)
		// 2. Adds entry to main spam table for admin tracking
		// 3. Handles proper error logging
		if err := s.DB.IncrementArticleSpam(name, articleNum); err != nil {
			continue // Skip articles that fail spam increment
		}

		// Also set the hide flag for these future-dated articles
		groupDBs, err := s.DB.GetGroupDBs(name)
		if err != nil {
			continue // Skip if can't get DB connection
		}
		_, err = database.RetryableExec(groupDBs.DB, "UPDATE articles SET hide = 1 WHERE article_num = ?", articleNum)
		groupDBs.Return(s.DB)

		if err != nil {
			continue // Skip articles that fail hide update
		}

		processedCount++
	}

	if processedCount > 0 {
		session.SetSuccess(fmt.Sprintf("Successfully processed %d future-dated articles in newsgroup %s. Articles marked as spam and hidden, now visible in spam management.", processedCount, name))
	} else {
		session.SetError("Failed to process any future-dated articles in newsgroup: " + name)
	}

	c.Redirect(http.StatusSeeOther, buildNewsgroupAdminRedirectURL(c))
}
