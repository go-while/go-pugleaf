package nntp

// Unit tests of the NNTP intake wiring (plan nntp-intake-injection, slice intake-nntp):
// size-limit replies, the K4 reply table, proto-article injection, the relay Path and
// closeAfterReply. net.Pipe connections, no database; identifiers use intk / TestIntk.

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
)

// intkConn wires a ClientConnection with a config (MaxArtSize, Hostname) to the test
// side of a net.Pipe. proc may be nil.
func intkConn(t *testing.T, proc ArticleProcessor, maxArtSize int, hostname string, authenticated bool) (*ClientConnection, *textproto.Conn) {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	cfg := &config.ServerConfig{Hostname: hostname}
	cfg.NNTP.MaxArtSize = maxArtSize
	server := &NNTPServer{
		Config: cfg,
		DB: &database.Database{
			Batch: &database.SQ3batch{TasksMap: make(map[string]*database.BatchTasks)},
		},
		Stats:    NewServerStats(),
		local430: &Local430{cache: make(map[string]time.Time)},
	}
	if proc != nil {
		server.Processor = proc
	}
	c := NewClientConnection(serverEnd, server, false)
	c.authenticated = authenticated
	client := textproto.NewConn(clientEnd)
	t.Cleanup(func() {
		serverEnd.Close()
		clientEnd.Close()
	})
	return c, client
}

// intkArticle returns the wire lines (with the terminating ".") of an article of
// roughly size bytes.
func intkArticle(messageID string, size int) []string {
	lines := []string{
		"Path: peer.intk.invalid!not-for-mail",
		"From: Tester <tester@test.invalid>",
		"Newsgroups: t7.intk.a",
		"Subject: intk",
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: " + messageID,
		"",
	}
	n := 0
	for i := 0; n < size; i++ {
		l := fmt.Sprintf("%04d %s", i, strings.Repeat("x", 70))
		lines = append(lines, l)
		n += len(l) + 2
	}
	return append(lines, ".")
}

// intkReadLine reads one line or fails.
func intkReadLine(t *testing.T, client *textproto.Conn) string {
	t.Helper()
	line, err := client.ReadLine()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return line
}

// intkExpect reads a status line and checks its code.
func intkExpect(t *testing.T, client *textproto.Conn, code int, what string) string {
	t.Helper()
	line := intkReadLine(t, client)
	if !strings.HasPrefix(line, fmt.Sprintf("%d ", code)) {
		t.Fatalf("%s: got %q, want %d", what, line, code)
	}
	return line
}

// intkHandle runs c.Handle in the background; the returned channel delivers its result.
func intkHandle(c *ClientConnection) chan error {
	errc := make(chan error, 1)
	go func() { errc <- c.Handle() }()
	return errc
}

// intkWaitHandle waits for Handle to return and requires a nil result.
func intkWaitHandle(t *testing.T, errc chan error) {
	t.Helper()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Handle returned %v", err)
		}
	case <-time.After(hDeadline):
		t.Fatal("Handle did not return")
	}
}

// TestIntkTooLargeReplies (SEC-3, BUG-7): an article over MaxArtSize is answered with
// 441 / 437 / 439 <id> and the next command on the same connection is answered: the
// handlers keep the stream in sync on ErrArticleTooLarge.
func TestIntkTooLargeReplies(t *testing.T) {
	const id = "<t7-intk-big-1@test.invalid>"
	article := intkArticle(id, 3000)
	for _, tc := range []struct {
		name  string
		cmd   string
		inter int // interim reply before the article (0: streaming)
		want  string
	}{
		{"post", "POST", 340, "441 "},
		{"ihave", "IHAVE " + id, 335, "437 "},
		{"takethis", "TAKETHIS " + id, 0, "439 " + id + " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := &wiringTestProcessor{checkResult: history.CasePass, processResult: history.CasePass}
			c, client := intkConn(t, proc, 1024, "news.intk.invalid", true)
			errc := intkHandle(c)
			intkExpect(t, client, 200, "greeting")
			lines := append([]string{tc.cmd}, article...)
			if tc.inter != 0 {
				if err := client.PrintfLine("%s", tc.cmd); err != nil {
					t.Fatal(err)
				}
				intkExpect(t, client, tc.inter, tc.cmd)
				lines = article
			}
			werr := wiringTestWriteLines(client, lines)
			line := intkReadLine(t, client)
			if !strings.HasPrefix(line, tc.want) || !strings.Contains(line, "too large") {
				t.Fatalf("%s of a 3 KB article with MaxArtSize 1024: %q, want %q... too large", tc.cmd, line, tc.want)
			}
			if err := <-werr; err != nil {
				t.Fatalf("write article: %v", err)
			}
			if err := client.PrintfLine("MODE READER"); err != nil {
				t.Fatal(err)
			}
			intkExpect(t, client, 200, "MODE READER after the rejection")
			if err := client.PrintfLine("QUIT"); err != nil {
				t.Fatal(err)
			}
			intkExpect(t, client, 205, "QUIT")
			intkWaitHandle(t, errc)
			if n := len(proc.processedArticles()); n != 0 {
				t.Fatalf("%d oversized articles processed", n)
			}
		})
	}
}

