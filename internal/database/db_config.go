package database

import (
	"database/sql"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
)

// GetConfigValue retrieves a configuration value using the cache layer
func (db *Database) GetConfigValue(key string) (string, error) {
	if db.ConfigCache != nil {
		return db.ConfigCache.GetConfigValue(key)
	}
	// Fallback to direct query if cache is not initialized
	return db.getConfigValueDirect(key)
}

// getConfigValueDirect retrieves a configuration value directly from the database
func (db *Database) getConfigValueDirect(key string) (string, error) {
	var value string
	err := RetryableQueryRowScan(db.mainDB, "SELECT value FROM config WHERE key = ?", []interface{}{key}, &value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil // Return empty string for missing keys
		}
		return "", err
	}
	return value, nil
}

// SetConfigValue sets or updates a configuration value using the cache layer
func (db *Database) SetConfigValue(key, value string) error {
	if db.ConfigCache != nil {
		return db.ConfigCache.SetConfigValue(key, value)
	}
	// Fallback to direct update if cache is not initialized
	return db.setConfigValueDirect(key, value)
}

// setConfigValueDirect sets or updates a configuration value directly in the database
func (db *Database) setConfigValueDirect(key, value string) error {
	_, err := RetryableExec(db.mainDB, `
		INSERT OR REPLACE INTO config (key, value)
		VALUES (?, ?)
	`, key, value)
	return err
}

// GetConfigBool retrieves a boolean configuration value
func (db *Database) GetConfigBool(key string) (bool, error) {
	value, err := db.GetConfigValue(key)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetConfigBool sets a boolean configuration value
func (db *Database) SetConfigBool(key string, value bool) error {
	var stringValue string
	if value {
		stringValue = "true"
	} else {
		stringValue = "false"
	}
	return db.SetConfigValue(key, stringValue)
}

// IsRegistrationEnabled checks if user registration is enabled
func (db *Database) IsRegistrationEnabled() (bool, error) {
	// Default to true if no setting exists
	value, err := db.GetConfigValue("registration_enabled")
	if err != nil {
		return false, err
	}
	if value == "" {
		return true, nil // Default to enabled
	}
	return value == "true", nil
}

// MaxCrossPosts returns the config key MaxCrossPosts: the most newsgroups one article may be
// filed into (1..config.MaxCrossPostsLimit). A missing, invalid or out-of-range value reads as
// config.DefaultMaxCrossPosts. The read goes through the config cache, so it is cheap enough
// for every article.
func (db *Database) MaxCrossPosts() int {
	v, err := db.GetConfigValue(config.CFG_KEY_MAXCROSSPOSTS)
	if err != nil {
		log.Printf("Database: Failed to read %s, using %d: %v", config.CFG_KEY_MAXCROSSPOSTS, config.DefaultMaxCrossPosts, err)
		return config.DefaultMaxCrossPosts
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > config.MaxCrossPostsLimit {
		return config.DefaultMaxCrossPosts
	}
	return n
}

// PeerDateMaxAge returns the admin setting PeerArticleMaxAgeHours as a duration: how old the
// Date (or Injection-Date) of an article a peer feeds us may be before intake refuses it
// (INN artcutoff). 0 means no past cutoff; a missing, invalid, negative or out-of-range
// value reads as config.DefaultPeerDateMaxAgeHours. The read goes through the config cache,
// so it is cheap enough for every article.
func (db *Database) PeerDateMaxAge() time.Duration {
	return db.peerDateLimit(config.CFG_KEY_PEERDATEMAXAGE, config.DefaultPeerDateMaxAgeHours)
}

// PeerDateMaxFuture returns the admin setting PeerArticleMaxFutureHours as a duration: how
// far a peer's article may lie in the future (INN DATE_FUZZ). 0 means no future limit; a
// missing, invalid, negative or out-of-range value reads as
// config.DefaultPeerDateMaxFutureHours.
func (db *Database) PeerDateMaxFuture() time.Duration {
	return db.peerDateLimit(config.CFG_KEY_PEERDATEMAXFUTURE, config.DefaultPeerDateMaxFutureHours)
}

// peerDateLimit reads one of the two peer Date limits (hours) and returns it as a duration.
// A nil database (tests without a main DB) reads as the default.
func (db *Database) peerDateLimit(key string, def int) time.Duration {
	if db == nil {
		return time.Duration(def) * time.Hour
	}
	v, err := db.GetConfigValue(key)
	if err != nil {
		log.Printf("Database: Failed to read %s, using %d: %v", key, def, err)
		return time.Duration(def) * time.Hour
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 || n > config.PeerDateHoursLimit {
		return time.Duration(def) * time.Hour
	}
	return time.Duration(n) * time.Hour
}

// Bounds of the admin setting "Article Size Limit" (config WebPostMaxArticleSize), the range
// the settings form and the web post page accept.
const (
	MinArticleSizeLimit int64 = 1000
	MaxArticleSizeLimit int64 = 16 << 20
)

// ArticleSizeLimit returns the admin setting "Article Size Limit" (config
// WebPostMaxArticleSize) in bytes: the limit of web posts, and of nntp-server when it runs
// without -maxartsize. ok is false when the value is missing, unreadable or out of range.
func (db *Database) ArticleSizeLimit() (int64, bool) {
	v, err := db.GetConfigValue(config.CFG_KEY_WEBPOSTSIZE)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < MinArticleSizeLimit || n > MaxArticleSizeLimit {
		return 0, false
	}
	return n, true
}
