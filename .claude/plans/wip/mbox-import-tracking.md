# Plan: mbox-import-tracking — import tracking DB, logs in SQLite, inputs from iafetch's DB

## Context
`cmd/mbox-importer` (testing-001 0ae659e) remembers what it imported only by renaming an input to `INPUT.done`
(`-mark-done`, default true). Its log goes to stderr. Nothing records per-input results across runs.

On arc1-ca (memory `arc1-ca-deploy`) the importer is going to read the archive.org mirror `/tank0/archive.org`
(~10 TB, ~2,600 items, written by user `mbox2nntp`, group `archive`):
- The item folders are `drwxrwxr-x mbox2nntp:mbox2nntp`: user `go-pugleaf` can read but **not rename**, so
  `-mark-done` fails on every input there. A failed rename that is not `os.ErrExist` makes the input an input error
  (`import.go`, `markDone` branch), and the run exits 1. Renaming would also make the downloader fetch the file again.
- The downloader is mbox2nntp's `iafetch` (`../mbox2nntp/cmd/iafetch`; the user, 2026-11-06: the `iadl.py` run on
  arc1-ca was a mistake, the mbox2nntp agent switches to iafetch). Its SQLite DB (`-db`, default `iafetch.sqlite`;
  modernc driver, WAL, `busy_timeout(10000)`) has
  `items(identifier PK, ord, tier, list, status, files, bytes, note, updated)` — status `done` = every selected file
  downloaded and verified; others `incomplete`, `dark`, `missing`, `empty` — and
  `files(identifier, name, size, md5, path, done_at, PK(identifier, name))`. A `files` row is written only after the
  file's size and md5 matched the metadata and `FILE.part` was renamed to `FILE` (`fetch.go`), so a recorded file is
  complete even while its item is still downloading. Only `source == original` files that are not archive.org's
  generated ones (`isGenerated`) are selected; `-include`/`-exclude` narrow that. `path` is
  `filepath.Join(-o, identifier, name)` as given on iafetch's command line, so it can be **relative to iafetch's
  working directory** (`-o` defaults to `ia-download`). Files already on disk that match are adopted and recorded,
  so the tree `iadl.py` wrote is picked up by iafetch's first run.
- An item holds more than article archives: `*.mbox.zip`/`*.mbox.gz` (most), `_files.xml`, `_meta.xml`,
  `_meta.sqlite`, `_archive.torrent`, OCR derivatives (`_djvu.txt`, `_hocr.html`, `_jp2.zip`, ...), and formats the
  importer cannot read yet (`.iso` CD images, `.sqsh`, `.tar`, `.ufs`, `.img`, plain `.txt`).
- The download takes ~30 h, so files finish while imports run.

The user asked (2026-11-06): read the downloader's tracking, keep our own import DB, keep the log output in SQLite.

