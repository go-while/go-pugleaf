package main

// Tests of the followup-leftovers slice fl-ops-tools (nntp-fetcher): -reset-progress
// with a wildcard -group only touches the groups with that prefix (decision D-O1), and
// the -fetch-newsgroups-from-remote loop stops once a shutdown signal was seen.
// No database and no network: addMissingGroups runs without -fetch-newsgroups-force,
// so it never touches the database.

import (
	"errors"
	"reflect"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

func flOpsNames(groups []*models.Newsgroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Name)
	}
	return out
}

func TestFlOpsFetchResetProgressTargets(t *testing.T) {
	groups := []*models.Newsgroup{{Name: "alt.test"}, {Name: "comp.lang.go"}, {Name: "alt.misc"}, {Name: "altogether.other"}}
	if got := flOpsNames(resetProgressTargets(groups, "")); !reflect.DeepEqual(got, []string{"alt.test", "comp.lang.go", "alt.misc", "altogether.other"}) {
		t.Fatalf("no wildcard: %v, want every group", got)
	}
	if got := flOpsNames(resetProgressTargets(groups, "alt.")); !reflect.DeepEqual(got, []string{"alt.test", "alt.misc"}) {
		t.Fatalf("wildcard alt.*: %v, want [alt.test alt.misc]", got)
	}
	if got := resetProgressTargets(groups, "nomatch."); len(got) != 0 {
		t.Fatalf("wildcard nomatch.*: %v, want none", flOpsNames(got))
	}
}

func TestFlOpsFetchRemoteListStopsOnShutdown(t *testing.T) {
	remote := []nntp.GroupInfo{{Name: "alt.one", Count: 1}, {Name: "alt.two", Count: 2}, {Name: "alt.three", Count: 3}}

	// no stop: every missing group is listed (dry run)
	n, skipped, msgs, err := addMissingGroups(nil, remote, map[string]bool{"alt.two": true}, true, "", nil, false, func() bool { return false })
	if err != nil || n != 2 || skipped != 0 || msgs != 4 {
		t.Fatalf("no stop: new=%d skipped=%d messages=%d err=%v, want 2 0 4 <nil>", n, skipped, msgs, err)
	}

	// a shutdown after the first group stops the loop
	calls := 0
	n, _, _, err = addMissingGroups(nil, remote, map[string]bool{}, true, "", nil, false, func() bool {
		calls++
		return calls > 1
	})
	if !errors.Is(err, errUpdateListStopped) {
		t.Fatalf("err = %v, want errUpdateListStopped", err)
	}
	if n != 1 {
		t.Fatalf("new groups before the stop = %d, want 1", n)
	}
}
