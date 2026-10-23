package nntp

import (
	"log"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/history"
)

// streamingAllowed reports whether this client may use streaming (RFC 4644 §2.2,
// plan D5): a processor is configured and the client is not an authenticated user
// without posting rights (exactly postingAllowed). It is the single condition for
// the STREAMING capability and for the 203 reply to MODE STREAM, so STREAMING is
// listed iff MODE STREAM gives 203.
func (c *ClientConnection) streamingAllowed() bool {
	return c.postingAllowed()
}

// handleModeStream handles MODE STREAM (RFC 4644 §2.3); args are the words after
// STREAM. It changes no state (§2.3.2): 203 when streaming is allowed, 501 without
// a processor or with surplus arguments, 502 for a user without posting rights.
func (c *ClientConnection) handleModeStream(args []string) error {
	if len(args) != 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error")
	}
	if c.server.Processor == nil {
		c.rateLimitOnError()
		return c.sendResponse(501, "MODE STREAM not supported")
	}
	if !c.streamingAllowed() {
		return c.sendResponse(502, "Streaming not permitted")
	}
	return c.sendResponse(203, "Streaming permitted")
}

// handleCheck handles CHECK (RFC 4644 §2.4) in the gate order of IHAVE: processor,
// argument count, authentication, posting permission, message-id syntax, then one
// history lookup. Every reply except the 501 carries the message-id when exactly
// one argument was given, so a streaming peer can match pipelined replies by id.
// Only 500 and 501 are delayed; 238/431/438/480/502 are normal streaming answers.
// CHECK never reads from the connection.
func (c *ClientConnection) handleCheck(args []string) error {
	id := ""
	if len(args) == 1 {
		id = args[0] + " "
	}
	if c.server.Processor == nil {
		c.rateLimitOnError()
		return c.sendResponse(500, id+"CHECK not supported on this server")
	}
	if len(args) != 1 {
		c.rateLimitOnError()
		return c.sendResponse(501, "CHECK requires exactly one message-id")
	}
	if !c.authenticated {
		return c.sendResponse(480, id+"Transfer permission denied")
	}
	if c.user != nil && !c.user.Posting {
		return c.sendResponse(502, id+"Transfer not permitted")
	}
	messageID := args[0]
	if !common.ValidMessageID(messageID) {
		return c.sendResponse(438, id+"Rejected (invalid message-id)")
	}

	switch c.server.Processor.CheckMessageID(messageID) {
	case history.CasePass:
		return c.sendResponse(238, id+"Send article")
	case history.CaseDupes:
		return c.sendResponse(438, id+"Not wanted")
	case history.CaseRetry:
		return c.sendResponse(431, id+"Retry later")
	default: // history.CaseError: temporary history trouble
		log.Printf("[NNTP] CHECK history error for %s", messageID)
		return c.sendResponse(431, id+"Retry later")
	}
}
