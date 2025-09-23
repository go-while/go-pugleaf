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
	Backend     *BackendConfig // links to internal/nntp/nntp-client.go:68
	connections chan *BackendConn
	maxConns    int
	activeConns int
	idleTimeout time.Duration
	closed      bool

	// Statistics
	totalCreated int64
	totalClosed  int64
}

var ErrNewsgroupNotFound = fmt.Errorf("newsgroup not found")
var ErrArticleNotFound = fmt.Errorf("article not found")
var ErrArticleRemoved = fmt.Errorf("article removed (DMCA)")

// NewPool creates a new connection pool
func NewPool(cfg *BackendConfig) *Pool {
	pool := &Pool{
		Backend:     cfg,
		connections: make(chan *BackendConn, cfg.MaxConns),
		maxConns:    cfg.MaxConns,
		idleTimeout: DefaultConnExpire,
	}
	go pool.startCleanupWorker()
	return pool
}

func (pool *Pool) XOver(group string, start, end int64, enforceLimit bool) ([]OverviewLine, error) {
	pool.mux.RLock()
	if pool.closed {
		pool.mux.RUnlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	pool.mux.RUnlock()

	// Get a connection from the pool
	client, err := pool.Get(MODE_READER_MV)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	// Perform the XOVER command
	result, err := client.XOver(group, start, end, enforceLimit)
	if err != nil {
		// Close connection on error
		go pool.CloseConn(client, true)
		return nil, err
	}

	// Put back connection only if no error
	pool.Put(client)
	return result, nil
}

func (pool *Pool) XHdr(group string, header string, start, end int64) ([]*HeaderLine, error) {
	// Get a connection from the pool
	client, err := pool.Get(MODE_READER_MV)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	result, err := client.XHdr(group, header, start, end)
	if err != nil {
		// Close connection on error
		go pool.CloseConn(client, true)
		return nil, err
	}

	// Put back connection only if no error
	pool.Put(client)
	return result, nil
}

// XHdrStreamed performs XHDR command and streams results through a channel
// The channel will be closed when all results are sent or an error occurs
// NOTE: This function takes ownership of the connection and will return it to the pool when done
func (pool *Pool) XHdrStreamed(group string, header string, start, end int64, xhdrChan chan<- *HeaderLine, shutdownChan <-chan struct{}) error {
	// Get a connection from the pool
	client, err := pool.Get(MODE_READER_MV)
	if err != nil {
		close(xhdrChan)
		return fmt.Errorf("failed to get connection: %w", err)
	}

	// Handle connection cleanup in a goroutine so the function can return immediately
	go func(client *BackendConn, group string, header string, start, end int64, resultChan chan<- *HeaderLine, shutdownChan <-chan struct{}) {
		// Use the streaming XHdr function on the client
		if err := client.XHdrStreamed(group, header, start, end, resultChan, shutdownChan); err != nil {
			// If there's an error, close the connection instead of returning it
			err := pool.CloseConn(client, true)
			if err != nil {
				log.Printf("[NNTP-POOL] Failed to close connection after XHdrStreamed error: %v", err)
			}
		} else {
			pool.Put(client)
		}
	}(client, group, header, start, end, xhdrChan, shutdownChan)

	return err
}

func (pool *Pool) GetArticle(messageID *string, bulkmode bool) (*models.Article, error) {
	pool.mux.RLock()
	if pool.closed {
		pool.mux.RUnlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	pool.mux.RUnlock()

	// Get a connection from the pool
	client, err := pool.Get(MODE_READER_MV)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	article, err := client.GetArticle(messageID, bulkmode)
	if err != nil || article == nil {
		if err == ErrArticleNotFound || err == ErrArticleRemoved {
			log.Printf("[NNTP-POOL] Article '%s' not found err='%v'", *messageID, err)
			pool.Put(client)
			return nil, err
		} else {
			go pool.CloseConn(client, true) // Close the connection on error
			log.Printf("[NNTP-POOL] Failed to get article %s: %v", *messageID, err)
		}
		return nil, fmt.Errorf("nntp-pool: GetArticle failed to get article: %w", err)
	}

	// Only put back if no error occurred
	pool.Put(client)
	return article, nil
}

func (pool *Pool) SelectGroup(group string) (*GroupInfo, error) {
	pool.mux.RLock()
	if pool.closed {
		pool.mux.RUnlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	pool.mux.RUnlock()

	// Get a connection from the pool
	client, err := pool.Get(MODE_READER_MV)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	gi, code, err := client.SelectGroup(group)
	if err != nil && code != 411 {
		// Close connection on unexpected errors (not "group not found")
		go pool.CloseConn(client, true)
		return nil, err
	}

	// Put back connection (even for code 411 - group not found)
	pool.Put(client)

	if code == 411 {
		err = ErrNewsgroupNotFound // silence error
	}
	return gi, err
}

const MODE_READER_MV int = 1
const MODE_STREAM_MV int = 2

// Get retrieves a connection from the pool or creates a new one
func (pool *Pool) Get(mode int) (*BackendConn, error) {
	pool.mux.Lock()
	if pool.closed {
		pool.mux.Unlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	pool.mux.Unlock()

	// Try to get an existing idle connection without waiting
	select {
	case pconn := <-pool.connections:
		pconn.mu.Lock()
		if pconn.ModeStream && mode == MODE_READER_MV {
			if err := pconn.SwitchMode(MODE_READER_MV); err != nil {
				pconn.mu.Unlock()
				pool.CloseConn(pconn, true)
				log.Printf("[NNTP-POOL] Failed to switch mode: provider='%s': %v", pool.Backend.Provider.Name, err)
				time.Sleep(time.Second)
				goto newConn
			} else {
				pconn.mu.Unlock()
			}
		} else if pconn.ModeReader && mode == MODE_STREAM_MV {
			// INN does not allow switching to mode stream when mode reader is active
			// we must close this connection and create a new one
			log.Printf("[NNTP-POOL] want streaming but got connection in reader mode, closing and getting a new one")
			pconn.mu.Unlock()
			pool.CloseConn(pconn, true)
			break // exit select to create a new connection
		} else {
			pconn.mu.Unlock()
		}
		// Check if connection is still valid
		if pool.isConnectionValid(pconn) {
			pconn.UpdateLastUsed()
			return pconn, nil
		}
		// Connection expired, close it and create a new one
		pool.CloseConn(pconn, true)

	default:
		// No idle connections available
	}

newConn:
	// Create new connection if under limit
	pool.mux.Lock()
	if pool.activeConns < pool.maxConns {
		pool.activeConns++
		pool.mux.Unlock()
		pconn, err := pool.createConnection()
		if err != nil {
			pool.mux.Lock()
			pool.activeConns--
			pool.mux.Unlock()
			return nil, err
		}
		err = pconn.SwitchMode(mode)
		if err != nil {
			pool.mux.Lock()
			pool.activeConns--
			pool.mux.Unlock()
			if pconn.conn != nil {
				pconn.conn.Close()
			}
			log.Printf("[NNTP-POOL] Failed to switch mode: provider='%s': %v", pool.Backend.Provider.Name, err)
			return nil, err
		}
		pconn.UpdateLastUsed() // Mark as used since we're handing it out
		pool.mux.Lock()
		pool.totalCreated++
		pool.mux.Unlock()
		return pconn, nil
	}
	pool.mux.Unlock()

	// Wait for a connection to become available
	select {
	case pconn := <-pool.connections:
		pconn.mu.Lock()
		if pconn.ModeStream && mode == MODE_READER_MV {
			if err := pconn.SwitchMode(MODE_READER_MV); err != nil {
				pconn.mu.Unlock()
				pool.CloseConn(pconn, true)
				time.Sleep(time.Second)
				log.Printf("[NNTP-POOL] Failed to switch mode: provider='%s': %v", pool.Backend.Provider.Name, err)
				goto newConn
			} else {
				pconn.mu.Unlock()
			}
		} else if pconn.ModeReader && mode == MODE_STREAM_MV {
			// INN does not allow switching to mode stream when mode reader is active
			// we must close this connection and create a new one
			log.Printf("[NNTP-POOL] want streaming but got connection in reader mode, closing and getting a new one")
			pconn.mu.Unlock()
			pool.CloseConn(pconn, true)
			goto newConn
		} else {
			pconn.mu.Unlock()
		}
		if pool.isConnectionValid(pconn) {
			pconn.UpdateLastUsed()
			return pconn, nil
		}
		// Connection expired, close and create new one
		pool.CloseConn(pconn, true)
		goto newConn
	case <-time.After(30 * time.Second):
		// Timeout waiting for a connection
		return nil, fmt.Errorf("timeout waiting for connection from pool after 30s")
	}
}

// Put returns a connection to the pool
func (pool *Pool) Put(client *BackendConn) error {
	var forceClose bool
	if client != nil {
		client.mu.RLock()
		if client.ForceClose {
			forceClose = client.ForceClose
		}
		client.mu.RUnlock()
	}

	pool.mux.Lock()
	if pool.closed || forceClose {
		pool.mux.Unlock()
		if client != nil {
			client.CloseFromPoolOnly()
		} else {
			log.Printf("[NNTP-POOL] ERROR: Attempted to put nil client back into pool")
		}
		pool.mux.Lock()
		pool.totalClosed++
		pool.activeConns--
		pool.mux.Unlock()
		return nil
	}
	pool.mux.Unlock()

	client.UpdateLastUsed()
	// Try to return connection to pool
	select {
	case pool.connections <- client:
		return nil
	default:
		log.Printf("[NNTP-POOL] ERROR: Pool is full or closed. Closing conn for %s:%d", pool.Backend.Host, pool.Backend.Port)
		client.CloseFromPoolOnly()
		pool.mux.Lock()
		pool.totalClosed++
		pool.activeConns--
		pool.mux.Unlock()
		return nil
	}
}

// Closes a specific connection
func (pool *Pool) CloseConn(client *BackendConn, lock bool) error {

	if client == nil {
		return nil
	}

	err := client.CloseFromPoolOnly()
	if err != nil {
		return fmt.Errorf("failed to close connection: %w", err)
	}
	// Remove from active connections
	if lock {
		pool.mux.Lock()
		pool.totalClosed++
		pool.activeConns--
		pool.mux.Unlock()
	}
	return nil
}

// Close closes all connections in the pool
func (pool *Pool) ClosePool() error {
	pool.mux.Lock()

	if pool.closed {
		pool.mux.Unlock()
		log.Printf("[NNTP-POOL] Pool is already closed")
		return nil
	}
	pool.closed = true
	pool.mux.Unlock()
	// Close all connections in the pool
	close(pool.connections)
	for client := range pool.connections { // drain channel
		client.CloseFromPoolOnly()
		pool.mux.Lock()
		pool.totalClosed++
		pool.mux.Unlock()
	}
	pool.mux.Lock()
	if pool.activeConns > 0 {
		log.Printf("[NNTP-POOL] WARNING: Pool closed with positive count %d active connections remaining ?!?!", pool.activeConns)
	}
	pool.activeConns = 0
	pool.mux.Unlock()
	return nil
}

// Stats returns pool statistics
func (pool *Pool) Stats() PoolStats {
	pool.mux.RLock()
	defer pool.mux.RUnlock()

	return PoolStats{
		MaxConnections:    pool.maxConns,
		ActiveConnections: pool.activeConns,
		IdleConnections:   len(pool.connections),
		TotalCreated:      pool.totalCreated,
		TotalClosed:       pool.totalClosed,
		Closed:            pool.closed,
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
func (pool *Pool) createConnection() (*BackendConn, error) {
	//log.Printf("[NNTP-POOL] Creating new connection to %s:%d", pool.Backend.Host, pool.Backend.Port)
	client := NewConn(pool.Backend)
	client.Pool = pool // Set the pool reference BEFORE calling Connect()

	if err := client.Connect(); err != nil {
		log.Printf("[NNTP-POOL] Failed to create connection to %s:%d: %v", pool.Backend.Host, pool.Backend.Port, err)
		return nil, fmt.Errorf("failed to create connection: %w", err)
	}
	//log.Printf("[NNTP-POOL] Successfully created connection to %s:%d", pool.Backend.Host, pool.Backend.Port)
	return client, nil
}

// isConnectionValid checks if a connection is still valid and not expired
func (pool *Pool) isConnectionValid(client *BackendConn) bool {
	if client == nil || !client.connected {
		return false
	}

	// Acquire read lock to safely access client fields
	client.mu.RLock()
	lastUsed := client.lastUsed
	client.mu.RUnlock()

	// Check if connection has been idle too long
	if time.Since(lastUsed) > pool.idleTimeout {
		return false
	}

	return true
}

// Cleanup periodically cleans up expired connections
func (pool *Pool) Cleanup() {
	pool.mux.Lock()

	if pool.closed {
		pool.mux.Unlock()
		return
	}
	pool.mux.Unlock()
	// Check connections in the pool for expiration
	var validConnections []*BackendConn

	// Drain the channel and check each connection
	for {
		select {
		case client := <-pool.connections:
			if pool.isConnectionValid(client) {
				validConnections = append(validConnections, client)
			} else {
				client.CloseFromPoolOnly()
				pool.mux.Lock()
				pool.totalClosed++
				pool.activeConns--
				pool.mux.Unlock()
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
		case pool.connections <- client:
			// Successfully returned to pool
		default:
			log.Printf("[NNTP-POOL] CLEANUP ERROR: Pool is full while returning connection for %s:%d", pool.Backend.Host, pool.Backend.Port)
			// Pool is full, close the connection
			client.CloseFromPoolOnly()
			pool.mux.Lock()
			pool.totalClosed++
			pool.activeConns--
			pool.mux.Unlock()
		}
	}
}

// startCleanupWorker starts a goroutine that periodically cleans up expired connections
func (pool *Pool) startCleanupWorker() {
	for {
		time.Sleep(5 * time.Second)
		pool.Cleanup()

		// Check if pool is closed
		pool.mux.RLock()
		closed := pool.closed
		pool.mux.RUnlock()

		if closed {
			return
		}
	}
}
