package nntp

// NNTP server authentication tests (plan nntp-audit-tests, slice test-nntp-auth).
//
// Covers AUTHINFO USER/PASS (RFC 4643 §2.3), the posting gates of POST, IHAVE
// and TAKETHIS (RFC 3977 §3.2.1, §6.3; RFC 4644 §2.5), the AuthManager helpers
// and the NNTP auth cache (SEC-6). Every assertion states the RFC-correct
// behavior; where the server differs today the result of that check goes to
// knownBug with the seed id (SEC-5, SEC-6, RFC-5) or a new T-nntp-auth-<n> id.
//
// Identifiers use the auth/TestAuth prefix (contract K2). Users are named
// t2user<seq> (at least 10 characters), message-ids <t2-<test>-<seq>@test.invalid>.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// authNewUser inserts an active-or-inactive NNTP user named t2user<seq> with
// the given password (a default one when pass is empty) and MaxConns. It
// returns the username, the password and the user id (0 for inactive users,
// which GetNNTPUserByUsername does not return).
func authNewUser(t *testing.T, posting, active bool, maxConns int, pass string) (string, string, int) {
	t.Helper()
	db := hTestDB(t)
	user := fmt.Sprintf("t2user%04d", hSeq.Add(1))
	if pass == "" {
		pass = "pw-" + user
	}
	if err := db.InsertNNTPUser(&models.NNTPUser{
		Username: user, Password: pass, MaxConns: maxConns, Posting: posting, WebUserID: 0, IsActive: active,
	}); err != nil {
		t.Fatalf("InsertNNTPUser %s: %v", user, err)
	}
	if !active {
		return user, pass, 0
	}
	u, err := db.GetNNTPUserByUsername(user)
	if err != nil {
		t.Fatalf("GetNNTPUserByUsername %s: %v", user, err)
	}
	return user, pass, u.ID
}

// authMessageID returns a unique message-id for test.
func authMessageID(test string) string {
	return fmt.Sprintf("<t2-%s-%d@test.invalid>", test, hSeq.Add(1))
}

// authLogin sends AUTHINFO USER (asserting 381, RFC 4643 §2.3.2) and then
// AUTHINFO PASS, and returns the reply to PASS. pass is sent verbatim, so it
// may contain spaces.
func authLogin(t *testing.T, c *hClient, user, pass string) (int, string) {
	t.Helper()
	code, line := c.cmd(t, "AUTHINFO USER %s", user)
	if err := hExpectCode(code, line, 381); err != nil {
		t.Fatalf("AUTHINFO USER %s: %v", user, err)
	}
	return c.cmd(t, "AUTHINFO PASS %s", pass)
}

// authMustLogin is authLogin that requires 281 (RFC 4643 §2.3.2).
func authMustLogin(t *testing.T, c *hClient, user, pass string) {
	t.Helper()
	code, line := authLogin(t, c, user, pass)
	if err := hExpectCode(code, line, 281); err != nil {
		t.Fatalf("login %s: %v", user, err)
	}
}

// authReplyText returns the text after the status code of a reply line.
func authReplyText(line string) string {
	if len(line) <= 4 {
		return ""
	}
	return line[4:]
}

// TestAuthUserPass: RFC 4643 §2.3.2, AUTHINFO USER with a known username gives
// 381 (password required) and AUTHINFO PASS with the right password gives 281.
func TestAuthUserPass(t *testing.T) {
	user, pass, _ := authNewUser(t, true, true, 10, "")
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "AUTHINFO USER %s", user)
	if err := hExpectCode(code, line, 381); err != nil {
		t.Fatalf("AUTHINFO USER: %v", err)
	}
	code, line = c.cmd(t, "AUTHINFO PASS %s", pass)
	if err := hExpectCode(code, line, 281); err != nil {
		t.Fatalf("AUTHINFO PASS: %v", err)
	}
}

