// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Admin-only functions for managing spam and hidden articles

func (s *WebServer) decrementSpam(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	group := c.Param("group")
	articleNumStr := c.Param("articleNum")

	articleNum, err := strconv.ParseInt(articleNumStr, 10, 64)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/groups/"+group)
		return
	}

	// Decrement spam counter in database
	err = s.DB.DecrementArticleSpam(group, articleNum)
	if err != nil {
		log.Printf("Error decrementing spam count: %v", err)
		session.SetError("Failed to decrement spam counter: " + err.Error())
	} else {
		log.Printf("DEBUG: Successfully decremented spam count for article %d", articleNum)
		session.SetSuccess("Spam counter decremented successfully")
	}

	// Check referer to determine redirect location and preserve pagination
	referer := c.GetHeader("Referer")
	redirectURL := "/groups/" + group // Default fallback

	if referer != "" {
		// Parse referer to preserve query parameters (like page number)
		if parsedURL, err := url.Parse(referer); err == nil {
			if strings.Contains(referer, "/admin") {
				redirectURL = "/admin?tab=spam"
				if parsedURL.RawQuery != "" {
					// Preserve any existing query parameters from admin page
					redirectURL = "/admin?" + parsedURL.RawQuery
					if !strings.Contains(redirectURL, "tab=spam") {
						redirectURL += "&tab=spam"
					}
				}
			} else if strings.Contains(referer, "/threads") {
				// Preserve pagination for threads view
				redirectURL = "/groups/" + group + "/threads"
				if parsedURL.RawQuery != "" {
					redirectURL += "?" + parsedURL.RawQuery
				}
				// Add anchor for the specific thread (threads use thread- prefix)
				redirectURL += "#thread-" + strconv.FormatInt(articleNum, 10)
			} else {
				// Preserve pagination for group view
				redirectURL = "/groups/" + group
				if parsedURL.RawQuery != "" {
					redirectURL += "?" + parsedURL.RawQuery
				}
				// Add anchor for the specific article (group view uses article- prefix)
				redirectURL += "#article-" + strconv.FormatInt(articleNum, 10)
			}
		} else {
			log.Printf("Error parsing referer URL: %v", err)
		}
	}

	c.Redirect(http.StatusSeeOther, redirectURL)
}

func (s *WebServer) unhideArticle(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	group := c.Param("group")
	articleNumStr := c.Param("articleNum")

	articleNum, err := strconv.ParseInt(articleNumStr, 10, 64)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/groups/"+group)
		return
	}

	// Unhide article in database
	err = s.DB.UnHideArticle(group, articleNum)
	if err != nil {
		log.Printf("Error unhiding article: %v", err)
		session.SetError("Failed to unhide article: " + err.Error())
	} else {
		log.Printf("DEBUG: Successfully unhid article %d", articleNum)
		session.SetSuccess("Article unhidden successfully")
	}

	// Check referer to determine redirect location and preserve pagination
	referer := c.GetHeader("Referer")
	redirectURL := "/groups/" + group // Default fallback

	if referer != "" {
		// Parse referer to preserve query parameters (like page number)
		if parsedURL, err := url.Parse(referer); err == nil {
			if strings.Contains(referer, "/admin") {
				redirectURL = "/admin?tab=spam"
				if parsedURL.RawQuery != "" {
					// Preserve any existing query parameters from admin page
					redirectURL = "/admin?" + parsedURL.RawQuery
					if !strings.Contains(redirectURL, "tab=spam") {
						redirectURL += "&tab=spam"
					}
				}
			} else if strings.Contains(referer, "/threads") {
				// Preserve pagination for threads view
				redirectURL = "/groups/" + group + "/threads"
				if parsedURL.RawQuery != "" {
					redirectURL += "?" + parsedURL.RawQuery
				}
				// Add anchor for the specific thread (threads use thread- prefix)
				redirectURL += "#thread-" + strconv.FormatInt(articleNum, 10)
			} else {
				// Preserve pagination for group view
				redirectURL = "/groups/" + group
				if parsedURL.RawQuery != "" {
					redirectURL += "?" + parsedURL.RawQuery
				}
				// Add anchor for the specific article (group view uses article- prefix)
				redirectURL += "#article-" + strconv.FormatInt(articleNum, 10)
			}
		} else {
			log.Printf("Error parsing referer URL: %v", err)
		}
	}

	c.Redirect(http.StatusSeeOther, redirectURL)
}
