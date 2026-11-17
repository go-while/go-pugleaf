package nntp

// XOVER row cap (plan fetcher-date-search-holes, slice xover-row-cap; report of uarc-py-28cb):
// a server may not return more overview lines than article numbers asked for, as for XHDR.
// Before, a broken or hostile server answering "XOVER 5-5" with endless rows was bounded only
// by the 512 MiB list budget per call. Servers are cliStoreServer on 127.0.0.1:0; every call
// runs under cliQuirkRun (cliWait is a hang guard only, no wall-clock assertion).

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

// xoverCapReply is a 224 reply with the rows of nums.
func xoverCapReply(nums ...int) []string {
	lines := []string{"224 overview follows"}
	for _, n := range nums {
		lines = append(lines, xoverCapRow(n))
	}
	return append(lines, ".")
}

// xoverCapServer starts a store server with articles 1..20 whose first XOVER reply on each
// connection is lines (later ones are the store's own).
func xoverCapServer(t *testing.T, lines []string) *cliFakeServer {
	t.Helper()
	st := cliFakeNewStore()
	cliQuirkFill(st, xoverCapGroup, 1, 20)
	return cliStoreServer(t, st, cliFakeStoreOpts{}, cliFakeFaults{
		Override:      map[string][]string{"XOVER": lines},
		OverrideTimes: map[string]int{"XOVER": 1},
	})
}

// xoverCapCall runs c.XOver under the hang guard.
func xoverCapCall(t *testing.T, c *BackendConn, start, end int64) ([]OverviewLine, error) {
	t.Helper()
	var ovs []OverviewLine
	err := cliQuirkRun(t, fmt.Sprintf("XOver %d-%d", start, end), func() error {
		var e error
		ovs, e = c.XOver(xoverCapGroup, start, end, false)
		return e
	})
	return ovs, err
}

// A reply with many more rows than numbers asked for ends in ErrResponseTooLarge after the
// first extra row, and the connection is marked for close.
func TestCliXOverRowsCapped(t *testing.T) {
	nums := make([]int, 5000)
	for i := range nums {
		nums[i] = 5
	}
	c, err := cliDial(t, xoverCapServer(t, xoverCapReply(nums...)), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ovs, err := xoverCapCall(t, c, 5, 5)
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
	c, err := cliDial(t, xoverCapServer(t, xoverCapReply(1, 2, 3)), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ovs, err := xoverCapCall(t, c, 1, 3)
	if err != nil {
		t.Fatalf("XOVER 1-3 with 3 rows: %v", err)
	}
	if len(ovs) != 3 {
		t.Fatalf("XOVER 1-3: %d rows, want 3", len(ovs))
	}
}

// One row more than asked for is an error.
func TestCliXOverOneRowOverCap(t *testing.T) {
	c, err := cliDial(t, xoverCapServer(t, xoverCapReply(1, 2, 3, 4)), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := xoverCapCall(t, c, 1, 3); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("XOVER 1-3 with 4 rows: err %v, want ErrResponseTooLarge", err)
	}
}

// The single-number form "XOVER n" allows one row.
func TestCliXOverSingleNumberCap(t *testing.T) {
	c, err := cliDial(t, xoverCapServer(t, xoverCapReply(7, 8)), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := xoverCapCall(t, c, 7, 0); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("XOVER 7 with 2 rows: err %v, want ErrResponseTooLarge", err)
	}
}

// Through the pool: the connection that got too many rows is closed, and the next call gets a
// fresh connection and the store's correct reply.
func TestCliXOverCapPoolReplacesConn(t *testing.T) {
	s := xoverCapServer(t, xoverCapReply(1, 2, 3, 4))
	pool := cliQuirkPool(t, s)
	run := func() ([]OverviewLine, error) {
		var ovs []OverviewLine
		err := cliQuirkRun(t, "pool.XOver 1-3", func() error {
			var e error
			ovs, e = pool.XOver(xoverCapGroup, 1, 3, false)
			return e
		})
		return ovs, err
	}
	if _, err := run(); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("first pool.XOver 1-3 with 4 rows: err %v, want ErrResponseTooLarge", err)
	}
	ovs, err := run()
	if err != nil {
		t.Fatalf("second pool.XOver 1-3: %v (the bad connection was reused?)", err)
	}
	if len(ovs) != 3 {
		t.Fatalf("second pool.XOver 1-3: %d rows, want 3", len(ovs))
	}
	if got := pool.Stats().TotalCreated; got < 2 {
		t.Fatalf("pool created %d connections, want a fresh one after the rejected reply", got)
	}
}
