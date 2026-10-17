package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fuFormsUser reloads a user from the database.
func fuFormsUser(t *testing.T, userID int64) *models.User {
	t.Helper()
	u, err := w0DB(t).GetUserByID(userID)
	if err != nil {
		t.Fatalf("GetUserByID(%d): %v", userID, err)
	}
	return u
}

// fuFormsPostProfile posts form to /profile as cookie's user and asserts the redirect.
func fuFormsPostProfile(t *testing.T, cookie *http.Cookie, form url.Values) {
	t.Helper()
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/profile", Form: form, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /profile = %d, want 303", rec.Code)
	}
}

// TestFuFormsProfileAtomicPartialWrite is the test A1 exists for: the second write of the
// profile update fails, and the first one must not survive it.
//
// The failure is forced through the UNIQUE constraint on users.email (migration 0001):
// the email of a second user is handed to UpdateUserProfile together with a new password
// hash. The password statement runs first and succeeds, the email statement then fails,
// and only a real transaction undoes the password. With the three separate statements this
// finding describes, password_hash would be left changed.
func TestFuFormsProfileAtomicPartialWrite(t *testing.T) {
	db := w0DB(t)
	victim, _ := w0NewUser(t, false)
	other, _ := w0NewUser(t, false)

	before := fuFormsUser(t, victim.ID)
	takenEmail := fuFormsUser(t, other.ID).Email
	newHash := before.PasswordHash + "-fuforms-moved"
	newName := "fuforms-newname"

	err := db.UpdateUserProfile(victim.ID, &newHash, &takenEmail, &newName)
	if err == nil {
		t.Fatalf("UpdateUserProfile with the email of user %d = nil, want a UNIQUE failure", other.ID)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("UpdateUserProfile error = %v, want a UNIQUE constraint failure (the test no longer forces the second write to fail)", err)
	}

	after := fuFormsUser(t, victim.ID)
	if after.PasswordHash != before.PasswordHash {
		t.Errorf("password_hash changed although the email write failed: the writes are not atomic")
	}
	if after.Email != before.Email {
		t.Errorf("email = %q, want the unchanged %q", after.Email, before.Email)
	}
	if after.DisplayName != before.DisplayName {
		t.Errorf("display_name = %q, want the unchanged %q", after.DisplayName, before.DisplayName)
	}
	// The other user is untouched too.
	if got := fuFormsUser(t, other.ID).Email; got != takenEmail {
		t.Errorf("email of user %d = %q, want the unchanged %q", other.ID, got, takenEmail)
	}
}

// TestFuFormsProfileRuneLimitRejectsWholeUpdate pins the display-name limit of
// UpdateUserProfile: 64 runes (not bytes), exactly like UpdateUserDisplayName, and a name
// over it rejects the email in the same call instead of writing it.
func TestFuFormsProfileRuneLimitRejectsWholeUpdate(t *testing.T) {
	db := w0DB(t)
	user, _ := w0NewUser(t, false)
	before := fuFormsUser(t, user.ID)

	tooLong := strings.Repeat("ä", 65) // 65 runes, 130 bytes
	rejectedEmail := user.Username + ".fuforms-rejected@test.invalid"
	if err := db.UpdateUserProfile(user.ID, nil, &rejectedEmail, &tooLong); err == nil {
		t.Fatal("UpdateUserProfile with a 65 rune display name = nil, want an error")
	}
	after := fuFormsUser(t, user.ID)
	if after.Email != before.Email {
		t.Errorf("email = %q after a rejected display name, want the unchanged %q", after.Email, before.Email)
	}
	if after.DisplayName != before.DisplayName {
		t.Errorf("display_name = %q, want the unchanged %q", after.DisplayName, before.DisplayName)
	}

	// 64 runes of the same multi-byte character are accepted: the limit counts runes.
	ok := strings.Repeat("ä", 64)
	okEmail := user.Username + ".fuforms-ok@test.invalid"
	if err := db.UpdateUserProfile(user.ID, nil, &okEmail, &ok); err != nil {
		t.Fatalf("UpdateUserProfile with a 64 rune display name: %v", err)
	}
	after = fuFormsUser(t, user.ID)
	if after.Email != okEmail || after.DisplayName != ok {
		t.Errorf("64 rune update not applied: email=%q display_name has %d runes", after.Email, len([]rune(after.DisplayName)))
	}
}

// TestFuFormsProfileNilFieldsKeepColumns pins the nil contract: a nil argument leaves that
// column alone, and an all-nil call writes nothing at all.
func TestFuFormsProfileNilFieldsKeepColumns(t *testing.T) {
	db := w0DB(t)
	user, _ := w0NewUser(t, false)
	before := fuFormsUser(t, user.ID)

	if err := db.UpdateUserProfile(user.ID, nil, nil, nil); err != nil {
		t.Fatalf("UpdateUserProfile(nil, nil, nil): %v", err)
	}
	after := fuFormsUser(t, user.ID)
	if after.Email != before.Email || after.DisplayName != before.DisplayName || after.PasswordHash != before.PasswordHash {
		t.Error("UpdateUserProfile(nil, nil, nil) changed a column")
	}

	// Only the display name: email and password hash stay.
	name := "fuforms-only-name"
	if err := db.UpdateUserProfile(user.ID, nil, nil, &name); err != nil {
		t.Fatalf("UpdateUserProfile(display name only): %v", err)
	}
	after = fuFormsUser(t, user.ID)
	if after.DisplayName != name {
		t.Errorf("display_name = %q, want %q", after.DisplayName, name)
	}
	if after.Email != before.Email {
		t.Errorf("email = %q, want the unchanged %q", after.Email, before.Email)
	}
	if after.PasswordHash != before.PasswordHash {
		t.Error("password_hash changed by a display-name-only update")
	}
}

