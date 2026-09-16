# Plan: web + SQLite hardening leftovers (paths, shutdown, sessions at rest, error detail, audit tool)

- **Slug:** `web-sqlite-leftovers`
- **Integration branch:** `plan-web-sqlite-leftovers` (from `testing-001`; it must contain merge 03d0178 of `web-sqlite-hardening`)
- **Run with:** `/run-plan .claude/plans/queued/web-sqlite-leftovers.md`
- **Written:** 2026-09-15 in the session that ran `web-sqlite-hardening` (WSH, `.claude/plans/done/web-sqlite-hardening.md`), at `testing-001` @ d11201a. All file:line references are at d11201a. Amended the same day with the code review of 03d0178 (findings F1–F22).
- **Parallelism:** wave 1 has 5 implementer slices; wave 2 has 5 (`lo2-pool` plus the review-fix slices `lo2-db-writer`, `lo2-web-pages`, `lo2-web-forms`, `lo2-web-server`) plus inline docs. Within a wave, no two slices own the same file.
- **Order with `nntp-audit-tests` (NAT):** run this plan first. NAT then gets a full review in a new session (its `audit-web`/`audit-db` slices read the code this plan changes). See "Relation to nntp-audit-tests" below.

---

## Context

WSH merged on 2026-09-15 (03d0178). Its `## Outcome` lists leftovers. This plan re-traced every one of them in the code at d11201a, cross-checked the WSH findings table for items no slice owned, and added the defects found while tracing. "Latent" means the defect is real but no current code path reaches it.

### Findings

**A. Data paths and group DB layout**
| ID | Where (at d11201a) | Defect |
|----|----|----|
| A1 | `cmd/nntp-fetcher/main.go:138` | `NewProgressDB("data/progress.db")` ignores `-data` (the tool sets `dbConfig.DataDir = *dataDir` at `:127`), so the progress DB always lands in `./data/progress.db/` relative to the cwd. (`NewProgressDB` takes a directory and appends `progress.db`.) |
| A2 | `internal/nntp/nntp-backend-pool.go:561-562`; only caller `cmd/nntp-fetcher/main.go:286` | `(*Pool).FileCachedListNewsgroups` reads and writes `data/cache/<host>.list` relative to the cwd. |
| A3 | `internal/processor/analyze.go:484-490` (callers `:152`, `:178`, `:391`, `:505`, `:582`) | `getCacheFilePath` builds `data/cache/<provider>/<sha256>.overview` relative to the cwd, although `proc.DB.GetDataDir()` is available. |
| A4 | `cmd/web/main_functions.go:345` (in `rsyncInactiveGroupsToDir`, called at `cmd/web/main.go:284`) | `-rsync-inactive-groups` looks for the source group DBs in `data/db/<md5>` relative to the cwd, not in `-data`. |
| A5 | `internal/database/queries.go:2851-2922`; caller `cmd/rslight-importer/main.go:134` | `ResetAllNewsgroupData` lists `<data>/groups` and treats sub-directory names as newsgroup names. Group DBs live in `<data>/db/<MD5Hash(name)>/<SanitizeGroupName(name)>.db` (`db_groupdbs.go:214-223`), so the directory never exists: the tool logs "No groups directory found" and resets only the main DB counters. Note: `ResetNewsgroupData` (`:2924`) calls `GetGroupDB`, which **creates** a DB for any name, so a fix must check the file exists first. |
| A6 | `internal/database/utils.go:112-126`, `db_groupdbs.go:214-223`, `groups_hashmap.go:132` | WSH out-of-scope item "SanitizeGroupName `.`→`_` collisions". `a.b` and `a_b` sanitize to the same file name, but the directory is `MD5Hash(real name)`, so the files never collide. No production change: add a regression test and close the item. |

**B. Web**
| ID | Where | Defect |
|----|----|----|
| B1 | `cmd/web/main.go:514-519`, `internal/web/webserver.go:108-117` | Shutdown waits 10s, then continues while handlers may still run (AI chat waits up to 90s for its proxy, slow admin POSTs) and keep using NNTP, processor and DB as those shut down. Request contexts are never cancelled, so outbound calls keep going. |
| B2 | `internal/web/web_aichatPage.go:271` (copy), `:342-344` (store), clear handler `:414` | A send copies the history, waits for the proxy, then replaces the stored entry: two sends to the same model lose one exchange, and a clear during a send is undone. The proxy URL is a `const` (`:21`), so tests cannot point it at a fake. |
| B3 | `internal/web/web_sitePostPage.go:323` vs `:423-439` | `TryReserveWebPost` (post_count+1, lastpost_unix=now) runs before the queue `select`. On "Server is busy" the user is charged a post and blocked for the 42s back-off. `user.LastPostUnix` from `GetUserByID` (`:178`) is available to undo it. |
| B4 | `internal/web/web_apiHandlers.go:62-67`, `:321-403` | `getStats` cache: when it expires, every concurrent request loads all groups and recomputes (thundering herd). |
| B5 | `internal/web/web_sitePostPage.go:394-400` | `defer groupDB.Return()` inside the reply loop keeps every opened group DB acquired until the handler returns. |
| B6 | `internal/web/web_apitokens.go:38-44`, `internal/database/db_apitokens.go:107-117` | Every authenticated API request starts a goroutine that runs one `UPDATE api_tokens` (unbounded goroutines, one main-DB write per request; the WSH P3 remainder). |
| B7 | pages: `web_groupsPage.go:39`, `web_hierarchiesPage.go:40`, `:81`, `:210`, `web_sectionsPage.go:39`, `:95`, `:219`, `web_searchPage.go:55`, `:94`, `web_groupThreadsPage.go:33`, `:42`, `web_threadTreePage.go:133`, `:209`, `web_threadPage.go:83`, `web_registerPage.go:27`, `:53`, `web_templates.go:126`; API: `web_apiHandlers.go:114`, `:191`, `:310` | Internal error text (SQLite errors, file paths, template errors) is shown to visitors, and the three `error.html` pages answer 200. Out of this item: validation messages meant for users (`web_registerPage.go:80`, `:86`, `web_profile.go:146`, `:180`, `web_sitePostPage.go:199`), admin flash messages (28 sites in `web_admin_*.go`, admin audience), and `web_apitokens.go:63` (inside commented-out demo code). |
| B8 | `internal/web/web_templates.go:40-49` | The template cache key only records *whether* a FuncMap is used. Two call sites with different FuncMaps and the same name and files would share one entry (only the admin page uses a FuncMap today). |
| B9 | `internal/web/embedded_static.go:73-127`, `webserver_core_routes.go:307-313` | Latent. `getContentType` compares the last 4 bytes, so `.js` never matches and `.json`/`.map`/`.webp` are unknown. Only `EmbeddedFileHandler` uses it, and its single route asks for `web/favicon.ico`, which is not in the embedded `static/*` FS, so it always falls back to the disk file. `/static/*` itself is served by `http.FileServer` with correct types. |
| B10 | `internal/web/web_auth.go:136-197` (`WebAuthRequired`, `WebAdminRequired`), `internal/database/tree_view_api.go:101-166` (`HandleThreadTreeAPI`), `internal/database/db_rescan.go:822-848` (`initializeThreadCacheSimple`) | Dead code, never registered or called. `HandleThreadTreeAPI` opens `GetGroupDB` for any name; `WebAdminRequired` shows `err.Error()` and ignores the user-ID-1 admin rule. |
| B11 | `internal/web/webserver_core_routes.go:351-352`; used by `internal/web/static/js/thread-tree.js:495`; documented in `web/templates/help.html:212`, `:315` | WSH out-of-scope item: the article preview under `/api/v1` is served even when `APIEnabled` is false. Putting the existing route behind the setting as-is would break the tree view's hover previews. |
| B12 | `internal/web/README.md` (e.g. `:45`, `:76-84`) | Lists files and functions that no longer exist (`server_core.go`, `createWebSession`, `isAdminUser`, ...). |
| B13 | `docs/` | WSH behaviour changes (trusted proxies and `Host` passthrough, CSRF, foreign keys, group DB migrations, template cache, lockout) exist only in the plan file. |

**C. Database**
| ID | Where | Defect |
|----|----|----|
| C1 | `internal/database/db_sessions.go:35-60`, `:63-100`, `:115-122`; `migrations/0001_main_schema.sql:239`, `:245` | WSH out-of-scope item: `users.session_id` stores the raw session token, so a DB read (backup, copied file, SQL access) yields working sessions. Only `db_sessions.go` reads or writes the column (grep: `internal/web` only uses the cookie value). |
| C2 | `internal/database/db_sessions.go:126-236` vs `:104-122`, `:239-255` | The login lockout window uses `users.updated_at` as "time of the last attempt", but `InvalidateUserSession*` (logout) and `CleanupExpiredSessions` (every 15 minutes) also set `updated_at`, so they restart a running lockout window. |
| C3 | `internal/database/sqlite_retry.go:21-23`, `:58` | `SQLiteMaxRetryWait` is a plain `var` read on every retry. A tool that changes it after DB activity has started races with the batch writer. |
| C4 | `internal/database/thread_cache.go:288-293` | The child page query `article_num IN (<=25>) AND hide = 0 ORDER BY date_sent` walks `idx_articles_hide_date (hide=?)` over every visible article when `sqlite_stat1` is absent (verified by the WSH w2-dbperf review; with ANALYZE it uses rowid lookups). |
| C5 | `internal/database/db_rescan.go:816-820`, `:865`, `:459` | Correction of the WSH Outcome: `query_initializeThreadCacheSimple1` (`INSERT OR REPLACE INTO thread_cache`) is **live**. `batchInitializeThreadCache` uses it in `RebuildThreadsFromScratch` (`cmd/recover-db/main.go:151`, `:285`, `:1376`). REPLACE deletes the row, which with `foreign_keys=ON` cascades to `tree_stats`. The rebuild clears `tree_stats` first (`:459`), so nothing is lost today, and for derived cache data the cascade is the correct invalidation. Only the dead wrapper goes (B10); the constant gets a comment. |
| C6 | `internal/database/db_init.go:98-99`, `:306-307` | WSH out-of-scope item: the main DB pool (`MaxOpenConns 100`, `MaxIdleConns 25`) was never measured for SQLite. Measure first. |

**D. Data audit**
| ID | Where | Defect |
|----|----|----|
| D1 | production data; `internal/web/web_sitePostPage.go:356-375` (web posts store their header lines joined by `\n` in `HeadersJSON`) | Web posts accepted before 03d0178 can carry injected header lines (CR/LF in display name, subject or reply message-id: WSH H10). WSH blocks new ones only. Nothing finds old ones, and some may already have gone to peers (`post_queue.posted_to_remote = 1`). |

**E. Project checks**
| ID | Where | Defect |
|----|----|----|
| E1 | `.claude/CLAUDE.md:21-22` | The "green" test line lacks `./internal/database/... ./internal/web/...`, which have tests since WSH. |

