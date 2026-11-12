package nntp

import (
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/nntp-testkit/client"
)

// peerMaxSizeIHAVE offers one ~3.8 KB article by IHAVE and returns the final status code.
func peerMaxSizeIHAVE(t *testing.T, c *client.Conn, id, group string) int {
	t.Helper()
	filler := strings.Repeat("x some filler to pass the byte limit\r\n", 100)
	art := append(peerArticleRaw(id, "upstream.invalid!x", group), filler...)
	code, line, sent := c.Ihave(id, art)
	if !sent {
		t.Fatalf("IHAVE %s: %q, want 335 and the article transferred", id, line)
	}
	return code
}

// TestPeerInMaxSize: a peer's in_max_size replaces the server limit for its IHAVE/TAKETHIS,
// lower or higher than the server limit; POST keeps the server limit.
func TestPeerInMaxSize(t *testing.T) {
	g := hGroup(t, "t9.maxsize", 0, nil)
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}

	t.Run("lower than the server", func(t *testing.T) {
		p := hPeerSpec("127.0.0.1", "*")
		p.InMaxSize = 1024
		hs := newTestServer(t, hServerOpts{Proc: proc, Peers: hPeers(p), Hostname: peerFeedHost})
		if code := peerMaxSizeIHAVE(t, hs.dial(t), hMessageID(g, 1), g); code != 437 {
			t.Fatalf("IHAVE of a 3.8 KB article with in_max_size 1024: %d, want 437", code)
		}
	})
	t.Run("higher than the server", func(t *testing.T) {
		p := hPeerSpec("127.0.0.1", "*")
		p.InMaxSize = 65536
		hs := newTestServer(t, hServerOpts{Proc: proc, Peers: hPeers(p), Hostname: peerFeedHost, MaxArtSize: 1024})
		if code := peerMaxSizeIHAVE(t, hs.dial(t), hMessageID(g, 2), g); code != 235 {
			t.Fatalf("IHAVE of a 3.8 KB article with in_max_size 65536 over a 1024 server limit: %d, want 235", code)
		}
	})
	t.Run("server limit without a peer limit", func(t *testing.T) {
		p := hPeerSpec("127.0.0.1", "*")
		hs := newTestServer(t, hServerOpts{Proc: proc, Peers: hPeers(p), Hostname: peerFeedHost, MaxArtSize: 1024})
		if code := peerMaxSizeIHAVE(t, hs.dial(t), hMessageID(g, 3), g); code != 437 {
			t.Fatalf("IHAVE of a 3.8 KB article over a 1024 server limit: %d, want 437", code)
		}
	})
}

// TestArticleSizeSource: a size source replaces Config.NNTP.MaxArtSize while it reports a
// value, and the config applies again when it reports none or is removed.
func TestArticleSizeSource(t *testing.T) {
	hs := newTestServer(t, hServerOpts{MaxArtSize: 1024})
	s := hs.S
	if got := s.MaxArticleBytes(); got != 1024 {
		t.Fatalf("MaxArticleBytes without a source = %d, want 1024", got)
	}
	n, ok := int64(262144), true
	s.SetArticleSizeSource(func() (int64, bool) { return n, ok })
	if got := s.MaxArticleBytes(); got != 262144 {
		t.Fatalf("with a source = %d, want 262144", got)
	}
	ok = false
	if got := s.MaxArticleBytes(); got != 1024 {
		t.Fatalf("source without a value = %d, want the config 1024", got)
	}
	s.SetArticleSizeSource(nil)
	if got := s.MaxArticleBytes(); got != 1024 {
		t.Fatalf("source removed = %d, want 1024", got)
	}
}
