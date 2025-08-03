package web

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// NNTP User Management Functions

// generateRandomHex generates a random hexadecimal string of the specified length
func generateRandomHex(length int) (string, error) {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// countActiveNNTPUsers counts the number of active NNTP users
func (s *WebServer) countActiveNNTPUsers(nntpUsers []*models.NNTPUser) int {
	count := 0
	for _, user := range nntpUsers {
		if user.IsActive {
			count++
		}
	}
	return count
}

// countPostingNNTPUsers counts the number of NNTP users who can post
func (s *WebServer) countPostingNNTPUsers(nntpUsers []*models.NNTPUser) int {
	count := 0
	for _, user := range nntpUsers {
		if user.IsActive && user.Posting {
			count++
		}
	}
	return count
}

// adminCreateNNTPUser handles NNTP user creation
func (s *WebServer) adminCreateNNTPUser(c *gin.Context) {
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

	// Generate random credentials
	username, err := generateRandomHex(16)
	if err != nil {
		log.Printf("Error generating random username: %v", err)
		session.SetError("Failed to generate random username")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	password, err := generateRandomHex(16)
	if err != nil {
		log.Printf("Error generating random password: %v", err)
		session.SetError("Failed to generate random password")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Get form data (excluding username and password which are now generated)
	maxConnsStr := strings.TrimSpace(c.PostForm("maxconns"))
	postingStr := c.PostForm("posting")
	webUserIDStr := strings.TrimSpace(c.PostForm("web_user_id"))

	// Parse max connections
	maxConns := 1
	if maxConnsStr != "" {
		if parsed, err := strconv.Atoi(maxConnsStr); err == nil && parsed >= 1 && parsed <= 10 {
			maxConns = parsed
		} else {
			session.SetError("Max connections must be between 1 and 10")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
	}

	// Parse posting permission
	posting := postingStr == "on" || postingStr == "true"

	// Parse web user ID (default to admin user ID 1 if no mapping selected)
	webUserID := int64(1) // Default to admin user
	if webUserIDStr != "" && webUserIDStr != "0" {
		if parsed, err := strconv.ParseInt(webUserIDStr, 10, 64); err == nil {
			webUserID = parsed
		}
	}

	// Check if NNTP username already exists
	_, err = s.DB.GetNNTPUserByUsername(username)
	if err == nil {
		session.SetError("NNTP user already exists")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Create NNTP user
	nntpUser := &models.NNTPUser{
		Username:  username,
		Password:  password,
		MaxConns:  maxConns,
		Posting:   posting,
		WebUserID: webUserID,
		IsActive:  true,
	}

	err = s.DB.InsertNNTPUser(nntpUser)
	if err != nil {
		log.Printf("Error creating NNTP user: %v", err)
		session.SetError("Failed to create NNTP user")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	session.SetSuccess("NNTP user created successfully - Username: " + username + ", Password: " + password)
	c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
}

// adminUpdateNNTPUser handles NNTP user updates
func (s *WebServer) adminUpdateNNTPUser(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Get NNTP user ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Verify NNTP user exists
	_, err = s.DB.GetNNTPUserByID(id)
	if err != nil {
		session.SetError("NNTP user not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Get form data
	maxConnsStr := strings.TrimSpace(c.PostForm("maxconns"))
	postingStr := c.PostForm("posting")
	webUserIDStr := strings.TrimSpace(c.PostForm("web_user_id"))
	password := strings.TrimSpace(c.PostForm("password"))

	// Parse max connections
	if maxConnsStr != "" {
		if parsed, err := strconv.Atoi(maxConnsStr); err == nil && parsed >= 1 && parsed <= 10 {
			err = s.DB.UpdateNNTPUserPermissions(id, parsed, postingStr == "on" || postingStr == "true")
			if err != nil {
				log.Printf("Error updating NNTP user permissions: %v", err)
				session.SetError("Failed to update NNTP user permissions")
				c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
				return
			}
		} else {
			session.SetError("Max connections must be between 1 and 10")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
	}

	// Update password if provided
	if password != "" {
		if len(password) > 16 {
			session.SetError("Password must be 16 characters or less")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
		err = s.DB.UpdateNNTPUserPassword(id, password)
		if err != nil {
			log.Printf("Error updating NNTP user password: %v", err)
			session.SetError("Failed to update NNTP user password")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
	}

	// Update web user mapping if provided
	if webUserIDStr != "" {
		// This would require adding a new function to update web user mapping
		// For now, we'll skip this as it requires extending the database layer
	}

	session.SetSuccess("NNTP user updated successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
}

// adminDeleteNNTPUser handles NNTP user deletion
func (s *WebServer) adminDeleteNNTPUser(c *gin.Context) {
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

	// Get NNTP user ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Delete NNTP user
	err = s.DB.DeleteNNTPUser(id)
	if err != nil {
		log.Printf("Error deleting NNTP user: %v", err)
		session.SetError("Failed to delete NNTP user")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	session.SetSuccess("NNTP user deleted successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
}

// adminToggleNNTPUser handles activating/deactivating NNTP users
func (s *WebServer) adminToggleNNTPUser(c *gin.Context) {
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

	// Get NNTP user ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Get current user status and toggle
	nntpUser, err := s.DB.GetNNTPUserByID(id)
	if err != nil {
		session.SetError("NNTP user not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	if nntpUser.IsActive {
		err = s.DB.DeactivateNNTPUser(id)
		if err != nil {
			log.Printf("Error deactivating NNTP user: %v", err)
			session.SetError("Failed to deactivate NNTP user")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
		session.SetSuccess("NNTP user deactivated successfully")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
	} else {
		err = s.DB.ActivateNNTPUser(id)
		if err != nil {
			log.Printf("Error activating NNTP user: %v", err)
			session.SetError("Failed to activate NNTP user")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
		session.SetSuccess("NNTP user activated successfully")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
	}
}

// adminEnableNNTPUser handles activating NNTP users
func (s *WebServer) adminEnableNNTPUser(c *gin.Context) {
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

	// Get NNTP user ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Enable NNTP user
	err = s.DB.ActivateNNTPUser(id)
	if err != nil {
		log.Printf("Error enabling NNTP user: %v", err)
		session.SetError("Failed to enable NNTP user")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	session.SetSuccess("NNTP user enabled successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
}
