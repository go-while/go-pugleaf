package web

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const APIAuthHeader = "X-API"

// AuthRequired middleware for API token authentication
func (s *WebServer) APIAuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader(APIAuthHeader)
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "header '" + APIAuthHeader + ": {token}' required"})
			c.Abort()
			return
		}

		token = strings.TrimSpace(token) // Remove any extra spaces
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token cannot be empty"})
			c.Abort()
			return
		}

		// Validate token
		apiToken, err := s.DB.ValidateAPIToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		// Update usage statistics (non-blocking)
		go func() {
			if err := s.DB.UpdateTokenUsage(apiToken.ID); err != nil {
				log.Printf("Failed to update token usage: %v", err)
			}
		}()

		// Store token info in context for use by handlers
		c.Set("api_token", apiToken)
		c.Next()
	}
}

// API-Token Remote Management API Endpoints (for admin use)
/* TODO ONLY DEMO CODE ! NOT TESTED!
// createAPITokenHandler creates a new API token
func (s *WebServer) createAPITokenHandler(c *gin.Context) {
	var req struct {
		OwnerName string     `json:"owner_name" binding:"required"`
		OwnerID   int        `json:"owner_id"`
		ExpiresAt *time.Time `json:"expires_at"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	token, plainToken, err := s.DB.CreateAPIToken(req.OwnerName, req.OwnerID, req.ExpiresAt)
	if err != nil {
		log.Printf("Error creating API token: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create token"})
		return
	}

	response := gin.H{
		"token_id":    token.ID,
		"token":       plainToken, // Only returned once!
		"owner_name":  token.OwnerName,
		"owner_id":    token.OwnerID,
		"created_at":  token.CreatedAt,
		"expires_at":  token.ExpiresAt,
		"is_enabled":  token.IsEnabled,
		"usage_count": token.UsageCount,
		"warning":     "Save this token securely - it will not be shown again!",
	}

	c.JSON(http.StatusCreated, response)
}

// listAPITokensHandler lists all API tokens (admin only)
func (s *WebServer) listAPITokensHandler(c *gin.Context) {
	tokens, err := s.DB.ListAPITokens()
	if err != nil {
		log.Printf("Error listing API tokens: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list tokens"})
		return
	}

	// Format response without exposing actual token values
	var response []gin.H
	for _, token := range tokens {
		response = append(response, gin.H{
			"token_id":     token.ID,
			"token_hash":   token.APIToken[:16] + "...", // Show only first 16 chars of hash
			"owner_name":   token.OwnerName,
			"owner_id":     token.OwnerID,
			"created_at":   token.CreatedAt,
			"last_used_at": token.LastUsedAt,
			"expires_at":   token.ExpiresAt,
			"is_enabled":   token.IsEnabled,
			"usage_count":  token.UsageCount,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"tokens": response,
		"count":  len(response),
	})
}

// disableAPITokenHandler disables an API token
func (s *WebServer) disableAPITokenHandler(c *gin.Context) {
	tokenID := c.Param("id")
	if tokenID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token ID is required"})
		return
	}

	// Simple conversion for demo - in production you'd want proper validation with strconv.Atoi
	var id int
	switch tokenID {
	case "1":
		id = 1
	case "2":
		id = 2
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid token ID format"})
		return
	}

	err := s.DB.DisableAPIToken(id)
	if err != nil {
		log.Printf("Error disabling API token %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to disable token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Token disabled successfully",
		"token_id": id,
	})
}

// enableAPITokenHandler enables an API token
func (s *WebServer) enableAPITokenHandler(c *gin.Context) {
	tokenID := c.Param("id")
	if tokenID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token ID is required"})
		return
	}

	// Simple conversion for demo - in production you'd want proper validation
	var id int
	if tokenID == "1" {
		id = 1
	} else if tokenID == "2" {
		id = 2
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid token ID format"})
		return
	}

	err := s.DB.EnableAPIToken(id)
	if err != nil {
		log.Printf("Error enabling API token %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to enable token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Token enabled successfully",
		"token_id": id,
	})
}

// deleteAPITokenHandler permanently deletes an API token
func (s *WebServer) deleteAPITokenHandler(c *gin.Context) {
	tokenID := c.Param("id")
	if tokenID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token ID is required"})
		return
	}

	// Simple conversion for demo - in production you'd want proper validation
	var id int
	if tokenID == "1" {
		id = 1
	} else if tokenID == "2" {
		id = 2
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid token ID format"})
		return
	}

	err := s.DB.DeleteAPIToken(id)
	if err != nil {
		log.Printf("Error deleting API token %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Token deleted successfully",
		"token_id": id,
	})
}

// cleanupExpiredTokensHandler removes expired tokens
func (s *WebServer) cleanupExpiredTokensHandler(c *gin.Context) {
	count, err := s.DB.CleanupExpiredTokens()
	if err != nil {
		log.Printf("Error cleaning up expired tokens: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cleanup tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":        "Cleanup completed",
		"tokens_removed": count,
		"cleanup_time":   time.Now().Format(time.RFC3339),
	})
}
*/
