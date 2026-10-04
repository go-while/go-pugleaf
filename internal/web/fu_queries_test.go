package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fuQueriesSection creates a section with a unique name and returns it.
func fuQueriesSection(t *testing.T) *models.Section {
	t.Helper()
	section := &models.Section{
		Name:        w0Name("fuqsec"),
		DisplayName: "Fu queries section",
		CreatedAt:   time.Now(),
	}
	if err := w0DB(t).CreateSection(section); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	// sectionValidationMiddleware routes /:section against this cache, which is built once
	// at startup; without the reload a section created by a test is redirected away.
	w0Srv.loadSectionsCache()
	return section
}

// fuQueriesAddMember puts newsgroupName into section (as a category header when header).
func fuQueriesAddMember(t *testing.T, sectionID int, newsgroupName string, header bool) {
	t.Helper()
	if err := w0DB(t).CreateSectionGroup(&models.SectionGroup{
		SectionID: sectionID, NewsgroupName: newsgroupName,
		IsCategoryHeader: header, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateSectionGroup %s: %v", newsgroupName, err)
	}
}

// fuQueriesDeletePost posts the admin delete form for name and returns the response plus
// the flash messages the handler left for that session.
func fuQueriesDeletePost(t *testing.T, cookie *http.Cookie, name string) (int, string, string) {
	t.Helper()
	rec := w0Do(t, w0Req{
		Method:  http.MethodPost,
		Path:    "/admin/newsgroups/delete",
		Form:    url.Values{"name": {name}},
		Cookies: []*http.Cookie{cookie},
	})
	success, _ := GetAndClearFlash(cookie.Value, "success")
	_, failure := GetAndClearFlash(cookie.Value, "error")
	return rec.Code, success, failure
}

// TestFuQueriesAdminDeleteNewsgroupFlash: the handler used to flash "deleted successfully"
// whenever DeleteNewsgroup returned nil, including for an active group, which the
// `active = 0` guard refuses to delete. Now it reports success only on a real delete (B3).
func TestFuQueriesAdminDeleteNewsgroupFlash(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)

	inactive := w0NewGroup(t, false)
	code, success, failure := fuQueriesDeletePost(t, cookie, inactive)
	if code != http.StatusSeeOther {
		t.Fatalf("POST delete %s = %d, want 303", inactive, code)
	}
	if !strings.Contains(success, "deleted successfully") {
		t.Errorf("deleting the inactive %s: success flash %q, want the success message", inactive, success)
	}
	if failure != "" {
		t.Errorf("deleting the inactive %s: unexpected error flash %q", inactive, failure)
	}
	if _, err := db.GetNewsgroupID(inactive); err == nil {
		t.Errorf("newsgroup %s still exists after the delete", inactive)
	}

	active := w0NewGroup(t, true)
	code, success, failure = fuQueriesDeletePost(t, cookie, active)
	if code != http.StatusSeeOther {
		t.Fatalf("POST delete %s = %d, want 303", active, code)
	}
	if success != "" {
		t.Errorf("deleting the active %s: success flash %q, want none - nothing was deleted", active, success)
	}
	if !strings.Contains(failure, "not deleted") || !strings.Contains(failure, active) {
		t.Errorf("deleting the active %s: error flash %q, want a 'not deleted' message naming the group", active, failure)
	}
	if _, err := db.GetNewsgroupID(active); err != nil {
		t.Errorf("active newsgroup %s disappeared: %v", active, err)
	}
}

// TestFuQueriesSectionPageHidesUnopenableGroups: a section_groups row survives both a
// deactivation and a delete, and the group routes 404 on either, so the listing used to
// show a visitor groups it could not open. Non-admins now get the strict listing; admins
// keep the full one, including the inactive groups they can open (E1).
func TestFuQueriesSectionPageHidesUnopenableGroups(t *testing.T) {
	section := fuQueriesSection(t)
	active := w0NewGroup(t, true)
	inactive := w0NewGroup(t, false)
	orphan := w0Name("fuqorphan") // a section_groups row with no newsgroups row
	header := w0Name("fuqheader") // a category header, never a newsgroup

	fuQueriesAddMember(t, section.ID, active, false)
	fuQueriesAddMember(t, section.ID, inactive, false)
	fuQueriesAddMember(t, section.ID, orphan, false)
	fuQueriesAddMember(t, section.ID, header, true)

	path := "/" + section.Name + "/"

	rec := w0Do(t, w0Req{Path: path})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s (anon) = %d, want 200", path, rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, active) {
		t.Errorf("anon listing of %s does not show the active group %s", path, active)
	}
	if !strings.Contains(body, header) {
		t.Errorf("anon listing of %s dropped the category header %s", path, header)
	}
	if strings.Contains(body, inactive) {
		t.Errorf("anon listing of %s still shows the inactive group %s", path, inactive)
	}
	if strings.Contains(body, orphan) {
		t.Errorf("anon listing of %s still shows the orphan row %s", path, orphan)
	}

	// What the listing promises must match what the group routes serve.
	if rec := w0Do(t, w0Req{Path: "/" + section.Name + "/" + inactive + "/"}); rec.Code != http.StatusNotFound {
		t.Errorf("GET the inactive group in the section (anon) = %d, want 404", rec.Code)
	}

	_, adminCookie := w0NewUser(t, true)
	rec = w0Do(t, w0Req{Path: path, Cookies: []*http.Cookie{adminCookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s (admin) = %d, want 200", path, rec.Code)
	}
	body = rec.Body.String()
	for _, want := range []string{active, inactive, orphan, header} {
		if !strings.Contains(body, want) {
			t.Errorf("admin listing of %s does not show %s; the admin view keeps every member", path, want)
		}
	}
}

// TestFuQueriesSectionPageEmptyAfterFiltering: with every member filtered out the page
// still answers 200, including for a page number far past the end (the pagination clamp
// divides by the page size and slices the empty listing).
func TestFuQueriesSectionPageEmptyAfterFiltering(t *testing.T) {
	section := fuQueriesSection(t)
	fuQueriesAddMember(t, section.ID, w0NewGroup(t, false), false)
	fuQueriesAddMember(t, section.ID, w0Name("fuqorphan2"), false)

	for _, q := range []string{"", "?page=2", "?page=100000000000000000", "?sort=name"} {
		path := "/" + section.Name + "/" + q
		if rec := w0Do(t, w0Req{Path: path}); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}
