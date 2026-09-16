package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// lo2FormsSetExpiry writes session_expires_at of a user directly.
func lo2FormsSetExpiry(t *testing.T, userID int64, d time.Duration) {
	t.Helper()
	if _, err := database.RetryableExec(w0DB(t).GetMainDB(),
		"UPDATE users SET session_expires_at = ? WHERE id = ?", time.Now().UTC().Add(d), userID); err != nil {
		t.Fatalf("set session_expires_at: %v", err)
	}
}

// lo2FormsSetDisplayName writes display_name directly, bypassing the validation in
// UpdateUserDisplayName: this is how a legacy name got into the column.
func lo2FormsSetDisplayName(t *testing.T, userID int64, name string) {
	t.Helper()
	if _, err := database.RetryableExec(w0DB(t).GetMainDB(),
		"UPDATE users SET display_name = ? WHERE id = ?", name, userID); err != nil {
		t.Fatalf("set display_name: %v", err)
	}
}

func lo2FormsUser(t *testing.T, userID int64) *models.User {
	t.Helper()
	u, err := w0DB(t).GetUserByID(userID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	return u
}

// TestLo2FormsCookieFollowsExpiry pins F2: the cookie Max-Age follows the server-side
// expiry instead of always claiming a full SessionTimeout.
func TestLo2FormsCookieFollowsExpiry(t *testing.T) {
	user, cookie := w0NewUser(t, false)
	lo2FormsSetExpiry(t, user.ID, 58*time.Minute)

	rec := w0Do(t, w0Req{Path: "/profile", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /profile = %d, want 200", rec.Code)
	}
	var got *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "session_id" {
			got = ck
		}
	}
	if got == nil {
		t.Fatal("no session_id cookie in the response")
	}
	if got.Value != cookie.Value {
		t.Errorf("cookie value changed")
	}
	if got.MaxAge < 3400 || got.MaxAge > 3480 {
		t.Errorf("Max-Age = %d, want 3400..3480 (the remaining 58 minutes)", got.MaxAge)
	}

	// A fresh login keeps the full timeout.
	full := sessionCookieMaxAge(time.Now().Add(2 * database.SessionTimeout))
	if want := int(database.SessionTimeout.Seconds()); full != want {
		t.Errorf("sessionCookieMaxAge(far future) = %d, want the %d clamp", full, want)
	}
	if past := sessionCookieMaxAge(time.Now().Add(-time.Hour)); past != 1 {
		t.Errorf("sessionCookieMaxAge(past) = %d, want 1", past)
	}
}

// TestLo2FormsLegacyDisplayNameAllowsEmailChange pins F5: a stored legacy display name
// that the form re-sends unchanged must not block an email change.
func TestLo2FormsLegacyDisplayNameAllowsEmailChange(t *testing.T) {
	user, cookie := w0NewUser(t, false)
	legacy := "Jane <jane@lo2forms.invalid>"
	lo2FormsSetDisplayName(t, user.ID, legacy)

	newEmail := user.Username + ".changed@test.invalid"
	form := url.Values{
		"email":            {newEmail},
		"display_name":     {legacy},
		"current_password": {w0Password},
	}
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/profile", Form: form, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /profile = %d, want 303", rec.Code)
	}
	after := lo2FormsUser(t, user.ID)
	if after.Email != newEmail {
		t.Errorf("email = %q, want %q (the legacy display name blocked the change)", after.Email, newEmail)
	}
	if after.DisplayName != legacy {
		t.Errorf("display_name = %q, want the unchanged %q", after.DisplayName, legacy)
	}
}

