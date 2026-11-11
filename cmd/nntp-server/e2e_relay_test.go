package main

// TestE2ERelayBinary is gap test G1 of plan e2e-gap-tests (slice relay-e2e, source
// diablo-nntp tests/e2e/dnewslink_test.go TestDnewslinkStreaming/TestDnewslinkPeerHas and
// feedlimits_test.go): the whole relay path of the real pugleaf-nntp-server binary, from a
// peer's IHAVE through the outgoing queue to a second peer.
//
// Two kit fake transit peers (fakeserver.NewPeer) stand for the peers we feed; the test
// itself is peer A's inbound session (it talks from 127.0.0.1, which is peer A's hosts
// entry, so no AUTHINFO is needed). Both peers have an outgoing feed:
//
//	peer A  path_token relay.peer-a.invalid, hosts 127.0.0.1, out feed to fake peer A
//	peer B  path_token relay.peer-b.invalid, hosts "" (never matched inbound),
//	        out feed to fake peer B, out_flags "<4096,G1,H4"
//
// What it checks:
//
//   - IHAVE from peer A is accepted (235), stored, and the served Path is
//     "<us>!relay.peer-a.invalid!not-for-mail" (the inbound rewrite of plan nntp-peering);
//   - a row for peer B appears in data/cfg/feed-queue.sq3 while the sender is held in its
//     first CHECK (PeerOptions.BeforeReply + Conn.Wait), and no row is ever written for
//     peer A, whose path_token is in that Path: an article is never offered back to the
//     peer it came from;
//   - the four articles queued while the peer is held go out by CHECK/TAKETHIS in queue
//     order (one connection, max_conns 1, Claim is oldest first) and arrive at fake peer B
//     with the relayed Path unchanged - our identity exactly once;
//   - an article peer B already has (CHECK 438) gets no TAKETHIS, is not transferred and
//     leaves the queue anyway;
//   - the outgoing limits of peer B's out_flags end to end: an article over 4096 octets, one
//     crossposted to two groups and one with six Path elements are all stored here but never
//     queued for peer B and never reach it (the unit tests of the same flags are
//     TestFlagsAllowsSize, TestFlagsAllowsGroupsAndHops and TestEnqueueSelectOutFlags);
//   - the queue is empty afterwards, fake peer A was never even dialed, and the peer
//     statistics (data/cfg/peer-stats.sq3, written on shutdown) count every article in from
//     peer A and the accepted and refused ones out to peer B.
//
// The binary runs through the kit's process runner (e2eBuild/proc.GoBuild + proc.StartTCP) with its
// working directory and an absolute -data under t.TempDir(), so the default ./data is never
// reached; nntpmgr creates the scratch database (schema and migrations) and the groups and
// peers are seeded by SQL. Ports come from the OS (testkit.FreePort and the fake peers'
// 127.0.0.1:0). Every wait is a poll bounded by testWait (PUGLEAF_TEST_DEADLINE), no wall
// clock is asserted; the sender's own poll interval (5 s) is why the test takes some seconds.
//
// Skipped like TestE2EXrefBinary and TestE2EUlimitBinary: -short, PUGLEAF_E2E_ADDR set
// (external-server mode), as root (uid < 1000: OpenDatabase exits), without the go tool, or
// when no usable -nntphostname resolves.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/feed"
	"github.com/go-while/go-pugleaf/internal/peerstats"
	testkit "github.com/go-while/nntp-testkit"
	"github.com/go-while/nntp-testkit/article"
	"github.com/go-while/nntp-testkit/client"
	"github.com/go-while/nntp-testkit/fakeserver"
	"github.com/go-while/nntp-testkit/proc"
	_ "github.com/mattn/go-sqlite3"
)

const (
	relayGroupA = "relay.test.a"
	relayGroupB = "relay.test.b"
	// relayTokenA and relayTokenB are the peers' path_token values. The articles are fed
	// with relayTokenA as the leftmost Path element, as peer A's own server would.
	relayTokenA = "relay.peer-a.invalid"
	relayTokenB = "relay.peer-b.invalid"
	// relayOutFlags are peer B's out_flags: at most 4096 octets, at most one newsgroup, at
	// most four Path elements. The relayed articles are a few hundred octets, in one group,
	// with a Path of three elements ("<us>!relay.peer-a.invalid!not-for-mail").
	relayOutFlags = "<4096,G1,H4"
	// relayBigLines and relayBigLine make the article that breaks the "<4096" flag: 100
	// lines of 60 octets plus CRLF is about 6 KiB, well below the server's -maxartsize.
	relayBigLines = 100
	relayBigLine  = "0123456789012345678901234567890123456789012345678901234567"
	// relayMaxArtSize is the server's -maxartsize for this run, so the oversized article is
	// refused by peer B's flags and not by the server's intake.
	relayMaxArtSize = 32768
)

