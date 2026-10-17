package web

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// NNTP User Management Functions

// Limits of the admin NNTP user form, aligned with cmd/nntpmgr (D9 of audit-delta-fixes).
const (
	nntpAdminPasswordMin = 10 // bytes
	nntpAdminPasswordMax = 72 // bytes; bcrypt ignores everything after byte 72
)

// resolveNNTPWebMapping turns the web_user_id form value into the web user an NNTP account
// is mapped to. Empty or "0" means no mapping and returns 0 (stored as NULL). Otherwise the
// web user must exist and, unless it is the account's current mapping (currentWebUserID),
// must not own an NNTP account yet: a web user owns at most one. currentNNTPUserID is the
// account being edited (0 on create). The returned error text is shown to the admin.
// A current mapping whose web user no longer exists (a legacy row: the user was deleted) is
// treated as "no mapping" (0) with a notice for the admin instead of blocking the update.
func resolveNNTPWebMapping(s *WebServer, raw string, currentNNTPUserID int, currentWebUserID int64) (id int64, notice string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return 0, "", nil
	}
	id, err = strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", errors.New("invalid web user ID")
	}
	if _, err := s.DB.GetUserByID(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if currentWebUserID > 0 && id == currentWebUserID {
				return 0, fmt.Sprintf("the mapped web user %d no longer exists, the mapping was removed", id), nil
			}
			return 0, "", errors.New("web user not found")
		}
		log.Printf("[WEB]: NNTP user %d: looking up web user %d: %v", currentNNTPUserID, id, err)
		return 0, "", errors.New("failed to look up web user")
	}
	if id == currentWebUserID {
		return id, "", nil
	}
	n, err := s.DB.CountNNTPUsersByWebUserID(id)
	if err != nil {
		log.Printf("[WEB]: NNTP user %d: counting NNTP accounts of web user %d: %v", currentNNTPUserID, id, err)
		return 0, "", errors.New("failed to check the NNTP accounts of the web user")
	}
	if n > 0 {
		return 0, "", errors.New("web user already has an NNTP account")
	}
	return id, "", nil
}

