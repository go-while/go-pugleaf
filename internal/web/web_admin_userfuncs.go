package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// countAdminUsers counts how many users have admin permissions
func (s *WebServer) countAdminUsers(users []*models.User) (count int64) {
	for _, user := range users {
		if s.isAdmin(user) {
			count++
		}
	}
	return
}

// countActiveSessions returns the number of active sessions (placeholder)
func (s *WebServer) countActiveSessions() int64 {
	// TODO: Implement actual session counting
	return 0
}

// adminCreateUser handles user creation
func (s *WebServer) adminCreateUser(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get form data
	var err error
	username := strings.TrimSpace(c.PostForm("username"))
	email := strings.TrimSpace(c.PostForm("email"))
	displayName := strings.TrimSpace(c.PostForm("displayName"))
	password := c.PostForm("password")

	// Get checkbox values (checkboxes not present = 0, present = 1)
	verified := 0
	if c.PostForm("verified") == "on" {
		verified = 1
	}
	disabled := 0
	if c.PostForm("disabled") == "on" {
		disabled = 1
	}
	noPosting := 0
	if c.PostForm("no_posting") == "on" {
		noPosting = 1
	}

	// Validate input
	if username == "" || email == "" || password == "" {
		session.SetError("All fields are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	if len(password) < 12 {
		session.SetError("Password must be at least 12 characters")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Check if username already exists
	_, err = s.DB.GetUserByUsername(username)
	if err == nil {
		session.SetError("Username already exists")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Check if email already exists
	_, err = s.DB.GetUserByEmail(email)
	if err == nil {
		session.SetError("Email already exists")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Hash password
	hashedPassword, err := hashPassword(password)
	if err != nil {
		session.SetError("Failed to create user")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Create user
	user := &models.User{
		Username:     username,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: hashedPassword,
		Verified:     verified,
		Disabled:     disabled,
		NoPosting:    noPosting,
		PostCount:    0,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	err = s.DB.InsertUser(user)
	if err != nil {
		session.SetError("Failed to create user")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Get the created user to obtain the ID
	createdUser, err := s.DB.GetUserByUsername(username)
	if err != nil {
		session.SetSuccess("User created successfully (but NNTP account creation failed)")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Automatically create an NNTP user for this web user
	err = s.DB.CreateNNTPUserForWebUser(int64(createdUser.ID))
	if err != nil {
		session.SetSuccess("User created successfully (but NNTP account creation failed)")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	session.SetSuccess("User and NNTP account created successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=users")
}

// adminUpdateUser handles user updates
func (s *WebServer) adminUpdateUser(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get user ID
	userIDStr := c.PostForm("user_id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		session.SetError("Invalid user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Get form data
	email := strings.TrimSpace(c.PostForm("email"))

	// Get checkbox values (checkboxes not present = 0, present = 1)
	verified := 0
	if c.PostForm("verified") == "1" {
		verified = 1
	}
	disabled := 0
	if c.PostForm("disabled") == "1" {
		disabled = 1
	}
	noPosting := 0
	if c.PostForm("no_posting") == "1" {
		noPosting = 1
	}

	// Validate input
	if email == "" {
		session.SetError("Email is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Update email
	err = s.DB.UpdateUserEmail(userID, email)
	if err != nil {
		session.SetError("Failed to update user email")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Update user status fields
	err = s.DB.UpdateUserStatus(userID, verified, disabled, noPosting)
	if err != nil {
		session.SetError("Failed to update user status")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Update display name (we need to add this method)
	// For now, just update email

	// Handle NNTP user updates if NNTP ID is provided
	nntpIDStr := strings.TrimSpace(c.PostForm("nntp_id"))
	if nntpIDStr != "" {
		nntpID, err := strconv.Atoi(nntpIDStr)
		if err == nil {
			// Update NNTP user permissions
			nntpMaxConnsStr := strings.TrimSpace(c.PostForm("nntp_maxconns"))
			nntpPosting := c.PostForm("nntp_posting") == "on"
			nntpPassword := strings.TrimSpace(c.PostForm("nntp_password"))

			// Update NNTP permissions
			if nntpMaxConnsStr != "" {
				if parsed, err := strconv.Atoi(nntpMaxConnsStr); err == nil && parsed >= 1 && parsed <= 10 {
					err = s.DB.UpdateNNTPUserPermissions(nntpID, parsed, nntpPosting)
					if err != nil {
						log.Printf("Error updating NNTP user permissions: %v", err)
						session.SetError("Failed to update NNTP user permissions")
						c.Redirect(http.StatusSeeOther, "/admin?tab=users")
						return
					}
				}
			}

			// Update NNTP password if provided
			if nntpPassword != "" {
				err = s.DB.UpdateNNTPUserPassword(nntpID, nntpPassword)
				if err != nil {
					log.Printf("Error updating NNTP user password: %v", err)
					session.SetError("Failed to update NNTP user password")
					c.Redirect(http.StatusSeeOther, "/admin?tab=users")
					return
				}
			}
		}
	}

	session.SetSuccess("User updated successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=users")
}

// adminDeleteUser handles user deletion
func (s *WebServer) adminDeleteUser(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)
	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil {
		session.SetError("Failed to load user")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	// Get user ID
	userIDStr := c.PostForm("user_id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		session.SetError("Invalid user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Prevent deleting own account
	if userID == int64(currentUser.ID) {
		session.SetError("Cannot delete your own account")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Delete user and all associated data
	err = s.DB.DeleteUser(userID)
	if err != nil {
		session.SetError(fmt.Sprintf("Failed to delete user: %v", err))
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	session.SetSuccess("User deleted successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=users")
}

// isAdmin checks if user has admin permissions
func (s *WebServer) isAdmin(user *models.User) bool {
	// Simple check - you can implement more sophisticated permission checking
	// For now, check if user has admin permission or is user ID 1
	if user.ID == 1 {
		return true
	}

	permissions, err := s.DB.GetUserPermissions(user.ID)
	if err != nil {
		return false
	}

	for _, perm := range permissions {
		if perm.Permission == "admin" {
			return true
		}
	}

	return false
}
