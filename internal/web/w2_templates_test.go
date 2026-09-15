package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

var w2TmplTitleRe = regexp.MustCompile(`(?s)<title>(.*?)</title>`)

// w2TmplTitle returns the content of the first <title> element of body.
func w2TmplTitle(t *testing.T, body string) string {
	t.Helper()
	m := w2TmplTitleRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no <title> in body: %.300q", body)
	}
	return m[1]
}

func TestW2TmplLoadTemplatesCached(t *testing.T) {
	files := []string{templateDir + "base.html", templateDir + "help.html"}
	a, err := loadTemplates("w2tmpl-cache", nil, files...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadTemplates("w2tmpl-cache", nil, files...)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("second loadTemplates returned a new set (%p != %p)", a, b)
	}

	// A FuncMap never shares the entry of the same name and files without one.
	f, err := loadTemplates("w2tmpl-cache", adminTemplateFuncs, files...)
	if err != nil {
		t.Fatal(err)
	}
	if f == a {
		t.Fatal("set parsed with a FuncMap shares the cache entry of the set without one")
	}

	t.Setenv("PUGLEAF_DEV_TEMPLATES", "1")
	c, err := loadTemplates("w2tmpl-cache", nil, files...)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("PUGLEAF_DEV_TEMPLATES=1 returned the cached set")
	}
	d, err := loadTemplates("w2tmpl-cache", nil, files...)
	if err != nil {
		t.Fatal(err)
	}
	if d == c || d == a {
		t.Fatal("PUGLEAF_DEV_TEMPLATES=1 did not re-parse on every call")
	}
}

func TestW2TmplMissingTemplate(t *testing.T) {
	for i := 0; i < 2; i++ { // the error must not be cached either
		tmpl, err := loadTemplates("w2tmpl-missing", nil, templateDir+"base.html", templateDir+"w2tmpl-does-not-exist.html")
		if err == nil || tmpl != nil {
			t.Fatalf("missing template: got tmpl=%v err=%v, want an error", tmpl, err)
		}
	}
	if _, err := loadTemplates("w2tmpl-nofiles", nil); err == nil {
		t.Fatal("loadTemplates without files returned no error")
	}

	// writeTemplateSet writes nothing on error.
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	if err := writeTemplateSet(c, http.StatusOK, "page", nil, "base.html", nil, "base.html", "w2tmpl-does-not-exist.html"); err == nil {
		t.Fatal("writeTemplateSet with a missing file returned no error")
	}
	if c.Writer.Written() || rec.Body.Len() != 0 {
		t.Fatalf("writeTemplateSet wrote a response on error: %q", rec.Body.String())
	}

	// renderPage answers a missing template with the 500 error page, not a panic.
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	w0Srv.renderPage(c, http.StatusOK, w0Srv.getBaseTemplateData(c, "x"), "w2tmpl-does-not-exist.html")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("renderPage with a missing template: status %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Template error") {
		t.Fatalf("renderPage with a missing template: body lacks the error page: %.300q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != htmlContentType {
		t.Fatalf("Content-Type %q, want %q", ct, htmlContentType)
	}
}

func TestW2TmplPagesRender(t *testing.T) {
	for _, path := range []string{"/login", "/register", "/groups", "/search?q=a"} {
		rec := w0Do(t, w0Req{Path: path})
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != htmlContentType {
			t.Errorf("GET %s: Content-Type %q, want %q", path, ct, htmlContentType)
		}
		if title := w2TmplTitle(t, rec.Body.String()); !strings.Contains(title, "go-pugleaf") {
			t.Errorf("GET %s: <title> %q lacks go-pugleaf", path, title)
		}
	}
}

func TestW2TmplLoginErrorStatus(t *testing.T) {
	// A failed login renders the login page with 400 (renderLoginError).
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/login", Form: url.Values{
		"username": {w0Name("w2tmplnouser")}, "password": {"wrong-password-0123456789"},
	}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("failed login: status %d, want 400", rec.Code)
	}
	if !strings.Contains(w2TmplTitle(t, rec.Body.String()), "Login") {
		t.Fatalf("failed login did not render the login page: %.300q", rec.Body.String())
	}
}

func TestW2TmplSearchTitleEscaped(t *testing.T) {
	q := url.Values{"q": {"<script>alert(1)</script>"}, "searchType": {"groups"}}
	rec := w0Do(t, w0Req{Path: "/search?" + q.Encode()})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	title := w2TmplTitle(t, rec.Body.String())
	if strings.Contains(title, "<script") {
		t.Fatalf("<title> contains an unescaped tag: %q", title)
	}
	if !strings.Contains(title, "&lt;script&gt;") {
		t.Fatalf("<title> %q does not contain the escaped query", title)
	}
}

func TestW2TmplConcurrentRender(t *testing.T) {
	paths := []string{"/", "/groups", "/login", "/SiteHelp", "/SiteNews"}
	var wg sync.WaitGroup
	errs := make(chan string, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			rec := w0Do(t, w0Req{Path: path})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "</html>") {
				errs <- path
			}
		}(paths[i%len(paths)])
	}
	wg.Wait()
	close(errs)
	for p := range errs {
		t.Errorf("GET %s: not a complete 200 page", p)
	}
}
