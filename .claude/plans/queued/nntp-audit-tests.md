# Plan: NNTP server test suite + subagent audit (security, bugs, missing/wrong functions, RFC conformity)

- **Slug:** `nntp-audit-tests`
- **Integration branch:** `plan-nntp-audit-tests` (from `testing-001`)
- **Run with:** `/run-plan .claude/plans/queued/nntp-audit-tests.md`
- **Status:** re-anchored on 2026-10-08 to `testing-001` @ 84c2081 (originally approved 2026-09-14 @ 08c29f5). Nothing has run yet.
- **Parallelism:** wave 1 has 8 audit slices, wave 2 has 6 test slices, wave 4 has 4 fix-plan slices. Wave 2 doesn't depend on wave 1, so both can go out in one message if the user OKs 14 concurrent agents. Within a wave, no two slices own the same file.
- **Relation to merged plans:** `web-sqlite-hardening.md` (**WSH**, merge 03d0178), `web-sqlite-leftovers.md` (**LO**, e52d1a8) and `web-db-followups.md` (**FU**, f819215) are all merged into `testing-001` and live in `.claude/plans/done/`. NAT shares no owned production file with them (wave 0 touches only `internal/nntp`). `audit-web` and `audit-db` are a delta over all three; see "Already covered by merged plans". Every harness API in D2 was re-checked at 84c2081 and exists unchanged.

---

## Context

### Request and decisions
- **User request:** subagents review the codebase, analyze its security, and find bugs, missing or obviously wrong functions and RFC non-conformity; tests are written for the NNTP server.
- **Decisions (asked during planning):**
  1. Findings go to `docs/audit/` in the repo. The repo is public: this plan never pushes, and the branch shouldn't be pushed before the security fixes land.
  2. Fixes are **not** implemented here. Wave 4 writes detailed fix plans into `.claude/plans/queued/`, at the level of detail of WSH.
  3. Web and DB auditing here is a **delta**. The three merged plans (WSH, LO, FU) hold traced reviews of `internal/web` and `internal/database`; this plan cites their findings as `WSH:<id>`, `LO:<id>`, `FU:<id>` and doesn't re-report them (list under "Already covered by merged plans").
- **Preflight note:** this plan file is tracked. Worktrees are created from the main checkout's HEAD, so worktree agents can only read the plan once its move to `wip/` is committed. Wave 0 therefore `git mv`s it to `.claude/plans/wip/` and commits the move together with the harness, before any agent is spawned.

### State at re-anchoring (`testing-001` @ 84c2081, 2026-10-08)
- The CLAUDE.md checks are green. The gofmt baseline lists 2 files: `internal/database/embedded_migrations.go`, `internal/web/web_admin_provider.go` (K4: gofmt runs only on owned files, so they stay).
- `internal/nntp`, `internal/common` and the intake files of `internal/processor` are unchanged since 08c29f5 except `nntp-backend-pool.go` (`FileCachedListNewsgroups` takes a `cacheDir`). Every server, intake and client seed below was re-traced on 2026-10-08; the `Status` column records what the three merged plans closed.
- NNTP server tests today: `nntp-wiring_test.go` (IHAVE/TAKETHIS wiring and message-id lookup via `local430`, with a fake `wiringTestProcessor` and a bare `&database.Database{}` over `net.Pipe`), `nntp-history-parse_test.go` (parse helpers), `nntp-peering-pattern_test.go`, `lo1_paths_test.go`. No `TestMain` and no real database is opened in package `nntp`; `cmd/nntp-server` has no tests.
  - Untested: greeting, CAPABILITIES, MODE, AUTHINFO, GROUP, LISTGROUP, LIST, XOVER, XHDR, ARTICLE/HEAD/BODY/STAT by number, the accept loop, TLS, shutdown, and `cmd/nntp-server` end to end.
- `internal/database/testmain_test.go` and `internal/web/testmain_test.go` (from WSH) already use the one-`OpenDatabase`-per-binary pattern that D2 proposes: `os.MkdirTemp("", "pugleaf-dbtest-")`, a shared `w0TestDB` behind `w0DB(t)`, unique names from `w0Name(prefix)`. Web's TestMain sets `config.AppVersion`, `processor.LocalNNTPHostname` and `database.GlobalDateParser` before the open. Neither closes `StopChan`, waits on `WG` or calls `Shutdown`; D2's teardown order is stricter and stays.
- Tools: Go 1.27.1 toolchain on a `go 1.25.3` module. `~/go/bin/{staticcheck 2025.1.1, govulncheck 1.1.4, golangci-lint v1.64.8}`. golangci-lint was built with go1.24.3, so it may not load this module (wave 0 saves the load error). `sqlite3`, `curl`, `nc` and `timeout` are installed. Python has no `nntplib`, so NNTP clients are Go `net/textproto`.

### Seed findings (traced in code during planning, re-traced at 84c2081; the audit confirms, rejects or extends them)

Ids here are canonical, and tests use them with `knownBug`. New audit findings get `A-<slug>-<n>`. `Status` is as of 2026-10-08: OPEN, PARTLY (one half closed by a merged plan, the rest open) or FIXED. Paths are under `internal/` unless they start with `cmd/`, `web/static`, `migrations/` or a root file.

