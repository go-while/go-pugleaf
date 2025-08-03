package nntp

import (
	"fmt"
	"strings"
)

// handleList handles LIST command
func (c *ClientConnection) handleList(args []string) error {
	listType := "ACTIVE"
	if len(args) > 0 {
		listType = strings.ToUpper(args[0])
	}

	switch listType {
	case "ACTIVE":
		return c.handleListActive()
	case "NEWSGROUPS":
		return c.handleListNewsgroups()
	default:
		return c.sendResponse(501, fmt.Sprintf("Unknown LIST type: %s", listType))
	}
}

// handleListActive lists active newsgroups
func (c *ClientConnection) handleListActive() error {
	// Get groups from database
	groups, err := c.server.DB.GetActiveNewsgroups()
	if err != nil {
		return c.sendResponse(503, "Failed to retrieve group list")
	}

	c.sendResponse(215, "List of newsgroups follows")
	for _, group := range groups {
		if group.Active {
			posting := "n" // no posting allowed by default
			if err := c.sendLine(fmt.Sprintf("%s %d %d %s", group.Name, group.LastArticle, 1, posting)); err != nil {
				return err
			}
		}
	}
	return c.sendLine(DOT)
}

// handleListNewsgroups lists newsgroups with descriptions
func (c *ClientConnection) handleListNewsgroups() error {
	// Get groups from database
	groups, err := c.server.DB.GetActiveNewsgroups()
	if err != nil {
		return c.sendResponse(503, "Failed to retrieve group list")
	}

	c.sendResponse(215, "List of newsgroups follows")
	for _, group := range groups {
		if group.Active {
			if err := c.sendLine(fmt.Sprintf("%s %s", group.Name, group.Description)); err != nil {
				return err
			}
		}
	}
	return c.sendLine(DOT)
}
