package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// w1ServerSaveBotConfig restores the bot and IP blocking globals when the test ends.
func w1ServerSaveBotConfig(t *testing.T) {
	t.Helper()
	config.BadBotsMutex.RLock()
	blockBots, bots := config.BlockBadBots, config.Default_BadBots
	config.BadBotsMutex.RUnlock()
	config.BadIPsMutex.RLock()
	blockIPs, ips := config.BlockBadIPs, config.Default_BlockedIPs
	config.BadIPsMutex.RUnlock()
	t.Cleanup(func() {
		config.BadBotsMutex.Lock()
		config.BlockBadBots, config.Default_BadBots = blockBots, bots
		config.BadBotsMutex.Unlock()
		config.BadIPsMutex.Lock()
		config.BlockBadIPs, config.Default_BlockedIPs = blockIPs, ips
		config.BadIPsMutex.Unlock()
	})
}

func TestW1ServerBotMiddlewareReleasesLock(t *testing.T) {
	w1ServerSaveBotConfig(t)
	config.UpdateBadBots("SomeBot", true)
	if err := config.UpdateBadIPs("", true); err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.Use(w0Srv.BotDetectionMiddleware())
	r.GET("/update", func(c *gin.Context) {
		// Takes BadBotsMutex.Lock and BadIPsMutex.Lock while the middleware is still on the stack
		config.UpdateBadBots("x", false)
		if err := config.UpdateBadIPs("", false); err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		c.String(http.StatusOK, "ok")
	})

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/update", nil))
		done <- rec.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler calling UpdateBadBots/UpdateBadIPs deadlocked: middleware holds the lock across c.Next()")
	}
}

func TestW1ServerBotPatternsLowercase(t *testing.T) {
	w1ServerSaveBotConfig(t)
	config.UpdateBadBots(" EvilCrawler ,  ", true)
	config.BadBotsMutex.RLock()
	got := append([]string(nil), config.Default_BadBots...)
	config.BadBotsMutex.RUnlock()
	if len(got) != 1 || got[0] != "evilcrawler" {
		t.Fatalf("Default_BadBots = %q, want [evilcrawler]", got)
	}

	r := gin.New()
	r.Use(w0Srv.BotDetectionMiddleware())
	r.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	for ua, want := range map[string]int{
		"Mozilla/5.0 (compatible; EVILCRAWLER/2.1)": http.StatusForbidden,
		"Mozilla/5.0 Firefox/130":                   http.StatusOK,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("UA %q: status %d, want %d", ua, rec.Code, want)
		}
	}
}

func TestW1ServerSectionsCacheRace(t *testing.T) {
	name := strings.ReplaceAll(w0Name("w1srvsec"), ".", "")
	if _, err := database.RetryableExec(w0DB(t).GetMainDB(), "INSERT INTO sections(name, display_name) VALUES (?, ?)", name, "w1"); err != nil {
		t.Fatal(err)
	}
	w0Srv.loadSectionsCache()
	if !w0Srv.isValidSection(name) {
		t.Fatalf("isValidSection(%q) = false after loadSectionsCache", name)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = w0Srv.isValidSection(name)
				_ = w0Srv.isValidSection("no-such-section")
			}
		}()
	}
	for i := 0; i < 200; i++ {
		w0Srv.loadSectionsCache()
	}
	close(stop)
	wg.Wait()
	if !w0Srv.isValidSection(name) || w0Srv.isValidSection("no-such-section") {
		t.Fatal("sections cache content wrong after reloads")
	}
}

