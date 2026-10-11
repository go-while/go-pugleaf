package nntp

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"github.com/go-while/go-pugleaf/internal/database"
)

// handleXHdr handles XHDR <header> [range|message-id] (RFC 2980 §2.6, with the RFC 3977
// §8.5 codes and value rules). Any header is served: the 7 overview fields from their
// columns, the others from headers_json. An article without the header gives the line
// "<n>" (decision 3). The range is read in chunks of database.OverviewRangeChunk rows.
func (c *ClientConnection) handleXHdr(args []string) error {
	if len(args) < 1 {
		c.rateLimitOnError()
		return c.sendResponse(501, "XHDR requires a header field argument")
	}
	field := args[0]

	// message-id form: no group needed
	if len(args) >= 2 && strings.HasPrefix(args[1], "<") && strings.HasSuffix(args[1], ">") {
		return c.xhdrByMessageID(field, args[1])
	}

	if !c.requireGroup() {
		return nil
	}

	var r articleRange
	if len(args) < 2 {
		if c.currentArticle == 0 {
			c.rateLimitOnError()
			return c.sendResponse(420, "Current article number is invalid")
		}
		r = articleRange{First: c.currentArticle, Last: c.currentArticle}
	} else {
		var err error
		if r, err = c.parseRangeArg(args[1]); err != nil {
			c.rateLimitOnError()
			return c.sendResponse(501, "Syntax error in range")
		}
	}
	if r.Empty() {
		c.rateLimitOnError()
		return c.sendResponse(423, "No articles in that range")
	}

	groupDB, err := c.server.DB.GetGroupDB(c.currentGroup)
	if err != nil {
		log.Printf("[NNTP] XHDR: group DB %s: %v", c.currentGroup, err)
		return c.sendResponse(403, "Internal fault")
	}
	defer groupDB.Return()

	// First chunk before the status line: an empty range gets 423, a DB fault 403.
	chunk, err := c.server.DB.GetHeaderFieldRange(groupDB, field, r.First, r.Last, database.OverviewRangeChunk)
	if err != nil {
		log.Printf("[NNTP] XHDR %s %d-%d: %v", c.currentGroup, r.First, r.Last, err)
		return c.sendResponse(403, "Internal fault")
	}
	if len(chunk) == 0 {
		c.rateLimitOnError()
		return c.sendResponse(423, "No articles in that range")
	}

	// Echo at most 64 bytes of the field name, printable ASCII only (A-nntp-rfc-6).
	name := field
	if len(name) > 64 {
		name = name[:64]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x21 || r > 0x7e {
			return '?'
		}
		return r
	}, name)
	if err := c.sendResponse(221, name+" follows"); err != nil {
		return err
	}
	d := c.newDataBlock()
	for {
		for _, hv := range chunk {
			line := strconv.FormatInt(hv.ArticleNum, 10)
			if v := strings.TrimSpace(sanitizeOverviewField(hv.Value)); v != "" {
				line += " " + v
			}
			if err := d.Line(line); err != nil {
				return err
			}
		}
		last := chunk[len(chunk)-1].ArticleNum
		if len(chunk) < database.OverviewRangeChunk || last >= r.Last || last == math.MaxInt64 {
			break
		}
		if chunk, err = c.server.DB.GetHeaderFieldRange(groupDB, field, last+1, r.Last, database.OverviewRangeChunk); err != nil {
			// The status line is out: no silent truncation, the connection closes (decision 15).
			log.Printf("[NNTP] XHDR %s %d-%d: %v", c.currentGroup, last+1, r.Last, err)
			return fmt.Errorf("XHDR %s: %w", c.currentGroup, err)
		}
		if len(chunk) == 0 {
			break
		}
	}
	return d.Close()
}

// xhdrByMessageID answers XHDR <field> <message-id>: the current group first, then (with a
// processor) the article's group found through the history index. The value is always
// re-read with the hide = 0 filter, so a hidden article stays hidden.
func (c *ClientConnection) xhdrByMessageID(field, messageID string) error {
	lookup := func(group string) (string, bool, error) {
		groupDB, err := c.server.DB.GetGroupDB(group)
		if err != nil {
			return "", false, err
		}
		defer groupDB.Return()
		_, value, err := c.server.DB.NNTPHeaderByMessageID(groupDB, field, messageID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return value, true, nil
	}

	var value string
	found := false
	if c.currentGroup != "" {
		v, ok, err := lookup(c.currentGroup)
		if err != nil {
			log.Printf("[NNTP] XHDR %s in %s: %v", messageID, c.currentGroup, err)
			return c.sendResponse(403, "Internal fault")
		}
		value, found = v, ok
	}
	if !found && c.server.Processor != nil {
		article, err := c.server.Processor.FindArticleByMessageID(messageID, "")
		switch {
		case err == nil && article != nil && len(article.NewsgroupsPtr) > 0 && article.NewsgroupsPtr[0] != nil:
			group := *article.NewsgroupsPtr[0]
			v, ok, err := lookup(group)
			if err != nil {
				log.Printf("[NNTP] XHDR %s in %s: %v", messageID, group, err)
				return c.sendResponse(403, "Internal fault")
			}
			value, found = v, ok
		case err != nil && !errors.Is(err, ErrArticleNotFound):
			// history/DB fault, not a miss: 403 without the error delay (decision 15)
			log.Printf("[NNTP] XHDR: lookup of %s failed: %v", messageID, err)
			return c.sendResponse(403, "Internal fault")
		}
	}
	if !found {
		c.rateLimitOnError()
		return c.sendResponse(430, "No article with that message-id")
	}

	if err := c.sendResponse(221, "Header follows"); err != nil {
		return err
	}
	d := c.newDataBlock()
	line := messageID
	if v := strings.TrimSpace(sanitizeOverviewField(value)); v != "" {
		line += " " + v
	}
	if err := d.Line(line); err != nil {
		return err
	}
	return d.Close()
}
