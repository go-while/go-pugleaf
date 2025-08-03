package nntp

import (
	"fmt"
	"strings"
)

// handleCapabilities responds with server capabilities
func (c *ClientConnection) handleCapabilities() error {
	capabilities := c.getServerCapabilities()
	return c.sendMultilineResponse(101, "Capability list:", capabilities)
}

// handleMode handles MODE command (typically MODE READER)
func (c *ClientConnection) handleMode(args []string) error {
	if len(args) == 0 {
		return c.sendResponse(501, "MODE command requires an argument")
	}

	mode := strings.ToUpper(args[0])
	switch mode {
	case "READER":
		return c.sendResponse(200, "Hello, you can post")
	default:
		return c.sendResponse(500, fmt.Sprintf("Unknown MODE: %s", mode))
	}
}

// handleHelp handles HELP command
func (c *ClientConnection) handleHelp() error {
	helpLines := []string{
		"Commands supported:",
		"  CAPABILITIES - List server capabilities",
		"  MODE READER - Switch to reader mode",
		"  AUTHINFO USER|PASS - Authenticate",
		"  LIST [ACTIVE|NEWSGROUPS] - List groups",
		"  GROUP <group> - Select newsgroup",
		"  LISTGROUP [<group>] - List articles in group",
		"  STAT [<msgid>|<num>] - Article status",
		"  HEAD [<msgid>|<num>] - Article headers",
		"  BODY [<msgid>|<num>] - Article body",
		"  ARTICLE [<msgid>|<num>] - Full article",
		"  XOVER [<range>] - Article overview",
		"  XHDR <header> [<range>] - Header information",
		"  QUIT - Close connection",
		"",
		"For more information, see RFC 3977.",
	}
	return c.sendMultilineResponse(100, "Help text follows", helpLines)
}

// handleQuit handles QUIT command
func (c *ClientConnection) handleQuit() error {
	c.sendResponse(205, "Goodbye")
	c.Close()
	return nil
}

func (c *ClientConnection) Close() {
	// Close the connection
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	// Remove from server connections
	/*
		if c.server != nil {
			c.server.RemoveConnection(c)
		}
	*/
}
