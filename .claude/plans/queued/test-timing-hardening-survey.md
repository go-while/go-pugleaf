# Survey: wall-clock dependent tests (input for plan "test-timing-hardening")

Repo `go-pugleaf`, branch `plan-adf-optional`, HEAD `b25df6c`. This survey was read-only: 123 `*_test.go` files under `internal/` and `cmd/`, 77 of them with a timing hit.
Rules applied: (a) an upper bound needs a margin of at least 10x against what it measures, and a structural assertion is preferred; (b) lower bounds are OK; (c) hang guards must be 30 s or more; (d) delete `TestNSHAuthUnknownUserTiming`.

**Race list (CLAUDE.md):** `internal/database/...` (includes `adfreopen`), `internal/web/...`, `internal/history/...`, `internal/nntp/...`, `internal/processor/...`, `cmd/expire-news/...`, `cmd/history-rebuild/...` (no timing hits), `cmd/nntp-server/...`.
**Not in the race list:** `internal/postmgr`, `internal/cache`, `cmd/tcp2tor`, `cmd/web`.

Facts that change several rewrites:
- `rateLimitOnError` (`internal/nntp/nntp-cmd-helpers.go:8`) and the ArticleCmdDelay sleeps (`nntp-article-common.go:47`, `nntp-cmd-nextlast.go:46`, `nntp-cmd-over-hdr.go:117,240`) are plain `time.Sleep`. Nothing counts them, so a structural "was the delay applied" check needs a small production counter (proposal P1 below).
- `DefaultArticleCmdDelay` is **25 ms**, not 200 ms as the comment in `TestArtNoDelay` says. So the regression it guards against (50 x 25 ms = 1.25 s) is only 1.25x above its 1 s bound.
- Client `Connect` wraps its errors with `%w` (`nntp-client.go:275,352,378`; `proxy.go:70,72`). A structural `errors.As(err, &net.Error) && Timeout()` check is possible. The SOCKS path must be verified.
- `cronDBEvery` (`internal/database/database.go:31`) only exits on its next 10 s tick.

---

## 1. Upper-bound duration assertions