**F. Code review of 03d0178** (a `/code-review` of `0f27e76..03d0178`; V = verdict, C confirmed, P plausible: the code path is traced, but the trigger needs timing or a future migration)
| ID | V | Where | Defect |
|----|----|----|----|
| F1 | C | `internal/web/webgroupPage.go:40`, `web_sectionsPage.go:188`, `models/models.go:412-429`, `web/templates/pagination.html:28,47,52` | `clampOffsetPage` caps both group pages at page 101 (`maxOffsetArticles` 12800 / 128 + 1), but `TotalPages` uses the real count and the template only links `?page=N`. From page 101, Next and Last reload page 101, and older articles cannot be reached. Cursor mode (`?cursor=`) exists, but no template links to it, and in cursor mode Next would link `?page=0`. |
| F2 | C | `internal/database/db_sessions.go:90`, `internal/web/web_auth.go:226`, `:358-390` | The DB expiry slides only when less than `SessionTimeout/2` remains, but every request refreshes the cookie to a full hour. Sessions die after 30-60 idle minutes while the browser still holds a valid-looking cookie, and a reply typed in that window is lost at submit. |
| F3 | C | `internal/web/webserver_core_routes.go:564` | `RemoteIPHeaders = {"X-Forwarded-For", "X-Real-IP"}`: behind a trusted proxy that sets only X-Real-IP and passes the client's own X-Forwarded-For through, that header becomes `ClientIP()`. The spoofed IP bypasses the BadIPs block and is stored as `last_login_ip`. No fixed header order is safe for every proxy setup. |
| F4 | C | `internal/web/webserver_core_routes.go:209`, `:556` | `DefaultReverseProxy` lacks `127.0.0.0/8` (except .1) and `fc00::/7`, which the removed `isPrivateOrLoopbackIP` trusted. A proxy that is not listed (for example docker 172.17.0.1 with `ReverseProxyAddr=127.0.0.1`) makes every visitor share the proxy IP, and cookies lose `Secure`. Nothing is logged. |
| F5 | C | `internal/web/web_profile.go:145`, `web/templates/profile.html:56-60` | `validateDisplayName` runs on every profile POST, and the form re-sends the stored name. A stored `Jane <jane@example.org>` (a format the template still suggests) blocks email and password changes. |
| F6 | C | `internal/web/web_auth.go:288-300` (64 runes), `internal/database/queries.go:742` (64 bytes), `web_profile.go:167-216` | A name within 64 characters but over 64 bytes passes validation, but only after the password and email writes does `UpdateUserDisplayName` reject it: a partial update, and the name can never be saved. |
| F7 | C | `internal/web/web_sitePostPage.go:464` | `^<[^<>\s@]+@[^<>\s@]+>$` rejects replies to stored legacy ids (`<bnews.x.1>`, `<a@b@c>`; `article.html:78` and `thread.html:164` post the stored id). RE2 `\s` does not cover `\x0b`, `\x7f` or other control bytes, and those pass into `References`. |
| F8 | C | `internal/web/web_sitePostPage.go:279` | "No valid newsgroups specified" is added whenever an earlier error skipped parsing, for example next to "Invalid reply message-id". |
| F9 | C | `internal/web/web_apiHandlers.go:315` | `X-Next-Offset` is `offset+limit` without the `apiThreadsMaxOffset` bound. A client following it past 1,000,000 gets the same page and header forever. |
| F10 | C | `internal/web/web_sectionsPage.go:134-160`, `internal/database/queries.go:446-466` | `sectionGroupAllowed` checks only `section_groups` membership, and `DeleteNewsgroup` leaves its section_groups rows. Section routes, now including the tree route, recreate group DBs for deleted groups and show inactive groups to anonymous users. |
| F11 | C | `internal/web/web_aichatPage.go:236` | `ShouldBindJSON` decodes an unbounded body before the 1024-byte message check. No handler uses `MaxBytesReader`, so a logged-in user can exhaust memory. Pre-existing. |
| F12 | C | `internal/web/web_admin_ollama.go:205` | `http.Get(ProxyURL)` with no timeout and no request context: a hung proxy blocks the handler forever, and `Shutdown` waits for it. Pre-existing. |
| F13 | C | `internal/web/cronjobs.go:82-86` | One `GetAllCronJobs` error returns from the only loop that starts jobs, so no job starts again until restart. Pre-existing. |
| F14 | C | `internal/config/config.go:595` | `UpdateBadBots("")` keeps the old patterns until restart, while a start with an empty setting has none (`Default_BadBots` is nil). Pre-existing. |
| F15 | C | `internal/web/web_sectionsPage.go:80-111`, `internal/database/thread_cache.go:270-280` (via `web_threadPage.go:40-43`) | An unbounded `page` makes `(page-1)*size` overflow negative, so slicing panics and Recovery answers 500 (for example `?page=100000000000000000`). Pre-existing. |
| F16 | P | `internal/database/db_groupdbs.go:120`, `:177-182`, `db_batch.go:604-607` | `GetGroupDB` gives up after 60 s while another goroutine initializes the group (WAL hook, migration 0009 index drops on a large DB, busy_timeout). `processNewsgroupBatch` has already drained the articles from `BATCHchan` and returns, so they are lost. |
| F17 | P | `internal/database/sqlite_retry.go:58`, `db_batch.go:747-755` | The retry cap counts from before the first attempt, so work longer than 5 min that hits BUSY at the end is not retried at all. The batch stats transaction then logs and returns, and `message_count`/`last_article` miss the increment. |
| F18 | P | `internal/database/db_spam_flags.go:54-60` | The counter UPDATE's `RowsAffected` is ignored. An article removed between the existence check and the UPDATE leaves orphan flag and spam rows and returns success. |
| F19 | P | `internal/database/db_migrate.go:320` | `[^;]*` spans newlines. A future PRAGMA line without `;` would strip the following statement, and the migration would still be recorded as applied. Latent. |
| F20 | P | `internal/database/database.go:23-34`, `db_batch.go:1318` | `CronDB` returns when `StopChan` closes, but the batch drain keeps opening group DBs afterwards. No idle or forced close happens during the final flush, so a large backlog can exceed `MaxOpenDatabases` and the fd limit. CronDB is outside `db.WG` (`db_init.go:189`). |
| F21 | C | `internal/processor/rslight.go:285-302` | `INSERT OR IGNORE` plus `LastInsertId` returns a stale id when the section exists. With foreign keys enforced, the following `section_groups` inserts fail, or attach groups to the wrong section. |
| F22 | C | `internal/database/db_nntp_users.go:281-311` | NNTP auth ignores `users.disabled`, and cache hits (`GetNNTPUserByID`) skip `is_active`, so a disabled web user's linked NNTP account keeps reading and posting. Pre-existing; WSH made web access respect `disabled`, which left the two sides inconsistent. |

### Dropped (no action)
- **cmd/web FetchRoutine is not in `db.WG`** (WSH leftover): `FetchRoutine` is never started. Its only call is its own restart at `cmd/web/main_functions.go:716`.
- **The one-time cost of group migration 0009:** release note only (B13).
- **The REPLACE cascade in rebuilds:** see C5 (comment only).
- **`max_depth` clamp (review H4):** 0 means unlimited by design (`web_threadTreePage.go:66`, `static/js/thread-tree.js:9`), and values above 50 are capped. Build cost depends on thread size, not on the depth option.
- **FK failure when caching thread trees (review X1)** and **"schema is locked" no longer retried (review D5):** both refuted during the review.
- **Admin flag memoized false after a transient `GetUserByID` error (review X4):** fails closed, and it did so before WSH too.
- **"A test leaves a cron goroutine running" (review sweep):** `TestW1ServerStartShutdown` passes `noCronjobs=true` (`w1_server_test.go:288`). `w0Srv`'s process-lifetime cron loop is intended, since migration 0021 disables the default jobs.

### Decisions (proposed; wave 0 confirms them with the user before wave 1)
1. **C1:** store `sha256(token)` as lowercase hex in `users.session_id`; the cookie keeps the raw token. Migration 0028 clears existing `session_id` and `session_expires_at`, so every user logs in once after the upgrade (sessions slide for 1h anyway).
2. **B11:** add the web route `GET /groups/:group/articles/:articleNum/preview` (same handler and group access checks, independent of the API setting) and switch `thread-tree.js` to it. Put `/api/v1/groups/:group/articles/:articleNum/preview` behind `requireAPIEnabled()` (still without a token), and update `help.html`.
3. **B1:** `(*WebServer).Shutdown(ctx)` runs `http.Server.Shutdown(ctx)`. When that returns an error (deadline), it cancels every request context (via `http.Server.BaseContext`) and waits up to 5s more for in-flight handlers, then logs how many were still running. cmd/web keeps its 10s context.
4. **B2:** one in-flight send per user and model. A second send gets 429 `{"error": "A reply is still being generated"}`. A clear bumps a generation counter, so a reply that arrives later is returned to the client but not stored.
5. **B6:** API token usage is counted in memory and flushed every 30s and on Shutdown. A crash loses at most 30s of usage counts.
6. **B7:** visitors see "Please try again later." (JSON: `{"error": "Internal server error"}`), and the detail goes to the log with the component prefix. The three `error.html` pages become `renderError(..., 500, ...)`.
7. **C2:** new column `users.login_attempt_at`. The lockout functions read and write only that column.
8. **C6:** measurement and report only. Pool defaults change only after the user has read the report.
9. **D1:** new read-only tool `cmd/audit-web-posts` with `build_audit-web-posts.sh`. It reports and exits 1 when it finds something; remediation (delete, cancel) is the user's decision.
10. **E1:** add `./internal/database/... ./internal/web/...` to the CLAUDE.md test line. NAT wave 3 adds `./cmd/nntp-server/...` later.
11. **F1: DEFERRED** (user decision, wave 0 of the run on 2026-09-16). Deep group pages keep today's behaviour: from page 101 the Next/Last links reload page 101 and older articles stay unreachable by number. No cursor links, no `PaginationInfo` fields, no `GetOverviewsPrevCursor`, no `pagination.html` change. Carried into the Outcome as a known limitation; `lo2-web-pages` keeps only F6/F9/F10/F15.
12. **F2:** the DB expiry slides at most once every 5 minutes, so the idle timeout is at least 55 minutes, and the cookie Max-Age follows the DB expiry.
13. **F3/F4:** the client IP comes from one configured header: the new setting `ReverseProxyIPHeader` (`X-Forwarded-For` by default, or `X-Real-IP`), set in admin settings and applied after a restart. `DefaultReverseProxy` adds `127.0.0.0/8` and `fc00::/7`. Forwarded headers from an untrusted private or loopback peer are logged once per IP.
14. **F5/F6:** the display name is validated only when it changes, the limit is 64 characters (runes) in web and DB, and the profile page stops suggesting `Name <address>`.
15. **F7:** a reply message-id is printable ASCII without `<`, `>` or space inside the brackets (the `@` rule is dropped, for legacy ids), at most 250 bytes.
16. **F10:** section routes require the group to exist in `newsgroups`, and to be active for non-admins, exactly like `/groups/:group`. `DeleteNewsgroup` also removes the group's `section_groups` rows.
17. **F16/F17/F22:** the batch writer waits for a group DB that is still initializing and drops articles, with a loud log, only after repeated init failures or once group DBs are shut down; its stats update retries busy errors until they clear; the retry cap counts busy time only; NNTP authentication is refused when the NNTP user is inactive or the linked web user is disabled, cache hits included.

### Out of scope (list in Outcome)
- Creating shell cron jobs through `-edit-cronjobs` (flag-gated by design; WSH S1 remainder).
- Scheduling `PRAGMA optimize` or `ANALYZE` (measure first; C4 fixes the known query without it).
- Remediating rows that D1 finds.
- Regenerating `FuncStructList.txt` (the user runs `updateFuncStructList.sh`).
- NNTP server protocol and security work (NAT).
- Adding the new build script to `build_ALL.sh` (release script, off-limits).
- **Admin handlers that can run past `WriteTimeout` 120 s (review S3, plausible):** for example `adminUpdateHierarchies` → `UpdateHierarchyCounts`. Measure on a scratch copy first.
- **LISTGROUP creating group DBs for any name (review H7):** tracked as SEC-2 in NAT.
- **Password-change invalidation of the NNTP auth cache:** the rest of NAT SEC-6; F22 only covers the disabled/inactive check.

### Relation to nntp-audit-tests (NAT, queued; full review in a new session after this plan)
- **Files:** no overlap with NAT's wave-0 hooks (`internal/nntp/nntp-server.go`, `nntp-server-cliconns.go`, `nntp-cmd-helpers.go`, `nntp-article-common.go`, `nntp-cmd-group.go`) or with its test files, scripts, `docs/audit/*`, `BUGS.md`, root `README.md` or CI. Shared: `internal/nntp/nntp-backend-pool.go` (A2 changes one method signature; NAT's `audit-nntp-client` reads it and its fix plans may own it later) and `.claude/CLAUDE.md` (E1 here, the Checks list in NAT wave 3).
- **Contracts:** NAT K1 pins "all `internal/database` exported functions". This plan removes the dead `database.HandleThreadTreeAPI`, replaces the var `SQLiteMaxRetryWait` with `SetSQLiteMaxRetryWait`/`GetSQLiteMaxRetryWait`, and adds `HashSessionToken`, `AddTokenUsage`, `ReleaseWebPost`. None of these is used by NAT's harness.
- **Review note for NAT:** its seed findings and `audit-web`/`audit-db` scope must be re-anchored on the tree after this plan (`.claude/plans/done/web-sqlite-leftovers.md`).
- **F22** changes `AuthenticateNNTPUser` so that cache hits re-check `is_active`, which is part of NAT SEC-6. NAT's review must re-anchor SEC-6 after this plan; its password-change half stays open.
- **New exported DB functions from the review fixes:** `UpsertSectionID` (`GetOverviewsPrevCursor` dropped with F1). Not used by NAT's harness.

---

## Design

### Contracts all slices rely on (unchanged in wave 1)
- **K1: exported and shared signatures.**
  - `web` (WSH K1): `NewWebServer(db, webconfig, nntp, cronEdit, noCronjobs) *WebServer`, `WebServer.Router`, `WebServer.DB`, `getWebSession`, `checkGroupAccess`, `checkGroupAccessAPI`, `requireAdminAuth`, `requireAdminAuthJSON`, `getBaseTemplateData`, `renderError`, `renderTemplate`, `isAdmin`. Also since WSH: `renderPage`, `renderTemplateSet`, `loadTemplates`, `writeTemplateSet`, `(*WebServer).Shutdown(ctx) error`, `rootHandler`, `newHTTPServer`, `getArticlePreview`, `requireAPIEnabled`.
  - `database`: `CreateUserSession(userID, remoteIP) (token string, err error)` returns the **raw** token; `ValidateUserSession(token)` and `InvalidateUserSessionBySessionID(token)` take the raw token; `InvalidateUserSession`, `CleanupExpiredSessions`, `ReserveLoginAttemptByID`, `IsUserLockedOutByID`, `IncrementLoginAttemptsByID`, `TryReserveWebPost`, `UpdateTokenUsage`, `ValidateAPIToken`, all `Retryable*`, `GetGroupDB`/`Return`, `MD5Hash`, `SanitizeGroupName`, `GetDataDir`, `GetUserByID`, and the identifiers the NAT harness uses (`Shutdown`, `StopChan`, `WG`, `Batch`, `ENABLE_ARTICLE_CACHE`, `NO_CACHE_BOOT`, `BatchInterval`, `SetProcessor`, `InsertNewsgroup`, `MainDBGetNewsgroup`, `GetArticleByNum`, `GetArticleByMessageID`, `InsertNNTPUser`, `DeactivateNNTPUser`, `UpdateNNTPUserPassword`).
  - Planned changes (owned, single callers): `(*nntp.Pool).FileCachedListNewsgroups(cacheDir string)` (lo-paths); the var `SQLiteMaxRetryWait` becomes `SetSQLiteMaxRetryWait(time.Duration)` + `GetSQLiteMaxRetryWait() time.Duration` (lo-db); `ProxyURL` becomes a var (lo2-web-server).
  - Unchanged by the wave-2 review slices: `web` `configureTrustedProxies(router, addrs)`, `setSessionCookie(c, id)`, `validatePostHeaders`, `sectionGroupAllowed`; `database` `DeleteNewsgroup(name) error`, `UpdateUserDisplayName`, `AuthenticateNNTPUser`, `CronDB()`.
