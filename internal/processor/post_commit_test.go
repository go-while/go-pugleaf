package processor

// ProcessPostedArticle (plan post-430-window, slice post-commit-wait): POST answers 240 only
// after the batch writer committed the article to its groups. The tests open a database in a
// temp dir (flNntpdbOpenDB) and run the real non-bulk processArticle; the commit barrier is
// either the real WaitCommitted or a fake through the postWaitCommitted seam.

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// local groups of these tests (rows in the main DB); postCommitRemote has no row
const (
	postCommitGroupA = "pcw.test.a"
	postCommitGroupB = "pcw.test.b"
	postCommitRemote = "pcw.remote.none"
)

// postCommitWaitCall is one call of the fake commit barrier.
type postCommitWaitCall struct {
	mark database.CommitMark
	stop <-chan struct{}
	d    time.Duration
}

// postCommitFake replaces postWaitCommitted with a fake that records its calls and returns ret.
type postCommitFake struct {
	mu    sync.Mutex
	calls []postCommitWaitCall
	ret   error
}

func postCommitInstallFake(t *testing.T, ret error) *postCommitFake {
	t.Helper()
	f := &postCommitFake{ret: ret}
	orig := postWaitCommitted
	t.Cleanup(func() { postWaitCommitted = orig })
	postWaitCommitted = func(sq *database.SQ3batch, m database.CommitMark, stop <-chan struct{}, d time.Duration) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, postCommitWaitCall{mark: m, stop: stop, d: d})
		return f.ret
	}
	return f
}

func (f *postCommitFake) snapshot() []postCommitWaitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]postCommitWaitCall(nil), f.calls...)
}

// postCommitLog captures the standard logger (the batch writer logs from its own goroutines).
type postCommitLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *postCommitLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *postCommitLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func postCommitCaptureLog(t *testing.T) *postCommitLog {
	t.Helper()
	l := &postCommitLog{}
	orig := log.Writer()
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(orig) })
	return l
}

// postCommitSetup opens a scratch database with the two local groups and returns a processor.
func postCommitSetup(t *testing.T) *Processor {
	t.Helper()
	db := flNntpdbOpenDB(t)
	for _, name := range []string{postCommitGroupA, postCommitGroupB} {
		if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Description: "post-commit-wait", Active: true, Status: "y", LowWater: 1}); err != nil {
			t.Fatalf("InsertNewsgroup %s: %v", name, err)
		}
	}
	// processArticle uses the global history.MsgIdCache: NewMsgIdItemCache creates it once
	return &Processor{DB: db, MsgIdCache: history.NewMsgIdItemCache()}
}

// postCommitMsgID returns a message-id unique to this run (the msgid cache is global).
func postCommitMsgID(tag string) string {
	return fmt.Sprintf("<pcw-%s-%d@test.invalid>", tag, time.Now().UnixNano())
}

// postCommitArticle builds a posted article like the NNTP server's buildIncomingArticle:
// NewsgroupsPtr holds every name of the Newsgroups header, Posted is set.
func postCommitArticle(t *testing.T, proc *Processor, msgid string, groups []string, subject bool) *models.Article {
	t.Helper()
	head := []string{
		"Path: poster.test.invalid!not-for-mail",
		"From: Tester <tester@test.invalid>",
		"Newsgroups: " + strings.Join(groups, ","),
	}
	if subject {
		head = append(head, "Subject: post commit wait")
	}
	head = append(head,
		"Date: Sun, 11 Oct 2026 12:00:00 +0000",
		"Message-ID: "+msgid,
	)
	body := []string{"post commit wait body"}
	lines := append(append(append([]string{}, head...), ""), body...)
	art, err := nntp.ParseLegacyArticleLines(msgid, lines, false)
	if err != nil {
		t.Fatalf("ParseLegacyArticleLines: %v", err)
	}
	art.NNTPhead = head
	art.NNTPbody = body
	art.ArticleNums = make(map[*string]int64, len(groups))
	for _, g := range groups {
		ptr := proc.DB.Batch.GetNewsgroupPointer(g)
		art.NewsgroupsPtr = append(art.NewsgroupsPtr, ptr)
		art.ArticleNums[ptr] = -1
	}
	art.Posted = true
	return art
}

// postCommitStored reports whether the group database of group stores msgid.
func postCommitStored(t *testing.T, proc *Processor, group, msgid string) bool {
	t.Helper()
	groupDB, err := proc.DB.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	stored, err := groupDB.LookupMsgIdInArticlesDB(msgid)
	if err != nil {
		t.Fatalf("LookupMsgIdInArticlesDB(%s, %s): %v", group, msgid, err)
	}
	return stored
}

// TestPostCommitWaitStored: with the real barrier, CasePass means the row is in every
// target group's database when ProcessPostedArticle returns.
func TestPostCommitWaitStored(t *testing.T) {
	proc := postCommitSetup(t)
	msgid := postCommitMsgID("stored")
	art := postCommitArticle(t, proc, msgid, []string{postCommitGroupA, postCommitGroupB}, true)

	code, err := proc.ProcessPostedArticle(art, nil, testWait)
	if code != history.CasePass || err != nil {
		t.Fatalf("ProcessPostedArticle = %d, %v; want CasePass, nil", code, err)
	}
	for _, g := range []string{postCommitGroupA, postCommitGroupB} {
		if !postCommitStored(t, proc, g, msgid) {
			t.Errorf("%s not in group database '%s' when ProcessPostedArticle returned", msgid, g)
		}
	}
}

