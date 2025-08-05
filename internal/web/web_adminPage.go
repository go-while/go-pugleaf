package web

import (
	"html/template"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adminPage displays the admin interface
func (s *WebServer) adminPage(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login?redirect=/admin")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	// Check if user is admin
	if !s.isAdmin(currentUser) {
		s.renderError(c, http.StatusForbidden, "Access Denied", "You don't have permission to access the admin interface")
		return
	}

	// Get all users
	users, err := s.DB.GetAllUsers()
	if err != nil {
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load users")
		return
	}

	// Get newsgroups with pagination and search
	page := 1
	pageSize := 50 // Static page size for newsgroups (cache efficiency)
	searchTerm := c.Query("search")

	if p := c.Query("ng_page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	var newsgroups []*models.Newsgroup
	var newsgroupCount int

	if searchTerm != "" {
		// Use search function if search term provided
		// For admin page, get all results without pagination for now
		newsgroups, err = s.DB.SearchNewsgroups(searchTerm, 1000, 0) // High limit for admin
		if err != nil {
			s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to search newsgroups")
			return
		}
		newsgroupCount = len(newsgroups)

		// Apply manual pagination to search results
		start := (page - 1) * pageSize
		end := start + pageSize
		if start >= len(newsgroups) {
			newsgroups = []*models.Newsgroup{}
		} else {
			if end > len(newsgroups) {
				end = len(newsgroups)
			}
			newsgroups = newsgroups[start:end]
		}
	} else {
		// Use paginated function for normal listing
		newsgroups, newsgroupCount, err = s.DB.GetNewsgroupsPaginatedAdmin(page, pageSize)
		if err != nil {
			log.Printf("Failed to load newsgroups: %v", err)
			s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load newsgroups")
			return
		}
	}

	// Get all providers
	providers, err := s.DB.GetProviders()
	if err != nil {
		log.Printf("Failed to load providers: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load providers")
		return
	}

	// Get all API tokens
	apiTokens, err := s.DB.ListAPITokens()
	if err != nil {
		log.Printf("Failed to load API tokens: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load API tokens")
		return
	}

	// Get all AI models
	aiModels, err := s.DB.GetAllAIModels()
	if err != nil {
		log.Printf("Failed to load AI models: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load AI models")
		return
	}

	// Get all site news
	siteNews, err := s.DB.GetAllSiteNews()
	if err != nil {
		log.Printf("Failed to load site news: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load site news")
		return
	}

	// Get all NNTP users
	nntpUsers, err := s.DB.GetAllNNTPUsers()
	if err != nil {
		log.Printf("Failed to load NNTP users: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load NNTP users")
		return
	}

	// Get all sections with their group counts (efficient single query)
	sections, err := s.DB.GetAllSectionsWithCounts()
	if err != nil {
		log.Printf("Failed to load sections: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load sections")
		return
	}

	// Get all section groups (needed for both sections and newsgroups tabs)
	sectionGroups, err := s.DB.GetAllSectionGroups()
	if err != nil {
		log.Printf("Failed to load section groups: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load section groups")
		return
	}

	// Read tab parameter to control active tab in template
	activeTab := c.Query("tab")
	if activeTab == "" {
		activeTab = "users" // or any other default tab you want
	}

	// Create pagination info for newsgroups
	newsgroupPagination := models.NewPaginationInfo(page, pageSize, newsgroupCount)

	// Get cache statistics
	var cacheStats map[string]interface{}
	if cache := models.GetSanitizedCache(); cache != nil {
		cacheStats = cache.Stats()
		// Calculate utilization percentage
		if entries, ok := cacheStats["entries"].(int); ok {
			if maxEntries, ok := cacheStats["max_entries"].(int); ok && maxEntries > 0 {
				utilization := float64(entries) / float64(maxEntries) * 100
				cacheStats["utilization_percent"] = int(utilization)
			} else {
				cacheStats["utilization_percent"] = 0
			}
		} else {
			cacheStats["utilization_percent"] = 0
		}
		cacheStats["status"] = "active"
		cacheStats["size_bytes"] = cache.GetCachedSize()
		cacheStats["size_human"] = cache.GetCachedSizeHuman()
	} else {
		cacheStats = map[string]interface{}{
			"entries":             0,
			"max_entries":         0,
			"max_age":             "N/A",
			"status":              "not initialized",
			"utilization_percent": 0,
			"size_bytes":          0,
			"size_human":          "0 bytes",
		}
	}

	// Get newsgroup cache statistics
	var newsgroupCacheStats map[string]interface{}
	if ngCache := models.GetNewsgroupCache(); ngCache != nil {
		newsgroupCacheStats = ngCache.GetStats()
		// Calculate utilization percentage
		if entries, ok := newsgroupCacheStats["entries"].(int); ok {
			if maxEntries, ok := newsgroupCacheStats["max_entries"].(int); ok && maxEntries > 0 {
				utilization := float64(entries) / float64(maxEntries) * 100
				newsgroupCacheStats["utilization_percent"] = int(utilization)
			} else {
				newsgroupCacheStats["utilization_percent"] = 0
			}
		} else {
			newsgroupCacheStats["utilization_percent"] = 0
		}
		newsgroupCacheStats["status"] = "active"
	} else {
		newsgroupCacheStats = map[string]interface{}{
			"entries":             0,
			"max_entries":         0,
			"max_age":             "N/A",
			"status":              "not initialized",
			"utilization_percent": 0,
			"size_bytes":          0,
			"size_human":          "0 bytes",
		}
	}

	// Get article cache statistics
	var articleCacheStats map[string]interface{}
	if s.DB.ArticleCache != nil {
		articleCacheStats = s.DB.ArticleCache.Stats()
		articleCacheStats["status"] = "active"
	} else {
		articleCacheStats = map[string]interface{}{
			"size":                0,
			"max_size":            0,
			"hits":                0,
			"misses":              0,
			"evictions":           0,
			"hit_rate":            0.0,
			"total_size":          0,
			"memory_mb":           0.0,
			"max_age":             "0s",
			"utilization_percent": 0.0,
			"status":              "not initialized",
		}
	}

	// Get NNTP auth cache statistics
	var nntpAuthCacheStats map[string]interface{}
	if s.DB.NNTPAuthCache != nil {
		nntpAuthCacheStats = s.DB.NNTPAuthCache.Stats()
		nntpAuthCacheStats["status"] = "active"
	} else {
		nntpAuthCacheStats = map[string]interface{}{
			"entries":   0,
			"hits":      0,
			"misses":    0,
			"hit_rate":  0.0,
			"evictions": 0,
			"memory_mb": 0.0,
			"status":    "not initialized",
		}
	}

	// Get message ID cache statistics
	var messageIdCacheStats map[string]interface{}
	if history.MsgIdCache != nil {
		totalBuckets, occupiedBuckets, items, maxChainLength, loadFactor := history.MsgIdCache.DetailedStats()

		// Calculate utilization percentage (how much of the available capacity is used)
		utilizationPercent := 0.0
		if history.UpperLimitMsgIdCacheSize > 0 {
			utilizationPercent = float64(totalBuckets) / float64(history.UpperLimitMsgIdCacheSize) * 100
		}

		// Calculate bucket occupancy percentage (how many buckets have items)
		bucketOccupancyPercent := 0.0
		if totalBuckets > 0 {
			bucketOccupancyPercent = float64(occupiedBuckets) / float64(totalBuckets) * 100
		}

		messageIdCacheStats = map[string]interface{}{
			"total_buckets":            totalBuckets,
			"occupied_buckets":         occupiedBuckets,
			"items":                    items,
			"max_chain_length":         maxChainLength,
			"load_factor":              loadFactor,
			"utilization_percent":      utilizationPercent,
			"bucket_occupancy_percent": bucketOccupancyPercent,
			"max_buckets":              history.UpperLimitMsgIdCacheSize,
			"max_load_factor":          history.MaxLoadFactor,
			"status":                   "active",
		}
	} else {
		messageIdCacheStats = map[string]interface{}{
			"total_buckets":            0,
			"occupied_buckets":         0,
			"items":                    0,
			"max_chain_length":         0,
			"load_factor":              0.0,
			"utilization_percent":      0.0,
			"bucket_occupancy_percent": 0.0,
			"max_buckets":              history.UpperLimitMsgIdCacheSize,
			"max_load_factor":          history.MaxLoadFactor,
			"status":                   "not initialized",
		}
	}

	// Load spam data if spam tab is active
	var spamArticles []*SpamArticleInfo
	var spamPagination *models.PaginationInfo
	if activeTab == "spam" {
		// Get spam pagination parameters
		spamPage := 1
		spamPageSize := 50
		if p := c.Query("page"); p != "" {
			if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
				spamPage = parsed
			}
		}

		// Calculate offset
		spamOffset := (spamPage - 1) * spamPageSize

		// Get spam articles with pagination
		var spamCount int
		var groupNames []string
		var overviews []*models.Overview
		overviews, groupNames, spamCount, err = s.DB.GetSpamArticles(spamOffset, spamPageSize)
		if err != nil {
			log.Printf("Failed to load spam articles: %v", err)
			// Don't fail completely, just show empty spam data
			spamArticles = []*SpamArticleInfo{}
			spamCount = 0
		} else {
			// Convert to SpamArticleInfo
			spamArticles = make([]*SpamArticleInfo, len(overviews))
			for i, overview := range overviews {
				spamArticles[i] = &SpamArticleInfo{
					Overview:  overview,
					GroupName: groupNames[i],
				}
			}
		}

		// Create pagination info for spam
		spamPagination = models.NewPaginationInfo(spamPage, spamPageSize, spamCount)
	}

	// Get registration status
	registrationEnabled, err := s.DB.IsRegistrationEnabled()
	if err != nil {
		log.Printf("Failed to get registration status: %v", err)
		registrationEnabled = true // Default to enabled on error
	}

	data := AdminPageData{
		TemplateData:        s.getBaseTemplateData(c, "Admin Interface"),
		Users:               users,
		Newsgroups:          newsgroups,
		NewsgroupPagination: newsgroupPagination,
		NewsgroupSearch:     searchTerm,
		Providers:           providers,
		APITokens:           apiTokens,
		AIModels:            aiModels,
		NNTPUsers:           nntpUsers,
		SiteNews:            siteNews,
		Sections:            sections,
		SectionGroups:       sectionGroups,
		SpamArticles:        spamArticles,
		SpamPagination:      spamPagination,
		CurrentUser:         currentUser,
		AdminCount:          s.countAdminUsers(users),
		EnabledTokensCount:  s.countEnabledAPITokens(apiTokens),
		ActiveSessions:      s.countActiveSessions(),
		ActiveNNTPUsers:     s.countActiveNNTPUsers(nntpUsers),
		PostingNNTPUsers:    s.countPostingNNTPUsers(nntpUsers),
		Uptime:              s.getUptime(),
		CacheStats:          cacheStats,
		NewsgroupCacheStats: newsgroupCacheStats,
		ArticleCacheStats:   articleCacheStats,
		NNTPAuthCacheStats:  nntpAuthCacheStats,
		MessageIdCacheStats: messageIdCacheStats,
		RegistrationEnabled: registrationEnabled,
		Success:             session.GetSuccess(),
		Error:               session.GetError(),
		ActiveTab:           activeTab, // <-- add this field to AdminPageData struct
	}

	// Load modular admin templates
	tmpl := template.Must(template.ParseFiles(
		"web/templates/base.html",
		"web/templates/admin_modular.html",
		"web/templates/admin_users.html",
		"web/templates/admin_newsgroups.html",
		"web/templates/admin_providers.html",
		"web/templates/admin_apitokens.html",
		"web/templates/admin_aimodels.html",
		"web/templates/admin_nntpusers.html",
		"web/templates/admin_sitenews.html",
		"web/templates/admin_sections.html",
		"web/templates/admin_statistics.html",
		"web/templates/admin_spam.html",
	))
	c.Header("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(c.Writer, "base.html", data)
	if err != nil {
		// Log the error but don't render error page since response has started
		log.Printf("[ERROR] Template execution failed: %v", err)
		// Write a simple error message that won't break the layout
		c.Writer.WriteString(`<div class="alert alert-danger">Template Error: ` + err.Error() + `</div>`)
	}
}
