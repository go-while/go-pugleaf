package database

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// HierarchyCache provides fast access to hierarchy data
type HierarchyCache struct {
	mu sync.RWMutex

	// Main hierarchy data
	hierarchies    []*models.Hierarchy
	hierarchiesMap map[string]*models.Hierarchy
	totalCount     int

	// Sub-level cache: prefix -> {sublevel -> count}
	subLevelsCache map[string]map[string]int
	subLevelCounts map[string]int // prefix -> total count

	// Direct groups cache: prefix -> groups
	directGroupsCache map[string][]*models.Newsgroup
	directGroupCounts map[string]int

	// Cache warming status
	isWarming    bool
	warmingError error
	warmComplete chan struct{}

	// Invalidation (A-db-8): InvalidateHierarchy marks a top level dirty instead of
	// deleting its entries, and the readers rebuild a dirty top level from the database
	// before they read it. invalSeq counts the marks, markSeq holds the seq of the last
	// mark per top level, loadSeq the seq at which a top level was last rebuilt and
	// bulkLoadSeq the seq at which the last full tree build started. A top level is dirty
	// while its last mark is newer than both loads.
	lastInvalidation time.Time
	invalSeq         uint64
	markSeq          map[string]uint64
	loadSeq          map[string]uint64
	bulkLoadSeq      uint64

	// rebuildMus holds one mutex per top level (under mu): the rebuilds of one dirty top
	// level are serialized, so concurrent readers of it run one query, while the rebuild
	// of another top level - and its RetryableQuery, which may wait out a busy main
	// database - does not wait for it.
	rebuildMus map[string]*sync.Mutex

	// rebuilding holds the top levels whose rebuild query is running, bulkBuilds the
	// number of running full builds. A batch whose stats arrive meanwhile may be missing
	// from the rows read, and its increment lands on a struct the build replaces:
	// UpdateNewsgroupStats then marks the top level again, so the next reader rebuilds.
	rebuilding map[string]bool
	bulkBuilds int

	// testRebuildHook, when set (tests only, per cache), runs before the query of a
	// rebuild (top level) or a full build ("*"), after the build was registered.
	testRebuildHook func(top string)
}

// hierarchySortOrders are the sort orders kept in directGroupsCache ("prefix:sort" keys).
var hierarchySortOrders = []string{"name", "activity", "groups"}

// NewHierarchyCache creates a new hierarchy cache
func NewHierarchyCache() *HierarchyCache {
	return &HierarchyCache{
		hierarchiesMap:    make(map[string]*models.Hierarchy),
		subLevelsCache:    make(map[string]map[string]int),
		subLevelCounts:    make(map[string]int),
		directGroupsCache: make(map[string][]*models.Newsgroup),
		directGroupCounts: make(map[string]int),
		warmComplete:      make(chan struct{}),
		markSeq:           make(map[string]uint64),
		rebuilding:        make(map[string]bool),
		loadSeq:           make(map[string]uint64),
	}
}

// WarmCache pre-loads all hierarchy data in background with bulk operations
func (hc *HierarchyCache) WarmCache(db *Database) {
	hc.mu.Lock()
	if hc.isWarming {
		hc.mu.Unlock()
		return
	}
	hc.isWarming = true
	hc.mu.Unlock()

	go func() {
		defer func() {
			hc.mu.Lock()
			hc.isWarming = false
			close(hc.warmComplete)
			hc.warmComplete = make(chan struct{})
			hc.mu.Unlock()
		}()

		log.Printf("HierarchyCache: Starting bulk cache warming...")
		start := time.Now()

		// 1. Bulk load all hierarchies at once
		if err := hc.loadAllHierarchies(db); err != nil {
			log.Printf("HierarchyCache: Failed to load hierarchies: %v", err)
			hc.warmingError = err
			return
		}

		// 2. Bulk build hierarchy tree structure from all newsgroups
		if err := hc.buildHierarchyTreeBulk(db); err != nil {
			log.Printf("HierarchyCache: Failed to build hierarchy tree: %v", err)
			hc.warmingError = err
			return
		}

		log.Printf("HierarchyCache: Bulk cache warming completed in %v", time.Since(start))
	}()
}

