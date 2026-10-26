# Plan: fetcher-hostile-peer — client, pool, fetcher and server against the kit's new faults

## Context
nntp-testkit v0.5.0 (go.mod bump 789a2b5, 2026-10-23) brought options none of our tests use: the fault hooks
`Drip`, `LineDelay`, `Endless`, `LongLine`, `OverrideLine`, `OverrideFrom`, `OverrideTimes`; server `MaxConns` /
`MaxConnsGreeting`; the Store's generated groups (`AddGenerated`, `Generator`, `NumbersIn`, `GenLimit`,
`SetGeneratedHigh`); `StoreOptions.GroupNumbers` with `HighestExisting` (lagging frontends), `OverviewFields`
(overview out of step with the articles), `ListgroupRange`, `EmptyOverReply`, `MaxOverRange` /
`OverRangeTooLarge`; and `proc.Options.Ulimit`. Our fake-server tests (`internal/nntp/nntp-client-*_test.go`,
aliases in `nntp-client-fake_test.go`) use only Options, Step, Store and the four original faults.

What exists on our side and is untested in these shapes:
- Client read bounds: `BackendConfig.MaxResponseLineBytes` (`DefaultClientMaxLineBytes`, sticky
  `ErrResponseLineTooLong` in `nntp-client-conn.go`), per-Read idle `ReadTimeout` (`DefaultClientReadTimeout` 2 min),
  per-command line caps `MaxReadLines*` and the octet budget (`ErrResponseTooLarge`) in
  `nntp-client-commands.go` (~L790-860), XHDR's "more lines than requested" check.
- Pool (`nntp-backend-pool.go`): `SelectGroup` turns a 480 into `ErrAuthRequired` and closes the connection;
  **nothing outside `internal/nntp` handles `ErrAuthRequired`** (no callers in processor or cmd). A 502 or 400
  greeting on connect is handled by `Connect` in `nntp-client.go` in some way the tests do not pin down.
- Fetcher: `Processor.DownloadArticles` / `DownloadArticlesFromDate` (`internal/processor/proc_DLArt.go`) drive
  XOVER/XHDR then ARTICLE through the pool; progress lives in `database.ProgressDB`. No processor test runs
  against a fake server (`fakeserver` is not imported under `internal/processor` or `cmd`).
- Server: `nntp-server.go` accept loop (~L340-372, writes "400 Too many connections" over its own cap); no test runs
  the server under an fd limit. `proc` from the kit is not used anywhere in go-pugleaf.

Decisions already made that bind this plan: no timing assertions ([[no-timing-tests]]: deadlines only as hang
guards, `PUGLEAF_TEST_DEADLINE`/`hDeadline`/`cliWait`); the hardcoded provider caps stay; tests use scratch data
roots only; kit changes need a new tag (never a `replace`); the kit's queued `nntp-testkit-conformance` plan is a
different thing (black-box server checks) and is not touched here.

## Design
Tests first, fixes only where a test shows wrong behaviour. Every fix is minimal, lands in the slice that found it,
and is named in the slice report; a fix outside the slice's owned files is reported as a leftover instead.

Expected behaviours (the tests assert these; a deviation is a finding, not a reason to weaken the test):
1. **Bounded reads.** A reply line over the cap → `ErrResponseLineTooLong`, the connection is unusable afterwards
   and the pool does not hand it out again. An endless multi-line reply → `ErrResponseTooLarge` (or the
   `MaxReadLines*` error) before the kit's Deadline, never a hang. A dripped reply that completes within
   `ReadTimeout` succeeds unchanged (the idle timeout is per Read, not per reply); a `LineDelay` longer than a
   short test `ReadTimeout` fails with a timeout error and the connection is closed. The greeting is subject to the
   same line cap.
2. **Session faults.** A failing N-th ARTICLE (`OverrideFrom`, `OverrideTimes`) is reported for that article only;
   the pool connection is dropped or kept according to the reply class (4xx keeps, 5xx/EOF drops) and the next
   request works on a fresh connection. A 480 on an authenticated connection (kit: ends the login) → the pool
   closes that connection and a new `Get` dials and authenticates again; `ErrAuthRequired` surfaces to the caller
   once, the fetcher logs it and does not loop. `MaxConns` on the fake with a larger pool cap → the extra connect
   returns an error naming the 502 greeting, is not retried in a tight loop, is not counted as an open pool
   connection, and after a `Put` the next `Get` succeeds.
