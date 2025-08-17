package nntp

// Package nntp provides NNTP command implementations for go-pugleaf.

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
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
	code, _, err := c.textConn.ReadCodeLine(223)
	c.textConn.EndResponse(id)

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
func (c *BackendConn) GetArticle(messageID *string) (*models.Article, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.textConn.Cmd("ARTICLE %s", *messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send ARTICLE command: %w", err)
	}

	c.textConn.StartResponse(id)
	code, message, err := c.textConn.ReadCodeLine(ArticleFollows)
	if err != nil {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("failed to read ARTICLE response: %w", err)
	}

	if code != ArticleFollows {
		c.textConn.EndResponse(id)
		switch code {
		case NoSuchArticle:
			return nil, fmt.Errorf("article not found: %s", *messageID)
		case DMCA:
			return nil, fmt.Errorf("article removed (DMCA): %s", *messageID)
		default:
			return nil, fmt.Errorf("unexpected ARTICLE response: %d %s", code, message)
		}
	}

	// Read the article content
	lines, err := c.readMultilineResponse("article")
	c.textConn.EndResponse(id)

	if err != nil {
		return nil, fmt.Errorf("failed to read article content: %w", err)
	}

	// Parse article into headers and body
	article, err := ParseLegacyArticleLines(*messageID, lines)
	if err != nil {
		return nil, fmt.Errorf("failed to parse article: %w", err)
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
	code, message, err := c.textConn.ReadCodeLine(HeadFollows)
	if err != nil {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("failed to read HEAD response: %w", err)
	}

	if code != HeadFollows {
		c.textConn.EndResponse(id)
		switch code {
		case NoSuchArticle:
			return nil, fmt.Errorf("article not found: %s", messageID)
		case DMCA:
			return nil, fmt.Errorf("article removed (DMCA): %s", messageID)
		default:
			return nil, fmt.Errorf("unexpected HEAD response: %d %s", code, message)
		}
	}

	// Read the headers
	lines, err := c.readMultilineResponse("headers")
	c.textConn.EndResponse(id)

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
	code, message, err := c.textConn.ReadCodeLine(BodyFollows)
	if err != nil {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("failed to read BODY response: %w", err)
	}

	if code != BodyFollows {
		c.textConn.EndResponse(id)
		switch code {
		case NoSuchArticle:
			return nil, fmt.Errorf("article not found: %s", messageID)
		case DMCA:
			return nil, fmt.Errorf("article removed (DMCA): %s", messageID)
		default:
			return nil, fmt.Errorf("unexpected BODY response: %d %s", code, message)
		}
	}

	// Read the body
	lines, err := c.readMultilineResponse("body")
	c.textConn.EndResponse(id)

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
	code, message, err := c.textConn.ReadCodeLine(215)
	if err != nil {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("failed to read LIST response: %w", err)
	}

	if code != 215 {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("unexpected LIST response: %d %s", code, message)
	}

	// Read the group list
	lines, err := c.readMultilineResponse("list")
	c.textConn.EndResponse(id)

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
	code, message, err := c.textConn.ReadCodeLine(215)
	if err != nil {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("failed to read LIST response: %w", err)
	}

	if code != 215 {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("unexpected LIST response: %d %s", code, message)
	}

	// Read the group list with limit
	var groups []GroupInfo
	lineCount := 0

	for {
		if lineCount >= maxGroups {
			break
		}

		line, err := c.textConn.ReadLine()
		if err != nil {
			c.textConn.EndResponse(id)
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
			if line == "." {
				break
			}
		}
	}

	c.textConn.EndResponse(id)
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
	code, message, err := c.textConn.ReadCodeLine(211)
	c.textConn.EndResponse(id)

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
		Name:      groupName,
		Count:     count,
		First:     first,
		Last:      last,
		PostingOK: true, // Assume posting is OK unless we know otherwise
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
	code, message, err := c.textConn.ReadCodeLine(224)
	if err != nil {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("failed to read XOVER response: %w", err)
	}

	if code != 224 {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("XOVER failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("xover")
	c.textConn.EndResponse(id)

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

	if !c.connected {
		return nil, fmt.Errorf("not connected")
	}
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
	code, message, err := c.textConn.ReadCodeLine(221)
	if err != nil {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("failed to read XHDR response: %w", err)
	}

	if code != 221 {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("XHDR failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("xhdr")
	c.textConn.EndResponse(id)

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
	code, message, err := c.textConn.ReadCodeLine(211)
	if err != nil {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("failed to read LISTGROUP response: %w", err)
	}

	if code != 211 {
		c.textConn.EndResponse(id)
		return nil, fmt.Errorf("LISTGROUP failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("listgroup")
	c.textConn.EndResponse(id)

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
func ParseLegacyArticleLines(messageID string, lines []string) (*models.Article, error) {
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
		// No body separator found, treat all as headers
		bodyStart = len(lines)
	}

	// Parse headers
	headerLines := lines[:bodyStart-1]
	if err := ParseHeaders(article, headerLines); err != nil {
		return nil, err
	}

	// Store original header lines for peering
	article.NNTPhead = headerLines

	// Parse body
	if bodyStart < len(lines) {
		bodyLines := lines[bodyStart:]
		body := strings.Join(bodyLines, "\n")
		article.BodyText = body
		// Store original body lines for peering
		article.NNTPbody = bodyLines
	}
	article.Bytes = len(article.BodyText)
	return article, nil
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
		article.Headers[currentHeader] = append(article.Headers[currentHeader], headerValue)
	}

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