// TestAuthFailures: RFC 4643 §2.3.2, a rejected password gives 481. The reply
// for an unknown user must be the same as for a wrong password, so the reply
// text does not reveal which usernames exist (RFC 4643 §5, Security Considerations).
func TestAuthFailures(t *testing.T) {
	user, _, _ := authNewUser(t, true, true, 10, "")
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)

	code, wrongLine := authLogin(t, c, user, "definitely-not-the-password")
	if err := hExpectCode(code, wrongLine, 481); err != nil {
		t.Fatalf("wrong password: %v", err)
	}

	unknown := fmt.Sprintf("t2user%04d-unknown", hSeq.Add(1))
	code, unknownLine := authLogin(t, c, unknown, "whatever-password")
	if err := hExpectCode(code, unknownLine, 481); err != nil {
		t.Fatalf("unknown user: %v", err)
	}
	if authReplyText(wrongLine) != authReplyText(unknownLine) {
		t.Fatalf("481 text differs: wrong password %q, unknown user %q", wrongLine, unknownLine)
	}
}

// TestAuthArgErrors covers out-of-sequence and malformed AUTHINFO commands.
func TestAuthArgErrors(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})

	// RFC 4643 §2.3.2: AUTHINFO PASS without a preceding AUTHINFO USER gives
	// 482 (authentication commands issued out of sequence).
	t.Run("pass-before-user", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "AUTHINFO PASS some-password")
		if err := hExpectCode(code, line, 482); err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §3.2.1 / RFC 4643 §2.3.2: a missing argument is a syntax error, 501.
	t.Run("no-subcommand", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "AUTHINFO")
		if err := hExpectCode(code, line, 501); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("user-without-name", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "AUTHINFO USER")
		if err := hExpectCode(code, line, 501); err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §3.2.1: an unknown keyword (variant) of a known command is a
	// syntax error, 501, not 500 (unknown command). RFC-5: the server sends 500.
	t.Run("unknown-subcommand", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "AUTHINFO FOO x")
		knownBug(t, "RFC-5", hExpectCode(code, line, 501))
	})
}

// TestAuthSecondAuthinfo: RFC 4643 §2.3.2 (and §2.2), after a successful
// authentication any further AUTHINFO command gives 502. SEC-5: the server
// accepts a new AUTHINFO USER (381) and lets the session re-authenticate.
func TestAuthSecondAuthinfo(t *testing.T) {
	user, pass, _ := authNewUser(t, true, true, 10, "")
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	authMustLogin(t, c, user, pass)
	code, line := c.cmd(t, "AUTHINFO USER %s", user)
	err := hExpectCode(code, line, 502)
	code, line = c.cmd(t, "AUTHINFO PASS %s", pass)
	if err2 := hExpectCode(code, line, 502); err2 != nil {
		err = errors.Join(err, fmt.Errorf("AUTHINFO PASS after login: %w", err2))
	}
	knownBug(t, "SEC-5", err)
}

// TestAuthInactiveUser: an inactive account is refused with 481 even with the
// right password (RFC 4643 §2.3.2: 481 for rejected credentials).
func TestAuthInactiveUser(t *testing.T) {
	user, pass, _ := authNewUser(t, true, false, 10, "")
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := authLogin(t, c, user, pass)
	if err := hExpectCode(code, line, 481); err != nil {
		t.Fatal(err)
	}
}

// TestAuthPasswordWithSpace: RFC 4643 §4 (formal syntax):
//
//	authinfo-pass-command = "AUTHINFO" WS "PASS" WS password
//	password = 1*user-pass-char
//	user-pass-char = B-CHAR
//
// and B-CHAR (RFC 3977 §9.8) = CTRL / TAB / SP / %x21-7E / UTF8-non-ascii, so
// a password may contain spaces: everything after "AUTHINFO PASS " is the
// password. T-nntp-auth-1: the server splits the line with strings.Fields and
// uses only the first word, so such a password never authenticates.
func TestAuthPasswordWithSpace(t *testing.T) {
	db := hTestDB(t)
	user, pass, _ := authNewUser(t, true, true, 10, "pw with  spaces")
	// Precondition: the password is stored with its spaces (no cache involved).
	if _, err := db.VerifyNNTPUserPassword(user, pass); err != nil {
		t.Fatalf("VerifyNNTPUserPassword: %v", err)
	}
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := authLogin(t, c, user, pass)
	knownBug(t, "T-nntp-auth-1", hExpectCode(code, line, 281))
}

