package database

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// Session security constants
const (
	SessionIDLength  = 64               // 64 character session ID
	SessionTimeout   = 3 * time.Hour    // 3 hour sliding timeout
	MaxLoginAttempts = 5                // Max failed login attempts
	LoginLockoutTime = 15 * time.Minute // Lockout time after max attempts
)

// GenerateSecureSessionID creates a cryptographically secure session ID
func GenerateSecureSessionID() (string, error) {
	bytes := make([]byte, SessionIDLength/2) // hex encoding doubles the length
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate secure session ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// CreateUserSession creates a new session for the user and invalidates any existing session
func (db *Database) CreateUserSession(userID int, remoteIP string) (string, error) {
	// Generate new session ID
	sessionID, err := GenerateSecureSessionID()
	if err != nil {
		return "", err
	}

	// Calculate expiration time (3 hours from now)
	expiresAt := time.Now().Add(SessionTimeout)

	// Update user with new session (this invalidates any existing session)
	query := `UPDATE users SET
		session_id = ?,
		last_login_ip = ?,
		session_expires_at = ?,
		login_attempts = 0,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`

	_, err = retryableExec(db.mainDB, query, sessionID, remoteIP, expiresAt, userID)
	if err != nil {
		return "", fmt.Errorf("failed to create user session: %w", err)
	}

	return sessionID, nil
}

// ValidateUserSession checks if the session is valid and extends expiration
func (db *Database) ValidateUserSession(sessionID string) (*models.User, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("empty session ID")
	}

	// Get user by session ID (read operation)
	query := `SELECT id, username, email, password_hash, display_name, session_id,
		last_login_ip, session_expires_at, login_attempts, created_at, updated_at
		FROM users WHERE session_id = ? AND session_expires_at > CURRENT_TIMESTAMP`

	var user models.User
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{sessionID},
		&user.ID, &user.Username, &user.Email, &user.PasswordHash,
		&user.DisplayName, &user.SessionID, &user.LastLoginIP,
		&user.SessionExpiresAt, &user.LoginAttempts, &user.CreatedAt, &user.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("invalid or expired session")
	}

	// Extend session expiration (sliding timeout) - write operation
	newExpiresAt := time.Now().Add(SessionTimeout)
	updateQuery := `UPDATE users SET session_expires_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err = retryableExec(db.mainDB, updateQuery, newExpiresAt, user.ID)
	if err != nil {
		// Log error but don't fail validation
		fmt.Printf("Warning: Failed to extend session expiration: %v\n", err)
	}

	// Update the user struct with new expiration
	user.SessionExpiresAt = &newExpiresAt
	return &user, nil
}

// InvalidateUserSession clears the user's session
func (db *Database) InvalidateUserSession(userID int) error {
	query := `UPDATE users SET
		session_id = '',
		session_expires_at = NULL,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, userID)
	return err
}

// InvalidateUserSessionBySessionID clears session by session ID
func (db *Database) InvalidateUserSessionBySessionID(sessionID string) error {
	query := `UPDATE users SET
		session_id = '',
		session_expires_at = NULL,
		updated_at = CURRENT_TIMESTAMP
		WHERE session_id = ?`
	_, err := retryableExec(db.mainDB, query, sessionID)
	return err
}

// IncrementLoginAttempts increases the failed login counter
func (db *Database) IncrementLoginAttempts(username string) error {
	query := `UPDATE users SET
		login_attempts = login_attempts + 1,
		updated_at = CURRENT_TIMESTAMP
		WHERE username = ?`

	_, err := retryableExec(db.mainDB, query, username)
	return err
}

// ResetLoginAttempts clears the failed login counter
func (db *Database) ResetLoginAttempts(userID int) error {
	query := `UPDATE users SET
		login_attempts = 0,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`

	_, err := retryableExec(db.mainDB, query, userID)
	return err
}

// IsUserLockedOut checks if user is temporarily locked out due to failed attempts
func (db *Database) IsUserLockedOut(username string) (bool, error) {
	query := `SELECT login_attempts, updated_at FROM users WHERE username = ?`

	var attempts int
	var updatedAt time.Time
	err := db.mainDB.QueryRow(query, username).Scan(&attempts, &updatedAt)
	if err != nil {
		return false, err
	}

	// Check if user has exceeded max attempts
	if attempts >= MaxLoginAttempts {
		// Check if lockout period has expired
		lockoutExpires := updatedAt.Add(LoginLockoutTime)
		if time.Now().Before(lockoutExpires) {
			return true, nil // Still locked out
		} else {
			// Lockout period expired, reset attempts
			resetQuery := `UPDATE users SET login_attempts = 0, updated_at = CURRENT_TIMESTAMP WHERE username = ?`
			db.mainDB.Exec(resetQuery, username)
		}
	}

	return false, nil
}

// CleanupExpiredSessions removes expired sessions from the database
func (db *Database) CleanupExpiredSessions() error {
	query := `UPDATE users SET
		session_id = '',
		session_expires_at = NULL,
		updated_at = CURRENT_TIMESTAMP
		WHERE session_expires_at < CURRENT_TIMESTAMP`

	result, err := db.mainDB.Exec(query)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		fmt.Printf("Cleaned up %d expired sessions\n", rowsAffected)
	}

	return nil
}
