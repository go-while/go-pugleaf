package database

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// NNTP User Management Functions

// InsertNNTPUser creates a new NNTP user with bcrypt password hashing
func (db *Database) InsertNNTPUser(u *models.NNTPUser) error {
	// Hash the password using bcrypt
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	query := `INSERT INTO nntp_users (username, password, maxconns, posting, web_user_id, is_active)
	          VALUES (?, ?, ?, ?, ?, ?)`
	_, err = retryableExec(db.mainDB, query, u.Username, string(hashedPassword), u.MaxConns, u.Posting, u.WebUserID, u.IsActive)
	return err
}

// GetNNTPUserByUsername retrieves an NNTP user by username
func (db *Database) GetNNTPUserByUsername(username string) (*models.NNTPUser, error) {
	query := `SELECT id, username, password, maxconns, posting, web_user_id, created_at, updated_at, last_login, is_active
	          FROM nntp_users WHERE username = ? AND is_active = 1`

	var u models.NNTPUser
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{username}, &u.ID, &u.Username, &u.Password, &u.MaxConns, &u.Posting, &u.WebUserID,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.IsActive)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetNNTPUserByID retrieves an NNTP user by ID
func (db *Database) GetNNTPUserByID(id int) (*models.NNTPUser, error) {
	query := `SELECT id, username, password, maxconns, posting, web_user_id, created_at, updated_at, last_login, is_active
	          FROM nntp_users WHERE id = ?`

	var u models.NNTPUser
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{id}, &u.ID, &u.Username, &u.Password, &u.MaxConns, &u.Posting, &u.WebUserID,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.IsActive)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetAllNNTPUsers retrieves all NNTP users
func (db *Database) GetAllNNTPUsers() ([]*models.NNTPUser, error) {
	query := `SELECT id, username, password, maxconns, posting, web_user_id, created_at, updated_at, last_login, is_active
	          FROM nntp_users ORDER BY username`
	rows, err := retryableQuery(db.mainDB, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*models.NNTPUser
	for rows.Next() {
		var u models.NNTPUser
		err := rows.Scan(&u.ID, &u.Username, &u.Password, &u.MaxConns, &u.Posting, &u.WebUserID,
			&u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.IsActive)
		if err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	return users, nil
}

// VerifyNNTPUserPassword verifies a user's password against the stored bcrypt hash
func (db *Database) VerifyNNTPUserPassword(username, password string) (*models.NNTPUser, error) {
	user, err := db.GetNNTPUserByUsername(username)
	if err != nil {
		return nil, err
	}

	// Verify the password against the stored hash
	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password))
	if err != nil {
		return nil, fmt.Errorf("invalid password")
	}

	return user, nil
}

// UpdateNNTPUserPassword updates an NNTP user's password with bcrypt hashing
func (db *Database) UpdateNNTPUserPassword(userID int, password string) error {
	// Hash the new password using bcrypt
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	query := `UPDATE nntp_users SET password = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err = retryableExec(db.mainDB, query, string(hashedPassword), userID)
	return err
}

// UpdateNNTPUserLastLogin updates the last login timestamp
func (db *Database) UpdateNNTPUserLastLogin(userID int) error {
	query := `UPDATE nntp_users SET last_login = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, userID)
	return err
}

// UpdateNNTPUserPermissions updates maxconns and posting permissions
func (db *Database) UpdateNNTPUserPermissions(userID int, maxConns int, posting bool) error {
	query := `UPDATE nntp_users SET maxconns = ?, posting = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, maxConns, posting, userID)
	return err
}

// DeactivateNNTPUser deactivates an NNTP user (soft delete)
func (db *Database) DeactivateNNTPUser(userID int) error {
	query := `UPDATE nntp_users SET is_active = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, userID)
	return err
}

// ActivateNNTPUser activates an NNTP user (reverses soft delete)
func (db *Database) ActivateNNTPUser(userID int) error {
	query := `UPDATE nntp_users SET is_active = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, userID)
	return err
}

// DeleteNNTPUser permanently deletes an NNTP user
func (db *Database) DeleteNNTPUser(userID int) error {
	// First delete any sessions
	_, err := retryableExec(db.mainDB, `DELETE FROM nntp_sessions WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete NNTP sessions: %w", err)
	}

	// Then delete the user
	_, err = retryableExec(db.mainDB, `DELETE FROM nntp_users WHERE id = ?`, userID)
	return err
}

// NNTP Session Management Functions

