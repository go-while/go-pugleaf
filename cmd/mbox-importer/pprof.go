package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"
)

// startPprof serves the net/http/pprof handlers on addr (-pprof, a debug aid) and turns on
// block and mutex profiling, so a slow import shows where its goroutines wait:
// go tool pprof http://ADDR/debug/pprof/block, or /debug/pprof/goroutine?debug=1.
// The returned func shuts the listener down.
func startPprof(addr string) func() {
	runtime.SetBlockProfileRate(int(time.Millisecond)) // one sample per ms spent blocked
	runtime.SetMutexProfileFraction(10)
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[MBOX-IMPORT] -pprof %q: %v (profiling not served)", addr, err)
		return func() {}
	}
	log.Printf("[MBOX-IMPORT] -pprof: serving http://%s/debug/pprof/", ln.Addr())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[MBOX-IMPORT] -pprof: %v", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-done
	}
}
