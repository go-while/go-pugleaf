package processor

// Intake policy tests of plan e2e-gap-tests, slice intake-policy: the article-layer rules the
// real intake path applies (the fake processor of the internal/nntp harness never runs
// validateIntakeHeaders, so these cases live here).
//
//	G9  framing rejects: no blank line after the headers, a blank first line, an empty
//	    article, a NUL octet in the body (sources: INN tests/innd/artparse-t.c, lib/wire-t.c)
//	D1  per-peer header_check: relay (innd) accepts a header line over 998 octets and a bare
//	    CR inside a header line and stores it verbatim; strict (nnrpd) refuses both
//	D2  a NUL octet in the body refuses the article on every path
//	D4  relay needs SP or HTAB after the colon and accepts an empty value, strict needs
//	    exactly ": " and a non-empty value; POST is always strict
//	(b) a newsgroup's max_art_size at exactly N and N+1 octets
//	(c) the group limit measures the article as it arrived (IntakeWireSize), not the stored
//	    block after the server prepended its Path identity
//	Date limits: the admin settings PeerArticleMaxAgeHours / PeerArticleMaxFutureHours
//	    (INN artcutoff / DATE_FUZZ), 0 = off, applied to peer intake in both modes
//
// Waits are hang guards only (testWait, PUGLEAF_TEST_DEADLINE): no wall-clock assertion.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// the local group of these tests (a row in the scratch main DB)
const ipolGroup = "ipol.test.group"

// ipolSetup opens a scratch database with ipolGroup (max_art_size bytes, 0 = no group limit)
// and returns a processor on it. History stays nil: these tests are about the article layer,
// and history.Exists is nil-safe.
func ipolSetup(t *testing.T, maxArtSize int) *Processor {
	t.Helper()
	db := flNntpdbOpenDB(t)
	g := &models.Newsgroup{Name: ipolGroup, Description: "intake policy tests", Active: true,
		Status: "y", LowWater: 1, MaxArtSize: maxArtSize}
	if err := db.InsertNewsgroup(g); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", ipolGroup, err)
	}
	// ProcessArticle uses the global history.MsgIdCache: a fresh one per test
	return &Processor{DB: db, MsgIdCache: history.NewMsgIdItemCache()}
}

// ipolMsgID returns a message-id unique to this run (the msgid cache is global).
func ipolMsgID(tag string) string {
	return fmt.Sprintf("<ipol-%s-%d@test.invalid>", tag, time.Now().UnixNano())
}

// ipolHead returns the mandatory header lines of a valid article for ipolGroup, as a peer
// transferred them (the Date is current, so the peer age cutoff accepts it), plus extra.
func ipolHead(msgid string, extra ...string) []string {
	return append([]string{
		"Path: peer.test.invalid!not-for-mail",
		"From: Tester <tester@test.invalid>",
		"Newsgroups: " + ipolGroup,
		"Subject: intake policy",
		"Date: " + common.FormatNewsDate(time.Now()),
		"Message-ID: " + msgid,
	}, extra...)
}

// ipolBuild is what the NNTP server hands to the processor: the article of head/body as
// readArticleBlock produced them (dot-stuffing undone), with NewsgroupsPtr filled like
// buildIncomingArticle and the intake policy of the connection (setIntakePolicy):
// headerCheck is the sending peer's header_check ("" = the default relay) and the carried
// size is the article as it arrived. size 0 means common.WireArticleSize(head, body).
// An error means the lines do not parse at all: the server answers 441/437/439 itself and
// the processor never sees the article.
func ipolBuild(proc *Processor, msgid string, head, body []string, headerCheck string, size int) (*models.Article, error) {
	lines := make([]string, 0, len(head)+1+len(body))
	lines = append(lines, head...)
	lines = append(lines, "")
	lines = append(lines, body...)
	art, err := nntp.ParseLegacyArticleLines(msgid, lines, false)
	if err != nil {
		return nil, err
	}
	art.NNTPhead = head
	art.NNTPbody = body
	art.IntakeHeaderCheck = headerCheck
	if size == 0 {
		size = common.WireArticleSize(head, body)
	}
	art.IntakeWireSize = size
	name := ipolGroup
	ptr := proc.DB.Batch.GetNewsgroupPointer(name)
	art.NewsgroupsPtr = []*string{ptr}
	art.ArticleNums = map[*string]int64{ptr: -1}
	return art, nil
}

