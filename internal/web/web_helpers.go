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
	// Admin users can access inactive groups too, but the group must exist:
	// callers open the group DB next, and GetGroupDB creates files for any name
	if s.isAdminRequest(c) {
		if _, err := s.DB.GetNewsgroupID(groupName); err != nil {
			s.renderError(c, http.StatusNotFound, "Group Not Found", "The requested newsgroup does not exist or is not active.")
			return false
		}
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
	// Admin users can access inactive groups too, but the group must exist:
	// callers open the group DB next, and GetGroupDB creates files for any name
	if s.isAdminRequest(c) {
		if _, err := s.DB.GetNewsgroupID(groupName); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "The requested newsgroup does not exist or is not active"})
			return false
		}
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
