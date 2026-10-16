package nntp

// Tests for the local Xref: header (plan xref-history, slice xref-serve): xrefLine, the
// -xref modes off/partial/full in HEAD, ARTICLE, OVER, XOVER, HDR, XHDR and LIST
// OVERVIEW.FMT, the fallbacks to partial (no processor, lookup error) and the drop of a
// stored Xref: line. Identifiers start with xrf/TestXref. No wall-clock asserts; waits use
// the harness hang guard.

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

func TestXrefLine(t *testing.T) {
	others := []XrefLocation{
		{Group: "a.serving", ArticleNum: 4}, // the serving group itself: skipped
		{Group: "b.other", ArticleNum: 7},
		{Group: "", ArticleNum: 3},             // no group: skipped
		{Group: "c.unnumbered", ArticleNum: 0}, // unknown number: skipped
		{Group: "d.negative", ArticleNum: -1},  // skipped
		{Group: "e.last", ArticleNum: 12},
	}
	cases := []struct {
		name   string
		host   string
		group  string
		num    int64
		others []XrefLocation
		want   string
	}{
		{"serving-only", "news.test", "a.serving", 4, nil, "Xref: news.test a.serving:4"},
		{"serving-first-then-others", "news.test", "a.serving", 4, others, "Xref: news.test a.serving:4 b.other:7 e.last:12"},
		{"no-serving-group", "news.test", "", 0, others, "Xref: news.test a.serving:4 b.other:7 e.last:12"},
		{"serving-number-0", "news.test", "a.serving", 0, nil, ""},
		{"nothing", "news.test", "", 0, nil, ""},
		{"only-skipped", "news.test", "", 0, []XrefLocation{{Group: "", ArticleNum: 1}, {Group: "x.y", ArticleNum: 0}}, ""},
		{"duplicates-first-wins", "news.test", "a.serving", 4,
			[]XrefLocation{{Group: "b.other", ArticleNum: 7}, {Group: "a.serving", ArticleNum: 9}, {Group: "b.other", ArticleNum: 8}, {Group: "e.last", ArticleNum: 12}, {Group: "e.last", ArticleNum: 13}},
			"Xref: news.test a.serving:4 b.other:7 e.last:12"},
		{"duplicates-no-serving-group", "news.test", "", 0,
			[]XrefLocation{{Group: "b.other", ArticleNum: 0}, {Group: "b.other", ArticleNum: 7}, {Group: "b.other", ArticleNum: 8}},
			"Xref: news.test b.other:7"},
	}
	for _, tc := range cases {
		if got := xrefLine(tc.host, tc.group, tc.num, tc.others); got != tc.want {
			t.Errorf("%s: xrefLine = %q, want %q", tc.name, got, tc.want)
		}
		wantValue := strings.TrimPrefix(tc.want, "Xref: ")
		if got := xrefValue(tc.host, tc.group, tc.num, tc.others); got != wantValue {
			t.Errorf("%s: xrefValue = %q, want %q", tc.name, got, wantValue)
		}
	}
}

func TestXrefArticleHeaderLines(t *testing.T) {
	stored := &xrfArticle{headers: "Path: p!not-for-mail\nXref: upstream.invalid other.group:99\n\tother.two:5\nSubject: s\nxref: second.invalid g:1\nX-After: kept"}
	a := stored.article()
	// off: the stored lines unchanged (today's output), no local line
	off := articleHeaderLines(a, XrefOff, "Xref: ignored g:1")
	wantOff := strings.Split(stored.headers, "\n")
	if err := hExpectLines(off, wantOff); err != nil {
		t.Errorf("off: %v", err)
	}
	a = stored.article()
	got := articleHeaderLines(a, XrefPartial, "Xref: local.test g.local:3")
	want := []string{"Path: p!not-for-mail", "Subject: s", "X-After: kept", "Xref: local.test g.local:3"}
	if err := hExpectLines(got, want); err != nil {
		t.Errorf("partial: %v", err)
	}
	// no local value (by message-id outside the group, partial): the stored line is still dropped
	a = stored.article()
	got = articleHeaderLines(a, XrefPartial, "")
	if err := hExpectLines(got, want[:3]); err != nil {
		t.Errorf("partial without a value: %v", err)
	}
	// empty headers_json: just the local line
	got = articleHeaderLines((&xrfArticle{}).article(), XrefFull, "Xref: h g:1")
	if err := hExpectLines(got, []string{"Xref: h g:1"}); err != nil {
		t.Errorf("empty headers: %v", err)
	}
	if got := articleHeaderLines((&xrfArticle{}).article(), XrefOff, ""); len(got) != 0 {
		t.Errorf("empty headers, off: %q", got)
	}
}

