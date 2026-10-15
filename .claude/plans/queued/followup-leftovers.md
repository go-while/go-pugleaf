# Plan: followup-leftovers

Five parallel implementer slices, one wave, then the verifier. Written 2026-10-15 after all ten
audit/hardening plans were merged into `testing-001` (HEAD 15ef2a7).

## Context

Every earlier plan left small items behind in its Outcome, two follow-ups sit in `issues.md`, and `BUGS.md` still
lists audit ids as deferred. An inventory (two read-only agents, 2026-10-15) found about 90 open items; most are
small, several are already fixed but still documented as open (A-db-3/A-db-4), and a few need the user. The user
decided (2026-10-15): upgrade `go.mod` (toolchain, x/net, quic-go, drop the unused profiler module); skip the
nonce-based CSP for now; leave the fenced items deferred (history fail-stop exits Q3, the destructive root scripts
A-ops-13, the update pipeline A-ops-14, the `php/` debug pages A-ops-16). Up to five agents in parallel.

Goal: close the actionable leftovers in one wave of five disjoint slices (CI + toolchain, post-queue attempt
counter, web leftovers, ops-tool leftovers, NNTP/database leftovers incl. the fetcher commit barrier), fix the
stale docs inline, and leave the plan queue empty again. Everything stays behind the existing checks; no behaviour
change without a decision line below.

Out of scope (stay deferred, listed in the Outcome): Q3, A-ops-13/14/16, Q5 nonce CSP, cursor-mode paging, NEWNEWS
and the other "by design" NNTP commands, cancel/supersedes/Xref/NoCem, `-accept-unknown-groups`, nntp-transfer UI
auth and 431 requeue, webserver/fetcher memory growth, `PRAGMA optimize` scheduling, remediation of old web posts,
the "first user becomes admin" rule, T-nntp-article-1 (accepted).

## Design

### Contracts (names other slices rely on; owners may only ADD to them)
- `internal/nntp` exported client API used by postmgr and the tools: `NewPool`, `Pool.Get/Put`, `ClosePool`,
  `ForceCloseConn`, `PostArticle`, `GetArticle`, `XHdrStreamed`, `BackendConfig`: unchanged signatures.
- `internal/processor.DownloadArticles` / `DownloadArticlesFromDate`: unchanged signatures (callers in
  `cmd/nntp-fetcher/main.go` L598/L622/L637/L646 are owned by fl-ops-tools).
- Migration numbers: `0030_main_post_queue_attempts.sql` is fl-postqueue's; `0031_main_nntp_users_unique_web.sql`
  is fl-nntp-db's. No other migrations.
- Test files and new identifiers use the slice prefix (`fl_ci_`, `fl_pq_`, `fl_web_`, `fl_ops_`, `fl_nntpdb_`);
  nobody edits a package `testmain_test.go`; the test wait constants from plan test-timing-hardening
  (`hDeadline`, `cliWait`, `testWait`) are reused, never new literal deadlines under 30 s.

### D-A: post-queue attempt counter (fl-postqueue)
- Migration 0030: `ALTER TABLE post_queue ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`,
  `ADD COLUMN last_error TEXT`, and `INSERT OR IGNORE INTO config(key,value) VALUES ('PostQueueMaxAttempts','5')`.
  No `parked` column: a row is parked when `attempts >= max`; raising the key un-parks.
- `internal/config/config.go`: `CFG_KEY_POSTQUEUE_MAXATTEMPTS = "PostQueueMaxAttempts"` (append only).
- `db_post_queue.go`: `PostQueueMaxAttempts() int` (invalid/≤0 → 5); `PostQueueEntry.Attempts/LastError`
  (`COALESCE(last_error,'')`); `GetPendingPostQueueEntries` adds `AND attempts < ?` (signature unchanged);
  new `RecordPostQueueFailure(id, errText string, countAttempt bool)` (one `RetryableExec`: `in_processing=0,
  last_error=?, attempts=attempts+CASE WHEN ? THEN 1 ELSE 0 END`); `GetAllPostQueueEntries`/`GetPostQueueStats`
  gain status `parked` (pending excludes parked); `RetryPostQueueEntry` resets `attempts=0, last_error=NULL`
  guarded by `posted_to_remote=0 AND in_processing=0`; `DeletePostQueueEntry` becomes `WHERE id=? AND
  in_processing=0` (today only posted rows can be deleted although the page offers the button).
