package history

import (
	"hash/fnv"
	"log"
	"sync"
	"time"
)

var (
	MIICmutex  sync.RWMutex    // Global mutex for the cache
	MsgIdCache *MsgIdItemCache // Global instance of the message ID item cache

	UpperLimitMsgIdCacheSize = 1024 * 1024
	// This is the maximum size for the cache, used to prevent overflow

	DefaultMsgIdCacheSize = 4 * 1024 // N buckets
	MaxLoadFactor         = 0.75     // Resize when load factor exceeds this
	ResizeMultiplier      = 2        // Double the size when resizing

	// TTL Configuration Constants
	TmpCacheTTL       = 15 * time.Second // TTL for temporary processing (CaseWrite items) // @AI!!! DO NOT CHANGE THIS!!!!
	CachedEntryTTL    = 15 * time.Second // TTL for cache entries (CaseDupes items after flushing) // @AI!!! DO NOT CHANGE THIS!!!!
	ErrorCaseTTL      = 15 * time.Second // TTL for error cases (shorter to retry sooner)  // @AI!!! DO NOT CHANGE THIS!!!!
	MaxCachedEntryTTL = 15 * time.Second // Maximum TTL for any cache entry // @AI!!! DO NOT CHANGE THIS!!!!
)

type MsgIdItemCache struct {
	mux         sync.RWMutex
	Pages       map[int]*MsgIdItemCachePage
	bucketCount int  // Current number of buckets
	itemCount   int  // Current number of items (cached for performance)
	resizing    bool // Flag to indicate if resize is in progress
}

type MsgIdItemCachePage struct {
	MsgIdItem *MessageIdItem
	Next      *MsgIdItemCachePage
	Prev      *MsgIdItemCachePage
	mux       sync.RWMutex // Mutex for this page to allow concurrent reads
}

func NewMsgIdItemCache() *MsgIdItemCache {
	MIICmutex.Lock()
	defer MIICmutex.Unlock()
	if MsgIdCache != nil {
		return MsgIdCache // Return existing cache if already created
	}
	MsgIdCache = &MsgIdItemCache{
		Pages:       make(map[int]*MsgIdItemCachePage, DefaultMsgIdCacheSize),
		bucketCount: DefaultMsgIdCacheSize, // Initialize with default size
		itemCount:   0,
		resizing:    false,
	}
	return MsgIdCache // Return the newly created cache
}

func (c *MsgIdItemCache) NewMsgIdItem(messageId string) *MessageIdItem {
	return &MessageIdItem{
		MessageId: messageId,
		//MessageIdHash:  ComputeMessageIDHash(messageId),
		GroupThreading: make(map[*string]*ThreadingInfo),
	}
}

func (c *MsgIdItemCache) GetORCreate(messageId string) *MessageIdItem {
	pageHash := c.FNVKey(messageId)

	// Check if we need to resize before proceeding
	c.checkAndResize()

	c.mux.RLock()
	page, exists := c.Pages[pageHash]
	c.mux.RUnlock()

	if !exists {
		msgIdItem := &MessageIdItem{
			MessageId: messageId,
			//MessageIdHash:      ComputeMessageIDHash(messageId),
			CachedEntryExpires: time.Now().Add(CachedEntryTTL), // Use configurable TTL for cache entries
		}
		c.mux.Lock()
		page, exists = c.Pages[pageHash]
		if !exists {
			// Create a new page for this hash bucket if it doesn't exist
			c.Pages[pageHash] = &MsgIdItemCachePage{
				MsgIdItem: msgIdItem,
			}
			c.itemCount++
			c.mux.Unlock()
			return msgIdItem
		}
		c.mux.Unlock()
		// Fall through to traverse the page chain that another goroutine created
	}

	//log.Printf("MsgIdItemCache: GetORCreate: msgId='%s' => pageHash %d exists", messageId, pageHash)
	//start := time.Now()
	//surfed := 0
	// Traverse the linked list to find the matching message ID
	for page != nil {
		page.mux.RLock()
		// No need for page locks since we hold the cache lock
		if page.MsgIdItem != nil && page.MsgIdItem.MessageId == messageId {
			page.mux.RUnlock()
			return page.MsgIdItem
		}
		if page.Next != nil {
			nextPage := page.Next
			page.mux.RUnlock()
			page = nextPage // Move to next
			continue
		}
		//fall through if page.Next is nil
		page.mux.RUnlock()

		// check again if page unchanged
		page.mux.Lock()
		if page.Next != nil {
			nextPage := page.Next
			page.mux.Unlock() // Unlock current page
			page = nextPage   // Move to next
			//surfed++
			continue
		}
		//fall through if page.Next is nil
		msgIdItem := &MessageIdItem{
			MessageId:          messageId,
			MessageIdHash:      ComputeMessageIDHash(messageId),
			CachedEntryExpires: time.Now().Add(CachedEntryTTL), // Use configurable TTL for cache entries
		}
		newPage := &MsgIdItemCachePage{
			MsgIdItem: msgIdItem, // No item found, will create a new one
			Prev:      page,      // Link to the previous page
			Next:      nil,       // No next page yet
		}
		page.Next = newPage
		c.mux.Lock()
		c.itemCount++
		c.mux.Unlock()
		page.mux.Unlock() // Unlock the current page
		//log.Printf("[OK] MsgIdItemCache: GetORCreate: msgId='%s' => created in pH %d (took %v) surfed=%d", messageId, pageHash, time.Since(start), surfed)
		return msgIdItem // Return the new item
	}
	//log.Printf("[??] MsgIdItemCache: GetORCreate: msgId='%s' => no match found in pH %d (took %v) surfed=%d", messageId, pageHash, time.Since(start), surfed)
	return nil // No matching item found and did not add?
}

