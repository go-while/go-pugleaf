package nntp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// Forward declaration to avoid circular imports
// Processor interface for article processing
type ArticleProcessor interface {
	ProcessIncomingArticle(article *models.Article) (int, error)
	// CheckMessageID returns history.CasePass (wanted), history.CaseDupes (have it),
	// history.CaseRetry (in progress) or history.CaseError for an offered message-id
	CheckMessageID(messageID string) int
	// FindArticleByMessageID looks in currentGroup first, then in the groups from the history index.
	// A definite miss returns an error wrapping ErrArticleNotFound (nntp-backend-pool.go).
	FindArticleByMessageID(messageID, currentGroup string) (*models.Article, error)
	// XrefLocations returns every group (name) and article number the history index holds for
	// messageID, for the Xref: header in XrefFull mode. nil, nil = nothing known; an error makes
	// the server fall back to XrefPartial for that reply.
	XrefLocations(messageID string) ([]XrefLocation, error)
	// XrefLocationsBatch is XrefLocations for many message-ids at once (OVER/XOVER and
	// HDR/XHDR ranges in XrefFull mode with the batched lookup enabled): one history query per
	// shard table instead of one per id. Ids the index does not know are absent from the map.
	// An error makes the server fall back to per-id XrefLocations for that range.
	XrefLocationsBatch(messageIDs []string) (map[string][]XrefLocation, error)
}

const (
	// NNTP protocol constants
	DOT  = "."
	CR   = "\r"
	LF   = "\n"
	CRLF = CR + LF
)

// Default per-command delays of the server. NewNNTPServer seeds the per-server
// fields from these; tests set the fields to 0 to run without delays.
var (
	DefaultArticleCmdDelay = 25 * time.Millisecond // ARTICLE/HEAD/BODY/STAT
	DefaultGroupCmdDelay   = 25 * time.Millisecond // GROUP
	DefaultErrorDelay      = time.Second           // before an error reply (rateLimitOnError)
)

// DefaultShutdownTimeout is how long Stop waits for open client connections to end.
const DefaultShutdownTimeout = 10 * time.Second

// PlaintextAuthPolicy decides how AUTHINFO USER/PASS is handled on connections
// without TLS (RFC 4643 §2.2, §2.3.1).
type PlaintextAuthPolicy int

const (
	// PlaintextAuthAccept accepts AUTHINFO on plaintext connections but does not
	// advertise it there (TLS connections always advertise it). Default.
	PlaintextAuthAccept PlaintextAuthPolicy = iota
	// PlaintextAuthAdvertise accepts and advertises AUTHINFO on plaintext connections.
	PlaintextAuthAdvertise
	// PlaintextAuthRefuse answers AUTHINFO on plaintext connections with 483.
	PlaintextAuthRefuse
)

// ParsePlaintextAuthPolicy parses "accept", "advertise" or "refuse" (case-insensitive).
func ParsePlaintextAuthPolicy(s string) (PlaintextAuthPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "accept":
		return PlaintextAuthAccept, nil
	case "advertise":
		return PlaintextAuthAdvertise, nil
	case "refuse":
		return PlaintextAuthRefuse, nil
	}
	return PlaintextAuthAccept, fmt.Errorf("invalid plaintext auth policy %q (want accept, advertise or refuse)", s)
}

