package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/database"
)

const w1AuthGenericMsg = "Invalid username/email or password"

// w1AuthNoDelay disables the login delay for one test (no t.Parallel: it is a package var).
func w1AuthNoDelay(t *testing.T) {
	t.Helper()
	old := loginDelay
	loginDelay = 0
	t.Cleanup(func() { loginDelay = old })
}

// w1AuthLogin posts the login form.
func w1AuthLogin(t *testing.T, username, password string, extra url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	for k, v := range extra {
		form[k] = v
	}
	return w0Do(t, w0Req{Method: http.MethodPost, Path: "/login", Form: form})
}

// w1AuthSessionCookie returns the non-empty session_id cookie set by rec, or nil.
func w1AuthSessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "session_id" && ck.Value != "" {
			return ck
		}
	}
	return nil
}

// w1AuthProfileCode returns the status of GET /profile with cookie.
func w1AuthProfileCode(t *testing.T, cookie *http.Cookie) int {
	t.Helper()
	return w0Do(t, w0Req{Path: "/profile", Cookies: []*http.Cookie{cookie}}).Code
}

func TestW1AuthSafeRedirect(t *testing.T) {
	cases := map[string]string{
		"/x":                            "/x",
		"/groups?page=2":                "/groups?page=2",
		"":                              "/",
		"//evil":                        "/",
		"/\\evil":                       "/",
		"https://evil":                  "/",
		"javascript:x":                  "/",
		"evil.example/x":                "/",
		"/x\r\nSet-Cookie: a=b":         "/",
		"/x\n":                          "/",
		"/" + strings.Repeat("a", 2048): "/",
	}
	for in, want := range cases {
		if got := safeRedirect(in); got != want {
			t.Errorf("safeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestW1AuthValidatePassword(t *testing.T) {
	if err := validatePassword(strings.Repeat("a", 72)); err != nil {
		t.Errorf("72 bytes rejected: %v", err)
	}
	err := validatePassword(strings.Repeat("a", 73))
	if err == nil || !strings.Contains(err.Error(), "72 bytes") {
		t.Errorf("73 bytes: err=%v, want the 72 bytes message", err)
	}
	// 24 runes of 3 bytes each = 72 bytes; one more rune exceeds the limit
	if err := validatePassword(strings.Repeat("€", 25)); err == nil {
		t.Errorf("75-byte multibyte password accepted")
	}
	if err := validatePassword("short"); err == nil {
		t.Errorf("short password accepted")
	}
}

func TestW1AuthValidateDisplayName(t *testing.T) {
	ok := []string{"", "Alice", "Jörg Müller", strings.Repeat("ä", 64), "O'Brien (home)"}
	bad := []string{"Evil\r\nControl: cancel", "a\tb", "a\x00b", "<x@y>", "a\"b", "a>b", strings.Repeat("a", 65), "a\x7fb", "bad\xffutf8"}
	for _, s := range ok {
		if err := validateDisplayName(s); err != nil {
			t.Errorf("validateDisplayName(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range bad {
		if err := validateDisplayName(s); err == nil {
			t.Errorf("validateDisplayName(%q) = nil, want error", s)
		}
	}
}

func TestW1AuthRegisterLogsIn(t *testing.T) {
	db := w0DB(t)
	old, err := db.GetConfigValue("registration_enabled")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConfigValue("registration_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue("registration_enabled", old); err != nil {
			t.Errorf("restore registration_enabled: %v", err)
		}
	})

	name := fmt.Sprintf("w1auth_reg_%d", w0Seq.Add(1))
	pw := "w1auth-register-pw-0123"
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/register", Form: url.Values{
		"username": {name}, "email": {name + "@test.invalid"}, "password1": {pw}, "password2": {pw},
	}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /register = %d, want 303; body: %.300s", rec.Code, rec.Body.String())
	}
	cookie := w1AuthSessionCookie(rec)
	if cookie == nil {
		t.Fatalf("POST /register set no session cookie")
	}
	if code := w1AuthProfileCode(t, cookie); code != http.StatusOK {
		t.Fatalf("GET /profile after register = %d, want 200", code)
	}

	// A too long password is rejected with a clear message
	long := strings.Repeat("a", 80)
	name2 := fmt.Sprintf("w1auth_reg_%d", w0Seq.Add(1))
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/register", Form: url.Values{
		"username": {name2}, "email": {name2 + "@test.invalid"}, "password1": {long}, "password2": {long},
	}})
	if !strings.Contains(rec.Body.String(), "72 bytes") {
		t.Fatalf("80-byte password: body lacks the 72 bytes message (status %d)", rec.Code)
	}
}