- `postmgr.go`: `postToProvider` returns a typed `errPostRejected` for 441; `processEntry` returns the rejection
  only when every pool answered 441; `ProcessPendingPosts` calls `RecordPostQueueFailure(id, err.Error(),
  errors.Is(err, errPostRejected))` per failed entry (connection/pool errors record `last_error` but do not
  count); `Run`: `if pause <= 0 { pause = 10 * time.Second }`; new `ErrStopped` returned from the stop branch
  (the remainder reset stays).
- `cmd/post-queue/main.go`: exit 130 only when `errors.Is(err, postmgr.ErrStopped)`; a batch that finished before
  the signal exits 0.
- Admin: `web_admin_postqueue.go` gets `adminRetryPostQueueEntry` (copy of the delete handler);
  `webserver_core_routes.go` L503 gets one line `POST /admin/postqueue/retry`; `admin_postqueue.html` shows
  attempts, last error, a `parked` badge, filter and stat, Retry when parked or attempts > 0.

### D-B: fetcher commit barrier, A-tools-5b (fl-nntp-db)
- New `internal/database/db_batch_commit.go`: per-group `captured`/`settled`/`dropped` counters beside
  `SQ3batch.groupNames` under a leaf `commitMu` (never in `TasksMap`: `expireIdleTasks` deletes those);
  `NoteCaptured(groups []*string)`, `CommitMark(newsgroup) CommitMark` (baseline of `dropped`),
  `WaitCommitted(m CommitMark, stop <-chan struct{}, timeout time.Duration) error` with
  `ErrBatchDropped`/`ErrBatchWaitStopped`/`ErrBatchWaitTimeout`, `settle(group, n, dropped)` closing a per-group
  `changed` channel; an `atomic.Bool flushReq` that `StartOrch`'s 125 ms loop honours by running
  `processAllPendingBatches(maxDBbatch-1)` (do not touch the "@AI: DO NOT CHANGE" lines).
- Accounting is synchronous: `threading.go:processArticle` L340-353 calls `NoteCaptured` BEFORE spawning the
  capture goroutine (counting inside `BatchCaptureOverviewForLater` races with the waiter; `gots++` in
  `DownloadArticles` also counts `alreadyStored` passes and is not "handed to the batch").
- `settle(n,false)` right after `batchInsertOverviews` succeeds in `processNewsgroupBatch` (before PHASE 2);
  `settle(..,true)` on every drop path: `getBatchGroupDB` gives up (L856-865), insert grace expired (L873-879),
  capture after shutdown (L178-183), `BatchDivider` returning on stop while holding an article (L1955-1959);
  the waiter also returns on `db.backgroundStop()`.
- `proc_DLArt.go:DownloadArticles`: `mark := CommitMark(newsgroup)` near L204; before `UpdateProgress`
  (L443-449) and only when articles were queued, `WaitCommitted(mark, shutdownChan, CommitWaitTimeout)`
  (package var, default 5 min, no flag); on error log, skip `UpdateProgress`, return a wrapped error (the next
  run refetches from the old progress; `LookupMsgIdInArticlesDB` dedups). No partial "last good end": drops are
  whole batches. `DownloadArticlesFromDate` needs no edit. Optional same slice: L248 `lastGoodEnd = start` →
  `start-1`.
- Decision D-B1 (default): on a dropped batch progress is held back and the run reports an error.

### D-C: smaller contracts
- fl-web exports `models.DecodeBodyText(body string, headers string) string` (wrapper around the O1 decoder) for
  the preview API and reply quoting; `ConvertToUTF8` stays for anything else.
- fl-nntp-db migration 0031 partial unique index `CREATE UNIQUE INDEX ... ON nntp_users(web_user_id) WHERE
  web_user_id IS NOT NULL AND active = 1` after a repair statement that keeps the lowest-id active mapping per
  web user and NULLs the rest (decision D-C1 default: lowest id wins; the repair logs nothing, the Outcome
  lists how to see what changed: `SELECT web_user_id, COUNT(*) ... HAVING COUNT(*) > 1` before upgrading).

## Waves