// NNTPServer represents the main NNTP server
type NNTPServer struct {
	Config      *config.ServerConfig
	DB          *database.Database
	Listener    net.Listener
	TLSListener net.Listener
	AuthManager *AuthManager
	Stats       *ServerStats
	Processor   ArticleProcessor // For handling incoming articles (POST/IHAVE/TAKETHIS)
	shutdown    chan struct{}
	wg          *sync.WaitGroup // Use external waitgroup for coordination
	mu          sync.RWMutex
	local430    *Local430     // Cache for 430 responses
	xref        xrefModeStore // Xref: header mode (SetXrefMode)
	running     bool
	bgOnce      sync.Once // starts the background goroutines once

	// Open client connections: Stop closes them and waits for their handlers.
	connsMu   sync.Mutex
	conns     map[net.Conn]struct{}
	userConns map[int]int // authenticated connections per NNTP user id (EnforceUserMaxConns)
	stopping  bool        // set by Stop under connsMu; trackConn refuses connections from then on
	handlers  sync.WaitGroup
	rejectSem chan struct{} // bounds the goroutines sending the MaxConns 400 greeting

	// Per-server timing. Zero delay means no sleep; IdleTimeout <= 0 falls back
	// to DefaultNNTPcliconnTimeout. Per-server (not package vars) so tests don't race.
	ArticleCmdDelay time.Duration
	GroupCmdDelay   time.Duration
	ErrorDelay      time.Duration
	IdleTimeout     time.Duration
	// Delay-site counters (test observability, plan test-timing-hardening P1): incremented every time a
	// command reaches an error-delay or article-delay site, whether or not the configured delay is > 0,
	// so tests can run with zero delays and still assert structurally that a reply did (not) pay a delay.
	errorDelays   atomic.Int64
	articleDelays atomic.Int64
	// ShutdownTimeout bounds the wait for client connections in Stop (<= 0: DefaultShutdownTimeout).
	ShutdownTimeout time.Duration

	// Access policy. Set these before Start/Serve; they must not change while serving.
	PlaintextAuth       PlaintextAuthPolicy // AUTHINFO on connections without TLS
	ReadRequiresAuth    bool                // reader commands need a successful AUTHINFO (480 otherwise)
	EnforceUserMaxConns bool                // enforce nntp_users.maxconns per user (<= 0 = unlimited)
}

// NewNNTPServer creates a new NNTP server instance. It starts no goroutines:
// Start or Serve open the listeners and start the background cleanup.
func NewNNTPServer(db *database.Database, cfg *config.ServerConfig, mainWG *sync.WaitGroup, processor ArticleProcessor) (*NNTPServer, error) {
	if db == nil {
		return nil, fmt.Errorf("database cannot be nil")
	}
	if cfg == nil {
		return nil, fmt.Errorf("server config cannot be nil")
	}
	if mainWG == nil {
		return nil, fmt.Errorf("main waitgroup cannot be nil")
	}
	// processor can be nil for read-only NNTP servers

	server := &NNTPServer{
		Config:      cfg,
		DB:          db,
		AuthManager: NewAuthManager(db),
		Stats:       NewServerStats(),
		Processor:   processor,
		shutdown:    make(chan struct{}),
		wg:          mainWG, // Use external waitgroup for coordination
		local430:    &Local430{cache: make(map[string]time.Time), ttl: DefaultLocal430TTL},
		conns:       make(map[net.Conn]struct{}),
		userConns:   make(map[int]int),
		rejectSem:   make(chan struct{}, 32),

		ArticleCmdDelay: DefaultArticleCmdDelay,
		GroupCmdDelay:   DefaultGroupCmdDelay,
		ErrorDelay:      DefaultErrorDelay,
		IdleTimeout:     DefaultNNTPcliconnTimeout,
		ShutdownTimeout: DefaultShutdownTimeout,
	}
	server.SetXrefMode(XrefPartial)
	server.SetXrefBatchedLookup(true)
	return server, nil
}

// listenAddr is the listen address for port on the IP addr ("" = all interfaces).
// net.JoinHostPort brackets IPv6 literals: "::1" gives "[::1]:port".
func listenAddr(addr string, port int) string {
	return net.JoinHostPort(addr, strconv.Itoa(port))
}

// Start validates the configuration, opens the configured listeners (on
// Config.NNTP.Addr, all interfaces when empty) and starts serving. When it
// returns an error nothing is left listening.
func (s *NNTPServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server is already running")
	}
	select {
	case <-s.shutdown:
		return fmt.Errorf("server has been stopped")
	default:
	}

	addr, port, tlsPort := s.Config.NNTP.Addr, s.Config.NNTP.Port, s.Config.NNTP.TLSPort
	if port <= 0 && tlsPort <= 0 {
		return fmt.Errorf("no listener configured (set -nntptcpport and/or -nntptlsport)")
	}

	// Load the certificate before any listener is opened.
	var tlsConfig *tls.Config
	if tlsPort > 0 {
		if s.Config.NNTP.TLSCert == "" || s.Config.NNTP.TLSKey == "" {
			return fmt.Errorf("TLS port %d needs a certificate and a key (set -nntpcertfile and -nntpkeyfile)", tlsPort)
		}
		cert, err := tls.LoadX509KeyPair(s.Config.NNTP.TLSCert, s.Config.NNTP.TLSKey)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificate: %w", err)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	var plainListener, tlsListener net.Listener
	if port > 0 {
		l, err := net.Listen("tcp", listenAddr(addr, port))
		if err != nil {
			return fmt.Errorf("failed to start NNTP listener on %s: %w", listenAddr(addr, port), err)
		}
		plainListener = l
	}
	if tlsConfig != nil {
		l, err := tls.Listen("tcp", listenAddr(addr, tlsPort), tlsConfig)
		if err != nil {
			if plainListener != nil {
				_ = plainListener.Close()
			}
			return fmt.Errorf("failed to start NNTP TLS listener on %s: %w", listenAddr(addr, tlsPort), err)
		}
		tlsListener = l
	}

	if plainListener != nil {
		if addr == "" {
			log.Printf("[NNTP] NNTP server listening on port %d", port)
		} else {
			log.Printf("[NNTP] NNTP server listening on %s", listenAddr(addr, port))
		}
		s.serveLocked(plainListener, false)
	}
	if tlsListener != nil {
		if addr == "" {
			log.Printf("[NNTP] NNTP TLS server listening on port %d", tlsPort)
		} else {
			log.Printf("[NNTP] NNTP TLS server listening on %s", listenAddr(addr, tlsPort))
		}
		s.serveLocked(tlsListener, true)
	}
	s.startBackgroundLocked()
	s.running = true
	log.Println("[NNTP] NNTP server started successfully")
	return nil
}

