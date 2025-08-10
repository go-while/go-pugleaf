package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const OllamaHostname = "ollama-proxy.local:21434"
const ProxyURL = "http://" + OllamaHostname + "/models"

// ProxyModelResponse represents the response from ollama-proxy /models endpoint
type ProxyModelResponse struct {
	Models []ProxyModel `json:"models"`
}

type ProxyModel struct {
	Name   string   `json:"name"`
	Post   string   `json:"post"`
	Caps   []string `json:"caps"`
	Size   int64    `json:"size"`
	Family string   `json:"family"`
}

// AI Model Management Functions

// adminCreateAIModel handles AI model creation
func (s *WebServer) adminCreateAIModel(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	// Get form data
	postKey := strings.TrimSpace(c.PostForm("post_key"))
	ollamaModelName := strings.TrimSpace(c.PostForm("ollama_model_name"))
	displayName := strings.TrimSpace(c.PostForm("display_name"))
	description := strings.TrimSpace(c.PostForm("description"))
	sortOrderStr := strings.TrimSpace(c.PostForm("sort_order"))
	isActiveStr := c.PostForm("is_active")
	isDefaultStr := c.PostForm("is_default")

	// Validate input
	if postKey == "" || ollamaModelName == "" || displayName == "" {
		session.SetError("Post key, Ollama model name, and display name are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	// Parse sort order
	sortOrder := 0
	if sortOrderStr != "" {
		sortOrder, err = strconv.Atoi(sortOrderStr)
		if err != nil || sortOrder < 0 {
			session.SetError("Invalid sort order")
			c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
			return
		}
	}

	// Parse checkboxes
	isActive := isActiveStr == "on"
	isDefault := isDefaultStr == "on"

	// Check if post key already exists
	existingModel, err := s.DB.GetAIModelByPostKey(postKey)
	if err == nil && existingModel != nil {
		session.SetError("AI model with this post key already exists")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	// Create AI model
	_, err = s.DB.CreateAIModel(postKey, ollamaModelName, displayName, description, isActive, isDefault, sortOrder)
	if err != nil {
		log.Printf("Error creating AI model: %v", err)
		session.SetError("Failed to create AI model")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	session.SetSuccess("AI model created successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
}

// adminUpdateAIModel handles AI model updates
func (s *WebServer) adminUpdateAIModel(c *gin.Context) {
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

	// Get model ID
	modelIDStr := c.PostForm("model_id")
	if modelIDStr == "" {
		session.SetError("Missing model ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}
	modelID, err := strconv.ParseInt(modelIDStr, 10, 64)
	if err != nil {
		session.SetError("Invalid model ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	// Get form data
	postKey := strings.TrimSpace(c.PostForm("post_key"))
	ollamaModelName := strings.TrimSpace(c.PostForm("ollama_model_name"))
	displayName := strings.TrimSpace(c.PostForm("display_name"))
	description := strings.TrimSpace(c.PostForm("description"))
	sortOrderStr := strings.TrimSpace(c.PostForm("sort_order"))
	isActiveStr := c.PostForm("is_active")
	isDefaultStr := c.PostForm("is_default")
	log.Printf("DEBUG: Updating AI model ID %d with post_key=%s, ollama_model_name=%s, display_name=%s, description=%s, sort_order=%s, is_active=%s, is_default=%s",
		int(modelID), postKey, ollamaModelName, displayName, description, sortOrderStr, isActiveStr, isDefaultStr)
	// Validate input
	if postKey == "" || ollamaModelName == "" || displayName == "" {
		session.SetError("Post key, Ollama model name, and display name are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	// Parse sort order
	sortOrder := 0
	if sortOrderStr != "" {
		sortOrder, err = strconv.Atoi(sortOrderStr)
		if err != nil || sortOrder < 0 {
			session.SetError("Invalid sort order")
			c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
			return
		}
	}

	// Parse checkboxes
	isActive := isActiveStr == "on"
	isDefault := isDefaultStr == "on"

	// Update AI model
	err = s.DB.UpdateAIModel(int(modelID), ollamaModelName, displayName, description, isActive, isDefault, sortOrder)
	if err != nil {
		log.Printf("Error updating AI model: %v", err)
		session.SetError("Failed to update AI model")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	session.SetSuccess("AI model updated successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
}

// adminDeleteAIModel handles AI model deletion
func (s *WebServer) adminDeleteAIModel(c *gin.Context) {
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

	// Get model ID
	modelIDStr := c.PostForm("model_id")
	if modelIDStr == "" {
		session.SetError("Missing model ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}
	modelID, err := strconv.ParseInt(modelIDStr, 10, 64)
	if err != nil {
		session.SetError("Invalid model ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	// Delete AI model
	err = s.DB.DeleteAIModel(int(modelID))
	if err != nil {
		log.Printf("Error deleting AI model: %v", err)
		session.SetError("Failed to delete AI model")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	session.SetSuccess("AI model deleted successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
}

// adminSyncOllamaModels handles the sync new models request
func (s *WebServer) adminSyncOllamaModels(c *gin.Context) {
	log.Printf("DEBUG: adminSyncOllamaModels handler called")
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

	// Fetch models from ollama-proxy
	resp, err := http.Get(ProxyURL)
	if err != nil {
		log.Printf("Failed to fetch models from proxy: %v", err)
		session.SetError("Failed to connect to ollama-proxy")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Proxy returned status %d", resp.StatusCode)
		session.SetError("Ollama-proxy returned an error")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	var proxyResponse ProxyModelResponse
	if err := json.NewDecoder(resp.Body).Decode(&proxyResponse); err != nil {
		log.Printf("Failed to decode proxy response: %v", err)
		session.SetError("Failed to parse proxy response")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	// Get existing models from database to check for duplicates
	existingModels, err := s.DB.GetAllAIModels()
	if err != nil {
		log.Printf("Failed to get existing models: %v", err)
		session.SetError("Database error")
		c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
		return
	}

	// Create a map of existing post_keys for quick lookup
	existingPostKeys := make(map[string]bool)
	for _, model := range existingModels {
		existingPostKeys[model.PostKey] = true
	}

	// Track sync results
	var addedCount int
	var skippedCount int
	var errors []string

	// Process each model from proxy
	for _, proxyModel := range proxyResponse.Models {
		// Generate postkey from the real model name
		postKey := generatePostKey(proxyModel.Name)

		// Skip if model already exists (use generated post_key as unique identifier)
		if existingPostKeys[postKey] {
			skippedCount++
			log.Printf("Skipping existing model: %s (post_key: %s)", proxyModel.Name, postKey)
			continue
		}

		ollamaModelName := proxyModel.Name

		// Generate display name and description
		displayName := generateDisplayName(proxyModel.Name)
		description := generateDescription(proxyModel)

		// Create new AIModel via database
		_, err := s.DB.CreateAIModel(postKey, ollamaModelName, displayName, description, true, false, 100)
		if err != nil {
			errorMsg := fmt.Sprintf("Failed to add model %s: %v", proxyModel.Name, err)
			errors = append(errors, errorMsg)
			log.Print(errorMsg)
			continue
		}

		addedCount++
		log.Printf("Added new model: %s (post_key: %s)", proxyModel.Name, postKey)
	}

	// Prepare success/error message
	var message string
	if len(errors) > 0 {
		message = fmt.Sprintf("Sync completed with errors: %d added, %d skipped, %d errors", addedCount, skippedCount, len(errors))
	} else {
		message = fmt.Sprintf("Sync completed successfully: %d new models added, %d skipped", addedCount, skippedCount)
	}

	session.SetSuccess(message)
	c.Redirect(http.StatusSeeOther, "/admin?tab=aimodels")
}

// generatePostKey creates a user-friendly post key from the Ollama model name
func generatePostKey(ollamaModelName string) string {
	// Convert model name to postkey format
	// e.g., "gemma3:12b" -> "gemma3_12b"
	// e.g., "deepseek-coder:7b-instruct-v1.5-q6_K" -> "deepseek_coder_7b_instruct_v1_5_q6_K"
	postKey := strings.ReplaceAll(ollamaModelName, "/", "_")
	postKey = strings.ReplaceAll(postKey, ":", "_")
	postKey = strings.ReplaceAll(postKey, "-", "_")
	postKey = strings.ReplaceAll(postKey, ".", "_")
	return postKey
}

// generateDisplayName creates a user-friendly display name from the model name
func generateDisplayName(modelName string) string {
	// Remove common suffixes and clean up
	name := strings.ReplaceAll(modelName, ":", " ")
	name = strings.ReplaceAll(name, "-", " ")
	name = strings.ReplaceAll(name, "_", " ")

	// Capitalize words
	words := strings.Fields(name)
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
		}
	}

	return strings.Join(words, " ")
}

// generateDescription creates a description based on model properties
func generateDescription(proxyModel ProxyModel) string {
	desc := fmt.Sprintf("Ollama model: %s", proxyModel.Name)

	if proxyModel.Family != "" {
		desc += fmt.Sprintf(" (%s family)", proxyModel.Family)
	}

	if len(proxyModel.Caps) > 0 {
		desc += fmt.Sprintf(" - Capabilities: %s", strings.Join(proxyModel.Caps, ", "))
	}

	if proxyModel.Size > 0 {
		desc += fmt.Sprintf(" - Size: %.1f GB", float64(proxyModel.Size)/(1024*1024*1024))
	}

	return desc
}
