# Audit audit-tools (base b962f3a)

Audit of the fetch, analyze and import tools. The report follows `docs/audit/README.md` (D1, K6).
Findings from the merged plans are cited as `WSH:<id>`, `LO:<id>`, `FU:<id>`. Findings from sibling
reports are cited, not reported again: `A-db-1`, `A-db-2`, `A-db-9` (`audit-db.md`), `A-intake-3` and
`A-intake-11` (`audit-intake.md`), and `OPS-1`/`OPS-3`/`OPS-4` (seeds owned by `audit-ops`). Every
`path:line` anchor below exists at b962f3a. Paths are repo-relative.

## Scope and method

Files read in full:
- `cmd/nntp-fetcher/main.go`, `cmd/nntp-analyze/main.go` (+ `README.md`), `cmd/tcp2tor/{main.go,build.sh,example.cfg}`,
  `cmd/import-flat-files/main.go`, `cmd/rslight-importer/main.go` (+ `README.md`), `cmd/merge-active/main.go`,
  `cmd/merge-descriptions/{main.go,build.sh}`, `cmd/extract_hierarchies/extract_hierarchies.go`,
  `cmd/test-nntp/main.go`, `cmd/parsedates/main.go`, `cmd/benchmark_hash/{main.go,debug.log}`
- `internal/processor/{analyze.go, rslight.go, rslight_articles.go}`, `internal/database/progress.go`

Callers and callees read for reachability and the memory analysis:
- `internal/processor/proc_DLArt.go` (`DownloadArticles`, `GetOrCreateGroupBatch`, `DownloadArticlesFromDate`)
- `internal/processor/threading.go:57-283` (`processArticle`), `internal/processor/processor.go:36-113`
- `internal/database/db_batch.go:120-164,1753-1810` (`NewSQ3batch`, `BatchCaptureOverviewForLater`, `BatchDivider`)
- `internal/database/db_init.go:95-151`, `internal/database/db_groupdbs.go:218-227`, `internal/database/utils.go:108-127`
- `internal/nntp/nntp-client.go:150-235`, `internal/nntp/nntp-backend-pool.go:39-48,97-114,210-292`,
  `internal/nntp/proxy.go:26-66`, `internal/nntp/nntp-client-commands.go:850-892` (`ParseLegacyArticleLines`)
- `github.com/go-while/go-cpu-mem-profiler@v0.0.0-20240612221627-856954a5fc83/PROFILE.go` (`PprofWeb`, `memoryProfiler`)

Tools: `docs/audit/tools/*.txt` filtered to the scope:
- staticcheck/unused: `getRealMemoryUsage`, `ProgressDB.mux`, `formatNumber`
- errcheck: tcp2tor `Close`/`SetDeadline`, `progress.go:50,100`, `merge-descriptions` `Close`/`Flush`
- gosec: G201 at `import-flat-files/main.go:345` (A-tools-17), G302 at `analyze.go:955`, and weak-hash/rand
  hits in `benchmark_hash` (false positives for a benchmark)
- govulncheck: `cmd/rslight-importer/main.go:27` reaches `http.Server.ListenAndServe` through `PprofWeb` (A-tools-14)
- go vet: clean

No RFCs apply to this slice.

Repros: all scratch data lives under `./data-test-audit-tools/` (gitignored). Nothing connected to an external
server, and no tool ran against `./data`, `data_ram` or `data_local`.
- `_repro/batchpark/main.go`: parked `BatchCaptureOverviewForLater` goroutines (A-tools-1)
- `_repro/groupqueues/main.go`: retained `GroupQueues` channels (A-tools-4)
- `_repro/progressdb/main.go`: `NewProgressDB` hang (A-tools-6)
- `_repro/analyze/main.go`, run with cwd `./data-test-audit-tools` (A-tools-7, A-tools-8, A-tools-10)

  All four run with `cd data-test-audit-tools && go run ./_repro/<name>`.
- temporary `internal/processor/zz_audit_tools_test.go` (rslight parser, A-tools-15) and
  `cmd/tcp2tor/zz_audit_tools_test.go` (silent local SOCKS listener on 127.0.0.1:0, `-race`; OPS-4,
  A-tools-13). Both were deleted before the commit.
- built binaries run in scratch directories: `extract_hierarchies` (A-tools-18), and `import-flat-files` with
  scratch `-head/-body/-db` (A-tools-17, killed by `timeout 600`). The resulting 392 MiB DB file was deleted.

## Summary

Counts by severity: **high 2, medium 12, low 9** (23 findings: 21 new, plus the verified parts of the CLI-6 and
OPS-4 seeds). By category: bug 9, robustness 5, security 4, dead-code 3, wrong-function 2.

| severity | findings |
|----|----|
| high | A-tools-1, A-tools-2 |
| medium | A-tools-3, A-tools-4, A-tools-5, A-tools-6, A-tools-7, A-tools-8, A-tools-9, OPS-4 (tcp2tor part), A-tools-12, A-tools-14, A-tools-15, A-tools-17 |
| low | A-tools-10, A-tools-11, A-tools-13, A-tools-16, A-tools-18, A-tools-19, A-tools-20, A-tools-21, CLI-6 (test-nntp part) |

Five most important:
1. **A-tools-1 (high)**: `processArticle` hands every stored article to `go BatchCaptureOverviewForLater(...)`. When
   the batch writer is behind, each call parks a goroutine that holds the full article, and nothing bounds how many
   park. This is the fetcher memory growth noted in `BUGS.md`. Measured: 20,000 articles of 16 KiB → 19,872 parked
   goroutines, +377 MiB live heap.
2. **A-tools-2 (high)**: `nntp-fetcher -fetch-newsgroups-from-remote` opens the main DB with `OpenDatabase(nil)`, i.e.
   `./data`, whatever `-data` says. With `-fetch-newsgroups-force` it inserts rows into the production DB.
3. **A-tools-3 (medium)**: the same path builds its backend without the provider's proxy settings and without
   `Provider`. A provider configured for Tor/SOCKS is contacted directly (the real IP leaks). An onion host, or any
   connect failure, dereferences the nil `Provider` and panics.
4. **A-tools-5 (medium)**: progress is stored as soon as articles are handed to the batch (not committed), and the
   fetcher handles only SIGINT. SIGTERM, an OOM kill (see A-tools-1) or the late `log.Fatalf` drop the queued articles
   for good, because the next run starts after them.
