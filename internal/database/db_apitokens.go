package database

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// APIToken represents an API token record
type APIToken struct {
	ID         int        `db:"id"`
	APIToken   string     `db:"apitoken"`
	OwnerName  string     `db:"ownername"`
	OwnerID    int        `db:"ownerid"`
	CreatedAt  time.Time  `db:"created_at"`
	LastUsedAt *time.Time `db:"last_used_at"`
	ExpiresAt  *time.Time `db:"expires_at"`
	IsEnabled  bool       `db:"is_enabled"`
	UsageCount int        `db:"usage_count"`
}

// GenerateAPIToken creates a new cryptographically secure API token
func GenerateAPIToken() (string, error) {
	bytes := make([]byte, 32) // 32 bytes = 64 hex characters
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// HashToken creates a SHA-256 hash of the token for database storage
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// CreateAPIToken generates and stores a new API token
func (db *Database) CreateAPIToken(ownerName string, ownerID int, expiresAt *time.Time) (*APIToken, string, error) {
	// Generate plain token
	plainToken, err := GenerateAPIToken()
	if err != nil {
		return nil, "", err
	}

	// Hash for storage
	hashedToken := HashToken(plainToken)

	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	query := `INSERT INTO api_tokens (apitoken, ownername, ownerid, expires_at, is_enabled)
	          VALUES (?, ?, ?, ?, 1)`

	result, err := retryableExec(db.mainDB, query, hashedToken, ownerName, ownerID, expiresAt)
	if err != nil {
		return nil, "", err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, "", err
	}

	token := &APIToken{
		ID:         int(id),
		APIToken:   hashedToken,
		OwnerName:  ownerName,
		OwnerID:    ownerID,
		CreatedAt:  time.Now(),
		ExpiresAt:  expiresAt,
		IsEnabled:  true,
		UsageCount: 0,
	}

	return token, plainToken, nil
}

// ValidateAPIToken checks if a token exists, is enabled, and not expired
func (db *Database) ValidateAPIToken(plainToken string) (*APIToken, error) {
	hashedToken := HashToken(plainToken)

	db.MainMutex.RLock()
	defer db.MainMutex.RUnlock()

	query := `SELECT id, apitoken, ownername, ownerid, created_at, last_used_at, expires_at, is_enabled, usage_count
	          FROM api_tokens
	          WHERE apitoken = ? AND is_enabled = 1`

	var token APIToken
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{hashedToken},
		&token.ID, &token.APIToken, &token.OwnerName, &token.OwnerID,
		&token.CreatedAt, &token.LastUsedAt, &token.ExpiresAt,
		&token.IsEnabled, &token.UsageCount,
	)
	if err != nil {
		return nil, err // Token not found or disabled
	}

	// Check if token has expired
	if token.ExpiresAt != nil && token.ExpiresAt.Before(time.Now()) {
		return nil, err // Token expired
	}

	return &token, nil
}

// UpdateTokenUsage updates the last_used_at timestamp and increments usage_count
func (db *Database) UpdateTokenUsage(tokenID int) error {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	query := `UPDATE api_tokens
	          SET last_used_at = CURRENT_TIMESTAMP, usage_count = usage_count + 1
	          WHERE id = ?`

	_, err := retryableExec(db.mainDB, query, tokenID)
	return err
}

// ListAPITokens returns all API tokens (for admin purposes)
func (db *Database) ListAPITokens() ([]*APIToken, error) {
	db.MainMutex.RLock()
	defer db.MainMutex.RUnlock()

	query := `SELECT id, apitoken, ownername, ownerid, created_at, last_used_at, expires_at, is_enabled, usage_count
	          FROM api_tokens
	          ORDER BY created_at DESC`

	rows, err := retryableQuery(db.mainDB, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*APIToken
	for rows.Next() {
		var token APIToken
		err := rows.Scan(
			&token.ID, &token.APIToken, &token.OwnerName, &token.OwnerID,
			&token.CreatedAt, &token.LastUsedAt, &token.ExpiresAt,
			&token.IsEnabled, &token.UsageCount,
		)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, &token)
	}

	return tokens, nil
}

// DisableAPIToken deactivates a token
func (db *Database) DisableAPIToken(tokenID int) error {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	query := `UPDATE api_tokens SET is_enabled = 0 WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, tokenID)
	return err
}

// EnableAPIToken reactivates a token
func (db *Database) EnableAPIToken(tokenID int) error {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	query := `UPDATE api_tokens SET is_enabled = 1 WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, tokenID)
	return err
}

// DeleteAPIToken permanently removes a token
func (db *Database) DeleteAPIToken(tokenID int) error {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	query := `DELETE FROM api_tokens WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, tokenID)
	return err
}

// CleanupExpiredTokens removes expired tokens from the database
func (db *Database) CleanupExpiredTokens() (int, error) {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	query := `DELETE FROM api_tokens WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP`
	result, err := retryableExec(db.mainDB, query)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	return int(rowsAffected), err
}
