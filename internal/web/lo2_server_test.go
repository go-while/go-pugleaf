package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
)

// Tests of the lo2-web-server slice (plan web-sqlite-leftovers, findings F3, F4, F11-F14).
// They replace the standard logger's output and the package globals ProxyURL,
// ollamaSyncHTTPClient, untrustedForwarderSeen and config.Default_BadBots, so none of them may
// run in parallel; every change is restored with t.Cleanup. w0Srv is never shut down here.

// lo2ServerLogBuf collects log output. The standard logger serializes its writes, but the test
// goroutine reads the buffer while background goroutines still log into it.
type lo2ServerLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lo2ServerLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lo2ServerLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// countLines returns how many captured lines contain substr.
func (b *lo2ServerLogBuf) countLines(substr string) int {
	n := 0
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// lo2ServerCaptureLog sends log output to a buffer for the duration of one test.
func lo2ServerCaptureLog(t *testing.T) *lo2ServerLogBuf {
	t.Helper()
	b := &lo2ServerLogBuf{}
	old := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(old) })
	return b
}

// lo2ServerClientIPEngine builds an engine whose only route reports the resolved client IP.
func lo2ServerClientIPEngine(addrs, header string) *gin.Engine {
	r := gin.New()
	s := &WebServer{trustedProxyNets: configureTrustedProxiesWithHeader(r, addrs, header)}
	r.Use(s.ReverseProxyMiddleware())
	r.GET("/", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
	return r
}

// lo2ServerClientIP serves one request and returns the client IP the engine resolved.
func lo2ServerClientIP(engine *gin.Engine, peer string, header map[string]string) string {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = peer
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec.Body.String()
}

// F3: the client IP comes from the one configured header, never from both.
func TestLo2ServerTrustedHeader(t *testing.T) {
	both := map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Real-IP": "203.0.113.7"}
	realOnly := map[string]string{"X-Real-IP": "203.0.113.7"}
	xffOnly := map[string]string{"X-Forwarded-For": "198.51.100.7"}

	engines := map[string]*gin.Engine{
		"":                lo2ServerClientIPEngine("", ""),
		"X-Forwarded-For": lo2ServerClientIPEngine("", "X-Forwarded-For"),
		"X-Real-IP":       lo2ServerClientIPEngine("", "X-Real-IP"),
		"X-Client-Ip":     lo2ServerClientIPEngine("", "X-Client-Ip"),
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
			if got := lo2ServerClientIP(engines[tc.header], "127.0.0.1:1", tc.send); got != tc.want {
				t.Errorf("header %q with %v: ClientIP = %q, want %q", tc.header, tc.send, got, tc.want)
			}
		})
	}
}

// F4: the default trusted list covers all of 127.0.0.0/8 and fc00::/7.
func TestLo2ServerDefaultProxies(t *testing.T) {
	engine := lo2ServerClientIPEngine("", "")
	tests := []struct {
		peer string
		want string
	}{
		{"127.0.0.2:1", "198.51.100.7"},
		{"127.0.0.1:1", "198.51.100.7"},
		{"[fd00::1]:1", "198.51.100.7"},
		{"10.0.0.5:1", "198.51.100.7"},
		{"203.0.113.50:1", "203.0.113.50"}, // untrusted: the header is ignored
	}
	for _, tc := range tests {
		got := lo2ServerClientIP(engine, tc.peer, map[string]string{"X-Forwarded-For": "198.51.100.7"})
		if got != tc.want {
			t.Errorf("peer %s: ClientIP = %q, want %q", tc.peer, got, tc.want)
		}
	}
}

// lo2ServerResetForwarderSet empties the untrusted-forwarder notice set for one test and puts
// the process-wide one back afterwards.
func lo2ServerResetForwarderSet(t *testing.T) {
	t.Helper()
	untrustedForwarderSeen.Lock()
	oldIPs, oldFull := untrustedForwarderSeen.ips, untrustedForwarderSeen.full
	untrustedForwarderSeen.ips, untrustedForwarderSeen.full = make(map[string]struct{}), false
	untrustedForwarderSeen.Unlock()
	t.Cleanup(func() {
		untrustedForwarderSeen.Lock()
		untrustedForwarderSeen.ips, untrustedForwarderSeen.full = oldIPs, oldFull
		untrustedForwarderSeen.Unlock()
	})
}

