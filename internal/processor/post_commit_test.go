package processor

// ProcessPostedArticle (plan post-430-window, slice post-commit-wait): POST answers 240 only
// after the batch writer committed the article to its groups. The tests open a database in a
// temp dir (flNntpdbOpenDB) and run the real non-bulk ProcessArticle; the commit barrier is
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
	// ProcessArticle uses the global history.MsgIdCache: NewMsgIdItemCache creates it once
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

// postCommitRows replaces postArticleStored: answer(group, n) gives the result of the n-th
// lookup (0-based) of group; the lookups are recorded.
type postCommitRows struct {
	mu      sync.Mutex
	lookups map[string]int
	answer  func(group string, n int) (bool, error)
}

func postCommitInstallRows(t *testing.T, answer func(group string, n int) (bool, error)) *postCommitRows {
	t.Helper()
	r := &postCommitRows{lookups: make(map[string]int), answer: answer}
	orig := postArticleStored
	t.Cleanup(func() { postArticleStored = orig })
	postArticleStored = func(proc *Processor, group, messageID string) (bool, error) {
		r.mu.Lock()
		n := r.lookups[group]
		r.lookups[group] = n + 1
		r.mu.Unlock()
		return r.answer(group, n)
	}
	return r
}

func (r *postCommitRows) count(group string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookups[group]
}

// postCommitCallsFor counts the barrier calls whose mark is the current mark of group
// (nothing is dropped in these tests, so a mark equals a fresh mark of its group).
func postCommitCallsFor(proc *Processor, calls []postCommitWaitCall, group string) int {
	want := proc.DB.Batch.CommitMark(group)
	n := 0
	for _, c := range calls {
		if c.mark == want {
			n++
		}
	}
	return n
}

// postCommitSkipIfCommitted skips when the batch writer committed the article to group
// before ProcessPostedArticle took its snapshot of the captured groups (the periodic flush
// can land in that window; the group then needs no wait and the barrier is not called).
func postCommitSkipIfCommitted(t *testing.T, proc *Processor, calls []postCommitWaitCall, group, msgid string) {
	t.Helper()
	if len(calls) == 0 && postCommitStored(t, proc, group, msgid) {
		t.Skipf("'%s' committed before the snapshot: no barrier call to check", group)
	}
}

