# Plan: peering statistics, inpaths/top1000, optional NoCeM

> 2026-11-09: the peer statistics part (Decisions 1-3, Design "Peer stats", slices peerstats-core,
> peerstats-wiring, web-stats) was split out into `peering-stats.md` and is run first. What stays here:
> inpaths, top1000 mail (SMTP local/relay/mx), NoCeM. Drop the peer-stats slices when this plan runs.

## Context

User requests (2026-11-09):
- "peering stats like inn prints we lack too? inn style output would be great for files and a webview like
  openusenetserver has it."
- "inpaths, optional nocem support, report to top1000"
- "local / external smtp instead of sendmail" and "the web should allow setting an mx".

What exists (after nntp-peering, 4dfca0c):
- Outgoing: `internal/feed` sender keeps cumulative per-peer counters (offered, accepted, refused, rejected,
  deferred) in `feed_stats` of `data/cfg/feed-queue.sq3`, at most once a minute; `[FEED]` log line; /admin/peers
  shows them with the queue depth. No bytes, no time buckets, no history.
- Incoming: peer-only IHAVE/CHECK/TAKETHIS (`internal/nntp/nntp-peers.go`, `nntp-cmd-posting.go`,
  `nntp-cmd-stream.go`). No counters at all.
- nntp-server logs to stdout only (mbox-mo1 now has a systemd `append:` drop-in to `logs/nntp-server.log`).
- No inpaths, no mailer, no NoCeM. Control messages are refused everywhere (stays so; NoCeM notices are
  ordinary articles, mostly in `news.lists.filters`).

OpenUsenetServer (github.com/tgeek77/OpenUsenetServer, research 2026-11-09) has little to copy: an Ops page
with tiles (mode, peers on/off, queue, load, disk, feed ok/fail) and a per-peer table Peer / Host / Patterns /
Queue / Ping (2 s NNTP connect) / "Flush queue"; a stats page with top groups/posters/Path providers rebuilt
hourly from stored articles; an `internal/inpaths` package. No per-peer offered/accepted/bytes, no time series.
We take its tiles + per-peer table layout and add INN's counters and hourly history.

INN formats (from the INN source in ../inn, use these exactly so innreport can read our file):
- incoming, innd `innd/nc.c:275`, `innd/chan.c:321,332`:
  `<site> checkpoint seconds %lu accepted %lu refused %lu rejected %lu duplicate %lu accepted size %llu duplicate size %llu rejected size %llu`
  and the same with `closed` when a connection ends; `<site> connected <n> streaming allowed` on connect.
- outgoing, innfeed `innfeed/host.c:2890` (checkpoint), `:2005/2472` (global), `:2021` (ME global):
  `<peer> checkpoint seconds %ld offered %u accepted %u refused %u rejected %u missing %u accsize %.0f rejsize %.0f spooled %u on_close %u unspooled %u deferred %u/%.1f requeued %u queue %.1f/%u:%s`
  (copy the exact format strings from host.c into the code with a comment naming the source line).
