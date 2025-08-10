package nntp

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
)

// Default exclusion patterns for administrative and unwanted groups (INN2 $DEFAULT)
// These !patterns exclude specific groups but allow crossposted articles
var DefaultNoSendPatterns = []string{
	"!control", "!control.*", // Exclude control messages
	"!junk", "!junk.*", // Exclude junk/spam groups
	"!local", "!local.*", // Exclude local server groups
	"!ka.*", "!gmane.*", "!gwene.*", // Exclude gateway and mailing list groups
}

// Default exclusion patterns for binary groups (INN2 style)
// These @patterns will reject entire articles if any newsgroup matches
var DefaultBinaryExcludePatterns = []string{
	"@dk.b.*", "@*dvdnordic*",
	"@a.b.*", "@ab.alt.*", "@ab.mom*", "@alt.b.*",
	"@*alt-bin*", "@*alt.bin*", "@*alt.dvd*", "@*alt.hdtv*",
	"@*alt.binaries*", "@*alt.binaries.dvd*", "@*alt.binaries.hdtv*",
	"@*nairies*", "@*naries*", "@*.bain*", "@*.banar*", "@*.banir*",
	"@*.biana*", "@*.bianr*", "@*.biin*", "@*.binar*", "@*.binai*",
	"@*.binaer*", "@*.bineri*", "@*.biniar*", "@*.binira*",
	"@*.binrie*", "@*.biya*", "@*.boneles*", "@*cd.image*",
	"@*dateien*", "@*.files*", "@*.newfiles*", "@*music.bin*",
	"@*nzb*", "@*mp3*", "@*ictures*", "@*iktures*",
	"@*crack*", "@*serial*", "@*warez*",
	"@unidata.*",
}

var DefaultSexExcludePatterns = []string{
	"@*erotic*", "@*gay*", "@*paedo*", "@*pedo*", "@*porn*", "@*sex*", "@*xxx*",
}

// PeeringManager handles NNTP peer configuration and management
// This integrates with our existing config and database systems
type PeeringManager struct {
	mux sync.RWMutex

	// Core configuration (from our existing system)
	mainConfig *config.MainConfig
	dbConfig   *database.DBConfig

	// Peering-specific configuration
	peeringConfig *PeeringConfig

	// Active peers
	peers    []Peer
	peersMap map[string]*Peer // hostname -> peer

	// Statistics and monitoring
	stats PeeringStats

	// DNS rate limiting
	dnsQueryLimiter chan struct{}
}

// PeeringConfig holds NNTP peering configuration
// Extends our existing config system rather than replacing it
type PeeringConfig struct {
	// Server identification
	Hostname string `json:"hostname" yaml:"hostname"`

	// Connection management
	AcceptAllGroups  bool          `json:"accept_all_groups" yaml:"accept_all_groups"`
	AcceptMaxGroups  int           `json:"accept_max_groups" yaml:"accept_max_groups"`
	ConnectionDelay  time.Duration `json:"connection_delay" yaml:"connection_delay"`
	ReloadInterval   time.Duration `json:"reload_interval" yaml:"reload_interval"`
	MaxOutgoingFeeds int           `json:"max_outgoing_feeds" yaml:"max_outgoing_feeds"`
	MaxIncomingFeeds int           `json:"max_incoming_feeds" yaml:"max_incoming_feeds"`

	// Authentication settings
	AuthRequired      bool `json:"auth_required" yaml:"auth_required"`
	MinPasswordLength int  `json:"min_password_length" yaml:"min_password_length"`
	ListRequiresAuth  bool `json:"list_requires_auth" yaml:"list_requires_auth"`

	// DNS and network settings
	DNSQueryLimit      int           `json:"dns_query_limit" yaml:"dns_query_limit"`
	DNSQueryTimeout    time.Duration `json:"dns_query_timeout" yaml:"dns_query_timeout"`
	ForceConnectionACL bool          `json:"force_connection_acl" yaml:"force_connection_acl"`
}

