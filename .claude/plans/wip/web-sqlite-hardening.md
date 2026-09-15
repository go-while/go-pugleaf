# Plan: web + SQLite hardening (security, crashes, performance)

Slug: `web-sqlite-hardening` · Integration branch: `plan-web-sqlite-hardening` (from `testing-001`)
Run with: `/run-plan .claude/plans/queued/web-sqlite-hardening.md`
Parallelism: wave 1 has 4 implementer slices, wave 2 has 2. Within a wave, no two slices own the same file.

---

## Context

A read-only review of `internal/web` (about 13k lines), `internal/database` (about 11k lines) and the migrations,
done on `testing-001` @ 08c29f5, found remote crashes, auth bugs, SQLite lifecycle races and
per-request performance waste. Every finding below was traced in code. "Plausible" marks the one
where a downstream step was not fully proven. Finding IDs are used in the slices.

### Findings

**Critical: remote crash, DoS or freeze**
| ID | Where (at 08c29f5) | Defect |
|----|----|----|
| C1 | `database/db_apitokens.go:101-103`, `web/web_apitokens.go:39-43` | An expired token makes `ValidateAPIToken` return `(nil, nil)`. The middleware continues, and its goroutine dereferences `apiToken.ID` on nil. The panic is outside gin Recovery, so **the process dies**. |
| C2 | `web/webserver_core_routes.go:273`, `web/cronjobs.go:74`, `web/web_admin_crons.go:242,324` | With `-no-cronjobs`, `CronManager` is nil, but `StartCronManager()` is still called and its goroutine derefs `cm.stopChannel`, so the process crashes at startup. The admin cron handlers deref nil too. |
| C3 | `web/webserver_core_routes.go:650-677`, callers `web_admin_sections.go:86,166,209` | The sections map is replaced and filled while requests read it, which is a fatal `concurrent map read and map write`. |
| C4 | `web/web_threadTreePage.go:28-55` (`/api/thread-tree`, public), `:172-198` (section tree), `web/web_sitePostPage.go:76` | These call `GetGroupDB(<any string>)` with no existence or active check. `GetGroupDB` (`db_groupdbs.go:101-171`) **creates a directory, a SQLite file and the schema** for any name. Consequences: disk and inode DoS, forced closes past 256 open DBs, and inactive groups readable. The section group, article and message-id pages are **not** affected because they check `section_groups` membership. |
| C5 | `web/webserver_core_routes.go:532-533`, `web/web_aichatPage.go:250` | `BotDetectionMiddleware` holds `config.BadBotsMutex.RLock` across `c.Next()`, and the Ollama `http.Post` has no timeout. When an admin saves bot settings, `Lock()` waits for the hung request and every new request blocks, so the **whole site freezes**. |
| C6 | `web/webserver_core_routes.go:494-507` | `Router.Run`/`RunTLS` start an `http.Server` with no timeouts and nothing ever calls `Shutdown` on it, so slowloris connections exhaust file descriptors. |

**High**
| ID | Where | Defect |
|----|----|----|
| H1 | `web/webserver_core_routes.go:556-596` | Any loopback or RFC1918 peer is trusted. RemoteAddr is taken from the **first** `X-Forwarded-For` entry (client-controlled) and then from an unvalidated `X-Real-IP`, so client IP spoofing bypasses IP blocks and fakes `last_login_ip`. `X-Forwarded-Host` overwrites `Host`. `SetTrustedProxies` has no effect. |
| H2 | `web/web_login.go:25-29,62-67,132`, `web/web_auth.go:111,127` | Open redirect: `redirect` is used as-is (`https://evil`, `//evil`). `?redirect=` is built unescaped. |
| H3 | `web/web_login.go:77-118`, `database/db_sessions.go:118-164` | The lockout lookup runs `WHERE username=?`. **Email login always fails** with "Login error". Unknown users get "Login error" and known users get "Invalid…", which **enumerates usernames**. The increment is a no-op for email and the check-then-increment races. |
| H4 | `web/web_registerPage.go:132`, `web/web_auth.go:192-219` | Registration creates the session in the legacy `sessions` table, but auth reads `users.session_id`, so **new users are not logged in**. |
| H5 | `web/web_auth.go:258-264` (x/crypto v0.43.0) | Passwords of 73–255 bytes pass validation, but `bcrypt.GenerateFromPassword` returns `ErrPasswordTooLong`. |
| H6 | `database/db_groupdbs.go:87-100` vs `database/database.go:31-126` | TOCTOU: `GetGroupDB` checks state, unlocks, then `IncrementWorkers`. In between, `cleanupIdleGroups` (the force path at 256 or more open DBs has no idle requirement) can close the DB, and the caller uses a closed `*sql.DB`. |
| H7 | `database/db_groupdbs.go:119,130,144,159`, `database/database.go:128-132` | When init fails, the map entry is set to nil, but waiters already polling `state==CREATED` spin **forever**. |
| H8 | `database/db_init.go:317-382`, `database/db_groupdbs.go:128-139` | PRAGMAs run with `(*sql.DB).Exec`, so only **one pooled connection** gets `foreign_keys`/`busy_timeout`/`synchronous`/`cache_size`/`temp_store`. New group DBs skip the pragmas (`if dbExists`). Group pools have no idle limit. |
| H9 | `migrations/0001_single_db_schema.sql:8-13`, `database/db_migrate.go:228-238` | The group schema runs `PRAGMA synchronous=OFF; journal_mode=DELETE` on the first open. A migration and its `schema_migrations` row are not atomic, so a crash leaves a half-applied schema that fails on every start. |
| H10 | `web/web_sitePostPage.go:332-351,398`, `web/web_profile.go:103,211`, `nntp/nntp-cmd-helpers.go:19-22` | The display name (no CR/LF check at profile update) goes into `From:` inside `HeadersJSON`, which the NNTP server splits on `\n`, so **header injection** is confirmed. Subject and reply `message_id` are unchecked too (plausible path). A display name containing `<>` is used raw, so `From` can be spoofed. |
| H11 | `web/web_aichatPage.go:87,128-129,181,215,232` | The **HttpOnly session ID is written into page JS**. Chat history is keyed by a client token. `chatHistoryCache`/`chatRateLimiter` are unbounded. `SessionToken[:8]` panics on short input. Chat content is logged. |
| H12 | `web/web_login.go` + `database/db_sessions.go:67-69` | **Disabled users can log in**, and existing sessions of disabled users stay valid. |

**Medium: performance**
| ID | Where | Defect |
|----|----|----|
| P1 | 37 `template.ParseFiles` sites in 21 files under `internal/web` | Templates are read and parsed from disk on every request, and `template.Must` panics on error. |
| P2 | `web/web_utils.go:65-114`, `web/web_auth.go:160-189`, `database/db_sessions.go:81-88`, `web/web_helpers.go:15,47` | A logged-in page view costs about 2 `UPDATE users` and about 10 SELECTs. `getWebSession` and `GetUserByID`+`GetUserPermissions` run 2–3 times per request, and base data (site news, header sections, AI models) is not cached. |
| P3 | `database/db_groupdbs.go:87`, `database/db_apitokens.go:52-180`, `database/db_aimodels.go` | The global `MainMutex` is taken exclusively for **every** group DB lookup. The API token and AI-model functions hold `MainMutex` around main-DB queries with retry sleeps, and each API request spawns a goroutine that takes `Lock`. |
| P4 | `database/sqlite_retry.go:13-15,24-28,167-231` | 10000 retries with a 2.5s cap means about 7 hours of hanging. The "busy"/"locked" substring matching is too broad. The shadowed `err` in `RetryableTransactionExec` returns nil after the commit retries are exhausted. A log line is written on every retry. |
| P5 | `web/webgroupPage.go:79-100`, `web/web_apiHandlers.go:121-134`, `web/web_sectionsPage.go:212-226`, `web/web_apiHandlers.go:242,252` | `OFFSET (page-1)*128` with unbounded `page` means deep scans. The threads API is unpaginated. The public `getStats` loads all groups on each call. |
| P6 | `database/queries.go:1441-1617` | Search uses `LIKE ? COLLATE NOCASE` without a usable index (a full scan plus a COUNT scan). `%`/`_` are not escaped. |
| P8 | `migrations/0001_main_schema.sql:43`, `0001_single_db_schema.sql:95,151-152`, `0008_single_spam_hide_performance_indexes.sql` | Redundant indexes on every group DB's `articles`: `idx_articles_message_id` (duplicates UNIQUE), `idx_articles_hide` (= `idx_articles_hide_article_num`, since article_num is the rowid), and `idx_articles_spam` (prefix of `idx_articles_spam_hide`). `idx_name` duplicates UNIQUE(name). |

**Medium/Low: security and robustness**
- **S1:** No CSRF protection besides SameSite=Lax. `GET /logout` is CSRF-able. Admin can create shell cron jobs when `-edit-cronjobs` is set.
- **S2:** `database/tree_view_api.go:224-313` puts an unescaped `groupName` into `template.HTML`. `TemplateData.Title` is `template.HTML` built from user and subject strings (today it only renders inside `<title>`).
- **S3:** The spam flag check-then-increment race lets one user raise the count several times (`webgroupPage.go:170-201`). The posting back-off check-then-update race bypasses the 42s back-off (`web_sitePostPage.go:236,303`).
- **S4:** `sectionThreadTreePage` builds `section+"."+group`, while all other section routes use the full group name.
- **L1:** `getContentType` suffix bug (`embedded_static.go:88-121`).
- **L2:** Session cleanup is never started (`web_session_cleanup.go`).
- **L3:** Bad-bot patterns are not lowercased (`config.go:587-600`).
- **L4:** The stats-logger goroutine and `CronDB` loop ignore `StopChan` (`database.go:22-29,249-255`).
- **L5:** Bubble sort in cleanup; nil deref in the `GroupDB.Return()` log.
- **L6:** Rune-unsafe preview truncation (`web_apiHandlers.go:363-369`).
- **L7:** `sectionValidationMiddleware` allocates `knownPaths` on every request.