## Design
### 1. Tracking DB (`cmd/mbox-importer/track.go`, new)
- File `-trackdb PATH`, default `<data>/cfg/mbox-import.sq3`; `-trackdb off` disables it (today's behaviour).
  Not opened with `-dry-run`. Opened with the same driver and pragmas as the group DBs (WAL, busy timeout);
  writes through `internal/database`'s retry helpers (`RetryableExec`, `RetryableTransactionExec`, ...).
- Schema (versioned with `PRAGMA user_version`, version 1):
  - `runs(id INTEGER PK, started TEXT, finished TEXT, version TEXT, host TEXT, args TEXT, exit_code INTEGER,
    inputs INTEGER, read, stored, present, skipped, rejected, errors INTEGER)`.
  - `inputs(id INTEGER PK, path TEXT UNIQUE NOT NULL, source TEXT NOT NULL /* cli|iafetch */, identifier TEXT,
    name TEXT, size INTEGER, mtime INTEGER, md5 TEXT, status TEXT NOT NULL, reason TEXT, group_name TEXT,
    read, stored, present, skipped INTEGER, skipped_detail TEXT, rejected, errors, parser_skipped, near_misses INTEGER,
    first_run INTEGER, last_run INTEGER, started TEXT, finished TEXT)`; index on `(status)`, `(identifier)`.
    `status`: `pending` (known, not imported), `importing` (a run holds it), `done`, `failed` (input error, article
    errors or commit-wait failure), `stopped` (signal), `unsupported` (reason: suffix, derivative, part file).
  - `log(id INTEGER PK, run_id INTEGER NOT NULL, input_id INTEGER, ts TEXT NOT NULL, msg TEXT NOT NULL)`; index on
    `(run_id)`, `(input_id)`.
- **Done = same file:** an input whose row is `done` with the same `size` and `mtime` (and the same `md5` when the
  source names one) is skipped as `already-imported` (counted in the run summary, one log line per run with the
  count, not per file). A changed file is imported again (its articles already in the group count as `present`).
  `-reimport` ignores the done state.
- **Rename vs tracking:** `-mark-done` keeps its default and meaning. With the tracking DB on, a rename that fails
  with a permission error (`errors.Is(err, fs.ErrPermission)`) is logged once per input and is **not** an input error
  (the DB holds the state). Without the DB, today's behaviour stays.
- **A run left `importing` by a crash** is `failed` with reason `interrupted` on the next run's start.
- **Two runs on one tracking DB:** the run takes `BEGIN IMMEDIATE` to flip a row `pending|failed|stopped` →
  `importing`; a row another live run holds (`importing`, `last_run` not finished) is skipped. No lock file.
- **Log tee:** `log.SetOutput(io.MultiWriter(os.Stderr, tw))` for the whole process (importer, processor, database
  lines alike). `tw` puts each line on a buffered channel (capacity 10000) read by one goroutine that inserts
  batches (1000 lines or 1 s, one transaction). The writer never blocks the import: a full channel drops the line
  and counts it; the count is written as one last log row and printed at the end. `input_id` comes from the
  importer's own context (the worker sets the current input in the line's prefix via a per-input logger helper,
  or the tee parses `file="..."` of `[MBOX-IMPORT]` lines; pick the one that needs no change to shared packages).
  The goroutine stops after the import ends: close the channel, drain, close the DB (no unbounded goroutine).
  `-log-db=false` keeps the run and input rows but not the log lines.
- **`-status`**: prints from the tracking DB without importing: inputs per status (count, bytes), the last 10
  failed inputs with reason, the last run's totals; exit 0. `-status -json` for scripts.
- Retention: none (a log line is ~100 bytes; a full 10 TB import with per-article processor warnings is a few GB at
  worst). `-log-keep-runs N` (0 = all) deletes older runs' log rows at start. Documented in the README.

### 2. Inputs from iafetch's DB (`cmd/mbox-importer/iasource.go`, new)
- `-from-iafetch DB`: the inputs are the rows of `files`, joined with `items` for the order: `ORDER BY items.ord,
  files.identifier, files.name` (rows without an `items` row last, by identifier). Any recorded file is complete,
  whatever its item's status, so a file is imported as soon as iafetch recorded it.
- Paths: an absolute `path` is used as is; a relative one is joined to `-from-iafetch-base DIR` (iafetch's working
  directory). A relative path without `-from-iafetch-base` is an error at start (exit 2, the message names the first
  such row). `-from-iafetch-base` defaults to nothing on purpose: guessing the folder is how the wrong tree gets read.
