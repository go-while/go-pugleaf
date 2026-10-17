# Plan: mbox-importer (import Usenet mbox archives in bulk mode)

Slug for run-plan: `mbox-importer`.

## Context

`/tank0/efs/GO/src/github.com/go-while/mbox2nntp/` feeds the archive.org Usenet collections (mbox files,
one per group, often crossposted articles) to INN with IHAVE, one article at a time. go-pugleaf needs the
same input imported **directly into its SQLite databases** through the processor in **bulk mode**: the
fetcher's path (`bulkmode=true` in `internal/processor/threading.go`), which files every article into
exactly one group, never processes crossposts, does no history check and answers `CasePass` for an
article already in that group. `cmd/rslight-importer` is the existing model for an offline bulk importer
(DB/processor lifecycle, group creation, shutdown order).

User decisions (2026-10-17):
- **Target group = the first group of the article's `Newsgroups:` header** (one mbox can feed many groups;
  missing groups are created on the fly). An optional `-group NAME` forces every article of a run into NAME.
- **Inputs**: `NAME.mbox`/`NAME.bag`, `NAME.mbox.gz` (stdlib, streamed), `NAME.mbox.zip` (stdlib, streamed),
  `NAME.mbox.7z` via the **installed `7z` binary** (mbox2nntp drops its sevenzip Go dependency next commit and
  uses the installed 7z on all OS). Several inputs per run; a directory argument = its `*.mbox*`/`*.bag*` files.
- **`-mark-done` default on**: an input is renamed `INPUT.done` when every article was stored or already
  present and the parser reached the end; never in a dry run.
- **`processArticle` is exported** as `ProcessArticle` (user: "we can export the processArticle"); no wrapper.

Parser library: `github.com/go-while/go-lib-mbox2nntp` (import alias `mbox`), pin
`v0.0.0-20261017083307-028f55355c84` (commit 028f553, in the module cache and pushed; it uses Go's `regexp`
and has no third-party dependency: `regexp2` was dropped from the library and from mbox2nntp on 2026-10-17).
API: `mbox.CreateMboxStream(io.Reader) (*MboxStream, error)` (`io.EOF` = empty or not an mbox),
`(*MboxStream).ReadMessage() (*Message, error)` (`io.EOF` = end; a parse error skips one message and the next
call continues; the same error text twice in a row = stuck), `Message.Headers() []string` (ordered lines, CR
stripped), `Message.BodyReader() io.Reader` (must be drained before the next ReadMessage),
`(*MboxStream).NearMisses() (int, []NearMiss{Line int; Text string})`.

Verified facts (paths relative to the repo):
- `internal/processor/threading.go:42` `processArticle(article *models.Article, legacyNewsgroup string, bulkmode bool) (int, error)`;
  callers `processor.go:406,477`, `proc_DLArt.go:396`, `rslight.go:390`, `PostQueue.go:156`, `wiring_test.go`,
  `fl_nntpdb_commit_test.go:88`. Bulk branch (`threading.go:102-143,216-230,260-270,361-372`): `CheckHeaderBlock(HeaderBlockFetch)`
  on `HeadersJSON` (rejects NUL/CR/LF and duplicate Date/From/Message-ID/Newsgroups/Path/Subject), the
  `Message-ID:` header must equal `article.MessageID`, `Newsgroups:` must give 1..9 groups passing
  `common.IsValidGroupName`, Subject and From non-empty, `ParseNNTPDate` must parse and not be >24 h ahead;
  then `newsgroups = [legacyNewsgroup]` only, Path gets `LocalNNTPHostname!` prefixed (`host!unknown!not-for-mail`
  when absent), `syncStoredPath(stripXrefLines(lines))`; group DB open failure → `CaseRetry`/`ErrIntakeTemporary`;
  rejection → `CaseError` wrapping `common.ErrIntakeRejected`; already in the group → `CasePass`. The message-id
  itself is not validated anywhere on this path (the fetcher stores what the provider sends).
- `nntp.ParseLegacyArticleLines(msgid string, lines []string, bulkmode bool) (*models.Article, error)`
  (`internal/nntp/nntp-client-commands.go:871`): one `[]string` (headers, "", body), no dot handling, fills
  `Headers` map, `HeadersJSON`, `BodyText`, `Bytes`, `Lines`, `Subject`, `FromHeader`, `Path`, `References`,
  `RefSlice`, `DateString`. The NNTP server dot-stuffs on output (`nntp-client-commands.go:1344`), so bodies are
  stored unstuffed.
