# Audit audit-ops (base ae176eb)

Audit of the operator binaries, the destructive maintenance tools, the root and `scripts/` shell
scripts, the release workflow and the deploy/update path. NNTP server startup/TLS is `audit-nntp-sec`
(BUG-7, including the `log.Fatalf` that masks a TLS cert failure in `cmd/nntp-server/main.go:133-134`),
`nntp-transfer` and its pprof exposure are `audit-nntp-client` (OPS-3), the fetcher/importers/tcp2tor
are `audit-tools`, and the library sides of OPS-1 are `audit-db` (DB) and `audit-intake`
(processor/history). This report cites those instead of re-reporting them. It follows
`docs/audit/README.md` (D1) and K6. All `path:line` anchors are at ae176eb and were checked with
`grep -n`/`sed -n`.

## Scope and method

**Read in full:**
- `cmd/web/{main.go, main_functions.go, main_adapters.go}`, `cmd/recover-db/main.go`,
  `cmd/history-rebuild/{main.go, helpers.go}` (plus `helpers_test.go` names), `cmd/expire-news/main.go`
  (plus `main_test.go` names), `cmd/fix-references/main.go`, `cmd/fix-thread-activity/main.go`,
  `cmd/usermgr/main.go`, `cmd/nntpmgr/main.go`.
- `cmd/history-demo/` and `cmd/test-MsgIdItemCache/`: only `main.go.off` sources (not built) plus two
  tracked ELF binaries; inventoried, not audited line by line.
- Every root `*.sh` (48 files), `scripts/*.sh`, `getUpdate.sh.template`, `.github/workflows/release.yml`,
  `.gitignore`.
- `active_files/hierarchies/` (282 `.active` files, `organize_by_theme.{go,sh}`, `README.md`, `themed/`).
- `internal/nntp/nntp-peering-pattern_test.go` (file-writing hygiene only).
- `php/` (inventory: every top-level file, `includes/`, `php_examples/`).
- Callees needed for the traces: `internal/database/{db_nntp_users.go, db_user_profile.go, utils.go
  (RsyncDIR), queries.go (GetActiveNewsgroupByName, UpdateUserPassword, DeleteUser), database.go
  (Get/SetHistoryUseShortHashLen), db_init.go}`, `internal/config/config.go:546-548`,
  `internal/processor/{processor.go (SetHostname, validateHostname, NewProcessor), proc-utils.go
  (ParseNNTPDate)}`, `internal/history/{history.go, history_config.go}` (cgo symbols),
  `go-sqlite3@v1.14.32/static_mock.go`.
- `docs/audit/tools/*.txt` filtered to the scope: staticcheck SA4006/SA4010 (`cmd/recover-db/main.go:300,1108`),
  U1000 (`cmd/web/main.go:28` `isleep`); gosec G201/G202 on `cmd/expire-news/main.go:603,619` and
  `cmd/history-rebuild/{helpers.go:187, main.go:502}` (placeholder lists/table names built from
  constants, not user input: no finding), G108/G114 on `cmd/history-rebuild/main.go:12,100` (OPS-3,
  `audit-nntp-client`); errcheck noise in `active_files/hierarchies/organize_by_theme.go`.
  go vet and govulncheck have nothing in scope.

**Repros run** (scratch data only: `-data ./data-test-audit-ops`, port 21207; binaries built with the
per-tool scripts `build_nntpmgr.sh`, `build_recover-db.sh`, `build_fix-references.sh`,
`build_expire-news.sh`, `build_webserver.sh`; the scratch dir was deleted afterwards):
- `nntpmgr -create/-list/-update` (OPS-2, A-ops-9).
- `fix-references` startup (A-ops-7).
- `webserver` with the flags used by `run_web.sh`/`scripts.sh` and `-withnntp` (OPS-2, OPS-4);
  `go build cmd/web/main.go` (as `go_run_web.sh` does).
- `recover-db -parsedates -rewritedates` on a seeded scratch group (A-ops-5); `recover-db -db X -data Y`
  (A-ops-1); `recover-db -group <typo>` (A-ops-8).
- `expire-news -dry-run -respect-expiry` for a single group vs a wildcard (A-ops-6).
- `webserver -disable-active <descriptions file>` (A-ops-4).
- `webserver` started on the scratch root, then SIGTERM vs SIGINT (A-ops-3).
- Cross-compile of `cmd/expire-news` for `linux/arm64` as `release.yml` does (A-ops-2).
- `go build ./cmd/test-MsgIdItemCache`, `./cmd/history-demo` (A-ops-17); `nntpmgr -data ""` (OPS-4).

No script was executed. Release, update, rsync, recover, history-rebuild, delete_ALL_users and
manual_sqlite_migration scripts were read only.

## Summary

| Severity | Count | Ids |
|----|----|----|
| critical | 0 | |
| high | 1 | A-ops-1 |
| medium | 7 | A-ops-2, A-ops-3, A-ops-4, A-ops-5, A-ops-6, OPS-1 (ops side), OPS-4 |
| low | 11 | OPS-2, A-ops-7, A-ops-8, A-ops-9, A-ops-10, A-ops-11, A-ops-12, A-ops-13, A-ops-14, A-ops-15, A-ops-16 |
| info | 1 | A-ops-17 |

| Category | Count |
|----|----|
| bug | 6 (A-ops-1, A-ops-2, A-ops-5, A-ops-7, A-ops-9, OPS-2) |
| robustness | 8 (A-ops-3, A-ops-4, A-ops-8, A-ops-11, A-ops-12, A-ops-13, A-ops-14, OPS-1) |
| wrong-function | 1 (A-ops-6) |
| security | 3 (A-ops-10, A-ops-15, A-ops-16) |
| test-gap | 1 (OPS-4: CI) |
| dead-code | 1 (A-ops-17) |

Seeds: OPS-1 CONFIRMED (library fatal sites and re-open globals cited from `audit-db`/`audit-intake`;
the binaries' own exits added here; the shutdown-retry half is verified FIXED). OPS-2 CONFIRMED (all
three parts, with repros). OPS-4 CONFIRMED except one sub-claim: the `findOrphanedDatabases` `"data"`
fallback is unreachable (see "Seed items rejected").

**The 5 most important findings:**
1. **A-ops-1 (high)**: `recover-db` has two directory flags. `-db` (documented as "required", default
   `data`) is only checked with `os.Stat` and printed as "Data Path"; the database actually opened is
   `-data` (default `./data`). Following the usage text (`-db /path/to/databases -repair`) runs the
   repair, `-rebuild-threads` or `-rewritedates` against `./data`.
2. **A-ops-3 (medium)**: `cmd/web` (and `expire-news`, `history-rebuild`, the fetcher, `nntp-transfer`,
   `post-queue`) only handle `os.Interrupt`. `kill`, systemd and `docker stop` send SIGTERM, which ends
   the web server immediately (exit 143) with no web/NNTP stop, no processor/history close, no batch
   drain and no DB shutdown.
3. **A-ops-4 (medium)**: `webserver -disable-active <file>` disables every newsgroup not in the file. A
   file with no 4-field lines (a descriptions file, an empty or truncated download) gives an empty keep
   set, and every group is disabled without a prompt, dry-run or sanity threshold.
4. **A-ops-5 (medium)**: `recover-db -parsedates -rewritedates` clears `hide` and `spam` on every
   rewritten article and writes the parser's fallback date for unparseable `Date:` strings (the
   `IsZero` branch falls through because its `continue` is commented out).
5. **A-ops-2 (medium)**: the release workflow can't produce a release. 4 of the 5 matrix jobs
   (linux/arm64, darwin/amd64, darwin/arm64, windows/amd64) cross-compile with `CGO_ENABLED=0`, and `internal/history` uses cgo-only `go-sqlite3` symbols, so
   those jobs fail to compile; the default `fail-fast` cancels the rest and the `release` job never runs.

## Findings

