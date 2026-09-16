package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
)

// Tests of the lo-web-handlers slice (plan web-sqlite-leftovers, findings B2, B3, B5, B7, B8).
// They change package globals (ollamaProxyURL, chatCooldown, the AbuseMail config), so none of
// them may run in parallel; every change is restored with t.Cleanup.

// lo1HandFakeProxy points ollamaProxyURL at a test server running h and restores it afterwards.
func lo1HandFakeProxy(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	old := ollamaProxyURL
	ollamaProxyURL = srv.URL
	t.Cleanup(func() {
		ollamaProxyURL = old
		srv.Close()
	})
	return srv
}

// lo1HandNoCooldown disables the per-user chat rate limit for one test. chatCooldown is only ever
// read under rateLimiterMux, so writing it under that lock is race-free.
func lo1HandNoCooldown(t *testing.T) {
	t.Helper()
	rateLimiterMux.Lock()
	old := chatCooldown
	chatCooldown = 0
	rateLimiterMux.Unlock()
	t.Cleanup(func() {
		rateLimiterMux.Lock()
		chatCooldown = old
		rateLimiterMux.Unlock()
	})
}

// lo1HandNewModel creates an active (non-default) AI model and returns its post key.
func lo1HandNewModel(t *testing.T) string {
	t.Helper()
	postKey := w0Name("lo1hand-model")
	if _, err := w0DB(t).CreateAIModel(postKey, "lo1hand-ollama", "Lo1Hand Model", "test model", true, false, 0); err != nil {
		t.Fatalf("CreateAIModel: %v", err)
	}
	return postKey
}

// lo1HandChatSend posts one chat message as JSON. It never calls t.Fatal, so it is safe to call
// from a goroutine.
func lo1HandChatSend(cookie *http.Cookie, model, message string) *httptest.ResponseRecorder {
	body, err := json.Marshal(map[string]string{"message": message, "model": model})
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/aichat/send", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:1234"
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	w0Srv.Router.ServeHTTP(rec, req)
	return rec
}

// lo1HandInFlight reports the inFlight flag of one history entry (false when there is none).
func lo1HandInFlight(userID int64, model string) bool {
	chatCacheMux.RLock()
	defer chatCacheMux.RUnlock()
	entry, ok := chatHistoryCache[chatHistoryKey(userID, model)]
	return ok && entry.inFlight
}

