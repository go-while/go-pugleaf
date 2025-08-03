package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adminClearCache clears the sanitized content cache, newsgroup cache, and article cache
func (s *WebServer) adminClearCache(c *gin.Context) {
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

	// Clear all caches
	cachesCleared := 0
	var cacheMessages []string

	// Clear sanitized content cache
	if cache := models.GetSanitizedCache(); cache != nil {
		cache.Clear()
		cachesCleared++
		cacheMessages = append(cacheMessages, "sanitized content cache")
	}

	// Clear newsgroup cache
	if ngCache := models.GetNewsgroupCache(); ngCache != nil {
		ngCache.Clear()
		cachesCleared++
		cacheMessages = append(cacheMessages, "newsgroup cache")
	}

	// Clear article cache
	if s.DB.ArticleCache != nil {
		s.DB.ArticleCache.Clear()
		cachesCleared++
		cacheMessages = append(cacheMessages, "article cache")
	}

	if cachesCleared > 0 {
		message := "Cleared: " + joinStrings(cacheMessages, ", ")
		session.SetSuccess(message)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": message, "caches_cleared": cachesCleared})
	} else {
		session.SetError("No caches are initialized")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "No caches are initialized"})
	}
}

// joinStrings is a simple helper to join strings with a separator
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
