// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/utils"
)

// This file should contain utility and helper functions from server.go:
//
// Functions to be moved from server.go:
// - func (s *WebServer) GetPort() int (line ~235)
//   Returns the web server listening port
// - func (s *WebServer) NNTPGetTCPPort() int (line ~239)
//   Returns the NNTP TCP port
// - func (s *WebServer) NNTPGetTLSPort() int (line ~247)
//   Returns the NNTP TLS port
// - func (s *WebServer) getBaseTemplateData(c *gin.Context, title string) TemplateData (line ~255)
//   Creates base template data used by all page handlers
// - func (s *WebServer) isAdminUser(user *models.User) bool (line ~279)
//   Checks if a user has admin privileges
// - func (s *WebServer) renderError(c *gin.Context, statusCode int, message string, errstring string) (line ~1280)
//   Renders error pages with consistent formatting
// - func (s *WebServer) renderTemplate(c *gin.Context, templateName string, data interface{}) (line ~1308)
//   Renders templates with base template data
// - func (s *WebServer) GetGroupCount() int (line ~1320)
//   Returns the total number of active newsgroups
// - func referencesAnyInThread(references string, threadMessageIDs map[string]bool) bool (line ~1329)
//   Utility function for checking thread references
// - utils.ParseReferences(references string) []string (shared utility package)
//   Utility function for parsing message references
//
// This file will contain utility functions, helper methods, and common functionality
// used across multiple page handlers.

// GetPort returns the listening port from the config
func (s *WebServer) GetPort() int {
	return s.Config.ListenPort
}

// NNTPGetTCPPort returns the listening TCP port from the config
func (s *WebServer) NNTPGetTCPPort() int {
	if s.NNTP == nil || s.NNTP.Config == nil {
		return -1
	}
	return s.NNTP.Config.NNTP.Port
}

// NNTPGetTLSPort returns the listening TLS port from the config
func (s *WebServer) NNTPGetTLSPort() int {
	if s.NNTP == nil || s.NNTP.Config == nil {
		return -1
	}
	return s.NNTP.Config.NNTP.TLSPort
}

// getBaseTemplateData creates a TemplateData struct with common information including user auth
func (s *WebServer) getBaseTemplateData(c *gin.Context, title string) TemplateData {
	// Check registration status (default to true if error)
	registrationEnabled := true
	if enabled, err := s.DB.IsRegistrationEnabled(); err == nil {
		registrationEnabled = enabled
	}

	// Load visible site news for home page display
	var siteNews []*models.SiteNews
	if visibleNews, err := s.DB.GetVisibleSiteNews(); err == nil {
		siteNews = visibleNews
	}

	// Load available sections for global navigation
	var availableSections []*models.Section
	if sections, err := s.DB.GetHeaderSections(); err == nil {
		availableSections = sections
	}

	// Load available AI models for conditional navigation
	var availableAIModels []*models.AIModel
	if aiModels, err := s.DB.GetActiveAIModels(); err == nil {
		availableAIModels = aiModels
	}

	data := TemplateData{
		Title:               template.HTML(title),
		CurrentTime:         time.Now().Format("2006-01-02 15:04:05"),
		Port:                s.GetPort(),
		NNTPtcpPort:         s.NNTPGetTCPPort(),
		NNTPtlsPort:         s.NNTPGetTLSPort(),
		AppVersion:          config.AppVersion,
		GroupCount:          0, // TODO: implement if needed
		RegistrationEnabled: registrationEnabled,
		SiteNews:            siteNews,
		AvailableSections:   availableSections,
		AvailableAIModels:   availableAIModels,
	}

	// Add user information if logged in
	if session := s.getWebSession(c); session != nil {
		data.User = session.User
		// Check if user is admin
		if userModel, err := s.DB.GetUserByID(session.UserID); err == nil {
			data.IsAdmin = s.isAdminUser(userModel)
		}
	}

	return data
}

// isAdminUser checks if a user has admin permissions (helper for base template)
func (s *WebServer) isAdminUser(user *models.User) bool {
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

// renderError renders an error page
func (s *WebServer) renderError(c *gin.Context, statusCode int, message string, errstring string) {
	errorData := struct {
		TemplateData
		Error      string
		StatusCode int
	}{
		TemplateData: s.getBaseTemplateData(c, "Error"),
		Error:        message,
		StatusCode:   statusCode,
	}
	log.Printf("[ERROR]:internal/web/server.go: Error %d: %s - %s", statusCode, message, errstring)

	// Load template individually to avoid engine setup issues
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/error.html"))
	c.Header("Content-Type", "text/html")
	c.Status(statusCode)
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", errorData)
	if err != nil {
		log.Printf("Error rendering error template: %v", err)
		c.String(statusCode, "Error: %s - %s", message, errstring)
	}
}

// renderTemplate renders a template with base template data
func (s *WebServer) renderTemplate(c *gin.Context, templateName string, data interface{}) {
	// Load template individually to avoid engine setup issues
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/"+templateName))
	c.Header("Content-Type", "text/html")
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		log.Printf("Error rendering template %s: %v", templateName, err)
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
	}
}

// GetGroupCount returns the total number of active newsgroups
func (s *WebServer) GetGroupCount() int {
	groups, err := s.DB.GetActiveNewsgroups()
	if err != nil {
		return 0
	}
	return len(groups)
}

// referencesAnyInThread checks if an article references any message ID that's already in the thread
func referencesAnyInThread(references string, threadMessageIDs map[string]bool) bool {
	if references == "" {
		return false
	}

	refIDs := utils.ParseReferences(references)
	for _, refID := range refIDs {
		if threadMessageIDs[refID] {
			return true
		}
	}

	return false
}