func TestXrefOverviewFmt(t *testing.T) {
	base := []string{"Subject:", "From:", "Date:", "Message-ID:", "References:", ":bytes", ":lines"}
	for _, mode := range []XrefMode{XrefOff, XrefPartial, XrefFull} {
		want := base
		if mode != XrefOff {
			want = append(append([]string{}, base...), "Xref:full")
		}
		if err := hExpectLines(overviewFmtLines(mode), want); err != nil {
			t.Errorf("%s: %v", mode, err)
		}
	}
}

// TestXrefModes: every command that serves Xref, in each mode, by number, by range and by
// message-id inside the current group, and by message-id without a selected group.
func TestXrefModes(t *testing.T) {
	g := hGroup(t, "xrf.modes", 3, nil)
	other := hGroupName("xrf.other") // only named in the history locations
	id1, id2, id3 := hMessageID(g, 1), hMessageID(g, 2), hMessageID(g, 3)
	proc := ovhFindIn(g, id1, id2, id3)
	proc.xrefLocs = map[string][]XrefLocation{
		// as the processor returns them: the serving group too (skipped), entries without a
		// number or group (skipped)
		id2: {{Group: g, ArticleNum: 2}, {Group: other, ArticleNum: 7}, {Group: "", ArticleNum: 5}, {Group: "xrf.unknown", ArticleNum: 0}},
	}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	const host = "go-pugleaf" // Config.Hostname is empty in the harness: the greeting's fallback

	type want struct {
		in2     string // value for article 2 in g
		in1     string // value for article 1 in g (no other locations)
		outside string // value for article 2 by message-id without a group
	}
	modes := []struct {
		mode XrefMode
		want want
	}{
		{XrefOff, want{}},
		{XrefPartial, want{
			in2: host + " " + g + ":2",
			in1: host + " " + g + ":1",
		}},
		{XrefFull, want{
			in2:     host + " " + g + ":2 " + other + ":7",
			in1:     host + " " + g + ":1",
			outside: host + " " + g + ":2 " + other + ":7",
		}},
	}
	for _, m := range modes {
		t.Run(m.mode.String(), func(t *testing.T) {
			hs.S.SetXrefMode(m.mode)
			w := m.want
			c := hs.dial(t)
			defer c.close()

			// --- without a selected group (RFC 3977 §6.2.1.2: number 0)
			xrfCheckHead(t, c, "HEAD "+id2, w.outside)
			xrfCheckArticle(t, c, "ARTICLE "+id2, w.outside)
			xrfCheckOver(t, m.mode, xrfBlock(t, c, 224, "OVER %s", id2), []string{w.outside})
			if err := hExpectLines(xrfBlock(t, c, 225, "HDR Xref %s", id2), []string{xrfHdr("0", w.outside)}); err != nil {
				t.Errorf("HDR Xref <id> outside: %v", err)
			}
			if err := hExpectLines(xrfBlock(t, c, 221, "XHDR Xref %s", id2), []string{xrfHdr(id2, w.outside)}); err != nil {
				t.Errorf("XHDR Xref <id> outside: %v", err)
			}

			// --- in the group
			xrfSelect(t, c, g)
			// lookups: exactly one per served article in full, none in off/partial (the
			// command's reply is fully read before the count, so the handler has returned)
			wantCalls := func(n int) int {
				if m.mode == XrefFull {
					return n
				}
				return 0
			}
			before := proc.xrefCount()
			xrfCheckHead(t, c, "HEAD 2", w.in2)
			if d := proc.xrefCount() - before; d != wantCalls(1) {
				t.Errorf("HEAD 2: %d XrefLocations calls, want %d", d, wantCalls(1))
			}
			xrfCheckArticle(t, c, "ARTICLE 2", w.in2)
			xrfCheckHead(t, c, "HEAD "+id2, w.in2)
			xrfCheckArticle(t, c, "ARTICLE "+id2, w.in2)
			xrfCheckHead(t, c, "HEAD 1", w.in1)

			partial3 := ""
			if m.mode != XrefOff {
				partial3 = host + " " + g + ":3"
			}
			// a range in full (batched lookup, the default): one XrefLocationsBatch call with
			// every id of the chunk and no XrefLocations call; none of either in off/partial
			before = proc.xrefCount()
			beforeCalls, beforeIDs := proc.xrefBatchCount()
			overLines := xrfBlock(t, c, 224, "OVER 1-3")
			if d := proc.xrefCount() - before; d != 0 {
				t.Errorf("OVER 1-3: %d XrefLocations calls for %d lines, want 0", d, len(overLines))
			}
			calls, ids := proc.xrefBatchCount()
			wantBatch := 0
			if m.mode == XrefFull {
				wantBatch = 1
			}
			if calls-beforeCalls != wantBatch || ids-beforeIDs != wantBatch*len(overLines) {
				t.Errorf("OVER 1-3: %d XrefLocationsBatch calls with %d ids, want %d with %d", calls-beforeCalls, ids-beforeIDs, wantBatch, wantBatch*len(overLines))
			}
			xrfCheckOver(t, m.mode, overLines, []string{w.in1, w.in2, partial3})
			xrfCheckOver(t, m.mode, xrfBlock(t, c, 224, "OVER %s", id2), []string{w.in2})
			xrfCheckOver(t, m.mode, xrfBlock(t, c, 224, "XOVER 1-3"), []string{w.in1, w.in2, partial3})
			xrfCheckOver(t, m.mode, xrfBlock(t, c, 224, "XOVER 2"), []string{w.in2})

			wantRange := []string{xrfHdr("1", w.in1), xrfHdr("2", w.in2), xrfHdr("3", partial3)}
			if err := hExpectLines(xrfBlock(t, c, 225, "HDR Xref 1-3"), wantRange); err != nil {
				t.Errorf("HDR Xref 1-3: %v", err)
			}
			if err := hExpectLines(xrfBlock(t, c, 225, "HDR XREF 2-"), wantRange[1:]); err != nil {
				t.Errorf("HDR XREF 2-: %v", err)
			}
			if err := hExpectLines(xrfBlock(t, c, 225, "HDR Xref %s", id2), []string{xrfHdr("2", w.in2)}); err != nil {
				t.Errorf("HDR Xref <id>: %v", err)
			}
			if err := hExpectLines(xrfBlock(t, c, 221, "XHDR Xref 1-3"), wantRange); err != nil {
				t.Errorf("XHDR Xref 1-3: %v", err)
			}
			if err := hExpectLines(xrfBlock(t, c, 221, "XHDR xref %s", id2), []string{xrfHdr(id2, w.in2)}); err != nil {
				t.Errorf("XHDR Xref <id>: %v", err)
			}

			fmtLines := xrfBlock(t, c, 215, "LIST OVERVIEW.FMT")
			hasFull := len(fmtLines) > 0 && fmtLines[len(fmtLines)-1] == "Xref:full"
			if hasFull != (m.mode != XrefOff) || len(fmtLines) != len(overviewFmtLines(m.mode)) {
				t.Errorf("LIST OVERVIEW.FMT in %s: %q", m.mode, fmtLines)
			}
			// off and partial never look up locations (subtests run in order off, partial, full)
			if bc, _ := proc.xrefBatchCount(); m.mode != XrefFull && (proc.xrefCount() != 0 || bc != 0) {
				t.Errorf("%s: %d XrefLocations and %d XrefLocationsBatch calls, want 0", m.mode, proc.xrefCount(), bc)
			}
		})
	}
	if proc.xrefCount() == 0 {
		t.Error("full mode never called XrefLocations")
	}
}

