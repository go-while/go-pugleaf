// Package web provides the HTTP server and web interface for go-pugleaf
package web

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"github.com/go-while/go-pugleaf/internal/cache"
	"github.com/go-while/go-pugleaf/internal/models"
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
// up to shutdownGrace more, then logs the handlers that are still running. The API token usage of
// the drained requests is written before Shutdown returns, because the caller closes the database
// next: it waits for the flusher's own last flush and then flushes once more itself. Last, it
// stops the cleanup goroutines of the global sanitized and newsgroup caches (once per cache instance).
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
			} else {
				// Every handler stopped after the cancellation, so the shutdown did complete:
				// do not report the graceful deadline as a failure to the caller.
				log.Printf("[WEB]: all requests finished after cancellation, web server stopped")
				err = nil
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
	// The flusher stops as soon as stopCh closes, which is before the drain above: every API
	// request served during the drain was counted into a buffer nobody writes any more.
	// Flush once more here, with the drained requests included, but bounded like every other
	// step: one UPDATE on a main DB that another process holds open retries for minutes of
	// busy time (busy_timeout is 30s per attempt, the retry cap counts busy time only), and
	// nothing else may shut down meanwhile. flushTokenUsage logs the counts it could not
	// write, so giving up here loses no information.
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		s.flushTokenUsage()
	}()
	// Bounded by shutdownGrace, not by ctx: when ctx already ended (the case this flush exists
	// for) a ctx bound would give the drained counts no chance at all.
	select {
	case <-flushed:
	case <-time.After(shutdownGrace):
		log.Printf("[WEB]: API token usage of the drained requests not written within %v, giving up on it", shutdownGrace)
	}

	if s.cancelBase != nil {
		s.cancelBase()
	}
	stopGlobalCaches()
	return err
}

// stoppedCaches remembers which cache instances stopGlobalCaches has stopped. The sanitized and
// newsgroup caches are process globals shared by every WebServer; NewsgroupCache.Stop is a bare
// close that panics on a second call, while Shutdown may run several times (per server, and for
// several servers in the tests). The guard is per instance: a cache re-initialised after a
// Shutdown (models.InitSanitizedCache / InitNewsgroupCache) is a new instance and is stopped by
// the next Shutdown, and an instance is never stopped twice. The sets hold weak pointers, so a
// replaced cache is not kept alive; entries of reclaimed caches are pruned on the next stop.
var stoppedCaches struct {
	mu        sync.Mutex
	sanitized map[weak.Pointer[cache.SanitizedCache]]struct{}
	newsgroup map[weak.Pointer[cache.NewsgroupCache]]struct{}
}

// stopGlobalCaches stops the cleanup goroutines of the current global sanitized and newsgroup
// caches, once per cache instance (A-web-19). A stopped cache still serves Get and Set; only
// expiry sweeping ends.
func stopGlobalCaches() {
	stopCaches(models.GetSanitizedCache(), models.GetNewsgroupCache())
}

// stopCaches stops sc and nc unless that instance was stopped before; nil is skipped.
func stopCaches(sc *cache.SanitizedCache, nc *cache.NewsgroupCache) {
	stoppedCaches.mu.Lock()
	defer stoppedCaches.mu.Unlock()
	if stopOncePerInstance(&stoppedCaches.sanitized, sc) {
		sc.Stop()
	}
	if stopOncePerInstance(&stoppedCaches.newsgroup, nc) {
		nc.Stop()
	}
}

// stopOncePerInstance records p in *set and reports whether it was not there yet (the caller
// stops it then). A nil p is never recorded. Entries of reclaimed instances are dropped.
// Caller holds stoppedCaches.mu.
func stopOncePerInstance[T any](set *map[weak.Pointer[T]]struct{}, p *T) bool {
	if p == nil {
		return false
	}
	if *set == nil {
		*set = make(map[weak.Pointer[T]]struct{})
	}
	key := weak.Make(p)
	if _, done := (*set)[key]; done {
		return false
	}
	for k := range *set {
		if k.Value() == nil {
			delete(*set, k)
		}
	}
	(*set)[key] = struct{}{}
	return true
}
