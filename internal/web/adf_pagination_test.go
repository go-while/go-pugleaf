package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// adfPageGet serves path and fails the test unless it answers 200; it returns the body.
func adfPageGet(t *testing.T, path string) string {
	t.Helper()
	rec := w0Do(t, w0Req{Path: path})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, rec.Code)
	}
	return rec.Body.String()
}

// TestAdfPageGroupPastEnd: an in-range page past the real end (the clamp bound is 101, so
// ?page=50 survives it) finds no row at its OFFSET and serves the newest page. The header
// must say page 1, not "Page 50 of 3" over page 1's articles (FU:E17). Both twins: the group
// page and the section group page.
func TestAdfPageGroupPastEnd(t *testing.T) {
	// A 3-page group by count, holding a single real article: OFFSET 49*128-1 has no row.
	group := fuPaginationBigGroup(t, 3*LIMIT_groupPage)
	section := lo2PagesSection(t)
	lo2PagesAssign(t, section.ID, group)

	for _, path := range []string{
		"/groups/" + group + "?page=50",
		"/" + section.Name + "/" + group + "/?page=50",
	} {
		body := adfPageGet(t, path)
		if strings.Contains(body, "Page <strong>50</strong>") {
			t.Errorf("%s: page 1's articles are labelled page 50: %.2000s", path, body)
		}
		if !strings.Contains(body, "Page <strong>1</strong>") {
			t.Errorf("%s: header does not say page 1: %.2000s", path, body)
		}
		if !strings.Contains(body, "fuPagination article") {
			t.Errorf("%s: the newest page was not served: %.2000s", path, body)
		}
		// Described as page 1 of 3, the page links its real neighbour.
		if !strings.Contains(body, `href="?page=2"`) {
			t.Errorf("%s: page 1 of 3 has no Next link to page 2: %.2000s", path, body)
		}
	}
}

// TestAdfPageCursorNoNeighbourLinks: a cursor view has no page number, so it must not link
// First/Previous/Next as ?page=0 or ?page=1 (both serve page 1) - D15 suppresses them (FU:E12).
func TestAdfPageCursorNoNeighbourLinks(t *testing.T) {
	group := fuPaginationBigGroup(t, 3*LIMIT_groupPage)
	section := lo2PagesSection(t)
	lo2PagesAssign(t, section.ID, group)

	for _, path := range []string{
		"/groups/" + group + "?cursor=2",
		"/" + section.Name + "/" + group + "/?cursor=2",
	} {
		body := adfPageGet(t, path)
		if !strings.Contains(body, "fuPagination article") {
			t.Errorf("%s: the article below the cursor was not served: %.2000s", path, body)
		}
		if strings.Contains(body, `href="?page=0"`) {
			t.Errorf("%s: cursor view links ?page=0: %.2000s", path, body)
		}
		for _, label := range []string{`aria-label="First"`, `aria-label="Previous"`, `aria-label="Next"`} {
			if strings.Contains(body, label) {
				t.Errorf("%s: cursor view still links %s", path, label)
			}
		}
	}
}

// TestAdfPageThreadClamp: an out-of-range ?page= of a two-page thread is clamped to page 2 and
// that page is fetched again, so it carries its replies and its navigation (FU:E14).
func TestAdfPageThreadClamp(t *testing.T) {
	const replies = 60 // page 1: root + 50 replies, page 2: 10 replies (article_num 52..61)
	group := fuPaginationThread(t, replies)
	body := adfPageGet(t, "/groups/"+group+"/thread/1?page=9")

	if !strings.Contains(body, "Page 2 of 2") {
		t.Errorf("?page=9 is not rendered as page 2 of 2: %.2000s", body)
	}
	// Every rendered message carries one "#<article_num>" badge.
	if got := strings.Count(body, `<span class="text-light">#`); got != 10 {
		t.Errorf("?page=9 rendered %d messages, want the 10 replies of page 2", got)
	}
	for num := ThreadMessages_perPage + 2; num <= replies+1; num++ {
		if !strings.Contains(body, fmt.Sprintf(`<span class="text-light">#%d</span>`, num)) {
			t.Errorf("?page=9 does not hold reply #%d of page 2", num)
		}
	}
	if strings.Contains(body, `<span class="text-light">#2</span>`) {
		t.Errorf("?page=9 holds reply #2, which belongs to page 1")
	}
	// The navigation is present and leads back to page 1.
	if !strings.Contains(body, `href="?page=1"`) {
		t.Errorf("?page=9 has no navigation back to page 1: %.2000s", body)
	}
}
