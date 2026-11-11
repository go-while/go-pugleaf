package web

// Admin forms of plan e2e-gap-tests, slice intake-policy: the per-peer header_check field of
// /admin/peers and the two peer Date limits of /admin/settings (PeerArticleMaxAgeHours,
// PeerArticleMaxFutureHours; 0 = off).

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
)

// TestIpwPeerHeaderCheckForm: the peers form stores header_check, an empty field keeps the
// default "relay" and an unknown value is refused with the stored row unchanged.
func TestIpwPeerHeaderCheckForm(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	name := w0Name("ipw-hc")
	form := adfPeersForm(name)
	form.Set("header_check", "strict")
	p := adfPeersCreate(t, cookie, form)
	if p.HeaderCheck != database.PeerHeaderCheckStrict {
		t.Fatalf("created peer header_check = %q, want %q", p.HeaderCheck, database.PeerHeaderCheckStrict)
	}

	// an edit without the field falls back to the column default
	edit := adfPeersForm(name)
	edit.Set("id", strconv.FormatInt(p.ID, 10))
	if _, failure := adfPeersPost(t, cookie, "/admin/peers/update", edit); failure != "" {
		t.Fatalf("edit without header_check: refused: %q", failure)
	}
	if got := adfPeersGet(t, p.ID); got.HeaderCheck != database.PeerHeaderCheckRelay {
		t.Errorf("header_check after an edit without the field = %q, want %q", got.HeaderCheck, database.PeerHeaderCheckRelay)
	}

	// an unknown value is refused and nothing is written
	bad := adfPeersForm(name)
	bad.Set("id", strconv.FormatInt(p.ID, 10))
	bad.Set("header_check", "lax")
	if _, failure := adfPeersPost(t, cookie, "/admin/peers/update", bad); failure == "" {
		t.Error("edit with header_check \"lax\": want an error flash")
	}
	if got := adfPeersGet(t, p.ID); got.HeaderCheck != database.PeerHeaderCheckRelay {
		t.Errorf("header_check after the refused edit = %q, want %q", got.HeaderCheck, database.PeerHeaderCheckRelay)
	}
}

// TestIpwPeerDateLimitSettings: the settings form stores the two peer Date limits, 0 turns a
// limit off, and values outside 0..config.PeerDateHoursLimit are refused.
func TestIpwPeerDateLimitSettings(t *testing.T) {
	db := w0DB(t)
	_, cookie := w0NewUser(t, true)
	for _, c := range []struct {
		field, key string
		read       func() time.Duration
	}{
		{config.FORM_FIELD_PEERDATEMAXAGE, config.CFG_KEY_PEERDATEMAXAGE, db.PeerDateMaxAge},
		{config.FORM_FIELD_PEERDATEMAXFUTURE, config.CFG_KEY_PEERDATEMAXFUTURE, db.PeerDateMaxFuture},
	} {
		old, err := db.GetConfigValue(c.key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.SetConfigValue(c.key, old); err != nil {
				t.Errorf("restoring %s: %v", c.key, err)
			}
		})
		post := func(value string) *http.Response {
			rec := w0Do(t, w0Req{
				Method:  http.MethodPost,
				Path:    "/admin/settings",
				Form:    url.Values{"setting": {c.field}, c.field: {value}},
				Cookies: []*http.Cookie{cookie},
			})
			return rec.Result()
		}
		if res := post("12"); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s 12: status %d, want 303", c.field, res.StatusCode)
		}
		if got := c.read(); got != 12*time.Hour {
			t.Errorf("%s after saving 12: %v, want 12h", c.key, got)
		}
		if res := post("0"); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s 0: status %d, want 303", c.field, res.StatusCode)
		}
		if got := c.read(); got != 0 {
			t.Errorf("%s after saving 0: %v, want 0 (the limit is off)", c.key, got)
		}
		for _, bad := range []string{"-1", strconv.Itoa(config.PeerDateHoursLimit + 1), "x", ""} {
			if res := post(bad); res.StatusCode != http.StatusSeeOther {
				t.Errorf("POST %s %q: status %d, want 303 with an error", c.field, bad, res.StatusCode)
			}
			if v, err := db.GetConfigValue(c.key); err != nil {
				t.Fatal(err)
			} else if v != "0" {
				t.Errorf("%s = %q after the refused value %q, want \"0\"", c.key, v, bad)
			}
		}
	}
}
