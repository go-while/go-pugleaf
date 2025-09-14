package processor

import (
	"crypto/md5"
	"fmt"
	"log"
	"strings"
	"time"

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
		msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
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

	// Pipeline safety: Implement CaseWrite/CaseDupes logic for deduplication
	if !bulkmode { // rslight legacy importer runs in bulkmode! so we skip history checks here!!!

		// Thread-safe check and set of processing state
		msgIdItem.Mux.Lock()

		// Check current state of the message
		switch msgIdItem.Response {
		case history.CaseDupes:
			// Article already processed and written to database
			msgIdItem.Mux.Unlock()
			return history.CaseDupes, nil
		case history.CaseWrite:
			// Article is currently being processed by another goroutine
			msgIdItem.Mux.Unlock()
			return history.CaseDupes, nil // Return duplicate to avoid race condition
		case history.CasePass:
			// Article is new, mark as being processed
			msgIdItem.Response = history.CaseLock
			msgIdItem.Mux.Unlock()
			// Continue with processing
		default:
			// Also check history database for final determination
			msgIdItem.Mux.Unlock()
			response, err := proc.Lookup(msgIdItem)
			if err != nil {
				log.Printf("Error looking up message ID %s in history: %v", msgIdItem.MessageId, err)
				return history.CaseError, err
			}
			if response != history.CasePass {
				return history.CaseDupes, nil // Already exists in history
			}
			// If not in history, mark as being processed
			msgIdItem.Mux.Lock()
			if msgIdItem.Response == history.CasePass { // Re-check after lock
				msgIdItem.Response = history.CaseWrite
			} else {
				// State changed while we were checking history, treat as duplicate
				msgIdItem.Mux.Unlock()
				return history.CaseDupes, nil
			}
			msgIdItem.Mux.Unlock()
		}
	}

	var newsgroups []string

	if bulkmode {
		msgIdItem.Mux.Lock()
		msgIdItem.Response = history.CaseLock
		msgIdItem.Mux.Unlock()
		// dont process crossposts if we downloaded articles in bulkmode
		// Use legacy newsgroup in bulkmode. add article only to single newsgroup db.
		newsgroups = append(newsgroups, legacyNewsgroup)

	} else if !RunRSLIGHTImport && !bulkmode {

		newsgroupsStr := getHeaderFirst(article.Headers, "newsgroups")
		if newsgroupsStr == "" {
			log.Printf("[SPAM:HDR] Article '%s' no newsgroups header", article.MessageID)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", article.MessageID)
		}

		newsgroups = proc.extractGroupsFromHeaders(article.MessageID, newsgroupsStr)
		if len(newsgroups) > MaxCrossPosts {
			log.Printf("[SPAM:EMP] Article '%s' newsgroups=%d", article.MessageID, len(newsgroups))
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("error processArticle: article '%s' crossposts=%d", article.MessageID, len(newsgroups))
		}

	} else {
		log.Printf("ERROR processArticle: article '%s' has no 'newsgroups' header and no legacy newsgroup provided", article.MessageID)
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", article.MessageID)
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

	// part of parsing data moved to nntp-client-commands.go:L~850 (func ParseLegacyArticleLines)
	article.ReplyCount = 0 // Will be updated by threading
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
	// Apply fallbacks for missing essential fields
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
	if article.Path == "" {
		//log.Printf("[WARN:OLD] Article '%s' empty path... ?! headers='%#v'", article.MessageID, article.Headers)
		article.Path = LocalNNTPHostname + "!unknown!not-for-mail"
	} else {
		article.Path = LocalNNTPHostname + "!" + article.Path // Ensure path is prefixed with hostname
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
				if proc.MsgIdCache.HasMessageIDInGroup(article.MessageID, newsgroupPtr) { // CHECK GLOBAL PROCESSOR CACHE with POINTER
					log.Printf("processArticle: article '%s' already exists in cache for newsgroup '%s', skipping crosspost", article.MessageID, *newsgroupPtr)
					continue
				}
			}

			//log.Printf("Crossposted article '%s' to newsgroup '%s'", article.MessageID, group)
			groupDBs, err := proc.DB.GetGroupDBs(newsgroup)
			if err != nil {
				log.Printf("Failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
				if groupDBs != nil {
					groupDBs.Return(proc.DB) // Return connection even on error
				}
				continue // Continue with other groups
			}
			if groupDBs.ExistsMsgIdInArticlesDB(article.MessageID) {
				groupDBs.Return(proc.DB) // Return connection before continuing
				continue
			}
			/*
				// Skip database duplicate check for bulk legacy imports
				if !bulkmode {
					// check if article exists in articledb - this is the expensive operation
					if groupDBs.ExistsMsgIdInArticlesDB(article.MessageID) {
						groupDBs.Return(proc.DB) // Return connection before continuing
						continue
					}
				}
			*/
			groupDBs.Return(proc.DB)

			proc.DB.Batch.BatchCaptureOverviewForLater(newsgroupPtr, article)

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

	} else {
		log.Printf("No newsgroups found in article '%s', skipping processing", article.MessageID)
		// Pipeline safety: Reset CaseWrite on error
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", article.MessageID)
	}

	return history.CasePass, nil
} // end func processArticle
