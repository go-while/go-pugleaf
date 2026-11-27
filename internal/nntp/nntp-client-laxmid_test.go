package nntp

// Plan laxmid (B), client side: with -laxmid (common.SetLaxMessageID) the client sends RFC
// 3977 ids without "@"; by default it refuses them as before. The trim of overview ids (A) is
// in nntp-client-msgid-trim_test.go. Hang guards only (cliCmdRun, cliWait), no timing
// assertion.

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
)

// cliLaxmidSet turns the lax message-id syntax on for this test and back off afterwards.
func cliLaxmidSet(t *testing.T) {
	t.Helper()
	common.SetLaxMessageID(true)
	t.Cleanup(func() { common.SetLaxMessageID(false) })
}

// TestCliLaxmidArg: validMessageIDArg refuses an id without "@" by default and takes it with
// -laxmid; ">" inside, controls, SP and more than 250 octets are refused in both modes.
func TestCliLaxmidArg(t *testing.T) {
	laxOnly := []string{"<12345_67890>", "<12799969_70875456>", "<00000003501CE2A1085223638>", "<20010303112347.31041.>"}
	never := []string{"<a>b>", "<a\x01b>", "<a\rb>", "<a b>", " <12345_67890>",
		"<" + strings.Repeat("a", 249) + ">"} // 251 octets
	check := func(mode string, id string, wantOK bool) {
		t.Helper()
		err := validMessageIDArg(id)
		if wantOK && err != nil {
			t.Errorf("%s: validMessageIDArg(%q) = %v, want nil", mode, id, err)
		}
		if !wantOK && !errors.Is(err, ErrInvalidCommandArg) {
			t.Errorf("%s: validMessageIDArg(%q) = %v, want ErrInvalidCommandArg", mode, id, err)
		}
	}
	for _, id := range laxOnly {
		check("strict", id, false)
	}
	for _, id := range never {
		check("strict", id, false)
	}
	cliLaxmidSet(t)
	for _, id := range laxOnly {
		check("lax", id, true)
	}
	for _, id := range never {
		check("lax", id, false)
	}
	check("lax", "<a@b>", true)
}

// TestCliLaxmidArticleSent: by default ARTICLE <12345_67890> is refused before anything is
// written; with -laxmid the command is sent and the article is returned.
func TestCliLaxmidArticleSent(t *testing.T) {
	const id = "<12345_67890>"
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "ARTICLE " + id, Reply: cliTrimArticle(id)},
	}})
	c := cliCmdDial(t, s, nil)
	msgID := id
	if _, err := cliCmdRun(t, func() error { _, err := c.GetArticle(&msgID, true); return err }); !errors.Is(err, ErrInvalidCommandArg) {
		t.Fatalf("strict: GetArticle(%s) = %v, want ErrInvalidCommandArg", id, err)
	}
	if got := s.Lines(); len(got) != 0 {
		t.Fatalf("strict: server saw %q, want nothing", got)
	}

	cliLaxmidSet(t)
	var art *models.Article
	if _, err := cliCmdRun(t, func() error { var err error; art, err = c.GetArticle(&msgID, true); return err }); err != nil {
		t.Fatalf("lax: GetArticle(%s): %v", id, err)
	}
	if art == nil || art.MessageID != id {
		t.Fatalf("lax: GetArticle(%s) = %+v, want the article", id, art)
	}
	if got, want := s.Lines(), []string{"ARTICLE " + id}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lax: server saw %q, want %q", got, want)
	}
	if m := s.Mismatches(); len(m) != 0 {
		t.Fatalf("Mismatches() = %q", m)
	}
}