// TestXrefNoProcessor: full without a processor behaves like partial.
func TestXrefNoProcessor(t *testing.T) {
	g := hGroup(t, "xrf.noproc", 2, nil)
	hs := newTestServer(t, hServerOpts{})
	hs.S.SetXrefMode(XrefFull)
	c := hs.dial(t)
	defer c.close()
	xrfSelect(t, c, g)
	want := "go-pugleaf " + g + ":2"
	xrfCheckHead(t, c, "HEAD 2", want)
	xrfCheckHead(t, c, "HEAD "+hMessageID(g, 2), want)
	xrfCheckOver(t, XrefFull, xrfBlock(t, c, 224, "OVER 2"), []string{want})
	if err := hExpectLines(xrfBlock(t, c, 225, "HDR Xref 2"), []string{"2 " + want}); err != nil {
		t.Error(err)
	}
}

// TestXrefLookupError: a failing XrefLocations falls back to partial, logged once per server
// (the batched range lookup fails too and falls back to the failing per-id lookup).
func TestXrefLookupError(t *testing.T) {
	g := hGroup(t, "xrf.lookuperr", 2, nil)
	proc := &hFakeProcessor{xrefErr: errors.New("history closed"), xrefBatchErr: errors.New("history closed")}
	proc.xrefLocs = map[string][]XrefLocation{hMessageID(g, 2): {{Group: "never.served", ArticleNum: 1}}}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	hs.S.SetXrefMode(XrefFull)
	c := hs.dial(t)
	defer c.close()
	xrfSelect(t, c, g)
	xrfCheckHead(t, c, "HEAD 2", "go-pugleaf "+g+":2")
	xrfCheckOver(t, XrefFull, xrfBlock(t, c, 224, "OVER 1-2"), []string{"go-pugleaf " + g + ":1", "go-pugleaf " + g + ":2"})
	if proc.xrefCount() < 3 {
		t.Errorf("XrefLocations calls: %d, want >= 3", proc.xrefCount())
	}
	if !hs.S.xref.errLogged.Load() {
		t.Error("lookup failure not recorded as logged")
	}
}