// ipolFeed runs one peer feed (IHAVE/TAKETHIS) through the real intake path.
func ipolFeed(t *testing.T, proc *Processor, msgid string, head, body []string, headerCheck string, size int) (int, error) {
	t.Helper()
	art, err := ipolBuild(proc, msgid, head, body, headerCheck, size)
	if err != nil {
		// the NNTP layer refuses such an article before the processor (437/439)
		return history.CaseError, fmt.Errorf("%w: invalid article: %v", common.ErrIntakeRejected, err)
	}
	return proc.ProcessIncomingArticle(art)
}

// ipolPost runs one POST through the real intake path (Posted, so the processor applies the
// POST policy and the strict header rules whatever the connection says).
func ipolPost(t *testing.T, proc *Processor, msgid string, head, body []string, headerCheck string) (int, error) {
	t.Helper()
	art, err := ipolBuild(proc, msgid, head, body, headerCheck, 0)
	if err != nil {
		return history.CaseError, fmt.Errorf("%w: invalid article: %v", common.ErrIntakeRejected, err)
	}
	art.Posted = true
	return proc.ProcessPostedArticle(art, nil, 0)
}

// ipolWantRejected fails unless code/err is a permanent refusal; reason "" accepts any.
func ipolWantRejected(t *testing.T, what string, code int, err error, reason string) {
	t.Helper()
	if code != history.CaseError || !errors.Is(err, common.ErrIntakeRejected) {
		t.Errorf("%s: intake = %d, %v; want CaseError wrapping ErrIntakeRejected", what, code, err)
		return
	}
	if reason != "" && !strings.HasSuffix(err.Error(), ": "+reason) {
		t.Errorf("%s: intake error %v, want the reason %q", what, err, reason)
	}
}

// ipolWantAccepted fails unless the article was queued.
func ipolWantAccepted(t *testing.T, what string, code int, err error) {
	t.Helper()
	if code != history.CasePass || err != nil {
		t.Errorf("%s: intake = %d, %v; want CasePass, nil", what, code, err)
	}
}

