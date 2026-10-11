# Audit audit-db (base 4c08048)

Delta audit of `internal/database` after the three merged plans WSH, LO and FU. Their findings are
cited as `WSH:<id>`, `LO:<id>` and `FU:<id>` and are not reported again. Server-side NNTP findings are
in `audit-nntp-sec.md` and cited by their ids (`SEC-2` server side, `A-nntp-sec-3`, `A-nntp-sec-4`,
`A-nntp-sec-6`). The report follows `docs/audit/README.md` (D1, K6). `internal/database` is
byte-identical between 84c2081 and 4c08048 (`git diff --stat 84c2081..HEAD -- internal/database` is
empty), so every `path:line` anchor below holds at both commits. Paths are under `internal/database/`
unless a path is given.

## Scope and method

Files read in full: `db_batch.go`, `database.go`, `db_init.go`, `db_groupdbs.go`, `db_migrate.go`,
`embedded_migrations.go`, `sqlite_retry.go`, `groups_hashmap.go`, `utils.go`, `users.go`,
`hierarchy_cache.go`, `db_config.go`, `config_cache.go`, `db_cron_jobs.go`, `article_cache.go`,
`nntp_auth_cache.go`, and the migrations that define `newsgroups` and `articles`
(`0001_main_schema.sql`, `0001_single_db_schema.sql`, `0002_main_add_nntp_fields.sql`,
`0007_main_optimize_hierarchy_queries.sql`, `0010_main_add_max_art_size.sql`).
Read in part: `queries.go` (all newsgroup, article, overview, range, hierarchy, reset and
date-filter functions, every dynamic-SQL site), `thread_cache.go` (the parts FU did not test:
`InitializeThreadCache`, `MemCachedThreads.CleanCron`, `UpdateThreadMetadata`), `db_rescan.go`
(entry points, `GetLatestArticleNumber*`, the table-clearing `Sprintf`), `db_aimodels.go`,
`db_nntp_users.go` (auth path only). I also read the callers needed to rate reachability:
`internal/processor/threading.go` (`processArticle`), `proc_DLArt.go`, `processor.go`,
`proc-utils.go` (`extractGroupsFromHeaders`), the `GetGroupDB` callers in `cmd/*` and
`internal/{processor,postmgr,preloader,nntp}`, `cmd/expire-news/main.go` (delete set),
`internal/web/web_hierarchiesPage.go` (page parsing).

Excluded as already covered: WSH H6–H9, P3, P4, P6, P8, L4, L5; LO C3–C5, F16, F17, F20, F22;
FU A2–A4, C1, E5–E9, E11; and the unassigned FU:E13, FU:E15, FU:E16 (not reported again).
I checked the FU shutdown bounds of the batch writer and did not audit them again:
`batchShutdownClock` guards `getBatchGroupDB` (`db_batch.go:652`), the `retry1` insert loop
(`:777`) and `updateNewsgroupStatsWithRetry` (`:713`). All three are present and wired to `IsDBshutdown`.

Tools (`docs/audit/tools/`, filtered to this package): staticcheck/golangci-lint `unused`
(`getOrCreateStringSlice`, `returnStringSlice`, `BatchOrchestrator.mux`, `isDirectChild`, three
`query_*Search*` consts), errcheck (unchecked `Close`/`Rollback` in `db_batch.go`, `db_rescan.go`,
`config_cache.go:79`, `db_aimodels.go`), ineffassign (`db_batch.go:1627`, `db_rescan.go:590`), and
gosec G101 false positives on SQL consts. These appear in the function inventory where they matter.
govulncheck reaches this package only through stdlib/x-net call paths (`config_cache.go:110`,
`db_sessions.go:35`), and none of those is a database defect.

Repros:
- A temporary `internal/database/zz_audit_db_test.go` ran on the package `TestMain` (shared
  `w0TestDB`, temp data root). An isolated `Database` with its own data dir and group map shares the
  main DB and carries a fake `ProcessorInterface` that counts `AddArticleToHistory` calls. The
  command was `go test -count=1 -run TestZZAuditDB -v ./internal/database/`, and every test passed
  (15.8 s). I deleted the file before committing. A copy is kept at the gitignored
  `data-test-audit-db/_repro/zz_audit_db_test.go.txt`.
- A standalone program `data-test-audit-db/_repro/reopen/main.go` (gitignored) ran with
  `go run ./data-test-audit-db/_repro/reopen/main.go`. Its data root is
  `./data-test-audit-db/reopen`, deleted afterwards.
- No server was started, and nothing ran against `./data`, `data_ram` or `data_local`.

## Summary

Counts by severity: **high 5, medium 10, low 6** (21 findings: 7 seeds, 14 new).
By category: bug 10, robustness 9, security 2.

| severity | findings |
|----|----|
| high | SEC-2 (DB side), INT-5 (DB side), INT-6, A-db-1, A-db-2 |
| medium | SEC-6 (DB side), BUG-1, BUG-2, OPS-1 (DB side), A-db-3, A-db-4, A-db-5, A-db-6, A-db-7, A-db-8 |
| low | A-db-9, A-db-10, A-db-11, A-db-12, A-db-13, A-db-14 |

Five most important:

1. **A-db-1 (high)**: newsgroups rows that the batch writer creates (`query_updateNewsgroupsStats`)
   have `hierarchy = NULL`. `MainDBGetNewsgroup`, `MainDBGetNewsgroupByID` and `MainDBGetAllNewsgroups`
   scan that column into a `string` and fail. One such row stops `nntp-fetcher` (`$all`),
   `expire-news $all`, `history-rebuild`, `recover-db`, `nntp-transfer` and `nntp-analyze`. NNTP
   `GROUP` answers 411 for the group, while `LIST ACTIVE` still lists it. Measured.
2. **INT-6 (high)**: history adds are gated on `MainDBGetNewsgroup`. Because of A-db-1 they are
   skipped for **every** batch of an auto-created group, not only the first: 0 history adds for 4
   committed articles over two batches (measured). Message-id lookups and dedup then miss those articles.
3. **A-db-2 (high)**: `ArticleCache.removeElement` rebuilds the map key through
   `Batch.GetNewsgroupPointer`. `ExpireCache` replaces that pointer after about 2 minutes, so evictions
   remove the list element but leave the map entry, and the cache map grows without bound with full
   article bodies (measured: maxSize 2, after 5 Puts the list holds 2 and the map 5; an evicted
   entry is still served).
4. **INT-5 (high, DB side)**: `articles.article_num` is a rowid without AUTOINCREMENT. After the
   top-numbered articles are deleted (expire-news), new articles reuse their numbers. The leftover
   `thread_cache` row is then inherited: the new root #1 showed the old thread's `children="2"`,
   `count=2`, and `newsgroups.last_article` stayed at 2. Measured.
5. **SEC-2 (high, DB side)**: `GetGroupDB` creates a directory, file and schema for any name, and the
   batch upsert creates an `active=1` newsgroups row for any name the intake accepts. It also brings
   back groups an admin deleted. Every caller outside `internal/web` is classified below.

The NNTP auth cache old-password window is confirmed again (SEC-6, medium). The DB-side fix has to
compare against the stored hash because the processes are separate.

## Findings