// Serve accepts connections on an existing listener (for example one bound to
// 127.0.0.1:0 by a test) and marks the server as running. The listener is stored
// in Listener, or in TLSListener when isTLS is set, so Stop() closes it.
func (s *NNTPServer) Serve(l net.Listener, isTLS bool) error {
	if l == nil {
		return fmt.Errorf("listener cannot be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.shutdown:
		return fmt.Errorf("server has been stopped")
	default:
	}
	s.serveLocked(l, isTLS)
	s.startBackgroundLocked()
	s.running = true
	return nil
}

// serveLocked stores the listener and starts its accept loop. Caller holds s.mu.
func (s *NNTPServer) serveLocked(l net.Listener, isTLS bool) {
	if isTLS {
		s.TLSListener = l
	} else {
		s.Listener = l
	}
	s.wg.Add(1)
	go s.serve(l, isTLS)
}

// startBackgroundLocked starts the background goroutines once (the local430
// cleanup); they end when Stop closes s.shutdown. Caller holds s.mu.
func (s *NNTPServer) startBackgroundLocked() {
	s.bgOnce.Do(func() {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.local430.run(s.shutdown)
		}()
	})
}

// serve handles incoming connections on the given listener
func (s *NNTPServer) serve(listener net.Listener, isTLS bool) {
	defer s.wg.Done()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				log.Printf("[NNTP] listener %s closed, accept loop ends", listener.Addr())
				return
			}
			log.Printf("[NNTP] Error accepting connection: %v", err)
			// back off briefly so a persistent error (e.g. EMFILE) does not spin
			select {
			case <-s.shutdown:
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}

		// The connection limit counts at accept time, so a burst cannot overshoot it.
		maxConns := s.Config.NNTP.MaxConns
		if maxConns <= 0 {
			maxConns = config.NNTPServerMaxConns
		}
		if s.Stats.GetActiveConnections() >= maxConns {
			log.Printf("[NNTP] Connection limit %d reached, rejecting connection from %s", maxConns, conn.RemoteAddr())
			// RFC 3977 §5.1.2: a 400 greeting, then close. It runs in its own
			// goroutine (at most cap(rejectSem) at once, each bounded by a 2 s
			// deadline that covers reads too: on a TLS listener the first Write
			// runs the handshake), so stalled clients can't hold the accept loop.
			// When all reject slots are busy the connection is closed silently.
			select {
			case s.rejectSem <- struct{}{}:
				s.wg.Add(1)
				go func(conn net.Conn) {
					defer s.wg.Done()
					defer func() { <-s.rejectSem }()
					_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
					_, _ = io.WriteString(conn, "400 Too many connections, try again later\r\n")
					_ = conn.Close()
				}(conn)
			default:
				_ = conn.Close()
			}
			continue
		}

		s.Stats.ConnectionStarted()
		if !s.trackConn(conn) {
			s.Stats.ConnectionEnded()
			continue
		}
		s.wg.Add(1)
		go s.handleConnection(conn, isTLS)
	}
}

// trackConn registers conn and counts it in s.handlers. Once Stop has begun it
// closes conn instead and returns false. handlers.Add runs under connsMu, so it
// always happens before the handlers.Wait in Stop.
func (s *NNTPServer) trackConn(conn net.Conn) bool {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if s.stopping {
		_ = conn.Close()
		return false
	}
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	s.conns[conn] = struct{}{}
	s.handlers.Add(1)
	return true
}