// Peer represents a single NNTP peer configuration
type Peer struct {
	// Basic identification
	Enabled      bool   `json:"enabled" yaml:"enabled"`
	Hostname     string `json:"hostname" yaml:"hostname"`           // FQDN/RDNS for network connections (used as map key)
	PathHostname string `json:"path_hostname" yaml:"path_hostname"` // Hostname used in Path headers (can differ from network hostname)
	DisplayName  string `json:"display_name" yaml:"display_name"`   // Human-readable name
	Description  string `json:"description" yaml:"description"`     // Optional description

	// Network configuration
	Port        int    `json:"port" yaml:"port"`                 // Default: 119
	IPv4Address string `json:"ipv4_address" yaml:"ipv4_address"` // Static IPv4 for outgoing
	IPv6Address string `json:"ipv6_address" yaml:"ipv6_address"` // Static IPv6 for outgoing
	IPv4CIDR    string `json:"ipv4_cidr" yaml:"ipv4_cidr"`       // CIDR for incoming validation
	IPv6CIDR    string `json:"ipv6_cidr" yaml:"ipv6_cidr"`       // CIDR for incoming validation

	// Connection limits and performance
	MaxIncomingConns int   `json:"max_incoming_conns" yaml:"max_incoming_conns"` // Limit incoming from this peer
	MaxOutgoingConns int   `json:"max_outgoing_conns" yaml:"max_outgoing_conns"` // Limit outgoing to this peer
	SpeedLimitKBps   int64 `json:"speed_limit_kbps" yaml:"speed_limit_kbps"`     // Speed limit per connection

	// Authentication
	LocalUsername  string `json:"local_username" yaml:"local_username"`   // Username this peer must use to auth
	LocalPassword  string `json:"local_password" yaml:"local_password"`   // Password this peer must use to auth
	RemoteUsername string `json:"remote_username" yaml:"remote_username"` // Username we use when connecting
	RemotePassword string `json:"remote_password" yaml:"remote_password"` // Password we use when connecting

	// TLS/SSL configuration
	UseSSL      bool `json:"use_ssl" yaml:"use_ssl"`           // Connect using SSL/TLS
	SSLInsecure bool `json:"ssl_insecure" yaml:"ssl_insecure"` // Allow self-signed certs
	RequireSSL  bool `json:"require_ssl" yaml:"require_ssl"`   // Require SSL for incoming

	// Feed configuration (similar to INN2 newsfeeds patterns)
	SendPatterns    []string `json:"send_patterns" yaml:"send_patterns"`       // Newsgroup patterns to send
	AcceptPatterns  []string `json:"accept_patterns" yaml:"accept_patterns"`   // Newsgroup patterns to accept
	ExcludePatterns []string `json:"exclude_patterns" yaml:"exclude_patterns"` // !patterns - exclude specific groups from send
	RejectPatterns  []string `json:"reject_patterns" yaml:"reject_patterns"`   // @patterns - reject entire article if any group matches

	// Feed behavior
	ReadOnlyAccess bool   `json:"read_only_access" yaml:"read_only_access"` // Peer can only read, not post
	Streaming      bool   `json:"streaming" yaml:"streaming"`               // Use streaming mode (CHECK/TAKETHIS)
	MaxArticleSize int64  `json:"max_article_size" yaml:"max_article_size"` // Maximum article size
	FeedMode       string `json:"feed_mode" yaml:"feed_mode"`               // "realtime", "batch", "pull"

	// Statistics and monitoring
	ArticlesSent     int64     `json:"articles_sent" yaml:"-"`
	ArticlesReceived int64     `json:"articles_received" yaml:"-"`
	BytesSent        int64     `json:"bytes_sent" yaml:"-"`
	BytesReceived    int64     `json:"bytes_received" yaml:"-"`
	LastConnected    time.Time `json:"last_connected" yaml:"-"`
	LastError        string    `json:"last_error" yaml:"-"`
	ErrorCount       int64     `json:"error_count" yaml:"-"`

	// Internal state
	ActiveIncoming   int       `json:"-" yaml:"-"` // Current incoming connections
	ActiveOutgoing   int       `json:"-" yaml:"-"` // Current outgoing connections
	LastConfigReload time.Time `json:"-" yaml:"-"` // When config was last reloaded
}

// PeeringStats holds statistics for the peering system
type PeeringStats struct {
	TotalPeers         int
	EnabledPeers       int
	ActiveConnections  int
	TotalArticlesSent  int64
	TotalArticlesRecvd int64
	TotalBytesSent     int64
	TotalBytesRecvd    int64
	LastReload         time.Time
}

// NewPeeringManager creates a new peering manager
func NewPeeringManager(mainConfig *config.MainConfig, dbConfig *database.DBConfig) *PeeringManager {
	return &PeeringManager{
		mainConfig:      mainConfig,
		dbConfig:        dbConfig,
		peeringConfig:   DefaultPeeringConfig(),
		peersMap:        make(map[string]*Peer),
		stats:           PeeringStats{},
		dnsQueryLimiter: make(chan struct{}, 1), // Limit DNS queries to 1 at a time
	}
}

