# Plan: test-timing-hardening

Queued 2026-10-15 by user request ("queue it as its own plan after O1") during the optional waves of
`audit-delta-fixes`. Companion file with the full inventory (one table per category, file:line, margins, proposed
rewrites): `.claude/plans/queued/test-timing-hardening-survey.md` (surveyed at `plan-adf-optional` b25df6c; re-check
anchors against the tree the run starts from).

## Context

- Several test runs this month failed only because the machine was loaded (five implementers running `go test -race`
  at once, load average 12-20): `AUTHINFO PASS i/o timeout` (5 s harness deadline), `Connect took 2.008s, want < 2s`,
  `TestStmCheck i/o timeout`, `TestNSHAuthUnknownUserTiming`, tcp2tor idle tests with 4x margins. Single runs pass.
- User rules (2026-10-15, see memory `no-timing-tests`):
  1. No test asserts "took less than X" unless the margin against what it measures is at least 10x; a structural
     assertion (error type, counter, hook) is preferred over any duration.
  2. Lower-bound assertions ("a 501 took at least the configured delay") are fine.
  3. Hang guards (client deadlines, `time.After` waits, contexts) exist only to stop a hung test: 30 s or more,
     set centrally, never used as the assertion.
  4. Password timing-enumeration tests are overkill for this project ("we are not a bank"):
     `TestNSHAuthUnknownUserTiming` is deleted. The dummy-compare in the auth path stays (it is free).
- Inventory (survey): 39 upper-bound assertions (36 to change), 16 lower-bound (fine), ~150 literal hang-guard sites
  (~300 counting helper call sites), 1 statistical test, 5 weak sleep-based synchronisations.

## Decisions

- D1 (default, confirm at wave 0): **P1 accepted**: `NNTPServer` gets two `atomic.Int64` counters `errorDelays` and
  `articleDelays`, incremented in `rateLimitOnError` (`internal/nntp/nntp-cmd-helpers.go`) and at the four
  ArticleCmdDelay sites. One atomic add per delayed reply; no behaviour change. Seven "no error delay" checks become
  structural (counter unchanged across the command) instead of "took < 1 s".
- D2: `PosterManager` idle pause becomes injectable (`idlePause` field, default 10 s) so the postmgr stop test asserts
  "returned" instead of "returned within 2 s".
- D3: one package-level test constant per package, `testWait`, read from `PUGLEAF_TEST_DEADLINE` (default 60 s);
  the wait helpers (`hs.dial` deadline, `WaitOpen`, `sessExpectClosed`, `time.After` guards) use it. Negative
  "must still be blocked" windows and the known-bug poll (`nntp-server-article_test.go:900`) keep their short
  values on purpose. Web `Shutdown` contexts are pass/fail and go to 30 s+.
- D4: the `TestArtNoDelay` guard is rewritten structurally (D1 counter) because `DefaultArticleCmdDelay` is 25 ms,
  not the 200 ms its comment claims, so 50 STATs vs a 1 s bound guards almost nothing.
- D5: `Backend()` in the client tests sets `ConnectTimeout` to 30 s where the test does not exercise the timeout;
  the timeout tests assert the error type (`net.Error.Timeout()` / `context.DeadlineExceeded`) and keep a 10x bound.

## Design

- Rewrite rules per category (survey tables 1-5):
  - upper bound → structural check where a counter/hook/error type exists; else bound ≥ 10x the configured value;
  - hang guard → `testWait` (helpers raise shorter values to it; literal sites replaced where touched);
  - sleep-based sync → channel/WaitGroup/poll-until with `testWait` as the ceiling;
  - statistical → delete.
- The harness file headers that say "the harness is read-only" are lifted for slice 1 only.
- No change to production timeouts except P1 counters and D2's injectable pause.

## Waves

