# Audit audit-web (base b962f3a)

Delta audit of the web layer after the three merged plans WSH, LO and FU. Their findings are cited as
`WSH:<id>`, `LO:<id>` and `FU:<id>` and are not reported again. Findings of the sibling reports are
cited by id (`A-db-1` in `audit-db.md`, `A-intake-5`/`A-intake-6` in `audit-intake.md`). The report
follows `docs/audit/README.md` (D1, K6). Every `path:line` anchor was taken with `grep -n`/`sed -n`
at b962f3a. Paths are under `internal/` unless they start with `web/`, `cmd/` or `docs/`.

## Scope and method

Read in full (the files the merged plans did not review three times):
`web/web_admin_crons.go`, `web/web_admin_nntp.go`, `web/web_admin_newsgroups.go`,
`web/web_admin_provider.go`, `web/web_admin_spam.go`, `web/web_admin_postqueue.go`,
`web/web_admin_sitenews.go`, `web/web_admin_sections.go`, `web/web_admin_apitokens.go`,
`web/web_admin_cache.go`, `web/web_apiHandlers.go`, `web/web_threadTreePage.go`,
`web/web_articlePage.go`, `web/web_utils.go`, `web/web_helpers.go`, `web/web_groupsPage.go`,
`web/static/js/thread-tree.js`, `web/static/theme-switcher.js`, `models/sanitizing.go`,
`models/cache.go`, `cache/sanitized_cache.go`, `cache/newsgroup_cache.go` (structure, key, eviction),
`utils/references.go`, `database/db_apitokens.go`, `database/tree_view_api.go`,
`database/sanitize_cache.go`, `database/sections_cache.go`.
Read in part: `web/webserver_core_routes.go` (route table `:329-536`, `secure.Config` `:280-296`,
`BotDetectionMiddleware`), `web/webserver.go` (`rootHandler` `:54-62`), `web/cronjobs.go` (start,
reload, exec, log buffer), `web/web_hierarchiesPage.go` (the two admin handlers), `web/web_profile.go`
(NNTP password reset), `web/web_sitePostPage.go` (`:330-420`, header construction), `web/web_auth.go`
(`isAdminRequest`, flash store, cookies), `web/web_adminPage.go` (FuncMap), `web/webgroupPage.go`
(spam/hide handlers), `database/tree_cache.go` (outline), `database/thread_cache.go:309-315`,
`database/queries.go` (`GetArticleByNum` `:890-914`, `GetProviderByName` `:1440-1453`,
`GetNewsgroupsPaginated` `:1068-1072`), `database/db_nntp_users.go` (`:57-63`, `:335-350`),
`processor/PostQueue.go:64-126`.
Templates: every `web/templates/*.html` was scanned for `template.HTML` values, `<script>` blocks with
template actions, `href`/`src`/`action` starting with an action, `on*=`/`style=` attributes with
actions, and `innerHTML`/`document.write`/`insertAdjacentHTML`; the hits were read in context
(`aichat.html`, `base.html`, `base_chat.html`, `article.html`, `thread.html`, `thread-tree.html`,
`admin_crons.html`, `admin_newsgroups.html`, `admin_nntpusers.html`, `admin_providers.html`,
`admin_sitenews.html`, `admin_modular.html`, `hierarchy_tree.html`, `section-article.html`, `news.html`).
Not reviewed again: the files WSH/LO/FU changed and tested (`web_auth.go`, `web_login.go`,
`web_registerPage.go`, `web_profile.go` outside the NNTP reset, `web_sectionsPage.go`,
`web_threadPage.go`, `webgroupPage.go` outside spam/hide, `web_admin_userfuncs.go`,
`web_admin_ollama.go`, `web_admin_settings_unified.go`, `web_aichatPage.go`, `db_sessions.go`,
`db_sections.go`), and the vendored `static/bootstrap-*`, `static/npm/*`, fonts and CSS.

Tools: `docs/audit/tools/*` filtered to the scope. staticcheck/golangci-lint `unused`
(`SanitizedCache.xxxhashMessageID`, `SanitizedCache.evictOldest`, `models.splitDomainFromEmail`,
`WebServer.adminCleanupExpiredTokens`, `WebServer.requireAdminAuthJSON`, `referencesAnyInThread`) are
in the function inventory; `evictOldest` being dead is the root of A-web-2. gosec G203 on
`models/sanitizing.go:249,535,643` is a false positive (the value is `html.EscapeString` output).
gosec G101 on `database/db_apitokens.go:42` is a false positive (SQL const). govulncheck reaches the
scope only through stdlib/x-net call paths (`webserver.go:125,128,145`, `models/sanitizing.go:39,67`);
those are fixed by the toolchain/module upgrades listed in `docs/audit/README.md`.

Repros: a temporary `internal/web/zz_audit_web_test.go` using the package `TestMain` (`w0Srv`,
`w0TestDB`, `w0NewGroup`, `w0NewUser`, `w0Do` against `w0Srv.Router`) and `lo1CoreSetAPIEnabled`.
Run with `go test -count=1 -run TestZZAudit -v ./internal/web/` (and `-race` for A-web-3). The file
was deleted before the commit. `cmd/web` was not started; no binary touched `./data`.

## Summary

| Severity | Count | Ids |
|----|----|----|
| critical | 0 | |
| high | 2 | WEB-1, WEB-2 |
| medium | 6 | INT-7, A-web-1, A-web-2, A-web-3, A-web-4, A-web-5 |
| low | 15 | A-web-6 … A-web-20 |

| Category | Count |
|----|----|
| security | 7 (WEB-1, WEB-2, A-web-1, A-web-10, A-web-11, A-web-12, A-web-13) |
| bug | 11 (A-web-4, A-web-6, A-web-7, A-web-8, A-web-9, A-web-14, A-web-15, A-web-16, A-web-17, A-web-18, A-web-20) |
| robustness | 3 (A-web-2, A-web-3, A-web-19) |
| rfc | 1 (INT-7) |
| wrong-function | 1 (A-web-5) |

Most important:
1. **WEB-2 (high)**: stored XSS. The article preview returns the raw, entity-decoded body and
   `thread-tree.js` inserts it with `innerHTML`. Anyone who gets an article into an active group (any
   Usenet poster via the fetcher, NNTP POST/IHAVE, a registered web user) runs script in the session of
   whoever clicks a node in the tree view, the API setting does not matter, and there is no CSP.
   Against an admin this chains into WEB-1.
2. **WEB-1 (high)**: `-edit-cronjobs` does not gate cron create/toggle. An admin session (or a WEB-2
   payload riding it) creates and enables a `sh -c` job; the cron manager registers it within a minute
   and runs it after `interval_minutes`.
3. **A-web-1 (medium)**: articles an admin hid (`hide=1`) stay readable by anonymous users by number,
   message-id, preview route and `/api/thread-tree`. Only the listings filter them.
4. **A-web-2 (medium)**: the sanitized-article cache never enforces `-maxsanartcache`; every article
   page an unauthenticated crawler requests stays in memory for the full expiry (30 min).
5. **A-web-4 (medium)**: sanitizing rewrites the shared `*models.Article` held by the article cache, so
   after one page view the JSON API and the reply prefill return a filtered From and a regex-stripped
   body (the preview gets the stripped body), and unlocked readers race with the write.

## Findings

### WEB-1: cron create, toggle and stop ignore `-edit-cronjobs`

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| WEB-1 | WEB-1 | high | security | `web/web_admin_crons.go:18-69,148-173,307-347`, `web/webserver_core_routes.go:471-475`, `web/cronjobs.go:51-62,145,481` | CONFIRMED |