// F4: a private peer that forwards headers but is not trusted is logged exactly once.
func TestLo2ServerUntrustedForwarderLoggedOnce(t *testing.T) {
	lo2ServerResetForwarderSet(t)

	logs := lo2ServerCaptureLog(t)
	engine := lo2ServerClientIPEngine("127.0.0.1", "")
	for i := 0; i < 2; i++ {
		if got := lo2ServerClientIP(engine, "10.9.9.9:1", map[string]string{"X-Forwarded-For": "198.51.100.7"}); got != "10.9.9.9" {
			t.Fatalf("request #%d: ClientIP = %q, want the untrusted peer 10.9.9.9", i, got)
		}
	}
	// A peer without forwarded headers, and a trusted one with them, are never logged.
	lo2ServerClientIP(engine, "10.9.9.8:1", nil)
	lo2ServerClientIP(engine, "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.7"})

	if n := logs.countLines("10.9.9.9"); n != 1 {
		t.Errorf("log lines naming 10.9.9.9 = %d, want 1:\n%s", n, logs.String())
	}
	if n := logs.countLines("10.9.9.8"); n != 0 {
		t.Errorf("log lines naming 10.9.9.8 = %d, want 0 (no forwarded headers)", n)
	}
	if !strings.Contains(logs.String(), "ignoring forwarded client IP headers") {
		t.Errorf("log does not carry the untrusted-forwarder message:\n%s", logs.String())
	}
}

// F4: neither the notice set nor the log may grow without bound, and the helper must be safe
// under concurrency. Past the cap the helper goes quiet instead of logging every request.
func TestLo2ServerUntrustedForwarderBounded(t *testing.T) {
	lo2ServerResetForwarderSet(t)
	logs := lo2ServerCaptureLog(t)

	s := &WebServer{}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				s.noteUntrustedForwarder(net.ParseIP(fmt.Sprintf("10.%d.%d.%d", w, i/256, i%256)))
			}
		}(w)
	}
	wg.Wait()

	untrustedForwarderSeen.Lock()
	n := len(untrustedForwarderSeen.ips)
	untrustedForwarderSeen.Unlock()
	if n > maxUntrustedForwarderIPs {
		t.Errorf("untrustedForwarderSeen holds %d IPs, want at most %d", n, maxUntrustedForwarderIPs)
	}
	if got := logs.countLines("suppressing further notices"); got != 1 {
		t.Errorf("suppression notices = %d, want exactly 1:\n%s", got, logs.String())
	}
	if got := logs.countLines("ignoring forwarded client IP headers"); got > maxUntrustedForwarderIPs {
		t.Errorf("%d notice lines for 800 peers, want at most %d", got, maxUntrustedForwarderIPs)
	}

	// Past the cap a new peer must stay silent, however often it comes back.
	for i := 0; i < 5; i++ {
		s.noteUntrustedForwarder(net.ParseIP("172.16.9.9"))
	}
	if got := logs.countLines("172.16.9.9"); got != 0 {
		t.Errorf("log lines naming a peer seen after the cap = %d, want 0", got)
	}
}

// F11: an oversized chat body is rejected with 413 before it is decoded.
func TestLo2ServerChatBodyLimit(t *testing.T) {
	_, cookie := w0NewUser(t, false)

	send := func(message string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"message": message})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/aichat/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.10:1234"
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		w0Srv.Router.ServeHTTP(rec, req)
		return rec
	}

	rec := send(strings.Repeat("a", 200000))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chat body: status %d, want 413 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Request too large") {
		t.Errorf("oversized chat body: %q, want the \"Request too large\" error", rec.Body.String())
	}
}

// F12: a proxy that never answers must not block the sync handler.
func TestLo2ServerOllamaSyncTimeout(t *testing.T) {
	// A listener that accepts and then stays silent, so only the client timeout ends the call.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var connMu sync.Mutex
	var conns []net.Conn
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connMu.Lock()
			conns = append(conns, conn)
			connMu.Unlock()
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		connMu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		connMu.Unlock()
	})

	oldURL, oldClient := ProxyURL, ollamaSyncHTTPClient
	ProxyURL = "http://" + ln.Addr().String() + "/models"
	ollamaSyncHTTPClient = &http.Client{Timeout: 100 * time.Millisecond}
	t.Cleanup(func() {
		ProxyURL, ollamaSyncHTTPClient = oldURL, oldClient
	})

	_, cookie := w0NewUser(t, true)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/aimodels/sync", Cookies: []*http.Cookie{cookie}})
	}()
	select {
	case rec := <-done:
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("sync against a dead proxy: status %d, want 303 (body %q)", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adminSyncOllamaModels did not return within 2s against a proxy that never answers")
	}
	select {
	case <-accepted:
	default:
		t.Error("the fake proxy never saw a connection: ProxyURL was not used")
	}
}