### Wave 0 (inline, orchestrator) — DONE 2026-10-15
- User accepted D1 (P1 counters) and D2, Q2 = 60 s. Done inline so every slice can run in one wave: `NNTPServer`
  got `errorDelays`/`articleDelays` (`atomic.Int64`, incremented at the five delay sites whether or not the delay is
  > 0; accessors `ErrorDelayCount()`/`ArticleDelayCount()`), and the harness got `hDeadline` (var, from
  `PUGLEAF_TEST_DEADLINE`, default 60 s) used by `hs.dial`. Slices use these; wave B below collapses into wave A.

### Wave A (7 parallel slices, owned files disjoint; tth-nntp-rfc no longer waits for wave B)
- slice `tth-nntp-harness`: `internal/nntp/nntp-server-harness_test.go`, `nntp-server-session_test.go`,
  `nntp-server-auth_test.go`, `nntp-server-overview_test.go`, `nntp-server-article_test.go`,
  `nntp-server-rfccmds_test.go`, `nntp-server-group_test.go`, `nntp-intake_test.go`, `nntp-wiring_test.go`.
  `hDeadline` + env override, clamps in `tryDial`/`sessExpectClosed`; TestHarnessIsolatedTimeout, TestSessStop,
  TestOvrHugeRange, TestArtNoDelay (structural via ArticleDelayCount), artPollStat223.
- slice `tth-nntp-client`: `nntp-client-{fake,conn,cmds,validate,progress}_test.go`: `cliWait`, WaitOpen clamp,
  D5; the eight "Connect took < Ns" bounds become timeout-error checks.
- slice `tth-database`: delete `TestNSHAuthUnknownUserTiming` (`nsh_auth_test.go`, keep the `time` import if still
  used); `fu_batch_test.go`, `lo2_writer_test.go`, `w2_dbperf_test.go`, `adfreopen/reopen_test.go` (12 s vs 10 s
  CronDB tick → 2 min poll), `adf_opt_postqueue_test.go`, `adf_batch_test.go`, `adf_cache_test.go`,
  `adf_lifecycle_test.go`, `w1_sqlite_test.go`, `adf_opt_progress_test.go`, `testWait` next to `w0DB`.
- slice `tth-web`: `lo1_core_test.go` (Shutdown grace 5 s vs 8 s bound), `lo1_handlers_test.go`, `lo2_server_test.go`,
  `w1_server_test.go`, `adf_cron_test.go`, `adf_opt_admin_test.go`, `adf_post_test.go`, `fu_cron_test.go`,
  `testmain_test.go`.
- slice `tth-small`: `internal/processor/{intake,intake_dl,wiring}_test.go`, `internal/history/history_test.go`,
  `internal/postmgr/adf_opt_postmgr_test.go` + `postmgr.go` (D2), `internal/cache/adf_sanitized_cache_test.go`,
  `cmd/web/adf_web_test.go`, `cmd/expire-news/main_test.go`.
- slice `tth-tcp2tor-e2e`: `cmd/tcp2tor/adf_tcp2tor_test.go` (3x/4x margins at :62/:84), `adf_opt_tcp2tor_test.go`
  (:366 Serve-after-Stop → `testWait`), `cmd/nntp-server/server_e2e_test.go`, `e2e_binary_test.go`,
  `e2e_binary_rfc_test.go` (10 s/20 s guards → `testWait`, 1 s `intakeDLTestWait`).

- slice `tth-nntp-rfc` (now in wave A): `nntp-rfc-{stream,starttls,reader,lists,overview}_test.go` and
  `nntp-wildmat_test.go` rewritten against the P1 counters and `hDeadline` (production files already changed in wave 0).

## Checks
```bash
gofmt -l <touched dirs>; go vet ./...; go build ./...
go test -race ./internal/database/... ./internal/web/... ./internal/history/... ./internal/nntp/... \
              ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/...
go test -race ./internal/postmgr/ ./internal/cache/ ./cmd/tcp2tor/ ./cmd/web/
PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/   # list unchanged (T-nntp-article-1), fails on purpose
```
- `./internal/nntp/` wall time must not grow by more than a few seconds; the known-bug path must not wait on a long guard.

