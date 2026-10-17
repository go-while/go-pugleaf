// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
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

	s.renderPage(c, http.StatusOK, data, "home.html")
}
