# mbox Importer

This tool imports Usenet mbox archives (for example the archive.org Usenet collections) directly
into the go-pugleaf databases. It is the database-side counterpart of
[mbox2nntp](https://github.com/go-while/mbox2nntp), which feeds the same files to a news server
with IHAVE.

## Features

- Reads `.mbox`/`.bag` files, gzip (`.gz`), zip (`.zip`) and 7-Zip (`.7z`, via the installed `7z`
  program) archives, streamed: nothing beyond one article is held in memory
- Reads Usenet Archive Toolkit archives (`.usenet`, `.usenet.xz`, `.usenet.7z`; via the installed
  `zstd` program, plus `xz` or `7z`), see [UAT archives](#uat-archives)
- Several inputs per run; a directory argument imports its mbox files and UAT archives in name order
- Files every article like a fetched one, through the processor in bulk mode (message-id history,
  overview, threading): into the first group of its `Newsgroups:` header, or into `-group NAME`
- Creates missing newsgroups on the fly
- Skips articles that are already in their target group, so a run can be repeated
- Renames a fully imported input to `INPUT.done` (`-mark-done`), so a directory can be re-run
- Dry run (`-dry-run`): parses the inputs and counts per target group without opening a database
  (for UAT archives `zstd`, and `xz` or `7z`, still run)

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

### UAT archives (`.usenet`)

```bash
./build/mbox-importer -dry-run pl.rec.gory.usenet.xz
./build/mbox-importer -data ./data -tmpdir /var/tmp pl.rec.gory.usenet.xz
./build/mbox-importer -data ./data -tmpdir /var/tmp /archive/usenet.nereid.pl/   # a directory of .usenet.xz files
./build/mbox-importer -data ./data rec.games.rogue.usenet.7z
```

Import UAT archives after the mbox collections of the same groups, see [UAT archives](#uat-archives).

### Force every article into one group

```bash
./build/mbox-importer -data ./data -group local.archive.commerce de.comm.internet.commerce.mbox.gz
```

Ctrl+C (or SIGTERM) finishes the current article, starts no further input, shuts the processor
(history) and the database down cleanly and exits with code 130. The interrupted input is not
renamed; the copy of a `.usenet.xz`/`.usenet.7z` unpacked into `-tmpdir` is removed.

## Command Line Options

| Flag | Default | Meaning |
|------|---------|---------|
| `-data` | `./data` | go-pugleaf data directory (main DB, group DBs, history) |
| `-nntphostname` | (none) | Your NNTP hostname; required on the first run, later runs use the value stored in the database. It must resolve in DNS: the processor checks it at start |
| `-useshorthashlen` | `7` | Legacy short hash length (2-7). The value of the first run is stored in the database; later runs use the stored value and warn if it differs. No effect on the history index |
| `-history` | `true` | Maintain and use the message-id history index in `<data>/history` |
| `-group` | (none) | File every article of the run into this group instead of the first group of its `Newsgroups:` header |
| `-7z` | `7z` | Program used to unpack `.7z` and `.usenet.7z` inputs (name in `PATH` or a full path); also unpacks `.usenet.xz` when `-xz` is not found |
| `-zstd` | `zstd` | The zstd program (name in `PATH` or a full path), needed for every UAT input (`.usenet`, `.usenet.xz`, `.usenet.7z`) |
| `-xz` | `xz` | Program used to unpack `.usenet.xz` inputs; when it is not found, 7-Zip (`-7z`) is used with `-txz` |
| `-tmpdir` | (system temp dir) | Where `.usenet.xz` and `.usenet.7z` inputs are unpacked, into a private subdirectory that is removed afterwards, and where the zstd dictionary is written. A `.usenet.xz` or `.usenet.7z` needs free space there for its whole unpacked `.usenet` file |
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
| `NAME.mbox.7z` | the installed 7z program (`-7z`): `7z x -so -bd -r -- ARCHIVE '*.mbox' '*.bag' '*.MBOX' '*.BAG'` (members in subdirectories too, concatenated in archive order), streamed from its standard output |
| `NAME.usenet` | a [UAT archive](#uat-archives), one newsgroup per file: the messages are decompressed by one run of the zstd program (`-zstd`) over the whole message store |
| `NAME.usenet.xz` | a UAT archive compressed with xz, as published on [usenet.nereid.pl](https://usenet.nereid.pl/): unpacked with `xz -dc -- ARCHIVE` (`-xz`; if xz is not found, `7z e -so -bd -txz -- ARCHIVE`) to `NAME.usenet` in a private subdirectory of `-tmpdir`, then read like `NAME.usenet` |
| `NAME.usenet.7z` | a UAT archive in 7z, as published on archive.org ([`usenet-uat-pc-games`](https://archive.org/details/usenet-uat-pc-games)): the entry `NAME.usenet` is unpacked with the 7z program (`-7z`) to a private subdirectory of `-tmpdir`, then read like `NAME.usenet` |
| `DIR` | its files whose names contain `.mbox`, `.bag` or `.usenet` and do not end in `.done`, sorted by name; subdirectories are not entered |

A missing path, an archive that cannot be opened or unpacked, a 7z program that cannot be found and
an archive without an mbox member are input errors: the input is reported, the other inputs are
still imported, and the run exits with code 1. An empty (0-byte) mbox input counts as fully imported.
A non-empty input that is not an mbox is an input error too. A 7z that fails only at the end (for
example a CRC error) leaves the articles already imported in place and the input is not marked done.

For UAT inputs these are input errors as well: a zstd program that cannot be found (checked before
anything is unpacked), an xz or 7z that cannot be found (for `.usenet.xz`: neither of them), fails
or writes nothing, a `.usenet.7z` without the entry `NAME.usenet`, a file that is not a UAT archive
(an empty, 0-byte UAT input included), and a zstd failure or size mismatch while the messages are
read. An archive that fails mid-way is not marked done; the articles already handed to the
processor stay stored.

### Programs needed

| Input | Programs |
|-------|----------|
| `.mbox`, `.bag`, `.mbox.gz`, `.mbox.zip` | none |
| `.mbox.7z` | 7-Zip (`-7z`) |
| `.usenet` | zstd (`-zstd`) |
| `.usenet.xz` | zstd, and xz (`-xz`) or else 7-Zip (`-7z`) |
| `.usenet.7z` | zstd and 7-Zip (`-7z`) |

On Linux: the `zstd` package; the `xz-utils` package (Debian, Ubuntu) or `xz`; the `7zip` package
(Debian 13+, recent Ubuntu; on older releases `p7zip-full`, or pass `-7z 7zz`).

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

For a UAT input, "the parser reached the end of the file" means that zstd ran through the whole
message store without an error or a size mismatch. The archive given on the command line is what
gets renamed (`X.usenet.xz` becomes `X.usenet.xz.done`); the copy unpacked into `-tmpdir` is always
removed, whether or not the input was marked done.

Skipped and rejected articles do not block the rename: they are counted and logged, and a second
run would skip them again. Nothing is ever renamed in a dry run or with `-mark-done=false`. An input
whose name already ends in `.done` (a re-run of a renamed input) keeps its name, and an existing
`INPUT.done` is never replaced (the input is then logged as not marked done). An input
that is not marked done is logged with its counters; an unread rest or a stuck parser is not an
error by itself, article errors and a failed commit wait are (exit code 1).

Exit codes:

| Code | Meaning |
|------|---------|
| `0` | every input was read without an article error (marked done or not, see the log; also when there was nothing to import) |
| `1` | an input could not be opened or read (open, unpack, 7z exit status, close or rename error; for a UAT input also a missing zstd, a zstd failure or a size mismatch), an article could not be stored (`errors` > 0), or the commit wait failed; a database/processor/hostname error |
| `2` | bad command line (unknown flag, no input, invalid `-group` name, `-useshorthashlen` outside 2-7, negative `-commit-timeout`) |
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

UAT archives are not parsed as mbox, so they have no near-misses (`near-misses=0`).

## UAT archives

The [Usenet Archive Toolkit](https://github.com/wolfpld/usenetarchive) (UAT) keeps one newsgroup per
`GROUP.usenet` file, each message compressed with zstd and a dictionary. Two collections are
published:

- [usenet.nereid.pl](https://usenet.nereid.pl/): 333 Polish groups as `.usenet.xz`, 41 GB;
- archive.org [`usenet-uat-pc-games`](https://archive.org/details/usenet-uat-pc-games): 39 groups
  as `.usenet.7z`.

The reader is the package `github.com/go-while/mbox2nntp/uat`; the zstd program decompresses the
messages (see [Programs needed](#programs-needed)).

**The articles are not the originals.** The toolkit rewrote every message when it built the
archive: it decoded the headers to UTF-8, kept only the first text/plain part of the body (an HTML
part when there is none; other parts and attachments are gone) and converted it to UTF-8, and
dropped `Content-Type` and `Content-Transfer-Encoding`. The toolkit can also filter spam and
duplicates when an archive is built. Before the usual [header rewrite](#what-is-stored), the
importer makes every message a valid article again (`uat.Fix`):

- `Content-Type: text/plain; charset=UTF-8` and `Content-Transfer-Encoding: 8bit` (`7bit` for a
  plain ASCII body) are added, and `MIME-Version: 1.0` where it is missing;
- non-ASCII header text goes back into RFC 2047 encoded-words where it can be parsed: in
  `Subject:`, `Organization:` and the other free-text fields, and in the display names of `From:`,
  `Reply-To:` and the other address fields;
- `Message-ID`, `References`, `Newsgroups`, `Path`, `Date` and the other structured fields are never
  changed.

The Message-IDs are the originals. The skip reasons apply as for mbox inputs: a message with an
empty body is `no-body` (an empty message, without headers, is `no-msgid`), a body with a line
starting with `=ybegin` is `binary` (unless `-keep-binary`).

**Import UAT archives after the mbox collections.** An article whose Message-ID is already in its
target group counts as `present` and the UAT copy is not stored, so the original from an mbox
collection stays. A UAT copy imported first is the one that stays: a later mbox import of the same
article counts it as `present`.

The target group follows the same rule as for mbox inputs: the first group of the article's
`Newsgroups:` header, or `-group NAME`. The archive's own group name is only logged.

Each UAT input logs one `uat` line before the import and a second one right after its usual summary
line (with `near-misses=0`):

```
[MBOX-IMPORT] uat file="…" group="…" version=… messages=…
[MBOX-IMPORT] file="…" read=… stored=… … near-misses=0 … marked-done=…
[MBOX-IMPORT] uat file="…" re-encoded-fields=… added-mime-version=… raw-fields=…
```

- `group`, `version`, `messages`: the archive's group name, its UAT format version and its number
  of messages;
- `re-encoded-fields`: header fields written back as RFC 2047 encoded-words;
  `added-mime-version`: articles that got a `MIME-Version: 1.0` header; both are summed over every
  message read, including skipped and present ones;
- `raw-fields`: header fields left with non-ASCII in them (structured fields, and fields that could
  not be encoded, such as an address field that does not parse), as `name=count` pairs sorted by
  name, `-` when none.

The `.done` rule applies to the archive given on the command line (see
[What happens to an input](#what-happens-to-an-input)). A dry run works for UAT inputs too: it
opens no database and renames nothing, but zstd (and xz or 7z) still run, so a compressed archive is
still unpacked into `-tmpdir`.

## Log output

Every line has the prefix `[MBOX-IMPORT]`. A progress line is logged every 10000 messages, a summary
line after every input (for UAT inputs with the two `uat` lines, see [UAT archives](#uat-archives)),
then a totals line:

```
[MBOX-IMPORT] file="…" read=… stored=… present=… skipped=… (no-subject=…,bad-group=…) rejected=… errors=… parser-skipped=… near-misses=… stopped-early=… unread=… marked-done=…
[MBOX-IMPORT]   near-miss file="…" line=… "…"
[MBOX-IMPORT] done inputs=… marked-done=… input-errors=… read=… stored=… present=… skipped=… rejected=… errors=… commit-errors=…
```

A dry run also logs one `[MBOX-IMPORT] dry-run group="…" articles=…` line per target group (largest
first); there `stored` counts the articles that would be handed to the processor.

- `read`: messages the parser returned (for UAT inputs: messages read from the archive); `stored`: handed to the processor and accepted;
  `present`: already in the target group; `skipped`: see [Skip reasons](#skip-reasons) (followed by
  the non-zero counts per reason in parentheses, `-` when none); `rejected`: refused by the
  processor's bulk checks (each logged as `rejected msgid=… group=…: error`); `errors`: temporary or
  database errors (each logged as `error msgid=… group=…: error`)
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
- [github.com/go-while/mbox2nntp/uat](https://github.com/go-while/mbox2nntp) - the UAT archive reader
- github.com/mattn/go-sqlite3
- the `7z` program, only for `.7z` and `.usenet.7z` inputs (and `.usenet.xz` without `xz`)
- the `zstd` program, only for UAT inputs; the `xz` program, only for `.usenet.xz` inputs
- go-pugleaf internal packages
