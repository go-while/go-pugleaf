package nntp

import (
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// wiringTestProcessor is a fake ArticleProcessor
type wiringTestProcessor struct {
	mu            sync.Mutex
	checkResult   int
	processResult int
	processErr    error
	processed     []*models.Article
	checked       []string
	findArticle   *models.Article
	findErr       error
	findCalls     int
	lastFindGroup string
}

func (p *wiringTestProcessor) ProcessIncomingArticle(article *models.Article) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.processed = append(p.processed, article)
	return p.processResult, p.processErr
}

func (p *wiringTestProcessor) CheckMessageID(messageID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked = append(p.checked, messageID)
	return p.checkResult
}

func (p *wiringTestProcessor) FindArticleByMessageID(messageID, currentGroup string) (*models.Article, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.findCalls++
	p.lastFindGroup = currentGroup
	return p.findArticle, p.findErr
}

func (p *wiringTestProcessor) processedArticles() []*models.Article {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*models.Article(nil), p.processed...)
}

// wiringTestConn wires a ClientConnection to the test side of a net.Pipe.
// proc may be nil (read-only server).
func wiringTestConn(t *testing.T, proc ArticleProcessor, authenticated bool) (*ClientConnection, *textproto.Conn) {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	server := &NNTPServer{
		DB: &database.Database{
			Batch: &database.SQ3batch{TasksMap: make(map[string]*database.BatchTasks)},
		},
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

func wiringTestArticle(messageID string) []string {
	lines := []string{
		"From: Tester <tester@example.org>",
		"Newsgroups: alt.test",
		"Subject: wiring test",
		"Date: Sat, 12 Sep 2026 10:11:12 +0000",
	}
	if messageID != "" {
		lines = append(lines, "Message-ID: "+messageID)
	}
	return append(lines, "", "body line", "..dot-stuffed", ".")
}

// wiringTestRun runs handler in the background and returns a channel closed when it returns
func wiringTestRun(t *testing.T, handler func() error) chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := handler(); err != nil {
			t.Logf("handler returned: %v", err)
		}
	}()
	return done
}

func wiringTestWriteLines(client *textproto.Conn, lines []string) chan error {
	errc := make(chan error, 1)
	go func() {
		for _, line := range lines {
			if err := client.PrintfLine("%s", line); err != nil {
				errc <- err
				return
			}
		}
		errc <- nil
	}()
	return errc
}

func wiringTestReadCode(t *testing.T, client *textproto.Conn) (int, string) {
	t.Helper()
	line, err := client.ReadLine()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var code int
	if _, err := fmt.Sscanf(line, "%d", &code); err != nil {
		t.Fatalf("bad response line %q", line)
	}
	return code, line
}

func wiringTestWait(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
	}
}

// wiringTestNoExtraReply verifies the handler sent exactly one reply: the pipe has nothing more to read
func wiringTestNoExtraReply(t *testing.T, c *ClientConnection, client *textproto.Conn) {
	t.Helper()
	c.conn.Close() // handler is done: closing our end makes the client read EOF if nothing else was sent
	if line, err := client.ReadLine(); err == nil {
		t.Fatalf("unexpected extra reply %q", line)
	}
}

func TestWiringIHavePreCheck(t *testing.T) {
	const msgid = "<ihave-pre@example.org>"
	cases := []struct {
		name  string
		check int
		want  int
	}{
		{"dupes", history.CaseDupes, 435},
		{"retry", history.CaseRetry, 436},
		{"error", history.CaseError, 436},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc := &wiringTestProcessor{checkResult: tc.check}
			c, client := wiringTestConn(t, proc, true)
			done := wiringTestRun(t, func() error { return c.handleIHave([]string{msgid}) })
			if code, line := wiringTestReadCode(t, client); code != tc.want {
				t.Fatalf("got %q, want %d", line, tc.want)
			}
			wiringTestWait(t, done, "IHAVE")
			wiringTestNoExtraReply(t, c, client)
			if len(proc.processedArticles()) != 0 {
				t.Fatalf("article must not be processed")
			}
		})
	}
}

func TestWiringIHaveTransfer(t *testing.T) {
	const msgid = "<ihave-transfer@example.org>"
	cases := []struct {
		name          string
		articleMsgID  string
		processResult int
		want          int
		wantProcessed bool
	}{
		{"accepted", msgid, history.CasePass, 235, true},
		{"no-message-id-uses-argument", "", history.CasePass, 235, true},
		{"message-id-mismatch", "<other@example.org>", history.CasePass, 437, false},
		{"duplicate-while-processing", msgid, history.CaseDupes, 437, true},
		{"processing-error", msgid, history.CaseError, 436, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc := &wiringTestProcessor{checkResult: history.CasePass, processResult: tc.processResult}
			c, client := wiringTestConn(t, proc, true)
			done := wiringTestRun(t, func() error { return c.handleIHave([]string{msgid}) })
			if code, line := wiringTestReadCode(t, client); code != 335 {
				t.Fatalf("got %q, want 335", line)
			}
			if err := <-wiringTestWriteLines(client, wiringTestArticle(tc.articleMsgID)); err != nil {
				t.Fatalf("write article: %v", err)
			}
			if code, line := wiringTestReadCode(t, client); code != tc.want {
				t.Fatalf("got %q, want %d", line, tc.want)
			}
			wiringTestWait(t, done, "IHAVE")
			wiringTestNoExtraReply(t, c, client)
			processed := proc.processedArticles()
			if tc.wantProcessed != (len(processed) == 1) {
				t.Fatalf("processed %d articles, want processed=%v", len(processed), tc.wantProcessed)
			}
			if tc.wantProcessed && processed[0].MessageID != msgid {
				t.Fatalf("processed MessageID %q, want %q", processed[0].MessageID, msgid)
			}
		})
	}
}

