package database

import (
	"log"
	"sync"
	"time"
)

// ConfigCache provides a cache layer for configuration values with global expiration
type ConfigCache struct {
	cache     map[string]string
	mutex     sync.RWMutex
	lastLoad  time.Time
	cacheTime time.Duration
	db        *Database
}

// NewConfigCache creates a new configuration cache with 5-minute refresh interval
func NewConfigCache(db *Database) *ConfigCache {
	cache := &ConfigCache{
		cache:     make(map[string]string),
		cacheTime: 5 * time.Minute,
		db:        db,
	}
	return cache
}

// GetConfigValue retrieves a configuration value from cache or database
func (cc *ConfigCache) GetConfigValue(key string) (string, error) {
	cc.mutex.RLock()

	// Check if cache needs refresh (global expiration)
	needsRefresh := time.Since(cc.lastLoad) > cc.cacheTime

	if !needsRefresh {
		// Return cached value if exists
		if value, exists := cc.cache[key]; exists {
			cc.mutex.RUnlock()
			return value, nil
		}
	}

	cc.mutex.RUnlock()

	// Need to refresh cache or key doesn't exist
	if needsRefresh {
		err := cc.RefreshCache()
		if err != nil {
			log.Printf("Warning: Failed to refresh config cache, falling back to direct DB query: %v", err)
			// Fallback to direct database query
			return cc.getConfigValueDirect(key)
		}

		// Try again with fresh cache
		cc.mutex.RLock()
		value, exists := cc.cache[key]
		cc.mutex.RUnlock()

		if exists {
			return value, nil
		}
	} else {
		// Key doesn't exist in cache but cache is fresh
		// This means the key doesn't exist in the database
		return "", nil
	}

	// Key not found in fresh cache, return empty string
	return "", nil
}

// refreshCache loads all configuration values from the database
func (cc *ConfigCache) RefreshCache() error {
	// Query all config values from database
	rows, err := cc.db.mainDB.Query("SELECT key, value FROM config")
	if err != nil {
		return err
	}
	defer rows.Close()

	newCache := make(map[string]string)

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		newCache[key] = value
	}

	if err := rows.Err(); err != nil {
		return err
	}

	// Update cache atomically
	cc.mutex.Lock()
	cc.cache = newCache
	cc.lastLoad = time.Now()
	cc.mutex.Unlock()

	//log.Printf("Config cache refreshed with %d entries", len(newCache))
	return nil
}

// getConfigValueDirect performs a direct database query (fallback method)
func (cc *ConfigCache) getConfigValueDirect(key string) (string, error) {
	var value string
	err := retryableQueryRowScan(cc.db.mainDB, "SELECT value FROM config WHERE key = ?", []interface{}{key}, &value)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return "", nil // Return empty string for missing keys
		}
		return "", err
	}
	return value, nil
}

// InvalidateCache forces a cache refresh on next access
func (cc *ConfigCache) InvalidateCache() {
	cc.mutex.Lock()
	cc.lastLoad = time.Time{} // Reset to zero time to force refresh
	cc.mutex.Unlock()
}

// SetConfigValue updates both database and cache
func (cc *ConfigCache) SetConfigValue(key, value string) error {
	// Update database first
	err := cc.db.setConfigValueDirect(key, value)
	if err != nil {
		return err
	}

	// Update cache
	cc.mutex.Lock()
	cc.cache[key] = value
	cc.mutex.Unlock()

	return nil
}

// GetCacheStats returns cache statistics for monitoring
func (cc *ConfigCache) GetCacheStats() map[string]interface{} {
	cc.mutex.RLock()
	defer cc.mutex.RUnlock()

	stats := map[string]interface{}{
		"entries":    len(cc.cache),
		"last_load":  cc.lastLoad,
		"cache_time": cc.cacheTime,
		"age":        time.Since(cc.lastLoad),
	}

	return stats
}
