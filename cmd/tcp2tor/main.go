// tcp2tor - General TCP proxy tool for go-pugleaf
// This tool creates a local TCP listener that forwards raw TCP connections through a SOCKS5 proxy
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
)

var appVersion = "-unset-"

// ProxyConfig holds the configuration for the SOCKS5 proxy
type ProxyConfig struct {
	SocksHost string
	SocksPort int
	SocksAuth *proxy.Auth // Optional authentication
}

// showUsageExamples displays usage examples for tcp2tor
func showUsageExamples() {
	fmt.Println("\n=== tcp2tor - General TCP Proxy Tool ===")
	fmt.Println("Creates a local TCP listener that forwards raw TCP connections through SOCKS5 proxy.")
	fmt.Println("Note: Works with any TCP service and any SOCKS5 proxy - not limited to NNTP or Tor.")
	fmt.Println()
	fmt.Println("Basic Usage:")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target test.onion:119")
	fmt.Println("  ./tcp2tor -listen-port 1563 -listen-host 127.2.3.4 -target test.onion:563")
	fmt.Println()
	fmt.Println("Custom SOCKS5 Proxy:")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target test.onion:119 -socks5-host 127.0.0.1 -socks5-port 9050")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target test.onion:119 -socks5-proxy 192.168.1.100:9050")
	fmt.Println()
	fmt.Println("SOCKS5 Authentication:")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target test.onion:119 -socks5-user myuser -socks5-pass mypass")
	fmt.Println()
	fmt.Println("Multiple Targets (use multiple instances):")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target news1.onion:119 &")
	fmt.Println("  ./tcp2tor -listen-port 1120 -listen-host 127.2.3.5 -target news2.onion:119 &")
	fmt.Println()
	fmt.Println("Then configure your NNTP client to connect to localhost:1119")
	fmt.Println()
}

func main() {
	log.Printf("Starting tcp2tor (version %s)", appVersion)

	// Command line flags
	var (
		listenPort = flag.Int("listen-port", 1119, "Local port to listen on for incoming connections")
		listenHost = flag.String("listen-host", "", "Local host/IP to bind to like 127.2.3.4")
		targetAddr = flag.String("target", "", "Target onion address and port (e.g., example.onion:119)")

		// SOCKS5 proxy configuration
		socksHost  = flag.String("socks5-host", "127.0.0.1", "SOCKS5 proxy host")
		socksPort  = flag.Int("socks5-port", 9050, "SOCKS5 proxy port")
		socksProxy = flag.String("socks5-proxy", "", "SOCKS5 proxy address (host:port) - overrides -socks5-host/-socks5-port")
		socksUser  = flag.String("socks5-user", "", "SOCKS5 proxy username (optional)")
		socksPass  = flag.String("socks5-pass", "", "SOCKS5 proxy password (optional)")

		// Operation options
		showHelp = flag.Bool("help", false, "Show usage examples and exit")
		timeout  = flag.Int("timeout", 30, "Connection timeout in seconds")
		verbose  = flag.Bool("verbose", false, "Enable verbose logging")
	)
	flag.Parse()

	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}

	// Validate required flags
	if *targetAddr == "" {
		log.Fatalf("Error: -target must be specified (e.g., example.onion:119)")
	}

	// Parse target address
	targetHost, targetPort, err := parseTargetAddress(*targetAddr)
	if err != nil {
		log.Fatalf("Error: Invalid target address '%s': %v", *targetAddr, err)
	}

	// Validate listen port
	if *listenPort < 1 || *listenPort > 65535 {
		log.Fatalf("Error: listen-port must be between 1 and 65535 (got %d)", *listenPort)
	}

	// Parse SOCKS5 proxy configuration
	proxyConfig, err := parseProxyConfig(*socksProxy, *socksHost, *socksPort, *socksUser, *socksPass)
	if err != nil {
		log.Fatalf("Error: Invalid SOCKS5 proxy configuration: %v", err)
	}

	// Create listen address
	listenAddr := fmt.Sprintf("%s:%d", *listenHost, *listenPort)

	log.Printf("Configuration:")
	log.Printf("  Listen: %s", listenAddr)
	log.Printf("  Target: %s:%s", targetHost, targetPort)
	log.Printf("  SOCKS5 Proxy: %s:%d", proxyConfig.SocksHost, proxyConfig.SocksPort)
	if proxyConfig.SocksAuth != nil {
		log.Printf("  SOCKS5 Auth: %s", proxyConfig.SocksAuth.User)
	}
	log.Printf("  Timeout: %d seconds", *timeout)

	// Test SOCKS5 proxy connection
	if err := testSOCKS5Connection(proxyConfig, targetHost, targetPort, *timeout, *verbose); err != nil {
		log.Fatalf("Error: Failed to connect through SOCKS5 proxy: %v", err)
	}
	log.Printf("✓ SOCKS5 proxy connection test successful")

	// Start the proxy server
	server := &ProxyServer{
		ListenAddr:  listenAddr,
		TargetHost:  targetHost,
		TargetPort:  targetPort,
		ProxyConfig: proxyConfig,
		Timeout:     time.Duration(*timeout) * time.Second,
		Verbose:     *verbose,
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start server in goroutine
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Start()
	}()

	// Wait for shutdown signal or server error
	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down gracefully...", sig)
		server.Stop()
	case err := <-serverDone:
		if err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	log.Printf("tcp2tor proxy shutdown complete")
}

