package nntp

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// cliValRun runs f in a goroutine and fails the test when it does not return
// within cliWait (a hang guard: a rejected command must never block on the
// connection).
func cliValRun(t *testing.T, name string, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(cliWait):
		t.Fatalf("%s: no return within %v", name, cliWait)
		return nil
	}
}

// A-nntp-client-3: the three argument validators.
func TestCliValArgs(t *testing.T) {
	id250 := "<" + strings.Repeat("a", 242) + "@x.inv>" // 250 octets: the maximum
	id251 := "<" + strings.Repeat("a", 243) + "@x.inv>" // 251 octets
	if len(id250) != 250 || len(id251) != 251 {
		t.Fatalf("id250/id251 have %d/%d octets", len(id250), len(id251))
	}
	group497 := strings.Repeat("g", 497)

	msgIDs := []struct {
		in string
		ok bool
	}{
		{"<a@b>", true},
		{"<part1.part2@example.invalid>", true},
		{"", false},
		{"<a\rb@c>", false},
		{"<a\nb@c>", false},
		{"<a\x00b@c>", false},
		{"<a b@c>", false},
		{"<a@b> x", false},
		{"<a@b>\r\nQUIT", false},
		{"a@b", false},
		{"<ab>", false},
		{id250, true},
		{id251, false},
	}
	for _, tc := range msgIDs {
		err := validMessageIDArg(tc.in)
		if tc.ok && err != nil {
			t.Errorf("validMessageIDArg(%q) = %v, want nil", tc.in, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidCommandArg) {
			t.Errorf("validMessageIDArg(%q) = %v, want ErrInvalidCommandArg", tc.in, err)
		}
	}

	groups := []struct {
		in string
		ok bool
	}{
		{"alt.test", true},
		{"de.comp.lang.go", true},
		{group497, true},
		{group497 + "g", false},
		{"", false},
		{"a,b", false},
		{"a b", false},
		{"a\tb", false},
		{"a\rb", false},
		{"a\nb", false},
		{"a\x00b", false},
		{"a\x7fb", false},
		{"a\x1fb", false},
	}
	for _, tc := range groups {
		err := validGroupArg(tc.in)
		if tc.ok && err != nil {
			t.Errorf("validGroupArg(%q) = %v, want nil", tc.in, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidCommandArg) {
			t.Errorf("validGroupArg(%q) = %v, want ErrInvalidCommandArg", tc.in, err)
		}
	}

	tokens := []struct {
		in         string
		allowSpace bool
		ok         bool
	}{
		{"Subject", false, true},
		{"message-id", false, true},
		{"Sub ject", false, false},
		{"Sub\tject", false, false},
		{"pass word", true, true},
		{"pass\tword", true, true},
		{"", false, false},
		{"", true, false},
		{"a\rb", true, false},
		{"a\nb", true, false},
		{"a\x00b", true, false},
		{"a\r\nQUIT", false, false},
	}
	for _, tc := range tokens {
		err := validTokenArg(tc.in, tc.allowSpace)
		if tc.ok && err != nil {
			t.Errorf("validTokenArg(%q, %v) = %v, want nil", tc.in, tc.allowSpace, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidCommandArg) {
			t.Errorf("validTokenArg(%q, %v) = %v, want ErrInvalidCommandArg", tc.in, tc.allowSpace, err)
		}
		if err != nil && tc.in != "" && strings.Contains(err.Error(), tc.in) {
			t.Errorf("validTokenArg(%q) error %q contains the value", tc.in, err)
		}
	}
}

// A-nntp-client-3: rejected arguments write nothing and leave the connection
// usable; a following valid STAT is the first line the server sees.
func TestCliValNothingWritten(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "STAT <ok@x>", Reply: []string{"223 0 <ok@x>"}},
	}})
	c, err := cliDial(t, s, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	newsgroup := "alt.test"
	badID := "<a\rb@c>"
	spaceID := "<a@b> x"
	rejected := []struct {
		name string
		call func() error
	}{
		{"STAT CR", func() error { _, err := c.StatArticle(badID); return err }},
		{"STAT trailing arg", func() error { _, err := c.StatArticle(spaceID); return err }},
		{"GROUP space", func() error { _, _, err := c.SelectGroup("a b"); return err }},
		{"GROUP comma", func() error { _, _, err := c.SelectGroup("a,b"); return err }},
		{"XHDR field space", func() error { _, err := c.XHdr(newsgroup, "Sub ject", 1, 10); return err }},
		{"XHDR group CR", func() error { _, err := c.XHdr("a\rb", "Subject", 1, 10); return err }},
		{"ARTICLE CR", func() error { _, err := c.GetArticle(&badID, false); return err }},
		{"ARTICLE nil", func() error { _, err := c.GetArticle(nil, false); return err }},
		{"HEAD CR", func() error { _, err := c.GetHead(badID); return err }},
		{"BODY CR", func() error { _, err := c.GetBody(badID); return err }},
		{"XOVER group space", func() error { _, err := c.XOver("a b", 1, 10, true); return err }},
		{"LISTGROUP group LF", func() error { _, err := c.ListGroup("a\nb", 1, 10); return err }},
		{"XHDR stream field CR", func() error {
			return c.XHdrStreamedBatch(newsgroup, "Subject\r", 1, 10, make(chan HeaderLine, 1), make(chan struct{}))
		}},
		{"CHECK second id invalid", func() error {
			ok := "<ok1@x>"
			job := &CHTTJob{Newsgroup: &newsgroup}
			_, err := c.SendCheckMultiple([]*string{&ok, &spaceID}, nil, job, nil)
			return err
		}},
		{"TAKETHIS invalid id", func() error {
			art := &models.Article{MessageID: spaceID, Subject: "s", FromHeader: "a@b", BodyText: "x"}
			_, n, err, doContinue := c.SendTakeThisArticleStreaming(art, &newsgroup, newsgroup, nil, nil, nil, 0, 1)
			if !doContinue || n != 0 {
				t.Errorf("TAKETHIS: doContinue=%v txBytes=%d, want true, 0", doContinue, n)
			}
			return err
		}},
	}
	for _, r := range rejected {
		err := cliValRun(t, r.name, r.call)
		if r.name == "TAKETHIS invalid id" {
			// ReconstructHeaders rejects the id first; either way nothing is written
			if err == nil {
				t.Errorf("%s: err = nil, want an error", r.name)
			}
		} else if !errors.Is(err, ErrInvalidCommandArg) {
			t.Errorf("%s: err = %v, want ErrInvalidCommandArg", r.name, err)
		}
		if got := s.Lines(); len(got) != 0 {
			t.Fatalf("%s: server saw %q, want nothing", r.name, got)
		}
	}
	if !c.IsConnected() {
		t.Fatalf("connection closed after rejected arguments")
	}

	var ok bool
	if err := cliValRun(t, "STAT ok", func() error { var err error; ok, err = c.StatArticle("<ok@x>"); return err }); err != nil || !ok {
		t.Fatalf("STAT <ok@x>: ok=%v err=%v, want true, nil", ok, err)
	}
	if got, want := s.Lines(), []string{"STAT <ok@x>"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines() = %q, want %q", got, want)
	}
	if m := s.Mismatches(); len(m) != 0 {
		t.Fatalf("Mismatches() = %q", m)
	}
}

