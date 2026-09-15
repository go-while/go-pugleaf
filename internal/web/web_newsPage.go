// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// NewsPageData represents data for news page
type NewsPageData struct {
	TemplateData
}

// newsPage handles the "/SiteNews" route to display site news
func (s *WebServer) newsPage(c *gin.Context) {
	data := NewsPageData{
		TemplateData: s.getBaseTemplateData(c, "Site News"),
	}

	s.renderPage(c, http.StatusOK, data, "news.html")
}