// parseTargetAddress parses target address in format "host:port"
func parseTargetAddress(target string) (host, port string, err error) {
	parts := strings.Split(target, ":")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("target must be in format 'host:port'")
	}

	host = strings.TrimSpace(parts[0])
	port = strings.TrimSpace(parts[1])

	if host == "" {
		return "", "", fmt.Errorf("host cannot be empty")
	}

	// Validate port
	if portNum, err := strconv.Atoi(port); err != nil || portNum < 1 || portNum > 65535 {
		return "", "", fmt.Errorf("port must be a number between 1 and 65535")
	}

	return host, port, nil
}

// parseProxyConfig creates proxy configuration from command line flags
func parseProxyConfig(socksProxy, socksHost string, socksPort int, socksUser, socksPass string) (*ProxyConfig, error) {
	config := &ProxyConfig{}

	// Parse proxy address
	if socksProxy != "" {
		// Use -socks5-proxy flag (overrides host/port)
		parts := strings.Split(socksProxy, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("socks5-proxy must be in format 'host:port'")
		}
		config.SocksHost = strings.TrimSpace(parts[0])
		if port, err := strconv.Atoi(strings.TrimSpace(parts[1])); err != nil {
			return nil, fmt.Errorf("invalid port in socks5-proxy: %v", err)
		} else {
			config.SocksPort = port
		}
	} else {
		// Use individual host/port flags
		config.SocksHost = socksHost
		config.SocksPort = socksPort
	}

	// Validate proxy configuration
	if config.SocksHost == "" {
		return nil, fmt.Errorf("SOCKS5 proxy host cannot be empty")
	}
	if config.SocksPort < 1 || config.SocksPort > 65535 {
		return nil, fmt.Errorf("SOCKS5 proxy port must be between 1 and 65535")
	}

	// Set up authentication if provided
	if socksUser != "" || socksPass != "" {
		config.SocksAuth = &proxy.Auth{
			User:     socksUser,
			Password: socksPass,
		}
	}

	return config, nil
}

// testSOCKS5Connection tests the SOCKS5 proxy connection
func testSOCKS5Connection(config *ProxyConfig, targetHost, targetPort string, timeoutSec int, verbose bool) error {
	// Create SOCKS5 dialer
	proxyAddr := fmt.Sprintf("%s:%d", config.SocksHost, config.SocksPort)

	var dialer proxy.Dialer
	var err error

	if config.SocksAuth != nil {
		if verbose {
			log.Printf("Creating SOCKS5 dialer with authentication to %s", proxyAddr)
		}
		dialer, err = proxy.SOCKS5("tcp", proxyAddr, config.SocksAuth, proxy.Direct)
	} else {
		if verbose {
			log.Printf("Creating SOCKS5 dialer without authentication to %s", proxyAddr)
		}
		dialer, err = proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	}

	if err != nil {
		return fmt.Errorf("failed to create SOCKS5 dialer: %v", err)
	}

	// Test connection
	targetAddr := fmt.Sprintf("%s:%s", targetHost, targetPort)
	if verbose {
		log.Printf("Testing connection to %s through SOCKS5 proxy...", targetAddr)
	}

	conn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to %s through SOCKS5 proxy: %v", targetAddr, err)
	}
	defer conn.Close()

	if verbose {
		log.Printf("Successfully connected to %s", targetAddr)
	}

	return nil
}

