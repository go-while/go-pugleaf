package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
	"golang.org/x/crypto/bcrypt"
)

// Global flash message map and mutex
var (
	flashMessages   = make(map[string]map[string]string)
	flashSetAt      = make(map[string]time.Time) // last Set per session, for pruning
	flashLastPrune  time.Time                    // last prune scan, throttled to flashPruneEvery
	flashMessagesMu sync.RWMutex
)

const (
	flashMaxSessions = 1000             // prune once more sessions than this hold messages
	flashMaxAge      = 15 * time.Minute // messages older than this are pruned
	flashPruneEvery  = time.Minute      // at most one prune scan per interval
)

// setFlashLocked stores one flash message; the caller holds flashMessagesMu.
// Messages that are never read (for example after a redirect the client did not follow)
// would otherwise stay in the map forever, so old entries are pruned when it grows.
func setFlashLocked(sessionID, mtype, msg string) {
	now := time.Now()
	if len(flashMessages) > flashMaxSessions && now.Sub(flashLastPrune) >= flashPruneEvery {
		flashLastPrune = now
		for id, at := range flashSetAt {
			if now.Sub(at) > flashMaxAge {
				delete(flashMessages, id)
				delete(flashSetAt, id)
			}
		}
		for id := range flashMessages {
			if _, ok := flashSetAt[id]; !ok {
				delete(flashMessages, id)
			}
		}
	}
	if flashMessages[sessionID] == nil {
		flashMessages[sessionID] = make(map[string]string)
	}
	flashMessages[sessionID][mtype] = msg
	flashSetAt[sessionID] = now
}

// SetFlashError sets a temporary error message for a session
func SetFlashError(sessionID, msg string) {
	flashMessagesMu.Lock()
	setFlashLocked(sessionID, "error", msg)
	flashMessagesMu.Unlock()
}

// SetFlashSuccess sets a temporary success message for a session
func SetFlashSuccess(sessionID, msg string) {
	flashMessagesMu.Lock()
	setFlashLocked(sessionID, "success", msg)
	flashMessagesMu.Unlock()
}

// GetAndClearFlash retrieves and clears flash messages for a session
func GetAndClearFlash(sessionID string, mtype string) (success, errorMsg string) {
	flashMessagesMu.Lock()
	switch mtype {
	case "success":
		success = flashMessages[sessionID]["success"]
		delete(flashMessages[sessionID], "success")
	case "error":
		errorMsg = flashMessages[sessionID]["error"]
		delete(flashMessages[sessionID], "error")
	}
	if len(flashMessages[sessionID]) == 0 {
		delete(flashMessages, sessionID)
		delete(flashSetAt, sessionID)
	}
	if len(flashMessages) == 0 {
		flashMessages = make(map[string]map[string]string)
		flashSetAt = make(map[string]time.Time)
	}
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
	succ, _ := GetAndClearFlash(s.SessionID, "success")
	return succ
}

// GetError retrieves and clears the temporary error message
func (s *SessionData) GetError() string {
	_, err := GetAndClearFlash(s.SessionID, "error")
	return err
}

