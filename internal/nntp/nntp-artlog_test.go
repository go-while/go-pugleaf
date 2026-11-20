package nntp

// Tests of the INN-style article log of rejected feed articles (plan inn-reject-log): every
// 437 (IHAVE) and 439 (TAKETHIS) gives exactly one peerstats.Collector Article call with the
// code and the reply text the peer got (without the message-id in front), accepted and
// deferred articles give none. The last test runs a real peerstats.Service and reads the file.
//
// Identifiers start with al / TestArtLog. No test asserts on wall-clock time.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/peerstats"
	"github.com/go-while/nntp-testkit/client"
)

// alControlReason is the processor's reject of a control message (internal/processor/intake.go).
const alControlReason = "control messages not accepted"

// alID returns a unique message-id that carries marker, which alProcessor dispatches on.
func alID(marker string) string {
	return fmt.Sprintf("<al-%s-%d@test.invalid>", marker, hSeq.Add(1))
}

// alProcessor answers CheckMessageID and ProcessIncomingArticle by the marker in the id:
// dupcheck/inprog/histerr in the offered id decide the history answer, control/storedup/
// procretry/temp in the article's Message-ID the intake result; everything else passes.
func alProcessor() *hFakeProcessor {
	return &hFakeProcessor{
		checkFn: func(id string) int {
			switch {
			case strings.Contains(id, "-dupcheck-"):
				return history.CaseDupes
			case strings.Contains(id, "-inprog-"):
				return history.CaseRetry
			case strings.Contains(id, "-histerr-"):
				return history.CaseError
			}
			return history.CasePass
		},
		processFn: func(a *models.Article) (int, error) {
			switch {
			case strings.Contains(a.MessageID, "-control-"):
				return history.CaseError, fmt.Errorf("%w: %s", common.ErrIntakeRejected, alControlReason)
			case strings.Contains(a.MessageID, "-storedup-"):
				return history.CaseDupes, nil
			case strings.Contains(a.MessageID, "-procretry-"):
				return history.CaseRetry, nil // another connection holds the claim
			case strings.Contains(a.MessageID, "-temp-"):
				return history.CaseRetry, common.ErrIntakeTemporary
			}
			return history.CasePass, nil
		},
	}
}

