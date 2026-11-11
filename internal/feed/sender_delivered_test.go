package feed

import (
	"slices"
	"sync"
	"testing"

	testkit "github.com/go-while/nntp-testkit"
)

// TestSenderDelivered: Config.Delivered gets the ids a peer took (239) or already had (438),
// never a rejected (437), deferred (431) or dropped one, so only a post that is out gets its
// post_queue entry marked.
func TestSenderDelivered(t *testing.T) {
	var (
		mu  sync.Mutex
		got []string
	)
	f := newSndFixture(t, func(c *Config) {
		c.Delivered = func(ids []string) {
			mu.Lock()
			got = append(got, ids...)
			mu.Unlock()
		}
	})
	const group = "feed.test"
	took, had, rejected, retry, dropped := "<dl1@test.invalid>", "<dl2@test.invalid>", "<dl3@test.invalid>", "<dl4@test.invalid>", "<dl5@test.invalid>"
	ids := []string{took, had, rejected, retry, dropped}
	items := make([]Item, 0, len(ids))
	for _, id := range ids {
		f.arts.add(group, sndArticle(id, group))
		items = append(items, Item{PeerID: 1, MessageID: id, Newsgroup: group})
	}
	if err := f.q.Enqueue(items); err != nil {
		t.Fatal(err)
	}
	f.dial.newT = func() *sndTransport {
		return &sndTransport{reply: func(_ int, arts []Outgoing) ([]Result, int, error) {
			res := make([]Result, 0, len(arts))
			for _, a := range arts {
				id := a.Article.MessageID
				switch id {
				case took:
					res = append(res, Result{MessageID: id, Code: 239, Outcome: OutcomeDone, Transferred: true})
				case had:
					res = append(res, Result{MessageID: id, Code: 438, Outcome: OutcomeDone})
				case rejected:
					res = append(res, Result{MessageID: id, Code: 437, Outcome: OutcomeDone, Transferred: true})
				case dropped:
					res = append(res, Result{MessageID: id, Outcome: OutcomeDrop, Err: "invalid message-id"})
				default:
					res = append(res, Result{MessageID: id, Code: 431, Outcome: OutcomeDefer, Err: "CHECK 431"})
				}
			}
			return res, len(arts), nil
		}}
	}
	f.peers.set(sndPeer(1, "dlpeer"))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	testkit.WaitFor(t, sndWait, "four items done", func() bool { return len(f.q.doneIDs()) == 4 })
	f.s.Stop()

	mu.Lock()
	defer mu.Unlock()
	slices.Sort(got)
	if want := []string{took, had}; !slices.Equal(got, want) {
		t.Errorf("Delivered got %v, want %v", got, want)
	}
}
