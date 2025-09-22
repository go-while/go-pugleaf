package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ProfilePageData represents data for profile page
type ProfilePageData struct {
	TemplateData
	User                *models.User
	NNTPUser            *models.NNTPUser
	LocalNNTPServerAddr string
	Joined              string
	Error               string
	Success             string
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
	// Get NNTP user details if available
	nntpUser, err := s.DB.GetNNTPUserByWebUserID(int64(session.UserID))
	if err != nil {
		// NNTP user doesn't exist - this is okay, not all users have NNTP accounts
		nntpUser = nil
	}

	// days since User.CreatedAt
	timereg := int(time.Since(user.CreatedAt).Hours() / 24)
	joined := ""
	if timereg == 0 {
		joined = "today"
	} else if timereg == 1 {
		joined = "1 day ago"
	} else {
		joined = fmt.Sprintf("%d days ago", timereg)
	}
	localNNTPaddr, err := s.DB.GetConfigValue("LocalNNTPServerAddr")
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Config Error", "Failed to load NNTP server address")
		return
	}
	data := ProfilePageData{
		TemplateData:        s.getBaseTemplateData(c, "Profile"),
		User:                user,
		Joined:              joined,
		NNTPUser:            nntpUser,
		LocalNNTPServerAddr: localNNTPaddr,
		Error:               session.GetError(),
		Success:             session.GetSuccess(),
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
		c.Redirect(http.StatusSeeOther, "/logout")
		return
	}

	// Get form data
	email := strings.TrimSpace(c.PostForm("email"))
	currentPassword := c.PostForm("current_password")
	newPassword := c.PostForm("new_password")
	confirmPassword := c.PostForm("confirm_password")
	resetNNTPPassword := c.PostForm("reset_nntp_password")

	// Handle NNTP password reset
	if resetNNTPPassword == "true" {
		// Check if user has an NNTP account
		nntpUser, err := s.DB.GetNNTPUserByWebUserID(int64(session.UserID))
		if err != nil {
			session.SetError("No NNTP account found")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}

		// Generate new random 12-character password
		newNNTPPassword, err := generateRandomHex(12)
		if err != nil {
			session.SetError("Failed to generate new NNTP password")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}

		// Update NNTP password
		err = s.DB.UpdateNNTPUserPassword(nntpUser.ID, newNNTPPassword)
		if err != nil {
			session.SetError("Failed to reset NNTP password")
			c.Redirect(http.StatusSeeOther, "/profile")
			return
		}

		// Invalidate auth cache for this user
		s.DB.InvalidateNNTPUserAuth(nntpUser.Username)

		session.SetSuccess(fmt.Sprintf("NNTP password reset successfully. New password: %s", newNNTPPassword))
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

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

		// Validate password
		if err := validatePassword(newPassword); err != nil {
			session.SetError(err.Error())
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
