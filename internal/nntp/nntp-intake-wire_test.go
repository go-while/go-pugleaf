package nntp

// Wire-level intake tests of the NNTP server (plan e2e-gap-tests, slice intake-wire):
//
//	G5  bare TAKETHIS (article drained, next pipelined command answered) and bare IHAVE
//	    (source: diablo-nntp tests/e2e/d91_takethis_test.go, replycodes_test.go)
//	G6  far too many header lines over IHAVE and TAKETHIS
//	    (source: diablo-nntp tests/e2e/maxheadersize_test.go)
//	G7  the size limits at exactly N and N+1 wire bytes (source: INN tests/innd/artparse-t.c)
//	G8  byte-at-a-time intake with the kit's SendDrip (source: INN artparse "slow"/"shift")
//	G10 unusual valid message-ids round trip (source: NewsCache TestHistoryUnusualMessageIDs)
//	G13 a re-offer of a hidden, deleted or expired article (source: INN tests/lib/hissqlite-t.c)
//
// Identifiers use the iw / TestIw prefix (contract K2). Where a decision of the plan
// (D1-D4) will change the behaviour, the assertion pins TODAY's behaviour and carries a
// "// D1" .. "// D4" comment, so wave 2 updates exactly those lines.
//
// Waits are hang guards only (hDeadline, PUGLEAF_TEST_DEADLINE): no wall-clock assertion.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/nntp-testkit/client"
)

// ---------------------------------------------------------------------------
// Helpers

// iwPass is a processor that accepts every offer and every article.
func iwPass() *hFakeProcessor {
	return &hFakeProcessor{checkResult: history.CasePass, processResult: history.CasePass}
}

// iwPeerServer returns a server with proc, one peer that may feed from 127.0.0.1 without a
// login and the Path identity peerFeedHost. maxArtSize is Config.NNTP.MaxArtSize (0: the
// default 1 MiB), inMaxSize the peer's in_max_size (0: none, so the server limit applies).
func iwPeerServer(t *testing.T, proc ArticleProcessor, maxArtSize int, inMaxSize int64) *hServer {
	t.Helper()
	p := hPeerSpec("127.0.0.1", "*")
	p.InMaxSize = inMaxSize
	return newTestServer(t, hServerOpts{Proc: proc, Peers: hPeers(p), Hostname: peerFeedHost, MaxArtSize: maxArtSize})
}

// iwID returns a message-id for group that no other probe uses (hSeq). Every offer gets a
// fresh one, so a refusal the server remembers for one probe (RememberReject) can never
// decide the answer to a later probe of the same test.
func iwID(group string) string {
	return hMessageID(group, hSeq.Add(1))
}

// iwHead returns the mandatory header lines (RFC 5536 §3) of a valid article.
func iwHead(id, group, subject string) []string {
	return []string{
		"Path: upstream.invalid!not-for-mail",
		"From: Tester <tester@test.invalid>",
		"Newsgroups: " + group,
		"Subject: " + subject,
		"Date: " + common.FormatNewsDate(time.Now()),
		"Message-ID: " + id,
	}
}