// GetHierarchiesPaginated returns cached hierarchy data with pagination
func (hc *HierarchyCache) GetHierarchiesPaginated(db *Database, page, pageSize int, sortBy string) ([]*models.Hierarchy, int, error) {
	hc.mu.RLock()
	if len(hc.hierarchies) == 0 {
		hc.mu.RUnlock()
		log.Printf("HierarchyCache: Cache is empty - should be warmed on startup")
		return []*models.Hierarchy{}, 0, nil
	}

	// Deep copy hierarchies for thread-safe processing
	hierarchies := make([]*models.Hierarchy, len(hc.hierarchies))
	for i, h := range hc.hierarchies {
		// Create a deep copy to avoid data races during sorting
		hierarchies[i] = &models.Hierarchy{
			ID:          h.ID,
			Name:        h.Name,
			Description: h.Description, // Fix: Include description field
			GroupCount:  h.GroupCount,
			LastUpdated: h.LastUpdated, // Safe read under RLock
			CreatedAt:   h.CreatedAt,   // Fix: Include created_at field
		}
	}
	hc.mu.RUnlock()

	// Sort hierarchies based on sortBy parameter

	switch sortBy {
	case "name":
		sort.Slice(hierarchies, func(i, j int) bool {
			return hierarchies[i].Name < hierarchies[j].Name
		})
	case "groups":
		sort.Slice(hierarchies, func(i, j int) bool {
			return hierarchies[i].GroupCount > hierarchies[j].GroupCount
		})
	case "activity":
		sort.Slice(hierarchies, func(i, j int) bool {
			return hierarchies[i].LastUpdated.After(hierarchies[j].LastUpdated)
		})
	}

	// Apply pagination
	start, end := pageWindow(len(hierarchies), page, pageSize)
	return hierarchies[start:end], len(hierarchies), nil
}

// pageWindow returns the slice bounds of page (1-based) of n items with pageSize items
// per page (A-db-10). page < 1 is page 1; a page past the end, pageSize <= 0 or n == 0
// give an empty window. Nothing is multiplied before the page is bounded, so a huge page
// can neither wrap into a negative index nor panic.
func pageWindow(n, page, pageSize int) (start, end int) {
	if n <= 0 || pageSize <= 0 {
		return 0, 0
	}
	if page < 1 {
		page = 1
	}
	pages := n / pageSize
	if n%pageSize != 0 {
		pages++
	}
	if page > pages {
		return n, n
	}
	start = (page - 1) * pageSize // < n, since page <= pages
	if pageSize >= n-start {
		return start, n
	}
	return start, start + pageSize
}

// GetHierarchySubLevels returns cached sub-level data.
// A top level marked dirty by InvalidateHierarchy is rebuilt from db first (A-db-8).
func (hc *HierarchyCache) GetHierarchySubLevels(db *Database, prefix string, page, pageSize int) (map[string]int, int, error) {
	hc.refreshIfDirty(db, prefix)

	// Convert to a slice for sorting and pagination
	type subLevel struct {
		name  string
		count int
	}

	hc.mu.RLock()
	subLevels, exists := hc.subLevelsCache[prefix]
	totalCount := hc.subLevelCounts[prefix]
	sorted := make([]subLevel, 0, len(subLevels))
	for name, count := range subLevels {
		sorted = append(sorted, subLevel{name, count})
	}
	hc.mu.RUnlock()

	// If not in cache, return empty (cache should be warmed on startup)
	if !exists {
		log.Printf("HierarchyCache: No sub-levels found for prefix: %s", prefix)
		return make(map[string]int), 0, nil
	}

	// Sort alphabetically
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].name < sorted[j].name
	})

	// Apply pagination
	start, end := pageWindow(len(sorted), page, pageSize)
	result := make(map[string]int, end-start)
	for _, item := range sorted[start:end] {
		result[item.name] = item.count
	}

	return result, totalCount, nil
}

// GetDirectGroupsAtLevel returns cached direct groups data. The page holds copies made
// under the read lock: UpdateNewsgroupStats and UpdateNewsgroupActiveStatus change the
// cached structs and re-sort the cached slices in place (A-db-14). A top level marked
// dirty by InvalidateHierarchy is rebuilt from db first (A-db-8).
func (hc *HierarchyCache) GetDirectGroupsAtLevel(db *Database, prefix, sortBy string, page, pageSize int) ([]*models.Newsgroup, int, error) {
	hc.refreshIfDirty(db, prefix)

	cacheKey := prefix + ":" + sortBy

	hc.mu.RLock()
	groups, exists := hc.directGroupsCache[cacheKey]
	totalCount := hc.directGroupCounts[prefix]
	var out []*models.Newsgroup
	if exists {
		// Apply pagination
		start, end := pageWindow(len(groups), page, pageSize)
		out = make([]*models.Newsgroup, 0, end-start)
		for _, g := range groups[start:end] {
			cp := *g
			out = append(out, &cp)
		}
	}
	hc.mu.RUnlock()

	// If not in cache, return empty (cache should be warmed on startup)
	if !exists {
		log.Printf("HierarchyCache: No direct groups found for prefix: %s", prefix)
		return []*models.Newsgroup{}, 0, nil
	}

	return out, totalCount, nil
}

