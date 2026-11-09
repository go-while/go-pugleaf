package nntp

import (
	"log"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/history"
)

// streamingAllowed reports whether this client may use streaming (RFC 4644 §2.2,
// plan D5): a processor is configured and the connection is a peer connection
// (exactly feedAllowed, plan nntp-peering). It is the single condition for the
// STREAMING capability and for the 203 reply to MODE STREAM, so STREAMING is
// listed iff MODE STREAM gives 203.
func (c *ClientConnection) streamingAllowed() bool {
	return c.feedAllowed()
}

// handleModeStream handles MODE STREAM (RFC 4644 §2.3); args are the words after
// STREAM. It changes no state (§2.3.2): 203 for a peer, 501 without a processor or
// with surplus arguments, 480/502 for everyone else (plan nntp-peering: 480 while a
// peer login could still help).
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
		code := c.feedDeniedCode()
		c.peerStatsDenied(code) // a feed command from no (enabled) peer, plan peering-stats
		if code == 480 {
			return c.sendResponse(480, "Streaming not permitted (authentication required)")
		}
		return c.sendResponse(502, "Streaming not permitted (peers only)")
	}
	return c.sendResponse(203, "Streaming permitted")
}

// handleCheck handles CHECK (RFC 4644 §2.4) in the gate order of IHAVE: processor,
// argument count, peer permission (plan nntp-peering), message-id syntax, then one
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
	if !c.feedAllowed() {
		code := c.feedDeniedCode()
		c.peerStatsDenied(code) // plan peering-stats
		if code == 480 {
			return c.sendResponse(480, id+"Transfer permission denied")
		}
		return c.sendResponse(502, id+"Transfer not permitted (peers only)")
	}
	messageID := args[0]
	if !common.ValidMessageID(messageID) {
		// An offer the peer cannot repeat with TAKETHIS: the id is not remembered.
		c.peerStatsAdd(peerFeedCounters(c.peerStatsOffer(true, ""), 438, 0, false))
		return c.sendResponse(438, id+"Rejected (invalid message-id)")
	}
	// A CHECK is one offer; the TAKETHIS of the same id does not count a second one.
	offer := c.peerStatsOffer(true, messageID)

	switch c.server.Processor.CheckMessageID(messageID) {
	case history.CasePass:
		c.peerStatsAdd(peerFeedCounters(offer, 238, 0, false))
		return c.sendResponse(238, id+"Send article")
	case history.CaseDupes:
		c.peerStatsAdd(peerFeedCounters(offer, 438, 0, false))
		return c.sendResponse(438, id+"Not wanted")
	case history.CaseRetry:
		c.peerStatsAdd(peerFeedCounters(offer, 431, 0, false))
		return c.sendResponse(431, id+"Retry later")
	default: // history.CaseError: temporary history trouble
		log.Printf("[NNTP] CHECK history error for %s", messageID)
		c.peerStatsAdd(peerFeedCounters(offer, 431, 0, false))
		return c.sendResponse(431, id+"Retry later")
	}
}
