// Package config provides configuration management for go-pugleaf.
// Adapted from NZBreX for newsgroup server use.
package config

import (
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

var AppVersion = "-unset-" // will be set at build time

const (
	// NNTP protocol constants
	DOT  = "."
	CR   = "\r"
	LF   = "\n"
	CRLF = CR + LF

	// Default connection settings
	DefaultConnectTimeout  = 30 * time.Second
	DefaultConnectErrSleep = 5 * time.Second
	DefaultRequeueDelay    = 10 * time.Second
	DefaultMaxArticleSize  = 32 * 1024 // 'N' KB max article size

	// NNTPServer defaults
	NNTPServerMaxConns = 500 // Maximum concurrent NNTP connections
)

// Global bot configuration variables (populated from database on startup)
var Default_BadBots []string
var BlockBadBots bool
var BadBotsMutex sync.RWMutex

// Global IP blocking configuration variables (populated from database on startup)
var Default_BlockedIPs []*net.IPNet
var BlockBadIPs bool
var BadIPsMutex sync.RWMutex

// Config database keys
const CFG_KEY_HOSTNAME string = "local_nntp_hostname"
const CFG_KEY_WEBPOSTSIZE string = "WebPostMaxArticleSize"
const CFG_KEY_ABUSEMAIL string = "AbuseMail"
const CFG_KEY_WEBLOCALNNTP string = "WebLocalNNTPServerAddrInfo"
const CFG_KEY_REVERSEPROXY string = "ReverseProxyAddr"
const CFG_KEY_REGISTRATION string = "registration_enabled"
const CFG_KEY_USERSALT string = "UserSalt"
const CFG_KEY_BADBOTS string = "BadBots"
const CFG_KEY_BLOCKBADBOTS string = "BlockBadBots"
const CFG_KEY_BADIPS string = "BadIPs"
const CFG_KEY_BLOCKBADIPS string = "BlockBadIPs"
const CFG_KEY_API_ENABLED string = "APIEnabled"

// Admin settings form field names
const FORM_FIELD_HOSTNAME string = "local_nntp_hostname"
const FORM_FIELD_WEBPOSTSIZE string = "web_post_size"
const FORM_FIELD_ABUSEMAIL string = "abuse_mail"
const FORM_FIELD_WEBLOCALNNTP string = "web_local_nntp_server_addr_info"
const FORM_FIELD_REVERSEPROXY string = "reverse_proxy_addr"
const FORM_FIELD_REGISTRATION string = "registration_toggle"
const FORM_FIELD_BADBOTS string = "bad_bots"
const FORM_FIELD_BLOCKBADBOTS string = "block_bad_bots"
const FORM_FIELD_BADIPS string = "bad_ips"
const FORM_FIELD_BLOCKBADIPS string = "block_bad_ips"
const FORM_FIELD_API_ENABLED string = "api_toogle"

// Config holds the main configuration for go-pugleaf
type MainConfig struct {
	MaxArtSize int `json:"max_article_size"`

	// Mutex for thread-safe access
	mux sync.Mutex `json:"-"`

	// NNTP Provider configurations
	Providers []Provider `json:"providers"`

	// Server settings
	Server ServerConfig `json:"server"`

	// Database settings
	Database DatabaseConfig `json:"database"`

	// Web interface settings
	Web WebConfig `json:"web"`

	AppVersion string `json:"app_version"` // Application version, set at build time
}

// Provider represents an NNTP server configuration
type Provider struct {
	Grp        string `json:"grp"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	SSL        bool   `json:"ssl"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	MaxConns   int    `json:"max_connections"`
	Enabled    bool   `json:"enabled"`
	Priority   int    `json:"priority"`         // Lower numbers = higher priority
	MaxArtSize int    `json:"max_article_size"` // Maximum article size in bytes
	Posting    bool   `json:"posting"`          // Whether posting is enabled for this provider
	// Proxy configuration fields
	ProxyEnabled  bool   `json:"proxy_enabled"`  // Whether to use proxy for this provider
	ProxyType     string `json:"proxy_type"`     // Proxy type: socks4, socks5
	ProxyHost     string `json:"proxy_host"`     // Proxy server hostname/IP
	ProxyPort     int    `json:"proxy_port"`     // Proxy server port
	ProxyUsername string `json:"proxy_username"` // Proxy authentication username
	ProxyPassword string `json:"proxy_password"` // Proxy authentication password
}

// ServerConfig holds Web and NNTP server configuration
type ServerConfig struct {
	WEB      *WebConfig
	Hostname string `json:"hostname"` // Server hostname for NNTP Path headers and identification
	// NNTP server-specific configuration
	NNTP struct {
		Enabled    bool   `json:"enabled"`
		Port       int    `json:"port"`
		TLSPort    int    `json:"tls_port"`
		MaxConns   int    `json:"max_connections"`
		TLSCert    string `json:"tls_cert"`
		TLSKey     string `json:"tls_key"`
		MaxArtSize int    `json:"max_article_size"` // Maximum article size in bytes
	} `json:"nntp"`
}

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	MainDB    string `json:"main_db"`    // Path to main database
	GroupsDir string `json:"groups_dir"` // Directory for per-group databases
	BackupDir string `json:"backup_dir"` // Directory for backups
}

