package history

import (
	"context"
	"fmt"
)

// EnableArticleAdmission lets an importer reserve queue capacity before
// handing articles to the database. Call it before submitting history
// work. The budget covers reservations, queued operations and the active flush.
func (h *History) EnableArticleAdmission() {
	if h.Enabled() && h.admission == nil {
		h.admission = make(chan struct{}, cap(h.opChan))
	}
}

// ArticleReservation is one history slot owned by the caller until it is
// canceled or handed to the batch that will commit the article.
type ArticleReservation struct {
	history *History
}

// ReserveArticle waits for capacity outside the article writer. A disabled
// index needs no slot. The caller must publish or cancel every returned slot.
func (h *History) ReserveArticle(stop <-chan struct{}) (*ArticleReservation, error) {
	if !h.Enabled() || h.admission == nil {
		return nil, nil
	}
	if err := h.acquireAdmission(stop); err != nil {
		return nil, err
	}
	return &ArticleReservation{history: h}, nil
}

// Publish queues the actual committed location using the reserved capacity.
// The history writer releases the slot after the flush has finished.
func (r *ArticleReservation) Publish(messageID string, groupID, articleNum int64) {
	h := r.history
	if h == nil {
		return
	}
	if messageID == "" || groupID <= 0 || articleNum <= 0 {
		h.recordAdmissionError(fmt.Errorf("missing history location for committed article %q (group=%d, number=%d)", messageID, groupID, articleNum))
		r.Cancel()
		return
	}
	r.history = nil
	if !h.queueOp(historyOp{op: opAdd, messageID: messageID, groupID: groupID, articleNum: articleNum}) {
		h.recordAdmissionError(fmt.Errorf("queue the history location for %q: %w", messageID, ErrHistoryClosed))
	}
}

// Cancel releases an unused reservation. Only its owner may call either
// Cancel or Publish.
func (r *ArticleReservation) Cancel() {
	if r != nil && r.history != nil {
		r.history.releaseAdmission(1)
		r.history = nil
	}
}

func (h *History) acquireAdmission(stop <-chan struct{}) error {
	if h.admission == nil {
		return nil
	}
	select {
	case <-stop:
		return context.Canceled
	default:
	}
	select {
	case <-stop:
		return context.Canceled
	case <-h.closeChan:
		return ErrHistoryClosed
	case h.admission <- struct{}{}:
		return nil
	}
}

func (h *History) releaseAdmission(count int) {
	if h.admission != nil {
		for range count {
			<-h.admission
		}
	}
}

func (h *History) recordAdmissionError(err error) {
	if h.admission != nil {
		h.admissionErr.CompareAndSwap(nil, &err)
	}
}
