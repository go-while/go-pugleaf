# Plan: NNTP article intake and injection (RFC 5536/5537 validation, injection, dedupe codes, size limit)

Slug: `nntp-intake-injection` · Integration branch: `plan-nntp-intake-injection` (from `testing-001`)
Run with: `/run-plan .claude/plans/queued/nntp-intake-injection.md`
Parallelism: wave 1 has 3 implementer slices, wave 2 has 1, wave 3 has 1. Within a wave, no two slices own the same file.
**Depends on: `nntp-server-hardening` (merged into `testing-001` first).** Cross-plan run order (final):
nntp-server-hardening → nntp-intake-injection → nntp-client-transfer → nntp-rfc-commands → audit-delta-fixes.
From hardening this plan consumes, in package `nntp`: the bounded article reader `readArticleBlock(maxBytes)`,
`ErrArticleTooLarge`, `ErrArticleDrainLimit`, `DefaultMaxArticleBytes`, `(*NNTPServer).MaxArticleBytes()`,
`maxArticleDrainBytes` (64 MiB cap); `Config.Hostname` set from `processor.LocalNNTPHostname` in
`cmd/nntp-server/main.go`; the `-maxartsize` flag; the served-side dot-stuffing (A-nntp-rfc-2). The preflight
re-reads those names at the integration-branch HEAD and adjusts S4's calls to their exact merged signatures.
For the client plan this plan provides `common.ValidMessageID` and `common.CheckHeaderBlock(lines, HeaderBlockFetch)` (contract K1).
Source: audit `docs/audit/AUDIT-2026-10.md` (§4 "Fix plan" column `nntp-intake-injection`, §11 decisions 4, 5, 6, 9)
and `docs/audit/audit-intake.md`; written by wave 4 slice `fixplan-nntp-intake` of `.claude/plans/wip/nntp-audit-tests.md`.
Base for every `file:line` below: `plan-nntp-audit-tests` @ 03d5ef7 (verified with `grep -n` / `sed -n`). None of
the owned files differ between 03d5ef7 and the `testing-001` merge of `nntp-audit-tests`; the preflight re-checks
that (`git diff --stat 03d5ef7 HEAD -- <owned files>` must be empty, otherwise re-anchor before wave 1).

---

## Context

The intake path is POST/IHAVE/TAKETHIS (`internal/nntp/nntp-cmd-posting.go` →
`ParseLegacyArticleLines` → `processor.ProcessIncomingArticle` → `processArticle(bulkmode=false)` → batch writer)
plus the bulk paths (fetcher `proc_DLArt.go`, web `PostQueue.go`, rslight) that share `processArticle(bulkmode=true)`.
The audit found that intake never validates the header block, never resolves the target groups against the
`newsgroups` table, answers success when nothing was stored, never injects (Message-ID/Date/Path/Injection-Info),
has no byte limit, and maps dupes and temporary failures to the wrong reply codes. Every finding below was traced
in code and reproduced by the audit (`audit-intake.md` Appendix A) or by a `knownBug` test.

### Findings

Paths are under `internal/` unless they start with `cmd/`. Severity is the consolidated one (AUDIT §4).

