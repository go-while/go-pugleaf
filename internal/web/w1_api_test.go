package web

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// w1APIEnable turns the API on for the test and restores the previous value afterwards.
func w1APIEnable(t *testing.T) {
	t.Helper()
	db := w0DB(t)
	old, err := db.GetConfigValue(config.CFG_KEY_API_ENABLED)
	if err != nil {
		old = "false"
	}
	if err := db.SetConfigValue(config.CFG_KEY_API_ENABLED, "true"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_API_ENABLED, old); err != nil {
			t.Errorf("restore %s: %v", config.CFG_KEY_API_ENABLED, err)
		}
	})
}

// w1APINoGroupFile fails when any group DB file (incl. -wal/-shm) exists for name.
func w1APINoGroupFile(t *testing.T, name string) {
	t.Helper()
	pattern := filepath.Join(w0DataDir, "db", "*", database.SanitizeGroupName(name)+".db*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(matches) > 0 {
		t.Fatalf("group DB files created for %q: %v", name, matches)
	}
}

func TestW1APITokens(t *testing.T) {
	w1APIEnable(t)
	db := w0DB(t)

	past := time.Now().Add(-time.Hour)
	_, expired, err := db.CreateAPIToken("w1api-expired", 0, &past)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	rec := w0Do(t, w0Req{Path: "/api/v1/groups", Header: map[string]string{APIAuthHeader: expired}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: status %d, want 401; body %s", rec.Code, rec.Body.String())
	}

	_, valid, err := db.CreateAPIToken("w1api-valid", 0, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	rec = w0Do(t, w0Req{Path: "/api/v1/groups", Header: map[string]string{APIAuthHeader: valid}})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	rec = w0Do(t, w0Req{Path: "/api/v1/groups", Header: map[string]string{APIAuthHeader: "no-such-token"}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: status %d, want 401", rec.Code)
	}
}

func TestW1APIThreadsPaged(t *testing.T) {
	w1APIEnable(t)
	db := w0DB(t)
	_, token, err := db.CreateAPIToken("w1api-threads", 0, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	group := w0NewGroup(t, true)
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := database.RetryableExec(groupDB.DB,
			"INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (1, NULL, ?, 0, ?)", i, i); err != nil {
			groupDB.Return()
			t.Fatalf("insert thread: %v", err)
		}
	}
	groupDB.Return()

	hdr := map[string]string{APIAuthHeader: token}
	rec := w0Do(t, w0Req{Path: "/api/v1/groups/" + group + "/threads?limit=2", Header: hdr})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Next-Offset"); got != "2" {
		t.Fatalf("X-Next-Offset = %q, want 2", got)
	}
	if n := strings.Count(rec.Body.String(), `"child_article"`); n != 2 {
		t.Fatalf("threads in body = %d, want 2: %s", n, rec.Body.String())
	}
	rec = w0Do(t, w0Req{Path: "/api/v1/groups/" + group + "/threads?limit=2&offset=2", Header: hdr})
	if rec.Code != http.StatusOK || rec.Header().Get("X-Next-Offset") != "" {
		t.Fatalf("last page: status %d X-Next-Offset %q", rec.Code, rec.Header().Get("X-Next-Offset"))
	}
	if n := strings.Count(rec.Body.String(), `"child_article"`); n != 1 {
		t.Fatalf("threads on last page = %d, want 1", n)
	}
}

func TestW1APIThreadTreeUnknownGroup(t *testing.T) {
	name := w0Name("w1api.unknown")
	rec := w0Do(t, w0Req{Path: "/api/thread-tree?group=" + url.QueryEscape(name) + "&thread_root=1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", rec.Code, rec.Body.String())
	}
	w1APINoGroupFile(t, name)
}

func TestW1APIThreadTreeInactiveGroup(t *testing.T) {
	group := w0NewGroup(t, false)
	path := "/api/thread-tree?group=" + url.QueryEscape(group) + "&thread_root=1"
	if rec := w0Do(t, w0Req{Path: path}); rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous: status %d, want 404", rec.Code)
	}
	_, cookie := w0NewUser(t, false)
	if rec := w0Do(t, w0Req{Path: path, Cookies: []*http.Cookie{cookie}}); rec.Code != http.StatusNotFound {
		t.Fatalf("non-admin: status %d, want 404", rec.Code)
	}
	w1APINoGroupFile(t, group)
}

func TestW1APISectionTreeGroupNotInSection(t *testing.T) {
	db := w0DB(t)
	sectionName := fmt.Sprintf("w1apisec%d", w0Seq.Add(1))
	section := &models.Section{Name: sectionName, DisplayName: "W1API", CreatedAt: time.Now()}
	if err := db.CreateSection(section); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	// The router only serves known sections (web cache); the handler also checks the DB cache.
	w0Srv.loadSectionsCache()
	db.SectionsCache.AddGroupToSectionsCache(sectionName)

	unknown := w0Name("w1api.secfake")
	rec := w0Do(t, w0Req{Path: "/" + sectionName + "/" + unknown + "/tree/1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown group: status %d, want 404", rec.Code)
	}
	w1APINoGroupFile(t, unknown)

	// A real group that belongs to another section.
	other := &models.Section{Name: fmt.Sprintf("w1apisec%d", w0Seq.Add(1)), DisplayName: "Other", CreatedAt: time.Now()}
	if err := db.CreateSection(other); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	group := w0NewGroup(t, true)
	if err := db.CreateSectionGroup(&models.SectionGroup{SectionID: other.ID, NewsgroupName: group, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateSectionGroup: %v", err)
	}
	rec = w0Do(t, w0Req{Path: "/" + sectionName + "/" + group + "/tree/1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("group of another section: status %d, want 404", rec.Code)
	}
	w1APINoGroupFile(t, group)
}

func TestW1APISitePostPrefillUnknownGroup(t *testing.T) {
	_, cookie := w0NewUser(t, false)
	name := w0Name("w1api.prefill")
	rec := w0Do(t, w0Req{
		Method:  http.MethodPost,
		Path:    "/SitePost",
		Form:    url.Values{"newsgroup": {name}, "reply_to": {"1"}, "message_id": {"<a@b>"}},
		Cookies: []*http.Cookie{cookie},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	w1APINoGroupFile(t, name)
}

func TestW1APISitePostSubmitHeaderValidation(t *testing.T) {
	_, cookie := w0NewUser(t, false)
	group := w0NewGroup(t, true)
	cases := []struct {
		form url.Values
		want string
	}{
		{url.Values{"newsgroups": {group}, "subject": {"Hi\r\nApproved: yes"}, "body": {"hello"}}, "Subject contains invalid characters"},
		{url.Values{"newsgroups": {group}, "subject": {"Re: x"}, "body": {"hello"}, "reply_to": {"1"},
			"message_id": {"<a@b>\r\nControl: cancel <x@y>"}}, "Invalid reply message-id"},
	}
	for _, tc := range cases {
		rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/SitePostSubmit", Form: tc.form, Cookies: []*http.Cookie{cookie}})
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("form %v: body lacks %q (status %d)", tc.form, tc.want, rec.Code)
		}
	}
}

func TestW1APISpamInactiveGroupNonAdmin(t *testing.T) {
	_, cookie := w0NewUser(t, false)
	group := w0NewGroup(t, false)
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/groups/" + group + "/articles/1/spam", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/groups" {
		t.Fatalf("status %d location %q, want 303 /groups", rec.Code, rec.Header().Get("Location"))
	}
	w1APINoGroupFile(t, group)
}

func TestW1APIValidatePostHeaders(t *testing.T) {
	long := "<" + strings.Repeat("a", 250) + "@b>"
	cases := []struct {
		subject, msgID string
		isReply        bool
		want           string // "" = ok
	}{
		{"Hello", "", false, ""},
		{"Hello", "garbage", false, ""}, // message-id only checked for replies
		{"Re: Hello", "<abc$1@example.org>", true, ""},
		{"Hi\r\nApproved: yes", "", false, "Subject contains invalid characters"},
		{"Hi\nX", "", false, "Subject contains invalid characters"},
		{"Hi\x00", "", false, "Subject contains invalid characters"},
		{"Re: x", "<a@b>\r\nControl: cancel <x@y>", true, "Invalid reply message-id"},
		{"Re: x", "a@b", true, "Invalid reply message-id"},
		{"Re: x", "<a b@c>", true, "Invalid reply message-id"},
		{"Re: x", "<a@b@c>", true, "Invalid reply message-id"},
		{"Re: x", "<ab>", true, "Invalid reply message-id"},
		{"Re: x", "<a@b><c@d>", true, "Invalid reply message-id"},
		{"Re: x", long, true, "Invalid reply message-id"},
	}
	for _, tc := range cases {
		err := validatePostHeaders(tc.subject, tc.msgID, tc.isReply)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("validatePostHeaders(%q, %q, %v) = %q, want %q", tc.subject, tc.msgID, tc.isReply, got, tc.want)
		}
	}
}

func TestW1APISanitizePostDisplayName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Alice", "Alice"},
		{"  Alice   Smith ", "Alice Smith"},
		{"Evil\r\nControl: cancel <x@y>", "Evil Control: cancel x@y"},
		{"<admin@example.org>", "admin@example.org"},
		{`"Quoted"`, "Quoted"},
		{"a\x00b\x7fc", "abc"},
		{"<>\"", ""},
		{"Jörg Übel", "Jörg Übel"},
		{strings.Repeat("ä", 70), strings.Repeat("ä", 64)},
		{"\t", ""},
	}
	for _, tc := range cases {
		if got := sanitizePostDisplayName(tc.in); got != tc.want {
			t.Errorf("sanitizePostDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestW1APIThreadTreeHTMLEscapesGroup(t *testing.T) {
	tree := &database.ThreadTree{
		ThreadRoot: 1,
		TotalNodes: 2,
		MaxDepth:   1,
		LeafCount:  1,
		RootNode: &database.TreeNode{ArticleNum: 1, Children: []*database.TreeNode{
			{ArticleNum: 2, Depth: 1},
		}},
	}
	out := string(tree.GetThreadTreeHTML(`a"b<c`))
	if strings.Contains(out, `a"b<c`) {
		t.Fatalf("raw group name in tree HTML:\n%s", out)
	}
	if !strings.Contains(out, `data-group-name="a&#34;b&lt;c"`) {
		t.Fatalf("escaped data-group-name missing:\n%s", out)
	}
	if !strings.Contains(out, `data-group="a&#34;b&lt;c"`) {
		t.Fatalf("escaped data-group missing:\n%s", out)
	}
	if !strings.Contains(out, `href="/groups/a%22b%3Cc/articles/2"`) {
		t.Fatalf("escaped href missing:\n%s", out)
	}
}

func TestW1APIClampOffsetPage(t *testing.T) {
	cases := []struct{ page, size, max, want int }{
		{1, 128, 12800, 1},
		{0, 128, 12800, 1},
		{-5, 128, 12800, 1},
		{101, 128, 12800, 101},
		{102, 128, 12800, 101},
		{1 << 40, 128, 12800, 101},
		{7, 0, 12800, 7},
	}
	for _, tc := range cases {
		if got := clampOffsetPage(tc.page, tc.size, tc.max); got != tc.want {
			t.Errorf("clampOffsetPage(%d, %d, %d) = %d, want %d", tc.page, tc.size, tc.max, got, tc.want)
		}
	}
}

func TestW1APITruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 3, "hel..."},
		{"äöüß", 2, "äö..."},
		{"", 3, ""},
		{"abc", 0, "..."},
	}
	for _, tc := range cases {
		if got := truncateRunes(tc.in, tc.n); got != tc.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