5. **A-tools-14 (medium)**: `rslight-importer` always serves `net/http/pprof` on `:51111` (all interfaces), before
   flag parsing, with no way to turn it off. It also writes `mem.pprof.<ts>.out` into the cwd.

## Findings

### A-tools-1: one parked goroutine per stored article; unbounded fetcher memory growth

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-1 | high | robustness | `internal/processor/threading.go:256,283`, `internal/database/db_batch.go:156-164,1753,1787-1801`, `internal/processor/proc_DLArt.go:282-290`, `cmd/nntp-fetcher/main.go:536-584` | CONFIRMED |

- **Scenario:** `nntp-fetcher` with many connections (`MaxConns` download workers, `main.go:536`) downloads faster
  than the SQLite batch writer commits. This is the `BUGS.md` case: "eats memory when sucking with few hundreds of
  connections". The heap grows until the process is OOM-killed.
- **Evidence:**
  - Code trace:
    - `processArticle` ends every stored group with `go proc.DB.Batch.BatchCaptureOverviewForLater(newsgroupPtr, article)`
      (`threading.go:256`) and returns `CasePass` right away (`:283`).
    - `BatchCaptureOverviewForLater` blocks on `BatchDividerChan <- article` (`db_batch.go:163`; capacity 128, `:1753`).
    - The single `BatchDivider` blocks on a full per-group channel (`:1801`, capacity `maxDBbatch` ≤ 1000) and is
      supposed to block on `MaxQueued` (`:1787`). A-db-9 shows that counter drifts negative, so the `MaxQueued`
      brake never engages either.
    - The consumer in `DownloadArticles` counts the article as `gots++` (`proc_DLArt.go:284-290`), finishes the
      group and moves on. No semaphore, counter or blocking send limits the parked goroutines. Each one keeps
      `BodyText`, `HeadersJSON` and the other parsed fields alive.
  - Measured (`_repro/batchpark`, divider not running, i.e. the writer is stalled):
    ```
    articles handed over: 20000
    BatchDividerChan len/cap: 128/128
    parked goroutines: 19872
    live heap growth after GC: 377.0 MiB
    ```
  - Contributing, cited and not counted here: with `-reuse-crossposts` (default on) `reuseStoredArticle` reads
    through `GetArticleByMessageID` (`internal/processor/proc_reuse.go:76`), which `Put`s into `ArticleCache`.
    A-db-2 shows that map grows without bound.
- **Fix:** make the hand-off synchronous: call `BatchCaptureOverviewForLater` inline, so the send blocks the caller.
  Alternatively, take a slot from a bounded semaphore sized to `MaxQueued` before the `go`, and release it in the
  divider. Fix A-db-9 so the `MaxQueued` brake is real. Effort S–M.

### A-tools-2: `-fetch-newsgroups-from-remote` ignores `-data` and opens `./data`

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-2 | high | bug | `cmd/nntp-fetcher/main.go:76,91-96,806-813,948-950`, `internal/database/db_init.go:95-97,149-151` | CONFIRMED |

- **Scenario:** an operator runs
  `nntp-fetcher -data ./data-test-x -fetch-newsgroups-from-remote '$all' -fetch-newsgroups-force`. The tool opens,
  migrates and writes `./data/cfg/pugleaf.sq3` (relative to the cwd), not `./data-test-x`. This breaks the data-safety
  contract in CLAUDE.md ("always pass an explicit scratch root").
- **Evidence:**
  - Code trace:
    - The branch runs at `main.go:91-96`, before `dbConfig.DataDir = *dataDir` (`:128`).
    - `UpdateNewsgroupList` calls `database.OpenDatabase(nil)` (`:810`). `OpenDatabase` substitutes
      `DefaultDBConfig()` for nil (`db_init.go:149-151`), and that config has `DataDir: "./data"` (`:97`).
    - With `-fetch-newsgroups-force` every new remote group is inserted (`main.go:948-950`). Without it the main DB
      is still opened and migrated.
    - The function never shuts the DB down: `main` calls `os.Exit(0)` (`:95`).
  - Not executed, because running it would open `./data`.
- **Fix:** pass the `-data` value into `UpdateNewsgroupList` and open with a `DBConfig` whose `DataDir` is set.
  Close the DB (`StopChan`/`WG.Wait`/`Shutdown`) before returning. Effort S.

### A-tools-3: newsgroup-list update bypasses the provider proxy and panics on a nil Provider

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-3 | medium | security | `cmd/nntp-fetcher/main.go:838-848` (vs `:226-266`), `internal/nntp/nntp-client.go:165-167,173-191`, `internal/nntp/nntp-backend-pool.go:277,289` | CONFIRMED |

- **Scenario:** two cases.
  - A provider is configured with `ProxyEnabled`/`ProxyType socks5` (e.g. Tor) so that the server never sees the
    operator's address. The fetch path copies the proxy fields (`main.go:240-245,260-265`). The list-update path
    builds `BackendConfig{Host, Port, SSL, Username, Password, MaxConns: 1}` only (`:838-845`), so
    `NewProxyDialer(...).Dial` takes the `!ProxyEnabled` branch (`internal/nntp/proxy.go:35-36`) and connects
    directly. The IP leaks.
  - The provider host is a `.onion`, or the connection fails for any reason. `Provider` is nil in this config, and
    `Connect` (`nntp-client.go:167`) or `Pool.Get` (`nntp-backend-pool.go:277,289`) format `pool.Backend.Provider.Name`,
    which panics with a nil pointer dereference.
- **Evidence:** code trace above. Not run, because the path opens `./data` (A-tools-2). The same class of nil-`Provider`
  dereference was reproduced at a different site on the analyze path (A-tools-8).
- **Fix:** build the list-update backend with the same helper as the fetch path, so the proxy fields and `Provider`
  are always set. In `nntp`, stop dereferencing `Backend.Provider` in log and error paths: use `Host` or a nil-safe
  accessor. Effort S.

### A-tools-4: per-group ReturnQ channels are never released; `-max-batch` scales them unbounded

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-4 | medium | robustness | `internal/processor/proc_DLArt.go:44-64,80`, `internal/processor/processor.go:45,55`, `cmd/nntp-fetcher/main.go:65,115-117,123-124`, `internal/database/db_batch.go:122-126` | CONFIRMED |

