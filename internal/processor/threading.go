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

var MaxCrossPosts = 15 // HARDCODED Maximum number of crossposts to allow per article

var LocalHostnamePath = "" // Hostname must be set before processing articles
/*
var (

	processedArticleCount int64
	lastGCTime            time.Time
	gcMutex               sync.Mutex

)
*/
const DefaultArticleItemPath = "no-path!not-for-mail"

// Removed all object pools - they were causing race conditions and data corruption

// Removed all pool functions - they were causing race conditions and data corruption
// Objects are now allocated normally and Go's GC handles cleanup

// triggerGCIfNeeded forces garbage collection periodically during bulk imports to manage memory
func triggerGCIfNeeded(bulkmode bool) {
	/*
		if !bulkmode {
			return // Only do this for bulk imports
		}

		gcMutex.Lock()
		defer gcMutex.Unlock()

		processedArticleCount++
		now := time.Now()

		// Force GC every 1000 articles or every 10 seconds, whichever comes first (made more aggressive for memory management)

		if processedArticleCount%1000 == 0 || now.Sub(lastGCTime) > 10*time.Second {
			//runtime.GC()
			lastGCTime = now
			//log.Printf("[MEMORY] Forced GC after %d articles", processedArticleCount)
		}
	*/
}

// ComputeMessageIDHash computes MD5 hash of a message-ID
func ComputeMessageIDHash(messageID string) string {
	hash := md5.Sum([]byte(messageID))
	return fmt.Sprintf("%x", hash)
}
func CheckMessageIdFormat(messageID string) bool {
	// Check if the message ID is a valid format
	if messageID == "" {
		return false
	}
	// A simple check could be to see if it contains '@' and '.'
	if len(messageID) < 5 || !containsAtAndDot(messageID) {
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
func (proc *Processor) processArticle(art *models.Article, legacyNewsgroup string, bulkmode bool) (int, error) {
	// external caller supplies ONLY art *models.Article!
	if art == nil || art.MessageID == "" {
		return history.CaseError, fmt.Errorf("processArticle: article is nil")
	}
	msgIdItem := history.MsgIdCache.GetORCreate(art.MessageID)
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

	//if art != nil && groupDBs == nil && article == nil {
	// Parse article headers
	//log.Printf("processArticle: parseHeaders article '%s' [ groupDBs=%v article=%v ]", art.MessageID, groupDBs != nil, article != nil)
	lines := countLines([]byte(art.BodyText))

	if bulkmode {
		msgIdItem.Mux.Lock()
		msgIdItem.Response = history.CaseLock
		msgIdItem.Mux.Unlock()
		// dont process crossposts if we downloaded articles in bulkmode
		newsgroups = append(newsgroups, legacyNewsgroup) // Use legacy newsgroup if no newsgroups found

	} else if !RunRSLIGHTImport && !bulkmode {

		newsgroupsStr := getHeaderFirst(art.Headers, "newsgroups")
		if newsgroupsStr == "" {
			log.Printf("[SPAM:HDR] Article '%s' no newsgroups header", art.MessageID)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", art.MessageID)
		}

		newsgroups = proc.extractGroupsFromHeaders(art.MessageID, newsgroupsStr)
		if len(newsgroups) > MaxCrossPosts {
			log.Printf("[SPAM:EMP] Article '%s' newsgroups=%d", art.MessageID, len(newsgroups))
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("error processArticle: article '%s' crossposts=%d", art.MessageID, len(newsgroups))
		}

	} else {
		log.Printf("ERROR processArticle: article '%s' has no 'newsgroups' header and no legacy newsgroup provided", art.MessageID)
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", art.MessageID)
	}

	// Time the article creation
	dateSent := ParseNNTPDate(getHeaderFirst(art.Headers, "date"))
	dateString := getHeaderFirst(art.Headers, "date")
	if dateSent.IsZero() {
		log.Printf("[WARN:OLD] Article '%s' no valid date... headerDate='%v' dateString='%s'", art.MessageID, dateSent, dateString)
		//dateString = time.Now().Format(time.RFC1123Z) // Use current time as fallback
	}

	// Check for future posts (more than 48 hours in the future) and skip processing
	if !dateSent.IsZero() {
		futureThreshold := time.Now().Add(48 * time.Hour)
		if dateSent.After(futureThreshold) {
			log.Printf("[FUTURE] Article '%s' posted more than 48 hours in future (date: %v), skipping processing", art.MessageID, dateSent)
			proc.setCaseDupes(msgIdItem, bulkmode)
			return history.CaseError, fmt.Errorf("article '%s' posted too far in future: %v", art.MessageID, dateSent)
		}
	}

	// Create article record - no pool, just regular allocation
	article := &models.Article{
		MessageID:   art.MessageID,
		References:  getHeaderFirst(art.Headers, "references"),
		Subject:     getHeaderFirst(art.Headers, "subject"),
		FromHeader:  getHeaderFirst(art.Headers, "from"),
		Path:        getHeaderFirst(art.Headers, "path"),
		DateSent:    dateSent,
		DateString:  dateString,
		Bytes:       len(art.BodyText),
		Lines:       lines,
		ReplyCount:  0, // Will be updated by threading
		HeadersJSON: multiLineHeaderToMergedString(art.NNTPhead),
		BodyText:    art.BodyText,
		ImportedAt:  time.Now(),
		MsgIdItem:   msgIdItem,
	}
	if article.Subject == "" {
		log.Printf("[WARN:OLD] Article '%s' empty subject... headers='%#v'", art.MessageID, art.Headers)
		article.Subject = "No Subject" // Fallback to a default value
	}
	if article.FromHeader == "" {
		log.Printf("[WARN:OLD] Article '%s' empty from header... headers='%#v'", art.MessageID, art.Headers)
		article.FromHeader = "Unknown <anon@nohost.local>" // Fallback to a default value
	}
	if article.Path == "" {
		//log.Printf("[WARN:OLD] Article '%s' empty path... ?! headers='%#v'", art.MessageID, art.Headers)
		// Fallback to message ID if no path is provided
		article.Path = LocalHostnamePath + "!" + DefaultArticleItemPath
	} else {
		article.Path = LocalHostnamePath + "!" + article.Path // Ensure path is prefixed with hostname
	}
	if bulkmode {
		article.NNTPhead = nil // not needed in bulmode: free memory
		article.NNTPbody = nil // not needed in bulmode: free memory

	}
	if len(newsgroups) > 0 {
		// Process groups directly inline - no goroutines/channels needed

		for _, newsgroup := range newsgroups {

			// Get the newsgroup pointer once from batch system for memory efficiency
			newsgroupPtr := proc.DB.Batch.GetNewsgroupPointer(newsgroup)
			if newsgroupPtr == nil {
				log.Printf("error processArticle: GetNewsgroupPointer nil exception! msgId='%s' newsgroup '%s' skipping crosspost", art.MessageID, newsgroup)
				proc.setCaseDupes(msgIdItem, bulkmode)
				continue // Skip this group if not found
			}
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

			// Skip database duplicate check for bulk legacy imports
			if !bulkmode {
				// check if article exists in articledb - this is the expensive operation
				if groupDBs.ExistsMsgIdInArticlesDB(article.MessageID) {
					groupDBs.Return(proc.DB) // Return connection before continuing
					continue
				}
			}

			groupDBs.Return(proc.DB)

			proc.DB.Batch.BatchCaptureOverviewForLater(newsgroupPtr, article)

			// Return connection immediately after processing
			//log.Printf("BatchCaptureOverviewForLater: msgid='%s' ng: '%s'", article.MessageID, group)
			go GroupCounter.Increment(newsgroup) // Increment the group counter

			// Bridge article to Fediverse/Matrix if enabled
			if proc.BridgeManager != nil {
				go proc.BridgeManager.BridgeArticle(article, newsgroup)
			}

		}
		//log.Printf("All posts completed: (%d) for article %s", len(newsgroups), art.MessageID)

	} else {
		log.Printf("No newsgroups found in article '%s', skipping processing", art.MessageID)
		// Pipeline safety: Reset CaseWrite on error
		proc.setCaseDupes(msgIdItem, bulkmode)
		return history.CaseError, fmt.Errorf("error processArticle: article '%s' has no 'newsgroups' header", art.MessageID)
	}

	// Memory optimization: trigger GC periodically during bulk imports
	triggerGCIfNeeded(bulkmode)

	return history.CasePass, nil
} // end func processArticle
