package database

// Tests of plan nntp-server-hardening, slice nntp-auth-db: the NNTP auth cache
// re-checks the stored bcrypt hash on a hit (SEC-6), unknown users cost a bcrypt
// compare (A-nntp-sec-6), same-process invalidation, and the admin lookups used by
// nntpmgr and the web handlers (A-ops-9, A-web-5 DB part).

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// nshAuthNewUser inserts an NNTP user with a fresh name and returns it (read back in
// any state) with its plaintext password. The user and its cache entries are
// removed when the test ends.
func nshAuthNewUser(t *testing.T, active bool, webUserID int64) (*models.NNTPUser, string) {
	t.Helper()
	db := w0DB(t)
	name := w0Name("nshauth")
	pass := "pw-" + name
	if err := db.InsertNNTPUser(&models.NNTPUser{Username: name, Password: pass, MaxConns: 1, WebUserID: webUserID, IsActive: active}); err != nil {
		t.Fatalf("InsertNNTPUser(%s): %v", name, err)
	}
	u, err := db.GetNNTPUserByUsernameAnyState(name)
	if err != nil {
		t.Fatalf("GetNNTPUserByUsernameAnyState(%s): %v", name, err)
	}
	t.Cleanup(func() {
		db.InvalidateNNTPUserAuth(name)
		if err := db.DeleteNNTPUser(u.ID); err != nil {
			t.Logf("cleanup DeleteNNTPUser(%d): %v", u.ID, err)
		}
	})
	return u, pass
}

// nshAuthCache returns the shared DB's auth cache or fails the test.
func nshAuthCache(t *testing.T) *NNTPAuthCache {
	t.Helper()
	c := w0DB(t).NNTPAuthCache
	if c == nil {
		t.Fatal("NNTPAuthCache is nil: the cache paths cannot be tested")
	}
	return c
}

// TestNSHAuthOldPasswordRejected (SEC-6): after a password change the old password
// stops working at once, also when the change was made by another process (here: a
// direct UPDATE that leaves this process's cache entry in place), and the new
// password works and is cached with the new stored hash.
func TestNSHAuthOldPasswordRejected(t *testing.T) {
	db := w0DB(t)
	cache := nshAuthCache(t)

	t.Run("other-process", func(t *testing.T) {
		u, oldPass := nshAuthNewUser(t, true, 0)
		newPass := "new-" + oldPass
		if _, err := db.AuthenticateNNTPUser(u.Username, oldPass); err != nil {
			t.Fatalf("login with the old password: %v", err)
		}
		if _, hash, ok := cache.GetEntry(u.Username, oldPass); !ok || hash != u.Password {
			t.Fatalf("login not cached with the stored hash: ok=%v hash=%q want %q", ok, hash, u.Password)
		}

		// Another process changes the password: same bcrypt hashing, but this
		// process's cache is not told.
		newHash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("bcrypt: %v", err)
		}
		if _, err := RetryableExec(db.GetMainDB(), `UPDATE nntp_users SET password = ? WHERE id = ?`, string(newHash), u.ID); err != nil {
			t.Fatalf("direct password update: %v", err)
		}
		if _, ok := cache.Get(u.Username, oldPass); !ok {
			t.Fatal("the cache entry is gone already: the test would not exercise the stored-hash check")
		}

		if got, err := db.AuthenticateNNTPUser(u.Username, oldPass); err == nil {
			t.Fatalf("old password accepted after the change (user %+v)", got)
		}
		if _, ok := cache.Get(u.Username, oldPass); ok {
			t.Fatal("the stale cache entry survived a refused login")
		}
		if _, err := db.AuthenticateNNTPUser(u.Username, newPass); err != nil {
			t.Fatalf("login with the new password: %v", err)
		}
		if _, hash, ok := cache.GetEntry(u.Username, newPass); !ok || hash != string(newHash) {
			t.Fatalf("new login not cached with the new hash: ok=%v hash=%q", ok, hash)
		}
	})

	t.Run("same-process", func(t *testing.T) {
		u, oldPass := nshAuthNewUser(t, true, 0)
		newPass := "new-" + oldPass
		if _, err := db.AuthenticateNNTPUser(u.Username, oldPass); err != nil {
			t.Fatalf("login with the old password: %v", err)
		}
		if err := db.UpdateNNTPUserPassword(u.ID, newPass); err != nil {
			t.Fatalf("UpdateNNTPUserPassword: %v", err)
		}
		if _, ok := cache.Get(u.Username, oldPass); ok {
			t.Fatal("UpdateNNTPUserPassword left the cached login in place")
		}
		if _, err := db.AuthenticateNNTPUser(u.Username, oldPass); err == nil {
			t.Fatal("old password accepted after UpdateNNTPUserPassword")
		}
		if _, err := db.AuthenticateNNTPUser(u.Username, newPass); err != nil {
			t.Fatalf("login with the new password: %v", err)
		}
	})
}