- **Scenario:** `cmd/web` runs without `-edit-cronjobs` (`cmd/web/main.go:128`, default false, help text
  "Adding/Editing Crons is disabled by default"). An admin POSTs `/admin/crons/create` with
  `enabled=on` and any `command` (this holds unconditionally), or toggles on a disabled job that the
  manager has not registered since startup (created disabled, or disabled at boot; a job disabled
  later stays dead until restart, A-web-20). The job row is written enabled; `StartCronManager`
  reloads every `cronReloadEvery` (1 min, `cronjobs.go:65`, `:145`) and `loadAndStartJobs` registers
  every enabled job; the scheduler's first run is at now + `interval_minutes` (`calculateNextRun`,
  `:229-233`), where `executeJob` (`:455`) runs `getShellCommand(job.Command)` (`:481`), i.e.
  `sh -c <command>` (`:61`). So the operator's safety switch protects only update
  (`web_admin_crons.go:79`) and delete (`:183`). Create (`:18`) and toggle (`:148`) check only
  `requireAdminAuth`. Stop (`:307`) is also ungated: `StopJob` sends SIGINT to a running job, and its
  non-blocking send on `job.stopChan` (`cronjobs.go:294-298`) makes an idle job's scheduler return
  (`:427-430`) while the job stays in `cm.jobs`, so `startJob` answers `ErrCronExists` (`:349-350`) and
  the job never runs again until restart. Stop can cancel runs but cannot start anything (low impact).
  The create form is rendered regardless of the flag (`web/templates/admin_crons.html:163-226`); the
  only hint is the text at `:13-14`.
- **Chain:** the admin session is reachable by WEB-2 (same-origin `fetch` passes
  `http.CrossOriginProtection` in `webserver.go:59-62`), so WEB-2 + WEB-1 is code execution as the
  server user from a Usenet post, given one admin click in a tree view. Separately, the first account
  to register on a fresh install becomes admin (`isAdmin` user-ID-1 rule, `web/web_admin_userfuncs.go:290`;
  documented in `README.md:70`; registration defaults to on, `database/migrations/0001_main_schema.sql:253`).
- **Evidence:** `TestZZAuditWebCronNoEdit` (admin cookie, `w0Srv.CronEdit=false`):
  ```
  w0Srv.CronEdit=false CronManager!=nil=true
  POST /admin/crons/create (CronEdit=false) -> 303 success="Cron job created successfully" error=""
  row: id=4 name=zzauditcron.t3 command="true" enabled=false
  POST /admin/crons/update -> 303 success="" error="Editing Crons is disabled"
  POST /admin/crons/toggle -> 303 success="Cron job status updated successfully" error=""; enabled now=true
  ```
  (the repro created the job disabled and deleted it right after the toggle so the manager never ran it).
- **Fix:** check `s.CronEdit` in create and toggle (refuse enabling; allow disabling), hide the create
  form and toggle-on button when the flag is off, and keep stop allowed. Effort S.

### WEB-2: stored XSS through the article preview in the tree view

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| WEB-2 | WEB-2 | high | security | `web/web_apiHandlers.go:448-501` (body `:488-498`), `web/static/js/thread-tree.js:515,522-541`, `web/webserver_core_routes.go:502,504`, `models/sanitizing.go:36-63` | CONFIRMED |

