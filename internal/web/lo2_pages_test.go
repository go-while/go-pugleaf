package web

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// lo2PagesSection creates a section, refreshes the router's sections cache and returns its name.
func lo2PagesSection(t *testing.T) *models.Section {
	t.Helper()
	section := &models.Section{
		Name:        fmt.Sprintf("lo2pagessec%d", w0Seq.Add(1)),
		DisplayName: "Lo2 pages",
		CreatedAt:   time.Now(),
	}
	if err := w0DB(t).CreateSection(section); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	// The router only serves sections known to the web sections cache.
	w0Srv.loadSectionsCache()
	return section
}

// lo2PagesAssign puts a group name into a section (no newsgroups row is required).
func lo2PagesAssign(t *testing.T, sectionID int, group string) {
	t.Helper()
	if err := w0DB(t).CreateSectionGroup(&models.SectionGroup{
		SectionID: sectionID, NewsgroupName: group, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateSectionGroup %s: %v", group, err)
	}
}

// TestLo2PagesSectionHugePage: an unbounded ?page= must not overflow (page-1)*LIMIT_sectionPage
// into a negative slice index, which panicked and answered 500 (F15).
func TestLo2PagesSectionHugePage(t *testing.T) {
	section := lo2PagesSection(t)
	lo2PagesAssign(t, section.ID, w0NewGroup(t, true))

	for _, page := range []string{"100000000000000000", "4611686018427387904", "2", "128"} {
		rec := w0Do(t, w0Req{Path: "/" + section.Name + "/?page=" + page})
		if rec.Code != http.StatusOK {
			t.Errorf("GET /%s/?page=%s = %d, want 200", section.Name, page, rec.Code)
		}
	}
}

// TestLo2PagesSectionRouteNeedsGroup: section routes apply the same group access rule as
// /groups/:group - the newsgroup must exist and, for non-admins, be active. section_groups
// rows outlive a deleted newsgroup, and the handlers would otherwise create its group DB (F10).
func TestLo2PagesSectionRouteNeedsGroup(t *testing.T) {
	section := lo2PagesSection(t)

	// A member with no newsgroups row at all.
	gone := w0Name("lo2pages.gone")
	lo2PagesAssign(t, section.ID, gone)
	for _, path := range []string{"/" + section.Name + "/" + gone + "/", "/" + section.Name + "/" + gone + "/tree/1"} {
		if rec := w0Do(t, w0Req{Path: path}); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	w1APINoGroupFile(t, gone)

	// An inactive member: hidden from anonymous and non-admin users, visible to admins.
	inactive := w0NewGroup(t, false)
	lo2PagesAssign(t, section.ID, inactive)
	path := "/" + section.Name + "/" + inactive + "/"
	if rec := w0Do(t, w0Req{Path: path}); rec.Code != http.StatusNotFound {
		t.Errorf("anonymous GET %s = %d, want 404", path, rec.Code)
	}
	_, plain := w0NewUser(t, false)
	if rec := w0Do(t, w0Req{Path: path, Cookies: []*http.Cookie{plain}}); rec.Code != http.StatusNotFound {
		t.Errorf("non-admin GET %s = %d, want 404", path, rec.Code)
	}
	if rec := w0Do(t, w0Req{Path: "/" + section.Name + "/" + inactive + "/tree/1"}); rec.Code != http.StatusNotFound {
		t.Errorf("anonymous GET tree of inactive group = %d, want 404", rec.Code)
	}
	w1APINoGroupFile(t, inactive)

	_, admin := w0NewUser(t, true)
	if rec := w0Do(t, w0Req{Path: path, Cookies: []*http.Cookie{admin}}); rec.Code != http.StatusOK {
		t.Errorf("admin GET %s = %d, want 200 (admins keep access to inactive groups)", path, rec.Code)
	}

	// An active member still works for everyone.
	active := w0NewGroup(t, true)
	lo2PagesAssign(t, section.ID, active)
	activePath := "/" + section.Name + "/" + active + "/"
	if rec := w0Do(t, w0Req{Path: activePath}); rec.Code != http.StatusOK {
		t.Errorf("anonymous GET %s = %d, want 200", activePath, rec.Code)
	}
}

// TestLo2PagesNextThreadsOffsetHeader: the header must not point past the offset the handler
// accepts, or a client following it gets the same page forever (F9).
func TestLo2PagesNextThreadsOffsetHeader(t *testing.T) {
	tests := []struct {
		offset, limit, n int
		want             string
	}{
		{0, 500, 500, "500"},
		{999000, 1000, 1000, "1000000"},
		{1000000, 2000, 2000, ""},
		{999999, 2000, 2000, ""},
		{998000, 2000, 2000, "1000000"},
		{0, 500, 499, ""},
		{0, 500, 0, ""},
	}
	for _, tt := range tests {
		got := nextThreadsOffsetHeader(tt.offset, tt.limit, tt.n, apiThreadsMaxOffset)
		if got != tt.want {
			t.Errorf("nextThreadsOffsetHeader(%d, %d, %d, %d) = %q, want %q",
				tt.offset, tt.limit, tt.n, apiThreadsMaxOffset, got, tt.want)
		}
	}
}
