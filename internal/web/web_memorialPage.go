package web

import "net/http"

// memorialPage handles "/SiteMemory": the tribute to Retro Guy, the lead developer of
// rslight, linked from the footer of every page.
func (s *WebServer) memorialPage(c *Context) {
	data := s.getBaseTemplateData(c, "In memory of Retro Guy")
	s.renderPage(c, http.StatusOK, data, "memorial.html")
}
