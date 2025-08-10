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

	// Invalidation throttling (5 minute cooldown)
	lastInvalidation     time.Time
	invalidationCooldown time.Duration
}

// NewHierarchyCache creates a new hierarchy cache
func NewHierarchyCache() *HierarchyCache {
	return &HierarchyCache{
		hierarchiesMap:       make(map[string]*models.Hierarchy),
		subLevelsCache:       make(map[string]map[string]int),
		subLevelCounts:       make(map[string]int),
		directGroupsCache:    make(map[string][]*models.Newsgroup),
		directGroupCounts:    make(map[string]int),
		warmComplete:         make(chan struct{}),
		invalidationCooldown: 5 * time.Minute, // 5 minute throttle
	}
} // WarmCache pre-loads all hierarchy data in background with bulk operations
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

		// Update last invalidation time to now since cache is fully rebuilt
		hc.mu.Lock()
		hc.lastInvalidation = time.Now()
		hc.mu.Unlock()
		log.Printf("HierarchyCache: Reset invalidation timer - next invalidation allowed after %v",
			hc.lastInvalidation.Add(hc.invalidationCooldown).Format("15:04:05"))
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

	// Copy hierarchies for processing
	hierarchies := make([]*models.Hierarchy, len(hc.hierarchies))
	copy(hierarchies, hc.hierarchies)
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
	start := (page - 1) * pageSize
	end := start + pageSize

	if start >= len(hierarchies) {
		return []*models.Hierarchy{}, len(hierarchies), nil
	}

	if end > len(hierarchies) {
		end = len(hierarchies)
	}

	return hierarchies[start:end], len(hierarchies), nil
}

// GetHierarchySubLevels returns cached sub-level data
func (hc *HierarchyCache) GetHierarchySubLevels(db *Database, prefix string, page, pageSize int) (map[string]int, int, error) {
	hc.mu.RLock()
	subLevels, exists := hc.subLevelsCache[prefix]
	totalCount := hc.subLevelCounts[prefix]
	hc.mu.RUnlock()

	// If not in cache, return empty (cache should be warmed on startup)
	if !exists {
		log.Printf("HierarchyCache: No sub-levels found for prefix: %s", prefix)
		return make(map[string]int), 0, nil
	}

	// Convert to sorted slice for pagination
	type subLevel struct {
		name  string
		count int
	}

	var sorted []subLevel
	for name, count := range subLevels {
		sorted = append(sorted, subLevel{name, count})
	}

	// Sort alphabetically
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].name < sorted[j].name
	})

	// Apply pagination
	start := (page - 1) * pageSize
	end := start + pageSize

	if start >= len(sorted) {
		return make(map[string]int), totalCount, nil
	}

	if end > len(sorted) {
		end = len(sorted)
	}

	result := make(map[string]int)
	for _, item := range sorted[start:end] {
		result[item.name] = item.count
	}

	return result, totalCount, nil
}

// GetDirectGroupsAtLevel returns cached direct groups data
func (hc *HierarchyCache) GetDirectGroupsAtLevel(db *Database, prefix, sortBy string, page, pageSize int) ([]*models.Newsgroup, int, error) {
	cacheKey := prefix + ":" + sortBy

	hc.mu.RLock()
	groups, exists := hc.directGroupsCache[cacheKey]
	totalCount := hc.directGroupCounts[prefix]
	hc.mu.RUnlock()

	// If not in cache, return empty (cache should be warmed on startup)
	if !exists {
		log.Printf("HierarchyCache: No direct groups found for prefix: %s", prefix)
		return []*models.Newsgroup{}, 0, nil
	}

	// Apply pagination
	start := (page - 1) * pageSize
	end := start + pageSize

	if start >= len(groups) {
		return []*models.Newsgroup{}, totalCount, nil
	}

	if end > len(groups) {
		end = len(groups)
	}

	return groups[start:end], totalCount, nil
}