// untrackConn removes conn from the set of open connections.
func (s *NNTPServer) untrackConn(conn net.Conn) {
	s.connsMu.Lock()
	delete(s.conns, conn)
	s.connsMu.Unlock()
}

// acquireUserSlot takes a connection slot for an authenticated user. It always
// succeeds unless EnforceUserMaxConns is set; then a user with MaxConns > 0 gets
// at most MaxConns authenticated connections on this server (MaxConns <= 0: unlimited).
func (s *NNTPServer) acquireUserSlot(u *models.NNTPUser) bool {
	if !s.EnforceUserMaxConns || u == nil {
		return true
	}
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if s.userConns == nil {
		s.userConns = make(map[int]int)
	}
	if u.MaxConns > 0 && s.userConns[u.ID] >= u.MaxConns {
		return false
	}
	s.userConns[u.ID]++
	return true
}

// releaseUserSlot gives back a slot taken by acquireUserSlot.
func (s *NNTPServer) releaseUserSlot(u *models.NNTPUser) {
	if !s.EnforceUserMaxConns || u == nil {
		return
	}
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if n := s.userConns[u.ID]; n > 1 {
		s.userConns[u.ID] = n - 1
	} else {
		delete(s.userConns, u.ID)
	}
}

// handleConnection processes a single client connection. The accept loop has
// already counted it (Stats.ConnectionStarted, trackConn).
func (s *NNTPServer) handleConnection(conn net.Conn, isTLS bool) {
	defer s.wg.Done()
	defer s.handlers.Done()
	defer s.untrackConn(conn)
	defer s.Stats.ConnectionEnded()
	defer conn.Close()

	client := NewClientConnection(conn, s, isTLS)
	defer func() {
		// c.user is only set after acquireUserSlot succeeded
		if client.user != nil {
			s.releaseUserSlot(client.user)
		}
	}()
	// Registered last so it runs first: a panic in one session must not take the
	// process down, and the cleanups above still run.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[NNTP] panic in connection %s: %v\n%s", conn.RemoteAddr(), r, debug.Stack())
		}
	}()

	client.UpdateDeadlines()
	if err := client.Handle(); err != nil {
		log.Printf("Connection error from %s: %v", conn.RemoteAddr(), err)
	}
}

// Stop shuts the server down: it closes the listeners and every open client
// connection, then waits up to ShutdownTimeout for the connection handlers.
// The caller's WaitGroup (mainWG of NewNNTPServer) covers all server goroutines.
func (s *NNTPServer) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	log.Println("[NNTP] Shutting down NNTP server...")
	s.running = false
	close(s.shutdown)
	for _, l := range []net.Listener{s.Listener, s.TLSListener} {
		if l == nil {
			continue
		}
		if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("[NNTP] closing listener %s: %v", l.Addr(), err)
		}
	}
	timeout := s.ShutdownTimeout
	s.mu.Unlock()
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}

	// From here on trackConn refuses new connections, so the copied set is complete.
	s.connsMu.Lock()
	s.stopping = true
	open := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		open = append(open, c)
	}
	s.connsMu.Unlock()
	for _, c := range open {
		// Close the raw socket of a TLS conn: tls.Conn.Close would first send
		// close_notify under its own 5 s write deadline, per stalled client.
		if tc, ok := c.(*tls.Conn); ok {
			_ = tc.NetConn().Close()
		} else {
			_ = c.Close()
		}
	}

	done := make(chan struct{})
	go func() {
		s.handlers.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Println("[NNTP] NNTP server shut down gracefully")
	case <-time.After(timeout):
		s.connsMu.Lock()
		left := len(s.conns)
		s.connsMu.Unlock()
		log.Printf("[NNTP] %d connection(s) did not end within %s", left, timeout)
	}
	return nil
}

// IsRunning returns whether the server is currently running
func (s *NNTPServer) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// ErrorDelayCount returns how many replies reached the error-delay site (rateLimitOnError) so far.
func (s *NNTPServer) ErrorDelayCount() int64 { return s.errorDelays.Load() }

// ArticleDelayCount returns how many commands reached an ArticleCmdDelay site so far.
func (s *NNTPServer) ArticleDelayCount() int64 { return s.articleDelays.Load() }
