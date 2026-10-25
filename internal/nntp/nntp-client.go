package nntp

// nntp provides NNTP client functionality for go-pugleaf.

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/textproto"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
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
	// AuthRequired is the server's 480: authentication required (RFC 4643), also sent
	// when a login expired mid-session. The client maps it to ErrAuthRequired.
	AuthRequired int = 480

	// DefaultConnExpire is the default connection expiration duration.
	DefaultConnExpire = 25 * time.Second

	// DefaultClientReadTimeout is the default idle timeout per Read on a connected client socket.
	DefaultClientReadTimeout = 2 * time.Minute
	// DefaultClientWriteTimeout is the default idle timeout per Write on a connected client socket.
	DefaultClientWriteTimeout = 2 * time.Minute
	// DefaultAuthFailureBackoff is the default sleep after a failed AUTHINFO (the socket is closed first).
	DefaultAuthFailureBackoff = 15 * time.Second
	// DefaultPoolWaitTimeout is the default time Pool.Get waits for a free connection slot.
	DefaultPoolWaitTimeout = 30 * time.Second

	// DefaultClientMaxLineBytes is the default cap of one response line (BackendConfig.MaxResponseLineBytes).
	DefaultClientMaxLineBytes = 1 << 20
	// DefaultClientMaxArticleBytes is the default budget of one ARTICLE/HEAD/BODY response
	// (BackendConfig.MaxArticleResponseBytes).
	DefaultClientMaxArticleBytes = 64 << 20
	// DefaultClientMaxListBytes is the default budget of one LIST/XOVER/XHDR/LISTGROUP response
	// (BackendConfig.MaxListResponseBytes).
	DefaultClientMaxListBytes = 512 << 20

	// MaxReadLines is the maximum lines to read per response (allow for large group lists).
	MaxReadLines = 1024 * 1024
)

// ErrAuthRequired is returned when the server answers 480 (authentication required)
// to a command; the connection is force-closed.
var ErrAuthRequired = errors.New("authentication required")

// ErrAuthRejected wraps an AUTHINFO USER/PASS reply other than 281/381.
var ErrAuthRejected = errors.New("authentication rejected")

// DefaultBufferTX is the default buffer size for bufio.Writer
// when sending articles via POST/TAKETHIS/IHAVE commands.
var DefaultBufferTX int = 16 * 1024

// BackendConn represents an NNTP connection to a server.
// It manages the connection state, authentication, and provides methods
// for interacting with the NNTP server.
type BackendConn struct {
	conn     net.Conn      // the deadlineConn while connected (same as dconn)
	dconn    *deadlineConn // socket wrapper with idle deadlines (nil when not connected)
	closing  atomic.Bool   // set once by ForceCloseConn: the conn is on its way out, never hand it out again
	TextConn *textproto.Conn
	Backend  *BackendConfig
	mux      sync.RWMutex
	Pool     *Pool // link to parent pool

	// Connection state
	isConnected chan struct{}
	hasClosed   chan struct{}
	ModeReader  bool
	ModeStream  bool
	forceClose  bool
	created     time.Time
	lastUsed    time.Time
	// INN allows switching to mode reader when mode stream is active
	// but INN does not allow switching to mode stream when mode reader is active
	// when using check/takethis and we got a mode reader connection from pool
	// we must set ForceClose to true and Put the connection back in pool and get a new one...

	greetingCode int // the server's welcome code (200: posting allowed, 201: no posting)

	// counters hold the octets this connection moved in both directions, the
	// raw (NNTP) stream and the wire stream; they differ only with COMPRESS
	// DEFLATE (nntp-client-compress.go).
	counters byteCounters
}

// TxBytesRaw returns the octets this connection sent as the NNTP layer wrote
// them (before compression).
func (c *BackendConn) TxBytesRaw() int64 { return c.counters.txRaw.Load() }

// TxBytesWire returns the octets this connection sent over the socket (after
// compression); without COMPRESS DEFLATE it equals TxBytesRaw.
func (c *BackendConn) TxBytesWire() int64 { return c.counters.txWire.Load() }

// RxBytesRaw returns the octets this connection received as the NNTP layer
// read them (after decompression).
func (c *BackendConn) RxBytesRaw() int64 { return c.counters.rxRaw.Load() }

// RxBytesWire returns the octets this connection received over the socket
// (still compressed); without COMPRESS DEFLATE it equals RxBytesRaw.
func (c *BackendConn) RxBytesWire() int64 { return c.counters.rxWire.Load() }

