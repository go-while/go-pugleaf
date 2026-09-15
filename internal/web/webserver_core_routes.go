// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-contrib/secure"
	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// Server represents the web server
type WebServer struct {
	DB            *database.Database
	Router        *gin.Engine
	Config        *config.WebConfig
	NNTP          *nntp.NNTPServer
	templates     *template.Template
	StartTime     time.Time                       // Track server start time for uptime calculations
	sectionsCache atomic.Pointer[map[string]bool] // Valid section names for route filtering (replaced as a whole, never mutated)
	robotsTxtPath string                          // Path to robots.txt file if it exists
	CronManager   *CronJobManager                 // Background cron job manager (nil with -no-cronjobs)
	CronEdit      bool

	trustedProxyNets []*net.IPNet // reverse proxies whose X-Forwarded-* headers are trusted

	httpServerMu sync.Mutex
	httpServer   *http.Server  // set by Start, stopped by Shutdown
	stopCh       chan struct{} // closed by Shutdown; background goroutines of the server stop on it
	stopOnce     sync.Once
}

// TemplateData represents common template data
type TemplateData struct {
	Title               string
	CurrentTime         string
	Port                int
	NNTPtcpPort         int
	NNTPtlsPort         int
	GroupCount          int
	User                *AuthUser
	IsAdmin             bool
	AppVersion          string
	RegistrationEnabled bool
	SiteNews            []*models.SiteNews // For home page news display
	AvailableSections   []*models.Section  // For global sections navigation
	AvailableAIModels   []*models.AIModel  // For conditional AI chat navigation
}

// GroupPageData represents data for group page
type GroupPageData struct {
	TemplateData
	GroupName  string
	GroupPtr   *string
	Articles   []*models.Overview
	Pagination *models.PaginationInfo
}

// ArticlePageData represents data for article page
type ArticlePageData struct {
	TemplateData
	GroupName   string
	GroupPtr    *string
	ArticleNum  int64
	Article     *models.Article
	Thread      []*models.Overview
	PrevArticle int64
	NextArticle int64
}

// StatsPageData represents data for stats page
type StatsPageData struct {
	TemplateData
	Groups        []*models.Newsgroup
	TotalArticles int64
	APIEnabled    bool
}

// GroupsPageData represents data for groups page
type GroupsPageData struct {
	TemplateData
	Groups     []*models.Newsgroup
	Pagination *models.PaginationInfo
	GroupCount int
}

// GroupThreadsPageData represents data for group threads page
type GroupThreadsPageData struct {
	TemplateData
	GroupName     string
	ForumThreads  []*models.ForumThread
	TotalThreads  int64
	TotalMessages int
	CurrentPage   int64
	TotalPages    int64
	PerPage       int64
	HasPrevPage   bool
	HasNextPage   bool
	PrevPage      int64
	NextPage      int64
}

// HierarchiesPageData represents data for hierarchies page
type HierarchiesPageData struct {
	TemplateData
	Hierarchies []*models.Hierarchy
	Pagination  *models.PaginationInfo
	SortBy      string
}

// HierarchyGroupsPageData represents data for groups within a hierarchy
type HierarchyGroupsPageData struct {
	TemplateData
	HierarchyName string
	Groups        []*models.Newsgroup
	Pagination    *models.PaginationInfo
	SortBy        string
}

// HierarchyTreePageData represents data for hierarchical tree navigation
type HierarchyTreePageData struct {
	TemplateData
	HierarchyName  string
	CurrentPath    string
	RelativePath   string // Path relative to hierarchy (e.g., "arts" for "alt.arts")
	ParentPath     string
	Breadcrumbs    []HierarchyBreadcrumb
	SubHierarchies []HierarchyNode
	Groups         []*models.Newsgroup
	TotalSubItems  int
	TotalGroups    int
	ShowingGroups  bool
	SortBy         string
	Pagination     *models.PaginationInfo
	AtMaxDepth     bool // True if we're at the maximum supported hierarchy depth (level 3)
}

// HierarchyBreadcrumb represents a breadcrumb item in hierarchy navigation
type HierarchyBreadcrumb struct {
	Name   string
	Path   string
	IsLast bool
}

