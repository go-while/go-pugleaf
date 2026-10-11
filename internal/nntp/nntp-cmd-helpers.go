package nntp

import (
	"time"
)

// Helper functions
func (c *ClientConnection) rateLimitOnError() {
	// Delay after an error response; per-server so tests can run without it.
	if c.server != nil && c.server.ErrorDelay > 0 {
		time.Sleep(c.server.ErrorDelay)
	}
}
