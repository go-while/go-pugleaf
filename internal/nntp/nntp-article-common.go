package nntp

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ArticleRetrievalType defines what content to send
type ArticleRetrievalType int

const (
	RetrievalArticle ArticleRetrievalType = iota // Head + Body
	RetrievalHead                                // Head only
	RetrievalBody                                // Body only
	RetrievalStat                                // Status only (no content)
)

// handleArticle handles ARTICLE command
func (c *ClientConnection) handleArticle(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalArticle)
}

// handleHead handles HEAD command
func (c *ClientConnection) handleHead(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalHead)
}

// handleBody handles BODY command
func (c *ClientConnection) handleBody(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalBody)
}

// handleStat handles STAT command
func (c *ClientConnection) handleStat(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalStat)
}

// retrieveArticleCommon handles the common logic for ARTICLE, HEAD, BODY, and STAT commands
func (c *ClientConnection) retrieveArticleCommon(args []string, retrievalType ArticleRetrievalType) error {
	time.Sleep(time.Second / 5) // TODO hardcoded ratelimit

	// Get article data using common logic
	article := c.getArticleData(args, retrievalType)
	if article == nil {
		// 430 error handled in getArticleData
		return nil
	}
	// Update current article if we have a current group
	if c.currentGroup != "" {
		c.currentArticle = article.DBArtNum
		/* disabled
		task := c.server.DB.Batch.GetOrCreateTasksMapKey(c.currentGroup)
		if task != nil && result.MsgIdItem != nil {
			result.MsgIdItem.Mux.Lock()
			result.MsgIdItem.GroupName = task.Newsgroup
			result.MsgIdItem.ArtNum = result.ArticleNum
			result.MsgIdItem.Mux.Unlock()
		}
		*/
	}

	// Send appropriate response based on retrieval type
	switch retrievalType {
	case RetrievalArticle:

		return c.sendArticleContent(article)
	case RetrievalHead:
		return c.sendHeadContent(article)
	case RetrievalBody:
		return c.sendBodyContent(article)
	case RetrievalStat:
		return c.sendStatContent(article)
	default:
		return c.sendResponse(500, "Internal error: unknown retrieval type")
	}
}

