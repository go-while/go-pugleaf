# audit-delta-fixes: re-anchor of the optional findings (waves O1/O2), 2026-10-15

Written by five read-only agents at HEAD faec07e (`plan-adf-optional`, = `testing-001` after af235b3).
The audit documents (`docs/audit/audit-*.md`, commit 84c2081) stay the historical record; their line numbers
are stale for the files rewritten by the intake, client-transfer, rfc-commands and audit-delta-fixes plans.
Implementers of the O1/O2 slices read their slice here first, then the audit block for the full scenario.

Status: 31 ids OPEN, PARTLY FIXED: A-tools-12, A-tools-20, A-web-16, A-web-19, A-ops-17 (partly invalid:
`build_TestMsgIdItemCache.sh` is already deleted). None FIXED, none INVALID.


---

# Report: web

# Re-anchor report: web slices of wave O1 (HEAD faec07e, branch plan-adf-optional)

Read-only. Checked: `git log/diff 84c2081..HEAD` on every anchored file, current code by function name,
`go test -count=1 -run TestAdfSan ./internal/models/ ./internal/cache/` (both ok at HEAD).
Note: `internal/web/web_admin_newsgroups.go` and `web/templates/aichat.html` are unchanged since 84c2081,
so the audit's line numbers still hold for them.

## adf-opt-sanitize

Context: wave 1 (a8e24bf, 9c31f37, merge 15e0b34) fixed A-web-2/3/4 only. It bounded the cache, read GetField
under article.mux and stopped the Article write-back through `sanitizedFields()` plus the per-field helpers
`sanitizedSubject/FromHeader/BodyText/References`. Those helpers still call `sanitizeText` and
`filterMailDomainInFromHeader`, so none of A-web-6..10 is done. The **Overview** path (`Overview.PrintSanitized`,
`BatchSanitizeOverviews`) still writes sanitized values into the shared `*Overview`, which is the A-web-4 pattern
again; overviews are shared through the tree cache (`database/tree_cache.go:113`). The A-web-6/9 fix removes that
write as a side effect.
`models.DisableSanitizedCache` defaults to true, but `cmd/web/main.go:89` and `internal/web/testmain_test.go:88`
set it to false, so the cache is live in production and in the web tests.

### A-web-6 — OPEN (low)
- Anchors: internal/models/sanitizing.go:filterMailDomainInFromHeader:L416-L459; :cleanFromHeaderName:L380-L414; :SplitStringForLastSpace:L359-L377; :sanitizedFromHeader:L487-L489; :StripDangerousHTML:L500-L513 (L506); :Overview.PrintSanitized:L517-L579 (write at L531); :BatchSanitizeOverviews:L718-L778 (write at L739). The From is decoded later, at L244 / L568 / L679 / L752. The dead `splitDomainFromEmail` is at L259-L280.
- Done already: none. Byte-truncation to 16 runs before RFC 2047 decoding, unchanged since 84c2081.
- Fix now: write a `displayFrom(raw string) string`. It decodes RFC 2047 first (the header decoder of A-web-8), then calls `net/mail.ParseAddress`. It takes `Name`, or for the `addr (Name)` form the comment, and otherwise the local part. Fall back to `cleanFromHeaderName` on the decoded string. Truncate by runes (32 + "..."). `sanitizedFromHeader`, the Overview path and `StripDangerousHTML` all use it. Remove the 16-byte cuts.
- Test idea: table test with the four audit inputs: `=?UTF-8?Q?J=C3=BCrgen_M=C3=BCller?= <j@test.invalid>` → "Jürgen Müller", `=?ISO-8859-1?Q?J=FCrgen?=` → "Jürgen", `j@test.invalid (Jürgen Müller)` → "Jürgen Müller", and an output that never contains "@".

### A-web-7 — OPEN (low)
- Anchors: sanitizing.go:truncateSubject:L642-L647 (byte slice `subject[:128]`). Callers: Article.PrintSanitized:L193-L255 (L216); Overview.PrintSanitized (L540); Article.GetCleanSubject:L583-L599 (L593); Overview.GetCleanSubject:L603-L619 (L613); BatchSanitizeArticles:L651-L714 (L676); BatchSanitizeOverviews (L749).
- Done already: none.
- Fix now: at every call site, decode first (header decoder) and truncate afterwards. `truncateSubject` becomes rune-based (`utf8.RuneCountInString` / `[]rune`, 128 + "..."). `truncateRunes` lives in `internal/web/web_apiHandlers.go:L52` and models cannot import web, so add a local rune helper in sanitizing.go.
- Test idea: 133-byte subject "Grüße x…" → valid UTF-8, no "Ã"; a 172-byte `=?UTF-8?B?…?=` subject → decoded text, no "=?UTF-8?B?".

### A-web-8 — OPEN (low)
- Anchors: sanitizing.go:ConvertToUTF8:L36-L63 (unconditional `DecodeHeader` L39, `decodeQuotedPrintable` L45, `html.UnescapeString` L48, whole-string Latin-1 fallback L51-L62); :decodeQuotedPrintable:L65-L72. Body uses: PrintSanitized "bodytext" L233-L234 → L244; BatchSanitizeArticles L682. Callers outside the owned files: `internal/web/web_apiHandlers.go:514` (preview body) and `internal/web/web_sitePostPage.go:103-115` (reply quoting of subject/body/from).
- Done already: none.
- Fix now: split into `decodeHeaderText` (RFC 2047 + entity decode + per-sequence repair) and `decodeBodyText(body, headers)`. The body decoder applies QP or base64 only when the `Content-Transfer-Encoding` in `a.HeadersJSON` says so. Note that `a.HeadersJSON` is the merged raw header block (`MultiLineHeaderToMergedString`), not JSON, and the body is stored raw (`nntp-client-commands.go:888`). It decodes the `Content-Type` charset through the existing `decodeCharsetToUTF8`, and on invalid UTF-8 repairs per invalid byte sequence (Latin-1 per bad rune) instead of re-decoding the whole string. Keep `ConvertToUTF8` exported for the two outside callers, as is or as a thin wrapper.
- Constraint: `internal/web/adf_xss_test.go:TestAdfXSSPreviewIsPlainJSON` expects `&lt;svg onload=alert(2)&gt;` in the preview body to come out as `<svg onload=alert(2)>`. That only works while the preview path (`ConvertToUTF8`) keeps `html.UnescapeString`. Do not change the entity decoding of `ConvertToUTF8`, or update that test (it is not owned).
- Test idea: `"x =41 y"` and `"soft=\nbreak"` unchanged without a QP CTE header; the same strings decoded with `Content-Transfer-Encoding: quoted-printable`; `"ok utf8 ü plus latin1 \xfc"` → "ok utf8 ü plus latin1 ü".

### A-web-9 — OPEN (low)
- Anchors: sanitizing.go regex vars L26-L29; :sanitizeText:L621-L639. Display-path uses: sanitizedSubject/FromHeader/BodyText/References L485-L493; sanitizedFields L473-L481 (DateString L478); Overview.PrintSanitized L529-L535; GetCleanSubject L593 and L613; BatchSanitizeOverviews L737-L743. `StripDangerousHTML` (L500-L513) has no production caller left (grep: tests only).
- Done already: none. 9c31f37 only split the call per field; the output is byte-identical.
- Fix now: remove `sanitizeText` from every display helper and from both GetCleanSubject functions, and rely on `html.EscapeString` / `html/template`. Remove the write-back block in `Overview.PrintSanitized` (L529-L535) and in `BatchSanitizeOverviews` (L736-L743), and compute into locals as the Article path does. Then either remove `StripDangerousHTML`, `sanitizeText` and the four regexes, or keep `StripDangerousHTML` documented as non-display.
- Collision: `internal/models/adf_sanitizing_test.go:TestAdfSanitizeNoWriteBack` (wave 1) asserts the stripped values `"body  end"` and `"subj  tail"` (L61, L65, L104-L108) and compares against `StripDangerousHTML` (L74-L84). It fails as soon as A-web-9 lands. It must be updated by this slice, so it has to be added to the owned files.
- Test idea: a body `click onclick='go()' here and <script>x</script> end` renders escaped and complete (`&lt;script&gt;x&lt;/script&gt;`); an Overview is unchanged after PrintSanitized and BatchSanitizeOverviews.

### A-web-10 — OPEN (low)
- Anchors: internal/models/cache.go:CacheKey:L32-L35 (unused); :GetCachedSanitized:L38-L43; :SetCachedSanitized:L46-L53; :BatchSetCachedSanitized:L56-L63 and :GetCachedSanitizedArticle:L66-L71 (both have no callers). sanitizing.go: Article.PrintSanitized L193 (`groupName ...string` ignored; cache get L200-L204, set L250-L252); Overview.PrintSanitized L517 (L523-L527, L574-L576); BatchSanitizeArticles L651 (skip check L664, `batch[MessageID]` L705, date format `"2006-Jan-02 15:04:05"` under key "datestring" L691-L696); BatchSanitizeOverviews L718 (L732, L762-L771). internal/cache/sanitized_cache.go: GetField:L79-L131, SetField:L153-L216, SetArticle:L219-L273 and BatchSetArticles:L276-L344. These four are separate alias switches: GetField/SetField/SetArticle have no "from", "from_header", "message_id", "body_text" or "datestring". SetField adds the size of an unknown field (L205-L207).
- Done already: none. a8e24bf rewrote sanitized_cache.go (LRU bound, GetField lock) but kept the message-id key and the switches. GetField now treats an unknown field as a miss.
- Fix now:
  1. Build the key in models as `group + "\x00" + messageID`, with `groupName[0]`, and skip caching when the group is empty. The cache API keeps its plain string key, so `internal/cache/adf_sanitized_cache_test.go` stays valid.
  2. Add one `canonicalField(field)` map in sanitized_cache.go and use it in all four switches. An unknown field is not stored and its size is not counted.
  3. The batch sanitizers emit "date_string" in PrintSanitized's format `Mon, 02 Jan 2006 15:04`.
  4. Remove `CacheKey` or use it.
- Batch callers: `BatchSanitizeArticles` and `BatchSanitizeOverviews` take no group. Give them a variadic `groupName ...string`, which keeps the signature compatible. Without a group they do nothing, because their prefill would otherwise sit under a key that PrintSanitized never reads. For the prefill to keep working, add the one-line caller changes (pass the group) to the owned files: `internal/web/web_threadPage.go:153`, `internal/web/web_articlePage.go:83,145`, `internal/web/web_groupThreadsPage.go:59` and `internal/database/tree_cache.go:113`. No O1 slice owns these files.
- Collision: TestAdfSanitizeNoWriteBack (L100-L110) reads `sc.GetField(art.MessageID, …)` after `BatchSanitizeArticles`. The new key breaks it, which is the same file as under A-web-9.
- Test idea: two Articles with the same Message-ID but different subjects, rendered with group A and then group B, each show their own subject. Fields "from" and "fromheader" hit the same slot. The batch date and the PrintSanitized date are identical.

### Slice notes
- Owned files, corrected list:
  - Keep: `internal/models/sanitizing.go`, `internal/models/cache.go`, `internal/cache/sanitized_cache.go`, `internal/models/adf_opt_sanitize_test.go` (new).
  - Add: `internal/models/adf_sanitizing_test.go`, because its wave-1 assertions encode the A-web-9 stripping and the message-id key and must be rewritten.
  - Add for A-web-10, one line each: `internal/web/web_threadPage.go`, `internal/web/web_articlePage.go`, `internal/web/web_groupThreadsPage.go`, `internal/database/tree_cache.go`.
  - Optional: `internal/web/web_apiHandlers.go:514` and `internal/web/web_sitePostPage.go:103-115` (body decoder by CTE). If they are not taken, leave them on `ConvertToUTF8` and record them as follow-ups.
