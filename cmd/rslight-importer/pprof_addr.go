package main

// Copy of cmd/history-rebuild/pprof_addr.go (package main there cannot be imported).

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
)

// loopbackPprofAddr validates the -pprof address and returns the address to
// listen on. Only loopback hosts are accepted: an empty host (":6060") becomes
// 127.0.0.1, "localhost" is allowed, any other host must be a loopback IP.
func loopbackPprofAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("-pprof %q: only loopback addresses are allowed (e.g. localhost:6060): %v", addr, err)
	}
	switch host {
	case "":
		return net.JoinHostPort("127.0.0.1", port), nil
	case "localhost":
		return addr, nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return addr, nil
	}
	return "", fmt.Errorf("-pprof %q: only loopback addresses are allowed (e.g. localhost:6060)", addr)
}

// newPprofMux returns a private mux with the pprof handlers, without
// /debug/pprof/cmdline (it would return the process argv).
func newPprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}