// iwWire returns head and body as a client writes them after a 340/335 or with TAKETHIS:
// CRLF line ends and the empty separator line, but no dot-stuffing and no terminator (what
// client.StuffBytes takes). Its length is exactly common.WireArticleSize(head, body), the
// octets readArticleBlock counts against the size limit (the terminator does not count).
func iwWire(head, body []string) []byte {
	var b strings.Builder
	for _, l := range head {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	for _, l := range body {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

// iwSized returns the wire bytes of a valid article for group whose wire size is exactly
// size octets, padding its single body line. It fails the test when size leaves no room.
func iwSized(t *testing.T, id, group string, size int) []byte {
	t.Helper()
	head := iwHead(id, group, "sized")
	fixed := len(iwWire(head, nil)) + 2 // headers, separator and the body line's CRLF
	if size < fixed {
		t.Fatalf("iwSized: size %d is below the %d octets of the header block", size, fixed)
	}
	raw := iwWire(head, []string{strings.Repeat("x", size-fixed)})
	if len(raw) != size {
		t.Fatalf("iwSized: built %d octets, want %d", len(raw), size)
	}
	if n := common.WireArticleSize(head, []string{strings.Repeat("x", size-fixed)}); n != size {
		t.Fatalf("iwSized: WireArticleSize %d, want %d", n, size)
	}
	return raw
}

// iwProcessedIDs returns the message-ids the processor was handed, in order.
func iwProcessedIDs(p *hFakeProcessor) []string {
	arts := p.processedArticles()
	out := make([]string, 0, len(arts))
	for _, a := range arts {
		out = append(out, a.MessageID)
	}
	return out
}

// iwWantProcessed fails when the processor was handed other articles than want.
func iwWantProcessed(t *testing.T, p *hFakeProcessor, want ...string) {
	t.Helper()
	got := iwProcessedIDs(p)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("processed %q, want %q", got, want)
	}
}

// iwCarriesID reports whether the reply's second word is id (RFC 4644 §2.4.2, §2.5.2).
func iwCarriesID(line, id string) bool {
	f := strings.Fields(line)
	return len(f) >= 2 && f[1] == id
}

// ---------------------------------------------------------------------------
// G5: bare TAKETHIS and bare IHAVE

// TestIwBareTakethis: RFC 4644 §2.5, a TAKETHIS without its message-id argument is a
// syntax error (501), but the article the peer streamed behind it must still be read and
// discarded, so the next pipelined TAKETHIS is answered normally (239) and not taken for
// article data. Source: diablo-nntp d91_takethis_test.go, replycodes_test.go.
func TestIwBareTakethis(t *testing.T) {
	g := hGroup(t, "t10.bare", 0, nil)
	proc := iwPass()
	hs := iwPeerServer(t, proc, 0, 0)
	c := hs.dial(t)

	idDrained, idNext := iwID(g), iwID(g)
	drained := client.StuffBytes(iwWire(iwHead(idDrained, g, "bare takethis"), []string{"body"}))
	next := client.StuffBytes(iwWire(iwHead(idNext, g, "after the bare takethis"), []string{"body"}))

	replies := c.Pipeline([]string{
		"TAKETHIS\r\n" + string(drained),
		"TAKETHIS " + idNext + "\r\n" + string(next),
	}, nil)
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2", len(replies))
	}
	if err := client.ExpectCode(replies[0].Code, replies[0].Line, 501); err != nil {
		t.Errorf("bare TAKETHIS: %v", err)
	}
	if err := client.ExpectCode(replies[1].Code, replies[1].Line, 239); err != nil {
		t.Errorf("TAKETHIS after the bare one: %v", err)
	} else if !iwCarriesID(replies[1].Line, idNext) {
		t.Errorf("TAKETHIS reply %q does not carry %s", replies[1].Line, idNext)
	}
	// Only the second article reached the processor: the drained one is never kept.
	iwWantProcessed(t, proc, idNext)

	// The session is still usable after both.
	code, line := c.Cmd("MODE STREAM")
	if err := client.ExpectCode(code, line, 203); err != nil {
		t.Errorf("MODE STREAM after the bare TAKETHIS: %v", err)
	}
}

// TestIwBareIhave: RFC 3977 §6.3.2, IHAVE without a message-id and with surplus arguments
// is a syntax error (501) and reads no article, so the session stays in sync and the next
// IHAVE transfers normally. Source: diablo-nntp replycodes_test.go.
func TestIwBareIhave(t *testing.T) {
	g := hGroup(t, "t10.bareihave", 0, nil)
	proc := iwPass()
	hs := iwPeerServer(t, proc, 0, 0)
	c := hs.dial(t)

	for _, cmd := range []string{"IHAVE", "IHAVE " + iwID(g) + " " + iwID(g)} {
		code, line := c.Cmd("%s", cmd)
		if err := client.ExpectCode(code, line, 501); err != nil {
			t.Errorf("%q: %v", cmd, err)
		}
	}
	id := iwID(g)
	code, line, sent := c.Ihave(id, iwWire(iwHead(id, g, "after the bare ihave"), []string{"body"}))
	if err := client.ExpectCode(code, line, 235); err != nil || !sent {
		t.Fatalf("IHAVE %s after the syntax errors: %v (sent=%v, %q)", id, err, sent, line)
	}
	iwWantProcessed(t, proc, id)
}

// ---------------------------------------------------------------------------
// G6: far too many header lines

// TestIwManyHeaderLines: an article with far more header lines than readArticleBlock's
// 1024-line cap is read to its terminator and refused (437 for IHAVE, 439 for TAKETHIS,
// RFC 3977 §6.3.2.2 / RFC 4644 §2.5.2), the id is remembered as rejected, and the next
// article on the same connection transfers normally. Source: diablo maxheadersize_test.go.
func TestIwManyHeaderLines(t *testing.T) {
	const nHead = 2000 // over the 1024 head-line cap; ~34 KB, far below the 1 MiB default
	g := hGroup(t, "t10.manyhead", 0, nil)

	for _, tc := range []struct {
		name       string
		wantBig    int
		wantNormal int
	}{
		{"ihave", 437, 235},
		{"takethis", 439, 239},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := iwPass()
			hs := iwPeerServer(t, proc, 0, 0)
			c := hs.dial(t)
			idBig, idNormal := iwID(g), iwID(g)

			head := iwHead(idBig, g, "many header lines")
			for i := 0; i < nHead; i++ {
				head = append(head, fmt.Sprintf("X-Pad-%04d: y", i))
			}
			big := iwWire(head, []string{"body"})
			normal := iwWire(iwHead(idNormal, g, "after the big head"), []string{"body"})

			var code int
			var line string
			if tc.name == "ihave" {
				code, line, _ = c.Ihave(idBig, big)
			} else {
				code, line, _ = c.Takethis(idBig, big)
			}
			if err := client.ExpectCode(code, line, tc.wantBig); err != nil {
				t.Fatalf("%s of an article with %d header lines: %v", tc.name, nHead, err)
			}
			if tc.name == "takethis" && !iwCarriesID(line, idBig) {
				t.Errorf("TAKETHIS reply %q does not carry %s", line, idBig)
			}
			// A permanent refusal is remembered, so the peer stops offering it.
			if got := proc.rejectedIDs(); len(got) != 1 || got[0] != idBig {
				t.Errorf("RememberReject calls %q, want [%s]", got, idBig)
			}

			if tc.name == "ihave" {
				code, line, _ = c.Ihave(idNormal, normal)
			} else {
				code, line, _ = c.Takethis(idNormal, normal)
			}
			if err := client.ExpectCode(code, line, tc.wantNormal); err != nil {
				t.Fatalf("%s of the next article: %v", tc.name, err)
			}
			iwWantProcessed(t, proc, idNormal)
		})
	}
}

