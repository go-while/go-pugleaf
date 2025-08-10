package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// RegisterPageData represents data for register page
type RegisterPageData struct {
	TemplateData
	Error    string
	Username string
	Email    string
}

// registerPage displays the registration form
func (s *WebServer) registerPage(c *gin.Context) {
	// Check if registration is enabled
	registrationEnabled, err := s.DB.IsRegistrationEnabled()
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Database Error", err.Error())
		return
	}
	if !registrationEnabled {
		s.renderError(c, http.StatusForbidden, "Registration Disabled", "New user registration is currently disabled.")
		return
	}

	// Check if user is already logged in
	if session := s.getWebSession(c); session != nil {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}

	data := RegisterPageData{
		TemplateData: s.getBaseTemplateData(c, "Register"),
	}

	// Load template individually
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/register.html"))
	c.Header("Content-Type", "text/html")
	if err := tmpl.ExecuteTemplate(c.Writer, "base.html", data); err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template Error", err.Error())
	}
}

// registerSubmit processes registration form submission
func (s *WebServer) registerSubmit(c *gin.Context) {
	// Check if registration is enabled
	registrationEnabled, err := s.DB.IsRegistrationEnabled()
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Database Error", err.Error())
		return
	}
	if !registrationEnabled {
		s.renderError(c, http.StatusForbidden, "Registration Disabled", "New user registration is currently disabled.")
		return
	}

	username := strings.TrimSpace(c.PostForm("username"))
	email := strings.TrimSpace(c.PostForm("email"))
	password1 := c.PostForm("password1")
	password2 := c.PostForm("password2")

	// Validate input
	if username == "" || email == "" || password1 == "" || password2 == "" {
		s.renderRegisterError(c, "All fields are required", username, email)
		return
	}

	// Validate passwords match
	if password1 != password2 {
		s.renderRegisterError(c, "Passwords do not match", username, email)
		return
	}

	// Validate username
	if err := validateUsername(username); err != nil {
		s.renderRegisterError(c, err.Error(), username, email)
		return
	}

	// Validate password
	if err := validatePassword(password1); err != nil {
		s.renderRegisterError(c, err.Error(), username, email)
		return
	}

	// Validate email
	if !validateEmail(email) {
		s.renderRegisterError(c, "Invalid email format", username, email)
		return
	}

	// Check if username already exists
	existingUser, err := s.DB.GetUserByUsername(username)
	if err == nil && existingUser != nil {
		s.renderRegisterError(c, "Username already exists", username, email)
		return
	}

	// Check if email already exists
	existingUser, err = s.DB.GetUserByEmail(email)
	if err == nil && existingUser != nil {
		s.renderRegisterError(c, "Email already exists", username, email)
		return
	}

	// Hash password
	passwordHash, err := hashPassword(password1)
	if err != nil {
		s.renderRegisterError(c, "Failed to process password", username, email)
		return
	}

	// Create user
	user, err := s.createUser(username, email, passwordHash, username)
	if err != nil {
		fmt.Printf("ERROR: Failed to create user %s: %v\n", username, err)
		s.renderRegisterError(c, "Failed to create user: "+err.Error(), username, email)
		return
	}
	fmt.Printf("INFO: Successfully created user %s with ID %d\n", user.Username, user.ID)

	// Create session
	err = s.createWebSession(c, int64(user.ID))
	if err != nil {
		// Log the actual error for debugging
		fmt.Printf("ERROR: Failed to create web session for user %s (ID: %d): %v\n", user.Username, user.ID, err)
		s.renderRegisterError(c, "Registration successful but failed to log in: "+err.Error(), username, email)
		return
	}

	// Redirect to home
	c.Redirect(http.StatusSeeOther, "/")
}

// createUser creates a new user
func (s *WebServer) createUser(username, email, passwordHash, displayName string) (*models.User, error) {
	user := &models.User{
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		DisplayName:  displayName,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err := s.DB.InsertUser(user)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	// Get the created user to obtain the ID
	createdUser, err := s.DB.GetUserByUsername(username)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve created user: %w", err)
	}

	// Automatically create an NNTP user for this web user
	err = s.DB.CreateNNTPUserForWebUser(int64(createdUser.ID))
	if err != nil {
		// Log the error but don't fail the registration
		fmt.Printf("Warning: Failed to create NNTP user for web user %s (ID: %d): %v\n",
			createdUser.Username, createdUser.ID, err)
	}

	return createdUser, nil
}

// renderRegisterError renders register page with error
func (s *WebServer) renderRegisterError(c *gin.Context, errorMsg, username, email string) {
	data := RegisterPageData{
		TemplateData: s.getBaseTemplateData(c, "Register"),
		Error:        errorMsg,
		Username:     username,
		Email:        email,
	}

	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/register.html"))
	c.Header("Content-Type", "text/html")
	c.Status(http.StatusBadRequest)
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template Error", err.Error())
	}
}
