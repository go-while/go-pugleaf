package nntp

// Byte accounting of the pool (plan fetcher-compress-deflate, slice
// compress-stats, design point 4).
//
// Pinned here:
//   - a Pool against a kit Store server with StoreOptions.Compress reports the
//     octets of its connections in PoolStats: rx raw above rx wire above zero,
//     tx counted in both directions;
//   - the totals survive a connection close: what a closed connection moved
//     stays in the pool totals and the next connection adds to them;
//   - a plain pool (no COMPRESS) reports raw == wire.
//
// There is no wall-clock assertion: cliWait (PUGLEAF_TEST_DEADLINE) is the
// hang guard of cliSessWaitStats, never the measurement.

import "testing"

// poolCompFetch runs one ARTICLE and one XOVER round trip over pool.
func poolCompFetch(t *testing.T, pool *Pool, n, articles int) {
	t.Helper()
	id := cliCompMsgID(n)
	var got string
	if _, err := cliCmdRun(t, func() error {
		a, e := pool.GetArticle(&id, true)
		if e != nil {
			return e
		}
		got = a.MessageID
		return nil
	}); err != nil {
		t.Fatalf("pool.GetArticle %s: %v", id, err)
	}
	if got != id {
		t.Fatalf("pool.GetArticle returned %q, want %q", got, id)
	}
	var over []OverviewLine
	if _, err := cliCmdRun(t, func() error {
		var e error
		over, e = pool.XOver(cliCompGroup, 1, int64(articles), false)
		return e
	}); err != nil {
		t.Fatalf("pool.XOver: %v", err)
	}
	if len(over) != articles {
		t.Fatalf("pool.XOver returned %d lines, want %d", len(over), articles)
	}
}

// TestPoolCompressByteStats: the pool aggregates the per-connection counters
// of a compressed provider, and keeps them when a connection is closed.
func TestPoolCompressByteStats(t *testing.T) {
	const articles = 5
	s := cliCompServer(t, articles, cliFakeFaults{})
	pool := cliCmdPool(t, s, func(cfg *BackendConfig) {
		cfg.Compress = true
		cfg.IdleExpire = -1
	})

	for i := 1; i <= 3; i++ {
		poolCompFetch(t, pool, i, articles)
	}

	st := pool.Stats()
	if st.RxBytesWire <= 0 || st.TxBytesRaw <= 0 || st.TxBytesWire <= 0 {
		t.Fatalf("pool stats %+v: want every byte counter above zero", st)
	}
	if st.RxBytesRaw <= st.RxBytesWire {
		t.Fatalf("rx raw %d <= rx wire %d: the replies were not compressed (%+v)", st.RxBytesRaw, st.RxBytesWire, st)
	}

	// Close the connection the round trips used: its octets must stay in the
	// pool totals.
	conn, err := pool.Get(MODE_READER_MV)
	if err != nil {
		t.Fatalf("pool.Get: %v", err)
	}
	conn.ForceCloseConn()
	cliSessWaitStats(t, pool, "the closed connection to be counted",
		func(s PoolStats) bool { return s.TotalClosed >= 1 && s.ActiveConnections == 0 })

	closedStats := pool.Stats()
	if closedStats.RxBytesRaw < st.RxBytesRaw || closedStats.RxBytesWire < st.RxBytesWire ||
		closedStats.TxBytesRaw < st.TxBytesRaw || closedStats.TxBytesWire < st.TxBytesWire {
		t.Fatalf("pool stats after the close %+v lost octets of %+v", closedStats, st)
	}
	if closedStats.RxBytesRaw <= closedStats.RxBytesWire {
		t.Fatalf("rx raw %d <= rx wire %d after the close (%+v)", closedStats.RxBytesRaw, closedStats.RxBytesWire, closedStats)
	}

	// The next connection adds to the totals of the closed one.
	poolCompFetch(t, pool, 4, articles)
	grown := pool.Stats()
	if grown.RxBytesRaw <= closedStats.RxBytesRaw || grown.TxBytesRaw <= closedStats.TxBytesRaw {
		t.Fatalf("pool stats %+v did not grow over %+v after a new connection", grown, closedStats)
	}
	if grown.RxBytesRaw <= grown.RxBytesWire {
		t.Fatalf("rx raw %d <= rx wire %d over both connections (%+v)", grown.RxBytesRaw, grown.RxBytesWire, grown)
	}
}

// TestPoolPlainByteStats: without COMPRESS the pool reports raw == wire.
func TestPoolPlainByteStats(t *testing.T) {
	const articles = 5
	s := cliStoreServer(t, cliCompStore(articles, 20), cliFakeStoreOpts{}, cliFakeFaults{})
	pool := cliCmdPool(t, s, func(cfg *BackendConfig) { cfg.IdleExpire = -1 })

	for i := 1; i <= 3; i++ {
		poolCompFetch(t, pool, i, articles)
	}

	st := pool.Stats()
	if st.RxBytesRaw <= 0 || st.TxBytesRaw <= 0 {
		t.Fatalf("pool stats %+v: want the byte counters above zero", st)
	}
	if st.RxBytesRaw != st.RxBytesWire || st.TxBytesRaw != st.TxBytesWire {
		t.Fatalf("pool stats %+v: want raw == wire on a plain connection", st)
	}

	conn, err := pool.Get(MODE_READER_MV)
	if err != nil {
		t.Fatalf("pool.Get: %v", err)
	}
	conn.ForceCloseConn()
	cliSessWaitStats(t, pool, "the closed connection to be counted",
		func(s PoolStats) bool { return s.TotalClosed >= 1 && s.ActiveConnections == 0 })

	closedStats := pool.Stats()
	if closedStats.RxBytesRaw < st.RxBytesRaw || closedStats.TxBytesRaw < st.TxBytesRaw {
		t.Fatalf("pool stats after the close %+v lost octets of %+v", closedStats, st)
	}
	if closedStats.RxBytesRaw != closedStats.RxBytesWire || closedStats.TxBytesRaw != closedStats.TxBytesWire {
		t.Fatalf("pool stats %+v: want raw == wire after the close", closedStats)
	}
}
