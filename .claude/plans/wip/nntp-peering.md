# Plan: NNTP peering — peers only feed, Path loops, remembered rejects, transit feed

## Context

Review of 2026-11-07 (peering inventory of testing-001 plus a look at github.com/tgeek77/OpenUsenetServer,
"OUS", a Go/PostgreSQL INN-style server). go-pugleaf takes feeds (IHAVE, MODE STREAM, CHECK, TAKETHIS,
RFC-checked) and can push a feed by hand (`nntp-transfer`), but it is no transit server: nothing it
accepts is passed on, and any NNTP user with `posting=1` may feed.

User decisions on the findings (2026-11-07):
1. `nntp-transfer` stays as it is: it is only used by the user and Jesse to move an offline archive,
   no new articles arrive there (its progress-DB skip of finished groups is fine for that).
2. A Path loop check is needed (for transit).
3. Users must not use the peering commands. Today a peer is just an `nntp_users` row with
   `posting=1`, and IHAVE/TAKETHIS skip every POST injection check. This workaround is removed and
   replaced by real peers.
4. Rejected message-ids are stored in history too (INN "remembertrash"), so peers stop re-offering them.
5. The article size limit stays a setting (`-maxartsize`, `newsgroups.max_art_size`).
6. Fix: control messages pulled by the fetcher are stored as ordinary articles (the bulk path has no
   Control check). What to do with control messages in general is open (see Open questions).
7. Dead code is fixed or removed: the newsfeeds-style matcher `MatchArticleForPeer` /
   `MatchNewsgroupPatterns` (internal/nntp/nntp-peering-pattern.go) and `Default*Patterns`
   (nntp-peering.go) are never called; the `peering` branch in internal/postmgr/postmgr.go:305-346 is
   a dead TODO.

What exists and stays (see the inventory in the session notes; key places):
- Feed commands: internal/nntp/nntp-cmd-posting.go (IHAVE :106-186, TAKETHIS :221-309,
  `relayPath` :493-505 prepends `<identity>!.SEEN.<ip>!`), nntp-cmd-stream.go (CHECK, MODE STREAM),
  gates in nntp-server-commands.go:50 `postingAllowed()` (true for anonymous clients too) and
  nntp-server-cliconns.go:112/240-243 (CAPABILITIES advertises IHAVE/STREAMING by `postingAllowed`).
- Intake: internal/processor/intake.go (`validateIntakeHeaders` refuses Control :122), threading.go
  `ProcessArticle` (bulk path :106-149 without the Control check; Xref stripped).
- History: internal/history/history.go — rows `message_id, newsgroups` where newsgroups is a CSV of
  `groupID[:articleNum]` tokens (`historyToken` :118), `Exists` :285 is a plain row lookup,
  `LookupGroups`/`LookupLocations` parse the tokens, `sqlDeleteEmpty` removes rows whose CSV became
  empty; rejected articles are only cached 15 s (`setCaseDupes`, history_MsgIdItemCache.go:20-23).
- `nntp-transfer` (outgoing batch push with pipelined CHECK/TAKETHIS, in-flight bound, IHAVE
  fallback) — reused as the client side of the transit sender, not changed in behaviour.
- Recorded decisions kept: relay as stored, one Path line; NEWNEWS 500; 439 for an in-flight
  TAKETHIS (D9); intake never creates groups (D1); `-port 433` default of nntp-transfer.

Ideas taken from OUS (what to copy, what not):
- copy: a peers table edited in the admin UI (name, Path token, hosts, patterns, flags, passwords);
  a durable per-peer outgoing queue filled at accept time (`feed_queue(peer, message_id, attempts,
  next_attempt, last_error)`), Path-based suppression at queue time (`Ap` + Path token), the
  newsfeeds flag subset (`<N`/`>N`, `Hcount` path hops, `Gcount` group count, `Nm/Nu`), and later
  "paste INN incoming.conf/innfeed.conf/newsfeeds, get our side back" onboarding and TOP1000/ninpaths.
- do not copy: one TCP connection per article (our streaming client is far better), open inbound by
  default, unauthenticated cancels, control messages without PGP, a Perl process per article.

## Design

