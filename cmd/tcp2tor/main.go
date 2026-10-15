// tcp2tor - General TCP proxy tool
//
//	This tool creates a local TCP listener
//	that forwards raw TCP connections through a SOCKS5 proxy
package main

import (
	"bufio"
	"context"
	"errors"
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
	"sync/atomic"
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

const (
	// defaultMaxConns is the per-listener limit of concurrently forwarded connections.
	defaultMaxConns = 64
	// maxMaxConns is the upper bound accepted for -max-conns / max_conns.
	maxMaxConns = 10000
	// defaultIdleTimeout closes a forwarded connection after this long without traffic in either direction.
	defaultIdleTimeout = 10 * time.Minute
)

// serverLimits holds the per-listener limits and the bind policy shared by both run modes.
type serverLimits struct {
	MaxConns        int           // concurrently forwarded connections per listener
	IdleTimeout     time.Duration // close a forwarded connection after this long without traffic
	AllowPublicBind bool          // permit listen hosts that are not loopback addresses
}

// validate checks the limits after flags and config-file keys have been merged.
func (l serverLimits) validate() error {
	if l.MaxConns < 1 || l.MaxConns > maxMaxConns {
		return fmt.Errorf("max-conns must be between 1 and %d (got %d)", maxMaxConns, l.MaxConns)
	}
	if l.IdleTimeout <= 0 {
		return fmt.Errorf("idle-timeout must be > 0 (got %v)", l.IdleTimeout)
	}
	return nil
}

// fileSettings holds the optional "key = value" settings of a configuration file.
// A nil field means the key was not present.
type fileSettings struct {
	MaxConns        *int
	IdleTimeout     *time.Duration
	AllowPublicBind *bool
}

// apply merges the file settings into base. A key from the file overrides the flag default,
// but a flag given explicitly on the command line (named in explicit) wins over the file.
func (fs fileSettings) apply(base serverLimits, explicit map[string]bool) serverLimits {
	if fs.MaxConns != nil && !explicit["max-conns"] {
		base.MaxConns = *fs.MaxConns
	}
	if fs.IdleTimeout != nil && !explicit["idle-timeout"] {
		base.IdleTimeout = *fs.IdleTimeout
	}
	if fs.AllowPublicBind != nil && !explicit["allow-public-bind"] {
		base.AllowPublicBind = *fs.AllowPublicBind
	}
	return base
}

// checkListenHost refuses listen hosts that are not loopback addresses unless allowPublic is set.
// "localhost" and any IP for which net.IP.IsLoopback is true (127.0.0.0/8, ::1) are accepted.
// An empty host (bind all interfaces) and any other hostname count as public.
func checkListenHost(host string, allowPublic bool) error {
	h := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(host), "["), "]")
	if strings.EqualFold(h, "localhost") {
		return nil
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return nil
	}
	if allowPublic {
		log.Printf("WARNING: listen host %q is not a loopback address: anyone who can reach it can use this proxy", host)
		return nil
	}
	return fmt.Errorf("listen host %q is not a loopback address; tcp2tor has no authentication, "+
		"so binding it would expose the SOCKS5 proxy to the network. Use a 127.x.x.x address, "+
		"or set 'allow_public_bind = true' in the config file or pass -allow-public-bind to bind it anyway", host)
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
	fmt.Println("Optional settings in the configuration file (key = value):")
	fmt.Println("  max_conns = 64            # concurrent connections per listener (flag -max-conns)")
	fmt.Println("  idle_timeout = 10m        # close a connection after this long without traffic (flag -idle-timeout)")
	fmt.Println("  allow_public_bind = false # permit non-loopback listen hosts (flag -allow-public-bind)")
	fmt.Println("  A flag given on the command line overrides the same key in the file.")
	fmt.Println()
	fmt.Println("Limits and bind policy:")
	fmt.Println("  Listen hosts must be loopback addresses (127.x.x.x, ::1, localhost) unless -allow-public-bind")
	fmt.Println("  is given: tcp2tor has no authentication. The default 127.2.3.4 is loopback on Linux only.")
	fmt.Println("  ./tcp2tor -listen-port 1119 -target test.onion:119 -max-conns 16 -idle-timeout 5m")
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

		// Limits and bind policy (config file keys: max_conns, idle_timeout, allow_public_bind)
		maxConns        = flag.Int("max-conns", defaultMaxConns, "Maximum concurrent forwarded connections per listener")
		idleTimeout     = flag.Duration("idle-timeout", defaultIdleTimeout, "Close a forwarded connection after this long without traffic in either direction")
		allowPublicBind = flag.Bool("allow-public-bind", false, "Allow listen hosts that are not loopback addresses (the proxy has no authentication)")
	)
	flag.Parse()

	limits := serverLimits{MaxConns: *maxConns, IdleTimeout: *idleTimeout, AllowPublicBind: *allowPublicBind}
	explicit := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	// Show help if requested
	if *showHelp {
		showUsageExamples()
		os.Exit(0)
	}

	// Handle add mode - add entry to config file and exit
	if *addToConfig != "" {
		addEntryToConfigFile(*addToConfig, *listenHost, *listenPort, *targetAddr, *allowPublicBind)
		os.Exit(0)
	}

	// Determine mode: config file or single target
	if *configFile != "" {
		// Config file mode - run multiple proxies
		runConfigFileMode(*configFile, *socksProxy, *socksHost, *socksPort, *socksUser, *socksPass, *timeout, *verbose, limits, explicit)
	} else {
		// Single target mode - run one proxy
		runSingleTargetMode(*targetAddr, *listenHost, *listenPort, *socksProxy, *socksHost, *socksPort, *socksUser, *socksPass, *timeout, *verbose, limits)
	}
}

