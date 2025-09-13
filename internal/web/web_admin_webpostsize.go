package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// adminSetWebPostSize sets the WebPostMaxArticleSize configuration
func (s *WebServer) adminSetWebPostSize(c *gin.Context) {
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

	// Get the size from form
	sizeStr := strings.TrimSpace(c.PostForm("size"))

	// Validate the size
	if sizeStr == "" {
		session.SetError("Size cannot be empty")
		c.Redirect(http.StatusSeeOther, "/admin")
		return
	}

	size, err := strconv.Atoi(sizeStr)
	if err != nil {
		session.SetError("Invalid size format: must be a number")
		c.Redirect(http.StatusSeeOther, "/admin")
		return
	}

	// Validate size range (minimum 1KB, maximum 1MB)
	if size < 1024 {
		session.SetError("Size must be at least 1024 bytes (1KB)")
		c.Redirect(http.StatusSeeOther, "/admin")
		return
	}

	if size > 1048576 {
		session.SetError("Size must not exceed 1048576 bytes (1MB)")
		c.Redirect(http.StatusSeeOther, "/admin")
		return
	}

	// Save to database
	err = s.DB.SetConfigValue("WebPostMaxArticleSize", sizeStr)
	if err != nil {
		session.SetError("Failed to save configuration: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin")
		return
	}

	// Set success message
	session.SetSuccess("Web post article size limit set to: " + sizeStr + " bytes")

	// Redirect back to admin page
	c.Redirect(http.StatusSeeOther, "/admin")
}
