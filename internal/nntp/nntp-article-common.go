package nntp

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ArticleRetrievalType defines what content to send
type ArticleRetrievalType int

const (
	RetrievalArticle ArticleRetrievalType = iota // Head + Body
	RetrievalHead                                // Head only
	RetrievalBody                                // Body only
	RetrievalStat                                // Status only (no content)
)

// handleArticle handles ARTICLE command
func (c *ClientConnection) handleArticle(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalArticle)
}

// handleHead handles HEAD command
func (c *ClientConnection) handleHead(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalHead)
}

// handleBody handles BODY command
func (c *ClientConnection) handleBody(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalBody)
}

// handleStat handles STAT command
func (c *ClientConnection) handleStat(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalStat)
}

// retrieveArticleCommon handles the common logic for ARTICLE, HEAD, BODY, and STAT commands
func (c *ClientConnection) retrieveArticleCommon(args []string, retrievalType ArticleRetrievalType) error {
	c.server.articleDelays.Add(1)
	if c.server.ArticleCmdDelay > 0 {
		time.Sleep(c.server.ArticleCmdDelay) // ratelimit (DefaultArticleCmdDelay)
	}

	article, replyNum, ok, err := c.resolveArticleArg(args, retrievalType == RetrievalStat)
	if !ok {
		return err // the error reply has been sent
	}

	// Send appropriate response based on retrieval type
	switch retrievalType {
	case RetrievalArticle:
		return c.sendArticleContent(article, replyNum)
	case RetrievalHead:
		return c.sendHeadContent(article, replyNum)
	case RetrievalBody:
		return c.sendBodyContent(article, replyNum)
	case RetrievalStat:
		return c.sendStatContent(article, replyNum)
	default:
		return c.sendResponse(403, "Internal fault")
	}
}