// WebAuthRequired middleware for web authentication (different from API auth)
func (s *WebServer) WebAuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := s.getWebSession(c)
		if session == nil {
			c.Redirect(http.StatusSeeOther, "/login?redirect="+url.QueryEscape(c.Request.URL.RequestURI()))
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
			c.Redirect(http.StatusSeeOther, "/login?redirect="+url.QueryEscape(c.Request.URL.RequestURI()))
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

// Gin context keys of the per-request auth cache.
const (
	ctxKeyWebSessionChecked = "pugleaf.webSessionChecked" // bool: getWebSession already ran
	ctxKeyWebSession        = "pugleaf.webSession"        // *SessionData or nil
	ctxKeyIsAdmin           = "pugleaf.isAdmin"           // bool: memoized isAdminRequest
)

// getWebSession retrieves session from cookie and returns full session data.
// The result (including "not logged in") is memoized in the gin context, so helpers
// called several times per request validate the session only once.
func (s *WebServer) getWebSession(c *gin.Context) *SessionData {
	if v, ok := c.Get(ctxKeyWebSessionChecked); ok {
		if checked, _ := v.(bool); checked {
			sd, _ := c.MustGet(ctxKeyWebSession).(*SessionData)
			return sd
		}
	}
	session := s.lookupWebSession(c)
	c.Set(ctxKeyWebSession, session)
	c.Set(ctxKeyWebSessionChecked, true)
	return session
}

// lookupWebSession validates the session cookie against the database.
func (s *WebServer) lookupWebSession(c *gin.Context) *SessionData {
	sessionID, err := c.Cookie("session_id")
	if err != nil {
		return nil
	}

	// Use the new session validation system
	user, err := s.DB.ValidateUserSession(sessionID)
	if err != nil {
		return nil
	}

	// Refresh cookie to keep client-side max age in sync with sliding server timeout
	s.setSessionCookie(c, sessionID)

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

// clearRequestSession drops the memoized session and admin flag of this request
// (after login or logout changed the session).
func (s *WebServer) clearRequestSession(c *gin.Context) {
	c.Set(ctxKeyWebSessionChecked, false)
	c.Set(ctxKeyWebSession, (*SessionData)(nil))
	c.Set(ctxKeyIsAdmin, nil)
}

// isAdminRequest reports whether the logged-in user of this request is an admin.
// The result is memoized in the gin context.
func (s *WebServer) isAdminRequest(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeyIsAdmin); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	admin := false
	if session := s.getWebSession(c); session != nil {
		if user, err := s.DB.GetUserByID(session.UserID); err == nil {
			admin = s.isAdmin(user)
		}
	}
	c.Set(ctxKeyIsAdmin, admin)
	return admin
}

// safeRedirect returns u when it is a local path on this site, otherwise "/".
// It rejects absolute and scheme-relative URLs ("//host", "/\host") and header-breaking input.
func safeRedirect(u string) string {
	if u == "" || len(u) > 2048 || strings.ContainsAny(u, "\r\n\\") {
		return "/"
	}
	if !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") {
		return "/"
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return "/"
	}
	return u
}

// validateDisplayName checks a user-supplied display name: at most 64 runes, no control
// characters and none of < > " (it ends up in the From: header of web posts). Empty is allowed.
func validateDisplayName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("display name contains invalid characters")
	}
	if utf8.RuneCountInString(name) > 64 {
		return fmt.Errorf("display name must be at most 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '<' || r == '>' || r == '"' {
			return fmt.Errorf("display name contains invalid characters")
		}
	}
	return nil
}

// dummyPasswordHash is compared against when a login names an unknown user, so that
// the response time does not reveal whether the user exists.
var dummyPasswordHash struct {
	once sync.Once
	hash []byte
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
	if len(password) < 12 {
		return fmt.Errorf("password must be at least 12 characters long")
	}
	if len(password) > 72 {
		// bcrypt rejects longer passwords (ErrPasswordTooLong)
		return fmt.Errorf("password must be at most 72 bytes")
	}
	return nil
}

// Helper function to set session cookie
func (s *WebServer) setSessionCookie(c *gin.Context, sessionID string) {
	// Detect HTTPS from the current request perspective only
	// Prefer actual TLS on the request or trusted reverse proxy header
	// Gin guarantees c.Request is non-nil for handlers
	if v, exists := c.Get("is_https"); exists {
		if b, ok := v.(bool); ok {
			// Use scheme determined by ReverseProxyMiddleware
			isHTTPS := b
			cookie := &http.Cookie{
				Name:     "session_id",
				Value:    sessionID,
				Path:     "/",
				HttpOnly: true,
				Secure:   isHTTPS,
				SameSite: http.SameSiteLaxMode,                   // Works well with reverse proxies
				MaxAge:   int(database.SessionTimeout.Seconds()), // align with server-side sliding timeout
			}
			http.SetCookie(c.Writer, cookie)
			return
		}
	}
	// Fallback if middleware wasn't applied
	isHTTPS := c.Request.TLS != nil

	cookie := &http.Cookie{
		Name:     "session_id",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS,
		SameSite: http.SameSiteLaxMode,                   // Works well with reverse proxies
		MaxAge:   int(database.SessionTimeout.Seconds()), // align with server-side sliding timeout
	}

	http.SetCookie(c.Writer, cookie)
}

// Helper function to clear session cookie
func (s *WebServer) clearSessionCookie(c *gin.Context) {
	// Detect HTTPS from the current request perspective only
	// Gin guarantees c.Request is non-nil for handlers
	isHTTPS := c.Request.TLS != nil
	if v, exists := c.Get("is_https"); exists {
		if b, ok := v.(bool); ok {
			isHTTPS = b
		}
	}

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
