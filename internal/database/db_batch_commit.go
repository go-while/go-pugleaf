package database

import (
	"errors"
	"fmt"
	"time"
)

// Commit barrier of the batch writer (A-tools-5b, plan followup-leftovers D-B).
//
// The fetcher hands articles to the batch writer and used to record its progress right away,
// so a batch that was dropped later (group database closed, insert still failing after the
// shutdown grace) left progress past articles that were never stored. The barrier lets a caller
// wait until every article it handed over has been committed or dropped:
//
//   - NoteCaptured counts an article per target group when it is handed to the batch writer
//     (ProcessArticle calls it synchronously, before the capture goroutine starts).
//   - settle counts the articles of a batch once it was committed (batchInsertOverviews
//     succeeded) or dropped (every drop path of the batch writer).
//   - CommitMark takes the dropped baseline of a group at the start of a run, WaitCommitted
//     waits until the group's settled count reaches its captured count and reports a drop
//     since the mark as ErrBatchDropped.
//
// The counters live beside SQ3batch.groupNames, never in TasksMap: expireIdleTasks deletes
// idle tasks, and a task created again must not start the counts from zero. commitMu is a
// leaf lock: nothing else is locked while it is held.

// ErrBatchDropped: the batch writer dropped articles of the group since the CommitMark.
var ErrBatchDropped = errors.New("batch writer dropped articles")

// ErrBatchWaitStopped: WaitCommitted returned because the stop channel or the database's
// background stop channel was closed.
var ErrBatchWaitStopped = errors.New("stopped while waiting for the batch writer")

// ErrBatchWaitTimeout: the articles were not settled within the timeout of WaitCommitted.
var ErrBatchWaitTimeout = errors.New("timed out waiting for the batch writer")

// commitFlushEvery is how often a waiting WaitCommitted asks the orchestrator again to flush
// the pending batches (flushReq), so a waiter does not sit out BatchInterval.
const commitFlushEvery = 125 * time.Millisecond

// commitCounters are the barrier counts of one newsgroup (commitMu).
type commitCounters struct {
	captured uint64        // articles handed to the batch writer (NoteCaptured)
	settled  uint64        // articles whose batch was committed or dropped (settle)
	dropped  uint64        // the part of settled that was dropped
	changed  chan struct{} // closed and replaced by every settle; nil until a waiter needs it
}

// CommitMark is the state of one newsgroup at the start of a run (CommitMark).
type CommitMark struct {
	group   string
	dropped uint64
}

// countersLocked returns the counters of group, creating them. sq.commitMu must be held.
func (sq *SQ3batch) countersLocked(group string) *commitCounters {
	if sq.commits == nil {
		sq.commits = make(map[string]*commitCounters)
	}
	c, ok := sq.commits[group]
	if !ok {
		c = &commitCounters{}
		sq.commits[group] = c
	}
	return c
}

// NoteCaptured counts one article for every group in groups as handed to the batch writer.
// It must be called before the article can reach the batch writer, so a waiter that starts
// after the caller returned sees it. A nil pointer is skipped; a nil SQ3batch does nothing.
func (sq *SQ3batch) NoteCaptured(groups []*string) {
	if sq == nil || len(groups) == 0 {
		return
	}
	sq.commitMu.Lock()
	defer sq.commitMu.Unlock()
	for _, g := range groups {
		if g == nil {
			continue
		}
		sq.countersLocked(*g).captured++
	}
}

// settle counts n articles of group as committed (dropped false) or dropped (dropped true) and
// wakes the waiters of the group. A settle without a matching NoteCaptured (a test, or a
// capture path that does not count) raises captured with it, so settled never exceeds captured.
func (sq *SQ3batch) settle(group string, n int, dropped bool) {
	if sq == nil || n <= 0 {
		return
	}
	sq.commitMu.Lock()
	defer sq.commitMu.Unlock()
	c := sq.countersLocked(group)
	c.settled += uint64(n)
	if dropped {
		c.dropped += uint64(n)
	}
	if c.settled > c.captured {
		c.captured = c.settled
	}
	if c.changed != nil {
		close(c.changed)
		c.changed = nil
	}
}

// settleArticles settles the articles of one batch for group (nil entries are not counted).
func (sq *SQ3batch) settleArticles(group *string, n int, dropped bool) {
	if group == nil {
		return
	}
	sq.settle(*group, n, dropped)
}

// CommitMark returns the mark of newsgroup for a later WaitCommitted: drops before the mark
// are not reported.
func (sq *SQ3batch) CommitMark(newsgroup string) CommitMark {
	if sq == nil {
		return CommitMark{group: newsgroup}
	}
	sq.commitMu.Lock()
	defer sq.commitMu.Unlock()
	return CommitMark{group: newsgroup, dropped: sq.countersLocked(newsgroup).dropped}
}

// WaitCommitted waits until every article counted by NoteCaptured for the group of m before
// this call has been settled. It returns nil when all of them were committed, an error
// wrapping ErrBatchDropped as soon as an article of the group was dropped since the mark,
// ErrBatchWaitStopped when stop or the database's background stop channel is closed and
// ErrBatchWaitTimeout after timeout (<= 0: no timeout).
//
// The count is per group, not per caller: other captures for the same group that settle
// first can satisfy it (the fetcher runs one download per group at a time). While it waits
// it asks the orchestrator to flush pending batches every commitFlushEvery (flushReq).
func (sq *SQ3batch) WaitCommitted(m CommitMark, stop <-chan struct{}, timeout time.Duration) error {
	if sq == nil {
		return nil
	}
	sq.commitMu.Lock()
	c := sq.countersLocked(m.group)
	target := c.captured
	sq.commitMu.Unlock()

	var timeoutC <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutC = timer.C
	}
	var bgStop <-chan struct{}
	if sq.db != nil {
		bgStop = sq.db.backgroundStop()
	}
	var ticker *time.Ticker
	for {
		sq.commitMu.Lock()
		settled, dropped := c.settled, c.dropped
		var changed chan struct{}
		if settled < target && dropped <= m.dropped {
			if c.changed == nil {
				c.changed = make(chan struct{})
			}
			changed = c.changed
		}
		sq.commitMu.Unlock()

		if dropped > m.dropped {
			return fmt.Errorf("%w: %d articles of '%s' dropped since the mark", ErrBatchDropped, dropped-m.dropped, m.group)
		}
		if settled >= target {
			return nil
		}
		sq.flushReq.Store(true)
		if ticker == nil {
			ticker = time.NewTicker(commitFlushEvery)
			defer ticker.Stop()
		}
		select {
		case <-changed:
		case <-ticker.C:
		case <-stop:
			return fmt.Errorf("%w: '%s' (%d of %d articles settled)", ErrBatchWaitStopped, m.group, settled, target)
		case <-bgStop:
			return fmt.Errorf("%w: '%s' database stopped (%d of %d articles settled)", ErrBatchWaitStopped, m.group, settled, target)
		case <-timeoutC:
			return fmt.Errorf("%w: '%s' after %v (%d of %d articles settled)", ErrBatchWaitTimeout, m.group, timeout, settled, target)
		}
	}
}