func TestLo1HandChatConcurrentSend(t *testing.T) {
	user, cookie := w0NewUser(t, false)
	model := lo1HandNewModel(t)
	lo1HandNoCooldown(t)

	atProxy := make(chan struct{}, 4)
	release := make(chan struct{})
	lo1HandFakeProxy(t, func(w http.ResponseWriter, r *http.Request) {
		atProxy <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"reply":"pong"}`)
	})

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- lo1HandChatSend(cookie, model, "first message") }()

	select {
	case <-atProxy:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("first send never reached the proxy")
	}

	// The second send finds the entry claimed and is refused without reaching the proxy.
	rec2 := lo1HandChatSend(cookie, model, "second message")
	if rec2.Code != http.StatusTooManyRequests {
		close(release)
		t.Fatalf("second send: status %d, want %d (body %q)", rec2.Code, http.StatusTooManyRequests, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), chatBusyMessage) {
		close(release)
		t.Fatalf("second send: body %q lacks %q", rec2.Body.String(), chatBusyMessage)
	}
	if len(atProxy) != 0 {
		close(release)
		t.Fatalf("the refused send reached the proxy %d times", len(atProxy))
	}

	close(release)
	rec1 := <-first
	if rec1.Code != http.StatusOK {
		t.Fatalf("first send: status %d, want 200 (body %q)", rec1.Code, rec1.Body.String())
	}

	history := getChatHistory(chatHistoryKey(user.ID, model), time.Now())
	if len(history) != 2 {
		t.Fatalf("history has %d messages, want 2 (%+v)", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "first message" {
		t.Errorf("history[0] = %+v, want the first user message", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "pong" {
		t.Errorf("history[1] = %+v, want the assistant reply", history[1])
	}
	if lo1HandInFlight(user.ID, model) {
		t.Error("entry still marked inFlight after both sends returned")
	}
}

func TestLo1HandChatClearDuringSend(t *testing.T) {
	user, cookie := w0NewUser(t, false)
	model := lo1HandNewModel(t)
	lo1HandNoCooldown(t)

	atProxy := make(chan struct{}, 4)
	release := make(chan struct{})
	lo1HandFakeProxy(t, func(w http.ResponseWriter, r *http.Request) {
		atProxy <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"reply":"late reply"}`)
	})

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- lo1HandChatSend(cookie, model, "hello") }()
	select {
	case <-atProxy:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("send never reached the proxy")
	}

	// Clearing while the proxy works must not be undone by the reply that arrives afterwards.
	clear := w0Do(t, w0Req{Method: http.MethodPost, Path: "/aichat/clear/" + url.PathEscape(model), Cookies: []*http.Cookie{cookie}})
	if clear.Code != http.StatusOK {
		close(release)
		t.Fatalf("clear: status %d, want 200 (body %q)", clear.Code, clear.Body.String())
	}

	close(release)
	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("send: status %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "late reply") {
		t.Errorf("send: body %q does not carry the reply", rec.Body.String())
	}
	if history := getChatHistory(chatHistoryKey(user.ID, model), time.Now()); len(history) != 0 {
		t.Errorf("history has %d messages after the clear, want 0 (%+v)", len(history), history)
	}
	if lo1HandInFlight(user.ID, model) {
		t.Error("entry still marked inFlight after the send returned")
	}

	// A cleared entry stays in the map with msgs nil: the history route must still answer with
	// an empty JSON array, not null.
	hist := w0Do(t, w0Req{Method: http.MethodPost, Path: "/aichat/history/" + url.PathEscape(model), Cookies: []*http.Cookie{cookie}})
	if hist.Code != http.StatusOK {
		t.Fatalf("history: status %d, want 200 (body %q)", hist.Code, hist.Body.String())
	}
	if !strings.Contains(hist.Body.String(), `"history":[]`) {
		t.Errorf("history body %q, want an empty array", hist.Body.String())
	}
}

func TestLo1HandChatStaleClaimReleased(t *testing.T) {
	now := time.Now()
	stuck := chatHistoryKey(-1, w0Name("lo1hand-stuck"))
	fresh := chatHistoryKey(-1, w0Name("lo1hand-fresh"))
	chatCacheMux.Lock()
	chatHistoryCache[stuck] = &chatEntry{inFlight: true, claimedAt: now.Add(-2 * chatClaimStuckAfter), lastUsed: now}
	chatHistoryCache[fresh] = &chatEntry{inFlight: true, claimedAt: now, lastUsed: now}
	chatCacheMux.Unlock()
	t.Cleanup(func() {
		chatCacheMux.Lock()
		delete(chatHistoryCache, stuck)
		delete(chatHistoryCache, fresh)
		chatCacheMux.Unlock()
	})

	sweepChatCaches(now)

	chatCacheMux.RLock()
	stuckEntry, stuckOK := chatHistoryCache[stuck]
	freshEntry, freshOK := chatHistoryCache[fresh]
	var stuckInFlight, freshInFlight bool
	if stuckOK {
		stuckInFlight = stuckEntry.inFlight
	}
	if freshOK {
		freshInFlight = freshEntry.inFlight
	}
	chatCacheMux.RUnlock()

	if !stuckOK || !freshOK {
		t.Fatalf("entries removed although both were used just now (stuck=%v fresh=%v)", stuckOK, freshOK)
	}
	if stuckInFlight {
		t.Error("a claim older than chatClaimStuckAfter was not released, so the user stays at 429")
	}
	if !freshInFlight {
		t.Error("a claim made just now was released")
	}
}

