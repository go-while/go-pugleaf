package processor

// Tests of the followup-leftovers slice fl-ops-tools (analyze): an IPv6 host literal
// gives a usable overview cache directory name; plain hosts keep their old name.

import (
	"testing"

	"github.com/go-while/go-pugleaf/internal/nntp"
)

func TestFlOpsAnalyzeProviderNameIPv6(t *testing.T) {
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		{"[2001:db8::1]", "2001_db8__1", true},
		{"[2001:db8::1]:119", "2001_db8__1", true},
		{"2001:db8::1", "2001_db8__1", true},
		{"[::1]:563", "__1", true},
		{"[]", "", false},
		{"[]:119", "", false},
		// plain hosts keep their historic directory name
		{"news.example.org", "news.example.org", true},
		{"news.example.org:563", "news.example.org", true},
		{"192.0.2.7:119", "192.0.2.7", true},
	}
	for _, c := range cases {
		proc := &Processor{Pool: &nntp.Pool{Backend: &nntp.BackendConfig{Host: c.host}}}
		got, err := proc.analyzeProviderName()
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("host %q: got %q, %v; want %q", c.host, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("host %q: got %q, want error", c.host, got)
		}
	}
}
