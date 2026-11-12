# go-pugleaf v0.6.0

go-pugleaf becomes a peering news server. Only configured peers may feed it now. Articles that peers feed in or users
post locally are passed on to the other peers over persistent streaming connections. Refused message-ids are
remembered, so peers stop offering them again. The mbox importer learned to read whole archive.org mirrors, disc images
and old news spools.

**Read "Upgrading" before you start the new nntp-server:** a server that used to accept a feed from a posting user
refuses it after the upgrade until that sender has a row in the new `peers` table.

## Upgrading

1. **Stop the services, back up `data/`, install the new binaries, start again.** One migration (0034: the `peers`
   table and the `HistoryRejectDays` setting) runs automatically on the first start of any tool.
2. **Add your peers before you open the port.** On the web admin, go to `/admin/peers` (new link in the admin menu)
   and add one row per server that feeds you or that you feed. Give each one its Path token, plus `hosts` (IP, CIDR,
   hostname) and/or a login. Until a sender has a row, its `IHAVE`, `CHECK`, `TAKETHIS` and `MODE STREAM` get `502`
   (or `480` while a peer login is still possible).
3. **`nntp-transfer` into a go-pugleaf target** needs a peers row for the sending host on the target, for the same
   reason (see `docs/TRANSFER.md`).
4. **History index:** the first daily prune of remembered rejects builds one small partial index per history table,
   one table per step, in the background. It does not change existing rows.

### Changed behaviour

| Tool | What | Was | Now |
|---|---|---|---|
| nntp-server | who may use IHAVE / CHECK / TAKETHIS / MODE STREAM | any user with `posting=1` | rows of the `peers` table only |
| nntp-server | CAPABILITIES | IHAVE / STREAMING when posting was allowed | only on a peer connection |
| nntp-server | Path of a fed article | `<you>!.SEEN.<ip>!…` | `<you>!…`, or `<you>!<token>.MISMATCH!…` when the Path does not start with the peer's token |
| nntp-server | refused feed articles | forgotten after 15 s | remembered in history for `HistoryRejectDays` (30) days |
| nntp-fetcher, mbox-importer | articles with a `Control:` header | stored as ordinary articles | refused and remembered, like on NNTP intake |
| nntp-server | outgoing feed | none | on (`-feed`); idle until a peer has an outgoing feed |

## New

- **Peers** (`/admin/peers`): name, Path token, inbound hosts and login, inbound group patterns (INN wildmat with `!`
  and `@`), outgoing host, port, TLS, login, patterns and INN flags (`<N >N Hcount Gcount Nm Nu`), connections. Peer
  passwords are write-only. The nntp-server applies edits within 30 s, without a restart.
- **Transit feed** (`nntp-server -feed`, default on): every article accepted from a peer or posted on this server is
  queued per peer in `data/cfg/feed-queue.sq3` and sent with pipelined `CHECK`/`TAKETHIS` (IHAVE fallback). Failed
  sends are retried with backoff from 30 s up to 30 min, and rows that still fail after 3 days are dropped. Peers that
  already have an article are skipped through their Path token. Fetched and imported articles are never fed. The admin
  page shows queue depth and per-peer counters.
- **Path loop check:** a fed article whose Path already holds this server's name is refused.
- **Remembered rejects** (INN "remembertrash"): permanent refusals of the feed and import paths are stored in history,
  so a peer gets `438`/`435` on the next offer. They expire after `HistoryRejectDays` (0 = keep). One process per
  history directory prunes them daily, and `expire-news -trim-history` also prunes them. POST refusals and temporary
  failures are not remembered.
- **nntp-server `-disable-webserver-post-threads`:** for a standalone NNTP server, store no threads (only the web view
  reads them). `recover-db -rebuild-threads` builds them later if you add the web server.
- **mbox-importer:**
  - Tracks every input file in its own database (`data/cfg/mbox-import.sq3`, `-status`, logs in SQLite).
  - `-from-iafetch` takes its inputs straight from an iafetch download database, with `-follow` for mirrors that are
    still downloading and `-verify-md5`.
  - Imports disc images (`.iso`, `.cdr`, `.sqsh`, `.img`: mounted with fuseiso/squashfuse, 7z as fallback) and news
    spools (`-spool DIR|ZIP`).
  - Reads A-news batches and early B-news fields (`Article-I.D.`, `Title:`, `Posted:`).
  - `-pprof` for profiling.

## Performance

- The batch writer flushes small groups sooner the more articles are queued, and both orchestrators wake on a signal
  from the divider instead of sleeping a fixed time. Together with mounting disc images instead of unpacking them,
  an archive import on HDD storage went from about 400 to about 2,400 articles per second.
- The daily reject prune runs in its own goroutine with a 20 ms budget per step and a partial index, so it never
  delays article writes.

## Testing

- New suites for peers, the feed queue and the sender, remembered rejects and the admin peers page; all run under the
  race detector.
- End-to-end check with two peered servers: a POST relayed with the right Path, the refused back-offer, loop and
  pattern refusals, a remembered reject expiring, 50 queued articles drained after the peer came back, and a refused
  import control message.
- `PUGLEAF_KNOWN_BUGS=1` still lists only `nntp-article-430-cache-delay`.

## Known issues

- Web posts (the post queue) are not passed on to peers.
- When the outgoing queue's in-memory buffer is full, the articles that do not fit are counted and logged, but not
  queued later.
- Control messages are refused everywhere. Acting on PGP-signed `newgroup`/`rmgroup` is planned separately.
- An article the fetcher stores can answer `430` on nntp-server for up to 60 s (accepted, unchanged).
