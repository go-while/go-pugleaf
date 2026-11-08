package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// Tests of the admin peers page (/admin/peers, plan nntp-peering, slice peers-admin).

// adfPeersPost posts form to path as the session of cookie and returns the flash messages the
// handler left. Every peers handler answers with a 303 back to the page.
func adfPeersPost(t *testing.T, cookie *http.Cookie, path string, form url.Values) (success, failure string) {
	t.Helper()
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: path, Form: form, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST %s = %d, want 303", path, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/peers" {
		t.Errorf("POST %s redirected to %q, want /admin/peers", path, loc)
	}
	success, _ = GetAndClearFlash(cookie.Value, "success")
	_, failure = GetAndClearFlash(cookie.Value, "error")
	return success, failure
}

// adfPeersForm is the create/edit form of a peer named name, with every field filled.
func adfPeersForm(name string) url.Values {
	return url.Values{
		"name":          {name},
		"path_token":    {name},
		"hosts":         {"192.0.2.7, 2001:db8::/32, news.example.org"},
		"in_username":   {name + "-in"},
		"in_password":   {"in-password-0123"},
		"in_patterns":   {"*,!control*"},
		"out_enabled":   {"off", "on"},
		"out_host":      {"news.example.org"},
		"out_port":      {"1119"},
		"out_tls":       {"off", "on"},
		"out_username":  {name + "-out"},
		"out_password":  {"out-password-0123"},
		"out_patterns":  {"*,!control*,!junk"},
		"out_flags":     {"Nm"},
		"max_conns":     {"3"},
		"enabled":       {"off", "on"},
		"notes":         {"created by the peers-admin test"},
		"unknown_field": {"ignored"}, // a field no handler reads must not matter
	}
}

// adfPeersCreate creates a peer through the handler and returns the stored row.
func adfPeersCreate(t *testing.T, cookie *http.Cookie, form url.Values) *models.Peer {
	t.Helper()
	success, failure := adfPeersPost(t, cookie, "/admin/peers", form)
	if failure != "" {
		t.Fatalf("create peer %q: refused: %q", form.Get("name"), failure)
	}
	if !strings.Contains(success, form.Get("name")) {
		t.Errorf("create peer %q: success flash %q does not name the peer", form.Get("name"), success)
	}
	p, err := w0DB(t).GetPeerByName(form.Get("name"))
	if err != nil {
		t.Fatalf("GetPeerByName(%s): %v", form.Get("name"), err)
	}
	return p
}

// adfPeersGet reloads a peer by id.
func adfPeersGet(t *testing.T, id int64) *models.Peer {
	t.Helper()
	p, err := w0DB(t).GetPeerByID(id)
	if err != nil {
		t.Fatalf("GetPeerByID(%d): %v", id, err)
	}
	return p
}

// adfPeersUpdate posts the edit form for the peer id.
func adfPeersUpdate(t *testing.T, cookie *http.Cookie, id int64, form url.Values) (success, failure string) {
	t.Helper()
	form.Set("id", strconv.FormatInt(id, 10))
	return adfPeersPost(t, cookie, "/admin/peers/update", form)
}

// TestAdfPeersCreate: the create form fills every column, the two passwords are stored (the
// incoming one hashed, the outgoing one as given) and the page lists the peer without them.
func TestAdfPeersCreate(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, cookie, adfPeersForm(name))

	if p.PathToken != name || p.InUsername != name+"-in" || p.OutUsername != name+"-out" {
		t.Errorf("stored path_token=%q in_username=%q out_username=%q", p.PathToken, p.InUsername, p.OutUsername)
	}
	// ValidatePeer normalises the hosts list to a comma-separated one without spaces.
	if p.Hosts != "192.0.2.7,2001:db8::/32,news.example.org" {
		t.Errorf("stored hosts = %q", p.Hosts)
	}
	if p.InPatterns != "*,!control*" || p.OutPatterns != "*,!control*,!junk" || p.OutFlags != "Nm" {
		t.Errorf("stored in_patterns=%q out_patterns=%q out_flags=%q", p.InPatterns, p.OutPatterns, p.OutFlags)
	}
	if !p.OutEnabled || p.OutHost != "news.example.org" || p.OutPort != 1119 || !p.OutTLS {
		t.Errorf("stored out_enabled=%v out_host=%q out_port=%d out_tls=%v", p.OutEnabled, p.OutHost, p.OutPort, p.OutTLS)
	}
	if p.MaxConns != 3 || !p.Enabled || p.Notes == "" {
		t.Errorf("stored max_conns=%d enabled=%v notes=%q", p.MaxConns, p.Enabled, p.Notes)
	}
	if p.InPasswordHash == "" || p.InPasswordHash == "in-password-0123" {
		t.Errorf("in_password_hash = %q, want a bcrypt hash", p.InPasswordHash)
	}
	if p.OutPassword != "out-password-0123" {
		t.Errorf("out_password = %q, want the password as given", p.OutPassword)
	}

	// The page lists the peer and never shows a password or a hash.
	rec := w0Do(t, w0Req{Path: "/admin/peers", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/peers = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, name) {
		t.Errorf("peers page does not list peer %q", name)
	}
	for _, secret := range []string{p.InPasswordHash, "in-password-0123", "out-password-0123"} {
		if strings.Contains(body, secret) {
			t.Errorf("peers page shows the secret %q", secret)
		}
	}
	// The password state is shown instead.
	if !strings.Contains(body, "password set") {
		t.Errorf("peers page does not show that the peer login password is set")
	}
}

