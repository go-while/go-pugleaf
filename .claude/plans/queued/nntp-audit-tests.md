# Plan: NNTP server test suite + subagent audit (security, bugs, missing/wrong functions, RFC conformity)

- **Slug:** `nntp-audit-tests`
- **Integration branch:** `plan-nntp-audit-tests` (from `testing-001`)
- **Run with:** `/run-plan .claude/plans/queued/nntp-audit-tests.md`
- **Status:** approved on 2026-09-14 and queued. Nothing has run yet.
- **Parallelism:** wave 1 has 8 audit slices, wave 2 has 6 test slices, wave 4 has 4 fix-plan slices. Wave 2 doesn't depend on wave 1, so both can go out in one message if the user OKs 14 concurrent agents. Within a wave, no two slices own the same file.
- **Relation to the queued `web-sqlite-hardening.md` (WSH):** no shared files, so either plan can run first. The harness uses only APIs that WSH keeps stable (its contract K1).

---

## Context

### Request and decisions
- **User request:** subagents review the codebase, analyze its security, and find bugs, missing or obviously wrong functions and RFC non-conformity; tests are written for the NNTP server.
- **Decisions (asked during planning):**
  1. Findings go to `docs/audit/` in the repo. The repo is public: this plan never pushes, and the branch shouldn't be pushed before the security fixes land.
  2. Fixes are **not** implemented here. Wave 4 writes detailed fix plans into `.claude/plans/queued/`, at the level of detail of WSH.
  3. Web and DB auditing here is a **delta**. WSH already holds a traced review of `internal/web` and `internal/database` (ids C1–C6, H1–H12, P1–P8, S1–S4, L1–L7); this plan cites those as `WSH:<id>` and doesn't re-report them.
- **Preflight note:** the `.claude/` setup is committed (d1eca91). This plan file is untracked when queued, and worktrees are created from HEAD, so worktree agents can't read it until it is committed. Wave 0 therefore commits the plan file (now in `wip/`) together with the harness, before any agent is spawned.

### State at planning (`testing-001` @ 08c29f5)
- `go build`, `go vet` and `go test ./...` are green. The gofmt baseline lists 4 files: `db_groupdbs.go`, `db_init.go`, `embedded_migrations.go`, `web_admin_provider.go`.
- NNTP server tests today only cover IHAVE/TAKETHIS wiring, message-id lookup via `local430` (`internal/nntp/nntp-wiring_test.go`) and the parse helpers.
  - Untested: greeting, CAPABILITIES, MODE, AUTHINFO, GROUP, LISTGROUP, LIST, XOVER, XHDR, ARTICLE/HEAD/BODY/STAT by number, the accept loop, TLS, shutdown, and `cmd/nntp-server` end to end.
- Tools: `~/go/bin/{staticcheck 2025.1.1, govulncheck, golangci-lint v1.64.8}`. golangci-lint was built with Go 1.24, so it may not load this Go 1.25 module. `sqlite3`, `curl`, `nc` and `timeout` are installed. Python 3.13 has no `nntplib`, so NNTP clients are Go `net/textproto`.

### Seed findings (traced in code during planning; the audit confirms, rejects or extends them)

Ids here are canonical, and tests use them with `knownBug`. New audit findings get `A-<slug>-<n>`.

**NNTP server: security and DoS**
| ID | Where (at 08c29f5) | Defect |
|----|----|----|
| SEC-1 | `nntp/nntp-cmd-xhdr.go:54,97`, `database/queries.go:1740` | The DB read is clamped to start+1000, but the send loop walks the unclamped client range with no I/O. `XHDR subject 1-9223372036854775807` spins forever (the counter wraps at MaxInt64), pinning a core. No auth is needed. |
| SEC-2 | `nntp/nntp-cmd-group.go:49` → `database/db_groupdbs.go:101-171`; intake `processor/threading.go:232` → `database/db_batch.go:551-560` | `LISTGROUP <any name>` (no auth) and unknown `Newsgroups:` on intake create a directory, SQLite file and schema. Intake also upserts a `newsgroups` row with `active=1`. The web callers are WSH:C4. |
| SEC-3 | `nntp/nntp-server-cliconns.go:73`, `nntp/nntp-cmd-posting.go:224-233` | No byte limits. Command and article lines use unbounded `textproto.ReadLine`, and articles are capped only by line counts. `Server.NNTP.MaxArtSize` and `newsgroups.max_art_size` are never read. |
| SEC-4 | `nntp/nntp-server-cliconns.go:112,150` | `Stats.CommandExecuted(command)` stores any word before dispatch, and unknown commands aren't delayed, so the stats map grows without bound. |
| SEC-5 | `nntp/nntp-auth-manager.go:47-90`, `nntp/nntp-cmd-auth.go` | Reading never requires auth. `CheckGroupAccess`, `CheckConnectionLimit` and sessions are unused. AUTHINFO is accepted in plaintext (no STARTTLS, no 483), 481 has no delay, and a second AUTHINFO after success is accepted (RFC 4643: 502). |
| SEC-6 | `database/db_nntp_users.go:281-311`, `web/web_admin_nntp.go:190,277`, `web/web_admin_userfuncs.go:223`, `cmd/nntpmgr` | The auth cache (15 min) loads the user by ID **without `is_active`**. Deactivating a user or changing the password never invalidates it, so revoked users and old passwords keep working. |
| SEC-7 | `nntp/nntp-cmd-group.go:57`, `database/queries.go:621-640` | LISTGROUP loads every overview row of the group into memory and logs each call. |

**NNTP server: bugs and dead code**
| ID | Where | Defect |
|----|----|----|
| BUG-1 | `database/queries.go:821-843`, `nntp/nntp-article-common.go:58` | `GetArticleByNum` never sets `DBArtNum`, so ARTICLE/HEAD/BODY `<n>` set `currentArticle=0`. When it happens depends on the article cache. |
| BUG-2 | `database/queries.go:1678,1740` | XOVER and XHDR are silently truncated to 1001 rows, so readers see phantom gaps. |
| BUG-3 | `nntp/nntp-article-common.go:46`, `nntp/nntp-cmd-group.go:10`, `nntp/nntp-cmd-helpers.go:15` | Hardcoded sleeps: 200 ms per article command, 333 ms per GROUP, 1 s per error. That is about 5 articles/s per connection. |
| BUG-4 | `nntp/nntp-cmd-group.go:29-33,65`, `nntp/nntp-cmd-list.go:36-37` | GROUP reports low=1 and doesn't set the current article. LISTGROUP's 211 line lacks count/low/high/group, doesn't select the group, and ignores the range. LIST ACTIVE reports low=1 and status is always `n`. |
| BUG-5 | `nntp/nntp-cmd-helpers.go:67-79`, `common/headers.go:580-601` | Overview fields aren't sanitized for TAB/CR/LF (repeated headers are joined with LF), there is a trailing TAB, and no Xref. |
| BUG-6 | `nntp/nntp-cmd-helpers.go:19-22,48-64`, `nntp/nntp-article-common.go:229-248` | Header lines aren't dot-stuffed. Empty `headers_json` puts a blank line inside HEAD, and an empty body adds an extra blank line. |
| BUG-7 | `nntp/nntp-cmd-posting.go:227-229`, `nntp/nntp-cmd-basic.go:53-63`, `nntp/nntp-server.go:75,102-121,178-216` | "Too large" writes to a closed conn, and QUIT is logged as a connection error. `Stop()` neither closes clients nor waits (its wait is a no-op). `CronLocal430` leaks one goroutine per server, and a TLS cert failure leaks the plain listener. |
| BUG-8 | `nntp/nntp-article-common.go:129-155`, `nntp/nntp-cache-local.go` | `local430` isn't invalidated when the article arrives: a 430 is served for up to 75 s after 235/239/240. |
| BUG-9 | `nntp/nntp-server-cliconns.go:15,54-59` | The idle timeout is 60 s (RFC 3977 §3.1 wants at least 3 min), and the write deadline covers the whole response. |
| FN-1 | `nntp/nntp-cmd-{article,head,body,stat,reader}.go`, `nntp/nntp-cmd-helpers.go:24`, `nntp/nntp-server-statistics.go`, `nntp/nntp-auth-manager.go` | Stub files, plus unused functions: `parseArticleHeadersShort`, `ServerStats.Reset/GetUptime/GetCommandCount`, `AuthManager.CheckGroupAccess/IsAdmin/CheckConnectionLimit`. `internal/nntp/README.md` claims features that don't exist. |

