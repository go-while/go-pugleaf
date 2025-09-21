// tcp2tor - General TCP proxy tool
//
//	This tool creates a local TCP listener
//	that forwards raw TCP connections through a SOCKS5 proxy
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
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

// ProxyTarget holds configuration for a single proxy target
type ProxyTarget struct {
	ListenHost string
	ListenPort int
	TargetHost string
	TargetPort string
}

// ConfigEntry represents one line from the configuration file
type ConfigEntry struct {
	Target ProxyTarget
	Config *ProxyConfig
}

// showUsageExamples displays usage examples for tcp2tor
func showUsageExamples() {
	fmt.Println("\n=== tcp2tor - General TCP Proxy Tool ===")
	fmt.Println("Creates a local TCP listener that forwards raw TCP connections through SOCKS5 proxy.")
	fmt.Println("Note: Works with any TCP service and any SOCKS5 proxy - not limited to NNTP or Tor.")
	fmt.Println()
	fmt.Println("Single Target Mode:")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target test.onion:119")
	fmt.Println("  ./tcp2tor -listen-port 1563 -listen-host 127.2.3.4 -target test.onion:563")
	fmt.Println()
	fmt.Println("Configuration File Mode (multiple targets):")
	fmt.Println("  ./tcp2tor -config example.cfg")
	fmt.Println()
	fmt.Println("Add Entry to Configuration File:")
	fmt.Println("  ./tcp2tor -add myconfig.cfg -listen-host 127.2.3.4 -listen-port 1119 -target test.onion:119")
	fmt.Println("  ./tcp2tor -add myconfig.cfg -listen-host 127.2.3.5 -listen-port 1120 -target news.onion:119")
	fmt.Println()
	fmt.Println("Configuration file format (one per line):")
	fmt.Println("  listen_host:listen_port:target_host:target_port")
	fmt.Println("  127.2.3.4:1119:news1.onion:119")
	fmt.Println("  127.2.3.5:1120:news2.onion:119")
	fmt.Println("  127.2.3.6:1563:secure.onion:563")
	fmt.Println()
	fmt.Println("Custom SOCKS5 Proxy:")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target test.onion:119 -socks5-host 127.0.0.1 -socks5-port 9050")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target test.onion:119 -socks5-proxy 192.168.1.100:9050")
	fmt.Println()
	fmt.Println("SOCKS5 Authentication:")
	fmt.Println("  ./tcp2tor -listen-port 1119 -listen-host 127.2.3.4 -target test.onion:119 -socks5-user myuser -socks5-pass mypass")
	fmt.Println()
	fmt.Println("Then configure your clients to connect to the respective local ports")
	fmt.Println()
}

