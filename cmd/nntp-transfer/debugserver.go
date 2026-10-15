package main

import (
	"net/http"
	"net/http/pprof"
	"time"
)

// newPprofMux returns a private mux with the pprof handlers. /debug/pprof/cmdline
// is deliberately not registered: it returns the process argv, which holds the
// NNTP and Redis passwords; pprof.Index answers 404 "Unknown profile" for it.
func newPprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// newPprofServer returns the pprof server for addr (main passes localhost:<port>).
// It has no read/write timeout so long CPU profiles and traces still work.
func newPprofServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           newPprofMux(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// newUIServer returns the results web UI server for addr on its own mux: only
// "/" (exact) and "/results" are served; every other path, /debug/pprof/
// included, is 404.
func newUIServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", handleIndex)
	mux.HandleFunc("/results", handleResults)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
