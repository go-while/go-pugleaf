package nntp

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// OVER, HDR, LIST OVERVIEW.FMT and LIST HEADERS (RFC 3977 §8.3-§8.6; plan nntp-rfc-commands,
// decisions D1/D2). They are thin wrappers over the XOVER/XHDR machinery: parseRangeArg,
// requireGroup, the chunked range readers, overviewLine, sanitizeOverviewField and the
// data-block writer. Ranges are read in chunks of database.OverviewRangeChunk rows and stop
// at the last row, so "n-" never walks unused numbers.

// isMessageIDArg reports whether s has the form of a message-id argument ("<...>").
func isMessageIDArg(s string) bool {
	return len(s) >= 3 && s[0] == '<' && s[len(s)-1] == '>'
}

// hdrMetadata maps an HDR metadata item to its column: ":bytes" → "bytes", ":lines" →
// "lines" (case-insensitive). Any other value starting with ':' gives ok = false
// (RFC 3977 §8.5.2: 503). A plain header name is returned unchanged with ok = true.
func hdrMetadata(field string) (column string, ok bool) {
	if !strings.HasPrefix(field, ":") {
		return field, true
	}
	switch strings.ToLower(field) {
	case ":bytes":
		return "bytes", true
	case ":lines":
		return "lines", true
	}
	return "", false
}

// overviewFmtLines returns the LIST OVERVIEW.FMT lines (RFC 3977 §8.4.2). overviewLine
// writes the Xref field in every mode but XrefOff, so "Xref:full" is listed then.
func overviewFmtLines(mode XrefMode) []string {
	lines := []string{"Subject:", "From:", "Date:", "Message-ID:", "References:", ":bytes", ":lines"}
	if mode != XrefOff {
		lines = append(lines, "Xref:full")
	}
	return lines
}

// listHeadersLines returns the LIST HEADERS lines (RFC 3977 §8.6.2): HDR serves any header
// (":") and the metadata items :bytes and :lines.
func listHeadersLines() []string {
	return []string{":", ":bytes", ":lines"}
}

// handleListOverviewFmt handles LIST OVERVIEW.FMT; args are the words after the keyword.
func (c *ClientConnection) handleListOverviewFmt(args []string) error {
	if len(args) != 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error")
	}
	return c.sendDataBlock(215, "Order of fields in overview database", overviewFmtLines(c.server.XrefMode()))
}

// handleListHeaders handles LIST HEADERS [MSGID|RANGE]; args are the words after the keyword.
// Both forms give the same list (§8.6.2: the server MAY ignore the argument).
func (c *ClientConnection) handleListHeaders(args []string) error {
	if len(args) > 1 || (len(args) == 1 && !strings.EqualFold(args[0], "MSGID") && !strings.EqualFold(args[0], "RANGE")) {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error")
	}
	return c.sendDataBlock(215, "Headers and metadata items supported", listHeadersLines())
}

// overHdrRange selects the range of the current/range forms of OVER and HDR. arg is nil for
// the current-article form. It sends the 412/420/501/423 replies itself (ok = false, err the
// send error). emptyCode is the reply when the range turns out to hold no visible article.
func (c *ClientConnection) overHdrRange(arg *string) (r articleRange, emptyCode int, ok bool, err error) {
	if !c.requireGroup() {
		return r, 0, false, nil
	}
	if arg == nil {
		if c.currentArticle == 0 {
			c.rateLimitOnError()
			return r, 0, false, c.sendResponse(420, "Current article number is invalid")
		}
		return articleRange{First: c.currentArticle, Last: c.currentArticle}, 420, true, nil
	}
	r, perr := c.parseRangeArg(*arg)
	if perr != nil {
		c.rateLimitOnError()
		return r, 0, false, c.sendResponse(501, "Syntax error in range")
	}
	if r.Empty() {
		c.rateLimitOnError()
		return r, 0, false, c.sendResponse(423, "No articles in that range")
	}
	return r, 423, true, nil
}

// sendEmptyRange answers a range without a visible article: 420 for the current-article form
// (the article is gone), 423 for a range.
func (c *ClientConnection) sendEmptyRange(emptyCode int) error {
	c.rateLimitOnError()
	if emptyCode == 420 {
		return c.sendResponse(420, "Current article number is invalid")
	}
	return c.sendResponse(423, "No articles in that range")
}

