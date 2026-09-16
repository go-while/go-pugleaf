package database

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// Session security constants
const (
	SessionIDLength = 64
)

var (
	SessionTimeout   = 1 * time.Hour   // 1 hour sliding timeout
	LoginLockoutTime = 1 * time.Minute // Lockout time after max attempts
	MaxLoginAttempts = 5               // Max failed login attempts
)

// GenerateSecureSessionID creates a cryptographically secure session ID
func GenerateSecureSessionID() (string, error) {
	bytes := make([]byte, SessionIDLength/2) // hex encoding doubles the length
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate secure session ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// HashSessionToken returns the value stored in users.session_id for a raw session
// token: lowercase hex of its SHA-256. The raw token only ever lives in the cookie,
// so a DB read (backup, copied file, SQL access) does not yield working sessions.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateUserSession creates a new session for the user and invalidates any existing
// session. It returns the raw token for the cookie; the database stores its hash.
func (db *Database) CreateUserSession(userID int64, remoteIP string) (string, error) {
	// Generate new session ID
	sessionID, err := GenerateSecureSessionID()
	if err != nil {
		return "", err
	}

	// Calculate expiration time in UTC for consistent DB comparison
	expiresAt := time.Now().UTC().Add(SessionTimeout)

	// Update user with new session (this invalidates any existing session).
	// login_attempt_at is left alone: a successful login clears the counter, and
	// the lockout window is driven by login_attempt_at only while attempts remain.
	query := `UPDATE users SET
		session_id = ?,
		last_login_ip = ?,
		session_expires_at = ?,
		login_attempts = 0,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`

	_, err = RetryableExec(db.mainDB, query, HashSessionToken(sessionID), remoteIP, expiresAt, userID)
	if err != nil {
		return "", fmt.Errorf("failed to create user session: %w", err)
	}

	return sessionID, nil
}

// ValidateUserSession checks if the session is valid and extends expiration.
// sessionID is the raw token from the cookie; the lookup uses its hash.
func (db *Database) ValidateUserSession(sessionID string) (*models.User, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("empty session ID")
	}

	// Get user by session ID (read operation). user.SessionID holds the hash.
	query := `SELECT id, username, email, password_hash, display_name, session_id,
		last_login_ip, session_expires_at, login_attempts, created_at, updated_at
		FROM users WHERE session_id = ? AND session_expires_at > CURRENT_TIMESTAMP AND disabled = 0`

	var user models.User
	err := RetryableQueryRowScan(db.mainDB, query, []interface{}{HashSessionToken(sessionID)},
		&user.ID, &user.Username, &user.Email, &user.PasswordHash,
		&user.DisplayName, &user.SessionID, &user.LastLoginIP,
		&user.SessionExpiresAt, &user.LoginAttempts, &user.CreatedAt, &user.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("invalid or expired session")
	}

	if user.SessionExpiresAt == nil {
		return nil, fmt.Errorf("invalid or expired session")
	}

	// Extend session expiration (sliding timeout) in UTC, but only once less than half of
	// SessionTimeout remains: sliding on every request costs a write per page view.
	// login_attempt_at is left alone so the login lockout window is not disturbed.
	if time.Until(*user.SessionExpiresAt) < SessionTimeout/2 {
		newExpiresAt := time.Now().UTC().Add(SessionTimeout)
		updateQuery := `UPDATE users SET session_expires_at = ? WHERE id = ?`
		if _, err := RetryableExec(db.mainDB, updateQuery, newExpiresAt, user.ID); err != nil {
			// Log error but don't fail validation
			log.Printf("[DATABASE]: Warning: failed to extend session expiration for user %d: %v", user.ID, err)
		} else {
			user.SessionExpiresAt = &newExpiresAt
		}
	}
	return &user, nil
}

// InvalidateUserSession clears the user's session
func (db *Database) InvalidateUserSession(userID int64) error {
	query := `UPDATE users SET
		session_id = '',
		session_expires_at = NULL,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`
	_, err := RetryableExec(db.mainDB, query, userID)
	return err
}

// InvalidateUserSessionBySessionID clears the session of the raw session token.
func (db *Database) InvalidateUserSessionBySessionID(sessionID string) error {
	// Mirror ValidateUserSession: refuse the empty token before hashing, so an absent
	// cookie cannot become a silent no-op against sha256("").
	if sessionID == "" {
		return fmt.Errorf("empty session ID")
	}
	query := `UPDATE users SET
		session_id = '',
		session_expires_at = NULL,
		updated_at = CURRENT_TIMESTAMP
		WHERE session_id = ?`
	_, err := RetryableExec(db.mainDB, query, HashSessionToken(sessionID))
	return err
}