### A-db-1: batch-created newsgroups rows have NULL hierarchy and break MainDBGetNewsgroup / MainDBGetAllNewsgroups

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-1 | high | bug | `db_batch.go:553-562,944-950`, `queries.go:190-257`, `migrations/0007_main_optimize_hierarchy_queries.sql:5`, `models/models.go:59` | CONFIRMED |

**Scenario:** an article whose `Newsgroups:` names a group with no newsgroups row reaches the batch
writer. In practice this is non-bulk intake: IHAVE/TAKETHIS/POST via
`ProcessIncomingArticle`, `processor.go:273` with `bulkmode=false`. The fetcher runs in bulk mode
(`proc_DLArt.go:201`) and stores only into the fetched group. The upsert inserts
`(name, message_count, last_article, updated_at)`, so `hierarchy` stays NULL. From then on:
- `MainDBGetAllNewsgroups` fails for the whole table. `cmd/nntp-fetcher/main.go:194-198` returns,
  `cmd/history-rebuild/main.go:229-233` returns with an error, `cmd/expire-news/main.go:355-358`
  (`$all`) errors, and so do `recover-db`, `nntp-transfer`, `nntp-analyze`, `fix-references`,
  `fix-thread-activity`, `cmd/web/main.go:416` (startup listing), `preloader.go:268` and the unused
  `GetTotalThreadsCount`.
- `MainDBGetNewsgroup(<that group>)` fails. NNTP `GROUP` answers `411 No such newsgroup`
  (`nntp/nntp-cmd-group.go:20-22`), but `LIST ACTIVE` (`GetActiveNewsgroups`, which does not select
  `hierarchy`) lists it. `DeleteNewsgroup` falls back to the name, and
  `GetArticleCountFromMainDB` fails.
- `MainDBGetNewsgroupByID` fails, so `NewsgroupDBsIDcache` (`db_groupdbs.go:65`),
  `postmgr.go:117` and `history-rebuild` id lookups (`:589`) fail.

The only repair is `UpdateHierarchyCounts` (`queries.go:2286-2291`,
`UPDATE ... SET hierarchy = ... WHERE hierarchy IS NULL`), which only the admin "update hierarchies"
button calls (`web/web_hierarchiesPage.go:117`).

**Evidence:** `models.Newsgroup.Hierarchy` is a `string` (`models/models.go:59`). The three queries
select `hierarchy` and scan it into `&g.Hierarchy` (`queries.go:202`, `:224`, `:248`). Migration 0007
adds the column without a default. `InsertNewsgroup` fills it (`queries.go:124-126`), but the batch
upsert (`db_batch.go:553-562`) and `UpsertNewsgroupDescription` (`queries.go:1018-1020`, unused) do not.
`TestZZAuditDBAutoCreatedGroup` (one batch of 3 articles for an unknown name, then one more):
```
ZZ autocreate batch1: row={hierNull:true msgCount:3 lastArt:3 active:true ...} historyAdds=0
ZZ autocreate MainDBGetNewsgroup err=sql: Scan error on column index 12, name "hierarchy": converting NULL to string is unsupported
ZZ autocreate MainDBGetAllNewsgroups err=sql: Scan error on column index 12, name "hierarchy": converting NULL to string is unsupported
ZZ autocreate GetActiveNewsgroups n=1 err=<nil>
ZZ autocreate MainDBGetNewsgroupByID err=sql: Scan error on column index 12, name "hierarchy": converting NULL to string is unsupported
```
The same row also appeared through the real capture path (global `BatchDividerChan` → orchestrator
of the shared test DB, `TestZZAuditDBQueuedCounterAndRealPath`): `row={hierNull:true msgCount:5 ...}`.

**Fix (S):** set `hierarchy` in `query_updateNewsgroupsStats`
(`ExtractHierarchyFromGroupName(name)`). Add a main migration
`UPDATE newsgroups SET hierarchy = ... WHERE hierarchy IS NULL` (the 0007 expression). Make the three
readers robust with `COALESCE(hierarchy, '')` or a `sql.NullString`. Whether the batch should create
newsgroups rows at all is SEC-2.

---

### INT-6: history adds skipped for auto-created groups, permanently

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-6 | INT-6 | high | bug | `db_batch.go:855-891` (gate `:861-869`, CaseDupes `:884-889`) | CONFIRMED (worse than the seed) |

**Scenario:** the seed says the first batch of an auto-created group skips the history adds,
because the newsgroups row is created later in the same batch (`:944-950`). Because of A-db-1 the gate
fails **for every later batch too**: the row exists, but `MainDBGetNewsgroup` cannot scan it. None of
the articles of such a group reach the history index. Two more effects follow:
- `msgIdItem.Response` is set to `CaseDupes` only inside the `historyOK` branch (`:884-889`). The
  item stays `CaseLock` until `TmpCacheTTL` (15 s, `history/history_MsgIdItemCache.go:20`) runs out.
- The articles cannot be found by message-id through history. History-based `CHECK`/`IHAVE`
  answers "wanted" for them. `history-rebuild` cannot repair this, because it fails on the same row
  (A-db-1).

**Evidence:** `TestZZAuditDBAutoCreatedGroup` with the fake processor:
`batch1 ... historyAdds=0`, then
`batch2: row={hierNull:true msgCount:4 lastArt:4 ...} historyAdds=0 (4 articles committed)`.
With a pre-created row (`InsertNewsgroup` sets `hierarchy`), the same code adds history
(`TestZZAuditDBDuplicateCounted`: `historyAdds=2`).

**Fix (M):** resolve the group id from the upsert itself (`INSERT ... ON CONFLICT ... RETURNING id`,
SQLite ≥ 3.35) or look it up after the stats upsert. Run PHASE 3's history loop after the stats
transaction, or retry the lookup there. Fix A-db-1 first. Set `CaseDupes` for every committed article,
whether or not the history add succeeded.

---

### A-db-2: ArticleCache map grows without bound; evicted entries stay and are served

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-2 | high | robustness | `article_cache.go:127-140` (key at `:129-130`), `queries.go:906,911,929,935`, `db_batch.go:166-184,187-210`, `db_groupdbs.go:157` | CONFIRMED |

**Scenario:** `ArticleCache` stores entries under `group:num` (`Put`, key from the caller's
`articleNum`). Eviction and `Cleanup` remove by a key they recompute as
`entry.Article.ArticleNums[ac.DB.Batch.GetNewsgroupPointer(entry.GroupName)]`. Readers fill
`ArticleNums` under `groupDB.NewsgroupPtr` (`queries.go:906`, `:929`), the pointer taken from
`TasksMap` when the GroupDB was created (`db_groupdbs.go:157`). `ExpireCache` deletes a `TasksMap`
entry once `Expires` (120 s, set at creation and only renewed by `processNewsgroupBatch`) has
passed and its channel is empty (`db_batch.go:172`). In a reader process (`nntp-server`, `web`) that
is always the case about 2 minutes after the group was first opened. The next `GetNewsgroupPointer`
then returns a new pointer, `ArticleNums[newPtr]` is 0, and `removeElement` deletes `group:0` (a no-op)
but removes the list element. The real map entry stays, pointing at an element no longer in the list.
- The LRU bound (`maxSize`, default 1000) limits only the list. The map keeps every article ever
  read, with `BodyText` and `HeadersJSON`, for the life of the process. A crawler that walks a spool
  through ARTICLE/HEAD or the web article pages loads all of it into RAM.
