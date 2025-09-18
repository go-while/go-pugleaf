package nntp

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/proxy"
)

// ProxyType represents the type of proxy connection
type ProxyType string

const (
	ProxyTypeSOCKS4 ProxyType = "socks4"
	ProxyTypeSOCKS5 ProxyType = "socks5"
)

// ProxyDialer provides proxy-aware dialing capabilities for NNTP connections
type ProxyDialer struct {
	config *BackendConfig
}

// NewProxyDialer creates a new proxy dialer based on the backend configuration
func NewProxyDialer(config *BackendConfig) *ProxyDialer {
	return &ProxyDialer{
		config: config,
	}
}

// Dial establishes a connection to the target host, optionally through a proxy
func (pd *ProxyDialer) Dial(network, address string) (net.Conn, error) {
	// If proxy is not enabled, use direct connection
	if !pd.config.ProxyEnabled {
		return pd.dialDirect(network, address)
	}

	switch ProxyType(pd.config.ProxyType) {
	case ProxyTypeSOCKS4:
		return pd.dialSOCKS4(network, address)
	case ProxyTypeSOCKS5:
		return pd.dialSOCKS5(network, address)
	default:
		return nil, fmt.Errorf("unsupported proxy type: %s", pd.config.ProxyType)
	}
}

// DialTLS establishes a TLS connection to the target host, optionally through a proxy
func (pd *ProxyDialer) DialTLS(network, address string, tlsConfig *tls.Config) (net.Conn, error) {
	// First establish the connection (direct or through proxy)
	// pd.Dial() handles all the proxy logic internally
	conn, err := pd.Dial(network, address)
	if err != nil {
		return nil, err
	}

	// Now wrap the connection (whether direct or proxied) with TLS
	tlsConn := tls.Client(conn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		if pd.config.ProxyEnabled {
			return nil, fmt.Errorf("TLS handshake failed through %s proxy: %w", pd.config.ProxyType, err)
		}
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}

	return tlsConn, nil
}

// dialDirect establishes a direct connection without proxy
func (pd *ProxyDialer) dialDirect(network, address string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout: pd.config.ConnectTimeout,
	}
	return dialer.Dial(network, address)
}

// dialSOCKS4 establishes a connection through a SOCKS4 proxy
// Note: golang.org/x/net/proxy doesn't have native SOCKS4 support, so we use SOCKS5 without auth
func (pd *ProxyDialer) dialSOCKS4(network, address string) (net.Conn, error) {
	proxyAddr := net.JoinHostPort(pd.config.ProxyHost, fmt.Sprintf("%d", pd.config.ProxyPort))

	// SOCKS4 doesn't support authentication, so we use SOCKS5 without auth
	// Most SOCKS4 proxies also support SOCKS5 without authentication (said AI)
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, &net.Dialer{
		Timeout: pd.config.ConnectTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SOCKS4/5 proxy dialer: %w", err)
	}

	return dialer.Dial(network, address)
}

// dialSOCKS5 establishes a connection through a SOCKS5 proxy (including Tor)
func (pd *ProxyDialer) dialSOCKS5(network, address string) (net.Conn, error) {
	proxyAddr := net.JoinHostPort(pd.config.ProxyHost, fmt.Sprintf("%d", pd.config.ProxyPort))

	var auth *proxy.Auth
	if pd.config.ProxyUsername != "" {
		auth = &proxy.Auth{
			User:     pd.config.ProxyUsername,
			Password: pd.config.ProxyPassword,
		}
	}

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, &net.Dialer{
		Timeout: pd.config.ConnectTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create SOCKS5 proxy dialer: %w", err)
	}

	return dialer.Dial(network, address)
}

// IsOnionAddress checks if the given host is a Tor .onion address
func IsOnionAddress(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), ".onion")
}
