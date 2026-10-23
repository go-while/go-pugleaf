package web

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
)

const APIAuthHeader = "X-API"

// AuthRequired middleware for API token authentication
func (s *WebServer) APIAuthRequired() HandlerFunc {
	return func(c *Context) {
		token := c.GetHeader(APIAuthHeader)
		if token == "" {
			c.JSON(http.StatusUnauthorized, H{"error": "header '" + APIAuthHeader + ": {token}' required"})
			c.Abort()
			return
		}

		token = strings.TrimSpace(token) // Remove any extra spaces
		if token == "" {
			c.JSON(http.StatusUnauthorized, H{"error": "Token cannot be empty"})
			c.Abort()
			return
		}

		// Validate token
		apiToken, err := s.DB.ValidateAPIToken(token)
		if err != nil || apiToken == nil {
			c.JSON(http.StatusUnauthorized, H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		// Count the request in memory; runTokenUsageFlusher writes it to the main DB
		s.recordTokenUsage(apiToken.ID, time.Now())
		c.Next()
	}
}

// tokenUsageFlushEvery is how often the buffered API token usage is written to the main DB.
// A crash loses at most this much usage counting.
const tokenUsageFlushEvery = 30 * time.Second

// recordTokenUsage counts one request for an API token in memory (see tokenUsageBuffer).
func (s *WebServer) recordTokenUsage(tokenID int64, now time.Time) {
	s.tokenUsageBuffer.mu.Lock()
	s.tokenUsageBuffer.counts[tokenID]++
	s.tokenUsageBuffer.last[tokenID] = now
	s.tokenUsageBuffer.mu.Unlock()
}

// runTokenUsageFlusher writes the buffered API token usage every tokenUsageFlushEvery and once
// more when the server shuts down. Shutdown waits for tokenUsageDone, so the last flush happens
// before the caller closes the database.
func (s *WebServer) runTokenUsageFlusher() {
	defer close(s.tokenUsageDone)
	for {
		select {
		case <-s.stopCh:
			s.flushTokenUsage()
			return
		case <-time.After(tokenUsageFlushEvery):
			s.flushTokenUsage()
		}
	}
}

// flushTokenUsage writes the buffered usage counts, one UPDATE per token. Counts that could not
// be written are added back to the buffer, so a later flush retries them. While the server is
// shutting down there may be no later flush, so a failed write is retried once after a short
// pause and, if that fails too, the lost counts are logged per token so they stay recoverable.
// The UPDATE adds to usage_count, so a concurrent flush (Shutdown flushes after the flusher has
// stopped) can only write counts twice if they were buffered twice, which the mutex prevents.
//
// While stopping, the whole flush keeps to a budget of shutdownGrace: a single RetryableExec
// gives up only after minutes of busy time, and Shutdown stops waiting for this flush after the
// same grace, so without the budget the remaining tokens would keep a pointless writer alive
// long after the database was closed.
func (s *WebServer) flushTokenUsage() {
	stopping := false
	select {
	case <-s.stopCh:
		stopping = true
	default:
	}
	var budget time.Time
	if stopping {
		budget = time.Now().Add(shutdownGrace)
	}

	s.tokenUsageBuffer.mu.Lock()
	counts, last := s.tokenUsageBuffer.counts, s.tokenUsageBuffer.last
	if len(counts) == 0 {
		s.tokenUsageBuffer.mu.Unlock()
		return
	}
	s.tokenUsageBuffer.counts = make(map[int64]int64, len(counts))
	s.tokenUsageBuffer.last = make(map[int64]time.Time, len(last))
	s.tokenUsageBuffer.mu.Unlock()

	for tokenID, count := range counts {
		var err error
		if stopping && !time.Now().Before(budget) {
			// The shutdown budget is gone: do not start another write that can block for
			// half a minute on a busy database while everything else waits to stop.
			err = context.DeadlineExceeded
		} else {
			err = s.DB.AddTokenUsage(tokenID, count, last[tokenID])
			if err != nil && stopping && time.Now().Before(budget) {
				// No periodic flush follows: give a transient busy error one more chance.
				time.Sleep(100 * time.Millisecond)
				err = s.DB.AddTokenUsage(tokenID, count, last[tokenID])
			}
		}
		if err == nil {
			continue
		}
		// Put the counts back, so a flush that still follows (Shutdown runs one after the
		// drain) picks them up, and make the possible loss visible in the log.
		s.tokenUsageBuffer.mu.Lock()
		s.tokenUsageBuffer.counts[tokenID] += count
		if t, ok := s.tokenUsageBuffer.last[tokenID]; !ok || t.Before(last[tokenID]) {
			s.tokenUsageBuffer.last[tokenID] = last[tokenID]
		}
		s.tokenUsageBuffer.mu.Unlock()
		if stopping {
			log.Printf("[API]: shutdown: usage of token %d NOT written and may be lost: %d request(s), last used %s: %v",
				tokenID, count, last[tokenID].UTC().Format("2006-01-02 15:04:05"), err)
			continue
		}
		log.Printf("[API]: Failed to update usage of token %d (%d request(s)), retrying at the next flush: %v",
			tokenID, count, err)
	}
}
