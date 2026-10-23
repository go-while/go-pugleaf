package processor

// Fake provider for the DownloadArticles tests of plan fetcher-hostile-peer, slice
// fetcher-download-fake.
//
// Why a handler and not the kit's Store mode: Processor.DownloadArticles drives
// GROUP + XHDR Message-ID + ARTICLE <message-id>, and the kit's StoreHandler
// (nntp-testkit v0.5.0) does not implement XHDR at all (storehandler.go answers 500 for
// it), so a Store-mode server cannot serve one fetcher run. This file therefore uses
// fakeserver.New with Options.Handler and serves the commands the fetcher sends from a
// kit *fakeserver.Store (generated groups, articles, message-id lookup). The kit's
// Faults and StoreOptions do not apply to an external handler, so the quirks this slice
// needs (GroupNumbers, the overview/XHDR message-id rewrite, an endless ARTICLE, a 480
// mid-run) are the dlFake hooks below, named after their kit counterparts.
//
// The server never contacts any host: fakeserver listens on 127.0.0.1:0.

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/nntp-testkit/fakeserver"
)

// credentials of the tests that make the provider ask for a login
const (
	dlFakeUser = "fetcher"
	dlFakePass = "s3cret"
)

// dlFakeArticle is article n of the generated group: a complete Usenet article as the
// fetcher's bulk intake needs it (Newsgroups, Subject, From, Date, Message-ID; the
// kit's GeneratedArticle has no Newsgroups header and no Date header).
func dlFakeArticle(group string, n int64) (fakeserver.Article, bool) {
	id := dlFakeMsgID(group, n)
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
	return fakeserver.Article{
		Number:    n,
		MessageID: id,
		Headers: []string{
			"Path: fake.provider.invalid!not-for-mail",
			"From: Generated Poster <poster@generated.invalid>",
			"Newsgroups: " + group,
			fmt.Sprintf("Subject: generated article %d", n),
			"Date: " + date.Format(fakeserver.DateLayout),
			"Message-ID: " + id,
		},
		Body: []string{fmt.Sprintf("body of generated article %d", n)},
		Date: date,
	}, true
}

// dlFakeMsgID is the message-id of article n (the kit's generated form, so the store's
// default Lookup resolves it).
func dlFakeMsgID(group string, n int64) string {
	return fmt.Sprintf("<%d.%s@fakeserver.invalid>", n, group)
}

// dlFakeStore returns a store whose only group is a generated group of high articles.
func dlFakeStore(group string, high int64) *fakeserver.Store {
	st := fakeserver.NewStore()
	st.AddGenerated(group, "generated group of the hostile-peer tests", fakeserver.Generator{
		Low:     1,
		High:    high,
		Article: dlFakeArticle,
	})
	return st
}

// dlFake is the provider behaviour of one test: the store plus the quirks and faults.
// Every field is set before the server starts and only read afterwards; the counters
// are atomic.
type dlFake struct {
	st   *fakeserver.Store
	high int64 // generated High, for the test's own bookkeeping

	// user/pass: when user is set every command but AUTHINFO/QUIT answers 480 until
	// the connection logged in (the kit's StoreOptions.User/Pass).
	user, pass string

	// groupNumbers rewrites what GROUP reports (the kit's StoreOptions.GroupNumbers):
	// a lagging frontend. nil: the store's numbers.
	groupNumbers func(conn int, g fakeserver.Group) fakeserver.Group

	// xhdrID rewrites the message-id XHDR reports for article n (the XHDR counterpart
	// of the kit's StoreOptions.OverviewFields). nil: the article's own message-id.
	xhdrID func(conn int, group string, n int64, id string) string

	// articleHeaders rewrites the header block ARTICLE serves for article n (an
	// article whose own Message-ID header is not the one it was asked for, a blanked
	// Subject, ...). nil: the article's headers.
	articleHeaders func(n int64, headers []string) []string

	// endlessID: ARTICLE for this message-id answers 220 and then writes lines
	// forever (until the client closes the connection, endlessMax lines or the
	// deadline). The kit's Faults.Endless keys on the command, which would poison
	// every ARTICLE.
	endlessID  string
	endlessMax int64 // 0: dlFakeEndlessMax

	// auth480From: from the N-th ARTICLE of the whole server on, ARTICLE answers 480
	// and the connection's login ends (the kit: any 480 ends the login). 0: off.
	// auth480Left is the remaining budget of such replies over all connections; a test
	// sets it to 1 for a single expired login and to 0 to disarm the fault for a re-run.
	auth480From int64
	auth480Left atomic.Int64

	// deadline bounds the endless writer; 0: dlFakeDeadline.
	deadline time.Duration

	articles atomic.Int64 // ARTICLE commands over all connections
	sent480  atomic.Int64 // 480 replies written to ARTICLE
}