// IncrementLoginAttempts increases the failed login counter.
// login_attempt_at marks the last attempt (ReserveLoginAttemptByID stamps it before the
// password check, so successful logins move it too) and starts the lockout window.
func (db *Database) IncrementLoginAttempts(username string) error {
	query := `UPDATE users SET
		login_attempts = login_attempts + 1,
		login_attempt_at = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP
		WHERE username = ?`

	_, err := RetryableExec(db.mainDB, query, username)
	return err
}

// ResetLoginAttempts clears the failed login counter and the lockout clock.
func (db *Database) ResetLoginAttempts(userID int64) error {
	query := `UPDATE users SET
		login_attempts = 0,
		login_attempt_at = NULL,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`

	_, err := RetryableExec(db.mainDB, query, userID)
	return err
}

// IsUserLockedOut checks if user is temporarily locked out due to failed attempts
func (db *Database) IsUserLockedOut(username string) (bool, error) {
	query := `SELECT COALESCE(login_attempts, 0), login_attempt_at FROM users WHERE username = ?`

	var attempts int
	var attemptAt sql.NullTime
	err := RetryableQueryRowScan(db.mainDB, query, []interface{}{username}, &attempts, &attemptAt)
	if err != nil {
		return false, err
	}

	// Check if user has exceeded max attempts
	if attempts >= MaxLoginAttempts {
		// Check if lockout period has expired
		if attemptAt.Valid && time.Now().Before(attemptAt.Time.Add(LoginLockoutTime)) {
			return true, nil // Still locked out
		}
		// Lockout period expired, reset attempts
		resetQuery := `UPDATE users SET login_attempts = 0, login_attempt_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE username = ?`
		if _, err := RetryableExec(db.mainDB, resetQuery, username); err != nil {
			log.Printf("[DATABASE]: failed to reset login attempts for user %s: %v", username, err)
		}
	}

	return false, nil
}

// IncrementLoginAttemptsByID increases the failed login counter of the user with the given ID.
// login_attempt_at marks the last attempt (ReserveLoginAttemptByID stamps it before the
// password check, so successful logins move it too) and starts the lockout window.
func (db *Database) IncrementLoginAttemptsByID(userID int64) error {
	query := `UPDATE users SET
		login_attempts = login_attempts + 1,
		login_attempt_at = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`

	_, err := RetryableExec(db.mainDB, query, userID)
	return err
}

// IsUserLockedOutByID checks if the user with the given ID is temporarily locked out due to
// failed login attempts. An expired lockout resets the counter.
func (db *Database) IsUserLockedOutByID(userID int64) (bool, error) {
	query := `SELECT COALESCE(login_attempts, 0), login_attempt_at FROM users WHERE id = ?`

	var attempts int
	var attemptAt sql.NullTime
	err := RetryableQueryRowScan(db.mainDB, query, []interface{}{userID}, &attempts, &attemptAt)
	if err != nil {
		return false, err
	}

	if attempts < MaxLoginAttempts {
		return false, nil
	}
	if attemptAt.Valid && time.Now().Before(attemptAt.Time.Add(LoginLockoutTime)) {
		return true, nil // Still locked out
	}

	// Lockout period expired, reset attempts
	resetQuery := `UPDATE users SET login_attempts = 0, login_attempt_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	if _, err := RetryableExec(db.mainDB, resetQuery, userID); err != nil {
		log.Printf("[DATABASE]: failed to reset login attempts for user %d: %v", userID, err)
	}
	return false, nil
}

// ReserveLoginAttemptByID atomically counts one login attempt for the user before the
// password is checked. It returns allowed=false (and counts nothing) while the user has
// MaxLoginAttempts attempts inside the LoginLockoutTime window since the last counted one.
// Once the window has passed, the counter restarts at 1. A successful login resets it
// (CreateUserSession). Being a single UPDATE, concurrent attempts cannot exceed the limit.
func (db *Database) ReserveLoginAttemptByID(userID int64) (bool, error) {
	query := `UPDATE users SET
		login_attempts = CASE WHEN COALESCE(login_attempts, 0) >= ? THEN 1 ELSE COALESCE(login_attempts, 0) + 1 END,
		login_attempt_at = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND (COALESCE(login_attempts, 0) < ?
			OR datetime(login_attempt_at) IS NULL
			OR datetime(login_attempt_at) < datetime('now', ?))`

	window := fmt.Sprintf("-%d seconds", int64(LoginLockoutTime/time.Second))
	res, err := RetryableExec(db.mainDB, query, MaxLoginAttempts, userID, MaxLoginAttempts, window)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// CleanupExpiredSessions removes expired sessions from the database
func (db *Database) CleanupExpiredSessions() error {
	query := `UPDATE users SET
		session_id = '',
		session_expires_at = NULL,
		updated_at = CURRENT_TIMESTAMP
		WHERE session_expires_at < CURRENT_TIMESTAMP`

	result, err := RetryableExec(db.mainDB, query)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		fmt.Printf("Cleaned up %d expired sessions\n", rowsAffected)
	}

	return nil
}
