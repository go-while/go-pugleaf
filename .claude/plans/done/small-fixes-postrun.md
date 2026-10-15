# Plan: small-fixes-postrun

Queued 2026-10-15 at the user's request after plan `followup-leftovers`: the three actionable leftovers plus the
missing fetcher drop-path test. Four disjoint slices, one wave, then the verifier. Anchors at `testing-001` @ 876c397.

## Context

- **Post queue cannot interrupt an in-flight POST.** `PostArticle` (`internal/nntp/nntp-client-commands.go`
  L1222-1301) holds `c.mux` for the whole command and blocks in `ReadCodeLine(340)`, `writer.Flush()` and
  `ReadCodeLine(240)`; the only bound is the per-Read idle timeout (`deadlineConn.Read` re-arms it, default 2 min;
  cmd/post-queue sets none). `ForceCloseConn` only sets `closing` and then waits for the same mutex, so a stop
  from `PosterManager` takes effect only after the peer hangs up. `ProcessPendingPosts` checks `stopped()` only
  between entries. The verifier (plan followup-leftovers) saw a one-entry run exit 0 after 60 s only when the stub
  closed. Also "Processed N pending posts" (`cmd/post-queue/main.go` L173) counts failures.
- **`high_water` never written on intake.** `query_updateNewsgroupsStats` (`internal/database/db_batch.go`
  L623-634, exec at L1062) sets `message_count`, `last_article`, `updated_at` only; `high_water` stays 0 until
  `-repair-watermarks` (`internal/preloader/preloader.go:RepairNewsgroupWatermarks`). `-write-active-file` reads
  `high_water` raw, so a group with 300 posted articles exports as `group 0 1 y`. `UpdateNewsgroupWatermarks`
  (`queries.go` L2000) has no caller. The nntp test harness sets `high_water` by hand to work around it.
- **Thread tree depends on map order.** `BuildThreadTree` (`internal/database/tree_cache.go` L66-196) iterates
  the `overviews` map: `tree.RootNode` is set only when the loop reaches the root (an orphan met first fails with
  "root node is nil when trying to attach orphaned node"), `Depth = parent.Depth + 1` reads a parent depth that
  may not be set yet, the parent search is O(n²) and random with duplicate message-ids, a self-reference or
  2-cycle makes `countDescendants`/`assignSortOrderRecursive` recurse forever, and `sort.Slice` by date is not
  stable. Found by the fl-web fixture (plan followup-leftovers).
- **No test drives `DownloadArticles` through a dropped batch.** The barrier (`proc_DLArt.go` L431-439) and the
  progress write (L440-446) have no processor-side test; the database tests cover `ErrBatchDropped` only.

## Design

### D1 post-queue interrupt (slice `sfp-postqueue-stop`)
- `nntp-client-commands.go`: `PostArticleContext(ctx, article)`; after `c.mux.Lock()`: `dc := c.dconn;
  stop := context.AfterFunc(ctx, func() { c.closing.Store(true); _ = dc.Conn.Close() }); defer stop()`.
  `PostArticle(a)` stays as `PostArticleContext(context.Background(), a)`. Close is sticky and safe concurrently;
  a past deadline would be re-armed by the next Read, a Close is not.
- `postmgr.go`: `PosterManager` gets `ctx`/`cancel`; `closeStop` (L50-55) cancels; `postToProvider` uses
  `pool.GetContext(pm.ctx, MODE_READER_MV)` and `conn.PostArticleContext(pm.ctx, article)` (the existing
  `ForceCloseConn` after the return stays); `processEntry` checks `pm.stopped()` between pools and returns
  `ErrStopped`; `ProcessPendingPosts`: when stopped after a failed entry, the entry goes into the reset list (not
  `RecordPostQueueFailure`), then the existing stop branch. Documented caveat: an article flushed before the lost
  240 may be posted twice; the next run gets the 441 duplicate.
- Count `posted`/`failed` in the loop; keep the `int` return (Run relies on `done == limit`) and log
  `Processed %d pending posts (%d posted, %d failed)` inside `ProcessPendingPosts`; cmd/post-queue's own line says
  the same.
