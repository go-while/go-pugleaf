// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// most code moved into
/*

	### **Core Files:**
	1. **`server_core.go`** - Server setup, route configuration, and data structures
	2. **`utils.go`** - Utility functions, template helpers, and rendering functions

	### **Page Handler Files:**
	3. **`homePage.go`** - Home/root page handler
	4. **`groupsPage.go`** - Groups listing page handler
	5. **`groupPage.go`** - Individual group page handler
	6. **`articlePage.go`** - Article page handlers (by number and message ID)
	7. **`searchPage.go`** - Search functionality page handler
	8. **`statsPage.go`** - Statistics page handler
	9. **`helpPage.go`** - Help page handler
	10. **`sectionsPage.go`** - All section-related page handlers
	11. **`threadPage.go`** - Single thread flat view handler
	12. **`threadTreePage.go`** - Thread tree view handlers and API

	### **API File:**
	13. **`apiHandlers.go`** - All REST API endpoints that return JSON


*/

// newHTTPServer returns an http.Server with timeouts, so slow or idle clients cannot hold
// connections forever (slowloris). WriteTimeout leaves room for the AI chat proxy call (90s).
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// rootHandler is the handler chain Start serves: cross-origin (CSRF) protection around the router.
// Unsafe methods (POST, ...) are rejected with 403 when Sec-Fetch-Site or Origin show a
// cross-origin browser request; requests without those headers (curl, API clients) pass.
// Rejections are logged (at most one line per second), because a reverse proxy that rewrites Host
// makes every browser POST fail the Origin/Host comparison.
func (s *WebServer) rootHandler() http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(crossOriginDenied))
	return cop.Handler(s.Router)
}

// crossOriginLogLast is the UnixNano time of the last logged cross-origin rejection.
var crossOriginLogLast atomic.Int64

// crossOriginDenied answers a request rejected by CrossOriginProtection with 403 and logs it (sampled).
func crossOriginDenied(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UnixNano()
	last := crossOriginLogLast.Load()
	if now-last >= int64(time.Second) && crossOriginLogLast.CompareAndSwap(last, now) {
		log.Printf("[WEB]: cross-origin request rejected: method=%s path=%s origin=%q sec-fetch-site=%q host=%q",
			r.Method, r.URL.Path, r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site"), r.Host)
	}
	http.Error(w, "403 cross-origin request rejected", http.StatusForbidden)
}

// Start starts the web server with SSL support if configured.
// It returns http.ErrServerClosed after Shutdown.
func (s *WebServer) Start() error {
	addr := ":" + strconv.Itoa(s.Config.ListenPort)
	s.StartTime = time.Now() // Set the start time for uptime calculations
	if s.Config.SSL && (s.Config.CertFile == "" || s.Config.KeyFile == "") {
		return errors.New("SSL enabled but cert_file or key_file not specified in config")
	}

	srv := newHTTPServer(addr, s.rootHandler())
	s.httpServerMu.Lock()
	select {
	case <-s.stopCh:
		s.httpServerMu.Unlock()
		return http.ErrServerClosed
	default:
	}
	s.httpServer = srv
	s.httpServerMu.Unlock()

	if s.Config.SSL {
		log.Printf("Starting HTTPS server on %s", addr)
		return srv.ListenAndServeTLS(s.Config.CertFile, s.Config.KeyFile)
	}
	log.Printf("Starting HTTP server on %s", addr)
	return srv.ListenAndServe()
}

// Shutdown stops the background goroutines of the web server (session cleanup, chat cache sweeper)
// and gracefully shuts down the HTTP server started by Start, waiting for active requests until ctx ends.
func (s *WebServer) Shutdown(ctx context.Context) error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.httpServerMu.Lock()
	srv := s.httpServer
	s.httpServerMu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}