| ID | Where (03d5ef7) | Defect | Sev |
|----|----|----|----|
| SEC-2 (intake half) | `processor/threading.go:125-137,208-243`, `processor/proc-utils.go:414-454` (`GetNewsgroupPointer` before validation `:423`), `database/db_batch.go:553-563` | An unknown `Newsgroups:` name passes the lax regex, `processArticle` calls `GetGroupDB` (`threading.go:232`, creates dir + SQLite file + schema) and the batch upsert creates an `active=1` row. Inactive/deleted groups come back. RFC 5537 §3.7: "MUST NOT create new newsgroups". | high |
| A-intake-2 | `nntp/nntp-cmd-posting.go:243-245`, `nntp/nntp-client-commands.go:929,948-950`, `common/headers.go:591-601`, `processor/threading.go:108-161` | The header block is never validated: a `..` line is stored as `.`, colon-less lines are stored, repeated Subject/From/Date are LF-joined into the overview columns, repeated Newsgroups are unioned. Same parser on the fetch path. Root cause of A-nntp-rfc-2 (served side, `nntp-server-hardening`). | high |
| A-db-1 (alias A-intake-1), upsert part | `database/db_batch.go:553-563` (`query_updateNewsgroupsStats`), exec `:946-947` | The batch upsert INSERTs `(name, message_count, last_article, updated_at)` only, so `hierarchy` stays NULL; `MainDBGetNewsgroup`/`MainDBGetAllNewsgroups` then fail on that row (fetcher `$all`, history-rebuild, expire-news, recover-db abort; GROUP 411). The readers and the repair migration are `audit-delta-fixes`. | high |
| INT-6 | `database/db_batch.go:855-891` (gate `:861-869`, CaseDupes only inside `if historyOK` `:875-891`) | History adds are gated on `MainDBGetNewsgroup`, which fails for a new row (first batch: `ErrNoRows`) and for every later batch (NULL `hierarchy` scan). The msgid item stays `CaseLock` until eviction (10 min 15 s). | high |
| INT-1 | `processor/threading.go:59-61,155-161,185-192`, `nntp/nntp-cmd-posting.go:42-44`, `nntp/nntp-cmd-helpers.go:20-23` | POST without Message-ID or Date → 441 (the injecting agent must add them, RFC 5537 §3.5 step 5). The local path identity goes only into the `path` column; HEAD/ARTICLE serve `headers_json` with the original `Path:`. No `!.POSTED`, no Injection-Info, no Injection-Date when generating. An already `host!`-prefixed Path is not prefixed again (`threading.go:189`). | medium |
| INT-2 | `processor/threading.go:76-81,240-243,270-283`, `nntp/nntp-cmd-posting.go:102-110,177-179` | An article stored in every target group but unknown to history → `CasePass` → IHAVE 235 / TAKETHIS 239. An in-flight id (`CaseLock`/`CaseWrite`) → `CaseDupes` → 437/439 ("do not retry"). | medium |
| INT-3 | `processor/proc-utils.go:416` (`SeparatorRegex` `[,;:\s]+`, `common/headers.go:24`), `common/headers.go:539-578` (lax `IsValidGroupName`), `processor/proc-utils.go:587,756-772` (brute-force fallback 1990-01-01), `nntp/nntp-cmd-posting.go:283-288`, `processor/PostQueue.go:164-179` | Garbage `Date:` is stored as 1990-01-01 12:00 UTC; Newsgroups split on space/`;`/`:` too, with upper case, `..`, `&`, no length bound and reserved names accepted; three different splitters. | medium |
| INT-4 | `processor/threading.go:57-283` (no branch), `nntp/nntp-client-commands.go:936-939,950` | No Control/Supersedes/Approved handling: cancels are stored as normal articles, unapproved posts to `status='m'` groups are accepted, a foreign `Xref:` stays in `headers_json` and is served. | medium |
| A-intake-3 | `nntp/nntp-cmd-posting.go:115-121,267`, `processor/threading.go:21-41,59-61`, `processor/proc_DLArt.go:140-146` | Message-ID syntax/length never checked (bracket-less and 316-octet ids stored); IHAVE/TAKETHIS of an article without `Message-ID:` is stored under the command id; the fetcher never compares the fetched header with the requested id. | medium |
| A-intake-4 | `nntp/nntp-cmd-posting.go:14-47` | POST accepts proto-articles with `Injection-Info:`, `Xref:` or `.POSTED` in Path (forged trace data). RFC 5537 §3.5 step 2: MUST reject. | medium |
| A-intake-5 | `processor/PostQueue.go:84-100` (shared `Headers` map `:96`), `processor/threading.go:200-205` | Web crossposts land only in the first group: the first bulk `processArticle` nils every value of the shared `Headers` map, the second copy has no Newsgroups. | medium |
| A-intake-6 (intake half) | `common/headers.go:165-537` (sender, client plan), `processor/threading.go:185-205` | The sender rewrites articles because intake stores what it must not: foreign Xref, unvalidated blocks, a `headers_json` Path that differs from the stored `path`. Intake half: store a valid block whose Path line equals the `path` column and has no Xref, so `nntp-client-transfer` can send `headers_json` with only the Path prepend. | medium |
| A-intake-8 | `processor/threading.go:208-268` | `newsgroups.active` and `status` are never read: posts and feeds land in inactive groups and groups with status `n`/`x`. | medium |
| A-intake-9 | `processor/threading.go:232-239,270-283` | When no group DB can be opened (`GetGroupDB` error, e.g. a 303-char name, disk errors), `queued` stays 0 and `processArticle` still returns `CasePass`: 240/235/239 with nothing stored. | medium |
| SEC-3 (intake side) | `nntp/nntp-cmd-posting.go:221-261` (`maxLines, maxHead := 16384, 1024` `:224`), `:128` (TAKETHIS buffers before the auth check `:145`), `config/config.go:131,574` | Articles are capped only by line counts; any line may be arbitrarily long; `Server.NNTP.MaxArtSize` (default `DefaultMaxArticleSize` = 32 KiB, `config.go:26`) and `newsgroups.max_art_size` are never read. TAKETHIS buffers a full article for unauthenticated clients. The bounded reader itself is hardening's (`readArticleBlock`); this plan wires it into POST/IHAVE/TAKETHIS (replies, discard for unauthenticated TAKETHIS, per-group limit). | high |
| BUG-7 ("too large" part only; handler side, the reader is hardening's) | `nntp/nntp-cmd-posting.go:227-230`, `:39`, `:95`, `:141` | On "too large" `readArticleLines` closes the connection and the handler still writes 441/436/439, then `Handle` writes 500 to the closed socket; with ~3,600 unread lines in the receive buffer the kernel sends RST and the client gets no status at all (e2e `TestE2ESizeLimits/lines-20000`). The rest of BUG-7 (Stop, CronLocal430, TLS listener, QUIT log) stays in `nntp-server-hardening`. | medium |
| RFC-5 (every code in `nntp-cmd-posting.go`) | `nntp/nntp-cmd-posting.go:17,27,53,95,106-110,132` | POST without a processor and POST by a user without posting rights → 502 (RFC 3977 §6.3.1.2: 440); IHAVE/TAKETHIS without a processor → 502 (not offered: 500); IHAVE answers 436 ("retry") for permanent rejects (validation errors, garbled article), so peers retry forever; 437 is required (RFC 3977 §6.3.2.2). The MODE/AUTHINFO 501s stay in `nntp-server-hardening`. | medium |
| A-nntp-rfc-7 | `nntp/nntp-cmd-posting.go:56-72,134-137` | `IHAVE not-a-message-id` → 335; TAKETHIS accepts any token as id. RFC 3977 §3.2.1: 501. | low |
| A-nntp-rfc-8 | `nntp/nntp-cmd-posting.go:131-152,160-165` | TAKETHIS 480/501/502 replies lack the message-id; a history error answers `439 … Retry later` (439 = do not retry). RFC 4644 §2.5.2: temporary error → 400 and close. | low |
| A-intake-7 | `processor/threading.go:162-168`, `processor/proc-utils.go:778-785` | Future margin 25 h (RFC 5537 §3.7 step 2: 24 h), Injection-Date ignored, no past cutoff for POST (§3.5 step 3: SHOULD, at least 72 h). | low |
| A-tools-1 (handed over by audit-delta-fixes) | `processor/threading.go:256` | `go proc.DB.Batch.BatchCaptureOverviewForLater(...)` per stored article with no bound: one parked goroutine plus the full article each (19 872 goroutines, +377 MiB for 20 000 fetched articles). It cannot become a plain call: `processArticle` holds `article.Mux` (`:68-69`) and `BatchCaptureOverviewForLater`/`BatchDivider` lock the same mutex (`database/db_batch.go:157`, `:1769`). | high |
| A-db-3 (handed over by audit-delta-fixes) | `database/db_batch.go:999` (`INSERT OR IGNORE`), `:1063-1094` (number mapping by message-id), `:813-823,947` (`len(batches)` counted), `processor/threading.go:240` / `database/db_groupdbs.go:336-343` (`ExistsMsgIdInArticlesDB` returns false on any error) | A message-id already stored in the group, or twice in one batch, is "committed" with the existing number: `message_count` grows, history is re-added, and `batchProcessThreadRoots` inserts a second `threads` row (no unique constraint). | medium |
| A-db-4 (handed over by audit-delta-fixes) | `database/db_batch.go:893-916` (`latestDate` taken only after the "still referenced" `continue`), `:946-947` | A batch of crossposts writes `updated_at = '0001-01-01 00:00:00'`; a backfill batch moves `updated_at` backwards. | medium |
| A-intake-11 | `processor/proc_DLArt.go:44-64,80,124-192,245-248` | A timed-out `DownloadArticles` returns while its XHDR/feeder goroutines keep running; late results land in the per-group global `ReturnQ` and are counted by the next run of that group. PLAUSIBLE (not reproduced). | low |

Cross-references only (not fixed here; owner in brackets):
- **INT-5** `bytes` = LF-joined body length (`nntp/nntp-client-commands.go:876-878`) [nntp-client-transfer]; article-number reuse (`migrations/0001_single_db_schema.sql:12`) [audit-delta-fixes]. This plan adds `common.WireArticleSize` (wave 1), which the client plan may reuse for `bytes`.
- **A-intake-10** post queue `in_processing` rows, raw `Exec/Begin` in `database/db_post_queue.go` [audit-delta-fixes].
- **INT-7** web posts stored without Subject/Message-ID/Date/Path/References in `headers_json` (`web/web_sitePostPage.go:358-390`) [audit-delta-fixes]. This plan's bulk-mode checks are written so that today's web header block passes (see S3).
- **CLI-1** `common/headers.go` `ReconstructHeaders` duplicate Path / repeated folded Newsgroups / dropped final empty body line [nntp-client-transfer]. This plan only guarantees what intake stores: one Path line equal to the `path` column, no Xref, no duplicate mandatory headers, no colon-less lines (strict intake).
- **A-nntp-rfc-2** served side (dot-stuffing and LF sanitizing in HEAD/ARTICLE/XOVER/XHDR) [nntp-server-hardening]. Hardening removes the `e2eKnownBug(t, "A-nntp-rfc-2", …)` marker in `TestE2EDotHeaderLine` (`server_e2e_test.go:1329`, served-side stuffing makes the 240 branch pass). S3 then changes that test's expectation to 441, because intake now refuses the article (A-intake-2).
- **BUG-8** `local430` not invalidated on arrival [nntp-server-hardening: `(*Local430).Remove`; the TTL is 60 s by user decision, so the `TestArtLocal430/ihave-arrival` marker is still open at this plan's base and S4 must remove it]. S4 calls `c.server.local430.Remove(id)` after every 240/235/239 (the call site is in this plan's file).
- **A-db-5, A-db-9** live in `db_batch.go` functions this plan does not touch (see K6) [audit-delta-fixes]. A-db-9 (the divider's `MaxQueued` brake) complements A-tools-1's limiter here. A-tools-1, A-db-3 and A-db-4 were handed over by audit-delta-fixes and are fixed here (S3, S2). **A-tools-4** (per-group `ReturnQ` never released) is resolved as a side effect of S5 (per-run queue); audit-delta-fixes should re-check and close it rather than re-implement.

### Decisions (behaviour changes, report to the user at the end)

- **D1 Unknown-group policy (AUDIT §11.4).** Intake never creates groups. For POST, IHAVE and TAKETHIS (non-bulk), every
  syntactically valid name is looked up in `newsgroups` (`GetIntakeGroup`). A name without a row, with `active=0`,
  a reserved name (D8), or a group whose status is not `y`/`m`/`n` is dropped from the target list. Status `n` is
  dropped for POST only (RFC 3977 §7.6.3: "posting not permitted", feeds still accepted). If no group is left, the
  article is rejected: POST 441, IHAVE 437, TAKETHIS 439. Partially unknown crossposts are stored in the known groups
  (RFC 5537 §3.5 step 4 only requires "at least one valid group"). No `-accept-unknown-groups` flag in this plan;
  peers get the same policy. `control` / `junk` are reserved names (D8), so they are never targets.
- **D2 Moderated groups (INT-4).** If any listed group that survives D1 has status `m` and the article has no
  `Approved:` header, the whole article is rejected (RFC 5537 §3.7 step 5, MUST). With `Approved:` it is stored
  (no moderator forwarding exists).
- **D3 Control messages, cancel, Supersedes (AUDIT §11.6).** Non-bulk articles with a `Control:` header are rejected
  (POST 441, IHAVE 437, TAKETHIS 439) with the log line `[INTAKE] control message <id> (<type>) refused: control
  messages are not processed`. `Supersedes:` is accepted and stored as an ordinary article; the superseded article
  stays, with the log line `[INTAKE] Supersedes ignored for <id>`. Implementing cancel/Supersedes is out of scope.
- **D4 Message-ID generation (INT-1).** `<` + base32 (RFC 4648 lower-case alphabet, no padding) of 16 bytes from
  `crypto/rand` + `@` + path identity + `>`, e.g. `<k5q3…26 chars…@news.example.org>`. Generated only for POST.
- **D5 Date injection (INT-1).** POST without `Date:` gets `Date: <now UTC as "Mon, 02 Jan 2006 15:04:05 +0000">`.
  A present Date must parse with `net/mail.ParseDate`; otherwise 441.
- **D6 Injection-Date / Injection-Info (INT-1, RFC 5537 §3.5 steps 10/11).** `Injection-Date:` (same format, same
  instant) is added **only** when the server generated the Message-ID or the Date. Every POST gets
  `Injection-Info: <identity>; posting-account="<hash>"; logging-data="<nonce>"`, where nonce is the injection Unix
  time and hash is `db.ComputeHashedUsername(<NNTP username>, nonce)` (the web uses the same function). When the salt
  is missing or the hash fails, the header is `Injection-Info: <identity>` only. No `posting-host` and no client IP
  anywhere (privacy; same as the web's `.POSTED!not-for-mail`). No `mail-complaints-to` (no config key).
- **D7 Path policy (INT-1, AUDIT §11.5).** The served Path is the stored Path; the `path` column always equals the
  value of the stored `Path:` line.
  - POST: `Path: <identity>!.POSTED!<proto Path value>`; without a proto Path: `<identity>!.POSTED!not-for-mail`.
  - IHAVE/TAKETHIS: `Path: <identity>!.SEEN.<peer IP>!<received value>`; a missing Path is not invented (the article is rejected as lacking a mandatory header).
  - The prefix is always prepended (no "already starts with our host" shortcut for NNTP intake).
  - Fetched/imported articles (bulk): the stored `Path:` line gets the same value as today's `path` column (`<identity>!<received>`, no diagnostic); a block without a Path line (web posts, INT-7) is left alone.
  - Identity = `c.server.Config.Hostname` (nil-safe). Hardening sets it from `processor.LocalNNTPHostname` after `SetHostname` in `cmd/nntp-server/main.go`. `cmd/web`'s embedded NNTP block (`cmd/web/main.go:428-473`, dead) is deleted by audit-delta-fixes, so no `cmd/web` change here. No interface assertion on the processor (fake processors and `cmd/web`'s adapter have no identity method; the config is the one source). Without an identity, POST is refused (441 `Posting failed (no path identity)`, log `[NNTP] POST refused: no path identity`) and IHAVE/TAKETHIS keep the received Path.
- **D8 Group-name validation (INT-3).** Intake (non-bulk) uses the RFC 5536 §3.1.4 grammar: names split on `,` with optional surrounding whitespace, each name `component *("." component)`, `component = 1*(ALPHA / DIGIT / "+" / "-" / "_")`, total length ≤ 255 octets. Empty list elements (`a,,b`, trailing comma) are ignored; any other syntax error rejects the article. Upper case is syntactically valid but only matches an existing row with exactly that name. Reserved (always dropped): first component `example`, `to` or `control`; whole name `junk` or `poster`; any component `all` or `ctl`. The existing `common.IsValidGroupName` (lax) stays for the bulk paths and other callers.
- **D9 Dedupe and failure reply codes (INT-2, RFC-5, A-nntp-rfc-8).** Table K4 below. Already stored → POST 441 / IHAVE 437 / TAKETHIS 439 (and history learns the id). In flight → POST 441 / IHAVE 436 / TAKETHIS 439 (INN-compatible: RFC 4644 has no retry code for TAKETHIS and a 400 would tear down the peer's stream on a benign race; documented deviation). Temporary failure (history or DB error) → POST 441 / IHAVE 436 / TAKETHIS 400 + close. Gates (RFC-5, all in this plan): POST without a processor or without posting permission → 440; IHAVE without a processor → 500; TAKETHIS without a processor → `500 <id>` (after consuming the article).
- **D10 Date window (A-intake-7, AUDIT §11 "past-date cutoff").** Future margin 24 h for every `processArticle` path (`MaxFutureDate`; `bruteForceDateParse`'s own internal 25 h sanity bound stays as it is, K1), measured on `Injection-Date` when present and parseable, else `Date`. Past cutoff only for POST: `Date` older than `nntp.PostDateMaxAge = 72 * time.Hour` → 441 (RFC 5537 §3.5 step 3 minimum). Feeds and the fetcher have no past cutoff. Unparseable dates (no year/month found) are rejected on non-bulk intake; the bulk paths keep the lenient parser (archives), so `ParseNNTPDate` keeps its current results for every existing caller (recover-db, `GlobalDateParser`).
- **D11 Article size (SEC-3, AUDIT §11.9).** The wire limit and the bounded reader are hardening's: `(*NNTPServer).MaxArticleBytes()` (from `Config.NNTP.MaxArtSize` / `-maxartsize`, fallback `DefaultMaxArticleBytes`) and `readArticleBlock(maxBytes)`, which drains an oversized article to its terminator (`ErrArticleTooLarge`, stream in sync) up to `maxArticleDrainBytes` (`ErrArticleDrainLimit` beyond). This plan's handlers answer `ErrArticleTooLarge` with POST 441 / IHAVE 437 / TAKETHIS 439 (connection stays usable) and `ErrArticleDrainLimit` with `400` via `closeAfterReply` (CloseWrite + drain ≤ 1 s + close). Unauthenticated or not-permitted TAKETHIS reads through the same bounded reader and discards the result before replying. Per-group `newsgroups.max_art_size` (> 0) drops that group (D1 rules), measured with `common.WireArticleSize`. **The config default stays 32 KiB** (`config.go:26`); see Q1.
- **D12 Fetch-path header checks (A-intake-2, A-intake-3).** Bulk articles (fetcher, rslight, web post queue) go through the same block check in "fetch" mode: reject a NUL, CR or LF octet inside a line (other control octets such as ESC in ISO-2022-JP headers pass), a duplicate Date/From/Message-ID/Newsgroups/Path/Subject, and a `Message-ID:` header that differs from the requested id; colon-less lines and leading continuation lines are dropped from `headers_json` (normalized). Strict mode (NNTP intake) rejects all of these and every other C0 control except TAB.

### Out of scope (list them in Outcome)
- Cancel / Supersedes processing, `control.*` filing, moderator forwarding (D2/D3).
- Local `Xref:` generation; the served XOVER Xref field (BUG-5, nntp-server-hardening).
- `bytes` semantics and article-number reuse (INT-5: client plan / audit-delta-fixes).
- Readers of NULL-hierarchy rows and the repair migration (A-db-1 readers, A-db-11, A-db-13: audit-delta-fixes).
- `ReconstructHeaders` (CLI-1, A-intake-6 sender half: nntp-client-transfer).
- MODE/AUTHINFO 501 (RFC-5 rest: nntp-server-hardening); the bounded reader and `-maxartsize` (hardening).
- A default change for `MaxArtSize` (Q1), and `-accept-unknown-groups` for peers.
- Web posting headers and `X-pugleaf-Trace` → Injection-Info (INT-7: audit-delta-fixes).

---

## Design

### Contracts all slices rely on
- **K1: stable signatures (unchanged by this plan).**
  - `nntp.ArticleProcessor` (`nntp/nntp-server.go:18-26`): `ProcessIncomingArticle(*models.Article) (int, error)`, `CheckMessageID(string) int`, `FindArticleByMessageID(string, string) (*models.Article, error)`. Fake processors in tests keep compiling.
  - `(*processor.Processor).ProcessIncomingArticle`, `CheckMessageID`, `processArticle(article, legacyNewsgroup string, bulkmode bool) (int, error)`, `ParseNNTPDate(string) time.Time` and `bruteForceDateParse` (same results for every input, including the internal 25 h bound), `IsValidGroupName`.
  - Bulk mode of `processArticle` keeps returning `CasePass, nil` when every target group already stores the article (`rslight.go:347` treats anything else as an error); only non-bulk returns `CaseDupes` for that case (M3 decision: no rslight change).
  - **Cross-plan contract (consumed by nntp-client-transfer):** `common.ValidMessageID(s string) bool` (RFC 5536 §3.1.3 grammar, exactly one `@`, ≤ 250 octets) and `common.CheckHeaderBlock(lines []string, mode HeaderBlockMode) ([]string, error)` with `HeaderBlockFetch` semantics as in D12. Names, signatures and fetch-mode rules stay stable after S1; there is no separate normalize function.
  - `database`: `GetGroupDB` (auto-create stays, D4 Appendix B), `MainDBGetNewsgroup`, `InsertNewsgroup`, `ProcessorInterface`, `(*SQ3batch).BatchCaptureOverviewForLater`, `updateNewsgroupStatsWithRetry`, the log text `"[BATCH] processNewsgroupBatch Main database connection is nil, cannot update newsgroup stats for '%s'"` (asserted by `TestFuBatchFaultsThreadingFailureFallsThroughToPhase3`).
  - `nntp`: every `handle*` name, `reconcileMessageID` (name kept, semantics change in S4), `parseIncomingArticleLines(headLines, bodyLines []string) (*models.Article, []string, error)`, `extractHeaderValue`, `buildIncomingArticle`.
  - `processor.ProcessorAdapter` in `cmd/nntp-server` keeps its 4 methods; no new method.
  - From hardening (consumed, not changed): `readArticleBlock(maxBytes)` (refreshes the read deadline per line and keeps the 16384-line cap as a secondary bound: not re-implemented here), `ErrArticleTooLarge`, `ErrArticleDrainLimit`, `DefaultMaxArticleBytes`, `(*NNTPServer).MaxArticleBytes()`, `maxArticleDrainBytes`, `(*Local430).Remove(messageID)` (60 s TTL; hardening left the BUG-8 marker in place for S4).
- **K2: new names reserved per slice** (all free at 03d5ef7, checked with `grep -rnw <name> internal cmd`):
  - S1 `intake-common` (package `common`, new files): `ErrIntakeRejected`, `ErrIntakeTemporary`, `ValidMessageID`, `ParseNewsgroups`, `ValidGroupNameSyntax`, `IsReservedGroupName`, `HeaderBlockMode`, `HeaderBlockStrict`, `HeaderBlockFetch`, `CheckHeaderBlock`, `HeaderValue`, `HeaderValues`, `ReplaceHeader`, `RemoveHeaders`, `WireArticleSize`, `GenerateMessageID`, `FormatNewsDate`. Package `models`: field `Article.Posted`.
  - S2 `intake-db`: `IntakeGroup`, `(*Database).GetIntakeGroup`, `query_GetIntakeGroup`, `(*GroupDB).LookupMsgIdInArticlesDB`.
  - S3 `intake-processor`: `captureLimiter`, `newCaptureLimiter`, `(*captureLimiter).acquire`, `(*captureLimiter).release`, `captureLimit`, `MaxCaptureInFlight`, `defaultCaptureLimiter`, `CaptureInFlight`, `resolveIntakeGroups`, `intakeGroupDecision`, `releaseClaim`, `ParseNNTPDateStrict`, `bruteForceDateParseOK`, `MaxFutureDate`, `validateIntakeHeaders`, `syncStoredPath`, `stripXrefLines`.
  - S4 `intake-nntp`: `(*ClientConnection).pathIdentity`, `injectProtoArticle`, `relayPath`, `intakeKind` (+ consts `intakePost`, `intakeIHave`, `intakeTakeThis`), `intakeReply`, `closeAfterReply`, `PostDateMaxAge`, `closeDrainTimeout`. (Re-check at the S4 base that hardening did not take any of them.)
  - S5 `intake-dlart`: `newDownloadRun`, `drainLateResults`.
- **K3: tests.**
  - No new `TestMain`. Package `database` tests use the existing `w0DB`/`w0Name`/`fuBatch*` helpers; `cmd/nntp-server` uses the existing `e2e*` helpers; package `nntp` uses `wiringTest*` and the harness.
  - Prefixes: S1 `icTest…`/`TestIntakeCommon…`/`FuzzIntakeCommon…` (`internal/common/intake_test.go`); S2 `idb…`/`TestIntakeDB…` (`internal/database/nntp_intake_test.go`); S3 `ipr…`/`TestIntakeProc…` (`internal/processor/intake_test.go`) and `TestE2E…` in `server_e2e_test.go` (K2 of the audit plan); S4 `intk…`/`TestIntk…` (`internal/nntp/nntp-intake_test.go`), `TestWiring…` cases in `nntp-wiring_test.go`, `FuzzE2E…`, `TestE2E…`; S5 `TestIntakeDL…` (`internal/processor/intake_dl_test.go`).
  - Group names `t7.<test>.<seq>` in new e2e tests (existing tests keep `t6.*`); message-ids `<t7-<test>-<seq>@test.invalid>`. No `t.Parallel()`, no package global mutated after `TestMain` (pass parameters instead), no sleep over 100 ms (poll with `e2eWaitFor`). One explicit exception: `TestE2EInFlightRetry` sets and resets the `history.MsgIdCache` item of its own unique message-id (under the item's `Mux`); no other test touches that id.
  - A fixed bug makes its `knownBug`/`e2eKnownBug` call fatal: the slice that fixes it removes the call **and keeps the assertion** (list per slice below).
- **K4: return-code contract of `ProcessIncomingArticle` (non-bulk) and its NNTP mapping (`intakeReply`).**

  | `processArticle` returns | Meaning | POST | IHAVE (after 335) | TAKETHIS |
  |----|----|----|----|----|
  | `CasePass, nil` | queued for ≥ 1 group | `240 Article posted successfully` | `235 Article transferred successfully` | `239 <id> Article transferred successfully` |
  | `CaseDupes, nil` | history has it, the cache says stored, or every target group already stores it | `441 Posting failed (duplicate)` | `437 Rejected (duplicate)` | `439 <id> Rejected (duplicate)` |
  | `CaseRetry, nil` | in flight (another goroutine holds `CaseLock`/`CaseWrite`) | `441 Posting failed (in progress, try again later)` | `436 Retry later (in progress)` | `439 <id> In progress` |
  | `CaseRetry, err` with `errors.Is(err, common.ErrIntakeTemporary)` | history or DB error, no group DB could be opened | `441 Posting failed (temporary failure, try again later)` | `436 Retry later (temporary failure)` | `400 Service temporarily unavailable` + close |
  | `CaseError, err` with `errors.Is(err, common.ErrIntakeRejected)` | validation or policy rejection | `441 Posting failed (<reason>)` | `437 Rejected (<reason>)` | `439 <id> Rejected (<reason>)` |
  | anything else (unclassified, incl. `CaseError, nil`) | defensive | `441 Posting failed (processing error)` | `436 Transfer failed (processing error)` | `400 Service temporarily unavailable` + close |

  `<reason>` is one of a fixed set of short strings (`invalid header block`, `missing mandatory header`, `invalid message-id`, `invalid newsgroups`, `too many newsgroups`, `no valid newsgroup`, `moderated group without Approved`, `control messages not accepted`, `invalid date`, `date in the future`, `date too old`, `invalid injection date`, `forged trace`, `invalid article`, `no path identity`, `article too large`); never client input. Pre-transfer replies (`CheckMessageID` at the offer stage) stay: IHAVE 435/436/436, TAKETHIS 439/439 and for `CaseError` now `400` + close (A-nntp-rfc-8). Gate replies (RFC-5): POST no processor / no permission `440`, IHAVE no processor `500`, TAKETHIS no processor `500 <id>`; 480/501/502 TAKETHIS replies carry the id.
  After every accept (240/235/239) the handler calls `c.server.local430.Remove(<id>)` (BUG-8 audit fix; hardening provides `Remove`).
- **K5: claim handling in `processArticle`.** A rejection keeps today's `setCaseDupes` (re-offers within `CachedEntryTTL` get 435/439). A temporary failure calls `releaseClaim(item)` (`Response = history.CaseError`, `CachedEntryExpires = now + history.ErrorCaseTTL`), so `CheckMessageID` and the next `processArticle` fall through to the history lookup and the article can be retried. `CasePass` leaves the item to the batch writer (S2 now sets `CaseDupes` for every committed article).
- **K6: no-touch zones.** S3 changes the capture only as A-tools-1 specifies (one slot per article taken before `article.Mux.Lock()` in both modes, one capture goroutine per article). **`db_batch.go` ownership:** S2 owns `:549-563` (`query_updateNewsgroupsStats`), `:794-823` (PHASE 1 call, number loop), `:855-933` (PHASE 3), `:935-967` (stats block), `:969-1001` (`batchInsertOverviews`) and `:1003-1103` (`processOverviewBatch`). audit-delta-fixes (runs after this plan) changes only the `query_processOverviewBatch` constant on top of S2's version, plus the divider, orchestrator, `ExpireCache` and queue-counter parts (A-db-5, A-db-9); the threading functions stay untouched by both. S4 owns every reply code in `nntp-cmd-posting.go` (gate lines included; hardening agreed) but does not touch `nntp-server-cliconns.go` or hardening's reader/`Local430` code.
- **K7:** Nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt`, `internal/config/config.go` or files outside their ownership list. `gofmt` runs only on owned files (baseline: `internal/database/embedded_migrations.go`, `internal/web/web_admin_provider.go`). No slice calls a helper another slice of the same wave adds.

### Component design
- **Validation layers.** Wire layer (S4, `nntp-cmd-posting.go`): replies on top of hardening's bounded reader, RFC-5 gate codes, message-id syntax of the command argument, POST proto-article checks and injection, relay Path. Article layer (S3, `processArticle`): header-block check (strict for non-bulk, fetch mode for bulk), mandatory headers, Message-ID header vs id, Control, Newsgroups grammar, dates, group resolution (D1/D2, per-group size), Xref strip, Path sync. Pure helpers (S1, `common`) are shared by both and by the client plan.
- **Group resolution** (S3) uses `GetIntakeGroup` (S2): one main-DB `SELECT` per listed name (≤ `MaxCrossPosts`=9), NULL-safe, never touching `GetGroupDB` for a rejected name. No cache in this plan (intake volume is small next to the main-DB cost of the batch writer).
- **Batch writer** (S2): the stats statement becomes an `UPDATE` (never inserts, so no NULL-hierarchy row and no resurrected group); PHASE 3 resolves the group ID with `GetIntakeGroup` and sets `CaseDupes` on every committed article whether or not the history add ran.
- **Size limit** (S4): handlers read with hardening's `readArticleBlock(c.server.MaxArticleBytes())` and map its errors (D11); no second reader. `closeAfterReply` is the only place that closes a connection from the intake handlers: write the reply, `CloseWrite` when the conn supports it, read and discard for at most `closeDrainTimeout = time.Second` / 1 MiB, close the conn, discard `c.textConn.R.Buffered()` bytes, return nil (so `Handle` does not write a 500). If hardening's merged `Handle` has a close sentinel or a `Close` that is safe with pipelined input (A-nntp-sec-1 fix), use that instead of closing by hand.

---

## Waves

Common rules for every slice:
- Follow K1–K7; read the findings rows for your ids and every owned file (plus the callees named in the steps) before editing.
- Checks in your worktree:
  ```bash
  gofmt -l <each owned dir>                 # none of your owned files listed
  go vet ./...
  go build ./...
  go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
    ./internal/nntp/... ./internal/processor/... ./internal/common/... ./cmd/expire-news/... \
    ./cmd/history-rebuild/... ./cmd/nntp-server/...
  PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
    | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u   # must equal the slice's "expected set after"
  git status --short                        # clean (no data-test-*, active.out, zz_* files)
  ```
- Report: ids fixed, `knownBug` calls removed (file, test, id), new tests, the known-bug set after the slice, and any change you needed outside your files (do not make it).

### Wave 1 (3 parallel `pugleaf-implementer` slices, from the integration branch HEAD)

#### Slice `intake-common` (S1): pure helpers and the `Posted` flag
Ids: none (enabler for A-intake-2, A-intake-3, INT-1, INT-3, A-nntp-rfc-7, SEC-3 in S3/S4).

Owned files:
- `internal/common/intake.go` (new)
- `internal/common/intake_test.go` (new)
- `internal/models/models.go` (only the `Article` struct and `RecycleArticle`)

Changes:
1. `internal/common/intake.go` (stdlib only; `common` must not import `database`, `nntp` or `processor`):
   - `var ErrIntakeRejected = errors.New("article rejected")`, `var ErrIntakeTemporary = errors.New("temporary intake failure")`. Doc comment: producers wrap with `fmt.Errorf("%w: <reason>", ErrIntakeRejected)`; the reason after `": "` is a fixed string from K4.
   - `ValidMessageID(s string) bool`: RFC 5536 §3.1.3 / RFC 3977 §3.6: `len(s) <= 250`, starts with `<`, ends with `>`, exactly one `@` outside a `[...]` literal, non-empty left and right parts, every octet in `%x21-7E` except `<`, `>` inside, no WSP. Right part either dot-atom-text or a `[...]` no-fold-literal.
   - `ValidGroupNameSyntax(name string) bool` (D8 grammar, ≤ 255 octets) and `IsReservedGroupName(name string) bool` (D8 list, compared lower-case).
   - `ParseNewsgroups(value string) ([]string, error)`: split on `,`, trim SP/TAB, skip empty elements, return `fmt.Errorf("%w: invalid newsgroups", ErrIntakeRejected)` on the first name failing `ValidGroupNameSyntax`, de-duplicate keeping order, error when the result is empty.
   - `type HeaderBlockMode int`; `HeaderBlockStrict`, `HeaderBlockFetch`.
   - `CheckHeaderBlock(lines []string, mode HeaderBlockMode) ([]string, error)`; input lines are un-stuffed, without CRLF, without the separator. Rules:
     1. both modes: a line containing NUL, CR or LF → reject. Strict mode also rejects every other C0 control except TAB; fetch mode lets them pass (ESC in ISO-2022-JP encoded headers, archives).
     2. a line that starts with SP/TAB and has no preceding field line (first line, or after a dropped line) → strict: reject; fetch: drop.
     3. a non-continuation line without `:`, with an empty field name, or with a name octet outside `%x21-39 / %x3B-7E` (RFC 5322 ftext; this covers a lone `.`) → strict: reject; fetch: drop it and its continuation lines.
     4. duplicates (case-insensitive name): strict mode rejects a second copy of the fields that feed overview columns or intake decisions: `Date, From, Message-ID, Newsgroups, Path, Subject, References, Injection-Date, Injection-Info, Xref, Control, Supersedes, Approved`; fetch mode rejects only the first six. Other repeated fields (Organization, User-Agent, …) pass in both modes.
     5. strict only: a line longer than 998 octets → reject.
     Errors wrap `ErrIntakeRejected` with reason `invalid header block`. Returns the input slice unchanged when nothing was dropped, else a new slice.
   - `HeaderValue(lines []string, name string) string` (first field, unfolded with single spaces, trimmed; same semantics as `nntp.extractHeaderValue`), `HeaderValues(lines, name) []string` (all copies), `ReplaceHeader(lines []string, name, value string) []string` (replace the first field including its continuation lines with one `Name: value` line, insert at index 0 when absent; the name's spelling of the existing line is kept, `Name` as given otherwise), `RemoveHeaders(lines []string, name string) []string` (drop every copy with continuations).
   - `WireArticleSize(head, body []string) int`: `Σ(len(l)+2)` over head and body + 2 (separator CRLF); dot-stuffing not counted (RFC 3977 §8.1.1 `:bytes` semantics).
   - `GenerateMessageID(identity string) (string, error)` (D4) and `FormatNewsDate(t time.Time) string` (`t.UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")`).
2. `internal/models/models.go`: add to `Article`, after `ProcessQueue`: `Posted bool \`json:"-" db:"-"\` // set by the NNTP POST handler: injected here (proto-article)`; `RecycleArticle` resets it to false.
3. Tests `internal/common/intake_test.go`: tables for `ValidMessageID` (valid, no brackets, two `@`, space, 250/251 octets, literal right part), `ValidGroupNameSyntax`/`IsReservedGroupName` (`comp.lang.go` ok, `Comp.Lang.Go` ok, `ai..dots.`, `a b`, `c++` ok, `&x`, 255/256 octets, `junk`, `control.cancel`, `example.foo`, `to.bob`, `alt.all.x`), `ParseNewsgroups` (`a.b, c.d`, `a.b,,`, `a.b c.d` → error, `a.b;c.d` → error, dupes), `CheckHeaderBlock` (each rule in both modes, including the audit's repro block: two From/Newsgroups/Subject/Message-ID, a `.` line, a colon-less line), `HeaderValue`/`ReplaceHeader`/`RemoveHeaders` with folded lines, `WireArticleSize` (`[]`/`[]` → 2, `["A: b"]`/`["x"]` → 6+2+3). `FuzzIntakeCommonCheckHeaderBlock` (seed corpus only in `go test`): never panics; a strict-mode success contains no line equal to `.`, no C0 control except TAB and no duplicate of the six; a fetch-mode success contains no NUL/CR/LF; fetch output is a subsequence of the input. Add a fetch-mode table case with an ESC-containing ISO-2022-JP `Subject:` that must pass. `FuzzIntakeCommonParseNewsgroups`: every returned name passes `ValidGroupNameSyntax` and contains no `,`.

Acceptance: E01 (all `TestIntakeCommon*` and both fuzz seed runs pass under `-race`). Known-bug set after: unchanged (29 ids).

#### Slice `intake-db` (S2): batch stats UPDATE, NULL-safe group lookup, history gating, duplicate counting
Ids: A-db-1 (alias A-intake-1, upsert part), INT-6, A-db-3, A-db-4.

Owned files:
- `internal/database/db_batch.go` (only the parts listed in K6: `:549-563`, `:794-823`, `:855-933`, `:935-967`, `:969-1001`, `:1003-1103`)
- `internal/database/nntp_intake.go` (new)
- `internal/database/nntp_intake_test.go` (new)

Changes:
1. `nntp_intake.go`:
   ```go
   // IntakeGroup is the main-DB state of a newsgroup that article intake needs. Scanned NULL-safe:
   // it works for rows with a NULL hierarchy (A-db-1) or NULL counters.
   type IntakeGroup struct {
   	ID          int64
   	Name        string
   	Active      bool
   	Status      string // 'y' when NULL
   	MaxArtSize  int64  // 0 = no per-group limit
   	LastArticle int64
   	HighWater   int64
   }
   const query_GetIntakeGroup = `SELECT id, name, active, COALESCE(status,'y'), COALESCE(max_art_size,0),
   	COALESCE(last_article,0), COALESCE(high_water,0) FROM newsgroups WHERE name = ?`
   // GetIntakeGroup returns sql.ErrNoRows (unwrapped, errors.Is works) when no row has that exact name.
   func (db *Database) GetIntakeGroup(name string) (*IntakeGroup, error)
   ```
   via `RetryableQueryRowScan(db.mainDB, query_GetIntakeGroup, []interface{}{name}, …)`; a nil `db.mainDB` returns an error (not a panic).
   Also `func (db *GroupDB) LookupMsgIdInArticlesDB(messageID string) (bool, error)`: same query as `ExistsMsgIdInArticlesDB` (`db_groupdbs.go:336-343`) through the retry helper, but `sql.ErrNoRows` → `(false, nil)` and any other error is returned (A-db-3). `ExistsMsgIdInArticlesDB` stays unchanged for its other callers (`proc_DLArt.go:148`); S3 switches `processArticle` to the new method.
1a. **A-db-3, PHASE 1.** `processNewsgroupBatch` creates `inserted := make(map[*models.Article]struct{}, len(batches))` **before** the `retry1:` label (so a retry after a commit whose follow-up SELECT failed still knows what it inserted) and passes it through `batchInsertOverviews` to `processOverviewBatch` (both unexported; add the parameter). In `processOverviewBatch`: after each `stmt.Exec`, read `RowsAffected()` (checked error); 1 → add the article to `inserted`. In the number-mapping loop (`:1078-1094`) assign a number only to an article that is in `inserted` and still has number 0, and keep scanning (no `break`) past a matching article that is not in `inserted`. Ignored rows (message-id already in the group, or a second copy in the batch) therefore keep number 0, which PHASE 2 already skips (`:1117`, `:1185`, `:1257`) and PHASE 3 treats as not committed. Log the ignored count once per batch: `[OVB-BATCH] group '%s': %d duplicate message-ids ignored`.
2. `query_updateNewsgroupsStats` becomes:
   ```sql
   UPDATE newsgroups SET
   	message_count = COALESCE(message_count, 0) + ?,
   	last_article = CASE WHEN ? > COALESCE(last_article, 0) THEN ? ELSE last_article END,
   	updated_at = CASE WHEN ? = '' THEN updated_at
   	                  WHEN updated_at IS NULL OR ? > updated_at THEN ? ELSE updated_at END
   WHERE name = ?
   ```
   The exec at `:946-947` passes `len(inserted)` (A-db-3: only newly inserted rows count), `maxArticleNum, maxArticleNum`, then the date string three times (`""` when `latestDate` is zero, A-db-4: never write 0001-01-01; `MAX` semantics, so a backfill never moves `updated_at` backwards), then `*task.Newsgroup`. When `len(inserted) == 0` skip the statement entirely (nothing changed). It records `res.RowsAffected()` (checked error) in a variable outside the closure. After a successful update: when 0 rows were affected, log `[BATCH] processNewsgroupBatch newsgroup '%s' has no newsgroups row: %d stored articles not counted (rows are never created by the batch writer)` and skip `HierarchyCache.UpdateNewsgroupStats`; otherwise call it with `len(inserted)` instead of `len(batches)`. Fix the comment above the constant (it describes the function, not the query). The `statsWhat` text uses `len(inserted)`.
3. PHASE 3 (`:855-933`): replace `sq.db.MainDBGetNewsgroup` by `sq.db.GetIntakeGroup`; on error log (as today, with the error) and keep `historyOK=false`. In the per-article loop, read `messageID`, `committed` (number > 0), `msgIdItem` and `DateSent` under the read lock first; then:
   - set the `msgIdItem` to `CaseDupes` for every article with `messageID != ""`, committed or ignored as a duplicate (both are "stored"; INT-6 and A-db-3), outside `if historyOK`;
   - call `AddArticleToHistory` only when `historyOK && committed` (A-db-3: no re-add for ignored duplicates);
   - A-db-4: take `latestDate` from every committed article here, **before** the "still referenced by other groups" `continue` (`:904-908`), and delete the old assignment at `:914-916`. Keep the clearing and the clamp to `now` (`:939-941`) as they are.
4. Tests `nntp_intake_test.go` (no global mutation; an isolated batch like `fuBatchIsolatedDB` whose `mainDB` is set to `w0DB(t).mainDB` and reset to nil in a `t.Cleanup` registered **after** `fuBatchIsolatedDB`, so its `Shutdown` never closes the shared main DB; check `Shutdown` before relying on that), with a fake `ProcessorInterface` `idbProc` that records `AddArticleToHistory` calls:
   - `TestIntakeDBGetIntakeGroup`: row inserted with `hierarchy` NULL, `status` NULL, `last_article` NULL via raw SQL → `GetIntakeGroup` succeeds with `Status=="y"`, zeros; unknown name → `errors.Is(err, sql.ErrNoRows)`.
   - `TestIntakeDBBatchNeverCreatesRow` (A-db-1/SEC-2 DB side): `fuBatchTask` for a fresh name without a row, `fuBatchRun` → no `newsgroups` row afterwards (`SELECT count(*)` = 0), the log line from step 2 present.
   - `TestIntakeDBHistoryForNullHierarchyRow` (INT-6): row with NULL hierarchy, two consecutive batches of 2 articles → `idbProc` saw 4 adds with the row's ID; `message_count` = 4, `last_article` = 4.
   - `TestIntakeDBCaseDupesWithoutRow` (INT-6): batch for a name without a row, articles carry a `history.MessageIdItem` set to `CaseLock` → afterwards every item is `CaseDupes`, `idbProc` saw 0 adds.
   - `TestIntakeDBDuplicatesNotCounted` (A-db-3): row inserted first; batch `A, A, B` → 2 article rows, `message_count` = 2, 2 history adds, 2 `threads` rows; second batch `A` → `message_count` still 2, still 2 history adds, still 2 `threads` rows, A's item `CaseDupes`.
   - `TestIntakeDBUpdatedAtCrosspost` (A-db-4): row with `updated_at = '2026-10-11 10:00:00'`; one article dated 2026-10-12 whose `NewsgroupsPtr` holds this group and one more → afterwards `updated_at = '2026-10-12 …'` (never `0001-01-01`); a later batch with an article dated 2020 leaves it unchanged.
   - `TestIntakeDBLookupMsgId` (A-db-3): `LookupMsgIdInArticlesDB` → `(true, nil)` for a stored id, `(false, nil)` for an unknown one, an error after the group DB was force-closed.

Acceptance: E02 (the seven tests pass; `TestFuBatch*`, `TestLo2Writer*` unchanged and green). Known-bug set after: unchanged.

#### Slice `intake-dlart` (S5): per-run return queue for DownloadArticles
Ids: A-intake-11.

Owned files:
- `internal/processor/proc_DLArt.go`
- `internal/processor/intake_dl_test.go` (new)

Changes:
1. `newDownloadRun() *GroupBatch` returns `&GroupBatch{ReturnQ: make(chan *BatchItem, MaxBatchSize)}`. `DownloadArticles` uses it instead of `Batch.GetOrCreateGroupBatch(newsgroup)` (`:80`). Keep `GetOrCreateGroupBatch` and `GroupQueues` (exported; add `// Deprecated: DownloadArticles uses a per-run queue (A-intake-11)`).
2. A per-run `runDone := make(chan struct{})`. Register `defer close(runDone)` **after** the deferred `ForceCloseGroupDB` at `:102`, so (LIFO) `runDone` closes before the group DB is force-closed. The feeder goroutine (`:131-192`) does every send to `groupBatch.ReturnQ` and to `Batch.GetQ` inside `select { case …: case <-runDone: }`; once `runDone` is closed it stops queueing but keeps ranging over `xhdrChan` and discarding entries until the channel closes, because `XHdrStreamedBatch` blocks on a full `xhdrChan` and has no stop channel of its own. The final `releaseChan`/`notifyChan` sends are already buffered (cap 1) and stay. The feeder must not call `groupDB.ExistsMsgIdInArticlesDB` after `runDone` (the DB may be closed).
3. Count items handed to `Batch.GetQ` or `ReturnQ` (`queued` + dups, atomic) and items received in the processing loop; on every return path after the feeder started, when handed > received, start `go drainLateResults(run.ReturnQ, handed-received, shutdownChan)`: receive and drop (resetting each item's msgid cache entry to `CaseError` with a 5 s TTL as the loop does at `:253-262`) until the count is reached or `shutdownChan` closes. The goroutine is bounded by the outstanding count and the shutdown channel (CLAUDE.md goroutine rule).
4. No change to the progress update (`:316-322`): the timeout path already returns before it.
5. Tests `intake_dl_test.go`: if practical, extract the feeder loop into a function taking `xhdrChan`, `runDone` and the send targets so `TestIntakeDLFeederDrainsAfterDone` can check that, after `runDone` closes, the feeder still consumes every remaining `xhdrChan` entry and returns when it closes; `TestIntakeDLRunQueuesAreFresh` (two `newDownloadRun()` calls give different channels, cap `MaxBatchSize`); `TestIntakeDLDrainLateResults` (3 outstanding, 3 items sent late → returns; 3 outstanding, 1 sent, shutdown closed → returns within 1 s; no goroutine left, checked with a done channel).

Acceptance: E03 (both tests pass; `go vet` clean). Known-bug set after: unchanged. Note for audit-delta-fixes: A-tools-4 is resolved by this slice.

### Wave 2 (1 `pugleaf-implementer` slice, from the merged wave 1)

#### Slice `intake-processor` (S3): article-layer validation, group policy, dedupe, Path sync
Ids: SEC-2 (intake half), A-intake-8, A-intake-2, A-intake-3, INT-2, INT-3, INT-4, A-intake-5, A-intake-6 (intake half), A-intake-7, A-intake-9, A-tools-1.

Owned files:
- `internal/processor/threading.go`
- `internal/processor/proc-utils.go`
- `internal/processor/PostQueue.go`
- `internal/processor/intake.go` (new)
- `internal/processor/intake_test.go` (new)
- `internal/processor/wiring_test.go` (only `TestWiringProcessArticleIntakeCheck`)
- `cmd/nntp-server/server_e2e_test.go`

Changes:
0. **Ask Q3 (control messages) before this slice starts**; D3 is implemented here.
1. `proc-utils.go`:
   - `bruteForceDateParseOK(dateStr string) (time.Time, bool)`: the body of `bruteForceDateParse` (`:587-791`) with flags `yearFound`/`monthFound` set where the parser actually extracts them; `ok` is false when either was defaulted (`:757-765`). `bruteForceDateParse` becomes `t, _ := bruteForceDateParseOK(s); return t` (results unchanged). The internal 25 h bound at `:780` stays (K1).
   - `var MaxFutureDate = 24 * time.Hour` (A-intake-7; replaces the literal 25 h at `threading.go:163` only).
   - `ParseNNTPDateStrict(dateStr string) (time.Time, bool)`: the `ParseNNTPDate` pipeline (`:502-559`), returning `ok=true` for a layout match or a brute-force result with year and month found; the rslight epoch fallback is not used. `ParseNNTPDate` is unchanged for all inputs (K1).
   - `extractGroupsFromHeaders` (`:414-454`): call `GetNewsgroupPointer` only for names that pass validation (move `:423` below the checks; the rslight branch needs the pointer, so get it there).
2. `intake.go` (new):
   - `intakeGroupDecision(g database.IntakeGroup, posted bool) (keep bool, moderated bool)`: D1/D2 table (inactive → drop; status `y` keep; `m` keep + moderated; `n` keep unless posted; anything else drop). Pure.
   - `(proc *Processor) resolveIntakeGroups(names []string, posted, approved bool, wireSize int) (keep []*database.IntakeGroup, err error)`: for each name: `IsReservedGroupName` → drop; `GetIntakeGroup` → `sql.ErrNoRows` drop, other error → `fmt.Errorf("%w: newsgroup lookup: %v", common.ErrIntakeTemporary, err)`; decision; per-group `MaxArtSize > 0 && wireSize > MaxArtSize` → drop. Any kept moderated group with `!approved` → `ErrIntakeRejected` "moderated group without Approved". Empty result → `ErrIntakeRejected` "no valid newsgroup". Log each drop once with the `[INTAKE]` prefix (name, reason).
   - `releaseClaim(item *history.MessageIdItem)` (K5).
   - A-tools-1 limiter (spec from audit-delta-fixes hand-off 1):
     ```go
     // captureLimiter bounds the goroutines parked in BatchCaptureOverviewForLater (A-tools-1).
     type captureLimiter struct{ slots chan struct{} }
     func newCaptureLimiter(n int) *captureLimiter // n < 1 → 1
     // acquire returns true when a slot was taken. wait=false never blocks (overflow allowed, counted,
     // one log line per minute "[INTAKE] capture limiter full: %d overflow"). wait=true blocks until a slot
     // is free, polling stop() every 100 ms; when stop() reports true it returns false (fall back to unbounded).
     func (l *captureLimiter) acquire(wait bool, stop func() bool) bool
     func (l *captureLimiter) release() // non-blocking receive
     var MaxCaptureInFlight = 2048      // read once, at first use
     var defaultCaptureLimiter *captureLimiter // built with sync.Once from MaxCaptureInFlight
     func captureLimit() *captureLimiter        // returns defaultCaptureLimiter
     ```
     The overflow counter is an `atomic.Int64`; the once-per-minute log uses an `atomic.Int64` Unix timestamp (no goroutine). Tests use `newCaptureLimiter` directly, never `MaxCaptureInFlight` (no global mutation).
   - `validateIntakeHeaders(lines []string, messageID string) (normalized []string, approved bool, err error)` for strict mode: `common.CheckHeaderBlock(lines, HeaderBlockStrict)`; the six mandatory headers present and non-empty (RFC 5536 §3.1; reason `missing mandatory header`); `common.HeaderValue(lines,"Message-ID") == messageID && common.ValidMessageID(messageID)` (reason `invalid message-id`); a `Control:` header → reject with D3's log line; `Supersedes:` → D3 log line only; `approved = HeaderValue(lines,"Approved") != ""`.
   - `stripXrefLines(lines []string) []string` (= `common.RemoveHeaders(lines, "Xref")`) and `syncStoredPath(lines []string, path string) []string` (replace the existing Path line's value with `path`; return the input unchanged when the block has no Path line).
3. `threading.go` `processArticle`:
   - `:59-61`: an empty id or nil article → `CaseError, fmt.Errorf("%w: invalid message-id", common.ErrIntakeRejected)`.
   - `:76-81` (INT-2): `CaseLock`/`CaseWrite` → `return history.CaseRetry, nil`; `CaseDupes` → `return history.CaseDupes, nil`.
   - `:86-94` history error: `releaseClaim`, `return history.CaseRetry, fmt.Errorf("%w: history: %v", common.ErrIntakeTemporary, err)`.
   - Non-bulk branch (`:123-137`) is replaced by: `lines := article.NNTPhead` (fallback `strings.Split(article.HeadersJSON, "\n")` when nil); `validateIntakeHeaders`; `names, err := common.ParseNewsgroups(common.HeaderValue(lines,"Newsgroups"))`; `len(names) > MaxCrossPosts` → reject; dates (step below); `groups, err := proc.resolveIntakeGroups(names, article.Posted, approved, common.WireArticleSize(article.NNTPhead, article.NNTPbody))`; `newsgroups` = the kept names. Every rejection keeps `setCaseDupes` and returns `CaseError` with an `ErrIntakeRejected`-wrapped reason; a temporary error from `resolveIntakeGroups` calls `releaseClaim` and returns `CaseRetry`.
   - Bulk branch (`:101-122`): before the existing checks run `common.CheckHeaderBlock(strings.Split(article.HeadersJSON, "\n"), HeaderBlockFetch)` (skip when `HeadersJSON == ""`); on error `setCaseDupes` + `CaseError` (rejected); otherwise store the normalized lines back into `HeadersJSON`. If the block has a `Message-ID:` line whose value differs from `article.MessageID` → reject (A-intake-3 fetch part). Group handling stays as today (D12; bulk keeps the lax name rules and the fetched group).
   - Dates (`:155-168`): non-bulk: `t, ok := ParseNNTPDateStrict(article.DateString)`; `!ok` → reject `invalid date`; the future check uses `ParseNNTPDateStrict(HeaderValue(lines,"Injection-Date"))` when ok, else `t`, against `time.Now().Add(MaxFutureDate)`. Bulk keeps `ParseNNTPDate` and the zero check, with `MaxFutureDate`. Remove the `TODO: add article cutoff` comment (D10: the POST past cutoff lives in S4).
   - Path (`:185-192`) keeps its column logic. Then store the block: non-bulk `HeadersJSON = strings.Join(stripXrefLines(lines), "\n")` (the strict-validated block, no Path rewrite: S4's injection/relay step puts the prefixed Path into the lines before the processor sees them, so the column then equals the line); bulk `HeadersJSON = strings.Join(syncStoredPath(stripXrefLines(normalized), article.Path), "\n")`, so the fetcher's stored Path line gains the `<identity>!` prefix (D7). Without a path identity (only test servers) a non-bulk column and line can differ; that is accepted.
   - `:200-205` (A-intake-5): replace the loop that nils every value of the caller's map with `article.Headers = nil` (drop the reference only).
   - `:240` and the commented block `:248` (A-db-3): use S2's `groupDB.LookupMsgIdInArticlesDB`; an error counts the group like a failed `GetGroupDB` (not opened → may end in the temporary `CaseRetry` below) instead of "not stored".
   - A-tools-1 (one capture slot per article, both modes):
     - At the top of `processArticle`, after the nil/empty-id check and **before** `article.Mux.Lock()` (`:68`): `acquired := captureLimit().acquire(true, proc.DB.IsDBshutdown)`. Bulk and non-bulk both wait, so POST/IHAVE/TAKETHIS connections get back-pressure too (the unauthenticated-reachable surface). A waiting producer holds no article mutex, so there is no lock cycle with `BatchCaptureOverviewForLater`/`BatchDivider`. `acquire` returning false (shutdown) means "proceed without a slot".
     - Every return path that does not hand the article to the capture goroutine releases the slot (`if acquired { captureLimit().release() }`); implement it as a `handedOff` flag checked in one deferred function registered right after `acquire`, so no early return can leak a slot.
     - Before the group loop, under the lock, reset `article.NewsgroupsPtr = article.NewsgroupsPtr[:0]`: `buildIncomingArticle` (`nntp-cmd-posting.go:210-215`) pre-fills it with every header group, which would otherwise keep unknown or skipped groups in the list and stop the batch from ever clearing the article.
     - In the group loop (`:211-268`): append `newsgroupPtr` to `article.NewsgroupsPtr` only for groups that will be captured (after `GetGroupDB` and `LookupMsgIdInArticlesDB` succeeded and the article is not stored there yet), and collect them in a local `capture []*string`. All appends happen while `processArticle` still holds `article.Mux`. Groups that are skipped are no longer left in `NewsgroupsPtr` for good (A-db-4's root in `threading.go:220`).
     - After the loop, when `len(capture) > 0`, set `handedOff = true` and start **one** goroutine per article:
       ```go
       go func(ptrs []*string, acquired bool) {
       	if acquired {
       		defer captureLimit().release()
       	}
       	for _, ptr := range ptrs {
       		proc.DB.Batch.BatchCaptureOverviewForLater(ptr, article)
       	}
       }(capture, acquired)
       ```
       It starts its work only after `processArticle` returns and unlocks `article.Mux`. `ProcessQueue` has capacity 16 and `len(capture) <= MaxCrossPosts` (9), so the sends to `ProcessQueue` never block. Because every target pointer is already in `NewsgroupsPtr` before the first capture, no group's PHASE 3 can clear the article (`db_batch.go:904-928`, which sets `ProcessQueue = nil`) before the last capture has been sent. That closes today's race, where a late crosspost capture could send on the nil `ProcessQueue` and block forever.
     - The call cannot become synchronous (deadlock on `article.Mux`, see the A-tools-1 row). `GroupCounter.Increment` stays in the loop.
   - `:211-283` (INT-2, A-intake-9): non-bulk takes the kept `*IntakeGroup` list (so `GetGroupDB` runs only for resolved names). Track `queued`, `opened` (GetGroupDB ok) and `haveID` (ID of the first group whose `ExistsMsgIdInArticlesDB` is true; bulk uses 0). After the loop: `queued > 0` → `CasePass`; else if a group already had it (`alreadyStored`) → non-bulk: `proc.AddArticleToHistory(article.MessageID, haveID)` when `haveID > 0` (self-heal; history then answers 435 for later offers), `setCaseDupes`, return `CaseDupes, nil`; bulk: `setCaseDupes`, return `CasePass, nil` exactly as today (K1/M3: `rslight.go:347` errors on anything but `CasePass`; the fetcher and PostQueue only test `err`); else (no group DB opened, both modes) → `releaseClaim`, return `CaseRetry, fmt.Errorf("%w: no group database could be opened", common.ErrIntakeTemporary)` (bulk callers then count an error instead of a silent success). No change to `rslight.go`.
   - Delete the dead `CheckMessageIdFormat`/`containsAtAndDot` only if no caller remains outside comments (`proc_DLArt.go:141` is a comment); otherwise leave them.
4. `PostQueue.go`: `pre_processArticle` gives each copy its own map (`Headers: maps.Clone(article.Headers)`, `:96`). `parseNewsgroups` (`:164-179`) stays a split-only parser (comma + trim), unchanged: the web validated the names, and a strict parser must never drop a web post silently. INT-3's "three splitters" is resolved by the two intake splitters (S3 processor, S4 nntp) both using `common.ParseNewsgroups`; the web queue keeps its documented split. Add a comment saying so.
4a. `wiring_test.go` `TestWiringProcessArticleIntakeCheck` (`:115-121`): `CaseLock`/`CaseWrite` now expect `CaseRetry, nil`, `CaseDupes` still `CaseDupes, nil` (split the loop into two tables).
5. `intake_test.go` (no DB): `TestIntakeProcParseNNTPDateStrict` (RFC 5322 date ok; `garbage`, `Tuesday 7pm` → `!ok`; for both `ParseNNTPDate` still returns its old value 1990-01-01 12:00 UTC: regression guard for K1), `TestIntakeProcGroupDecision` (D1/D2 table incl. `posted` × status `n`), `TestIntakeProcSyncPathAndXref`, `TestIntakeProcFutureMargin` (24 h boundary with a fixed `now` passed in: if the date check needs a clock, give the helper a `now time.Time` parameter instead of mutating a global), `TestIntakeProcCaptureLimiter` (A-tools-1: `newCaptureLimiter(2)`: two `acquire(true, …)` succeed; a third `acquire(true, …)` blocks until `release()`; it returns false within 200 ms once `stop` reports true; `acquire(false, …)` on a full limiter returns false at once and counts an overflow; no goroutine left, checked with done channels; `newCaptureLimiter(0)` has one slot). The processArticle wiring is covered end to end: `TestE2ECrosspost` (one goroutine captures both groups) and a new `TestE2ECaptureSlotReleased` in `server_e2e_test.go` that, with the default limiter, posts a rejected article (unknown group), a duplicate and an accepted crosspost and then checks that `captureLimit()` reports all slots free again within `e2eCommitWait` (through an exported `CaptureInFlight() int` in `intake.go` that returns `len(defaultCaptureLimiter.slots)`, reserved in K2; package main cannot read unexported names).
6. `cmd/nntp-server/server_e2e_test.go` — remove the markers this slice fixes, keep their assertions:
   - `TestE2EIHaveDuplicateNotInHistory`: drop `e2eKnownBug(t, "INT-2", bugErr)`; `t.Fatal` when `code != 437`; then, within `e2eCommitWait`, `IHAVE <id>` must answer 435 (history self-heal).
   - `TestE2ERejectUnknownGroup`: drop `e2eKnownBug(t, "SEC-2", bugErr)`; `t.Fatal` on any collected error.
   - `TestE2EDotHeaderLine` (`:1304-1330`): hardening has already removed the A-nntp-rfc-2 marker (its served-side stuffing makes the 240 branch pass). S3 changes the expectation to 441 only (A-intake-2: refused at intake); the 240 branch becomes a `t.Fatalf` with the served bytes. If the marker is unexpectedly still there at the S3 base, remove it in the same edit and say so in the report.
   - New tests (prefix `TestE2E`, groups `t7.*`, using `e2eGroup`-style inserts with explicit `Active`/`Status`; add a helper `e2eGroupWith(t, test, active bool, status string, maxArtSize int)` in this file):
     - `TestE2EHeaderBlockRejects` (A-intake-2): POST with two `Subject:` → 441; with a colon-less line → 441; with a NUL byte in a header → 441; IHAVE with two `From:` → 335 then 437 (current posting.go maps `CaseError` to 436 — so assert `code == 437 || code == 436` here and leave the strict 437 assertion to S4's update, see S4 step 16).
     - `TestE2EGroupPolicy` (SEC-2, A-intake-8, D1/D2/D8): inactive group → POST 441; status `x` → 441; crosspost `<known>,t7.nosuch.<seq>` → 240 and stored only in the known group, no row/file for the unknown name; Newsgroups `a b` (space separated) → 441; reserved `junk` alone → 441, no `junk` row; moderated without `Approved:` → 441, with `Approved: x` → 240; IHAVE to a status `n` group → 235 (POST to `n` is S4's, it needs `Posted`).
     - `TestE2ENoNullHierarchyRow` (A-db-1 end to end): after a POST to an existing group and one to an unknown name, `e2eDB.MainDBGetAllNewsgroups()` returns no error.
     - `TestE2EHistoryAfterIntake` (INT-6): insert the group row with raw SQL and `hierarchy` NULL (`INSERT INTO newsgroups(name,description,last_article,message_count,active,status,low_water,high_water) VALUES(?, '', 0, 0, 1, 'y', 1, 0)` on `e2eDB.GetMainDB()`), POST two articles (two batches) → within `e2eCommitWait` `e2eHist.Exists(id)` is true for both and `IHAVE <id>` → 435.
     - `TestE2EMessageIDSyntax` (A-intake-3): POST with `Message-ID: no-brackets@x` → 441; with a 251-octet id → 441; nothing stored (`e2eStored` errors).
     - `TestE2EDateChecks` (INT-3, A-intake-7): POST `Date: garbage` → 441; `Date` = now+24h30m → 441; `Date` = now+23h → 240.
     - `TestE2EControlXref` (INT-4, A-intake-6): POST with `Control: cancel <x@y>` → 441; with `Supersedes: <x@y>` → 240; with a foreign `Xref: other.example g:1` → 240 (S4 will make POST with Xref a 441, so send this one by IHAVE: 335 → 235) and the stored `headers_json` has no `Xref:` line (read the row with raw SQL like `e2eStored`). The "Path line = path column" assertion is S4's (`TestE2EInjectionPathColumn`).
     - `TestE2EInFlightRetry` (INT-2 in flight): `IHAVE <id>` → 335; then set `history.MsgIdCache.GetORCreate(id)` to `CaseLock` (lock its `Mux`); send the article → 436; reset the item's `Response` to 0 and IHAVE again → 335 → 235.
     - `TestE2EGroupDBOpenFailure` (A-intake-9): insert an active group whose name is exactly 255 octets (`t7.dbfail.` + `a`×245; the group file name exceeds NAME_MAX) → POST 441, IHAVE 436, nothing stored, `e2eHist.Exists` false.
     - `TestE2EWebCrosspost` (A-intake-5): two `t7` groups; `w := e2eProc.NewPostQueueWorker(); w.Start(); defer w.Stop()`; send an article built like `web/web_sitePostPage.go:358-390` (`HeadersJSON` with the web's header lines incl. the folded `X-pugleaf-Trace`, `Headers["newsgroups"] = "g1,g2"`, `Path: ".POSTED!not-for-mail"`) to `models.PostQueueChannel`; poll `e2eStored(id, g1, g2)` → stored in both.

Acceptance: E04 (`TestIntakeProc*` pass), E05 (the e2e tests above pass; all pre-existing `TestE2E*` still pass), E06 (`TestFuBatch*`/`TestLo2Writer*`/`TestReuse*`/`TestWiring*` in `internal/processor` unchanged and green).
Known-bug set after S3: the set recorded at the integration-branch base (hardening's remaining set, see `## Checks`) minus INT-2; SEC-2 disappears only if hardening already closed `TestGrpListGroupUnknownNoFile`. In the "this plan only" accounting of `## Checks`: 27 ids = AUDIT §10's 29 minus INT-2 and A-nntp-rfc-2 (`A-nntp-rfc-3 A-nntp-sec-1 BUG-1 BUG-2 BUG-4 BUG-5 BUG-6 BUG-7 BUG-8 BUG-9 INT-1 RFC-1 RFC-2 RFC-3 RFC-4 RFC-5 RFC-6 SEC-1 SEC-2 SEC-3 SEC-4 SEC-5 SEC-6 SEC-7 T-nntp-article-1 T-nntp-auth-1 T-nntp-group-1`). `TestE2EPost`'s INT-1 marker must still be open after S3 (S3 does not rewrite the Path line of non-bulk articles); if it turns fatal, stop and report instead of removing it.

### Wave 3 (1 `pugleaf-implementer` slice, from the merged wave 2)

#### Slice `intake-nntp` (S4): reader wiring, injection, relay Path, reply codes
Ids: INT-1, A-intake-4, SEC-3 (intake side), BUG-7 ("too large" part), RFC-5 (all codes in `nntp-cmd-posting.go`), A-nntp-rfc-7, A-nntp-rfc-8.

Owned files:
- `internal/nntp/nntp-cmd-posting.go`
- `internal/nntp/nntp-intake_test.go` (new)
- `internal/nntp/nntp-wiring_test.go`
- `internal/nntp/nntp-server-intake-fuzz_test.go`
- `internal/nntp/nntp-server-auth_test.go` (only the `TestAuthPostingGates/no-posting-post` hunk, `:300-307` at 03d5ef7; hardening agreed)
- `internal/nntp/nntp-server-article_test.go` (only if the `knownBug(t, "BUG-8", …)` in `TestArtLocal430/ihave-arrival` is still present at the S4 base and the `Remove` call below makes it pass: remove that call, keep the assertion)
- `cmd/nntp-server/server_e2e_test.go`
- `cmd/nntp-server/e2e_binary_test.go`
- `scripts/test-nntp-server.sh`

Changes (all in `nntp-cmd-posting.go` unless stated). First read hardening's merged `readArticleBlock`, `ErrArticleTooLarge`, `ErrArticleDrainLimit`, `(*NNTPServer).MaxArticleBytes()` and `(*Local430).Remove` and use their exact signatures; do not re-implement the per-line deadline refresh or the 16384-line secondary cap (they are in hardening's reader).
1. **Names.** `var PostDateMaxAge = 72 * time.Hour`, `const closeDrainTimeout = time.Second`, `type intakeKind int` with `intakePost`, `intakeIHave`, `intakeTakeThis`.
2. **Identity.** `(c *ClientConnection) pathIdentity() string` returns `c.server.Config.Hostname` (`""` when `Config` is nil). The value comes from hardening's `Hostname` line in `cmd/nntp-server/main.go`; no `cmd/web` edit (audit-delta-fixes deletes its dead embedded-NNTP block).
3. **Reading (SEC-3, BUG-7).** POST/IHAVE/TAKETHIS read with hardening's `readArticleBlock(c.server.MaxArticleBytes())`. `ErrArticleTooLarge` → the per-command "too large" reply below (the stream is in sync, the connection stays open); `ErrArticleDrainLimit` → `closeAfterReply(400, "Article too large, closing connection")`; other errors are I/O errors: return them without writing. If hardening left `readArticleLines`/`readArticleData` in this file, delete them now (all callers use the new flow); the `c.textConn.Close()` at `:227-230` must be gone. `readArticleData()` (`:194-202`) is deleted: POST/IHAVE call the reader, then `injectProtoArticle`/`relayPath`, then `buildIncomingArticle` directly.
4. **`closeAfterReply(code int, text string) error`** per the Component design.
5. **`intakeReply(kind intakeKind, code int, err error, messageID string) (reply int, text string, closeConn bool)`** implements K4 exactly (reason = the text after the first `": "` of an `ErrIntakeRejected` error, else the K4 default). Handlers call `c.closeAfterReply` when `closeConn`, else `sendResponse`. After every accept (240/235/239) call `c.server.local430.Remove(<message-id>)` (nil-safe on `local430`; BUG-8 audit fix, hardening's `Remove`).
6. **POST** (`:14-47`, RFC-5 gates included): no processor → `440 Posting not permitted (no article processor)`; unauthenticated → 480 (unchanged); no posting permission → `440 Posting not permitted for this user`; 340; read (too large → `rateLimitOnError`, 441 `Posting failed (article too large)`); `head, err = c.injectProtoArticle(head)` → 441 `Posting failed (<reason>)`; `buildIncomingArticle` error → 441 `Posting failed (invalid article)`; `article.Posted = true`; reply via `intakeReply(intakePost, …)`.
7. **`injectProtoArticle(head []string) ([]string, error)`** (INT-1, A-intake-4, D4–D7, D10): identity required; reject (reason in parentheses) `Injection-Info` present (`forged trace`), `Xref` present (`forged trace`), Path value containing `.POSTED` (`forged trace`), `Injection-Date` present while Message-ID or Date is missing (`invalid injection date`); Message-ID present → `common.ValidMessageID` (`invalid message-id`), absent → `common.GenerateMessageID(identity)`; Date present → `mail.ParseDate` (`invalid date`), > now+24 h (`date in the future`), < now−`PostDateMaxAge` (`date too old`); absent → `common.FormatNewsDate(now)`; if either was generated append `Injection-Date: <FormatNewsDate(now)>`; append Injection-Info (D6; call `c.server.DB.ComputeHashedUsername` only when `c.user != nil` and `c.server.DB` is non-nil, and treat `err != nil || hash == ""` as "no posting-account": the function returns `"", nil` when the salt is empty); Path via `common.ReplaceHeader(head, "Path", identity+"!.POSTED!"+old)` (`old` = `not-for-mail` when absent). Errors wrap `common.ErrIntakeRejected`.
8. **IHAVE** (`:50-111`): no processor → `500 IHAVE not supported on this server` (RFC-5; IHAVE is not offered without a processor); argument and auth gates unchanged (`501`, `480`, `502`); after `messageID := args[0]`: `!common.ValidMessageID` → `rateLimitOnError`, `501 Syntax error in message-id` (A-nntp-rfc-7). Offer-stage switch unchanged. After 335: read (over-drain → 400 close; too large → 437 `Rejected (article too large)`; I/O → return); `head = c.relayPath(head)`; build error → 437 `Rejected (invalid article)`; `reconcileMessageID` false → 437; reply via `intakeReply(intakeIHave, …)`.
9. **`reconcileMessageID`** returns `article.MessageID == cmdMessageID` (a missing header is now a mismatch: RFC 5537 §3.6 step 1). Comment updated.
10. **`relayPath(head []string) []string`**: identity empty or no Path line → unchanged; else `ReplaceHeader(head, "Path", identity+"!.SEEN."+peerIP+"!"+old)` with `peerIP` from `net.SplitHostPort(c.conn.RemoteAddr().String())` (nil-safe; `unknown` when it fails).
11. **TAKETHIS** (`:124-182`, A-nntp-rfc-8): compute `id` = `args[0]` if any; `allowed := Processor != nil && len(args)==1 && c.authenticated && (c.user == nil || c.user.Posting) && common.ValidMessageID(id)`; always read the article with `readArticleBlock(c.server.MaxArticleBytes())` (bounded; when `!allowed` the lines are dropped right away, so an unauthenticated client never makes the server keep an article beyond the read); then in order: drain limit → `closeAfterReply(400, …)`; I/O → return; no processor → `500 <id> TAKETHIS not supported on this server` (RFC-5); bad args → `501 <id> TAKETHIS requires exactly one message-id`; unauthenticated → `480 <id> Transfer permission denied`; no posting → `502 <id> Transfer not permitted`; invalid id → `439 <id> Rejected (invalid message-id)`; too large → `439 <id> Rejected (article too large)`; offer check: Dupes → `439 <id> Not wanted`, Retry → `439 <id> In progress`, Error → `closeAfterReply(400, "Service temporarily unavailable")`; `relayPath`; build/reconcile → 439; process → `intakeReply(intakeTakeThis, …)`. Every `rateLimitOnError` call stays where it is today.
12. **`parseIncomingArticleLines`** (`:282-292`): groups from `common.ParseNewsgroups(common.GetHeaderFirst(article.Headers, "newsgroups"))`; on error return it (wrapped). `buildIncomingArticle` therefore only calls `GetNewsgroupPointer` for syntactically valid names (INT-3 call site; the INT-3 id belongs to S3).
13. **Tests — `internal/nntp/nntp-intake_test.go`** (net.Pipe like `wiringTestConn`, a `*config.ServerConfig` with `NNTP.MaxArtSize` set, no global mutation): `TestIntkTooLargeReplies` (POST/IHAVE/TAKETHIS of a 3 KB article with `MaxArtSize` 1 KB → 441 / 437 / `439 <id>` and the next command on the same pipe is answered: the handlers keep the stream in sync on hardening's `ErrArticleTooLarge`); `TestIntkReply` (every K4 row × kind); `TestIntkInjectProtoArticle` (each reject reason; generation adds Message-ID/Date/Injection-Date; with both present no Injection-Date; Path rewrite with/without proto Path; Injection-Info without DB/user is `Injection-Info: <identity>`); `TestIntkRelayPath`; `TestIntkCloseAfterReply` (client reads the reply, then EOF; no panic on a second command already buffered).
14. **`nntp-wiring_test.go`**: `TestWiringIHaveTransfer`: `no-message-id-uses-argument` → want 437, `wantProcessed=false` (rename the case to `no-message-id-rejected`); add `rejected` (`processResult: CaseError`, `processErr: fmt.Errorf("%w: no valid newsgroup", common.ErrIntakeRejected)`) → 437; `in-flight` (`CaseRetry`, nil) → 436; `temporary` (`CaseRetry`, `ErrIntakeTemporary`) → 436; `processing-error` (`CaseError`, nil) stays 436. `TestWiringTakeThis`: `no-message-id-uses-argument` → 439, not processed; `history-error` → 400 and the connection closed (assert with `wiringTestNoExtraReply`); `no-processor` → 500 (was 502); add `processing-temporary` → 400, `processing-rejected` → 439; extend the id check to 480/500/501/502 replies. If any wiring case builds a server without `Config`, `MaxArticleBytes()` must still work (hardening's nil handling; check, and set a `&config.ServerConfig{}` in `wiringTestConn` if needed). Add `TestWiringIHaveInvalidMessageID` (`IHAVE not-a-message-id` → 501, `CheckMessageID` not called).
14a. **`nntp-server-auth_test.go`** `TestAuthPostingGates/no-posting-post`: drop `knownBug(t, "RFC-5", hExpectCode(code, line, 440))`, keep the assertion as `if err := hExpectCode(code, line, 440); err != nil { t.Fatal(err) }`.
15. **`nntp-server-intake-fuzz_test.go`**: in the fuzz body assert every returned group passes `common.ValidGroupNameSyntax`; add seeds `"Newsgroups: a.b c.d"` and `"Newsgroups: ai..dots."` (both must error).
16. **`cmd/nntp-server/server_e2e_test.go`** — remove the markers this slice fixes, keep the assertions:
    - `TestE2EPost`: drop `e2eKnownBug(t, "INT-1", pathErr)`; `t.Fatal(pathErr)` when non-nil; add: the served Path starts with `e2eHostname + "!.POSTED!"`, an `Injection-Info:` header starting with `e2eHostname` is served, no `Injection-Date:` (both Message-ID and Date were sent).
    - `TestE2EPostGeneratedHeaders/no-message-id` and `/no-date`: drop both `e2eKnownBug(t, "INT-1", err)`; `t.Fatal(err)`; add: `Injection-Date:` present and parseable.
    - `TestE2ERejectMissingNewsgroups/ihave`: drop `e2eKnownBug(t, "RFC-5", bugErr)`; require exactly 437.
    - `TestE2ESizeLimits/bytes-over-MaxArtSize`: drop `e2eKnownBug(t, "SEC-3", bugErr)`; require 441, then `MODE READER` 200/201.
    - `TestE2ESizeLimits/lines-20000`: drop `e2eKnownBug(t, "BUG-7", err)`; `if err != nil { t.Fatal(err) }` (the child's three `e2eBug7Marker` assertions stay and must pass: a 441 status, a usable connection, no write to a closed socket). Update the child's trailing "Note for the fix plan" comment to describe the drain.
    - `TestE2EHeaderBlockRejects`: tighten the IHAVE assertion added by S3 to exactly 437.
    - New: `TestE2EInjectionPathColumn` (for a POST and for an IHAVE, the stored `path` column equals the stored and served `Path:` value; D7); `TestE2EProtoArticleRejects` (POST with `Injection-Info:` → 441; with `Xref:` → 441; Path `x!.POSTED!y` → 441; Date 73 h old → 441; Date 71 h old → 240); `TestE2ERelayPath` (IHAVE: served Path starts with `e2eHostname + "!.SEEN.127.0.0.1!"`); `TestE2EIHaveInvalidMessageID` (`IHAVE not-a-message-id` → 501; pipelined `TAKETHIS not-an-id` + article → `439 not-an-id …`, then a valid TAKETHIS in the same stream → 239); `TestE2EMissingMessageIDHeader` (IHAVE `<id>` with an article lacking `Message-ID:` → 437, not stored); `TestE2EOversizeFeed` (IHAVE of a 64 KiB article → 437 then `MODE READER` works; TAKETHIS of a 64 KiB article pipelined with a small valid TAKETHIS → `439 <id1>` then `239 <id2>`); `TestE2EPostNoPostingStatus` (POST to a status `n` group → 441; IHAVE to it → 235); `TestE2ETakeThisUnauthenticatedCarriesID` (unauthenticated TAKETHIS → `480 <id> …`).
17. **`cmd/nntp-server/e2e_binary_test.go`**: add `TestE2EBinaryIntake` (same `e2eReport` helpers, env `PUGLEAF_E2E_ADDR/USER/PASS`, plus `PUGLEAF_E2E_HOST` (identity, default `example.org`) and the groups `e2e.intake.gen` / `e2e.intake.mod`) printing the N40–N49 lines of `## End-to-end`. Leave `TestE2EBinary` (N01–N11) unchanged.
18. **`scripts/test-nntp-server.sh`**: in setup step 2 add a second `INSERT` for `e2e.intake.gen` (status `y`) and `e2e.intake.mod` (status `m`) with `hierarchy='e2e'`; after the N01–N11 block add a `# ---- N40-N49 (nntp-intake-injection)` block that runs `go test -tags e2e -count=1 -v -run '^TestE2EBinaryIntake$' ./cmd/nntp-server/` with `PUGLEAF_E2E_HOST="$NNTPHOST"`, counts its PASS/FAIL/INFO lines like the N01–N11 block (expect 9 checks N40, N42–N49), then does the N41 shell check. Keep the other blocks untouched (hardening, merged first, already turned N20–N22 into PASS checks; add the new block after its version of the script). Update the header comment's "Checks:" line.

Acceptance: E07 (`TestIntk*`, `TestWiring*` pass), E08 (all `TestE2E*` pass), E09 (`TestAuthPostingGates` with its RFC-5 marker removed, `TestArtLocal430` and the rest of `./internal/nntp/...` green; no other harness test edited), E10 (`bash -n scripts/test-nntp-server.sh`).
Known-bug set after S4 (= after this plan): see `## Checks`.

---

## Acceptance checks (summary)
| Check | Slice | What |
|----|----|----|
| E01 | S1 | `go test -race ./internal/common/...` passes incl. both fuzz seed corpora |
| E02 | S2 | `TestIntakeDB*` pass; no batch-created `newsgroups` row; history adds for NULL-hierarchy rows; CaseDupes without a row; duplicates not counted (A-db-3); `updated_at` never 0001-01-01 or backwards (A-db-4) |
| E03 | S5 | `TestIntakeDL*` pass |
| E04 | S3 | `TestIntakeProc*` pass incl. `TestIntakeProcCaptureLimiter`; `ParseNNTPDate` results unchanged for the table inputs; `grep -n 'captureLimit().acquire(true' internal/processor/threading.go` finds the call before `article.Mux.Lock()` (audit-delta-fixes checks it) |
| E05 | S3 | new `TestE2E*` of S3 pass; markers INT-2, SEC-2 (e2e) removed; `TestE2EDotHeaderLine` expects 441 |
| E06 | S3 | pre-existing processor and database tests green |
| E07 | S4 | `TestIntk*` and updated `TestWiring*` pass |
| E08 | S4 | all `TestE2E*` pass; markers INT-1 ×3, RFC-5 (e2e), SEC-3 (e2e), BUG-7 (e2e) removed |
| E09 | S4 | RFC-5 marker in `TestAuthPostingGates/no-posting-post` removed; the rest of `./internal/nntp/...` green |
| E10 | S4 | the script parses; N40–N49 PASS in `## End-to-end` |

## Checks

On every merged tree (after each merge in waves 1–3):
```bash
gofmt -l ./cmd ./internal            # may list only internal/database/embedded_migrations.go and internal/web/web_admin_provider.go
go vet ./...
go build ./...
go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
  ./internal/nntp/... ./internal/processor/... ./internal/common/... ./cmd/expire-news/... \
  ./cmd/history-rebuild/... ./cmd/nntp-server/...
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u
git status --short                   # no data-test-*, active.out, analysis_*.txt or zz_* leftovers
./build_nntp-server.sh && ./build_nntpmgr.sh
```

Expected known-bug set, "this plan only" accounting (AUDIT §10's 29 ids / 82 assertions minus the markers this plan
and its prerequisite remove in the files it touches): **26 ids / 72 assertions**. Removed markers (10): INT-1 ×3,
INT-2, SEC-2 (`TestE2ERejectUnknownGroup`), SEC-3 (`bytes-over-MaxArtSize`), BUG-7 (`lines-20000`), RFC-5
(`TestE2ERejectMissingNewsgroups/ihave`), RFC-5 (`TestAuthPostingGates/no-posting-post`) and A-nntp-rfc-2
(`TestE2EDotHeaderLine`, removed by hardening, kept absent here). INT-1, INT-2 and A-nntp-rfc-2 disappear; SEC-2,
SEC-3, RFC-5 and BUG-7 keep other markers:
```
known bug A-nntp-rfc-3 still open
known bug A-nntp-sec-1 still open
known bug BUG-1 still open
known bug BUG-2 still open
known bug BUG-4 still open
known bug BUG-5 still open
known bug BUG-6 still open
known bug BUG-7 still open
known bug BUG-8 still open
known bug BUG-9 still open
known bug RFC-1 still open
known bug RFC-2 still open
known bug RFC-3 still open
known bug RFC-4 still open
known bug RFC-5 still open
known bug RFC-6 still open
known bug SEC-1 still open
known bug SEC-2 still open
known bug SEC-3 still open
known bug SEC-4 still open
known bug SEC-5 still open
known bug SEC-6 still open
known bug SEC-7 still open
known bug T-nntp-article-1 still open
known bug T-nntp-auth-1 still open
known bug T-nntp-group-1 still open
```
On the real integration branch (hardening merged first, per the run order) the preflight records hardening's
remaining set as the base set; after wave 3 the set must equal that base set minus INT-1 and INT-2, and minus SEC-2,
SEC-3, RFC-5, BUG-7 or BUG-8 wherever the markers removed here (and the conditional BUG-8 one) were the last of their
id. INT-1, INT-2 and A-nntp-rfc-2 must be absent either way. After waves 1 and 2 the set is the one stated in each slice.

Merge order: wave 1 `intake-db`, `intake-common`, `intake-dlart` (disjoint, no semantic interplay); then S3; then S4,
running `## Checks` after each merge. Semantic interplay to watch: S3's `resolveIntakeGroups` uses S2's
`GetIntakeGroup`; S4's `article.Posted` is read by S3's `intakeGroupDecision`; S4's `intakeReply` relies on S3
returning K4 codes (a `CaseError` without `ErrIntakeRejected` from S3 would show up as 436/400 in `TestE2E*`).

## End-to-end

Runner: `pugleaf-verifier` on the main checkout (`plan-nntp-intake-injection`), after wave 3.
```bash
./build_nntp-server.sh && ./build_nntpmgr.sh
PORT=21129 DATA=./data-test-nntp-intake-injection scripts/test-nntp-server.sh
```
The script keeps its guards (DATA must be `./data-test-*`, `-nntphostname example.org` must resolve, else `SKIP dns`
and exit 2, refuses a busy port, runs the binaries with cwd `$DATA/run` and an absolute `-data`).

New checks (S4 adds them; `TestE2EBinaryIntake` prints them unless marked "script"):
- **N40** POST to `e2e.nosuch.group` → 441.
- **N41** (script) after N40: `find "$DATA/db" -name 'e2e_nosuch_group.db' | wc -l` = 0 and `sqlite3 … "SELECT count(*) FROM newsgroups WHERE name='e2e.nosuch.group'"` = 0; also `SELECT count(*) FROM newsgroups WHERE hierarchy IS NULL` = 0.
- **N42** POST to `e2e.intake.gen` without Message-ID and Date → 240; within 20 s `GROUP e2e.intake.gen` → `211 1 …`; `HEAD 1` → a generated `Message-ID: <…@$NNTPHOST>`, a `Date:`, an `Injection-Date:` and an `Injection-Info: $NNTPHOST…`.
- **N43** that HEAD's `Path:` starts with `$NNTPHOST!.POSTED!`.
- **N44** IHAVE of a new id whose article has two `Subject:` lines → 335 then 437.
- **N45** POST of a 64 KiB article (server default `MaxArtSize` 32 KiB) → 441, then `MODE READER` on the same connection → 200/201.
- **N46** pipelined TAKETHIS of a 64 KiB article followed by a small valid one → `439 <id1>` then `239 <id2>`.
- **N47** `IHAVE not-a-message-id` → 501.
- **N48** POST to `e2e.intake.mod` (status `m`) without `Approved:` → 441.
- **N49** POST with a header line `..` → 441, then `MODE READER` → 200/201.

Expected after this plan: N01–N11 PASS, N30 PASS, N40–N49 PASS (10 checks: N41 from the script, 9 from go test),
N20–N22 PASS (hardening turned them into PASS checks; this plan must not regress them). `SUMMARY … fail=0`. In `$DATA/server.log`: no
`use of closed network connection` on a write, no `DATA RACE`, and the `[INTAKE]` drop lines for N40.

---

## Open questions

Answered 2026-10-11 by the user ("1-3 with your defaults"): Q3 control messages are rejected (D3 as written);
Q1 the `MaxArtSize` default stays 32 KiB (`config.go` untouched); Q2 TAKETHIS in flight answers `439 <id> In progress` (D9 as written).
- **Q3 Control messages (ask before wave 2; D3 is implemented in S3).** D3 rejects them (peers get 437/439). The
  alternative is accept-and-drop (answer success, record in history, store nothing). Confirm.
- **Q1 MaxArtSize default (ask before wave 3).** The limit is enforced (hardening's reader, `-maxartsize` flag from
  hardening); only the default is open: 32 KiB (`config/config.go:26`, used by `NewDefaultConfig` `:574`) rejects
  long FAQ-style articles from posters and peers unless the operator passes `-maxartsize`. Keep 32 KiB or raise it
  (e.g. 1 MiB) in `config.go`; this plan does not change `config.go`.
- **Q2 TAKETHIS in-flight (ask before wave 3).** D9 answers 439 (INN behaviour) instead of RFC 4644's "400 + close"
  for a temporary condition. Confirm.

## Progress

(filled in by `/run-plan`)

### Preflight (2026-10-11, orchestrator; base `testing-001` @ 9a7ac3e, hardening merged as d9de34e)
- Owned-file drift since 03d5ef7: only `cmd/nntp-server/server_e2e_test.go` (A-nntp-rfc-2 marker removed by hardening),
  `internal/nntp/nntp-server-article_test.go`, `internal/nntp/nntp-server-auth_test.go`, `scripts/test-nntp-server.sh`
  (all hardening test/script changes the plan anticipates). `internal/nntp/nntp-cmd-posting.go` is unchanged
  (still has `readArticleData`/`readArticleLines`/`textConn.Close()`: S4 deletes them).
- Hardening names present at HEAD: `readArticleBlock(maxBytes int64) (headLines, bodyLines []string, err error)`,
  `ErrArticleTooLarge`, `ErrArticleDrainLimit`, `maxArticleDrainBytes`, `(*NNTPServer).MaxArticleBytes() int64`,
  `(*Local430).Remove(messageID string)`, `errQuit` (handleQuit returns it; `Handle` treats `errQuit` and `net.ErrClosed`
  as a clean close), `mainConfig.Server.Hostname = processor.LocalNNTPHostname` in `cmd/nntp-server/main.go:131`.
- All K2 reserved names free (`grep -rnw` empty for every one).
- Markers at the base: e2e INT-1 ×3 (`:920,:943,:962`), INT-2 `:1027`, RFC-5 `:1116`, SEC-2 `:1164`, BUG-7 `:1186`,
  SEC-3 `:1209`; `nntp-server-auth_test.go:318` RFC-5; `nntp-server-article_test.go:886` BUG-8 (S4 removes it), `:898` T-nntp-article-1 (stays).
- Base known-bug set (code identical to hardening's verified tree bcb361b): **BUG-7 BUG-8 INT-1 INT-2 RFC-1 RFC-5 SEC-2 SEC-3 T-nntp-article-1** (9).
  Expected after wave 3: **RFC-1, T-nntp-article-1** only (INT-1, INT-2 gone; BUG-7, BUG-8, RFC-5, SEC-2, SEC-3 lose their last markers here).

### Wave 1 (2026-10-11; 3 implementers, each reviewed before merge)
| Slice | Branch (commits) | Review | Merge |
|---|---|---|---|
| intake-db (S2) | worktree-agent-a0bc167187195b985 (d27c46d, 0f76132) | merge; 2 minors fixed (`rows.Err()`/Scan errors in `processOverviewBatch` now return to the retry path; the isolated test batch opens its own main-DB pool so a stuck batch can never close the shared one); untested retry path noted | b7f5e05 |
| intake-common (S1) | worktree-agent-a3ad4286e2deb3e09 (722278e, 5f90357) | merge; 2 minors fixed (`\[`/`\]`/`\\` escapes in a `[...]` message-id literal; WSP-only continuation lines rejected strict / dropped fetch). Recorded deviations for the client plan: `ValidMessageID`'s left part is lax (any printable except `<>@`, so `<a..b@x>` passes and a quoted `<"a@b"@c>` fails); DEL (0x7F) passes strict mode in field values | ca63b03 |
| intake-dlart (S5) | worktree-agent-a27ce2c1d0931f73e (057907f, 55c5230) | merge; 3 minors fixed (non-blocking `runDone` check before each send, `resetLateMsgID` on abandoned items, tautological test check); extra unexported names `feedDownloadRun`, `resetLateMsgID` accepted; A-tools-4 resolved (the `GroupQueues` map init in `processor.go:55` is now unused; audit-delta-fixes may delete it) | 82278fd |

Checks on 82278fd: gofmt baseline 2 files, vet/build OK, 9 package sets green under `-race` (incl. `internal/common`), known bugs = base set (9 ids), build scripts OK.
Noted: `TestAuthFailureDelay` (hardening's auth test) timed out once under heavy host load (3 agents' race suites in parallel) and passed on every rerun; timing-sensitive, outside this plan.

### Wave 2 (2026-10-11; 1 implementer, reviewed before merge)
| Slice | Branch (commits) | Review | Merge |
|---|---|---|---|
| intake-processor (S3) | worktree-agent-aabb41b2d4b1a88fe (2b13adf) | merge; 2 minors fixed inline by the orchestrator (157d097: rslight comment, `TestE2EGroupDBOpenFailure` deactivates its unopenable row in cleanup); deviations accepted (`defaultCaptureLimiterOnce`, `checkIntakeDates(now)`, `iprHead`, HeadersJSON/BodyText fallbacks, dead `CheckMessageIdFormat`/`containsAtAndDot` deleted). The commit carries an Opus 5.5 trailer (the implementer's own model). Reviewer note for S4: `buildIncomingArticle` still calls `GetNewsgroupPointer` for every header name before the processor runs (TasksMap entries for unknown names until they expire; no row/file) | d62e4e3 |

Checks on 157d097: gofmt baseline 2 files, vet/build OK, 9 package sets green under `-race`, known bugs = **BUG-7 BUG-8 INT-1 RFC-1 RFC-5 SEC-3 T-nntp-article-1** (7; INT-2 and SEC-2 gone), build scripts OK.

### Wave 3 (2026-10-11; 1 implementer, reviewed before merge)
| Slice | Branch (commits) | Review | Merge |
|---|---|---|---|
| intake-nntp (S4) | worktree-agent-a203ab6eea1cb9826 (8808914, 37d7f82) | merge; 3 minors fixed (`e2eOversizeChild` no longer waits 20 s for a log line the fixed server never writes, `TestE2ESizeLimits` 27 s → 8.5 s; the child now requires exactly 441 and a working `MODE READER`; `.POSTED` forged-trace check case-insensitive); duplicate "no path identity" log dropped. Deviations accepted: `NewsgroupsPtr` pre-fill kept for syntactically valid names (`processor.ProcessIncomingArticle` reads `*NewsgroupsPtr[0]`; S3 resets the list; TasksMap entries for unknown names expire after 120 s, no row/file), POST build error → `441 (invalid article)`, old Message-ID-mismatch texts kept, `rateLimitOnError` only before the new rejections. Commits carry an Opus 5.5 trailer | 84a3b51 |

Orchestrator follow-up (c-commit after 84a3b51): two stale comments refreshed in `nntp-server-article_test.go` (BUG-8 now fixed) and `nntp-history-parse_test.go` (`readArticleBlock`).
Checks on the final tree: gofmt baseline 2 files, vet (+`-tags e2e`) and build OK, 9 package sets green under `-race`, known bugs = **RFC-1 T-nntp-article-1** (2), build scripts OK,
`PORT=21129 DATA=./data-test-nntp-intake-injection scripts/test-nntp-server.sh` → `SUMMARY pass=30 fail=0 info=1` (N01–N11, N20–N28, N30, N40–N49 all PASS); server.log: 0 panic / DATA RACE / closed-socket writes, 1 `[INTAKE] … dropped` line (N40).

## Outcome

- **Integration branch:** `plan-nntp-intake-injection` @ aa2929b (23 commits from `testing-001` @ 9a7ac3e; 28 files, +5,056/−519 lines).
  Not yet merged into `testing-001` (user decision). Nothing pushed.
- **Worktrees / branches** (left in place for the user to clean up):
  - `/tank0/claude/trees/go-pugleaf/agent-a0bc167187195b985` — `worktree-agent-a0bc167187195b985` @ 0f76132 (intake-db, S2)
  - `/tank0/claude/trees/go-pugleaf/agent-a3ad4286e2deb3e09` — `worktree-agent-a3ad4286e2deb3e09` @ 5f90357 (intake-common, S1)
  - `/tank0/claude/trees/go-pugleaf/agent-a27ce2c1d0931f73e` — `worktree-agent-a27ce2c1d0931f73e` @ 55c5230 (intake-dlart, S5)
  - `/tank0/claude/trees/go-pugleaf/agent-aabb41b2d4b1a88fe` — `worktree-agent-aabb41b2d4b1a88fe` @ 2b13adf (intake-processor, S3)
  - `/tank0/claude/trees/go-pugleaf/agent-a203ab6eea1cb9826` — `worktree-agent-a203ab6eea1cb9826` @ 37d7f82 (intake-nntp, S4)
- **Verification** (pugleaf-verifier on afd59be/aa2929b, main checkout): gofmt baseline 2 files; vet (+`-tags e2e`) and build
  clean; 9 package sets green under `-race` (113 s); known bugs exactly `RFC-1 T-nntp-article-1` (12 assertions: RFC-1 ×11,
  T-nntp-article-1 ×1; no "no longer reproduces", no panic); build scripts OK (`build_nntp-server.sh` still without `-race`,
  OPS-4); `PORT=21129 DATA=./data-test-nntp-intake-injection-verify scripts/test-nntp-server.sh` → `SUMMARY pass=30 fail=0
  info=1` (N01–N11, N20–N26, N28, N30, N40–N49; there is no N27), 0 panic / DATA RACE / closed-socket writes, the N40
  `[INTAKE] … dropped` line present; `-count=2` rerun of nntp, nntp-server, processor, common green (206 s); manual probes
  on port 21135 all as expected (generated Message-ID/Date/Injection-Date/Injection-Info without any IP, `!.POSTED!not-for-mail`
  Path, duplicate → 441, Control → 441 + log, partially unknown crosspost stored only in the known group with no file/row/NULL
  hierarchy, IHAVE relay Path `example.org!.SEEN.127.0.0.1!…` with Xref stripped, `IHAVE no-brackets` → 501, 41 KiB POST →
  441 then MODE READER 200).
- **Decisions taken during the run:** Q1 MaxArtSize default stays 32 KiB; Q2 TAKETHIS in flight → `439 <id> In progress`;
  Q3 control messages rejected. `ValidMessageID` left part lax (printable except `<>@`), right side RFC 5536-strict with
  escapes; DEL passes strict mode; `UpdateNNTPUserWebUserID`-style additions none here. `buildIncomingArticle` keeps a
  `NewsgroupsPtr` pre-fill for syntactically valid names (the processor reads `NewsgroupsPtr[0]`), so an unknown but
  valid name leaves a 120 s TasksMap entry and nothing else.
- **Behaviour changes to report:** intake never creates groups (unknown/inactive/reserved/status-x dropped, status n for POST
  only, moderated needs `Approved:`, partially unknown crossposts stored in the known groups, none left → 441/437/439);
  strict header-block validation on NNTP intake (duplicate core headers, colon-less or `.` lines, control octets, > 998-octet
  lines, missing mandatory headers, Message-ID mismatch, invalid or space-separated Newsgroups, > 9 groups, unparseable dates,
  dates > 24 h ahead, POST dates > 72 h old, forged Injection-Info/Xref/`.POSTED`, Control messages) → 441/437/439 with a
  fixed reason; POST injection (Message-ID, Date, Injection-Date when generated, Injection-Info with a hashed posting-account,
  `Path: <host>!.POSTED!…`); feeds get `Path: <host>!.SEEN.<ip>!…`; Xref never stored; the stored Path line equals the `path`
  column; dedupe/failure codes per K4 (POST 441, IHAVE 435/436/437, TAKETHIS 439 / 400+close); RFC-5 gates (440 for POST
  without processor/permission, 500 for IHAVE/TAKETHIS without processor, TAKETHIS gate replies carry the id); article size
  limit enforced on POST/IHAVE/TAKETHIS (default 32 KiB, `-maxartsize`), oversize → reply with the stream in sync, past the
  drain bound → 400 and a clean close; unauthenticated TAKETHIS never keeps an article; local430 entry removed after accepts
  (BUG-8); the batch writer never creates a `newsgroups` row, counts only newly inserted rows, never writes `updated_at`
  backwards or as 0001-01-01, and history/msgid state no longer depends on a readable row; bulk paths (fetcher, web queue,
  rslight) get fetch-mode header normalization, Xref stripped, `<host>!` Path prefix in the stored line, 24 h future margin,
  and "no group DB could be opened" is now a temporary error instead of a silent success; web crossposts land in every group;
  fetcher captures bounded to 2048 in flight (back-pressure on every producer); `DownloadArticles` uses a per-run queue
  (late results drained, A-tools-4 resolved).
- **Leftovers / known issues:** `build_nntp-server.sh` without `-race` (OPS-4, audit-delta-fixes D11); `e2eKnownBug` in
  `server_e2e_test.go` has no callers left (harness helper, `unused` may flag it); the unused `GroupQueues` map init
  (`processor.go:55`) and deprecated `GetOrCreateGroupBatch` for audit-delta-fixes to delete; `TestAuthFailureDelay` timed out
  once under heavy load (passes on rerun); the S2 retry path (commit ok, SELECT fails, retry1) is traced but untested;
  `CreateNNTPUserForWebUser` fixed in hardening already uses the any-state lookup. S3/S4 commits carry an Opus 5.5 trailer.
  Gitignored leftovers: `build/`, `./data-test-nntp-intake-injection`.
- **Out of scope (as planned):** cancel/Supersedes processing, `control.*` filing, moderator forwarding, local `Xref:`
  generation, `bytes` semantics (INT-5), NULL-hierarchy readers and repair migration (A-db-1 readers, audit-delta-fixes),
  `ReconstructHeaders` (nntp-client-transfer), MaxArtSize default change, `-accept-unknown-groups`, web posting headers (INT-7).
- **Next:** `/run-plan nntp-client-transfer` (depends on hardening + intake, both now in or awaiting `testing-001`).