// TestPostCommitWaitBarrier covers the error mapping and the timeout sharing with a fake
// barrier; the articles themselves are accepted by the real processArticle.
func TestPostCommitWaitBarrier(t *testing.T) {
	proc := postCommitSetup(t)

	t.Run("dropped", func(t *testing.T) {
		fake := postCommitInstallFake(t, fmt.Errorf("%w: 1 articles of '%s' dropped since the mark", database.ErrBatchDropped, postCommitGroupA))
		art := postCommitArticle(t, proc, postCommitMsgID("dropped"), []string{postCommitGroupA}, true)
		code, err := proc.ProcessPostedArticle(art, nil, 5*time.Second)
		if code != history.CaseError || !errors.Is(err, common.ErrIntakeRejected) {
			t.Fatalf("ProcessPostedArticle = %d, %v; want CaseError wrapping ErrIntakeRejected", code, err)
		}
		if got := iprReason(err); got != "not stored" {
			t.Errorf("rejection reason %q, want %q", got, "not stored")
		}
		if n := len(fake.snapshot()); n != 1 {
			t.Errorf("barrier called %d times, want 1", n)
		}
	})

	for name, ret := range map[string]error{
		"timeout": fmt.Errorf("%w: '%s' after 5s (0 of 1 articles settled)", database.ErrBatchWaitTimeout, postCommitGroupA),
		"stopped": fmt.Errorf("%w: '%s' (0 of 1 articles settled)", database.ErrBatchWaitStopped, postCommitGroupA),
	} {
		t.Run(name, func(t *testing.T) {
			fake := postCommitInstallFake(t, ret)
			logs := postCommitCaptureLog(t)
			msgid := postCommitMsgID(name)
			art := postCommitArticle(t, proc, msgid, []string{postCommitGroupA}, true)
			stop := make(chan struct{})
			code, err := proc.ProcessPostedArticle(art, stop, 5*time.Second)
			if code != history.CasePass || err != nil {
				t.Fatalf("ProcessPostedArticle = %d, %v; want CasePass, nil", code, err)
			}
			calls := fake.snapshot()
			if len(calls) != 1 {
				t.Fatalf("barrier called %d times, want 1", len(calls))
			}
			if calls[0].stop != (<-chan struct{})(stop) {
				t.Errorf("barrier got another stop channel than the caller's")
			}
			want := fmt.Sprintf("[INTAKE] POST %s: commit not confirmed within 5s: %v", msgid, ret)
			if n := strings.Count(logs.String(), want); n != 1 {
				t.Errorf("log has %d lines %q, want 1; log:\n%s", n, want, logs.String())
			}
			if n := strings.Count(logs.String(), "commit not confirmed"); n != 1 {
				t.Errorf("log has %d 'commit not confirmed' lines, want 1", n)
			}
		})
	}

	t.Run("no wait", func(t *testing.T) {
		fake := postCommitInstallFake(t, errors.New("barrier must not be called"))
		for _, d := range []time.Duration{0, -time.Second} {
			art := postCommitArticle(t, proc, postCommitMsgID("nowait"), []string{postCommitGroupA}, true)
			code, err := proc.ProcessPostedArticle(art, nil, d)
			if code != history.CasePass || err != nil {
				t.Fatalf("timeout %v: ProcessPostedArticle = %d, %v; want CasePass, nil", d, code, err)
			}
		}
		if n := len(fake.snapshot()); n != 0 {
			t.Errorf("barrier called %d times with timeout <= 0, want 0", n)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		fake := postCommitInstallFake(t, errors.New("barrier must not be called"))
		groups := []string{postCommitGroupA}
		wantCode, wantErr := proc.ProcessIncomingArticle(postCommitArticle(t, proc, postCommitMsgID("rej-in"), groups, false))
		code, err := proc.ProcessPostedArticle(postCommitArticle(t, proc, postCommitMsgID("rej-post"), groups, false), nil, 5*time.Second)
		if wantCode != history.CaseError || !errors.Is(wantErr, common.ErrIntakeRejected) {
			t.Fatalf("ProcessIncomingArticle without Subject = %d, %v; want a rejection", wantCode, wantErr)
		}
		if code != wantCode || err == nil || err.Error() != wantErr.Error() {
			t.Errorf("ProcessPostedArticle = %d, %v; want %d, %v (as ProcessIncomingArticle)", code, err, wantCode, wantErr)
		}
		if n := len(fake.snapshot()); n != 0 {
			t.Errorf("barrier called %d times for a rejected article, want 0", n)
		}
	})

	t.Run("marks per name", func(t *testing.T) {
		fake := postCommitInstallFake(t, nil)
		groups := []string{postCommitGroupA, postCommitRemote, postCommitGroupB}
		const timeout = 7 * time.Second
		art := postCommitArticle(t, proc, postCommitMsgID("marks"), groups, true)
		code, err := proc.ProcessPostedArticle(art, nil, timeout)
		if code != history.CasePass || err != nil {
			t.Fatalf("ProcessPostedArticle = %d, %v; want CasePass, nil", code, err)
		}
		calls := fake.snapshot()
		if len(calls) != len(groups) {
			t.Fatalf("barrier called %d times, want %d (one per Newsgroups name)", len(calls), len(groups))
		}
		prev := timeout
		for i, c := range calls {
			// nothing was dropped: the mark of a name equals a fresh mark of that name
			if want := proc.DB.Batch.CommitMark(groups[i]); c.mark != want {
				t.Errorf("call %d: mark %+v, want the mark of '%s' %+v", i, c.mark, groups[i], want)
			}
			if c.d <= 0 || c.d > prev {
				t.Errorf("call %d: remaining timeout %v, want in (0, %v]", i, c.d, prev)
			}
			prev = c.d
		}
	})
}