### Wave 0 (inline, orchestrator, on `plan-followup-leftovers` from `testing-001`)
1. `git mv` this plan to `.claude/plans/wip/`; preflight per the skill.
2. Docs fix-ups in one commit: `BUGS.md` (A-db-4 fixed in d27c46d/2b13adf; A-db-3 reduced to the
   `proc_DLArt.go` L283 cleanup done by fl-nntp-db; the queued→done path of test-timing-hardening; the deferred
   list restated as Q3/A-ops-13/14/16/Q5 only), `.github/copilot-instructions.md` L49/L125 (`test-nntp` gone),
   `README.md` L130 (no `-download-end-date`), delete `test_embed.go.bak` (calls a deleted function),
   `internal/database/thread_cache.README.md` (describe the current `batchUpdateThreadCache` shape).
3. Baseline checks; record `go vet` and the govulncheck summary before the toolchain change.

### Wave 1 (5 parallel slices; owned files disjoint; fl-ci-toolchain is MERGED FIRST, then `go vet ./...`
reruns on the merged tree before the other four merge, because the `go` directive can surface new vet findings)

- **`fl-ci-toolchain`** — owned: `.github/workflows/tests.yml` (new), `.github/workflows/release.yml`, `go.mod`,
  `go.sum`, `docs/audit/tools/govulncheck.txt` (regenerated). Forbidden: every `*.go`, migrations, templates.
  Goal: (1) `tests.yml` on `push` (branches `testing-001`, `main`) and `pull_request`: job `test` = the release
  workflow's test job (`go vet ./...`, `go test -race` over the CLAUDE.md package list plus
  `./internal/postmgr/ ./internal/cache/ ./cmd/tcp2tor/ ./cmd/web/`, `go-version-file: go.mod`, `PUGLEAF_TEST_DEADLINE=120s`),
  job `known-bugs` with `continue-on-error: true` running `PUGLEAF_KNOWN_BUGS=1 go test -count=1 ./internal/nntp/... ./cmd/nntp-server/...`;
  `permissions: contents: read`; concurrency group per ref. (2) `release.yml`: pin every action by commit SHA
  (with the version in a comment), `fail-fast: false` on the build matrix so one platform does not block the
  others (decision D-CI1 default), the same `PUGLEAF_TEST_DEADLINE`. (3) `go.mod`: add `toolchain go1.27.2`
  (keep the `go` directive unless a dependency forces it; say which), `go get golang.org/x/net@v0.60.0
  github.com/quic-go/quic-go@v0.59.1`, `go mod tidy` (drops `go-cpu-mem-profiler` and the indirect `gorilla/mux`),
  then `govulncheck ./...` → regenerate `docs/audit/tools/govulncheck.txt` and report the remaining count.
  Checks: `go build ./...`, `go vet ./...`, the full race list, both workflows parse (`actionlint` if available,
  else `python3 -c 'import yaml'`), `grep -c uses: release.yml` all pinned.
- **`fl-postqueue`** — owned: `internal/database/migrations/0030_main_post_queue_attempts.sql` (new),
  `internal/database/db_post_queue.go`, `internal/database/adf_opt_postqueue_test.go`, `internal/postmgr/postmgr.go`,
  `internal/postmgr/adf_opt_postmgr_test.go`, `cmd/post-queue/main.go`, `internal/config/config.go` (append the
  key only), `internal/web/web_admin_postqueue.go`, `internal/web/webserver_core_routes.go` (one route line),
  `web/templates/admin_postqueue.html`, new `internal/web/fl_pq_postqueue_test.go`. Forbidden: `internal/nntp/**`,
  `internal/processor/**`, `db_batch.go`, `queries.go`, `webserver.go`, `web_adminPage.go`, `web_admin.go`, other
  templates, `go.mod`. Goal: D-A in full plus the exit-130 fix. Tests: increment, park, selection skips parked,
  retry resets, raising the max un-parks, 441 counts vs transient errors, `ErrStopped`, retry handler.
  Checks: gofmt; vet; build; `go test -race ./internal/database/... ./internal/postmgr/... ./cmd/post-queue/... ./internal/web/...`.
