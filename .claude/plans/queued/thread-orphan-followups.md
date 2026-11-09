# Plan: thread-orphan-followups — follow-ups without a parent in their group start a thread

## Context
Reported by the user (2026-11-06): a follow-up posted only to group B (or B,C), while its thread lives in group A,
does not create a thread in B. Review of testing-001 (87bf28e) confirms it and finds the same gap in the rebuild:

1. **Root/reply is decided once per article, not per group** (`internal/processor/threading.go:254`):
   `len(RefSlice) == 0` → `IsThrRoot`, else `IsReply`. Any article with a `References:` header is a reply in
   every group it is filed into.
2. **Intake files a reply only when a referenced article is a thread root in the same group**
   (`internal/database/db_batch.go`, `batchProcessReplies` → `findThreadRoot`): it walks the refs from the last
   one and asks `threads WHERE root_article = <article_num of ref>`. No hit → `threadRoot = 0` → no `threads` row,
   no `thread_cache` entry. The article is stored and readable by number/message-id, but it is in no thread, so
   the group's thread list never shows it. Cases that hit this:
   - parent and root only in another group (the reported case);
   - parent not arrived yet (out-of-order import: archives, CDs, mbox files in any order);
   - parent expired or never carried;
   - refs whose only local hits are replies: intake writes **no** `threads` row for replies (only roots and
     `thread_cache.child_articles`), so a reply whose refs name local replies but not the local root (trimmed
     References, RFC 5537 §3.4.4) is lost even though its parent is here;
   - a reply to an orphan from 2. is lost as well (its parent has no thread).
3. **The rebuild drops the same articles** (`internal/database/db_rescan.go`, `processThreadBatch`): a reply whose
   refs name no article of the group has `parentArticleNum == 0` and is skipped (`if parentArticleNum > 0`), so
   `recover-db -rebuild-threads` and `recover-db -reorder-by-datesent` (which calls `RebuildThreadsFromScratch`
   on the `.new` DB) do not fix it. The rebuild's root is "first ref that exists"; with 3. that ref can be an
   article that is itself in no thread.
4. **Intake and rebuild write different `threads` shapes**: rebuild inserts reply rows (`root, parent, child,
   depth`), intake does not. `GetThreadsCount` (`queries.go`, used by the stats total) counts all rows, so the
   thread total jumps after a rebuild.
5. **`articles.reply_count` undercounts** (`batchUpdateReplyCounts`): `parentMessageIDs` is keyed by
   `*string` (`&article.RefSlice[last]`), one key per reply, so two replies to the same parent in one batch give
   two `WHEN message_id = ?` arms for the same id and only the first one applies (+1 instead of +2). The rebuild
   never recomputes `reply_count`, and reorder copies the wrong value. Low impact (no web view reads it; NNTP reads
   select it), fixed here because the code is touched anyway.

What already works and stays: the tree view (`tree_cache.go` `buildTreeFromOverviews`) takes the thread root as
the root even when it has References and hangs a child whose parent is missing on the root
(`[TREE:CROSSPOST]` log); `threads(child_article)` has an index (`idx_threads_child`, migration 0001); the fetcher
files only the fetched group in bulk mode (keep: memory `fetcher-bulk-mode-ordering`).

## Design
**Rule (per group):** an article whose References name no article *of this group that is in a thread* is a
thread root in this group, with or without References. An article that has such a reference joins that article's
thread. This is the newsreader rule for broken/foreign threads (slrn, tin, Thunderbird show the follow-up as a
thread start).

1. **`threads` holds every threaded article, at intake as after a rebuild.** Roots: `(root, NULL, root, 0)` as
   today. Replies: `(root, parent, child, depth)` as the rebuild writes them. `depth` = parent's depth + 1 (not
   the ref position: trimmed refs make the position wrong); the rebuild uses the same definition.