// Stats returns cache statistics for monitoring and debugging
func (c *MsgIdItemCache) Stats() (buckets, items, maxChainLength int) {
	c.mux.RLock()
	defer c.mux.RUnlock()

	buckets = c.bucketCount // Total hash table size (not just occupied buckets)
	items = c.itemCount     // Use cached count for better performance
	maxChainLength = 0

	for _, page := range c.Pages {
		chainLength := 0
		for p := page; p != nil; p = p.Next {
			chainLength++
		}
		if chainLength > maxChainLength {
			maxChainLength = chainLength
		}
	}

	return buckets, items, maxChainLength
}

// DetailedStats returns comprehensive cache statistics for monitoring and debugging
func (c *MsgIdItemCache) DetailedStats() (totalBuckets, occupiedBuckets, items, maxChainLength int, loadFactor float64) {
	c.mux.RLock()
	defer c.mux.RUnlock()

	totalBuckets = c.bucketCount   // Total hash table size
	occupiedBuckets = len(c.Pages) // Number of buckets with at least one item
	items = c.itemCount            // Total number of items
	maxChainLength = 0

	for _, page := range c.Pages {
		chainLength := 0
		for p := page; p != nil; {
			chainLength++
			p.mux.RLock()
			nextP := p.Next
			p.mux.RUnlock()
			p = nextP
		}
		if chainLength > maxChainLength {
			maxChainLength = chainLength
		}
	}

	// Calculate load factor based on total buckets
	if totalBuckets > 0 {
		loadFactor = float64(items) / float64(totalBuckets)
	}

	return totalBuckets, occupiedBuckets, items, maxChainLength, loadFactor
}

func (c *MsgIdItemCache) Delete(messageId string) bool {
	pageHash := c.FNVKey(messageId)

	c.mux.RLock()
	page, exists := c.Pages[pageHash]
	c.mux.RUnlock()

	if !exists {
		return false
	}

	// Find the item to delete
	for page != nil {
		page.mux.RLock()
		if page.MsgIdItem != nil && page.MsgIdItem.MessageId == messageId {
			page.mux.RUnlock()

			// Found it - now delete
			page.mux.Lock()

			// Update links
			if page.Prev != nil {
				page.Prev.mux.Lock()
				page.Prev.Next = page.Next
				page.Prev.mux.Unlock()
			} else {
				// This is the first page in the bucket
				c.mux.Lock()
				if page.Next != nil {
					c.Pages[pageHash] = page.Next
					page.Next.Prev = nil
				} else {
					delete(c.Pages, pageHash)
				}
				c.mux.Unlock()
			}

			if page.Next != nil {
				page.Next.mux.Lock()
				page.Next.Prev = page.Prev
				page.Next.mux.Unlock()
			}

			// Clean up the MessageIdItem properly before unlinking
			if page.MsgIdItem != nil {
				c.cleanupMessageIdItem(page.MsgIdItem)
				page.MsgIdItem = nil
			}

			// Break page references to help GC
			page.Next = nil
			page.Prev = nil
			page.mux.Unlock()

			// Update item count
			c.mux.Lock()
			c.itemCount--
			c.mux.Unlock()

			return true
		}
		page.mux.RUnlock()
		page = page.Next
	}

	return false
}

