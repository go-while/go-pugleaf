package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// adminEnableRegistration enables user registration
func (s *WebServer) adminEnableRegistration(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Get current user
	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}

	// Check if user is admin
	if !s.isAdmin(currentUser) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
		return
	}

	// Enable registration
	err = s.DB.SetConfigValue("registration_enabled", "true")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to enable registration"})
		return
	}

	// Redirect back to admin page with success
	c.Redirect(http.StatusSeeOther, "/admin?msg=Registration+enabled")
}

// adminDisableRegistration disables user registration
func (s *WebServer) adminDisableRegistration(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Get current user
	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}

	// Check if user is admin
	if !s.isAdmin(currentUser) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
		return
	}

	// Disable registration
	err = s.DB.SetConfigValue("registration_enabled", "false")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to disable registration"})
		return
	}

	// Redirect back to admin page with success
	c.Redirect(http.StatusSeeOther, "/admin?msg=Registration+disabled")
}