- **Scenario:** a `$all` or wildcard fetch over tens of thousands of groups. Every group that reaches
  `DownloadArticles` adds `GroupQueues[name] = &GroupBatch{ReturnQ: make(chan *BatchItem, MaxBatchSize)}`.
  Nothing deletes from the map (`grep -rn GroupQueues` finds only `proc_DLArt.go:25,50-59` and `processor.go:55`).
  `-max-batch` is only warned about above 1000 (`main.go:115-117`) and flows into `processor.MaxBatchSize`
  unclamped (`:124`). The DB batch size, by contrast, is clamped to 1000 (`db_batch.go:122-126`).
  A timed-out run also leaves late results with full articles in that group's `ReturnQ` until the process exits.
  The fetcher handles each group once per run, so nobody drains them. This adds a memory angle to A-intake-11
  and is not counted again.
- **Evidence:** measured (`_repro/groupqueues`):
  ```
  MaxBatchSize=1000 groups=20000 map=20000 retained heap=159.7 MiB
  MaxBatchSize=10000 groups=20000 map=20000 retained heap=1565.9 MiB
  ```
- **Fix:** create the `ReturnQ` per `DownloadArticles` call (which A-intake-11 already proposes) and drop the
  global map, or delete the entry when the call returns. Clamp `-max-batch` to the same upper bound as `MaxDBbatch`.
  Effort S.

### A-tools-5: progress is persisted before articles are committed; SIGTERM is not handled

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-5 | medium | bug | `internal/processor/proc_DLArt.go:223-225,284-290,300-322`, `internal/processor/threading.go:256`, `cmd/nntp-fetcher/main.go:146-147,660-663` | CONFIRMED (crash path), PLAUSIBLE (graceful path) |