// dlFakeEndlessMax bounds the endless ARTICLE writer: the client's octet budget
// (BackendConfig.MaxArticleResponseBytes) must trip long before it.
const dlFakeEndlessMax = 1 << 20

// dlFakeStart starts the fake provider with testWait as its hang guard and
// t.Cleanup(Close).
func dlFakeStart(t *testing.T, f *dlFake) *fakeserver.Server {
	t.Helper()
	if f.st == nil {
		t.Fatal("dlFakeStart: no store")
	}
	if f.deadline == 0 {
		f.deadline = testWait
	}
	return fakeserver.New(t, fakeserver.Options{
		Deadline: testWait,
		Handler:  f.handle,
	})
}

// handle serves one connection: the subset of RFC 3977 that DownloadArticles uses.
func (f *dlFake) handle(c *fakeserver.Conn) {
	authOK := f.user == ""
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
		cmd := strings.ToUpper(fields[0])
		args := fields[1:]
		switch cmd {
		case "QUIT":
			_ = c.WriteLines("205 closing connection")
			return
		case "AUTHINFO":
			authOK = f.authinfo(c, args, authOK)
			continue
		}
		if !authOK {
			_ = c.WriteLines("480 authentication required")
			continue
		}
		switch cmd {
		case "MODE":
			_ = c.WriteLines("200 reader mode")
		case "CAPABILITIES":
			_ = c.WriteLines("101 capability list follows", "VERSION 2", "READER", ".")
		case "DATE":
			_ = c.WriteLines("111 " + time.Now().UTC().Format("20060102150405"))
		case "GROUP":
			group = f.group(c, args, group)
		case "XHDR":
			f.xhdr(c, group, args)
		case "ARTICLE":
			if !f.article(c, group, args) {
				authOK = false // the 480 ended this connection's login
			}
		default:
			_ = c.WriteLines("500 unknown command")
		}
	}
}

// authinfo answers AUTHINFO USER/PASS and reports whether the connection is logged in.
func (f *dlFake) authinfo(c *fakeserver.Conn, args []string, authOK bool) bool {
	if f.user == "" {
		_ = c.WriteLines("502 authentication not needed")
		return authOK
	}
	if len(args) < 2 {
		_ = c.WriteLines("501 syntax error")
		return authOK
	}
	switch strings.ToUpper(args[0]) {
	case "USER":
		if args[1] != f.user {
			_ = c.WriteLines("481 authentication failed")
			return false
		}
		_ = c.WriteLines("381 password required")
		return false
	case "PASS":
		if args[1] != f.pass {
			_ = c.WriteLines("481 authentication failed")
			return false
		}
		_ = c.WriteLines("281 authentication accepted")
		return true
	}
	_ = c.WriteLines("501 unknown AUTHINFO variant")
	return authOK
}

