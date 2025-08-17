package database

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// README for this files is: /go-pugleaf/internal/database/thread-cache-readme.md

// ThreadCacheEntry represents a cached thread
type ThreadCacheEntry struct {
	ThreadRoot      int64
	RootDate        time.Time
	MessageCount    int
	ChildArticles   string
	LastChildNumber int64
	LastActivity    time.Time
	CreatedAt       time.Time
}

// InitializeThreadCache creates a new cache entry for a thread root
func (db *Database) InitializeThreadCache(groupDBs *GroupDBs, threadRoot int64, rootArticle *models.Article) error {

	query := `
		INSERT INTO thread_cache (
			thread_root, root_date, message_count, child_articles, last_child_number, last_activity
		) VALUES (?, ?, 1, '', ?, ?)
		ON CONFLICT(thread_root) DO UPDATE SET
			root_date = excluded.root_date,
			last_child_number = excluded.last_child_number,
			last_activity = excluded.last_activity
	`

	// Format dates as UTC strings to avoid timezone encoding issues
	rootDateUTC := rootArticle.DateSent.UTC().Format("2006-01-02 15:04:05")

	_, err := retryableExec(groupDBs.DB, query,
		threadRoot,
		rootDateUTC,
		threadRoot, // last_child_number starts as the root itself
		rootDateUTC,
	)

	if err != nil {
		return fmt.Errorf("failed to initialize thread cache for root %d: %w", threadRoot, err)
	}

	return nil
}

// UpdateThreadCache updates an existing cache entry when a reply is added
func (db *Database) UpdateThreadCache(groupDBs *GroupDBs, threadRoot int64, childArticleNum int64, childDate time.Time) error {

	// First, get the current cache entry
	var currentChildren string
	var currentCount int

	query := `SELECT child_articles, message_count FROM thread_cache WHERE thread_root = ?`
	err := retryableQueryRowScan(groupDBs.DB, query, []interface{}{threadRoot}, &currentChildren, &currentCount)
	if err != nil {
		// If the thread cache entry doesn't exist, queue it for batch initialization
		// This can happen if the root article was processed without initializing the cache
		//log.Printf("Thread cache entry for root %d not found, queuing for batch initialization", threadRoot)

		// Create a minimal article object for initialization
		rootArticle := &models.Article{
			DateSent: childDate, // Use child date as fallback for root date
		}

		// Initialize directly instead of batch processing
		err = db.InitializeThreadCache(groupDBs, threadRoot, rootArticle)
		if err != nil {
			log.Printf("Failed to initialize thread cache for root %d: %v", threadRoot, err)
			// Continue with defaults to allow the update to proceed
		}

		// Update memory cache immediately so subsequent operations can use it
		if db.MemThreadCache != nil {
			// Initialize with minimal values - batch processing will update the database
			db.MemThreadCache.UpdateThreadMetadata(groupDBs.Newsgroup, threadRoot, 1, childDate, "")
		}

		// For now, set defaults so we can continue with the update
		currentChildren = ""
		currentCount = 0
	}

	// Add the new child to the list
	var newChildren string
	if currentChildren == "" {
		newChildren = strconv.FormatInt(childArticleNum, 10)
	} else {
		newChildren = currentChildren + "," + strconv.FormatInt(childArticleNum, 10)
	}

	// Update the cache
	updateQuery := `
		UPDATE thread_cache
		SET child_articles = ?,
			message_count = ?,
			last_child_number = ?,
			last_activity = ?
		WHERE thread_root = ?
	`

	// Format childDate as UTC string to avoid timezone encoding issues
	childDateUTC := childDate.UTC().Format("2006-01-02 15:04:05")

	_, err = retryableExec(groupDBs.DB, updateQuery,
		newChildren,
		currentCount+1,
		childArticleNum,
		childDateUTC,
		threadRoot,
	)

	if err != nil {
		return fmt.Errorf("failed to update thread cache for root %d: %w", threadRoot, err)
	}

	// Update memory cache
	if db.MemThreadCache != nil {
		db.MemThreadCache.UpdateThreadMetadata(groupDBs.Newsgroup, threadRoot, currentCount+1, childDate, newChildren)
	}

	return nil
}

var MemCacheThreadsExpiry = 5 * time.Minute // Default expiry for thread cache entries TODO should match cron cycle