### Peers (main DB, migration 0034_main_peers.sql)
```
peers(id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
      path_token TEXT NOT NULL,            -- the peer's Path identity (leftmost element it writes)
      hosts TEXT NOT NULL DEFAULT '',      -- comma list: hostname, IPv4/IPv6, CIDR (inbound match)
      in_username TEXT NOT NULL DEFAULT '', in_password_hash TEXT NOT NULL DEFAULT '', -- optional AUTHINFO for the peer
      in_patterns TEXT NOT NULL DEFAULT '*', -- wildmat list the peer may send us (MatchNewsgroupPatterns syntax)
      out_enabled INTEGER NOT NULL DEFAULT 0, out_host TEXT, out_port INTEGER DEFAULT 119,
      out_tls INTEGER DEFAULT 0, out_username TEXT DEFAULT '', out_password TEXT DEFAULT '',
      out_patterns TEXT NOT NULL DEFAULT '*,!control*,!junk', out_flags TEXT NOT NULL DEFAULT '',
      max_conns INTEGER NOT NULL DEFAULT 4, enabled INTEGER NOT NULL DEFAULT 1, notes TEXT DEFAULT '',
      created_at, updated_at)
```
`in_password_hash` with the same hash as `nntp_users`. `out_password` must be sent to the remote, so
it is stored as given (same as `providers.password` today; noted, not solved here).

### Inbound: only peers feed
- A connection is a **peer connection** when its remote IP matches an enabled peer's `hosts`
  (hostnames resolved, cached 5 min, re-resolved on miss) and, if that peer has `in_username`, after
  `AUTHINFO USER/PASS` with the peer's credentials (checked against `peers`, not `nntp_users`).
  Without `hosts` a peer matches by AUTHINFO only. The matched peer is stored on the connection.
- IHAVE, CHECK, TAKETHIS and MODE STREAM are **peer-only**. For anyone else: 480 when a peer login
  could still help (a peer with that IP needs AUTHINFO), else 502. MODE STREAM answers 203 only to
  a peer. CAPABILITIES lists IHAVE and STREAMING only on a peer connection. `nntp_users.posting`
  keeps meaning POST and nothing else (`postingAllowed` loses its feed role; a separate
  `feedAllowed()`), and the user workaround and its docs/tests are removed.
- Peer patterns: an article whose Newsgroups match none of the peer's `in_patterns` (revived
  `MatchNewsgroupPatterns`, INN `!`/`@` semantics: `@` poisons the whole article) is refused
  (437 IHAVE / 439 TAKETHIS) and remembered as rejected.
- Path on intake from a peer (INN convention): leftmost element equal to the peer's `path_token`
  → prepend `<identity>!`; otherwise prepend `<identity>!<path_token>.MISMATCH!`. `.SEEN.<ip>` is
  gone for peers. POST keeps `<identity>!.POSTED!`.
- **Path loop check**: an incoming feed article whose Path already holds our identity (any element,
  case-insensitive, `.SEEN`/`.MISMATCH`/`.POSTED` suffixes ignored) is refused as a loop (437/439,
  remembered). The bulk (fetcher) path keeps its "skip the prefix if the Path starts with host!"
  rule but no longer trusts it for loop purposes.

### Remembered rejects (history)
- A reject token in the CSV column: `r<unixday>` (e.g. `r20399`), never a group id. `Exists` is
  unchanged (any row = known: CHECK 438, IHAVE 435, TAKETHIS 439), so a rejected id is not offered
  again. `LookupGroups`/`LookupLocations` and every token parser skip `r` tokens (an id with only a
  reject token has no locations). Accepting an id later (rare: a group created after the reject)
  replaces the reject token; `sqlDeleteEmpty` treats a row with only an expired reject as empty.
- `History.AddReject(messageID)` (async through the writer like AddArticle). Called for permanent
  intake refusals: CaseError / ErrIntakeRejected from `ProcessArticle` (unknown/inactive groups,
  bad headers, Control, loop, peer pattern, too large on the feed path, crosspost limit, dates). Not
  for temporary failures (436/431/400) and not for "duplicate".
- Expiry: rejects older than `history_reject_days` (config key, default 30; 0 = keep forever) are
  pruned by `expire-news -trim-history` and by a daily prune in the history writer (bounded batches).
  This also replaces the 15 s `setCaseDupes` window as the long-term memory (the cache stays for the
  in-flight case).
- With `-history=false` nothing is remembered (as today).

### Control messages (decision 6, minimal fix)
The bulk (fetcher/importer) path refuses an article with a `Control:` header exactly like NNTP intake
does today (D3), logs it once per group per run, and remembers it as rejected. Supersedes stays
"logged, ignored". General handling is an open question below; until it is decided nothing acts on
control messages anywhere.