**NNTP server: security and DoS**
| ID | Where (at 84c2081) | Defect | Status |
|----|----|----|----|
| SEC-1 | `nntp/nntp-cmd-xhdr.go:85,97`, `database/queries.go:1878-1880` | The DB read is clamped to start+1000 (`endNum = startNum + 1000`), but the send loop at `:97` walks the unclamped client range with no I/O. `XHDR subject 1-9223372036854775807` spins forever (the counter wraps at MaxInt64), pinning a core. No auth is needed. XOVER walks the returned rows, so it truncates (BUG-2) but doesn't spin. | OPEN |
| SEC-2 | `nntp/nntp-cmd-group.go:49` → `database/db_groupdbs.go:117-190` (creation in `initGroupDB` `:194-270`); intake `processor/threading.go:232` → `database/db_batch.go:553,944-950` | `LISTGROUP <any name>` (no auth) and unknown `Newsgroups:` on intake create a directory, SQLite file and schema. Intake also upserts a `newsgroups` row with `active=1` (`query_updateNewsgroupsStats`). Only `handleGroup` (`nntp-cmd-group.go:18`) checks `MainDBGetNewsgroup` first. | PARTLY: web callers FIXED (WSH:C4, LO:F10 — `checkGroupAccess`/`checkGroupAccessAPI` in `web/web_helpers.go`, `sectionGroupAllowed`). NNTP and intake sides OPEN. |
| SEC-3 | `nntp/nntp-server-cliconns.go:73`, `nntp/nntp-cmd-posting.go:224-232` | No byte limits. Command and article lines use unbounded `textproto.ReadLine`, and articles are capped only by line counts (`maxLines, maxHead := 16384, 1024 // HARDCODED`). `Server.NNTP.MaxArtSize` (`config/config.go:131`) and `newsgroups.max_art_size` are never read. | OPEN |
| SEC-4 | `nntp/nntp-server-cliconns.go:112,151` | `Stats.CommandExecuted(command)` stores any word before dispatch, and unknown commands aren't delayed, so the stats map grows without bound. | OPEN |
| SEC-5 | `nntp/nntp-auth-manager.go:47-90`, `nntp/nntp-cmd-auth.go:34,38` | Reading never requires auth. `CheckGroupAccess`, `CheckConnectionLimit` and the `*NNTPSession` DB functions (`database/db_nntp_users.go:193-222`) are unused. AUTHINFO is accepted in plaintext (no STARTTLS, no 483), 481 has no delay (`:34`), and a second AUTHINFO after success is accepted (`:38`; RFC 4643: 502). | OPEN |
| SEC-6 | `database/db_nntp_users.go:137,164,178,281-350`, `database/nntp_auth_cache.go:65-90`, `web/web_admin_nntp.go:169,190,233,277`, `web/web_admin_userfuncs.go:217,229`, `cmd/nntpmgr` | The auth cache (15 min, `NNTP_AUTH_CACHE_TIME` in `db_init.go:22`) keys a hit on the sha256 of the password used at login. `UpdateNNTPUserPassword`, `DeactivateNNTPUser` and `DeleteNNTPUser` never call `InvalidateNNTPUserAuth` (only `web/web_profile.go:129` does), so after a password change the old password keeps working until the TTL expires. The web and NNTP binaries are separate processes (OPS-2), so the fix belongs in the DB layer, not in the admin handlers. | PARTLY: the revoked-user half is FIXED by LO:F22 — every cache hit runs `refuseDisabledNNTPUser` (`:293`, `:338-350`: `nntp_users.is_active` and `users.disabled`, invalidates on refusal). The old-password half is OPEN. |
| SEC-7 | `nntp/nntp-cmd-group.go:57`, `database/queries.go:696-715` | LISTGROUP loads every overview row of the group into memory (unbounded SELECT at `:696`) and logs each call (`:699`). | OPEN |

**NNTP server: bugs and dead code**
| ID | Where | Defect | Status |
|----|----|----|----|
| BUG-1 | `database/queries.go:892-914`, `nntp/nntp-article-common.go:58` | `GetArticleByNum` sets only `ArticleNums[groupDB.NewsgroupPtr]` (`:906`) and never `DBArtNum` (only the message-id path does, `:930`), so ARTICLE/HEAD/BODY `<n>` set `currentArticle=0`. When it happens depends on the article cache. | OPEN |
| BUG-2 | `database/queries.go:1810-1818` (`GetOverviewsRange`), `:1864-1911` (`GetHeaderFieldRange`) | XOVER and XHDR are silently truncated to 1001 rows (clamp in Go, no SQL LIMIT), so readers see phantom gaps. | OPEN |
| BUG-3 | `nntp/nntp-article-common.go:46`, `nntp/nntp-cmd-group.go:10`, `nntp/nntp-cmd-helpers.go:15`, `nntp/nntp-cache-local.go:17` | Hardcoded sleeps: 200 ms per article command, 333 ms per GROUP, 1 s per error, 15 s per `CronLocal430` tick. That is about 5 articles/s per connection. | OPEN |
| BUG-4 | `nntp/nntp-cmd-group.go:29-33,65`, `nntp/nntp-cmd-list.go:36-37` | GROUP reports low=1 and doesn't set the current article. LISTGROUP's 211 line lacks count/low/high/group, doesn't select the group, and ignores the range. LIST ACTIVE reports low=1 and status is always `n`. | OPEN |
| BUG-5 | `nntp/nntp-cmd-helpers.go:67-79`, `common/headers.go:580-601` | Overview fields aren't sanitized for TAB/CR/LF (repeated headers are joined with LF), there is a trailing TAB, and no Xref. | OPEN |
| BUG-6 | `nntp/nntp-cmd-helpers.go:19-22,48-64`, `nntp/nntp-article-common.go:229-248,265-269` | Header lines aren't dot-stuffed. Empty `headers_json` puts a blank line inside HEAD, and an empty body adds an extra blank line. | OPEN |
| BUG-7 | `nntp/nntp-cmd-posting.go:228`, `nntp/nntp-cmd-basic.go:53-57`, `nntp/nntp-server.go:75,96-120,173,202` | "Too large" closes the conn and then still sends 441/436, and QUIT is logged as a connection error (`:173`). `Stop()` neither closes clients nor waits (`:202` says so). `CronLocal430` leaks one goroutine per server, and a TLS cert failure leaves the plain listener running with `running=false`. | OPEN |
| BUG-8 | `nntp/nntp-article-common.go:129-155`, `nntp/nntp-cache-local.go` | `local430` isn't invalidated when the article arrives (only `Check` and `Add` exist): a 430 is served for up to 75 s after 235/239/240. | OPEN |
| BUG-9 | `nntp/nntp-server-cliconns.go:15,54-59,77` | The idle timeout is 60 s (`DefaultNNTPcliconnTimeout`; RFC 3977 §3.1 wants at least 3 min), and the write deadline set per command covers the whole response. | OPEN |
| FN-1 | `nntp/nntp-cmd-{article,head,body,stat,reader}.go`, `nntp/nntp-cmd-helpers.go:24`, `nntp/nntp-server-statistics.go:50-111`, `nntp/nntp-auth-manager.go:47-90`, `nntp/README.md` | Stub files, plus unused functions: `parseArticleHeadersShort`, `ServerStats.{GetTotalConnections,GetCommandCount,GetAllCommandCounts,GetAuthStats,GetUptime,Reset}`, `AuthManager.{CheckGroupAccess,CanPost,IsAdmin,CheckConnectionLimit}`; `ClientConnection.capabilities` is never read. `README.md` points at a non-existent `examples/nntp-server`, names the stub files as the handlers, puts LISTGROUP in `nntp-cmd-list.go`, claims "graceful shutdown with timeout" and auth via `GetUserByUsername`. | OPEN |

