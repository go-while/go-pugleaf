# Audit audit-intake (base 5cefa96)

Article validation, injection, history and header rebuilding against RFC 5536/5537. Base: `plan-nntp-audit-tests` @
5cefa96. None of the scope files, `internal/nntp/nntp-cmd-posting.go`, `internal/nntp/nntp-client-commands.go` or
`internal/database/db_batch.go` changed between 84c2081 and 5cefa96 (`git diff --stat 84c2081 5cefa96` on them is
empty), so the line numbers of the seed table still hold. All `path:line` anchors below are at 5cefa96; paths are under
`internal/` unless they start with `cmd/`.

## Scope and method

- **Files read in full:**
  - `processor/{processor.go, threading.go, proc-utils.go, proc_DLArt.go, proc_DLXHDR.go, proc_ImportOV.go,
    proc_reuse.go, PostQueue.go, bridges.go, counter.go, interface.go, proc_MsgIDtmpCache.go, proc_MsgIdItemCache.go,
    unified_cache.go}`
  - `common/{common.go, headers.go}`
  - `history/{history.go, history_config.go, history_MsgIdItemCache.go, "history sqlite3_sharded.go",
    "history sqlite3 normal.go", history_sqlite3_Pool.go, utils.go}` (the two quoted names contain spaces)
  - `postmgr/postmgr.go`, `preloader/preloader.go`, `fediverse/{bridge.go, activitypub.go}`,
    `matrix/{bridge.go, client.go}`, `config/config.go`, `database/db_post_queue.go`, `cmd/post-queue/main.go`
  - `spam/`: inventory only (README, `config.example.yaml`, four `*.go.off` files)
- **Callees read:** `nntp/nntp-cmd-posting.go` (POST/IHAVE/TAKETHIS, `readArticleLines`, `parseIncomingArticleLines`),
  `nntp/nntp-client-commands.go:820-950` (`readMultilineResponse`, `ParseLegacyArticleLines`, `ParseHeaders`) and
  `:1100-1290` (TAKETHIS/POST senders), `database/db_batch.go:150-215,553-563,731-1100` (capture, stats upsert,
  `processNewsgroupBatch`, `processOverviewBatch`), `database/queries.go:205-250,1028-1046,2285-2300`,
  `database/db_groupdbs.go:117-247`, `database/utils.go:112-127`, `web/web_sitePostPage.go:300-410`,
  `cmd/web/main.go:130-150`, `cmd/web/main_functions.go:647-800`, `migrations/0001_*.sql`.
- **RFCs fetched from rfc-editor.org (2026-10-11):** RFC 5536, 5537; RFC 3977 and RFC 4644 for the reply-code and
  `:bytes` quotes. Quotes below are from those texts.
- **Tools:** `docs/audit/tools/*.txt` filtered to the scope. Hits: unused `parseReferencesToContinuedLineString`,
  `multiLineStringToSlice` (`common/headers.go:146,626`), `countLines` (`processor/proc-utils.go:575`),
  `containsAtAndDot` (`processor/threading.go:43`); SA4006 `common/headers.go:534` (dead `validNewsgroups = nil`);
  errcheck on unchecked `tx.Rollback`/`Close` in `history/history.go:496` and `"history sqlite3_sharded.go":79,127,135,210`;
  gosec G401/G501 (md5 for message-id routing in `history/history_config.go:253`, not a security use) and G301
  (`MkdirAll 0755`, `:241`). govulncheck's `fediverse`/`matrix` traces (`SendActivity`, `CreateRoom`) are reachable
  only through code that is dead today (see the inventory).
