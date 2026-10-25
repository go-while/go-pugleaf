package nntp

// IHAVE client tests of plan transfer-ihave-fallback, slice ihave-client:
// BackendConn.IHave against the kit's transit peer without streaming
// (PeerOptions.NoStream) and against scripted peers for the replies the peer
// does not model (480/500/501/502, a connection that dies after the 335).
//
// What is pinned: the four codes the caller counts (235/435/436/437) come back
// with a nil error, the sentinels (ErrAuthRequired, ErrIHaveUnsupported) are
// recognisable with errors.Is and leave the connection usable, a broken
// exchange force-closes it, an unsafe header block is skipped before anything
// is written, and the article IHAVE puts on the wire is byte-identical to the
// one TAKETHIS sends (both go through transferHeaders + writeArticleLines).
//
// No wall-clock assertion: every wait is the hang guard cliWait.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
)

// cliIHHost is the nntphostname of these transfers (the Path prefix).
const cliIHHost = "news.local.invalid"

// cliIHGroup is the newsgroup argument of IHave/SendTakeThisArticleStreaming.
const cliIHGroup = "a.b"

// cliIHPeer starts a kit transit peer without streaming (innd with streaming
// turned off: MODE STREAM 501, CHECK/TAKETHIS unknown, IHAVE works).
func cliIHPeer(t *testing.T, o *cliPeerOpts) *cliFakeServer {
	t.Helper()
	o.NoStream = true
	if o.Have == nil {
		o.Have = make(map[string]bool)
	}
	return newCliFakeServer(t, cliFakeOpts{Handler: cliPeerHandler(o)})
}

// cliIHSend calls c.IHave inside cliCmdRun (hang guard + panic guard).
func cliIHSend(t *testing.T, c *BackendConn, a *models.Article) (code int, txBytes int, err error) {
	t.Helper()
	if _, rerr := cliCmdRun(t, func() error {
		code, txBytes, err = c.IHave(a, ptrOfString(cliIHHost), cliIHGroup)
		return nil
	}); rerr != nil {
		t.Fatal(rerr)
	}
	return code, txBytes, err
}

// ptrOfString is the address of a local copy of s (the nntphostname argument).
func ptrOfString(s string) *string { return &s }

