package processor

// CheckBulkArticle (plan importer-empty-groups): the article-only checks of ProcessArticle's
// bulk path, run without side effects by a bulk caller before it creates a group, refuse
// exactly what ProcessArticle refuses, with the same reason.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// bcLines is a good bulk article whose named headers are replaced (or, with value "",
// dropped), with extra header lines appended and the given body.
func bcLines(msgid, group string, replace map[string]string, extra []string, body string) []string {
	head := [][2]string{
		{"Path", "peer.test.invalid!not-for-mail"},
		{"From", "Tester <tester@test.invalid>"},
		{"Newsgroups", group},
		{"Subject", "bulk check"},
		{"Date", "Sun, 11 Oct 2026 12:00:00 +0000"},
		{"Message-ID", msgid},
	}
	var lines []string
	for _, h := range head {
		v := h[1]
		if r, ok := replace[h[0]]; ok {
			v = r
		}
		if v == "" {
			continue
		}
		lines = append(lines, h[0]+": "+v)
	}
	lines = append(lines, extra...)
	return append(lines, "", body)
}

func bcArticle(t *testing.T, msgid string, lines []string) *models.Article {
	t.Helper()
	art, err := nntp.ParseLegacyArticleLines(msgid, lines, true)
	if err != nil {
		t.Fatalf("ParseLegacyArticleLines: %v", err)
	}
	return art
}

// TestBulkCheckSameReasonsAsProcessArticle: for every article-only reject of the bulk path,
// CheckBulkArticle returns the error ProcessArticle returns, and nil for a good article.
func TestBulkCheckSameReasonsAsProcessArticle(t *testing.T) {
	history.NewMsgIdItemCache()
	db := flNntpdbOpenDB(t)
	proc := &Processor{DB: db, MsgIdCache: history.MsgIdCache}
	group := fmt.Sprintf("bc.test.%d", time.Now().UnixNano())

	tooMany := make([]string, db.MaxCrossPosts()+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("bc.many.g%d", i)
	}
	for _, tc := range []struct {
		name    string
		replace map[string]string
		extra   []string
		body    string
		reason  string // "" = passes
	}{
		{name: "good"},
		{name: "nul in body", body: "a\x00b", reason: "article contains NUL"},
		{name: "duplicate subject", extra: []string{"Subject: second subject"}, reason: "invalid header block"},
		{name: "message-id header differs", replace: map[string]string{"Message-ID": "<other@test.invalid>"}, reason: "invalid message-id"},
		{name: "control", extra: []string{"Control: cancel <victim@test.invalid>"}, reason: "control messages not accepted"},
		{name: "no newsgroups", replace: map[string]string{"Newsgroups": ""}, reason: "missing mandatory header"},
		{name: "invalid newsgroups", replace: map[string]string{"Newsgroups": ","}, reason: "invalid newsgroups"},
		{name: "too many newsgroups", replace: map[string]string{"Newsgroups": strings.Join(tooMany, ",")}, reason: "too many newsgroups"},
		{name: "no subject", replace: map[string]string{"Subject": ""}, reason: "missing mandatory header"},
		{name: "no from", replace: map[string]string{"From": ""}, reason: "missing mandatory header"},
		{name: "invalid date", replace: map[string]string{"Date": "garbage"}, reason: "invalid date"},
		{name: "date in the future", replace: map[string]string{"Date": "Mon, 01 Jan 2099 00:00:00 +0000"}, reason: "date in the future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgid := fmt.Sprintf("<bc-%d@test.invalid>", time.Now().UnixNano())
			body := tc.body
			if body == "" {
				body = "bulk check body"
			}
			lines := bcLines(msgid, group, tc.replace, tc.extra, body)
			err := proc.CheckBulkArticle(bcArticle(t, msgid, lines))
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("CheckBulkArticle = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, common.ErrIntakeRejected) || !strings.HasSuffix(err.Error(), ": "+tc.reason) {
				t.Fatalf("CheckBulkArticle = %v, want ErrIntakeRejected with reason %q", err, tc.reason)
			}
			slots := CaptureInFlight()
			code, perr := proc.ProcessArticle(bcArticle(t, msgid, lines), group, true)
			if code != history.CaseError || perr == nil || perr.Error() != err.Error() {
				t.Fatalf("ProcessArticle = %d, %v; CheckBulkArticle = %v: want the same refusal", code, perr, err)
			}
			// the refusal frees its capture slot and leaves the id CaseDupes, never CaseLock
			// (more slots than before is a leak; fewer can be an earlier test's capture ending)
			if n := CaptureInFlight(); n > slots {
				t.Errorf("capture slots in flight %d after the refusal, want at most %d", n, slots)
			}
			if state := wiringTestState(history.MsgIdCache.GetORCreate(msgid)); state != history.CaseDupes {
				t.Errorf("message-id cache state %d after the refusal, want CaseDupes (%d)", state, history.CaseDupes)
			}
		})
	}
}