// directParent returns the prefix a newsgroup is stored under in directGroupsCache: its
// name without the last part (see addGroupToAllPossibleLevels). A name without a dot is
// not stored at any level.
func directParent(name string) (string, bool) {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return "", false
	}
	return name[:i], true
}

// sortDirectGroups sorts a direct groups list for one sort order: "name" A-Z, "activity"
// newest UpdatedAt first (like getDirectGroupsAtLevelDirect's updated_at DESC), "groups"
// most messages first. Ties are ordered by name so the pages stay stable.
func sortDirectGroups(groups []*models.Newsgroup, sortBy string) {
	switch sortBy {
	case "name":
		sort.Slice(groups, func(i, j int) bool {
			return groups[i].Name < groups[j].Name
		})
	case "activity":
		sort.Slice(groups, func(i, j int) bool {
			if !groups[i].UpdatedAt.Equal(groups[j].UpdatedAt) {
				return groups[i].UpdatedAt.After(groups[j].UpdatedAt)
			}
			return groups[i].Name < groups[j].Name
		})
	case "groups":
		sort.Slice(groups, func(i, j int) bool {
			if groups[i].MessageCount != groups[j].MessageCount {
				return groups[i].MessageCount > groups[j].MessageCount
			}
			return groups[i].Name < groups[j].Name
		})
	}
}

// UpdateNewsgroupActiveStatus updates the active status of a newsgroup in cache
func (hc *HierarchyCache) UpdateNewsgroupActiveStatus(newsgroupName string, active bool) {
	// The group is cached under its direct parent, not under its top level.
	parent, ok := directParent(newsgroupName)
	if !ok {
		return
	}

	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Update direct groups cache for all sort orders
	for _, sortBy := range hierarchySortOrders {
		cacheKey := parent + ":" + sortBy
		if groups, exists := hc.directGroupsCache[cacheKey]; exists {
			// Find and update the specific newsgroup in the cached list
			for _, group := range groups {
				if group.Name == newsgroupName {
					group.Active = active
					break
				}
			}
		}
	}

	log.Printf("HierarchyCache: Updated active status for newsgroup '%s': active=%t", newsgroupName, active)
}

// UpdateNewsgroupStats applies one stored batch to the cached stats of a newsgroup: it adds
// the newly inserted articles to the group's message count, raises its last article number
// and moves the group's UpdatedAt and its top level's LastUpdated forward to latest, the
// batch's newest article date (zero leaves both dates alone). A hierarchy's GroupCount
// counts groups, not articles, and an article number is never a date (A-db-7).
//
// A group that is not cached (the tree lists only groups with message_count > 0, so a group
// gets here without an entry when this batch brought its first articles) cannot be updated in
// place: with messageCountIncrement > 0 its top level is marked dirty, so the next reader
// rebuilds it and lists the group.
func (hc *HierarchyCache) UpdateNewsgroupStats(newsgroupName string, messageCountIncrement int, newLastArticle int64, latest time.Time) {
	hierarchy := ExtractHierarchyFromGroupName(newsgroupName)
	parent, hasParent := directParent(newsgroupName)
	// Cached dates are parsed as UTC from the DB: compare and store in one location.
	latest = latest.UTC()

	hc.mu.Lock()
	defer hc.mu.Unlock()

	markDirty := func() {
		if hc.markSeq == nil {
			hc.markSeq = make(map[string]uint64)
		}
		hc.invalSeq++
		hc.markSeq[hierarchy] = hc.invalSeq
	}

	// A rebuild of this top level or a full build is reading rows right now: this batch
	// may be missing from them, and the structs updated below are about to be replaced.
	// Mark the top level again, so the next reader rebuilds it.
	if hc.rebuilding[hierarchy] || hc.bulkBuilds > 0 {
		markDirty()
	}

	// Update hierarchy data if cached
	if h, exists := hc.hierarchiesMap[hierarchy]; exists && !latest.IsZero() && latest.After(h.LastUpdated) {
		h.LastUpdated = latest
	}

	// Update the direct groups cache of the group's direct parent for all sort orders.
	// The lists of one prefix share their *Newsgroup structs: update each struct once.
	if hasParent {
		var updated *models.Newsgroup
		for _, sortBy := range hierarchySortOrders {
			groups, exists := hc.directGroupsCache[parent+":"+sortBy]
			if !exists {
				continue
			}
			for _, group := range groups {
				if group.Name != newsgroupName {
					continue
				}
				if group != updated {
					group.MessageCount += int64(messageCountIncrement)
					if newLastArticle > group.LastArticle {
						group.LastArticle = newLastArticle
					}
					if !latest.IsZero() && latest.After(group.UpdatedAt) {
						group.UpdatedAt = latest
					}
					updated = group
				}
				break
			}

			// Re-sort if necessary (only for activity and groups sort orders). The readers
			// copy under the read lock, so sorting in place is safe (A-db-14).
			if sortBy != "name" {
				sortDirectGroups(groups, sortBy)
			}
		}
		if updated == nil && messageCountIncrement > 0 {
			// not cached: its first articles, or a list that was never built
			markDirty()
		}
	}

	log.Printf("HierarchyCache: Updated stats for newsgroup '%s': +%d messages, last_article=%d",
		newsgroupName, messageCountIncrement, newLastArticle)
}