- `Get` still finds the orphaned entry and returns it (`MoveToFront` of a detached element is a
  no-op). A later `Put` on that key updates the detached entry, which is never evicted again.
- Together with INT-5, a reused article number is answered from the stale entry for good.
  Hide/spam changes and resets (`ResetNewsgroupData`) don't invalidate entries either.

**Evidence:** `TestZZAuditDBExpireCacheAndArticleCache`. The real `ExpireCache` of the shared test DB
deleted the `TasksMap` entry once `Expires` was in the past. Then the test called
`Put(name, i, a)` five times with `a.ArticleNums[groupDB.NewsgroupPtr] = i`, which is what
`GetArticleByNum` does (`queries.go:905-911`), on a `NewArticleCache(2, ...)`:
```
ZZ acache groupDB.NewsgroupPtr=0x213e5cf24670 current TasksMap ptr=0x213e5d346fe0 same=false
ZZ acache maxSize=2 after 5 Puts: lruList.Len=2 len(map)=5
ZZ acache Get(evicted #1) hit=true
```

**Fix (S):** store the map key in `ArticleCacheEntry` and delete by that key; don't derive keys from
`ArticleNums`/`GetNewsgroupPointer`. Also see A-db-5 about replacing pointers that are still in use.
Add a unit test that the map length is at most `maxSize`.

---

### INT-5 (DB side): article numbers are reused; derived rows and counters are inherited

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-5 | INT-5 | high | bug | `migrations/0001_single_db_schema.sql:12`, `db_batch.go:999,553-562`, `thread_cache.go:28-56`, `database.go:396,484`, `queries.go:3060-3138`, `cmd/expire-news/main.go:603,620` | CONFIRMED (DB side) |

**Scenario:** `article_num INTEGER PRIMARY KEY` without AUTOINCREMENT takes `max(rowid)+1`, or 1 on
an empty table. Expire deletes by age, so the top number goes whenever the newest-numbered article is
past the cutoff: a quiet group expires completely, or an old-dated backfilled article holds the top
number. The next insert then reuses numbers. Every row keyed on the number survives the delete:
- `thread_cache` (expire-news deletes only `articles` and `threads`, `cmd/expire-news/main.go:603`,
  `:620`). A new root with a reused number goes through `InitializeThreadCache`'s
  `ON CONFLICT(thread_root) DO UPDATE`, which keeps the old `child_articles` and `message_count`. The
  thread page then lists the previous thread's children, whose numbers may themselves be reused.
  `cached_trees` and `tree_stats` are left behind the same way.
- the main-DB `spam(newsgroup_id, article_num)` and `user_spam_flags` rows
  (`database.go:396`, `:494`, `HasUserFlaggedSpam` `:508-526`). A new article inherits the spam
  listing, and a user who flagged the old article cannot flag the new one.
- the article cache (A-db-2) serves the old article under the reused number.
- `newsgroups.last_article` keeps the old maximum (`CASE WHEN excluded.last_article > last_article`,
  `db_batch.go:558-561`), and `message_count` only increments. NNTP clients holding a high-water mark
  treat the reused numbers as already read.

`ResetNewsgroupData` restarts numbering on purpose (`queries.go:3117`) but leaves the spam tables, the
article cache and the main-DB counters alone (`ResetNewsgroupCounters` is a separate call).

**Evidence:** `TestZZAuditDBArticleNumberReuse`: a root and a reply through the batch writer, then
`DELETE FROM articles; DELETE FROM threads` (what expire-news does for expired rows), then a new root:
```
ZZ reuse after root+reply: articles=[1 <zzadb-root-10@...>] [2 <zzadb-reply-11@...>]  thread_cache={root=1 count=2 children="2"}  newsgroups(last_article=2 message_count=2)
ZZ reuse after expire-all + new root: articles=[1 <zzadb-newroot-12@...>]  thread_cache={root=1 count=2 children="2"}  newsgroups(last_article=2 message_count=3)
```

**Fix (M):** make numbering monotonic. One option is `AUTOINCREMENT`, which needs a table rebuild
migration per group DB. The other is to insert with an explicit
`article_num = max(newsgroups.high_water, max(rowid)) + 1` inside the batch transaction and keep
`high_water` current. Either way, delete `thread_cache`, `cached_trees`, `tree_stats` and the spam
rows together with the articles (expire-news and reset), and invalidate the article cache on
delete/reset. The RFC side (`bytes` semantics) belongs to `audit-intake`.

---

### SEC-2 (DB side): group DBs and active newsgroups rows are created for any name

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-2 | SEC-2 | high | security | `db_groupdbs.go:117-190,194-270`, `db_batch.go:553-562,944-950`, `queries.go:468-541` | CONFIRMED (DB side) |

**Scenario:** two DB-side mechanisms create state for names nobody validated.
1. `GetGroupDB` creates `<data>/db/<md5>/<sanitized>.db` with its directory and schema for any
   string (`initGroupDB` `:218-247`). The server-side measurement is SEC-2/A-nntp-sec-4 in
   `audit-nntp-sec.md` (4 files from 4 `LISTGROUP` names, plus map entries and open handles).
2. The batch upsert creates a newsgroups row for any name the intake accepted, with `active` defaulting
   to 1 and `hierarchy` NULL (A-db-1). Non-bulk intake (`processArticle`) runs `GetGroupDB` and
   enqueues for every group in `Newsgroups:` that passes the lax `IsValidGroupName`
   (`processor/proc-utils.go:414-454`, `threading.go:209-256`), and never checks the main DB. So an
   authenticated poster or a peering feed that crossposts to groups not carried locally creates them
   as active groups. A group that an admin deactivated and deleted (`DeleteNewsgroup` removes only
   the row, never the file) comes back as `active=1` on the next crosspost. An inactive group keeps
   receiving articles, because the upsert does not touch `active` and the intake doesn't check it.

`GetActiveNewsgroups` (LIST ACTIVE, web home/stats) shows such rows at once.

**Evidence:** item 1 is measured in audit-nntp-sec. For item 2, `TestZZAuditDBAutoCreatedGroup`
shows a fresh name ending as `active:true msgCount:3` after one batch, and the same happened on the
real capture path (`TestZZAuditDBQueuedCounterAndRealPath`). Bring-back after delete: `query_DeleteNewsgroup`
removes the row (`queries.go:468`). The next upsert has no conflict and inserts with the column
default `active BOOLEAN NOT NULL DEFAULT 1` (`migrations/0001_main_schema.sql:36`).

**Group-DB creation by caller outside `internal/web`** (web callers are WSH:C4 / LO:F10):

