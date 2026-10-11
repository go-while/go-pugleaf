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
		log.Printf("[NNTP-AUTH] authentication failed for user %q: %v", username, err)
		return nil, fmt.Errorf("authentication failed")
	}

	// Update last login timestamp
	if err := am.db.UpdateNNTPUserLastLogin(user.ID); err != nil {
		log.Printf("[NNTP-AUTH] Failed to update last login for user %s: %v", username, err)
		// Don't fail authentication for this
	}

	log.Printf("[NNTP-AUTH] user %s authenticated successfully", username)
	return user, nil
}
