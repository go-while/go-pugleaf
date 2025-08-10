package nntp

import (
	"strconv"
	"strings"
)

// handleXOver handles XOVER command
func (c *ClientConnection) handleXOver(args []string) error {
	if c.currentGroup == "" {
		c.rateLimitOnError()
		return c.sendResponse(412, "No newsgroup selected")
	}

	var startNum, endNum int64
	var err error

	// Parse range argument
	if len(args) == 0 {
		// Use current article as single article
		if c.currentArticle == 0 {
			c.rateLimitOnError()
			return c.sendResponse(420, "Current article number is invalid")
		}
		startNum = c.currentArticle
		endNum = c.currentArticle
	} else {
		rangeArg := args[0]
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
		c.rateLimitOnError()
		return c.sendResponse(501, "Invalid range: start greater than end")
	}

	// Get group database
	groupDBs, err := c.server.DB.GetGroupDBs(c.currentGroup)
	if err != nil {
		c.rateLimitOnError()
		return c.sendResponse(411, "No such newsgroup")
	}
	defer groupDBs.Return(c.server.DB)

	// Get overview data for the range
	overviews, err := c.server.DB.GetOverviewsRange(groupDBs, startNum, endNum)
	if err != nil {
		c.rateLimitOnError()
		return c.sendResponse(503, "Failed to retrieve overview data")
	}

	// Send response: 224 Overview information follows
	if err := c.sendResponse(224, "Overview information follows"); err != nil {
		return err
	}

	// Send overview lines
	for _, overview := range overviews {
		line := c.formatOverviewLine(overview)
		if err := c.sendLine(line); err != nil {
			return err
		}
	}

	// Send termination line
	return c.sendLine(DOT)
}
