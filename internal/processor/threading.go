package processor

import (
	"crypto/md5"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ComputeMessageIDHash computes MD5 hash of a message-ID
func ComputeMessageIDHash(messageID string) string {
	hash := md5.Sum([]byte(messageID))
	return fmt.Sprintf("%x", hash)
}

func (proc *Processor) setCaseDupes(msgIdItem *history.MessageIdItem, bulkmode bool) {
	if msgIdItem != nil {
		msgIdItem.Mux.Lock()
		msgIdItem.Response = history.CaseDupes
		msgIdItem.CachedEntryExpires = time.Now().Add(history.CachedEntryTTL)
		msgIdItem.Mux.Unlock()
	}
}

// processArticle processes a fetched article and generates overview data.
//
// Return codes for non-bulk intake (contract K4 of plan nntp-intake-injection):
//   - CasePass, nil: queued for at least one group
//   - CaseDupes, nil: history or the cache knows it, or every target group already stores it
//   - CaseRetry, nil: another goroutine holds the claim (in flight)
//   - CaseRetry, err wrapping common.ErrIntakeTemporary: history or database error, the claim is released
//   - CaseError, err wrapping common.ErrIntakeRejected: validation or policy rejection (fixed reason)
//
// Bulk mode keeps returning CasePass, nil when every target group already stores the article.
func (proc *Processor) processArticle(article *models.Article, legacyNewsgroup string, bulkmode bool) (int, error) {
	// external caller supplies ONLY art *models.Article!
	if article == nil || article.MessageID == "" {
		return history.CaseError, fmt.Errorf("%w: invalid message-id", common.ErrIntakeRejected)
	}
	rejected := func(reason string) error {
		return fmt.Errorf("%w: %s", common.ErrIntakeRejected, reason)
	}

	// A-tools-1: one capture slot per article, taken before article.Mux is locked (a waiting
	// producer holds no article mutex, so there is no lock cycle with the capture goroutines).
	// Every return that does not hand the article to the capture goroutine frees the slot.
	acquired := captureLimit().acquire(true, proc.DB.IsDBshutdown)
	handedOff := false
	defer func() {
		if acquired && !handedOff {
			captureLimit().release()
		}
	}()

	msgIdItem := history.MsgIdCache.GetORCreate(article.MessageID)
	if msgIdItem == nil {
		return history.CaseError, fmt.Errorf("error in processArticle: msgIdItem is nil")
	}
	var newsgroups []string
	var intakeGroups []*database.IntakeGroup // non-bulk: resolved groups, same order as newsgroups
	var lines []string                       // checked header block that will be stored
	article.Mux.Lock()
	defer article.Mux.Unlock()

	// Pipeline safety: Implement CaseWrite/CaseDupes logic for deduplication
	if !bulkmode { // rslight legacy importer runs in bulkmode! so we skip history checks here!!!

		// claim the message-id first, then check history: only one goroutine can hold the claim
		msgIdItem.Mux.Lock()
		switch msgIdItem.Response {
		case history.CaseLock, history.CaseWrite:
			// being processed by another goroutine: the sender may retry later (INT-2)
			msgIdItem.Mux.Unlock()
			return history.CaseRetry, nil
		case history.CaseDupes:
			msgIdItem.Mux.Unlock()
			return history.CaseDupes, nil
		}
		msgIdItem.Response = history.CaseLock
		msgIdItem.CachedEntryExpires = time.Now().Add(history.TmpCacheTTL)
		msgIdItem.Mux.Unlock()

		exists, err := proc.History.Exists(article.MessageID)
		if err != nil {
			log.Printf("Error looking up message ID %s in history: %v", article.MessageID, err)
			releaseClaim(msgIdItem)
			return history.CaseRetry, fmt.Errorf("%w: history: %v", common.ErrIntakeTemporary, err)
		}
		if exists {
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseDupes, nil
		}
	}

	if bulkmode {
		msgIdItem.Mux.Lock()
		msgIdItem.Response = history.CaseLock
		msgIdItem.Mux.Unlock()

		// D12: fetch-mode header block check; malformed lines are dropped from the stored block
		if article.HeadersJSON != "" {
			normalized, err := common.CheckHeaderBlock(strings.Split(article.HeadersJSON, "\n"), common.HeaderBlockFetch)
			if err != nil {
				log.Printf("[INTAKE] article '%s' (bulk) rejected: %v", article.MessageID, err)
				proc.setCaseDupes(msgIdItem, bulkmode)
				return history.CaseError, err
			}
			// A-intake-3: the fetched Message-ID header must be the requested id
			if mid := common.HeaderValue(normalized, "Message-ID"); mid != "" && mid != article.MessageID {
				log.Printf("[INTAKE] article '%s' (bulk) rejected: Message-ID header %q differs", article.MessageID, mid)
				proc.setCaseDupes(msgIdItem, bulkmode)
				return history.CaseError, rejected("invalid message-id")
			}
			lines = normalized
		}

		// dont process crossposts if we downloaded articles in bulkmode
		// Use legacy newsgroup in bulkmode. add article only to single newsgroup db.

		newsgroupsStr := common.GetHeaderFirst(article.Headers, "newsgroups")
		if newsgroupsStr == "" {
			log.Printf("[SPAM:HDR] Article '%s' no newsgroups header", article.MessageID)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, rejected("missing mandatory header")
		}

		ngs := proc.extractGroupsFromHeaders(article.MessageID, newsgroupsStr)
		if len(ngs) == 0 || len(ngs) > MaxCrossPosts {
			log.Printf("[SPAM:EMP] Article '%s' newsgroups=%d", article.MessageID, len(ngs))
			proc.setCaseDupes(msgIdItem, bulkmode)
			if len(ngs) == 0 {
				return history.CaseError, rejected("invalid newsgroups")
			}
			return history.CaseError, rejected("too many newsgroups")
		}
		newsgroups = append(newsgroups, legacyNewsgroup)

	} else if !RunRSLIGHTImport {
		// non-bulk intake (POST/IHAVE/TAKETHIS): strict article-layer checks
		lines = article.NNTPhead
		if lines == nil && article.HeadersJSON != "" {
			lines = strings.Split(article.HeadersJSON, "\n")
		}
		var approved bool
		var err error
		lines, approved, err = validateIntakeHeaders(lines, article.MessageID)
		if err != nil {
			log.Printf("[INTAKE] article %q rejected: %v", article.MessageID, err)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, err
		}
		names, err := common.ParseNewsgroups(common.HeaderValue(lines, "Newsgroups"))
		if err != nil {
			log.Printf("[INTAKE] article %s rejected: %v", article.MessageID, err)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, err
		}
		if len(names) > MaxCrossPosts {
			log.Printf("[INTAKE] article %s rejected: %d newsgroups (max %d)", article.MessageID, len(names), MaxCrossPosts)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, rejected("too many newsgroups")
		}
		dateStr := article.DateString
		if dateStr == "" {
			dateStr = common.HeaderValue(lines, "Date")
		}
		dateSent, err := checkIntakeDates(dateStr, common.HeaderValue(lines, "Injection-Date"), time.Now())
		if err != nil {
			log.Printf("[INTAKE] article %s rejected: %v (Date %q)", article.MessageID, err, dateStr)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, err
		}
		article.DateSent = dateSent
		body := article.NNTPbody
		if body == nil && article.BodyText != "" {
			body = strings.Split(article.BodyText, "\n")
		}
		groups, err := proc.resolveIntakeGroups(names, article.Posted, approved, common.WireArticleSize(lines, body))
		if err != nil {
			log.Printf("[INTAKE] article %s refused: %v", article.MessageID, err)
			if errors.Is(err, common.ErrIntakeTemporary) {
				releaseClaim(msgIdItem)
				return history.CaseRetry, err
			}
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, err
		}
		intakeGroups = groups
		for _, g := range groups {
			newsgroups = append(newsgroups, g.Name)
		}
	} else {
		log.Printf("ERROR in processArticle: invalid bulk import flags")
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, rejected("invalid article")
	}
	if article.Subject == "" {
		log.Printf("[HDR-SPAM] Article '%s' empty subject... headers='%#v'", article.MessageID, article.Headers)
		//article.Subject = "No Subject" // Fallback to a default value
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, rejected("missing mandatory header")
	}
	if article.FromHeader == "" {
		log.Printf("[HDR-SPAM] Article '%s' empty from header... headers='%#v'", article.MessageID, article.Headers)
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, rejected("missing mandatory header")
	}

	if bulkmode {
		// the bulk paths keep the lenient parser (archives, D10)
		article.DateSent = ParseNNTPDate(article.DateString)
		if article.DateSent.IsZero() {
			log.Printf("[ERROR-HDR] Article '%s' no valid date... headerDate='%v' dateString='%s'", article.MessageID, article.DateSent, article.DateString)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, rejected("invalid date")
		}
		// Check for future posts (more than MaxFutureDate in the future) and skip processing
		if article.DateSent.After(time.Now().Add(MaxFutureDate)) {
			log.Printf("[SPAM-HDR:FUTURE] Article '%s' posted too far in future (date: %v), skipping", article.MessageID, article.DateSent)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, rejected("date in the future")
		}
	}

	// part of parsing data moved to nntp-client-commands.go:L~850 (func ParseLegacyArticleLines)
	// the history index is written by the batch system after the article was committed (db_batch.go)
	article.MsgIdItem = msgIdItem

	article.ArticleNums = make(map[*string]int64)
	article.ProcessQueue = make(chan *string, 16) // Initialize process queue

	if len(article.RefSlice) == 0 {
		article.IsThrRoot = true
		article.IsReply = false
	} else {
		article.IsThrRoot = false
		article.IsReply = true
	}

	if article.Path == "" {
		//log.Printf("[WARN:OLD] Article '%s' empty path... ?! headers='%#v'", article.MessageID, article.Headers)
		article.Path = LocalNNTPHostname + "!unknown!not-for-mail"
	} else {
		if !strings.HasPrefix(article.Path, LocalNNTPHostname+"!") {
			article.Path = LocalNNTPHostname + "!" + article.Path // Ensure path is prefixed with hostname
		}
	}

	// Store the checked header block without a foreign Xref (INT-4, A-intake-6). Non-bulk
	// stores the strict-validated block as received (the NNTP layer puts the prefixed Path
	// into it before the processor sees it); bulk gets its Path line synced to the path
	// column (D7). A bulk article without a header block (HeadersJSON "") is left alone.
	if !bulkmode {
		article.HeadersJSON = strings.Join(stripXrefLines(lines), "\n")
	} else if lines != nil {
		article.HeadersJSON = strings.Join(syncStoredPath(stripXrefLines(lines), article.Path), "\n")
	}

	// Free memory from transient fields after extracting what we need
	if bulkmode {
		article.NNTPhead = nil // not needed in bulkmode: free memory
		article.NNTPbody = nil // not needed in bulkmode: free memory
	}

	// drop the reference only: the caller may share the map with other copies (A-intake-5)
	article.Headers = nil

	if len(newsgroups) == 0 {
		log.Printf("No newsgroups found in article '%s', skipping processing", article.MessageID)
		// Pipeline safety: Reset CaseWrite on error
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, rejected("no valid newsgroup")
	}

	// NewsgroupsPtr holds exactly the groups captured below: buildIncomingArticle pre-fills
	// it with every header group, and a group left in it that never gets the article would
	// stop the batch from ever clearing the article.
	article.NewsgroupsPtr = article.NewsgroupsPtr[:0]
	var capture []*string // groups this article is captured for
	opened := 0           // groups whose database could be opened and queried
	alreadyStored := false
	var haveID int64 // non-bulk: ID of the first group that already stores the article
	for i, newsgroup := range newsgroups {

		// Get the newsgroup pointer once from batch system for memory efficiency
		newsgroupPtr := proc.DB.Batch.GetNewsgroupPointer(newsgroup)
		if newsgroupPtr == nil {
			log.Printf("error processArticle: GetNewsgroupPointer nil exception! msgId='%s' newsgroup '%s' skipping crosspost", article.MessageID, newsgroup)
			continue // Skip this group if not found
		}

		//log.Printf("Crossposted article '%s' to newsgroup '%s'", article.MessageID, group)
		groupDB, err := proc.DB.GetGroupDB(newsgroup)
		if err != nil {
			log.Printf("Failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
			if groupDB != nil {
				groupDB.Return() // Return connection even on error
			}
			continue // Continue with other groups
		}
		// A-db-3: a failed lookup is not "not stored"; the group counts as not opened
		stored, err := groupDB.LookupMsgIdInArticlesDB(article.MessageID)
		groupDB.Return() // Return connection immediately after the lookup
		if err != nil {
			log.Printf("processArticle: message-id lookup of '%s' in '%s' failed: %v", article.MessageID, newsgroup, err)
			continue
		}
		opened++
		if stored {
			alreadyStored = true
			if haveID == 0 && i < len(intakeGroups) {
				haveID = intakeGroups[i].ID
			}
			continue
		}

		// every append happens while article.Mux is held, before the capture goroutine starts
		article.NewsgroupsPtr = append(article.NewsgroupsPtr, newsgroupPtr)
		capture = append(capture, newsgroupPtr)

		//log.Printf("BatchCaptureOverviewForLater: msgid='%s' ng: '%s'", article.MessageID, group)
		GroupCounter.Increment(newsgroup) // Increment the group counter
		/*
			// Bridge article to Fediverse/Matrix if enabled
			if proc.BridgeManager != nil {
				go proc.BridgeManager.BridgeArticle(art, newsgroup)
			}
		*/
	}
	//log.Printf("All posts completed: (%d) for article %s", len(newsgroups), article.MessageID)

	switch {
	case len(capture) > 0:
		// One goroutine per article captures every target group (A-tools-1). It runs once
		// processArticle has returned and unlocked article.Mux (BatchCaptureOverviewForLater
		// locks it). len(capture) <= MaxCrossPosts < cap(ProcessQueue), so the sends to
		// ProcessQueue never block, and since every target is already in NewsgroupsPtr no
		// group's batch can clear the article before the last capture was sent.
		handedOff = true
		// Commit barrier (A-tools-5b): counted here, synchronously, before the capture
		// goroutine can reach the batch writer, so a WaitCommitted that starts after this
		// call returned always sees the article.
		proc.DB.Batch.NoteCaptured(capture)
		go func(ptrs []*string, acquired bool) {
			if acquired {
				defer captureLimit().release()
			}
			for _, ptr := range ptrs {
				proc.DB.Batch.BatchCaptureOverviewForLater(ptr, article)
			}
		}(capture, acquired)
		return history.CasePass, nil

	case alreadyStored:
		// nothing to store: the batch system will not finalize the item, so do not leave it in CaseLock
		proc.setCaseDupes(msgIdItem, bulkmode)
		if bulkmode {
			// bulk callers (rslight.go) treat anything but CasePass as an error (K1)
			return history.CasePass, nil
		}
		// INT-2: stored but unknown to history: teach history (later offers get 435) and refuse
		if haveID > 0 {
			proc.AddArticleToHistory(article.MessageID, haveID, 0) // number of the stored copy unknown here
		}
		return history.CaseDupes, nil

	default:
		// A-intake-9: no group database could be opened, nothing was stored
		log.Printf("[INTAKE] article %s not stored: no group database could be opened (%d groups, %d opened)", article.MessageID, len(newsgroups), opened)
		releaseClaim(msgIdItem)
		return history.CaseRetry, fmt.Errorf("%w: no group database could be opened", common.ErrIntakeTemporary)
	}
} // end func processArticle