// TestPostCommitWaitBarrier covers the error mapping, the row check and the timeout sharing
// with a fake barrier (and a fake row lookup where the row state must be forced); the
// articles themselves are accepted by the real ProcessArticle.
func TestPostCommitWaitBarrier(t *testing.T) {
	proc := postCommitSetup(t)

	t.Run("dropped, row absent", func(t *testing.T) {
		fake := postCommitInstallFake(t, fmt.Errorf("%w: 1 articles of '%s' dropped since the mark", database.ErrBatchDropped, postCommitGroupA))
		rows := postCommitInstallRows(t, func(string, int) (bool, error) { return false, nil })
		msgid := postCommitMsgID("dropped")
		art := postCommitArticle(t, proc, msgid, []string{postCommitGroupA}, true)
		code, err := proc.ProcessPostedArticle(art, nil, 5*time.Second)
		postCommitSkipIfCommitted(t, proc, fake.snapshot(), postCommitGroupA, msgid)
		if code != history.CaseError || !errors.Is(err, common.ErrIntakeRejected) {
			t.Fatalf("ProcessPostedArticle = %d, %v; want CaseError wrapping ErrIntakeRejected", code, err)
		}
		if got := iprReason(err); got != "not stored" {
			t.Errorf("rejection reason %q, want %q", got, "not stored")
		}
		if n := len(fake.snapshot()); n != 1 {
			t.Errorf("barrier called %d times, want 1", n)
		}
		if n := rows.count(postCommitGroupA); n != 1 {
			t.Errorf("row looked up %d times, want 1", n)
		}
	})

	t.Run("dropped, row present", func(t *testing.T) {
		// another article of the group was dropped; this one is committed (real barrier,
		// real row lookup): the row wins, no 441
		var calls int
		orig := postWaitCommitted
		t.Cleanup(func() { postWaitCommitted = orig })
		postWaitCommitted = func(sq *database.SQ3batch, m database.CommitMark, stop <-chan struct{}, d time.Duration) error {
			calls++ // only the caller's goroutine calls the seam
			if err := sq.WaitCommitted(m, stop, d); err != nil {
				return err
			}
			return fmt.Errorf("%w: 1 articles of '%s' dropped since the mark", database.ErrBatchDropped, postCommitGroupA)
		}
		msgid := postCommitMsgID("dropped-present")
		art := postCommitArticle(t, proc, msgid, []string{postCommitGroupA}, true)
		code, err := proc.ProcessPostedArticle(art, nil, testWait)
		if code != history.CasePass || err != nil {
			t.Fatalf("ProcessPostedArticle = %d, %v; want CasePass, nil (the row is stored)", code, err)
		}
		if !postCommitStored(t, proc, postCommitGroupA, msgid) {
			t.Errorf("%s not in '%s' after CasePass", msgid, postCommitGroupA)
		}
		if calls > 1 {
			t.Errorf("barrier called %d times, want at most 1", calls)
		}
	})

	t.Run("committed count, row absent then present", func(t *testing.T) {
		// the per-group count was satisfied by another article: wait again until the row is there
		fake := postCommitInstallFake(t, nil)
		rows := postCommitInstallRows(t, func(_ string, n int) (bool, error) { return n > 0, nil })
		msgid := postCommitMsgID("again")
		art := postCommitArticle(t, proc, msgid, []string{postCommitGroupA}, true)
		code, err := proc.ProcessPostedArticle(art, nil, 5*time.Second)
		calls := fake.snapshot()
		postCommitSkipIfCommitted(t, proc, calls, postCommitGroupA, msgid)
		if code != history.CasePass || err != nil {
			t.Fatalf("ProcessPostedArticle = %d, %v; want CasePass, nil", code, err)
		}
		if len(calls) != 2 || rows.count(postCommitGroupA) != 2 {
			t.Fatalf("barrier called %d times, row looked up %d times; want 2 and 2", len(calls), rows.count(postCommitGroupA))
		}
		if calls[1].d > calls[0].d {
			t.Errorf("second round got %v, more than the first %v (the deadline is shared)", calls[1].d, calls[0].d)
		}
	})

	t.Run("lookup error", func(t *testing.T) {
		fake := postCommitInstallFake(t, nil)
		rows := postCommitInstallRows(t, func(_ string, n int) (bool, error) {
			if n == 0 {
				return false, errors.New("lookup failed")
			}
			return true, nil
		})
		logs := postCommitCaptureLog(t)
		msgid := postCommitMsgID("lookuperr")
		art := postCommitArticle(t, proc, msgid, []string{postCommitGroupA}, true)
		code, err := proc.ProcessPostedArticle(art, nil, 5*time.Second)
		postCommitSkipIfCommitted(t, proc, fake.snapshot(), postCommitGroupA, msgid)
		if code != history.CasePass || err != nil {
			t.Fatalf("ProcessPostedArticle = %d, %v; want CasePass, nil", code, err)
		}
		if n := rows.count(postCommitGroupA); n != 2 {
			t.Errorf("row looked up %d times, want 2 (an error counts as absent)", n)
		}
		if n := strings.Count(logs.String(), "[INTAKE] POST "+msgid+": row lookup in"); n != 1 {
			t.Errorf("log has %d lookup error lines, want 1", n)
		}
	})

	for name, ret := range map[string]error{
		"timeout": fmt.Errorf("%w: '%s' after 5s (0 of 1 articles settled)", database.ErrBatchWaitTimeout, postCommitGroupA),
		"stopped": fmt.Errorf("%w: '%s' (0 of 1 articles settled)", database.ErrBatchWaitStopped, postCommitGroupA),
	} {
		t.Run(name, func(t *testing.T) {
			fake := postCommitInstallFake(t, ret)
			rows := postCommitInstallRows(t, func(string, int) (bool, error) { return false, nil })
			logs := postCommitCaptureLog(t)
			msgid := postCommitMsgID(name)
			art := postCommitArticle(t, proc, msgid, []string{postCommitGroupA}, true)
			stop := make(chan struct{})
			code, err := proc.ProcessPostedArticle(art, stop, 5*time.Second)
			calls := fake.snapshot()
			postCommitSkipIfCommitted(t, proc, calls, postCommitGroupA, msgid)
			if code != history.CasePass || err != nil {
				t.Fatalf("ProcessPostedArticle = %d, %v; want CasePass, nil", code, err)
			}
			if len(calls) != 1 {
				t.Fatalf("barrier called %d times, want 1", len(calls))
			}
			if calls[0].stop != (<-chan struct{})(stop) {
				t.Errorf("barrier got another stop channel than the caller's")
			}
			if n := rows.count(postCommitGroupA); n != 0 {
				t.Errorf("row looked up %d times after %s, want 0", n, name)
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
		rows := postCommitInstallRows(t, func(string, int) (bool, error) { return false, errors.New("lookup must not be called") })
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
		if n := rows.count(postCommitGroupA); n != 0 {
			t.Errorf("row looked up %d times with timeout <= 0, want 0", n)
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

	t.Run("captured groups only", func(t *testing.T) {
		// the remote name has no row in the main DB: ProcessArticle does not capture it, so
		// it is not waited for; each captured group once, with a non-increasing remaining time
		fake := postCommitInstallFake(t, nil)
		rows := postCommitInstallRows(t, func(string, int) (bool, error) { return true, nil })
		groups := []string{postCommitGroupA, postCommitRemote, postCommitGroupB}
		const timeout = 7 * time.Second
		msgid := postCommitMsgID("captured")
		art := postCommitArticle(t, proc, msgid, groups, true)
		code, err := proc.ProcessPostedArticle(art, nil, timeout)
		if code != history.CasePass || err != nil {
			t.Fatalf("ProcessPostedArticle = %d, %v; want CasePass, nil", code, err)
		}
		calls := fake.snapshot()
		if n := postCommitCallsFor(proc, calls, postCommitRemote) + rows.count(postCommitRemote); n != 0 {
			t.Errorf("remote group waited for or looked up %d times, want 0", n)
		}
		for _, g := range []string{postCommitGroupA, postCommitGroupB} {
			n := postCommitCallsFor(proc, calls, g)
			// a group the batch writer committed before the snapshot is not waited for
			if n == 0 && !postCommitStored(t, proc, g, msgid) {
				t.Errorf("captured group '%s' not waited for and not stored", g)
			}
			if n > 1 || rows.count(g) != n {
				t.Errorf("'%s': %d barrier calls and %d row lookups, want at most 1 and equal", g, n, rows.count(g))
			}
		}
		prev := timeout
		for i, c := range calls {
			if c.d <= 0 || c.d > prev {
				t.Errorf("call %d: remaining timeout %v, want in (0, %v]", i, c.d, prev)
			}
			prev = c.d
		}
	})
}