// ---------------------------------------------------------------------------
// G7: the size limits at exactly N and N+1 wire bytes

// iwLimitProbe offers one article of exactly size octets by cmd and returns the reply.
func iwLimitProbe(t *testing.T, c *client.Conn, cmd, id, group string, size int) (int, string) {
	t.Helper()
	raw := iwSized(t, id, group, size)
	switch cmd {
	case "POST":
		code, line, _ := c.Post(raw)
		return code, line
	case "IHAVE":
		code, line, _ := c.Ihave(id, raw)
		return code, line
	case "TAKETHIS":
		code, line, _ := c.Takethis(id, raw)
		return code, line
	}
	t.Fatalf("iwLimitProbe: unknown command %q", cmd)
	return 0, ""
}

// TestIwServerSizeLimitExact: Config.NNTP.MaxArtSize is an inclusive limit on the wire
// octets of the article (every line plus CRLF and the empty separator line; the
// terminating ".\r\n" does not count): exactly N passes, N+1 is refused with 441 / 437 /
// 439 (SEC-3, contract K4). Source: INN artparse-t.c case 6 (size at the boundary).
func TestIwServerSizeLimitExact(t *testing.T) {
	const limit = 1024
	g := hGroup(t, "t10.sizeexact", 0, nil)

	t.Run("post", func(t *testing.T) {
		proc := iwPass()
		hs := newTestServer(t, hServerOpts{Proc: proc, Hostname: peerFeedHost, MaxArtSize: limit})
		user, pass := hUser(t, true, true)
		c := hs.dial(t)
		authMustLogin(t, c, user, pass)

		idOK, idOver := iwID(g), iwID(g)
		code, line := iwLimitProbe(t, c, "POST", idOK, g, limit)
		if err := client.ExpectCode(code, line, 240); err != nil {
			t.Errorf("POST of exactly %d octets: %v", limit, err)
		}
		code, line = iwLimitProbe(t, c, "POST", idOver, g, limit+1)
		if err := client.ExpectCode(code, line, 441); err != nil {
			t.Errorf("POST of %d octets: %v", limit+1, err)
		} else if !strings.Contains(line, "too large") {
			t.Errorf("POST of %d octets: %q, want a 441 about the size", limit+1, line)
		}
		iwWantProcessed(t, proc, idOK)
	})

	for _, cmd := range []struct {
		name     string
		ok, over int
	}{
		{"IHAVE", 235, 437},
		{"TAKETHIS", 239, 439},
	} {
		t.Run(strings.ToLower(cmd.name), func(t *testing.T) {
			proc := iwPass()
			hs := iwPeerServer(t, proc, limit, 0)
			c := hs.dial(t)
			idOK, idOver := iwID(g), iwID(g)
			code, line := iwLimitProbe(t, c, cmd.name, idOK, g, limit)
			if err := client.ExpectCode(code, line, cmd.ok); err != nil {
				t.Errorf("%s of exactly %d octets: %v", cmd.name, limit, err)
			}
			code, line = iwLimitProbe(t, c, cmd.name, idOver, g, limit+1)
			if err := client.ExpectCode(code, line, cmd.over); err != nil {
				t.Errorf("%s of %d octets: %v", cmd.name, limit+1, err)
			} else if !strings.Contains(line, "too large") {
				t.Errorf("%s of %d octets: %q, want a reply about the size", cmd.name, limit+1, line)
			}
			iwWantProcessed(t, proc, idOK)
		})
	}
}

// TestIwPeerSizeLimitExact: a peer's in_max_size is the same inclusive wire-octet limit and
// replaces the (here much larger) server limit for IHAVE and TAKETHIS.
func TestIwPeerSizeLimitExact(t *testing.T) {
	const peerLimit = 900
	g := hGroup(t, "t10.peersizeexact", 0, nil)

	for _, cmd := range []struct {
		name     string
		ok, over int
	}{
		{"IHAVE", 235, 437},
		{"TAKETHIS", 239, 439},
	} {
		t.Run(strings.ToLower(cmd.name), func(t *testing.T) {
			proc := iwPass()
			hs := iwPeerServer(t, proc, 8192, peerLimit)
			c := hs.dial(t)
			idOK, idOver := iwID(g), iwID(g)
			code, line := iwLimitProbe(t, c, cmd.name, idOK, g, peerLimit)
			if err := client.ExpectCode(code, line, cmd.ok); err != nil {
				t.Errorf("%s of exactly in_max_size (%d) octets: %v", cmd.name, peerLimit, err)
			}
			code, line = iwLimitProbe(t, c, cmd.name, idOver, g, peerLimit+1)
			if err := client.ExpectCode(code, line, cmd.over); err != nil {
				t.Errorf("%s of in_max_size+1 (%d) octets: %v", cmd.name, peerLimit+1, err)
			}
			iwWantProcessed(t, proc, idOK)
		})
	}
}

