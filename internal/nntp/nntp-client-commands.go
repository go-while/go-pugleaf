package nntp

// Package nntp provides NNTP command implementations for go-pugleaf.

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

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

// StatArticle checks if an article exists on the server
func (c *BackendConn) StatArticle(messageID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return false, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.textConn.Cmd("STAT %s", messageID)
	if err != nil {
		return false, fmt.Errorf("failed to send STAT command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, _, err := c.textConn.ReadCodeLine(223)
	if err != nil {
		return false, fmt.Errorf("failed to read STAT response: %w", err)
	}

	switch code {
	case ArticleExists:
		return true, nil
	case NoSuchArticle, DMCA:
		return false, nil
	default:
		return false, fmt.Errorf("unexpected STAT response: %d", code)
	}
}

// GetArticle retrieves a complete article from the server
func (c *BackendConn) GetArticle(messageID *string, bulkmode bool) (*models.Article, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()
	/*
		// Set a per-operation timeout (10 seconds for article retrieval)
		if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return nil, fmt.Errorf("failed to set read deadline: %w", err)
		}
		defer func() {
			// Clear the deadline when operation completes
			if c.conn != nil {
				c.conn.SetReadDeadline(time.Time{})
			}
		}()
	*/
	id, err := c.textConn.Cmd("ARTICLE %s", *messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send ARTICLE '%s' command: %w", *messageID, err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(ArticleFollows)
	if err != nil && code == 0 {
		log.Printf("[ERROR] failed to read ARTICLE '%s' code=%d message='%s' err: %v", *messageID, code, message, err)
		return nil, fmt.Errorf("failed to read ARTICLE '%s' code=%d message='%s' err: %v", *messageID, code, message, err)
	}

	if code != ArticleFollows {
		switch code {
		case NoSuchArticle:
			log.Printf("[BECONN] GetArticle: not found: '%s' code=%d message='%s' err='%v'", *messageID, code, message, err)
			return nil, ErrArticleNotFound
		case DMCA:
			log.Printf("[BECONN] GetArticle: removed (DMCA): '%s' code=%d message='%s' err='%v'", *messageID, code, message, err)
			return nil, ErrArticleRemoved
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
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.textConn.Cmd("HEAD %s", messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send HEAD command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(HeadFollows)
	if err != nil {
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
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.textConn.Cmd("BODY %s", messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send BODY command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(BodyFollows)
	if err != nil {
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
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.textConn.Cmd("LIST")
	if err != nil {
		return nil, fmt.Errorf("failed to send LIST command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(215)
	if err != nil {
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
		group, err := c.parseGroupLine(line)
		if err != nil {
			continue // Skip malformed lines
		}
		groups = append(groups, group)
	}

	return groups, nil
}

// ListGroupsLimited retrieves a limited number of newsgroups for testing
func (c *BackendConn) ListGroupsLimited(maxGroups int) ([]GroupInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.textConn.Cmd("LIST")
	if err != nil {
		return nil, fmt.Errorf("failed to send LIST command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(215)
	if err != nil {
		return nil, fmt.Errorf("failed to read LIST response: %w", err)
	}

	if code != 215 {
		return nil, fmt.Errorf("unexpected LIST response: %d %s", code, message)
	}

	// Read the group list with limit
	var groups []GroupInfo
	lineCount := 0

	for {
		if lineCount >= maxGroups {
			c.textConn.Close() // Close connection on limit reached
			c = nil
			log.Printf("Connection reached maximum group limit: %d", maxGroups)
			break
		}

		line, err := c.textConn.ReadLine()
		if err != nil {
			return nil, fmt.Errorf("failed to read group list: %w", err)
		}

		// Check for end marker
		if line == "." {
			break
		}

		// Handle dot-stuffing
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}

		// Parse group information
		group, err := c.parseGroupLine(line)
		if err != nil {
			continue // Skip malformed lines
		}

		groups = append(groups, group)
		lineCount++
	}

	// Read remaining lines until end marker if we hit the limit
	if lineCount >= maxGroups {
		for {
			line, err := c.textConn.ReadLine()
			if err != nil {
				break
			}
			if line == DOT {
				break
			}
		}
	}

	return groups, nil
}

// SelectGroup selects a newsgroup for operation
func (c *BackendConn) SelectGroup(groupName string) (*GroupInfo, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, 0, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.textConn.Cmd("GROUP %s", groupName)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send GROUP '%s' command: %w", groupName, err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(211)
	if err != nil {
		return nil, code, fmt.Errorf("failed to read GROUP '%s' response: %w", groupName, err)
	}

	if code != 211 {
		return nil, code, fmt.Errorf(
			"group selection failed: expected code 211, got %d - response: %s group %s",
			code, message, groupName,
		)
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
	//log.Printf("XOver group '%s' start=%d end=%d", groupName, start, end)
	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}
	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil && code != 411 {
		return nil, fmt.Errorf("failed to select group '%s': cdeo=%d err=%w", groupName, code, err)
	}
	_ = groupInfo // groupInfo is not used further, but we keep it for clarity
	c.lastUsed = time.Now()

	// Limit to 1000 articles maximum to prevent SQLite overload (only if enforceLimit is true)
	if enforceLimit && end > 0 && (end-start+1) > MaxReadLinesXover {
		end = start + MaxReadLinesXover - 1
	}

	var id uint
	if end > 0 {
		id, err = c.textConn.Cmd("XOVER %d-%d", start, end)
	} else {
		id, err = c.textConn.Cmd("XOVER %d", start)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send XOVER command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(224)
	if err != nil {
		return nil, fmt.Errorf("failed to read XOVER response: %w", err)
	}

	if code != 224 {
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
	c.mu.Lock()
	if !c.connected {
		c.mu.Unlock()
		return nil, fmt.Errorf("not connected")
	}
	c.mu.Unlock()
	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil && code != 411 {
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
		id, err = c.textConn.Cmd("XHDR %s %d-%d", field, start, end)
	} else {
		id, err = c.textConn.Cmd("XHDR %s %d", field, start)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send XHDR command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(221)
	if err != nil {
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
		headers = append(headers, header)
	}

	return headers, nil
}

var ErrOutOfRange error = fmt.Errorf("end range exceeds group last article number")

// XHdrStreamed performs XHDR command and streams results line by line through a channel
func (c *BackendConn) XHdrStreamed(groupName, field string, start, end int64, resultChan chan<- *HeaderLine) error {
	c.mu.Lock()
	if !c.connected {
		c.mu.Unlock()
		close(resultChan)
		return fmt.Errorf("not connected")
	}
	c.mu.Unlock()

	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil && code != 411 {
		close(resultChan)
		return fmt.Errorf("failed to select group '%s': code=%d err=%w", groupName, code, err)
	}
	if end > groupInfo.Last {
		close(resultChan)
		return ErrOutOfRange
	}
	c.lastUsed = time.Now()

	// Limit to 1000 articles maximum to prevent SQLite overload
	if end > 0 && (end-start+1) > MaxReadLinesXover {
		end = start + MaxReadLinesXover - 1
	}
	//log.Printf("XHdrStreamed group '%s' field '%s' start=%d end=%d", groupName, field, start, end)

	var id uint
	if end > 0 {
		id, err = c.textConn.Cmd("XHDR %s %d-%d", field, start, end)
	} else {
		id, err = c.textConn.Cmd("XHDR %s %d-%d", field, start, start)
	}
	if err != nil {
		close(resultChan)
		return fmt.Errorf("failed to send XHDR command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	// Set timeout for initial response
	/*
		if err := c.conn.SetReadDeadline(time.Now().Add(9 * time.Second)); err != nil {
			close(resultChan)
			return fmt.Errorf("failed to set read deadline: %w", err)
		}
		defer func() {
			// Clear the deadline when operation completes
			if c.conn != nil {
				c.conn.SetReadDeadline(time.Time{})
			}
		}()
	*/
	code, message, err := c.textConn.ReadCodeLine(221)
	if err != nil {
		close(resultChan)
		return fmt.Errorf("failed to read XHDR response: %w", err)
	}

	if code != 221 {
		close(resultChan)
		return fmt.Errorf("XHDR failed: ng: '%s' %d %s", groupName, code, message)
	}

	// Read multiline response line by line and send to channel immediately
	for {
		/*
			// Set a shorter timeout for each line read (3 seconds per line)
			if err := c.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				log.Printf("[ERROR] XHdrStreamed failed to set line deadline ng: '%s' err='%v'", groupName, err)
				break
			}
		*/
		line, err := c.textConn.ReadLine()
		if err != nil {
			log.Printf("[ERROR] XHdrStreamed read error ng: '%s' err='%v'", groupName, err)
			// EOF or error, finish streaming
			break
		}

		// Check for end marker
		if line == DOT {
			break
		}

		// Parse the header line
		header, parseErr := c.parseHeaderLine(line)
		if parseErr != nil {
			log.Printf("[ERROR] XHdrStreamed parse error ng: '%s' err='%v'", groupName, parseErr)
			continue // Skip malformed lines
		}

		// Send through channel
		resultChan <- &header
	}

	// Close channel
	close(resultChan)
	return nil
}

// ListGroup retrieves article numbers for a specific group
func (c *BackendConn) ListGroup(groupName string, start, end int64) ([]int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	var id uint
	var err error
	if start > 0 && end > 0 {
		id, err = c.textConn.Cmd("LISTGROUP %s %d-%d", groupName, start, end)
	} else {
		id, err = c.textConn.Cmd("LISTGROUP %s", groupName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send LISTGROUP command: %w", err)
	}

	c.textConn.StartResponse(id)
	defer c.textConn.EndResponse(id) // Always clean up response state

	code, message, err := c.textConn.ReadCodeLine(211)
	if err != nil {
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

// readMultilineResponse reads a multi-line response ending with "."
func (c *BackendConn) readMultilineResponse(src string) ([]string, error) {
	var lines []string
	lineCount := 0
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
			c.textConn.Close() // Close connection on too many lines
			c = nil
			return nil, fmt.Errorf("too many lines in response (limit: %d)", maxReadLines)
		}

		line, err := c.textConn.ReadLine()
		if err != nil {
			return nil, err
		}

		// Check for end marker
		if line == "." {
			break
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

	// Parse article
	if bodyStart < len(lines) {
		article.BodyText = strings.Join(lines[bodyStart:], "\n")
		article.Bytes = len(article.BodyText)
		article.Lines = len(lines) - bodyStart
		if !bulkmode {
			// original body lines for peering
			article.NNTPhead = lines[:bodyStart-1]
			article.NNTPbody = lines[bodyStart:]
		}
	}
	article.Subject = getHeaderFirst(article.Headers, "subject")
	article.FromHeader = getHeaderFirst(article.Headers, "from")
	article.Path = getHeaderFirst(article.Headers, "path")
	article.References = getHeaderFirst(article.Headers, "references")
	article.RefSlice = utils.ParseReferences(article.References) // capture all references for thread chain analysis
	article.DateString = getHeaderFirst(article.Headers, "date")
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
func (c *BackendConn) parseGroupLine(line string) (GroupInfo, error) {
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
	}, nil
}

// parseOverviewLine parses a single XOVER response line
// Format: articlenum<tab>subject<tab>from<tab>date<tab>message-id<tab>references<tab>bytes<tab>lines
func (c *BackendConn) parseOverviewLine(line string) (OverviewLine, error) {
	parts := strings.Split(line, "\t")
	if len(parts) < 7 {
		return OverviewLine{}, fmt.Errorf("malformed XOVER line: %s", line)
	}

	articleNum, _ := strconv.ParseInt(parts[0], 10, 64)
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

	articleNum, _ := strconv.ParseInt(parts[0], 10, 64)

	return HeaderLine{
		ArticleNum: articleNum,
		Value:      parts[1],
	}, nil
}