// InvalidateHierarchy marks the top level of hierarchyName dirty after a newsgroup in it
// was inserted, changed or deleted. The cached entries stay readable, and the next reader
// of that top level (GetHierarchySubLevels, GetDirectGroupsAtLevel) first rebuilds the
// top level and all its sub-levels from the database (A-db-8). There is no throttle:
// every call marks.
func (hc *HierarchyCache) InvalidateHierarchy(hierarchyName string) {
	top := ExtractHierarchyFromGroupName(hierarchyName)

	hc.mu.Lock()
	if hc.markSeq == nil {
		hc.markSeq = make(map[string]uint64)
	}
	hc.invalSeq++
	hc.markSeq[top] = hc.invalSeq
	hc.lastInvalidation = time.Now()
	hc.mu.Unlock()

	log.Printf("HierarchyCache: Marked hierarchy '%s' for rebuild on next read", top)
}

// InvalidateAll clears all cached data
func (hc *HierarchyCache) InvalidateAll() {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.hierarchies = nil
	hc.hierarchiesMap = make(map[string]*models.Hierarchy)
	hc.totalCount = 0

	hc.subLevelsCache = make(map[string]map[string]int)
	hc.subLevelCounts = make(map[string]int)

	hc.directGroupsCache = make(map[string][]*models.Newsgroup)
	hc.directGroupCounts = make(map[string]int)

	// Nothing is loaded any more: every top level marked so far is dirty again.
	hc.loadSeq = make(map[string]uint64)
	hc.bulkLoadSeq = 0
	hc.lastInvalidation = time.Now()

	log.Printf("HierarchyCache: All cache data invalidated")
}

// ForceInvalidateHierarchy is InvalidateHierarchy; there is no throttle to bypass any more.
func (hc *HierarchyCache) ForceInvalidateHierarchy(hierarchyName string) {
	hc.InvalidateHierarchy(hierarchyName)
}

// GetInvalidationStatus returns the time of the last invalidation and the time since then.
// The last value (time until the next invalidation is allowed) is always 0: invalidations
// are not throttled (A-db-8).
func (hc *HierarchyCache) GetInvalidationStatus() (time.Time, time.Duration, time.Duration) {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	return hc.lastInvalidation, time.Since(hc.lastInvalidation), 0
}

// WaitForWarmup waits for cache warming to complete
func (hc *HierarchyCache) WaitForWarmup(timeout time.Duration) error {
	hc.mu.RLock()
	if !hc.isWarming {
		hc.mu.RUnlock()
		return nil
	}
	warmComplete := hc.warmComplete
	hc.mu.RUnlock()

	select {
	case <-warmComplete:
		return hc.warmingError
	case <-time.After(timeout):
		return fmt.Errorf("cache warming timeout after %v", timeout)
	}
}

// Private helper methods