- **Repro:** a standalone program `./data-test-audit-intake/_repro/intake/main.go` (gitignored, not committed). It opens
  a real `database.OpenDatabase` on `./data-test-audit-intake/run` (`BatchInterval=100ms`,
  `ENABLE_ARTICLE_CACHE=false`, `NO_CACHE_BOOT=true`), builds the processor as the plan's e2e TestMain does
  (`processor.LocalNNTPHostname="news.test.invalid"`, `history.ENABLE_HISTORY=true`, `history.NewHistory` on
  `run/history`, `history.NewMsgIdItemCache()`, `&processor.Processor{DB: db, History: hist}`,
  `db.Batch.SetProcessor(proc)`), and serves `nntp.NewNNTPServer` with a 3-method adapter (same as
  `cmd/nntp-server/processor_adapter.go`) on `127.0.0.1:21204`. It drives POST/IHAVE/TAKETHIS/HEAD/XOVER/GROUP/LIST
  with `net/textproto`, reads the group DBs and the main DB directly, and calls `common.ReconstructHeaders` and
  `processor.(*Processor).NewPostQueueWorker` in-process. Run with `go run ./data-test-audit-intake/_repro/intake/main.go`
  (about 20 s); it stops the server, closes `StopChan`, runs `proc.Close()`, waits `db.WG` and calls `db.Shutdown()`.
  Port 21204 was free afterwards (`ss -ltn`). Trimmed output in [Appendix A](#appendix-a-repro-transcript). Nothing
  touched `./data`, `data_ram` or `data_local`.

## Summary

| Severity | Count | Ids |
|----|----|----|
| critical | 0 | |
| high | 4 | SEC-2 (intake half), SEC-3 (intake side), A-intake-1, A-intake-2 |
| medium | 13 | INT-1, INT-2, INT-3, INT-4, INT-6, CLI-1, OPS-1, A-intake-3, A-intake-4, A-intake-5, A-intake-6, A-intake-8, A-intake-9 |
| low | 4 | INT-5, A-intake-7, A-intake-10, A-intake-11 |
| info | 0 | |

SEC-2 and SEC-3 are the intake halves of seeds whose server halves `audit-nntp-sec` already rated high; count each
seed once in the consolidated report. A-intake-2 is the intake root cause of A-nntp-rfc-2 (high); merge the two there.

| Category | Count |
|----|----|
| security | 3 (SEC-2, SEC-3, A-intake-2) |
| rfc | 8 (INT-1, INT-2, INT-3, INT-5, A-intake-3, A-intake-4, A-intake-6, A-intake-7) |
| bug | 6 (INT-6, CLI-1, A-intake-1, A-intake-5, A-intake-8, A-intake-9) |
| missing | 1 (INT-4) |
| robustness | 3 (OPS-1, A-intake-10, A-intake-11) |

Every seed in scope (INT-1…6, CLI-1 `common/headers.go` part, OPS-1 processor/history) is **CONFIRMED**; INT-6 and
CLI-1 are worse than the seed text. One sub-claim of INT-1 is rejected (see `## Seed items rejected`).

**The 5 most important findings:**
1. **A-intake-1 (high):** an article to an unknown group auto-creates a `newsgroups` row with `hierarchy` NULL. From then
   on `MainDBGetNewsgroup` and `MainDBGetAllNewsgroups` fail with a scan error: history is never written for that group
   (every batch, not just the first), `GROUP` answers 411 while `LIST ACTIVE` lists the group, and the fetcher (`$all`),
   `history-rebuild`, `expire-news`, `recover-db`, `nntp-transfer`, `nntp-analyze` and the preloader repair abort.
   One POST by any posting user, or one TAKETHIS by a peer, is enough.
2. **SEC-2, intake half (high):** `processArticle` accepts any name that passes a lax regex and creates the group DB
   before the reply. One POST created 7 group DBs and 7 `active=1` rows, including the reserved `control`, `junk`,
   `example.foo`, the non-conforming `AI.TEST.UPPER` and `ai..dots.`, and `c++` (RFC 5537 §3.7: "MUST NOT create new
   newsgroups").
3. **A-intake-2 (high, root of A-nntp-rfc-2):** the header block is never validated. A posted header line `..` is stored
   as `.`, colon-less lines are stored, and repeated Subject/From are stored LF-joined in the overview columns. Repeated
   Newsgroups headers are unioned, so the article lands in both groups. The same parser handles fetched articles.
4. **INT-2 (medium):** an article already stored but unknown to history is answered 235/239 instead of 435/437/439. An
   in-flight id is answered 437/439 (permanent) instead of 436/400. Together with INT-6, an article already stored was
   accepted a second time into another group after the 10-minute cache eviction
   (eviction simulated with `MsgIdCache.Delete`, which is what `CleanExpiredEntries` does after `CachedEntryExpires + StuckEntryMaxAge`, `history_MsgIdItemCache.go:194-200`).
5. **A-intake-9 (medium):** when no group DB can be opened (for example a 303-character group name that passes
   `validGroupNameRegexLazy`), `processArticle` still returns `CasePass`: POST answers `240 Article posted successfully`,
   and nothing is stored anywhere.

## Findings

### A-intake-1: auto-created groups get `hierarchy` NULL, which breaks every `MainDBGetNewsgroup`/`MainDBGetAllNewsgroups` caller

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new (extends INT-6, SEC-2) | A-intake-1 | high | bug | `database/db_batch.go:553-563,861-869`, `database/queries.go:190-230`, `processor/threading.go:72-99,232`, `history/history_MsgIdItemCache.go:25-27,194-200` | CONFIRMED |

- **Scenario:** a posting user (POST) or a peer (IHAVE/TAKETHIS) sends one article whose Newsgroups names a group that
  is not in `newsgroups`. Then:
  - the batch writer upserts the row with `query_updateNewsgroupsStats`, which sets only `name, message_count,
    last_article, updated_at`, so `hierarchy` stays NULL (`active` defaults to 1);
  - `MainDBGetNewsgroup` scans `hierarchy` into a Go `string` and fails;
  - the history adds are skipped for every later batch of that group, not only the first one (INT-6 says the first);
  - each such article's `MsgIdItem` stays `CaseLock`, because only the history branch sets `CaseDupes`
    (`db_batch.go:875-891`). `CheckMessageID` answers `CaseRetry` (IHAVE 436) until the entry is evicted
    `TmpCacheTTL + StuckEntryMaxAge` = 15 s + 10 min later. After that the id is "new" again (INT-2);
  - `handleGroup` (`nntp/nntp-cmd-group.go:20`) gets the scan error → `411 No such newsgroup`. `LIST ACTIVE`
    (`GetActiveNewsgroups`, `queries.go:1028`, which doesn't select `hierarchy`) lists the group;
  - `MainDBGetAllNewsgroups` (`queries.go:190-207`) fails for the whole table. Callers that abort on that error:
    `cmd/nntp-fetcher/main.go:194-198` (`$all`/wildcard: `return`), `cmd/history-rebuild/main.go:229-233`,
    `cmd/expire-news/main.go:355,372`, `cmd/recover-db/main.go:109,119`, `cmd/nntp-transfer/main.go:789`,
    `cmd/nntp-analyze/main.go:292`, `cmd/fix-references/main.go:109`, `cmd/fix-thread-activity/main.go:39`,
    `preloader/preloader.go:268` (`RepairNewsgroupWatermarks`), `cmd/web/main_functions.go:391,1059`. `recover-db`,
    `fix-references` and `fix-thread-activity` `log.Fatalf` on it; the others return the error. `expire-news` fails on its
    all-groups and wildcard paths.
  - further `MainDBGetNewsgroup` callers that break on the row: the admin UI cannot repair the group
    (`adminToggleNewsgroup`, `adminMigrateNewsgroupActivity`, `adminFixThreadActivity`, `adminHideFuturePosts` answer
    "Newsgroup not found", `web/web_admin_newsgroups.go:401,521,580,746`; `adminCreateNewsgroup` `:94` passes its exists
    check and then hits the UNIQUE constraint); `cmd/nntp-fetcher/main.go:359,636`; `cmd/expire-news/main.go:195`;
    `processor/proc_DLArt.go:114` (crosspost reuse is turned off for the group).
  - The only code that fills NULL hierarchies is `UpdateHierarchyCounts` (`queries.go:2286-2291`), reached only from
    the admin button `adminUpdateHierarchies` (`web/web_hierarchiesPage.go:117`).
- **Evidence:** repro, section "SEC-2 (intake)":
  ```
  POST to unknown ai.auto.new1: 240 Article posted successfully
    newsgroups row: no row (sql: Scan error on column index 12, name "hierarchy": converting NULL to string is unsupported)
    history.Exists(<sec2-new1@audit.invalid>) after 3s: false
    CheckMessageID now: 0x2C (CaseRetry)
  POST 2nd article to ai.auto.new1: 240 ...
    history.Exists(<sec2-new2@audit.invalid>): false
    GROUP ai.auto.new1: 411 No such newsgroup
    LIST ACTIVE line: "ai.auto.new1 2 1 n"
    MainDBGetAllNewsgroups() err=sql: Scan error on column index 12, name "hierarchy": converting NULL to string is unsupported
  ```
  `server.log` shows both causes for the same group: the first batch `... skipping history adds for 1 articles: sql: no
  rows in result set` (INT-6 as seeded), the second batch `... Scan error on column index 12, name "hierarchy"`.
  `sqlite3 run/cfg/pugleaf.sq3 "SELECT name,active,status,hierarchy FROM newsgroups WHERE hierarchy IS NULL"` lists all
  8 auto-created rows with `active=1`, `status='y'`.
- **Fix:** (1) SEC-2's fix (no auto-create on intake) removes the trigger. (2) Independently, the upsert must not create
  rows: use `UPDATE ... WHERE name=?` in `processNewsgroupBatch`, or insert `hierarchy` and `status` too. (3) Scan
  nullable columns through `sql.NullString`/`COALESCE(hierarchy,'')` in every `newsgroups` scan (the DB side belongs to
  `audit-db`, "NULL scans"). (4) Set `CaseDupes` (or delete the cache item) when the history add is skipped, so the id
  does not stay `CaseLock` for 10 minutes. Effort M.

### SEC-2 (intake half): unknown Newsgroups create group DBs and `active=1` rows

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-2 | SEC-2 | high | security | `processor/threading.go:125-137,208-243`, `processor/proc-utils.go:414-454`, `common/headers.go:26-30,539-578`, `database/db_groupdbs.go:117-247`, `database/db_batch.go:553-563,940-950` | CONFIRMED (intake half; the NNTP half is in `audit-nntp-sec`) |

- **Scenario:** `extractGroupsFromHeaders` keeps every token that passes `IsValidGroupName` (no lookup in
  `newsgroups`). `processArticle` then calls `proc.DB.GetGroupDB(newsgroup)` (`threading.go:232`), which creates
  `<DataDir>/db/<md5>/<sanitized>.db` with schema before the reply. The batch writer's upsert creates the
  `newsgroups` row with `active=1`. Only an authenticated posting user or peer can do this, but any of them can, and the
  web posting path checks existence (`web/web_sitePostPage.go:287-308`) while NNTP does not.
- **Evidence:** repro, section "INT-3":
  ```
  POST Newsgroups "ai.x:AI.TEST.UPPER;example.foo,control,junk,ai..dots.,c++": 240 Article posted successfully
    group DB files created by this POST: 7 (before=2)
  ```
  Files after the run: `ai_x.db AI_TEST_UPPER.db example_foo.db control.db junk.db ai_dots.db c.db` (each in its own
  `md5(name)` directory, so LO:A6's collision analysis still holds). Rows: see A-intake-1. `MaxCrossPosts=9`
  (`processor.go:40`) caps one article at 9 new groups; there is no cap across articles.
- **RFC:** RFC 5537 §3.7: "Serving agents MUST NOT create new newsgroups simply because an unrecognized
  <newsgroup-name> occurs in a Newsgroups header field." §3.5 step 4: an injecting agent "SHOULD reject any proto-article
  whose Newsgroups header field does not contain at least one <newsgroup-name> for a valid group".
- **Fix:** in `processArticle`, resolve each name with `MainDBGetNewsgroup` (or a cached active set) and drop unknown
  names. Reject when none is left: 441 for POST, 437 for IHAVE, 439 for TAKETHIS. For a peer feed, keep the current
  behavior only behind an explicit `-accept-unknown-groups` policy. `GetGroupDB`'s auto-create stays (Appendix B of the
  plan); the existence check belongs in the caller. Effort S.

### SEC-3 (intake side): no article size limit anywhere in the intake

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-3 | SEC-3 | high | security | `processor/threading.go:57-283`, `nntp/nntp-cmd-posting.go:221-260`, `config/config.go:131,574`, `models/models.go:58` | CONFIRMED (intake side; the server side is in `audit-nntp-sec`) |

- **Scenario:** `processArticle` checks presence of headers, a date and a group count, never a size. Neither
  `Server.NNTP.MaxArtSize` (default 32768, `config.go:574`, commented "128 KB") nor `newsgroups.max_art_size`
  (`models.Newsgroup.MaxArtSize`, scanned in `MainDBGetNewsgroup` but never compared) is read on any intake path
  (`grep -rn MaxArtSize` outside `config` and CRUD: no hit).
- **Evidence:** repro: `POST with one 4 MiB body line ... 240 Article posted successfully, stored=true`. The whole body
  is held in `bodyLines`, `article.BodyText` (joined) and `NNTPbody`, and then written to `articles.body_text`.
- **Fix:** enforce the server limit while reading (`audit-nntp-sec` SEC-3), and the per-group `max_art_size` in
  `processArticle` once the groups are resolved (SEC-2 fix), answering 441/437/439. Effort S.

### A-intake-2: the header block is never validated; repeated and malformed headers are stored verbatim (root of A-nntp-rfc-2)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new (root of A-nntp-rfc-2) | A-intake-2 | high | security | `nntp/nntp-client-commands.go:905-950`, `common/headers.go:580-601`, `nntp/nntp-cmd-posting.go:243-245,266-293`, `nntp/nntp-client-commands.go:117-123,837-839`, `processor/threading.go:108-161` | CONFIRMED |

- **Scenario:** `ParseHeaders` puts only six headers into `article.Headers`. It skips lines without a colon
  (`:929`), appends repeated headers (`:948`) and stores **all** raw header lines in `HeadersJSON` (`:950`), which the
  batch writes to `articles.headers_json`. `GetHeaderFirst` joins repeated values with `"\n"` (`headers.go:587`), so:
  - two `Subject:` → `subject` column `"first\nsecond"` (served by XOVER/XHDR: A-nntp-rfc-2 scenario B);
  - two `From:` → `from_header` `"a <…>\nb <…>"`;
  - two `Newsgroups:` → the value is split on `[,;:\s]+`, so the article goes to the union of both headers;
  - two `Date:` → `date_sent` takes the first Date (`ParseNNTPDate` falls through to `bruteForceDateParse`, which finds
    the first date's components), while `date_string` stores the LF-joined `"d1\nd2"`, which XOVER then serves (the
    same desync as Subject);
  - a header line `..` on the wire is un-stuffed to `.` (`nntp-cmd-posting.go:243-245`) and stored (A-nntp-rfc-2
    scenario A: HEAD/ARTICLE end the block there);
  - two `Message-ID:` → the first wins (`extractHeaderValue`, `:297-321`); the second stays in `headers_json`.
  `processArticle` checks only that Newsgroups/Subject/From are non-empty and the Date parses (`threading.go:108-161`).
- **Fetch path:** `GetArticle` reads with `readMultilineResponse` (`nntp-client-commands.go:117`), which also un-stuffs
  `..` to `.` (`:837-839`), and parses with the same `ParseLegacyArticleLines` (`:123`). So an upstream server can plant
  the same lines. This answers the open question in `audit-nntp-rfc` "Not covered".
- **Evidence:** repro, section "repeated / malformed headers":
  ```
  POST with 2x From/Newsgroups/Subject/Message-ID, a '.' line and a colon-less line: 240 Article posted successfully
    ai.test.a: subject="first\nsecond" from="a <a@example.invalid>\nb <b@example.invalid>"
    headers_json="Path: x!not-for-mail\nFrom: a <…>\nFrom: b <…>\nNewsgroups: ai.test.a\nNewsgroups: ai.test.b\n
                  Subject: first\nSubject: second\n.\nno colon line\nMessage-ID: <rep-1@…>\nMessage-ID: <rep-2@…>\nDate: …"
    also stored in ai.test.b (2nd Newsgroups header)=true; stored under 2nd Message-ID=false
  ```
- **RFC:** RFC 5536 §3.1: "MUST have exactly one of each of the following header fields: Date, From, Message-ID,
  Newsgroups, Path, and Subject." RFC 5537 §3.7 step 1: a serving agent "MUST reject any article that does not include
  all the mandatory header fields or any article that contains header fields that do not have valid contents."
- **Fix:** add a `validateHeaderBlock(headLines)` used by POST, IHAVE, TAKETHIS and the fetcher before
  `processArticle`. It rejects a line that is neither `name:value` (name = printable ASCII without `:`; RFC 5536 §2.2
  requires generating a space after the colon and allows accepting it without) nor a continuation, a line `.`, a NUL/CR/LF byte inside a line, and a second copy of any of
  the six mandatory headers (and of Injection-Date/Injection-Info/Xref/Control/Supersedes/Approved). Reply 441/437/439;
  the fetcher counts the article as an error. Effort M.

### INT-1: no injection: Message-ID/Date not generated, Path identity not served, no Injection-Info

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-1 | INT-1 | medium | rfc | `processor/threading.go:59-61,155-161,185-192`, `nntp/nntp-cmd-posting.go:42-46`, `nntp/nntp-cmd-helpers.go:20-23` | CONFIRMED (one sub-claim rejected) |

- **Scenario:**
  - POST without Message-ID: `extractHeaderValue` returns "" → `processArticle` returns `CaseError` ("article is nil",
    `:59-61`) → 441.
  - POST without Date: `ParseNNTPDate("")` → zero → `CaseError` (`:155-161`) → 441.
  - The Path is prefixed with `LocalNNTPHostname + "!"` only in `article.Path` (`:185-192`), which goes to the `path`
    column. `headers_json` keeps the original `Path:` line, and HEAD/ARTICLE serve `headers_json` verbatim
    (`parseArticleHeadersFull`, `nntp-cmd-helpers.go:20-23`). No `!.POSTED` diagnostic is added for POST, and no
    `!.SEEN.`/`!!` diagnostic for IHAVE/TAKETHIS. A Path that already starts with `news.test.invalid!` is not prefixed
    again (`:189`), so a peer can make the server appear to be in the path already.
  - No Injection-Info is added to POSTed articles.
- **Evidence:** repro, section "INT-1":
  ```
  POST without Message-ID: 441 Posting failed (processing error)
  POST without Date: 441 Posting failed (processing error)
  POST complete: 240 ...    stored path column: "news.test.invalid!poster.example!not-for-mail"
  HEAD <int1-ok>: 221 ...   served HEAD line: "Path: poster.example!not-for-mail"
  headers_json has Injection-*: false
  ```
- **RFC:** RFC 5537 §3.5 step 5: "The Message-ID and Date header fields with appropriate contents MUST be added when not
  present in the proto-article." §3.2: "All news server components ... MUST identify themselves, when processing an
  article, by prepending their <path-identity> ... to the Path header field." §3.2.1 step 2: "An injecting agent SHOULD
  prepend the <path-diagnostic> "!.POSTED"". §3.5 step 10: "An Injection-Info header field SHOULD be added".
- **Fix:** an `injectProtoArticle` step for POST only: generate `Message-ID: <random@LocalNNTPHostname>` and
  `Date:` (RFC 5322, `+0000`) when missing, then add Injection-Date (required once either was generated, see the rejected
  sub-claim), `Injection-Info: <host>; posting-account="<hash>"; mail-complaints-to=...`, and rewrite the stored `Path:`
  header line itself to `<host>!.POSTED[.<client-ip>]!<old path or "not-for-mail">`. For IHAVE/TAKETHIS, rewrite the
  stored `Path:` line to `<host>!.SEEN.<peer-ip>!<old>` (or `!!` for a verified peer identity). The `path` column
  becomes a copy of the served value. Effort M.

### INT-2: wrong reply codes for duplicates and in-flight ids

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-2 | INT-2 | medium | rfc | `processor/threading.go:72-99,240-243,270-283`, `processor/processor.go:187-213`, `nntp/nntp-cmd-posting.go:102-110,177-181` | CONFIRMED |

- **Scenario A (already stored, unknown to history):** history says "new" (INT-6/A-intake-1 groups, a history write
  error, `-history=false`, or articles seeded or imported without history). `CheckMessageID` → `CasePass` → 335.
  `processArticle` finds the id in each target group (`ExistsMsgIdInArticlesDB`, `:240`), queues nothing, sets
  `CaseDupes` and still returns `CasePass` (`:270-283`) → 235/239.
- **Scenario B (in flight):** `CheckMessageID` passes, then a concurrent transfer claims the id (`CaseLock`) before this
  connection's `processArticle` runs. `processArticle` maps `CaseLock`/`CaseWrite` to `CaseDupes` (`:76-81`), which
  IHAVE answers 437 and TAKETHIS 439, both "do not retry". If the first transfer then fails, both copies are lost.
- **Scenario C (accepted twice):** after the stuck `CaseLock` of A-intake-1 is evicted (10 min 15 s), the same id is
  "new" again, and an offer or post with a different Newsgroups stores a second copy (eviction simulated with `MsgIdCache.Delete`, which is what `CleanExpiredEntries` does after `CachedEntryExpires + StuckEntryMaxAge`, `history_MsgIdItemCache.go:194-200`).
- **Evidence:** repro, section "INT-2":
  ```
  IHAVE <int2-seeded> (already stored in ai.test.b, unknown to history): 335 Send me -> 235 Article transferred successfully
  TAKETHIS <int2-seeded-tt> (same situation): 239 <int2-seeded-tt@audit.invalid> Article transferred successfully
  after cache eviction (StuckEntryMaxAge=10m0s): CheckMessageID(<sec2-new1>)=0xF1
    POST <sec2-new1> again, now to ai.test.a: 240 Article posted successfully
    second copy stored in ai.test.a=true
  in-flight: CheckMessageID before claim=0xF1, ProcessIncomingArticle after concurrent claim=0x1C err=<nil>
  ```
  (The in-flight case is reproduced deterministically: the program sets the cache item to `CaseLock` between
  `CheckMessageID` and `ProcessIncomingArticle`, which is what the first transfer's `processArticle:82` does.) An
  earlier run of the same program printed `IHAVE <sec2-new1> ...: 436 Retry later` while the item was still stuck.
- **RFC:** RFC 3977 §6.3.2.2: "If the server does not want the article (if, for example, the server already has a copy
  of it), a 435 response MUST be returned ... if ... another client is in the process of sending the same article to the
  server), a 436 response MUST be returned." After the transfer, 235 means "successfully transferred". RFC 4644 §2.5.2:
  for a temporary failure the server "MUST reply with a 400 response to the client and close the connection." RFC 5537
  §3.7 step 3: a serving agent "MUST reject any article that has already been accepted."
- **Fix:** have `processArticle` return a distinct `CaseDupes` when nothing was queued because every group has the
  article (IHAVE 437, TAKETHIS 439, POST 441 "duplicate"), and `CaseRetry` for `CaseLock`/`CaseWrite` (IHAVE 436,
  TAKETHIS 400 + close or 431 after CHECK). Record the history entry when a group already has the article but history
  doesn't (self-healing). Effort S.

### INT-3: lenient Date and Newsgroups parsing

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-3 | INT-3 | medium | rfc | `processor/proc-utils.go:414-454,502-559,587-791` (fallback `:756-772`), `common/headers.go:24-30,539-578`, `nntp/nntp-cmd-posting.go:283-291`, `processor/PostQueue.go:164-179` | CONFIRMED |

- **Scenario (Date):** `ParseNNTPDate` tries ~370 layouts, then `bruteForceDateParse`. When nothing matches, that
  function defaults year 1990, month 1, day 1, 12:00 UTC (`:757-765`) and returns it as a valid date. `processArticle`
  accepts it. `Date: garbage` and `Date: Tuesday 7pm` are both stored as `1990-01-01 12:00:00`. The fabricated date is
  later sent to peers by `ReconstructHeaders` (A-intake-6).
- **Scenario (Newsgroups):** `common.SeparatorRegex` `[,;:\s]+` splits on space, `;` and `:` as well as `,`, so
  `ai.test.a ai.test.b` posts to both groups and `ai.x:AI.TEST.UPPER` to two groups. `IsValidGroupName` in its default
  (non-strict) mode accepts upper case (`validGroupNameRegexCaps`), empty components and a trailing dot
  (`validGroupNameRegexLazy` `^[a-zA-Z0-9][a-zA-Z0-9._+&-]*$` matches `ai..dots.`), `&`, and has no length bound. The
  reserved names of RFC 5536 §3.1.4 are not checked. The strict branch is dead: `UseStrictGroupValidation` is never set
  to true (`cmd/merge-active/main.go:55` and `cmd/rslight-importer/main.go:100` set it to false).
- **Scenario (three splitters):** POST/IHAVE/TAKETHIS split on `,` only (`nntp-cmd-posting.go:283`), `processArticle`
  on the regex, `PostQueueWorker` on `,` (`PostQueue.go:164-179`). The POST split only feeds `NewsgroupsPtr[0]`
  (logged) and the "no Newsgroups" check, because `processArticle` re-derives the groups and replaces `ArticleNums`
  (`threading.go:174`). The inconsistency is real, its effect today is a wrong log line and a stale pointer in
  `NewsgroupsPtr`.
- **Evidence:** repro, section "INT-3": `POST Date 'garbage': 240 ... stored date_sent=1990-01-01 12:00:00`;
  `POST Newsgroups 'ai.test.a ai.test.b' (space separated): 240 ... stored in ai.test.a=true ai.test.b=true`; the 7
  groups of SEC-2.
- **RFC:** RFC 5536 §3.1.4: `newsgroup-list = *WSP newsgroup-name *( [FWS] "," [FWS] newsgroup-name ) *WSP`,
  `component = 1*component-char`; "A <component> ... SHOULD NOT contain uppercase letters"; "The following
  <newsgroup-name>s are reserved and MUST NOT be used as the name of a newsgroup: Groups whose first (or only)
  <component> is "example"" and "MUST NOT be used for the names of normal newsgroups: ... "control" ... "junk"". RFC
  5537 §3.1: "Unacceptable articles MUST be rejected rather than corrected."
- **Fix:** one shared `ParseNewsgroups(value) ([]string, error)` in `common`: split on `,` with optional FWS, require
  each name to match `^[a-z0-9+_-]+(\.[a-z0-9+_-]+)*$` with a length bound (e.g. 255), and reject (not drop) reserved
  names unless a local exception lists them. Uppercase stays accepted only for groups that already exist (RFC 5536 allows
  that). For Date: make `bruteForceDateParse` return zero when it found no year and month, so the article is rejected
  (the fetcher may keep the lenient path for archives behind an explicit flag). Effort M.

### INT-4: no control messages, Supersedes, moderation or Approved check; foreign Xref stored and served

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-4 | INT-4 | medium | missing | `processor/threading.go:57-283` (no handling), `nntp/nntp-client-commands.go:936-939,950`, `nntp/nntp-cmd-helpers.go:20-23` | CONFIRMED |

- **Scenario:** `processArticle` has no branch for `Control:`, `Supersedes:` or `Approved:`, and never reads
  `newsgroups.status`. A cancel is stored as an ordinary article in the listed group and the target stays. A
  `Supersedes:` article doesn't remove the old one. A post to a group with `status='m'` without `Approved:` is accepted
  (the web path refuses it, `web/web_sitePostPage.go:300-303`). `ParseHeaders` drops `Xref` only from the map
  (`:936-939`); the line stays in `headers_json` and HEAD/ARTICLE serve another server's numbers. No local Xref is
  generated, so the XOVER Xref field is empty.
- **Evidence:** repro, section "INT-4":
  ```
  POST Control: cancel <int1-ok>: 240 ...  control stored as normal article in ai.test.a; cancel target still present=true
  POST Supersedes: <int3-space>: 240 ...   superseded article still present=true
  POST to moderated ai.test.mod (status m) without Approved: 240 ...  stored=true
  POST with foreign Xref: 240 ...  served HEAD line: "Xref: other.example ai.test.a:99999"
    XOVER fields=9 last=""
  ```
- **RFC:** RFC 5537 §3.7 step 5: a serving agent "MUST reject any article without an Approved header field posted to any
  newsgroup listed as moderated." Step 7: it "MUST remove any Xref header field from each article ... It then MAY (and
  usually will) add a new Xref header field". "If the serving agent stores articles by newsgroup, control messages
  SHOULD NOT be stored in the newsgroups listed in the control message's Newsgroups header field." §3.5 step 7 for
  injecting agents (forward to moderator or reject).
- **Fix:** (1) moderation: reject unapproved posts to `status='m'` groups (441/437/439) until forwarding exists. (2)
  Xref: strip the foreign line on intake and generate `Xref: <host> group:num ...` from `ArticleNums` after the batch
  assigned the numbers (a `headers_json` update in `processNewsgroupBatch`, or synthesize it at serve time). (3) control
  messages: store them in `control.<type>` (or `control`) instead of the listed groups; cancel/Supersedes processing is a
  separate policy decision (Not covered). Effort L.

### INT-5: `bytes` is the LF-joined body length; article numbers can be reused

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-5 | INT-5 | low | rfc | `nntp/nntp-client-commands.go:876-878`, `migrations/0001_single_db_schema.sql:12` | CONFIRMED |

- **Scenario:** `article.Bytes = len(article.BodyText)` counts the body joined with LF, without headers, CRLFs or the
  separator line. `article_num INTEGER PRIMARY KEY` has no `AUTOINCREMENT`, so SQLite reuses the highest number once its
  row is deleted (admin or spam delete of the newest article, or expiry of an article whose date fell back to 1990, INT-3).
- **Evidence:** repro: `bytes column=23 lines=2; article on the wire (CRLF, headers+body)=218 octets`;
  `article <int5-n2> had number 2, was deleted; next article <int5-n3> got number 2`.
- **RFC:** RFC 3977 §8.1.1: ":bytes ... SHOULD equal the number of octets in the entire article: headers, body, and
  separating empty line (counting a CRLF pair as two octets ...)". RFC 3977 §6: "articles arriving later MUST have
  higher numbers than those that arrive earlier"; §6.1.1.2: articles reinstated with the same number are "a
  reinstatement of the previous article, not a new article reusing the number", and new articles get "numbers greater
  than the reported high water mark".
- **Fix:** compute `bytes` from `NNTPhead`/`NNTPbody` (`sum(len(line)+2) + 2`). Numbering: track the high-water mark per
  group (`newsgroups.high_water`, or a one-row table) and insert with an explicit `article_num = high_water + 1`;
  `AUTOINCREMENT` cannot be added to the existing tables without a rebuild. Effort M.

### INT-6: history adds gated on the newsgroups row that the same batch creates

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| INT-6 | INT-6 | medium | bug | `database/db_batch.go:861-891` vs `:553-563,940-950` | CONFIRMED (worse than seeded: see A-intake-1) |

- **Scenario:** as seeded, the first batch of an auto-created group sees `sql.ErrNoRows` and skips the history adds. The
  seed assumes later batches work. They don't: the row exists after the first batch, but `hierarchy` is NULL, so every
  later `MainDBGetNewsgroup` fails with the scan error (A-intake-1).
- **Evidence:** `server.log` lines for `ai.auto.new1`: first batch `sql: no rows in result set`, second batch
  `Scan error on column index 12, name "hierarchy"`; `history.Exists` false for both articles.
- **Fix:** with SEC-2 fixed, intake never writes into a group without a row. Keep a defensive path: resolve the group ID
  through `NewsgroupDBsIDcache.GetNewsgroupIDByName` and, if it fails, retry the history adds after the stats upsert
  instead of skipping them. Effort S.

### CLI-1 (`common/headers.go` part): ReconstructHeaders emits the Path three times and repeats folded Newsgroups lines

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-1 | CLI-1 | medium | bug | `common/headers.go:267-297,396-411` | CONFIRMED (the Path part is worse than seeded) |

- **Scenario (Path):** `pathline` starts as `"Path: <host>!.TX!<path>"`, then the loop over `strings.Fields(pathline)`
  appends every field again, `"Path:"` included (`:276-289`). The result is one header line with two copies of the
  path, followed by a second `Path:` header (`:293-297`): three copies in two lines. `article.Path` already holds the
  local prefix from `processArticle:185-192`, so the local identity appears twice in each copy.
- **Scenario (folded Newsgroups):** the `getLines` loop appends the continuation lines and increments `i` (`:402-403`),
  but `i` is the `range` variable, so the next iterations process the same continuation lines again. They are
  `isSpacedLine` with `ignoreLine == false` and are emitted again after the unfolded line.
- **Scenario (final empty body line):** storage joins body lines with LF and never adds a trailing LF, so a body whose
  last line is empty is stored as `"...\n"` (repro: `body_text="line1\n"`). The sender loops at
  `nntp-client-commands.go:1149-1153,1271-1275` treat a trailing empty element as the joiner's artifact and drop it. The
  sender half belongs to `audit-nntp-client`; the storage format confirms the seed.
- **Evidence:** repro, section "CLI-1":
  ```
  "Path: news.test.invalid!.TX!news.test.invalid!poster.example!not-for-mail Path: news.test.invalid!.TX!news.test.invalid!poster.example!not-for-mail"
  "Path: news.test.invalid!.TX!news.test.invalid!poster.example!not-for-mail"
  "Newsgroups: ai.test.a, ai.test.b"
  " ai.test.b"
  ```
- **Fix:** build the path line once (`"Path: " + host + "!" + diag + "!" + article.Path` with the stored path not
  re-prefixed), fold only at `!`, and drop the second append. Replace the `range` loop with an index loop (or skip
  continuation lines after an unfold). Store bodies so that the sender can tell a final empty line from the joiner:
  keep `NNTPbody` semantics (no trailing-LF stripping in the sender). Effort S.

### OPS-1 (processor and history): library code kills the process; globals and goroutines without a close path

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| OPS-1 | OPS-1 | medium | robustness | `processor/processor.go:59-113`, `history/history.go:458-500`, `history/history_MsgIdItemCache.go:233-241`, `common/common.go:8-34`, `processor/processor.go:36-57,150-179`, `processor/counter.go:8-10` | CONFIRMED (code trace) |

- **Fatal sites in scope:**
  - `processor.NewProcessor`: `log.Fatalf` at `:63` (no hostname in DB), `:69` (`validateHostname` does a DNS A/AAAA
    lookup, `:381`, so a resolver outage at startup kills the binary), `:88` (history init).
  - `history.beginTx`: `log.Fatalf` after 100 busy retries (`history.go:468-470`). Each attempt can wait 30 s
    (`_busy_timeout=30000`), plus up to 5 s backoff.
  - `history.executeDBTransaction`: `os.Exit(1)` on **any** error inside the transaction, prepare/exec/commit included
    (`:494-500`; the comment forbids touching it). One bad op or a full disk ends `nntp-server`/`web`/fetcher without
    running shutdown. Articles of the current batch are committed but their history entries are lost (INT-2 scenario A).
  - `config.NewDefaultConfig`: `log.Fatalf` when `AppVersion` is unset (`config.go:548`).
- **Re-open state:** `history.NewHistory` itself can be opened again (no package state besides the driver's
  `sync.Once`). What doesn't reset:
  - `history.MsgIdCache` is a process singleton (`history_MsgIdItemCache.go:44-53`). Its `CaseLock`/`CaseDupes` items
    survive a re-open for 15 s to 10 min.
  - `StartCleanupRoutine` starts an endless goroutine (`:234-240`) with no stop channel, one more per `NewProcessor`.
  - `common.ShutdownChan` is closed once by `ForceShutdown` and can never be reopened, so `WantShutdown()` stays true
    and `DownloadArticles` returns at once for the rest of the process.
  - `processor.Batch`, `processor.GroupCounter`, `LocalNNTPHostname` are package globals.
  - `WaitForBatchCompletion` waits forever (`:157,167-172`: the timeout is commented out "we want to wait indefinitely").
- **Fix:** return errors from `NewProcessor` (`(*Processor, error)`) and let the binaries decide. Turn `os.Exit` in
  history into an error that stops the writer and marks history as failed: `Exists` returns an error → `CaseError`, which
  IHAVE answers 436. Give `MsgIdItemCache` a `Stop()` and tie it to `db.StopChan`. Bound `WaitForBatchCompletion` by a
  context. The DB-side globals (`INIT`, `BatchDividerChan`, `migratedDBsCache`) belong to `audit-db`. Effort M.

### A-intake-3: Message-ID syntax and length never validated

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-3 | medium | rfc | `nntp/nntp-cmd-posting.go:267`, `processor/threading.go:21-41,59-66`, `processor/proc_DLArt.go:140-146`, `nntp/nntp-client-commands.go:123,850-853` | CONFIRMED |

- **Scenario:** POST takes the Message-ID header value as is. `processArticle` checks only that it is non-empty.
  `CheckMessageIdFormat` exists (`threading.go:21`), but its only caller is commented out (`proc_DLArt.go:140-146`),
  and it would allow 255 octets. A bracket-less id, an id with spaces, or a 316-octet id is accepted, keyed in history,
  and stored. Such ids cannot be retrieved with `ARTICLE <id>` by RFC-conformant clients and are rejected by peers. On
  the fetch path, the article is stored under the **requested** id (`ParseLegacyArticleLines(*messageID, ...)`): the
  fetched article's own `Message-ID:` header is never compared, so an upstream that returns a different article
  attaches it to the requested id. IHAVE/TAKETHIS of an article without any `Message-ID:` header is accepted under the
  command's id (`reconcileMessageID`, `nntp-cmd-posting.go:115-121`); `headers_json` then has no Message-ID line, so
  HEAD/ARTICLE serve the article without one (code trace; RFC 5537 §3.6 step 1: a relaying agent "MUST reject any
  article without a Newsgroups header field or Message-ID header field"). IHAVE/TAKETHIS argument syntax: A-nntp-rfc-7.
- **Evidence:** repro: `POST Message-ID without angle brackets: 240 ... stored=true`;
  `POST Message-ID of 316 octets: 240 ... stored=true`.
- **RFC:** RFC 5536 §3.1.3: `msg-id = "<" msg-id-core ">"`, "The <msg-id> MUST NOT be more than 250 octets in length."
  RFC 5537 §3.5 step 2: the injecting agent "MUST reject any proto-article that ... is not syntactically valid as
  defined by [RFC5536]."
- **Fix:** one `common.ValidMessageID(s)` (RFC 5536 grammar: `<` dot-atom-text `@` (dot-atom-text | no-fold-literal)
  `>`, at most 250 octets, no WSP) used by POST, IHAVE, TAKETHIS and the fetcher. The fetcher also compares the parsed
  header with the requested id and counts a mismatch as an error. Effort S.

### A-intake-4: POST accepts proto-articles that an injecting agent must reject

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-4 | medium | rfc | `nntp/nntp-cmd-posting.go:14-47`, `processor/threading.go:185-192` | CONFIRMED |

- **Scenario:** POST runs the same `processArticle` as IHAVE/TAKETHIS. It does not reject a proto-article that already
  has `Injection-Info:`, `Injection-Date:` or `Xref:`, or whose `Path:` contains `.POSTED`. Such articles carry forged
  trace data (a fake injecting host and posting account) to every reader and peer.
- **Evidence:** repro:
  ```
  POST with Path containing .POSTED and an Injection-Info header: 240 Article posted successfully
    stored: path="news.test.invalid!evil.example!.POSTED!not-for-mail" headers_json contains Injection-Info=true
  POST with foreign Xref: 240 ...
  ```
- **RFC:** RFC 5537 §3.5 step 2: the injecting agent "MUST reject any proto-article that does not have the proper
  mandatory header fields for a proto-article, that has Injection-Info or Xref header fields, that has a Path header field
  containing the "POSTED" <diag-keyword>".
- **Fix:** a POST-only `checkProtoArticle` before INT-1's injection step: 441 for Injection-Info/Xref present, `POSTED`
  in Path, or an Injection-Date present together with a missing Message-ID or Date. Effort S.

### A-intake-5: web crossposts are stored only in the first group

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-5 | medium | bug | `processor/PostQueue.go:64-126,130-160`, `processor/threading.go:108-113,200-205` | CONFIRMED |

- **Scenario:** `pre_processArticle` copies the article per group, but every copy shares the original `Headers` map
  (`PostQueue.go:96`). The first `processArticle` (bulk mode) frees memory by setting every value of that shared map to
  nil (`threading.go:200-205`). For the second group `GetHeaderFirst(article.Headers, "newsgroups")` returns "", so
  `processArticle` fails with "no 'newsgroups' header" (`:108-113`). The error is only logged. The `post_queue` row is
  written for `newsgroups[0]` only (`PostQueue.go:114-125`).
- **Evidence:** repro, section "web crosspost": `stored in ai.test.web1=true ai.test.web2=false`.
- **Fix:** deep-copy `Headers` per copy (or pass the group list explicitly and stop reading the header in bulk mode),
  and don't mutate maps the caller owns in `processArticle`. Effort S.

### A-intake-6: ReconstructHeaders rewrites relayed and posted articles

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-6 | medium | rfc | `common/headers.go:165-537` | CONFIRMED |

- **Scenario:** TAKETHIS feeds (`nntp-client-commands.go:1114`, `cmd/nntp-transfer/main.go:500`) and upstream POST
  (`:1206`, used by `postmgr`) send `ReconstructHeaders(...)`, not the stored header block. The function:
  - replaces a non-RFC `Date` with `DateSent` (`:182-217`). Together with INT-3 the fabricated `Mon, 01 Jan 1990
    12:00:00 +0000` is sent to peers;
  - reorders the block: Date, From, Message-ID, Subject, References, Path first (`:242-311`), then the rest;
  - drops header lines shorter than 4 octets, without a colon, without a space after the colon, or with a space in the
    name (`:335-374`), and silently drops a repeated Subject/From/Date/Message-ID/References/Path (`IgnoreHeadersMap`,
    `:375-378`);
  - upper-cases the first character of a header name (`:342-347`) and renames a repeated Newsgroups to `X-RW-...`
    (`:384-390`);
  - lowercases and filters the Newsgroups, moves it to the end and adds `X-pugleaf-debug:` (`:440-527`);
  - adds the non-standard diagnostic `!.TX!` after the local identity (`:272,294,304`), plus CLI-1's duplicates.
- **Evidence:** repro, "rewrite demo" (input `DateString "Tuesday 7pm"`, `HeadersJSON` with `Newsgroups:
  Comp.Lang.Go,ai.test.a`, `x-lower`, `NoSpace:v`, two `Organization`, a second `Subject`):
  ```
  "Date: Mon, 01 Jan 1990 12:00:00 +0000"
  ... "X-lower: v" "X: 1" "Organization: first" "Organization: second"
  "X-pugleaf-debug: badGroups=1 valid=2"
  "Newsgroups: comp.lang.go,ai.test.a"
  ```
  (`NoSpace:v` and `Subject: dup` are gone.)
- **RFC:** RFC 5537 §3.6: "Relaying agents MUST NOT alter, delete, or rearrange any part of an article except for the
  Path and Xref header fields. ... If an article is not acceptable as is, the article MUST be rejected rather than
  modified." §3.1: "agents (other than moderators) MUST NOT modify articles in ways other than described here."
  §3.2.1 defines the diagnostics (`!`, `!.MISMATCH.`, `!.SEEN.`, `!.POSTED`); `.TX` is not one of them.
- **Fix:** send `NNTPhead`/stored `headers_json` unchanged except for (a) the new Path (prepend `<host>!`) and (b) Xref
  removal. Articles that don't pass the intake validation (A-intake-2, INT-3) are rejected at intake, so the sender no
  longer needs to repair them. Keep `ReconstructHeaders` only for legacy rows without `headers_json`, and skip (log)
  instead of repairing. The sender side of the change is in the `client` slice. Effort M.

### A-intake-7: future-date margin is 25 h, Injection-Date ignored, no past cutoff for POST

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-7 | low | rfc | `processor/threading.go:155-167`, `processor/proc-utils.go:778-785` | CONFIRMED |

- **Scenario:** the future check uses 25 h (`threading.go:163`, also `bruteForceDateParse:780`). It reads only `Date`,
  never `Injection-Date`. POST has no past limit, and neither does relaying (no cutoff interval, so history must be kept
  forever; it is).
- **Evidence:** repro: `POST Date now+24h30m (...): 240 Article posted successfully`.
- **RFC:** RFC 5537 §3.7 step 2: a serving agent "MUST examine the Injection-Date header field or, if absent, the Date
  header field, and reject the article if that date is more than 24 hours into the future." §3.5 step 3 (injecting):
  "SHOULD similarly reject any article whose Date header field is too far in the past ... this interval SHOULD NOT be
  any shorter than 72 hours into the past."
- **Fix:** use `Injection-Date` when present, 24 h for the future margin, and a configurable past limit for POST only
  (default 72 h). Effort S.

### A-intake-8: NNTP posts accepted into inactive and no-posting groups

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-8 | medium | bug | `processor/threading.go:208-268`, `processor/proc-utils.go:414-454` | CONFIRMED |

- **Scenario:** `processArticle` never reads `newsgroups.active` or `status`. A group the admin deactivated
  (`active=0`) or set to `n`/`x` keeps receiving NNTP posts and feeds. The web path refuses both
  (`web/web_sitePostPage.go:295-307`), `PostQueueWorker` skips inactive groups (`PostQueue.go:137-140`).
- **Evidence:** repro: `POST to inactive ai.test.off: 240 ... stored in inactive group=true; row: id=4 active=false`.
- **Fix:** part of SEC-2's lookup: drop groups with `active=0`, reject POST to `status` `n`/`x` (441) and treat `m` as in
  INT-4. Effort S.

### A-intake-9: "posted successfully" when nothing was stored

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-9 | medium | bug | `processor/threading.go:211-243,270-283` | CONFIRMED |

- **Scenario:** when `GetGroupDB` fails for every target group, the loop `continue`s (`:233-239`), `queued` stays 0, and
  the function returns `CasePass` (`:283`). POST answers 240, IHAVE 235, TAKETHIS 239, and the article is lost.
  Triggers: a group name longer than the file-system limit (it passes `validGroupNameRegexLazy`, which has no bound), a
  group DB that fails to migrate, or disk errors. The same line also answers INT-2 scenario A with success.
- **Evidence:** repro: `POST to a 303-char group name (passes validGroupNameRegexLazy): 240 Article posted successfully;
  group DB files created: 0; history.Exists=false`. `server.log`: `Failed to get group DBs for newsgroup 'ai.g…g': ...
  unable to open database file: file name too long`.
- **Fix:** distinguish "queued ≥ 1" (`CasePass`), "all groups already have it" (`CaseDupes`, INT-2) and "nothing could be
  queued" (`CaseError` → 441/436/439). Effort S.

### A-intake-10: post queue rows can stay `in_processing` forever; queue DB access bypasses the retry helpers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-10 | low | robustness | `postmgr/postmgr.go:85-99`, `database/db_post_queue.go:22-160`, `cmd/post-queue/main.go:113-121` | CONFIRMED (code trace) |

- **Scenario:**
  - `GetPendingPostQueueEntries` marks up to `limit` rows `in_processing=1` in one transaction (`:38-102`).
  - On stop, `ProcessPendingPosts` returns from inside the loop and resets only the failed IDs (`postmgr.go:86-93`).
    The selected but unprocessed rows stay `in_processing=1`. The same happens when the process is killed between
    select and `MarkPostQueueAsPostedToRemote`.
  - `ResetAllPostQueueProcessing` ("useful for cleanup on startup", `:145`) has no caller. Stuck rows are never selected
    again, so those web posts never reach upstream unless an admin presses retry.
  - A 441 from upstream is retried every 10 s forever (no attempt counter).
  - Every function in `db_post_queue.go` uses raw `d.mainDB.Exec/Query/Begin` instead of the `Retryable*` helpers
    (CLAUDE.md). `GetPendingPostQueueEntries` uses a deferred transaction that reads, then writes, and doesn't check
    `rows.Err()`.
  - The non-daemon path of `cmd/post-queue` ends with `os.Exit` (`:117,120`), which skips the deferred `db.Shutdown()`
    and `ClosePool()`.
- **Fix:** call `ResetAllPostQueueProcessing` at post-queue startup, reset the unprocessed remainder on stop, add an
  attempt counter column, move the queries to `RetryableTransactionExec`/`RetryableExec`, and `return` from `main`
  instead of `os.Exit`. Effort S.

### A-intake-11: a timed-out DownloadArticles leaves its feeder and late results to the next run

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-intake-11 | low | robustness | `processor/proc_DLArt.go:44-64,124-192,245-248,300-322` | PLAUSIBLE |

- **Scenario:** after 12 × 5 s without progress, `DownloadArticles` returns (`:245-248`). The XHDR goroutine and the
  feeder goroutine keep running. Items still being fetched (`Batch.GetQ`) are later sent to the group's `ReturnQ`, which
  lives on in the global `Batch.GroupQueues` (`:44-64`). The next `DownloadArticles` for that group counts them as its own
  (`gots+errs+notf == gotQueued`, `:223,300`), so it can stop before its own items are processed and then move the
  progress past them (`:316-322`). A feeder blocked on a full `ReturnQ` (capacity 1000) holds a pool connection until
  the next run drains it.
- **Missing step:** not reproduced. It needs a fake upstream that stalls XHDR or ARTICLE for more than 60 s
  (`audit-nntp-client` has a fake-server repro under its own scratch directory).
- **Fix:** give each run its own `ReturnQ` (create per call, not per group), make the feeder and XHDR goroutines select
  on a per-run `done` channel, and update progress only up to the highest contiguous processed number. Effort M.

## Header handling matrix

"Intake" covers POST, IHAVE and TAKETHIS (`nntp-cmd-posting.go` → `ParseLegacyArticleLines` → `processArticle`) and the
fetcher (`GetArticle` → same parser, bulk mode). "Served" is HEAD/ARTICLE, which send `headers_json` verbatim
(`nntp-cmd-helpers.go:20-23`), and XOVER/XHDR, which use the columns. "Outbound" is `ReconstructHeaders` (TAKETHIS feed,
upstream POST).

| Header | Validated on intake | Generated when missing | Rewritten | Served over NNTP | RFC requirement | Ids |
|----|----|----|----|----|----|----|
| Date | present + `ParseNNTPDate`; garbage → 1990-01-01 12:00 UTC; future > 25 h rejected; no past limit; repeated → `date_sent` from the first Date, `date_string` LF-joined | no (POST 441) | stored line unchanged; `date_sent` normalized UTC; outbound replaced by `DateSent` when not RFC 1123/822 | HEAD/ARTICLE: original line(s); XOVER: `date_string` column (LF inside for repeated Date) | 5536 §3.1, §3.1.1 (exactly one; RFC 5322 date-time, accept GMT); 5537 §3.5(3,5), §3.7(2) | INT-1, INT-3, A-intake-6, A-intake-7 |
| From | non-empty only; repeated → LF-joined in `from_header` | no (441) | no (outbound: repeats dropped) | original line(s); XOVER `from_header` with LF | 5536 §3.1, §3.1.2 (exactly one mailbox-list) | A-intake-2 |
| Message-ID | non-empty (POST); must equal the command id (IHAVE/TAKETHIS, `reconcileMessageID`); no syntax/length check; first of several wins; fetcher uses the requested id | no (441; IHAVE/TAKETHIS copy the command id into a header-less article) | no | original line(s) | 5536 §3.1.3 (`<id-left@id-right>`, ≤ 250 octets); 5537 §3.5(2,5,6) | INT-1, A-intake-3, A-nntp-rfc-7 |
| Newsgroups | non-empty; split on `[,;:\s]+`; lax names (upper case, `..`, trailing `.`, `&`, no length bound, reserved names); no existence/active/status check; ≤ 9 groups; repeated → union | no (441); bulk mode uses the fetched group | no (outbound: lowercased, filtered, moved last, `X-RW-` for repeats) | original line(s); groups created on the fly | 5536 §3.1.4 (grammar, reserved names); 5537 §3.5(4), §3.7 ("MUST NOT create new newsgroups") | SEC-2, INT-3, A-intake-1, A-intake-8 |
| Path | none (any value; already-prefixed values are not prefixed again) | `path` column only: `<host>!unknown!not-for-mail` | `path` column `<host>!<path>` (no diagnostic); the served line is never changed; outbound `<host>!.TX!<host>!<path>`, three copies | original line (local identity missing) | 5536 §3.1.5; 5537 §3.2, §3.2.1 (prepend identity + diagnostic, `!.POSTED` for injection) | INT-1, CLI-1, A-intake-6 |
| Subject | non-empty only; repeated → LF-joined in `subject` | no (441) | no (outbound: repeats dropped) | original line(s); XOVER/XHDR `subject` with LF (desync) | 5536 §3.1, §3.1.6 (exactly one) | A-intake-2, A-nntp-rfc-2, BUG-5 |
| Injection-Date | not read (not used for the date checks) | no. Compliant today, because POST requires Message-ID and Date; required once INT-1 generates them | no | original line if present | 5536 §3.2.7; 5537 §3.5(11), §3.6(2), §3.7(2) | A-intake-7, INT-1 |
| Injection-Info | not read; a POST carrying one is accepted | no | no | original line (forged trace possible) | 5536 §3.2.8; 5537 §3.5(2) MUST reject if present, (10) SHOULD add | A-intake-4, INT-1 |
| References | none (parsed with `utils.ParseReferences` for threading; repeated/folded joined with spaces) | no (web replies build it) | outbound refolded at 1000 octets | original line(s); XOVER `references` | 5536 §3.2.10 (msg-id list) | — |
| Control | ignored: stored as an ordinary article in the listed groups | n/a | no | original line | 5537 §3.7 (store in `control.*`), §5 | INT-4 |
| Supersedes | ignored: the superseded article stays | n/a | no | original line | 5536 §3.2.12; 5537 §3.6(5), §3.7(4) | INT-4 |
| Approved | ignored: unapproved posts to `status='m'` groups accepted (NNTP; the web refuses) | n/a | no | original line | 5536 §3.2.1; 5537 §3.5(7), §3.7(5) MUST reject | INT-4 |
| Xref | dropped from the parsed map only; foreign line kept in `headers_json`; a POST carrying one is accepted | never (no local Xref) | outbound: dropped (allowed) | HEAD/ARTICLE: the foreign line; XOVER: empty Xref field | 5536 §3.2.14 (at most one, local); 5537 §3.5(2), §3.7(7) MUST remove | INT-4, A-intake-4, BUG-5 |
| Lines | header ignored; `lines` column = number of body lines | n/a (obsolete header) | no | original line; XOVER `:lines` from the column (correct) | 5536 §3.3.1 (obsolete) | — |
| Bytes | header ignored (web posts add `Bytes: len(body)-lines`); `bytes` column = `len(LF-joined body)` | n/a (not a header) | no | XOVER `:bytes` from the column (wrong) | 3977 §8.1.1 (whole article, CRLF) | INT-5 |

## Function inventory

| function/file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `processor.CheckMessageIdFormat` (`threading.go:21`) | | yes | | yes | | only caller commented out (`proc_DLArt.go:140-146`); allows 255 octets (RFC 250) |
| `processor.containsAtAndDot` (`threading.go:43`), `ComputeMessageIDHash` (`:16`), `countLines` (`proc-utils.go:575`) | | | yes | | | staticcheck U1000 / no callers; `ComputeMessageIDHash` duplicates `history.ComputeMessageIDHash` |
| `processor.processArticle` (`threading.go:57`) | | | | | yes | `CasePass` with nothing queued (INT-2, A-intake-9); nils the caller's `Headers` map (A-intake-5) |
| `(*Processor).ProcessIncomingArticle` (`processor.go:258`) | | | | yes | | "Use the primary newsgroup for processing": the argument is ignored outside bulk mode |
| `(*Processor).ImportOverview` (`proc_ImportOV.go:14`) | | yes | | | yes | no caller; compares remote numbers with local `MAX(article_num)` (`:42-49`); unchecked `tx.Commit()` (`:95`); `PRAGMA synchronous=FULL` reaches one pooled connection (`:108`) |
| `(*Processor).GetXHDR` (`proc_DLXHDR.go:6`), `GetHistoryStats` (`processor.go:126`) | | | yes | | | no callers |
| `Counter.{GetReset,GetResetAll,Add,Increment}` (`counter.go:23-64`) | | | | | yes | ignore the receiver and always use the global `GroupCounter`, so `ThreadCounter`/`LegacyCounter` (`processor.go:94-95`) are aliases of it |
| `proc_MsgIDtmpCache.go`, `proc_MsgIdItemCache.go`, `unified_cache.go` | yes | | | | | whole files commented out / `// stub` |
| `(*Processor).extractGroupsFromHeaders` (`proc-utils.go:414`) | | | | | yes | calls `GetNewsgroupPointer` for every raw token before validation (`:423`): `TasksMap` entries for invalid names (expire after 120 s) |
| `processor.BridgeManager.BridgeArticle` (`bridges.go:86`), `EnableBridges` (`processor.go:278`) | | yes | | | | the call is commented out (`threading.go:262-267`); the cmd/web flags are commented out (`cmd/web/main.go:138-146`) and so is `FetchRoutine`'s body (`main_functions.go:663-`) |
| `fediverse.Bridge.BridgeArticle` (`fediverse/bridge.go:63`), `ActivityPubServer.SendActivity` (`activitypub.go:116`) | yes | yes | yes | | | `_ = activity // TODO: actually send`; `PublicKeyPem: "TODO: RSA public key"`; unsigned requests; message-id unescaped in URLs |
| `matrix.MatrixClient` (`matrix/client.go`) | | yes | | | yes | access token in the URL query (`:71,99`); subject/from/body interpolated unescaped into `formatted_body` HTML (`:115-126`); reachable only through dead bridges |
| `processor.PostQueueWorker.pre_processArticle` (`PostQueue.go:64`), `parseNewsgroups` (`:164`) | | | | | yes | crossposts (A-intake-5); returns nil (success) when Newsgroups is missing (`:66-70`); a third Newsgroups splitter (INT-3) |
| `common.ReconstructHeaders` (`headers.go:165`) | | | | | yes | CLI-1, A-intake-6 |
| `common.parseReferencesToContinuedLineString` (`headers.go:146`), `multiLineStringToSlice` (`:626`) | | | yes | | | staticcheck U1000 |
| `common.IsValidGroupName` (`headers.go:539`), `processor.IsValidGroupName` (`proc-utils.go:410`) | | | | yes | | doc says "according to RFC standards"; the default lazy mode accepts non-RFC names (INT-3); strict branch dead (`UseStrictGroupValidation` never true) |
| `common.ForceShutdown`/`ShutdownChan` (`common.go:8-34`) | | | | | | one-shot, not re-openable (OPS-1) |
| `history.DefaultHistoryDir = "./data/history"` (`history.go:28`), `ValidateConfig` (`history_config.go:158-160`) | | | | yes | | an empty `HistoryDir` silently becomes `./data/history` (the production path) instead of an error |
| `HistoryConfig.{CacheExpires,CachePurge,UseShortHashLen}`, `HistoryStats.{TotalFileLookups,CacheHits,CacheMisses}` | | | yes | | | set or reported, never used (marked legacy) |
| `history.{UnixTimeSec,Str2int64,FileExists,Mkdir}` (`utils.go`) | | | yes | | | no callers (`DirExists` is used by `cmd/history-rebuild`) |
| `"history sqlite3 normal.go"` | yes | | | | | `// stub` |
| `SQLite3ShardedDB.GetShardedDB(dbIndex, write)` (`"history sqlite3_sharded.go":146`) | | | | yes | | `write` ignored; `SQLite3DB.mux` only guards `Close` |
| history DSN `_synchronous=OFF` (`history_config.go:201`) | | | | | | durability: a power loss can drop recent history rows, so those ids are accepted again (rebuildable with `history-rebuild`) |
| `MsgIdItemCache.StartCleanupRoutine` (`history_MsgIdItemCache.go:234`) | | | | | yes | endless goroutine, no stop (OPS-1) |
| `postmgr.PosterManager.Run`/`Stop` (`postmgr.go:33-65`) | | | | | yes | both `close(pm.stopCh)`: `Stop()` then a shutdown signal panics (latent: `Stop` has no caller); `peering` branch dead (`:174,201-207`); `ResetPostQueueProcessing` error unchecked (`:91`) |
| `preloader.LoadNewsgroupsFromActive`/`FromDescriptions` (`preloader.go:19,123`) | | | | yes | | `ctx` never checked; group names and status not validated; any `GetNewsgroupByName` error is treated as "missing"; comment "Default to moderated posting allowed" for status `y` (`:178`) |
| `config.ServerConfig.Hostname`, `NNTP.MaxArtSize`, `MainConfig.MaxArtSize` (`config.go:122,131,76`) | | | yes | yes | | the hostname for Path comes from `LocalNNTPHostname`/DB; MaxArtSize never enforced (SEC-3); comment "128 KB" on a 32 KiB value (`:574`) |
| `database.ResetAllPostQueueProcessing` (`db_post_queue.go:146`) | | | yes | | | no caller (A-intake-10) |
| `cmd/post-queue` usage (`main.go:131-163`, flag `:34`) | | | | yes | | help lists `-max-providers`, `-max-concurrent`, `-retry-attempts`, `-retry-delay`, `-check-interval`, `-dry-run`, which don't exist; `-cleanup-older` says "Delete all post_queue entries", the code deletes only posted ones; `BackupDir` stays `./backups` regardless of `-data` |
| `internal/spam/*` | yes | yes | | yes | | only `*.go.off` files, a README and a YAML example; nothing compiles; the README advertises active features with ✅ |

## Seed items rejected

No seed in scope is rejected as a whole. One sub-claim is rejected:

- **INT-1, "No Injection-Date":** not a defect today. Every article the code accepts has both Message-ID and Date (POST
  rejects the others with 441), and RFC 5537 §3.5 step 11 says: "If the proto-article had both a Message-ID header field
  and a Date header field, an Injection-Date header field MUST NOT be added". Injection-Date becomes mandatory only when
  INT-1's fix starts generating a missing Message-ID or Date. The Injection-Info (SHOULD), `.POSTED` (SHOULD) and Path
  (MUST) parts of INT-1 stay CONFIRMED.

Seed text corrected, not rejected:
- **INT-6** says the first batch of an auto-created group skips the history adds. Every batch skips them (A-intake-1).
- **CLI-1** says two Path headers. There are two Path header lines with three copies of the path, each containing the
  local identity twice.
- **INT-3**, "the POST path splits Newsgroups with a plain `strings.Split(",")`": confirmed, but the split result is used
  only for logging and the empty check; `processArticle` re-derives the groups.
- **INT-5**, "article numbers can be reused": only when the highest-numbered row is deleted; plain oldest-first expiry
  doesn't trigger it.

## Not covered / follow-ups

- **Owned elsewhere, referenced only:** the server half of SEC-2 and SEC-3, SEC-4, BUG-8 (`audit-nntp-sec`). In one
  early run, `HEAD <id>` right after a 240 got 430, because history had not been flushed yet and `local430` then cached
  the miss: the window is history flush lag (`BatchTimeout`, here 50 ms; production 1000 ms) plus BUG-8. The served side
  of A-intake-2 is A-nntp-rfc-2 (`audit-nntp-rfc`). IHAVE/TAKETHIS argument syntax is A-nntp-rfc-7, TAKETHIS temporary
  errors A-nntp-rfc-8, IHAVE 436 for permanent rejects RFC-5. NULL-hierarchy scans in other queries and the re-open
  globals of the DB are `audit-db`. Web post headers (INT-7) are `audit-web`. The sender loops of CLI-1 (final empty
  line, `nntp-client-commands.go:1149-1153,1271-1275`) are `audit-nntp-client`. `rslight` import paths are
  `audit-tools`.
- **Not run:** the fetcher path end to end (no upstream; traced in code: same parser, bulk mode, no history check, per-group
  dedupe at `proc_DLArt.go:148` and `threading.go:240`). A real concurrent IHAVE race (the in-flight window was
  reproduced by setting the cache state; a timing race would be flaky). A-intake-11 (needs a stalling fake upstream).
  `cmd/post-queue` against a real upstream.
- **Policy decisions for `fixplan-nntp-intake`:** cancel/Supersedes processing (honour, ignore, or honour only from the
  same poster/injection host); whether peer feeds may create groups at all; the past cutoff for POST; whether the
  fetcher keeps lenient dates (archives) behind a flag; Message-ID generation format
  (`<base32(random 16)>@<LocalNNTPHostname>`); the Injection-Info `posting-account` hash (reuse `ComputeHashedUsername`
  as the web does).
- **Tests to add (wave 2 / fix plans):** A-intake-1 (POST to an unknown group, then `MainDBGetAllNewsgroups` must not fail
  and `GROUP` must not be 411), A-intake-2 (`..` header line and repeated Subject must be rejected), A-intake-5 (web
  crosspost lands in both groups), A-intake-9 (POST to an over-long group name must not answer 240). None of these ids is
  in the wave-2 test lists yet; `test-nntp-intake-e2e` covers INT-1, INT-2 and SEC-2.

## Appendix A: repro transcript

`go run ./data-test-audit-intake/_repro/intake/main.go` (final run, 2026-10-11; 87 lines; two mislabeled
`newsgroups row` probes for `ai`/`x` removed, long strings elided with `…`):

```
== INT-1: injection duties (POST)
POST without Message-ID: 441 Posting failed (processing error)
POST without Date: 441 Posting failed (processing error)
POST complete: 240 Article posted successfully
stored path column: "news.test.invalid!poster.example!not-for-mail"
HEAD <int1-ok>: 221 0 <int1-ok@audit.invalid> Headers follow
served HEAD line: "Path: poster.example!not-for-mail"
headers_json has Injection-*: false
POST with Path containing .POSTED and an Injection-Info header: 240 Article posted successfully
  stored: path="news.test.invalid!evil.example!.POSTED!not-for-mail" headers_json contains Injection-Info=true
== INT-3: Date / Newsgroups parsing
POST Date 'garbage': 240 Article posted successfully
  stored date_sent=1990-01-01 12:00:00
POST Date 'Tuesday': 240 Article posted successfully
  stored date_sent=1990-01-01 12:00:00
POST Date now+24h30m (Mon, 12 Oct 2026 11:26:11 +0000): 240 Article posted successfully
POST Newsgroups 'ai.test.a ai.test.b' (space separated): 240 Article posted successfully
  stored in ai.test.a=true ai.test.b=true
POST Newsgroups "ai.x:AI.TEST.UPPER;example.foo,control,junk,ai..dots.,c++": 240 Article posted successfully
  group DB files created by this POST: 7 (before=2)
== SEC-2 (intake): unknown group, INT-6: history gating
POST to unknown ai.auto.new1: 240 Article posted successfully
  group DB files right after the 240: +1
  stored=true
  newsgroups row: no row (sql: Scan error on column index 12, name "hierarchy": converting NULL to string is unsupported)
  history.Exists(<sec2-new1@audit.invalid>) after 3s: false
  CheckMessageID now: 0x2C (CaseRetry=0x2C CaseDupes=0x1C CasePass=0xF1)
POST 2nd article to ai.auto.new1: 240 Article posted successfully
  history.Exists(<sec2-new2@audit.invalid>): false
  GROUP ai.auto.new1: 411 No such newsgroup
  LIST ACTIVE line: "ai.auto.new1 2 1 n"
  LIST ACTIVE line: "control 1 1 n"
  LIST ACTIVE line: "junk 1 1 n"
  MainDBGetAllNewsgroups() err=sql: Scan error on column index 12, name "hierarchy": converting NULL to string is unsupported
POST to inactive ai.test.off: 240 Article posted successfully
  stored in inactive group=true; row: id=4 active=false status="y" hierarchy="ai"
== INT-2: duplicate reply codes
IHAVE <int2-seeded> (already stored in ai.test.b, unknown to history): 335 Send me -> 235 Article transferred successfully
TAKETHIS <int2-seeded-tt> (already stored in ai.test.b, unknown to history): 239 <int2-seeded-tt@audit.invalid> Article transferred successfully
after cache eviction (StuckEntryMaxAge=10m0s): CheckMessageID(<sec2-new1>)=0xF1
  POST <sec2-new1> again, now to ai.test.a: 240 Article posted successfully
  second copy stored in ai.test.a=true
IHAVE <sec2-new1> (435 from history: the item was deleted and the re-post put the id in history): 435 Not wanted
IHAVE <int1-ok> (stored, in history): 435 Not wanted
in-flight: CheckMessageID before claim=0xF1, ProcessIncomingArticle after concurrent claim=0x1C err=<nil> (IHAVE maps 0x1C to 437, TAKETHIS to 439)
== INT-4: Control, Supersedes, Approved, Xref
POST Control: cancel <int1-ok>: 240 Article posted successfully
  control stored as normal article in ai.test.a; cancel target still present=true
POST Supersedes: <int3-space>: 240 Article posted successfully
  superseded article still present=true
POST to moderated ai.test.mod (status m) without Approved: 240 Article posted successfully
  stored=true
POST with foreign Xref: 240 Article posted successfully
  served HEAD line: "Xref: other.example ai.test.a:99999"
  XOVER fields=9 last=""
== repeated / malformed headers (root of A-nntp-rfc-2)
POST with 2x From/Newsgroups/Subject/Message-ID, a '.' line and a colon-less line: 240 Article posted successfully
  ai.test.a: subject="first\nsecond" from="a <a@example.invalid>\nb <b@example.invalid>"
  headers_json="Path: x!not-for-mail\nFrom: a <a@example.invalid>\nFrom: b <b@example.invalid>\nNewsgroups: ai.test.a\nNewsgroups: ai.test.b\nSubject: first\nSubject: second\n.\nno colon line\nMessage-ID: <rep-1@audit.invalid>\nMessage-ID: <rep-2@audit.invalid>\nDate: Sun, 11 Oct 2026 10:56:19 +0000"
  also stored in ai.test.b (2nd Newsgroups header)=true; stored under 2nd Message-ID=false
POST Message-ID without angle brackets: 240 Article posted successfully
  stored=true
POST Message-ID of 316 octets: 240 Article posted successfully
  stored=true
== SEC-3 (intake): size, INT-5: bytes / numbering
POST with one 4 MiB body line (config MaxArtSize default=0): 240 Article posted successfully
  stored=true
POST for bytes check: 240 Article posted successfully
  bytes column=23 lines=2; article on the wire (CRLF, headers+body)=218 octets
POST body 'line1' + final empty line: 240 Article posted successfully
  body_text="line1\n" lines=2
  article <int5-n2> had number 2, was deleted; next article <int5-n3> got number 2
== web crosspost through PostQueueWorker (bulkmode processArticle)
  stored in ai.test.web1=true ai.test.web2=false
  web1 path="news.test.invalid!.POSTED!not-for-mail"
== nothing stored, still 240
POST to a 303-char group name (passes validGroupNameRegexLazy): 240 Article posted successfully
  group DB files created: 0; history.Exists=false
== CLI-1 (common/headers.go): ReconstructHeaders
withPath=true err=<nil>
  "Date: Mon, 02 Jan 2006 15:04:05 +0000"
  "From: a <a@example.invalid>"
  "Message-ID: <cli1@audit.invalid>"
  "Subject: s"
  "Path: news.test.invalid!.TX!news.test.invalid!poster.example!not-for-mail Path: news.test.invalid!.TX!news.test.invalid!poster.example!not-for-mail"
  "Path: news.test.invalid!.TX!news.test.invalid!poster.example!not-for-mail"
  "Newsgroups: ai.test.a, ai.test.b"
  " ai.test.b"
  "Organization: Org"
rewrite demo err=<nil>
  "Date: Mon, 01 Jan 1990 12:00:00 +0000"
  "From: a <a@example.invalid>"
  "Message-ID: <cli1b@audit.invalid>"
  "Subject: s"
  "Path: news.test.invalid!.TX!news.test.invalid!x!not-for-mail Path: news.test.invalid!.TX!news.test.invalid!x!not-for-mail"
  "Path: news.test.invalid!.TX!news.test.invalid!x!not-for-mail"
  "X-lower: v"
  "X: 1"
  "Organization: first"
  "Organization: second"
  "X-pugleaf-debug: badGroups=1 valid=2"
  "Newsgroups: comp.lang.go,ai.test.a"
== done
```

Main DB after the run (`sqlite3 run/cfg/pugleaf.sq3 "SELECT name,active,status,COALESCE(hierarchy,'NULL'),message_count
FROM newsgroups WHERE hierarchy IS NULL"`):

```
ai.auto.new1|1|y|NULL|2
junk|1|y|NULL|1
ai.x|1|y|NULL|1
example.foo|1|y|NULL|1
AI.TEST.UPPER|1|y|NULL|1
ai..dots.|1|y|NULL|1
control|1|y|NULL|1
c++|1|y|NULL|1
```

The repro's "config MaxArtSize default=0" refers to the zero-valued `&config.ServerConfig{}` the harness builds;
`NewDefaultConfig` sets 32768. Neither value is read by the intake (SEC-3).