// TestIwSizeLimitDotStuffing (W2-size, wave 2): the server limit measures the article as it
// is stored and fed on, so the stuffing dots readArticleBlock strips do NOT count. An
// article whose un-stuffed size is exactly the limit is accepted although one "." body line
// makes N+1 octets cross the wire, and the same article one octet larger is refused. This is
// the count a newsgroup's max_art_size and a peer's in_max_size use as well
// (common.WireArticleSize, INN's Bytes).
func TestIwSizeLimitDotStuffing(t *testing.T) {
	const limit = 1024
	g := hGroup(t, "t10.sizestuff", 0, nil)
	proc := iwPass()
	hs := iwPeerServer(t, proc, limit, 0)
	c := hs.dial(t)

	// stuffed builds a valid article for group g of exactly size un-stuffed octets with one
	// "." body line (one stuffing dot on the wire).
	stuffed := func(id string, size int) []byte {
		head := iwHead(id, g, "dot stuffed")
		fixed := len(iwWire(head, nil)) + 2 + 3 // headers, separator, the "." line, the pad line's CRLF
		body := []string{strings.Repeat("x", size-fixed), "."}
		raw := iwWire(head, body)
		if len(raw) != size {
			t.Fatalf("built %d un-stuffed octets, want exactly %d", len(raw), size)
		}
		if len(client.StuffBytes(raw)) != size+1+3 { // one stuffing dot plus the ".\r\n" terminator
			t.Fatalf("stuffed form has %d octets, want %d", len(client.StuffBytes(raw)), size+4)
		}
		return raw
	}

	idOK, idOver := iwID(g), iwID(g)
	code, line, _ := c.Ihave(idOK, stuffed(idOK, limit))
	if err := client.ExpectCode(code, line, 235); err != nil {
		t.Errorf("IHAVE of exactly %d un-stuffed octets with one dot-stuffed line: %v", limit, err)
	}
	code, line, _ = c.Ihave(idOver, stuffed(idOver, limit+1))
	if err := client.ExpectCode(code, line, 437); err != nil {
		t.Errorf("IHAVE of %d un-stuffed octets with one dot-stuffed line: %v", limit+1, err)
	}
	iwWantProcessed(t, proc, idOK)
}

// iwOfferedPath is the Path value of every article these tests offer.
const iwOfferedPath = "upstream.invalid!not-for-mail"

// TestIwProcessorWireSize (W2-size, wave 2): the lines the server hands to the processor are
// the lines that arrived (dot-stuffing undone), with one change: feedPath prepended our Path
// identity, so common.WireArticleSize of the handed-over block is larger than what the peer
// transferred. The honest size travels in models.Article.IntakeWireSize instead, taken
// before feedPath, and that is the number a newsgroup's max_art_size is compared against
// (internal/processor/intake.go resolveIntakeGroups).
func TestIwProcessorWireSize(t *testing.T) {
	const size = 700
	g := hGroup(t, "t10.procwiresize", 0, nil)

	for _, cmd := range []string{"IHAVE", "TAKETHIS"} {
		t.Run(strings.ToLower(cmd), func(t *testing.T) {
			proc := iwPass()
			hs := iwPeerServer(t, proc, 0, 0)
			c := hs.dial(t)
			for _, n := range []int{size, size + 1} {
				id := iwID(g)
				code, line := iwLimitProbe(t, c, cmd, id, g, n)
				if code != 235 && code != 239 {
					t.Fatalf("%s of %d octets: %q", cmd, n, line)
				}
				arts := proc.processedArticles()
				a := arts[len(arts)-1]
				grew := len(common.HeaderValue(a.NNTPhead, "Path")) - len(iwOfferedPath)
				if grew <= 0 {
					t.Fatalf("%s: Path %q was not extended", cmd, common.HeaderValue(a.NNTPhead, "Path"))
				}
				if got := common.WireArticleSize(a.NNTPhead, a.NNTPbody); got != n+grew {
					t.Errorf("%s of %d octets: the processor sees WireArticleSize %d, want %d (%d + %d Path octets)",
						cmd, n, got, n+grew, n, grew)
				}
				if a.IntakeWireSize != n {
					t.Errorf("%s of %d octets: IntakeWireSize = %d, want %d (the transferred size, before feedPath)",
						cmd, n, a.IntakeWireSize, n)
				}
				if a.IntakeHeaderCheck != "relay" {
					t.Errorf("%s: IntakeHeaderCheck = %q, want the peer's header_check %q", cmd, a.IntakeHeaderCheck, "relay")
				}
			}
		})
	}
}

// TestIwPeerHeaderCheckCarried (D1/D4, wave 2): the header_check of the peer that fed the
// article reaches the processor on the article (setIntakePolicy), for IHAVE and TAKETHIS, and
// a POST on the same connection is marked Posted (always strict). The rules themselves are
// tested against the real intake path in internal/processor (intake_policy_test.go): the
// fake processor of this harness never runs validateIntakeHeaders.
func TestIwPeerHeaderCheckCarried(t *testing.T) {
	g := hGroup(t, "t10.headercheck", 0, nil)
	for _, mode := range []string{database.PeerHeaderCheckRelay, database.PeerHeaderCheckStrict} {
		t.Run(mode, func(t *testing.T) {
			proc := iwPass()
			p := hPeerSpec("127.0.0.1", "*")
			p.HeaderCheck = mode
			hs := newTestServer(t, hServerOpts{Proc: proc, Peers: hPeers(p), Hostname: peerFeedHost})
			c := hs.dial(t)
			for _, cmd := range []string{"IHAVE", "TAKETHIS"} {
				id := iwID(g)
				raw := iwWire(iwHead(id, g, "header check"), []string{"body"})
				var code int
				var line string
				if cmd == "IHAVE" {
					code, line, _ = c.Ihave(id, raw)
				} else {
					code, line, _ = c.Takethis(id, raw)
				}
				if code != 235 && code != 239 {
					t.Fatalf("%s %s: %q", cmd, id, line)
				}
				arts := proc.processedArticles()
				a := arts[len(arts)-1]
				if a.IntakeHeaderCheck != mode {
					t.Errorf("%s: IntakeHeaderCheck = %q, want the peer's %q", cmd, a.IntakeHeaderCheck, mode)
				}
				if a.Posted {
					t.Errorf("%s: a fed article must not be marked Posted", cmd)
				}
			}
			// POST on the same (peer) connection: Posted, so the processor checks strictly
			user, pass := hUser(t, true, true)
			poster := hs.dial(t)
			authMustLogin(t, poster, user, pass)
			id := iwID(g)
			code, line, _ := poster.Post(iwWire(iwHead(id, g, "posted"), []string{"body"}))
			if err := client.ExpectCode(code, line, 240); err != nil {
				t.Fatalf("POST %s: %v", id, err)
			}
			arts := proc.processedArticles()
			if a := arts[len(arts)-1]; !a.Posted {
				t.Errorf("the POSTed article is not marked Posted (IntakeHeaderCheck %q)", a.IntakeHeaderCheck)
			}
		})
	}
}

