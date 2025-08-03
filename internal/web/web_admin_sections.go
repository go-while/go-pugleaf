package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// SectionsHandler handles the main sections admin page
func (s *WebServer) SectionsHandler(c *gin.Context) {
	// This is handled by adminPage which includes sections data
	s.adminPage(c)
}

// CreateSectionHandler handles creating a new section
func (s *WebServer) CreateSectionHandler(c *gin.Context) {
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

	name := c.PostForm("name")
	displayName := c.PostForm("display_name")
	description := c.PostForm("description")
	sortOrderStr := c.PostForm("sort_order")
	showInHeader := c.PostForm("show_in_header") == "on"
	enableLocalSpool := c.PostForm("enable_local_spool") == "on"

	// Validate required fields
	if name == "" || displayName == "" {
		session.SetError("Section name and display name are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Parse sort order
	sortOrder := 0
	if sortOrderStr != "" {
		if parsed, err := strconv.Atoi(sortOrderStr); err == nil {
			sortOrder = parsed
		}
	}

	// Check if section name already exists
	exists, err := s.DB.SectionNameExists(name)
	if err != nil {
		log.Printf("Error checking section existence: %v", err)
		session.SetError("Database error occurred")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	if exists {
		session.SetError(fmt.Sprintf("Section with name '%s' already exists", name))
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Create the section
	section := &models.Section{
		Name:             name,
		DisplayName:      displayName,
		Description:      description,
		ShowInHeader:     showInHeader,
		EnableLocalSpool: enableLocalSpool,
		SortOrder:        sortOrder,
		CreatedAt:        time.Now(),
	}

	err = s.DB.CreateSection(section)
	if err != nil {
		log.Printf("Error creating section: %v", err)
		session.SetError("Failed to create section")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Refresh sections cache after creating a section
	s.refreshSectionsCache()

	session.SetSuccess(fmt.Sprintf("Section '%s' created successfully", displayName))
	c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
}

// UpdateSectionHandler handles updating an existing section
func (s *WebServer) UpdateSectionHandler(c *gin.Context) {
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

	idStr := c.PostForm("id")
	name := c.PostForm("name")
	displayName := c.PostForm("display_name")
	description := c.PostForm("description")
	sortOrderStr := c.PostForm("sort_order")
	showInHeader := c.PostForm("show_in_header") == "on"
	enableLocalSpool := c.PostForm("enable_local_spool") == "on"

	// Parse section ID
	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid section ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Validate required fields
	if name == "" || displayName == "" {
		session.SetError("Section name and display name are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Parse sort order
	sortOrder := 0
	if sortOrderStr != "" {
		if parsed, err := strconv.Atoi(sortOrderStr); err == nil {
			sortOrder = parsed
		}
	}

	// Check if section name already exists (excluding current section)
	exists, err := s.DB.SectionNameExistsExcluding(name, id)
	if err != nil {
		log.Printf("Error checking section existence: %v", err)
		session.SetError("Database error occurred")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	if exists {
		session.SetError(fmt.Sprintf("Section with name '%s' already exists", name))
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Update the section
	section := &models.Section{
		ID:               id,
		Name:             name,
		DisplayName:      displayName,
		Description:      description,
		ShowInHeader:     showInHeader,
		EnableLocalSpool: enableLocalSpool,
		SortOrder:        sortOrder,
	}

	err = s.DB.UpdateSection(section)
	if err != nil {
		log.Printf("Error updating section: %v", err)
		session.SetError("Failed to update section")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Refresh sections cache after updating a section
	s.refreshSectionsCache()

	session.SetSuccess(fmt.Sprintf("Section '%s' updated successfully", displayName))
	c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
}

// DeleteSectionHandler handles deleting a section
func (s *WebServer) DeleteSectionHandler(c *gin.Context) {
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

	idStr := c.PostForm("id")

	// Parse section ID
	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid section ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Get section name for feedback
	section, err := s.DB.GetSectionByID(id)
	if err != nil {
		log.Printf("Error getting section: %v", err)
		session.SetError("Section not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Delete the section (this will also delete associated section_groups in a transaction)
	err = s.DB.DeleteSection(id)
	if err != nil {
		log.Printf("Error deleting section: %v", err)
		session.SetError("Failed to delete section")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Refresh sections cache after deleting a section
	s.refreshSectionsCache()

	session.SetSuccess(fmt.Sprintf("Section '%s' deleted successfully", section.DisplayName))
	c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
}

// AssignNewsgroupHandler handles assigning newsgroups to a section using pattern matching
func (s *WebServer) AssignNewsgroupHandler(c *gin.Context) {
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

	newsgroupPattern := c.PostForm("newsgroup_pattern")
	sectionIDStr := c.PostForm("section_id")

	// Validate inputs
	if newsgroupPattern == "" || sectionIDStr == "" {
		session.SetError("Newsgroup pattern and section must be provided")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Parse section ID
	sectionID, err := strconv.Atoi(sectionIDStr)
	if err != nil {
		session.SetError("Invalid section ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Get section name for feedback
	section, err := s.DB.GetSectionByID(sectionID)
	if err != nil {
		log.Printf("Error getting section: %v", err)
		session.SetError("Section not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Find matching newsgroups
	var matchingNewsgroups []*models.Newsgroup
	if strings.Contains(newsgroupPattern, "%") {
		// Pattern-based search - use pattern directly as SQL LIKE pattern
		matchingNewsgroups, err = s.DB.GetNewsgroupsByPattern(newsgroupPattern)
		if err != nil {
			log.Printf("Error finding newsgroups by pattern: %v", err)
			session.SetError("Failed to find matching newsgroups")
			c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
			return
		}
	} else {
		// Exact match - single newsgroup
		newsgroup, err := s.DB.GetNewsgroupByName(newsgroupPattern)
		if err != nil {
			log.Printf("Error finding newsgroup '%s': %v", newsgroupPattern, err)
			session.SetError(fmt.Sprintf("Newsgroup '%s' not found", newsgroupPattern))
			c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
			return
		}
		if newsgroup != nil {
			matchingNewsgroups = []*models.Newsgroup{newsgroup}
		}
	}

	if len(matchingNewsgroups) == 0 {
		session.SetError(fmt.Sprintf("No newsgroups found matching pattern '%s'", newsgroupPattern))
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Process assignments
	assignedCount := 0
	skippedCount := 0
	//var assignedGroups []string

	for _, newsgroup := range matchingNewsgroups {
		// Check if assignment already exists
		exists, err := s.DB.SectionGroupExists(sectionID, newsgroup.Name)
		if err != nil {
			log.Printf("Error checking section group existence for %s: %v", newsgroup.Name, err)
			continue
		}

		if exists {
			skippedCount++
			continue
		}

		// Create the assignment
		sectionGroup := &models.SectionGroup{
			SectionID:        sectionID,
			NewsgroupName:    newsgroup.Name,
			GroupDescription: newsgroup.Description,
			SortOrder:        0, // Default sort order
			IsCategoryHeader: false,
			CreatedAt:        time.Now(),
		}

		err = s.DB.CreateSectionGroup(sectionGroup)
		if err != nil {
			log.Printf("Error creating section group for %s: %v", newsgroup.Name, err)
			continue
		}

		assignedCount++
		//assignedGroups = append(assignedGroups, newsgroup.Name)
	}

	// Prepare success message
	var message string
	if assignedCount > 0 {
		if assignedCount == 1 {
			message = fmt.Sprintf("1 newsgroup assigned to section '%s'", section.DisplayName)
		} else {
			message = fmt.Sprintf("%d newsgroups assigned to section '%s'", assignedCount, section.DisplayName)
		}

		if skippedCount > 0 {
			message += fmt.Sprintf(" (%d already assigned)", skippedCount)
		}
	} else {
		if skippedCount > 0 {
			message = fmt.Sprintf("All matching newsgroups are already assigned to section '%s'", section.DisplayName)
		} else {
			message = "No newsgroups were assigned"
		}
	}

	if assignedCount > 0 {
		session.SetSuccess(message)
	} else {
		session.SetError(message)
	}

	c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
}

// UnassignNewsgroupHandler handles removing a newsgroup from a section
func (s *WebServer) UnassignNewsgroupHandler(c *gin.Context) {
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

	idStr := c.PostForm("id")

	// Parse section group ID
	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid assignment ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Get section group info for feedback
	sectionGroup, err := s.DB.GetSectionGroupByID(id)
	if err != nil {
		log.Printf("Error getting section group: %v", err)
		session.SetError("Assignment not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	// Delete the assignment
	err = s.DB.DeleteSectionGroup(id)
	if err != nil {
		log.Printf("Error deleting section group: %v", err)
		session.SetError("Failed to remove newsgroup assignment")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
		return
	}

	session.SetSuccess(fmt.Sprintf("Newsgroup '%s' removed from section", sectionGroup.NewsgroupName))
	c.Redirect(http.StatusSeeOther, "/admin?tab=sections")
}
