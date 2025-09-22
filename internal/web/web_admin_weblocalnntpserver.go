package web

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// adminSetWebLocalNNTPServerAddrInfo sets the WebLocalNNTPServerAddrInfo configuration
func (s *WebServer) adminSetWebLocalNNTPServerAddrInfo(c *gin.Context) {
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

	// Get the WebLocalNNTPServerAddrInfo from form
	webLocalNNTPServerAddrInfo := strings.TrimSpace(c.PostForm("web_local_nntp_server_addr_info"))
	
	// WebLocalNNTPServerAddrInfo can be empty, so we don't validate for emptiness

	// Set the configuration value
	err = s.DB.SetConfigValue("WebLocalNNTPServerAddrInfo", webLocalNNTPServerAddrInfo)
	if err != nil {
		session.SetError("Failed to set WebLocalNNTPServerAddrInfo: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}

	if webLocalNNTPServerAddrInfo == "" {
		session.SetSuccess("WebLocalNNTPServerAddrInfo cleared successfully")
	} else {
		session.SetSuccess("WebLocalNNTPServerAddrInfo set to: " + webLocalNNTPServerAddrInfo)
	}

	// Redirect back to admin settings tab
	c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
}