// TestLo2FormsProfileValidatesBeforeWriting pins F5/F6: a rejected field must not leave a
// partial update behind.
func TestLo2FormsProfileValidatesBeforeWriting(t *testing.T) {
	user, cookie := w0NewUser(t, false)
	before := lo2FormsUser(t, user.ID)

	post := func(form url.Values) {
		t.Helper()
		rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/profile", Form: form, Cookies: []*http.Cookie{cookie}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("POST /profile = %d, want 303", rec.Code)
		}
	}

	// A new email plus mismatched new passwords: nothing is written.
	post(url.Values{
		"email":            {user.Username + ".pw@test.invalid"},
		"display_name":     {before.DisplayName},
		"current_password": {w0Password},
		"new_password":     {"lo2forms-newpassword-1"},
		"confirm_password": {"lo2forms-newpassword-2"},
	})
	after := lo2FormsUser(t, user.ID)
	if after.Email != before.Email {
		t.Errorf("email = %q after a rejected password change, want the unchanged %q", after.Email, before.Email)
	}
	if after.PasswordHash != before.PasswordHash {
		t.Error("password_hash changed although the new passwords did not match")
	}

	// A new email plus a display name of 65 runes: nothing is written.
	post(url.Values{
		"email":            {user.Username + ".name@test.invalid"},
		"display_name":     {strings.Repeat("ä", 65)},
		"current_password": {w0Password},
	})
	after = lo2FormsUser(t, user.ID)
	if after.Email != before.Email {
		t.Errorf("email = %q after a rejected display name, want the unchanged %q", after.Email, before.Email)
	}
	if after.DisplayName != before.DisplayName {
		t.Errorf("display_name = %q, want the unchanged %q", after.DisplayName, before.DisplayName)
	}

	// A valid name is still accepted, together with the email. It is kept ASCII: the byte
	// limit inside UpdateUserDisplayName becomes a rune limit in slice lo2-web-pages
	// (F6, DB half), and this slice must not depend on that.
	name64 := strings.Repeat("a", 64)
	email := user.Username + ".ok@test.invalid"
	post(url.Values{
		"email":            {email},
		"display_name":     {name64},
		"current_password": {w0Password},
	})
	after = lo2FormsUser(t, user.ID)
	if after.Email != email || after.DisplayName != name64 {
		t.Errorf("valid update not applied: email=%q display_name has %d runes", after.Email, len([]rune(after.DisplayName)))
	}
}

// TestLo2FormsNoMisleadingNewsgroupError pins F8: an unrelated error must not drag
// "No valid newsgroups specified" along.
func TestLo2FormsNoMisleadingNewsgroupError(t *testing.T) {
	db := w0DB(t)
	oldAbuse, err := db.GetConfigValue(config.CFG_KEY_ABUSEMAIL)
	if err != nil {
		t.Fatalf("GetConfigValue(%s): %v", config.CFG_KEY_ABUSEMAIL, err)
	}
	if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, "abuse@lo2forms.invalid"); err != nil {
		t.Fatalf("SetConfigValue(%s): %v", config.CFG_KEY_ABUSEMAIL, err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, oldAbuse); err != nil {
			t.Errorf("restore %s: %v", config.CFG_KEY_ABUSEMAIL, err)
		}
	})

	_, cookie := w0NewUser(t, false)
	group := w0NewGroup(t, true)
	form := url.Values{
		"newsgroups": {group},
		"subject":    {"Re: lo2forms"},
		"body":       {"hello"},
		"reply_to":   {"1"},
		"message_id": {"<a@b>\r\nControl: cancel <x@y>"},
	}
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/SitePostSubmit", Form: form, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /SitePostSubmit = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Invalid reply message-id") {
		t.Errorf("body lacks \"Invalid reply message-id\"")
	}
	if strings.Contains(body, "No valid newsgroups specified") {
		t.Errorf("body still shows \"No valid newsgroups specified\" next to the message-id error")
	}
}

// TestLo2FormsLegacyReplyMessageID pins F7 end to end: a legacy id without '@' is
// accepted as a reply target, a control byte in it is not.
func TestLo2FormsLegacyReplyMessageID(t *testing.T) {
	cases := []struct {
		msgID string
		valid bool
	}{
		{"<bnews.x.1>", true},
		{"<a@b@c>", true},
		{"<a\x0bb@c>", false},
		{"<a\x7f@c>", false},
		{"<ä@c>", false},
		{"<a b@c>", false},
	}
	for _, tc := range cases {
		err := validatePostHeaders("Re: x", tc.msgID, true)
		if tc.valid && err != nil {
			t.Errorf("validatePostHeaders(%q) = %v, want accepted", tc.msgID, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("validatePostHeaders(%q) accepted, want rejected", tc.msgID)
		}
	}
	if err := validatePostHeaders("Re: x", "<"+strings.Repeat("a", 249)+">", true); err == nil {
		t.Error("a 252-byte message-id was accepted")
	}
}
