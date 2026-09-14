# history-rebuild

Maintenance tool for the go-pugleaf message-id history index.

## What the history index is

A global lookup table `message-id -> newsgroup IDs` (IDs of the `newsgroups` table in
`<data>/cfg/pugleaf.sq3`). It is used for duplicate checks (posting, IHAVE, TAKETHIS)
and to find an article by message-id without searching every group database.

The per-group databases (`<data>/db/...`) are the source of truth. The index only
mirrors them, so it can always be rebuilt from the group databases.

## File layout

```
<data>/history/hashdb_0.sqlite3 ... hashdb_f.sqlite3   16 SQLite files
    tables _00 ... _ff                                 256 tables per file
        message_id TEXT PRIMARY KEY, newsgroups TEXT   newsgroups = "12,345,6789"
<data>/history/rebuild.progress                        last fully rebuilt group (resume)
```

Routing: `md5(message-id)` as hex; the 1st char selects the file, chars 2-3 select the table.
Full message-ids are stored, so there are no hash collisions. `-useshorthashlen` is still
accepted but has no effect since Nov 2025.

## Modes

| Command | What it does | Writes |
|---------|--------------|--------|
| `history-rebuild` | Rebuild: scan all groups (sorted by name) and add every article | yes |
| `history-rebuild -restart` | Rebuild from the first group, ignore/remove `rebuild.progress` | yes |
| `history-rebuild -validate-only [-verbose]` | Check every article: missing message-id, or entry without this group. `-verbose` prints the first 100 misses | no |
| `history-rebuild -analyze-only [-verbose]` | Open the 16 files read-only: rows per file (per table with `-verbose`), total, histogram of groups per message-id | no |
| `history-rebuild -trim` | Full sweep over all history rows: remove group IDs whose group is gone from the main DB, has no group DB file, or no longer has the article | yes |

Other flags: `-data <dir>` (default `./data`), `-progress N` (progress line every N articles),
`-batch-size N` (article_num range per query, default 10000), `-verbose`, `-pprof :6060`.

Exit code: 0 ok, 1 errors, 130 interrupted.

## Resuming and idempotency

- After each fully processed group its name is written to `rebuild.progress`
  (tmp file + rename). A new run skips all groups up to and including that name.
  When the run reaches the end, the file is removed.
- Ctrl+C stops after the current range. Queued history writes are flushed before exit;
  the interrupted group is not marked done and is scanned again on the next run.
- Adding is idempotent (a group ID is only appended once), so re-running a rebuild
  or rebuilding while the index already has entries is safe.
- Groups without a group DB file are skipped.

## When to use -trim

Articles deleted from group databases (expire-news without `-trim-history`, deleted groups,
manual deletes) keep their history entries, so they are still rejected as duplicates.
This is intended (INN-style remember). Run `-trim` when you want the index to match the
group databases again, e.g. after deleting groups or to reclaim space.

`-trim` is slow: every row triggers lookups in the group databases. Run it while no
fetcher / nntp-server is importing articles, otherwise a just-imported article whose
group DB write is still queued can be trimmed (a rebuild fixes that).

## Building

```bash
go build -o build/history-rebuild ./cmd/history-rebuild
```