// HierarchyNode represents a node in the hierarchy tree
type HierarchyNode struct {
	Name       string
	FullPath   string
	GroupCount int
	HasGroups  bool
}

// SectionPageData represents data for section page (shows groups in section)
type SectionPageData struct {
	TemplateData
	Section           *models.Section
	Groups            []*models.SectionGroup
	Pagination        *models.PaginationInfo
	TotalGroups       int
	AvailableSections []*models.Section
	SortBy            string
}

// SectionGroupPageData represents data for group page within a section
type SectionGroupPageData struct {
	TemplateData
	Section     *models.Section
	GroupName   string
	Articles    []*models.Overview
	Pagination  *models.PaginationInfo
	GroupExists bool
}

// SectionArticlePageData represents data for article page within a section
type SectionArticlePageData struct {
	TemplateData
	Section     *models.Section
	GroupName   string
	ArticleNum  int64
	Article     *models.Article
	Thread      []*models.Overview
	PrevArticle int64
	NextArticle int64
}

// SearchPageData represents data for search page
type SearchPageData struct {
	TemplateData
	Query       string
	SearchType  string
	Results     interface{}
	ResultCount int
	HasResults  bool
	Pagination  *models.PaginationInfo
}

var DefaultReverseProxy = []string{"127.0.0.1", "::1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// NewWebServer creates a new web server instance
func NewWebServer(db *database.Database, webconfig *config.WebConfig, nntpconfig *nntp.NNTPServer, cronEdit bool, noCronjobs bool) *WebServer {
	// Set Gin to release mode for production
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()

	// Client IPs come from X-Forwarded-For/X-Real-IP only when the peer is a trusted proxy
	ReverseProxyAddr, err := db.GetConfigValue(config.CFG_KEY_REVERSEPROXY)
	if err != nil {
		log.Printf("[WEB]: Error getting ReverseProxyAddr: %v", err)
	}
	trustedProxyNets := configureTrustedProxies(router, ReverseProxyAddr)

	// Configure security headers based on SSL setup
	secureConfig := secure.Config{
		FrameDeny:          true,
		ContentTypeNosniff: true,
		BrowserXssFilter:   true,
		ReferrerPolicy:     "strict-origin-when-cross-origin",
	}

	// Only add SSL-specific headers if SSL is enabled on the application itself
	// (not when running behind a reverse proxy like nginx with SSL)
	if webconfig.SSL {
		secureConfig.SSLRedirect = true
		secureConfig.STSSeconds = 31536000
		secureConfig.STSIncludeSubdomains = true
	}

	// Apply security middleware
	router.Use(secure.New(secureConfig))

	// Don't use ParseGlob - it causes template name conflicts
	// Instead, we'll load templates individually in each handler
	// router.SetHTMLTemplate(templates)
	var cronmgr *CronJobManager
	if !noCronjobs {
		cronmgr = NewCronJobManager(db)
	} else {
		cronEdit = false
	}

	server := &WebServer{
		DB:               db,
		Router:           router,
		Config:           webconfig,
		NNTP:             nntpconfig,
		templates:        nil, // We'll handle templates individually
		CronManager:      cronmgr,
		CronEdit:         cronEdit,
		trustedProxyNets: trustedProxyNets,
		stopCh:           make(chan struct{}),
	}

	// Check if robots.txt file exists
	robotsPath := "./web/robots.txt"
	if _, err := os.Stat(robotsPath); err == nil {
		server.robotsTxtPath = robotsPath
		log.Printf("Found robots.txt file at: %s", robotsPath)
	} else {
		log.Printf("No robots.txt file found, will use inline version")
	}

	// Initialize sections cache
	server.loadSectionsCache()

	// Start the cron job manager (nil with -no-cronjobs)
	if server.CronManager != nil {
		server.CronManager.StartCronManager()
	}

	// Expire idle AI chat histories and rate-limiter entries until Shutdown
	go server.runChatCacheSweeper()

	// Add reverse proxy middleware for handling X-Forwarded headers
	router.Use(server.ReverseProxyMiddleware())

	server.setupRoutes()
	return server
}

// setupRoutes configures all HTTP routes
func (s *WebServer) setupRoutes() {
	// Static files first (highest priority)
	s.Router.Use(s.BotDetectionMiddleware())

	// Use embedded static files if available, otherwise fall back to filesystem
	if UseEmbeddedStatic() {
		s.Router.GET("/static/*filepath", EmbeddedStaticHandler("/static"))
		log.Printf("[WEB]: Using embedded static files")
	} else {
		s.Router.Static("/static", "./web/static")
		log.Printf("[WEB]: Using filesystem static files from ./web/static")
	}

	// Handle favicon to prevent it from being caught by section routes
	if UseEmbeddedStatic() {
		s.Router.GET("/favicon.ico", EmbeddedFileHandler("web/favicon.ico"))
	} else {
		s.Router.GET("/favicon.ico", func(c *gin.Context) {
			c.File("web/favicon.ico")
		})
	}
	s.Router.GET("/sitemap.xml", func(c *gin.Context) {
		c.Status(http.StatusNotFound)
	})
	s.Router.GET("/robots.txt", func(c *gin.Context) {
		// Check if we have a physical robots.txt file
		if s.robotsTxtPath != "" {
			c.File(s.robotsTxtPath)
		} else {
			// Fallback to inline robots.txt with all allowed
			c.String(http.StatusOK, "User-agent: *\nDisallow:\n")
		}
	})
	s.Router.GET("/ping", func(c *gin.Context) {
		c.String(200, "pong")
	})

	// Handle API base routes to prevent them from being caught by section routes
	s.Router.GET("/api", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/help")
	})
	s.Router.GET("/api/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/help")
	})
	s.Router.GET("/api/v1", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/help")
	})
	s.Router.GET("/api/v1/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/help")
	})

	// API routes (high priority - before dynamic section routes)
	// stats are public
	s.Router.GET("/api/v1/stats", s.requireAPIEnabled(), s.getStats)  // API endpoint for stats
	s.Router.GET("/api/v1/stats/", s.requireAPIEnabled(), s.getStats) // API endpoint for stats

	// Public article preview endpoint (no auth required)
	//s.Router.GET("/api/v1/groups/:group/articles/:articleNum/preview", s.requireAPIEnabled(), s.getArticlePreview)
	s.Router.GET("/api/v1/groups/:group/articles/:articleNum/preview", s.getArticlePreview)

	api := s.Router.Group("/api/v1")
	api.Use(s.requireAPIEnabled()) // Check if API is enabled
	api.Use(s.APIAuthRequired())   // Then check authentication
	{
		//api.GET("/stats/", s.getStats) // not public, needs auth
		api.GET("/groups", s.listGroups)
		api.GET("/groups/", s.listGroups)
		api.GET("/groups/:group/overview", s.getGroupOverview)
		api.GET("/groups/:group/overview/", s.getGroupOverview)
		api.GET("/groups/:group/articles/:articleNum", s.getArticle)
		api.GET("/groups/:group/message/:messageId", s.getArticleByMessageId)
		api.GET("/groups/:group/threads", s.getGroupThreads)
		api.GET("/groups/:group/threads/", s.getGroupThreads)
	}

	// Authentication routes (high priority)
	s.Router.GET("/login", s.loginPage)
	s.Router.POST("/login", s.loginSubmit)
	s.Router.GET("/register", s.registerPage)
	s.Router.POST("/register", s.registerSubmit)
	s.Router.GET("/logout", s.logout)
	s.Router.GET("/profile", s.profilePage)
	s.Router.POST("/profile", s.profileUpdate)

	// AI Chat page (authenticated)
	s.Router.GET("/aichat", s.aichatPage)
	s.Router.POST("/aichat/send", s.aichatSend)
	s.Router.GET("/aichat/models", s.aichatModels)
	s.Router.POST("/aichat/history/:model", s.aichatLoadHistory)
	s.Router.POST("/aichat/clear/:model", s.aichatClearHistory)
	s.Router.POST("/aichat/clear/all", s.aichatClearHistory)
	s.Router.GET("/aichat/counts", s.aichatGetCounts)

	// Admin interface (authenticated)
	s.Router.GET("/admin", s.adminPage)
	s.Router.POST("/admin/users", s.adminCreateUser)
	s.Router.POST("/admin/users/update", s.adminUpdateUser)
	s.Router.POST("/admin/users/delete", s.adminDeleteUser)
	s.Router.POST("/admin/aimodels", s.adminCreateAIModel)
	s.Router.POST("/admin/aimodels/update", s.adminUpdateAIModel)
	s.Router.POST("/admin/aimodels/delete", s.adminDeleteAIModel)
	s.Router.POST("/admin/aimodels/sync", s.adminSyncOllamaModels)
	s.Router.POST("/admin/newsgroups", s.adminCreateNewsgroup)
	s.Router.POST("/admin/newsgroups/edit", s.adminUpdateNewsgroup)
	s.Router.POST("/admin/newsgroups/update", s.adminUpdateNewsgroup)
	s.Router.POST("/admin/newsgroups/delete", s.adminDeleteNewsgroup)
	s.Router.POST("/admin/newsgroups/assign-section", s.adminAssignNewsgroupSection)
	s.Router.POST("/admin/newsgroups/toggle", s.adminToggleNewsgroup)
	s.Router.POST("/admin/newsgroups/migrate-activity", s.adminMigrateNewsgroupActivity)
	s.Router.POST("/admin/newsgroups/hide-future-posts", s.adminHideFuturePosts)
	s.Router.POST("/admin/newsgroups/fix-thread-activity", s.adminFixThreadActivity)
	s.Router.POST("/admin/newsgroups/bulk-enable", s.adminBulkEnableNewsgroups)
	s.Router.POST("/admin/newsgroups/bulk-disable", s.adminBulkDisableNewsgroups)
	s.Router.POST("/admin/newsgroups/bulk-delete", s.adminBulkDeleteNewsgroups)
	s.Router.POST("/admin/providers", s.adminCreateProvider)
	s.Router.POST("/admin/providers/update", s.adminUpdateProvider)
	s.Router.POST("/admin/providers/delete", s.adminDeleteProvider)
	s.Router.POST("/admin/apitokens", s.adminCreateAPIToken)
	s.Router.POST("/admin/apitokens/toggle", s.adminToggleAPIToken)
	s.Router.POST("/admin/apitokens/delete", s.adminDeleteAPIToken)
	s.Router.POST("/admin/sitenews", s.adminCreateSiteNews)
	s.Router.POST("/admin/sitenews/update", s.adminUpdateSiteNews)
	s.Router.POST("/admin/sitenews/delete", s.adminDeleteSiteNews)
	s.Router.POST("/admin/sitenews/toggle", s.adminToggleSiteNewsVisibility)
	s.Router.POST("/admin/nntpusers", s.adminCreateNNTPUser)
	s.Router.POST("/admin/nntpusers/update", s.adminUpdateNNTPUser)
	s.Router.POST("/admin/nntpusers/delete", s.adminDeleteNNTPUser)
	s.Router.POST("/admin/nntpusers/toggle", s.adminToggleNNTPUser)
	s.Router.POST("/admin/nntpusers/enable", s.adminEnableNNTPUser)
	s.Router.POST("/admin/sections", s.CreateSectionHandler)
	s.Router.POST("/admin/sections/update", s.UpdateSectionHandler)
	s.Router.POST("/admin/sections/delete", s.DeleteSectionHandler)
	s.Router.POST("/admin/sections/assign", s.AssignNewsgroupHandler)
	s.Router.POST("/admin/sections/unassign", s.UnassignNewsgroupHandler)
	s.Router.POST("/admin/cache/clear", s.adminClearCache)
	s.Router.POST("/admin/hierarchies/update", s.adminUpdateHierarchies)
	s.Router.POST("/admin/hierarchies/restore-descriptions", s.adminRestoreHierarchyDescriptions)
	s.Router.POST("/admin/settings", s.adminUpdateSettings)
	s.Router.POST("/admin/crons/create", s.adminCreateCronJob)
	s.Router.POST("/admin/crons/update", s.adminUpdateCronJob)
	s.Router.POST("/admin/crons/toggle", s.adminToggleCronJob)
	s.Router.POST("/admin/crons/delete", s.adminDeleteCronJob)
	s.Router.POST("/admin/crons/stop", s.adminStopCronJob)
	s.Router.GET("/admin/cronjobs/viewlog/:id", s.adminViewCronJobLog)
	s.Router.POST("/admin/postqueue/delete", s.adminDeletePostQueueEntry)
	// Legacy/admin routes (high priority - must come before dynamic routes)
	s.Router.GET("/", s.homePage)
	s.Router.GET("/groups", s.groupsPage)                                                // groups listing
	s.Router.GET("/groups/", s.groupsPage)                                               // Handle trailing slash
	s.Router.GET("/hierarchies", s.hierarchiesPage)                                      // Hierarchies listing
	s.Router.GET("/hierarchies/", s.hierarchiesPage)                                     // Handle trailing slash
	s.Router.GET("/hierarchy-groups/:hierarchy", s.hierarchyGroupsPage)                  // Flat groups listing (legacy)
	s.Router.GET("/hierarchy/:hierarchy", s.hierarchyTreePage)                           // Hierarchical tree view
	s.Router.GET("/hierarchy/:hierarchy/", s.hierarchyTreePage)                          // Handle trailing slash
	s.Router.GET("/hierarchy/:hierarchy/:level1", s.hierarchyTreePage)                   // Level 1: hierarchy.level1
	s.Router.GET("/hierarchy/:hierarchy/:level1/", s.hierarchyTreePage)                  // Level 1 with slash
	s.Router.GET("/hierarchy/:hierarchy/:level1/:level2", s.hierarchyTreePage)           // Level 2: hierarchy.level1.level2
	s.Router.GET("/hierarchy/:hierarchy/:level1/:level2/", s.hierarchyTreePage)          // Level 2 with slash
	s.Router.GET("/hierarchy/:hierarchy/:level1/:level2/:level3", s.hierarchyTreePage)   // Level 3: hierarchy.level1.level2.level3
	s.Router.GET("/hierarchy/:hierarchy/:level1/:level2/:level3/", s.hierarchyTreePage)  // Level 3 with slash
	s.Router.GET("/groups/:group", s.groupPage)                                          // Legacy group access
	s.Router.GET("/groups/:group/", s.groupPage)                                         // Legacy group access with slash
	s.Router.POST("/groups/:group/articles/:articleNum/spam", s.incrementSpam)           // Spam button
	s.Router.POST("/groups/:group/articles/:articleNum/hide", s.incrementHide)           // Hide button
	s.Router.POST("/groups/:group/articles/:articleNum/decrement-spam", s.decrementSpam) // Admin: Decrement spam
	s.Router.POST("/groups/:group/articles/:articleNum/unhide", s.unhideArticle)         // Admin: Unhide article
	s.Router.GET("/groups/:group/threads", s.groupThreadsPage)                           // Legacy threads access
	s.Router.GET("/groups/:group/thread/:threadRoot", s.singleThreadPage)                // View single thread flat
	s.Router.GET("/groups/:group/articles/:articleNum", s.articlePage)                   // Legacy article access
	s.Router.GET("/groups/:group/message/:messageId", s.articleByMessageIdPage)          // Article by message ID
	s.Router.GET("/groups/:group/tree/:threadRoot", s.threadTreePage)                    // View thread as tree
	s.Router.GET("/search", s.searchPage)
	s.Router.GET("/search/", s.searchPage)
	s.Router.GET("/stats", s.statsPage)
	s.Router.GET("/stats/", s.statsPage)
	s.Router.GET("/SiteHelp", s.helpPage)
	s.Router.GET("/SiteHelp/", s.helpPage)
	s.Router.GET("/SiteNews", s.newsPage)
	s.Router.GET("/SiteNews/", s.newsPage)
	s.Router.GET("/SiteIRC", s.ircPage)
	s.Router.GET("/SiteIRC/", s.ircPage)
	s.Router.GET("/SitePost", s.sitePostPage)
	s.Router.POST("/SitePost", s.sitePostPage)
	s.Router.POST("/SitePostSubmit", s.sitePostSubmit) // Handle form submission
	s.Router.GET("/sections", s.sectionsPage)
	s.Router.GET("/sections/", s.sectionsPage)

	// Demo and testing routes
	s.Router.GET("/demo/thread-tree", s.threadTreeDemoPage)
	s.Router.GET("/demo/thread-tree/", s.threadTreeDemoPage)

	// Tree view API routes
	s.Router.GET("/api/thread-tree", s.handleThreadTreeAPI)

	// Tree view for specific threads

	// Apply section validation middleware for dynamic section routes
	sectionRoutes := s.Router.Group("/")
	sectionRoutes.Use(s.sectionValidationMiddleware())
	{
		// Dynamic section routes with validation (lower priority)
		sectionRoutes.GET("/:section", s.sectionPage)                                             // /rocksolid - list groups in section
		sectionRoutes.GET("/:section/", s.sectionPage)                                            // /rocksolid/ - list groups in section
		sectionRoutes.GET("/:section/:group/", s.sectionGroupPage)                                // /rocksolid/group.name/ - list articles
		sectionRoutes.GET("/:section/:group/articles/:articleNum", s.sectionArticlePage)          // /rocksolid/group.name/articles/123
		sectionRoutes.GET("/:section/:group/message/:messageId", s.sectionArticleByMessageIdPage) // /rocksolid/group.name/message/msgid
		sectionRoutes.GET("/:section/:group/tree/:threadRoot", s.sectionThreadTreePage)           // View thread as tree in section
	}
}