// TestIwPostWireSize (W2-size, wave 2): a POST carries the size of what the poster sent, not
// of the proto-article after injectProtoArticle added Message-ID/Date/Injection-* and the
// Path identity, so a newsgroup's max_art_size counts no header this server injected.
func TestIwPostWireSize(t *testing.T) {
	const size = 700
	g := hGroup(t, "t10.postwiresize", 0, nil)
	proc := iwPass()
	hs := iwPeerServer(t, proc, 0, 0)
	user, pass := hUser(t, true, true)
	c := hs.dial(t)
	authMustLogin(t, c, user, pass)

	id := iwID(g)
	code, line := iwLimitProbe(t, c, "POST", id, g, size)
	if err := client.ExpectCode(code, line, 240); err != nil {
		t.Fatalf("POST of %d octets: %v", size, err)
	}
	arts := proc.processedArticles()
	a := arts[len(arts)-1]
	if a.IntakeWireSize != size {
		t.Errorf("IntakeWireSize = %d, want %d (what the poster sent)", a.IntakeWireSize, size)
	}
	if got := common.WireArticleSize(a.NNTPhead, a.NNTPbody); got <= size {
		t.Errorf("the injected block has WireArticleSize %d, want more than the %d transferred octets", got, size)
	}
}

// ---------------------------------------------------------------------------
// G8: byte-at-a-time intake

// iwDripPause paces SendDrip so the writes do not coalesce into one segment. It is not an
// assertion: the test only cares that every octet arrives in its own write.
const iwDripPause = 100 * time.Microsecond

// iwDotHead is a header field whose wire form starts with "..": it tests that
// readArticleBlock undoes the stuffing of a header line, also when the two dots arrive in
// separate writes.
const iwDotHead = ".X-Dot: dripped"

