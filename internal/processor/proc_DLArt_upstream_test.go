package processor

// Upstream-fault tests of plan e2e-gap-tests, slice fetcher-faults: the fetcher half of the
// gap tests G14 (the upstream drops the XHDR stream or an ARTICLE reply in the middle), G15
// (a provider that caps the records per XHDR reply), G16 (low water 0 and an emptied group),
// G17 (MODE READER refused) and G19 (NUL / bare CR in a fetched header block). Sources: NewsCache
// tests/e2e/upstream_test.go TestUpstreamCutOffArticle/CutOffActive, TestUpstreamOverRangeCap,
// TestUpstreamLowZero, TestUpstreamEmptiedGroup, TestUpstreamConnectRefusals,
// TestUpstreamArticleNULAndCR. The client half is
// internal/nntp/nntp-client-upstream-faults_test.go.
//
// What matters here is what the run leaves behind: the stored articles and the fetch
// progress. The progress may never pass an article number the run did not see.
//
// The provider is upFakeStart below: a kit fakeserver on 127.0.0.1:0 over a kit Store, like
// proc_DLArt_fake_srv_test.go (the kit's Faults do not apply to an external handler, so the
// quirks are hooks of upFake). The database is a scratch database under t.TempDir() and the
// download workers are dlFakeEnv's, as cmd/nntp-fetcher runs them.
//
// No wall-clock assertion: testWait (PUGLEAF_TEST_DEADLINE) is a hang guard only.

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/nntp-testkit/fakeserver"
)

// upFake is the provider behaviour of one test: the store plus the faults this slice needs
// (the kit's Faults are not available behind an external handler). Every field is set
// before the server starts and only read afterwards; the counters are atomic.
type upFake struct {
	st   *fakeserver.Store
	high int64 // generated High of the store's group, for the test's own bookkeeping

	// modeReader replaces the reply to MODE READER ("503 ...", "500 ..."): a provider
	// without a reader mode.
	modeReader string

	// groupNumbers rewrites what GROUP reports (the kit's StoreOptions.GroupNumbers):
	// low water 0, an emptied group. nil: the store's numbers.
	groupNumbers func(g fakeserver.Group) fakeserver.Group

	// xhdrCap > 0: at most this many records per XHDR reply, terminated normally. A
	// provider that caps the records of one request (NewsCache StoreOptions.MaxOverRange).
	xhdrCap int

	// xhdrDrop > 0: close the connection after this many records of an XHDR reply, without
	// the terminating dot (the kit's Faults.DropInMultiline for XHDR).
	xhdrDrop int

	// articleHeaders rewrites the header block ARTICLE serves (a bare CR, a NUL octet).
	// nil: the article's headers.
	articleHeaders func(n int64, headers []string) []string

	// articleBody rewrites the body ARTICLE serves (a NUL octet). nil: the article's body.
	articleBody func(n int64, body []string) []string

	// articleDropID: ARTICLE for this message-id writes its status line and one header line
	// and then closes the connection (the kit's Faults.DropInMultiline for one article).
	articleDropID string

	xhdrs    atomic.Int64 // XHDR commands over all connections
	articles atomic.Int64 // ARTICLE commands over all connections
}

// upFakeStart starts the fake provider with testWait as its hang guard and t.Cleanup(Close).
func upFakeStart(t *testing.T, f *upFake) *fakeserver.Server {
	t.Helper()
	if f.st == nil {
		t.Fatal("upFakeStart: no store")
	}
	return fakeserver.New(t, fakeserver.Options{Deadline: testWait, Handler: f.handle})
}

// handle serves one connection: the subset of RFC 3977 that DownloadArticles uses.
func (f *upFake) handle(c *fakeserver.Conn) {
	group := ""
	for {
		line, err := c.ReadLine()
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			_ = c.WriteLines("500 empty command")
			continue
		}
		args := fields[1:]
		switch strings.ToUpper(fields[0]) {
		case "QUIT":
			_ = c.WriteLines("205 closing connection")
			return
		case "MODE":
			if f.modeReader != "" {
				_ = c.WriteLines(f.modeReader)
				continue
			}
			_ = c.WriteLines("200 reader mode")
		case "CAPABILITIES":
			_ = c.WriteLines("101 capability list follows", "VERSION 2", "READER", ".")
		case "DATE":
			_ = c.WriteLines("111 " + time.Now().UTC().Format("20060102150405"))
		case "GROUP":
			group = f.group(c, args, group)
		case "XHDR":
			if !f.xhdr(c, group, args) {
				return // the reply was cut: the connection is gone
			}
		case "ARTICLE":
			if !f.article(c, group, args) {
				return // the reply was cut: the connection is gone
			}
		default:
			_ = c.WriteLines("500 unknown command")
		}
	}
}