// TestIntkReply: every row of contract K4 for every command.
func TestIntkReply(t *testing.T) {
	const id = "<t7-intk-reply@test.invalid>"
	rej := func(reason string) error { return fmt.Errorf("%w: %s", common.ErrIntakeRejected, reason) }
	temp := fmt.Errorf("%w: history: disk I/O error", common.ErrIntakeTemporary)
	type want struct {
		code  int
		text  string
		close bool
	}
	for _, tc := range []struct {
		name               string
		code               int
		err                error
		post, ihave, taket want
	}{
		{"pass", history.CasePass, nil,
			want{240, "Article posted successfully", false},
			want{235, "Article transferred successfully", false},
			want{239, id + " Article transferred successfully", false}},
		{"dupes", history.CaseDupes, nil,
			want{441, "Posting failed (duplicate)", false},
			want{437, "Rejected (duplicate)", false},
			want{439, id + " Rejected (duplicate)", false}},
		{"in-flight", history.CaseRetry, nil,
			want{441, "Posting failed (in progress, try again later)", false},
			want{436, "Retry later (in progress)", false},
			want{439, id + " In progress", false}},
		{"temporary", history.CaseRetry, temp,
			want{441, "Posting failed (temporary failure, try again later)", false},
			want{436, "Retry later (temporary failure)", false},
			want{400, "Service temporarily unavailable", true}},
		{"rejected", history.CaseError, rej("no valid newsgroup"),
			want{441, "Posting failed (no valid newsgroup)", false},
			want{437, "Rejected (no valid newsgroup)", false},
			want{439, id + " Rejected (no valid newsgroup)", false}},
		{"rejected-wrapped", history.CaseError, fmt.Errorf("newsgroups header: %w", rej("invalid newsgroups")),
			want{441, "Posting failed (invalid newsgroups)", false},
			want{437, "Rejected (invalid newsgroups)", false},
			want{439, id + " Rejected (invalid newsgroups)", false}},
		{"error-unclassified", history.CaseError, nil,
			want{441, "Posting failed (processing error)", false},
			want{436, "Transfer failed (processing error)", false},
			want{400, "Service temporarily unavailable", true}},
		{"error-plain", history.CaseError, errors.New("boom"),
			want{441, "Posting failed (processing error)", false},
			want{436, "Transfer failed (processing error)", false},
			want{400, "Service temporarily unavailable", true}},
		{"pass-with-error", history.CasePass, errors.New("odd"),
			want{441, "Posting failed (processing error)", false},
			want{436, "Transfer failed (processing error)", false},
			want{400, "Service temporarily unavailable", true}},
		{"retry-plain-error", history.CaseRetry, errors.New("odd"),
			want{441, "Posting failed (processing error)", false},
			want{436, "Transfer failed (processing error)", false},
			want{400, "Service temporarily unavailable", true}},
	} {
		for _, k := range []struct {
			kind intakeKind
			name string
			want want
		}{{intakePost, "post", tc.post}, {intakeIHave, "ihave", tc.ihave}, {intakeTakeThis, "takethis", tc.taket}} {
			code, text, closeConn := intakeReply(k.kind, tc.code, tc.err, id)
			if code != k.want.code || text != k.want.text || closeConn != k.want.close {
				t.Errorf("%s/%s: got (%d, %q, %v), want (%d, %q, %v)", tc.name, k.name,
					code, text, closeConn, k.want.code, k.want.text, k.want.close)
			}
		}
	}
}

// intkHead is a complete proto-article head; empty msgid or date leave the header out.
func intkHead(msgid, date string, extra ...string) []string {
	h := []string{
		"From: Tester <tester@test.invalid>",
		"Newsgroups: t7.intk.a",
		"Subject: injection",
	}
	if date != "" {
		h = append(h, "Date: "+date)
	}
	if msgid != "" {
		h = append(h, "Message-ID: "+msgid)
	}
	return append(h, extra...)
}