- **Scenario:**
  - `DownloadArticles` leaves its loop once `gots+errs+notf == gotQueued` (`proc_DLArt.go:223-225,300-302`). It
    then writes `progressDB.UpdateProgress(..., end)` (`:316-322`). `gots` counts articles that were only handed to
    a goroutine (A-tools-1), not committed.
  - The fetcher installs a handler for `os.Interrupt` only (`main.go:147`). On SIGTERM (systemd's stop signal), an
    OOM kill, or the `log.Fatalf` for an invalid `-download-start-date` raised inside a worker (`:660-663`, after the
    DB and processor are running), the process ends while articles are still parked or queued.
  - The stored progress already points past them, so the next run never fetches them again. Only a date rescan
    recovers them.
- **Evidence:** code trace above.
  - Graceful path (SIGINT): `waitHere.Wait` → `close(db.StopChan)` → `proc.Close` → `db.WG.Wait` → `Shutdown`
    (`main.go:750-764`). Whether parked goroutines that are still blocked on `BatchDividerChan` get drained before
    `Shutdown` was not verified. Missing step: a run with a stalled writer and SIGINT, then a count of committed rows.
- **Fix:** move the progress update to the batch writer: record the highest contiguous article number per group
  after commit. Alternatively, wait until the group's queued articles are committed before `UpdateProgress`. Add
  `syscall.SIGTERM` to `signal.Notify`. Validate `-download-start-date` before opening the DB. Effort M.

### A-tools-6: `NewProgressDB` retries schema creation forever and silently

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-6 | medium | robustness | `internal/database/progress.go:78-88`; callers `cmd/nntp-fetcher/main.go:139`, `cmd/web/main.go:226` | CONFIRMED |

- **Scenario:** `progress.db` cannot be opened or written: permissions, a read-only filesystem, a corrupt file, or a
  directory with that name. `initSchema` loops `Exec` → `sleep 100ms` → `continue` with no limit and no log line.
  `nntp-fetcher` and `web` both hang at startup and never say why.
- **Evidence:** `_repro/progressdb`, with `progress.db` created as a directory:
  ```
  NewProgressDB still blocked after 5.000358195s (no log line, no error)
  ```
- **Fix:** use `RetryableExec` (it is bounded and retries only busy/locked) and return the error. Effort S.

### A-tools-7: `nntp-analyze` writes its cache to `./data/cache` regardless of `-data` (LO:A3 residual)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new (residual of LO:A3) | A-tools-7 | medium | bug | `internal/processor/analyze.go:495-501`, `internal/processor/analyze.go:908-911`, `cmd/nntp-analyze/main.go:175-181,206-210,479-481` | CONFIRMED |

- **Scenario:** `nntp-analyze -data ./data-test-x -group alt.test` (single group) or `-groups-file f` creates and
  overwrites `data/cache/<host>/<sha256>.overview` relative to the cwd. In the repo checkout that is the production
  `./data`. LO:A3 switched `getCacheFilePath` to `proc.DB.GetDataDir()`, but it falls back to `"data"` when
  `proc.DB == nil` (`analyze.go:496-499`). Two of the three modes build `&processor.Processor{Pool: pool}` without a
  DB (`analyze.go:909-911`, `main.go:479-481`), and `-data` is passed only to `analyzeAllGroups` (`main.go:186-188`).
- **Evidence:** `_repro/analyze`, run from `./data-test-audit-tools`: the cache lookup resolves to
  `data/cache/example.invalid/20ec6135….overview` under the cwd.
- **Fix:** pass `-data` to every mode. Give `Processor` an explicit cache root, or set a DB-less data dir field, and
  remove the `"data"` fallback. Effort S.

### A-tools-8: analyze cache keyed two ways; `-clear-cache` panics; `-cache-stats`/`-validate-cache` never find the cache

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-8 | medium | bug | `internal/processor/analyze.go:124-129,152` vs `:386-391,510-514,587-591,607-611,632-636,691-694` | CONFIRMED |

- **Scenario:** `AnalyzeGroup` names the cache directory after `filepath.Base(Backend.Host)` (`:124-129`). Every
  other cache function uses `Backend.Provider.Name`, or `"unknown"` when `Provider` is nil. `nntp-analyze` never sets
  `Provider`. Results:
  - `ClearCache` (`:589-590`) and `GetCachedMessageIDs` (`:512-513`) check only `Backend != nil` and then dereference
    `Provider.Name`, so they panic. `nntp-analyze -clear-cache` always crashes.
  - `GetCacheStats`, `ValidateCacheIntegrity`, `FindArticlesByDateRange` and `GetArticleCountByDateRange` look under
    `cache/unknown/`, so they report "no cached data" for a cache that exists.
- **Evidence:** `_repro/analyze` (the cache file was written where `AnalyzeGroup` writes it):
  ```
  GetCacheStats("alt.test") err=no cached data found for group 'alt.test'
  ValidateCacheIntegrity("alt.test") err=cache file does not exist for group 'alt.test'
  ClearCache("alt.test") PANIC: runtime error: invalid memory address or nil pointer dereference
  GetCachedMessageIDs("alt.test") PANIC: runtime error: invalid memory address or nil pointer dereference
  ```
- **Fix:** one `cacheProviderName()` helper used everywhere (Host-based, since that is what exists in every mode),
  nil-safe. Effort S.

### A-tools-9: analyze persists partial caches as complete and trusts them forever

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-9 | medium | bug | `internal/processor/analyze.go:155-158,186-193,205-219,282,303-377` | CONFIRMED (code trace) |

- **Scenario:**
  - `analyzeFromRemote` truncates the cache file with `os.Create` (`:186`). It skips any XOVER batch that errors
    (`:215-219`, `continue`) and then sets `CacheExists = true` (`:282`).
  - The next run sees a non-empty file and uses it unless `-force-refresh` is given (`:155-158`). One transient error
    therefore fixes wrong counts, sizes and date ranges for good. A crash in the middle leaves a truncated file that
    is used the same way, and the deferred `Flush` error is dropped (`:193`).
  - `analyzeFromCache` never checks `scanner.Err()` (`:303-377`). A line over 64 KiB (long `References`) ends the
    scan silently, and the stats cover only the prefix.
  - A hostile or broken `GROUP` reply with `Last` near `MaxInt64` makes `batchStart += AnalyzeBatchSize` (`:205`)
    overflow, so the loop never ends.
- **Evidence:** code trace above.
- **Fix:** write to `<file>.tmp` and rename only after every batch succeeded, and fail the analysis on a batch error.
  Check `scanner.Err()` and raise the buffer. Bound the range: `Last-First` sane, no overflow. Effort S.

### A-tools-10: JSON export is not JSON; CSV export is not quoted

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-10 | low | wrong-function | `internal/processor/analyze.go:740-769,772-786` | CONFIRMED |

- **Scenario:** `ExportAnalysisToJSON` builds JSON with `Sprintf`: a trailing comma after `cached_articles`
  (`:753`), and no escaping of `group_name`/`provider_name`. Every `-export json` file fails to parse. The CSV
  export does not quote fields either.
- **Evidence:** `_repro/analyze`:
  `json.Valid=false unmarshal err=invalid character 'q' after object key:value pair` (quoted name) and
  `plain group: json.Valid=false` (trailing comma).
- **Fix:** `encoding/json.Marshal` of a tagged struct and `encoding/csv`. Effort S.

### A-tools-11: analyze export path is built from an unvalidated group name, under `./out` in the cwd

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-11 | low | security | `internal/processor/analyze.go:928-955`, `cmd/nntp-analyze/main.go:43-55,529,598` | CONFIRMED (code trace) |

- **Scenario:** `-groups-file` takes `fields[0]` of each active line unvalidated (`main.go:50-55`). Active files are
  often downloaded from remote servers (the help text names `active.i2pn2.txt`). With `-export json` the name goes
  into `fmt.Sprintf("out/%s.json", GroupName)` (`analyze.go:939`). A line `../../../tmp/x 1 1 y` writes
  `out/../../../tmp/x.json`. The `.json` suffix is fixed, so the impact is limited to creating or overwriting
  `*.json` files the user can write.
  - The CSV name uses the first label (`:928,953`).
  - All output goes to `./out` in the cwd (`:930-935`, `main.go:529,598`), not under `-data`.
  - A write error calls `os.Exit(1)` from library code. That is OPS-1 (`analyze.go:934,943,948,962`), cited, not
    counted here.
- **Evidence:** code trace above.
- **Fix:** validate group names (`common.IsValidGroupName`) on input and name export files by a sanitized name or a
  hash. Add an `-out` flag. Effort S.

### OPS-4 (tcp2tor part): `-timeout` is stored and never read; dials have no deadline

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| OPS-4 | OPS-4 | medium | robustness | `cmd/tcp2tor/main.go:102,177,252,503,514,519,532,551,626,628,642` | CONFIRMED |

- **Scenario:** `-timeout` (`:102`) is copied into `ProxyServer.Timeout` (`:177,252`). That field is never read
  (`:551`), and `testSOCKS5Connection`'s `timeoutSec` parameter (`:503`) is unused. Both dialers are
  `proxy.SOCKS5(..., proxy.Direct)` (`:514,519,626,628`), whose dial and SOCKS handshake have no deadline. Against a
  SOCKS proxy (Tor) that accepts TCP and then stalls, the startup self-test hangs, and so does each client
  connection, forever.
- **Evidence:** temporary `cmd/tcp2tor/zz_audit_tools_test.go` with a local listener on 127.0.0.1:0 that accepts and
  never answers:
  ```
  testSOCKS5Connection(timeoutSec=1) still blocked after 5.004369778s
  handleConnection (Timeout=1s) still blocked after 5s
  ```
- **Fix:** use `&net.Dialer{Timeout: s.Timeout}` as the forward dialer, wrap the dial in a context with
  `proxy.ContextDialer.DialContext`, and set a deadline on the handshake. Effort S.

### A-tools-12: tcp2tor has no connection limit, no idle timeout and no bind restriction

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-12 | medium | security | `cmd/tcp2tor/main.go:88,245,584-596,611-675`, `cmd/tcp2tor/example.cfg:7` | CONFIRMED (code trace) |

- **Scenario:** each accepted connection gets an unbounded goroutine (`:596`) that dials through Tor and pumps with
  `io.Copy` in both directions with no deadline (`:657-675`). There is no maximum connection count and no idle or
  total timeout. Together with the missing dial timeout (OPS-4), a client can open connections until file
  descriptors or Tor circuits run out.
  - The listen address is free text. The shipped `example.cfg` documents `0.0.0.0:3119:public-ip.onion:119`.
  - Bound that way, the tool is an unauthenticated relay that lets anyone reach the configured hidden service
    through the operator's Tor client, under the operator's identity.
  - The default `127.2.3.4` (`:88`) is loopback on Linux only.
- **Evidence:** code trace above. No limit, deadline or ACL exists in `Start`/`handleConnection`.
- **Fix:** add a `-max-conns` semaphore and idle/total deadlines (refreshed per read). Refuse non-loopback binds
  unless `-allow-public` is given. Drop the `0.0.0.0` example. Effort S.

### A-tools-13: tcp2tor `Start`/`Stop` race; `Stop` before `Start` leaves the listener running

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-13 | low | bug | `cmd/tcp2tor/main.go:558-608,187-195,257-280` | CONFIRMED |

- **Scenario:** `Start` assigns `s.listener` (`:560`) and `s.shutdown` (`:566`) in the server goroutine. `Stop`
  reads them from the signal path (`:601-608`) with no synchronization.
  - A signal that arrives before `Start` has set them finds nil and closes nothing. The server keeps accepting.
  - In config mode the following `wg.Wait()` (`:280`) then never returns.
  - `handleConnection` returns as soon as one direction ends (`:675`). A client that half-closes after its last
    command can therefore lose the server's final reply. This part is PLAUSIBLE and was not reproduced.
- **Evidence:** `go test -race` on the temporary test: `WARNING: DATA RACE` write at `main.go:560`/`:566` by `Start`,
  previous read at `:605`/`:602` by `Stop`, then `Start still running 3s after Stop()`.
- **Fix:** create the listener and the shutdown channel before starting the goroutine (`net.Listen` in the caller),
  or guard them with a mutex. Use `CloseWrite` for half-close. Effort S.

### A-tools-14: rslight-importer always exposes pprof on `:51111` and writes heap profiles to the cwd

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new (same class as OPS-3) | A-tools-14 | medium | security | `cmd/rslight-importer/main.go:26-28`; `go-cpu-mem-profiler/PROFILE.go:34-53,109-139` | CONFIRMED |

- **Scenario:** at startup, before `flag.Parse`, `Prof.PprofWeb(":51111")` starts an `http.Server` on all
  interfaces. It has `/debug/pprof/{,cmdline,profile,symbol,trace,…}`, no auth and no timeouts. Anyone who can reach
  the host during an import can read argv and heap/goroutine profiles, or start CPU profiles and traces, which costs
  CPU.
  - `StartMemProfile(5m, 30s)` writes `mem.pprof.<unix>.out` into the cwd 30 s after start.
  - No flag disables either.
  - govulncheck lists this call path as reaching `net/http` vulnerabilities (`docs/audit/tools/govulncheck.txt`,
    `rslight-importer/main.go:27`).
- **Evidence:** code trace of `PprofWeb` (gorilla router, `server := &http.Server{Addr: addr, Handler: router}`,
  `ListenAndServe`).
- **Fix:** remove it, or put it behind `-pprof 127.0.0.1:port` (empty = off), as the OPS-3 fix plan does for the
  other tools. Effort S.

### A-tools-15: rslight article parser keeps the dot terminator and dot-stuffing; CRLF input loses the body

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-15 | medium | bug | `internal/processor/rslight.go:61,307-322,333-334`, `internal/nntp/nntp-client-commands.go:857-884` | CONFIRMED (parser); PLAUSIBLE (that the spool uses CRLF) |

- **Scenario:** `LegacyArticle.Article` is documented as "wireformat! article incl. full headers, body and final
  DOT+CRLF pair" (`rslight.go:61`).
  - `parseArticleTextToSlice` splits on `"\n"` only and drops the last element only when it is exactly `"."`
    (`:312-317`). For text that ends in `".\n"`, the last element is `""`, so the `"."` terminator survives into
    the body.
  - Lines starting with `".."` are never unstuffed.
  - With CRLF line ends, the blank separator line is `"\r"`. `ParseLegacyArticleLines` looks for `""`
    (`nntp-client-commands.go:858-862`) and finds it only at the trailing empty element. The whole body is then
    parsed as headers, and the stored body is empty.
- **Evidence:** temporary `internal/processor/zz_audit_tools_test.go`:
  ```
  LF+dotstuffed: lines=8 size=70 subject="s" body="line1\n..dotline\n.\n"
  CRLF wireformat: lines=8 size=77 subject="s" body=""
  ```
  Missing step for the CRLF half: no RockSolid Light spool was available to confirm the stored line ends.
- **Fix:** normalize `\r\n` → `\n`, strip a final `"."` line (allowing for the trailing empty element), and unstuff
  leading `".."`. Add a unit test with real spool samples. Effort S.

### A-tools-16: rslight import: shared `err` across goroutines, success reported on failure, counters wrong

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-16 | low | bug | `internal/processor/rslight.go:374,464,471,482-498`, `internal/processor/rslight_articles.go:105-113,143`, `cmd/rslight-importer/main.go:107-161,163` | CONFIRMED (code trace) |

- **Scenario:**
  - The worker goroutines assign the enclosing function's `err` (`rslight.go:374` declared, `:464`
    `err = leg.ImportSQLiteArticles(file)`), a data race with `-threads > 1`.
  - `ImportAllSQLiteDatabases` counts the errors but always returns nil (`:498`), so `main` prints
    "SUCCESS! Import process completed!" (`main.go:163`).
  - `groupsDone` is incremented twice per file (`rslight_articles.go:143` and `rslight.go:471`).
  - `skipped`/`skippedEmpty` are swapped: non-empty failures increment `skippedEmpty`
    (`rslight_articles.go:107-110`). Duplicates (`response != CasePass` with nil err, `rslight.go:348-349`) count as
    failures.
  - Every `log.Fatalf` after the DB is open (`main.go:107-161`) skips `proc.Close()`. The tool's own comment
    (`:165-186`) says that produces 0-byte history files.