// runSingleTargetMode runs a single proxy instance
func runSingleTargetMode(targetAddr, listenHost string, listenPort int, socksProxy, socksHost string, socksPort int, socksUser, socksPass string, timeout int, verbose bool, limits serverLimits) {
	// Validate required flags
	if targetAddr == "" {
		log.Fatalf("Error: -target must be specified (e.g., example.onion:119)")
	}
	if err := limits.validate(); err != nil {
		log.Fatalf("Error: %v", err)
	}
	if err := checkListenHost(listenHost, limits.AllowPublicBind); err != nil {
		log.Fatalf("Error: %v", err)
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
	listenAddr := net.JoinHostPort(listenHost, strconv.Itoa(listenPort))

	log.Printf("Single Target Configuration:")
	log.Printf("  Listen: %s", listenAddr)
	log.Printf("  Target: %s:%s", targetHost, targetPort)
	log.Printf("  SOCKS5 Proxy: %s:%d", proxyConfig.SocksHost, proxyConfig.SocksPort)
	if proxyConfig.SocksAuth != nil {
		log.Printf("  SOCKS5 Auth: %s", proxyConfig.SocksAuth.User)
	}
	log.Printf("  Timeout: %d seconds", timeout)
	log.Printf("  Max connections: %d, idle timeout: %v", limits.MaxConns, limits.IdleTimeout)

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
		MaxConns:    limits.MaxConns,
		IdleTimeout: limits.IdleTimeout,
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Bind synchronously so that a later Stop always sees the listener
	if err := server.Listen(); err != nil {
		log.Fatalf("Error: %v", err)
	}

	// Serve in goroutine
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve()
	}()

	// Wait for shutdown signal or server error
	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down gracefully...", sig)
		server.Stop()
		if err := <-serverDone; err != nil {
			log.Printf("Server error during shutdown: %v", err)
		}
	case err := <-serverDone:
		if err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	log.Printf("tcp2tor proxy shutdown complete")
}

