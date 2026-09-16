package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

const maxChatInputLineLength = 1024

// maxChatRequestBytes bounds the JSON body of a chat send. The message itself may be at most
// maxChatInputLineLength; the rest leaves room for the model field and JSON escaping.
const maxChatRequestBytes = 64 << 10

// ollamaProxyURL is the endpoint every chat request is proxied to. It is a var so tests can
// point it at a fake proxy; nothing but aichatSend reads it.
var ollamaProxyURL = "http://ollama-proxy.local:21434/proxy"

// chatBusyMessage is returned when the user already has a send waiting for the proxy.
const chatBusyMessage = "A reply is still being generated"

// chatHTTPClient calls the Ollama proxy; the timeout bounds a hung proxy (the request context
// additionally ends when the client goes away).
var chatHTTPClient = &http.Client{Timeout: 90 * time.Second}

const (
	maxChatProxyResponseBytes = 1 << 20          // proxy responses larger than this are rejected
	chatHistoryMaxIdle        = 2 * time.Hour    // histories unused for longer are swept
	chatCacheMaxEntries       = 10000            // per map; the oldest entries are evicted beyond it
	chatSweepInterval         = 10 * time.Minute // how often runChatCacheSweeper runs
)

// Rate limiting for AI chat
var (
	chatRateLimiter = make(map[int64]time.Time) // user ID -> last request time
	rateLimiterMux  sync.Mutex
	chatCooldown    = 5 * time.Second
)

// chatEntry is the chat history of one user with one model.
// All fields are guarded by chatCacheMux.
type chatEntry struct {
	msgs      []ChatMessage
	lastUsed  time.Time
	inFlight  bool      // a send of this user and model is waiting for the proxy
	claimedAt time.Time // when inFlight was set; sweepChatCaches releases claims stuck past it
	gen       uint64    // bumped by every clear; a reply of an older generation is not stored
}

// chatClaimStuckAfter bounds how long an entry may stay claimed. A send cannot take longer than
// the proxy timeout plus a little request handling, so a claim older than this leaked (a panic
// between claiming and the deferred release) and the sweeper takes it back. Without it such an
// entry would answer 429 for the rest of the process and never be swept.
var chatClaimStuckAfter = chatHTTPClient.Timeout + 5*time.Minute

// Chat history cache - in-memory storage per user and model (key: chatHistoryKey)
var (
	chatHistoryCache = make(map[string]*chatEntry)
	chatCacheMux     sync.RWMutex
	maxHistoryLength = 200 // Keep last N messages per user and model
)

// chatHistoryKey returns the history cache key of a user and model.
func chatHistoryKey(userID int64, modelPostKey string) string {
	return strconv.FormatInt(userID, 10) + "_" + modelPostKey
}

// getChatHistory returns a copy of the history (never nil) and marks the entry as used.
func getChatHistory(key string, now time.Time) []ChatMessage {
	chatCacheMux.Lock()
	defer chatCacheMux.Unlock()
	entry, ok := chatHistoryCache[key]
	if !ok {
		return []ChatMessage{}
	}
	entry.lastUsed = now
	if len(entry.msgs) == 0 {
		// A cleared entry keeps msgs nil, and slices.Clone(nil) is nil: answer with an empty
		// slice, so the JSON of every history route stays [] instead of null.
		return []ChatMessage{}
	}
	return slices.Clone(entry.msgs)
}

// clearChatEntry empties one history and bumps its generation, so a send that is still waiting
// for the proxy does not store its exchange afterwards. The caller holds chatCacheMux.
// The (now empty) entry stays in the map; sweepChatCaches removes it once it is idle.
func clearChatEntry(entry *chatEntry) {
	entry.msgs = nil
	entry.gen++
}

// getAllChatHistoryCounts gets chat history counts of a user for all models
func getAllChatHistoryCounts(userID int64, models []*models.AIModel) map[string]int {
	chatCacheMux.RLock()
	defer chatCacheMux.RUnlock()
	counts := make(map[string]int, len(models))
	for _, model := range models {
		if entry, ok := chatHistoryCache[chatHistoryKey(userID, model.PostKey)]; ok {
			counts[model.PostKey] = len(entry.msgs)
		} else {
			counts[model.PostKey] = 0
		}
	}
	return counts
}

