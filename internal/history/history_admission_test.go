package history

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
)

func TestArticleAdmissionKeepsRoomForCommittedBatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &History{enabled: true, closeChan: make(chan struct{}), opChan: make(chan historyOp, 2)}
		h.EnableArticleAdmission()

		first, err := h.ReserveArticle(nil)
		if err != nil {
			t.Fatal(err)
		}
		second, err := h.ReserveArticle(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(h.opChan) != 0 || h.pending.Load() != 0 {
			t.Fatal("admission published history before the articles committed")
		}

		// Park an ordinary queue producer and an import reader while both
		// slots are reserved. Neither may take a slot from an admitted batch.
		rejected := make(chan struct{})
		go func() {
			h.AddReject("<reject@admission.test>")
			close(rejected)
		}()
		stop := make(chan struct{})
		stopped := make(chan error, 1)
		go func() {
			slot, err := h.ReserveArticle(stop)
			slot.Cancel()
			stopped <- err
		}()
		synctest.Wait()
		select {
		case <-rejected:
			t.Fatal("ordinary history producer bypassed the reserved capacity")
		default:
		}
		select {
		case err := <-stopped:
			t.Fatalf("third article passed admission while full: %v", err)
		default:
		}

		first.Publish("<first@admission.test>", 7, 100)
		second.Publish("<second@admission.test>", 9, 200)
		for _, want := range []historyOp{
			{op: opAdd, messageID: "<first@admission.test>", groupID: 7, articleNum: 100},
			{op: opAdd, messageID: "<second@admission.test>", groupID: 9, articleNum: 200},
		} {
			if got := <-h.opChan; got != want {
				t.Fatalf("history op = %+v, want %+v", got, want)
			}
		}
		synctest.Wait()
		select {
		case <-rejected:
			t.Fatal("dequeue freed a slot while writes were still in progress")
		default:
		}
		close(stop)
		if err := <-stopped; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting reader stopped with %v", err)
		}
		h.releaseAdmission(2)
		<-rejected
		if op := <-h.opChan; op.op != opReject {
			t.Fatalf("first operation after capacity returned = %+v", op)
		}
	})
}

func TestArticleAdmissionReturnsCanceledSlots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &History{enabled: true, closeChan: make(chan struct{}), opChan: make(chan historyOp, 1)}
		h.EnableArticleAdmission()
		slot, err := h.ReserveArticle(nil)
		if err != nil {
			t.Fatal(err)
		}
		slot.Cancel()
		slot.Cancel()
		slot, err = h.ReserveArticle(nil)
		if err != nil {
			t.Fatal(err)
		}
		slot.Cancel()
		if len(h.opChan) != 0 || h.pending.Load() != 0 {
			t.Fatal("canceled slot produced history work")
		}
	})
}

func TestArticleAdmissionReturnsSlotsOnFlushFailure(t *testing.T) {
	histTestEnable(t, true)
	cfg := histTestConfig(filepath.Join(t.TempDir(), "history"))
	cfg.BatchSize = 1
	h, err := NewHistory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.EnableArticleAdmission()
	t.Cleanup(func() { _ = h.Close() })

	const id = "<failed@admission.test>"
	shard, _, err := h.routeHash(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.DBPools[shard].DB.Close(); err != nil {
		t.Fatal(err)
	}
	slot, err := h.ReserveArticle(nil)
	if err != nil {
		t.Fatal(err)
	}
	slot.Publish(id, 1, 1)
	if err := h.Close(); err == nil || !strings.Contains(err.Error(), "write history database") {
		t.Fatalf("Close = %v, want a history write error", err)
	}
	if got := len(h.admission); got != 0 {
		t.Fatalf("%d history slots still reserved after failed flush", got)
	}
}
