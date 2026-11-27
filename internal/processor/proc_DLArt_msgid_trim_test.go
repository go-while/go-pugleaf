package processor

// Plan laxmid (A): Processor.DownloadArticles against a provider whose XHDR Message-ID
// values carry blanks around the id ("2  <id>", "<id> "), as arc1-ca's gmane run saw
// (" <erih4t$1bs$1@sea.gmane.org>"). Every article must be stored; before the fix the blank
// failed the ARTICLE argument check, the article was skipped and the progress moved past it.
// This file uses no API of the laxmid change, so it also runs on the base commit (where it
// fails). No wall-clock assertion: testWait is only a hang guard.

import (
	"testing"
)

func TestDLArtFakeXHDRBlankMessageID(t *testing.T) {
	const high = 30
	group := dlFakeNewGroup()
	f := &dlFake{
		high: high,
		xhdrID: func(conn int, g string, n int64, id string) string {
			switch n % 3 {
			case 1:
				return " " + id // "n  <id>"
			case 2:
				return id + " " // trailing blank
			}
			return "\t" + id + "\t"
		},
	}
	e := dlFakeSetup(t, group, f, nil)
	if err := e.run(1, high); err != nil {
		t.Fatalf("DownloadArticles: %v", err)
	}
	e.wantStoredRange(1, high)
	e.wantProgress(high)
	if n := e.srv.Count("ARTICLE"); n != high {
		t.Errorf("ARTICLE commands = %d, want %d (one per article, with the trimmed id)", n, high)
	}
}
