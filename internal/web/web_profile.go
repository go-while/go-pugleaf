package web

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ProfilePageData represents data for profile page
type ProfilePageData struct {
	TemplateData
	User    *models.User
	Error   string
	Success string
}

// profilePage displays the user profile
func (s *WebServer) profilePage(c *gin.Context) {
	// Check authentication
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login?redirect=/profile")
		return
	}

	// Get user details
	user, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "User Error", "Failed to load user profile")
		return
	}

	data := ProfilePageData{
		TemplateData: s.getBaseTemplateData(c, "Profile"),
		User:         user,
		Error:        c.Query("error"),
		Success:      c.Query("success"),
	}

	// Load template
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/profile.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template Error", err.Error())
	}
}

// profileUpdate handles profile updates
func (s *WebServer) profileUpdate(c *gin.Context) {
	// Check authentication
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login?redirect=/profile")
		return
	}

	// Get current user
	user, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil {
		session.SetError("Failed to load user")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	// Get form data
	email := strings.TrimSpace(c.PostForm("email"))
	currentPassword := c.PostForm("current_password")
	newPassword := c.PostForm("new_password")
	confirmPassword := c.PostForm("confirm_password")

	// Validate email
	if email == "" {
		session.SetError("Email is required")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	// Check if email is already taken by another user
	if email != user.Email {
		existingUser, err := s.DB.GetUserByEmail(email)
		if err == nil && existingUser.ID != user.ID {
			session.SetError("Email is already in use")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}
	}

	// If password change is requested
	if currentPassword != "" || newPassword != "" || confirmPassword != "" {
		// Validate current password
		if !checkPassword(currentPassword, user.PasswordHash) {
			session.SetError("Current password is incorrect")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}

		// Validate new password
		if newPassword != confirmPassword {
			session.SetError("New passwords do not match")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}

		if len(newPassword) < 6 {
			session.SetError("Password must be at least 6 characters")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}

		// Hash new password
		hashedPassword, err := hashPassword(newPassword)
		if err != nil {
			session.SetError("Failed to update password")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}

		// Update password
		err = s.DB.UpdateUserPassword(int64(user.ID), hashedPassword)
		if err != nil {
			session.SetError("Failed to update password")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}
	}

	// Update email
	err = s.DB.UpdateUserEmail(int64(user.ID), email)
	if err != nil {
		session.SetError("Failed to update email")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	session.SetSuccess("Profile updated successfully")
	c.Redirect(http.StatusSeeOther, "/profile")
}
