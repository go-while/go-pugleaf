package web

// Tests of the net/http adapter (httpctx.go, httprouter.go, httpclientip.go). No database use;
// run them alone with: go test -run Httpx ./internal/web/

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// httpxLogBuf collects log output; other tests of the package may log concurrently.
type httpxLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *httpxLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *httpxLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func httpxCaptureLog(t *testing.T) *httpxLogBuf {
	t.Helper()
	b := &httpxLogBuf{}
	old := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(old) })
	return b
}

// httpxServe sends one request through h and returns the recorder.
func httpxServe(h http.Handler, method, target string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, m := range mod {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// httpxPattern is a handler that answers with the matched pattern.
func httpxPattern(c *Context) { c.String(http.StatusOK, "%s", c.Request.Pattern) }

func httpxTestCtx(method, target string) (*Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	return NewTestContext(rec, httptest.NewRequest(method, target, nil)), rec
}

func TestHttpxString(t *testing.T) {
	c, rec := httpxTestCtx(http.MethodGet, "/")
	// Through a func value: vet checks String's format like Printf, but without values the
	// format is written raw, so a lone "%" must stay "%".
	str := c.String
	str(http.StatusOK, "100% raw")
	if got := rec.Body.String(); got != "100% raw" {
		t.Errorf("raw String body = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("String Content-Type = %q", got)
	}

	c, rec = httpxTestCtx(http.MethodGet, "/")
	c.String(http.StatusCreated, "%d-%s", 7, "x")
	if rec.Code != http.StatusCreated || rec.Body.String() != "7-x" {
		t.Errorf("formatted String = %d %q, want 201 \"7-x\"", rec.Code, rec.Body.String())
	}

	// A Content-Type set before wins.
	c, rec = httpxTestCtx(http.MethodGet, "/")
	c.Header("Content-Type", "text/csv")
	c.String(http.StatusOK, "a,b")
	if got := rec.Header().Get("Content-Type"); got != "text/csv" {
		t.Errorf("preset Content-Type = %q, want text/csv", got)
	}

	// No body for 204.
	c, rec = httpxTestCtx(http.MethodGet, "/")
	c.String(http.StatusNoContent, "dropped")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("204 String = %d %q", rec.Code, rec.Body.String())
	}
}

func TestHttpxJSON(t *testing.T) {
	c, rec := httpxTestCtx(http.MethodGet, "/")
	c.JSON(http.StatusAccepted, H{"b": "<x>&", "a": 1, "c": []H{{"z": true}}})
	if want := `{"a":1,"b":"\u003cx\u003e\u0026","c":[{"z":true}]}`; rec.Body.String() != want {
		t.Errorf("JSON body = %s, want %s", rec.Body.String(), want)
	}
	if rec.Code != http.StatusAccepted {
		t.Errorf("JSON status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("JSON Content-Type = %q", got)
	}

	// An unencodable value: status and type, no body, chain aborted.
	httpxCaptureLog(t)
	c, rec = httpxTestCtx(http.MethodGet, "/")
	c.JSON(http.StatusOK, H{"f": func() {}})
	c.Writer.WriteHeaderNow()
	if rec.Body.Len() != 0 || !c.IsAborted() {
		t.Errorf("unencodable JSON: body %q aborted %v", rec.Body.String(), c.IsAborted())
	}
}

func TestHttpxDataStatusFile(t *testing.T) {
	c, rec := httpxTestCtx(http.MethodGet, "/")
	c.Data(http.StatusAccepted, "text/html; charset=utf-8", []byte("<p>"))
	if rec.Code != http.StatusAccepted || rec.Body.String() != "<p>" || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("Data = %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	if c.Writer.Size() != 3 || !c.Writer.Written() || c.Writer.Status() != http.StatusAccepted {
		t.Errorf("writer size %d written %v status %d", c.Writer.Size(), c.Writer.Written(), c.Writer.Status())
	}

	// Status is recorded; the header goes out with WriteHeaderNow (or at the end of a routed request).
	c, rec = httpxTestCtx(http.MethodGet, "/")
	c.Status(http.StatusNotFound)
	if c.Writer.Written() || c.Writer.Status() != http.StatusNotFound || c.Writer.Size() != -1 {
		t.Errorf("after Status: written %v status %d size %d", c.Writer.Written(), c.Writer.Status(), c.Writer.Size())
	}
	c.Writer.WriteHeaderNow()
	if rec.Code != http.StatusNotFound {
		t.Errorf("Status then WriteHeaderNow = %d, want 404", rec.Code)
	}
	c.Status(http.StatusOK) // ignored after the header went out
	if c.Writer.Status() != http.StatusNotFound {
		t.Errorf("Status after write changed the status to %d", c.Writer.Status())
	}

	r := NewRouter()
	r.GET("/s", func(c *Context) { c.Status(http.StatusNotFound) })
	if rec := httpxServe(r, http.MethodGet, "/s"); rec.Code != http.StatusNotFound {
		t.Errorf("routed Status(404) = %d", rec.Code)
	}

	// File, favicon style: the Content-Type set before is kept.
	dir := t.TempDir()
	ico := filepath.Join(dir, "favicon.ico")
	if err := os.WriteFile(ico, []byte("ICO"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, rec = httpxTestCtx(http.MethodGet, "/favicon.ico")
	c.Header("Content-Type", "image/x-icon")
	c.File(ico)
	if rec.Code != http.StatusOK || rec.Body.String() != "ICO" || rec.Header().Get("Content-Type") != "image/x-icon" {
		t.Errorf("File = %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	c, rec = httpxTestCtx(http.MethodGet, "/x")
	c.File(filepath.Join(dir, "missing"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing File = %d", rec.Code)
	}

	// AbortWithStatus writes the header at once.
	c, rec = httpxTestCtx(http.MethodGet, "/")
	c.AbortWithStatus(http.StatusUnauthorized)
	if rec.Code != http.StatusUnauthorized || !c.Writer.Written() || !c.IsAborted() {
		t.Errorf("AbortWithStatus = %d written %v aborted %v", rec.Code, c.Writer.Written(), c.IsAborted())
	}
}

func TestHttpxWriterUnwrapFlush(t *testing.T) {
	c, rec := httpxTestCtx(http.MethodGet, "/")
	if c.Writer.Unwrap() != http.ResponseWriter(rec) {
		t.Error("Unwrap does not return the recorder")
	}
	c.Status(http.StatusAccepted)
	if err := http.NewResponseController(c.Writer).Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if !rec.Flushed || rec.Code != http.StatusAccepted {
		t.Errorf("Flush: flushed %v code %d", rec.Flushed, rec.Code)
	}
}

func TestHttpxRedirect(t *testing.T) {
	for _, code := range []int{http.StatusSeeOther, http.StatusFound, http.StatusMovedPermanently} {
		c, rec := httpxTestCtx(http.MethodPost, "/a")
		c.Redirect(code, "/target?x=1")
		if rec.Code != code || rec.Header().Get("Location") != "/target?x=1" {
			t.Errorf("Redirect(%d) = %d %q", code, rec.Code, rec.Header().Get("Location"))
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("Redirect(200) did not panic")
		}
	}()
	c, _ := httpxTestCtx(http.MethodGet, "/")
	c.Redirect(http.StatusOK, "/x")
}

func TestHttpxHeaderCookieParam(t *testing.T) {
	c, rec := httpxTestCtx(http.MethodGet, "/")
	c.Request.Header.Set("X-In", "v")
	if c.GetHeader("x-in") != "v" {
		t.Error("GetHeader")
	}
	c.Header("X-Out", "1")
	c.Header("X-Out", "")
	if _, ok := rec.Header()["X-Out"]; ok {
		t.Error("Header with empty value did not delete")
	}

	c.Request.AddCookie(&http.Cookie{Name: "session_id", Value: "a%20b+c"})
	if v, err := c.Cookie("session_id"); err != nil || v != "a b c" {
		t.Errorf("Cookie = %q, %v; want \"a b c\"", v, err)
	}
	if _, err := c.Cookie("none"); !errors.Is(err, http.ErrNoCookie) {
		t.Errorf("missing Cookie err = %v", err)
	}

	c.Request.SetPathValue("id", "7")
	if c.Param("id") != "7" || c.Param("nope") != "" {
		t.Errorf("Param = %q %q", c.Param("id"), c.Param("nope"))
	}
}

func TestHttpxQuery(t *testing.T) {
	c, _ := httpxTestCtx(http.MethodGet, "/?a=1&a=2&e=&sp=a+b")
	if c.Query("a") != "1" || c.Query("missing") != "" || c.Query("sp") != "a b" {
		t.Errorf("Query: a=%q missing=%q sp=%q", c.Query("a"), c.Query("missing"), c.Query("sp"))
	}
	if c.DefaultQuery("e", "d") != "" || c.DefaultQuery("m", "d") != "d" || c.DefaultQuery("a", "d") != "1" {
		t.Errorf("DefaultQuery: e=%q m=%q a=%q", c.DefaultQuery("e", "d"), c.DefaultQuery("m", "d"), c.DefaultQuery("a", "d"))
	}
}

func httpxCheckForm(t *testing.T, kind string, c *Context) {
	t.Helper()
	if c.PostForm("a") != "1" || !reflect.DeepEqual(c.PostFormArray("a"), []string{"1", "2"}) {
		t.Errorf("%s: PostForm a=%q array=%q", kind, c.PostForm("a"), c.PostFormArray("a"))
	}
	if c.PostForm("q") != "" {
		t.Errorf("%s: query value q=%q merged into the post form", kind, c.PostForm("q"))
	}
	if v, ok := c.GetPostForm("e"); v != "" || !ok {
		t.Errorf("%s: GetPostForm(e) = %q, %v; want \"\", true", kind, v, ok)
	}
	if v, ok := c.GetPostForm("missing"); v != "" || ok {
		t.Errorf("%s: GetPostForm(missing) = %q, %v", kind, v, ok)
	}
	if v, ok := c.GetPostFormArray("missing"); v != nil || ok {
		t.Errorf("%s: GetPostFormArray(missing) = %q, %v", kind, v, ok)
	}
	if v, ok := c.GetPostFormArray("a"); len(v) != 2 || !ok {
		t.Errorf("%s: GetPostFormArray(a) = %q, %v", kind, v, ok)
	}
}

func TestHttpxPostForm(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/f?q=fromquery&a=query", strings.NewReader("a=1&a=2&e="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpxCheckForm(t, "urlencoded", NewTestContext(httptest.NewRecorder(), req))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, kv := range [][2]string{{"a", "1"}, {"a", "2"}, {"e", ""}} {
		if err := mw.WriteField(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/f?q=fromquery&a=query", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	httpxCheckForm(t, "multipart", NewTestContext(httptest.NewRecorder(), req))

	// A GET has no post form, whatever the query holds.
	c, _ := httpxTestCtx(http.MethodGet, "/f?a=1")
	if v, ok := c.GetPostForm("a"); ok || v != "" {
		t.Errorf("GET post form a = %q, %v", v, ok)
	}
}

func TestHttpxShouldBindJSON(t *testing.T) {
	var v struct {
		Message string `json:"message"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"message":"hi"}`))
	c := NewTestContext(httptest.NewRecorder(), req)
	if err := c.ShouldBindJSON(&v); err != nil || v.Message != "hi" {
		t.Errorf("ShouldBindJSON = %v, %q", err, v.Message)
	}

	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"message":"`+strings.Repeat("x", 100)+`"}`))
	c = NewTestContext(httptest.NewRecorder(), req)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16)
	err := c.ShouldBindJSON(&v)
	var mbe *http.MaxBytesError
	if !errors.As(err, &mbe) {
		t.Errorf("over-limit body: err = %v (%T), want *http.MaxBytesError", err, err)
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = nil
	if err := NewTestContext(httptest.NewRecorder(), req).ShouldBindJSON(&v); err == nil {
		t.Error("nil body: no error")
	}
}

func TestHttpxKeys(t *testing.T) {
	c, _ := httpxTestCtx(http.MethodGet, "/")
	if _, ok := c.Get("k"); ok {
		t.Error("Get on an empty context found a key")
	}
	c.Set("k", 42)
	if v, ok := c.Get("k"); !ok || v != 42 {
		t.Errorf("Get = %v, %v", v, ok)
	}
	if c.MustGet("k") != 42 {
		t.Error("MustGet")
	}
	defer func() {
		if recover() == nil {
			t.Error("MustGet of a missing key did not panic")
		}
	}()
	c.MustGet("missing")
}

func TestHttpxTestContextIPAndFullPath(t *testing.T) {
	c, _ := httpxTestCtx(http.MethodGet, "/t")
	c.Request.RemoteAddr = "192.0.2.10:1234"
	c.Request.Header.Set("X-Forwarded-For", "198.51.100.1")
	if c.RemoteIP() != "192.0.2.10" || c.ClientIP() != c.RemoteIP() {
		t.Errorf("RemoteIP %q ClientIP %q", c.RemoteIP(), c.ClientIP())
	}
	if c.FullPath() != "" {
		t.Errorf("unmatched FullPath = %q", c.FullPath())
	}
	c.Request.Pattern = "GET /t/{id}"
	if c.FullPath() != "/t/{id}" {
		t.Errorf("FullPath = %q", c.FullPath())
	}
}

func TestHttpxChainOrderAndAbort(t *testing.T) {
	var trace []string
	mark := func(name string) HandlerFunc {
		return func(c *Context) {
			trace = append(trace, name+">")
			c.Next()
			trace = append(trace, "<"+name)
		}
	}
	r := NewRouter()
	r.Use(mark("g1"), mark("g2"))
	g := r.Group("/grp", mark("m1"))
	g.Use(mark("m2"))
	g.GET("/x", func(c *Context) { trace = append(trace, "h"); c.String(http.StatusOK, "ok") })
	g.GET("/stop", func(c *Context) {
		trace = append(trace, "h1")
		c.Abort()
		c.Next() // no-op after Abort
		trace = append(trace, "h1-after")
	}, func(c *Context) { trace = append(trace, "h2") })

	rec := httpxServe(r, http.MethodGet, "/grp/x")
	if want := []string{"g1>", "g2>", "m1>", "m2>", "h", "<m2", "<m1", "<g2", "<g1"}; !reflect.DeepEqual(trace, want) || rec.Body.String() != "ok" {
		t.Errorf("chain = %v body %q, want %v", trace, rec.Body.String(), want)
	}

	trace = nil
	httpxServe(r, http.MethodGet, "/grp/stop")
	if want := []string{"g1>", "g2>", "m1>", "m2>", "h1", "h1-after", "<m2", "<m1", "<g2", "<g1"}; !reflect.DeepEqual(trace, want) {
		t.Errorf("route chain abort = %v, want %v", trace, want)
	}

	// Abort in a route middleware: the handler does not run, the global chain sees the abort.
	var sawAbort bool
	r2 := NewRouter()
	r2.Use(func(c *Context) { c.Next(); sawAbort = c.IsAborted() })
	r2.Group("/a", func(c *Context) { c.String(http.StatusForbidden, "no"); c.Abort() }).
		GET("/b", func(c *Context) { t.Error("handler ran after a route middleware aborted") })
	rec = httpxServe(r2, http.MethodGet, "/a/b")
	if rec.Code != http.StatusForbidden || !sawAbort {
		t.Errorf("route middleware abort: %d, global saw abort %v", rec.Code, sawAbort)
	}

	// Abort in the global chain: later middleware and the dispatch do not run.
	r3 := NewRouter()
	r3.Use(func(c *Context) { c.String(http.StatusForbidden, "403"); c.Abort() },
		func(c *Context) { t.Error("global middleware ran after Abort") })
	r3.GET("/x", func(c *Context) { t.Error("route ran after a global Abort") })
	if rec := httpxServe(r3, http.MethodGet, "/x"); rec.Code != http.StatusForbidden || rec.Body.String() != "403" {
		t.Errorf("global abort = %d %q", rec.Code, rec.Body.String())
	}
	// Abort also stops the miss handling (no 404 body after the 403).
	if rec := httpxServe(r3, http.MethodGet, "/nope"); rec.Code != http.StatusForbidden || rec.Body.String() != "403" {
		t.Errorf("global abort on a miss = %d %q", rec.Code, rec.Body.String())
	}
}

func TestHttpxGroupPrefix(t *testing.T) {
	r := NewRouter()
	var mw int
	api := r.Group("/api/v1", func(c *Context) { mw++; c.Next() })
	api.GET("/groups", httpxPattern)
	api.GET("/groups/{$}", httpxPattern)
	r.Group("/", func(c *Context) { c.Next() }).GET("/root", httpxPattern)
	r.GET("/plain", httpxPattern)
	for target, want := range map[string]string{
		"/api/v1/groups":  "GET /api/v1/groups",
		"/api/v1/groups/": "GET /api/v1/groups/{$}",
		"/root":           "GET /root",
		"/plain":          "GET /plain",
	} {
		if rec := httpxServe(r, http.MethodGet, target); rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", target, rec.Code, rec.Body.String(), want)
		}
	}
	if mw != 2 {
		t.Errorf("group middleware ran %d times, want 2", mw)
	}
}

func TestHttpxFallback(t *testing.T) {
	r := NewRouter()
	var fbMW []string
	r.GET("/groups", httpxPattern)
	fb := r.Fallback(func(c *Context) { fbMW = append(fbMW, c.Request.URL.Path); c.Next() })
	fb.GET("/{section}", httpxPattern)
	fb.GET("/{section}/{group}/{$}", httpxPattern)

	if rec := httpxServe(r, http.MethodGet, "/groups"); rec.Body.String() != "GET /groups" {
		t.Errorf("/groups = %q", rec.Body.String())
	}
	if rec := httpxServe(r, http.MethodGet, "/rocksolid"); rec.Body.String() != "GET /{section}" {
		t.Errorf("/rocksolid = %q", rec.Body.String())
	}
	if want := []string{"/rocksolid"}; !reflect.DeepEqual(fbMW, want) {
		t.Errorf("fallback middleware ran for %v, want %v", fbMW, want)
	}
	// The fallback mux's trailing-slash case is answered like gin: 301 for GET.
	rec := httpxServe(r, http.MethodGet, "/rocksolid/a.b?x=1")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/rocksolid/a.b/?x=1" {
		t.Errorf("fallback TSR = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestHttpxSlashRemovalBeforeFallback: a path with a trailing slash whose slash-less form has a
// main route is redirected there, not taken by the fallback's /{section}/{$}; other paths with a
// slash still reach the fallback.
func TestHttpxSlashRemovalBeforeFallback(t *testing.T) {
	r := NewRouter()
	r.GET("/login", httpxPattern)
	r.POST("/login", httpxPattern)
	fb := r.Fallback()
	fb.GET("/{section}", httpxPattern)
	fb.GET("/{section}/{$}", httpxPattern)

	for _, tc := range []struct {
		method, target string
		code           int
		location       string
	}{
		{http.MethodGet, "/login/", http.StatusMovedPermanently, "/login"},
		{http.MethodGet, "/login/?redirect=%2Fx", http.StatusMovedPermanently, "/login?redirect=%2Fx"},
		{http.MethodPost, "/login/", http.StatusTemporaryRedirect, "/login"},
	} {
		rec := httpxServe(r, tc.method, tc.target)
		if rec.Code != tc.code || rec.Header().Get("Location") != tc.location {
			t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.target, rec.Code, rec.Header().Get("Location"), tc.code, tc.location)
		}
	}
	if rec := httpxServe(r, http.MethodGet, "/rocksolid/"); rec.Code != http.StatusOK || rec.Body.String() != "GET /{section}/{$}" {
		t.Errorf("GET /rocksolid/ = %d %q, want the fallback route", rec.Code, rec.Body.String())
	}
	if rec := httpxServe(r, http.MethodGet, "/login"); rec.Body.String() != "GET /login" {
		t.Errorf("GET /login = %d %q", rec.Code, rec.Body.String())
	}
}

func TestHttpxLateRegistration(t *testing.T) {
	r := NewRouter()
	r.GET("/early", httpxPattern)
	if rec := httpxServe(r, http.MethodGet, "/late"); rec.Code != http.StatusNotFound {
		t.Fatalf("before registration: %d", rec.Code)
	}
	r.GET("/late", httpxPattern)
	if rec := httpxServe(r, http.MethodGet, "/late"); rec.Code != http.StatusOK || rec.Body.String() != "GET /late" {
		t.Errorf("after registration: %d %q", rec.Code, rec.Body.String())
	}
	defer func() {
		if recover() == nil {
			t.Error("duplicate pattern did not panic")
		}
	}()
	r.GET("/late", httpxPattern)
}

func TestHttpxHeadFromGet(t *testing.T) {
	r := NewRouter()
	r.GET("/x", httpxPattern)
	r.GET("/favicon.ico", httpxPattern)
	r.HEAD("/favicon.ico", httpxPattern)
	if rec := httpxServe(r, http.MethodHead, "/x"); rec.Code != http.StatusOK || rec.Body.String() != "GET /x" {
		t.Errorf("HEAD /x = %d %q", rec.Code, rec.Body.String())
	}
	if rec := httpxServe(r, http.MethodHead, "/favicon.ico"); rec.Body.String() != "HEAD /favicon.ico" {
		t.Errorf("HEAD /favicon.ico = %q", rec.Body.String())
	}
}

func TestHttpxTrailingSlash(t *testing.T) {
	r := NewRouter()
	r.Use(SecureHeaders(SecureConfig{}))
	r.GET("/a/{$}", httpxPattern)
	r.GET("/b", httpxPattern)
	r.POST("/c", httpxPattern)
	r.GET("/e/{$}", httpxPattern)
	r.POST("/e", httpxPattern)
	r.GET("/{$}", httpxPattern)

	tests := []struct {
		method, target string
		code           int
		location       string
	}{
		{http.MethodGet, "/a?x=1&y=%2F", http.StatusMovedPermanently, "/a/?x=1&y=%2F"},
		{http.MethodGet, "/b/", http.StatusMovedPermanently, "/b"},
		{http.MethodPost, "/c/", http.StatusTemporaryRedirect, "/c"},
		{http.MethodHead, "/a", http.StatusTemporaryRedirect, "/a/"},
		// TSR is tried before 405: GET /e has a POST route, but GET /e/ exists.
		{http.MethodGet, "/e", http.StatusMovedPermanently, "/e/"},
		{http.MethodGet, "/", http.StatusOK, ""},
		// Unclean paths get the mux's 307 to the cleaned path.
		{http.MethodGet, "/x/../b", http.StatusTemporaryRedirect, "/b"},
	}
	for _, tc := range tests {
		rec := httpxServe(r, tc.method, tc.target)
		if rec.Code != tc.code || rec.Header().Get("Location") != tc.location {
			t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.target, rec.Code, rec.Header().Get("Location"), tc.code, tc.location)
		}
		if rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
			t.Errorf("%s %s: no CSP header", tc.method, tc.target)
		}
	}

	// The root is never redirected.
	r2 := NewRouter()
	r2.GET("/x", httpxPattern)
	if rec := httpxServe(r2, http.MethodGet, "/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET / without a root route = %d, want 404", rec.Code)
	}
	r2.RedirectTrailingSlash = false
	if rec := httpxServe(r2, http.MethodGet, "/x/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /x/ with RedirectTrailingSlash off = %d, want 404", rec.Code)
	}
}

func TestHttpxMethodNotAllowedAndNotFound(t *testing.T) {
	var global int
	r := NewRouter()
	r.Use(func(c *Context) { global++; c.Next() }, SecureHeaders(SecureConfig{}))
	r.GET("/g", httpxPattern)
	r.POST("/p", httpxPattern)
	r.Fallback().GET("/{section}/{group}/articles/{n}", httpxPattern)

	tests := []struct {
		method, target, allow string
	}{
		{http.MethodPost, "/g", "GET, HEAD"},
		{http.MethodGet, "/p", "POST"},
		{http.MethodHead, "/p", "POST"},
		{http.MethodDelete, "/g", "GET, HEAD"},
		{http.MethodPost, "/s/g/articles/1", "GET, HEAD"},
	}
	for _, tc := range tests {
		rec := httpxServe(r, tc.method, tc.target)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != tc.allow {
			t.Errorf("%s %s = %d Allow %q, want 405 %q", tc.method, tc.target, rec.Code, rec.Header().Get("Allow"), tc.allow)
		}
		if tc.method != http.MethodHead && rec.Body.String() != "Method Not Allowed\n" {
			t.Errorf("%s %s body = %q", tc.method, tc.target, rec.Body.String())
		}
		if rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy || rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s %s: secure headers missing on 405", tc.method, tc.target)
		}
	}

	global = 0
	rec := httpxServe(r, http.MethodGet, "/a/b/c/d/e")
	if rec.Code != http.StatusNotFound || rec.Body.String() != "404 page not found\n" {
		t.Errorf("404 = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy || global != 1 {
		t.Errorf("404 did not run the global chain: CSP %q, middleware ran %d", rec.Header().Get("Content-Security-Policy"), global)
	}
}

func TestHttpxFullPath(t *testing.T) {
	r := NewRouter()
	var unmatched string
	r.Use(func(c *Context) { c.Next(); unmatched = c.FullPath() })
	r.POST("/aichat/clear/{model}", func(c *Context) { c.String(http.StatusOK, "%s|%v", c.FullPath(), c.FullPath() == "/aichat/clear/all") })
	r.POST("/aichat/clear/all", func(c *Context) { c.String(http.StatusOK, "%s|%v", c.FullPath(), c.FullPath() == "/aichat/clear/all") })
	if rec := httpxServe(r, http.MethodPost, "/aichat/clear/all"); rec.Body.String() != "/aichat/clear/all|true" {
		t.Errorf("clear/all = %q", rec.Body.String())
	}
	if rec := httpxServe(r, http.MethodPost, "/aichat/clear/m1"); rec.Body.String() != "/aichat/clear/{model}|false" {
		t.Errorf("clear/m1 = %q", rec.Body.String())
	}
	httpxServe(r, http.MethodGet, "/none")
	if unmatched != "" {
		t.Errorf("FullPath on a miss = %q", unmatched)
	}
}

// TestHttpxPathValues: %2F stays inside its segment, '+' stays '+', %2B becomes '+' (the
// TestAdfOptRawPathParams table); the catch-all value has no leading slash.
func TestHttpxPathValues(t *testing.T) {
	r := NewRouter()
	r.GET("/g/{group}/m/{id}", func(c *Context) { c.String(http.StatusOK, "%s|%s", c.Param("group"), c.Param("id")) })
	r.GET("/s/{filepath...}", func(c *Context) { c.String(http.StatusOK, "%s", c.Param("filepath")) })
	for target, want := range map[string]string{
		"/g/comp.lang.c++/m/%3Cab+c@x%3E":          "comp.lang.c++|<ab+c@x>",
		"/g/a.b/m/%3Cx@y%3E":                       "a.b|<x@y>",
		"/g/a.b/m/%3Ca%20b%25c%3Fd%23e@x%3E":       "a.b|<a b%c?d#e@x>",
		"/g/a_b-c/m/plain":                         "a_b-c|plain",
		"/s/js/thread-tree.js":                     "js/thread-tree.js",
		"/s/a%20b.css":                             "a b.css",
		"/g/comp.lang.c++/m/%3Ca%2Fb,c+d@x%3E":     "comp.lang.c++|<a/b,c+d@x>",
		"/g/comp.lang.c%2B%2B/m/%3Ca%2Bb%2Fc@x%3E": "comp.lang.c++|<a+b/c@x>",
		"/g/comp.lang.c++/m/<ab+c@x>":              "comp.lang.c++|<ab+c@x>",
		"/g/a.b/m/%3ca%2cb@x%3e":                   "a.b|<a,b@x>",
		"/g/a.b/m/%3Ca%2Cb+c@x%3E":                 "a.b|<a,b+c@x>",
		"/s/a%2Fb+c.css":                           "a/b+c.css",
	} {
		rec := httpxServe(r, http.MethodGet, target)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", target, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestHttpxRecovery(t *testing.T) {
	logs := httpxCaptureLog(t)
	r := NewRouter()
	r.Use(Recovery())
	r.GET("/boom", func(c *Context) { c.Status(http.StatusAccepted); panic("httpx-boom") })
	r.GET("/abort", func(c *Context) { panic(http.ErrAbortHandler) })

	rec := httpxServe(r, http.MethodGet, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panic = %d, want 500", rec.Code)
	}
	if out := logs.String(); !strings.Contains(out, "[WEB]: panic serving GET /boom: httpx-boom") || !strings.Contains(out, "goroutine") {
		t.Errorf("panic log line missing or without stack: %q", out)
	}

	func() {
		defer func() {
			if v := recover(); v != http.ErrAbortHandler {
				t.Errorf("ErrAbortHandler: recovered %v, want it re-panicked", v)
			}
		}()
		httpxServe(r, http.MethodGet, "/abort")
	}()
}

func TestHttpxAccessLog(t *testing.T) {
	logs := httpxCaptureLog(t)
	r := NewRouter()
	r.Use(Recovery(), AccessLog())
	r.GET("/httpx-acc/{x}", func(c *Context) {
		c.Request.URL.Path = "/rewritten"
		c.Request.RequestURI = "/rewritten"
		c.String(http.StatusAccepted, "hello")
	})
	httpxServe(r, http.MethodGet, "/httpx-acc/1?q=2", func(req *http.Request) { req.RemoteAddr = "192.0.2.7:5555" })
	var lines []string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "httpx-acc") || strings.Contains(l, "/rewritten") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], `[WEB]: 192.0.2.7 "GET /httpx-acc/1?q=2" 202 5B `) {
		t.Errorf("access log lines = %q", lines)
	}
}

func TestHttpxSecureHeaders(t *testing.T) {
	want := map[string]string{
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"X-Xss-Protection":        "1; mode=block",
		"Content-Security-Policy": contentSecurityPolicy,
		"Referrer-Policy":         "strict-origin-when-cross-origin",
	}
	check := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		for k, v := range want {
			if got := rec.Header().Values(k); len(got) != 1 || got[0] != v {
				t.Errorf("%s: %s = %q, want %q", name, k, got, v)
			}
		}
	}

	plain := NewRouter()
	plain.Use(SecureHeaders(SecureConfig{}))
	plain.GET("/p", httpxPattern)
	rec := httpxServe(plain, http.MethodGet, "/p")
	check("no SSL", rec)
	if rec.Code != http.StatusOK || rec.Header().Get("Strict-Transport-Security") != "" {
		t.Errorf("no SSL: %d HSTS %q", rec.Code, rec.Header().Get("Strict-Transport-Security"))
	}

	ssl := NewRouter()
	ssl.Use(SecureHeaders(SecureConfig{SSLRedirect: true, STS: true}))
	ssl.GET("/p", httpxPattern)
	rec = httpxServe(ssl, http.MethodGet, "http://example.com/p?q=1")
	check("SSL plain HTTP", rec)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://example.com/p?q=1" {
		t.Errorf("plain HTTP with SSL = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for name, mod := range map[string]func(*http.Request){
		"tls":               func(req *http.Request) { req.TLS = &tls.ConnectionState{} },
		"x-forwarded-proto": func(req *http.Request) { req.Header.Set("X-Forwarded-Proto", "HTTPS") },
		"url scheme":        func(req *http.Request) { req.URL.Scheme = "https" },
	} {
		rec = httpxServe(ssl, http.MethodGet, "http://example.com/p", mod)
		check("SSL "+name, rec)
		if rec.Code != http.StatusOK || rec.Header().Get("Strict-Transport-Security") != "max-age=31536000; includeSubDomains" {
			t.Errorf("SSL %s: %d HSTS %q", name, rec.Code, rec.Header().Get("Strict-Transport-Security"))
		}
	}
}

// httpxTrustRouter is a router whose only route answers the client IP, with the trusted proxies
// and the header chosen the way configureTrustedProxiesWithHeader does.
func httpxTrustRouter(addrs, header string) *Router {
	_, nets := parseTrustedProxyList(strings.Fields(strings.ReplaceAll(addrs, ",", " ")))
	if len(nets) == 0 {
		_, nets = parseTrustedProxyList(DefaultReverseProxy)
	}
	h := "X-Forwarded-For"
	if strings.EqualFold(strings.TrimSpace(header), "X-Real-IP") {
		h = "X-Real-IP"
	}
	r := NewRouter()
	r.SetTrustedProxies(nets, h)
	r.GET("/", func(c *Context) { c.String(http.StatusOK, "%s", c.ClientIP()) })
	return r
}

func httpxClientIP(r *Router, peer string, header map[string]string) string {
	return httpxServe(r, http.MethodGet, "/", func(req *http.Request) {
		req.RemoteAddr = peer
		for k, v := range header {
			req.Header.Set(k, v)
		}
	}).Body.String()
}

// The cases of TestW1ServerReverseProxy (wantHTTPS belongs to ReverseProxyMiddleware, not here).
func TestHttpxClientIPReverseProxy(t *testing.T) {
	httpxCaptureLog(t) // parseTrustedProxyList logs the invalid entries
	routers := map[string]*Router{
		"":                                httpxTrustRouter("", ""),
		"10.9.9.9, 2001:db8::/32 bad/x ?": httpxTrustRouter("10.9.9.9, 2001:db8::/32 bad/x ?", ""),
	}
	tests := []struct {
		name      string
		addrs     string
		peer      string
		header    map[string]string
		wantIP    string
		wantHTTPS bool
	}{
		{"xff right-most untrusted", "", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.7"}, "198.51.100.7", false},
		{"xff skips trusted hops", "", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.7, 10.0.0.5"}, "198.51.100.7", false},
		{"invalid x-real-ip ignored", "", "127.0.0.1:1", map[string]string{"X-Real-IP": "not-an-ip"}, "127.0.0.1", false},
		{"x-real-ip ignored under the default header", "", "192.168.1.1:1", map[string]string{"X-Real-IP": "198.51.100.8"}, "192.168.1.1", false},
		{"untrusted peer xff ignored", "", "203.0.113.50:1", map[string]string{"X-Forwarded-For": "1.2.3.4"}, "203.0.113.50", false},
		{"untrusted peer proto ignored", "", "203.0.113.50:1", map[string]string{"X-Forwarded-Proto": "https"}, "203.0.113.50", false},
		{"trusted peer proto https", "", "127.0.0.1:1", map[string]string{"X-Forwarded-Proto": "HTTPS"}, "127.0.0.1", true},
		{"trusted peer proto http", "", "127.0.0.1:1", map[string]string{"X-Forwarded-Proto": "http"}, "127.0.0.1", false},
		{"forwarded host not applied", "", "127.0.0.1:1", map[string]string{"X-Forwarded-Host": "evil.example"}, "127.0.0.1", false},
		{"configured proxy trusted", "10.9.9.9, 2001:db8::/32 bad/x ?", "10.9.9.9:1", map[string]string{"X-Forwarded-For": "198.51.100.9", "X-Forwarded-Proto": "https"}, "198.51.100.9", true},
		{"configured v6 proxy trusted", "10.9.9.9, 2001:db8::/32 bad/x ?", "[2001:db8::1]:1", map[string]string{"X-Forwarded-For": "198.51.100.10"}, "198.51.100.10", false},
		{"default list not used when configured", "10.9.9.9, 2001:db8::/32 bad/x ?", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.11", "X-Forwarded-Proto": "https"}, "127.0.0.1", false},
		// Beyond the original table.
		{"peer without port", "", "127.0.0.1", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "", false},
		{"invalid xff entry falls back to the peer", "", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.7, garbage"}, "127.0.0.1", false},
		{"all hops trusted gives the left-most", "", "127.0.0.1:1", map[string]string{"X-Forwarded-For": " 10.0.0.7 , 10.0.0.5"}, "10.0.0.7", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpxClientIP(routers[tc.addrs], tc.peer, tc.header); got != tc.wantIP {
				t.Errorf("ClientIP = %q, want %q", got, tc.wantIP)
			}
		})
	}
}

// The cases of TestLo2ServerTrustedHeader and TestLo2ServerDefaultProxies.
func TestHttpxClientIPTrustedHeader(t *testing.T) {
	both := map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Real-IP": "203.0.113.7"}
	realOnly := map[string]string{"X-Real-IP": "203.0.113.7"}
	xffOnly := map[string]string{"X-Forwarded-For": "198.51.100.7"}

	routers := map[string]*Router{
		"":                httpxTrustRouter("", ""),
		"X-Forwarded-For": httpxTrustRouter("", "X-Forwarded-For"),
		"X-Real-IP":       httpxTrustRouter("", "X-Real-IP"),
		"X-Client-Ip":     httpxTrustRouter("", "X-Client-Ip"),
	}
	if routers["X-Real-IP"].RemoteIPHeader() != "X-Real-IP" || routers["X-Client-Ip"].RemoteIPHeader() != "X-Forwarded-For" {
		t.Errorf("RemoteIPHeader = %q / %q", routers["X-Real-IP"].RemoteIPHeader(), routers["X-Client-Ip"].RemoteIPHeader())
	}

	tests := []struct {
		name   string
		header string
		send   map[string]string
		want   string
	}{
		{"x-real-ip wins over xff", "X-Real-IP", both, "203.0.113.7"},
		{"x-real-ip alone", "X-Real-IP", realOnly, "203.0.113.7"},
		{"x-real-ip setting ignores xff", "X-Real-IP", xffOnly, "127.0.0.1"},
		{"default uses xff", "", both, "198.51.100.7"},
		{"default ignores x-real-ip", "", realOnly, "127.0.0.1"},
		{"explicit xff setting", "X-Forwarded-For", both, "198.51.100.7"},
		{"invalid header falls back to xff", "X-Client-Ip", both, "198.51.100.7"},
		{"invalid header ignores x-real-ip", "X-Client-Ip", realOnly, "127.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpxClientIP(routers[tc.header], "127.0.0.1:1", tc.send); got != tc.want {
				t.Errorf("header %q with %v: ClientIP = %q, want %q", tc.header, tc.send, got, tc.want)
			}
		})
	}

	def := routers[""]
	for _, tc := range []struct {
		peer string
		want string
	}{
		{"127.0.0.2:1", "198.51.100.7"},
		{"127.0.0.1:1", "198.51.100.7"},
		{"[fd00::1]:1", "198.51.100.7"},
		{"10.0.0.5:1", "198.51.100.7"},
		{"203.0.113.50:1", "203.0.113.50"}, // untrusted: the header is ignored
	} {
		if got := httpxClientIP(def, tc.peer, xffOnly); got != tc.want {
			t.Errorf("peer %s: ClientIP = %q, want %q", tc.peer, got, tc.want)
		}
	}

	// A router without trusted proxies ignores every header.
	if got := httpxClientIP(httpxNoTrustRouter(), "127.0.0.1:1", both); got != "127.0.0.1" {
		t.Errorf("no trusted proxies: ClientIP = %q", got)
	}
}

// httpxNoTrustRouter is a router without SetTrustedProxies whose only route answers the client IP.
func httpxNoTrustRouter() *Router {
	r := NewRouter()
	r.GET("/", func(c *Context) { c.String(http.StatusOK, "%s", c.ClientIP()) })
	return r
}

// httpxMainRoutes is the production route table of setupRoutes (webserver_core_routes.go) in
// ServeMux syntax: ":name" -> "{name}", "*filepath" -> "{filepath...}", a trailing "/" -> "/{$}".
var httpxMainRoutes = []string{
	"GET /static/{filepath...}",
	"GET /favicon.ico",
	"HEAD /favicon.ico",
	"GET /sitemap.xml",
	"GET /robots.txt",
	"GET /ping",
	"GET /api",
	"GET /api/{$}",
	"GET /api/v1",
	"GET /api/v1/{$}",
	"GET /api/v1/stats",
	"GET /api/v1/stats/{$}",
	"GET /api/v1/groups/{group}/articles/{articleNum}/preview",
	"GET /api/v1/groups",
	"GET /api/v1/groups/{$}",
	"GET /api/v1/groups/{group}/overview",
	"GET /api/v1/groups/{group}/overview/{$}",
	"GET /api/v1/groups/{group}/articles/{articleNum}",
	"GET /api/v1/groups/{group}/message/{messageId}",
	"GET /api/v1/groups/{group}/threads",
	"GET /api/v1/groups/{group}/threads/{$}",
	"GET /login",
	"POST /login",
	"GET /register",
	"POST /register",
	"GET /logout",
	"GET /profile",
	"POST /profile",
	"GET /aichat",
	"POST /aichat/send",
	"GET /aichat/models",
	"POST /aichat/history/{model}",
	"POST /aichat/clear/{model}",
	"POST /aichat/clear/all",
	"GET /aichat/counts",
	"GET /admin",
	"POST /admin/users",
	"POST /admin/users/update",
	"POST /admin/users/delete",
	"POST /admin/aimodels",
	"POST /admin/aimodels/update",
	"POST /admin/aimodels/delete",
	"POST /admin/aimodels/sync",
	"POST /admin/newsgroups",
	"POST /admin/newsgroups/edit",
	"POST /admin/newsgroups/update",
	"POST /admin/newsgroups/delete",
	"POST /admin/newsgroups/assign-section",
	"POST /admin/newsgroups/toggle",
	"POST /admin/newsgroups/migrate-activity",
	"POST /admin/newsgroups/hide-future-posts",
	"POST /admin/newsgroups/fix-thread-activity",
	"POST /admin/newsgroups/bulk-enable",
	"POST /admin/newsgroups/bulk-disable",
	"POST /admin/newsgroups/bulk-delete",
	"POST /admin/providers",
	"POST /admin/providers/update",
	"POST /admin/providers/delete",
	"POST /admin/apitokens",
	"POST /admin/apitokens/toggle",
	"POST /admin/apitokens/delete",
	"POST /admin/sitenews",
	"POST /admin/sitenews/update",
	"POST /admin/sitenews/delete",
	"POST /admin/sitenews/toggle",
	"POST /admin/nntpusers",
	"POST /admin/nntpusers/update",
	"POST /admin/nntpusers/delete",
	"POST /admin/nntpusers/toggle",
	"POST /admin/nntpusers/enable",
	"POST /admin/sections",
	"POST /admin/sections/update",
	"POST /admin/sections/delete",
	"POST /admin/sections/assign",
	"POST /admin/sections/unassign",
	"POST /admin/cache/clear",
	"POST /admin/hierarchies/update",
	"POST /admin/hierarchies/restore-descriptions",
	"POST /admin/settings",
	"POST /admin/crons/create",
	"POST /admin/crons/update",
	"POST /admin/crons/toggle",
	"POST /admin/crons/delete",
	"POST /admin/crons/stop",
	"GET /admin/cronjobs/viewlog/{id}",
	"POST /admin/postqueue/delete",
	"POST /admin/postqueue/retry",
	"GET /{$}",
	"GET /groups",
	"GET /groups/{$}",
	"GET /hierarchies",
	"GET /hierarchies/{$}",
	"GET /hierarchy-groups/{hierarchy}",
	"GET /hierarchy/{hierarchy}",
	"GET /hierarchy/{hierarchy}/{$}",
	"GET /hierarchy/{hierarchy}/{level1}",
	"GET /hierarchy/{hierarchy}/{level1}/{$}",
	"GET /hierarchy/{hierarchy}/{level1}/{level2}",
	"GET /hierarchy/{hierarchy}/{level1}/{level2}/{$}",
	"GET /hierarchy/{hierarchy}/{level1}/{level2}/{level3}",
	"GET /hierarchy/{hierarchy}/{level1}/{level2}/{level3}/{$}",
	"GET /groups/{group}",
	"GET /groups/{group}/{$}",
	"POST /groups/{group}/articles/{articleNum}/spam",
	"POST /groups/{group}/articles/{articleNum}/hide",
	"POST /groups/{group}/articles/{articleNum}/decrement-spam",
	"POST /groups/{group}/articles/{articleNum}/unhide",
	"GET /groups/{group}/threads",
	"GET /groups/{group}/thread/{threadRoot}",
	"GET /groups/{group}/articles/{articleNum}",
	"GET /groups/{group}/articles/{articleNum}/preview",
	"GET /groups/{group}/message/{messageId}",
	"GET /groups/{group}/tree/{threadRoot}",
	"GET /search",
	"GET /search/{$}",
	"GET /stats",
	"GET /stats/{$}",
	"GET /SiteHelp",
	"GET /SiteHelp/{$}",
	"GET /SiteNews",
	"GET /SiteNews/{$}",
	"GET /SiteIRC",
	"GET /SiteIRC/{$}",
	"GET /SitePost",
	"POST /SitePost",
	"POST /SitePostSubmit",
	"GET /sections",
	"GET /sections/{$}",
	"GET /demo/thread-tree",
	"GET /demo/thread-tree/{$}",
	"GET /api/thread-tree",
}

// httpxFallbackRoutes are the six section routes, which conflict with the main table.
var httpxFallbackRoutes = []string{
	"GET /{section}",
	"GET /{section}/{$}",
	"GET /{section}/{group}/{$}",
	"GET /{section}/{group}/articles/{articleNum}",
	"GET /{section}/{group}/message/{messageId}",
	"GET /{section}/{group}/tree/{threadRoot}",
}

// TestHttpxProductionRouteTable registers the converted production table (no conflict panic)
// and checks that representative requests reach the intended pattern.
func TestHttpxProductionRouteTable(t *testing.T) {
	// 137 live registrations (setupRoutes also has one commented-out api.GET).
	if n := len(httpxMainRoutes) + len(httpxFallbackRoutes); n != 137 {
		t.Errorf("route table has %d registrations, want 137", n)
	}
	r := NewRouter()
	r.Use(SecureHeaders(SecureConfig{}))
	reg := func(add func(method, path string, h ...HandlerFunc), routes []string) {
		for _, route := range routes {
			method, path, ok := strings.Cut(route, " ")
			if !ok {
				t.Fatalf("bad route %q", route)
			}
			func() {
				defer func() {
					if v := recover(); v != nil {
						t.Errorf("registering %q panicked: %v", route, v)
					}
				}()
				add(method, path, httpxPattern)
			}()
		}
	}
	reg(r.Handle, httpxMainRoutes)
	reg(r.Fallback(func(c *Context) { c.Next() }).Handle, httpxFallbackRoutes)

	tests := []struct {
		method, target string
		code           int
		want           string // pattern, or Location for redirects, or Allow for 405
	}{
		{http.MethodGet, "/", 200, "GET /{$}"},
		{http.MethodGet, "/hierarchy/a/b/c", 200, "GET /hierarchy/{hierarchy}/{level1}/{level2}"},
		{http.MethodGet, "/hierarchy/a/b/c/d/", 200, "GET /hierarchy/{hierarchy}/{level1}/{level2}/{level3}/{$}"},
		{http.MethodGet, "/sec/grp/articles/1", 200, "GET /{section}/{group}/articles/{articleNum}"},
		{http.MethodGet, "/groups/grp/articles/1", 200, "GET /groups/{group}/articles/{articleNum}"},
		{http.MethodGet, "/static/js/x.js", 200, "GET /static/{filepath...}"},
		{http.MethodPost, "/aichat/clear/all", 200, "POST /aichat/clear/all"},
		{http.MethodPost, "/aichat/clear/m", 200, "POST /aichat/clear/{model}"},
		{http.MethodGet, "/rocksolid", 200, "GET /{section}"},
		{http.MethodGet, "/rocksolid/", 200, "GET /{section}/{$}"},
		{http.MethodGet, "/groups", 200, "GET /groups"},
		{http.MethodGet, "/groups/", 200, "GET /groups/{$}"},
		{http.MethodHead, "/favicon.ico", 200, "HEAD /favicon.ico"},
		{http.MethodGet, "/groups/g/message/%3Ca%2Fb+c@x%3E", 200, "GET /groups/{group}/message/{messageId}"},
		{http.MethodGet, "/api/v1/groups/g/threads/", 200, "GET /api/v1/groups/{group}/threads/{$}"},
		{http.MethodGet, "/rocksolid/rocksolid.nodes.help", 301, "/rocksolid/rocksolid.nodes.help/"},
		{http.MethodPost, "/login/", 307, "/login"},
		{http.MethodPost, "/groups", 405, "GET, HEAD"},
		{http.MethodGet, "/admin/users/update", 405, "POST"},
		// A two-segment path also matches the fallback's /{section}/{group}/{$} with a slash:
		// the trailing-slash redirect comes before 405.
		{http.MethodGet, "/aichat/send", 301, "/aichat/send/"},
		{http.MethodGet, "/a/b/c/d/e", 404, ""},
	}
	for _, tc := range tests {
		rec := httpxServe(r, tc.method, tc.target)
		var got string
		switch rec.Code {
		case http.StatusOK:
			got = rec.Body.String()
		case http.StatusMovedPermanently, http.StatusTemporaryRedirect:
			got = rec.Header().Get("Location")
		case http.StatusMethodNotAllowed:
			got = rec.Header().Get("Allow")
		}
		if rec.Code != tc.code || got != tc.want {
			t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.target, rec.Code, got, tc.code, tc.want)
		}
		if rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
			t.Errorf("%s %s: no CSP header", tc.method, tc.target)
		}
	}
}

// TestHttpxMultipartTempCleanup: the temporary file of a large multipart part, parsed on the
// router's request copy, is removed when the request ends.
func TestHttpxMultipartTempCleanup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("username", "u1"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("upload", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(bytes.Repeat([]byte("x"), httpxMaxMultipartMemory+64<<10)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewRouter()
	var sawFile bool
	r.POST("/login", func(c *Context) {
		user := c.PostForm("username")
		sawFile = c.Request.MultipartForm != nil && len(c.Request.MultipartForm.File["upload"]) == 1
		c.String(http.StatusOK, "%s", user)
	})
	rec := httpxServe(r, http.MethodPost, "/login", func(req *http.Request) {
		req.Body = io.NopCloser(&body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
	})
	if rec.Code != http.StatusOK || rec.Body.String() != "u1" || !sawFile {
		t.Fatalf("POST = %d %q, file part parsed %v", rec.Code, rec.Body.String(), sawFile)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary files left after the request: %v", entries)
	}
}

// TestHttpxHijack: a hijacked connection gets no status line from the router; Hijack after a
// write fails.
func TestHttpxHijack(t *testing.T) {
	logs := httpxCaptureLog(t)
	crlf := string([]byte{13, 10})
	raw := strings.Join([]string{"HTTP/1.1 200 OK", "Content-Length: 2", "Connection: close", "", "ok"}, crlf)
	r := NewRouter()
	r.GET("/h", func(c *Context) {
		c.Status(http.StatusAccepted) // recorded only; must not be written after the hijack
		conn, brw, err := c.Writer.Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString(raw)
		_ = brw.Flush()
	})
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(done)
		r.ServeHTTP(w, req)
	}))
	defer ts.Close()

	client := &http.Client{Timeout: testWait}
	resp, err := client.Get(ts.URL + "/h")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(got) != "ok" {
		t.Errorf("hijacked answer = %d %q, %v", resp.StatusCode, got, err)
	}
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("handler did not return")
	}
	if out := logs.String(); strings.Contains(out, "hijacked") {
		t.Errorf("router wrote on the hijacked connection: %q", out)
	}

	c, _ := httpxTestCtx(http.MethodGet, "/")
	c.String(http.StatusOK, "x")
	if _, _, err := c.Writer.Hijack(); !errors.Is(err, errHttpxHijackWritten) {
		t.Errorf("Hijack after a write: err = %v", err)
	}
}

// TestHttpxAccessLogPanic: with AccessLog before Recovery a panicking request gets the
// recovery line and an access line with the 500.
func TestHttpxAccessLogPanic(t *testing.T) {
	logs := httpxCaptureLog(t)
	r := NewRouter()
	r.Use(AccessLog(), Recovery())
	r.GET("/httpx-accpanic", func(c *Context) { panic("httpx-accpanic-boom") })
	rec := httpxServe(r, http.MethodGet, "/httpx-accpanic", func(req *http.Request) { req.RemoteAddr = "192.0.2.8:1" })
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "[WEB]: panic serving GET /httpx-accpanic: httpx-accpanic-boom") {
		t.Errorf("no recovery line: %q", out)
	}
	if !strings.Contains(out, `[WEB]: 192.0.2.8 "GET /httpx-accpanic" 500 0B `) {
		t.Errorf("no access line with 500: %q", out)
	}
}