- Test: extend `flPqFake` with a hang mode (read the dot lines, close a `received` channel, block until the conn
  is closed); pool with `ReadTimeout: -1` so a return proves the interrupt, not a timeout; run
  `ProcessPendingPosts` in a goroutine, wait for `received`, `pm.Stop()`; assert return within `testWait` with
  `ErrStopped`, row not posted, `in_processing = 0`, `attempts = 0`. Plus a `Run`/`shutdownChan` variant.

### D2 high_water on intake (slice `sfp-high-water`)
- `query_updateNewsgroupsStats`: add `high_water = CASE WHEN ? > COALESCE(high_water, 0) THEN ? ELSE high_water END`
  (never lowers it), pass `maxArticleNum` twice more at L1063, fix the argument comment at L623-627. Nothing else
  (`HierarchyCache.UpdateNewsgroupStats` keeps its cached `HighWater` as is).
- Test: `nntp_intake_test.go` `idbStats` returns `high_water`; `TestIntakeDBHistoryForNullHierarchyRow`-style
  two batches of two → `high_water == 4`; a stored higher value is not lowered.

### D3 deterministic thread tree (slice `sfp-tree-order`)
- Extract `buildTreeFromOverviews(threadRoot int64, ov map[int64]*models.Overview) (*ThreadTree, error)`;
  `BuildThreadTree` keeps loading (L75-118) and caching (L190).
- Sorted article numbers; message-id → number index built ascending (first wins); the root node is created and
  `tree.RootNode` set before any loop; parent = index[last reference], missing / self / cycle (walk `parentOf`
  with a guard) → root; `parentOf` recorded; second pass from the root sets `Depth`; unreached nodes attach to
  the root at depth 1; `assignSortOrderRecursive` uses `sort.SliceStable` with an `ArticleNum` tiebreak.
- Test: new `internal/database/fu_tree_build_test.go`, pure in-memory overviews (like `adfFlagsTree` in
  `adf_flags_test.go`): root 10, child 3 replying to 7, 7 replying to 10, an orphan with an unknown reference,
  a self-reference, a 2-cycle; loop 50 times; assert no error, `RootNode != nil`, exact `ParentArticle` and
  `Depth` per node, `MaxDepth`, `TotalNodes`, identical `TreePath`s every iteration. Keep
  `TestFlWebTree*`, `adf_hide_test.go` and `TestAdfFlags*` passing.

### D4 drop-path test (slice `sfp-dl-drop-test`)
- `proc_DLArt.go`: move L431-446 into `func (proc *Processor) commitThenProgress(newsgroup, providerName string,
  mark database.CommitMark, progressDB *database.ProgressDB, end, gots, gotQueued, dups int64, stop <-chan
  struct{}) error` and a package var `downloadWaitCommitted = func(sq *database.SQ3batch, m database.CommitMark,
  stop <-chan struct{}, d time.Duration) error { return sq.WaitCommitted(m, stop, d) }`. Behaviour unchanged.
  Also: an `UpdateProgress` error is returned, not only logged.
- Test: new `internal/processor/fl_dl_drop_test.go`: `database.NewProgressDB(t.TempDir())`, progress 10, waiter
  overridden to return `fmt.Errorf("%w: test", database.ErrBatchDropped)` (restored in `t.Cleanup`), call with
  `end=20, gots=1, gotQueued=1` → `errors.Is(err, database.ErrBatchDropped)` and last article still 10; control
  case waiter nil → 20. Reuse the package's `testWait`; no DB beyond the progress DB, no NNTP.

## Waves

### Wave 0 (inline, orchestrator)
- `git mv` to wip; `plan-small-fixes-postrun` from `testing-001`; baseline `go vet`, `go build`, and
  `go test -race ./internal/postmgr/ ./internal/database/ ./internal/processor/ ./internal/nntp/`.

### Wave 1 (4 parallel slices, owned files disjoint)
- **`sfp-postqueue-stop`** — owned: `internal/postmgr/postmgr.go`, `internal/postmgr/adf_opt_postmgr_test.go`,
  `internal/nntp/nntp-client-commands.go` (only `PostArticle`/`PostArticleContext`), `cmd/post-queue/main.go`
  (only the log line). Forbidden: `internal/nntp/nntp-client.go`, `nntp-backend-pool.go`, `internal/database/**`,
  `internal/web/**`. Goal: D1. Checks: gofmt; vet; build; `go test -race -count=3 ./internal/postmgr/`;
  `go test -race ./internal/nntp/ -run 'TestCli'`; `go test -race ./internal/web/ -run 'FlPq'`.
