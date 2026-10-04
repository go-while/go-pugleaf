# Plan: web + SQLite follow-ups (transactions, dead code, shutdown test coverage, section residuals)

- **Slug:** `web-db-followups`
- **Integration branch:** `plan-web-db-followups` (from `testing-001`; it must contain the merge of `web-sqlite-leftovers`)
- **Run with:** `/run-plan .claude/plans/queued/web-db-followups.md`
- **Written:** 2026-09-16, at the end of the `web-sqlite-leftovers` (WSL) run. Every item below was
  re-verified against the code at `plan-web-sqlite-leftovers` @ `2510642` while writing this file —
  line numbers still drift, so grep before editing.
- **Size:** small. Two waves of three slices; no migration, no new tool, no config key.

---

## Context

`web-sqlite-leftovers` closed 22 of its 23 findings. Its `## Outcome` and the three progress notes
record what it deliberately did **not** take, either because the item was pre-existing and out of
slice, or because taking it at integration time would have meant an untested behaviour change. This
plan is that list, plus the items the post-merge review wave raised.

Nothing here is a regression introduced by WSL. Two items (D1, D2) are decisions rather than defects.

### Findings

**A. Transactions and error handling**
| ID | Where (at `2510642`) | Defect |
|----|----|----|
| A1 | `internal/web/web_profile.go` (`profileUpdate`, the write block after the validation fence) | The three writes — `UpdateUserPassword`, `UpdateUserEmail`, `UpdateUserDisplayName` — are separate un-transacted statements. WSL routed all three through `RetryableExec`, which shrinks the window, but a transient failure on the second still leaves the first committed: a user changing password **and** email can end up with the password silently changed and the email not, seeing only "Failed to update email". |
| A2 | `internal/database/queries.go`, `ResetAllNewsgroupData` step 1 | The mass counter `UPDATE newsgroups SET message_count = 0, ...` still uses a bare `db.mainDB.Exec`. WSL tightened the read path directly around it (`listNewsgroupNames`) but left this one, so the function violates the `Retryable*` convention in the very place it was cleaned up. |
| A3 | `cmd/web/main_functions.go`, `rsyncInactiveGroupsToDir` | Has `defer rows.Close()` and a `for rows.Next()` loop but **never checks `rows.Err()`**, so a truncated result set is silently treated as a complete one — the tool then rsyncs a partial group list and reports success. |
| A4 | `internal/database/queries.go`, `DeleteNewsgroup` | WSL made it delete the group's `section_groups` rows in the same transaction. It still leaves `user_spam_flags` rows keyed on the deleted `newsgroup_id` (`migrations/0007_main_user_spam_flags.sql` has an FK on `user_id` only; `post_queue` *is* cascaded by 0016). Dead rows only — `newsgroups.id` is `AUTOINCREMENT`, so the id is never reused — but they accumulate. |

**B. Dead code and misleading UI**
| ID | Where | Defect |
|----|----|----|
| B1 | `internal/web/embedded_static.go` | `EmbeddedFileHandler` has **no caller** (verified: only its own definition and doc comment). `staticContentType` is reachable only from it and from `lo1_core_test.go`. `/static/*` is served by `http.FileServer`, which uses Go's own mime table. WSL kept both because its slice text said to. **DECIDED 2026-10-04: delete, do not wire back.** Verified at wave 0: `EmbeddedStaticHandler` (`:38`) **is** live — `webserver_core_routes.go:334` serves `/static/*` through it with `http.FileServer(http.FS(staticFS))`, which uses Go's own mime table and never calls `staticContentType`. So the embed itself stays and three things go: `EmbeddedFileHandler` (`:69`), `staticContentType` (`:97`) and — same dead-export class, found while checking — `ListEmbeddedFiles` (`:23`, "for debugging", no caller anywhere). Keep `EmbeddedStaticFS`, `UseEmbeddedStatic`, `EmbeddedStaticHandler`. Note `internal/web/static/npm/bootstrap-icons@1.13.1/font/bootstrap-icons.scss` **is** shipped and `.scss` is absent from the type table — harmless only because nothing serves it through that function. |
| B2 | `internal/database/db_sessions.go`, `InvalidateUserSessionBySessionID` | No caller in `internal/` or `cmd/` (logout goes through `InvalidateUserSession(userID)`). Exported API that only tests use. **DECIDED 2026-10-04: delete, do not wire logout to it.** Logout already has the user id from the validated session, so `InvalidateUserSession(userID)` is the natural call and the by-token form buys nothing. No coverage is lost: `TestLo1DBSessionHashedAtRest` proves the hashing with `ValidateUserSession(raw)` succeeding and `ValidateUserSession(hash)` failing; only the assertion specific to the deleted function goes with it. |
| B3 | `internal/web/web_admin_newsgroups.go`, `adminDeleteNewsgroup` | Flashes "Newsgroup deleted successfully" whenever `DeleteNewsgroup` returns nil — including when nothing was deleted, because the group was still active (`query_DeleteNewsgroup` has an `active = 0` guard, so deleting an active group is a no-op). The admin is told a delete happened that did not. `DeleteNewsgroup` now computes this internally; K1 of WSL pinned its `(name) error` signature, which this plan is free to change. |

