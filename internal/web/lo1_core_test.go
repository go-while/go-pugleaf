package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
)

// lo1CoreNewServer builds a server of its own (never w0Srv, which the whole package shares) with
// the cron manager off, and makes sure its background goroutines are stopped when the test ends.
func lo1CoreNewServer(t *testing.T) *WebServer {
	t.Helper()
	cfg := config.NewDefaultConfig().Server.WEB
	cfg.ListenPort = 0
	cfg.SSL = false
	srv := NewWebServer(w0DB(t), cfg, nil, false, true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return srv
}

// lo1CoreServe serves one GET request through srv.Router.
func lo1CoreServe(t *testing.T, srv *WebServer, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "192.0.2.10:1234"
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Router.ServeHTTP(rec, req)
	return rec
}

// lo1CoreSetAPIEnabled sets APIEnabled and restores the value it replaced when the test ends.
func lo1CoreSetAPIEnabled(t *testing.T, value string) {
	t.Helper()
	db := w0DB(t)
	old, err := db.GetConfigValue(config.CFG_KEY_API_ENABLED)
	if err != nil {
		old = "false"
	}
	if err := db.SetConfigValue(config.CFG_KEY_API_ENABLED, value); err != nil {
		t.Fatalf("SetConfigValue(%s, %s): %v", config.CFG_KEY_API_ENABLED, value, err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_API_ENABLED, old); err != nil {
			t.Errorf("restore %s: %v", config.CFG_KEY_API_ENABLED, err)
		}
	})
}

// lo1CoreTokenUsage reads the stored usage of an API token.
func lo1CoreTokenUsage(t *testing.T, tokenID int64) (int64, string) {
	t.Helper()
	var count int64
	var lastUsed string
	err := database.RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT COALESCE(usage_count,0), COALESCE(last_used_at,'') FROM api_tokens WHERE id = ?",
		[]interface{}{tokenID}, &count, &lastUsed)
	if err != nil {
		t.Fatalf("read usage of token %d: %v", tokenID, err)
	}
	return count, lastUsed
}

