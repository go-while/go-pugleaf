package main

import (
	"log"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// fetcherShutdownWait bounds how long fetcherShutdown waits for the database
// background tasks (db.WG) before it shuts the database down anyway.
const fetcherShutdownWait = 60 * time.Second

// fetcherBackendConfig builds the BackendConfig for provider p with maxConns
// connections: the config.Provider copy (Provider field) and the proxy fields
// are always set, so a Tor/SOCKS provider is never contacted directly.
func fetcherBackendConfig(p *models.Provider, maxConns int) *nntp.BackendConfig {
	// Convert models.Provider to config.Provider for the BackendConfig
	configProvider := &config.Provider{
		Grp:        p.Grp,
		Name:       p.Name,
		Host:       p.Host,
		Port:       p.Port,
		SSL:        p.SSL,
		Username:   p.Username,
		Password:   p.Password,
		MaxConns:   maxConns,
		Enabled:    p.Enabled,
		Priority:   p.Priority,
		MaxArtSize: p.MaxArtSize,
		Posting:    p.Posting,
		Compress:   p.Compress,
		// Copy proxy configuration from database provider
		ProxyEnabled:  p.ProxyEnabled,
		ProxyType:     p.ProxyType,
		ProxyHost:     p.ProxyHost,
		ProxyPort:     p.ProxyPort,
		ProxyUsername: p.ProxyUsername,
		ProxyPassword: p.ProxyPassword,
	}

	return &nntp.BackendConfig{
		Host:     p.Host,     // copy values to first level
		Port:     p.Port,     // copy values to first level
		SSL:      p.SSL,      // copy values to first level
		Username: p.Username, // copy values to first level
		Password: p.Password, // copy values to first level
		MaxConns: maxConns,   // copy values to first level
		//ConnectTimeout: 30 * time.Second,
		//ReadTimeout:    60 * time.Second,
		//WriteTimeout:   30 * time.Second,
		Provider: configProvider, // Set the Provider field
		Compress: p.Compress,     // COMPRESS DEFLATE (RFC 8054) when the provider asks for it
		// Copy proxy configuration from database provider
		ProxyEnabled:  p.ProxyEnabled,
		ProxyType:     p.ProxyType,
		ProxyHost:     p.ProxyHost,
		ProxyPort:     p.ProxyPort,
		ProxyUsername: p.ProxyUsername,
		ProxyPassword: p.ProxyPassword,
	}
}

// openFetcherDB opens the main database below dataDir (default DB config otherwise).
// The returned cleanup closes it again through fetcherShutdown; calling it more than
// once is safe.
func openFetcherDB(dataDir string) (*database.Database, func(), error) {
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dataDir
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		return nil, func() {}, err
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() { fetcherShutdown(db) })
	}
	return db, cleanup, nil
}

// fetcherShutdown stops the database background tasks (close StopChan), waits for
// them at most fetcherShutdownWait and then shuts the database down.
func fetcherShutdown(db *database.Database) {
	close(db.StopChan)
	done := make(chan struct{})
	go func() {
		db.WG.Wait()
		close(done)
	}()
	timer := time.NewTimer(fetcherShutdownWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Printf("[FETCHER]: database background tasks did not stop within %v, shutting down anyway", fetcherShutdownWait)
	}
	if err := db.Shutdown(); err != nil {
		log.Printf("[FETCHER]: Failed to shutdown database: %v", err)
	}
}
