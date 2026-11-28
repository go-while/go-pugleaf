# go-pugleaf v0.6.26

Changes since v0.6.21: the fetcher can take different hierarchies from different providers (routing and global
pins), the NNTP server and the fetcher can accept message-ids without `@` (`-laxmid`), and the mbox importer reads
`.tar.gz` news spools, skips foreign articles of one-group archives and can import without threading. The release
archives now also contain `mbox-importer` (Linux and macOS), `usermgr` and `history-rebuild`. One database migration
(0039, main database).

## Upgrading

1. **Build with Go 1.27.4 or newer**, as for v0.6.21. go-sqlite3 still needs cgo and a C compiler.
2. **Stop the services, the fetcher and any running import, install the new binaries, start again.** Migration 0039
   runs on the first start: it adds the column `providers.routing` (empty) and the table `group_routes` to the main
   database. With no routing and no routes the fetcher fetches as before.
3. The fetcher's `-group` now takes several patterns separated by blanks, `,` or `;`. Before, the whole value was
   one name (or one prefix if it ended in `*`), so a call like `-group 'tin.*,lists.*'` fetched nothing.
4. Routing is read only by `pugleaf-fetcher`. The web gateway opens connections to every enabled provider, and
   `post-queue` posts through every enabled provider with "Posting" set, routed or not: check the Posting flag of a
   provider you add for fetching only.

## Changes since v0.6.21

### Fetcher: group routing and global pins
- A provider can carry a routing list in the style of INN's newsfeeds (`gmane.*, gwene.*`; `!` excludes, the last
  match wins). It is then used only for the groups the list wants, and first for them; a group its LIST does not
  carry falls back to the primary. The primary (the first usable enabled provider with empty routing) fetches every
  group that no route or routing assigns elsewhere.
- Global routes (the "Global routing" card on the admin providers page) pin groups to one or more providers: a pinned
  group is fetched only from one of them (the first, by route priority, whose LIST has it), never from another
  provider, and not at all while they are down or disabled.
- A run reads the LIST of every provider that takes part, gives each group to exactly one provider, and then makes
  one pass per provider that got groups. A provider whose LIST fails gets no groups; the run stops only when every
  LIST fails. LISTs are cached for 24 hours per host, and a group whose provider fails at GROUP time (down, 411) is
  not tried at another provider in the same run.
- New flags: `-provider NAME` (only that provider's pass; also the provider for `-reset-progress` and
  `-fetch-newsgroups-from-remote`), `-routing-dry-run` (prints the group → provider assignment and exits; not
  together with `-reset-progress` or `-fetch-newsgroups-from-remote`). `-fetch-newsgroups-from-remote` adds only the
  groups the routing lets that provider serve.
- When a provider has no stored progress for a group that already has articles (for example after routing moved the
  group to it), the fetcher starts at the newest local article date, as before. It now logs this per group and in a
  pass summary, with the command for the older articles: `-download-start-date YYYY-MM-DD -group G -provider P`.
- The admin providers page has a routing field per provider and a preset menu for the public servers listed by
  newsdeef.eu (it fills the form; nothing is saved until you save). A provider that a global route still uses cannot
  be deleted.
- Details: `docs/ROUTING.md`.

### Message-ids without `@` (`-laxmid`)
- Old mailing-list gateways wrote message-ids like `<12799969_70875456>` (RFC 3977 allows them, RFC 5536 does not).
  `pugleaf-fetcher -laxmid` fetches such articles instead of refusing them. `pugleaf-nntp-server -laxmid` accepts them in
  IHAVE, CHECK and TAKETHIS and sends them in the outgoing feed. Without the flag nothing changes. `nntp-transfer`
  and `mbox-importer` have no `-laxmid` and skip such ids.
- POST stays strict. A strict feed peer that answers 501 to such an id refuses that one article. Queued articles with
  such an id stay in the feed queue while the server runs without `-laxmid`; the queue drops what it could not send
  within 3 days, as for every article.
- The NNTP client trims blanks around the message-ids of XHDR and XOVER replies; before, a server that padded them
  made the fetcher log them as invalid.

### mbox importer
- **`.tar.gz` / `.tgz` news spools** (the textusenet and gmane backups on archive.org) are read as a stream through
  gzip and tar, one member at a time, never unpacked to disk. Article numbers follow the archive order, not the
  dates, and the flat group list (newest article number first) shows that order; `recover-db -reorder-by-datesent`
  writes a copy `<group>.db.new` renumbered by date (swap it in by hand with the services stopped). A failed or
  stopped `.tar.gz` import starts again from the beginning; what was stored counts as present. A gmane archive's file name supplies the `gmane.`
  its paths leave out. Truncated archives, trailing data and bad tar headers are input errors. See "`.tar.gz`
  spools" in `cmd/mbox-importer/README.md`.
- **Spool order:** `.artN` files in spool directories, zip spools and disc images are now read in numeric order
  (`.art9` before `.art10`).
- **Foreign articles (`-foreign skip|file`, default `skip`).** An input whose file name names a group or hierarchy
  (`alt.test.20141010.mbox.gz`, `free.pt.mbox.7z`) can hold articles that list neither that group nor a group below
  it (usually also in their own group's file). They are now skipped and counted as `foreign`, instead of being filed
  one by one into other groups; `-foreign file` keeps the old behaviour. `-group` and `-group-from-name` force a
  group and are not affected. An input whose articles are all foreign
  fails instead of being marked done; this also hits a dotted file name that is not a group (`news.backup.mbox`):
  import it with `-foreign file` or `-group`. The log lines now read `skipped=N (reasons)`.
- **`-no-threading`** stores the articles without threading them: the batch writer skips the threads rows, the
  thread caches and the reply counts, work that can be done once per group afterwards. Run `recover-db -data DIR -force -rebuild-threads -group G` for each imported group
  afterwards (once the importer has stopped writing into it); until then the web view does not list the new articles
  in threads. `-reorder-by-datesent` rebuilds the threads too, in the `.db.new` file it writes.
- **History cache knobs:** `-history-cache-mb` and `-history-mmap-mb` set the SQLite page cache and mmap size of the
  message-id history index per SQLite connection (defaults unchanged). The README explains the memory cost (up to
  128 connections) and the SIGBUS risk of mapped reads.
- **History budget:** with history enabled, the importer's readers reserve history capacity before they hand
  articles to the batch writer. The budget (20,000) covers reservations, queued history operations and the running
  flush, so a full history queue no longer blocks the group batch workers. A history write error now makes the
  importer exit with an error status (the stored articles stay).

### Release archives
- `mbox-importer` (Linux and macOS only; it uses Unix process groups), `usermgr` and `history-rebuild` are now built
  and packaged with the other tools.
