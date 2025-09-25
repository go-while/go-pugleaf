package web

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// adminDeletePostQueue handles the delete request for a specific post queue entry
func (s *WebServer) adminDeletePostQueueEntry(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login?redirect=/admin?tab=postqueue")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	// Get the ID from the form
	idStr := c.PostForm("id")
	if idStr == "" {
		session.SetError("No post queue entry ID provided")
		c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		session.SetError("Invalid post queue entry ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
		return
	}

	// Delete the entry
	err = s.DB.DeletePostQueueEntry(id)
	if err != nil {
		log.Printf("Failed to delete post queue entry %d: %v", id, err)
		session.SetError("Failed to delete post queue entry")
		c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
		return
	}

	log.Printf("Admin %s deleted post queue entry %d", currentUser.Username, id)
	session.SetSuccess("Post queue entry has been deleted")
	c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
}