### Decisions already made (behaviour changes, to report to the user at the end)
1. Client IP comes from gin's trusted proxies. Config `ReverseProxyAddr` sets them; when empty, the existing `DefaultReverseProxy` list (loopback + RFC1918) is used. XFF is evaluated right to left.
2. CSRF uses Go 1.25 `http.CrossOriginProtection` (checks `Sec-Fetch-Site`/`Origin`) around the whole router. Forms and templates don't change. A reverse proxy must pass `Host` through.
3. Passwords are limited to 72 bytes (bcrypt).
4. Web posts always use `From: <sanitized display name> <noreply@pugleaf.net.invalid>`.
5. `/api/thread-tree` and the section tree need an accessible group. Admins keep access to inactive groups, as elsewhere.
6. `/api/v1/groups/:group/threads` paginates (`limit` default 500, max 2000; `offset`). The body stays a JSON array, and the `X-Next-Offset` header is set when more rows exist.
7. The section tree route uses the full group name, like the other section routes.
8. SQLite retries are capped by the total wait `SQLiteMaxRetryWait` (default 5m, a variable tools can raise).
9. The YouTube redirect for unknown sections stays as is.

### Out of scope (leftovers, list them in Outcome)
- Hashing `users.session_id` at rest.
- Main DB `MaxOpenConns` tuning (measure first).
- `SanitizeGroupName` `.`→`_` file-name collisions (a layout decision).
- Linking the API preview endpoint to `APIEnabled`.

---

## Design

### Contracts all slices rely on (must stay unchanged in wave 1)
- **K1: exported and shared signatures.**
  - `web`: `NewWebServer(db, webconfig, nntp, cronEdit, noCronjobs) *WebServer`, and the fields `WebServer.Router` and `WebServer.DB`.
  - `web` handler helpers: `(*WebServer).getWebSession(c) *SessionData` (nil when not logged in), `checkGroupAccess(c, name) bool`, `checkGroupAccessAPI(c, name) bool`, `requireAdminAuth(c) bool`, `requireAdminAuthJSON(c) bool`, `getBaseTemplateData(c, title) TemplateData`, `renderError(c, code, msg, detail)`, `renderTemplate(c, name, data)`, `isAdmin(user) bool`.
  - `database` group DBs: `GetGroupDB`, `(*GroupDB).Return`, `ForceCloseGroupDB`, `GetGroupDBWithSuffix`.
  - `database` helpers: all `Retryable*` helpers, `OpenDatabase`, `GetMainDB`, `GetActiveNewsgroupByName`, `GetNewsgroupID`, `IncrementArticleSpam`, `UpdateUserPostCount`, `CreateUserSession`, `ValidateUserSession`, `GetUserByID`, `GetUserByUsername`, `GetUserByEmail`, `InsertUser`, `InsertUserPermission`.
