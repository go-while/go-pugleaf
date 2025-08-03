package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

const maxChatInputLineLength = 1024

const ollamaProxyURL = "http://ollama-proxy.local:21434/proxy"

// Rate limiting for AI chat
var (
	chatRateLimiter = make(map[string]time.Time) // sessionID -> last request time
	rateLimiterMux  sync.RWMutex
	chatCooldown    = 5 * time.Second
)

// Chat history cache - in-memory storage per session
var (
	chatHistoryCache = make(map[string][]ChatMessage) // sessionToken -> chat history
	chatCacheMux     sync.RWMutex
	maxHistoryLength = 200 // Keep last N messages per session
)

// getModelCacheKey generates a model-specific cache key
func getModelCacheKey(sessionToken, modelPostKey string) string {
	return sessionToken + "_" + modelPostKey
}

// getChatHistoryCount gets chat history count for a specific model
func getChatHistoryCount(sessionToken, modelPostKey string) int {
	chatCacheMux.RLock()
	defer chatCacheMux.RUnlock()

	cacheKey := getModelCacheKey(sessionToken, modelPostKey)
	if history, exists := chatHistoryCache[cacheKey]; exists {
		return len(history)
	}
	return 0
}

// getAllChatHistoryCounts gets chat history counts for all models
func getAllChatHistoryCounts(sessionToken string, models []*models.AIModel) map[string]int {
	counts := make(map[string]int)
	for _, model := range models {
		counts[model.PostKey] = getChatHistoryCount(sessionToken, model.PostKey)
	}
	return counts
}

// ChatMessage represents a single chat message
// (extend as needed for frontend)
type ChatMessage struct {
	Role    string // "user" or "assistant"
	Content string
}

// AIChatPageData for the chat page template
type AIChatPageData struct {
	TemplateData
	ChatHistory     []ChatMessage
	Error           string
	SessionToken    string            // Strong session token for chat history
	AvailableModels []*models.AIModel // Available AI models for selection
	DefaultModel    *models.AIModel   // Default selected model
	ChatCounts      map[string]int    // Chat message counts per model
	MaxInputLength  int               // Maximum input length for chat messages
}

// aichatPage renders the AI chat page
func (s *WebServer) aichatPage(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login?redirect=/aichat")
		return
	}

	// Use web session ID as chat session token for persistence across page reloads
	sessionToken := session.SessionID

	// Load available AI models from database
	availableModels, err := s.DB.GetActiveAIModels()
	if err != nil {
		log.Printf("Error loading AI models: %v", err)
		s.renderChatError(c, "Database Error", "Failed to load AI models. Please try again later.")
		return
	}

	// Load default AI model
	defaultModel, err := s.DB.GetDefaultAIModel()
	if err != nil {
		log.Printf("Error loading default AI model: %v", err)
		// If no default found, use first available model as fallback
		if len(availableModels) > 0 {
			defaultModel = availableModels[0]
		} else {
			s.renderChatError(c, "Configuration Error", "No active AI models found. Please contact administrator.")
			return
		}
	}

	// Load existing chat history for the default model
	defaultCacheKey := getModelCacheKey(sessionToken, defaultModel.PostKey)
	chatCacheMux.RLock()
	existingHistory := chatHistoryCache[defaultCacheKey]
	if existingHistory == nil {
		existingHistory = []ChatMessage{}
	}
	chatCacheMux.RUnlock()

	// Get chat counts for all models
	chatCounts := getAllChatHistoryCounts(sessionToken, availableModels)

	data := AIChatPageData{
		TemplateData:    s.getBaseTemplateData(c, "AI Chat"),
		ChatHistory:     existingHistory,
		Error:           c.Query("error"),
		SessionToken:    sessionToken,
		AvailableModels: availableModels,
		DefaultModel:    defaultModel,
		ChatCounts:      chatCounts,
		MaxInputLength:  maxChatInputLineLength,
	}

	tmpl, err := template.ParseFiles("web/templates/base_chat.html", "web/templates/aichat.html")
	if err != nil {
		log.Printf("Failed to parse chat templates: %v", err)
		s.renderChatError(c, "Template Parse Error", fmt.Sprintf("Failed to load chat interface: %v", err))
		return
	}

	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base_chat.html", data)
	if err != nil {
		log.Printf("Failed to execute chat template: %v", err)
		s.renderChatError(c, "Template Execution Error", fmt.Sprintf("Failed to render chat interface: %v", err))
		return
	}
}