// TestXrefBatchedRanges (plan xref-batched-lookup): in full with the batched lookup,
// OVER/XOVER/HDR/XHDR ranges make one XrefLocationsBatch call per chunk of
// database.OverviewRangeChunk rows, with every id of that chunk, and no XrefLocations call;
// with the batched lookup off one XrefLocations call per line and no batch call. The served
// lines are byte-identical either way. off and partial call neither; HEAD and ARTICLE keep the
// per-id lookup.
func TestXrefBatchedRanges(t *testing.T) {
	n := database.OverviewRangeChunk + 1 // two chunks: OverviewRangeChunk rows, then 1
	const wantChunks = 2
	g := hGroup(t, "xrf.batched", n, nil)
	other := hGroupName("xrf.batchother") // only named in the history locations
	proc := &hFakeProcessor{xrefLocs: map[string][]XrefLocation{}}
	for i := int64(1); i <= int64(n); i += 3 {
		// as the processor returns them: the serving group too (skipped by xrefValue)
		proc.xrefLocs[hMessageID(g, i)] = []XrefLocation{{Group: g, ArticleNum: i}, {Group: other, ArticleNum: i + 10}}
	}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	if !hs.S.XrefBatchedLookup() {
		t.Fatal("a new server must default to the batched lookup")
	}
	const host = "go-pugleaf"
	c := hs.dial(t)
	defer c.close()
	xrfSelect(t, c, g)

	type counts struct{ perID, batchCalls, batchIDs int }
	run := func(t *testing.T, mode XrefMode, batched bool, code int, cmd string) ([]string, counts) {
		t.Helper()
		hs.S.SetXrefMode(mode)
		hs.S.SetXrefBatchedLookup(batched)
		per0 := proc.xrefCount()
		bc0, bi0 := proc.xrefBatchCount()
		lines := xrfBlock(t, c, code, "%s", cmd) // fully read: the handler has returned
		bc, bi := proc.xrefBatchCount()
		return lines, counts{proc.xrefCount() - per0, bc - bc0, bi - bi0}
	}
	cmds := []struct {
		code int
		cmd  string
	}{
		{224, "OVER 1-"},
		{224, fmt.Sprintf("XOVER 1-%d", n)},
		{225, "HDR Xref 1-"},
		{221, fmt.Sprintf("XHDR xref 1-%d", n)},
	}
	for _, mode := range []XrefMode{XrefOff, XrefPartial, XrefFull} {
		for _, cc := range cmds {
			t.Run(mode.String()+"/"+cc.cmd, func(t *testing.T) {
				on, cOn := run(t, mode, true, cc.code, cc.cmd)
				off, cOff := run(t, mode, false, cc.code, cc.cmd)
				if len(on) != n || len(off) != n {
					t.Fatalf("%d lines batched, %d per-id, want %d", len(on), len(off), n)
				}
				for i := range on {
					if on[i] != off[i] {
						t.Fatalf("line %d differs:\nbatched %q\nper-id  %q", i, on[i], off[i])
					}
				}
				wantOn, wantOff := counts{}, counts{}
				if mode == XrefFull {
					wantOn = counts{perID: 0, batchCalls: wantChunks, batchIDs: n}
					wantOff = counts{perID: n}
				}
				if cOn != wantOn {
					t.Errorf("batched lookup: %+v, want %+v", cOn, wantOn)
				}
				if cOff != wantOff {
					t.Errorf("per-id lookup: %+v, want %+v", cOff, wantOff)
				}
				// the served values themselves (not only equal both ways): article 1 has another
				// location, article 2 none, the last row (the second chunk) per its index
				want1, want2 := "", ""
				switch mode {
				case XrefPartial:
					want1, want2 = host+" "+g+":1", host+" "+g+":2"
				case XrefFull:
					want1, want2 = host+" "+g+":1 "+other+":11", host+" "+g+":2"
				}
				lastNum := int64(n)
				wantLast := ""
				if mode != XrefOff {
					wantLast = host + " " + g + ":" + fmt.Sprint(lastNum)
					if mode == XrefFull && (lastNum-1)%3 == 0 {
						wantLast += " " + other + ":" + fmt.Sprint(lastNum+10)
					}
				}
				if cc.code == 224 {
					xrfCheckOver(t, mode, []string{on[0], on[1], on[n-1]}, []string{want1, want2, wantLast})
				} else {
					got := []string{on[0], on[1], on[n-1]}
					want := []string{xrfHdr("1", want1), xrfHdr("2", want2), xrfHdr(fmt.Sprint(lastNum), wantLast)}
					if err := hExpectLines(got, want); err != nil {
						t.Error(err)
					}
				}
			})
		}
	}

	// HEAD/ARTICLE (by number and by message-id) keep the per-id lookup with batching on
	hs.S.SetXrefMode(XrefFull)
	hs.S.SetXrefBatchedLookup(true)
	per0 := proc.xrefCount()
	bc0, _ := proc.xrefBatchCount()
	want1 := host + " " + g + ":1 " + other + ":11"
	xrfCheckHead(t, c, "HEAD 1", want1)
	xrfCheckArticle(t, c, "ARTICLE 1", want1)
	xrfCheckHead(t, c, "HEAD "+hMessageID(g, 1), want1)
	xrfCheckOver(t, XrefFull, xrfBlock(t, c, 224, "OVER %s", hMessageID(g, 1)), []string{want1})
	if bc, _ := proc.xrefBatchCount(); proc.xrefCount()-per0 != 4 || bc != bc0 {
		t.Errorf("single-article commands: %d XrefLocations and %d XrefLocationsBatch calls, want 4 and 0", proc.xrefCount()-per0, bc-bc0)
	}
}