// Custom bot detection middleware
func (s *WebServer) BotDetectionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Copy the settings under the read locks and never hold them across c.Next():
		// UpdateBadBots/UpdateBadIPs would wait for the slowest request and block every new one.
		// The update functions replace the slices instead of mutating them, so the copies stay valid.
		config.BadIPsMutex.RLock()
		blockBadIPs := config.BlockBadIPs
		blockedIPs := config.Default_BlockedIPs
		config.BadIPsMutex.RUnlock()

		config.BadBotsMutex.RLock()
		blockBadBots := config.BlockBadBots
		badBots := config.Default_BadBots
		config.BadBotsMutex.RUnlock()

		// Check IP blocking first
		if blockBadIPs && len(blockedIPs) > 0 {
			clientIP := c.ClientIP()
			if ip := net.ParseIP(clientIP); ip != nil {
				for _, ipNet := range blockedIPs {
					if ipNet.Contains(ip) {
						log.Printf("IP blocked: %s (matches %s)", clientIP, ipNet.String())
						c.String(403, "403")
						c.Abort()
						return
					}
				}
			}
		}

		// Check user agent against bad bot patterns (UpdateBadBots stores them lowercase)
		if blockBadBots && len(badBots) > 0 {
			ua := strings.ToLower(c.GetHeader("User-Agent"))
			for _, pattern := range badBots {
				if pattern != "" && strings.Contains(ua, pattern) {
					log.Printf("Bot blocked: '%s' IP: '%s'", c.GetHeader("User-Agent"), c.ClientIP())
					c.String(403, "403")
					c.Abort()
					return
				}
			}
		}
		c.Next()
	}
}