// relayRecorder collects the articles one fake peer took, in arrival order. The peer handler
// calls add on the connection's goroutine.
type relayRecorder struct {
	mu   sync.Mutex
	got  []string
	wire map[string][]byte
}

func newRelayRecorder() *relayRecorder {
	return &relayRecorder{wire: make(map[string][]byte)}
}

// add records one article the peer took (fakeserver.PeerOptions.OnArticleRaw).
func (r *relayRecorder) add(msgID string, raw []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, msgID)
	r.wire[msgID] = append([]byte(nil), raw...)
}

// ids returns the message-ids the peer took, in arrival order.
func (r *relayRecorder) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

// article returns the article the peer took under msgID, or nil.
func (r *relayRecorder) article(msgID string) *article.Article {
	r.mu.Lock()
	raw, ok := r.wire[msgID]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return article.ParseDotBlock(raw)
}

// relayFakePeer starts a kit fake transit peer on a free port of 127.0.0.1 and records what it
// takes. have are the ids it already holds (CHECK answers 438, no article is transferred);
// before, when not nil, is PeerOptions.BeforeReply.
func relayFakePeer(t *testing.T, have []string, before func(c *fakeserver.Conn, cmd, line, status string)) (*fakeserver.Server, *relayRecorder) {
	t.Helper()
	rec := newRelayRecorder()
	held := make(map[string]bool, len(have))
	for _, id := range have {
		held[id] = true
	}
	srv := fakeserver.NewPeer(t, fakeserver.Options{Addr: "127.0.0.1:0", Deadline: testWait}, &fakeserver.PeerOptions{
		Have:         held,
		OnArticleRaw: rec.add,
		BeforeReply:  before,
	})
	return srv, rec
}

// relayOpenRO opens a SQLite file of the running server read-only (WAL readers do not disturb
// the writer). The caller closes the handle.
func relayOpenRO(path string) (*sql.DB, error) {
	// SQLite's mode=ro error for a missing file says nothing a caller can test, so the stat
	// comes first: a file the server has not created yet is reported as os.ErrNotExist
	// (feed.OpenReadOnly does the same).
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=10000")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		if cerr := db.Close(); cerr != nil {
			return nil, fmt.Errorf("%w (and closing: %v)", err, cerr)
		}
		return nil, err
	}
	return db, nil
}