// TestXrefBatchedLookupError: a failing XrefLocationsBatch is logged once per server and the
// range falls back to one XrefLocations call per line: the output is still the full value.
func TestXrefBatchedLookupError(t *testing.T) {
	g := hGroup(t, "xrf.batcherr", 3, nil)
	other := hGroupName("xrf.batcherrother")
	id2 := hMessageID(g, 2)
	proc := &hFakeProcessor{xrefBatchErr: errors.New("shard table locked")}
	proc.xrefLocs = map[string][]XrefLocation{id2: {{Group: other, ArticleNum: 7}}}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	hs.S.SetXrefMode(XrefFull)
	c := hs.dial(t)
	defer c.close()
	xrfSelect(t, c, g)
	const host = "go-pugleaf"
	values := []string{host + " " + g + ":1", host + " " + g + ":2 " + other + ":7", host + " " + g + ":3"}
	hdr := []string{xrfHdr("1", values[0]), xrfHdr("2", values[1]), xrfHdr("3", values[2])}

	check := func(what string, serve func()) {
		t.Helper()
		per0 := proc.xrefCount()
		bc0, _ := proc.xrefBatchCount()
		serve()
		bc, _ := proc.xrefBatchCount()
		if proc.xrefCount()-per0 != 3 || bc-bc0 != 1 {
			t.Errorf("%s: %d XrefLocations and %d XrefLocationsBatch calls, want 3 and 1", what, proc.xrefCount()-per0, bc-bc0)
		}
	}
	check("OVER 1-3", func() { xrfCheckOver(t, XrefFull, xrfBlock(t, c, 224, "OVER 1-3"), values) })
	check("XOVER 1-3", func() { xrfCheckOver(t, XrefFull, xrfBlock(t, c, 224, "XOVER 1-3"), values) })
	check("HDR Xref 1-3", func() {
		if err := hExpectLines(xrfBlock(t, c, 225, "HDR Xref 1-3"), hdr); err != nil {
			t.Errorf("HDR Xref 1-3: %v", err)
		}
	})
	check("XHDR Xref 1-3", func() {
		if err := hExpectLines(xrfBlock(t, c, 221, "XHDR Xref 1-3"), hdr); err != nil {
			t.Errorf("XHDR Xref 1-3: %v", err)
		}
	})
	if !hs.S.xref.errLogged.Load() {
		t.Error("batch lookup failure not recorded as logged")
	}
}

