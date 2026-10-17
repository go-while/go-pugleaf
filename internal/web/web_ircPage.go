// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// NewsPageData represents data for news page
type IRCPageData struct {
	TemplateData
}

// ircPage handles the "/SiteIRC" route to display IRC server information
func (s *WebServer) ircPage(c *gin.Context) {
	data := IRCPageData{
		TemplateData: s.getBaseTemplateData(c, "IRC Server"),
	}

	s.renderPage(c, http.StatusOK, data, "irc.html")
}
