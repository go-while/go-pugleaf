package web

import (
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// FlashMessage represents a temporary success/error message
type FlashMessage struct {
	Type    string // "success" or "error"
	Message string
}

// SpamArticleInfo wraps Overview with group name for admin spam management
type SpamArticleInfo struct {
	*models.Overview
	GroupName string
}

// AdminPageData represents data for admin page
type AdminPageData struct {
	TemplateData
	Users                 []*models.User
	Newsgroups            []*models.Newsgroup
	NewsgroupPagination   *models.PaginationInfo
	NewsgroupSearch       string
	Providers             []*models.Provider
	APITokens             []*database.APIToken
	AIModels              []*models.AIModel
	NNTPUsers             []*models.NNTPUser
	SiteNews              []*models.SiteNews     // Added for site news management
	Sections              []*models.Section      // Added for section management
	SectionGroups         []*models.SectionGroup // Added for section-newsgroup assignments
	SpamArticles          []*SpamArticleInfo     // Added for spam management
	SpamPagination        *models.PaginationInfo // Added for spam pagination
	CurrentUser           *models.User
	AdminCount            int
	EnabledTokensCount    int
	ActiveSessions        int
	ActiveNNTPUsers       int
	PostingNNTPUsers      int
	Uptime                string
	CacheStats            map[string]interface{} // Added for cache monitoring
	NewsgroupCacheStats   map[string]interface{} // Added for newsgroup cache monitoring
	ArticleCacheStats     map[string]interface{} // Added for article cache monitoring
	NNTPAuthCacheStats    map[string]interface{} // Added for NNTP auth cache monitoring
	MessageIdCacheStats   map[string]interface{} // Added for message ID cache monitoring
	RegistrationEnabled   bool                   // Added for registration control
	CurrentHostname       string                 // Added for NNTP hostname configuration
	WebPostMaxArticleSize string                 // Added for web post size configuration
	Success               string
	Error                 string
	ActiveTab             string // Added for tab state
}

// getUptime returns server uptime (placeholder)
func (s *WebServer) getUptime() string {
	// TODO: Implement actual uptime calculation
	uptime := time.Since(s.StartTime) // Assuming StartTime is set when the server starts
	return uptime.String()
}