// Clear removes all items from the cache
func (c *MsgIdItemCache) Clear() {
	c.mux.Lock()
	// Clean up all items properly and break references before clearing
	for _, page := range c.Pages {
		for p := page; p != nil; {
			next := p.Next
			if p.MsgIdItem != nil {
				c.cleanupMessageIdItem(p.MsgIdItem)
				p.MsgIdItem = nil
			}
			// Break page references to help GC
			p.Next = nil
			p.Prev = nil
			p = next
		}
	}
	c.Pages = make(map[int]*MsgIdItemCachePage, DefaultMsgIdCacheSize)
	c.bucketCount = DefaultMsgIdCacheSize
	c.itemCount = 0
	c.mux.Unlock()
}

// cleanupMessageIdItem properly clears all internal references in a MessageIdItem
// to help garbage collection and prevent memory leaks
func (c *MsgIdItemCache) cleanupMessageIdItem(item *MessageIdItem) {
	if item == nil {
		return
	}

	item.Mux.Lock()

	// Clear GroupThreading map and its contents
	if item.GroupThreading != nil {
		for k := range item.GroupThreading {
			// Clear the ThreadingInfo contents (though it's a struct, not pointers)
			delete(item.GroupThreading, k)
		}
		item.GroupThreading = nil
	}

	// Clear other pointer fields (but don't nil the newsgroup pointer as it's reused)

	// Clear string fields
	item.MessageId = ""
	item.MessageIdHash = ""
	item.StorageToken = ""

	// Reset other fields to zero values
	item.ArtNum = 0
	item.Arrival = 0
	item.Response = 0
	item.GroupName = nil
	item.Mux.Unlock()
}

// FNVKey efficiently calculates a hash for the given string
// We use a simple approach that's still efficient for moderate loads
func (c *MsgIdItemCache) FNVKey(str string) int {
	if c == nil {
		log.Printf("Error MsgIdItemCache not initialized!")
		return CaseError
	}
	h := fnv.New32a()
	h.Write([]byte(str))
	c.mux.RLock()
	i := uint32(c.bucketCount)
	c.mux.RUnlock()
	retval := int(h.Sum32() % i)
	return retval
}

// checkAndResize checks if the cache needs to be resized and triggers resize if needed
func (c *MsgIdItemCache) checkAndResize() {
	c.mux.RLock()
	loadFactor := float64(c.itemCount) / float64(c.bucketCount)
	resizing := c.resizing
	c.mux.RUnlock()

	// Only resize if load factor exceeds threshold and we're not already resizing
	if loadFactor > MaxLoadFactor && !resizing {
		c.resize()
	}
}

// resize doubles the cache size and rehashes all items
func (c *MsgIdItemCache) resize() {
	c.mux.Lock()

	// Double-check that we still need to resize and we're not already resizing
	if c.resizing || float64(c.itemCount)/float64(c.bucketCount) <= MaxLoadFactor {
		c.mux.Unlock()
		return
	}

	// Check upper limit
	newSize := c.bucketCount * ResizeMultiplier
	if newSize > UpperLimitMsgIdCacheSize {
		c.mux.Unlock()
		return // Don't resize beyond upper limit
	}
	c.resizing = true
	oldPages := c.Pages

	// Create new larger map
	c.bucketCount = newSize
	c.Pages = make(map[int]*MsgIdItemCachePage, c.bucketCount)
	c.itemCount = 0 // Will be recounted during rehash

	c.mux.Unlock()

	// Rehash all items from old map to new map
	for _, page := range oldPages {
		c.rehashChain(page)
	}

	// Clear reference to old pages map to help GC
	oldPages = nil

	c.mux.Lock()
	c.resizing = false
	c.mux.Unlock()
}

