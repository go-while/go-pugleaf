package web

import (
	"net/http"
	"strings"
	"testing"
)

// TestHmHomeAndMemorial: the home page carries the quick links and this node's site news,
// not the old development checklists or the node list; the tribute moved to /SiteMemory,
// which every page links from its footer.
func TestHmHomeAndMemorial(t *testing.T) {
	rec := w0Do(t, w0Req{Path: "/"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, gone := range []string{"Development Updates", "Network Nodes", "- [x]", "Gateway Goes Live"} {
		if strings.Contains(body, gone) {
			t.Errorf("home page still contains %q", gone)
		}
	}
	for _, want := range []string{`href="/groups"`, `href="/SiteNews"`, "Site News", `href="/SiteMemory"`} {
		if !strings.Contains(body, want) {
			t.Errorf("home page lacks %s", want)
		}
	}

	rec = w0Do(t, w0Req{Path: "/SiteMemory"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Retro Guy") {
		t.Errorf("GET /SiteMemory: status %d, tribute present: %t", rec.Code, strings.Contains(rec.Body.String(), "Retro Guy"))
	}
	rec = w0Do(t, w0Req{Path: "/SiteMemory/"})
	if rec.Code != http.StatusOK {
		t.Errorf("GET /SiteMemory/: status %d, want 200", rec.Code)
	}
	if !knownNonSectionPaths["SiteMemory"] {
		t.Error("SiteMemory is not a reserved section name")
	}
}
