package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// countAdminUsers counts how many users have admin permissions
func (s *WebServer) countAdminUsers(users []*models.User) int {
	count := 0
	for _, user := range users {
		if s.isAdmin(user) {
			count++
		}
	}
	return count
}

// countActiveSessions returns the number of active sessions (placeholder)
func (s *WebServer) countActiveSessions() int {
	// TODO: Implement actual session counting
	return 0
}

// adminCreateUser handles user creation
func (s *WebServer) adminCreateUser(c *gin.Context) {
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
	username := strings.TrimSpace(c.PostForm("username"))
	email := strings.TrimSpace(c.PostForm("email"))
	displayName := strings.TrimSpace(c.PostForm("displayName"))
	password := c.PostForm("password")

	// Validate input
	if username == "" || email == "" || password == "" {
		session.SetError("All fields are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	if len(password) < 6 {
		session.SetError("Password must be at least 6 characters")
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

	// Validate input
	if email == "" {
		session.SetError("Email is required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Update email
	err = s.DB.UpdateUserEmail(userID, email)
	if err != nil {
		session.SetError("Failed to update user")
		c.Redirect(http.StatusSeeOther, "/admin?tab=users")
		return
	}

	// Update display name (we need to add this method)
	// For now, just update email
	session.SetSuccess("User updated successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=users")
}

// adminDeleteUser handles user deletion
func (s *WebServer) adminDeleteUser(c *gin.Context) {
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