| caller | name source | verdict |
|----|----|----|
| `processor/threading.go:232` (`processArticle`, non-bulk) | `Newsgroups:` header, lax regex only | **unvalidated**: creates DB + active row (this finding) |
| `nntp/nntp-cmd-group.go:51` (`LISTGROUP`) | client argument | **unvalidated**: SEC-2 server side (audit-nntp-sec) |
| `nntp/nntp-cmd-xover.go:71`, `nntp-cmd-xhdr.go:77`, `nntp-article-common.go:163` | `c.currentGroup`, set only by `GROUP` after `MainDBGetNewsgroup` | validated |
| `nntp/nntp-article-common.go:203` | message-id path group list | see audit-nntp-sec |
| `processor/proc_DLArt.go:90`, `cmd/nntp-fetcher/main.go:452` | fetch list from `MainDBGetAllNewsgroups` or `-group` | validated (rows), or operator flag |
| `processor/PostQueue.go:143` | `GetNewsgroupByName` + `Active` check (`:132-140`) | validated |
| `processor/processor.go:245`, `proc_reuse.go:71` | `NewsgroupDBsIDcache` (main-DB id → name) | validated |
| `postmgr/postmgr.go:156` | post_queue entry, `MainDBGetNewsgroupByID` at `:117` | validated |
| `processor/rslight.go:432` | legacy import file / sections DB | operator input |
| `preloader/preloader.go:278`, `cmd/history-rebuild/main.go:324`, `cmd/recover-db/main.go:718,1036`, `cmd/fix-references/main.go:132`, `cmd/nntp-transfer/main.go:1227` | `MainDBGetAllNewsgroups` rows | row exists; a row without a file gets an empty DB |
| `cmd/expire-news/main.go:407,687,769`, `cmd/recover-db/main.go:363,1232`, `cmd/fix-thread-activity/main.go:63`, `cmd/history-rebuild/main.go:611`, `cmd/nntp-transfer/main.go:482`, `cmd/web/main_functions.go:62,179,220` | `-group` flags, debug lists, main-DB rows | operator input: a typo creates an empty DB (low) |
| `database.go:410,426` (`IncrementArticleHide`, `UnHideArticle`), `db_spam_flags.go:23` | web admin/spam handlers | web side (covered by WSH:C4) |
| `queries.go:1539` (`GetTotalThreadsCount`) | every newsgroups row | unused; would open or create one DB per row |
| `db_rescan.go:16,101,400`, `queries.go:3064,3488,3548,3649` | callers above | inherit the caller's verdict |

**Fix (M):** keep `GetGroupDB` as it is (D4 Appendix B). In `processArticle`, drop groups that have
no newsgroups row, or that are inactive, before `GetGroupDB`/enqueue (one `MainDBGetNewsgroup` per
group, cacheable). The batch upsert must never INSERT: turn it into an `UPDATE ... WHERE name = ?` and
log when 0 rows are affected. `DeleteNewsgroup` should also move or delete the group file, or keep a
tombstone row so a crosspost cannot bring the group back.

---

### SEC-6 (DB side): the auth cache accepts the old password after a change

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-6 | SEC-6 | medium | security | `nntp_auth_cache.go:50-90`, `db_nntp_users.go:137-147,281-328` | CONFIRMED (old-password half) |

**Scenario:** the cache key is the username, and a hit only compares `sha256(password)` with the
value stored at login (`nntp_auth_cache.go:82-89`). Neither `UpdateNNTPUserPassword` nor
`DeactivateNNTPUser` nor `DeleteNNTPUser` touches the cache, and they usually run in another process
anyway (`cmd/web`, `cmd/nntpmgr`). The old password keeps working until either the TTL
(`NNTP_AUTH_CACHE_TIME` 15 min) runs out or a login with the new password overwrites the entry.
The revoked-user half (LO:F22, `refuseDisabledNNTPUser`) is in place on every hit (`:293-296`) and is
not reported again. Two more DB-side notes: the cache holds unsalted `sha256(password)` in memory, and
`cleanupExpired` (`:154`) never stops (Global state table).

**Evidence:** `TestZZAuditDBSec6`:
`after UpdateNNTPUserPassword: old err=<nil> new err=<nil> old-after-new err=invalid password`.
This matches audit-nntp-sec's repro.

**Fix (S):** on a hit, `AuthenticateNNTPUser` already loads the row (`GetNNTPUserByID`, `:286`). Store
the bcrypt hash string (`user.Password`) in `AuthCacheEntry` at `Set`, compare it on `Get`, and
invalidate and fall through to bcrypt when it differs. That works across processes. Calling
`InvalidateNNTPUserAuth` from the three mutators only helps inside one process.

---

### BUG-1: GetArticleByNum never sets DBArtNum

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-1 | BUG-1 | medium | bug | `queries.go:892-915` (`:906`), compare `:929-930` | CONFIRMED |

**Scenario/Evidence:** `GetArticleByNum` sets only `ArticleNums[groupDB.NewsgroupPtr]`. The message-id
variant also sets `DBArtNum` (`:930`). `TestZZAuditDBMisc`: `GetArticleByNum(1300).DBArtNum=0`.
The server effect (current article 0) is in audit-nntp-sec. On the DB side there is a second effect:
the article cache can hold the same article from either path, so `DBArtNum` depends on which call
filled it first.

**Fix (S):** `a.DBArtNum = artnum` at `:906`.

---

### BUG-2: range reads clamp the number range, not the row count

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-2 | BUG-2 | medium | bug | `queries.go:1816-1818` (`GetOverviewsRange`), `:1878-1880` (`GetHeaderFieldRange`) | CONFIRMED, wider than the seed |

**Scenario:** the clamp is `endNum = startNum + 1000` on the number range. A dense range returns
1001 rows, which the seed covers. A **sparse** range returns rows only from the first 1001 numbers,
and that can be none at all. After expiry a group starts at, say, 1201. A client that asks
`XOVER 1-1500` (low=1 is what GROUP/LIST report, BUG-4) gets an empty 224 and concludes that the
group is empty.

**Evidence:** `TestZZAuditDBMisc`:
`1500 articles, range 1-5000: GetOverviewsRange=1001 GetHeaderFieldRange=1001`, then after deleting
1–1200: `range 1-1500: GetOverviewsRange=0`. Missing `rows.Err()` and retry in both functions is
A-nntp-sec-3.

**Fix (S):** drop the Go clamp and use `... AND article_num BETWEEN ? AND ? ORDER BY article_num
LIMIT ?` with a row limit. The caller sends what it got and can continue from the last number returned.

---

### OPS-1 (DB side): fatal exits and globals that block re-open

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| OPS-1 | OPS-1 | medium | robustness | `db_init.go:117,140-148,161-174,185-196,204-230`, `db_batch.go:1573,1753-1812`, `db_migrate.go:25`, `groups_hashmap.go:27,82-109` | CONFIRMED |

**Scenario/Evidence:**
- `log.Fatal` in library code: `db_init.go:141` (euid < 1000), `:206`, `:212`, `:224` and `:229`
  (config reads and writes during open). No other `log.Fatal`, `os.Exit` or `panic` exists in the package.
- `INIT` is set before anything can fail (`:148`) and is never reset. A failed open leaves
  `INIT=true`, and the process can never open a database again. The failed open also leaks the
  opened `mainDB` when `Migrate` or `LoadDefaultProviders` fails (`:166-174` return without
  `mainDB.Close()`). Repro (`_repro/reopen`):
  ```
  open#1 (bad data dir): err=failed to initialize main database: failed to ping main database: unable to open database file: not a directory INIT=true
  open#2 (valid data dir): db=false err=main database already initialized
  ```
- After the documented teardown (`close(StopChan)`, `WG.Wait()`, `Shutdown()`, then 500 ms), these
  goroutines of the package are still running (same program, pprof goroutine dump):
  `BatchDivider`, `cronDBEvery`/`CronDB` (exits on its next 10 s tick), `NNTPAuthCache.cleanupExpired`,
  `SQ3batch.ExpireCache`, `GHmap.GHmapCron`, `GroupSectionDBCache.CronClean`,
  `MemCachedThreads.CleanCron`. Every one except `cronDBEvery` loops forever. `BatchDivider`
  reads the package-global `BatchDividerChan`, so a second `Database` in the same process (only
  possible after resetting `INIT`) gets its articles routed by whichever divider receives them.
