package nntp

// Plan laxmid, server side: by default IHAVE, CHECK and TAKETHIS refuse a message-id without
// "@" (501, 438, 439) exactly as before; with -laxmid (common.SetLaxMessageID) they take it.
// ">" inside and more than 250 octets are refused in both modes. The lookup commands
// (ARTICLE/HEAD/BODY/STAT <id>) never check the syntax: a stored id is found in both modes.
// The real processor (intake, history) is covered by cmd/nntp-server/e2e_laxmid_test.go.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/history"
)

// srvLaxmidID returns a unique RFC 3977 message-id without "@", like news.php.net's.
func srvLaxmidID() string { return fmt.Sprintf("<%d_67890>", hSeq.Add(1)) }

// srvLaxmidWire is a complete article with Message-ID id in group, dot-terminated, as a feed
// command sends it.
func srvLaxmidWire(id, group string) string {
	return strings.Join([]string{
		"Path: peer.invalid!not-for-mail",
		"From: A <a@test.invalid>",
		"Newsgroups: " + group,
		"Subject: laxmid",
		"Date: Sat, 11 Oct 2026 10:00:00 +0000",
		"Message-ID: " + id,
		"",
		"body",
		".",
	}, "\r\n") + "\r\n"
}

func TestServerLaxmid(t *testing.T) {
	stored := srvLaxmidID()
	g := hGroup(t, "lx1.laxmid", 2, func(i int, a *hArticle) {
		if i == 2 {
			a.MessageID = stored
		}
	})
	peerReg, _ := hPeerIP("")
	proc := &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
	srv := newTestServer(t, hServerOpts{Proc: proc, Peers: peerReg})
	tooLong := "<" + strings.Repeat("a", 249) + ">" // 251 octets

	lookup := func(t *testing.T) {
		t.Helper()
		c := srv.dial(t)
		c.ExpectCmd(211, "GROUP %s", g)
		if line := c.ExpectCmd(220, "ARTICLE %s", stored); !strings.HasPrefix(line, "220 2 "+stored) {
			t.Fatalf("ARTICLE %s: %q, want 220 2 %s", stored, line, stored)
		}
		c.ReadDotLines()
		c.ExpectCmd(221, "HEAD %s", stored)
		c.ReadDotLines()
		c.ExpectCmd(222, "BODY %s", stored)
		c.ReadDotLines()
		if line := c.ExpectCmd(223, "STAT %s", stored); !strings.HasPrefix(line, "223 2 "+stored) {
			t.Fatalf("STAT %s: %q, want 223 2 %s", stored, line, stored)
		}
	}
	// the refusals of both modes
	invalid := func(t *testing.T) {
		t.Helper()
		for _, id := range []string{"<a>b>", "<a>b@c>", tooLong} {
			c := srv.dial(t)
			c.ExpectCmd(501, "IHAVE %s", id)
			c.ExpectCmd(438, "CHECK %s", id)
			if err := c.Send("TAKETHIS " + id + "\r\n" + srvLaxmidWire(id, g)); err != nil {
				t.Fatal(err)
			}
			if code, line, err := c.ReadStatus(); err != nil || code != 439 {
				t.Fatalf("TAKETHIS %q: code=%d line=%q err=%v, want 439", id, code, line, err)
			}
		}
	}

	t.Run("strict", func(t *testing.T) {
		if common.LaxMessageID() {
			t.Fatal("lax message-ids are on by default")
		}
		lookup(t)
		invalid(t)
		before := len(proc.processedArticles())
		id := srvLaxmidID()
		c := srv.dial(t)
		c.ExpectCmd(501, "IHAVE %s", id)
		c.ExpectCmd(438, "CHECK %s", id)
		if err := c.Send("TAKETHIS " + id + "\r\n" + srvLaxmidWire(id, g)); err != nil {
			t.Fatal(err)
		}
		if code, line, err := c.ReadStatus(); err != nil || code != 439 || !strings.Contains(line, "invalid message-id") {
			t.Fatalf("TAKETHIS %s: code=%d line=%q err=%v, want 439 ... invalid message-id", id, code, line, err)
		}
		if n := len(proc.processedArticles()); n != before {
			t.Fatalf("%d articles reached the processor in strict mode, want none", n-before)
		}
	})

	t.Run("lax", func(t *testing.T) {
		common.SetLaxMessageID(true)
		t.Cleanup(func() { common.SetLaxMessageID(false) })
		lookup(t)
		invalid(t)

		c := srv.dial(t)
		ihave, stream := srvLaxmidID(), srvLaxmidID()
		c.ExpectCmd(238, "CHECK %s", ihave)
		c.ExpectCmd(335, "IHAVE %s", ihave)
		if err := c.Send(srvLaxmidWire(ihave, g)); err != nil {
			t.Fatal(err)
		}
		if code, line, err := c.ReadStatus(); err != nil || code != 235 {
			t.Fatalf("IHAVE %s article: code=%d line=%q err=%v, want 235", ihave, code, line, err)
		}
		if err := c.Send("TAKETHIS " + stream + "\r\n" + srvLaxmidWire(stream, g)); err != nil {
			t.Fatal(err)
		}
		if code, line, err := c.ReadStatus(); err != nil || code != 239 || !strings.HasPrefix(line, "239 "+stream) {
			t.Fatalf("TAKETHIS %s: code=%d line=%q err=%v, want 239 %s", stream, code, line, err, stream)
		}
		got := map[string]bool{}
		for _, a := range proc.processedArticles() {
			got[a.MessageID] = true
		}
		if !got[ihave] || !got[stream] {
			t.Fatalf("processor got %v, want %s and %s", got, ihave, stream)
		}
	})
}

// TestServerLaxmidPostStaysStrict (round 0 H1): POST checks a poster-supplied Message-ID with
// the RFC 5536 syntax also with -laxmid, so a poster cannot claim a gateway's guessable id
// before the real article arrives; a strict id is still accepted.
func TestServerLaxmidPostStaysStrict(t *testing.T) {
	g := hGroup(t, "lx2.laxpost", 0, nil)
	proc := iwPass()
	hs := iwPeerServer(t, proc, 0, 0)
	user, pass := hUser(t, true, true)
	common.SetLaxMessageID(true)
	t.Cleanup(func() { common.SetLaxMessageID(false) })
	poster := hs.dial(t)
	authMustLogin(t, poster, user, pass)

	lax := srvLaxmidID()
	code, line, _ := poster.Post(iwWire(iwHead(lax, g, "lax post"), []string{"body"}))
	if code != 441 || !strings.Contains(line, "invalid message-id") {
		t.Fatalf("lax: POST with Message-ID %s: %q, want 441 ... invalid message-id", lax, line)
	}
	for _, a := range proc.processedArticles() {
		if a.MessageID == lax {
			t.Fatalf("lax: POST with Message-ID %s reached the processor", lax)
		}
	}
	ok := iwID(g)
	if code, line, _ := poster.Post(iwWire(iwHead(ok, g, "strict post"), []string{"body"})); code != 240 {
		t.Fatalf("lax: POST with Message-ID %s: %q, want 240", ok, line)
	}
}