// ipolStoredHeaders waits (hang guard testWait) until msgid is in ipolGroup's database and
// returns its stored header block.
func ipolStoredHeaders(t *testing.T, proc *Processor, msgid string) string {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for {
		groupDB, err := proc.DB.GetGroupDB(ipolGroup)
		if err != nil {
			t.Fatalf("GetGroupDB(%s): %v", ipolGroup, err)
		}
		art, err := proc.DB.GetArticleByMessageID(groupDB, msgid)
		groupDB.Return()
		if err == nil && art != nil {
			return art.HeadersJSON
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not stored within %v (last error %v)", msgid, testWait, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// G9: framing rejects

// TestIpolFramingRejects: the article shapes a defect wire article leaves behind are refused
// in both header modes and nothing is stored. Sources: INN tests/innd/artparse-t.c,
// tests/lib/wire-t.c (an article without an empty line after the headers, a leading empty
// line, an empty article) and decision D2 (a NUL octet in the body).
func TestIpolFramingRejects(t *testing.T) {
	for _, mode := range []string{database.PeerHeaderCheckRelay, database.PeerHeaderCheckStrict} {
		t.Run(mode, func(t *testing.T) {
			proc := ipolSetup(t, 0)
			for _, tc := range []struct {
				name   string
				head   func(msgid string) []string
				body   []string
				reason string
			}{
				// no empty line after the headers: readArticleBlock puts the body lines
				// into the header block, where they are not header lines
				{name: "no-blank-line", head: func(id string) []string {
					return ipolHead(id, "this is the body, not a header")
				}, reason: "invalid header block"},
				// a blank first line: the whole article is body, there are no headers
				{name: "blank-first-line", head: func(id string) []string { return nil },
					body: []string{"Subject: looks like a header", "", "body"}, reason: "missing mandatory header"},
				{name: "empty-article", head: func(id string) []string { return nil }, reason: "missing mandatory header"},
				// D2: a NUL octet in the body
				{name: "nul-in-body", head: func(id string) []string { return ipolHead(id) },
					body: []string{"before\x00after"}, reason: "article contains NUL"},
				{name: "nul-in-last-body-line", head: func(id string) []string { return ipolHead(id) },
					body: []string{"ok", "also ok", "\x00"}, reason: "article contains NUL"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					msgid := ipolMsgID(tc.name)
					code, err := ipolFeed(t, proc, msgid, tc.head(msgid), tc.body, mode, 0)
					// a shape the parser already refuses is a reject as well, but then
					// without a fixed reason
					if errors.Is(err, common.ErrIntakeRejected) && strings.Contains(err.Error(), "invalid article:") {
						ipolWantRejected(t, tc.name, code, err, "")
					} else {
						ipolWantRejected(t, tc.name, code, err, tc.reason)
					}
					if ipolIsStored(t, proc, msgid) {
						t.Errorf("%s: the refused article is in the group database", tc.name)
					}
				})
			}
		})
	}
}

// ipolIsStored reports whether msgid is in ipolGroup's database (no wait: used after a
// refusal, where nothing may ever be stored).
func ipolIsStored(t *testing.T, proc *Processor, msgid string) bool {
	t.Helper()
	groupDB, err := proc.DB.GetGroupDB(ipolGroup)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", ipolGroup, err)
	}
	defer groupDB.Return()
	stored, err := groupDB.LookupMsgIdInArticlesDB(msgid)
	if err != nil {
		t.Fatalf("LookupMsgIdInArticlesDB(%s): %v", msgid, err)
	}
	return stored
}

// ---------------------------------------------------------------------------
// D1 / D4: relay vs strict, per peer

// TestIpolRelayVsStrict: one defect header block per case, offered by a peer whose
// header_check is relay and by one whose header_check is strict. relay mirrors innd (no line
// length limit, a bare CR or LF kept verbatim, the other C0 controls and 8-bit octets
// accepted, an empty field value accepted), strict mirrors nnrpd (all of that rejects). What
// both refuse: NUL, a colon without SP/HTAB, white space in the field name, a missing or
// duplicate core header, a bad Message-ID and an unparseable Date.
func TestIpolRelayVsStrict(t *testing.T) {
	long := "X-Long: " + strings.Repeat("a", 1000)
	for _, tc := range []struct {
		name  string
		extra []string
		// relayReason/strictReason: "" = the article is accepted in that mode
		relayReason, strictReason string
	}{
		{name: "line-over-998", extra: []string{long}, strictReason: "invalid header block"},
		{name: "bare-cr", extra: []string{"X-Cr: before\rafter"}, strictReason: "invalid header block"},
		{name: "bare-lf", extra: []string{"X-Lf: before\nafter"}, strictReason: "invalid header block"},
		{name: "colon-htab", extra: []string{"X-Tab:\tvalue"}, strictReason: "invalid header block"},
		{name: "empty-value", extra: []string{"X-Empty: "}, strictReason: "invalid header block"},
		{name: "esc-control", extra: []string{"X-Esc: \x1bvalue"}, strictReason: "invalid header block"},
		{name: "8bit", extra: []string{"X-8bit: \xc3\xa4\xff"}},
		// refused in both modes
		{name: "no-colon-space", extra: []string{"X-Bad:value"},
			relayReason: "invalid header block", strictReason: "invalid header block"},
		{name: "colon-at-eol", extra: []string{"X-Bad:"},
			relayReason: "invalid header block", strictReason: "invalid header block"},
		{name: "nul-in-header", extra: []string{"X-Bad: a\x00b"},
			relayReason: "invalid header block", strictReason: "invalid header block"},
		{name: "space-in-name", extra: []string{"X Bad: v"},
			relayReason: "invalid header block", strictReason: "invalid header block"},
		{name: "space-before-colon", extra: []string{"X-Bad : v"},
			relayReason: "invalid header block", strictReason: "invalid header block"},
		{name: "duplicate-subject", extra: []string{"Subject: again"},
			relayReason: "invalid header block", strictReason: "invalid header block"},
		{name: "leading-continuation", extra: nil,
			relayReason: "invalid header block", strictReason: "invalid header block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := ipolSetup(t, 0)
			for _, mode := range []struct {
				name, headerCheck, reason string
			}{
				{"relay", database.PeerHeaderCheckRelay, tc.relayReason},
				{"strict", database.PeerHeaderCheckStrict, tc.strictReason},
			} {
				msgid := ipolMsgID(tc.name + "-" + mode.name)
				head := ipolHead(msgid, tc.extra...)
				if tc.name == "leading-continuation" {
					head = append([]string{" leading fold"}, head...)
				}
				code, err := ipolFeed(t, proc, msgid, head, []string{"body"}, mode.headerCheck, 0)
				what := tc.name + "/" + mode.name
				if mode.reason != "" {
					ipolWantRejected(t, what, code, err, mode.reason)
					continue
				}
				ipolWantAccepted(t, what, code, err)
				// an accepted block is stored exactly as it arrived
				if len(tc.extra) == 1 {
					if stored := ipolStoredHeaders(t, proc, msgid); !strings.Contains(stored, tc.extra[0]) {
						t.Errorf("%s: the stored block does not hold %q verbatim: %q", what, tc.extra[0], stored)
					}
				}
			}
		})
	}
}