3. **Provider quirks.** Generated group of 100k+ articles: `Pool.XOver` with `enforceLimit` splits or caps the span
   so that a server `OverRangeTooLarge` 503 never aborts a run that stays inside the limit; `ListGroup` with
   `ListgroupRange: "reject"` falls back or errors cleanly (never treats the 501 as an empty group);
   `EmptyOverReply` (a 224 with no lines) is an empty overview, not an error. `GroupNumbers` reporting
   "211 0 0 0" or a High below what the frontend already fetched → `SelectGroup` returns the numbers as given and
   the fetcher **never rewinds** progress or deletes anything because of it (A-xref/progress rules). `OverviewFields`
   rewriting Message-ID or number → the article is filed under its own Message-ID header (overview values are
   hints), and the mismatch is logged once per group, not per article.
4. **Server under fd pressure.** With `ulimit -n` low, the server keeps serving the connections it has, logs the
   accept error with a backoff (no busy loop: the accept loop sleeps on `EMFILE`-class errors), and accepts again
   once connections close. Clean shutdown still works.

Harness additions (wave 0): aliases in `nntp-client-fake_test.go` for `fakeserver.Store`, `StoreOptions`,
`Faults`, `Generator`, `NewStore`, `StoreHandler`, `GeneratedArticle` under the existing `cliFake*` naming; a
`cliStoreServer(t, st *Store, so StoreOptions, f Faults) *cliFakeServer` helper. Slices then only add new files.

## Waves
### Wave 0 (inline)
- `internal/nntp/nntp-client-fake_test.go`: the aliases and `cliStoreServer` helper above. Commit on the plan branch.

### Wave 1 (parallel, all client-side, each slice its own new test file)
- slice `client-read-bounds`: `internal/nntp/nntp-client-hostile_test.go` (new). Faults `LongLine` on greeting,
  ARTICLE, XOVER, LIST; `Endless` on ARTICLE, BODY, XOVER, LISTGROUP, LIST ACTIVE; `Drip` on GROUP/ARTICLE with a
  reply that completes; `LineDelay` beyond a short `ReadTimeout` (300 ms class, as the existing connect-timeout
  tests do). Assert the error classes of design point 1 and that the pool never hands out the poisoned connection
  (`Pool.Get` after the failure dials anew: `s.Accepted()` grows). Owned: that file only; a needed fix in
  `nntp-client-conn.go`/`nntp-client-commands.go` is allowed but must be listed in the report.
- slice `client-session-faults`: `internal/nntp/nntp-client-session_test.go` (new). `OverrideFrom`/`OverrideTimes`
  on ARTICLE (503 then normal; EOF via `CloseAfter`); Store with `User/Pass` plus `Override["ARTICLE"]=480` with
  `OverrideTimes 1` → pool behaviour of design point 2; `Options.MaxConns: 1` with `cliBackend` MaxConns 2 → the 502
  greeting path. Owned: the new file plus `internal/nntp/nntp-backend-pool.go` and the `Connect` greeting handling
  in `internal/nntp/nntp-client.go` for fixes.
- slice `client-provider-quirks`: `internal/nntp/nntp-client-quirks_test.go` (new). Generated group
  (`AddGenerated`, 150k articles, default `GenLimit`) with `Pool.XOver` spans above and below the limit and
  `OverRangeTooLarge` set and unset; `ListgroupRange` "reject" and "ignore"; `EmptyOverReply`; `GroupNumbers`
  with `HighestExisting` and with a constant "0 0 0"; `OverviewFields` swapping Message-ID and number on every
  line → `XOver` returns what the server said (the client does not repair), `nntp-client-validate.go` rules
  documented in the test. Owned: the new file; fixes in `nntp-client-commands.go` XOVER/LISTGROUP paths listed.