**C. Shutdown: test coverage and two loose ends**
| ID | Where | Defect |
|----|----|----|
| C1 | `internal/database/db_batch.go`, the `retry1` / `retry2` labels | The loop bounds added by the WSL post-merge wave have **no unit test**. Exercising them needs a live `SQ3batch` plus a group DB whose insert keeps failing, which no WSL slice could build. `batchShutdownClock` itself is tested (`TestLo2WriterBatchShutdownClock`); the two call sites are not. This is the code that stops a wedged batch pinning `db.WG.Wait()`, so it is worth a real test. |
| C2 | `internal/web/cronjobs.go`, `loadAndStartJobs` vs `StopCronManager` | `loadAndStartJobs` does not re-check `stopChannel` between jobs, so a job started while `StopCronManager` is taking its `jobIDs` snapshot is never stopped, and `db.WG.Done()` runs while that job's goroutine is still executing. Pre-existing; WSL made the window smaller (one select instead of up to 60s) but did not close it. |
| C3 | `cmd/nntp-analyze/main.go` (~`:280`) | `defer close(db.StopChan)` with **no** `db.Shutdown()`, so with WSL's F20 change `cronDBEvery` never returns in that tool. Harmless (the goroutine is outside `db.WG` and the process exits), but it is the one tool where the new exit condition is unreachable. |