// group answers GROUP and returns the newly selected group (or the old one).
func (f *upFake) group(c *fakeserver.Conn, args []string, cur string) string {
	if len(args) != 1 {
		_ = c.WriteLines("501 syntax error")
		return cur
	}
	g, ok := f.st.Group(args[0])
	if !ok {
		_ = c.WriteLines("411 no such newsgroup")
		return cur
	}
	if f.groupNumbers != nil {
		g = f.groupNumbers(g)
	}
	_ = c.WriteLines(fmt.Sprintf("211 %d %d %d %s", g.Count, g.Low, g.High, args[0]))
	return args[0]
}

// xhdr answers "XHDR message-id <from>-<to>" for the selected group, with the record cap and
// the mid-reply drop of this test. It returns false when the reply was cut (the caller must
// stop serving the connection).
func (f *upFake) xhdr(c *fakeserver.Conn, group string, args []string) bool {
	f.xhdrs.Add(1)
	if group == "" {
		_ = c.WriteLines("412 no newsgroup selected")
		return true
	}
	if len(args) != 2 || !strings.EqualFold(args[0], "message-id") {
		_ = c.WriteLines("501 syntax error")
		return true
	}
	from, to, ok := dlFakeRange(args[1])
	if !ok {
		_ = c.WriteLines("501 bad range")
		return true
	}
	lines := []string{"221 message-id fields follow"}
	for _, n := range f.st.NumbersIn(group, from, to) {
		if f.xhdrCap > 0 && len(lines) > f.xhdrCap {
			break // the provider serves only the first N records of the range
		}
		if f.xhdrDrop > 0 && len(lines) > f.xhdrDrop {
			_ = c.WriteLines(lines...) // no terminating dot: the stream is cut
			return false
		}
		a, ok := f.st.Article(group, n)
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d %s", n, a.MessageID))
	}
	_ = c.WriteLines(append(lines, ".")...)
	return true
}

// article answers "ARTICLE <message-id>". It returns false when the reply was cut (the
// caller must stop serving the connection).
func (f *upFake) article(c *fakeserver.Conn, group string, args []string) bool {
	f.articles.Add(1)
	if len(args) != 1 || !strings.HasPrefix(args[0], "<") {
		_ = c.WriteLines("501 ARTICLE by number not served")
		return true
	}
	if f.articleDropID != "" && args[0] == f.articleDropID {
		// the status line and one header line, then the connection goes away
		_ = c.WriteLines("220 0 "+args[0], "Path: fake.provider.invalid!not-for-mail")
		return false
	}
	a, _, ok := f.st.ByMessageID(args[0])
	if !ok {
		_ = c.WriteLines("430 no such article")
		return true
	}
	a = a.WithDefaults(group)
	headers := a.Headers
	if f.articleHeaders != nil {
		headers = f.articleHeaders(a.Number, append([]string(nil), headers...))
	}
	out := make([]string, 0, len(headers)+len(a.Body)+3)
	out = append(out, fmt.Sprintf("220 %d %s", a.Number, args[0]))
	for _, h := range headers {
		out = append(out, dlFakeStuff(h))
	}
	out = append(out, "")
	body := a.Body
	if f.articleBody != nil {
		body = f.articleBody(a.Number, append([]string(nil), body...))
	}
	for _, b := range body {
		out = append(out, dlFakeStuff(b))
	}
	_ = c.WriteLines(append(out, ".")...)
	return true
}

