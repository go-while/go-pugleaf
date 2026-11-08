package web

import (
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// TestPeerConfigBothWays: a peer that feeds us by address and gets our feed produces the INN
// incoming.conf/newsfeeds/innfeed.conf blocks and a Diablo label with both directions.
func TestPeerConfigBothWays(t *testing.T) {
	p := &models.Peer{Name: "b", PathToken: "news.b.example", Hosts: "198.51.100.7",
		InPatterns: "comp.*, !comp.lang.*,@alt.binaries.*", OutEnabled: true, OutHost: "news.b.example",
		OutPort: 119, OutPatterns: "*,!control*,!junk", MaxConns: 3}
	got := peerConfigSnippets(p, "news.a.example")

	for _, want := range []string{
		"peer news.a.example {\n    # the address(es) our feed comes from\n    hostname:        \"news.a.example\"",
		"patterns:        \"*,!control*,!junk\"",
		"max-connections: 3",
		"news.a.example\\\n    :comp.*,!comp.lang.*,@alt.binaries.*,!control,!control.*\\\n    :Tm:innfeed!",
		"ip-name:         \"news.a.example\"",
		"port-number:     119",
		"We accept your feed from: 198.51.100.7",
	} {
		if !strings.Contains(got.INN, want) {
			t.Errorf("INN snippet lacks %q:\n%s", want, got.INN)
		}
	}
	if strings.Contains(got.INN, "password:") || strings.Contains(got.INN, "username:") {
		t.Errorf("INN snippet mentions a login although the peer has none:\n%s", got.INN)
	}
	for _, want := range []string{
		"label news.a.example\n    alias       news.a.example\n",
		"inhost      news.a.example",
		"maxconnect  3",
		"hostname    news.a.example",
		"addgroup    comp.*\n    delgroup    comp.lang.*\n    delgroupany alt.binaries.*\n    # we refuse control messages\n    delgroup    control.*\nend",
		"What we send you: *,!control*,!junk",
	} {
		if !strings.Contains(got.Diablo, want) {
			t.Errorf("Diablo snippet lacks %q:\n%s", want, got.Diablo)
		}
	}
	if strings.Contains(got.Diablo, "dnewslink sends no AUTHINFO") {
		t.Errorf("Diablo snippet warns about a login although the peer has hosts:\n%s", got.Diablo)
	}
}

// TestPeerConfigLoginOnly: a login-only peer without our outgoing feed gets innfeed
// username/password (placeholder), no incoming.conf block, and a Diablo warning that
// dnewslink cannot log in.
func TestPeerConfigLoginOnly(t *testing.T) {
	p := &models.Peer{Name: "c", PathToken: "news.c.example", InUsername: "c-feed", InPatterns: "*",
		InPasswordHash: "$2a$10$secret", OutPassword: "out-secret"}
	got := peerConfigSnippets(p, "news.a.example")
	if !strings.Contains(got.INN, "nothing to add, we do not feed you") {
		t.Errorf("INN snippet: want no incoming.conf block:\n%s", got.INN)
	}
	if !strings.Contains(got.INN, "username:        \"c-feed\"") || !strings.Contains(got.INN, "<the login password set for you on our side>") {
		t.Errorf("INN snippet: want the innfeed login with a password placeholder:\n%s", got.INN)
	}
	if !strings.Contains(got.Diablo, "dnewslink sends no AUTHINFO") {
		t.Errorf("Diablo snippet: want the no-AUTHINFO note:\n%s", got.Diablo)
	}
	if strings.Contains(got.Diablo, "inhost") {
		t.Errorf("Diablo snippet: inhost without our outgoing feed:\n%s", got.Diablo)
	}
	for _, s := range []string{got.INN, got.Diablo} {
		if strings.Contains(s, "secret") {
			t.Errorf("a snippet contains a password:\n%s", s)
		}
	}
}

// TestPeerConfigNoHost: without a configured host name the snippets use a visible placeholder.
func TestPeerConfigNoHost(t *testing.T) {
	got := peerConfigSnippets(&models.Peer{Name: "d", PathToken: "d", Hosts: "192.0.2.1", InPatterns: "*"}, " ")
	if !strings.Contains(got.INN, "news.your-site.example") || !strings.Contains(got.Diablo, "news.your-site.example") {
		t.Errorf("want the placeholder host name:\n%s\n%s", got.INN, got.Diablo)
	}
}
