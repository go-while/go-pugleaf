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
	mode := c.server.XrefMode() // one mode for every line of the reply
	d := c.newDataBlock()
	for {
		batch := c.xrefBatchOverviews(mode, chunk) // nil: one lookup per line (or none)
		for _, o := range chunk {
			if err := d.Line(c.overviewLineBatch(o, mode, batch)); err != nil {
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
// Message-ID, References, :bytes, :lines, TAB-separated. In XrefOff there is no further
// field; in the other modes (LIST OVERVIEW.FMT lists "Xref:full") a 9th field
// "Xref: <value>" follows, empty when xref (the value, see xrefFor) is empty.
func overviewLine(o *models.Overview, mode XrefMode, xref string) string {
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
	if mode != XrefOff {
		b.WriteByte('\t')
		if xref != "" {
			b.WriteString("Xref: ")
			b.WriteString(sanitizeOverviewField(xref))
		}
	}
	return b.String()
}

// overviewLineIn is overviewLine for the row o of the current group in mode.
func (c *ClientConnection) overviewLineIn(o *models.Overview, mode XrefMode) string {
	return overviewLine(o, mode, c.xrefFor(mode, o.MessageID, c.currentGroup, o.ArticleNum))
}

// overviewLineBatch is overviewLineIn with the other Xref locations taken from batch (an
// xrefBatchOverviews result of o's chunk); a nil batch looks them up per line.
func (c *ClientConnection) overviewLineBatch(o *models.Overview, mode XrefMode, batch map[string][]XrefLocation) string {
	return overviewLine(o, mode, c.xrefForBatch(mode, o.MessageID, c.currentGroup, o.ArticleNum, batch))
}

// xrefBatchOverviews looks up the Xref locations of the rows of one OVER/XOVER range chunk
// with one xrefOthersBatch call; nil when the batched lookup does not apply (not XrefFull,
// switched off, no processor) or failed, so the lines fall back to one lookup each.
func (c *ClientConnection) xrefBatchOverviews(mode XrefMode, chunk []*models.Overview) map[string][]XrefLocation {
	if !c.xrefBatchWanted(mode) {
		return nil
	}
	ids := make([]string, 0, len(chunk))
	for _, o := range chunk {
		if o != nil && o.MessageID != "" {
			ids = append(ids, o.MessageID)
		}
	}
	return c.xrefOthersBatch(mode, ids)
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
