package nntp

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

// handleNext handles NEXT (RFC 3977 §6.1.4): move the current article to the next
// visible article of the current group.
func (c *ClientConnection) handleNext(args []string) error {
	return c.moveCurrentArticle(args, true)
}

// handleLast handles LAST (RFC 3977 §6.1.3): move the current article to the previous
// visible article of the current group.
func (c *ClientConnection) handleLast(args []string) error {
	return c.moveCurrentArticle(args, false)
}

// moveCurrentArticle implements NEXT (next = true) and LAST (next = false).
//
// Replies in check order: arguments → 501 (delayed); no group → 412 (delayed, requireGroup);
// current article invalid → 420 (delayed); then the ArticleCmdDelay throttle; a group the
// server does not carry any more → 412 (openCurrentGroupDB); group DB
// fault → 403; no next/previous visible article → 421/422 (no delay: the normal end of a
// walk); other DB error → 403; else 223 n <message-id>. The current group and article
// stay unchanged on every reply but 223 (§6.1.3.2, §6.1.4.2).
func (c *ClientConnection) moveCurrentArticle(args []string, next bool) error {
	cmd := "LAST"
	if next {
		cmd = "NEXT"
	}
	if len(args) != 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error")
	}
	if !c.requireGroup() {
		return nil
	}
	if c.currentArticle == 0 {
		c.rateLimitOnError()
		return c.sendResponse(420, "Current article number is invalid")
	}
	c.server.articleDelays.Add(1)
	if c.server.ArticleCmdDelay > 0 {
		time.Sleep(c.server.ArticleCmdDelay) // ratelimit (DefaultArticleCmdDelay)
	}

	groupDB, ok, err := c.openCurrentGroupDB(cmd)
	if !ok {
		return err
	}
	defer groupDB.Return()

	var num int64
	var msgID string
	if next {
		num, msgID, err = c.server.DB.NNTPNextArticle(groupDB, c.currentArticle)
	} else {
		num, msgID, err = c.server.DB.NNTPPrevArticle(groupDB, c.currentArticle)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if next {
				return c.sendResponse(421, "No next article in this group")
			}
			return c.sendResponse(422, "No previous article in this group")
		}
		log.Printf("[NNTP] %s in %s: %v", cmd, c.currentGroup, err)
		return c.sendResponse(403, "Internal fault")
	}
	c.currentArticle = num
	return c.sendResponse(223, fmt.Sprintf("%d %s Article found", num, msgID))
}
