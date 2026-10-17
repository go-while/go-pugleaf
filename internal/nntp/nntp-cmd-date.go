package nntp

import (
	"time"
)

// handleDate handles DATE (RFC 3977 §7.1): "111 yyyymmddhhmmss" in UTC. §7.1.2 asks for
// the same clock that dates articles and groups: the DB defaults (CURRENT_TIMESTAMP) of
// articles.imported_at and newsgroups.created_at are UTC from this host's clock too.
// Arguments are a syntax error (501, delayed).
func (c *ClientConnection) handleDate(args []string) error {
	if len(args) != 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error")
	}
	return c.sendResponse(111, time.Now().UTC().Format("20060102150405"))
}
