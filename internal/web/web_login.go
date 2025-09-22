package web

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

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
		redirectURL := c.Query("redirect")
		if redirectURL == "" {
			redirectURL = "/"
		}
		c.Redirect(http.StatusSeeOther, redirectURL)
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

	data := LoginPageData{
		TemplateData: s.getBaseTemplateData(c, "Login"),
		Error:        errorMsg,
		RedirectURL:  c.Query("redirect"),
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
	redirectURL := c.PostForm("redirect")

	time.Sleep(2 * time.Second) // stupid brute-force protection

	if redirectURL == "" {
		redirectURL = "/"
	}

	// Validate input
	if username == "" || password == "" {
		s.renderLoginError(c, "Username and password are required", redirectURL)
		return
	}

	// Check if user is locked out
	lockedOut, err := s.DB.IsUserLockedOut(username)
	if err != nil {
		s.renderLoginError(c, "Login error. Please try again.", redirectURL)
		return
	}
	if lockedOut {
		s.renderLoginError(c, "Invalid username/email or password", redirectURL)
		log.Printf("Login locked out for username/email: '%x'", username)
		return
	}

	// Try to find user by username or email
	var user *models.User

	// Check if username contains @ (email login)
	if strings.Contains(username, "@") {
		// Try to get user by email
		user, err = s.DB.GetUserByEmail(username)
		if err != nil {
			s.DB.IncrementLoginAttempts(username)
			s.renderLoginError(c, "Invalid username/email or password", redirectURL)
			log.Printf("Login failed for email: '%x' err='%v'", username, err)
			return
		}
	} else {
		// Get user by username
		user, err = s.DB.GetUserByUsername(username)
		if err != nil {
			s.DB.IncrementLoginAttempts(username)
			s.renderLoginError(c, "Invalid username/email or password", redirectURL)
			log.Printf("Login failed for username: '%x' err='%v'", username, err)
			return
		}
	}

	// Check password
	if !checkPassword(password, user.PasswordHash) {
		s.DB.IncrementLoginAttempts(username)
		s.renderLoginError(c, "Invalid username/email or password", redirectURL)
		log.Printf("Login failed for username: '%x' err='%v'", username, err)
		return
	}

	// Successful login - create new session (this invalidates any existing session)
	sessionID, err := s.DB.CreateUserSession(user.ID, c.ClientIP())
	if err != nil {
		s.renderLoginError(c, "Failed to create session", redirectURL)
		log.Printf("Failed to create session for user: '%s' err='%v'", username, err)
		return
	}

	// Set secure session cookie
	s.setSessionCookie(c, sessionID)

	// Redirect to destination
	c.Redirect(http.StatusSeeOther, redirectURL)
}

// logout handles user logout
func (s *WebServer) logout(c *gin.Context) {
	// Get current session to invalidate it
	session := s.getWebSession(c)
	if session != nil {
		s.DB.InvalidateUserSession(session.UserID)
	}

	// Clear session cookie
	s.clearSessionCookie(c)

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