// sweepChatCaches removes chat histories idle for more than chatHistoryMaxIdle and rate-limiter
// entries older than 10*chatCooldown, then caps both maps at chatCacheMaxEntries by evicting the
// oldest entries. Entries with a send in flight are never removed, so the sender still finds its
// entry when the proxy answers. It takes chatCacheMux and rateLimiterMux itself.
func sweepChatCaches(now time.Time) {
	chatCacheMux.Lock()
	for key, entry := range chatHistoryCache {
		if entry.inFlight {
			if now.Sub(entry.claimedAt) <= chatClaimStuckAfter {
				continue
			}
			// No send can still be running: take the claim back instead of refusing this user
			// and model forever, and let the entry expire by the normal idle rule below.
			log.Printf("[WEB]: chat cache: releasing stuck in-flight claim %q after %s", key, now.Sub(entry.claimedAt).Truncate(time.Second))
			entry.inFlight = false
		}
		if now.Sub(entry.lastUsed) > chatHistoryMaxIdle {
			delete(chatHistoryCache, key)
		}
	}
	if over := len(chatHistoryCache) - chatCacheMaxEntries; over > 0 {
		keys := make([]string, 0, len(chatHistoryCache))
		for key, entry := range chatHistoryCache {
			if entry.inFlight {
				continue
			}
			keys = append(keys, key)
		}
		if over > len(keys) {
			over = len(keys)
		}
		slices.SortFunc(keys, func(a, b string) int {
			return chatHistoryCache[a].lastUsed.Compare(chatHistoryCache[b].lastUsed)
		})
		for _, key := range keys[:over] {
			delete(chatHistoryCache, key)
		}
	}
	chatCacheMux.Unlock()

	rateLimiterMux.Lock()
	for uid, last := range chatRateLimiter {
		if now.Sub(last) > 10*chatCooldown {
			delete(chatRateLimiter, uid)
		}
	}
	if over := len(chatRateLimiter) - chatCacheMaxEntries; over > 0 {
		uids := make([]int64, 0, len(chatRateLimiter))
		for uid := range chatRateLimiter {
			uids = append(uids, uid)
		}
		slices.SortFunc(uids, func(a, b int64) int {
			return chatRateLimiter[a].Compare(chatRateLimiter[b])
		})
		for _, uid := range uids[:over] {
			delete(chatRateLimiter, uid)
		}
	}
	rateLimiterMux.Unlock()
}

// runChatCacheSweeper runs sweepChatCaches periodically until Shutdown.
func (s *WebServer) runChatCacheSweeper() {
	ticker := time.NewTicker(chatSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			sweepChatCaches(now)
		}
	}
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

	// Load existing chat history for the default model (keyed by user, never by the session ID)
	existingHistory := getChatHistory(chatHistoryKey(session.UserID, defaultModel.PostKey), time.Now())

	// Get chat counts for all models
	chatCounts := getAllChatHistoryCounts(session.UserID, availableModels)

	data := AIChatPageData{
		TemplateData:    s.getBaseTemplateData(c, "AI Chat"),
		ChatHistory:     existingHistory,
		Error:           c.Query("error"),
		AvailableModels: availableModels,
		DefaultModel:    defaultModel,
		ChatCounts:      chatCounts,
		MaxInputLength:  maxChatInputLineLength,
	}

	s.renderTemplateSet(c, http.StatusOK, "chat", nil, "base_chat.html", data, "base_chat.html", "aichat.html")
}

