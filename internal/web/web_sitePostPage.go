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
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
	"github.com/go-while/go-pugleaf/internal/utils"
)

var WebPostingBackOff = 42 * time.Second

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
	user, err := s.DB.GetUserByID(session.User.ID)
	if err != nil {
		log.Printf("Failed to get user by ID: %v", err)
		c.Redirect(http.StatusFound, "/login")
		return
	}
	if user.NoPosting > 0 || user.Disabled > 0 {
		session.SetError("Your account is not permitted to post")
		c.Redirect(http.StatusFound, "/profile")
		return
	}
	if user.LastPostUnix > time.Now().Add(-WebPostingBackOff).Unix() {
		session.SetError(fmt.Sprintf("You can only post once every %d seconds", int(WebPostingBackOff.Seconds())))
		c.Redirect(http.StatusFound, "/profile")
		return
	}
	// Get prefilled newsgroup from POST form data (from "New Thread" button)
	prefilledNewsgroup := c.PostForm("newsgroup")

	// Check if this is a reply
	replyToArticleNum := c.PostForm("reply_to")
	replyToMessageID := c.PostForm("message_id")
	isReply := replyToArticleNum != "" && replyToMessageID != ""

	article := &models.Article{}
	if isReply {
		// Get the original article to extract subject and body for reply
		if articleNum, err := strconv.ParseInt(replyToArticleNum, 10, 64); err == nil {
			// Get group database connection
			if groupDBs, err := s.DB.GetGroupDBs(prefilledNewsgroup); err == nil {
				defer groupDBs.Return()
				if reply_article, err := s.DB.GetArticleByNum(groupDBs, articleNum); err == nil {
					// Handle subject with "Re: " prefix
					if !strings.HasPrefix(strings.ToLower(reply_article.Subject), "re:") {
						article.Subject = "Re: " + models.ConvertToUTF8(reply_article.Subject)
					} else {
						article.Subject = models.ConvertToUTF8(reply_article.Subject)
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
	maxArticleSizeStr, err := s.DB.GetConfigValue(config.CFG_KEY_WEBPOSTSIZE)
	if err != nil {
		log.Printf("Warning: Failed to get WebPostMaxArticleSize config in sitePostPage, using default: %v", err)
		maxArticleSizeStr = "32768" // fallback to default
	}

	// Create template data with no errors (this is just displaying the form)
	pageTitle := "New Thread"
	if isReply {
		pageTitle = fmt.Sprintf("Reply to: [%s] %s)", replyToArticleNum, replyToMessageID)
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
	user, err := s.DB.GetUserByID(session.User.ID)
	if err != nil {
		log.Printf("Failed to get user by ID: %v", err)
		c.Redirect(http.StatusFound, "/login")
		return
	}
	// Get form data
	subject := strings.TrimSpace(c.PostForm("subject"))
	body := strings.TrimSpace(c.PostForm("body"))
	newsgroupsStr := strings.TrimSpace(c.PostForm("newsgroups"))
	//log.Printf("User %s is posting to newsgroups: %v, subject: %s", session.User.Username, newsgroupsStr, subject)

	// Check if this is a reply
	replyTo := strings.TrimSpace(c.PostForm("reply_to"))
	messageID := strings.TrimSpace(c.PostForm("message_id"))
	isReply := replyTo != "" && messageID != ""
	var errors []string

	abuseMail, err := s.DB.GetConfigValue(config.CFG_KEY_ABUSEMAIL)
	if err != nil {
		log.Printf("Warning: Failed to get AbuseMail config: %v", err)
	}
	if abuseMail == "" || abuseMail == "abuse@invalid.invalid" {
		errors = append(errors, "System Abuse email is not configured. Please contact the administrator.")
	}

	// Get max article size from database config
	maxArticleSizeStr, err := s.DB.GetConfigValue(config.CFG_KEY_WEBPOSTSIZE)
	if err != nil {
		log.Printf("Warning: Failed to get WebPostMaxArticleSize config: %v", err)
	}

	maxArticleSize := -1
	if parsed, err := strconv.Atoi(maxArticleSizeStr); err == nil && parsed > 0 {
		if parsed >= 1000 && parsed <= 16*1024*1024 {
			maxArticleSize = parsed
		}
	}

	if maxArticleSize <= 0 {
		errors = append(errors, "Server configuration error: invalid max WebPostMaxArticleSize")
		log.Printf("Warning: WebPostMaxArticleSize config value is out of valid range (1000-16777216)")
	}

	if len(errors) == 0 {
		// Validate required fields
		if subject == "" {
			errors = append(errors, "Subject is required")
		}
		if len(subject) > 255 {
			errors = append(errors, "Subject limited to 255 characters")
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
		if user.NoPosting > 0 || user.Disabled > 0 {
			errors = append(errors, "Your account is not permitted to post")
		}
		if user.LastPostUnix > time.Now().Add(-WebPostingBackOff).Unix() {
			errors = append(errors, fmt.Sprintf("You can only post once every %d seconds", int(WebPostingBackOff.Seconds())))
		}
	}

	// Parse newsgroups (space or comma separated)
	var newsgroups []string
	if len(errors) == 0 && newsgroupsStr != "" {
		// Replace commas with spaces and split
		newsgroupsStr = strings.ReplaceAll(newsgroupsStr, ",", " ")
		parts := strings.FieldsSeq(newsgroupsStr)
		for part := range parts {
			if part != "" {
				if processor.IsValidGroupName(part) {
					newsgroups = append(newsgroups, part)
				} else {
					errors = append(errors, "Invalid newsgroup name: "+part)
					break
				}
			}
		}
	}

	if len(newsgroups) == 0 {
		errors = append(errors, "No valid newsgroups specified")
	}
	if len(newsgroups) > processor.MaxCrossPosts {
		errors = append(errors, fmt.Sprintf("You can post to a maximum of %d newsgroups at once", processor.MaxCrossPosts))
	}

	if len(errors) == 0 {
		// Validate that all newsgroups exist and are active
		var validNewsgroups []string
		for _, newsgroup := range newsgroups {
			ng, err := s.DB.GetNewsgroupByName(newsgroup)
			if err != nil {
				errors = append(errors, fmt.Sprintf("Newsgroup '%s' does not exist.", newsgroup))
				continue
			}
			if !ng.Active {
				errors = append(errors, fmt.Sprintf("Newsgroup '%s' is not active.", newsgroup))
				continue
			}
			if ng.Status == "m" || ng.Status == "mod" || ng.Status == "moderated" {
				errors = append(errors, fmt.Sprintf("Newsgroup '%s' is moderated.", newsgroup))
				continue
			}
			if ng.Status == "n" || ng.Status == "x" {
				errors = append(errors, fmt.Sprintf("Newsgroup '%s' does not allow posting.", newsgroup))
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
	}
	now := time.Now().Unix()
	nonce := strconv.FormatInt(now, 10)
	hashedUser, err := s.DB.ComputeHashedUsername(session.User.Username, nonce)
	if err != nil {
		errors = append(errors, "Failed to compute hashed username")
	}
	if len(errors) == 0 {
		if err := s.DB.UpdateUserPostCount(session.User.ID, now); err != nil {
			log.Printf("Failed to update user post count: %v", err)
			errors = append(errors, "Failed to update post count")
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
	displayName := strings.TrimSpace(session.User.DisplayName)
	if displayName != "" && !strings.Contains(displayName, "<") && !strings.Contains(displayName, ">") {
		displayName = fmt.Sprintf("%s <noreply@pugleaf.invalid>", session.User.DisplayName)
	}
	if displayName == "" {
		// Fallback if display name is empty
		displayName = fmt.Sprintf("Lorem Ipsum <oops@%s>", processor.LocalNNTPHostname)
	}
	var headers []string
	linesCount := strings.Count(body, "\n") + 1
	bytesCount := len(body)
	headers = append(headers, "MIME-Version: 1.0")
	headers = append(headers, "Content-Type: text/plain; charset=\"UTF-8\"")
	headers = append(headers, "Content-Transfer-Encoding: 8bit")
	headers = append(headers, "Newsgroups: "+strings.Join(newsgroups, ","))
	// Injection-Info / X-Trace header for tracking
	headers = append(headers, "X-pugleaf-Trace: "+processor.LocalNNTPHostname+";")
	headers = append(headers, "\tnonce=\""+nonce+"\"; mail-complaints-to=\""+abuseMail+"\";")
	headers = append(headers, "\tposting-account=\""+hashedUser+"\";")
	headers = append(headers, "From: "+displayName)
	headers = append(headers, "Lines: "+strconv.Itoa(linesCount))
	headers = append(headers, "Bytes: "+strconv.Itoa(bytesCount))

	// Create article similar to threading.go
	article := &models.Article{
		MessageID:   generateMessageID(),
		Subject:     subject,
		HeadersJSON: strings.Join(headers, "\n"),
		FromHeader:  displayName,
		DateString:  time.Now().Format(time.RFC1123Z),
		BodyText:    body,
		IsThrRoot:   !isReply, // Only new threads are thread roots
		IsReply:     isReply,
		Lines:       linesCount,
		Bytes:       bytesCount,
		Path:        ".POSTED!not-for-mail",
		ArticleNums: make(map[*string]int64),
		RefSlice:    []string{},
		Headers:     make(map[string][]string, 6),
	}
	article.Headers["newsgroups"] = []string{strings.Join(newsgroups, ",")}
	// If this is a reply, set up References header
	if isReply {
		log.Printf("Setting up References header for reply to message ID: %s", messageID)
		// Try to find the original article to get its References
		var originalRefs string
		for _, newsgroup := range newsgroups {
			groupDBs, err := s.DB.GetGroupDBs(newsgroup)
			if err != nil {
				log.Printf("Warning: Failed to get group DB for %s: %v", newsgroup, err)
				continue
			}
			defer groupDBs.Return()

			originalArticle, err := s.DB.GetArticleByMessageID(groupDBs, messageID)
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
		article.References = strings.TrimSpace(originalRefs + " " + messageID)
		article.RefSlice = utils.ParseReferences(article.References)
		article.Headers["references"] = []string{article.References}
		article.Headers["lines"] = []string{strconv.Itoa(article.Lines)}
		article.Headers["bytes"] = []string{strconv.Itoa(article.Bytes)}
	}
	//log.Printf("Web posting: User '%s': article='%#v'", session.User.Username, article)

	// Put article into the queue channel
	// This channel will be processed by the PostQueueWorker in the processor package
	select {
	case models.PostQueueChannel <- article:
		log.Printf("Article queued successfully for user '%s', message-id: '%s'", session.User.Username, article.MessageID)

	default:
		log.Printf("Warning: Post queue channel is full, article is lost.")
		data := PostPageData{
			TemplateData:          s.getBaseTemplateData(c, "Posting failed"),
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
		pageTitle = "Reply to"
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