// TestIpolRelayDefectsLogged: an accepted relay article with a bare CR and a bare LF is
// logged once per defect kind, like innd ("Article accepted but includes CR without LF").
func TestIpolRelayDefectsLogged(t *testing.T) {
	proc := ipolSetup(t, 0)
	logs := postCommitCaptureLog(t)
	msgid := ipolMsgID("defect-log")
	head := ipolHead(msgid, "X-Cr: a\rb\rc", "X-Lf: a\nb")
	code, err := ipolFeed(t, proc, msgid, head, []string{"body"}, database.PeerHeaderCheckRelay, 0)
	ipolWantAccepted(t, "relay with CR and LF", code, err)
	out := logs.String()
	if n := strings.Count(out, "accepted but includes CR without LF(2)"); n != 1 {
		t.Errorf("%d CR log lines, want exactly 1 (per article): %q", n, out)
	}
	if n := strings.Count(out, "accepted but includes LF without CR(1)"); n != 1 {
		t.Errorf("%d LF log lines, want exactly 1 (per article): %q", n, out)
	}
}

// TestIpolPostAlwaysStrict: POST is checked strictly whatever the connection's peer says, so
// the header blocks a relay peer may feed are refused when they are posted.
func TestIpolPostAlwaysStrict(t *testing.T) {
	proc := ipolSetup(t, 0)
	for _, extra := range []string{
		"X-Long: " + strings.Repeat("a", 1000),
		"X-Cr: before\rafter",
		"X-Tab:\tvalue",
		"X-Empty: ",
	} {
		msgid := ipolMsgID("post-strict")
		code, err := ipolPost(t, proc, msgid, ipolHead(msgid, extra), []string{"body"}, database.PeerHeaderCheckRelay)
		ipolWantRejected(t, "POST with "+extra[:5], code, err, "invalid header block")
	}
	// the same article without the defect is accepted
	msgid := ipolMsgID("post-ok")
	code, err := ipolPost(t, proc, msgid, ipolHead(msgid, "X-Ok: value"), []string{"body"}, database.PeerHeaderCheckRelay)
	ipolWantAccepted(t, "POST of a clean article", code, err)
}

