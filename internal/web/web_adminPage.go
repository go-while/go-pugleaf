package web

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adminPage displays the admin interface
func (s *WebServer) adminPage(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get current user for template
	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil {
		session.SetError("Failed to load user")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	var users []*models.User
	userSearch := c.Query("user_search")
	nonce := c.Query("nonce")

	// Only search users if search term is provided and has at least 2 characters
	if userSearch != "" && len(strings.TrimSpace(userSearch)) >= 2 {
		var err error

		// If nonce is provided, treat userSearch as a computed hash and search by hash
		if nonce != "" && len(strings.TrimSpace(nonce)) > 0 {
			targetHash := strings.TrimSpace(userSearch)
			nonceValue := strings.TrimSpace(nonce)

			// Search for user by computed hash
			user, err := s.DB.SearchUserByComputedHash(targetHash, nonceValue)
			if err != nil {
				log.Printf("Failed to search user by computed hash: %v", err)
				s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to search user by computed hash")
				return
			}

			if user != nil {
				users = []*models.User{user}
			}
		} else {
			// Regular search by username, email, or display name
			users, err = s.DB.SearchUsers(strings.TrimSpace(userSearch), 100) // Limit to 100 results for performance
			if err != nil {
				log.Printf("Failed to search users: %v", err)
				s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to search users")
				return
			}
		}
	}

	// Create a map of user IDs to their NNTP users
	userNNTPMap := make(map[int64]*models.NNTPUser)
	for _, user := range users {
		nntpUser, err := s.DB.GetNNTPUserByWebUserID(user.ID)
		if err == nil && nntpUser != nil {
			userNNTPMap[user.ID] = nntpUser
		}
	}

	// Get newsgroups with pagination and search
	page := 1
	pageSize := 50 // Static page size for newsgroups (cache efficiency)
	searchTerm := c.Query("search")
	searchDescription := c.Query("search_description") == "on"

	if p := c.Query("ng_page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	var newsgroups []*models.Newsgroup
	var newsgroupCount int

	if searchTerm != "" {
		// Use search function with description option if search term provided
		newsgroups, err = s.DB.SearchNewsgroupsWithOptions(searchTerm, 1000, 0, true, searchDescription) // High limit for admin
		if err != nil {
			s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to search newsgroups")
			return
		}
		// Get accurate count using the same search options
		newsgroupCount, err = s.DB.CountSearchNewsgroupsWithOptions(searchTerm, searchDescription, true)
		if err != nil {
			s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to count search results")
			return
		}

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

	// Get all cron jobs
	cronJobs, err := s.DB.GetAllCronJobs()
	if err != nil {
		log.Printf("Failed to load cron jobs: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load cron jobs")
		return
	}

	// Get all NNTP users
	var nntpUsers []*models.NNTPUser
	nntpUserSearch := c.Query("nntp_user_search")

	// Only search NNTP users if search term is provided and has at least 2 characters
	if nntpUserSearch != "" && len(strings.TrimSpace(nntpUserSearch)) >= 2 {
		var err error
		nntpUsers, err = s.DB.SearchNNTPUsers(strings.TrimSpace(nntpUserSearch), 100) // Limit to 100 results for performance
		if err != nil {
			log.Printf("Failed to search NNTP users: %v", err)
			s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to search NNTP users")
			return
		}
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
		activeTab = "settings" // or any other default tab you want
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

	// Handle post queue data if on postqueue tab
	var postQueue []*database.PostQueueEntryWithDetails
	var queueStats map[string]int
	statusFilter := c.Query("status_filter")
	queueSearch := c.Query("search")

	if activeTab == "postqueue" {
		// Get post queue entries with filtering
		var err error
		postQueue, err = s.DB.GetAllPostQueueEntries(100, 0, statusFilter, queueSearch)
		if err != nil {
			log.Printf("Failed to load post queue entries: %v", err)
			s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load post queue entries")
			return
		}

		// Get queue statistics
		queueStats, err = s.DB.GetPostQueueStats()
		if err != nil {
			log.Printf("Failed to load post queue stats: %v", err)
			// Don't fail the request, just log the error
		}
	}

	// Get registration status
	registrationEnabled, err := s.DB.IsRegistrationEnabled()
	if err != nil {
		log.Printf("Failed to get registration status: %v", err)
		registrationEnabled = true // Default to enabled on error
	}

	// Get current NNTP hostname from database
	currentHostname, err := s.DB.GetConfigValue("local_nntp_hostname")
	if err != nil {
		log.Printf("Failed to get NNTP hostname: %v", err)
		currentHostname = "" // Default to empty on error
	}

	// Get current WebPostMaxArticleSize from database
	webPostMaxSize, err := s.DB.GetConfigValue(config.CFG_KEY_WEBPOSTSIZE)
	if err != nil {
		log.Printf("Failed to get WebPostMaxArticleSize: %v", err)
		webPostMaxSize = "32768" // Default to 32KB on error
	}

	// Get current AbuseMail from database
	abuseMail, err := s.DB.GetConfigValue(config.CFG_KEY_ABUSEMAIL)
	if err != nil {
		log.Printf("Failed to get AbuseMail: %v", err)
		abuseMail = "" // Default to empty on error
	}

	// Get current WebLocalNNTPServerAddrInfo from database
	webLocalNNTPServerAddrInfo, err := s.DB.GetConfigValue(config.CFG_KEY_WEBLOCALNNTP)
	if err != nil {
		log.Printf("Failed to get WebLocalNNTPServerAddrInfo: %v", err)
		webLocalNNTPServerAddrInfo = "" // Default to empty on error
	}

	// Get current ReverseProxyAddr from database
	reverseProxyAddr, err := s.DB.GetConfigValue(config.CFG_KEY_REVERSEPROXY)
	if err != nil {
		log.Printf("Failed to get ReverseProxyAddr: %v", err)
		reverseProxyAddr = "" // Default to empty on error
	}

	// Get current BadBots from database
	badBots, err := s.DB.GetConfigValue(config.CFG_KEY_BADBOTS)
	if err != nil {
		log.Printf("Failed to get BadBots: %v", err)
		badBots = "" // Default to empty on error
	}

	// Get current BlockBadBots from database
	blockBadBotsStr, err := s.DB.GetConfigValue(config.CFG_KEY_BLOCKBADBOTS)
	if err != nil {
		log.Printf("Failed to get BlockBadBots: %v", err)
		blockBadBotsStr = "false" // Default to false on error
	}
	blockBadBots := blockBadBotsStr == "true"

	// Get current BadIPs from database
	badIPs, err := s.DB.GetConfigValue(config.CFG_KEY_BADIPS)
	if err != nil {
		log.Printf("Failed to get BadIPs: %v", err)
		badIPs = "" // Default to empty on error
	}

	// Get current BlockBadIPs from database
	blockBadIPsStr, err := s.DB.GetConfigValue(config.CFG_KEY_BLOCKBADIPS)
	if err != nil {
		log.Printf("Failed to get BlockBadIPs: %v", err)
		blockBadIPsStr = "false" // Default to false on error
	}
	blockBadIPs := blockBadIPsStr == "true"

	// Get current API Enabled from database
	apiEnabledStr, err := s.DB.GetConfigValue(config.CFG_KEY_API_ENABLED)
	if err != nil {
		log.Printf("Failed to get APIEnabled: %v", err)
		apiEnabledStr = "false" // Default to false on error
	}
	apiEnabled := apiEnabledStr == "true"

	// Get total users count for statistics
	totalUsersCount := s.DB.GetUsersCount()
	// Get total admin users count for statistics
	totalAdminCount := s.DB.GetAdminUsersCount()

	data := AdminPageData{
		TemplateData:               s.getBaseTemplateData(c, "Admin Interface"),
		Users:                      users,
		UserSearch:                 userSearch,
		Nonce:                      nonce,
		UserNNTPMap:                userNNTPMap,
		Newsgroups:                 newsgroups,
		NewsgroupPagination:        newsgroupPagination,
		NewsgroupSearch:            searchTerm,
		SearchDescription:          searchDescription,
		Providers:                  providers,
		APITokens:                  apiTokens,
		AIModels:                   aiModels,
		NNTPUsers:                  nntpUsers,
		NNTPUserSearch:             nntpUserSearch,
		SiteNews:                   siteNews,
		CronJobs:                   cronJobs,
		Sections:                   sections,
		SectionGroups:              sectionGroups,
		SpamArticles:               spamArticles,
		SpamPagination:             spamPagination,
		CurrentUser:                currentUser,
		TotalUsersCount:            totalUsersCount,
		AdminCount:                 s.countAdminUsers(users),
		TotalAdminCount:            totalAdminCount,
		EnabledTokensCount:         s.countEnabledAPITokens(apiTokens),
		ActiveSessions:             s.countActiveSessions(),
		ActiveNNTPUsers:            s.countActiveNNTPUsers(nntpUsers),
		PostingNNTPUsers:           s.countPostingNNTPUsers(nntpUsers),
		Uptime:                     s.getUptime(),
		CacheStats:                 cacheStats,
		NewsgroupCacheStats:        newsgroupCacheStats,
		ArticleCacheStats:          articleCacheStats,
		NNTPAuthCacheStats:         nntpAuthCacheStats,
		MessageIdCacheStats:        messageIdCacheStats,
		RegistrationEnabled:        registrationEnabled,
		CurrentHostname:            currentHostname,
		WebPostMaxArticleSize:      webPostMaxSize,
		AbuseMail:                  abuseMail,
		WebLocalNNTPServerAddrInfo: webLocalNNTPServerAddrInfo,
		ReverseProxyAddr:           reverseProxyAddr,
		BadBots:                    badBots,
		BlockBadBots:               blockBadBots,
		BadIPs:                     badIPs,
		BlockBadIPs:                blockBadIPs,
		APIEnabled:                 apiEnabled,
		// Form field constants for admin settings
		FormFieldHostname:     config.FORM_FIELD_HOSTNAME,
		FormFieldWebPostSize:  config.FORM_FIELD_WEBPOSTSIZE,
		FormFieldAbuseMail:    config.FORM_FIELD_ABUSEMAIL,
		FormFieldWebLocalNNTP: config.FORM_FIELD_WEBLOCALNNTP,
		FormFieldReverseProxy: config.FORM_FIELD_REVERSEPROXY,
		FormFieldRegistration: config.FORM_FIELD_REGISTRATION,
		FormFieldBadBots:      config.FORM_FIELD_BADBOTS,
		FormFieldBlockBadBots: config.FORM_FIELD_BLOCKBADBOTS,
		FormFieldBadIPs:       config.FORM_FIELD_BADIPS,
		FormFieldBlockBadIPs:  config.FORM_FIELD_BLOCKBADIPS,
		FormFieldAPIEnabled:   config.FORM_FIELD_API_ENABLED,
		PostQueue:             postQueue,
		QueueStats:            queueStats,
		StatusFilter:          statusFilter,
		QueueSearch:           queueSearch,
		Success:               session.GetSuccess(),
		Error:                 session.GetError(),
		ActiveTab:             activeTab,
	}

	// Load modular admin templates
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"since": func(t time.Time) int64 {
			return int64(time.Since(t).Seconds())
		},
		"div": func(a, b int64) int64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
	}).ParseFiles(
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
		"web/templates/admin_settings.html",
		"web/templates/admin_crons.html",
		"web/templates/admin_postqueue.html",
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
