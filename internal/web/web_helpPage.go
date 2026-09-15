// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
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

	s.renderPage(c, http.StatusOK, data, "help.html")
}