**NNTP server: RFC conformity**
| ID | Where | Defect | Status |
|----|----|----|----|
| RFC-1 | `nntp/nntp-server-cliconns.go:115-152` | Missing mandatory READER commands DATE, NEXT, LAST, NEWGROUPS. Also missing: OVER, HDR, LIST OVERVIEW.FMT/HEADERS, LIST wildmat, NEWNEWS, MODE STREAM and CHECK (RFC 4644, although TAKETHIS exists), STARTTLS (RFC 4642). Dispatch today: CAPABILITIES, MODE, AUTHINFO, QUIT, HELP, LIST, GROUP, LISTGROUP, STAT, HEAD, BODY, ARTICLE, XOVER, XHDR, POST, IHAVE, TAKETHIS. | OPEN |
| RFC-2 | `nntp/nntp-server-cliconns.go:196-213` | CAPABILITIES lists READER together with MODE-READER, plus the XOVER/XHDR/TAKETHIS labels. AUTHINFO USER stays listed after auth and without TLS, and POST is listed regardless of permission. STREAMING, OVER, HDR and IMPLEMENTATION are never listed. | OPEN |
| RFC-3 | `nntp/nntp-article-common.go:93-97,159,193-194` | ARTICLE/HEAD/BODY/STAT with no argument → 501 (should use the current article, or 420). Number 0 or negative → 502. | OPEN |
| RFC-4 | `nntp/nntp-server-cliconns.go:93-99`, `nntp/nntp-cmd-basic.go:22-23` | The greeting and MODE READER always say 200 "posting allowed", even without a processor (should be 201). The hostname is hardcoded (`"go-pugleaf"`; `Config.Hostname` is never read here). | OPEN |
| RFC-5 | `nntp/nntp-cmd-basic.go:25`, `nntp/nntp-cmd-auth.go:45`, `nntp/nntp-cmd-posting.go:27,95,109` | Unknown MODE/AUTHINFO variant → 500 (should be 501). POST not permitted → 502 (should be 440). IHAVE permanent rejects → 436 (should be 437). | OPEN |
| RFC-6 | `nntp/nntp-cmd-xhdr.go:52-73,92`, `nntp/nntp-cmd-xover.go:86`, `database/queries.go:1864-1911` | XHDR supports only 7 headers (an unknown one gives an empty 221, `:1910`), has no "(none)" and no message-id form (fails `ParseInt` → 501, not rate limited). An empty XOVER range → an empty 224. | OPEN |

**Article intake (RFC 5536/5537)**
| ID | Where | Defect | Status |
|----|----|----|----|
| INT-1 | `processor/threading.go:59-61,155-160,185-192`, `nntp/nntp-cmd-posting.go:42-43`, `nntp/nntp-cmd-helpers.go:19-22` | POST without Message-ID or Date → 441; the injecting agent must add them (RFC 5537 §3.5). No Injection-Date/Info or `.POSTED`. The Path prefix is stored only in the `path` column; HEAD/ARTICLE serve the raw stored headers with the original Path. | OPEN |
| INT-2 | `processor/threading.go:76-81,270-283`, `processor/processor.go:187-213` | An article already in every target group → CasePass, so IHAVE answers 235 and TAKETHIS 239. An in-flight message-id → CaseDupes instead of Retry (the `CheckMessageID` pre-check returns Retry, but the window between it and `processArticle` answers Dupes). | OPEN |
| INT-3 | `processor/proc-utils.go:414-454,756-787`, `common/headers.go:24-30,539`, `nntp/nntp-cmd-posting.go:283` | A malformed Date is accepted as 1990-01-01. Newsgroups is split on `[,;:\s]+` with a lax name regex (`validGroupNameRegexLazy`). The POST path splits Newsgroups with a plain `strings.Split(",")`, differently from `processArticle`. | OPEN |
| INT-4 | `processor/threading.go`, `nntp/nntp-client-commands.go:936-939,950` | No control messages, Supersedes, moderation or Approved check. A foreign Xref is stored and served, and no local Xref is generated (XOVER sends an empty field). | OPEN |
| INT-5 | `nntp/nntp-client-commands.go:877`, `migrations/0001_single_db_schema.sql:12` | `bytes` is the LF-joined body length (RFC 3977 §8.1.1: the whole article with CRLF). Article numbers are rowids without AUTOINCREMENT, so they can be reused. | OPEN |
| INT-6 | `database/db_batch.go:855-891` vs `:553,944-950` | The history adds are gated on `MainDBGetNewsgroup` (which returns `sql.ErrNoRows` for a new group), but the row is only created later in the same batch by `query_updateNewsgroupsStats`, so the first batch of an auto-created group skips them. | OPEN |
| INT-7 | `web/web_sitePostPage.go:358-377`, `processor/PostQueue.go:93` | Web posts are stored with only MIME-Version, Content-Type, CTE, X-pugleaf-Trace, From, Newsgroups, Lines and Bytes in `headers_json` — no Subject, Date, Message-ID, Path or References — and NNTP serves them that way. | OPEN |

**NNTP client, transfer and peering**
| ID | Where | Defect | Status |
|----|----|----|----|
| CLI-1 | `common/headers.go:267-297,393-411,487`, `nntp/nntp-client-commands.go:1149-1153,1271-1275` | `ReconstructHeaders` sends two Path headers on every TAKETHIS (`:291` and `:294`). Folded Newsgroups lines are sent twice (`i++` inside the range at `:403`), and a final empty body line is dropped. | OPEN |
| CLI-2 | `nntp/nntp-client-commands.go:636-641` | Nil dereference (panic) when GROUP returns 411 in `XHdrStreamedBatch`. | OPEN |
| CLI-3 | `nntp/nntp-client.go:193-232,345-362`, `nntp/proxy.go:59-60`, `nntp/nntp-client-commands.go:78` | No read/write deadlines (the helpers are dead code, renamed `xSetReadDeadline`/`xSetWriteDeadline`; the one client deadline at `:78` is commented out), no TLS handshake timeout, and the socket leaks on connect errors (`:214,219,232` return after `c.conn = conn`). | OPEN |
| CLI-4 | `nntp/nntp-transfer-demuxer.go:185-191` | The demuxer expects the first CHECK to be command id 1 (`d.LastID+1 != cmdInfo.CmdID`, `LastID` starts at 0), but AUTHINFO + MODE STREAM use ids 0–2, so `-username` transfers stall. | OPEN |
| CLI-5 | `nntp/nntp-client.go:211-219,251-259`, `nntp/nntp-backend-pool.go:189-201`, `nntp/nntp-client-commands.go:51-54,150-153,206-209,1231-1232` | A 201 greeting fails `Connect` (`ReadCodeLine(NNTPWelcomeCodeMin)`). A 281 reply to AUTHINFO USER counts as failure. 480 is reported as "group not found". The 430/451 branches are unreachable in `StatArticle`, `GetHead` and `GetBody`. The 401 check never matches. | PARTLY: `GetArticle` (`:97`) does reach its 430/451 branches. The rest is OPEN. |
| CLI-6 | `nntp/nntp-peering.go:165-173,196-204,248-263,291-305,341-345,413-418`, `cmd/test-nntp` | `PeeringManager` is unused and unfinished: `LoadConfiguration` is a stub, the DNS limiter deadlocks (the 1-slot channel is taken twice), `AddPeer` keeps pointers into a slice that append reallocates and returns the loop copy. `test-nntp` builds, is hardcoded to an external server, and its calls to APIs that no longer exist sit in a comment block. | OPEN |