// DefaultPeeringConfig returns sensible defaults for peering
func DefaultPeeringConfig() *PeeringConfig {
	return &PeeringConfig{
		Hostname:           "localhost",
		AcceptAllGroups:    false,
		AcceptMaxGroups:    100,
		ConnectionDelay:    5 * time.Second,
		ReloadInterval:     60 * time.Second,
		MaxOutgoingFeeds:   50,
		MaxIncomingFeeds:   100,
		AuthRequired:       true,
		MinPasswordLength:  8,
		ListRequiresAuth:   true,
		DNSQueryLimit:      10,
		DNSQueryTimeout:    30 * time.Second,
		ForceConnectionACL: true,
	}
}

// LoadConfiguration loads peering configuration from database or file
func (pm *PeeringManager) LoadConfiguration() error {
	pm.mux.Lock()
	defer pm.mux.Unlock()

	// TODO: Load from database first, then fall back to file
	// For now, return success with default config
	log.Printf("PeeringManager: Using default configuration")
	pm.stats.LastReload = time.Now()
	return nil
}

// ValidatePeerConfig validates a peer configuration
func (pm *PeeringManager) ValidatePeerConfig(peer *Peer) error {
	if peer.Hostname == "" {
		return fmt.Errorf("peer hostname cannot be empty")
	}

	if peer.Port <= 0 || peer.Port > 65535 {
		return fmt.Errorf("invalid port number: %d", peer.Port)
	}

	// Validate IPv4 address if provided
	if peer.IPv4Address != "" {
		if net.ParseIP(peer.IPv4Address) == nil {
			return fmt.Errorf("invalid IPv4 address: %s", peer.IPv4Address)
		}
	}

	// Validate IPv6 address if provided
	if peer.IPv6Address != "" {
		if net.ParseIP(peer.IPv6Address) == nil {
			return fmt.Errorf("invalid IPv6 address: %s", peer.IPv6Address)
		}
	}

	// Validate CIDR blocks
	if peer.IPv4CIDR != "" {
		if _, _, err := net.ParseCIDR(peer.IPv4CIDR); err != nil {
			return fmt.Errorf("invalid IPv4 CIDR: %s", peer.IPv4CIDR)
		}
	}

	if peer.IPv6CIDR != "" {
		if _, _, err := net.ParseCIDR(peer.IPv6CIDR); err != nil {
			return fmt.Errorf("invalid IPv6 CIDR: %s", peer.IPv6CIDR)
		}
	}

	return nil
}

// AddPeer adds a new peer to the configuration
func (pm *PeeringManager) AddPeer(peer *Peer) error {
	if err := pm.ValidatePeerConfig(peer); err != nil {
		return fmt.Errorf("invalid peer configuration: %w", err)
	}

	pm.mux.Lock()
	defer pm.mux.Unlock()

	// Check for duplicate hostname
	if _, exists := pm.peersMap[peer.Hostname]; exists {
		return fmt.Errorf("peer with hostname %s already exists", peer.Hostname)
	}

	// Add to peers slice and map
	pm.peers = append(pm.peers, *peer)
	pm.peersMap[peer.Hostname] = &pm.peers[len(pm.peers)-1]

	pm.updateStats()
	log.Printf("PeeringManager: Added peer %s", peer.Hostname)
	return nil
}

// GetPeer retrieves a peer by hostname
func (pm *PeeringManager) GetPeer(hostname string) (*Peer, bool) {
	pm.mux.RLock()
	defer pm.mux.RUnlock()

	peer, exists := pm.peersMap[hostname]
	return peer, exists
}

// CheckConnectionACL validates an incoming connection against peer ACLs
func (pm *PeeringManager) CheckConnectionACL(conn net.Conn) (*Peer, bool) {
	remoteAddr, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		log.Printf("PeeringManager: Invalid remote address: %v", err)
		return nil, false
	}

	pm.mux.RLock()
	defer pm.mux.RUnlock()

	// Check each enabled peer
	for _, peer := range pm.peers {
		if !peer.Enabled {
			continue
		}

		// Check static IP addresses first
		if peer.IPv4Address == remoteAddr || peer.IPv6Address == remoteAddr {
			log.Printf("PeeringManager: Connection from %s matched peer %s (static IP)", remoteAddr, peer.Hostname)
			return &peer, true
		}

		// Check CIDR ranges
		if pm.matchesCIDR(remoteAddr, peer.IPv4CIDR) || pm.matchesCIDR(remoteAddr, peer.IPv6CIDR) {
			log.Printf("PeeringManager: Connection from %s matched peer %s (CIDR)", remoteAddr, peer.Hostname)
			return &peer, true
		}
	}

	// If no static match, try reverse DNS lookup
	if hostname, ok := pm.reverseDNSLookup(remoteAddr); ok {
		if peer, exists := pm.peersMap[hostname]; exists && peer.Enabled {
			log.Printf("PeeringManager: Connection from %s matched peer %s (reverse DNS)", remoteAddr, hostname)
			return peer, true
		}
	}

	log.Printf("PeeringManager: Connection from %s denied (no matching peer)", remoteAddr)
	return nil, false
}

