package nntp

import (
	"log"
	"strings"
)

// authinfoArgument returns the raw rest of an AUTHINFO line after "AUTHINFO",
// the subcommand and one run of SP/TAB. Spaces inside and at the end are kept
// (RFC 4643 §4: a password may contain SP).
func authinfoArgument(line string) string {
	isWS := func(b byte) bool { return b == ' ' || b == '\t' }
	i := 0
	for word := 0; word < 2; word++ {
		for i < len(line) && isWS(line[i]) {
			i++
		}
		for i < len(line) && !isWS(line[i]) {
			i++
		}
	}
	for i < len(line) && isWS(line[i]) {
		i++
	}
	return line[i:]
}

// handleAuthInfo handles AUTHINFO USER/PASS (RFC 4643 §2.3). No reply echoes
// client input.
func (c *ClientConnection) handleAuthInfo(args []string, line string) error {
	if len(args) == 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "AUTHINFO requires a subcommand")
	}
	// RFC 4643 §2.3.2: any AUTHINFO after a successful login → 502
	if c.authenticated {
		return c.sendResponse(502, "Already authenticated")
	}
	if !c.isTLS && c.server.PlaintextAuth == PlaintextAuthRefuse {
		return c.sendResponse(483, "Encryption required")
	}

	subcommand := strings.ToUpper(args[0])
	if subcommand != "USER" && subcommand != "PASS" {
		c.rateLimitOnError()
		return c.sendResponse(501, "Unknown AUTHINFO subcommand")
	}
	argument := authinfoArgument(line)
	if argument == "" {
		c.rateLimitOnError()
		return c.sendResponse(501, "AUTHINFO USER and PASS require an argument")
	}

	if subcommand == "USER" {
		// Store username for next password command
		c.authUsername = argument
		return c.sendResponse(381, "Password required")
	}

	// PASS: needs a preceding USER
	if c.authUsername == "" {
		c.rateLimitOnError()
		return c.sendResponse(482, "AUTHINFO USER required first")
	}
	username := c.authUsername
	c.authUsername = ""

	user, err := c.server.AuthManager.AuthenticateUser(username, argument)
	if err != nil {
		c.server.Stats.AuthFailure()
		c.rateLimitOnError()
		return c.sendResponse(481, "Authentication failed")
	}
	if !c.server.acquireUserSlot(user) {
		log.Printf("[NNTP-AUTH] user %s refused: connection limit %d reached", user.Username, user.MaxConns)
		c.rateLimitOnError()
		return c.sendResponse(481, "Too many connections for this user")
	}

	c.authenticated = true
	c.user = user
	c.server.Stats.AuthSuccess()
	return c.sendResponse(281, "Authentication accepted")
}