**Web, not covered by the merged plans**
| ID | Where | Defect | Status |
|----|----|----|----|
| WEB-1 | `web/web_admin_crons.go:18,148,307`, `web/webserver_core_routes.go:471-474` | Create, toggle and stop only check `requireAdminAuth`, not `s.CronEdit` (update `:79` and delete `:183` do), so admins can create and enable `sh -c` jobs without `-edit-cronjobs`. LO put the flag itself out of scope; no merged plan touched these three handlers. | OPEN |
| WEB-2 | `web/web_apiHandlers.go:448-502`, `web/static/js/thread-tree.js:514-555`, `web/webserver_core_routes.go:502` | `getArticlePreview` returns the body through `models.ConvertToUTF8` (`html.UnescapeString`, `models/sanitizing.go:48`) with no escaping (`:488-498`; subject and from are `PrintSanitized`). thread-tree.js concatenates `article.body` and `article.group` into HTML (`:515`) and inserts it with `innerHTML` (`:539`); `escapeHtml` (`:557`) is never called on it and `formatPreviewText` only normalizes newlines. Since LO:B11 the JS fetches the **web** route `GET /groups/:group/articles/:articleNum/preview`, which is not behind `requireAPIEnabled`. Stored XSS candidate. WSH:S2 did not cover it. | OPEN, severity up (reachable with the API disabled) |

**Process and ops**
| ID | Where | Defect | Status |
|----|----|----|----|
| OPS-1 | `database/db_init.go:141,145,206-229`, `config/config.go:548`, `processor/processor.go:63-88`, `history/history.go:469,498`, `processor/analyze.go:934-962`, `database/db_batch.go:1753-1765`, `database/db_migrate.go:25` | Library code kills the process (`log.Fatal`/`os.Exit`). The DB can't be reopened in one process (`INIT` is never reset, the global `BatchDividerChan` divider loop never exits, `migratedDBsCache` outlives the DB). | PARTLY: "the batch retries forever after `Shutdown`" is FIXED (LO/FU: `batchShutdownGrace`, `batchShutdownClock`, `retryShutdownGrace()`, `IsDBshutdown()` at `db_init.go:270` bound every retry loop). The fatal sites and the re-open globals are OPEN. |
| OPS-2 | `cmd/web/main.go:105,428`, `cmd/nntpmgr/main.go:153,183-186`, `nntp/nntp-peering-pattern_test.go:409-410,557-558,607-608` | The embedded NNTP server in cmd/web can't start (`-withnntp` is commented out, so `&& withnntp` is always false). `nntpmgr -list` prints bcrypt hashes (`%-16s` pads, doesn't truncate) and `-create` echoes the plaintext. A test writes `active.out` and `analysis_*.txt` into the cwd (only when a hardcoded `/home/fed/...` active file exists). | OPEN |
| OPS-3 | `cmd/nntp-transfer/main.go:12,544-551,2934-2940`, `cmd/history-rebuild/main.go:12,75,97-101` | `net/http/pprof` is registered on the default mux, which `-web-port` serves on all interfaces (`http.ListenAndServe(":%d", nil)`); the pprof-only listener is localhost. history-rebuild's `-pprof` accepts any address. | OPEN |
| OPS-4 | `cmd/history-rebuild/main.go:74-79`, `cmd/tcp2tor/main.go:177,252,503,551`, `cmd/web/main_functions.go:826`, `run_web*.sh`, `scripts.sh`, `run_test.sh`, `build_*.sh`, `.github/workflows/release.yml` | Flags that are ignored (`-useshorthashlen` says "No effect"; tcp2tor `-timeout` is stored and never read, the dialers use `proxy.Direct`). Scripts pass flags that don't exist or reference missing files. 11 build scripts use `-race`. The only workflow (`release.yml`, tags/dispatch) runs `go mod download/verify` and builds, with no test or vet step. | PARTLY: web `-data` is FIXED (WSH; `cmd/web/main.go:214`; `findOrphanedDatabases` keeps a `"data"` fallback for an empty argument). The rest is OPEN. |

**Already covered by merged plans (not re-reported; cite as `WSH:<id>`, `LO:<id>`, `FU:<id>`):**
- **WSH** (`.claude/plans/done/web-sqlite-hardening.md`, findings table at its lines 16-66; all merged): C1 expired-token nil deref, C2 `-no-cronjobs` crash, C3 sections map race, C4 `GetGroupDB` for any name from web routes, C5 BadBots RLock across `c.Next()` and Ollama without timeout, C6 no `http.Server` timeouts; H1 client IP spoofing, H2 open redirect, H3 email login/enumeration/lockout, H4 registration session, H5 bcrypt 73+ byte passwords, H6 `GetGroupDB` vs idle-close TOCTOU, H7 group DB init failure spins waiters, H8 PRAGMAs on one pooled conn, H9 `synchronous=OFF` and non-atomic migrations, H10 header injection from display name/subject/message-id, H11 session id in chat JS, H12 disabled users keep sessions; P1 templates parsed per request, P2 per-page UPDATEs/SELECTs, P3 `MainMutex` per lookup, P4 unbounded SQLite retries, P5 deep OFFSET paging, P6 unindexed LIKE search, P8 redundant indexes (no P7); S1 CSRF, S2 unescaped `groupName` in tree `template.HTML`, S3 spam-counter/back-off races, S4 section tree group name; L2 session cleanup never started, L3 bad-bot patterns not lowercased, L4 stats logger/CronDB ignore StopChan, L5 bubble sort and nil deref in cleanup, L6 preview truncation not rune-safe, L7 `knownPaths` per request. L1 (`getContentType`) became LO:B9.
  - Non-test files WSH changed: `internal/database/{database, db_aimodels, db_apitokens, db_groupdbs, db_init, db_migrate, db_sections, db_sessions, db_spam_flags, db_threads_paged, db_web_posting, queries, sqlite_retry, tree_view_api, ui_cache}.go`, migrations 0001/0009/0027; `internal/web/{cronjobs, web_adminPage, web_admin_crons, web_admin_userfuncs, web_aichatPage, web_apiHandlers, web_apitokens, web_articlePage, web_auth, web_groupThreadsPage, web_groupsPage, web_helpPage, web_helpers, web_hierarchiesPage, web_homePage, web_ircPage, web_login, web_newsPage, web_profile, web_registerPage, web_searchPage, web_sectionsPage, web_session_cleanup, web_sitePostPage, web_statsPage, web_templates, web_threadPage, web_threadTreePage, web_utils, webgroupPage, webserver, webserver_core_routes}.go`.
