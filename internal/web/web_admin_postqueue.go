package web

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
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
	if errors.Is(err, database.ErrPostQueueEntryUnchanged) {
		session.SetError("Post queue entry not found or being posted right now")
		c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
		return
	}
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

// adminRetryPostQueueEntry resets the attempt counter and last error of a post queue entry, so a
// parked entry is posted again by the next post-queue run. Posted entries and entries being
// posted right now are left alone.
func (s *WebServer) adminRetryPostQueueEntry(c *gin.Context) {
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

	// Reset attempts and last error
	err = s.DB.RetryPostQueueEntry(id)
	if errors.Is(err, database.ErrPostQueueEntryUnchanged) {
		session.SetError("Post queue entry not found, already posted or being posted right now")
		c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
		return
	}
	if err != nil {
		log.Printf("Failed to retry post queue entry %d: %v", id, err)
		session.SetError("Failed to retry post queue entry")
		c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
		return
	}

	log.Printf("Admin reset post queue entry %d for retry", id)
	session.SetSuccess("Post queue entry will be posted again by the next post-queue run")
	c.Redirect(http.StatusSeeOther, "/admin?tab=postqueue")
}