// lo1CoreWaitInFlight waits until srv has no request in a handler any more.
func lo1CoreWaitInFlight(srv *WebServer, timeout time.Duration) int64 {
	deadline := time.Now().Add(timeout)
	for {
		n := srv.inFlightRequests.Load()
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLo1CoreShutdownCancelsInFlight: when the graceful deadline passes, Shutdown cancels the
// request contexts instead of returning while handlers keep using the database (B1).
func TestLo1CoreShutdownCancelsInFlight(t *testing.T) {
	srv := lo1CoreNewServer(t)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	srv.Router.GET("/lo1core/block", func(c *gin.Context) {
		close(started)
		<-c.Request.Context().Done()
		close(cancelled)
		c.String(http.StatusOK, "cancelled")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.serveOn(ln) }()

	url := "http://" + ln.Addr().String() + "/lo1core/block"
	go func() {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", "lo1core-test/1.0")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the blocking handler never ran")
	}
	if n := srv.inFlightRequests.Load(); n < 1 {
		t.Fatalf("inFlightRequests = %d while a handler runs, want >= 1", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Shutdown(ctx) }()
	select {
	case err := <-done:
		// The handler stopped on the cancellation and within the grace, so the shutdown did
		// complete: the graceful deadline alone is not reported as a failure.
		if err != nil {
			t.Fatalf("Shutdown = %v, want nil after the cancelled handler finished", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return within 2s")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the request context of the blocked handler was never cancelled")
	}
	if n := lo1CoreWaitInFlight(srv, 2*time.Second); n != 0 {
		t.Fatalf("inFlightRequests = %d after Shutdown, want 0", n)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serveOn returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveOn did not return after Shutdown")
	}
}

// TestLo1CoreTokenUsageBuffered: API token usage is counted in memory and written by the flusher,
// not by one goroutine and one main-DB write per request (B6).
func TestLo1CoreTokenUsageBuffered(t *testing.T) {
	lo1CoreSetAPIEnabled(t, "true")
	db := w0DB(t)
	token, plain, err := db.CreateAPIToken("lo1core-usage", 0, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	srv := lo1CoreNewServer(t)
	hdr := map[string]string{APIAuthHeader: plain}
	for i := 0; i < 7; i++ {
		if rec := lo1CoreServe(t, srv, "/api/v1/groups", hdr); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, body %s", i, rec.Code, rec.Body.String())
		}
	}
	if count, lastUsed := lo1CoreTokenUsage(t, token.ID); count != 0 || lastUsed != "" {
		t.Fatalf("usage written before a flush: usage_count=%d last_used_at=%q", count, lastUsed)
	}

	srv.flushTokenUsage()
	count, lastUsed := lo1CoreTokenUsage(t, token.ID)
	if count != 7 || lastUsed == "" {
		t.Fatalf("after flush: usage_count=%d last_used_at=%q, want 7 and a timestamp", count, lastUsed)
	}
	srv.tokenUsageBuffer.mu.Lock()
	left := len(srv.tokenUsageBuffer.counts)
	srv.tokenUsageBuffer.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d token(s) left in the buffer after a successful flush", left)
	}
	srv.flushTokenUsage() // an empty buffer writes nothing
	if count, _ := lo1CoreTokenUsage(t, token.ID); count != 7 {
		t.Fatalf("usage_count = %d after an empty flush, want 7", count)
	}

	// The flusher writes the rest on Shutdown and stops afterwards.
	for i := 0; i < 3; i++ {
		if rec := lo1CoreServe(t, srv, "/api/v1/groups", hdr); rec.Code != http.StatusOK {
			t.Fatalf("request %d after flush: status %d", i, rec.Code)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-srv.tokenUsageDone:
	default:
		t.Fatal("the token usage flusher is still running after Shutdown returned")
	}
	if count, _ := lo1CoreTokenUsage(t, token.ID); count != 10 {
		t.Fatalf("usage_count = %d after Shutdown, want 10", count)
	}
}

// TestLo1CoreFinalFlushBounded: the last usage flush of Shutdown cannot hold the shutdown. One
// UPDATE on a main DB that somebody else keeps locked blocks for busy_timeout (30s) per attempt
// and retries for minutes of busy time, while the NNTP server, the workers and the database are
// all still up, so Shutdown must give up on it after shutdownGrace.
func TestLo1CoreFinalFlushBounded(t *testing.T) {
	lo1CoreSetAPIEnabled(t, "true")
	db := w0DB(t)
	token, _, err := db.CreateAPIToken("lo1core-bound", 0, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	srv := lo1CoreNewServer(t)

	// First shutdown: the flusher does its own last flush and stops (tokenUsageDone is closed),
	// so the second one below runs exactly the final flush this test is about.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	// Usage counted after that, as a request served during the drain does.
	srv.recordTokenUsage(token.ID, time.Now())

	// Hold the main DB write lock, so every AddTokenUsage attempt runs into SQLITE_BUSY.
	tx, err := db.GetMainDB().Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("UPDATE api_tokens SET ownername = ownername WHERE id = ?", token.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("lock the main DB: %v", err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = tx.Rollback() }) }
	defer release()

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_ = srv.Shutdown(ctx)
		done <- time.Since(start)
	}()
	select {
	case took := <-done:
		if took > shutdownGrace+3*time.Second {
			t.Fatalf("Shutdown took %v with a locked main DB, want at most the %v grace", took, shutdownGrace)
		}
	case <-time.After(shutdownGrace + 3*time.Second):
		t.Fatalf("Shutdown did not return within %v although the main DB is locked", shutdownGrace+3*time.Second)
	}
	release() // let the abandoned writer finish instead of retrying into the next test
}

// TestLo1CoreTokenUsageDuringDrain: the usage of a request that is served while Shutdown drains
// the in-flight requests still reaches the database. The flusher stops when stopCh closes, which
// happens before the drain, so Shutdown has to flush once more after it (B6 regression guard).
func TestLo1CoreTokenUsageDuringDrain(t *testing.T) {
	lo1CoreSetAPIEnabled(t, "true")
	db := w0DB(t)
	token, plain, err := db.CreateAPIToken("lo1core-drain", 0, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	srv := lo1CoreNewServer(t)

	started := make(chan struct{})
	drainCode := make(chan int, 1)
	srv.Router.GET("/lo1core/drain", func(c *gin.Context) {
		close(started)
		// Stay in flight until Shutdown is draining, then make an authenticated API request
		// from inside that window: by then the usage flusher has already stopped.
		time.Sleep(300 * time.Millisecond)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/groups", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		req.Header.Set(APIAuthHeader, plain)
		rec := httptest.NewRecorder()
		srv.Router.ServeHTTP(rec, req)
		drainCode <- rec.Code
		c.String(http.StatusOK, "drained")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.serveOn(ln) }()
	url := "http://" + ln.Addr().String() + "/lo1core/drain"
	go func() {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", "lo1core-test/1.0")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the draining handler never ran")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err) // the handler finishes well inside the deadline
	}
	select {
	case code := <-drainCode:
		if code != http.StatusOK {
			t.Fatalf("API request during the drain: status %d, want 200", code)
		}
	case <-time.After(time.Second):
		t.Fatal("the API request during the drain never completed")
	}
	if count, lastUsed := lo1CoreTokenUsage(t, token.ID); count != 1 || lastUsed == "" {
		t.Fatalf("usage of the request served during the drain: usage_count=%d last_used_at=%q, want 1 and a timestamp",
			count, lastUsed)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serveOn returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveOn did not return after Shutdown")
	}
}

// TestLo1CoreStatsSingleflight: concurrent requests on a cold cache recompute the statistics once (B4).
func TestLo1CoreStatsSingleflight(t *testing.T) {
	lo1CoreSetAPIEnabled(t, "true")
	lo1CoreExpireStats := func() {
		apiStatsCache.mu.Lock()
		apiStatsCache.stats = nil
		apiStatsCache.at = time.Time{}
		apiStatsCache.mu.Unlock()
	}
	lo1CoreExpireStats()
	t.Cleanup(lo1CoreExpireStats) // leave no cached answer behind for other tests

	before := statsComputeCount.Load()
	const requests = 20
	codes := make([]int, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
			req.RemoteAddr = "192.0.2.10:1234"
			rec := httptest.NewRecorder()
			w0Srv.Router.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, code)
		}
	}
	if got := statsComputeCount.Load() - before; got != 1 {
		t.Fatalf("statistics recomputed %d times for %d concurrent requests, want 1", got, requests)
	}
}

// TestLo1CoreStaticContentType covers the extensions the old last-4-bytes comparison got wrong (B9).
func TestLo1CoreStaticContentType(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"web/favicon.ico", "image/x-icon"},
		{"static/js/thread-tree.js", "text/javascript; charset=utf-8"},
		{"static/js/app.min.map", "application/json"},
		{"static/data/groups.json", "application/json"},
		{"static/fonts/x.woff", "font/woff"},
		{"static/fonts/x.woff2", "font/woff2"},
		{"static/fonts/x.ttf", "font/ttf"},
		{"static/img/x.webp", "image/webp"},
		{"static/img/x.png", "image/png"},
		{"static/css/style.css", "text/css; charset=utf-8"},
		{"static/img/logo.svg", "image/svg+xml"},
		{"static/x.unknown-extension", "application/octet-stream"},
		{"noextension", "application/octet-stream"},
	} {
		if got := staticContentType(tc.path); got != tc.want {
			t.Errorf("staticContentType(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
	// Upper case extensions and an extension known only to the system MIME database.
	if got := staticContentType("static/img/LOGO.PNG"); got != "image/png" {
		t.Errorf("staticContentType(LOGO.PNG) = %q, want image/png", got)
	}
}

// TestLo1CoreFavicon: /favicon.ico is served from disk with an image type (B9).
func TestLo1CoreFavicon(t *testing.T) {
	rec := w0Do(t, w0Req{Path: "/favicon.ico"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /favicon.ico = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/x-icon" {
		t.Fatalf("Content-Type = %q, want image/x-icon", got)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty favicon body")
	}
	// Browsers and proxies probe the icon with HEAD, which gin does not answer from a GET route.
	req := httptest.NewRequest(http.MethodHead, "/favicon.ico", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	head := httptest.NewRecorder()
	w0Srv.Router.ServeHTTP(head, req)
	if head.Code != http.StatusOK || head.Header().Get("Content-Type") != "image/x-icon" {
		t.Fatalf("HEAD /favicon.ico = %d %q, want 200 image/x-icon", head.Code, head.Header().Get("Content-Type"))
	}
}

// TestLo1CorePreviewRoutes: the tree view preview has a web route that ignores the API setting,
// while the /api/v1 one follows it (B11). Group access is checked on both.
func TestLo1CorePreviewRoutes(t *testing.T) {
	group := w0NewGroup(t, true)
	groupDB, err := w0DB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	_, err = database.RetryableExec(groupDB.DB,
		`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
			"references", bytes, lines, reply_count, path, headers_json, body_text, downloaded)
		 VALUES (1, ?, ?, ?, ?, '', '', 10, 1, 0, '.POSTED!not-for-mail', '', ?, 1)`,
		"<lo1core."+group+"@test.invalid>", "Lo1 Core Preview", "tester@test.invalid",
		time.Now().UTC().Format("2006-01-02 15:04:05"), "preview body")
	groupDB.Return()
	if err != nil {
		t.Fatalf("insert article: %v", err)
	}
	inactive := w0NewGroup(t, false)

	webPath := "/groups/" + group + "/articles/1/preview"
	apiPath := "/api/v1" + webPath

	lo1CoreSetAPIEnabled(t, "true")
	for _, path := range []string{webPath, apiPath} {
		rec := w0Do(t, w0Req{Path: path})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s with the API enabled = %d, want 200; body %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Lo1 Core Preview") {
			t.Fatalf("GET %s: subject missing from %s", path, rec.Body.String())
		}
	}
	for _, path := range []string{
		"/groups/" + inactive + "/articles/1/preview",
		"/api/v1/groups/" + inactive + "/articles/1/preview",
	} {
		if rec := w0Do(t, w0Req{Path: path}); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s (inactive group, anonymous) = %d, want 404", path, rec.Code)
		}
	}

	lo1CoreSetAPIEnabled(t, "false")
	if rec := w0Do(t, w0Req{Path: apiPath}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s with the API disabled = %d, want 503", apiPath, rec.Code)
	}
	if rec := w0Do(t, w0Req{Path: webPath}); rec.Code != http.StatusOK {
		t.Fatalf("GET %s with the API disabled = %d, want 200", webPath, rec.Code)
	}
}