- **`sfp-high-water`** — owned: `internal/database/db_batch.go` (only the stats const and its exec),
  `internal/database/nntp_intake_test.go`. Forbidden: everything else in `internal/database` (slice 3 owns
  `tree_cache.go` and adds a test file there), `internal/nntp/**` test harness lines that set `high_water` by hand
  (leave them; note which could go). Goal: D2. Checks: gofmt; vet; build;
  `go test -race ./internal/database/... ./internal/nntp/ -run 'TestIntakeDB|TestAdfBatch|TestFlNntpdb|TestGrp|TestSess'`.
- **`sfp-tree-order`** — owned: `internal/database/tree_cache.go`, new `internal/database/fu_tree_build_test.go`
  (helper names prefixed `fuTree*`; do not redefine package helpers). Forbidden: `db_batch.go`, every other
  database file, `internal/web/**`. Goal: D3. Checks: gofmt; vet; build; `go test -race -count=3 -run
  'FuTree|AdfFlags|AdfHide' ./internal/database/`; `go test -race ./internal/web/ -run 'FlWebTree|AdfHide|W1API'`.
- **`sfp-dl-drop-test`** — owned: `internal/processor/proc_DLArt.go` (only the commit/progress block and the
  seam var), new `internal/processor/fl_dl_drop_test.go`. Forbidden: `internal/database/**`, `cmd/nntp-fetcher/**`,
  `threading.go`, `intake.go`. Goal: D4. Checks: gofmt; vet; build; `go test -race -count=3 ./internal/processor/`;
  `go test -race ./cmd/nntp-fetcher/`.

## Checks (merged tree)
The CLAUDE.md list (same as `.github/workflows/tests.yml`), plus `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/
./cmd/nntp-server/` (only T-nntp-article-1).

## End-to-end (verifier, scratch root `-data ./data-test-small-fixes-postrun`)
1. Post queue: provider pointing at a Python stub that answers 200/340 and then never replies; one queued row;
   `build/post-queue -data <root>`; SIGINT after 2 s → exit 130 within a few seconds (not after the peer closes),
   row `in_processing = 0`, `attempts = 0`; the log line reads `Processed 1 pending posts (0 posted, 1 failed)`
   on a 441 stub run.
2. high_water: post 3 articles through a scratch nntp-server (non-default port), then `build/webserver -data
   <root> -write-active-file <file>` → the group line shows `3` as high water without `-repair-watermarks`.
3. Tree: `go test -race -count=20 -run FuTree ./internal/database/` green; the web thread-tree page of a scratch
   group whose replies arrive out of order (seed as `flWebSeed` does, but with a reply referencing a later
   article) answers 200 on 20 consecutive requests.
4. Drop path: `go test -race -count=5 -v -run 'FlDLDrop' ./internal/processor/` green and five tests listed (the -run pattern is case-sensitive).

## Progress

### Wave 0 + wave 1 (2026-10-15)
- `plan-small-fixes-postrun` from `testing-001` @ b45d053 (e2cf14d plan to wip). Four parallel slices, all reviewed.
- sfp-dl-drop-test: `worktree-agent-ab022f2e53acb41fc` (46e775b), merge 4c45a72. `commitThenProgress` +
  `downloadWaitCommitted` seam; an UpdateProgress error is now returned. Review MERGE; it caught a case-sensitive
  `-run` pattern in this plan's E2E step (fixed in the plan). Note: the not-committed message lost the range start.
- sfp-tree-order: `worktree-agent-a1ea50c74677a38f9` (8e08248), merge 91dbf16. `buildTreeFromOverviews` with
  root-first, ascending numbers, cycle guard, depth pass, stable sort; iterative `countDescendants`; extra kept:
  `GetCachedTree` links children in `sort_order` (cache hits had random sibling order) and skips self-parent rows.
  Review MERGE (minor: per-node seen-map allocation in countDescendants; performance only).