// rehashChain rehashes a chain of pages into the new bucket structure
func (c *MsgIdItemCache) rehashChain(page *MsgIdItemCachePage) {
	for page != nil {
		nextPage := page.Next // Store next before we potentially break the chain

		if page.MsgIdItem != nil {
			messageId := page.MsgIdItem.MessageId
			newHash := c.FNVKey(messageId)

			// Insert into new bucket
			c.mux.Lock()
			existingPage, exists := c.Pages[newHash]
			if !exists {
				// Create new page for this hash bucket
				c.Pages[newHash] = &MsgIdItemCachePage{
					MsgIdItem: page.MsgIdItem,
				}
				c.itemCount++
			} else {
				// Add to end of existing chain
				for existingPage.Next != nil {
					existingPage = existingPage.Next
				}
				existingPage.Next = &MsgIdItemCachePage{
					MsgIdItem: page.MsgIdItem,
					Prev:      existingPage,
				}
				c.itemCount++
			}
			c.mux.Unlock()
		}

		// Break references in old page to help GC
		// NOTE: Don't cleanup the MsgIdItem itself since it's now referenced by the new page
		page.Next = nil
		page.Prev = nil
		page.MsgIdItem = nil

		page = nextPage
	}
}

// GetResizeInfo returns information about cache resizing for monitoring
func (c *MsgIdItemCache) GetResizeInfo() (bucketCount int, itemCount int, loadFactor float64, isResizing bool) {
	c.mux.RLock()
	defer c.mux.RUnlock()

	bucketCount = c.bucketCount
	itemCount = c.itemCount
	loadFactor = float64(c.itemCount) / float64(c.bucketCount)
	isResizing = c.resizing

	return
}

// GetMsgIdFromCache retrieves threading information for a message ID in a specific group
// This replaces the functionality from MsgTmpCache.GetMsgIdFromTmpCache
func (c *MsgIdItemCache) GetMsgIdFromCache(newsgroupPtr *string, messageID string) (int64, int64, bool) {
	pageHash := c.FNVKey(messageID)

	c.mux.RLock()
	page, exists := c.Pages[pageHash]
	c.mux.RUnlock()

	if !exists {
		return 0, 0, false
	}
	var item *MessageIdItem
	// Traverse the chain to find the message ID
	for page != nil {
		page.mux.RLock()
		if page.MsgIdItem != nil {
			item = page.MsgIdItem
		}
		nextPage := page.Next
		page.mux.RUnlock()
		if item != nil {
			item.Mux.RLock()
			if item.MessageId == messageID {
				// Check group-specific threading information
				threadingInfo, hasGroupInfo := item.GroupThreading[newsgroupPtr]
				if hasGroupInfo {
					defer item.Mux.RUnlock()
					return threadingInfo.ArtNum, threadingInfo.RootArticle, threadingInfo.IsThreadRoot
				}
			}
			item.Mux.RUnlock()
		}
		item = nil
		page = nextPage
	} // end for
	return 0, 0, false
}

// SetThreadingInfo sets threading information for a message ID in a specific group
func (c *MsgIdItemCache) SetThreadingInfo(messageID string, rootArticle int64, isThreadRoot bool) bool {
	// This is a compatibility method that requires the item to already have a group set
	// For new code, use SetThreadingInfoForGroup instead with explicit group parameter
	item := c.GetORCreate(messageID)
	if item == nil {
		return false
	}

	item.Mux.Lock()
	defer item.Mux.Unlock()

	// Threading operations REQUIRE a group - no defaults allowed
	if item.GroupName == nil {
		log.Printf("ERROR: SetThreadingInfo: Threading requires group context for messageID %s - use SetThreadingInfoForGroup", messageID)
		return false
	}

	// Initialize GroupThreading map if needed
	if item.GroupThreading == nil {
		item.GroupThreading = make(map[*string]*ThreadingInfo)
	}

	// Create or update threading info for the primary group
	item.GroupThreading[item.GroupName] = &ThreadingInfo{
		RootArticle:  rootArticle,
		ChildArticle: 0, // Not used in current implementation
		IsThreadRoot: isThreadRoot,
		ArtNum:       rootArticle, // Use root as art num for compatibility
		//TmpExpires:   time.Now().Add(TmpCacheTTL),
	}

	return true
}