// A-nntp-client-3: a command line over 510 octets (or with CR/LF/NUL from
// any argument) is refused by sendCmd before anything is written.
func TestCliValSendCmdLength(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "STAT <ok@x>", Reply: []string{"223 0 <ok@x>"}},
	}})
	c, err := cliDial(t, s, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	arg600 := strings.Repeat("x", 600-len("XHDR "))
	cases := []struct {
		name   string
		format string
		args   []any
	}{
		{"600 octets", "XHDR %s", []any{arg600}},
		{"511 octets", "%s", []any{strings.Repeat("y", 511)}},
		{"CR", "STAT %s", []any{"<a\r@b>"}},
		{"LF", "XHDR %s 1-2", []any{"Subject\nQUIT"}},
		{"NUL", "GROUP %s", []any{"a\x00b"}},
	}
	for _, tc := range cases {
		_, err := c.sendCmd(tc.format, tc.args...)
		if !errors.Is(err, ErrInvalidCommandArg) {
			t.Errorf("%s: err = %v, want ErrInvalidCommandArg", tc.name, err)
		}
		if got := s.Lines(); len(got) != 0 {
			t.Fatalf("%s: server saw %q, want nothing", tc.name, got)
		}
	}
	// exactly 510 octets is allowed: the server answers 500 (not scripted) and
	// the textproto pipeline stays in step for the following STAT
	line510 := "STAT <" + strings.Repeat("z", 510-len("STAT <@x>")) + "@x>"
	if len(line510) != 510 {
		t.Fatalf("line510 has %d octets", len(line510))
	}
	id, err := c.sendCmd("%s", line510)
	if err != nil {
		t.Fatalf("510 octets: err = %v, want nil", err)
	}
	c.TextConn.StartResponse(id)
	code, _, _ := c.TextConn.ReadCodeLine(0)
	c.TextConn.EndResponse(id)
	if code != 500 {
		t.Fatalf("510 octets: code = %d, want 500 from the fake", code)
	}
	var ok bool
	if err := cliValRun(t, "STAT ok", func() error { var err error; ok, err = c.StatArticle("<ok@x>"); return err }); err != nil || !ok {
		t.Fatalf("STAT <ok@x>: ok=%v err=%v, want true, nil", ok, err)
	}
	if got, want := s.Lines(), []string{line510, "STAT <ok@x>"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines() = %q, want %q", got, want)
	}
}

