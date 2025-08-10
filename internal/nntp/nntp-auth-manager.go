package nntp

import (
	"fmt"
	"log"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// AuthManager handles NNTP authentication
type AuthManager struct {
	db *database.Database
}

// NewAuthManager creates a new authentication manager
func NewAuthManager(db *database.Database) *AuthManager {
	return &AuthManager{
		db: db,
	}
}

// AuthenticateUser authenticates a user with username and password
func (am *AuthManager) AuthenticateUser(username, password string) (*models.NNTPUser, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("username and password are required")
	}

	// Use the cached authentication function with bcrypt verification
	user, err := am.db.AuthenticateNNTPUser(username, password)
	if err != nil {
		log.Printf("NNTP authentication failed for user %s: %v", username, err)
		return nil, fmt.Errorf("authentication failed")
	}

	// Update last login timestamp
	if err := am.db.UpdateNNTPUserLastLogin(user.ID); err != nil {
		log.Printf("Failed to update last login for NNTP user %s: %v", username, err)
		// Don't fail authentication for this
	}

	log.Printf("NNTP user %s authenticated successfully", username)
	return user, nil
}

// CheckGroupAccess checks if a user has access to a specific newsgroup
func (am *AuthManager) CheckGroupAccess(user *models.NNTPUser, groupName string) bool {
	if user == nil {
		return false
	}

	// For now, all authenticated NNTP users have read access to all groups
	// This can be extended with group-specific permissions later
	return true
}

// CanPost checks if a user has posting privileges
func (am *AuthManager) CanPost(user *models.NNTPUser) bool {
	if user == nil {
		return false
	}
	return user.Posting
}

// IsAdmin checks if a user has admin privileges
func (am *AuthManager) IsAdmin(user *models.NNTPUser) bool {
	if user == nil {
		return false
	}

	// For NNTP users, admin privileges could be determined by username or separate field
	// For now, no NNTP users have admin privileges
	return false
}

// CheckConnectionLimit checks if user can create another connection
func (am *AuthManager) CheckConnectionLimit(user *models.NNTPUser) bool {
	if user == nil {
		return false
	}

	// Get current active sessions for this user
	activeSessions, err := am.db.GetActiveNNTPSessionsForUser(user.ID)
	if err != nil {
		log.Printf("Failed to check connection limit for user %s: %v", user.Username, err)
		return false
	}

	return activeSessions < user.MaxConns
}
