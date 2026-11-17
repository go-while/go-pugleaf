package nntp

// XOVER row cap (plan fetcher-date-search-holes, slice xover-row-cap; report of uarc-py-28cb):
// a server may not return more overview lines than article numbers asked for, as for XHDR.
// Before, a broken or hostile server answering "XOVER 5-5" with endless rows was bounded only
// by the 512 MiB list budget per call. Servers are cliStoreServer on 127.0.0.1:0; no wall-clock
// assertion (cliWait and the kit's Options.Deadline are hang guards only).

import (
	"errors"
	"fmt"
	"testing"
)

const xoverCapGroup = "xover.cap"

// xoverCapRow is an overview line for article n of xoverCapGroup.
func xoverCapRow(n int) string {
	return fmt.Sprintf("%d\tsubject %d\tcap@test.invalid\tMon, 02 Jan 2006 15:04:05 +0000\t<%d.cap@test.invalid>\t\t100\t5", n, n, n)
}

// xoverCapServer starts a store server with articles 1..20 whose XOVER reply is lines.
func xoverCapServer(t *testing.T, lines []string) *cliFakeServer {
	t.Helper()
	st := cliFakeNewStore()
	cliQuirkFill(st, xoverCapGroup, 1, 20)
	return cliStoreServer(t, st, cliFakeStoreOpts{}, cliFakeFaults{Override: map[string][]string{"XOVER": lines}})
}

// A reply with more rows than numbers asked for ends in ErrResponseTooLarge after the first
// extra row, and the connection is not reused.
func TestCliXOverRowsCapped(t *testing.T) {
	lines := []string{"224 overview follows"}
	for i := 1; i <= 5000; i++ {
		lines = append(lines, xoverCapRow(5))
	}
	lines = append(lines, ".")
	s := xoverCapServer(t, lines)
	c, err := cliDial(t, s, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ovs, err := c.XOver(xoverCapGroup, 5, 5, false)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("XOVER 5-5 with 5000 rows: %d rows, err %v, want ErrResponseTooLarge", len(ovs), err)
	}
	if !c.forceClose {
		t.Fatal("connection not marked for close after a reply over the row cap")
	}
}

// Exactly as many rows as numbers asked for are accepted (the cap is checked after the end
// marker, not before it).
func TestCliXOverRowsExactlyAtCap(t *testing.T) {
	s := xoverCapServer(t, []string{"224 overview follows", xoverCapRow(1), xoverCapRow(2), xoverCapRow(3), "."})
	c, err := cliDial(t, s, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ovs, err := c.XOver(xoverCapGroup, 1, 3, false)
	if err != nil {
		t.Fatalf("XOVER 1-3 with 3 rows: %v", err)
	}
	if len(ovs) != 3 {
		t.Fatalf("XOVER 1-3: %d rows, want 3", len(ovs))
	}
}

// One row more than asked for is an error.
func TestCliXOverOneRowOverCap(t *testing.T) {
	s := xoverCapServer(t, []string{"224 overview follows", xoverCapRow(1), xoverCapRow(2), xoverCapRow(3), xoverCapRow(4), "."})
	c, err := cliDial(t, s, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := c.XOver(xoverCapGroup, 1, 3, false); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("XOVER 1-3 with 4 rows: err %v, want ErrResponseTooLarge", err)
	}
}