**NNTP server: RFC conformity**
| ID | Where | Defect |
|----|----|----|
| RFC-1 | `nntp/nntp-server-cliconns.go:115-152` | Missing mandatory READER commands DATE, NEXT, LAST, NEWGROUPS. Also missing: OVER, HDR, LIST OVERVIEW.FMT/HEADERS, LIST wildmat, NEWNEWS, MODE STREAM and CHECK (RFC 4644, although TAKETHIS exists), STARTTLS (RFC 4642). |
| RFC-2 | `nntp/nntp-server-cliconns.go:196-212` | CAPABILITIES lists READER together with MODE-READER, plus the XOVER/XHDR/TAKETHIS labels. AUTHINFO USER stays listed after auth and without TLS, and POST is listed regardless of permission. STREAMING, OVER, HDR and IMPLEMENTATION are never listed. |
| RFC-3 | `nntp/nntp-article-common.go:93-97,159,193` | ARTICLE/HEAD/BODY/STAT with no argument → 501 (should use the current article, or 420). Number 0 or negative → 502. |
| RFC-4 | `nntp/nntp-server-cliconns.go:93-99`, `nntp/nntp-cmd-basic.go:22-23` | The greeting and MODE READER always say 200 "posting allowed", even without a processor (should be 201). The hostname is hardcoded. |
| RFC-5 | `nntp/nntp-cmd-basic.go:25`, `nntp/nntp-cmd-auth.go:45`, `nntp/nntp-cmd-posting.go:26-27,95,108-109` | Unknown MODE/AUTHINFO variant → 500 (should be 501). POST not permitted → 502 (should be 440). IHAVE permanent rejects → 436 (should be 437). |
| RFC-6 | `nntp/nntp-cmd-xhdr.go`, `nntp/nntp-cmd-xover.go` | XHDR supports only 7 headers, has no "(none)" and no message-id form. An empty XOVER range → an empty 224. |

**Article intake (RFC 5536/5537)**
| ID | Where | Defect |
|----|----|----|
| INT-1 | `processor/threading.go:57-61,185-192` | POST without Message-ID or Date → 441; the injecting agent must add them (RFC 5537 §3.5). No Injection-Date/Info or `.POSTED`. The Path prefix is stored only in the `path` column; HEAD/ARTICLE serve the original Path. |
| INT-2 | `processor/threading.go:76-81,270-283` | An article already in every target group → CasePass, so IHAVE answers 235 and TAKETHIS 239. An in-flight message-id → CaseDupes instead of Retry. |
| INT-3 | `processor/proc-utils.go:414-454,756-787` | A malformed Date is accepted as 1990-01-01. Newsgroups is split on `[,;:\s]+` with a lax name regex. |
| INT-4 | `processor/threading.go`, `nntp/nntp-client-commands.go:936-950` | No control messages, Supersedes, moderation or Approved check. A foreign Xref is stored and served, and no local Xref is generated. |
| INT-5 | `nntp/nntp-client-commands.go:877`, `migrations/0001_single_db_schema.sql:17` | `bytes` is the LF-joined body length (RFC 3977 §8.1.1: the whole article with CRLF). Article numbers are rowids without AUTOINCREMENT, so they can be reused. |
| INT-6 | `database/db_batch.go:664-672` vs `:747-751` | The first batch of an auto-created group skips the history adds. |
| INT-7 | `web/web_sitePostPage.go:343-359` | Web posts are stored without Subject, Date, Message-ID or Path in `headers_json`, and NNTP serves them that way. |

**NNTP client, transfer and peering**
| ID | Where | Defect |
|----|----|----|
| CLI-1 | `common/headers.go:267-297,396-411`, `nntp/nntp-client-commands.go:1149,1271` | `ReconstructHeaders` sends two Path headers on every TAKETHIS. Folded Newsgroups lines are sent twice, and a final empty body line is dropped. |
| CLI-2 | `nntp/nntp-client-commands.go:636-641` | Nil dereference (panic) when GROUP returns 411 in `XHdrStreamedBatch`. |
| CLI-3 | `nntp/nntp-client.go:208-232,346-362`, `nntp/proxy.go:59` | No read/write deadlines (the helpers are dead code), no TLS handshake timeout, and the socket leaks on connect errors. |
| CLI-4 | `nntp/nntp-transfer-demuxer.go:185-191` | The demuxer expects the first CHECK to be command id 1, but AUTHINFO + MODE STREAM use ids 0–2, so `-username` transfers stall. |
| CLI-5 | `nntp/nntp-client.go:211-260`, `nntp/nntp-backend-pool.go:189-201`, `nntp/nntp-client-commands.go:51-54,1231-1250` | A 201 greeting fails `Connect`. A 281 reply to AUTHINFO USER counts as failure. 480 is reported as "group not found". The 430/451 branches are unreachable. The 401 check never matches. |
| CLI-6 | `nntp/nntp-peering.go`, `cmd/test-nntp` | `PeeringManager` is unused and unfinished: `LoadConfiguration` is a stub, the DNS limiter deadlocks, and `AddPeer` keeps pointers into a slice that append reallocates. `test-nntp` is hardcoded to an external server and calls APIs that no longer exist. |

**Web, not in WSH**
| ID | Where | Defect |
|----|----|----|
| WEB-1 | `web/web_admin_crons.go:15,145` | Create and toggle only check `requireAdminAuth`, not `s.CronEdit` (update and delete do), so admins can create and enable `sh -c` jobs without `-edit-cronjobs`. |
| WEB-2 | `web/web_apiHandlers.go:360-376`, `web/static/js/thread-tree.js:513-538` | The public preview API returns the body entity-decoded and unescaped ("JS will handle HTML"), and thread-tree.js inserts it with `innerHTML` ("already sanitized"). Stored XSS candidate; check `formatPreviewText`. |

**Process and ops**
| ID | Where | Defect |
|----|----|----|
| OPS-1 | `database/db_init.go:116-144`, `config/config.go:541-544`, `processor/processor.go:60-70`, `history/history.go:494-500`, `database/db_batch.go:602-619` | Library code kills the process (`log.Fatal`/`os.Exit`). The batch retries forever after `Shutdown`. The DB can't be reopened in one process (`INIT`, global `BatchDividerChan`). |
| OPS-2 | `cmd/web/main.go:105,426`, `cmd/nntpmgr` (`-list`), `nntp/nntp-peering-pattern_test.go:410,558,608` | The embedded NNTP server in cmd/web can't start (`-withnntp` is commented out). `nntpmgr -list` prints bcrypt hashes. A test writes `active.out` and `analysis_*.txt` into the source tree. |
| OPS-3 | `cmd/nntp-transfer/main.go:12,2935-2940`, `cmd/history-rebuild/main.go:97-101` | `net/http/pprof` is served on all interfaces through the default mux, and `-pprof` accepts any address. |
| OPS-4 | `cmd/history-rebuild/main.go:79`, `cmd/web/main.go:136,213`, `cmd/tcp2tor/main.go:102`, `run_web*.sh`, `scripts.sh`, `run_test.sh`, `build_*.sh`, `.github/workflows` | Flags that are ignored (`-useshorthashlen`, web `-data`, tcp2tor `-timeout`). Scripts pass flags that don't exist or reference missing files. Release builds use `-race`. CI has no test or vet step. |

**Already in WSH (not re-reported):**
- the expired-token crash (C1)
- the `-no-cronjobs` crash (C2)
- group DB creation from `/api/thread-tree` (C4)
- HTTP server timeouts (C6)
- open redirect, lockout and registration-session bugs (H2–H4)
- display name, subject and message-id header injection (H10)
- the AI chat session id (H11)
- CSRF (S1)
- tree HTML escaping (S2)

