package processor

// Remembered rejects (plan nntp-peering, slice history-rejects): the feed path
// (ProcessIncomingArticle) remembers every permanent article-level refusal in the history
// index, POST and temporary failures do not, and the bulk (fetcher/importer) path refuses a
// control message, remembers it and still re-evaluates an article whose only history row is a
// remembered reject.

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

const rejTestGroup = "rej.test.group"

// rejTestMsgID returns a message-id unique to this run (the msgid cache is global)
func rejTestMsgID(tag string) string {
	return fmt.Sprintf("<rej-%s-%d@test.invalid>", tag, time.Now().UnixNano())
}

// rejTestLines is an article as a feed or the fetcher hands it over; control != "" adds a
// Control header.
func rejTestLines(group, msgid, control string) []string {
	lines := []string{
		"Path: peer.test.invalid!not-for-mail",
		"From: Tester <tester@test.invalid>",
		"Newsgroups: " + group,
		"Subject: remembered rejects",
		"Date: Sun, 11 Oct 2026 12:00:00 +0000",
		"Message-ID: " + msgid,
	}
	if control != "" {
		lines = append(lines, "Control: "+control)
	}
	return append(lines, "", "remembered rejects body")
}

func rejTestArticle(t *testing.T, group, msgid, control string, bulkmode bool) *models.Article {
	t.Helper()
	art, err := nntp.ParseLegacyArticleLines(msgid, rejTestLines(group, msgid, control), bulkmode)
	if err != nil {
		t.Fatalf("ParseLegacyArticleLines: %v", err)
	}
	return art
}

// rejTestFeedArticle is rejTestArticle with the NewsgroupsPtr ProcessIncomingArticle needs
func rejTestFeedArticle(t *testing.T, group, msgid, control string) *models.Article {
	t.Helper()
	art := rejTestArticle(t, group, msgid, control, false)
	name := group
	art.NewsgroupsPtr = []*string{&name}
	return art
}

