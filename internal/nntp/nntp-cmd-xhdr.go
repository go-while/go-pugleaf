package nntp

import (
	"fmt"
	"strconv"
	"strings"
)

// handleXHdr handles XHDR command
func (c *ClientConnection) handleXHdr(args []string) error {
	if c.currentGroup == "" {
		c.rateLimitOnError()
		return c.sendResponse(412, "No newsgroup selected")
	}

	if len(args) < 1 {
		c.rateLimitOnError()
		return c.sendResponse(501, "XHDR command requires header field argument")
	}

	headerField := strings.ToLower(args[0])
	var startNum, endNum int64
	var err error

	// Parse range argument
	if len(args) < 2 {
		// Use current article as single article
		if c.currentArticle == 0 {
			c.rateLimitOnError()
			return c.sendResponse(420, "Current article number is invalid")
		}
		startNum = c.currentArticle
		endNum = c.currentArticle
	} else {
		rangeArg := args[1]
		if strings.Contains(rangeArg, "-") {
			// Range format: "start-end"
			parts := strings.Split(rangeArg, "-")
			if len(parts) != 2 {
				c.rateLimitOnError()
				return c.sendResponse(501, "Invalid range format")
			}

			startNum, err = strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				c.rateLimitOnError()
				return c.sendResponse(501, "Invalid start number")
			}

			if parts[1] == "" {
				// Open-ended range: "start-"
				endNum = c.currentLast
			} else {
				endNum, err = strconv.ParseInt(parts[1], 10, 64)
				if err != nil {
					c.rateLimitOnError()
					return c.sendResponse(501, "Invalid end number")
				}
			}
		} else {
			// Single article number
			startNum, err = strconv.ParseInt(rangeArg, 10, 64)
			if err != nil {
				c.rateLimitOnError()
				return c.sendResponse(501, "Invalid article number")
			}
			endNum = startNum
		}
	}

	// Validate range
	if startNum > endNum {
		return c.sendResponse(501, "Invalid range: start greater than end")
	}

	// Get group database
	groupDBs, err := c.server.DB.GetGroupDBs(c.currentGroup)
	if err != nil {
		c.rateLimitOnError()
		return c.sendResponse(411, "No such newsgroup")
	}
	defer groupDBs.Return(c.server.DB)

	// Get header field data for the range
	headerData, err := c.server.DB.GetHeaderFieldRange(groupDBs, headerField, startNum, endNum)
	if err != nil {
		c.rateLimitOnError()
		return c.sendResponse(503, "Failed to retrieve header data")
	}

	// Send response: 221 Header follows
	if err := c.sendResponse(221, fmt.Sprintf("Header %s follows", headerField)); err != nil {
		return err
	}

	// Send header lines: "articlenum headervalue"
	for articleNum := startNum; articleNum <= endNum; articleNum++ {
		if value, exists := headerData[articleNum]; exists {
			line := fmt.Sprintf("%d %s", articleNum, value)
			if err := c.sendLine(line); err != nil {
				return err
			}
		}
	}

	// Send termination line
	return c.sendLine(DOT)
}
