package processor

// Plan laxmid (B), fetch side: by default a provider article whose Message-ID has no "@"
// (news.php.net's <12799969_70875456>) is not fetched and not stored, as before; with
// -laxmid (common.SetLaxMessageID) it is fetched, passes the intake and is stored. The other
// articles of the group are stored in both modes. No wall-clock assertion.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/nntp-testkit/fakeserver"
)

// laxmidSet turns the lax message-id syntax on for this test and back off afterwards.
func laxmidSet(t *testing.T) {
	t.Helper()
	common.SetLaxMessageID(true)
	t.Cleanup(func() { common.SetLaxMessageID(false) })
}

// laxmidStore is dlFakeStore with article laxN served under the message-id laxID (in XHDR,
// ARTICLE and its Message-ID header).
func laxmidStore(group string, high, laxN int64, laxID string) *fakeserver.Store {
	st := fakeserver.NewStore()
	st.AddGenerated(group, "laxmid fetch test group", fakeserver.Generator{
		Low:  1,
		High: high,
		Article: func(g string, n int64) (fakeserver.Article, bool) {
			a, ok := dlFakeArticle(g, n)
			if n == laxN {
				a.MessageID = laxID
				for i, h := range a.Headers {
					if strings.HasPrefix(h, "Message-ID:") {
						a.Headers[i] = "Message-ID: " + laxID
					}
				}
			}
			return a, ok
		},
		Lookup: func(id string) (int64, bool) {
			if id == laxID {
				return laxN, true
			}
			// dlFakeMsgID: "<n.group@fakeserver.invalid>"
			rest, ok := strings.CutPrefix(id, "<")
			if !ok {
				return 0, false
			}
			num, ok := strings.CutSuffix(rest, "."+group+"@fakeserver.invalid>")
			if !ok {
				return 0, false
			}
			n, err := strconv.ParseInt(num, 10, 64)
			if err != nil || n == laxN || n < 1 || n > high {
				return 0, false
			}
			return n, true
		},
	})
	return st
}

// laxmidRun downloads a group of high articles whose article laxN has an id without "@" and
// returns what was stored.
func laxmidRun(t *testing.T, high, laxN int64) (stored map[string]int, group, laxID string) {
	t.Helper()
	group = dlFakeNewGroup()
	laxID = fmt.Sprintf("<%d_70875456>", dlFakeGroupSeq.Add(1))
	e := dlFakeSetup(t, group, &dlFake{high: high, st: laxmidStore(group, high, laxN, laxID)}, nil)
	if err := e.run(1, high); err != nil {
		t.Fatalf("DownloadArticles: %v", err)
	}
	return e.stored(), group, laxID
}

func TestLaxmidFetchStrictDefault(t *testing.T) {
	const high, laxN = 10, 4
	stored, group, laxID := laxmidRun(t, high, laxN)
	if stored[laxID] != 0 {
		t.Fatalf("strict: %s stored, want it skipped", laxID)
	}
	for n := int64(1); n <= high; n++ {
		if n == laxN {
			continue
		}
		if id := dlFakeMsgID(group, n); stored[id] != 1 {
			t.Errorf("strict: article %d (%s) stored %d times, want 1", n, id, stored[id])
		}
	}
	if len(stored) != high-1 {
		t.Errorf("strict: %d articles stored, want %d", len(stored), high-1)
	}
}

func TestLaxmidFetchLax(t *testing.T) {
	laxmidSet(t)
	const high, laxN = 10, 4
	stored, group, laxID := laxmidRun(t, high, laxN)
	if stored[laxID] != 1 {
		t.Fatalf("lax: %s stored %d times, want 1 (stored: %v)", laxID, stored[laxID], stored)
	}
	for n := int64(1); n <= high; n++ {
		if n == laxN {
			continue
		}
		if id := dlFakeMsgID(group, n); stored[id] != 1 {
			t.Errorf("lax: article %d (%s) stored %d times, want 1", n, id, stored[id])
		}
	}
	if len(stored) != high {
		t.Errorf("lax: %d articles stored, want %d", len(stored), high)
	}
}

// TestLaxmidIntakeHeaders: the intake header check refuses a Message-ID without "@" by
// default and takes it with -laxmid; ">" inside stays refused.
func TestLaxmidIntakeHeaders(t *testing.T) {
	head := func(id string) []string {
		return []string{
			"Path: x!not-for-mail", "From: A <a@b.invalid>", "Newsgroups: a.b", "Subject: s",
			"Date: Sat, 11 Oct 2026 10:00:00 +0000", "Message-ID: " + id,
		}
	}
	const laxID = "<12345_67890>"
	if _, _, _, err := validateIntakeHeaders(head(laxID), laxID, common.HeaderBlockFetch); !errors.Is(err, common.ErrIntakeRejected) {
		t.Fatalf("strict: validateIntakeHeaders(%s) = %v, want ErrIntakeRejected", laxID, err)
	}
	laxmidSet(t)
	if _, _, _, err := validateIntakeHeaders(head(laxID), laxID, common.HeaderBlockFetch); err != nil {
		t.Fatalf("lax: validateIntakeHeaders(%s) = %v, want nil", laxID, err)
	}
	const bad = "<a>b>"
	if _, _, _, err := validateIntakeHeaders(head(bad), bad, common.HeaderBlockFetch); !errors.Is(err, common.ErrIntakeRejected) {
		t.Fatalf("lax: validateIntakeHeaders(%s) = %v, want ErrIntakeRejected", bad, err)
	}
}