## End-to-end (verifier)
- Stress pass: `go test -race -count=5 -cpu 1,4` over the touched packages while a CPU hog runs in parallel
  (a second full `go test -race ./...` is enough; no external tools needed). Zero failures.
- A grep audit: no `want <`/`took` upper-bound assertion with a margin under 10x remains (list the survivors with
  their margin in the Outcome); no literal client deadline under 30 s outside the deliberate short windows.
- Scratch data only (`-data ./data-test-test-timing-hardening`); never `./data`, `data_ram`, `data_local`.

## Progress

### Wave 0 + wave A (2026-10-15)
- `plan-test-timing-hardening` from `testing-001` @ d694e6a; wave 0 inline 7eed92b (P1 counters, `hDeadline`).
- tth-small: `worktree-agent-a02cf379364c6d82c` (0e0bee8, 717c71c, review fix 250cacd), merge 24cf27f. D2 done
  (`idlePause`). Review MERGE; minors fixed (comment, acquire(false) under testWait, 1 s/10x latency bound kept
  beside the structural check, negative-window comments).
- tth-web: `worktree-agent-ae3b84fafcab33e6e` (0b1bf5c, review fix 5e83b42), merge 2ac6749. `testShutdownWait()`
  >= 30 s; FinalFlushBounded structural via the grace log line; TokenUsageDuringDrain waits on `tokenUsageDone`;
  fu_cron stack poll; Ollama test proves the client through a counting transport plus a 10 s / 100x bound.
- tth-tcp2tor-e2e: `worktree-agent-a81fbc10ac9fe3cb3` (b29f85e, 996784a, review fix bfa567c), merge 6809ae4;
  inline 852cd25 raised `scripts/test-nntp-server.sh` outer timeout 300 → 900 s. Timeout tests keep a 10x bound
  beside the error-type check; StopDuringDial structural (dial timeout 10x testWait, proxy conn closed);
  lines-20000 waits for active connections == 0 instead of a 1.5 s log window; N51 soft window 10 s.
- tth-nntp-rfc: `worktree-agent-a9d2927cd5154f049` (99d5c1e, review fix 8a77227), merge 6c5aaf5. Seven "no error
  delay" checks → `ErrorDelayCount()` deltas (== 0 / == 1); the 501/412 lower bounds stay; overview 10 s bounds
  dropped (per-read hDeadline); wildmat fuzz termination guard with panic recovery.
- tth-nntp-client: `worktree-agent-ad7fc8f0f71aabdae` (c6f648f, review fix a15328e), merge 4b69355. `cliWait`,
  WaitOpen clamp, Backend ConnectTimeout 30 s (D5); eight "Connect took" bounds → timeout-error checks plus a
  15 s (50x) bound against the 30 s default; back-off window 3 s and commented.
- tth-database: `worktree-agent-a5ffccd8abdb1a025` (4bd70a0, review fix d5f2a9e), merge ef4e204.
  `TestNSHAuthUnknownUserTiming` deleted; retry2/stats-retry/shutdown-clock/TTL-concurrent structural;
  adfreopen per-run temp root (was not repeatable with -count>1, a real bug) and 2 min CronDB budget;
  post-queue retry poll on the "database is locked" log line.
- tth-nntp-harness: `worktree-agent-a4db13ecf84d5e907` (f09ecab, review fix cfbd3cf), merge cf4ada3. tryDial
  clamps to hDeadline; TestHarnessIsolatedTimeout / TestSessStop / TestOvrHugeRange / TestArtNoDelay structural;
  ihave-arrival a single STAT; the known-bug poll keeps its 1 s window on purpose; Stop poll breaks only on a
  dial error; huge-range child 4x hDeadline.