// A-nntp-client-3: AUTHINFO user/password may contain spaces but no CR/LF/NUL;
// an invalid password is refused before AUTHINFO PASS is written.
func TestCliValAuthinfo(t *testing.T) {
	t.Run("password with CR", func(t *testing.T) {
		s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
			{Expect: "AUTHINFO USER u", Reply: []string{"381 password required"}},
		}})
		_, err := cliDial(t, s, func(cfg *BackendConfig) {
			cfg.Username = "u"
			cfg.Password = "p\r\nQUIT"
			cfg.AuthFailureBackoff = -1
		})
		if !errors.Is(err, ErrInvalidCommandArg) {
			t.Fatalf("Connect: err = %v, want ErrInvalidCommandArg", err)
		}
		if strings.Contains(err.Error(), "QUIT") {
			t.Fatalf("error %q contains the password", err)
		}
		s.WaitOpen(t, 0, 2*time.Second)
		if got, want := s.Lines(), []string{"AUTHINFO USER u"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Lines() = %q, want %q", got, want)
		}
	})
	t.Run("spaces pass", func(t *testing.T) {
		s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
			{Expect: "AUTHINFO USER u v", Reply: []string{"381 password required"}},
			{Expect: "AUTHINFO PASS p q", Reply: []string{"281 ok"}},
		}})
		_, err := cliDial(t, s, func(cfg *BackendConfig) {
			cfg.Username = "u v"
			cfg.Password = "p q"
			cfg.AuthFailureBackoff = -1
		})
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
	})
}

// A-nntp-client-3: Pool.GetArticle puts the connection back after
// ErrInvalidCommandArg (healthy, like 430/451).
func TestCliValPoolGetArticle(t *testing.T) {
	s := newCliFakeServer(t, cliFakeOpts{Script: []cliFakeStep{
		{Expect: "MODE READER", Reply: []string{"200 reader"}},
	}})
	cfg := s.Backend()
	cfg.AuthFailureBackoff = -1
	pool := NewPool(cfg)
	t.Cleanup(func() { _ = pool.ClosePool() })

	bad := "<a b@c>"
	err := cliValRun(t, "GetArticle", func() error { _, err := pool.GetArticle(&bad, false); return err })
	if !errors.Is(err, ErrInvalidCommandArg) {
		t.Fatalf("GetArticle: err = %v, want ErrInvalidCommandArg", err)
	}
	st := pool.Stats()
	if st.IdleConnections != 1 || st.TotalClosed != 0 {
		t.Fatalf("Stats = %+v, want 1 idle, 0 closed", st)
	}
	if got := s.Open(); got != 1 {
		t.Fatalf("server Open() = %d, want 1", got)
	}
	if got, want := s.Lines(), []string{"MODE READER"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines() = %q, want %q", got, want)
	}
}
