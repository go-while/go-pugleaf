package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// fuPaginationRender executes the "pagination" template with p and returns the HTML.
func fuPaginationRender(t *testing.T, p *models.PaginationInfo) string {
	t.Helper()
	tmpl, err := loadTemplates("fu-pagination", nil, templateDir+"pagination.html")
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "pagination", struct{ Pagination *models.PaginationInfo }{p}); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	return buf.String()
}

// TestFuPaginationNoBoundUnchanged: NewPaginationInfo without a bound must behave exactly as
// before - no clamp, and the "Last" link still goes to the real last page.
func TestFuPaginationNoBoundUnchanged(t *testing.T) {
	for _, tt := range []struct {
		page, size, count     int
		wantPages, wantHasNxt int
	}{
		{1, 25, 0, 1, 0},
		{1, 25, 100, 4, 1},
		{4, 25, 100, 4, 0},
		{1, 0, 10, 10, 1}, // the pageSize < 1 guard wave 1 added
		{7, 25, 100, 4, 0},
	} {
		p := models.NewPaginationInfo(tt.page, tt.size, tt.count)
		if p.TotalPages != tt.wantPages {
			t.Errorf("NewPaginationInfo(%d,%d,%d).TotalPages = %d, want %d",
				tt.page, tt.size, tt.count, p.TotalPages, tt.wantPages)
		}
		if p.LinkablePages != 0 || p.IsClamped() {
			t.Errorf("NewPaginationInfo(%d,%d,%d): LinkablePages = %d, IsClamped = %v, want 0/false",
				tt.page, tt.size, tt.count, p.LinkablePages, p.IsClamped())
		}
		if p.LastLinkablePage() != p.TotalPages {
			t.Errorf("NewPaginationInfo(%d,%d,%d): LastLinkablePage = %d, want TotalPages %d",
				tt.page, tt.size, tt.count, p.LastLinkablePage(), p.TotalPages)
		}
		if want := tt.wantHasNxt == 1; p.HasNext != want {
			t.Errorf("NewPaginationInfo(%d,%d,%d).HasNext = %v, want %v",
				tt.page, tt.size, tt.count, p.HasNext, want)
		}
	}

	// A zero (or negative) bound is "no bound" as well.
	for _, bound := range []int{0, -1} {
		p := models.NewPaginationInfo(1, 25, 100, bound)
		if p.LinkablePages != 0 || p.LastLinkablePage() != 4 || !p.HasNext {
			t.Errorf("bound %d: LinkablePages = %d, LastLinkablePage = %d, HasNext = %v, want 0/4/true",
				bound, p.LinkablePages, p.LastLinkablePage(), p.HasNext)
		}
	}
	if body := fuPaginationRender(t, models.NewPaginationInfo(1, 25, 100)); !strings.Contains(body, `href="?page=4"`) {
		t.Errorf("unclamped pagination has no Last link to page 4: %s", body)
	}
}

// TestFuPaginationBoundedLinks: with the article clamp bound, no rendered link may point
// above maxOffsetArticles/pageSize+1 - the handler would serve another page under that URL (D1).
func TestFuPaginationBoundedLinks(t *testing.T) {
	const size = 128
	wantBound := maxOffsetArticles/size + 1 // 101
	if wantBound != clampOffsetPage(1<<30, size, maxOffsetArticles) {
		t.Fatalf("bound %d disagrees with clampOffsetPage", wantBound)
	}

	// A group with 5000 pages of articles.
	p := models.NewPaginationInfo(1, size, 5000*size, maxOffsetArticles)
	if p.TotalPages != 5000 {
		t.Fatalf("TotalPages = %d, want 5000", p.TotalPages)
	}
	if p.LinkablePages != wantBound || !p.IsClamped() || p.LastLinkablePage() != wantBound {
		t.Fatalf("LinkablePages = %d, IsClamped = %v, LastLinkablePage = %d, want %d/true/%d",
			p.LinkablePages, p.IsClamped(), p.LastLinkablePage(), wantBound, wantBound)
	}
	body := fuPaginationRender(t, p)
	if strings.Contains(body, `href="?page=5000"`) {
		t.Errorf("rendered a Last link to page 5000, which the clamp rewrites to %d: %s", wantBound, body)
	}
	if !strings.Contains(body, fmt.Sprintf(`href="?page=%d"`, wantBound)) {
		t.Errorf("no Last link to page %d: %s", wantBound, body)
	}
	if !strings.Contains(body, "cursor") {
		t.Errorf("clamped listing does not mention the cursor parameter: %s", body)
	}

	// On the last linkable page there is no next page to offer.
	last := models.NewPaginationInfo(wantBound, size, 5000*size, maxOffsetArticles)
	if last.HasNext {
		t.Errorf("page %d of a clamped listing still has a next page", wantBound)
	}
	lastBody := fuPaginationRender(t, last)
	if strings.Contains(lastBody, fmt.Sprintf(`href="?page=%d"`, wantBound+1)) {
		t.Errorf("rendered a Next link to page %d, above the clamp: %s", wantBound+1, lastBody)
	}

	// A group small enough to fit under the bound keeps every link and says nothing.
	small := models.NewPaginationInfo(1, size, 10*size, maxOffsetArticles)
	if small.IsClamped() || small.LastLinkablePage() != 10 {
		t.Errorf("small listing: IsClamped = %v, LastLinkablePage = %d, want false/10",
			small.IsClamped(), small.LastLinkablePage())
	}
	if smallBody := fuPaginationRender(t, small); !strings.Contains(smallBody, `href="?page=10"`) ||
		strings.Contains(smallBody, "cursor") {
		t.Errorf("small listing lost its Last link or gained a clamp note: %s", smallBody)
	}
}