// WebConfig holds web interface configuration
type WebConfig struct {
	ListenPort int    `json:"listen_port"`
	SSL        bool   `json:"ssl"`
	CertFile   string `json:"cert_file,omitempty"`
	KeyFile    string `json:"key_file,omitempty"`
	StaticDir  string `json:"static_dir"`
	Debug      bool   `json:"debug"`     // Enable debug logging for sessions/auth
	CronEdit   bool   `json:"cron_edit"` // Allow editing cron jobs via web interface
}

var DefaultProviders = []Provider{
	{
		Grp:          "localhost",
		Name:         "localhost",
		Host:         "localhost",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     100,
		Enabled:      false,
		Priority:     97,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Archive",
		Name:         "pugleaf Archive",
		Host:         "81-171-22-215.pugleaf.net",
		Username:     "pugleaf",
		Password:     "rslight",
		Port:         563,
		SSL:          true,
		MaxConns:     50,
		Enabled:      false,
		Priority:     98,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - Washington DC",
		Host:         "news-wdc.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-wdc",
		Host:         "tws5lk6q4k343oxnvp5ejnxldhkpaolll6wl6yo5etcb3opq4u2333id.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - San Francisco",
		Host:         "news-sfo.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-sfo",
		Host:         "mlmwfop6ysdohrlidazpramtkjeanznrpuj2o54jaxfew7ubqf5gbxqd.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - Los Angeles",
		Host:         "news-lax.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-lax",
		Host:         "derpefchhgbmou6nivzq2ajnkegpguk25xc654meujs5ldu6x7avl3id.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - Canada",
		Host:         "news-can.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-can",
		Host:         "pkq37mrydcjc4hkv2pu6alvad2f2iysnyffwbkquj5twzi7lshwje7id.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - Tokyo",
		Host:         "news-tyo.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-tyo",
		Host:         "h7yirgkelxgefct6qrn7esakg7cxjurmttnel2hkhp2lhojr6h5d7kad.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - Singapore",
		Host:         "news-sin.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-sin",
		Host:         "k2jncpwygu7u6p24aol6zxs36jhnyrvey2kjjpq3ktpzr433v7texvad.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - Frankfurt",
		Host:         "news-fra.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-fra",
		Host:         "daizdh5dzadcx4v56vwilbpw76y4fnfhkutn3pspbkklxp3swkg4oaqd.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - Amsterdam",
		Host:         "news-ams.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-ams",
		Host:         "apjhyuwozpsrxhudiefxp547dpv2u46xowo4l6ufpqgek2xu5irot5yd.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Secondary",
		Name:         "pugleaf - London",
		Host:         "news-lon.pugleaf.net",
		Username:     "",
		Password:     "",
		Port:         563,
		SSL:          true,
		MaxConns:     3,
		Enabled:      false,
		Priority:     99,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Secondary",
		Name:         "TOR: news-lon",
		Host:         "iat3kqk4wx75ai24rjz7u5lqr6xwbgp7gvqwt5t2kcb74wtkpamfk6ad.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Priority:     100,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "Pub",
		Name:         "news.tcpreset.net - Germany",
		Host:         "news.tcpreset.net",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Posting:      true,
		Priority:     101,
		MaxArtSize:   32768,
		ProxyEnabled: false,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:          "TOR Pub",
		Name:         "TOR: tcpreset",
		Host:         "peannyjkqwqfynd24p6dszvtchkq7hfkwymi5by5y332wmosy5dwfaqd.onion",
		Username:     "",
		Password:     "",
		Port:         119,
		SSL:          false,
		MaxConns:     3,
		Enabled:      false,
		Posting:      true,
		Priority:     102,
		MaxArtSize:   32768,
		ProxyEnabled: true,
		ProxyType:    "socks5",
		ProxyHost:    "127.0.0.1",
		ProxyPort:    9050,
	},
	{
		Grp:        "Backup",
		Name:       "BlueWorldHosting Archive",
		Host:       "news.blueworldhosting.com",
		Username:   "",
		Password:   "",
		Port:       563,
		SSL:        true,
		MaxConns:   3,
		Enabled:    false,
		Priority:   199,
		MaxArtSize: 32768,
	},
}

