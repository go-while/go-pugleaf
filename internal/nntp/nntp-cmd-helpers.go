package nntp

import (
	"time"
)

// Helper functions
func (c *ClientConnection) rateLimitOnError() {
	// Delay after an error response; per-server so tests can run without it.
	if c.server == nil {
		return
	}
	c.server.errorDelays.Add(1)
	if c.server.ErrorDelay > 0 {
		time.Sleep(c.server.ErrorDelay)
	}
}
