package nntp

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	failedConns uint64
	idleTimeout time.Duration
	closed      bool
	chanClosed  bool

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
		client.ForceCloseConn()
		return nil, err
	}

	// Put back connection only if no error
	pool.Put(client)
	return result, nil
}

func (pool *Pool) XHdr(group string, header string, start, end int64) ([]HeaderLine, error) {
	// Get a connection from the pool
	client, err := pool.Get(MODE_READER_MV)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	result, err := client.XHdr(group, header, start, end)
	if err != nil {
		// Close connection on error
		client.ForceCloseConn()
		return nil, err
	}

	// Put back connection only if no error
	pool.Put(client)
	return result, nil
}

// ListNewsgroups lists available newsgroups from the NNTP server
func (pool *Pool) ListNewsgroups() ([]GroupInfo, error) {
	// Get a connection from the pool
	client, err := pool.Get(MODE_READER_MV)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	remoteGroups, err := client.ListGroups()
	if err != nil {
		// Close connection on error
		client.ForceCloseConn()
		return nil, err
	}

	// Put back connection only if no error
	pool.Put(client)
	return remoteGroups, nil
}

// XHdrStreamed performs XHDR command and streams results through a channel
// The channel will be closed when all results are sent or an error occurs
// NOTE: This function takes ownership of the connection and will return it to the pool when done
func (pool *Pool) XHdrStreamed(group string, header string, start, end int64, xhdrChan chan<- HeaderLine, shutdownChan <-chan struct{}) error {
	// Get a connection from the pool
	client, err := pool.Get(MODE_READER_MV)
	if err != nil {
		close(xhdrChan)
		return fmt.Errorf("failed to get connection: %w", err)
	}

	// Handle connection cleanup in a goroutine so the function can return immediately
	go func(client *BackendConn, group string, header string, start, end int64, resultChan chan<- HeaderLine, shutdownChan <-chan struct{}) {
		// Use the streaming XHdr function on the client
		if err := client.XHdrStreamed(group, header, start, end, resultChan, shutdownChan); err != nil {
			// If there's an error, close the connection instead of returning it
			client.ForceCloseConn()
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
			client.ForceCloseConn()
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
		return nil, err
	}

	gi, code, err := client.SelectGroup(group)
	if err != nil && code != 411 {
		// Close connection on unexpected any other error than "group not found"
		client.ForceCloseConn()
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
func (pool *Pool) Get(wantMode int) (*BackendConn, error) {
	pool.mux.RLock()
	if pool.closed {
		pool.mux.RUnlock()
		pool.mux.Lock()
		if pool.closed && pool.activeConns == 0 && !pool.chanClosed {
			close(pool.connections)
			pool.chanClosed = true
		}
		pool.mux.Unlock()
		return nil, fmt.Errorf("connection pool is closed")
	}
	pool.mux.RUnlock()

	// Try to get an existing idle connection without waiting
	select {
	case pconn, ok := <-pool.connections:
		if !ok {
			return nil, fmt.Errorf("connection pool is closed")
		}
		pconn.mux.Lock()
		if pconn.ModeStream && wantMode == MODE_READER_MV {
			if err := pconn.SwitchMode(MODE_READER_MV); err != nil {
				pconn.mux.Unlock()
				pool.closeConn(pconn, true)
				log.Printf("[NNTP-POOL] Failed to switch mode: provider='%s': %v", pool.Backend.Provider.Name, err)
				time.Sleep(time.Second)
				goto newConn
			} else {
				pconn.mux.Unlock()
			}
		} else if pconn.ModeReader && wantMode == MODE_STREAM_MV {
			// INN does not allow switching to mode stream when mode reader is active
			// we must close this connection and create a new one
			log.Printf("[NNTP-POOL] want streaming but got connection in reader mode, closing and getting a new one")
			pconn.mux.Unlock()
			pool.closeConn(pconn, true)
			break // exit select to create a new connection
		} else {
			pconn.mux.Unlock()
		}
		// Check if connection is still valid
		if pool.isConnectionValid(pconn) {
			return pconn, nil
		}
		// Connection expired, close it and create a new one
		pool.closeConn(pconn, true)

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
			if pconn != nil && pconn.conn != nil {
				pconn.conn.Close()
			}
			pool.mux.Lock()
			pool.activeConns--
			pool.failedConns++
			pool.mux.Unlock()
			log.Printf("[NNTP-POOL] Failed to create new connection: provider='%s': %v", pool.Backend.Provider.Name, err)
			return nil, err
		}
		err = pconn.SwitchMode(wantMode)
		if err != nil {
			pool.mux.Lock()
			pool.activeConns--
			pool.failedConns++
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
		pconn.mux.Lock()
		if pconn.ModeStream && wantMode == MODE_READER_MV {
			if err := pconn.SwitchMode(MODE_READER_MV); err != nil {
				pconn.mux.Unlock()
				pool.closeConn(pconn, true)
				time.Sleep(time.Second)
				log.Printf("[NNTP-POOL] Failed to switch mode: provider='%s': %v", pool.Backend.Provider.Name, err)
				goto newConn
			} else {
				pconn.mux.Unlock()
			}
		} else if pconn.ModeReader && wantMode == MODE_STREAM_MV {
			// INN does not allow switching to mode stream when mode reader is active
			// we must close this connection and create a new one
			log.Printf("[NNTP-POOL] want streaming but got connection in reader mode, closing and getting a new one")
			pconn.mux.Unlock()
			pool.closeConn(pconn, true)
			goto newConn
		} else {
			pconn.mux.Unlock()
		}
		if pool.isConnectionValid(pconn) {
			return pconn, nil
		}
		// Connection expired, close and create new one
		pool.closeConn(pconn, true)
		goto newConn
	case <-time.After(30 * time.Second):
		// Timeout waiting for a connection
		return nil, fmt.Errorf("timeout waiting for connection from pool after 30s")
	}
}

// Put returns a connection to the pool
func (pool *Pool) Put(conn *BackendConn) error {
	forceClose := false
	// Check if connection should be closed
	if conn != nil {
		conn.mux.Lock()
		if conn.forceClose || !conn.IsConnected() {
			forceClose = true
		}
		conn.mux.Unlock()
	}

	pool.mux.RLock()
	if pool.closed || forceClose {
		pool.mux.RUnlock()

		pool.closeConn(conn, true)

		pool.mux.Lock()
		if pool.closed && pool.activeConns == 0 && !pool.chanClosed {
			close(pool.connections)
			pool.chanClosed = true
		}
		pool.mux.Unlock()
		return nil
	}
	pool.mux.RUnlock()

	conn.UpdateLastUsed() // set lastused before returning to pool
	// Try to return connection to pool
	select {
	case pool.connections <- conn:
		// put into pool
		return nil
	default:
		log.Printf("[NNTP-POOL] ERROR: Pool is full or closed. Closing conn for %s:%d", pool.Backend.Host, pool.Backend.Port)
		pool.closeConn(conn, true)
		return nil
	}
}

// Closes a specific connection
func (pool *Pool) closeConn(client *BackendConn, lock bool) error {

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
	if !pool.chanClosed && pool.activeConns == 0 {
		pool.chanClosed = true
		close(pool.connections)
	}
	log.Printf("[NNTP-POOL] Closing (%s:%d) active=%d", pool.Backend.Host, pool.Backend.Port, pool.activeConns)
	allClosed := pool.activeConns == 0
	pool.mux.Unlock()

	if !allClosed {
		// Close all connections in the pool
	closeWait:
		for {
			select {
			case conn, ok := <-pool.connections:
				if !ok {
					break closeWait
				}
				if conn != nil {
					conn.ForceCloseConn()
				}
			default:
				// pass
				break closeWait
			}
		}
	}

	pool.mux.Lock()
	if pool.activeConns > 0 {
		log.Printf("[NNTP-POOL] WARNING: Pool closed with positive count %d active connections remaining ?!?!", pool.activeConns)
	}
	log.Printf("[NNTP-POOL] Closed (%s:%d) created: %d, closed: %d, active: %d, failed: %d", pool.Backend.Host, pool.Backend.Port, pool.totalCreated, pool.totalClosed, pool.activeConns, pool.failedConns)
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
	log.Printf("[NNTP-POOL] Successfully created connection to %s:%d", pool.Backend.Host, pool.Backend.Port)
	return client, nil
}

// isConnectionValid checks if a connection is still valid and not expired
func (pool *Pool) isConnectionValid(client *BackendConn) bool {
	if client == nil {
		return false
	}

	// Acquire read lock to safely access client fields
	client.mux.Lock()
	defer client.mux.Unlock()

	if client.forceClose || !client.IsConnected() {
		return false
	}

	// Check if connection has been idle too long
	if time.Since(client.lastUsed) > pool.idleTimeout {
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
				pool.closeConn(client, true)
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
			pool.closeConn(client, true)
		}
	}
}

// startCleanupWorker starts a goroutine that periodically cleans up expired connections
func (pool *Pool) startCleanupWorker() {
	var closed bool
	for {
		time.Sleep(5 * time.Second)
		pool.Cleanup()
		// Check if pool is closed
		pool.mux.RLock()
		closed = pool.closed
		pool.mux.RUnlock()
		if closed {
			return
		}
	}
}

func (pool *Pool) FileCachedListNewsgroups() ([]string, error) {
	cacheFile := filepath.Join("data", "cache", fmt.Sprintf("%s.list", pool.Backend.Provider.Host))
	groups, err := LoadNewsgroupListFromFile(cacheFile)
	if len(groups) > 0 && err == nil {
		return groups, nil
	} else if err != nil {
		log.Printf("[NNTP-POOL] Failed to load cached newsgroup list from %s: %v", cacheFile, err)
	}
	log.Printf("[NNTP-POOL] No valid cached newsgroup list found at %s, fetching from server...", cacheFile)
	remoteGroups, err := pool.ListNewsgroups()
	if err != nil {
		return nil, err
	}
	if err := WriteNewsgroupListToFile(cacheFile, remoteGroups); err != nil {
		log.Printf("[NNTP-POOL] Failed to write cached newsgroup list to %s: %v", cacheFile, err)
	}
	var returnGroups []string
	for i := range remoteGroups {
		returnGroups = append(returnGroups, remoteGroups[i].Name)
	}
	return returnGroups, nil
}

func WriteNewsgroupListToFile(filename string, groups []GroupInfo) error {
	// Ensure the directory exists
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	for _, group := range groups {
		line := fmt.Sprintf("%s\n", group.Name)
		_, err := writer.WriteString(line)
		if err != nil {
			return fmt.Errorf("failed to write to file: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush writer: %w", err)
	}
	return nil
}

func LoadNewsgroupListFromFile(filename string) ([]string, error) {
	var groups []string
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()
	// check file age
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if time.Since(info.ModTime()) > 24*time.Hour {
		err := os.Remove(filename)
		if err != nil {
			return nil, fmt.Errorf("failed to remove stale cache file: %w", err)
		}
		log.Printf("[NNTP-POOL] Cache file %s is stale (age: %v), refreshing...", filename, time.Since(info.ModTime()))
		return nil, nil
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			log.Printf("[NNTP-POOL] Failed to parse group info from line %q: %v", line, err)
			continue
		}
		groups = append(groups, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	log.Printf("[NNTP-POOL] Loaded %d newsgroups from cache file %s", len(groups), filename)
	return groups, nil
}