// upFakeSetup is dlFakeSetup for an upFake provider: it starts the server, opens a scratch
// database and progress DB, builds the pool and starts the download workers. The result is a
// dlFakeEnv, so the run/stored/progress helpers of proc_DLArt_fake_test.go apply.
func upFakeSetup(t *testing.T, group string, f *upFake) *dlFakeEnv {
	t.Helper()
	if f.st == nil {
		f.st = dlFakeStore(group, f.high)
	}
	srv := upFakeStart(t, f)

	db := flNntpdbOpenDB(t)
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: group, Description: "upstream fault fetcher test", Active: true, Status: "y", LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", group, err)
	}
	pdb, err := database.NewProgressDB(t.TempDir())
	if err != nil {
		t.Fatalf("NewProgressDB: %v", err)
	}
	t.Cleanup(func() { _ = pdb.Close() })

	cfg := &nntp.BackendConfig{
		Host:               srv.Host(),
		Port:               srv.Port(),
		MaxConns:           2,
		ConnectTimeout:     30 * time.Second,
		AuthFailureBackoff: -1,
		Provider:           &config.Provider{Name: dlFakeProvider, Host: srv.Host(), Port: srv.Port()},
	}
	pool := nntp.NewPool(cfg)
	t.Cleanup(func() { _ = pool.ClosePool() })

	e := &dlFakeEnv{
		t:     t,
		group: group,
		srv:   srv,
		proc:  &Processor{DB: db, Pool: pool, MsgIdCache: history.NewMsgIdItemCache()},
		pool:  pool,
		pdb:   pdb,
		dlPar: make(chan struct{}, 1),
		stop:  make(chan struct{}),
	}
	e.startWorkers(cfg.MaxConns)
	return e
}

// G14: the upstream closes the connection in the middle of the XHDR stream. The message-id
// list of the range is incomplete, so the run must fail and the progress must stay where it
// was: the next run asks for the same range again (articles already stored are skipped by
// the message-id lookup). Nothing above the cut may be stored.
func TestDLArtUpXHdrCutOff(t *testing.T) {
	const high = 200
	const cut = 50
	group := dlFakeNewGroup()
	e := upFakeSetup(t, group, &upFake{high: high, xhdrDrop: cut})

	logs := postCommitCaptureLog(t)
	err := e.run(1, high)
	if err == nil {
		t.Fatal("DownloadArticles returned nil although the XHDR stream was cut off")
	}
	if !strings.Contains(err.Error(), "XHDR") {
		t.Errorf("error %v does not name the XHDR stream", err)
	}
	e.wantProgress(0) // the range was never listed completely
	if n := strings.Count(logs.String(), "progress not updated"); n != 1 {
		t.Errorf("%d 'progress not updated' log lines, want 1\n%s", n, logs.String())
	}
	// Only articles the cut-off stream did list may be stored. How many of them are in the
	// database when the run returns is not fixed: the failing run returns before the commit
	// barrier (commitThenProgress is never reached), so the batch writer may still owe some
	// of them. Only the upper bound is pinned.
	stored := e.stored()
	for id, n := range stored {
		if n != 1 {
			t.Errorf("article %s stored %d times", id, n)
		}
	}
	if len(stored) > cut {
		t.Errorf("%d articles stored, want at most the %d listed before the cut", len(stored), cut)
	}
	for n := int64(cut + 1); n <= high; n++ {
		if id := dlFakeMsgID(group, n); stored[id] != 0 {
			t.Errorf("article %d (%s) is stored although it was never listed", n, id)
		}
	}
}

// G14: the upstream closes the connection in the middle of an ARTICLE reply. Nothing
// partial may be stored, the rest of the range is fetched on a fresh connection and the
// article is asked for once (no retry loop).
//
// FU-progress: pinned, and a finding of the slice: the progress moves past the article whose
// reply was cut, so that one article is never fetched again. It is the same trade-off as the poisoned
// article of TestDLArtFakePoisonedArticle - the commit barrier only holds the progress back
// for articles that were queued for storage - and changing it would let one unreadable
// article block a group for good.
func TestDLArtUpArticleCutOff(t *testing.T) {
	const high = 30
	const cut = 7
	group := dlFakeNewGroup()
	f := &upFake{high: high, articleDropID: dlFakeMsgID(group, cut)}
	e := upFakeSetup(t, group, f)

	if err := e.run(1, high); err != nil {
		t.Fatalf("DownloadArticles: %v", err)
	}
	got := e.stored()
	if n := got[dlFakeMsgID(group, cut)]; n != 0 {
		t.Errorf("the article whose reply was cut off is stored %d times, want 0", n)
	}
	if len(got) != high-1 {
		t.Errorf("%d articles stored, want %d (all but the cut-off one)", len(got), high-1)
	}
	for n := int64(1); n <= high; n++ {
		if n == cut {
			continue
		}
		if id := dlFakeMsgID(group, n); got[id] != 1 {
			t.Fatalf("article %d (%s) stored %d times, want 1", n, id, got[id])
		}
	}
	if n := f.articles.Load(); n != high {
		t.Errorf("%d ARTICLE commands, want %d (the cut-off article is asked for once)", n, high)
	}
	e.wantProgress(high)
}

