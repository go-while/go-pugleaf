# go-pugleaf v0.6.14

The first release since v0.5.0 (2026-11-01). It covers the 0.6.x line: go-pugleaf becomes a peering news server.
Only configured peers may feed it, and articles from peers, web users and NNTP users are passed on to the other
peers over persistent streaming connections. Peers can be set to INN-style relay checks or strict checks. There are
peer statistics with INN-format logs. Threads now start per group, the mbox importer reads whole archive.org mirrors,
disc images and news spools, and nntp-analyze uses COMPRESS DEFLATE.
(`docs/RELEASE-v0.6.0.md` describes the peering part in the shape it had at 0.6.0. That version was never published,
and this page supersedes it.)

**Read "Upgrading" before you start the new nntp-server.** After the upgrade, a server that used to accept a feed
from a posting user refuses it until that sender has a row in the new `peers` table.

## Upgrading

1. **Build with Go 1.27.3 or newer.** go-sqlite3 still needs cgo and a C compiler.
2. **Stop the services, back up `data/`, install the new binaries, start again.** Five migrations run automatically
   on the first start of any tool:
   - 0034: the `peers` table and the `HistoryRejectDays` setting
   - 0035: corrected default providers
   - 0036: per-peer `in_max_size`
   - 0037: per-peer `header_check` and the peer Date limits
   - 0038: new bad-bot User-Agents
3. **Add your peers before you open the port.** On the web admin, go to `/admin/peers` and add one row per server that
   feeds you or that you feed. Give each one its Path token, plus `hosts` (IP, CIDR, hostname) and/or a login. Until a
   sender has a row, its `IHAVE`, `CHECK`, `TAKETHIS` and `MODE STREAM` get `502` (or `480` while a peer login is
   still possible). The page also shows a ready-made INN `newsfeeds`/`incoming.conf` and Diablo snippet for the
   other side.
4. **`nntp-transfer` into a go-pugleaf target** needs a peers row for the sending host on the target, for the same
   reason (see `docs/TRANSFER.md`).
5. **Rebuild the threads once.** Follow-ups whose parent is not in the same group now start their own thread, at
   intake and in the rebuild. Articles stored before the upgrade keep their old placement until you stop web and
   nntp-server and run:
   `recover-db -data <dir> -group '$all' -rebuild-threads -force`
6. **History index:** the first daily prune of remembered rejects builds one small partial index per history table,
   one table per step, in the background. It does not change existing rows.

### Changed behaviour

| Tool | What | Was | Now |
|---|---|---|---|
| nntp-server | who may use IHAVE / CHECK / TAKETHIS / MODE STREAM | any user with `posting=1` | rows of the `peers` table only |
| nntp-server | CAPABILITIES | IHAVE / STREAMING when posting was allowed | only on a peer connection |
| nntp-server | Path of a fed article | `<you>!.SEEN.<ip>!…` | `<you>!…`, or `<you>!<token>.MISMATCH!…` when the Path does not start with the peer's token |
| nntp-server | refused feed articles | forgotten after 15 s | remembered in history for `HistoryRejectDays` (30) days |
| nntp-server | header checks on IHAVE / TAKETHIS | strict: a header line over 998 octets or a bare CR → `437`, and remembered | per peer `header_check`: `relay` (default, innd rules: no line limit, bare CR/LF kept verbatim) or `strict` (nnrpd rules) |
| nntp-server | peer article Date | no lower limit | older than 10 days (`PeerArticleMaxAgeHours` 240) or more than 24 h ahead (`PeerArticleMaxFutureHours` 24) → refused, not remembered; 0 turns a limit off |
| nntp-server | NUL byte in an article body | accepted | refused on POST, IHAVE and TAKETHIS (and in fetched articles) |
| nntp-server | message-id with `<` | always refused | allowed inside a `[..]` domain literal (RFC 5536) |
| nntp-server | POST header `X:value` or `X:` | accepted | refused: `": "` and a value are required (RFC 5536 §2.2) |
| nntp-server | article size limits (server, peer, group) | measured with dot-stuffing and our added Path / injection headers | measured on the article as received, without either |
| nntp-server | server article size limit | `-maxartsize` | `-maxartsize 0` (default) follows the admin setting "Article Size Limit"; a peer's `in_max_size` wins for its feed |
| nntp-server | outgoing feed | none | on (`-feed`); idle until a peer has an outgoing feed |
| nntp-fetcher | a bare CR in a fetched header | the article was dropped | stored verbatim |
| nntp-fetcher | provider caps an XHDR reply | rest of the range skipped, progress moved on (articles lost) | asks again after the last record (up to 16 times per batch) |
| nntp-fetcher, mbox-importer | articles with a `Control:` header | stored as ordinary articles | refused and remembered, like on NNTP intake |
| web | web posts | sent to providers only | also queued to the peers; a peer taking one marks the post-queue row |
| web | NNTP account created for a web user | read-only | may post when the web user may |
| web | newsgroup search (admin and public) | prefix only (`term%`) | `*` and `%` are wildcards anywhere (at most 10); a plain term is still a prefix search |
| web | threads | a follow-up joined its parent's thread across groups | thread roots per group; an orphan follow-up starts a thread |
| nntp-analyze | provider connection | plain | COMPRESS DEFLATE when the server offers it (`-compress=false` to turn it off) |

## New

- **Peers** (`/admin/peers`):
  - name, Path token, inbound hosts and login, inbound group patterns (INN wildmat with `!` and `@`)
  - outgoing host, port, TLS, login, patterns and INN flags (`<N >N Hcount Gcount Nm Nu`), connections
  - per-peer article size limit (`in_max_size`) and header check (`relay` | `strict`)
  - INN and Diablo config snippets for the other side, and newsdeef pattern presets

  Peer passwords are write-only. The nntp-server applies edits within 30 s, without a restart.