// resolveArticleArg resolves the argument of ARTICLE/HEAD/BODY/STAT (RFC 3977 §6.2).
// When ok is false the reply has already been sent and err is the send error (if any).
// replyNum is the article number for the status line: the number in the current group,
// or 0 when the article was found by message-id outside it (§6.2.1.2). For stat, the
// number forms read only the message-id.
//
//   - no argument: the current article; 412 without a group, 420 when it is invalid
//     (also when it was hidden or expired since it became current).
//   - "<message-id>": the current group first, then the groups of a recentArrivals entry
//     younger than ArrivalGrace (recentArrivalArticle), then local430, then (with a processor)
//     the history lookup re-checked by visibleInOtherGroup; a miss is 430. For a recent
//     arrival local430 is neither checked nor filled (plan post-430-window). The current article
//     is never changed. Without a processor only the current group is searched. For stat
//     the group databases are read for the article number only (no headers, no body).
//   - number: 412 without a group; not 1..19 digits or above math.MaxInt64 → 501; <= 0 or
//     no visible article → 423. A hit sets the current article to the number (BUG-1).
//
// DB faults answer 403 without the error delay (decision 15).
func (c *ClientConnection) resolveArticleArg(args []string, stat bool) (article *models.Article, replyNum int64, ok bool, err error) {
	var num int64
	fromCurrent := len(args) == 0
	switch {
	case fromCurrent:
		if !c.requireGroup() {
			return nil, 0, false, nil
		}
		if c.currentArticle <= 0 {
			c.rateLimitOnError()
			return nil, 0, false, c.sendResponse(420, "Current article number is invalid")
		}
		num = c.currentArticle

	case strings.HasPrefix(args[0], "<") && strings.HasSuffix(args[0], ">"):
		messageID := args[0]
		// (a) the currently selected group: cheap and needs no history. A group the server
		// does not carry any more is skipped and the selection dropped (SEC-2 residual).
		groupDB, inGroup, gerr := c.currentGroupDBForMsgID()
		if gerr != nil {
			log.Printf("[NNTP] %s: group DB %s: %v", messageID, c.currentGroup, gerr)
		} else if inGroup {
			a, aerr := c.articleByMessageID(groupDB, messageID, stat)
			groupDB.Return()
			if aerr == nil {
				return a, a.DBArtNum, true, nil
			}
			if !errors.Is(aerr, sql.ErrNoRows) {
				log.Printf("[NNTP] %s in %s: %v", messageID, c.currentGroup, aerr)
				return nil, 0, false, c.sendResponse(403, "Internal fault")
			}
		}
		// (b) a same-process arrival of the last ArrivalGrace: its groups, read directly (the
		// history may not know it yet), and no local430 for it in either direction
		ra, _, recent, rerr := c.recentArrivalArticle(messageID, stat)
		if rerr != nil {
			log.Printf("[NNTP] lookup of recent arrival %s failed: %v", messageID, rerr)
			return nil, 0, false, c.sendResponse(403, "Internal fault")
		}
		if ra != nil {
			// found outside the current group: its article number means nothing there
			return ra, 0, true, nil
		}
		// (c) a recent definite miss, then the history index (other groups)
		if (!recent && c.server.local430.Check(messageID)) || c.server.Processor == nil {
			c.rateLimitOnError()
			return nil, 0, false, c.sendResponse(430, "No article with that message-id")
		}
		found, ferr := c.server.Processor.FindArticleByMessageID(messageID, "")
		if ferr != nil && !errors.Is(ferr, ErrArticleNotFound) {
			// history/DB fault, not a miss
			log.Printf("[NNTP] lookup of %s failed: %v", messageID, ferr)
			return nil, 0, false, c.sendResponse(403, "Internal fault")
		}
		if ferr != nil || found == nil {
			if ferr != nil && !recent {
				c.server.local430.Add(messageID) // definite miss: remember it for a while
				// an arrival noted since the check above must not be shadowed by this miss
				if _, now := c.server.recentArrivals.RecentArrival(messageID); now {
					c.server.local430.Remove(messageID)
				}
			}
			c.rateLimitOnError()
			return nil, 0, false, c.sendResponse(430, "No article with that message-id")
		}
		visible, vok, verr := c.visibleInOtherGroup(found, stat)
		if verr != nil {
			log.Printf("[NNTP] re-read of %s failed: %v", messageID, verr)
			return nil, 0, false, c.sendResponse(403, "Internal fault")
		}
		if !vok {
			c.rateLimitOnError()
			return nil, 0, false, c.sendResponse(430, "No article with that message-id")
		}
		// found in another group: its article number means nothing in the current group
		return visible, 0, true, nil

	default:
		if !c.requireGroup() {
			return nil, 0, false, nil
		}
		arg := args[0]
		valid := len(arg) > 0 && len(arg) <= 19
		for i := 0; valid && i < len(arg); i++ {
			valid = arg[i] >= '0' && arg[i] <= '9'
		}
		var perr error
		if valid {
			num, perr = strconv.ParseInt(arg, 10, 64) // fails above math.MaxInt64
		}
		if !valid || perr != nil {
			c.rateLimitOnError()
			return nil, 0, false, c.sendResponse(501, "Invalid article number")
		}
		if num <= 0 {
			c.rateLimitOnError()
			return nil, 0, false, c.sendResponse(423, "No article with that number")
		}
	}

	// number lookup in the current group (no argument or the number form); the group is
	// re-checked in the main DB before it is opened (openCurrentGroupDB: 412 when it is gone)
	groupDB, ok, err := c.openCurrentGroupDB(fmt.Sprintf("article %d", num))
	if !ok {
		return nil, 0, false, err
	}
	defer groupDB.Return()
	if stat {
		var messageID string
		messageID, err = c.server.DB.NNTPArticleMessageID(groupDB, num)
		if err == nil {
			article = &models.Article{DBArtNum: num, MessageID: messageID}
		}
	} else {
		article, err = c.server.DB.NNTPArticleByNum(groupDB, num)
	}
	if errors.Is(err, sql.ErrNoRows) {
		c.rateLimitOnError()
		if fromCurrent {
			// hidden or expired since it became current: §6.2.1.2 allows only 420 here
			return nil, 0, false, c.sendResponse(420, "Current article number is invalid")
		}
		return nil, 0, false, c.sendResponse(423, "No article with that number")
	}
	if err != nil {
		log.Printf("[NNTP] article %d in %s: %v", num, c.currentGroup, err)
		return nil, 0, false, c.sendResponse(403, "Internal fault")
	}
	c.currentArticle = num // RFC 3977 §6.2.1.2 / §6.2.4.2 (no-op for the current article)
	return article, num, true, nil
}