// cliIHWaitClosed polls until c is no longer connected (ForceCloseConn closes
// asynchronously); the hang guard is cliWait.
func cliIHWaitClosed(t *testing.T, c *BackendConn) {
	t.Helper()
	deadline := time.Now().Add(cliWait)
	for c.IsConnected() {
		if time.Now().After(deadline) {
			t.Fatalf("connection still connected %v after a broken IHAVE", cliWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cliIHRelayArticle is an article with a complete stored header block (relayed
// by RelayHeaders).
func cliIHRelayArticle(msgID string) *models.Article {
	hdr := strings.Join([]string{
		"Path: up.invalid!poster!not-for-mail",
		"From: A <a@b.invalid>",
		"Newsgroups: a.b,",
		" c.d",
		"Subject: ih",
		"Date: " + cliCmdDate,
		"Message-ID: " + msgID,
		"Xref: up.invalid a.b:1",
		".X-Dot: y",
	}, "\n")
	return cliCmdArticle(msgID, hdr, "a\n.b\n")
}

// TestCliIHaveAccepted: the peer answers 335 and then 235. The command line is
// the only line the peer reads (the article goes into its dot block), and the
// article is recorded.
func TestCliIHaveAccepted(t *testing.T) {
	s := cliIHPeer(t, &cliPeerOpts{})
	c := cliCmdDial(t, s, nil)
	a := cliIHRelayArticle("<ih-ok@test.invalid>")

	code, txBytes, err := cliIHSend(t, c, a)
	if err != nil || code != 235 {
		t.Fatalf("IHave = (%d, %d, %v), want (235, >0, nil)", code, txBytes, err)
	}
	if txBytes <= 0 {
		t.Errorf("txBytes = %d, want the octets of the article", txBytes)
	}
	if lines := s.Lines(); len(lines) != 1 || lines[0] != "IHAVE <ih-ok@test.invalid>" {
		t.Fatalf("peer read %q, want exactly one %q", lines, "IHAVE <ih-ok@test.invalid>")
	}
	if got := cliCmdWaitArticles(t, s, 1); len(got) != 1 {
		t.Fatalf("peer recorded %d articles, want 1", len(got))
	}
	if !c.IsConnected() {
		t.Error("connection closed after an accepted IHAVE")
	}
}

// TestCliIHaveWireEqualsTakeThis: IHAVE and TAKETHIS put byte-identical
// articles on the wire (one shared transferHeaders + writeArticleLines), for a
// relayed stored block and for a web-post block that falls back to
// ReconstructHeaders.
func TestCliIHaveWireEqualsTakeThis(t *testing.T) {
	web := cliCmdArticle("<ih-web@test.invalid>", strings.Join([]string{
		"MIME-Version: 1.0", "Content-Type: text/plain; charset=UTF-8",
		"From: A <a@b.invalid>", "Newsgroups: a.b"}, "\n"), "hello\n")
	web.Path = "web.invalid!not-for-mail"

	for _, tc := range []struct {
		name    string
		article *models.Article
	}{
		{"relayed block", cliIHRelayArticle("<ih-wire@test.invalid>")},
		{"reconstructed block", web},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// TAKETHIS against a streaming peer, IHAVE against a peer without streaming.
			st := newCliCmdStream(t)
			if doContinue, err := st.send(t, tc.article); err != nil || !doContinue {
				t.Fatalf("SendTakeThisArticleStreaming: err=%v doContinue=%v", err, doContinue)
			}
			want := cliCmdWaitArticles(t, st.s, 1)[0]

			s := cliIHPeer(t, &cliPeerOpts{})
			c := cliCmdDial(t, s, nil)
			if code, _, err := cliIHSend(t, c, tc.article); err != nil || code != 235 {
				t.Fatalf("IHave = (%d, %v), want (235, nil)", code, err)
			}
			got := cliCmdWaitArticles(t, s, 1)[0]

			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("IHAVE wire differs from TAKETHIS:\n IHAVE: %q\nTAKETHIS: %q", got, want)
			}
		})
	}
}

// TestCliIHaveNotWanted: an id the peer already holds is answered 435 before
// the article; nothing is written and the connection stays usable.
func TestCliIHaveNotWanted(t *testing.T) {
	id := "<ih-dup@test.invalid>"
	s := cliIHPeer(t, &cliPeerOpts{Have: map[string]bool{id: true}})
	c := cliCmdDial(t, s, nil)

	code, txBytes, err := cliIHSend(t, c, cliIHRelayArticle(id))
	if err != nil || code != 435 || txBytes != 0 {
		t.Fatalf("IHave = (%d, %d, %v), want (435, 0, nil)", code, txBytes, err)
	}
	if got := s.Articles(); len(got) != 0 {
		t.Errorf("peer recorded %d articles after a 435, want none: %q", len(got), got)
	}
	if lines := s.Lines(); len(lines) != 1 || lines[0] != "IHAVE "+id {
		t.Errorf("peer read %q, want only the IHAVE line", lines)
	}
	if !c.IsConnected() {
		t.Error("connection closed after a 435")
	}
}

// TestCliIHaveRejected: the peer takes the article (335) and refuses it
// afterwards (437). The caller counts it; the connection stays usable.
func TestCliIHaveRejected(t *testing.T) {
	id := "<ih-rej@test.invalid>"
	s := cliIHPeer(t, &cliPeerOpts{Reject: map[string]bool{id: true}})
	c := cliCmdDial(t, s, nil)

	code, txBytes, err := cliIHSend(t, c, cliIHRelayArticle(id))
	if err != nil || code != 437 {
		t.Fatalf("IHave = (%d, %d, %v), want (437, >0, nil)", code, txBytes, err)
	}
	if txBytes <= 0 {
		t.Errorf("txBytes = %d, want the octets of the transferred article", txBytes)
	}
	cliCmdWaitArticles(t, s, 1) // the peer read the article before refusing it
	if !c.IsConnected() {
		t.Error("connection closed after a 437")
	}
}

// TestCliIHaveDeferred: a 436 before the article (the peer is paused) is no
// error and writes nothing; offering the same article again works.
func TestCliIHaveDeferred(t *testing.T) {
	id := "<ih-defer@test.invalid>"
	s := cliIHPeer(t, &cliPeerOpts{Defer436Once: map[string]bool{id: true}})
	c := cliCmdDial(t, s, nil)
	a := cliIHRelayArticle(id)

	code, txBytes, err := cliIHSend(t, c, a)
	if err != nil || code != 436 || txBytes != 0 {
		t.Fatalf("first IHave = (%d, %d, %v), want (436, 0, nil)", code, txBytes, err)
	}
	if got := s.Articles(); len(got) != 0 {
		t.Errorf("peer recorded %d articles after a 436, want none: %q", len(got), got)
	}
	code, txBytes, err = cliIHSend(t, c, a)
	if err != nil || code != 235 || txBytes <= 0 {
		t.Fatalf("second IHave = (%d, %d, %v), want (235, >0, nil)", code, txBytes, err)
	}
	cliCmdWaitArticles(t, s, 1)
}

// TestCliIHaveRefusals: the replies a peer sends instead of 335 that end the
// run. 480 wraps ErrAuthRequired, 500/501 (no IHAVE at all) and 502 (transfer
// permission denied) wrap ErrIHaveUnsupported; all four leave the connection
// usable, because the caller only stops offering articles. An unknown status
// force-closes it: the peer's state is unknown.
//
// The kit's Faults do not reach an external Handler (PeerHandler), so these
// replies come from a scripted peer.
func TestCliIHaveRefusals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reply      string
		wantCode   int
		wantIs     error
		wantClosed bool
	}{
		{"480 auth", "480 authentication required", 480, ErrAuthRequired, false},
		{"500 unknown", "500 Unknown command", 500, ErrIHaveUnsupported, false},
		{"501 syntax", "501 Not supported", 501, ErrIHaveUnsupported, false},
		{"502 permission", "502 Transfer permission denied", 502, ErrIHaveUnsupported, false},
		{"unknown status", "999 what", 999, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
				{Expect: "IHAVE .*", Reply: []string{tc.reply}},
			}})
			c := cliCmdDial(t, s, nil)

			code, txBytes, err := cliIHSend(t, c, cliIHRelayArticle("<ih-ref@test.invalid>"))
			if err == nil {
				t.Fatalf("IHave = (%d, %d, nil), want an error", code, txBytes)
			}
			if code != tc.wantCode || txBytes != 0 {
				t.Errorf("IHave = (%d, %d, %v), want (%d, 0, ...)", code, txBytes, err, tc.wantCode)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want errors.Is %v", err, tc.wantIs)
			}
			if !strings.Contains(err.Error(), strings.Fields(tc.reply)[0]) {
				t.Errorf("err = %v, want the peer's status %q in it", err, strings.Fields(tc.reply)[0])
			}
			if got := s.Articles(); len(got) != 0 {
				t.Errorf("peer recorded %d articles although it refused the offer", len(got))
			}
			if tc.wantClosed {
				cliIHWaitClosed(t, c)
			} else if !c.IsConnected() {
				t.Errorf("connection closed after %q, want it usable", tc.reply)
			}
		})
	}
}