// SetThreadingInfoForGroup sets threading information for a message ID in a specific group
func (c *MsgIdItemCache) SetThreadingInfoForGroup(newsgroupPtr *string, messageID string, artNum int64, rootArticle int64, isThreadRoot bool) bool {
	item := c.GetORCreate(messageID)
	if item == nil {
		return false
	}

	item.Mux.Lock()
	defer item.Mux.Unlock()

	// Initialize GroupThreading map if needed
	if item.GroupThreading == nil {
		item.GroupThreading = make(map[*string]*ThreadingInfo)
	}

	// Create or update threading info for this group
	item.GroupThreading[newsgroupPtr] = &ThreadingInfo{
		RootArticle:  rootArticle,
		ChildArticle: 0, // Not used in current implementation
		IsThreadRoot: isThreadRoot,
		ArtNum:       artNum,
		//TmpExpires:   time.Now().Add(TmpCacheTTL),
	}

	return true
}

// AddMsgIdToCache adds a message ID with article number to the cache for a specific group
func (c *MsgIdItemCache) AddMsgIdToCache(newsgroupPtr *string, messageID string, articleNum int64) bool {
	item := c.GetORCreate(messageID)
	if item == nil {
		return false
	}

	item.Mux.Lock()
	defer item.Mux.Unlock()

	// Initialize GroupThreading map if needed
	if item.GroupThreading == nil {
		item.GroupThreading = make(map[*string]*ThreadingInfo)
	}

	// Get existing threading info for this group or create new
	threadingInfo, exists := item.GroupThreading[newsgroupPtr]
	if !exists {
		threadingInfo = &ThreadingInfo{}
		item.GroupThreading[newsgroupPtr] = threadingInfo
	}

	// Set article number for this group
	threadingInfo.ArtNum = articleNum

	// Update primary group if not set
	if item.GroupName == nil {
		item.GroupName = newsgroupPtr
		item.Arrival = time.Now().Unix()
	}

	return true
}

// CleanExpiredEntries removes expired temporary cache entries
// This replaces the functionality from MsgTmpCache.CronClean
// Efficiently unlinks expired items during chain traversal to avoid double-walking
func (c *MsgIdItemCache) CleanExpiredEntries() int {

	c.mux.RLock()
	// Get a snapshot of hash buckets to iterate over
	buckets := make(chan int, len(c.Pages))
	for i := range c.Pages {
		buckets <- i
	}
	close(buckets)
	c.mux.RUnlock()

	start := time.Now()
	cleaned := 0
	now := start
	countCaseLocked := 0
	countCaseWrite := 0
	countCaseError := 0
	countCaseDupes := 0
	countDeleteDupes := 0
	countDeleteWrite := 0
	// Process each hash bucket
	for i := range buckets {
		c.mux.RLock()
		firstPage, exists := c.Pages[i]
		c.mux.RUnlock()
		if !exists {
			continue // Bucket was deleted by another goroutine
		}

		// Walk the chain and unlink expired items directly
		page := firstPage
		var prevPage *MsgIdItemCachePage = nil

		for page != nil {
			page.mux.RLock()
			nextPage := page.Next // Store next before potential unlinking
			item := page.MsgIdItem
			page.mux.RUnlock()

			shouldDelete := false

			if item != nil {
				item.Mux.Lock()
				if item.CachedEntryExpires.IsZero() {
					log.Printf("[CACHE-CLEANUP] Item with zero CachedEntryExpires")
					item.CachedEntryExpires = time.Now().Add(CachedEntryTTL) // Set default expiration if zero
				}
				// Check if item should be cleaned up based on expiration criteria
				switch item.Response {
				case CaseDupes:
					// Items successfully written to disk - remove when cache TTL expires
					if now.After(item.CachedEntryExpires) {
						shouldDelete = true
						countDeleteDupes++
					}
					countCaseDupes++
				case CaseWrite:
					// Items ARE still being processed - DO NOT remove while processing
					if now.After(item.CachedEntryExpires) {
						countDeleteWrite++
					}
					countCaseWrite++
				case CaseError:
					// Error cases - remove when TTL expires to allow retry
					if now.After(item.CachedEntryExpires) {
						shouldDelete = true
					}
					countCaseError++
				case CaseLock:
					// Lock cases - DO NOT remove while locked
					countCaseLocked++
				default:
					// Unknown/uninitialized state - remove if expired
					// This handles Response=0 (uninitialized) and other unknown states
					if now.After(item.CachedEntryExpires) {
						//shouldDelete = true
						log.Printf("[CACHE-CLEANUP] Unknown/uninitialized state '%x' for item %#v", item.Response, item)
					}
				}
				item.Mux.Unlock()
			}
			if shouldDelete {
				// Unlink this page from the chain
				page.mux.Lock()

				if prevPage != nil {
					// Middle or end of chain
					prevPage.mux.Lock()
					prevPage.Next = page.Next
					prevPage.mux.Unlock()
				} else {
					// First page in chain
					c.mux.Lock()
					if page.Next != nil {
						c.Pages[i] = page.Next
						page.Next.Prev = nil
					} else {
						// Last item in bucket - delete the bucket
						delete(c.Pages, i)
					}
					c.mux.Unlock()
				}

				if page.Next != nil {
					page.Next.mux.Lock()
					page.Next.Prev = prevPage
					page.Next.mux.Unlock()
				}

				// Clean up the MessageIdItem properly before unlinking
				if page.MsgIdItem != nil {
					c.cleanupMessageIdItem(page.MsgIdItem)
					page.MsgIdItem = nil
				}

				// Break page references to help GC
				page.Next = nil
				page.Prev = nil
				page.mux.Unlock()

				// Update item count
				c.mux.Lock()
				c.itemCount--
				c.mux.Unlock()

				cleaned++

				// Don't update prevPage since we removed current page
			} else {
				// Keep this page - it becomes the new previous page
				prevPage = page
			}

			page = nextPage
		}
	}

	if cleaned > 0 {
		c.mux.RLock()
		log.Printf("[CACHE-CLEANUP] Cleaned %d expired cache entries: CaseWrite=%d, CaseDupes=%d, CaseError=%d, CaseLock=%d expired but not deleted??==countDeleteWrite=%d [took %s]", cleaned, countCaseWrite, countCaseDupes, countCaseError, countCaseLocked, countDeleteWrite, time.Since(start))
		c.mux.RUnlock()
	} else {
		// Add debug information when nothing is cleaned up
		//log.Printf("[CACHE-CLEANUP] No expired cache entries found - checked %d buckets with CaseWrite=%d, CaseDupes=%d, CaseError=%d, CaseLock=%d items (took %s)", len(buckets), countCaseWrite, countCaseDupes, countCaseError, countCaseLocked, time.Since(start))
	}
	return cleaned
}

