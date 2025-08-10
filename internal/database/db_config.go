package database

import (
	"database/sql"
)

// GetConfigValue retrieves a configuration value from the config table
func (db *Database) GetConfigValue(key string) (string, error) {
	var value string
	err := retryableQueryRowScan(db.mainDB, "SELECT value FROM config WHERE key = ?", []interface{}{key}, &value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil // Return empty string for missing keys
		}
		return "", err
	}
	return value, nil
}

// SetConfigValue sets or updates a configuration value in the config table
func (db *Database) SetConfigValue(key, value string) error {
	_, err := retryableExec(db.mainDB, `
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