- **Evidence:** code trace above.
- **Fix:** use a local `err` in the goroutine, return an aggregated error, fix the counters, and replace the fatal
  exits with an error return through one shutdown path. Effort S.

### A-tools-17: import-flat-files: one odd filename drops a 10k batch; first DB open takes over 10 minutes

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-17 | medium | bug | `cmd/import-flat-files/main.go:88-135,145-150,319-369,373,458-478` | CONFIRMED (code trace + measurement) |

- **Scenario:**
  - The table name is `articles_<dir3><first 3 chars of the file name>` (`:145-150`), put into SQL with `Sprintf`
    (`:345`, gosec G201). Only three characters are attacker-influenced, so this is not an injection. A file whose
    name does not start with three hex digits (the only check is length ≥ 32, `:373`) makes `tx.Prepare` fail. That
    returns from `processBatch`, and the deferred `Rollback` discards every other article of the up-to-10,000 batch
    (`:330-349`, logged as errors only).
  - A failing `Exec` (e.g. a duplicate without `-update`) is counted as an error and also as processed (`:353-366`).
  - `readFile` uses a default `bufio.Scanner` (`:466`). A line over 64 KiB fails the article, and CR/LF is
    normalized silently.
  - `GetDB` creates 65,536 tables plus 65,536 indexes that duplicate the primary key, one autocommit statement each,
    while holding the manager's write lock (`:88-130`). Every worker waits.
