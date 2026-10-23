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
3. `cmd/nntp-server` under ulimit by hand: `sh -c 'ulimit -n 24; exec ./build/pugleaf-nntp-server -data ./data-test-hostile -nntphostname hostile.test -nntpbind 127.0.0.1 -nntptcpport 11119 -nntptlsport 0 -maxconnections 100'`
   (flags as the unit test uses them), 40 parallel `nc` connections: the first ones get a greeting and answer
   `CAPABILITIES`, the log shows an accept error with backoff and no log flood (fewer than 20 accept lines per
   second), after the `nc`s exit a new connection is served; `kill -TERM` stops it within the shutdown grace.
4. Report: which design points held without a fix, which fixes were needed (file, behaviour before/after), and
   leftovers (e.g. an `ErrAuthRequired` strategy for the fetcher beyond "stop the group run" if the slice found
   more is needed).