// MemGroupThreadCache holds all thread cache data for a single newsgroup
type MemGroupThreadCache struct {
	Expiry            time.Time                   // When this group cache expires
	CountThreads      int64                       // Total thread count
	ThreadRoots       []int64                     // Ordered thread roots by last_activity DESC
	ThreadRootsTS     time.Time                   // When ThreadRoots was last updated
	ThreadMeta        map[int64]*ThreadCacheEntry // [thread_root] -> metadata
	ThreadMetaTS      map[int64]time.Time         // [thread_root] -> last updated
	CacheWindowOffset int64                       // The database offset where this cache window starts
}

type MemCachedThreads struct {
	mux    sync.RWMutex
	Groups map[string]*MemGroupThreadCache // [group] -> all cache data
}

func NewMemCachedThreads() *MemCachedThreads {
	mem := &MemCachedThreads{
		Groups: make(map[string]*MemGroupThreadCache, 256),
	}
	go mem.CleanCron()
	return mem
}

func (mem *MemCachedThreads) CleanCron() {
	// Periodically clean expired thread roots and metadata
	for {
		time.Sleep(15 * time.Second) // TODO (mem *MemCachedThreads) CleanCron hardcoded run every

		mem.mux.Lock()
		if len(mem.Groups) == 0 {
			//log.Printf("[CACHE:THREADS] No cached thread roots to clean")
			mem.mux.Unlock()
			continue // Nothing to clean
		}
		//log.Printf("[CACHE:THREADS] Cached: %d (run cleanup)", len(mem.Groups))
		cleaned := 0
		now := time.Now()
		// Clean expired thread roots
		for group, groupCache := range mem.Groups {
			// FIX: expire when Expiry is BEFORE now (not after)
			if groupCache.Expiry.Before(now) {
				log.Printf("[CACHE:THREADS] Clean thread roots ng: '%s'", group)
				delete(mem.Groups, group)
				cleaned++
			}
		}
		log.Printf("[CACHE:THREADS] Cleaned: %d | Cached: %d", cleaned, len(mem.Groups))
		mem.mux.Unlock()
	}
}

func (mem *MemCachedThreads) GetMemCachedTreadsCount(group string) int64 {
	startTime := time.Now()
	// Get the count of threads in the cache
	mem.mux.RLock()
	defer mem.mux.RUnlock()

	groupCache, exists := mem.Groups[group]
	if !exists {
		log.Printf("[PERF:COUNT] GetMemCachedTreadsCount miss took %v for group '%s'", time.Since(startTime), group)
		return 0 // No threads for this group
	}

	log.Printf("[MEM:HIT] thread count for group '%s': %d", group, groupCache.CountThreads)
	log.Printf("[PERF:COUNT] GetMemCachedTreadsCount hit took %v for group '%s'", time.Since(startTime), group)
	return groupCache.CountThreads
}

// GetCachedThreads retrieves cached thread data with pagination (thread list only - no children)
// First tries memory cache, falls back to database if cache miss
func (db *Database) GetCachedThreads(groupDBs *GroupDBs, page int64, pageSize int64) ([]*models.ForumThread, int64, error) {
	startTime := time.Now()
	log.Printf("[PERF:THREADS] Starting GetCachedThreads for group '%s', page %d, pageSize %d", groupDBs.Newsgroup, page, pageSize)

	// Try memory cache first (fast path)
	if db.MemThreadCache != nil {
		cacheStartTime := time.Now()
		if threads, count, hit := db.MemThreadCache.GetCachedThreadsFromMemory(db, groupDBs, groupDBs.Newsgroup, page, pageSize); hit {
			log.Printf("[PERF:THREADS] Memory cache HIT took %v for group '%s' (%d threads)", time.Since(cacheStartTime), groupDBs.Newsgroup, len(threads))
			log.Printf("[PERF:THREADS] Total GetCachedThreads took %v (cache hit)", time.Since(startTime))
			return threads, count, nil
		}
		log.Printf("[PERF:THREADS] Memory cache MISS took %v for group '%s'", time.Since(cacheStartTime), groupDBs.Newsgroup)

		// Cache miss - refresh from database
		refreshStartTime := time.Now()
		log.Printf("[MEM:MISS] Refreshing thread cache for group '%s'", groupDBs.Newsgroup)
		if err := db.MemThreadCache.RefreshThreadCache(db, groupDBs, groupDBs.Newsgroup, page, pageSize); err != nil {
			log.Printf("Failed to refresh thread cache: %v", err)
			// Continue to database fallback
		} else {
			log.Printf("[PERF:THREADS] RefreshThreadCache took %v for group '%s'", time.Since(refreshStartTime), groupDBs.Newsgroup)
			// Try memory cache again after refresh
			retryStartTime := time.Now()
			if threads, count, hit := db.MemThreadCache.GetCachedThreadsFromMemory(db, groupDBs, groupDBs.Newsgroup, page, pageSize); hit {
				log.Printf("[PERF:THREADS] Memory cache retry took %v for group '%s' (%d threads)", time.Since(retryStartTime), groupDBs.Newsgroup, len(threads))
				log.Printf("[PERF:THREADS] Total GetCachedThreads took %v (after refresh)", time.Since(startTime))
				return threads, count, nil
			}
		}
	} else {
		log.Printf("[WARN] MemThreadCache is nil")
	}
	log.Printf("[PERF:THREADS] Total GetCachedThreads FAILED took %v", time.Since(startTime))
	return nil, 0, fmt.Errorf("no cached threads found for group '%s'", groupDBs.Newsgroup)
}

