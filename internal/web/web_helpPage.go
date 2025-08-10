// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"
)

// This file should contain the help page related functions from server.go:
//
// Functions to be moved from server.go:
//   - func (s *WebServer) helpPage(c *gin.Context) (line ~880)
//     Handles "/help" route to display help information
//
// This file will handle the help page functionality, providing user documentation and API information.
func (s *WebServer) helpPage(c *gin.Context) {
	data := s.getBaseTemplateData(c, "Help & Commands")

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/help.html"))
	c.Header("Content-Type", "text/html")
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}