func TestLo1HandChatSequential(t *testing.T) {
	user, cookie := w0NewUser(t, false)
	model := lo1HandNewModel(t)
	lo1HandNoCooldown(t)
	lo1HandFakeProxy(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"reply":"ok"}`)
	})

	for i, msg := range []string{"one", "two"} {
		if rec := lo1HandChatSend(cookie, model, msg); rec.Code != http.StatusOK {
			t.Fatalf("send %d: status %d, want 200 (body %q)", i, rec.Code, rec.Body.String())
		}
	}
	history := getChatHistory(chatHistoryKey(user.ID, model), time.Now())
	if len(history) != 4 {
		t.Fatalf("history has %d messages, want 4 (%+v)", len(history), history)
	}
	want := []string{"one", "ok", "two", "ok"}
	for i, w := range want {
		if history[i].Content != w {
			t.Errorf("history[%d].Content = %q, want %q", i, history[i].Content, w)
		}
	}
}

// lo1HandDrainPostQueue empties models.PostQueueChannel.
//
// TestLo1HandPostQueueFullReleases owns that channel for its whole duration: it fills it to
// capacity and drains it again. That is only safe because the channel is a package global with
// a single consumer (processor.PostQueueWorker, which cmd/web starts but this test binary never
// does) and because no test in this package runs in parallel. A test that starts a post queue
// worker, or a t.Parallel anywhere near a submit, breaks this.
func lo1HandDrainPostQueue() {
	for {
		select {
		case <-models.PostQueueChannel:
		default:
			return
		}
	}
}

func TestLo1HandPostQueueFullReleases(t *testing.T) {
	// This test owns models.PostQueueChannel (see lo1HandDrainPostQueue): drain whatever an
	// earlier test left behind, and leave it empty again.
	lo1HandDrainPostQueue()
	t.Cleanup(lo1HandDrainPostQueue)

	db := w0DB(t)
	oldAbuse, err := db.GetConfigValue(config.CFG_KEY_ABUSEMAIL)
	if err != nil {
		t.Fatalf("GetConfigValue(%s): %v", config.CFG_KEY_ABUSEMAIL, err)
	}
	if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, "abuse@lo1hand.invalid"); err != nil {
		t.Fatalf("SetConfigValue(%s): %v", config.CFG_KEY_ABUSEMAIL, err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, oldAbuse); err != nil {
			t.Errorf("restore %s: %v", config.CFG_KEY_ABUSEMAIL, err)
		}
	})

	user, cookie := w0NewUser(t, false)
	group := w0NewGroup(t, true)
	before, err := db.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}

	// Fill the queue, so the submit below takes the "Server is busy" branch.
	filled := 0
	for len(models.PostQueueChannel) < cap(models.PostQueueChannel) {
		select {
		case models.PostQueueChannel <- &models.Article{MessageID: fmt.Sprintf("<lo1hand.%d@test.invalid>", filled)}:
			filled++
		default:
			t.Fatal("post queue channel refused an article although it is not full")
		}
	}

	form := url.Values{"newsgroups": {group}, "subject": {"lo1hand busy"}, "body": {"hello"}}
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/SitePostSubmit", Form: form, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: status %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Server is busy") {
		t.Fatalf("submit: body lacks \"Server is busy\": %q", lo1HandShorten(rec.Body.String()))
	}

	after, err := db.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if after.PostCount != before.PostCount {
		t.Errorf("post_count = %d, want the unchanged %d", after.PostCount, before.PostCount)
	}
	// The post is given back, but a short retry window stays, so a full queue cannot be
	// hammered: the user waits seconds, not the full WebPostingBackOff.
	backoff := int64(WebPostingBackOff.Seconds())
	waitLeft := after.LastPostUnix + backoff - time.Now().Unix()
	if waitLeft > 10 {
		t.Fatalf("user must wait %ds after a full queue, want at most 10s of %ds", waitLeft, backoff)
	}
	if after.LastPostUnix <= before.LastPostUnix {
		t.Errorf("lastpost_unix = %d, want a short retry window above the previous %d",
			after.LastPostUnix, before.LastPostUnix)
	}

	// Once that short window has passed the next post is accepted, with the queue drained.
	lo1HandDrainPostQueue()
	if waitLeft > 0 {
		time.Sleep(time.Duration(waitLeft+1) * time.Second)
	}
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/SitePostSubmit", Form: form, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("second submit: status %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "You can only post once every") {
		t.Fatalf("second submit hit the back-off: %q", lo1HandShorten(body))
	}
	if !strings.Contains(body, "queued for posting") {
		t.Fatalf("second submit was not queued: %q", lo1HandShorten(body))
	}
}

// lo1HandShorten keeps failure messages readable when a whole HTML page is quoted.
func lo1HandShorten(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

func TestLo1HandReleaseWebPostGuard(t *testing.T) {
	db := w0DB(t)
	user, _ := w0NewUser(t, false)
	before, err := db.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}

	now := time.Now().Unix()
	ok, err := db.TryReserveWebPost(user.ID, now, 42)
	if err != nil || !ok {
		t.Fatalf("TryReserveWebPost = %v, %v; want true, nil", ok, err)
	}

	// A wrong reservedAt must not touch the row.
	released, err := db.ReleaseWebPost(user.ID, now+1, before.LastPostUnix)
	if err != nil {
		t.Fatalf("ReleaseWebPost: %v", err)
	}
	if released {
		t.Fatal("ReleaseWebPost with a wrong reservedAt reported success")
	}
	mid, err := db.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if mid.PostCount != before.PostCount+1 || mid.LastPostUnix != now {
		t.Fatalf("row changed by the guarded release: post_count=%d lastpost_unix=%d, want %d and %d",
			mid.PostCount, mid.LastPostUnix, before.PostCount+1, now)
	}

	// The matching reservedAt undoes the reservation exactly once.
	released, err = db.ReleaseWebPost(user.ID, now, before.LastPostUnix)
	if err != nil || !released {
		t.Fatalf("ReleaseWebPost = %v, %v; want true, nil", released, err)
	}
	after, err := db.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if after.PostCount != before.PostCount || after.LastPostUnix != before.LastPostUnix {
		t.Fatalf("after release: post_count=%d lastpost_unix=%d, want %d and %d",
			after.PostCount, after.LastPostUnix, before.PostCount, before.LastPostUnix)
	}
	released, err = db.ReleaseWebPost(user.ID, now, before.LastPostUnix)
	if err != nil {
		t.Fatalf("ReleaseWebPost: %v", err)
	}
	if released {
		t.Error("a second ReleaseWebPost for the same reservation reported success")
	}
}

func TestLo1HandTemplateKeyFuncMaps(t *testing.T) {
	if devTemplates() {
		t.Skip("PUGLEAF_DEV_TEMPLATES=1 disables the template cache")
	}
	files := templatePaths([]string{"base.html", "error.html"})
	fm1 := template.FuncMap{"lo1HandMark": func() string { return "one" }}
	fm2 := template.FuncMap{"lo1HandMark": func() string { return "two" }}

	t1, err := loadTemplates("lo1hand.key", fm1, files...)
	if err != nil {
		t.Fatalf("loadTemplates fm1: %v", err)
	}
	t2, err := loadTemplates("lo1hand.key", fm2, files...)
	if err != nil {
		t.Fatalf("loadTemplates fm2: %v", err)
	}
	if t1 == t2 {
		t.Error("two different FuncMaps share one cached template set")
	}
	again, err := loadTemplates("lo1hand.key", fm1, files...)
	if err != nil {
		t.Fatalf("loadTemplates fm1 again: %v", err)
	}
	if again != t1 {
		t.Error("the same FuncMap did not hit the cache")
	}
	plain, err := loadTemplates("lo1hand.key", nil, files...)
	if err != nil {
		t.Fatalf("loadTemplates nil funcs: %v", err)
	}
	if plain == t1 || plain == t2 {
		t.Error("a set without funcs shares an entry with one that has funcs")
	}
	if key := tmplCacheKey("lo1hand.key", fm1, files); key == tmplCacheKey("lo1hand.key", fm2, files) {
		t.Errorf("tmplCacheKey is equal for two FuncMaps: %q", key)
	}
}

func TestLo1HandTemplateErrorHidesDetail(t *testing.T) {
	missing := "lo1hand-missing-template.html"
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/lo1hand", nil)
	c.Request.RemoteAddr = "192.0.2.10:1234"

	w0Srv.renderPage(c, http.StatusOK, gin.H{}, missing)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{missing, templateDir, "no such file"} {
		if strings.Contains(body, leak) {
			t.Errorf("body leaks %q: %s", leak, lo1HandShorten(body))
		}
	}
	if !strings.Contains(body, publicErrorDetail) && !strings.Contains(body, "Template error") {
		t.Errorf("body carries neither the public detail nor the title: %s", lo1HandShorten(body))
	}
}
