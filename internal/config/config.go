// Package config provides configuration management for go-pugleaf.
// Adapted from NZBreX for newsgroup server use.
package config

import (
	"log"
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
	Debug      bool   `json:"debug"` // Enable debug logging for sessions/auth
}

var DefaultProviders = []Provider{
	{
		Grp:        "Primary",
		Name:       "localhost",
		Host:       "localhost",
		Username:   "",
		Password:   "",
		Port:       11120,
		SSL:        false,
		MaxConns:   250,
		Enabled:    false,
		Priority:   97,
		MaxArtSize: 32768,
	},
	{
		Grp:        "Primary",
		Name:       "NewsDeef Archive",
		Host:       "lux-feed1.newsdeef.eu",
		Username:   "pugleaf",
		Password:   "rslight",
		Port:       563,
		SSL:        true,
		MaxConns:   100,
		Enabled:    true,
		Priority:   97,
		MaxArtSize: 32768,
	},
	{
		Grp:        "Secondary",
		Name:       "Open Proxy",
		Host:       "proxy.usenet-server.com",
		Username:   "pugleaf",
		Password:   "usenets",
		Port:       1563,
		SSL:        true,
		MaxConns:   5,
		Enabled:    true,
		Priority:   98,
		MaxArtSize: 32768,
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
		Enabled:    true,
		Priority:   99,
		MaxArtSize: 32768,
	},
}

// NewDefaultConfig returns a configuration with sensible defaults
func NewDefaultConfig() *MainConfig {

	maincfg := &MainConfig{
		AppVersion: AppVersion, // Set application version

		Server: ServerConfig{
			WEB: &WebConfig{
				ListenPort: 11980,
				SSL:        false,
				StaticDir:  "web/static",
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
				TLSCert:    "ssl/cert.pem",
				TLSKey:     "ssl/privkey.pem",
				MaxArtSize: DefaultMaxArticleSize, // 128 KB
			},
		},
		Database: DatabaseConfig{
			MainDB:    "data/pugleaf.sq3",
			GroupsDir: "data/groups",
			BackupDir: "backups",
		},
		Providers: DefaultProviders,
	}

	maincfg.mux.Lock()
	log.Printf("MainConfig initialized with %d providers", len(maincfg.Providers))
	maincfg.mux.Unlock()
	return maincfg
}
