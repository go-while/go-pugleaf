package web

// Tests for slice fl-web of plan followup-leftovers, posting items:
//   - the Subject of a web post is limited so the stored "Subject: " line is at most 998 octets
//     (RFC 5322); a longer one is refused, the reply prefill starts within the limit;
//   - a reply to an article hidden for the poster (D2) does not copy its References;
//   - the preview API and the reply quote decode the body with models.DecodeBodyText (the
//     article's Content-Transfer-Encoding and charset; entities in a body stay text).

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
)

// flWebPostSetup gives the test the post queue (drained before and after) and an AbuseMail,
// both as TestAdfPostQueuedArticle does.
func flWebPostSetup(t *testing.T) {
	t.Helper()
	lo1HandDrainPostQueue()
	t.Cleanup(lo1HandDrainPostQueue)
	db := w0DB(t)
	old, err := db.GetConfigValue(config.CFG_KEY_ABUSEMAIL)
	if err != nil {
		t.Fatalf("GetConfigValue(%s): %v", config.CFG_KEY_ABUSEMAIL, err)
	}
	if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, "abuse@flweb.invalid"); err != nil {
		t.Fatalf("SetConfigValue(%s): %v", config.CFG_KEY_ABUSEMAIL, err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, old); err != nil {
			t.Errorf("restore %s: %v", config.CFG_KEY_ABUSEMAIL, err)
		}
	})
}

// flWebSubmit posts form to /SitePostSubmit as a fresh user (the back-off is per user) and
// returns the response body.
func flWebSubmit(t *testing.T, admin bool, form url.Values) string {
	t.Helper()
	_, cookie := w0NewUser(t, admin)
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/SitePostSubmit", Form: form, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /SitePostSubmit = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// flWebQueueEmpty fails when an article is waiting in models.PostQueueChannel.
func flWebQueueEmpty(t *testing.T, what string) {
	t.Helper()
	select {
	case a := <-models.PostQueueChannel:
		t.Fatalf("%s: article %s was queued", what, a.MessageID)
	default:
	}
}

func TestFlWebPostSubjectLimit(t *testing.T) {
	flWebPostSetup(t)
	if maxWebPostSubjectOctets != 989 {
		t.Fatalf("maxWebPostSubjectOctets = %d, want 998-len(\"Subject: \") = 989", maxWebPostSubjectOctets)
	}
	group := w0NewGroup(t, true)

	// The longest accepted Subject gives a header line of exactly 998 octets.
	subject := strings.Repeat("s", maxWebPostSubjectOctets)
	body := flWebSubmit(t, false, url.Values{"subject": {subject}, "body": {"b"}, "newsgroups": {group}})
	if strings.Contains(body, "alert-danger") || !strings.Contains(body, "queued for posting") {
		t.Fatalf("989-byte subject refused: %q", lo1HandShorten(body))
	}
	a := adfPostReceive(t)
	found := false
	for _, l := range strings.Split(a.HeadersJSON, "\n") {
		if len(l) > 998 {
			t.Errorf("stored header line of %d octets: %.60q...", len(l), l)
		}
		if l == "Subject: "+subject {
			found = true
			if len(l) != 998 {
				t.Errorf("Subject line = %d octets, want 998", len(l))
			}
		}
	}
	if !found {
		t.Fatalf("HeadersJSON lacks the Subject line")
	}

	// One octet more (ASCII, or a multi-byte rune across the limit) is refused.
	for name, subj := range map[string]string{
		"ascii": strings.Repeat("s", maxWebPostSubjectOctets+1),
		"utf8":  strings.Repeat("ü", (maxWebPostSubjectOctets+1)/2),
	} {
		body := flWebSubmit(t, false, url.Values{"subject": {subj}, "body": {"b"}, "newsgroups": {group}})
		if !strings.Contains(body, "Subject limited to 989 bytes") {
			t.Errorf("%s subject of %d bytes: no refusal: %q", name, len(subj), lo1HandShorten(body))
		}
		flWebQueueEmpty(t, name+" subject over the limit")
	}

	if got := cutToOctets("aü", 2); got != "a" {
		t.Errorf("cutToOctets(aü, 2) = %q, want a", got)
	}
	if got := cutToOctets("abc", 5); got != "abc" {
		t.Errorf("cutToOctets(abc, 5) = %q", got)
	}
}

// TestFlWebReplyPrefillSubjectWithinLimit: replying to an article whose Subject already uses
// the whole limit prefills "Re: " plus the Subject cut to the limit.
func TestFlWebReplyPrefillSubjectWithinLimit(t *testing.T) {
	long := strings.Repeat("x", maxWebPostSubjectOctets)
	mid := flWebMID("longsubj")
	group := flWebSeed(t, "flweblong", []flWebArt{{num: 1, mid: mid, subject: long, from: "a <a@test.invalid>", body: "b"}})
	_, cookie := w0NewUser(t, false)
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/SitePost", Cookies: []*http.Cookie{cookie},
		Form: url.Values{"newsgroup": {group}, "reply_to": {"1"}, "message_id": {mid}}})
	body := rec.Body.String()
	want := "Re: " + strings.Repeat("x", maxWebPostSubjectOctets-len("Re: "))
	if !strings.Contains(body, want) || strings.Contains(body, want+"x") {
		t.Fatalf("reply prefill subject is not cut to %d bytes", maxWebPostSubjectOctets)
	}
}

