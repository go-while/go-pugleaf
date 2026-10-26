package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// adfNNTPUsersPost posts form to the admin NNTP user endpoint path and returns the flash
// messages the handler left for the session of cookie.
func adfNNTPUsersPost(t *testing.T, cookie *http.Cookie, path string, form url.Values) (success, failure string) {
	t.Helper()
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: path, Form: form, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST %s = %d, want 303", path, rec.Code)
	}
	success, _ = GetAndClearFlash(cookie.Value, "success")
	_, failure = GetAndClearFlash(cookie.Value, "error")
	return success, failure
}

// adfNNTPUsersCreate creates an NNTP user through the handler (it generates a 16-character
// username and password) and returns the stored row. It fails the test on a refusal.
func adfNNTPUsersCreate(t *testing.T, cookie *http.Cookie, form url.Values) *models.NNTPUser {
	t.Helper()
	success, failure := adfNNTPUsersPost(t, cookie, "/admin/nntpusers", form)
	if failure != "" {
		t.Fatalf("create NNTP user %v: refused: %q", form, failure)
	}
	_, rest, ok := strings.Cut(success, "Username: ")
	if !ok {
		t.Fatalf("create NNTP user %v: success flash %q has no username", form, success)
	}
	username, _, _ := strings.Cut(rest, ",")
	if len(username) < 10 {
		t.Fatalf("generated NNTP username %q is shorter than 10 characters", username)
	}
	u, err := w0DB(t).GetNNTPUserByUsernameAnyState(username)
	if err != nil {
		t.Fatalf("GetNNTPUserByUsernameAnyState(%s): %v", username, err)
	}
	return u
}

// adfNNTPUsersUpdate posts the edit form for the NNTP user id.
func adfNNTPUsersUpdate(t *testing.T, cookie *http.Cookie, id int, form url.Values) (success, failure string) {
	t.Helper()
	form.Set("id", strconv.Itoa(id))
	return adfNNTPUsersPost(t, cookie, "/admin/nntpusers/update", form)
}

// adfNNTPUsersWebUserID reads the raw web_user_id column of an NNTP user (Valid=false: NULL).
func adfNNTPUsersWebUserID(t *testing.T, id int) sql.NullInt64 {
	t.Helper()
	var v sql.NullInt64
	err := database.RetryableQueryRowScan(w0DB(t).GetMainDB(), `SELECT web_user_id FROM nntp_users WHERE id = ?`, []interface{}{id}, &v)
	if err != nil {
		t.Fatalf("read web_user_id of NNTP user %d: %v", id, err)
	}
	return v
}

// adfNNTPUsersGet reloads an NNTP user by id.
func adfNNTPUsersGet(t *testing.T, id int) *models.NNTPUser {
	t.Helper()
	u, err := w0DB(t).GetNNTPUserByID(id)
	if err != nil {
		t.Fatalf("GetNNTPUserByID(%d): %v", id, err)
	}
	return u
}

// TestAdfNNTPUsersCreateWithoutMapping: an NNTP user created without a mapping used to be
// linked to web user 1; now empty and 0 both store NULL (D9).
func TestAdfNNTPUsersCreateWithoutMapping(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	for _, raw := range []string{"", "0"} {
		u := adfNNTPUsersCreate(t, cookie, url.Values{"web_user_id": {raw}})
		if v := adfNNTPUsersWebUserID(t, u.ID); v.Valid {
			t.Errorf("web_user_id=%q: stored web_user_id = %d, want NULL", raw, v.Int64)
		}
	}
	// No field at all behaves the same.
	u := adfNNTPUsersCreate(t, cookie, url.Values{"maxconns": {"2"}})
	if v := adfNNTPUsersWebUserID(t, u.ID); v.Valid {
		t.Errorf("no web_user_id field: stored web_user_id = %d, want NULL", v.Int64)
	}
}

