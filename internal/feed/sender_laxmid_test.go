package feed

// Plan laxmid, outgoing feed (round 0 items H2 and H3):
//   - H2: a process without -laxmid keeps (defers) a queued article whose message-id only the
//     lax syntax accepts, instead of dropping it from the queue; an id that is invalid in
//     every syntax is dropped as before.
//   - H3: with -laxmid, a strict peer answering 501 to CHECK or IHAVE of such an id refuses
//     that one article (finished for this peer); the rest of the batch goes through and the
//     connection stays usable. 501 for other ids keeps its meaning.
// No wall-clock assertion: sndWait is only a hang guard.

import (
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
	testkit "github.com/go-while/nntp-testkit"
	"github.com/go-while/nntp-testkit/fakeserver"
)

const (
	sndLaxID  = "<12799969_70875456>" // news.php.net style, no "@"
	sndJunkID = "not-a-message-id"
)

// sndStrictPeerHandler is a transit peer without laxmid (INN's default): CHECK and IHAVE of
// an id without "@" answer 501, every other id is wanted and accepted. noStream makes it an
// IHAVE-only peer (MODE STREAM 501).
func sndStrictPeerHandler(noStream bool) func(*fakeserver.Conn) {
	return func(c *fakeserver.Conn) {
		for {
			line, err := c.ReadLine()
			if err != nil {
				return
			}
			cmd, arg, _ := strings.Cut(line, " ")
			var out []string
			switch strings.ToUpper(cmd) {
			case "MODE":
				if noStream {
					out = []string{"501 Unknown MODE variant"}
				} else {
					out = []string{"203 streaming ok"}
				}
			case "CHECK":
				if !strings.Contains(arg, "@") {
					out = []string{"501 Syntax error in message-id"}
				} else {
					out = []string{"238 " + arg}
				}
			case "TAKETHIS":
				if _, err := c.ReadDotBlock(); err != nil {
					return
				}
				out = []string{"239 " + arg}
			case "IHAVE":
				if !strings.Contains(arg, "@") {
					out = []string{"501 Syntax error in message-id"}
					break
				}
				if err := c.WriteLines("335 send it"); err != nil {
					return
				}
				if _, err := c.ReadDotBlock(); err != nil {
					return
				}
				out = []string{"235 thanks"}
			case "QUIT":
				_ = c.WriteLines("205 bye")
				return
			default:
				out = []string{"500 unexpected"}
			}
			if err := c.WriteLines(out...); err != nil {
				return
			}
		}
	}
}

// TestFeedLaxmidStrictProcessKeepsLaxIDs (H2): without -laxmid both transports defer an id
// only the lax syntax accepts (nothing on the wire), drop an invalid one and send the rest.
func TestFeedLaxmidStrictProcessKeepsLaxIDs(t *testing.T) {
	if common.LaxMessageID() {
		t.Fatal("lax message-ids are on by default")
	}
	for _, noStream := range []bool{false, true} {
		s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(&fakeserver.PeerOptions{NoStream: noStream})})
		tr := sndDialReal(t, s, nil)
		good := "<lxs1@test.invalid>"
		res, offered, err := tr.Send(sndOutgoing(sndLaxID, sndJunkID, good))
		if err != nil {
			t.Fatalf("%s: Send: %v", tr.Mode(), err)
		}
		sndCheckResult(t, res, 0, sndLaxID, 0, OutcomeDefer)
		sndCheckResult(t, res, 1, sndJunkID, 0, OutcomeDrop)
		okCode := 239
		if noStream {
			okCode = 235
		}
		sndCheckResult(t, res, 2, good, okCode, OutcomeDone)
		if offered != 1 {
			t.Errorf("%s: offered = %d, want 1 (only the valid id)", tr.Mode(), offered)
		}
		if n := s.Count("CHECK") + s.Count("IHAVE"); n != 1 {
			t.Errorf("%s: %d offer(s) on the wire, want 1", tr.Mode(), n)
		}
		tr.Close()
	}
}

