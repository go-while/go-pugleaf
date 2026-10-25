package nntp

// Package nntp provides NNTP command implementations for go-pugleaf.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/utils"
)

// Constants for maximum lines to read in various commands

// MaxReadLinesArticle Maximum lines for ARTICLE command, including headers and body
const MaxReadLinesArticle = 256 * 1024

// MaxReadLinesHeaders Maximum lines for HEAD command, which only retrieves headers
const MaxReadLinesHeaders = 1024

// MaxReadLinesXover Maximum lines for XOVER command, which retrieves overview lines
var MaxReadLinesXover int64 = 100 // XOVER command typically retrieves overview lines MaxBatch REFERENCES this in processor!!!

// MaxReadLinesBody Maximum lines for BODY command, which retrieves the body of an article
const MaxReadLinesBody = MaxReadLinesArticle - MaxReadLinesHeaders

// ErrResponseTooLarge is returned when a multi-line response exceeds its octet
// budget (BackendConfig.MaxArticleResponseBytes / MaxListResponseBytes) or an
// XHDR stream returns more lines than articles were requested. The connection
// is unusable afterwards and is force-closed.
var ErrResponseTooLarge = errors.New("nntp: response too large")

// authRequiredErr is the client's error for a 480 on a reader command: it wraps
// ErrAuthRequired (errors.Is works through the pool's %w wrapping) and keeps the
// command and the server's line. A 480 after a successful login means the server
// dropped the login (timed out, or the account changed); the pool closes that
// connection and the next Get dials and authenticates again. Before this every
// command but GROUP returned a plain error, so a caller could not tell an expired
// login from any other failure (plan fetcher-hostile-peer).
func authRequiredErr(cmd string, code int, message string) error {
	return fmt.Errorf("%w: %s: %d %s", ErrAuthRequired, cmd, code, strings.TrimSpace(message))
}

// ErrNoArticlesInRange is wrapped into the XOver error when the server answers
// 423 (RFC 3977 §8.3.2: no articles in that range). The response line was read
// completely; callers may treat the range as empty.
var ErrNoArticlesInRange = errors.New("no articles in range")