// TestNSHAuthRemoveUserID: RemoveUserID drops every entry of one user ID and
// nothing else, is safe on a nil cache, and the DB writes that end a login
// (password change, deactivation, deletion) drop the cached entry at once.
func TestNSHAuthRemoveUserID(t *testing.T) {
	t.Run("cache", func(t *testing.T) {
		// A literal cache: no cleanup goroutine for a test-local value.
		c := &NNTPAuthCache{entries: make(map[string]*AuthCacheEntry), ttl: time.Minute}
		c.Set(1, "nshauth-a", "pa", "hash-a")
		c.Set(1, "nshauth-b", "pb", "hash-b")
		c.Set(2, "nshauth-c", "pc", "hash-c")
		if id, hash, ok := c.GetEntry("nshauth-c", "pc"); !ok || id != 2 || hash != "hash-c" {
			t.Fatalf("GetEntry = (%d, %q, %v), want (2, hash-c, true)", id, hash, ok)
		}
		if _, _, ok := c.GetEntry("nshauth-c", "wrong"); ok {
			t.Fatal("GetEntry accepted a wrong password")
		}
		c.RemoveUserID(1)
		if _, ok := c.Get("nshauth-a", "pa"); ok {
			t.Fatal("entry a of user 1 survived RemoveUserID(1)")
		}
		if _, ok := c.Get("nshauth-b", "pb"); ok {
			t.Fatal("entry b of user 1 survived RemoveUserID(1)")
		}
		if id, ok := c.Get("nshauth-c", "pc"); !ok || id != 2 {
			t.Fatalf("entry of user 2 removed by RemoveUserID(1): (%d, %v)", id, ok)
		}
		var nilCache *NNTPAuthCache
		nilCache.RemoveUserID(1) // must not panic
	})

	db := w0DB(t)
	cache := nshAuthCache(t)
	for _, tc := range []struct {
		name  string
		write func(u *models.NNTPUser, pass string) error
	}{
		{"password", func(u *models.NNTPUser, pass string) error { return db.UpdateNNTPUserPassword(u.ID, "new-"+pass) }},
		{"deactivate", func(u *models.NNTPUser, _ string) error { return db.DeactivateNNTPUser(u.ID) }},
		{"delete", func(u *models.NNTPUser, _ string) error { return db.DeleteNNTPUser(u.ID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, pass := nshAuthNewUser(t, true, 0)
			if _, err := db.AuthenticateNNTPUser(u.Username, pass); err != nil {
				t.Fatalf("login: %v", err)
			}
			if _, ok := cache.Get(u.Username, pass); !ok {
				t.Fatal("login was not cached")
			}
			if err := tc.write(u, pass); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, ok := cache.Get(u.Username, pass); ok {
				t.Fatalf("%s left the cached login in place", tc.name)
			}
		})
	}
}

// TestNSHAuthUnknownUserTiming (A-nntp-sec-6): a login for an unknown user costs a
// bcrypt compare like a wrong password for a known user, so the reply time does not
// tell whether the user exists. Minimum of 3 tries each; unknown >= 0.5 x known.
func TestNSHAuthUnknownUserTiming(t *testing.T) {
	db := w0DB(t)
	u, _ := nshAuthNewUser(t, true, 0)
	unknown := w0Name("nshauth-unknown")

	measure := func(username string) time.Duration {
		best := time.Duration(-1)
		for i := 0; i < 3; i++ {
			start := time.Now()
			if _, err := db.AuthenticateNNTPUser(username, "nshauth-wrong-password"); err == nil {
				t.Fatalf("AuthenticateNNTPUser(%s) with a wrong password succeeded", username)
			}
			if d := time.Since(start); best < 0 || d < best {
				best = d
			}
		}
		return best
	}
	known := measure(u.Username)
	unk := measure(unknown)
	t.Logf("min known-user wrong password %v, min unknown user %v", known, unk)
	if unk*2 < known {
		t.Fatalf("unknown user answered in %v, known user with a wrong password in %v: usernames can be enumerated by timing", unk, known)
	}
}

// TestNSHAuthWebUserIDLookup (A-web-5 DB part, D6 contract): several NNTP users
// mapped to one web user: GetNNTPUserByWebUserID returns the active one with the
// lowest id, 0 and negative ids give sql.ErrNoRows, CountNNTPUsersByWebUserID counts
// every state, UpdateNNTPUserWebUserID(.., 0) stores NULL. CreateNNTPUserForWebUser
// uses DefaultNNTPUserMaxConns.
func TestNSHAuthWebUserIDLookup(t *testing.T) {
	db := w0DB(t)
	web := w1APIUser(t)
	u1, _ := nshAuthNewUser(t, true, web.ID)
	u2, _ := nshAuthNewUser(t, true, web.ID)
	if u1.ID >= u2.ID {
		t.Fatalf("ids not increasing: %d, %d", u1.ID, u2.ID)
	}

	got, err := db.GetNNTPUserByWebUserID(web.ID)
	if err != nil {
		t.Fatalf("GetNNTPUserByWebUserID: %v", err)
	}
	if got.ID != u1.ID {
		t.Fatalf("GetNNTPUserByWebUserID returned id %d, want the lowest id %d", got.ID, u1.ID)
	}
	for _, id := range []int64{0, -1} {
		if _, err := db.GetNNTPUserByWebUserID(id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("GetNNTPUserByWebUserID(%d) error = %v, want sql.ErrNoRows", id, err)
		}
	}

	if n, err := db.CountNNTPUsersByWebUserID(web.ID); err != nil || n != 2 {
		t.Fatalf("CountNNTPUsersByWebUserID = (%d, %v), want 2", n, err)
	}
	if n, err := db.CountNNTPUsersByWebUserID(0); err != nil || n != 0 {
		t.Fatalf("CountNNTPUsersByWebUserID(0) = (%d, %v), want 0", n, err)
	}

	// The lowest id inactive: the next active user is returned, the count keeps both.
	if err := db.DeactivateNNTPUser(u1.ID); err != nil {
		t.Fatalf("DeactivateNNTPUser: %v", err)
	}
	if got, err := db.GetNNTPUserByWebUserID(web.ID); err != nil || got.ID != u2.ID {
		t.Fatalf("GetNNTPUserByWebUserID with the lowest id inactive = (%v, %v), want id %d", got, err, u2.ID)
	}
	if n, err := db.CountNNTPUsersByWebUserID(web.ID); err != nil || n != 2 {
		t.Fatalf("CountNNTPUsersByWebUserID (one inactive) = (%d, %v), want 2", n, err)
	}

	if err := db.UpdateNNTPUserWebUserID(u2.ID, 0); err != nil {
		t.Fatalf("UpdateNNTPUserWebUserID(%d, 0): %v", u2.ID, err)
	}
	var isNull bool
	if err := RetryableQueryRowScan(db.GetMainDB(), `SELECT web_user_id IS NULL FROM nntp_users WHERE id = ?`, []interface{}{u2.ID}, &isNull); err != nil {
		t.Fatalf("read web_user_id: %v", err)
	}
	if !isNull {
		t.Fatal("UpdateNNTPUserWebUserID(.., 0) did not store NULL")
	}
	if n, err := db.CountNNTPUsersByWebUserID(web.ID); err != nil || n != 1 {
		t.Fatalf("CountNNTPUsersByWebUserID after unmapping = (%d, %v), want 1", n, err)
	}
	if err := db.UpdateNNTPUserWebUserID(u2.ID, web.ID); err != nil {
		t.Fatalf("UpdateNNTPUserWebUserID(%d, %d): %v", u2.ID, web.ID, err)
	}
	if n, err := db.CountNNTPUsersByWebUserID(web.ID); err != nil || n != 2 {
		t.Fatalf("CountNNTPUsersByWebUserID after re-mapping = (%d, %v), want 2", n, err)
	}
	if err := db.UpdateNNTPUserWebUserID(-1<<30, 0); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateNNTPUserWebUserID for a missing user: %v, want sql.ErrNoRows", err)
	}

	// A new web user's automatic NNTP account gets the default maxconns.
	web2 := w1APIUser(t)
	if err := db.CreateNNTPUserForWebUser(web2.ID); err != nil {
		t.Fatalf("CreateNNTPUserForWebUser: %v", err)
	}
	auto, err := db.GetNNTPUserByWebUserID(web2.ID)
	if err != nil {
		t.Fatalf("GetNNTPUserByWebUserID(auto): %v", err)
	}
	t.Cleanup(func() {
		if err := db.DeleteNNTPUser(auto.ID); err != nil {
			t.Logf("cleanup DeleteNNTPUser(%d): %v", auto.ID, err)
		}
	})
	if auto.MaxConns != DefaultNNTPUserMaxConns {
		t.Fatalf("CreateNNTPUserForWebUser maxconns = %d, want %d", auto.MaxConns, DefaultNNTPUserMaxConns)
	}
}