// StartCleanupRoutine starts a background goroutine to clean expired entries
func (c *MsgIdItemCache) StartCleanupRoutine() {
	go func() {
		for {
			time.Sleep(TmpCacheTTL)
			start := time.Now()
			cleaned := c.CleanExpiredEntries()
			// Adjust cleanup frequency based on cache size
			c.mux.RLock()
			currentSize := c.itemCount
			c.mux.RUnlock()

			if cleaned > 0 {
				// Log cleanup activity for monitoring
				log.Printf("[CACHE-CLEANUP] Cleaned %d expired cache entries (current size: %d items) took %v", cleaned, currentSize, time.Since(start))
			}
			/*
				// Adaptive cleanup frequency based on cache size and cleanup activity
				var newInterval time.Duration
				if currentSize > 300000 || cleaned > 100000 {
					// High load mode: cleanup every 2 seconds
					newInterval = 2 * time.Second
				} else if currentSize > 100000 || cleaned > 10000 {
					// Medium load mode: cleanup every 5 seconds
					newInterval = 5 * time.Second
				} else {
					// Normal mode: cleanup every 10 seconds
					newInterval = 10 * time.Second
				}

				// Reset ticker if interval changed
				if newInterval != currentInterval {
					ticker.Stop()
					ticker = time.NewTicker(newInterval)
					currentInterval = newInterval
					log.Printf("[CACHE-CLEANUP] Adjusted cleanup interval to %v (cache size: %d, cleaned: %d)", newInterval, currentSize, cleaned)
				}
			*/
		}
	}()
}

// GetOrCreateForGroup gets or creates a message ID item for a specific group
// This provides group-specific functionality similar to MsgTmpCache
func (c *MsgIdItemCache) GetOrCreateForGroup(messageID string, newsgroupPtr *string) *MessageIdItem {
	item := c.GetORCreate(messageID)
	if item == nil {
		return nil
	}

	item.Mux.Lock()
	defer item.Mux.Unlock()

	// If this is the first time we're seeing this messageID for this group,
	// or if it's for a different group, update the group information
	if item.GroupName == nil || item.GroupName != newsgroupPtr {
		item.GroupName = newsgroupPtr
		item.Arrival = time.Now().Unix()
	}

	return item
}