// StatArticle checks if an article exists on the server
func (c *BackendConn) StatArticle(messageID string) (bool, error) {
	if err := validMessageIDArg(messageID); err != nil {
		return false, err
	}
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return false, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.sendCmd("STAT %s", messageID)
	if err != nil {
		return false, fmt.Errorf("failed to send STAT command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, _, err := c.TextConn.ReadCodeLine(223)
	if err != nil && code == 0 {
		return false, fmt.Errorf("failed to read STAT response: %w", err)
	}

	switch code {
	case ArticleExists:
		return true, nil
	case NoSuchArticle, DMCA:
		return false, nil
	case AuthRequired:
		return false, authRequiredErr("STAT "+messageID, code, "")
	default:
		return false, fmt.Errorf("unexpected STAT response: %d", code)
	}
}

// GetArticle retrieves a complete article from the server
func (c *BackendConn) GetArticle(messageID *string, bulkmode bool) (*models.Article, error) {
	if messageID == nil {
		return nil, fmt.Errorf("%w: nil message-id", ErrInvalidCommandArg)
	}
	if err := validMessageIDArg(*messageID); err != nil {
		return nil, err
	}
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()
	id, err := c.sendCmd("ARTICLE %s", *messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send ARTICLE '%s' command: %w", *messageID, err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(ArticleFollows)
	if err != nil && code == 0 {
		log.Printf("[ERROR] failed to read ARTICLE '%s' code=%d message='%s' err: %v", *messageID, code, message, err)
		return nil, fmt.Errorf("failed to read ARTICLE '%s' code=%d message='%s' err: %w", *messageID, code, message, err)
	}

	if code != ArticleFollows {
		switch code {
		case NoSuchArticle:
			// ---> internal/nntp/nntp-backend-pool.go:158
			//log.Printf("[BECONN] GetArticle: not found: '%s' code=%d message='%s' err='%v'", *messageID, code, message, err)
			return nil, ErrArticleNotFound
		case DMCA:
			log.Printf("[BECONN] GetArticle: removed (DMCA): '%s' code=%d message='%s' err='%v'", *messageID, code, message, err)
			return nil, ErrArticleRemoved
		case AuthRequired:
			return nil, authRequiredErr("ARTICLE "+*messageID, code, message)
		default:
			return nil, fmt.Errorf("unexpected ARTICLE '%s' code=%d message='%s' err='%v'", *messageID, code, message, err)
		}
	}

	// Read the article content
	lines, err := c.readMultilineResponse("article")
	if err != nil {
		return nil, fmt.Errorf("failed to read article '%s' content: %w", *messageID, err)
	}

	// Parse article into headers and body
	article, err := ParseLegacyArticleLines(*messageID, lines, bulkmode)
	if err != nil {
		return nil, fmt.Errorf("failed to parse article '%s': %w", *messageID, err)
	}

	return article, nil
}

// GetHead retrieves only the headers of an article
func (c *BackendConn) GetHead(messageID string) (*models.Article, error) {
	if err := validMessageIDArg(messageID); err != nil {
		return nil, err
	}
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.sendCmd("HEAD %s", messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send HEAD command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(HeadFollows)
	if err != nil && code == 0 {
		return nil, fmt.Errorf("failed to read HEAD response: %w", err)
	}

	if code != HeadFollows {
		switch code {
		case NoSuchArticle:
			log.Printf("[INFO] head not found: %s", messageID)
			return nil, ErrArticleNotFound
		case DMCA:
			log.Printf("[INFO] head removed (DMCA): %s", messageID)
			return nil, ErrArticleRemoved
		case AuthRequired:
			return nil, authRequiredErr("HEAD "+messageID, code, message)
		default:
			return nil, fmt.Errorf("unexpected HEAD response: %d %s", code, message)
		}
	}

	// Read the headers
	lines, err := c.readMultilineResponse("headers")
	if err != nil {
		return nil, fmt.Errorf("failed to read headers: %w", err)
	}

	// Parse headers only
	article := &models.Article{
		MessageID: messageID,
		Headers:   make(map[string][]string),
	}

	if err := ParseHeaders(article, lines); err != nil {
		return nil, fmt.Errorf("failed to parse headers: %w", err)
	}

	return article, nil
}

// GetBody retrieves only the body of an article
func (c *BackendConn) GetBody(messageID string) ([]byte, error) {
	if err := validMessageIDArg(messageID); err != nil {
		return nil, err
	}
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.sendCmd("BODY %s", messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send BODY command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(BodyFollows)
	if err != nil && code == 0 {
		return nil, fmt.Errorf("failed to read BODY response: %w", err)
	}

	if code != BodyFollows {
		switch code {
		case NoSuchArticle:
			log.Printf("[INFO] body not found: %s", messageID)
			return nil, ErrArticleNotFound
		case DMCA:
			log.Printf("[INFO] body removed (DMCA): %s", messageID)
			return nil, ErrArticleRemoved
		case AuthRequired:
			return nil, authRequiredErr("BODY "+messageID, code, message)
		default:
			return nil, fmt.Errorf("unexpected BODY response: %d %s", code, message)
		}
	}

	// Read the body
	lines, err := c.readMultilineResponse("body")
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	// Join lines with CRLF
	body := strings.Join(lines, "\r\n")
	return []byte(body), nil
}

// ListGroups retrieves a list of available newsgroups
func (c *BackendConn) ListGroups() ([]GroupInfo, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.sendCmd("LIST")
	if err != nil {
		return nil, fmt.Errorf("failed to send LIST command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(215)
	if err != nil {
		if code == AuthRequired {
			return nil, authRequiredErr("LIST", code, message)
		}
		return nil, fmt.Errorf("failed to read LIST response: %w", err)
	}

	if code != 215 {
		return nil, fmt.Errorf("unexpected LIST response: %d %s", code, message)
	}

	// Read the group list
	lines, err := c.readMultilineResponse("list")
	if err != nil {
		return nil, fmt.Errorf("failed to read group list: %w", err)
	}

	// Parse group information
	var groups = make([]GroupInfo, 0, len(lines))
	for _, line := range lines {
		group, err := ParseGroupLine(line)
		if err != nil {
			continue // Skip malformed lines
		}
		groups = append(groups, group)
	}

	return groups, nil
}

// SelectGroup selects a newsgroup for operation
func (c *BackendConn) SelectGroup(groupName string) (*GroupInfo, int, error) {
	if err := validGroupArg(groupName); err != nil {
		return nil, 0, err
	}
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, 0, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.sendCmd("GROUP %s", groupName)
	if err != nil {
		return nil, 0, err
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(211)
	if err != nil {
		if code != 411 {
			log.Printf("[ERROR] failed to read GROUP '%s' code=%d message='%s' err: %v", groupName, code, message, err)
		}
		return nil, code, err
	}

	// Parse group information from response
	// RFC 3977: Response code is 211
	// message format is "count first last group"
	parts := strings.Fields(message)
	if len(parts) < 4 {
		return nil, code, fmt.Errorf(
			"malformed GROUP response (expected 'count first last group'): %s group %s",
			message, groupName,
		)
	}

	count, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, code, fmt.Errorf("failed to parse count in GROUP '%s' response: %w", groupName, err)
	}
	first, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, code, fmt.Errorf("failed to parse first in GROUP '%s' response: %w", groupName, err)
	}
	last, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, code, fmt.Errorf("failed to parse last in GROUP '%s' response: %w", groupName, err)
	}

	//log.Printf("Selected group '%s' with %d articles (range: %d-%d)", groupName, count, first, last)

	return &GroupInfo{
		Name:  groupName,
		Count: count,
		First: first,
		Last:  last,
		//PostingOK: true, // Assume posting is OK unless we know otherwise
	}, code, nil
}

// XOver retrieves overview data for a range of articles
// This is essential for efficiently building newsgroup databases
// enforceLimit controls whether to limit to max 1000 articles to prevent SQLite overload
func (c *BackendConn) XOver(groupName string, start, end int64, enforceLimit bool) ([]OverviewLine, error) {
	if groupName == "" {
		return nil, fmt.Errorf("error XOver: group name is required")
	}
	if err := validGroupArg(groupName); err != nil {
		return nil, err
	}
	//log.Printf("XOver group '%s' start=%d end=%d", groupName, start, end)
	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil {
		if code == 411 {
			return nil, fmt.Errorf("XOVER group '%s': %w", groupName, ErrNewsgroupNotFound)
		}
		return nil, fmt.Errorf("failed to select group '%s': code=%d err=%w", groupName, code, err)
	}
	_ = groupInfo // groupInfo is not used further, but we keep it for clarity
	c.lastUsed = time.Now()

	// Limit to 1000 articles maximum to prevent SQLite overload (only if enforceLimit is true)
	if enforceLimit && end > 0 && (end-start+1) > MaxReadLinesXover {
		end = start + MaxReadLinesXover - 1
	}

	var id uint
	if end > 0 {
		id, err = c.sendCmd("XOVER %d-%d", start, end)
	} else {
		id, err = c.sendCmd("XOVER %d", start)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send XOVER command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(224)
	if err != nil {
		if code == 423 {
			return nil, fmt.Errorf("failed to read XOVER response: %w: %w", err, ErrNoArticlesInRange)
		}
		if code == AuthRequired {
			return nil, authRequiredErr("XOVER", code, message)
		}
		return nil, fmt.Errorf("failed to read XOVER response: %w", err)
	}

	if code != 224 {
		if code == 423 {
			return nil, fmt.Errorf("XOVER failed: %d %s: %w", code, message, ErrNoArticlesInRange)
		}
		return nil, fmt.Errorf("XOVER failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("xover")
	if err != nil {
		return nil, fmt.Errorf("failed to read XOVER data: %w", err)
	}

	// Parse overview lines
	added := 0
	// nolint
	var overviews []OverviewLine
	for _, line := range lines {
		overview, err := c.parseOverviewLine(line)
		if err != nil {
			continue // Skip malformed lines
		}
		overviews = append(overviews, overview)
		added++
	}
	//log.Printf("XOver found %d articles in group '%s' read lines=%d", added, groupName, len(lines))
	return overviews, nil
}

// XHdr retrieves specific header field for a range of articles
// Automatically limits to max 1000 articles to prevent SQLite overload
func (c *BackendConn) XHdr(groupName, field string, start, end int64) ([]HeaderLine, error) {
	if err := validGroupArg(groupName); err != nil {
		return nil, err
	}
	if err := validTokenArg(field, false); err != nil {
		return nil, fmt.Errorf("XHDR field: %w", err)
	}
	c.mux.Lock()
	if !c.IsConnected() {
		c.mux.Unlock()
		return nil, fmt.Errorf("not connected")
	}
	c.mux.Unlock()
	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil {
		if code == 411 {
			return nil, fmt.Errorf("XHDR group '%s': %w", groupName, ErrNewsgroupNotFound)
		}
		return nil, fmt.Errorf("failed to select group '%s': code=%d err=%w", groupName, code, err)
	}
	_ = groupInfo // groupInfo is not used further, but we keep it for clarity
	c.lastUsed = time.Now()

	// Limit to 1000 articles maximum to prevent SQLite overload
	if end > 0 && (end-start+1) > MaxReadLinesXover {
		end = start + MaxReadLinesXover - 1
	}
	log.Printf("XHdr group '%s' field '%s' start=%d end=%d", groupName, field, start, end)
	var id uint
	if end > 0 {
		id, err = c.sendCmd("XHDR %s %d-%d", field, start, end)
	} else {
		id, err = c.sendCmd("XHDR %s %d", field, start)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send XHDR command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(221)
	if err != nil {
		if code == AuthRequired {
			return nil, authRequiredErr("XHDR", code, message)
		}
		return nil, fmt.Errorf("failed to read XHDR response: %w", err)
	}

	if code != 221 {
		return nil, fmt.Errorf("XHDR failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("xhdr")
	if err != nil {
		return nil, fmt.Errorf("failed to read XHDR data: %w", err)
	}

	// Parse header lines
	var headers = make([]HeaderLine, 0, len(lines))
	for _, line := range lines {
		header, err := c.parseHeaderLine(line)
		if err != nil {
			continue // Skip malformed lines
		}
		if header.ArticleNum > 0 {
			headers = append(headers, header)
		}
	}

	return headers, nil
}

var ErrOutOfRange error = fmt.Errorf("end range exceeds group last article number")

func (c *BackendConn) WantShutdown(shutdownChan <-chan struct{}) bool {
	select {
	case _, ok := <-shutdownChan:
		if !ok {
			// channel is closed
			return true
		}
	default:
	}
	return false
}

// XHdrStreamed performs XHDR command and streams results line by line through a channel
// Fetches max 1000 hdrs and starts a new fetch if the channel is less than 10% capacity.
// xhdrChan is always closed when it returns; a panic is turned into an error.
func (c *BackendConn) XHdrStreamed(groupName, field string, start, end int64, xhdrChan chan<- HeaderLine, shutdownChan <-chan struct{}) (err error) {
	defer close(xhdrChan)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[NNTP-CLIENT] XHdrStreamed panic ng='%s': %v", groupName, r)
			err = fmt.Errorf("XHdrStreamed panic: %v", r)
		}
	}()
	channelCap := cap(xhdrChan)
	lowWaterMark := channelCap / 10 // 10% threshold
	if lowWaterMark < 1 {
		lowWaterMark = 1
	}

	currentStart := start
	var isleep int64 = 10
	for currentStart <= end {
		// Check for shutdown signal
		if c.WantShutdown(shutdownChan) {
			log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
			return fmt.Errorf("shutdown requested")
		}

		// Wait if channel is not empty
		for len(xhdrChan) > lowWaterMark {
			time.Sleep(time.Duration(isleep) * time.Millisecond)
			if c.WantShutdown(shutdownChan) {
				log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
				return fmt.Errorf("shutdown requested")
			}
		}

		// Calculate batch end (max 1000 articles)
		batchEnd := currentStart + 999 // 1000 articles max
		if batchEnd > end {
			batchEnd = end
		}

		// Fetch this batch
		startStream := time.Now()
		if err := c.XHdrStreamedBatch(groupName, field, currentStart, batchEnd, xhdrChan, shutdownChan); err != nil {
			return fmt.Errorf("XHdrStreamedBatch failed for range %d-%d: %w", currentStart, batchEnd, err)
		}
		isleep = time.Since(startStream).Milliseconds() / 2
		if isleep < 10 {
			isleep = 10
		}

		// Move to next batch
		currentStart = batchEnd + 1

	}

	return nil // the deferred close ends the stream
}

// XHdrStreamedBatch performs XHDR command and streams results line by line through a channel
func (c *BackendConn) XHdrStreamedBatch(groupName, field string, start, end int64, xhdrChan chan<- HeaderLine, shutdownChan <-chan struct{}) error {
	if err := validGroupArg(groupName); err != nil {
		return err
	}
	if err := validTokenArg(field, false); err != nil {
		return fmt.Errorf("XHDR field: %w", err)
	}
	c.mux.Lock()
	if !c.IsConnected() {
		c.mux.Unlock()
		return fmt.Errorf("not connected")
	}
	c.mux.Unlock()

	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil || groupInfo == nil {
		if code == 411 {
			return fmt.Errorf("XHDR group '%s': %w", groupName, ErrNewsgroupNotFound)
		}
		return fmt.Errorf("failed to select group '%s': code=%d err=%w", groupName, code, err)
	}
	if end > groupInfo.Last {
		return ErrOutOfRange
	}
	c.lastUsed = time.Now()

	// Limit to 1000 articles maximum to prevent SQLite overload
	const maxFetchLimit = 1000
	if end > 0 && (end-start+1) > maxFetchLimit {
		end = start + maxFetchLimit - 1
	}
	//log.Printf("XHdrStreamed group '%s' field '%s' start=%d end=%d", groupName, field, start, end)

	// The server may not return more lines than articles requested (A-nntp-client-1).
	maxLines := int64(1)
	var id uint
	if end > 0 {
		id, err = c.sendCmd("XHDR %s %d-%d", field, start, end)
		if end >= start {
			maxLines = end - start + 1
		}
	} else {
		id, err = c.sendCmd("XHDR %s %d-%d", field, start, start)
	}
	if err != nil {
		return fmt.Errorf("failed to send XHDR command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	// Check for shutdown before reading initial response
	if c.WantShutdown(shutdownChan) {
		log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
		return fmt.Errorf("shutdown requested")
	}

	code, message, err := c.TextConn.ReadCodeLine(221)
	if err != nil {
		if code == AuthRequired {
			return authRequiredErr("XHDR", code, message)
		}
		return fmt.Errorf("failed to read XHDR response: %w", err)
	}

	if code != 221 {
		return fmt.Errorf("XHDR failed: ng: '%s' %d %s", groupName, code, message)
	}

	// Read multiline response line by line and send to channel immediately.
	// Every error return leaves the response unread: the pool wrapper
	// force-closes the connection on any error.
	var lineCount int64
	for {
		// Check for shutdown signal between reads
		if c.WantShutdown(shutdownChan) {
			log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
			return fmt.Errorf("shutdown requested")
		}

		line, err := c.TextConn.ReadLine()
		if err != nil {
			// a cut-off stream is an error, not a complete result
			log.Printf("[ERROR] XHdrStreamed read error ng: '%s' err='%v'", groupName, err)
			return fmt.Errorf("XHDR stream ng '%s': %w", groupName, err)
		}

		// Check for end marker
		if line == DOT {
			break
		}

		lineCount++
		if lineCount > maxLines {
			return fmt.Errorf("%w: XHDR returned more lines than requested (%d) ng '%s'", ErrResponseTooLarge, maxLines, groupName)
		}

		// Parse the header line
		header, parseErr := c.parseHeaderLine(line)
		if parseErr != nil {
			log.Printf("[ERROR] XHdrStreamed parse error ng: '%s' err='%v'", groupName, parseErr)
			continue // Skip malformed lines
		}
		if header.ArticleNum <= 0 {
			continue // Skip invalid article numbers (A-nntp-client-7)
		}

		if c.WantShutdown(shutdownChan) {
			log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
			return fmt.Errorf("shutdown requested")
		}

		xhdrChan <- header
	}

	// Don't close channel here - let the main function handle it
	return nil
}

// ListGroup retrieves article numbers for a specific group
func (c *BackendConn) ListGroup(groupName string, start, end int64) ([]int64, error) {
	if err := validGroupArg(groupName); err != nil {
		return nil, err
	}
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	var id uint
	var err error
	if start > 0 && end > 0 {
		id, err = c.sendCmd("LISTGROUP %s %d-%d", groupName, start, end)
	} else {
		id, err = c.sendCmd("LISTGROUP %s", groupName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send LISTGROUP command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(211)
	if err != nil {
		if code == AuthRequired {
			return nil, authRequiredErr("LISTGROUP", code, message)
		}
		return nil, fmt.Errorf("failed to read LISTGROUP response: %w", err)
	}

	if code != 211 {
		return nil, fmt.Errorf("LISTGROUP failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("listgroup")
	if err != nil {
		return nil, fmt.Errorf("failed to read article numbers: %w", err)
	}

	// Parse article numbers
	var articleNums []int64
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		num, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			continue // Skip invalid numbers
		}
		articleNums = append(articleNums, num)
	}

	return articleNums, nil
}

// responseBudget returns the octet budget of a multi-line response of kind src
// (readMultilineResponse): article/headers/body use MaxArticleResponseBytes,
// everything else MaxListResponseBytes. 0 selects the default, a negative
// value means unlimited (returned as 0).
func (c *BackendConn) responseBudget(src string) int64 {
	var v, def int64
	switch src {
	case "article", "headers", "body":
		v, def = c.Backend.MaxArticleResponseBytes, DefaultClientMaxArticleBytes
	default:
		v, def = c.Backend.MaxListResponseBytes, DefaultClientMaxListBytes
	}
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0
	}
	return v
}

// readMultilineResponse reads a multi-line response ending with "."
func (c *BackendConn) readMultilineResponse(src string) ([]string, error) {
	var lines []string
	lineCount := 0
	budget := c.responseBudget(src) // 0: unlimited
	var total int64
	maxReadLines := MaxReadLines // Use the constant defined in your package

	switch src {

	case "article":
		// For ARTICLE, we expect headers and body
		maxReadLines = MaxReadLinesArticle

	case "headers":
		// For HEAD, we expect only headers
		maxReadLines = MaxReadLinesHeaders

	case "body":
		maxReadLines = MaxReadLinesBody // BODY can be large, but we limit it

	default:
		// pass
		/*
			case "list":
				// For LIST, we expect group names and info
				maxReadLines = MaxReadLinesList

			case "overview":
				// For XOVER, we expect overview lines
				maxReadLines = MaxReadLinesOverview

			case "xover":
				maxReadLines = MaxReadLinesOverview

			case "xhdr":
				maxReadLines = MaxReadLinesXHDR

			case "listgroup":
				maxReadLines = MaxReadLinesListGroup
		*/

	}
	for {
		if lineCount >= maxReadLines {
			c.conn.Close() // Close connection on limit reached
			c.forceClose = true
			return nil, fmt.Errorf("%w: too many lines in response (limit: %d)", ErrResponseTooLarge, maxReadLines)
		}

		line, err := c.TextConn.ReadLine()
		if err != nil {
			if errors.Is(err, ErrResponseLineTooLong) {
				c.forceClose = true // a limit was exceeded: never reuse this connection
			}
			return nil, err
		}

		// Check for end marker
		if line == "." {
			break
		}

		total += int64(len(line)) + 2 // CRLF
		if budget > 0 && total > budget {
			if c.conn != nil {
				_ = c.conn.Close() // Close connection on limit reached
			}
			c.forceClose = true
			return nil, fmt.Errorf("%w: %s response over %d bytes", ErrResponseTooLarge, src, budget)
		}

		// Handle dot-stuffing (lines starting with .. become .)
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}

		lines = append(lines, line)
		lineCount++
	}

	return lines, nil
}

// ParseArticleLines parses article lines into headers and body
func ParseLegacyArticleLines(messageID string, lines []string, bulkmode bool) (*models.Article, error) {
	article := &models.Article{
		MessageID: messageID,
		Headers:   make(map[string][]string),
	}

	// Find the separator between headers and body
	bodyStart := -1
	for i, line := range lines {
		if line == "" {
			bodyStart = i + 1
			break
		}
	}

	if bodyStart == -1 {
		return nil, fmt.Errorf("malformed article: no header-body separator found in msgId='%s'", messageID)
	}

	// Parse headers
	if err := ParseHeaders(article, lines[:bodyStart-1]); err != nil {
		return nil, err
	}

	// RFC 3977 §8.1.1 :bytes: the whole article with CRLF line ends (also for an empty body)
	article.Bytes = common.WireArticleSize(lines[:bodyStart-1], lines[bodyStart:])

	// Parse article
	if bodyStart < len(lines) {
		article.BodyText = strings.Join(lines[bodyStart:], "\n")
		article.Lines = len(lines) - bodyStart
		if !bulkmode {
			// original body lines for peering
			article.NNTPhead = lines[:bodyStart-1]
			article.NNTPbody = lines[bodyStart:]
		}
	}
	article.Subject = common.GetHeaderFirst(article.Headers, "subject")
	article.FromHeader = common.GetHeaderFirst(article.Headers, "from")
	article.Path = common.GetHeaderFirst(article.Headers, "path")
	article.References = common.GetHeaderFirst(article.Headers, "references")
	article.RefSlice = utils.ParseReferences(article.References) // capture all references for thread chain analysis
	article.DateString = common.GetHeaderFirst(article.Headers, "date")
	return article, nil
}

func MultiLineHeaderToMergedString(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) == 1 {
		return vals[0] // Fast path for single-line headers (most common case)
	}
	return strings.Join(vals, "\n") // Ultra fast for multi-line
}

// parseHeaders parses header lines into the article headers map
func ParseHeaders(article *models.Article, headerLines []string) error {
	var currentHeader string

	for _, line := range headerLines {
		if line == "" {
			break
		}

		// Check for header continuation (line starts with space or tab)
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if currentHeader != "" {
				// Append to previous header
				existing := article.Headers[currentHeader]
				if len(existing) > 0 {
					existing[len(existing)-1] += " " + strings.TrimSpace(line)
					article.Headers[currentHeader] = existing
				}
			}
			continue
		}

		// Parse new header
		colonPos := strings.Index(line, ":")
		if colonPos == -1 {
			continue // Skip malformed headers
		}

		headerName := strings.TrimSpace(line[:colonPos])
		headerValue := strings.TrimSpace(line[colonPos+1:])

		currentHeader = strings.ToLower(headerName)
		if strings.ToLower(headerName) == "xref" {
			currentHeader = ""
			continue
		}
		switch strings.ToLower(headerName) {
		case "newsgroups", "date", "references", "subject", "from", "path":
			//pass
		default:
			// not needed
			currentHeader = ""
			continue
		}
		article.Headers[currentHeader] = append(article.Headers[currentHeader], headerValue)
	}
	article.HeadersJSON = MultiLineHeaderToMergedString(headerLines)
	return nil
}

// parseGroupLine parses a single line from LIST command response
func ParseGroupLine(line string) (GroupInfo, error) {
	// Format: "group last first posting"
	parts := strings.Fields(line)
	if len(parts) < 4 {
		return GroupInfo{}, fmt.Errorf("malformed group line: %s", line)
	}

	last, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return GroupInfo{}, fmt.Errorf("invalid last article number in group line: %s", line)
	}
	first, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return GroupInfo{}, fmt.Errorf("invalid first article number in group line: %s", line)
	}
	postingOK := parts[3] == "y"

	count := int64(0)
	if last >= first {
		count = last - first + 1
	}

	return GroupInfo{
		Name:      parts[0],
		Count:     count,
		First:     first,
		Last:      last,
		PostingOK: postingOK,
		Status:    parts[3],
	}, nil
}

// parseOverviewLine parses a single XOVER response line
// Format: articlenum<tab>subject<tab>from<tab>date<tab>message-id<tab>references<tab>bytes<tab>lines
func (c *BackendConn) parseOverviewLine(line string) (OverviewLine, error) {
	parts := strings.Split(line, "\t")
	if len(parts) < 7 {
		return OverviewLine{}, fmt.Errorf("malformed XOVER line: %s", line)
	}

	articleNum, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || articleNum <= 0 {
		return OverviewLine{}, fmt.Errorf("invalid article number in XOVER line: %q", parts[0])
	}
	bytes, _ := strconv.ParseInt(parts[6], 10, 64)
	lines := int64(0)
	if len(parts) > 7 {
		lines, _ = strconv.ParseInt(parts[7], 10, 64)
	}

	return OverviewLine{
		ArticleNum: articleNum,
		Subject:    parts[1],
		From:       parts[2],
		Date:       parts[3],
		MessageID:  parts[4],
		References: parts[5],
		Bytes:      bytes,
		Lines:      lines,
	}, nil
}

// parseHeaderLine parses a single XHDR response line
// Format: articlenum<space>header-value
func (c *BackendConn) parseHeaderLine(line string) (HeaderLine, error) {
	parts := strings.SplitN(line, " ", 2)
	if len(parts) < 2 {
		return HeaderLine{}, fmt.Errorf("malformed XHDR line: %s", line)
	}

	articleNum, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		log.Printf("Invalid article number in XHDR line: %q", parts[0])
		return HeaderLine{}, fmt.Errorf("invalid article number in XHDR line: %q", parts[0])
	}
	headerline := HeaderLine{
		ArticleNum: articleNum,
		Value:      parts[1],
	}
	return headerline, nil
}

// SendCheckMultiple sends CHECK commands for multiple message IDs without returning responses!
// Registers each command ID with the demuxer for proper response routing
func (c *BackendConn) SendCheckMultiple(messageIDs []*string, readCHECKResponsesChan chan *ReadRequest, job *CHTTJob, demuxer *ResponseDemuxer) (checksSent uint64, err error) {
	c.mux.Lock()

	if !c.IsConnected() {
		c.mux.Unlock()
		return 0, fmt.Errorf("not connected")
	}

	if c.ModeReader {
		c.mux.Unlock()
		return 0, fmt.Errorf("cannot check article in reader mode")
	}
	c.lastUsed = time.Now()
	c.mux.Unlock()

	if len(messageIDs) == 0 {
		return 0, fmt.Errorf("no message IDs provided")
	}

	// Reject an invalid message-id before any CHECK of the batch is written
	// (nntp-transfer drops them when it builds the batch).
	for _, msgID := range messageIDs {
		if msgID == nil || *msgID == "" {
			continue // skipped below
		}
		if err := validMessageIDArg(*msgID); err != nil {
			return 0, fmt.Errorf("CHECK: %w", err)
		}
	}

	//writer := bufio.NewWriter(c.conn)
	//defer writer.Flush()
	//log.Printf("Newsgroup: '%s' | SendCheckMultiple commands for %d message IDs", *job.Newsgroup, len(messageIDs))

	for n, msgID := range messageIDs {
		if msgID == nil || *msgID == "" {
			log.Printf("Newsgroup: '%s' | Skipping empty message ID in CHECK command", *job.Newsgroup)
			continue
		}
		//log.Printf("Newsgroup: '%s' | CHECK '%s' acquire c.mux.Lock() (%d/%d)", *job.Newsgroup, *msgID, n+1, len(messageIDs))
		c.mux.Lock()
		cmdID, err := c.sendCmd("CHECK %s", *msgID)
		c.mux.Unlock()
		if err != nil {
			return checksSent, fmt.Errorf("failed to send CHECK '%s': %w", *msgID, err)
		}

		checksSent++

		// Register command ID with demuxer as TYPE_CHECK
		demuxer.RegisterCommand(cmdID, TYPE_CHECK)

		//log.Printf("Newsgroup: '%s' | CHECK sent '%s' (CmdID=%d) pass notify to readResponsesChan=%d", *job.Newsgroup, *msgID, cmdID, len(readCHECKResponsesChan))
		//readCHECKResponsesChan <- &ReadRequest{CmdID: cmdID, Job: job, MsgID: msgID, N: n + 1, Reqs: len(messageIDs)}
		readCHECKResponsesChan <- GetReadRequest(cmdID, job, msgID, n+1, len(messageIDs))
		//log.Printf("Newsgroup: '%s' | CHECK notified response reader '%s' (CmdID=%d) readCHECKResponsesChan=%d", *job.Newsgroup, *msgID, cmdID, len(readCHECKResponsesChan))
	}

	// Update job counter with how many CHECK commands were actually sent
	job.Mux.Lock()
	job.CheckSentCount += checksSent
	job.Mux.Unlock()

	return checksSent, nil
}

// transferHeaders builds the header lines of an article for an outbound
// transfer (TAKETHIS and IHAVE): relay the stored header block where it is
// complete (RFC 5537 §3.6), rebuild it with ReconstructHeaders where it is
// incomplete (web posts, rows without headers_json). An error means the
// article must not be transferred; common.ErrRelayHeadersInvalid marks the
// unsafe stored blocks the caller skips (the connection stays healthy).
func transferHeaders(article *models.Article, nntphostname *string, newsgroup string) ([]string, error) {
	host := ""
	if nntphostname != nil {
		host = *nntphostname
	}
	var headers []string
	var err error
	if article.HeadersJSON != "" {
		headers, err = common.RelayHeaders(article, host)
	}
	if article.HeadersJSON == "" || errors.Is(err, common.ErrRelayHeadersIncomplete) {
		headers, err = common.ReconstructHeaders(article, true, nntphostname, newsgroup)
	}
	if err != nil {
		return nil, err
	}
	return headers, nil
}

// IHave offers one article to a transit peer with IHAVE (RFC 3977 §6.3.2), the
// fallback for peers without streaming (plan transfer-ihave-fallback). Unlike
// the pipelined CHECK/TAKETHIS path it is synchronous and takes c.mux for the
// whole exchange, like PostArticleContext: one article, one answer.
//
// The returned code is the peer's status; err is nil exactly for the four
// outcomes the caller counts and decides on:
//
//	235 - transferred and accepted
//	435 - not wanted (the peer has it, or refuses the id)
//	436 - transfer failed, try again later (before or after the article)
//	437 - rejected after the transfer: do not offer it again
//
// Everything else returns an error: a 480 wraps ErrAuthRequired, 500/501 (no
// IHAVE) and 502 (transfer permission denied) wrap ErrIHaveUnsupported — all
// three leave the connection usable, the caller stops offering articles. An
// unsafe or incomplete header block is skipped before anything is written
// (common.ErrRelayHeadersInvalid, ErrInvalidCommandArg; the connection stays
// connected). A read error, EOF or any other status force-closes the
// connection: the article may be half-sent and the peer's state is unknown.
//
// txBytes is the number of octets of the article written on the wire (headers,
// separator, dot-stuffed body and terminator), 0 when nothing was written.
func (c *BackendConn) IHave(article *models.Article, nntphostname *string, newsgroup string) (code int, txBytes int, err error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return 0, 0, fmt.Errorf("not connected")
	}

	headers, err := transferHeaders(article, nntphostname, newsgroup)
	if err != nil {
		if errors.Is(err, common.ErrRelayHeadersInvalid) {
			log.Printf("[NNTP-CLIENT] Newsgroup: '%s' | skip IHAVE '%s': %v", newsgroup, article.MessageID, err)
		}
		return 0, 0, err
	}
	// RelayHeaders/ReconstructHeaders reject invalid Message-IDs already; this
	// guards the command argument itself (skipped, nothing written).
	if err := validMessageIDArg(article.MessageID); err != nil {
		log.Printf("[NNTP-CLIENT] Newsgroup: '%s' | skip IHAVE: %v", newsgroup, err)
		return 0, 0, err
	}
	c.lastUsed = time.Now()

	id, err := c.sendCmd("IHAVE %s", article.MessageID)
	if errors.Is(err, ErrInvalidCommandArg) {
		return 0, 0, err // refused before writing: the connection is healthy
	}
	if err != nil {
		c.ForceCloseConn()
		return 0, 0, fmt.Errorf("failed to send IHAVE command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id)

	// First response: 335 asks for the article, everything else ends here.
	code, line, err := c.TextConn.ReadCodeLine(335)
	if err != nil && code == 0 {
		c.ForceCloseConn()
		return 0, 0, fmt.Errorf("failed to read IHAVE '%s' response: %w", article.MessageID, err)
	}
	switch code {
	case 335:
		// send the article below
	case 435, 436:
		return code, 0, nil // not wanted / deferred: nothing was written
	case AuthRequired:
		return code, 0, authRequiredErr("IHAVE "+article.MessageID, code, line)
	case 500, 501, 502:
		return code, 0, fmt.Errorf("%w: %d %s", ErrIHaveUnsupported, code, strings.TrimSpace(line))
	default:
		c.ForceCloseConn()
		return code, 0, fmt.Errorf("unexpected IHAVE '%s' response: %d %s", article.MessageID, code, strings.TrimSpace(line))
	}

	// Send headers, separator, body (dot-stuffed, byte-exact) and terminator
	writer := bufio.NewWriter(c.conn)
	tx, werr := writeArticleLines(writer, headers, article.BodyText)
	txBytes += tx
	if werr != nil {
		c.ForceCloseConn()
		return 0, txBytes, fmt.Errorf("failed to write article for IHAVE '%s': %w", article.MessageID, werr)
	}
	if werr := writer.Flush(); werr != nil {
		c.ForceCloseConn()
		return 0, txBytes, fmt.Errorf("failed to flush article data for IHAVE '%s': %w", article.MessageID, werr)
	}

	// Second response: the peer's verdict on the transferred article.
	code, line, err = c.TextConn.ReadCodeLine(235)
	if err != nil && code == 0 {
		c.ForceCloseConn()
		return 0, txBytes, fmt.Errorf("failed to read IHAVE '%s' article response: %w", article.MessageID, err)
	}
	switch code {
	case 235, 436, 437:
		return code, txBytes, nil // accepted / deferred / rejected: the caller counts
	case AuthRequired:
		return code, txBytes, authRequiredErr("IHAVE "+article.MessageID, code, line)
	default:
		c.ForceCloseConn()
		return code, txBytes, fmt.Errorf("unexpected IHAVE '%s' response after the article: %d %s", article.MessageID, code, strings.TrimSpace(line))
	}
}

// SendTakeThisArticleStreaming IS UNSAFE! MUST BE LOCKED AND UNLOCKED OUTSIDE FOR THE WHOLE BATCH!!!
// sends TAKETHIS command and article content without waiting for response
// Returns command ID for later response reading - used for streaming mode
// Registers the command ID with the demuxer for proper response routing
// return value doContinue indicates whether the caller should continue sending more articles
func (c *BackendConn) SendTakeThisArticleStreaming(article *models.Article, nntphostname *string, newsgroup string, demuxer *ResponseDemuxer, readTAKETHISResponsesChan chan *ReadRequest, job *CHTTJob, n int, reqs int) (cmdID uint, txBytes int, err error, doContinue bool) {
	//start := time.Now()
	//c.mux.Lock()
	//defer c.mux.Unlock()

	if !c.IsConnected() {
		//c.mux.Unlock()
		return 0, 0, fmt.Errorf("not connected"), false
	}

	if c.ModeReader {
		//c.mux.Unlock()
		return 0, 0, fmt.Errorf("cannot send article in reader mode"), false
	}
	c.lastUsed = time.Now()
	//c.mux.Unlock()

	// Prepare article for transfer (see transferHeaders). Nothing is written
	// before this decision.
	var headers []string
	headers, err = transferHeaders(article, nntphostname, newsgroup)
	if err != nil {
		if errors.Is(err, common.ErrRelayHeadersInvalid) {
			log.Printf("[NNTP-CLIENT] Newsgroup: '%s' | skip TAKETHIS '%s': %v", newsgroup, article.MessageID, err)
		}
		return 0, 0, err, true
	}
	// RelayHeaders/ReconstructHeaders reject invalid Message-IDs already; this
	// guards the command argument itself (skipped, nothing written).
	if err := validMessageIDArg(article.MessageID); err != nil {
		log.Printf("[NNTP-CLIENT] Newsgroup: '%s' | skip TAKETHIS: %v", newsgroup, err)
		return 0, 0, err, true
	}
	//writer := bufio.NewWriterSize(c.conn, c.GetBufSize(article.Bytes)) // Slightly larger buffer than article size for headers
	writer := bufio.NewWriter(c.conn)

	//c.mux.Lock()
	//defer c.mux.Unlock()

	//startSend := time.Now()
	// Send TAKETHIS command
	cmdID, err = c.sendCmd("TAKETHIS %s", article.MessageID)
	if errors.Is(err, ErrInvalidCommandArg) {
		return 0, 0, err, true // refused before writing: the connection is healthy
	}
	if err != nil {
		c.ForceCloseConn() // the pipeline is broken (async: the caller holds c.mux)
		return 0, 0, fmt.Errorf("failed SendTakeThisArticleStreaming command: %w", err), false
	}

	// Send headers, separator, body (dot-stuffed, byte-exact) and terminator
	tx, err := writeArticleLines(writer, headers, article.BodyText)
	txBytes += tx
	if err != nil {
		c.ForceCloseConn()
		return 0, txBytes, fmt.Errorf("failed to write article SendTakeThisArticleStreaming: %w", err), false
	}
	//log.Printf("Newsgroup: '%s' | TAKETHIS sent CmdID=%d '%s' txBytes: %d in %v (sending took: %v) readTAKETHISResponsesChanLen=%d/%d", newsgroup, cmdID, article.MessageID, txBytes, time.Since(start), time.Since(startSend), len(readTAKETHISResponsesChan), cap(readTAKETHISResponsesChan))

	//startFlush := time.Now()
	if err := writer.Flush(); err != nil {
		c.ForceCloseConn()
		return 0, txBytes, fmt.Errorf("failed to flush article data SendTakeThisArticleStreaming: %w", err), false
	}

	//chanStart := time.Now()
	// Register command ID with demuxer as TYPE_TAKETHIS (CRITICAL: must match CHECK pattern)
	demuxer.RegisterCommand(cmdID, TYPE_TAKETHIS)

	//log.Printf("Newsgroup: '%s' | TAKETHIS flushed CmdID=%d '%s' (flushing took: %v) total time: %v readTAKETHISResponsesChan=%d/%d", newsgroup, cmdID, article.MessageID, time.Since(startFlush), time.Since(start), len(readTAKETHISResponsesChan), cap(readTAKETHISResponsesChan))
	// Queue ReadRequest IMMEDIATELY after command (like SendCheckMultiple does at line 1608)
	//readTAKETHISResponsesChan <- &ReadRequest{CmdID: cmdID, Job: job, MsgID: &article.MessageID, N: 1, Reqs: 1}
	readTAKETHISResponsesChan <- GetReadRequest(cmdID, job, &article.MessageID, n+1, reqs) // reuse global struct to reduce GC pressure
	//log.Printf("Newsgroup: '%s' | TAKETHIS notified response reader CmdID=%d '%s' waited %v readTAKETHISResponsesChan=%d/%d", newsgroup, cmdID, article.MessageID, time.Since(chanStart), len(readTAKETHISResponsesChan), cap(readTAKETHISResponsesChan))
	// Return command ID without reading response (streaming mode)
	return cmdID, txBytes, nil, true
}

// PostArticle posts an article using the POST command. It is
// PostArticleContext without a way to interrupt it.
func (c *BackendConn) PostArticle(article *models.Article) (int, error) {
	return c.PostArticleContext(context.Background(), article)
}

// PostArticleContext posts an article using the POST command. When ctx is
// done while the command runs (waiting for the 340, sending the article or
// waiting for the 240), the connection is force-closed: the raw socket is
// closed, so the blocked Read or Write returns at once instead of after the
// idle timeout, and the error wraps the context's cause. A deadline in the past
// would be re-armed by the next deadlineConn.Read; a Close is sticky. The
// connection is unusable afterwards: ForceCloseConn hands it back to its pool,
// which frees the slot (a later ForceCloseConn or Pool.Put by the caller is a
// no-op or closes it again idempotently).
//
// A stop that lands after the article was flushed but before the 240 was read
// may leave the article posted at the provider while the caller sees an error;
// posting it again later gets the provider's duplicate answer (441).
func (c *BackendConn) PostArticleContext(ctx context.Context, article *models.Article) (code int, err error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return 0, fmt.Errorf("not connected")
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("POST not sent: %w", context.Cause(ctx))
	}
	if dc := c.dconn; dc != nil {
		fired := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(fired)
			c.ForceCloseConn() // closing first: never handed out again; its goroutine waits for c.mux
			_ = dc.Conn.Close()
		})
		defer func() {
			if stop() {
				return // ctx was not done during the command
			}
			<-fired // the close ran or is running: wait, so the caller sees a closed conn
			if err != nil {
				err = fmt.Errorf("POST interrupted: %w: %w", context.Cause(ctx), err)
			}
		}()
	}
	// Prepare article for posting
	headers, err := common.ReconstructHeaders(article, false, nil, "")
	if err != nil {
		return 0, fmt.Errorf("failed to reconstruct headers: %v", err)
	}
	c.lastUsed = time.Now()

	// Send POST command
	id, err := c.sendCmd("POST")
	if err != nil {
		return 0, fmt.Errorf("failed to send POST command: %w", err)
	}

	c.TextConn.StartResponse(id)
	// Read response to POST command
	code, line, err := c.TextConn.ReadCodeLine(340)
	c.TextConn.EndResponse(id)
	if err != nil && code == 0 {
		return code, fmt.Errorf("POST command failed: %s", line)
	}
	switch code {
	case 340:
		// pass, posted

	case 401:
		// RFC 3977 §3.2.1: "401 MODE-READER" asks for MODE READER first
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "MODE-READER") {
			if err := c.SwitchMode(MODE_READER_MV); err != nil {
				return code, fmt.Errorf("POST '%s' failed. switching to reader mode failed: %w", article.MessageID, err)
			}

			// Send POST command again
			id, err := c.sendCmd("POST")
			if err != nil {
				return 0, fmt.Errorf("failed to send POST command: %w", err)
			}
			c.TextConn.StartResponse(id)
			defer c.TextConn.EndResponse(id)
			// Read response to POST command
			code, line, err = c.TextConn.ReadCodeLine(340)
			if err != nil {
				return code, fmt.Errorf("POST command failed: %s", line)
			}
			c.ModeReader = true
		}
	}

	if code != 340 {
		return code, fmt.Errorf("POST command rejected (code %d): %s", code, line)
	}

	// Send headers, separator, body (dot-stuffed, byte-exact) and terminator
	writer := bufio.NewWriter(c.conn)
	if _, err := writeArticleLines(writer, headers, article.BodyText); err != nil {
		return 0, fmt.Errorf("failed to write article: %w", err)
	}

	// Flush the writer to ensure all data is sent
	if err := writer.Flush(); err != nil {
		return 0, fmt.Errorf("failed to flush article data: %w", err)
	}

	// Read final response
	code, _, err = c.TextConn.ReadCodeLine(240)
	if err != nil {
		return code, fmt.Errorf("failed to read POST response: %w", err)
	}

	// Parse response codes
	// 240 - article posted successfully
	// 441 - posting failed
	return code, nil
}

// dotStuff returns line with an extra leading "." when it starts with "."
// (RFC 3977 §3.1.1).
func dotStuff(line string) string {
	if strings.HasPrefix(line, ".") {
		return "." + line
	}
	return line
}

// wireBodyLines splits a stored body into its wire lines: the exact inverse of
// the parser's strings.Join(lines, "\n"), so a final empty line is kept. One
// trailing "\r" per line is removed (CRLF is added on the wire). An empty body
// has no lines.
func wireBodyLines(body string) []string {
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// writeArticleLines writes an article in the POST/TAKETHIS wire format: every
// header line dot-stuffed + CRLF, an empty line, every wireBodyLines(body)
// line dot-stuffed + CRLF, then "." CRLF. It returns the bytes written into w
// (the caller flushes).
func writeArticleLines(w *bufio.Writer, headers []string, body string) (int, error) {
	total := 0
	for _, h := range headers {
		n, err := w.WriteString(dotStuff(h) + CRLF)
		total += n
		if err != nil {
			return total, fmt.Errorf("write header line: %w", err)
		}
	}
	n, err := w.WriteString(CRLF)
	total += n
	if err != nil {
		return total, fmt.Errorf("write header/body separator: %w", err)
	}
	for _, l := range wireBodyLines(body) {
		n, err := w.WriteString(dotStuff(l) + CRLF)
		total += n
		if err != nil {
			return total, fmt.Errorf("write body line: %w", err)
		}
	}
	n, err = w.WriteString(DOT + CRLF)
	total += n
	if err != nil {
		return total, fmt.Errorf("write article terminator: %w", err)
	}
	return total, nil
}

// SwitchMode switches the NNTP connection to a specific mode
// Supported modes: "reader", "stream"
func (c *BackendConn) SwitchMode(mode int) error {
	switch mode {
	case MODE_READER_MV:
		return c.SwitchToModeReader()
	case MODE_STREAM_MV:
		return c.SwitchToModeStream()
	default:
		return fmt.Errorf("unsupported mode: %d (supported: reader, stream)", mode)
	}
}

// SwitchToModeReader switches the connection to MODE READER
func (c *BackendConn) SwitchToModeReader() error {

	if c.ModeReader {
		// Already in reader mode
		return nil
	}

	c.lastUsed = time.Now()

	// Send MODE READER command
	id, err := c.sendCmd("MODE READER")
	if err != nil {
		return fmt.Errorf("failed to send MODE READER command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id)

	code, line, err := c.TextConn.ReadCodeLine(200)
	if code == 0 && err != nil {
		return fmt.Errorf("failed to read MODE READER response: %w", err)
	}

	if code < 200 || code > 201 {
		return fmt.Errorf("set MODE READER failed (code %d): %s", code, line)
	}

	c.ModeReader = true
	return nil
}

// SwitchToModeStream switches the connection to MODE STREAM
func (c *BackendConn) SwitchToModeStream() error {

	if c.ModeStream {
		// Already in stream mode
		return nil
	}
	if c.ModeReader {
		return fmt.Errorf("cannot switch from MODE READER to MODE STREAM on same connection")
	}

	c.lastUsed = time.Now()

	// Send MODE STREAM command
	id, err := c.sendCmd("MODE STREAM")
	if err != nil {
		return fmt.Errorf("failed to send MODE STREAM command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id)

	code, line, err := c.TextConn.ReadCodeLine(203)
	if err != nil && code == 0 {
		return fmt.Errorf("failed to read MODE STREAM response: %w", err)
	}

	if code == 500 || code == 501 {
		return fmt.Errorf("%w: %d %s", ErrStreamingUnsupported, code, strings.TrimSpace(line))
	}
	if code != 203 {
		return fmt.Errorf("MODE STREAM failed (code %d): %s", code, line)
	}

	c.ModeStream = true
	return nil
}