// group answers GROUP and returns the newly selected group (or the old one).
func (f *dlFake) group(c *fakeserver.Conn, args []string, cur string) string {
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
		g = f.groupNumbers(c.ID(), g)
	}
	_ = c.WriteLines(fmt.Sprintf("211 %d %d %d %s", g.Count, g.Low, g.High, args[0]))
	return args[0]
}

// xhdr answers "XHDR <field> <from>-<to>" for the selected group. Only message-id is
// served (the only field DownloadArticles asks for).
func (f *dlFake) xhdr(c *fakeserver.Conn, group string, args []string) {
	if group == "" {
		_ = c.WriteLines("412 no newsgroup selected")
		return
	}
	if len(args) != 2 {
		_ = c.WriteLines("501 syntax error")
		return
	}
	if !strings.EqualFold(args[0], "message-id") {
		_ = c.WriteLines("501 unsupported XHDR field")
		return
	}
	from, to, ok := dlFakeRange(args[1])
	if !ok {
		_ = c.WriteLines("501 bad range")
		return
	}
	lines := []string{"221 message-id fields follow"}
	for _, n := range f.st.NumbersIn(group, from, to) {
		a, ok := f.st.Article(group, n)
		if !ok {
			continue
		}
		id := a.MessageID
		if f.xhdrID != nil {
			id = f.xhdrID(c.ID(), group, n, id)
		}
		lines = append(lines, fmt.Sprintf("%d %s", n, id))
	}
	_ = c.WriteLines(append(lines, ".")...)
}

// dlFakeRange parses "n-m" (also "n" and "n-").
func dlFakeRange(s string) (int64, int64, bool) {
	lo, hi, dash := strings.Cut(s, "-")
	from, err := strconv.ParseInt(lo, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if !dash {
		return from, from, true
	}
	if hi == "" {
		return from, 0, true // open range: NumbersIn takes to <= 0 as High
	}
	to, err := strconv.ParseInt(hi, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return from, to, true
}

// article answers "ARTICLE <message-id>". It returns false when it wrote a 480 (the
// connection's login ended).
func (f *dlFake) article(c *fakeserver.Conn, group string, args []string) bool {
	seen := f.articles.Add(1)
	if len(args) != 1 || !strings.HasPrefix(args[0], "<") {
		_ = c.WriteLines("501 ARTICLE by number not served")
		return true
	}
	id := args[0]
	if f.auth480From > 0 && seen >= f.auth480From && f.auth480Left.Add(-1) >= 0 {
		f.sent480.Add(1)
		_ = c.WriteLines("480 login expired")
		return false
	}
	if f.endlessID != "" && id == f.endlessID {
		f.endless(c, id)
		return true
	}
	a, _, ok := f.st.ByMessageID(id)
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
	out = append(out, fmt.Sprintf("220 %d %s", a.Number, id))
	for _, h := range headers {
		out = append(out, dlFakeStuff(h))
	}
	out = append(out, "")
	for _, b := range a.Body {
		out = append(out, dlFakeStuff(b))
	}
	_ = c.WriteLines(append(out, ".")...)
	return true
}

// endless writes a 220 and then lines that never end, until the client closes the
// connection, endlessMax lines or the deadline (Faults.Endless for one message-id).
func (f *dlFake) endless(c *fakeserver.Conn, id string) {
	if err := c.WriteLines("220 0 "+id, "Path: fake.provider.invalid!not-for-mail", ""); err != nil {
		return
	}
	max := f.endlessMax
	if max <= 0 {
		max = dlFakeEndlessMax
	}
	stop := time.Now().Add(f.deadline)
	for i := int64(0); i < max; i++ {
		if err := c.WriteLines(fmt.Sprintf("endless line %d", i)); err != nil {
			return
		}
		if i%1024 == 0 && time.Now().After(stop) {
			return
		}
	}
}

// dlFakeStuff dot-stuffs one wire line (RFC 3977 §3.1.1).
func dlFakeStuff(line string) string {
	if strings.HasPrefix(line, ".") {
		return "." + line
	}
	return line
}