// aichatSend handles chat message POSTs and proxies to Ollama
func (s *WebServer) aichatSend(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Rate limiting: check and record under one lock (before processing the request)
	now := time.Now()
	rateLimiterMux.Lock()
	lastRequestTime, ok := chatRateLimiter[session.UserID]
	if ok && now.Sub(lastRequestTime) < chatCooldown {
		rateLimiterMux.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests. Please wait before sending another message."})
		return
	}
	chatRateLimiter[session.UserID] = now
	rateLimiterMux.Unlock()

	// Accept new message and model selection from frontend (a legacy sessionToken field is ignored)
	var req struct {
		Message string `json:"message"`
		Model   string `json:"model"` // Selected model's post key
	}
	// Bound the body before decoding it: the message length is only checked after ShouldBindJSON,
	// so without this an authenticated client could make the decoder allocate without limit.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatRequestBytes)
	if err := c.ShouldBindJSON(&req); err != nil || req.Message == "" {
		if errors.As(err, new(*http.MaxBytesError)) {
			log.Printf("AI Chat request body too large (user %d, limit %d bytes)", session.UserID, maxChatRequestBytes)
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Request too large"})
			return
		}
		log.Printf("AI Chat Invalid request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: message required"})
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
		log.Printf("AI Chat invalid model: %q, err: %v", modelPostKey, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or inactive AI model selected"})
		return
	}

	// Validate message length (N character limit)
	if len(req.Message) > maxChatInputLineLength {
		log.Printf("AI Chat message too long: %d chars", len(req.Message))
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message too long..."})
		return
	}

	// Claim the history of this user and model: only one send at a time may wait for the proxy,
	// so two sends can no longer overwrite each other's exchange. gen is remembered here and
	// re-checked before storing, so a clear during the send is not undone.
	modelCacheKey := chatHistoryKey(session.UserID, modelPostKey)
	chatCacheMux.Lock()
	entry, ok := chatHistoryCache[modelCacheKey]
	if !ok {
		entry = &chatEntry{}
		chatHistoryCache[modelCacheKey] = entry
	}
	if entry.inFlight {
		chatCacheMux.Unlock()
		log.Printf("AI Chat busy: user=%d model=%q", session.UserID, modelPostKey)
		c.JSON(http.StatusTooManyRequests, gin.H{"error": chatBusyMessage})
		return
	}
	entry.inFlight = true
	entry.claimedAt = now
	entry.lastUsed = now
	gen := entry.gen
	history := slices.Clone(entry.msgs)
	chatCacheMux.Unlock()
	// Runs on every return path below, so a failed send never leaves the entry claimed.
	defer func() {
		chatCacheMux.Lock()
		if cur, ok := chatHistoryCache[modelCacheKey]; ok && cur == entry {
			cur.inFlight = false
		}
		chatCacheMux.Unlock()
	}()

	// Add new user message to the copy sent to the proxy
	history = append(history, ChatMessage{Role: "user", Content: req.Message})

	// Never log message contents: lengths and the model only
	log.Printf("AI Chat request: user=%d model='%s' msg_len=%d history_len=%d", session.UserID, selectedModel.DisplayName, len(req.Message), len(history))

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

	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, ollamaProxyURL, bytes.NewReader(proxyBody))
	if err != nil {
		log.Printf("AI Chat Proxy request build error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build proxy request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := chatHTTPClient.Do(httpReq)
	if err != nil {
		log.Printf("AI Chat Proxy request error: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Ollama proxy error"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		log.Printf("AI Chat Proxy returned status %d", resp.StatusCode)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Ollama proxy error"})
		return
	}

	// Parse the proxy response (simple format: {"reply": "..."})
	var proxyResp struct {
		Reply string `json:"reply"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxChatProxyResponseBytes)).Decode(&proxyResp); err != nil {
		log.Printf("AI Chat Proxy decode error: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to decode proxy response"})
		return
	}

	if proxyResp.Reply == "" {
		log.Printf("AI Chat Proxy no reply from AI")
		c.JSON(http.StatusBadGateway, gin.H{"error": "No reply from AI"})
		return
	}

	// Append the exchange to the current history, unless it was cleared while the proxy worked:
	// then the reply is still returned to the client, but no longer stored.
	chatCacheMux.Lock()
	if cur, ok := chatHistoryCache[modelCacheKey]; ok && cur == entry && cur.gen == gen {
		cur.msgs = append(cur.msgs,
			ChatMessage{Role: "user", Content: req.Message},
			ChatMessage{Role: "assistant", Content: proxyResp.Reply})
		// Trim history if it gets too long (keep last N messages)
		if len(cur.msgs) > maxHistoryLength {
			cur.msgs = cur.msgs[len(cur.msgs)-maxHistoryLength:]
		}
		cur.lastUsed = time.Now()
	}
	chatCacheMux.Unlock()

	log.Printf("AI Chat got reply: user=%d model='%s' reply_len=%d", session.UserID, selectedModel.DisplayName, len(proxyResp.Reply))
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

// aichatLoadHistory loads the chat history of the logged-in user for a specific model
func (s *WebServer) aichatLoadHistory(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Get model from URL parameter
	modelPostKey := c.Param("model")
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
	history := getChatHistory(chatHistoryKey(session.UserID, modelPostKey), time.Now())

	c.JSON(http.StatusOK, gin.H{
		"history": history,
		"model":   selectedModel.DisplayName,
		"count":   len(history),
	})
}

// aichatClearHistory clears the chat history of the logged-in user for a specific model or all models
func (s *WebServer) aichatClearHistory(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	// Get model from URL parameter (can be "all" for clearing all)
	modelParam := c.Param("model")
	if modelParam == "" && c.FullPath() == "/aichat/clear/all" {
		// The static /aichat/clear/all route has no :model parameter
		modelParam = "all"
	}

	chatCacheMux.Lock()
	defer chatCacheMux.Unlock()

	if modelParam == "all" {
		// Clear all model histories for this user
		prefix := strconv.FormatInt(session.UserID, 10) + "_"
		for key, entry := range chatHistoryCache {
			if strings.HasPrefix(key, prefix) {
				clearChatEntry(entry)
			}
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "All chats cleared"})
	} else if modelParam != "" {
		// Clear specific model history
		if entry, ok := chatHistoryCache[chatHistoryKey(session.UserID, modelParam)]; ok {
			clearChatEntry(entry)
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Model chat cleared"})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model parameter required in URL"})
	}
}

// aichatGetCounts returns chat message counts of the logged-in user for all models
func (s *WebServer) aichatGetCounts(c *gin.Context) {
	session := s.getWebSession(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	availableModels, err := s.DB.GetActiveAIModels()
	if err != nil {
		log.Printf("Error loading AI models: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load AI models"})
		return
	}

	// Get chat counts for all models
	chatCounts := getAllChatHistoryCounts(session.UserID, availableModels)

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
