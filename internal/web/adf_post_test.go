package web

import (
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// adfPostHeaderCounts counts the field lines of a header block by lower-case name;
// continuation lines (leading SP/TAB) are counted under "" .
func adfPostHeaderCounts(lines []string) map[string]int {
	counts := make(map[string]int)
	for _, l := range lines {
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			counts[""]++
			continue
		}
		name, _, _ := strings.Cut(l, ":")
		counts[strings.ToLower(name)]++
	}
	return counts
}

// adfPostHeaderValue returns the value of the first field line called name (case-insensitive).
func adfPostHeaderValue(lines []string, name string) string {
	prefix := strings.ToLower(name) + ":"
	for _, l := range lines {
		if strings.HasPrefix(strings.ToLower(l), prefix) {
			return strings.TrimSpace(l[len(prefix):])
		}
	}
	return ""
}

// adfPostUnfold returns the unfolded value of the first field called name: the field line
// plus its continuation lines, whitespace runs collapsed to one space.
func adfPostUnfold(lines []string, name string) string {
	prefix := strings.ToLower(name) + ":"
	for i, l := range lines {
		if !strings.HasPrefix(strings.ToLower(l), prefix) {
			continue
		}
		value := l[len(prefix):]
		for _, c := range lines[i+1:] {
			if !strings.HasPrefix(c, " ") && !strings.HasPrefix(c, "\t") {
				break
			}
			value += c
		}
		return strings.Join(strings.Fields(value), " ")
	}
	return ""
}

func adfPostHeaderInput() webPostHeaderInput {
	return webPostHeaderInput{
		Host:       "adfpost.test.invalid",
		From:       "adfpost user <noreply@pugleaf.net.invalid>",
		Subject:    "Re: adfpost subject",
		MessageID:  "<adf-adfpost-1@test.invalid>",
		References: "<adf-adfpost-0@test.invalid> <adf-adfpost-00@test.invalid>",
		AbuseMail:  "abuse@adfpost.invalid",
		HashedUser: "0123abcd",
		Nonce:      "1760000000",
		Newsgroups: []string{"adfpost.a", "adfpost.b"},
		Now:        time.Date(2026, 10, 15, 12, 34, 56, 0, time.FixedZone("X", 2*3600)),
		Body:       "line one\nline two\n\nline four",
	}
}

