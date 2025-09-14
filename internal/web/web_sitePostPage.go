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
	"github.com/go-while/go-pugleaf/internal/utils"
)

// PostPageData represents data for posting page
type PostPageData struct {
	TemplateData
	PrefilledNewsgroup    string
	PrefilledSubject      string
	PrefilledBody         string
	Error                 string
	Success               string
	WebPostMaxArticleSize string
	IsReply               bool
	ReplyTo               string
	MessageID             string
	ReplyToArticleNum     string
	ReplyToMessageID      string
	ReplySubject          string
}

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

	// Check if this is a reply
	replyToArticleNum := c.PostForm("reply_to")
	replyToMessageID := c.PostForm("message_id")
	isReply := replyToArticleNum != "" && replyToMessageID != ""

	var err error
	article := &models.Article{}
	if isReply {
		// Get the original article to extract subject and body for reply
		if articleNum, err := strconv.ParseInt(replyToArticleNum, 10, 64); err == nil {
			// Get group database connection
			if groupDBs, err := s.DB.GetGroupDBs(prefilledNewsgroup); err == nil {
				defer groupDBs.Return(s.DB)
				if reply_article, err := s.DB.GetArticleByNum(groupDBs, articleNum); err == nil {
					// Handle subject with "Re: " prefix
					if !strings.HasPrefix(strings.ToLower(reply_article.Subject), "re:") {
						article.Subject = "Re: " + models.ConvertToUTF8(reply_article.Subject)
					}
					// Quote the original message body
					if reply_article.BodyText != "" {
						// Clean the body text first to remove HTML entities
						cleanBodyText := models.ConvertToUTF8(reply_article.BodyText)
						lines := strings.Split(cleanBodyText, "\n")
						var quotedLines []string

						// Add header line with properly decoded FromHeader for NNTP posting
						cleanFromHeader := models.ConvertToUTF8(reply_article.FromHeader)
						quotedLines = append(quotedLines, fmt.Sprintf("On %s, %s wrote:",
							reply_article.DateString, cleanFromHeader))
						quotedLines = append(quotedLines, "")

						// Quote each line with "> "
						for _, line := range lines {
							quotedLines = append(quotedLines, "> "+line)
						}

						// Add empty lines for user's response
						quotedLines = append(quotedLines, "", "")

						article.BodyText = strings.Join(quotedLines, "\n")
					}
				} else {
					log.Printf("Warning: Failed to get article for reply: %v", err)
				}
			} else {
				log.Printf("Warning: Failed to get group database for reply: %v", err)
			}
		}
	}

	// Get max article size from database config
	maxArticleSizeStr, err := s.DB.GetConfigValue("WebPostMaxArticleSize")
	if err != nil {
		log.Printf("Warning: Failed to get WebPostMaxArticleSize config in sitePostPage, using default: %v", err)
		maxArticleSizeStr = "32768" // fallback to default
	}

	// Create template data with no errors (this is just displaying the form)
	pageTitle := "New Thread"
	if isReply {
		pageTitle = "Reply to"
	}
	var prefilledBodyStr string
	if isReply && len(article.BodyText) > 0 {
		// Use raw text for textarea - Go's html/template will automatically escape it
		// This gives us clean text in the form while still being XSS-safe
		prefilledBodyStr = article.BodyText
	}
	var prefilledSubjectStr string
	if isReply && len(article.Subject) > 0 {
		// Use raw text for input field - Go's html/template will automatically escape it
		prefilledSubjectStr = article.Subject
	}
	data := PostPageData{
		TemplateData:          s.getBaseTemplateData(c, pageTitle),
		PrefilledNewsgroup:    prefilledNewsgroup,
		PrefilledSubject:      prefilledSubjectStr,
		PrefilledBody:         prefilledBodyStr,
		Error:                 "", // No errors when just displaying the form
		Success:               "", // No success message when just displaying the form
		WebPostMaxArticleSize: maxArticleSizeStr,
		IsReply:               isReply,
		ReplyToArticleNum:     replyToArticleNum,
		ReplyToMessageID:      replyToMessageID,
		ReplySubject:          prefilledSubjectStr,
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

	// Check if this is a reply
	replyTo := strings.TrimSpace(c.PostForm("reply_to"))
	messageID := strings.TrimSpace(c.PostForm("message_id"))
	isReply := replyTo != "" && messageID != ""

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

	if len(errors) == 0 {
		// Validate that all newsgroups exist and are active
		if len(newsgroups) > processor.MaxCrossPosts {
			errors = append(errors, fmt.Sprintf("You can post to a maximum of %d newsgroups at once", processor.MaxCrossPosts))
		} else {
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
		}
		// Check if we have any valid newsgroups after validation
		if len(newsgroups) == 0 && len(errors) == 0 {
			errors = append(errors, "No valid active newsgroups found")
		}
	}

	// Check if there are validation errors
	if len(errors) > 0 {
		data := PostPageData{
			TemplateData:          s.getBaseTemplateData(c, "Posting failed"),
			PrefilledNewsgroup:    newsgroupsStr,
			PrefilledSubject:      subject,
			PrefilledBody:         body,
			Error:                 strings.Join(errors, "; "),
			WebPostMaxArticleSize: strconv.Itoa(maxArticleSize),
			IsReply:               isReply,
			ReplyToArticleNum:     replyTo,
			ReplyToMessageID:      messageID,
		}

		tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/sitepost.html"))
		c.Header("Content-Type", "text/html")
		err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
		if err != nil {
			s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		}
		return
	}
	var headers []string
	headers = append(headers, "Newsgroups: "+strings.Join(newsgroups, ","))
	// Create article similar to threading.go
	article := &models.Article{
		MessageID:   generateMessageID(),
		Subject:     subject,
		HeadersJSON: strings.Join(headers, "\n"),
		FromHeader:  fmt.Sprintf("%s <%s>", user.DisplayName, user.DisplayName+"@"+processor.LocalNNTPHostname),
		DateString:  time.Now().Format(time.RFC1123Z),
		BodyText:    body,
		IsThrRoot:   !isReply, // Only new threads are thread roots
		IsReply:     isReply,
		Lines:       strings.Count(body, "\n") + 1,
		Bytes:       len(body),
		Path:        ".POSTED!not-for-mail",
		ArticleNums: make(map[*string]int64),
		RefSlice:    []string{},
	}
	article.Headers["newsgroups"] = []string{strings.Join(newsgroups, ",")}
	article.Headers["subject"] = []string{subject}
	article.Headers["from"] = []string{article.FromHeader}
	article.Headers["date"] = []string{article.DateString}
	article.Headers["message-id"] = []string{article.MessageID}
	// If this is a reply, set up References header
	if isReply {
		// Try to find the original article to get its References
		var originalRefs string
		for _, newsgroup := range newsgroups {
			groupDB, err := s.DB.GetGroupDBs(newsgroup)
			if err != nil {
				log.Printf("Warning: Failed to get group DB for %s: %v", newsgroup, err)
				continue
			}
			defer groupDB.DB.Close()

			originalArticle, err := s.DB.GetArticleByMessageID(groupDB, messageID)
			if err != nil {
				log.Printf("Warning: Failed to find original article %s in %s: %v", messageID, newsgroup, err)
				continue
			}
			if originalArticle.References != "" {
				originalRefs = originalArticle.References
			}
			break
		}

		// Build new References header: original References + original Message-ID
		article.References = originalRefs + " " + messageID
		article.RefSlice = utils.ParseReferences(article.References)
		article.Headers["references"] = []string{article.References}

	}
	log.Printf("Web posting: User %s posting to newsgroups: %v, subject: %s", user.Username, newsgroups, subject)

	// Put article into the queue channel
	// This channel will be processed by the PostQueueWorker in the processor package
	select {
	case models.PostQueueChannel <- article:
		log.Printf("Article queued successfully for user %s, message-id: %s", user.Username, article.MessageID)

	default:
		log.Printf("Warning: Post queue channel is full, article is lost.")
		data := PostPageData{
			TemplateData:          s.getBaseTemplateData(c, "New Thread"),
			PrefilledNewsgroup:    newsgroupsStr,
			PrefilledSubject:      subject,
			PrefilledBody:         body,
			Error:                 "Server is busy, please try again later",
			WebPostMaxArticleSize: strconv.Itoa(maxArticleSize),
			IsReply:               isReply,
			ReplyToArticleNum:     replyTo,
			ReplyToMessageID:      messageID,
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
	successMsg := "Your message has been queued for posting. It will be processed shortly."
	pageTitle := "New Thread"
	if isReply {
		successMsg = "Your reply has been queued for posting. It will be processed shortly."
		pageTitle = "Reply to Thread"
	}

	data := PostPageData{
		TemplateData:          s.getBaseTemplateData(c, pageTitle),
		Success:               successMsg,
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
	random, err := generateRandomHex(8)
	if err != nil {
		log.Printf("Error in generateMessageID: generating random hex: %v", err)
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), processor.LocalNNTPHostname)
	}
	return fmt.Sprintf("<%d$%s@%s>", time.Now().UnixNano(), random, processor.LocalNNTPHostname)
}
