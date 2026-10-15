# Plan: audit delta fixes (web, database, tools, ops findings of AUDIT-2026-10)

- **Slug:** `audit-delta-fixes`
- **Integration branch:** `plan-audit-delta-fixes` (from `testing-001`)
- **Run with:** `/run-plan .claude/plans/queued/audit-delta-fixes.md`
- **Depends on** (final run order: `nntp-server-hardening` → `nntp-intake-injection` → `nntp-client-transfer` →
  `nntp-rfc-commands` → `audit-delta-fixes`; wave 0 stops unless all four are merged into `testing-001`):
  - `nntp-server-hardening` (A-web-5 DB contract). Without it slice `adf-web-nntpusers` does not build
    (`CountNNTPUsersByWebUserID`, `UpdateNNTPUserWebUserID` do not exist) and `GetNNTPUserByWebUserID` still picks an
    arbitrary row; the NNTP hide predicate (its decision 2) would also differ from D2 here. It edits
    `queries.go` functions (`GetOverviewsRange`, `GetHeaderFieldRange`, one line of `GetArticleByNum`) next to the
    functions `adf-db-readers` edits: merged first, the hunks don't collide.
  - `nntp-intake-injection` (`db_batch.go`). Its S2 owns `query_updateNewsgroupsStats`, the PHASE 1 number loop
    (`:794-823`), `batchInsertOverviews` (`:969-1001`, incl. `query_processOverviewBatch` `:999`),
    `processOverviewBatch` (`:1003-1103`), PHASE 3 and the stats block of `db_batch.go`; `adf-db-batch` changes only
    the `query_processOverviewBatch` constant on top of intake's version plus functions intake doesn't touch, and
    would conflict textually if run first, and INT-5's
    numbering assumes the stats statement is already an `UPDATE` that never inserts. It also provides
    `common.FormatNewsDate`, `common.WireArticleSize` and `processor.ParseNNTPDateStrict` (used by `adf-web-post` and
    `adf-ops-recover`: build fails without them), fixes A-intake-5 (web crossposts) and A-tools-4 (per-run queue;
    done 2026-10-11: `DownloadArticles` no longer touches `Batch.GroupQueues`; the map init in `processor.go:55` and
    the deprecated `GetOrCreateGroupBatch` are now unused and may be deleted here),
    and implements the agreed A-tools-1 bound (hand-off 1 below). It no longer edits `cmd/web/main.go`
    (final decision (ii)).
  - `nntp-client-transfer` (`cmd/history-rebuild/main.go` pprof lines; it also adds SIGTERM to history-rebuild and
    nntp-transfer, A-ops-3d/f, its step 5a). Its hunk at `:12,:75,:97-104` is adjacent to
    this plan's `-useshorthashlen` hunk (`:74,:79`); applied earlier, one of the two would not apply. Its
    `providerLabel()` nil guard (A-tools-3 client half) is what keeps a `BackendConfig` without `Provider` from
    panicking; this plan adds the fetcher half. Its e2e E14 expects a `-race` transfer binary, which this plan's
    build-script change (D11) turns into an opt-in.
  - `nntp-rfc-commands`: no shared file; listed only to keep the run order (it must not see this plan's build-script
    change mid-run).
- **Source:** `docs/audit/AUDIT-2026-10.md` (§4 rows whose fix plan is `audit-delta-fixes` / `audit-delta-fixes*`,
  §11 decisions 2, 5, 10, §12), the slice reports `docs/audit/{audit-web,audit-db,audit-tools,audit-ops}.md`, and the
  unassigned `FU:E12`–`FU:E17` of `.claude/plans/done/web-db-followups.md` (carried over verbatim). Written by the
  wave-4 "Afterwards (inline)" step of `.claude/plans/wip/nntp-audit-tests.md`.
- **Base for every `file:line`:** `plan-nntp-audit-tests` @ 2fb7741 (verified with `grep -n` / `sed -n`).
  `git diff --stat 84c2081 2fb7741 -- internal/web internal/database internal/models internal/cache cmd web .github
  internal/processor internal/config '*.sh'` lists only `cmd/nntp-server/*_test.go` and `scripts/test-nntp-server.sh`,
  so the audit anchors in scope hold unchanged. The four sibling plans move lines in `db_batch.go`, `queries.go`,
  `threading.go`, `cmd/history-rebuild/main.go`: **every slice locates code by function name first** and uses the
  line only as a hint.