// TestIwDripIhave: an IHAVE article written one octet per write (the kit's SendDrip, INN's
// artparse "slow"/"shift" modes) is read correctly: the terminator and a dot-stuffed header
// line are split across writes, and the article the processor gets holds the un-stuffed
// lines.
func TestIwDripIhave(t *testing.T) {
	g := hGroup(t, "t10.dripihave", 0, nil)
	proc := iwPass()
	hs := iwPeerServer(t, proc, 0, 0)
	c := hs.dial(t)

	id := iwID(g)
	head := append(iwHead(id, g, "dripped"), iwDotHead)
	body := []string{"first", ".", "last"}
	code, line := c.Cmd("IHAVE %s", id)
	if err := client.ExpectCode(code, line, 335); err != nil {
		t.Fatalf("IHAVE %s: %v", id, err)
	}
	if err := c.SendDrip(client.StuffBytes(iwWire(head, body)), iwDripPause); err != nil {
		t.Fatalf("drip the article: %v", err)
	}
	code, line, err := c.ReadStatus()
	if err != nil {
		t.Fatalf("IHAVE status: %v (%q)", err, line)
	}
	if err := client.ExpectCode(code, line, 235); err != nil {
		t.Fatalf("dripped IHAVE: %v", err)
	}
	arts := proc.processedArticles()
	if len(arts) != 1 {
		t.Fatalf("processed %d articles, want 1", len(arts))
	}
	// Path is the one line the server rewrites (feedPath); everything else, the dot-stuffed
	// header line included, must arrive exactly as it was written.
	if got, want := arts[0].NNTPhead[1:], head[1:]; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("head after the drip:\n%q\nwant\n%q", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p := common.HeaderValue(arts[0].NNTPhead, "Path"); !strings.HasSuffix(p, iwOfferedPath) {
		t.Errorf("Path after the drip: %q, want it to end in %q", p, iwOfferedPath)
	}
	if got := strings.Join(arts[0].NNTPbody, "\n"); got != strings.Join(body, "\n") {
		t.Errorf("body after the drip: %q, want %q", got, strings.Join(body, "\n"))
	}
}

// TestIwDripPipelined: a pipelined CHECK and TAKETHIS written one octet per write are
// answered 238 and 239, in order and each carrying its message-id (RFC 4644 §2.1).
func TestIwDripPipelined(t *testing.T) {
	g := hGroup(t, "t10.drippipe", 0, nil)
	proc := iwPass()
	hs := iwPeerServer(t, proc, 0, 0)
	c := hs.dial(t)

	id := iwID(g)
	head := append(iwHead(id, g, "dripped pipeline"), iwDotHead)
	raw := "CHECK " + id + "\r\nTAKETHIS " + id + "\r\n" + string(client.StuffBytes(iwWire(head, []string{"."})))
	if err := c.SendDrip([]byte(raw), iwDripPause); err != nil {
		t.Fatalf("drip CHECK and TAKETHIS: %v", err)
	}
	for _, want := range []int{238, 239} {
		code, line, err := c.ReadStatus()
		if err != nil {
			t.Fatalf("reply %d: %v (%q)", want, err, line)
		}
		if err := client.ExpectCode(code, line, want); err != nil {
			t.Fatalf("dripped pipeline: %v", err)
		}
		if !iwCarriesID(line, id) {
			t.Errorf("reply %q does not carry %s", line, id)
		}
	}
	iwWantProcessed(t, proc, id)
}

// ---------------------------------------------------------------------------
// G10: unusual valid message-ids

// iwUnusualIDs are message-ids real articles carry that are unusual but valid under
// RFC 5536 §3.1.3 / RFC 5322 §3.6.4 (source: NewsCache TestHistoryUnusualMessageIDs):
// a quoted local part, specials, a domain literal, mixed case and one of exactly 250
// octets. tag goes into every id, so two calls give two disjoint sets (the POST leg must
// not offer an id the IHAVE leg already stored).
func iwUnusualIDs(tag string) []string {
	domain := tag + ".iw.test.invalid"
	long := "<" + strings.Repeat("x", 250-3-len(domain)) + "@" + domain + ">" // exactly 250 octets
	return []string{
		`<"quoted.local"@` + domain + `>`,
		`<a/b=c?d{e}%f!g@` + domain + `>`,
		`<` + tag + `@[127.0.0.1]>`,
		`<UPPER.lower@` + strings.ToUpper(tag) + `.IW.Test.INVALID>`,
		long,
	}
}

// iwStoreArticle inserts the article a processor was handed into group's database under
// article number num, with the columns the harness also writes (hArticleSQL). It runs on
// the server goroutine, so it never calls t.Fatal.
func iwStoreArticle(db *database.Database, group string, num int64, a *models.Article) error {
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		return err
	}
	defer gdb.Return()
	_, err = database.RetryableExec(gdb.DB,
		`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references",
		                       bytes, lines, reply_count, path, headers_json, body_text, downloaded, imported_at)
		 VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, 0, ?, ?, ?, 1, CURRENT_TIMESTAMP)`,
		num, a.MessageID, a.Subject, a.FromHeader, a.DateSent.UTC().Format("2006-01-02 15:04:05"), a.DateString,
		common.WireArticleSize(a.NNTPhead, a.NNTPbody), len(a.NNTPbody), a.Path,
		strings.Join(a.NNTPhead, "\n"), strings.Join(a.NNTPbody, "\n"))
	return err
}

