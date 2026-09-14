package nntp

import (
	"fmt"
	"log"
	"strings"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// handlePost handles the POST command for article posting
func (c *ClientConnection) handlePost() error {
	// Check if processor is available
	if c.server.Processor == nil {
		return c.sendResponse(502, "Posting not supported on this server")
	}

	// Check if user is authenticated
	if !c.authenticated {
		return c.sendResponse(480, "Authentication required for posting")
	}

	// Check if user has posting permission
	if c.user != nil && !c.user.Posting {
		return c.sendResponse(502, "Posting not permitted for this user")
	}

	// Start article reception
	if err := c.sendResponse(340, "Send article to be posted. End with <CR-LF>.<CR-LF>"); err != nil {
		return err
	}

	// Read article data
	article, err := c.readArticleData()
	if err != nil {
		log.Printf("Failed to read POST article data: %v", err)
		return c.sendResponse(441, "Posting failed (unable to read article)")
	}

	if response, err := c.server.Processor.ProcessIncomingArticle(article); response != history.CasePass || err != nil {
		return c.sendResponse(441, "Posting failed (processing error)")
	}

	return c.sendResponse(240, "Article posted successfully")
}

// handleIHave handles the IHAVE command for article offering
func (c *ClientConnection) handleIHave(args []string) error {
	// Check if processor is available
	if c.server.Processor == nil {
		return c.sendResponse(502, "Transfer not supported on this server")
	}

	if len(args) != 1 {
		return c.sendResponse(501, "IHAVE command requires exactly one argument (message-ID)")
	}

	// Check authentication and permission before requesting the article
	if !c.authenticated {
		c.rateLimitOnError()
		return c.sendResponse(480, "Transfer permission denied (authentication required)")
	}
	if c.user != nil && !c.user.Posting {
		c.rateLimitOnError()
		return c.sendResponse(502, "Transfer not permitted for this user")
	}
	messageID := args[0]

	// Check if we want this article
	switch c.server.Processor.CheckMessageID(messageID) {
	case history.CasePass:
		// wanted
	case history.CaseDupes:
		return c.sendResponse(435, "Not wanted")
	case history.CaseRetry:
		// another connection is transferring it right now
		return c.sendResponse(436, "Retry later")
	default: // history.CaseError
		log.Printf("Error checking article history for %s", messageID)
		c.rateLimitOnError()
		return c.sendResponse(436, "Retry later (history error)")
	}

	// Request the article
	if err := c.sendResponse(335, "Send me"); err != nil {
		return err
	}

	// Read article data
	article, err := c.readArticleData()
	if err != nil {
		log.Printf("Failed to read IHAVE article data: %v", err)
		return c.sendResponse(436, "Bad") //Invalid
	}
	if !reconcileMessageID(article, messageID) {
		log.Printf("IHAVE message-id mismatch: command '%s' article '%s'", messageID, article.MessageID)
		return c.sendResponse(437, "Rejected (Message-ID mismatch)")
	}

	response, err := c.server.Processor.ProcessIncomingArticle(article)
	switch {
	case err == nil && response == history.CasePass:
		return c.sendResponse(235, "Article transferred successfully")
	case response == history.CaseDupes:
		return c.sendResponse(437, "Rejected (duplicate)")
	default:
		return c.sendResponse(436, "Transfer failed (processing error)")
	}
}

// reconcileMessageID sets the article's Message-ID from the command argument if the article has none.
// Returns false if both exist and differ.
func reconcileMessageID(article *models.Article, cmdMessageID string) bool {
	if article.MessageID == "" {
		article.MessageID = cmdMessageID
		return true
	}
	return article.MessageID == cmdMessageID
}

// handleTakeThis handles the TAKETHIS command for streaming article transfer
func (c *ClientConnection) handleTakeThis(args []string) error {
	// Read article data immediately (streaming mode):
	// the client sends the article without waiting, so it must be consumed
	// before replying to keep the stream in sync (also for the early error replies).
	headLines, bodyLines, err := c.readArticleLines()

	// Check if processor is available
	if c.server.Processor == nil {
		return c.sendResponse(502, "Streaming not supported on this server")
	}
	if len(args) != 1 {
		return c.sendResponse(501, "TAKETHIS command requires exactly one argument (message-ID)")
	}
	messageID := args[0]

	if err != nil {
		log.Printf("Failed to read TAKETHIS article data: %v", err)
		return c.sendResponse(439, fmt.Sprintf("%s Transfer failed (unable to read article)", messageID))
	}

	// Check authentication and permission (exactly one response per TAKETHIS)
	if !c.authenticated {
		c.rateLimitOnError()
		return c.sendResponse(480, "Transfer permission denied (authentication required)")
	}
	if c.user != nil && !c.user.Posting {
		c.rateLimitOnError()
		return c.sendResponse(502, "Transfer not permitted for this user")
	}

	// Check if we want this article
	switch c.server.Processor.CheckMessageID(messageID) {
	case history.CasePass:
		// wanted
	case history.CaseDupes:
		return c.sendResponse(439, fmt.Sprintf("%s Not wanted", messageID))
	case history.CaseRetry:
		return c.sendResponse(439, fmt.Sprintf("%s Retry later", messageID))
	default: // history.CaseError
		log.Printf("Error checking article history for %s", messageID)
		return c.sendResponse(439, fmt.Sprintf("%s Retry later (history error)", messageID))
	}

	article, err := c.buildIncomingArticle(headLines, bodyLines)
	if err != nil {
		log.Printf("Failed to parse TAKETHIS article data: %v", err)
		return c.sendResponse(439, fmt.Sprintf("%s Transfer failed (invalid article)", messageID))
	}
	if !reconcileMessageID(article, messageID) {
		log.Printf("TAKETHIS message-id mismatch: command '%s' article '%s'", messageID, article.MessageID)
		return c.sendResponse(439, fmt.Sprintf("%s Transfer failed (Message-ID mismatch)", messageID))
	}

	if response, err := c.server.Processor.ProcessIncomingArticle(article); response != history.CasePass || err != nil {
		return c.sendResponse(439, fmt.Sprintf("%s Transfer failed", messageID))
	}

	return c.sendResponse(239, fmt.Sprintf("%s Article transferred successfully", messageID))
}

/*
// ArticleData represents parsed article data with extracted metadata
type ArticleData struct {
	Head       []string            // Full header as a single string
	Body       []string            // Article as individual lines
	Newsgroups []string            // All newsgroups from header (for cross-posting)
	Headers    map[string][]string // Parsed headers for spam checking
}
*/

// readArticleData reads article data from the client until terminator (.<CR><LF>)
// and parses it into a models.Article with newsgroup pointers set.
func (c *ClientConnection) readArticleData() (*models.Article, error) {
	headLines, bodyLines, err := c.readArticleLines()
	if err != nil {
		return nil, err
	}
	return c.buildIncomingArticle(headLines, bodyLines)
}

// buildIncomingArticle parses raw head/body lines and maps the newsgroups to pointers
func (c *ClientConnection) buildIncomingArticle(headLines, bodyLines []string) (*models.Article, error) {
	article, newsgroups, err := parseIncomingArticleLines(headLines, bodyLines)
	if err != nil {
		return nil, err
	}
	article.ArticleNums = make(map[*string]int64, len(newsgroups))
	for _, ng := range newsgroups {
		newsgroupPtr := c.server.DB.Batch.GetNewsgroupPointer(ng)
		article.NewsgroupsPtr = append(article.NewsgroupsPtr, newsgroupPtr)
		article.ArticleNums[newsgroupPtr] = -1
	}
	return article, nil
}

// readArticleLines reads raw article lines from the client until terminator (.<CR><LF>),
// undoing dot-stuffing and splitting at the first empty line into head and body lines.
func (c *ClientConnection) readArticleLines() (headLines []string, bodyLines []string, err error) {
	inHeaders := true
	lineCount, headCount := 0, 0
	maxLines, maxHead := 16384, 1024 // HARDCODED limit for article size

	for {
		if headCount > maxHead || lineCount > maxLines {
			c.textConn.Close()
			return nil, nil, fmt.Errorf("article too large (limit: %d lines)", maxLines)
		}

		line, err := c.textConn.ReadLine()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read article line: %w", err)
		}

		// Check for end marker
		if line == "." {
			break
		}

		// Handle dot-stuffing (lines starting with .. become .)
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}

		if inHeaders {
			if line == "" {
				// Empty line marks end of headers, start of body
				inHeaders = false
			} else {
				headLines = append(headLines, line)
				headCount++
			}
		} else {
			bodyLines = append(bodyLines, line)
		}
		lineCount++
	}
	return headLines, bodyLines, nil
}

