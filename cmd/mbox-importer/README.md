# mbox Importer

This tool imports Usenet mbox archives (for example the archive.org Usenet collections) directly
into the go-pugleaf databases. It is the database-side counterpart of
[mbox2nntp](https://github.com/go-while/mbox2nntp), which feeds the same files to a news server
with IHAVE.

## Features

- Reads `.mbox`/`.bag` files, gzip (`.gz`), zip (`.zip`) and 7-Zip (`.7z`, via the installed `7z`
  program) archives, streamed: nothing beyond one article is held in memory
- Several inputs per run; a directory argument imports its mbox files in name order
- Files every article like a fetched one, through the processor in bulk mode (message-id history,
  overview, threading): into the first group of its `Newsgroups:` header, or into `-group NAME`
- Creates missing newsgroups on the fly
- Skips articles that are already in their target group, so a run can be repeated
- Renames a fully imported input to `INPUT.done` (`-mark-done`), so a directory can be re-run
- Dry run (`-dry-run`): parses the inputs and counts per target group without opening a database

## Building

```bash
./build_mbox-importer.sh       # writes build/mbox-importer, with the version from appVersion.txt
# or
go build -o build/mbox-importer ./cmd/mbox-importer
```

## Usage

`-nntphostname` is needed on the first run against a data directory; later runs read it from the
database. When testing, always use a scratch root such as `-data ./data-test-<name>`: the default
`-data ./data` is the live data directory.

### Dry run first

```bash
./build/mbox-importer -dry-run de.comm.internet.commerce.mbox
```

Opens no database, renames nothing, and prints the per-group counts, the skip reasons and the
near-miss samples (see [Near-misses](#near-misses)).

### Plain mbox

```bash
./build/mbox-importer -data ./data -nntphostname news.example.org de.comm.internet.commerce.mbox
```

### Compressed archives

```bash
./build/mbox-importer -data ./data de.comm.internet.commerce.mbox.gz
./build/mbox-importer -data ./data de.comm.internet.commerce.mbox.zip
./build/mbox-importer -data ./data -7z /usr/bin/7z de.comm.internet.commerce.mbox.7z
```

### A directory

```bash
./build/mbox-importer -data ./data /archive/usenet/de.comm/
```

### Force every article into one group

```bash
./build/mbox-importer -data ./data -group local.archive.commerce de.comm.internet.commerce.mbox.gz
```

Ctrl+C (or SIGTERM) finishes the current article, starts no further input, shuts the processor
(history) and the database down cleanly and exits with code 130. The interrupted input is not
renamed.

## Command Line Options

| Flag | Default | Meaning |
|------|---------|---------|
| `-data` | `./data` | go-pugleaf data directory (main DB, group DBs, history) |
| `-nntphostname` | (none) | Your NNTP hostname; required on the first run, later runs use the value stored in the database. It must resolve in DNS: the processor checks it at start |
| `-useshorthashlen` | `7` | Legacy short hash length (2-7). The value of the first run is stored in the database; later runs use the stored value and warn if it differs. No effect on the history index |
| `-history` | `true` | Maintain and use the message-id history index in `<data>/history` |
| `-group` | (none) | File every article of the run into this group instead of the first group of its `Newsgroups:` header |
| `-7z` | `7z` | Program used to unpack `.7z` inputs (name in `PATH` or a full path) |
| `-dry-run` | `false` | Parse and count only: no database is opened, no input is renamed |
| `-keep-binary` | `false` | Import yEnc binary articles (`=ybegin`) instead of skipping them |
| `-mark-done` | `true` | Rename a fully imported input to `INPUT.done` (see below) |
| `-strict-msgid` | `false` | Require Message-IDs that pass go-pugleaf's own validator instead of the lax mbox2nntp rules |
| `-commit-timeout` | `30m` | How long to wait, per input, until the batch writer has committed its articles; `0` = no limit |
| `-help` | | Print the usage and exit |

Positional arguments are the inputs; at least one is required.

## Inputs

| Input | How it is read |
|-------|----------------|
| `NAME.mbox`, `NAME.bag` | the file itself |
| `NAME.mbox.gz` | gzip, streamed (concatenated gzip members are read as one stream) |
| `NAME.mbox.zip` | zip: the first member named `*.mbox` or `*.bag`, else the only member, streamed |
| `NAME.mbox.7z` | the installed 7z program (`-7z`): `7z x -so -bd -- ARCHIVE '*.mbox' '*.bag'`, streamed from its standard output |
| `DIR` | its files whose names contain `.mbox` or `.bag` and do not end in `.done`, sorted by name; subdirectories are not entered |

A missing path, an archive that cannot be opened or unpacked, a 7z program that cannot be found and
an archive without an mbox member are input errors: the input is reported, the other inputs are
still imported, and the run exits with code 1. An empty (0-byte) input counts as fully imported.
A non-empty input that is not an mbox is an input error too. A 7z that fails only at the end (for
example a CRC error) leaves the articles already imported in place and the input is not marked done.

## What happens to an input

The inputs are imported one after the other, in the order given (a directory in name order), by one
reader that hands each article to the processor. At the end of an input the importer waits until
the batch writer has committed that input's articles (bounded by `-commit-timeout`) and logs the
summary line.

The input is renamed to `INPUT.done` when all of these hold:

- every article that was not skipped or rejected was stored or was already present in its target
  group,
- the parser reached the end of the file (it did not stop early and nothing was left unread),
- no article failed with an error, the archive unpacked cleanly (7z exit status) and the commit
  wait finished.

Skipped and rejected articles do not block the rename: they are counted and logged, and a second
run would skip them again. Nothing is ever renamed in a dry run or with `-mark-done=false`. An input
that is not marked done is logged with its counters; it is not an error by itself.

Exit codes:

| Code | Meaning |
|------|---------|
| `0` | every input was read (marked done or not, see the log) |
| `1` | an input could not be opened, unpacked or started, or a database/processor error |
| `2` | bad command line (unknown flag, no input) |
| `130` | stopped by SIGINT/SIGTERM |

## What is stored

- **One group per article.** The target group is the first group of the article's `Newsgroups:`
  header, or `-group NAME`. Crossposts are not processed: the article is filed in that one group
  only, which is how the fetcher's bulk mode works. Missing groups are created (active) on the fly.
- **Header rewrite** before the article is handed to the processor:
  - `Xref:`, `Expires:` and stray `From ` lines (with their continuation lines) are dropped;
  - NUL, backspace and tab characters become spaces;
  - lines without a colon (that are not continuations) and empty headers without a continuation
    are dropped; `Key:value` and `Key :value` are repaired to `Key: value`;
  - the `Message-ID:` header is reduced to one line `Message-ID: <msgid>` (added if it was lost);
  - a broken `Date:` line from some Google Groups headers is dropped (`isBadDateLine`, as in
    mbox2nntp);
  - `Path:` is kept as in the file; the processor prefixes it with the local hostname
    (`-nntphostname`).
- **Bodies are stored as in the file.** `>From ` lines are left untouched, line endings are
  normalised, trailing empty lines are removed. Dot-stuffing is added by the NNTP server on output.
- **Already present** articles (the Message-ID is already in the target group) are counted as
  `present` and not processed again.
- **Threading uses `References:` only.** A reply that is imported before its parent gets no thread
  root. Import the files of a group in chronological order (archive mbox files usually are);
  `recover-db -data <dir> -group NAME -rebuild-threads -force` rebuilds the thread relationships
  later.

## Skip reasons

Skipped articles are counted per reason in the summary line.

| Reason | The article ... |
|--------|-----------------|
| `no-msgid` | has no Message-ID |
| `bad-msgid` | has a Message-ID that fails the check (lax by default: `<...>` without whitespace after the mbox2nntp repairs; `-strict-msgid`: go-pugleaf's validator) |
| `no-body` | has an empty body (for example a cancel) |
| `binary` | is a yEnc binary (`=ybegin`) and `-keep-binary` is not set |
| `no-date` | has no `Date:` header |
| `no-subject` | has no `Subject:` header |
| `too-many-headers` | has more than 256 header lines |
| `no-newsgroups` | has no `Newsgroups:` header |
| `bad-group` | has a first group in `Newsgroups:` that is not a valid group name (no fallback to a later group) |

With `-group`, the first group is not looked up, so `bad-group` does not apply. `no-newsgroups`
still applies, because the processor requires the header.

**Rejected** articles passed the importer's checks but failed the processor's bulk checks: more than
9 groups in `Newsgroups:`, no valid group name at all in `Newsgroups:` (only possible with
`-group`), an empty `From:`, a `Date:` that cannot be parsed or lies more than 24 h
in the future, or a duplicate core header (`Date`, `From`, `Message-ID`, `Newsgroups`, `Path`,
`Subject`). Each rejection is logged with the processor's error.

## Near-misses

The parser reports body lines that start like a message separator (`From ` or `#! rnews `) but were
*not* taken as one, and up to five samples are logged with their line numbers. In ordinary prose
this is normal (`From Table 1 ...`). If the samples look like separators
(`From user@host Thu Dec  8 ...`), the collection uses a separator shape the parser does not know
and articles were merged: fix that in `go-lib-mbox2nntp` before importing the collection.

**Dry-run one file of every new collection first and read the near-miss samples.**

## Log output

Every line has the prefix `[MBOX-IMPORT]`. A progress line is logged every 10000 messages, a summary
line after every input, then a totals line:

```
[MBOX-IMPORT] file=… read=… stored=… present=… skipped=… rejected=… errors=… parser-skipped=… near-misses=… stopped-early=… unread=… marked-done=…
```

- `read`: messages the parser returned; `stored`: handed to the processor and accepted;
  `present`: already in the target group; `skipped`: see [Skip reasons](#skip-reasons) (followed by
  the count per reason); `rejected`: refused by the processor's bulk checks; `errors`: temporary or
  database errors
- `parser-skipped`: malformed messages the parser stepped over (the rest of the file is still read)
- `near-misses`: see [Near-misses](#near-misses)
- `stopped-early=true`: the parser lost its place before the end of the file
- `unread=true`: the stream ended before the underlying file did
- `marked-done`: whether the input was renamed to `INPUT.done`

## Notes

- `-nntphostname` must resolve in DNS: the processor validates it when it starts.
- When testing, run against a scratch root (`-data ./data-test-<name>`), never against live data.
- Run one importer per data directory at a time (SQLite).
- The import is sequential: one input and one article at a time.
- With `-history=false` no history entries are written; `history-rebuild` can backfill them later.

## Dependencies

- Go (see `go.mod` for the version)
- [github.com/go-while/go-lib-mbox2nntp](https://github.com/go-while/go-lib-mbox2nntp) - the mbox parser
- github.com/mattn/go-sqlite3
- the `7z` program, only for `.7z` inputs
- go-pugleaf internal packages
