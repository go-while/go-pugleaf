// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
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

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/irc.html"))
	c.Header("Content-Type", "text/html")
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}
