package processor

import (
	"crypto/md5"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ComputeMessageIDHash computes MD5 hash of a message-ID
func ComputeMessageIDHash(messageID string) string {
	hash := md5.Sum([]byte(messageID))
	return fmt.Sprintf("%x", hash)
}

func CheckMessageIdFormat(messageID string) bool {
	// Check if the message ID is a valid format
	if messageID == "" {
		log.Printf("[SPAM:HDR] Invalid message ID: empty string")
		return false
	}
	// A simple check could be to see if it contains '@' and '.'
	if len(messageID) < 5 || len(messageID) > 255 {
		log.Printf("[SPAM:HDR] Invalid message ID length or format: '%s'", messageID)
		return false
	}
	if messageID[0] != '<' || messageID[len(messageID)-1] != '>' {
		log.Printf("[SPAM:HDR] Invalid message ID format: '%s'", messageID)
		return false
	}
	if !strings.Contains(messageID, "@") {
		log.Printf("[SPAM:HDR] Invalid message ID format: missing '@' in '%s'", messageID)
		return false
	}
	return true
}

func containsAtAndDot(messageID string) bool {
	return strings.Contains(messageID, "@") && strings.Contains(messageID, ".")
}

func (proc *Processor) setCaseDupes(msgIdItem *history.MessageIdItem, bulkmode bool) {
	if msgIdItem != nil {
		msgIdItem.Mux.Lock()
		msgIdItem.Response = history.CaseDupes
		msgIdItem.CachedEntryExpires = time.Now().Add(history.CachedEntryTTL)
		msgIdItem.Mux.Unlock()
	}
}

// processArticle processes a fetched article and generates overview data
func (proc *Processor) processArticle(article *models.Article, legacyNewsgroup string, bulkmode bool) (int, error) {
	// external caller supplies ONLY art *models.Article!
	if article == nil || article.MessageID == "" {
		return history.CaseError, fmt.Errorf("processArticle: article is nil")
	}

	msgIdItem := history.MsgIdCache.GetORCreate(article.MessageID)
	if msgIdItem == nil {
		return history.CaseError, fmt.Errorf("error in processArticle: msgIdItem is nil")
	}
	var newsgroups []string
	article.Mux.Lock()
	defer article.Mux.Unlock()

	// Pipeline safety: Implement CaseWrite/CaseDupes logic for deduplication
	if !bulkmode { // rslight legacy importer runs in bulkmode! so we skip history checks here!!!

		// claim the message-id first, then check history: only one goroutine can hold the claim
		msgIdItem.Mux.Lock()
		switch msgIdItem.Response {
		case history.CaseLock, history.CaseWrite, history.CaseDupes:
			// being processed by another goroutine or already stored
			msgIdItem.Mux.Unlock()
			return history.CaseDupes, nil
		}
		msgIdItem.Response = history.CaseLock
		msgIdItem.CachedEntryExpires = time.Now().Add(history.TmpCacheTTL)
		msgIdItem.Mux.Unlock()

		exists, err := proc.History.Exists(article.MessageID)
		if err != nil {
			log.Printf("Error looking up message ID %s in history: %v", article.MessageID, err)
			msgIdItem.Mux.Lock()
			msgIdItem.Response = history.CaseError
			msgIdItem.CachedEntryExpires = time.Now().Add(history.ErrorCaseTTL)
			msgIdItem.Mux.Unlock()
			return history.CaseError, err
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
		// dont process crossposts if we downloaded articles in bulkmode
		// Use legacy newsgroup in bulkmode. add article only to single newsgroup db.

		newsgroupsStr := common.GetHeaderFirst(article.Headers, "newsgroups")
		if newsgroupsStr == "" {
			log.Printf("[SPAM:HDR] Article '%s' no newsgroups header", article.MessageID)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", article.MessageID)
		}

		ngs := proc.extractGroupsFromHeaders(article.MessageID, newsgroupsStr)
		if len(ngs) == 0 || len(ngs) > MaxCrossPosts {
			log.Printf("[SPAM:EMP] Article '%s' newsgroups=%d", article.MessageID, len(ngs))
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("error processArticle: article '%s' crossposts=%d", article.MessageID, len(ngs))
		}
		newsgroups = append(newsgroups, legacyNewsgroup)

	} else if !RunRSLIGHTImport && !bulkmode {

		newsgroupsStr := common.GetHeaderFirst(article.Headers, "newsgroups")
		if newsgroupsStr == "" {
			log.Printf("[SPAM:HDR] Article '%s' no newsgroups header", article.MessageID)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", article.MessageID)
		}

		newsgroups = proc.extractGroupsFromHeaders(article.MessageID, newsgroupsStr)
		if len(newsgroups) == 0 || len(newsgroups) > MaxCrossPosts {
			log.Printf("[SPAM:EMP] Article '%s' newsgroups=%d", article.MessageID, len(newsgroups))
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("error processArticle: article '%s' crossposts=%d", article.MessageID, len(newsgroups))
		}
	} else {
		log.Printf("ERROR in processArticle: invalid bulk import flags")
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("error processArticle")
	}
	if article.Subject == "" {
		log.Printf("[HDR-SPAM] Article '%s' empty subject... headers='%#v'", article.MessageID, article.Headers)
		//article.Subject = "No Subject" // Fallback to a default value
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'subject' header", article.MessageID)
	}
	if article.FromHeader == "" {
		log.Printf("[HDR-SPAM] Article '%s' empty from header... headers='%#v'", article.MessageID, article.Headers)
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'from' header", article.MessageID)
	}

	article.DateSent = ParseNNTPDate(article.DateString)
	if article.DateSent.IsZero() {
		log.Printf("[ERROR-HDR] Article '%s' no valid date... headerDate='%v' dateString='%s'", article.MessageID, article.DateSent, article.DateString)
		proc.setCaseDupes(msgIdItem, bulkmode)
		//dateString = time.Now().Format(time.RFC1123Z) // Use current time as fallback
		return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no valid 'date' header", article.MessageID)
	}
	// Check for future posts (more than 25 hours in the future) and skip processing
	if article.DateSent.After(time.Now().Add(25 * time.Hour)) {
		log.Printf("[SPAM-HDR:FUTURE] Article '%s' posted too far in future (date: %v), skipping", article.MessageID, article.DateSent)
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("article '%s' posted too far in future: %v", article.MessageID, article.DateSent)
	}
	// TODO: add article cutoff date checks here

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

	// Free memory from transient fields after extracting what we need
	if bulkmode {
		article.NNTPhead = nil // not needed in bulkmode: free memory
		article.NNTPbody = nil // not needed in bulkmode: free memory
	}

	if article.Headers != nil {
		for k := range article.Headers {
			article.Headers[k] = nil // Free each header slice
		}
		article.Headers = nil // Free headers map after extracting all needed values
	}

	//article.Newsgroups = nil // Free newsgroups slice if it exists
	if len(newsgroups) > 0 {
		// Process groups directly inline - no goroutines/channels needed
		queued := 0 // groups the article was queued for
		for _, newsgroup := range newsgroups {

			// Get the newsgroup pointer once from batch system for memory efficiency
			newsgroupPtr := proc.DB.Batch.GetNewsgroupPointer(newsgroup)
			if newsgroupPtr == nil {
				log.Printf("error processArticle: GetNewsgroupPointer nil exception! msgId='%s' newsgroup '%s' skipping crosspost", article.MessageID, newsgroup)
				proc.setCaseDupes(msgIdItem, bulkmode)
				continue // Skip this group if not found
			}
			article.NewsgroupsPtr = append(article.NewsgroupsPtr, newsgroupPtr)
			// @AI !!! NO CACHE CHECK for bulk legacy import!!
			if !bulkmode { // @AI !!! NO CACHE CHECK for bulk legacy import!!
				// @AI !!! NO CACHE CHECK for bulk legacy import!!
				// Cache check still provides some throttling while avoiding the expensive DB query
				//if proc.MsgIdCache.HasMessageIDInGroup(article.MessageID, newsgroupPtr) { // CHECK GLOBAL PROCESSOR CACHE with POINTER
				//	log.Printf("processArticle: article '%s' already exists in cache for newsgroup '%s', skipping crosspost", article.MessageID, *newsgroupPtr)
				//	continue
				//}
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
			if groupDB.ExistsMsgIdInArticlesDB(article.MessageID) {
				groupDB.Return() // Return connection before continuing
				continue
			}
			/*
				// Skip database duplicate check for bulk legacy imports
				if !bulkmode {
					// check if article exists in articledb - this is the expensive operation
					if groupDB.ExistsMsgIdInArticlesDB(article.MessageID) {
						groupDB.Return(proc.DB) // Return connection before continuing
						continue
					}
				}
			*/
			groupDB.Return()

			go proc.DB.Batch.BatchCaptureOverviewForLater(newsgroupPtr, article)
			queued++

			// Return connection immediately after processing
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
		if queued == 0 {
			// nothing to store (e.g. every group already has it): the batch system will not
			// finalize the item, so do not leave it in CaseLock
			proc.setCaseDupes(msgIdItem, bulkmode)
		}

	} else {
		log.Printf("No newsgroups found in article '%s', skipping processing", article.MessageID)
		// Pipeline safety: Reset CaseWrite on error
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", article.MessageID)
	}

	return history.CasePass, nil
} // end func processArticle
