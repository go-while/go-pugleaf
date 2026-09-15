package database

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// w1AuthUser inserts a user row and returns it with its ID.
func w1AuthUser(t *testing.T) *models.User {
	t.Helper()
	db := w0DB(t)
	name := w0Name("w1auth_user")
	if err := db.InsertUser(&models.User{Username: name, Email: name + "@test.invalid", PasswordHash: "x", DisplayName: name}); err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	u, err := db.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	return u
}

// w1AuthExpiresRaw returns session_expires_at as stored (text), to detect any rewrite.
func w1AuthExpiresRaw(t *testing.T, id int64) string {
	t.Helper()
	var v string
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(), "SELECT CAST(session_expires_at AS TEXT) FROM users WHERE id = ?", []interface{}{id}, &v); err != nil {
		t.Fatalf("read session_expires_at: %v", err)
	}
	return v
}

func TestW1AuthValidateUserSessionThrottledSlide(t *testing.T) {
	db := w0DB(t)
	u := w1AuthUser(t)
	sid, err := db.CreateUserSession(u.ID, "192.0.2.1")
	if err != nil {
		t.Fatalf("CreateUserSession: %v", err)
	}
	if _, err := db.ValidateUserSession(sid); err != nil {
		t.Fatalf("ValidateUserSession #1: %v", err)
	}
	before := w1AuthExpiresRaw(t, u.ID)
	if _, err := db.ValidateUserSession(sid); err != nil {
		t.Fatalf("ValidateUserSession #2: %v", err)
	}
	if after := w1AuthExpiresRaw(t, u.ID); after != before {
		t.Fatalf("session_expires_at rewritten on a fresh session: before=%q after=%q", before, after)
	}

	// Less than half of SessionTimeout left: the session slides.
	near := time.Now().UTC().Add(SessionTimeout / 4)
	if _, err := RetryableExec(db.GetMainDB(), "UPDATE users SET session_expires_at = ? WHERE id = ?", near, u.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.ValidateUserSession(sid)
	if err != nil {
		t.Fatalf("ValidateUserSession #3: %v", err)
	}
	if got.SessionExpiresAt == nil || time.Until(*got.SessionExpiresAt) < SessionTimeout*3/4 {
		t.Fatalf("session did not slide: expires=%v", got.SessionExpiresAt)
	}
	if after := w1AuthExpiresRaw(t, u.ID); after == before {
		t.Fatalf("session_expires_at not updated in the DB")
	}
}

func TestW1AuthValidateUserSessionDisabled(t *testing.T) {
	db := w0DB(t)
	u := w1AuthUser(t)
	sid, err := db.CreateUserSession(u.ID, "192.0.2.1")
	if err != nil {
		t.Fatalf("CreateUserSession: %v", err)
	}
	if _, err := db.ValidateUserSession(sid); err != nil {
		t.Fatalf("ValidateUserSession: %v", err)
	}
	if err := db.UpdateUserStatus(u.ID, 0, 1, 0); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if _, err := db.ValidateUserSession(sid); err == nil {
		t.Fatalf("session of a disabled user is still valid")
	}
}

func TestW1AuthLoginAttemptsByID(t *testing.T) {
	db := w0DB(t)
	u := w1AuthUser(t)
	for i := 0; i < MaxLoginAttempts; i++ {
		locked, err := db.IsUserLockedOutByID(u.ID)
		if err != nil {
			t.Fatalf("IsUserLockedOutByID: %v", err)
		}
		if locked {
			t.Fatalf("locked out after %d failures, want %d", i, MaxLoginAttempts)
		}
		if err := db.IncrementLoginAttemptsByID(u.ID); err != nil {
			t.Fatalf("IncrementLoginAttemptsByID: %v", err)
		}
	}
	locked, err := db.IsUserLockedOutByID(u.ID)
	if err != nil {
		t.Fatalf("IsUserLockedOutByID: %v", err)
	}
	if !locked {
		t.Fatalf("not locked out after %d failures", MaxLoginAttempts)
	}

	// Lockout window expired: not locked and the counter is reset.
	old := time.Now().UTC().Add(-2 * LoginLockoutTime).Format("2006-01-02 15:04:05")
	if _, err := RetryableExec(db.GetMainDB(), "UPDATE users SET updated_at = ? WHERE id = ?", old, u.ID); err != nil {
		t.Fatal(err)
	}
	locked, err = db.IsUserLockedOutByID(u.ID)
	if err != nil {
		t.Fatalf("IsUserLockedOutByID: %v", err)
	}
	if locked {
		t.Fatalf("still locked out after the lockout window")
	}
	var attempts int
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT login_attempts FROM users WHERE id = ?", []interface{}{u.ID}, &attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("login_attempts = %d after expired lockout, want 0", attempts)
	}

	if _, err := db.IsUserLockedOutByID(-1); err == nil {
		t.Fatalf("IsUserLockedOutByID(unknown) returned no error")
	}
}

func TestW1AuthReserveLoginAttemptByID(t *testing.T) {
	db := w0DB(t)
	u := w1AuthUser(t)

	const workers = 20
	var allowed atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := db.ReserveLoginAttemptByID(u.ID)
			if err != nil {
				errs <- err
				return
			}
			if ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("ReserveLoginAttemptByID: %v", err)
	}
	if got := allowed.Load(); got != int64(MaxLoginAttempts) {
		t.Fatalf("%d concurrent reservations allowed %d, want %d", workers, got, MaxLoginAttempts)
	}
	if locked, err := db.IsUserLockedOutByID(u.ID); err != nil || !locked {
		t.Fatalf("IsUserLockedOutByID after the limit = %v, %v; want true", locked, err)
	}

	// Window passed: the next attempt is allowed and the counter restarts at 1
	old := time.Now().UTC().Add(-2 * LoginLockoutTime).Format("2006-01-02 15:04:05")
	if _, err := RetryableExec(db.GetMainDB(), "UPDATE users SET updated_at = ? WHERE id = ?", old, u.ID); err != nil {
		t.Fatal(err)
	}
	ok, err := db.ReserveLoginAttemptByID(u.ID)
	if err != nil || !ok {
		t.Fatalf("ReserveLoginAttemptByID after the window = %v, %v; want true", ok, err)
	}
	var attempts int
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT login_attempts FROM users WHERE id = ?", []interface{}{u.ID}, &attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("login_attempts = %d after the window, want 1", attempts)
	}

	// A successful login (CreateUserSession) resets the counter
	if _, err := db.CreateUserSession(u.ID, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxLoginAttempts; i++ {
		if ok, err := db.ReserveLoginAttemptByID(u.ID); err != nil || !ok {
			t.Fatalf("reservation %d after reset = %v, %v; want true", i, ok, err)
		}
	}
	if ok, err := db.ReserveLoginAttemptByID(u.ID); err != nil || ok {
		t.Fatalf("reservation past the limit = %v, %v; want false", ok, err)
	}

	if ok, err := db.ReserveLoginAttemptByID(-1); err != nil || ok {
		t.Fatalf("ReserveLoginAttemptByID(unknown) = %v, %v; want false, nil", ok, err)
	}
}