// G15: a provider that serves at most N records per XHDR reply (and terminates the reply
// normally) must not make the fetcher skip the rest of the range. The run has to ask again
// after the last record it got; the progress may only move once the whole range was listed.
//
// The second case is the one that bounds the re-asking: a range whose tail holds no articles
// any more (expired) answers with records below the end of the range for good, so the run
// must ask once more and then move on. This provider answers the empty re-ask with "221" and
// no record; the RFC 423 shape is TestCliUpXHdrRecordCap/trailing-hole in internal/nntp.
func TestDLArtUpXHdrRecordCap(t *testing.T) {
	t.Run("capped-records", func(t *testing.T) {
		const high = 250
		const recs = 100
		group := dlFakeNewGroup()
		f := &upFake{high: high, xhdrCap: recs}
		e := upFakeSetup(t, group, f)

		if err := e.run(1, high); err != nil {
			t.Fatalf("DownloadArticles: %v", err)
		}
		e.wantStoredRange(1, high) // nothing skipped
		e.wantProgress(high)
		if n := f.xhdrs.Load(); n != 3 {
			t.Errorf("%d XHDR commands, want 3 (100 records each for 250 articles)", n)
		}
	})

	t.Run("trailing-hole", func(t *testing.T) {
		const high = 60 // what GROUP reports
		const last = 40 // the highest article that still exists
		group := dlFakeNewGroup()
		st := fakeserver.NewStore()
		st.AddGenerated(group, "upstream fault test group", fakeserver.Generator{Low: 1, High: last, Article: dlFakeArticle})
		f := &upFake{
			st: st,
			groupNumbers: func(g fakeserver.Group) fakeserver.Group {
				g.High = high // the numbers above last are gone (expired)
				return g
			},
		}
		e := upFakeSetup(t, group, f)

		if err := e.run(1, high); err != nil {
			t.Fatalf("DownloadArticles: %v", err)
		}
		e.wantStoredRange(1, last)
		e.wantProgress(high) // the whole range was listed: the hole is not fetched again
		if n := f.xhdrs.Load(); n != 2 {
			t.Errorf("%d XHDR commands, want 2 (one re-ask after the last record, then the empty tail)", n)
		}
	})
}

// G16: a provider that reports low water 0 for a group that has articles, and an emptied
// group (low = high+1, RFC 3977 §6.1.1.2). No chunk may be skipped and nothing may crash.
// The "211 0 0 0" shape is covered by TestDLArtFakeLaggingFrontend.
func TestDLArtUpLowWater(t *testing.T) {
	t.Run("low-zero", func(t *testing.T) {
		const high = 100
		group := dlFakeNewGroup()
		f := &upFake{
			high: high,
			groupNumbers: func(g fakeserver.Group) fakeserver.Group {
				g.Low = 0 // a provider that reports no low water at all
				return g
			},
		}
		e := upFakeSetup(t, group, f)
		if err := e.run(1, high); err != nil {
			t.Fatalf("DownloadArticles: %v", err)
		}
		e.wantStoredRange(1, high)
		e.wantProgress(high)
	})

	// The articles below the low water are gone: the range is listed completely (no
	// records at all), nothing is stored and the progress is not moved.
	t.Run("emptied-group", func(t *testing.T) {
		const high = 50
		group := dlFakeNewGroup()
		st := fakeserver.NewStore()
		st.AddGroup(group, "upstream fault test group")
		f := &upFake{
			st: st,
			groupNumbers: func(g fakeserver.Group) fakeserver.Group {
				g.Count, g.Low, g.High = 0, high+1, high
				return g
			},
		}
		e := upFakeSetup(t, group, f)
		if err := e.run(1, high); err != nil {
			t.Fatalf("DownloadArticles on an emptied group: %v", err)
		}
		if got := e.stored(); len(got) != 0 {
			t.Errorf("%d articles stored from an emptied group: %v", len(got), got)
		}
		// FU-progress: nothing was fetched, so no progress is recorded - and the next run
		// asks for the same range again. A range that stays empty for good (the provider
		// answers the first ask with 423, or lists it completely with no record) therefore
		// never advances: the livelock to decide on in a follow-up, pinned here.
		e.wantProgress(0)
		if n := f.articles.Load(); n != 0 {
			t.Errorf("%d ARTICLE commands for an emptied group, want 0", n)
		}
	})
}