**D. Decisions, not defects**
| ID | Where | Question |
|----|----|----|
| D1 | `internal/web/webgroupPage.go`, `web_sectionsPage.go`, `web/templates/pagination.html` | **DECIDED 2026-09-16: make the clamp honest.** Do *not* wire cursor links. Article listings clamp `?page=` to 101 and the clamp is currently **silent**: the template renders a "Last" link to the real page count, and following it serves page 101's articles under that URL. Cap the rendered links at the real bound instead, so no link promises a page the server will not serve. **Why the cap exists** (traced, so nobody removes it): `webgroupPage.go:73-94` turns `?page=N` into a cursor with `SELECT article_num ... ORDER BY article_num DESC LIMIT 1 OFFSET (page-1)*128 - 1`. The page fetch itself is cursor-based and cheap; that *conversion* is the deep-OFFSET scan, and SQLite discards `skipCount` rows to answer it — ~640k rows at page 5000. `maxOffsetArticles = 12800` fixes the worst case at ~12.8k discarded rows. The constant's own comment says "Deeper pages must use the cursor parameter"; `?cursor=` does work and skips the conversion entirely, it was simply never linked. Note the API already sends `X-Page-Clamped: 1` (`web_apiHandlers.go`), so the HTML side is the inconsistent half. |
| D2 | `cmd/audit-web-posts` | **DECIDED 2026-09-16: keep `-strict`, keep the fallback default, and make the flag self-announcing.** An unused flag costs nothing and it is the only hard "touch nothing" guarantee the tool can give; flipping the default was tried during WSL and **failed the e2e check**, because a pending `-wal` is routinely left behind (`stop_server`, and the fetcher's `log.Fatalf` path exits without checkpointing). The real problem was discoverability: the refusal message named `-strict`, but you only saw it if you already knew to pass the flag, while the fallback warning — the one you actually hit — did not mention it. Fixed outside this plan (see the note below): the fallback warning now reads "Stop the writer, or audit a copy, or pass `-strict` to refuse instead." **No work left for this plan.** |

**E. Residuals from the WSL reviews**
| ID | Where | Defect |
|----|----|----|
| E1 | `internal/database/queries.go`, `query_GetSectionGroupsWithActivity` | A `LEFT JOIN newsgroups n ON ... AND n.active = 1`, so `sectionPage` still **lists** (a) inactive member groups and (b) `section_groups` rows orphaned by deletes that happened before WSL's F10 fix — both with `message_count 0`. Clicking one now correctly 404s (F10), so the visitor sees a listed group that cannot be opened. No migration cleans the pre-existing orphans either. |
| E2 | `internal/database/thread_cache.go`, `GetCachedThreadReplies` | Returns `totalReplies = message_count - 1` while paginating over `len(childArticles)`. If `thread_cache.message_count` and `child_articles` ever disagree, the caller's `totalPages` (`web_threadPage.go`) and the page the guard allows disagree too, so a link to the "last" page can render empty. Pre-existing and untouched by WSL's overflow guard. |
| E8 | `internal/database/queries.go:753,761,769,771` and `internal/database/db_user_profile.go:13-15` | **Found by the wave-2 review of `fu-web-forms`.** Two consequences of A1, neither fixable by the slice that caused them (it does not own `queries.go`). (a) **`UpdateUserDisplayName` now has no production caller at all** — `profileUpdate` was its only one, and its sole remaining reference is two assertions in `lo2_pages_test.go`. That is exactly the dead-exported-API class this plan's own `fu-deadcode` slice deleted for B2, and it leaves a second, unexercised copy of the 64-rune limit that can rot unnoticed. (b) **The same three user columns are now written from two places** with byte-identical SQL — verified column for column, so nothing diverges *today*. The drift is one-sided and silent: a *rename* would fail loudly on both sides, but someone adding `updated_at = CURRENT_TIMESTAMP` or a `WHERE ... AND disabled = 0` guard to `UpdateUserEmail` for the admin path and not here would make the admin-edited email behave differently from the user-edited one, with nothing failing or warning. Fix, in the direction that also removes the dead code: have the per-field helpers delegate (`UpdateUserEmail(id, e) { return db.UpdateUserProfile(id, nil, &e, nil) }`, likewise password), delete `UpdateUserDisplayName` and repoint `lo2_pages_test.go`'s two assertions, and drop the three local `query_UpdateUserProfile*` consts in favour of the `queries.go` ones — one owner for the columns, one for the rune limit. **Assigned to wave 3** (`fu-profile-consolidate`), which needs `queries.go` after `fu-queries` has merged. |
| E7 | `internal/database/thread_cache.go:69` **and** `internal/database/db_batch.go:1426-1430` | **Found by `fu-threadcount` in wave 2 while fixing E5; it correctly did not touch it. The wave-2 review then found it is in BOTH fallbacks, not just one, and that E5's fix makes it silent.** The fallback fires on **any** error from the `SELECT`, not only `sql.ErrNoRows`. So when the row *does* exist but the read failed — a lock surviving every retry — `InitializeThreadCache`'s `ON CONFLICT` leaves the row's `child_articles` alone and the following `UPDATE` then overwrites it with just the one new child: **the previously listed children are lost.** Worked example from the review: root 10 holds `child_articles = "11,12,13"`, `message_count = 4`; article 14 arrives while another writer holds the write lock past the retry cap; the row ends as `child_articles = "14"`, and articles 11-13 vanish from both the listing and the thread page's pagination. Nothing in `articles` or `threads` is lost — only the derived cache — and `RebuildThreadsFromScratch` restores it. Likelihood is low in the batch path (its SELECT sits inside the transaction, so a lock usually fails `initStmt.Exec` too and the whole thing rolls back and retries) and non-trivial in `UpdateThreadCache`, whose three statements are independent so the lock can clear between them.
**The part that needed acting on: E5's fix silences the detection.** Before it, the damaged row read `message_count = 1, child_articles = "14"` — invariant broken, so wave 1's throttled mismatch warning fired. After it the row reads `2, "14"` — self-consistent, so that warning can never fire on exactly the rows that lost data. A loud data loss became a silent one. Mitigated at integration (see the progress note): both fallbacks now log a `Warning: ... non-ErrNoRows error, treating the row as missing: previously listed children may be lost until a rescan` line, which names the real cause rather than relying on a downstream symptom. That restores detection **without** deciding the behaviour question.
Fix still open: gate the fallback on `errors.Is(err, sql.ErrNoRows)` and return the error otherwise. **Same class of decision as E6** — it changes what the batch writer and fetcher do under a real fault (surface the error instead of continuing), so both should be decided together. **Not assigned to a wave.** |
| E6 | `internal/database/db_batch.go`, the `retry2` error branch and `batchProcessThreading` | **Found by `fu-batch-tests` in wave 1 while trying to drive the loop.** `batchProcessThreading` logs the failures of `batchProcessThreadRoots`/`batchProcessReplies` ("Continue processing - don't fail the whole batch") and returns `nil` on **every** path, so `if err := sq.batchProcessThreading(...); err != nil` is unreachable and `threadClock` is dead code. A `BEFORE INSERT ... RAISE(ABORT)` trigger on `threads` cannot drive that loop at all: the articles commit and the function returns at once. Consequence worth stating plainly: the previous plan's post-merge "retry2 abandons a batch after phase 1 committed" fix was hardening a path that cannot currently execute — the bound itself is correct (wave 1 proved it returns at 2m0.35s once the path is made reachable), it is simply never entered. **This needs a decision, not a patch:** either let threading failures propagate — today a blocked threading write still commits the articles and the batch continues, and changing that alters batch behaviour under a real fault — or delete the dead branch and `threadClock` with it. `TestFuBatchRetry2ThreadingFailureIsNotRetried` pins the current premise and will fail with a directed message the day it changes, so there is no rush and no silent drift. **Not assigned to a wave** — bring it to the user. |
| E5 | `internal/database/thread_cache.go:91` and `:441`, `internal/database/db_batch.go:1423,1445` | **Found by the wave-1 review of `fu-misc-hygiene`; it is the root cause behind E2.** Two live writers permanently create the `message_count` off-by-one that E2 works around: `UpdateThreadCache`'s "cache row missing" fallback (`thread_cache.go:91`) sets `currentCount = 0` *after* `InitializeThreadCache` already wrote `message_count = 1`, then writes `currentCount+1`; `batchUpdateThreadCache` (`db_batch.go:1423,1445`) does the same on a select-miss, inserting `message_count = 1`, resetting `currentCount = 0`, then writing `newCount = 0 + len(updates)`. Nothing recomputes `message_count` outside a rescan, so every affected row stays one low forever. Consequences, all real: any thread through either fallback under-reports by a reply, a thread with exactly one such reply showed **zero** replies and served no reply page (its articles unreachable from the web — E2 fixes that symptom), and after E2 the thread *page* counts `len(child_articles)` while the group listing at `thread_cache.go:441` (`GetCachedThreadsFromMemory`, `MessageCount: meta.MessageCount - 1`) still counts `message_count - 1`, so the two now disagree. Fix: seed `currentCount = 1` in both fallbacks to match the row just inserted, and make the listing consistent with the page. Fixing the writers also silences E2's new warning for all newly-created rows. Existing bad rows persist until a rescan, which is why E2's throttled warning stays useful. |
| E4 | `internal/web/web_threadPage.go:88-89` | **Found during wave 1 by `fu-misc-hygiene`, which correctly did not fix it** (no slice owned that file). A second, independent cause of E2's symptom, on the caller's side: `totalMessages := totalReplies + 1` then `totalPages := ceil(totalMessages / ThreadMessages_perPage)` with `perPage = 50`. The root article is counted into `totalPages` but is **not** part of what `GetCachedThreadReplies` paginates — it is prepended only when `page == 1`. So a thread whose reply count is an exact multiple of 50 gets one page too many: 50 replies → `totalPages = ceil(51/50) = 2`, and page 2 has no replies and no root. Fixing E2 on the database side alone leaves this reachable, so the two belong together. **Exact threshold, confirmed by the review: `totalReplies > 0 && totalReplies % 50 == 0`** — 50, 100, 150 … replies, one extra page each time. Walked through for 50 replies: `totalMessages = 51`, `totalPages = 2`, `HasNextPage` true on page 1, `?page=2` hits the overflow guard (`page-1 = 1 >= ceil(50/50) = 1`) and returns an empty slice, and since `page != 1` the root is not prepended either — a linked, reachable, entirely empty page. The `page > totalPages` clamp does not help because `2 == totalPages`. Fix: `totalPages := ceil(totalReplies / ThreadMessages_perPage)` with the existing `if totalPages == 0 { totalPages = 1 }`, keeping `MessageCount: totalReplies + 1` for display. (The underlying asymmetry is that page 1 holds 51 items and every other page 50; this is the minimal correct bound without changing that.) **Assigned to wave 3** (`fu-pagination`) — same pagination theme, and no other slice owns that file. |
| E3 | `scripts/test-web-hardening.sh`, E12 | Now passes for a weaker reason: it sends an invalid `X-Real-IP` and asserts the peer IP, but since WSL's F3 that header is never consulted, so it would pass with a *valid* one too. The intent holds and the outcome is strictly safer; the label is now wrong. `TestLo2ServerTrustedHeader` is the real coverage. Relabel only — do not weaken the assertion. |

### Out of scope (list in Outcome)
- `runTokenUsageFlusher` taking no `db.WG` slot. **Deliberate** — a slot would move the block into
  `db.WG.Wait()`. With WSL's bounded final flush the write-to-a-closing-DB window is short and logged
  (`[API]: shutdown: usage of token N NOT written`). Do not "fix" this.
- NNTP server protocol and security work — that is `nntp-audit-tests` (NAT), still queued.
- Anything requiring a new migration, config key or tool.

---

## Design

### Contracts
- **K1: signatures that may change** (each has a single caller set, all inside this plan):
  `DeleteNewsgroup(name string) (bool, error)` — returning whether a row was deleted (B3);
  `GetCachedThreadReplies` gains no parameter but may change what it returns for `totalReplies` (E2).
  Everything else keeps its signature, in particular the WSL K1 list (`NewWebServer`, `getWebSession`,
  `checkGroupAccess*`, `renderError`, `CreateUserSession`, `ValidateUserSession`, `Retryable*`,
  `GetGroupDB`/`Return`, `AuthenticateNNTPUser`, `configureTrustedProxies`, `setSessionCookie`).
- **K2: new names, one owner each** —
  `fu-web-forms`: `(*Database).UpdateUserProfile` (in the new `db_user_profile.go`);
  `fu-queries`: `query_DeleteUserSpamFlagsByNewsgroup` (A4) and
  `query_GetSectionGroupsWithActivityStrict` (E1);
  `fu-batch-tests`: `fuBatch…` helpers;
  `fu-misc-hygiene`: `fuHygiene…` helpers;
  `fu-deadcode`: no new names (deletions only — removes `EmbeddedFileHandler`, `staticContentType`,
  `ListEmbeddedFiles`, `InvalidateUserSessionBySessionID`).
  Check each with `grep -rnw '<name>' internal cmd` at the base commit before using it.
- **K3: tests** are `fu_<slice>_test.go` with identifiers prefixed `fu<Slice>…`. The WSL and WSH
  helpers are reused and never edited: `internal/database/testmain_test.go` (`w0DB`, `w0Name`) and
  `internal/web/testmain_test.go` (`w0Srv`, `w0DB`, `w0Do`, `w0NewUser`, `w0NewGroup`, `w0DataDir`).
  The rules that held through WSL still hold: never `Shutdown` `w0Srv`; no `t.Parallel` in a test that
  mutates a global; restore every global with `t.Cleanup`; anything that touches every user or group
  uses an isolated `&Database{...}`.
- **K4:** nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt`, `BUGS.md`, root
  `README.md`, `build_ALL.sh`, or the NAT-owned NNTP files. `scripts/test-web-hardening.sh` is
  editable **only** for E3's relabel, which wave 0 does inline. `scripts/test-web-leftovers.sh` must keep passing unchanged.
- **K5:** no slice calls a helper another slice of the same wave adds.

### Component designs
- **A1 (`UpdateUserProfile`):** one `RetryableTransactionExec` that writes password hash, email and
  display name in a single transaction, taking `nil` for any field left unchanged. `profileUpdate`
  keeps its current validation fence and calls it once. The existing per-field helpers stay for their
  other callers — **correction, found by the wave-2 review:** that is true of `UpdateUserEmail`
  (`web_admin_userfuncs.go`) and `UpdateUserPassword` (`cmd/usermgr`) but **not** of
  `UpdateUserDisplayName`, whose only production caller was `profileUpdate` itself. See E8.
- **B3 (`DeleteNewsgroup`):** return `(deleted bool, err error)`. `adminDeleteNewsgroup` flashes
  success only on `deleted`, and otherwise says the group must be deactivated first. Grep for every
  caller before changing the signature.
- **C1 (batch loop tests):** build an `SQ3batch` against an isolated `&Database{}` with a temp data
  dir, insert a group DB whose `articles` table has a `BEFORE INSERT ... RAISE(ABORT)` trigger so
  `batchInsertOverviews` fails deterministically, set the shutdown flag, and assert
  `processNewsgroupBatch` returns within the grace and logs the drop line. Same shape for `retry2`
  with a trigger on the threading write.

---

## Waves

`internal/database/queries.go` carries **four** of the findings in three separate regions
(`ResetAllNewsgroupData` for A2, `DeleteNewsgroup` for A4 and B3, `query_GetSectionGroupsWithActivity`
for E1). Two slices in one wave cannot both edit it, so **one slice owns that file outright** and the
rest are arranged around it. Two atomicity constraints drove the layout:

- **B3 cannot be split across waves.** Changing `DeleteNewsgroup` to `(bool, error)` and updating
  `adminDeleteNewsgroup` must land together, or the tree does not build between waves. So whichever
  slice owns `queries.go` also owns `internal/web/web_admin_newsgroups.go`.
- **A1 does not need `queries.go` at all.** `UpdateUserProfile` goes in a new
  `internal/database/db_user_profile.go`; the existing per-field helpers stay untouched for their other
  callers (`web_admin_userfuncs.go`, `cmd/usermgr`). That is what frees A1 to run in parallel.

### Wave 0 (inline, orchestrator)
1. Preflight from the skill; `git merge-base --is-ancestor e52d1a8 HEAD` must succeed.
2. Baseline: the `## Checks` below, plus both e2e scripts (expect `pass=25 fail=0` and `pass=16 fail=0`).
3. **D1 and D2 are already decided** (see the table above) — no questions to ask. D1 runs as wave 3;
   D2 needs no work at all.
4. **E3** inline: relabel E12 in `scripts/test-web-hardening.sh` (label only — the assertion does not
   change, and `pass=25 fail=0` must still hold). This is the only edit K4 permits to that script.

### Wave 1 (3 parallel slices — no `queries.go`, fully disjoint)
- slice `fu-deadcode` — **B1**, **B2**, **C3**. Both B1 and B2 are **decided: delete** (see the
  findings table). Owns `internal/web/embedded_static.go`,
  `internal/database/db_sessions.go` (`InvalidateUserSessionBySessionID` only),
  `cmd/nntp-analyze/main.go`, and — **for the dependent assertions only** —
  `internal/web/lo1_core_test.go` (`TestLo1CoreStaticContentType`, lines ~405-422) and
  `internal/database/lo1_db_test.go` (the `InvalidateUserSessionBySessionID` block in
  `TestLo1DBSessionHashedAtRest`). Those two test files are otherwise **not** owned: remove only what
  references a deleted symbol, change nothing else in them. No `fu_deadcode_test.go` is needed — this
  slice only deletes; `go build`/`go vet` plus the untouched suite are the proof.
  Re-grep every symbol before deleting it; if any grows a caller since wave 0, stop and report.
- slice `fu-batch-tests` — **C1**, **C2**. Owns `internal/database/fu_batch_test.go` (new),
  `internal/web/cronjobs.go`, `internal/web/fu_cron_test.go` (new).
  C1 is the substantial one: it is the first real test of the loop bounds that stop a wedged batch
  pinning `db.WG.Wait()`.
- slice `fu-misc-hygiene` — **A3**, **E2**. Owns `cmd/web/main_functions.go`
  (`rsyncInactiveGroupsToDir` only), `internal/database/thread_cache.go`
  (`GetCachedThreadReplies`' `totalReplies` only — **do not** touch `threadChildrenQuery` or the
  overflow guard), `internal/database/fu_hygiene_test.go` (new).

### Wave 2 (2 parallel slices, based on merged wave 1)
- slice `fu-queries` — **A2**, **A4**, **B3**, **E1**. Owns `internal/database/queries.go`
  **in full**, plus `internal/web/web_admin_newsgroups.go` (B3's caller, atomic with the signature)
  and `internal/web/web_sectionsPage.go` (E1's listing filter), `internal/database/fu_queries_test.go`
  (new), `internal/web/fu_queries_test.go` (new).
  Note it inherits WSL's constraint in reverse: it may change `DeleteNewsgroup`,
  `ResetAllNewsgroupData` and `query_GetSectionGroupsWithActivity`, and must leave every other
  function in that ~3700-line file byte-identical (verify with `git diff -U0`).
- slice `fu-web-forms` — **A1**. Owns `internal/web/web_profile.go` (`profileUpdate` only),
  `internal/database/db_user_profile.go` (new), `internal/web/fu_forms_test.go` (new).
  Must not touch `queries.go`.
- slice `fu-threadcount` — **E5**, added after the wave-1 review. Owns
  `internal/database/thread_cache.go` (the `UpdateThreadCache` fallback at `:91` and
  `GetCachedThreadsFromMemory` at `:441`), `internal/database/db_batch.go`
  (the `batchUpdateThreadCache` fallback at `:1423,1445` **only** — do not touch the `retry1`/`retry2`
  loops, `batchShutdownClock`, `batchShutdownGrace`, `retryGrace` or `retryShutdownGrace`, all of which
  `fu-batch-tests` now covers with tests; its hunks are at `:97`, `:584`, and the four loop call sites,
  so there is no textual overlap with the fallback),
  `internal/database/fu_threadcount_test.go` (new).
  It must leave wave 1's `GetCachedThreadReplies` count and its throttled warning intact — this slice
  fixes the *cause*, that one handles rows already on disk. Disjoint from `fu-queries` and
  `fu-web-forms`; it edits `thread_cache.go` only after `fu-misc-hygiene` has merged.

### Wave 3
- slice `fu-profile-consolidate` — **E8**, added after the wave-2 review. Owns
  `internal/database/queries.go` (the three `UpdateUser*` helpers and their query consts only — it
  runs after `fu-queries` has merged, so no conflict), `internal/database/db_user_profile.go`,
  `internal/database/lo2_pages_test.go` (the two `UpdateUserDisplayName` assertions only).
  Collapse the duplication so the three user columns and the 64-rune limit each have one owner, and
  delete `UpdateUserDisplayName` now that nothing calls it. Disjoint from `fu-pagination` below.

### Wave 3 (D1 — confirmed, runs)
- slice `fu-pagination` — **D1, "make the clamp honest"**, plus **E4**. Owns
  `web/templates/pagination.html`, `internal/models/models.go` (`PaginationInfo` fields only),
  `internal/web/webgroupPage.go`, `internal/web/web_sectionsPage.go` (the pagination block only),
  and `internal/web/web_threadPage.go` (the `totalPages` arithmetic only, for E4).
  **E4** is the thread-page off-by-one wave 1 surfaced: the root article is counted into `totalPages`
  but is not paginated, so a reply count that is an exact multiple of `ThreadMessages_perPage` links a
  final empty page. It is the caller-side half of E2 and must land with D1, because both are "a
  pagination link that promises a page the server will not fill".
  It shares `web_sectionsPage.go` with `fu-queries`, which is why it cannot run before wave 2.
  Scope: `PaginationInfo` learns the clamped bound (e.g. `LinkablePages`), `NewPaginationInfo` takes it
  from the caller's `maxOffset/pageSize + 1`, and `pagination.html` renders "Last" and the page numbers
  against that bound instead of `TotalPages`. When the real total is larger, say so in the info text
  rather than linking a page that will be clamped. **Do not** add cursor links and **do not** raise
  `maxOffsetArticles` — the cap is a real guard on the page→cursor OFFSET scan (see D1).
  Checks: a group with more than 101 pages renders no link above the bound, `?page=5000` still answers
  200, and `NewPaginationInfo` is unchanged for every caller that passes no bound.

---

## Checks
On every merged tree:
```bash
gofmt -l ./cmd ./internal      # only internal/database/embedded_migrations.go and internal/web/web_admin_provider.go
go vet ./...
go build ./...
go test -race -count=1 ./internal/database/... ./internal/web/... ./internal/history/... ./internal/nntp/... \
  ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/audit-web-posts/...
./build_webserver.sh && ./build_fetcher.sh && ./build_audit-web-posts.sh
```

## End-to-end
Both existing scripts must stay green — this plan adds no new e2e script:
```bash
PORT=18981 DATA=./data-test-web-sqlite-hardening scripts/test-web-hardening.sh   # pass=25 fail=0
PORT=18991 DATA=./data-test-web-sqlite-leftovers scripts/test-web-leftovers.sh   # pass=16 fail=0
```

## Note for whoever runs this
Two lessons from the WSL run, both of which cost time there:
1. **Review the orchestrator's own inline commits.** The run-plan skill reviews implementer branches,
   not the integration commits the orchestrator writes between merges. Two real defects hid there.
2. **A claim in a subagent report is not a fact.** Open the file before repeating it to the user or
   writing it into a plan — one leftover in WSL was recorded wrongly for exactly this reason, and the
   "one-line fix" it proposed would have panicked.

---

## Progress

### Wave 0 — `ed472ef`
Integration branch `plan-web-db-followups` from `testing-001` @ `60c0b63`. Baseline re-verified after an
18-day gap: gofmt/vet/build clean, `-race` green across 8 packages, both e2e scripts at
`pass=16 fail=0` and `pass=25 fail=0`. **E3** done (E12 relabelled, label only). The two questions still
embedded in **B1** and **B2** were decided **delete**, after verifying that `EmbeddedStaticHandler` is
genuinely live and that nothing depends on the by-token session invalidation.

### Wave 1 — merged, all 3 slices

| Slice | Branch | Impl commits | Merge | Review |
|----|----|----|----|----|
| `fu-deadcode` | `worktree-agent-a9e63f333f38a283b` | `12c2950`, `a16735c` | `005cc4f` (+ README `014b021`) | MERGE |
| `fu-misc-hygiene` | `worktree-agent-a6cad8ec8717e5ed8` | `d24d9fe`, `ccd2e7d`, `8183b85` | `c08cb33` | MERGE after one minor |
| `fu-batch-tests` | `worktree-agent-aecdd086682537625` | `57a504a`, `464975c`, `93f2d6a`, `df93e20` | `2d5a978` (+ seam `677c3a2`) | MERGE after three minors |

**Checks on the merged tree (`677c3a2`):** gofmt/vet/build clean; `-race` green across all 8 packages;
`scripts/test-web-leftovers.sh` **`pass=16 fail=0`**; `scripts/test-web-hardening.sh`
**`pass=25 fail=0`** with the relabelled E12, E19 and E23 all passing; 0 DATA RACE in both logs.

**What the reviews added that the implementers' own green checks did not:**
1. **E2 turned out to be a live bug, not a hypothetical.** The review traced two writers that create the
   `message_count` mismatch *permanently* (E5), so a thread with one such reply reported **zero** replies
   and served no reply page at all — its articles unreachable from the web. It also showed the fix is
   bit-identical on healthy rows, since the rebuild path writes `1 + len(children)`.
2. **C3's justification was wrong while its code was right.** The implementer said the `db.WG.Wait()` is
   safe because `Shutdown` sets `groupDBsShutdown`; the actual reason is that `IsDBshutdown()` is a
   non-blocking receive on `StopChan` and so is already true *before* the wait, and `cronDBEvery` was
   never in `db.WG` at all. Recorded because the stated reason would mislead the next editor.
3. **The grace seam was partial.** `getBatchGroupDB` and `updateNewsgroupStatsWithRetry` still took the
   2-minute const, so a 3-second test could hit a 2-minute wait in the nested loop and fail with the
   wrong diagnosis. Completed inline in `677c3a2`; all four bounded loops now read the seam.

**Judgement calls made during the wave:**
- **Authorized `fu-batch-tests` to edit `db_batch.go` mid-slice** for the grace seam. Its C1 test cost
  120s, taking `internal/database` from 27.6s to **146.6s**; the predictable result is people stop
  running the suite. With the seam: retry1 test **3.02s**, package **~30s**, full 8-package suite
  **42s** (was 2m29s). Production reads the unchanged const through a zero field, asserted by a test.
- **Accepted a better fix than the one I proposed.** I suggested a `sync.Map` keyed on
  `(newsgroup, threadRoot)` for E2's warning throttle; the implementer refused it because that key set
  grows *with the fault*, and used two atomics instead — O(1) regardless of how many rows are broken.
- **Confirmed `threadCacheMismatch*` over the `fuHygiene…` prefix** for new production symbols: K2's
  prefix governs test identifiers, and naming live code after a plan slice reads badly once the plan is
  filed.
- **Approved C2's second gate**, beyond the slice text. The loop-top check alone only narrows the
  window; the check inside `startJob` under `cm.mutex` closes it, because `StopCronManager` closes
  `stopChannel` before taking that same lock.

**Findings discovered during wave 1 and recorded rather than patched:** **E4** (wave 3), **E5**
(new wave-2 slice `fu-threadcount`), **E6** (unassigned, needs a user decision).