// runConfigFileMode runs multiple proxy instances from configuration file
func runConfigFileMode(configFile, socksProxy, socksHost string, socksPort int, socksUser, socksPass string, timeout int, verbose bool, limits serverLimits, explicit map[string]bool) {
	// Read and parse configuration file
	configs, settings, err := parseConfigFile(configFile, socksProxy, socksHost, socksPort, socksUser, socksPass)
	if err != nil {
		log.Fatalf("Error: Failed to parse config file '%s': %v", configFile, err)
	}

	if len(configs) == 0 {
		log.Fatalf("Error: No valid configuration entries found in '%s'", configFile)
	}

	limits = settings.apply(limits, explicit)
	if err := limits.validate(); err != nil {
		log.Fatalf("Error: %v", err)
	}
	for i, config := range configs {
		if err := checkListenHost(config.Target.ListenHost, limits.AllowPublicBind); err != nil {
			log.Fatalf("Error: entry %d: %v", i+1, err)
		}
	}

	log.Printf("Connecting %d target(s), max connections per listener: %d, idle timeout: %v",
		len(configs), limits.MaxConns, limits.IdleTimeout)
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

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Bind every listener synchronously first, so a Stop always sees them all
	for i, config := range configs {
		listenAddr := net.JoinHostPort(config.Target.ListenHost, strconv.Itoa(config.Target.ListenPort))

		server := &ProxyServer{
			ListenAddr:  listenAddr,
			TargetHost:  config.Target.TargetHost,
			TargetPort:  config.Target.TargetPort,
			ProxyConfig: config.Config,
			Timeout:     time.Duration(timeout) * time.Second,
			Verbose:     verbose,
			MaxConns:    limits.MaxConns,
			IdleTimeout: limits.IdleTimeout,
		}
		if err := server.Listen(); err != nil {
			for _, srv := range servers {
				srv.Stop()
			}
			log.Fatalf("Error: proxy server %d: %v", i+1, err)
		}
		servers = append(servers, server)
	}

	for i, server := range servers {
		wg.Add(1)
		go func(srv *ProxyServer, idx int) {
			defer wg.Done()
			if err := srv.Serve(); err != nil {
				serverErrors <- fmt.Errorf("server %d error: %v", idx+1, err)
			}
		}(server, i)

		log.Printf("Started proxy server %d: %s", i+1, server.ListenAddr)
	}

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

// parseSettingLine parses one "key = value" line of a configuration file into fs.
// A trailing "# comment" is ignored. Unknown keys, duplicate keys and invalid values are errors.
func parseSettingLine(line string, fs *fileSettings) error {
	if i := strings.Index(line, "#"); i >= 0 {
		line = line[:i]
	}
	key, value, _ := strings.Cut(line, "=")
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("setting '%s' has no value", key)
	}
	switch key {
	case "max_conns":
		if fs.MaxConns != nil {
			return fmt.Errorf("duplicate setting '%s'", key)
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > maxMaxConns {
			return fmt.Errorf("invalid max_conns '%s', must be a number between 1-%d", value, maxMaxConns)
		}
		fs.MaxConns = &n
	case "idle_timeout":
		if fs.IdleTimeout != nil {
			return fmt.Errorf("duplicate setting '%s'", key)
		}
		// Accept a Go duration ("10m", "90s") or a plain number of seconds.
		d, err := time.ParseDuration(value)
		if err != nil {
			secs, aerr := strconv.Atoi(value)
			if aerr != nil {
				return fmt.Errorf("invalid idle_timeout '%s', want a duration like 10m or a number of seconds", value)
			}
			d = time.Duration(secs) * time.Second
		}
		if d <= 0 {
			return fmt.Errorf("invalid idle_timeout '%s', must be > 0", value)
		}
		fs.IdleTimeout = &d
	case "allow_public_bind":
		if fs.AllowPublicBind != nil {
			return fmt.Errorf("duplicate setting '%s'", key)
		}
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid allow_public_bind '%s', want true or false", value)
		}
		fs.AllowPublicBind = &b
	default:
		return fmt.Errorf("unknown setting '%s' (known: max_conns, idle_timeout, allow_public_bind)", key)
	}
	return nil
}