// matchesCIDR checks if an IP address matches a CIDR block
func (pm *PeeringManager) matchesCIDR(ipAddress, cidr string) bool {
	if cidr == "" {
		return false
	}

	ip := net.ParseIP(ipAddress)
	if ip == nil {
		return false
	}

	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}

	return ipNet.Contains(ip)
}

// reverseDNSLookup performs reverse DNS lookup with timeout and validation
func (pm *PeeringManager) reverseDNSLookup(ipAddress string) (string, bool) {
	// Rate limit DNS queries
	if pm.dnsQueryLimiter != nil {
		pm.dnsQueryLimiter <- struct{}{}
		defer func() { <-pm.dnsQueryLimiter }()
	}

	hosts, err := net.LookupAddr(ipAddress)
	if err != nil || len(hosts) == 0 {
		log.Printf("ERROR reverseDNSLookup LookupAddr err='%v'", err)
		return "", false
	}

	// Validate each hostname to prevent DNS spoofing
	for _, hostname := range hosts {
		hostname = strings.TrimSuffix(hostname, ".")
		if pm.validateHostnameForward(hostname, ipAddress) {
			return hostname, true
		}
	}

	return "", false
}

// Helper functions migrated from legacy peering system

// RemoteAddr extracts the IP address from a connection
func RemoteAddr(conn net.Conn) string {
	remoteAddr, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		log.Printf("ERROR RemoteAddr net.SplitHostPort err='%v'", err)
		return ""
	}
	return remoteAddr
}

// StrIsIPv4 checks if a string is a valid IPv4 address
func StrIsIPv4(address string) bool {
	testInput := net.ParseIP(address)
	return testInput != nil && testInput.To4() != nil
}

// StrIsIPv6 checks if a string contains IPv6 format (simple check)
func StrIsIPv6(address string) bool {
	return strings.Contains(address, ":")
}

// Enhanced reverseDNSLookup with rate limiting and validation
func (pm *PeeringManager) enhancedReverseDNSLookup(remoteAddr string) (string, bool) {
	// Rate limit DNS queries
	if pm.dnsQueryLimiter != nil {
		pm.dnsQueryLimiter <- struct{}{}
		defer func() { <-pm.dnsQueryLimiter }()
	}

	hosts, err := net.LookupAddr(remoteAddr)
	if err != nil {
		log.Printf("ERROR reverseDNSLookup LookupAddr err='%v'", err)
		return "", false
	}

	for _, hostname := range hosts {
		hostname = strings.TrimSuffix(hostname, ".")
		if pm.validateHostnameForward(hostname, remoteAddr) {
			return hostname, true
		}
	}

	return "", false
}

// validateHostnameForward validates that a hostname resolves to the expected IP
func (pm *PeeringManager) validateHostnameForward(hostname string, expectedAddr string) bool {
	// Rate limit DNS queries
	if pm.dnsQueryLimiter != nil {
		pm.dnsQueryLimiter <- struct{}{}
		defer func() { <-pm.dnsQueryLimiter }()
	}

	addrs, err := net.LookupHost(hostname)
	if err != nil {
		log.Printf("ERROR validateHostnameForward LookupHost err='%v'", err)
		return false
	}

	for _, addr := range addrs {
		if addr == expectedAddr {
			return true
		}
	}
	return false
}

// updateStats recalculates peering statistics
func (pm *PeeringManager) updateStats() {
	pm.stats.TotalPeers = len(pm.peers)
	pm.stats.EnabledPeers = 0
	pm.stats.TotalArticlesSent = 0
	pm.stats.TotalArticlesRecvd = 0
	pm.stats.TotalBytesSent = 0
	pm.stats.TotalBytesRecvd = 0

	for _, peer := range pm.peers {
		if peer.Enabled {
			pm.stats.EnabledPeers++
		}
		pm.stats.TotalArticlesSent += peer.ArticlesSent
		pm.stats.TotalArticlesRecvd += peer.ArticlesReceived
		pm.stats.TotalBytesSent += peer.BytesSent
		pm.stats.TotalBytesRecvd += peer.BytesReceived
	}
}