// TestIwUnusualMessageIDs: an article whose Message-ID is unusual but valid is accepted by
// IHAVE and by POST (both check it with common.ValidMessageID), stored under that id and
// found again by "ARTICLE <id>" / "STAT <id>", in the selected group and through the
// processor lookup. Source: NewsCache TestHistoryUnusualMessageIDs.
func TestIwUnusualMessageIDs(t *testing.T) {
	db := hTestDB(t)
	g := hGroup(t, "t10.unusualids", 0, nil)
	gOther := hGroup(t, "t10.unusualids-other", 1, nil)

	var num atomic.Int64
	proc := iwPass()
	proc.processFn = func(a *models.Article) (int, error) {
		if err := iwStoreArticle(db, g, num.Add(1), a); err != nil {
			return history.CaseError, err
		}
		return history.CasePass, nil
	}
	proc.findFn = func(messageID, currentGroup string) (*models.Article, error) {
		gdb, err := db.GetGroupDB(g)
		if err != nil {
			return nil, err
		}
		defer gdb.Return()
		a, err := db.GetArticleByMessageID(gdb, messageID)
		if err != nil {
			return nil, fmt.Errorf("iw %s: %w", messageID, ErrArticleNotFound)
		}
		return a, nil
	}
	hs := iwPeerServer(t, proc, 0, 0)
	hs.S.SetXrefMode(XrefOff) // stored-header passthrough (Xref: lines: nntp-xref_test.go)
	feed := hs.dial(t)

	ids := iwUnusualIDs("ihave")
	for _, id := range ids {
		code, line, sent := feed.Ihave(id, iwWire(iwHead(id, g, "unusual id"), []string{"body"}))
		if err := client.ExpectCode(code, line, 235); err != nil || !sent {
			t.Fatalf("IHAVE %s (%d octets): %v (sent=%v, %q)", id, len(id), err, sent, line)
		}
	}

	// The POST leg: the same ids, offered by a posting user (injectProtoArticle runs the
	// same common.ValidMessageID check on the Message-ID header). The connection is a peer
	// connection as well, which does not take the POST right away: POST needs the login.
	postIDs := iwUnusualIDs("post")
	user, pass := hUser(t, true, true)
	poster := hs.dial(t)
	authMustLogin(t, poster, user, pass)
	for _, id := range postIDs {
		code, line, sent := poster.Post(iwWire(iwHead(id, g, "unusual id"), []string{"body"}))
		if err := client.ExpectCode(code, line, 240); err != nil || !sent {
			t.Fatalf("POST %s (%d octets): %v (sent=%v, %q)", id, len(id), err, sent, line)
		}
	}
	ids = append(ids, postIDs...)

	// (a) the id is found in the selected group (RFC 3977 §6.2.1.3 step 1)
	t.Run("in-group", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.Cmd("GROUP %s", g)
		if err := client.ExpectCode(code, line, 211); err != nil {
			t.Fatalf("GROUP %s: %v", g, err)
		}
		for _, id := range ids {
			code, line := c.Cmd("STAT %s", id)
			if err := client.ExpectCode(code, line, 223); err != nil {
				t.Errorf("STAT %s: %v", id, err)
				continue
			}
			if f := strings.Fields(line); len(f) < 3 || f[2] != id {
				t.Errorf("STAT %s: reply %q does not carry the id", id, line)
			}
			if code, line := c.Cmd("ARTICLE %s", id); code != 220 {
				t.Errorf("ARTICLE %s: %q, want 220", id, line)
			} else {
				c.ReadDotLines()
			}
		}
	})

	// (b) another group is selected: the processor lookup finds it, number 0 (§6.2.1.2)
	t.Run("other-group", func(t *testing.T) {
		c := hs.dial(t)
		code, line := c.Cmd("GROUP %s", gOther)
		if err := client.ExpectCode(code, line, 211); err != nil {
			t.Fatalf("GROUP %s: %v", gOther, err)
		}
		for _, id := range ids {
			code, line := c.Cmd("STAT %s", id)
			if err := client.ExpectCode(code, line, 223); err != nil {
				t.Errorf("STAT %s through the processor: %v", id, err)
				continue
			}
			if f := strings.Fields(line); len(f) < 3 || f[1] != "0" || f[2] != id {
				t.Errorf("STAT %s: reply %q, want \"223 0 %s\"", id, line, id)
			}
		}
	})

	// D3 (wave 2): "<" inside a no-fold-literal id-right (RFC 5536 mdtext) is valid, so
	// such an id is offered like any other: IHAVE answers 335 and takes the article, CHECK
	// answers 238. Each command gets its own connection, so the article the IHAVE leg sends
	// cannot be read as a command by the CHECK leg.
	t.Run("angle-in-domain-literal", func(t *testing.T) {
		const id = `<x@[a<b]>`
		// Ihave sends the article after the 335 and returns the final reply; sent false
		// means the offer itself was refused (today's 501).
		code, line, sent := hs.dial(t).Ihave(id, iwWire(iwHead(id, g, "angle in literal"), []string{"body"}))
		if !sent {
			t.Fatalf("IHAVE %s: %q, want 335 and the article taken", id, line) // D3
		}
		if err := client.ExpectCode(code, line, 235); err != nil { // D3
			t.Errorf("IHAVE %s article: %v", id, err)
		}
		code, line = hs.dial(t).Cmd("CHECK %s", id)
		if err := client.ExpectCode(code, line, 238); err != nil { // D3
			t.Errorf("CHECK %s: %v", id, err)
		}
	})
}

// ---------------------------------------------------------------------------
// G13: a re-offer of an article that is gone

// iwHistory returns an enabled history index in a temp dir, closed at cleanup.
//
// history.ENABLE_HISTORY is a package global that NewHistory reads once (history.go:189),
// so it has to be true across that call and is restored afterwards. Writing another
// package's global from here is only safe because: no test of internal/nntp calls
// t.Parallel, so no other test runs while this one does; nothing outside internal/history
// reads ENABLE_HISTORY (the running instance uses its own copy, h.enabled); and this is
// the only test in the package that builds a history index. A second history-using test,
// or a t.Parallel anywhere in this package, must move the flip into TestMain instead.
func iwHistory(t *testing.T) *history.History {
	t.Helper()
	old := history.ENABLE_HISTORY
	history.ENABLE_HISTORY = true
	t.Cleanup(func() { history.ENABLE_HISTORY = old })

	cfg := history.DefaultConfig()
	cfg.HistoryDir = filepath.Join(t.TempDir(), "history")
	cfg.BatchTimeout = 50
	cfg.MaxConnections = 4
	var wg sync.WaitGroup
	h, err := history.NewHistory(cfg, &wg)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() {
		h.Close()
		wg.Wait()
	})
	return h
}

// iwWaitExists polls the history index until Exists(id) reports want; hDeadline is the hang
// guard of the asynchronous writer, not a timing assertion.
func iwWaitExists(t *testing.T, h *history.History, id string, want bool) {
	t.Helper()
	deadline := time.Now().Add(hDeadline)
	for {
		got, err := h.Exists(id)
		if err != nil {
			t.Fatalf("history Exists(%s): %v", id, err)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("history Exists(%s) is still %v after %s", id, got, hDeadline)
		}
		time.Sleep(time.Millisecond)
	}
}

// iwHistProc answers the history questions from a real history index, as
// Processor.CheckMessageID and Processor.FindArticleByMessageID do
// (internal/processor/processor.go); everything else is the harness fake.
type iwHistProc struct {
	*hFakeProcessor
	h  *history.History
	db *database.Database
}

// CheckMessageID: an id the history index holds is a duplicate, an unknown one passes.
func (p *iwHistProc) CheckMessageID(messageID string) int {
	p.hFakeProcessor.CheckMessageID(messageID) // record the call
	exists, err := p.h.Exists(messageID)
	if err != nil {
		return history.CaseError
	}
	if exists {
		return history.CaseDupes
	}
	return history.CasePass
}