// parseConfigFile reads and parses a configuration file with multiple targets.
// Lines are either "listen_host:listen_port:target_host:target_port" entries or
// "key = value" settings (see parseSettingLine); empty lines and "#" comments are skipped.
func parseConfigFile(filename, socksProxy, socksHost string, socksPort int, socksUser, socksPass string) ([]*ConfigEntry, fileSettings, error) {
	var settings fileSettings
	file, err := os.Open(filename)
	if err != nil {
		return nil, settings, fmt.Errorf("failed to open config file: %v", err)
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

		// Settings: key = value (a "=" inside a trailing "# comment" does not count)
		uncommented, _, _ := strings.Cut(line, "#")
		if strings.Contains(uncommented, "=") {
			if err := parseSettingLine(line, &settings); err != nil {
				return nil, settings, fmt.Errorf("line %d: %v", lineNum, err)
			}
			continue
		}

		// Parse line format: listen_host:listen_port:target_host:target_port
		parts := strings.Split(line, ":")
		if len(parts) != 4 {
			return nil, settings, fmt.Errorf("line %d: invalid format, expected 'listen_host:listen_port:target_host:target_port', got '%s'", lineNum, line)
		}

		listenHost := strings.TrimSpace(parts[0])
		listenPortStr := strings.TrimSpace(parts[1])
		targetHost := strings.TrimSpace(parts[2])
		targetPortStr := strings.TrimSpace(parts[3])

		// Validate and parse listen port
		listenPort, err := strconv.Atoi(listenPortStr)
		if err != nil || listenPort < 1 || listenPort > 65535 {
			return nil, settings, fmt.Errorf("line %d: invalid listen port '%s', must be number between 1-65535", lineNum, listenPortStr)
		}

		// Validate target host
		if targetHost == "" {
			return nil, settings, fmt.Errorf("line %d: target host cannot be empty", lineNum)
		}

		// Validate and parse target port
		if strings.Contains(targetPortStr, "#") {
			targetPortStr = strings.Split(targetPortStr, "#")[0]
		}
		targetPortStr = strings.TrimSpace(targetPortStr)
		targetPortNum, err := strconv.Atoi(targetPortStr)
		if err != nil || targetPortNum < 1 || targetPortNum > 65535 {
			return nil, settings, fmt.Errorf("line %d: invalid target port '%s', must be number between 1-65535", lineNum, targetPortStr)
		}

		// Create proxy configuration (same for all entries)
		proxyConfig, err := parseProxyConfig(socksProxy, socksHost, socksPort, socksUser, socksPass)
		if err != nil {
			return nil, settings, fmt.Errorf("line %d: invalid SOCKS5 proxy configuration: %v", lineNum, err)
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
		return nil, settings, fmt.Errorf("error reading config file: %v", err)
	}

	return configs, settings, nil
}

// addEntryToConfigFile adds a new configuration entry to the specified config file.
// The listen host is checked against the bind policy (checkListenHost with
// allowPublic): a non-loopback host is still added, with a warning, because the
// policy is applied when the file is run (-allow-public-bind or allow_public_bind).
func addEntryToConfigFile(configFile, listenHost string, listenPort int, targetAddr string, allowPublic bool) {
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
	if err := checkListenHost(listenHost, allowPublic); err != nil {
		log.Printf("WARNING: %v", err)
		log.Printf("WARNING: the entry is added anyway; 'tcp2tor -config %s' refuses to start it without the setting above", configFile)
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
	timeout := time.Duration(timeoutSec) * time.Second

	var dialer proxy.Dialer
	var err error

	if config.SocksAuth != nil {
		if verbose {
			log.Printf("Creating SOCKS5 dialer with authentication to %s", proxyAddr)
		}
		dialer, err = proxy.SOCKS5("tcp", proxyAddr, config.SocksAuth, socksForwardDialer(timeout))
	} else {
		if verbose {
			log.Printf("Creating SOCKS5 dialer without authentication to %s", proxyAddr)
		}
		dialer, err = proxy.SOCKS5("tcp", proxyAddr, nil, socksForwardDialer(timeout))
	}

	if err != nil {
		return fmt.Errorf("failed to create SOCKS5 dialer: %v", err)
	}

	// Test connection
	targetAddr := fmt.Sprintf("%s:%s", targetHost, targetPort)
	if verbose {
		log.Printf("Testing connection to %s through SOCKS5 proxy...", targetAddr)
	}

	conn, err := dialThroughSOCKS(dialer, targetAddr, timeout)
	if err != nil {
		return fmt.Errorf("failed to connect to %s through SOCKS5 proxy: %v", targetAddr, err)
	}
	defer conn.Close()

	if verbose {
		log.Printf("Successfully connected to %s", targetAddr)
	}

	return nil
}

// socksForwardDialer returns the dialer used to reach the SOCKS5 proxy itself, so that the TCP
// connect to the proxy is bounded by the -timeout value. A timeout <= 0 means 30 s (the flag default).
func socksForwardDialer(timeout time.Duration) proxy.Dialer {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &net.Dialer{Timeout: timeout}
}

// dialThroughSOCKS dials addr through the SOCKS5 dialer d and gives up after timeout, covering the
// proxy connect, the SOCKS5 handshake and the CONNECT reply. A timeout <= 0 means 30 s (the flag default).
// Dialers that implement proxy.ContextDialer (proxy.SOCKS5 does) get a context deadline; any other
// dialer runs in a goroutine, and a connection that arrives after the timeout is closed.
func dialThroughSOCKS(d proxy.Dialer, addr string, timeout time.Duration) (net.Conn, error) {
	return dialThroughSOCKSContext(context.Background(), d, addr, timeout)
}

// dialThroughSOCKSContext is dialThroughSOCKS that also gives up when parent is cancelled
// (the proxy server cancels it on Stop).
func dialThroughSOCKSContext(parent context.Context, d proxy.Dialer, addr string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, "tcp", addr)
	}

	type dialResult struct {
		conn net.Conn
		err  error
	}
	resCh := make(chan dialResult)
	abandoned := make(chan struct{})
	go func() {
		conn, err := d.Dial("tcp", addr)
		select {
		case resCh <- dialResult{conn: conn, err: err}:
		case <-abandoned:
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()

	select {
	case res := <-resCh:
		return res.conn, res.err
	case <-ctx.Done():
		close(abandoned)
		return nil, ctx.Err() // context.DeadlineExceeded on timeout, context.Canceled on Stop
	}
}

// errServerStopped is returned by Listen after Stop has been called.
var errServerStopped = errors.New("proxy server stopped")

// ProxyServer handles the TCP proxy functionality.
//
// Call Listen (synchronously), then Serve (usually in a goroutine). Stop may be called from any
// goroutine, at any time and more than once; a Stop before Listen makes Listen fail and Serve
// return nil.
type ProxyServer struct {
	ListenAddr  string
	TargetHost  string
	TargetPort  string
	ProxyConfig *ProxyConfig
	Timeout     time.Duration
	Verbose     bool
	MaxConns    int           // concurrently forwarded connections; <= 0 means defaultMaxConns
	IdleTimeout time.Duration // close a forwarded connection after this long without traffic; <= 0 means defaultIdleTimeout

	// dialTarget, when set, replaces the SOCKS5 dial to the target (used by tests).
	dialTarget func(ctx context.Context, addr string) (net.Conn, error)

	mu       sync.Mutex
	listener net.Listener
	stopped  bool
	ctx      context.Context // cancelled by Stop; aborts dials in progress
	cancel   context.CancelFunc
	sem      chan struct{}         // connection slots, capacity MaxConns
	conns    map[net.Conn]struct{} // live client and target connections, closed by Stop
	stopOnce sync.Once
	handlers sync.WaitGroup
}

// initLocked creates the lazily initialised state. s.mu must be held.
func (s *ProxyServer) initLocked() {
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	if s.sem == nil {
		n := s.MaxConns
		if n <= 0 {
			n = defaultMaxConns
		}
		s.sem = make(chan struct{}, n)
	}
}

// idleTimeout returns the effective idle timeout.
func (s *ProxyServer) idleTimeout() time.Duration {
	if s.IdleTimeout <= 0 {
		return defaultIdleTimeout
	}
	return s.IdleTimeout
}

// serverContext returns the server context that Stop cancels.
func (s *ProxyServer) serverContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	return s.ctx
}

// Listen binds ListenAddr. It fails after Stop or when the server is already listening.
func (s *ProxyServer) Listen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return errServerStopped
	}
	if s.listener != nil {
		return fmt.Errorf("already listening on %s", s.ListenAddr)
	}
	ln, err := net.Listen("tcp", s.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", s.ListenAddr, err)
	}
	s.initLocked()
	s.listener = ln
	log.Printf("tcp2tor proxy listening on %s (max %d connections, idle timeout %v)", ln.Addr(), cap(s.sem), s.idleTimeout())
	log.Printf("Forwarding connections to %s:%s through SOCKS5 proxy %s:%d",
		s.TargetHost, s.TargetPort, s.ProxyConfig.SocksHost, s.ProxyConfig.SocksPort)
	return nil
}