// GetStats returns current peering statistics
func (pm *PeeringManager) GetStats() PeeringStats {
	pm.mux.RLock()
	defer pm.mux.RUnlock()

	pm.updateStats()
	return pm.stats
}

// GetAllPeers returns a copy of all peers
func (pm *PeeringManager) GetAllPeers() []Peer {
	pm.mux.RLock()
	defer pm.mux.RUnlock()

	peers := make([]Peer, len(pm.peers))
	copy(peers, pm.peers)
	return peers
}

// Close gracefully shuts down the peering manager
func (pm *PeeringManager) Close() error {
	pm.mux.Lock()
	defer pm.mux.Unlock()

	log.Printf("PeeringManager: Shutting down with %d peers", len(pm.peers))
	return nil
}

// ApplyDefaultExclusions applies both default exclusion patterns and binary rejection patterns to a peer
func (pm *PeeringManager) ApplyDefaultExclusions(peer *Peer) {
	// Apply default exclusion patterns (! patterns) to ExcludePatterns
	if peer.ExcludePatterns == nil {
		peer.ExcludePatterns = make([]string, 0)
	}

	existingExcludePatterns := make(map[string]bool)
	for _, pattern := range peer.ExcludePatterns {
		existingExcludePatterns[pattern] = true
	}

	for _, defaultPattern := range DefaultNoSendPatterns {
		if !existingExcludePatterns[defaultPattern] {
			peer.ExcludePatterns = append(peer.ExcludePatterns, defaultPattern)
		}
	}

	// Apply binary exclusions (@ patterns) to RejectPatterns
	if peer.RejectPatterns == nil {
		peer.RejectPatterns = make([]string, 0)
	}

	existingRejects := make(map[string]bool)
	for _, pattern := range peer.RejectPatterns {
		existingRejects[pattern] = true
	}

	for _, defaultPattern := range DefaultBinaryExcludePatterns {
		if !existingRejects[defaultPattern] {
			peer.RejectPatterns = append(peer.RejectPatterns, defaultPattern)
		}
	}
}

// ApplyDefaultBinaryExclusions applies the default binary exclusion patterns to a peer
func (pm *PeeringManager) ApplyDefaultBinaryExclusions(peer *Peer) {
	if peer.RejectPatterns == nil {
		peer.RejectPatterns = make([]string, 0)
	}

	// Add default binary exclusions if not already present
	existingPatterns := make(map[string]bool)
	for _, pattern := range peer.RejectPatterns {
		existingPatterns[pattern] = true
	}

	for _, defaultPattern := range DefaultBinaryExcludePatterns {
		if !existingPatterns[defaultPattern] {
			peer.RejectPatterns = append(peer.RejectPatterns, defaultPattern)
		}
	}
}

// CreateDefaultPeer creates a peer with sensible defaults including default exclusions and binary rejections
func (pm *PeeringManager) CreateDefaultPeer(hostname, pathHostname string) *Peer {
	peer := &Peer{
		Enabled:      false, // Start disabled for safety
		Hostname:     hostname,
		PathHostname: pathHostname,
		Port:         119,

		// Default patterns - use INN2 style: "*,$DEFAULT,$NOBINARY"
		SendPatterns:    []string{"*"},                                     // Send all groups (equivalent to "*" in INN2)
		AcceptPatterns:  []string{"*"},                                     // Accept everything by default
		ExcludePatterns: make([]string, len(DefaultNoSendPatterns)),        // Apply $DEFAULT exclusions
		RejectPatterns:  make([]string, len(DefaultBinaryExcludePatterns)), // Apply $NOBINARY rejections

		// Conservative defaults
		ReadOnlyAccess:   true,  // Start read-only for safety
		Streaming:        false, // Traditional mode first
		MaxIncomingConns: 5,
		MaxOutgoingConns: 5,
		MaxArticleSize:   1024 * 1024, // 1MB limit
		FeedMode:         "realtime",
	}

	// Copy default patterns
	copy(peer.ExcludePatterns, DefaultNoSendPatterns)
	copy(peer.RejectPatterns, DefaultBinaryExcludePatterns)

	return peer
}
