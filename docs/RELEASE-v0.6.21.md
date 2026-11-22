# go-pugleaf v0.6.21

A small release after v0.6.20: the mbox importer no longer creates a group for an article that its article checks
refuse, and it can remove the empty groups that earlier versions left behind. No database migrations.

## Upgrading

1. **Build with Go 1.27.4 or newer**, as for v0.6.20. go-sqlite3 still needs cgo and a C compiler.
2. **Stop the services and any running import, install the new binaries, start again.**
3. If an earlier import left empty groups, you can remove them with the new cleanup mode (below), with `-dry-run`
   first, while no import and no other go-pugleaf program writes to the same data directory.

## Changes since v0.6.20

### mbox importer: no group for a refused article
- The importer created the target group before the article went through the article checks, so an article that was
  then refused left an empty group behind; such groups show up in the flat group lists of the hierarchy pages. On a
  large archive import (2026-11) they came mostly from control messages (cancels and newgroups, for example old
  B-News `*.ctl` cancel groups) and from truncated articles (for example groups like `comp.grapFrom`).
- The article-only checks (NUL, header block, Message-ID header, control message, Newsgroups and the crosspost limit,
  Subject, From, date) now run before the group is created; an article they refuse creates no group and no group
  database. The `[MBOX-IMPORT] rejected ...` line shows the same reasons as before; the processor's own per-article
  diagnostic lines (`[INTAKE]`, `[SPAM:HDR]`, the once-per-group control line) are no longer printed for these
  articles.
- An empty group can still appear in other ways, for example when a group database cannot be opened after the group
  was created, or when an import stops before the batch with the group's first article is written. A damaged article
  that passes every check can still create an unwanted group.
- An article already stored in its group that the checks now refuse counts as rejected instead of present; its
  stored copy is not touched.
- `-dry-run` applies the same checks, with the default crosspost limit (9) instead of the database setting, and
  without the lookup of articles already present.

### mbox importer: `-drop-empty-groups FILE`
- New cleanup mode: for each group named in FILE (one per line; blank and `#` lines are ignored), it deletes the
  group when its article count is 0 (or unset) and its group database holds no articles: the row, the rows that
  depend on it, and the group database files. A listed group with articles is skipped and reported, and so is a
  name without a row, unless an empty group database file of it is left (for example after an interrupted cleanup):
  then only that file is removed. `-dry-run` only lists what would be done.
- `-data` must be given explicitly. A real cleanup refuses to run while the import tracking database (the default
  one in `-data`, or the `-trackdb` you pass) shows an unfinished run; `-force-stale-runs` accepts such runs as dead
  without checking them (a live run on the same host is always refused). A dry run only warns. An importer that used
  another tracking database or `-trackdb off`, and any other go-pugleaf program writing to the same data, are not
  detected: stop them first.
- After an interrupted or failed cleanup, fix the cause and run it again with the same list: it retries the groups
  that are left and removes empty group database files whose rows are gone. See `cmd/mbox-importer/README.md` for
  the report lines and exit codes.