- **Parallelism:** wave 1 has 15 implementer slices (run in batches of 4–5, the user's usual size), wave 2 has 3,
  optional wave O1 has 10 and O2 has 1. Within a wave no two slices own the same file (where a file is shared across
  waves, the function list is given).

---

## Context

The audit left four kinds of work for this plan: the web findings (stored XSS in the tree preview, the cron gate,
hidden articles readable anonymously, the web post header set, the NNTP-user mapping, the sanitized-article cache),
the non-NNTP database findings (NULL-hierarchy readers, the article-cache leak, article-number reuse, process-global
state that blocks a re-open), the tool findings (the fetcher writing into `./data`, the fetcher proxy bypass) and the
ops findings (`recover-db` opening the wrong directory, SIGTERM without shutdown, broken scripts, `-race` production
builds, a release workflow that cannot release). Plus the six unassigned follow-ups of `web-db-followups`.

Appendix B of `nntp-audit-tests`: `internal/web/*` and the non-NNTP `internal/database/*` files are touched only by
this plan. The coordinator's ownership notes of 2026-10-11 are applied: no edit of `db_batch.go` PHASE 3 or the stats
block, no edit of `internal/processor/PostQueue.go` or `threading.go`, only the `-useshorthashlen` hunk of
`cmd/history-rebuild/main.go`, nothing in `internal/nntp/*` except the unowned `nntp-peering-pattern_test.go`, and
nothing in `cmd/nntpmgr/main.go`, `db_nntp_users.go`, `nntp_auth_cache.go`.

### Findings (mandatory)

Paths are under `internal/` unless they start with `cmd/`, `web/`, `.github/` or a root file. Severity is the
consolidated one (AUDIT §4). Ids with a letter suffix (`OPS-1a`) are this plan's split of one audit id across files;
each sub-id is in exactly one slice.

**Web**
| ID | Where (2fb7741) | Defect | Sev | Slice |
|----|----|----|----|----|
| WEB-1 | `web/web_admin_crons.go:18-19` (create), `:148-149` (toggle), `:307-308` (stop); gate only in update `:79` and delete `:183`; routes `web/webserver_core_routes.go:471-475`; form `web/templates/admin_crons.html:163` | Create and toggle check only `requireAdminAuth`, not `s.CronEdit`: without `-edit-cronjobs` an admin (or a WEB-2 payload) creates/enables a `sh -c` job. | high | adf-web-cron |
| A-web-20 | `web/cronjobs.go:373` (`runJobScheduler`, returns at `:384-388` and others), `:349-350` (`ErrCronExists`) | A scheduler that returns leaves the job in `cm.jobs`; re-enabling it answers `ErrCronExists`, so the job never runs again until restart. | low | adf-web-cron |
| A-web-17 | `web/web_admin_crons.go:265-281` | The log filter result is overwritten by the unfiltered tail when the log is longer than `limit`. | low | adf-web-cron |
| WEB-2 | `web/web_apiHandlers.go:448-501` (body `:488-489`, misleading comment `:486-487`), `web/static/js/thread-tree.js:515,518,520-522,539,557`, routes `web/webserver_core_routes.go:391,502` | Preview returns the entity-decoded raw body; `renderArticlePreview` concatenates it (and the raw group) into `innerHTML`; `escapeHtml` is never called. Stored XSS for any Usenet poster, API switch irrelevant. | high | adf-web-xss |
| A-web-13 | `web/webserver_core_routes.go:280-296` (`secure.Config`) | No Content-Security-Policy. | low | adf-web-xss |
| INT-7 | `web/web_sitePostPage.go:356-402` (headers `:358-371`, `HeadersJSON` `:377`, Bytes `:360`) | Web posts store only MIME-Version/Content-Type/CTE/X-pugleaf-Trace/From/Newsgroups/Lines/Bytes in `headers_json`: no Subject, Date, Message-ID, Path, References; `Bytes = len(body)-lines`. | medium | adf-web-post |
| A-web-5 (web half) | `web/web_admin_nntp.go:91-97` (default `webUserID := int64(1)`), `:160-207` (update: posting only with maxconns `:167-181`, password 1–16 chars `:184-189`, empty `web_user_id` branch `:199-203`); `web/templates/admin_nntpusers.html:36,210` | Unmapped NNTP users are linked to web user 1; the mapping cannot be changed; no minimum password length. DB half done by `nntp-server-hardening`. | medium | adf-web-nntpusers |
| A-web-2 | `cache/sanitized_cache.go:33,62-65` (`maxEntries` stored only), `:181,246,302` (inserts), `:420` (`evictOldest`, no caller), `:461` (`cleanup`) | `-maxsanartcache` is never enforced: a crawler keeps 30 min of bodies resident. | medium | adf-web-cache |
| A-web-3 | `cache/sanitized_cache.go:78-159` vs `:181-243` | `GetField` reads the fields after unlocking `article.mux`; race detector hit. | medium | adf-web-cache |
| A-web-4 | `models/sanitizing.go:209-211,464-477,634-636` | `StripDangerousHTML` overwrites Subject/From/Body/Date/References of the shared cached `*Article`. | medium | adf-web-cache |
| A-web-1 | `web/web_articlePage.go:22,94`, `web/web_sectionsPage.go:291,343`, `web/web_apiHandlers.go:237,267,448` (token API `getArticle`/`getArticleByMessageId`, preview), `web/web_sitePostPage.go:86-100` (reply prefill), `web/web_threadTreePage.go:28,100,174`, `database/tree_view_api.go:152,176` | Hidden (`hide>0`) articles stay readable anonymously by number, message-id, section routes, preview, tree page, `/api/thread-tree`, the token API and the reply prefill. | medium | adf-web-hide |
| A-web-11 | `web/webserver_core_routes.go:526`, `web/web_threadTreePage.go:28-96` (`Cache-Control: public` `:95`) | `/api/thread-tree` is outside `requireAPIEnabled`, returns raw From and hidden articles, cacheable publicly even for admin responses. | low | adf-web-hide |
| A-web-15 | `database/queries.go:890,917` (select lists), `web/templates/article.html:86,92,100` | `Article.Spam`/`Hide` are never loaded on the article page (counters always 0). | low | adf-web-hide |

**Database**
| ID | Where (2fb7741) | Defect | Sev | Slice |
|----|----|----|----|----|
| A-db-1 (readers + repair) | `database/queries.go:190-209` (`MainDBGetAllNewsgroups`), `:211-233` (`MainDBGetNewsgroup`), `:235-257` (`MainDBGetNewsgroupByID`); `models.Newsgroup.Hierarchy` is a `string` | A `newsgroups` row with NULL `hierarchy` (created by the old batch upsert, fixed by `nntp-intake-injection`) makes all three scans fail: fetcher `$all`, history-rebuild, expire-news, recover-db abort; GROUP answers 411. Existing rows need a repair. | high | adf-db-readers |
| A-db-11 | `database/hierarchy_cache.go:462` (`buildHierarchyTreeBulk`, `continue` on scan error), `database/queries.go:2814` (`GetNewsgroupsByHierarchy`), `:2187` (`getHierarchiesPaginatedDirect`, `:2232`) | Rows that fail to scan are dropped silently; no `rows.Err()`. | low | adf-db-readers |
| A-db-13 | `database/queries.go:1030,1050,1074,1109` (`GetActiveNewsgroups*`, `GetNewsgroupsPaginated*`; bare `Query` `:1084,1119`) | One NULL counter aborts the listing; bare `Query`; no `rows.Err()`. | low | adf-db-readers |
| SEC-7 (residual) | `database/queries.go:699` | `GetOverviews` logs one line per call (handed over by `nntp-server-hardening`: after it, the caller is the web only). | — | adf-db-readers |
| A-db-2 | `database/article_cache.go:127-140` (`removeElement` recomputes the key via `GetNewsgroupPointer` `:129-130`) | After `ExpireCache` replaces the TasksMap pointer, evictions remove the list element but not the map entry: the map grows without bound with full bodies; evicted entries are still served. | high | adf-db-cache |
| OPS-1e | `database/thread_cache.go:79-90` (`NewMemCachedThreads`, `CleanCron` endless loop) | Background loop without stop path. | (OPS-1) | adf-db-cache |
| OPS-1a | `database/db_init.go:139-148` (`log.Fatal` euid `:141`; `INIT` set before anything can fail `:148`), `:161-174` (mainDB not closed on error), `:206,212,224,229` (`log.Fatalf`), `:185` (`NewGHmap` → `GHmapCron` re-spawns forever, `groups_hashmap.go:22-27,82-108`), `sections_cache.go:18-24,41-66` (`CronClean` endless), `database.go:139` (`Shutdown` never resets `INIT`), `db_migrate.go:25,384,423` (`migratedDBsCache`) | Library code kills the process; a failed open blocks every later open; background loops never stop; the migration cache outlives the DB. | medium | adf-db-lifecycle |
| A-db-6 | `database/db_migrate.go:376-389,421-425` | `migratedDBsCache` keyed by group name only: a re-created group DB skips its schema (`no such table: articles`). | medium | adf-db-lifecycle |
| A-db-12 | `database/groups_hashmap.go:141-151` (`:150`) | `GetGroupFromHash` dereferences nil for an unknown hash (no caller). | low | adf-db-lifecycle |
| INT-5 (number reuse) | `database/migrations/0001_single_db_schema.sql:11-12` (`article_num INTEGER PRIMARY KEY`, no AUTOINCREMENT); insert `database/db_batch.go:999` (`query_processOverviewBatch`; the surrounding `batchInsertOverviews`/`processOverviewBatch` are intake-owned); `cmd/expire-news/main.go:560,603,620`; `database/queries.go:3060` (`ResetNewsgroupData`, `sqlite_sequence` reset `:3122-3123`) | After the top-numbered articles are deleted, new articles reuse their numbers and inherit `thread_cache`, `cached_trees`, `tree_stats`, main-DB `spam`/`user_spam_flags` rows and cached bodies. | high | adf-db-batch |
| A-db-9 | `database/db_batch.go:1758-1812` (`BatchDivider` adds in steps of `maxDBbatch` `:1803-1810`), `:766` (subtract per batch) | `queued` drifts negative, so the `MaxQueued` brake (`:1787`) never engages (the brake A-tools-1 relies on). | low | adf-db-batch |
| A-db-5 | `database/db_batch.go:166-184` (`ExpireCache`), `:187-210` (`GetNewsgroupPointer`) | Tasks still processing are expired; pointers change under readers (root cause of A-db-2). | medium | adf-db-batch |
| OPS-1d | `database/db_batch.go:1753` (`BatchDividerChan` package global), `:1758` (`BatchDivider` endless), `:166-184` (`ExpireCache` re-spawns itself), `:1570-1573` (`StartOrch` starts the divider), `:273` (`CheckNoMoreWorkInMaps` reads it) | Process-global channel shared by every `Database`; loops never stop. | (OPS-1) | adf-db-batch |

**Tools and processor**
| ID | Where (2fb7741) | Defect | Sev | Slice |
|----|----|----|----|----|
| A-tools-2 | `cmd/nntp-fetcher/main.go:74-75,91-96` (branch before `dbConfig.DataDir = *dataDir` `:128`), `:806-813` (`OpenDatabase(nil)` `:810`), `:948-950` | `-fetch-newsgroups-from-remote` opens `./data` whatever `-data` says, and writes rows with `-fetch-newsgroups-force`; never shuts the DB down. | high | adf-tools-fetcher |
| A-tools-3 (fetcher half) | `cmd/nntp-fetcher/main.go:838-845` vs `:226-266` | The list-update backend has no proxy fields and no `Provider`: a Tor/SOCKS provider is contacted directly. (Client nil guard: `nntp-client-transfer`.) | medium | adf-tools-fetcher |
| A-ops-3b | `cmd/nntp-fetcher/main.go:147` | SIGTERM not handled. | (A-ops-3) | adf-tools-fetcher |
| A-tools-5a | `cmd/nntp-fetcher/main.go:660-663` | `-download-start-date` validated inside a worker after the DB is open (`log.Fatalf` loses queued articles). The progress-before-commit half (A-tools-5b) is deferred. | (A-tools-5) | adf-tools-fetcher |
| OPS-1b | `processor/processor.go:59-88` (`log.Fatalf` `:63,69,88`), `processor/analyze.go:921-962` (`os.Exit(1)` `:934,943,948,962`; callers `:835,874`) | Library constructors and the analyze exporter kill the process. | (OPS-1) | adf-proc-fatal |
| A-tools-1 | `processor/threading.go:256` (`go proc.DB.Batch.BatchCaptureOverviewForLater(...)` per stored article) | One parked goroutine plus the full article per stored article, no bound (+377 MiB for 20 000). **Handed to `nntp-intake-injection`** (it owns `threading.go`); this plan provides A-db-9 and verifies (see "Cross-plan hand-offs"). | high | hand-off |

**Ops**
| ID | Where (2fb7741) | Defect | Sev | Slice |
|----|----|----|----|----|
| A-ops-1 | `cmd/recover-db/main.go:36` (`-db`), `:44` (`-data`), `:50-57` (usage), `:69-72` (stat `-db`), `:80` (opens `-data`), `:126` (prints `-db`) | Checks and prints `-db`, opens `-data` (default `./data`); destructive modes ask nothing. | high | adf-ops-recover |
| A-ops-5 | `cmd/recover-db/main.go:819-845` (ParseFailed branch, `continue` commented out), `:849-858`, `:877` (`hide = 0, spam = 0`) | `-rewritedates` un-hides spam and writes the parser's fallback date. | medium | adf-ops-recover |
| OPS-1f | `cmd/recover-db/main.go:86` (`defer db.Shutdown()`), `:169,186,197,208,305,671,674` (`os.Exit`) | Every mode exits without the teardown. | (OPS-1) | adf-ops-recover |
| OPS-4d | `run_web.sh:4`, `run_web_ssl.sh:3` (`-update-descr` undefined), `scripts.sh:1` (`rm data/history -r`), `:6-7` (`-update-newsgroup-activity` undefined), `:13-14` (`fix-references -dry-run`), `go_run_web.sh:1`, `run_test.sh:1`, `build_TestMsgIdItemCache.sh:3`, `manual_sqlite_migration_0002.sh:6-7` and `manual_sqlite_migration_0008.sh:5` (`migrations/…sql` paths that don't exist at the root; the 0002 script points at the 0008 file) | Scripts call undefined flags, missing files or packages. | (OPS-4) | adf-ops-scripts |
| OPS-4e | `build_{fix-references,fix-thread-activity,history-rebuild,import_flat-files,merge-active,merge-descriptions,nntp-transfer,nntpmgr,post-queue,tcp2tor,webserver}.sh` (e.g. `build_webserver.sh:3`) | 11 build scripts build with `-race`, including the deployed web server. | (OPS-4) | adf-ops-scripts |
| OPS-4f, A-ops-2, A-ops-15 | `.github/workflows/release.yml:19-26` (matrix on `ubuntu-latest`), `:33-35` (`go-version: '1.25.3'`), `:46-47,52` (dispatch input interpolated into shell), `:106-109` (cross build with cgo off), `:161-163` (`release` needs all) | No vet/test; 4 of 5 cross builds fail (cgo-only `go-sqlite3` symbols in `internal/history`), so no release is created; injection pattern. | medium | adf-ops-scripts |
| OPS-4c | `cmd/tcp2tor/main.go:102` (`-timeout`), `:177,252,551` (stored, never read), `:503` (`timeoutSec` unused), `:514,519,626,628` (`proxy.Direct`) | `-timeout` has no effect; dials and SOCKS handshakes have no deadline. | (OPS-4) | adf-ops-tcp2tor |
| OPS-2b | `nntp/nntp-peering-pattern_test.go:17` (hardcoded `/home/fed/...`), `:409-410` (writes `active.out` in the cwd), `:437,520` (read it), `:557-558,607-608` (write `analysis_*.txt`) | Tests depend on files of earlier runs and write into the source tree. | (OPS-2) | adf-ops-misc |
| OPS-4a | `cmd/history-rebuild/main.go:74,79` | `-useshorthashlen` is a silent no-op. | (OPS-4) | adf-ops-misc |
| A-ops-3c, A-ops-3e | `cmd/expire-news/main.go:146`, `cmd/post-queue/main.go:74` | SIGTERM not handled. | (A-ops-3) | adf-ops-misc |
| OPS-2a | `cmd/web/main.go:31-32` (`withnntp`, `withfetch`), `:28` (`isleep`), `:105-107` (flags commented out), `:113-116,150,154`, `:428-473` (dead NNTP block, incl. `:448`) | The embedded NNTP server can never start; four NNTP flags are parsed and ignored. | low | adf-web-cmd |
| OPS-4b | `cmd/web/main.go:102,342-361`, `cmd/web/main_functions.go:656-657`, `database/database.go:296-300` | `-useshorthashlen` has no effect but a value outside 2..7 aborts the first start; the processor gets the CLI value. | (OPS-4) | adf-web-cmd |
| A-ops-3a | `cmd/web/main.go:486-487` | SIGTERM ends the web server without any shutdown (exit 143). | medium | adf-web-cmd |
| A-ops-4 | `cmd/web/main.go:305-315`, `cmd/web/main_functions.go:1015-1099` (`:1040-1044`, `:1071-1079`) | `-disable-active` disables every group when the file has no 4-field lines. | medium | adf-web-cmd |
| OPS-1c | `cmd/web/main.go:221-350,379-396,425,509` (`log.Fatalf`/`os.Exit` after `OpenDatabase`), `cmd/web/main_functions.go:510-541` (`startHierarchyUpdater`, no stop, wrong log text `:524`), `:545-577` (`monitorUpdateFile`) | The web binary skips the DB teardown on every exit path after open; two loops have no stop path. | (OPS-1) | adf-web-cmd |

### Carried over verbatim from `web-db-followups.md` (lines 62-67, ids kept, not re-found)

Line numbers in these rows are as recorded at `2510642`; they still hold at 2fb7741 except where noted in the
slice text (`webgroupPage.go:89-93`, `web_sectionsPage.go:245-248`, `web_threadPage.go:80,94-101`,
`thread_cache.go:298-306`, `queries.go:3334-3340` re-verified).

| ID | Where (at `2510642`) | Defect |
|----|----|----|
| E17 | `internal/web/webgroupPage.go:89-93` and the twin at `web_sectionsPage.go:242-248` | **Found by the post-merge review of the combined `internal/web` diff; pre-existing and byte-identical at `60c0b63`.** When the page→cursor `OFFSET` lookup returns no row — an in-range page past the real end, e.g. `?page=50` on a 10-page group, which the 101 clamp leaves alone — the code falls back to `lastArticleNum = 0` (the newest page) but `PaginationInfo` is still built from the *requested* page. Result: page 1's 128 newest articles rendered under "Page **50** of 10", with Next/Last disabled. Same "a page number that does not mean what it says" family as D1, in a file `fu-pagination` owned, which D1 simply did not reach. Fix: clamp `page` to `ceil(totalCount/LIMIT_groupPage)` as well as to the offset bound before building `PaginationInfo`, or set `page = 1` on the `ErrNoRows` fallback so the header matches what was served. **Not assigned.** |
| E16 | `internal/database/thread_cache.go:300-305`, `GetCachedThreadReplies` | **Found by the post-merge review of the combined `internal/database` diff.** The function at the centre of E2 still does `if err == nil { replies = append(...) }` with no `else`, and returns `replies, totalReplies, nil` without `rows.Err()`. Wave 1 made `totalReplies = len(childNums)` the promise the caller builds `totalPages` from, so a truncated read or one bad row now yields a page that is short or empty under a count that says otherwise — the E2 symptom, with no error and no log. E10b added `rows.Err()` to the section listing and A3 to the rsync loop, but not here. **Related caveat, recorded rather than fixed:** `threadChildrenQuery` filters `+hide = 0` while `child_articles` lists hidden children too, so wave 1's stated invariant "every page the count promises carries rows" does not hold for a thread with hidden replies (`fu_hygiene_test.go` only inserts visible ones). Not a regression — `message_count - 1` counted hidden children as well — and wave 2 propagated the same overcount to the listing via `threadCacheReplyCount`. Fix: log the scan error with the `[CACHE:THREADS]` prefix and return `rows.Err()` in place of the bare `nil`; soften the `thread_cache.go` comment to say `child_articles` is the source of truth for the *count*, not a guarantee that every promised page renders rows. **Not assigned.** |
| E15 | `internal/database/migrations/0006_main_spam_system.sql:2-7`, the main-DB `spam` table | **Found by the wave-3 review of `fu-queries-cleanup`: E9's own argument applied to a third table.** `spam(newsgroup_id, article_num)` has **no FK and no cascade**, and is written live by `db_spam_flags.go:65` and `database.go:396`. Neither `DeleteNewsgroup` (A4) nor the new `BulkDeleteNewsgroups` (E9) removes those rows, so they outlive the group exactly as `user_spam_flags` rows did before A4. Unlike `user_spam_flags`, this one has a **visible** symptom, and it is the same family as D1/E4/E12: `queries.go:3334` counts every row (`SELECT COUNT(*) FROM spam`) while `:3335-3340` INNER JOINs `newsgroups`, so after flagging 40 articles in a group and then deleting it, the admin spam page reports 40, returns 0 rows, and paginates to pages the server cannot fill. Not a defect in the slice — it was scoped to mirror `DeleteNewsgroup`'s two deletes, which it did exactly. Fix: a third `DELETE FROM spam WHERE newsgroup_id IN (SELECT id FROM newsgroups WHERE …)` in **both** delete paths, or make the count go through the same join. **Not assigned.** |
| E14 | `internal/web/web_threadPage.go:80` vs `:94-101` | **Found by the wave-3 review of `fu-pagination`.** `GetCachedThreadReplies` is called with the **unclamped** `page`; the `if page > totalPages { page = totalPages }` clamp runs afterwards and no second fetch happens. So an out-of-range `?page=` renders the root article with **zero replies**, and because `totalPages == 1` the `{{if gt .TotalPages 1}}` nav block is suppressed — there are no controls to recover from it. **That unrecoverable form is specifically the one-page thread** (`totalReplies <= 50`): for a thread with more than one page the same URL renders with no messages at all — the root is prepended only on page 1 — but the nav survives, so Previous recovers. Reproduced: 50 replies, `?page=2` → HTTP 200, root present, 0 of 50 replies, badge "51 total messages", no nav. The clamp-after-fetch asymmetry is **pre-existing** for any out-of-range page, and E4 strictly improved this URL (it previously rendered neither the root nor any reply) — but E4 moved the boundary onto a page number that **used to be linked**, so stale bookmarks, caches and crawled `?page=2` URLs for exact-multiple-of-50 threads now land on the root-only render. Note `fu_pagination_test.go:250` asserts only that the root is present there, which locks the partial render in as expected behaviour rather than flagging it. Fix: clamp `page` against `ceil(totalReplies/ThreadMessages_perPage)` *before* the fetch — the handler cannot know `totalReplies` first, so either refetch once after clamping or redirect to the clamped page. **Not assigned.** |
| E13 | `internal/database/queries.go`, `BulkDeleteNewsgroups` | **Raised by `fu-queries-cleanup` about its own change, which is the right way to surface it.** The function still uses a plain `Begin`/`defer Rollback`/`Commit` rather than `RetryableTransactionExec` like `DeleteNewsgroup` does, so under `SQLITE_BUSY` an admin bulk delete fails outright instead of waiting the lock out. E9 made it a **three**-statement write (spam flags, section groups, newsgroups), so the busy window is now slightly wider than before this plan touched it. The slice deliberately did not convert it — that was outside E9's scope and would have been a silent behaviour widening — and flagged it instead. Same class as A2, which did exactly this conversion for `ResetAllNewsgroupData`'s step 1. Fix: wrap it in `RetryableTransactionExec`, keeping the three statements and their ordering, and keep the returned count coming from the `newsgroups` delete. **Not assigned.** |
| E12 | `internal/web/webgroupPage.go` and `web_sectionsPage.go`, the cursor-mode `PaginationInfo` literal | **Found by `fu-pagination` in wave 3 while fixing D1; it correctly did not fix it.** The third member of the same family as D1 and E4 — a link that does not go where it says. In cursor mode the literal leaves `NextPage`/`PrevPage` at 0, so `pagination.html` emits `href="?page=0"` for Next (and for Previous when `HasPrev`); the handler parses 0, ignores it and serves page 1. The files *are* that slice's, but fixing it means deciding what cursor-mode paging should link to — which is precisely the cursor wiring **D1 declined** — so leaving it was right. It is worse than first recorded: `HasPrev = lastArticleNum > 0` is **unconditionally true** in cursor mode, so **First, Previous and Next all land on page 1** — a cursor view is a forward dead end, not just a missing Next. Reachability was low (D1 added no cursor links, so a visitor only got there by hand-typing `?cursor=`) — but **D1's own clamp note then recommended the cursor parameter**, inviting visitors into exactly that dead end, which is the same sin D1 set out to fix. Mitigated at integration: the note now states the page bound and no longer points at `?cursor=`, with a comment and an inverted test assertion recording why and what to restore once cursor navigation links through. D1 did also *improve* cursor mode's Last link, from a silently clamped `?page={{.TotalPages}}` to an honest `?page=101`. Options when it is taken up: carry the cursor through the Next/Prev hrefs (i.e. finally do the D1 cursor wiring), or suppress both links in cursor mode. **Related residual, no action:** `web/templates/thread.html` builds its own pagination from the `gin.H` keys instead of `PaginationInfo`. It is *correct* after E4 because `totalPages` is now correct, but it is a second, duplicated pagination block; unifying it was out of scope. **Not assigned — bundle with any future cursor decision.** |

Slices: FU:E12, FU:E14, FU:E17 → `adf-web-pagination`; FU:E13, FU:E15 → `adf-db-readers`; FU:E16 → `adf-db-cache`.

### Optional findings (wave O1/O2; a run includes them only when the user says so at the wave-2 boundary)

All `audit-delta-fixes*` medium/low rows not listed above. Anchors are the audit's (unchanged since 84c2081).
| ID | File(s) | Slice |
|----|----|----|
| A-web-6, A-web-7, A-web-8, A-web-9, A-web-10 | `models/sanitizing.go`, `models/cache.go`, `cache/sanitized_cache.go` | adf-opt-sanitize |
| A-web-12, A-web-14, A-web-16, A-web-18, A-web-19 | `web/templates/aichat.html`, `web/web_admin_provider.go`, `web/web_admin_newsgroups.go`, `models/models.go` (`GetData` only), `web/webserver.go` (`Shutdown` only) | adf-opt-web-admin |
| A-db-7, A-db-8, A-db-10, A-db-14 | `database/hierarchy_cache.go` | adf-opt-hierarchy |
| A-tools-7, A-tools-8, A-tools-9, A-tools-10, A-tools-11 | `processor/analyze.go`, `cmd/nntp-analyze/main.go` | adf-opt-analyze |
| A-tools-12, A-tools-13 | `cmd/tcp2tor/main.go`, `cmd/tcp2tor/example.cfg` | adf-opt-tcp2tor |
| A-tools-14, A-tools-15, A-tools-16 | `cmd/rslight-importer/main.go`, `processor/rslight.go`, `processor/rslight_articles.go` | adf-opt-rslight |
| A-tools-6, A-tools-17, A-tools-18, A-tools-20 | `database/progress.go`, `cmd/import-flat-files/main.go`, `cmd/{extract_hierarchies,merge-active,merge-descriptions}/`, `cmd/nntp-fetcher/main.go` | adf-opt-tools-misc |
| A-ops-6, A-ops-7, A-ops-8, A-ops-10 | `cmd/expire-news/main.go`, `cmd/fix-references/main.go`, `cmd/fix-thread-activity/main.go`, `cmd/recover-db/main.go`, `cmd/usermgr/main.go` | adf-opt-ops-tools |
| A-ops-11, A-ops-12 | `cmd/web/main.go`, `cmd/web/main_functions.go`, `database/utils.go` | adf-opt-web-maint |
| A-intake-10 | `database/db_post_queue.go`, `cmd/post-queue/main.go` | adf-opt-postqueue |
| A-tools-19, A-ops-17, A-tools-21 | tracked binaries, `active_files/hierarchies/organize_by_theme.go`, build-script ldflags, help texts/READMEs | adf-opt-artifacts-docs (wave O2) |

### Deferred (no slice; reason)
- **A-db-3** (duplicate message-ids counted, second `threads` row): the count lives in the stats block and the
  history re-add in PHASE 3 of `db_batch.go`, both owned by `nntp-intake-injection`. Hand-off note: count only rows
  whose `stmt.Exec` RowsAffected is 1 and skip the rest in PHASE 2/3; `ExistsMsgIdInArticlesDB` → `(bool, error)`.
- **A-db-4** (`updated_at` = 0001-01-01 for crossposts): the `latestDate` computation is in PHASE 3 and the stats
  block (intake-owned). Hand-off note: compute `latestDate` over all committed articles before the clearing step and
  write `updated_at = MAX(updated_at, excluded)` skipping a zero time.
- **A-tools-5b** (progress stored before commit): `processor/proc_DLArt.go:316-322` is intake-owned; needs a
  commit-acknowledged high-water mark from the batch writer (a design, not a fix).
- **A-ops-3d** (`cmd/history-rebuild/main.go:161`) and **A-ops-3f** (`cmd/nntp-transfer/main.go:347`): done by
  `nntp-client-transfer` (step 5a); closed here after the wave-0 grep (hand-off 3).
- Notes from the `nntp-client-transfer` run (2026-10-15): A-ops-3d/f and the A-tools-3 client guard are in; A-tools-4 is
  closed. Follow-up candidates this plan may pick up or record: `Pool.XHdrStreamed` still drops the stream error, so
  `processor/proc_DLArt.go` sees a cut-off XHDR stream as a normal channel close (unchanged behaviour); `TransferResult`
  in `internal/nntp/transfer-progress.go` and `Batch.GroupQueues`/`GetOrCreateGroupBatch` are unused; `NewsgroupExists`
  keeps "any row exists" semantics (no caller); `.github/copilot-instructions.md:50,126` still mention `cmd/test-nntp`
  (deleted); the `pprof.Index` page lists a `cmdline` link that answers 404.
- **OPS-1, parts in files other plans own or the code owner froze:** `history/history.go:469` (`beginTx` `log.Fatalf`)
  and `:498` (`os.Exit(1)`, marked "DO NOT TOUCH THIS LINE OF CODE" by the author: a deliberate fail-stop so that a
  failed history write is never acknowledged; changing it is a user decision, Q3); `config/config.go:548`
  (exempt by D5: it runs before any resource is opened); `database/nntp_auth_cache.go` `cleanupExpired`
  (hardening-owned); the teardown of `cmd/nntpmgr` (hardening), `cmd/nntp-server` (hardening), `cmd/nntp-transfer`
  (client), `cmd/rslight-importer` (optional slice `adf-opt-rslight` covers A-tools-16's exits), `cmd/usermgr`,
  `cmd/fix-thread-activity`, `cmd/fix-references` (optional `adf-opt-ops-tools`).
- **A-ops-13** (destructive root scripts `history-rebuild.sh`, `delete_ALL_users.sh`): CLAUDE.md lists them as never
  to run; editing them is a user decision (Q6). `scripts.sh`'s destructive first line is neutralized by OPS-4d.
- **A-ops-14** (update pipeline): `getUpdate.sh*`, `rsync*.sh`, `createUpdate.sh`, `bump.sh` are off-limits (CLAUDE.md).
- **A-ops-16** (`php/` debug pages): separate deployment, not part of the Go build; user decision.
- **govulncheck upgrade** (Go 1.27.2, `golang.org/x/net` v0.60.0, `quic-go` v0.59.1, AUDIT §8): changes `go.mod`
  (K4 of every plan); user decision (Q4).

### Cross-plan hand-offs (the orchestrator relays them; this plan does not edit those files)
1. **A-tools-1 → `nntp-intake-injection` slice S3 (`intake-processor`)** (final cross-plan decision (i); the intake
   plan is amended by the orchestrator). Why the bound can't live in a database file: the call site spawns the
   goroutine before `BatchCaptureOverviewForLater` runs, so any wait inside it only parks more goroutines; and the
   call can't simply become synchronous, because `processArticle` holds `article.Mux` for its whole body
   (`threading.go:67-68`) while `BatchCaptureOverviewForLater` and `BatchDivider` lock that same mutex
   (`db_batch.go:157`, `:1769`). Agreed spec (implemented in intake's `threading.go` and new `intake.go`):
   - a `captureLimiter` (bounded slot channel, `MaxCaptureInFlight`) in `processor/intake.go`;
   - `processArticle` takes **one slot per article at the top, before `article.Mux.Lock()`**, in bulk and non-bulk
     mode alike (no lock is held while waiting, so there is no lock cycle; NNTP intake gets back-pressure too). The
     wait gives up when `proc.DB.IsDBshutdown()` reports true;
   - every early return of `processArticle` (dupes, rejects, errors, nothing queued) releases the slot;
   - the per-group `go … BatchCaptureOverviewForLater` calls become **one goroutine per article** that captures all
     its groups in order and releases the slot after the last one;
   - all `article.NewsgroupsPtr` entries are appended while `processArticle` still holds the lock (so the capture
     goroutine's own append is a no-op).
   This plan's part: A-db-9 in `adf-db-batch` (the divider's `MaxQueued` brake becomes real). Wave 0 and wave 3 check
   `grep -n 'captureLimit' internal/processor/intake.go internal/processor/threading.go` (limiter defined and used in
   `processArticle`) and that `grep -c 'go proc.DB.Batch.BatchCaptureOverviewForLater' internal/processor/threading.go`
   prints 0 (no per-group goroutine left). When the amendment was not applied, A-tools-1 is reported as still open in
   the Outcome (this plan never edits `threading.go`).
2. **A-tools-4 → closed by `nntp-intake-injection` S5** (per-run `ReturnQ`). Wave 0 checks
   `grep -n 'func newDownloadRun' internal/processor/proc_DLArt.go`; if present the id is recorded as closed; if not,
   it is reported as open (the `-max-batch` clamp half moves to the optional `adf-opt-tools-misc`).
3. **A-ops-3d / A-ops-3f → done by `nntp-client-transfer`** (its `cli-transfer` step 5a; final decision (iii)).
   Closed here; wave 0 checks `grep -n 'syscall.SIGTERM' cmd/history-rebuild/main.go cmd/nntp-transfer/main.go`
   (one hit each) and records them as closed, otherwise as open.
4. **A-db-3, A-db-4 → `nntp-intake-injection`** (stats block / PHASE 3), notes in "Deferred".
5. **From `nntp-rfc-commands` (2026-10-15):** `cmd/web/main.go:440-446` still clears `TLSCert`/`TLSKey` when no TLS
   port is set, so the embedded NNTP server in `cmd/web` could never offer STARTTLS; D10 of this plan removes that
   dead block, which closes the gap. `cmd/nntp-server` now accepts the certificate flags without `-nntptlsport`
   (STARTTLS on the plain port) and fails at startup when only one of the two is given or the pair does not load.

### Decisions (behaviour changes; report them to the user at the end)
- **D1 CSP (A-web-13, WEB-2).** Every response gets an enforced
  `Content-Security-Policy: default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://fonts.googleapis.com; font-src 'self' data: https://cdn.jsdelivr.net https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'`
  via `secure.Config.ContentSecurityPolicy`. `'unsafe-inline'` stays for scripts because 9 templates have inline
  `<script>` blocks and 14 inline `on*=` handlers; the XSS fix itself is the escaping (WEB-2), the policy limits the
  blast radius (no external script, no exfiltration by `fetch`/`connect`, no plugins, no framing, forms only to
  self). A nonce-based `script-src` is out of scope (Q5). The external hosts are the ones the templates load today
  (`base.html:35-36`, `base_chat.html:13,33-34`).
- **D2 Hide semantics (A-web-1, AUDIT §11.2; same predicate as `nntp-server-hardening` decision 2).** `hide > 0` =
  gone for everyone but admins: article by number, by message-id, both section variants, the preview and the token
  API `GET /api/v1/groups/:group/articles/:articleNum` / `…/message/:messageId` (`getArticle`/`getArticleByMessageId`;
  a token is not an admin, so token holders don't see hidden articles either) answer **404 "Article not found"** to
  non-admins; the site-post reply prefill (`web_sitePostPage.go:86-100`) does not quote a hidden article for a
  non-admin (the form opens without the quote and shows "Article not found"); the tree page renders a hidden node as `[hidden article]` without
  subject, author or link (children stay, the structure is public); `/api/thread-tree` returns hidden nodes without
  `overview`. Admins see everything, with the existing hide counter. `spam` (user flags) is not filtered.
- **D3 `/api/thread-tree` (A-web-11).** Kept, behind `requireAPIEnabled` (like the other `/api/v1` routes), with
  `Cache-Control: private, max-age=60` (never `public`), redacted per D2 for non-admins. No UI caller exists; removal
  would also be acceptable, but gating keeps API users working.
- **D4 Article numbering (INT-5): per-group-DB high-water table, not AUTOINCREMENT.** A new group migration
  `0010_single_article_num_seq.sql` creates `article_num_seq(id INTEGER PRIMARY KEY CHECK (id = 1), last_num INTEGER
  NOT NULL)`, seeds it with `COALESCE(MAX(article_num), 0)` and adds
  `CREATE TRIGGER IF NOT EXISTS articles_num_seq_ai AFTER INSERT ON articles BEGIN UPDATE article_num_seq SET
  last_num = NEW.article_num WHERE id = 1 AND NEW.article_num > last_num; END`. The batch insert passes
  `article_num = (SELECT last_num + 1 FROM article_num_seq WHERE id = 1)`; a missing row yields NULL, which SQLite
  turns into the old `max(rowid)+1` (safe before the migration ran). `ResetNewsgroupData` sets `last_num = 0`
  (numbering restarts on purpose, as today).
  - **Why not AUTOINCREMENT:** SQLite cannot add AUTOINCREMENT to an existing table. It needs a table rebuild
    (create, copy every row with `headers_json`/`body_text`, drop, rename, recreate indexes and triggers) in each of
    the thousands of group DBs. Migrations run lazily on the first `GetGroupDB` of each group, inside one transaction
    per migration (WSH:H9), so a large group would hold its write lock for minutes on first open while readers and
    the batch writer wait, and a crash mid-copy leaves a doubled file on disk. A failed rebuild leaves the group
    unusable until manual repair. The table above is created in microseconds (`MAX(article_num)` is a rowid lookup)
    and is invisible to older binaries (their inserts still work; they only don't use it).
  - **Why not `newsgroups.last_article` in the main DB:** a different database file, so the number cannot be
    assigned and the article inserted atomically; a crash between them loses or reuses numbers.
  - Derived rows: expire-news deletes `thread_cache`, `cached_trees`, `tree_stats` rows whose `thread_root` is
    deleted, and the main-DB `spam`/`user_spam_flags` rows of the deleted numbers; reset and delete invalidate the
    article cache of the group.
- **D5 Fatal → error policy (OPS-1).** Library code (`internal/...`) returns wrapped errors instead of
  `log.Fatal`/`os.Exit`/`panic` for runtime conditions. Where an exported constructor without an error result has
  callers in files this plan does not own, a sibling `...E` variant returning `(T, error)` is added and the old name
  stays as a thin wrapper that keeps today's fatal behaviour (marked `// Deprecated:`); callers this plan owns switch.
  Fatal stays allowed in a `main` before any resource is opened (that is why `config.NewDefaultConfig` is exempt) and
  in code its author froze (history, Q3). After `OpenDatabase`, a binary exits only through a helper that runs
  `close(StopChan)` → `WG.Wait()` (bounded) → `Shutdown()`.
- **D6 SIGTERM (A-ops-3).** Every long-running binary this plan owns (`cmd/web`, `cmd/nntp-fetcher`,
  `cmd/expire-news`, `cmd/post-queue`) handles `syscall.SIGTERM` exactly like `os.Interrupt` (same graceful path).
  No second-signal force exit is added. History-rebuild and nntp-transfer are handed off (hand-off 3).
- **D7 Cron gate (WEB-1, AUDIT §11.10).** `-edit-cronjobs` gates **create** and **enabling** (toggle of a disabled job).
  Disabling, stop and the already gated update/delete keep their behaviour: turning a job off is always allowed. The
  create form and the "enable" button are hidden when the flag is off. This also gates enabling the default cron jobs
  that migration 0021 ships disabled (Q8). Side effect of the A-web-20 fix: a job that was stopped while still
  enabled is re-registered by the manager's reload within a minute (`cronReloadEvery`) and runs again at its next
  scheduled time; to keep it off, disable it. The first-registered-user-becomes-admin rule is not changed here (Q1).
- **D8 Web post headers (INT-7, AUDIT §11.5).** The stored block is, in this order: `Path: .POSTED!not-for-mail`
  (intake's bulk `syncStoredPath` rewrites the value to the prefixed `path` column), `From`, `Newsgroups`,
  `Subject`, `Date` (`common.FormatNewsDate(now)`), `Message-ID`, `References` (replies only), `MIME-Version`,
  `Content-Type`, `Content-Transfer-Encoding`, `Injection-Date` (same instant; RFC 5537 §3.5 step 11: the server
  generated both Message-ID and Date), `Injection-Info: <LocalNNTPHostname>; posting-account="<hash>";
  logging-data="<nonce>"` plus `; mail-complaints-to="<AbuseMail>"` when AbuseMail is set (same hash function as
  intake D6), `Lines`. **`X-pugleaf-Trace` is replaced by `Injection-Info`** (same data, standard header). No `Bytes:`
  header line; `article.Bytes = common.WireArticleSize(headLines, bodyLines)` (RFC 3977 §8.1.1). `DateString` equals
  the `Date:` value. The Message-ID comes from intake's `common.GenerateMessageID(processor.LocalNNTPHostname)` (D4 of
  intake) instead of the web's own `generateMessageID` (deleted if it has no other caller).
- **D9 NNTP user mapping (A-web-5).** Form value `0`/empty = no mapping (stored NULL). A web user may own at most one
  NNTP account: create/update refuse a mapping when `CountNNTPUsersByWebUserID(w) > 0` for another account. Existing
  rows mapped to user 1 are not migrated (an admin unmaps them in the form). Posting is updated independently of
  maxconns. Ranges aligned with `nntp-server-hardening`'s nntpmgr: passwords 10–72 bytes, maxconns 1–25.
- **D10 `cmd/web` NNTP flags (OPS-2a).** **The embedded NNTP server is removed from `cmd/web`** (it could never start;
  `cmd/nntp-server` is the NNTP binary). `adf-web-cmd` deletes the dead block `cmd/web/main.go:428-473` (incl. `:448`;
  intake S4 no longer edits it, final decision (ii)) and the `withnntp`/`withfetch`/`isleep` variables; `-nntptcpport`, `-nntptlsport`,
  `-nntpcertfile`, `-nntpkeyfile` stay parsed (scripts pass them) and log one
  `[WEB]: -nntptcpport/-nntptlsport/-nntpcertfile/-nntpkeyfile are ignored: run cmd/nntp-server` line when set.
- **D11 — DONE 2026-10-12 outside this plan (user decision: "remove all -race from normal build scripts and fix the docs"):**
  `-race` was removed outright from the 11 build scripts (no `${RACE:+-race}` opt-in), and `.claude/CLAUDE.md` plus
  `.github/copilot-instructions.md` were corrected, in a commit on `testing-001`. Q9 is answered; `adf-ops-scripts` skips
  OPS-4e and the CLAUDE.md sentence (E16's `grep -nE 'go build[^$]*-race' build_*.sh` is already empty).
- **D11 Build scripts (OPS-4e) — original text (superseded):**
  Proposed: the 11 scripts build without `-race`; `RACE=1 ./build_<x>.sh` restores it
  (`go build ${RACE:+-race} …`). Production binaries built through `build_ALL.sh` are therefore no longer race
  builds. Verifiers that want race detection (WSH E19, the client plan's E14, this plan's E19) build with `RACE=1`.
  `.claude/CLAUDE.md`'s "(output: `build/`, built with `-race`)" becomes "(output: `build/`; `RACE=1` for a `-race`
  build)" — only on a yes. On a no, `adf-ops-scripts` leaves the 11 scripts and CLAUDE.md alone and OPS-4e is
  recorded as declined.
- **D12 Release workflow (A-ops-2, OPS-4f, A-ops-15).** One native runner per target with `CGO_ENABLED=1`
  (`ubuntu-24.04` linux/amd64, `ubuntu-24.04-arm` linux/arm64, `macos-14` darwin/arm64, `macos-13` darwin/amd64,
  `windows-latest` windows/amd64 with `shell: bash`), `fail-fast: false`, `go-version-file: go.mod`, a `test` job
  (`go vet ./...`, `go test -race` on the CLAUDE.md list) that `build` needs, and the dispatch input passed through
  `env:` and validated against `^[0-9]+\.[0-9]+\.[0-9]+$`. Pinning actions by commit SHA needs network lookups and
  is an optional step. A push/PR `tests.yml` is a separate user decision (Q2, optional step).
- **D13 `recover-db` directories (A-ops-1).** `-db` becomes a deprecated alias of `-data`: given alone it sets the
  data dir; given together with a different `-data` the tool exits 2 before opening anything. "Data Path" prints the
  directory actually opened. The write modes (`-repair`, `-rebuild-threads`, `-rewritedates`,
  `-reorder-by-datesent`) require an explicit data directory (`-data`, or the deprecated `-db` alias given on the
  command line, which counts as explicit) and `-force`; without them the tool prints what it would do and
  exits 2. `-rewritedates` updates only `date_sent` and skips (and reports) strings that `ParseNNTPDateStrict`
  rejects.
- **D14 `-disable-active` (A-ops-4).** Refuse (exit 1, nothing changed) when the file yields no group names.
  `-disable-active-dry-run` prints the groups instead of updating. Lines with one field (a plain group list) are
  accepted. **No percentage threshold and no `-disable-active-force` (user decision 2026-10-15, Q10: "there is no cut
  off needed. there was none, we need none. the dry-run makes sense.").**
- **D15 Cursor-mode paging (FU:E12).** Cursor mode suppresses Previous and Next (`HasPrev = HasNext = false` in the
  cursor `PaginationInfo` literal); the cursor wiring that D1 of `web-db-followups` declined stays out of scope.
- **D16 A-tools-1 routing.** Handed to `nntp-intake-injection` (hand-off 1), not implemented in a database file, for
  the reasons given there.

### Out of scope (list them in Outcome)
- Nonce-based CSP and moving inline scripts/handlers to `/static` (Q5).
- Cursor wiring for the group/section listings (D15).
- The first-registered-user-becomes-admin rule (Q1).
- Everything under "Deferred" and every optional id when the user does not include wave O1/O2.
- NNTP-side hide filtering, `GetArticleByNum`'s `DBArtNum`, the overview range reads (`nntp-server-hardening`).

---

## Design

### Contracts
- **K1: stable signatures (unchanged by this plan).**
  - `web`: `NewWebServer(db, webconfig, nntp, cronEdit, noCronjobs) *WebServer`, `WebServer.Router`/`DB`/`CronEdit`/
    `CronManager`, `getWebSession`, `checkGroupAccess`, `checkGroupAccessAPI`, `requireAdminAuth`, `requireAPIEnabled`,
    `isAdmin`, `isAdminRequest`, `renderPage`, `renderError`, `getBaseTemplateData`, `truncateRunes`.
  - `database`: `OpenDatabase(*DBConfig) (*Database, error)`, `(*Database).Shutdown() error`, `StopChan`, `WG`,
    `GetGroupDB`/`Return`, every `Retryable*` helper, `MainDBGetNewsgroup`/`ByID`/`MainDBGetAllNewsgroups`,
    `GetActiveNewsgroups*`, `(*SQ3batch).BatchCaptureOverviewForLater(*string, *models.Article)` (callers in
    intake-owned `threading.go`), `GetNewsgroupPointer`, `NewArticleCache`, `(*ArticleCache).Get/Put/Remove/Clear/
    ClearGroup`, `NewMemCachedThreads()`, `NewGHmap()`, `NewGroupSectionDBCache()`, `GetCachedThreadReplies`,
    `GetThreadTreeView`, `(*ThreadTree).GetThreadTreeHTML(string)`, `ResetNewsgroupData`, `BulkDeleteNewsgroups`,
    `DeleteNewsgroup`, `UpdateHierarchyCounts`, `IsDBshutdown`.
  - `processor`: `NewProcessor(db, pool, useShortHashLen) *Processor` (callers in hardening/client-owned
    `cmd/nntp-server`, `cmd/nntp-transfer`, and `processor/rslight.go`).
  - `models`: `(*Article).PrintSanitized`, `(*Overview).PrintSanitized`, `BatchSanitizeArticles`, `ConvertToUTF8`,
    `StripDangerousHTML` (kept, no longer called by the display path).
  - `cache`: `NewSanitizedCache`, `(*SanitizedCache).GetField/SetField/SetArticle/BatchSetArticles/GetArticle/Stop`.
  - Contracts consumed from siblings: hardening `GetNNTPUserByWebUserID` (lowest id, `sql.ErrNoRows` for ≤ 0),
    `CountNNTPUsersByWebUserID(int64) (int, error)`, `UpdateNNTPUserWebUserID(int, int64) error`; intake
    `common.FormatNewsDate(time.Time) string`, `common.WireArticleSize(head, body []string) int`,
    `processor.ParseNNTPDateStrict(string) (time.Time, bool)`.
- **K2: new names reserved per slice** (free at 2fb7741: `grep -rnw '<name>' internal cmd` is empty; the slice
  re-checks after the siblings merged):
  - `adf-web-cron`: `(*CronJobManager).forgetJob`, `AdminPageData.CronEdit`, `cronEditRefused`.
  - `adf-web-xss`: `contentSecurityPolicy` (package const), JS `renderPreviewNode` (method of `ArticlePreview`).
  - `adf-web-post`: `buildWebPostHeaders`, `webPostHeaderInput`.
  - `adf-web-nntpusers`: `nntpAdminPasswordMin`, `nntpAdminPasswordMax`, `resolveNNTPWebMapping`.
  - `adf-web-pagination`: none (edits only).
  - `adf-web-cache`: `(*SanitizedCache).enforceLimitLocked`, `sanitizedArticleFields`, `(*Article).sanitizedFields`.
  - `adf-db-readers`: `query_RepairNullHierarchy` (none in Go if the migration suffices), migration file
    `0029_main_repair_null_hierarchy.sql`, `query_BulkDeleteSpamRows`, `query_DeleteSpamRowsByNewsgroup`.
  - `adf-db-cache`: `ArticleCacheEntry.key`, `NewMemCachedThreadsStop`, `(*MemCachedThreads).stop` (field).
  - `adf-db-lifecycle`: `Database.bgStop`, `Database.bgStopOnce`, `(*Database).stopBackground`,
    `(*Database).backgroundStop`, `NewGHmapStop`, `newGroupSectionDBCacheStop`, `ErrRunAsRoot`.
  - `adf-proc-fatal`: `NewProcessorE`.
  - `adf-tools-fetcher`: `fetcherBackendConfig`, `openFetcherDB`, `fetcherShutdown` (file `cmd/nntp-fetcher/backend.go`).
  - `adf-ops-recover`: `resolveRecoverDataDir`, `recoverWriteModes`, `recoverExit`, `query_rewriteDateSent`.
  - `adf-ops-tcp2tor`: `socksForwardDialer`, `dialThroughSOCKS`.
  - `adf-web-hide`: DB `(*Database).GetArticleFlags`, `(*Database).GetArticleFlagsByMessageID` (new file
    `database/web_article_flags.go`), `(*ThreadTree).RedactedCopy`, `(*ThreadTree).GetThreadTreeHTMLFor`,
    `ArticlePageData.ArticleSpam`, `ArticlePageData.ArticleHide`, `(*WebServer).articleHiddenFor`.
  - `adf-db-batch`: migration `0010_single_article_num_seq.sql`, `SQ3batch.dividerChan`, `SQ3batch.queuedN`
    (if an atomic is used), `(*Database).DeleteSpamRowsForArticles` (new file `database/article_num_cleanup.go`),
    expire-news `deleteDerivedThreadRows`.
  - `adf-web-cmd`: `exitAfterDB`, `fatalAfterDB`, `webStop` (channel), `disableActiveOpts`, flag
    `-disable-active-dry-run`.
- **K3: tests.**
  - Package `web` tests use the existing w0 harness (`internal/web/testmain_test.go`: `w0Srv`, `w0Do(t, w0Req{…})`,
    `w0NewUser`, `w0NewGroup`, `w0DB`, `w0Name`, `lo1CoreSetAPIEnabled`); package `database` tests use `w0DB`/`w0Name`
    and the `fuBatch*` helpers (`fu_batch_test.go`). No new `TestMain` in those two packages.
  - New `TestMain`s only where no test binary opens a DB yet: `cmd/web/adf_web_test.go` (adf-web-cmd),
    `internal/database/adfreopen/reopen_test.go`
    (adf-db-lifecycle, its own package so it can open, shut down and re-open). Each checks `os.Geteuid() >= 1000`
    before `OpenDatabase` (skip with a printed reason otherwise), uses `os.MkdirTemp("", "pugleaf-adf-")`, sets
    `database.NO_CACHE_BOOT = true` before the open and never uses `./data`.
  - File and identifier prefixes per slice: `internal/web/adf_<slice>_test.go` with `adfCron…`/`TestAdfCron…`,
    `adfXSS…`, `adfPost…`, `adfNNTPUsers…`, `adfPage…`, `adfHide…`; `internal/cache/adf_sanitized_cache_test.go`
    (`TestAdfSanCache…`), `internal/models/adf_sanitizing_test.go` (`TestAdfSanitize…`);
    `internal/database/adf_<slice>_test.go` with `adfReaders…`, `adfCache…`, `adfLife…`, `adfBatch…`, `adfFlags…`;
    `internal/processor/adf_proc_test.go` (`TestAdfProc…`); `cmd/<tool>/adf_<tool>_test.go` (`TestAdfFetch…`,
    `TestAdfRecover…`, `TestAdfTor…`, `TestAdfWebCmd…`, `TestAdfExpire…`); optional slices `adfOpt<Slice>…`.
  - `cmd/nntp-fetcher` gets no `TestMain`: its one DB test opens and tears down the DB itself (adf-tools-fetcher).
  - Groups `w0Name("adf<slice>")`, message-ids `<adf-<slice>-<seq>@test.invalid>`. No `t.Parallel()`. A test that
    changes a shared field (`w0Srv.CronEdit`, `processor.LocalNNTPHostname`) restores it with `t.Cleanup`; no package
    global read by a background goroutine is changed after `TestMain`. No sleep over 100 ms (poll with a deadline).
- **K4:** nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt`, the off-limits release/deploy files
  of CLAUDE.md, or files outside their ownership list. `gofmt` runs only on owned files; the 2-file baseline
  (`internal/database/embedded_migrations.go`, `internal/web/web_admin_provider.go`) stays — `adf-opt-web-admin` edits
  `web_admin_provider.go` without running `gofmt -w` on it and reports the baseline line as expected.
- **K5:** no slice calls a helper another slice of the same wave adds (example: `adf-web-cmd` uses `NewProcessorE`
  from wave 1; `adf-db-batch` wires `NewMemCachedThreadsStop` from wave 1 into `db_init.go`).
- **K6: ownership against the sibling plans (checked).** Files this plan touches that a sibling also touches, with the
  disjoint part:
  | File | Sibling part | This plan's part |
  |----|----|----|
  | `database/queries.go` | hardening: `GetOverviewsRange`, `GetHeaderFieldRange` + constants (`:1804-1937`), one line in `GetArticleByNum` (`:906`) | `MainDBGetAllNewsgroups`, `MainDBGetNewsgroup`, `MainDBGetNewsgroupByID` + constants (`:190-257`), `BulkDeleteNewsgroups` (`:402`), `DeleteNewsgroup` (`:486`), `GetOverviews` (`:698`), `GetActiveNewsgroups*`, `GetNewsgroupsPaginated*` (`:1030-1137`), `getHierarchiesPaginatedDirect` (`:2187`), `GetNewsgroupsByHierarchy` (`:2814`), `GetSpamArticles` count constant (`:3334`) (wave 1); `ResetNewsgroupData` (`:3060`, wave 2) |
  | `database/db_batch.go` | intake S2: `query_updateNewsgroupsStats`, PHASE 1 number loop (`:794-823`), `batchInsertOverviews` (`:969-1001`), `processOverviewBatch` (`:1003-1103`), PHASE 3 (`:855-891`), stats block (`:935-967`) | `SQ3batch` struct, `NewSQ3batch`, `BatchCaptureOverviewForLater`, `ExpireCache`, `GetNewsgroupPointer`, `CheckNoMoreWorkInMaps` (`:273`), the `sq.queued -= batched` hunk (`:764-768`), the `query_processOverviewBatch` constant only (on top of intake's version), `StartOrch`, `BatchDivider`, `BatchDividerChan` (wave 2) |
  | `cmd/history-rebuild/main.go` | client: `:12`, `:75`, `:97-104` (pprof) | `:74`, `:79` (`-useshorthashlen`) only |
  | `models/models.go` | intake S1: `Article` struct, `RecycleArticle` | optional `adf-opt-web-admin`: `(*Article).GetData` only |
  Files checked and **not** touched because a sibling owns them: `internal/nntp/*` (all but the unowned
  `nntp-peering-pattern_test.go`), `cmd/nntpmgr/main.go`, `database/db_nntp_users.go`, `database/nntp_auth_cache.go`,
  `database/nntp_*.go` (new files of hardening/intake/rfc-commands), `processor/{threading.go, proc-utils.go,
  PostQueue.go, proc_DLArt.go, intake.go}`, `common/*`, `cmd/nntp-server/*`, `cmd/nntp-transfer/*`,
  `cmd/test-nntp/`, `scripts/test-nntp-server.sh`. Not owned by any sibling and used here: `processor/processor.go`,
  `processor/analyze.go`, `internal/web/*`, `internal/cache/*`, `models/sanitizing.go`, `models/cache.go`,
  `database/{db_init,database,db_migrate,groups_hashmap,sections_cache,article_cache,thread_cache,hierarchy_cache,
  tree_view_api}.go`, `cmd/{web,nntp-fetcher,recover-db,expire-news,post-queue,tcp2tor}/`, root scripts,
  `.github/workflows/release.yml`.

### Component design
- **Background stop (OPS-1a/d/e).** `Database` gains `bgStop chan struct{}` (made in `OpenDatabase`) and
  `bgStopOnce sync.Once`; `stopBackground()` closes it once and is called at the **start of `Shutdown()`** (not on
  `StopChan`: after `close(StopChan)` the orchestrator still drains, LO:F20, and needs the divider). Loops started by
  `OpenDatabase` (`GHmapCron`, `CronClean`, `CleanCron`, `ExpireCache`, `BatchDivider`) become `for { select { case
  <-stop: return; case <-time.After(d): … } }`. Constructors keep their names (K1) and gain `…Stop(stop <-chan
  struct{})` siblings; the old ones pass a nil channel (blocks forever = today's behaviour).
- **INIT.** `OpenDatabase` sets `INIT = true` only after every step succeeded; on any error after `initMainDB` it
  closes `mainDB`. `Shutdown()` sets `INIT = false` under `GlobalDBMutex` as its last step.
- **Article cache key (A-db-2).** `ArticleCacheEntry` stores the `key` it was inserted under; `removeElement`,
  `Cleanup` and `evictIfNeeded` delete by `entry.key`; no `GetNewsgroupPointer` call remains in `article_cache.go`.
- **Preview rendering (WEB-2).** The server keeps returning plain decoded text (JSON); the browser builds the preview
  with `document.createElement` + `textContent` (`white-space: pre-wrap` on the body element) and an `<a>` whose
  `href` is `'/groups/' + encodeURIComponent(group) + '/articles/' + Number(article_num)`. No article field reaches
  `innerHTML` anywhere in `thread-tree.js`.
- **Visibility helper (A-web-1).** `(s *WebServer) articleHiddenFor(c *gin.Context, hide int) bool` = `hide > 0 &&
  !s.isAdminRequest(c)`; flags come from `GetArticleFlags*` (one indexed row read), never from the cached `*Article`.

---

## Waves

Common rules for every slice:
- Follow K1–K6; read the findings rows for your ids, every owned file, and the callers/callees named in the steps.
- Locate code by function name; line numbers are hints at 2fb7741.
- Checks in your worktree:
  ```bash
  gofmt -l <each owned dir>               # none of your owned files listed (baseline files excepted, K4)
  go vet ./...
  go build ./...
  go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
    ./internal/nntp/... ./internal/processor/... ./internal/cache/... ./internal/models/... \
    ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/... ./cmd/web/... \
    ./cmd/nntp-fetcher/... ./cmd/recover-db/... ./cmd/tcp2tor/...
  git status --short                      # clean: no data-test-*, active.out, analysis_*.txt, zz_* files
  ```
  plus the slice-specific checks. Scratch binaries run only with `-data ./data-test-audit-delta-fixes-<slice>` (inside
  the worktree) on ports 18990–18999; never against `./data`, never a fetcher against a real server.
- Report: ids fixed, tests added, any change needed outside your files (do not make it).

### Wave 0 (inline, orchestrator on `plan-audit-delta-fixes`)
Owned files: `scripts/test-audit-delta-fixes.sh` (new).

1. **Preflight** as in the skill (`git mv` this plan to `wip/`). Verify the dependencies are merged into the branch:
   - hardening: `grep -n 'func (db \*Database) CountNNTPUsersByWebUserID' internal/database/*.go` and
     `grep -n 'func (db \*Database) UpdateNNTPUserWebUserID' internal/database/*.go` both hit;
   - intake: `grep -n 'func FormatNewsDate\|func WireArticleSize' internal/common/*.go` (2 hits),
     `grep -n 'func ParseNNTPDateStrict' internal/processor/*.go`, and `grep -n 'UPDATE newsgroups SET' internal/database/db_batch.go` (the stats statement is an UPDATE);
   - client: `grep -n 'loopbackPprofAddr' cmd/history-rebuild/*.go` hits;
   - rfc-commands: `ls internal/nntp/nntp-cmd-date.go`.
   Any miss: stop and report (the plan must not run earlier, see header).
2. **Hand-off checks** (record in the progress note, no edits): the two A-tools-1 greps of hand-off 1 (limiter
   defined and used, no per-group capture goroutine left), `grep -n 'func newDownloadRun' internal/processor/proc_DLArt.go`
   (A-tools-4 closed?) and `grep -n 'syscall.SIGTERM' cmd/history-rebuild/main.go cmd/nntp-transfer/main.go`
   (A-ops-3d/f closed?).
3. **Re-anchor:** for every row of the findings tables, re-run `grep -n` on the cited symbol at HEAD; record moved
   anchors in the progress note (slices read that note).
4. **Baseline:** run `## Checks`; gofmt may list only the 2 baseline files. Record PASS/FAIL.
5. **Write `scripts/test-audit-delta-fixes.sh`** from `## End-to-end` (`chmod +x`, `bash -n`). Run it once on the
   baseline with `RACE=1 ./build_webserver.sh` etc.; most E-checks are expected to FAIL. Record the list.
6. **Commit** `test: wave-0 e2e script for audit-delta-fixes` (include the plan move). Record the base SHA.

### Wave 1 (15 parallel `pugleaf-implementer` slices; batches of 4–5)

#### Slice `adf-web-cron`: cron gate, dead-after-disable, log filter
Ids: WEB-1, A-web-20, A-web-17. Smoke port 18990.

Owned files:
- `internal/web/web_admin_crons.go`
- `internal/web/cronjobs.go`
- `internal/web/web_admin.go` (only the `AdminPageData` struct: add `CronEdit bool`)
- `internal/web/web_adminPage.go` (only the `AdminPageData{…}` literal at `:522`: set `CronEdit: s.CronEdit`)
- `web/templates/admin_crons.html`
- `internal/web/adf_cron_test.go` (new)

Changes:
1. **Create (WEB-1).** In `adminCreateCronJob`, right after the auth check: `if !s.CronEdit { session.SetError(cronEditRefused);
   redirect /admin?tab=crons; return }` with `const cronEditRefused = "Adding or enabling cron jobs is disabled (start with -edit-cronjobs)"`.
   Nothing is written.
2. **Toggle (WEB-1, D7).** In `adminToggleCronJob`, before `ToggleCronJob`: load `job, err := s.DB.GetCronJobByID(cronID)`
   (error → "Cron job not found"); when `!job.Enabled && !s.CronEdit` refuse with `cronEditRefused`; disabling is
   always allowed. Keep the existing success message.
3. **Stop:** unchanged (D7).
4. **A-web-20.** `runJobScheduler` restarts itself today with a tail call `go cm.runJobScheduler(job)`
   (`cronjobs.go:452`), so a deferred cleanup would fire after every run and orphan schedulers (multiplying `sh -c`
   runs). First turn the body into a loop: `for { …one cycle… }` where each former `return` that ends the scheduler
   stays a `return` and the tail call `go cm.runJobScheduler(job)` becomes the end of the loop iteration (`continue`).
   Then add `func (cm *CronJobManager) forgetJob(job *CronJob)`: under `cm.mutex.Lock()` delete `cm.jobs[job.ID]` only
   when `cm.jobs[job.ID] == job` (pointer identity, so a newer registration survives), and call it with
   `defer cm.forgetJob(job)` as the first statement of `runJobScheduler`: with the loop, the defer runs only when the
   scheduler really ends (closed, lookup error, disabled, stop channel). Grep for any other `go cm.runJobScheduler`
   site; there must be exactly one start per registration (in `startJob`). Check that `StopCronManager` and `StopJob` don't hold
   `cm.mutex` while waiting for schedulers to return (they would now deadlock); if one does, release the lock before
   waiting and say so in the report. `loadAndStartJobs` then re-registers a re-enabled job on the next reload.
5. **A-web-17.** In `adminViewCronJobLog` apply `limit` to `displayOutput` after filtering:
   `if len(displayOutput) > limit { displayOutput = displayOutput[len(displayOutput)-limit:] }` (replace the
   `totalLines > limit` overwrite; keep the header lines that the existing code prints based on the filtered count).
6. **Template.** `admin_crons.html`: wrap the create form (`:163-226`) in `{{if .CronEdit}}…{{end}}` and the
   "enable" toggle button of disabled jobs (inside the job `range`, so `{{if $.CronEdit}}`); when off, show the existing hint text (`:13-14`). Disable buttons stay.
7. **Tests** `adf_cron_test.go` (admin cookie from `w0NewUser(t, true)`; set `w0Srv.CronEdit` per test and restore it
   with `t.Cleanup`; jobs inserted with `w0DB(t).InsertCronJob(&models.CronJob{Name: w0Name("adfcron"), Command: "true", Enabled: false, …})`, deleted in cleanup):
   - `TestAdfCronCreateRefusedWithoutEdit`: POST `/admin/crons/create` with `enabled=on` → 303, flash contains
     `-edit-cronjobs`, no row with that name exists.
   - `TestAdfCronToggleEnableRefused`: disabled job, toggle → still disabled; enabled job, toggle → disabled (allowed).
   - `TestAdfCronCreateAllowedWithEdit`: `CronEdit=true` → row created (then deleted).
   - `TestAdfCronForgetJob`: a `CronJobManager` from `NewCronJobManager(w0DB(t))` (not started), register a
     `*CronJob` in `cm.jobs` by `startJob` for a disabled job row, wait (poll ≤ 2 s) until the scheduler returned
     and `cm.jobs` no longer has the id; then enable the row and `startJob` again → no `ErrCronExists`. Stop the
     manager in cleanup.
   - `TestAdfCronSingleScheduler`: (a) source check: `cronjobs.go` (read relative to the package dir) contains
     exactly one `go cm.runJobScheduler(`, inside `startJob`; (b) after `startJob` of an enabled job, sample
     `runtime.Stack(all)` every 50 ms for 2 s: never more than one goroutine with a `runJobScheduler` frame for it.
   - `TestAdfCronLogFilterLimit`: unit test on the filter/limit helper if you extract one (`adminViewCronJobLog`
     needs a live job; extracting the slice logic into an unexported function in the owned file is fine).
   - Admin page without `CronEdit`: GET `/admin?tab=crons` does not contain `action="/admin/crons/create"`.

Acceptance: E02, E03 PASS; `TestAdfCron*` and the existing `TestFuCron*`/`TestW1Server*` pass.

#### Slice `adf-web-xss`: preview escaping and CSP
Ids: WEB-2, A-web-13. Smoke port 18991.

Owned files:
- `internal/web/web_apiHandlers.go` (only `getArticlePreview`)
- `internal/web/static/js/thread-tree.js`
- `internal/web/webserver_core_routes.go` (only the `secure.Config` block `:280-296`)
- `internal/web/adf_xss_test.go` (new)

Changes:
1. **Server (WEB-2).** `getArticlePreview` keeps `models.ConvertToUTF8` + `truncateRunes(…, 500)` (plain text in JSON is
   safe; the browser no longer interprets it). Replace the comment at `:486-487` with: `// Plain text: the browser
   inserts it with textContent (thread-tree.js renderArticlePreview), never as HTML.` Return `group` as
   `groupName` unchanged (the JS encodes it). Nothing else changes in the handler (A-web-1's hide check is wave 2).
2. **JS (WEB-2).** Rewrite `ArticlePreview.renderArticlePreview(contentDiv, article)`:
   - `contentDiv.textContent = ''`; build `const body = document.createElement('div')` with class
     `article-preview-body`, a child `div.preview-text` with `style.whiteSpace = 'pre-wrap'` and
     `textContent = this.formatPreviewText(article.body ? article.body.substring(0, 2000) : 'No content available')`;
   - when `article.body` was truncated or present, append `const a = document.createElement('a')`,
     `a.href = '/groups/' + encodeURIComponent(String(article.group || '')) + '/articles/' + encodeURIComponent(String(Number(article.article_num) || 0))`,
     `a.textContent = '...'`;
   - `contentDiv.appendChild(body)`.
   - Delete the two commented-out HTML blocks (`:523-536`) and the false comment at `:520-521`.
   - Keep `escapeHtml` (now unused) or delete it together with the unused `getAPIToken`; grep the file for callers
     first. The static `innerHTML` literals at `:340,492,502,506` stay (no data).
3. **CSP (A-web-13, D1).** In the `secure.Config` literal add `ContentSecurityPolicy: contentSecurityPolicy` with
   the package const from D1 (one string). Check `github.com/gin-contrib/secure` exposes `ContentSecurityPolicy` in
   the vendored version (`go doc github.com/gin-contrib/secure Config`); if not, set the header in a tiny middleware
   registered next to `secure.New` and say so.
4. **Tests** `adf_xss_test.go` (anonymous; article seeded with raw SQL into a `w0NewGroup(t, true)` group with body
   `hello <img src=x onerror=alert(1)>\n&lt;svg onload=alert(2)&gt;`):
   - `TestAdfXSSPreviewIsPlainJSON`: GET `/groups/<g>/articles/1/preview` → 200, `Content-Type` starts with
     `application/json`, the decoded `body` contains `<svg onload=alert(2)>` as **text** (documenting that the server
     returns text, not HTML) — the protection is asserted on the JS side below.
   - `TestAdfXSSThreadTreeJSUsesTextContent`: GET `/static/js/thread-tree.js` → 200; the body contains
     `textContent` inside `renderArticlePreview` (substring search between `renderArticlePreview(` and
     `formatPreviewText(`), does not contain `${cleanPreview}`, `${article.body`, `${article.group`, and no line
     matches `innerHTML\s*=.*article\.` (regexp).
   - `TestAdfXSSCSPHeader`: GET `/` (and the preview route) carries `Content-Security-Policy` containing
     `default-src 'self'`, `connect-src 'self'`, `object-src 'none'`, `frame-ancestors 'none'`.
   - Pages still render under the CSP: GET `/`, `/groups/<g>`, `/groups/<g>/tree/1` → 200 (no template change).

Acceptance: E04, E05 PASS; `TestW1API*`, `TestLo1*` preview tests unchanged and green.

#### Slice `adf-web-post`: full header set for web posts
Ids: INT-7. Smoke port 18992.

Owned files:
- `internal/web/web_sitePostPage.go`
- `internal/web/adf_post_test.go` (new)

Changes:
1. Add a pure helper in the owned file:
   ```go
   type webPostHeaderInput struct {
   	Host, From, Subject, MessageID, References, AbuseMail, HashedUser, Nonce string
   	Newsgroups []string
   	Now        time.Time
   	Body       string
   }
   // buildWebPostHeaders returns the stored header lines (D8 order), the Date value and the RFC 3977 :bytes size.
   func buildWebPostHeaders(in webPostHeaderInput) (lines []string, date string, bytes int)
   ```
   - `date = common.FormatNewsDate(in.Now)`; lines exactly as D8 (`Injection-Info` folded as
     `Injection-Info: <host>;` + ` posting-account="<hash>";` + ` logging-data="<nonce>"` (+ `;
     mail-complaints-to="<abuse>"` when non-empty) — continuation lines start with a space);
   - `Lines:` = `strings.Count(body, "\n") + 1` (as today);
   - `bytes = common.WireArticleSize(lines, strings.Split(in.Body, "\n"))`.
2. `sitePostSubmit` (`:356-402`): generate `messageID, err := common.GenerateMessageID(processor.LocalNNTPHostname)`
   first (error → the existing error page path), call `buildWebPostHeaders`, set
   `HeadersJSON = strings.Join(lines, "\n")`, `MessageID = messageID`, `DateString = date`, `Bytes = bytes`,
   `Lines = linesCount`; for replies compute `References` before the helper and pass it in; set
   `article.Headers["references"]` as today, drop `article.Headers["bytes"]` (no `Bytes:` header anywhere) and keep
   `article.Headers["lines"]`. Remove the X-pugleaf-Trace lines (`:364-366`) and the `Bytes:` line (`:371`).
   `Path` field stays `.POSTED!not-for-mail`. Do not touch `PostQueue.go` (intake): if the queue worker needs
   another change, report the exact hunk as a cross-plan note.
3. **Tests** `adf_post_test.go`:
   - `TestAdfPostHeaderBuilder` (pure): for a reply input, the lines contain exactly one each of `Path:`, `From:`,
     `Newsgroups:`, `Subject:`, `Date:`, `Message-ID:`, `References:`, `Injection-Date:`, `Injection-Info:` (with
     continuation lines), `Lines:`; no `X-pugleaf-Trace`, no `Bytes:`; `Date` parses with `net/mail.ParseDate`;
     `bytes` equals `common.WireArticleSize`; a non-reply has no `References:`;
     `common.CheckHeaderBlock(lines, common.HeaderBlockFetch)` (intake S1) returns no error.
   - `TestAdfPostQueuedArticle`: `w0NewUser(t, false)` + active group; POST `/SitePostSubmit` (form fields `newsgroups`,
     `subject`, `body`, as `sitePostSubmit` reads them at `:185-187`) → redirect/200 without error flash; read one article from
     `models.PostQueueChannel` within 2 s (nothing else consumes it in the w0 binary — verify with grep that w0 does
     not start a PostQueueWorker) and assert its `HeadersJSON` lines contain `Message-ID: <a.MessageID>`,
     `Subject: <subject>` and `Date: <a.DateString>`, and `a.Bytes == common.WireArticleSize(...)`.

Acceptance: E08 PASS; `TestW1API*Post*`, `TestLo2Forms*` unchanged and green.

#### Slice `adf-web-nntpusers`: NNTP user mapping in the admin form
Ids: A-web-5 (web half). Smoke port 18993.

Owned files:
- `internal/web/web_admin_nntp.go`
- `web/templates/admin_nntpusers.html`
- `internal/web/adf_nntpusers_test.go` (new)

Changes (D9):
1. `resolveNNTPWebMapping(s *WebServer, raw string, currentNNTPUserID int, currentWebUserID int64) (int64, error)`:
   empty or `0` → 0; otherwise parse (`> 0`), `s.DB.GetUserByID(id)` must succeed ("web user not found"), and when
   `id != currentWebUserID`, `s.DB.CountNNTPUsersByWebUserID(id)` must be 0 ("web user already has an NNTP
   account").
2. `adminCreateNNTPUser`: replace `webUserID := int64(1)` (`:91-97`) by `resolveNNTPWebMapping(s, webUserIDStr, 0, 0)`;
   error → flash + redirect. `InsertNNTPUser` already stores `NULLIF(?, 0)`.
3. `adminUpdateNNTPUser` (`:130-207`): load the user (`GetNNTPUserByID`) first. `maxconns`: when given, validate 1–10
   → 1–25 (hardening's nntpmgr range), else keep the stored value; `posting` is always taken from the form (checkbox semantics: absent = off),
   so call `UpdateNNTPUserPermissions(id, maxConns, posting)` whenever either was submitted (the form always submits
   the checkbox state; verify in the template). Password: when given, `nntpAdminPasswordMin = 10` ≤ len ≤
   `nntpAdminPasswordMax = 72` bytes, else flash. Mapping: when the field is present in the form,
   `resolveNNTPWebMapping(s, raw, id, current)` then `s.DB.UpdateNNTPUserWebUserID(id, w)` (hardening contract).
   Note from the hardening run (2026-10-11): `UpdateNNTPUserWebUserID` returns `sql.ErrNoRows` when no NNTP user
   has that id; map it to a "not found" error in the handler.
   Replace the empty branch at `:199-203`.
4. Template: the create/edit `<select name="web_user_id">` gets a first `<option value="0">— no mapping —</option>`,
   selected by default on create and when `.WebUserID` is 0 on edit.
5. **Tests** `adf_nntpusers_test.go` (admin cookie; NNTP users created through the handler; usernames ≥ 10 chars):
   create without mapping → stored `web_user_id` IS NULL (raw SQL); create mapped to web user A → mapped; second
   create mapped to A → refused; update mapping to 0 → NULL; update posting without maxconns → posting changes,
   maxconns unchanged; maxconns 25 accepted, 26 refused; a 9-byte password refused, 10 and 72 bytes accepted, 73
   refused.

Acceptance: `TestAdfNNTPUsers*` pass; hardening's `TestNsh*` auth tests stay green.

#### Slice `adf-web-pagination`: FU:E12, FU:E14, FU:E17
Ids: FU:E12, FU:E14, FU:E17. Smoke port 18994.

Owned files:
- `internal/web/webgroupPage.go`
- `internal/web/web_sectionsPage.go` (only `sectionGroupPage` `:200-289`)
- `internal/web/web_threadPage.go`
- `internal/web/fu_pagination_test.go` (only the `?page=2` assertions of the thread test, `:250-260`)
- `internal/web/adf_pagination_test.go` (new)

Changes:
1. **E17.** In the page→cursor fallback (`webgroupPage.go:88-93`, `web_sectionsPage.go:244-248`), whenever the
   `OFFSET` lookup fails (`sql.ErrNoRows` or any other error; both fall back to the newest page today), set
   `page = 1` together with `lastArticleNum = 0`, so the `PaginationInfo` built later describes the page actually
   served; log a non-`ErrNoRows` error as today.
2. **E12 (D15).** In both cursor-mode `PaginationInfo` literals (`webgroupPage.go:112-120`, `web_sectionsPage.go:267-275`)
   set `HasPrev: false` and `HasNext: false` with a comment `// cursor mode has no linkable neighbours (FU:E12, D15)`.
   Leave `web/templates/pagination.html` and the D1 clamp note untouched; the existing assertion that the note does
   not recommend `?cursor=` (`fu_pagination_test.go:99-106`) stays.
3. **E14.** In `singleThreadPage` (`web_threadPage.go:80-101`): after `totalPages` is computed and `page` clamped,
   when the clamped page differs from the page that was fetched, call `GetCachedThreadReplies` once more with the
   clamped page (same error handling as the first call).
4. `fu_pagination_test.go:250-260`: keep the root assertion and add: the `?page=2` render of the 50-reply thread
   contains the last reply's subject (the clamped page 1 now carries its replies). Update the comment above it.
5. **Tests** `adf_pagination_test.go`: `TestAdfPageGroupPastEnd` (a 3-page group, `?page=50` → body says page 1, not
   "Page 50"); `TestAdfPageCursorNoNeighbourLinks` (`?cursor=<n>` → no `href="?page=0"`); `TestAdfPageThreadClamp`
   (a 60-reply thread, `?page=9` → page 2 rendered with 10 replies and nav present).

Acceptance: `TestAdfPage*` and all `TestFuPagination*` pass.

#### Slice `adf-web-cache`: sanitized-cache bound, race, no write-back
Ids: A-web-2, A-web-3, A-web-4. Smoke port 18995.

Owned files:
- `internal/cache/sanitized_cache.go`
- `internal/models/sanitizing.go`
- `internal/cache/adf_sanitized_cache_test.go` (new)
- `internal/models/adf_sanitizing_test.go` (new)

Changes:
1. **A-web-2.** `enforceLimitLocked()` (called with `sc.mutex` held at the end of `SetField`, `SetArticle` and
   `BatchSetArticles` when a new message-id was added): while `len(sc.cache) > sc.maxEntries && sc.maxEntries > 0`,
   evict the entry with the oldest `LastUsed` (reuse/repair `evictOldest`, which then has a caller). For large caches
   avoid an O(n²) loop: evict down to 90 % of `maxEntries` in one pass over a sorted slice of (key, LastUsed).
   Drop the per-minute log line when `cleanup` removed nothing.
2. **A-web-3.** `GetField`: read the requested field while holding `article.mux.RLock()` (update `LastUsed` under the
   write lock as today, then read under the read lock, or read under the write lock); no field read after unlock.
3. **A-web-4.** In `sanitizing.go` add `type sanitizedArticleFields struct{ Subject, FromHeader, BodyText, DateString,
   References string }` and `func (a *Article) sanitizedFields() sanitizedArticleFields` computing the same values
   `StripDangerousHTML` writes today (`sanitizeText`, `filterMailDomainInFromHeader`) from the unmodified fields.
   `PrintSanitized` and `BatchSanitizeArticles` use the local struct for those five fields and never call
   `StripDangerousHTML` or set `Sanitized`; `PrintSanitized` takes `a.Mux.RLock()` instead of `Lock()`.
   `StripDangerousHTML` stays exported (K1), documented as mutating and unused by the display path.
4. **Tests:** `TestAdfSanCacheBounded` (`NewSanitizedCache(100, time.Hour)`, 1000 distinct `SetArticle` → `len ≤ 100`,
   the most recently set ids present); `TestAdfSanCacheGetFieldRace` (`-race`: 8 goroutines × 1000 `SetField`/`GetField`
   on one id); `TestAdfSanitizeNoWriteBack` (an `Article` with From `Alice Example <alice@test.invalid>` and body
   `body <script>x</script> end`: after `PrintSanitized("fromheader")`, `PrintSanitized("bodytext")` and
   `BatchSanitizeArticles`, `a.FromHeader` and `a.BodyText` are byte-identical to the input; the returned HTML is
   still the filtered/escaped value). Call `Stop()` on caches you create.

Acceptance: the three tests pass under `-race`; the web article-page tests stay green.

#### Slice `adf-db-readers`: NULL-safe readers, repair migration, delete paths
Ids: A-db-1 (readers + repair), A-db-11, A-db-13, FU:E13, FU:E15, SEC-7 (residual log). Smoke port —.

Owned files:
- `internal/database/queries.go` (wave 1 functions of K6 only)
- `internal/database/hierarchy_cache.go` (only `buildHierarchyTreeBulk`)
- `internal/database/migrations/0029_main_repair_null_hierarchy.sql` (new)
- `internal/database/adf_readers_test.go` (new)

Changes:
1. **Readers (A-db-1).** In `query_MainDBGetAllNewsgroups`, `query_MainDBGetNewsgroup`, `query_MainDBGetNewsgroupByID`
   select `COALESCE(last_article, 0)`, `COALESCE(message_count, 0)`, `COALESCE(high_water, 0)`,
   `COALESCE(low_water, 1)`, `COALESCE(status, 'y')`, `COALESCE(hierarchy, '')` (same column order and scan
   targets). `MainDBGetAllNewsgroups` also returns `rows.Err()`.
2. **Repair (A-db-1).** `0029_main_repair_null_hierarchy.sql` (embedded automatically by `//go:embed
   migrations/*.sql`; the file name must not collide with a sibling's migration: check `ls internal/database/migrations`
   after the merges and take the next free main number if 0029 is taken):
   ```sql
   -- go-pugleaf: repair newsgroups rows created by the old batch upsert (A-db-1, AUDIT-2026-10)
   UPDATE newsgroups SET hierarchy = CASE WHEN INSTR(name, '.') > 0 THEN SUBSTR(name, 1, INSTR(name, '.') - 1) ELSE name END
    WHERE hierarchy IS NULL;
   UPDATE newsgroups SET last_article = 0 WHERE last_article IS NULL;
   UPDATE newsgroups SET message_count = 0 WHERE message_count IS NULL;
   ```
   (the same expression as `query_UpdateHierarchyCounts1`).
3. **A-db-13.** `GetActiveNewsgroups`, `GetActiveNewsgroupsWithMessages`, `GetNewsgroupsPaginated`,
   `GetNewsgroupsPaginatedAdmin`: `COALESCE` the nullable counters as in step 1; `db.mainDB.Query` →
   `RetryableQuery(db.mainDB, …)`, `QueryRow(...).Scan` → `RetryableQueryRowScan`; `defer rows.Close()`; return
   `rows.Err()`.
4. **A-db-11.** `buildHierarchyTreeBulk`, `GetNewsgroupsByHierarchy`, `getHierarchiesPaginatedDirect`: scan
   `hierarchy` (and other nullable text) via `COALESCE` in the SQL; on a scan error log the first one per call with
   the `[HIERARCHY]` prefix and continue; return `rows.Err()` (wrapped) at the end.
5. **FU:E13.** `BulkDeleteNewsgroups`: wrap the three (now four, step 6) statements in
   `RetryableTransactionExec(db.mainDB, func(tx *sql.Tx) error {…})`, keeping their order; the returned count comes
   from the `newsgroups` delete; reset the captured count at the start of the closure (it can be retried).
6. **FU:E15.** Add `query_BulkDeleteSpamRows = DELETE FROM spam WHERE newsgroup_id IN (SELECT id FROM newsgroups WHERE
   name IN (%s) AND active = 0)` before the newsgroups delete in `BulkDeleteNewsgroups`, and
   `query_DeleteSpamRowsByNewsgroup = DELETE FROM spam WHERE newsgroup_id IN (SELECT id FROM newsgroups WHERE name = ?
   AND active = 0)` before `query_DeleteNewsgroup` in `DeleteNewsgroup`. Change `query_GetSpamArticles1` to count
   through the same join as `query_GetSpamArticles2` (`SELECT COUNT(*) FROM spam s JOIN newsgroups n ON s.newsgroup_id = n.id`),
   so already orphaned rows stop inflating the count.
7. **SEC-7 residual.** Delete the per-call `log.Printf` at the start of `GetOverviews` (`:699`).
8. **Tests** `adf_readers_test.go`: insert a newsgroups row with raw SQL leaving `hierarchy`, `last_article`,
   `message_count`, `status` NULL → `MainDBGetNewsgroup`, `MainDBGetNewsgroupByID`, `MainDBGetAllNewsgroups`,
   `GetActiveNewsgroups` succeed (zeros, `Status == "y"`, `Hierarchy == ""`); run the migration SQL through
   `w0DB(t).GetMainDB()` (read the embedded file via the package's embed FS helper, or `os.ReadFile` of the
   migration path relative to the package dir) → `hierarchy` is the first label; `TestAdfReadersBulkDeleteSpam`
   (inactive group with 3 `spam` rows → after `BulkDeleteNewsgroups` no row left, `GetSpamArticles` count 0);
   same for `DeleteNewsgroup`; `TestAdfReadersBulkDeleteRetries` is not required (no busy injection exists) — assert
   the result count only.

Acceptance: E09 PASS; `TestFuQueries*`, `TestW2DBPerf*`, `TestLo2Pages*` unchanged and green.

#### Slice `adf-db-cache`: article-cache key, thread replies errors, thread-cache cron stop
Ids: A-db-2, FU:E16, OPS-1e.

Owned files:
- `internal/database/article_cache.go`
- `internal/database/thread_cache.go` (only `NewMemCachedThreads`, `CleanCron`, `GetCachedThreadReplies` and the
  comment above it)
- `internal/database/adf_cache_test.go` (new)

Changes:
1. **A-db-2.** `ArticleCacheEntry` gets `key string`, set in `Put`; `removeElement` deletes `ac.cache[entry.key]`;
   `Cleanup`/`ClearGroup`/`evictIfNeeded` use `entry.key`; the `ac.DB.Batch.GetNewsgroupPointer` call disappears.
   Fix `totalSize` bookkeeping to add and subtract the same quantity (`len(BodyText)+len(HeadersJSON)` stored in the
   entry as `size`).
2. **FU:E16.** In `GetCachedThreadReplies` (`:290-306`): on a scan error log once per call
   `[CACHE:THREADS] GetCachedThreadReplies(%s/%d): scan: %v` and continue; after the loop
   `if err := rows.Err(); err != nil { return nil, 0, fmt.Errorf(...) }`. Soften the comment as FU:E16 says
   (`child_articles` is the source of truth for the count, not a guarantee that every promised page renders rows).
3. **OPS-1e.** `NewMemCachedThreadsStop(stop <-chan struct{}) *MemCachedThreads` (K1: `NewMemCachedThreads()` stays and
   calls it with nil); `CleanCron` selects on `mem.stop` and returns when it is closed. Wiring into `db_init.go` is
   `adf-db-batch`'s (wave 2, K5).
4. **Tests:** `TestAdfCacheMapBounded` (`NewArticleCache(2, time.Hour, w0DB(t))`, 5 `Put`s whose `ArticleNums` use a
   pointer that is **not** the current TasksMap pointer → `len(ac.cache) <= 2`, `Get` of an evicted key misses);
   `TestAdfCacheThreadRepliesRowsErr` (corrupt one reply row's `date_sent` type so the scan fails, as
   `fuBatchFaultsUnreadableRow` does → no panic, the other replies returned, the count unchanged);
   `TestAdfCacheCleanCronStops` (`NewMemCachedThreadsStop(ch)`, close `ch`; `runtime.NumGoroutine()` returns to the
   value before the constructor within 1 s, polled every 10 ms; no package-level test hook, K3).

Acceptance: the three tests pass under `-race`; `TestFuThreadcount*`, `TestFuHygiene*` unchanged and green.

#### Slice `adf-db-lifecycle`: fatal sites, INIT, background loops, migration cache
Ids: OPS-1a, A-db-6, A-db-12.

Owned files:
- `internal/database/db_init.go` (everything except the `NewMemCachedThreads` line, which stays as is in wave 1)
- `internal/database/database.go` (only the `Database` struct fields and `Shutdown`)
- `internal/database/db_migrate.go`
- `internal/database/groups_hashmap.go`
- `internal/database/sections_cache.go`
- `internal/database/adf_lifecycle_test.go` (new)
- `internal/database/adfreopen/reopen_test.go` (new package `adfreopen`)

Changes:
1. **Fatal → error (D5).** `OpenDatabase`: `checkRootUser()` error → `return nil, fmt.Errorf("%w: %v", ErrRunAsRoot, err)`
   (new exported `var ErrRunAsRoot = errors.New("refusing to run as root")`); the four `log.Fatalf` at `:206,212,224,229`
   (UserSalt/AbuseMail config) → return wrapped errors. Every error return after `initMainDB` closes `db.mainDB`
   (helper `closeOnErr`), and nothing global is left half-set.
2. **INIT.** Move `INIT = true` after the last fallible step; `Shutdown()` (database.go) sets `INIT = false` under
   `GlobalDBMutex` as its last step (after the main DB is closed).
3. **Background stop.** `Database` fields `bgStop chan struct{}`, `bgStopOnce sync.Once`; `OpenDatabase` makes
   `bgStop`; `(*Database).stopBackground()` closes it once and is a no-op when `bgStop` is nil (seven test files build
   a bare `&Database{}`; `Shutdown` must keep working on those); `(*Database).backgroundStop() <-chan struct{}` returns it;
   `Shutdown()` calls `stopBackground()` first. `NewGHmapStop(stop)` (old `NewGHmap()` → `NewGHmapStop(nil)`):
   `GHmapCron` becomes a loop with `select { case <-stop: return; case <-time.After(DefaultCronGroupHash): … }`
   (no self re-spawn). `newGroupSectionDBCacheStop(stop)` likewise for `CronClean` (old constructor kept).
   `OpenDatabase` uses the `…Stop(db.bgStop)` variants for both.
4. **A-db-6 / migratedDBsCache (OPS-1a).** Remove `migratedDBsCache`, `migratedDBsMux`, `initMigratedDBsCache` and the
   cache branch in `migrateGroupDB` (`:376-389`, `:421-425`): every group open reads `schema_migrations` (one indexed
   query, AUDIT A-db-6 fix "or stop caching"). Keep the `useCache` parameter (callers in `db_groupdbs.go`) but ignore
   it, with a comment; grep for other users first.
5. **A-db-12.** `GetGroupFromHash`: `if !exists || entry == nil { h.Gneg(); return "", false }`.
6. **Tests:**
   - `adf_lifecycle_test.go`: `TestAdfLifeGroupDBRecreated` (open a group, close it with `ForceCloseGroupDB`, delete its
     file, `GetGroupDB` again → `SELECT count(*) FROM articles` works: the schema was re-applied);
     `TestAdfLifeGetGroupFromHashUnknown` (no panic, `ok == false`).
   - `adfreopen/reopen_test.go` (own `TestMain`, K3): `TestAdfReopenAfterFailureAndShutdown`:
     (a) `OpenDatabase` with `DataDir` pointing at a regular file → error, no panic, no exit;
     (b) `OpenDatabase` on a temp dir → ok; record `runtime.NumGoroutine()` before (a);
     (c) `close(db.StopChan)`, `db.WG.Wait()` (≤ 30 s), `db.Shutdown()`;
     (d) `OpenDatabase` again on a second temp dir → ok (INIT was reset), then the same teardown;
     (e) after teardown, compare goroutine stacks rather than a bare count: poll for up to 12 s (`CronDB` only exits
     on its next 10 s tick) and then require that no goroutine stack contains `GHmapCron`, `CronClean` or `CronDB`;
     log `runtime.Stack(all)` on failure. Known leftovers in wave 1 (fixed in wave 2 by `adf-db-batch`): one
     `BatchDivider`, one `ExpireCache`, one `CleanCron` per open (two opens → up to 6), plus
     `NNTPAuthCache.cleanupExpired` (hardening-owned, no stop path: one per open → 2), about 7–8 in total. Assert
     those as a named allow-list with a `// tightened by adf-db-batch` comment; `adf-db-batch` removes its three from
     the allow-list, leaving only `cleanupExpired` (≤ 2 after two cycles).

Acceptance: both test files pass under `-race`; every existing `internal/database` test unchanged and green.

#### Slice `adf-proc-fatal`: processor constructor and analyze export return errors
Ids: OPS-1b.

Owned files:
- `internal/processor/processor.go` (only `NewProcessor` and the new `NewProcessorE`)
- `internal/processor/analyze.go` (only `printAnalysisResults` and its two callers `:835`, `:874`)
- `internal/processor/adf_proc_test.go` (new)

Changes:
1. `func NewProcessorE(db *database.Database, nntpPool *nntp.Pool, useShortHashLen int) (*Processor, error)` with the
   body of `NewProcessor`, returning wrapped errors at `:63`, `:69`, `:88` (`hostname: %w`, `hostname validation:
   %w`, `history init: %w`). `NewProcessor` becomes:
   ```go
   // Deprecated: use NewProcessorE. Kept for callers that still exit on error.
   func NewProcessor(db *database.Database, nntpPool *nntp.Pool, useShortHashLen int) *Processor {
   	proc, err := NewProcessorE(db, nntpPool, useShortHashLen)
   	if err != nil {
   		log.Fatalf("FATAL: %v", err)
   	}
   	return proc
   }
   ```
2. `printAnalysisResults(analysis, exportFormat) error`: every `os.Exit(1)` → `return fmt.Errorf(...)`; its callers
   return/propagate that error through their existing error results (check their signatures; if a caller has no error
   result, log with the analyze prefix and continue). No other analyze change (optional slice).
3. **Tests:** `TestAdfProcNewProcessorEInvalidHost` (`LocalNNTPHostname = "localhost"`, restored in `t.Cleanup`;
   `NewProcessorE(nil, nil, 7)`: with a non-empty hostname the `SetHostname(…, db)` branch is skipped and
   `validateHostname` rejects `localhost` before `db` is used — re-check this order after editing) → error mentioning
   validation, the process keeps running; `TestAdfProcPrintAnalysisResultsError` (export format `json` with the cwd
   changed to a temp dir that contains a **file** named `out` → error returned, no exit; restore the cwd).

Acceptance: both tests pass; `go build ./...` (all `NewProcessor` callers compile unchanged).

#### Slice `adf-tools-fetcher`: list update honours `-data`, proxy fields, SIGTERM
Ids: A-tools-2, A-tools-3 (fetcher half), A-ops-3b, A-tools-5a.

Owned files:
- `cmd/nntp-fetcher/main.go`
- `cmd/nntp-fetcher/backend.go` (new)
- `cmd/nntp-fetcher/adf_fetcher_test.go` (new)

Changes:
1. `backend.go`: `func fetcherBackendConfig(p *models.Provider, maxConns int) *nntp.BackendConfig` building exactly the
   `config.Provider` + `BackendConfig` pair of `main.go:226-266` (proxy fields, `Provider` set). The fetch path uses it
   (`maxConns = p.MaxConns`), `UpdateNewsgroupList` uses it with `maxConns = 1` (A-tools-3).
   `func openFetcherDB(dataDir string) (*database.Database, func(), error)`: `cfg := database.DefaultDBConfig();
   cfg.DataDir = dataDir; database.OpenDatabase(cfg)`; the returned cleanup runs `close(db.StopChan)`, `db.WG.Wait()`
   (≤ 60 s, then log), `db.Shutdown()`.
2. `UpdateNewsgroupList(dataDir string, updateList, excludePrefix *string, force bool) error`: open with
   `openFetcherDB(dataDir)` and `defer cleanup()`. `main` passes `*dataDir` (A-tools-2); the branch at `:91-96`
   returns the error code without `log.Fatalf` after the DB is closed (`os.Exit(1)` after the deferred cleanup ran:
   restructure as `if err := run(); err != nil {…}` within the branch).
3. `signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)` (`:147`, D6).
4. **A-tools-5a.** Parse and validate `-download-start-date` right after `flag.Parse()` (before `OpenDatabase`); the
   worker then uses the parsed `time.Time`; delete the `log.Fatalf` at `:660-663`.
5. **Tests** (no `TestMain` that opens a DB: the single `OpenDatabase` happens inside
   `TestAdfFetchOpenFetcherDBHonoursData`, the only DB test in this package; euid check as in K3):
   `TestAdfFetchBackendConfigCopiesProxy` (a provider with SOCKS5 proxy fields → every
   proxy field and `Provider` set, `MaxConns` as passed); `TestAdfFetchOpenFetcherDBHonoursData` (record whether
   `./data` exists in the package dir before; `openFetcherDB(<tmp>)` → `<tmp>/cfg/pugleaf.sq3` exists; `./data`
   existence unchanged; cleanup). The test never calls `UpdateNewsgroupList` (no network, ever).

Acceptance: E13 PASS; both tests pass.

#### Slice `adf-ops-recover`: recover-db directories, safe date rewrite, teardown
Ids: A-ops-1, A-ops-5, OPS-1f.

Owned files:
- `cmd/recover-db/main.go`
- `cmd/recover-db/adf_recover_test.go` (new)

Changes (D13):
1. `resolveRecoverDataDir(dbFlag, dataFlag string, dbSet, dataSet bool) (string, error)` (sets from `flag.Visit`):
   only `-db` set → `dbFlag`; both set and `filepath.Clean` differs → error ("-db is a deprecated alias of -data; they
   disagree"); else `dataFlag`. Usage text and example mention only `-data`; `-db` help says "deprecated alias of
   -data". Delete the `os.Stat(*dbPath)` block; print `📂 Data Path: <resolved>`.
2. `recoverWriteModes(...)` returns the list of write modes requested; when non-empty and (`-data` not explicitly set
   or `-force` false) print the modes and the directory and exit 2 **before** `OpenDatabase`. Add `-force` (bool).
3. **A-ops-5.** `const query_rewriteDateSent = "UPDATE articles SET date_sent = ? WHERE article_num = ?"` replaces the
   statement at `:877`. In `checkGroupDates`, use `processor.ParseNNTPDateStrict` (intake K1/K2); when `!ok` record
   the `ParseFailed` problem and `continue` (restore the commented-out `continue`), so no rewrite happens for it.
4. **OPS-1f.** A `recoverExit(db *database.Database, code int)` helper (close `StopChan` once, `WG.Wait` ≤ 60 s,
   `Shutdown`, `os.Exit(code)`) replaces every `os.Exit` after `OpenDatabase` (`:169,186,197,208,671,674`; the
   "DO NOT CHANGE" exit at `:305` keeps exiting with 1 but through the helper — keep its comment). Drop the
   `defer db.Shutdown()`.
5. **Tests:** `TestAdfRecoverResolveDataDir` (table: only db, only data, both equal, both different → error, neither →
   default); `TestAdfRecoverRewriteQueryKeepsFlags` (the constant contains neither `hide` nor `spam`); a table test of
   the skip decision if you extract it (`shouldRewrite(ok bool, reparsed, stored time.Time) bool`).

Acceptance: E14, E15 PASS; tests pass; `./build_recover-db.sh` builds.

#### Slice `adf-ops-scripts`: scripts, build scripts, release workflow
Ids: OPS-4d, OPS-4e, OPS-4f, A-ops-2, A-ops-15, OPS-4g (optional step, user decision Q2).

Owned files:
- `build_fix-references.sh`, `build_fix-thread-activity.sh`, `build_history-rebuild.sh`, `build_import_flat-files.sh`,
  `build_merge-active.sh`, `build_merge-descriptions.sh`, `build_nntp-transfer.sh`, `build_nntpmgr.sh`,
  `build_post-queue.sh`, `build_tcp2tor.sh`, `build_webserver.sh`
- `run_web.sh`, `run_web_ssl.sh`, `scripts.sh`, `go_run_web.sh`, `run_test.sh`, `build_TestMsgIdItemCache.sh`
- `manual_sqlite_migration_0002.sh`, `manual_sqlite_migration_0008.sh` (edit only; never run them, CLAUDE.md)
- `.github/workflows/release.yml`
- `.github/workflows/tests.yml` (new; only if the user said yes to Q2 at the wave-1 boundary)
- `.claude/CLAUDE.md` (only the build-script sentence, D11)

Changes:
1. **OPS-4e (D11, only if the user said yes to Q9 at the wave-1 boundary).** In each of the 11 scripts replace `-race` in the `go build` line with `${RACE:+-race}`; nothing
   else changes (keep `GOEXPERIMENT`, ldflags, output paths).
2. **OPS-4d.**
   - `run_web.sh:4` → `./webserver` (drop `-useshorthashlen 7`, a no-op after OPS-4b, and the undefined
     `-update-descr …`). `run_web_ssl.sh:3` → `./webserver -webssl -websslcert fullchain.pem -websslkey privkey.pem -webport 11981`.
   - `scripts.sh` is a notes file: insert as line 1 `echo "scripts.sh is a notes file, not a script; copy the commands you need" >&2; exit 1`
     and a `#!/bin/sh` shebang above it, fix `-update-newsgroup-activity` → `-update-newsgroups-activity 1` on
     lines 6-7 and 9, and `-rebuild-threads` → `-rebuild-threads -dry-run=false` on line 14 (A-ops-7 comment: the
     tool cannot start before the optional A-ops-7 fix; add a `#` note). Do not run it.
   - `go_run_web.sh` → `go run ${RACE:+-race} ./cmd/web -webport 11980 -data ./data-test-go-run-web -nntphostname "${NNTPHOST:?set NNTPHOST to a resolvable FQDN}" "$@"`
     (whole package, real flag names, scratch data by default; cmd/web validates the hostname by DNS). If Q9 was
     answered no, keep `-race` there.
   - `manual_sqlite_migration_0002.sh:6-7` and `manual_sqlite_migration_0008.sh:5`: point at the real files under
     `internal/database/migrations/` (read both scripts; use the file that matches each script's own skip check, e.g.
     the 0002 script checks for the `spam` column). `bash -n` only.
   - `run_test.sh` (references a missing `examples/` file) → delete it (`git rm`); `build_TestMsgIdItemCache.sh`
     (the package has only `main.go.off`) → delete it.
3. **A-ops-2 / OPS-4f / A-ops-15 (D12)** in `release.yml`:
   - new job `test` on `ubuntu-24.04`: checkout, `actions/setup-go` with `go-version-file: go.mod`, `go vet ./...`,
     `go test -race -count=1 -timeout 900s` over the CLAUDE.md package list;
   - `build`: `needs: test`, `strategy: { fail-fast: false, matrix: { include: [...] } }` with the five
     `{os, arch, runner}` entries of D12, `runs-on: ${{ matrix.runner }}`, `defaults: run: shell: bash`,
     `env: CGO_ENABLED: "1"`, `go-version-file: go.mod`; remove the "removing -race for cross-compilation" comment;
     check the runner-images list for the current macOS labels before writing them (`macos-13` may be retired: use the
     current Intel label or drop darwin/amd64 and say so); replace the `zip`/`sha256sum` calls, which are not on every
     runner, with portable steps (archive and checksum with a small `go run` helper kept under `.github/scripts/`, or
     select `shasum -a 256` / `sha256sum` by `runner.os`);
   - version step: `env: INPUT_VERSION: ${{ github.event.inputs.version }}` and in the script
     `VERSION="$INPUT_VERSION"`, then `[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "invalid version"; exit 1; }`;
     the same pattern for the release job (`:176`).
   - Optional step: pin `actions/checkout`, `actions/setup-go`, `actions/upload-artifact`, `actions/download-artifact`,
     `softprops/action-gh-release` by full commit SHA with the tag as a trailing comment (needs network lookups;
     skip and list as leftover if unavailable).
4. **OPS-4g (optional, Q2).** `tests.yml`: on `push` and `pull_request`, the same `test` job as above plus a
   `known-bugs` job with `continue-on-error: true` running `PUGLEAF_KNOWN_BUGS=1 go test -count=1 ./internal/nntp/... ./cmd/nntp-server/...`.
5. `.claude/CLAUDE.md` (only on a yes to Q9): replace "(output: `build/`, built with `-race`)" with "(output: `build/`; `RACE=1` for a
   `-race` build)".
6. **Checks (shell):** `bash -n` on every touched `.sh`; `grep -nE 'go build[^$]*-race' build_*.sh` prints nothing;
   `grep -c 'RACE:+-race' build_*.sh` shows 11 files with 1 (both only after a yes to Q9); `python3 -c 'import sys,yaml; yaml.safe_load(open(sys.argv[1]))' .github/workflows/release.yml`
   (skip with a note when PyYAML is missing; `actionlint` if installed). Never run `build_ALL.sh`/`Build_DEV.sh`.

Acceptance: E16 PASS.

#### Slice `adf-ops-tcp2tor`: `-timeout` effective
Ids: OPS-4c.

Owned files:
- `cmd/tcp2tor/main.go`
- `cmd/tcp2tor/adf_tcp2tor_test.go` (new)

Changes:
1. `socksForwardDialer(timeout time.Duration) proxy.Dialer` = `&net.Dialer{Timeout: timeout}`; both `proxy.SOCKS5(...,
   proxy.Direct)` pairs (`:514,519`, `:626,628`) pass it instead.
2. `dialThroughSOCKS(d proxy.Dialer, addr string, timeout time.Duration) (net.Conn, error)`: when `d` implements
   `proxy.ContextDialer`, `DialContext` with `context.WithTimeout(timeout)`; else run `Dial` in a goroutine and return
   `context.DeadlineExceeded` after `timeout` (closing a late conn). `testSOCKS5Connection` uses `timeoutSec`;
   `handleConnection` uses `s.Timeout` (`:551` is then read). A zero timeout means 30 s (the flag default).
3. **Tests:** a silent listener on `127.0.0.1:0` (accepts, never writes) as the SOCKS proxy:
   `TestAdfTorSelfTestTimeout` (`testSOCKS5Connection` with `timeoutSec=1` returns an error within 3 s);
   `TestAdfTorDialTimeout` (`dialThroughSOCKS` with 500 ms returns within 2 s). Close everything in cleanup.

Acceptance: tests pass under `-race` (`go test -race ./cmd/tcp2tor/...`).

#### Slice `adf-ops-misc`: peering test hygiene, history-rebuild flag, SIGTERM in two tools
Ids: OPS-2b, OPS-4a, A-ops-3c, A-ops-3e.

Owned files:
- `internal/nntp/nntp-peering-pattern_test.go`
- `cmd/history-rebuild/main.go` (only `:74` and `:79`)
- `cmd/expire-news/main.go` (only the `signal.Notify` line `:146` and its import block)
- `cmd/post-queue/main.go` (only the `signal.Notify` line `:74` and its import block)

Changes:
1. **OPS-2b.** `parseActiveFile` reads `filepath.Join("..", "..", "active_files", "ninja-nl.active")` (tracked, 3.4 MB);
   read at most the first 20 000 lines unless `PUGLEAF_PEERING_REALDATA=1`. Replace the `active.out` hand-over
   between tests by a helper `peeringUnmatched(t)` that computes the unmatched list in-process (shared by the three
   tests); every output file goes to `t.TempDir()`. Keep the existing test and helper names (unprefixed, K2 of the
   audit plan). Check: `go test -race -count=1 -run 'Pattern|Peering|Binary|Unmatched' ./internal/nntp/` < 60 s and
   no file appears in `internal/nntp/` (`git status --short`).
2. **OPS-4a.** `:74` help text stays honest; replace `_ = useShortHashLen` (`:79`) with a `flag.Visit` check that
   logs `[HISTORY-REBUILD] -useshorthashlen is ignored (history stores full message-ids)` when the flag was given.
   Touch no other line (client plan owns `:12,:75,:97-104`); merges after it.
3. **A-ops-3c/e (D6).** `signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)` in both tools.

Acceptance: the peering tests run (no SKIP for a missing file) and leave no files; E01-style SIGTERM check for
`post-queue` is INFO (no scratch server needed).

### Wave 2 (3 parallel slices, from the merged wave 1)

#### Slice `adf-web-hide`: hidden articles, tree API, counters
Ids: A-web-1, A-web-11, A-web-15. Smoke port 18996.

Owned files:
- `internal/web/web_articlePage.go`
- `internal/web/web_sectionsPage.go` (only `sectionArticlePage`, `sectionArticleByMessageIdPage`)
- `internal/web/web_threadTreePage.go`
- `internal/web/web_apiHandlers.go` (only `getArticlePreview`, `getArticle` `:237`, `getArticleByMessageId` `:267`)
- `internal/web/web_sitePostPage.go` (only the reply prefill block of `sitePostPage`, `:86-110`)
- `internal/web/webserver_core_routes.go` (only the `ArticlePageData` struct `:86` and the route line `:526`)
- `web/templates/article.html` (only the counter block `:84-100`)
- `internal/database/web_article_flags.go` (new)
- `internal/database/tree_view_api.go` (new methods only, plus `getNodeHTML`'s hidden branch)
- `internal/web/adf_hide_test.go`, `internal/database/adf_flags_test.go` (new)

Changes (D2, D3):
1. `web_article_flags.go`: `func (db *Database) GetArticleFlags(groupDB *GroupDB, articleNum int64) (spam, hide int, err error)`
   and `func (db *Database) GetArticleFlagsByMessageID(groupDB *GroupDB, messageID string) (articleNum int64, spam, hide int, err error)`
   via `RetryableQueryRowScan` (`SELECT spam, hide FROM articles WHERE article_num = ?` / `… WHERE message_id = ?`);
   `sql.ErrNoRows` unwrapped.
2. `articlePage`, `articleByMessageIdPage`, `sectionArticlePage`, `sectionArticleByMessageIdPage`: after the article is
   found, read the flags; when `articleHiddenFor(c, hide)` render the existing 404 "Article not found" page; else set
   `ArticleSpam`/`ArticleHide` in `ArticlePageData` (new fields) in `articlePage`/`articleByMessageIdPage`. The
   section handlers use `SectionArticlePageData` (`webserver_core_routes.go:201`): there only the 404 gate is added,
   no counter fields. Never write into the cached `*Article`.
3. `article.html`: `{{.Article.Spam}}` → `{{.ArticleSpam}}`, `{{.Article.Hide}}` → `{{.ArticleHide}}`,
   `{{if gt .Article.Spam 0}}` → `{{if gt .ArticleSpam 0}}` (A-web-15). Check `section-article.html` for the same
   fields; if it uses them, add it to your report (it is not owned here; the section handlers pass the same data
   type, so it needs no edit if it doesn't reference them).
4. `getArticlePreview`, `getArticle`, `getArticleByMessageId` (token API): before building the response, flags →
   hidden for non-admin (a token request is never admin) → `404 {"error": "Article not found"}`. Site-post reply
   prefill: when the replied-to article is hidden for this user, skip the quote and set the form error
   "Article not found".
5. `tree_view_api.go`: `func (tree *ThreadTree) RedactedCopy() *ThreadTree` (deep copy of nodes and `NodeMap`;
   `Overview = nil` where `Overview != nil && Overview.Hide > 0`); `func (tree *ThreadTree) GetThreadTreeHTMLFor(groupName
   string, showHidden bool) template.HTML` (`GetThreadTreeHTML` = `GetThreadTreeHTMLFor(groupName, true)`, K1);
   `getNodeHTML` gains the flag and renders a hidden node as `[hidden article]` (escaped literal, no link, no
   subject/author) when `!showHidden`. Do not mutate cached trees.
6. `web_threadTreePage.go`: `threadTreePage`/`sectionThreadTreePage` call `GetThreadTreeHTMLFor(group, s.isAdminRequest(c))`;
   `handleThreadTreeAPI` returns `response` with `Tree = Tree.RedactedCopy()` for non-admins and sets
   `Cache-Control: private, max-age=60` (`:95`).
7. Route `:526`: `s.Router.GET("/api/thread-tree", s.requireAPIEnabled(), s.handleThreadTreeAPI)` (D3).
8. **Tests:** `adf_flags_test.go` (flags for a seeded row, `ErrNoRows` for a missing number/id). `adf_hide_test.go`
   (article seeded and `UPDATE articles SET hide = 1`): anonymous GET by number, by message-id, section variants (seed a
   section with the group), preview → 404; admin → 200 and the page shows `👁️ 1`; tree page anonymous → contains
   `[hidden article]`, not the subject; `/api/thread-tree` with the API disabled → not 200, with the API enabled
   (`lo1CoreSetAPIEnabled(t, "true")`) anonymous → 200, JSON has no `overview` for the hidden node; token API
   (`/api/v1/groups/<g>/articles/1` and `/message/<id>` with a seeded token as `TestW1API*` does) → 404; GET
   `/SitePost?…reply_to=1` (user cookie) → no quoted body;
   `Cache-Control` starts with `private`; a visible article page shows the real spam counter after
   `IncrementArticleSpam`.

Acceptance: E06, E07 PASS; `TestW1APIThreadTreeHTMLEscapesGroup` and the other tree tests stay green.

#### Slice `adf-db-batch`: monotonic article numbers, batch counters, batch globals
Ids: INT-5 (number reuse), A-db-9, A-db-5, OPS-1d.

Owned files:
- `internal/database/db_batch.go` (K6 functions only; **not** `query_updateNewsgroupsStats`, the PHASE 1 number loop,
  `batchInsertOverviews`, `processOverviewBatch`, PHASE 3, the stats block — intake S2 owns those; only the
  `query_processOverviewBatch` constant is edited, on top of intake's version)
- `internal/database/db_init.go` (only the `NewMemCachedThreads()` call → `NewMemCachedThreadsStop(db.bgStop)`)
- `internal/database/queries.go` (only `ResetNewsgroupData`, `InsertOverview` and `query_InsertOverview` `:668-690`)
- `internal/database/migrations/0010_single_article_num_seq.sql` (new; take the next free `single` number if taken)
- `internal/database/article_num_cleanup.go` (new)
- `cmd/expire-news/main.go` (only `deleteArticles` and its two call sites `:466`, `:748`)
- `internal/database/adf_batch_test.go`, `cmd/expire-news/adf_expire_test.go` (new)
- `internal/database/adfreopen/reopen_test.go` (only the goroutine allow-list, see `adf-db-lifecycle`)

Changes:
1. **Migration (D4):**
   ```sql
   -- go-pugleaf: monotonic article numbers per group (INT-5, AUDIT-2026-10)
   -- Runs lazily on the first open of every group database: one tiny table, one trigger, no table rebuild.
   CREATE TABLE IF NOT EXISTS article_num_seq (id INTEGER PRIMARY KEY CHECK (id = 1), last_num INTEGER NOT NULL);
   INSERT OR IGNORE INTO article_num_seq (id, last_num) SELECT 1, COALESCE(MAX(article_num), 0) FROM articles;
   CREATE TRIGGER IF NOT EXISTS articles_num_seq_ai AFTER INSERT ON articles
   BEGIN
       UPDATE article_num_seq SET last_num = NEW.article_num WHERE id = 1 AND NEW.article_num > last_num;
   END;
   ```
   Check `stripMigrationPragmas` and the multi-statement execution handle a trigger body (`BEGIN … END;`); if the
   migration runner splits statements, adapt the file (one statement per line is not required by
   `tx.ExecContext`, which runs the whole script — verify with the test).
2. **Insert (INT-5).** `query_processOverviewBatch` (constant only; its callers are intake's) gets `article_num` as the
   first column with value `(SELECT last_num + 1 FROM article_num_seq WHERE id = 1)`; the `stmt.Exec` argument list
   is unchanged (the value is a subquery). If intake's version changed the column list, add the column to that
   version. The other number-less insert, `query_InsertOverview` (`queries.go:668`, used by `InsertOverview` `:680`
   and the dead `proc_ImportOV.go:85`), gets the same subquery; `query_ImportOverview` passes an explicit number and
   stays. Within the batch transaction each insert sees the trigger's update, so numbers are consecutive;
   `INSERT OR IGNORE` of a duplicate fires no trigger and consumes no number.
3. **Reset.** `ResetNewsgroupData`: inside its transaction after `DELETE FROM articles`, `UPDATE article_num_seq SET
   last_num = 0 WHERE id = 1` (the group was opened through `GetGroupDB`, so the migration ran). After commit: delete
   the group's main-DB `spam` and `user_spam_flags` rows (all article numbers of that newsgroup id, via
   `RetryableTransactionExec`; numbers restart, so the old rows would be inherited) and
   `db.ArticleCache.ClearGroup(newsgroupName)` when the cache is enabled (nil check).
4. **Derived rows.** (`deleteDerivedThreadRows` is a function in `cmd/expire-news/main.go` itself, not a new file:
   keep the tool a one-file package.) `article_num_cleanup.go`: `func (db *Database) DeleteSpamRowsForArticles(newsgroup string, nums []int64) error`
   deleting `spam` and `user_spam_flags` rows `WHERE newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?) AND
   article_num IN (…)` in chunks of 500 via `RetryableTransactionExec`. Expire-news: `deleteArticles` also runs
   `DELETE FROM cached_trees WHERE thread_root IN (…)`, `DELETE FROM tree_stats WHERE thread_root IN (…)`,
   `DELETE FROM thread_cache WHERE thread_root IN (…)` in the same transaction, ignoring a "no such table" error like
   the existing `overview` delete; both call sites then call `db.DeleteSpamRowsForArticles(groupName, batch)` (log
   errors with the tool's prefix, don't abort) and clear the article cache of the group if the tool enabled it.
5. **A-db-9.** Count per article: in `BatchDivider` increment `sq.queued` by 1 under `sq.GMux.Lock()` **before**
   `tasks.BATCHchan <- article` (so a fast batch can't subtract it first; drop `tmpQueued` stepping); the brake compares the live value (`realQueue = sq.queued`
   read under `RLock`). `processNewsgroupBatch`'s subtraction (`:764-768`) stays. Add a log-once warning if `queued`
   ever goes negative.
6. **A-db-5.** `ExpireCache`: skip tasks with `BATCHprocessing == true` or `len(BATCHchan) > 0`;
   `GetNewsgroupPointer` renews `Expires` on a hit (`task.Mux.Lock()`). Convert `ExpireCache` to a loop
   (`select { case <-sq.db.bgStop: return; case <-time.After(15 * time.Second): … }`, no self re-spawn).
7. **OPS-1d.** `BatchDividerChan` becomes the field `sq.dividerChan` (made in `NewSQ3batch`, cap 128); keep a
   package-level `var BatchDividerChan` only if something outside `db_batch.go` uses it (grep; tests included) —
   otherwise delete it. `CheckNoMoreWorkInMaps` (`:273`) reads `len(sq.dividerChan)`. `BatchCaptureOverviewForLater` sends with
   `select { case sq.dividerChan <- article: case <-sq.db.bgStop: log once "[BATCH] capture after Shutdown dropped" }`.
   `BatchDivider` selects on `sq.db.bgStop` and returns. `db_init.go`: `NewMemCachedThreadsStop(db.bgStop)`.
   `reopen_test.go`: remove `BatchDivider`, `ExpireCache`, `CleanCron` from the allow-list (only
   `NNTPAuthCache.cleanupExpired`, hardening-owned, remains).
8. **Tests:** `adf_batch_test.go` (use `fuBatchIsolatedDB`/`fuBatchTask`/`fuBatchRun`):
   `TestAdfBatchNumbersNotReused` (batch of 2 → numbers 1, 2; `DELETE FROM articles`; batch of 1 → number 3;
   `article_num_seq.last_num == 3`); `TestAdfBatchSeqSeededFromExisting` (group with rows 1..5 inserted raw, drop the
   table and the `schema_migrations` row of the new migration, run `migrateGroupDB(groupDB, false)` → `last_num == 5`);
   `TestAdfBatchDuplicateConsumesNoNumber` (a batch with a message-id already stored → no gap);
   `TestAdfBatchQueuedNeverNegative` (5 articles through the capture path of an isolated batch → `queued` returns to 0,
   never < 0); `TestAdfBatchExpireSkipsBusyTask`. `adf_expire_test.go` (reuse `toolsTestGroupDB` style, create
   `thread_cache`/`cached_trees`/`tree_stats` tables): after `deleteArticles` of a root, no derived row is left.

Acceptance: tests pass; `TestFuBatch*`, `TestLo2Writer*`, intake's `TestIntakeDB*` unchanged and green.

#### Slice `adf-web-cmd`: web binary teardown, SIGTERM, dead flags, `-disable-active`
Ids: OPS-1c, OPS-2a, OPS-4b, A-ops-3a, A-ops-4. Smoke port 18997.

Owned files:
- `cmd/web/main.go`
- `cmd/web/main_functions.go`
- `cmd/web/adf_web_test.go` (new, own `TestMain`)

Changes:
1. **OPS-1c (D5).** `exitAfterDB(db *database.Database, code int)` and `fatalAfterDB(db, format string, args ...any)`
   (close `StopChan` once, `WG.Wait` ≤ 60 s, `Shutdown`, then `os.Exit`). Replace every `os.Exit`/`log.Fatalf` after
   `OpenDatabase` (`:228-509`, `ConnectPools` `main_functions.go:583`) with them. `NewFetchProcessor` uses
   `processor.NewProcessorE` and returns `(*processor.Processor, error)`; the caller at `:423-425` uses
   `fatalAfterDB`. A `webStop` channel closed at the start of the shutdown sequence stops `startHierarchyUpdater`
   (loop with `select`, log text fixed to the real 10-minute period) and `monitorUpdateFile`.
2. **A-ops-3a (D6).** `signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)` (`:487`).
3. **OPS-2a (D10).** Remove the embedded NNTP server: delete `withnntp`, `withfetch`, `isleep`, the commented-out flag
   lines `:105-107`, the dead NNTP block `:428-473` (incl. `:448`; no other plan edits it) and every import only it used; the startup line `:150` drops `NNTP=`/`Fetch=`; when any of the
   four NNTP flags is set, log the D10 line once.
4. **OPS-4b.** `-useshorthashlen`: no validation, no fatal: if the stored value is locked, use it; if not locked and
   the CLI value is outside 2..7, log a warning and store/use 7; `NewFetchProcessor` receives `finalUseShortHashLen`,
   never the raw CLI value (`main_functions.go:656-657`). The help text says "legacy, no effect on the message-id
   history schema".
5. **A-ops-4 (D14).** `disableNewsgroupsNotInActiveFile(db, path string, opts disableActiveOpts)` with
   `opts.DryRun` from the new flag; accept 1-field lines; refuse an empty keep set (error, nothing changed);
   `DryRun` prints and changes nothing. No percentage threshold, no force flag (Q10 answered).
6. **Tests** `adf_web_test.go` (`TestMain` per K3; seed 4 active groups with `InsertNewsgroup`):
   `TestAdfWebCmdDisableActiveEmptyFile` (a file with only comments/blank lines → error, all 4 still active);
   `TestAdfWebCmdDisableActiveOneOfFour` (file with 1 of 4 → applied: 1 active, 3 disabled);
   `TestAdfWebCmdDisableActiveDryRun` (same file, DryRun → all 4 still active);
   `TestAdfWebCmdPlainList` (1-field lines accepted).

Acceptance: E01, E10, E11, E12 PASS; tests pass; `./build_webserver.sh` builds.

### Wave 3 (inline, orchestrator)
1. Re-run the hand-off greps of wave 0 step 2 on the merged branch; record A-tools-1, A-tools-4 and A-ops-3d/f as closed
   (with the sibling commit) or open.
2. Run `## Checks` and `## End-to-end`; write the Outcome: ids fixed per slice, the hand-offs and their state, the
   deferred list, the decisions D1–D16 as behaviour changes, open questions answered or not.
3. Ask the user whether to run wave O1/O2 (optional findings).

### Wave O1 (optional; 10 parallel slices after wave 3, only when the user says so)
Common: same rules and checks; each slice reads its audit blocks (`docs/audit/audit-*.md`) for the exact fix
("Fix:" lines) and implements that fix plus one test per id where a test is practical; shell/doc-only items get a
`bash -n` or grep check. Owned files are disjoint within the wave.

- **`adf-opt-sanitize`** (A-web-6, A-web-7, A-web-8, A-web-9, A-web-10): `internal/models/sanitizing.go`,
  `internal/models/cache.go`, `internal/cache/sanitized_cache.go`, `internal/models/adf_opt_sanitize_test.go`.
  Decode before truncating (`truncateRunes`), From via `net/mail.ParseAddress` after RFC 2047 decoding, body decoding
  by stored CTE only, drop `sanitizeText` from the display path, cache key group+message-id with one field-name map.
- **`adf-opt-web-admin`** (A-web-12, A-web-14, A-web-16, A-web-18, A-web-19): `web/templates/aichat.html`,
  `internal/web/web_admin_provider.go` (baseline gofmt file, K4), `internal/web/web_admin_newsgroups.go`,
  `internal/models/models.go` (only `(*Article).GetData`), `internal/web/webserver.go` (only `Shutdown`),
  `internal/web/adf_opt_admin_test.go`.
- **`adf-opt-hierarchy`** (A-db-7, A-db-8, A-db-10, A-db-14): `internal/database/hierarchy_cache.go`,
  `internal/database/adf_opt_hierarchy_test.go`. A-db-7 is fixed inside `UpdateNewsgroupStats` only (don't add to
  `GroupCount`, don't compare an article number with a Unix time); its caller sits in intake's stats block and is not
  edited.
- **`adf-opt-analyze`** (A-tools-7, A-tools-8, A-tools-9, A-tools-10, A-tools-11): `internal/processor/analyze.go`,
  `cmd/nntp-analyze/main.go`, `internal/processor/adf_opt_analyze_test.go`.
- **`adf-opt-tcp2tor`** (A-tools-12, A-tools-13): `cmd/tcp2tor/main.go`, `cmd/tcp2tor/example.cfg`,
  `cmd/tcp2tor/adf_opt_tcp2tor_test.go`.
- **`adf-opt-rslight`** (A-tools-14, A-tools-15, A-tools-16): `cmd/rslight-importer/main.go`,
  `internal/processor/rslight.go`, `internal/processor/rslight_articles.go`, `internal/processor/adf_opt_rslight_test.go`.
  pprof only behind `-pprof 127.0.0.1:port` (empty = off).
- **`adf-opt-tools-misc`** (A-tools-6, A-tools-17, A-tools-18, A-tools-20): `internal/database/progress.go`,
  `cmd/import-flat-files/main.go`, `cmd/extract_hierarchies/extract_hierarchies.go`, `cmd/merge-active/main.go`,
  `cmd/merge-descriptions/main.go`, `cmd/nntp-fetcher/main.go`, tests next to them. Also the `-max-batch` clamp if
  A-tools-4 was not closed by intake.
- **`adf-opt-ops-tools`** (A-ops-6, A-ops-7, A-ops-8, A-ops-10): `cmd/expire-news/main.go`, `cmd/fix-references/main.go`,
  `cmd/fix-thread-activity/main.go`, `cmd/recover-db/main.go`, `cmd/usermgr/main.go`, tests. Includes their
  `NO_CACHE_BOOT` and teardown (OPS-1 deferred part for these tools).
- **`adf-opt-web-maint`** (A-ops-11, A-ops-12): `cmd/web/main.go`, `cmd/web/main_functions.go`,
  `internal/database/utils.go` (only `RsyncDIR`), tests.
- **`adf-opt-postqueue`** (A-intake-10): `internal/database/db_post_queue.go`, `cmd/post-queue/main.go` (wave 1
  touched only its signal line), `internal/database/adf_opt_postqueue_test.go`. Reset stale `in_processing` rows at
  worker start, `Retryable*` helpers for every queue statement, error return instead of `os.Exit` after the DB open.

### Wave O2 (optional; 1 slice after O1)
- **`adf-opt-artifacts-docs`** (A-tools-19, A-ops-17, A-tools-21): `git rm --cached` the tracked binaries
  (`cmd/benchmark_hash/benchmark_hash`, `cmd/benchmark_hash/debug.log`, `cmd/test-MsgIdItemCache/test-cache`,
  `cmd/test-MsgIdItemCache/test-MsgIdItemCache`) plus `.gitignore` lines; `//go:build ignore` on
  `active_files/hierarchies/organize_by_theme.go`; `-X main.appVersion` in `build_{analyze,expire-news,history-rebuild,usermgr}.sh`;
  help texts and READMEs of `cmd/nntp-fetcher`, `cmd/nntp-analyze`, `cmd/rslight-importer` rewritten from the flag
  definitions (owned: those files plus `cmd/nntp-fetcher/main.go` `showUsageExamples`, `cmd/nntp-analyze/main.go`
  usage/defaults, `cmd/rslight-importer/main.go` usage).

---

## Acceptance checks (summary)
| Check | Slice | What |
|----|----|----|
| U01 | adf-web-cron | `TestAdfCron*` pass |
| U02 | adf-web-xss | `TestAdfXSS*` pass |
| U03 | adf-web-post | `TestAdfPost*` pass |
| U04 | adf-web-nntpusers | `TestAdfNNTPUsers*` pass |
| U05 | adf-web-pagination | `TestAdfPage*`, `TestFuPagination*` pass |
| U06 | adf-web-cache | `TestAdfSanCache*`, `TestAdfSanitize*` pass under `-race` |
| U07 | adf-db-readers | `TestAdfReaders*` pass |
| U08 | adf-db-cache | `TestAdfCache*` pass |
| U09 | adf-db-lifecycle | `TestAdfLife*`, `TestAdfReopen*` pass |
| U10 | adf-proc-fatal | `TestAdfProc*` pass |
| U11 | adf-tools-fetcher | `TestAdfFetch*` pass |
| U12 | adf-ops-recover | `TestAdfRecover*` pass |
| U13 | adf-ops-scripts | shell checks of step 6 pass |
| U14 | adf-ops-tcp2tor | `TestAdfTor*` pass |
| U15 | adf-ops-misc | peering tests run without SKIP and leave no files |
| U16 | adf-web-hide | `TestAdfHide*`, `TestAdfFlags*` pass |
| U17 | adf-db-batch | `TestAdfBatch*`, `TestAdfExpire*` pass; reopen allow-list only `cleanupExpired` |
| U18 | adf-web-cmd | `TestAdfWebCmd*` pass |
| E01–E19 | all | `scripts/test-audit-delta-fixes.sh` (End-to-end) |

## Checks

On every merged tree (after wave 0 and after each merge):
```bash
gofmt -l ./cmd ./internal            # may list only internal/database/embedded_migrations.go and internal/web/web_admin_provider.go
go vet ./...
go build ./...
go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
  ./internal/nntp/... ./internal/processor/... ./internal/cache/... ./internal/models/... ./internal/common/... \
  ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/... ./cmd/web/... ./cmd/nntp-fetcher/... \
  ./cmd/recover-db/... ./cmd/tcp2tor/...
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u    # unchanged by this plan (it fixes no NNTP marker)
git status --short                   # no data-test-*, active.out, analysis_*.txt, zz_* leftovers
RACE=1 ./build_webserver.sh && ./build_recover-db.sh && ./build_fetcher.sh && ./build_tcp2tor.sh && ./build_expire-news.sh
```
Merge order in wave 1: DB slices first (`adf-db-lifecycle`, `adf-db-readers`, `adf-db-cache`), then `adf-proc-fatal`,
the web slices (`adf-web-cache`, `adf-web-xss`, `adf-web-cron`, `adf-web-post`, `adf-web-nntpusers`,
`adf-web-pagination`), then tools/ops (`adf-tools-fetcher`, `adf-ops-recover`, `adf-ops-tcp2tor`, `adf-ops-misc`,
`adf-ops-scripts` last because it changes how the build scripts build). Wave 2: `adf-db-batch`, `adf-web-hide`,
`adf-web-cmd`. Semantic interplay to watch: `adf-db-lifecycle`'s `bgStop` is used by `adf-db-batch`; `adf-web-cache`'s
no-write-back change and `adf-web-hide`'s flags reads both touch the article page; `adf-ops-scripts` removes `-race`
from the scripts every later verifier uses (build with `RACE=1`).

## End-to-end

Runner: `pugleaf-verifier` on the main checkout (`plan-audit-delta-fixes`), after wave 2.
```bash
RACE=1 ./build_webserver.sh && ./build_recover-db.sh && ./build_fetcher.sh
PORT=18998 DATA=./data-test-audit-delta-fixes scripts/test-audit-delta-fixes.sh
```

### `scripts/test-audit-delta-fixes.sh` (wave 0 writes it; later only real script bugs are fixed)
Modelled on `scripts/test-web-leftovers.sh` / `scripts/test-web-hardening.sh`:
- `set -u`; `cd "$(git rev-parse --show-toplevel)"`; `PORT` (18998), `DATA` (`./data-test-audit-delta-fixes`),
  `BIN` (`./build/webserver`), `RDB` (`./build/recover-db`), `FET` (the fetcher binary `build_fetcher.sh` writes;
  read the script for the name).
- Guards: `DATA` must match `./data-test-*` and contain no `..`; refuse when `./.update` exists; refuse when something
  answers on `$PORT`; tools `curl sqlite3 timeout getent`; `NNTPHOST` detection copied from
  `scripts/test-web-leftovers.sh` (first FQDN that `getent hosts` resolves, else exit 2 with the "set NNTPHOST"
  message), because `cmd/web` checks `-nntphostname` with `net.LookupIP` at start; `pass()/fail()/info()` print `PASS|FAIL|INFO <id> [<area>] <text>`;
  `start_server`/`stop_server` (SIGINT, wait 90 s, KILL) as in the WSH script, `trap stop_server EXIT`;
  every web start passes `-nntphostname "$NNTPHOST"`. Binaries other than the web server run with cwd `$DATA/run` and an absolute
  `-data`, so a regression can only write inside `$DATA`.
- Setup: `rm -rf "$DATA"`; first start creates the schema; stop; seed with `sqlite3` while stopped: config
  `registration_enabled=true`, `AbuseMail`, `APIEnabled=false`; groups `adf.e2e.a` (active), `adf.e2e.b` (active);
  one row `adf.e2e.nullhier` with `hierarchy`, `last_article`, `message_count` NULL (raw INSERT); a cron job row
  `adf-e2e-disabled` (`enabled=0`, command `true`). Restart (no `-edit-cronjobs`). Register user 1 (admin) and user 2
  via the web forms (cookie jars), as the WSH script does.

Checks:
- **E01** (A-ops-3a) after all other web checks: `kill -TERM $PID` → within 30 s the log has
  `Graceful shutdown completed` and the process is gone.
- **E02** (WEB-1) admin POST `/admin/crons/create` (`name=adf-e2e-new`, `command=true`, `enabled=on`) → no row
  `adf-e2e-new` in `cron_jobs`.
- **E03** (WEB-1) admin POST `/admin/crons/toggle` for `adf-e2e-disabled` → still `enabled=0`.
- **E04** (A-web-13) `curl -sD- /` has `Content-Security-Policy:` with `default-src 'self'` and `connect-src 'self'`.
- **E05** (WEB-2) `/static/js/thread-tree.js` contains `textContent` and not `${cleanPreview}`.
- **E06** (A-web-1) insert an article into `adf.e2e.a` with raw SQL (all NOT NULL columns, `hide=1`), bump
  `last_article`; anonymous GET `/groups/adf.e2e.a/articles/1`, `/groups/adf.e2e.a/message/<id>`,
  `/groups/adf.e2e.a/articles/1/preview` → 404; admin GET of the first → 200.
- **E07** (A-web-11) anonymous `/api/thread-tree?group=adf.e2e.a&thread_root=1` → not 200 (API disabled).
- **E08** (INT-7) user 2 posts to `adf.e2e.b` through `POST /SitePostSubmit` (`newsgroups`, `subject`, `body`); within 20 s a row exists in the group DB (find the file
  with `find "$DATA/db" -name 'adf_e2e_b.db'`) whose `headers_json` contains `Message-ID:`, `Date:`, `Subject:`,
  `Path:`, `Injection-Info:`, and contains neither `X-pugleaf-Trace` nor a `Bytes:` line.
- **E09** (A-db-1) after the restart, `SELECT hierarchy FROM newsgroups WHERE name='adf.e2e.nullhier'` = `adf`, and
  `SELECT count(*) FROM newsgroups WHERE hierarchy IS NULL` = 0.
- **E10** (OPS-2a) a separate short start with `-nntptcpport 21998` on port `$((PORT+1))`: the log has `are ignored`
  and nothing listens on 21998; stop it.
- **E11** (A-ops-4, server stopped) `$BIN -data "$DATA" -webport $((PORT+2)) -nntphostname "$NNTPHOST" -disable-active <file>`:
  a comment-only file → exit ≠ 0, `SELECT count(*) FROM newsgroups WHERE active=1` unchanged; a one-line list
  `adf.e2e.a` with `-disable-active-dry-run` → exit 0, unchanged; the same list without dry-run → exit 0, only
  `adf.e2e.a` active (1-field lines accepted). Active flags restored afterwards.
- **E12** (OPS-4b) a fresh scratch root `$DATA/fresh`: first start on port `$((PORT+3))` with `-useshorthashlen 9`
  reaches `/ping`; stop.
- **E13** (A-tools-2) with the web server stopped (it holds the main DB), guard first: `SELECT count(*) FROM providers WHERE enabled=1` must be 0 (else `fail` and skip:
  never contact a real server). Then in `$DATA/run`: `timeout 60 $FET -data "$ABSDATA" -fetch-newsgroups-from-remote '$all'`
  → exit ≠ 0 with `no enabled providers`, `$DATA/run/data` does not exist, `$DATA/cfg/pugleaf.sq3` exists.
- **E14** (A-ops-1) in `$DATA/run`: `$RDB -db "$ABSDATA" -data "$ABSDATA-other" -group adf.e2e.a` → exit 2 and
  `"$ABSDATA-other"` does not exist; `$RDB -data "$ABSDATA" -group adf.e2e.a -repair` (no `-force`) → exit 2,
  nothing changed (`md5sum` of the `adf_e2e_a.db` file before and after, server stopped).
- **E15** (A-ops-5) server stopped; seed two articles in `adf.e2e.b` with `hide=1, spam=1`: one with a parseable
  `date_string` (`Wed, 01 Jan 2020 10:00:00 +0000`) but a wrong `date_sent` (`2020-01-01 00:00:00`), one with an
  unparseable `date_string`; run `$RDB -data "$ABSDATA" -group adf.e2e.b -parsedates -rewritedates -force` → both
  still `hide=1, spam=1`; the first now has `date_sent = 2020-01-01 10:00:00`; the unparseable one keeps its
  `date_sent`.
- **E16** (OPS-4d/e) `grep -nE 'go build[^$]*-race' build_*.sh` empty (INFO instead when Q9 was answered no); `bash -n run_web.sh run_web_ssl.sh go_run_web.sh`;
  the flags of `run_web_ssl.sh` line 3 (minus the binary) followed by `-help` exit 0 when passed to `$BIN`.
- **E17** INFO: article-number reuse is covered by `TestAdfBatchNumbersNotReused` (posting enough web articles to
  trigger reuse would take minutes because of the posting back-off).
- **E18** INFO: `SELECT name FROM sqlite_master WHERE name='article_num_seq'` in the `adf_e2e_b.db` file = 1 row.
- **E19** no `DATA RACE` in `$DATA/webserver.log` mentioning `internal/web`, `internal/database`, `internal/cache` or
  `internal/models`.
- `SUMMARY pass= fail= info= data=$DATA log=$LOG`, exit `min(fail,125)`; setup errors exit 2.

Expected after wave 2: E01–E16, E18, E19 PASS, E17 INFO, `SUMMARY … fail=0`.

---

## Open questions (ask the user at the wave-1 boundary unless noted)
- **Q1** First registered user becomes admin (user-ID-1 rule, registration on by default; AUDIT §11.10): keep, or
  admin only via `usermgr` / registration off until an admin exists? Not changed by this plan.
- **Q2** Add `.github/workflows/tests.yml` (push/PR vet + test + known-bugs job)? Optional step of `adf-ops-scripts`.
  **Answered 2026-10-15: not in this plan; filed in `issues.md` for later.**
- **Q3** `history/history.go:469,498` fail-stop exits: the author marked them "DO NOT TOUCH"; convert to errors with a
  caller-side fail-stop, or keep? Deferred.
- **Q4** Toolchain/module upgrade for the 14 govulncheck findings (`go.mod`): separate change?
- **Q5** Nonce-based CSP (moving 9 inline script blocks and 14 inline handlers): a later plan?
- **Q6** Guards for the destructive root scripts (`history-rebuild.sh`, `delete_ALL_users.sh`, A-ops-13)?
- **Q7** (at wave 0) Was the agreed A-tools-1 bound applied by `nntp-intake-injection`? If not: amend and re-run its S3,
  or accept A-tools-1 as open.
- **Q8** D7 gates enabling the default cron jobs that migration 0021 ships disabled: an operator without
  `-edit-cronjobs` can no longer switch them on from the admin page. Acceptable?
  **Answered 2026-10-15: accepted (user's "go" on the defaults).**
- **Q9** (wave-1 boundary, before `adf-ops-scripts` merges) D11: build scripts without `-race` by default (`RACE=1`
  opt-in) and the matching `.claude/CLAUDE.md` sentence? Production binaries from `build_ALL.sh` would stop being race
  builds. `.claude/CLAUDE.md` is edited only on a yes.
- **Q10** D14: is 50 % the right threshold for `-disable-active` to refuse without `-disable-active-force`?
  **Answered 2026-10-15: no threshold, no force flag; only the empty-keep-set refusal and `-disable-active-dry-run`.**

## Progress

(filled in by `/run-plan`)

### Wave 0 (2026-10-15, orchestrator, `plan-audit-delta-fixes` from `testing-001` @ 4eb92da)
- Dependencies verified: `CountNNTPUsersByWebUserID`/`UpdateNNTPUserWebUserID` (`nntp_user_admin.go:33,48`),
  `FormatNewsDate`/`WireArticleSize` (`common/intake.go:467,434`), `ParseNNTPDateStrict` (`processor/proc-utils.go:532`),
  stats statement is `UPDATE newsgroups SET` (`db_batch.go:555`), `loopbackPprofAddr` in `cmd/history-rebuild`,
  `internal/nntp/nntp-cmd-date.go` exists.
- Hand-offs: **A-tools-1 closed** (`captureLimiter` in `processor/intake.go:178`, used in `threading.go:54,58,349`; no
  per-group capture goroutine left); **A-tools-4 closed** (`newDownloadRun` at `proc_DLArt.go:77`); **A-ops-3d/f closed**
  (`syscall.SIGTERM` in `cmd/history-rebuild/main.go:166` and `cmd/nntp-transfer/main.go:371`).
- Re-anchoring (symbols all present; moved lines): `queries.go` `getHierarchiesPaginatedDirect:2201`,
  `GetNewsgroupsByHierarchy:2828`, `ResetNewsgroupData:3074`, `query_GetSpamArticles1:3348`, `GetActiveNewsgroups:1031`,
  `GetNewsgroupsPaginated:1075/1110`; `db_batch.go` `query_processOverviewBatch:1037`, `sq.queued -= batched:768`,
  `BatchDividerChan:1816`, `BatchDivider:1821`, `ExpireCache:166`, `GetNewsgroupPointer:187`, `CheckNoMoreWorkInMaps:272`;
  `hierarchy_cache.go` scan `continue` at `:492`; `db_migrate.go` `migratedDBsCache:25,384,423`, `migrateGroupDB:367`;
  `cmd/web/main.go` `useshorthashlen:102`, NNTP flags `:113-116`, startup line `:150`, `log.Fatalf` after open `:221-240`,
  `os.Exit` sites `:248-334`, dead NNTP block from `:428`, `disable-active:132`; `cmd/web/main_functions.go`
  `startHierarchyUpdater:510`, `monitorUpdateFile:545`, `ConnectPools:579`, `NewFetchProcessor:647`,
  `disableNewsgroupsNotInActiveFile:1015`; `cmd/nntp-fetcher/main.go` `UpdateNewsgroupList:806` (`OpenDatabase(nil):810`),
  `signal.Notify:147`, backend literal `:248`/`:838`; `cmd/recover-db/main.go` `-db:36`, `-data:44`, `defer db.Shutdown():86`,
  `os.Exit:169,186,197,208,305,671,674`, rewrite statement `:877`; `cmd/tcp2tor/main.go` `-timeout:102`,
  `testSOCKS5Connection:503`, `proxy.Direct:514,...`; `cmd/history-rebuild/main.go` `:74,:79` unchanged;
  `cmd/expire-news/main.go` `signal.Notify:146`, `deleteArticles:560` (callers `:466,:748`); `cmd/post-queue/main.go:74`;
  `internal/web` anchors unchanged (`web_admin_crons.go:18,148,210,307`, `cronjobs.go:367,373,451`,
  `web_apiHandlers.go:237,267,448`, `thread-tree.js:514-536`, `webserver_core_routes.go:280,296,391,502,526`,
  `web_sitePostPage.go:365-384,520`, `web_admin_nntp.go:92,130`, `webgroupPage.go:90,118-119`,
  `web_sectionsPage.go:246,273-274`, `web_threadPage.go:80,98`, `web_threadTreePage.go:95`).
  Migration numbers free: main `0029`, single `0010`. `run_web_ssl.sh:3` and `run_web.sh:4` as cited.
- Baseline checks on 4eb92da: gofmt = 2 baseline files only; vet OK; build OK; race run over the 15 package sets all ok
  (`internal/cache`, `internal/models`, `cmd/web`, `cmd/nntp-fetcher`, `cmd/recover-db`, `cmd/tcp2tor` have no tests
  yet); known bugs: `T-nntp-article-1` only.
- `scripts/test-audit-delta-fixes.sh` written (E01–E19; E11 is three-part: comment-only file, 1-of-3 list dry-run, 1-of-3 list applied;
  E09 drops the `0029` `schema_migrations` row before the restart so the repair runs on the seeded NULL row, and deletes
  the row afterwards; a third group `adf.e2e.c` is seeded for the E11 threshold). Baseline run
  (`PORT=18998 DATA=./data-test-audit-delta-fixes`): `SUMMARY pass=1 fail=16 info=2`; only E19 passes, E17/E18 INFO.
  Baseline observation: with the NULL-hierarchy row present, `-disable-active` fails in `MainDBGetAllNewsgroups`
  (A-db-1 reproduced).
- Decisions already settled before this run: D11 done 2026-10-12 (no `-race` in the 11 build scripts; `adf-ops-scripts`
  skips OPS-4e and the CLAUDE.md sentence; E16's grep is empty at baseline). `RACE=1` in `## End-to-end` is a no-op:
  the verifier builds the web server with `go build -race -o build/webserver ./cmd/web` for E19 instead.

### Wave 1, batch A (2026-10-15): adf-db-lifecycle, adf-db-readers, adf-db-cache, adf-proc-fatal, adf-web-cache
- adf-proc-fatal: worktree `/tank0/claude/trees/go-pugleaf/agent-a0624188eade0125f`, branch
  `worktree-agent-a0624188eade0125f`, commit 83fdee3, review "merge" (2 nits dropped: CSV `f.Close()` error ignored,
  pre-existing; wrap wording), merge 9a591d3.
- adf-db-cache: `agent-a068bcfa9d7476428`, commit 8326485, review "merge" + MINOR (goroutine-count test could flake),
  merge 3717ef6; fixed inline in ef3ca64 (`TestAdfCacheCleanCronStops` counts `CleanCron` stack frames).
- adf-web-cache: `agent-a6dcb6699b233a849`, commits a8e24bf + 9c31f37, review "fix first" (MAJOR: `PrintSanitized`
  re-sanitized all five fields, body included, per call: ~29 ms per article render at 32 KiB; fixed by per-field
  helpers, output byte-identical), then merge 15e0b34. Nit dropped: `cachedSize` can drift when a write lands on a
  just-evicted entry (statistic only, pre-existing class).
- adf-db-readers: `agent-ae881c05f513c505d`, commit 85741cf, review "merge" (2 test-hygiene nits dropped), merge
  36a2753. Note: timestamp columns deliberately not COALESCEd (mattn returns text for COALESCE expressions; integers
  still arrive as int64 by storage class). Migration `0029_main_repair_null_hierarchy.sql` added.
- adf-db-lifecycle: `agent-a28c5f46d678d866b`, commit 3ba6c0d, review "merge" + MINOR (double `Shutdown` reset INIT
  twice), merge a4f2d0d; fixed inline in bf63f91 (`initResetOnce`). Second MINOR noted, no change: the reopen test
  never starts `CronClean` (NO_CACHE_BOOT), covered by `TestAdfLifeCronLoopsStop`. Open order of `OpenDatabase`
  changed (config cache/UserSalt/AbuseMail before caches and goroutines). The implementer saw one `TestStmCheck`
  failure under load in its first full run (passed on rerun and 5× in review); not attributed to the change.
- Checks on bf63f91: gofmt baseline only; vet OK; build OK; race over 12 package sets ok (incl. new
  `internal/database/adfreopen`, `internal/cache`, `internal/models`); known bugs `T-nntp-article-1` only.
- Batch B spawned from bf63f91: adf-web-xss, adf-web-cron, adf-web-post, adf-web-nntpusers, adf-web-pagination.

### Wave 1, batch B (2026-10-15): adf-web-xss, adf-web-cron, adf-web-post, adf-web-nntpusers, adf-web-pagination
- adf-web-pagination: `agent-a720fb7d0a4f828b6`, commit e484d7b, review "merge" + MINOR noted (D15 side effect: with
  `HasPrev`/`HasNext` false, `pagination.html` also hides First and Last in cursor mode; record for the cursor
  decision), merge 8eb175c; comment wording fixed inline in b56ce8f.
- adf-web-xss: `agent-a98e5e7cf72150c95`, commit 60edaec, review "merge" (2 nits dropped), merge 4c7aa4c. CSP equals
  D1 byte for byte; the hosts cover `base_chat.html`'s jsdelivr CSS/fonts; `escapeHtml`/`getAPIToken` removed.
- adf-web-post: `agent-a31339ec21e85d5f5`, commits 5239582 + ccf3c04, review "fix first": MAJOR outside the slice
  (`common.ReconstructHeaders` with `withPath=false`, the cmd/post-queue POST path, would relay the stored
  `Injection-Info`/`Injection-Date` and an injecting agent answers 441) → fixed inline in **bd2ffe4**
  (`PostOnlyIgnoreHeadersMap`, test `TestAdfPostHeadersDropInjectionOnPost` in `internal/common`); MINOR
  cmd/audit-web-posts allow-list → same commit (D8 names + single-header list, legacy X-pugleaf-Trace/Bytes kept,
  "clean D8 block" test case); MINORs in the slice (CRLF bodies normalized, References folded at 78 columns) fixed
  in ccf3c04; merge 7dd90b7. Leftover: a 1000-byte Subject gives a 1009-octet stored line (pre-existing form limit);
  empty UserSalt would write `posting-account=""` (intake omits it; salt is generated at init, so unreachable).
- adf-web-nntpusers: `agent-a1eab6765d3954fdb`, commit 2996537, review "merge" + MINOR noted (a legacy mapping to
  a deleted web user blocks the whole update until "no mapping" is chosen), merge f761604. Leftovers: check-then-write
  race on the one-account rule (needs a partial unique index after a duplicate-unmapping migration); the edit form
  always submits the posting state via a hidden `posting=off` field (handler reads `GetPostFormArray`).
- adf-web-cron: `agent-af21f31672fb93ec5`, commits bb1e8a6 + c1e0af3 (ownership extended to
  `internal/web/fu_cron_test.go`: `TestFuCronLoadAndStartJobsStartsWhileRunning` used rows without a DB row whose
  schedulers now forget themselves, so it raced; rewritten with parked real rows), review "merge", merge 09799ce;
  template "create below" sentence gated and the hint assertion fixed inline in 576103d. Leftovers: an ended job's
  log buffer is dropped with its registration (A-web-20 side effect); toggle check-then-flip race (needs a
  conditional UPDATE in `db_cron_jobs.go`); `calculateNextRun` divides by zero for interval 0 with a start time
  (unreachable through the handlers).
- Load-related nntp test flakes seen by implementers while several suites ran at once (`TestStmCheck` once,
  `TestAuthFailures` once); both pass alone and in the orchestrator's full runs.
- Checks on 576103d: gofmt baseline only; vet OK; build OK; race over 13 package sets ok (incl. cmd/audit-web-posts);
  known bugs `T-nntp-article-1` only.
- Batch C spawned from 576103d: adf-tools-fetcher, adf-ops-recover, adf-ops-tcp2tor, adf-ops-misc, adf-ops-scripts.
