package nntp

// nntp provides NNTP client functionality for go-pugleaf.

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/textproto"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
)

const (
	// NNTPWelcomeCodeMin is the minimum welcome code for NNTP servers.
	NNTPWelcomeCodeMin int = 200
	// NNTPWelcomeCodeMax is the maximum welcome code for NNTP servers.
	NNTPWelcomeCodeMax int = 201
	// NNTPMoreInfoCode indicates more information is required (e.g., password).
	NNTPMoreInfoCode int = 381
	// NNTPAuthSuccess indicates successful authentication.
	NNTPAuthSuccess int = 281

	// ArticleFollows indicates that an article follows (multi-line).
	ArticleFollows int = 220
	// HeadFollows indicates that the head of an article follows (multi-line).
	HeadFollows int = 221
	// BodyFollows indicates that the body of an article follows (multi-line).
	BodyFollows int = 222
	// ArticleExists indicates that the article exists (no body follows).
	ArticleExists int = 223

	// NoSuchArticle indicates that no such article exists.
	NoSuchArticle int = 430
	// DMCA indicates a DMCA takedown.
	DMCA int = 451

	// DefaultConnExpire is the default connection expiration duration.
	DefaultConnExpire = 25 * time.Second

	// MaxReadLines is the maximum lines to read per response (allow for large group lists).
	MaxReadLines = 500000
)

// BackendConn represents an NNTP connection to a server.
// It manages the connection state, authentication, and provides methods
// for interacting with the NNTP server.
type BackendConn struct {
	conn     net.Conn
	textConn *textproto.Conn
	writer   *bufio.Writer
	Backend  *BackendConfig
	mu       sync.RWMutex
	Pool     *Pool // link to parent pool

	// Connection state
	connected     bool
	authenticated bool
	created       time.Time
	lastUsed      time.Time
}

// BackendConfig holds configuration for an NNTP client
type BackendConfig struct {
	Host           string        // hostname or IP address of the NNTP server
	Port           int           // port number for the NNTP server
	SSL            bool          // whether to use SSL/TLS
	Username       string        // username for authentication
	Password       string        // password for authentication
	ConnectTimeout time.Duration // timeout for establishing a connection
	//ReadTimeout    time.Duration    // timeout for reading from the connection
	//WriteTimeout   time.Duration    // timeout for writing to the connection
	MaxConns int              // maximum number of connections to this backend
	Provider *config.Provider // link to provider config
	Mux      sync.Mutex
}

// Article represents an NNTP article
/*
type Article struct {
	MessageID      string
	Headers        map[string][]string // For quick lookup (lowercase keys)
	Body           []byte
	Size           int64
}
*/

// GroupInfo represents newsgroup information
type GroupInfo struct {
	Name      string
	Count     int64
	First     int64
	Last      int64
	PostingOK bool
}

// OverviewLine represents a line from XOVER command
type OverviewLine struct {
	ArticleNum int64
	Subject    string
	From       string
	Date       string
	MessageID  string
	References string
	Bytes      int64
	Lines      int64
}

// HeaderLine represents a line from XHDR command
type HeaderLine struct {
	ArticleNum int64
	Value      string
}

// NewConn creates a new empty NNTP connection with the provided backend configuration.
func NewConn(backend *BackendConfig) *BackendConn {
	return &BackendConn{
		Backend: backend,
		created: time.Now(),
	}
}