- **Scenario:**
  - `getArticlePreview` builds `body` from `models.ConvertToUTF8(article.BodyText)` (`:488`), which
    MIME/QP-decodes and then runs `html.UnescapeString` (`sanitizing.go:48`); no escaping follows (the
    comment at `:486-487` says "JS will handle HTML context"). `subject`/`from` are `PrintSanitized`
    (escaped). `group` is the raw route parameter (`:493`).
  - `ArticlePreview.renderArticlePreview` concatenates `article.body` and
    `<a href="/groups/${article.group}/...">` (`thread-tree.js:515`), runs `formatPreviewText` (newline
    normalisation only, `:544-555`) and assigns the result to `contentDiv.innerHTML` (`:522`, sink at
    `:539`). `escapeHtml` (`:557`) has no caller; the comment at `:520-521` ("already sanitized via
    PrintSanitized") is wrong for the body.
  - Since LO:B11 the JS fetches the web route `/groups/:group/articles/:articleNum/preview`
    (`thread-tree.js:496`, route `webserver_core_routes.go:502`), which has no `requireAPIEnabled`.
  - The preview runs when a visitor clicks any `.node-link` or `.btn-preview` in the tree view
    (`thread-tree.js:446-461`); the tree view (`/groups/:group/tree/:root`, `:504`, and the section
    variant) is linked from every thread page (`web/templates/thread.html:39`), and the node-link covers
    the subject/author text a reader clicks to open an article.
  - Writers of the body: any poster on upstream Usenet (fetched articles are stored verbatim), NNTP
    POST/IHAVE/TAKETHIS clients, and registered web users (`web_sitePostPage.go:380`). Because of the
    `html.UnescapeString`, an entity-encoded payload (`&lt;svg onload=…&gt;`) that looks inert in the
    raw article becomes live markup.
  - No mitigation: `secure.Config` sets no `ContentSecurityPolicy` (`webserver_core_routes.go:280-285`,
    A-web-13). `sanitizeText` is not one either: it runs only when the article page sanitized the cached
    object first (A-web-4), and `eventRegex` (`sanitizing.go:29`) only removes quoted handlers, so
    `<img src=x onerror=alert(1)>` survives it.
  - Second, weaker sink: `article.group` lands unescaped in an `href` inside the same `innerHTML`. The
    group must exist and be active; intake names are limited by `validGroupNameRegexLazy`
    (`common/headers.go:28`, no quotes or `<>`), admin-created names are not validated (A-web-16).
- **Evidence:** `TestZZAuditWebPreviewXSS` (body seeded with raw SQL into an active group, API disabled,
  anonymous request):
  ```
  GET /groups/w0grp.t1/articles/1/preview (API disabled, anonymous) -> 200 application/json; charset=utf-8
  CSP header: ""
  body: {"article_num":1,"body":"hello <img src=x onerror=alert(document.domain)>\n<svg onload=alert(2)>\n<a href=\"javascript:alert(3)\">x</a>","date":"…","from":"Mallory","group":"w0grp.t1","lines":1,"subject":"zz subject &lt;b&gt;"}
  GET /groups/w0grp.t1/tree/1 -> 200; has thread-tree.js=true btn-preview=true node-link=true data-group-name=true CSP=""
  GET /static/js/thread-tree.js -> 200; fetches web preview route=true; inserts cleanPreview via innerHTML=true; escapeHtml( calls=1
  ```
  The seeded body was `hello <img src=x onerror=…>\n&lt;svg onload=alert(2)&gt;\n<a href="javascript:alert(3)">x</a>`:
  the `&lt;svg…&gt;` line comes back as live `<svg onload=alert(2)>`. The one `escapeHtml(` match is
  the method definition. The `body:` line is shown JSON-decoded: on the wire gin writes `<`, `>` and
  `&` as backslash-u escapes (`u003c`, `u003e`, `u0026`), which `response.json()` in `thread-tree.js:498`
  turns back into the characters shown. The browser step (execution on click) follows from the
  `innerHTML` assignment; it was not run in a browser.
- **Fix:** build the preview DOM with `textContent` (or call `escapeHtml` on body, subject, from and
  group and `encodeURIComponent` on the group in the `href`); stop unescaping entities for this field on
  the server (or return the `PrintSanitized("bodytext")` value and drop the "already sanitized" comment
  only once that is true); add a CSP (A-web-13). Effort S.

### INT-7: web posts are stored and served with a truncated header set

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-7 | INT-7 | medium | rfc | `web/web_sitePostPage.go:358-402`, `processor/PostQueue.go:84-99`, `nntp/nntp-cmd-helpers.go:20-22` | CONFIRMED |

- **Scenario:** `sitePostSubmit` builds `headers` as MIME-Version, Content-Type, Content-Transfer-Encoding,
  a folded `X-pugleaf-Trace` (nonce, mail-complaints-to, posting-account), From, Newsgroups, Lines and
  Bytes (`:361-371`) and stores `strings.Join(headers, "\n")` as `HeadersJSON` (`:377`). Subject,
  Message-ID, Date, References and Path exist only as struct fields (`:374-389`, `:398`).
  `pre_processArticle` copies `HeadersJSON` unchanged per group (`PostQueue.go:93`) and
  `ProcessArticle` never rewrites it. The NNTP server serves HEAD/ARTICLE from
  `strings.Split(article.HeadersJSON, "\n")` (`nntp-cmd-helpers.go:20-22`), so a web post is served
  without Subject, Message-ID, Date, Path and References.
- **Also:** `Bytes` is `len(body) - linesCount` (`:360`), which is neither the body octet count nor the
  RFC 3977 `:bytes` value; Lines/Bytes go into `article.Headers` only for replies (`:401-402`).
  Crossposted web posts land in the first group only (A-intake-5). Upstream posting regenerates the
  header block through `ReconstructHeaders` (A-intake-6), so peers see a different header set from
  local NNTP readers.
- **Evidence:** code trace above; no `HeadersJSON` assignment in `processor/threading.go`
  (`grep -n HeadersJSON internal/processor/threading.go` is empty).
- **RFC:** RFC 5536 §3.1: "Message-ID, Date, From, Newsgroups, Path, and Subject … MUST be present in
  every article"; RFC 5537 §3.5: the injecting agent adds Message-ID/Date when missing.
- **Fix:** build the stored header block from the same values as the struct fields (Path, Message-ID,
  Date in RFC 5322 form, Subject, References for replies) in `sitePostSubmit`, compute Bytes per RFC
  3977 §8.1.1, and decide with the `nntp-intake-injection` plan whether X-pugleaf-Trace becomes
  Injection-Info. Effort S.

### A-web-1: hidden articles remain readable by anonymous users

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-1 | medium | security | `database/queries.go:890`, `database/thread_cache.go:309-315`, `web/web_articlePage.go`, `web/web_apiHandlers.go:448-501`, `web/web_threadTreePage.go:28-96` | CONFIRMED |

- **Scenario:** the admin "Hide" button (`incrementHide`, `webgroupPage.go:225-253`) and
  `adminHideFuturePosts` (`web_admin_newsgroups.go:806`) set `articles.hide`. Only the listings filter
  on it (`web_apiHandlers.go:188`, `web_sectionsPage.go:242`, the thread/overview queries). The single
  article reads have no `hide` predicate: `query_GetArticleByNum` (`queries.go:890`),
  `GetArticleByMessageID` (`:917`) and `GetOverviewByArticleNum` (`thread_cache.go:310-315`). So an
  article hidden as spam or abuse is still served at its stable URL, by message-id, by the preview route
  (with the API disabled) and in the `/api/thread-tree` JSON, to anonymous visitors.
- **Evidence:** `TestZZAuditWebHiddenAndTreeAPI` (article seeded, then `UPDATE articles SET hide = 1`):
  ```
  anon GET /groups/w0grp.t1/articles/1 (hide=1, API disabled) -> 200 subject=true body=true
  anon GET /groups/w0grp.t1/message/<zzaudit.hidden.w0grp.t1@test.invalid> (hide=1, API disabled) -> 200 subject=true body=true
  anon GET /groups/w0grp.t1/articles/1/preview (hide=1, API disabled) -> 200 subject=true body=true
  anon GET /api/thread-tree?group=w0grp.t1&thread_root=1 (hide=1, API disabled) -> 200 subject=true rawEmail=true cache-control="public, max-age=300"
  ```
- **Fix:** decide the semantics (hide = gone for non-admins) and enforce it in the web handlers: load
  `hide` with the article (see A-web-15) and answer 404 to non-admins, filter hidden nodes in the tree
  and the preview. Effort M.

### A-web-2: the sanitized-article cache has no size bound

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-2 | medium | robustness | `cache/sanitized_cache.go:30-41,62-75,181-243,420-442`, `models/cache.go:12`, `cmd/web/main.go:91,96-97,364` | CONFIRMED |

- **Scenario:** `cmd/web` enables the cache (`models.DisableSanitizedCache = false`, `main.go:91`) with
  `-maxsanartcache` 10000 and `-maxsanartcacheexpiry` 30 min (`:96-97`, `:364`). `maxEntries` is stored
  (`sanitized_cache.go:65`) and only printed in the stats (`:393`); `SetField`/`SetArticle`/
  `BatchSetArticles` never check it, and the only eviction helper `evictOldest` (`:420`) has no caller
  (staticcheck U1000). `cleanup` drops entries by age only (`:461-501`). Every article page stores the
  escaped body, subject, from, references, path and headers (`BatchSanitizeArticles`,
  `models/sanitizing.go:615-680`, called by `web_articlePage.go:70`), so an unauthenticated crawler that
  walks article numbers keeps 30 minutes of full bodies resident, without limit. The flag's help text
  promises a bound that does not exist. The cleanup also logs a line every minute even when it removed
  nothing (`:496-500`).
- **Evidence:** `grep -n maxEntries internal/cache/sanitized_cache.go` → `:33`, `:62`, `:65`, `:393` only.
  Memory growth was not measured.
- **Fix:** enforce `maxEntries` on insert (evict by `LastUsed`, synchronously, under `sc.mutex`) and
  account sizes in one place; drop the empty-cleanup log. Effort S.

### A-web-3: data race in `SanitizedCache.GetField`

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-3 | medium | robustness | `cache/sanitized_cache.go:78-159` vs `:181-243` | CONFIRMED |

- **Scenario:** `GetField` takes `article.mux` only to update `LastUsed` (`:99-101`) and then reads
  `article.Subject`, `.Date`, `.Path`, … after `Unlock` (`:104-158`), while `SetField` writes the same
  fields under `article.mux` (`:198-235`). Two concurrent renders of the same article (two visitors, or
  one page with several `PrintSanitized` calls racing another request) race on string headers; a torn
  string read can crash the process.
- **Evidence:** `go test -race -run TestZZAuditWebArticleCacheRace ./internal/web/` (8 concurrent GETs of
  one article page and its preview) reported 2 races:
  ```
  WARNING: DATA RACE
  Write at 0x00c0001d4748 by goroutine 76:
    cache.(*SanitizedCache).SetField()  internal/cache/sanitized_cache.go:213
    models.SetCachedSanitized()         internal/models/cache.go:51
    models.(*Article).PrintSanitized()  internal/models/sanitizing.go:253
  Previous read at 0x00c0001d4748 by goroutine 80:
    cache.(*SanitizedCache).GetField()  internal/cache/sanitized_cache.go:124
    models.GetCachedSanitized()         internal/models/cache.go:42
  WARNING: DATA RACE
  Write … SetField() sanitized_cache.go:223 / Previous read … GetField() sanitized_cache.go:142
  ```
  The existing `go test -race ./internal/web/...` suite does not hit it because no test renders one
  article concurrently.
- **Fix:** read the field under `article.mux.RLock()` (or keep the fields immutable and swap a struct
  pointer). Effort S.

### A-web-4: sanitizing mutates the shared cached `*Article`

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-4 | medium | bug | `models/sanitizing.go:206-211,464-477,631-636`, `database/queries.go:892-914`, `web/web_apiHandlers.go:488`, `web/web_apiHandlers.go:237-265` | CONFIRMED (race part PLAUSIBLE) |

- **Scenario:** `GetArticleByNum` returns the pointer held by `ArticleCache` (`queries.go:894-897`,
  `:910-912`; `ENABLE_ARTICLE_CACHE` defaults to true, `database/db_init.go:21`).
  `Article.PrintSanitized` and `BatchSanitizeArticles` call `StripDangerousHTML` on that object
  (`sanitizing.go:209-211`, `:634-636`), which overwrites `Subject`, `BodyText`, `DateString`,
  `References` with `sanitizeText` output and `FromHeader` with the display name from
  `filterMailDomainInFromHeader` (`:469-473`). After one article page view, every later reader of the
  cached object gets the altered data: the JSON API `getArticle` (`web_apiHandlers.go:237-265`) and the
  reply quote prefill (`web_sitePostPage.go:90-100`) see both the filtered `FromHeader` and the stripped
  body. The preview (`:488`) is affected only in its body: its `from` comes from
  `overview.PrintSanitized("fromheader")`, which filters on its own. The writes hold `a.Mux`; the
  preview and the JSON encoder read without it.
- **Evidence:** `TestZZAuditWebArticleCacheRace` (article seeded with From `Alice Example
  <alice@test.invalid>` and body `body <script>x</script> onclick='y()' end`):
  ```
  preview after article page views: {"article_num":1,"body":"body   end","date":"…","from":"Alice Example",…}
  cached Article after page views: FromHeader="Alice Example" BodyText="body   end" Sanitized=true
  ```
  The race detector flagged only A-web-3 in that run; the unlocked read of `BodyText` at
  `web_apiHandlers.go:488` against the locked write at `sanitizing.go:471` is a trace, not a detector hit.
- **Fix:** sanitize into local variables (or a copy) and never write back into the model; drop
  `Sanitized` and `StripDangerousHTML` from the shared struct. Effort M.

### A-web-5: NNTP users created without a mapping are linked to web user 1

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-5 | medium | wrong-function | `web/web_admin_nntp.go:91-97,160-207`, `web/web_profile.go:105-129`, `database/db_nntp_users.go:57-63,335-350`, `database/migrations/0001_main_schema.sql:139,151` | CONFIRMED |

- **Scenario:**
  - `adminCreateNNTPUser` defaults `webUserID := int64(1)` when the form sends no mapping or `0`
    (`:91-97`; the comment says "Default to admin user"), although the model documents 0 as "no mapping"
    (`models/models.go:570`). `web_user_id` has no UNIQUE constraint (`0001_main_schema.sql:139`, plain
    index `:151`).
  - So every unmapped account (peer feeds, readers) is "owned" by user 1. `GetNNTPUserByWebUserID`
    picks one of them arbitrarily (`QueryRow`, no ORDER BY, `db_nntp_users.go:59-63`). User 1's profile
    shows that account (`web_profile.go:42`), and "Reset NNTP Password" resets it (`:105-121`), breaking
    whichever peer or reader owns it.
  - LO:F22's `refuseDisabledNNTPUser` (`db_nntp_users.go:338-350`) refuses every NNTP account linked to
    a disabled web user, so disabling user 1 locks out all unmapped accounts.
  - `adminUpdateNNTPUser` silently ignores `web_user_id` (`:199-203`) and still reports success. It
    changes `posting` only when `maxconns` is non-empty (`:167-181`) and accepts passwords of 1 to 16
    characters with no minimum (`:184-189`).
- **Evidence:** code trace; `web_adminPage.go:72` uses the same lookup for the user list.
- **Fix:** pass 0 for "no mapping" (`InsertNNTPUser` already stores `NULLIF(?, 0)`,
  `db_nntp_users.go:22-25`), add the missing update of `web_user_id`, update `posting` independently,
  and enforce a minimum password length. Effort S.

### A-web-6: From display breaks on RFC 2047 names and leaks the address in comment form

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-6 | low | bug | `models/sanitizing.go:382-461,470,495` | CONFIRMED |

- **Scenario:** `filterMailDomainInFromHeader` splits the raw header at the last space, cuts the name to
  16 bytes and runs `cleanFromHeaderName` (keeps letters, digits, space, `-`, `.`) **before**
  `ConvertToUTF8` decodes encoded words (`:418-461`; decoding happens later at `:246`/`:532`). Encoded
  names, which are common on Usenet, are shown as fragments of the encoding. The RFC 5322 comment form
  `addr (Name)` shows the address with the `@` removed. The full From still appears unfiltered in the
  headers panel of the article page (`web/templates/article.html:156`), so the filter does not hide
  addresses anyway.
- **Evidence:** `TestZZAuditWebEncodedFrom`:
  ```
  from "=?UTF-8?Q?J=C3=BCrgen_M=C3=BCller?= <j@test.invalid>" -> "UTF-8QJC3B"
  from "=?ISO-8859-1?Q?J=FCrgen?= <j@test.invalid>" -> "ISO-8859-1QJ"
  from "\"Jürgen Müller\" <j@test.invalid>" -> "Jürgen Müller"
  from "j@test.invalid (Jürgen Müller)" -> "jtest.invalid"
  ```
- **Fix:** parse with `net/mail.ParseAddress` after RFC 2047 decoding, take the display name (or
  comment), truncate by runes. Effort S.

### A-web-7: subjects are cut at byte 128 before decoding

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-7 | low | bug | `models/sanitizing.go:218,504,557,577,606-611,642,715` | CONFIRMED |

- **Scenario:** `truncateSubject` slices `subject[:128]` (`:610`) on the raw, still encoded value. A cut
  inside a multi-byte rune makes the string invalid UTF-8, and `ConvertToUTF8` then re-decodes the
  **whole** subject as ISO-8859-1 (`:51-57`). A cut inside an encoded word leaves the raw
  `=?UTF-8?B?…` text. This affects every listing and title (the same function feeds `GetCleanSubject`
  and both batch sanitizers). WSH:L6 fixed only the preview truncation (`truncateRunes`,
  `web_apiHandlers.go:50-61`).
- **Evidence:** `TestZZAuditWebUTF8Truncation`:
  ```
  subject len=133 bytes; PrintSanitized(subject)="GrÃ¼Ã\u009fe xxxx…xxxâ..."
  encoded-word subject len=172 -> "=?UTF-8?B?QUJDQUJD…QUJDQU..."
  ```
- **Fix:** decode first, then truncate with `truncateRunes`. Effort S.

### A-web-8: `ConvertToUTF8` decodes bodies as quoted-printable and RFC 2047 unconditionally

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-8 | low | bug | `models/sanitizing.go:36-72` | CONFIRMED |

- **Scenario:** the same function serves headers and bodies (`PrintSanitized("bodytext")`,
  `BatchSanitizeArticles` `:648`, the preview). It runs `mime.WordDecoder.DecodeHeader` and a
  quoted-printable reader over every body regardless of `Content-Transfer-Encoding` (`:39`, `:45`), then
  `html.UnescapeString` (`:48`). A plain 8bit body that happens to be valid QP is altered (soft line
  breaks join lines, `=XX` becomes a byte). Any invalid byte makes the whole text be re-decoded as
  ISO-8859-1, which garbles every correct UTF-8 character in it (`:51-57`).
- **Evidence:** `TestZZAuditWebConvert`:
  ```
  ConvertToUTF8("x =41 y")="x A y"
  ConvertToUTF8("soft=\nbreak")="softbreak"
  ConvertToUTF8("ok utf8 ü plus latin1 \xfc")="ok utf8 Ã¼ plus latin1 ü"
  ```
- **Fix:** split header and body decoding; decode bodies per the stored Content-Type/CTE (or at intake),
  and convert per invalid sequence instead of per string. Effort M.

### A-web-9: `sanitizeText` rewrites plain text and is not an XSS control

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-9 | low | bug | `models/sanitizing.go:26-29,464-477,585-603` | CONFIRMED |

- **Scenario:** every rendered field is HTML-escaped afterwards, so the regex stripping adds no safety
  for templates. It does delete legitimate text from plain-text articles that discuss HTML or JavaScript
  (`<script>…</script>`, `javascript:…`, quoted `on…='…'`), and through A-web-4 that deletion leaks
  into the API. It does not cover the one unescaped sink (WEB-2): unquoted handlers pass `eventRegex`.
- **Evidence:** `TestZZAuditWebConvert`: body `click onclick='go()' here and <script>x</script> end` →
  `Article body PrintSanitized="click  here and  end"`.
- **Fix:** remove `sanitizeText`/`StripDangerousHTML` from the display path and rely on escaping. Effort S.

### A-web-10: sanitized cache keyed by Message-ID only

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-10 | low | security | `models/cache.go:31-53`, `models/sanitizing.go:190-204,479-491`, `cache/sanitized_cache.go:78-82` | CONFIRMED |

- **Scenario:** cache entries are keyed by `MessageID` alone; the `groupName` argument documented as
  "for caching" (`sanitizing.go:190-192`, `:479-480`) is never used. Message-ids are only deduplicated
  per group by the fetcher (`audit-intake.md`: per-group dedupe at `proc_DLArt.go:148`), so two different
  articles with the same id in two groups share one entry for 30 minutes: whichever renders first decides
  the subject, author and body shown for the other. A poster who reuses a target article's Message-ID in
  another fetched group can replace its displayed text (escaped, so content spoofing only). The
  `CacheKey` struct (`models/cache.go:31-35`) is unused. Field-name aliases also drift between the three
  switch statements: `"from"` and `"message_id"` are not cases in `SetField`/`GetField`
  (`sanitized_cache.go:104-158`, `:201-230`), so they are never cached, while `SetField` still adds
  their size to `article.Size` (`:231-242`). `BatchSetArticles` maps `"datestring"` into the same `Date`
  slot as `"date_string"` (`:337`), so the batch format `2006-Jan-02 15:04:05` (`sanitizing.go:657`) and
  the `PrintSanitized` format `Mon, 02 Jan 2006 15:04` (`:223`) overwrite each other, and the date
  shown depends on which path filled the entry first.
- **Evidence:** `TestZZAuditWebSanitizedCacheKey`: `A="legit subject in group A"`,
  `B="legit subject in group A"` (B's own subject is "attacker subject in group B").
- **Fix:** key by group plus article number (or group plus message-id); normalise field names in one map.
  Effort S.

### A-web-11: `/api/thread-tree` is a public JSON API outside the API switch

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-11 | low | security | `web/webserver_core_routes.go:526`, `web/web_threadTreePage.go:28-96`, `web/static/js/thread-tree.js:303-322` | CONFIRMED |

- **Scenario:** the route has no `requireAPIEnabled` and no token (the same class as LO:B11, which only
  fixed the preview). It returns the tree with full `models.Overview` JSON, including the unfiltered
  `from_header` and hidden articles (A-web-1). It sets `Cache-Control: public, max-age=300` (`:95`) even
  when an admin requests an inactive group (admin bypass in `checkGroupAccessAPI`,
  `web_helpers.go:42-48`), so a shared proxy may serve admin-only content to others. No UI calls it: the
  only client, `ThreadTree.loadTreeData` (`thread-tree.js:303`), has no caller. `help.html:181,300` says
  only `/api/v1/stats` is public.
- **Evidence:** `TestZZAuditWebHiddenAndTreeAPI`: `anon GET /api/thread-tree?… (API disabled) -> 200
  … rawEmail=true cache-control="public, max-age=300"`.
- **Fix:** remove the route (and `loadTreeData`), or put it behind `requireAPIEnabled` with
  `Cache-Control: private` and filtered overviews. Effort S.

### A-web-12: AI chat renders messages with `innerHTML`

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-12 | low | security | `web/templates/aichat.html:254,314-326,403,429,455,459` | PLAUSIBLE |

- **Scenario:** `appendMessage` interpolates `content` into `div.innerHTML` (`:326`) for the user's own
  message (`:429`), the model reply (`:455`), server error text (`:459`) and history reloads (`:254`,
  `:403`). Chat history is keyed by user id (`chatHistoryKey(session.UserID, …)`, `web_aichatPage.go:329`; WSH:H11), so the user's own input is self-XSS. Model output
  (Ollama via the proxy) is inserted as markup; a reply that contains HTML, whether prompted by the user
  or produced by a compromised proxy, executes in the user's session.
- **Evidence:** code trace, see Scenario (`aichat.html:314-326`).
- **Missing step:** not run against a real proxy/model.
- **Fix:** render with `textContent` and convert only the `<think>` markers into elements. Effort S.

### A-web-13: no Content-Security-Policy

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-13 | low | security | `web/webserver_core_routes.go:280-296` | CONFIRMED |

- **Scenario:** `secure.Config` sets frame-deny, nosniff, X-XSS-Protection and Referrer-Policy, but no
  `ContentSecurityPolicy`, so WEB-2 and A-web-12 run unrestricted. The templates use inline scripts
  (`base.html:20-30`, `aichat.html`, `admin_crons.html:307-335`), so a CSP needs nonces or moving those
  scripts to `/static`.
- **Evidence:** the repro responses carry `CSP header: ""`.
- **Fix:** add a CSP with `script-src 'self'` plus nonces, after moving inline handlers. Effort M.

### A-web-14: provider create/update report the wrong outcome

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-14 | low | bug | `web/web_admin_provider.go:142-149,221,326-348`, `database/queries.go:79-96,1440-1453,1458-1466` | CONFIRMED |

- **Scenario:** `GetProviderByName` returns `(nil, nil)` when the name is absent and `(p, nil)` when it
  exists. `adminCreateProvider` treats only `err != nil` as "already exists" (`:143-149`), so an existing
  name passes the check and fails later on `UNIQUE(name)` with the generic "Failed to create provider",
  while a real DB error is reported as "Provider already exists". `adminUpdateProvider` gets
  `(nil, nil)` for an unknown id, updates zero rows and flashes "Provider updated successfully". The
  update also requires `name` (`:221`) but `query_SetProvider` has no `name` column
  (`queries.go:79-83`, `SetProvider` `:85-96`), so a rename is silently dropped.
- **Evidence:** code trace, see Scenario (`web_admin_provider.go:143-149,221,327-348`, `queries.go:79-96,1440-1453`).
- **Fix:** check `res != nil` for existence, treat a nil provider as not found, and either write `name`
  or stop requiring it. Effort S.

### A-web-15: article spam/hide counters are never loaded

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-15 | low | bug | `database/queries.go:890,917`, `web/templates/article.html:84-100` | CONFIRMED |

- **Scenario:** `query_GetArticleByNum` and `query_GetArticleByMessageID` do not select `spam`/`hide`,
  so `Article.Spam` and `Article.Hide` (`models/models.go:144-145`) are always 0 on the article page:
  the flag button shows `🚩 0`, the hide button `👁️ 0`, and the admin "decrement spam" form
  (`{{if gt .Article.Spam 0}}`, `article.html:92`) never renders there. The thread page reads overviews,
  which do carry the counters (`thread.html:138,172`).
- **Evidence:** code trace, see Scenario (`queries.go:890,917` select lists; `article.html:86,92,100`).
- **Fix:** select `spam, hide` in both queries (also needed for A-web-1). Effort S.

### A-web-16: admin newsgroup handlers accept unknown or invalid names

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-16 | low | bug | `web/web_admin_newsgroups.go:19-31,42-54,136-252,302-382,764-781` | CONFIRMED |

- **Scenario:**
  - `adminCreateNewsgroup` checks only `name != ""`: no RFC 3977 wildmat/name syntax check, so names with
    spaces, quotes or markup can be created. They are escaped by `html/template`, but they feed the
    WEB-2 `href` sink and make NNTP `LIST` output invalid.
  - `adminUpdateNewsgroup` runs five or six UPDATEs by name and flashes success when the name matches no row
    (`:206-251`).
  - `adminAssignNewsgroupSection` creates a `section_groups` row for any name without checking that the
    group exists (`:357-373`), the orphan class of FU:E1/FU:E9. It also removes only the first existing
    assignment (`break`, `:345`).
  - `buildNewsgroupAdminRedirectURL` appends the raw `search` form value to the Location (`:21-23`):
    `&`/`#` in a search term break the redirect parameters. Header splitting is prevented by net/http.
  - `adminHideFuturePosts` uses a bare `groupDB.DB.Query` without `RetryableQuery` and never checks
    `rows.Err()` (`:765-781`), against the CLAUDE.md conventions.
  - Toggle, migrate-activity, fix-thread-activity and hide-future-posts call `MainDBGetNewsgroup` first,
    which fails for batch-created rows with NULL hierarchy (A-db-1): those groups cannot be managed here.
- **Evidence:** code trace, see Scenario (`web_admin_newsgroups.go:21-23,50-54,206-251,345,357-373,765-781`).
- **Fix:** validate names with the intake regex (or stricter), check RowsAffected/existence, `url.Values`
  for the redirect, `RetryableQuery` plus `rows.Err()`. Effort S.

### A-web-17: cron log filter is dropped for long logs

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-17 | low | bug | `web/web_admin_crons.go:265-281` | CONFIRMED |

- **Scenario:** `adminViewCronJobLog` builds `displayOutput` from the filtered lines (`:268-278`), then,
  when the unfiltered log has more than `limit` lines, overwrites it with the unfiltered tail
  (`:279-281`). With the default `limit=1000` and up to `maxLogLines=25000` lines (`cronjobs.go:48`), the
  filter does nothing on any busy job.
- **Evidence:** code trace, see Scenario (`web_admin_crons.go:268-281`).
- **Fix:** apply the limit to the filtered slice. Effort S.

### A-web-18: parent/reference links do not escape the message-id

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-18 | low | bug | `models/models.go:225-251`, `web/templates/article.html:172`, `web/templates/section-article.html:36` | CONFIRMED |

- **Scenario:** `GetData("parent_URL"|"references_URLs")` formats `/groups/%s/message/%s` with the raw
  group and message-id. RFC 5536 message-ids may contain `/`, `?`, `#` and `%`; `html/template` keeps
  those in a URL attribute, so the link becomes a different path or query and the "Reply to parent"
  button 404s (gin's `:messageId` does not match `/`). The `references_URLs` TODO at `:232` also uses the
  first reference instead of the last.
- **Evidence:** code trace, see Scenario (`models/models.go:232,246`).
- **Fix:** `url.PathEscape` both parts. Effort S.

### A-web-19: cache maintenance goroutines never stop

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-19 | low | robustness | `database/sections_cache.go:18-24,41-66`, `database/db_init.go:179`, `cache/sanitized_cache.go:72,402-404,446-458`, `cache/newsgroup_cache.go:62,289-301,331` | CONFIRMED |

- **Scenario:** `GroupSectionDBCache.CronClean` is a `for { time.Sleep(15s) … }` loop with no exit,
  started by every `OpenDatabase` (`db_init.go:179`). The sanitized and newsgroup caches have `Stop()`
  methods, but nothing in `internal/web` or `cmd/web` calls them. This is harmless at process exit, but
  it violates the CLAUDE.md rule "Goroutines must stop on shutdown" and keeps test binaries' goroutines
  alive.
- **Evidence:** code trace, see Scenario (`sections_cache.go:22,41-66`; no caller of `SanitizedCache.Stop`/`NewsgroupCache.Stop` in `internal/web` or `cmd/web`).
- **Fix:** select on `db.StopChan` in `CronClean`; call the caches' `Stop` from `WebServer.Shutdown`.
  Effort S.

### A-web-20: a cron job disabled at runtime never runs again until restart

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-web-20 | low | bug | `web/cronjobs.go:345-350,384-388` | CONFIRMED |

- **Scenario:** `runJobScheduler` re-reads the job at the top of each cycle and returns when it is no
  longer enabled (`:384-388`), but leaves the `*CronJob` in `cm.jobs`. After the admin toggles the job
  back on, `loadAndStartJobs` calls `startJob`, which answers `ErrCronExists` (`:349-350`) and is
  skipped silently, so the job stays dead until the web server restarts. The same applies after `stop`
  on an idle job (see WEB-1).
- **Evidence:** code trace, see Scenario.
- **Fix:** delete the job from `cm.jobs` (under `cm.mutex`) whenever its scheduler returns. Effort S.

## Route guard table

Admin and moderation routes from `web/webserver_core_routes.go:427-498`. All POSTs pass
`http.CrossOriginProtection` (`webserver.go:59-62`); requests without `Origin`/`Sec-Fetch-Site` (curl)
are not checked by it, so the session cookie is the real guard. "rAA" = `requireAdminAuth`
(`web_utils.go:112-128`: session plus `isAdminRequest`). "WSH/LO/FU" marks handlers a merged plan changed.

| Route (line) | Handler (file:line) | Guard | Extra gate | Existence/validation | Notes / finding |
|----|----|----|----|----|----|
| GET /admin (427) | `adminPage` web_adminPage.go:19 | rAA | – | – | WSH/LO |
| POST /admin/users{,/update,/delete} (428-430) | web_admin_userfuncs.go:32,145,245 | rAA | – | – | WSH; not re-audited |
| POST /admin/aimodels{,/update,/delete,/sync} (431-434) | web_admin_ollama.go:45,106,173,208 | rAA | – | – | LO:F12 |
| POST /admin/newsgroups (435) | `adminCreateNewsgroup` web_admin_newsgroups.go:34 | rAA | – | exists check; **no name syntax check** | A-web-16 |
| POST /admin/newsgroups/{edit,update} (436-437) | `adminUpdateNewsgroup` :127 | rAA | – | **none** (success for unknown name) | A-web-16 |
| POST /admin/newsgroups/delete (438) | `adminDeleteNewsgroup` :256 | rAA | – | `deleted` flag | FU:B3, FU:E10 |
| POST /admin/newsgroups/assign-section (439) | :302 | rAA | – | **none** for the group | A-web-16 |
| POST /admin/newsgroups/toggle (440) | :385 | rAA | – | `MainDBGetNewsgroup` | A-db-1 |
| POST /admin/newsgroups/migrate-activity (441) | :505 | rAA | – | `MainDBGetNewsgroup` | A-db-1 |
| POST /admin/newsgroups/hide-future-posts (442) | :730 | rAA | – | `MainDBGetNewsgroup` | A-db-1, A-web-16 |
| POST /admin/newsgroups/fix-thread-activity (443) | :564 | rAA | – | `MainDBGetNewsgroup` | A-db-1 |
| POST /admin/newsgroups/bulk-{enable,disable,delete} (444-446) | :429,434,439 | rAA | – | inactive-only delete | FU:E9, FU:E13 |
| POST /admin/providers (447) | `adminCreateProvider` web_admin_provider.go:16 | rAA | – | inverted exists check | A-web-14 |
| POST /admin/providers/update (448) | :186 | rAA | – | nil provider = success | A-web-14 |
| POST /admin/providers/delete (449) | :427 | rAA | – | none (0 rows = success, `queries.go:71-77`) | – |
| POST /admin/apitokens (450) | `adminCreateAPIToken` web_admin_apitokens.go:24 | rAA | – | expiry parsed as UTC midnight (token dies at the start of the chosen day) | plain token only in the flash; DB keeps SHA-256 (`db_apitokens.go:37-40`) |
| POST /admin/apitokens/{toggle,delete} (451-452) | :81,124 | rAA | – | none | – |
| POST /admin/sitenews{,/update,/delete,/toggle} (453-456) | web_admin_sitenews.go:16,68,136,166 | rAA | – | update checks id | content rendered escaped (`news.html:24`) |
| POST /admin/nntpusers (457) | `adminCreateNNTPUser` web_admin_nntp.go:47 | rAA | – | random 64-bit user/pass | A-web-5 (default web_user_id=1) |
| POST /admin/nntpusers/update (458) | :130 | rAA | – | password 1..16 chars | A-web-5; SEC-6 (cache not invalidated) |
| POST /admin/nntpusers/{delete,toggle,enable} (459-461) | :210,246,300 | rAA | – | delete: none | SEC-6 (deactivate covered by LO:F22) |
| POST /admin/sections{,/update,/delete} (462-464) | web_admin_sections.go:22,93,173 | rAA | – | name uniqueness; no path-safety check on `name` | refreshes cache (WSH:C3) |
| POST /admin/sections/{assign,unassign} (465-466) | :216,349 | rAA | – | `%` pattern goes to LIKE | – |
| POST /admin/cache/clear (467) | web_admin_cache.go:11 | rAA | – | – | – |
| POST /admin/hierarchies/update (468) | `adminUpdateHierarchies` web_hierarchiesPage.go:102 | own check (session + `GetUserByID` + `isAdmin`) | – | – | answers JSON 401/403 but redirects on success/error (inconsistent, no security impact) |
| POST /admin/hierarchies/restore-descriptions (469) | :296 | rAA | – | – | – |
| POST /admin/settings (470) | web_admin_settings_unified.go:27 | rAA | – | per-key validators | LO |
| POST /admin/crons/create (471) | `adminCreateCronJob` web_admin_crons.go:18 | rAA | **missing `CronEdit`** | – | **WEB-1** |
| POST /admin/crons/update (472) | :72 | rAA | `CronEdit` | – | – |
| POST /admin/crons/toggle (473) | :148 | rAA | **missing `CronEdit`** | – | **WEB-1**, A-web-20 |
| POST /admin/crons/delete (474) | :176 | rAA | `CronEdit` | – | – |
| POST /admin/crons/stop (475) | :307 | rAA | `CronManager != nil` | – | WEB-1 (low impact: SIGINT to a running job, or cancels an idle job until restart) |
| GET /admin/cronjobs/viewlog/:id (476) | :210 | own check (session + `isAdmin`) | `CronManager != nil` | – | text/plain; A-web-17 |
| POST /admin/postqueue/delete (477) | web_admin_postqueue.go:12 | rAA | – | none | – |
| POST /groups/:g/articles/:n/spam (495) | `incrementSpam` webgroupPage.go:134 | session; non-admins need active group | – | per-user flag (atomic) | WSH:S3 |
| POST /groups/:g/articles/:n/hide (496) | `incrementHide` webgroupPage.go:225 | own check (session + `isAdmin`) | – | no group check (admin) | hide is not enforced on reads: A-web-1 |
| POST /groups/:g/articles/:n/decrement-spam (497) | `decrementSpam` web_admin_spam.go:16 | rAA | – | no group check (admin) | – |
| POST /groups/:g/articles/:n/unhide (498) | `unhideArticle` web_admin_spam.go:83 | rAA | – | no group check (admin) | – |

Admin handlers that open a group DB without an existence check (hide, unhide, decrement-spam) are admin-only
and are the web side of SEC-2 that `audit-db.md` classifies under WSH:C4.

Public API surface (non-admin):

| Route | Gate | Data | Notes |
|----|----|----|----|
| GET /api/v1/stats (386-387) | `requireAPIEnabled` | group counts | LO:B4 |
| GET /api/v1/groups/:g/articles/:n/preview (391) | `requireAPIEnabled` | raw body | WEB-2, A-web-1 |
| GET /groups/:g/articles/:n/preview (502) | none (`checkGroupAccessAPI`) | raw body | **WEB-2**, A-web-1 |
| /api/v1/groups… (393-405) | `requireAPIEnabled` + `APIAuthRequired` | full `models.Article` JSON (headers, raw From, path) | same groups as anonymous pages (active only); WSH:C1, LO:B6 |
| GET /api/thread-tree (526) | none (`checkGroupAccessAPI`) | `models.Overview` JSON | **A-web-11** |

## Sink table

| Sink | Location | Data source | Escaping | Reachable by | Verdict |
|----|----|----|----|----|----|
| `contentDiv.innerHTML` (preview body) | `static/js/thread-tree.js:522,539` | preview JSON `body` = `ConvertToUTF8(BodyText)` | none; entities are decoded server-side | any article author | **WEB-2** |
| `href="/groups/${article.group}/…"` in that `innerHTML` | `thread-tree.js:515,525` | route param `group` (must be an existing active group) | none | admin-created names (A-web-16) | WEB-2 (secondary) |
| `${article.subject}`/`${article.from}` in an HTML comment | `thread-tree.js:532-533` | `PrintSanitized` | HTML-escaped (`-->` becomes `--&gt;`) | – | safe |
| `innerHTML` with static strings | `thread-tree.js:340,492,502,506` | literals | n/a | – | safe |
| `div.innerHTML` in `appendMessage` | `web/templates/aichat.html:326` | user input, model reply, history | none | the user / model output | A-web-12 |
| `div.innerHTML` in `fixPreRenderedHTML` | `aichat.html:275-281` | server-rendered escaped history, only `&lt;br&gt;` restored | escaped | – | safe |
| `document.write` theme link | `web/templates/base.html:29`, `base_chat.html:28` | `localStorage['preferred-theme']` | none | only script already on the origin | info |
| `template.HTML` from `GetThreadTreeHTML` | `database/tree_view_api.go:152-173,176-255` | group name, subject, author, ints | `html.EscapeString`, `url.PathEscape`, `PrintSanitized` | – | safe (WSH:S2, `TestW1APIThreadTreeHTMLEscapesGroup`) |
| `template.HTML(html.EscapeString(...))` | `models/sanitizing.go:249,535,643-669,716-735` | article/overview fields | escaped | – | safe (gosec G203 false positive); content correctness: A-web-6…A-web-10 |
| `{{.Article.PrintSanitized …}}` in templates | `article.html:156` and the listing templates (`subject`, `fromheader`, `bodytext`, `references`, `messageid`, `path`, `headers_json`, `date_*`) | cached `template.HTML` | escaped | – | safe; cache poisoning changes text only (A-web-10) |
| URL attributes starting with an action | `hierarchy_tree.html:12,41,173` (`.Path`, `.ParentPath`), `article.html:172`, `section-article.html:36` (`GetData "parent_URL"`) | route params, References | html/template URL filter (blocks `javascript:`) | – | safe; broken links: A-web-18 |
| JS string contexts in templates | `aichat.html:226-233`, `admin_newsgroups.html:283` (`confirm('…{{.Name}}…')`) | model names, group names | html/template JS escaping | – | safe |
| `data-*` attributes read via `.value` | `admin_crons.html:58-63,307-335` | cron name/command | attribute-escaped; assigned to `.value` | – | safe |
| `<title>{{.Title}}` | `base.html:7`, `base_chat.html:7` | `GetCleanSubject` (unescaped string) | `Title` is a `string` now, so auto-escaped | – | safe (WSH:S2 second half fixed) |
| `c.String` with user input | `web_articlePage.go:67,112`, `web_admin_crons.go:286-301` | group, message-id, cron fields | text/plain + `nosniff` | – | safe |
| Flash messages with user input | `admin_modular.html:29,36`, `profile.html:20,26` | group/section/provider names, NNTP and API credentials | escaped | admin only | safe; credentials live in the in-memory flash map until read (`web_auth.go:73-93`) |

## Function inventory

| function/file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `ArticlePreview.escapeHtml` (`thread-tree.js:557`) | | | x | | | no caller; the preview needed it (WEB-2) |
| `ArticlePreview.getAPIToken` (`thread-tree.js:563-567`) | x | | x | | | returns `''`, no caller |
| `ThreadTree.loadTreeData` (`thread-tree.js:303`) | | | x | | | only client of `/api/thread-tree` (A-web-11) |
| comment "Data from API is already sanitized" (`thread-tree.js:520-521`) | | | | x | | false for `body` (WEB-2) |
| `getArticlePreview` comment (`web_apiHandlers.go:486-487`) | | | | x | | "JS will handle HTML context": it does not |
| `SanitizedCache.evictOldest` (`cache/sanitized_cache.go:420`) | | x | | | | staticcheck U1000; A-web-2 |
| `SanitizedCache.xxxhashMessageID` (`:407`) | | x | | | | U1000 |
| `SanitizedCache.Stop`, `NewsgroupCache` stop channel | | | x | | | never called (A-web-19) |
| `models.CacheKey` (`models/cache.go:31-35`) | | | x | | | key is message-id only (A-web-10) |
| `models.DisableSanitizedCache` (`models/cache.go:12`) | | | | x | | "compile-time" toggle defaults to true but `cmd/web/main.go:91` sets false |
| `Article/Overview.PrintSanitized` `groupName` parameter (`sanitizing.go:193,481`) | | | x | x | | documented "for caching", ignored (A-web-10) |
| `models.splitDomainFromEmail` (`sanitizing.go:261`) | | x | | | | U1000; caller commented out (`:441-456`) |
| `filterMailDomainInFromHeader` (`sanitizing.go:418`) | | | | x | x | name says "filter mail domain"; truncates raw header (A-web-6) |
| `StripDangerousHTML` / `sanitizeText` (`sanitizing.go:464,585`) | | | | x | x | mutates shared model, strips plain text (A-web-4, A-web-9) |
| `ConvertToUTF8` (`sanitizing.go:36`) | | | | x | x | used for bodies, decodes QP/2047 unconditionally (A-web-8) |
| `database.SanitizedArticleCache`, `SanitizedFields` (`database/sanitize_cache.go`) | | x | | | | types without users |
| `WebServer.requireAdminAuthJSON` (`web_utils.go:132`) | | x | | | | U1000 |
| `referencesAnyInThread` (`web_utils.go:185`) | | x | | | | U1000 |
| `WebServer.adminCleanupExpiredTokens` (`web_admin_apitokens.go:159`) | | x | | | | no route |
| `WebServer.SectionsHandler` (`web_admin_sections.go:16`) | | x | | | | no route; wraps `adminPage` |
| `adminCreateProvider` exists check (`web_admin_provider.go:143`) | | | | | x | A-web-14 |
| `adminUpdateNNTPUser` `web_user_id` branch (`web_admin_nntp.go:199-203`) | x | | | | | empty block, success reported (A-web-5) |
| `adminViewCronJobLog` filter (`web_admin_crons.go:268-281`) | | | | | x | A-web-17 |
| `generateDisplayName` (`web_admin_ollama.go:334`) | | | | | x | `word[:1]` byte slice, mangles a non-ASCII first letter (admin-only, cosmetic) |
| `GroupSectionDBCache.CronClean` (`database/sections_cache.go:41`) | | | | | x | no exit (A-web-19) |
| `Article.GetData` (`models/models.go:218`) | | | | | x | unescaped URL parts (A-web-18) |

## Seed items rejected

None. WEB-1, WEB-2 and INT-7 are CONFIRMED. Corrections to the seed text:
- WEB-1: stop (`:307`) is ungated as the seed says, but it can only interrupt a running job or cancel
  an idle one's future runs until restart, never start one, so the high rating rests on create and
  toggle. Toggle starts a job only if the manager has not registered it since startup (A-web-20).
- WEB-2: the sink lines at b962f3a are `thread-tree.js:515` (concatenation), `:522`/`:539`
  (`innerHTML`) and `:557` (`escapeHtml`, defined, never called). Entity decoding by
  `html.UnescapeString` makes the issue worse than the seed states (encoded payloads become live).
  `sanitizeText` is not a mitigation (A-web-9).
- INT-7: the seed list of stored headers is accurate (`web_sitePostPage.go:361-371`); in addition the
  stored `Bytes` value is wrong (`:360`).

## Not covered / follow-ups

- **Not run:** a browser execution of WEB-2 (the JSON body and the `innerHTML` sink were shown; script
  execution follows from the DOM API). AI chat against a real proxy (A-web-12). Memory growth of A-web-2
  was not measured. `cmd/web` was not started; all repros went through `w0Srv.Router` with `httptest`,
  which bypasses `rootHandler` (CrossOriginProtection) as the existing tests do.
- **Only skimmed (merged plans' files):** `web_auth.go`, `web_login.go`, `web_registerPage.go`,
  `web_profile.go` (outside the NNTP reset), `web_admin_userfuncs.go`, `web_admin_ollama.go`,
  `web_admin_settings_unified.go`, `web_aichatPage.go`, `web_sectionsPage.go`, `web_threadPage.go`,
  `db_sessions.go`, `db_sections.go`, `tree_cache.go` (structure only).
- **Design decisions for the fix plan (`audit-delta-fixes.md`):** what `hide` means for non-admins
  (A-web-1); whether `/api/thread-tree` stays (A-web-11); whether the From filter should hide addresses
  at all, given the full headers panel (A-web-6); whether `-edit-cronjobs` should also gate
  enabling/disabling or only enabling (WEB-1). The first-registered-user-becomes-admin rule is
  documented (`README.md:70`) and was not reported, but with WEB-1 it means that an install that is
  reachable before the operator registers gives shell access to whoever registers first.
- **Tests to add with the fixes:** a web test that the preview response body and the rendered preview are
  escaped (WEB-2); `CronEdit=false` refuses create/toggle-on (WEB-1); hidden articles 404 for anonymous
  users (A-web-1); `-race` concurrent render of one article (A-web-3); cache bound (A-web-2);
  `PrintSanitized` with long UTF-8 and encoded subjects and From values (A-web-6, A-web-7); a web post
  served over NNTP carries Message-ID/Date/Subject/Path (INT-7, `cmd/nntp-server` e2e).
