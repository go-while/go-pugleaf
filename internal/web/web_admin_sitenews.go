package web

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// Site News Management Functions

// adminCreateSiteNews handles site news creation
func (s *WebServer) adminCreateSiteNews(c *gin.Context) {
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

	// Parse form data
	subject := c.PostForm("subject")
	content := c.PostForm("content")
	datePublishedStr := c.PostForm("date_published")
	isVisibleStr := c.PostForm("is_visible")

	// Validate required fields
	if subject == "" || content == "" || datePublishedStr == "" {
		session.SetError("Subject, content, and date are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	// Parse date
	datePublished, err := time.Parse("2006-01-02T15:04", datePublishedStr)
	if err != nil {
		session.SetError("Invalid date format")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	// Parse visibility (checkbox is present if checked, absent if not)
	isVisible := isVisibleStr == "on"

	// Create news entry
	news := &models.SiteNews{
		Subject:       subject,
		Content:       content,
		DatePublished: datePublished,
		IsVisible:     isVisible,
	}

	err = s.DB.CreateSiteNews(news)
	if err != nil {
		log.Printf("Failed to create site news: %v", err)
		session.SetError("Failed to create news entry")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	session.SetSuccess("Site news entry created successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
}

// adminUpdateSiteNews handles site news updates
func (s *WebServer) adminUpdateSiteNews(c *gin.Context) {
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

	// Parse news ID
	newsIDStr := c.PostForm("news_id")
	newsID, err := strconv.Atoi(newsIDStr)
	if err != nil {
		session.SetError("Invalid news ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	// Get existing news entry
	existingNews, err := s.DB.GetSiteNewsByID(newsID)
	if err != nil {
		log.Printf("Failed to get site news by ID %d: %v", newsID, err)
		session.SetError("News entry not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	// Parse form data
	subject := c.PostForm("subject")
	content := c.PostForm("content")
	datePublishedStr := c.PostForm("date_published")
	isVisibleStr := c.PostForm("is_visible")

	// Validate required fields
	if subject == "" || content == "" || datePublishedStr == "" {
		session.SetError("Subject, content, and date are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	// Parse date
	datePublished, err := time.Parse("2006-01-02T15:04", datePublishedStr)
	if err != nil {
		session.SetError("Invalid date format")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	// Parse visibility
	isVisible := isVisibleStr == "on"

	// Update news entry
	existingNews.Subject = subject
	existingNews.Content = content
	existingNews.DatePublished = datePublished
	existingNews.IsVisible = isVisible

	err = s.DB.UpdateSiteNews(existingNews)
	if err != nil {
		log.Printf("Failed to update site news ID %d: %v", newsID, err)
		session.SetError("Failed to update news entry")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	session.SetSuccess("Site news entry updated successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
}

// adminDeleteSiteNews handles site news deletion
func (s *WebServer) adminDeleteSiteNews(c *gin.Context) {
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

	// Parse news ID
	newsIDStr := c.PostForm("news_id")
	newsID, err := strconv.Atoi(newsIDStr)
	if err != nil {
		session.SetError("Invalid news ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	// Delete news entry
	err = s.DB.DeleteSiteNews(newsID)
	if err != nil {
		log.Printf("Failed to delete site news ID %d: %v", newsID, err)
		session.SetError("Failed to delete news entry")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	session.SetSuccess("Site news entry deleted successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
}

// adminToggleSiteNewsVisibility handles toggling news visibility
func (s *WebServer) adminToggleSiteNewsVisibility(c *gin.Context) {
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

	// Parse news ID
	newsIDStr := c.PostForm("news_id")
	newsID, err := strconv.Atoi(newsIDStr)
	if err != nil {
		session.SetError("Invalid news ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	// Toggle visibility
	err = s.DB.ToggleSiteNewsVisibility(newsID)
	if err != nil {
		log.Printf("Failed to toggle visibility for site news ID %d: %v", newsID, err)
		session.SetError("Failed to toggle news visibility")
		c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
		return
	}

	session.SetSuccess("News visibility toggled successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=sitenews")
}