// flWebRefsFixture: root R, article 2 hidden (References R), article 3 visible (References R).
type flWebRefsFixture struct {
	group, root, hidden, visible string
}

func flWebRefsSeed(t *testing.T) flWebRefsFixture {
	t.Helper()
	f := flWebRefsFixture{root: flWebMID("refroot"), hidden: flWebMID("refhidden"), visible: flWebMID("refvisible")}
	f.group = flWebSeed(t, "flwebrefs", []flWebArt{
		{num: 1, mid: f.root, subject: "fl refs root", from: "r <r@test.invalid>", body: "r"},
		{num: 2, mid: f.hidden, subject: "Re: fl refs hidden", from: "h <h@test.invalid>", body: "h", refs: f.root, hide: 1},
		{num: 3, mid: f.visible, subject: "Re: fl refs visible", from: "v <v@test.invalid>", body: "v", refs: f.root},
	})
	return f
}

// TestFlWebReplyReferencesHidden: a reply to an article hidden for the poster names only that
// message-id; admins and replies to visible articles get the full chain.
func TestFlWebReplyReferencesHidden(t *testing.T) {
	flWebPostSetup(t)
	f := flWebRefsSeed(t)
	cases := []struct {
		name     string
		admin    bool
		replyTo  string
		parent   string
		wantRefs string
	}{
		{"user replies to hidden", false, "2", f.hidden, f.hidden},
		{"admin replies to hidden", true, "2", f.hidden, f.root + " " + f.hidden},
		{"user replies to visible", false, "3", f.visible, f.root + " " + f.visible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := flWebSubmit(t, tc.admin, url.Values{"subject": {"Re: fl refs"}, "body": {"reply"},
				"newsgroups": {f.group}, "reply_to": {tc.replyTo}, "message_id": {tc.parent}})
			if strings.Contains(body, "alert-danger") || !strings.Contains(body, "queued for posting") {
				t.Fatalf("reply refused: %q", lo1HandShorten(body))
			}
			a := adfPostReceive(t)
			if a.References != tc.wantRefs {
				t.Errorf("References = %q, want %q", a.References, tc.wantRefs)
			}
			if got := adfPostUnfold(strings.Split(a.HeadersJSON, "\n"), "References"); got != tc.wantRefs {
				t.Errorf("References header = %q, want %q", got, tc.wantRefs)
			}
		})
	}
}

// flWebQPHeaders is a raw header block declaring a quoted-printable UTF-8 body.
const flWebQPHeaders = "Subject: q\nContent-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: quoted-printable"

// flWebQPBody decodes to "Grüße &amp; =" with DecodeBodyText; ConvertToUTF8 also turned the
// entity into "&".
const flWebQPBody = "Gr=C3=BC=C3=9Fe &amp; =3D"

// TestFlWebBodyDecodingPreviewAndQuote: the preview API and the reply quote decode the body by
// its stored headers, like the article page; an entity in the body stays text.
func TestFlWebBodyDecodingPreviewAndQuote(t *testing.T) {
	const want = "Grüße &amp; ="
	if got := models.DecodeBodyText(flWebQPBody, flWebQPHeaders); got != want {
		t.Fatalf("DecodeBodyText = %q, want %q", got, want)
	}
	mid := flWebMID("qp")
	group := flWebSeed(t, "flwebqp", []flWebArt{{num: 1, mid: mid, subject: "qp", from: "q <q@test.invalid>",
		body: flWebQPBody, headers: flWebQPHeaders}})

	rec := w0Do(t, w0Req{Path: "/groups/" + group + "/articles/1/preview"})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Body string `json:"body"`
		From string `json:"from"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if resp.Body != want {
		t.Errorf("preview body = %q, want %q", resp.Body, want)
	}

	_, cookie := w0NewUser(t, false)
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/SitePost", Cookies: []*http.Cookie{cookie},
		Form: url.Values{"newsgroup": {group}, "reply_to": {"1"}, "message_id": {mid}}})
	// html/template escapes the quote: "&amp;" of the body text becomes "&amp;amp;".
	if body := rec.Body.String(); !strings.Contains(body, "&gt; Grüße &amp;amp; =") {
		quote := body
		if i := strings.Index(body, "wrote:"); i >= 0 {
			quote = body[i:]
		}
		t.Errorf("reply quote is not the DecodeBodyText of the body: %q", lo1HandShorten(quote))
	}
}