- Existing tests that must keep passing: `internal/cache/adf_sanitized_cache_test.go` (no change needed if the cache API stays string-keyed), `internal/web/adf_xss_test.go:TestAdfXSSPreviewIsPlainJSON` (entity decoding in ConvertToUTF8), and the full `./internal/web/...` race run, since it renders From and subject through PrintSanitized.
- Collisions with other O1 slices: none. `adf-opt-web-admin` touches `internal/cache/` only if it makes `NewsgroupCache.Stop` idempotent (see A-web-19). It must not edit `sanitized_cache.go`; that file is owned here.
- Minor, not an id: `SetField` updates `cachedSize` in a `go func` per call (sanitized_cache.go:L211-L215). That is one goroutine per field write; doing it inline is cheap and removes them.

## adf-opt-web-admin

### A-web-12 — OPEN (low, PLAUSIBLE)
- Anchors: web/templates/aichat.html:appendMessage:L314-L340. It builds `<div class="thinking-block">` from `<think>` at L319-L324 and writes `div.innerHTML = …${processedContent}…` at L326. Callers: loadInitialChatHistory:L236-L266 (L254); switchModel:L378-L416 (L403); sendMessage:L418-L466 (L429 user input, L455 `data.reply`, L459 `data.error`, plus L447, L461, L464). Also fixPreRenderedHTML:L272-L290, which re-assigns `div.innerHTML` to turn `&lt;br&gt;` into `<br>`. The server-rendered history at L185-L190 is html/template-escaped, so L272-L290 only re-injects `<br>`; low risk, but it is the same pattern.
- Done already: none in the template. Wave 1 added a CSP (`internal/web/webserver_core_routes.go:230-236`, used at L302), but it keeps `script-src 'self' 'unsafe-inline'`, so an injected `<img onerror=…>` still runs. The CSP does not mitigate this.
- Fix now: in appendMessage, build the "You/AI:" span, the body and the `<hr>` with createElement/textContent. For the assistant role, split the content on `<think>…</think>` and put each think segment into a `div.thinking-block.collapsed` via textContent, keeping the click toggle. Replace fixPreRenderedHTML's innerHTML rewrite with text nodes plus `<br>` elements, or drop it.
- Test idea: GET `/aichat` as a logged-in user (as in `w1_server_test.go:341`). Assert that the served page has no `.innerHTML =` assignment fed by `content` or `processedContent`, and that appendMessage uses `textContent`. The check style is the one in `adf_xss_test.go:TestAdfXSSThreadTreeJSUsesTextContent`.

### A-web-14 — OPEN (low)
- Anchors: internal/web/web_admin_provider.go:adminCreateProvider:L16-L183, existence check L142-L149 (`if err != nil` → "already exists"); :adminUpdateProvider:L186-L424, `name == ""` required at L221, `GetProviderByID` at L327-L332 (a nil provider is accepted, and `existingProvider != nil` guards at L338/L345/L358/L365), success at L415-L422. DB side, not owned and not needed: internal/database/queries.go: `query_SetProvider` L79-L83 (no `name` column), `SetProvider` L85-L97, `GetProviderByName` L1484-L1497, `GetProviderByID` L1502-L1515. Both return `(nil, nil)` for not found.
- Done already: none. The file is unchanged since 84c2081 in these hunks.
- Fix now:
  - Create: `err != nil` → "Failed to check provider" plus a log line; `res != nil` → "Provider already exists".
  - Update: `existingProvider == nil` → "Provider not found", and return before SetProvider.
  - Rename: the edit form already has the name `readonly` (`web/templates/admin_providers.html:132`). Drop `name` from the required check at L221, or reject `name != existingProvider.Name` with an error. Do not change `query_SetProvider`, so no queries.go edit is needed.
  - Per K4, edit without `gofmt -w`. `gofmt -l` lists this file on the baseline (confirmed at HEAD, together with `internal/database/embedded_migrations.go`).
- Test idea: as admin, POST /admin/providers twice with the same name → the second POST flashes "already exists" and leaves one row. POST /admin/providers/update with an unknown id → error flash, no success.

