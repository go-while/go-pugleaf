package database

import (
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// lo2FormsUser inserts a user row of this slice and returns it with its ID.
func lo2FormsUser(t *testing.T) *models.User {
	t.Helper()
	db := w0DB(t)
	name := w0Name("lo2forms_user")
	if err := db.InsertUser(&models.User{Username: name, Email: name + "@test.invalid", PasswordHash: "x", DisplayName: name}); err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	u, err := db.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	return u
}

// lo2FormsExpiresRaw returns session_expires_at as stored (text), to detect any rewrite.
func lo2FormsExpiresRaw(t *testing.T, id int64) string {
	t.Helper()
	var v string
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(), "SELECT CAST(session_expires_at AS TEXT) FROM users WHERE id = ?", []interface{}{id}, &v); err != nil {
		t.Fatalf("read session_expires_at: %v", err)
	}
	return v
}

// TestLo2FormsSessionSlideEvery pins F2: the DB expiry moves at most once every
// sessionSlideEvery, so an idle session survives at least SessionTimeout-sessionSlideEvery.
func TestLo2FormsSessionSlideEvery(t *testing.T) {
	db := w0DB(t)
	u := lo2FormsUser(t)
	token, err := db.CreateUserSession(u.ID, "192.0.2.1")
	if err != nil {
		t.Fatalf("CreateUserSession: %v", err)
	}

	setExpiry := func(d time.Duration) {
		t.Helper()
		if _, err := RetryableExec(db.GetMainDB(), "UPDATE users SET session_expires_at = ? WHERE id = ?",
			time.Now().UTC().Add(d), u.ID); err != nil {
			t.Fatalf("set session_expires_at: %v", err)
		}
	}

	// 58 minutes left (> SessionTimeout-sessionSlideEvery): no write.
	setExpiry(SessionTimeout - 2*time.Minute)
	before := lo2FormsExpiresRaw(t, u.ID)
	user, err := db.ValidateUserSession(token)
	if err != nil {
		t.Fatalf("ValidateUserSession (fresh): %v", err)
	}
	if after := lo2FormsExpiresRaw(t, u.ID); after != before {
		t.Errorf("session_expires_at rewritten with %v left: before=%q after=%q", SessionTimeout-2*time.Minute, before, after)
	}
	if user.SessionExpiresAt == nil || time.Until(*user.SessionExpiresAt) > SessionTimeout-time.Minute {
		t.Errorf("returned expiry %v, want the stored one", user.SessionExpiresAt)
	}

	// 50 minutes left (< SessionTimeout-sessionSlideEvery): it slides to a full timeout.
	setExpiry(SessionTimeout - 10*time.Minute)
	before = lo2FormsExpiresRaw(t, u.ID)
	user, err = db.ValidateUserSession(token)
	if err != nil {
		t.Fatalf("ValidateUserSession (stale): %v", err)
	}
	if user.SessionExpiresAt == nil {
		t.Fatal("ValidateUserSession returned a nil expiry")
	}
	if left := time.Until(*user.SessionExpiresAt); left < SessionTimeout-time.Minute || left > SessionTimeout {
		t.Errorf("slid expiry has %v left, want about %v", left, SessionTimeout)
	}
	if after := lo2FormsExpiresRaw(t, u.ID); after == before {
		t.Errorf("session_expires_at not updated in the DB (still %q)", before)
	}
}