// Addr returns the bound listen address, or nil before Listen.
func (s *ProxyServer) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// acceptRetryMin and acceptRetryMax bound the pause after a failed Accept (net/http
// style: it starts at acceptRetryMin and doubles up to acceptRetryMax).
const (
	acceptRetryMin = 5 * time.Millisecond
	acceptRetryMax = 1 * time.Second
)

// nextAcceptDelay returns the pause after a failed Accept given the previous one (0
// after a successful Accept).
func nextAcceptDelay(prev time.Duration) time.Duration {
	if prev <= 0 {
		return acceptRetryMin
	}
	if next := prev * 2; next < acceptRetryMax {
		return next
	}
	return acceptRetryMax
}

// retryableAcceptErr reports whether a failed Accept is worth retrying: the net/http
// rule, a net.Error that is a timeout or temporary (e.g. EMFILE/ENFILE, too many open
// files). Temporary is checked through an interface because net.Error.Temporary is
// deprecated, but it is still what syscall.Errno reports for those errors.
func retryableAcceptErr(err error) bool {
	var ne net.Error
	if !errors.As(err, &ne) {
		return false
	}
	if ne.Timeout() {
		return true
	}
	if t, ok := ne.(interface{ Temporary() bool }); ok {
		return t.Temporary()
	}
	return false
}