// Connect establishes connection to the NNTP server
func (c *BackendConn) Connect() error {
	c.Backend.Mux.Lock()
	if c.Backend.ConnectTimeout == 0 {
		c.Backend.ConnectTimeout = config.DefaultConnectTimeout
	}
	c.Backend.Mux.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected {
		return nil
	}
	// Build server address
	serverAddr := net.JoinHostPort(c.Backend.Host, fmt.Sprintf("%d", c.Backend.Port))

	// Set connection timeout
	var conn net.Conn
	var err error

	if c.Backend.SSL {
		tlsConfig := &tls.Config{
			ServerName: c.Backend.Host,
			MinVersion: tls.VersionTLS12,
		}
		conn, err = tls.DialWithDialer(&net.Dialer{
			Timeout: c.Backend.ConnectTimeout,
		}, "tcp", serverAddr, tlsConfig)
	} else {
		conn, err = net.DialTimeout("tcp", serverAddr, c.Backend.ConnectTimeout)
	}

	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", serverAddr, err)
	}

	c.conn = conn
	c.textConn = textproto.NewConn(conn)
	c.writer = bufio.NewWriter(conn)

	// Read welcome message
	code, message, err := c.textConn.ReadCodeLine(NNTPWelcomeCodeMin)
	if err != nil {
		if err := c.Pool.CloseConn(c, true); err != nil {
			log.Printf("Failed to close connection: %v", err)
		}
		return fmt.Errorf("failed to read welcome: %w", err)
	}

	if code < NNTPWelcomeCodeMin || code > NNTPWelcomeCodeMax {
		log.Printf("[NNTP-CONN] Invalid welcome code %d from %s:%d: %s", code, c.Backend.Host, c.Backend.Port, message)
		if err := c.Pool.CloseConn(c, true); err != nil {
			log.Printf("[NNTP-CONN] Failed to close connection after invalid welcome: %v", err)
		}
		return fmt.Errorf("unexpected welcome code %d: %s", code, message)
	}

	//log.Printf("[NNTP-CONN] Successfully connected to %s:%d with welcome code %d", c.Backend.Host, c.Backend.Port, code)

	c.connected = true
	c.lastUsed = time.Now()

	// Authenticate if credentials provided
	if c.Backend.Username != "" {
		//log.Printf("[NNTP-AUTH] Attempting authentication for user '%s' on %s:%d", c.Backend.Username, c.Backend.Host, c.Backend.Port)
		if err := c.authenticate(); err != nil {
			log.Printf("[NNTP-AUTH] Authentication FAILED for user '%s' on %s:%d: %v", c.Backend.Username, c.Backend.Host, c.Backend.Port, err)
			if err := c.Pool.CloseConn(c, true); err != nil {
				log.Printf("[NNTP-AUTH] Failed to close connection after auth failure: %v", err)
			}
			return fmt.Errorf("authentication failed: %w", err)
		}
		//log.Printf("[NNTP-AUTH] Authentication SUCCESS for user '%s' on %s:%d", c.Backend.Username, c.Backend.Host, c.Backend.Port)
	} else {
		//log.Printf("[NNTP-AUTH] No credentials provided, skipping authentication for %s:%d", c.Backend.Host, c.Backend.Port)
	}

	return nil
}

// authenticate performs NNTP authentication
func (c *BackendConn) authenticate() error {
	// Send AUTHINFO USER
	id, err := c.textConn.Cmd("AUTHINFO USER %s", c.Backend.Username)
	if err != nil {
		return err
	}

	c.textConn.StartResponse(id)
	code, message, err := c.textConn.ReadCodeLine(NNTPMoreInfoCode)
	c.textConn.EndResponse(id)

	if err != nil {
		return err
	}

	if code != NNTPMoreInfoCode {
		return fmt.Errorf("unexpected response to AUTHINFO USER: %d %s", code, message)
	}

	// Send AUTHINFO PASS
	id, err = c.textConn.Cmd("AUTHINFO PASS %s", c.Backend.Password)
	if err != nil {
		return err
	}

	c.textConn.StartResponse(id)
	code, message, err = c.textConn.ReadCodeLine(NNTPAuthSuccess)
	c.textConn.EndResponse(id)

	if err != nil {
		return err
	}

	if code != NNTPAuthSuccess {
		return fmt.Errorf("authentication failed: %d %s", code, message)
	}

	c.authenticated = true
	return nil
}

// CloseFromPoolOnly closes a raw NNTP connection
func (c *BackendConn) CloseFromPoolOnly() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil
	}

	if c.textConn != nil {
		if err := c.textConn.Close(); err != nil {
			//log.Printf("Error closing text connection: %v", err)
		}
	}

	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			//log.Printf("xx Error closing connection: %v", err)
		}
	}

	c.connected = false
	c.authenticated = false
	c.textConn = nil
	c.conn = nil
	c.writer = nil
	//log.Printf("Closed NNTP Connection to %s", c.Backend.Host)
	return nil
}

// SetReadDeadline sets the read deadline for the connection
func (c *BackendConn) xSetReadDeadline(t time.Time) error {

	if c.conn == nil {
		return fmt.Errorf("connection not established")
	}

	return c.conn.SetReadDeadline(t)
}

// SetReadDeadline sets the read deadline for the connection
func (c *BackendConn) xSetWriteDeadline(t time.Time) error {
	if c.conn == nil {
		return fmt.Errorf("connection not established")
	}

	return c.conn.SetWriteDeadline(t)
}

// UpdateLastUsed updates the last used timestamp
func (c *BackendConn) UpdateLastUsed() {
	c.mu.Lock()
	c.lastUsed = time.Now()
	c.mu.Unlock()
	//c.SetReadDeadline(time.Now().Add(c.Backend.ReadTimeout))
	//c.SetWriteDeadline(time.Now().Add(c.Backend.WriteTimeout))
}
