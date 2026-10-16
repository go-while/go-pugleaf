package nntp

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// PostDateMaxAge is the oldest Date a POST may carry (RFC 5537 §3.5 step 3: at least 72 hours).
// Feeds (IHAVE/TAKETHIS) and the fetcher have no past cutoff.
var PostDateMaxAge = 72 * time.Hour

// closeDrainTimeout bounds how long closeAfterReply reads and discards client input
// (at most 1 MiB) before it closes the connection.
const closeDrainTimeout = time.Second

// intakeKind selects the reply texts of intakeReply.
type intakeKind int

const (
	intakePost intakeKind = iota
	intakeIHave
	intakeTakeThis
)

// handlePost handles the POST command (RFC 3977 §6.3.1).
func (c *ClientConnection) handlePost() error {
	// RFC 3977 §6.3.1.2: 440 posting not permitted
	if c.server.Processor == nil {
		return c.sendResponse(440, "Posting not permitted (no article processor)")
	}

	// Check if user is authenticated
	if !c.authenticated {
		return c.sendResponse(480, "Authentication required for posting")
	}

	// Check if user has posting permission
	if c.user != nil && !c.user.Posting {
		return c.sendResponse(440, "Posting not permitted for this user")
	}

	// Start article reception
	if err := c.sendResponse(340, "Send article to be posted. End with <CR-LF>.<CR-LF>"); err != nil {
		return err
	}

	head, body, err := c.readArticleBlock(c.server.MaxArticleBytes())
	switch {
	case errors.Is(err, ErrArticleDrainLimit):
		log.Printf("[NNTP] POST: article too large to drain, closing the connection")
		return c.closeAfterReply(400, "Article too large, closing connection")
	case errors.Is(err, ErrArticleTooLarge):
		// the article was read up to its terminator: the stream is in sync
		c.rateLimitOnError()
		return c.sendResponse(441, "Posting failed (article too large)")
	case err != nil:
		return err // I/O error: nothing can be written
	}

	head, err = c.injectProtoArticle(head)
	if err != nil {
		log.Printf("[NNTP] POST refused: %v", err)
		c.rateLimitOnError()
		reply, text, _ := intakeReply(intakePost, history.CaseError, err, "")
		return c.sendResponse(reply, text)
	}

	article, err := c.buildIncomingArticle(head, body)
	if err != nil {
		log.Printf("[NNTP] POST: invalid article: %v", err)
		c.rateLimitOnError()
		return c.sendResponse(441, "Posting failed (invalid article)")
	}
	article.Posted = true // injected here: the processor applies the POST-only group policy

	// Copy the id and the groups before the hand-off: once the processor returns, the batch
	// goroutine may already have cleared the struct (db_batch.go processNewsgroupBatch),
	// so reading article.MessageID afterwards is a data race and can yield "".
	messageID := article.MessageID
	groups := articleGroupNames(article)
	// 240 only once the batch writer committed the article (at most PostCommitWait), so the
	// poster can read it back at once (plan post-430-window)
	code, perr := c.server.Processor.ProcessPostedArticle(article, c.server.shutdown, c.server.PostCommitWait())
	reply, text, closeConn := intakeReply(intakePost, code, perr, messageID)
	if reply == 240 {
		c.server.noteArrival(messageID, groups)
	}
	if closeConn {
		return c.closeAfterReply(reply, text)
	}
	return c.sendResponse(reply, text)
}