// generateRandomHex generates a random hexadecimal string of the specified length
func generateRandomHex(length int) (string, error) {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// countActiveNNTPUsers counts the number of active NNTP users
func (s *WebServer) countActiveNNTPUsers(nntpUsers []*models.NNTPUser) (count int64) {
	for _, user := range nntpUsers {
		if user.IsActive {
			count++
		}
	}
	return
}

// countPostingNNTPUsers counts the number of NNTP users who can post
func (s *WebServer) countPostingNNTPUsers(nntpUsers []*models.NNTPUser) (count int64) {
	for _, user := range nntpUsers {
		if user.IsActive && user.Posting {
			count++
		}
	}
	return
}

// adminCreateNNTPUser handles NNTP user creation
func (s *WebServer) adminCreateNNTPUser(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Generate random credentials
	username, err := generateRandomHex(16)
	if err != nil {
		log.Printf("Error generating random username: %v", err)
		session.SetError("Failed to generate random username")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	password, err := generateRandomHex(16)
	if err != nil {
		log.Printf("Error generating random password: %v", err)
		session.SetError("Failed to generate random password")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Get form data (excluding username and password which are now generated)
	maxConnsStr := strings.TrimSpace(c.PostForm("maxconns"))
	postingStr := c.PostForm("posting")
	webUserIDStr := strings.TrimSpace(c.PostForm("web_user_id"))

	// Parse max connections
	maxConns := 1
	if maxConnsStr != "" {
		if parsed, err := strconv.Atoi(maxConnsStr); err == nil && parsed >= 1 && parsed <= 25 {
			maxConns = parsed
		} else {
			session.SetError("Max connections must be between 1 and 25")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
	}

	// Parse posting permission
	posting := postingStr == "on" || postingStr == "true"

	// Web user mapping: empty or 0 = none (stored as NULL)
	webUserID, _, err := resolveNNTPWebMapping(s, webUserIDStr, 0, 0)
	if err != nil {
		session.SetError("NNTP user not created: " + err.Error())
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Check if NNTP username already exists
	_, err = s.DB.GetNNTPUserByUsername(username)
	if err == nil {
		session.SetError("NNTP user already exists")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Create NNTP user
	nntpUser := &models.NNTPUser{
		Username:  username,
		Password:  password,
		MaxConns:  maxConns,
		Posting:   posting,
		WebUserID: webUserID,
		IsActive:  true,
	}

	err = s.DB.InsertNNTPUser(nntpUser)
	if err != nil {
		log.Printf("Error creating NNTP user: %v", err)
		if database.IsWebUserMappingConflict(err) {
			session.SetError(database.ErrWebUserHasNNTPAccount.Error())
		} else {
			session.SetError("Failed to create NNTP user")
		}
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	session.SetSuccess("NNTP user created successfully - Username: " + username + ", Password: " + password)
	c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
}

// adminUpdateNNTPUser handles NNTP user updates
func (s *WebServer) adminUpdateNNTPUser(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get NNTP user ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Load the NNTP user first: fields that are not submitted keep their stored values
	nntpUser, err := s.DB.GetNNTPUserByID(id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[WEB]: Error loading NNTP user %d: %v", id, err)
		}
		session.SetError("NNTP user not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Get form data. The edit form always submits maxconns and the posting state (a hidden
	// "off" in front of the checkbox), so posting is updated even when maxconns is absent.
	maxConnsStr := strings.TrimSpace(c.PostForm("maxconns"))
	postingVals, postingGiven := c.GetPostFormArray("posting")
	webUserIDStr, webUserIDGiven := c.GetPostForm("web_user_id")
	password := strings.TrimSpace(c.PostForm("password"))

	// Validate every field before writing anything
	maxConns := nntpUser.MaxConns
	if maxConnsStr != "" {
		parsed, err := strconv.Atoi(maxConnsStr)
		if err != nil || parsed < 1 || parsed > 25 {
			session.SetError("Max connections must be between 1 and 25")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
		maxConns = parsed
	}
	posting := false // checkbox semantics: absent = off
	for _, v := range postingVals {
		if v == "on" || v == "true" {
			posting = true
		}
	}

	if password != "" && (len(password) < nntpAdminPasswordMin || len(password) > nntpAdminPasswordMax) {
		session.SetError(fmt.Sprintf("Password must be between %d and %d bytes", nntpAdminPasswordMin, nntpAdminPasswordMax))
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	webUserID := nntpUser.WebUserID
	mappingNotice := ""
	if webUserIDGiven {
		webUserID, mappingNotice, err = resolveNNTPWebMapping(s, webUserIDStr, id, nntpUser.WebUserID)
		if err != nil {
			session.SetError("NNTP user not updated: " + err.Error())
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
	}

	// Update max connections and posting permission
	if maxConnsStr != "" || postingGiven {
		err = s.DB.UpdateNNTPUserPermissions(id, maxConns, posting)
		if err != nil {
			log.Printf("[WEB]: Error updating NNTP user permissions: %v", err)
			session.SetError("Failed to update NNTP user permissions")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
	}

	// Update password if provided
	if password != "" {
		err = s.DB.UpdateNNTPUserPassword(id, password)
		if err != nil {
			log.Printf("[WEB]: Error updating NNTP user password: %v", err)
			session.SetError("Failed to update NNTP user password")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
	}

	// Update the web user mapping (0 = none, stored as NULL)
	if webUserIDGiven && webUserID != nntpUser.WebUserID {
		err = s.DB.UpdateNNTPUserWebUserID(id, webUserID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				session.SetError("NNTP user not found")
			} else if errors.Is(err, database.ErrWebUserHasNNTPAccount) || database.IsWebUserMappingConflict(err) {
				session.SetError(database.ErrWebUserHasNNTPAccount.Error())
			} else {
				log.Printf("[WEB]: Error updating NNTP user web mapping: %v", err)
				session.SetError("Failed to update NNTP user web mapping")
			}
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
	}

	if mappingNotice != "" {
		session.SetSuccess("NNTP user updated successfully (" + mappingNotice + ")")
	} else {
		session.SetSuccess("NNTP user updated successfully")
	}
	c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
}

// adminDeleteNNTPUser handles NNTP user deletion
func (s *WebServer) adminDeleteNNTPUser(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get NNTP user ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Delete NNTP user
	err = s.DB.DeleteNNTPUser(id)
	if err != nil {
		log.Printf("Error deleting NNTP user: %v", err)
		session.SetError("Failed to delete NNTP user")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	session.SetSuccess("NNTP user deleted successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
}

// adminToggleNNTPUser handles activating/deactivating NNTP users
func (s *WebServer) adminToggleNNTPUser(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get NNTP user ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Get current user status and toggle
	nntpUser, err := s.DB.GetNNTPUserByID(id)
	if err != nil {
		session.SetError("NNTP user not found")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	if nntpUser.IsActive {
		err = s.DB.DeactivateNNTPUser(id)
		if err != nil {
			log.Printf("Error deactivating NNTP user: %v", err)
			session.SetError("Failed to deactivate NNTP user")
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
		session.SetSuccess("NNTP user deactivated successfully")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
	} else {
		err = s.DB.ActivateNNTPUser(id)
		if err != nil {
			log.Printf("Error activating NNTP user: %v", err)
			if database.IsWebUserMappingConflict(err) {
				session.SetError(database.ErrWebUserHasNNTPAccount.Error())
			} else {
				session.SetError("Failed to activate NNTP user")
			}
			c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
			return
		}
		session.SetSuccess("NNTP user activated successfully")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
	}
}

// adminEnableNNTPUser handles activating NNTP users
func (s *WebServer) adminEnableNNTPUser(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get NNTP user ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid NNTP user ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	// Enable NNTP user
	err = s.DB.ActivateNNTPUser(id)
	if err != nil {
		log.Printf("Error enabling NNTP user: %v", err)
		if database.IsWebUserMappingConflict(err) {
			session.SetError(database.ErrWebUserHasNNTPAccount.Error())
		} else {
			session.SetError("Failed to enable NNTP user")
		}
		c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
		return
	}

	session.SetSuccess("NNTP user enabled successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=nntpusers")
}