func (hc *HierarchyCache) loadAllHierarchies(db *Database) error {
	hierarchies, totalCount, err := db.getHierarchiesPaginatedDirect(1, 10000, "activity") // Load all
	if err != nil {
		return err
	}

	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.hierarchies = hierarchies
	hc.hierarchiesMap = make(map[string]*models.Hierarchy)
	for _, h := range hierarchies {
		hc.hierarchiesMap[h.Name] = h
	}
	hc.totalCount = totalCount

	log.Printf("HierarchyCache: Loaded %d hierarchies", len(hierarchies))
	return nil
}

// hierarchyTree is the cache content built from a list of newsgroups.
type hierarchyTree struct {
	subLevels    map[string]map[string]int      // prefix -> {sublevel -> count}
	directGroups map[string][]*models.Newsgroup // "prefix:sort" -> sorted groups
	directCounts map[string]int                 // prefix -> number of direct groups
}

// query_hierarchyTopLevelGroups selects the groups of one top level for a rebuild
// (A-db-8): the same columns and filter as buildHierarchyTreeBulk, restricted to the
// names in [top+".", top+"/") ('/' follows '.'), so no LIKE wildcard ('_') applies.
const query_hierarchyTopLevelGroups = `
		SELECT name, COALESCE(hierarchy, '') AS hierarchy, COALESCE(message_count, 0), last_article, updated_at
		FROM newsgroups
		WHERE active = 1 AND message_count > 0 AND name >= ? AND name < ?
		ORDER BY hierarchy, name
	`

// buildHierarchyTreeBulk builds the complete hierarchy tree structure with one query
func (hc *HierarchyCache) buildHierarchyTreeBulk(db *Database) error {
	log.Printf("HierarchyCache: Building hierarchy tree from all newsgroups...")

	// Marks after this point are not covered by the tree built here. Stats updates
	// while the build runs mark their top level (see UpdateNewsgroupStats).
	hc.mu.Lock()
	startSeq := hc.invalSeq
	hc.bulkBuilds++
	hook := hc.testRebuildHook
	hc.mu.Unlock()
	defer func() {
		hc.mu.Lock()
		hc.bulkBuilds--
		hc.mu.Unlock()
	}()
	if hook != nil {
		hook("*")
	}

	// Get ALL active newsgroups with their hierarchies in one query.
	// hierarchy and message_count are nullable (A-db-1): COALESCE them, so a row the old
	// batch upsert left with NULLs is still listed instead of failing the scan (A-db-11).
	rows, err := RetryableQuery(db.mainDB, `
		SELECT name, COALESCE(hierarchy, '') AS hierarchy, COALESCE(message_count, 0), last_article, updated_at
		FROM newsgroups
		WHERE active = 1 AND message_count > 0
		ORDER BY hierarchy, name
	`)
	if err != nil {
		return fmt.Errorf("failed to query all newsgroups: %w", err)
	}
	defer rows.Close()

	allGroups, err := hc.scanHierarchyTreeRows(rows, "buildHierarchyTreeBulk")
	if err != nil {
		return fmt.Errorf("failed to read all newsgroups: %w", err)
	}
	tree := hc.buildTree(allGroups)

	// Now populate all the caches
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Clear existing cache
	hc.subLevelsCache = make(map[string]map[string]int)
	hc.subLevelCounts = make(map[string]int)
	hc.directGroupsCache = make(map[string][]*models.Newsgroup)
	hc.directGroupCounts = make(map[string]int)
	hc.installTreeLocked(tree)

	// The whole tree is as of startSeq now: drop the per-top-level loads (a top level
	// rebuilt after startSeq was just replaced by older rows and is dirty again if it
	// was marked after startSeq).
	hc.bulkLoadSeq = startSeq
	hc.loadSeq = make(map[string]uint64)

	log.Printf("HierarchyCache: Built complete hierarchy tree with %d groups, %d hierarchies, %d sub-levels",
		len(allGroups), len(tree.directCounts), len(tree.subLevels))
	return nil
}

// refreshIfDirty rebuilds the top level of prefix when InvalidateHierarchy marked it dirty
// (A-db-8). Without a database the cached entries are served as they are. A failed rebuild
// is logged and the old entries are served; the top level stays dirty, so the next reader
// tries again.
func (hc *HierarchyCache) refreshIfDirty(db *Database, prefix string) {
	if db == nil || db.mainDB == nil {
		return
	}
	top := ExtractHierarchyFromGroupName(prefix)

	hc.mu.RLock()
	dirty := hc.isDirtyLocked(top)
	hc.mu.RUnlock()
	if !dirty {
		return
	}
	if err := hc.rebuildTopLevel(db, top); err != nil {
		log.Printf("[HIERARCHY] rebuild of top level '%s' failed, serving cached entries: %v", top, err)
	}
}

