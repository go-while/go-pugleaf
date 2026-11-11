package nntp

// AUTHINFO sequence tests (plan e2e-gap-tests, slice intake-wire, G11; source: NewsCache
// tests/e2e/auth_test.go). RFC 4643 §2.3 leaves the order of USER and PASS to the server
// state machine; what matters is that a failed attempt leaves no half-open state:
//
//   - AUTHINFO PASS after a failed PASS gives 482 again (the pending user was consumed),
//     and a fresh USER + PASS then still succeeds with 281;
//   - a second AUTHINFO USER replaces the pending one, so the first user's password no
//     longer authenticates (nntp-cmd-auth.go: c.authUsername = argument);
//   - the same for a peer login (plan nntp-peering: in_username goes to the peers table).
//
// "AUTHINFO PASS without a USER" (482) and "AUTHINFO after a successful login" (502) are
// covered by TestAuthArgErrors / TestAuthSecondAuthinfo in nntp-server-auth_test.go.
//
// Identifiers use the ao / TestAo prefix (contract K2).

import (
	"testing"

	"github.com/go-while/nntp-testkit/client"
)

// aoExpect sends one command and checks its reply code.
func aoExpect(t *testing.T, c *client.Conn, want int, format string, args ...any) string {
	t.Helper()
	code, line := c.Cmd(format, args...)
	if err := client.ExpectCode(code, line, want); err != nil {
		t.Errorf("%v", err)
	}
	return line
}

// TestAoPassAfterFailedPass: a rejected AUTHINFO PASS consumes the pending username, so a
// second PASS is out of sequence (482, RFC 4643 §2.3.2) and not a second password guess
// against the same account; a fresh USER + PASS on the same connection then succeeds.
func TestAoPassAfterFailedPass(t *testing.T) {
	user, pass, _ := authNewUser(t, true, true, 10, "")
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)

	code, line := authLogin(t, c, user, "wrong-"+pass)
	if err := client.ExpectCode(code, line, 481); err != nil {
		t.Fatalf("first PASS with a wrong password: %v", err)
	}
	// The pending user is gone: another PASS is out of sequence, whatever it carries.
	aoExpect(t, c, 482, "AUTHINFO PASS %s", pass)
	aoExpect(t, c, 482, "AUTHINFO PASS %s", "wrong-again")

	// A complete new attempt still works (the session is not locked out).
	aoExpect(t, c, 381, "AUTHINFO USER %s", user)
	aoExpect(t, c, 281, "AUTHINFO PASS %s", pass)
	// And after the success every AUTHINFO is 502 (RFC 4643 §2.3.2).
	aoExpect(t, c, 502, "AUTHINFO USER %s", user)
}

// TestAoReplacementUser: AUTHINFO USER may be repeated before PASS (RFC 4643 §2.3.1), and
// the last one wins: the password of the user named first must not authenticate any more.
func TestAoReplacementUser(t *testing.T) {
	first, firstPass, _ := authNewUser(t, true, true, 10, "")
	second, secondPass, _ := authNewUser(t, true, true, 10, "")
	hs := newTestServer(t, hServerOpts{})

	t.Run("unknown-replacement", func(t *testing.T) {
		c := hs.dial(t)
		aoExpect(t, c, 381, "AUTHINFO USER %s", first)
		aoExpect(t, c, 381, "AUTHINFO USER %s", first+"-does-not-exist")
		// The pending user is the unknown one: the first user's password is rejected.
		aoExpect(t, c, 481, "AUTHINFO PASS %s", firstPass)
		// Nothing of the first attempt survived: a complete login still works.
		aoExpect(t, c, 381, "AUTHINFO USER %s", first)
		aoExpect(t, c, 281, "AUTHINFO PASS %s", firstPass)
	})

	t.Run("other-user-replacement", func(t *testing.T) {
		c := hs.dial(t)
		aoExpect(t, c, 381, "AUTHINFO USER %s", first)
		aoExpect(t, c, 381, "AUTHINFO USER %s", second)
		aoExpect(t, c, 481, "AUTHINFO PASS %s", firstPass)
		aoExpect(t, c, 381, "AUTHINFO USER %s", second)
		aoExpect(t, c, 281, "AUTHINFO PASS %s", secondPass)
	})

	// Pinned: a malformed AUTHINFO (501) is not an attempt and leaves the pending user
	// alone, so the PASS that follows belongs to the USER that was accepted.
	t.Run("syntax-error-keeps-the-pending-user", func(t *testing.T) {
		c := hs.dial(t)
		aoExpect(t, c, 381, "AUTHINFO USER %s", first)
		aoExpect(t, c, 501, "AUTHINFO USER")
		aoExpect(t, c, 501, "AUTHINFO BOGUS x")
		aoExpect(t, c, 281, "AUTHINFO PASS %s", firstPass)
	})
}

// TestAoPeerLoginOrder: the same sequence rules hold for a peer login, which is checked
// against the peers table instead of nntp_users (plan nntp-peering). A failed peer PASS
// consumes the pending name (482 afterwards), and a fresh pair logs the peer in, which is
// what opens the feed commands (CHECK answers 238 instead of 480).
func TestAoPeerLoginOrder(t *testing.T) {
	reg, peer, pass := hPeerLogin(t, "127.0.0.1")
	hs := newTestServer(t, hServerOpts{Proc: iwPass(), Peers: reg})
	c := hs.dial(t)

	g := hGroupName("ao.peer")
	id := hMessageID(g, 1)
	// Before the login the feed commands say "authentication required" (480).
	aoExpect(t, c, 480, "CHECK %s", id)

	aoExpect(t, c, 381, "AUTHINFO USER %s", peer.InUsername)
	aoExpect(t, c, 481, "AUTHINFO PASS %s", "wrong-"+pass)
	aoExpect(t, c, 482, "AUTHINFO PASS %s", pass)
	aoExpect(t, c, 480, "CHECK %s", id)

	aoExpect(t, c, 381, "AUTHINFO USER %s", peer.InUsername)
	aoExpect(t, c, 281, "AUTHINFO PASS %s", pass)
	aoExpect(t, c, 238, "CHECK %s", id)
	// RFC 4643 §2.3.2: no second AUTHINFO after the peer logged in.
	aoExpect(t, c, 502, "AUTHINFO USER %s", peer.InUsername)
}
