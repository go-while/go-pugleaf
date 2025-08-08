package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// checkGroupAccess validates if a user can access a group based on its active status
// Returns true if access is allowed, false otherwise
// For non-admin users, only active groups are accessible
// Admin users can access both active and inactive groups
func (s *WebServer) checkGroupAccess(c *gin.Context, groupName string) bool {
	// Check if user is admin
	session := s.getWebSession(c)
	var isAdminUser bool

	if session != nil {
		currentUser, err := s.DB.GetUserByID(int64(session.UserID))
		if err == nil {
			isAdminUser = s.isAdmin(currentUser)
		}
	}

	// Admin users can access any group
	if isAdminUser {
		return true
	}

	// Non-admin users can only access active groups
	activeGroup, err := s.DB.GetActiveNewsgroupByName(groupName)
	if err != nil || activeGroup == nil {
		// Group doesn't exist or is not active
		s.renderError(c, http.StatusNotFound, "Group Not Found", "The requested newsgroup does not exist or is not active.")
		return false
	}

	return true
}

// checkGroupAccessAPI validates if a user can access a group based on its active status for API endpoints
// Returns true if access is allowed, false otherwise and sends JSON error response
// For non-admin users, only active groups are accessible
// Admin users can access both active and inactive groups
func (s *WebServer) checkGroupAccessAPI(c *gin.Context, groupName string) bool {
	// Check if user is admin
	session := s.getWebSession(c)
	var isAdminUser bool

	if session != nil {
		currentUser, err := s.DB.GetUserByID(int64(session.UserID))
		if err == nil {
			isAdminUser = s.isAdmin(currentUser)
		}
	}

	// Admin users can access any group
	if isAdminUser {
		return true
	}

	// Non-admin users can only access active groups
	activeGroup, err := s.DB.GetActiveNewsgroupByName(groupName)
	if err != nil || activeGroup == nil {
		// Group doesn't exist or is not active
		c.JSON(http.StatusNotFound, gin.H{"error": "The requested newsgroup does not exist or is not active"})
		return false
	}

	return true
}