---

## Design

### D1 Audit reports (`docs/audit/`)
- **Files.**
  - One report per audit slice: `docs/audit/<slug>.md`.
  - The consolidated report is `docs/audit/AUDIT-2026-09.md`.
  - The schema lives in `docs/audit/README.md`; tool output goes in `docs/audit/tools/`.
- **Report layout.**
  1. `# Audit <slug> (base <sha>)`
  2. `## Scope and method`: files read, tools, RFCs fetched from rfc-editor.org, repros run.
  3. `## Summary`: counts by severity and category, plus the 5 most important findings.
  4. `## Findings`, one block per finding:
     - a table with `seed` (SEC-1 or `new`), `id`, `severity` (critical/high/medium/low/info), `category` (security | bug | missing | wrong-function | rfc | robustness | dead-code | test-gap), `location` (path:line) and `verdict` (CONFIRMED or PLAUSIBLE)
     - **Scenario:** input/state → wrong result
     - **Evidence:** an exact code trace, or the repro commands and trimmed output
     - **RFC:** section plus a 1–2 line quote (rfc category only)
     - **Fix:** a suggestion plus effort S/M/L
  5. `## Function inventory`: a table of `function/file | stub | dead | unused | misleading | wrong | evidence`.
  6. `## Seed items rejected`: each with the reason.
  7. `## Not covered / follow-ups`
- **Repros.**
  - Standalone programs go under `./data-test-audit-<slug>/_repro/<name>/main.go` and run with `go run ./data-test-audit-<slug>/_repro/<name>/main.go`. The directory is gitignored via `/data*`, and the `_` prefix keeps it out of `go vet ./...` and `go build ./...`. They are inside the module, so they may import `internal/...`.
  - A repro that needs unexported identifiers may use a temporary `zz_audit_<slug>_test.go`, which must be deleted before the commit.
  - Servers use `-data ./data-test-audit-<slug>`, a port from 21200 + slice index, and are stopped afterwards.

### D2 Test harness (package `nntp`)
- **Import cycle.** `internal/processor` imports `internal/nntp`, so package-`nntp` tests use a fake `ArticleProcessor`. Tests with the real processor live in `cmd/nntp-server` (package main, next to `ProcessorAdapter`).
- **One `database.OpenDatabase` per test binary**, opened in `TestMain`. A second open fails because of `INIT`, and the global `BatchDividerChan` with dividers that never exit sends articles to the wrong DB.
  - Before opening, `TestMain` sets `database.BatchInterval = 100*time.Millisecond`, `database.ENABLE_ARTICLE_CACHE = false` (the cache has no TTL and would leak between tests) and `database.NO_CACHE_BOOT = true`.
  - It checks `os.Geteuid() >= 1000` **before** `OpenDatabase`, which would `log.Fatal`. When run as root, DB tests skip with a printed reason.
  - It uses `PUGLEAF_TEST_DATADIR` if set (see `runIsolated`), otherwise `os.MkdirTemp`, and asserts the directory is under `os.TempDir()`. It never uses `./data`.
  - It calls `log.SetOutput(io.Discard)` unless `PUGLEAF_TEST_LOG=1`.
  - Teardown order: `close(db.StopChan)` → `db.WG.Wait()` → `db.Shutdown()` → remove the dir.
- **What never goes in tests:**
  - `config.NewDefaultConfig()` (`log.Fatal`); use `&config.ServerConfig{}` with `NNTP.MaxConns`.
  - `processor.NewProcessor` (DNS lookup plus `log.Fatal`).
  - `db.Batch.SetProcessor` inside package `nntp`, where seeding uses raw SQL, not the batch.
- **Seeding** (seed helpers commit synchronously, so no waiting is needed):
  - `db.InsertNewsgroup(&models.Newsgroup{Name, Description, Active, Status: "y", LowWater: 1, Hierarchy: <first label>})`.
  - Articles via raw SQL on `GetGroupDB(g).DB`:
    ```sql
    INSERT INTO articles (article_num,message_id,subject,from_header,date_sent,date_string,"references",
                          bytes,lines,reply_count,path,headers_json,body_text,downloaded,imported_at)
    VALUES (...)
    ```
    - No NULLs.
    - `headers_json` is the header lines joined with `"\n"`; `body_text` is LF-joined and dot-unstuffed.
    - Then update `newsgroups.last_article`, `message_count` and `high_water` in one statement.
  - Users: `db.InsertNNTPUser` (username at least 10 characters, `IsActive: true`, `WebUserID: 0`).
- **Production hooks** (wave 0, default behavior unchanged):
  - `NNTPServer` gains `ArticleCmdDelay`, `GroupCmdDelay`, `ErrorDelay` and `IdleTimeout` (`time.Duration`).
    - `NewNNTPServer` seeds them from new exported package defaults `DefaultArticleCmdDelay = time.Second/5`, `DefaultGroupCmdDelay = time.Second/3`, `DefaultErrorDelay = time.Second`, and the existing `DefaultNNTPcliconnTimeout`.
    - Handlers read `c.server.<field>`. `IdleTimeout <= 0` falls back to `DefaultNNTPcliconnTimeout`; a zero delay means no sleep. Existing literal-built test servers therefore run without delays.
    - Per-server fields avoid data races between tests (package vars read by server goroutines would race under `-race`).
  - `func (s *NNTPServer) Serve(l net.Listener, isTLS bool) error`: under `s.mu`, set `running`, store the listener (`Listener`, or `TLSListener` when `isTLS`), `s.wg.Add(1)`, `go s.serve(l, isTLS)`.
    - `Start()` keeps its behavior: it creates the listeners, then calls a lock-free internal `serveLocked`, so the lock is taken exactly once.
    - This lets tests and TLS tests use `127.0.0.1:0`.
- **Harness API** (`internal/nntp/nntp-server-harness_test.go`):
  - `hTestDB() *database.Database`
  - `newTestServer(t, hServerOpts{Proc ArticleProcessor; MaxConns int; IdleTimeout, ErrorDelay time.Duration; TLS *tls.Config}) *hServer`
    - builds via `NewNNTPServer`, with its **own** `*sync.WaitGroup`, and `Serve` on `127.0.0.1:0`
    - cleanup: `Stop()`, close every tracked client, then wait for the server's WaitGroup **at most 10 s**; on timeout call `t.Errorf` without blocking
  - `(*hServer).dial(t) *hClient`: 5 s deadline, reads the greeting and returns it in `c.Greeting`
  - Client methods:
    - `c.cmd(t, format, args...) (code int, line string)` (fatal on I/O error)
    - `c.tryCmd(format, args...) (int, string, error)`
    - `c.readDotLines(t) []string`
    - `c.rawUntilDot(t) []byte` (byte-exact CRLF and dot-stuffing checks)
    - `c.send(raw string)` (pipelining)
  - Seeding: `hGroup(t, prefix string, n int, mutate func(i int, a *hArticle)) string` (returns a unique group name), `hArticleSQL(...)`, `hUser(t, posting, active bool) (user, pass string)`
  - `hFakeProcessor`: the same behavior as `wiringTestProcessor`, kept separate so `nntp-wiring_test.go` isn't touched.
  - `hExpectCode(code int, line string, want ...int) error`, `hExpectLines(got, want []string) error`
- **`knownBug(t, id string, err error)`** takes the result of an RFC-correct check:
  - `err == nil` → `t.Fatalf("known bug %s no longer reproduces: remove knownBug, keep the assertion", id)`. A fixed bug fails the suite until the marker is removed.
  - `err != nil` with `PUGLEAF_KNOWN_BUGS=1` → `t.Errorf("known bug %s still open: %v", id, err)` (lists the open bugs).
  - `err != nil` otherwise → `t.Skipf("known bug %s: %v", id, err)`.