// TestBulkCheckPassingArticleStored: an article CheckBulkArticle passes is stored by
// ProcessArticle's bulk path with what the checks produced, as the inline code did before
// the checks moved: the date (the Date header, or the fallback header when Date is
// unusable), the header block without the line the fetch-mode check drops, and the Path
// prefixed with the local hostname.
func TestBulkCheckPassingArticleStored(t *testing.T) {
	history.NewMsgIdItemCache()
	db := flNntpdbOpenDB(t)
	h := wiringTestHistory(t)
	proc := &Processor{DB: db, History: h, MsgIdCache: history.MsgIdCache}
	db.Batch.SetProcessor(proc)
	group := fmt.Sprintf("bc.pass.%d", time.Now().UnixNano())
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: group, Description: "bulk check", Active: true, Status: "y", LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup: %v", err)
	}

	for _, tc := range []struct {
		name     string
		replace  map[string]string
		extra    []string
		headers  map[string]string // put into the parsed header map (the parser keeps only a few)
		wantDate string            // date_sent as stored (UTC)
	}{
		{name: "date header", wantDate: "2026-10-11 12:00:00"},
		{name: "date fallback", replace: map[string]string{"Date": "garbage"},
			headers: map[string]string{"nntp-posting-date": "Mon, 12 Oct 2026 08:30:00 +0000"}, wantDate: "2026-10-12 08:30:00"},
		{name: "dropped junk line", extra: []string{"junk line without a colon"}, wantDate: "2026-10-11 12:00:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgid := fmt.Sprintf("<bc-pass-%d@test.invalid>", time.Now().UnixNano())
			lines := bcLines(msgid, group, tc.replace, tc.extra, "stored body")
			build := func() *models.Article {
				art := bcArticle(t, msgid, lines)
				for k, v := range tc.headers {
					art.Headers[k] = []string{v}
				}
				return art
			}
			if err := proc.CheckBulkArticle(build()); err != nil {
				t.Fatalf("CheckBulkArticle = %v, want nil", err)
			}
			mark := db.Batch.CommitMark(group)
			if code, err := proc.ProcessArticle(build(), group, true); code != history.CasePass || err != nil {
				t.Fatalf("ProcessArticle = %d, %v; want CasePass, nil", code, err)
			}
			if err := db.Batch.WaitCommitted(mark, nil, testWait); err != nil {
				t.Fatalf("WaitCommitted: %v", err)
			}
			g, err := db.GetGroupDB(group)
			if err != nil {
				t.Fatalf("GetGroupDB: %v", err)
			}
			defer g.Return()
			var date, headers string
			if err := g.DB.QueryRow(`SELECT strftime('%Y-%m-%d %H:%M:%S', date_sent), COALESCE(headers_json, '') FROM articles WHERE message_id = ?`,
				msgid).Scan(&date, &headers); err != nil {
				t.Fatalf("read stored article: %v", err)
			}
			if date != tc.wantDate {
				t.Errorf("date_sent %q, want %q", date, tc.wantDate)
			}
			wantPath := "Path: " + LocalNNTPHostname + "!peer.test.invalid!not-for-mail"
			if !strings.Contains(headers, wantPath) || !strings.Contains(headers, "Subject: bulk check") || strings.Contains(headers, "junk line") {
				t.Errorf("stored headers %q: want %q, the Subject and no junk line", headers, wantPath)
			}
		})
	}
}

// TestBulkCheckNoSideEffects: CheckBulkArticle leaves no message-id cache entry, no history
// row, no log line about it and an unchanged article, for a refused and for a good article.
func TestBulkCheckNoSideEffects(t *testing.T) {
	history.NewMsgIdItemCache()
	db := flNntpdbOpenDB(t)
	h := wiringTestHistory(t)
	proc := &Processor{DB: db, History: h, MsgIdCache: history.MsgIdCache}
	logs := postCommitCaptureLog(t)
	group := fmt.Sprintf("bc.side.%d", time.Now().UnixNano())

	for _, tc := range []struct {
		name  string
		extra []string
		want  bool // refused
	}{
		{"good", nil, false},
		{"control", []string{"Control: cancel <victim@test.invalid>"}, true},
	} {
		msgid := fmt.Sprintf("<bc-side-%s-%d@test.invalid>", tc.name, time.Now().UnixNano())
		art := bcArticle(t, msgid, bcLines(msgid, group, nil, tc.extra, "side effects"))
		headersJSON, subject := art.HeadersJSON, art.Subject
		err := proc.CheckBulkArticle(art)
		if (err != nil) != tc.want {
			t.Fatalf("%s: CheckBulkArticle = %v, want refused=%t", tc.name, err, tc.want)
		}
		if history.MsgIdCache.Delete(msgid) {
			t.Errorf("%s: CheckBulkArticle left a message-id cache entry", tc.name)
		}
		if art.Headers == nil || art.HeadersJSON != headersJSON || art.Subject != subject || !art.DateSent.IsZero() || art.MsgIdItem != nil {
			t.Errorf("%s: CheckBulkArticle changed the article", tc.name)
		}
		if s := logs.String(); strings.Contains(s, msgid) {
			t.Errorf("%s: CheckBulkArticle logged:\n%s", tc.name, s)
		}
		rejTestNotRemembered(t, h, msgid)
	}
	if err := proc.CheckBulkArticle(nil); !errors.Is(err, common.ErrIntakeRejected) {
		t.Errorf("CheckBulkArticle(nil) = %v, want ErrIntakeRejected", err)
	}
}