### A-ops-1: recover-db opens `-data` while checking and printing `-db`

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-1 | high | bug | `cmd/recover-db/main.go:36,44,50-57,69-72,80,126` | CONFIRMED |

**Scenario:** an operator runs `recover-db -db /srv/pugleaf/data-copy -group '$all' -repair` (or
`-rebuild-threads`, `-parsedates -rewritedates`, `-reorder-by-datesent`), as the usage text says. The
tool checks that `/srv/pugleaf/data-copy` exists, prints it as "Data Path", and then opens `./data`
relative to the cwd (the `-data` default) and repairs or rewrites that. In the production checkout
`./data` is the live data. None of the destructive modes asks for confirmation.

**Evidence:**
- `:36` `dbPath = flag.String("db", "data", "Data Path to main data directory (required)")`;
  `:44` `dataDir = flag.String("data", "./data", ...)`.
- `:50-57` the usage message and example name only `-db`; `:69-72` stat `*dbPath`; `:80`
  `dbConfig.DataDir = *dataDir`; `:126` prints `*dbPath` as "Data Path". `*dbPath` is used nowhere else.
- Repro (scratch roots only; `-data` given explicitly so nothing could reach `./data`):
  ```
  $ ./build/recover-db -data ./data-test-audit-ops -db ./data-test-audit-ops-elsewhere -group audit.ops.dates
  📂 Data Path: ./data-test-audit-ops-elsewhere
  📊 Newsgroups:    1          <- checked the group of ./data-test-audit-ops
  ```

**Fix (S):** drop `-db` (or make it an alias that sets `DataDir`), make `-data` required for every
write mode, print the path actually opened, and ask for confirmation (or require `-force`) for
`-repair`, `-rebuild-threads`, `-rewritedates` and `-reorder-by-datesent`.

---

### A-ops-2: release.yml cross-compiles with CGO disabled; 4 of the 5 matrix jobs fail and no release is created

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-2 | medium | bug | `.github/workflows/release.yml:19-26,58-66,106-116,161-163`; `internal/history/history.go:448,450`, `internal/history/history_config.go:223` | CONFIRMED |

