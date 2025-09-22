package web

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// adminSetAbuseMail sets the AbuseMail configuration
func (s *WebServer) adminSetAbuseMail(c *gin.Context) {
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

	// Get the abuse email from form
	abuseMail := strings.TrimSpace(c.PostForm("abuse_mail"))
	if abuseMail == "" {
		session.SetError("Abuse email cannot be empty")
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}

	// Validate email format
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(abuseMail) {
		session.SetError("Invalid email format for abuse email")
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}

	// Save to database
	err = s.DB.SetConfigValue("AbuseMail", abuseMail)
	if err != nil {
		session.SetError("Failed to set abuse email: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}

	session.SetSuccess("Abuse email set to: " + abuseMail)
	c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
}