// TestXrefStoredLineDropped: a stored Xref: line (another server's numbers) is never served
// next to the local one; in off mode the stored lines are sent as today.
func TestXrefStoredLineDropped(t *testing.T) {
	g := hGroup(t, "xrf.stored", 1, func(i int, a *hArticle) {
		a.Headers = strings.Join([]string{
			"Path: " + a.Path,
			"From: " + a.From,
			"Xref: upstream.invalid other.group:99",
			"\tother.two:5",
			"Subject: " + a.Subject,
			"Message-ID: " + a.MessageID,
		}, "\n")
	})
	a := hDefaultArticle(g, 1)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	defer c.close()
	xrfSelect(t, c, g)

	hs.S.SetXrefMode(XrefPartial)
	want := []string{"Path: " + a.Path, "From: " + a.From, "Subject: " + a.Subject, "Message-ID: " + a.MessageID, "Xref: go-pugleaf " + g + ":1"}
	if err := hExpectLines(xrfBlock(t, c, 221, "HEAD 1"), want); err != nil {
		t.Errorf("partial: %v", err)
	}
	hs.S.SetXrefMode(XrefOff)
	wantOff := []string{"Path: " + a.Path, "From: " + a.From, "Xref: upstream.invalid other.group:99", "\tother.two:5", "Subject: " + a.Subject, "Message-ID: " + a.MessageID}
	if err := hExpectLines(xrfBlock(t, c, 221, "HEAD 1"), wantOff); err != nil {
		t.Errorf("off: %v", err)
	}
}

// TestXrefHostname: Config.Hostname is the Xref host when set.
func TestXrefHostname(t *testing.T) {
	g := hGroup(t, "xrf.host", 1, nil)
	hs := xrfServerWithHostname(t, "news.xref.test")
	if got := hs.S.XrefMode(); got != XrefPartial {
		t.Fatalf("default mode %s, want partial", got)
	}
	c := hs.dial(t)
	defer c.close()
	xrfSelect(t, c, g)
	xrfCheckHead(t, c, "HEAD 1", "news.xref.test "+g+":1")
	if err := hExpectLines(xrfBlock(t, c, 225, "HDR Xref 1"), []string{"1 news.xref.test " + g + ":1"}); err != nil {
		t.Error(err)
	}
	if got := (&NNTPServer{Config: &config.ServerConfig{}}).xrefHostname(); got != "go-pugleaf" {
		t.Errorf("fallback hostname %q", got)
	}
}

// ---------------------------------------------------------------------------
// Helpers

// xrfArticle builds an article with a given header block for articleHeaderLines.
type xrfArticle struct{ headers string }

func (x *xrfArticle) article() *models.Article {
	return &models.Article{HeadersJSON: x.headers}
}