// Serve accepts connections on the listener created by Listen until Stop is called or the
// listener is closed (both return nil). A timeout or temporary Accept error (e.g. too
// many open files, retryableAcceptErr) is logged and retried after a pause that grows from
// acceptRetryMin to acceptRetryMax; Stop ends the pause. Any other Accept error ends Serve
// and is returned. Connections beyond MaxConns are closed right away. Serve returns after
// every connection handler has finished.
func (s *ProxyServer) Serve() error {
	s.mu.Lock()
	ln := s.listener
	stopped := s.stopped
	s.initLocked()
	sem := s.sem
	s.mu.Unlock()
	if ln == nil {
		if stopped {
			return nil
		}
		return errors.New("serve called before listen")
	}

	var serveErr error
	ctx := s.serverContext()
	var retryDelay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.isStopped() || errors.Is(err, net.ErrClosed) {
				break
			}
			if !retryableAcceptErr(err) {
				serveErr = fmt.Errorf("failed to accept connection: %v", err)
				break
			}
			retryDelay = nextAcceptDelay(retryDelay)
			log.Printf("Accept error on %s: %v; retrying in %v", s.ListenAddr, err, retryDelay)
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done(): // Stop
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		retryDelay = 0

		select {
		case sem <- struct{}{}:
		default:
			log.Printf("Connection limit (%d) reached on %s, refusing %s", cap(sem), s.ListenAddr, conn.RemoteAddr())
			_ = conn.Close()
			continue
		}

		// Handle connection in goroutine; Stop closes its connections and Serve waits for it
		s.handlers.Add(1)
		go func(c net.Conn) {
			defer s.handlers.Done()
			defer func() { <-sem }()
			s.handleConnection(c)
		}(conn)
	}

	s.Stop() // closes the listener and all live connections (no-op after an earlier Stop)
	s.handlers.Wait()
	if serveErr == nil {
		log.Printf("Proxy server %s shut down", s.ListenAddr)
	}
	return serveErr
}

