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
- Reads CD/DVD images and news spools: `.iso`, `.cdr`, `.sqsh` and `.img` images are unpacked into
  `-tmpdir` with the `7z` program and walked article by article, and with `-spool` a directory (a
  mounted image, an unpacked tree) or a `SPOOL.zip` is walked as it is, see
  [Disc images and news spools](#disc-images-and-news-spools)
- Several inputs per run; a directory argument imports its mbox files and UAT archives in name order,
  one after the other or `-parallel N` at a time (CSV files such as archive.org's `NAME.mbox.csv.gz` index are never inputs)
- Files every article like a fetched one, through the processor in bulk mode (message-id history,
  overview, threading): into the first group of its `Newsgroups:` header, or into `-group NAME`
- Creates missing newsgroups on the fly
- Skips articles that are already in their target group, so a run can be repeated
- Renames a fully imported input to `INPUT.done` (`-mark-done`), so a directory can be re-run
- Remembers every input, every run and the whole log output in a tracking database
  (`<data>/cfg/mbox-import.sq3`, `-trackdb off` turns it off), so an input is not imported twice
  even when it cannot be renamed, and `-status` says what is done and what is left
- Takes its inputs from the download database of mbox2nntp's `iafetch` (`-from-iafetch`), reading
  that database and the mirror only, and with `-follow D` keeps scanning it while the download runs,
  see [Importing an archive.org mirror](#importing-an-archiveorg-mirror)
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

### What the importer already did

```bash
mbox-importer -data ./data-test-mbox -status          # done, pending, unsupported, failed, last run
mbox-importer -data ./data-test-mbox -status -json    # the same for scripts
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

### Disc images and `-spool`

```bash
./build/mbox-importer -dry-run -tmpdir /big/tmp NN_1992_16.iso          # analyse the image first
./build/mbox-importer -data ./data -tmpdir /big/tmp NN_1992_16.iso      # unpacked with 7z, then walked
./build/mbox-importer -data ./data -tmpdir /big/tmp utzoo.sqsh          # 5.2 GB in 2.08M files
./build/mbox-importer -data ./data -spool /mnt/netnews-cd               # an image mounted by hand
./build/mbox-importer -data ./data -spool NN_1992_25.zip                # a zipped spool, not unpacked
```

`-tmpdir` must have room for the whole unpacked image, so give it a real directory on a big file
system. See [Disc images and news spools](#disc-images-and-news-spools).

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
| `-group-from-name` | `false` | File every article of an input into the group named by the input's file name: the base name cut before the first `.mbox`, `.bag` or `.usenet` (`de.comm.foo.mbox.gz` becomes `de.comm.foo`, `pl.comp.lang.c.usenet.xz` becomes `pl.comp.lang.c`). A file whose name gives no valid group is an input error. Not together with `-group`. This is what `mbox-import.sh` in the repository root uses for a directory of per-group archives |
| `-7z` | `7z` | Program used to unpack `.7z` and `.usenet.7z` inputs (name in `PATH` or a full path); also unpacks `.usenet.xz` when `-xz` is not found |
| `-zstd` | `zstd` | The zstd program (name in `PATH` or a full path), needed for every UAT input (`.usenet`, `.usenet.xz`, `.usenet.7z`) |
| `-xz` | `xz` | Program used to unpack `.usenet.xz` inputs; when it is not found, 7-Zip (`-7z`) is used with `-txz` |
| `-tmpdir` | (system temp dir) | Where `.usenet.xz`, `.usenet.7z` and disc image inputs are unpacked, into a private subdirectory that is removed afterwards, and where the zstd dictionary is written. A `.usenet.xz` or `.usenet.7z` needs free space there for its whole unpacked `.usenet` file; a disc image for its whole unpacked contents (`utzoo.sqsh`: 5.2 GB in 2.08M files) |
| `-spool` | `false` | Read a positional **directory** or **`.zip`** input as a news spool (one article per file) instead of scanning the directory for `*.mbox*` files: one input per argument, walked article by article like an unpacked disc image. Disc images (`.iso`, `.cdr`, `.sqsh`, `.img`) are read that way without the flag. Not together with `-from-iafetch` (which has no positional inputs). With `-spool` **every** `.zip` argument is a spool, so import mbox zips in a run of their own. See [Disc images and news spools](#disc-images-and-news-spools) |
| `-dry-run` | `false` | Parse and count only: no database is opened, no input is renamed |
| `-keep-binary` | `false` | Import yEnc binary articles (`=ybegin`) instead of skipping them |
| `-mark-done` | `true` | Rename a fully imported input to `INPUT.done` (see below) |
| `-strict-msgid` | `false` | Require Message-IDs that pass go-pugleaf's own validator instead of the lax mbox2nntp rules |
| `-max-article-size` | `1048576` | Skip articles larger than this many bytes, header and body, counted as `too-big` (mbox2nntp's 1 MiB cap); `0` = no limit. A bigger mbox article is read past without being held in memory; for UAT inputs the reader skips it |
| `-commit-timeout` | `30m` | How long to wait, per input, until the batch writer has committed its articles; `0` = no limit |
| `-parallel` | `1` | Inputs imported at the same time (1-32). Each worker takes the next input in the given order; the summary keeps that order. Every worker still hands its articles to the one batch writer, so more workers mostly help with many small inputs or slow unpacking |
| `-max-batch` | `1000` | Articles the batch writer commits per group database transaction (100-1000). Smaller batches commit sooner and hold less in memory; larger ones are faster on big inputs |
| `-max-queue` | `0` | Articles the batch writer may hold queued over all groups (16-100000) before it stops taking more. `0` sizes it from the other two flags: twice `-parallel` × `-max-batch`, at least 1280 (so `-parallel 4` gives 8000). See [Throughput](#throughput) |
| `-trackdb` | (none) | The [tracking database](#tracking-database). Empty means `<data>/cfg/mbox-import.sq3`; `off` turns the tracking off (the behaviour of the versions before it existed). A dry run never opens it, and a run with nothing to import does not create it |
| `-reimport` | `false` | Import an input the tracking database knows as done again (it is read once more; its articles are then counted as `present`) |
| `-log-db` | `true` | Write the log output of the run into the `log` table of the tracking database as well |
| `-log-keep-runs` | `0` | At start, delete the `log` rows of all but the newest N runs (`0` = keep all). N counts the run doing the deleting, so `-log-keep-runs 1` keeps only this run's lines. The `runs` and `inputs` rows are never deleted |
| `-status` | `false` | Print what the tracking database knows and exit, importing nothing (no input argument needed) |
| `-json` | `false` | Print `-status` as JSON |
| `-from-iafetch` | (none) | Take the inputs from the download database of mbox2nntp's `iafetch` at this path instead of positional arguments; see [Importing an archive.org mirror](#importing-an-archiveorg-mirror). The database is only read. Not together with positional inputs, and not with `-trackdb off` (except in a dry run, which opens no database at all) |
| `-from-iafetch-base` | (none) | iafetch's working directory: a relative `files.path` of its database is resolved against it. Needed when iafetch ran with a relative `-o`; it is never guessed. Only with `-from-iafetch` |
| `-verify-md5` | `false` | Hash an input and compare it with the md5 iafetch recorded, after the run has taken the input and before it reads it: a file the tracking database knows as `done`, or one another run holds, is never hashed. A mismatch is no error: the input becomes `pending` with the reason `md5-mismatch`, its sum is remembered (`md5_seen`) and the file is not hashed again while it is unchanged. A row without a recorded md5 cannot be checked and is logged and imported. Only with `-from-iafetch` (nothing else records an md5) |
| `-follow` | `0` | After the inputs of a scan are done, wait this long and scan iafetch's database again, until SIGINT/SIGTERM stops the run. `0` means one scan. Only with `-from-iafetch`; ignored in a dry run |
| `-help` | | Print the usage and exit |

Positional arguments are the inputs; at least one is required (except with `-status` and
`-from-iafetch`, which bring their own).

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
| `IMAGE.iso`, `IMAGE.cdr`, `IMAGE.sqsh`, `IMAGE.img` | a [disc image](#disc-images-and-news-spools) holding a news spool: unpacked with the 7z program (`-7z`) into a private subdirectory of `-tmpdir` (`7z x -y -bd -o<dir> -- IMAGE`), walked article by article, and the directory is removed again |
| `DIR` | its files whose names contain `.mbox`, `.bag` or `.usenet` and do not end in `.done`, sorted by name; subdirectories are not entered |
| `DIR` with `-spool` | the directory itself is one input: a [news spool](#disc-images-and-news-spools), walked article by article (a mounted image, an unpacked tree) |
| `SPOOL.zip` with `-spool` | the zip itself is one input: a news spool read through `archive/zip`, nothing is unpacked |

A missing path, an archive that cannot be opened or unpacked, a 7z program that cannot be found and
an archive without an mbox member are input errors: the input is reported, the other inputs are
still imported, and the run exits with code 1. An empty (0-byte) mbox input counts as fully imported.
A non-empty input that is not an mbox is an input error too. A 7z that fails only at the end (for
example a CRC error) leaves the articles already imported in place and the input is not marked done.

A disc image whose 7z run fails (not an image, a format this 7z cannot read, no space in `-tmpdir`)
is an input error too, and so is a `.zip` spool that cannot be opened; the unpack directory is
removed in every case. A disc image is an input only when it is named on the command line or comes
from `-from-iafetch`: a directory scan still looks for mbox files, not for images.

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
| a `-spool` directory or `.zip` | none |
| `.mbox.7z` | 7-Zip (`-7z`) |
| `.iso`, `.cdr`, `.sqsh`, `.img` | 7-Zip (`-7z`); a build that reads squashfs is needed for `.sqsh` (the `7zip`/`7zz` packages do, the old `p7zip` may not: `7z l utzoo.sqsh` says) |
| `.usenet` | zstd (`-zstd`) |
| `.usenet.xz` | zstd, and xz (`-xz`) or else 7-Zip (`-7z`) |
| `.usenet.7z` | zstd and 7-Zip (`-7z`) |

On Linux: the `zstd` package; the `xz-utils` package (Debian, Ubuntu) or `xz`; the `7zip` package
(Debian 13+, recent Ubuntu; on older releases `p7zip-full`, or pass `-7z 7zz`).

## What happens to an input

The inputs are imported in the order given (a directory in name order): one after the other, or
with `-parallel N` by N readers that each take the next input, every reader handing its articles to
the processor. At the end of an input the importer waits until the batch writer has committed that
input's articles (bounded by `-commit-timeout`; with parallel inputs the per-group count also covers
articles other inputs handed to the same group, which only makes the wait longer) and logs the
summary line. The log lines of parallel inputs interleave; each carries its `file=`.

### Throughput

The batch writer commits a group's articles as soon as `-max-batch` of them are queued for that
group, otherwise every 3 seconds. It stops taking articles from the readers when 80 % of
`-max-queue` are queued over all groups and resumes at 20 %. The two limits have to fit together:
with N inputs in flight the N groups share the queue, so N × `-max-batch` must stay below 80 % of
`-max-queue`, or no group ever fills a batch and the import crawls along at the 3-second timer
(about `-max-queue` articles per 3 seconds, whatever `-parallel` is). The default `-max-queue 0`
keeps that invariant by itself; set it explicitly only to bound memory (each queued article is
held in full) or when `-group` funnels all inputs into one group.

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

For a news spool input it means that the walk reached the end of the tree. A **disc image** is
renamed like any other input (`NN_1992_16.iso` becomes `NN_1992_16.iso.done`), and its unpack
directory under `-tmpdir` is removed in every case (success, input error, signal). A **spool
directory** (`-spool DIR`) is **never** renamed — it is a tree, often a mount point, not the thing
that was imported — and one line says so; only the [tracking database](#tracking-database)
remembers it. A `-spool SPOOL.zip` is a file and is renamed like any other input.

Skipped and rejected articles do not block the rename: they are counted and logged, and a second
run would skip them again. Nothing is ever renamed in a dry run or with `-mark-done=false`. An input
whose name already ends in `.done` (a re-run of a renamed input) keeps its name, and an existing
`INPUT.done` is never replaced (the input is then logged as not marked done). An input
that is not marked done is logged with its counters; an unread rest or a stuck parser is not an
error by itself, article errors and a failed commit wait are (exit code 1). With the
[tracking database](#tracking-database) on, a rename the file system refuses is not an error either:
the database remembers that the input was imported.

Exit codes:

| Code | Meaning |
|------|---------|
| `0` | every input was read without an article error (marked done or not, see the log; also when there was nothing to import) |
| `1` | an input could not be opened or read (open, unpack, 7z exit status, close or rename error; for a UAT input also a missing zstd, a zstd failure or a size mismatch; for a disc image a failed `7z x`, for a spool a failed walk), an article could not be stored (`errors` > 0), or the commit wait failed; a database/processor/hostname error, or the tracking database could not be opened |
| `2` | bad command line (unknown flag, no input, invalid `-group` name, `-group` with `-group-from-name`, `-spool` with `-from-iafetch`, `-useshorthashlen` outside 2-7, negative `-commit-timeout`, `-parallel` outside 1-32, `-max-batch` outside 100-1000, `-max-queue` outside 16-100000, negative `-max-article-size`, negative `-log-keep-runs`, `-status` with `-trackdb off`, `-from-iafetch` with positional inputs or with `-trackdb off`, negative `-follow`, `-from-iafetch-base`/`-verify-md5`/`-follow` without `-from-iafetch`, and the first scan of `-from-iafetch` failing: a database that cannot be read or a relative path without `-from-iafetch-base`) |
| `130` | stopped by SIGINT/SIGTERM while an input was being imported. A signal that reaches a `-follow` run between two inputs ends it with `0`: while it waits for the next scan, while it scans, and while it was hashing an input for `-verify-md5` (that input is left `stopped` in the tracking database) |

## Tracking database

Every run that imports something writes to one SQLite file, `<data>/cfg/mbox-import.sq3` by default
(`-trackdb PATH` for another one, `-trackdb off` for none). It is the importer's memory of what it
has seen, independent of the `INPUT.done` rename:

| Table | Holds |
|-------|-------|
| `runs` | one row per run: start and end, version, host, pid, boot id and process start time, the command line, the exit code and the totals |
| `inputs` | one row per input path: source (`cli` or `iafetch`), item identifier and file name, size, mtime, md5, the sum `-verify-md5` read when it did not match (`md5_seen`), status, reason, target group, the counters of the last import and the first/last run that saw it |
| `log` | the log lines of the runs (without the logger's own date and time: the `ts` column holds that), with their run id and, where the line names a file, its input id |

The `path` of an input row is absolute: a positional input is made absolute and cleaned before it is
recorded, so the same file given as `./g.mbox` and as `/archive/g.mbox` is one row, and an input
given twice in one command line is imported once.

The schema version is the file's `PRAGMA user_version` (currently `1`); a file written by a newer
importer is refused instead of being changed. The database is opened in WAL mode with a 30 second
busy timeout, and every transaction is a `BEGIN IMMEDIATE`.

An input row has one of these states:

| Status | Meaning |
|--------|---------|
| `pending` | known, not imported yet. With `-from-iafetch` also a file the importer could read but will not read now: `reason` is `missing` (recorded, not on disk), `size-changed`, `stat-error` or `md5-mismatch`/`md5-error` (`-verify-md5`). Every scan looks at these again |
| `importing` | a run holds it right now |
| `done` | imported |
| `failed` | an input error, article errors or a failed commit wait; `reason` says which |
| `stopped` | SIGINT/SIGTERM ended the import of this input |
| `unsupported` | the importer cannot read this file at all; `reason` says why (`tar`, `txt`, `ufs`, `xml`, `torrent`, `csv`, `unsupported-suffix`). These are final: no scan tries them again. Disc images (`iso`, `sqsh`, `img`, `.cdr`) were in this list until the importer learned to read them |

**An input is imported once.** At the start of an input the run compares the file with its row: a
row that is `done` for the same size and mtime (and the same md5, when the row and the source both
know one) means the file was imported before. It is not read again and counted in one line per run:

```
[MBOX-IMPORT] tracking: not imported: already-imported=2 pending=1 unsupported=5 held-by-another-run=0
```

A file that changed (another size or mtime) is imported again; its articles that are already in the
group are counted as `present`. `-reimport` ignores the done state and reads every input again.

**The rename is no longer the only memory.** `-mark-done` keeps its default and its meaning, but
with the tracking database on, a rename the file system refuses (`permission denied`) is logged once
for that input and is **not** an input error any more, because the database holds the state:

```
[MBOX-IMPORT] file="…" not marked done: rename …: permission denied
[MBOX-IMPORT] file="…" rename not permitted: the tracking database holds the import state
```

That is what an archive.org mirror written by another user needs. With `-trackdb off` such a rename
is an input error and the run exits 1, as before. An existing `INPUT.done` is no error either way.

**A crashed run** leaves its inputs in `importing`. The next run marks them `failed` with the reason
`interrupted` and imports them again, unless the run that holds them is really still running. That
test is identity-aware, not just a pid test: the `runs` row has to have no end time, the same host
name, the same kernel boot id (`/proc/sys/kernel/random/boot_id`, so a pid from before a reboot
never counts) and the same process start time (field 22 of `/proc/<pid>/stat`, so a pid the kernel
handed out again does not count either). Only where `/proc` gives neither value does it fall back to
asking whether the pid exists. An input a live run holds is left to that run and counted as
`held-by-another-run`, so two importers on one tracking database never import the same input. There
is no lock file.

An unfinished run of **another host** cannot be checked from here (the tracking database may sit on
a shared file system), so its inputs stay `importing` and are skipped. `-reimport` takes such a row
away from it — and any row whose run is no longer in the table — and logs one line per row:

```
[MBOX-IMPORT] tracking: -reimport takes input "…" from run 7 on host "arc1-ca"
```

A row that a run of this host is holding right now is never taken, not even with `-reimport`. If a
row is stuck in `importing` and `-reimport` is not what you want, clear it with sqlite3:

```bash
sqlite3 data/cfg/mbox-import.sq3 \
  "select i.id, i.path, i.last_run, r.host, r.pid, r.finished from inputs i
     left join runs r on r.id = i.last_run where i.status = 'importing';"
# the run above is really gone: let the next importer read these inputs again
sqlite3 data/cfg/mbox-import.sq3 \
  "update inputs set status = 'failed', reason = 'cleared by hand' where status = 'importing';"
# or close the run that holds them, so the next run's sweep does it
sqlite3 data/cfg/mbox-import.sq3 \
  "update runs set finished = datetime('now') where id = 7 and finished is null;"
```

**The log in the database.** With `-log-db` (the default) the whole log output of the process — the
importer's lines, the processor's and the database's alike — also goes into the `log` table: the
writer puts every line on a buffered channel (10000 lines) that one goroutine inserts in batches
(1000 lines or one second per transaction) and that stops with the run. It never slows the import
down: when the channel is full the line is dropped and counted, and the count is written as the last
log row and printed at the end. Nothing is deleted by itself; `-log-keep-runs N` deletes the log rows
of older runs at the start of a run (a line is about 100 bytes, so even a very large import is a few
GB at worst). `-log-db=false` keeps the `runs` and `inputs` rows and writes no log lines.

**`-status`** prints what the database knows and imports nothing (exit 0, also when there is no
database yet). `-status -json` prints the same as JSON for scripts:

```
$ mbox-importer -data ./data-test-mbox -status
tracking db: data-test-mbox/cfg/mbox-import.sq3
inputs by status:
  done                1       633 B
last run: id=3 started=… finished=… version=… exit=0 inputs=0 read=0 stored=0 present=0 skipped=0 rejected=0 errors=0
log rows: 136
```

It lists the inputs per status with their byte count, the last 10 failed inputs with their reason,
the totals of the last run and the number of log rows.

## Importing an archive.org mirror

`-from-iafetch DB` takes the inputs from the download database of mbox2nntp's `iafetch`
(`../mbox2nntp/cmd/iafetch`, `-db`, default `iafetch.sqlite`) instead of from positional arguments:
every row of its `files` table is one planned input, in the order the items were selected
(`items.ord`, then identifier and file name; rows whose item has no `ord` come last). iafetch writes
a `files` row only after the file's size and md5 matched archive.org's metadata and `FILE.part` was
renamed to `FILE`, so a recorded file is complete even while its item is still downloading — the
importer does not wait for the item's `status`.

On arc1-ca, where the ~10 TB mirror is downloaded into `/tank0/archive.org` while the import runs:

```bash
mbox-importer -data /tank0/go-pugleaf/data \
  -from-iafetch /tank0/archive.org/.iafetch/iafetch.sqlite \
  -group-from-name -parallel 4 -follow 10m
```

Run it as the user that owns the go-pugleaf data (`go-pugleaf`) in a `screen`, or from a oneshot
unit with a timer. Check iafetch's database file name before the first run, and whether iafetch ran
with an absolute `-o`: `files.path` is `filepath.Join(-o, identifier, name)` as iafetch got it on its
command line, so with a relative `-o` the paths are relative to iafetch's working directory and
`-from-iafetch-base DIR` has to name that directory. It is never guessed — a guessed folder is how
the wrong tree gets read — and a relative path without it ends the run before it starts (exit 2,
the message names the first such row).

**Nothing in the mirror is written.** iafetch's database is opened read-only
(`file:...?mode=ro&_busy_timeout=10000`), with one short query per scan, and it is closed again
before the import starts; iafetch keeps writing meanwhile. `-mark-done` is ignored with
`-from-iafetch` (one line in the log says so), so no input is renamed: the mirror's item folders
belong to the downloading user and a rename would make iafetch fetch the file again. The
[tracking database](#tracking-database) is therefore required — `-from-iafetch` with `-trackdb off`
is refused, because without it every scan would import every recorded file again. A dry run is the
exception: it imports nothing and opens no database, so `-dry-run -trackdb off` is allowed and
useful to see what a mirror holds.

**Read access to the whole database folder.** iafetch keeps its database in WAL mode, so a reader
needs the folder, the database file and its `-wal` and `-shm` files readable, not just the database
file; SQLite also wants to create or map the `-shm`. If the open or the first query fails, the error
says so:

```
iafetch DB "/tank0/archive.org/.iafetch/iafetch.sqlite": unable to open database file (the DB folder
and the -wal/-shm files next to the DB need to be readable: iafetch keeps the DB in WAL mode)
```

On arc1-ca the folder is `drwxrwsr-x mbox2nntp:archive` (setgid) and `go-pugleaf` is in the group
`archive`, so the files iafetch creates there are group-readable.

**Pending or unsupported.** An item holds much more than article archives, and a recorded file may
not be where iafetch put it any more. Both are recorded instead of being imported, and the two are
not the same:

| | Status | Reasons | On the next scan |
|---|--------|---------|------------------|
| the importer has no reader for the name | `unsupported` | `tar`, `ufs` (spool trees in formats 7z does not unpack for us), `txt` (OCR derivatives), `xml` (`_files.xml`, `_meta.xml`), `torrent`, `csv` (`NAME.mbox.csv.gz`, the offset index next to each `NAME.mbox.gz`), `unsupported-suffix` (everything else, among it the `.7z` of a raw `.bin`/`.cue` CD dump: it holds no `.mbox` member and no file system to walk) | nothing: they are listed for later tools, never imported |
| the file is not ready on disk | `pending` | `missing`, `size-changed`, `stat-error`, and with `-verify-md5` `md5-mismatch` or `md5-error` | tried again (a `md5-mismatch` that is unchanged is not hashed again) |

Supported names are the ones the importer reads anyway (`.mbox`, `.bag`, `.mbox.gz`, `.mbox.zip`,
`.mbox.7z`, `.usenet`, `.usenet.xz`, `.usenet.7z`, and the disc images `.iso`, `.cdr`, `.sqsh`,
`.img`), decided by the file's base name. A `.done` name is not an input, for an image either.
Every scan logs what it found and what it did with it:

```
[MBOX-IMPORT] scan 1: 2613 planned input(s): supported=2495 pending=3 unsupported=115
[MBOX-IMPORT] scan 1: pending missing=3
[MBOX-IMPORT] scan 1: unsupported txt=38 xml=5 unsupported-suffix=72
[MBOX-IMPORT] scan 1 done: imported=2495 already-imported=0 pending=3 unsupported=115 held-by-another-run=0 md5-stopped=0 input-errors=0 read=… stored=… present=… errors=0
```

**The disc images come first.** Within a scan every image (`.iso`, `.cdr`, `.sqsh`, `.img`) is
planned before every other input, each half still in iafetch's own order (`items.ord`, then
identifier and file name). That is deliberate: the images hold the oldest articles of the mirror,
and unpacking one takes long enough that it should not wait behind thousands of mbox files. A
zipped spool such as `NN_1992_25.zip` is **not** detected automatically (its name looks like an
mbox zip): import it by hand with `-spool`, see
[Disc images and news spools](#disc-images-and-news-spools).

`-verify-md5` compares an input with the md5 iafetch recorded for it (iafetch verified it when it
downloaded it, so this is off by default: it reads the file a second time). The check happens
**after** the run has taken the input from the tracking database and before it reads it, so only a
file that is really going to be imported is hashed — an input that is already `done`, one another
run holds and an unsupported name such as an `_djvu.txt` are never read for a sum (a disc image is
supported, so it is hashed like any other input: that reads a multi-gigabyte file twice). A mismatch is not an input error: the row goes
back to `pending` with the reason `md5-mismatch` and the sum that was read in `md5_seen`, and one
line is logged:

```
[MBOX-IMPORT] file="…/usenet-foo/de.comm.foo.mbox.zip" md5 3f1a… does not match the recorded 9b7c…: not imported, pending for the next scan
```

The next scan does **not** hash that file again while it is unchanged (same size and mtime, the
same recorded md5, and the remembered sum still differs); it logs one line and leaves the row
pending. A re-download by iafetch (another recorded md5) or a replaced file (another size or mtime)
makes it hash again. A file that cannot be read for hashing becomes `pending`/`md5-error`, and a
`files` row without an md5 is logged once per scan and imported unchecked:

```
[MBOX-IMPORT] file="…" -verify-md5: the source recorded no md5: not checked
```

The hashing stops on SIGINT/SIGTERM between two 1 MiB chunks, so even a 5 GB input does not hold up
the shutdown; the input is then left `stopped` in the tracking database and the run still ends with
exit code 0, because nothing of that input was read.

**`-follow D`** keeps the importer going: after the inputs of a scan are done it waits `D`, scans
iafetch's database again and imports what is new (a file that was `pending` and is now readable
included). The process is **one run** of the tracking database, whatever the number of scans: the
`runs` row holds the totals of all of them, and the summary lines above tell the scans apart. A scan
that fails (the database is unreadable for a moment) is logged, is **not** a scan of its own — it
gets no number and no summary line — and is tried again after the next wait; only the first scan can
end the run. SIGINT/SIGTERM while it waits, while it scans or while it hashes an input for
`-verify-md5` ends the run with exit code 0 (no input was in progress); inside an input it ends it
with 130 as usual. Without `-follow` there is exactly one scan. An empty database at start is fine:
the run keeps the tracking database and waits for iafetch to record its first file.

`-status` shows what is left to do, including the formats that need other tools:

```bash
mbox-importer -data /tank0/go-pugleaf/data -status
```

## What is stored

- **One group per article.** The target group is the first group of the article's `Newsgroups:`
  header, or `-group NAME`. Crossposts are not processed: the article is filed in that one group
  only, which is how the fetcher's bulk mode works. Missing groups are created (active) on the fly.
- **Header rewrite** before the article is handed to the processor:
  - `Xref:`, `Expires:`, `X-Mbox2nntp-Format:` and stray `From ` lines (with their continuation
    lines) are dropped;
  - NUL, backspace and tab characters become spaces;
  - lines without a colon (that are not continuations) and empty headers without a continuation
    are dropped; `Key:value` and `Key :value` are repaired to `Key: value`;
  - the `Message-ID:` header is reduced to one line `Message-ID: <msgid>` (added if it was lost);
    the ID comes from the first `Message-ID:` field; in lax mode a field holding several IDs
    (`<a@b> <c@d>`) is cut to the first, as in mbox2nntp v0.3.2;
  - a broken `Date:` line from some Google Groups headers is dropped (`isBadDateLine`, as in
    mbox2nntp);
  - `Path:` is kept as in the file; the processor prefixes it with the local hostname
    (`-nntphostname`).
- **Bodies are stored as in the file.** `>From ` lines are left untouched, line endings are
  normalised, trailing empty lines are removed. Dot-stuffing is added by the NNTP server on output.
  The exception are articles written by mbox2nntp's exporters (`uatexport`, `ncexport`, v0.3.0+):
  their header ends with `X-Mbox2nntp-Format: mboxrd`, and they are quoted mboxrd, so one `>` is
  taken off every header and body line that starts with `>`s followed by `From ` or `#! rnews `
  (old archive mboxes are not quoted: there a `>From ` line is part of the article as posted).
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
| `too-big` | is larger than `-max-article-size` (header and body; UAT: the stored message size) |

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
this is normal (`From Table 1 ...`). The RFC 4155 line most mbox writers use
(`From user@host Thu Dec  8 12:55:04 2016`, the whole line) is a known separator since
go-lib-mbox2nntp 73e35b8. If the samples still look like separators, the collection uses a
separator shape the parser does not know and articles were merged: fix that in `go-lib-mbox2nntp`
before importing the collection.

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

## Disc images and news spools

A news spool is the oldest shape of a Usenet archive: **one article per file** in a directory tree
named after the group (`SPOOL/alt/sources/1234`, `news/comp/lang/c/42`, `utzoo/net.games/17.txt`).
The CD-ROMs of the early nineties and the tape archives are spools, not mboxes, so the importer
walks such a tree directly instead of expecting a separator-delimited file.

| Input | How it is read |
|-------|----------------|
| `IMAGE.iso` | ISO 9660 (Rock Ridge and Joliet included): unpacked with `7z x -y -bd -o<dir> -- IMAGE`, then walked |
| `IMAGE.cdr` | a raw CD dump (`CDROM_March92.cdr`), the same way |
| `IMAGE.sqsh` | a squashfs image (`utzoo.sqsh`), the same way |
| `IMAGE.img` | a raw FAT floppy image (the `DOUG.*` Amiga floppies), the same way |
| `-spool DIR` | the directory is walked as it is: a mounted image, an unpacked tree, a copy from a tape |
| `-spool SPOOL.zip` | the zip is walked through `archive/zip`, nothing is unpacked (`NN_1992_25.zip`) |

The suffix decides, case-insensitively, with a trailing `.done` ignored like for every other input.
A disc image never needs `-spool`; `-spool` is for the two inputs whose name says nothing about
their contents. With `-spool`, **every** `.zip` argument is a spool, so a run with `-spool` should
not mix in `NAME.mbox.zip` files.

**Space in `-tmpdir`.** An image is unpacked in full, into a private subdirectory of `-tmpdir` that
is removed again on every path (success, input error, Ctrl-C). It needs room for the whole unpacked
contents — `utzoo.sqsh` unpacks to **5.2 GB in 2.08M files**, a NetNews CD to about 600 MB in
~190k files — and a file system that is happy with millions of small files. The default (the system
temp directory) is usually not that, so pass `-tmpdir /big/tmp`. The unpack is logged:

```
[MBOX-IMPORT] unpacking disc file="…/NN_1992_16.iso" (650117120 bytes) to "/big/tmp/mbox-importer-disc-123" with 7z
[MBOX-IMPORT] unpacked disc file="…/NN_1992_16.iso" to "/big/tmp/mbox-importer-disc-123"
```

A **mounted image** is the way around the unpack when the space is not there (and the only way for
a format this `7z` cannot read):

```bash
fuseiso NN_1992_16.iso /mnt/x          # ISO only; squashfuse for .sqsh, mount -o loop as root
mbox-importer -data ./data -spool /mnt/x
fusermount -u /mnt/x
```

**7z runs in its own process group.** Ctrl-C therefore does not reach it directly: the importer
kills it (and its children), removes the unpack directory and ends the input as stopped, exit code
130. A failed `7z x` is an input error (exit code 1) and the directory goes too.

### What the walk counts

A spool tree holds much more than articles — `LIB/ACTIVE`, `BIN/`, `.overview` files, `YMTRANS.TBL`,
tarballs, `.Z` files — so every file is classified by its first bytes, not by its name, and the
per-input summary line gains four counters:

```
[MBOX-IMPORT] file="…/NN_1992_16.iso" read=… stored=… present=… skipped=… (…) rejected=… errors=… … files=191203 not-article=4821 xref-pointers=12044 anews-batches=0
```

- `files`: regular files walked;
- `not-article`: files that are no article, no A-news batch and no mbox (binaries, archives,
  `README`, `.overview`, `YMTRANS.TBL`);
- `xref-pointers`: the `<n>.XRF` files of the NetNews CDs, a crosspost pointer to another group's
  article (`alt/cad/autocad/1005`), counted and not read — the article itself is in the group the
  pointer names, and crossposts are not processed by this importer anyway;
- `anews-batches`: files in the old A-news batch format (several articles in one file), as in the
  earliest utzoo tapes.

An mbox file inside a spool (the `DOUG.*` floppies are plain mboxes without a suffix) is read with
the normal mbox reader, so its articles are counted in `read`/`stored` like any other.

### Message-IDs of the old formats

Most spool articles carry full RFC 1036 headers and keep their own `Message-ID`. Two older shapes
do not, and the IDs the importer gives them are **Google Groups' form**, so the same article from
another archive is recognised as a duplicate:

| Article | Message-ID |
|---------|------------|
| A-news (`Aalice.154`, no header names, newsgroups/path/date/title lines) | `<anews.Aalice.154>` |
| early B-news with `Article-I.D.: decvax.123` and no `Message-ID` | `<bnews.decvax.123>` (the `Article-I.D.` line stays) |
| a stripped `comp.sources` posting (`Subject`/`Newsgroups`/`Approved` only, no `Message-ID`) | none: it is skipped as `no-msgid` |

The A-news header is rebuilt from the batch: `Path` and `From: <site>!<user>` from the path line,
`Newsgroups`, `Subject` from the title and `Date` from the ctime line (converted to RFC 5322,
`+0000`).

### Analyse every image before importing it

A dry run opens no database, stores nothing and renames nothing, but it unpacks the image and walks
it, so it is the cheap way to see what a disc holds (and whether the counters look sane) before an
import writes to the group databases:

```bash
for i in /tank0/archive.org/*/*.iso /tank0/archive.org/*/*.sqsh; do
  mbox-importer -dry-run -trackdb off -tmpdir /big/tmp "$i"
done
```

Read the `files=… not-article=… xref-pointers=… anews-batches=…` counter line, the target groups
(`dry-run group="…" articles=…`) and the [skip reasons](#skip-reasons) of each image. A disc whose
`not-article` is most of its `files` is a disc of software, not of articles.

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

A run with the [tracking database](#tracking-database) logs its file and run id at the start, one
line for the inputs it did not import, and one line per unusual event (an interrupted input of an
earlier run, deleted log rows, dropped log lines):

```
[MBOX-IMPORT] tracking db="data/cfg/mbox-import.sq3" run=7
[MBOX-IMPORT] tracking: not imported: already-imported=2 unsupported=5 held-by-another-run=0
```

Unless `-log-db=false` is given, every one of these lines (and every line of the processor and the
database) is stored in the `log` table of that database as well.

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
- `files`, `not-article`, `xref-pointers`, `anews-batches`: only for a news spool input (a disc
  image or `-spool`), see [What the walk counts](#what-the-walk-counts)

## Notes

- `-nntphostname` must resolve in DNS: the processor validates it when it starts.
- When testing, run against a scratch root (`-data ./data-test-<name>`), never against live data.
- Run one importer per data directory at a time (SQLite). Two importers that share only the
  tracking database (different data directories) do not import the same input: see
  [Tracking database](#tracking-database).
- The import is sequential: one input and one article at a time.
- With `-history=false` no history entries are written; `history-rebuild` can backfill them later.

## Dependencies

- Go (see `go.mod` for the version)
- [github.com/go-while/go-lib-mbox2nntp](https://github.com/go-while/go-lib-mbox2nntp) - the mbox parser
- [github.com/go-while/mbox2nntp/uat](https://github.com/go-while/mbox2nntp) - the UAT archive reader
- github.com/mattn/go-sqlite3
- the `7z` program, only for `.7z`, `.usenet.7z` and disc image inputs (and `.usenet.xz` without `xz`)
- the `zstd` program, only for UAT inputs; the `xz` program, only for `.usenet.xz` inputs
- go-pugleaf internal packages