// TestNSHAuthAnyState (A-ops-9): an inactive user is invisible to the login lookup
// but found by GetNNTPUserByUsernameAnyState; an unknown name gives sql.ErrNoRows.
func TestNSHAuthAnyState(t *testing.T) {
	db := w0DB(t)
	u, pass := nshAuthNewUser(t, false, 0)
	if u.IsActive {
		t.Fatal("user was inserted active")
	}
	if _, err := db.GetNNTPUserByUsername(u.Username); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetNNTPUserByUsername(inactive) error = %v, want sql.ErrNoRows", err)
	}
	if _, err := db.AuthenticateNNTPUser(u.Username, pass); err == nil {
		t.Fatal("an inactive user authenticated")
	}
	got, err := db.GetNNTPUserByUsernameAnyState(u.Username)
	if err != nil {
		t.Fatalf("GetNNTPUserByUsernameAnyState(inactive): %v", err)
	}
	if got.ID != u.ID || got.IsActive {
		t.Fatalf("GetNNTPUserByUsernameAnyState = id %d active %v, want id %d inactive", got.ID, got.IsActive, u.ID)
	}
	if _, err := db.GetNNTPUserByUsernameAnyState(w0Name("nshauth-missing")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetNNTPUserByUsernameAnyState(missing) error = %v, want sql.ErrNoRows", err)
	}
}
