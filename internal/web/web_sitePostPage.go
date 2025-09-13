// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// PostPageData represents data for posting page
type PostPageData struct {
	TemplateData
	PrefilledNewsgroup    string
	Error                 string
	Success               string
	WebPostMaxArticleSize string
}

// PostQueueChannel is the channel for articles posted from web interface
// TODO: This will be processed later by a background worker
var PostQueueChannel = make(chan *models.Article, 100)

// sitePostPage handles the "/SitePost" route to display the posting form
func (s *WebServer) sitePostPage(c *gin.Context) {
	// Check if user is authenticated
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusFound, "/login?redirect=/SitePost")
		return
	}

	// Get prefilled newsgroup from POST form data (from "New Thread" button)
	prefilledNewsgroup := c.PostForm("newsgroup")

	// Get max article size from database config
	maxArticleSizeStr, err := s.DB.GetConfigValue("WebPostMaxArticleSize")
	if err != nil {
		log.Printf("Warning: Failed to get WebPostMaxArticleSize config in sitePostPage, using default: %v", err)
		maxArticleSizeStr = "32768" // fallback to default
	}

	// Create template data with no errors (this is just displaying the form)
	data := PostPageData{
		TemplateData:          s.getBaseTemplateData(c, "New Thread"),
		PrefilledNewsgroup:    prefilledNewsgroup,
		Error:                 "", // No errors when just displaying the form
		Success:               "", // No success message when just displaying the form
		WebPostMaxArticleSize: maxArticleSizeStr,
	}

	// Load and render the posting form template
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/sitepost.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