// F13: one failing load must not stop the cron loop.
func TestLo2ServerCronLoopSurvivesLoadError(t *testing.T) {
	logs := lo2ServerCaptureLog(t)
	db := w0DB(t)

	var calls atomic.Int64
	cm := &CronJobManager{
		db:          db,
		jobs:        make(map[int64]*CronJob),
		stopChannel: make(chan struct{}),
		reloadEvery: 10 * time.Millisecond,
	}
	cm.loadJobs = func() ([]*models.CronJob, error) {
		if calls.Add(1) <= 2 {
			return nil, errors.New("lo2server: simulated GetAllCronJobs failure")
		}
		return nil, nil // no jobs: nothing is ever executed
	}

	// StartCronManager callers take the MainWG slot that StopCronManager releases.
	db.WG.Add(1)
	cm.StartCronManager()

	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := calls.Load(); n < 3 {
		t.Fatalf("loadJobs called %d times, want at least 3: the loop stopped on the first error", n)
	}
	if !strings.Contains(logs.String(), "Failed to load cron jobs") {
		t.Errorf("load failures were not logged:\n%s", logs.String())
	}

	// StopCronManager must end the loop promptly: it stops calling loadJobs, which it would do
	// every 10ms while it runs. (The MainWG slot is released by StopCronManager's own goroutine;
	// the shared test DB has other slot holders, so it cannot be observed by waiting on db.WG.)
	cm.StopCronManager()
	deadline = time.Now().Add(3 * time.Second)
	for {
		before := calls.Load()
		time.Sleep(100 * time.Millisecond) // 10 reload intervals
		if calls.Load() == before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cron loop still ran %d loads after StopCronManager", calls.Load()-before)
		}
	}
}

// lo2ServerBadBots copies the active bad bot patterns under the lock the middleware uses.
func lo2ServerBadBots() []string {
	config.BadBotsMutex.RLock()
	defer config.BadBotsMutex.RUnlock()
	return append([]string(nil), config.Default_BadBots...)
}

// lo2ServerKeepBadBots restores the BadBots setting and globals after one test.
func lo2ServerKeepBadBots(t *testing.T) {
	t.Helper()
	config.BadBotsMutex.RLock()
	oldBots, oldBlock := config.Default_BadBots, config.BlockBadBots
	config.BadBotsMutex.RUnlock()
	t.Cleanup(func() {
		config.BadBotsMutex.Lock()
		config.Default_BadBots, config.BlockBadBots = oldBots, oldBlock
		config.BadBotsMutex.Unlock()
	})
}

// F14: clearing the setting clears the patterns, like a start with an empty setting.
func TestLo2ServerClearBadBots(t *testing.T) {
	lo2ServerKeepBadBots(t)

	config.UpdateBadBots("lo2serverbot-a,lo2serverbot-b", true)
	if n := len(lo2ServerBadBots()); n != 2 {
		t.Fatalf("Default_BadBots has %d patterns after setting two, want 2", n)
	}

	config.UpdateBadBots("", true)
	if got := lo2ServerBadBots(); len(got) != 0 {
		t.Errorf("Default_BadBots = %q after clearing, want empty", got)
	}
	config.BadBotsMutex.RLock()
	block := config.BlockBadBots
	config.BadBotsMutex.RUnlock()
	if !block {
		t.Error("BlockBadBots = false after clearing the list, want the flag untouched")
	}
}

// F14 through the admin form: the success message claims the change took effect, so the running
// server must really be updated. The dispatch used to store the row without calling UpdateBadBots.
func TestLo2ServerBadBotsSettingApplied(t *testing.T) {
	db := w0DB(t)
	oldValue, err := db.GetConfigValue(config.CFG_KEY_BADBOTS)
	if err != nil {
		t.Fatal(err)
	}
	lo2ServerKeepBadBots(t)
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_BADBOTS, oldValue); err != nil {
			t.Errorf("restoring %s: %v", config.CFG_KEY_BADBOTS, err)
		}
	})

	_, cookie := w0NewUser(t, true)
	post := func(value string) *httptest.ResponseRecorder {
		return w0Do(t, w0Req{
			Method:  http.MethodPost,
			Path:    "/admin/settings",
			Form:    url.Values{"setting": {config.FORM_FIELD_BADBOTS}, config.FORM_FIELD_BADBOTS: {value}},
			Cookies: []*http.Cookie{cookie},
		})
	}

	if rec := post("Lo2ServerBot"); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST bad bots list: status %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if got := lo2ServerBadBots(); len(got) != 1 || got[0] != "lo2serverbot" {
		t.Fatalf("Default_BadBots = %q after saving one pattern, want [lo2serverbot]: the form does not apply the list", got)
	}
	if v, err := db.GetConfigValue(config.CFG_KEY_BADBOTS); err != nil || v != "Lo2ServerBot" {
		t.Errorf("stored %s = %q (err %v), want \"Lo2ServerBot\"", config.CFG_KEY_BADBOTS, v, err)
	}

	if rec := post(""); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST empty bad bots list: status %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if got := lo2ServerBadBots(); len(got) != 0 {
		t.Errorf("Default_BadBots = %q after clearing through the form, want empty", got)
	}
	if v, err := db.GetConfigValue(config.CFG_KEY_BADBOTS); err != nil || v != "" {
		t.Errorf("stored %s = %q (err %v), want empty", config.CFG_KEY_BADBOTS, v, err)
	}
}