func TestAdfPostHeaderBuilder(t *testing.T) {
	in := adfPostHeaderInput()
	lines, date, size := buildWebPostHeaders(in)

	wantOrder := []string{"path", "from", "newsgroups", "subject", "date", "message-id", "references",
		"mime-version", "content-type", "content-transfer-encoding", "injection-date", "injection-info", "lines"}
	var gotOrder []string
	for _, l := range lines {
		if strings.HasPrefix(l, " ") {
			continue
		}
		name, _, _ := strings.Cut(l, ":")
		gotOrder = append(gotOrder, strings.ToLower(name))
	}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("header order = %v, want %v", gotOrder, wantOrder)
	}
	counts := adfPostHeaderCounts(lines)
	for _, name := range []string{"path", "from", "newsgroups", "subject", "date", "message-id", "references",
		"injection-date", "injection-info", "lines"} {
		if counts[name] != 1 {
			t.Errorf("%s: %d lines, want exactly 1", name, counts[name])
		}
	}
	for _, name := range []string{"x-pugleaf-trace", "bytes"} {
		if counts[name] != 0 {
			t.Errorf("%s: %d lines, want none", name, counts[name])
		}
	}

	if got := adfPostHeaderValue(lines, "Path"); got != ".POSTED!not-for-mail" {
		t.Errorf("Path = %q", got)
	}
	if got := adfPostHeaderValue(lines, "Newsgroups"); got != "adfpost.a,adfpost.b" {
		t.Errorf("Newsgroups = %q", got)
	}
	if got := adfPostHeaderValue(lines, "Message-ID"); got != in.MessageID {
		t.Errorf("Message-ID = %q, want %q", got, in.MessageID)
	}
	if got := adfPostUnfold(lines, "References"); got != in.References {
		t.Errorf("References = %q, want %q", got, in.References)
	}
	if got := adfPostHeaderValue(lines, "Lines"); got != "4" {
		t.Errorf("Lines = %q, want 4", got)
	}

	// Date: RFC 5322, UTC, the same instant as Injection-Date
	if want := common.FormatNewsDate(in.Now); date != want {
		t.Errorf("date = %q, want %q", date, want)
	}
	if got := adfPostHeaderValue(lines, "Date"); got != date {
		t.Errorf("Date header = %q, want %q", got, date)
	}
	if got := adfPostHeaderValue(lines, "Injection-Date"); got != date {
		t.Errorf("Injection-Date = %q, want %q", got, date)
	}
	parsed, err := mail.ParseDate(date)
	if err != nil {
		t.Fatalf("mail.ParseDate(%q): %v", date, err)
	}
	if !parsed.Equal(in.Now) {
		t.Errorf("Date parses to %v, want %v", parsed, in.Now)
	}

	// Injection-Info is folded: field line plus continuation lines starting with a space.
	idx := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "Injection-Info:") {
			idx = i
		}
	}
	if idx < 0 || idx+3 >= len(lines) {
		t.Fatalf("Injection-Info missing or truncated: %q", lines)
	}
	wantInfo := []string{
		"Injection-Info: adfpost.test.invalid;",
		` posting-account="0123abcd";`,
		` logging-data="1760000000";`,
		` mail-complaints-to="abuse@adfpost.invalid"`,
	}
	for i, w := range wantInfo {
		if lines[idx+i] != w {
			t.Errorf("Injection-Info line %d = %q, want %q", i, lines[idx+i], w)
		}
	}

	if want := common.WireArticleSize(lines, strings.Split(in.Body, "\n")); size != want {
		t.Errorf("bytes = %d, want %d", size, want)
	}
	if _, err := common.CheckHeaderBlock(lines, common.HeaderBlockFetch); err != nil {
		t.Errorf("CheckHeaderBlock(fetch): %v", err)
	}
	if _, err := common.CheckHeaderBlock(lines, common.HeaderBlockStrict); err != nil {
		t.Errorf("CheckHeaderBlock(strict): %v", err)
	}

	// A deep reply chain: References is folded onto continuation lines, no line passes
	// 998 octets and the unfolded value is the input.
	var deep []string
	for i := 0; i < 20; i++ {
		deep = append(deep, fmt.Sprintf("<adf-adfpost-deep-%02d.%s@test.invalid>", i, strings.Repeat("x", 40)))
	}
	in.References = strings.Join(deep, " ")
	lines, _, size = buildWebPostHeaders(in)
	for _, l := range lines {
		if len(l) > 998 {
			t.Errorf("deep References: line of %d octets: %q", len(l), l)
		}
	}
	if got := adfPostUnfold(lines, "References"); got != in.References {
		t.Errorf("deep References unfolds to %q, want %q", got, in.References)
	}
	if n := adfPostHeaderCounts(lines)["references"]; n != 1 {
		t.Errorf("deep References: %d field lines, want 1", n)
	}
	if want := common.WireArticleSize(lines, strings.Split(in.Body, "\n")); size != want {
		t.Errorf("deep References: bytes = %d, want %d", size, want)
	}
	if _, err := common.CheckHeaderBlock(lines, common.HeaderBlockFetch); err != nil {
		t.Errorf("deep References CheckHeaderBlock(fetch): %v", err)
	}
	if _, err := common.CheckHeaderBlock(lines, common.HeaderBlockStrict); err != nil {
		t.Errorf("deep References CheckHeaderBlock(strict): %v", err)
	}

	// A new thread has no References; without AbuseMail there is no mail-complaints-to.
	in.References = ""
	in.AbuseMail = ""
	lines, _, _ = buildWebPostHeaders(in)
	if n := adfPostHeaderCounts(lines)["references"]; n != 0 {
		t.Errorf("non-reply: %d References lines, want 0", n)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "mail-complaints-to") {
		t.Errorf("no AbuseMail: block still has mail-complaints-to:\n%s", joined)
	}
	if !strings.Contains(joined, "\n posting-account=\"0123abcd\";\n logging-data=\"1760000000\"\nLines: 4") {
		t.Errorf("no AbuseMail: Injection-Info not folded as expected:\n%s", joined)
	}
	if _, err := common.CheckHeaderBlock(lines, common.HeaderBlockFetch); err != nil {
		t.Errorf("non-reply CheckHeaderBlock(fetch): %v", err)
	}
}

// adfPostReceive reads one article from models.PostQueueChannel or fails after testWait.
// Nothing else consumes that channel in this test binary: NewWebServer starts no
// processor.PostQueueWorker (only cmd/web does), see lo1HandDrainPostQueue.
func adfPostReceive(t *testing.T) *models.Article {
	t.Helper()
	select {
	case a := <-models.PostQueueChannel:
		return a
	case <-time.After(testWait):
		t.Fatalf("no article in models.PostQueueChannel within %v", testWait)
		return nil
	}
}