// fuPaginationBigGroup creates an active group whose newsgroups row claims count articles
// (GetOverviewsPaginated reads the total from there) and inserts a single real article.
func fuPaginationBigGroup(t *testing.T, count int) string {
	t.Helper()
	group := w0NewGroup(t, true)
	if _, err := database.RetryableExec(w0DB(t).GetMainDB(),
		"UPDATE newsgroups SET message_count = ?, last_article = ? WHERE name = ?", count, count, group); err != nil {
		t.Fatalf("UPDATE newsgroups: %v", err)
	}
	groupDB, err := w0DB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()
	now := time.Now().Add(-time.Hour)
	ov := &models.Overview{
		ArticleNum: 1, Subject: "fuPagination article", FromHeader: "a <a@test.invalid>",
		DateSent: now, DateString: now.Format(time.RFC1123Z),
		MessageID: "<fupagination-" + group + "@test.invalid>", Downloaded: 1,
	}
	if _, err := w0DB(t).InsertOverview(groupDB, ov); err != nil {
		t.Fatalf("InsertOverview: %v", err)
	}
	return group
}

// TestFuPaginationGroupPageHugePage: /groups/:group?page=5000 still answers 200, and the page
// it serves links no page above the clamp bound (D1).
func TestFuPaginationGroupPageHugePage(t *testing.T) {
	bound := maxOffsetArticles/LIMIT_groupPage + 1
	group := fuPaginationBigGroup(t, 5000*LIMIT_groupPage)

	for _, page := range []string{"1", "5000", "100000000000000000"} {
		rec := w0Do(t, w0Req{Path: "/groups/" + group + "?page=" + page})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /groups/%s?page=%s = %d, want 200", group, page, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, `href="?page=5000"`) {
			t.Errorf("?page=%s: body links page 5000, which the clamp rewrites to %d", page, bound)
		}
		for _, above := range []int{bound + 1, bound + 2} {
			if strings.Contains(body, fmt.Sprintf(`href="?page=%d"`, above)) {
				t.Errorf("?page=%s: body links page %d, above the clamp bound %d", page, above, bound)
			}
		}
		if !strings.Contains(body, fmt.Sprintf("<strong>%d</strong>", bound)) {
			t.Errorf("?page=%s: body does not say the listing is limited to %d pages: %.1500s", page, bound, body)
		}
	}

	// Page 1 keeps a usable Last link, pointing at the bound rather than at page 5000.
	rec := w0Do(t, w0Req{Path: "/groups/" + group + "?page=1"})
	if !strings.Contains(rec.Body.String(), fmt.Sprintf(`href="?page=%d"`, bound)) {
		t.Errorf("page 1 has no Last link to page %d: %.1500s", bound, rec.Body.String())
	}
}