// BackendConfig holds configuration for an NNTP client
type BackendConfig struct {
	Host           string        // hostname or IP address of the NNTP server
	Port           int           // port number for the NNTP server
	SSL            bool          // whether to use SSL/TLS
	Username       string        // username for authentication
	Password       string        // password for authentication
	Compress       bool          // send COMPRESS DEFLATE (RFC 8054) after the greeting/AUTHINFO; a refusal keeps the connection plain
	ConnectTimeout time.Duration // timeout for dial, TLS handshake, greeting and AUTHINFO (each Read/Write)
	// ReadTimeout is the idle timeout per Read after connect; 0 = DefaultClientReadTimeout, <0 = none.
	ReadTimeout time.Duration
	// WriteTimeout is the idle timeout per Write after connect; 0 = DefaultClientWriteTimeout, <0 = none.
	WriteTimeout time.Duration
	// AuthFailureBackoff is the sleep after a failed AUTHINFO; 0 = DefaultAuthFailureBackoff, <0 = none.
	AuthFailureBackoff time.Duration
	// IdleExpire is the idle expiry of pooled connections; 0 = DefaultConnExpire, <0 = never expire.
	IdleExpire time.Duration
	// PoolWaitTimeout bounds Pool.Get's wait for a free slot; 0 = DefaultPoolWaitTimeout, <0 = wait for ctx only
	// (Pool.Get uses context.Background(), so a negative value can block it forever: use GetContext then).
	PoolWaitTimeout time.Duration
	MaxConns        int              // maximum number of connections to this backend
	Provider        *config.Provider // link to provider config
	// MaxResponseLineBytes caps one response line; 0 = DefaultClientMaxLineBytes, <0 = unlimited.
	MaxResponseLineBytes int
	// MaxArticleResponseBytes caps one ARTICLE/HEAD/BODY response; 0 = DefaultClientMaxArticleBytes, <0 = unlimited.
	MaxArticleResponseBytes int64
	// MaxListResponseBytes caps one LIST/XOVER/XHDR/LISTGROUP response; 0 = DefaultClientMaxListBytes, <0 = unlimited.
	MaxListResponseBytes int64
	// Proxy configuration fields
	ProxyEnabled  bool   // whether to use proxy for connections
	ProxyType     string // proxy type: socks4, socks5
	ProxyHost     string // proxy server hostname/IP
	ProxyPort     int    // proxy server port
	ProxyUsername string // proxy authentication username
	ProxyPassword string // proxy authentication password
	Mux           sync.Mutex
}

// pickDuration resolves a duration setting: 0 selects def, a negative value
// disables (returns 0), anything else is used as is.
func pickDuration(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	if v < 0 {
		return 0
	}
	return v
}

// providerLabel names the backend in logs and errors without dereferencing a
// nil Provider: the provider name when set, else host:port.
func (b *BackendConfig) providerLabel() string {
	if b.Provider != nil && b.Provider.Name != "" {
		return b.Provider.Name
	}
	return net.JoinHostPort(b.Host, strconv.Itoa(b.Port))
}

// cacheHost is the host used for the newsgroup list cache file name: the
// provider host when set, else the backend host.
func (b *BackendConfig) cacheHost() string {
	if b.Provider != nil && b.Provider.Host != "" {
		return b.Provider.Host
	}
	return b.Host
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
	Name       string
	Count      int64
	First      int64
	Last       int64
	FetchStart int64
	FetchEnd   int64
	PostingOK  bool
	Status     string
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
		Backend:     backend,
		created:     time.Now(),
		isConnected: make(chan struct{}, 1),
		hasClosed:   make(chan struct{}, 1),
	}
}

