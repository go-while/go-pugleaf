package database

import (
	"database/sql"
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
