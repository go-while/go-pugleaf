package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adminCreateProvider handles provider creation
func (s *WebServer) adminCreateProvider(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	// Get form data
	name := strings.TrimSpace(c.PostForm("name"))
	grp := strings.TrimSpace(c.PostForm("grp"))
	host := strings.TrimSpace(c.PostForm("host"))
	portStr := strings.TrimSpace(c.PostForm("port"))
	sslStr := c.PostForm("ssl")
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	maxConnsStr := strings.TrimSpace(c.PostForm("max_conns"))
	priorityStr := strings.TrimSpace(c.PostForm("priority"))
	maxArtSizeStr := strings.TrimSpace(c.PostForm("max_art_size"))
	enabledStr := c.PostForm("enabled")
	postingEnabledStr := c.PostForm("posting_enabled")

	// Get proxy form data
	proxyEnabledStr := c.PostForm("proxy_enabled")
	proxyType := strings.TrimSpace(c.PostForm("proxy_type"))
	proxyHost := strings.TrimSpace(c.PostForm("proxy_host"))
	proxyPortStr := strings.TrimSpace(c.PostForm("proxy_port"))
	proxyUsername := strings.TrimSpace(c.PostForm("proxy_username"))
	proxyPassword := c.PostForm("proxy_password")

	// Validate required fields
	if name == "" || host == "" {
		session.SetError("Provider name and host are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	// Parse port
	port := 119 // Default NNTP port
	if portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			session.SetError("Invalid port number")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Parse SSL
	ssl := sslStr == "on" || sslStr == "true"

	// Parse max connections
	maxConns := 1
	if maxConnsStr != "" {
		maxConns, err = strconv.Atoi(maxConnsStr)
		if err != nil || maxConns < 1 {
			session.SetError("Invalid max connections")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Parse priority
	priority := 1
	if priorityStr != "" {
		priority, err = strconv.Atoi(priorityStr)
		if err != nil || priority < 1 {
			session.SetError("Invalid priority")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Parse max article size
	maxArtSize := 0
	if maxArtSizeStr != "" {
		maxArtSize, err = strconv.Atoi(maxArtSizeStr)
		if err != nil || maxArtSize < 0 {
			session.SetError("Invalid max article size")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Parse enabled status
	enabled := enabledStr == "on" || enabledStr == "true"

	// Parse posting enabled status
	postingEnabled := postingEnabledStr == "on" || postingEnabledStr == "true"

	// Parse proxy settings
	proxyEnabled := proxyEnabledStr == "on" || proxyEnabledStr == "true"

	// Parse proxy port
	proxyPort := 0
	if proxyPortStr != "" {
		proxyPort, err = strconv.Atoi(proxyPortStr)
		if err != nil || proxyPort < 0 || proxyPort > 65535 {
			session.SetError("Invalid proxy port number")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Validate proxy configuration if enabled
	if proxyEnabled {
		// Validate proxy type
		if proxyType != "socks4" && proxyType != "socks5" {
			session.SetError("Invalid proxy type. Must be socks4 or socks5")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}

		// Validate host and port for proxy
		if proxyHost == "" {
			session.SetError("Proxy host is required for proxy type " + proxyType)
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
		if proxyPort <= 0 || proxyPort > 65535 {
			session.SetError(fmt.Sprintf("Invalid proxy port %d for proxy type %s", proxyPort, proxyType))
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Check if provider already exists
	res, err := s.DB.GetProviderByName(name)
	if err != nil {
		log.Printf("Error checking provider existence: %v res='%v'", err, res)
		session.SetError("Provider already exists '" + name + "'")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	// Create provider
	provider := &models.Provider{
		Name:          name,
		Grp:           grp,
		Host:          host,
		Port:          port,
		SSL:           ssl,
		Username:      username,
		Password:      password,
		MaxConns:      maxConns,
		Priority:      priority,
		MaxArtSize:    maxArtSize,
		Enabled:       enabled,
		Posting:       postingEnabled,
		ProxyEnabled:  proxyEnabled,
		ProxyType:     proxyType,
		ProxyHost:     proxyHost,
		ProxyPort:     proxyPort,
		ProxyUsername: proxyUsername,
		ProxyPassword: proxyPassword,
		CreatedAt:     time.Now(),
	}

	err = s.DB.AddProvider(provider)
	if err != nil {
		session.SetError("Failed to create provider")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	session.SetSuccess("Provider created successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
}

// adminUpdateProvider handles provider updates
func (s *WebServer) adminUpdateProvider(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	// Get form data
	idStr := strings.TrimSpace(c.PostForm("id"))
	name := strings.TrimSpace(c.PostForm("name"))
	grp := strings.TrimSpace(c.PostForm("grp"))
	host := strings.TrimSpace(c.PostForm("host"))
	portStr := strings.TrimSpace(c.PostForm("port"))
	sslStr := c.PostForm("ssl")
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	clearUsernameStr := c.PostForm("clear_username")
	clearPasswordStr := c.PostForm("clear_password")
	maxConnsStr := strings.TrimSpace(c.PostForm("max_conns"))
	priorityStr := strings.TrimSpace(c.PostForm("priority"))
	maxArtSizeStr := strings.TrimSpace(c.PostForm("max_art_size"))
	enabledStr := c.PostForm("enabled")
	postingEnabledStr := c.PostForm("posting_enabled")

	// Get proxy form data
	proxyEnabledStr := c.PostForm("proxy_enabled")
	proxyType := strings.TrimSpace(c.PostForm("proxy_type"))
	proxyHost := strings.TrimSpace(c.PostForm("proxy_host"))
	proxyPortStr := strings.TrimSpace(c.PostForm("proxy_port"))
	proxyUsername := strings.TrimSpace(c.PostForm("proxy_username"))
	proxyPassword := c.PostForm("proxy_password")
	clearProxyUsernameStr := c.PostForm("clear_proxy_username")
	clearProxyPasswordStr := c.PostForm("clear_proxy_password")

	// Validate required fields
	if idStr == "" || name == "" || host == "" {
		session.SetError("Provider ID, name and host are required")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	// Parse ID
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		session.SetError("Invalid provider ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	// Parse port
	port := 119
	if portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			session.SetError("Invalid port number")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Parse SSL
	ssl := sslStr == "on" || sslStr == "true"

	// Parse max connections
	maxConns := 1
	if maxConnsStr != "" {
		maxConns, err = strconv.Atoi(maxConnsStr)
		if err != nil || maxConns < 1 {
			session.SetError("Invalid max connections")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Parse priority
	priority := 1
	if priorityStr != "" {
		priority, err = strconv.Atoi(priorityStr)
		if err != nil || priority < 1 {
			session.SetError("Invalid priority")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Parse max article size
	maxArtSize := 0
	if maxArtSizeStr != "" {
		maxArtSize, err = strconv.Atoi(maxArtSizeStr)
		if err != nil || maxArtSize < 0 {
			session.SetError("Invalid max article size")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Parse enabled status
	enabled := enabledStr == "on" || enabledStr == "true"

	// Parse posting enabled status
	postingEnabled := postingEnabledStr == "on" || postingEnabledStr == "true"

	// Parse proxy settings
	proxyEnabled := proxyEnabledStr == "on" || proxyEnabledStr == "true"

	// Parse proxy port
	proxyPort := 0
	if proxyPortStr != "" {
		proxyPort, err = strconv.Atoi(proxyPortStr)
		if err != nil || proxyPort < 0 || proxyPort > 65535 {
			session.SetError("Invalid proxy port number")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Validate proxy configuration if enabled
	if proxyEnabled {
		if proxyHost == "" || proxyPort == 0 {
			session.SetError("ERROR: Proxy host and port are required when proxy is enabled")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
		if proxyPort <= 0 || proxyPort > 65535 {
			session.SetError("ERROR: Invalid proxy port number")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}

		if proxyType == "" {
			session.SetError("ERROR: Proxy type is not set.")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	// Handle username and password clearing/preservation
	clearUsername := clearUsernameStr == "on" || clearUsernameStr == "true"
	clearPassword := clearPasswordStr == "on" || clearPasswordStr == "true"

	// Get existing provider to preserve current credentials if needed
	existingProvider, err := s.DB.GetProviderByID(id)
	if err != nil {
		session.SetError("Failed to get existing provider")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	// Handle username: clear if checkbox is checked, otherwise use form value or preserve existing
	finalUsername := username
	if clearUsername {
		finalUsername = ""
	} else if username == "" && existingProvider != nil {
		finalUsername = existingProvider.Username
	}

	// Handle password: clear if checkbox is checked, otherwise use form value or preserve existing
	finalPassword := password
	if clearPassword {
		finalPassword = ""
	} else if password == "" && existingProvider != nil {
		finalPassword = existingProvider.Password
	}

	// Handle proxy credentials clearing/preservation
	clearProxyUsername := clearProxyUsernameStr == "on" || clearProxyUsernameStr == "true"
	clearProxyPassword := clearProxyPasswordStr == "on" || clearProxyPasswordStr == "true"

	// Handle proxy username: clear if checkbox is checked, otherwise use form value or preserve existing
	finalProxyUsername := proxyUsername
	if clearProxyUsername {
		finalProxyUsername = ""
	} else if proxyUsername == "" && existingProvider != nil {
		finalProxyUsername = existingProvider.ProxyUsername
	}

	// Handle proxy password: clear if checkbox is checked, otherwise use form value or preserve existing
	finalProxyPassword := proxyPassword
	if clearProxyPassword {
		finalProxyPassword = ""
	} else if proxyPassword == "" && existingProvider != nil {
		finalProxyPassword = existingProvider.ProxyPassword
	}

	// Create provider struct for update
	provider := &models.Provider{
		ID:            id,
		Name:          name,
		Grp:           grp,
		Host:          host,
		Port:          port,
		SSL:           ssl,
		Username:      finalUsername,
		Password:      finalPassword,
		MaxConns:      maxConns,
		Priority:      priority,
		MaxArtSize:    maxArtSize,
		Enabled:       enabled,
		Posting:       postingEnabled,
		ProxyEnabled:  proxyEnabled,
		ProxyType:     proxyType,
		ProxyHost:     proxyHost,
		ProxyPort:     proxyPort,
		ProxyUsername: finalProxyUsername,
		ProxyPassword: finalProxyPassword,
	}

	// Validate proxy configuration if enabled
	if provider.ProxyEnabled {
		// Validate proxy type
		if proxyType != "socks4" && proxyType != "socks5" {
			session.SetError("Invalid proxy type. Must be socks4 or socks5")
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}

		// Validate host and port for proxy
		if proxyHost == "" {
			session.SetError("Proxy host is required for proxy type " + proxyType)
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
		if proxyPort <= 0 || proxyPort > 65535 {
			session.SetError(fmt.Sprintf("Invalid proxy port %d for proxy type %s", proxyPort, proxyType))
			c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
			return
		}
	}

	err = s.DB.SetProvider(provider)
	if err != nil {
		session.SetError("Failed to update provider")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	session.SetSuccess("Provider updated successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
}

// adminDeleteProvider handles provider deletion
func (s *WebServer) adminDeleteProvider(c *gin.Context) {
	// Check authentication and admin permissions
	session := s.getWebSession(c)
	if session == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}

	currentUser, err := s.DB.GetUserByID(int64(session.UserID))
	if err != nil || !s.isAdmin(currentUser) {
		session.SetError("Access denied")
		c.Redirect(http.StatusSeeOther, "/profile")
		return
	}

	// Get provider ID
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" {
		session.SetError("Invalid provider ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		session.SetError("Invalid provider ID")
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	// Delete provider (we need to add DeleteProvider function to queries.go)
	err = s.DB.DeleteProvider(id)
	if err != nil {
		log.Printf("Error deleting provider ID %d: %v", id, err)
		session.SetError("Failed to delete provider id=" + idStr)
		c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
		return
	}

	session.SetSuccess("Provider deleted successfully")
	c.Redirect(http.StatusSeeOther, "/admin?tab=providers")
}