// Connect establishes connection to the NNTP server
func (c *BackendConn) Connect() error {
	c.Backend.Mux.Lock()
	if c.Backend.ConnectTimeout == 0 {
		c.Backend.ConnectTimeout = config.DefaultConnectTimeout
	}
	c.Backend.Mux.Unlock()

	c.mux.Lock()
	defer c.mux.Unlock()
	if c.IsConnected() {
		log.Printf("[NNTP-CONN] ERROR: Tried Connect() but this conn is already connected to %s:%d hasClosed=%d", c.Backend.Host, c.Backend.Port, len(c.hasClosed))
		return fmt.Errorf("connection already established to %s:%d", c.Backend.Host, c.Backend.Port)
	}

	// Check if this is a .onion address and automatically enable Tor if not already configured
	if IsOnionAddress(c.Backend.Host) && !c.Backend.ProxyEnabled {
		log.Printf("[NNTP-PROXY] Detected .onion address %s but TOR proxy is not configured!", c.Backend.Host)
		return fmt.Errorf("error in Connect: TOR proxy is not configured: %s", c.Backend.providerLabel())
	}

	// Build server address
	serverAddr := net.JoinHostPort(c.Backend.Host, fmt.Sprintf("%d", c.Backend.Port))

	// Create proxy dialer
	proxyDialer := NewProxyDialer(c.Backend)

	// Set connection timeout
	var conn net.Conn
	var err error

	if c.Backend.SSL {
		tlsConfig := &tls.Config{
			ServerName: c.Backend.Host,
			MinVersion: tls.VersionTLS12,
		}

		// Use proxy-aware TLS dialer
		conn, err = proxyDialer.DialTLS("tcp", serverAddr, tlsConfig)
	} else {
		// Use proxy-aware dialer
		conn, err = proxyDialer.Dial("tcp", serverAddr)
	}

	if err != nil {
		// Log proxy information if proxy was attempted
		if c.Backend.ProxyEnabled {
			log.Printf("[NNTP-PROXY] Failed to connect to %s via %s proxy %s:%d: %v", serverAddr, c.Backend.ProxyType, c.Backend.ProxyHost, c.Backend.ProxyPort, err)
		}
		return fmt.Errorf("failed to connect to %s: %w", serverAddr, err)
	}

	// Log successful proxy connection
	if c.Backend.ProxyEnabled {
		log.Printf("[NNTP-PROXY] Successfully connected to %s via %s proxy %s:%d", serverAddr, c.Backend.ProxyType, c.Backend.ProxyHost, c.Backend.ProxyPort)
	}

	// Until the connection is ready, every Read/Write (greeting, AUTHINFO) is
	// bounded by ConnectTimeout; the idle timeouts are set at the end.
	dc := newDeadlineConn(conn, c.Backend.ConnectTimeout, c.Backend.ConnectTimeout)
	maxLine := c.Backend.MaxResponseLineBytes // < 0: unlimited (setMaxLine treats <= 0 as no cap)
	if maxLine == 0 {
		maxLine = DefaultClientMaxLineBytes
	}
	dc.setMaxLine(maxLine)
	dc.setCounters(&c.counters)
	c.dconn = dc
	c.conn = dc
	c.TextConn = textproto.NewConn(dc)

	// Read welcome message: 200 (posting allowed) or 201 (no posting), RFC 3977 §5.1.1
	code, message, err := c.TextConn.ReadCodeLine(2)
	if err != nil && code == 0 {
		log.Printf("[NNTP-CONN] Error reading welcome from %s:%d: %v", c.Backend.Host, c.Backend.Port, err)
		c.abortConnect()
		return err
	}

	if code != NNTPWelcomeCodeMin && code != NNTPWelcomeCodeMax {
		log.Printf("[NNTP-CONN] Invalid welcome code %d from %s:%d: %s", code, c.Backend.Host, c.Backend.Port, message)
		c.abortConnect()
		return fmt.Errorf("unexpected welcome code %d: %s", code, message)
	}
	c.greetingCode = code

	//log.Printf("[NNTP-CONN] Successfully connected to %s:%d with welcome code %d", c.Backend.Host, c.Backend.Port, code)

	c.lastUsed = time.Now()

	// Authenticate if credentials provided
	if c.Backend.Username != "" {
		//log.Printf("[NNTP-AUTH] Attempting authentication for user '%s' on %s:%d", c.Backend.Username, c.Backend.Host, c.Backend.Port)
		if err := c.authenticate(); err != nil {
			backoff := pickDuration(c.Backend.AuthFailureBackoff, DefaultAuthFailureBackoff)
			log.Printf("[NNTP-AUTH] Authentication FAILED for user '%s' on %s:%d err: %v (sleep %v)", c.Backend.Username, c.Backend.Host, c.Backend.Port, err, backoff)
			c.abortConnect() // close the socket before sleeping
			if backoff > 0 {
				time.Sleep(backoff)
			}
			return err
		}
		//log.Printf("[NNTP-AUTH] Authentication SUCCESS for user '%s' on %s:%d", c.Backend.Username, c.Backend.Host, c.Backend.Port)
	} else {
		//log.Printf("[NNTP-AUTH] No credentials provided, skipping authentication for %s:%d", c.Backend.Host, c.Backend.Port)
	}

	// COMPRESS DEFLATE (RFC 8054 §2.2.2: after authentication). A refusal is
	// logged once per provider and keeps the connection plain; only an I/O or
	// protocol error aborts the connect (plan fetcher-compress-deflate).
	if c.Backend.Compress {
		if err := c.startCompress(); err != nil {
			log.Printf("[NNTP-CONN] COMPRESS DEFLATE failed on %s:%d: %v", c.Backend.Host, c.Backend.Port, err)
			c.abortConnect()
			return err
		}
	}

	dc.setTimeouts(pickDuration(c.Backend.ReadTimeout, DefaultClientReadTimeout), pickDuration(c.Backend.WriteTimeout, DefaultClientWriteTimeout))
	c.isConnected <- struct{}{}
	return nil
}

