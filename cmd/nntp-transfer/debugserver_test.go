package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// txDbgGet fetches path from srv and returns the status code and body.
func txDbgGet(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

// TestTxDbgPprofMuxNoCmdline: OPS-3, the pprof mux serves the index but not
// /debug/pprof/cmdline (argv holds the NNTP and Redis passwords).
func TestTxDbgPprofMuxNoCmdline(t *testing.T) {
	srv := httptest.NewServer(newPprofMux())
	defer srv.Close()

	if code, _ := txDbgGet(t, srv, "/debug/pprof/"); code != http.StatusOK {
		t.Fatalf("/debug/pprof/ = %d, want 200", code)
	}
	code, body := txDbgGet(t, srv, "/debug/pprof/cmdline")
	if code != http.StatusNotFound {
		t.Fatalf("/debug/pprof/cmdline = %d, want 404", code)
	}
	if strings.Contains(body, os.Args[0]) {
		t.Fatalf("/debug/pprof/cmdline body contains argv[0]: %q", body)
	}
}

// TestTxDbgUIServerNoPprof: OPS-3 / A-nntp-client-10, the UI server has its own
// mux: only "/" (exact) and "/results"; pprof and any other path are 404.
func TestTxDbgUIServerNoPprof(t *testing.T) {
	ui := newUIServer("127.0.0.1:0")
	if ui.Handler == nil {
		t.Fatalf("newUIServer: nil Handler would serve http.DefaultServeMux")
	}
	srv := httptest.NewServer(ui.Handler)
	defer srv.Close()

	for _, p := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/nope"} {
		if code, _ := txDbgGet(t, srv, p); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, code)
		}
	}
	if code, _ := txDbgGet(t, srv, "/results"); code != http.StatusOK {
		t.Errorf("/results = %d, want 200", code)
	}
	if code, _ := txDbgGet(t, srv, "/"); code != http.StatusOK && code != http.StatusInternalServerError {
		t.Errorf("/ = %d, want 200 (or 500 for empty template data), never 404", code)
	}
}

// TestTxDbgUIServerTimeouts: OPS-3 (gosec G112/G114), the UI server has timeouts
// and a header size limit; the pprof server has a header timeout.
func TestTxDbgUIServerTimeouts(t *testing.T) {
	ui := newUIServer("127.0.0.1:0")
	if ui.Addr != "127.0.0.1:0" {
		t.Errorf("Addr = %q", ui.Addr)
	}
	if ui.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", ui.ReadHeaderTimeout)
	}
	if ui.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", ui.ReadTimeout)
	}
	if ui.WriteTimeout != 60*time.Second {
		t.Errorf("WriteTimeout = %v, want 60s", ui.WriteTimeout)
	}
	if ui.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", ui.IdleTimeout)
	}
	if ui.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes = %d, want %d", ui.MaxHeaderBytes, 1<<20)
	}
	pp := newPprofServer("localhost:0")
	if pp.Handler == nil || pp.ReadHeaderTimeout != 10*time.Second || pp.IdleTimeout != 120*time.Second {
		t.Errorf("pprof server: Handler nil=%t ReadHeaderTimeout=%v IdleTimeout=%v", pp.Handler == nil, pp.ReadHeaderTimeout, pp.IdleTimeout)
	}
	if pp.ReadTimeout != 0 || pp.WriteTimeout != 0 {
		t.Errorf("pprof server: ReadTimeout=%v WriteTimeout=%v, want 0 (CPU profiles run longer)", pp.ReadTimeout, pp.WriteTimeout)
	}
}

// TestTxDbgRedisKey: NC-3, Redis keys are scoped per target host and port.
func TestTxDbgRedisKey(t *testing.T) {
	got := buildRedisKeyPrefix("peer.example", 119) + "<a@b>"
	if want := "pugleaf:tx:peer.example:119:<a@b>"; got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
	a := buildRedisKeyPrefix("peer-a.example", 119) + "<a@b>"
	b := buildRedisKeyPrefix("peer-b.example", 119) + "<a@b>"
	c := buildRedisKeyPrefix("peer-a.example", 563) + "<a@b>"
	if a == b || a == c {
		t.Fatalf("keys not scoped: %q %q %q", a, b, c)
	}
}