2. **Intake** (`db_batch.go`, `batchProcessThreading`): per group and batch, after the articles got their numbers:
   - Sort the batch's candidates by `DateSent`, then article number (stable; a batch normally is in order already).
   - Keep an in-memory map `msgid → (articleNum, root, depth)` for the articles of this batch filed so far.
   - For each article with refs: walk the refs from the last one; the first ref found **in the batch map or in
     the group DB** (`SELECT t.root_article, t.depth, a.article_num FROM articles a JOIN threads t ON
     t.child_article = a.article_num WHERE a.message_id = ?`, one statement prepared per batch) gives
     `parent = that article`, `root = its root`, `depth = its depth + 1`. No hit → the article is a root.
   - Articles without refs are roots (today's path).
   - One transaction per batch for the `threads` inserts (roots and replies); then `InitializeThreadCache` for the
     new roots and `batchUpdateThreadCache` for the replies as today (same order: commit, then caches).
   - `IsThrRoot`/`IsReply` on `models.Article` stay as header facts (used elsewhere); the per-group decision is
     local to the batch code. Check every reader of `IsThrRoot`/`IsReply` and say in the slice report that none
     relies on them for per-group threading.
   - `findThreadRoot` (root-only lookup) is replaced by the lookup above; remove it if unused.
   - `batchUpdateReplyCounts`: key by the parent message-id **string** value; count only parents found in the
     group (the parent from the walk above, not blindly the last ref), so `reply_count` = direct replies in this
     group.
   - **Pinned:** no re-parenting at intake. An orphan root stays a root when its parent arrives later (out-of-order
     import); later replies attach to the nearest ref, which can be the orphan. `recover-db -rebuild-threads` merges
     them. A per-arrival "who references me" scan has no index and is not worth it.
3. **Rebuild** (`db_rescan.go`, `RebuildThreadsFromScratch` / `processThreadBatch`): same rule, whole group in
   memory (it already holds `msgIDToArticleNum`):
   - Pass 1: per article, `parent` = nearest ref (last to first) present in the group, else none.
   - Pass 2: `root`/`depth` by following parents with memo and a cycle guard (a self-reference or a cycle makes
     the article where the cycle is detected a root; log once per group with the count). An article without a
     parent is a root.
   - Insert roots and replies as in 1.; thread_cache as today (roots initialized, children appended per root).
   - Recompute `articles.reply_count` = number of direct children per article (one `UPDATE` per changed article in
     a transaction; set 0 first for the group).
   - Memory: two `map[int64]int64` (parent, root) over the group next to the existing message-id map. Log the
     group's article count and the time; no change for groups that fit today.
4. **Stats:** `GetThreadsCount` counts roots: `SELECT COUNT(*) FROM threads WHERE parent_article IS NULL`.
   Check the other `threads` readers (`GetThreads`, `GetThreadsPaged` in `web_apiHandlers.go` API) and keep their
   output meaning (all rows: say so in the API doc comment; roots only would be a behaviour change: don't).
5. **Existing data:** articles filed before the fix stay unthreaded until a rebuild. Document in
   `THREAD_REBUILD_IMPLEMENTATION.md`: after updating, run `recover-db -data <dir> -group '$all' -rebuild-threads
   -force` once (services stopped), or per group.

## Waves
### Wave 1 (parallel)
- slice `intake`: owns `internal/database/db_batch.go` (threading part only: `batchProcessThreading`,
  `batchProcessThreadRoots`, `batchProcessReplies`, `findThreadRoot`, `batchUpdateReplyCounts`),
  `internal/database/queries.go` (`GetThreadsCount` only), new `internal/database/thr_intake_test.go`.
  Tests (group DBs via the existing batch test helpers in `fu_batch_test.go` / `adf_batch_test.go`):
  follow-up with refs to an unknown message-id → root in its group, visible in `thread_cache`;
  crosspost A,B where the parent is only in A → reply in A, root in B; parent and reply in the same batch (reply
  first in the slice) → reply attached; reply → orphan → reply chain in one batch and across two batches;
  trimmed refs `[root, r3]` where only `r1..r3` and root exist → attaches to r3 with root's root, depth r3+1;
  two replies to the same parent in one batch → `reply_count` 2; reply arriving before its parent (two batches)
  → two roots (pinned behaviour); `GetThreadsCount` counts roots only.
- slice `rebuild`: owns `internal/database/db_rescan.go` (`RebuildThreadsFromScratch`, `processThreadBatch` and
  helpers), new `internal/database/thr_rebuild_test.go`, `THREAD_REBUILD_IMPLEMENTATION.md`.
  Tests: the intake cases above rebuilt from the `articles` rows give the same `threads`/`thread_cache` as intake
  for in-order input (helper compares both; shared fixture builder in the test file, no shared test helper file
  with slice `intake`); out-of-order input → rebuild merges the orphan under its parent; cycle (`A refs B`,
  `B refs A`) → one root, no hang; self-reference → root; `reply_count` recomputed; `recover-db
  -reorder-by-datesent` path keeps the result (it calls the same function: one test in
  `cmd/recover-db/adf_recover_test.go` is welcome, not required).

### Wave 2 (inline after merge)
- `BUGS.md`: entry for the fixed orphan follow-up gap (closed by this plan) if a matching entry exists; none → no
  entry.
- Review both slices together for the shared definition of `depth` and the root rule (intake vs rebuild must agree
  on in-order input).

## Checks
gofmt on touched dirs, `go vet ./...`, `go build ./...`, the full race test list from `.claude/CLAUDE.md`.

## End-to-end (pugleaf-verifier, scratch data only: `-data ./data-test-thread-orphans`)
1. Craft an mbox with: root R in `test.a`; follow-up F1 (refs R) posted to `test.b` only; follow-up F2 (refs R)
   crossposted `test.a,test.b`; reply F3 (refs R F1) to `test.b`; a reply G2 placed **before** its parent G1 in the
   file for `test.c`.
2. `./build_mbox-importer.sh`, import into the scratch root; inspect with `sqlite3` (`threads`, `thread_cache`):
   `test.a`: one thread R with F2; `test.b`: thread F1 (with F3) and thread F2 as roots; `test.c`: G1 and G2 both
   roots (pinned).
3. `./build_webserver.sh`, start on a non-default port (`-webport 19xxx -webhost 127.0.0.1`) with the scratch root,
   fetch `/groups/test.b/threads` (browser User-Agent): F1 and F2 listed; stop the server.
4. `go run ./cmd/recover-db -data ./data-test-thread-orphans -group '$all' -rebuild-threads -force`: `test.a`/`test.b`
   unchanged, `test.c` now one thread G1 with G2; thread total on the stats path counts roots.
5. Remove the scratch root at the end.

## Out of scope / follow-ups (not in this plan)
- Showing the foreign parent of an orphan root ("in reply to <msgid> in group A" via the history index) in the
  article/thread view: a separate decision for the user.
- `recover-db -reorder-by-datesent` writes `<group>.db.new` and stops: it does not swap files, update the main
  DB's group counters, or fix the `groupID:articleNum` history tokens (needs `history-rebuild`). Printing the
  remaining steps, or a `-swap` mode, would be its own plan.