- Accepted limits (D1): a bare `time.Sleep` before a no-delay reply would not be seen by the counters; TestArtNoDelay
  no longer proves that ArticleCmdDelay=0 skips the sleep (D4). Leftovers: `cliWaitFromEnv` duplicates
  `hDeadlineFromEnv` (fold after merge); `testSOCKS5Connection` wraps with %v (an errors.Is check would need %w);
  `GetArticle`'s `%v` at nntp-client-commands.go:108 drops the net.Error on a slow 220.
- Merged tree cf4ada3: gofmt clean, vet (incl. -tags e2e) and build ok, race suite over 13 package sets ok in 188 s
  wall (run with no other load), known bugs unchanged (T-nntp-article-1). Grep audit of the surviving upper bounds:
  every one is >= 10x (tcp2tor 10x, client 50x, intake 10x, web busy checkpoint 100x, Ollama 100x) or a lower bound.

## Open questions (ask at wave 0)
- Q1: P1 counters on `NNTPServer` (D1): accept the two atomics in production code for test observability?
- Q2: `PUGLEAF_TEST_DEADLINE` default 60 s, or 30 s?

## Outcome (2026-10-15)
- **Integration branch:** `plan-test-timing-hardening` from `testing-001` @ d694e6a, merged into `testing-001` as 03b63c3
  (`git merge --no-ff`, 2026-10-15). Nothing pushed. Survey: `.claude/plans/done/test-timing-hardening-survey.md`.
- **Waves:** 0 inline (7eed92b: P1 counters `ErrorDelayCount`/`ArticleDelayCount`, harness `hDeadline`), A with
  7 parallel slices, all reviewed and merged (see Progress). Production diff versus `testing-001`: the five nntp
  files of P1, `postmgr.go` (`idlePause`), `scripts/test-nntp-server.sh` (timeout 900), and 4956980: `handlePost`
  copies the message-id before `ProcessIncomingArticle` (pre-existing data race with `processNewsgroupBatch`
  clearing the struct, found by the stress pass; it could answer 240 with an empty id and remove the wrong
  local430 key).
- **Worktrees:** `/tank0/claude/trees/go-pugleaf/agent-{a02cf379364c6d82c,ae3b84fafcab33e6e,a81fbc10ac9fe3cb3,
  a9d2927cd5154f049,ad7fc8f0f71aabdae,a5ffccd8abdb1a025,a4db13ecf84d5e907}` (branches `worktree-agent-<id>`);
  the user cleans them up.
- **Verification (verifier on 5929038, code cf4ada3):** gofmt/vet(+e2e tag)/build clean; race suite alone 174 s
  wall, `internal/nntp` 170 s (no growth); stress pass `-race -count=5 -cpu 1,4` over 11 packages under a go-test
  CPU hog (load 12-18, 32 min): zero timing failures, one data race in `cmd/nntp-server` (fixed in 4956980; package
  then ok with `-count=3 -cpu 1,4`); `PUGLEAF_TEST_DEADLINE=5s` passes nntp/web/database/tcp2tor/postmgr (a
  passing test never waits on the guard); known bug T-nntp-article-1 still fails in 1.02 s; grep audit: every
  surviving upper bound >= 10x, no unclamped hang guard under 30 s; `TestNSHAuthUnknownUserTiming` gone,
  `db_nntp_users.go` untouched; `scripts/test-nntp-server.sh` SUMMARY pass=38 fail=0 info=1.
- **Leftovers:** `cliWaitFromEnv` duplicates `hDeadlineFromEnv`; `testSOCKS5Connection` wraps with %v;
  `GetArticle` %v at nntp-client-commands.go:108; `TestSessIdleTimeout/activity-resets-timer` has ~450 ms of
  absolute slack (10x by rule); `postmgr.idlePause` has no zero fallback (only NewPosterManager constructs it);
  accepted D1/D4 limits (a bare sleep before a no-delay reply is invisible to the counters).