- **LO** (`web-sqlite-leftovers.md`, lines 16-89; everything shipped except F1 → FU:D1): A1 fetcher progress DB ignores `-data`, A1b fetcher panic without provider, A2 pool newsgroup-list cache path, A3 analyze cache path, A4 rsync source path, A5 `ResetAllNewsgroupData` layout, A6 `SanitizeGroupName` collisions (test only); B1 shutdown doesn't drain handlers, B2 concurrent chat sends, B3 full post queue charges back-off, B4 getStats thundering herd, B5 `defer Return()` in loop, B6 token-usage goroutine per request, B7 internal error text shown, B8 template cache key, B9 `getContentType`/favicon HEAD, B10 dead `WebAuthRequired`/`HandleThreadTreeAPI`/`initializeThreadCacheSimple`, B11 preview API ignored `APIEnabled` (a web preview route was added — WEB-2's sink moved there), B12 `internal/web/README.md`, B13 `docs/web-deployment.md`; C1 `users.session_id` hashed (migration 0028), C2 lockout window, C3 `SQLiteMaxRetryWait` setter, C4 thread_cache ANALYZE, C5 REPLACE cascade comment, C6 main DB pool measured; D1 `cmd/audit-web-posts`; E1 CLAUDE.md test list; F2 session idle timeout, F3 `ReverseProxyIPHeader`, F4 `DefaultReverseProxy` ranges, F5/F6 display name, F7 reply message-id regex, F8 spurious "No valid newsgroups", F9 `X-Next-Offset` bound, F10 section routes don't recreate deleted/inactive group DBs, F11 chat JSON body bound, F12 Ollama timeout, F13 cron loop error, F14 `UpdateBadBots("")`, F15 huge `?page=`, F16 batch dropped articles on init wait, F17 retry cap origin, F18 spam flag RowsAffected, F19 PRAGMA strip regex, F20 CronDB/batch drain after StopChan, F21 rslight LastInsertId, F22 NNTP auth re-checks disabled/inactive on cache hits (SEC-6 first half).
  - Non-test files LO changed: `internal/database/{database, db_apitokens, db_batch, db_groupdbs, db_migrate, db_nntp_users, db_rescan, db_sections_upsert, db_sessions, db_spam_flags, db_web_posting, queries, sqlite_retry, thread_cache, tree_view_api}.go`, migration 0028; `internal/web/{README.md, cronjobs, embedded_static, static/js/thread-tree.js, web_admin, web_adminPage, web_admin_ollama, web_admin_settings_unified, web_aichatPage, web_apiHandlers, web_apitokens, web_auth, web_groupThreadsPage, web_groupsPage, web_hierarchiesPage, web_profile, web_registerPage, web_searchPage, web_sectionsPage, web_sitePostPage, web_templates, web_threadPage, web_threadTreePage, webserver, webserver_core_routes}`; `cmd/audit-web-posts`, `docs/web-deployment.md`, `docs/perf/main-db-pool.md`, `scripts/test-web-leftovers.sh`.
- **FU** (`web-db-followups.md`, lines 22-77; 23 closed, 6 unassigned): A1 profile writes in one transaction, A2 `ResetAllNewsgroupData` bare Exec, A3 rsync `rows.Err()`, A4 `DeleteNewsgroup` spam flags; B1 dead `EmbeddedFileHandler`, B2 dead `InvalidateUserSessionBySessionID`, B3 admin delete flash; C1 batch retry bounds test, C2 cron start after Stop, C3 nntp-analyze `Shutdown`; D1 page-101 clamp honest, D2 `audit-web-posts -strict`; E1 section listing orphans, E2 thread reply count, E3 check label, E4 empty page link, E5 thread_cache `message_count`, E6 dead threading retry2, E7 `child_articles` overwrite on read error, E8 `UpdateUserProfile` single owner, E9 `BulkDeleteNewsgroups` dependents, E10 misc, E11 dead `UpdateThreadCache`. **Unassigned, inherited by `audit-delta-fixes.md` as-is:** E12 cursor mode links `?page=0`, E13 `BulkDeleteNewsgroups` not under `RetryableTransactionExec`, E14 thread page fetches before clamping, E15 `spam` table outlives a deleted group, E16 `GetCachedThreadReplies` swallows scan errors and skips `rows.Err()`, E17 in-range page past the end renders page 1.
  - Non-test files FU changed: `internal/database/{db_batch, db_sessions, db_user_profile, queries, thread_cache}.go`, `thread_cache.README.md`; `internal/web/{README.md, cronjobs, embedded_static, web_admin_newsgroups, web_profile, web_sectionsPage, web_threadPage, webgroupPage}`.

---

## Design

### D1 Audit reports (`docs/audit/`)
- **Files.**
  - One report per audit slice: `docs/audit/<slug>.md`.
  - The consolidated report is `docs/audit/AUDIT-2026-10.md`.
  - The schema lives in `docs/audit/README.md`; tool output goes in `docs/audit/tools/`. `docs/audit/` doesn't exist yet (`docs/` holds `history-index-implementation-patch.md`, `perf/`, `PostQueue.md`, `web-deployment.md`).
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
    - No NULLs. The table also has `spam` and `hide` (`INTEGER DEFAULT 0 NOT NULL`, present since before 08c29f5; no column was added since), which the INSERT may omit.
    - `headers_json` is the header lines joined with `"\n"`; `body_text` is LF-joined and dot-unstuffed.
    - Then update `newsgroups.last_article`, `message_count` and `high_water` in one statement.
  - Users: `db.InsertNNTPUser` (username at least 10 characters, `IsActive: true`, `WebUserID: 0`).