// HasMessageIDInGroup checks if a message ID exists in a specific group and hasn't expired
func (c *MsgIdItemCache) HasMessageIDInGroup(messageID string, newsgroupPtr *string) bool {
	artNum, _, _ := c.GetMsgIdFromCache(newsgroupPtr, messageID)
	return artNum != 0 // If artNum is 0, the item wasn't found or expired
}

// FindThreadRootInCache searches for thread root in cache by following references
// This replaces the functionality from MsgTmpCache.FindThreadRootInCache
func (c *MsgIdItemCache) FindThreadRootInCache(newsgroupPtr *string, references []string) *MessageIdItem {
	// Try to find any of the referenced messages in cache, starting with most recent
	for i := len(references) - 1; i >= 0; i-- {
		refMessageID := references[i]
		artNum, rootArticle, isThreadRoot := c.GetMsgIdFromCache(newsgroupPtr, refMessageID)

		if artNum != 0 { // Found the referenced message
			// If this is a thread root, return it
			if isThreadRoot {
				item := c.GetORCreate(refMessageID)
				return item
			}

			// If it has a root reference, try to find the root
			if rootArticle > 0 && rootArticle != artNum {
				// Look for the root article in cache by iterating through all items
				// Note: This is less efficient than the old method but maintains functionality
				// A future optimization could be to add an artNum->messageID lookup map
				c.mux.RLock()
				pages := make([]*MsgIdItemCachePage, 0, len(c.Pages))
				for _, page := range c.Pages {
					pages = append(pages, page)
				}
				c.mux.RUnlock()

				for _, page := range pages {
					for p := page; p != nil; p = p.Next {
						p.mux.RLock()
						if p.MsgIdItem != nil {
							item := p.MsgIdItem
							p.mux.RUnlock()

							item.Mux.RLock()
							if threadingInfo, hasGroup := item.GroupThreading[newsgroupPtr]; hasGroup &&
								threadingInfo.ArtNum > 0 && threadingInfo.ArtNum == rootArticle &&
								threadingInfo.IsThreadRoot {
								item.Mux.RUnlock()
								return item
							}
							item.Mux.RUnlock()
						} else {
							p.mux.RUnlock()
						}
					}
				}
			}
		}
	}
	return nil
}

// UpdateThreadRootToTmpCache updates an existing cache entry with thread root information
// This replaces the functionality from MsgTmpCache.UpdateThreadRootToTmpCache
func (c *MsgIdItemCache) UpdateThreadRootToTmpCache(newsgroupPtr *string, messageID string, rootArticle int64, isThreadRoot bool) bool {
	// Get or create the item
	item := c.GetORCreate(messageID)
	if item == nil {
		return false
	}

	item.Mux.Lock()
	defer item.Mux.Unlock()

	// Initialize GroupThreading map if needed
	if item.GroupThreading == nil {
		item.GroupThreading = make(map[*string]*ThreadingInfo)
	}

	// Get existing threading info for this group or create new
	threadingInfo, exists := item.GroupThreading[newsgroupPtr]
	if !exists {
		threadingInfo = &ThreadingInfo{}
		item.GroupThreading[newsgroupPtr] = threadingInfo
	}

	// Set threading information
	threadingInfo.RootArticle = rootArticle
	threadingInfo.IsThreadRoot = isThreadRoot

	// Update primary group if not set or different
	if item.GroupName == nil || item.GroupName != newsgroupPtr {
		item.GroupName = newsgroupPtr
		item.Arrival = time.Now().Unix()
	}

	return true
}

// MsgIdExists checks if a message ID exists in the cache for a specific group
// This replaces the functionality from MsgTmpCache.MsgIdExists
func (c *MsgIdItemCache) MsgIdExists(newsgroupPtr *string, messageID string) *MessageIdItem {
	artNum, _, _ := c.GetMsgIdFromCache(newsgroupPtr, messageID)
	if artNum != 0 {
		return c.GetORCreate(messageID)
	}
	return nil
}
