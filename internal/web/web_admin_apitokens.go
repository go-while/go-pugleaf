package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
)

// countEnabledAPITokens counts how many API tokens are enabled
func (s *WebServer) countEnabledAPITokens(tokens []*database.APIToken) int {
	count := 0
	for _, token := range tokens {
		if token.IsEnabled {
			count++
		}
	}
	return count
}

// adminCreateAPIToken handles API token creation from admin form
func (s *WebServer) adminCreateAPIToken(c *gin.Context) {
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
	ownerName := strings.TrimSpace(c.PostForm("owner_name"))
	ownerIDStr := strings.TrimSpace(c.PostForm("owner_id"))
	expiresAtStr := strings.TrimSpace(c.PostForm("expires_at"))

	// Validate required fields
	if ownerName == "" {
		session.SetError("Owner name is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	// Parse owner ID (optional)
	ownerID := 0
	if ownerIDStr != "" {
		ownerID, err = strconv.Atoi(ownerIDStr)
		if err != nil {
			session.SetError("Invalid owner ID")
			c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
			return
		}
	}

	// Parse expiration date (optional)
	var expiresAt *time.Time
	if expiresAtStr != "" {
		parsedTime, err := time.Parse("2006-01-02", expiresAtStr)
		if err != nil {
			session.SetError("Invalid expiration date format (use YYYY-MM-DD)")
			c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
			return
		}
		expiresAt = &parsedTime
	}

	// Create API token
	_, plainToken, err := s.DB.CreateAPIToken(ownerName, ownerID, expiresAt)
	if err != nil {
		session.SetError("Failed to create API token")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	// Store the plain token in session flash for one-time display
	session.SetSuccess("API token created successfully. Token: " + plainToken + " - Save this token securely - it will not be shown again!")
	c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
}

// adminToggleAPIToken handles enabling/disabling API tokens
func (s *WebServer) adminToggleAPIToken(c *gin.Context) {
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
	idStr := strings.TrimSpace(c.PostForm("id"))
	action := strings.TrimSpace(c.PostForm("action"))

	if idStr == "" || (action != "enable" && action != "disable") {
		session.SetError("Invalid token ID or action")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	// Parse ID
	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid token ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	// Perform action
	if action == "enable" {
		err = s.DB.EnableAPIToken(id)
	} else {
		err = s.DB.DisableAPIToken(id)
	}

	if err != nil {
		session.SetError("Failed to " + action + " API token")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	session.SetSuccess("API token " + action + "d successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
}

// adminDeleteAPIToken handles API token deletion
func (s *WebServer) adminDeleteAPIToken(c *gin.Context) {
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

	// Get token ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid token ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid token ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	// Delete token
	err = s.DB.DeleteAPIToken(id)
	if err != nil {
		session.SetError("Failed to delete API token")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	session.SetSuccess("API token deleted successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
}

// adminCleanupExpiredTokens handles cleanup of expired tokens
func (s *WebServer) adminCleanupExpiredTokens(c *gin.Context) {
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

	// Cleanup expired tokens
	count, err := s.DB.CleanupExpiredTokens()
	if err != nil {
		session.SetError("Failed to cleanup expired tokens")
		c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
		return
	}

	if count > 0 {
		session.SetSuccess(strconv.Itoa(count) + " expired tokens cleaned up successfully")
	} else {
		session.SetSuccess("No expired tokens found to cleanup")
	}
	c.Redirect(http.StatusSeeOther, "/admin?tab=apitokens")
}
