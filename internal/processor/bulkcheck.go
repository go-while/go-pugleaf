package processor

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
)

// bulkChecked is what the article-only checks of the bulk path (bulkArticleChecks) hand back
// to ProcessArticle for an article that passed them.
type bulkChecked struct {
	lines    []string  // the checked header block that will be stored (nil: the article has none)
	maxCross int       // MaxCrossPosts as read for the Newsgroups check
	dateSent time.Time // the date the article is filed under
}

// bulkArticleChecks runs the checks of ProcessArticle's bulk (fetcher, importer, post queue)
// path that depend on the article alone, in ProcessArticle's order: a NUL octet in the body,
// the header block in fetch mode, the Message-ID header, a Control header, the Newsgroups
// header and its group count, Subject and From, and the date (unusable, or too far in the
// future). Every error wraps common.ErrIntakeRejected with ProcessArticle's fixed reason, and
// every one is a refusal ProcessArticle remembers (rememberBulkReject).
//
// It changes neither the article nor the history, the message-id cache or a database: it
// reads the MaxCrossPosts setting (and under RunRSLIGHTImport extractGroupsFromHeaders reads
// the sections). verbose prints ProcessArticle's log lines (the reject lines, the accepted
// header defects, the date fallback and the once-per-group control line for legacyNewsgroup);
// without it only extractGroupsFromHeaders still logs an invalid group name.
func (proc *Processor) bulkArticleChecks(article *models.Article, legacyNewsgroup string, verbose bool) (bulkChecked, error) {
	var res bulkChecked
	rejected := func(reason string) error {
		return fmt.Errorf("%w: %s", common.ErrIntakeRejected, reason)
	}
	logf := func(format string, args ...any) {
		if verbose {
			log.Printf(format, args...)
		}
	}

	// D2: a NUL octet in the body refuses a fetched or imported article too
	// (decision 2026-11-11: the fetch path follows the relay rules). It is remembered on the
	// bulk path, like the header-block reject below, so a re-import or a peer offering the
	// same id does not read and refuse it again.
	if intakeBodyHasNUL(article) {
		logf("[INTAKE] article '%s' (bulk) rejected: article contains NUL", article.MessageID)
		return res, rejected("article contains NUL")
	}

	// D12: fetch-mode header block check; malformed lines are dropped from the stored
	// block, a bare CR or LF is kept verbatim and logged (relay rules)
	if article.HeadersJSON != "" {
		normalized, defects, err := common.CheckHeaderBlockDefects(strings.Split(article.HeadersJSON, "\n"), common.HeaderBlockFetch)
		if err != nil {
			logf("[INTAKE] article '%s' (bulk) rejected: %v", article.MessageID, err)
			return res, err
		}
		if verbose {
			logHeaderDefects(article.MessageID, defects)
		}
		// A-intake-3: the fetched Message-ID header must be the requested id
		if mid := common.HeaderValue(normalized, "Message-ID"); mid != "" && mid != article.MessageID {
			logf("[INTAKE] article '%s' (bulk) rejected: Message-ID header %q differs", article.MessageID, mid)
			return res, rejected("invalid message-id")
		}
		res.lines = normalized
	}

	// D3 and decision 6 of plan nntp-peering: the bulk (fetcher/importer) path refuses a
	// control message exactly like NNTP intake does (validateIntakeHeaders), logs it once
	// per group per run and remembers it as rejected. A Supersedes header stays ignored.
	if ctl := bulkControlHeader(article, res.lines); ctl != "" {
		if verbose && controlRefusals.first(legacyNewsgroup) {
			log.Printf("[INTAKE] control message %s (%s) in '%s' refused: control messages are not processed (logged once per group)",
				article.MessageID, ctl, legacyNewsgroup)
		}
		return res, rejected("control messages not accepted")
	}

	// dont process crossposts if we downloaded articles in bulkmode: the caller files the
	// article into its legacy newsgroup only, the header just has to be there and sane
	newsgroupsStr := common.GetHeaderFirst(article.Headers, "newsgroups")
	if newsgroupsStr == "" {
		logf("[SPAM:HDR] Article '%s' no newsgroups header", article.MessageID)
		return res, rejected("missing mandatory header")
	}
	ngs := proc.extractGroupsFromHeaders(article.MessageID, newsgroupsStr)
	res.maxCross = proc.DB.MaxCrossPosts()
	if len(ngs) == 0 || len(ngs) > res.maxCross {
		logf("[SPAM:EMP] Article '%s' newsgroups=%d", article.MessageID, len(ngs))
		if len(ngs) == 0 {
			return res, rejected("invalid newsgroups")
		}
		return res, rejected("too many newsgroups")
	}

	if article.Subject == "" {
		logf("[HDR-SPAM] Article '%s' empty subject... headers='%#v'", article.MessageID, article.Headers)
		return res, rejected("missing mandatory header")
	}
	if article.FromHeader == "" {
		logf("[HDR-SPAM] Article '%s' empty from header... headers='%#v'", article.MessageID, article.Headers)
		return res, rejected("missing mandatory header")
	}

	// the bulk paths keep the lenient parser (archives, D10); an unreadable or
	// pre-Usenet Date is taken from Posted, Date-Received, ... when one is usable
	dateSent, dateFrom := bulkArticleDate(article.DateString, article.Headers)
	if dateFrom != "" {
		logf("[HDR-DATE] Article '%s': Date '%s' unusable, using %s '%s'", article.MessageID, article.DateString, dateFrom, common.GetHeaderFirst(article.Headers, dateFrom))
	}
	if dateSent.IsZero() {
		logf("[ERROR-HDR] Article '%s' no valid date... headerDate='%v' dateString='%s'", article.MessageID, dateSent, article.DateString)
		return res, rejected("invalid date")
	}
	// Check for future posts (more than MaxFutureDate in the future) and skip processing
	if dateSent.After(time.Now().Add(MaxFutureDate)) {
		logf("[SPAM-HDR:FUTURE] Article '%s' posted too far in future (date: %v), skipping", article.MessageID, dateSent)
		return res, rejected("date in the future")
	}
	res.dateSent = dateSent
	return res, nil
}

// CheckBulkArticle reports whether ProcessArticle's bulk path refuses article for a reason of
// the article alone: nil, or the error ProcessArticle(article, group, true) returns for it
// (wrapping common.ErrIntakeRejected, with the same reason). A bulk caller runs it before it
// creates the target group or opens the group's database, so an article that can never be
// stored leaves no empty group behind (plan importer-empty-groups).
//
// It has no side effects: no history lookup or write, no message-id cache entry, no capture
// slot, no database write and no log line of its own (an invalid name in Newsgroups is still
// logged by the group parser); the article is not changed. The caller owns the article (it is
// not locked here). An article it passes may still be refused by ProcessArticle for a reason
// of the group. ProcessArticle refuses an article it refuses the same way and also remembers
// the message-id as rejected (rememberBulkReject): a caller that drops the article instead of
// calling ProcessArticle calls RememberReject itself to keep that.
func (proc *Processor) CheckBulkArticle(article *models.Article) error {
	if article == nil || article.MessageID == "" {
		return fmt.Errorf("%w: invalid message-id", common.ErrIntakeRejected)
	}
	_, err := proc.bulkArticleChecks(article, "", false)
	return err
}