- **`fl-web`** — owned: `internal/web/web_apiHandlers.go`, `web_sitePostPage.go`, `web_threadTreePage.go`,
  `cronjobs.go`, `web_admin_crons.go`, `web_admin_nntp.go`, `webserver.go`, `internal/database/db_cron_jobs.go`,
  `internal/models/sanitizing.go`, `internal/cache/sanitized_cache.go`, `web/templates/thread-tree.html`, new
  `internal/web/fl_web_*_test.go`, `internal/models/fl_web_sanitize_test.go`. Forbidden: `webserver_core_routes.go`
  (a needed route line goes to the orchestrator at merge), `web_admin_postqueue.go`, `admin_postqueue.html`,
  `db_post_queue.go`, `queries.go`, `db_batch.go`, `internal/config/**`, `internal/postmgr/**`, migrations.
  Goal: `/api/thread-tree` returns the redacted From (same helper the HTML tree uses); hidden-root placeholder in
  `thread-tree.html` shows no empty Message-ID row and no dead "View article" button; a tree node whose overview
  failed to load can still be marked hidden (or the link answers a proper message instead of 404);
  `lookupReplyReferences` must not copy a hidden article's References; cron: an ended job keeps its log buffer
  until the next start (bounded), `calculateNextRun` guards interval 0, the enable/disable toggle uses a
  conditional `UPDATE ... WHERE enabled = ?` (`db_cron_jobs.go`) and reports "already changed"; NNTP user admin:
  a legacy mapping to a deleted web user no longer blocks the update (treated as "no mapping" with a notice);
  web post Subject folded/limited so the stored line is ≤ 998 octets; preview API and reply quoting use
  `models.DecodeBodyText` (adjust `adf_xss_test.go` expectations only if the entity-decoding assertion depends on
  it: say so); sanitizer: a malformed QP sequence falls back per line, not per body; `cachedSize` cannot drift on a
  write to a just-evicted entry; the cache stop once-guard is per cache instance (re-init after Shutdown is
  stoppable). Decisions D-W1 (default): section assign keeps the new single-membership behaviour; the "first
  registered user is admin" rule stays. Checks: gofmt; vet; build;
  `go test -race ./internal/web/... ./internal/models/... ./internal/cache/... ./internal/database/...`.
- **`fl-ops-tools`** — owned: `cmd/recover-db/`, `cmd/web/main.go`, `cmd/web/main_functions.go`,
  `cmd/web/adf_opt_web_test.go`, `cmd/nntp-fetcher/main.go`, `cmd/nntp-fetcher/adf_opt_fetcher_test.go`,
  `cmd/nntp-analyze/`, `internal/processor/analyze.go`, `internal/processor/adf_opt_analyze_test.go`,
  `cmd/import-flat-files/`, `build_import_flat-files.sh`, `cmd/rslight-importer/`, `internal/processor/rslight.go`,
  `internal/processor/rslight_articles.go`, `internal/processor/adf_opt_rslight_test.go`,
  `cmd/history-rebuild/pprof_addr.go` (+ its test), `cmd/tcp2tor/main.go`, `cmd/tcp2tor/adf_opt_tcp2tor_test.go`,
  `cmd/fix-references/`, `cmd/fix-thread-activity/`. Forbidden: `internal/processor/{proc_DLArt,intake,processor,threading}.go`,
  `internal/database/**`, `internal/nntp/**`, `cmd/nntp-server/**`, `cmd/post-queue/**`, `internal/config/**`.
  Goal: recover-db repair step 6 queries `threads` without the non-existent `t.child_articles` (use the real
  column or the thread_cache count), `fixed++` only after a successful UPDATE, cap the parse-failure problem list;
  cmd/web: `progressDB` closed on every exit path, `signal.Notify` before the one-shot modes, a failed
  `UpdateNewsgroupActive` exits 1, delete the dead `FetchRoutine` (body is commented out, call uses an old
  signature); fetcher: `-fetch-newsgroups-from-remote` path gets the SIGTERM/SIGINT handler, `-reset-progress`
  with a wildcard `-group` applies the wildcard filter (decision D-O1 default) or refuses; nntp-analyze:
  `-timeout` applied in `$all` mode (`ConnectTimeout`), the 3 s CSV-header sleep removed, IPv6 host literal gives
  a sane cache dir name (`[2001:db8::1]` → `2001_db8__1`); import-flat-files: `-dry-run` opens no database files
  (`GetDB` gated), `build_import_flat-files.sh` either drops the `appVersion` ldflag or the package declares the
  var (prefer the var + a version log line); rslight-importer: `errors.Join` output reduced to count + first five,
  SIGINT/SIGTERM during the start delay and the import go through `shutdown()` (cancellation into
  `ImportAllSQLiteDatabases`); history-rebuild `-pprof` requires a port (same fix as rslight's copy, with a
  test); tcp2tor: `-add` checks the bind policy and warns, a non-closed `Accept` error retries with backoff
  (net/http style) instead of `log.Fatalf`; fix-references and fix-thread-activity get a SIGINT/SIGTERM handler
  that goes through their exit helper. Checks: gofmt; vet; build; the build scripts of every touched tool;
  `go test -race ./cmd/recover-db/... ./cmd/web/... ./cmd/nntp-fetcher/... ./cmd/nntp-analyze/... ./internal/processor/... ./cmd/import-flat-files/... ./cmd/rslight-importer/... ./cmd/history-rebuild/... ./cmd/tcp2tor/... ./cmd/fix-references/... ./cmd/fix-thread-activity/...`.
