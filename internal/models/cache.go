package models

import (
	"html/template"
	"time"

	"github.com/go-while/go-pugleaf/internal/cache"
)

// Global toggle to enable/disable the sanitized cache at compile-time.
// Set to true to completely bypass Get/Set operations for sanitized cache.
var DisableSanitizedCache = true

// Global sanitized cache instance
var sanitizedCache *cache.SanitizedCache

// InitSanitizedCache initializes the global sanitized cache
func InitSanitizedCache(maxEntries int, maxAge time.Duration) {
	if DisableSanitizedCache {
		sanitizedCache = nil
		return
	}
	sanitizedCache = cache.NewSanitizedCache(maxEntries, maxAge)
}

// GetSanitizedCache returns the global sanitized cache instance
func GetSanitizedCache() *cache.SanitizedCache {
	return sanitizedCache
}

// sanitizedCacheKey builds the sanitized-cache key for one article in one
// group (A-web-10). Message-IDs are only unique per group, so the group is part
// of the key. It returns "" (do not cache) when either part is empty.
func sanitizedCacheKey(groupName, messageID string) string {
	if groupName == "" || messageID == "" {
		return ""
	}
	return groupName + "\x00" + messageID
}

// firstGroupName returns the optional group argument of the display functions.
func firstGroupName(groupName []string) string {
	if len(groupName) == 0 {
		return ""
	}
	return groupName[0]
}

// GetCachedSanitized retrieves a cached sanitized field value of an article in a group.
// Without a group or message ID nothing is cached.
func GetCachedSanitized(groupName, messageID, field string) (template.HTML, bool) {
	key := sanitizedCacheKey(groupName, messageID)
	if DisableSanitizedCache || sanitizedCache == nil || key == "" {
		return "", false
	}
	return sanitizedCache.GetField(key, field)
}

// SetCachedSanitized stores a sanitized field value of an article in a group.
// Without a group or message ID nothing is cached.
func SetCachedSanitized(groupName, messageID, field string, value template.HTML) {
	key := sanitizedCacheKey(groupName, messageID)
	if DisableSanitizedCache || sanitizedCache == nil || key == "" {
		return
	}
	sanitizedCache.SetField(key, field, value)
}

// Global newsgroup cache instance
var newsgroupCache *cache.NewsgroupCache

// InitNewsgroupCache initializes the global newsgroup cache
func InitNewsgroupCache(maxEntries int, maxAge time.Duration) {
	newsgroupCache = cache.NewNewsgroupCache(maxEntries, maxAge)
}

// GetNewsgroupCache returns the global newsgroup cache instance
func GetNewsgroupCache() *cache.NewsgroupCache {
	return newsgroupCache
}

// ClearNewsgroupCache clears all cached newsgroup data
func ClearNewsgroupCache() {
	if newsgroupCache != nil {
		newsgroupCache.Clear()
	}
}

// ClearSanitizedCache clears all cached sanitized article data
// This is useful when encoding logic changes and cached articles need to be re-processed
func ClearSanitizedCache() {
	if sanitizedCache != nil {
		sanitizedCache.Clear()
	}
}

// GetCachedNewsgroups retrieves cached newsgroup data
func GetCachedNewsgroups(page, pageSize int) ([]*Newsgroup, int, bool) {
	if newsgroupCache == nil {
		return nil, 0, false
	}

	// Get from cache (returns cache.Newsgroup slice)
	cacheGroups, totalCount, found := newsgroupCache.Get(page, pageSize)
	if !found {
		return nil, 0, false
	}

	// Convert cache.Newsgroup to models.Newsgroup
	groups := make([]*Newsgroup, len(cacheGroups))
	for i, cg := range cacheGroups {
		if cg != nil {
			groups[i] = &Newsgroup{
				ID:           cg.ID,
				Name:         cg.Name,
				Active:       cg.Active,
				Description:  cg.Description,
				LastArticle:  cg.LastArticle,
				MessageCount: cg.MessageCount,
				ExpiryDays:   cg.ExpiryDays,
				MaxArticles:  cg.MaxArticles,
				HighWater:    cg.HighWater,
				LowWater:     cg.LowWater,
				Status:       cg.Status,
				CreatedAt:    cg.CreatedAt,
				UpdatedAt:    cg.UpdatedAt,
			}
		}
	}

	return groups, totalCount, true
}

// SetCachedNewsgroups stores newsgroup data in cache
func SetCachedNewsgroups(page, pageSize int, groups []*Newsgroup, totalCount int) {
	if newsgroupCache == nil || len(groups) == 0 {
		return
	}

	// Convert models.Newsgroup to cache.Newsgroup
	cacheGroups := make([]*cache.Newsgroup, len(groups))
	for i, g := range groups {
		if g != nil {
			cacheGroups[i] = &cache.Newsgroup{
				ID:           g.ID,
				Name:         g.Name,
				Active:       g.Active,
				Description:  g.Description,
				LastArticle:  g.LastArticle,
				MessageCount: g.MessageCount,
				ExpiryDays:   g.ExpiryDays,
				MaxArticles:  g.MaxArticles,
				HighWater:    g.HighWater,
				LowWater:     g.LowWater,
				Status:       g.Status,
				CreatedAt:    g.CreatedAt,
				UpdatedAt:    g.UpdatedAt,
			}
		}
	}

	newsgroupCache.Set(page, pageSize, cacheGroups, totalCount)
}