// configureTrustedProxies makes gin take the client IP from X-Forwarded-For/X-Real-IP only when the
// direct peer is a configured reverse proxy (comma or whitespace separated IPs/CIDRs; empty or
// all-invalid means DefaultReverseProxy). gin evaluates X-Forwarded-For right to left and ignores
// invalid addresses. It returns the parsed networks for isTrustedPeer.
func configureTrustedProxies(router *gin.Engine, addrs string) []*net.IPNet {
	list, nets := parseTrustedProxyList(strings.Fields(strings.ReplaceAll(addrs, ",", " ")))
	if len(list) == 0 {
		list, nets = parseTrustedProxyList(DefaultReverseProxy)
	}
	if err := router.SetTrustedProxies(list); err != nil {
		log.Printf("[WEB]: Error setting trusted proxies %v: %v", list, err)
	}
	router.ForwardedByClientIP = true
	router.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}
	return nets
}

// parseTrustedProxyList parses IPs (as /32 or /128) and CIDRs, skipping invalid entries.
func parseTrustedProxyList(entries []string) ([]string, []*net.IPNet) {
	list := make([]string, 0, len(entries))
	nets := make([]*net.IPNet, 0, len(entries))
	for _, entry := range entries {
		var ipNet *net.IPNet
		if strings.Contains(entry, "/") {
			_, n, err := net.ParseCIDR(entry)
			if err != nil {
				log.Printf("[WEB]: Ignoring invalid reverse proxy address '%s': %v", entry, err)
				continue
			}
			ipNet = n
		} else {
			ip := net.ParseIP(entry)
			if ip == nil {
				log.Printf("[WEB]: Ignoring invalid reverse proxy address '%s'", entry)
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				ipNet = &net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}
			} else {
				ipNet = &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
			}
		}
		list = append(list, entry)
		nets = append(nets, ipNet)
	}
	return list, nets
}