**Scenario:** a `v*` tag or a dispatch starts the matrix: 3 OS × 2 arch minus windows/arm64 = 5 jobs on
`ubuntu-latest`. Only `linux/amd64` is a native build; the other 4 (linux/arm64, darwin/amd64,
darwin/arm64, windows/amd64) cross-compile. For those the Go toolchain disables cgo
by default when cross-compiling, so `mattn/go-sqlite3` compiles its `!cgo` stub (`static_mock.go`),
which lacks `sqlite3.Error`, `ErrBusy`, `ErrLocked` and `SQLiteConn.Exec`. `internal/history` uses all
of them, so the build step exits 1. No `fail-fast: false` is set, so the remaining jobs are cancelled,
and the `release` job (`needs: build`, `:163`) is skipped. Even without the history package, every
cross-built binary would only contain the stub driver ("Binary was compiled with 'CGO_ENABLED=0',
go-sqlite3 requires cgo to work") and fail at the first `sql.Open`.

**Evidence:**
```
$ GOOS=windows GOARCH=amd64 go env CGO_ENABLED   -> 0   (same for linux/arm64, darwin/arm64; native: 1)
$ GOOS=linux GOARCH=arm64 go build -ldflags "-X main.appVersion=0.0.0-audit" ./cmd/expire-news
internal/history/history.go:448:24: undefined: sqlite3.Error
internal/history/history.go:450:36: undefined: sqlite3.ErrBusy
internal/history/history.go:450:73: undefined: sqlite3.ErrLocked
internal/history/history_config.go:223:21: conn.Exec undefined (type *sqlite3.SQLiteConn has no field or method Exec)
```
The workflow also has no `go vet`/`go test` step (OPS-4).

**Fix (M):** build each target on its own runner (`ubuntu-24.04-arm`, `macos-*`, `windows-*`) with
`CGO_ENABLED=1`, or use a cgo cross toolchain (zig cc / xgo), or drop the targets that aren't supported.
Add `fail-fast: false` so one platform can't block the others, and add a vet/test job.

---

### A-ops-3: SIGTERM ends cmd/web without any shutdown

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-3 | medium | robustness | `cmd/web/main.go:486-487,505-566`; same pattern `cmd/expire-news/main.go:146`, `cmd/history-rebuild/main.go:161`, `cmd/nntp-fetcher/main.go:147`, `cmd/nntp-transfer/main.go:347`, `cmd/post-queue/main.go:74` | CONFIRMED |

**Scenario:** the web server is stopped with `kill <pid>`, by systemd, by `docker stop` or by a
supervisor. All of them send SIGTERM. `signal.Notify(sigChan, os.Interrupt)` (`:487`) does not register
SIGTERM, so Go's default action terminates the process at once: no `server.Shutdown`, no NNTP
`Stop`, no `postQueueWorker.Stop`, no `proc.Close()` (history flush), no `close(db.StopChan)` /
`db.WG.Wait()` / `db.Shutdown()` (`:514-566`). Queued batch writes (up to `BatchInterval`) and queued
history adds are lost. Only `cmd/nntp-server` (`main.go:141`) and `cmd/tcp2tor` register SIGTERM.

**Evidence** (scratch root, port 21207):
```
SIGTERM: log ends at "[WEB]: Hierarchy updater started ..."; exit=143; 0 lines matching
         "Received shutdown signal|Graceful shutdown completed|All databases closed"
SIGINT:  "[WEB]: Received shutdown signal, initiating graceful shutdown..."
         "[WEB]: Graceful shutdown completed"; exit=0
```

**Fix (S):** `signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)` in every long-running binary
(the batch-style tools treat it like Ctrl+C). Optionally a second signal forces exit.

---

### A-ops-4: `-disable-active` disables every group when the file has no active-format lines

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-4 | medium | robustness | `cmd/web/main.go:306-315`, `cmd/web/main_functions.go:1015-1099` (`:1040-1044`, `:1071-1079`) | CONFIRMED |

**Scenario:** the operator passes the wrong file (a `newsgroups` descriptions file with
`name<TAB>description`, the `-update-newsgroups-expiry-from-file` format, an empty or truncated
download). Every line has fewer than 4 fields, so it is skipped as "malformed" (`:1041-1044`), the keep
set is empty, and the loop disables every active group (`:1071-1079`). Nothing checks for an empty keep
set, nothing asks for confirmation, and there is no dry-run. Disabled groups vanish from listings and
fetching. They can be re-enabled in bulk (admin `POST /admin/newsgroups/bulk-enable`,
`internal/web/web_admin_newsgroups.go:429`, `BulkUpdateNewsgroupActive`), but nothing records which
groups were already inactive before the run, so the previous state can't be restored exactly.

**Evidence** (scratch root with 3 active groups, a 3-line descriptions file):
```
[WEB]: Warning: Skipping malformed line 1 in active file: audit.ops.dates	Dates group
[WEB]: Warning: Skipping malformed line 2 ... / line 3 ...
[WEB]: Found 0 active groups in file
[WEB]:   - Groups kept active (in active file): 0
[WEB]:   - Groups newly disabled (not in active file): 3
$ sqlite3 .../cfg/pugleaf.sq3 "select name,active from newsgroups"
audit.ops.dates|0
audit.ops.b|0
audit.ops.c|0
```

**Fix (S):** refuse when the keep set is empty or when more than N% of the active groups would be
disabled (unless `-force`), accept 1-field lines (a plain group list), and add `-dry-run` that prints
the groups instead of updating them.

---

### A-ops-5: recover-db `-rewritedates` un-hides spam and writes the fallback date for unparseable strings

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-5 | medium | bug | `cmd/recover-db/main.go:819-845,849-858,874-883` (`:877`); `internal/processor/proc-utils.go:502` | CONFIRMED |

**Scenario:**
- Every article whose re-parsed date differs by more than 1 s gets
  `UPDATE articles SET date_sent = ?, hide = 0, spam = 0` (`:877`). Articles hidden by moderation, by
  user spam flags or by `-update-newsgroups-hide-futureposts` (which sets `hide=1` and increments
  `spam`, `cmd/web/main_functions.go:214,225`) become visible again. The main-DB `spam` table rows
  stay, so the admin spam view and the group DB disagree.
- When `ParseNNTPDate` fails, the branch at `:821` records a `ParseFailed` problem, but its `continue`
  sits inside the comment block (`:837-844`). Execution falls through to the diff (`:849-858`), and the
  rewrite stores whatever the parser returned: `0001-01-01 00:00:00` for a zero time, or the parser's
  fallback date (INT-3) for strings it "parses" by fallback. A correct stored `date_sent` is replaced by
  a sentinel.

**Evidence** (scratch group, two articles with `spam>0, hide=1`, `date_sent='2020-01-01 00:00:00'`):
```
$ ./build/recover-db -data ./data-test-audit-ops -db ./data-test-audit-ops -group audit.ops.dates -parsedates -rewritedates
🔧 Article 1 (<a1@audit.invalid>): 'Wed, 01 Jan 2020 10:00:00 +0000' stored as 2020-01-01T00:00:00Z, should be 2020-01-01T10:00:00Z
🔧 Article 2 (<a2@audit.invalid>): 'not a date at all' stored as 2020-01-01T00:00:00Z, should be 1990-01-01T12:00:00Z
before: 1|2020-01-01 00:00:00|1|1   2|2020-01-01 00:00:00|2|1    (article_num|date_sent|spam|hide)
after:  1|2020-01-01 10:00:00|0|0   2|1990-01-01 12:00:00|0|0
```
`scripts.sh:5,9` runs exactly this command (`./recover-db -parsedates -rewritedates`) on the default
`./data`, for all groups.

**Fix (S):** update only `date_sent`; skip (and report) articles whose date string doesn't parse or
parses to a fallback; restore the `continue` after the `ParseFailed` branch.

---

### A-ops-6: expire-news `-respect-expiry` and `-prune` silently do nothing for a single group

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-6 | medium | wrong-function | `cmd/expire-news/main.go:386-398,209,245`; `internal/database/queries.go:1006-1015` | CONFIRMED |

**Scenario:** `expire-news -group comp.lang.go -respect-expiry -force` (or `-prune`). For a specific
group the tool builds `&models.Newsgroup{Name, ExpiryDays: nga.ExpiryDays}` (`:395-398`) from
`GetActiveNewsgroupByName`, which selects only `id, name, active` (`queries.go:1006,1011`). So
`ExpiryDays` and `MaxArticles` are always 0: the expiry block (`:209`) and the prune block (`:245`) are
skipped and the run ends with "No operations performed". The `$all` and wildcard paths use
`MainDBGetAllNewsgroups` and work. A cron job expiring one group never expires anything and reports
success.

**Evidence** (scratch group with `expiry_days=7` and two articles from 2020):
```
$ ./build/expire-news -data ./data-test-audit-ops -group audit.ops.dates -respect-expiry -dry-run
Would process 0 articles total (scanned 0 articles)
$ ./build/expire-news -data ./data-test-audit-ops -group 'audit.*' -respect-expiry -dry-run
[1/1] Processing audit.ops.dates expiry (using group setting: 7 days)
Would process audit.ops.dates: expired 2 (age) (scanned 2)
```

**Fix (S):** use `MainDBGetNewsgroup(name)` (then check `Active`) for the single-group path, or extend
the query to `expiry_days, max_articles`. A test next to `main_test.go` covers both paths.

---

### OPS-1 (ops side): fatal exits in library code, re-open globals, and the binaries' own exit paths

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| OPS-1 | OPS-1 | medium | robustness | library: see `audit-db` OPS-1, `audit-intake` OPS-1; binaries: `cmd/web/main.go:228,240,248-338,344,350,425,509`, `cmd/web/main_functions.go:510-541,545-577,583`, `cmd/recover-db/main.go:86,169,186,197,208,305,671,674`, `cmd/nntpmgr/main.go:79-117`, `cmd/usermgr/main.go:63-101`, `cmd/fix-thread-activity/main.go:30-41`, `cmd/fix-references/main.go:101,111` | CONFIRMED |

**Scenario:** library code and the binaries exit the process or skip the documented teardown, and some
globals and goroutines outlive a `Database`.

**Evidence:**

**Library side (cited, not re-traced):**
- `audit-db` OPS-1: `log.Fatal` in `db_init.go:141,206,212,224,229`; `INIT` set before anything can
  fail and never reset; seven package goroutines without a stop path; `BatchDividerChan`,
  `migratedDBsCache` (A-db-6) as process globals.
- `audit-intake` OPS-1: `processor.NewProcessor` fatal on hostname/DNS/history init
  (`processor.go:63,69,88`), `history.executeDBTransaction` `os.Exit(1)`, `beginTx` fatal after 100
  busy retries, `MsgIdItemCache` cleanup goroutine without stop, `config.NewDefaultConfig` fatal
  (`config.go:548`).
- The "batch retries forever after `Shutdown`" half is FIXED: `batchShutdownGrace`
  (`db_batch.go:582`), `retryShutdownGrace` (`:584-`) and `IsDBshutdown()` bound every retry loop
  (verified by `audit-db`; the constants and helpers exist at ae176eb).

**Binary side (new detail in this scope):**
- `cmd/web` exits with `log.Fatalf`/`os.Exit` after `OpenDatabase` on every startup error
  (`:228,240,344,350,425,509`, `ConnectPools` `main_functions.go:583`) and after every one-shot
  maintenance mode (`:248-338`), always without `close(db.StopChan)`, `db.WG.Wait()` or
  `db.Shutdown()`. The traced maintenance modes write directly (e.g. `IncrementArticleSpam`,
  `database.go:367-400`, uses `RetryableExec`), not through the batch writer, so this loses nothing
  today (the preloader paths were not traced), but a
  `log.Fatalf` at `:509` (web listener failed, e.g. port in use) happens after the processor and the
  history writer are running.
- `startHierarchyUpdater` (`main_functions.go:510-541`) is an endless loop with `time.Sleep(10*time.Minute)`
  and no stop channel; its log line says "every 30 minutes" (`:524`). It can run
  `UpdateHierarchiesLastUpdated` against a closed DB during shutdown. `monitorUpdateFile` (`:545-577`)
  only returns when it finds `.update`.
- `recover-db` ends every mode with `os.Exit` (`:169,186,197,208`, `printSummary` `:671,674`, and the
  "DO NOT CHANGE" exit at `:305`), so the `defer db.Shutdown()` at `:86` never runs.
- `nntpmgr`, `usermgr`, `fix-thread-activity` and `fix-references` call `defer db.Shutdown()` without
  `close(StopChan)`/`WG.Wait()`, and their `log.Fatal` paths skip even that. `fix-thread-activity` also
  doesn't set `database.NO_CACHE_BOOT`, so a one-shot repair boots every cache (`db_init.go:178`).

**Fix (M):** library: as in `audit-db`/`audit-intake`. Binaries: one `shutdown()` helper per binary
(the `expire-news`/`history-rebuild` pattern: `close(StopChan)`, `WG.Wait()`, `Shutdown()`), called on
every exit path after `OpenDatabase`; tie `startHierarchyUpdater` and `monitorUpdateFile` to
`db.StopChan`; set `NO_CACHE_BOOT` in all CLI tools.

---

### OPS-2: dead `-withnntp`, nntpmgr prints hashes and plaintext, peering test writes into the cwd

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| OPS-2 | OPS-2 | low | bug | `cmd/web/main.go:31,105,113-116,150,428`; `cmd/nntpmgr/main.go:30,152-153,172-192`; `internal/nntp/nntp-peering-pattern_test.go:17-22,408-410,437-440,520-523,557-558,607-608`; `.gitignore:15,30` | CONFIRMED |

**Scenario / Evidence:**
- **Embedded NNTP server:** the `-withnntp` flag is commented out (`:105`), so `withnntp` stays false
  and `(nntptcpport > 0 || nntptlsport > 0) && withnntp` (`:428`) never holds. `-nntptcpport`,
  `-nntptlsport`, `-nntpcertfile` and `-nntpkeyfile` (`:113-116`) are parsed and logged (`:154`) but
  have no effect; the startup line always says `NNTP=false` (`:150`). Repro:
  `webserver -data ./data-test-audit-ops -withnntp` → `flag provided but not defined: -withnntp`, exit 2.
  The web admin's NNTP user changes therefore always reach a *separate* `nntp-server` process (the
  cross-process point behind SEC-6).