// GetCachedThreadReplies retrieves paginated replies for a specific thread
func (db *Database) GetCachedThreadReplies(groupDBs *GroupDBs, threadRoot int64, page int, pageSize int) ([]*models.Overview, int, error) {
	// Get the cached thread entry
	var childArticles string
	var totalReplies int

	query := `SELECT child_articles, message_count FROM thread_cache WHERE thread_root = ?`
	err := retryableQueryRowScan(groupDBs.DB, query, []interface{}{threadRoot}, &childArticles, &totalReplies)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get thread cache for root %d: %w", threadRoot, err)
	}

	// Subtract 1 from message_count since it includes the root
	totalReplies = totalReplies - 1
	if totalReplies <= 0 {
		return []*models.Overview{}, 0, nil
	}

	// Parse child article numbers
	childNums := strings.Split(childArticles, ",")
	if len(childNums) == 0 || (len(childNums) == 1 && childNums[0] == "") {
		return []*models.Overview{}, 0, nil
	}

	// Calculate pagination for replies
	offset := (page - 1) * pageSize
	end := offset + pageSize
	if end > len(childNums) {
		end = len(childNums)
	}
	if offset >= len(childNums) {
		return []*models.Overview{}, totalReplies, nil
	}

	// Get only the slice of children for this page
	pageChildNums := childNums[offset:end]

	// Build query with limited placeholders (max pageSize, typically 25)
	placeholders := strings.Repeat("?,", len(pageChildNums))
	placeholders = placeholders[:len(placeholders)-1] // Remove trailing comma

	childQuery := fmt.Sprintf(`
		SELECT article_num, subject, from_header, date_sent, date_string,
			   message_id, "references", bytes, lines, reply_count, downloaded
		FROM articles
		WHERE article_num IN (%s) AND hide = 0
		ORDER BY date_sent ASC
	`, placeholders)

	// Convert pageChildNums to interface{} slice for query
	args := make([]interface{}, len(pageChildNums))
	for i, num := range pageChildNums {
		args[i] = num
	}

	rows, err := retryableQuery(groupDBs.DB, childQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query thread replies: %w", err)
	}
	defer rows.Close()

	var replies []*models.Overview
	for rows.Next() {
		reply := &models.Overview{}
		err := rows.Scan(
			&reply.ArticleNum, &reply.Subject, &reply.FromHeader,
			&reply.DateSent, &reply.DateString, &reply.MessageID,
			&reply.References, &reply.Bytes, &reply.Lines,
			&reply.ReplyCount, &reply.Downloaded,
		)
		if err == nil {
			replies = append(replies, reply)
		}
	}

	return replies, totalReplies, nil
}

// GetOverviewByArticleNum gets a single overview from articles table by article number
func (db *Database) GetOverviewByArticleNum(groupDBs *GroupDBs, articleNum int64) (*models.Overview, error) {
	query := `
		SELECT article_num, subject, from_header, date_sent, date_string,
			   message_id, "references", bytes, lines, reply_count, downloaded, spam, hide
		FROM articles
		WHERE article_num = ? LIMIT 1
	`

	overview := &models.Overview{}
	err := retryableQueryRowScan(groupDBs.DB, query, []interface{}{articleNum},
		&overview.ArticleNum, &overview.Subject, &overview.FromHeader,
		&overview.DateSent, &overview.DateString, &overview.MessageID,
		&overview.References, &overview.Bytes, &overview.Lines,
		&overview.ReplyCount, &overview.Downloaded, &overview.Spam, &overview.Hide,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get overview for article %d: %w", articleNum, err)
	}

	return overview, nil
}