// isStopped reports whether Stop has been called.
func (s *ProxyServer) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// Stop stops the proxy server: it closes the listener, cancels dials in progress and closes all
// forwarded connections. It is safe to call concurrently, before Listen and more than once.
func (s *ProxyServer) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		s.initLocked()
		ln := s.listener
		conns := make([]net.Conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		cancel := s.cancel
		s.mu.Unlock()

		cancel()
		if ln != nil {
			if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("Error closing listener %s: %v", s.ListenAddr, err)
			}
		}
		for _, c := range conns {
			_ = c.Close()
		}
	})
}

// trackConn registers a live connection so that Stop can close it. It returns false (and the
// caller must close c) when the server is already stopped.
func (s *ProxyServer) trackConn(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	s.initLocked()
	s.conns[c] = struct{}{}
	return true
}

// untrackConn removes a connection registered by trackConn.
func (s *ProxyServer) untrackConn(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

// capturingDialer is the forward dialer handed to proxy.SOCKS5. It remembers the raw TCP
// connection to the SOCKS5 proxy, because the connection proxy.SOCKS5 returns wraps it in a type
// that does not expose CloseWrite.
type capturingDialer struct {
	forward proxy.Dialer
	mu      sync.Mutex
	conn    net.Conn
}

func (d *capturingDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *capturingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var (
		c   net.Conn
		err error
	)
	if cd, ok := d.forward.(proxy.ContextDialer); ok {
		c, err = cd.DialContext(ctx, network, addr)
	} else {
		c, err = d.forward.Dial(network, addr)
	}
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.conn = c
	d.mu.Unlock()
	return c, nil
}

func (d *capturingDialer) raw() net.Conn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn
}

// halfCloseConn is a connection through the SOCKS5 proxy whose CloseWrite half-closes the
// underlying TCP connection to the proxy.
type halfCloseConn struct {
	net.Conn
	raw net.Conn
}

func (c *halfCloseConn) CloseWrite() error {
	return closeWrite(c.raw)
}

// closeWrite half-closes c (shuts down its sending side) when c supports it.
func closeWrite(c net.Conn) error {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.New("half-close not supported")
}

// dial connects to the target through the SOCKS5 proxy, or through dialTarget when set.
func (s *ProxyServer) dial(ctx context.Context, targetAddr string) (net.Conn, error) {
	if s.dialTarget != nil {
		return s.dialTarget(ctx, targetAddr)
	}

	proxyAddr := fmt.Sprintf("%s:%d", s.ProxyConfig.SocksHost, s.ProxyConfig.SocksPort)
	fwd := &capturingDialer{forward: socksForwardDialer(s.Timeout)}
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, s.ProxyConfig.SocksAuth, fwd)
	if err != nil {
		return nil, fmt.Errorf("failed to create SOCKS5 dialer: %v", err)
	}
	conn, err := dialThroughSOCKSContext(ctx, dialer, targetAddr, s.Timeout)
	if err != nil {
		return nil, err
	}
	if raw := fwd.raw(); raw != nil {
		return &halfCloseConn{Conn: conn, raw: raw}, nil
	}
	return conn, nil
}

// handleConnection handles a single client connection
func (s *ProxyServer) handleConnection(clientConn net.Conn) {
	defer func() { _ = clientConn.Close() }()
	if !s.trackConn(clientConn) {
		return
	}
	defer s.untrackConn(clientConn)

	clientAddr := clientConn.RemoteAddr().String()
	if s.Verbose {
		log.Printf("New connection from %s", clientAddr)
	}

	// Connect to target through SOCKS5 proxy
	targetAddr := fmt.Sprintf("%s:%s", s.TargetHost, s.TargetPort)
	if s.Verbose {
		log.Printf("Connecting to %s through SOCKS5 proxy for client %s", targetAddr, clientAddr)
	}

	targetConn, err := s.dial(s.serverContext(), targetAddr)
	if err != nil {
		log.Printf("Failed to connect to %s for client %s: %v", targetAddr, clientAddr, err)
		return
	}
	defer func() { _ = targetConn.Close() }()
	if !s.trackConn(targetConn) {
		return
	}
	defer s.untrackConn(targetConn)

	if s.Verbose {
		log.Printf("Connected to %s for client %s", targetAddr, clientAddr)
	}

	s.forward(clientConn, targetConn, clientAddr)

	if s.Verbose {
		log.Printf("Connection closed for client %s", clientAddr)
	}
}