// ProxyServer handles the TCP proxy functionality
type ProxyServer struct {
	ListenAddr  string
	TargetHost  string
	TargetPort  string
	ProxyConfig *ProxyConfig
	Timeout     time.Duration
	Verbose     bool
	listener    net.Listener
	shutdown    chan struct{}
}

// Start starts the proxy server
func (s *ProxyServer) Start() error {
	var err error
	s.listener, err = net.Listen("tcp", s.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", s.ListenAddr, err)
	}
	defer s.listener.Close()

	s.shutdown = make(chan struct{})
	log.Printf("tcp2tor proxy listening on %s", s.ListenAddr)
	log.Printf("Forwarding connections to %s:%s through SOCKS5 proxy %s:%d",
		s.TargetHost, s.TargetPort, s.ProxyConfig.SocksHost, s.ProxyConfig.SocksPort)

	for {
		select {
		case <-s.shutdown:
			log.Printf("Proxy server shutting down...")
			return nil
		default:
		}

		// Set accept timeout
		if tcpListener, ok := s.listener.(*net.TCPListener); ok {
			tcpListener.SetDeadline(time.Now().Add(1 * time.Second))
		}

		conn, err := s.listener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Timeout, check for shutdown
			}
			if strings.Contains(err.Error(), "use of closed network connection") {
				return nil // Normal shutdown
			}
			return fmt.Errorf("failed to accept connection: %v", err)
		}

		// Handle connection in goroutine
		go s.handleConnection(conn)
	}
}

// Stop stops the proxy server
func (s *ProxyServer) Stop() {
	if s.shutdown != nil {
		close(s.shutdown)
	}
	if s.listener != nil {
		s.listener.Close()
	}
}

// handleConnection handles a single client connection
func (s *ProxyServer) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	clientAddr := clientConn.RemoteAddr().String()
	if s.Verbose {
		log.Printf("New connection from %s", clientAddr)
	}

	// Create SOCKS5 dialer
	proxyAddr := fmt.Sprintf("%s:%d", s.ProxyConfig.SocksHost, s.ProxyConfig.SocksPort)

	var dialer proxy.Dialer
	var err error

	if s.ProxyConfig.SocksAuth != nil {
		dialer, err = proxy.SOCKS5("tcp", proxyAddr, s.ProxyConfig.SocksAuth, proxy.Direct)
	} else {
		dialer, err = proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	}

	if err != nil {
		log.Printf("Failed to create SOCKS5 dialer for %s: %v", clientAddr, err)
		return
	}

	// Connect to target through SOCKS5 proxy
	targetAddr := fmt.Sprintf("%s:%s", s.TargetHost, s.TargetPort)
	if s.Verbose {
		log.Printf("Connecting to %s through SOCKS5 proxy for client %s", targetAddr, clientAddr)
	}

	targetConn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("Failed to connect to %s for client %s: %v", targetAddr, clientAddr, err)
		return
	}
	defer targetConn.Close()

	if s.Verbose {
		log.Printf("Connected to %s for client %s", targetAddr, clientAddr)
	}

	// Start bidirectional forwarding
	done := make(chan struct{}, 2)

	// Forward client -> target
	go func() {
		defer func() { done <- struct{}{} }()
		written, err := io.Copy(targetConn, clientConn)
		if s.Verbose && err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("Client->Target copy error for %s: %v (wrote %d bytes)", clientAddr, err, written)
		}
	}()

	// Forward target -> client
	go func() {
		defer func() { done <- struct{}{} }()
		written, err := io.Copy(clientConn, targetConn)
		if s.Verbose && err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("Target->Client copy error for %s: %v (wrote %d bytes)", clientAddr, err, written)
		}
	}()

	// Wait for either direction to close
	<-done

	if s.Verbose {
		log.Printf("Connection closed for client %s", clientAddr)
	}
}