func TestW1ServerTrustedProxies(t *testing.T) {
	type result struct {
		ClientIP   string
		IsHTTPS    bool
		RemoteAddr string
		Host       string
	}
	newEngine := func(addrs string) *gin.Engine {
		r := gin.New()
		s := &WebServer{trustedProxyNets: configureTrustedProxies(r, addrs)}
		r.Use(s.ReverseProxyMiddleware())
		r.GET("/", func(c *gin.Context) {
			v, _ := c.Get("is_https")
			isHTTPS, _ := v.(bool)
			c.JSON(http.StatusOK, result{c.ClientIP(), isHTTPS, c.Request.RemoteAddr, c.Request.Host})
		})
		return r
	}
	engines := map[string]*gin.Engine{
		"":                                newEngine(""),
		"10.9.9.9, 2001:db8::/32 bad/x ?": newEngine("10.9.9.9, 2001:db8::/32 bad/x ?"),
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
		{"valid x-real-ip from trusted peer", "", "192.168.1.1:1", map[string]string{"X-Real-IP": "198.51.100.8"}, "198.51.100.8", false},
		{"untrusted peer xff ignored", "", "203.0.113.50:1", map[string]string{"X-Forwarded-For": "1.2.3.4"}, "203.0.113.50", false},
		{"untrusted peer proto ignored", "", "203.0.113.50:1", map[string]string{"X-Forwarded-Proto": "https"}, "203.0.113.50", false},
		{"trusted peer proto https", "", "127.0.0.1:1", map[string]string{"X-Forwarded-Proto": "HTTPS"}, "127.0.0.1", true},
		{"trusted peer proto http", "", "127.0.0.1:1", map[string]string{"X-Forwarded-Proto": "http"}, "127.0.0.1", false},
		{"forwarded host not applied", "", "127.0.0.1:1", map[string]string{"X-Forwarded-Host": "evil.example"}, "127.0.0.1", false},
		{"configured proxy trusted", "10.9.9.9, 2001:db8::/32 bad/x ?", "10.9.9.9:1", map[string]string{"X-Forwarded-For": "198.51.100.9", "X-Forwarded-Proto": "https"}, "198.51.100.9", true},
		{"configured v6 proxy trusted", "10.9.9.9, 2001:db8::/32 bad/x ?", "[2001:db8::1]:1", map[string]string{"X-Forwarded-For": "198.51.100.10"}, "198.51.100.10", false},
		{"default list not used when configured", "10.9.9.9, 2001:db8::/32 bad/x ?", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.11", "X-Forwarded-Proto": "https"}, "127.0.0.1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = "pugleaf.test"
			req.RemoteAddr = tc.peer
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			engines[tc.addrs].ServeHTTP(rec, req)
			var got result
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if got.ClientIP != tc.wantIP {
				t.Errorf("ClientIP = %q, want %q", got.ClientIP, tc.wantIP)
			}
			if got.IsHTTPS != tc.wantHTTPS {
				t.Errorf("is_https = %v, want %v", got.IsHTTPS, tc.wantHTTPS)
			}
			if got.RemoteAddr != tc.peer || got.Host != "pugleaf.test" {
				t.Errorf("RemoteAddr/Host rewritten: %q %q", got.RemoteAddr, got.Host)
			}
		})
	}
}

func TestW1ServerCrossOrigin(t *testing.T) {
	h := w0Srv.rootHandler()
	do := func(header map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, "/admin/sections", strings.NewReader("name=w1csrf&display_name=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "192.0.2.10:1234"
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Errorf("cross-site POST: status %d, want 403", code)
	}
	if code := do(map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Errorf("POST with foreign Origin only: status %d, want 403", code)
	}
	if code := do(map[string]string{"Sec-Fetch-Site": "same-origin"}); code == http.StatusForbidden {
		t.Errorf("same-origin POST: status 403")
	}
	if code := do(nil); code == http.StatusForbidden {
		t.Errorf("POST without browser headers: status 403")
	}
}

func TestW1ServerNilCron(t *testing.T) {
	var cm *CronJobManager
	cm.StartCronManager()
	cm.StopCronManager()
	if out := cm.GetJobOutput(1); out != nil {
		t.Errorf("GetJobOutput = %q, want nil", out)
	}
	if err := cm.StopJob(1); err == nil {
		t.Error("StopJob on nil manager returned nil error")
	}

	// A server built like cmd/web -no-cronjobs: startup and the admin cron handlers must not panic.
	cfg := config.NewDefaultConfig().Server.WEB
	cfg.ListenPort = 0
	srv := NewWebServer(w0DB(t), cfg, nil, true, true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	if srv.CronManager != nil || srv.CronEdit {
		t.Fatalf("CronManager=%v CronEdit=%v, want nil/false", srv.CronManager, srv.CronEdit)
	}
	_, cookie := w0NewUser(t, true)
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.RemoteAddr = "192.0.2.10:1234"
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.Router.ServeHTTP(rec, req)
		return rec
	}
	if rec := serve(http.MethodGet, "/admin/cronjobs/viewlog/1", ""); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("viewlog: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(http.MethodPost, "/admin/crons/stop", "cron_id=1"); rec.Code != http.StatusSeeOther {
		t.Errorf("stop: status %d, want 303", rec.Code)
	}
}

func TestW1ServerStartShutdown(t *testing.T) {
	cfg := config.NewDefaultConfig().Server.WEB
	cfg.ListenPort = 0
	cfg.SSL = false
	srv := NewWebServer(w0DB(t), cfg, nil, false, true)
	srv.StartSessionCleanup()
	errc := make(chan error, 1)
	go func() { errc <- srv.Start() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Shutdown may run before or after Start stored its http.Server: both must end Start.
	time.Sleep(50 * time.Millisecond)
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Shutdown")
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if err := srv.Start(); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Start after Shutdown = %v, want http.ErrServerClosed", err)
	}
}

func TestW1ServerChatNoSessionIDInPage(t *testing.T) {
	postKey := strings.ReplaceAll(w0Name("w1srvai"), ".", "_")
	display := "W1 Server Model " + postKey
	if _, err := w0DB(t).CreateAIModel(postKey, "w1-ollama", display, "test model", true, false, 0); err != nil {
		t.Fatal(err)
	}
	user, cookie := w0NewUser(t, false)
	other, otherCookie := w0NewUser(t, false)
	t.Cleanup(func() {
		chatCacheMux.Lock()
		delete(chatHistoryCache, chatHistoryKey(user.ID, postKey))
		delete(chatHistoryCache, chatHistoryKey(other.ID, postKey))
		chatCacheMux.Unlock()
	})

	rec := w0Do(t, w0Req{Path: "/aichat", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /aichat = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, display) {
		t.Fatalf("chat page does not list the seeded model %q", display)
	}
	if strings.Contains(body, cookie.Value) {
		t.Fatal("chat page contains the session ID")
	}
	if strings.Contains(body, "sessionToken") {
		t.Error("chat page still references sessionToken")
	}

	// History is keyed by user ID: another user does not see it, the owner does.
	chatCacheMux.Lock()
	chatHistoryCache[chatHistoryKey(user.ID, postKey)] = &chatEntry{
		msgs:     []ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}},
		lastUsed: time.Now(),
	}
	chatCacheMux.Unlock()
	counts := func(ck *http.Cookie) int {
		t.Helper()
		rec := w0Do(t, w0Req{Path: "/aichat/counts", Cookies: []*http.Cookie{ck}})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /aichat/counts = %d %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Counts map[string]int `json:"counts"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Counts[postKey]
	}
	if n := counts(cookie); n != 2 {
		t.Errorf("owner count = %d, want 2", n)
	}
	if n := counts(otherCookie); n != 0 {
		t.Errorf("other user count = %d, want 0", n)
	}
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/aichat/history/" + postKey, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":2`) {
		t.Errorf("history = %d %s", rec.Code, rec.Body.String())
	}
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/aichat/clear/all", Cookies: []*http.Cookie{otherCookie}})
	if rec.Code != http.StatusOK {
		t.Errorf("clear all (other user) = %d %s", rec.Code, rec.Body.String())
	}
	if n := counts(cookie); n != 2 {
		t.Errorf("owner count after other user's clear = %d, want 2", n)
	}
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/aichat/clear/all", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Errorf("clear all = %d %s", rec.Code, rec.Body.String())
	}
	if n := counts(cookie); n != 0 {
		t.Errorf("owner count after clear = %d, want 0", n)
	}
}

