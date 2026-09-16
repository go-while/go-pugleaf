// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"context"
	"errors"
	"log"
	"net"
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

// shutdownGrace bounds the extra work Shutdown does after its context ended: waiting for the
// handlers whose request context it just cancelled, and for the final API token usage flush.
const shutdownGrace = 5 * time.Second

// Start starts the web server with SSL support if configured.
// It returns http.ErrServerClosed after Shutdown.
func (s *WebServer) Start() error {
	if s.Config.SSL && (s.Config.CertFile == "" || s.Config.KeyFile == "") {
		return errors.New("SSL enabled but cert_file or key_file not specified in config")
	}
	addr := ":" + strconv.Itoa(s.Config.ListenPort)
	select {
	case <-s.stopCh:
		return http.ErrServerClosed
	default:
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.serveOn(ln)
}

// serveOn serves HTTP (HTTPS when configured) on ln until Shutdown, and always closes ln.
// Start uses it; tests serve on a 127.0.0.1:0 listener. It returns http.ErrServerClosed after Shutdown.
func (s *WebServer) serveOn(ln net.Listener) error {
	s.StartTime = time.Now() // Set the start time for uptime calculations
	addr := ln.Addr().String()
	srv := newHTTPServer(addr, s.rootHandler())
	// Every request context derives from baseCtx, so Shutdown can cancel handlers (and their
	// outbound calls) that are still running when its deadline passes.
	srv.BaseContext = func(net.Listener) context.Context { return s.baseCtx }

	s.httpServerMu.Lock()
	select {
	case <-s.stopCh:
		s.httpServerMu.Unlock()
		_ = ln.Close()
		return http.ErrServerClosed
	default:
	}
	s.httpServer = srv
	s.httpServerMu.Unlock()

	if s.Config.SSL {
		log.Printf("[WEB]: Starting HTTPS server on %s", addr)
		return srv.ServeTLS(ln, s.Config.CertFile, s.Config.KeyFile)
	}
	log.Printf("[WEB]: Starting HTTP server on %s", addr)
	return srv.Serve(ln)
}

// Shutdown stops the background goroutines of the web server (session cleanup, chat cache sweeper,
// API token usage flusher) and gracefully shuts down the HTTP server started by Start, waiting for
// active requests until ctx ends. When ctx ends first, it cancels every request context and waits
// up to shutdownGrace more, then logs the handlers that are still running. The final token usage
// flush always finishes before Shutdown returns, because the caller closes the database next.
func (s *WebServer) Shutdown(ctx context.Context) error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.httpServerMu.Lock()
	srv := s.httpServer
	s.httpServerMu.Unlock()

	var err error
	if srv != nil {
		if err = srv.Shutdown(ctx); err != nil {
			log.Printf("[WEB]: graceful shutdown ended with %d request(s) in flight (%v), cancelling them",
				s.inFlightRequests.Load(), err)
			s.cancelBase()
			deadline := time.Now().Add(shutdownGrace)
			for s.inFlightRequests.Load() > 0 && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if n := s.inFlightRequests.Load(); n > 0 {
				log.Printf("[WEB]: %d request(s) still running after %v, continuing shutdown", n, shutdownGrace)
			}
		}
	}

	if s.tokenUsageDone != nil {
		select {
		case <-s.tokenUsageDone:
		case <-time.After(shutdownGrace):
			log.Printf("[WEB]: API token usage flush did not finish within %v", shutdownGrace)
		}
	}
	if s.cancelBase != nil {
		s.cancelBase()
	}
	return err
}
