# go-pugleaf v0.6.19

A maintenance release after v0.6.14: a Go security update, a much faster history index for big imports, and fixes
in the news client that the fetcher and the mbox importer use. No database migrations.

## Upgrading

1. **Build with Go 1.27.4 or newer.** Go 1.27.4 fixes GO-2026-6859 in net/http: an HTTP/1 server that cuts a
   request body at its size limit could crash. The web server limits request bodies this way, so rebuild it.
   go-sqlite3 still needs cgo and a C compiler.
2. **Stop the services, install the new binaries, start again.** Nothing else to do.

## Changes since v0.6.14

### Faster history index
- The history writer keeps its prepared SQL statements across flushes (one write connection per history database
  file). On a large archive import the importer spent more than half of its CPU preparing statements; that is gone,
  and imports ran about a third faster on a busy pool.

### mbox importer
- An article is filed into its input's group (the spool folder or the input file's group), not into the first group
  of a crossposted Newsgroups header.
- `-from-iafetch` no longer stats every file at startup; each file is checked when its turn comes.

### News client (fetcher, importer, transfer)
- A server that sends extra lines after a short reply (for example a 430, 411 or 423 followed by text) no longer
  leaves the connection out of step: the pool closes a connection that still holds unread reply bytes, also on
  COMPRESS DEFLATE connections.
- An XOVER reply may not have more lines than article numbers asked for (as XHDR already checks). A broken or hostile
  server could otherwise make one call hold up to 512 MiB. A reply of exactly a reader's line limit is now accepted.
- A date-based fetch start (`-download-start-date`, or a first run with expiry days) no longer skips articles when
  the server has missing article numbers, answers a probe with an error, or has misdated articles. A server error
  now stops the date search for that group without saving progress; the next run tries again. Limits are listed in
  `issues.md` ("Date-start search assumes mostly rising Dates").

### Tests
- nntp-testkit v0.14.1 (reply corpus, per-connection overrides).