// TestAuthFailureDelay: a 481 reply must be delayed by at least ErrorDelay, so
// passwords cannot be guessed at full speed (SEC-5; RFC 4643 §5 recommends
// limiting failed attempts). An unknown user is used so the measurement is not
// inflated by bcrypt; the minimum of 3 attempts must reach ErrorDelay.
func TestAuthFailureDelay(t *testing.T) {
	const delay = 50 * time.Millisecond
	hs := newTestServer(t, hServerOpts{ErrorDelay: delay})
	c := hs.dial(t)
	unknown := fmt.Sprintf("t2user%04d-nodelay", hSeq.Add(1))
	minTook := time.Duration(1<<63 - 1)
	for i := 0; i < 3; i++ {
		code, line := c.cmd(t, "AUTHINFO USER %s", unknown)
		if err := hExpectCode(code, line, 381); err != nil {
			t.Fatalf("AUTHINFO USER: %v", err)
		}
		start := time.Now()
		code, line = c.cmd(t, "AUTHINFO PASS wrong-password-%d", i)
		took := time.Since(start)
		if err := hExpectCode(code, line, 481); err != nil {
			t.Fatalf("AUTHINFO PASS: %v", err)
		}
		if took < minTook {
			minTook = took
		}
	}
	var err error
	if minTook < delay {
		err = fmt.Errorf("481 sent after %s, want at least ErrorDelay %s", minTook, delay)
	}
	knownBug(t, "SEC-5", err)
}

// TestAuthPostingGates: posting commands need authentication and permission.
// A fake processor makes POST/IHAVE/TAKETHIS reachable; CheckMessageID answers
// "duplicate" so an allowed IHAVE/TAKETHIS stops at 435/439 right after the gate.
func TestAuthPostingGates(t *testing.T) {
	proc := &hFakeProcessor{checkFn: func(string) int { return history.CaseDupes }}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	reader, readerPass, _ := authNewUser(t, false, true, 10, "")
	poster, posterPass, _ := authNewUser(t, true, true, 10, "")

	takethis := func(t *testing.T, c *hClient, test string) (int, string) {
		t.Helper()
		id := authMessageID(test)
		article := "Message-ID: " + id + "\r\nNewsgroups: t2.gates.1\r\nFrom: t2 <t2@test.invalid>\r\n" +
			"Subject: gate\r\n\r\nbody\r\n.\r\n"
		if err := c.send("TAKETHIS " + id + "\r\n" + article); err != nil {
			t.Fatalf("send TAKETHIS: %v", err)
		}
		code, line, err := c.readStatus()
		if err != nil {
			t.Fatalf("TAKETHIS reply: %v (%q)", err, line)
		}
		return code, line
	}

	// RFC 3977 §3.2.1 and RFC 4643 §2.1: a command that needs authentication
	// gives 480 before the client has authenticated.
	t.Run("unauthenticated", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.cmd(t, "POST")
		if err := hExpectCode(code, line, 480); err != nil {
			t.Errorf("POST: %v", err)
		}
		code, line = c.cmd(t, "IHAVE %s", authMessageID("gates"))
		if err := hExpectCode(code, line, 480); err != nil {
			t.Errorf("IHAVE: %v", err)
		}
		code, line = takethis(t, c, "gates")
		if err := hExpectCode(code, line, 480); err != nil {
			t.Errorf("TAKETHIS: %v", err)
		}
	})

	// RFC 3977 §3.2.1: an authenticated client without permission for a
	// command gets 502 (IHAVE has no specific "not permitted" code).
	t.Run("no-posting-ihave", func(t *testing.T) {
		c := hs.dial(t)
		authMustLogin(t, c, reader, readerPass)
		code, line := c.cmd(t, "IHAVE %s", authMessageID("gates"))
		if err := hExpectCode(code, line, 502); err != nil {
			t.Fatal(err)
		}
	})

	// RFC 3977 §6.3.1.2: POST that is not permitted gives 440 (posting not
	// permitted). RFC-5: the server sends 502.
	t.Run("no-posting-post", func(t *testing.T) {
		c := hs.dial(t)
		authMustLogin(t, c, reader, readerPass)
		code, line := c.cmd(t, "POST")
		knownBug(t, "RFC-5", hExpectCode(code, line, 440))
	})

	// Control: a user with posting permission gets past every gate.
	// RFC 3977 §6.3.1.2: POST → 340; §6.3.2.2: IHAVE of an unwanted article → 435;
	// RFC 4644 §2.5.2: TAKETHIS of an unwanted article → 439.
	t.Run("posting-allowed", func(t *testing.T) {
		c := hs.dial(t)
		authMustLogin(t, c, poster, posterPass)
		code, line := c.cmd(t, "IHAVE %s", authMessageID("gates"))
		if err := hExpectCode(code, line, 435); err != nil {
			t.Errorf("IHAVE: %v", err)
		}
		code, line = takethis(t, c, "gates")
		if err := hExpectCode(code, line, 439); err != nil {
			t.Errorf("TAKETHIS: %v", err)
		}
		code, line = c.cmd(t, "POST")
		if err := hExpectCode(code, line, 340); err != nil {
			t.Fatalf("POST: %v", err)
		}
		// An empty article: any status line keeps the session in sync.
		if err := c.send(".\r\n"); err != nil {
			t.Fatalf("send article: %v", err)
		}
		if _, line, err := c.readStatus(); err != nil {
			t.Fatalf("POST result: %v (%q)", err, line)
		}
	})
}