func TestW1AuthLoginByEmail(t *testing.T) {
	w1AuthNoDelay(t)
	u, _ := w0NewUser(t, false)
	rec := w1AuthLogin(t, u.Email, w0Password, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login by email = %d, want 303; body: %.300s", rec.Code, rec.Body.String())
	}
	cookie := w1AuthSessionCookie(rec)
	if cookie == nil {
		t.Fatalf("login by email set no session cookie")
	}
	if code := w1AuthProfileCode(t, cookie); code != http.StatusOK {
		t.Fatalf("GET /profile after email login = %d, want 200", code)
	}
}

func TestW1AuthNoUsernameEnumeration(t *testing.T) {
	w1AuthNoDelay(t)
	u, _ := w0NewUser(t, false)
	unknown := w1AuthLogin(t, "w1auth_nosuchuser", "wrong-password", nil)
	unknownMail := w1AuthLogin(t, "w1auth_nosuchuser@test.invalid", "wrong-password", nil)
	wrong := w1AuthLogin(t, u.Username, "wrong-password", nil)
	for label, rec := range map[string]*httptest.ResponseRecorder{"unknown": unknown, "unknown email": unknownMail, "wrong password": wrong} {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", label, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, w1AuthGenericMsg) || strings.Contains(body, "Login error") {
			t.Errorf("%s: body lacks the generic message or shows 'Login error'", label)
		}
		if w1AuthSessionCookie(rec) != nil {
			t.Errorf("%s: session cookie set", label)
		}
	}
}

