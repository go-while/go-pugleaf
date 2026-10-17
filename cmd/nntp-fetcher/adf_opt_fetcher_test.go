package main

import (
	"errors"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// TestAdfOptFetchFeedSkipsFailedLookup: A-tools-20. One group whose lookup fails
// (or returns nil) is skipped; the groups after it are still queued.
func TestAdfOptFetchFeedSkipsFailedLookup(t *testing.T) {
	groups := []*models.Newsgroup{{Name: "a.one"}, {Name: "a.broken"}, {Name: "a.nil"}, {Name: "a.two"}, {Name: "b.other"}, {Name: "a.off"}}
	lookup := func(name string) (*models.Newsgroup, error) {
		switch name {
		case "a.broken":
			return nil, errors.New("row scan failed")
		case "a.nil":
			return nil, nil
		case "a.off":
			return &models.Newsgroup{Name: name, Active: false}, nil
		}
		return &models.Newsgroup{Name: name, Active: true}, nil
	}
	out := make(chan *string, len(groups))
	calls := 0
	st := feedCheckQueue(groups, "a.", true, lookup, out, func() bool { return false }, func() { calls++ })
	close(out)

	var got []string
	for n := range out {
		got = append(got, *n)
	}
	if len(got) != 2 || got[0] != "a.one" || got[1] != "a.two" {
		t.Fatalf("queued %v, want [a.one a.two]", got)
	}
	if st.queued != 2 || calls != 2 || st.skippedLookup != 2 || st.skippedWildcard != 1 || st.skippedInActive != 1 {
		t.Fatalf("stats %+v (onQueued calls %d)", st, calls)
	}
}

// TestAdfOptFetchFeedStopsOnShutdown: nothing is queued once shutdown is wanted.
func TestAdfOptFetchFeedStopsOnShutdown(t *testing.T) {
	groups := []*models.Newsgroup{{Name: "a.one"}}
	out := make(chan *string, 1)
	st := feedCheckQueue(groups, "", true, func(string) (*models.Newsgroup, error) {
		t.Fatal("lookup called after shutdown")
		return nil, nil
	}, out, func() bool { return true }, nil)
	if st.queued != 0 || len(out) != 0 {
		t.Fatalf("queued %d (chan %d) after shutdown", st.queued, len(out))
	}
}

// TestAdfOptFetchClampFlags: decision D-O3. -max-batch is clamped to 10..1000.
func TestAdfOptFetchClampFlags(t *testing.T) {
	cases := []struct {
		in, threads, queued, par         int
		wantBatch, wantThr, wantQ, wantP int
	}{
		{1000, 4, 1280, 1, 1000, 4, 1280, 1},
		{5000, 4, 1280, 1, fetcherMaxBatch, 4, 1280, 1},
		{1001, 2000, 0, 0, fetcherMaxBatch, 1024, 1, 1},
		{1, 0, -5, -1, fetcherMinBatch, 1, 1, 1},
		{500, 16, 10, 3, 500, 16, 10, 3},
	}
	for i, c := range cases {
		b, th, q, p := c.in, c.threads, c.queued, c.par
		clampFetcherFlags(&b, &th, &q, &p)
		if b != c.wantBatch || th != c.wantThr || q != c.wantQ || p != c.wantP {
			t.Errorf("case %d: got batch=%d threads=%d queued=%d par=%d, want %d/%d/%d/%d",
				i, b, th, q, p, c.wantBatch, c.wantThr, c.wantQ, c.wantP)
		}
	}
}