- **Transit feed** (`nntp-server -feed`, default on):
  - Every article accepted from a peer or posted on this server (web or NNTP) is queued per peer in
    `data/cfg/feed-queue.sq3` and sent with pipelined `CHECK`/`TAKETHIS`, falling back to IHAVE.
  - Failed sends are retried with backoff from 30 s up to 30 min. Rows that still fail after 3 days are dropped.
  - A `400` reply ends the connection and the rest of the batch is retried.
  - Peers that already have an article are skipped by their Path token.
  - Fetched and imported articles are never fed.
- **Peer statistics** (`/admin/peers/stats`):
  - hourly counters per peer and direction: offered, accepted, refused, deferred, bytes, connections
  - kept `-peer-stats-days` (90) days in `data/cfg/peer-stats.sq3`
  - `nntp-server -inn-log FILE` writes them as innd/innfeed log lines, for innreport
- **Path loop check:** a fed article whose Path already holds this server's name is refused.
- **Remembered rejects** (INN "remembertrash"):
  - Permanent refusals on the feed and import paths are stored in history, so a peer gets `438`/`435` the next time
    it offers that article.
  - They expire after `HistoryRejectDays` (0 = keep). One process per history directory prunes them daily, and
    `expire-news -trim-history` prunes them too.
  - Not remembered: POST refusals, temporary failures and Date-limit refusals (raising a limit brings those articles
    back).
- **nntp-server `-disable-webserver-post-threads`:** a standalone NNTP server can skip storing threads, since only the
  web view reads them. If you add the web server later, `recover-db -rebuild-threads` builds them.
- **mbox-importer:**
  - Tracks every input file in its own database (`data/cfg/mbox-import.sq3`), with `-status` and logs in SQLite.
  - `-from-iafetch` takes its inputs straight from an iafetch download database. `-follow` handles mirrors that are
    still downloading, and `-verify-md5` checks the files.
  - Imports disc images (`.iso`, `.cdr`, `.sqsh`, `.img`) by mounting them with fuseiso/squashfuse, with 7z as a
    fallback. Also imports news spools (`-spool DIR|ZIP`).
  - Reads A-news batches and early B-news fields (`Article-I.D.`, `Title:`, `Posted:`).
  - 3-digit and 2-digit years of old articles are read as 19xx. An archive Date far in the future falls back to the
    received date.
  - `-pprof` for profiling.
- **Web profile:** the NNTP card says that the newsreader login is separate from the website login. It also shows the
  NNTP username and server, and explains read-only accounts (`440`). A new NNTP password is shown once, after it is
  created.
- **Bad bots:** anticlone, censys, claudebot, infrawatch and twitterbot are added to the default "Bad Bots" setting.
- **nginx sample** (`WebCachingProxy_examples/nginx/`), rewritten from a production setup:
  - per-IP rate limits, with a lower limit for crawlers
  - a page cache for anonymous visitors only
  - blocked bots bypass the cache, so go-pugleaf's `403` still reaches them

## Fixes

- **Default providers** (migration 0035):
  - the removed "pugleaf Archive" host is deleted
  - "BlueWorldHosting Archive" points at `archive.usenet.blueworldhosting.com`
  - the tcpreset entries keep their posting flag

  Providers that you changed are left alone. A zero proxy port and a zero size can be edited again.
- The thread tree page answers `404`, not `500`, for a missing root article.
- The global FNA/FTA jobs moved to the CFG maintenance page. A new group's first articles set its `updated_at`.
- The thread rebuild survives broken dates, keeps `last_activity` and skips self-references.
- `expire-news` keeps the thread rows of surviving replies whose parent expired.
- Peer host entries that do not parse are skipped instead of being looked up in DNS.

## Performance

- The batch writer flushes small groups sooner the more articles are queued. Both orchestrators wake on a signal from
  the divider instead of sleeping for a fixed time. Together with mounting disc images instead of unpacking them, an
  archive import on HDD storage went from about 400 to about 2,400 articles per second.
- The daily reject prune runs in its own goroutine with a 20 ms budget per step and a partial index, so it never
  delays article writes.
- nntp-analyze over COMPRESS DEFLATE: 8.0 MB of overview data came in as 1.4 MB.

## Testing

- go-pugleaf's NNTP tests use `github.com/go-while/nntp-testkit` v0.12.0 (fake servers, strict client, process runner,
  TLS certificates, RFC conformance) instead of local copies.
- New test suites cover peers, the feed queue and sender, remembered rejects, peer statistics, the admin peers page,
  per-group threading, and the relay/strict intake rules. The gaps were taken from the INN, NewsCache and diablo-nntp
  test suites. All suites run under the race detector.
- End-to-end, two real servers peer with each other and check:
  - relay and strict intake, and the refused back-offer
  - loop and pattern refusals
  - a remembered reject expiring
  - 50 queued articles drained after the peer came back
  - exact peer statistics on both sides
- `PUGLEAF_KNOWN_BUGS=1` still lists only `nntp-article-430-cache-delay`.

## Known issues

- When the outgoing queue's in-memory buffer is full, the articles that do not fit are counted and logged, but not
  queued later.
- Control messages are refused everywhere. Acting on PGP-signed `newgroup`/`rmgroup` is planned separately.
- An article the fetcher stores can answer `430` on nntp-server for up to 60 s (accepted, unchanged).
- When a provider cuts off an `ARTICLE` reply, the fetcher tries once and then moves its progress past that article.
- A range the provider no longer serves at all (`423`) makes the fetcher ask for the same range again.
- Date fields out of range ("32 May", "25:01:32") are still accepted. INN refuses them.
- There is no per-provider extra root CA for TLS.