func TestWiringIHaveNotAuthenticated(t *testing.T) {
	proc := &wiringTestProcessor{checkResult: history.CasePass}
	c, client := wiringTestConn(t, proc, false)
	done := wiringTestRun(t, func() error { return c.handleIHave([]string{"<unauth@example.org>"}) })
	if code, line := wiringTestReadCode(t, client); code != 480 {
		t.Fatalf("got %q, want 480", line)
	}
	wiringTestWait(t, done, "IHAVE")
	if len(proc.checked) != 0 {
		t.Fatalf("CheckMessageID must not be called before auth")
	}
}

func TestWiringTakeThis(t *testing.T) {
	const msgid = "<takethis@example.org>"
	cases := []struct {
		name          string
		noProcessor   bool
		args          []string
		check         int
		articleMsgID  string
		processResult int
		want          int
		wantProcessed bool
	}{
		{"accepted", false, []string{msgid}, history.CasePass, msgid, history.CasePass, 239, true},
		{"no-message-id-uses-argument", false, []string{msgid}, history.CasePass, "", history.CasePass, 239, true},
		{"message-id-mismatch", false, []string{msgid}, history.CasePass, "<other@example.org>", history.CasePass, 439, false},
		{"dupes", false, []string{msgid}, history.CaseDupes, msgid, history.CasePass, 439, false},
		{"retry", false, []string{msgid}, history.CaseRetry, msgid, history.CasePass, 439, false},
		{"history-error", false, []string{msgid}, history.CaseError, msgid, history.CasePass, 439, false},
		{"processing-failed", false, []string{msgid}, history.CasePass, msgid, history.CaseDupes, 439, true},
		{"no-processor", true, []string{msgid}, history.CasePass, msgid, history.CasePass, 502, false},
		{"missing-argument", false, nil, history.CasePass, msgid, history.CasePass, 501, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc := &wiringTestProcessor{checkResult: tc.check, processResult: tc.processResult}
			var ap ArticleProcessor = proc
			if tc.noProcessor {
				ap = nil
			}
			c, client := wiringTestConn(t, ap, true)
			done := wiringTestRun(t, func() error { return c.handleTakeThis(tc.args) })
			// streaming: the article follows the command without waiting.
			// net.Pipe is unbuffered, so the write only completes if the server consumes the article.
			if err := <-wiringTestWriteLines(client, wiringTestArticle(tc.articleMsgID)); err != nil {
				t.Fatalf("write article (server did not consume it?): %v", err)
			}
			code, line := wiringTestReadCode(t, client)
			if code != tc.want {
				t.Fatalf("got %q, want %d", line, tc.want)
			}
			if (code == 239 || code == 439) && !strings.Contains(line, msgid) {
				t.Fatalf("reply %q does not carry the message-id", line)
			}
			wiringTestWait(t, done, "TAKETHIS")
			wiringTestNoExtraReply(t, c, client)
			processed := proc.processedArticles()
			if tc.wantProcessed != (len(processed) == 1) {
				t.Fatalf("processed %d articles, want processed=%v", len(processed), tc.wantProcessed)
			}
			if tc.wantProcessed && processed[0].MessageID != msgid {
				t.Fatalf("processed MessageID %q, want %q", processed[0].MessageID, msgid)
			}
		})
	}
}

func TestWiringMessageIDLookupLocal430(t *testing.T) {
	const msgid = "<lookup-miss@example.org>"
	proc := &wiringTestProcessor{findErr: fmt.Errorf("miss: %w", ErrArticleNotFound)}
	c, client := wiringTestConn(t, proc, false)
	for i := 0; i < 2; i++ {
		done := wiringTestRun(t, func() error { return c.handleStat([]string{msgid}) })
		if code, line := wiringTestReadCode(t, client); code != 430 {
			t.Fatalf("round %d: got %q, want 430", i, line)
		}
		wiringTestWait(t, done, "STAT")
	}
	if proc.findCalls != 1 {
		t.Fatalf("FindArticleByMessageID calls = %d, want 1 (second miss answered from local430)", proc.findCalls)
	}
}

func TestWiringMessageIDLookupKeepsCurrentArticle(t *testing.T) {
	const msgid = "<lookup-hit@example.org>"
	proc := &wiringTestProcessor{findArticle: &models.Article{MessageID: msgid, DBArtNum: 77}}
	c, client := wiringTestConn(t, proc, false)
	// the current-group lookup fails (DB without config), the processor finds it in another group:
	// currentArticle must stay untouched (RFC 3977)
	c.currentGroup = "alt.test"
	c.currentArticle = 5
	done := wiringTestRun(t, func() error { return c.handleStat([]string{msgid}) })
	code, line := wiringTestReadCode(t, client)
	if code != 223 {
		t.Fatalf("got %q, want 223", line)
	}
	// found outside the current group: the reply carries article number 0 (RFC 3977 6.2.1)
	if !strings.HasPrefix(line, "223 0 "+msgid) {
		t.Fatalf("reply %q, want article number 0", line)
	}
	wiringTestWait(t, done, "STAT")
	if c.currentArticle != 5 {
		t.Fatalf("currentArticle = %d, want 5", c.currentArticle)
	}
	if proc.findArticle.DBArtNum != 77 {
		t.Fatalf("shared article DBArtNum changed to %d", proc.findArticle.DBArtNum)
	}
	if proc.lastFindGroup != "" {
		t.Fatalf("FindArticleByMessageID currentGroup = %q, want \"\" (current group already checked)", proc.lastFindGroup)
	}
}