// TestAdfPeersCreateDefaults: the empty pattern, port and max_conns fields fall back to the
// column defaults of ValidatePeer, and empty hosts with a login mean "AUTHINFO only".
func TestAdfPeersCreateDefaults(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, cookie, url.Values{"name": {name}, "path_token": {name}, "in_username": {name + "-login"}, "enabled": {"on"}})

	if p.Hosts != "" {
		t.Errorf("hosts = %q, want empty (AUTHINFO only)", p.Hosts)
	}
	if p.InPatterns != "*" || p.OutPatterns != "*,!control*,!junk" {
		t.Errorf("in_patterns=%q out_patterns=%q, want the defaults", p.InPatterns, p.OutPatterns)
	}
	if p.OutPort != 119 || p.MaxConns != 4 {
		t.Errorf("out_port=%d max_conns=%d, want 119/4", p.OutPort, p.MaxConns)
	}
	if p.OutEnabled || p.InPasswordHash != "" || p.OutPassword != "" {
		t.Errorf("out_enabled=%v, in hash %q, out password %q: want off and no passwords", p.OutEnabled, p.InPasswordHash, p.OutPassword)
	}
}

// TestAdfPeersUpdateKeepsPasswords: an edit with empty password fields keeps both stored
// passwords and writes every other field.
func TestAdfPeersUpdateKeepsPasswords(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, cookie, adfPeersForm(name))

	form := adfPeersForm(name)
	form.Set("in_password", "")
	form.Set("out_password", "")
	form.Set("hosts", "198.51.100.0/24")
	form.Set("in_patterns", "comp.*,!comp.lang.*")
	form.Set("max_conns", "9")
	form.Set("notes", "edited")
	if _, failure := adfPeersUpdate(t, cookie, p.ID, form); failure != "" {
		t.Fatalf("update peer %d: refused: %q", p.ID, failure)
	}

	got := adfPeersGet(t, p.ID)
	if got.InPasswordHash != p.InPasswordHash {
		t.Errorf("empty in_password changed the stored hash")
	}
	if got.OutPassword != p.OutPassword {
		t.Errorf("empty out_password changed the stored password to %q", got.OutPassword)
	}
	if got.Hosts != "198.51.100.0/24" || got.InPatterns != "comp.*,!comp.lang.*" || got.MaxConns != 9 || got.Notes != "edited" {
		t.Errorf("update wrote hosts=%q in_patterns=%q max_conns=%d notes=%q", got.Hosts, got.InPatterns, got.MaxConns, got.Notes)
	}

	// A non-empty field replaces the password.
	form.Set("out_password", "out-password-second")
	if _, failure := adfPeersUpdate(t, cookie, p.ID, form); failure != "" {
		t.Fatalf("update with a new out password: refused: %q", failure)
	}
	if got := adfPeersGet(t, p.ID); got.OutPassword != "out-password-second" {
		t.Errorf("out_password = %q, want the new one", got.OutPassword)
	}
}

