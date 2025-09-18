package web

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// adminSetHostname sets the NNTP hostname configuration
func (s *WebServer) adminSetHostname(c *gin.Context) {
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

	// Get the hostname from form
	hostname := strings.TrimSpace(c.PostForm("hostname"))

	// Use SetHostname function to validate and save the hostname
	// This will handle validation and database persistence
	err = processor.SetHostname(hostname, s.DB)
	if err != nil {
		session.SetError("Failed to set hostname: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin")
		return
	}

	// Set success message based on whether hostname was set or cleared
	if hostname == "" {
		session.SetSuccess("NNTP hostname cleared successfully")
	} else {
		session.SetSuccess("NNTP hostname set to: " + hostname)
	}

	// Redirect back to admin page
	c.Redirect(http.StatusSeeOther, "/admin")
}