- Lifecycle to copy from `cmd/rslight-importer/main.go`: `history.ENABLE_HISTORY` before opening (read once),
  `database.NO_CACHE_BOOT = true`, `database.OpenDatabase(cfg)` (one per process, refuses euid<1000),
  `processor.SetHostname(host, db)` (`processor.go:590`; stores/reads config key `local_nntp_hostname`),
  `db.GetHistoryUseShortHashLen`/`SetHistoryUseShortHashLen` lock, `processor.NewProcessorE(db, nil, hashLen)`
  (does a DNS lookup of the hostname, `processor.go:669`; a nil pool is fine for bulk imports: check `NewProcessorE`
  tolerates it, else `nntp.NewPool(&nntp.BackendConfig{})` like rslight), `database.GlobalDateParser = processor.ParseNNTPDate`,
  group creation `rslight.go:703` `insertNewsgroupIfNotExists` (`MainDBGetNewsgroup` then `InsertNewsgroup`
  with `Active:true, LowWater:1, Status:"y"`), `db.Batch.CommitMark(group)` before the hand-off and
  `db.Batch.WaitCommitted(mark, stop, timeout)` (`db_batch_commit.go:119,137`), shutdown `proc.Close()`,
  `close(db.StopChan)`, `db.WG.Wait()`, `db.Shutdown()`. WaitCommitted must run before `proc.Close()` (capture
  goroutines not yet in the divider are invisible to `CheckNoMoreWorkInMaps`). The batch writer adds the
  `groupID:articleNum` history token only when the `newsgroups` row exists. Backpressure: `captureLimit` 2048
  blocks ProcessArticle, batches of 1000 every `BatchInterval` 3 s, divider brake at 80 % of 1280.
- Tool conventions: `cmd/post-queue/main.go` (`main` → `os.Exit(run(os.Args[1:]))`, `runWith(args, stopChan)`,
  `flag.NewFlagSet(..., flag.ContinueOnError)`, exit 0/1/2/130, `notifyShutdown`), tests `cmd/post-queue/main_test.go`
  (`TestMain` euid<1000 skip, per-package `testWaitFromEnv` for `PUGLEAF_TEST_DEADLINE`, open-seed-close then run,
  one DB at a time), `cmd/import-flat-files/fl_ops_import_test.go:14` (dry run opens no DB),
  `cmd/nntp-server/server_e2e_test.go:163-182` (hand-built `Processor` with history, no DNS). No wall-clock
  assertions (project rule). No `testdata/` convention: fixtures are written in Go under `t.TempDir()`.
- Sample: `/tank0/efs/GO/src/github.com/go-while/mbox2nntp/data/mbox/de.comm.internet.commerce.mbox` (5.7 MB,
  ~2300 articles, separator `From nobody Thu Dec  8 13:43:59 2016`, Xref lines; first-group distribution
  ~1938 de.comm.internet.commerce, 207 de.alt.rec.digitalfotografie, 29 de.comm.internet.misc, ~40 groups).
  `7z` and `7za` are in PATH. `7z x -so -bd -- ARCHIVE '*.mbox' '*.bag'` streams only the mbox member
  (plain `7z x -so ARCHIVE` concatenates every member); a missing member gives an empty stream and exit 0.

## Design

**Layout: the cmd owns everything.** `cmd/mbox-importer/` = `main.go` (flags, lifecycle, exit codes),
`import.go` (read loop, sinks, counters, mark-done), `inputs.go` (input discovery, decompression, eofReader),
`article.go` (header rewrite, body reader, article builder). Shared seams in `internal/processor`:
`ProcessArticle` (renamed) and `EnsureNewsgroup` (extracted). Parsing code is pure and unit-tested without a DB.

**Run (`runWith(args, stopChan) int`)**
1. Flags (`flag.NewFlagSet("mbox-importer", flag.ContinueOnError)`): `-data ./data` (project default; always
   pass a scratch root when testing), `-nntphostname` (first run only; later runs read the DB), `-useshorthashlen 7`,
   `-history true`, `-group NAME` (override), `-7z 7z` (program for `.7z` inputs), `-dry-run`, `-keep-binary`,
   `-mark-done true`, `-strict-msgid false` (see Message-ID rule), `-commit-timeout 30m` (bound on each
   per-input WaitCommitted; 0 = none), `-help`. Positional args = inputs (≥1, else usage + exit 2).
   `config.AppVersion = appVersion`, `database.NO_CACHE_BOOT = true`, `database.DBidleTimeOut = 15s`,
   `history.ENABLE_HISTORY = *useHistory`, `database.GlobalDateParser = processor.ParseNNTPDate`.
