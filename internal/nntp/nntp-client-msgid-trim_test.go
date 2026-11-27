package nntp

// Plan laxmid (A): the message-ids of XHDR Message-ID and XOVER replies are trimmed of the
// SP/HT around them, so an id the server sends with a leading blank (arc1-ca, gmane:
// " <erih4t$1bs$1@sea.gmane.org>") is still fetched instead of failing the ARTICLE argument
// check and being skipped. The values of other fields are left as sent. This file uses no
// API of the laxmid change, so it also runs on the base commit (where it fails).
// Hang guards only (cliCmdRun, cliWait), no timing assertion.

import (
	"reflect"
	"testing"
)

// cliTrimArticle is the ARTICLE reply for id.
func cliTrimArticle(id string) []string {
	return []string{"220 0 " + id,
		"Path: x!not-for-mail", "From: A <a@b.invalid>", "Newsgroups: g", "Subject: s",
		"Date: " + cliCmdDate, "Message-ID: " + id, "", "body", "."}
}

// TestCliOverviewTrimsMessageID: a Message-ID that an XHDR or XOVER reply carries with blanks
// around it ("1  <id>", "<id> ", a tab) is returned without them, and the following ARTICLE
// is sent and the article fetched. Another XHDR field and the other overview fields are
// returned as sent.
func TestCliOverviewTrimsMessageID(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		// the fetcher's path: XHdrStreamed
		{Expect: "GROUP g", Reply: []string{"211 3 1 3 g"}},
		{Expect: "XHDR message-id 1-3", Reply: []string{"221 message-id follows",
			"1  <m1@x.invalid>", "2 <m2@x.invalid> ", "3 \t<m3@x.invalid>\t", "."}},
		// XHdr (analyze)
		{Expect: "GROUP g", Reply: []string{"211 3 1 3 g"}},
		{Expect: "XHDR Message-ID 1-3", Reply: []string{"221 message-id follows",
			"1  <m1@x.invalid>", "2 <m2@x.invalid>", "3 <m3@x.invalid>  ", "."}},
		// another field: left as sent
		{Expect: "GROUP g", Reply: []string{"211 3 1 3 g"}},
		{Expect: "XHDR Subject 1-3", Reply: []string{"221 subject follows",
			"1  padded ", "2 plain", "3 x", "."}},
		// XOVER (import-overview, date search)
		{Expect: "GROUP g", Reply: []string{"211 3 1 3 g"}},
		{Expect: "XOVER 1-3", Reply: []string{"224 overview follows",
			"1\t s \tf\t" + cliCmdDate + "\t <m1@x.invalid>\t <r@x.invalid>\t10\t1",
			"2\ts\tf\t" + cliCmdDate + "\t<m2@x.invalid> \t\t10\t1",
			"3\ts\tf\t" + cliCmdDate + "\t<m3@x.invalid>\t\t10\t1",
			"."}},
		{Expect: `ARTICLE <m1@x\.invalid>`, Reply: cliTrimArticle("<m1@x.invalid>")},
	}})
	c := cliCmdDial(t, s, nil)
	want := []HeaderLine{{ArticleNum: 1, Value: "<m1@x.invalid>"}, {ArticleNum: 2, Value: "<m2@x.invalid>"}, {ArticleNum: 3, Value: "<m3@x.invalid>"}}

	ch := make(chan HeaderLine, 16)
	if _, err := cliCmdRun(t, func() error { return c.XHdrStreamed("g", "message-id", 1, 3, ch, nil) }); err != nil {
		t.Fatalf("XHdrStreamed: %v", err)
	}
	streamed := cliCmdDrain(t, ch)
	if !reflect.DeepEqual(streamed, want) {
		t.Errorf("XHdrStreamed message-id = %q, want %q", streamed, want)
	}

	var hdrs []HeaderLine
	if _, err := cliCmdRun(t, func() error { var err error; hdrs, err = c.XHdr("g", "Message-ID", 1, 3); return err }); err != nil {
		t.Fatalf("XHdr: %v", err)
	}
	if !reflect.DeepEqual(hdrs, want) {
		t.Errorf("XHdr Message-ID = %q, want %q", hdrs, want)
	}

	if _, err := cliCmdRun(t, func() error { var err error; hdrs, err = c.XHdr("g", "Subject", 1, 3); return err }); err != nil {
		t.Fatalf("XHdr Subject: %v", err)
	}
	if wantSubj := []HeaderLine{{ArticleNum: 1, Value: " padded "}, {ArticleNum: 2, Value: "plain"}, {ArticleNum: 3, Value: "x"}}; !reflect.DeepEqual(hdrs, wantSubj) {
		t.Errorf("XHdr Subject = %q, want %q (other fields are not trimmed)", hdrs, wantSubj)
	}

	var ov []OverviewLine
	if _, err := cliCmdRun(t, func() error { var err error; ov, err = c.XOver("g", 1, 3, false); return err }); err != nil {
		t.Fatalf("XOver: %v", err)
	}
	if len(ov) != 3 {
		t.Fatalf("XOver = %+v, want 3 lines", ov)
	}
	for i, o := range ov {
		if o.MessageID != want[i].Value {
			t.Errorf("XOver line %d MessageID = %q, want %q", i+1, o.MessageID, want[i].Value)
		}
	}
	if ov[0].Subject != " s " || ov[0].References != " <r@x.invalid>" {
		t.Errorf("XOver line 1 Subject %q References %q, want them as sent", ov[0].Subject, ov[0].References)
	}

	// the id from the XHDR stream is fetched
	if len(streamed) == 0 {
		t.Fatal("XHdrStreamed returned nothing")
	}
	msgID := streamed[0].Value
	if _, err := cliCmdRun(t, func() error { _, err := c.GetArticle(&msgID, true); return err }); err != nil {
		t.Fatalf("GetArticle(%q): %v", msgID, err)
	}
	if m := s.Mismatches(); len(m) != 0 {
		t.Fatalf("Mismatches() = %q", m)
	}
}
