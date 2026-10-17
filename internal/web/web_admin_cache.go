package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adminClearCache clears the sanitized content cache, newsgroup cache, and article cache
func (s *WebServer) adminClearCache(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

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
		session.SetSuccess("Cleared: " + joinStrings(cacheMessages, ", "))
	} else {
		session.SetError("No caches are initialized")
	}
	c.Redirect(http.StatusSeeOther, "/admin")
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
