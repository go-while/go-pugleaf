package nntp

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// handleXOver handles XOVER [range] (RFC 2980 §2.8, with the RFC 3977 §8.3 codes).
// The range is read in chunks of database.OverviewRangeChunk rows, so a huge or open
// range costs one query per chunk of existing articles and never walks unused numbers.
func (c *ClientConnection) handleXOver(args []string) error {
	if !c.requireGroup() {
		return nil
	}

	var r articleRange
	if len(args) == 0 {
		if c.currentArticle == 0 {
			c.rateLimitOnError()
			return c.sendResponse(420, "Current article number is invalid")
		}
		r = articleRange{First: c.currentArticle, Last: c.currentArticle}
	} else {
		var err error
		if r, err = c.parseRangeArg(args[0]); err != nil {
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
		log.Printf("[NNTP] XOVER: group DB %s: %v", c.currentGroup, err)
		return c.sendResponse(403, "Internal fault")
	}
	defer groupDB.Return()

	// First chunk before the status line: an empty range gets 423, a DB fault 403.
	chunk, err := c.server.DB.GetOverviewsRange(groupDB, r.First, r.Last, database.OverviewRangeChunk)
	if err != nil {
		log.Printf("[NNTP] XOVER %s %d-%d: %v", c.currentGroup, r.First, r.Last, err)
		return c.sendResponse(403, "Internal fault")
	}
	if len(chunk) == 0 {
		c.rateLimitOnError()
		return c.sendResponse(423, "No articles in that range")
	}

	if err := c.sendResponse(224, "Overview information follows"); err != nil {
		return err
	}
	d := c.newDataBlock()
	for {
		for _, o := range chunk {
			if err := d.Line(overviewLine(o)); err != nil {
				return err
			}
		}
		last := chunk[len(chunk)-1].ArticleNum
		if len(chunk) < database.OverviewRangeChunk || last >= r.Last || last == math.MaxInt64 {
			break
		}
		if chunk, err = c.server.DB.GetOverviewsRange(groupDB, last+1, r.Last, database.OverviewRangeChunk); err != nil {
			// The status line is out: no silent truncation, the connection closes (decision 15).
			log.Printf("[NNTP] XOVER %s %d-%d: %v", c.currentGroup, last+1, r.Last, err)
			return fmt.Errorf("XOVER %s: %w", c.currentGroup, err)
		}
		if len(chunk) == 0 {
			break
		}
	}
	return d.Close()
}

// overviewLine formats one overview line (RFC 3977 §8.3.2): number, Subject, From, Date,
// Message-ID, References, :bytes, :lines, TAB-separated, without a trailing field.
func overviewLine(o *models.Overview) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(o.ArticleNum, 10))
	for _, f := range []string{o.Subject, o.FromHeader, o.DateString, o.MessageID, o.References} {
		b.WriteByte('\t')
		b.WriteString(sanitizeOverviewField(f))
	}
	b.WriteByte('\t')
	b.WriteString(strconv.Itoa(o.Bytes))
	b.WriteByte('\t')
	b.WriteString(strconv.Itoa(o.Lines))
	return b.String()
}

// sanitizeOverviewField prepares a header value for an overview or XHDR line
// (RFC 3977 §8.3.2): CRLF pairs are removed, then each TAB, CR, LF and NUL becomes one space.
func sanitizeOverviewField(s string) string {
	if !strings.ContainsAny(s, "\t\r\n\x00") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "")
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\r', '\n', 0:
			return ' '
		}
		return r
	}, s)
}
