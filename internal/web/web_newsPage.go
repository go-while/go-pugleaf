// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"html/template"
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

	// Load template individually to avoid conflicts
	tmpl := template.Must(template.ParseFiles("web/templates/base.html", "web/templates/news.html"))
	c.Header("Content-Type", "text/html")
	err := tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Template error", err.Error())
		return
	}
}