- **Evidence:**
  - Scratch run: `-head/-body ./…` with one good pair and one `zz-x….head` pair, `-db ./out -workers 1`. After 10
    minutes `timeout 600` killed it (exit 124). The first `00.db` had reached 392 MiB, the table creation had not
    finished, and the log showed `Processed=0` throughout.
  - The batch-drop path was therefore not reached at runtime. It is confirmed by the code trace above.
  - The tool's output schema is not read by any other pugleaf code (`grep imported_articles`, `articles_%04x`: no
    hits outside the tool).
- **Fix:** validate the hash (`^[0-9a-f]{3}`), else skip the file. Use `INSERT OR IGNORE` per statement, so one row
  never fails the transaction. Count `processed` per successful row. Create tables lazily, or as one transaction,
  and drop the redundant index. Read files with `os.ReadFile`. Effort M.

### A-tools-18: helper tools depend on hardcoded or missing paths

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-18 | low | dead-code | `cmd/extract_hierarchies/extract_hierarchies.go:24-25,168,175`, `cmd/merge-active/main.go:84-85,103-120,560,589,606-608`, `cmd/merge-descriptions/main.go:34-48,126-149` | CONFIRMED |

- **Scenario:**
  - `extract_hierarchies` reads `migrations/0003_main_create_hierarchies.sql` and `active_files/active.2025-06`
    relative to the cwd. Neither exists at the repo root (the migrations live in `internal/database/migrations/`).
    The tool also creates `sample.db` in the cwd.
  - `merge-active`:
    - The default mode needs `active_files/active.isc`, which is not in the repo (only
      `active_files/isc/active.isc.<ts>`).
    - The `active.*` glob also picks up its own previous `active.merged` (`:109-118`).
    - Output order is map order.
    - `-overview` hardcodes `/tank0/OV/overview/nntp-server-new` (`:589`) and `../active_files/local-mode.active`
      (`:560`, a different base than the default mode), and aborts on the first missing overview file (`:606-608`)
      instead of skipping it.
  - `merge-descriptions` ignores `Flush`/`Close` errors (`:126-149`), so a full disk truncates
    `newsgroups.descriptions` silently. Year files are applied in lexicographic order with first-wins, so the
    oldest year beats newer ones.
- **Evidence:** a scratch build of `extract_hierarchies` run in `./data-test-audit-tools/exh`:
  `Error initializing hierarchies: failed to read SQL file migrations/0003_main_create_hierarchies.sql: open …: no such file or directory`, exit 1.
  `ls active_files/` shows no `active.isc` or `active.2025-06`.
- **Fix:** take paths as flags, embed the hierarchy list from `internal/database/migrations` (it is already
  embedded), skip missing overview files, and check `Flush`/`Close`. Or delete the tools if they are no longer
  used. Effort S.

### A-tools-19: a compiled binary and a stale log are tracked under `cmd/benchmark_hash`

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-19 | low | dead-code | `cmd/benchmark_hash/benchmark_hash`, `cmd/benchmark_hash/debug.log`, `cmd/benchmark_hash/main.go:35` | CONFIRMED |

- **Scenario:** `git ls-files cmd/benchmark_hash` lists a 2.5 MB statically linked ELF
  (`file`: "ELF 64-bit LSB executable, x86-64 … not stripped") and `debug.log`. The log reports 100,000 ids, while
  `main.go:35` generates 1,000,000. Nobody can verify the binary against the source, and it ships in every clone.
  The gosec hits on this tool (MD5/SHA1/math/rand) are expected for a benchmark and are not findings.
- **Evidence:** `git ls-files`, `file` output above.
- **Fix:** `git rm --cached` both files and add them to `.gitignore`. Effort S.

### A-tools-20: nntp-fetcher control-flow leftovers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-20 | low | robustness | `cmd/nntp-fetcher/main.go:98-101,191,214-218,313,359-363,591,639,660-663` | CONFIRMED (code trace) |

- **Scenario:**
  - `processor.DownloadMaxPar` is assigned only when the flag is below 1 (`:98-101`). Its only consumer, the
    `TodoQ` capacity, is computed at package init (`processor.go:53`) anyway.
  - The feed goroutine `return`s on the first `MainDBGetNewsgroup` failure (`:359-363`), silently skipping every
    remaining group. One A-db-1 row is enough.
  - `errChan` (`:591`, capacity `len(newsgroups)` per worker) is written and never read.
  - Hardcoded per-host connection caps for eternal-september/blueworldhosting (`:214-218`) and "debug sleep"s
    (`:191,313`) remain.
  - The invalid-start-date `log.Fatalf` sits inside a worker (`:660-663`; data-loss side in A-tools-5).
- **Evidence:** code trace above.
- **Fix:** validate flags up front, `continue` instead of `return` in the feed loop, and remove the dead channel,
  the dead assignment and the debug sleeps. Move the caps to provider config. Effort S.

### A-tools-21: help texts, READMEs and defaults that do not match the code

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-tools-21 | low | wrong-function | `cmd/nntp-fetcher/main.go:39,47`, `cmd/nntp-analyze/main.go:133-136,138,195-203,312`, `cmd/nntp-analyze/README.md:52,143,146,208`, `cmd/rslight-importer/main.go:43-55`, `cmd/rslight-importer/README.md:8,19-35` | CONFIRMED |