// TestIpolBothModesCoreRules: the rules neither mode relaxes (INN, confirmed by inn-1):
// every required field present, a valid Message-ID of at most 250 octets, a Date that parses.
func TestIpolBothModesCoreRules(t *testing.T) {
	// ipolPadID keeps the unique part of def and pads the id to n octets, so every mode of a
	// case offers its own id (a remembered reject would otherwise answer the second offer).
	ipolPadID := func(def string, n int) string {
		core := strings.TrimSuffix(strings.TrimPrefix(def, "<"), ">")
		return "<" + strings.Repeat("a", n-2-len(core)) + core + ">"
	}
	for _, tc := range []struct {
		name   string
		head   func(msgid string) []string
		msgid  func(def string) string
		reason string
	}{
		{name: "no-subject", head: func(id string) []string {
			var out []string
			for _, l := range ipolHead(id) {
				if !strings.HasPrefix(l, "Subject:") {
					out = append(out, l)
				}
			}
			return out
		}, reason: "missing mandatory header"},
		{name: "no-path", head: func(id string) []string { return ipolHead(id)[1:] },
			reason: "missing mandatory header"},
		// 251 octets: one over the RFC 5536 §3.1.3 limit
		{name: "id-too-long", head: func(id string) []string { return ipolHead(id) },
			msgid: func(def string) string { return ipolPadID(def, 251) }, reason: "invalid message-id"},
		{name: "id-unparseable", head: func(id string) []string { return ipolHead(id) },
			msgid: func(def string) string { return strings.Trim(def, "<>") }, reason: "invalid message-id"},
		{name: "date-garbage", head: func(id string) []string {
			var out []string
			for _, l := range ipolHead(id) {
				if strings.HasPrefix(l, "Date:") {
					l = "Date: not a date at all"
				}
				out = append(out, l)
			}
			return out
		}, reason: "invalid date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := ipolSetup(t, 0)
			for _, mode := range []string{database.PeerHeaderCheckRelay, database.PeerHeaderCheckStrict} {
				msgid := ipolMsgID(tc.name + "-" + mode)
				if tc.msgid != nil {
					msgid = tc.msgid(msgid)
				}
				code, err := ipolFeed(t, proc, msgid, tc.head(msgid), []string{"body"}, mode, 0)
				ipolWantRejected(t, tc.name+"/"+mode, code, err, tc.reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (b) and (c): the group size limit

// TestIpolGroupMaxArtSize: a newsgroup's max_art_size is an inclusive limit on the article as
// it arrived, so N octets are accepted and N+1 are not. The limit measures the carried size
// (models.Article.IntakeWireSize, taken before the server prepended its Path identity), not
// the larger block the processor stores.
func TestIpolGroupMaxArtSize(t *testing.T) {
	const limit = 800
	for _, tc := range []struct {
		name string
		size int
		want bool // accepted
	}{
		{"exactly-the-limit", limit, true},
		{"one-octet-over", limit + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := ipolSetup(t, limit)
			msgid := ipolMsgID(tc.name)
			head := ipolHead(msgid)
			// pad the body so the article as it arrived is exactly tc.size octets
			fixed := common.WireArticleSize(head, nil) + 2
			if tc.size < fixed {
				t.Fatalf("size %d is below the %d octets of the header block", tc.size, fixed)
			}
			body := []string{strings.Repeat("x", tc.size-fixed)}
			if got := common.WireArticleSize(head, body); got != tc.size {
				t.Fatalf("built %d octets, want %d", got, tc.size)
			}
			code, err := ipolFeed(t, proc, msgid, head, body, database.PeerHeaderCheckRelay, 0)
			if tc.want {
				ipolWantAccepted(t, tc.name, code, err)
			} else {
				ipolWantRejected(t, tc.name, code, err, "no valid newsgroup")
			}
		})
	}

	// (c): the stored block is larger than what arrived (the server prepends its Path
	// identity before the processor sees the article), and that must not count against the
	// group limit: an article of exactly the limit stays accepted.
	t.Run("path-growth-not-counted", func(t *testing.T) {
		proc := ipolSetup(t, limit)
		msgid := ipolMsgID("pathgrowth")
		arrived := ipolHead(msgid)
		fixed := common.WireArticleSize(arrived, nil) + 2
		body := []string{strings.Repeat("x", limit-fixed)}
		size := common.WireArticleSize(arrived, body)
		if size != limit {
			t.Fatalf("built %d octets, want the limit %d", size, limit)
		}
		// what feedPath hands over: the same article with our identity in the Path
		stored := common.ReplaceHeader(arrived, "Path", "news.local.invalid!peer.test.invalid!not-for-mail")
		if common.WireArticleSize(stored, body) <= limit {
			t.Fatalf("the test needs a Path line that grew: %d octets", common.WireArticleSize(stored, body))
		}
		code, err := ipolFeed(t, proc, msgid, stored, body, database.PeerHeaderCheckRelay, size)
		ipolWantAccepted(t, "an article of exactly max_art_size whose Path grew", code, err)
	})
}

// ---------------------------------------------------------------------------
// The peer Date limits (admin settings, INN artcutoff / DATE_FUZZ)

// TestIpolPeerDateLimits: peer intake applies the two admin settings in both header modes,
// 0 turns a limit off, and a POST has no past cutoff in the processor (the NNTP POST handler
// applies nntp.PostDateMaxAge to the proto-article instead).
func TestIpolPeerDateLimits(t *testing.T) {
	for _, tc := range []struct {
		name        string
		age, future string // the stored CFG values (hours)
		offset      time.Duration
		reason      string // "" = accepted
	}{
		{name: "old-beyond-default", offset: -100 * time.Hour, reason: "date too old"},
		{name: "old-inside-default", offset: -70 * time.Hour},
		{name: "old-cutoff-off", age: "0", offset: -100 * time.Hour},
		{name: "old-cutoff-raised", age: "240", offset: -100 * time.Hour},
		{name: "future-beyond-default", offset: 30 * time.Hour, reason: "date in the future"},
		{name: "future-limit-off", future: "0", offset: 30 * time.Hour},
		{name: "future-limit-lowered", future: "1", offset: 2 * time.Hour, reason: "date in the future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := ipolSetup(t, 0)
			if tc.age != "" {
				if err := proc.DB.SetConfigValue(config.CFG_KEY_PEERDATEMAXAGE, tc.age); err != nil {
					t.Fatalf("SetConfigValue(%s): %v", config.CFG_KEY_PEERDATEMAXAGE, err)
				}
			}
			if tc.future != "" {
				if err := proc.DB.SetConfigValue(config.CFG_KEY_PEERDATEMAXFUTURE, tc.future); err != nil {
					t.Fatalf("SetConfigValue(%s): %v", config.CFG_KEY_PEERDATEMAXFUTURE, err)
				}
			}
			for _, mode := range []string{database.PeerHeaderCheckRelay, database.PeerHeaderCheckStrict} {
				msgid := ipolMsgID(tc.name + "-" + mode)
				head := make([]string, 0, 6)
				for _, l := range ipolHead(msgid) {
					if strings.HasPrefix(l, "Date:") {
						l = "Date: " + common.FormatNewsDate(time.Now().Add(tc.offset))
					}
					head = append(head, l)
				}
				code, err := ipolFeed(t, proc, msgid, head, []string{"body"}, mode, 0)
				if tc.reason == "" {
					ipolWantAccepted(t, tc.name+"/"+mode, code, err)
				} else {
					ipolWantRejected(t, tc.name+"/"+mode, code, err, tc.reason)
				}
			}
		})
	}

	// a POST of an article older than the peer cutoff is not refused here
	t.Run("post-has-no-past-cutoff", func(t *testing.T) {
		proc := ipolSetup(t, 0)
		msgid := ipolMsgID("post-old")
		var head []string
		for _, l := range ipolHead(msgid) {
			if strings.HasPrefix(l, "Date:") {
				l = "Date: " + common.FormatNewsDate(time.Now().Add(-200*time.Hour))
			}
			head = append(head, l)
		}
		code, err := ipolPost(t, proc, msgid, head, []string{"body"}, "")
		ipolWantAccepted(t, "POST with a 200 h old Date", code, err)
	})
}

// TestIpolPeerDateDefaults: the defaults of the two settings are the seeded 72 h / 24 h.
func TestIpolPeerDateDefaults(t *testing.T) {
	proc := ipolSetup(t, 0)
	if got := proc.DB.PeerDateMaxAge(); got != 72*time.Hour {
		t.Errorf("PeerDateMaxAge() = %v, want 72h", got)
	}
	if got := proc.DB.PeerDateMaxFuture(); got != 24*time.Hour {
		t.Errorf("PeerDateMaxFuture() = %v, want 24h", got)
	}
	maxAge, maxFuture := intakeDateLimits(proc.DB, false)
	if maxAge != 72*time.Hour || maxFuture != 24*time.Hour {
		t.Errorf("intakeDateLimits(peer) = %v, %v; want 72h, 24h", maxAge, maxFuture)
	}
	// POST: no past cutoff in the processor, the RFC 5537 future margin
	if maxAge, maxFuture = intakeDateLimits(proc.DB, true); maxAge != 0 || maxFuture != MaxFutureDate {
		t.Errorf("intakeDateLimits(posted) = %v, %v; want 0, %v", maxAge, maxFuture, MaxFutureDate)
	}
}

// TestIpolHeaderModeSelection: the mode per path (POST strict, a peer feed per its
// header_check, an unset header_check = relay).
func TestIpolHeaderModeSelection(t *testing.T) {
	for _, tc := range []struct {
		posted      bool
		headerCheck string
		want        common.HeaderBlockMode
	}{
		{false, "", common.HeaderBlockRelay},
		{false, database.PeerHeaderCheckRelay, common.HeaderBlockRelay},
		{false, database.PeerHeaderCheckStrict, common.HeaderBlockStrict},
		{true, database.PeerHeaderCheckRelay, common.HeaderBlockStrict},
		{true, database.PeerHeaderCheckStrict, common.HeaderBlockStrict},
	} {
		art := &models.Article{Posted: tc.posted, IntakeHeaderCheck: tc.headerCheck}
		if got := intakeHeaderMode(art); got != tc.want {
			t.Errorf("intakeHeaderMode(posted=%v, header_check=%q) = %v, want %v",
				tc.posted, tc.headerCheck, got, tc.want)
		}
	}
}
