# Plan: peering statistics (INN-style log + admin web view)

Split out of `.claude/plans/queued/peering-stats-inpaths-nocem.md` on 2026-11-09 (inpaths, top1000 mail and
NoCeM stay queued there). User: "then build the stats so we can see what's going on before enabling the
peer" (lax1 → mbox-mo1 takeover: mbox-mo1's peer news-lax.pugleaf.net is disabled, lax1's innfeed gets 502
and spools; the stats must show that).

## Context
- Outgoing: `internal/feed` sender keeps cumulative per-peer counters in `feed_stats` of
  `data/cfg/feed-queue.sq3` (offered/accepted/refused/rejected/deferred), shown on /admin/peers. Stays as is.
- Incoming: peer-only IHAVE/CHECK/TAKETHIS/MODE STREAM (`internal/nntp/nntp-peers.go`,
  `nntp-cmd-posting.go`, `nntp-cmd-stream.go`); `PeerRegistry` holds enabled peers only. No counters.
- INN formats (copy the format strings, cite the source lines of ../inn in a comment):
  - innd incoming, `innd/nc.c:275`, `innd/chan.c:321,332`:
    `<site> checkpoint seconds %lu accepted %lu refused %lu rejected %lu duplicate %lu accepted size %llu duplicate size %llu rejected size %llu`,
    same with `closed` at connection end; `<site> connected <n>` on connect (check nc.c for the exact text).
  - innfeed outgoing, `innfeed/host.c:2890` (checkpoint), `:2005`/`:2472` (`<peer> global`), `:2867`:
    `<peer> checkpoint seconds %ld offered %u accepted %u refused %u rejected %u missing %u accsize %.0f rejsize %.0f spooled %u on_close %u unspooled %u deferred %u/%.1f requeued %u queue %.1f/%u:%s`
    (fields we don't have are written as 0; the queue histogram as `0,0,0,0,0,0`).
  - Lines in the log file are syslog-style: `Mmm _d hh:mm:ss <host> innd[<pid>]: <msg>` /
    `innfeed[<pid>]: <msg>`, so INN's innreport can parse the file unchanged.

## Decisions (from the parent plan, user said "build the stats")
- Store `data/cfg/peer-stats.sq3`, written only by nntp-server (flush once a minute and on shutdown), read
  read-only by the web process. Hourly buckets per peer and direction, 90 days (nntp-server flag
  `-peer-stats-days`, default 90), pruned by the writer.
- `-inn-log <file>` on nntp-server: optional INN-style log; checkpoint lines every 10 minutes for peers with
  activity, `connected`/`closed` per connection. Reopened when the file was moved away (logrotate rename) and on
  SIGHUP-free operation: check the inode on each checkpoint; copytruncate also works (O_APPEND).
- Web: admin-only `GET /admin/peers/stats` (`?range=24h|7d|30d`, default 24h): tiles (peers enabled, incoming
  accepted/refused/rejected/denied in range, outgoing offered/accepted/queue), one table per direction per peer,
  inline SVG hourly bar chart per direction (accepted vs refused+rejected; denied in a third colour), no JS lib.
  Link from /admin/peers. Shows "no data yet" when the store file is missing.
- **Visibility before enabling** (new): the incoming side also counts what a peer row that is *disabled* tried:
  connections from its hosts and feed commands refused with 502/480 count as `Denied` for that peer. Feed
  commands from hosts that match no peer row count as `Denied` under peer ID 0, name `unknown`.
  Only feed commands (IHAVE, CHECK, TAKETHIS, MODE STREAM) count as denied; plain readers never show up.

## Design

### Contract (Wave 0, `internal/peerstats/api.go`, written by the orchestrator)
Types `Dir` (In/Out), `PeerRef{ID, Name}`, `Counters`, `Bucket`, interfaces `Collector` and `Store`, `Nop`,
`DefaultFile`, `Path(dataDir)`. Slices code against these only. See the file for the exact semantics.

### Counting rules (incoming)
- Offered: every IHAVE / CHECK / TAKETHIS from a peer connection (TAKETHIS after a CHECK of the same id counts
  once: a CHECK followed by TAKETHIS is one offer, like innd).
- Accepted: 235 / 239 (AccBytes += wire size). Refused: 435 / 438 (peer offered something we have or don't
  want before transfer). Rejected: 437 / 439 (RejBytes += size when the article was read). Duplicate: a
  TAKETHIS/IHAVE body that turned out to be a duplicate at store time (DupBytes += size). Deferred: 436 / 431.
- Denied: a feed command refused because the connection is no (enabled) peer (502/480).
- Conns/ConnSeconds: a connection counts for a peer once it is a peer connection (address match or peer login),
  or, for a disabled peer row, when its first feed command is denied.

### Counting rules (outgoing)
Same names from the feed sender's view: offered (CHECK/IHAVE sent), accepted, refused, rejected, deferred,
missing (article gone from the DB), AccBytes/RejBytes, Conns/ConnSeconds per sender connection; queue depth via
`SetQueue`.

## Waves

### Wave 0 (inline)
- `internal/peerstats/api.go` (contract) + `internal/peerstats/nop.go` if needed. Commit on `plan-peering-stats`.

### Wave 1 (parallel)
- slice `peerstats-core`: implement the package: `collector.go` (`New(Config) *Service`, `Service` implements
  `Collector`; in-memory current-hour buckets under one mutex; `Start()` runs one goroutine: flush every
  minute to the Store, checkpoint lines every 10 minutes, prune once a day; `Stop()` final flush + `closed`
  lines are the callers' job, Stop writes innfeed `global`/innd checkpoint finals), `store.go` (`Open(path)`,
  `OpenReadOnly(path)`, SQLite via `internal/database` retry helpers, WAL, schema
  `peer_stats_hourly(peer_id, peer_name, dir, hour, offered, accepted, refused, rejected, duplicate, missing,
  deferred, denied, acc_bytes, rej_bytes, dup_bytes, conns, conn_seconds, PRIMARY KEY(peer_id, dir, hour))`,
  upsert-add), `innlog.go` (syslog-style writer, exact INN formats, reopen on rename).
  Owned: `internal/peerstats/*` except `api.go`. Tests: format golden strings vs INN, hour rollover, upsert-add,
  read-only open of a missing file, prune, Stop flushes.
- slice `stats-in`: incoming counting in `internal/nntp` + feed sender counting in `internal/feed`.
  - `internal/nntp/nntp-peers.go`: registry also keeps all peer rows (store gets `GetPeers`), new
    `MatchAnyIP(ip) *models.Peer` (enabled or not, hosts only) for Denied attribution; `NNTPServer` gets a
    `Stats peerstats.Collector` field (nil → `peerstats.Nop`).
  - `nntp-cmd-posting.go`, `nntp-cmd-stream.go`, `nntp-server-cliconns.go` (per-connection Counters,
    Connected/Closed calls), plus the peers test files.
  - `internal/feed/sender.go`, `sender_conn.go`, `api.go` (Config gets `Stats peerstats.Collector`): Add per
    response, Connected/Closed per connection, SetQueue on flush.
  Owned: those files and their `_test.go` files. Not `cmd/`.
- slice `web-stats`: `internal/web/web_admin_peer_stats.go` (new), `web/templates/admin_peer_stats.html` (new),
  route in `internal/web/webserver_core_routes.go`, link in `web/templates/admin_peers.html`, opener setter
  `SetPeerStatsOpener(func(path string) (PeerStatsReader, error))` mirroring `SetFeedQueueOpener`, route count in
  `internal/web/httpx_test.go`, tests with a fake reader (`internal/web/adf_peer_stats_test.go`).
  Owned: those files. Not `cmd/web/main.go` (orchestrator wires it).

### Wave 2 (inline, orchestrator)
- `cmd/nntp-server/main.go`: open the store (`peerstats.Open(peerstats.Path(dataDir))`), `-inn-log`,
  `-peer-stats-days`, `peerstats.New`, set `nntpServer.Stats` and `feed.Config.Stats`, Start; shutdown: after
  the server and sender stopped, `Stop()` then store Close.
- `cmd/web/main.go`: `web.SetPeerStatsOpener(func(p string) (web.PeerStatsReader, error) { return peerstats.OpenReadOnly(p) })`.
- CLAUDE.md test list + `.github/workflows/tests.yml`: add `./internal/peerstats/...`.

## Checks
CLAUDE.md checks (gofmt, go vet, go build, the full `go test -race` list) plus `./internal/peerstats/...`.

## End-to-end (scratch only)
`-data ./data-test-peerstats-a` and `-data ./data-test-peerstats-b`, non-default ports:
1. B has peer row A (by host 127.0.0.1) **disabled**. A has peer B outgoing enabled. Post 10 articles on A:
   A's outgoing shows offered/deferred, B's incoming shows Denied for peer A and Conns; B's `-inn-log` has
   `connected`/`closed` lines; /admin/peers/stats on B's web (scratch, non-default port) shows them.
2. Enable B's peer row: within ~1 min the backlog flows; B incoming accepted 10, A outgoing accepted 10; INN
   log has innd checkpoint/closed lines with accepted 10 and accepted size > 0; innfeed-style lines on A.
3. Restart B: hourly buckets survive (store), the page still shows them.
