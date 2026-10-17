# Plan: mbox-importer-uat (import Usenet Archive Toolkit archives)

Slug for run-plan: `mbox-importer-uat`. Follow-up of `.claude/plans/done/mbox-importer.md` (merged 0db3dcf).

## Context

mbox2nntp learned to feed UAT archives (Usenet Archive Toolkit, https://github.com/wolfpld/usenetarchive):
one newsgroup per `GROUP.usenet` file, published as `.usenet.xz` (usenet.nereid.pl, 333 Polish groups, 41 GB)
and `.usenet.7z` (archive.org `usenet-uat-pc-games`, 39 groups). The user wants `cmd/mbox-importer` to import
them too (decided 2026-10-17 after mbox-importer was verified: separate follow-up plan, not a wave of the first).

User decisions (2026-10-17):
- Reader code comes from **`github.com/go-while/mbox2nntp/uat`** (import the module; the user moved the test
  fixture writer to the importable path `github.com/go-while/mbox2nntp/uat/uattest`, commit **823cac0**, pushed).
- **Target group = the first group of the article's `Newsgroups:` header, `-group` overrides** (same rule as
  for mbox inputs; the archive's own group name is only logged).
- External programs like mbox2nntp: `zstd` (required for every UAT input), `xz` for `.usenet.xz` (7-Zip with
  `-txz` as fallback), 7-Zip for `.usenet.7z`.

Facts (verified in /tank0/efs/GO/src/github.com/go-while/mbox2nntp at 823cac0; paths relative to that repo):
- Package `uat` (stdlib only: bufio, encoding/binary, errors, fmt, io, mime, net/mail, os, os/exec, regexp,
  strings). `uat.Open(path) (*Package, error)` needs a seekable file (reads the table of contents, sizes and the
  zstd dictionary; versions 0-3); `Package{Name, DescShort string; Version int}`, `Count() int`, `Close()`,
  `Each(zstd, tmpdir string, fn func(i int, msg []byte) error) error`: writes the dictionary to a temp file in
  tmpdir ("" = system temp), runs `zstd -d -q -c -D dict` once over the whole message store, splits the output
  by the stored sizes and calls fn per message (msg valid only during the call; an error from fn stops the run
  and is returned; a zstd failure or a size mismatch is returned as an error). `uat.Fix(msg []byte) (head, body
  []string, info Info)`: drops Content-Type/Content-Transfer-Encoding, adds `MIME-Version: 1.0` when missing,
  appends `Content-Type: text/plain; charset=UTF-8` and `Content-Transfer-Encoding: 8bit|7bit`, RFC 2047-encodes
  non-ASCII in unstructured and address fields, never touches Message-ID/References/Newsgroups/Path/Date;
  `Info{Encoded int; Raw []string; AddedMIMEVersion bool}`. head/body have no line endings, continuation lines
  start with space/tab: exactly what `rewriteHead`/`buildArticle` take.
- `uat/uattest`: `Zstd(t testing.TB) string` (skips the test when zstd is missing), `Write(t, zstd, dir, name,
  desc string, msgs []string) string` (writes a version-3 `<dir>/<name>.usenet` and returns its path),
  `WriteRaw(...)`. The toolkit can store an empty message.
- mbox2nntp's own flow (`uatinput.go`, `cmd/uatexport/main.go`, `internal/unpack/unpack.go`, not importable):
  find zstd first; `.usenet.xz` → `xz -dc -- archive` into a temp file (fallback `7z e -so -bd -txz -- archive`),
  `.usenet.7z` → `7z l -slt -spd -- archive` to check the entry `NAME.usenet` exists (7z succeeds and writes
  nothing for an unknown name), then `7z e -so -bd -spd -- archive NAME.usenet` into a temp file; `uat.Open`
  on the file; per message `uat.Fix` → ParseHeaderKeys (msgid) → yEnc check → trailing blank lines removed →
  empty body skipped; the unpacked copy is removed after the archive is marked done. README: "Feed UAT archives
  last": their articles keep the original Message-IDs but are rewritten copies.
- go-pugleaf side (`cmd/mbox-importer`, merged tree 0db3dcf): `importInputs` → `importInput(path, opts, sink,
  stop) (c *counters, inputErr, stopped bool)` → `openSource` + `readMbox` → `handleMessage(c, rawHead, bodyReader,
  opts, sink)` which does readBody, messageIDFromHeaders, binary/no-body skips, rewriteHead, firstNewsgroup/-group,
  `sink.Handle(msgid, build, group)` and the stored/present/rejected/errors accounting; `sink.beginInput()` /
  `endInput(timeout)`; mark-done rule and the per-input summary line live in `importInput`; `expandInputs` uses
  `isInputName` (".mbox"/".bag" in the name, not ".done"); `inputKind` ignores a trailing ".done". Flags in
  main.go (`-7z`, `-group`, `-dry-run`, `-keep-binary`, `-mark-done`, `-strict-msgid`, `-commit-timeout`, ...).
  The processor's bulk checks apply unchanged (Newsgroups header with 1..9 valid groups, Subject/From/Date,
  `CheckHeaderBlock` fetch mode; RFC 2047 encoded-words are plain ASCII and pass).
