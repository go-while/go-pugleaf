package nntp

import (
	"fmt"
	"log"
	"strings"
)

// handleList handles LIST [ACTIVE|NEWSGROUPS [wildmat]] (RFC 3977 §7.6). The wildmat is
// ignored for now (RFC-1, plan nntp-rfc-commands).
func (c *ClientConnection) handleList(args []string) error {
	listType := "ACTIVE"
	if len(args) > 0 {
		listType = strings.ToUpper(args[0])
	}

	switch listType {
	case "ACTIVE":
		return c.handleListActive()
	case "NEWSGROUPS":
		return c.handleListNewsgroups()
	default:
		// no echo of the client's keyword (A-nntp-rfc-6)
		c.rateLimitOnError()
		return c.sendResponse(501, "Unknown LIST keyword")
	}
}

// handleListActive lists the active newsgroups as "name high low status" (RFC 3977 §7.6.3)
// from the main DB only (no group DB is opened). low is max(low_water, 1); the status is
// the group's "y" or "m" when this client may post, else "n" (decision 14).
func (c *ClientConnection) handleListActive() error {
	groups, err := c.server.DB.NNTPActiveNewsgroups()
	if err != nil {
		log.Printf("[NNTP] LIST ACTIVE: %v", err)
		return c.sendResponse(403, "Internal fault")
	}
	posting := c.postingAllowed()

	if err := c.sendResponse(215, "List of newsgroups follows"); err != nil {
		return err
	}
	d := c.newDataBlock()
	for _, g := range groups {
		low := int64(g.LowWater)
		if low < 1 {
			low = 1
		}
		status := "n"
		if posting && (g.Status == "y" || g.Status == "m") {
			status = g.Status
		}
		if err := d.Line(fmt.Sprintf("%s %d %d %s", g.Name, g.LastArticle, low, status)); err != nil {
			return err
		}
	}
	return d.Close()
}

// handleListNewsgroups lists the active newsgroups as "name description" (RFC 3977 §7.6.6);
// TAB, CR and LF in a description become spaces, so each group is one line.
func (c *ClientConnection) handleListNewsgroups() error {
	groups, err := c.server.DB.NNTPActiveNewsgroups()
	if err != nil {
		log.Printf("[NNTP] LIST NEWSGROUPS: %v", err)
		return c.sendResponse(403, "Internal fault")
	}

	if err := c.sendResponse(215, "List of newsgroups follows"); err != nil {
		return err
	}
	d := c.newDataBlock()
	for _, g := range groups {
		desc := strings.Map(func(r rune) rune {
			switch r {
			case '\t', '\r', '\n':
				return ' '
			}
			return r
		}, g.Description)
		if err := d.Line(g.Name + " " + desc); err != nil {
			return err
		}
	}
	return d.Close()
}