// fuPaginationThread creates an active group with a thread root and replies replies, plus the
// thread_cache row GetCachedThreadReplies reads, and returns the group name.
func fuPaginationThread(t *testing.T, replies int) string {
	t.Helper()
	group := w0NewGroup(t, true)
	groupDB, err := w0DB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer groupDB.Return()
	now := time.Now().Add(-time.Hour)
	// The thread page loads full articles, so insert every column it scans (an overview-only
	// row leaves headers_json/body_text NULL and the article load then fails).
	insert := func(num int64, subject, msgID, refs string, sent time.Time) {
		if _, err := database.RetryableExec(groupDB.DB,
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
				"references", bytes, lines, reply_count, path, headers_json, body_text, downloaded)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 10, 1, 0, '.POSTED!not-for-mail', '', ?, 1)`,
			num, msgID, subject, "a <a@test.invalid>", sent.UTC().Format("2006-01-02 15:04:05"),
			sent.Format(time.RFC1123Z), refs, "body of "+msgID); err != nil {
			t.Fatalf("insert article %d: %v", num, err)
		}
	}
	rootMsgID := fmt.Sprintf("<fupagination-thr-%s-1@test.invalid>", group)
	insert(1, "fuPagination thread root", rootMsgID, "", now)
	children := make([]string, 0, replies)
	for i := 0; i < replies; i++ {
		num := int64(i + 2)
		insert(num, "Re: fuPagination thread root",
			fmt.Sprintf("<fupagination-thr-%s-%d@test.invalid>", group, num),
			rootMsgID, now.Add(time.Duration(i+1)*time.Minute))
		children = append(children, fmt.Sprintf("%d", num))
	}
	ts := now.UTC().Format("2006-01-02 15:04:05")
	if _, err := database.RetryableExec(groupDB.DB,
		`INSERT INTO thread_cache (thread_root, root_date, message_count, child_articles, last_child_number, last_activity)
		 VALUES (1, ?, ?, ?, ?, ?)`,
		ts, replies+1, strings.Join(children, ","), replies+1, ts); err != nil {
		t.Fatalf("INSERT thread_cache: %v", err)
	}
	return group
}

// TestFuPaginationThreadExactPage: a thread whose reply count is an exact multiple of the page
// size must not link a further page - the root is counted in the message total but is not part
// of what the reply pagination serves, so that page came out empty (E4).
func TestFuPaginationThreadExactPage(t *testing.T) {
	group := fuPaginationThread(t, ThreadMessages_perPage)
	rec := w0Do(t, w0Req{Path: "/groups/" + group + "/thread/1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /groups/%s/thread/1 = %d, want 200", group, rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `href="?page=2"`) {
		t.Errorf("a thread with %d replies links page 2, which holds neither a reply nor the root",
			ThreadMessages_perPage)
	}
	// MessageCount keeps counting the root, so the display total is unchanged.
	if !strings.Contains(body, fmt.Sprintf("%d total messages", ThreadMessages_perPage+1)) {
		t.Errorf("thread page does not show %d total messages: %.1500s", ThreadMessages_perPage+1, body)
	}
	// Page 2 is not linked, and asking for it anyway must not render an empty page of its own:
	// the handler clamps it back to the only page there is.
	rec2 := w0Do(t, w0Req{Path: "/groups/" + group + "/thread/1?page=2"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET ?page=2 = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "fuPagination thread root") {
		t.Errorf("?page=2 of a %d-reply thread rendered without the root article: %.1500s",
			ThreadMessages_perPage, rec2.Body.String())
	}
}

// TestFuPaginationThreadOneOverPage: one reply past the page size still gets a second page,
// and that page is not empty.
func TestFuPaginationThreadOneOverPage(t *testing.T) {
	group := fuPaginationThread(t, ThreadMessages_perPage+1)
	rec := w0Do(t, w0Req{Path: "/groups/" + group + "/thread/1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /groups/%s/thread/1 = %d, want 200", group, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `href="?page=2"`) {
		t.Fatalf("a thread with %d replies does not link page 2: %.1500s",
			ThreadMessages_perPage+1, rec.Body.String())
	}
	rec2 := w0Do(t, w0Req{Path: "/groups/" + group + "/thread/1?page=2"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET ?page=2 = %d, want 200", rec2.Code)
	}
	// The last reply (article_num = replies+1) is the only message on page 2.
	if !strings.Contains(rec2.Body.String(), fmt.Sprintf("fupagination-thr-%s-%d@test.invalid",
		group, ThreadMessages_perPage+2)) {
		t.Errorf("page 2 does not hold the last reply: %.2000s", rec2.Body.String())
	}
}
