package nntp

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// Package nntp provides connection pool management for go-pugleaf.

// Pool manages a pool of NNTP client connections
type Pool struct {
	mux         sync.RWMutex
	Backend     *BackendConfig
	connections chan *BackendConn
	maxConns    int
	activeConns int
	idleTimeout time.Duration
	closed      bool

	// Statistics
	totalCreated int64
	totalClosed  int64
}

var ErrNewsgroupNotFound error

// NewPool creates a new connection pool
func NewPool(cfg *BackendConfig) *Pool {
	pool := &Pool{
		Backend:     cfg,
		connections: make(chan *BackendConn, cfg.MaxConns),
		maxConns:    cfg.MaxConns,
		idleTimeout: DefaultConnExpire,
	}
	return pool
}

func (p *Pool) XOver(group string, start, end int64, enforceLimit bool) ([]OverviewLine, error) {
	p.mux.RLock()
	if p.closed {
		p.mux.RUnlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	p.mux.RUnlock()

	// Get a connection from the pool
	client, err := p.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	// Perform the XOVER command
	result, err := client.XOver(group, start, end, enforceLimit)
	if err != nil {
		// Close connection on error
		p.CloseConn(client, true)
		return nil, err
	}

	// Put back connection only if no error
	p.Put(client)
	return result, nil
}

func (p *Pool) XHdr(group string, header string, start, end int64) ([]HeaderLine, error) {
	// Get a connection from the pool
	client, err := p.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	result, err := client.XHdr(group, header, start, end)
	if err != nil {
		// Close connection on error
		p.CloseConn(client, true)
		return nil, err
	}

	// Put back connection only if no error
	p.Put(client)
	return result, nil
}

// XHdrStreamed performs XHDR command and streams results through a channel
// The channel will be closed when all results are sent or an error occurs
// NOTE: This function takes ownership of the connection and will return it to the pool when done
func (p *Pool) XHdrStreamed(group string, header string, start, end int64, resultChan chan<- *HeaderLine) error {
	// Get a connection from the pool
	client, err := p.Get()
	if err != nil {
		close(resultChan)
		return fmt.Errorf("failed to get connection: %w", err)
	}

	// Handle connection cleanup in a goroutine so the function can return immediately
	go func(client *BackendConn, group string, header string, start, end int64, resultChan chan<- *HeaderLine) {
		// Use the streaming XHdr function on the client
		if err := client.XHdrStreamed(group, header, start, end, resultChan); err != nil {
			// If there's an error, close the connection instead of returning it
			err := p.CloseConn(client, true)
			if err != nil {
				log.Printf("[NNTP-POOL] Failed to close connection after XHdrStreamed error: %v", err)
			}
		} else {
			p.Put(client)
		}
	}(client, group, header, start, end, resultChan)

	return err
}

func (p *Pool) GetArticle(messageID *string) (*models.Article, error) {
	p.mux.RLock()
	if p.closed {
		p.mux.RUnlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	p.mux.RUnlock()

	// Get a connection from the pool
	client, err := p.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	result, err := client.GetArticle(messageID)
	if err != nil {
		if err != ErrArticleNotFound && err != ErrArticleRemoved {
			p.CloseConn(client, true) // Close the connection on error
			log.Printf("[NNTP-POOL] Failed to get article %s: %v", *messageID, err)
		}
		return nil, fmt.Errorf("failed to get article: %w", err)
	}

	// Only put back if no error occurred
	p.Put(client)
	return result, nil
}

func (p *Pool) SelectGroup(group string) (*GroupInfo, error) {
	p.mux.RLock()
	if p.closed {
		p.mux.RUnlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	p.mux.RUnlock()

	// Get a connection from the pool
	client, err := p.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	gi, code, err := client.SelectGroup(group)
	if err != nil && code != 411 {
		// Close connection on unexpected errors (not "group not found")
		p.CloseConn(client, true)
		return nil, err
	}

	// Put back connection (even for code 411 - group not found)
	p.Put(client)

	if code == 411 {
		err = ErrNewsgroupNotFound // silence error
	}
	return gi, err
}

// Get retrieves a connection from the pool or creates a new one
func (p *Pool) Get() (*BackendConn, error) {
	p.mux.Lock()
	if p.closed {
		p.mux.Unlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	p.mux.Unlock()

	// Try to get an existing connection
	select {
	case pconn := <-p.connections:
		// Check if connection is still valid
		if p.isConnectionValid(pconn) {
			pconn.UpdateLastUsed()
			return pconn, nil
		}
		// Connection expired, close it and create a new one
		if err := p.CloseConn(pconn, true); err != nil {
			log.Printf("Failed to close expired connection: %v", err)
		}

	default:
		// No connections available
	}

	// Create new connection if under limit
	p.mux.Lock()
	if p.activeConns < p.maxConns {
		p.activeConns++
		p.mux.Unlock()
		pconn, err := p.createConnection()
		if err != nil {
			p.mux.Lock()
			p.activeConns--
			p.mux.Unlock()
			return nil, err
		}
		pconn.UpdateLastUsed() // Mark as used since we're handing it out
		p.mux.Lock()
		p.totalCreated++
		p.mux.Unlock()
		return pconn, nil
	}
	p.mux.Unlock()

	// Wait for a connection to become available
	select {
	case pconn := <-p.connections:
		if p.isConnectionValid(pconn) {
			pconn.UpdateLastUsed()
			return pconn, nil
		}
		// Connection expired, close and create new one
		p.CloseConn(pconn, true)
		// Create replacement connection
		newPconn, err := p.createConnection()
		if err != nil {
			return nil, err
		}
		newPconn.UpdateLastUsed() // Mark as used since we're handing it out
		p.mux.Lock()
		p.activeConns++
		p.totalCreated++
		p.mux.Unlock()
		return newPconn, nil
	case <-time.After(30 * time.Second):
		// Timeout waiting for a connection
		return nil, fmt.Errorf("timeout waiting for connection from pool after 30s")
	}
}

// Put returns a connection to the pool
func (p *Pool) Put(client *BackendConn) error {
	p.mux.Lock()
	if p.closed || client == nil {
		p.mux.Unlock()
		if client != nil {
			client.CloseFromPoolOnly()
		} else {
			log.Printf("[NNTP-POOL] ERROR: Attempted to put nil client back into pool")
			p.mux.Unlock()
		}
		p.mux.Lock()
		p.totalClosed++
		p.activeConns--
		p.mux.Unlock()
		return nil
	}
	p.mux.Unlock()

	client.UpdateLastUsed()
	// Try to return connection to pool
	select {
	case p.connections <- client:
		return nil
	default:
		log.Printf("[NNTP-POOL ERROR: Pool is full ?! should be fatal! closing connection for %s:%d", p.Backend.Host, p.Backend.Port)
		// Pool is full, close the connection
		client.CloseFromPoolOnly()
		p.mux.Lock()
		p.totalClosed++
		p.activeConns--
		p.mux.Unlock()
		return nil
	}
}

// Closes a specific connection
func (p *Pool) CloseConn(client *BackendConn, lock bool) error {

	if client == nil {
		return nil
	}

	err := client.CloseFromPoolOnly()
	if err != nil {
		return fmt.Errorf("failed to close connection: %w", err)
	}
	// Remove from active connections
	if lock {
		p.mux.Lock()
		p.totalClosed++
		p.activeConns--
		p.mux.Unlock()
	}
	return nil
}

// Close closes all connections in the pool
func (p *Pool) ClosePool() error {
	p.mux.Lock()

	if p.closed {
		p.mux.Unlock()
		log.Printf("[NNTP-POOL] Pool is already closed")
		return nil
	}
	p.closed = true
	p.mux.Unlock()
	// Close all connections in the pool
	close(p.connections)
	for client := range p.connections { // drain channel
		client.CloseFromPoolOnly()
		p.mux.Lock()
		p.totalClosed++
		p.mux.Unlock()
	}
	if p.activeConns > 0 {
		log.Printf("[NNTP-POOL] WARNING: Pool closed with positive count %d active connections remaining ?!?!", p.activeConns)
	}
	p.activeConns = 0
	return nil
}

// Stats returns pool statistics
func (p *Pool) Stats() PoolStats {
	p.mux.RLock()
	defer p.mux.RUnlock()

	return PoolStats{
		MaxConnections:    p.maxConns,
		ActiveConnections: p.activeConns,
		IdleConnections:   len(p.connections),
		TotalCreated:      p.totalCreated,
		TotalClosed:       p.totalClosed,
		Closed:            p.closed,
	}
}

// PoolStats contains pool statistics
type PoolStats struct {
	MaxConnections    int
	ActiveConnections int
	IdleConnections   int
	TotalCreated      int64
	TotalClosed       int64
	Closed            bool
}

// createConnection creates a new NNTP client connection
func (p *Pool) createConnection() (*BackendConn, error) {
	//log.Printf("[NNTP-POOL] Creating new connection to %s:%d", p.Backend.Host, p.Backend.Port)
	client := NewConn(p.Backend)
	client.Pool = p // Set the pool reference BEFORE calling Connect()

	if err := client.Connect(); err != nil {
		log.Printf("[NNTP-POOL] Failed to create connection to %s:%d: %v", p.Backend.Host, p.Backend.Port, err)
		return nil, fmt.Errorf("failed to create connection: %w", err)
	}
	//log.Printf("[NNTP-POOL] Successfully created connection to %s:%d", p.Backend.Host, p.Backend.Port)
	return client, nil
}

// isConnectionValid checks if a connection is still valid and not expired
func (p *Pool) isConnectionValid(client *BackendConn) bool {
	if client == nil || !client.connected {
		return false
	}

	// Acquire read lock to safely access client fields
	client.mu.RLock()
	lastUsed := client.lastUsed
	client.mu.RUnlock()

	// Check if connection has been idle too long
	if time.Since(lastUsed) > p.idleTimeout {
		return false
	}

	return true
}

// Cleanup periodically cleans up expired connections
func (p *Pool) Cleanup() {
	p.mux.Lock()

	if p.closed {
		p.mux.Unlock()
		return
	}
	p.mux.Unlock()
	// Check connections in the pool for expiration
	var validConnections []*BackendConn

	// Drain the channel and check each connection
	for {
		select {
		case client := <-p.connections:
			if p.isConnectionValid(client) {
				validConnections = append(validConnections, client)
			} else {
				client.CloseFromPoolOnly()
				p.mux.Lock()
				p.totalClosed++
				p.activeConns--
				p.mux.Unlock()
			}
		default:
			// Channel is empty
			goto done
		}
	}

done:
	// Put valid connections back
	for _, client := range validConnections {
		select {
		case p.connections <- client:
			// Successfully returned to pool
		default:
			log.Printf("[NNTP-POOL] ERROR: Pool is full while returning connection for %s:%d", p.Backend.Host, p.Backend.Port)
			// Pool is full, close the connection
			client.CloseFromPoolOnly()
			p.mux.Lock()
			p.totalClosed++
			p.activeConns--
			p.mux.Unlock()
		}
	}
}

// StartCleanupWorker starts a goroutine that periodically cleans up expired connections
func (p *Pool) StartCleanupWorker(interval time.Duration) {
	if interval <= 0 {
		interval = 8 * time.Second // Default cleanup interval
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			p.Cleanup()

			// Check if pool is closed
			p.mux.RLock()
			closed := p.closed
			p.mux.RUnlock()

			if closed {
				return
			}
		}
	}()
}