// TestAdfNNTPUsersCreateMappedOnce: a web user owns at most one NNTP account; a mapping
// to a missing web user is refused too.
func TestAdfNNTPUsersCreateMappedOnce(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	a, _ := w0NewUser(t, false)
	aID := strconv.FormatInt(a.ID, 10)

	u := adfNNTPUsersCreate(t, cookie, url.Values{"web_user_id": {aID}})
	if v := adfNNTPUsersWebUserID(t, u.ID); !v.Valid || v.Int64 != a.ID {
		t.Fatalf("stored web_user_id = %+v, want %d", v, a.ID)
	}

	_, failure := adfNNTPUsersPost(t, cookie, "/admin/nntpusers", url.Values{"web_user_id": {aID}})
	if !strings.Contains(failure, "web user already has an NNTP account") {
		t.Errorf("second create mapped to web user %d: error flash %q, want the refusal", a.ID, failure)
	}
	if n, err := w0DB(t).CountNNTPUsersByWebUserID(a.ID); err != nil || n != 1 {
		t.Errorf("NNTP accounts of web user %d = %d (%v), want 1", a.ID, n, err)
	}

	_, failure = adfNNTPUsersPost(t, cookie, "/admin/nntpusers", url.Values{"web_user_id": {"999999999"}})
	if !strings.Contains(failure, "web user not found") {
		t.Errorf("create mapped to a missing web user: error flash %q, want \"web user not found\"", failure)
	}
	_, failure = adfNNTPUsersPost(t, cookie, "/admin/nntpusers", url.Values{"web_user_id": {"-3"}})
	if failure == "" {
		t.Errorf("create mapped to web user -3: accepted, want a refusal")
	}
}

// TestAdfNNTPUsersUpdateMapping: the mapping can now be changed and removed in the form.
func TestAdfNNTPUsersUpdateMapping(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	a, _ := w0NewUser(t, false)
	b, _ := w0NewUser(t, false)
	aID := strconv.FormatInt(a.ID, 10)
	bID := strconv.FormatInt(b.ID, 10)

	u := adfNNTPUsersCreate(t, cookie, url.Values{"web_user_id": {aID}})

	// Re-submitting the current mapping is fine (the account already is A's one).
	if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"web_user_id": {aID}}); failure != "" {
		t.Errorf("update keeping the mapping to %d: refused: %q", a.ID, failure)
	}

	if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"web_user_id": {"0"}}); failure != "" {
		t.Fatalf("update mapping to 0: refused: %q", failure)
	}
	if v := adfNNTPUsersWebUserID(t, u.ID); v.Valid {
		t.Errorf("after mapping to 0: web_user_id = %d, want NULL", v.Int64)
	}

	if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"web_user_id": {bID}}); failure != "" {
		t.Fatalf("update mapping to %d: refused: %q", b.ID, failure)
	}
	if v := adfNNTPUsersWebUserID(t, u.ID); !v.Valid || v.Int64 != b.ID {
		t.Errorf("after mapping to %d: web_user_id = %+v", b.ID, v)
	}

	// A second account cannot take B, and a refused mapping changes nothing else either.
	other := adfNNTPUsersCreate(t, cookie, url.Values{"maxconns": {"3"}})
	_, failure := adfNNTPUsersUpdate(t, cookie, other.ID, url.Values{"web_user_id": {bID}, "maxconns": {"9"}})
	if !strings.Contains(failure, "web user already has an NNTP account") {
		t.Errorf("mapping a second account to %d: error flash %q, want the refusal", b.ID, failure)
	}
	if v := adfNNTPUsersWebUserID(t, other.ID); v.Valid {
		t.Errorf("refused mapping stored web_user_id = %d", v.Int64)
	}
	if got := adfNNTPUsersGet(t, other.ID).MaxConns; got != 3 {
		t.Errorf("refused update changed maxconns to %d, want 3", got)
	}

	// An update without the field keeps the mapping.
	if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"maxconns": {"4"}}); failure != "" {
		t.Fatalf("update without web_user_id: refused: %q", failure)
	}
	if v := adfNNTPUsersWebUserID(t, u.ID); !v.Valid || v.Int64 != b.ID {
		t.Errorf("update without web_user_id changed the mapping to %+v", v)
	}

	if _, failure := adfNNTPUsersUpdate(t, cookie, 999999999, url.Values{"web_user_id": {"0"}}); !strings.Contains(failure, "not found") {
		t.Errorf("update of a missing NNTP user: error flash %q, want \"not found\"", failure)
	}
}

// TestAdfNNTPUsersUpdatePostingWithoutMaxconns: posting used to change only together with
// maxconns; now it follows the form on its own and maxconns keeps its stored value.
func TestAdfNNTPUsersUpdatePostingWithoutMaxconns(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	u := adfNNTPUsersCreate(t, cookie, url.Values{"maxconns": {"7"}})
	if u.Posting || u.MaxConns != 7 {
		t.Fatalf("created posting=%v maxconns=%d, want false/7", u.Posting, u.MaxConns)
	}

	// The edit form sends a hidden "off" in front of the checkbox.
	if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"posting": {"off", "on"}}); failure != "" {
		t.Fatalf("update posting on: refused: %q", failure)
	}
	if got := adfNNTPUsersGet(t, u.ID); !got.Posting || got.MaxConns != 7 {
		t.Errorf("after posting on: posting=%v maxconns=%d, want true/7", got.Posting, got.MaxConns)
	}

	if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"posting": {"off"}}); failure != "" {
		t.Fatalf("update posting off: refused: %q", failure)
	}
	if got := adfNNTPUsersGet(t, u.ID); got.Posting || got.MaxConns != 7 {
		t.Errorf("after posting off: posting=%v maxconns=%d, want false/7", got.Posting, got.MaxConns)
	}
}