// G17: a provider that refuses MODE READER. The run must fail with a clean error, the
// progress must stay where it was, and the fetcher must not dial again and again (one dial
// for the XHDR connection, no connection per article).
func TestDLArtUpModeReaderRefused(t *testing.T) {
	const high = 50
	for _, reply := range []string{"503 program fault - command not performed", "500 unknown command"} {
		code, _, _ := strings.Cut(reply, " ")
		t.Run(code, func(t *testing.T) {
			group := dlFakeNewGroup()
			f := &upFake{high: high, modeReader: reply}
			e := upFakeSetup(t, group, f)

			err := e.run(1, high)
			if err == nil {
				t.Fatal("DownloadArticles returned nil although MODE READER is refused")
			}
			if !strings.Contains(err.Error(), code) {
				t.Errorf("error %v does not name the %s reply", err, code)
			}
			e.wantProgress(0)
			if got := e.stored(); len(got) != 0 {
				t.Errorf("%d articles stored although no connection was usable", len(got))
			}
			if n := f.articles.Load(); n != 0 {
				t.Errorf("%d ARTICLE commands although MODE READER is refused", n)
			}
			// the refusal is not retried per article: a handful of dials, not one per
			// article of the range
			if n := e.srv.Accepted(); n > 4 {
				t.Errorf("%d connections accepted for one run, want at most 4 (no reconnect storm)", n)
			}
		})
	}
}

// storedHeaders returns the stored header blocks of the group database, joined with "\n".
func (e *dlFakeEnv) storedHeaders() string {
	e.t.Helper()
	groupDB, err := e.proc.DB.GetGroupDB(e.group)
	if err != nil {
		e.t.Fatalf("GetGroupDB(%s): %v", e.group, err)
	}
	defer groupDB.Return()
	rows, err := database.RetryableQuery(groupDB.DB, "SELECT headers_json FROM articles", nil)
	if err != nil {
		e.t.Fatalf("select headers_json: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			e.t.Fatalf("scan headers_json: %v", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatalf("rows: %v", err)
	}
	return strings.Join(out, "\n")
}

// G19 (wave 2, "Fetch = relay rules"): a fetched article whose header block holds a bare CR,
// or whose header block or body holds a NUL octet.
//
// The fetch path follows the relay rules (plan e2e-gap-tests, item (e)): a bare CR inside a
// header line is accepted and stored verbatim (counted, and logged once per article like
// innd "accepted but includes CR without LF"), a NUL in the header block or in the body
// refuses the whole article. Either way the run succeeds and the progress moves past the
// range: a refused article is not fetched again. The client keeps both octets verbatim
// (internal/nntp/nntp-client-upstream-faults_test.go TestCliUpArticleNULAndCR).
func TestDLArtUpArticleNULAndCR(t *testing.T) {
	const crSubject = "Subject: before\rafter"
	for _, tc := range []struct {
		name    string
		subject string // "" = the generated Subject
		body    string // "" = the generated body
		stored  bool   // the article is stored (accepted)
		logLine string // log text expected once per article
	}{
		{name: "bare-cr-header", subject: crSubject, stored: true, logLine: "accepted but includes CR without LF(1)"},
		{name: "nul-header", subject: "Subject: before\x00after", logLine: "(bulk) rejected"},
		{name: "nul-body", body: "body before\x00after", logLine: "(bulk) rejected: article contains NUL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const high = 10
			group := dlFakeNewGroup()
			f := &upFake{high: high}
			if tc.subject != "" {
				f.articleHeaders = func(n int64, headers []string) []string {
					for i, h := range headers {
						if strings.HasPrefix(h, "Subject:") {
							headers[i] = tc.subject
						}
					}
					return headers
				}
			}
			if tc.body != "" {
				f.articleBody = func(n int64, body []string) []string { return []string{tc.body} }
			}
			e := upFakeSetup(t, group, f)

			logs := postCommitCaptureLog(t)
			if err := e.run(1, high); err != nil {
				t.Fatalf("DownloadArticles: %v", err)
			}
			if tc.stored {
				e.wantStoredRange(1, high)
				// the bare CR is stored verbatim, nothing is normalized away
				if headers := e.storedHeaders(); !strings.Contains(headers, crSubject) {
					t.Errorf("the stored header block does not hold %q verbatim: %q", crSubject, headers)
				}
			} else if got := e.stored(); len(got) != 0 {
				t.Errorf("%d articles with a NUL octet stored: %v", len(got), got)
			}
			if n := strings.Count(logs.String(), tc.logLine); n != high {
				t.Errorf("%d %q log lines, want %d (one per article)", n, tc.logLine, high)
			}
			e.wantProgress(high)
		})
	}
}
