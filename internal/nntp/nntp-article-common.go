package nntp

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ArticleRetrievalType defines what content to send
type ArticleRetrievalType int

const (
	RetrievalArticle ArticleRetrievalType = iota // Headers + Body
	RetrievalHead                                // Headers only
	RetrievalBody                                // Body only
	RetrievalStat                                // Status only (no content)
)

// ArticleRetrievalResult contains the result of article lookup
type ArticleRetrievalResult struct {
	Article    *models.Article
	Overview   *models.Overview
	ArticleNum int64
	MsgIdItem  *history.MessageIdItem
	GroupDBs   *database.GroupDBs
}

// retrieveArticleCommon handles the common logic for ARTICLE, HEAD, BODY, and STAT commands
func (c *ClientConnection) retrieveArticleCommon(args []string, retrievalType ArticleRetrievalType) error {
	time.Sleep(time.Second / 5) // ratelimit

	// Get article data using common logic
	result, err := c.getArticleData(args)
	if result == nil || err != nil {
		log.Printf("retrieveArticleCommon Error retrieving article data: %v", err)
		return nil // Error already handled in getArticleData
	}
	defer func() {
		if result.GroupDBs != nil {
			result.GroupDBs.Return(c.server.DB)
		}
	}()

	// Update current article if we have a current group
	if c.currentGroup != "" {
		c.currentArticle = result.ArticleNum
		task := c.server.DB.Batch.GetOrCreateTasksMapKey(c.currentGroup)
		if task != nil && result.MsgIdItem != nil {
			result.MsgIdItem.Mux.Lock()
			result.MsgIdItem.GroupName = task.Newsgroup
			result.MsgIdItem.ArtNum = result.ArticleNum
			result.MsgIdItem.Mux.Unlock()
		}
	}

	// Send appropriate response based on retrieval type
	switch retrievalType {
	case RetrievalArticle:
		return c.sendArticleContent(result)
	case RetrievalHead:
		return c.sendHeadContent(result)
	case RetrievalBody:
		return c.sendBodyContent(result)
	case RetrievalStat:
		return c.sendStatContent(result)
	default:
		return c.sendResponse(500, "Internal error: unknown retrieval type")
	}
}