// TestCliIHaveDiesAfter335: the peer asks for the article and goes away. The
// article may be half-sent, so the connection is force-closed and the error is
// reported; a second IHave on it is "not connected".
func TestCliIHaveDiesAfter335(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "IHAVE .*", Reply: []string{"335 send it"}, CloseAfter: true},
	}})
	c := cliCmdDial(t, s, nil)
	a := cliIHRelayArticle("<ih-dead@test.invalid>")

	code, _, err := cliIHSend(t, c, a)
	if err == nil {
		t.Fatalf("IHave = (%d, nil), want an error after the peer disappeared", code)
	}
	cliIHWaitClosed(t, c)

	if _, _, err = cliIHSend(t, c, a); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("second IHave err = %v, want \"not connected\"", err)
	}
}

// TestCliIHaveInvalidHeadersSkipped: an unsafe stored block (two Subject
// lines) is skipped before the command is written; the connection stays
// connected and the next article is the first and only IHAVE on the wire.
func TestCliIHaveInvalidHeadersSkipped(t *testing.T) {
	s := cliIHPeer(t, &cliPeerOpts{})
	c := cliCmdDial(t, s, nil)

	bad := cliCmdArticle("<ih-bad@test.invalid>", strings.Join([]string{
		"Path: p!not-for-mail", "From: A <a@b.invalid>", "Newsgroups: a.b",
		"Subject: one", "Subject: two", "Date: " + cliCmdDate,
		"Message-ID: <ih-bad@test.invalid>"}, "\n"), "x\n")
	code, txBytes, err := cliIHSend(t, c, bad)
	if !errors.Is(err, common.ErrRelayHeadersInvalid) || code != 0 || txBytes != 0 {
		t.Fatalf("IHave of an unsafe block = (%d, %d, %v), want (0, 0, ErrRelayHeadersInvalid)", code, txBytes, err)
	}
	if !c.IsConnected() {
		t.Fatal("connection closed although nothing was written")
	}

	good := cliIHRelayArticle("<ih-good@test.invalid>")
	if code, _, err := cliIHSend(t, c, good); err != nil || code != 235 {
		t.Fatalf("IHave after the skip = (%d, %v), want (235, nil)", code, err)
	}
	if lines := s.Lines(); len(lines) != 1 || lines[0] != "IHAVE <ih-good@test.invalid>" {
		t.Fatalf("peer read %q, want only the IHAVE of the valid article", lines)
	}
}