// abortConnect closes the socket of a half-open connection (dialed, but the
// greeting or AUTHINFO failed) and forgets it. The caller holds c.mux.
func (c *BackendConn) abortConnect() {
	if c.TextConn != nil {
		_ = c.TextConn.Close() // closes the socket underneath
	} else if c.conn != nil {
		_ = c.conn.Close()
	}
	c.TextConn, c.conn, c.dconn = nil, nil, nil
}

// authenticate performs NNTP authentication (RFC 4643 §2.3): 281 directly
// after AUTHINFO USER is success without a password, 381 asks for AUTHINFO
// PASS. Other replies wrap ErrAuthRejected; I/O errors are returned as is.
func (c *BackendConn) authenticate() error {
	// Send AUTHINFO USER (RFC 4643: no CR/LF/NUL; spaces are allowed)
	if err := validTokenArg(c.Backend.Username, true); err != nil {
		return fmt.Errorf("AUTHINFO USER: %w", err)
	}
	id, err := c.sendCmd("AUTHINFO USER %s", c.Backend.Username)
	if err != nil {
		return err
	}

	c.TextConn.StartResponse(id)
	code, message, err := c.TextConn.ReadCodeLine(0)
	c.TextConn.EndResponse(id)

	if err != nil && code == 0 {
		return err
	}

	switch code {
	case NNTPAuthSuccess:
		return nil // accepted without a password
	case NNTPMoreInfoCode:
		// send PASS below
	default:
		return fmt.Errorf("%w: AUTHINFO USER: %d %s", ErrAuthRejected, code, message)
	}

	// Send AUTHINFO PASS
	if err := validTokenArg(c.Backend.Password, true); err != nil {
		return fmt.Errorf("AUTHINFO PASS: %w", err)
	}
	id, err = c.sendCmd("AUTHINFO PASS %s", c.Backend.Password)
	if err != nil {
		return err
	}

	c.TextConn.StartResponse(id)
	code, message, err = c.TextConn.ReadCodeLine(0)
	c.TextConn.EndResponse(id)

	if err != nil && code == 0 {
		return err
	}

	if code != NNTPAuthSuccess {
		return fmt.Errorf("%w: AUTHINFO PASS: %d %s", ErrAuthRejected, code, message)
	}

	return nil
}

// CloseFromPoolOnly closes a raw NNTP connection
func (c *BackendConn) CloseFromPoolOnly() error {
	c.closeFromPool()
	return nil
}

// closeFromPool closes the connection and reports whether this call did the
// connected→closed transition (false when it was not connected). Pool
// counters must change only on true.
func (c *BackendConn) closeFromPool() bool {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return false
	}

	if c.TextConn != nil {
		_ = c.TextConn.Close() // closes the socket underneath
	}
	if c.conn != nil {
		_ = c.conn.Close() // already closed via TextConn: error ignored
	}

	close(c.hasClosed)
	<-c.isConnected
	log.Printf("Closed NNTP Connection to %s", c.Backend.Host)
	return true
}

// ForceCloseConn marks the connection as unusable and closes it
// asynchronously (callers may hold c.mux or other locks). It is idempotent:
// only the first call per connection starts the closing goroutine, which ends
// after the close. Pool.Get never hands out a conn once this was called.
func (c *BackendConn) ForceCloseConn() {
	if c.closing.Swap(true) {
		return
	}
	go func() {
		c.mux.Lock()
		c.forceClose = true
		c.mux.Unlock()
		if c.Pool != nil {
			_ = c.Pool.Put(c)
		} else {
			c.closeFromPool()
		}
	}()
}

func (c *BackendConn) IsConnected() bool {
	if common.IsClosedChannel(c.hasClosed) {
		return false
	}
	if len(c.isConnected) == 0 {
		return false
	}
	/*
		c.mux.Lock()
		//log.Printf("IsConnected check: connected=%v conn=%v", c.connected, c.conn)
		defer c.mux.Unlock()
		if !c.connected {
			return false
		}
		if c.conn == nil {
			return false
		}
	*/
	return true
}

// UpdateLastUsed updates the last used timestamp
func (c *BackendConn) UpdateLastUsed() {
	c.mux.Lock()
	c.lastUsed = time.Now()
	c.mux.Unlock()
}

func (c *BackendConn) GetBufSize(size int) int {
	if size+1024 <= DefaultBufferTX {
		return size + 1024
	}
	return DefaultBufferTX // hardcoded default max buffer size
}

func (c *BackendConn) Lock() {
	c.mux.Lock()
}
func (c *BackendConn) Unlock() {
	c.mux.Unlock()
}