- **Anchors at 84c2081** (what the hooks attach to; all re-checked 2026-10-08):
  - `nntp-server.go`: `ArticleProcessor` interface `:18-26` (`ProcessIncomingArticle`, `CheckMessageID`, `FindArticleByMessageID`); `NNTPServer` struct `:37-50` (`Config, DB, Listener, TLSListener, AuthManager, Stats, Processor, shutdown chan struct{}, wg *sync.WaitGroup, mu sync.RWMutex, local430, running bool`); `NewNNTPServer(db, cfg, mainWG, processor) (*NNTPServer, error)` `:53` (starts `CronLocal430` at `:75`); `Start()` `:80` (`net.Listen(":%d")`, `s.wg.Add(1); go s.serve(l, false)` `:97-98`, TLS block `:102-121`, `running = true` `:123`); unexported accept loop `serve(listener net.Listener, isTLS bool)` `:129` (`MaxConns` check `:149`, `go s.handleConnection` `:156-157`); `handleConnection` `:163`; `Stop()` `:178`; `IsRunning()` `:219`. There is no exported `Serve` yet. `wg` is the caller's WaitGroup (`cmd/nntp-server/main.go:90,126,133,145,148`).
  - `nntp-server-cliconns.go`: `DefaultNNTPcliconnTimeout` var `:15` (60 s); `NewClientConnection(conn, server, isTLS)` `:37`; `UpdateDeadlines` `:54-59` (both deadlines from the var; also referenced at `nntp-server.go:171`); `Handle` `:62`; `handleCommand` `:102`.
  - `nntp-cmd-helpers.go:12-16` `rateLimitOnError` (plain `time.Sleep(time.Second)`); `nntp-article-common.go:46` (`time.Sleep(time.Second / 5)`); `nntp-cmd-group.go:10` (`time.Sleep(time.Second / 3)`).
  - `cmd/nntp-server/processor_adapter.go:9-38`: `ProcessorAdapter`/`NewProcessorAdapter` implement the 3 interface methods plus `CheckNoMoreWorkInHistory`.
  - Database (all exist, signatures unchanged): `BatchInterval time.Duration` (3 s), `ENABLE_ARTICLE_CACHE`, `NO_CACHE_BOOT`, `INIT` (bools, `db_init.go:20-22,145`); `OpenDatabase(*DBConfig)` takes `GlobalDBMutex`, euid check `log.Fatal` at `db_init.go:141`, `INIT` guard right after; `StopChan chan struct{}` (cap 1), `WG *sync.WaitGroup` (`Add(2)` inside open), `Shutdown()` closes the DBs only; `Batch *SQ3batch` with `SetProcessor(ProcessorInterface)` (needs `CheckNoMoreWorkInHistory`, `AddArticleToHistory(string, int64)`, `ForceCloseGroupDB(*GroupDB) error` — the real `*processor.Processor` satisfies it); `GetGroupDB(name) (*GroupDB, error)` with `.DB *sql.DB`, callers `defer groupDB.Return()`; `MainDBGetNewsgroup` returns `sql.ErrNoRows` when absent; `InsertNewsgroup`, `InsertNNTPUser` (`WebUserID 0` stored as NULL), `DeactivateNNTPUser`, `UpdateNNTPUserPassword`, `AuthenticateNNTPUser`, `InvalidateNNTPUserAuth`, `GetArticleByNum`, `GetArticleByMessageID` (`queries.go:892,919`).
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

  Harness identifiers start with `h`, `newTestServer`, `knownBug` or `runIsolated`. Already taken in package `nntp`: `Wiring*`/`wiringTest*` (`nntp-wiring_test.go`), `NNTPHist*` (`nntp-history-parse_test.go`), `Lo1Paths*` (`lo1_paths_test.go`) and the unprefixed peering tests (`parseActiveFile`, `min`).
- **K3: test data.**
  - Only wave 0 creates `TestMain` in `internal/nntp`; `test-nntp-intake-e2e` creates the one in `cmd/nntp-server`.
  - Groups are named `t<N>.<test>.<seq>` and message-ids `<t<N>-<test>-<seq>@test.invalid>`. Users are `t<N>user<seq>` padded to 10 or more characters. `N` is the slice number 1–6.
  - No `t.Parallel()`. No package global is mutated after `TestMain`; use per-server fields.
  - No sleep longer than 100 ms; poll with a deadline instead.
- **K4:** nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt` or production code outside the wave-0 hooks. `gofmt` runs only on owned files; the 2-file gofmt baseline stays.
- **K5:** audit slices commit only their report and follow D1's repro rules. Test slices commit only their test files (plus, for T6, its script).
- **K6: ids.**
  - Seed ids are canonical and never renumbered.
  - New findings from audit slices are `A-<slug>-<n>`.
  - Bugs a test slice finds that aren't in the seed list are `T-<slug>-<n>`; the slice lists them in its report, and wave 3 adds them to the report.
  - Findings of the merged plans are referenced as `WSH:<id>`, `LO:<id>` (web-sqlite-leftovers) and `FU:<id>` (web-db-followups).

### D4 Fix plans (wave 4 output)
- **Queue files.** Each follows the WSH structure: header, Context with a findings table, decisions, out of scope, Design with contracts and reserved names, Waves with disjoint owned files and exact changes, the tests that remove `knownBug` markers, acceptance E-checks, Checks, and End-to-end.
  | File | Covers |
  |----|----|
  | `nntp-server-hardening.md` | SEC-1…7 (server side), BUG-1…9, RFC-2…6, FN-1, and new server findings |
  | `nntp-intake-injection.md` | INT-1…6, SEC-2/SEC-3 on the intake side |
  | `nntp-client-transfer.md` | CLI-1…6, OPS-3 |
  | `nntp-rfc-commands.md` | RFC-1 (new commands) |
  | `audit-delta-fixes.md` | new critical/high findings from audit-web/db/tools/ops, plus WEB-1, WEB-2, INT-7, OPS-1/2/4 and the unassigned FU:E12–E17 (carried over as-is, not re-found) |
- **Coordination rules** (Appendix B):
  - NNTP handlers check group existence with `MainDBGetNewsgroup` (returns `sql.ErrNoRows`) before `GetGroupDB`. `GetGroupDB`'s auto-creation (`db_groupdbs.go:194-270`) is shared with the batch writer and stays as it is; the SEC-2 fix is the existence check in the callers.
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
1. **Preflight** as in the skill (`git mv .claude/plans/queued/nntp-audit-tests.md .claude/plans/wip/`). Run the baseline `## Checks` and record PASS/FAIL in the progress note; gofmt may list only the 2 baseline files.
2. **Static analysis.** Run `go vet ./...`, `~/go/bin/staticcheck ./...`, `~/go/bin/govulncheck ./...` and `~/go/bin/golangci-lint run --enable gosec --timeout 10m`. Save each output (or the load error) to `docs/audit/tools/<tool>.txt`. If an output has more than 5000 lines, keep the first 5000 plus a per-linter count.
3. **Audit README.** Write `docs/audit/README.md` from D1 and K6, including the slice table below and the seed id tables (copied from Context).
4. **Hooks.** Implement the D2 production hooks. Keep default behavior identical: `NewNNTPServer` seeds the defaults, and `Start()` still binds `:%d`.
5. **Harness.** Write the harness from D2 with 4 self-tests:
   - `TestHarnessSmoke`: greeting, CAPABILITIES (multi-line), QUIT → 205.
   - `TestHarnessSeed`: `hGroup` with 3 articles, then GROUP → 211 with count 3.
   - `TestHarnessKnownBug`: a table test of the pure decision function `hKnownBugAction(err error, envSet bool) (action, msg)`. `knownBug` is only a thin wrapper around it, because a `Fatalf` branch can't be exercised inside the same test.
   - `TestHarnessIsolatedTimeout`: a child that sleeps 30 s gives `timed out` within about 3 s.