- **K2: new names reserved per slice** (check with `grep -rnw '<name>' internal cmd` that they're free at the base commit):
  - `lo-web-core`: `inFlightRequests`, `(*WebServer).trackInFlight`, `baseCtx`, `cancelBase`, `shutdownGrace`, `(*WebServer).serveOn`, `tokenUsageBuffer`, `tokenUsageFlushEvery`, `(*WebServer).recordTokenUsage`, `(*WebServer).runTokenUsageFlusher`, `(*WebServer).flushTokenUsage`, `tokenUsageDone`, `staticContentType`, `statsComputeCount`; DB `(*Database).AddTokenUsage`.
  - `lo-web-handlers`: the fields `chatEntry.inFlight` and `chatEntry.gen`, `ollamaProxyURL` (const becomes var), `chatBusyMessage`, `(*WebServer).lookupReplyReferences`, `publicErrorDetail`, `funcMapKey`; DB `(*Database).ReleaseWebPost`.
  - `lo-db`: `HashSessionToken`, `SetSQLiteMaxRetryWait`, `GetSQLiteMaxRetryWait`, `sqliteMaxRetryWait`, migration `0028_main_session_hash_login_attempt_at.sql`, column `users.login_attempt_at`.
  - `lo-paths`: `groupDBFileExists` (unexported, queries.go), `analyzeCachePath` (unexported, processor).
  - `lo-audit-tool`: package `main` in `cmd/audit-web-posts`; `build_audit-web-posts.sh`.
  - `lo2-pool`: `BenchmarkLo2Pool*`, `lo2Pool*`.
  - `lo2-db-writer`: `errGroupDBInitTimeout`, `batchGroupDBRetry`, `batchGroupDBAttempts`, `updateNewsgroupStatsWithRetry`, `batchStatsRetryEvery`, `(*Database).cronDBEvery`, `(*Database).UpsertSectionID`, `query_nntpUserAuthState`.
  - `lo2-web-pages`: `nextThreadsOffsetHeader`, `query_DeleteSectionGroupsByNewsgroup`. (F1 deferred: no `buildGroupPagination`, `GetOverviewsPrevCursor`, `query_GetOverviewsPrevCursor` or new `models.PaginationInfo` fields.)
  - `lo2-web-forms`: `sessionSlideEvery`, `(*WebServer).setSessionCookieMaxAge`, `sessionCookieMaxAge`.
  - `lo2-web-server`: `CFG_KEY_REVERSEPROXY_IPHEADER`, `FORM_FIELD_REVERSEPROXY_IPHEADER`, `configureTrustedProxiesWithHeader`, `untrustedForwarderSeen`, `(*WebServer).noteUntrustedForwarder`, `maxChatRequestBytes`, `ollamaSyncHTTPClient`, `(*CronJobManager).loadAndStartJobs`, and the fields `CronJobManager.reloadEvery`, `CronJobManager.loadJobs`.
- **K3: tests.**
  - New test files are `lo1_<slice>_test.go` (`lo1_core_test.go`, `lo1_handlers_test.go`, `lo1_db_test.go`, `lo1_paths_test.go`) and `cmd/audit-web-posts/main_test.go`. Package-level identifiers are prefixed `lo1Core…`, `lo1Hand…`, `lo1DB…`, `lo1Paths…`, `lo1Audit…`, `lo2Pool…`.
  - The wave-2 review slices add `lo2_writer_test.go` (database), `lo2_pages_test.go` (web and database), `lo2_forms_test.go` (web and database) and `lo2_server_test.go` (web), with the prefixes `lo2Writer…`, `lo2Pages…`, `lo2Forms…`, `lo2Server…`.
  - Use the WSH wave-0 helpers and never edit them: `internal/database/testmain_test.go` (`w0DB`, `w0Name`) and `internal/web/testmain_test.go` (`w0Srv`, `w0DB`, `w0Do`, `w0NewUser`, `w0CreateUser`, `w0NewGroup`, `w0Name`, `w0DataDir`, `w0RepoRoot`). Web tests run from a temp cwd that only links `web/`.
  - The DB and server are shared per test binary: never call `Shutdown` on `w0Srv`; never change a global that a background goroutine reads; restore changed globals with `t.Cleanup`; no `t.Parallel` in tests that change globals. Tests that would reset or mutate every group or user use an isolated `&Database{...}` (as `TestW1SQLiteShutdownDuringInit` does), never `w0DB`.
- **K4:** nobody changes `go.mod`/`go.sum` (no `x/sync`; write the small singleflight by hand), `appVersion.txt`, `FuncStructList.txt`, `BUGS.md`, root `README.md`, `build_ALL.sh`, the NAT-owned files listed above, `scripts/test-web-hardening.sh`, `scripts/test-web-leftovers.sh` (wave 0 owns it; report real bugs) or `internal/*/testmain_test.go`. `gofmt` runs only on owned files.
- **K5:** no slice calls a helper another slice of the same wave adds. This holds within wave 2 as well.

### Component designs
- **Hashed sessions (lo-db):** `HashSessionToken(raw) = hex(sha256(raw))`. `CreateUserSession` stores the hash and returns the raw token. `ValidateUserSession` rejects an empty token, then looks up `session_id = HashSessionToken(token)`. `models.User.SessionID` then holds the hash (only `db_sessions.go` scans it).
- **Lockout column (lo-db):** migration 0028 runs `ALTER TABLE users ADD COLUMN login_attempt_at DATETIME;`, `UPDATE users SET login_attempt_at = updated_at WHERE login_attempts > 0;` and `UPDATE users SET session_id = '', session_expires_at = NULL;` (no PRAGMA lines; migrations run in one transaction). `ReserveLoginAttemptByID` becomes `... login_attempt_at = CURRENT_TIMESTAMP WHERE id = ? AND (COALESCE(login_attempts,0) < ? OR datetime(login_attempt_at) IS NULL OR datetime(login_attempt_at) < datetime('now', ?))`. `IsUserLockedOutByID`, `IncrementLoginAttemptsByID` and the kept username variants move to the same column. Session writes stop being part of the lockout clock.
- **Shutdown (lo-web-core):** `NewWebServer` creates `baseCtx, cancelBase`. The `trackInFlight` middleware is registered first and counts requests in `inFlightRequests` (`atomic.Int64`). `Start` sets `srv.BaseContext` to return `baseCtx` and serves through `serveOn(ln)` (tests use a `127.0.0.1:0` listener). `Shutdown(ctx)` closes `stopCh` once, runs `srv.Shutdown(ctx)`, calls `cancelBase()` when that errors, polls `inFlightRequests` every 50ms for up to `shutdownGrace` (5s), then waits for the token-usage flusher (`tokenUsageDone`, bounded by the same grace), and always calls `cancelBase()` before returning.
- **Token usage (lo-web-core):** `recordTokenUsage(id, now)` adds to `tokenUsageBuffer` (mutex, `map[int64]int64` counts and `map[int64]time.Time` last-used). `runTokenUsageFlusher` (started in `NewWebServer`, one goroutine) calls `flushTokenUsage` every `tokenUsageFlushEvery` (30s) and once more when `stopCh` closes, then closes `tokenUsageDone`. `flushTokenUsage` swaps the maps under the lock, then writes each token with `AddTokenUsage(id, n, lastUsed)` = `UPDATE api_tokens SET usage_count = usage_count + ?, last_used_at = ? WHERE id = ?` (RetryableExec). On error it puts the counts back and logs `[API]`.
- **Stats singleflight (lo-web-core):** `apiStatsCache` gets `loading chan struct{}`. On a miss under the lock: if `loading` is set, unlock, wait for it, then serve the cached value (computing only if it is still missing); otherwise create the channel, unlock, compute, store under the lock, close and clear the channel. `statsComputeCount` (`atomic.Int64`) counts computations for the test.
- **Chat (lo-web-handlers):** under `chatCacheMux.Lock`, get or create the entry. If `inFlight`, unlock and return 429; else set `inFlight`, copy `msgs` and remember `gen`. A deferred block (every return path) takes the lock and clears `inFlight` on the current entry. On a reply, if `entry.gen == gen`, append the user message and reply to the **current** `entry.msgs` (not the copy), trim to `maxHistoryLength` and set `lastUsed`. Clear sets `msgs = nil` and does `gen++` (it no longer deletes an in-flight entry). `sweepChatCaches` skips `inFlight` entries.
- **Post reservation undo (lo-web-handlers):** `ReleaseWebPost(userID, reservedAt, prevLastPost)` = `UPDATE users SET post_count = MAX(post_count - 1, 0), lastpost_unix = ? WHERE id = ? AND lastpost_unix = ?` with args `(prevLastPost, userID, reservedAt)`, returning `RowsAffected()==1`. The queue-full branch calls it with `user.LastPostUnix` read at `:178`.
- **Audit tool (lo-audit-tool):** see the slice.

---

## Waves

### Wave 0 (inline, orchestrator on `plan-web-sqlite-leftovers`)
Owned files: `.claude/CLAUDE.md` (the test line only), `scripts/test-web-leftovers.sh` (new).

Steps:
1. Preflight from the skill. `git merge-base --is-ancestor 03d0178 HEAD` must succeed. Run the baseline `## Checks` and `PORT=18981 DATA=./data-test-web-sqlite-hardening scripts/test-web-hardening.sh` (expected `pass=25 fail=0`); record PASS/FAIL in the progress note.
2. Confirm decisions 1–17 with the user (AskUserQuestion; group related ones, at most 4 questions per call). Record the answers in the progress note and adjust the affected slice text before spawning.
3. E1: in `.claude/CLAUDE.md`, change the `go test -race` line to include `./internal/database/... ./internal/web/...`. Change nothing else there.
4. Write `scripts/test-web-leftovers.sh` from `## End-to-end`, then `chmod +x`, then `bash -n`. Build the binaries (`./build_webserver.sh && ./build_fetcher.sh`; the audit tool does not exist yet, so its check reports "missing binary" as FAIL). Run it on the baseline (`PORT=18990`) and record the PASS/FAIL list. Expected at baseline: L01, L02, L04, L05, L06, L08, L09, L10, L11, L12, L13, L14, L15, L16 FAIL. L03 and L07 may already PASS: the per-request goroutine also counts usage, so L03 is a regression guard for the buffering; `/favicon.ico` falls back to `http.ServeFile`, whose type comes from `/etc/mime.types` here (`image/vnd.microsoft.icon`).
   - Before the first L10 run, confirm by reading `cmd/nntp-fetcher/main.go` and its log that with every provider disabled the fetcher exits without any network connection (expected: `log.Fatalf("No provider backend available")` after `NewProcessor`). If it would connect anywhere, drop L10 from the script and move its assertion to a unit test in lo-paths.
5. Commit on `plan-web-sqlite-leftovers` with `test: wave-0 checks and e2e script for web-sqlite-leftovers`, including the plan move to `wip/`. Record the base SHA.

### Wave 1 (5 parallel `pugleaf-implementer` slices)

Common rules for every wave-1 slice:
- Follow K1–K5. Read the findings rows for your IDs and your owned files before editing; grep again, since line numbers can drift by a few lines.
- Checks, run in your worktree:
  ```bash
  gofmt -l <owned dirs>        # none of your files listed; baseline-only: internal/database/embedded_migrations.go, internal/web/web_admin_provider.go
  go vet ./...
  go build ./...
  go test -race -count=1 ./internal/database/... ./internal/web/... ./internal/history/... ./internal/nntp/... \
      ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/...
  ./build_webserver.sh && ./build_fetcher.sh && PORT=<your port> scripts/test-web-leftovers.sh | grep -E "\[(<your tag>)\]|SUMMARY"
  PORT=<your port+10> DATA=./data-test-web-sqlite-hardening scripts/test-web-hardening.sh | grep -E "^FAIL|SUMMARY"   # regression: fail=0
  ```
  E-checks tagged for other slices may FAIL in your worktree; that is expected.
- Report the L-check lines with your tag and the regression SUMMARY.

---

#### Slice `lo-web-core`: shutdown, API token usage, stats, API error detail, static types, preview route
Findings: B1, B4, B6, B7 (API part), B9, B11. Tag `lo-web-core`, ports 18992/19002.

Owned files:
- `internal/web/webserver.go`
- `internal/web/webserver_core_routes.go` (routes, middleware registration, goroutine starts in `NewWebServer`, favicon route)
- `cmd/web/main.go` (shutdown block only)
- `internal/web/embedded_static.go`
- `internal/web/web_apiHandlers.go`
- `internal/web/web_apitokens.go`
- `internal/database/db_apitokens.go`
- `internal/web/static/js/thread-tree.js` (preview URL only)
- `web/templates/help.html` (preview endpoint documentation only)
- `internal/web/lo1_core_test.go` (new)

Changes:
1. **B1** as in Design "Shutdown". Keep the cmd/web order (web Shutdown, NNTP stop, post queue worker, cron, `close(db.StopChan)`, `db.WG.Wait()`, `db.Shutdown()`). Log with `[WEB]`.
2. **B6** as in Design "Token usage". `APIAuthRequired` calls `recordTokenUsage` instead of starting a goroutine. Keep `UpdateTokenUsage` (K1). The final flush must happen before `Shutdown` returns, because cmd/web closes the DB afterwards.
3. **B4** as in Design "Stats singleflight".
4. **B7 (API):** the three `c.JSON(500, gin.H{"error": err.Error()})` at `web_apiHandlers.go:114`, `:191`, `:310` become `{"error": "Internal server error"}` plus an `[API]` log line with the error.
5. **B9:** replace `getContentType` with `staticContentType(p)`: `mime.TypeByExtension(path.Ext(p))` plus explicit fallbacks (`.ico` → `image/x-icon`, `.woff2` → `font/woff2`, `.woff` → `font/woff`, `.ttf` → `font/ttf`, `.map` → `application/json`, `.js` → `text/javascript; charset=utf-8`), otherwise `application/octet-stream`. Make `/favicon.ico` always serve `web/favicon.ico` from disk with `Content-Type: image/x-icon`, with the same 404 behaviour when the file is missing. Keep `EmbeddedFileHandler` for paths inside the embedded FS.
6. **B11:**
   - Add `s.Router.GET("/groups/:group/articles/:articleNum/preview", s.getArticlePreview)` next to the other `/groups/:group/articles/...` routes.
   - Change the API route to `s.Router.GET("/api/v1/groups/:group/articles/:articleNum/preview", s.requireAPIEnabled(), s.getArticlePreview)`.
   - In `thread-tree.js:495`, fetch `/groups/${encodeURIComponent(groupName)}/articles/${articleNum}/preview`.
   - In `help.html`, state that the API preview needs the API to be enabled.
7. Tests (`lo1_core_test.go`):
   - `ShutdownCancelsInFlight`: a separate server (never `w0Srv`) with a test route that blocks until `c.Request.Context().Done()`, served on a `127.0.0.1:0` listener via `serveOn`. Start a request, call `Shutdown` with a 200ms context. The handler sees cancellation, `Shutdown` returns within 2s, and `inFlightRequests` is 0.
   - `TokenUsageBuffered`: enable the API (restore in `t.Cleanup`), create a token, make 7 requests via `w0Do`. `usage_count` is unchanged until `flushTokenUsage()`, then +7 and `last_used_at` is set. A flusher on a separate server stops after `Shutdown` and has flushed.
   - `StatsSingleflight`: expire the cache, run 20 concurrent `/api/v1/stats` requests; `statsComputeCount` increases by exactly 1.
   - `StaticContentType`: a table covering all fallbacks and `.css`/`.png`/`.svg`.
   - `Favicon`: `GET /favicon.ico` returns 200 with `image/x-icon` (the temp cwd links `web/`).
   - `PreviewRoutes`: create a group and an article row in its group DB. With `APIEnabled=true` both routes return 200; with `APIEnabled=false` the API route returns 503 and the web route 200. An inactive group gives 404 for anonymous users on both.

Acceptance: L03, L04, L05, L06, L07, L09 PASS; the listed tests pass with -race; the WSH script stays at `fail=0`.

---

#### Slice `lo-web-handlers`: chat concurrency, post reservation undo, reply loop, page error detail, template key, dead middleware
Findings: B2, B3, B5, B7 (pages), B8, B10 (web part). Tag `lo-web-handlers`, ports 18993/19003.

Owned files:
- `internal/web/web_aichatPage.go`
- `internal/web/web_sitePostPage.go`
- `internal/database/db_web_posting.go`
- `internal/web/web_templates.go`
- `internal/web/web_auth.go` (delete `WebAuthRequired` and `WebAdminRequired` only)
- the B7 lines only: `internal/web/web_groupsPage.go`, `web_hierarchiesPage.go`, `web_sectionsPage.go`, `web_searchPage.go`, `web_groupThreadsPage.go`, `web_threadTreePage.go`, `web_threadPage.go`, `web_registerPage.go`
- `internal/web/lo1_handlers_test.go` (new)

Changes:
1. **B2** as in Design "Chat". `ollamaProxyURL` becomes an unexported `var` (tests set it and restore it in `t.Cleanup`; no background goroutine reads it). Messages and status codes of the other paths stay the same.
2. **B3** as in Design "Post reservation undo". Log `[WEB]` when the undo does not apply (`RowsAffected()==0`).
3. **B5:** move the reply-references lookup (`:392-410`) into `(*WebServer).lookupReplyReferences(newsgroups []string, messageID string) string`, which calls `groupDB.Return()` at the end of each iteration (no `defer` in the loop). Behaviour stays identical.
4. **B7 (pages):** at every site listed in B7 (pages), replace the error text shown to the visitor with `publicErrorDetail = "Please try again later."`, keep the page title, and log the error with `[WEB]` and the handler name. The three `renderPage(c, http.StatusOK, gin.H{"Error": err.Error()}, "error.html")` calls become `s.renderError(c, http.StatusInternalServerError, "Database Error", publicErrorDetail)`. At `web_templates.go:126`, log the template error and show `publicErrorDetail`. Validation messages and admin flash messages stay unchanged.
5. **B8:** `tmplCacheKey` writes `funcMapKey(funcs)` = `fmt.Sprintf("funcs@%x\x01", reflect.ValueOf(funcs).Pointer())` when `funcs != nil`. Update the comment.
6. **B10 (web):** grep `WebAuthRequired|WebAdminRequired` over `internal` and `cmd`. If only the definitions remain, delete both.
7. Tests (`lo1_handlers_test.go`):
   - `ChatConcurrentSend`: a fake proxy (`httptest.Server`) that blocks until the test releases it. Two concurrent sends for the same user and model: one 200, one 429. The stored history holds exactly one exchange (2 messages).
   - `ChatClearDuringSend`: clear while the proxy blocks, then release; the reply is returned, but the history is empty afterwards and `inFlight` is false.
   - `ChatSequential`: two sequential sends store 4 messages.
   - `PostQueueFullReleases`: fill `models.PostQueueChannel` to capacity (drain in `t.Cleanup`; drain it first too) and submit a valid post: the response says "Server is busy", and `post_count` and `lastpost_unix` equal the values from before. After draining, an immediate post is accepted without the back-off error.
   - `ReleaseWebPostGuard`: `ReleaseWebPost` with a wrong `reservedAt` changes nothing.
   - `TemplateKeyFuncMaps`: same name and files with two different FuncMaps give different `*template.Template` pointers; the same FuncMap gives the same pointer.
   - `TemplateErrorHidesDetail`: `renderPage` with a missing template file returns 500, and the body contains neither the file path nor "no such file".

Acceptance: L08 PASS; the listed tests pass with -race; the WSH script stays at `fail=0`.

---

#### Slice `lo-db`: hashed sessions, lockout column, retry wait, child query, dead DB code
Findings: C1, C2, C3, C4, C5, B10 (DB part). Tag `lo-db`, ports 18994/19004.

Owned files:
- `internal/database/db_sessions.go`
- `internal/database/migrations/0028_main_session_hash_login_attempt_at.sql` (new)
- `internal/database/sqlite_retry.go`
- `internal/database/thread_cache.go` (the child page query only)
- `internal/database/db_rescan.go` (delete `initializeThreadCacheSimple`; comment on `query_initializeThreadCacheSimple1`)
- `internal/database/tree_view_api.go` (delete `HandleThreadTreeAPI` and the imports it alone used)
- `internal/database/w1_auth_test.go` (only the lockout tests that move `updated_at`, which must move `login_attempt_at` instead)
- `internal/database/lo1_db_test.go` (new)

Changes:
1. **C1** as in Design "Hashed sessions". `InvalidateUserSessionBySessionID` hashes its argument. `CleanupExpiredSessions` and `InvalidateUserSession` keep their WHERE clauses.
2. **C2** as in Design "Lockout column", including the migration. Session writes (`CreateUserSession`, `InvalidateUserSession*`, `CleanupExpiredSessions`, `ValidateUserSession`) no longer influence the lockout window.
3. **C3:** replace the var with `sqliteMaxRetryWait atomic.Int64` (nanoseconds, initialised to 5 minutes), `SetSQLiteMaxRetryWait(d time.Duration)` (d <= 0 restores the default) and `GetSQLiteMaxRetryWait()`. The give-up check reads it once per decision. Grep `internal` and `cmd` for other users of the old var.
4. **C4:** change the child query to `WHERE article_num IN (%s) AND +hide = 0 ORDER BY date_sent ASC`. Results stay the same.
5. **C5:** add a comment above `query_initializeThreadCacheSimple1`: REPLACE deletes and re-inserts the row, and with `foreign_keys=ON` this cascades to `tree_stats`; callers rebuild `tree_stats` afterwards. Delete the dead `initializeThreadCacheSimple` (grep first).
6. **B10 (DB):** grep `HandleThreadTreeAPI` in `internal` and `cmd`. If only the definition remains, delete it.
7. Tests (`lo1_db_test.go`):
   - `SessionHashedAtRest`: after `CreateUserSession`, the column equals `HashSessionToken(raw)` and not `raw`. `ValidateUserSession(raw)` succeeds, `ValidateUserSession(hash)` fails, and `InvalidateUserSessionBySessionID(raw)` clears it.
   - `Migration0028`: `schema_migrations` has the file, `pragma_table_info('users')` has `login_attempt_at`, and on a fresh temp DB with the embedded migrations a pre-0028 row with a raw `session_id` is cleared.
   - `LockoutIgnoresSessionWrites`: reserve `MaxLoginAttempts` attempts (the next one is refused); run `InvalidateUserSession` and `CleanupExpiredSessions`; still refused. After moving `login_attempt_at` back past the window, the next attempt is allowed.
   - `RetryWaitRace`: goroutines call `SetSQLiteMaxRetryWait` while others run `RetryableExec` on a busy DB (-race clean). Restore the default in `t.Cleanup`.
   - `ChildQueryPlan`: on a group DB from `GetGroupDB(w0Name(...))`, `EXPLAIN QUERY PLAN` of the child query uses `INTEGER PRIMARY KEY` and does not mention `idx_articles_hide_date`. Seeded rows (hidden ones included) come back in the same order as with the old query.
   - Adjust the WSH lockout tests in `w1_auth_test.go` that move `updated_at` so they move `login_attempt_at`.

Acceptance: L01, L02 PASS; the listed tests pass with -race; the WSH script stays at `fail=0` (E01–E05, E15, E16 cover login and sessions).

---

#### Slice `lo-paths`: honour `-data` in fetcher, pool cache, analyze cache and rsync; fix the reset layout
Findings: A1, A2, A3, A4, A5, A6. Tag `lo-paths`, ports 18995/19005.

Owned files:
- `cmd/nntp-fetcher/main.go` (`:138` and the call at `:286` only)
- `internal/nntp/nntp-backend-pool.go` (`FileCachedListNewsgroups` only)
- `internal/processor/analyze.go` (`getCacheFilePath` only)
- `cmd/web/main_functions.go` (the source path in `rsyncInactiveGroupsToDir` only)
- `internal/database/queries.go` (`ResetAllNewsgroupData` only; every other function stays byte-identical)
- `internal/database/lo1_paths_test.go` (new), `internal/nntp/lo1_paths_test.go` (new), `internal/processor/lo1_paths_test.go` (new)

Changes:
1. **A1:** `database.NewProgressDB(filepath.Join(*dataDir, "progress.db"))`. The default stays `data/progress.db/progress.db`.
1b. **A1b** (found in wave 0, same file, same lines you already edit): with no *enabled* provider, `pools` stays empty and `pools[0].FileCachedListNewsgroups()` at `:286` panics with `index out of range [0] with length 0`. The `GetProviders` guard at `:176` does not catch it, because the default DB seeds 23 providers with `enabled=0`. Before `:286` (and before the `*resetProgress` block at `:273`, which also indexes `pools[0]`), add
   `if len(pools) == 0 { log.Fatalf("[FETCHER]: No enabled provider backend available (%d providers, none enabled)", len(providers)) }`.
   No network connection happens on this path either way — verified in wave 0 — so the e2e check L10 is safe to run.
2. **A2:** `FileCachedListNewsgroups(cacheDir string)` uses `filepath.Join(cacheDir, host+".list")`; `WriteNewsgroupListToFile` already creates the directory. The fetcher passes `filepath.Join(*dataDir, "cache")`.
3. **A3:** `getCacheFilePath` uses `analyzeCachePath(dataDir, provider, group)`, where `dataDir = proc.DB.GetDataDir()` (fall back to `"data"` only when `proc.DB` is nil). The file name stays unchanged.
4. **A4:** `rsyncInactiveGroupsToDir` builds the source dir as `filepath.Join(db.GetDataDir(), "db", groupsHash)`.
5. **A5:** `ResetAllNewsgroupData` keeps step 1 (main counters). Step 2 iterates `SELECT name FROM newsgroups` (RetryableQuery, `rows.Close`, `rows.Err`). For each name whose file `<data>/db/<MD5Hash(name)>/<SanitizeGroupName(name)>.db` exists (`groupDBFileExists`), it calls `ResetNewsgroupData(name)`, **never** calling `GetGroupDB` for a missing file. Logging and the error summary keep their meaning.
6. **A6:** no production change.
7. Tests:
   - `internal/nntp/lo1_paths_test.go`: write `<tmp>/cache/<host>.list` and call `FileCachedListNewsgroups(<tmp>/cache)` on a pool with that host. It returns the groups without dialling: the backend points at `127.0.0.1:1`, and any dial fails the test.
   - `internal/processor/lo1_paths_test.go`: `analyzeCachePath("/x/data", "prov", "a.b")` is under `/x/data/cache/prov/`.
   - `internal/database/lo1_paths_test.go`:
     - `ResetAllUsesLayout`: an isolated `&Database{...}` with a temp DataDir (as in `TestW1SQLiteShutdownDuringInit`), two groups with article rows in their group DBs and one group without a DB file. After `ResetAllNewsgroupData`, both group DBs are empty, main counters are reset, and no file was created for the third group.
     - `SanitizedNamesDoNotCollide`: `a.b` and `a_b` map to different directories and open independently via `GetGroupDB` (use `w0Name` prefixes).
   - L10 in the e2e script covers the fetcher.

Acceptance: L10 PASS; the listed tests pass with -race; the WSH script stays at `fail=0`.

---

#### Slice `lo-audit-tool`: read-only audit of web posts for injected header lines
Findings: D1. Tag `lo-audit-tool`, ports 18996/19006.

Owned files:
- `cmd/audit-web-posts/main.go` (new)
- `cmd/audit-web-posts/main_test.go` (new)
- `build_audit-web-posts.sh` (new; same form as `build_webserver.sh`, output `build/audit-web-posts`)

Changes:
1. **Flags:** `-data` (default `./data`, like the other tools), `-all` (also scan every article of the groups that have `post_queue` rows, not only the queued message-ids), `-v`.
2. **Read-only by construction:**
   - Open `<data>/cfg/pugleaf.sq3` and group DBs with the plain `sqlite3` driver and DSN `file:<path>?mode=ro`.
   - Never call `database.OpenDatabase` (it migrates and writes).
   - Never `CREATE`, `INSERT`, `UPDATE` or `DELETE`.
   - The group DB path is `<data>/db/<database.MD5Hash(name)>/<database.SanitizeGroupName(name)>.db`; a missing file is reported as "no group DB" and skipped.
3. **What it reads:**
   - `post_queue` (`id`, `newsgroup_id`, `message_id`, `created`, `posted_to_remote`) joined to `newsgroups.name`.
   - For each row, the article with that `message_id` in the group DB (`article_num`, `subject`, `from_header`, `"references"`, `message_id`, `headers_json`).
4. **It flags:**
   1. `\r` or NUL in any of these fields;
   2. `\n` in `subject`, `from_header`, `references` or `message_id`;
   3. a `message_id` that doesn't match `^<[^<>\s@]+@[^<>\s@]+>$`;
   4. in `headers_json` (header lines joined by `\n`; lines starting with a space or tab are continuations), any header line whose name is not one that `sitePostSubmit` writes (build the allow-list from `internal/web/web_sitePostPage.go:340-375` at the slice base and list it in a comment), or a second `From`/`Subject`/`Newsgroups`/`Message-ID` line.
5. **Output:** one TSV line per finding (`group`, `article_num`, `message_id`, `posted_to_remote`, `created`, `field`, `reason`), then a summary line (`checked=<n> flagged=<n> missing=<n>`). Exit 0 when clean, 1 when something is flagged, 2 on errors.
6. Factor `main` into `run(args []string, stdout, stderr io.Writer) int` for tests.
7. **Tests** (`main_test.go`), all under `t.TempDir()`:
   - Build a data dir: a main DB with `newsgroups` and `post_queue` created by the same column definitions as the migrations, and a group DB created by executing `internal/database/migrations/0001_single_db_schema.sql` read through `database.EmbeddedMigrationsFS`.
   - Seed one clean web post and one injected post (`from_header` `"Evil\r\nControl: cancel <x@y>"`, and a `headers_json` with an extra `Control:` line).
   - `run` returns 1 and lists only the injected message-id; with the injected rows removed it returns 0.
   - The sha256 of both DB files is unchanged after `run`.
   - A missing group DB counts as `missing`.

Acceptance: L11 PASS; `go test -race ./cmd/audit-web-posts/...` passes; `./build_audit-web-posts.sh` builds.

---

### Wave 2 (based on merged wave 1; 5 parallel slices)

The common rules and checks of wave 1 apply to every wave-2 slice, with its own tag and ports.

#### Slice `lo2-pool`: measure the main DB pool (no production change)
Findings: C6. No port.

Owned files:
- `internal/database/lo2_pool_bench_test.go` (new)
- `docs/perf/main-db-pool.md` (new)

Changes:
1. Write `BenchmarkLo2PoolMix` with sub-benchmarks for `MaxOpenConns` 4, 8, 16, 32 and 100 (`MaxIdleConns` = min(n, 25)). Each opens a temp DB with the package's main driver (`driverNameMain`; the pragma list `OpenDatabase` stored applies) and creates `users`, `site_news` and `post_queue` from the embedded main migrations. It then runs, for a fixed duration:
   - 32 reader goroutines doing the `GetUserByID` and visible-site-news SELECTs;
   - 2 writers doing the session-slide UPDATE and a `post_queue` INSERT.
   Report ops/s, p50/p99 read and write latency, and the number of busy retries (count through a test hook or by timing, without changing `sqlite_retry.go`).
2. Run it 3 times per size with `go test -run '^$' -bench Lo2PoolMix -benchtime 10s ./internal/database/`, recording `uptime` before each run.
3. Write `docs/perf/main-db-pool.md`: machine, SQLite version (`select sqlite_version()`), raw numbers, medians, and a recommendation (keep, or new defaults with reasoning). Do not change `db_init.go`.

Checks: `go vet ./...`, `go test -race -count=1 ./internal/database/...` (the benchmark must not run in normal tests), `gofmt -l internal/database`.

Acceptance: the report exists with numbers from 3 runs per size. The orchestrator shows the recommendation to the user; a defaults change becomes an inline commit only after the user's OK.

---

#### Slice `lo2-db-writer`: batch writer robustness, retry cap, spam flag, pragma strip, CronDB, rslight sections, NNTP auth
Findings: F16, F17, F18, F19, F20, F21, F22. Tag `lo2-db-writer`, ports 19011/19021.

Owned files:
- `internal/database/db_groupdbs.go` (the timeout error in `GetGroupDB` only)
- `internal/database/db_batch.go` (`processNewsgroupBatch`: the `retry1` error branch and the stats update only)
- `internal/database/sqlite_retry.go` (only where each helper takes the time it passes to `retryBackoff`)
- `internal/database/db_spam_flags.go`
- `internal/database/db_migrate.go` (`migrationPragmaLine` only)
- `internal/database/database.go` (`CronDB` only)
- `internal/database/db_sections_upsert.go` (new)
- `internal/processor/rslight.go` (`insertSection` only)
- `internal/database/db_nntp_users.go` (`AuthenticateNNTPUser` only)
- `internal/database/lo2_writer_test.go` (new)

Changes:
1. **F16:**
   - Add `var errGroupDBInitTimeout = errors.New("timeout waiting for group database initialization")`; the timeout return becomes `fmt.Errorf("group database %s: %w", groupName, errGroupDBInitTimeout)`.
   - At `retry1`, replace log-and-return with a loop driven by `batchGroupDBRetry(err error, attempt int) (retry bool, delay time.Duration)`:
     - `errGroupDBClosed` → no retry.
     - `errGroupDBInitTimeout` → retry every 1 s without a limit (the init is still running).
     - any other error → delay `min(2^attempt s, 30 s)`, for `batchGroupDBAttempts = 8` attempts.
   - Log on attempts 1, 10 and 100. When it gives up: `[BATCH] dropping %d articles for '%s' (first %s): %v`, then return.
2. **F17:**
   - `sqlite_retry.go`: every `Retryable*` helper starts its wait clock at the **first retryable error**, not before the first attempt, so the cap measures busy time. `retryBackoff` and C3's getter stay as they are.
   - `db_batch.go`: the stats update goes through `updateNewsgroupStatsWithRetry(exec func() error, every time.Duration) error`, which repeats `exec` while the error is retryable (`isRetryableSQLiteError`), sleeps `batchStatsRetryEvery = 5 * time.Second` between rounds, and logs rounds 1, 10 and 100 with the group, `+count` and the max article number. Non-retryable errors return at once, as today.
3. **F18:** check `RowsAffected()` of the counter UPDATE. On 0, delete the flag row (as on error) and return `ErrArticleNotFound`.
4. **F19:** `migrationPragmaLine` becomes `(?im)^[ \t]*PRAGMA[ \t]+[^;\n]*;[ \t\r]*(--[^\n]*)?$`, so a PRAGMA line without `;` is left in place and the migration fails instead of losing the next statement.
5. **F20:** `CronDB()` calls `db.cronDBEvery(10 * time.Second)`. That loop runs `cleanupIdleGroups` on each tick and returns once `db.groupDBsShutdown` is set (read under `MainMutex.RLock`), no longer on `StopChan`. CronDB is not in `db.WG` (`db_init.go:189`), so `WG.Wait()` cannot block on it.
6. **F21:** `(*Database).UpsertSectionID(s *models.Section) (int64, error)` runs the existing `INSERT OR IGNORE INTO sections ...`, then `SELECT id FROM sections WHERE name = ?` (both Retryable). `insertSection` returns `int(id)` from it and no longer reads `LastInsertId`.
7. **F22:** after a cache hit (`GetNNTPUserByID`) and after a successful bcrypt check on a miss, run `query_nntpUserAuthState`:
   ```sql
   SELECT n.is_active, COALESCE(u.disabled, 0) FROM nntp_users n LEFT JOIN users u ON u.id = n.web_user_id WHERE n.id = ?
   ```
   When the NNTP user is inactive or the web user is disabled, call `NNTPAuthCache.Remove(username)` and return `fmt.Errorf("user disabled")`, so only allowed logins stay cached.
8. Tests (`lo2_writer_test.go`):
   - `BatchGroupDBRetry`: a table over wrapped errors covering closed, timeout at attempt 50, init failure at attempts 0-8, and the delay cap.
   - `RetryCapCountsBusyTime`: `SetSQLiteMaxRetryWait(100ms)` (restored in `t.Cleanup`); a `RetryableTransactionExec` closure that sleeps 300 ms and returns `sqlite3.Error{Code: sqlite3.ErrBusy}` on its first call and nil on its second must succeed.
   - `StatsRetry`: `updateNewsgroupStatsWithRetry` with an exec that returns BUSY twice and then nil (3 calls, nil), and one that returns a non-retryable error (1 call, that error).
   - `SpamFlagZeroRows`: on a `w0Name` group DB with `CREATE TRIGGER lo2_ignore BEFORE UPDATE OF spam ON articles BEGIN SELECT RAISE(IGNORE); END`, flagging an existing article returns `ErrArticleNotFound` and leaves no `user_spam_flags` and no `spam` row.
   - `PragmaStrip`: a table with a whole line, a trailing comment, CRLF, and `PRAGMA foreign_keys = ON` followed by `\nCREATE TABLE t(x);` (kept byte-identical).
   - `CronDBRunsUntilShutdown`: an isolated `&Database{...}` as in `TestW1SQLiteShutdownDuringInit`, with `go db.cronDBEvery(10ms)`. After closing `StopChan` the goroutine still runs 50 ms later; after `Shutdown()` it returns within 1 s.
   - `UpsertSectionID`: two calls return the same id, and a `section_groups` insert with it succeeds with foreign keys on.
   - `NNTPAuthDisabledWebUser`: a web user with a linked NNTP user authenticates and is cached; after `UpdateUserStatus(disabled=1)` the same credentials fail. A second NNTP user fails after `DeactivateNNTPUser`, on the cache-hit path too.

Acceptance: the listed tests pass with `-race`; the WSH script stays at `fail=0`; L03 and L11 still PASS.

---

#### Slice `lo2-web-pages`: section access, huge pages, threads API offset, display-name DB limit
Findings: F6 (DB half), F9, F10, F15. **F1 is deferred** (decision 11): do not touch pagination, `webgroupPage.go`, `models.PaginationInfo` or `pagination.html`. Tag `lo2-web-pages`, ports 18997/19007.

Owned files:
- `internal/web/web_sectionsPage.go`
- `internal/web/web_apiHandlers.go` (the `X-Next-Offset` line in `getGroupThreads` only)
- `internal/database/queries.go` (`DeleteNewsgroup` and `UpdateUserDisplayName` only)
- `internal/database/thread_cache.go` (the offset guard in `GetCachedThreadReplies` only)
- `internal/web/lo2_pages_test.go` (new), `internal/database/lo2_pages_test.go` (new)

Changes:
1. **F1: skipped** (deferred by the user in wave 0; see Decisions 11).
2. **F15:**
   - `sectionPage`: clamp `page` to `max(1, ceil(totalCount/LIMIT_sectionPage))` before computing `start`.
   - `GetCachedThreadReplies`: set `page = max(page, 1)` and return an empty slice with `totalReplies` when `pageSize <= 0 || page-1 >= ceil(len(childNums)/pageSize)`, before any multiplication.
3. **F9:** the header comes from `nextThreadsOffsetHeader(offset, limit, len(threads), apiThreadsMaxOffset) string`, which returns `""` unless `n == limit && offset+limit <= maxOffset`.
4. **F10:**
   - `sectionGroupAllowed` calls `s.checkGroupAccess(c, groupName)` after the membership match and returns `nil, false` when it fails; that renders the same 404 as `/groups/:group`, and admins keep access to inactive groups.
   - `DeleteNewsgroup` runs the newsgroup delete and `query_DeleteSectionGroupsByNewsgroup` (`DELETE FROM section_groups WHERE newsgroup_name = ?`) in one `RetryableTransactionExec`, the second only when the first affected 1 row. On success it keeps the hierarchy cache invalidation. (**Amended during the run:** the spec also asked for `uiCacheHeaderSections.invalidate()`. That call was implemented, then removed again in `51ac7a6` — the cache holds `sections` rows only (`query_GetHeaderSections`), which deleting a newsgroup and its `section_groups` rows cannot change, so it was dead work that also made every concurrent in-flight load discard its result. The removal's justification was independently re-verified in the post-merge review wave; no cache covers section→group membership at all.)
5. **F6 (DB half):** `UpdateUserDisplayName` rejects `utf8.RuneCountInString(displayName) > 64` instead of a byte length.
6. Tests:
   - web (`lo2_pages_test.go`):
     - `SectionHugePage`: a section with one active group; `GET /<section>/?page=100000000000000000` returns 200.
     - `SectionRouteNeedsGroup`: a `section_groups` row for a name with no `newsgroups` row gives 404 on `/<section>/<name>/` and `/<section>/<name>/tree/1`, and `filepath.Glob(<datadir>/db/*/<sanitized>.db*)` stays empty; an inactive member group gives 404 for anonymous users and 200 for an admin.
     - `NextThreadsOffsetHeader` table: `(0,500,500)`→`"500"`, `(999000,1000,1000)`→`"1000000"`, `(1000000,2000,2000)`→`""`, `n<limit`→`""`.
   - database (`lo2_pages_test.go`):
     - `ThreadRepliesHugePage`: a `thread_cache` row with 3 children; page `1<<60` returns an empty slice without panicking.
     - `DeleteNewsgroupRemovesSectionGroups`: deleting an inactive group removes its `section_groups` rows; an active group is not deleted and keeps its rows.
     - `DisplayNameRuneLimit`: 64 × `ä` is accepted, 65 × `ä` is rejected.

Acceptance: L14 and L15 PASS; the WSH script stays at `fail=0` (E08-E10 cover group access).

---

#### Slice `lo2-web-forms`: session idle timeout, profile validation, reply message-id, newsgroups message
Findings: F2, F5, F6 (web half), F7, F8. Tag `lo2-web-forms`, ports 18998/19008.

Owned files:
- `internal/database/db_sessions.go` (the slide condition in `ValidateUserSession` only)
- `internal/web/web_auth.go` (`setSessionCookie` and `lookupWebSession` only)
- `internal/web/web_profile.go` (`profileUpdate` only)
- `web/templates/profile.html` (the display-name help text only)
- `internal/web/web_sitePostPage.go` (`postMessageIDRe` and the newsgroups message only)
- `internal/web/w1_api_test.go` (rows of `TestW1APIValidatePostHeaders` only)
- `internal/web/lo2_forms_test.go` (new), `internal/database/lo2_forms_test.go` (new)

Changes:
1. **F2:**
   - `const sessionSlideEvery = 5 * time.Minute`; slide when `time.Until(*user.SessionExpiresAt) < SessionTimeout-sessionSlideEvery`. The comment says: at most one write per user every 5 minutes, idle timeout at least 55 minutes.
   - `setSessionCookie(c, id)` keeps its signature and calls `setSessionCookieMaxAge(c, id, int(database.SessionTimeout.Seconds()))`; `lookupWebSession` passes `sessionCookieMaxAge(*user.SessionExpiresAt)`, the remaining seconds clamped to 1..`SessionTimeout`.
2. **F5 and F6 (web half):** `profileUpdate` validates everything before its first write, in this order: email required; `validateDisplayName` only when `displayName != user.DisplayName`; current password; email not used by another user; and, when a password change is requested, match plus `validatePassword` plus hashing. Only then the writes: password, email, and the display name only when it changed.
   In `profile.html`, replace the two format bullets with "Posts use 'Your Name &lt;noreply@…&gt;'. Up to 64 characters; &lt;, &gt; and \" are not allowed." and keep the warning lines.
3. **F7:** `postMessageIDRe = ^<[\x21-\x3B\x3D\x3F-\x7E]{1,248}>$` (printable ASCII without `<`, `>` and space), keeping the 250-byte check; update the comment. In `TestW1APIValidatePostHeaders`, `<a@b@c>` and `<ab>` become valid, and `"<a\x0bb@c>"`, `"<a\x7f@c>"` and `"<ä@c>"` are added as invalid; the other rows stay.
4. **F8:** move the "No valid newsgroups specified" check into the parse block, after the loop, as `if len(errors) == 0 && len(newsgroups) == 0`.
5. Tests:
   - database (`lo2_forms_test.go`): `SessionSlideEvery`: an expiry at now+58 min is left alone, one at now+50 min moves to about now+60 min.
   - web (`lo2_forms_test.go`):
     - `CookieFollowsExpiry`: a `w0NewUser` session whose DB expiry is set to now+58 min; `GET /profile` sets a cookie with Max-Age between 3400 and 3480.
     - `LegacyDisplayNameAllowsEmailChange`: with `Jane <jane@x.invalid>` stored, POST `/profile` with that name and a new email gives 303, a changed email and an unchanged name.
     - `ProfileValidatesBeforeWriting`: a new email with mismatched new passwords is rejected and the email is unchanged; the same for a new email with a 65-rune name.
     - `NoMisleadingNewsgroupError`: a reply with an invalid message-id to an active group shows "Invalid reply message-id" and not "No valid newsgroups specified".

Acceptance: L13 PASS; WSH E01-E05, E15-E17, E25 and E26 still PASS.

---

#### Slice `lo2-web-server`: client IP header setting, trusted defaults, chat body limit, Ollama sync timeout, cron loop, BadBots clear
Findings: F3, F4, F11, F12, F13, F14. Tag `lo2-web-server`, ports 18999/19009.

Owned files:
- `internal/web/webserver_core_routes.go` (`DefaultReverseProxy`, the trusted-proxy setup in `NewWebServer`, `configureTrustedProxies*`, `ReverseProxyMiddleware` only)
- `internal/config/config.go` (the two new constants and `UpdateBadBots` only)
- `internal/web/web_admin_settings_unified.go` (one new entry and the BadBots success message)
- `internal/web/web_admin.go`, `internal/web/web_adminPage.go` (the two new template fields only)
- `web/templates/admin_settings.html` (one new row after "Reverse Proxy")
- `internal/web/web_aichatPage.go` (the body limit in `aichatSend` only)
- `internal/web/web_admin_ollama.go` (`ProxyURL` and the HTTP call in `adminSyncOllamaModels` only)
- `internal/web/cronjobs.go` (the `CronJobManager` fields, `NewCronJobManager` and the `StartCronManager` loop)
- `internal/web/lo2_server_test.go` (new)

Changes:
1. **F3:**
   - Add `CFG_KEY_REVERSEPROXY_IPHEADER string = "ReverseProxyIPHeader"` and `FORM_FIELD_REVERSEPROXY_IPHEADER string = "reverse_proxy_ip_header"`. A missing key reads as `""`.
   - `configureTrustedProxiesWithHeader(router *gin.Engine, addrs, header string) []*net.IPNet` sets `RemoteIPHeaders` to exactly one header: `""` or `X-Forwarded-For` → X-Forwarded-For; `X-Real-IP` → X-Real-IP; anything else → a `[WEB]` log line and X-Forwarded-For.
   - `configureTrustedProxies(router, addrs)` stays and calls it with `""`; `NewWebServer` reads the new key next to `CFG_KEY_REVERSEPROXY`.
   - A settings entry (validator for those three values, `EmptyAllowed: true`) whose success message says to restart the web server; the admin page fields `ReverseProxyIPHeader` and `FormFieldReverseProxyIPHeader`; a `<select>` row in `admin_settings.html`.
2. **F4:**
   - `DefaultReverseProxy = []string{"127.0.0.0/8", "::1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}`.
   - `NewWebServer` logs the effective trusted list and the header once.
   - `ReverseProxyMiddleware` calls `noteUntrustedForwarder(peer)` when the peer is untrusted, is loopback or private, and the request carries `X-Forwarded-For` or `X-Real-IP`. It logs `[WEB]: ignoring forwarded client IP headers from untrusted proxy %s: add it to ReverseProxyAddr` once per IP, remembering at most 256 IPs in `untrustedForwarderSeen` (mutex plus map, stops adding when full).
3. **F11:** `const maxChatRequestBytes = 64 << 10`; set `c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatRequestBytes)` before `ShouldBindJSON`, and return 413 `{"error": "Request too large"}` when `errors.As(err, new(*http.MaxBytesError))`.
4. **F12:** `ProxyURL` becomes a `var` (tests set and restore it); the sync uses `ollamaSyncHTTPClient = &http.Client{Timeout: 15 * time.Second}` with `http.NewRequestWithContext(c.Request.Context(), http.MethodGet, ProxyURL, nil)` and decodes through `io.LimitReader(resp.Body, 1<<20)`.
5. **F13:** `CronJobManager` gets `reloadEvery time.Duration` (1 minute) and `loadJobs func() ([]*models.CronJob, error)` (`db.GetAllCronJobs`), both set in `NewCronJobManager`. The loop body becomes `loadAndStartJobs() error`; an error is logged and the loop continues, and the wait becomes `select { case <-cm.stopChannel: return; case <-time.After(cm.reloadEvery): }`.
6. **F14:** `UpdateBadBots("")` sets `Default_BadBots = nil`, the same state as a start with an empty setting; the settings message becomes "Bad bots list cleared (no patterns)".
7. Tests (`lo2_server_test.go`):
   - `TrustedHeader` on engines from `configureTrustedProxiesWithHeader`: with `X-Real-IP` that header wins over X-Forwarded-For; by default X-Forwarded-For is used and X-Real-IP ignored; an invalid header name behaves like the default.
   - `DefaultProxies`: peers `127.0.0.2` and `fd00::1` with X-Forwarded-For are trusted.
   - `UntrustedForwarderLoggedOnce`: with `ReverseProxyAddr=127.0.0.1`, two requests from `10.9.9.9` carrying X-Forwarded-For produce exactly one log line naming that IP (capture the log output and restore it in `t.Cleanup`, and count only lines with 10.9.9.9).
   - `ChatBodyLimit`: POST `/aichat/send` with a 200 KiB JSON body and a `w0NewUser` cookie returns 413.
   - `OllamaSyncTimeout`: `ProxyURL` points at a server that never answers and `ollamaSyncHTTPClient` has a 100 ms timeout (both restored); the handler redirects with an error within 2 s.
   - `CronLoopSurvivesLoadError`: a manager with `reloadEvery` 10 ms whose `loadJobs` fails twice and then returns no jobs is called at least 3 times, and `StopCronManager` ends it (take a `db.WG` slot as `StartCronManager` callers do).
   - `ClearBadBots`: `UpdateBadBots("a,b", true)` then `UpdateBadBots("", true)` leaves `Default_BadBots` empty; restore both values.

Acceptance: L12 and L16 PASS; WSH E11-E14, E18-E20 and E23 still PASS.

---

#### Wave 2 inline (orchestrator, after `lo2-pool` merged)
- **B12:** rewrite `internal/web/README.md` as a short, current file map (file → responsibility), without per-function line numbers, and point to `go doc ./internal/web` for details.
- **B13:** write `docs/web-deployment.md` with these sections:
  - reverse proxy: `ReverseProxyAddr`, `X-Forwarded-For`/`X-Forwarded-Proto`, `Host` passthrough, e.g. `proxy_set_header Host $http_host`
  - reverse proxy, from the review fixes: the `ReverseProxyIPHeader` setting (which header your proxy actually sets, with an nginx example for each choice), the widened default trusted ranges, and the "ignoring forwarded client IP headers" log line
  - sessions: the idle timeout is 55-60 minutes and the cookie follows the server-side expiry
  - group pages: known limitation, numbered links stop at page 100 (F1 deferred)
  - CSRF behaviour and its log line
  - sessions: hashed at rest, one re-login after 0028, lockout rules
  - foreign keys: what cascades; run `PRAGMA foreign_key_check;` on a copy before deploying
  - group DB migrations: WAL; the index drops of 0009 on first open
  - templates: `PUGLEAF_DEV_TEMPLATES=1`
  - `-data` for web and fetcher
  - `audit-web-posts` usage and the exit codes (remediation is manual)
- Commit both with `docs: web deployment notes and web package map`.

---

## Checks
On every merged tree (after wave 0, after each merge in waves 1 and 2):
```bash
gofmt -l ./cmd ./internal      # only internal/database/embedded_migrations.go and internal/web/web_admin_provider.go may be listed
go vet ./...
go build ./...
go test -race -count=1 ./internal/database/... ./internal/web/... ./internal/history/... ./internal/nntp/... \
  ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/audit-web-posts/...
./build_webserver.sh && ./build_fetcher.sh && ./build_audit-web-posts.sh     # the last one after lo-audit-tool merges
```
Merge order in wave 1: `lo-db`, `lo-paths`, `lo-web-core`, `lo-web-handlers`, `lo-audit-tool`, with the checks after each merge (merging as branches become ready is fine; the file sets are disjoint). Semantic interplay to watch:
- `lo-db` changes what `users.session_id` holds, so the WSH e2e E01/E02/E15/E16/E18 must still pass after its merge.
- `lo-web-core`'s `trackInFlight` middleware and `lo-web-handlers`' chat 429 both touch request flow: run the WSH script after both.
- `lo-web-core` starts one more goroutine in `NewWebServer` (token usage flusher): the whole web test binary must stay `-race` clean.

Merge order in wave 2: `lo2-db-writer`, `lo2-web-pages`, `lo2-web-forms`, `lo2-web-server`, `lo2-pool`, with the checks after each merge. Semantic interplay to watch:
- `lo2-db-writer` changes retry timing under the batch writer: run `go test -race ./internal/processor/... ./internal/nntp/...` again after its merge.
- `lo2-web-server` changes how the client IP is resolved: run the WSH script's E11 and E12 after its merge.

## End-to-end

Runner: `pugleaf-verifier` on the main checkout (`plan-web-sqlite-leftovers`), scratch data only.
```bash
./build_webserver.sh && ./build_fetcher.sh && ./build_audit-web-posts.sh
PORT=18981 DATA=./data-test-web-sqlite-hardening scripts/test-web-hardening.sh     # regression: SUMMARY pass=25 fail=0
PORT=18991 DATA=./data-test-web-sqlite-leftovers scripts/test-web-leftovers.sh      # SUMMARY fail=0
```
Expected: both summaries have `fail=0`, and neither `webserver.log` contains a `DATA RACE` that mentions `internal/web` or `internal/database`.

Upgrade check (verifier, scratch only):
1. Copy a data dir that the WSH-merged binary created. Build that binary from 03d0178 via `git archive` into the scratchpad, never a worktree in the checkout, and run `scripts/test-web-hardening.sh` with `BIN=<it>` to create the dir.
2. Start the new binary on the copy and check:
   - 0028 is applied, `session_id` is empty for all users, and `login_attempt_at` exists;
   - the old session cookie gets 303 to `/login`;
   - a fresh login works and stores a 64-hex hash;
   - `/groups` returns 200.

### `scripts/test-web-leftovers.sh` (wave 0 writes it; fix only real script bugs later)
Build it from `scripts/test-web-hardening.sh`:
- **Copy the whole safety skeleton** (lines 1–76 at d11201a): `git rev-parse` cd, the `PORT`/`DATA`/`BIN` defaults, the `..` and `./data-test-*` guards, the tool checks, the `curl()` User-Agent wrapper, `NNTPHOST` detection, the `.update` guard, `BIN_ABS`, `rm -rf "$DATA"; mkdir -p "$DATA/run"`, the `web` symlink, `pass`/`fail`/`info`/`q`/`code`/`alive`, `start_server` with cwd `$RUN` and absolute `-data`, `stop_server`, `ensure_up`, the trap, the port-in-use refusal, and the schema check after the first start.
- **Defaults:** `PORT=18990`, `DATA=./data-test-web-sqlite-leftovers`, `BIN=./build/webserver`, `FETCHER=./build/pugleaf-fetcher` (the real output name of `build_fetcher.sh`; the plan said `nntp-fetcher`), `AUDIT=./build/audit-web-posts`.
- **Seed** while stopped: `APIEnabled=true`, `registration_enabled=true`, `AbuseMail`; newsgroup `smoke.test` (active, `hierarchy='smoke'`); API token `smoke-valid-token` (sha256 hex). Register `smokeadmin` (user id 1) and log in with a cookie jar, as the WSH script does.
- **Seed for the review checks**, also while stopped: the active AI model row the WSH script seeds; newsgroup `smoke.inactive2` (inactive, `hierarchy='smoke'`); section `lo2sec` with `section_groups` rows for `zz.lo2.gone` (which has no `newsgroups` row) and for `smoke.inactive2`. After the start, register the user `smokeip` the same way as `smokeadmin`, with its own jar.

Checks (tag in brackets):
- **L01 [lo-db] hashed session at rest:** `sid` from the jar, `db=$(q "SELECT session_id FROM users WHERE username='smokeadmin'")`. PASS when `[ -n "$sid" ] && [ "$db" != "$sid" ] && [ ${#db} = 64 ]` and `/profile` with the jar returns 200.
- **L02 [lo-db] migration 0028:** `q "SELECT count(*) FROM schema_migrations WHERE filename LIKE '0028_main_%'"` = 1 and `q "SELECT count(*) FROM pragma_table_info('users') WHERE name='login_attempt_at'"` = 1.
- **L03 [lo-web-core] API usage flushed on shutdown:** 5× `curl -H 'X-API: smoke-valid-token' $BASE/api/v1/groups`, then `stop_server`. PASS when `usage_count` = 5 and `last_used_at` is not NULL.
- **Article for L04/L05/L11:** `start_server`, POST `/SitePostSubmit` with the admin jar (`newsgroups=smoke.test`, `subject=Leftovers smoke`, `body=hello`). Poll up to 30s until `f=$(find "$DATA/db" -name smoke_test.db)` exists and `sqlite3 "$f" "SELECT article_num FROM articles WHERE subject='Leftovers smoke'"` returns a number (`ART`). If it never appears, FAIL L04 and L05 with "no article" and skip them.
- **L04 [lo-web-core] web preview route:** `GET /groups/smoke.test/articles/$ART/preview` returns 200 and the body contains `Leftovers smoke`.
- **L05 [lo-web-core] API preview follows APIEnabled:**
  1. The API preview returns 200.
  2. `stop_server`; `q "UPDATE config SET value='false' WHERE key='APIEnabled'"`; `start_server`.
  3. The API preview returns 503 and the web preview still returns 200.
  4. `stop_server`; set `APIEnabled` back to `true`; `start_server`.
- **L06 [lo-web-core] tree view uses the web route:** `curl -s $BASE/static/js/thread-tree.js` contains `/articles/${articleNum}/preview` and does not contain `/api/v1/groups/`.
- **L07 [lo-web-core] favicon type:** `curl -sI $BASE/favicon.ico | tr -d '\r' | grep -qi '^content-type: image/'`.
- **L08 [lo-web-handlers] no internal error text in visitor pages (source check):** `grep -nE 'renderError\([^)]*err\.Error\(\)|gin\.H\{"Error": err\.Error\(\)\}' internal/web/*.go | grep -v '_test.go' | grep -v 'web_admin'` prints nothing.
- **L09 [lo-web-core] no internal error text in API JSON (source check):** `grep -n 'gin.H{"error": err.Error()}' internal/web/web_apiHandlers.go` prints nothing.
- **L10 [lo-paths] fetcher honours -data:**
  1. `stop_server`; `FETCHER_ABS` like `BIN_ABS`.
  2. `( cd "$RUN" && exec timeout 120 "$FETCHER_ABS" -data "$DATA_ABS" -nntphostname "$NNTPHOST" ) > "$DATA/fetcher.log" 2>&1`, and record `rc`.
  3. PASS when `rc` != 124, `[ -f "$DATA/progress.db/progress.db" ]` and `[ ! -e "$RUN/data" ]`.
- **L11 [lo-audit-tool] audit tool finds injected rows and is read-only** (server stopped):
  1. `[ -x "$AUDIT" ]`, otherwise FAIL "missing binary".
  2. `"$AUDIT" -data "$DATA"` gives rc 0 and the summary line has `flagged=0`.
  3. Inject into the main DB: `INSERT INTO post_queue(newsgroup_id, message_id, created, posted_to_remote) VALUES((SELECT id FROM newsgroups WHERE name='smoke.test'), '<inject.1@smoke.invalid>', CURRENT_TIMESTAMP, 1)`.
  4. Inject into the group DB: `INSERT INTO articles(article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, path, headers_json, body_text) VALUES(900001, '<inject.1@smoke.invalid>', 'x', 'Evil' || char(13,10) || 'Control: cancel <x@y>', CURRENT_TIMESTAMP, '', '', 1, 1, '.POSTED!not-for-mail', 'From: Evil' || char(13,10) || 'Control: cancel <x@y>' || char(10) || 'Subject: x', 'b')`.
  5. Take `h1=$(sha256sum "$DB" "$f")`, run the audit again (rc 1, output contains `<inject.1@smoke.invalid>`), take `h2`.
  6. PASS when `"$h1" = "$h2"`.
- **L12 [lo2-web-server] configured client IP header:**
  1. `stop_server`; `q "INSERT OR REPLACE INTO config(key,value) VALUES('ReverseProxyIPHeader','X-Real-IP')"`; `start_server`.
  2. Log in as `smokeip` with `-H 'X-Forwarded-For: 198.51.100.9' -H 'X-Real-IP: 203.0.113.7'`; `q "SELECT last_login_ip FROM users WHERE username='smokeip'"` must be `203.0.113.7`.
  3. `stop_server`; `q "DELETE FROM config WHERE key='ReverseProxyIPHeader'"`; `start_server`.
  4. The same login must now store `198.51.100.9`. PASS when both steps match.
- **L13 [lo2-web-forms] a stored legacy display name does not block a profile change:** `q "UPDATE users SET display_name='Jane <jane@smoke.invalid>' WHERE username='smokeadmin'"`, then POST `/profile` with the admin jar (`email=smokeadmin2@smoke.invalid`, `display_name=Jane <jane@smoke.invalid>`, `current_password=$PW`). PASS when the stored email is `smokeadmin2@smoke.invalid`.
- **L14 [lo2-web-pages] section routes need an existing, active group:** anonymous `/lo2sec/zz.lo2.gone/` and `/lo2sec/zz.lo2.gone/tree/1` both return 404 and `find "$DATA/db" -name 'zz_lo2_gone.db*'` prints nothing; anonymous `/lo2sec/smoke.inactive2/` returns 404.
- **L15 [lo2-web-pages] huge page numbers:** `code "$BASE/lo2sec/?page=100000000000000000"` is 200 (500 at baseline).
- **L16 [lo2-web-server] chat body limit:** write `{"message":"<200000 × a>"}` to `$DATA/big.json`, then `code -b "$JAR_ADMIN" -H 'Content-Type: application/json' -X POST --data-binary @"$DATA/big.json" "$BASE/aichat/send"` is 413.
- **Finish:** print `SUMMARY pass=<n> fail=<n> data=$DATA log=$LOG` and exit with the fail count (capped at 125), as the WSH script does.

---

## Progress

### Wave 0 — 2026-09-16, `plan-web-sqlite-leftovers` from `testing-001` @ `194a8c2`

**Preflight.** Working tree clean, `03d0178` confirmed as an ancestor of HEAD, `/tank0/claude/trees` writable.
Baseline `## Checks` on `194a8c2`: `gofmt -l ./cmd ./internal` lists only the two known files
(`internal/database/embedded_migrations.go`, `internal/web/web_admin_provider.go`); `go vet ./...` clean;
`go build ./...` clean; `go test -race -count=1` green for database, web, history, nntp, processor,
expire-news, history-rebuild. `PORT=18981 DATA=./data-test-web-sqlite-hardening scripts/test-web-hardening.sh`
→ **`SUMMARY pass=25 fail=0`**.

**Decisions confirmed with the user.**
- 1 (C1 hashed sessions + migration 0028 clearing `session_id`/`session_expires_at`, one re-login for everyone): **as written**.
- 11 (F1 deep pagination): **DEFERRED**, see Decisions 11. `lo2-web-pages` loses F1; `webgroupPage.go`,
  `models.PaginationInfo`, `pagination.html`, `web_group_pagination.go` and `db_overview_cursor.go` are untouched.
- 13 (F3/F4 `ReverseProxyIPHeader` setting + widened `DefaultReverseProxy` + untrusted-forwarder log): **as written**.
- 2-10, 12, 14-17: **as written**.

**E1 done.** `.claude/CLAUDE.md` test line now includes `./internal/database/... ./internal/web/...`.

**`scripts/test-web-leftovers.sh` written** (safety skeleton copied from `test-web-hardening.sh`), `chmod +x`, `bash -n` clean.
Two corrections against the plan text: `FETCHER` defaults to `./build/pugleaf-fetcher` (the real output of
`build_fetcher.sh`), and L03 compares a usage-count *delta* instead of the absolute 5, so it stays valid
wherever it runs in the sequence.

**Baseline run** `PORT=18990 scripts/test-web-leftovers.sh` → **`SUMMARY pass=1 fail=15`**, matching the plan's
expectation exactly:

| Check | Baseline | Detail |
|----|----|----|
| L01 lo-db | FAIL | `session_id` in the DB equals the cookie value |
| L02 lo-db | FAIL | no 0028 migration, no `login_attempt_at` |
| L03 lo-web-core | **PASS** | regression guard for the buffering (today's per-request goroutine already counts) |
| L04 lo-web-core | FAIL | web preview route 404 |
| L05 lo-web-core | FAIL | API preview answers 200 with `APIEnabled=false` |
| L06 lo-web-core | FAIL | `thread-tree.js` still fetches `/api/v1/groups/` |
| L07 lo-web-core | FAIL | `/favicon.ico` → `Content-Type: text/plain` (B9 confirmed, not the mime.types fallback the plan expected) |
| L08 lo-web-handlers | FAIL | 17 sites show `err.Error()` to visitors |
| L09 lo-web-core | FAIL | 3 API sites show `err.Error()` |
| L10 lo-paths | FAIL | `$RUN/data/progress.db/progress.db` created relative to the cwd |
| L11 lo-audit-tool | FAIL | missing binary (tool does not exist yet) |
| L12 lo2-web-server | FAIL | `X-Real-IP` setting has no effect (both 198.51.100.9) |
| L13 lo2-web-forms | FAIL | legacy display name blocks the email change |
| L14 lo2-web-pages | FAIL | `/lo2sec/zz.lo2.gone/` 200, tree 500, 3 group DB files created, inactive group 200 |
| L15 lo2-web-pages | FAIL | `?page=100000000000000000` → 500 |
| L16 lo2-web-server | FAIL | oversized chat body → 400, not 413 |

**L10 precondition checked** (plan wave-0 step 4). With the scratch data dir the fetcher does **not** open any
network connection: the default DB seeds 23 providers all with `enabled=0`, so no pool is built and
`nntp.NewPool` is never called. It does not exit cleanly either — it panics at
`cmd/nntp-fetcher/main.go:286` with `index out of range [0] with length 0`. L10 therefore stays in the script,
and the missing guard became **A1b** in the `lo-paths` slice (same file, same lines that slice already edits).

**Base commit for wave 1:** the commit below.

### Wave 1 — merged, all 5 slices

Merge commits on `plan-web-sqlite-leftovers`, in merge order:

| Slice | Branch | Impl commits | Merge | Review verdict |
|----|----|----|----|----|
| `lo-paths` | `worktree-agent-abc86bcd09fd14b0c` | `00fffba` | `b59d869` (+ minors `24c5e85`) | MERGE |
| `lo-db` | `worktree-agent-a5e5d0ec0bce9dec8` | `d973d66`, `c4bb5ff`, `3df5c79` | `22bcb97` (+ minors `c5c5b89`) | MERGE |
| `lo-web-handlers` | `worktree-agent-ad3e4d0aaa02c68e3` | `6322123`, `85356fe` | `46d90fd` | MERGE AFTER FIXES → fixed |
| `lo-web-core` | `worktree-agent-aab185f8152794720` | `37a5300`, `f5646fc` | `2544883` | MERGE AFTER FIXES → fixed |
| `lo-audit-tool` | `worktree-agent-a103990bc940c4fa8` | `40db1ac`, `0a6203c` | `cad30fd` | MERGE AFTER FIXES → fixed |

**Checks on the fully merged tree (`cad30fd`):** `gofmt -l ./cmd ./internal` lists only the two baseline
files; `go vet ./...` and `go build ./...` clean; `go test -race -count=1` green for database, web,
history, nntp, processor, expire-news, history-rebuild and audit-web-posts; all three build scripts
produce their binaries. `scripts/test-web-leftovers.sh` → **`pass=11 fail=5`** (L01-L11 all PASS; the 5
failures are exactly the wave-2 tags L12-L16). `scripts/test-web-hardening.sh` → **`pass=25 fail=0`**,
0 DATA RACE reports in both scratch logs.

**Review findings that mattered** (each was missed by the slice's own passing tests):
1. **`lo-audit-tool`, blocker.** `mode=ro` does not stop SQLite creating the wal-index: every pugleaf DB
   is WAL, so the tool left `-shm`/`-wal` next to every database it opened, i.e. it wrote into `data/cfg/`
   and `data/db/`. Its tests passed because the fixtures were built without pragmas and were therefore
   `journal_mode=delete`. Fixed with `immutable=1`, gated: used when no non-empty `-wal` sibling exists,
   otherwise a plain `mode=ro` open plus a stderr warning, with the new `-strict` flag to refuse instead.
   Refuse-by-default was tried first and **failed L11** — a pending `-wal` is routinely left behind
   (`stop_server`, and the fetcher's `log.Fatalf` path exits without checkpointing).
2. **`lo-audit-tool`, major.** `-all` applied the web-poster header allow-list to peer-fetched articles,
   which legitimately carry `Path`, `Date`, `Organization`…: one normal peer article produced 5 findings
   and rc 1. Now rules 1-3 apply to all articles and rule 4 only to queued rows or `path = '.POSTED!not-for-mail'`.
3. **`lo-web-handlers`, major — a finding against this plan, not the implementer.** The plan specified
   `funcMapKey` = the FuncMap's *address*, but nothing retained the map (`Template.Funcs` copies entries),
   and a reproduction showed **1 distinct address across 2000 allocate/drop/GC cycles**. The first call site
   building a per-request FuncMap would have silently rendered one request's page with another's functions.
   Fixed by storing the map in the cache entry (`tmplEntry`), making the key unique by construction.
4. **`lo-web-core`, major.** `Shutdown` closed `stopCh` first, so the token-usage flusher did its final
   flush and exited *before* `srv.Shutdown(ctx)` drained in-flight requests; usage recorded during the
   drain was lost on every shutdown — a deterministic regression of the property B6 exists to protect.
   Fixed with a final `flushTokenUsage()` after the drain, plus explicit `[API]` logging of counts that
   still could not be written. Regression test verified to fail without the fix.
5. **`lo-db`, minor but load-bearing.** `w2_dbperf_test.go` kept its own copy of the child query spelled
   `AND hide = 0`. Since that guard only asserts `!strings.Contains(plan, "SCAN articles")` and the old
   spelling plans as a `SEARCH` on `idx_articles_hide_date`, a revert of C4 would have passed the guard.

**Corrections to the plan's findings, for the Outcome:**
- **B7 was overstated.** `renderError` only ever put `message` into the template data, so most of the 17
  sites were not leaking internal text to visitors. Only the three `error.html` 200s and a rare `c.String`
  fallback did. The change is still correct defence-in-depth, but it did not close 17 live leaks.
- **B9's live half was not `getContentType`.** `/static/*` is served by `http.FileServer` (Go's own mime
  table), and `EmbeddedFileHandler` has no caller, so the content-type table is test-only. The real defect
  was that `/favicon.ico` was registered for GET only and gin answers HEAD from a separate route tree:
  `curl -sI` fell through to the built-in 404 with `text/plain`. That, not `/etc/mime.types`, is what the
  wave-0 baseline recorded.
- **A5 has a live-data consequence.** `rslight-importer -reset-groups` used to be a guaranteed no-op
  (it looked for a `<data>/groups` directory that never exists). It now really deletes articles, threads
  and caches in every group DB that has a file; the only guard is the 5-second countdown at
  `cmd/rslight-importer/main.go:121-132`. Worth a release note.

**Deliberate departure from the plan text:** the queue-full post undo restores a bounded ~5s back-off
(`now - WebPostingBackOff + 5`) rather than the full previous `lastpost_unix` the Design specifies.
`WebPostingBackOff` is the only per-user throttle on `/SitePostSubmit`, so restoring it fully let a client
hammer the route while the queue is full. B3's intent (not charging the user a post, not punishing them
for 42s over a server-side failure) is preserved.

**New leftovers raised by wave 1:**
- `EmbeddedFileHandler`/`staticContentType` are dead in production — wire them back or delete both.
  `.scss` is shipped under `internal/web/static/` but absent from the type table (harmless today).
- `audit-web-posts` against a live-ish data dir may warn about a pending `-wal` and, for a database whose
  wal-index is absent, create `-shm`/`-wal`. `-strict` guarantees zero touch; auditing a copy is the other way.
- `InvalidateUserSessionBySessionID` still has no in-tree caller (logout goes through `InvalidateUserSession`).
- `rsyncInactiveGroupsToDir` never checks `rows.Err()` (pre-existing, outside the wave-1 slices).

### Wave 2 — merged, all 5 slices + inline docs

| Slice | Branch | Impl commits | Merge | Review verdict |
|----|----|----|----|----|
| `lo2-web-pages` | `worktree-agent-a587de24703d4f43e` | `3cefdc9`, `bc4050b` | `d4efcef` (+ minors `51ac7a6`) | MERGE |
| `lo2-web-forms` | `worktree-agent-ad2a075c1c0ccb2de` | `3a0095b`, `d577846`, `a31391d` | `30dab58` (+ minors `49d2755`) | MERGE |
| `lo2-pool` | `worktree-agent-abf4ef7566b16b3ac` | `981aada`, `eab8aff`, `1f4437a` | `72d9277` | measurement only, no review agent |
| `lo2-db-writer` | `worktree-agent-ae0262a129317988b` | `cf86624`, `85a7ba4`, `39f8387` | `01a544e` | MERGE AFTER FIXES → fixed |
| `lo2-web-server` | `worktree-agent-a16c71c90604c2787` | `9ae6416`, `24319fc`, `0efaaca` | `a8fad7a` (+ test row `c9d6565`) | MERGE AFTER FIXES → fixed |

Inline: `a50b385` (B12 `internal/web/README.md`, B13 `docs/web-deployment.md`).

**Checks on the fully merged tree (`a50b385`):** gofmt/vet/build clean (two baseline files only);
`go test -race -count=1` green across all 8 packages, with `./internal/processor/...` and
`./internal/nntp/...` run twice because the retry clock moved. **`scripts/test-web-leftovers.sh`
→ `pass=16 fail=0`** (baseline was `pass=1 fail=15`), **`scripts/test-web-hardening.sh` →
`pass=25 fail=0`**, 0 DATA RACE reports in both scratch logs.

**Review findings that mattered in wave 2** (again, each missed by the slice's own passing tests):
1. **`lo2-db-writer`, major — shutdown could hang forever.** `updateNewsgroupStatsWithRetry` retried
   SQLITE_BUSY unbounded (Decision 17) while sitting on the `db.WG.Wait()` path. Every tool runs
   `close(StopChan)` → `WG.Wait()` → **then** `db.Shutdown()`, so the "it exits when the DB closes"
   escape could never fire — the close is downstream of the wait it blocks. A foreign write lock on
   `pugleaf.sq3` during a fetcher shutdown meant SIGKILL. Now bounded once `IsDBshutdown()` is true.
2. **`lo2-db-writer`, major — `retry2` abandoned a batch mid-write.** After phase 1 committed the
   articles, a threading failure plus a failed reopen left them with no threading, no history entries
   and no `message_count`/`last_article`, announced by a single `Failed2` log line. Now retried like
   `retry1`, with its own "committed without threading/history/stats (rebuild needed)" wording.
   **Not** recorded as covered by F16.
3. **`lo2-web-server`, major — F14's fix was unreachable.** `config.UpdateBadBots("")` is correct and
   unit-tested, but the admin dispatch at `web_admin_settings_unified.go:208` never called the BadBots
   processor, so `processBadBotsUpdate` was dead code. Clearing the field wrote the row, displayed the
   new "Bad bots list cleared (no patterns)" message, and left the server blocking those agents until
   restart — the slice made a pre-existing bug user-visible by asserting a state the server was not in.
   Now wired, with a test that drives the real admin form and fails without the fix.
4. **`lo2-web-server`, minor — the fix for F4 could itself flood the log.** `noteUntrustedForwarder`
   stopped *inserting* at 256 IPs but kept *logging*, i.e. one line per request instead of one per IP.
   Bounded now by a `full` flag plus one suppression line.
5. **`lo2-web-pages`, minor — the F15 clamp had a hole.** A non-positive `LIMIT_sectionPage` (a package
   `var`) makes `end` negative, which neither existing clamp catches — the same panic class the slice
   closes. Fixed at merge.

**Intended behaviour change that broke an existing test.** F3 reduces `RemoteIPHeaders` to the one
configured header, so `TestW1ServerTrustedProxies/"valid x-real-ip from trusted peer"` no longer holds:
with no XFF present, gin returns the trusted peer's own address. Confirmed against the gin source
(`gin.go:472-475`, `context.go:951-959`); the row now expects `192.168.1.1` and is renamed
(`c9d6565`). X-Real-IP as the *configured* header is covered by `TestLo2ServerTrustedHeader`.

**C6 outcome (Decision 8): keep `MaxOpenConns 100` / `MaxIdleConns 25`.** `db_init.go` unchanged.
Across 35 measured cells (7 sweeps × 5 sizes, tmpfs and ZFS) there were **zero** SQLITE_BUSY retries,
and `MaxOpenConns=100` never opened more than **34** connections — `sql.DB` opens lazily, so the 100
is a ceiling, not an allocation. Cheap statements improve monotonically with pool size (147 µs p50 at
100 vs 3.41 ms at 4); the only clearly wrong value is a *small* pool, which costs 30-50% throughput.
The real memory lever is `CacheSize` (16 MiB per connection), not the pool size. Full report:
`docs/perf/main-db-pool.md`.

**Honest caveat on the regression suite.** WSH check E12 now passes for a weaker reason: it sends an
invalid `X-Real-IP` and asserts the peer IP, but X-Real-IP is never consulted now, so it would pass
with a valid one too. The outcome is strictly safer and the real coverage moved to
`TestLo2ServerTrustedHeader`; K4 forbids editing the script, so this is a note, not a change.

**New leftovers raised by wave 2:**
- ~~`processBadIPsUpdate` is dead~~ — **fixed after the wave, in `3995443`.** The note first recorded
  here was wrong on two counts and is corrected for the record: `FORM_FIELD_BADIPS` did **not** have the
  same defect as BadBots. Its table entry declared `Processor: nil` *deliberately*
  (`web_admin_settings_unified.go:154`), and its success message never claimed "applied immediately" —
  that string is line 138, the BadBots one. So the "one-line fix" first written here (adding the field to
  the `:208` dispatch alone) would have called `cfg.Processor(s, value)` on a nil func and panicked on
  every save of the IP list. What was true: `processBadIPsUpdate` had no reference anywhere, and saving
  the blocked-IP list wrote the config row without calling `config.UpdateBadIPs`, so the running server
  kept its old ranges until a restart. The real fix needed three edits — set the processor at `:154`, add
  the field to the `:208` condition, add a `case` at `:224` — plus `TestLo2ServerBadIPsSettingApplied`,
  verified to fail without the dispatch change. It also makes the existing "Bad IPs list cleared (using
  defaults)" message true for the first time.
  *Process note:* this entry originally repeated an implementer's aside without opening the file. Claims
  that do not gate a merge still reach the reader as assertions and need the same verification.
- `profileUpdate`'s three writes (password, email, display name) are separate un-transacted statements.
  They now go through `RetryableExec`, but a transient failure on the second still leaves the first
  committed. The full fix is one `RetryableTransactionExec` around all three.
- `cronjobs.go`: a job started during shutdown can escape `StopCronManager`'s `jobIDs` snapshot.
  Pre-existing; the window is now smaller, not larger.
- `cmd/nntp-analyze/main.go:280` closes `StopChan` without `db.Shutdown()`, so with F20 `cronDBEvery`
  never returns there. Harmless (outside `db.WG`, process exits), left untouched.
- The shutdown bounds are time-shaped constants (`batchStatsShutdownRounds` 24,
  `batchGroupDBShutdownAttempts` 120, both ≈2 min); a loaded box may want more.
- `getBatchGroupDB`'s shutdown bound has no unit test — reaching it costs the real 60s `GetGroupDB`
  deadline. It is a plain counter beside the tested `batchGroupDBRetry` table.