- The DB is opened read-only (`file:...?mode=ro&_busy_timeout=10000`, the mattn driver go-pugleaf already uses).
  A WAL DB needs its `-wal`/`-shm` readable and the folder's `-shm` present: if the open or the first query fails
  for that, the error says so (the fix is read access for group `archive` on iafetch's DB folder); no copy, never a
  write. Reads are short (one query per scan); iafetch keeps writing meanwhile.
- A row → supported when its name has a suffix the importer reads today (same list as the directory scan in
  `inputs.go`: `.mbox`, `.bag`, `.mbox.gz`, `.mbox.zip`, `.mbox.7z`, `.usenet`, `.usenet.xz`, `.usenet.7z`; factor
  that list into one function used by both). Others are recorded `unsupported` with the reason (`iso`, `sqsh`, `tar`,
  `txt`, `ufs`, `img`, `xml`, `torrent`, other suffix) so `-status` shows what is left for later tools (CD images,
  spool trees). A recorded file missing on disk, or with another size than recorded, is `pending` with reason
  `missing`/`size-changed` and not imported this scan.
- The recorded md5 is stored in `inputs.md5`. `-verify-md5` checks it before import (off by default: iafetch
  verified it).
- The target group still follows `-group` / `-group-from-name` / first `Newsgroups:` group as today.
- `-follow DURATION`: after the inputs of a scan are done, wait DURATION, scan again, import the new ones; stops on
  SIGINT/SIGTERM like a normal run (exit 130 only when stopped inside an input, 0 when stopped while waiting).
  Without `-follow` one scan.
- Positional inputs and `-from-iafetch` exclude each other (exit 2).

### 3. Pinned
- No change to `internal/` packages (the tee lives in the command).
- iafetch's files and DB are only read, never written or renamed by the importer (`-mark-done` is ignored with
  `-from-iafetch`, logged once).
- CD images and other unsupported formats are out of scope (they are listed, not imported).

## Waves
### Wave 0 (inline, 517b177)
`cmd/mbox-importer/planned.go`: `plannedInput` (Path, Source, Identifier, Name, Size, Mtime, MD5, Supported, Reason)
and the source constants `sourceCLI`, `sourceIAFetch`. Both wave-1 slices use it; neither edits it. The suffix rule
is the existing `isInputName` in `inputs.go` (no slice owns `inputs.go`). New files in `cmd/mbox-importer/` need
`git add -f` (the `.gitignore` entry for the binary matches the folder).
### Wave 1 (parallel)
- slice `trackdb`: owns `cmd/mbox-importer/track.go`, `track_test.go` (new), `import.go`, `uat.go`, `main.go`,
  `README.md` (sections tracking/status/logs). Hooks: run start/end, input begin/end with counters, done-skip,
  rename-permission rule, interrupted rows, log tee, `-trackdb`, `-reimport`, `-log-db`, `-log-keep-runs`,
  `-status [-json]`. Expose `func (t *tracker) recordPlanned(in plannedInput) error` for wave 2 (`plannedInput`
  from wave 0).
  Tests: schema created and versioned; done-skip on same size/mtime, reimport on change and with `-reimport`;
  `importing` left by a crash becomes `failed/interrupted`; permission-denied rename is no input error with the DB and
  stays one without; log rows written with run and input ids; a blocked DB does not block the import (drop count);
  shutdown drains and closes (no goroutine left: `goleak`-style count via `runtime.NumGoroutine` before/after is a
  timing test: don't; check the writer's done channel instead); `-status` output; `-trackdb off` = today.
- slice `iasource`: owns `cmd/mbox-importer/iasource.go`, `iasource_test.go` (new); uses `isInputName` from
  `inputs.go` unchanged. Returns `[]plannedInput` in import order for `-from-iafetch`.
  Tests: a fixture DB created with iafetch's schema (copy the DDL from `../mbox2nntp/cmd/iafetch/db.go` into the test
  with a comment naming the source commit) in WAL mode: items with statuses done/incomplete/dark and their files
  (mbox.zip, mbox.gz, .usenet.xz, .iso, .txt), absolute and relative paths → `ord` order, files of an incomplete
  item included, supported/unsupported reasons, md5, relative paths joined to the base and refused without it,
  missing and size-changed files `pending`; a read-only folder holding the WAL DB (error names the cause or the read
  works: pin what SQLite does).

### Wave 2 (one implementer, after merge)
- `main.go`/`import.go`: flags `-from-iafetch`, `-from-iafetch-base`, `-verify-md5`, `-follow`; feed `plannedInput`s through `recordPlanned` and the existing worker pool; `-mark-done`
  ignored for `-from-iafetch`; exclusions (exit 2). Tests in `track_test.go`/`main_test.go`: a `-from-iafetch` run
  imports the supported files, records the rest, a second run imports nothing; `-follow` picks up a `files` row added
  between scans (drive the scan loop with an injected tick channel, no sleeps).