// recentArrivalArticle looks for messageID when it is a same-process arrival of the last
// ArrivalGrace (recentArrivals): in the groups it was sent to, read directly from their group
// DBs with the hide = 0 filter (for stat only the number), since the history index may not
// know it yet. Only names that pass selectableGroup (known and active) are opened, so a
// Newsgroups name the server does not carry never creates a group DB; the current group is
// skipped (the caller searched it). article is the visible copy and group the group it was
// read from (nil, "" when no hint has it); recent reports whether messageID is a recent
// arrival at all; err is a DB fault (not a miss). Used by ARTICLE/HEAD/BODY/STAT/OVER/HDR
// (resolveArticleArg) and XHDR by message-id (plan post-430-window).
func (c *ClientConnection) recentArrivalArticle(messageID string, stat bool) (article *models.Article, group string, recent bool, err error) {
	groups, recent := c.server.recentArrivals.RecentArrival(messageID)
	if !recent {
		return nil, "", false, nil
	}
	for _, name := range groups {
		if name == c.currentGroup {
			continue
		}
		g, ok, serr := c.selectableGroup(name)
		if serr != nil {
			return nil, "", true, fmt.Errorf("group %s: %w", name, serr)
		}
		if !ok {
			continue
		}
		groupDB, gerr := c.server.DB.GetGroupDB(g.Name)
		if gerr != nil {
			return nil, "", true, fmt.Errorf("group DB %s: %w", g.Name, gerr)
		}
		a, aerr := c.articleByMessageID(groupDB, messageID, stat)
		groupDB.Return()
		if aerr == nil {
			return a, g.Name, true, nil
		}
		if !errors.Is(aerr, sql.ErrNoRows) { // ErrNoRows: absent or hidden there, try the next hint
			return nil, "", true, fmt.Errorf("%s in %s: %w", messageID, g.Name, aerr)
		}
	}
	return nil, "", true, nil
}

// articleByMessageID reads the visible article with messageID from groupDB: the whole row,
// or for stat only its number (the returned article then carries DBArtNum and MessageID).
func (c *ClientConnection) articleByMessageID(groupDB *database.GroupDB, messageID string, stat bool) (*models.Article, error) {
	if !stat {
		return c.server.DB.NNTPArticleByMessageID(groupDB, messageID)
	}
	num, err := c.server.DB.NNTPArticleNumByMessageID(groupDB, messageID)
	if err != nil {
		return nil, err
	}
	return &models.Article{DBArtNum: num, MessageID: messageID}, nil
}

// visibleInOtherGroup re-reads an article that the processor found through the history
// index with the hide = 0 filter, in its first group (*article.NewsgroupsPtr[0]), and
// returns that fresh copy (for stat only its number and message-id); ok is false when it is
// absent or hidden there. An article without group information (a processor that does not
// set NewsgroupsPtr) cannot be re-read and is served as found unless its Hide counter is set.
//
// Known limitation: hide is per group DB, and the processor returns only the first group
// that has the message-id, so a crosspost that is hidden in that group but visible in
// another one answers 430 instead of being served from the other group.
//
// Only a group the server still carries is opened (openSelectableGroupDB), so a history
// entry naming a group that has been deactivated or deleted is a miss (ok false, 430) and
// never re-creates its group database file (SEC-2 residual).
func (c *ClientConnection) visibleInOtherGroup(article *models.Article, stat bool) (visible *models.Article, ok bool, err error) {
	if len(article.NewsgroupsPtr) == 0 || article.NewsgroupsPtr[0] == nil {
		return article, article.Hide == 0, nil
	}
	group := *article.NewsgroupsPtr[0]
	groupDB, found, err := c.openSelectableGroupDB(group)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	defer groupDB.Return()
	visible, err = c.articleByMessageID(groupDB, article.MessageID, stat)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return visible, true, nil
}

