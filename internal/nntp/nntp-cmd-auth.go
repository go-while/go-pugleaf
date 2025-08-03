package nntp

import (
	"fmt"
	"strings"
)

// handleAuthInfo handles AUTHINFO command for authentication
func (c *ClientConnection) handleAuthInfo(args []string) error {
	if len(args) < 2 {
		return c.sendResponse(501, "AUTHINFO command requires subcommand and argument")
	}

	subcommand := strings.ToUpper(args[0])
	argument := args[1]

	switch subcommand {
	case "USER":
		// Store username for next password command
		c.authUsername = argument
		return c.sendResponse(381, fmt.Sprintf("Password required for %s", argument))

	case "PASS":
		// Check if we have a username from previous USER command
		if c.authUsername == "" {
			return c.sendResponse(482, "AUTHINFO USER required first")
		}

		// Authenticate the user with the stored username and provided password
		user, err := c.server.AuthManager.AuthenticateUser(c.authUsername, argument)
		if err != nil {
			c.server.Stats.AuthFailure()
			c.authUsername = "" // Clear stored username
			return c.sendResponse(481, "Authentication failed")
		}

		// Authentication successful
		c.authenticated = true
		c.user = user
		c.authUsername = "" // Clear stored username
		c.server.Stats.AuthSuccess()
		return c.sendResponse(281, fmt.Sprintf("Authentication accepted for user %s", user.Username))

	default:
		return c.sendResponse(500, fmt.Sprintf("Unknown AUTHINFO subcommand: %s", subcommand))
	}
}