- `migratedDBsCache` (`db_migrate.go:25`) is keyed by group name only and is never cleared: see
  A-db-6 for the effect it has inside a single process.
- The "batch retries forever after Shutdown" half of the seed is fixed (FU, verified above).

**Fix (M):** return errors instead of `log.Fatal` (the euid check may stay in `main`). Set `INIT`
only after a successful open and reset it in `Shutdown`. Close `mainDB` on every error path after
`initMainDB`. Give every background loop a stop channel tied to `StopChan`/`Shutdown`. Make
`BatchDividerChan`, `GroupHashMap` and `migratedDBsCache` fields of `Database` or `SQ3batch`.

---

### A-db-3: duplicate message-ids are counted in message_count, and a re-delivered one adds a second threads row

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-3 | medium | bug | `db_batch.go:999,1052-1103` (`:1083-1091`), `:813-823,947`, `:1150,1193`, `db_groupdbs.go:336-343` | CONFIRMED |

**Scenario:** PHASE 1 inserts with `INSERT OR IGNORE` and then maps message-ids back to numbers.
- Two copies of one message-id in a batch: one row is stored, and only the first copy gets a number
  (`break forBatches` at `:1091`, "did not reassign" at `:1087`). The stats upsert still adds
  `len(batches)` (`:947`).
- A message-id already stored in the group: the `SELECT` returns the **existing** number, so the
  article counts as committed. `message_count` goes up, the history add runs again, and
  `batchProcessThreadRoots` inserts another `threads` row for the existing root (`:1193`; the
  `threads` table has no unique constraint).

Both are reachable. The code itself says remote overviews contain duplicates
(`db_batch.go:1118`: "we got a duplicate in remote servers overview"). The fetcher runs in bulk
mode with no history claim (`threading.go:101-104`), and its only guard is
`ExistsMsgIdInArticlesDB` at enqueue time (`threading.go:240`). That guard cannot see a copy that is
still in `BATCHchan`, and it returns `false` on **any** error (`db_groupdbs.go:339-341`).

**Evidence:** `TestZZAuditDBDuplicateCounted` (newsgroups row created first, so history adds work):
```
ZZ dup batch1 (A,A,B): article rows=2 message_count=3 historyAdds=2
ZZ dup batch2 (A again): article rows=2 message_count=4 historyAdds=3 threads rows=3
```

**Fix (S):** count only rows whose `RowsAffected()` is 1 (check it per `stmt.Exec`), and mark the
ignored ones so PHASE 2/3 skip them. Let `ExistsMsgIdInArticlesDB` return `(bool, error)` and treat
an error as "unknown, retry later".

---

### A-db-4: newsgroups.updated_at written as 0001-01-01 for crossposted articles

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-4 | medium | bug | `db_batch.go:870-932` (`:904-908`, `:914-916`), `:939-947`; `processor/threading.go:220` | CONFIRMED |

**Scenario:** `latestDate` is taken only from articles that PHASE 3 clears, that is, articles no
longer referenced by another group (`:904-908` `continue` before `:914`). `processArticle` appends
every target group to `NewsgroupsPtr` before it checks whether that group already holds the article
(`threading.go:220` vs `:240`), so skipped groups stay in the list for good. A batch made of
crossposts, or of articles with a skipped group, therefore writes
`updated_at = '0001-01-01 00:00:00'` (`:947`, unconditional `updated_at = excluded.updated_at`).
Otherwise `updated_at` is the newest `DateSent` of the batch, so a backfill batch also moves it
backwards. `updated_at` is the "activity" order and the last-activity display:
`getDirectGroupsAtLevelDirect` (`queries.go:2102`), `GetNewsgroupsByHierarchy` (`:2845`), section
listings (`:1367`), the hierarchy cache `UpdatedAt` (`hierarchy_cache.go:488-526`) and
`UpdateHierarchiesLastUpdated`.

**Evidence:** `TestZZAuditDBCrosspostUpdatedAt` (one article, `NewsgroupsPtr` = this group + one more):
`updated_at before="2026-10-11 10:56:57" after="0001-01-01 00:00:00" (article date 2026-10-01 12:00:00)`.

**Fix (S):** compute `latestDate` over all committed articles before the clearing step, and write
`updated_at = MAX(updated_at, excluded.updated_at)` (skipping a zero time).

---

### A-db-5: ExpireCache deletes TasksMap entries that are still in use

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-5 | medium | robustness | `db_batch.go:166-184` (`:172`), `:187-210`, `:731-745` | CONFIRMED (deletion); PLAUSIBLE (concurrent batch) |

**Scenario:** the sweep deletes a task when `Expires` has passed and `len(BATCHchan) == 0`. It does
not check `BATCHprocessing`, and `GetNewsgroupPointer` never renews `Expires`. Effects:
1. A batch that runs past 120 s has already drained its channel: a `GetGroupDB` wait, the `retry1`
   loop or the stats retry. Its task is deleted while `BATCHprocessing=true`. The next article for
   that group creates a fresh task with `BATCHprocessing=false`, so a **second
   `processNewsgroupBatch` for the same group** can start while the first is still running. SQLite
   serializes the writes, and the thread-cache read-modify-write runs in a retried transaction, but
   `MemThreadCache` updates can then be applied out of order. (PLAUSIBLE: needs a batch slower than
   120 s plus new traffic for the group.)
2. Pointer identity breaks. `GroupDB.NewsgroupPtr`, `article.NewsgroupsPtr` and `ArticleNums`
   keys all use the old `*string`, while new lookups return a new one. That causes A-db-2, and it
   makes PHASE 3's `NewsgroupsPtr` removal (`:895-903`) miss, so the article is never cleared (A-db-4).

**Evidence:** `TestZZAuditDBExpireCacheAndArticleCache`. A task with `BATCHprocessing=true` and
`Expires` in the past was swept by the real `ExpireCache`:
`busy task: old=0x213e5d28aa00 processing=true -> new=0x213e5d2289b0 processing=false`.

**Fix (S):** skip tasks with `BATCHprocessing` or a non-nil channel holding work, renew `Expires` in
`GetNewsgroupPointer`/`GetOrCreateTasksMapKey`, and keep the newsgroup `*string` in a separate
interning map that never expires (or compare by string value).

---

### A-db-6: migratedDBsCache makes a re-created group DB skip its schema

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-6 | medium | robustness | `db_migrate.go:18-36,377-426` (`:380`, `:387-389`, `:421-425`) | CONFIRMED |

**Scenario:** the cache key is `"<group>:group"`, which records neither the data dir nor the file. It
is set on the second open of a fully migrated DB and never cleared. If the file is removed or replaced
while the process runs (`RsyncDIR --remove-source-files`, recover-db moving `.new` files into place,
an operator deleting a broken group DB), the next `GetGroupDB` creates an empty file and skips all
migrations. The group then fails every query with `no such table: articles` until the process is
restarted. A second `Database` with a different data root in the same process (tests, OPS-1) hits
the same path.