// articleHeaderLines returns the stored header lines of a (headers_json split on LF);
// empty lines are dropped, so HEAD never contains the header/body separator and an empty
// headers_json gives no lines. Dot-stuffing and CR/LF/NUL cleaning are done by
// dataBlockWriter.Line.
//
// In XrefOff the stored lines are returned unchanged. In the other modes any stored Xref:
// line (and its continuation lines) is dropped, as it would carry another server's numbers,
// and xref (the local "Xref: ..." line, see xrefHeaderLine) is appended as the last header
// line when it is not empty.
func articleHeaderLines(a *models.Article, mode XrefMode, xref string) []string {
	var out []string
	if a.HeadersJSON != "" {
		lines := strings.Split(a.HeadersJSON, "\n")
		out = lines[:0]
		inXref := false
		for _, l := range lines {
			t := strings.TrimSuffix(l, "\r")
			if t == "" {
				continue
			}
			if mode != XrefOff {
				if t[0] == ' ' || t[0] == '\t' {
					if inXref {
						continue // continuation of a dropped Xref: line
					}
				} else {
					inXref = len(t) >= 5 && strings.EqualFold(t[:5], "xref:")
					if inXref {
						continue
					}
				}
			}
			out = append(out, l)
		}
	}
	if mode != XrefOff && xref != "" {
		out = append(out, xref)
	}
	return out
}

// articleBodyLines returns the body lines of a: nil for an empty body, else the body split
// on LF (a trailing LF gives a last empty line). Dot-stuffing is done by dataBlockWriter.Line.
func articleBodyLines(a *models.Article) []string {
	if a.BodyText == "" {
		return nil
	}
	return strings.Split(a.BodyText, "\n")
}

// sendArticleContent sends full article (headers + body) for ARTICLE command
func (c *ClientConnection) sendArticleContent(article *models.Article, replyNum int64) error {
	if c == nil || c.textConn == nil {
		return fmt.Errorf("nil connection in sendArticleContent")
	}
	// 220 n message-id Article follows
	if err := c.sendResponse(220, fmt.Sprintf("%d %s Article follows", replyNum, article.MessageID)); err != nil {
		return err
	}
	d := c.newDataBlock()
	for _, line := range c.servedHeaderLines(article, replyNum) {
		if err := d.Line(line); err != nil {
			return err
		}
	}
	// blank line separating headers from body
	if err := d.Line(""); err != nil {
		return err
	}
	for _, line := range articleBodyLines(article) {
		if err := d.Line(line); err != nil {
			return err
		}
	}
	return d.Close()
}

// sendHeadContent sends only headers for HEAD command
func (c *ClientConnection) sendHeadContent(article *models.Article, replyNum int64) error {
	if c == nil || c.textConn == nil {
		return fmt.Errorf("nil connection in sendHeadContent")
	}
	// 221 n message-id Headers follow
	if err := c.sendResponse(221, fmt.Sprintf("%d %s Headers follow", replyNum, article.MessageID)); err != nil {
		return err
	}
	d := c.newDataBlock()
	for _, line := range c.servedHeaderLines(article, replyNum) {
		if err := d.Line(line); err != nil {
			return err
		}
	}
	return d.Close()
}

// servedHeaderLines returns the header lines HEAD/ARTICLE send for article: the stored
// lines plus the local Xref: line of the server's mode. replyNum > 0 is the article's
// number in the current group (by number, or by message-id inside it); 0 means it was found
// by message-id outside the current group, so there is no serving group.
func (c *ClientConnection) servedHeaderLines(article *models.Article, replyNum int64) []string {
	mode := c.server.XrefMode()
	return articleHeaderLines(article, mode, c.xrefHeaderLine(mode, article.MessageID, c.currentGroup, replyNum))
}

// sendBodyContent sends only body for BODY command
func (c *ClientConnection) sendBodyContent(article *models.Article, replyNum int64) error {
	if c == nil || c.textConn == nil {
		return fmt.Errorf("nil connection in sendBodyContent")
	}
	// 222 n message-id Body follows
	if err := c.sendResponse(222, fmt.Sprintf("%d %s Body follows", replyNum, article.MessageID)); err != nil {
		return err
	}
	d := c.newDataBlock()
	for _, line := range articleBodyLines(article) {
		if err := d.Line(line); err != nil {
			return err
		}
	}
	return d.Close()
}

// sendStatContent sends only status for STAT command
func (c *ClientConnection) sendStatContent(article *models.Article, replyNum int64) error {
	if c == nil || c.textConn == nil {
		return fmt.Errorf("nil connection in sendStatContent")
	}
	// 223 n message-id status
	return c.sendResponse(223, fmt.Sprintf("%d %s Article exists", replyNum, article.MessageID))
}