- **`fl-nntp-db`** — owned: `internal/nntp/nntp-backend-pool.go`, `nntp-client-commands.go`,
  `transfer-progress.go`, the server session/group/article command files needed for the two server items,
  `nntp-client-fake_test.go` (`cliWait = hDeadline`), `nntp-server-harness_test.go` (`hIsErrNotFound`),
  `cmd/nntp-server/main.go` (`-local430ttl`), `cmd/nntp-server/server_e2e_test.go` (`e2eKnownBug`),
  `internal/database/hierarchy_cache.go`, `db_batch.go`, new `db_batch_commit.go`, `db_groupdbs.go`,
  `migrations/0031_main_nntp_users_unique_web.sql` (new), `nntp_user_admin.go` (or wherever the one-account rule
  is checked), `internal/database/adf_opt_hierarchy_test.go`, `adf_batch_test.go`, `nntp_intake_test.go`, new
  `fl_nntpdb_*_test.go`, `internal/processor/proc_DLArt.go`, `intake.go`, `processor.go`, `threading.go`,
  `intake_dl_test.go`. Forbidden: `cmd/nntp-fetcher/**`, `cmd/web/**`, `internal/postmgr/**`, `cmd/post-queue/**`,
  `db_post_queue.go`, `db_cron_jobs.go`, `internal/models/**`, `internal/web/**`, `internal/config/**`,
  `processor/{analyze,rslight,rslight_articles}.go`; exported nntp API may only grow.
  Goal: D-B (commit barrier) and D-C1 (unique mapping index); `Pool.XHdrStreamed` returns the stream error and
  `proc_DLArt.go` treats a cut-off stream as an error, not a normal close; the "Pool closed with positive count"
  warning at the end of a clean run goes (count the handed-back connection); `-local430ttl` flag (default 60 s)
  wired to the local430 cache; a session drops its `currentGroup` when an admin deactivates the group (next
  command answers 411/412 as the RFC path does); STAT by message-id reads a lean row (id/number/message-id);
  A-db-3 leftover: `proc_DLArt.go` L283 uses `LookupMsgIdInArticlesDB` and errors return a `BatchItem` error,
  `ExistsMsgIdInArticlesDB` deleted; hierarchy cache: per-top rebuild mutex instead of the global `rebuildMu`
  across `RetryableQuery`, and `UpdateNewsgroupStats` marks the top dirty when the group is missing and
  `messageCountIncrement > 0`; a test for the batch writer's S2 retry path (commit ok, SELECT fails, retry1);
  dead code: `TransferResult`, `Batch.GroupQueues`/`GetOrCreateGroupBatch` (fix `intake_dl_test.go:104`),
  `hIsErrNotFound`, `e2eKnownBug`; `GetArticle` wraps with `%w` at nntp-client-commands.go:108. Decision D-N1
  (default): `-plaintextauth` default stays `allow` (no behaviour change). Checks: gofmt; vet; build;
  `go test -race ./internal/nntp/... ./internal/database/... ./internal/processor/... ./cmd/nntp-server/...`;
  `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` lists only T-nntp-article-1.