// TestAdfPeersUpdateKeepsNumbers: an edit that submits no port and no connection limit keeps
// the stored ones instead of resetting them to the column defaults.
func TestAdfPeersUpdateKeepsNumbers(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, cookie, adfPeersForm(name))
	if p.OutPort != 1119 || p.MaxConns != 3 {
		t.Fatalf("created peer has out_port=%d max_conns=%d, want 1119/3", p.OutPort, p.MaxConns)
	}

	form := adfPeersForm(name)
	form.Set("in_password", "")
	form.Set("out_password", "")
	form.Set("out_port", "")
	form.Set("max_conns", "")
	if _, failure := adfPeersUpdate(t, cookie, p.ID, form); failure != "" {
		t.Fatalf("update without the numbers: refused: %q", failure)
	}
	if got := adfPeersGet(t, p.ID); got.OutPort != 1119 || got.MaxConns != 3 {
		t.Errorf("after an update without the numbers: out_port=%d max_conns=%d, want 1119/3", got.OutPort, got.MaxConns)
	}

	// A submitted number still wins.
	form.Set("out_port", "563")
	form.Set("max_conns", "8")
	if _, failure := adfPeersUpdate(t, cookie, p.ID, form); failure != "" {
		t.Fatalf("update with new numbers: refused: %q", failure)
	}
	if got := adfPeersGet(t, p.ID); got.OutPort != 563 || got.MaxConns != 8 {
		t.Errorf("after the update: out_port=%d max_conns=%d, want 563/8", got.OutPort, got.MaxConns)
	}
}

// TestAdfPeersInPasswordLength: the peer login password is bcrypt-hashed, so the form bounds it
// like the NNTP user form (10..72 bytes); the outgoing password is the remote's choice and is
// stored whatever its length.
func TestAdfPeersInPasswordLength(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, cookie, adfPeersForm(name))

	for _, length := range []int{nntpAdminPasswordMin - 1, nntpAdminPasswordMax + 1} {
		create := adfPeersForm(w0Name("adfpeer"))
		create.Set("in_password", strings.Repeat("p", length))
		if _, failure := adfPeersPost(t, cookie, "/admin/peers", create); !strings.Contains(failure, "between") {
			t.Errorf("create with a %d-byte peer login password: error flash %q, want the length refusal", length, failure)
		}

		edit := adfPeersForm(name)
		edit.Set("in_password", strings.Repeat("p", length))
		if _, failure := adfPeersUpdate(t, cookie, p.ID, edit); !strings.Contains(failure, "between") {
			t.Errorf("update with a %d-byte peer login password: error flash %q, want the length refusal", length, failure)
		}
		if got := adfPeersGet(t, p.ID); got.InPasswordHash != p.InPasswordHash {
			t.Errorf("refused %d-byte peer login password changed the stored hash", length)
		}
	}

	// A short outgoing password is the peer's business and is accepted.
	edit := adfPeersForm(name)
	edit.Set("in_password", "")
	edit.Set("out_password", "short")
	if _, failure := adfPeersUpdate(t, cookie, p.ID, edit); failure != "" {
		t.Fatalf("update with a 5-byte outgoing password: refused: %q", failure)
	}
	if got := adfPeersGet(t, p.ID); got.OutPassword != "short" {
		t.Errorf("out_password = %q, want \"short\"", got.OutPassword)
	}
}

// TestAdfPeersClearPasswords: the clear checkbox clears a password even when the field holds
// one, and the page then reports it as not set.
func TestAdfPeersClearPasswords(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, cookie, adfPeersForm(name))
	if p.InPasswordHash == "" || p.OutPassword == "" {
		t.Fatalf("created peer has no passwords to clear")
	}

	form := adfPeersForm(name)
	form.Set("in_password_clear", "on")
	form.Set("out_password_clear", "on")
	if _, failure := adfPeersUpdate(t, cookie, p.ID, form); failure != "" {
		t.Fatalf("update with both clear boxes: refused: %q", failure)
	}
	got := adfPeersGet(t, p.ID)
	if got.InPasswordHash != "" {
		t.Errorf("in_password_hash = %q, want empty after clear", got.InPasswordHash)
	}
	if got.OutPassword != "" {
		t.Errorf("out_password = %q, want empty after clear", got.OutPassword)
	}

	rec := w0Do(t, w0Req{Path: "/admin/peers", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/peers = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no password") {
		t.Errorf("peers page does not show the cleared peer login password as unset")
	}
}