// TestAuthManagerUnits covers the AuthManager helpers (unused by the server
// today, FN-1/SEC-5, but they must give the right answers).
func TestAuthManagerUnits(t *testing.T) {
	db := hTestDB(t)
	am := NewAuthManager(db)
	posterName, _, _ := authNewUser(t, true, true, 10, "")
	readerName, _, _ := authNewUser(t, false, true, 10, "")
	poster, err := db.GetNNTPUserByUsername(posterName)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := db.GetNNTPUserByUsername(readerName)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("CanPost", func(t *testing.T) {
		if am.CanPost(nil) {
			t.Error("CanPost(nil) = true, want false")
		}
		if !am.CanPost(poster) {
			t.Error("CanPost(posting user) = false, want true")
		}
		if am.CanPost(reader) {
			t.Error("CanPost(read-only user) = true, want false")
		}
	})

	t.Run("CheckGroupAccess", func(t *testing.T) {
		if am.CheckGroupAccess(nil, "t2.access.1") {
			t.Error("CheckGroupAccess(nil) = true, want false")
		}
	})

	t.Run("CheckConnectionLimit", func(t *testing.T) {
		if am.CheckConnectionLimit(nil) {
			t.Error("CheckConnectionLimit(nil) = true, want false")
		}
		name, _, id := authNewUser(t, true, true, 1, "")
		u, err := db.GetNNTPUserByUsername(name)
		if err != nil {
			t.Fatal(err)
		}
		if !am.CheckConnectionLimit(u) {
			t.Fatal("MaxConns=1, no sessions: CheckConnectionLimit = false, want true")
		}
		connID := fmt.Sprintf("t2conn-%d", hSeq.Add(1))
		if err := db.CreateNNTPSession(id, connID, "127.0.0.1:1"); err != nil {
			t.Fatalf("CreateNNTPSession: %v", err)
		}
		if am.CheckConnectionLimit(u) {
			t.Error("MaxConns=1, 1 active session: CheckConnectionLimit = true, want false")
		}
		if err := db.CloseNNTPSession(connID); err != nil {
			t.Fatalf("CloseNNTPSession: %v", err)
		}
		if !am.CheckConnectionLimit(u) {
			t.Error("MaxConns=1, session closed: CheckConnectionLimit = false, want true")
		}
	})
}