// TestFuFormsProfileUpdateAllThree walks the handler: one POST that changes password,
// email and display name together must apply all three, and the new password must work.
func TestFuFormsProfileUpdateAllThree(t *testing.T) {
	user, cookie := w0NewUser(t, false)
	before := fuFormsUser(t, user.ID)

	newEmail := user.Username + ".fuforms-all@test.invalid"
	newName := "fuforms All Three"
	newPassword := "fuforms-newpassword-1"
	fuFormsPostProfile(t, cookie, url.Values{
		"email":            {newEmail},
		"display_name":     {newName},
		"current_password": {w0Password},
		"new_password":     {newPassword},
		"confirm_password": {newPassword},
	})

	after := fuFormsUser(t, user.ID)
	if after.Email != newEmail {
		t.Errorf("email = %q, want %q", after.Email, newEmail)
	}
	if after.DisplayName != newName {
		t.Errorf("display_name = %q, want %q", after.DisplayName, newName)
	}
	if after.PasswordHash == before.PasswordHash {
		t.Error("password_hash unchanged, want the new password stored")
	}
	if !checkPassword(newPassword, after.PasswordHash) {
		t.Error("the new password does not verify against the stored hash")
	}
}

// TestFuFormsProfileValidationFenceStillHolds re-checks, through the handler, that making
// the writes atomic did not move or weaken the validation fence in front of them: a
// rejected field still leaves every column untouched, and a legacy display name that the
// form re-sends unchanged still does not block an email change (L13).
func TestFuFormsProfileValidationFenceStillHolds(t *testing.T) {
	user, cookie := w0NewUser(t, false)
	before := fuFormsUser(t, user.ID)

	assertUnchanged := func(what string) {
		t.Helper()
		after := fuFormsUser(t, user.ID)
		if after.Email != before.Email || after.DisplayName != before.DisplayName || after.PasswordHash != before.PasswordHash {
			t.Errorf("%s: a column changed although the request was rejected (email=%q display_name=%q)",
				what, after.Email, after.DisplayName)
		}
	}

	// Missing email.
	fuFormsPostProfile(t, cookie, url.Values{
		"display_name":     {before.DisplayName},
		"current_password": {w0Password},
	})
	assertUnchanged("empty email")

	// Wrong current password, with a new email and a new display name.
	fuFormsPostProfile(t, cookie, url.Values{
		"email":            {user.Username + ".fuforms-wrongpw@test.invalid"},
		"display_name":     {"fuforms wrongpw"},
		"current_password": {w0Password + "-wrong"},
	})
	assertUnchanged("wrong current password")

	// A CR/LF display name (hardening E17) is still refused before any write.
	fuFormsPostProfile(t, cookie, url.Values{
		"email":            {user.Username + ".fuforms-crlf@test.invalid"},
		"display_name":     {"Evil\r\nControl: cancel <x@y>"},
		"current_password": {w0Password},
	})
	assertUnchanged("CR/LF display name")

	// An email already used by another user is still refused before any write.
	other, _ := w0NewUser(t, false)
	fuFormsPostProfile(t, cookie, url.Values{
		"email":            {fuFormsUser(t, other.ID).Email},
		"display_name":     {"fuforms taken"},
		"current_password": {w0Password},
	})
	assertUnchanged("email already in use")

	// Mismatched new passwords: nothing is written, not even the email.
	fuFormsPostProfile(t, cookie, url.Values{
		"email":            {user.Username + ".fuforms-mismatch@test.invalid"},
		"display_name":     {before.DisplayName},
		"current_password": {w0Password},
		"new_password":     {"fuforms-newpassword-a"},
		"confirm_password": {"fuforms-newpassword-b"},
	})
	assertUnchanged("mismatched new passwords")

	// L13: a stored legacy display name the form re-sends unchanged does not block the
	// email change, because validateDisplayName only runs when the name actually changed.
	legacy := "Jane <jane@fuforms.invalid>"
	if err := w0DB(t).UpdateUserProfile(user.ID, nil, nil, &legacy); err != nil {
		t.Fatalf("seed the legacy display name: %v", err)
	}
	legacyEmail := user.Username + ".fuforms-legacy@test.invalid"
	fuFormsPostProfile(t, cookie, url.Values{
		"email":            {legacyEmail},
		"display_name":     {legacy},
		"current_password": {w0Password},
	})
	after := fuFormsUser(t, user.ID)
	if after.Email != legacyEmail {
		t.Errorf("email = %q, want %q (the legacy display name blocked the change)", after.Email, legacyEmail)
	}
	if after.DisplayName != legacy {
		t.Errorf("display_name = %q, want the unchanged %q", after.DisplayName, legacy)
	}
}