// NewDefaultConfig returns a configuration with sensible defaults
func NewDefaultConfig() *MainConfig {
	if AppVersion == "-unset-" {
		log.Fatalf("config.AppVersion is unset")
	}
	maincfg := &MainConfig{
		AppVersion: AppVersion, // Set application version

		Server: ServerConfig{
			WEB: &WebConfig{
				ListenPort: 11980,
				SSL:        false,
				StaticDir:  "./web/static",
			},
			NNTP: struct {
				Enabled    bool   `json:"enabled"`
				Port       int    `json:"port"`
				TLSPort    int    `json:"tls_port"`
				MaxConns   int    `json:"max_connections"`
				TLSCert    string `json:"tls_cert"`
				TLSKey     string `json:"tls_key"`
				MaxArtSize int    `json:"max_article_size"`
			}{
				Enabled:    true,
				Port:       1119,
				TLSPort:    1563,
				MaxConns:   NNTPServerMaxConns,
				TLSCert:    "ssl/fullchain.pem",
				TLSKey:     "ssl/privkey.pem",
				MaxArtSize: DefaultMaxArticleSize, // 128 KB
			},
		},
		Database: DatabaseConfig{
			MainDB:    "./data/cfg/pugleaf.sq3",
			GroupsDir: "./data/db",
			BackupDir: "./backups",
		},
		Providers: DefaultProviders,
	}

	maincfg.mux.Lock()
	log.Printf("MainConfig initialized with %d providers", len(maincfg.Providers))
	maincfg.mux.Unlock()
	return maincfg
}

// UpdateBadBots safely updates the global bad bots configuration
func UpdateBadBots(badBotsStr string, blockEnabled bool) {
	BadBotsMutex.Lock()
	defer BadBotsMutex.Unlock()

	BlockBadBots = blockEnabled

	if badBotsStr != "" {
		// Parse comma-separated list and trim whitespace
		Default_BadBots = []string{}
		for _, pattern := range strings.Split(badBotsStr, ",") {
			trimmed := strings.TrimSpace(pattern)
			if trimmed != "" {
				Default_BadBots = append(Default_BadBots, trimmed)
			}
		}
		log.Printf("Updated BadBots list (%d patterns)", len(Default_BadBots))
	}

	log.Printf("Bot configuration updated: BlockBadBots=%t, patterns=%d", BlockBadBots, len(Default_BadBots))
}

// UpdateBadIPs safely updates the global bad IPs configuration
func UpdateBadIPs(badIPsStr string, blockEnabled bool) error {
	BadIPsMutex.Lock()
	defer BadIPsMutex.Unlock()

	BlockBadIPs = blockEnabled

	if badIPsStr == "" {
		// Use default IP ranges if config is empty
		defaultRanges := []string{
			"47.74.0.0/15",     // alibaba
			"47.76.0.0/14",     // alibaba
			"47.80.0.0/13",     // alibaba
			"185.191.171.0/24", // semrush
		}

		Default_BlockedIPs = make([]*net.IPNet, 0, len(defaultRanges))
		for _, cidr := range defaultRanges {
			_, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				log.Printf("Warning: failed to parse default CIDR %s: %v", cidr, err)
				continue
			}
			Default_BlockedIPs = append(Default_BlockedIPs, ipNet)
		}
		log.Printf("Using default BlockedIPs list (%d ranges)", len(Default_BlockedIPs))
	} else {
		// Parse comma-separated list and trim whitespace
		Default_BlockedIPs = []*net.IPNet{}
		for _, cidr := range strings.Split(badIPsStr, ",") {
			trimmed := strings.TrimSpace(cidr)
			if trimmed == "" {
				continue
			}

			// Handle single IP addresses by adding /32 or /128 suffix
			if !strings.Contains(trimmed, "/") {
				ip := net.ParseIP(trimmed)
				if ip != nil {
					if ip.To4() != nil {
						trimmed += "/32" // IPv4
					} else {
						trimmed += "/128" // IPv6
					}
				}
			}

			_, ipNet, err := net.ParseCIDR(trimmed)
			if err != nil {
				log.Printf("Warning: failed to parse CIDR %s: %v", trimmed, err)
				continue
			}
			Default_BlockedIPs = append(Default_BlockedIPs, ipNet)
		}
		log.Printf("Updated BlockedIPs list (%d ranges)", len(Default_BlockedIPs))
	}

	log.Printf("IP blocking configuration updated: BlockBadIPs=%t, ranges=%d", BlockBadIPs, len(Default_BlockedIPs))
	return nil
}
