# Plan: mbox-importer reads CD images and news spools

## Context

The archive.org mirror on arc1-ca (`/tank0/archive.org`, fetched by iafetch, database
`/tank0/archive.org/.iafetch/iafetch.sqlite`, absolute paths) holds 25 disc images that
`-from-iafetch` records as `unsupported` (`iso`, `sqsh`, `img`; `.cdr` as `unsupported-suffix`).
The user wants them imported among the first items. Analysed on 2026-11-06 with `7z l` / `7z e`
(read-only, no mount):

| Image(s) | Layout inside | Articles (approx.) |
|---|---|---|
| `nn-1992-{7,16..31}`, `nn-1993-{1,3}` `NN_*.iso` (13), NetNews/CD 1992-93 | `SPOOL/<GROUP>/<PATH>/<n>` one article per file, full RFC 1036 headers; `<n>.XRF` files are crosspost pointers (content `alt/cad/autocad/1005`); `LIB/ACTIVE`, `BIN/`, `SRC/`, `MAN/` | ~2.1M files |
| `nn-1992-25/NN_1992_25.zip` | the same disc as a zip (`UsenetCDROM/SPOOL/...`, 185,858 files) | ~170k |
| `nn-1992-7/[PC] Netnews CD April 06-20 1992.7z` | `.bin`/`.cue` raw CD of the same disc as `NETNEWS_CD_APR06_APR20_92-01.iso` | duplicate, leave out |
| `NetNewsOffline-Vol{1,2}` (Amiga GRn, 1995) | `news/<group/path>/<n>` one article per file, full headers; `.overview`, `.next` per group | ~443k |
| `utzoo-squashed-fs/utzoo.sqsh` (Henry Spencer's utzoo tapes, 1981-91) | `<tape>/<group/path>/<n>.txt` one article per file (B-news, often `Xref:` first); `news00Nf1/aNN...` early files are **A-news batches** (several articles per file, see below); `Scavenged/` partial | ~2.08M files |
| InfoMagic USENET CD 1993-07, 1994-01, 1994-10 disk 1+2; `CDROM_March92.cdr` | `answers/` (FAQs, full headers), `altsrc/articles/`, `USENET/ALTSRCS/` (full headers); `sources/<unix|misc|x|games|...>/volumeN/<pkg>/partNN` comp.sources postings, **often stripped headers** (Subject/Newsgroups/Approved, no Message-ID/Date/From); plus `.Z`, `.tar`, `.zip`, `.arc`, `.exe`, `YMTRANS.TBL` (not articles) | tens of thousands |
| `usenet-comp-sys-amiga-1994-doug/*.img` (3 FAT floppies) | `DOUG.PRO`, `DOUG.GAM`, `DOUG.HRD`, `DOUG.ANN`: plain mbox (`From ` separator) without a suffix | a few thousand |

A-news format (utzoo `news001f1/a13`), one article after another, no header names:
```
Aalice.154                                   <- "A" + article id
net.games                                    <- newsgroups (comma separated)
utzoo!decvax!ucbvax!mhtsa!research!alice!ark <- path
Mon Nov 23 23:44:52 1981                     <- date (ctime)
Re: Quitting                                 <- title
<body lines up to the next line that starts a new A-news header block>
```
Early B-news articles carry `Article-I.D.: decvax.123` instead of `Message-ID:`.

What exists: `handleMessage(c, rawHead, bodyReader, opts, sink)` in `import.go` takes one
header block plus a body reader (all skip rules, mboxrd, size cap, sink); `importInput` opens an
mbox-like source (`openSource`: plain/gz/zip/7z, UAT); the tracking DB (plan
mbox-import-tracking) keeps one row per input; `start7z` runs 7z with a private `-tmpdir`
subdirectory. `7z` reads all four image formats (ISO 9660 incl. Rock Ridge/Joliet, `.cdr`,
squashfs, FAT `.img`); squashfuse is not installed, fuseiso only reads ISO.

## Design

### Inputs
- New input kind `disc` (inputKind): `.iso`, `.cdr`, `.sqsh`, `.img` (case-insensitive).
  `importInput` for a disc: `7z x -y -o<private dir under -tmpdir> -- IMAGE` (the `-7z` program),
  then the spool walk over that directory, then the directory is removed (also on stop/error).
  `-tmpdir` needs the unpacked size (utzoo: 5.2 GB in 2.08M files). A failed or killed 7z is an
  input error; stop kills it like the existing 7z path.
- New flag `-spool`: a positional **directory** (or a `.zip`) argument is walked as a news spool
  instead of being scanned for `*.mbox*` files. This also covers an image the user mounted by
  hand (`fuseiso IMG /mnt/x`, then `mbox-importer -spool /mnt/x`). A zip spool is read through
  `archive/zip` as an `fs.FS`, no unpacking. Both use one walker over `fs.FS` (`os.DirFS` /
  `*zip.Reader`).
- `-from-iafetch`: `iso`, `cdr`, `sqsh`, `img` become supported (kind `disc`); a `.zip` whose
  name has no `.mbox`/`.bag` and whose members contain a `SPOOL/` or `news/` tree is not
  detected automatically: `NN_1992_25.zip` is imported by hand with `-spool`. Disc images are
  planned **before** the mbox files (the user's order), still in iafetch order among themselves.
  The `.7z` with `.bin`/`.cue` stays `unsupported-suffix`.

### Spool walk (spool.go)
- Walk in lexical order of directories, numeric order for all-digit file names (article
  numbers), so a group is read oldest first. Symlinks are not followed.
- Per regular file, classify by content (first 4 KiB), not by name:
  - `mbox`: first line is an mbox separator (`From ` + address + date, the go-lib check):
    read with the normal mbox reader (the DOUG.* floppies).
  - `anews`: first line matches `^A[A-Za-z0-9_.-]+\.[0-9]+$` and line 4 parses as a ctime
    date: A-news batch (anews.go).
  - `article`: the first line is a header field (`validFieldName` + `:`) and the header block
    (up to the first empty line, max `maxHeaderLines`) has at least one of `Newsgroups`,
    `Message-ID`, `Article-I.D.`, `Path`, `From`, `Subject`: one article -> `handleMessage`.
  - `xref-pointer`: name ends in `.XRF`/`.xrf` (NetNews/CD): counted, not read.
  - otherwise `not-article` (binaries, `.Z`, tar, `.overview`, `YMTRANS.TBL`, README): counted.
  CR before LF is dropped (DOS-side CD copies).
- Counters: `counters` gets `Files`, `NotArticle`, `XrefPointers`, `ANewsBatches` (logged in
  the per-input line; the tracking DB keeps its existing columns, the new numbers go to its log
  table through the normal log lines; no schema change).
- Progress line every `progressEvery` articles as today, plus files walked.

### Message-ID policy (decided, see Decisions)
- A-news: `Message-ID: <anews.A<site>.<n>>` (Google Groups' form, e.g. `<anews.Aalice.154>`),
  so the same article from another archive is a duplicate. Header built as
  `Path`, `From: <site>!<user>` (last two path elements), `Newsgroups`, `Subject` (title),
  `Date` (ctime converted to RFC 5322, zone `+0000`), `Message-ID`.
- B-news with `Article-I.D.: site.n` and no `Message-ID`: `Message-ID: <bnews.site.n>`
  (Google's form); the `Article-I.D.` line stays.
- Stripped comp.sources postings (no Message-ID at all): skipped as `no-msgid` (default).

### Output per input
The per-input log line gains `files=… not-article=… xref-pointers=… anews-batches=…`; `-dry-run`
lists target groups as today. A dry run of every disc is the "analysis" step before importing.

## Waves

### Wave 0 (inline, on plan-mbox-import-discs)
- `counters` fields `Files`, `NotArticle`, `XrefPointers`, `ANewsBatches` (+ `add`, log line).
- Stub signatures with `panic("wave 1")`-free empty bodies returning errors:
  `readSpool(fsys fs.FS, label string, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error)` (spool.go),
  `parseANews(r io.Reader, emit func(head []string, body io.Reader)) error` and
  `bnewsMessageID(head []string) (string, bool)` (anews.go).
- Owned: `import.go` (counters only), `spool.go` (stub), `anews.go` (stub).

### Wave 1 (parallel)
- slice `spool`: the walker and classifier, `-spool` for directories and zip files, mbox files
  inside a spool through the existing reader. Owned: `spool.go`, `spool_test.go` (new).
  Tests: a temp tree with numeric articles, an `.XRF`, a binary, a stripped posting, an mbox
  file, CRLF lines, a zip of the same tree; counts and stored message-ids; stop between files.
- slice `anews`: A-news batch parser, ctime -> RFC 5322 date, `<anews.…>`/`<bnews.…>` IDs, the
  `Article-I.D.` fallback in `messageIDFromHeaders` (only when there is no Message-ID line).
  Owned: `anews.go`, `anews_test.go` (new), `article.go` (the fallback only), `article_test.go`.
  Tests: the two utzoo samples above verbatim (multi-article batch), a broken batch line,
  B-news `Article-I.D.`, an article with both IDs (Message-ID wins).
- slice `disc`: kind `disc`, 7z unpack into a private `-tmpdir` dir and cleanup, `-spool` flag and
  dispatch in `importInput`/`expandInputs`, `-from-iafetch` support (`iaReadableSuffixes`,
  `iaUnsupportedSuffixes`, planning order discs first), README. Owned: `inputs.go`, `inputs_test.go`,
  `import.go` (dispatch, not counters), `iasource.go`, `iasource_test.go`, `main.go`, `README.md`.
  Tests: an ISO built with `genisoimage -R` and a squashfs with `mksquashfs` in `t.TempDir()`
  (skip when the tool is missing), cleanup of the unpack dir after success, error and stop.

### Wave 2
- Review fixes; verifier E2E.

## Checks
`gofmt -l cmd/mbox-importer`, `go vet ./...`, `go build ./...`, the full CLAUDE.md test list.

## End-to-end
Local, scratch data only (`-data ./data-test-discs`, `-tmpdir ./data-test-discs/tmp`):
1. Build a tree with numeric articles, an XRF pointer, an A-news batch, a binary and a stripped
   posting; pack it as ISO (`genisoimage -R`), squashfs (`mksquashfs`) and zip.
2. `-dry-run IMAGE.iso`, `IMAGE.sqsh`, `-spool tree.zip`, `-spool dir/`: same counts each time.
3. Import the ISO; rerun: `already-imported` from the tracking DB; `-reimport`: all `present`.
4. SIGINT during a 7z unpack and during the walk: exit 0 or 130 as documented, unpack dir removed.
5. `-from-iafetch` with a fake iafetch DB listing one ISO and one mbox: the ISO is imported first.
On arc1-ca, after deploy, read-only: `-dry-run -trackdb off` on `NN_1992_16.iso`, one InfoMagic ISO,
`utzoo.sqsh` and one DOUG floppy, with `-tmpdir /tank0/go-pugleaf/tmp`; report the counts.

## Decisions (user, 2026-11-06)
1. A-news / early B-news get Google's `<anews.A<site>.<n>>` / `<bnews.<site>.<n>>` Message-IDs.
2. Stripped comp.sources postings without any Message-ID are skipped (`no-msgid`).
3. Branch from `plan-mbox-import-tracking` (c84c1ed), which is not merged into testing-001 yet.

## Progress

### Wave 0 (41407d2)
Counters Files/NotArticle/XrefPointers/ANewsBatches + spoolDetail, readSpool/parseANews/bnewsMessageID stubs.

### Wave 1
- anews: worktree-agent-a9d36174b3c3a1813 (11ecf24, 33a632a, review fixes f7460fd), merged 02ce4ae.
  Beyond the plan: B-news `Posted:`/`Date-Received:`/`NNTP-Posting-Date:`/`Injection-Date:` give a Date when
  there is none (strict layouts only, >= 1980); pre-1980 A-news dates → no-date; 4 MiB cap per A-news article.
- spool: worktree-agent-a40f5ba065af0f505 (aab4d32, review fixes a207fdf), merged 763b1ba.
  Decision (orchestrator): unreadable files / mid-file read errors count `FileErrors`, logged by name, do not
  block done or change the exit code; tracking reason "N unreadable file(s)". One-article files are read
  whole before handleMessage, so no truncated article is stored.
- disc: worktree-agent-a99daf26b16568175 (d5d6aae, f3bf33f, review fixes 1d3f64d), merged 60fd0b7.
  7z exit 1 → warning; exit 2 with only "Dangerous link path was ignored" → continue; a spool without any
  article is an input error; -spool directory identity = path + top-level mtime (logged, README).
- inline: 59bab45 (FileErrors status test; .gitignore `/post-queue`, `/mbox-importer` anchored, they hid
  cmd/post-queue and cmd/mbox-importer); integration commit (spoolZip test helper clash, stub-era tests made
  strict for the merged walker).
Deferred: a present-but-unreadable Date with a good Posted is still rejected by the processor
(internal/nntp.ParseHeaders drops the fallback fields) — outside this plan.

### Verification
- Local E2E (pugleaf-verifier, 2d1bfcb): all steps PASS — ISO/sqsh/zip/dir equal counts, <anews.*>/<bnews.*>
  stored, Posted → 1981 Date, rerun already-imported, -reimport present, SIGINT in unpack and walk → 130 with
  no unpack dir left, README-only spool → exit 1, -from-iafetch imports the ISO before an earlier-ord mbox,
  .mbox.csv.gz → unsupported csv. README gaps fixed in e250403/d6d63ad (file-errors, 7z refuses every `..` link).
- arc1-ca read-only dry runs (`-dry-run -trackdb off`, build 2d1bfcb), all exit 0, file-errors=0:
  | image | read | stored | main skips | secs |
  |---|---|---|---|---|
  | NN_1992_16.iso | 171,564 | 171,247 | no-body 315 | 205 |
  | NN_1992_25.zip (-spool) | 164,928 | 164,566 | no-body 358 | 16 |
  | NetNews Offline Vol 1 | 226,456 | 225,958 | no-body 459 | 54 |
  | InfoMagic 1993-07 | 12,323 | 8,314 | no-msgid 3,950 (stripped postings) | 10 |
  | CDROM_March92.cdr | 7,523 | 5,635 | no-msgid 1,855 | 9 |
  | utzoo.sqsh | 2,084,943 | 2,038,585 | no-subject 39,918, no-body 6,188 | 3171 |
  | DOUG disk1 .img | 243 | 182 | parser-skipped 139, no-msgid 61 | 0 |
- utzoo no-subject: early B-news `Title:` instead of `Subject:` (38,592 in a 249k-file sample) → fixed in
  6af2edf (Title gives the Subject, line kept); expect ~+39k stored on utzoo.
- DOUG floppies: the text was hard-wrapped when written (separators and Newsgroups lines split), so the
  parser skips are damage in the data; left as is.

## Outcome (2026-11-07)
- Integration branch `plan-mbox-import-discs` (from plan-mbox-import-tracking c84c1ed, pushed), merged into
  testing-001 as 7cbd929.
- Worktree branches (left in place) under `/tank0/claude/trees/go-pugleaf/`: `worktree-agent-a40f5ba065af0f505`
  (spool), `worktree-agent-a9d36174b3c3a1813` (anews), `worktree-agent-a99daf26b16568175` (disc).
- After verification: 6af2edf (early B-news `Title:` gives the Subject; ~39k utzoo articles), 939c874
  (`-group-from-name` is ignored by spool inputs — the README's arc1-ca recipe would have failed every disc —
  and the file-name rule cuts `.YYYYMMDD` / `.(N)` before `.mbox`; the recipe drops `-group-from-name`).
- Verification: full check list green; local E2E all PASS; arc1-ca read-only dry runs of 7 real images all
  exit 0 with file-errors=0 (table above).
- Leftovers: a present-but-unreadable Date with a good Posted is still rejected by the processor
  (internal/nntp.ParseHeaders drops the fallback fields); the DOUG floppies lose ~200 messages to hard-wrapped
  lines in the data; `[PC] Netnews CD April 06-20 1992.7z` (.bin/.cue copy of an ISO on the mirror) stays
  unsupported; `NN_1992_25.zip` is not picked up by -from-iafetch and needs `-spool` by hand.