// copyResult is the outcome of one direction of a forwarded connection.
type copyResult struct {
	dir     string
	written int64
	err     error
}

// forward copies data in both directions until both are done. When one side ends its stream
// cleanly (EOF), the other side is half-closed (CloseWrite) so its peer sees EOF too, and the
// remaining direction keeps running until it ends or is idle for one idle timeout. On an error
// or an idle timeout both connections are closed at once.
func (s *ProxyServer) forward(clientConn, targetConn net.Conn, clientAddr string) {
	idle := s.idleTimeout()
	act := &activity{}
	act.touch()

	results := make(chan copyResult, 2)
	pipe := func(dir string, dst, src net.Conn) {
		n, err := copyWithIdle(dst, src, idle, act)
		if err == nil {
			// src sent EOF: pass it on so that dst's peer sees a clean end of stream
			if cerr := closeWrite(dst); cerr != nil && s.Verbose {
				log.Printf("%s half-close for %s: %v", dir, clientAddr, cerr)
			}
		}
		results <- copyResult{dir: dir, written: n, err: err}
	}
	go pipe("Client->Target", targetConn, clientConn)
	go pipe("Target->Client", clientConn, targetConn)

	closeBoth := func() {
		_ = clientConn.Close()
		_ = targetConn.Close()
	}

	first := <-results
	s.logCopyResult(clientAddr, first)
	if first.err != nil {
		closeBoth()
	}
	// After a clean half-close the remaining direction needs no extra timer: copyWithIdle bounds
	// every blocking read and write by idle, and the finished direction no longer touches act,
	// so the remaining one ends after one idle period without its own traffic.
	s.logCopyResult(clientAddr, <-results)
}

// logCopyResult logs a failed direction in verbose mode (closed connections are not errors).
func (s *ProxyServer) logCopyResult(clientAddr string, r copyResult) {
	if s.Verbose && r.err != nil && !errors.Is(r.err, net.ErrClosed) {
		log.Printf("%s copy error for %s: %v (wrote %d bytes)", r.dir, clientAddr, r.err, r.written)
	}
}

// idleRetries counts reads that hit their idle deadline but were retried because the other
// direction moved data within the idle timeout (read by tests).
var idleRetries atomic.Int64

// activity records the time of the last traffic on a forwarded connection, in either direction.
type activity struct {
	last atomic.Int64 // UnixNano
}

func (a *activity) touch() {
	a.last.Store(time.Now().UnixNano())
}

func (a *activity) since() time.Duration {
	return time.Since(time.Unix(0, a.last.Load()))
}

// copyWithIdle copies src to dst like io.Copy and returns nil when src reaches EOF. Read and
// write deadlines are refreshed on every chunk; a read that times out while the other direction
// (sharing act) moved data within idle is retried, so the connection is only closed after idle
// without traffic in either direction.
func copyWithIdle(dst, src net.Conn, idle time.Duration, act *activity) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		if err := src.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return written, err
		}
		nr, rerr := src.Read(buf)
		if nr > 0 {
			act.touch()
			if err := dst.SetWriteDeadline(time.Now().Add(idle)); err != nil {
				return written, err
			}
			nw, werr := dst.Write(buf[:nr])
			written += int64(nw)
			if werr != nil {
				return written, werr
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
			act.touch()
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return written, nil
			}
			if errors.Is(rerr, os.ErrDeadlineExceeded) {
				if act.since() < idle {
					idleRetries.Add(1)
					continue // the other direction is active
				}
				return written, fmt.Errorf("idle for %v: %w", idle, rerr)
			}
			return written, rerr
		}
	}
}
