// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"
)

// This file should contain the home page related functions from server.go:
//
// Functions to be moved from server.go:
// - func (s *WebServer) homePage(c *gin.Context) (line ~538)
//   Main handler for the home/root page ("/")
//
// This file will handle the main landing page functionality.

func (s *WebServer) homePage(c *gin.Context) {
	groups, _ := s.DB.GetActiveNewsgroups()

	data := s.getBaseTemplateData(c, "Home")
	data.GroupCount = len(groups)

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/home.html"))
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}
