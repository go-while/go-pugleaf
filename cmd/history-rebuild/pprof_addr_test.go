package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHRPprofAddr: OPS-3, history-rebuild -pprof accepts loopback addresses only.
func TestHRPprofAddr(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: ":6060", want: "127.0.0.1:6060"},
		{in: "localhost:6060", want: "localhost:6060"},
		{in: "127.0.0.1:1", want: "127.0.0.1:1"},
		{in: "[::1]:2", want: "[::1]:2"},
		{in: "0.0.0.0:6060", wantErr: true},
		{in: "192.0.2.1:6060", wantErr: true},
		{in: "example.org:1", wantErr: true},
		{in: "6060", wantErr: true},
		{in: "[::]:6060", wantErr: true},
	}
	for _, c := range cases {
		got, err := loopbackPprofAddr(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("loopbackPprofAddr(%q) = %q, want error", c.in, got)
			} else if !strings.Contains(err.Error(), "loopback") {
				t.Errorf("loopbackPprofAddr(%q) error %q does not mention loopback", c.in, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("loopbackPprofAddr(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// TestHRPprofMuxNoCmdline: OPS-3, the history-rebuild pprof mux has no /debug/pprof/cmdline.
func TestHRPprofMuxNoCmdline(t *testing.T) {
	srv := httptest.NewServer(newPprofMux())
	defer srv.Close()
	for path, want := range map[string]int{"/debug/pprof/": http.StatusOK, "/debug/pprof/cmdline": http.StatusNotFound} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}