// handleIHave handles the IHAVE command (RFC 3977 §6.3.2).
func (c *ClientConnection) handleIHave(args []string) error {
	// IHAVE is not offered without a processor (RFC 3977 §3.2.1: 500)
	if c.server.Processor == nil {
		return c.sendResponse(500, "IHAVE not supported on this server")
	}

	if len(args) != 1 {
		return c.sendResponse(501, "IHAVE command requires exactly one argument (message-ID)")
	}

	// Check authentication and permission before requesting the article
	if !c.authenticated {
		c.rateLimitOnError()
		return c.sendResponse(480, "Transfer permission denied (authentication required)")
	}
	if c.user != nil && !c.user.Posting {
		c.rateLimitOnError()
		return c.sendResponse(502, "Transfer not permitted for this user")
	}
	messageID := args[0]
	// RFC 3977 §3.2.1, §3.6: a malformed message-id argument is a syntax error
	if !common.ValidMessageID(messageID) {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error in message-id")
	}

	// Check if we want this article
	switch c.server.Processor.CheckMessageID(messageID) {
	case history.CasePass:
		// wanted
	case history.CaseDupes:
		return c.sendResponse(435, "Not wanted")
	case history.CaseRetry:
		// another connection is transferring it right now
		return c.sendResponse(436, "Retry later")
	default: // history.CaseError
		log.Printf("Error checking article history for %s", messageID)
		c.rateLimitOnError()
		return c.sendResponse(436, "Retry later (history error)")
	}

	// Request the article
	if err := c.sendResponse(335, "Send me"); err != nil {
		return err
	}

	head, body, err := c.readArticleBlock(c.server.MaxArticleBytes())
	switch {
	case errors.Is(err, ErrArticleDrainLimit):
		log.Printf("[NNTP] IHAVE %s: article too large to drain, closing the connection", messageID)
		return c.closeAfterReply(400, "Article too large, closing connection")
	case errors.Is(err, ErrArticleTooLarge):
		c.rateLimitOnError()
		return c.sendResponse(437, "Rejected (article too large)")
	case err != nil:
		return err // I/O error: nothing can be written
	}
	head = c.relayPath(head)

	article, err := c.buildIncomingArticle(head, body)
	if err != nil {
		log.Printf("[NNTP] IHAVE %s: invalid article: %v", messageID, err)
		c.rateLimitOnError()
		return c.sendResponse(437, "Rejected (invalid article)")
	}
	if !reconcileMessageID(article, messageID) {
		log.Printf("IHAVE message-id mismatch: command '%s' article '%s'", messageID, article.MessageID)
		return c.sendResponse(437, "Rejected (Message-ID mismatch)")
	}

	groups := articleGroupNames(article) // before the hand-off, see handlePost
	code, perr := c.server.Processor.ProcessIncomingArticle(article)
	reply, text, closeConn := intakeReply(intakeIHave, code, perr, messageID)
	if reply == 235 {
		c.server.noteArrival(messageID, groups)
	}
	if closeConn {
		return c.closeAfterReply(reply, text)
	}
	return c.sendResponse(reply, text)
}

// articleGroupNames returns the names behind article.NewsgroupsPtr (the Newsgroups header as
// received, syntactically valid names only) as a new slice; nil pointers are skipped.
func articleGroupNames(article *models.Article) []string {
	names := make([]string, 0, len(article.NewsgroupsPtr))
	for _, p := range article.NewsgroupsPtr {
		if p != nil && *p != "" {
			names = append(names, *p)
		}
	}
	return names
}

// noteArrival records an article this process accepted (240/235/239): the id leaves
// local430 (BUG-8: the article exists now) and enters recentArrivals with its groups, so
// lookups during ArrivalGrace try those groups and cache no miss (plan post-430-window).
func (s *NNTPServer) noteArrival(messageID string, groups []string) {
	if s.local430 != nil {
		s.local430.Remove(messageID)
	}
	s.recentArrivals.NoteArrival(messageID, groups)
}

// reconcileMessageID reports whether the article's Message-ID header equals the
// message-id of the IHAVE/TAKETHIS command. An article without a Message-ID header
// is a mismatch (RFC 5537 §3.6 step 1: a relaying agent must not invent one).
func reconcileMessageID(article *models.Article, cmdMessageID string) bool {
	return article.MessageID == cmdMessageID
}