**Evidence:** `TestZZAuditDBMigratedCache`:
```
ZZ migcache other data dir GetGroupDB err=<nil>
ZZ migcache other data dir SELECT articles: no such table: articles
ZZ migcache file removed, reopen err=<nil>
ZZ migcache file removed, SELECT articles: no such table: articles
```

**Fix (S):** key the cache by the absolute file path, and drop the entry in `initGroupDB` when the
file did not exist before `sql.Open` (or stop caching: `getAppliedMigrations` is one indexed query).

---

### A-db-7: HierarchyCache.UpdateNewsgroupStats mixes units

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-7 | medium | bug | `hierarchy_cache.go:262-304` (`:269`, `:270-272`, `:293-297`); caller `db_batch.go:959-961` | CONFIRMED |

**Scenario:** after each batch, `h.GroupCount += messageCountIncrement` adds the **article** count of
the batch to the hierarchy's **group** count, which the hierarchies page shows and sorts by. Then
`if newLastArticle > h.LastUpdated.Unix() { h.LastUpdated = time.Unix(newLastArticle, 0) }` compares
an article number with a Unix time. A loaded hierarchy (2026 timestamp) is never updated, and one with
a zero `LastUpdated` jumps to 1970. The "activity" order of direct groups sorts by `LastArticle`, an
article number, not by date (`:293-294`).

**Evidence:** `TestZZAuditDBHierarchyCache` (`GroupCount 3`, `UpdateNewsgroupStats(.., 1000, 42)`):
`zz GroupCount=1003 LastUpdated=2026-10-01 00:00:00 +0000 UTC | yy GroupCount=1003 LastUpdated=1970-01-01 00:00:42 +0000 UTC`.

**Fix (S):** don't touch `GroupCount` there (it changes only when groups are added or removed). Pass
the batch's latest date (A-db-4) and update `LastUpdated`/`UpdatedAt` from it. Sort "activity" by
`UpdatedAt`.

---

### A-db-8: HierarchyCache invalidation empties listings until the next full warm-up

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-8 | medium | bug | `hierarchy_cache.go:155-203,205-233,309-352`; callers `queries.go:131,273`, `BulkDeleteNewsgroups`, `DeleteNewsgroup` | CONFIRMED |

**Scenario:** `InvalidateHierarchy` deletes the cache entries for a prefix. The readers
`GetDirectGroupsAtLevel`/`GetHierarchySubLevels` treat a missing entry as "no data" and return an
empty result ("cache should be warmed on startup") with no fallback to the DB. Every
`InsertNewsgroup`, `UpdateNewsgroup` and delete therefore blanks that hierarchy's pages until
`WarmCache` runs again, which happens only at startup or from the admin "update hierarchies" action.
The 5-minute throttle means some invalidations are dropped while others blank the page. Only the
top-level prefix and its parents are invalidated, so stale sub-level entries survive.

**Evidence:** `TestZZAuditDBHierarchyCache`:
`before invalidate: 2 groups (count 2)` → `after InvalidateHierarchy: 0 groups (count 0)`.

**Fix (S):** fall back to the direct query (`getDirectGroupsAtLevelDirect` /
`getHierarchySubLevelsDirect`) on a cache miss and store the result, or rebuild the affected prefix
right away instead of deleting it.

---

### A-db-9: the batch queue counter goes negative; MaxQueued back-pressure never triggers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-9 | low | robustness | `db_batch.go:1758-1812` (`:1787-1810`), `:764-768` | CONFIRMED |

**Scenario:** `BatchDivider` adds to `sq.queued` only in steps of `maxDBbatch` (`:1803-1810`).
`processNewsgroupBatch` subtracts every batch it processes (`:766`). With traffic below 1000
articles between steps, `queued` drifts negative, `realQueue >= maxQueue` (`:1787`) never holds,
and the documented global `MaxQueued` limit does nothing. Only the per-group channel capacity
(`maxDBbatch`) still blocks. Note also that the single divider goroutine blocks on one full group
channel (`:1801`) and so stalls all groups while that group's batch waits (for example on
`errGroupDBInitTimeout`, which is retried without limit while the DB is live).

**Evidence:** `TestZZAuditDBQueuedCounterAndRealPath` sent 5 articles through
`BatchCaptureOverviewForLater` → global `BatchDividerChan` → orchestrator:
`queued before=0 after=-5 (5 articles enqueued and processed)`.

**Fix (S):** increment `sq.queued` per article (under `GMux`, or with an atomic) and compare against
the live value.

---

### A-db-10: HierarchyCache pagination overflows into a slice panic

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-10 | low | robustness | `hierarchy_cache.go:142-157,186-199,221-232` | CONFIRMED |

**Scenario:** `start := (page - 1) * pageSize` with no bound. The web handlers accept any `page > 1`
(`web/web_hierarchiesPage.go:27-31`, `:67-71`). A page that makes the product wrap negative
(`4611686018427387905` with page size 10) slices with a negative index and panics. gin Recovery
answers 500. This is the same class as LO:F15, in a file LO did not touch.

**Evidence:** `TestZZAuditDBHierarchyCache`:
`huge page recover=runtime error: slice bounds out of range [:-9223372036854775798]`.

**Fix (S):** clamp `page` to `ceil(len/pageSize)` before multiplying (one helper for the three readers).

---

### A-db-11: hierarchy listings silently drop rows they cannot scan

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-11 | low | robustness | `hierarchy_cache.go:476-495` (`:490-493`), `queries.go:2883-2892`, `:2229-2234` | CONFIRMED (code trace) |

**Scenario:** `buildHierarchyTreeBulk`, `GetNewsgroupsByHierarchy` and `getHierarchiesPaginatedDirect`
`continue` on a scan error, and none of them checks `rows.Err()`. For a sub-hierarchy listing
(`LIKE` path, `queries.go:2854-2858`), a NULL-hierarchy group (A-db-1) is counted in `totalCount` but
missing from the page, and it is dropped from the warmed tree without any log line. This is the
opposite failure mode from A-db-1's listings, which abort completely.

**Fix (S):** scan `hierarchy` as `sql.NullString`, log the first scan error per call, return
`rows.Err()`.

---

### A-db-12: GHmap.GetGroupFromHash dereferences nil for an unknown hash

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-12 | low | dead-code | `groups_hashmap.go:141-151` (`:150`) | CONFIRMED |

**Scenario/Evidence:** `return entry.Group, exists` runs with `entry == nil` when the hash is unknown.
`TestZZAuditDBMisc`: `GetGroupFromHash(unknown) recover=runtime error: invalid memory address or nil pointer dereference`.
The function has no caller (only a comment at `:19`), so this is latent.

**Fix (S):** return `"", false` when the hash is missing, or delete the function and the `Groups`
map it alone reads.

---

### A-db-13: GetActiveNewsgroups and its siblings abort on one NULL and skip the retry helper

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-13 | low | robustness | `queries.go:1028-1066,1074-1101,1109-1137`; `migrations/0001_main_schema.sql:34-35` | PLAUSIBLE |