func TestW1AuthLockoutByID(t *testing.T) {
	w1AuthNoDelay(t)
	u, _ := w0NewUser(t, false)
	// Failures by email count against the same user ID as failures by username.
	for i := 0; i < database.MaxLoginAttempts; i++ {
		login := u.Username
		if i%2 == 0 {
			login = u.Email
		}
		if rec := w1AuthLogin(t, login, "wrong-password", nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("failure %d: status %d, want 400", i, rec.Code)
		}
	}
	rec := w1AuthLogin(t, u.Username, w0Password, nil)
	if rec.Code == http.StatusSeeOther || w1AuthSessionCookie(rec) != nil {
		t.Fatalf("correct password accepted while locked out (status %d)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), w1AuthGenericMsg) {
		t.Fatalf("locked out login lacks the generic message")
	}
}

func TestW1AuthDisabledUser(t *testing.T) {
	w1AuthNoDelay(t)
	db := w0DB(t)

	// Disabled before login: rejected with the generic message
	u, _ := w0NewUser(t, false)
	if err := db.UpdateUserStatus(u.ID, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	rec := w1AuthLogin(t, u.Username, w0Password, nil)
	if rec.Code != http.StatusBadRequest || w1AuthSessionCookie(rec) != nil {
		t.Fatalf("disabled user login: status %d cookie=%v, want 400 without cookie", rec.Code, w1AuthSessionCookie(rec) != nil)
	}
	if !strings.Contains(rec.Body.String(), w1AuthGenericMsg) {
		t.Fatalf("disabled user login lacks the generic message")
	}

	// Disabled after login: the existing session is rejected
	u2, cookie := w0NewUser(t, false)
	if code := w1AuthProfileCode(t, cookie); code != http.StatusOK {
		t.Fatalf("GET /profile before disabling = %d, want 200", code)
	}
	if err := db.UpdateUserStatus(u2.ID, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if code := w1AuthProfileCode(t, cookie); code != http.StatusSeeOther {
		t.Fatalf("GET /profile after disabling = %d, want 303 (redirect to login)", code)
	}
}

func TestW1AuthLogoutCrossSite(t *testing.T) {
	_, cookie := w0NewUser(t, false)
	for _, site := range []string{"cross-site", "same-site"} {
		rec := w0Do(t, w0Req{Path: "/logout", Cookies: []*http.Cookie{cookie}, Header: map[string]string{"Sec-Fetch-Site": site}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
			t.Fatalf("%s logout: status %d location %q, want 303 to /", site, rec.Code, rec.Header().Get("Location"))
		}
		if code := w1AuthProfileCode(t, cookie); code != http.StatusOK {
			t.Fatalf("GET /profile after %s logout = %d, want 200", site, code)
		}
	}

	rec := w0Do(t, w0Req{Path: "/logout", Cookies: []*http.Cookie{cookie}, Header: map[string]string{"Sec-Fetch-Site": "same-origin"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("same-origin logout: status %d, want 303", rec.Code)
	}
	if code := w1AuthProfileCode(t, cookie); code != http.StatusSeeOther {
		t.Fatalf("GET /profile after same-origin logout = %d, want 303", code)
	}
}

func TestW1AuthLoginOpenRedirect(t *testing.T) {
	w1AuthNoDelay(t)
	u, _ := w0NewUser(t, false)
	cases := map[string]string{
		"https://evil.example/x": "/",
		"//evil.example/x":       "/",
		"/\\evil.example":        "/",
		"/groups":                "/groups",
	}
	for redirect, want := range cases {
		rec := w1AuthLogin(t, u.Username, w0Password, url.Values{"redirect": {redirect}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("redirect=%q: status %d, want 303", redirect, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != want {
			t.Errorf("redirect=%q: Location %q, want %q", redirect, loc, want)
		}
	}

	// The login page does not echo an external redirect target into the form
	rec := w0Do(t, w0Req{Path: "/login", Form: url.Values{"redirect": {"https://evil.example/x"}}})
	if strings.Contains(rec.Body.String(), "evil.example") {
		t.Errorf("GET /login echoes the external redirect target")
	}
}

func TestW1AuthProfileDisplayNameRejected(t *testing.T) {
	u, cookie := w0NewUser(t, false)
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/profile", Cookies: []*http.Cookie{cookie}, Form: url.Values{
		"email": {u.Email}, "display_name": {"Evil\r\nControl: cancel <x@y>"}, "current_password": {w0Password},
	}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /profile = %d, want 303", rec.Code)
	}
	got, err := w0DB(t).GetUserByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != u.DisplayName {
		t.Fatalf("display name changed to %q", got.DisplayName)
	}

	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/profile", Cookies: []*http.Cookie{cookie}, Form: url.Values{
		"email": {u.Email}, "display_name": {"Plain Name"}, "current_password": {w0Password},
	}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /profile (valid) = %d, want 303", rec.Code)
	}
	if got, err = w0DB(t).GetUserByID(u.ID); err != nil || got.DisplayName != "Plain Name" {
		t.Fatalf("valid display name not stored: %v %+v", err, got)
	}
}

func TestW1AuthRequestCache(t *testing.T) {
	admin, adminCookie := w0NewUser(t, true)
	_, userCookie := w0NewUser(t, false)
	_ = admin

	newCtx := func(cookie *http.Cookie) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		if cookie != nil {
			c.Request.AddCookie(cookie)
		}
		return c
	}

	c := newCtx(adminCookie)
	s1 := w0Srv.getWebSession(c)
	if s1 == nil {
		t.Fatal("no session for a valid cookie")
	}
	if !w0Srv.isAdminRequest(c) {
		t.Fatal("admin not detected")
	}
	// Invalidate in the DB: the memoized result of this request stays
	if err := w0DB(t).InvalidateUserSession(s1.UserID); err != nil {
		t.Fatal(err)
	}
	if s2 := w0Srv.getWebSession(c); s2 != s1 {
		t.Fatalf("getWebSession not memoized: %p != %p", s2, s1)
	}
	if !w0Srv.isAdminRequest(c) {
		t.Fatal("isAdminRequest not memoized")
	}
	w0Srv.clearRequestSession(c)
	if s3 := w0Srv.getWebSession(c); s3 != nil {
		t.Fatalf("session still valid after clearRequestSession and invalidation")
	}
	if w0Srv.isAdminRequest(c) {
		t.Fatal("isAdminRequest true without a session")
	}

	c = newCtx(userCookie)
	if w0Srv.getWebSession(c) == nil || w0Srv.isAdminRequest(c) {
		t.Fatal("plain user: want a session and no admin")
	}
	c = newCtx(nil)
	if w0Srv.getWebSession(c) != nil || w0Srv.getWebSession(c) != nil || w0Srv.isAdminRequest(c) {
		t.Fatal("anonymous: want no session and no admin")
	}
}

func TestW1AuthFlashPrune(t *testing.T) {
	flashMessagesMu.Lock()
	for i := 0; i < flashMaxSessions+10; i++ {
		id := fmt.Sprintf("w1auth-old-%d", i)
		flashMessages[id] = map[string]string{"error": "x"}
		flashSetAt[id] = time.Now().Add(-time.Hour)
	}
	flashMessagesMu.Unlock()

	SetFlashError("w1auth-new", "hello")

	flashMessagesMu.Lock()
	_, oldLeft := flashMessages["w1auth-old-0"]
	n := len(flashMessages)
	flashMessagesMu.Unlock()
	if oldLeft || n > flashMaxSessions {
		t.Fatalf("old flash messages not pruned: len=%d oldLeft=%v", n, oldLeft)
	}
	if _, msg := GetAndClearFlash("w1auth-new", "error"); msg != "hello" {
		t.Fatalf("new flash message lost: %q", msg)
	}
}