// CreateNNTPSession creates a new NNTP session
func (db *Database) CreateNNTPSession(userID int, connectionID, remoteAddr string) error {
	query := `INSERT INTO nntp_sessions (user_id, connection_id, remote_addr) VALUES (?, ?, ?)`
	_, err := retryableExec(db.mainDB, query, userID, connectionID, remoteAddr)
	return err
}

// UpdateNNTPSessionActivity updates the last activity timestamp
func (db *Database) UpdateNNTPSessionActivity(connectionID string) error {
	query := `UPDATE nntp_sessions SET last_activity = CURRENT_TIMESTAMP WHERE connection_id = ? AND is_active = 1`
	_, err := retryableExec(db.mainDB, query, connectionID)
	return err
}

// CloseNNTPSession marks a session as inactive
func (db *Database) CloseNNTPSession(connectionID string) error {
	query := `UPDATE nntp_sessions SET is_active = 0 WHERE connection_id = ?`
	_, err := retryableExec(db.mainDB, query, connectionID)
	return err
}

// GetActiveNNTPSessionsForUser counts active sessions for a user
func (db *Database) GetActiveNNTPSessionsForUser(userID int) (int, error) {
	query := `SELECT COUNT(*) FROM nntp_sessions WHERE user_id = ? AND is_active = 1`
	var count int
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{userID}, &count)
	return count, err
}

// CleanupOldNNTPSessions removes inactive sessions older than specified duration
func (db *Database) CleanupOldNNTPSessions(olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	query := `DELETE FROM nntp_sessions WHERE is_active = 0 AND last_activity < ?`
	_, err := retryableExec(db.mainDB, query, cutoff)
	return err
}

// Helper function to generate random 16-character alphanumeric string
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// Use crypto/rand for secure random generation
	result := make([]byte, length)
	for i := range result {
		// Generate random index
		randomBytes := make([]byte, 1)
		_, err := rand.Read(randomBytes)
		if err != nil {
			// Fallback to time-based seed if crypto/rand fails
			result[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		} else {
			result[i] = charset[randomBytes[0]%byte(len(charset))]
		}
	}
	return string(result)
}

// CreateNNTPUserForWebUser automatically creates an NNTP user for a web user
func (db *Database) CreateNNTPUserForWebUser(webUserID int64) error {
	// Generate random 16-character username and password
	nntpUsername := generateRandomString(16)
	nntpPassword := generateRandomString(16)

	// Ensure username is unique by checking database
	for {
		_, err := db.GetNNTPUserByUsername(nntpUsername)
		if err != nil {
			// Username doesn't exist, we can use it
			break
		}
		// Username exists, generate a new one
		nntpUsername = generateRandomString(16)
	}

	// Create NNTP user with read-only permissions
	nntpUser := &models.NNTPUser{
		Username:  nntpUsername,
		Password:  nntpPassword,
		MaxConns:  1,     // Default 1 connection
		Posting:   false, // Read-only by default
		WebUserID: webUserID,
		IsActive:  true,
	}

	return db.InsertNNTPUser(nntpUser)
}

// AuthenticateNNTPUser authenticates an NNTP user with caching support
// This function first checks the authentication cache before doing expensive bcrypt verification
func (db *Database) AuthenticateNNTPUser(username, password string) (*models.NNTPUser, error) {
	// First check the authentication cache
	if db.NNTPAuthCache != nil {
		if userID, found := db.NNTPAuthCache.Get(username, password); found {
			// Cache hit - get the user details (this is fast)
			return db.GetNNTPUserByID(userID)
		}
	}

	// Cache miss - do full authentication
	user, err := db.GetNNTPUserByUsername(username)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	// Verify password against stored bcrypt hash
	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password))
	if err != nil {
		return nil, fmt.Errorf("invalid password")
	}

	// Authentication successful - cache it
	if db.NNTPAuthCache != nil {
		db.NNTPAuthCache.Set(user.ID, username, password)
	}

	// Update last login timestamp
	db.UpdateNNTPUserLastLogin(user.ID)

	return user, nil
}

// InvalidateNNTPUserAuth removes a user from the authentication cache
// Call this when changing passwords or deactivating users
func (db *Database) InvalidateNNTPUserAuth(username string) {
	if db.NNTPAuthCache != nil {
		db.NNTPAuthCache.Remove(username)
	}
}

// GetNNTPAuthCacheStats returns authentication cache statistics
func (db *Database) GetNNTPAuthCacheStats() map[string]interface{} {
	if db.NNTPAuthCache != nil {
		return db.NNTPAuthCache.Stats()
	}
	return map[string]interface{}{
		"cache_enabled": false,
	}
}