func main() {
	log.Printf("Starting tcp2tor (version %s)", appVersion)

	// Command line flags
	var (
		listenPort  = flag.Int("listen-port", 0, "Local port to listen on for incoming connections")
		listenHost  = flag.String("listen-host", "127.2.3.4", "Local host/IP to bind to like 127.2.3.4")
		targetAddr  = flag.String("target", "", "Target onion address and port (e.g., example.onion:119)")
		configFile  = flag.String("config", "", "Configuration file with multiple targets (format: listen_host:listen_port:target_host:target_port)")
		addToConfig = flag.String("add", "", "Add current listen-host:listen-port:target configuration to specified config file and exit")

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

	// Handle add mode - add entry to config file and exit
	if *addToConfig != "" {
		addEntryToConfigFile(*addToConfig, *listenHost, *listenPort, *targetAddr)
		os.Exit(0)
	}

	// Determine mode: config file or single target
	if *configFile != "" {
		// Config file mode - run multiple proxies
		runConfigFileMode(*configFile, *socksProxy, *socksHost, *socksPort, *socksUser, *socksPass, *timeout, *verbose)
	} else {
		// Single target mode - run one proxy
		runSingleTargetMode(*targetAddr, *listenHost, *listenPort, *socksProxy, *socksHost, *socksPort, *socksUser, *socksPass, *timeout, *verbose)
	}
}

// runSingleTargetMode runs a single proxy instance
func runSingleTargetMode(targetAddr, listenHost string, listenPort int, socksProxy, socksHost string, socksPort int, socksUser, socksPass string, timeout int, verbose bool) {
	// Validate required flags
	if targetAddr == "" {
		log.Fatalf("Error: -target must be specified (e.g., example.onion:119)")
	}

	// Parse target address
	targetHost, targetPort, err := parseTargetAddress(targetAddr)
	if err != nil {
		log.Fatalf("Error: Invalid target address '%s': %v", targetAddr, err)
	}

	// Validate listen port
	if listenPort < 1 || listenPort > 65535 {
		log.Fatalf("Error: listen-port must be between 1 and 65535 (got %d)", listenPort)
	}

	// Parse SOCKS5 proxy configuration
	proxyConfig, err := parseProxyConfig(socksProxy, socksHost, socksPort, socksUser, socksPass)
	if err != nil {
		log.Fatalf("Error: Invalid SOCKS5 proxy configuration: %v", err)
	}

	// Create listen address
	listenAddr := fmt.Sprintf("%s:%d", listenHost, listenPort)

	log.Printf("Single Target Configuration:")
	log.Printf("  Listen: %s", listenAddr)
	log.Printf("  Target: %s:%s", targetHost, targetPort)
	log.Printf("  SOCKS5 Proxy: %s:%d", proxyConfig.SocksHost, proxyConfig.SocksPort)
	if proxyConfig.SocksAuth != nil {
		log.Printf("  SOCKS5 Auth: %s", proxyConfig.SocksAuth.User)
	}
	log.Printf("  Timeout: %d seconds", timeout)

	// Test SOCKS5 proxy connection
	if err := testSOCKS5Connection(proxyConfig, targetHost, targetPort, timeout, verbose); err != nil {
		log.Fatalf("Error: Failed to connect through SOCKS5 proxy: %v", err)
	}
	log.Printf("✓ SOCKS5 proxy connection test successful")

	// Start the proxy server
	server := &ProxyServer{
		ListenAddr:  listenAddr,
		TargetHost:  targetHost,
		TargetPort:  targetPort,
		ProxyConfig: proxyConfig,
		Timeout:     time.Duration(timeout) * time.Second,
		Verbose:     verbose,
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

// runConfigFileMode runs multiple proxy instances from configuration file
func runConfigFileMode(configFile, socksProxy, socksHost string, socksPort int, socksUser, socksPass string, timeout int, verbose bool) {
	// Read and parse configuration file
	configs, err := parseConfigFile(configFile, socksProxy, socksHost, socksPort, socksUser, socksPass)
	if err != nil {
		log.Fatalf("Error: Failed to parse config file '%s': %v", configFile, err)
	}

	if len(configs) == 0 {
		log.Fatalf("Error: No valid configuration entries found in '%s'", configFile)
	}

	log.Printf("Connecting %d target(s)", len(configs))
	for i, config := range configs {
		log.Printf("  [%d] Listen: %s:%d -> Target: %s:%s", i+1,
			config.Target.ListenHost, config.Target.ListenPort,
			config.Target.TargetHost, config.Target.TargetPort)
	}

	// Test SOCKS5 proxy connections for all targets
	proxyConfig := configs[0].Config // Use the first config for testing (they should all be the same)
	log.Printf("Testing SOCKS5 proxy: %s:%d", proxyConfig.SocksHost, proxyConfig.SocksPort)

	for i, config := range configs {
		if err := testSOCKS5Connection(config.Config, config.Target.TargetHost, config.Target.TargetPort, timeout, verbose); err != nil {
			log.Fatalf("Error: Failed to connect to target %d (%s:%s) through SOCKS5 proxy: %v",
				i+1, config.Target.TargetHost, config.Target.TargetPort, err)
		}
		if verbose {
			log.Printf("✓ Target %d SOCKS5 connection test successful", i+1)
		}
	}
	log.Printf("✓ All SOCKS5 proxy connection tests successful")

	// Start all proxy servers
	var servers []*ProxyServer
	var wg sync.WaitGroup
	serverErrors := make(chan error, len(configs))

	for i, config := range configs {
		listenAddr := fmt.Sprintf("%s:%d", config.Target.ListenHost, config.Target.ListenPort)

		server := &ProxyServer{
			ListenAddr:  listenAddr,
			TargetHost:  config.Target.TargetHost,
			TargetPort:  config.Target.TargetPort,
			ProxyConfig: config.Config,
			Timeout:     time.Duration(timeout) * time.Second,
			Verbose:     verbose,
		}
		servers = append(servers, server)

		wg.Add(1)
		go func(srv *ProxyServer, idx int) {
			defer wg.Done()
			if err := srv.Start(); err != nil {
				serverErrors <- fmt.Errorf("server %d error: %v", idx+1, err)
			}
		}(server, i)

		log.Printf("Started proxy server %d: %s", i+1, listenAddr)
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Wait for shutdown signal or server error
	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down all proxies gracefully...", sig)
		for i, server := range servers {
			log.Printf("Stopping proxy server %d...", i+1)
			server.Stop()
		}
		wg.Wait()
	case err := <-serverErrors:
		log.Fatalf("Server error: %v", err)
	}

	log.Printf("All tcp2tor proxies shutdown complete")
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

// parseConfigFile reads and parses a configuration file with multiple targets
func parseConfigFile(filename, socksProxy, socksHost string, socksPort int, socksUser, socksPass string) ([]*ConfigEntry, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %v", err)
	}
	defer file.Close()

	var configs []*ConfigEntry
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse line format: listen_host:listen_port:target_host:target_port
		parts := strings.Split(line, ":")
		if len(parts) != 4 {
			return nil, fmt.Errorf("line %d: invalid format, expected 'listen_host:listen_port:target_host:target_port', got '%s'", lineNum, line)
		}

		listenHost := strings.TrimSpace(parts[0])
		listenPortStr := strings.TrimSpace(parts[1])
		targetHost := strings.TrimSpace(parts[2])
		targetPortStr := strings.TrimSpace(parts[3])

		// Validate and parse listen port
		listenPort, err := strconv.Atoi(listenPortStr)
		if err != nil || listenPort < 1 || listenPort > 65535 {
			return nil, fmt.Errorf("line %d: invalid listen port '%s', must be number between 1-65535", lineNum, listenPortStr)
		}

		// Validate target host
		if targetHost == "" {
			return nil, fmt.Errorf("line %d: target host cannot be empty", lineNum)
		}

		// Validate and parse target port
		if strings.Contains(targetPortStr, "#") {
			targetPortStr = strings.Split(targetPortStr, "#")[0]
		}
		targetPortStr = strings.TrimSpace(targetPortStr)
		targetPortNum, err := strconv.Atoi(targetPortStr)
		if err != nil || targetPortNum < 1 || targetPortNum > 65535 {
			return nil, fmt.Errorf("line %d: invalid target port '%s', must be number between 1-65535", lineNum, targetPortStr)
		}

		// Create proxy configuration (same for all entries)
		proxyConfig, err := parseProxyConfig(socksProxy, socksHost, socksPort, socksUser, socksPass)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid SOCKS5 proxy configuration: %v", lineNum, err)
		}

		// Create config entry
		config := &ConfigEntry{
			Target: ProxyTarget{
				ListenHost: listenHost,
				ListenPort: listenPort,
				TargetHost: targetHost,
				TargetPort: targetPortStr,
			},
			Config: proxyConfig,
		}

		configs = append(configs, config)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading config file: %v", err)
	}

	return configs, nil
}

// addEntryToConfigFile adds a new configuration entry to the specified config file
func addEntryToConfigFile(configFile, listenHost string, listenPort int, targetAddr string) {
	// Validate required parameters
	if targetAddr == "" {
		log.Fatalf("Error: -target must be specified when using -add")
	}
	if listenHost == "" {
		log.Fatalf("Error: -listen-host must be specified when using -add")
	}
	if listenPort < 1 || listenPort > 65535 {
		log.Fatalf("Error: -listen-port must be between 1 and 65535 when using -add (got %d)", listenPort)
	}

	// Parse and validate target address
	targetHost, targetPort, err := parseTargetAddress(targetAddr)
	if err != nil {
		log.Fatalf("Error: Invalid target address '%s': %v", targetAddr, err)
	}

	// Create the configuration line
	configLine := fmt.Sprintf("%s:%d:%s:%s", listenHost, listenPort, targetHost, targetPort)

	// Check if config file exists and read existing content
	var existingLines []string
	if _, err := os.Stat(configFile); err == nil {
		// File exists, read it
		file, err := os.Open(configFile)
		if err != nil {
			log.Fatalf("Error: Failed to open existing config file '%s': %v", configFile, err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			existingLines = append(existingLines, line)

			// Check for duplicates
			if line == configLine {
				log.Printf("Warning: Entry '%s' already exists in config file", configLine)
				return
			}
		}

		if err := scanner.Err(); err != nil {
			log.Fatalf("Error: Failed to read existing config file '%s': %v", configFile, err)
		}
	}

	// Open file for appending (create if it doesn't exist)
	file, err := os.OpenFile(configFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error: Failed to open config file '%s' for writing: %v", configFile, err)
	}
	defer file.Close()

	// Add header comment if this is a new file
	if len(existingLines) == 0 {
		fmt.Fprintf(file, "# tcp2tor configuration file\n")
		fmt.Fprintf(file, "# Format: listen_host:listen_port:target_host:target_port\n")
		fmt.Fprintf(file, "#\n")
	}

	// Write the new configuration line
	fmt.Fprintf(file, "%s\n", configLine)

	log.Printf("✓ Added configuration entry to '%s': %s", configFile, configLine)
	log.Printf("You can now run: ./tcp2tor -config %s", configFile)
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
