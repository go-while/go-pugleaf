package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
)

func TestMboxFullHistoryStopsBeforeNewCapture(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := t.TempDir()
	oldEnable := history.ENABLE_HISTORY
	history.ENABLE_HISTORY = true
	t.Cleanup(func() { history.ENABLE_HISTORY = oldEnable })
	// All four IDs route to the same file.
	var ids []string
	for i := 0; len(ids) < 4; i++ {
		id := fmt.Sprintf("<hold-%d@mbox.test>", i)
		if strings.HasPrefix(history.ComputeMessageIDHash(id), "0") {
			ids = append(ids, id)
		}
	}
	miWithDB(t, data, func(db *database.Database) {
		proc, err := miNewProcessorWithBatch(db, 7, 1)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := proc.Close(); err != nil {
				t.Errorf("processor close: %v", err)
			}
		}()

		// Hold the shard until both article batches finish with history pending.
		shard, err := sql.Open("sqlite3", filepath.Join(data, "history", "hashdb_0.sqlite3")+"?_txlock=immediate")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := shard.Close(); err != nil {
				t.Errorf("close held history shard: %v", err)
			}
		}()
		var readers sync.WaitGroup
		defer readers.Wait()
		hold, err := shard.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := hold.Rollback(); err != nil {
				t.Errorf("release held history shard: %v", err)
			}
		}()

		stop := make(chan struct{})
		stopReaders := sync.OnceFunc(func() { close(stop) })
		defer stopReaders()
		sink := newProcessorSink(db, proc, stop)
		put := func(group, id string) (inputSink, bool, error) {
			in := sink.beginInput()
			stored, _, err := in.Handle(id, func() (*models.Article, error) {
				return buildArticle(id, []string{
					"Path: test.invalid!not-for-mail",
					"From: Tester <tester@example.invalid>",
					"Newsgroups: " + group,
					"Subject: History backpressure test",
					"Date: Tue, 24 Jun 2003 19:30:12 +0200",
					"Message-ID: " + id,
				}, []string{"body"})
			}, group)
			return in, stored, err
		}
		for i, group := range []string{miAlpha, miBeta} {
			in, stored, err := put(group, ids[i])
			if err != nil || !stored {
				t.Fatalf("submit %s: stored=%t err=%v", group, stored, err)
			}
			if err := in.endInput(testWait); err != nil {
				t.Fatal(err)
			}
		}
		// The shutdown barrier includes history. Inspect these two group
		// workers directly to check that they are free before history flushes.
		timeout := time.NewTimer(testWait)
		defer timeout.Stop()
		poll := time.NewTicker(time.Millisecond)
		defer poll.Stop()
		for _, group := range []string{miAlpha, miBeta} {
			task := db.Batch.GetOrCreateTasksMapKey(group)
			for {
				task.Mux.RLock()
				idle := !task.BATCHprocessing && len(task.BATCHchan) == 0
				task.Mux.RUnlock()
				if idle {
					break
				}
				select {
				case <-poll.C:
				case <-timeout.C:
					t.Fatalf("article batch %s waited for the locked history shard", group)
				}
			}
		}
		if stats := proc.History.GetStats(); stats.Pending != 2 || stats.TotalCommitted != 0 {
			t.Fatalf("history under lock = %+v, want two pending writes", stats)
		}

		waitingGroups := []string{"t.mbox.queue.stopped.third", "t.mbox.queue.stopped.fourth"}
		type result struct {
			group, id string
			stored    bool
			err       error
		}
		results := make(chan result, len(waitingGroups))
		for i, group := range waitingGroups {
			id := ids[2+i]
			readers.Go(func() {
				in, stored, err := put(group, id)
				results <- result{group, id, stored, errors.Join(err, in.endInput(testWait))}
			})
		}
		stacks := make([]byte, 512<<10)
		for {
			// The old writer fits one active write and two buffered writes.
			// A fourth pending write with a busy group proves it blocked a
			// group worker. On the fix, both readers wait before capture.
			if pending := proc.History.GetStats().Pending; pending >= 4 {
				for _, group := range waitingGroups {
					task := db.Batch.GetOrCreateTasksMapKey(group)
					task.Mux.RLock()
					busy := task.BATCHprocessing
					task.Mux.RUnlock()
					if busy {
						t.Fatalf("full history queue held group worker %s (%d pending writes)", group, pending)
					}
				}
			}
			n := runtime.Stack(stacks, true)
			if bytes.Count(stacks[:n], []byte("(*History).acquireAdmission(")) == len(waitingGroups) {
				break
			}
			select {
			case <-poll.C:
			case <-timeout.C:
				t.Fatal("readers did not reach the history admission wait")
			}
		}
		stopReaders()
		for range waitingGroups {
			select {
			case got := <-results:
				if !errors.Is(got.err, errStopped) || got.stored || miStoredIn(t, db, got.group, got.id) {
					t.Errorf("stopped reader submitted %s: stored=%t err=%v", got.id, got.stored, got.err)
				}
			case <-timeout.C:
				t.Fatal("reader stayed blocked after stop")
			}
		}
	})
	miWithHistory(t, data, func(h *history.History) {
		for i, id := range ids {
			want := 1
			if i >= 2 {
				want = 0
			}
			got, err := h.LookupLocations(id)
			if err != nil || len(got) != want {
				t.Errorf("history %s = %v, %v, want %d locations", id, got, err, want)
			}
		}
	})
}

func TestMboxImportHistoryWithSmallBatchQueue(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	newProcessor = func(db *database.Database, hashLen int) (*processor.Processor, error) {
		return miNewProcessorWithBatch(db, hashLen, 64)
	}
	data, inputs := t.TempDir(), t.TempDir()
	const perGroup = 1050
	groups := []string{"t.mbox.queue.alpha", "t.mbox.queue.beta", "t.mbox.queue.gamma", "t.mbox.queue.delta"}
	args := []string{"-data", data, "-parallel", "4", "-max-batch", "1000", "-max-queue", "1280", "-mark-done=false"}
	messageID := func(group string, i int) string { return fmt.Sprintf("<%d@%s.test>", i, group) }
	for _, group := range groups {
		var mbox strings.Builder
		for i := range perGroup {
			mbox.WriteString(miArticle(messageID(group, i), group, "Import with history"))
		}
		args = append(args, miWrite(t, inputs, group+".mbox", mbox.String()))
	}
	code, summary := miRun(t, args...)
	if code != 0 || summary == nil || summary.Total.Stored != len(groups)*perGroup {
		t.Fatalf("small queue import exited %d with summary %+v", code, summary)
	}
	locations := make(map[string]history.Location, 2*len(groups))
	miWithDB(t, data, func(db *database.Database) {
		for _, name := range groups {
			group, err := db.GetIntakeGroup(name)
			if err != nil {
				t.Fatal(err)
			}
			groupDB, err := db.GetGroupDB(name)
			if err != nil {
				t.Fatal(err)
			}
			defer groupDB.Return()
			for _, i := range []int{0, perGroup - 1} {
				id := messageID(name, i)
				want := history.Location{GroupID: group.ID}
				if err := database.RetryableQueryRowScan(groupDB.DB, "SELECT article_num FROM articles WHERE message_id = ?", []interface{}{id}, &want.ArticleNum); err != nil {
					t.Fatal(err)
				}
				locations[id] = want
			}
		}
	})
	miWithHistory(t, data, func(h *history.History) {
		for id, want := range locations {
			got, err := h.LookupLocations(id)
			if err != nil || len(got) != 1 || got[0] != want {
				t.Errorf("history %s = %v, %v, want %v", id, got, err, want)
			}
		}
	})
}