// handleOver handles OVER [range|<message-id>] (RFC 3977 §8.3).
func (c *ClientConnection) handleOver(args []string) error {
	if len(args) > 1 {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error")
	}

	if len(args) == 1 && isMessageIDArg(args[0]) {
		c.server.articleDelays.Add(1)
		if c.server.ArticleCmdDelay > 0 {
			time.Sleep(c.server.ArticleCmdDelay) // ratelimit as ARTICLE/STAT (D11)
		}
		a, num, ok, err := c.resolveArticleArg([]string{args[0]}, false)
		if !ok {
			return err // 430/403 sent
		}
		o := &models.Overview{
			ArticleNum: num, // 0 outside the current group (§8.3.2)
			Subject:    a.Subject,
			FromHeader: a.FromHeader,
			DateString: a.DateString,
			MessageID:  a.MessageID,
			References: a.References,
			Bytes:      a.Bytes,
			Lines:      a.Lines,
		}
		// num is 0 outside the current group: no serving group for the Xref field
		return c.sendDataBlock(224, "Overview information follows", []string{c.overviewLineIn(o, c.server.XrefMode())})
	}

	var arg *string
	if len(args) == 1 {
		arg = &args[0]
	}
	r, emptyCode, ok, err := c.overHdrRange(arg)
	if !ok {
		return err
	}

	groupDB, ok, err := c.openCurrentGroupDB("OVER")
	if !ok {
		return err
	}
	defer groupDB.Return()

	// First chunk before the status line: an empty range gets 423/420, a DB fault 403.
	chunk, err := c.server.DB.GetOverviewsRange(groupDB, r.First, r.Last, database.OverviewRangeChunk)
	if err != nil {
		log.Printf("[NNTP] OVER %s %d-%d: %v", c.currentGroup, r.First, r.Last, err)
		return c.sendResponse(403, "Internal fault")
	}
	if len(chunk) == 0 {
		return c.sendEmptyRange(emptyCode)
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
		if len(chunk) < database.OverviewRangeChunk || last == math.MaxInt64 || last >= r.Last {
			break
		}
		if chunk, err = c.server.DB.GetOverviewsRange(groupDB, last+1, r.Last, database.OverviewRangeChunk); err != nil {
			// The status line is out: no silent truncation, the connection closes.
			log.Printf("[NNTP] OVER %s %d-%d: %v", c.currentGroup, last+1, r.Last, err)
			return fmt.Errorf("OVER %s: %w", c.currentGroup, err)
		}
		if len(chunk) == 0 {
			break
		}
	}
	return d.Close()
}

// hdrLine formats one HDR data line: "n value", or just "n" when the value is empty.
func hdrLine(num int64, value string) string {
	line := strconv.FormatInt(num, 10)
	if v := strings.TrimSpace(sanitizeOverviewField(value)); v != "" {
		line += " " + v
	}
	return line
}

// hdrArticleValue returns the value of field for the article a found by message-id, from
// the same sources GetHeaderFieldRange uses for the range form: the overview columns for the
// 7 overview fields (and the metadata items), the path column for Path, never the upstream
// Xref (the local value is built by xrefFor in handleHdr), headers_json for every other header.
func hdrArticleValue(a *models.Article, field string) string {
	switch strings.ToLower(field) {
	case ":bytes", "bytes":
		return strconv.Itoa(a.Bytes)
	case ":lines", "lines":
		return strconv.Itoa(a.Lines)
	case "subject":
		return a.Subject
	case "from":
		return a.FromHeader
	case "date":
		return a.DateString
	case "message-id":
		return a.MessageID
	case "references":
		return a.References
	case "path":
		return a.Path
	case "xref":
		return ""
	}
	return database.HeaderValueFromJSON(a.HeadersJSON, field)
}