// aichatSend handles chat message POSTs and proxies to Ollama
func (s *WebServer) aichatSend(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Rate limiting logic
	rateLimiterMux.RLock()
	lastRequestTime, ok := chatRateLimiter[session.SessionID]
	rateLimiterMux.RUnlock()

	if ok && time.Since(lastRequestTime) < chatCooldown {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests. Please wait before sending another message."})
		return
	}

	// Update last request time for rate limiting (before processing the request)
	rateLimiterMux.Lock()
	chatRateLimiter[session.SessionID] = time.Now()
	rateLimiterMux.Unlock()

	// Accept new message, session token, and model selection from frontend
	var req struct {
		Message      string `json:"message"`
		SessionToken string `json:"sessionToken"`
		Model        string `json:"model"` // Selected model's post key
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Message == "" || req.SessionToken == "" {
		log.Printf("AI Chat Invalid request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: message and sessionToken required"})
		return
	}

	// If no model specified, use default
	modelPostKey := req.Model
	if modelPostKey == "" {
		defaultModel, err := s.DB.GetDefaultAIModel()
		if err != nil {
			log.Printf("AI Chat no default model: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No default AI model configured"})
			return
		}
		modelPostKey = defaultModel.PostKey
	}

	// Validate the selected model exists and is active
	selectedModel, err := s.DB.GetAIModelByPostKey(modelPostKey)
	if err != nil || !selectedModel.IsActive {
		log.Printf("AI Chat invalid model: %s, err: %v", modelPostKey, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or inactive AI model selected"})
		return
	}

	// Validate message length (N character limit)
	if len(req.Message) > maxChatInputLineLength {
		log.Printf("AI Chat message too long: %d chars", len(req.Message))
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message too long..."})
		return
	}

	// Get existing chat history from model-specific cache
	modelCacheKey := getModelCacheKey(req.SessionToken, modelPostKey)
	chatCacheMux.RLock()
	history := chatHistoryCache[modelCacheKey]
	if history == nil {
		history = []ChatMessage{}
	} else {
		// Make a copy to avoid race conditions
		historyCopy := make([]ChatMessage, len(history))
		copy(historyCopy, history)
		history = historyCopy
	}
	chatCacheMux.RUnlock()

	// Add new user message to history
	userMessage := ChatMessage{Role: "user", Content: req.Message}
	history = append(history, userMessage)

	log.Printf("Received chat request: session=%s, message='%s', model='%s', history_len=%d", req.SessionToken[:8], req.Message, selectedModel.DisplayName, len(history))

	// Prepare request for proxy (using the real Ollama model name)
	proxyReq := struct {
		Model    string        `json:"model"`
		Messages []ChatMessage `json:"messages"`
	}{
		Model:    selectedModel.OllamaModelName, // Use the real Ollama model name
		Messages: history,
	}

	proxyBody, err := json.Marshal(proxyReq)
	if err != nil {
		log.Printf("AI Chat Proxy encode error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode request"})
		return
	}

	resp, err := http.Post(ollamaProxyURL, "application/json", bytes.NewReader(proxyBody))
	if err != nil {
		log.Printf("AI Chat Proxy request error: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Ollama proxy error"})
		return
	}
	defer resp.Body.Close()

	// Parse the proxy response (simple format: {"reply": "..."})
	var proxyResp struct {
		Reply string `json:"reply"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&proxyResp); err != nil {
		log.Printf("AI Chat Proxy decode error: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to decode proxy response"})
		return
	}

	if proxyResp.Reply == "" {
		log.Printf("AI Chat Proxy no reply from AI")
		c.JSON(http.StatusBadGateway, gin.H{"error": "No reply from AI"})
		return
	}

	// Add AI response to history
	aiMessage := ChatMessage{Role: "assistant", Content: proxyResp.Reply}
	history = append(history, aiMessage)

	// Trim history if it gets too long (keep last N messages)
	if len(history) > maxHistoryLength {
		history = history[len(history)-maxHistoryLength:]
	}

	// Save updated history to model-specific cache
	chatCacheMux.Lock()
	chatHistoryCache[modelCacheKey] = history
	chatCacheMux.Unlock()

	log.Printf("AI Chat got reply: %s", proxyResp.Reply)
	c.JSON(http.StatusOK, gin.H{"reply": proxyResp.Reply})

}

// aichatModels returns available AI models for frontend selection
func (s *WebServer) aichatModels(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Get active AI models from database
	models, err := s.DB.GetActiveAIModels()
	if err != nil {
		log.Printf("Error loading AI models: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load AI models"})
		return
	}

	// Get default model
	defaultModel, err := s.DB.GetDefaultAIModel()
	if err != nil {
		log.Printf("Error loading default AI model: %v", err)
		// Continue without default if none found
	}

	response := gin.H{
		"models":  models,
		"default": defaultModel,
	}

	c.JSON(http.StatusOK, response)
}

// aichatLoadHistory loads chat history for a specific model
func (s *WebServer) aichatLoadHistory(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Get model from URL parameter
	modelPostKey := c.Param("model")

	// Get sessionToken from JSON body
	var req struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionToken required in request body"})
		return
	}

	if modelPostKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model parameter required in URL"})
		return
	}

	// Validate model
	selectedModel, err := s.DB.GetAIModelByPostKey(modelPostKey)
	if err != nil || !selectedModel.IsActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid model"})
		return
	}

	// Get model-specific history
	modelCacheKey := getModelCacheKey(req.SessionToken, modelPostKey)
	chatCacheMux.RLock()
	history := chatHistoryCache[modelCacheKey]
	if history == nil {
		history = []ChatMessage{}
	}
	chatCacheMux.RUnlock()

	c.JSON(http.StatusOK, gin.H{
		"history": history,
		"model":   selectedModel.DisplayName,
		"count":   len(history),
	})
}

// aichatClearHistory clears chat history for a specific model or all models
func (s *WebServer) aichatClearHistory(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Get model from URL parameter (can be "all" for clearing all)
	modelParam := c.Param("model")

	// Get sessionToken from form data or JSON body
	var sessionToken string

	// Try form data first (for HTML form submissions)
	sessionToken = c.PostForm("sessionToken")

	// If not in form data, try JSON body (for AJAX requests)
	if sessionToken == "" {
		var req struct {
			SessionToken string `json:"sessionToken"`
		}
		if err := c.ShouldBindJSON(&req); err == nil && req.SessionToken != "" {
			sessionToken = req.SessionToken
		}
	}

	if sessionToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionToken required in request body or form data"})
		return
	}

	chatCacheMux.Lock()
	defer chatCacheMux.Unlock()

	if modelParam == "all" {
		// Clear all model histories for this session
		toDelete := []string{}
		for key := range chatHistoryCache {
			if strings.HasPrefix(key, sessionToken+"_") {
				toDelete = append(toDelete, key)
			}
		}
		for _, key := range toDelete {
			delete(chatHistoryCache, key)
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "All chats cleared"})
	} else if modelParam != "" {
		// Clear specific model history
		modelCacheKey := getModelCacheKey(sessionToken, modelParam)
		delete(chatHistoryCache, modelCacheKey)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Model chat cleared"})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model parameter required in URL"})
	}
}

// aichatGetCounts returns chat message counts for all models
func (s *WebServer) aichatGetCounts(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	sessionToken := c.Query("sessionToken")
	if sessionToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionToken parameter required"})
		return
	}

	availableModels, err := s.DB.GetActiveAIModels()
	if err != nil {
		log.Printf("Error loading AI models: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load AI models"})
		return
	}

	// Get chat counts for all models
	chatCounts := getAllChatHistoryCounts(sessionToken, availableModels)

	c.JSON(http.StatusOK, gin.H{
		"counts": chatCounts,
	})
}

// renderChatError renders an error within the chat interface instead of using the main site error page
func (s *WebServer) renderChatError(c *gin.Context, title, message string) {
	// Create a minimal error template that matches the chat interface style
	errorHTML := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>AI Chat Error</title>
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">
    <style>
        html, body {
            margin: 0 !important;
            padding: 0 !important;
            height: 100% !important;
            overflow: hidden !important;
            font-family: 'Courier New', 'Monaco', monospace;
        }
        .error-container {
            height: 100vh;
            display: flex;
            flex-direction: column;
            justify-content: center;
            align-items: center;
            background-color: #1a1a1a;
            color: #fff;
            text-align: center;
            padding: 20px;
        }
        .error-header {
            color: #dc3545;
            font-size: 3rem;
            margin-bottom: 1rem;
        }
        .error-title {
            color: #ffc107;
            font-size: 1.5rem;
            margin-bottom: 1rem;
        }
        .error-message {
            color: #aaa;
            margin-bottom: 2rem;
            max-width: 600px;
        }
        .error-actions {
            margin-top: 2rem;
        }
        .btn-retro {
            background-color: #333;
            border: 1px solid #555;
            color: #fff;
            padding: 10px 20px;
            text-decoration: none;
            margin: 0 10px;
            font-family: 'Courier New', monospace;
        }
        .btn-retro:hover {
            background-color: #555;
            color: #fff;
            text-decoration: none;
        }
    </style>
</head>
<body>
    <div class="error-container">
        <div class="error-header">⚠️</div>
        <div class="error-title">` + title + `</div>
        <div class="error-message">` + message + `</div>
        <div class="error-actions">
            <a href="/aichat" class="btn-retro">🔄 Retry Chat</a>
            <a href="/" class="btn-retro">🏠 Home</a>
        </div>
    </div>
</body>
</html>`

	c.Header("Content-Type", "text/html")
	c.String(http.StatusInternalServerError, errorHTML)
}