// parseIncomingArticleLines builds a models.Article from already dot-unstuffed head and body lines
// (without the separating empty line) and returns the newsgroups from the Newsgroups header.
// It does not access the server or database.
func parseIncomingArticleLines(headLines, bodyLines []string) (*models.Article, []string, error) {
	messageID := extractHeaderValue(headLines, "message-id")

	lines := make([]string, 0, len(headLines)+1+len(bodyLines))
	lines = append(lines, headLines...)
	lines = append(lines, "")
	lines = append(lines, bodyLines...)

	article, err := ParseLegacyArticleLines(messageID, lines, false)
	if err != nil {
		return nil, nil, err
	}
	// Preserve original lines for peering, also when the body is empty
	article.NNTPhead = headLines
	article.NNTPbody = bodyLines

	var newsgroups []string
	for _, group := range strings.Split(common.GetHeaderFirst(article.Headers, "newsgroups"), ",") {
		group = strings.TrimSpace(group)
		if group != "" {
			newsgroups = append(newsgroups, group)
		}
	}
	if len(newsgroups) == 0 {
		return nil, nil, fmt.Errorf("no Newsgroups header found in article")
	}
	return article, newsgroups, nil
}

// extractHeaderValue returns the trimmed value of the first header named name (case-insensitive),
// unfolding continuation lines.
func extractHeaderValue(headLines []string, name string) string {
	for i, line := range headLines {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		colonPos := strings.Index(line, ":")
		if colonPos == -1 || !strings.EqualFold(strings.TrimSpace(line[:colonPos]), name) {
			continue
		}
		value := strings.TrimSpace(line[colonPos+1:])
		for _, cont := range headLines[i+1:] {
			if cont == "" || (cont[0] != ' ' && cont[0] != '\t') {
				break
			}
			if part := strings.TrimSpace(cont); part != "" {
				if value != "" {
					value += " "
				}
				value += part
			}
		}
		return value
	}
	return ""
}