// isTrustedPeer reports whether the direct peer is a configured reverse proxy.
func (s *WebServer) isTrustedPeer(peer net.IP) bool {
	if peer == nil {
		return false
	}
	for _, ipNet := range s.trustedProxyNets {
		if ipNet.Contains(peer) {
			return true
		}
	}
	return false
}

// ReverseProxyMiddleware detects HTTPS terminated by a trusted reverse proxy.
// The client IP is resolved by gin (see configureTrustedProxies); RemoteAddr and Host are never rewritten.
func (s *WebServer) ReverseProxyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		peer := net.ParseIP(c.RemoteIP())
		isHTTPS := c.Request.TLS != nil ||
			(s.isTrustedPeer(peer) && strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https"))
		if isHTTPS {
			c.Request.URL.Scheme = "https"
		}
		// Expose scheme result to handlers
		c.Set("is_https", isHTTPS)
		c.Next()
	}
}

func (s *WebServer) ApacheLogFormat() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf(`%s - - [%s] "%s %s %s" %d %d "%s" "%s"`+"\n",
			param.ClientIP,
			param.TimeStamp.Format("02/Jan/2006:15:04:05 -0700"),
			param.Method,
			param.Path,
			param.Request.Proto,
			param.StatusCode,
			param.BodySize,
			param.Request.Referer(),
			param.Request.UserAgent(),
		)
	})
}

