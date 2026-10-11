package processor

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// Tests for the per-run return queue of DownloadArticles (A-intake-11, plan nntp-intake-injection S5).

const intakeDLTestWait = time.Second

// TestIntakeDLHelper carries the helpers of these tests as methods (vet rejects Test* helper funcs).
type TestIntakeDLHelper struct{ t *testing.T }

// waitClosed fails the test unless ch is closed within intakeDLTestWait.
func (h TestIntakeDLHelper) waitClosed(ch <-chan struct{}, what string) {
	h.t.Helper()
	select {
	case <-ch:
	case <-time.After(intakeDLTestWait):
		h.t.Fatalf("%s: not done within %v", what, intakeDLTestWait)
	}
}

// TestIntakeDLFeeder runs feedDownloadRun in a goroutine; done is closed when it returned.
type TestIntakeDLFeeder struct {
	xhdr     chan nntp.HeaderLine
	runDone  chan struct{}
	stopped  chan struct{}
	done     chan struct{}
	handed   atomic.Int64
	prepared atomic.Int64
	queued   int64 // valid after done
	complete bool  // valid after done
}

// startFeeder runs feedDownloadRun on fresh channels; the caller must close f.xhdr.
func (h TestIntakeDLHelper) startFeeder(xhdrCap int, returnQ, getQ chan *BatchItem, prepare func(string) *BatchItem) *TestIntakeDLFeeder {
	f := &TestIntakeDLFeeder{
		xhdr:    make(chan nntp.HeaderLine, xhdrCap),
		runDone: make(chan struct{}),
		stopped: make(chan struct{}),
		done:    make(chan struct{}),
	}
	counted := func(id string) *BatchItem {
		f.prepared.Add(1)
		return prepare(id)
	}
	go func() {
		defer close(f.done)
		f.queued, f.complete = feedDownloadRun(f.xhdr, f.runDone, f.stopped, returnQ, getQ, counted, &f.handed)
	}()
	return f
}

func TestIntakeDLRunQueuesAreFresh(t *testing.T) {
	a := newDownloadRun()
	b := newDownloadRun()
	if a == nil || b == nil || a.ReturnQ == nil || b.ReturnQ == nil {
		t.Fatalf("newDownloadRun returned nil batch or queue: %v %v", a, b)
	}
	if a == b || a.ReturnQ == b.ReturnQ {
		t.Fatalf("two runs share a queue")
	}
	if cap(a.ReturnQ) != int(MaxBatchSize) || cap(b.ReturnQ) != int(MaxBatchSize) {
		t.Fatalf("cap = %d/%d, want MaxBatchSize %d", cap(a.ReturnQ), cap(b.ReturnQ), MaxBatchSize)
	}
	// the per-run queue is not registered in the deprecated per-group map
	Batch.Mutex.RLock()
	for name, gb := range Batch.GroupQueues {
		if gb == a || gb == b {
			Batch.Mutex.RUnlock()
			t.Fatalf("run queue registered in Batch.GroupQueues[%q]", name)
		}
	}
	Batch.Mutex.RUnlock()
}

func TestIntakeDLFeederRoutesItems(t *testing.T) {
	h := TestIntakeDLHelper{t}
	returnQ := make(chan *BatchItem, 4)
	getQ := make(chan *BatchItem, 4)
	prepare := func(id string) *BatchItem {
		switch id {
		case "<dup@test.invalid>":
			return &BatchItem{Error: errIsDuplicateError}
		case "<reuse@test.invalid>":
			return &BatchItem{Article: &models.Article{MessageID: id}, ReturnQ: returnQ}
		case "<skip@test.invalid>":
			return nil
		}
		return &BatchItem{ReturnQ: returnQ}
	}
	f := h.startFeeder(8, returnQ, getQ, prepare)
	for _, id := range []string{"<dup@test.invalid>", "<reuse@test.invalid>", "<skip@test.invalid>", "<get@test.invalid>"} {
		f.xhdr <- nntp.HeaderLine{Value: id}
	}
	close(f.xhdr)
	h.waitClosed(f.done, "feeder")
	h.waitClosed(f.stopped, "stopped")
	if !f.complete || f.queued != 2 || f.handed.Load() != 3 || f.prepared.Load() != 4 {
		t.Fatalf("complete=%v queued=%d handed=%d prepared=%d, want true/2/3/4", f.complete, f.queued, f.handed.Load(), f.prepared.Load())
	}
	if len(returnQ) != 2 || len(getQ) != 1 {
		t.Fatalf("returnQ=%d getQ=%d, want 2/1", len(returnQ), len(getQ))
	}
	if it := <-returnQ; !errors.Is(it.Error, errIsDuplicateError) {
		t.Fatalf("first returnQ item %+v, want the duplicate", it)
	}
	if it := <-returnQ; it.Article == nil {
		t.Fatalf("second returnQ item %+v, want the reused article", it)
	}
	if it := <-getQ; it.Article != nil || it.Error != nil {
		t.Fatalf("getQ item %+v, want a download request", it)
	}
}

