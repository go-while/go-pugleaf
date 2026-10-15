package nntp

import (
	"fmt"
	"log"
	"strings"

	"github.com/go-while/go-pugleaf/internal/models"
)

// activeLine formats g as a LIST ACTIVE line "name high low status" (RFC 3977
// §7.6.3), exactly as handleListActive does: low is max(low_water, 1); the status is
// the group's "y" or "m" when this client may post, else "n" (decision 14). NEWGROUPS
// uses the same format (§7.3.2).
func (c *ClientConnection) activeLine(g *models.Newsgroup) string {
	low := int64(g.LowWater)
	if low < 1 {
		low = 1
	}
	status := "n"
	if c.postingAllowed() && (g.Status == "y" || g.Status == "m") {
		status = g.Status
	}
	return fmt.Sprintf("%s %d %d %s", g.Name, g.LastArticle, low, status)
}

// newsgroupsLine formats g as a LIST NEWSGROUPS line "name description" (RFC 3977
// §7.6.6), exactly as handleListNewsgroups does.
func newsgroupsLine(g *models.Newsgroup) string {
	// strings.Map (not a Replacer): invalid UTF-8 becomes U+FFFD, the same as plain
	// LIST NEWSGROUPS, so both forms print one description byte for byte alike.
	desc := strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\r', '\n':
			return ' '
		}
		return r
	}, g.Description)
	return g.Name + " " + desc
}

// handleListWildmat handles LIST ACTIVE <wildmat> and LIST NEWSGROUPS <wildmat>
// (RFC 3977 §7.6.3, §7.6.6, wildmat §4). keyword is "ACTIVE" or "NEWSGROUPS", args
// are the words after it: exactly one wildmat. The groups come from the same
// main-DB list as plain LIST ACTIVE (no group DB is opened); only the groups whose
// name matches are written, in the list's order.
func (c *ClientConnection) handleListWildmat(keyword string, args []string) error {
	kw := strings.ToUpper(keyword)
	if kw != "ACTIVE" && kw != "NEWSGROUPS" {
		// no echo of the client's keyword (A-nntp-rfc-6)
		c.rateLimitOnError()
		return c.sendResponse(501, "Unknown LIST keyword")
	}
	if len(args) != 1 {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error")
	}
	pattern := args[0]
	if !wildmatValid(pattern) {
		c.rateLimitOnError()
		return c.sendResponse(501, "Invalid wildmat")
	}
	groups, err := c.server.DB.NNTPActiveNewsgroups()
	if err != nil {
		log.Printf("[NNTP] LIST %s wildmat: %v", kw, err)
		return c.sendResponse(403, "Internal fault")
	}

	if err := c.sendResponse(215, "List of newsgroups follows"); err != nil {
		return err
	}
	d := c.newDataBlock()
	for _, g := range groups {
		if !wildmatMatch(pattern, g.Name) {
			continue
		}
		var line string
		if kw == "NEWSGROUPS" {
			line = newsgroupsLine(g)
		} else {
			line = c.activeLine(g)
		}
		if err := d.Line(line); err != nil {
			return err
		}
	}
	return d.Close()
}