- sfp-high-water: `worktree-agent-ab4f66debde03f2f4` (13c2f03), merge 4596254. `high_water` raised in the batch
  stats UPDATE, never lowered (Q2). Review MERGE, no findings. Note for later: `RepairNewsgroupWatermarks` can still
  lower it after expiry (preloader, not in scope).
- sfp-postqueue-stop: `worktree-agent-a3ff11d10a9746df8` (f511886), merge afc66ea; inline 918b752 (a rejection
  completed just before the stop still counts; `%w` in postToProvider). `PostArticleContext` closes the raw socket
  through `context.AfterFunc` and calls `ForceCloseConn` (DEVIATION from D1's bare `closing.Store`: the reviewer
  proved by mutation that D1's version leaks the pool slot); PosterManager ctx; stop checks between pools; a
  failed-after-stop entry is reset, not counted; "Processed N (P posted, F failed)". Review MERGE. Leftover: a stop
  during a fresh connect (dial/handshake, `SwitchMode`) still waits for the connect timeout (GetContext watches
  the ctx only while waiting for a slot).
- Merged tree 918b752: gofmt clean, vet (+e2e tag) and build ok, race suite over 28 package sets ok in 257 s wall
  (no flakes), known bugs unchanged (T-nntp-article-1).

## Open questions (defaults apply unless overridden at the wave-1 boundary)
- Q1: on stop mid-POST the article may be posted twice if only the 240 was lost; accept (next run gets the 441
  duplicate) rather than tracking "sent" state? default yes.
- Q2: `high_water` only ever grows (CASE WHEN greater); a shrunk group after expiry keeps its old high water,
  as NNTP semantics expect? default yes.

## Outcome (2026-10-15)
- **Integration branch:** `plan-small-fixes-postrun` from `testing-001` @ b45d053. Not yet merged into `testing-001`
  (user decision). Nothing pushed.
- **Slices (4, one wave):** sfp-dl-drop-test 4c45a72, sfp-tree-order 91dbf16, sfp-high-water 4596254,
  sfp-postqueue-stop afc66ea (+ inline 918b752). Worktrees `/tank0/claude/trees/go-pugleaf/agent-{ab022f2e53acb41fc,
  a1ea50c74677a38f9,ab4f66debde03f2f4,a3ff11d10a9746df8}` (branches `worktree-agent-<id>`); the user cleans them up.
- **Verification (verifier on 0a5e90f, code 918b752):** gofmt/vet/build clean; race suite over 28 package sets in
  193 s wall, no flakes; flake pass `-count=5 -cpu 1,4` 900 PASS / 0 FAIL; known bug T-nntp-article-1 only; E2E:
  SIGINT during a POST to a stub that never replies cut the socket within ~10 ms and exited 130 after 2.3 s (the rest
  is the database teardown), row back to pending with attempts 0; a 441 run logs `Processed 1 pending posts
  (0 posted, 1 failed)`; three posts through a scratch nntp-server gave `high_water 3` and an active-file line
  `test.sfp.hw 3 1 y` without `-repair-watermarks`; the thread-tree page of an out-of-order thread with an in-thread
  orphan answered 200 on 20 cached and 20 fresh builds with identical structure (orphan under the root at depth 1,
  deepest reply at depth 3); the five FlDLDrop tests listed and green `-count=5`.
- **Design deviation (correct):** `PostArticleContext` calls `ForceCloseConn` inside the `context.AfterFunc` instead
  of the bare `closing.Store(true)` the plan wrote; the reviewer's mutation run showed the plan's version leaks the
  pool slot.
- **Leftovers:** a stop during a fresh connect (dial/handshake/`SwitchMode`) still waits for the connect timeout
  (`GetContext` watches the ctx only while waiting for a slot); `RepairNewsgroupWatermarks` can lower `high_water`
  after expiry (preloader; Q2 says it should only grow on intake, the repair is a deliberate recompute);
  `countDescendants` allocates a seen-map per node (performance only); the not-committed log lost the range start;
  an article whose only reference is unknown starts its own thread at intake (threading, not the tree builder), so
  the "orphan under the root" case only arises for in-thread orphans; the `flWebSeed` comment in
  `internal/web/fl_web_tree_test.go` about avoiding orphans is stale.