// TestIntkInjectProtoArticle (INT-1, A-intake-4, D4-D7, D10).
func TestIntkInjectProtoArticle(t *testing.T) {
	const host = "news.intk.invalid"
	const id = "<t7-intk-inject@test.invalid>"
	now := time.Now().UTC()
	date := func(d time.Duration) string { return now.Add(d).Format(time.RFC1123Z) }

	t.Run("rejects", func(t *testing.T) {
		c, _ := intkConn(t, nil, 1024, host, true)
		for _, tc := range []struct {
			name   string
			head   []string
			reason string
		}{
			{"injection-info", intkHead(id, date(0), "Injection-Info: other.invalid"), "forged trace"},
			{"xref", intkHead(id, date(0), "Xref: other.invalid g:1"), "forged trace"},
			{"posted-path", intkHead(id, date(0), "Path: x!.POSTED!y"), "forged trace"},
			{"posted-path-lower-case", intkHead(id, date(0), "Path: x!.posted!y"), "forged trace"},
			{"injection-date-without-message-id", intkHead("", date(0), "Injection-Date: "+date(0)), "invalid injection date"},
			{"injection-date-without-date", intkHead(id, "", "Injection-Date: "+date(0)), "invalid injection date"},
			{"bad-message-id", intkHead("no-brackets@test.invalid", date(0)), "invalid message-id"},
			{"empty-message-id", intkHead("", date(0), "Message-ID:"), "invalid message-id"},
			{"garbage-date", intkHead(id, "garbage"), "invalid date"},
			{"future-date", intkHead(id, date(25*time.Hour)), "date in the future"},
			{"old-date", intkHead(id, date(-73*time.Hour)), "date too old"},
		} {
			out, err := c.injectProtoArticle(tc.head)
			if err == nil {
				t.Errorf("%s: accepted: %q", tc.name, out)
				continue
			}
			if !errors.Is(err, common.ErrIntakeRejected) || !strings.HasSuffix(err.Error(), ": "+tc.reason) {
				t.Errorf("%s: error %v, want reason %q wrapping ErrIntakeRejected", tc.name, err, tc.reason)
			}
		}
	})

	t.Run("no-identity", func(t *testing.T) {
		c, _ := intkConn(t, nil, 1024, "", true)
		_, err := c.injectProtoArticle(intkHead(id, date(0)))
		if !errors.Is(err, common.ErrIntakeRejected) || !strings.HasSuffix(err.Error(), ": no path identity") {
			t.Fatalf("error %v, want no path identity", err)
		}
	})

	t.Run("generate", func(t *testing.T) {
		c, _ := intkConn(t, nil, 1024, host, true)
		in := intkHead("", "")
		inCopy := append([]string(nil), in...)
		out, err := c.injectProtoArticle(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(in, "\n") != strings.Join(inCopy, "\n") {
			t.Fatalf("input modified: %q", in)
		}
		mid := common.HeaderValue(out, "Message-ID")
		if !common.ValidMessageID(mid) || !strings.HasSuffix(mid, "@"+host+">") {
			t.Fatalf("generated Message-ID %q", mid)
		}
		d, err := mail.ParseDate(common.HeaderValue(out, "Date"))
		if err != nil || d.Sub(now) > time.Minute || now.Sub(d) > time.Minute {
			t.Fatalf("generated Date %q: %v", common.HeaderValue(out, "Date"), err)
		}
		inj := common.HeaderValue(out, "Injection-Date")
		if inj != common.HeaderValue(out, "Date") {
			t.Fatalf("Injection-Date %q, want the generated Date %q", inj, common.HeaderValue(out, "Date"))
		}
		if v := common.HeaderValues(out, "Injection-Info"); len(v) != 1 || v[0] != host {
			t.Fatalf("Injection-Info %q, want exactly %q (no DB, no user)", v, host)
		}
		if out[0] != "Path: "+host+"!.POSTED!not-for-mail" {
			t.Fatalf("Path line %q, want it inserted first with not-for-mail", out[0])
		}
	})

	t.Run("generate-message-id-only", func(t *testing.T) {
		c, _ := intkConn(t, nil, 1024, host, true)
		out, err := c.injectProtoArticle(intkHead("", date(-time.Hour)))
		if err != nil {
			t.Fatal(err)
		}
		if common.HeaderValue(out, "Injection-Date") == "" {
			t.Fatalf("no Injection-Date after a generated Message-ID: %q", out)
		}
		if common.HeaderValue(out, "Date") != date(-time.Hour) {
			t.Fatalf("the given Date was changed: %q", out)
		}
	})

	t.Run("both-present", func(t *testing.T) {
		c, _ := intkConn(t, nil, 1024, host, true)
		out, err := c.injectProtoArticle(intkHead(id, date(-71*time.Hour), "Path: client.invalid!not-for-mail"))
		if err != nil {
			t.Fatal(err)
		}
		if v := common.HeaderValues(out, "Injection-Date"); len(v) != 0 {
			t.Fatalf("Injection-Date %q added although Message-ID and Date were sent", v)
		}
		if v := common.HeaderValue(out, "Message-ID"); v != id {
			t.Fatalf("Message-ID %q, want %q", v, id)
		}
		if v := common.HeaderValues(out, "Path"); len(v) != 1 || v[0] != host+"!.POSTED!client.invalid!not-for-mail" {
			t.Fatalf("Path %q", v)
		}
	})

	t.Run("injection-date-with-both", func(t *testing.T) {
		c, _ := intkConn(t, nil, 1024, host, true)
		out, err := c.injectProtoArticle(intkHead(id, date(0), "Injection-Date: "+date(0)))
		if err != nil {
			t.Fatal(err)
		}
		if v := common.HeaderValues(out, "Injection-Date"); len(v) != 1 {
			t.Fatalf("Injection-Date copies %q, want the client's one only", v)
		}
	})
}

// intkAddrConn is a net.Conn with a fixed remote address.
type intkAddrConn struct {
	net.Conn
	remote net.Addr
}

func (c intkAddrConn) RemoteAddr() net.Addr { return c.remote }

// TestIntkRelayPath (D7): IHAVE/TAKETHIS prepend "<identity>!.SEEN.<peer IP>!".
func TestIntkRelayPath(t *testing.T) {
	const host = "news.intk.invalid"
	head := []string{"From: a <a@test.invalid>", "Path: peer.invalid!origin.invalid!not-for-mail", "Subject: x"}

	c, _ := intkConn(t, nil, 1024, host, true)
	c.conn = intkAddrConn{Conn: c.conn, remote: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 7), Port: 41119}}
	out := c.relayPath(head)
	if v := common.HeaderValue(out, "Path"); v != host+"!.SEEN.192.0.2.7!peer.invalid!origin.invalid!not-for-mail" {
		t.Fatalf("relayed Path %q", v)
	}
	if head[1] != "Path: peer.invalid!origin.invalid!not-for-mail" {
		t.Fatalf("input modified: %q", head)
	}

	// net.Pipe has no host:port address
	c2, _ := intkConn(t, nil, 1024, host, true)
	if v := common.HeaderValue(c2.relayPath(head), "Path"); v != host+"!.SEEN.unknown!peer.invalid!origin.invalid!not-for-mail" {
		t.Fatalf("relayed Path without a peer address %q", v)
	}

	// no Path: not invented; no identity: unchanged
	noPath := []string{"From: a <a@test.invalid>", "Subject: x"}
	if out := c.relayPath(noPath); strings.Join(out, "\n") != strings.Join(noPath, "\n") {
		t.Fatalf("Path invented: %q", out)
	}
	c3, _ := intkConn(t, nil, 1024, "", true)
	if out := c3.relayPath(head); strings.Join(out, "\n") != strings.Join(head, "\n") {
		t.Fatalf("head changed without an identity: %q", out)
	}
}