// sitePostSubmit handles the POST submission of new articles from web interface
func (s *WebServer) sitePostSubmit(c *gin.Context) {
	// Check if user is authenticated
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	user := session.User

	// Get form data
	subject := strings.TrimSpace(c.PostForm("subject"))
	body := strings.TrimSpace(c.PostForm("body"))
	newsgroupsStr := strings.TrimSpace(c.PostForm("newsgroups"))

	// Get max article size from database config
	maxArticleSizeStr, err := s.DB.GetConfigValue("WebPostMaxArticleSize")
	if err != nil {
		log.Printf("Warning: Failed to get WebPostMaxArticleSize config, using default: %v", err)
		maxArticleSizeStr = "32768" // fallback to default
	}

	maxArticleSize := 32768 // default fallback
	if parsed, err := strconv.Atoi(maxArticleSizeStr); err == nil && parsed > 0 {
		maxArticleSize = parsed
	}

	// Validate required fields
	var errors []string
	if subject == "" {
		errors = append(errors, "Subject is required")
	}
	if len(subject) > 72 {
		errors = append(errors, "Subject must be less than 72 characters")
	}
	if body == "" {
		errors = append(errors, "Message body is required")
	}
	if len(body) > maxArticleSize {
		errors = append(errors, fmt.Sprintf("Message body must be less than %d bytes", maxArticleSize))
	}
	if newsgroupsStr == "" {
		errors = append(errors, "At least one newsgroup is required")
	}

	// Parse newsgroups (space or comma separated)
	var newsgroups []string
	if newsgroupsStr != "" {
		// Replace commas with spaces and split
		newsgroupsStr = strings.ReplaceAll(newsgroupsStr, ",", " ")
		parts := strings.FieldsSeq(newsgroupsStr)
		for part := range parts {
			if part != "" {
				newsgroups = append(newsgroups, part)
			}
		}
	}

	if len(newsgroups) == 0 {
		errors = append(errors, "No valid newsgroups specified")
	}

	// Validate that all newsgroups exist and are active
	var validNewsgroups []string
	for _, newsgroup := range newsgroups {
		ng, err := s.DB.GetNewsgroupByName(newsgroup)
		if err != nil {
			errors = append(errors, fmt.Sprintf("Newsgroup '%s' does not exist", newsgroup))
			continue
		}
		if !ng.Active {
			errors = append(errors, fmt.Sprintf("Newsgroup '%s' is not active for posting", newsgroup))
			continue
		}
		validNewsgroups = append(validNewsgroups, newsgroup)
	}

	// Use only valid newsgroups for further processing
	newsgroups = validNewsgroups

	// Check if we have any valid newsgroups after validation
	if len(newsgroups) == 0 && len(errors) == 0 {
		errors = append(errors, "No valid active newsgroups found")
	}

	// Check if there are validation errors
	if len(errors) > 0 {
		data := PostPageData{
			TemplateData:          s.getBaseTemplateData(c, "New Thread"),
			PrefilledNewsgroup:    newsgroupsStr,
			Error:                 strings.Join(errors, "; "),
			WebPostMaxArticleSize: strconv.Itoa(maxArticleSize),
		}

		tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/sitepost.html"))
		c.Header("Content-Type", "text/html")
		err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
		if err != nil {
			s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		}
		return
	}

	// Create article similar to threading.go
	article := &models.Article{
		MessageID:   generateMessageID(), // We'll need to implement this
		Subject:     subject,
		FromHeader:  fmt.Sprintf("%s <%s>", user.DisplayName, user.Email),
		DateString:  time.Now().Format(time.RFC1123Z),
		DateSent:    time.Now(),
		BodyText:    body,
		ImportedAt:  time.Now(),
		IsThrRoot:   true, // New posts from web are always thread roots
		IsReply:     false,
		Lines:       strings.Count(body, "\n") + 1,
		Bytes:       len(body),
		Path:        fmt.Sprintf("pugleaf.local!%s", user.Username), // Local path
		ArticleNums: make(map[*string]int64),
		RefSlice:    []string{}, // No references for new threads
	}

	// Set newsgroups
	article.Headers = make(map[string][]string)
	article.Headers["newsgroups"] = []string{strings.Join(newsgroups, ",")}
	article.Headers["subject"] = []string{subject}
	article.Headers["from"] = []string{article.FromHeader}
	article.Headers["date"] = []string{article.DateString}
	article.Headers["message-id"] = []string{article.MessageID}

	log.Printf("Web posting: User %s posting to newsgroups: %v, subject: %s", user.Username, newsgroups, subject)

	// Put article into the queue channel
	// TODO: This channel will be processed later by a background worker
	select {
	case PostQueueChannel <- article:
		log.Printf("Article queued successfully for user %s, message-id: %s", user.Username, article.MessageID)

		// Record in post_queue table for tracking
		for _, newsgroup := range newsgroups {
			// Get newsgroup ID
			newsgroupModel, err := s.DB.GetNewsgroupByName(newsgroup)
			if err != nil {
				log.Printf("Warning: Failed to get newsgroup %s for post queue recording: %v", newsgroup, err)
				continue
			}

			// TODO: Insert into post_queue table
			// This would require implementing the post_queue database operations
			// For now, we'll just log it
			log.Printf("TODO: Record in post_queue table - user_id: %d, newsgroup_id: %d, message_id: %s",
				user.ID, newsgroupModel.ID, article.MessageID)
		}

	default:
		log.Printf("Warning: Post queue channel is full, article may be lost")
		data := PostPageData{
			TemplateData:          s.getBaseTemplateData(c, "New Thread"),
			PrefilledNewsgroup:    newsgroupsStr,
			Error:                 "Server is busy, please try again later",
			WebPostMaxArticleSize: strconv.Itoa(maxArticleSize),
		}

		tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/sitepost.html"))
		c.Header("Content-Type", "text/html")
		err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
		if err != nil {
			s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		}
		return
	}

	// Show success page
	data := PostPageData{
		TemplateData:          s.getBaseTemplateData(c, "New Thread"),
		Success:               "Your message has been queued for posting. It will be processed shortly.",
		WebPostMaxArticleSize: strconv.Itoa(maxArticleSize),
	}

	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/sitepost.html"))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}

// generateMessageID creates a unique message ID for web-posted articles
func generateMessageID() string {
	random, err := generateRandomHex(12)
	if err != nil {
		log.Printf("Error in generateMessageID: generating random hex: %v", err)
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), processor.LocalNNTPHostname)
	}
	return fmt.Sprintf("<%s@%s>", random, processor.LocalNNTPHostname)
}