- **Scenario:**
  - `nntp-fetcher -help` advertises `-test-conn` and `-update-list`. Neither flag exists (the second is
    `-fetch-newsgroups-from-remote`), so following the help exits with "flag provided but not defined".
  - `nntp-analyze`:
    - It defaults to the project's public server `81-171-22-215.pugleaf.net:563` with credentials `read`/`only`
      (`:133-136`).
    - `-timeout` is ignored in `$all` mode (`:312` commented out).
    - The `readGroupsFromFile` branch is unreachable (`:195-203`, already handled at `:175`).
    - The README documents `-force`, a `{sanitized_group_name}` cache name, "incremental updates" and "automatic
      retry and timeout handling", none of which exist.
  - `rslight-importer`:
    - The usage text names `-use-short-hash-len` and `-resetallgroups` and claims defaults `/etc/rslight` and
      `/var/spool/rslight`. The real flags are `-useshorthashlen` and `-YESresetallgroupsYES`, there are no such
      defaults, and `-etc` is mandatory even for a reset.
    - The README describes `cmd/sqlite-importer`, `-sqlite-dir`/`-legacy-path`/`-data-dir` and `*-data.db3`
      thread import. None of these exist; the code globs only `*-articles.db3` (`rslight.go:374`).
- **Evidence:** code trace above.
- **Fix:** rewrite the help texts and READMEs from the flag definitions. Require `-host` for `nntp-analyze`, or
  document the default. Effort S.

### CLI-6 (test-nntp part): hardcoded external server; commented calls to removed APIs

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-6 | CLI-6 | low | dead-code | `cmd/test-nntp/main.go:18-27,34-36,79-125` | CONFIRMED |

- **Scenario:** `test-nntp` builds (`go build ./...` passes). It connects to `news.blueworldhosting.com:563`
  (`:19-21`); no flag can change that. It never closes the connection. The block at `:79-125` calls
  `client.Pool.CloseConn`, `nntp.NewPool(config)` with a value, `pool.StartCleanupWorker(...)` and
  `pool.Get(ctx)`. Today these are the unexported `closeConn` (`nntp-backend-pool.go:378`), `NewPool(*BackendConfig)`
  (`:39`), the unexported `startCleanupWorker()` (`:546`) and `Get(wantMode int)` (`:210`). Only `Stats()` (`:444`)
  still exists with those fields.
  The `PeeringManager` half of CLI-6 (`internal/nntp/nntp-peering.go`) belongs to `audit-nntp-client` and was not
  re-checked here.
- **Evidence:** code trace above. Not run (it connects to an external server).
- **Fix:** delete the tool, or turn it into a `-host/-port/-ssl` smoke test with a deadline that closes its
  connection. Drop the comment block. Effort S.

## Flag table

Tool × flag × used? × default touches `./data`? ("cwd" = relative to the working directory, not to `-data`.)

| tool | flag | used? | default touches `./data`? |
|----|----|----|----|
| nntp-fetcher | `-data` | yes: main DB, `progress.db`, `cache/`, `history/` (`:128,139,291`, `processor.go:78`) | yes (`./data`); **ignored** by `-fetch-newsgroups-from-remote` (A-tools-2) |
| nntp-fetcher | `-fetch-newsgroups-from-remote`, `-fetch-newsgroups-force` | yes | yes, always `./data` (A-tools-2) |
| nntp-fetcher | `-exclude-prefix` | only with `-fetch-newsgroups-from-remote` | via that path |
| nntp-fetcher | `-reset-progress` | yes; writes `progress.db` under `-data`; cannot reset to 0 (`> 0` check, `:278`) | via `-data` |
| nntp-fetcher | `-max-batch` | yes; unbounded for `MaxBatchSize` (A-tools-4) | no |
| nntp-fetcher | `-max-batch-threads`, `-max-queue`, `-group`, `-nntphostname`, `-history`, `-useshorthashlen`, `-fetch-active-only`, `-reuse-crossposts`, `-download-start-date` | yes (`-download-start-date` validated late, A-tools-20) | via `-data` |
| nntp-fetcher | `-download-max-par` | yes for the worker count; the `processor.DownloadMaxPar` write is dead (A-tools-20) | no |
| nntp-analyze | `-data` | only in `$all`/wildcard mode (`:186-188`) | single-group and `-groups-file` modes write `data/cache` in the cwd (A-tools-7) |
| nntp-analyze | `-export` | yes; writes `out/` in the cwd (A-tools-11) | no |
| nntp-analyze | `-timeout` | in 2 of 3 modes (A-tools-21) | no |
| nntp-analyze | `-clear-cache` / `-cache-stats` / `-validate-cache` | broken (panic / wrong key, A-tools-8) | `-clear-cache` would remove under `data/` in the cwd |
| nntp-analyze | `-host`, `-port`, `-username`, `-password`, `-ssl` | yes; default external public server (A-tools-21) | no |
| nntp-analyze | `-group`, `-groups-file`, `-force-refresh`, `-max-analyze`, `-start-date`, `-end-date` | yes | via the cache path above |
| tcp2tor | `-timeout` | **no** (OPS-4) | no |
| tcp2tor | `-listen-host`, `-listen-port`, `-target`, `-config`, `-socks5-*`, `-verbose` | yes | no |
| tcp2tor | `-add <file>` | yes; appends to the named file | no |
| import-flat-files | `-head`, `-body` (`/mnt/xfshead`, `/mnt/xfsbody`), `-db` (`./imported_articles`), `-workers`, `-dry-run`, `-update`, `-verbose` | yes | no (own schema; no other code reads the output) |
| rslight-importer | `-data` | yes (main DB, group DBs, history) | yes (`./data`) |
| rslight-importer | `-YESresetallgroupsYES` | yes: `ResetAllNewsgroupData` on `-data` after a 5 s countdown, after sections were imported (`:107-143`) | yes, destructive |
| rslight-importer | `-etc` (required), `-spool`, `-threads`, `-useshorthashlen`, `-nntphostname`, `-history` | yes | via `-data` |
| rslight-importer | (none) pprof `:51111`, `mem.pprof.*.out` | always on (A-tools-14) | cwd |
| merge-active | `-filter <file>` | yes; writes `<file>.new`, `.filtered`, `.filtered.moreThan1000` | no |
| merge-active | `-overview` | yes; hardcoded `/tank0/OV/…`, `../active_files/…` (A-tools-18) | no |
| merge-active | (default mode) | reads `active_files/` in the cwd, writes `active_files/active.merged` | no |
| merge-descriptions | `<dir>` (positional) | yes; writes `<dir>/newsgroups.descriptions` | no |
| extract_hierarchies | (none) | hardcoded paths; writes `sample.db` and `active_files/hierarchies/` in the cwd (A-tools-18) | no |
| test-nntp | (none) | connects to `news.blueworldhosting.com:563` (CLI-6) | no |
| parsedates | `<date>` (positional) | yes | no |
| benchmark_hash | (none) | n/a | no |