- ninpaths dump format: `../inn/doc/pod/ninpaths.pod` "DUMP FILE FORMAT" (`!!NINP 3.1.1 <start> <end> <nsites>
  <narticles> <avgtime>`, site/count records, `!!NLREC`, `:a!b,count` relations, `!!NLEND <n>`). sendinpaths:
  `../inn/backends/sendinpaths.in` (report-days, keep-days, mails `ninpaths -r <pathhost>` output to
  top1000@anthologeek.net, http://top1000.anthologeek.net/).
- NoCeM: `../inn/control/perl-nocem.in`, `../inn/samples/nocem.ctl` (`issuer:type,type`), keyring
  `pgp/ncmring.gpg`, notices verified with gpg, listed ids cancelled.

## Decisions (recommendations; the user confirms or changes them before the run)

1. INN log file: optional `-inn-log <file>` on nntp-server only, syslog-style lines `Mmm dd hh:mm:ss <host> innd[<pid>]: ...` / `innfeed[<pid>]: ...` so innreport parses them
   unchanged. Checkpoint every 10 min (INN default `stathist`-like), `closed`/`final` on connection end.
2. Stats store: new `data/cfg/peer-stats.sq3`, written only by nntp-server (one writer goroutine, flush once a
   minute), read-only opener in the web process (same pattern as `feed.OpenReadOnly`). Hourly buckets per peer
   and direction; kept 90 days (config key), pruned by the writer.
3. Web view: admin-only `/admin/peers/stats` (tiles like OUS, per-peer table incoming + outgoing, last 24 h /
   7 d / 30 d, inline SVG bar charts, no JS library). A public page is not part of this plan.
4. inpaths counts the Path of articles stored live: nntp-server peer intake, POST, and the fetcher's normal
   (non-bulk) mode. mbox-importer, import-flat-files and other import tools never count (history, not traffic).
   Each process writes its own daily dump `data/logs/path/inpaths.<unix>.<process>`; the names start with
   `inpaths.` so INN's sendinpaths could read them too.
5. top1000 report: a web cron/maintenance job "send inpaths" (daily, off by default) plus `cmd/sendinpaths`
   for manual runs (`-n` print only, `-c` copy to the admin address). Mail is sent with net/smtp, never sendmail.
   Three modes, all set in the web admin:
   - `local`: localhost:25, no auth (a local MTA).
   - `relay`: external SMTP host, port, `starttls` | `tls` | `none`, optional user/password.
   - `mx`: deliver straight to the recipient domain's MX (net.LookupMX, lowest preference first, port 25,
     opportunistic STARTTLS); an optional MX host field overrides the lookup.
   From address, HELO name (default: local_nntp_hostname) and recipients (default top1000@anthologeek.net) are
   config keys. The SMTP password is stored like the provider passwords and never shown again.
6. NoCeM: optional, off by default. PGP verification in Go with `github.com/ProtonMail/go-crypto/openpgp`
   (new dependency; no gpg binary, in line with "Go port, never a sidecar"). Issuers and accepted types in an
   admin page (nocem.ctl equivalent), public keys pasted as ASCII armor or imported from a keyring file.
   Action on a listed id: hide the article in every group (`hide=1`) and remember it as a reject in history so
   neither the fetcher nor a peer brings it back. No deletion. Notices are taken from articles stored in
   `news.lists.filters` (the group must be active/fetched).

## Design

### Peer stats (`internal/peerstats`, new)
- `Collector` (in nntp-server): per peer and direction atomic counters; `Incoming(peer).Offered/Accepted/
  Refused/Rejected/Duplicate(bytes)`, connection open/close with seconds; `Outgoing` gets the same numbers from
  the feed sender (adds `Missing`, `Deferred`, `AccBytes`, `RejBytes`, `Spooled` = queued while no connection,
  queue depth). The feed sender keeps writing `feed_stats` (the peers page still uses it) and also calls the
  collector.
- Mapping to INN terms: incoming accepted = 235/239, refused = 435/438 (CHECK/IHAVE not wanted), rejected =
  437/439, duplicate = history duplicate at store time; outgoing as in `feed.Stats` comments.
- `Store` (`peer-stats.sq3`): `peer_stats_hourly(peer_id, dir, hour_unix, offered, accepted, refused, rejected,
  duplicate, missing, deferred, acc_bytes, rej_bytes, dup_bytes, conns, conn_seconds, PRIMARY KEY(peer_id, dir,
  hour_unix))`, upsert-add once a minute; `ReadRange(from, to)`; prune older than the retention.
- `INNLog` writer: formats the lines of Decision 1, one file, reopened on SIGHUP or when the file was rotated
  away (copytruncate also works).

### inpaths (`internal/inpaths`, new)
- `Counter.Add(path string)`: split the Path like ninpaths (drop the tail entry `not-for-mail` etc., same rules
  as ninpaths.c), count sites and relations in memory (bounded: cap sites/relations, log once when capped).
- `Dump(w)` writes the `!!NINP 3.1.1` format; daily rotation at a fixed hour and on shutdown.
- `Report(dumps, pathhost)` merges dumps and formats the report exactly like `ninpaths -r` (port the report
  part of ninpaths.c; golden test against a dump/report pair produced by the real ninpaths).
- Hook: the processor's store path calls a package-level `inpaths.Hook` when set (like `database.SetFeedHook`);
  the importer binaries never set it.

### Mail (`internal/mailer`, new)
- `Send(cfg Config, from string, to []string, subject, body string) error` with the three modes; per-recipient
  domain grouping for `mx`; timeouts; no retries inside (the job retries next day and logs the error).