// GetCachedThreadsFromMemory retrieves threads using the two-level memory cache
func (mem *MemCachedThreads) GetCachedThreadsFromMemory(db *Database, groupDBs *GroupDBs, group string, page int64, pageSize int64) ([]*models.ForumThread, int64, bool) {
	startTime := time.Now()
	mem.mux.RLock()
	defer mem.mux.RUnlock()

	// Check if we have cached data for this group
	groupCache, exists := mem.Groups[group]
	if !exists {
		log.Printf("[PERF:MEMORY] Cache miss (no group) for group '%s' took %v", group, time.Since(startTime))
		return nil, 0, false // Cache miss
	}
	if len(groupCache.ThreadRoots) == 0 {
		// Treat empty cache for a valid initialized group as a hit with empty page
		log.Printf("[MEM:HIT] Empty thread list for group '%s' (cold cache)", group)
		return []*models.ForumThread{}, groupCache.CountThreads, true
	}

	// Check if the cache is fresh (within 5 minute)
	if time.Since(groupCache.ThreadRootsTS) > 5*time.Minute { // TODO hardcoded cache expiry
		go mem.InvalidateThreadRoot(group, groupCache.ThreadRoots[0]) // Invalidate oldest root if cache expired
		log.Printf("[PERF:MEMORY] Cache miss (expired) for group '%s' took %v", group, time.Since(startTime))
		return nil, 0, false // Cache expired
	}

	// Calculate pagination using REAL database total, not cached count
	totalCount := groupCache.CountThreads // This is the real database total from RefreshThreadCache
	requestedOffset := (page - 1) * pageSize
	if requestedOffset >= totalCount {
		log.Printf("[PERF:MEMORY] Empty page for group '%s' took %v", group, time.Since(startTime))
		return []*models.ForumThread{}, totalCount, true // Empty page but cache hit
	}

	// Calculate the position within our cached window
	cacheOffset := requestedOffset - groupCache.CacheWindowOffset

	// For memory cache windowing, use cached thread count
	cachedCount := int64(len(groupCache.ThreadRoots))
	end := cacheOffset + pageSize
	if end > cachedCount {
		end = cachedCount
	}

	// Only serve pages that are within our cached window
	if cacheOffset < 0 || cacheOffset >= cachedCount {
		log.Printf("[PERF:MEMORY] Page beyond cache window for group '%s' (offset %d, cached %d) took %v", group, cacheOffset, cachedCount, time.Since(startTime))
		return nil, 0, false // Beyond cached window - need refresh
	}

	pageRoots := groupCache.ThreadRoots[cacheOffset:end]
	var forumThreads []*models.ForumThread

	// Get metadata for each thread root on this page
	overviewStartTime := time.Now()
	for _, rootID := range pageRoots {
		// Check if we have cached metadata for this thread
		meta, hasMeta := groupCache.ThreadMeta[rootID]
		if !hasMeta {
			// Partial cache miss - we'll need to refresh
			log.Printf("[PERF:MEMORY] Partial cache miss (no metadata) for group '%s' took %v", group, time.Since(startTime))
			return nil, 0, false
		}

		// Get the root overview (this should be fast from articles table)
		rootOverview, err := db.GetOverviewByArticleNum(groupDBs, rootID) // TODO maybe add caching here too
		if err != nil {
			log.Printf("failed to get root overview for thread %d: %v", rootID, err)
			continue
		}

		forumThread := &models.ForumThread{
			RootArticle:  rootOverview,
			Replies:      nil,                   // Will be loaded separately
			MessageCount: meta.MessageCount - 1, // Convert to reply count (total - root)
			LastActivity: meta.LastActivity,
		}

		forumThreads = append(forumThreads, forumThread)
	}
	log.Printf("[PERF:MEMORY] Loading %d overviews took %v for group '%s'", len(pageRoots), time.Since(overviewStartTime), group)

	log.Printf("[MEM:HIT] Served %d threads from memory cache for group '%s'", len(forumThreads), group)
	log.Printf("[PERF:MEMORY] Total GetCachedThreadsFromMemory took %v for group '%s'", time.Since(startTime), group)
	return forumThreads, totalCount, true
}