6. **Checks:** `gofmt -l internal/nntp`; `go vet ./...`; `go build ./...`; `go test -race -count=1 -timeout 300s ./internal/nntp/...`. The existing wiring, parse and peering tests must stay green, and nothing may be written into the source tree.
7. **Commit** with `test(nntp): wave-0 harness, per-server delay hooks, audit schema`, and include the plan's move to `.claude/plans/wip/nntp-audit-tests.md` (staged by the `git mv` in step 1) so worktree agents can read the plan. Record the base SHA.

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
- **Seeds:** SEC-1…7, BUG-1, BUG-2, BUG-7, BUG-8. For SEC-6, confirm the old-password half with a repro; the revoked-user half is LO:F22 (`refuseDisabledNNTPUser`) — verify it, don't re-report it.
- **The report must add:**
  - `## Unauthenticated attack surface`: command → resources touched (files, memory, goroutines, CPU) → limit today → proposed limit.
  - `## Measured repros`: CPU% for SEC-1, file count for SEC-2, RSS for SEC-3/SEC-4/SEC-7, each with commands and numbers.
  - `## Races`: `go test -race` on a temporary stress test (100 concurrent clients doing GROUP/XOVER/ARTICLE/STAT/QUIT).
  - `## TLS and shutdown`: `tls.Config` defaults, `Stop()` behavior with live clients, and the leak on cert failure.

#### Slice `audit-web`: delta web audit (after WSH, LO and FU)
- **Scope:** all `internal/web/*.go`, `internal/web/static/**`, `web/templates/*.html`, `internal/{models,cache,utils}`, and `internal/database/{db_apitokens.go, db_sessions.go, db_sections.go, sanitize_cache.go, sections_cache.go, tree_cache.go, tree_view_api.go}`.
- **Excluded:** every WSH, LO and FU finding ("Already covered by merged plans" in Context). Read the three done plans' findings tables first and cite `WSH:<id>`/`LO:<id>`/`FU:<id>` instead of re-reporting. The changed-file lists there say which files were reviewed three times; spend the time on the rest (`web_admin_crons.go`, `web_admin_nntp.go`, `web_admin_newsgroups.go`, `web_admin_provider.go`, `web_admin_spam*.go`, `web_admin_postqueue*.go`, `web_admin_sitenews*.go`, `web_admin_sections*.go`, `web_admin_hierarchies*.go`, `web_apiHandlers.go` beyond the preview, the static JS, and the templates).
- **Seeds:** WEB-1, WEB-2 (now reachable through the web preview route added by LO:B11), INT-7.
- **Focus:**
  - output escaping and DOM sinks (`innerHTML`, `template.HTML`, `safeHTML`/custom funcs, URL and attribute contexts, JS string contexts in templates)
  - `models/sanitizing.go` correctness
  - a per-route guard table for admin handlers not changed by the merged plans (crons create/toggle/stop, nntp users, provider, spam, postqueue, sitenews, hierarchies, apitokens)
  - cron gating, API surface and data exposure, cache key collisions (`models/cache.go`), UTF-8 slicing outside WSH:L6
- **The report must add** `## Route guard table` and `## Sink table`.

#### Slice `audit-db`: delta database audit (after WSH, LO and FU)
- **Scope:** `internal/database/{db_batch.go, queries.go, database.go, db_init.go, db_groupdbs.go, db_migrate.go, embedded_migrations.go, migrations/*.sql, sqlite_retry.go, groups_hashmap.go, utils.go, users.go, hierarchy_cache.go, db_config.go, config_cache.go, db_cron_jobs.go, db_aimodels.go, thread_cache.go, article_cache.go, db_rescan.go}`.
- **Excluded:** every WSH, LO and FU finding ("Already covered by merged plans"); in this scope mainly WSH H6–H9, P3, P4, P6, P8, L4, L5; LO C3–C5, F16, F17, F20, F22; FU A2–A4, C1, E5–E9, E11. `db_batch.go`, `thread_cache.go`, `queries.go`, `db_groupdbs.go`, `sqlite_retry.go` and `db_nntp_users.go` were reworked and tested by those plans (`fu_batch*_test.go`, `lo2_writer_test.go`, `w1_sqlite_test.go`); audit what they didn't touch and what the tests don't cover.
- **Seeds:** SEC-2 (DB side), SEC-6 (DB side: `nntp_auth_cache.go` password keying), BUG-1, BUG-2, INT-5, INT-6, OPS-1 (DB side). Known, not to be re-found: FU:E13 (`BulkDeleteNewsgroups` uses a plain transaction), FU:E15, FU:E16.
- **Focus:**
  - batch writer: correctness, auto-created `newsgroups` rows with NULL `hierarchy`, history gating (INT-6); shutdown bounds are done (`batchShutdownClock`) — verify, don't re-audit
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
- **SEC-6:** authenticate (cached), `DeactivateNNTPUser`, authenticate again → must fail. This passes today (LO:F22, `refuseDisabledNNTPUser` on every cache hit): plain assertion, no `knownBug`. Then, on a fresh active user: authenticate (cached), `UpdateNNTPUserPassword`, authenticate with the **old** password → must fail; today it succeeds until the 15-min TTL, so wrap it in `knownBug(t, "SEC-6", err)`.
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