- `zstd`, `xz`, `7z` are installed on this host (/usr/bin). No `.usenet` sample exists locally; usenet.nereid.pl
  lists its groups with sizes.

## Design

**New file `cmd/mbox-importer/uat.go`** (+ `uat_test.go`); small edits in `import.go`, `main.go`, `inputs.go`.

- `uatSuffix(path string) string`: ".usenet", ".usenet.xz" or ".usenet.7z" (a trailing ".done" is ignored like
  `inputKind` does), "" otherwise. `isInputName` additionally accepts names containing ".usenet"; `importInputs`
  calls `importUAT` instead of `importInput` when `uatSuffix(path) != ""`.
- New flags: `-zstd zstd` (program for UAT inputs), `-xz xz` (program for `.usenet.xz`; when not found and 7-Zip
  is, `7z e -so -bd -txz` is used), `-tmpdir ""` (where `.usenet.xz`/`.usenet.7z` are unpacked and the zstd
  dictionary is written; "" = the system temp dir). `runOptions` gains `zstd, xz, tmpDir string`.
- `importUAT(path, opts, sink, stop) (c *counters, inputErr, stopped bool)`, same contract and summary line as
  `importInput`:
  1. `exec.LookPath(opts.zstd)` first → input error when missing (before anything is unpacked).
  2. `.usenet.xz`/`.usenet.7z`: `dir := os.MkdirTemp(opts.tmpDir, "mbox-importer-")`, unpack to
     `<dir>/<NAME>.usenet` with `unpackXz(archive, dst, xz, sevenZip)` / `unpack7z(archive, dst, sevenZip)` (port of
     mbox2nntp's `UnXzTo`/`Un7zTo`/`RunTo`: stdout streamed to the file, stderr kept in a `tailBuffer`, a failing
     program is an error even if it wrote something, an empty result is an error); `defer os.RemoveAll(dir)` always.
  3. `p, err := uat.Open(file)`; log `[MBOX-IMPORT] uat file=%q group=%q version=%d messages=%d`.
  4. `sink.beginInput()`; `p.Each(opts.zstd, dir-or-tmpDir, fn)`; fn: if stop is closed → return `errStopped`;
     `c.Read++`; `head, body, info := uat.Fix(msg)`; accumulate `info` (encoded fields, added MIME-Version, raw
     field names); `handleLines(c, head, body, opts, sink)`.
  5. Each result: `errStopped` → stopped; any other error → `c.StoppedEarly = true`, logged, input error (exit 1).
  6. `p.Close()`, `sink.endInput(opts.commitTimeout)`, mark-done rule exactly as `importInput` (rename the
     archive path given, e.g. `X.usenet.xz` → `X.usenet.xz.done`), the summary line, plus one UAT line
     `[MBOX-IMPORT] uat file=%q re-encoded-fields=%d added-mime-version=%d raw-fields=%s` (raw = `name=count`
     list or "-"). Near-misses are reported as 0 (no mbox parser involved).
- `handleLines(c, rawHead, body []string, opts, sink)` is extracted from `handleMessage` (which keeps `readBody`
  and then calls it): the yEnc check for UAT bodies is `hasYEnc(body)` (any line starting with `=ybegin`) unless
  `-keep-binary`; trailing empty body lines are removed (`readBody` already does that for mbox; a shared helper
  `trimTrailingEmpty` or reuse of the existing one); empty body → `no-body`. Everything else (msgid rule, rewrite,
  first group / -group, sink accounting) is shared and unchanged.