// loadSectionsCache populates the sections cache with valid section names from database
func (s *WebServer) loadSectionsCache() {
	sections, err := s.DB.GetAllSections()
	if err != nil {
		log.Printf("Warning: Failed to load sections cache: %v", err)
		return
	}

	// Build a new map and publish it atomically; readers keep the old map until then
	m := make(map[string]bool, len(sections))
	for _, section := range sections {
		m[section.Name] = true
	}
	s.sectionsCache.Store(&m)

	log.Printf("Loaded %d sections into cache", len(m))
}

// refreshSectionsCache reloads the sections cache
func (s *WebServer) refreshSectionsCache() {
	s.loadSectionsCache()
}

// isValidSection checks if a section name exists in the cache
func (s *WebServer) isValidSection(sectionName string) bool {
	p := s.sectionsCache.Load()
	return p != nil && (*p)[sectionName]
}

// knownNonSectionPaths are first path segments that are never section names
var knownNonSectionPaths = map[string]bool{
	"favicon.ico":      true,
	"robots.txt":       true,
	"static":           true,
	"admin":            true,
	"api":              true,
	"login":            true,
	"logout":           true,
	"register":         true,
	"profile":          true,
	"groups":           true,
	"hierarchies":      true,
	"hierarchy":        true,
	"search":           true,
	"stats":            true,
	"SiteHelp":         true,
	"SiteNews":         true,
	"sections":         true,
	"demo":             true,
	"ping":             true,
	"aichat":           true,
	"hierarchy-groups": true,
}

// sectionValidationMiddleware validates section names against the cache before routing
func (s *WebServer) sectionValidationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get the first path segment which should be the section name
		path := c.Request.URL.Path
		if path == "/" {
			c.Next()
			return
		}

		// Remove leading slash and get first segment
		pathSegments := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(pathSegments) == 0 {
			c.Next()
			return
		}

		potentialSection := pathSegments[0]

		// Skip validation for known non-section paths
		if knownNonSectionPaths[potentialSection] {
			c.Next()
			return
		}

		// Check if this is a valid section
		if !s.isValidSection(potentialSection) {
			// Invalid section
			//c.JSON(http.StatusNotFound, gin.H{"error": "501 wannaplay?"})
			c.Redirect(http.StatusSeeOther, "https://www.youtube.com/watch?v=e--fDKBAkZA")
			c.Abort()
			return
		}

		c.Next()
	}
}