func TestIntakeDLFeederDrainsAfterDone(t *testing.T) {
	t.Run("blocked-send", func(t *testing.T) {
		h := TestIntakeDLHelper{t}
		const xhdrCap = 4
		returnQ := make(chan *BatchItem, 1)
		getQ := make(chan *BatchItem) // nobody receives: the feeder blocks on its first send
		f := h.startFeeder(xhdrCap, returnQ, getQ, func(string) *BatchItem { return &BatchItem{ReturnQ: returnQ} })
		f.xhdr <- nntp.HeaderLine{Value: "<t7-intakedl-blocked-0@test.invalid>"}
		deadline := time.Now().Add(intakeDLTestWait)
		for f.prepared.Load() < 1 {
			if time.Now().After(deadline) {
				t.Fatalf("feeder never called prepare")
			}
			time.Sleep(time.Millisecond)
		}
		close(f.runDone)
		h.waitClosed(f.stopped, "stopped after runDone")
		// after runDone the feeder must keep consuming xhdr (the XHDR reader blocks on a full channel):
		// 3*cap sends only complete when it does
		for i := 1; i <= 3*xhdrCap; i++ {
			select {
			case f.xhdr <- nntp.HeaderLine{Value: fmt.Sprintf("<t7-intakedl-blocked-%d@test.invalid>", i)}:
			case <-time.After(intakeDLTestWait):
				t.Fatalf("xhdr send %d blocked: feeder stopped consuming after runDone", i)
			}
		}
		select {
		case <-f.done:
			t.Fatalf("feeder returned before xhdr was closed")
		default:
		}
		close(f.xhdr)
		h.waitClosed(f.done, "feeder after xhdr close")
		if f.complete || f.queued != 0 || f.handed.Load() != 0 || f.prepared.Load() != 1 {
			t.Fatalf("complete=%v queued=%d handed=%d prepared=%d, want false/0/0/1", f.complete, f.queued, f.handed.Load(), f.prepared.Load())
		}
		if len(returnQ) != 0 {
			t.Fatalf("returnQ got %d items after runDone", len(returnQ))
		}
	})
	t.Run("idle-receive", func(t *testing.T) {
		h := TestIntakeDLHelper{t}
		returnQ := make(chan *BatchItem, 4)
		getQ := make(chan *BatchItem, 4)
		f := h.startFeeder(4, returnQ, getQ, func(string) *BatchItem { return &BatchItem{ReturnQ: returnQ} })
		f.xhdr <- nntp.HeaderLine{Value: "<t7-intakedl-idle-0@test.invalid>"}
		deadline := time.Now().Add(intakeDLTestWait)
		for f.handed.Load() < 1 {
			if time.Now().After(deadline) {
				t.Fatalf("feeder never handed the first item")
			}
			time.Sleep(time.Millisecond)
		}
		// the feeder now waits on an empty xhdr: runDone alone must stop queueing
		close(f.runDone)
		h.waitClosed(f.stopped, "stopped while idle")
		f.xhdr <- nntp.HeaderLine{Value: "<t7-intakedl-idle-1@test.invalid>"}
		close(f.xhdr)
		h.waitClosed(f.done, "feeder after xhdr close")
		if f.complete || f.queued != 1 || f.handed.Load() != 1 || f.prepared.Load() != 1 {
			t.Fatalf("complete=%v queued=%d handed=%d prepared=%d, want false/1/1/1", f.complete, f.queued, f.handed.Load(), f.prepared.Load())
		}
		if len(getQ) != 1 {
			t.Fatalf("getQ has %d items, want 1", len(getQ))
		}
	})
}

func TestIntakeDLDrainLateResults(t *testing.T) {
	t.Run("count-reached", func(t *testing.T) {
		h := TestIntakeDLHelper{t}
		const id = "<t7-intakedl-drain-1@test.invalid>"
		// NewMsgIdItemCache returns the process-wide singleton (creating it once), which drainLateResults uses
		msgIdItem := history.NewMsgIdItemCache().GetORCreate(id)
		msgIdItem.Mux.Lock()
		msgIdItem.Response = history.CaseLock
		msgIdItem.CachedEntryExpires = time.Now().Add(15 * time.Second)
		msgID := &msgIdItem.MessageId
		msgIdItem.Mux.Unlock()

		q := make(chan *BatchItem, 4)
		shutdown := make(chan struct{})
		defer close(shutdown)
		done := make(chan struct{})
		go func() {
			defer close(done)
			drainLateResults(q, 3, shutdown)
		}()
		late := []*BatchItem{
			{MessageID: msgID, Error: nntp.ErrArticleNotFound},
			{Error: errIsDuplicateError},
			nil,
		}
		for _, it := range late {
			q <- it
		}
		h.waitClosed(done, "drainLateResults with 3 of 3 results")
		if late[0].MessageID != nil || late[0].Error != nil {
			t.Fatalf("drained item not cleared: %+v", late[0])
		}
		msgIdItem.Mux.RLock()
		resp, exp := msgIdItem.Response, msgIdItem.CachedEntryExpires
		msgIdItem.Mux.RUnlock()
		if resp != history.CaseError {
			t.Fatalf("msgid item state %#x, want CaseError", resp)
		}
		if ttl := time.Until(exp); ttl <= 0 || ttl > 5*time.Second {
			t.Fatalf("msgid item TTL %v, want (0, 5s]", ttl)
		}
		// a result beyond the outstanding count stays in the queue
		q <- &BatchItem{Error: errIsDuplicateError}
		if len(q) != 1 {
			t.Fatalf("queue len %d, want 1: drain consumed more than outstanding", len(q))
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		h := TestIntakeDLHelper{t}
		q := make(chan *BatchItem, 4)
		shutdown := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			drainLateResults(q, 3, shutdown)
		}()
		q <- &BatchItem{Error: errIsDuplicateError}
		deadline := time.Now().Add(intakeDLTestWait)
		for len(q) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("drain did not receive the first item")
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case <-done:
			t.Fatalf("drain returned with 2 results outstanding and no shutdown")
		default:
		}
		close(shutdown)
		h.waitClosed(done, "drainLateResults after shutdown")
	})
	t.Run("nothing-outstanding", func(t *testing.T) {
		h := TestIntakeDLHelper{t}
		done := make(chan struct{})
		go func() {
			defer close(done)
			drainLateResults(make(chan *BatchItem), 0, nil)
		}()
		h.waitClosed(done, "drainLateResults with 0 outstanding")
	})
}