### Transit: the outgoing feed (wave 2-3)
- `feed_queue` in its own SQLite file `data/cfg/feed-queue.sq3` (keeps the hot queue out of the
  main DB): `(peer_id, message_id, enqueued_at, attempts, next_attempt, last_error,
  PRIMARY KEY(peer_id, message_id))`.
- **Enqueue** after an article is committed (the batch writer's commit point, where history is
  written; one call per article, not per group copy): for each enabled peer with `out_enabled`:
  skip if the peer's `path_token` is in the article's Path (it has it), if the article came from
  that peer, if `out_patterns` / `out_flags` (`<N`, `>N`, `Hcount`, `Gcount`, `Nm`, `Nu`) refuse it,
  or if the article is hidden/spam. Enqueue in batches inside the writer's goroutine, never blocking
  intake (bounded channel; on overflow log and mark the peer for a catch-up scan).
- **Sender** (`internal/feed`, started by nntp-server with `-feed`, default on when any peer has
  `out_enabled`): one goroutine set per peer with up to `max_conns` persistent connections, MODE
  STREAM with pipelined CHECK/TAKETHIS reusing the nntp-transfer client code (in-flight bound,
  demuxer), IHAVE fallback when the peer has no STREAMING; 238/239 → delete row; 438/439/435/437 →
  delete (refused is done); 431/436/400/network error → `attempts++`, `next_attempt` with backoff
  (30 s … 30 min), connection re-established with backoff. Articles are read through the existing
  `GetArticlesByIDs` + `transferHeaders` (relay as stored; Path gets our identity once).
- Per-peer counters (offered, accepted, refused, rejected, deferred, queue depth, last error) shown in
  the admin peers page and logged every minute.
- Out of scope here: ninpaths/TOP1000, an ops pause/throttle, INN-snippet onboarding, cleanfeed.

### Admin
Web admin `/admin/peers` (list, add, edit, delete, enable; queue depth and counters once wave 3 is
in), plus `usermgr`-style CLI flags if cheap. Peer passwords are never shown back.

## Waves

### Wave 0 (inline, on plan-nntp-peering)
- migration `0034_main_peers.sql`, `models.Peer`, DB functions in internal/database (`GetPeers`,
  `GetPeerByName`, `InsertPeer`, `UpdatePeer`, `DeletePeer`, `MatchPeerHost` helper signature),
  config key `history_reject_days`. Owned: internal/database/migrations/0034_main_peers.sql,
  internal/models/models.go (Peer only), internal/database/peers.go (new), internal/config (key only).

### Wave 1 (parallel)
- slice `peer-acl`: peer matching (IP/CIDR/hostname cache, peer AUTHINFO), peer-only IHAVE/CHECK/
  TAKETHIS/MODE STREAM and CAPABILITIES, removal of the user workaround (tests and docs that rely on a
  posting user feeding move to a peer fixture), Path rewrite with peer token / MISMATCH, Path loop
  check, inbound `in_patterns` via the revived `MatchNewsgroupPatterns`; `Default*Patterns` either
  become the default `out_patterns` or are deleted. Owned: internal/nntp/nntp-cmd-posting.go,
  nntp-cmd-stream.go, nntp-server-commands.go, nntp-server-cliconns.go, nntp-cmd-basic.go,
  nntp-cmd-list*.go (posting flag only), nntp-peering.go, nntp-peering-pattern.go (+ tests),
  nntp-peers.go (new), cmd/nntp-server/main.go, the nntp server tests that use feeding users.