2. `expandInputs(args)`: file → itself; directory → its non-directory entries matching `*.mbox*`/`*.bag*` and not
   ending in `.done`, sorted by name, no recursion; missing path → input error (exit 1 at the end, other inputs
   still run).
3. Dry run: open no database; run the read loop with a counting sink; print per-target-group counts; never rename.
4. Real run: `OpenDatabase`, `SetHostname` when given, hash-length lock, `newProcessor(db, hashLen)` (package var
   defaulting to `processor.NewProcessorE`; tests replace it with a hand-built processor), then the inputs
   **sequentially with one reader feeding `ProcessArticle` directly** (rslight does the same; file order per group
   is preserved; a `-threads` for parallel files can come later).
5. Per article: `readBody` (drained even when the article is skipped) → `messageIDFromHeaders` → `rewriteHead`
   → skip reasons → target group (`-group` or `firstNewsgroup`) → `EnsureNewsgroup` once per group per run
   (memoised) → `CommitMark` once per (input, group) → pre-lookup `groupDB.LookupMsgIdInArticlesDB(msgid)` →
   `present` (ProcessArticle not called) → else `buildArticle` + `proc.ProcessArticle(art, group, true)`:
   `CasePass` → stored; `CaseError` → rejected (logged with the error); `CaseRetry`/other/err → errors.