### Wave 2 (inline, orchestrator)
- Merge order: fl-ci-toolchain, then `go vet ./...` + build on the merged tree; then the other four (any order);
  resolve the single `webserver_core_routes.go` line if fl-web asked for a route.
- `BUGS.md`: move the closed ids to the "fixed by plan followup-leftovers" bullet; keep Q3/A-ops-13/14/16/Q5 and the
  out-of-scope list as the only deferred items. Update `.claude/CLAUDE.md` only if the package list for
  `go test -race` changed (it adds `./internal/postmgr/ ./internal/cache/ ./cmd/tcp2tor/ ./cmd/web/` to match
  `tests.yml`).

## Checks (merged tree)
```bash
gofmt -l internal cmd            # prints nothing
go vet ./... && go vet -tags e2e ./cmd/nntp-server/
go build ./...
go test -race ./internal/database/... ./internal/web/... ./internal/history/... ./internal/nntp/... \
              ./internal/processor/... ./internal/models/... ./internal/cache/... ./internal/common/... \
              ./internal/postmgr/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/... \
              ./cmd/nntp-fetcher/... ./cmd/nntp-analyze/... ./cmd/recover-db/... ./cmd/tcp2tor/... ./cmd/web/... \
              ./cmd/post-queue/... ./cmd/import-flat-files/... ./cmd/rslight-importer/... ./cmd/fix-references/... \
              ./cmd/fix-thread-activity/... ./cmd/audit-web-posts/...
PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/   # only T-nntp-article-1, fails on purpose
govulncheck ./...                                                   # count reported in the Outcome
for f in *.sh scripts/*.sh; do bash -n "$f"; done
./build_webserver.sh ./build_fetcher.sh ./build_nntp-server.sh ./build_import_flat-files.sh ./build_history-rebuild.sh  # one at a time
```

## End-to-end (verifier, scratch root `-data ./data-test-followup-leftovers`)
1. Post queue: seed a queue row for a provider that answers 441 (the NNTP test fake or a local nntp-server on a
   non-default port with posting refused), run `build/post-queue -data <scratch>` five times → the row shows
   `attempts=5`, `last_error` set, status `parked` on the admin page (web on a non-default port, `-edit-cronjobs`
   not needed); `POST /admin/postqueue/retry` resets it; raising `PostQueueMaxAttempts` to 6 selects it again; a
   transient connection error does not increment. SIGINT during a one-shot batch exits 130; a finished batch with a
   late signal exits 0.
2. Fetcher barrier: with a local nntp-server as backend (scratch data, non-default port) fetch a small group;
   `progress.db` records the end only after the group DB holds the rows (compare counts); simulate a dropped batch
   by closing the group DB mid-run in a unit test (`fl_nntpdb_commit_barrier_test.go`) → `ErrBatchDropped`,
   progress unchanged.
3. `cmd/web -write-active-file` and `-disable-active-dry-run` still exit through `exitAfterDB`; SIGTERM during a
   one-shot mode is handled; `recover-db -repair` on a scratch group prints no "Could not check threading".
4. `tests.yml` syntax validated; `release.yml` actions all pinned; `go version -m build/webserver` shows the new
   toolchain; govulncheck count ≤ the pre-change count minus the fixed advisories (report both numbers).
5. Stress sanity: `go test -race -count=3 -cpu 1,4 ./internal/nntp/ ./internal/database/ ./internal/postmgr/` with a
   `go test` hog in parallel: zero failures (the timing rules of plan test-timing-hardening still hold).

## Open questions (defaults apply unless the user overrides at the wave-1 boundary)
- D-CI1: `fail-fast: false` on the release build matrix (one failed platform no longer blocks the release)? default yes.
- D-B1: on a dropped batch the fetcher holds progress back and the run reports an error (refetch next run)? default yes.
- D-C1: duplicate web-user mappings: the lowest-id active NNTP user keeps the mapping, the others are NULLed? default yes.
- D-W1: section assign keeps the single-membership behaviour from O1? default yes.
- D-O1: `-reset-progress` with a wildcard group applies the wildcard filter (instead of refusing)? default yes.
- D-N1: `-plaintextauth` default stays `allow`? default yes (no behaviour change for existing clients).