// FindArticleByMessageID looks the id up in the groups the history index names, as
// Processor.findArticleInGroup does (hidden copies are returned: the server filters them).
func (p *iwHistProc) FindArticleByMessageID(messageID, currentGroup string) (*models.Article, error) {
	groupIDs, err := p.h.LookupGroups(messageID)
	if err != nil {
		return nil, fmt.Errorf("history lookup for %q failed: %w", messageID, err)
	}
	for _, id := range groupIDs {
		ng, err := p.db.MainDBGetNewsgroupByID(id)
		if err != nil || ng == nil || ng.Name == "" || ng.Name == currentGroup {
			continue // a group the main DB does not know any more: never opened (SEC-2)
		}
		gdb, err := p.db.GetGroupDB(ng.Name)
		if err != nil {
			continue
		}
		a, aerr := p.db.GetArticleByMessageID(gdb, messageID)
		gdb.Return()
		if aerr == nil && a != nil {
			return a, nil
		}
	}
	return nil, fmt.Errorf("message-id %q: %w", messageID, ErrArticleNotFound)
}

// iwGroupID returns the newsgroups row id of group.
func iwGroupID(t *testing.T, group string) int64 {
	t.Helper()
	ng, err := hTestDB(t).MainDBGetNewsgroup(group)
	if err != nil {
		t.Fatalf("MainDBGetNewsgroup %s: %v", group, err)
	}
	return ng.ID
}

// TestIwReofferGoneArticle: an article that is hidden, or whose group the server does not
// carry any more, keeps its history row, so a peer that offers it again is refused with
// 435 (IHAVE) / 438 (CHECK) and the article is not taken a second time; a reader gets 430.
// Expiry is different on purpose: cmd/expire-news removes the history token, which deletes
// the row once it is empty, so that id is wanted again. Source: INN lib/hissqlite-t.c.
func TestIwReofferGoneArticle(t *testing.T) {
	db := hTestDB(t)
	h := iwHistory(t)

	newProc := func() *iwHistProc {
		return &iwHistProc{hFakeProcessor: iwPass(), h: h, db: db}
	}

	// offer re-offers id on a fresh connection and returns the CHECK and IHAVE codes.
	offer := func(t *testing.T, hs *hServer, id, group string) (check, ihave int) {
		t.Helper()
		c := hs.dial(t)
		check, line := c.Cmd("CHECK %s", id)
		if !iwCarriesID(line, id) {
			t.Errorf("CHECK reply %q does not carry %s", line, id)
		}
		ihave, _, _ = c.Ihave(id, iwWire(iwHead(id, group, "re-offer"), []string{"body"}))
		return check, ihave
	}

	t.Run("hidden", func(t *testing.T) {
		g := hGroup(t, "t10.reoffer-hidden", 1, nil)
		id := hMessageID(g, 1)
		h.AddArticle(id, iwGroupID(t, g))
		iwWaitExists(t, h, id, true)
		artHide(t, g, 1)

		proc := newProc()
		hs := iwPeerServer(t, proc, 0, 0)
		if check, ihave := offer(t, hs, id, g); check != 438 || ihave != 435 {
			t.Errorf("re-offer of a hidden article: CHECK %d, IHAVE %d, want 438 and 435", check, ihave)
		}
		iwWantProcessed(t, proc.hFakeProcessor)

		// A reader does not see it either (RFC 3977 §6.2.4.2: 430).
		c := hs.dial(t)
		if code, line := c.Cmd("STAT %s", id); code != 430 {
			t.Errorf("STAT %s of a hidden article: %q, want 430", id, line)
		}
	})

	t.Run("group-deleted", func(t *testing.T) {
		g := hGroup(t, "t10.reoffer-deleted", 1, nil)
		id := hMessageID(g, 1)
		h.AddArticle(id, iwGroupID(t, g))
		iwWaitExists(t, h, id, true)
		if _, err := database.RetryableExec(db.GetMainDB(), `DELETE FROM newsgroups WHERE name = ?`, g); err != nil {
			t.Fatalf("delete newsgroup %s: %v", g, err)
		}

		proc := newProc()
		hs := iwPeerServer(t, proc, 0, 0)
		if check, ihave := offer(t, hs, id, g); check != 438 || ihave != 435 {
			t.Errorf("re-offer of an article whose group is gone: CHECK %d, IHAVE %d, want 438 and 435", check, ihave)
		}
		iwWantProcessed(t, proc.hFakeProcessor)

		c := hs.dial(t)
		if code, line := c.Cmd("STAT %s", id); code != 430 {
			t.Errorf("STAT %s after the group was deleted: %q, want 430", id, line)
		}
	})

	// Deliberate difference: an expired article is wanted again (the history row is gone).
	t.Run("expired", func(t *testing.T) {
		g := hGroup(t, "t10.reoffer-expired", 1, nil)
		id := hMessageID(g, 1)
		groupID := iwGroupID(t, g)
		h.AddArticle(id, groupID)
		iwWaitExists(t, h, id, true)
		h.RemoveArticleAt(id, groupID, 1) // what cmd/expire-news does when it trims
		iwWaitExists(t, h, id, false)

		proc := newProc()
		hs := iwPeerServer(t, proc, 0, 0)
		if check, ihave := offer(t, hs, id, g); check != 238 || ihave != 235 {
			t.Errorf("re-offer of an expired article: CHECK %d, IHAVE %d, want 238 and 235", check, ihave)
		}
		iwWantProcessed(t, proc.hFakeProcessor, id)
	})
}