// TestAdfPeersInvalidInput: every refusal reaches the admin as an error flash, nothing is
// written and nothing answers with a 500.
func TestAdfPeersInvalidInput(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, cookie, adfPeersForm(name))

	cases := []struct {
		what string
		set  map[string]string
		want string
	}{
		{"no name", map[string]string{"name": ""}, "name is empty"},
		{"path token with !", map[string]string{"path_token": "a!b"}, "path token"},
		{"path token with a space", map[string]string{"path_token": "a b"}, "path token"},
		{"bad hosts entry", map[string]string{"hosts": "bad_host"}, "hosts entry"},
		{"port out of range", map[string]string{"out_port": "70000"}, "out_port"},
		{"port not a number", map[string]string{"out_port": "x"}, "must be a number"},
		{"max_conns out of range", map[string]string{"max_conns": "65"}, "max_conns"},
		{"outgoing feed without a host", map[string]string{"out_host": ""}, "without out_host"},
	}
	for _, tc := range cases {
		form := adfPeersForm(w0Name("adfpeer"))
		for k, v := range tc.set {
			form.Set(k, v)
		}
		_, failure := adfPeersPost(t, cookie, "/admin/peers", form)
		if failure == "" {
			t.Errorf("create with %s: accepted, want a refusal", tc.what)
			continue
		}
		if !strings.Contains(failure, tc.want) {
			t.Errorf("create with %s: error flash %q, want it to mention %q", tc.what, failure, tc.want)
		}

		// The same input is refused on an edit, and the stored peer is unchanged.
		before := adfPeersGet(t, p.ID)
		editForm := adfPeersForm(name)
		for k, v := range tc.set {
			editForm.Set(k, v)
		}
		if _, failure := adfPeersUpdate(t, cookie, p.ID, editForm); failure == "" {
			t.Errorf("update with %s: accepted, want a refusal", tc.what)
		}
		if after := adfPeersGet(t, p.ID); *after != *before {
			t.Errorf("refused update with %s changed the stored peer", tc.what)
		}
	}

	// A duplicate name and a duplicate peer login are refused with a message, not a 500.
	dup := adfPeersForm(w0Name("adfpeer"))
	dup.Set("name", name)
	if _, failure := adfPeersPost(t, cookie, "/admin/peers", dup); !strings.Contains(failure, "already exists") {
		t.Errorf("create with a used name: error flash %q, want \"already exists\"", failure)
	}
	dup = adfPeersForm(w0Name("adfpeer"))
	dup.Set("in_username", name+"-in")
	if _, failure := adfPeersPost(t, cookie, "/admin/peers", dup); !strings.Contains(failure, "already used by peer") {
		t.Errorf("create with a used peer login: error flash %q, want the login refusal", failure)
	}

	// A missing or unparsable id is a message too.
	for _, path := range []string{"/admin/peers/update", "/admin/peers/delete", "/admin/peers/toggle"} {
		if _, failure := adfPeersPost(t, cookie, path, url.Values{"id": {""}}); !strings.Contains(failure, "Invalid peer ID") {
			t.Errorf("POST %s without an id: error flash %q, want \"Invalid peer ID\"", path, failure)
		}
		if _, failure := adfPeersPost(t, cookie, path, url.Values{"id": {"999999999"}}); !strings.Contains(failure, "not found") {
			t.Errorf("POST %s with an unknown id: error flash %q, want \"not found\"", path, failure)
		}
	}
}