// getArticleData handles the common article lookup logic
func (c *ClientConnection) getArticleData(args []string, retrievalType ArticleRetrievalType) (article *models.Article) {
	var wantArticleNum int64
	var msgIdItem *history.MessageIdItem
	// Parse argument: can be article number or message-id
	if len(args) == 0 {
		c.rateLimitOnError()
		c.sendResponse(501, "No article specified")
		return
	}

	if strings.HasPrefix(args[0], "<") && strings.HasSuffix(args[0], ">") {
		// Message-ID format
		msgIdItem = history.MsgIdCache.GetORCreate(args[0])
		if msgIdItem == nil {
			c.rateLimitOnError()
			c.sendResponse(500, "Error MsgId Cache")
			return
		}
		if c.server.local430.Check(msgIdItem) {
			c.rateLimitOnError()
			c.sendResponse(430, "Cache says no!")
			return
		}

	} else {
		if c.currentGroup == "" {
			c.rateLimitOnError()
			c.sendResponse(412, "No newsgroup selected")
			return
		}
		// Article number format
		awantArticleNum, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			c.rateLimitOnError()
			c.sendResponse(501, "Invalid article number")
			return
		}
		wantArticleNum = awantArticleNum
	}

	// Get article
	if msgIdItem != nil && wantArticleNum == 0 {
		// Handle message-ID lookup
		response, _, err := c.server.Processor.Lookup(msgIdItem, false)
		if err != nil {
			c.server.local430.Add(msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF0")
			return
		}
		found := false
		switch response {

		case history.CaseError:
			c.server.local430.Add(msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF1")
			return

		case history.CasePass:
			// Not found in history
			log.Printf("MsgIdItem not found in history: '%#v'", msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF2")
			return

		case history.CaseDupes:
			// Found in history- should have newsgroupIDs
			msgIdItem.Mux.RLock()
			found = len(msgIdItem.NewsgroupIDs) > 0
			msgIdItem.Mux.RUnlock()
		}

		if !found {
			log.Printf("MsgIdItem not found in cache: %#v", msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF3")
			return
		}

		// Get group database for the specific group from storage token
		article, err = c.server.DB.GetArticleFromAnyNewsgroupDB(msgIdItem)
		if err != nil {
			c.server.local430.Add(msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF8")
			return
		}
		return article

	} else if wantArticleNum > 0 {
		// Handle article number lookup
		groupDB, err := c.server.DB.GetGroupDB(c.currentGroup)
		if err != nil {
			c.rateLimitOnError()
			c.sendResponse(411, "No such newsgroup")
			return
		}
		defer groupDB.Return()

		if retrievalType == RetrievalStat {
			// For STAT command, we can use overview instead of full article
			overview, err := c.server.DB.GetOverviewByArticleNum(groupDB, wantArticleNum)
			if err != nil {
				c.rateLimitOnError()
				c.sendResponse(423, "No such article number")
				return
			}
			return &models.Article{
				DBArtNum:  wantArticleNum,
				MessageID: overview.MessageID,
			}
		}

		// For other commands, get the full article
		article, err = c.server.DB.GetArticleByNum(groupDB, wantArticleNum)
		if err != nil {
			c.rateLimitOnError()
			c.sendResponse(423, "No such article number")
			return
		}
		return article
	}

	c.rateLimitOnError()
	c.sendResponse(502, "Article not retrieved")
	return nil
}

// sendArticleContent sends full article (headers + body) for ARTICLE command
func (c *ClientConnection) sendArticleContent(article *models.Article) error {
	if c == nil || c.textConn == nil {
		return fmt.Errorf("nil connection in sendArticleContent")
	}
	// Parse headers and body from the article
	//log.Printf("sendArticleContent for result='%#v", result)
	headers := c.parseArticleHeadersFull(article)
	bodyLines := c.parseArticleBody(article)

	// Send response: 220 n message-id Article follows
	if err := c.textConn.PrintfLine("220 %d %s Article follows", article.DBArtNum, article.MessageID); err != nil {
		return err
	}

	// Send headers
	for _, header := range headers {
		if err := c.sendLine(header); err != nil {
			return err
		}
	}

	// Send blank line separating headers from body
	if err := c.sendLine(""); err != nil {
		return err
	}

	// Send body
	for _, line := range bodyLines {
		if err := c.sendLine(line); err != nil {
			return err
		}
	}

	// Send termination line
	return c.sendLine(DOT)
}

// sendHeadContent sends only headers for HEAD command
func (c *ClientConnection) sendHeadContent(article *models.Article) error {
	if c == nil || c.textConn == nil {
		return fmt.Errorf("nil connection in sendHeadContent")
	}
	// Parse headers from the article
	headers := c.parseArticleHeadersFull(article)

	// Send response: 221 n message-id Headers follow
	if err := c.sendResponse(221, fmt.Sprintf("%d %s Headers follow", article.DBArtNum, article.MessageID)); err != nil {
		return err
	}

	// Send headers
	for _, header := range headers {
		if err := c.sendLine(header); err != nil {
			return err
		}
	}

	// Send termination line
	return c.sendLine(DOT)
}

// sendBodyContent sends only body for BODY command
func (c *ClientConnection) sendBodyContent(article *models.Article) error {
	if c == nil || c.textConn == nil {
		return fmt.Errorf("nil connection in sendBodyContent")
	}
	// Parse body from the article
	bodyLines := c.parseArticleBody(article)

	// Send response: 222 n message-id Body follows
	if err := c.sendResponse(222, fmt.Sprintf("%d %s Body follows", article.DBArtNum, article.MessageID)); err != nil {
		return err
	}

	// Send body
	for _, line := range bodyLines {
		if err := c.sendLine(line); err != nil {
			return err
		}
	}

	// Send termination line
	return c.sendLine(DOT)
}

// sendStatContent sends only status for STAT command
func (c *ClientConnection) sendStatContent(article *models.Article) error {
	if c == nil || c.textConn == nil {
		return fmt.Errorf("nil connection in sendStatContent")
	}
	// Send response: 223 n message-id status
	return c.sendResponse(223, fmt.Sprintf("%d %s Article exists", article.DBArtNum, article.MessageID))
}