- **K2: new names are reserved per slice** (check with `grep -rn '\b<name>\b' internal cmd` that they're free at the base commit):
  - `w1-server`: `configureTrustedProxies`, `trustedProxyNets`, `isTrustedPeer`, `httpServerMu`, `httpServer`, `newHTTPServer`, `(*WebServer).Shutdown`, `stopCh`, `knownNonSectionPaths`, `chatHTTPClient`, `chatEntry`, `sweepChatCaches`, `chatHistoryKey`.
  - `w1-auth`: `safeRedirect`, `validateDisplayName`, `loginDelay`, `dummyPasswordHash`, `ctxKeyWebSession`, `ctxKeyWebSessionChecked`, `ctxKeyIsAdmin`, `(*WebServer).isAdminRequest`, `(*WebServer).clearRequestSession`, `flashSetAt`, DB `IsUserLockedOutByID`, `IncrementLoginAttemptsByID`.
  - `w1-api`: `ErrAPITokenExpired`, `ErrArticleNotFound`, `FlagArticleSpamByUser`, `TryReserveWebPost`, `GetThreadsPaged`, `sanitizePostDisplayName`, `validatePostHeaders`, `postMessageIDRe`, `clampOffsetPage`, `truncateRunes`, `apiStatsCache`, `(*WebServer).sectionGroupAllowed`, `maxOffsetArticles`.
  - `w1-sqlite`: `driverNameMain`, `driverNameGroup`, `mainConnPragmas`, `groupConnPragmas`, `stateFAILED`, `stateCLOSED`, `errGroupDBClosed`, `errGroupDBInitFailed`, `(*GroupDB).acquire`, `(*Database).cleanupIdleGroupsWith`, `SQLiteMaxRetryWait`, `isRetryableSQLiteError`, `stripMigrationPragmas`, `retryLogThrottle`.
- **K3: tests.**
  - Only wave 0 creates `TestMain` (`internal/database/testmain_test.go`, `internal/web/testmain_test.go`).
  - Slice test files are named `w<wave>_<slice>_test.go`, and every package-level identifier in them is prefixed `w1Server…`, `w1Auth…`, `w1API…`, `w1SQLite…`, `w2Tmpl…`, `w2DBPerf…`.
  - Tests use the wave-0 helpers (`w0DB`, `w0Srv`, `w0Do`, `w0NewUser`, `w0NewGroup`, `w0Name`) and unique names made with `w0Name(prefix)`.
  - The DB and server are shared per package test binary, so tests must not mutate package globals that background goroutines read (that is a data race under `-race`). Where a test needs a different value, add an unexported parameterized function in an owned file. Globals a test does change (for example `config.UpdateBadBots`, config values) are restored with `t.Cleanup`.
  - Tests that need the API call `db.SetConfigValue(config.CFG_KEY_API_ENABLED, "true")`, which also updates the config cache.
- **K4:** Nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt`, or files outside their ownership list. `gofmt` runs only on owned files.
- **K5:** No slice may call a helper that another slice of the same wave adds. For example, `w1-api` uses `s.isAdmin(user)`, not `isAdminRequest`.

### Component design (details per slice below)
- **Request auth cache:** `getWebSession` memoizes its result in the gin context (`ctxKeyWebSessionChecked`/`ctxKeyWebSession`). `isAdminRequest(c)` memoizes the admin flag. The session slide writes only when less than half of `SessionTimeout` remains.
- **Group DB lifecycle:** states `0=init`, `1=CREATED`, `2=FAILED`, `3=CLOSED`. Workers are incremented only under `groupDB.mux` while the state is CREATED. Closers set CLOSED under the same mutex and require `Workers==0`. Lookup takes a `MainMutex.RLock` fast path, then an exclusive slow path that creates the entry. A CLOSED hit retries the lookup (at most 3 times), and FAILED returns an error to all waiters.
- **Per-connection pragmas:** two `mattn/go-sqlite3` drivers are registered in `init()` with a `ConnectHook` that runs the pragma list stored in an `atomic.Pointer[[]string]`.
- **Migrations:** each migration runs on a dedicated `*sql.Conn`: `PRAGMA foreign_keys=OFF`, then `BEGIN`, then the SQL (whole-line `PRAGMA …;` stripped), then the `INSERT schema_migrations` row, then `COMMIT`. After that the connection logs `PRAGMA foreign_key_check` and runs `PRAGMA foreign_keys=<configured>` before it is released.
- **CSRF:** `http.NewCrossOriginProtection().Handler(router)` wraps the whole router in `Start`, and `GET /logout` refuses a `Sec-Fetch-Site` of `cross-site` or `same-site`.
- **Caches (wave 2):** templates are parsed once into a `sync.Map` (env `PUGLEAF_DEV_TEMPLATES=1` disables the cache). Site news, header sections and active AI models use a 30s TTL plus explicit invalidation on writes.

---

## Waves

### Wave 0 (inline, orchestrator on `plan-web-sqlite-hardening`)
Owned files (all new):
- `internal/database/testmain_test.go`
- `internal/web/testmain_test.go`
- `scripts/test-web-hardening.sh`

Steps:
1. Run the preflight from the skill. Then run the baseline checks from `## Checks` on the untouched branch and record PASS/FAIL in the progress note.
2. Write `internal/database/testmain_test.go`:
   ```go
   package database

   import ("fmt"; "os"; "sync/atomic"; "testing")

   var w0TestDB *Database
   var w0Seq atomic.Int64

   func TestMain(m *testing.M) {
   	dir, err := os.MkdirTemp("", "pugleaf-dbtest-")
   	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(2) }
   	cfg := DefaultDBConfig()
   	cfg.DataDir = dir
   	db, err := OpenDatabase(cfg)
   	if err != nil { fmt.Fprintln(os.Stderr, "OpenDatabase:", err); os.Exit(2) }
   	w0TestDB = db
   	code := m.Run()
   	if code == 0 { _ = os.RemoveAll(dir) } else { fmt.Fprintln(os.Stderr, "test data kept in", dir) }
   	os.Exit(code)
   }

   func w0DB(t *testing.T) *Database { t.Helper(); if w0TestDB == nil { t.Fatal("test DB not open") }; return w0TestDB }
   func w0Name(prefix string) string { return fmt.Sprintf("%s.t%d", prefix, w0Seq.Add(1)) }
   func TestW0Harness(t *testing.T) {
   	if _, err := w0DB(t).GetConfigValue("registration_enabled"); err != nil { t.Fatal(err) }
   }
   ```
3. Write `internal/web/testmain_test.go`:
   - `TestMain`:
     - `os.Chdir("../..")` (templates load from `web/templates`); verify `web/templates/base.html` exists.
     - Create a temp data dir and call `database.OpenDatabase`.
     - Set `processor.LocalNNTPHostname = "test.invalid"`.
     - Create the user `w0root` first so that no test user gets ID 1, which is implicitly admin.
     - Build `w0Srv = NewWebServer(db, config.NewDefaultConfig().Server.WEB, nil, false, false)`. The default cron jobs are disabled by migration 0021, so starting the cron manager is safe.
   - Helpers:
     - `w0Do(t, w0Req{Method, Path string; Form url.Values; Header map[string]string; Cookies []*http.Cookie; RemoteAddr string}) *httptest.ResponseRecorder` serves through `w0Srv.Router`. The default RemoteAddr is `192.0.2.10:1234`; the form is sent as `application/x-www-form-urlencoded`.
     - `w0NewUser(t, admin bool) (*models.User, *http.Cookie)` uses `InsertUser` (bcrypt hash of `w0Password = "w0-password-0123456789"`), then `GetUserByUsername`, then `InsertUserPermission{Permission:"admin"}` when admin, then `CreateUserSession`, and returns a cookie named `session_id`.
     - `w0NewGroup(t, active bool) string` inserts with `database.RetryableExec(db.GetMainDB(), "INSERT INTO newsgroups(name, description, last_article, message_count, active) VALUES (?, '', 0, 0, ?)", name, active)`.
     - `w0Name(prefix)`.
   - `TestW0Harness` asserts that `GET /ping` returns 200.
4. Run `go test -race ./internal/database/ ./internal/web/ -run W0 -count=1`. If the harness fails because of a pre-existing data race in background goroutines, stop and ask the user. Don't paper over it.
5. Write `scripts/test-web-hardening.sh` from the **End-to-end** section, then `chmod +x`, then `bash -n`.
6. Run `./build_webserver.sh && PORT=18980 scripts/test-web-hardening.sh` on the baseline. Record the PASS/FAIL list and the `ab` numbers in the progress note; most E-checks are expected to FAIL here.
7. Commit on `plan-web-sqlite-hardening` with the message `test: wave-0 harness and e2e script for web-sqlite-hardening`. Record the base SHA for wave 1.

### Wave 1 (4 parallel `pugleaf-implementer` slices)

Common rules for every wave-1 slice:
- Follow contracts K1–K5.
- Read the findings table rows for your IDs and the owned files before editing.
- Put new tests in `w1_<slice>_test.go` in the listed packages.
- Checks (run in your worktree):
  ```bash
  gofmt -l internal/web internal/database internal/config cmd/web   # none of your owned files listed (see Checks for known baseline files)
  go vet ./...
  go build ./...
  go test -race -count=1 ./internal/database/... ./internal/web/...
  go test -race ./internal/history/... ./internal/nntp/... ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/...
  ./build_webserver.sh && PORT=<your port> scripts/test-web-hardening.sh | grep -E "\[(<your tag>)\]|SUMMARY"
  ```
  Checks tagged for other slices may still FAIL in your worktree; that is expected.
- Report the E-check lines with your tag.

---

#### Slice `w1-server`: server core, CSRF, proxies, cron nil, AI chat
Findings: C2, C3, C5 (both halves), C6, H1, H11, S1 (CSRF part), L2, L3, L7. E-tag `w1-server`, smoke port 18982.

Owned files:
- `internal/web/webserver_core_routes.go`
- `internal/web/webserver.go`
- `internal/web/cronjobs.go`
- `internal/web/web_admin_crons.go`
- `internal/web/web_session_cleanup.go`
- `internal/web/web_aichatPage.go`
- `web/templates/aichat.html`
- `internal/config/config.go` (only `UpdateBadBots`/`UpdateBadIPs` and their globals)
- `cmd/web/main.go` (only the startup/shutdown wiring)
- `internal/web/w1_server_test.go` (new)

Changes:
1. **Trusted proxies (H1).** In `NewWebServer`, replace lines 213-218 with `configureTrustedProxies(router, ReverseProxyAddr)`:
   - Split the config string on `,` and whitespace; when it is empty, use `DefaultReverseProxy`.
   - Call `router.SetTrustedProxies(list)` and log an error if it fails.
   - Set `router.ForwardedByClientIP = true` and `router.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}`.
   - Return the parsed `[]*net.IPNet` (a bare IP becomes /32 or /128) and store it in `s.trustedProxyNets`.

   Rewrite `ReverseProxyMiddleware`:
   - `peer := net.ParseIP(c.RemoteIP())`.
   - `isHTTPS := c.Request.TLS != nil || (s.isTrustedPeer(peer) && strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https"))`.
   - When HTTPS, set `c.Request.URL.Scheme = "https"`, then `c.Set("is_https", isHTTPS)`.
   - Do **not** modify `RemoteAddr` or `Host`.

   Delete `isPrivateOrLoopbackIP` if nothing uses it (grep first).
2. **Bot and IP middleware (C5, L3).**
   - Under `BadIPsMutex.RLock`, copy `BlockBadIPs` and the `Default_BlockedIPs` slice header, then unlock. Do the same for `BadBotsMutex` (`BlockBadBots`, `Default_BadBots`). Evaluate after unlocking, with `ua := strings.ToLower(c.GetHeader("User-Agent"))` computed once, and call `c.Next()` with no lock held.
   - In `config.go`, `UpdateBadBots` stores patterns `strings.ToLower(trimmed)` and builds a **new local slice**, assigning it once. Make sure `UpdateBadIPs` also assigns a new slice instead of mutating in place.
3. **Sections cache (C3, L7).**
   - Replace `SectionsCache map[string]bool` with an unexported `sectionsCache atomic.Pointer[map[string]bool]`. Grep showed no users outside this file; re-check.
   - `loadSectionsCache` builds a local map and calls `Store(&m)`. `isValidSection` does `p := s.sectionsCache.Load(); return p != nil && (*p)[name]`.
   - Hoist `knownPaths` into a package-level `knownNonSectionPaths`.
4. **Cron nil (C2).**
   - In `NewWebServer`: `if server.CronManager != nil { server.CronManager.StartCronManager() }`.
   - In `cronjobs.go`, add `if cm == nil { return }` or a zero value to `StartCronManager`, `StopCronManager`, `GetJobOutput` (returns nil) and `StopJob` (returns `errors.New("cron jobs are disabled (-no-cronjobs)")`).
   - In `web_admin_crons.go`, when `s.CronManager == nil`, show "Cron jobs are disabled on this instance" instead of dereferencing.
5. **HTTP server, shutdown and CSRF (C6, S1, L2).**
   - Add to `WebServer`: `httpServerMu sync.Mutex`, `httpServer *http.Server`, `stopCh chan struct{}` (made in `NewWebServer`).
   - `newHTTPServer(addr string, h http.Handler) *http.Server` sets `ReadHeaderTimeout: 10s`, `ReadTimeout: 60s`, `WriteTimeout: 120s`, `IdleTimeout: 120s`, `MaxHeaderBytes: 1<<20`.
   - `Start()`:
     - Build `cop := http.NewCrossOriginProtection()` and `srv := newHTTPServer(addr, cop.Handler(s.Router))`, and store it under the mutex.
     - Serve with `srv.ListenAndServeTLS(cert, key)` or `srv.ListenAndServe()`.
   - `Shutdown(ctx)`:
     - Close `stopCh` once (`sync.Once`).
     - Take the server under the mutex; when it isn't nil, return `srv.Shutdown(ctx)`.
   - `StartSessionCleanup` loops with `select { case <-s.stopCh: return; case <-time.After(15*time.Minute): ... }`.
   - In `cmd/web/main.go`:
     - After `NewWebServer`, call `server.StartSessionCleanup()`.
     - After the shutdown `select`, before stopping NNTP, run `ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second); if err := server.Shutdown(ctx); err != nil { log.Printf("[WEB]: web server shutdown: %v", err) }; cancel()`.
     - The existing `err != http.ErrServerClosed` check stays.
6. **AI chat (C5 second half, H11).**
   - `chatHTTPClient = &http.Client{Timeout: 90 * time.Second}`. Use `http.NewRequestWithContext(c.Request.Context(), POST, ollamaProxyURL, body)` and check `resp.StatusCode == 200`. Decode through `io.LimitReader(resp.Body, 1<<20)`.
   - History key: `chatHistoryKey(session.UserID, modelPostKey)` = `strconv.FormatInt(uid,10)+"_"+model`. Ignore the client `sessionToken` everywhere (send, history, clear, counts) and no longer require it. Remove `SessionToken` from `AIChatPageData`.
   - In `aichat.html`, remove `const sessionToken = …` and every `sessionToken` in request bodies and URLs (5 fetch sites).
   - Caches: entries become `chatEntry{msgs []ChatMessage; lastUsed time.Time}`. `sweepChatCaches` removes entries idle for more than 2h and rate-limiter entries older than 10×`chatCooldown`, and caps each map at 10000 entries by evicting the oldest. Run it from a goroutine started in `NewWebServer` that stops on `s.stopCh`.
   - Rate limit: do the check and the set under one `Lock`.
   - Remove the `[:8]` slice and don't log message or reply contents; log lengths and the model only.
7. Tests in `internal/web/w1_server_test.go`:
   - `TestW1ServerBotMiddlewareReleasesLock`: a gin engine with the middleware and a handler that calls `config.UpdateBadBots("x", false)`. It must finish within 2s (at baseline it deadlocks).
   - `TestW1ServerSectionsCacheRace`: 50 goroutines call `isValidSection` while `loadSectionsCache` runs 200× (`-race`).
   - `TestW1ServerTrustedProxies`, as a table on an engine built with `configureTrustedProxies(r, "")`:
     - peer `127.0.0.1:1` with XFF `203.0.113.9, 198.51.100.7` gives `c.ClientIP()=="198.51.100.7"`.
     - peer `127.0.0.1:1` with X-Real-IP `not-an-ip` gives `127.0.0.1`.
     - peer `203.0.113.50:1` with XFF `1.2.3.4` gives `203.0.113.50`.
     - `is_https` is true only for a trusted peer that sends `X-Forwarded-Proto: https`.
   - `TestW1ServerCrossOrigin`: the Start handler chain (expose the handler via a small unexported helper if needed) returns 403 for POST with `Sec-Fetch-Site: cross-site`, and not 403 for same-origin.
   - `TestW1ServerNilCron`: a nil `*CronJobManager` doesn't panic in any of the 4 methods.
   - `TestW1ServerChatNoSessionIDInPage`: seed an active AI model, then `GET /aichat` with a `w0NewUser` cookie; the body contains no cookie value.
   - `TestW1ServerHTTPServerTimeouts`: check the fields.

Acceptance: E11, E12, E13, E14, E18, E19, E20, E23 PASS.

---

#### Slice `w1-auth`: login, register, sessions, per-request auth cache, profile validation
Findings: H2, H3, H4, H5, H12, P2 (session and admin part), S1 (logout), H10 (display-name input). E-tag `w1-auth`, smoke port 18983.

Owned files:
- `internal/web/web_auth.go`
- `internal/web/web_login.go`
- `internal/web/web_registerPage.go`
- `internal/web/web_profile.go`
- `internal/web/web_utils.go`
- `internal/web/web_helpers.go`
- `internal/web/web_admin_userfuncs.go`
- `internal/database/db_sessions.go`
- `internal/web/w1_auth_test.go` (new)
- `internal/database/w1_auth_test.go` (new)

Changes:
1. **`safeRedirect(u string) string`** (web_auth.go):
   - Return `"/"` for any of: empty, longer than 2048, containing `\r`, `\n` or `\\`, not starting with `/`, starting with `//`.
   - Return `"/"` when `url.Parse` fails or yields a Scheme or Host.
   - Otherwise return `u`.

   Use it:
   - `loginPage`: for the `RedirectURL` data and the "already logged in" redirect.
   - `loginSubmit`: on the POST value.
   - `WebAuthRequired`/`WebAdminRequired`: build `"/login?redirect="+url.QueryEscape(c.Request.URL.RequestURI())`.
2. **Login (H3, H12).** Add `var loginDelay = 2 * time.Second` (tests set it to 0) and replace `time.Sleep(2*time.Second)` with it. Order of operations:
   1. Resolve the user by email (`@`) or username.
   2. When not found, run `bcrypt.CompareHashAndPassword(dummyPasswordHash, pw)` (the hash is generated once with `sync.Once`) and give the generic `"Invalid username/email or password"`.
   3. `IsUserLockedOutByID(user.ID)`: a DB error gives "Login error. Please try again."; locked gives the generic message.
   4. On a wrong password, call `IncrementLoginAttemptsByID(user.ID)` and give the generic message.
   5. `user.Disabled > 0` gives the generic message and a log line.
   6. `CreateUserSession`, then `setSessionCookie`, then `s.clearRequestSession(c)`, then redirect.

   Log with `[WEB]` and hex usernames as today.
3. **DB (db_sessions.go).**
   - `IsUserLockedOutByID(id)` selects `login_attempts, updated_at` by id, with the same window logic. Reset by id when expired.
   - `IncrementLoginAttemptsByID(id)` runs `UPDATE users SET login_attempts = login_attempts + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`.
   - Keep the old username functions (K1 style); nothing new calls them.
   - `ValidateUserSession`:
     - Add `AND disabled = 0` to the WHERE clause.
     - Slide only when `time.Until(*user.SessionExpiresAt) < SessionTimeout/2`.
     - The UPDATE sets only `session_expires_at = ?`, not `updated_at`, so the lockout window isn't disturbed.
4. **Register (H4, H5).**
   - After `createUser`: `sessionID, err := s.DB.CreateUserSession(user.ID, c.ClientIP())`, then `setSessionCookie`. Delete `createWebSession`, checking with grep that nothing else calls it.
   - `validatePassword` rejects `len(password) > 72` with `"password must be at most 72 bytes"`.
   - `renderRegisterError` no longer shows `err.Error()` from `createUser`; use a generic "Failed to create user" and log the detail.
5. **Per-request cache (P2).**
   - `getWebSession`: when `c.Get(ctxKeyWebSessionChecked)` is true, return the stored `*SessionData` or nil. Otherwise compute, then store both keys.
   - `clearRequestSession(c)` resets them (used after login and logout).
   - `isAdminRequest(c) bool`: memoized in `ctxKeyIsAdmin`. It uses `getWebSession`, then `GetUserByID`, then `isAdmin`.

   Use `isAdminRequest` in `getBaseTemplateData`, `requireAdminAuth`, `requireAdminAuthJSON`, `checkGroupAccess` and `checkGroupAccessAPI`. Signatures stay the same (K1).
6. **Logout (S1).** When `Sec-Fetch-Site` is `cross-site` or `same-site`, redirect to `/` without invalidating. Otherwise behave as today and call `clearRequestSession`.
7. **Display name (H10 input).**
   - `validateDisplayName(s) error`: at most 64 runes; no `unicode.IsControl` runes; none of `<`, `>`, `"`. Empty is allowed.
   - Apply it in `profileUpdate` and in `adminCreateUser`/`adminUpdateUser` where a display name is set. Apply `validatePassword` in the admin create and update paths that set web passwords, if they don't already.
8. **Flash messages.** Add `flashSetAt map[string]time.Time`. On Set, when `len(flashMessages) > 1000`, delete entries older than 15 minutes.
9. Tests:
   - web:
     - `safeRedirect` table: `/x`→`/x`; `//evil`, `/\evil`, `https://evil`, `javascript:x`, CRLF → `/`.
     - `validatePassword` at 72 and 73 bytes.
     - `validateDisplayName`.
     - Register flow via `w0Do`: POST `/register`, then take the cookie, then `GET /profile` returns 200.
     - Login by email returns 303 plus a cookie.
     - The unknown-user and wrong-password bodies contain the same message.
     - Lockout after 5 failures by id.
     - A disabled user can't log in, and the session of a user disabled after login is rejected.
     - Logout with `Sec-Fetch-Site: cross-site` keeps the session.
     - An open redirect on login lands on `/`.
   - database:
     - `ValidateUserSession` called twice leaves `session_expires_at` unchanged the second time.
     - `IncrementLoginAttemptsByID` and `IsUserLockedOutByID`.

Acceptance: E01, E02, E03, E04, E05, E15, E16, E17 PASS.

---

#### Slice `w1-api`: API tokens, group access before GetGroupDB, posting, spam, pagination
Findings: C1, C4, H10 (posting output), P3 (token part), P5, S2 (tree HTML), S3, S4, L6. E-tag `w1-api`, smoke port 18984.

Owned files:
- `internal/web/web_apitokens.go`
- `internal/database/db_apitokens.go`
- `internal/web/web_apiHandlers.go`
- `internal/web/web_threadTreePage.go`
- `internal/database/tree_view_api.go`
- `internal/web/web_sitePostPage.go`
- `internal/web/webgroupPage.go`
- `internal/web/web_sectionsPage.go`
- `internal/database/db_spam_flags.go` (new)
- `internal/database/db_web_posting.go` (new)
- `internal/database/db_threads_paged.go` (new)
- `internal/web/w1_api_test.go` (new)
- `internal/database/w1_api_test.go` (new)

Changes:
1. **Tokens (C1, P3).**
   - Add `var ErrAPITokenExpired = errors.New("api token expired")`; `ValidateAPIToken` returns it for expired tokens.
   - Remove every `MainMutex` lock and unlock in `db_apitokens.go`.
   - In `APIAuthRequired`, treat `err != nil || apiToken == nil` as 401, and copy `tokenID := apiToken.ID` before the goroutine.
2. **Tree API (C4, S2).** In `handleThreadTreeAPI`:
   - Put `if !s.checkGroupAccessAPI(c, groupName) { return }` before `GetGroupDB`.
   - Clamp `max_depth` to 0..50.
   - Error JSON is generic (`"Failed to build thread tree"`); log the detail.

   In `tree_view_api.go`, apply `html.EscapeString` to `groupName` in the `data-*` attributes, and `html.EscapeString(url.PathEscape(groupName))` in the hrefs.
3. **Section tree (C4, S4).**
   - Extract `(*WebServer).sectionGroupAllowed(c, sectionName, groupName) (*models.Section, bool)` from the existing section + membership code (it renders the same 404s), and use it in `sectionGroupPage`, `sectionArticlePage` and `sectionArticleByMessageIdPage` with identical behaviour.
   - `sectionThreadTreePage` uses `groupName := c.Param("group")` plus `sectionGroupAllowed` before `GetGroupDB`, and keeps the `SectionsCache.IsInSections` check.
4. **SitePost (C4, H10, S3).**
   - Prefill: only call `GetGroupDB(prefilledNewsgroup)` when `processor.IsValidGroupName(prefilledNewsgroup)` is true and `GetActiveNewsgroupByName` succeeds.
   - Submit, `validatePostHeaders(subject, messageID string, isReply bool) error`:
     - Subject: no `\r`, `\n` or `\x00`.
     - Reply: `postMessageIDRe = ^<[^<>\s@]+@[^<>\s@]+>$`, at most 250 bytes. Error text: `"Invalid reply message-id"` and `"Subject contains invalid characters"`.
     - Body: no `\x00`.
     - Reject `abuseMail` or `processor.LocalNNTPHostname` containing CR/LF as a server config error.
     - Run `validatePostHeaders` **unconditionally**, right after reading the form and before the config and required-field block. Append its error to `errors` so the message shows up even when other errors exist. E25 and E26 grep for it.
   - `sanitizePostDisplayName(s)`: drop control runes and `<>"`, collapse whitespace, trim, cap at 64 runes. When empty, use the existing fallback. Otherwise `From = name + " <noreply@pugleaf.net.invalid>"`.
   - Replace `UpdateUserPostCount` with `ok, err := s.DB.TryReserveWebPost(user.ID, now, int64(WebPostingBackOff.Seconds()))`; when not ok, use the back-off error.
   - `db_web_posting.go`: `UPDATE users SET post_count = post_count + 1, lastpost_unix = ? WHERE id = ? AND COALESCE(lastpost_unix, 0) <= ?` with args `(now, id, now-backoff)`, and `RowsAffected()==1`.
5. **Spam (S3).** `db_spam_flags.go`, `FlagArticleSpamByUser(userID, group, articleNum) (bool, error)`:
   1. `GetNewsgroupID`.
   2. `GetGroupDB` + `defer Return`.
   3. Check the article exists (`SELECT 1 FROM articles WHERE article_num=?`, `ErrArticleNotFound`).
   4. `INSERT OR IGNORE INTO user_spam_flags`; 0 rows affected returns `(false, nil)`.
   5. `IncrementArticleSpam`; on error, delete the flag row and return the error.

   In `incrementSpam`:
   - Non-admins (`s.isAdmin(user)`) need `GetActiveNewsgroupByName(group)`; otherwise redirect with an error flash.
   - Replace the Has/Increment/Record sequence with `FlagArticleSpamByUser`.
   - Keep the redirect logic.
6. **Pagination (P5, L6).**
   - `clampOffsetPage(page, pageSize, maxOffset int) int`, with `maxOffsetArticles = 12800`.
   - Use it in `groupPage`, `sectionGroupPage` (clamp) and `getGroupOverview` (clamp; set response header `X-Page-Clamped: 1` when clamped).
   - `getGroupThreads`: `limit` (default 500, clamp 1..2000) and `offset` (0..1_000_000) go to `GetThreadsPaged(groupDB, limit, offset)`, which runs `SELECT … FROM threads ORDER BY id LIMIT ? OFFSET ?` with the same scan as `GetThreads`. Set `X-Next-Offset` when `len==limit`.
   - `getStats`: an `apiStatsCache` (mutex + time + `gin.H`) with a 60s TTL.
   - `getArticlePreview`: use `truncateRunes(s, 500)`.
7. Tests:
   - web:
     - An expired token gives 401 and the binary doesn't crash.
     - A valid token gives 200.
     - `/api/thread-tree` on an unknown group gives 404 and `filepath.Glob(<datadir>/db/*/<sanitized>.db)` is empty.
     - An inactive group gives 404 for a non-admin.
     - The section tree for a group not in the section gives 404 and no file is created.
     - `validatePostHeaders` table; `sanitizePostDisplayName` table.
     - `GetThreadTreeHTML` with the group `a"b<c` escapes the name.
     - `clampOffsetPage`, `truncateRunes`.
   - database:
     - `FlagArticleSpamByUser`: 10 concurrent calls by the same user leave the spam count at 1.
     - `TryReserveWebPost`: 10 concurrent calls give exactly 1 true.
     - `GetThreadsPaged` limit and offset.

Acceptance: E06, E07, E08, E09, E10, E25, E26 PASS.

---

#### Slice `w1-sqlite`: group DB lifecycle, per-connection pragmas, atomic migrations, retry
Findings: H6, H7, H8, H9, P3 (GetGroupDB and aimodels part), P4, L4, L5. E-tag `w1-sqlite`, smoke port 18985.

Owned files:
- `internal/database/db_groupdbs.go`
- `internal/database/database.go`
- `internal/database/db_init.go`
- `internal/database/db_migrate.go`
- `internal/database/sqlite_retry.go`
- `internal/database/db_aimodels.go`
- `internal/database/migrations/0001_single_db_schema.sql` (pragma lines only)
- `internal/database/w1_sqlite_test.go` (new)

Changes:
1. **Drivers (H8).**
   - `const driverNameMain = "sqlite3_pugleaf_main"` and `driverNameGroup = "sqlite3_pugleaf_group"`, plus `mainConnPragmas` and `groupConnPragmas` of type `atomic.Pointer[[]string]`.
   - In `init()`, `sql.Register` each name with `&sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error { for _, p := range *list { if _, err := conn.Exec(p, nil); err != nil { return fmt.Errorf("pragma %q: %w", p, err) } }; return nil }}`. When the list pointer is nil, run nothing.
   - Pragma order: `busy_timeout` first, then `foreign_keys`, `synchronous`, `cache_size`, `temp_store`, (main only: `mmap_size = 0`), `journal_mode = WAL`, `wal_autocheckpoint` (WAL only when `dbconfig.WALMode`).
   - `initMainDB` stores the main list, then calls `sql.Open(driverNameMain, …)`.
   - The group list keeps the existing lazy builder (`PragmasGroupDB` from the `SQLITE_*` vars and WALMode), stored before the first group open.
   - `GetGroupDB` and `GetGroupDBWithSuffix` use `driverNameGroup`, always get pragmas (remove the `if dbExists` skip), and set `SetMaxIdleConns(2)` and `SetConnMaxIdleTime(5*time.Minute)`.
   - Leave `progress.go`, `internal/nntp`, `cmd/*` on the plain `sqlite3` driver.
   - Remove the now-redundant pool-level pragma Exec calls, keeping the list builders.
   - Note: `foreign_keys` is now really ON on all connections. `db_nntp_users.go` already inserts NULL for `web_user_id=0`. Grep the other FK tables (`user_permissions`, `user_spam_flags`, `nntp_sessions`, `section_groups`, `spam`) for inserts that could violate a constraint and report them. Don't fix them outside your files.
2. **Lifecycle (H6, H7, P3, L5).**
   - Add `stateFAILED = 2` and `stateCLOSED = 3`, plus `errGroupDBClosed` and `errGroupDBInitFailed`.
   - `(*GroupDB).acquire(deadline time.Time) error` loops:
     - `mux.Lock`; switch on the state:
       - CREATED with `DB != nil`: `Workers++`, `Idle=now`, unlock, return nil.
       - FAILED: return `errGroupDBInitFailed`.
       - CLOSED: return `errGroupDBClosed`.
       - init: unlock, sleep 10ms.
     - Past the deadline (60s), return an error.
   - `GetGroupDB`:
     - Fast path: `MainMutex.RLock` read, then `acquire`. A closed DB retries the lookup, at most 3 attempts in total.
     - Slow path: under `MainMutex.Lock`, re-check. When the entry is missing, insert a `GroupDB{Workers:1, state:0}` and unlock. Then init: create the dir, `sql.Open`, pragmas via the hook, `migrateGroupDB`.
     - On any init error, lock `mux`, set FAILED, close any opened DB, and unlock. Then, under `MainMutex`, delete the entry only if `db.groupDB[name] == g`, and return the wrapped error.
     - On success, run `openDBsNum++` under `MainMutex` and set CREATED under `mux`.
   - Delete `removePartialInitializedGroupDB` (grep: only this file uses it).
   - `cleanupIdleGroups`:
     - Collect the candidates under `RLock` and sort them with `sort.Slice`.
     - Under `MainMutex.Lock` and each `mux.Lock`, for `Workers==0` (and idle time on the normal path): set CLOSED, `delete` the map entry, `openDBsNum--`, and append the `*sql.DB` to a close list.
     - Unlock, **then** close the DBs outside the locks.
   - `ForceCloseGroupDB`: keep its semantics (decrement, close at 0); also set CLOSED and close after unlocking.
   - `Shutdown`: set CLOSED and close.
   - `Return()`: `if dbs == nil { log…; return }`, then decrement under `mux` whether or not `DB` is nil.
3. **Migrations (H9).**
   - `stripMigrationPragmas(sql string) string` removes whole lines matching `(?im)^\s*PRAGMA\s+[^;]*;\s*$`.
   - `applyMigration(db *sql.DB, …)`:
     - `ctx := context.Background()`; `conn, err := db.Conn(ctx)`; `defer conn.Close()`.
     - `conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")`.
     - `tx, err := conn.BeginTx(ctx, nil)`; `tx.ExecContext(ctx, stripMigrationPragmas(content))`; insert the `schema_migrations` row; `Commit`. Roll back on every error.
     - After the commit, run `PRAGMA foreign_key_check` through `conn.QueryContext` and log any rows with `[DATABASE]`.
     - Finally `conn.ExecContext(ctx, "PRAGMA foreign_keys="+SQLITE_foreign_keys)` (main uses ON), deferred so it also runs on the error path.
   - In `0001_single_db_schema.sql`, delete the 5 PRAGMA lines (8, 10-13). Already-applied DBs aren't affected because migrations are tracked by filename.
4. **Retry (P4).**
   - `var SQLiteMaxRetryWait = 5 * time.Minute`.
   - `isRetryableSQLiteError(err)`: `var se sqlite3.Error; errors.As(err,&se) && (se.Code==sqlite3.ErrBusy || se.Code==sqlite3.ErrLocked)`, else a substring match on `"database is locked"` or `"database table is locked"` only. `isRetryableError` delegates to it.
   - Every `Retryable*` loop also stops when `time.Since(start) > SQLiteMaxRetryWait`.
   - Log on attempts 1, 10, 100 and 1000 plus the final give-up (`retryLogThrottle`).
   - Fix the shadowed `err` in `RetryableTransactionExec` (`tx, beginErr := db.Begin()`, assign the outer `err`).
5. **AI models and background loops (P3, L4).**
   - Remove all `MainMutex` locks from `db_aimodels.go`.
   - `CronDB()` and the stats logger in `OpenDatabase` `select` on `db.StopChan`.
6. Tests in `internal/database/w1_sqlite_test.go`:
   - `ConnectHookAllConns`: a temp file, open with `driverNameMain` after storing the list, `SetMaxOpenConns(8)`. Hold 8 `db.Conn`s at once; each reports `PRAGMA foreign_keys`=1 and `busy_timeout`=30000.
   - `NewGroupDBIsWAL`: `GetGroupDB(w0Name("w1sqlite.wal"))`, then `PRAGMA journal_mode`=`wal` and `synchronous`=1.
   - `ConcurrentAcquireAndCleanup`:
     - 16 goroutines × 200 iterations of `GetGroupDB` over 8 names, then `SELECT 1`, then `Return`.
     - Meanwhile a goroutine calls `db.cleanupIdleGroupsWith(0)` in a loop. Split `cleanupIdleGroups()` into a wrapper that calls the new unexported `cleanupIdleGroupsWith(idle time.Duration)` with `DBidleTimeOut`; don't mutate the global, which `CronDB` reads.
     - No `sql: database is closed` errors, under `-race`.
   - `InitFailureWaitersReturn`: pick a name, create a **file** at `<DataDir>/db/<GroupToHash(name)>` so directory creation fails, start 8 concurrent `GetGroupDB` calls, and require all of them to return an error within 5s.
   - `MigrationAtomic`: a fresh temp DB, `ensureMigrationsTable`, and a temp-file migration `CREATE TABLE w1a(x); CREATE TABLE w1a(x);` that fails. Afterwards, table `w1a` is absent and there is no `schema_migrations` row.
   - `FreshInstallAllMigrations`: all embedded main migrations apply to an empty DB through `migrateMainDB`-equivalent code, and group migrations apply to an empty group DB.
   - `RetryMatcher`: `sqlite3.Error{Code: sqlite3.ErrBusy}` is retryable; `errors.New("UNIQUE constraint failed: users.locked")` is not.

Acceptance: E27 PASS. All listed tests pass with `-race`, and the existing `go test` packages from `## Checks` stay green (the batch writer uses the retry helpers).

---

### Wave 2 (2 parallel slices, based on the merged wave 1)

#### Slice `w2-templates`: template cache and render helper
Findings: P1, S2 (Title type). Smoke port 18986.

Owned files:
- `internal/web/web_templates.go` (new)
- every handler file that parses templates: `web_groupsPage.go`, `web_newsPage.go`, `web_utils.go`, `web_searchPage.go`, `web_login.go`, `web_hierarchiesPage.go`, `web_profile.go`, `web_ircPage.go`, `web_helpPage.go`, `webgroupPage.go`, `web_sectionsPage.go`, `web_statsPage.go`, `web_groupThreadsPage.go`, `web_threadPage.go`, `web_homePage.go`, `web_articlePage.go`, `web_threadTreePage.go`, `web_adminPage.go`, `web_aichatPage.go`, `web_registerPage.go`, `web_sitePostPage.go`
- `internal/web/webserver_core_routes.go` (only the `TemplateData.Title` type)
- `internal/web/w2_templates_test.go` (new)

Changes:
1. Add `web_templates.go`:
   ```go
   var tmplCache sync.Map // key: name + "\x00" + strings.Join(files, "\x00")
   var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

   func devTemplates() bool { return os.Getenv("PUGLEAF_DEV_TEMPLATES") == "1" }

   func loadTemplates(name string, funcs template.FuncMap, files ...string) (*template.Template, error)
   // (*WebServer).renderPage(c, status, data, files...) executes "base.html" into a pooled
   // buffer, then c.Data(status, "text/html; charset=utf-8", buf.Bytes()).
   // On a load or exec error it logs and calls s.renderError (guard against recursion:
   // when the failing template set is the error page, fall back to c.String).
   ```
2. Replace all 37 `template.Must(template.ParseFiles(...))` + `ExecuteTemplate` sites with `s.renderPage` (or `loadTemplates` + a buffer for the admin FuncMap at `web_adminPage.go:583` and `base_chat.html` in aichat).
   - Keep the status codes: `renderLoginError` and `renderRegisterError` 400, `renderError` its code, others 200.
   - Remove the `c.Status(...)` calls before execution.
3. Change `TemplateData.Title` from `template.HTML` to `string`, update the assignments (`web_utils.go:91`, `web_searchPage.go:74,113`, the thread-tree gin.H), and grep for other `template.HTML(` title uses.
4. Tests:
   - `loadTemplates` returns the same pointer on the second call, and a new one with the env var set.
   - A missing template returns an error without panicking.
   - `GET /login`, `/register`, `/groups` and `/search?q=a` each return 200 with a `<title>` containing `go-pugleaf`.
   - A search query `<script>` is escaped in `<title>`.
   - Render 50 pages concurrently under `-race`.

Acceptance: E24 is reported as INFO with requests/s higher than the wave-0 baseline, all earlier E-checks still PASS, and checks are green.

#### Slice `w2-dbperf`: cached base data, search and indexes
Findings: P2 (base data), P6, P8. E-tag `w2-dbperf`, smoke port 18987.

Owned files:
- `internal/database/ui_cache.go` (new)
- `internal/database/queries.go` (functions `GetVisibleSiteNews`, `GetHeaderSections`, `CreateSiteNews`, `UpdateSiteNews`, `DeleteSiteNews`, `ToggleSiteNewsVisibility`, `InsertSection`, `SearchNewsgroupsWithOptions`, `CountSearchNewsgroupsWithOptions` and their query constants)
- `internal/database/db_sections.go`
- `internal/database/db_aimodels.go`
- `internal/database/migrations/0027_main_search_index_and_drop_redundant.sql` (new)
- `internal/database/migrations/0009_single_drop_redundant_indexes.sql` (new)
- `internal/database/w2_dbperf_test.go` (new)

Changes:
1. Add `ui_cache.go`: a generic `ttlCache[T]{mu sync.RWMutex; val T; at time.Time; ok bool}` with `get(ttl, load func() (T, error))` and `invalidate()`. Instantiate package-level caches for visible site news, header sections and active AI models (TTL 30s).
   - The cached `GetVisibleSiteNews`, `GetHeaderSections` and `GetActiveAIModels` return a **new slice** of the cached pointers.
   - Grep `internal/web` for mutation of `SiteNews`, `Section` and `AIModel` values returned by these three calls. If any exists, deep-copy instead.
   - Call invalidate in every write function listed above plus `CreateSection`, `UpdateSection`, `DeleteSection`, `CreateSectionGroup`, `DeleteSectionGroup` (header sections), `CreateAIModel`, `UpdateAIModel`, `SetDefaultAIModel` and `DeleteAIModel`.
2. Search:
   - `escapeLike(s)` escapes `\`, `%` and `_`; use `name LIKE ? ESCAPE '\'`.
   - Migration 0027 adds `CREATE INDEX IF NOT EXISTS idx_newsgroups_name_nocase ON newsgroups(name COLLATE NOCASE);` and `DROP INDEX IF EXISTS idx_name;`.
   - Adjust the name-only queries so that `EXPLAIN QUERY PLAN` shows `USING INDEX idx_newsgroups_name_nocase` (for example `WHERE name LIKE ? ESCAPE '\'`, with no `COLLATE` on the expression). If SQLite still refuses the index, use a range scan on the NOCASE index (`name >= ? COLLATE NOCASE AND name < ? COLLATE NOCASE`, where the upper bound is the prefix with its last byte incremented) and document the choice in the report.
   - Keep the description variants functionally the same.
3. Migration `0009_single_drop_redundant_indexes.sql`: `DROP INDEX IF EXISTS idx_articles_message_id; DROP INDEX IF EXISTS idx_articles_hide; DROP INDEX IF EXISTS idx_articles_spam;`.
   - Before dropping, run `EXPLAIN QUERY PLAN` on a migrated group DB for these queries: `query_GetOverviewsPaginated1/2/3`, the spam queries in `GetSpamArticles` and `database.go`, the `thread_cache.go:290` child query, and the `db_batch.go` message-id lookups. Include the before/after plans in the report.
   - Drop only the indexes whose removal doesn't change a plan to a full SCAN.
   - Don't touch `idx_articles_article_num_spam`.
4. Tests:
   - The cache returns the same data within the TTL, and a write invalidates it (site news create, then visible news contains the item immediately).
   - `escapeLike` table.
   - Searching `%` matches only groups starting with a literal `%`.
   - `EXPLAIN QUERY PLAN` for the name search contains `idx_newsgroups_name_nocase` (or the documented range-scan index).
   - Fresh group DB migrations apply, and `sqlite_master` lacks the dropped indexes.

Acceptance: E21 PASS, and checks are green.

---

## Checks
On every merged tree (after wave 0, after each merge in waves 1 and 2):
```bash
gofmt -l ./cmd ./internal            # may list only the baseline files noted below
go vet ./...
go build ./...
go test -race -count=1 ./internal/database/... ./internal/web/... \
  ./internal/history/... ./internal/nntp/... ./internal/processor/... \
  ./cmd/expire-news/... ./cmd/history-rebuild/...
./build_webserver.sh
```
Baseline gofmt at 08c29f5 lists `internal/database/db_groupdbs.go` and `internal/database/db_init.go`, which `w1-sqlite` owns and formats. It also lists `internal/database/embedded_migrations.go` and `internal/web/web_admin_provider.go`, which no slice owns: leave them unformatted and treat them as known.

Merge order in wave 1: `w1-sqlite`, `w1-server`, `w1-auth`, `w1-api`, running the checks after each merge. The file sets are disjoint, so textual conflicts aren't expected. Semantic interplay to watch:
- `w1-auth`'s cached `checkGroupAccess` is used by `w1-api`'s new call sites.
- `CrossOriginProtection` versus the admin POST tests.

## End-to-end

Runner: `pugleaf-verifier` on the main checkout (`plan-web-sqlite-hardening`).
```bash
./build_webserver.sh
PORT=18981 DATA=./data-test-web-sqlite-hardening scripts/test-web-hardening.sh
```
Expected: `SUMMARY … fail=0`, INFO lines for E22 and E24, and no `DATA RACE` in `./data-test-web-sqlite-hardening/webserver.log` that mentions `internal/web` or `internal/database`. Any other race is listed as a leftover.

### `scripts/test-web-hardening.sh` (wave 0 writes this; fix only real script bugs later)
```bash
#!/usr/bin/env bash
# End-to-end checks for plan web-sqlite-hardening. Uses scratch data only.
# Usage: scripts/test-web-hardening.sh [--build]
# Env: PORT (18980), DATA (./data-test-web-sqlite-hardening), BIN (./build/webserver)
set -u
cd "$(git rev-parse --show-toplevel)" || exit 2
PORT="${PORT:-18980}"
DATA="${DATA:-./data-test-web-sqlite-hardening}"
BIN="${BIN:-./build/webserver}"
BASE="http://127.0.0.1:${PORT}"
case "$DATA" in ./data-test-*) ;; *) echo "refusing DATA=$DATA (must start with ./data-test-)"; exit 2 ;; esac
for t in curl sqlite3 sha256sum timeout; do command -v "$t" >/dev/null || { echo "missing tool: $t"; exit 2; }; done
# cmd/web renames ./.update and shuts down when it exists (monitorUpdateFile): never touch it
[ -e ./.update ] && { echo "refusing to run: ./.update exists in $(pwd)"; exit 2; }
if [ "${1:-}" = "--build" ]; then ./build_webserver.sh || exit 2; fi
[ -x "$BIN" ] || { echo "missing $BIN (run with --build)"; exit 2; }

rm -rf "$DATA"; mkdir -p "$DATA"
LOG="$DATA/webserver.log"; DB="$DATA/cfg/pugleaf.sq3"
JAR_REG="$DATA/jar_reg"; JAR_ADMIN="$DATA/jar_admin"; JAR_USER="$DATA/jar_user"
PW='smoke-password-0123456789'; PASSES=0; FAILS=0; PID=""

pass() { PASSES=$((PASSES+1)); echo "PASS $1 [$2] $3"; }
fail() { FAILS=$((FAILS+1)); echo "FAIL $1 [$2] $3 :: ${4:-}"; }
info() { echo "INFO $1 [$2] $3"; }
q() { sqlite3 -cmd '.timeout 5000' "$DB" "$1"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
location() { curl -s -o /dev/null -D - "$@" | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}'; }
alive() { [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; }
start_server() {
  "$BIN" -data "$DATA" -webport "$PORT" -nntphostname smoke.invalid "$@" >>"$LOG" 2>&1 &
  PID=$!
  for _ in $(seq 1 90); do
    curl -fs "$BASE/ping" >/dev/null 2>&1 && return 0
    alive || return 1
    sleep 1
  done
  return 1
}
stop_server() {
  [ -n "$PID" ] || return 0
  kill -INT "$PID" 2>/dev/null
  for _ in $(seq 1 90); do alive || { PID=""; return 0; }; sleep 1; done
  kill -KILL "$PID" 2>/dev/null; PID=""
}
ensure_up() { alive || { PID=""; start_server || { echo "restart failed"; tail -40 "$LOG"; exit 2; }; }; }
trap stop_server EXIT

# first start creates the schema; then seed while stopped (config cache is 5 min)
start_server || { echo "server failed to start"; tail -60 "$LOG"; exit 2; }
stop_server
TOK_EXP=$(printf %s smoke-expired-token | sha256sum | cut -d' ' -f1)
TOK_OK=$(printf %s smoke-valid-token | sha256sum | cut -d' ' -f1)
q "INSERT OR REPLACE INTO config(key,value) VALUES('APIEnabled','true'),('AbuseMail','abuse@smoke.invalid'),('registration_enabled','true');
INSERT OR IGNORE INTO newsgroups(name,description,last_article,message_count,active) VALUES('smoke.test','',0,0,1),('smoke.inactive','',0,0,0);
INSERT OR IGNORE INTO sections(name,display_name) VALUES('smokesec','Smoke');
INSERT OR IGNORE INTO ai_models(post_key,ollama_model_name,display_name,description,is_active,is_default,sort_order) VALUES('smoke','smoke','Smoke','',1,1,0);
INSERT OR IGNORE INTO api_tokens(apitoken,ownername,ownerid,expires_at,is_enabled) VALUES('$TOK_EXP','smoke',0,'2000-01-01 00:00:00',1),('$TOK_OK','smoke',0,NULL,1);"
start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }

# E01 register logs in (user id 1 = smokeadmin = admin)
curl -s -o /dev/null -c "$JAR_REG" -b "$JAR_REG" -X POST --data-urlencode username=smokeadmin \
  --data-urlencode email=smokeadmin@smoke.invalid --data-urlencode "password1=$PW" --data-urlencode "password2=$PW" "$BASE/register"
p=$(code -b "$JAR_REG" "$BASE/profile")
[ "$p" = 200 ] && pass E01 w1-auth "register logs the user in" || fail E01 w1-auth "register logs the user in" "profile=$p"
curl -s -o /dev/null -X POST --data-urlencode username=smokeuser --data-urlencode email=smokeuser@smoke.invalid \
  --data-urlencode "password1=$PW" --data-urlencode "password2=$PW" "$BASE/register"
curl -s -o /dev/null -c "$JAR_ADMIN" -b "$JAR_ADMIN" -X POST --data-urlencode username=smokeadmin --data-urlencode "password=$PW" "$BASE/login"
[ "$(code -b "$JAR_ADMIN" "$BASE/profile")" = 200 ] || { echo "cannot log in as smokeadmin"; tail -40 "$LOG"; exit 2; }

# E02 login by email
loc=$(location -c "$JAR_USER" -b "$JAR_USER" -X POST --data-urlencode username=smokeuser@smoke.invalid --data-urlencode "password=$PW" "$BASE/login")
p=$(code -b "$JAR_USER" "$BASE/profile")
[ "$p" = 200 ] && pass E02 w1-auth "login by email" || fail E02 w1-auth "login by email" "location=$loc profile=$p"

# E03 same message for unknown user and wrong password
m1=$(curl -s -X POST --data-urlencode username=nosuchuser --data-urlencode password=wrong "$BASE/login" | grep -o 'Invalid username/email or password\|Login error[^<]*' | head -1)
m2=$(curl -s -X POST --data-urlencode username=smokeuser --data-urlencode password=wrong "$BASE/login" | grep -o 'Invalid username/email or password\|Login error[^<]*' | head -1)
[ -n "$m1" ] && [ "$m1" = "$m2" ] && pass E03 w1-auth "no username enumeration" || fail E03 w1-auth "no username enumeration" "unknown='$m1' wrong='$m2'"

# E04 open redirect
loc=$(location -X POST --data-urlencode username=smokeuser --data-urlencode "password=$PW" --data-urlencode redirect=https://evil.example/x "$BASE/login")
case "$loc" in //*|"") fail E04 w1-auth "login redirect stays local" "location=$loc" ;; /*) pass E04 w1-auth "login redirect stays local" ;; *) fail E04 w1-auth "login redirect stays local" "location=$loc" ;; esac

# E05 >72 byte password rejected with clear message
long=$(printf 'a%.0s' $(seq 1 80))
curl -s -X POST --data-urlencode username=smokelong --data-urlencode email=smokelong@smoke.invalid \
  --data-urlencode "password1=$long" --data-urlencode "password2=$long" "$BASE/register" | grep -q '72 bytes' \
  && pass E05 w1-auth "72-byte password limit message" || fail E05 w1-auth "72-byte password limit message"

# E06/E07 API tokens
c=$(code -H 'X-API: smoke-expired-token' "$BASE/api/v1/groups"); sleep 2
if [ "$c" = 401 ] && alive; then pass E06 w1-api "expired token -> 401, server alive"; else fail E06 w1-api "expired token -> 401, server alive" "status=$c alive=$(alive && echo yes || echo no)"; fi
ensure_up
c=$(code -H 'X-API: smoke-valid-token' "$BASE/api/v1/groups")
[ "$c" = 200 ] && pass E07 w1-api "valid token -> 200" || fail E07 w1-api "valid token -> 200" "status=$c"

# E08-E10 no group DB creation for unknown/inactive groups
c=$(code "$BASE/api/thread-tree?group=zz.smoke.fake&thread_root=1"); n=$(find "$DATA/db" -name 'zz_smoke_fake.db' 2>/dev/null | wc -l)
[ "$c" = 404 ] && [ "$n" = 0 ] && pass E08 w1-api "tree API unknown group" || fail E08 w1-api "tree API unknown group" "status=$c files=$n"
c=$(code "$BASE/api/thread-tree?group=smoke.inactive&thread_root=1")
[ "$c" = 404 ] && pass E09 w1-api "tree API inactive group (anon)" || fail E09 w1-api "tree API inactive group (anon)" "status=$c"
c=$(code "$BASE/smokesec/zz.smoke.fake2/tree/1"); n=$(find "$DATA/db" -name '*zz_smoke_fake2*' 2>/dev/null | wc -l)
[ "$c" = 404 ] && [ "$n" = 0 ] && pass E10 w1-api "section tree unknown group" || fail E10 w1-api "section tree unknown group" "status=$c files=$n"

# E11/E12 client IP
curl -s -o /dev/null -X POST -H 'X-Forwarded-For: 203.0.113.9, 198.51.100.7' --data-urlencode username=smokeuser --data-urlencode "password=$PW" "$BASE/login"
ip=$(q "SELECT last_login_ip FROM users WHERE username='smokeuser'")
[ "$ip" = 198.51.100.7 ] && pass E11 w1-server "XFF right-most untrusted" || fail E11 w1-server "XFF right-most untrusted" "ip=$ip"
curl -s -o /dev/null -c "$JAR_USER" -b "$JAR_USER" -X POST -H 'X-Real-IP: not-an-ip' --data-urlencode username=smokeuser --data-urlencode "password=$PW" "$BASE/login"
ip=$(q "SELECT last_login_ip FROM users WHERE username='smokeuser'")
[ "$ip" = 127.0.0.1 ] && pass E12 w1-server "invalid X-Real-IP ignored" || fail E12 w1-server "invalid X-Real-IP ignored" "ip=$ip"

# E13/E14 CSRF
c=$(code -b "$JAR_ADMIN" -H 'Sec-Fetch-Site: cross-site' -H 'Origin: https://evil.example' -X POST --data-urlencode name=csrfsec --data-urlencode display_name=x "$BASE/admin/sections")
n=$(q "SELECT count(*) FROM sections WHERE name='csrfsec'")
[ "$c" = 403 ] && [ "$n" = 0 ] && pass E13 w1-server "cross-site admin POST blocked" || fail E13 w1-server "cross-site admin POST blocked" "status=$c created=$n"
code -b "$JAR_ADMIN" -H 'Sec-Fetch-Site: same-origin' -X POST --data-urlencode name=smokesec2 --data-urlencode display_name=x "$BASE/admin/sections" >/dev/null
n=$(q "SELECT count(*) FROM sections WHERE name='smokesec2'")
[ "$n" = 1 ] && pass E14 w1-server "same-origin admin POST works" || fail E14 w1-server "same-origin admin POST works" "created=$n"

# E15 cross-site logout ignored
code -b "$JAR_ADMIN" -H 'Sec-Fetch-Site: cross-site' "$BASE/logout" >/dev/null
p=$(code -b "$JAR_ADMIN" "$BASE/profile")
[ "$p" = 200 ] && pass E15 w1-auth "cross-site GET /logout ignored" || fail E15 w1-auth "cross-site GET /logout ignored" "profile=$p"
if [ "$p" != 200 ]; then rm -f "$JAR_ADMIN"; curl -s -o /dev/null -c "$JAR_ADMIN" -b "$JAR_ADMIN" -X POST --data-urlencode username=smokeadmin --data-urlencode "password=$PW" "$BASE/login"; fi

# E16 no session write per request
e1=$(q "SELECT session_expires_at FROM users WHERE username='smokeadmin'")
curl -s -o /dev/null -b "$JAR_ADMIN" "$BASE/"; sleep 1; curl -s -o /dev/null -b "$JAR_ADMIN" "$BASE/"
e2=$(q "SELECT session_expires_at FROM users WHERE username='smokeadmin'")
[ -n "$e1" ] && [ "$e1" = "$e2" ] && pass E16 w1-auth "session slide throttled" || fail E16 w1-auth "session slide throttled" "before=$e1 after=$e2"

# E17 display name CR/LF rejected
curl -s -o /dev/null -b "$JAR_ADMIN" -X POST --data-urlencode email=smokeadmin@smoke.invalid \
  --data-urlencode $'display_name=Evil\r\nControl: cancel <x@y>' --data-urlencode "current_password=$PW" "$BASE/profile"
n=$(q "SELECT instr(display_name, char(10)) + instr(display_name, char(13)) FROM users WHERE username='smokeadmin'")
[ "$n" = 0 ] && pass E17 w1-auth "display name CR/LF rejected" || fail E17 w1-auth "display name CR/LF rejected" "instr=$n"

# E18 chat page must not contain the session cookie value
sid=$(awk '$6=="session_id"{print $7}' "$JAR_ADMIN" | tail -1)
body=$(curl -s -b "$JAR_ADMIN" "$BASE/aichat")
if [ -n "$sid" ] && printf %s "$body" | grep -q 'Smoke' && ! printf %s "$body" | grep -qF "$sid"; then pass E18 w1-server "session id not in chat page"; else fail E18 w1-server "session id not in chat page" "sid_len=${#sid}"; fi

# E19 sections cache race (binary built with -race)
( for _ in $(seq 1 300); do curl -s -o /dev/null "$BASE/smokesec/"; done ) & LOOP=$!
for i in $(seq 1 20); do code -b "$JAR_ADMIN" -H 'Sec-Fetch-Site: same-origin' -X POST --data-urlencode "name=race$i" --data-urlencode display_name=race "$BASE/admin/sections" >/dev/null; done
wait "$LOOP"
if alive && ! grep -A40 'DATA RACE\|concurrent map' "$LOG" | grep -q 'loadSectionsCache\|isValidSection\|SectionsCache\|sectionsCache'; then pass E19 w1-server "sections cache race-free"; else fail E19 w1-server "sections cache race-free" "alive=$(alive && echo yes || echo no)"; fi
ensure_up
info E22 any "DATA RACE reports in log so far: $(grep -c 'WARNING: DATA RACE' "$LOG")"

# E20 slowloris dropped
start=$SECONDS
timeout 30 bash -c "exec 3<>/dev/tcp/127.0.0.1/$PORT; printf 'GET / HTTP/1.1\r\nHost: smoke\r\n' >&3; cat <&3 >/dev/null"; rc=$?
el=$((SECONDS-start))
[ "$rc" -ne 124 ] && [ "$el" -le 20 ] && pass E20 w1-server "partial headers dropped (${el}s)" || fail E20 w1-server "partial headers dropped" "rc=$rc elapsed=${el}s"

# E21 LIKE wildcards escaped
curl -s "$BASE/search?q=%25&searchType=groups" | grep -q 'smoke.test' && fail E21 w2-dbperf "search % is literal" || pass E21 w2-dbperf "search % is literal"

# E24 throughput (informational)
if command -v ab >/dev/null; then info E24 w2-templates "$(ab -q -n 300 -c 10 "$BASE/groups" 2>/dev/null | grep 'Requests per second')"; fi

# E25/E26 posting header validation
b=$(curl -s -b "$JAR_ADMIN" -X POST --data-urlencode newsgroups=smoke.test --data-urlencode subject='Re: x' --data-urlencode body=hello \
  --data-urlencode reply_to=1 --data-urlencode $'message_id=<a@b>\r\nControl: cancel <x@y>' "$BASE/SitePostSubmit")
printf %s "$b" | grep -qi 'invalid reply message-id' && pass E25 w1-api "reply message-id CR/LF rejected" || fail E25 w1-api "reply message-id CR/LF rejected"
b=$(curl -s -b "$JAR_ADMIN" -X POST --data-urlencode newsgroups=smoke.test --data-urlencode $'subject=Hi\r\nApproved: yes' --data-urlencode body=hello "$BASE/SitePostSubmit")
printf %s "$b" | grep -qi 'subject contains invalid characters' && pass E26 w1-api "subject CR/LF rejected" || fail E26 w1-api "subject CR/LF rejected"

# E27 new group DB uses WAL
code -H 'X-API: smoke-valid-token' "$BASE/api/v1/groups/smoke.test/overview" >/dev/null
f=$(find "$DATA/db" -name 'smoke_test.db' | head -1)
jm=$( [ -n "$f" ] && sqlite3 "$f" 'PRAGMA journal_mode' )
[ "$jm" = wal ] && pass E27 w1-sqlite "new group DB journal_mode=wal" || fail E27 w1-sqlite "new group DB journal_mode=wal" "file=$f mode=$jm"

# E23 -no-cronjobs starts
stop_server
if start_server -no-cronjobs; then sleep 5; alive && curl -fs "$BASE/ping" >/dev/null && pass E23 w1-server "-no-cronjobs starts" || fail E23 w1-server "-no-cronjobs starts" "died after start"; else fail E23 w1-server "-no-cronjobs starts" "did not start"; fi
stop_server

echo "SUMMARY pass=$PASSES fail=$FAILS data=$DATA log=$LOG"
[ "$FAILS" -gt 125 ] && FAILS=125
exit "$FAILS"
```

---

## Progress

### Wave 0 (orchestrator, 2026-09-15)
- Integration branch `plan-web-sqlite-hardening` from `testing-001` @ 0f27e76; the plan moved to `wip/`.
- Baseline checks at 0f27e76: `gofmt -l` lists only the 4 known files. `go vet`, `go build`, `go test -race`
  (history, nntp, processor, expire-news, history-rebuild; database and web had no tests) and `./build_webserver.sh` PASS.
- **Deviations from the wave-0 steps** (all found while running them; wave-1 slices build on these):
  1. **`cmd/web` ignored `-data`.** It was the only tool that never set `dbConfig.DataDir`, and the progress DB path
     was hardcoded to `data/progress.db`. The e2e script as written would have run against the checkout's production
     `./data`. Fixed inline in `cmd/web/main.go`: `dbConfig.DataDir = dataDir` and
     `NewProgressDB(filepath.Join(dataDir, "progress.db"))`. The default is unchanged (`./data`,
     `data/progress.db/progress.db`), and `run_web*.sh` pass no `-data`.
     **Behaviour change to report:** a deployment that passes `-data <dir>` to the webserver now really uses `<dir>`.
     `w1-server` owns `cmd/web/main.go` and must keep this.
  2. **Web test harness cwd.** Instead of `os.Chdir("../..")`, `TestMain` runs from a temp dir that only symlinks
     `<checkout>/web`, so cwd-relative paths (`./data`, cron job commands) never reach the checkout. Additions:
     `w0RepoRoot`, `w0DataDir` (group DBs under `<w0DataDir>/db`), `w0TestDB`, `w0DB(t)` (also in web), `w0CreateUser`.
     It sets `config.AppVersion = "test"` first (`NewDefaultConfig` log.Fatalf's while unset, `config.go:542`),
     plus `database.GlobalDateParser` and the model caches, like cmd/web. `w0NewUser` names users `w0user_<n>`
     (the username validator rejects dots) and uses bcrypt MinCost. `w0NewGroup` also sets `hierarchy`, because
     `MainDBGetNewsgroup` scans it into a string and fails on NULL.
  3. **e2e script fixes** (real script bugs; the listing above is outdated on these points):
     - The server's working dir is `$DATA/run` (only `web/` is linked there), with an absolute `-data`. The script
       aborts when the schema does not appear in `$DB` (a binary that ignores `-data`), refuses `DATA` containing
       `..`, and refuses a port that already answers.
     - `-nntphostname smoke.invalid` can never start: `processor.SetHostname` requires an FQDN that resolves
       (`net.LookupIP`). The script uses `$NNTPHOST`, or else the first resolvable of `hostname -f`,
       `<hostname>.local`, `<hostname>.lan` (here `nuc.local` from `/etc/hosts`).
     - The default config blocks User-Agents containing `curl` (migration 0023, `BlockBadBots=true`), so every
       request got 403. A `curl()` wrapper sends `User-Agent: pugleaf-smoke/1.0`; bot blocking stays on.
     - Seeded newsgroups get `hierarchy='smoke'`.
- `go test -race ./internal/database/ ./internal/web/ -run W0 -count=3`: PASS, no races.
- Baseline e2e (`PORT=18980`, binary built from the wave-0 tree): `SUMMARY pass=4 fail=21`.
  - PASS: E07, E10, E14, E19. E19 did not catch the race at baseline; `TestW1ServerSectionsCacheRace` is the real check.
  - FAIL: E01–E06, E08, E09, E11–E13, E15–E18, E20, E21, E23, E25–E27. Confirmed in the log: E06 is the nil deref
    at `web_apitokens.go:40` (C1), E23 the nil deref at `cronjobs.go:74` (C2), E08 `status=500 files=1` (C4),
    E20 `rc=124 elapsed=30s` (C6).
  - INFO: E22 `0` races; E24 `634.25 req/s` on `/groups`.
- Leftover found: `cmd/nntp-fetcher/main.go:138` also hardcodes `NewProgressDB("data/progress.db")` (cwd-relative,
  ignores `-data`).
