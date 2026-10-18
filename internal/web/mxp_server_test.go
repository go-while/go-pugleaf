package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/config"
)

// TestMxpServerMaxCrosspostsSetting: the admin settings form stores MaxCrossPosts, refuses
// values outside 1..config.MaxCrossPostsLimit, and the site post form enforces the stored value.
func TestMxpServerMaxCrosspostsSetting(t *testing.T) {
	db := w0DB(t)
	old, err := db.GetConfigValue(config.CFG_KEY_MAXCROSSPOSTS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_MAXCROSSPOSTS, old); err != nil {
			t.Errorf("restoring %s: %v", config.CFG_KEY_MAXCROSSPOSTS, err)
		}
	})

	// The site post handler refuses every post while AbuseMail is unset.
	oldAbuse, err := db.GetConfigValue(config.CFG_KEY_ABUSEMAIL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, "abuse@mxp.invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, oldAbuse); err != nil {
			t.Errorf("restoring %s: %v", config.CFG_KEY_ABUSEMAIL, err)
		}
	})

	_, cookie := w0NewUser(t, true)
	post := func(value string) *httptest.ResponseRecorder {
		return w0Do(t, w0Req{
			Method:  http.MethodPost,
			Path:    "/admin/settings",
			Form:    url.Values{"setting": {config.FORM_FIELD_MAXCROSSPOSTS}, config.FORM_FIELD_MAXCROSSPOSTS: {value}},
			Cookies: []*http.Cookie{cookie},
		})
	}
	stored := func() string {
		t.Helper()
		v, err := db.GetConfigValue(config.CFG_KEY_MAXCROSSPOSTS)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	if rec := post("2"); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST max crossposts 2: status %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if v := stored(); v != "2" {
		t.Fatalf("stored %s = %q after saving 2, want \"2\"", config.CFG_KEY_MAXCROSSPOSTS, v)
	}
	if got := db.MaxCrossPosts(); got != 2 {
		t.Fatalf("MaxCrossPosts() = %d after saving 2, want 2: the form does not reach the accessor", got)
	}

	for _, bad := range []string{"0", "33", "x", ""} {
		if rec := post(bad); rec.Code != http.StatusSeeOther {
			t.Errorf("POST max crossposts %q: status %d, want 303 redirect with an error", bad, rec.Code)
		}
		if v := stored(); v != "2" {
			t.Errorf("stored %s = %q after rejected value %q, want \"2\"", config.CFG_KEY_MAXCROSSPOSTS, v, bad)
		}
	}

	// The site post form reports the configured limit, not a hardcoded 9.
	rec := w0Do(t, w0Req{
		Method: http.MethodPost,
		Path:   routeSitePostSubmit,
		Form: url.Values{
			"newsgroups": {"a.test b.test c.test"},
			"subject":    {"mxp"},
			"body":       {"mxp"},
		},
		Cookies: []*http.Cookie{cookie},
	})
	if !strings.Contains(rec.Body.String(), "maximum of 2 newsgroups") {
		t.Errorf("site post with 3 groups: status %d, body lacks \"maximum of 2 newsgroups\": %q", rec.Code, w0Trim(rec.Body.String()))
	}

	rec = w0Do(t, w0Req{Method: http.MethodGet, Path: "/admin?tab=settings", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="`+config.FORM_FIELD_MAXCROSSPOSTS+`"`) {
		t.Errorf("admin settings tab: status %d, form field %s missing", rec.Code, config.FORM_FIELD_MAXCROSSPOSTS)
	}
}

// w0Trim shortens a response body for a failure message.
func w0Trim(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

// TestMxpServerReverseProxyInput: the admin form stores the trusted-proxy list with commas
// whatever separator was typed, refuses a list with an entry that is not an IP or CIDR, and the
// loader treats such a list as an error instead of skipping the entry.
func TestMxpServerReverseProxyInput(t *testing.T) {
	db := w0DB(t)
	old, err := db.GetConfigValue(config.CFG_KEY_REVERSEPROXY)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_REVERSEPROXY, old); err != nil {
			t.Errorf("restoring %s: %v", config.CFG_KEY_REVERSEPROXY, err)
		}
	})
	_, cookie := w0NewUser(t, true)
	post := func(value string) *httptest.ResponseRecorder {
		return w0Do(t, w0Req{
			Method:  http.MethodPost,
			Path:    "/admin/settings",
			Form:    url.Values{"setting": {config.FORM_FIELD_REVERSEPROXY}, config.FORM_FIELD_REVERSEPROXY: {value}},
			Cookies: []*http.Cookie{cookie},
		})
	}
	stored := func() string {
		t.Helper()
		v, err := db.GetConfigValue(config.CFG_KEY_REVERSEPROXY)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	if rec := post("127.0.0.1; ::1  10.0.0.0/8"); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST proxy list: status %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if v := stored(); v != "127.0.0.1, ::1, 10.0.0.0/8" {
		t.Fatalf("stored %s = %q, want \"127.0.0.1, ::1, 10.0.0.0/8\" (semicolons and blanks normalized to commas)", config.CFG_KEY_REVERSEPROXY, v)
	}
	for _, bad := range []string{"127.0.0.1, nope", "10.0.0.0/33", "127.0.0.1;;::1;x"} {
		if rec := post(bad); rec.Code != http.StatusSeeOther {
			t.Errorf("POST proxy list %q: status %d, want 303 redirect with an error", bad, rec.Code)
		}
		if v := stored(); v != "127.0.0.1, ::1, 10.0.0.0/8" {
			t.Errorf("stored %s = %q after rejected value %q, want it unchanged", config.CFG_KEY_REVERSEPROXY, v, bad)
		}
	}
	if rec := post(""); rec.Code != http.StatusSeeOther {
		t.Errorf("POST empty proxy list: status %d, want 303", rec.Code)
	}
	if v := stored(); v != "" {
		t.Errorf("stored %s = %q after clearing, want empty", config.CFG_KEY_REVERSEPROXY, v)
	}

	// The loader: a semicolon list parses, a list with junk is an error, empty means the defaults.
	if nets, err := configureTrustedProxies(NewRouter(), "127.0.0.1; ::1"); err != nil || len(nets) != 2 {
		t.Errorf("configureTrustedProxies(\"127.0.0.1; ::1\") = %v, %v; want 2 nets, nil", nets, err)
	}
	if _, err := configureTrustedProxies(NewRouter(), "127.0.0.1, bad/x"); err == nil || !strings.Contains(err.Error(), "bad/x") {
		t.Errorf("configureTrustedProxies with a junk entry: err = %v, want an error naming bad/x", err)
	}
	if nets, err := configureTrustedProxies(NewRouter(), ""); err != nil || len(nets) != len(DefaultReverseProxy) {
		t.Errorf("configureTrustedProxies(\"\") = %d nets, %v; want the %d defaults", len(nets), err, len(DefaultReverseProxy))
	}
}
