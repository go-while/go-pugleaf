package nntp

import (
	"fmt"
	"time"
)

// handleGroup handles GROUP command
func (c *ClientConnection) handleGroup(args []string) error {
	time.Sleep(time.Second / 3) // ratelimit

	if len(args) == 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "GROUP command requires a group name")
	}

	// Get group info from database
	group, err := c.server.DB.MainDBGetNewsgroup(args[0])
	if err != nil {
		return c.sendResponse(411, "No such newsgroup")
	}

	if !group.Active {
		return c.sendResponse(411, "Newsgroup disabled")
	}

	// Update current group
	c.currentGroup = args[0]
	c.currentFirst = 1
	c.currentLast = group.LastArticle

	return c.sendResponse(211, fmt.Sprintf("%d %d %d %s",
		group.MessageCount, c.currentFirst, c.currentLast, args[0]))
}

// handleListGroup handles LISTGROUP command
func (c *ClientConnection) handleListGroup(args []string) error {
	groupName := c.currentGroup
	if len(args) > 0 {
		groupName = args[0]
	}

	if groupName == "" {
		c.rateLimitOnError()
		return c.sendResponse(412, "No newsgroup selected")
	}

	// Get group database
	groupDBs, err := c.server.DB.GetGroupDBs(groupName)
	if err != nil {
		c.rateLimitOnError()
		return c.sendResponse(411, "No such newsgroup")
	}
	defer groupDBs.Return(c.server.DB)

	// Get overview data to list article numbers
	overviews, err := c.server.DB.GetOverviews(groupDBs)
	if err != nil {
		c.rateLimitOnError()
		return c.sendResponse(503, "Failed to retrieve article list")
	}

	// Prepare article number list

	c.sendResponse(211, fmt.Sprintf("Article numbers follow for %s", groupName))
	for _, overview := range overviews {
		if err := c.sendLine(fmt.Sprintf("%d", overview.ArticleNum)); err != nil {
			return err
		}
	}
	return c.sendLine(DOT)
}
