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
			code, perr := proc.ProcessArticle(bcArticle(t, msgid, lines), group, true)
			if code != history.CaseError || perr == nil || perr.Error() != err.Error() {
				t.Fatalf("ProcessArticle = %d, %v; CheckBulkArticle = %v: want the same refusal", code, perr, err)
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
