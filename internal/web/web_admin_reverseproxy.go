package web

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// adminSetReverseProxyAddr sets the reverse proxy address configuration
func (s *WebServer) adminSetReverseProxyAddr(c *gin.Context) {
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

	// Get the reverse proxy address from form
	reverseProxyAddr := strings.TrimSpace(c.PostForm("reverse_proxy_addr"))

	// Empty is allowed (to disable reverse proxy)
	// Use SetConfigValue function to save the reverse proxy address
	// This will handle database persistence
	err = s.DB.SetConfigValue("ReverseProxyAddr", reverseProxyAddr)
	if err != nil {
		session.SetError("Failed to set reverse proxy address: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}

	if reverseProxyAddr == "" {
		session.SetSuccess("Reverse proxy address cleared (reverse proxy disabled)")
	} else {
		session.SetSuccess("Ok. Reboot webserver now! Reverse proxy address set to: " + reverseProxyAddr)
	}

	// Redirect back to admin settings tab
	c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
}