// headerFieldRange reads one chunk of the range form of HDR/XHDR (DB.GetHeaderFieldRange,
// database.OverviewRangeChunk rows) in the current group. For Xref in a mode other than
// XrefOff it reads the message-ids of the same rows (same hide filter and order) and builds
// the local value of each row with xrefFor from its article number: the DB layer never
// knows the hostname. In XrefOff the DB's Xref answer (always empty) is returned. In XrefFull
// with the batched lookup the other locations of the chunk come from one xrefOthersBatch
// call (one per chunk, never the whole range); without it, or when it fails, one per row.
func (c *ClientConnection) headerFieldRange(groupDB *database.GroupDB, field string, mode XrefMode, first, last int64) ([]database.HeaderFieldValue, error) {
	if !isXrefField(field) || mode == XrefOff {
		return c.server.DB.GetHeaderFieldRange(groupDB, field, first, last, database.OverviewRangeChunk)
	}
	chunk, err := c.server.DB.GetHeaderFieldRange(groupDB, "message-id", first, last, database.OverviewRangeChunk)
	if err != nil {
		return nil, err
	}
	var batch map[string][]XrefLocation
	if c.xrefBatchWanted(mode) {
		ids := make([]string, 0, len(chunk))
		for i := range chunk {
			if chunk[i].Value != "" {
				ids = append(ids, chunk[i].Value)
			}
		}
		batch = c.xrefOthersBatch(mode, ids)
	}
	for i := range chunk {
		chunk[i].Value = c.xrefForBatch(mode, chunk[i].Value, c.currentGroup, chunk[i].ArticleNum, batch)
	}
	return chunk, nil
}

// handleHdr handles HDR field [range|<message-id>] (RFC 3977 §8.5). Any header is served
// (D2); of the metadata items only :bytes and :lines (else 503).
func (c *ClientConnection) handleHdr(args []string) error {
	if len(args) == 0 || len(args) > 2 {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error")
	}
	field, ok := hdrMetadata(args[0])
	if !ok {
		c.rateLimitOnError()
		return c.sendResponse(503, "Metadata item not supported")
	}

	if len(args) == 2 && isMessageIDArg(args[1]) {
		c.server.articleDelays.Add(1)
		if c.server.ArticleCmdDelay > 0 {
			time.Sleep(c.server.ArticleCmdDelay) // ratelimit as ARTICLE/STAT (D11)
		}
		a, num, ok, err := c.resolveArticleArg([]string{args[1]}, false)
		if !ok {
			return err // 430/403 sent
		}
		value := hdrArticleValue(a, args[0])
		if isXrefField(args[0]) {
			// local Xref value; num is 0 outside the current group (no serving group)
			value = c.xrefFor(c.server.XrefMode(), a.MessageID, c.currentGroup, num)
		}
		return c.sendDataBlock(225, "Headers follow", []string{hdrLine(num, value)})
	}

	var arg *string
	if len(args) == 2 {
		arg = &args[1]
	}
	r, emptyCode, ok, err := c.overHdrRange(arg)
	if !ok {
		return err
	}

	groupDB, ok, err := c.openCurrentGroupDB("HDR")
	if !ok {
		return err
	}
	defer groupDB.Return()

	// First chunk before the status line: an empty range gets 423/420, a DB fault 403.
	mode := c.server.XrefMode() // one mode for every line of the reply
	chunk, err := c.headerFieldRange(groupDB, field, mode, r.First, r.Last)
	if err != nil {
		log.Printf("[NNTP] HDR %s %d-%d: %v", c.currentGroup, r.First, r.Last, err)
		return c.sendResponse(403, "Internal fault")
	}
	if len(chunk) == 0 {
		return c.sendEmptyRange(emptyCode)
	}

	if err := c.sendResponse(225, "Headers follow"); err != nil {
		return err
	}
	d := c.newDataBlock()
	for {
		for _, hv := range chunk {
			if err := d.Line(hdrLine(hv.ArticleNum, hv.Value)); err != nil {
				return err
			}
		}
		last := chunk[len(chunk)-1].ArticleNum
		if len(chunk) < database.OverviewRangeChunk || last == math.MaxInt64 || last >= r.Last {
			break
		}
		if chunk, err = c.headerFieldRange(groupDB, field, mode, last+1, r.Last); err != nil {
			// The status line is out: no silent truncation, the connection closes.
			log.Printf("[NNTP] HDR %s %d-%d: %v", c.currentGroup, last+1, r.Last, err)
			return fmt.Errorf("HDR %s: %w", c.currentGroup, err)
		}
		if len(chunk) == 0 {
			break
		}
	}
	return d.Close()
}