// rejTestWaitRemembered waits until messageID is a reject-only row of the history index
func rejTestWaitRemembered(t *testing.T, h *history.History, messageID string) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for {
		exists, rejectOnly, err := h.LookupReject(messageID)
		if err != nil {
			t.Fatalf("LookupReject(%s): %v", messageID, err)
		}
		if exists && rejectOnly {
			if ids, err := h.LookupGroups(messageID); err != nil || len(ids) != 0 {
				t.Fatalf("LookupGroups(%s) = %v, %v; want empty", messageID, ids, err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not remembered as rejected (exists=%v rejectOnly=%v)", messageID, exists, rejectOnly)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// rejTestNotRemembered waits for the history queue to settle and fails if messageID has a row
func rejTestNotRemembered(t *testing.T, h *history.History, messageID string) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !h.CheckNoMoreWorkInHistory() {
		if time.Now().After(deadline) {
			t.Fatalf("history still has pending work")
		}
		time.Sleep(5 * time.Millisecond)
	}
	exists, err := h.Exists(messageID)
	if err != nil {
		t.Fatalf("Exists(%s): %v", messageID, err)
	}
	if exists {
		t.Fatalf("%s is known to history, want no row", messageID)
	}
}

// TestRejectFeedPathRemembersControl: an article-level refusal of the feed path
// (IHAVE/TAKETHIS) is remembered, so the next CHECK/IHAVE/TAKETHIS of the id is refused.
func TestRejectFeedPathRemembersControl(t *testing.T) {
	history.NewMsgIdItemCache()
	h := wiringTestHistory(t)
	proc := &Processor{History: h, MsgIdCache: history.MsgIdCache}

	msgid := rejTestMsgID("feed-control")
	code, err := proc.ProcessIncomingArticle(rejTestFeedArticle(t, rejTestGroup, msgid, "cancel <victim@test.invalid>"))
	if code != history.CaseError || !errors.Is(err, common.ErrIntakeRejected) {
		t.Fatalf("ProcessIncomingArticle = %d, %v; want CaseError wrapping ErrIntakeRejected", code, err)
	}
	rejTestWaitRemembered(t, h, msgid)

	// known from now on: the feed path answers "duplicate" (CHECK 438 / IHAVE 435 / TAKETHIS 439)
	if got := proc.CheckMessageID(msgid); got != history.CaseDupes {
		t.Errorf("CheckMessageID = %#x, want CaseDupes", got)
	}
}

// TestRejectFeedPathMissingHeader: a missing mandatory header is remembered too.
func TestRejectFeedPathRemembersBadHeaders(t *testing.T) {
	history.NewMsgIdItemCache()
	h := wiringTestHistory(t)
	proc := &Processor{History: h, MsgIdCache: history.MsgIdCache}

	msgid := rejTestMsgID("feed-nosubject")
	art := rejTestFeedArticle(t, rejTestGroup, msgid, "")
	// drop the Subject line from the header block the intake checks
	var head []string
	for _, line := range art.NNTPhead {
		if !strings.HasPrefix(line, "Subject:") {
			head = append(head, line)
		}
	}
	art.NNTPhead = head
	art.Subject = ""

	code, err := proc.ProcessIncomingArticle(art)
	if code != history.CaseError || !errors.Is(err, common.ErrIntakeRejected) {
		t.Fatalf("ProcessIncomingArticle = %d, %v; want CaseError wrapping ErrIntakeRejected", code, err)
	}
	rejTestWaitRemembered(t, h, msgid)
}

// TestRejectFeedPathKeepsDuplicates: a duplicate is never turned into a reject, the stored
// locations stay.
func TestRejectFeedPathKeepsDuplicates(t *testing.T) {
	history.NewMsgIdItemCache()
	h := wiringTestHistory(t)
	proc := &Processor{History: h, MsgIdCache: history.MsgIdCache}

	msgid := rejTestMsgID("feed-dupe")
	wiringTestAddAndWait(t, h, msgid, 7)
	code, err := proc.ProcessIncomingArticle(rejTestFeedArticle(t, rejTestGroup, msgid, "cancel <x@test.invalid>"))
	if code != history.CaseDupes || err != nil {
		t.Fatalf("ProcessIncomingArticle = %d, %v; want CaseDupes, nil", code, err)
	}
	deadline := time.Now().Add(testWait)
	for !h.CheckNoMoreWorkInHistory() {
		if time.Now().After(deadline) {
			t.Fatalf("history still has pending work")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ids, err := h.LookupGroups(msgid)
	if err != nil || len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("LookupGroups = %v, %v; want [7] (the duplicate must keep its locations)", ids, err)
	}
}

// TestRejectFeedPathKeepsInFlight: an in-flight claim (CaseRetry) is no permanent refusal.
func TestRejectFeedPathKeepsInFlight(t *testing.T) {
	history.NewMsgIdItemCache()
	h := wiringTestHistory(t)
	proc := &Processor{History: h, MsgIdCache: history.MsgIdCache}

	msgid := rejTestMsgID("feed-inflight")
	wiringTestSetState(msgid, history.CaseLock)
	code, err := proc.ProcessIncomingArticle(rejTestFeedArticle(t, rejTestGroup, msgid, "cancel <x@test.invalid>"))
	if code != history.CaseRetry || err != nil {
		t.Fatalf("ProcessIncomingArticle = %d, %v; want CaseRetry, nil", code, err)
	}
	rejTestNotRemembered(t, h, msgid)
}

// TestRejectPostedArticleNotRemembered: POST refusals are not remembered (the user may fix
// the article and post it again with the same message-id).
func TestRejectPostedArticleNotRemembered(t *testing.T) {
	history.NewMsgIdItemCache()
	h := wiringTestHistory(t)
	proc := &Processor{History: h, MsgIdCache: history.MsgIdCache}

	msgid := rejTestMsgID("post-control")
	art := rejTestFeedArticle(t, rejTestGroup, msgid, "newgroup rej.test.new")
	art.Posted = true
	code, err := proc.ProcessPostedArticle(art, nil, 0)
	if code != history.CaseError || !errors.Is(err, common.ErrIntakeRejected) {
		t.Fatalf("ProcessPostedArticle = %d, %v; want CaseError wrapping ErrIntakeRejected", code, err)
	}
	rejTestNotRemembered(t, h, msgid)
}

// TestRejectBulkControl: the bulk (fetcher/importer) path refuses a control message like NNTP
// intake, remembers it and logs it once per group per run.
func TestRejectBulkControl(t *testing.T) {
	history.NewMsgIdItemCache()
	h := wiringTestHistory(t)
	proc := &Processor{History: h, MsgIdCache: history.MsgIdCache}
	logs := postCommitCaptureLog(t)
	group := fmt.Sprintf("rej.bulk.%d", time.Now().UnixNano())

	for i := 0; i < 2; i++ {
		msgid := rejTestMsgID(fmt.Sprintf("bulk-control-%d", i))
		code, err := proc.ProcessArticle(rejTestArticle(t, group, msgid, "cancel <victim@test.invalid>", true), group, true)
		if code != history.CaseError || !errors.Is(err, common.ErrIntakeRejected) {
			t.Fatalf("ProcessArticle %d = %d, %v; want CaseError wrapping ErrIntakeRejected", i, code, err)
		}
		if !strings.HasSuffix(err.Error(), ": control messages not accepted") {
			t.Fatalf("ProcessArticle %d err = %v; want the control reason", i, err)
		}
		rejTestWaitRemembered(t, h, msgid)
	}
	if n := strings.Count(logs.String(), "(logged once per group)"); n != 1 {
		t.Errorf("control refusal logged %d times for one group, want 1:\n%s", n, logs.String())
	}
}

// TestRejectBulkSitePostNotRemembered: a site post from the web queue (Posted) is refused like
// any other control message, but never remembered.
func TestRejectBulkSitePostNotRemembered(t *testing.T) {
	history.NewMsgIdItemCache()
	h := wiringTestHistory(t)
	proc := &Processor{History: h, MsgIdCache: history.MsgIdCache}

	msgid := rejTestMsgID("bulk-sitepost")
	art := rejTestArticle(t, rejTestGroup, msgid, "cancel <victim@test.invalid>", true)
	art.Posted = true
	code, err := proc.ProcessArticle(art, rejTestGroup, true)
	if code != history.CaseError || !errors.Is(err, common.ErrIntakeRejected) {
		t.Fatalf("ProcessArticle = %d, %v; want CaseError wrapping ErrIntakeRejected", code, err)
	}
	rejTestNotRemembered(t, h, msgid)
}

// TestRejectBulkRejectOnlyRowIsNoDuplicate: the bulk path evaluates an article whose only
// history row is a remembered reject (the group may exist by now) and its accept replaces the
// reject token.
func TestRejectBulkRejectOnlyRowIsNoDuplicate(t *testing.T) {
	history.NewMsgIdItemCache()
	db := flNntpdbOpenDB(t)
	h := wiringTestHistory(t)
	proc := &Processor{DB: db, History: h, MsgIdCache: history.MsgIdCache}
	db.Batch.SetProcessor(proc) // the batch writer records the committed article in history

	group := fmt.Sprintf("rej.bulk.accept.%d", time.Now().UnixNano())
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: group, Description: "history-rejects", Active: true, Status: "y", LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup: %v", err)
	}
	ng, err := db.MainDBGetNewsgroup(group)
	if err != nil || ng == nil || ng.ID <= 0 {
		t.Fatalf("MainDBGetNewsgroup: %v", err)
	}

	msgid := rejTestMsgID("bulk-accept")
	h.AddReject(msgid)
	rejTestWaitRemembered(t, h, msgid)
	// the 15 s in-flight cache must not make the bulk path skip the article either
	wiringTestSetState(msgid, history.CaseDupes)

	mark := db.Batch.CommitMark(group)
	code, err := proc.ProcessArticle(rejTestArticle(t, group, msgid, "", true), group, true)
	if code != history.CasePass || err != nil {
		t.Fatalf("ProcessArticle = %d, %v; want CasePass, nil (a reject-only row is no duplicate)", code, err)
	}
	if err := db.Batch.WaitCommitted(mark, nil, testWait); err != nil {
		t.Fatalf("WaitCommitted: %v", err)
	}

	deadline := time.Now().Add(testWait)
	for {
		ids, err := h.LookupGroups(msgid)
		if err != nil {
			t.Fatalf("LookupGroups: %v", err)
		}
		if len(ids) == 1 && ids[0] == ng.ID {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("LookupGroups = %v, want [%d]: the accept must replace the reject token", ids, ng.ID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRejectControlRefusalLogSet(t *testing.T) {
	var s controlRefusalLogSet
	if !s.first("a") || s.first("a") {
		t.Errorf("the first refusal of a group is logged once")
	}
	if !s.first("b") {
		t.Errorf("another group is logged too")
	}
	for i := 0; len(s.groups) < controlRefusalLogMax; i++ {
		s.first(fmt.Sprintf("g%d", i))
	}
	if s.first("past-the-cap") {
		t.Errorf("a group past the cap must not be logged")
	}
}

func TestRejectBulkControlHeader(t *testing.T) {
	msgid := "<bulk-control-header@test.invalid>"
	art := rejTestArticle(t, rejTestGroup, msgid, "cancel <victim@test.invalid>", true)
	// the parsed headers of a fetched article keep no Control: the checked block decides
	lines := strings.Split(art.HeadersJSON, "\n")
	if got := bulkControlHeader(art, lines); got != "cancel" {
		t.Errorf("bulkControlHeader = %q, want cancel", got)
	}
	if got := bulkControlHeader(art, nil); got != "" {
		t.Errorf("bulkControlHeader without a block = %q, want empty", got)
	}
	plain := rejTestArticle(t, rejTestGroup, msgid, "", true)
	if got := bulkControlHeader(plain, strings.Split(plain.HeadersJSON, "\n")); got != "" {
		t.Errorf("bulkControlHeader of an ordinary article = %q, want empty", got)
	}
	withMap := &models.Article{MessageID: msgid, Headers: map[string][]string{"control": {"newgroup alt.test y"}}}
	if got := bulkControlHeader(withMap, nil); got != "newgroup" {
		t.Errorf("bulkControlHeader from the headers map = %q, want newgroup", got)
	}
}