// handleTakeThis handles the TAKETHIS command for streaming article transfer (RFC 4644 §2.5).
// Every reply carries the message-id argument.
func (c *ClientConnection) handleTakeThis(args []string) error {
	id := ""
	if len(args) > 0 {
		id = args[0]
	}
	allowed := c.server.Processor != nil && len(args) == 1 && c.authenticated &&
		(c.user == nil || c.user.Posting) && common.ValidMessageID(id)

	// Streaming mode: the client sends the article without waiting, so it is always
	// consumed (bounded) before the reply, also for the early error replies.
	head, body, err := c.readArticleBlock(c.server.MaxArticleBytes())
	if !allowed {
		head, body = nil, nil // never keep an article this client may not send
	}
	reply := func(code int, text string) error {
		if id == "" {
			return c.sendResponse(code, text)
		}
		return c.sendResponse(code, id+" "+text)
	}
	tooLarge := errors.Is(err, ErrArticleTooLarge)
	switch {
	case errors.Is(err, ErrArticleDrainLimit):
		log.Printf("[NNTP] TAKETHIS %s: article too large to drain, closing the connection", id)
		return c.closeAfterReply(400, "Article too large, closing connection")
	case err != nil && !tooLarge:
		return err // I/O error: nothing can be written
	}

	// TAKETHIS is not offered without a processor (RFC 3977 §3.2.1: 500)
	if c.server.Processor == nil {
		return reply(500, "TAKETHIS not supported on this server")
	}
	if len(args) != 1 {
		return reply(501, "TAKETHIS requires exactly one message-id")
	}
	// Check authentication and permission (exactly one response per TAKETHIS)
	if !c.authenticated {
		c.rateLimitOnError()
		return reply(480, "Transfer permission denied")
	}
	if c.user != nil && !c.user.Posting {
		c.rateLimitOnError()
		return reply(502, "Transfer not permitted")
	}
	if !common.ValidMessageID(id) {
		c.rateLimitOnError()
		return reply(439, "Rejected (invalid message-id)")
	}
	if tooLarge {
		c.rateLimitOnError()
		return reply(439, "Rejected (article too large)")
	}

	// Check if we want this article
	switch c.server.Processor.CheckMessageID(id) {
	case history.CasePass:
		// wanted
	case history.CaseDupes:
		return reply(439, "Not wanted")
	case history.CaseRetry:
		return reply(439, "In progress")
	default: // history.CaseError: RFC 4644 §2.5.2, a temporary failure is 400 and a close
		log.Printf("[NNTP] TAKETHIS %s: error checking article history, closing", id)
		return c.closeAfterReply(400, "Service temporarily unavailable")
	}
	head = c.relayPath(head)

	article, err := c.buildIncomingArticle(head, body)
	if err != nil {
		log.Printf("Failed to parse TAKETHIS article data: %v", err)
		return reply(439, "Rejected (invalid article)")
	}
	if !reconcileMessageID(article, id) {
		log.Printf("TAKETHIS message-id mismatch: command '%s' article '%s'", id, article.MessageID)
		return reply(439, "Transfer failed (Message-ID mismatch)")
	}

	groups := articleGroupNames(article) // before the hand-off, see handlePost
	code, perr := c.server.Processor.ProcessIncomingArticle(article)
	r, text, closeConn := intakeReply(intakeTakeThis, code, perr, id)
	if r == 239 {
		c.server.noteArrival(id, groups)
	}
	if closeConn {
		return c.closeAfterReply(r, text)
	}
	return c.sendResponse(r, text)
}

// intakeReply maps a ProcessIncomingArticle result to the reply of a POST, IHAVE
// (after 335) or TAKETHIS (contract K4 of plan nntp-intake-injection). The reason of
// a rejection is the text after the ErrIntakeRejected prefix (a fixed string set by the
// producer, never client input); closeConn asks the caller to close the connection
// after the reply (TAKETHIS temporary failures, RFC 4644 §2.5.2).
func intakeReply(kind intakeKind, code int, err error, messageID string) (reply int, text string, closeConn bool) {
	// rejected builds the per-kind rejection reply for reason
	rejected := func(reason string) (int, string, bool) {
		switch kind {
		case intakePost:
			return 441, "Posting failed (" + reason + ")", false
		case intakeIHave:
			return 437, "Rejected (" + reason + ")", false
		default:
			return 439, messageID + " Rejected (" + reason + ")", false
		}
	}
	switch {
	case code == history.CasePass && err == nil:
		switch kind {
		case intakePost:
			return 240, "Article posted successfully", false
		case intakeIHave:
			return 235, "Article transferred successfully", false
		default:
			return 239, messageID + " Article transferred successfully", false
		}
	case code == history.CaseDupes && err == nil:
		return rejected("duplicate")
	case code == history.CaseRetry && err == nil:
		// in flight: another goroutine holds the claim
		switch kind {
		case intakePost:
			return 441, "Posting failed (in progress, try again later)", false
		case intakeIHave:
			return 436, "Retry later (in progress)", false
		default:
			return 439, messageID + " In progress", false
		}
	case code == history.CaseRetry && errors.Is(err, common.ErrIntakeTemporary):
		switch kind {
		case intakePost:
			return 441, "Posting failed (temporary failure, try again later)", false
		case intakeIHave:
			return 436, "Retry later (temporary failure)", false
		default:
			return 400, "Service temporarily unavailable", true
		}
	case code == history.CaseError && errors.Is(err, common.ErrIntakeRejected):
		reason := "invalid article"
		msg := err.Error()
		prefix := common.ErrIntakeRejected.Error() + ": "
		if i := strings.Index(msg, prefix); i >= 0 {
			if r := msg[i+len(prefix):]; r != "" && !strings.ContainsAny(r, "\r\n") {
				reason = r
			}
		}
		return rejected(reason)
	}
	// unclassified (defensive), including CaseError without a rejection
	switch kind {
	case intakePost:
		return 441, "Posting failed (processing error)", false
	case intakeIHave:
		return 436, "Transfer failed (processing error)", false
	default:
		return 400, "Service temporarily unavailable", true
	}
}

