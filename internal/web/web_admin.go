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
	Users                      []*models.User
	UserSearch                 string                     // Added for user search functionality
	Nonce                      string                     // Added for nonce input in hash search
	UserNNTPMap                map[int64]*models.NNTPUser // Added for NNTP user mapping
	Newsgroups                 []*models.Newsgroup
	NewsgroupPagination        *models.PaginationInfo
	NewsgroupSearch            string
	Providers                  []*models.Provider
	APITokens                  []*database.APIToken
	AIModels                   []*models.AIModel
	NNTPUsers                  []*models.NNTPUser
	NNTPUserSearch             string                 // Added for NNTP user search functionality
	CronJobs                   []*models.CronJob      // Added for cron job management
	SiteNews                   []*models.SiteNews     // Added for site news management
	Sections                   []*models.Section      // Added for section management
	SectionGroups              []*models.SectionGroup // Added for section-newsgroup assignments
	SpamArticles               []*SpamArticleInfo     // Added for spam management
	SpamPagination             *models.PaginationInfo // Added for spam pagination
	CurrentUser                *models.User
	AdminCount                 int
	EnabledTokensCount         int
	ActiveSessions             int
	ActiveNNTPUsers            int
	PostingNNTPUsers           int
	Uptime                     string
	CacheStats                 map[string]interface{}                // Added for cache monitoring
	NewsgroupCacheStats        map[string]interface{}                // Added for newsgroup cache monitoring
	ArticleCacheStats          map[string]interface{}                // Added for article cache monitoring
	NNTPAuthCacheStats         map[string]interface{}                // Added for NNTP auth cache monitoring
	MessageIdCacheStats        map[string]interface{}                // Added for message ID cache monitoring
	RegistrationEnabled        bool                                  // Added for registration control
	CurrentHostname            string                                // Added for NNTP hostname configuration
	WebPostMaxArticleSize      string                                // Added for web post size configuration
	AbuseMail                  string                                // Added for abuse email configuration
	WebLocalNNTPServerAddrInfo string                                // Added for NNTP server address info configuration
	ReverseProxyAddr           string                                // Added for reverse proxy address configuration
	BadBots                    string                                // Added for bad bots list configuration
	BlockBadBots               bool                                  // Added for bot blocking control
	BadIPs                     string                                // Added for bad IPs list configuration
	BlockBadIPs                bool                                  // Added for IP blocking control
	FormFieldHostname          string                                // NNTP hostname form field name
	FormFieldWebPostSize       string                                // Web post size form field name
	FormFieldAbuseMail         string                                // Abuse email form field name
	FormFieldWebLocalNNTP      string                                // Web local NNTP server form field name
	FormFieldReverseProxy      string                                // Reverse proxy form field name
	FormFieldRegistration      string                                // Registration toggle form field name
	FormFieldBadBots           string                                // Bad bots list form field name
	FormFieldBlockBadBots      string                                // Block bad bots toggle form field name
	FormFieldBadIPs            string                                // Bad IPs list form field name
	FormFieldBlockBadIPs       string                                // Block bad IPs toggle form field name
	PostQueue                  []*database.PostQueueEntryWithDetails // Added for post queue management
	QueueStats                 map[string]int                        // Added for post queue statistics
	StatusFilter               string                                // Added for post queue status filtering
	QueueSearch                string                                // Added for post queue search functionality
	Success                    string
	Error                      string
	ActiveTab                  string // Added for tab state
}

// getUptime returns server uptime (placeholder)
func (s *WebServer) getUptime() string {
	// TODO: Implement actual uptime calculation
	uptime := time.Since(s.StartTime) // Assuming StartTime is set when the server starts
	return uptime.String()
}