- slice `history-rejects`: reject tokens, `AddReject`, token parsers skip them, prune of expired
  rejects (writer + `expire-news -trim-history`), processor calls `AddReject` on permanent refusals
  (feed and bulk paths), bulk-path Control refusal (decision 6). Owned: internal/history/*,
  internal/processor/threading.go, internal/processor/intake.go, cmd/expire-news/*.
- slice `peers-admin`: web admin peers pages and routes, removal of the dead postmgr `peering`
  branch. Owned: internal/web/web_admin_peers.go (new), web/templates/admin_peers.html (new),
  internal/web/webserver_core_routes.go (routes only), internal/postmgr/postmgr.go.

### Wave 2
- slice `feed-queue`: `internal/feed/queue.go` (SQLite queue file, enqueue/claim/ack/defer, prune),
  the enqueue hook at the batch writer's commit point (internal/database/db_batch.go, one function
  call + a registered callback so internal/database does not import internal/feed), Path/pattern/
  flag selection (`out_flags` parser modelled on OUS internal/feed/flags.go, own code + tests).

### Wave 3
- slice `feed-sender`: `internal/feed/sender.go` (per-peer persistent streaming connections on the
  nntp-transfer client, backoff, counters), wiring in cmd/nntp-server (`-feed`), counters in the
  admin peers page.

## Checks
`gofmt -l` on touched dirs, `go vet ./...`, `go build ./...`, the full CLAUDE.md test list.
New tests: peer matching (IP, CIDR, hostname, AUTHINFO-only, wrong password), non-peer gets 480/502
on IHAVE/CHECK/TAKETHIS/MODE STREAM and no IHAVE/STREAMING in CAPABILITIES, a posting user can still
POST and can no longer IHAVE, Path rewrite (token match, MISMATCH), loop refusal, in_patterns with
`!`/`@`, reject tokens round trip (Exists true, LookupGroups empty, later accept replaces, expiry
prune), bulk Control refused and remembered, queue enqueue rules (Path token, origin peer, patterns,
flags), sender ack/defer/backoff against nntp-testkit's fake server.

## End-to-end
Two nntp-server instances on scratch data (`-data ./data-test-peer-a`, `./data-test-peer-b`), non-default
ports, resolvable distinct hostnames (e.g. `example.com` / `example.org`), each configured as the
other's peer (`hosts 127.0.0.1`, path tokens = hostnames), same test group on both:
1. A user POSTs to A → the article reaches B through A's feed; B's Path reads
   `example.org!example.com!.POSTED!…`.
2. B offers it back to A (B's feed) → A refuses it (history duplicate; with history disabled on A: the
   Path loop check) and B's queue row is deleted as refused.
3. A posting user of A sends IHAVE/CHECK/TAKETHIS to A → 502 (480 with a peer login pending);
   CAPABILITIES without IHAVE/STREAMING.
4. An article for a group A does not carry, fed by B → refused once; the second CHECK answers 438
   (remembered reject); after `expire-news -trim-history` with `history_reject_days` passed it is
   wanted again.
5. Stop B, post 50 articles to A → A's queue for B holds 50 with backoff; start B → drained, counters
   match.
6. The fetcher (or mbox-importer) imports an article with a `Control:` header → refused, remembered.

## Progress

### Wave 0 (inline) — done
- migration 0034_main_peers.sql (table peers, unique partial index on in_username, config row
  `HistoryRejectDays`=30; the key is CamelCase like the other new keys, not `history_reject_days`),
  `models.Peer`, internal/database/peers.go: GetPeers, GetEnabledPeers, GetPeerByID, GetPeerByName,
  ValidatePeer, ParsePeerHosts, InsertPeer(p, inPassword), UpdatePeer (no passwords),
  SetPeerInPassword, SetPeerOutPassword, DeletePeer, AuthenticatePeer (ErrPeerAuth), HistoryRejectDays.
  Host matching with DNS cache is peer-acl's (internal/nntp/nntp-peers.go).
- Shared contract: `History.AddReject(messageID)` (stub, history-rejects implements it),
  `Processor.RememberReject(messageID)` and `ArticleProcessor.RememberReject` (nntp-server.go, the
  cmd/nntp-server adapter and both nntp test fakes, which record `rejected`). peer-acl calls
  `c.server.Processor.RememberReject` for loop and in_patterns refusals.

### Wave 1 — done (merged into plan-nntp-peering)
- 61f2665 (inline, wave 0 follow-up): ValidatePeer needs hosts or in_username; InsertPeer refuses a
  login password without a login name.
- peers-admin `worktree-agent-abca3d5198f613fa4` (e5b8b24, 7c37a40) → merge e73104b, test fix afc0623.
  /admin/peers is its own page (nav link in admin_modular.html), not an /admin tab. Passwords
  write-only (empty keeps, clear checkbox), deleting an enabled peer refused, empty number fields keep
  the stored value, typed UNIQUE check, httpx route table 142. Queue/counter columns show n/a until
  wave 3 (PeerFeedStats ready). Not done: usermgr-style CLI flags for peers.
- peer-acl `worktree-agent-a2d5828036dd9a723` (80469a0, 8959fd3, b5f5017) → merge 66d3cfd.
  PeerRegistry (internal/nntp/nntp-peers.go): no goroutine; peer list cached 30 s (failed read
  retried after 5 s, query outside the mutex, fail closed), DNS cache 5 min with single flight,
  NXDOMAIN drops at once, other DNS faults keep the answer 15 min. Peer login sets peer state, not
  c.authenticated (no POST; with -readauth no reading). 502 when AUTHINFO is impossible
  (-plaintextauth refuse on plaintext). Path loop keywords only as whole components. Remembered by
  the NNTP layer: loop, in_patterns, too large, unparsable, Message-ID mismatch. A peer row without
  path_token is skipped. nntp-peering.go deleted. docs/TRANSFER.md: a go-pugleaf target needs a peers
  row; scripts/test-nntp-server.sh seeds one. Peers.Invalidate() exists but the web process cannot
  reach it (edits apply within 30 s).
- history-rejects `worktree-agent-ae34918d8147ac350` (f37c0ac, 9c9a8df, 3fe1e07, 7b10e44, 6e7a695,
  44e2f54) → merge 7591a50. Token r<unixday>; parseLocations skips it; accept replaces it. Partial
  index `<tbl>_rej ... WHERE substr(newsgroups,1,1)='r'` on new tables, created lazily per table by
  the prune on existing ones. Prune in its own goroutine (1 s tick, 20 ms budget checked before every
  table/batch), one owner per history dir via flock reject-prune.lock (no-op lock on !unix);
  expire-news -trim-history prunes without the lock, interruptible. Retention re-read every batch,
  0 stops the walk; a refused-again id refreshes its day. Processor remembers feed-path
  CaseError+ErrIntakeRejected (incl. a per-group max_art_size-only refusal on the feed path) and
  article-level bulk refusals; not POST, not web-queue site posts (Posted:true), not temporary, not
  duplicates. Bulk path refuses Control (logged once per group per run, cap 4096). history-rebuild
  -trim skips reject tokens (RejectsSkipped). The bulk path does no history dupe check, so a
  remembered reject never blocks a fetch/import.
- Deferred: an index for rejects older than the upgrade is built during the first prune cycle
  (one CREATE INDEX per step, may exceed 20 ms on HDD; busy_timeout 30 s covers the writer).

### Waves 2+3 — run in parallel against internal/feed/api.go (inline contract)
- internal/feed/api.go: Item (peer, message-id, newsgroup, attempts, last error), Stats (per-peer
  counters, written to the queue file by the sender so the web process can read them), Queue
  interface (Enqueue, Claim with lease, Done, Defer, Depth, DropPeer, WriteStats, ReadStats, Close).
- Decisions: only articles accepted through the NNTP feed (IHAVE/TAKETHIS) or NNTP POST are queued
  (non-bulk ProcessArticle); fetched and imported (bulk) articles never are, so an archive import
  does not flood peers. Web site posts (post_queue, bulk) are not queued either (open item). The
  peer an article came from is skipped through its path_token in the Path (our rewrite puts it
  there), so no origin field is needed. No catch-up scan on enqueue overflow in this plan: drops are
  counted and logged. cmd/nntp-server wiring (queue open, enqueue hook, senders, -feed) is done inline
  after both slices merge.
- slice feed-queue: internal/feed/queue.go, enqueue.go (+ tests), out_flags parser, the commit hook
  in internal/database (db_batch.go + a small feed_hook.go), the non-bulk marker.
- slice feed-sender: internal/feed/sender.go (+ tests against nntp-testkit or a fake server), the
  counters on the admin peers page (internal/web/web_admin_peers.go, web/templates/admin_peers.html).

## Open questions (answered 2026-11-08 with the recommendations, user said "run the peering plan")
1. Control: (a) refuse everywhere now; PGP-verified newgroup/rmgroup is a later plan.
2. Waves 2-3 stay in this plan.
3. HistoryRejectDays default 30.
4. Outbound peer passwords stored as given, like providers.

## Open questions (original)
1. **Control messages** (decision 6 left open). Options:
   a. keep refusing them everywhere (today's intake rule, extended to the bulk path by this plan) —
      safest, but the server never learns about new groups and never honours cancels;
   b. store them in the `control.*`/`control` pseudo-groups for the admin to read, act on none;
   c. act on newgroup/rmgroup/checkgroups only when PGP-signed by the hierarchy's key
      (ftp.isc.org/pub/pgpcontrol keys, as INN's pgpverify), with the action queued for the admin to
      approve in the web UI; cancels ignored (or later only with Cancel-Lock, RFC 8315).
   Recommendation: (a) now, (c) as its own plan later.
2. Should waves 2-3 (the transit feed) stay in this plan, or become a second plan after wave 1 runs
   on arc1-ca?
3. `history_reject_days` default 30 (INN keeps rejects as long as history)? 
4. Peer outbound passwords stored as given (like providers) acceptable for now?