// authCached reports whether the auth cache holds user with pass.
func authCached(t *testing.T, user, pass string) bool {
	t.Helper()
	db := hTestDB(t)
	if db.NNTPAuthCache == nil {
		t.Fatal("test database has no NNTPAuthCache")
	}
	_, ok := db.NNTPAuthCache.Get(user, pass)
	return ok
}

// TestAuthCacheDeactivated (SEC-6, first half): a deactivated account must not
// log in again through the auth cache (RFC 4643 §2.3.2: 481 for rejected
// credentials). Correct today since LO:F22 re-checks is_active on every hit.
func TestAuthCacheDeactivated(t *testing.T) {
	db := hTestDB(t)
	user, pass, id := authNewUser(t, true, true, 10, "")
	hs := newTestServer(t, hServerOpts{})

	c1 := hs.dial(t)
	authMustLogin(t, c1, user, pass)
	if !authCached(t, user, pass) {
		t.Fatal("login was not cached: the test would not exercise the cache")
	}
	if err := db.DeactivateNNTPUser(id); err != nil {
		t.Fatalf("DeactivateNNTPUser: %v", err)
	}
	c2 := hs.dial(t)
	code, line := authLogin(t, c2, user, pass)
	if err := hExpectCode(code, line, 481); err != nil {
		t.Fatalf("login after DeactivateNNTPUser: %v", err)
	}
}

// TestAuthCacheOldPassword (SEC-6, second half): after a password change the
// old password must be rejected at once (481, RFC 4643 §2.3.2). Today the
// cache still accepts it until NNTP_AUTH_CACHE_TIME (15 min) expires, because
// UpdateNNTPUserPassword never invalidates the cache entry.
func TestAuthCacheOldPassword(t *testing.T) {
	db := hTestDB(t)
	user, oldPass, id := authNewUser(t, true, true, 10, "")
	newPass := "new-" + oldPass
	hs := newTestServer(t, hServerOpts{})

	c1 := hs.dial(t)
	authMustLogin(t, c1, user, oldPass)
	if !authCached(t, user, oldPass) {
		t.Fatal("login was not cached: the test would not exercise the cache")
	}
	if err := db.UpdateNNTPUserPassword(id, newPass); err != nil {
		t.Fatalf("UpdateNNTPUserPassword: %v", err)
	}
	// The new password is stored (checked without the cache, so the cached
	// old-password entry stays in place for the assertion below).
	if _, err := db.VerifyNNTPUserPassword(user, newPass); err != nil {
		t.Fatalf("VerifyNNTPUserPassword(new): %v", err)
	}
	c2 := hs.dial(t)
	code, line := authLogin(t, c2, user, oldPass)
	knownBug(t, "SEC-6", hExpectCode(code, line, 481))
}

// TestAuthPlaintextNotOffered: RFC 4643 §2.2 says AUTHINFO USER is advertised
// in CAPABILITIES only when the server would accept it now, and §2.3.1 lets a
// server refuse plaintext AUTHINFO USER/PASS on an unencrypted connection with
// 483 (encryption required, RFC 3977 §3.2.1). On a non-TLS listener the server
// must give 483, or at least not advertise AUTHINFO USER. SEC-5: it advertises
// it and accepts it in plaintext.
func TestAuthPlaintextNotOffered(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "CAPABILITIES")
	if err := hExpectCode(code, line, 101); err != nil {
		t.Fatal(err)
	}
	advertised := false
	for _, capLine := range c.readDotLines(t) {
		f := strings.Fields(strings.ToUpper(capLine))
		if len(f) > 0 && f[0] == "AUTHINFO" {
			for _, arg := range f[1:] {
				if arg == "USER" {
					advertised = true
				}
			}
		}
	}
	user := fmt.Sprintf("t2user%04d", hSeq.Add(1))
	code, line = c.cmd(t, "AUTHINFO USER %s", user)
	var err error
	if code != 483 && advertised {
		err = fmt.Errorf("non-TLS: AUTHINFO USER advertised in CAPABILITIES and answered %q, want 483 or not advertised", line)
	}
	if err != nil {
		t.Fatal(err)
	}
}