**Scenario:** `GetActiveNewsgroups` is what `LIST ACTIVE`/`LIST NEWSGROUPS` (`nntp-cmd-list.go:28,48`)
and the web home, stats and API pages read. NULL hierarchy (A-db-1) does not affect it, because it
does not select that column. But it scans `last_article` and `message_count` into `int64`, and both
columns are nullable without a default. One NULL would fail the whole listing for every client. The
same holds for `GetActiveNewsgroupsWithMessages` and the two paginated variants. All four use bare
`db.mainDB.Query` instead of `RetryableQuery` and don't check `rows.Err()`.
Missing step: none of the current writers leaves those two columns NULL (`InsertNewsgroup`, the
batch upsert and `UpsertNewsgroupDescription` all set them), so a NULL row would have to come from
legacy data or a manual edit.

**Fix (S):** `COALESCE(last_article, 0), COALESCE(message_count, 0)` in the selects, use
`RetryableQuery`, and return `rows.Err()`.

---

### A-db-14: HierarchyCache hands out shared slices that UpdateNewsgroupStats sorts in place

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-db-14 | low | robustness | `hierarchy_cache.go:220-232` vs `:276-301` | PLAUSIBLE |

**Scenario:** `GetDirectGroupsAtLevel` returns `groups[start:end]`, a subslice of the cached slice of
shared `*models.Newsgroup`, after releasing the read lock. `UpdateNewsgroupStats` (after every batch
in a process that has both the cache and a batch writer: web with posting, nntp-server) mutates
`MessageCount`/`LastArticle` of those structs and re-sorts the same backing array under the write
lock, while a handler may still be iterating it. That is a data race, and a page can show
duplicated or missing groups.
Missing step: not run under `-race` with concurrent readers in this audit.

**Fix (S):** return copies (as `GetHierarchiesPaginated` already does), or build new sorted slices
in `UpdateNewsgroupStats` and swap them in.

## SQL construction table

Every place in the scope files where SQL text is built at run time.

| location | construction | inputs that reach the text | verdict |
|----|----|----|----|
| `db_batch.go:1057` | `query_processOverviewBatch2 + getPlaceholders(n) + ")"` | count only, n ≤ `maxDBbatch` (≤ 1000) | safe |
| `db_batch.go:1353` | `Sprintf(query_batchUpdateReplyCounts2, Repeat(CASE-arm, n), placeholders)` | count only, values bound (`*string` args are dereferenced by `database/sql`) | safe; the two templates are `var`, not `const` (`:1319-1320`) |
| `db_init.go:365-367` | `Sprintf("PRAGMA synchronous = %s" / cache_size %d / temp_store %s)` | `DBConfig.SyncMode/CacheSize/TempStore` (code and tool flags) | trusted, not validated: an empty or invalid `SyncMode` fails every main-DB connection in the ConnectHook |
| `db_init.go:395-399` | `Sprintf("PRAGMA ... = %v")` | package vars `SQLITE_*` | trusted; built once per process (Global state table) |
| `db_migrate.go:252` | `"PRAGMA foreign_keys=" + foreignKeys` | `SQLITE_foreign_keys` or `"ON"` | trusted |
| `db_migrate.go:263` | `tx.ExecContext(ctx, stripMigrationPragmas(content))` | embedded migration files | trusted (embedded); multi-statement |
| `db_rescan.go:462` | `Sprintf(query_RebuildThreadsFromScratch3, table)` | fixed list `tree_stats, cached_trees, thread_cache, threads` | safe |
| `thread_cache.go:179-190` | `threadChildrenQuery(placeholders)` | count only (≤ page size) | safe |
| `queries.go:356-359` | `UPDATE newsgroups SET active = ? WHERE name IN (%s)` | placeholder count = len(names) | safe; more than 32766 names fails with "too many SQL variables" (admin bulk form) |
| `queries.go:393-395,425-432` | bulk delete `IN (%s)` ×3 | placeholder count | safe; same bound (FU:E13 covers the transaction) |
| `queries.go:1362-1374` | `Sprintf(queryTemplate, orderBy)` | `orderBy` from a fixed switch on `sortBy` | safe (whitelist) |
| `queries.go:2097-2114` | `Sprintf(... ORDER BY %s ...)` | fixed `name ASC` / `updated_at DESC` | safe |
| `queries.go:2200-2215` | `"... ORDER BY " + orderBy + ...` | whitelist switch | safe |
| `queries.go:2840-2871` | `whereClause` + `orderBy` concatenated | both from fixed strings; the hierarchy value is bound | safe |
| `queries.go:3496-3505` | `selectPart + " WHERE message_id IN (" + placeholders + ")"` | placeholder count = len(wantedIDs) from nntp-transfer jobs | safe; bound as above |
| `queries.go:3560-3583`, `:3660-3683`, `:3725-3741` | `" WHERE " + Join(conditions)` | fixed condition strings with `?` | safe |
| `queries.go:291,307` | `LIKE ?` with `name + "%"` | bound, but `%`/`_` in the prefix are not escaped | functions unused (inventory) |
| `queries.go:1982,1994,2040,2048,2089,2114,2853` | `LIKE ?` with `prefix + ".%"` | bound; web hierarchy names may contain `%`/`_` | info: a `%` in the URL widens the listing to public data only; `escapeLike` (`queries.go:1705`) exists but is used only by search |

No SQL injection was found. Every value that reaches SQL text is a placeholder count, a whitelisted
`ORDER BY`, a fixed table name or a trusted configuration value.

## Global state table

| global | location | lifetime / writer | effect on re-open and shutdown |
|----|----|----|----|
| `INIT` | `db_init.go:117` | set at `:148` before the open can fail; never reset | blocks every later `OpenDatabase`, even after a failed one (OPS-1, repro) |
| `GlobalDBMutex` | `db_init.go:116` | held during `OpenDatabase` | none |
| `GroupHashMap` | `db_init.go:19,185` | replaced on each open; `NewGHmap` starts `GHmapCron`, which re-spawns itself every 15 min | the old cron is never stopped (repro list) |
| `BatchDividerChan` | `db_batch.go:1753` | package channel; one `BatchDivider` goroutine per `StartOrch` (`:1573`), no exit | not stopped by shutdown (repro); with two DBs both dividers read the same channel (OPS-1) |
| `migratedDBsCache` | `db_migrate.go:25` | keyed by group name, never cleared | skips migrations for a re-created file or another data root (A-db-6, repro) |
| `migrationCache`, `embeddedMigrationCache` | `db_migrate.go:20`, `embedded_migrations.go:19` | immutable after first load | none; a zero `EmbeddedMigrationsFS` makes `getEmbeddedMigrationFiles` and `getMigrationFiles` recurse into each other (inventory) |
| `mainConnPragmas`, `groupConnPragmas`, `PragmasGroupDB` | `db_init.go:332-334,379` | group list built once per process from the first DB's `WALMode` and `SQLITE_*` | a second DB with other settings silently inherits the first one's group pragmas |
| `ENABLE_ARTICLE_CACHE`, `NO_CACHE_BOOT`, `NNTP_AUTH_CACHE_TIME`, `SQLITE_*`, `BatchInterval`, `DBidleTimeOut`, `MemCacheThreadsExpiry`, `DefaultGroupHashExpiry`, `DefaultCronGroupHash` | `db_init.go:21-32`, `db_batch.go:18`, `database.go:15`, `thread_cache.go:61`, `groups_hashmap.go:9-10` | tuning knobs read at open or by background loops | must not change after open (WSH K3); `BatchInterval` is copied at `NewBatchOrchestrator` |
| `NewsgroupDBsIDcache` | `db_groupdbs.go:51` | id → name, never invalidated | safe in practice (`newsgroups.id` is AUTOINCREMENT, no rename API); survives re-open |
| `sqliteMaxRetryWait`, `queryID` | `sqlite_retry.go:27,222` | atomic | none (LO:C3 fixed) |
| `placeholderCache` | `db_batch.go:21` | `sync.Map`, only the `maxDBbatch` count | none |
| `shutdownLogLimiter` | `db_batch.go:257` | per-key timestamps | none |
| `query_batchUpdateReplyCounts1/2` | `db_batch.go:1319-1320` | `var` SQL templates | nothing writes them; should be `const` |
| `empty` | `queries.go:2068` | shared nil slice returned to callers | harmless (nil) |
| background goroutines with no stop path | `NNTPAuthCache.cleanupExpired` (`nntp_auth_cache.go:37,154`), `SQ3batch.ExpireCache` (`db_batch.go:196`, re-spawns itself), `GHmap.GHmapCron`, `MemCachedThreads.CleanCron` (`thread_cache.go:83`), `GroupSectionDBCache.CronClean`, `BatchDivider` | started by `OpenDatabase` | all still running after `StopChan` + `WG.Wait` + `Shutdown` (repro); `cronDBEvery` exits on its next 10 s tick; `HierarchyCache.WarmCache` is one-shot but unsynchronized with shutdown |