// alArticleRaw is one article in the form the kit transfer commands take (CRLF, not
// dot-stuffed, no terminator). headerID is its Message-ID header, extra more header lines.
func alArticleRaw(headerID, path, groups string, extra ...string) []byte {
	var b strings.Builder
	b.WriteString("Path: " + path + "\r\n")
	b.WriteString("From: Tester <tester@test.invalid>\r\n")
	b.WriteString("Newsgroups: " + groups + "\r\n")
	b.WriteString("Subject: al\r\n")
	b.WriteString("Date: Fri, 20 Nov 2026 15:17:19 +0000\r\n")
	b.WriteString("Message-ID: " + headerID + "\r\n")
	for _, h := range extra {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\nbody line\r\n")
	return []byte(b.String())
}

// alCase is one feed article: the id the command offers, the article and the expected reply
// and article log call (wantText "" = no Article call).
type alCase struct {
	name     string
	cmdID    string
	article  []byte
	wantCode int
	wantText string
}

// alPath is a Path that does not hold the server identity of psServer ("us.ps.invalid").
const alPath = "peer.al.invalid!not-for-mail"

// alCases are the IHAVE or TAKETHIS cases (code is the reject code: 437 or 439).
func alCases(code int) []alCase {
	takethis := code == 439
	big := strings.Repeat("filler line to pass the peer's in_max_size\r\n", 60)
	g := hGroupName("al.grp")
	var cases []alCase
	add := func(name, marker string, mk func(id string) []byte, wantCode int, wantText string) {
		id := alID(marker)
		cases = append(cases, alCase{name: name, cmdID: id, article: mk(id), wantCode: wantCode, wantText: wantText})
	}
	plain := func(id string) []byte { return alArticleRaw(id, alPath, g) }
	accepted := 235
	if takethis {
		accepted = 239
	}
	add("accepted", "ok", plain, accepted, "")
	add("too large", "big", func(id string) []byte {
		return append(alArticleRaw(id, alPath, g), []byte(big)...)
	}, code, "Rejected (article too large)")
	add("path loop", "loop", func(id string) []byte {
		return alArticleRaw(id, "us.ps.invalid!"+alPath, g)
	}, code, "Rejected (path loop)")
	add("invalid article", "inval", func(id string) []byte {
		return alArticleRaw(id, alPath, "..foo..")
	}, code, "Rejected (invalid article)")
	mismatch := "Rejected (Message-ID mismatch)"
	if takethis {
		mismatch = "Transfer failed (Message-ID mismatch)"
	}
	add("message-id mismatch", "mism", func(string) []byte {
		return alArticleRaw(alID("other"), alPath, g)
	}, code, mismatch)
	add("control message", "control", func(id string) []byte {
		return alArticleRaw(id, alPath, g, "Control: cancel <x@test.invalid>")
	}, code, "Rejected ("+alControlReason+")")
	add("duplicate at store time", "storedup", plain, code, "Rejected (duplicate)")
	if takethis {
		// TAKETHIS has the article on the wire already: these are 439 as well.
		add("duplicate in history", "dupcheck", plain, 439, "Not wanted")
		add("in progress in history", "inprog", plain, 439, "In progress")
		add("in progress at store time", "procretry", plain, 439, "In progress")
	} else {
		// IHAVE defers both (436): no reject, no line.
		add("intake temporary", "temp", plain, 436, "")
		add("in progress at store time", "procretry", plain, 436, "")
	}
	return cases
}

// alServer is a psServer whose peer may send at most 1 KiB (the "too large" case).
func alServer(t *testing.T) (*hServer, *psCollector, *models.Peer) {
	t.Helper()
	peer := psRow("127.0.0.1", true)
	peer.InMaxSize = 1024
	hs, col := psServer(t, alProcessor(), psRegistry(peer))
	return hs, col, peer
}

// alWantArticles checks the Article calls made since before: one with wantCode and wantText
// for a reject, none otherwise.
func alWantArticles(t *testing.T, col *psCollector, before int, peer *models.Peer, tc alCase, gotCode int, line string) {
	t.Helper()
	if gotCode != tc.wantCode {
		t.Errorf("%s: reply %d %q, want %d", tc.name, gotCode, line, tc.wantCode)
	}
	arts := col.articles()[before:]
	if tc.wantText == "" {
		if len(arts) != 0 {
			t.Errorf("%s: Article calls %+v, want none", tc.name, arts)
		}
		return
	}
	want := psArticleEvent{Peer: psRef(peer), MessageID: tc.cmdID, Code: tc.wantCode, Text: tc.wantText}
	if len(arts) != 1 || arts[0] != want {
		t.Errorf("%s: Article calls %+v, want exactly %+v", tc.name, arts, want)
	}
	if !strings.HasSuffix(line, tc.wantText) {
		t.Errorf("%s: reply %q does not end in the logged text %q", tc.name, line, tc.wantText)
	}
}

// TestArtLogIHave: the IHAVE rejects after 335 log once each with the 437 text, accepted and
// deferred articles not at all.
func TestArtLogIHave(t *testing.T) {
	hs, col, peer := alServer(t)
	c := hs.dial(t)
	for _, tc := range alCases(437) {
		before := len(col.articles())
		code, line, sent := c.Ihave(tc.cmdID, tc.article)
		if !sent {
			t.Fatalf("%s: IHAVE refused before the transfer: %q", tc.name, line)
		}
		alWantArticles(t, col, before, peer, tc, code, line)
	}
	// Refusals before the transfer are no rejects: 435 and 436 log nothing.
	before := len(col.articles())
	for _, marker := range []string{"dupcheck", "inprog", "histerr"} {
		id := alID(marker)
		if code, line := c.Cmd("IHAVE %s", id); code != 435 && code != 436 {
			t.Errorf("IHAVE %s: %d %q, want 435 or 436", id, code, line)
		}
	}
	if arts := col.articles()[before:]; len(arts) != 0 {
		t.Errorf("Article calls for 435/436 before the transfer: %+v", arts)
	}
}

// TestArtLogTakeThis: every 439 of TAKETHIS logs once with its text and without the id the
// reply carries in front; 239 logs nothing, nor do CHECK's 438 and TAKETHIS's 400.
func TestArtLogTakeThis(t *testing.T) {
	hs, col, peer := alServer(t)
	c := hs.dial(t)
	for _, tc := range alCases(439) {
		before := len(col.articles())
		code, line, _ := c.Takethis(tc.cmdID, tc.article)
		if !strings.HasPrefix(line, fmt.Sprintf("%d %s ", code, tc.cmdID)) {
			t.Errorf("%s: reply %q does not carry the id", tc.name, line)
		}
		alWantArticles(t, col, before, peer, tc, code, line)
	}

	// A malformed message-id: logged with the id as received (the collector prints "(null)").
	before := len(col.articles())
	if err := c.Send("TAKETHIS not-an-id\r\n" + string(client.StuffBytes(alArticleRaw(alID("x"), alPath, "al.grp")))); err != nil {
		t.Fatalf("send: %v", err)
	}
	code, line, err := c.ReadStatus()
	if err != nil || code != 439 {
		t.Fatalf("TAKETHIS not-an-id: %d %q %v, want 439", code, line, err)
	}
	want := psArticleEvent{Peer: psRef(peer), MessageID: "not-an-id", Code: 439, Text: "Rejected (invalid message-id)"}
	if arts := col.articles()[before:]; len(arts) != 1 || arts[0] != want {
		t.Errorf("Article calls %+v, want exactly %+v", arts, want)
	}
	// An invalid id that equals the first word of the reply text: the text keeps that word
	// (only a valid id is in front of a reply, round-0 F2).
	before = len(col.articles())
	if err := c.Send("TAKETHIS Rejected\r\n" + string(client.StuffBytes(alArticleRaw(alID("x"), alPath, "al.grp")))); err != nil {
		t.Fatalf("send: %v", err)
	}
	code, line, err = c.ReadStatus()
	if err != nil || code != 439 {
		t.Fatalf("TAKETHIS Rejected: %d %q %v, want 439", code, line, err)
	}
	want = psArticleEvent{Peer: psRef(peer), MessageID: "Rejected", Code: 439, Text: "Rejected (invalid message-id)"}
	if arts := col.articles()[before:]; len(arts) != 1 || arts[0] != want {
		t.Errorf("TAKETHIS Rejected: Article calls %+v, want exactly %+v", arts, want)
	}

	// CHECK refusals (438) are no transfers and log nothing.
	before = len(col.articles())
	if code, line := c.Cmd("CHECK %s", alID("dupcheck")); code != 438 {
		t.Errorf("CHECK: %d %q, want 438", code, line)
	}
	if code, line := c.Cmd("CHECK not-an-id"); code != 438 {
		t.Errorf("CHECK not-an-id: %d %q, want 438", code, line)
	}
	// TAKETHIS on a history error is 400 and a close: a deferral, not a reject.
	id := alID("histerr")
	code, line, _ = c.Takethis(id, alArticleRaw(id, alPath, "al.grp"))
	if code != 400 {
		t.Errorf("TAKETHIS on a history error: %d %q, want 400", code, line)
	}
	if arts := col.articles()[before:]; len(arts) != 0 {
		t.Errorf("Article calls for 438/400: %+v", arts)
	}
}

// TestArtLogFile: the end-to-end path with a real collector: one good article and one control
// message by TAKETHIS give one "-" line for the control message in innd's news log format,
// none for the good one, and the same collector still counts both as before (its store holds
// two offers, one accepted and one rejected article with their octets).
func TestArtLogFile(t *testing.T) {
	peer := psRow("127.0.0.1", true)
	hs := newTestServer(t, hServerOpts{Proc: alProcessor(), Peers: psRegistry(peer), Hostname: "us.ps.invalid"})
	dir := t.TempDir()
	path := filepath.Join(dir, "news")
	store, err := peerstats.Open(filepath.Join(dir, "cfg", peerstats.DefaultFile))
	if err != nil {
		t.Fatalf("peerstats.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})
	svc, err := peerstats.New(peerstats.Config{Store: store, ArtLogPath: path})
	if err != nil {
		t.Fatalf("peerstats.New: %v", err)
	}
	svc.Start()
	t.Cleanup(svc.Stop)
	hs.S.SetPeerStats(svc)

	c := hs.dial(t)
	g := hGroupName("al.file")
	good := alID("ok")
	if code, line, _ := c.Takethis(good, alArticleRaw(good, alPath, g)); code != 239 {
		t.Fatalf("TAKETHIS good: %d %q, want 239", code, line)
	}
	ctl := alID("control")
	if code, line, _ := c.Takethis(ctl, alArticleRaw(ctl, alPath, g, "Control: cancel <x@test.invalid>")); code != 439 {
		t.Fatalf("TAKETHIS control: %d %q, want 439", code, line)
	}
	svc.Stop() // writes what is queued and closes the file

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	re := regexp.MustCompile(`^[A-Z][a-z]{2} [ 123][0-9] [0-2][0-9]:[0-5][0-9]:[0-6][0-9]\.[0-9]{3} - ` +
		regexp.QuoteMeta(peer.Name+" "+ctl+" 439 Rejected ("+alControlReason+")") + `$`)
	if len(lines) != 1 || !re.MatchString(lines[0]) {
		t.Errorf("article log = %q, want one line matching %s", lines, re)
	}
	if strings.Contains(string(data), good) {
		t.Errorf("the accepted article %s is in the article log", good)
	}

	// Stop flushed the counters into the store.
	buckets, err := store.ReadRange(0, 1<<62)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	var got peerstats.Counters
	for _, b := range buckets {
		if b.Peer.ID == peer.ID && b.Dir == peerstats.In {
			got.Add(b.Counters)
		}
	}
	if got.Offered != 2 || got.Accepted != 1 || got.Rejected != 1 || got.Duplicate != 0 ||
		got.AccBytes <= 0 || got.RejBytes <= 0 {
		t.Errorf("counters = %+v, want offered 2, accepted 1, rejected 1, both with their octets", got)
	}
}