- Dry run works for UAT inputs too (zstd is still run; nothing is renamed).
- README (cmd + top-level): inputs table rows for `.usenet`, `.usenet.xz`, `.usenet.7z`; the three flags; the
  programs needed; what UAT articles are (rewritten by the toolkit, Fix makes them valid again, Message-IDs
  original) and the consequence in go-pugleaf terms: import UAT archives **after** the mbox collections, because
  an article already present in its target group counts as `present` and the UAT copy is not stored, while a
  UAT copy imported first is the one that stays; the UAT summary line.

### Shared signatures (wave 0)
```go
// cmd/mbox-importer/uat.go
func uatSuffix(path string) string
func importUAT(path string, opts *runOptions, sink articleSink, stop <-chan struct{}) (c *counters, inputErr, stopped bool)
func unpackXz(archive, dst, xz, sevenZip string) error
func unpack7z(archive, dst, sevenZip string) error
func hasYEnc(body []string) bool
// cmd/mbox-importer/import.go
func handleLines(c *counters, rawHead, body []string, opts *runOptions, sink articleSink)
// runOptions: + zstd, xz, tmpDir string
```

## Waves

### Wave 0 (inline, orchestrator, on `plan-mbox-importer-uat` from `testing-001`)
1. Move this plan to wip; preflight.
2. `go get github.com/go-while/mbox2nntp@823cac0 && go mod tidy` (its only requirement is go-lib-mbox2nntp at the
   version already in go.mod; check that go.sum gains only mbox2nntp lines and that `go build ./...` stays green).
3. `cmd/mbox-importer/uat.go` with the signatures above as compiling stubs (importing `uat` and `os/exec` so the
   dependency stays), `runOptions` fields added, `handleLines` extracted from `handleMessage` (behaviour-neutral;
   existing tests must stay green). Checks: gofmt, vet, build, `go test -race ./cmd/mbox-importer/...`. Commit.