// xrfServerWithHostname is newTestServer (no processor, zero delays) with Config.Hostname
// set before Serve, so no connection goroutine can see the write.
func xrfServerWithHostname(t *testing.T, hostname string) *hServer {
	t.Helper()
	db := hTestDB(t)
	cfg := &config.ServerConfig{}
	cfg.NNTP.MaxConns = 100
	cfg.Hostname = hostname
	hs := &hServer{}
	s, err := NewNNTPServer(db, cfg, &hs.wg, nil)
	if err != nil {
		t.Fatalf("NewNNTPServer: %v", err)
	}
	s.ArticleCmdDelay = 0
	s.GroupCmdDelay = 0
	s.ErrorDelay = 0
	hs.S = s
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	hs.Addr = l.Addr().String()
	if err := s.Serve(l, false); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { hs.stop(t) })
	return hs
}

// xrfSelect sends GROUP g and expects 211.
func xrfSelect(t *testing.T, c *hClient, g string) {
	t.Helper()
	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
}

// xrfBlock sends a command, expects the multi-line status want and returns the block.
func xrfBlock(t *testing.T, c *hClient, want int, format string, args ...interface{}) []string {
	t.Helper()
	code, line := c.cmd(t, format, args...)
	if err := hExpectCode(code, line, want); err != nil {
		t.Fatalf("%s: %v", fmt.Sprintf(format, args...), err)
	}
	return c.readDotLines(t)
}

// xrfHdr is the HDR/XHDR line for key and the Xref value (just key when it is empty).
func xrfHdr(key, value string) string {
	if value == "" {
		return key
	}
	return key + " " + value
}

// xrfXrefLines returns the header lines starting with "Xref:" (case-insensitive) and
// whether such a line is the last one.
func xrfXrefLines(head []string) (lines []string, last bool) {
	for i, l := range head {
		if len(l) >= 5 && strings.EqualFold(l[:5], "xref:") {
			lines = append(lines, l)
			last = i == len(head)-1
		}
	}
	return lines, last
}

// xrfCheckHeaders checks that head carries exactly the Xref line "Xref: value" as the last
// header line, or no Xref line when value is empty.
func xrfCheckHeaders(t *testing.T, what string, head []string, value string) {
	t.Helper()
	lines, last := xrfXrefLines(head)
	if value == "" {
		if len(lines) != 0 {
			t.Errorf("%s: unexpected Xref line(s) %q", what, lines)
		}
		return
	}
	if len(lines) != 1 || lines[0] != "Xref: "+value || !last {
		t.Errorf("%s: Xref lines %q (last=%v), want only %q as the last header line", what, lines, last, "Xref: "+value)
	}
}

// xrfCheckHead sends a HEAD command and checks its Xref line.
func xrfCheckHead(t *testing.T, c *hClient, cmd, value string) {
	t.Helper()
	xrfCheckHeaders(t, cmd, xrfBlock(t, c, 221, "%s", cmd), value)
}

// xrfCheckArticle sends an ARTICLE command and checks the Xref line of its header part.
func xrfCheckArticle(t *testing.T, c *hClient, cmd, value string) {
	t.Helper()
	block := xrfBlock(t, c, 220, "%s", cmd)
	head := block
	for i, l := range block {
		if l == "" {
			head = block[:i]
			break
		}
	}
	if len(head) == len(block) {
		t.Fatalf("%s: no header/body separator in %q", cmd, block)
	}
	xrfCheckHeaders(t, cmd, head, value)
}

// xrfCheckOver checks the field count of overview lines (8 in off, 9 otherwise) and the
// 9th field ("Xref: value", empty when value is empty).
func xrfCheckOver(t *testing.T, mode XrefMode, lines []string, values []string) {
	t.Helper()
	if len(lines) != len(values) {
		t.Fatalf("%d overview lines, want %d: %q", len(lines), len(values), lines)
	}
	for i, l := range lines {
		f := strings.Split(l, "\t")
		if mode == XrefOff {
			if len(f) != 8 {
				t.Errorf("off: line %d has %d fields, want 8: %q", i, len(f), l)
			}
			continue
		}
		want := ""
		if values[i] != "" {
			want = "Xref: " + values[i]
		}
		if len(f) != 9 || f[8] != want {
			t.Errorf("%s: line %d: %q, want 9 fields with the 9th %q", mode, i, l, want)
		}
	}
}