### Wave 2 (after wave 1 is merged; depends on the pool behaviour pinned in wave 1)
- slice `fetcher-download-fake`: `internal/processor/proc_DLArt_fake_test.go` (new) using a scratch DB (pattern of
  `postCommitSetup` in `post_commit_test.go` / `fl_nntpdb_commit_test.go`) and a kit Store server as the provider
  (`nntp.NewPool` on a `BackendConfig` for the fake). Scenarios: a normal run over a generated group of 2k articles
  completes and the progress DB holds the High; `GroupNumbers` "0 0 0" on the next run → no rewind, no delete,
  one log line; `OverviewFields` mismatch → articles filed under their own Message-ID, count matches;
  `Endless`/`LongLine` on one `OverrideLine` ("ARTICLE <id>") → that article is skipped or retried bounded, the
  run finishes, no goroutine leak (compare `runtime.NumGoroutine` before/after with the kit's Deadline as the
  hang guard only); 480 mid-run → run ends with `ErrAuthRequired` in the error, the pool is clean, a re-run after
  the fake accepts again works. Owned: the new test file, `internal/processor/proc_DLArt.go` for minimal fixes
  (an `ErrAuthRequired` branch that stops the group run instead of retrying every article is expected).
- slice `server-ulimit`: `cmd/nntp-server/e2e_ulimit_test.go` (new). Builds the server binary with
  `go build -o <t.TempDir()>/pugleaf-nntp-server ./cmd/nntp-server` (never a `build_*.sh` from a test), starts it
  through `proc.Start`/`proc.New` with `Ulimit: []string{"-n 24"}` on a scratch data root under `t.TempDir()`
  (`-data`, `-nntphostname`, `-nntpbind 127.0.0.1`, a non-default `-nntptcpport`, `-nntptlsport 0`, `-maxconnections` high), opens connections until accept fails, asserts design point 4 from
  the existing connections' replies and the log file (`proc.Options.Logs`), closes half, asserts new connections
  are accepted again, then a clean stop. Skips on Windows and when `/bin/sh` is missing. Owned: the new file,
  `internal/nntp/nntp-server.go` accept loop for the backoff if missing.

## Checks
```
gofmt -l internal/nntp internal/processor cmd/nntp-server
go vet ./... && go build ./...
go test -race ./internal/nntp/... ./internal/processor/... ./cmd/nntp-server/... ./cmd/nntp-fetcher/...
PUGLEAF_TEST_DEADLINE=120s go test -race -count=1 -run 'Hostile|Session|Quirk|DLArtFake|Ulimit' ./internal/nntp/ ./internal/processor/ ./cmd/nntp-server/
PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/   # unchanged set of known bugs, or a marker removed on purpose
```
plus the full CLAUDE.md list on the merged tree. No wall-clock assertions anywhere; deadlines are hang guards.

## End-to-end
1. `./build_fetcher.sh` and `./build_nntp-server.sh` succeed.
2. The verifier runs the wave-2 processor test with `-v` and `PUGLEAF_TEST_DEADLINE=180s` three times
   (`-count=3`) to show it is stable under `-race`.
3. `cmd/nntp-server` under ulimit by hand: `sh -c 'ulimit -n 24; exec ./build/pugleaf-nntp-server -data ./data-test-hostile -nntphostname example.org -nntpbind 127.0.0.1 -nntptcpport 11129 -nntptlsport 0 -maxconnections 100 -history=false'
   (the hostname must resolve: `validateHostname` does a DNS lookup; 11119 is taken on this machine by an unrelated nzbrex server)`
   (flags as the unit test uses them), 40 parallel `nc` connections: the first ones get a greeting and answer
   `CAPABILITIES`, the log shows an accept error with backoff and no log flood (fewer than 20 accept lines per
   second), after the `nc`s exit a new connection is served; `kill -TERM` stops it within the shutdown grace.
4. Report: which design points held without a fix, which fixes were needed (file, behaviour before/after), and
   leftovers (e.g. an `ErrAuthRequired` strategy for the fetcher beyond "stop the group run" if the slice found
   more is needed).

