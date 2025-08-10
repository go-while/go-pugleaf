package processor

// stub functionality is now in history_MsgIdItemCache.go
/*
import (
	"sync"
	"time"
)

// DefaultTmpCacheTime expiration time for temporary cache
//var DefaultTmpCacheTime = 60 * time.Second
//var MaxTmpCacheSize = 32 * 1024 // message-id strings!

//type MsgTmpCache struct {
	mux sync.RWMutex
	// TmpCache is a temporary cache for message-id strings
	cache map[string]map[string]*MsgIdTmpCacheItem
}

//func NewMsgTmpCache() *MsgTmpCache {
	newcache := &MsgTmpCache{
		cache: make(map[string]map[string]*MsgIdTmpCacheItem, 32),
	}
	go newcache.CronClean()
	return newcache
}

//func (c *MsgTmpCache) CronClean() {
	for {
		time.Sleep(15 * time.Second) // Run cleanup every 5s
		c.mux.RLock()
		if len(c.cache) == 0 {
			c.mux.RUnlock()
			continue // If cache is empty, skip cleanup
		}
		c.mux.RUnlock()

		now := time.Now()
		c.mux.Lock()
		// Iterate over all groups and their message IDs
		for group, messages := range c.cache {
			for messageID := range messages {
				// Check if the message ID has expired
				if entry, ok := messages[messageID]; ok && now.After(entry.expires) {
					delete(messages, messageID) // Remove expired message ID
				}
			}
			// If no messages left in the group, remove the group
			if len(messages) == 0 {
				delete(c.cache, group)
			}
		}
		c.mux.Unlock()
	}
}

type MsgIdTmpCacheItem struct {
	MessageId    string
	ArtNum       int64
	RootArticle  int64 // Thread root article number (0 if this IS the root)
	ChildArticle int64 // Not used in current implementation
	IsThreadRoot bool  // True if this article is a thread root
	expires      time.Time
}


//func (c *MsgTmpCache) AddMsgIdToTmpCache(group string, messageID string, articleNum int64) bool {
	//start := time.Now()
	//log.Printf("MsgTmpCache: AddMsgIdToTmpCache called for group %s, messageID %s", group, messageID)
	//defer log.Printf("MsgTmpCache: AddMsgIdToTmpCache returned. called for group %s, messageID %s", group, messageID)
	c.mux.Lock()
	defer c.mux.Unlock()
	if _, ok := c.cache[group]; !ok {
		c.cache[group] = make(map[string]*MsgIdTmpCacheItem, 128) // Initialize group cache if it doesn't exist
	}
	c.cache[group][messageID] = &MsgIdTmpCacheItem{
		MessageId:    messageID,
		ArtNum:       articleNum,
		RootArticle:  0,     // Will be set later when threading is processed
		IsThreadRoot: false, // Will be set to true if this becomes a thread root
		expires:      time.Now().Add(DefaultTmpCacheTime),
	}
	//log.Printf("MsgTmpCache: Added message ID %s to group %s in %.3f ms", messageID, group, time.Since(start).Seconds()*1000)
	return true // Successfully added
}

//func (c *MsgTmpCache) MsgIdExists(group string, messageID string) *MsgIdTmpCacheItem {
	c.mux.RLock()
	defer c.mux.RUnlock()
	if c.cache == nil {
		log.Printf("MsgTmpCache: cache is nil (not initialized?), cannot check existence of message ID %s in group %s", messageID, group)
		return nil // If the cache is nil, return false
	}
	if c.cache[group] == nil {
		return nil // If the group cache doesn't exist, return false
	}
	if groupCache, ok := c.cache[group]; ok {
		if item, exists := groupCache[messageID]; exists {
			return item
		}
	}
	return nil
}

//func (c *MsgTmpCache) Clear() {
	c.mux.Lock()
	c.cache = make(map[string]map[string]*MsgIdTmpCacheItem)
	c.mux.Unlock()
}

// UpdateThreadRootToTmpCache updates an existing cache entry with thread root information
//func (c *MsgTmpCache) UpdateThreadRootToTmpCache(group string, messageID string, rootArticle int64, isThreadRoot bool) bool {
	c.mux.Lock()
	defer c.mux.Unlock()

	if c.cache[group] == nil {
		return false // Group doesn't exist in cache
	}

	if item, exists := c.cache[group][messageID]; exists {
		item.RootArticle = rootArticle
		item.IsThreadRoot = isThreadRoot
		item.expires = time.Now().Add(DefaultTmpCacheTime) // Refresh expiration
		return true
	}
	return false
}

// AddThreadRootToTmpCache adds a new thread root entry to the cache
//func (c *MsgTmpCache) AddThreadRootToTmpCache(group string, messageID string, articleNum int64) bool {
	c.mux.Lock()
	defer c.mux.Unlock()

	if _, ok := c.cache[group]; !ok {
		c.cache[group] = make(map[string]*MsgIdTmpCacheItem, 128)
	}

	c.cache[group][messageID] = &MsgIdTmpCacheItem{
		MessageId:    messageID,
		ArtNum:       articleNum,
		RootArticle:  articleNum, // Root points to itself
		IsThreadRoot: true,
		expires:      time.Now().Add(DefaultTmpCacheTime),
	}
	return true
}

// FindThreadRootInCache searches for thread root in cache by following references
//func (c *MsgTmpCache) FindThreadRootInCache(group string, references []string) *MsgIdTmpCacheItem {
	c.mux.RLock()
	defer c.mux.RUnlock()

	if c.cache[group] == nil {
		return nil
	}

	// Try to find any of the referenced messages in cache, starting with most recent
	for i := len(references) - 1; i >= 0; i-- {
		refMessageID := references[i]
		if item, exists := c.cache[group][refMessageID]; exists {
			// If this is a thread root, return it
			if item.IsThreadRoot {
				return item
			}
			// If it has a root reference, try to find the root
			if item.RootArticle > 0 && item.RootArticle != item.ArtNum {
				// Look for the root article in cache
				for _, cachedItem := range c.cache[group] {
					if cachedItem.ArtNum == item.RootArticle && cachedItem.IsThreadRoot {
						return cachedItem
					}
				}
			}
		}
	}
	return nil
}
*/