func TestW1ServerChatSweep(t *testing.T) {
	now := time.Now()
	const prefix = "w1srvsweep_"
	t.Cleanup(func() {
		chatCacheMux.Lock()
		for k := range chatHistoryCache {
			if strings.HasPrefix(k, prefix) {
				delete(chatHistoryCache, k)
			}
		}
		chatCacheMux.Unlock()
		rateLimiterMux.Lock()
		delete(chatRateLimiter, -101)
		delete(chatRateLimiter, -102)
		rateLimiterMux.Unlock()
	})

	chatCacheMux.Lock()
	chatHistoryCache[prefix+"idle"] = &chatEntry{lastUsed: now.Add(-3 * time.Hour)}
	chatHistoryCache[prefix+"fresh"] = &chatEntry{lastUsed: now.Add(-time.Minute)}
	chatCacheMux.Unlock()
	rateLimiterMux.Lock()
	chatRateLimiter[-101] = now.Add(-11 * chatCooldown)
	chatRateLimiter[-102] = now
	rateLimiterMux.Unlock()

	sweepChatCaches(now)

	chatCacheMux.RLock()
	_, idle := chatHistoryCache[prefix+"idle"]
	_, fresh := chatHistoryCache[prefix+"fresh"]
	chatCacheMux.RUnlock()
	if idle || !fresh {
		t.Errorf("history after sweep: idle kept=%v fresh kept=%v", idle, fresh)
	}
	rateLimiterMux.Lock()
	_, oldRL := chatRateLimiter[-101]
	_, newRL := chatRateLimiter[-102]
	rateLimiterMux.Unlock()
	if oldRL || !newRL {
		t.Errorf("rate limiter after sweep: old kept=%v new kept=%v", oldRL, newRL)
	}

	// Cap: fill past chatCacheMaxEntries; the oldest entries are evicted.
	chatCacheMux.Lock()
	base := now.Add(-time.Hour)
	for i := 0; i <= chatCacheMaxEntries; i++ {
		chatHistoryCache[fmt.Sprintf("%scap%d", prefix, i)] = &chatEntry{lastUsed: base.Add(time.Duration(i) * time.Millisecond)}
	}
	chatCacheMux.Unlock()

	sweepChatCaches(now)

	chatCacheMux.RLock()
	n := len(chatHistoryCache)
	_, oldest := chatHistoryCache[prefix+"cap0"]
	_, newest := chatHistoryCache[fmt.Sprintf("%scap%d", prefix, chatCacheMaxEntries)]
	chatCacheMux.RUnlock()
	if n > chatCacheMaxEntries || oldest || !newest {
		t.Errorf("after cap sweep: len=%d oldest kept=%v newest kept=%v", n, oldest, newest)
	}
}

func TestW1ServerHTTPServerTimeouts(t *testing.T) {
	h := http.NewServeMux()
	srv := newHTTPServer(":1234", h)
	if srv.Addr != ":1234" || srv.Handler != h {
		t.Errorf("Addr/Handler not set: %q", srv.Addr)
	}
	if srv.ReadHeaderTimeout != 10*time.Second || srv.ReadTimeout != 60*time.Second ||
		srv.WriteTimeout != 120*time.Second || srv.IdleTimeout != 120*time.Second || srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("timeouts: rh=%v r=%v w=%v idle=%v maxHeader=%d",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
}