- **nntpmgr:** `-list` prints the stored bcrypt hash in the "Password" column; `%-16s` pads but
  never truncates, so the full hash is printed. `-create` echoes the password even when the operator
  supplied it. The password is also an argv flag (`:30`), visible in `ps` and shell history.
  ```
  ✅ NNTP user 'audituser02' created successfully
     Password: Secr3tPass98
  ID   Username         Password         MaxConns ...
  1    audituser01      $2a$10$T3XZdrCv.1Jls7S1HLqwOesSLt754NZQe1SaGMr9Gsbejy/pfvOF2 1 true true Never ...
  ```
- **Peering test:** `TestAllDefaultBinaryPatterns` writes `active.out` into the cwd (`:409-410`) when
  the hardcoded `/home/fed/WORKSPACES/.../ninja-nl.active` exists (`:17-22`; it doesn't on this host).
  `TestSearchUnmatchedForBinaryGroups` and `TestAnalyzeUnmatchedGroupsToFiles` only need a leftover
  `active.out` in the cwd (`:437-440`, `:520-523`) and then write `analysis_<keyword>.txt` /
  `analysis_pattern_<name>.txt` (`:557-558`, `:607-608`). So the tests depend on files from earlier
  runs. The outputs are gitignored (`*.out`, `internal/nntp/analysis_*`), so `git status` won't show
  them. The same data exists in the repo as `active_files/ninja-nl.active`.

**Fix (S):** delete the dead NNTP flags from `cmd/web` (or restore `-withnntp` deliberately); drop the
hash column from `-list`; print the password only when it was generated; read `-password` from a
prompt/env/file. Move the peering fixture to `testdata/` and write outputs to `t.TempDir()`.

---

### OPS-4: ignored flags, broken scripts, race-instrumented production builds, CI without tests

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| OPS-4 | OPS-4 | medium | test-gap | `cmd/history-rebuild/main.go:74,79`; `cmd/web/main.go:102,342-361`, `cmd/web/main_functions.go:656-657`, `internal/database/database.go:296-300`; `cmd/tcp2tor/main.go:102,177,252,503,551`; `run_web.sh:4`, `run_web_ssl.sh:3`, `scripts.sh:6-7,13-15`, `go_run_web.sh:1`, `run_test.sh:1`, `manual_sqlite_migration_0002.sh:6-7`, `manual_sqlite_migration_0008.sh:5`, `build_TestMsgIdItemCache.sh:3`; 11 `build_*.sh`; `.github/workflows/release.yml` | CONFIRMED (one sub-claim rejected) |

**Scenario:** flags that are parsed but have no effect, scripts that call flags or files that don't
exist, production builds with the race detector, and a CI that never runs the tests.

**Evidence:**

**Ignored flags:**
- `history-rebuild -useshorthashlen`: documented "No effect" (`:74`), discarded (`:79`). Honest, but
  `history-rebuild.sh:5` and `scripts.sh:2` still write `history_use_short_hash_len` into the config.
- `cmd/web -useshorthashlen` (`:102`) has the same non-effect (`history_config.go:126`: "legacy, unused
  by the message_id schema") with two side effects: on the first run a value outside 2..7 aborts startup
  (`SetHistoryUseShortHashLen` returns an error, `database.go:298-299`, → `log.Fatalf` at `:350`), and
  `NewFetchProcessor` passes the CLI value, not the stored one (`main_functions.go:656-657`), while the
  log says "Using database value" (`:357`).
- `tcp2tor -timeout` (`:102`): passed into `testSOCKS5Connection(..., timeoutSec, ...)` (`:503`), which
  never reads it, and stored in `Timeout` (`:177,252,551`), which nothing reads; the dialers are
  `proxy.SOCKS5(..., proxy.Direct)` (`:514,519,626,628`) with no deadline. (Tool scope: `audit-tools`.)

**Scripts that pass undefined flags or reference missing files** (all confirmed by running the binary
with the flag, or `ls`):
- `run_web.sh:4` and `run_web_ssl.sh:3`: `-update-descr` doesn't exist → `flag provided but not
  defined: -update-descr`, exit 2. Both loops restart the web server every 3 s forever, and it never
  starts.
- `scripts.sh:6-7`: `-update-newsgroup-activity` doesn't exist (the flag is
  `-update-newsgroups-activity`, an int); `scripts.sh:13-15` call `fix-references` (A-ops-7: can't start)
  and `-rebuild-threads` without `-dry-run=false` (no effect, dry-run defaults to true).
- `go_run_web.sh:1`: `go run -race cmd/web/main.go -port 11980` compiles only `main.go`
  (`undefined: hideFuturePosts`, `updateNewsgroupLastActivity`, `writeActiveFileFromDB`, ...), and
  `-port` is not a flag (`-webport`). It would also run against the default `./data`.
- `run_test.sh:1`: `examples/nntp_complete.go` doesn't exist (no `examples/` dir).
- `manual_sqlite_migration_0002.sh:6-7` and `_0008.sh:5`: `migrations/...sql` doesn't exist at the root
  (migrations live in `internal/database/migrations/` and are embedded); the 0002 script also points at
  the 0008 file while its skip check tests for the 0002 `spam` column.
- `build_TestMsgIdItemCache.sh:3`: `no Go files in .../cmd/test-MsgIdItemCache` (only `main.go.off`).
- `recover-db.sh:3` (commented): `-rewritedates` alone is rejected (`main.go:60-63`).

**Race builds:** 11 build scripts use `-race`: fix-references, fix-thread-activity, history-rebuild,
import_flat-files, merge-active, merge-descriptions, nntp-transfer, nntpmgr, post-queue, tcp2tor,
webserver. `build_ALL.sh`, which feeds `createUpdate.sh` (the self-hosted update tarball), includes
`build_webserver.sh`, `build_nntp-transfer.sh`, `build_post-queue.sh` and `build_tcp2tor.sh`, so the
deployed web server runs with the race detector (several times the memory and CPU, and a detected race
only prints by default). `release.yml` builds without `-race` (`:106`).

**CI:** the only workflow is `release.yml` (tags `v*` and dispatch). It runs `go mod download/verify`
and builds; there is no `go vet`, no `go test` (although `internal/{database,web,history,nntp,processor}`,
`cmd/expire-news` and `cmd/history-rebuild` have tests) and no push/PR trigger. Its cross builds fail
(A-ops-2).

**`-data` handling (WSH):** `cmd/web` honors `-data` (`main.go:214`). Every tool in scope defaults to
`./data` (see the flag table). The `findOrphanedDatabases` `"data"` fallback (`main_functions.go:825-827`)
is unreachable, see "Seed items rejected".

**Fix (S per item):** remove or wire the dead flags (tcp2tor: `net.Dialer{Timeout}` as the forward
dialer; web/history-rebuild: drop `-useshorthashlen` or keep it as a no-op without validation); fix the
scripts or delete them; build production binaries without `-race`; add a `tests.yml` with vet + test
(the plan's wave-3 optional CI).

---

### A-ops-7: fix-references can't start; its usage and dry-run text are wrong

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-7 | low | bug | `cmd/fix-references/main.go:62-91,66,74-80,178-181`; `internal/config/config.go:546-548`; `build_fix-references.sh:3` | CONFIRMED |

**Scenario / Evidence:**
- The package has no `appVersion` variable and never sets `config.AppVersion`, then calls
  `config.NewDefaultConfig()` (`:90`), which `log.Fatalf`s while the version is `"-unset-"`
  (`config.go:547-548`). `build_fix-references.sh` passes `-X main.appVersion=...`, which the linker
  silently ignores because the symbol doesn't exist. Every run dies:
  ```
  $ ./build/fix-references -data ./data-test-audit-ops -group x.y
  config.AppVersion is unset          (exit 1)
  ```
- The usage text and example use `-db` (`:75,79`), which isn't defined
  (`flag provided but not defined: -db`).
- `-dry-run` defaults to true (`:66`), yet the summary says "Run without -dry-run to apply fixes"
  (`:180`); the only way to write is `-dry-run=false`.
- Not in the release (`release.yml:77` comment), but `scripts.sh:13-14` uses it.
- Related: any binary built without `-X main.appVersion` (`go run`, `build_usermgr.sh`) that calls
  `NewDefaultConfig` dies the same way (`recover-db`, `cmd/web`, `rslight-importer`, `nntp-server`).

**Fix (S):** add `var appVersion = "-unset-"` and `config.AppVersion = appVersion`; fix the usage text
and the dry-run hint.

---

### A-ops-8: maintenance tools create group databases for unknown `-group` names

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-8 | low | robustness | `cmd/recover-db/main.go:89-105,718,363`; `cmd/fix-references/main.go:104-107,132`; `cmd/fix-thread-activity/main.go:32-35,63` | CONFIRMED (recover-db repro; the other two by code) |

**Scenario:** a typo in `-group` (or a comma list entry) is wrapped in `&models.Newsgroup{Name: ...}`
without a main-DB lookup, and the first `GetGroupDB` creates the directory, the SQLite file and the
schema (SEC-2 DB side, `audit-db`). The orphan then shows up in `-find-orphan-dbs`.
```
$ ./build/recover-db -data ./data-test-audit-ops -db ./data-test-audit-ops -group audit.ops.tpyo -parsedates
$ find ./data-test-audit-ops/db -name '*.db'
./data-test-audit-ops/db/63cb00284c65a845c01b3c7dada00877/audit_ops_dates.db
./data-test-audit-ops/db/f885723d472e5cac96da5524bd6f0f17/audit_ops_tpyo.db    <- not in newsgroups
```
**Fix (S):** look each name up with `MainDBGetNewsgroup` first and report unknown names (the same
existence check D4 prescribes for the NNTP handlers).

---

### A-ops-9: nntpmgr `-update` resets the flags it wasn't given; inactive users are unreachable

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-9 | low | bug | `cmd/nntpmgr/main.go:31-32,109-116,226-249,128-130,133-136`; `internal/database/db_nntp_users.go:32,157-161` | CONFIRMED |

**Scenario / Evidence:**
- `-update` writes both `maxconns` and `posting` from the flag values, whose defaults are 1 and false.
  `-update -username u -maxconns 5` therefore revokes posting, and `-update -username u -posting`
  resets maxconns to 1. Unlike `-create` (`:128-130`), `-update` has no range check, so 0 or 1000 is
  stored.
  ```
  -update -username audituser01 -maxconns 5   -> Max connections: 5, Posting allowed: false (was true)
  -update -username audituser02 -maxconns 0   -> stored maxconns=0
  ```
- `GetNNTPUserByUsername` filters `is_active = 1` (`db_nntp_users.go:32`). An inactive user can't be
  updated or deleted ("not found"), and `-create` with the same name passes the existence check
  (`:133-136`) and then fails on the UNIQUE constraint. There is no `-activate` and no password change.

**Fix (S):** only change fields whose flags were set (`flag.Visit`), validate ranges, look users up
without the `is_active` filter in the admin tool, add `-activate/-deactivate/-passwd`.

---

### A-ops-10: usermgr `-update` keeps existing sessions; weaker password rule than the web

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-10 | low | security | `cmd/usermgr/main.go:136-138,279-292,300-305`; `internal/database/queries.go:816-818`, `internal/database/db_user_profile.go:25-54`; `internal/web/web_auth.go:294-300` | CONFIRMED (code trace) |

**Scenario:** an admin resets a compromised account with `usermgr -update -username u`. Only
`password_hash` is updated (`UpdateUserPassword` → `UpdateUserProfile`); the `sessions` rows stay, so an
attacker's existing web session keeps working (the NNTP auth cache is a separate store, SEC-6). The CLI
accepts 6-character passwords (`:136`, `:279`) while web registration requires 12 to 72
(`web_auth.go:295-300`). `truncate` (`:300-305`) slices bytes and can split a UTF-8 rune in `-list`.

**Fix (S):** delete the user's sessions (and invalidate the NNTP auth for linked `nntp_users`) on a
password update; reuse the web's `validatePassword` rule; truncate by runes.

---

### A-ops-11: `-rsync-inactive-groups` moves live SQLite files; paths reach rsync unguarded

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-11 | low | robustness | `cmd/web/main.go:126-127,282-291`, `cmd/web/main_functions.go:317-377` (`:337,345,360`); `internal/database/utils.go:35-43` | CONFIRMED (argument handling); PLAUSIBLE (concurrent writer) |

**Scenario:**
- With `-rsync-remove-source`, the group directory (`.db`, `-wal`, `-shm`) is copied and then deleted
  while other processes (fetcher, nntp-server, another web) may hold the group open. Nothing checks for
  that, nothing checkpoints the WAL first, and there is no confirmation. A later `GetGroupDB` for the
  moved group (any caller without an existence check, SEC-2) recreates an empty DB in the old place.
  (PLAUSIBLE: needs a concurrent writer during the copy; not reproduced.)
- `RsyncDIR` passes `oldpath` and `newpath` as plain argv elements without `--` (`utils.go:37`). A
  `-rsync-inactive-groups` value starting with `-` becomes an rsync option, and one with `host:` before
  the first `/` becomes an ssh remote. There is no shell (`exec.Command`), and the value comes from the
  operator's own command line, so this is a foot-gun, not a privilege boundary. LO:A4 (source path) and
  FU:A3 (`rows.Err()`) are already fixed.

**Fix (S):** refuse when the newsgroup is open elsewhere (or run only with the web/fetcher stopped,
documented and checked via a lock file), `PRAGMA wal_checkpoint(TRUNCATE)` before copying, insert
`--` before the paths and prefix relative paths with `./`, and require `-force` for `-rsync-remove-source`.

---

### A-ops-12: web maintenance modes need a resolvable hostname and a full boot

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-12 | low | robustness | `cmd/web/main.go:149,163-165,219-241,243-339`; `internal/processor/processor.go:302-340,381` | CONFIRMED (code trace; the A-ops-4 repro needed `-nntphostname example.org` to resolve) |

**Scenario:** `-write-active-file`, `-find-orphan-dbs`, `-compare-active`, `-disable-active`,
`-update-newsgroups-expiry-from-file` and the other one-shot modes run only after
`config.NewDefaultConfig`, `OpenDatabase` (batch workers, and every cache because cmd/web never sets
`NO_CACHE_BOOT`) and `processor.SetHostname` (`:239`), which validates the hostname with a forward DNS
lookup (`processor.go:381`). An offline or DNS-less maintenance run (e.g. writing an active file from a
backup) fails with "DNS resolution failed" before it starts; `localhost` and IPs are rejected outright.

**Fix (S):** dispatch the DB-only maintenance modes right after `OpenDatabase` (with `NO_CACHE_BOOT`)
and before `SetHostname`; better, move them into a separate admin binary.

---

### A-ops-13: destructive root scripts without guards

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-13 | low | robustness | `history-rebuild.sh:2,4-6`, `scripts.sh:1-3`, `delete_ALL_users.sh:1-2`, `scripts/disable_BadBots.sh`, `scripts/disable_BadIPs.sh`, `import_sections.sh:1`, `run_rslight_importer.sh:1` | CONFIRMED (read only) |

**Scenario / Evidence:**
- `history-rebuild.sh` is executable and named like the binary. Its guard line is commented out (`:2`),
  so it runs `rm -rfv data/history` (`:4`) and then `./history-rebuild` from the cwd (`:6`), while the
  build script writes `build/history-rebuild`. If the binary isn't in the cwd, the index is gone and
  nothing rebuilds it.
- `scripts.sh` is a notes file, but its first line is `rm data/history -r` (`:1`). `sh scripts.sh` or
  `bash scripts.sh` deletes the history index, then runs the A-ops-5 date rewrite on all groups (`:5`).
- `delete_ALL_users.sh` (no shebang, not executable): `DELETE FROM users` plus a sequence reset on the
  hardcoded `data/cfg/pugleaf.sq3`, without a prompt. The `sqlite3` CLI starts with
  `PRAGMA foreign_keys=0` (WSH enables foreign keys per Go connection only), so the `DELETE` succeeds
  and leaves orphaned `sessions` rows and `nntp_users.web_user_id` values pointing at deleted users.
- Every script hardcodes `data/` (or `-data data`) relative to the cwd; none accepts a data root.
  `import_sections.sh`/`run_rslight_importer.sh` also hardcode `/var/www/html/rocksolidbbs.com/...`.

**Fix (S):** delete the notes scripts or make them non-executable `.txt` files; give the destructive
ones a `DATA` argument with the `./data-test-*`-style guard used by `scripts/test-web-*.sh`, an
explicit `--yes`, and `set -eu`.

---

### A-ops-14: update pipeline: integrity holds, but the trigger, the error handling and the copy step are fragile

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-14 | low | robustness | `getUpdate.sh.template:1-11`, `copynew.sh:2-3`, `createUpdate.sh:10-13`, `rsync.sh:2-19`, `bump.sh:10-13`, `cmd/web/main_functions.go:545-577`, `run_web.sh:3` | CONFIRMED (read only), PLAUSIBLE (`sed` URL) |

**Scenario:** an update moves from the build host to a server as described below; the integrity check
holds, but several steps fail silently or interact with a running web server.

**Evidence:**

**How an update flows** (read only): `createUpdate.sh` tars `build/` into `update.tar.gz` and writes
`sha256sum update.tar.gz > .update` (`:13`). `rsync.sh` uploads the tarball to the CDN and, per server,
rsyncs `web preload .update getUpdate.sh` over SSH (`:17`). On the server, `cmd/web` sees `./.update`
(cwd, checked every 60 s), renames it to `.update.todo` and shuts down. `run_web.sh` loops into
`copynew.sh` → `getUpdate.sh`: `wget` the tarball into `tmp/`, `sha256sum -c ../.update.todo.downloading`,
extract, `mv * ../new/`, then `cp -v new/* .`.

**Integrity:** the tarball is only extracted after its SHA-256 matches a hash that came over SSH, so a
tampered CDN copy or an http URL can't inject binaries. The per-binary `checksums.sha256` inside the
tarball is never checked (redundant with the tarball hash).

**Fragile parts:**
- `.update` is both the release hash file and the shutdown trigger, relative to the cwd. Running
  `createUpdate.sh`/`bump.sh` in a checkout where a web server runs from the same directory shuts that
  server down within 60 s (the WSH/LO test scripts refuse to run when `./.update` exists for this
  reason). `.update` is gitignored (`.gitignore:44`), but `.update.todo*` isn't.
- `bash -e` with a `&&` chain (`getUpdate.sh.template:8-10`): a failed checksum or extraction does not
  abort, `rm -v ../.update.todo.downloading` (`:11`) consumes the trigger, and the script exits 0. When
  `wget` fails, `cd tmp` never ran, `:11` fails, and the trigger stays renamed to `.downloading`, so the
  next run reports "update file not found".
- `new/` is never created (`mkdir -p tmp` only) and never emptied. `copynew.sh` runs `cp -v new/* .`
  unconditionally after a failed `getUpdate.sh`, so every restart re-copies the last downloaded files
  (including `update.tar.gz`) over the cwd, silently reverting a hand-placed binary.
- `rsync.sh:13` substitutes the URL with `sed "s/XXXURLXXX/$UPDATEURL/g"`. A URL contains `/`, which
  ends the `s` expression ("unknown option to `s'") unless the config file pre-escapes it; `&` would be
  replaced by the match. (PLAUSIBLE: depends on `../config/go-pugleaf_cdn-url.txt`, not in the repo.)
- `rsync.sh:15,17` starts the per-server rsyncs in the background with output discarded and no `wait`,
  so `bump.sh` prints the hash and date while transfers are still running or have failed.

**Fix (S):** use a dedicated trigger file under the data root (not the hash file in the cwd); in
`getUpdate.sh` use `set -euo pipefail` with explicit `if` checks, create and clear `new/`, and only
copy after a verified extract; use `sed "s|XXXURLXXX|$UPDATEURL|g"` (or envsubst); `wait` for the
rsyncs and report failures.

---

### A-ops-15: release workflow: dispatch input interpolated into shell, tag-pinned actions, old toolchain

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-15 | low | security | `.github/workflows/release.yml:9-13,35,46-47,52,165-166,176,220-222` | CONFIRMED (injection pattern); PLAUSIBLE (stdlib CVEs in release binaries) |

**Scenario / Evidence:**
- `workflow_dispatch` input `version` is expanded by the Actions template engine into the shell script
  (`VERSION="${{ github.event.inputs.version }}"`, `:47`, `:176`) and into `appVersion.txt` (`:52`) and
  the release tag (`:222`). A value such as `1.0"; curl ... | sh; "` runs in the job; the release job
  has `contents: write` (`:165-166`). Only users with write access can dispatch, so the impact is
  limited.
- Third-party actions are pinned by mutable tags (`actions/*@v4/v5`, `softprops/action-gh-release@v2`,
  `:220`), not by commit SHA, in a job with write permission.
- `go-version: '1.25.3'` (`:35`) pins an older toolchain than the go1.27.1 the audit used, and
  govulncheck already lists stdlib vulnerabilities fixed after go1.27.1 (`docs/audit/tools/govulncheck.txt`).
  Not verified against 1.25.3 specifically.

**Fix (S):** pass the input via `env: VERSION: ${{ inputs.version }}` and validate it against
`^[0-9]+\.[0-9]+\.[0-9]+$`; pin actions by SHA; use `go-version-file: go.mod` plus a `toolchain` line,
or `stable`.

---

### A-ops-16: php/: an unserved frontend whose debug pages get deployed

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-16 | low | security | `php/debug.php:13,18-19,182-187`, `php/{api-debug,api-test,controller-debug,group-debug,nginx-debug,route-debug,router-debug,ssl-debug,template-debug,threads-debug,url-debug,test}.php`, `php/php_examples/setup-nginx-ssl.sh:54`, `php/php_examples/deploy-setup.sh:58`, `php/config.inc.php:9-13`, `php/.htaccess:4-8,11,14-17` | PLAUSIBLE (deployment-dependent) |

**Scenario:** `php/` is deployed with the setup script, including its unauthenticated debug pages.

**Evidence:**

**Inventory:**
- **Purpose:** an optional PHP frontend (router, controllers, templates) that renders pages from the
  go-pugleaf JSON API (`PUGLEAF_API_BASE`). `deserialize-rslight-data-db-to-array-to-json.php` is a
  CLI-only converter (refuses non-CLI SAPI). `php/php/` is an older copy of the client/router.
- **Served by:** nothing in the Go code (no `.php`, FastCGI or `php/` reference in `cmd/` or
  `internal/`). It is deployed separately via nginx/Apache examples in `php/php_examples/`.
- **Secrets:** none. `config.inc.php` is tracked, although `deploy-setup.sh:58` calls it "gitignored";
  it names the production API and web hosts (`reader-nyc.newsdeef.eu:11981`, `pugleaf.newsdeef.eu`)
  with `DEBUG_MODE` false.
- **Debug endpoints:** 13 standalone `*-debug.php`/`*-test.php`/`test.php` pages with no auth.
  `debug.php` prints server software, `DOCUMENT_ROOT`, loaded extensions and the config constants, and
  echoes `REQUEST_URI`/`QUERY_STRING` into HTML without escaping (`:182-187`), a reflected-XSS sink
  where the client doesn't percent-encode `<>`. `template-debug.php` runs `shell_exec("php -l '...'")`
  on fixed paths. `setup-nginx-ssl.sh:54` deploys them with `cp -r php/* ${WEBROOT}/`; `.htaccess`
  routes only non-existent paths to `index.php` (`.htaccess:4-8`), so existing debug files are served.

**Fix (S):** move the debug pages out of the webroot (or delete them), escape with
`htmlspecialchars`, untrack `config.inc.php` (keep `config.sample.php`), and exclude `*-debug.php`
in the deploy script.

---

### A-ops-17: stray artifacts in the module and the repo

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-ops-17 | info | dead-code | `cmd/test-MsgIdItemCache/{test-cache, test-MsgIdItemCache, main.go.off}`, `cmd/history-demo/main.go.off`, `active_files/hierarchies/organize_by_theme.go:1,91`, `active_files/hierarchies/organize_by_theme.sh:7`, `build_{analyze,expire-news,history-rebuild,usermgr,TestMsgIdItemCache}.sh` | CONFIRMED |

**Scenario:** files in the module and the repo that are stale, never built, or built without the
version stamp.

**Evidence:**
- Two 2.9 MB statically linked x86-64 ELF binaries are tracked in `cmd/test-MsgIdItemCache/`; the
  sources of that tool and of `history-demo` are `.off` files, so `go build` reports
  `no Go files` for both packages.
- `active_files/hierarchies/organize_by_theme.go` is `package main` inside the module
  (`go list` shows `.../active_files/hierarchies`), so `go build ./...`, `go vet ./...` and the linters
  compile it (all the errcheck hits in `golangci-lint.txt` outside `cmd/`/`internal/` come from it). It
  reads `../../migrations/0003_main_create_hierarchies.sql` (`:91`), which moved to
  `internal/database/migrations/`, and writes `themed/` relative to the cwd. The `.sh` twin defines the
  same stale `SQL_FILE` (`:7`) but never uses it.
- `build_analyze.sh`, `build_expire-news.sh`, `build_history-rebuild.sh`, `build_usermgr.sh` and
  `build_TestMsgIdItemCache.sh` don't pass `-X main.appVersion`, so those binaries log `-unset-`;
  `build_usermgr.sh` has no shebang.
- `rsync_nntp-transfer_TEST.sh:1` and `scripts/nntp-transfer-loop.sh:15,57-60` publish a deploy
  user@host and server IPs in the public repo.

**Fix (S):** untrack the binaries, delete or restore the `.off` tools, move `organize_by_theme.go`
under `cmd/` (or add a `//go:build ignore` tag), add version ldflags everywhere.

## Flag table

`used?`: **yes** = read and effective; **dead** = parsed, no effect; **broken** = has an effect, but a
wrong or dangerous one (see the finding). `./data?`: does the default make the tool read or write
under `./data` relative to the cwd. Every tool in scope defaults its data root to `./data`, so an
explicit `-data ./data-test-<slug>` is always needed for scratch runs.

| Tool | Flag | Used? | Default touches `./data`? | Note |
|----|----|----|----|----|
| web | `-data` | yes | **yes** (`./data`) | honored for main/group DBs, history, `progress.db` (WSH) |
| web | `-webport`, `-webssl`, `-websslcert`, `-websslkey` | yes | no | port must be 1024-65535 |
| web | `-nntphostname` | yes | no | DNS-validated; falls back to the DB value (A-ops-12) |
| web | `-history` | yes | via `-data` | |
| web | `-maxsanartcache(expiry)`, `-maxngpagecache(expiry)`, `-maxarticlecache(expiry)` | yes | no | |
| web | `-useshorthashlen` | dead/broken | writes a config row | legacy; aborts first start outside 2..7 (OPS-4) |
| web | `-nntptcpport`, `-nntptlsport`, `-nntpcertfile`, `-nntpkeyfile` | dead | no | gated by the removed `-withnntp` (OPS-2) |
| web | `-withnntp`, `-withfetch`, `-isleep`, `-ignore-initial-tiny-groups` | removed | n/a | commented out; `isleep` var unused (U1000) |
| web | `-edit-cronjobs`, `-no-cronjobs` | yes | no | WEB-1 for the create/toggle/stop gap |
| web | `-update-overwrite-newsgroups-descriptions` | yes | yes (DB) | reads `preload/newsgroups.descriptions` relative to the cwd, not `-data` |
| web | `-update-newsgroups-descriptions-file`, `-update-newsgroups-descriptions-import-create-newsgroups`, `-import-active` | yes | yes (DB writes) | `-import-active` exits after import |
| web | `-repair-watermarks` | yes | yes (DB writes) | one-shot, exits |
| web | `-update-newsgroups-activity N` | yes | yes (DB writes) | one-shot |
| web | `-update-newsgroups-hide-futureposts` | yes | yes (group DB + spam writes) | one-shot |
| web | `-write-active-file`, `-write-active-only` | yes | reads DB | `-write-active-only=false` writes only *inactive* groups |
| web | `-rsync-inactive-groups`, `-rsync-remove-source` | yes/broken | **moves group DBs out of `./data/db`** | no confirmation (A-ops-11) |
| web | `-compare-active`, `-compare-active-min-articles` | yes | reads DB | |
| web | `-disable-active` | broken | **disables groups in `./data`** | A-ops-4 |
| web | `-find-orphan-dbs`, `-verbose` | yes | reads `./data/db` | `"data"` fallback unreachable |
| web | `-update-newsgroups-expiry-from-file` | yes | yes (DB writes) | |
| recover-db | `-data` | yes | **yes** (`./data`) | the DB actually opened |
| recover-db | `-db` | broken | only `os.Stat("data")` | printed as "Data Path" (A-ops-1) |
| recover-db | `-group` | yes | yes | default `$all`; unknown names create DBs (A-ops-8) |
| recover-db | `-repair`, `-rebuild-threads`, `-max-par` | yes | **writes** | no confirmation |
| recover-db | `-parsedates`, `-rewritedates` | yes/broken | **writes** with `-rewritedates` | A-ops-5 |
| recover-db | `-scan-out-of-order-overview` | yes | reads | |
| recover-db | `-reorder-by-datesent` | yes | writes `<group>.db.new` beside each DB | |
| recover-db | `-v` | yes | no | defaults to true |
| history-rebuild | `-data` | yes | **yes** | default mode rewrites `./data/history` |
| history-rebuild | `-validate-only`, `-analyze-only` | yes | reads | |
| history-rebuild | `-trim` | yes | **removes history entries** | |
| history-rebuild | `-restart`, `-batch-size`, `-progress`, `-verbose` | yes | | |
| history-rebuild | `-useshorthashlen` | dead (documented) | no | |
| history-rebuild | `-pprof` | yes | no | any address, default mux (OPS-3) |
| expire-news | `-data` | yes | **yes** | |
| expire-news | `-group`, `-days`, `-batch-size` | yes | | |
| expire-news | `-force`, `-dry-run` | yes | **deletes** with `-force` | one of the two is required |
| expire-news | `-respect-expiry`, `-prune` | yes; dead for a single group | | A-ops-6 |
| expire-news | `-trim-history` | yes | removes history entries | |
| expire-news | `-help` | yes | no | |
| fix-references | all (`-data`, `-group`, `-v`, `-dry-run`, `-limit`, `-rebuild-threads`, `-batch-size`) | unreachable | **yes** (`./data`) | startup fatal (A-ops-7); `-dry-run` defaults to true |
| fix-thread-activity | `-data`, `-group` | yes | **yes** (writes `thread_cache`) | no dry-run; unknown group creates a DB |
| usermgr | `-data` | yes | **yes** | |
| usermgr | `-create`, `-list`, `-delete`, `-update`, `-username`, `-email`, `-display`, `-admin` | yes | writes main DB | delete asks for confirmation; update keeps sessions (A-ops-10) |
| nntpmgr | `-data` | yes | **yes** | |
| nntpmgr | `-create`, `-list`, `-delete`, `-update` | yes | writes main DB | delete asks; `-list` prints hashes (OPS-2) |
| nntpmgr | `-username`, `-password` | yes | | default `random` generates 12 hex chars; password on argv |
| nntpmgr | `-maxconns`, `-posting` | yes/broken | | `-update` resets them to defaults (A-ops-9) |
| history-demo, test-MsgIdItemCache | n/a | not built | n/a | only `main.go.off` |

**Hardcoded paths outside `-data`:** `cmd/web` watches `./.update` in the cwd
(`main_functions.go:546`) and reads `preload/newsgroups.descriptions` (`main.go:402`); `recover-db -db`
defaults to `data`; the root scripts hardcode `data/` (A-ops-13).

## Function inventory

| function/file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `cmd/web` `FetchRoutine` (`main_functions.go:662-821`) | | yes | | | | whole body is a comment |
| `cmd/web` vars `isleep`, `withnntp`, `withfetch` (`main.go:28,31,32`) | | yes | yes | | | flags commented out (`:105-107`); staticcheck U1000 for `isleep` |
| `cmd/web` NNTP server block (`main.go:427-473`) | | yes | | | | `&& withnntp` always false (OPS-2) |
| `cmd/web` `startHierarchyUpdater` (`main_functions.go:510-541`) | | | | yes | | logs "every 30 minutes", sleeps 10; no stop path |
| `cmd/web` `findOrphanedDatabases` fallback (`main_functions.go:825-827`) | | yes | | | | `-data ""` fails in `OpenDatabase` first |
| `cmd/web` `updateNewsGroupActivityValue` (`main_functions.go:107,115`) | | | | | yes | passes `nil, latestDate` as query args to a query without placeholders (go-sqlite3 ignores extras); `rows.Scan` error unchecked |
| `cmd/web` `disableNewsgroupsNotInActiveFile` | | | | | yes | A-ops-4 |
| `cmd/web` `NewFetchProcessor` (`main_functions.go:656-657`) | | | | yes | | passes the CLI `useShortHashLen`, not the stored one |
| `cmd/recover-db` `repairDatabase` repairs 2, 3, 5 (`main.go:422-447`) | yes | | | yes | | print "Skipping ... (unified schema)" but are counted in "Found N repairable issues" (`:372-383`) |
| `cmd/recover-db` `createMissingOverview*`, `updateMainDBStats` (`main.go:480-555`) | | yes | | | | commented out |
| `cmd/recover-db` `errorGroups` (`main.go:300`), `allProblems` (`:1108`) | | | yes | | | staticcheck SA4006, SA4010 |
| `cmd/recover-db` `checkGroupDates` (`main.go:761-905`) | | | | | yes | A-ops-5 |
| `cmd/recover-db` `-db` flag (`main.go:36`) | | | | yes | | A-ops-1 |
| `cmd/expire-news` `getNewsgroupsToExpire` single-group branch (`main.go:386-398`) | | | | | yes | A-ops-6 |
| `cmd/expire-news` `showUsageExamples` (`main.go:47`) | | | | yes | | "Always runs in dry-run mode first unless -force": it refuses without `-dry-run`/`-force` (`:113-115`) |
| `cmd/expire-news` `deleteArticles` overview delete (`main.go:609-615`) | | yes | | | | no `overview` table in the single-DB schema; logs a warning per chunk |
| `cmd/fix-references` (whole tool) | | yes | | yes | | fatal at startup (A-ops-7); `rebuildThreadsInNewsgroup` deletes `threads` in its own transaction before the rebuild batches (`main.go:337-349`) |
| `cmd/history-rebuild` `useShortHashLen` (`main.go:74,79`) | | | yes | | | documented no-op |
| `cmd/nntpmgr` `listNNTPUsers` (`main.go:160-196`) | | | | | yes | prints bcrypt hashes (OPS-2) |
| `cmd/nntpmgr` `updateNNTPUser` (`main.go:226-249`) | | | | | yes | A-ops-9 |
| `cmd/usermgr` `truncate` (`main.go:300-305`) | | | | | yes | byte slicing of UTF-8 |
| `cmd/tcp2tor` `testSOCKS5Connection(timeoutSec)`, `ProxyServer.Timeout` | | | yes | yes | | OPS-4 |
| `active_files/hierarchies/organize_by_theme.go` | | yes | | | yes | stale SQL path (A-ops-17) |
| `cmd/history-demo`, `cmd/test-MsgIdItemCache` | | yes | | | | `.off` sources, tracked binaries |
| `internal/nntp/nntp-peering-pattern_test.go` `parseActiveFile` | | | | | yes | hardcoded `/home/fed/...` path (OPS-2) |

## Seed items rejected

- **OPS-4, sub-claim "`findOrphanedDatabases` keeps a `"data"` fallback for an empty argument" (as a
  risk): REJECTED.** The fallback exists (`cmd/web/main_functions.go:825-827`) but can't be reached:
  `main` always passes `dataDir`, and with `-data ""` `OpenDatabase` fails first, because
  `filepath.Join("", "/cfg/pugleaf.sq3")` is absolute:
  `nntpmgr -data "" -list` → `failed to create data directory: mkdir /cfg/: permission denied`. It is
  dead code (inventory), not a path to `./data`, for a user that can't create `/cfg/`. Root never gets
  this far (`checkRootUser`, `internal/database/db_init.go:120-128,140-143`, `log.Fatal`s for euid < 1000),
  but a user with write access to `/` would get `/cfg/` created by `createDirIfNotExists`
  (`db_init.go:291`), and `-find-orphan-dbs` would then reach the fallback and scan `./data/db` against
  the `/cfg` main DB.
- No other seed row in scope is rejected. OPS-1, OPS-2 and the rest of OPS-4 are CONFIRMED; OPS-1's
  shutdown-retry half is confirmed FIXED.

## Not covered / follow-ups

- `history-rebuild` and `expire-news -trim-history` running at the same time as a live web/fetcher:
  both write the history SQLite files, and `history.beginTx` `log.Fatal`s after 100 busy retries
  (`audit-intake` OPS-1). Not measured. The trim logic itself is careful (lookup errors never remove
  entries; `resolveTrimGroup` returns non-`ErrNoRows` errors, including the A-db-1 NULL-hierarchy scan
  error, instead of treating the group as gone).
- `recover-db -reorder-by-datesent` on a group with an existing `.db.new` from an earlier run (insert
  into a non-empty destination) was not tested.
- `cmd/web` cron jobs (`sh -c`) are `audit-web` (WEB-1); `nntp-server`/`nntp-transfer`/fetcher signal and
  startup handling beyond the SIGTERM list are `audit-nntp-sec`/`audit-nntp-client`/`audit-tools`.
- Fix-plan routing (D4): OPS-1, OPS-2 (web/peering parts), OPS-4 and the A-ops findings go to
  `audit-delta-fixes.md`; OPS-2's nntpmgr part is in Appendix B's `nntp-auth-db` slice;
  OPS-3 stays with `nntp-client-transfer.md`. A-ops-2 and A-ops-15 fit together with the plan's
  wave-3 optional `tests.yml`.