// UpdateNewsgroupActiveStatus updates the active status of a newsgroup in cache
func (hc *HierarchyCache) UpdateNewsgroupActiveStatus(newsgroupName string, active bool) {
	hierarchy := ExtractHierarchyFromGroupName(newsgroupName)

	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Update direct groups cache for all sort orders
	sortOrders := []string{"name", "activity", "groups"}
	for _, sortBy := range sortOrders {
		cacheKey := hierarchy + ":" + sortBy
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

// UpdateNewsgroupStats updates cached newsgroup statistics incrementally
func (hc *HierarchyCache) UpdateNewsgroupStats(newsgroupName string, messageCountIncrement int, newLastArticle int64) {
	hierarchy := ExtractHierarchyFromGroupName(newsgroupName)

	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Update hierarchy data if cached
	if h, exists := hc.hierarchiesMap[hierarchy]; exists {
		h.GroupCount += messageCountIncrement
		if newLastArticle > h.LastUpdated.Unix() {
			h.LastUpdated = time.Unix(newLastArticle, 0)
		}
	}

	// Update direct groups cache for all sort orders
	sortOrders := []string{"name", "activity", "groups"}
	for _, sortBy := range sortOrders {
		cacheKey := hierarchy + ":" + sortBy
		if groups, exists := hc.directGroupsCache[cacheKey]; exists {
			// Find and update the specific newsgroup in the cached list
			for _, group := range groups {
				if group.Name == newsgroupName {
					group.MessageCount += int64(messageCountIncrement)
					if newLastArticle > group.LastArticle {
						group.LastArticle = newLastArticle
					}
					break
				}
			}

			// Re-sort if necessary (only for activity and groups sort orders)
			if sortBy == "activity" {
				sort.Slice(groups, func(i, j int) bool {
					return groups[i].LastArticle > groups[j].LastArticle
				})
			} else if sortBy == "groups" {
				sort.Slice(groups, func(i, j int) bool {
					return groups[i].MessageCount > groups[j].MessageCount
				})
			}
		}
	}

	log.Printf("HierarchyCache: Updated stats for newsgroup '%s': +%d messages, last_article=%d",
		newsgroupName, messageCountIncrement, newLastArticle)
}

// InvalidateHierarchy invalidates cache entries for a specific hierarchy with throttling
func (hc *HierarchyCache) InvalidateHierarchy(hierarchyName string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Check if we're within the cooldown period
	timeSinceLastInvalidation := time.Since(hc.lastInvalidation)
	if timeSinceLastInvalidation < hc.invalidationCooldown {
		log.Printf("HierarchyCache: Throttling invalidation for hierarchy '%s' - last invalidation was %v ago (cooldown: %v)",
			hierarchyName, timeSinceLastInvalidation.Round(time.Second), hc.invalidationCooldown)
		return
	}

	// Proceed with invalidation
	log.Printf("HierarchyCache: Starting invalidation for hierarchy '%s' (last invalidation was %v ago)",
		hierarchyName, timeSinceLastInvalidation.Round(time.Second))

	// Invalidate sub-level caches for this hierarchy and its parents
	prefixes := []string{hierarchyName}
	parts := strings.Split(hierarchyName, ".")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], ".")
		prefixes = append(prefixes, prefix)
	}

	for _, prefix := range prefixes {
		delete(hc.subLevelsCache, prefix)
		delete(hc.subLevelCounts, prefix)

		// Invalidate direct groups cache for different sort orders
		for _, sortBy := range []string{"name", "activity", "groups"} {
			cacheKey := prefix + ":" + sortBy
			delete(hc.directGroupsCache, cacheKey)
		}
	}

	// Update last invalidation time to now
	hc.lastInvalidation = time.Now()

	log.Printf("HierarchyCache: Invalidated cache for hierarchy '%s' - next invalidation allowed after %v",
		hierarchyName, hc.lastInvalidation.Add(hc.invalidationCooldown).Format("15:04:05"))
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

	// Reset invalidation timer since all cache data is cleared
	hc.lastInvalidation = time.Now()

	log.Printf("HierarchyCache: All cache data invalidated - next invalidation allowed after %v",
		hc.lastInvalidation.Add(hc.invalidationCooldown).Format("15:04:05"))
}

// ForceInvalidateHierarchy forces invalidation bypassing the throttle (use sparingly)
func (hc *HierarchyCache) ForceInvalidateHierarchy(hierarchyName string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	log.Printf("HierarchyCache: FORCE invalidation for hierarchy '%s' (bypassing %v cooldown)",
		hierarchyName, hc.invalidationCooldown)

	// Invalidate sub-level caches for this hierarchy and its parents
	prefixes := []string{hierarchyName}
	parts := strings.Split(hierarchyName, ".")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], ".")
		prefixes = append(prefixes, prefix)
	}

	for _, prefix := range prefixes {
		delete(hc.subLevelsCache, prefix)
		delete(hc.subLevelCounts, prefix)

		// Invalidate direct groups cache for different sort orders
		for _, sortBy := range []string{"name", "activity", "groups"} {
			cacheKey := prefix + ":" + sortBy
			delete(hc.directGroupsCache, cacheKey)
		}
	}

	// Update last invalidation time to now
	hc.lastInvalidation = time.Now()

	log.Printf("HierarchyCache: FORCE invalidated cache for hierarchy '%s' - next invalidation allowed after %v",
		hierarchyName, hc.lastInvalidation.Add(hc.invalidationCooldown).Format("15:04:05"))
}

