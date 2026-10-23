package database

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"
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

	// web_user_id 0 means "no web user": store NULL, the column is a foreign key to users(id)
	query := `INSERT INTO nntp_users (username, password, maxconns, posting, web_user_id, is_active)
	          VALUES (?, ?, ?, ?, NULLIF(?, 0), ?)`
	_, err = RetryableExec(db.mainDB, query, u.Username, string(hashedPassword), u.MaxConns, u.Posting, u.WebUserID, u.IsActive)
	return err
}

// GetNNTPUserByUsername retrieves an NNTP user by username
func (db *Database) GetNNTPUserByUsername(username string) (*models.NNTPUser, error) {
	query := `SELECT id, username, password, maxconns, posting, COALESCE(web_user_id, 0), created_at, updated_at, last_login, is_active
	          FROM nntp_users WHERE username = ? AND is_active = 1`

	var u models.NNTPUser
	err := RetryableQueryRowScan(db.mainDB, query, []interface{}{username}, &u.ID, &u.Username, &u.Password, &u.MaxConns, &u.Posting, &u.WebUserID,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.IsActive)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetNNTPUserByID retrieves an NNTP user by ID
func (db *Database) GetNNTPUserByID(id int) (*models.NNTPUser, error) {
	query := `SELECT id, username, password, maxconns, posting, COALESCE(web_user_id, 0), created_at, updated_at, last_login, is_active
	          FROM nntp_users WHERE id = ?`

	var u models.NNTPUser
	err := RetryableQueryRowScan(db.mainDB, query, []interface{}{id}, &u.ID, &u.Username, &u.Password, &u.MaxConns, &u.Posting, &u.WebUserID,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.IsActive)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetNNTPUserByWebUserID retrieves the active NNTP user mapped to a web user ID. Several
// rows may share one mapping while inactive (migration 0031 allows one ACTIVE account per web user): the one with the lowest id wins.
// webUserID <= 0 means "no web user" and returns sql.ErrNoRows.
func (db *Database) GetNNTPUserByWebUserID(webUserID int64) (*models.NNTPUser, error) {
	if webUserID <= 0 {
		return nil, sql.ErrNoRows
	}
	query := `SELECT id, username, password, maxconns, posting, COALESCE(web_user_id, 0), created_at, updated_at, last_login, is_active
	          FROM nntp_users WHERE web_user_id = ? AND is_active = 1 ORDER BY id ASC LIMIT 1`

	var u models.NNTPUser
	err := RetryableQueryRowScan(db.mainDB, query, []interface{}{webUserID}, &u.ID, &u.Username, &u.Password, &u.MaxConns, &u.Posting, &u.WebUserID,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.IsActive)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// SearchNNTPUsers searches for NNTP users by username with a limit
func (db *Database) SearchNNTPUsers(searchTerm string, limit int) ([]*models.NNTPUser, error) {
	query := `SELECT id, username, password, maxconns, posting, COALESCE(web_user_id, 0), created_at, updated_at, last_login, is_active
	          FROM nntp_users
	          WHERE username LIKE ?
	          ORDER BY username
	          LIMIT ?`

	searchPattern := "%" + searchTerm + "%"
	rows, err := RetryableQuery(db.mainDB, query, searchPattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nntpUsers []*models.NNTPUser
	for rows.Next() {
		var u models.NNTPUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Password, &u.MaxConns, &u.Posting, &u.WebUserID, &u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.IsActive); err != nil {
			return nil, err
		}
		nntpUsers = append(nntpUsers, &u)
	}
	return nntpUsers, nil
}

// GetAllNNTPUsers retrieves all NNTP users
func (db *Database) GetAllNNTPUsers() ([]*models.NNTPUser, error) {
	query := `SELECT id, username, password, maxconns, posting, COALESCE(web_user_id, 0), created_at, updated_at, last_login, is_active
	          FROM nntp_users ORDER BY username`
	rows, err := RetryableQuery(db.mainDB, query)
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
	if _, err = RetryableExec(db.mainDB, query, string(hashedPassword), userID); err != nil {
		return err
	}
	// Drop cached logins of this process; other processes notice the new hash on their next hit.
	db.NNTPAuthCache.RemoveUserID(userID)
	return nil
}

// UpdateNNTPUserLastLogin updates the last login timestamp
func (db *Database) UpdateNNTPUserLastLogin(userID int) error {
	query := `UPDATE nntp_users SET last_login = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := RetryableExec(db.mainDB, query, userID)
	return err
}

// UpdateNNTPUserPermissions updates maxconns and posting permissions
func (db *Database) UpdateNNTPUserPermissions(userID int, maxConns int, posting bool) error {
	query := `UPDATE nntp_users SET maxconns = ?, posting = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := RetryableExec(db.mainDB, query, maxConns, posting, userID)
	return err
}

// DeactivateNNTPUser deactivates an NNTP user (soft delete)
func (db *Database) DeactivateNNTPUser(userID int) error {
	query := `UPDATE nntp_users SET is_active = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	if _, err := RetryableExec(db.mainDB, query, userID); err != nil {
		return err
	}
	db.NNTPAuthCache.RemoveUserID(userID)
	return nil
}

// ActivateNNTPUser activates an NNTP user (reverses soft delete)
func (db *Database) ActivateNNTPUser(userID int) error {
	query := `UPDATE nntp_users SET is_active = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := RetryableExec(db.mainDB, query, userID)
	return err
}

// DeleteNNTPUser permanently deletes an NNTP user
func (db *Database) DeleteNNTPUser(userID int) error {
	// First delete any sessions
	_, err := RetryableExec(db.mainDB, `DELETE FROM nntp_sessions WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete NNTP sessions: %w", err)
	}

	// Then delete the user
	if _, err = RetryableExec(db.mainDB, `DELETE FROM nntp_users WHERE id = ?`, userID); err != nil {
		return err
	}
	db.NNTPAuthCache.RemoveUserID(userID)
	return nil
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

	// Ensure username is unique by checking database (any state: an inactive
	// user holds its name too, and the column is UNIQUE)
	for {
		_, err := db.GetNNTPUserByUsernameAnyState(nntpUsername)
		if errors.Is(err, sql.ErrNoRows) {
			// Username doesn't exist, we can use it
			break
		}
		if err != nil {
			return fmt.Errorf("checking NNTP username: %w", err)
		}
		// Username exists, generate a new one
		nntpUsername = generateRandomString(16)
	}

	// Create NNTP user with read-only permissions
	nntpUser := &models.NNTPUser{
		Username:  nntpUsername,
		Password:  nntpPassword,
		MaxConns:  DefaultNNTPUserMaxConns,
		Posting:   false, // Read-only by default
		WebUserID: webUserID,
		IsActive:  true,
	}

	return db.InsertNNTPUser(nntpUser)
}

// nntpDummyOnce holds a bcrypt hash of a fixed string, generated once on first use.
var nntpDummyOnce struct {
	sync.Once
	hash []byte
}

// nntpDummyHash returns a bcrypt hash (bcrypt.DefaultCost) that no password is
// expected to match. Comparing against it for unknown users makes a failed login
// take about as long as one for an existing user (A-nntp-sec-6: no username
// enumeration by timing).
func nntpDummyHash() []byte {
	nntpDummyOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte("go-pugleaf-nntp-dummy-password"), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("[NNTP-AUTH] dummy hash generation failed: %v", err)
			return
		}
		nntpDummyOnce.hash = h
	})
	return nntpDummyOnce.hash
}

// AuthenticateNNTPUser authenticates an NNTP user with caching support
// This function first checks the authentication cache before doing expensive bcrypt verification
func (db *Database) AuthenticateNNTPUser(username, password string) (*models.NNTPUser, error) {
	// First check the authentication cache
	if db.NNTPAuthCache != nil {
		if userID, storedHash, found := db.NNTPAuthCache.GetEntry(username, password); found {
			// Cache hit - get the user details (this is fast)
			user, err := db.GetNNTPUserByID(userID)
			if err != nil {
				// The row is gone (deleted user) or unreadable: drop the cached
				// credentials, so only logins that were allowed stay cached.
				db.InvalidateNNTPUserAuth(username)
				return nil, err
			}
			if user.Password == storedHash {
				// A cached login must not outlive the account: re-check on every hit.
				if err := db.refuseDisabledNNTPUser(username, user.ID); err != nil {
					return nil, err
				}
				return user, nil
			}
			// The password was changed since this login was cached (possibly by
			// another process, SEC-6): drop the entry and do the full check below,
			// so an old password fails and a new one is cached again.
			db.InvalidateNNTPUserAuth(username)
		}
	}

	// Cache miss - do full authentication
	user, err := db.GetNNTPUserByUsername(username)
	if err != nil {
		// Spend the same bcrypt time as for an existing user (A-nntp-sec-6).
		_ = bcrypt.CompareHashAndPassword(nntpDummyHash(), []byte(password))
		return nil, fmt.Errorf("user not found: %w", err)
	}

	// Verify password against stored bcrypt hash
	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password))
	if err != nil {
		return nil, fmt.Errorf("invalid password")
	}

	// The password is right: refuse (and do not cache) an inactive NNTP account or one
	// whose linked web user is disabled.
	if err := db.refuseDisabledNNTPUser(username, user.ID); err != nil {
		return nil, err
	}

	// Authentication successful - cache it with the stored hash it was verified against
	if db.NNTPAuthCache != nil {
		db.NNTPAuthCache.Set(user.ID, username, password, user.Password)
	}

	// Update last login timestamp
	if err := db.UpdateNNTPUserLastLogin(user.ID); err != nil {
		log.Printf("[NNTP-AUTH] last login update for user %d: %v", user.ID, err)
	}

	return user, nil
}

// query_nntpUserAuthState reads the two flags that decide whether an NNTP login is
// allowed: the NNTP account's own is_active and the disabled flag of the linked web
// user, if there is one.
const query_nntpUserAuthState = `SELECT n.is_active, COALESCE(u.disabled, 0) FROM nntp_users n LEFT JOIN users u ON u.id = n.web_user_id WHERE n.id = ?`

// refuseDisabledNNTPUser returns an error when the NNTP user is inactive or its linked
// web user is disabled, and drops the cached credentials so a refused login does not
// stay usable through the authentication cache. A read error fails closed.
func (db *Database) refuseDisabledNNTPUser(username string, userID int) error {
	var isActive, disabled int
	err := RetryableQueryRowScan(db.mainDB, query_nntpUserAuthState, []interface{}{userID}, &isActive, &disabled)
	if err != nil {
		db.InvalidateNNTPUserAuth(username)
		return fmt.Errorf("failed to check user state: %w", err)
	}
	if isActive == 0 || disabled != 0 {
		db.InvalidateNNTPUserAuth(username)
		return fmt.Errorf("user disabled")
	}
	return nil
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