**`scripts/test-nntp-server.sh`** implements `## End-to-end`, following `scripts/test-web-leftovers.sh` (the newer of the two existing scripts):
- `set -u`; `cd "$(git rev-parse --show-toplevel)"`; `PORT`/`DATA`/`BIN` from the environment with defaults
- `DATA` guards: refuse `..`, require `./data-test-*`; refuse to run if `./.update` exists; refuse if something already answers on the port
- tool loop `for t in sqlite3 timeout nc go; do command -v "$t" >/dev/null || { echo "missing tool: $t"; exit 2; }; done`
- `pass()`/`fail()`/`info()` helpers printing `PASS|FAIL|INFO <id> [<area>] <text>`
- `stop_server`: SIGINT, wait up to 90 s, then KILL; `trap stop_server EXIT`
- `SUMMARY pass=$PASSES fail=$FAILS info=$INFOS data=$DATA log=$LOG`, `exit min(fail,125)`; setup errors exit 2

### Wave 3 (inline): consolidate

**Owned files:** `docs/audit/AUDIT-2026-10.md` (new), `BUGS.md`, `README.md` (nntp-server status lines only), `.claude/CLAUDE.md` (Checks list), and `.github/workflows/tests.yml` (new, only if the user agrees at this wave boundary).

1. **Merge.** If the skill's step 5 hasn't already merged them, merge the wave 1 and wave 2 branches: audits first (docs only), then the tests in slice order. Run `## Checks` after each test merge.
2. **Write `AUDIT-2026-10.md`:**
   - counts by severity, category and area; the 25 most urgent findings
   - all findings, deduplicated across slices, with canonical ids, `WSH:` cross-references and the fix plan each belongs to
   - the RFC command matrix (from `audit-nntp-rfc`); the unauthenticated attack surface (from `audit-nntp-sec`)
   - the function inventory (merged); static-analysis highlights
   - rejected seeds
   - the known-bug list (from `PUGLEAF_KNOWN_BUGS=1` output) with the test name for each
3. **Update docs.**
   - `BUGS.md`: replace the five bullets of `# nntp-server (low priority)` (lines 14-19) with the open critical and high ids, a link to the report, and the statement that the XHDR/LISTGROUP DoS must be fixed before any public deployment.
   - `README.md`: line 11 (`Full NNTP server implementation (RFC 3977 compliant) *TODO*`) and line 147 (`nntp-server - Standalone NNTP server (reading works partially, posting not yet)`) get "RFC 3977 partial, see docs/audit".
   - `.claude/CLAUDE.md`: append `./cmd/nntp-server/...` to the `go test -race` list (it already has database, web, history, nntp, processor, expire-news, history-rebuild).
4. **Optional CI.** With the user's OK, add `.github/workflows/tests.yml` next to the existing `release.yml` (tags/dispatch, build only): on push and pull_request, `actions/setup-go` with `go-version-file: go.mod`, `go vet ./...`, `go build ./...`, `go test -race -timeout 600s` on the tested packages, plus a `known-bugs` job with `continue-on-error: true` running `PUGLEAF_KNOWN_BUGS=1`.
5. **Commit.**

### Wave 4: fix plans (4 `pugleaf-implementer` slices, each owns one plan file; `audit-delta-fixes.md` is written inline by the orchestrator afterwards)

Rules for every slice:
- Read `AUDIT-2026-10.md`, the findings in scope, the code at `plan-nntp-audit-tests` HEAD, and `.claude/plans/done/web-sqlite-hardening.md` (WSH, for the format).
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
  - owned files within each wave are disjoint
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
- Write `.claude/plans/queued/audit-delta-fixes.md` for the critical/high items from `audit-web`, `audit-db`, `audit-tools` and `audit-ops` that the merged plans don't already cover, plus WEB-1, WEB-2, INT-7, OPS-1, OPS-2, OPS-4 and FU:E12–E17 (copied from `web-db-followups.md` lines 22-77 with their ids kept).
- Commit the 5 plans on `plan-nntp-audit-tests`.
- Then do the skill's Verify and Finish steps. The Outcome lists the open knownBug ids and the queued plan files.

---

## Checks

Run on every merged tree (after wave 0 and after each merge in waves 2–4):
```bash
gofmt -l ./cmd ./internal            # may list only the 2 baseline files (see Context)
go vet ./...
go build ./...
go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
  ./internal/nntp/... ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/...
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u    # must match the known-bug list in AUDIT-2026-10.md
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
| `nntp-auth-db` (hardening) | `internal/database/{nntp_auth_cache.go, db_nntp_users.go}`, `cmd/nntpmgr/main.go`. The invalidation lives inside `UpdateNNTPUserPassword`, `DeactivateNNTPUser` and `DeleteNNTPUser` (the cache is keyed or also indexed by user id), or `Get` re-checks the stored hash on a hit. The `is_active`/`disabled` re-check already exists (`refuseDisabledNNTPUser`, LO:F22). No web files change: the admin handlers run in a different process from the NNTP server, so a DB-layer fix is the only one that works. | SEC-6 (old-password half), OPS-2 (nntpmgr) |
| `intake` (intake-injection) | `internal/nntp/nntp-cmd-posting.go`, `internal/processor/{threading.go, proc-utils.go}`, `internal/database/db_batch.go` (upsert only) | SEC-2 (intake), SEC-3 (size), INT-1…3, INT-6, RFC-5 (POST/IHAVE codes) |
| `client` (client-transfer) | `internal/common/headers.go` (ReconstructHeaders), `internal/nntp/{nntp-client.go, nntp-client-commands.go, nntp-transfer-demuxer.go, nntp-backend-pool.go, proxy.go}`, `cmd/nntp-transfer/main.go` (pprof) | CLI-1…5, OPS-3 |
| `rfc-commands` (rfc-commands) | new `internal/nntp/nntp-cmd-{date,nextlast,newgroups,newnews,over-hdr,stream,starttls}.go`; dispatch and CAPABILITIES inline | RFC-1 |

- `queries.go` hunks in different slices touch different functions and are merged in slice order, with checks after each merge.
- `internal/web/*` and the non-NNTP `internal/database/*` files are only touched in `audit-delta-fixes.md`.