// closeAfterReply writes the reply, then closes the connection without losing it:
// CloseWrite (when the connection supports it), read and discard pending client input
// for at most closeDrainTimeout / 1 MiB (unread input would make the kernel answer with
// RST and drop the reply), close. It returns errQuit, so Handle ends the session
// cleanly: no further reply and no read of the pipelined input.
func (c *ClientConnection) closeAfterReply(code int, text string) error {
	if err := c.sendResponse(code, text); err != nil {
		log.Printf("[NNTP] reply %d before close failed: %v", code, err)
	} else if c.conn != nil {
		if cw, ok := c.conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(closeDrainTimeout))
		_, _ = io.CopyN(io.Discard, c.textConn.R, 1<<20)
	}
	c.Close()
	return errQuit
}

// pathIdentity is this server's path identity (RFC 5537 §3.2.1): Config.Hostname,
// "" when there is no config.
func (c *ClientConnection) pathIdentity() string {
	if c.server == nil || c.server.Config == nil {
		return ""
	}
	return c.server.Config.Hostname
}

// injectProtoArticle is the injecting agent's work on a POSTed proto-article
// (RFC 5537 §3.5): it refuses forged trace headers, checks or generates Message-ID
// and Date, adds Injection-Date (only when it generated one of them) and
// Injection-Info, and prepends "<identity>!.POSTED!" to the Path. Errors wrap
// common.ErrIntakeRejected with a fixed reason.
func (c *ClientConnection) injectProtoArticle(head []string) ([]string, error) {
	rejected := func(reason string) error {
		return fmt.Errorf("%w: %s", common.ErrIntakeRejected, reason)
	}
	identity := c.pathIdentity()
	if identity == "" {
		return nil, rejected("no path identity") // the handler logs "[NNTP] POST refused: ..."
	}
	present := func(name string) bool { return len(common.HeaderValues(head, name)) > 0 }

	// RFC 5537 §3.5 step 2: trace headers in a proto-article are forged
	if present("Injection-Info") || present("Xref") {
		return nil, rejected("forged trace")
	}
	for _, p := range common.HeaderValues(head, "Path") {
		if strings.Contains(strings.ToUpper(p), ".POSTED") { // RFC 5537 §3.2.1: keywords are case-insensitive
			return nil, rejected("forged trace")
		}
	}
	hasMsgID, hasDate := present("Message-ID"), present("Date")
	if present("Injection-Date") && (!hasMsgID || !hasDate) {
		return nil, rejected("invalid injection date")
	}

	out := make([]string, 0, len(head)+4)
	out = append(out, head...)
	now := time.Now()
	generated := false
	if hasMsgID {
		if !common.ValidMessageID(common.HeaderValue(head, "Message-ID")) {
			return nil, rejected("invalid message-id")
		}
	} else {
		id, err := common.GenerateMessageID(identity)
		if err != nil {
			log.Printf("[NNTP] POST refused: %v", err)
			return nil, rejected("no path identity")
		}
		out = append(out, "Message-ID: "+id)
		generated = true
	}
	if hasDate {
		t, err := mail.ParseDate(common.HeaderValue(head, "Date"))
		if err != nil {
			return nil, rejected("invalid date")
		}
		if t.After(now.Add(24 * time.Hour)) {
			return nil, rejected("date in the future")
		}
		if t.Before(now.Add(-PostDateMaxAge)) {
			return nil, rejected("date too old")
		}
	} else {
		out = append(out, "Date: "+common.FormatNewsDate(now))
		generated = true
	}
	if generated {
		out = append(out, "Injection-Date: "+common.FormatNewsDate(now))
	}

	// Injection-Info without posting-host or client IP (privacy)
	info := identity
	if c.user != nil && c.server.DB != nil {
		nonce := strconv.FormatInt(now.Unix(), 10)
		if hash, err := c.server.DB.ComputeHashedUsername(c.user.Username, nonce); err == nil && hash != "" {
			info = fmt.Sprintf("%s; posting-account=\"%s\"; logging-data=\"%s\"", identity, hash, nonce)
		}
	}
	out = append(out, "Injection-Info: "+info)

	old := common.HeaderValue(head, "Path")
	if old == "" {
		old = "not-for-mail"
	}
	return common.ReplaceHeader(out, "Path", identity+"!.POSTED!"+old), nil
}