// TestFeedLaxmidStrictProcessQueue (H2, through the sender): the queue row of a lax-only id is
// deferred, not marked done; the junk id is done (dropped) and the valid one delivered.
func TestFeedLaxmidStrictProcessQueue(t *testing.T) {
	s := fakeserver.New(t, fakeserver.Options{Handler: fakeserver.PeerHandler(&fakeserver.PeerOptions{})})
	f := newSndFixture(t, func(c *Config) {
		c.Dial = nil // the real DialPeer
		c.Hostname = sndIdentity
		c.ConnectTimeout = sndWait
	})
	good := "<lxq1@test.invalid>"
	var items []Item
	for _, id := range []string{sndLaxID, sndJunkID, good} {
		f.arts.add(sndGroup, sndArticle(id, sndGroup))
		items = append(items, Item{PeerID: 1, MessageID: id, Newsgroup: sndGroup})
	}
	if err := f.q.Enqueue(items); err != nil {
		t.Fatal(err)
	}
	f.peers.set(sndRowFor(s, nil))
	if err := f.s.Start(); err != nil {
		t.Fatal(err)
	}
	deferredLax := func() bool {
		for _, d := range f.q.deferred() {
			if d.MessageID == sndLaxID {
				return true
			}
		}
		return false
	}
	testkit.WaitFor(t, sndWait, "the valid and the junk id done, the lax id deferred", func() bool {
		return len(f.q.doneIDs()) == 2 && deferredLax()
	})
	f.s.Stop()
	for _, id := range f.q.doneIDs() {
		if id == sndLaxID {
			t.Fatalf("%s was removed from the queue by a process without -laxmid", sndLaxID)
		}
	}
	if d, err := f.q.Depth(1); err != nil || d != 1 {
		t.Errorf("queue depth = %d, %v; want 1 (the lax id is kept)", d, err)
	}
}

// TestFeedLaxmidStrictPeer501 (H3): with -laxmid, a strict peer's 501 to CHECK or IHAVE of a
// lax-only id finishes that article; the other articles of the batch are sent, no error ends
// the batch and the next batch goes over the same connection.
func TestFeedLaxmidStrictPeer501(t *testing.T) {
	common.SetLaxMessageID(true)
	t.Cleanup(func() { common.SetLaxMessageID(false) })
	for _, noStream := range []bool{false, true} {
		s := fakeserver.New(t, fakeserver.Options{Handler: sndStrictPeerHandler(noStream)})
		tr := sndDialReal(t, s, nil)
		okCode := 239
		if noStream {
			okCode = 235
		}
		first, last, next := "<lxp1@test.invalid>", "<lxp2@test.invalid>", "<lxp3@test.invalid>"
		res, offered, err := tr.Send(sndOutgoing(first, sndLaxID, last))
		if err != nil {
			t.Fatalf("%s: Send: %v", tr.Mode(), err)
		}
		sndCheckResult(t, res, 0, first, okCode, OutcomeDone)
		sndCheckResult(t, res, 1, sndLaxID, 501, OutcomeDone)
		sndCheckResult(t, res, 2, last, okCode, OutcomeDone)
		if offered != 3 {
			t.Errorf("%s: offered = %d, want 3", tr.Mode(), offered)
		}
		res, _, err = tr.Send(sndOutgoing(next))
		if err != nil {
			t.Fatalf("%s: second Send: %v", tr.Mode(), err)
		}
		sndCheckResult(t, res, 0, next, okCode, OutcomeDone)
		if n := s.Accepted(); n != 1 {
			t.Errorf("%s: %d connection(s), want 1 (the 501 did not drop it)", tr.Mode(), n)
		}
		tr.Close()
	}

	// 501 to an id with "@" keeps its meaning: the peer does not take our CHECK, the batch
	// ends with an error.
	s := fakeserver.New(t, fakeserver.Options{Handler: func(c *fakeserver.Conn) {
		for {
			line, err := c.ReadLine()
			if err != nil {
				return
			}
			reply := "500 unexpected"
			switch {
			case strings.EqualFold(line, "MODE STREAM"):
				reply = "203 streaming ok"
			case strings.HasPrefix(line, "CHECK "):
				reply = "501 no"
			}
			if err := c.WriteLines(reply); err != nil {
				return
			}
		}
	}})
	tr := sndDialReal(t, s, nil)
	res, _, err := tr.Send(sndOutgoing("<lxp4@test.invalid>"))
	if err == nil {
		t.Fatal("CHECK 501 for an id with '@': no error")
	}
	sndCheckResult(t, res, 0, "<lxp4@test.invalid>", 0, OutcomeDefer)
}