- Config keys: `SMTPMode`, `SMTPHost`, `SMTPPort`, `SMTPSecurity`, `SMTPUser`, `SMTPPassword`, `SMTPMXHost`,
  `SMTPFrom`, `SMTPHelo`, `InpathsReportTo`, `InpathsReportDays` (1), `InpathsKeepDays` (14), `InpathsEnabled`,
  `InpathsSendEnabled`.

### NoCeM (`internal/nocem`, new)
- Parse notices (`@@BEGIN NCM HEADERS` / `@@BEGIN NCM BODY` / `@@END NCM BODY`, Issuer, Type, Action=hide,
  Count, Notice-ID), verify the clearsigned PGP block against the issuer's key, check the issuer/type list.
- Worker in the web process (owns the main DB writes for hide): scans new articles of `news.lists.filters`
  since the last processed article number (config key), applies the ids, records `nocem_notices(notice_id,
  issuer, type, received, ids, applied, error)` so a notice is applied once.
- Admin page `/admin/nocem`: enable switch, issuers (issuer address, types, key), last notices with counts.

### Web
- `/admin/peers/stats` (Decision 3) reads `peer-stats.sq3` + `feed-queue.sq3`.
- CFG tab: SMTP section (mode select local/relay/mx, fields shown per mode, "send test mail" button), inpaths
  section (enable counting, enable daily send, report-to, "send now" / "preview" buttons).
- `/admin/nocem` (NoCeM section).

## Waves

### Wave 0 (inline, orchestrator)
- Migrations: main DB config keys of the Design (defaults off), `nocem_issuers`, `nocem_notices` tables.
- Contracts: `internal/peerstats/api.go` (Collector/Store interfaces, Bucket struct), `internal/inpaths/api.go`
  (Counter, Hook setter), `internal/mailer/api.go` (Config, modes), `internal/nocem/api.go` (Notice, Verifier).
- `go.mod`: add `github.com/ProtonMail/go-crypto` (pinned).

### Wave 1 (parallel)
- slice `peerstats-core`: `internal/peerstats/*` (collector, store, INN log writer, tests for the exact line
  formats against strings copied from INN's source).
- slice `inpaths-core`: `internal/inpaths/*` (counter, dump, report; golden tests).
- slice `mailer`: `internal/mailer/*` (local/relay/mx; tests with a fake SMTP server in-process, MX via an
  injectable resolver).
- slice `nocem-core`: `internal/nocem/*` (parse, verify, issuer policy; tests with a generated test key).

### Wave 2 (parallel)
- slice `peerstats-wiring`: `internal/nntp` counters at the peer feed commands, `internal/feed` sender calls,
  `cmd/nntp-server/main.go` (`-inn-log`, stats store open/close in the shutdown order next to the feed).
- slice `inpaths-wiring`: processor hook, nntp-server + fetcher flags (`-inpaths`, default from config),
  `cmd/sendinpaths/` (new), web job + CFG section.
- slice `web-stats`: `internal/web/web_admin_peer_stats.go`, template, read-only opener in `cmd/web/main.go`,
  SMTP CFG section + test-mail button.
- slice `nocem-wiring`: worker in the web process, `/admin/nocem` page and template.

## Checks
CLAUDE.md checks (gofmt, go vet, go build, the full `go test -race` list) plus the new packages
`./internal/peerstats/... ./internal/inpaths/... ./internal/mailer/... ./internal/nocem/... ./cmd/sendinpaths/...`
(add them to the CLAUDE.md list and `.github/workflows/tests.yml`).

## End-to-end (scratch data only: `-data ./data-test-peerstats`)
1. Two nntp-servers on scratch roots and non-default ports, A peers to B (enabled both ways), post 20 articles
   on A: B's `/admin/peers/stats` shows incoming accepted 20 from A, A shows outgoing offered/accepted 20; the
   `-inn-log` files hold `checkpoint`/`closed` lines; `innreport` (if installed, else a format test) parses them.
2. Disable B's peer: A's outgoing deferred grows, queue depth shown; enable: drains, counters match.
3. inpaths: after the posts, `sendinpaths -n -data ./data-test-peerstats` prints a report; real `ninpaths -u
   <our dump> -r <host>` (if INN is installed on a matrix host) accepts our dump.
4. Mail: `sendinpaths -c` against a local fake SMTP (relay mode), and mx mode with the MX override pointing to it.
5. NoCeM: a notice signed with a test key, issuer configured → listed article hidden in its group and a later
   IHAVE of that id is refused; a notice with a bad signature changes nothing.