### Wave 1 (2 parallel slices)
- slice `uat-import`: owned `cmd/mbox-importer/uat.go`, new `cmd/mbox-importer/uat_test.go`, `cmd/mbox-importer/import.go`
  (importInputs branch, handleLines body if needed), `cmd/mbox-importer/main.go` (three flags, runOptions wiring,
  usage text), `cmd/mbox-importer/inputs.go` (`isInputName` only). Not: article.go, READMEs, go.mod.
  Goal: the Design. Tests (`uattest.Zstd(t)` skips without zstd; fixtures under t.TempDir(); no sleeps or
  wall-clock asserts; DB tests follow main_test.go's helpers: `miRun`-style run with the hand-built processor,
  one DB at a time; keep the package's test runtime growth small): `uatSuffix` incl. `.done`; `isInputName` with
  `.usenet.xz`; `hasYEnc`; `unpackXz`/`unpack7z` (skip without the program; wrong entry name → error; empty
  output → error; stderr in the error); dry run on a 4-message package (one Polish subject, one crosspost whose
  first group differs, one empty message, one without Subject): read=4, stored=2 (counting sink), skipped
  no-body=1 no-subject=1, no rename, no data dir; real run: both first groups exist, rows present, the Polish
  subject is stored as an encoded-word and `buildArticle` gives that Subject, `MIME-Version`/`Content-Type` lines
  present in the stored headers (`GetGroupDB` + the article's headers), archive renamed `.usenet.done`; rerun →
  present; `.usenet.xz` and `.usenet.7z` inputs (skip without xz/7z): same result, the archive (not the temp
  copy) renamed, the temp dir removed (count entries of a dedicated `-tmpdir` before/after); missing zstd
  (`-zstd /nonexistent`) → input error exit 1 before unpacking; `-group forced.uat`; pre-closed stop → 130, no
  rename, temp dir removed; a fixture-keeping test `TestMboxUatWriteFixture` that only runs when
  `MBOX_IMPORTER_UAT_FIXTURE=<path>` is set and writes a 3-message `.usenet` there (used by the verifier).
  Checks: `gofmt -l cmd/mbox-importer`; `go vet ./...`; `go build ./...`; `go test -race -count=1 ./cmd/mbox-importer/...`;
  `./build_mbox-importer.sh`.
- slice `uat-docs`: owned `cmd/mbox-importer/README.md`, `README.md` (top-level: one usage line for a `.usenet.xz`
  input, the "needs zstd/xz/7z" note in the tool bullet). Not: any `*.go`, go.mod, workflows (no new package).
  Goal: the README items in the Design (inputs rows, flags `-zstd`/`-xz`/`-tmpdir`, programs, what UAT articles
  are, "import UAT archives after the mbox collections" with the present/stored reasoning, the UAT summary line,
  exit codes unchanged; keep the flag table in the order main.go prints them).
  Checks: `grep -c usenet` on both files; no other sections disturbed.

### Wave 2 (verifier)
Full CLAUDE.md checks (the race list, known bugs), `./build_mbox-importer.sh`, End-to-end below.

## Checks (merged tree)
```bash
gofmt -l cmd/mbox-importer; go vet ./... && go build ./...
go test -race -count=1 ./cmd/mbox-importer/... ./internal/processor/...
# then the full CLAUDE.md race list; PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/ → only T-nntp-article-1
./build_mbox-importer.sh && ./build/mbox-importer -help | grep -c -- '-zstd\|-xz\|-tmpdir'   # 3
```

## End-to-end (verifier; scratch root `-data ./data-test-mbox-uat`; never `./data`, `data_ram`, `data_local`)
```bash
cd /tank0/efs/GO/src/github.com/go-while/go-pugleaf && ./build_mbox-importer.sh && mkdir -p ./data-test-mbox-uat/in ./data-test-mbox-uat/tmp
# fixture (offline): MBOX_IMPORTER_UAT_FIXTURE=$PWD/data-test-mbox-uat/in/t.uat.fixture.usenet go test -count=1 -run TestMboxUatWriteFixture ./cmd/mbox-importer/
xz -k ./data-test-mbox-uat/in/t.uat.fixture.usenet -c > ./data-test-mbox-uat/in/copy1.usenet.xz
(cd ./data-test-mbox-uat/in && 7z a -bd copy2.usenet.7z t.uat.fixture.usenet >/dev/null)   # entry name must be NAME.usenet: rename the entry or build the 7z from a copy named copy2.usenet
# optional real data: download the smallest .usenet.xz from https://usenet.nereid.pl/ into ./data-test-mbox-uat/real/ (own dir, untrusted input, only passed as an argument)
HOST=i2pn2.pugleaf.net
./build/mbox-importer -dry-run ./data-test-mbox-uat/in/t.uat.fixture.usenet; echo exit=$?                 # 0, uat line with group/version/messages, nothing renamed
./build/mbox-importer -data ./data-test-mbox-uat -nntphostname $HOST -tmpdir ./data-test-mbox-uat/tmp ./data-test-mbox-uat/in/t.uat.fixture.usenet; echo exit=$?   # 0, stored=N, .usenet.done
./build/mbox-importer -data ./data-test-mbox-uat -tmpdir ./data-test-mbox-uat/tmp ./data-test-mbox-uat/in/copy1.usenet.xz ./data-test-mbox-uat/in/copy2.usenet.7z; echo exit=$?   # 0, present=N each, both renamed, tmp dir empty afterwards
./build/mbox-importer -data ./data-test-mbox-uat -zstd /nonexistent/zstd ./data-test-mbox-uat/in/copy1.usenet.xz.done; echo exit=$?   # 1, input error, nothing unpacked
# real archive (if downloaded): dry run first (read the uat line and the raw-fields list), then a real run; time it; sqlite3 counts = stored; history-rebuild -validate-only 0 missing
# read-back with ./build/pugleaf-nntp-server -data ./data-test-mbox-uat -nntpbind 127.0.0.1 -nntptcpport 11195: HEAD of a Polish-subject article shows the encoded-word Subject and Content-Type: text/plain; charset=UTF-8; stop the server (exit 0)
```
Leave `./data-test-mbox-uat` for the user.

## Decisions taken
- UAT articles follow the first-group rule and all mbox skip/reject rules; the archive's group name is logged only.
- An `Each` error after some messages (zstd failure, size mismatch) is an input error (exit 1) and blocks mark-done;
  the articles already handed over stay stored.
- The unpacked copy lives in a private temp dir under `-tmpdir` and is always removed; the archive given on the
  command line is what gets renamed `.done`.

## Risks
- Large `.usenet.xz` (GBs) need that much temp space: document `-tmpdir`.
- `zstd` output for an archive with millions of messages is streamed; memory stays one message (plus Fix copies).
- go.mod gains the mbox2nntp module (a main package module; only its `uat` and `uat/uattest` packages are imported).
