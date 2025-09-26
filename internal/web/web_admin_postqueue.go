package web

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// adminDeletePostQueue handles the delete request for a specific post queue entry
func (s *WebServer) adminDeletePostQueueEntry(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

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

	log.Printf("Admin deleted post queue entry %d", id)
	session.SetSuccess("Post queue entry has been deleted")
	c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
}