// isDirtyLocked reports whether top was marked after it was last loaded. hc.mu must be held.
func (hc *HierarchyCache) isDirtyLocked(top string) bool {
	mark, ok := hc.markSeq[top]
	if !ok {
		return false
	}
	return mark > hc.bulkLoadSeq && mark > hc.loadSeq[top]
}

// topRebuildMu returns the rebuild mutex of top, creating it. The map grows by one entry per
// top level ever rebuilt (a few hundred at most).
func (hc *HierarchyCache) topRebuildMu(top string) *sync.Mutex {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.rebuildMus == nil {
		hc.rebuildMus = make(map[string]*sync.Mutex)
	}
	m, ok := hc.rebuildMus[top]
	if !ok {
		m = &sync.Mutex{}
		hc.rebuildMus[top] = m
	}
	return m
}

// rebuildTopLevel reloads the groups of one top level with one range query and replaces
// every cache entry of top and top.* (A-db-8). It never falls back to
// getDirectGroupsAtLevelDirect: that one returns nothing above one page and does not
// filter on message_count, so its result cannot be cached. Only the rebuilds of the same
// top level wait for each other (topRebuildMu).
func (hc *HierarchyCache) rebuildTopLevel(db *Database, top string) error {
	topMu := hc.topRebuildMu(top)
	topMu.Lock()
	defer topMu.Unlock()

	hc.mu.Lock()
	dirty := hc.isDirtyLocked(top)
	startSeq := hc.invalSeq
	if dirty {
		// Stats updates while the query runs mark top again (see UpdateNewsgroupStats).
		if hc.rebuilding == nil {
			hc.rebuilding = make(map[string]bool)
		}
		hc.rebuilding[top] = true
	}
	hook := hc.testRebuildHook
	hc.mu.Unlock()
	if !dirty {
		return nil // another reader rebuilt it while this one waited
	}
	// Runs after the deferred Unlock of the install below, so the flag covers the
	// whole window between the query and the install.
	defer func() {
		hc.mu.Lock()
		delete(hc.rebuilding, top)
		hc.mu.Unlock()
	}()
	if hook != nil {
		hook(top)
	}

	rows, err := RetryableQuery(db.mainDB, query_hierarchyTopLevelGroups, top+".", top+"/")
	if err != nil {
		return fmt.Errorf("failed to query newsgroups of %s: %w", top, err)
	}
	defer rows.Close()

	groups, err := hc.scanHierarchyTreeRows(rows, "rebuildTopLevel")
	if err != nil {
		return fmt.Errorf("failed to read newsgroups of %s: %w", top, err)
	}
	tree := hc.buildTree(groups)

	hc.mu.Lock()
	defer hc.mu.Unlock()

	if hc.bulkLoadSeq >= startSeq {
		// A full build that started no earlier than this rebuild replaced the tree.
		return nil
	}

	inTop := func(prefix string) bool {
		return prefix == top || strings.HasPrefix(prefix, top+".")
	}
	for prefix := range hc.subLevelsCache {
		if inTop(prefix) {
			delete(hc.subLevelsCache, prefix)
		}
	}
	for prefix := range hc.subLevelCounts {
		if inTop(prefix) {
			delete(hc.subLevelCounts, prefix)
		}
	}
	for key := range hc.directGroupsCache {
		if i := strings.LastIndex(key, ":"); i >= 0 && inTop(key[:i]) {
			delete(hc.directGroupsCache, key)
		}
	}
	for prefix := range hc.directGroupCounts {
		if inTop(prefix) {
			delete(hc.directGroupCounts, prefix)
		}
	}
	hc.installTreeLocked(tree)

	if hc.loadSeq == nil {
		hc.loadSeq = make(map[string]uint64)
	}
	hc.loadSeq[top] = startSeq

	log.Printf("HierarchyCache: Rebuilt top level '%s' with %d groups", top, len(groups))
	return nil
}