| file:line | test | measured | configured | bound | margin | proposed fix |
|---|---|---|---|---|---|---|
| internal/nntp/nntp-client-conn_test.go:100 | TestCliConnGreetingTimeout | Connect, server never greets | ConnectTimeout 300ms | <2s | 6.7x | Drop the bound. Assert `errors.As(err,&ne)&&ne.Timeout()`. The hang guard is cliConnOuter (raise to 60s, cat. 3). |
| nntp-client-conn_test.go:131 | TestCliConnAuthFailureClosesSocket/no-backoff | Connect after 481 | AuthFailureBackoff -1 (default 15s) | <1s | ~1000x vs real, 15x vs failure | Bound 5s (still 3x under the 15s default), or a hook counter on the back-off sleep. |
| nntp-client-conn_test.go:178 | TestCliConnTLSHandshakeTimeout | TLS handshake against a stalled server | ConnectTimeout 300ms | <2s | 6.7x | Drop the bound. Assert "TLS handshake failed" (already done) plus a timeout `net.Error`. |
| nntp-client-conn_test.go:197 | TestCliConnTLSUnknownCA | Rejection of an unknown CA | Backend() ConnectTimeout **2s** | <2s | 1x vs config | Drop the bound. The error string check is the assertion. |
| nntp-client-conn_test.go:224 | TestCliConnReadIdleTimeout | GetArticle on a hung reply | ReadTimeout 300ms | <2s | 6.7x | Drop the upper bound. Keep the lower bound at :227 and add a timeout `net.Error` check. |
| nntp-client-conn_test.go:342 | TestCliConnForceCloseIdempotent | Get #3 wait | PoolWaitTimeout 200ms | 150ms..2s | 10x | Keep the lower bound. Drop the upper bound: the "timeout" error text is checked and cliConnOuter guards. |
| nntp-client-conn_test.go:372 | TestCliConnGetContextCancel | GetContext | ctx 100ms | <1s | 10x | Drop the bound. `errors.Is(DeadlineExceeded)` is already asserted. |
| nntp-client-cmds_test.go:558 | TestCliCmdSOCKSTimeout | Connect through a stalled SOCKS proxy | ConnectTimeout 300ms | <2s | 6.7x | Drop the bound. Assert a timeout error (verify the wrapping in x/net/proxy). The guard is cliCmdOuter. |
| nntp-rfc-stream_test.go:301 | TestStmPipelined | 50 pipelined 438 replies | ErrorDelay 200ms (a delay would cost 10s) | <1s | 10x vs failure, tight vs -race | P1 counter: error-delay count delta == 0. Fallback: ErrorDelay = 10 min on this server, so any delay trips the 60s client deadline. |
| nntp-rfc-starttls_test.go:259 | TestStlsRefusals/error-delay | 502 reply | ErrorDelay 300ms | <150ms | 2x | P1 counter: delta 0 after 502, 1 after 501. The :264 lower bound may stay. |
| nntp-rfc-reader_test.go:211,215 | TestRdrNextLastDelays | 422/421 replies | ErrorDelay 300ms | <150ms | 2x | P1 counter, delta 0. The :207 lower bound stays. |
| nntp-rfc-lists_test.go:196 | TestLstListWildmat | LIST ACTIVE wildmat (a DB query) | ErrorDelay 300ms | <300ms | 1x | P1 counter. This one is the most likely to flake. |
| nntp-rfc-lists_test.go:330 | TestLstNewGroups | NEWGROUPS 231 | ErrorDelay 300ms | <300ms | 1x | P1 counter. |
| nntp-rfc-overview_test.go:460,466,486 | TestOvhLargeRange | OVER/HDR over 2500 rows, open ranges | none | 10s each | unknown | Drop all three. The regression (a walk over 2^63 numbers) never ends, so the central client deadline catches it. The line counts and ordering are the assertion. |
| nntp-server-article_test.go:928 | TestArtNoDelay | 50 STATs | ArticleCmdDelay 0 (default **25ms**) | <1s | 1.25x vs failure | P1 counter (article-delay count == 0), or drop it: :913 already asserts `ArticleCmdDelay == 0`. Fix the stale "200 ms" comment. |
| nntp-server-harness_test.go:842 | TestHarnessIsolatedTimeout | Kill of a hung child | timeout 3s + WaitDelay 2s, child sleeps 30s | <=8s | 1.6x | Child blocks for 10 min (or `select{}`), timeout 3s, bound `took < 60s` (20x over the timeout, 10x under the child's sleep). The "timed out" error check is structural. |
| nntp-wildmat_test.go:165 | FuzzLstWildmat (seeds run in go test) | wildmatMatch on <=512 B | none | 1s | huge but stall-sensitive | Raise to 10s, or replace with a step counter (production change in `wildmatGlob`). |
| nntp-server-session_test.go:795 | TestSessStop | `hs.S.Stop()` | none | <2s | ms vs 2s | Run Stop in a goroutine under the central guard and drop the 2s bound. Also raise the listener poll (:801, 2s) and sessExpectClosed (:817, 2s) to the guard. |
| nntp-server-overview_test.go:664 | TestOvrHugeRange | "." per XHDR/XOVER huge range | c.Deadline 2s | 2s per cmd | n/a | Remove the 2s override and use the central deadline: the bug is an endless walk. Raise runIsolated 30s to 180s+. |
| nntp-server-session_test.go:691-697 | TestSessIdleTimeout/activity-resets-timer | Gap between commands | IdleTimeout 500ms, gap 50ms | implicit 450ms stall | 10x | Meets rule (a). Optional: IdleTimeout 2s (40x). |
| nntp-server-article_test.go:829 (artPollStat223, used :886) | TestArtLocal430/ihave-arrival | STAT 223 after IHAVE 235 | local430 TTL 60s | 1s poll | 60x vs TTL | Structural: after 235 the entry is gone, so a single STAT must answer 223, or check `hs.S.local430` directly. If polling stays, use 5s max (TTL/12). **Do not lengthen** the :900 known-bug path: it always runs to the window. |
| internal/database/fu_batch_test.go:257 | TestFuBatchRetry2ThreadingFailureIsNotRetried | processNewsgroupBatch returns | grace 3s | took <= 1.5s | ms vs 1.5s | Set `retryGrace` to 5 min for this test and assert `took < 30s` (10x both ways), or assert that the "Failed1"/retry log line count is 0. |
| database/lo2_writer_test.go:161 | TestLo2WriterStatsRetry | Shutdown bound | grace 50ms, round 30ms (~80ms) | <2s | 25x | Meets rule (a). Optional structural form: `calls <= 4`. |
| database/lo2_writer_test.go:184-189 | TestLo2WriterBatchShutdownClock | Two `expired()` calls back to back | grace 30ms | implicit <30ms | n/a | Use a 1-minute grace for the "not expired yet" checks, plus a second clock with a 1ms grace and `Sleep(5ms)` for the "expired" check (lower bound). |
| database/w2_dbperf_test.go:175-184 | TestW2DBPerfTTLCacheConcurrent | >=100 gets inside a 200ms window | none | throughput | n/a | Count-based loop: invalidate until gets >= 100 (and >= N invalidations), under a 60s guard. |
| database/adfreopen/reopen_test.go:240 | TestAdfReopenAfterFailureAndShutdown | CronDB exit after Shutdown | 10s tick | 12s | 1.2x | Poll budget of 2 min (12x the tick). The pass path still ends at the first tick. |
| internal/web/lo1_core_test.go:271,274 | TestLo1CoreFinalFlushBounded | Shutdown with a locked main DB | shutdownGrace 5s | grace+3s = 8s | 1.6x | Structural: capture the log and require "[WEB]: API token usage flush did not finish within". Guard at `shutdownGrace+60s` (the regression is minutes long). |
| web/lo1_core_test.go:149 | TestLo1CoreShutdownCancelsInFlight | Shutdown return | ctx 200ms | 2s | 10x | Raise to the 60s guard. The `cancelled` channel is the structural check. |
| web/lo2_server_test.go:308 | TestLo2ServerOllamaSyncTimeout | Admin sync against a dead proxy | client Timeout 100ms (prod 15s) | 2s | 20x | Raise to 10s (100x, and still below the 15s default client). The `accepted` check stays. |
| internal/processor/intake_test.go:275 | TestIntakeProcCaptureLimiter | acquire after stop | ticker 100ms (intake.go:210) | <=200ms | 2x | Bound 1s (10x the tick), or drop it and keep the 5s guard (raised to 60s). |
| processor/intake_test.go:287 | TestIntakeProcCaptureLimiter | acquire(false) | non-blocking | <=50ms | stall-sensitive | Drop. The overflow counter (:290) is the structural assertion; a block would trip the guard. |
| internal/postmgr/adf_opt_postmgr_test.go:174 (+171) | TestAdfOptPostmgrRunStopsOnShutdown | Run returns after shutdown | pause 10s (postmgr.go:91) | <2s, guard 5s | 5x / 2x | Make the pause injectable (a field, default 10s). The test sets it to 1h and asserts return within 60s (60x). See also cat. 5. |
| cmd/tcp2tor/adf_tcp2tor_test.go:62 | TestAdfTorSelfTestTimeout | testSOCKS5Connection | timeoutSec 1 | <=3s | 3x | **Not reworked.** Bound 10s, plus a timeout-error check. |
| adf_tcp2tor_test.go:84 | TestAdfTorDialTimeout | dialThroughSOCKS | 500ms | <=2s | 4x | **Not reworked.** Bound 5s (10x), or a `DeadlineExceeded`/timeout error check. |
| adf_tcp2tor_test.go:123 | TestAdfTorDialTimeoutPlainDialer | dialThroughSOCKS, blocking dialer | 200ms | <=2s | 10x | Meets rule (a). `errors.Is(DeadlineExceeded)` is already asserted, so the bound can go. |
| cmd/tcp2tor/adf_opt_tcp2tor_test.go:637,640 | TestAdfOptTorStopDuringDial | Stop during a dial | Timeout 30s | 3s | 10x | **Verified, rule met.** |
| adf_opt_tcp2tor_test.go:366 | TestAdfOptTorListenStopServe | Serve returns after Stop | none | 1s | ms | Raise to the 30s guard. |
| adf_opt_tcp2tor_test.go:476/515 | IdleOneWayTraffic / HalfCloseLongReply | Chunk gap | IdleTimeout 1s, gap 100ms | implicit | 10x | **Verified, rule met.** |
| cmd/web/adf_opt_web_test.go:250 | TestAdfOptWebCheckpointBusy | Busy checkpoint | busy wait 200ms | 20s | 100x | Meets rule (a). |

## 2. Lower-bound delay assertions (fine as they are)

| file:line | test | lower bound vs configured | note |
|---|---|---|---|
| nntp-client-conn_test.go:159 | TestCliConnAuthFailureClosesSocket/backoff-after-close | >= 1s backoff | OK |
| nntp-client-conn_test.go:227 | TestCliConnReadIdleTimeout | >= 250ms vs ReadTimeout 300ms | OK |
| nntp-client-conn_test.go:342 | TestCliConnForceCloseIdempotent | >= 150ms vs PoolWait 200ms | OK |
| nntp-client-conn_test.go:391 | TestCliConnIdleExpire | Sleep 250ms > IdleExpire 100ms | OK |
| nntp-rfc-starttls_test.go:264 | TestStlsRefusals | >= 300ms = ErrorDelay | OK |
| nntp-rfc-lists_test.go:204, 339 | TestLstListWildmat, TestLstNewGroups | >= ErrorDelay | OK |
| nntp-rfc-reader_test.go:207 | TestRdrNextLastDelays | >= ErrorDelay | OK |
| nntp-server-auth_test.go:255 | TestAuthFailureDelay | min of 3 >= 50ms ErrorDelay | OK. The comment "not inflated by bcrypt" is stale: unknown users now run the dummy bcrypt compare. Weaker, but not flaky. |
| nntp-server-session_test.go:681 | TestSessIdleTimeout/closes-idle-client | >= 150ms vs 200ms idle | OK |
| internal/history/history_test.go:286 | TestHistoryCloseFlushesAndWaitGroup | >= 100ms vs checker busy 150ms | OK. Weak: Close may take 100ms anyway. Optional: a call counter in histTestChecker. |
| database/fu_batch_test.go:217 | TestFuBatchRetry1DropsBatchAfterShutdownGrace | took >= grace 3s | OK |
| database/lo2_writer_test.go:81 / :193 | RetryCapCountsBusyTime / BatchShutdownClock | Sleep 300ms > cap 100ms / Sleep 40ms > 30ms | OK |
| database/w2_dbperf_test.go:90 | TestW2DBPerfTTLCacheBasics | Sleep 5ms > TTL 1ms | OK |
| cmd/tcp2tor/adf_opt_tcp2tor_test.go:455 | TestAdfOptTorIdleTimeout | >= 300ms idle | OK |
| internal/web/lo1_handlers_test.go:351 | (post back-off test) | Sleep waitLeft+1 s, up to 11s real time | OK but slow. Optional: an injectable clock. |
| Clock-relative checks | rfc-reader:135, server-group:674, e2e_binary_rfc:140 (DATE +-5min); nntp-intake:313 (+-1min); lo2_forms:63/77, w1_auth:64 (session expiry, minutes of slack); intake_dl:156/253 (TTL in (0,5s]) | margins of minutes, or >1000x | OK |

## 3. Hang guards / event waits under 30 s

Raising these only slows down the failure path; passing tests return as soon as the event happens.

| where (central?) | value | uses | note / proposal |
|---|---|---|---|
| **internal/nntp harness** `hs.dial` -> `tryDial(5s)` nntp-server-harness_test.go:243 (central) | 5s DialTimeout + per-read hClient.Deadline | ~140 `hs.dial(t)` call sites | Add `hDeadline = hEnvDuration("PUGLEAF_TEST_DEADLINE", 60s)` in the harness and use it here. This one constant fixes most NNTP server tests. |
| Copies of the session-helper literal `Deadline: 5*time.Second` plus `DialTimeout 5s`: rfc-reader:63/67, rfc-lists:62/66, rfc-overview:67/71, rfc-stream:64/68, rfc-starttls:147/151, session:1553/1568 | 5s | 6 helpers | Replace the literal with `hDeadline` (one line per file). |
| sessDialRaw(addr, 5s) session:1173 (fuzz) | 5s | 1 | Use `hDeadline`. |
| Per-test `c.Deadline` overrides: harness:854 (2s), session:510 (2s), server-overview:664 (2s, an assertion, see cat. 1), session:622 (30s), server-auth:69-73 (20s floor in authLogin) | 2-30s | 5 | Remove the 2s overrides. The authLogin floor becomes redundant (remove it, or floor it at hDeadline). |
| `hs.tryDial(d)`: session:527 (2s), :733 (2s), :745 (1s), :805 (500ms) | <=2s | 4 | Clamp inside tryDial to `max(d, hDeadline)`, or pass hDeadline. |
| `sessExpectClosed(c, d)` session:62 | 1s / 2s / 3s | 11 calls | Clamp `d` up to hDeadline inside the helper. Every caller expects a close. |
| TLS handshake deadlines: starttls:172, rfccmds:183, rfccmds:536 (5s); starttls:304 (2s close) | 2-5s | 4 | hDeadline. |
| Server stop wait harness:218 (10s), fuzz cleanup session:1127 (10s), TestMain WG harness:107 (30s) | 10s | 2+1 | 30s+ (`hDeadline`). |
| Poll loops: session:750 (3s), :801 (2s), :1071 (2s); server-auth:482 (3s) | 2-3s | 4 | hDeadline. |
| `artPollStat223` article:831 | 1s | 2 (+1 known-bug path) | See cat. 1. Keep the known-bug path short. |
| runIsolated timeouts: many 60s, TestOvrHugeRange 30s | 30-60s | ~10 | Must stay above the inner deadline: use `>= 3*hDeadline`. |
| **internal/nntp client fake** (nntp-client-fake_test.go): `WaitOpen(t,n,2s)` :334 | 2s | 19 calls | Clamp `d` up to `cliWait` (60s) inside WaitOpen. |
| `cliFakeWaitLines` :659, `cliCmdWaitArticles` cmds:115 | 2s hardcoded | 7 + n | Use the constant `cliWait`. |
| `cliConnOuter` conn:21, `cliCmdOuter` cmds:20 | 5s | all client tests | Set both to `cliWait` (60s). |
| `Backend()` ConnectTimeout :286 | 2s (a production timeout on the happy path) | all fake-backed connects | 30s (= DefaultConnectTimeout). Tests that need a short timeout already set 300ms explicitly. |
| cliFakeTextDial :631/:635, TLS self-test :762/:768, fake Close :368, validate:22, demuxer conn:272 (2s), ForceClose poll conn:303 (2s) | 2-5s | ~8 | `cliWait`. Leave the deliberate short "must time out" reads (fake :736 150ms, :800 100ms) alone. |
| nntp-intake:107 (10s), :436 (5s); nntp-wiring:141 (10s); client-progress:49 ctx 10s | 5-10s | 4 | `hDeadline`. |
| nntp-client-e2e:223/227 (15s/60s) | env-gated | 2 | Optional. |
| **cmd/nntp-server** `e2eDial` server_e2e:436 | 10s | all in-process e2e tests | One constant `e2eDeadline = 60s`. Also e2eDialAddr callers (15s, env-gated binary tests). |
| `e2eCommitWait` server_e2e:57; e2eWaitFor(20s) in e2e_binary*.go | 20s | 7 + 5 | 60s. |
| TestMain srvWG server_e2e:218 (10s); :1914 post queue (10s) | 10s | 2 | 60s. |
| **internal/database**: adf_batch:150 (5s), :628 (2s), :644 (5s), polls :336/:395/:447/:462/:531 (10s); adf_cache:132/141 (1s); adf_lifecycle:110/120 (5s); w1_sqlite:274 (5s); lo2_writer:158 (10s), :303 (1s); adf_opt_progress:33 (10s); adfreopen:196/220 (5s) | 1-10s | ~17 sites in 8 files | A package-level `w0Wait = 60s` in the test main (no shared central helper exists today). Already OK: fu_batch slack 30s / :170 30s, fu_queries:359 30s, adf_opt_postqueue:270 60s, adfBatchWait 60s, adfreopen:63 30s. |
| **internal/web**: lo1_core:29 cleanup Shutdown ctx 5s (`t.Errorf` on ctx error), :212/:242/:333 ctx 5s (Shutdown error = fail), :131/:330 (10s), :149/:154 (2s), lo1CoreWaitInFlight :160 (2s), :165/:355 (5s), :343 (1s); lo1_handlers:104/162 (10s); lo2_server:341 (5s), :356 (3s); w1_server:69 (2s), :258/:296 ctx 5s, :308 (5s); adfCronWait adf_cron:117 (2s, 6 uses); adf_opt_admin:562 ctx 5s, :569 (2s); adf_post:232 (2s); fu_cron:127 (10s) | 1-10s | ~30 sites in 9 files | A package const `w0Wait = 60s` in testmain_test.go. Shutdown contexts must also be >= 30s: they are pass/fail, not only guards. |
| **internal/history**: histTestWaitIdle :47 (20s, 10 uses), :297 (5s), :411 (2s) | 2-20s | 12 | `histWait = 60s`. |
| **internal/processor**: `intakeDLTestWait` intake_dl:17 (1s, 8 uses); intake_test:256/278 (5s); wiring:40 (20s) | 1-20s | 11 | A single const at 60s. Leave the negative windows (intake_test:248 50ms, :266 30ms) as they are. |
| internal/postmgr:171 (5s); internal/cache adf_sanitized_cache:70 (2s); cmd/web adf_web:206 (10s); cmd/expire-news main_test:146 (10s) | 2-10s | 4 | 60s each. |
| **cmd/tcp2tor**: adfOptTorStart cleanup :199 (5s); adfOptTorExpectClosed callers :398 (2s), :643/:673 (2s); MaxConns :389 (2s), :410 (2s), :425 (3s); IdleTimeout :460 (2s); half-close read :559 (5s); StopClosesLive :664 (2s), :672 (3s); adf_tcp2tor:133/148 (2s) | 2-5s | ~12 | Const `torWait = 30s` for expected events. :455 (lower bound) and IdleTimeout's ExpectClosed (3s vs 300ms) can stay. :490/:534 are already 15s, read guards for 3s streams, so raise them to 30s. |

**Central-raise proposal.** No shared test-helper package exists, so add one constant per package in its existing test-main or harness file:
`const`/`var testWait = envDuration("PUGLEAF_TEST_DEADLINE", 60*time.Second)`.
Let the wait helpers clamp: `d = max(d, testWait)` in `WaitOpen`, `sessExpectClosed`, `tryDial`, `cliFakeWaitLines`, `adfCronWait`, `histTestWaitIdle`, `lo1CoreWaitInFlight`.
This raises about 60% of the sites without touching their call sites. The rest are literal `time.After(Ns)` and get a mechanical replacement.
An `internal/testutil` package (non-test, imported only from tests) is the alternative if one env-parsing copy is wanted.

**Exceptions: keep these short.**
- Negative observation windows ("must still be blocked or open"): intake_test:248/266, lo2_writer:295, history:272, adf_batch:404, fu_queries:339, client-conn steady loop :309, fake :736/:800, adf_cron:395, e2e:1276.
- The artPollStat223 known-bug path at article:900.

## 4. Statistical tests

| file:line | test | action |
|---|---|---|
| internal/database/nsh_auth_test.go:176-205 | TestNSHAuthUnknownUserTiming (min of 3 runs, unknown*2 < known) | **Delete** the function and its comment block. Keep the `time` import (still used at :124). Keep the dummy compare in `db_nntp_users.go:314`. |

No other test compares two measured durations. `TestAuthFailureDelay` is a lower bound against a configured value (cat. 2). `lo2_pool_bench_test.go` is a benchmark and runs only with `-bench`.

## 5. Sleep-based synchronisation

| file:line | test | sleep | risk | suggested sync |
|---|---|---|---|---|
| internal/postmgr/adf_opt_postmgr_test.go:161 | TestAdfOptPostmgrRunStopsOnShutdown | 200ms "Run is in its 10s pause" | Weak: if ProcessPendingPosts is still running, Run exits at `stopped()` and the interruptible pause is never exercised. | A test hook or counter for "entered sleep" (for example `pm.sleeping atomic.Bool`, or an injectable sleep func). Combine with the injectable pause (cat. 1). |
| internal/web/fu_cron_test.go:118 | TestFuCronStartJobLosesRaceToStopSnapshot | 50ms "reach startJob and park on lock" | Weak: it can pass without the interleaving. | Poll goroutine stacks for `startJob` blocked in `RWMutex.Lock` (the `adfReopenStacks` pattern), under the guard. |
| internal/web/lo1_core_test.go:298 | TestLo1CoreTokenUsageDuringDrain | Handler sleeps 300ms to land in the drain | Weak: under load the request may run before the flusher stopped. | Inside the handler, wait on `<-srv.tokenUsageDone` (closed when the flusher stops), with a guard. |
| cmd/tcp2tor/adf_opt_tcp2tor_test.go:631 | TestAdfOptTorStopDuringDial | 200ms "handler reaches the SOCKS5 handshake" | Weak | Make adfTorSilentProxy signal accepted conns on a channel, and wait on it. |
| internal/database/adf_opt_postqueue_test.go:261 | TestAdfOptPostQueueSelectRetry | 300ms before Commit, so the reader hits the lock | Weak: it can pass without a retry. | Require the "SQLite retry attempt" log line, as fu_queries_test:347 (E10c) does. |
| cmd/nntp-server/server_e2e_test.go:1276 | TestE2ESizeLimits | 1.5s observation window for a log line | A false pass if the line is late | Acceptable. Optional: 5s. |
| nntp-server-session_test.go:577 | TestSessLineLimit/1MiB-without-LF | 50ms before MemStats | Heuristic, with a 16 MiB slack | Acceptable. |
| lo1_db_test.go:302, w1_sqlite_test.go:165/201, w1_server_test.go:299, adf_batch_test.go:404, history_test.go:272, lo2_writer_test.go:149 | various | Stress windows, or "either order is fine" | n/a | Acceptable. |
| Poll loops with a sleep inside a deadline (fake:345/668, cmds:124, conn:312/319, adf_cache, adf_lifecycle, adf_batch, adfreopen, cache:87, expire-news:149, processor wiring:52 / intake_dl, web adf_cron/adf_opt_admin/lo2_server/lo1_core:91, auth:488, article:839, session:756/813/1074, e2e:753, client-e2e:157) | n/a | Proper polling | n/a | Acceptable. Only their budgets matter (cat. 3). |

## Counts

Per category, counted per assertion site / table row:

| category | count |
|---|---|
| 1. Upper-bound assertions | 39 rows (36 need a change; 6 verified compliant: lo2_writer:161, tcp2tor-opt :640 and :476/515, adf_tcp2tor:123, adf_opt_web:250, activity-resets) |
| 2. Lower-bound assertions | 16 rows (OK) plus clock-relative checks |
| 3. Hang guards / event waits under 30 s | about 150 literal sites, or about 300 counting the helper call sites. Central: the nntp harness (5s), client fake (2s / 5s), e2e (10s / 20s). |
| 4. Statistical tests | 1 (delete) |
| 5. Sleep-based synchronisation | 5 weak tests to fix, plus about 10 acceptable |

Per package, tests whose result depends on wall-clock time (cat. 1, 4 and 5 tests, plus tests whose only issue is a guard under 30s):

| package | race list | cat. 1 | cat. 4 | cat. 5 weak | guard <30s |
|---|---|---|---|---|---|
| internal/nntp | yes | 21 | 0 | 0 | almost all server and client tests (~140 dial sites, 19 WaitOpen, 5s outer guards) |
| cmd/nntp-server | yes | 0 | 0 | 0 | all e2e tests (e2eDial 10s, commit wait 20s) |
| internal/database (+adfreopen) | yes | 5 | 1 | 1 | ~12 tests |
| internal/web | yes | 3 | 0 | 2 | ~20 tests |
| internal/processor | yes | 2 (1 test) | 0 | 0 | ~8 tests |
| internal/history | yes | 0 | 0 | 0 | ~11 tests |
| cmd/expire-news | yes | 0 | 0 | 0 | 1 |
| cmd/history-rebuild | yes | 0 | 0 | 0 | 0 |
| internal/postmgr | no | 1 | 0 | 1 | 1 |
| cmd/tcp2tor | no | 4 (3 non-opt + ListenStopServe) | 0 | 1 | ~10 |
| internal/cache | no | 0 | 0 | 0 | 1 |
| cmd/web | no | 0 (1 compliant) | 0 | 0 | 1 |

---

## Plan skeleton: test-timing-hardening

**P1, optional production change, decided before slicing.** Add `errorDelays` and `articleDelays` atomic counters on `NNTPServer`, incremented in `rateLimitOnError` and in the 4 ArticleCmdDelay sites. This is one `atomic.Add` per error reply. It makes 7 nntp checks structural.
If P1 is rejected, the fallback is a 10-minute ErrorDelay on a dedicated server for the "no delay" replies, plus the central client deadline. The catch: a failure then blocks test cleanup for up to the delay, because `time.Sleep` cannot be interrupted.
Also optional: make the postmgr idle pause injectable (`PosterManager.idlePause`).

Slices (owned files are disjoint and can run in parallel):

1. **nntp-harness**: `internal/nntp/nntp-server-harness_test.go`, `nntp-server-session_test.go`, `nntp-server-auth_test.go`, `nntp-server-overview_test.go`, `nntp-server-article_test.go`, `nntp-server-rfccmds_test.go`, `nntp-server-group_test.go`, `nntp-intake_test.go`, `nntp-wiring_test.go`.
   - `hDeadline` and the env override, clamps in tryDial and sessExpectClosed.
   - TestHarnessIsolatedTimeout, TestSessStop, TestOvrHugeRange, TestArtNoDelay, artPollStat223.
   - Note: the session file header says "the harness is read-only"; the plan has to lift that.
2. **nntp-rfc** (depends on slice 1's `hDeadline` and on P1): `nntp-rfc-{stream,starttls,reader,lists,overview}_test.go`, `nntp-wildmat_test.go`, and P1 in `nntp-cmd-helpers.go`, `nntp-server.go`, `nntp-article-common.go`, `nntp-cmd-nextlast.go`, `nntp-cmd-over-hdr.go`.
3. **nntp-client**: `nntp-client-{fake,conn,cmds,validate,progress}_test.go`.
   - `cliWait` constant, the WaitOpen clamp, Backend() ConnectTimeout 30s.
   - Drop the 8 upper bounds in favour of timeout-error checks.
4. **database**: `nsh_auth_test.go` (delete the test), `fu_batch_test.go`, `lo2_writer_test.go`, `w2_dbperf_test.go`, `adfreopen/reopen_test.go`, `adf_opt_postqueue_test.go`, `adf_batch_test.go`, `adf_cache_test.go`, `adf_lifecycle_test.go`, `w1_sqlite_test.go`, `adf_opt_progress_test.go`, plus the wait constant in the package's test main (find which file holds `w0DB`).
5. **web**: `lo1_core_test.go`, `lo1_handlers_test.go`, `lo2_server_test.go`, `w1_server_test.go`, `adf_cron_test.go`, `adf_opt_admin_test.go`, `adf_post_test.go`, `fu_cron_test.go`, `testmain_test.go`.
6. **small packages**: `internal/processor/{intake,intake_dl,wiring}_test.go`, `internal/history/history_test.go`, `internal/postmgr/adf_opt_postmgr_test.go` (+ `postmgr.go` if the pause becomes injectable), `internal/cache/adf_sanitized_cache_test.go`, `cmd/web/adf_web_test.go`, `cmd/expire-news/main_test.go`.
7. **tcp2tor + e2e**: `cmd/tcp2tor/adf_tcp2tor_test.go`, `adf_opt_tcp2tor_test.go`, `cmd/nntp-server/server_e2e_test.go`, `e2e_binary_test.go`, `e2e_binary_rfc_test.go`.

**Order.** Wave A: slices 1, 3, 4, 5, 6, 7 in parallel. Wave B: slice 2, after slice 1 is merged (it needs `hDeadline`) and once the P1 decision is made.

**Checks per slice and at the end:**
```bash
gofmt -l <touched dirs>; go vet ./...; go build ./...
go test -race ./internal/database/... ./internal/web/... ./internal/history/... ./internal/nntp/... ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/...
go test -race ./internal/postmgr/ ./internal/cache/ ./cmd/tcp2tor/ ./cmd/web/
PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/   # known-bug list unchanged, still fails on purpose
```
- **Stress check for the verifier:** `go test -race -count=5 -cpu 1,4` on the touched packages, run in parallel with a CPU hog (for example `stress -c $(nproc)`, or a second `go test -race ./...`).
- **Wall time:** the `./internal/nntp/` package runtime should not grow by more than a few seconds. The known-bug path must not wait on a long guard.