## Function inventory

| function/file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `processor.(*Processor).GetCachedMessageIDs` (`analyze.go:510`) | | | yes | | yes | no caller; nil deref when `Provider` is nil (A-tools-8) |
| `processor.(*Processor).FindArticlesByDateRange` / `findDateRangeInCache` (`analyze.go:381,401`) | | | yes | | yes | no caller; looks under `unknown/` (A-tools-8) |
| `processor.(*Processor).GetArticleCountByDateRange` (`analyze.go:690`) | | | yes | | yes | no caller; same key mismatch |
| `processor.formatNumber` (`analyze.go:1009`) | | | yes | | | staticcheck U1000 |
| `AnalyzeOptions.IncludeHeaders` (`analyze.go:170`) | | | yes | yes | | set at `:863`, never read |
| `processor.(*GroupAnalysis).ExportAnalysisToJSON` (`analyze.go:740`) | | | | | yes | invalid JSON (A-tools-10) |
| `processor.(*Processor).ClearCache` (`analyze.go:587`) | | | | | yes | panics (A-tools-8) |
| `processor.(*Processor).GetCacheStats` / `ValidateCacheIntegrity` (`analyze.go:607,632`) | | | | | yes | wrong cache key (A-tools-8) |
| `processor.(*Processor).getCacheFilePath` (`analyze.go:495`) | | | | yes | | `"data"` fallback (A-tools-7) |
| `processor.printAnalysisResults` (`analyze.go:921`) | | | | | yes | `os.Exit(1)` in library (OPS-1) |
| `processor.InvalidDates` (`analyze.go:173`) | | | | | | unbounded global map, read without lock in `cmd/nntp-analyze` (single goroutine today) |
| `processor.isValidDatabaseFilename` (`rslight.go:548`) | | | | yes | | "prevent SQL injection": the file name never reaches SQL; the regex is commented out |
| `processor.LegacyThread` (`rslight.go:65`) | | yes | yes | | | no reference |
| `processor.parseArticleTextToSlice` (`rslight.go:307`) | | | | | yes | A-tools-15 |
| `processor.(*LegacyImporter).ImportAllSQLiteDatabases` (`rslight.go:370`) | | | | | yes | always nil; data race (A-tools-16) |
| `processor.(*LegacyImporter).QuickOpenToGetNewsgroup` (`rslight_articles.go:14`) | | | | | | result only logged; opens the legacy DB read-write (no `mode=ro`) |
| `database.(*ProgressDB).initSchema` (`progress.go:78`) | | | | | yes | infinite loop (A-tools-6) |
| `database.(*ProgressDB).GetAllProgress` / `GetProgressForBackend` (`progress.go:143,185`) | | | yes | | | no caller; no `rows.Err()` |
| `database.ProgressDB.mux` (`progress.go:19`) | | | yes | | | staticcheck U1000; all lock calls are commented out |
| `main.getRealMemoryUsage` (`cmd/nntp-fetcher/main.go:779`) | | | yes | | | only use is commented out (`:625`) |
| `main.showUsageExamples` (`cmd/nntp-fetcher/main.go:33`) | | | | yes | | non-existent flags (A-tools-21) |
| `main.readGroupsFromFile` (`cmd/nntp-analyze/main.go:214`) | | yes | | | | unreachable branch (`:195-203`) |
| `ProxyServer.Timeout`, `testSOCKS5Connection(timeoutSec)` (`cmd/tcp2tor/main.go:551,503`) | | | yes | yes | | OPS-4 |
| `main.getTableName` fallback (`cmd/import-flat-files/main.go:146-148`) | | yes | | | | unreachable: hash length ≥ 32 is checked first (`:373`) |
| `main.processOverviewFiles` (`cmd/merge-active/main.go:556`) | | | | | yes | hardcoded host path; aborts on first miss (A-tools-18) |
| `cmd/extract_hierarchies` `main` | | | | | yes | missing input paths (A-tools-18) |
| `cmd/test-nntp` comment block (`:79-125`) | | yes | | | | removed APIs (CLI-6) |

## Seed items rejected

None. The `test-nntp` half of CLI-6 is CONFIRMED (low). The tcp2tor part of OPS-4 is CONFIRMED with a repro.

Focus items checked and found not to be defects:
- **Path traversal from group names into the data dir: rejected.** Group DB paths go through
  `SanitizeGroupName` (`[^a-zA-Z0-9_-]` → `_`, `internal/database/utils.go:108-127`) and a hash directory
  (`db_groupdbs.go:223-227`). Analyze cache files are named by sha256 (`analyze.go:485-491`). The only unsanitized
  name-to-path sink in scope is the analyze export (A-tools-11), which is under `./out`, not the data dir.
- **SQL injection in rslight-importer: rejected.** Legacy queries are constant strings
  (`rslight_articles.go:23-27,67-71`), and section/group inserts are parameterized (`rslight.go:298-303`).
- **Fetcher remote LIST data: no defect in scope.** Names are filtered with `common.IsValidGroupName` before insert
  (`cmd/nntp-fetcher/main.go:934-938`), and the fetch list is intersected with the remote list (`:295-306`). Remote
  message-id syntax is A-intake-3.

## Not covered / follow-ups

- No live fetch was run (as required). A-tools-1 and A-tools-4 are measured in isolation. The graceful-shutdown
  half of A-tools-5 needs a run against a fake upstream with a stalled writer (the fake server from
  `audit-nntp-client` could be reused).
- The RockSolid Light spool format (CRLF vs LF, dot-stuffing) was not verified on real data (A-tools-15).
- The `import-flat-files` batch-drop path could not be reached at runtime, because the first DB open did not finish
  in 10 minutes on this host (A-tools-17).
- `cmd/web/main.go:226` also calls `NewProgressDB`, so A-tools-6 affects web startup. The web side belongs to the
  web/ops reports.
- `internal/nntp` client parsing against hostile servers (`XOver`, `ListGroups`, `XHdrStreamed`) is `audit-nntp-client`
  scope. Here it was read only as far as the tools depend on it.
- Wave-4 input for `audit-delta-fixes.md`: A-tools-1 and A-tools-2 (high). Candidates to bundle with them: A-tools-3,
  A-tools-5, A-tools-6, A-tools-14.