## Function inventory

| function/file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `users.go` |  x |  |  |  |  | file body is `/// nothing here` |
| `GHmap.GetGroupFromHash` (`groups_hashmap.go:141`) |  | x | x |  | x | nil deref (A-db-12) |
| `HierarchyCache.isDirectChild` |  | x | x |  |  | staticcheck U1000 |
| `SQ3batch.getOrCreateStringSlice` / `returnStringSlice`, `BatchOrchestrator.mux` |  | x | x |  |  | staticcheck U1000 |
| `query_SearchNewsgroups`, `query_SearchNewsgroupsAdmin`, `query_CountSearchNewsgroups` |  | x | x |  |  | staticcheck U1000 |
| `GroupDB.IncrementWorkers` (`db_groupdbs.go:314`) |  | x | x |  |  | no caller; the unlocked pattern WSH:H6 replaced with `acquire` |
| `GetAnyNewsgroupDBfromIDs`, `GetNewsgroupsDBbyID` |  | x | x |  |  | no caller |
| `GetLatestArticleNumberFromOverview` (`db_rescan.go:14`) |  | x | x |  | x | no caller; `MAX(article_num)` on an empty group fails the int64 scan (`TestZZAuditDBMisc`) |
| `GetTotalThreadsCount` (`queries.go:1529`) |  | x | x |  | x | no caller; opens or creates one group DB per newsgroups row |
| `UpsertNewsgroupDescription` (`queries.go:1022`) |  | x | x |  | x | no caller; would insert `active=0` rows with NULL hierarchy (A-db-1) via bare `Exec` |
| `UpdateNewsgroupExpiryPrefix`, `UpdateNewsgroupMaxArticlesPrefix` |  | x | x |  | x | no caller; `LIKE name%` without escaping |
| `GetNewsgroupsByPrefix`, `GetNewsgroupsByExactPrefix` |  | x | x |  | x | no caller; `active = 1 AND name = ? OR (...)` precedence includes inactive groups (`queries.go:1989`) |
| `InsertOverview` / `query_ImportOverview` (`queries.go:668-693`) |  | x |  |  | x | only caller `Processor.ImportOverview`, which itself has no caller; rows it writes have NULL `headers_json`/`body_text`, which `GetArticleByNum` cannot scan |
| `ExistsMsgIdInArticlesDB` (`db_groupdbs.go:336`) |  |  |  | x |  | returns `false` on any error (A-db-3) |
| `ArticleCache.removeElement` / `totalSize` |  |  |  | x | x | key derived through `GetNewsgroupPointer` (A-db-2); size adds body length, subtracts `Bytes` |
| `HierarchyCache.UpdateNewsgroupStats` |  |  |  |  | x | A-db-7 |
| `getMigrationFiles` ↔ `getEmbeddedMigrationFiles` |  | x |  |  | x | the filesystem fallback (`./migrations` relative to the cwd) is unreachable while the embed is set (`SetEmbeddedMigrations` is only called with the package's own FS, `cmd/web/main.go:94`); with a zero FS the two recurse until the stack overflows; `sort.Slice` is unstable for equal versions (`0002_main_*`, `0007_main_*`) |
| `DeleteAIModel` (`db_aimodels.go:180`) |  |  |  | x |  | comment promises "if it's not the last active one", but there is no check |
| `CreateAIModel` / `UpdateAIModel` with `isDefault=true` |  |  |  |  | x | other defaults are not cleared (unlike `SetDefaultAIModel`) |
| `GetAllCronJobs` (`db_cron_jobs.go:15`) |  |  |  |  | x | no `rows.Err()` |
| `ConfigCache.getConfigValueDirect` |  |  |  | x |  | compares `err.Error()` with the `ErrNoRows` text instead of `errors.Is` |

## Seed items rejected

None. Corrections to the seed text:
- **INT-6**: the seed limits the miss to "the first batch of an auto-created group". With A-db-1 it
  affects every batch of such a group until an admin runs "update hierarchies".
- **BUG-2**: the clamp limits the article-number range, not the row count, so a sparse range can
  return no rows at all (see BUG-2).
- **OPS-1**: the "batch retries forever after `Shutdown`" part is fixed (verified). The fatal sites,
  `INIT`, the global channel, the never-stopping goroutines and `migratedDBsCache` are open.
- **SEC-2** intake side: the fetcher is **not** a trigger. It runs in bulk mode and stores only into
  the fetched group (`proc_DLArt.go:201`, `threading.go:101-125`). The trigger is non-bulk intake
  (IHAVE/TAKETHIS/POST).

## Not covered / follow-ups

- `db_rescan.go` (used only by `recover-db`) was read only at its entry points; its batch rebuild,
  `findMissingArticles` and `findOrphanedThreads` loops were not audited line by line.
  `thread_cache.go` was not audited again beyond the parts FU did not test.
- A-db-2 was not measured as RSS in a running `nntp-server`/`web`; the repro shows the map/list
  divergence directly.
- A-db-14 was not run under `-race` with concurrent readers.
- The `expire-news` side of INT-5 (deleting `thread_cache`/`cached_trees`/`tree_stats`/spam rows,
  keeping `low_water`/`high_water`) belongs to the tools slice. The intake side of SEC-2 and INT-6's
  `CaseDupes` handling belongs to `audit-intake`.
- Not in scope and not read: `db_sessions.go`, `db_apitokens.go`, `db_sections*.go`,
  `sanitize_cache.go`, `sections_cache.go`, `tree_*.go` (audit-web), `db_post_queue.go`,
  `db_spam_flags.go`, `db_threads_paged.go`, `db_user_profile.go`, `db_web_posting.go`, `progress.go`,
  `ui_cache.go`.
- Fix plans (wave 4): A-db-1, INT-6 and SEC-2's batch upsert belong together, because they share
  `query_updateNewsgroupsStats` and the PHASE 3 order. A-db-2 and A-db-5 share the `TasksMap` pointer
  lifetime. INT-5 needs a group-DB migration (AUTOINCREMENT rebuild) or explicit numbering.
