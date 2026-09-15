package web

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// loginDelay slows down every login attempt (simple brute-force protection).
// Tests set it to 0.
var loginDelay = 2 * time.Second

// LoginPageData represents data for login page
type LoginPageData struct {
	TemplateData
	Error       string
	RedirectURL string
}

// loginPage displays the login form
func (s *WebServer) loginPage(c *gin.Context) {
	// Check if user is already logged in
	if user, exists := c.Get("user"); exists && user != nil {
		c.Redirect(http.StatusSeeOther, safeRedirect(c.Query("redirect")))
		return
	}

	// Handle different message types
	var errorMsg string
	message := c.Query("message")
	switch message {
	case "session_expired":
		errorMsg = "⚠️ Your session has expired. Another session has logged in."
	case "logged_out":
		errorMsg = "" // No error for normal logout
	}

	redirectURL := ""
	if r := c.Query("redirect"); r != "" {
		redirectURL = safeRedirect(r)
	}
	data := LoginPageData{
		TemplateData: s.getBaseTemplateData(c, "Login"),
		Error:        errorMsg,
		RedirectURL:  redirectURL,
	}

	// Load template individually
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/login.html"))
	c.Header("Content-Type", "text/html")
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template Error", err.Error())
	}
}

// loginSubmit processes login form submission
func (s *WebServer) loginSubmit(c *gin.Context) {
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	redirectURL := safeRedirect(c.PostForm("redirect"))

	time.Sleep(loginDelay) // stupid brute-force protection

	// Validate input
	if username == "" || password == "" {
		s.renderLoginError(c, "Username and password are required", redirectURL)
		return
	}

	// Find the user by email (contains @) or username
	var user *models.User
	var err error
	if strings.Contains(username, "@") {
		user, err = s.DB.GetUserByEmail(username)
	} else {
		user, err = s.DB.GetUserByUsername(username)
	}
	// Every failure below shows the same message, and every rejection before the real
	// password check spends the same bcrypt time, so the response does not reveal whether
	// a username or email exists, is locked out or disabled.
	if err != nil || user == nil {
		dummyPasswordCompare(password)
		s.renderLoginError(c, "Invalid username/email or password", redirectURL)
		log.Printf("[WEB]: Login failed for username/email: '%x' err='%v'", username, err)
		return
	}

	// Count this attempt atomically before checking the password (lockout by user ID)
	allowed, err := s.DB.ReserveLoginAttemptByID(user.ID)
	if err != nil {
		s.renderLoginError(c, "Login error. Please try again.", redirectURL)
		log.Printf("[WEB]: Login attempt reservation failed for user %d: %v", user.ID, err)
		return
	}
	if !allowed {
		dummyPasswordCompare(password)
		s.renderLoginError(c, "Invalid username/email or password", redirectURL)
		log.Printf("[WEB]: Login locked out for username/email: '%x'", username)
		return
	}

	// Check password (the attempt is already counted)
	if !checkPassword(password, user.PasswordHash) {
		s.renderLoginError(c, "Invalid username/email or password", redirectURL)
		log.Printf("[WEB]: Login failed for username/email: '%x' (wrong password)", username)
		return
	}

	// Disabled users cannot log in
	if user.Disabled > 0 {
		s.renderLoginError(c, "Invalid username/email or password", redirectURL)
		log.Printf("[WEB]: Login rejected for disabled user %d: '%x'", user.ID, username)
		return
	}

	// Successful login - create new session (this invalidates any existing session)
	sessionID, err := s.DB.CreateUserSession(user.ID, c.ClientIP())
	if err != nil {
		s.renderLoginError(c, "Failed to create session", redirectURL)
		log.Printf("[WEB]: Failed to create session for user: '%x' err='%v'", username, err)
		return
	}

	// Set secure session cookie
	s.setSessionCookie(c, sessionID)
	s.clearRequestSession(c)

	// Redirect to destination
	c.Redirect(http.StatusSeeOther, redirectURL)
}

// dummyPasswordCompare spends the bcrypt time of a real password check, for login
// rejections that happen before the user's own hash is compared.
func dummyPasswordCompare(password string) {
	dummyPasswordHash.once.Do(func() {
		dummyPasswordHash.hash, _ = bcrypt.GenerateFromPassword([]byte("pugleaf-dummy-password"), bcrypt.DefaultCost)
	})
	_ = bcrypt.CompareHashAndPassword(dummyPasswordHash.hash, []byte(password))
}

// logout handles user logout
func (s *WebServer) logout(c *gin.Context) {
	// GET /logout must not be triggerable by other sites (CSRF): ignore cross-site requests
	switch strings.ToLower(c.GetHeader("Sec-Fetch-Site")) {
	case "cross-site", "same-site":
		c.Redirect(http.StatusSeeOther, "/")
		return
	}

	// Get current session to invalidate it
	session := s.getWebSession(c)
	if session != nil {
		if err := s.DB.InvalidateUserSession(session.UserID); err != nil {
			log.Printf("[WEB]: Failed to invalidate session of user %d: %v", session.UserID, err)
		}
	}

	// Clear session cookie
	s.clearSessionCookie(c)
	s.clearRequestSession(c)

	c.Redirect(http.StatusSeeOther, "/login?message=logged_out")
}

// renderLoginError renders login page with error
func (s *WebServer) renderLoginError(c *gin.Context, errorMsg, redirectURL string) {
	data := LoginPageData{
		TemplateData: s.getBaseTemplateData(c, "Login"),
		Error:        errorMsg,
		RedirectURL:  redirectURL,
	}

	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/login.html"))
	c.Header("Content-Type", "text/html")
	c.Status(http.StatusBadRequest)
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template Error", err.Error())
	}
}
