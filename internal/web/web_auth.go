package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// Global flash message map and mutex
var (
	flashMessages   = make(map[string]FlashMessage)
	flashMessagesMu sync.RWMutex
)

// SetFlashError sets a temporary error message for a session
func SetFlashError(sessionID, msg string) {
	flashMessagesMu.Lock()
	flashMessages[sessionID] = FlashMessage{Type: "error", Message: msg}
	flashMessagesMu.Unlock()
}

// SetFlashSuccess sets a temporary success message for a session
func SetFlashSuccess(sessionID, msg string) {
	flashMessagesMu.Lock()
	flashMessages[sessionID] = FlashMessage{Type: "success", Message: msg}
	flashMessagesMu.Unlock()
}

// GetAndClearFlash retrieves and clears flash messages for a session
func GetAndClearFlash(sessionID string) (success, errorMsg string) {
	flashMessagesMu.Lock()
	fm := flashMessages[sessionID]
	switch fm.Type {
	case "success":
		success = fm.Message
	case "error":
		errorMsg = fm.Message
	}
	delete(flashMessages, sessionID)
	flashMessagesMu.Unlock()
	return
}

// AuthUser represents a user for authentication
type AuthUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// SessionData represents session information with user data
type SessionData struct {
	SessionID  string
	UserID     int64
	User       *AuthUser
	ExpiresAt  time.Time
	TmpError   string // Temporary error message for rendering
	TmpSuccess string // Temporary success message for rendering
}

// SetError sets a temporary error message in session data
func (s *SessionData) SetError(msg string) {
	SetFlashError(s.SessionID, msg)
}

// SetSuccess sets a temporary success message in session data
func (s *SessionData) SetSuccess(msg string) {
	SetFlashSuccess(s.SessionID, msg)
}

// GetSuccess retrieves and clears the temporary success message
func (s *SessionData) GetSuccess() string {
	succ, _ := GetAndClearFlash(s.SessionID)
	return succ
}

// GetError retrieves and clears the temporary error message
func (s *SessionData) GetError() string {
	_, err := GetAndClearFlash(s.SessionID)
	return err
}

// WebAuthRequired middleware for web authentication (different from API auth)
func (s *WebServer) WebAuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := s.getWebSession(c)
		if session == nil {
			c.Redirect(http.StatusSeeOther, "/login?redirect="+c.Request.URL.Path)
			c.Abort()
			return
		}

		// Store user in context for handlers
		c.Set("user", session.User)
		c.Next()
	}
}

// WebAdminRequired middleware for admin-only routes
func (s *WebServer) WebAdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := s.getWebSession(c)
		if session == nil {
			c.Redirect(http.StatusSeeOther, "/login?redirect="+c.Request.URL.Path)
			c.Abort()
			return
		}

		// Check if user has admin permission
		permissions, err := s.DB.GetUserPermissions(session.UserID)
		if err != nil {
			s.renderError(c, http.StatusInternalServerError, "Database Error", err.Error())
			c.Abort()
			return
		}

		hasAdminPerm := false
		for _, perm := range permissions {
			if perm.Permission == "admin" {
				hasAdminPerm = true
				break
			}
		}

		if !hasAdminPerm {
			s.renderError(c, http.StatusForbidden, "Access Denied", "Admin access required")
			c.Abort()
			return
		}

		c.Set("user", session.User)
		c.Next()
	}
}

// getWebSession retrieves session from cookie and returns full session data
func (s *WebServer) getWebSession(c *gin.Context) *SessionData {
	sessionID, err := c.Cookie("session_id")
	if err != nil {
		return nil
	}

	// Use the new session validation system
	user, err := s.DB.ValidateUserSession(sessionID)
	if err != nil {
		return nil
	}

	authUser := &AuthUser{
		ID:          user.ID,
		Username:    user.Username,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		CreatedAt:   user.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	return &SessionData{
		SessionID: sessionID,
		UserID:    user.ID,
		User:      authUser,
		ExpiresAt: *user.SessionExpiresAt,
	}
}

// createWebSession creates a new session for user
func (s *WebServer) createWebSession(c *gin.Context, userID int64) error {
	// Generate random session ID
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return err
	}
	sessionID := hex.EncodeToString(bytes)

	// Set expiration to 7 days
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	// Store session in database
	session := &models.Session{
		ID:        sessionID,
		UserID:    userID,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}

	err := s.DB.InsertSession(session)
	if err != nil {
		return err
	}

	// Set cookie
	s.setSessionCookie(c, sessionID)
	return nil
}

// hashPassword creates a bcrypt hash of the password
func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// checkPassword checks if password matches hash
func checkPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// validateEmail performs basic email validation
func validateEmail(email string) bool {
	return strings.Contains(email, "@") && strings.Contains(email, ".")
}

// validateUsername validates username requirements
func validateUsername(username string) error {
	if len(username) < 3 {
		return fmt.Errorf("username must be at least 3 characters long")
	}
	if len(username) > 50 {
		return fmt.Errorf("username must be less than 50 characters")
	}
	// Only allow alphanumeric and underscore
	for _, char := range username {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_') {
			return fmt.Errorf("username can only contain letters, numbers, and underscores")
		}
	}
	return nil
}

// validatePassword validates password requirements
func validatePassword(password string) error {
	if len(password) < 6 {
		return fmt.Errorf("password must be at least 6 characters long")
	}
	if len(password) > 128 {
		return fmt.Errorf("password must be less than 128 characters")
	}
	return nil
}

// Helper function to set session cookie
func (s *WebServer) setSessionCookie(c *gin.Context, sessionID string) {
	// Detect HTTPS from the current request perspective only
	// Prefer actual TLS on the request or trusted reverse proxy header
	isHTTPS := c.Request != nil && (c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https"))

	cookie := &http.Cookie{
		Name:     "session_id",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS,
		SameSite: http.SameSiteLaxMode, // Works well with reverse proxies
		MaxAge:   int(7 * 24 * 3600),   // 7 days
	}

	http.SetCookie(c.Writer, cookie)
}

// Helper function to clear session cookie
func (s *WebServer) clearSessionCookie(c *gin.Context) {
	// Detect HTTPS from the current request perspective only
	isHTTPS := c.Request != nil && (c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https"))

	cookie := &http.Cookie{
		Name:     "session_id",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1, // Delete cookie
	}

	http.SetCookie(c.Writer, cookie)
}
