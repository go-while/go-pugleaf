package nntp

import (
	"strings"
)

// handleCapabilities responds with server capabilities
func (c *ClientConnection) handleCapabilities() error {
	return c.sendDataBlock(101, "Capability list:", c.getServerCapabilities())
}

// handleMode handles MODE READER (RFC 3977 §5.3): 200 when this client may
// post, else 201. Other variants are a syntax error (501).
func (c *ClientConnection) handleMode(args []string) error {
	if len(args) == 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "MODE requires an argument")
	}

	switch strings.ToUpper(args[0]) {
	case "READER":
		if c.postingAllowed() {
			return c.sendResponse(200, "Reader mode, posting allowed")
		}
		return c.sendResponse(201, "Reader mode, posting prohibited")
	default:
		c.rateLimitOnError()
		return c.sendResponse(501, "Unknown MODE variant")
	}
}

// handleHelp handles HELP (RFC 3977 §7.2): the commands handleCommand dispatches.
func (c *ClientConnection) handleHelp() error {
	helpLines := []string{
		"CAPABILITIES",
		"MODE READER",
		"AUTHINFO USER <name>",
		"AUTHINFO PASS <password>",
		"LIST [ACTIVE|NEWSGROUPS]",
		"GROUP <group>",
		"LISTGROUP [<group> [<range>]]",
		"STAT [<message-id>|<number>]",
		"HEAD [<message-id>|<number>]",
		"BODY [<message-id>|<number>]",
		"ARTICLE [<message-id>|<number>]",
		"XOVER [<range>]",
		"XHDR <header> [<range>|<message-id>]",
		"POST",
		"IHAVE <message-id>",
		"TAKETHIS <message-id>",
		"HELP",
		"QUIT",
	}
	return c.sendDataBlock(100, "Help text follows", helpLines)
}

// handleQuit handles QUIT (RFC 3977 §5.4): 205, then the connection loop ends
// (errQuit). Surplus arguments are ignored on purpose.
func (c *ClientConnection) handleQuit(args []string) error {
	_ = c.sendResponse(205, "Goodbye") // the session ends either way
	return errQuit
}

// Close closes the network connection. c.conn stays set, so pending reads and
// writes fail with net.ErrClosed instead of dereferencing nil.
func (c *ClientConnection) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}