// scanHierarchyTreeRows reads the rows of a tree query (name, hierarchy, message_count,
// last_article, updated_at). A row that fails to scan is skipped and logged once per call
// (A-db-11); a read error fails the call, so a truncated read never replaces cache entries.
func (hc *HierarchyCache) scanHierarchyTreeRows(rows *sql.Rows, who string) ([]*models.Newsgroup, error) {
	var allGroups []*models.Newsgroup
	scanErrs := 0

	// Process all newsgroups
	for rows.Next() {
		group := &models.Newsgroup{}
		var lastArticle sql.NullInt64
		var updatedAt sql.NullString

		err := rows.Scan(&group.Name, &group.Hierarchy, &group.MessageCount, &lastArticle, &updatedAt)
		if err != nil {
			// Log the first scan error of this call only; the rest are counted below.
			scanErrs++
			if scanErrs == 1 {
				log.Printf("[HIERARCHY] %s: failed to scan newsgroup row: %v", who, err)
			}
			continue
		}

		if lastArticle.Valid {
			group.LastArticle = lastArticle.Int64
		}

		// Set Active to true since we're only querying active groups
		group.Active = true

		// Handle updated_at field for last activity display
		if updatedAt.Valid {
			// Parse the updated_at timestamp - try RFC3339 format first (with Z timezone)
			if parsedTime, err := time.Parse(time.RFC3339, updatedAt.String); err == nil {
				group.UpdatedAt = parsedTime
				// Debug specific groups
				if group.Name == "adobe.photoshop.windows" || group.Name == "adobe.photoshop.macintosh" || group.Name == "adobe.acrobat.windows" {
					log.Printf("DEBUG hierarchy_cache.go: Group=%s, UpdatedAt string='%s', parsed=%v", group.Name, updatedAt.String, parsedTime)
				}
			} else if parsedTime, err := time.Parse("2006-01-02 15:04:05", updatedAt.String); err == nil {
				// Fallback to MySQL format if RFC3339 fails
				group.UpdatedAt = parsedTime
				// Debug specific groups
				if group.Name == "adobe.photoshop.windows" || group.Name == "adobe.photoshop.macintosh" || group.Name == "adobe.acrobat.windows" {
					log.Printf("DEBUG hierarchy_cache.go: Group=%s, UpdatedAt string='%s', parsed=%v (MySQL format)", group.Name, updatedAt.String, parsedTime)
				}
			} else {
				// Debug parsing errors
				if group.Name == "adobe.photoshop.windows" || group.Name == "adobe.photoshop.macintosh" || group.Name == "adobe.acrobat.windows" {
					log.Printf("DEBUG hierarchy_cache.go: Group=%s, FAILED to parse UpdatedAt='%s', error=%v", group.Name, updatedAt.String, err)
				}
			}
		} else {
			// Debug when updated_at is NULL
			if group.Name == "adobe.photoshop.windows" || group.Name == "adobe.photoshop.macintosh" || group.Name == "adobe.acrobat.windows" {
				log.Printf("DEBUG hierarchy_cache.go: Group=%s, UpdatedAt is NULL/invalid", group.Name)
			}
		}

		allGroups = append(allGroups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if scanErrs > 1 {
		log.Printf("[HIERARCHY] %s: skipped %d newsgroup rows that failed to scan", who, scanErrs)
	}
	return allGroups, nil
}

// buildTree computes the sub-level counts and the sorted direct groups lists of groups.
// The sub-levels are counted from the name's top level, so a group whose hierarchy column
// is NULL or differs gets the same entries as in a top-level rebuild (A-db-8). The three
// lists of one prefix share the *Newsgroup structs (UpdateNewsgroupStats relies on it).
func (hc *HierarchyCache) buildTree(groups []*models.Newsgroup) *hierarchyTree {
	hierarchyGroups := make(map[string][]*models.Newsgroup)
	tree := &hierarchyTree{
		subLevels:    make(map[string]map[string]int),
		directGroups: make(map[string][]*models.Newsgroup),
		directCounts: make(map[string]int),
	}

	for _, group := range groups {
		// Build sub-level counts by parsing group names
		hc.buildSubLevelCounts(group.Name, ExtractHierarchyFromGroupName(group.Name), tree.subLevels)

		// Add groups to the correct hierarchy levels for navigation
		hc.addGroupToAllPossibleLevels(group, hierarchyGroups, tree.directCounts)
	}

	// Direct groups lists, pre-sorted by the different criteria
	for prefix, list := range hierarchyGroups {
		for _, sortBy := range hierarchySortOrders {
			sorted := make([]*models.Newsgroup, len(list))
			copy(sorted, list)
			sortDirectGroups(sorted, sortBy)
			tree.directGroups[prefix+":"+sortBy] = sorted
		}
	}
	return tree
}

// installTreeLocked stores the entries of tree in the caches. hc.mu must be held for writing.
func (hc *HierarchyCache) installTreeLocked(tree *hierarchyTree) {
	for prefix, subLevels := range tree.subLevels {
		hc.subLevelsCache[prefix] = subLevels
		hc.subLevelCounts[prefix] = len(subLevels)
	}
	for key, groups := range tree.directGroups {
		hc.directGroupsCache[key] = groups
	}
	for prefix, count := range tree.directCounts {
		hc.directGroupCounts[prefix] = count
	}
}

// buildSubLevelCounts extracts sub-hierarchy levels from group names
func (hc *HierarchyCache) buildSubLevelCounts(groupName, hierarchy string, subLevelCounts map[string]map[string]int) {
	// Remove hierarchy prefix to get the sub-path
	if !strings.HasPrefix(groupName, hierarchy+".") {
		return // Group doesn't follow expected pattern
	}

	subPath := strings.TrimPrefix(groupName, hierarchy+".")
	parts := strings.Split(subPath, ".")

	// Build counts for each level
	currentPrefix := hierarchy
	for i, part := range parts {
		if i == len(parts)-1 {
			break // Don't count the final part (that's the group itself)
		}

		if subLevelCounts[currentPrefix] == nil {
			subLevelCounts[currentPrefix] = make(map[string]int)
		}

		subLevelCounts[currentPrefix][part]++
		currentPrefix += "." + part
	}
}

// isDirectChild checks if a group is a direct child of the hierarchy (no dots in sub-path)
func (hc *HierarchyCache) isDirectChild(groupName, hierarchy string) bool {
	if !strings.HasPrefix(groupName, hierarchy+".") {
		return false
	}
	subPath := strings.TrimPrefix(groupName, hierarchy+".")
	return !strings.Contains(subPath, ".")
}

// addGroupToAllPossibleLevels adds a group to all hierarchy levels where it could be a direct child
func (hc *HierarchyCache) addGroupToAllPossibleLevels(group *models.Newsgroup, hierarchyGroups map[string][]*models.Newsgroup, directGroupCounts map[string]int) {
	// Parse the group name to find all possible hierarchy levels
	// For example: alt.bikes.spills-mason.ouch belongs to:
	// - alt (top-level hierarchy) - only if it's a direct child (alt.something)
	// - alt.bikes - only if it's a direct child (alt.bikes.something)
	// - alt.bikes.spills-mason - only if it's a direct child (alt.bikes.spills-mason.something)

	parts := strings.Split(group.Name, ".")

	// Build hierarchy levels and only add the group where it's a direct child
	for i := 0; i < len(parts)-1; i++ {
		hierarchyLevel := strings.Join(parts[:i+1], ".")

		// Check if this group is a direct child at this level
		// A group is a direct child if there's exactly one more part after the hierarchy level
		if i == len(parts)-2 {
			// This is the direct parent level - add the group here
			hierarchyGroups[hierarchyLevel] = append(hierarchyGroups[hierarchyLevel], group)
			directGroupCounts[hierarchyLevel]++
		}
	}
}

// UpdateHierarchyLastUpdated updates the cached hierarchy last_updated values from the database
func (hc *HierarchyCache) UpdateHierarchyLastUpdated(db *Database) error {
	// Get updated hierarchy data from database
	rows, err := RetryableQuery(db.mainDB, `
		SELECT name, last_updated
		FROM hierarchies
		ORDER BY name
	`)
	if err != nil {
		return fmt.Errorf("failed to query hierarchies: %w", err)
	}
	defer rows.Close()

	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Update the cached hierarchies with new last_updated values
	updatedCount := 0
	for rows.Next() {
		var name string
		var lastUpdated time.Time

		if err := rows.Scan(&name, &lastUpdated); err != nil {
			log.Printf("HierarchyCache: Error scanning hierarchy %s: %v", name, err)
			continue
		}

		// Find and update this hierarchy in the cache
		for i, hierarchy := range hc.hierarchies {
			if hierarchy.Name == name {
				hc.hierarchies[i].LastUpdated = lastUpdated
				updatedCount++
				break
			}
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating hierarchies: %w", err)
	}

	log.Printf("HierarchyCache: Updated %d hierarchy last_updated values in cache", updatedCount)
	return nil
}
