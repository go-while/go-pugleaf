package processor

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// wiringTestHistory creates an enabled history index in a temp dir; closed via t.Cleanup
func wiringTestHistory(t *testing.T) *history.History {
	t.Helper()
	old := history.ENABLE_HISTORY
	history.ENABLE_HISTORY = true
	t.Cleanup(func() { history.ENABLE_HISTORY = old })

	cfg := history.DefaultConfig()
	cfg.HistoryDir = filepath.Join(t.TempDir(), "history")
	cfg.BatchTimeout = 50
	cfg.MaxConnections = 4
	var wg sync.WaitGroup
	h, err := history.NewHistory(cfg, &wg)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() {
		h.Close()
		wg.Wait()
	})
	return h
}

// wiringTestAddAndWait adds messageID to history and waits until it is committed
func wiringTestAddAndWait(t *testing.T, h *history.History, messageID string, groupID int64) {
	t.Helper()
	h.AddArticle(messageID, groupID)
	deadline := time.Now().Add(20 * time.Second)
	for {
		exists, err := h.Exists(messageID)
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if exists {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("history add of %s not committed", messageID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func wiringTestSetState(messageID string, response int) *history.MessageIdItem {
	item := history.NewMsgIdItemCache().GetORCreate(messageID)
	item.Mux.Lock()
	item.Response = response
	item.CachedEntryExpires = time.Now().Add(time.Minute)
	item.Mux.Unlock()
	return item
}

func wiringTestState(item *history.MessageIdItem) int {
	item.Mux.RLock()
	defer item.Mux.RUnlock()
	return item.Response
}

func TestWiringCheckMessageID(t *testing.T) {
	history.NewMsgIdItemCache()
	proc := &Processor{} // history nil = disabled

	if got := proc.CheckMessageID("<wiring-check-new@example.org>"); got != history.CasePass {
		t.Fatalf("unknown id: got %#x, want CasePass", got)
	}
	cases := []struct {
		state int
		want  int
	}{
		{history.CaseLock, history.CaseRetry},
		{history.CaseWrite, history.CaseRetry},
		{history.CaseDupes, history.CaseDupes},
		{history.CaseError, history.CasePass},
		{history.CasePass, history.CasePass},
	}
	for i, tc := range cases {
		id := "<wiring-check-state-" + string(rune('a'+i)) + "@example.org>"
		item := wiringTestSetState(id, tc.state)
		if got := proc.CheckMessageID(id); got != tc.want {
			t.Fatalf("state %#x: got %#x, want %#x", tc.state, got, tc.want)
		}
		if wiringTestState(item) != tc.state {
			t.Fatalf("CheckMessageID changed the item state")
		}
	}

	h := wiringTestHistory(t)
	proc.History = h
	const known = "<wiring-check-known@example.org>"
	wiringTestAddAndWait(t, h, known, 42)
	if got := proc.CheckMessageID(known); got != history.CaseDupes {
		t.Fatalf("id in history: got %#x, want CaseDupes", got)
	}
	if got := proc.CheckMessageID("<wiring-check-unknown@example.org>"); got != history.CasePass {
		t.Fatalf("id not in history: got %#x, want CasePass", got)
	}
}

func TestWiringProcessArticleIntakeCheck(t *testing.T) {
	history.NewMsgIdItemCache()
	h := wiringTestHistory(t)
	proc := &Processor{History: h}

	// in progress or already stored: duplicate without touching history
	for i, state := range []int{history.CaseLock, history.CaseWrite, history.CaseDupes} {
		id := "<wiring-intake-busy-" + string(rune('a'+i)) + "@example.org>"
		wiringTestSetState(id, state)
		got, err := proc.processArticle(&models.Article{MessageID: id}, "", false)
		if got != history.CaseDupes || err != nil {
			t.Fatalf("state %#x: got %#x, %v; want CaseDupes", state, got, err)
		}
	}

	// known in history: duplicate, item becomes CaseDupes
	const known = "<wiring-intake-known@example.org>"
	wiringTestAddAndWait(t, h, known, 7)
	item := history.MsgIdCache.GetORCreate(known)
	got, err := proc.processArticle(&models.Article{MessageID: known}, "", false)
	if got != history.CaseDupes || err != nil {
		t.Fatalf("known: got %#x, %v; want CaseDupes", got, err)
	}
	if s := wiringTestState(item); s != history.CaseDupes {
		t.Fatalf("known: item state %#x, want CaseDupes", s)
	}

	// new article: must NOT be reported as duplicate (it passes the claim and fails later on the
	// missing Newsgroups header, which needs no database) and must end in a terminal state
	for _, state := range []int{0, history.CasePass, history.CaseError, history.CaseRetry} {
		id := "<wiring-intake-new-" + string(rune('0'+state%10)) + "-" + time.Now().Format("150405.000000000") + "@example.org>"
		item := wiringTestSetState(id, state)
		got, err := proc.processArticle(&models.Article{MessageID: id, Headers: map[string][]string{}}, "", false)
		if got != history.CaseError || err == nil {
			t.Fatalf("new (state %#x): got %#x, %v; want CaseError from header check", state, got, err)
		}
		if s := wiringTestState(item); s != history.CaseDupes {
			t.Fatalf("new (state %#x): item state %#x, want terminal CaseDupes", state, s)
		}
	}
}
