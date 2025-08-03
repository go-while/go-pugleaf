package nntp

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// Forward declaration to avoid circular imports
// Processor interface for article processing
type ArticleProcessor interface {
	ProcessIncomingArticle(article *models.Article) (int, error)
	Lookup(msgIdItem *history.MessageIdItem) (int, error)
}

const (
	// NNTP protocol constants
	DOT  = "."
	CR   = "\r"
	LF   = "\n"
	CRLF = CR + LF
)

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
	local430    *Local430 // Cache for 430 responses
	running     bool
}

// NewNNTPServer creates a new NNTP server instance
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
		local430:    &Local430{cache: make(map[*history.MessageIdItem]time.Time, 65535)},
	}
	go server.local430.CronLocal430() // Start local 430 cache cleanup goroutine
	return server, nil
}

// Start starts the NNTP server on the configured ports
func (s *NNTPServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server is already running")
	}

	// Start regular NNTP listener
	if s.Config.NNTP.Port > 0 {
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.Config.NNTP.Port))
		if err != nil {
			return fmt.Errorf("failed to start NNTP listener on port %d: %w", s.Config.NNTP.Port, err)
		}
		s.Listener = listener
		log.Printf("NNTP server listening on port %d", s.Config.NNTP.Port)

		s.wg.Add(1)
		go s.serve(s.Listener, false)
	}

	// Start TLS NNTP listener if configured
	if s.Config.NNTP.TLSPort > 0 && s.Config.NNTP.TLSCert != "" && s.Config.NNTP.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(s.Config.NNTP.TLSCert, s.Config.NNTP.TLSKey)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificate: %w", err)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
		}

		listener, err := tls.Listen("tcp", fmt.Sprintf(":%d", s.Config.NNTP.TLSPort), tlsConfig)
		if err != nil {
			return fmt.Errorf("failed to start NNTP TLS listener on port %d: %w", s.Config.NNTP.TLSPort, err)
		}
		s.TLSListener = listener
		log.Printf("NNTP TLS server listening on port %d", s.Config.NNTP.TLSPort)

		s.wg.Add(1)
		go s.serve(s.TLSListener, true)
	}

	s.running = true
	log.Println("NNTP server started successfully")
	return nil
}

// serve handles incoming connections on the given listener
func (s *NNTPServer) serve(listener net.Listener, isTLS bool) {
	defer s.wg.Done()

	for {
		select {
		case <-s.shutdown:
			return
		default:
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-s.shutdown:
					return
				default:
					log.Printf("Error accepting connection: %v", err)
					continue
				}
			}

			// Check connection limits
			if s.Stats.GetActiveConnections() >= s.Config.NNTP.MaxConns {
				log.Printf("Connection limit reached, rejecting connection from %s", conn.RemoteAddr())
				conn.Close()
				continue
			}

			// Handle the connection
			s.wg.Add(1)
			go s.handleConnection(conn, isTLS)
		}
	}
}

// handleConnection processes a single client connection
func (s *NNTPServer) handleConnection(conn net.Conn, isTLS bool) {
	defer s.wg.Done()
	defer conn.Close()

	s.Stats.ConnectionStarted()
	defer s.Stats.ConnectionEnded()

	client := NewClientConnection(conn, s, isTLS)
	client.UpdateDeadlines()
	if err := client.Handle(); err != nil {
		log.Printf("Connection error from %s: %v", conn.RemoteAddr(), err)
	}
}

// Stop gracefully shuts down the NNTP server
func (s *NNTPServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	log.Println("Shutting down NNTP server...")

	// Signal shutdown
	close(s.shutdown)

	// Close listeners
	if s.Listener != nil {
		s.Listener.Close()
	}
	if s.TLSListener != nil {
		s.TLSListener.Close()
	}

	// Wait for all connections to finish (with timeout)
	done := make(chan struct{})
	go func() {
		// Note: We don't call s.wg.Wait() here because s.wg is the main waitgroup
		// The main application will handle waiting for all NNTP server goroutines
		close(done)
	}()

	select {
	case <-done:
		log.Println("NNTP server shut down gracefully")
	case <-time.After(30 * time.Second):
		log.Println("NNTP server shutdown timeout, forcing exit")
	}

	s.running = false
	return nil
}

// IsRunning returns whether the server is currently running
func (s *NNTPServer) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}
