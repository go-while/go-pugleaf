# go-pugleaf v0.6.26

Changes since v0.6.21: the fetcher can take different hierarchies from different providers (routing and global
pins), the NNTP server and the fetcher can accept message-ids without `@` (`-laxmid`), and the mbox importer reads
`.tar.gz` news spools, skips foreign articles of one-group archives and can import without threading. One database
migration (0039, main database).

## Upgrading

1. **Build with Go 1.27.4 or newer**, as for v0.6.21. go-sqlite3 still needs cgo and a C compiler.
2. **Stop the services, the fetcher and any running import, install the new binaries, start again.** Migration 0039
   runs on the first start: it adds the column `providers.routing` (empty) and the table `group_routes` to the main
   database. With no routing and no routes the fetcher behaves as before.
3. Scripts that pass several groups to the fetcher's `-group` with commas or blanks now get each of them (before,
   the whole string was taken as one name).

## Changes since v0.6.21

### Fetcher: group routing and global pins
- A provider can carry a routing list in the style of INN's newsfeeds (`gmane.*, gwene.*`; `!` excludes, the last
  match wins): it is then used only for the groups the list wants, and first for them. The primary (the first
  enabled provider with empty routing) fetches everything else.
- Global routes (the "Global routing" card on the admin providers page) pin groups to one or more providers: a pinned
  group is fetched only from them, in route priority order, never from another provider, and not at all while they
  are down or disabled.
- One fetcher run makes one pass per provider that takes part. Each group goes to exactly one provider per run.
- New flags: `-provider NAME` (only that provider's pass; also the provider for `-reset-progress` and
  `-fetch-newsgroups-from-remote`), `-routing-dry-run` (prints the group → provider assignment and exits).
  `-group` takes several patterns separated by blanks, `,` or `;`.
- A group that already has articles and is fetched from a provider for the first time starts at the newest local
  article date, with a log line; fetch older articles with `-download-start-date YYYY-MM-DD -group G -provider P`.
- The admin providers page has a routing field per provider and a preset menu with the public servers listed by
  newsdeef.eu (it fills the form, nothing is saved until you save). A provider that global routes still use cannot
  be deleted.
- Details: `docs/ROUTING.md`.

### Message-ids without `@` (`-laxmid`)
- Old mailing-list gateways wrote message-ids like `<12799969_70875456>` (RFC 3977 allows them, RFC 5536 does not).
  `pugleaf-fetcher -laxmid` fetches such articles instead of refusing them. `nntp-server -laxmid` accepts them in
  IHAVE, CHECK and TAKETHIS and sends them in the outgoing feed. Without the flag nothing changes.
- POST stays strict. A strict feed peer that answers 501 to such an id refuses that one article; queued lax-only
  articles wait while the sending server runs without `-laxmid`.
- The NNTP client trims blanks around the message-ids of XHDR and XOVER replies; before, a server that padded them
  made the fetcher log them as invalid.

### mbox importer
- **`.tar.gz` / `.tgz` news spools** (the textusenet and gmane backups on archive.org) are read as a stream through
  gzip and tar, one member at a time, never unpacked to disk. The archive order is kept, so article numbers are not
  in date order (the overview sorts by date; `recover-db -reorder-by-datesent` renumbers a group). A gmane archive's
  file name supplies the `gmane.` its paths leave out. Truncated archives, trailing data and bad tar headers are
  input errors. See "`.tar.gz` spools" in `cmd/mbox-importer/README.md`.
- **Foreign articles (`-foreign skip|file`, default `skip`).** An input whose file name names a group or hierarchy
  (`alt.test.20141010.mbox.gz`, `free.pt.mbox.7z`) often holds crossposts that list neither that group nor a group
  below it. They are now skipped and counted as `foreign`, instead of being filed one by one into other groups
  (`-foreign file` keeps the old behaviour). An input whose articles are all foreign fails instead of being marked
  done; import it again with `-foreign file` if that is wanted.
- **`-no-threading`** stores the articles without threads, thread caches and reply counts, which is most of a
  batch's work in a large group. Run `recover-db -data DIR -force -rebuild-threads -group G` for each imported group
  afterwards (once the importer has stopped writing into it); until then the web view does not list the new articles
  in threads. `-reorder-by-datesent` rebuilds the threads too, in the new file it writes.
- **History cache knobs:** `-history-cache-mb` and `-history-mmap-mb` set the SQLite page cache and mmap size of the
  message-id history index per connection (defaults unchanged). The README explains the memory cost (up to 128
  connections) and the SIGBUS risk of a large mmap.
- **History budget:** with history enabled, readers wait for room in the history writer (20,000 operations) before
  they queue articles, so a slow history write no longer lets the queue grow without bound.

### Fixes
- Fetcher: article workers drop their results after a shutdown signal instead of blocking the shutdown.