// TestAdfPeersToggleAndDelete: the toggle flips enabled, delete removes the row.
func TestAdfPeersToggleAndDelete(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, cookie, adfPeersForm(name))
	if !p.Enabled {
		t.Fatalf("created peer is disabled")
	}

	id := url.Values{"id": {strconv.FormatInt(p.ID, 10)}}
	if _, failure := adfPeersPost(t, cookie, "/admin/peers/toggle", id); failure != "" {
		t.Fatalf("toggle peer %d off: refused: %q", p.ID, failure)
	}
	if got := adfPeersGet(t, p.ID); got.Enabled {
		t.Errorf("peer still enabled after the first toggle")
	}
	if _, failure := adfPeersPost(t, cookie, "/admin/peers/toggle", id); failure != "" {
		t.Fatalf("toggle peer %d on: refused: %q", p.ID, failure)
	}
	if got := adfPeersGet(t, p.ID); !got.Enabled {
		t.Errorf("peer still disabled after the second toggle")
	}
	// The toggle does not touch the passwords either.
	if got := adfPeersGet(t, p.ID); got.InPasswordHash != p.InPasswordHash || got.OutPassword != p.OutPassword {
		t.Errorf("toggle changed a stored password")
	}

	// An enabled peer is not deleted: the handler refuses it, like the disabled button does.
	if _, failure := adfPeersPost(t, cookie, "/admin/peers/delete", id); !strings.Contains(failure, "disable it first") {
		t.Errorf("delete of an enabled peer: error flash %q, want \"disable it first\"", failure)
	}
	if _, err := w0DB(t).GetPeerByID(p.ID); err != nil {
		t.Fatalf("refused delete removed peer %d: %v", p.ID, err)
	}

	if _, failure := adfPeersPost(t, cookie, "/admin/peers/toggle", id); failure != "" {
		t.Fatalf("toggle peer %d off before the delete: refused: %q", p.ID, failure)
	}
	if _, failure := adfPeersPost(t, cookie, "/admin/peers/delete", id); failure != "" {
		t.Fatalf("delete peer %d: refused: %q", p.ID, failure)
	}
	if _, err := w0DB(t).GetPeerByID(p.ID); err == nil {
		t.Errorf("peer %d still exists after the delete", p.ID)
	}
	// Deleting it twice is a message, not a 500.
	if _, failure := adfPeersPost(t, cookie, "/admin/peers/delete", id); !strings.Contains(failure, "not found") {
		t.Errorf("second delete: error flash %q, want \"not found\"", failure)
	}
}

// TestAdfPeersNonAdminRefused: a plain user and an anonymous visitor reach no peers route, and
// no non-admin request changes a peer.
func TestAdfPeersNonAdminRefused(t *testing.T) {
	_, admin := w0NewUser(t, true)
	name := w0Name("adfpeer")
	p := adfPeersCreate(t, admin, adfPeersForm(name))

	_, plain := w0NewUser(t, false)
	countBefore := adfPeersCount(t)
	id := strconv.FormatInt(p.ID, 10)
	posts := []struct {
		path string
		form url.Values
	}{
		{"/admin/peers", adfPeersForm(w0Name("adfpeer"))},
		{"/admin/peers/update", url.Values{"id": {id}, "name": {name}, "path_token": {"evil"}}},
		{"/admin/peers/delete", url.Values{"id": {id}}},
		{"/admin/peers/toggle", url.Values{"id": {id}}},
	}

	// Logged in without the admin permission: redirected to /profile with "Access denied".
	rec := w0Do(t, w0Req{Path: "/admin/peers", Cookies: []*http.Cookie{plain}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/profile" {
		t.Errorf("GET /admin/peers as a plain user = %d %q, want 303 /profile", rec.Code, rec.Header().Get("Location"))
	}
	if _, failure := GetAndClearFlash(plain.Value, "error"); failure != "Access denied" {
		t.Errorf("GET /admin/peers as a plain user: error flash %q, want \"Access denied\"", failure)
	}
	for _, tc := range posts {
		rec := w0Do(t, w0Req{Method: http.MethodPost, Path: tc.path, Form: tc.form, Cookies: []*http.Cookie{plain}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/profile" {
			t.Errorf("POST %s as a plain user = %d %q, want 303 /profile", tc.path, rec.Code, rec.Header().Get("Location"))
		}
		GetAndClearFlash(plain.Value, "error")
	}

	// Anonymous: redirected to /login.
	rec = w0Do(t, w0Req{Path: "/admin/peers"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET /admin/peers anonymously = %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}
	for _, tc := range posts {
		rec := w0Do(t, w0Req{Method: http.MethodPost, Path: tc.path, Form: tc.form})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("POST %s anonymously = %d %q, want 303 /login", tc.path, rec.Code, rec.Header().Get("Location"))
		}
	}

	// Nothing was created, changed or deleted.
	got := adfPeersGet(t, p.ID)
	if *got != *p {
		t.Errorf("a non-admin request changed peer %d", p.ID)
	}
	if n := adfPeersCount(t); n != countBefore {
		t.Errorf("peers after the non-admin requests = %d, want %d", n, countBefore)
	}
}

// adfPeersCount returns the number of configured peers.
func adfPeersCount(t *testing.T) int {
	t.Helper()
	peers, err := w0DB(t).GetPeers()
	if err != nil {
		t.Fatalf("GetPeers: %v", err)
	}
	return len(peers)
}