// GetInvalidationStatus returns information about the throttling status
func (hc *HierarchyCache) GetInvalidationStatus() (time.Time, time.Duration, time.Duration) {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	timeSinceLastInvalidation := time.Since(hc.lastInvalidation)
	timeUntilNextAllowed := hc.invalidationCooldown - timeSinceLastInvalidation
	if timeUntilNextAllowed < 0 {
		timeUntilNextAllowed = 0
	}

	return hc.lastInvalidation, timeSinceLastInvalidation, timeUntilNextAllowed
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

// buildHierarchyTreeBulk builds the complete hierarchy tree structure with one query
func (hc *HierarchyCache) buildHierarchyTreeBulk(db *Database) error {
	log.Printf("HierarchyCache: Building hierarchy tree from all newsgroups...")

	// Get ALL active newsgroups with their hierarchies in one query
	rows, err := RetryableQuery(db.mainDB, `
		SELECT name, hierarchy, message_count, last_article, updated_at
		FROM newsgroups
		WHERE active = 1 AND message_count > 0
		ORDER BY hierarchy, name
	`)
	if err != nil {
		return fmt.Errorf("failed to query all newsgroups: %w", err)
	}
	defer rows.Close()

	// Maps to build the hierarchy structure
	hierarchyGroups := make(map[string][]*models.Newsgroup)
	subLevelCounts := make(map[string]map[string]int)
	directGroupCounts := make(map[string]int)

	var allGroups []*models.Newsgroup

	// Process all newsgroups and build the structure
	for rows.Next() {
		group := &models.Newsgroup{}
		var lastArticle sql.NullInt64
		var updatedAt sql.NullString

		err := rows.Scan(&group.Name, &group.Hierarchy, &group.MessageCount, &lastArticle, &updatedAt)
		if err != nil {
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

		// Build sub-level counts by parsing group names
		hc.buildSubLevelCounts(group.Name, group.Hierarchy, subLevelCounts)

		// Add groups to the correct hierarchy levels for navigation
		hc.addGroupToAllPossibleLevels(group, hierarchyGroups, directGroupCounts)
	}

	// Now populate all the caches
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Clear existing cache
	hc.subLevelsCache = make(map[string]map[string]int)
	hc.subLevelCounts = make(map[string]int)
	hc.directGroupsCache = make(map[string][]*models.Newsgroup)
	hc.directGroupCounts = make(map[string]int)

	// Populate sub-level caches
	for prefix, subLevels := range subLevelCounts {
		hc.subLevelsCache[prefix] = subLevels
		hc.subLevelCounts[prefix] = len(subLevels)
	}

	// Populate direct groups caches (pre-sorted by different criteria)
	for hierarchy, groups := range hierarchyGroups {
		// Sort by name
		nameGroups := make([]*models.Newsgroup, len(groups))
		copy(nameGroups, groups)
		sort.Slice(nameGroups, func(i, j int) bool {
			return nameGroups[i].Name < nameGroups[j].Name
		})
		hc.directGroupsCache[hierarchy+":name"] = nameGroups

		// Sort by activity (last_article)
		activityGroups := make([]*models.Newsgroup, len(groups))
		copy(activityGroups, groups)
		sort.Slice(activityGroups, func(i, j int) bool {
			return activityGroups[i].LastArticle > activityGroups[j].LastArticle
		})
		hc.directGroupsCache[hierarchy+":activity"] = activityGroups

		// Sort by message count
		groupsGroups := make([]*models.Newsgroup, len(groups))
		copy(groupsGroups, groups)
		sort.Slice(groupsGroups, func(i, j int) bool {
			return groupsGroups[i].MessageCount > groupsGroups[j].MessageCount
		})
		hc.directGroupsCache[hierarchy+":groups"] = groupsGroups

		// Set direct group count
		hc.directGroupCounts[hierarchy] = len(groups)
	}

	log.Printf("HierarchyCache: Built complete hierarchy tree with %d groups, %d hierarchies, %d sub-levels",
		len(allGroups), len(hierarchyGroups), len(subLevelCounts))
	return nil
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