- README: a section "Importing an archive.org mirror" with the arc1-ca command line.

## Checks
gofmt on touched dirs, `go vet ./...`, `go build ./...`, the full race test list from `.claude/CLAUDE.md`.

## End-to-end (pugleaf-verifier, scratch data only)
1. Under `./data-test-iatrack/mirror` put two items with small mbox.zip files (one article set crossing two groups),
   an `.iso` file and a `.txt`; create an iafetch DB `./data-test-iatrack/iafetch.sqlite` with iafetch's schema and
   rows for them (relative paths `mirror/<id>/<name>`), WAL mode. If the verifier can build `../mbox2nntp/cmd/iafetch`
   and has network, a real `iafetch -o mirror -match '^usenet-aaa$'` run is better; otherwise the hand-made DB.
2. `./build_mbox-importer.sh`; run `build/mbox-importer -data ./data-test-iatrack/data -nntphostname <resolvable>
   -from-iafetch ./data-test-iatrack/iafetch.sqlite -from-iafetch-base ./data-test-iatrack -group-from-name`:
   articles stored; `-status` shows done/unsupported.
3. Run again: 0 imported, `already-imported` count in the summary.
4. Start with `-follow 2s` in the background, insert a `files` row for a third mbox.zip, see it imported, SIGINT →
   exit 0.
5. `sqlite3 ./data-test-iatrack/data/cfg/mbox-import.sq3 'select count(*) from log'` > 0; rows carry input ids.
6. A positional run on a read-only folder: rename refused, not an input error, row `done`.
7. Remove `./data-test-iatrack`.

## Deploy note (arc1-ca, after merge and push)
iafetch's DB folder is `/tank0/archive.org/.iafetch/` (2026-11-06: `drwxrwsr-x mbox2nntp:archive`, setgid; go-pugleaf
is in `archive`, checked). Files created there get group `archive`; with mbox2nntp's umask 022 the DB, `-wal` and
`-shm` are group-readable. The DB was still empty on 2026-11-06 16:47 (iadl.py still running, iafetch not started).
Before the first run: check the DB file name and whether iafetch runs with an absolute `-o /tank0/archive.org` (then
`files.path` is absolute and `-from-iafetch-base` is not needed; otherwise pass iafetch's working directory).

`mbox-importer -data /tank0/go-pugleaf/data -from-iafetch /tank0/archive.org/.iafetch/<db> -group-from-name
-parallel 4 -follow 10m` as user go-pugleaf in a `screen`, or a oneshot/timer unit later.

## Progress
### Wave 0 (2026-11-06, inline 517b177 + plan note 30a53bc)
### Wave 1 (2026-11-06)
- slice `iasource`: branch worktree-agent-ab233f85c18dc4ccd (a67df74, 7e22add; review fixes bbba79f: escaped DSN
  `iafetchDSN`, read-only pinned by tests incl. cross-process writer, `iafetchPending`, size from disk when the DB
  has <= 0, supported = isInputName && readable suffix (.mbox/.bag/.gz/.zip/.7z/UAT)). Merged 6075719.
- slice `trackdb`: branch worktree-agent-ab6959be19ac4648d (5416a1e; review fixes a1b301d: run liveness by host +
  boot id + process start time, `-reimport` takes rows of foreign-host runs, README recovery SQL, path Abs/Clean +
  `dedupePlanned`, tee never closes its data channel, escaped DSNs, log prefix stripped). Merged c1904c0.
  Deviations kept: `runs.pid/boot_id/pstart`; DB not opened when nothing to import; tracking write errors don't
  change the exit code.
- For wave 2: `trackPlannedStatus` maps every unsupported input to `unsupported`; it must map
  `iafetchPending(reason)` to `pending`.