func TestAdfPostQueuedArticle(t *testing.T) {
	lo1HandDrainPostQueue()
	t.Cleanup(lo1HandDrainPostQueue)

	db := w0DB(t)
	oldAbuse, err := db.GetConfigValue(config.CFG_KEY_ABUSEMAIL)
	if err != nil {
		t.Fatalf("GetConfigValue(%s): %v", config.CFG_KEY_ABUSEMAIL, err)
	}
	if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, "abuse@adfpost.invalid"); err != nil {
		t.Fatalf("SetConfigValue(%s): %v", config.CFG_KEY_ABUSEMAIL, err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_ABUSEMAIL, oldAbuse); err != nil {
			t.Errorf("restore %s: %v", config.CFG_KEY_ABUSEMAIL, err)
		}
	})

	group := w0NewGroup(t, true)
	cases := []struct {
		name    string
		form    url.Values
		replyTo string
		// CRLF body: the stored body has LF only, Lines and Bytes count each line once
		wantBody      string
		wantBodyBytes int // body part of a.Bytes: sum of len(line)+2
	}{
		{name: "thread", form: url.Values{"subject": {"adfpost thread"}, "body": {"hello\nworld"}}},
		{name: "crlf", form: url.Values{"subject": {"adfpost crlf"}, "body": {"a\r\nbb\r\n\r\nccc\r\n"}},
			wantBody: "a\nbb\n\nccc", wantBodyBytes: (1 + 2) + (2 + 2) + (0 + 2) + (3 + 2)},
		{name: "reply", form: url.Values{"subject": {"Re: adfpost thread"}, "body": {"> hello\n\nreply"},
			"reply_to": {"1"}, "message_id": {"<adf-adfpost-1@test.invalid>"}}, replyTo: "<adf-adfpost-1@test.invalid>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cookie := w0NewUser(t, false) // a fresh user: the back-off is per user
			tc.form.Set("newsgroups", group)
			rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/SitePostSubmit", Form: tc.form, Cookies: []*http.Cookie{cookie}})
			if rec.Code != http.StatusOK {
				t.Fatalf("POST /SitePostSubmit = %d, want 200", rec.Code)
			}
			if body := rec.Body.String(); strings.Contains(body, "alert-danger") || !strings.Contains(body, "queued for posting") {
				t.Fatalf("submit did not succeed: %q", lo1HandShorten(body))
			}

			a := adfPostReceive(t)
			lines := strings.Split(a.HeadersJSON, "\n")
			if !common.ValidMessageID(a.MessageID) || !strings.HasSuffix(a.MessageID, "@"+processor.LocalNNTPHostname+">") {
				t.Errorf("MessageID = %q, want a valid id at %s", a.MessageID, processor.LocalNNTPHostname)
			}
			for _, want := range []string{
				"Path: .POSTED!not-for-mail",
				"Newsgroups: " + group,
				"Message-ID: " + a.MessageID,
				"Subject: " + tc.form.Get("subject"),
				"Date: " + a.DateString,
				"Injection-Date: " + a.DateString,
				"Injection-Info: " + processor.LocalNNTPHostname + ";",
				` mail-complaints-to="abuse@adfpost.invalid"`,
			} {
				found := false
				for _, l := range lines {
					if l == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("HeadersJSON lacks line %q:\n%s", want, a.HeadersJSON)
				}
			}
			counts := adfPostHeaderCounts(lines)
			if counts["x-pugleaf-trace"] != 0 || counts["bytes"] != 0 {
				t.Errorf("HeadersJSON still has X-pugleaf-Trace or Bytes:\n%s", a.HeadersJSON)
			}
			if _, err := mail.ParseDate(a.DateString); err != nil {
				t.Errorf("DateString %q: %v", a.DateString, err)
			}
			if want := common.WireArticleSize(lines, strings.Split(a.BodyText, "\n")); a.Bytes != want {
				t.Errorf("Bytes = %d, want %d", a.Bytes, want)
			}
			if want := strings.Count(a.BodyText, "\n") + 1; a.Lines != want {
				t.Errorf("Lines = %d, want %d", a.Lines, want)
			}
			if tc.wantBody != "" {
				if a.BodyText != tc.wantBody {
					t.Errorf("BodyText = %q, want %q", a.BodyText, tc.wantBody)
				}
				if want := strings.Count(tc.wantBody, "\n") + 1; a.Lines != want || adfPostHeaderValue(lines, "Lines") != fmt.Sprint(want) {
					t.Errorf("Lines = %d (header %q), want %d", a.Lines, adfPostHeaderValue(lines, "Lines"), want)
				}
				if want := common.WireArticleSize(lines, nil) + tc.wantBodyBytes; a.Bytes != want {
					t.Errorf("Bytes = %d, want %d", a.Bytes, want)
				}
			}
			if a.Path != ".POSTED!not-for-mail" {
				t.Errorf("Path = %q", a.Path)
			}
			if _, ok := a.Headers["bytes"]; ok {
				t.Errorf("article.Headers still has \"bytes\"")
			}
			if _, err := common.CheckHeaderBlock(lines, common.HeaderBlockFetch); err != nil {
				t.Errorf("CheckHeaderBlock(fetch): %v", err)
			}

			refs := adfPostUnfold(lines, "References")
			if tc.replyTo == "" {
				if counts["references"] != 0 || a.IsReply {
					t.Errorf("thread: References %q, IsReply %v", refs, a.IsReply)
				}
				return
			}
			if refs != tc.replyTo || a.References != tc.replyTo || len(a.RefSlice) != 1 || a.RefSlice[0] != tc.replyTo {
				t.Errorf("reply: References header %q, field %q, RefSlice %v; want %q", refs, a.References, a.RefSlice, tc.replyTo)
			}
		})
	}
}