// relayQueueRows reads the outgoing queue file of a data root and returns the queued
// message-ids per peer, in the order Claim would take them (enqueued_at, message_id).
func relayQueueRows(dataDir string) (out map[int64][]string, err error) {
	db, err := relayOpenRO(feed.QueuePath(dataDir))
	if err != nil {
		return nil, err
	}
	// Registered before the rows.Close below, so it runs after it (defers are LIFO): the
	// rows are closed before the handle, and a close error becomes the result.
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			out, err = nil, cerr
		}
	}()
	rows, err := db.Query(`SELECT peer_id, message_id FROM feed_queue ORDER BY peer_id, enqueued_at, message_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out = make(map[int64][]string)
	for rows.Next() {
		var id int64
		var msgID string
		if serr := rows.Scan(&id, &msgID); serr != nil {
			return nil, serr
		}
		out[id] = append(out[id], msgID)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	return out, nil
}

// relayQueued is relayQueueRows for a poll condition: the rows of one peer, with a queue file
// the server has not created yet counting as no rows. Every other read error fails the test,
// so a poll for an empty queue can never pass because the read failed. The conditions of
// testkit.WaitFor run on the test's goroutine, so t.Fatalf is allowed here.
func relayQueued(t *testing.T, dataDir string, peerID int64) []string {
	t.Helper()
	rows, err := relayQueueRows(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading the queue file: %v", err)
	}
	return rows[peerID]
}

// relayStats are the peer-statistics counters of one peer and direction.
type relayStats struct {
	offered, accepted, refused, rejected, deferred int64
}

// relayPeerStats reads data/cfg/peer-stats.sq3 and sums the hourly buckets of one peer and
// direction ("in" or "out").
func relayPeerStats(t *testing.T, dataDir string, peerID int64, dir string) relayStats {
	t.Helper()
	db, err := relayOpenRO(peerstats.Path(dataDir))
	if err != nil {
		t.Fatalf("peer statistics: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing the peer statistics: %v", err)
		}
	}()
	var s relayStats
	err = db.QueryRow(`SELECT COALESCE(SUM(offered),0), COALESCE(SUM(accepted),0), COALESCE(SUM(refused),0),
		COALESCE(SUM(rejected),0), COALESCE(SUM(deferred),0) FROM peer_stats_hourly WHERE peer_id = ? AND dir = ?`,
		peerID, dir).Scan(&s.offered, &s.accepted, &s.refused, &s.rejected, &s.deferred)
	if err != nil {
		t.Fatalf("peer statistics of peer %d %s: %v", peerID, dir, err)
	}
	return s
}

// relaySeed writes the two groups and the two peers into the scratch main database and returns
// the peer ids. The peers are inserted by SQL because the database package refuses a second
// open in this test binary (the in-process server of server_e2e_test.go holds the first).
func relaySeed(t *testing.T, dataDir string, portA, portB int) (idA, idB int64) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "cfg", "pugleaf.sq3"))
	if err != nil {
		t.Fatalf("open the main database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing the main database: %v", err)
		}
	}()
	if _, err := db.Exec(`INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status,low_water,high_water)
		VALUES(?,'',0,0,1,'relay','y',1,0),(?,'',0,0,1,'relay','y',1,0)`, relayGroupA, relayGroupB); err != nil {
		t.Fatalf("seed the groups: %v", err)
	}
	insert := func(name, token, hosts string, port int, flags string) int64 {
		// max_conns 1 is load-bearing: one connWorker per peer means one claim at a time,
		// so the order in which the articles are offered is the queue order.
		res, err := db.Exec(`INSERT INTO peers(name, path_token, hosts, in_patterns, in_max_size,
			out_enabled, out_host, out_port, out_patterns, out_flags, max_conns, enabled)
			VALUES(?, ?, ?, '*', 0, 1, '127.0.0.1', ?, '*', ?, 1, 1)`, name, token, hosts, port, flags)
		if err != nil {
			t.Fatalf("seed peer %s: %v", name, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("seed peer %s: %v", name, err)
		}
		return id
	}
	// Peer A may feed us from 127.0.0.1 (the test's own connections); peer B has no hosts
	// entry, so it is never matched inbound and only ever receives.
	idA = insert("e2e-relay-a", relayTokenA, "127.0.0.1", portA, "")
	idB = insert("e2e-relay-b", relayTokenB, "", portB, relayOutFlags)
	return idA, idB
}

// relayArticle builds one article as peer A's server would send it: Path "<pathHost>!not-for-mail",
// the given groups and body (nil = the kit's one-line body).
func relayArticle(t *testing.T, msgID, groups, pathHost string, body []string) *article.Article {
	t.Helper()
	return article.New(t, article.Options{
		Newsgroups: groups,
		MessageID:  msgID,
		PathHost:   pathHost,
		Subject:    "E2E relay " + msgID,
		Body:       body,
	})
}

// relayWaitStored polls HEAD <id> until the server serves the article and returns its Path
// header. The articles are committed asynchronously, so this is the commit barrier.
func relayWaitStored(t *testing.T, c *client.Conn, msgID string) string {
	t.Helper()
	var path string
	if err := e2eWaitFor(testWait, func() error {
		code, line, err := c.TryCmd("HEAD %s", msgID)
		if err != nil {
			return err
		}
		if code != 221 {
			return fmt.Errorf("HEAD %s: %q", msgID, line)
		}
		lines, err := c.TryReadDotLines()
		if err != nil {
			return err
		}
		path = e2eHeaderValue(lines, "Path")
		return nil
	}); err != nil {
		t.Fatalf("%s was not stored: %v", msgID, err)
	}
	return path
}

// relayWaitStoredIn polls HEAD <id> with group selected, so a crosspost is known to be
// committed in that group too - and with it the feed hook of that group's batch, which runs at
// the commit point. It dials a connection of its own: a selected group narrows a later
// "HEAD <id>" to that group, which would break the group-free waits on the shared connection.
func relayWaitStoredIn(t *testing.T, addr, group, msgID string) {
	t.Helper()
	c := client.DialWith(t, addr, testWait)
	defer c.Close()
	if err := e2eWaitFor(testWait, func() error {
		code, line, err := c.TryCmd("GROUP %s", group)
		if err != nil {
			return err
		}
		if code != 211 {
			return fmt.Errorf("GROUP %s: %q", group, line)
		}
		code, line, err = c.TryCmd("HEAD %s", msgID)
		if err != nil {
			return err
		}
		if code != 221 {
			return fmt.Errorf("HEAD %s in %s: %q", msgID, group, line)
		}
		if _, err := c.TryReadDotLines(); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("%s was not stored in %s: %v", msgID, group, err)
	}
}

func TestE2ERelayBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs the server binary")
	}
	if os.Getenv("PUGLEAF_E2E_ADDR") != "" {
		t.Skip("PUGLEAF_E2E_ADDR is set: external-server mode")
	}
	if os.Geteuid() < 1000 {
		t.Skip("running as root/system user (uid < 1000): the binaries refuse to open a database")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go tool not found: %v", err)
	}
	host, err := xrefHostname()
	if err != nil {
		t.Skipf("no usable -nntphostname: %v", err)
	}

	tmp := t.TempDir()
	server := e2eBuild(t, "github.com/go-while/go-pugleaf/cmd/nntp-server")
	mgr := e2eBuild(t, "github.com/go-while/go-pugleaf/cmd/nntpmgr")
	data := filepath.Join(tmp, "data-test-e2e-relay")
	run := filepath.Join(tmp, "run")
	for _, d := range []string{data, run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// The message-ids sort in the order they are fed, so the queue order (enqueued_at,
	// message_id) is the feed order even for rows written in the same second.
	nonce := time.Now().UnixNano()
	msgID := func(n int) string { return fmt.Sprintf("<e2e-relay-%02d-%d@test.invalid>", n, nonce) }
	const (
		nFirst = 1 // held at peer B's first CHECK
		nQ1    = 2 // queued behind it while the peer is held
		nQ2    = 3
		nQ3    = 4
		nDup   = 5 // peer B has it: CHECK 438, no TAKETHIS
		nBig   = 6 // over out_flags "<4096"
		nCross = 7 // two groups: out_flags "G1"
		nHops  = 8 // six Path elements: out_flags "H4"
		nLast  = 9 // the sentinel after the three refused by the flags
	)

	// Fake peer B holds its first CHECK reply until the test closes relayGate, so the queue
	// row of the first article and the three behind it can be read while they are queued.
	relayGate := make(chan struct{})
	var held atomic.Bool
	var gateOK atomic.Bool
	srvB, recB := relayFakePeer(t, []string{msgID(nDup)}, func(c *fakeserver.Conn, cmd, line, status string) {
		if cmd == "CHECK" && held.CompareAndSwap(false, true) {
			gateOK.Store(c.Wait(relayGate))
		}
	})
	srvA, recA := relayFakePeer(t, nil, nil)

	// nntpmgr creates the scratch database (schema and migrations); then the groups and peers.
	if out, code := proc.Run(t, proc.RunOptions{
		Path: mgr, Args: []string{"-data", data, "-list"}, Dir: run, Timeout: testWait,
	}); code != 0 {
		t.Fatalf("nntpmgr -list: exit %d\n%s", code, e2eTail(out, 20))
	}
	peerA, peerB := relaySeed(t, data, srvA.Port(), srvB.Port())
	t.Logf("peers: A #%d (%s, fake peer %s), B #%d (%s, fake peer %s, out_flags %q)",
		peerA, relayTokenA, srvA.Addr(), peerB, relayTokenB, srvB.Addr(), relayOutFlags)

	port := testkit.FreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	p := proc.StartTCP(t, proc.Options{
		Path: server,
		Args: []string{
			"-data", data, "-nntphostname", host,
			"-nntpbind", "127.0.0.1", "-nntptcpport", strconv.Itoa(port), "-nntptlsport", "0",
			"-maxconnections", "20", "-maxartsize", strconv.Itoa(relayMaxArtSize),
			"-articledelay", "0", "-groupdelay", "0", "-errordelay", "0",
			// a poll by message-id may answer 430 before the commit: do not cache that miss
			"-local430ttl", "1ms", "-peer-stats-days", "1",
		},
		Dir:         run,
		Deadline:    testWait,
		StopTimeout: testWait,
	}, addr)

	// The test is peer A's inbound session (matched by 127.0.0.1); r reads the stored articles.
	c := client.DialWith(t, addr, testWait)
	r := client.DialWith(t, addr, testWait)
	ihave := func(a *article.Article) {
		t.Helper()
		c.ExpectIhave(235, a.MessageID(), a.Bytes())
	}
	wantPath := host + "!" + relayTokenA + "!not-for-mail"

	// ---------------------------------------------------------------- 1. IHAVE → queue row
	first := relayArticle(t, msgID(nFirst), relayGroupA, relayTokenA, nil)
	ihave(first)
	if got := relayWaitStored(t, r, first.MessageID()); got != wantPath {
		t.Fatalf("stored Path %q, want %q", got, wantPath)
	}
	// The sender is now in its first CHECK, whose reply the fake peer holds.
	testkit.WaitFor(t, testWait, "the first CHECK at fake peer B", func() bool {
		return srvB.Count("CHECK") >= 1
	})
	rows, err := relayQueueRows(data)
	if err != nil {
		t.Fatalf("reading the queue file: %v", err)
	}
	if got := rows[peerB]; len(got) != 1 || got[0] != first.MessageID() {
		t.Fatalf("queue rows of peer B %q, want exactly [%s]", got, first.MessageID())
	}
	if got := rows[peerA]; len(got) != 0 {
		t.Fatalf("queue rows of peer A %q: an article whose Path holds %s must never be queued for it", got, relayTokenA)
	}
	t.Logf("queue row while the peer is held: peer %d %s", peerB, rows[peerB][0])

	// ---------------------------------------------------------------- 2. queue order
	queued := []*article.Article{first}
	for _, n := range []int{nQ1, nQ2, nQ3} {
		a := relayArticle(t, msgID(n), relayGroupA, relayTokenA, nil)
		ihave(a)
		relayWaitStored(t, r, a.MessageID())
		queued = append(queued, a)
	}
	testkit.WaitFor(t, testWait, "four rows in peer B's queue", func() bool {
		return len(relayQueued(t, data, peerB)) == len(queued)
	})
	close(relayGate)
	testkit.WaitFor(t, testWait, "fake peer B taking the four queued articles", func() bool {
		return len(recB.ids()) == len(queued)
	})
	if !gateOK.Load() {
		t.Errorf("the held CHECK reply was released by the deadline or by Close, not by the test")
	}
	want := make([]string, 0, len(queued))
	for _, a := range queued {
		want = append(want, a.MessageID())
	}
	if got := recB.ids(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("fake peer B took\n%q\nwant, in queue order,\n%q", got, want)
	}
	// The relayed article carries our identity exactly once and peer A's token below it.
	for _, a := range queued {
		got := recB.article(a.MessageID())
		if got == nil {
			t.Errorf("%s did not reach fake peer B", a.MessageID())
			continue
		}
		if pathHdr := got.Get("Path"); pathHdr != wantPath {
			t.Errorf("%s reached fake peer B with Path %q, want %q", a.MessageID(), pathHdr, wantPath)
		}
		if s := got.Get("Subject"); s != a.Get("Subject") {
			t.Errorf("%s reached fake peer B with Subject %q, want %q", a.MessageID(), s, a.Get("Subject"))
		}
	}
	testkit.WaitFor(t, testWait, "peer B's queue to be empty again", func() bool {
		return len(relayQueued(t, data, peerB)) == 0
	})

	// ---------------------------------------------------------------- 3. the peer has it (438)
	takethis := srvB.Count("TAKETHIS")
	dup := relayArticle(t, msgID(nDup), relayGroupA, relayTokenA, nil)
	ihave(dup)
	relayWaitStored(t, r, dup.MessageID())
	testkit.WaitFor(t, testWait, "the 438 of the article peer B has", func() bool {
		return len(relayQueued(t, data, peerB)) == 0 && srvB.Count("CHECK") >= len(queued)+1
	})
	if n := srvB.Count("TAKETHIS"); n != takethis {
		t.Errorf("TAKETHIS count %d after the 438, want %d: an article the peer has must not be transferred", n, takethis)
	}
	if got := recB.article(dup.MessageID()); got != nil {
		t.Errorf("%s was transferred although peer B answered 438", dup.MessageID())
	}

	// ---------------------------------------------------------------- 4. the outgoing limits
	big := make([]string, relayBigLines)
	for i := range big {
		big[i] = relayBigLine
	}
	limited := []*article.Article{
		relayArticle(t, msgID(nBig), relayGroupA, relayTokenA, big),                    // "<4096"
		relayArticle(t, msgID(nCross), relayGroupA+","+relayGroupB, relayTokenA, nil),  // "G1"
		relayArticle(t, msgID(nHops), relayGroupA, relayTokenA+"!hop2!hop3!hop4", nil), // "H4"
	}
	for _, a := range limited {
		ihave(a)
		relayWaitStored(t, r, a.MessageID())
		if strings.Contains(a.Get("Newsgroups"), relayGroupB) {
			// A crosspost is committed once per group and the feed hook runs per commit:
			// wait for the second group too, or its hook could reach the enqueuer after
			// the sentinel's and the sentinel would prove nothing about it.
			relayWaitStoredIn(t, addr, relayGroupB, a.MessageID())
		}
		// Nothing of these may be queued for peer B at any point: the rows of the one claim
		// that may still be open are read here too, not only after the sentinel.
		for _, id := range relayQueued(t, data, peerB) {
			if id == a.MessageID() {
				t.Errorf("%s was queued for peer B although its out_flags %q refuse it", id, relayOutFlags)
			}
		}
	}
	// The sentinel is fed after all three are stored, so the enqueuer saw them first: once it
	// reaches peer B, the three have been through the peer selection.
	last := relayArticle(t, msgID(nLast), relayGroupA, relayTokenA, nil)
	ihave(last)
	relayWaitStored(t, r, last.MessageID())
	testkit.WaitFor(t, testWait, "fake peer B taking the article after the three limited ones", func() bool {
		return recB.article(last.MessageID()) != nil
	})
	for _, a := range limited {
		if got := recB.article(a.MessageID()); got != nil {
			t.Errorf("%s reached fake peer B although peer B's out_flags %q refuse it", a.MessageID(), relayOutFlags)
		}
	}
	wantGot := append(append([]string(nil), want...), last.MessageID())
	if got := recB.ids(); strings.Join(got, " ") != strings.Join(wantGot, " ") {
		t.Errorf("fake peer B took\n%q\nwant\n%q", got, wantGot)
	}
	testkit.WaitFor(t, testWait, "an empty queue for peer B", func() bool {
		return len(relayQueued(t, data, peerB)) == 0
	})

	// ---------------------------------------------------------------- 5. never offered back
	if n := srvA.Accepted(); n != 0 {
		t.Errorf("fake peer A accepted %d connection(s): every article's Path holds %s, so nothing may be offered to peer A", n, relayTokenA)
	}
	if got := recA.ids(); len(got) != 0 {
		t.Errorf("fake peer A took %q", got)
	}

	// ---------------------------------------------------------------- 6. shutdown and counters
	c.Close()
	r.Close()
	p.Stop() // SIGTERM; the counters are written on the way out
	if err := p.Wait(); err != nil {
		t.Errorf("server did not exit cleanly on SIGTERM: %v\n%s", err, e2eTail(p.Stderr(), 20))
	}
	rows, err = relayQueueRows(data)
	if err != nil {
		t.Fatalf("reading the queue file after the shutdown: %v", err)
	}
	for id, ids := range rows {
		t.Errorf("the queue still holds %q for peer %d after the shutdown", ids, id)
	}

	const fed = 9 // the nine IHAVEs above
	in := relayPeerStats(t, data, peerA, "in")
	if in.offered != fed || in.accepted != fed {
		t.Errorf("peer statistics in of peer A %+v, want offered %d and accepted %d", in, fed, fed)
	}
	// Out: the five articles the flags let through were offered and accepted, the one peer B
	// has was offered and refused; the three the flags refuse were never offered.
	out := relayPeerStats(t, data, peerB, "out")
	if out.offered != 6 || out.accepted != 5 || out.refused != 1 || out.rejected != 0 || out.deferred != 0 {
		t.Errorf("peer statistics out of peer B %+v, want offered 6, accepted 5, refused 1, rejected 0, deferred 0", out)
	}
	t.Logf("peer statistics: A in %+v, B out %+v", in, out)
}
