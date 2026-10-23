package nntp

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"
)

// cliProgOpen opens a transfer progress DB in a per-test temp dir and closes it at cleanup.
func cliProgOpen(t *testing.T) *TransferProgressDB {
	t.Helper()
	tpdb, err := OpenTransferProgressDB(t.TempDir(), "peer.example.invalid")
	if err != nil {
		t.Fatalf("OpenTransferProgressDB: %v", err)
	}
	t.Cleanup(func() {
		if err := tpdb.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return tpdb
}

// cliProgDone returns the sorted keys of GetAllProgressNewsgroups for one date range.
func cliProgDone(t *testing.T, tpdb *TransferProgressDB, start, end *time.Time) []string {
	t.Helper()
	m, err := tpdb.GetAllProgressNewsgroups(start, end)
	if err != nil {
		t.Fatalf("GetAllProgressNewsgroups: %v", err)
	}
	groups := make([]string, 0, len(m))
	for g, ok := range m {
		if !ok {
			t.Errorf("map value for %q is false", g)
		}
		groups = append(groups, g)
	}
	sort.Strings(groups)
	return groups
}

// TestCliProgBusyTimeout (A-nntp-client-2): every pooled connection of the progress DB has
// busy_timeout 30000 and journal_mode wal, not only the first one.
func TestCliProgBusyTimeout(t *testing.T) {
	tpdb := cliProgOpen(t)
	ctx, cancel := context.WithTimeout(context.Background(), cliWait) // hang guard
	defer cancel()

	// Hold all four at once so the pool has to open distinct connections.
	conns := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, c := range conns {
			if err := c.Close(); err != nil {
				t.Errorf("conn Close: %v", err)
			}
		}
	}()
	for i := 0; i < 4; i++ {
		c, err := tpdb.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	if n := tpdb.db.Stats().OpenConnections; n < 4 {
		t.Fatalf("OpenConnections = %d, want >= 4", n)
	}
	for i, c := range conns {
		var busy int64
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("conn %d: PRAGMA busy_timeout: %v", i, err)
		}
		if busy != 30000 {
			t.Errorf("conn %d: busy_timeout = %d, want 30000", i, busy)
		}
		var mode string
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatalf("conn %d: PRAGMA journal_mode: %v", i, err)
		}
		if strings.ToLower(mode) != "wal" {
			t.Errorf("conn %d: journal_mode = %q, want wal", i, mode)
		}
	}
}

// TestCliProgIncompleteNotDone (NC-2, decision 14): a group counts as done for a date range only when
// its latest row for that remote and range has retry, tx_errors and conn_errors all 0.
func TestCliProgIncompleteNotDone(t *testing.T) {
	tpdb := cliProgOpen(t)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	otherEnd := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	insert := func(group string, s, e *time.Time, retry, txErrors, connErrors int64) {
		t.Helper()
		// sent, unwanted, checked, rejected, retry, skipped, txErrors, connErrors
		if err := tpdb.InsertResult(group, s, e, 5, 1, 7, 1, retry, 0, txErrors, connErrors); err != nil {
			t.Fatalf("InsertResult(%s): %v", group, err)
		}
	}
	insert("g.clean", &start, &end, 0, 0, 0)
	insert("g.retry", &start, &end, 1, 0, 0)
	insert("g.tx", &start, &end, 0, 1, 0)
	insert("g.conn", &start, &end, 0, 0, 1)

	if got := strings.Join(cliProgDone(t, tpdb, &start, &end), ","); got != "g.clean" {
		t.Fatalf("done after first run = %q, want g.clean", got)
	}

	// A later clean row for g.retry makes it done (the latest row wins).
	insert("g.retry", &start, &end, 0, 0, 0)
	if got := strings.Join(cliProgDone(t, tpdb, &start, &end), ","); got != "g.clean,g.retry" {
		t.Fatalf("done after clean rerun of g.retry = %q, want g.clean,g.retry", got)
	}

	// A clean row with another date range does not count for this range.
	insert("g.tx", &start, &otherEnd, 0, 0, 0)
	if got := strings.Join(cliProgDone(t, tpdb, &start, &end), ","); got != "g.clean,g.retry" {
		t.Fatalf("done after other-range row = %q, want g.clean,g.retry", got)
	}
	if got := strings.Join(cliProgDone(t, tpdb, &start, &otherEnd), ","); got != "g.tx" {
		t.Fatalf("done for other range = %q, want g.tx", got)
	}

	// A later failed row makes a previously done group pending again.
	insert("g.clean", &start, &end, 1, 0, 0)
	if got := strings.Join(cliProgDone(t, tpdb, &start, &end), ","); got != "g.retry" {
		t.Fatalf("done after failed rerun of g.clean = %q, want g.retry", got)
	}

	// nil dates are their own range (stored as "").
	insert("g.nodate", nil, nil, 0, 0, 0)
	if got := strings.Join(cliProgDone(t, tpdb, nil, nil), ","); got != "g.nodate" {
		t.Fatalf("done for nil range = %q, want g.nodate", got)
	}
}
