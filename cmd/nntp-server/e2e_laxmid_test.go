package main

// Plan laxmid: the feed commands against the real processor, history and databases. By
// default a message-id without "@" (news.php.net's <12799969_70875456>) is refused by IHAVE
// (501), CHECK (438) and TAKETHIS (439) and nothing is stored, as before; with -laxmid
// (common.SetLaxMessageID, what main sets from the flag) the article passes the intake, is
// stored and ARTICLE/STAT <id> serve it. ">" inside and more than 250 octets stay refused.
// No wall-clock assertion: e2eCommitWait only bounds the polls.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
)

// e2eLaxmidID returns a unique RFC 3977 message-id without "@".
func e2eLaxmidID() string { return fmt.Sprintf("<%d_70875456>", e2eSeq.Add(1)) }

func TestE2ELaxmid(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroup(t, "laxmid")
	tooLong := "<" + strings.Repeat("a", 249) + ">" // 251 octets

	refused := func(t *testing.T, id string) {
		t.Helper()
		c := e2ePoster(t)
		if code, line := c.Cmd("IHAVE %s", id); code != 501 {
			t.Fatalf("IHAVE %q: %q, want 501", id, line)
		}
		if code, line := c.Cmd("CHECK %s", id); code != 438 {
			t.Fatalf("CHECK %q: %q, want 438", id, line)
		}
		if err := c.Send("TAKETHIS " + id + "\r\n" + e2eWire(e2eHead(id, g, "E2E laxmid refused", e2eNow()), []string{"refused"})); err != nil {
			t.Fatal(err)
		}
		if code, line, err := c.ReadStatus(); err != nil || code != 439 {
			t.Fatalf("TAKETHIS %q: code=%d line=%q err=%v, want 439", id, code, line, err)
		}
	}

	t.Run("strict", func(t *testing.T) {
		if common.LaxMessageID() {
			t.Fatal("lax message-ids are on by default")
		}
		id := e2eLaxmidID()
		refused(t, id)
		refused(t, "<a>b>")
		refused(t, tooLong)
		if _, err := e2eStored(id, g); err == nil {
			t.Fatalf("%s stored in strict mode", id)
		}
	})

	t.Run("lax", func(t *testing.T) {
		common.SetLaxMessageID(true)
		t.Cleanup(func() { common.SetLaxMessageID(false) })
		refused(t, "<a>b>")
		refused(t, tooLong)

		ihave, stream := e2eLaxmidID(), e2eLaxmidID()
		c := e2ePoster(t)
		if code, line := c.Cmd("CHECK %s", ihave); code != 238 {
			t.Fatalf("CHECK %s: %q, want 238", ihave, line)
		}
		if code, line := c.Cmd("IHAVE %s", ihave); code != 335 {
			t.Fatalf("IHAVE %s: %q, want 335", ihave, line)
		}
		if err := c.Send(e2eWire(e2eHead(ihave, g, "E2E laxmid ihave", e2eNow()), []string{"ihave body"})); err != nil {
			t.Fatal(err)
		}
		if code, line, err := c.ReadStatus(); err != nil || code != 235 {
			t.Fatalf("IHAVE %s article: code=%d line=%q err=%v, want 235", ihave, code, line, err)
		}
		if err := c.Send("TAKETHIS " + stream + "\r\n" + e2eWire(e2eHead(stream, g, "E2E laxmid takethis", e2eNow()), []string{"takethis body"})); err != nil {
			t.Fatal(err)
		}
		if code, line, err := c.ReadStatus(); err != nil || code != 239 {
			t.Fatalf("TAKETHIS %s: code=%d line=%q err=%v, want 239", stream, code, line, err)
		}
		nums := e2eWaitStored(t, ihave, g)
		e2eWaitStored(t, stream, g)

		// served by message-id: without a group (history), then in the group
		code, line := c.Cmd("ARTICLE %s", ihave)
		if code != 220 || !strings.HasPrefix(line, "220 0 "+ihave) {
			t.Fatalf("ARTICLE %s without a group: %q, want 220 0 %s", ihave, line, ihave)
		}
		head, _ := e2eSplitArticle(c.ReadDotLines())
		if v := e2eHeaderValue(head, "Message-ID"); v != ihave {
			t.Fatalf("ARTICLE %s Message-ID header %q", ihave, v)
		}
		if code, line := c.Cmd("GROUP %s", g); code != 211 {
			t.Fatalf("GROUP %s: %q", g, line)
		}
		code, line = c.Cmd("STAT %s", ihave)
		if want := fmt.Sprintf("223 %d %s", nums[g], ihave); code != 223 || !strings.HasPrefix(line, want) {
			t.Fatalf("STAT %s in %s: %q, want %q", ihave, g, line, want)
		}
	})
}