// getArticleData handles the common article lookup logic
func (c *ClientConnection) getArticleData(args []string) (*ArticleRetrievalResult, error) {
	var groupDBs *database.GroupDBs
	var articleNum int64
	var msgIdItem *history.MessageIdItem
	var err error

	// Parse argument: can be article number or message-id
	if len(args) == 0 {
		if c.currentGroup == "" {
			c.rateLimitOnError()
			c.sendResponse(412, "No newsgroup selected")
			return nil, nil
		}
		// Use current article
		articleNum = c.currentArticle
		if articleNum == 0 {
			c.rateLimitOnError()
			c.sendResponse(420, "Current article number is invalid")
			return nil, nil
		}
		// Get group database
		groupDBs, err = c.server.DB.GetGroupDBs(c.currentGroup)
		if err != nil {
			c.rateLimitOnError()
			c.sendResponse(411, "No such newsgroup")
			return nil, nil
		}

	} else {
		if strings.HasPrefix(args[0], "<") && strings.HasSuffix(args[0], ">") {
			// Message-ID format
			msgIdItem = history.MsgIdCache.GetORCreate(args[0])
			if msgIdItem == nil {
				c.rateLimitOnError()
				c.sendResponse(500, "Error MsgId Cache")
				return nil, nil
			}
			if c.server.local430.Check(msgIdItem) {
				c.rateLimitOnError()
				c.sendResponse(430, "Cache says no!")
				return nil, nil
			}
		} else {
			if c.currentGroup == "" {
				c.rateLimitOnError()
				c.sendResponse(412, "No newsgroup selected")
				return nil, nil
			}
			// Article number format
			articleNum, err = strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				c.rateLimitOnError()
				c.sendResponse(501, "Invalid article number")
				return nil, nil
			}
		}
	}

	// Get article
	var article *models.Article
	var overview *models.Overview

	if msgIdItem != nil {
		// Handle message-ID lookup
		retCase, err := c.server.Processor.Lookup(msgIdItem)
		if err != nil {
			c.server.local430.Add(msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF1")
			return nil, nil
		}

		found := false
		switch retCase {

		case history.CaseError:
			c.server.local430.Add(msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF1")
			return nil, nil

		case history.CasePass:
			// Not found in history
			c.rateLimitOnError()
			log.Printf("MsgIdItem not found in history: '%#v'", msgIdItem)
			c.sendResponse(430, "NotF2")
			return nil, nil

		case history.CaseDupes:
			// Found in history - storage token should now be available
			msgIdItem.Mux.RLock()
			found = msgIdItem.StorageToken != "" || (msgIdItem.GroupName != nil && msgIdItem.ArtNum > 0)
			msgIdItem.Mux.RUnlock()
		}

		if !found {
			log.Printf("MsgIdItem not found in cache: %#v", msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF2")
			return nil, nil
		}

		// Extract storage token or use cached values
		msgIdItem.Mux.RLock()
		mustExtractStorageToken := (msgIdItem.GroupName == nil || msgIdItem.ArtNum == 0) && msgIdItem.StorageToken != ""
		msgIdItem.Mux.RUnlock()

		if mustExtractStorageToken {
			// Parse storage token: "group:articlenum"
			msgIdItem.Mux.RLock()
			parts := strings.SplitN(msgIdItem.StorageToken, ":", 2)
			msgIdItem.Mux.RUnlock()
			if len(parts) != 2 {
				c.server.local430.Add(msgIdItem)
				c.rateLimitOnError()
				c.sendResponse(430, "NotF3")
				log.Printf("Invalid storage token format: %#v", msgIdItem)
				return nil, nil
			}

			task := c.server.DB.Batch.GetOrCreateTasksMapKey(parts[0])
			if task == nil {
				c.server.local430.Add(msgIdItem)
				c.rateLimitOnError()
				c.sendResponse(430, "NotF4")
				return nil, nil
			}

			articleNumParsed, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				c.server.local430.Add(msgIdItem)
				c.rateLimitOnError()
				c.sendResponse(430, "NotF5")
				return nil, nil
			}

			msgIdItem.Mux.Lock()
			msgIdItem.GroupName = task.Newsgroup
			msgIdItem.ArtNum = articleNumParsed
			msgIdItem.Mux.Unlock()
		} else {
			if msgIdItem.GroupName == nil || msgIdItem.ArtNum <= 0 {
				c.server.local430.Add(msgIdItem)
				c.rateLimitOnError()
				c.sendResponse(430, "NotF6")
				return nil, nil
			}
		}

		// Get group database for the specific group from storage token
		if groupDBs == nil || groupDBs.Newsgroup != *msgIdItem.GroupName {
			groupDBs, err = c.server.DB.GetGroupDBs(*msgIdItem.GroupName)
			if err != nil {
				c.server.local430.Add(msgIdItem)
				c.rateLimitOnError()
				c.sendResponse(430, "NotF7")
				return nil, nil
			}
		}

		// Get article by the specific article number from storage token
		article, err = c.server.DB.GetArticleByNum(groupDBs, msgIdItem.ArtNum)
		if err != nil {
			c.server.local430.Add(msgIdItem)
			c.rateLimitOnError()
			c.sendResponse(430, "NotF8")
			return nil, nil
		}
		articleNum = article.ArticleNum

	} else {
		// Handle article number lookup
		if groupDBs == nil {
			groupDBs, err = c.server.DB.GetGroupDBs(c.currentGroup)
			if err != nil {
				c.rateLimitOnError()
				c.sendResponse(411, "No such newsgroup")
				return nil, nil
			}
		}

		// For STAT command, we can use overview instead of full article
		overview, err = c.server.DB.GetOverviewByArticleNum(groupDBs, articleNum)
		if err != nil {
			c.rateLimitOnError()
			c.sendResponse(423, "No such article number")
			return nil, nil
		}

		// For other commands, get the full article
		article, err = c.server.DB.GetArticleByNum(groupDBs, articleNum)
		if err != nil {
			c.rateLimitOnError()
			c.sendResponse(423, "No such article number")
			return nil, nil
		}

		// Create or get msgIdItem
		messageID := article.MessageID
		if overview != nil {
			messageID = overview.MessageID
		}
		msgIdItem = history.MsgIdCache.GetORCreate(messageID)
		if msgIdItem == nil {
			c.rateLimitOnError()
			c.sendResponse(500, "Error MsgId Cache")
			return nil, fmt.Errorf("error msgid cache")
		}

		task := c.server.DB.Batch.GetOrCreateTasksMapKey(groupDBs.Newsgroup)
		if task != nil {
			msgIdItem.Mux.Lock()
			msgIdItem.GroupName = task.Newsgroup
			msgIdItem.ArtNum = articleNum
			msgIdItem.Mux.Unlock()
		}
	}

	return &ArticleRetrievalResult{
		Article:    article,
		Overview:   overview,
		ArticleNum: articleNum,
		MsgIdItem:  msgIdItem,
		GroupDBs:   groupDBs,
	}, nil
}

// sendArticleContent sends full article (headers + body) for ARTICLE command
func (c *ClientConnection) sendArticleContent(result *ArticleRetrievalResult) error {
	// Parse headers and body from the article
	log.Printf("sendArticleContent for result='%#v", result)
	headers := c.parseArticleHeadersFull(result.Article)
	bodyLines := c.parseArticleBody(result.Article)

	// Send response: 220 n message-id Article follows
	if err := c.sendResponse(220, fmt.Sprintf("%d %s Article follows", result.ArticleNum, result.MsgIdItem.MessageId)); err != nil {
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
func (c *ClientConnection) sendHeadContent(result *ArticleRetrievalResult) error {
	// Parse headers from the article
	headers := c.parseArticleHeadersFull(result.Article)

	// Send response: 221 n message-id Headers follow
	if err := c.sendResponse(221, fmt.Sprintf("%d %s Headers follow", result.ArticleNum, result.MsgIdItem.MessageId)); err != nil {
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
func (c *ClientConnection) sendBodyContent(result *ArticleRetrievalResult) error {
	// Parse body from the article
	bodyLines := c.parseArticleBody(result.Article)

	// Send response: 222 n message-id Body follows
	if err := c.sendResponse(222, fmt.Sprintf("%d %s Body follows", result.ArticleNum, result.MsgIdItem.MessageId)); err != nil {
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
func (c *ClientConnection) sendStatContent(result *ArticleRetrievalResult) error {
	// Send response: 223 n message-id status
	return c.sendResponse(223, fmt.Sprintf("%d %s Article exists", result.ArticleNum, result.MsgIdItem.MessageId))
}
