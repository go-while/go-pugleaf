package web

import (
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/nntp"
)

// TestProviderPresetsValid: every newsdeef preset has a unique host, a name and a routing list
// that the routing validator accepts and that cleanRoutingPatterns keeps as is; the news.tin.org
// preset keeps the archive's tin.it.* groups out and carries a note saying so.
func TestProviderPresetsValid(t *testing.T) {
	presets := providerPresets()
	if len(presets) != 14 {
		t.Errorf("%d presets, want the 14 servers of the newsdeef.eu list", len(presets))
	}
	hosts := map[string]bool{}
	for _, p := range presets {
		if p.Host == "" || p.Name == "" || p.Routing == "" {
			t.Errorf("incomplete preset %+v", p)
		}
		if hosts[p.Host] {
			t.Errorf("duplicate preset host %s", p.Host)
		}
		hosts[p.Host] = true
		if err := nntp.ValidateRoutingPatterns(p.Routing); err != nil {
			t.Errorf("preset %s: routing %q invalid: %v", p.Host, p.Routing, err)
		}
		if got, err := cleanRoutingPatterns(p.Routing); err != nil || got != p.Routing {
			t.Errorf("preset %s: cleanRoutingPatterns(%q) = %q, %v; want it unchanged", p.Host, p.Routing, got, err)
		}
	}
	for host, cases := range map[string]map[string]bool{
		"news.gmane.io":        {"gmane.comp.x": true, "gwene.org.y": true, "comp.lang.c": false},
		"news.tin.org":         {"tin.misc": true, "ka.test": true, "lists.x": true, "tin.it.sport": false},
		"news.digitalmars.com": {"D": true, "D.gnu": true, "digitalmars.D": true, "D.other": false},
	} {
		var routing, note string
		for _, p := range presets {
			if p.Host == host {
				routing, note = p.Routing, p.Note
			}
		}
		if routing == "" {
			t.Errorf("no preset for %s", host)
			continue
		}
		for group, want := range cases {
			got := len(nntp.MatchNewsgroupPatterns(routing, []string{group}).Wanted) > 0
			if got != want {
				t.Errorf("preset %s (%q): wants %s = %v, want %v", host, routing, group, got, want)
			}
		}
		if host == "news.tin.org" && !strings.Contains(note, "tin.it.*") {
			t.Errorf("news.tin.org preset note %q does not explain tin.it.*", note)
		}
	}
}

// TestRoutingShort: the providers table cuts long routing lists.
func TestRoutingShort(t *testing.T) {
	if got := routingShort("gmane.*, gwene.*"); got != "gmane.*, gwene.*" {
		t.Errorf("routingShort(short) = %q", got)
	}
	long := strings.Repeat("comp.lang.x, ", 10)
	if got := routingShort(long); len([]rune(got)) != routingShortMax+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("routingShort(long) = %q, want %d characters and an ellipsis", got, routingShortMax)
	}
}