// TestAdfNNTPUsersMaxconnsRange: maxconns 1–25 (was 1–10), aligned with nntpmgr.
func TestAdfNNTPUsersMaxconnsRange(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	u := adfNNTPUsersCreate(t, cookie, url.Values{"maxconns": {"25"}})
	if u.MaxConns != 25 {
		t.Errorf("create with maxconns 25: stored %d", u.MaxConns)
	}
	if _, failure := adfNNTPUsersPost(t, cookie, "/admin/nntpusers", url.Values{"maxconns": {"26"}}); failure == "" {
		t.Errorf("create with maxconns 26: accepted, want a refusal")
	}

	if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"maxconns": {"5"}}); failure != "" {
		t.Fatalf("update maxconns 5: refused: %q", failure)
	}
	if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"maxconns": {"25"}}); failure != "" {
		t.Errorf("update maxconns 25: refused: %q", failure)
	}
	if got := adfNNTPUsersGet(t, u.ID).MaxConns; got != 25 {
		t.Errorf("after update maxconns 25: stored %d", got)
	}
	for _, bad := range []string{"26", "0", "x"} {
		if _, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"maxconns": {bad}}); failure == "" {
			t.Errorf("update maxconns %q: accepted, want a refusal", bad)
		}
	}
	if got := adfNNTPUsersGet(t, u.ID).MaxConns; got != 25 {
		t.Errorf("refused updates changed maxconns to %d, want 25", got)
	}
}

// TestAdfNNTPUsersPasswordLength: an admin-set password is 10–72 bytes (was 1–16).
func TestAdfNNTPUsersPasswordLength(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	u := adfNNTPUsersCreate(t, cookie, url.Values{})

	cases := []struct {
		length int
		ok     bool
	}{{9, false}, {10, true}, {72, true}, {73, false}}
	// bcrypt at the default cost is slow under -race: compare a hash only after an accepted
	// change; a refusal must leave the stored hash byte-for-byte unchanged.
	for _, tc := range cases {
		pw := strings.Repeat("p", tc.length)
		before := adfNNTPUsersGet(t, u.ID).Password
		_, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"password": {pw}})
		if tc.ok != (failure == "") {
			t.Errorf("password of %d bytes: error flash %q, want accepted=%v", tc.length, failure, tc.ok)
			continue
		}
		after := adfNNTPUsersGet(t, u.ID).Password
		if !tc.ok {
			if after != before {
				t.Errorf("refused %d-byte password changed the stored hash", tc.length)
			}
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(after), []byte(pw)); err != nil {
			t.Errorf("accepted %d-byte password: stored hash does not match: %v", tc.length, err)
		}
	}
}

// TestAdfNNTPUsersEditFormMapping: the edit form offers "no mapping" first, selected for an
// unmapped account; a mapped account keeps its current mapping selected even when the user
// list is empty, so saving the form does not unmap it by accident.
func TestAdfNNTPUsersEditFormMapping(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	a, _ := w0NewUser(t, false)
	unmapped := adfNNTPUsersCreate(t, cookie, url.Values{})
	mapped := adfNNTPUsersCreate(t, cookie, url.Values{"web_user_id": {strconv.FormatInt(a.ID, 10)}})

	page := func(nntpUser *models.NNTPUser) string {
		t.Helper()
		rec := w0Do(t, w0Req{
			Path:    "/admin",
			Form:    url.Values{"tab": {"nntpusers"}, "nntp_user_search": {nntpUser.Username}},
			Cookies: []*http.Cookie{cookie},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /admin?tab=nntpusers = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `name="posting" value="off"`) {
			t.Errorf("edit form lacks the hidden posting field")
		}
		return body
	}

	body := page(unmapped)
	if !strings.Contains(body, `<option value="0" selected>— no mapping —</option>`) {
		t.Errorf("unmapped account: \"no mapping\" option missing or not selected")
	}

	body = page(mapped)
	if !strings.Contains(body, `<option value="0" >— no mapping —</option>`) {
		t.Errorf("mapped account: \"no mapping\" option missing or selected")
	}
	want := `<option value="` + strconv.FormatInt(a.ID, 10) + `" selected>`
	if !strings.Contains(body, want) {
		t.Errorf("mapped account: current mapping %q not selected", want)
	}
}