### A-web-16 — PARTLY FIXED (low)
- Anchors (file unchanged since 84c2081, so the audit's line numbers hold): internal/web/web_admin_newsgroups.go:
  - buildNewsgroupAdminRedirectURL:L19-L31 (raw `search` at L21-L23)
  - adminCreateNewsgroup:L34-L124 (only `name == ""` at L50-L54; the existence check at L93-L99 treats any DB error as "does not exist")
  - adminUpdateNewsgroup:L127-L253 (UPDATEs at L206-L249, success at L251 for an unknown name)
  - adminAssignNewsgroupSection:L302-L382 (`break` at L345; GetNewsgroupByName at L356-L360 ignores not-found; CreateSectionGroup at L372 creates an orphan)
  - adminHideFuturePosts:L730-L823 (bare `groupDB.DB.Query` at L765; no `rows.Err()`; scan errors skipped at L774-L778).
- Done already: the A-db-1 sub-point is fixed. 85741cf (adf-db-readers) made `MainDBGetNewsgroup` NULL-safe (`queries.go:217-240`, COALESCE on hierarchy/status/…), so toggle, migrate-activity, fix-thread-activity and hide-future-posts can manage batch-created groups again. The other five sub-points are open.
- Fix now:
  - Create: validate with `common.ValidGroupNameSyntax` (`internal/common/intake.go:113`, the D8 grammar used by intake); treat only `errors.Is(err, sql.ErrNoRows)` as "free" (errors and sql are already imported).
  - Update and assign: before the UPDATEs, `s.DB.MainDBGetNewsgroup(name)` → `sql.ErrNoRows` gives "Newsgroup not found". This is web-only; `UpdateNewsgroup*` in queries.go do not return RowsAffected and are not owned. Assign also drops the `break`.
  - Redirect: build it with `url.Values{search, search_description, ng_page}.Encode()`.
  - Hide-future-posts: `database.RetryableQuery(groupDB.DB, …)`, `defer rows.Close()` before `groupDB.Return()`, check `rows.Err()`, and log scan errors.
- Test idea: create "bad name<x>" → error flash and no row; update or assign for a non-existent group → error flash and no `section_groups` row; a redirect with `search=a&b#c` keeps the value in `Location` (it round-trips through `url.ParseQuery`).

### A-web-18 — OPEN (low)
- Anchors: internal/models/models.go:(*Article).GetData:L220-L257 (`references_URLs` L227-L236 takes refs[0], TODO at L234; `parent_URL` L237-L253 formats the raw group and message-id at L248; local var `url` shadows the package name). Templates: web/templates/article.html:L172 and web/templates/section-article.html:L36, both `{{.Article.GetData "parent_URL" .GroupName}}`. adf-web-hide (af11262, 87a5364) touched these templates but not these lines, so the anchors still hold. `references_URLs` has no template caller.
- Done already: none.
- Fix now: `url.PathEscape(group)` and `url.PathEscape(ref)` in both cases. Use the last reference in `references_URLs`, or delete that case since it is unused. Rename the local `url` variable.
  - Imports: adding "net/url" changes the import block of models.go (L4-…). This is outside "GetData only" but has no collision: intake S1 is merged and no O1 slice owns models.go.
  - Limitation: gin runs with `UseRawPath=false` (`gin.Default()` at webserver_core_routes.go:249; no UseRawPath anywhere). `%2F` is therefore decoded before routing, and a message-id containing `/` still does not match `:messageId`. PathEscape fixes `?`, `#`, `%` and spaces, not `/`. The `/` case would need `router.UseRawPath = true` in webserver_core_routes.go, which is not owned. Record it or extend ownership.
- Test idea: `(&models.Article{References: "<a?b#c%d@x>"}).GetData("parent_URL", "g")` round-trips through `url.PathUnescape` of the last path segment to the original id. Then GET that URL on the test router → 200 for an inserted article with that id. Use `/` only once UseRawPath is decided.

### A-web-19 — PARTLY FIXED (low)
- Anchors:
  - internal/database/sections_cache.go:CronClean:L51-L…: a select on `g.stop`, wired by `newGroupSectionDBCacheStop(db.backgroundStop())` at internal/database/db_init.go:L228. `stopBackground` (db_init.go:L296) is called first in `Database.Shutdown` (database.go:L142).
  - internal/cache/sanitized_cache.go:Stop:L381-L383 (`close(sc.stopCleanup)`, not idempotent), with cleanupLoop at L447-L459.
  - internal/cache/newsgroup_cache.go:Stop:L331-L333 (`close`, not idempotent), with cleanup at L289-L301 and NewNewsgroupCache at L52-L65.
  - internal/web/webserver.go:(*WebServer).Shutdown:L137-L195.
  - The caches are process globals: `models.GetSanitizedCache()` and `models.GetNewsgroupCache()` (models/cache.go:L27, L82), initialised in cmd/web/main.go:343,347 and in internal/web/testmain_test.go:89-90.
- Done already: 3ba6c0d (adf-db-lifecycle, merge a4f2d0d) made `CronClean` stoppable, together with GHmapCron and MemCachedThreads.CleanCron (`NewGHmapStop`, `NewMemCachedThreadsStop`). The sanitized-cache and newsgroup-cache half is open: nothing calls their `Stop`.
- Fix now: at the end of `WebServer.Shutdown`, stop both caches, nil-checked, through a package-level `sync.Once` in internal/web. A per-server once is not enough, for two reasons:
  - `Shutdown` runs its body on every call; only `close(s.stopCh)` is behind `s.stopOnce`.
  - The caches are globals shared by every WebServer. internal/web tests build several servers and call Shutdown several times (lo1_core_test.go:31,141,214,244,266,335; w1_server_test.go:260,300,311), and a second `close` panics.
  - Alternative: make both `Stop` methods idempotent. For `SanitizedCache.Stop` that is a change in a file `adf-opt-sanitize` owns; for `NewsgroupCache.Stop` it is in unowned `internal/cache/newsgroup_cache.go`. A stopped cache still serves Get/Set, so later tests are unaffected.
- Test idea: start a server, call Shutdown twice, and check that there is no panic and that the cleanup goroutines are gone. Poll `runtime.Stack` for `cleanupLoop` / `(*NewsgroupCache).cleanup`, with a deadline of at most 100 ms per poll.

### Slice notes
- Owned files, corrected list:
  - Confirmed: `web/templates/aichat.html`, `internal/web/web_admin_provider.go` (gofmt baseline, K4), `internal/web/web_admin_newsgroups.go`, `internal/web/webserver.go` (`Shutdown` only, plus a package-level `sync.Once` var), `internal/web/adf_opt_admin_test.go` (new).
  - `internal/models/models.go`: `(*Article).GetData` plus the import block ("net/url").
  - Not needed: `internal/database/queries.go`. The A-web-14 and A-web-16 fixes are possible entirely in the handlers.
  - Optional: `internal/web/webserver_core_routes.go` (only `router.UseRawPath = true`, for `/` in message-ids under A-web-18); `internal/cache/newsgroup_cache.go` (only if `Stop` is made idempotent rather than guarded in web).
- Existing tests to keep green: `w1_server_test.go:341-394` (/aichat page and history), `lo1_handlers_test.go:168-191` (aichat clear/history), `fu_queries_test.go:47` (/admin/newsgroups/delete), `adf_xss_test.go:TestAdfXSSCSPHeader` (do not touch the CSP), and the Shutdown tests in lo1_core_test.go and w1_server_test.go (A-web-19 must not panic on repeated Shutdown).
- Collisions and dependencies on other O1 slices:
  - `adf-opt-web-maint` owns `cmd/web/main.go`, so stopping the caches there instead of in `WebServer.Shutdown` would collide. Keep it in Shutdown.
  - `adf-opt-sanitize` owns `internal/cache/sanitized_cache.go`; do not edit `SanitizedCache.Stop` from this slice.
  - Otherwise none.

## Per-id status
- A-web-6: OPEN. The From header is still cut at 16 bytes and cleaned before RFC 2047 decoding.
- A-web-7: OPEN. `truncateSubject` still slices at byte 128 before decoding.
- A-web-8: OPEN. `ConvertToUTF8` still decodes QP/2047 on bodies unconditionally and falls back to Latin-1 for the whole string. The xss preview test depends on its entity decoding.
- A-web-9: OPEN. `sanitizeText` is still in every display helper, and the Overview path still writes back. The wave-1 test `adf_sanitizing_test.go` asserts the stripped output and must join the owned files.
- A-web-10: OPEN. The cache is still keyed by message-id and the four field switches still differ. The batch callers need a group, which means one-line edits in 4 unowned files.
- A-web-12: OPEN. `innerHTML` is still used in appendMessage, and the CSP's `'unsafe-inline'` does not mitigate it.
- A-web-14: OPEN. The fix fits entirely in `web_admin_provider.go`; the edit form already sends the name readonly.
- A-web-16: PARTLY FIXED. The NULL-hierarchy sub-point was fixed by 85741cf. Name syntax, existence checks, the redirect encoding and `RetryableQuery` are open.
- A-web-18: OPEN. PathEscape fixes `?`, `#` and `%`; `/` also needs gin `UseRawPath`, in an unowned file.
- A-web-19: PARTLY FIXED. `CronClean` stops since 3ba6c0d. The sanitized and newsgroup cache `Stop` calls in `Shutdown` are still missing and need a once-guard, because `Stop` panics on a second call.

---

# Report: db

# Re-anchor report: adf-opt-hierarchy, adf-opt-postqueue (HEAD faec07e)

## adf-opt-hierarchy
Only change to `internal/database/hierarchy_cache.go` since 84c2081: 85741cf (adf-db-readers, A-db-11) in
`buildHierarchyTreeBulk` (COALESCE, scan-error log, rows.Err). Everything above L462 keeps its audit line numbers.

### A-db-7 — OPEN (medium)
- Anchors: hierarchy_cache.go:UpdateNewsgroupStats:L261-L306 (L268-L273 GroupCount += / article-vs-Unix compare;
  L275-L302 direct-groups loop; L291-L300 re-sort, "activity" by LastArticle);
  hierarchy_cache.go:buildHierarchyTreeBulk:L581-L587 ("activity" also sorted by LastArticle at warm-up);
  caller db_batch.go:processNewsgroupBatch:L1061-L1065 (`UpdateNewsgroupStats(*task.Newsgroup, len(inserted), maxArticleNum)`,
  called only when the stats UPDATE hit a row; `latestDate` (L951, clamped to now at L1028-L1030, may be zero) is in scope).
- Done already: none in the cache. (Caller side is sound now: d27c46d/bdf0c03 count only newly inserted rows, A-db-3/A-db-4.)
- Extra bug found (same function): the lookup key is `ExtractHierarchyFromGroupName(name)+":"+sortBy` (top level), but
  `addGroupToAllPossibleLevels` (L642-L663) stores a group only under its direct parent (`name[:LastIndex(name,".")]`).
  So groups with 3+ name parts (alt.bikes.foo) are never updated; same bug in UpdateNewsgroupActiveStatus L236-L258.
- Fix now: drop `h.GroupCount += ...`; never compare an article number with a Unix time; update
  `h.LastUpdated` and `group.UpdatedAt` from the batch's latest date (only if non-zero and newer); look up direct groups
  under the parent prefix (`name[:strings.LastIndex(name, ".")]`); sort "activity" by `UpdatedAt` desc in both
  UpdateNewsgroupStats and buildHierarchyTreeBulk (matches getDirectGroupsAtLevelDirect's `updated_at DESC`, queries.go L2155-L2160).
- Is "caller not edited" still sensible? Partly. Without a date argument the cache can only use `time.Now()` (wrong
  for backfills: DB updated_at gets the article date, cache would say "now"; hierarchy LastUpdated is resynced every
  10 min by cmd/web startHierarchyUpdater, group UpdatedAt only at the next warm-up) or leave dates alone (then the
  activity re-sort is pointless). Recommended: add a `latest time.Time` parameter and change the one call at
  db_batch.go:L1064 to pass `latestDate`. No O1 slice owns db_batch.go and adf-db-batch is merged, so no collision.
  If the plan's rule is kept: drop the GroupCount/LastUpdated lines, keep LastUpdated untouched, document it.
- Test idea: fresh NewHierarchyCache with hand-filled hierarchiesMap/directGroupsCache (3-part group under "a.b");
  UpdateNewsgroupStats leaves GroupCount, sets LastUpdated/UpdatedAt from the date, updates the 3-part group, activity order by UpdatedAt.

### A-db-8 — OPEN (medium)
- Anchors: hierarchy_cache.go:GetHierarchySubLevels:L157-L203 (miss → empty, L163-L167);
  GetDirectGroupsAtLevel:L206-L233 (miss → empty, L214-L218); InvalidateHierarchy:L309-L349 (5-min throttle
  L313-L319; prefixes L325-L342 = only the top level, twice); ForceInvalidateHierarchy:L374-L405 (no caller);
  WarmCache:L54-L98 (sets lastInvalidation=now at L92-L94, so every invalidation in the first 5 min after warm-up is dropped).
  Callers (unchanged behaviour, queries.go rewritten around them): InsertNewsgroup L122-L135 (L130-L131),
  UpdateNewsgroup L274-L290 (L285-L286), BulkDeleteNewsgroups L422-L476 (L468-L472), DeleteNewsgroup L511-L570 (L561-L562).
  All pass only a top-level name; InvalidateHierarchy has no *Database.
- Done already: none.
- Fix now (all inside hierarchy_cache.go, queries.go untouched): InvalidateHierarchy marks the top level dirty
  (`hc.dirty[top]`, plus a generation counter) instead of deleting, no throttle; the readers (they already receive
  `db *Database`, unused today) rebuild a dirty top level before reading: one query like buildHierarchyTreeBulk
  restricted with `name >= top||'.' AND name < top||'/'` (no LIKE wildcard issue with '_'), same filter
  `active = 1 AND message_count > 0`, then replace every key `top`, `top.*` in subLevelsCache/subLevelCounts/
  directGroupsCache/directGroupCounts (factor the 3-sort population L566-L599 into a helper shared with the bulk build).
  Don't fall back to getDirectGroupsAtLevelDirect: it returns `empty` when totalCount > pageSize (queries.go L2150-L2153),
  has no message_count filter, uses bare db.mainDB.Query, so its result can't be cached. db == nil → keep today's behaviour.
  Keep ForceInvalidateHierarchy consistent (mark dirty too) or leave it (no caller).
- Test idea: fresh hc + w0DB; insert two groups under a unique top, buildHierarchyTreeBulk, insert a third (incl. a
  3-part name), InvalidateHierarchy twice in a row → GetDirectGroupsAtLevel/GetHierarchySubLevels list all, sub-level "top.x" fresh.

### A-db-10 — OPEN (low)
- Anchors: hierarchy_cache.go:GetHierarchiesPaginated:L101-L154 (L141-L153); GetHierarchySubLevels:L185-L199;
  GetDirectGroupsAtLevel:L220-L232. Web still passes any page > 1 unbounded (web_hierarchiesPage.go L27-L31, L69-L71,
  L204-L207 hierarchyTreePage → GetHierarchySubLevels with page).
- Done already: none (web_sectionsPage.go L123-L128 / web_apiHandlers.go L44-L46 have their own clamps, not reusable from database).
- Fix now: one helper `pageWindow(n, page, pageSize int) (start, end int)`: pageSize <= 0 or n == 0 → (0,0); page < 1 → 1;
  `page > (n+pageSize-1)/pageSize` → (n,n) (empty page, keeps today's "past the end = empty" semantics) before any
  multiplication; use it in all three readers.
- Note: the uncached *Direct readers in queries.go have the same overflow but only produce a negative SQL OFFSET
  (SQLite treats as 0, no panic); out of scope (queries.go not owned).
- Test idea: page = math.MaxInt/10+1 (and page 0, pageSize 0) on all three readers → empty result, no panic.

### A-db-14 — OPEN (low)
- Anchors: hierarchy_cache.go:GetDirectGroupsAtLevel:L206-L233 (returns `groups[start:end]` after RUnlock, L212/L232);
  writers mutating the shared *models.Newsgroup / backing arrays under Lock: UpdateNewsgroupStats L275-L302 (fields +
  in-place sort.Slice), UpdateNewsgroupActiveStatus L242-L255 (`group.Active = active`). GetHierarchiesPaginated
  L109-L122 already deep-copies under RLock (pattern to reuse).
- Done already: none.
- Fix now: in GetDirectGroupsAtLevel compute the window and copy under RLock: `out[i] = &cp` with `cp := *groups[j]`
  (models.Newsgroup is plain values, no pointers/mutex). Then in-place mutation/sort under Lock stays safe.
  (A dirty-rebuild for A-db-8 swaps in new slices under Lock, so it is safe as well.)
- Test idea: `-race` test: one goroutine loops UpdateNewsgroupStats/UpdateNewsgroupActiveStatus on a filled fresh hc,
  readers loop GetDirectGroupsAtLevel and read every field of the result; no race report, no duplicate names per page.

### Slice notes
- Owned files: `internal/database/hierarchy_cache.go`, `internal/database/adf_opt_hierarchy_test.go` (new, no collision),
  plus recommended `internal/database/db_batch.go` (only the UpdateNewsgroupStats call at L1064, for A-db-7's date).
  queries.go not needed with the dirty-rebuild design above.
- Existing test to keep green: internal/database/adf_readers_test.go:TestAdfReadersHierarchyReaders (L211-L291) calls
  `hc.buildHierarchyTreeBulk(db)` and reads `hc.directGroupsCache[top+":name"]` under `hc.mu`: keep that method name/
  signature, the map field name and the "prefix:sort" key format. It also expects a NULL-hierarchy group to be placed
  (rebuild query must COALESCE hierarchy like L469).
- Tests share one DB (testmain_test.go w0DB) whose own db.HierarchyCache is live (NO_CACHE_BOOT false): use a fresh
  NewHierarchyCache() and unique w0Name tops; InsertNewsgroup on the shared DB only touches the shared cache.
- Collisions with other O1 slices: none (no other O1 slice owns hierarchy_cache.go, db_batch.go or queries.go).

## adf-opt-postqueue
No commit touched internal/postmgr/postmgr.go or internal/database/db_post_queue.go since 84c2081; cmd/post-queue/main.go
only 3ce4b56 (SIGTERM in signal.Notify, L75). Audit line numbers for those two files are unchanged; main.go shifted +1.

### A-intake-10 — OPEN (low)
- Anchors (db_post_queue.go, all bare d.mainDB.*): InsertPostQueueEntry:L22-L35 (Exec); GetPendingPostQueueEntries:L38-L102
  (deferred tx Begin, read then UPDATE ... IN, no rows.Err, rows open during UPDATE); MarkPostQueueAsPostedToRemote:L105-L116;
  ResetPostQueueProcessing:L119-L143; ResetAllPostQueueProcessing:L146-L165 (no caller); GetAllPostQueueEntries:L175-L261
  (Query, scan error `continue`, no rows.Err); GetPostQueueStats:L264-L300 (4× QueryRow); DeletePostQueueEntry:L303-L314;
  RetryPostQueueEntry:L317-L328 (no caller anywhere: the "admin presses retry" escape in the audit does not exist);
  CleanupPostedEntries:L333-L369.
  postmgr.go: Run:L33-L59 (shutdownChan only checked between batches; `time.Sleep(10s)` L52-L56 not interruptible);
  Stop:L62-L65 (no caller; double close with Run L40); ProcessPendingPosts:L68-L110 (stop path L86-L93 resets only failed
  IDs, return value of ResetPostQueueProcessing ignored at L91 → errcheck); processEntry:L113-L151.
  cmd/post-queue/main.go:main:L23-L130: `defer db.Shutdown()` L53 (error ignored, StopChan never closed, WG not waited:
  see expire-news shutdown L316-L338 "OpenDatabase adds the batch orchestrators to db.WG"); exits after the DB open:
  log.Fatalf L67, L87, L100; os.Exit L70, L92, L118, L120. One-shot path L114-L122 never consumes shutdownChan, so a
  SIGINT/SIGTERM (the web cron's StopJob sends os.Interrupt, internal/web/cronjobs.go L287-L330) is swallowed.
- Done already: SIGTERM added to Notify (3ce4b56). Nothing else.
- Real trigger today: the stop path L86-L93 is effectively dead (stopCh is closed only by Run between batches, never during
  ProcessPendingPosts), so stuck rows come from kill/crash between select and Mark, and from an un-retried
  ResetPostQueueProcessing failing on SQLITE_BUSY. An un-retried Mark failing after a successful POST resets the row → repost.
- Fix now:
  1. db_post_queue.go: every statement via RetryableExec/RetryableQuery/RetryableQueryRowScan; GetPendingPostQueueEntries
     in RetryableTransactionExec (reset `entries`/`ids` inside the closure, it may run again; close rows and check rows.Err
     before the UPDATE); rows.Err in GetAllPostQueueEntries.
  2. Startup: call `db.ResetAllPostQueueProcessing()` once in cmd/post-queue before ProcessPendingPosts/Run (only one
     post-queue runs at a time: the cron scheduler runs a job sequentially, cronjobs.go runJobScheduler; note the
     assumption in a comment). Stop: ProcessPendingPosts resets failed IDs plus the unprocessed remainder `entries[i:]`, error checked.
  3. postmgr: make stop effective: `stopOnce` for stopCh (Run + Stop), Run closes stopCh from a watcher on shutdownChan
     (or selects on both), sleeps via `select { case <-pm.stopCh: case <-time.After(d): }`; main's one-shot path closes the
     manager's stop on shutdownChan too.
  4. main.go: `func main() { os.Exit(run()) }`, run() returns codes; teardown like expire-news `shutdown` (close StopChan,
     WG.Wait, Shutdown with error checked) after pools are closed; no log.Fatalf/os.Exit after OpenDatabase.
  Not in the plan: attempt counter for 441 (needs a new migration 0030_*, unowned) → leave out, note in issues.
- Test idea: internal/database/adf_opt_postqueue_test.go (w0DB; insert a newsgroups row for the FK, post_queue rows):
  GetPendingPostQueueEntries marks them, ResetAllPostQueueProcessing / ResetPostQueueProcessing return them to pending,
  Mark sets posted; optional internal/postmgr test: stop closed before ProcessPendingPosts with nil pools → all selected rows back to in_processing=0
  (needs its own OpenDatabase on a temp dir with NO_CACHE_BOOT=true).

### Slice notes
- Owned files: plan list is incomplete. Correct list: `internal/database/db_post_queue.go`, `cmd/post-queue/main.go`,
  `internal/postmgr/postmgr.go` (needed: remainder reset on stop, errcheck at L91, stop wiring/stopOnce/interruptible sleep),
  `internal/database/adf_opt_postqueue_test.go` (new), optional `internal/postmgr/adf_opt_postqueue_test.go` (new;
  package has no tests and is not in the CLAUDE.md `go test -race` list: run `./internal/postmgr/...` explicitly).
- No existing tests reference post_queue except lo2_pool_bench_test.go (copies the INSERT text into its own const; unaffected).
- Collisions with other O1 slices: none (postmgr.go, db_post_queue.go, cmd/post-queue not owned elsewhere). Web admin
  readers (web_adminPage.go L415/L423, web_admin_postqueue.go L35) keep their signatures.

## Status summary
- A-db-7: OPEN (plus 3+-part group lookup bug in the same function; recommend owning the one caller line db_batch.go:L1064)
- A-db-8: OPEN (fix fully inside hierarchy_cache.go via dirty-mark + per-top rebuild in readers)
- A-db-10: OPEN
- A-db-14: OPEN
- A-intake-10: OPEN (only SIGTERM done in 3ce4b56); postmgr.go must be added to the owned files

---

# Report: tools1

# Re-anchor report: adf-opt-analyze, adf-opt-tcp2tor, adf-opt-rslight (HEAD faec07e)

Changes since 84c2081 in these files: `internal/processor/analyze.go` only 83fdee3 (OPS-1b: printAnalysisResults
returns error, no os.Exit); `cmd/tcp2tor/main.go` only 8164555 (OPS-4c: socksForwardDialer, dialThroughSOCKS,
Timeout read); `cmd/nntp-analyze/main.go`, `cmd/rslight-importer/main.go`, `internal/processor/rslight.go`,
`internal/processor/rslight_articles.go`, `cmd/tcp2tor/example.cfg` unchanged. Existing tests
`TestLo1Paths*`, `TestAdfProc*` (processor) and `./cmd/tcp2tor` (-race) pass at HEAD.

## adf-opt-analyze

### A-tools-7 — OPEN (medium)
- Anchors: internal/processor/analyze.go:getCacheFilePath:L495-L501 (`dataDir := "data"` fallback when proc.DB nil);
  analyze.go:AnalyzeModeStandalone:L884-L922 (`&Processor{Pool: pool}`, L911-913, no dataDir param);
  cmd/nntp-analyze/main.go:main:L126-L211 (`-data` L152 passed only to analyzeAllGroups L186-188; single-group call
  L206-208, groups-file call L176-178); main.go:analyzeAllGroups:L259-L437 (opens `db` L273-279, proc L324-326 without
  DB); main.go:analyzeGroupsFromFile:L440-L590 (proc L479-481); dead main.go:readGroupsFromFile:L214-L256 (unreachable,
  groups-file handled at L175 first; also calls AnalyzeModeStandalone).
- Done already: none (LO:A3 00fffba added analyzeCachePath/getCacheFilePath with the fallback; 83fdee3 untouched it).
- Fix now: give the analyzer an explicit cache root: new field on Processor (e.g. `AnalyzeDataDir string`, in
  processor.go struct L24-34) or a dataDir param; getCacheFilePath uses it, else proc.DB.GetDataDir(), else returns an
  error (drop `"data"`; callers L152,L178,L391,L516,L593,L613,L638,L696 handle the error). `$all`: set `proc.DB = db`.
  Thread `*dataDir` into AnalyzeModeStandalone and analyzeGroupsFromFile; delete dead readGroupsFromFile.
- Test idea: Processor with only the cache root set (no DB, no Pool) -> getCacheFilePath under t.TempDir(); nil/no root -> error.
- COLLISION: internal/processor/lo1_paths_test.go:TestLo1PathsGetCacheFilePathNilDB:L48-L56 asserts the `"data"`
  fallback; it fails once the fallback is removed. The slice must own lo1_paths_test.go (change that test to expect the
  error) — not in the plan's owned list.

### A-tools-8 — OPEN (medium)
- Anchors: analyze.go:AnalyzeGroup:L124-L129 (Host-based providerName) vs FindArticlesByDateRange:L386-L389,
  GetCachedMessageIDs:L511-L514 (Backend!=nil then `Backend.Provider.Name` -> nil deref), ClearCache:L588-L591 (same
  deref), GetCacheStats:L608-L611, ValidateCacheIntegrity:L633-L636, GetArticleCountByDateRange:L691-L694 (all
  `"unknown"` key; also `proc.Pool.Backend.Provider` deref panics when Pool!=nil but Backend==nil).
- Done already: none.
- Fix now: one nil-safe `func (proc *Processor) analyzeProviderName() string` (Pool/Backend nil -> error or
  "unknown"; else filepath.Base of Host without port, exactly as L124-L129) and use it in all 7 places; AnalyzeGroup
  keeps its nil checks L118-L123. GetCachedMessageIDs/FindArticlesByDateRange/GetArticleCountByDateRange have no
  callers (keep, but key-consistent).
- Test idea: Processor{Pool: &nntp.Pool{Backend: &nntp.BackendConfig{Host:"h.example:119"}}, cache root=TempDir}; write
  a cache file at the AnalyzeGroup path; GetCacheStats/ValidateCacheIntegrity find it, ClearCache removes it, no panic.
  (Check nntp.Pool can be built as a literal with Backend set; otherwise nntp.NewPool(cfg) without dialing.)

### A-tools-9 — OPEN (medium)
- Anchors: analyze.go:AnalyzeGroup:L151-L161 (cacheFileExists + !ForceRefresh -> trust); analyzeFromRemote:L177-L288
  (os.Create truncates L186; defer Flush error dropped L193-L194; WriteString error ignored L262; batch error
  `continue` L215-L219; loop `batchStart += AnalyzeBatchSize` L205 overflow; `CacheExists = true` L282);
  analyzeFromCache:L291-L378 (default 64 KiB bufio.Scanner L303, no scanner.Err() after the loop L305-L370).
- Done already: none.
- Fix now: write to `cacheFile+".tmp"`, return error on any XOver batch error (no continue), check WriteString/Flush/
  Close errors, os.Rename only on success (remove .tmp otherwise). scanner.Buffer(…, e.g. 1 MiB) + scanner.Err() in
  analyzeFromCache (the other 4 scanners L409, L533, L650, L709 already check Err(); give them the bigger buffer too). Validate GROUP range in AnalyzeGroup
  (First>=0, Last>=First, Last-First sane) and make the batch loop overflow-safe (break when end-batchStart <
  AnalyzeBatchSize). Extra (not in audit, same class): `-max-analyze` (goto L271) and `-start-date/-end-date` (continue
  before the write, L234-L240) also persist a PARTIAL cache that later runs trust — either don't persist the cache
  when MaxArticles>0 or a date filter is set, or always write every line and filter only the stats. Decide in slice.
- Test idea: fake Pool not practical; unit-test analyzeFromCache with a >64 KiB line (expects error) and a helper for
  the batch-range loop (Last near MaxInt64 terminates); tmp+rename via a small extracted writer helper.

### A-tools-10 — OPEN (low)
- Anchors: analyze.go:ExportAnalysisToJSON:L740-L769 (Sprintf, trailing comma L753, no escaping);
  ExportAnalysisToCSV:L772-L786 (unquoted); GetAnalysisCSVHeader:L789-L791; caller printAnalysisResults:L943-L966.
- Done already: none.
- Fix now: tagged export struct + encoding/json.MarshalIndent (same keys); ExportAnalysisToCSV via encoding/csv
  Writer into a strings.Builder (keep string return + header order). Header print in nntp-analyze main L161-166 can
  stay (it prints GetAnalysisCSVHeader twice-call no-op L164; harmless).
- Test idea: GroupName `a"b,c` -> json.Valid true and round-trips; csv.NewReader parses 11 fields.

### A-tools-11 — OPEN (low)
- Anchors: cmd/nntp-analyze/main.go:readGroupsFromActiveFile:L22-L67 (fields[0] unvalidated L50-55);
  analyze.go:printAnalysisResults:L926-L1008 (`out` in cwd L933-L938, `out/%s.json` L943, `out/%s.csv` by first label
  L931,L955); main.go:analyzeGroupsFromFile summary `out/...summary.txt` L529 + writeSummaryToFile:L593-L661 (MkdirAll
  "out" L598). Client-side validGroupArg (internal/nntp/nntp-client-validate.go:L38-L48) still allows "/", so the
  path is reachable if the server answers GROUP.
- Done already: 83fdee3 replaced the os.Exit calls with error returns (OPS-1 part). Nothing of the path fix.
- Fix now: skip names failing common.IsValidGroupName in readGroupsFromActiveFile (all regexes reject "/"; analyze.go
  then needs the common import, not yet imported there); in printAnalysisResults reject/sanitize names containing
  path separators or "..". Add `-out` flag (default "out") passed down to printAnalysisResults and writeSummaryToFile.
- Test idea: readGroupsFromActiveFile drops `../../tmp/x 1 1 y` (test in cmd/nntp-analyze, new file
  cmd/nntp-analyze/adf_opt_analyze_test.go) + printAnalysisResults with a traversal name writes nothing outside outDir.
- COLLISION: internal/processor/adf_proc_test.go:TestAdfProcPrintAnalysisResultsError:L30-L50 calls
  `printAnalysisResults(analysis, "json")` with cwd containing a regular FILE named `out` and expects an error containing
  "failed to write JSON file". A signature change (outDir param) breaks compile; switching to os.MkdirAll changes the
  error to "failed to create output directory" and breaks the assert. Either keep the 2-arg function as a wrapper
  (default "out") and keep the current Stat/Mkdir+WriteFile error path, or add adf_proc_test.go to owned files.

### Slice notes
- Owned files: internal/processor/analyze.go, cmd/nntp-analyze/main.go, internal/processor/adf_opt_analyze_test.go
  (confirmed) PLUS internal/processor/lo1_paths_test.go (required, A-tools-7), PLUS internal/processor/processor.go
  (only the Processor struct, if the cache-root field is chosen; no other O1 slice owns it), PLUS
  cmd/nntp-analyze/adf_opt_analyze_test.go (A-tools-11 input validation lives in package main). adf_proc_test.go only
  if the printAnalysisResults signature/error wording changes.
- Collisions / dependencies: none with other O1 slices. O2 adf-opt-artifacts-docs later edits cmd/nntp-analyze/main.go
  usage/defaults and cmd/nntp-analyze/README.md (L143 documents `data/cache/{provider}/...`; -data/-out text) —
  sequential, O2 must rebase on O1.

## adf-opt-tcp2tor

### A-tools-12 — PARTLY FIXED (medium)
- Anchors: cmd/tcp2tor/main.go:main:L83-L128 (flags L88-105; `-listen-host` default 127.2.3.4 L89);
  runSingleTargetMode:L131-L204 (listenAddr L154, ProxyServer L173-180); runConfigFileMode:L207-L287 (ProxyServer
  L248-255); parseConfigFile:L354-L431 (listenHost L380, free text); ProxyServer struct:L599-L608;
  Start:L611-L651 (`go s.handleConnection(conn)` L649, unbounded); handleConnection:L664-L733 (io.Copy both ways,
  no deadlines L709-L725); showUsageExamples:L50-L81; cmd/tcp2tor/example.cfg:L7 (`0.0.0.0:3119:public-ip.onion:119`).
- Done already: 8164555 (OPS-4c) bounded the dial + SOCKS handshake (socksForwardDialer L549-L554, dialThroughSOCKS
  L560-L596, used at L677-L699). So "missing dial timeout" part is closed; no conn limit, no idle timeout, no bind ACL.
- Fix now: `-max-conns` (e.g. default 64) as a buffered-chan semaphore in ProxyServer; when full, close the new conn.
  `-idle-timeout` via a small conn wrapper whose Read sets SetDeadline(now+idle) on both conns. Refuse non-loopback
  listen hosts (net.ParseIP(...).IsLoopback() or "localhost") in both modes unless `-allow-public`; check in one
  helper used by runSingleTargetMode and for each parseConfigFile entry. Remove the 0.0.0.0 line from example.cfg.
- Test idea: ProxyServer with MaxConns=1 and a stub dialer -> second client is closed; bind check helper table test
  (127.2.3.4 ok, 0.0.0.0/192.168.1.100 refused, allowed with allowPublic).
  Note handleConnection builds the SOCKS dialer itself; for a stub, inject a dial func field (default dialThroughSOCKS).

### A-tools-13 — OPEN (low)
- Anchors: main.go:Start:L611-L651 (s.listener L613, s.shutdown L619 assigned inside the server goroutine; closed-conn
  detection by string match L643); Stop:L654-L661 (unsynchronized reads; double Stop panics on close of closed chan);
  runSingleTargetMode `go server.Start()` L187-189 / Stop L196; runConfigFileMode goroutines L258-264, Stop+wg.Wait
  L276-281; handleConnection half-close: returns on first direction done L728.
- Done already: none (8164555 did not touch Start/Stop).
- Fix now: split into `Listen() error` (net.Listen + make shutdown chan, called synchronously before the goroutine) and
  `Serve() error`; Stop via sync.Once, guarded by a mutex; use errors.Is(err, net.ErrClosed). In handleConnection,
  after client->target io.Copy ends call CloseWrite on the *net.TCPConn (if available) and wait for both directions
  (with the idle deadline from A-tools-12 bounding the wait). Optionally track live conns so Stop closes them.
- Test idea: `go test -race`: Listen on 127.0.0.1:0, Stop immediately, Serve returns within 1 s, no race; double Stop
  no panic.

### Slice notes
- Owned files: cmd/tcp2tor/main.go, cmd/tcp2tor/example.cfg, cmd/tcp2tor/adf_opt_tcp2tor_test.go (confirmed).
- Existing cmd/tcp2tor/adf_tcp2tor_test.go (package main) defines adfTorSilentProxy, adfTorBlockingDialer,
  TestAdfTor{SelfTestTimeout,DialTimeout,DialTimeoutPlainDialer}; new test must use other names (e.g. adfOptTor*) and
  the existing tests must keep passing (they call testSOCKS5Connection, socksForwardDialer, dialThroughSOCKS — keep
  those signatures). Untracked cmd/tcp2tor/tcp2tor binary + .sha256 exist in the checkout (not in git; ignore).
- Collisions / dependencies: none.

## adf-opt-rslight

### A-tools-14 — OPEN (medium)
- Anchors: cmd/rslight-importer/main.go:main:L23-L211 (`prof.NewProf()` L26, `go Prof.PprofWeb(":51111")` L27,
  `Prof.StartMemProfile(5m,30s)` L28, before flag.Parse L40; import L11, global `Prof` L19).
- Done already: none here. Reusable pattern from 8ec5ca4: cmd/history-rebuild/pprof_addr.go (loopbackPprofAddr,
  newPprofMux without cmdline) + main.go L102-L114 (http.Server with ReadHeaderTimeout). It is package main there, so
  copy it (new cmd/rslight-importer/pprof_addr.go) rather than import.
- Fix now: drop go-cpu-mem-profiler use; add `-pprof` flag (empty = off), validate loopback, start a private-mux
  http.Server after flag.Parse; no heap-profile files in cwd (drop StartMemProfile). go.mod keeps the now-unused
  require (go build ok; `go mod tidy` would drop it + gorilla/mux indirect — leave go.mod alone unless the user says).
- Test idea: loopback validation table test on the copied helper (":6060"->127.0.0.1, "0.0.0.0:6060" refused).

### A-tools-15 — OPEN (medium)
- Anchors: internal/processor/rslight.go:LegacyArticle:L52-L62 (doc "final DOT+CRLF" L61);
  parseArticleTextToSlice:L307-L322 (split on "\n", removes "." only when last element; no unstuffing);
  importLegacyArticle:L325-L367 (caller L333-L334); context internal/nntp/nntp-client-commands.go:
  ParseLegacyArticleLines:L859-L903 (separator `line == ""` L867-L872; Bytes via common.WireArticleSize L884). The
  client's own dot-unstuffing is inline in the multiline reader (nntp-client-commands.go ~L846-L849), no exported helper.
- Done already: none in the parser. (nntp-client-commands.go rewritten by the client-transfer plan; separator logic
  unchanged, Bytes now computed from head/body lines so the `size` return of parseArticleTextToSlice is log-only.)
- Fix now: in parseArticleTextToSlice: ReplaceAll "\r\n"->"\n", drop trailing "" elements, then a final "." line,
  then unstuff leading ".." -> "." on every line. Keep the fix inside rslight.go (nntp package not owned).
- Test idea: LF, CRLF, dot-stuffed and no-terminator samples -> parseArticleTextToSlice + nntp.ParseLegacyArticleLines
  give subject "s", body "line1\n.dotline" (no ".", CRLF body non-empty).

### A-tools-16 — OPEN (low)
- Anchors: rslight.go:ImportAllSQLiteDatabases:L370-L499 (outer `err` L374 reassigned in goroutine L464 = race;
  groupsDone++ L470-L472; returns nil L498 despite errs L480-L487; unlocked skippedFiles++ L398,L425 (pre-worker,
  fine); hardcoded `time.Sleep(15s)` L449); importLegacyArticle:L346-L349 (dup `response != CasePass` with nil err
  -> error "%!w(<nil>)"); rslight_articles.go:ImportSQLiteArticles:L47-L148 (swapped skipped/skippedEmpty L106-L113;
  second groupsDone++ L143); NewLegacyImporter:L71-L82 (uses deprecated NewProcessor -> log.Fatalf);
  cmd/rslight-importer/main.go:main:L23-L211 (log.Fatalf after DB open L74,L80,L87,L109,L117,L136,L153,L161,L206;
  os.Exit(0) L143,L148; teardown L188-L210).
- Done already: only the enabling piece: 83fdee3 added processor.NewProcessorE (processor.go L71) that a
  NewLegacyImporterE can use. Nothing else.
- Fix now: `err := leg.ImportSQLiteArticles(file)` in the goroutine; return fmt.Errorf("%d of %d files failed", ...)
  (or errors.Join) from ImportAllSQLiteDatabases; one groupsDone++ (drop L143 or L471); fix the skipped/skippedEmpty
  branches and count history.CaseDupes as skipped-duplicate, not failure. main: `run() int` with one teardown
  (proc.Close -> close(db.StopChan) -> db.WG.Wait -> db.Shutdown), all post-open fatals become returns; NewLegacyImporterE
  via NewProcessorE. Make the 15 s pre-import sleep a package var so tests can zero it.
- Test idea: `-race` test of ImportAllSQLiteDatabases is heavy (needs a real Database + GetGroupDB + 15 s sleep);
  practical: extract the per-file worker/aggregation into a helper taking an import func, test error aggregation + race;
  unit-test the counter classification helper.

### Slice notes
- Owned files: cmd/rslight-importer/main.go, internal/processor/rslight.go, internal/processor/rslight_articles.go,
  internal/processor/adf_opt_rslight_test.go (confirmed) PLUS new cmd/rslight-importer/pprof_addr.go (+ optional
  cmd/rslight-importer/adf_opt_rslight_test.go for the pprof helper). No existing test file touches rslight code.
- Collisions / dependencies: adf-opt-analyze also writes into package internal/processor (different files; test helper
  names must not clash: prefix adfOptRslight*/adfOptAnalyze*). If adf-opt-analyze adds a field to processor.go,
  rslight does not need processor.go (NewProcessorE already exists). O2 later edits rslight-importer/main.go usage and
  README — sequential after O1.

---

# Report: tools2

# Re-anchor report: adf-opt-tools-misc (O1) and adf-opt-artifacts-docs (O2)
Checkout HEAD faec07e (branch plan-adf-optional). Read-only. `git diff 84c2081..HEAD` is empty for progress.go,
import-flat-files, extract_hierarchies, merge-active, merge-descriptions, benchmark_hash, test-MsgIdItemCache,
history-demo, active_files/hierarchies, nntp-analyze, rslight-importer and .gitignore. It is non-empty only for
cmd/nntp-fetcher (ff22aac, cf77891, merged as dc9beda) and build_history-rebuild.sh (4a322d9, only dropped -race).

## adf-opt-tools-misc
### A-tools-6: OPEN (medium)
- Anchors: internal/database/progress.go:(*ProgressDB).initSchema:L78-L88 (an unbounded `for { Exec; Sleep 100ms; continue }` that never logs);
  NewProgressDB:L34-L56 (calls initSchema at L50); callers cmd/nntp-fetcher/main.go:main:L151-L155 (log.Fatalf after
  OpenDatabase, so the DB is not shut down), cmd/web/main.go:main:L221-L224 (already uses fatalAfterDB, so no edit is needed).
- Done already: none.
- Fix now: replace the loop body with `_, err = RetryableExec(p.db, query_progressDB_initSchema); return err`. RetryableExec
  retries only busy/locked errors and is bounded by GetSQLiteMaxRetryWait. Return the error wrapped. Optional: in the fetcher,
  replace `log.Fatalf` with `fetcherShutdown(db)` (backend.go:L85) + `log.Printf` + `os.Exit(1)`. That is in main.go, which this slice owns.
- Test idea: create `<tmp>/progress.db/progress.db` as a directory. `NewProgressDB(<tmp>/progress.db)` must return a non-nil error within ~5 s
  (run it in a goroutine with a timer). Put the test in a new file `internal/database/adf_opt_progress_test.go` (package database).
### A-tools-17: OPEN (medium)
- Anchors (cmd/import-flat-files/main.go, unchanged):
  - (*DBManager).GetDB:L80-L136: creates all 65,536 tables plus a redundant `idx_%s_hash` index (L115-L130) under dm.mutex.Lock.
  - getTableName:L145-L150: the Sprintf table name. getStoredHash:L154-L159.
  - (*DBManager).ArticleExists:L162-L179: queries the same table name.
  - worker:L312-L456: processBatch closure L319-L370. Sprintf+Prepare at L345-L349 returns, and the deferred Rollback drops the whole batch.
    A failing Exec still counts as processed (L353-L366: L368 adds len(batch)). The only hash check is `len < 32` at L373-L377.
  - readFile:L458-L478: default bufio.Scanner, 64 KiB line limit, rewrites CRLF.
- Done already: none.
- Fix now:
  1. In worker, before routing, validate `MessageIDHash` (`^[0-9a-f]{3}` prefix, or the whole name hex ≥32) and Dir3 (1 hex char). Otherwise count it as skipped and `continue`.
  2. Use `INSERT OR IGNORE` and count processed per row whose RowsAffected is 1 (errors per failed row). A Prepare failure must not drop the other tables' rows: skip that table only.
  3. Create tables lazily: a per-DB `sync.Map`/set of created tables; `CREATE TABLE IF NOT EXISTS` (no extra index) on first use, outside dm.mutex or inside the tx.
     ArticleExists must then treat "no such table" as not-exists, or create the table first.
  4. Make readFile `os.ReadFile`.
- Test idea: new `cmd/import-flat-files/adf_opt_import_test.go`. Build a temp head/body tree `0/0/0/` with one valid 32-hex pair and one `zz…` pair, set
  `*dbPath = t.TempDir()`, run `worker` over a channel. Expect totalProcessed 1, the bad pair skipped, and the row present. Add a readFile case with a line over 64 KiB.
### A-tools-18: OPEN (low)
- Anchors:
  - cmd/extract_hierarchies/extract_hierarchies.go:main:L17-L163: hardcoded `active_files/active.2025-06` and `active_files/hierarchies` at L24-L25.
    The Fprintln/Close errors at L98-L105 and L117-L124 are not checked. createDatabaseAndReadHierarchies:L166-L209: `sample.db` in the cwd at L168,
    and the stale `migrations/0003_…sql` at L175-L176.
  - cmd/merge-active/main.go:main:L51-L176: `active_files` and `active.isc` at L84-L85. The `active.*` glob at L109-L119 also picks up its own `active.merged`.
    Output at L162. writeMergedActiveFile:L178-L200 writes in map order (L187) with an unchecked deferred Close. processOverviewFiles:L556-L641:
    `../active_files/local-mode.active` at L560, `/tank0/OV/overview/nntp-server-new` at L589, and an abort on the first missing file at L604-L608.
  - cmd/merge-descriptions/main.go: in main:L13-L57, the year glob at L34-L48 is applied ascending with first-wins, so the oldest year wins.
    writeDescriptions:L125-L150 ignores the Fprintf, Flush (deferred) and Close (deferred) errors.
- Done already: none.
- Fix now:
  - extract_hierarchies: add flags `-active` and `-out`. Read `migrations/0003_main_create_hierarchies.sql` from the exported
    `database.EmbeddedMigrationsFS` (internal/database/embedded_migrations.go:L14-L15) into an in-memory DB (`file::memory:`), not sample.db.
  - merge-active: add flags `-dir`, `-primary`, `-overview-dir` and `-overview-active`. Exclude `active.merged`. Sort the output by group name.
    Skip and count missing overview files.
  - merge-descriptions: process the year files newest first (sort.Sort(sort.Reverse)). Check the Fprintf, Flush and Close errors in writeDescriptions.
- Test idea:
  - merge-descriptions: writeDescriptions to `/dev/full` returns an error (skip if absent). Newest year wins for a duplicate group.
  - merge-active: the glob excludes active.merged, and the output is sorted.
  - extract_hierarchies: the loader returns a non-empty list from the embedded FS.
  - Test files: `cmd/{merge-descriptions,merge-active,extract_hierarchies}/adf_opt_*_test.go` (none exist yet).
### A-tools-20: PARTLY FIXED (low)
- Anchors (cmd/nntp-fetcher/main.go, all inside main):
  - L110-L113: the dead `processor.DownloadMaxPar = *downloadMaxPar`. TodoQ capacity is set at package init (internal/processor/processor.go:L37,L53).
  - L305: the "unchangeable (code not working yet)" comment.
  - L210 and L290: the two `time.Sleep // debug sleep` calls.
  - L233-L237: the hardcoded ET/BW caps, plus the consts BW_V4_Prefix/ET_V4/ET_V6 at L29-L31.
  - Feed goroutine L310-L358: a `return` on a MainDBGetNewsgroup error/nil at L329-L333.
  - Download workers L557-L708: `errChan := make(chan error, len(newsgroups))` at L562. It is written at L610, L641, L645, L654, L669, L672, L683 and L695 and never read.
- Done already: ff22aac (merge dc9beda) moved the `-download-start-date` validation up front (main.go:L92-L100) and removed the worker log.Fatalf.
  `startDates` is now `map[string]time.Time`.
- Fix now:
  1. In the feed loop, log and `continue` (count skipped) instead of `return`.
  2. Delete errChan and its sends: keep the logs, and turn `errChan <- nil` into a plain continue.
  3. Delete the dead DownloadMaxPar assignment and the two debug sleeps.
  4. USER DECISION D-O1 (2026-10-15): the ET/BW caps and their consts STAY. They are limits those servers impose on
     every client; a stored `MaxConns` must never raise them. Do not touch L29-L31 / L233-L237.
- `-max-batch` clamp: A-tools-4's memory half is closed (`newDownloadRun`, internal/processor/proc_DLArt.go:L77, commit 057907f, merge 82278fd).
  The clamp does NOT exist: main.go:L114-L129 has only a lower bound of 10 and a warning above 1000. L135-L136 feed it unclamped into nntp.MaxReadLinesXover and
  processor.MaxBatchSize, while db_batch clamps MaxDBbatch to 1000 (internal/database/db_batch.go:L133-L137). Add here: `if *maxBatch > 1000 { warn; *maxBatch = 1000 }`
  (or 10000 by user decision), and fix the contradictory warning text and the flag description.
- Test idea: extract the feed loop into a small func, for example `feedCheckQueue(newsgroups, get func(string)(*models.Newsgroup,error), out chan<- *string, …)`,
  and test that one failing group is skipped and the rest are queued. Same for a `clampFetcherFlags` helper. Use a new file `cmd/nntp-fetcher/adf_opt_fetcher_test.go`
  with prefix `TestAdfOptFetch*`, because `TestAdfFetch*` belongs to the existing adf_fetcher_test.go.
### Slice notes
- Owned files: the list is correct (progress.go, import-flat-files/main.go, extract_hierarchies.go, merge-active/main.go, merge-descriptions/main.go,
  nntp-fetcher/main.go, plus new adf_opt_* tests). backend.go is NOT needed (only reused). cmd/web/main.go is not needed.
- Collisions:
  - internal/database is shared in O1 with adf-opt-hierarchy (adf_opt_hierarchy_test.go), adf-opt-postqueue (adf_opt_postqueue_test.go) and
    adf-opt-web-maint (utils.go). Use a distinct file name (adf_opt_progress_test.go) and unique helper prefixes (e.g. `adfOptProg*`), because they share package scope.
  - Existing adf_fetcher_test.go must keep passing. Do not touch fetcherBackendConfig/openFetcherDB.
  - O2 later edits `showUsageExamples` and the flag descriptions in nntp-fetcher/main.go. That is sequential, but O2 must read the post-O1 clamp text.

## adf-opt-artifacts-docs (wave O2)
### A-tools-19: OPEN (low)
- Anchors: tracked files `cmd/benchmark_hash/benchmark_hash` (ELF, 2.5 MB) and `cmd/benchmark_hash/debug.log`. `git check-ignore` matches neither; .gitignore has no `*.log`
  and no per-path rule. cmd/benchmark_hash/main.go:L35 (generates 1,000,000 ids versus the log's 100,000).
- Done already: none.
- Fix now: `git rm --cached cmd/benchmark_hash/benchmark_hash cmd/benchmark_hash/debug.log`. Add .gitignore lines `cmd/benchmark_hash/benchmark_hash` and
  `cmd/benchmark_hash/debug.log`, in the same style as the existing `cmd/tcp2tor/tcp2tor` line.
- Test idea: shell check only. `git ls-files cmd/benchmark_hash` lists only main.go, and `git check-ignore` matches both paths.
### A-ops-17: PARTLY FIXED / partly INVALID (info)
- Anchors:
  - Tracked ELFs `cmd/test-MsgIdItemCache/test-cache` and `cmd/test-MsgIdItemCache/test-MsgIdItemCache` are still tracked and not ignored.
    These, plus benchmark_hash, are the only ELF files in `git ls-files`.
  - `active_files/hierarchies/organize_by_theme.go:L1` (`package main`, no build tag; `go list ./active_files/...` lists it as a package).
  - main:L90-L91 has the stale `../../migrations/0003_main_create_hierarchies.sql`.
  - `active_files/hierarchies/organize_by_theme.sh:L7` has the stale, unused `SQL_FILE`.
  - Build scripts: build_analyze.sh:L3, build_expire-news.sh:L3, build_history-rebuild.sh:L3 and build_usermgr.sh:L1 have no `-ldflags "-X main.appVersion=$(cat appVersion.txt)"`.
    build_usermgr.sh has no shebang; it is the only build_*.sh without one. All four target packages declare `var appVersion`.
- Done already: build_TestMsgIdItemCache.sh deleted in 1b67025 (that part is INVALID). `-race` was dropped from the build scripts in 4a322d9 (not part of the finding).
- Fix now:
  1. `git rm --cached` both test-MsgIdItemCache binaries and add .gitignore lines.
  2. Put `//go:build ignore` + blank line above `package main` in organize_by_theme.go. `go run organize_by_theme.go` still works. Fix its L91 path to
     `../../internal/database/migrations/0003_main_create_hierarchies.sql` and the .sh L7 to match.
  3. Use the build_webserver.sh pattern: `go build -o build/X -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/X`. Give build_usermgr.sh `#!/bin/bash`.
     build_expire-news.sh builds the single file `cmd/expire-news/main.go`; switch it to `./cmd/expire-news` so a later non-test file in the package does not break it.
  - Do NOT touch build_ALL.sh or appVersion.txt (CLAUDE.md).
- Out of plan scope, record only:
  - the `.off` sources (cmd/test-MsgIdItemCache/main.go.off, cmd/history-demo/main.go.off).
  - deploy host/IPs in rsync_nntp-transfer_TEST.sh and scripts/nntp-transfer-loop.sh. The rsync* files are off-limits per CLAUDE.md.
- Test idea: shell check. `go list ./active_files/...` prints "build constraints exclude all Go files". `bash -n` on the 4 scripts. `grep -L main.appVersion build_*.sh` lists only build_ALL.sh.
  `./build_analyze.sh` etc. build (the scripts write only build/).
### A-tools-21: OPEN (low)
Checklist from the current flag definitions. Re-read the flags after O1: adf-opt-analyze (A-tools-7/8/11) and adf-opt-rslight (A-tools-14, new `-pprof`) change these flag sets.

**cmd/nntp-fetcher/main.go**: showUsageExamples:L34-L53; flags L64-L83. There is no README.
- L40 `-test-conn`: the flag does not exist (it is an nntp-transfer flag). Remove the "Connection Testing" section. L35-L36 also wrongly say the tool "is used for testing connections".
- L48 `-update-list`: does not exist. The real flags are `-fetch-newsgroups-from-remote '<pattern>|$all'` plus `-fetch-newsgroups-force` to actually add (without it the run is dry), and optionally `-exclude-prefix`.
- L43 `-group alt.*`: must be quoted (`'alt.*'`). `$all` is accepted too (L212) but is undocumented.
- Not mentioned at all: `-data`, `-nntphostname` (needed on the first run, DB fallback afterwards), `-reset-progress N` (sets progress to N and exits), `-max-batch`, `-max-batch-threads`, `-max-queue`,
  `-download-max-par`, `-fetch-active-only`, `-history`, `-useshorthashlen`, `-reuse-crossposts`.
- Flag description errors:
  - L65 `-max-batch-threads` says "(default: 16)", but the default is 4.
  - L66 `-max-batch` "recommended: 100-10000" contradicts the >1000 warning at L127-L128. Align it with the new clamp.
  - L73 `-exclude-prefix` says "use with UpdateNewsgroupList"; it should name `-fetch-newsgroups-from-remote`.
  - L81 `-reset-progress` does not say that the value is the stored article number or that the run exits.
- L1 package comment "Real NNTP provider test" is stale.
- Not owned, optional: .github/copilot-instructions.md:L86 also shows `pugleaf-fetcher -group alt.test -test-conn`.

**cmd/nntp-analyze/main.go**: showUsageExamples:L70-L122; flags L132-L153.
- Defaults at L133-L136: `-host 81-171-22-215.pugleaf.net`, `-port 563`, `-username read`, `-password only`, `-ssl true`. Per the audit, require `-host` (no default) or document it. Decision needed.
- `-timeout` at L138 is ignored in `$all` mode (analyzeAllGroups:L304-L315; ConnectTimeout commented out at L312). Used at L467 and L901.
- readGroupsFromFile:L214-L256 and its call L194-L203 are unreachable (L175 already handles `-groups-file`). Delete them.
- The help text misses `-ssl`, `-timeout`, `-data` (needed for `$all`/wildcard) and `-help`. The `-group` description says "(required)", but `-groups-file` is an alternative.
- The `-cache-stats`/`-validate-cache`/`-clear-cache` examples depend on A-tools-8 (O1).
- These code items sit in the file adf-opt-analyze owns in O1. O2 edits it afterwards; the O1 slice may already have changed L304-L315.

**cmd/nntp-analyze/README.md**:
- L42 calls `-host` "Required", but it has a default.
- L43 says `-port` defaults to 119; the real default is 563.
- L49 implies `-ssl` is opt-in; the default is true. So the examples at L26 and L33 (`-port 119` without `-ssl=false`) would attempt TLS on 119.
- L52 and L71 `-force`: the flag is `-force-refresh`.
- L53 and L71 `-max-articles`: the flag is `-max-analyze`.
- Missing: `-groups-file`, `-data`, `-help`, and the default credentials.
- L143 cache path `data/cache/{provider}/{sanitized_group_name}.overview`. The real path is `<-data>/cache/<provider>/<sha256(sanitized name)>.overview`
  (internal/processor/analyze.go analyzeCachePath:L483-L491). Re-check after A-tools-7/8.
- L146 "Incremental Updates" and L211 "Automatic cache validation and rebuilding" do not exist (A-tools-9). L208 "Automatic retry and timeout handling" does not exist.
- L198 "limited to 10,000 articles by default" is wrong: `-max-analyze` defaults to 0, meaning no limit. 10,000 is only the batch size, AnalyzeBatchSize at analyze.go:L113.
- Export (L180-L194) writes to `./out` in the cwd (A-tools-11; re-check after O1).

**cmd/rslight-importer/main.go**: usage L43-L56; flags L29-L38.
- L44 `[-resetallgroups]`: the flag is `-YESresetallgroupsYES`.
- L46 `-etc` "(default: /etc/rslight)": there is no default. `-etc` is mandatory, even for a reset. The reset (L121-L144) runs only AFTER the sections import (L106-L118); document that or reorder (reordering is a code change).
- L47 `-spool` "(default: /var/spool/rslight)": there is no default. If empty, the run exits after the sections import (L146-L149).
- L49 `-use-short-hash-len`: the flag is `-useshorthashlen`.
- L50 `-nntphostname` "(required)": only on the first run; processor.SetHostname falls back to the DB value.
- `-history` (L37) is missing from the usage. `-pprof` will be added by A-tools-14 (O1).
- The usage is printed only when `-data` or `-etc` is empty.

**cmd/rslight-importer/README.md**:
- L1 title "Legacy SQLite Database Importer" / L3 is fine.
- L8 and L58-L64: `*-data.db3` thread import does not exist. The code globs only `*-articles.db3` (internal/processor/rslight.go:L374). The same applies to the example log at L88-L89 and the "Thread headers" note at L99.
- L19 and L25 `go run cmd/sqlite-importer/main.go`: the tool is `cmd/rslight-importer`.
- L26 and L33 `-sqlite-dir` is `-spool`. L27 and L35 `-legacy-path` is `-etc`, which is required, not optional. L28 and L34 `-data-dir` is `-data`.
- Missing: `-threads`, `-useshorthashlen`, `-nntphostname`, `-history`, `-YESresetallgroupsYES`.
- L117 "Go 1.19+": go.mod says go 1.25.3.
- Verify the example log lines (L82-L90) against the real log strings before keeping them.
- Done already: none for A-tools-21.
- Test idea: doc/grep check. `grep -n -- '-test-conn\|-update-list' cmd/nntp-fetcher/main.go`, `grep -- '-force \|-max-articles\|sqlite-importer\|-sqlite-dir\|-use-short-hash-len' cmd/{nntp-analyze,rslight-importer}/*` → no hits.
  Optional unit test: every `-flag` token in showUsageExamples output exists in `flag.CommandLine`. This needs the flags hoisted out of main; skip unless cheap.
### Slice notes
- Owned files (proposed):
  - .gitignore; the 4 tracked binaries (rm --cached); active_files/hierarchies/organize_by_theme.go and organize_by_theme.sh (the stale SQL path);
    build_analyze.sh, build_expire-news.sh, build_history-rebuild.sh, build_usermgr.sh.
  - cmd/nntp-fetcher/main.go (showUsageExamples and flag descriptions only), cmd/nntp-analyze/main.go (usage/defaults/dead branch), cmd/nntp-analyze/README.md,
    cmd/rslight-importer/main.go (usage only), cmd/rslight-importer/README.md.
  - Optional: .github/copilot-instructions.md:L86.
  - Remove build_TestMsgIdItemCache.sh from the list (already deleted).
- Dependencies: O2 runs after O1. nntp-analyze/main.go is owned by adf-opt-analyze, rslight-importer/main.go by adf-opt-rslight, and nntp-fetcher/main.go by adf-opt-tools-misc in O1.
  Write the docs from the post-O1 flag set. Removing the default `-host` changes behaviour and needs a user decision.
  `git rm --cached` keeps the local files, so the checkout still holds the binaries (untracked, ignored).

## Status summary
- A-tools-6: OPEN. The initSchema loop is unbounded; use RetryableExec and return the error.
- A-tools-17: OPEN. No hash validation, a whole-batch drop, eager creation of 65,536 tables and indexes, and the Scanner in readFile.
- A-tools-18: OPEN. Hardcoded paths in all three tools; unchecked Flush/Close; the oldest year wins.
- A-tools-20: PARTLY FIXED. Start-date validation moved up front (ff22aac). The feed `return`, dead errChan, dead DownloadMaxPar write, debug sleeps remain; the ET/BW caps stay by user decision D-O1.
  The `-max-batch` upper clamp does NOT exist (A-tools-4 closed only for the queue half) → add it in this slice.
- A-tools-19: OPEN. Both files are still tracked and not ignored.
- A-ops-17: PARTLY INVALID. build_TestMsgIdItemCache.sh is gone (1b67025). The binaries, the missing build tag and stale SQL path, and the 4 scripts without ldflags (usermgr without shebang) are open.
- A-tools-21: OPEN. A full checklist is above; write the docs after O1 changes the flags.

---

# Report: ops

# Re-anchor O1: ops slices (HEAD faec07e, branch plan-adf-optional)

Baseline: `go build ./...` OK; `go test ./cmd/expire-news/ ./cmd/recover-db/ ./cmd/web/` pass;
cmd/usermgr, cmd/fix-references, cmd/fix-thread-activity have no tests.

## adf-opt-ops-tools

### A-ops-6 — OPEN (medium)
- Anchors: cmd/expire-news/main.go:getNewsgroupsToExpire:L348-L403 (single-group `default:` branch L387-L400, builds
  `&models.Newsgroup{Name, ExpiryDays: nga.ExpiryDays}`); consumers main:L210 (expiry block `ng.ExpiryDays`) and
  main:L246 (prune block `ng.MaxArticles`); internal/database/queries.go:GetActiveNewsgroupByName:L1037-L1044
  (`query_GetActiveNewsgroupByName` L1035 selects only `id, name, active`, WHERE active = 1).
- Done already: none. Commits since 84c2081 on the file (3ce4b56 SIGTERM, bdf0c03 deleteDerivedThreadRows/cleanupAfterDelete) don't touch this branch.
- Fix now: in the default branch call `db.MainDBGetNewsgroup(targetGroup)` (queries.go:L220, returns sql.ErrNoRows when
  missing, fills ExpiryDays/MaxArticles/ID) → ErrNoRows: "not found"; `!Active`: "inactive"; append the returned struct.
  Leave queries.go untouched (not owned).
- Test idea: OpenDatabase on t.TempDir() (NO_CACHE_BOOT, skip when euid<1000 like cmd/web TestMain), insert group with
  expiry_days=7/max_articles=5, assert getNewsgroupsToExpire("grp") returns ExpiryDays=7, MaxArticles=5; inactive/missing → error.
  Do NOT call `shutdown()` in the test: it is guarded by a package-level `shutdownOnce` (L312); tear down manually
  (close StopChan, WG.Wait, Shutdown).

### A-ops-7 — OPEN (low)
- Anchors: cmd/fix-references/main.go:main:L62-L187 (no `appVersion` var / no `config.AppVersion =`; `config.NewDefaultConfig()` L90
  still fatals via internal/config/config.go:NewDefaultConfig:L546-L549); usage text with undefined `-db` L75, L79;
  `-dry-run` default true L66 vs hint "Run without -dry-run" L180; build_fix-references.sh:L3 already passes `-X main.appVersion`.
- Done already: none (only 4a322d9 dropped -race in the build script).
- Fix now: `var appVersion = "-unset-"` + `config.AppVersion = appVersion` first in main (usermgr/expire-news pattern);
  usage `-data <data-dir>` in both lines; hint "Run with -dry-run=false to apply fixes". `NewDefaultConfig` is only
  used for the version log line: may also drop it and log `appVersion`.
- Test idea: small pure helpers (`usageText`, `dryRunHint`) asserted in a new cmd/fix-references test; plus `go build -ldflags "-X main.appVersion=t"` + `-h` run on scratch.
- Side note: scripts.sh:L16 comment "fix-references cannot start until A-ops-7 is fixed" goes stale; scripts.sh is owned by nobody in O1 (leave it or hand to adf-opt-artifacts-docs in O2).

### A-ops-8 — OPEN (low)
- Anchors: cmd/recover-db/main.go:main:L189-L207 (comma list L192-L201 and single name L203-L206 wrap `&models.Newsgroup{Name}`
  without lookup; wildcard L208-L220 and $all L221-L227 come from the main DB and are fine); first creating calls:
  CheckDatabaseConsistency in the main loop (L336, L402), RebuildThreadsFromScratch (L255, L392), checkAndFixDates:L818ff (GetGroupDB L825),
  scanOutOfOrderOverview (GetGroupDB L1141), reorderSingleNewsgroup (L1337/L1355).
  cmd/fix-references/main.go:main:L103-L113 (single name L104-L107) → GetGroupDB L132.
  cmd/fix-thread-activity/main.go:main:L32-L36 → fixGroupThreadActivity:L62-L184 (GetGroupDB L63).
  internal/database/db_groupdbs.go:GetGroupDB:L117 still creates dir+file+schema (SEC-2 DB side not changed).
- Done already: none. (expire-news's single-group path already checks existence; A-ops-6 keeps that.)
- Fix now: per tool, resolve each `-group` name with `db.MainDBGetNewsgroup(name)` before the list is used; ErrNoRows →
  print "unknown newsgroup" and skip (exit 1 if none left / any unknown); other errors fatal through the teardown.
  recover-db: exit via `recoverExit(db, 1)`; use the returned *models.Newsgroup (has ID) instead of the bare struct.
- Test idea: helper `resolveNamedGroups(db, names) (found, unknown, err)` per tool; OpenDatabase on temp dir, unknown name →
  in `unknown`, and `<data>/db` holds no new directory.

### A-ops-10 — OPEN (low)
- Anchors: cmd/usermgr/main.go:updateUserPassword:L253-L298 (`len(password) < 6` L279, `db.UpdateUserPassword` L290, no session
  invalidation); createNewUser:L104-L191 (6-char rule L136); truncate:L300-L305 (byte slice); internal/database/queries.go:UpdateUserPassword:L844-L846
  (only caller is usermgr); web rule internal/web/web_auth.go:validatePassword:L294-L303 (12..72 bytes, unexported).
- Done already: none. Correction to the audit text: web sessions are NOT in a `sessions` table; the web uses
  `users.session_id` (db_sessions.go CreateUserSession/ValidateUserSession). The reusable helper is
  `db.InvalidateUserSession(userID)` (internal/database/db_sessions.go:L124-L133, RetryableExec).
- Fix now: after a successful UpdateUserPassword call `db.InvalidateUserSession(int64(user.ID))` (error → return it);
  replace both `< 6` checks with a local `validateCLIPassword` mirroring validatePassword (12..72 bytes; comment pointing at
  web_auth.go — not importable, web_auth.go not owned); `truncate` by runes (`[]rune`, maxLen-3 + "...").
  NNTP half is N/A for the CLI: nntp_users have their own `password` column (a web password change does not alter NNTP auth)
  and NNTPAuthCache is in-process in nntp-server (15 min TTL, db_init.go:L23) — a CLI cannot invalidate it; note only.
- Test idea: split the prompt from the write (`applyPasswordUpdate(db, user, pw []byte)`), OpenDatabase on temp dir: create user,
  CreateUserSession, apply → ValidateUserSession(token) fails; 11-byte password rejected; truncate("äöüäöü…",n) is valid UTF-8.

### OPS-1 deferred part (NO_CACHE_BOOT + teardown) for these tools
- `database.NO_CACHE_BOOT` (internal/database/db_init.go:L24; checked at L227 and in loadBot/loadIPBlocking L494/L518) skips
  SectionsCache, MemThreadCache, HierarchyCache + WarmCache goroutine, bot/IP config. All callers nil-check those caches
  (only `IsNewsGroupInSections` queries.go:L1519 doesn't, and it has no callers).
- expire-news: NO_CACHE_BOOT L77 yes; `shutdown()` L316-L338 (close StopChan, WG.Wait, Shutdown; pre-audit e959d36) on every
  exit after OpenDatabase — DONE.
- recover-db: NO_CACHE_BOOT L119 yes; `recoverExit` L74-L101 (be2bf44) on every exit after OpenDatabase (only log.Fatalf is the
  OpenDatabase failure L187) — DONE.
- usermgr: NO_CACHE_BOOT L25 yes; teardown OPEN: `defer db.Shutdown()` L63 without close(StopChan)/WG.Wait, and the 8
  `log.Fatal(f)` at L68-L99 skip even the defer.
- fix-references: both OPEN: no NO_CACHE_BOOT; `defer db.Shutdown()` L101; `log.Fatalf` L111 after the DB.
- fix-thread-activity: both OPEN: no NO_CACHE_BOOT; `defer db.Shutdown()` L30; `log.Fatalf` L35, L41 after the DB.
- Fix now: set `database.NO_CACHE_BOOT = true` before OpenDatabase in fix-references/fix-thread-activity; give the three tools a
  local `exitAfterDB(db, code)`/`fatalAfterDB` in the `recoverExit` style (close StopChan once, WG.Wait with timeout, Shutdown,
  os.Exit) and route every post-OpenDatabase exit through it, including the normal end of main. No shared library helper exists.

### Slice notes
- Owned files: list confirmed (cmd/expire-news/main.go, cmd/fix-references/main.go, cmd/fix-thread-activity/main.go,
  cmd/recover-db/main.go, cmd/usermgr/main.go) + new tests: cmd/expire-news/adf_opt_expire_test.go,
  cmd/recover-db/adf_opt_recover_test.go, cmd/fix-references/adf_opt_fixrefs_test.go, cmd/fix-thread-activity/adf_opt_fta_test.go,
  cmd/usermgr/adf_opt_usermgr_test.go. No library file needed (MainDBGetNewsgroup, InvalidateUserSession exist).
- Existing tests to keep green, no TestMain in either: cmd/expire-news/{main_test.go,adf_expire_test.go}
  (helpers toolsTestGroupDB/adfExpireGroupDB — don't redeclare), cmd/recover-db/adf_recover_test.go (TestAdfRecover*).
  Tests that OpenDatabase must skip at euid<1000 (db_init.go:L138), run sequentially (global INIT) and never call
  `recoverExit`/expire-news `shutdown()` (os.Exit / sync.Once).
- Collisions: none with other O1 slices (scripts.sh stale comment: unowned, see A-ops-7).

## adf-opt-web-maint

### A-ops-11 — OPEN (low)
- Anchors: internal/database/utils.go:RsyncDIR:L35-L100 (args L37 `{"-va","--progress",oldpath,newpath}` + `--remove-source-files`,
  no `--`, unchanged since 84c2081); cmd/web/main_functions.go:rsyncInactiveGroupsToDir:L317-L377 (RsyncDIR call L360, no checkpoint,
  no close, no -force); cmd/web/main.go flags L121-L122, dispatch L277-L287.
- Done already: LO:A4 (source path) and FU:A3 (`rows.Err()` L371-L373) only, both pre-wave; nothing of this fix.
- Fix now: (1) RsyncDIR: build args via a small `rsyncArgs(old,new,remove)` → options, then `--`, then paths, each relative path
  prefixed `./` (stops both `-x` options and `host:` remotes; `--` alone doesn't stop `host:`). (2) Before each RsyncDIR:
  `g,_ := db.GetGroupDB(name)`, `RetryableQueryRowScan`/Query of `PRAGMA wal_checkpoint(TRUNCATE)` reading (busy, log, ckpt);
  busy != 0 → refuse that group ("open elsewhere"), then `db.ForceCloseGroupDB(g)` (db_groupdbs.go:L273) so this process holds no handle.
  (3) new flag (e.g. `-rsync-force`) required when `-rsync-remove-source` is set, refusal via `exitAfterDB(db, 2)`; help text says
  "stop fetcher/nntp-server/web first". No cross-process lock-file mechanism exists in the repo; the busy-checkpoint is the practical check.
- Test idea: unit test of `rsyncArgs` (`-foo`, `host:x`, absolute path, remove flag) in a new internal/database test; cmd/web test for
  the -force refusal predicate.

### A-ops-12 — OPEN (low)
- Anchors: cmd/web/main.go:main:L87-L502: OpenDatabase L208-L216 (NO_CACHE_BOOT never set in cmd/web; only its test sets it),
  NewProgressDB L221, GlobalDateParser L228, `processor.SetHostname(nntphostname, db)` L234-L236 (fatalAfterDB) BEFORE every
  one-shot mode: hide-future L239, update-activity L253, write-active L265, rsync L277, compare-active L289, disable-active L301,
  find-orphan-dbs L313, expiry-file L325; after resolveUseShortHashLen L337: import-active L353-L366 (exits only when
  `importActiveFile != ""`; desc-only import continues to the server), repair-watermarks L368-L377.
  internal/processor/processor.go:SetHostname:L314-L352 → validateHostname:L365ff (net.LookupIP L393, localhost/IP rejected L381-L389).
- Done already: none for this id. 06769ad (adf-web-cmd) gave every exit `exitAfterDB`/`fatalAfterDB`/`teardownDB`
  (main_functions.go:L533-L569) — reuse them; 83fdee3 (adf-proc-fatal) only turned NewProcessor's hostname fatals into errors.
  None of the one-shot modes nor internal/preloader use processor/hostname/ProgressDB/history (checked: main_functions uses only
  GetMainDB/GetGroupDB/ForceCloseGroupDB/MainDBGetAllNewsgroups/UpdateNewsgroupActive/UpdateNewsgroupExpiry/IncrementArticleSpam,
  all cache-nil-safe).
- Fix now: after flag.Parse compute `oneShot := updateNewsgroupsHideFuture || updateNGAConcurrent > 0 || writeActiveFile != "" ||
  rsyncInactiveGroups != "" || compareActiveFile != "" || disableActiveFile != "" || findOrphanDBs || updateNewsgroupsExpiryFile != "" ||
  importActiveFile != "" || repairWatermarks`; if set, `database.NO_CACHE_BOOT = true` before OpenDatabase and move these blocks (keep
  their order) into a `runMaintenance(db) int` called right after OpenDatabase + GlobalDateParser, before NewProgressDB and
  SetHostname, ending in exitAfterDB. Do NOT include importDescFile-only and forceReloadDesc (they continue into the server).
  processor.go needs no change (not owned).
- Test idea: table test of the `oneShot` predicate (desc-only import false); runMaintenance write-active on adfWebCmdDB with
  `processor.LocalNNTPHostname == ""` succeeds.

### Slice notes
- Owned files: confirmed cmd/web/main.go, cmd/web/main_functions.go, internal/database/utils.go (RsyncDIR only); add tests
  cmd/web/adf_opt_webmaint_test.go and internal/database/adf_opt_rsync_test.go (disjoint from adf_opt_hierarchy_test.go /
  adf_opt_postqueue_test.go). internal/processor/processor.go not needed.
- Collisions: cmd/web/adf_web_test.go owns the package TestMain and the shared `adfWebCmdDB` (opened with NO_CACHE_BOOT) — the new
  cmd/web test must not add a TestMain, must use its own group names (not adf.webcmd.*), and restore any package flag vars it sets.
  internal/database has TestMain in testmain_test.go — new test file just adds Test funcs. The `rsyncArgs` test needs no rsync binary.
  No overlap with adf-opt-ops-tools.

## Status summary
- A-ops-6: OPEN (single-group path still uses GetActiveNewsgroupByName, ExpiryDays/MaxArticles always 0)
- A-ops-7: OPEN (no appVersion var, -db usage text, dry-run hint)
- A-ops-8: OPEN (recover-db, fix-references, fix-thread-activity; GetGroupDB still creates)
- A-ops-10: OPEN (no session invalidation — helper is InvalidateUserSession on users.session_id; 6-char rule; byte truncate; NNTP half N/A for CLI)
- OPS-1 tool part: expire-news and recover-db DONE; usermgr teardown OPEN; fix-references and fix-thread-activity NO_CACHE_BOOT + teardown OPEN
- A-ops-11: OPEN (no `--`/`./`, no checkpoint, no -force)
- A-ops-12: OPEN (SetHostname + ProgressDB + full cache boot before every one-shot mode)
