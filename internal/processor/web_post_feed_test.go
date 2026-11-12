package processor

import (
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
)

// TestWebPostQueuedForFeed: a web post (filed through the bulk path with Posted set, as the
// post queue worker does) reaches the outgoing feed hook at its commit; a fetched article on
// the same bulk path (not Posted) does not.
func TestWebPostQueuedForFeed(t *testing.T) {
	proc := postCommitSetup(t)
	var mu sync.Mutex
	hooked := map[string]bool{}
	database.SetFeedHook(func(a database.FeedArticle) {
		mu.Lock()
		hooked[a.MessageID] = true
		mu.Unlock()
	})
	t.Cleanup(func() { database.SetFeedHook(nil) })

	webPost := postCommitMsgID("webpost")
	fetched := postCommitMsgID("fetched")
	for _, tc := range []struct {
		id     string
		posted bool
	}{{webPost, true}, {fetched, false}} {
		art := postCommitArticle(t, proc, tc.id, []string{postCommitGroupA}, true)
		art.Posted = tc.posted
		if _, err := proc.ProcessArticle(art, postCommitGroupA, true); err != nil {
			t.Fatalf("ProcessArticle(%s): %v", tc.id, err)
		}
	}

	// hang guard only: wait until both rows are committed and the posted one was hooked
	deadline := time.Now().Add(testWait)
	for {
		mu.Lock()
		gotPost := hooked[webPost]
		mu.Unlock()
		if gotPost && postCommitStored(t, proc, postCommitGroupA, fetched) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not committed/hooked within %v: web post hooked=%v, fetched stored=%v",
				testWait, gotPost, postCommitStored(t, proc, postCommitGroupA, fetched))
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if hooked[fetched] {
		t.Errorf("a fetched (bulk, not posted) article reached the feed hook")
	}
}