- **`runIsolated(t, timeout time.Duration, child func(t *testing.T)) error`**, for handlers that can spin (SEC-1), which nothing inside the process can stop:
  - In the child (`PUGLEAF_ISOLATED == t.Name()`), run `child(t)` and return.
  - Otherwise, re-exec `os.Args[0] -test.run=^<QuoteMeta per subtest level>$ -test.count=1` with `PUGLEAF_ISOLATED=<t.Name()>` and `PUGLEAF_TEST_DATADIR=<t.TempDir()>`, under `exec.CommandContext` with the timeout.
  - Return `nil` if the child passed, an error with the tail of its output if it failed, or `"timed out after …"`. The parent kills the child, and `t.TempDir` cleans up its data.
  - The child's client uses a 2 s deadline, so a spinning server fails the child quickly; the child exits and its goroutines die with it.

### D3 Contracts
- **K1:** unchanged throughout this plan, apart from the wave-0 hooks: `NewNNTPServer`, `Start`/`Stop`/`IsRunning`, `ArticleProcessor`, `NewClientConnection`, every `handle*` method name, `ProcessorAdapter`, and all `internal/database` exported functions.
- **K2: name prefixes per test slice.** Every package-level identifier in a slice file uses its prefix:
  | Slice | Prefix |
  |----|----|
  | `test-nntp-session` | `sess` / `TestSess` |
  | `test-nntp-auth` | `auth` / `TestAuth` |
  | `test-nntp-group` | `grp` / `TestGrp` |
  | `test-nntp-article` | `art` / `TestArt` |
  | `test-nntp-overview` | `ovr` / `TestOvr` |
  | `test-nntp-intake-e2e` | `e2e` / `TestE2E` / `FuzzE2E` |

  Harness identifiers start with `h`, `newTestServer`, `knownBug` or `runIsolated`.
- **K3: test data.**
  - Only wave 0 creates `TestMain` in `internal/nntp`; `test-nntp-intake-e2e` creates the one in `cmd/nntp-server`.
  - Groups are named `t<N>.<test>.<seq>` and message-ids `<t<N>-<test>-<seq>@test.invalid>`. Users are `t<N>user<seq>` padded to 10 or more characters. `N` is the slice number 1–6.
  - No `t.Parallel()`. No package global is mutated after `TestMain`; use per-server fields.
  - No sleep longer than 100 ms; poll with a deadline instead.