// RefreshThreadCache loads thread data from database and updates memory cache
// Uses hybrid cursor+page pagination like articles for ultra-fast performance
func (mem *MemCachedThreads) RefreshThreadCache(db *Database, groupDBs *GroupDBs, group string, requestedPage int64, pageSize int64) error {
	startTime := time.Now()
	log.Printf("[PERF:REFRESH] Starting RefreshThreadCache for group '%s', page %d", group, requestedPage)

	mem.mux.Lock()
	defer mem.mux.Unlock()

	// Calculate cache window centered around the requested page
	cacheSize := pageSize * 6 // Cache about 6 pages worth of threads

	// For random page access, we need to calculate the offset for the cache window
	// Center the cache window around the requested page
	requestedOffset := (requestedPage - 1) * pageSize
	cacheWindowStart := requestedOffset - (cacheSize / 2)
	if cacheWindowStart < 0 {
		cacheWindowStart = 0
	}

	log.Printf("[PERF:REFRESH] Caching window starting at offset %d for page %d", cacheWindowStart, requestedPage)

	// Use OFFSET/LIMIT for cache loading (not cursor-based for random access)
	queryStartTime := time.Now()
	query := `
		SELECT thread_root, root_date, message_count, child_articles, last_child_number, last_activity
		FROM thread_cache
		ORDER BY last_activity DESC
		LIMIT ? OFFSET ?
	`
	args := []interface{}{cacheSize, cacheWindowStart}

	rows, err := retryableQuery(groupDBs.DB, query, args...)
	if err != nil {
		return fmt.Errorf("failed to query thread cache: %w", err)
	}
	defer rows.Close()
	log.Printf("[PERF:REFRESH] Database query took %v for group '%s'", time.Since(queryStartTime), group)

	scanStartTime := time.Now()
	var threadRoots []int64
	threadMeta := make(map[int64]*ThreadCacheEntry)

	for rows.Next() {
		var entry ThreadCacheEntry
		err := rows.Scan(
			&entry.ThreadRoot,
			&entry.RootDate,
			&entry.MessageCount,
			&entry.ChildArticles,
			&entry.LastChildNumber,
			&entry.LastActivity,
		)
		if err != nil {
			log.Printf("failed to scan thread cache entry: %v", err)
			continue
		}

		// Quick check if thread root article is hidden (fast single lookup)
		var hidden int
		checkQuery := `SELECT hide FROM articles WHERE article_num = ? LIMIT 1`
		err = retryableQueryRowScan(groupDBs.DB, checkQuery, []interface{}{entry.ThreadRoot}, &hidden)
		if err != nil || hidden != 0 {
			continue // Skip hidden threads
		}

		threadRoots = append(threadRoots, entry.ThreadRoot)
		threadMeta[entry.ThreadRoot] = &entry
	}
	log.Printf("[PERF:REFRESH] Scanning %d rows took %v for group '%s'", len(threadRoots), time.Since(scanStartTime), group)

	// Initialize or get the group cache
	if mem.Groups[group] == nil {
		mem.Groups[group] = &MemGroupThreadCache{
			ThreadMeta:        make(map[int64]*ThreadCacheEntry),
			ThreadMetaTS:      make(map[int64]time.Time),
			CacheWindowOffset: 0,
		}
	}

	updateStartTime := time.Now()
	groupCache := mem.Groups[group]

	// Get the REAL total count from database (not just cached count)
	var realTotalCount int64
	countQuery := `SELECT COUNT(*) FROM thread_cache`
	err = retryableQueryRowScan(groupDBs.DB, countQuery, []interface{}{}, &realTotalCount)
	if err != nil {
		log.Printf("[PERF:REFRESH] Failed to get real total count: %v", err)
		realTotalCount = int64(len(threadRoots)) // Fallback to cached count
	}

	// Update cache
	groupCache.ThreadRoots = threadRoots
	groupCache.ThreadRootsTS = time.Now()
	groupCache.ThreadMeta = threadMeta
	groupCache.CountThreads = realTotalCount // Use REAL total, not cached count
	groupCache.Expiry = time.Now().Add(MemCacheThreadsExpiry)

	// Store the cache window offset so we can calculate relative positions correctly
	groupCache.CacheWindowOffset = cacheWindowStart

	// Update timestamps for all thread metadata
	now := time.Now()
	for rootID := range threadMeta {
		groupCache.ThreadMetaTS[rootID] = now
	}
	log.Printf("[PERF:REFRESH] Cache update took %v for group '%s'", time.Since(updateStartTime), group)

	log.Printf("[MEM:REFRESH] Cached %d threads for group '%s' (window around page %d)", len(threadRoots), group, requestedPage)
	log.Printf("[PERF:REFRESH] Total RefreshThreadCache took %v for group '%s'", time.Since(startTime), group)
	return nil
}