// relayPath prepends "<identity>!.SEEN.<peer IP>!" to the Path of a relayed article
// (RFC 5537 §3.2.1). Without an identity or a Path header the head is returned unchanged.
func (c *ClientConnection) relayPath(head []string) []string {
	identity := c.pathIdentity()
	if identity == "" || len(common.HeaderValues(head, "Path")) == 0 {
		return head
	}
	peerIP := "unknown"
	if c.conn != nil && c.conn.RemoteAddr() != nil {
		if host, _, err := net.SplitHostPort(c.conn.RemoteAddr().String()); err == nil && host != "" {
			peerIP = host
		}
	}
	return common.ReplaceHeader(head, "Path", identity+"!.SEEN."+peerIP+"!"+common.HeaderValue(head, "Path"))
}

// buildIncomingArticle parses raw head/body lines and maps the newsgroups to pointers.
// ProcessIncomingArticle needs NewsgroupsPtr (its first entry names the primary group);
// only syntactically valid names get a pointer (parseIncomingArticleLines), and the
// processor rebuilds the list with the groups it resolved.
func (c *ClientConnection) buildIncomingArticle(headLines, bodyLines []string) (*models.Article, error) {
	article, newsgroups, err := parseIncomingArticleLines(headLines, bodyLines)
	if err != nil {
		return nil, err
	}
	article.ArticleNums = make(map[*string]int64, len(newsgroups))
	for _, ng := range newsgroups {
		newsgroupPtr := c.server.DB.Batch.GetNewsgroupPointer(ng)
		article.NewsgroupsPtr = append(article.NewsgroupsPtr, newsgroupPtr)
		article.ArticleNums[newsgroupPtr] = -1
	}
	return article, nil
}

// parseIncomingArticleLines builds a models.Article from already dot-unstuffed head and body lines
// (without the separating empty line) and returns the newsgroups from the Newsgroups header
// (common.ParseNewsgroups: RFC 5536 §3.1.4 names, comma separated). It does not access the
// server or database.
func parseIncomingArticleLines(headLines, bodyLines []string) (*models.Article, []string, error) {
	messageID := extractHeaderValue(headLines, "message-id")

	lines := make([]string, 0, len(headLines)+1+len(bodyLines))
	lines = append(lines, headLines...)
	lines = append(lines, "")
	lines = append(lines, bodyLines...)

	article, err := ParseLegacyArticleLines(messageID, lines, false)
	if err != nil {
		return nil, nil, err
	}
	// Preserve original lines for peering, also when the body is empty
	article.NNTPhead = headLines
	article.NNTPbody = bodyLines

	newsgroups, err := common.ParseNewsgroups(common.GetHeaderFirst(article.Headers, "newsgroups"))
	if err != nil {
		return nil, nil, fmt.Errorf("newsgroups header: %w", err)
	}
	return article, newsgroups, nil
}

// extractHeaderValue returns the trimmed value of the first header named name (case-insensitive),
// unfolding continuation lines.
func extractHeaderValue(headLines []string, name string) string {
	for i, line := range headLines {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		colonPos := strings.Index(line, ":")
		if colonPos == -1 || !strings.EqualFold(strings.TrimSpace(line[:colonPos]), name) {
			continue
		}
		value := strings.TrimSpace(line[colonPos+1:])
		for _, cont := range headLines[i+1:] {
			if cont == "" || (cont[0] != ' ' && cont[0] != '\t') {
				break
			}
			if part := strings.TrimSpace(cont); part != "" {
				if value != "" {
					value += " "
				}
				value += part
			}
		}
		return value
	}
	return ""
}