- **K4:** nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt` or production code outside the wave-0 hooks. `gofmt` runs only on owned files.
- **K5:** audit slices commit only their report and follow D1's repro rules. Test slices commit only their test files (plus, for T6, its script).
- **K6: ids.**
  - Seed ids are canonical and never renumbered.
  - New findings from audit slices are `A-<slug>-<n>`.
  - Bugs a test slice finds that aren't in the seed list are `T-<slug>-<n>`; the slice lists them in its report, and wave 3 adds them to the report.
  - WSH findings are referenced as `WSH:<id>`.

### D4 Fix plans (wave 4 output)
- **Queue files.** Each follows the WSH structure: header, Context with a findings table, decisions, out of scope, Design with contracts and reserved names, Waves with disjoint owned files and exact changes, the tests that remove `knownBug` markers, acceptance E-checks, Checks, and End-to-end.
  | File | Covers |
  |----|----|
  | `nntp-server-hardening.md` | SEC-1…7 (server side), BUG-1…9, RFC-2…6, FN-1, and new server findings |
  | `nntp-intake-injection.md` | INT-1…6, SEC-2/SEC-3 on the intake side |
  | `nntp-client-transfer.md` | CLI-1…6, OPS-3 |
  | `nntp-rfc-commands.md` | RFC-1 (new commands) |
  | `audit-delta-fixes.md` | new critical/high findings from audit-web/db/tools/ops, plus WEB-1, WEB-2, INT-7, OPS-1/2/4; states `Depends on: web-sqlite-hardening` when it touches WSH-owned files |
- **Coordination rules** (Appendix B):
  - A plan may not own a file that WSH owns unless it declares that dependency.
  - NNTP handlers check group existence with `MainDBGetNewsgroup` before `GetGroupDB`, so `db_groupdbs.go` (WSH `w1-sqlite`) stays untouched.
  - New DB functions go in new files, `internal/database/nntp_<topic>.go`.
  - The dispatch `switch` and `getServerCapabilities` in `nntp-server-cliconns.go` are edited only inline by the orchestrator. Slices hand over the exact lines they need.

---

## Waves

### Wave 0 (inline, orchestrator on `plan-nntp-audit-tests`)

**Owned files:**
- `internal/nntp/nntp-server.go`
- `internal/nntp/nntp-server-cliconns.go` (only `UpdateDeadlines`)
- `internal/nntp/nntp-cmd-helpers.go` (only `rateLimitOnError`)
- `internal/nntp/nntp-article-common.go` (only line 46)
- `internal/nntp/nntp-cmd-group.go` (only line 10)
- `internal/nntp/nntp-server-harness_test.go` (new)
- `docs/audit/README.md` (new)
- `docs/audit/tools/*.txt` (new)

**Steps:**
1. **Preflight** as in the skill. Run the baseline `## Checks` and record PASS/FAIL in the progress note.
2. **Static analysis.** Run `go vet ./...`, `~/go/bin/staticcheck ./...`, `~/go/bin/govulncheck ./...` and `~/go/bin/golangci-lint run --enable gosec --timeout 10m`. Save each output (or the load error) to `docs/audit/tools/<tool>.txt`. If an output has more than 5000 lines, keep the first 5000 plus a per-linter count.
3. **Audit README.** Write `docs/audit/README.md` from D1 and K6, including the slice table below and the seed id tables (copied from Context).
4. **Hooks.** Implement the D2 production hooks. Keep default behavior identical: `NewNNTPServer` seeds the defaults, and `Start()` still binds `:%d`.
5. **Harness.** Write the harness from D2 with 4 self-tests:
   - `TestHarnessSmoke`: greeting, CAPABILITIES (multi-line), QUIT → 205.
   - `TestHarnessSeed`: `hGroup` with 3 articles, then GROUP → 211 with count 3.
   - `TestHarnessKnownBug`: a table test of the pure decision function `hKnownBugAction(err error, envSet bool) (action, msg)`. `knownBug` is only a thin wrapper around it, because a `Fatalf` branch can't be exercised inside the same test.
   - `TestHarnessIsolatedTimeout`: a child that sleeps 30 s gives `timed out` within about 3 s.
6. **Checks:** `gofmt -l internal/nntp`; `go vet ./...`; `go build ./...`; `go test -race -count=1 -timeout 300s ./internal/nntp/...`. The existing wiring, parse and peering tests must stay green, and nothing may be written into the source tree.
7. **Commit** with `test(nntp): wave-0 harness, per-server delay hooks, audit schema`, and include `.claude/plans/wip/nntp-audit-tests.md` so worktree agents can read the plan. Record the base SHA.

### Wave 1: audit (8 `pugleaf-implementer` slices; each owns only `docs/audit/<slug>.md`)

Rules for every audit slice:
- Read the scope files fully, plus the Context seed rows listed for the slice and `docs/audit/tools/*` filtered to the scope. Follow D1 and K5.
- Every listed seed row ends up CONFIRMED, PLAUSIBLE or REJECTED. Every critical or high finding is CONFIRMED with evidence, or explicitly marked PLAUSIBLE with the missing step.
- **Checks:** `git diff --stat <base>..HEAD` lists only the owned report. `go build ./...` still passes. No leftover `zz_audit_*` files.
- **Reviewer task:** re-verify every critical/high item and at least 5 others against the code; flag unsupported claims and severity inflation.

#### Slice `audit-nntp-rfc`: command-by-command RFC conformity
- **Scope:**
  - `internal/nntp/{nntp-server.go, nntp-server-cliconns.go, nntp-server-statistics.go, nntp-cmd-*.go, nntp-article-common.go, nntp-cache-local.go, nntp-auth-manager.go, README.md}`
  - `cmd/nntp-server/`
- **RFCs:** 3977, 4643, 4644, 4642, 2980, 6048, 5536 §3 (only for served headers).
- **Seeds:** RFC-1…6, BUG-3…6, BUG-9, FN-1.
- **The report must add:**
  - `## Command matrix`: command/variant × RFC section × mandatory-when (READER, POST, …) × implemented × syntax OK × response codes OK (listing wrong ones) × multi-line format OK × finding ids.
  - `## Response code table`: every `sendResponse` code in the server vs the RFC-allowed codes for that command.
  - `## CAPABILITIES`: expected list per state (before/after auth, with/without processor, TLS).
- **Repro:** a harness-style test in a temporary `zz_audit_nntp-rfc_test.go` that runs every command against a seeded server. Paste the transcript into the report, then delete the file.

#### Slice `audit-nntp-sec`: security, DoS, concurrency of the server
- **Scope:** the same server files, plus:
  - the DB functions they call: `GetGroupDB`, `GetOverviews*`, `GetHeaderFieldRange`, `GetArticleBy*`
  - `internal/database/{db_nntp_users.go, nntp_auth_cache.go}`, in full
  - `cmd/nntp-server/main.go` (startup and shutdown)
- **Seeds:** SEC-1…7, BUG-1, BUG-2, BUG-7, BUG-8.
- **The report must add:**
  - `## Unauthenticated attack surface`: command → resources touched (files, memory, goroutines, CPU) → limit today → proposed limit.
  - `## Measured repros`: CPU% for SEC-1, file count for SEC-2, RSS for SEC-3/SEC-4/SEC-7, each with commands and numbers.
  - `## Races`: `go test -race` on a temporary stress test (100 concurrent clients doing GROUP/XOVER/ARTICLE/STAT/QUIT).
  - `## TLS and shutdown`: `tls.Config` defaults, `Stop()` behavior with live clients, and the leak on cert failure.

#### Slice `audit-web`: delta web audit (after WSH)
- **Scope:** all `internal/web/*.go`, `internal/web/static/**`, `web/templates/*.html`, `internal/{models,cache,utils}`, and `internal/database/{db_apitokens.go, db_sessions.go, db_sections.go, sanitize_cache.go, sections_cache.go, tree_cache.go, tree_view_api.go}`.
- **Excluded:** every WSH finding. Read WSH's Findings first and cite `WSH:<id>` instead of re-reporting.
- **Seeds:** WEB-1, WEB-2, INT-7.
- **Focus:**
  - output escaping and DOM sinks (`innerHTML`, `template.HTML`, `safeHTML`/custom funcs, URL and attribute contexts, JS string contexts in templates)
  - `models/sanitizing.go` correctness
  - a per-route guard table for admin handlers not changed by WSH (newsgroups, provider, spam, postqueue, settings, sitenews, ollama, sections, hierarchies, apitokens)
  - cron gating, API surface and data exposure, cache key collisions (`models/cache.go`), UTF-8 slicing outside WSH:L6
- **The report must add** `## Route guard table` and `## Sink table`.

#### Slice `audit-db`: delta database audit (after WSH)
- **Scope:** `internal/database/{db_batch.go, queries.go, database.go, db_init.go, db_groupdbs.go, db_migrate.go, embedded_migrations.go, migrations/*.sql, sqlite_retry.go, groups_hashmap.go, utils.go, users.go, hierarchy_cache.go, db_config.go, config_cache.go, db_cron_jobs.go, db_aimodels.go, thread_cache.go, article_cache.go, db_rescan.go}`.
- **Excluded:** WSH findings (H6–H9, P3, P4, P6, P8, L4, L5).
- **Seeds:** SEC-2 (DB side), BUG-1, BUG-2, INT-5, INT-6, OPS-1 (DB side).
- **Focus:**
  - batch writer: correctness, shutdown, endless retries, auto-created `newsgroups` rows with NULL `hierarchy`, history gating
  - NULL scans that break whole listings (`GetActiveNewsgroups`)
  - LIMIT clamps that silently truncate
  - article-number reuse
  - globals that block re-open (`INIT`, `BatchDividerChan`, `migratedDBsCache`)
  - every `fmt.Sprintf`/concatenated SQL (table with verdict)
  - group-DB file creation for names that were never validated, from every caller outside web
- **The report must add** `## SQL construction table` and `## Global state table`.

#### Slice `audit-intake`: article validation, injection, history, header rebuilding
- **Scope:**
  - `internal/processor/{processor.go, threading.go, proc-utils.go, proc_DLArt.go, proc_DLXHDR.go, proc_ImportOV.go, proc_reuse.go, PostQueue.go, bridges.go, counter.go, interface.go, proc_MsgIDtmpCache.go, proc_MsgIdItemCache.go, unified_cache.go}`
  - `internal/{common,history,postmgr,preloader,fediverse,matrix,config}` (quote the two history file names that contain spaces)
  - `internal/spam` (inventory only), `internal/database/db_post_queue.go`, `cmd/post-queue`
- **RFCs:** 5536, 5537.
- **Seeds:** INT-1…6, CLI-1 (`common/headers.go` part), OPS-1 (history and processor).
- **The report must add** `## Header handling matrix`: header × validated on intake × generated when missing × rewritten × served over NNTP × RFC requirement.

#### Slice `audit-nntp-client`: outbound NNTP
- **Scope:** `internal/nntp/{nntp-client.go, nntp-client-commands.go, nntp-backend-pool.go, proxy.go, nntp-transfer.go, nntp-transfer-demuxer.go, transfer-progress.go, transfer-progress-utils.go, nntp-peering.go, nntp-peering-pattern.go, nntp-peering-struct.go.bak (dead file)}`, `cmd/nntp-transfer/`.
- **Seeds:** CLI-1…6, OPS-3.
- **Focus:**
  - response parsing against hostile servers (octet, line and deadline limits)
  - CR/LF injection through message-ids and group names into commands
  - dot-stuffing in both directions
  - leaks in the pool and demuxer
  - proxy/TLS, the transfer web UI, pprof, Redis
- **The report must add** `## Client command table`: command × expected codes handled × limits × timeouts. The repro may use a tiny fake server under `./data-test-audit-nntp-client/`.

#### Slice `audit-tools`: fetch, analyze and import tools
- **Scope:** `cmd/{nntp-fetcher, nntp-analyze, tcp2tor, import-flat-files, rslight-importer, merge-active, merge-descriptions, extract_hierarchies, test-nntp, parsedates, benchmark_hash}`, `internal/processor/{analyze.go, rslight.go, rslight_articles.go}`, `internal/database/progress.go`.
- **Seeds:** CLI-6 (test-nntp).
- **Focus:** parsing of files and remote data, path construction, `os.Exit`/`log.Fatal` in library code, tcp2tor exposure and timeouts, and the fetcher memory growth mentioned in BUGS.md (read-only analysis, no live fetch).

#### Slice `audit-ops`: binaries, destructive tools, scripts, CI
- **Scope:**
  - `cmd/{web, recover-db, history-rebuild, expire-news, fix-references, fix-thread-activity, usermgr, nntpmgr, history-demo, test-MsgIdItemCache}`
  - `active_files/hierarchies/`, root `*.sh`, `scripts/`, `.github/workflows/`, `internal/nntp/nntp-peering-pattern_test.go` (hygiene)
  - `php/` (inventory only: purpose, whether anything serves it, secrets, debug endpoints)
  - Scripts are read only; never run release or data scripts (CLAUDE.md).
- **Seeds:** OPS-1, OPS-2, OPS-4.
- **Focus:**
  - destructive tools: confirmation prompts, handling of `-data`, hardcoded `data/` paths
  - dead or wrong flags
  - startup and shutdown order
  - update download integrity (`getUpdate.sh`, read only)
  - rsync argument injection
  - CI gaps
- **The report must add** `## Flag table`: tool × flag × used? × default touches `./data`?

### Wave 2: NNTP server tests (6 `pugleaf-implementer` slices; independent of wave 1)

Rules for every test slice:
- Create only the owned files; the harness and production code are read-only. Follow K2–K4.
- Every assertion states the RFC-correct behavior, with the section in a comment. Where current code differs, use `knownBug(t, <seed id, or a new T-<slug>-<n> id>, err)`. Anything that could spin goes through `runIsolated`.
- **Checks:**
  - `gofmt -l internal/nntp cmd/nntp-server` (owned files not listed)
  - `go vet ./...`; `go build ./...`
  - `go test -race -count=3 -timeout 600s <pkg>` (3 runs catch flakiness)
  - `PUGLEAF_KNOWN_BUGS=1 go test -count=1 <pkg>` fails only with `known bug … still open` lines
  - `git status --short` in the worktree is clean
- **Report:** test names, then a table of finding id → test → knownBug status (open, or passing because the behavior is already correct).
- **Reviewer task:**
  - determinism: no timing assumptions, polling with deadlines
  - each knownBug `err` really comes from the RFC-correct check
  - tests fail for the right reason
  - no production edits, prefixes followed

#### Slice `test-nntp-session`
**Owned file:** `internal/nntp/nntp-server-session_test.go` (new).

Tests:
- **Greeting:** 200 with a processor. Without one, 201 (RFC 3977 §5.1.1; RFC-4).
- **CAPABILITIES:** the first line is `VERSION 2`. READER without MODE-READER. No XOVER/XHDR/TAKETHIS labels. With a processor, POST (only when allowed), IHAVE and STREAMING when implemented. `AUTHINFO USER` is absent after auth (RFC 3977 §5.2; RFC 4643 §2.2; RFC-2).
- **MODE:** `MODE READER` gives 200/201. `MODE STREAM` gives 203 or 501 (RFC 4644 §2.3). `MODE FOO` gives 501 (RFC-5).
- **HELP** gives 100 plus a dot-terminated body. **QUIT** gives 205, then EOF within 1 s.
- **Unknown, empty and lowercase commands:** `foo` → 500, an empty line → 500 or 501, `quit` → 205.
- **Pipelining:** `GROUP x\r\nSTAT 1\r\nQUIT\r\n` in one write gives 3 replies in order (RFC 3977 §3.5).
- **Line limit (SEC-3):** a command of more than 512 octets gives 501 or a closed connection. A 1 MiB line without LF gives a close and server memory bounded (check `runtime.MemStats` delta < 16 MiB).
- **SEC-4:** 10k unique junk commands → `len(Stats.GetAllCommandCounts()) <= known commands + 1`.
- **Idle timeout:** `IdleTimeout=200ms` closes the idle client. Also assert the default ≥ 3 min (BUG-9).
- **MaxConns=2:** the third connection is rejected, ideally with 400 or 502.
- **Stop:** `Stop()` closes the listener and live clients within 2 s (BUG-7).
- **TLS:** a listener with an in-memory self-signed cert (`crypto/x509`, ECDSA P-256), handshake, greeting.
- **Units:** `Local430` Check/Add/Cleanup (entries older than 1 min are removed); `ServerStats` counters.
- **`FuzzSessCommandLine`**, seed corpus only during `go test`: arbitrary lines, then exactly one status line per non-multi-line command and no panic.

#### Slice `test-nntp-auth`
**Owned file:** `internal/nntp/nntp-server-auth_test.go` (new).

Tests:
- **AUTHINFO USER/PASS (RFC 4643 §2.3):** USER → 381, PASS → 281.
- **Failures:** wrong password → 481; an unknown user gets the same 481 text.
- **Argument errors:** PASS before USER → 482; `AUTHINFO` / `AUTHINFO USER` → 501; `AUTHINFO FOO x` → 501 (RFC-5).
- **Account and password edge cases:**
  - a second AUTHINFO after success → 502 (SEC-5)
  - an inactive user → 481
  - a password containing a space authenticates if stored that way (document the RFC 4643 ABNF)
  - a 481 reply is delayed by at least `ErrorDelay` (set to 50 ms; SEC-5)
- **Posting gates:** unauthenticated POST/IHAVE/TAKETHIS → 480. A user without posting → POST 440, IHAVE 502 (RFC-5).
- **`AuthManager` units:** `CanPost`, `CheckGroupAccess(nil)`, `CheckConnectionLimit`.
- **SEC-6:** authenticate (cached), `DeactivateNNTPUser`, authenticate again → must fail. The same after `UpdateNNTPUserPassword`: the old password must fail.
- **Plaintext AUTHINFO:** on a non-TLS listener it gives 483, or is at least not advertised (SEC-5, RFC 4643 §2.3.1).

#### Slice `test-nntp-group`
**Owned file:** `internal/nntp/nntp-server-group_test.go` (new).

Tests:
- **GROUP:**
  - on a seeded group (articles 1..5 with 1–2 deleted), `211 3 3 5 name` (RFC 3977 §6.1.1; BUG-4)
  - the current article is set to the low mark: a bare `STAT` → 223 3
  - an unknown group → 411 with the current group unchanged; an inactive group → 411; an empty group → `211 0 x x-1 name` or `211 0 0 0` per §6.1.1.2
  - no argument → 501
- **LISTGROUP:**
  - `211 count low high group` followed by sorted numbers (§6.1.2)
  - the range `4-` works
  - LISTGROUP selects the group (then `STAT` works)
  - an unknown group → 411 **and no new file under `<DataDir>/db`**, checked with `filepath.Glob` before and after (SEC-2)
  - a 20k-row group stays under a memory ceiling (SEC-7, `runtime.MemStats` delta)
- **LIST:**
  - `LIST ACTIVE` lines are `name high low status` with the real low mark and status `y` when posting is allowed (§7.6.3; BUG-4)
  - `LIST ACTIVE t3.*` and `LIST NEWSGROUPS t3.*` filter (RFC-1)
  - `LIST FOO` → 501; `LIST OVERVIEW.FMT` → 215 plus the §8.4 fields (RFC-1)
- **Missing commands (RFC-1):** `DATE` → `111 yyyymmddhhmmss`; `NEXT`/`LAST` → 223/421/422/412/420; `NEWGROUPS <date> <time> GMT` → 231; `NEWNEWS` → 230 or 500 (not advertised).

#### Slice `test-nntp-article`
**Owned file:** `internal/nntp/nntp-server-article_test.go` (new).

Tests:
- **By number:** ARTICLE/HEAD/BODY/STAT `<n>` → 220/221/222/223 `n <msgid>` (RFC 3977 §6.2).
- **By message-id in the current group** → the number in the current group.
- **Message-id found in another group** via `hFakeProcessor` → article number 0 (§6.2.1.2).
- **No argument:** → the current article, or 420 (RFC-3).
- **Invalid numbers:** `0`, `-1` and `99999999999999999999` → 423/501 (RFC-3).
- **Not found:** a missing number → 423; a missing message-id → 430; no group plus a number → 412.
- **currentArticle** after `ARTICLE 2`: a following bare `STAT` → 223 2 (BUG-1). A message-id lookup leaves it unchanged.
- **Byte-exact output:**
  - every line ends in CRLF
  - a body line `.dot` is sent as `..dot`, and a body line `.` as `..`
  - a header continuation line starting with `.` is stuffed (BUG-6)
  - an empty body gives exactly `\r\n.\r\n` after the headers' blank line
  - empty `headers_json` gives no blank line inside HEAD
- **local430:**
  - a miss via the fake processor (`ErrArticleNotFound`), then a second STAT → 430 without a processor call
  - after the article is seeded and the processor finds it, STAT → 223 within 1 s (BUG-8)
- **Delay:** with `ArticleCmdDelay=0`, 50 STATs finish in under 1 s. Default-delay behavior is documented in a comment only (BUG-3).

#### Slice `test-nntp-overview`
**Owned file:** `internal/nntp/nntp-server-overview_test.go` (new).

Tests:
- **XOVER ranges (RFC 2980 §2.8 / RFC 3977 §8.3):** `n`, `n-`, `n-m` → 224 plus exactly the matching rows. A reversed or empty range → 423 (RFC 3977 §8.3.2) or 420 (RFC 2980); while the server answers an empty 224, that is `knownBug` RFC-6. Garbage → 501. No group → 412; no current article → 420.
- **XOVER line format:** 8 mandatory fields (number, subject, from, date, message-id, references, bytes, lines), and no stray trailing empty field unless Xref is listed in OVERVIEW.FMT (BUG-5).
- **Sanitizing:** TAB/CR/LF inside subject, from and references are replaced by a space (§8.3.2; BUG-5).
- **Full range:** a 1500-article group with `XOVER 1-1500` → 1500 lines, and `XHDR subject 1-1500` → 1500 lines (BUG-2).
- **XHDR:**
  - `XHDR subject n-m` → `221` plus `n value` lines
  - an unsupported but present header (`Newsgroups`, `Path`) → its value
  - an absent header → `n (none)`
  - the message-id form `XHDR subject <id>` (RFC 2980 §2.6; RFC-6)
- **SEC-1** with `runIsolated(t, 20*time.Second, child)`. The child sends `XHDR subject 1-9223372036854775807` and `XOVER 1-9223372036854775807` on a seeded group and needs the dot terminator within 2 s. Pass the result to `knownBug(t, "SEC-1", err)`.
- **OVER/HDR (RFC 3977 §8.3, §8.5):** `OVER`/`HDR` → 224/225 (RFC-1).

#### Slice `test-nntp-intake-e2e`
**Owned files (all new):**
- `cmd/nntp-server/server_e2e_test.go`
- `cmd/nntp-server/e2e_binary_test.go` (build tag `e2e`)
- `internal/nntp/nntp-server-intake-fuzz_test.go`
- `scripts/test-nntp-server.sh`

**`server_e2e_test.go` TestMain** (package main):
- When `PUGLEAF_E2E_ADDR` is set (the binary run from the script), skip the in-process setup below and just run `m.Run()`.
- The DB and globals as in D2.
- `processor.LocalNNTPHostname = "news.test.invalid"`; `history.ENABLE_HISTORY = true`.
- `hist, _ := history.NewHistory(&history.HistoryConfig{HistoryDir: <tmp>/history, BatchTimeout: 50, MaxConnections: 4}, db.WG)`.
- `history.NewMsgIdItemCache()`; `proc := &processor.Processor{DB: db, History: hist}`; `db.Batch.SetProcessor(proc)`.
- Server: `nntp.NewNNTPServer(db, cfg, &wg, NewProcessorAdapter(proc))`, set its delay fields to 0, then `Serve` on `127.0.0.1:0`.
- Teardown: `Stop` → `close(db.StopChan)` → `proc.Close()` under a 30 s timeout → `db.WG.Wait()` → `db.Shutdown()`.
- Users from `db.InsertNNTPUser`; groups `t6.*` from `InsertNewsgroup`.

**Tests** (commit polling uses a 20 s deadline):
- **POST with all headers** → 240. Poll `GetArticleByMessageID`, then ARTICLE and HEAD by message-id return the body and headers. GROUP `t6.post.1` → count 1, and `ARTICLE 1` works. Check the served Path starts with `news.test.invalid!` (INT-1).
- **POST without Message-ID, and without Date:** the article is accepted with generated headers (INT-1, knownBug).
- **IHAVE:** `<new>` → 335, then the article → 235. The same id again → 435.
- **INT-2:** seed an article in `t6.dup.a` with raw SQL, so history doesn't know it, then IHAVE the same id: 335, then the article → 437 (today 235).
- **TAKETHIS:** a pipelined stream of 3 articles (new, duplicate, mismatched id) → 239, 439, 439 in order, each carrying the id.
- **Rejections:** a missing Newsgroups header → 441/437. POST to `t6.nosuch.group` → rejected, no `newsgroups` row, and no file under `<DataDir>/db` (SEC-2).
- **Size and dot-stuffing:** a 20 000-line article → a clean rejection with the connection still usable, or a close without a write to a closed socket (SEC-3, BUG-7). A body with `..leading` and `.` lines round-trips byte-exactly.
- **Crossposts:** a crosspost to `t6.x.a,t6.x.b` has a number in both groups. After GROUP `t6.x.b`, `ARTICLE <id>` gives that group's number; with no group selected it gives 0 (found through history).
- **`FuzzE2EParseIncomingArticleLines`** (package `nntp` file): random head and body lines never panic, and a parsed Message-ID equals `extractHeaderValue`.

**`e2e_binary_test.go`** (`//go:build e2e`):
- `TestE2EBinary` connects to `PUGLEAF_E2E_ADDR` using `PUGLEAF_E2E_USER`/`PASS` and runs the End-to-end client steps.
- It prints `PASS|FAIL|INFO N<nn> …` lines.

**`scripts/test-nntp-server.sh`** implements `## End-to-end`, following the style of `scripts/test-web-hardening.sh` in WSH:
- a `DATA` guard that requires `./data-test-*`
- tool checks
- a trap that stops the server
- `SUMMARY pass= fail=`

### Wave 3 (inline): consolidate

**Owned files:** `docs/audit/AUDIT-2026-09.md` (new), `BUGS.md`, `README.md` (nntp-server status lines only), `.claude/CLAUDE.md` (Checks list), and `.github/workflows/tests.yml` (new, only if the user agrees at this wave boundary).

1. **Merge.** If the skill's step 5 hasn't already merged them, merge the wave 1 and wave 2 branches: audits first (docs only), then the tests in slice order. Run `## Checks` after each test merge.
2. **Write `AUDIT-2026-09.md`:**
   - counts by severity, category and area; the 25 most urgent findings
   - all findings, deduplicated across slices, with canonical ids, `WSH:` cross-references and the fix plan each belongs to
   - the RFC command matrix (from `audit-nntp-rfc`); the unauthenticated attack surface (from `audit-nntp-sec`)
   - the function inventory (merged); static-analysis highlights
   - rejected seeds
   - the known-bug list (from `PUGLEAF_KNOWN_BUGS=1` output) with the test name for each
3. **Update docs.**
   - `BUGS.md`: the NNTP server section lists the open critical and high ids, links to the report, and states that the XHDR/LISTGROUP DoS must be fixed before any public deployment.
   - `README.md`: the NNTP server line gets "RFC 3977 partial, see docs/audit".
   - `.claude/CLAUDE.md`: add `./cmd/nntp-server/...` to the `go test -race` list.
4. **Optional CI.** With the user's OK, add `.github/workflows/tests.yml`: on push and pull_request, Go 1.25.3, `go vet ./...`, `go build ./...`, `go test -race -timeout 600s` on the tested packages, plus a `known-bugs` job with `continue-on-error: true` running `PUGLEAF_KNOWN_BUGS=1`.
5. **Commit.**

### Wave 4: fix plans (4 `pugleaf-implementer` slices, each owns one plan file; `audit-delta-fixes.md` is written inline by the orchestrator afterwards)

Rules for every slice:
- Read `AUDIT-2026-09.md`, the findings in scope, the code at `plan-nntp-audit-tests` HEAD, and WSH (for format and file ownership).
- Write a plan that `/run-plan` can execute unchanged, at WSH's level of detail:
  - findings table with file:line at the new base
  - decisions and out-of-scope list
  - contracts (K1-style stable APIs, reserved names per slice, test prefixes)
  - waves with disjoint owned files and **exact changes**
  - for each fix, the tests to update: remove the `knownBug(t, <id>, …)` call and keep the assertion
  - acceptance checks, `## Checks`, and `## End-to-end` reusing `scripts/test-nntp-server.sh` with new `N<nn>` checks
- Follow the D4 coordination rules.
- **Checks:**
  - every in-scope id appears in exactly one slice
  - owned files within each wave are disjoint, and none is owned by WSH unless declared
  - 10 random file:line references exist at base
  - only the owned plan file changed
- **Reviewer task:** feasibility of each change against the code, missing callers, ownership conflicts.

Slices:
- **`fixplan-nntp-server`** → `.claude/plans/queued/nntp-server-hardening.md`
  - Scope: SEC-1…7 (server side), BUG-1…9, RFC-2…6, FN-1, and new `A-nntp-*` server findings.
  - Its internal waves must follow Appendix B: session / read / overview / auth-db, with the dispatch and CAPABILITIES lines applied inline.
- **`fixplan-nntp-intake`** → `.claude/plans/queued/nntp-intake-injection.md`
  - Scope: INT-1…6, SEC-2/SEC-3 on the intake side, new `A-intake-*`.
  - Decisions to spell out: Message-ID generation format, Date injection, Injection-Info contents, the Path policy (served Path = stored prefixed Path), unknown-group policy (reject with 437/441, no auto-create), dedupe reply codes.
- **`fixplan-nntp-client`** → `.claude/plans/queued/nntp-client-transfer.md`
  - Scope: CLI-1…6, OPS-3, new `A-nntp-client-*`.
- **`fixplan-nntp-rfc-commands`** → `.claude/plans/queued/nntp-rfc-commands.md`
  - Scope: RFC-1 (DATE, NEXT, LAST, NEWGROUPS, NEWNEWS, OVER/HDR aliases plus message-id forms, LIST OVERVIEW.FMT/HEADERS/wildmat, MODE STREAM + CHECK, STARTTLS).
  - Each in its own handler file.
  - `Depends on: nntp-server-hardening` (shared range parser).

Afterwards (inline):
- Write `.claude/plans/queued/audit-delta-fixes.md` for the critical/high items from `audit-web`, `audit-db`, `audit-tools` and `audit-ops` that WSH doesn't already cover, plus WEB-1, WEB-2, INT-7, OPS-1, OPS-2, OPS-4. Its header states `Depends on: web-sqlite-hardening`.
- Commit the 5 plans on `plan-nntp-audit-tests`.
- Then do the skill's Verify and Finish steps. The Outcome lists the open knownBug ids and the queued plan files.

---

## Checks

Run on every merged tree (after wave 0 and after each merge in waves 2–4):
```bash
gofmt -l ./cmd ./internal            # may list only the 4 baseline files (see Context)
go vet ./...
go build ./...
go test -race -count=1 -timeout 900s ./internal/history/... ./internal/nntp/... ./internal/processor/... \
  ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/...
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u    # must match the known-bug list in AUDIT-2026-09.md
git status --short                   # no active.out, analysis_*.txt, data-test-* or zz_audit_* leftovers
```

## End-to-end

**Runner:** `pugleaf-verifier` on the main checkout (`plan-nntp-audit-tests`), after wave 3.

```bash
./build_nntp-server.sh && ./build_nntpmgr.sh
PORT=21119 DATA=./data-test-nntp-audit-tests scripts/test-nntp-server.sh
```

**What `scripts/test-nntp-server.sh` does:**
- **Guards:**
  - `DATA` must match `./data-test-*`
  - requires `sqlite3`, `timeout`, `nc`, `go`
  - `-nntphostname example.org`, which must resolve. If DNS fails, print `SKIP dns` and exit 2 (hostname validation is a hard startup dependency; see OPS-1).
- **Setup:**
  1. `rm -rf "$DATA"`, then a first start of `build/pugleaf-nntp-server -data "$DATA" -nntptcpport $PORT -nntphostname example.org` to create the schema, wait for the port, then SIGINT.
  2. Seed with `sqlite3 "$DATA/cfg/pugleaf.sq3"`: `INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status,low_water,high_water) VALUES('e2e.test.a','',0,0,1,'e2e','y',1,0),('e2e.test.b','',0,0,1,'e2e','y',1,0)`.
  3. `build/pugleaf-nntpmgr -data "$DATA" -create -username e2euser0001 -password e2epass0001 -posting`.
  4. Start again in the background, logging to `$DATA/server.log`.
- **Checks, via `go test -tags e2e -count=1 -run TestE2EBinary ./cmd/nntp-server/` with `PUGLEAF_E2E_ADDR=127.0.0.1:$PORT`:**
  - N01: greeting 200
  - N02: CAPABILITIES starts with `VERSION 2`
  - N03: AUTHINFO → 281
  - N04: POST with headers → 240
  - N05: within 20 s, GROUP e2e.test.a → `211 1 1 1`
  - N06: `XOVER 1-` → 1 line
  - N07: `ARTICLE 1` body matches
  - N08: `STAT <id>` → 223
  - N09: IHAVE the same id → 435
  - N10: TAKETHIS a new id → 239
  - N11: QUIT → 205
- **DoS probes** (INFO before the fix plans run; they become PASS checks in `nntp-server-hardening`):
  - N20: send `LISTGROUP zz.no.such` then `QUIT` over `nc` (or bash `/dev/tcp`), then count `find "$DATA/db" -name 'zz_no_such.db'`.
  - N21: a 2 MiB line without LF; report the RSS of the server pid before and after from `/proc/<pid>/status`.
- **Shutdown** (before the spin probe):
  - N30: SIGINT → exits within 30 s. Otherwise `kill -9`, FAIL, and record the tail of the log.
- **Spin probe** (last, on a fresh server start):
  - N22: `GROUP e2e.test.a` then `XHDR subject 1-9223372036854775807`. Sample the server's `ps -o %cpu` for 5 s, send SIGINT, wait 10 s, then `kill -9` if it is still alive. Report both CPU and whether it exited.
- Print `SUMMARY pass= fail= info=` and exit with `min(fail,125)`.

**Expected before any fix plan:**
- N01–N11 PASS. N05 may take up to about 3 s because of async commit.
- N30 PASS.
- N20, N21 and N22 report the SEC-2, SEC-3 and SEC-1 behavior as INFO: a file created, RSS growth, and about 100% CPU with no exit on SIGINT.

---

## Appendix B: ownership template for the fix plans

| Fix slice (plan) | Owned production files | Findings |
|----|----|----|
| `nntp-session` (hardening) | `internal/nntp/{nntp-server.go, nntp-server-cliconns.go (except dispatch/CAPABILITIES: inline), nntp-cmd-basic.go, nntp-cmd-auth.go, nntp-server-statistics.go, nntp-cache-local.go}` | SEC-3 (line limit), SEC-4, SEC-5, BUG-7, BUG-9, RFC-2, RFC-4, RFC-5 (MODE/AUTHINFO) |
| `nntp-read` (hardening) | `internal/nntp/{nntp-article-common.go, nntp-cmd-helpers.go, nntp-cmd-group.go, nntp-cmd-list.go}`; new `internal/database/nntp_listgroup.go` (range query); one-line `DBArtNum` fix in `database/queries.go:GetArticleByNum` | SEC-2 (existence check first), SEC-7, BUG-1, BUG-3, BUG-4, BUG-6, BUG-8, RFC-3 |
| `nntp-overview` (hardening) | `internal/nntp/{nntp-cmd-xover.go, nntp-cmd-xhdr.go}`; new `internal/nntp/nntp-cmd-range.go`; `database/queries.go:GetOverviewsRange, GetHeaderFieldRange` (chunked streaming) | SEC-1, BUG-2, BUG-5, RFC-6 |
| `nntp-auth-db` (hardening) | `internal/database/{nntp_auth_cache.go, db_nntp_users.go}`, `cmd/nntpmgr/main.go`. The invalidation lives inside `DeactivateNNTPUser`, `UpdateNNTPUserPassword` and `DeleteNNTPUser` (the cache is keyed or also indexed by user id), and a cache hit re-checks `is_active`. No web files change, which matters because WSH owns `web_admin_userfuncs.go`. | SEC-6, OPS-2 (nntpmgr) |
| `intake` (intake-injection) | `internal/nntp/nntp-cmd-posting.go`, `internal/processor/{threading.go, proc-utils.go}`, `internal/database/db_batch.go` (upsert only) | SEC-2 (intake), SEC-3 (size), INT-1…3, INT-6, RFC-5 (POST/IHAVE codes) |
| `client` (client-transfer) | `internal/common/headers.go` (ReconstructHeaders), `internal/nntp/{nntp-client.go, nntp-client-commands.go, nntp-transfer-demuxer.go, nntp-backend-pool.go, proxy.go}`, `cmd/nntp-transfer/main.go` (pprof) | CLI-1…5, OPS-3 |
| `rfc-commands` (rfc-commands) | new `internal/nntp/nntp-cmd-{date,nextlast,newgroups,newnews,over-hdr,stream,starttls}.go`; dispatch and CAPABILITIES inline | RFC-1 |

- `queries.go` hunks in different slices touch different functions and are merged in slice order, with checks after each merge.
- `internal/web/*` and the other WSH-owned `internal/database/*` files are only touched in `audit-delta-fixes.md`, after WSH.