// TestIntkCloseAfterReply: the client reads the reply and then EOF; input already
// buffered by the server is discarded and never answered.
func TestIntkCloseAfterReply(t *testing.T) {
	t.Run("direct", func(t *testing.T) {
		c, client := intkConn(t, nil, 1024, "news.intk.invalid", true)
		go func() {
			// one write: both lines land in the server's read buffer together
			_, _ = client.W.WriteString("FIRST\r\nHELP\r\n")
			_ = client.W.Flush()
		}()
		line, err := c.readCommandLine()
		if err != nil || line != "FIRST" {
			t.Fatalf("readCommandLine: %q, %v", line, err)
		}
		errc := make(chan error, 1)
		go func() { errc <- c.closeAfterReply(400, "Service temporarily unavailable") }()
		intkExpect(t, client, 400, "closeAfterReply")
		if line, err := client.ReadLine(); err == nil {
			t.Fatalf("read %q after the 400, want EOF", line)
		}
		select {
		case err := <-errc:
			if !errors.Is(err, errQuit) {
				t.Fatalf("closeAfterReply returned %v, want errQuit", err)
			}
		case <-time.After(hDeadline):
			t.Fatal("closeAfterReply did not return")
		}
	})

	// through Handle: a TAKETHIS whose history check fails, pipelined with HELP
	t.Run("handle", func(t *testing.T) {
		const id = "<t7-intk-close@test.invalid>"
		proc := &wiringTestProcessor{checkResult: history.CaseError}
		c, client := intkConn(t, proc, 4096, "news.intk.invalid", true)
		errc := intkHandle(c)
		intkExpect(t, client, 200, "greeting")
		lines := append(append([]string{"TAKETHIS " + id}, intkArticle(id, 200)...), "HELP")
		werr := wiringTestWriteLines(client, lines)
		intkExpect(t, client, 400, "TAKETHIS with a history error")
		if line, err := client.ReadLine(); err == nil {
			t.Fatalf("read %q after the 400, want EOF (HELP must not be answered)", line)
		}
		<-werr // the HELP write may fail once the server closed; either way it is not answered
		intkWaitHandle(t, errc)
	})
}