## Progress
### Wave 0 (2026-10-23)
- 2bc45a4 aliases + `cliStoreServer` in `nntp-client-fake_test.go`.
### Wave 1 (2026-10-23)
- `worktree-agent-adde089f8f273c94e` (client-provider-quirks, 1a694aa) merged 7bf42ca; `worktree-agent-a4b7ff0de42898ef7`
  (client-session-faults, 8950264) merged 22d0627; `worktree-agent-af94cd37b92ea42f2` (client-read-bounds, f85737a) merged 00ee7c8.
  All three tests-only; every design-point behaviour held except the 480 mapping.
- Inline on the plan branch: 236f2ae (capped XOver keeps its connection), a5f9f82 (**480 on any reader command is
  `ErrAuthRequired`**: `authRequiredErr`, `AuthRequired` constant; the session test asserts it), 2b469e4 (line-limit error
  wraps `ErrResponseTooLarge`; pool variant of the endless reply; smaller drip).
- Pinned current behaviour, not changed: `Pool.XOver` with enforceLimit caps the span to `MaxReadLinesXover` (100) and never
  splits; a 503 on XOVER force-closes the connection although the session is clean; `parseOverviewLine`/`ListGroup` drop
  unusable lines silently (XOver returns 0 lines, nil error); a 423 on ARTICLE drops the connection (only 430/451 keep it);
  `BackendConn.ListGroup` has no production caller; `Pool.XHdr` force-closes on a 423 (no `ErrNoArticlesInRange` wrapping).