6. End of input: `source.Unread()`, `NearMisses()`, `WaitCommitted` for every group the input touched (stop = nil
   so a signal still lets the committed count settle, bounded by `-commit-timeout`), then the mark-done rule:
   `!dryRun && markDone && !stoppedEarly && unread == 0 && errors == 0 && waitErr == nil` → `os.Rename(path, path+".done")`.
   Skipped and rejected articles never block mark-done (mbox2nntp's 437 semantics). An empty 0-byte input counts
   as done; a non-empty input whose stream cannot start (`CreateMboxStream` → `io.EOF`, e.g. a 7z without an mbox
   member) is an input error.
7. Signal (`stopChan` closed; checked between articles): finish the current article, no rename for that input,
   remaining inputs not started, shutdown, exit 130. Shutdown always: `proc.Close()`, `close(db.StopChan)`,
   `db.WG.Wait()`, `db.Shutdown()`. Exit 1 when any input failed to open/unpack/start, or on a DB/processor
   error; else 0 (an input not marked done is logged, not an error: mbox2nntp semantics).

**Header rewrite (`rewriteHead`, port of mbox2nntp `rewriteHead`/`cleanHeadline`/`isBadDateLine`)**: NUL,
backspace and tab → space; drop `Xref:`, `Expires:` and stray `From ` lines with their continuations; drop
no-colon non-continuation lines and empty-valued headers without continuation; repair `Key:value`/`Key :value`
→ `Key: value`; the `isBadDateLine` quirk for broken Google headers; keep `Path:` as stored (the processor
prefixes the local hostname; no `mbox2nntp-NAME!`); reduce Message-ID to one `Message-ID: <msgid>` line
(appended when the header was lost). `>From ` lines untouched. More than 256 header lines → skip.

**Message-ID rule**: `messageIDFromHeaders` applies mbox2nntp's lax repairs (`>#1/1` suffix, `>(UMass-...)`
and `> (...)` tails) and accepts `<...>` without whitespace (mbox2nntp's `-lax-msgid` default); with
`-strict-msgid` the id must pass `common.ValidMessageID`. Failing ids → `skip-bad-msgid`.

**Skip reasons** (counted per input, never block mark-done): `no-msgid`, `bad-msgid`, `no-body` (cancels),
`binary` (yEnc `=ybegin` unless `-keep-binary`), `no-date`, `no-subject`, `too-many-headers`, `no-newsgroups`,
`bad-group` (first group fails `common.IsValidGroupName`; no fallback to a later group).

**Body (`readBody`, port)**: read in 64 KiB chunks joining pieces until `\n`, `TrimRight "\r\n"`, `=ybegin`
detection, trailing empty lines removed. Article = `nntp.ParseLegacyArticleLines(msgid, append(head, "", body...), true)`.

**Inputs (`openSource`)**: `.mbox`/`.bag` `os.File`; `.gz` `compress/gzip` (multistream default); `.zip`
`archive/zip` on the file, the first member named `*.mbox`/`*.bag` (else the only member), streamed via
`File.Open()`; `.7z` `exec.Command(sevenZip, "x", "-so", "-bd", "--", archive, "*.mbox", "*.bag")` with
`StdoutPipe` (`exec.LookPath` failure → input error; `Close()` waits, non-zero exit → input error, no mark-done;
an early close kills the process). Each is wrapped in an `eofReader` (records whether the underlying reader hit
EOF) under a 1 MiB `bufio.Reader`, then `mbox.CreateMboxStream`. Nothing beyond one article is held in memory.

**Read loop (`readMbox`)**: `ReadMessage` → `io.EOF` ends; an identical consecutive error = stuck →
`stoppedEarly`; another error = parser-skipped message (counted, continue). Log prefix `[MBOX-IMPORT]`; a
progress line every 10000 messages; a per-input summary
`file=… read=… stored=… present=… skipped=… (per reason) rejected=… errors=… parser-skipped=… near-misses=… stopped-early=… unread=… marked-done=…`
plus up to five near-miss samples; a final totals line.

Threading uses `References` only; a reply imported before its parent gets thread root 0 (mbox files are usually
chronological). The README points at `fix-thread-activity`; not solved here.

### Shared signatures (fixed in wave 0)
```go
// internal/processor
func (proc *Processor) ProcessArticle(article *models.Article, legacyNewsgroup string, bulkmode bool) (int, error) // was processArticle
func EnsureNewsgroup(db *database.Database, name, description string) error // extracted from insertNewsgroupIfNotExists (rslight.go), which now calls it

// cmd/mbox-importer/inputs.go
type source struct { Path string; Stream *mbox.MboxStream /* nil: empty file */ /* unexported: eofReader, closers, *exec.Cmd */ }
func expandInputs(args []string) (files []string, errs []error)
func openSource(path, sevenZip string) (*source, error)
func (s *source) Unread() bool    // true when ReadMessage returned io.EOF before the underlying reader did
func (s *source) Close() error    // file/zip/gzip close; 7z: wait for the process, its exit error
func markDone(path string) error  // rename to path+".done"

// cmd/mbox-importer/article.go
type skipReason int               // skipNone, skipNoMessageID, skipBadMessageID, skipNoBody, skipBinary, skipNoDate, skipNoSubject, skipTooManyHeaders, skipNoNewsgroups, skipBadGroup
func (r skipReason) String() string
func readBody(r io.Reader, keepBinary bool) (body []string, isBinary bool)
func messageIDFromHeaders(head []string, strict bool) (msgid string, reason skipReason)
func rewriteHead(msgid string, head []string) (newhead []string, reason skipReason)
func firstNewsgroup(head []string) (group string, reason skipReason)
func buildArticle(msgid string, head, body []string) (*models.Article, error)

// cmd/mbox-importer/import.go
type counters struct { Read, Stored, Present, Rejected, Errors, ParserSkipped, NearMisses int; Skipped map[skipReason]int; StoppedEarly, Unread, MarkedDone bool }
type articleSink interface { Handle(msgid string, art func() (*models.Article, error), group string) (stored, present bool, err error) } // dry run: counting sink; real: processor sink (pre-lookup, EnsureNewsgroup, CommitMark)
func readMbox(src *source, opts *runOptions, sink articleSink, stop <-chan struct{}) (*counters, error)
func runWith(args []string, stopChan <-chan struct{}) int
var newProcessor = func(db *database.Database, hashLen int) (*processor.Processor, error) // test seam
```

## Waves

### Wave 0 (inline, orchestrator, on `plan-mbox-importer` from `testing-001`)
1. Rename this plan to `.claude/plans/wip/mbox-importer.md`; preflight.
2. `internal/processor`: rename `processArticle` → `ProcessArticle` (`threading.go:42`, doc comment) and every
   caller/comment (`grep -rn processArticle internal/ cmd/ docs/ BUGS.md`: `processor.go`, `proc_DLArt.go`,
   `rslight.go`, `PostQueue.go`, `wiring_test.go`, `fl_nntpdb_commit_test.go`). Extract
   `EnsureNewsgroup(db, name, description)` in `rslight.go`; `insertNewsgroupIfNotExists` calls it.
3. `cmd/mbox-importer/`: `main.go` (`var appVersion = "-unset-"`, `main`, `run`, `runWith` stub returning 1),
   `inputs.go`, `article.go`, `import.go` with the signatures above as compiling stubs (`errors.New("not implemented")`
   / zero values), importing `mbox` so the dependency stays. `go get github.com/go-while/go-lib-mbox2nntp@v0.0.0-20261017083307-028f55355c84 && go mod tidy`
   (offline from the module cache; the library has no third-party dependency, so go.sum gains only its own lines).
4. Checks: gofmt, `go vet ./...`, `go build ./...`, `go test -race ./internal/processor/...`. Commit.

### Wave 1 (2 parallel slices)
- slice `mbox-inputs-rewrite`: owned `cmd/mbox-importer/inputs.go`, `cmd/mbox-importer/article.go`, new
  `cmd/mbox-importer/inputs_test.go`, `cmd/mbox-importer/article_test.go`. Not: `main.go`, `import.go`, docs, go.mod.
  Goal: implement the input layer and the header rewrite as designed (port `readBody`, `rewriteHead`,
  `cleanHeadline`, `isBadDateLine`, `GetMessageID` lax repairs, `openMboxFile`/`eofReader`/`unreadBytes` from
  mbox2nntp; MIT-licensed own code, no attribution problem). Tests (no DB; fixtures in Go under `t.TempDir()`; an
  embedded mbox string with two articles using the `From nobody Thu Dec  8 13:43:59 2016` separator, an Xref
  line, a folded `Newsgroups:` with three groups, a `Key:value` line and a stray `From ` line): rewrite output
  byte-exact; every skip reason; `firstNewsgroup` on a folded header; lax vs strict message-id; `readBody` with a
  line longer than 64 KiB, CRLF, `=ybegin`; `buildArticle` fields (`Subject`, `Path`, `RefSlice`, `Bytes`);
  `expandInputs` on a dir with `.mbox`, `.mbox.gz`, `.bag`, `x.mbox.done`, a subdir → sorted list without
  `.done`; `openSource` for plain, gzip (two concatenated members), zip (generated with `archive/zip`), empty
  file (Stream nil), non-mbox file (error), `.7z` (skipped when `exec.LookPath("7z")` fails; otherwise `7z a`
  the fixture in TempDir), missing 7z program (error), 7z without an mbox member (error); `Unread()` true when
  a stream is abandoned after the first message and false after a full read; `markDone`.
  Checks: `gofmt -l cmd/mbox-importer`; `go vet ./...`; `go build ./...`; `go test -race ./cmd/mbox-importer/...`.
- slice `mbox-docs-ci`: owned new `cmd/mbox-importer/README.md`, new `build_mbox-importer.sh` (copy of
  `build_usermgr.sh`, output `build/mbox-importer`, executable), `README.md` (bullet in "Built with their own
  `build_<tool>.sh` script only": `**mbox-importer** - Import Usenet mbox archives (archive.org) through the
  processor, bulk mode` + a short usage section after the rslight-importer lines), `.claude/CLAUDE.md` (Checks
  block gets `./cmd/mbox-importer/...`; "24 tools" → 25), `.github/workflows/tests.yml` and
  `.github/workflows/release.yml` (test package list only, next to `./cmd/usermgr/...`; not the release build
  matrix). Not: any `*.go`, go.mod.
  Goal: `cmd/mbox-importer/README.md` modelled on `cmd/rslight-importer/README.md`: features, build, usage examples
  (`-dry-run`, plain, `.gz`, `.zip`, `.7z` with `-7z`, directory, `-group`), the flag table from the Design,
  input table, "what happens to an input" (`.done` rule, exit codes 0/1/2/130, signal), "what is stored" (rewrite
  rules, first group only, no crossposts, Path prefix by the processor, Xref dropped, threading note with
  `fix-thread-activity`), skip reasons, near-misses ("dry-run one file of a new collection first"), the DNS
  requirement of `-nntphostname`, and that `-data` must be a scratch root for tests.
  Checks: `bash -n build_mbox-importer.sh`; the two workflow files still parse (`python3 -c 'import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]' ...`);
  `grep -c mbox-importer` in the five edited files.

### Wave 2 (1 slice, after wave 1 merged)
- slice `mbox-cmd`: owned `cmd/mbox-importer/main.go`, `cmd/mbox-importer/import.go`, new
  `cmd/mbox-importer/main_test.go`. Not: `inputs.go`, `article.go` (report needed changes instead), docs, go.mod.
  Goal: flags/usage/exit codes, DB + processor lifecycle, `readMbox` loop with the two sinks, counters and
  logging, per-input `EnsureNewsgroup`/`CommitMark`/`WaitCommitted`, mark-done rule, `-group`, `-dry-run` (no DB
  opened), signals (`notifyShutdown` copy from post-queue; `stopChan` for tests), shutdown order.
  Tests (`TestMain` euid<1000 skip + `NO_CACHE_BOOT`/`DBidleTimeOut`; `testWaitFromEnv`;
  `processor.LocalNNTPHostname = "test.invalid"` with cleanup; `newProcessor` replaced by the
  `server_e2e_test.go:163-182` hand build with history under `<data>/history` and `database.BatchInterval`
  lowered; all waits bounded by `testWait`; one DB open at a time: run, then reopen to check): `-help` → 0;
  unknown flag → 2; no input → 2; `-dry-run` on a 3-article mbox → 0, data dir absent, no `.done`, per-group counts
  logged; real run → 0, both target groups exist (`MainDBGetNewsgroup`), group DBs hold the rows
  (`GetGroupDB` + `LookupMsgIdInArticlesDB`), history has the ids (`History.Exists`), file renamed `.done`; rerun
  on the `.done` path → present = N, stored = 0, exit 0; `-mark-done=false` keeps the name; article without
  Subject → skipped, input still marked done; invalid first group → skipped; `-group forced.group` → every
  article there; pre-closed `stopChan` → 130, nothing renamed; directory input with two files; missing input
  path → 1 after the other input was imported.
  Checks: `gofmt -l cmd/mbox-importer`; `go vet ./...`; `go build ./...`; `go test -race ./cmd/mbox-importer/... ./internal/processor/...`.

### Wave 3 (verifier)
Full Checks, `./build_mbox-importer.sh`, the End-to-end below.

## Checks (merged tree)
```bash
gofmt -l cmd/mbox-importer internal/processor      # nothing
go vet ./... && go build ./...
grep -rn "processArticle" internal/ cmd/ docs/ BUGS.md   # nothing: only ProcessArticle remains
go test -race ./internal/processor/... ./cmd/mbox-importer/... ./cmd/rslight-importer/... ./cmd/post-queue/... ./cmd/nntp-fetcher/... ./cmd/nntp-server/...
# then the full CLAUDE.md race list (now including ./cmd/mbox-importer/...)
PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/   # only T-nntp-article-1
./build_mbox-importer.sh && ls -la build/mbox-importer
```

## End-to-end (verifier; scratch root `-data ./data-test-mbox`; never `./data`, `data_ram`, `data_local`)
```bash
cd /tank0/efs/GO/src/github.com/go-while/go-pugleaf
./build_mbox-importer.sh && ./build_nntp-server.sh
mkdir -p ./data-test-mbox/in
cp /tank0/efs/GO/src/github.com/go-while/mbox2nntp/data/mbox/de.comm.internet.commerce.mbox ./data-test-mbox/in/
gzip -kc ./data-test-mbox/in/de.comm.internet.commerce.mbox > ./data-test-mbox/in/copy1.mbox.gz
(cd ./data-test-mbox/in && zip -q copy2.mbox.zip de.comm.internet.commerce.mbox && 7z a -bd copy3.mbox.7z de.comm.internet.commerce.mbox >/dev/null)
HOST=i2pn2.pugleaf.net     # must resolve: NewProcessorE does a DNS lookup

# 1. dry run: no database created, nothing renamed, per-group counts (~1938 de.comm.internet.commerce, 207 de.alt.rec.digitalfotografie, ...)
./build/mbox-importer -dry-run ./data-test-mbox/in/de.comm.internet.commerce.mbox; echo exit=$?   # 0
ls ./data-test-mbox            # only "in"

# 2. first real run: creates the groups, stores every article, marks the input done
./build/mbox-importer -data ./data-test-mbox -nntphostname "$HOST" ./data-test-mbox/in/de.comm.internet.commerce.mbox; echo exit=$?   # 0
ls ./data-test-mbox/in/*.done && find ./data-test-mbox/db -name '*.db' | wc -l   # ~40 group DBs
sqlite3 ./data-test-mbox/cfg/pugleaf.sq3 "select name,message_count from newsgroups order by message_count desc limit 5"
# summary: read=N stored=N-skipped-rejected present=0 errors=0 marked-done=true; skipped/rejected explained in the log
# the history has the ids: ./build/history-rebuild -data ./data-test-mbox -validate (or the tool's own validate flag) shows 0 "Without number"

# 3. rerun on the archives: everything present, nothing stored, each archive renamed .done
./build/mbox-importer -data ./data-test-mbox ./data-test-mbox/in/copy1.mbox.gz ./data-test-mbox/in/copy2.mbox.zip; echo exit=$?   # 0
./build/mbox-importer -data ./data-test-mbox -7z 7z ./data-test-mbox/in/copy3.mbox.7z; echo exit=$?                              # 0
./build/mbox-importer -data ./data-test-mbox -7z /nonexistent/7z ./data-test-mbox/in/copy3.mbox.7z.done; echo exit=$?            # 1, input error

# 4. directory input + -group override into a fresh group, -mark-done=false
cp ./data-test-mbox/in/de.comm.internet.commerce.mbox.done ./data-test-mbox/in/again.mbox
./build/mbox-importer -data ./data-test-mbox -group test.mbox.forced -mark-done=false ./data-test-mbox/in; echo exit=$?   # 0
ls ./data-test-mbox/in     # again.mbox not renamed
sqlite3 "$(find ./data-test-mbox/db -name 'test.mbox.forced.db')" "select count(*) from articles"   # = read - skipped - rejected

# 5. signal: start a run on again.mbox into another -group, SIGINT it → exit 130, no .done, clean shutdown log lines
# 6. read-back: ./build/pugleaf-nntp-server -data ./data-test-mbox -nntptcpport 11194 (non-default port); GROUP de.comm.internet.commerce;
#    ARTICLE <bda1r8$qitgb$1@ID-55065.news.dfncis.de> → Path starts with "$HOST!", no giganews Xref line, body intact; stop the server (exit 0).
```
Leave `./data-test-mbox` for the user to delete.

## Decisions taken (change at the wave boundary if wanted)
- Invalid first group → skip (`bad-group`), no fallback to a later group.
- More than 9 groups in `Newsgroups:` is rejected by the processor's bulk check; counted `rejected`, no rewrite.
- Lax message-ids by default (`-strict-msgid` opts into `common.ValidMessageID`).
- Present detection by pre-lookup `LookupMsgIdInArticlesDB` (one indexed SELECT per article).
- An input that was not marked done without a fatal error exits 0 (the log says why), like mbox2nntp.
- Sequential: one reader per run; `-threads` (files in parallel) is a later plan if needed.

## Risks
- `NewProcessorE` requires a resolvable hostname (DNS); tests use the `newProcessor` seam; README says so.
- Group names are used as given (no lowercasing), like rslight-importer.
- The same message-id twice in one mbox reaches the batch twice; the batch's `INSERT OR IGNORE` drops the second
  (logged `duplicate message-ids ignored`); `stored` may over-count by those.
- `.zip` needs a seekable file; `.7z` CRC errors surface only at `Close()`, after the articles were handed off:
  the input is then not marked done, nothing else is undone.
- `ENABLE_HISTORY` is read once; `-history=false` runs write no history tokens (a later `history-rebuild` backfills).
- Out-of-order replies get thread root 0; documented, not fixed.

### Progress
- Wave 0 (5d6d007 on `plan-mbox-importer`, branched from testing-001 ac73462): `processArticle` → `ProcessArticle`
  in every Go file and in docs/ + internal/spam/README.md (archived plans under .claude/plans/done keep the old
  name); `EnsureNewsgroup(db, name, description)` exported in rslight.go; `cmd/mbox-importer` stubs with the
  shared signatures; go.mod: `github.com/go-while/go-lib-mbox2nntp v0.0.0-20261017083307-028f55355c84` and,
  pulled in by its go directive, `go 1.26.0` → `go 1.27.1` (toolchain go1.27.2 unchanged; README version text is
  the docs slice's job). Checks: gofmt empty, vet, build, `go test -race ./internal/processor/...` green.