// InvalidateThreadRoot removes a specific thread from cache (when thread deleted)
func (mem *MemCachedThreads) InvalidateThreadRoot(group string, threadRoot int64) {
	mem.mux.Lock()
	defer mem.mux.Unlock()

	groupCache, exists := mem.Groups[group]
	if !exists {
		return
	}

	// Remove from thread roots list
	newRoots := make([]int64, 0, len(groupCache.ThreadRoots))
	for _, root := range groupCache.ThreadRoots {
		if root != threadRoot {
			newRoots = append(newRoots, root)
		}
	}
	groupCache.ThreadRoots = newRoots
	groupCache.ThreadRootsTS = time.Now()

	// Remove from metadata
	delete(groupCache.ThreadMeta, threadRoot)
	delete(groupCache.ThreadMetaTS, threadRoot)

	// NOTE: Don't update CountThreads here - it should remain the real database total
	// groupCache.CountThreads represents the TOTAL threads in database, not just cached ones

	log.Printf("[MEM:INVALIDATE] Removed thread %d from cache for group '%s'", threadRoot, group)
}

// UpdateThreadMetadata updates metadata for a specific thread (when new reply added)
func (mem *MemCachedThreads) UpdateThreadMetadata(group string, threadRoot int64, messageCount int, lastActivity time.Time, childArticles string) {
	mem.mux.Lock()
	defer mem.mux.Unlock()

	// Initialize group cache if it doesn't exist
	if mem.Groups[group] == nil {
		mem.Groups[group] = &MemGroupThreadCache{
			ThreadMeta:        make(map[int64]*ThreadCacheEntry),
			ThreadMetaTS:      make(map[int64]time.Time),
			CountThreads:      0, // Will be set correctly when RefreshThreadCache is called
			CacheWindowOffset: 0,
		}
	}

	groupCache := mem.Groups[group]

	// Update or create metadata
	if meta, exists := groupCache.ThreadMeta[threadRoot]; exists {
		meta.MessageCount = messageCount
		meta.LastActivity = lastActivity
		meta.ChildArticles = childArticles
	} else {
		groupCache.ThreadMeta[threadRoot] = &ThreadCacheEntry{
			ThreadRoot:    threadRoot,
			MessageCount:  messageCount,
			LastActivity:  lastActivity,
			ChildArticles: childArticles,
		}
	}

	groupCache.ThreadMetaTS[threadRoot] = time.Now()

	// Reorder thread roots by last activity (move updated thread to front)
	newRoots := make([]int64, 0, len(groupCache.ThreadRoots))
	found := false
	for _, root := range groupCache.ThreadRoots {
		if root == threadRoot {
			found = true
		} else {
			newRoots = append(newRoots, root)
		}
	}

	// Add threadRoot at the beginning (most recent activity)
	if found {
		groupCache.ThreadRoots = append([]int64{threadRoot}, newRoots...)
	} else {
		// New thread - add at beginning but don't update count here
		// CountThreads represents TOTAL database threads, not just cached ones
		groupCache.ThreadRoots = append([]int64{threadRoot}, newRoots...)
	}

	groupCache.ThreadRootsTS = time.Now()

	//log.Printf("[MEM:UPDATE] Updated thread %d metadata for group '%s' (count: %d)", threadRoot, group, messageCount)
}

// InvalidateGroup clears all cache for a group
func (mem *MemCachedThreads) InvalidateGroup(group string) {
	mem.mux.Lock()
	defer mem.mux.Unlock()

	delete(mem.Groups, group)

	log.Printf("[MEM:INVALIDATE] Cleared all cache for group '%s'", group)
}