- Leftovers (not this plan's fixes): duplicated `cli*Run`/`cli*Pool` helpers per test file; a plain read timeout on a bare
  `BackendConn` closes nothing (unreachable in production, every pool helper force-closes); `TestCliConnReadIdleTimeout`
  keeps a lower-bound elapsed check (`< 250 ms` → fatal) from test-timing-hardening; the kit applies no Faults to the
  greeting (the test uses `Options.Greeting`).
- Wave 2 must know: overview values come back unrepaired; `OverrideFrom`/`OverrideTimes` are per connection (use
  OverrideFrom >= 2 where the pool re-dials); `Pool.Stats` settles asynchronously after `ForceCloseConn` (poll it).
### Wave 2 (2026-10-23)
- `worktree-agent-aa1e5a192d81b5044` (server-ulimit, c806733 + 4de6430) merged e52fd9b; `worktree-agent-a7fd731beba22a4b7`
  (fetcher-download-fake, c7caf41) merged 368e1ea. Inline: 9241f24 (recovery line only after a real burst; probe sockets
  cleaned on Fatalf), 7ae38c1 (**cmd/nntp-fetcher worker read `*item.MessageID`/`*item.GroupName` after `item.ReturnQ <- item`:
  nil dereference on a failed article**, review major; `fetchedMsgIDHeader` via `common.HeaderValue`).
- Production changes of the wave: `nntp-server.go` accept loop: exponential backoff 5 ms..1 s with a rate-limited error line
  and a recovery line (before: flat 50 ms, 20 log lines/s during an fd outage). `proc_DLArt.go`: data race in
  `feedDownloadRun` (`item.Error` read after the send; a duplicate could leave `gotQueued` too high and the run end only by
  the death counter), `ErrAuthRequired` stops the group run before the progress is touched (before: nil return, every
  remaining article re-dialled + re-authenticated, progress moved past articles never fetched), Message-ID mismatch
  pre-check (one log line per run, article dropped, cache entry released).
- Pinned decisions: an ARTICLE whose Message-ID header differs from the requested id is **dropped**, not re-filed under its
  own id (A-intake-3: the claim/dedup was taken on the requested id; re-filing would let a provider inject articles under
  ids never asked for) — the design-point-3 wording "filed under its own Message-ID header" is superseded. Blank-subject
  articles are rejected per article by `ProcessArticle` (unchanged). `DownloadArticles` uses GROUP + XHDR Message-ID +
  ARTICLE, never XOVER, so the wave-1 `MaxReadLinesXover` cap is irrelevant to it; XHDR streams in batches of 1000.
- Kit leftover (new tag needed): `fakeserver.StoreHandler` has no XHDR (answers 500), so no fetcher run can use Store mode;
  the slice wrote a Handler-mode fake (`proc_DLArt_fake_srv_test.go`) with its own hooks. An `XhdrFields` hook mirroring
  `OverviewFields` would be the natural addition.
- Leftovers (decisions, not this plan's fixes): `commitThenProgress` moves the progress to `end` whenever anything was
  queued, so skipped/errored/mismatched articles are never fetched again (a provider out of step silently loses a range;
  the mismatch drop adds a new way to do this); after an auth abort up to ~queue depth (128) more ARTICLEs are still sent
  and the XHDR stream for the rest of the span keeps one connection until it ends; `cmd/nntp-fetcher` does not inspect
  `ErrAuthRequired`, so a dead login still walks every group once per run (bounded, noisy); the ulimit test has a fixed
  budget (`-n 24`, 14 served here) and fails rather than skips on an odd fd layout.

## Outcome (2026-10-24)
- Integration branch `plan-fetcher-hostile-peer` (20 commits from testing-001 c8c9d41-era base, 14 files, +2810/-15),
  merged into `testing-001` with `--no-ff` as **b6cc911** after the user's OK. Not pushed.
- Wave merges: 7bf42ca (client-provider-quirks), 22d0627 (client-session-faults), 00ee7c8 (client-read-bounds),
  e52fd9b (server-ulimit), 368e1ea (fetcher-download-fake). Inline fixes on the branch: a5f9f82/2b469e4 (480 →
  `ErrAuthRequired` on every reader command, `ErrResponseTooLarge` on the line limit), 9241f24, 7ae38c1, 236f2ae,
  4af4051 (end-to-end command corrected: resolvable hostname, port 11129, `-history=false`).
- Worktrees (user cleans up), all under `/tank0/claude/trees/go-pugleaf/`: `agent-adde089f8f273c94e`
  (`worktree-agent-adde089f8f273c94e`, client-provider-quirks), `agent-a4b7ff0de42898ef7`
  (`worktree-agent-a4b7ff0de42898ef7`, client-session-faults), `agent-af94cd37b92ea42f2`
  (`worktree-agent-af94cd37b92ea42f2`, client-read-bounds), `agent-aa1e5a192d81b5044`
  (`worktree-agent-aa1e5a192d81b5044`, server-ulimit), `agent-a7fd731beba22a4b7`
  (`worktree-agent-a7fd731beba22a4b7`, fetcher-download-fake).
- Verification (pugleaf-verifier on the branch, all PASS): gofmt/vet/build, the full CLAUDE.md test list (30 packages)
  under `-race`, `PUGLEAF_KNOWN_BUGS=1` = exactly the one accepted marker (T-nntp-article-1, renamed
  `nntp-article-430-cache-delay` in testing-001 507021e), `build_fetcher.sh` + `build_nntp-server.sh`, `TestDLArtFake*`
  stable with `-count=3`, hand-run ulimit scenario (`-n 24`: 14/40 connections served, ~1 accept log line/s, one recovery
  line, a new connection served afterwards, SIGTERM exit 0).
- Production fixes shipped: 480 mapped on all reader commands; fetcher data race on duplicate hand-off; fetcher worker
  nil dereference after returning an item; auth failure mid-run aborts the group run with progress untouched; Message-ID
  mismatch dropped with one log line per run; accept loop exponential backoff with rate-limited logging; line-limit error
  wraps `ErrResponseTooLarge`.
- Leftovers / decisions (see wave notes above): progress still advances over skipped and mismatched articles; bounded
  over-fetch after an auth abort; `cmd/nntp-fetcher` ignores `ErrAuthRequired` (dead login walks every group once per run);
  kit `StoreHandler` lacks XHDR (hand-written Handler fake stays until a kit tag adds it; v0.7.0 still has none);
  duplicated `cli*Run`/`cli*Pool` test helpers; `TestCliConnReadIdleTimeout` lower-bound elapsed check; ulimit test fails
  rather than skips on an odd fd layout; `Pool.XHdr` force-closes on 423. Pinned: XOVER caps at 100 lines and never
  splits; 503 on XOVER force-closes; unusable overview lines dropped silently; 423 on ARTICLE drops the connection;
  mismatched articles dropped, not re-filed.
