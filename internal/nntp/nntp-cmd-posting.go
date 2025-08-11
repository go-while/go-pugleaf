package nntp

import (
	"fmt"
	"log"
	"strings"

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
	//messageID := args[0]
	msgIdItem := history.MsgIdCache.GetORCreate(args[0])
	if msgIdItem == nil {
		c.rateLimitOnError()
		return c.sendResponse(500, "Error MsgId Cache")
	}

	// Check if we already have this article
	response, err := c.server.Processor.Lookup(msgIdItem)
	if err != nil {
		log.Printf("Error looking up message ID %s in history: %v", msgIdItem.MessageId, err)
		c.rateLimitOnError()
		return c.sendResponse(436, "Retry later (history error)")
	}
	switch response {
	case history.CaseError:
		log.Printf("Error checking article history for %s", msgIdItem.MessageId)
		return c.sendResponse(436, "Retry later (history error)")
	case history.CaseRetry:
		// Retry case, we can skip processing
		log.Printf("Article %s is in retry state, skipping transfer", msgIdItem.MessageId)
		return c.sendResponse(436, "Retry later")
	case history.CaseDupes:
		// If we already have the article, we can skip processing
		log.Printf("Article %s already exists in history, skipping transfer", msgIdItem.MessageId)
		c.sendResponse(435, "Not wanted")
	case history.CasePass:
		// pass
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

	if response, err := c.server.Processor.ProcessIncomingArticle(article); response != history.CasePass || err != nil {
		return c.sendResponse(436, "Transfer failed (processing error)")
	}

	return c.sendResponse(235, "Article transferred successfully")
}

// handleTakeThis handles the TAKETHIS command for streaming article transfer
func (c *ClientConnection) handleTakeThis(args []string) error {
	// Check if processor is available
	if c.server.Processor == nil {
		return c.sendResponse(502, "Streaming not supported on this server")
	}

	if len(args) != 1 {
		return c.sendResponse(501, "TAKETHIS command requires exactly one argument (message-ID)")
	}
	//messageID := args[0]
	msgIdItem := history.MsgIdCache.GetORCreate(args[0])
	if msgIdItem == nil {
		c.rateLimitOnError()
		return c.sendResponse(500, "Error MsgId Cache")
	}

	// Read article data immediately (streaming mode)
	article, err := c.readArticleData()
	if err != nil {
		log.Printf("Failed to read TAKETHIS article data: %v", err)
		return c.sendResponse(439, fmt.Sprintf("%s Transfer failed (unable to read article)", msgIdItem.MessageId))
	}

	// Check if we already have this article
	response, err := c.server.Processor.Lookup(msgIdItem)
	if err != nil {
		log.Printf("Error looking up message ID %s in history: %v", msgIdItem.MessageId, err)
		c.rateLimitOnError()
		return c.sendResponse(439, fmt.Sprintf("%s Retry later (history error)", msgIdItem.MessageId))
	}
	switch response {
	case history.CaseError:
		log.Printf("Error checking article history for %s", msgIdItem.MessageId)
		return c.sendResponse(439, fmt.Sprintf("%s Err", msgIdItem.MessageId))
	case history.CaseRetry:
		// Retry case, we can skip processing
		log.Printf("Article %s is in retry state, skipping transfer", msgIdItem.MessageId)
		return c.sendResponse(439, fmt.Sprintf("%s Ret", msgIdItem.MessageId))
	case history.CaseDupes:
		// If we already have the article, we can skip processing
		log.Printf("Article %s already exists in history, skipping transfer", msgIdItem.MessageId)
		c.sendResponse(439, fmt.Sprintf("%s Not", msgIdItem.MessageId))
	case history.CasePass:
		// pass
	}

	if response, err := c.server.Processor.ProcessIncomingArticle(article); response != history.CasePass || err != nil {
		return c.sendResponse(439, fmt.Sprintf("%s Transfer failed", msgIdItem.MessageId))
	}

	return c.sendResponse(239, fmt.Sprintf("%s Article transferred successfully", msgIdItem.MessageId))
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
// and parses headers on-the-fly to extract newsgroup information and enable spam checking
func (c *ClientConnection) readArticleData() (*models.Article, error) {
	var head []string
	var body []string
	var newsgroups []string
	var currentHeader string
	headers := make(map[string][]string)
	inHeaders := true
	lineCount, headCount := 0, 0
	maxLines, maxHead := 16384, 1024 // HARDCODED limit for article size
	var rxb int                      // Received bytes

	for {
		if headCount > maxHead || lineCount > maxLines {
			c.textConn.Close()
			return nil, fmt.Errorf("article too large (limit: %d lines)", maxLines)
		}

		line, err := c.textConn.ReadLine()
		if err != nil {
			return nil, fmt.Errorf("failed to read article line: %w", err)
		}
		rxb += len(line)

		// Check for end marker
		if line == "." {
			break
		}

		// Handle dot-stuffing (lines starting with .. become .)
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}

		// Parse headers on-the-fly until we hit the empty line separator
		if inHeaders {
			if line == "" {
				// Empty line marks end of headers, start of body
				inHeaders = false
			} else {
				// Check for header continuation (line starts with space or tab)
				if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
					// This is a continuation of the previous header
					// Append continuation to existing header value
					if currentHeader != "" {
						if _, exists := headers[currentHeader]; exists {
							headers[currentHeader] = append(headers[currentHeader], line)
						}
					}
				} else {
					// Parse new header
					colonPos := strings.Index(line, ":")
					if colonPos != -1 {
						headerName := strings.TrimSpace(line[:colonPos])
						headerValue := strings.TrimSpace(line[colonPos+1:])
						if currentHeader == "" || headerValue == "" {
							log.Printf("Invalid header format: %s", line)
							continue
						}
						currentHeader = strings.ToLower(headerName)
						if currentHeader == "xref" {
							// skip bad or Xref header
							currentHeader = ""
							continue
						}
						headers[currentHeader] = append(headers[currentHeader], line)

						// Extract ALL newsgroups if this is the Newsgroups header
						if currentHeader == "newsgroups" && len(newsgroups) == 0 {
							// Split by comma and trim each newsgroup
							groupList := strings.Split(headerValue, ",")
							for _, group := range groupList {
								group = strings.TrimSpace(group)
								if group != "" {
									newsgroups = append(newsgroups, group)
								}
							}
						}
					}
				}
				head = append(head, line)
				headCount++
			}
		} else {
			body = append(body, line)
		}
		lineCount++
	}

	if len(newsgroups) == 0 {
		return nil, fmt.Errorf("no Newsgroups header found in article")
	}

	// Convert to models.Article
	article := &models.Article{
		Headers:  headers,
		BodyText: strings.Join(body, "\n"),
		Lines:    len(body),
		NNTPhead: head, // Preserve original header order for peering
		NNTPbody: body, // Preserve original body lines for peering
	}
	for _, ng := range newsgroups {
		newsgroupPtr := c.server.DB.Batch.GetNewsgroupPointer(ng)
		article.NewsgroupsPtr = append(article.NewsgroupsPtr, newsgroupPtr)
		article.ArticleNums[newsgroupPtr] = -1
	}
	article.Bytes = rxb
	// Extract individual header fields if they exist
	if msgID := getHeaderFirst(headers, "message-id"); msgID != "" {
		article.MessageID = msgID
	}
	if subject := getHeaderFirst(headers, "subject"); subject != "" {
		article.Subject = subject
	}
	if from := getHeaderFirst(headers, "from"); from != "" {
		article.FromHeader = from
	}
	if references := getHeaderFirst(headers, "references"); references != "" {
		article.References = references
	}
	if path := getHeaderFirst(headers, "path"); path != "" {
		article.Path = path
	}
	if dateStr := getHeaderFirst(headers, "date"); dateStr != "" {
		article.DateString = dateStr
		// TODO: Parse DateSent from dateStr if needed
	}

	return article, nil
}

// getHeaderFirst helper function to get first header value
func getHeaderFirst(headers map[string][]string, key string) string {
	if vals, exists := headers[key]; exists && len(vals) > 0 {
		return vals[0]
	}
	return ""
}
