# Plan: post-430-window

## Context
Found by the xref-leftovers verification (2026-10-16): right after a successful POST in the same nntp-server
process, `STAT <message-id>` (also ARTICLE/HEAD/BODY/OVER/HDR by message-id) with no group selected answers
`430` and keeps answering 430 on every connection for up to the local430 TTL (default 60 s). Selecting the
group first works at once. BUGS.md (accepted limitation of T-nntp-article-1) says "same-process arrivals are
removed at once", which is wrong in this window.

Why (all pre-existing, none from the Xref plans):
1. `handlePost` (`internal/nntp/nntp-cmd-posting.go:91-95`) sends 240 as soon as `ProcessIncomingArticle`
   returns, and that returns when the article is handed to the batch writer (`internal/processor/threading.go`,
   NoteCaptured + capture goroutine). The group DB commit follows within `database.BatchInterval` (3 s) or on a
   flush request; the history row is queued only after the commit and flushed within the history
   `BatchTimeout` (1 s). So for up to ~4 s neither the group DB nor the history index knows the article.
2. The by-message-id lookup without a group (`internal/nntp/nntp-article-common.go:122-137`, step (b)) asks
   `Processor.FindArticleByMessageID(id, "")`, which only consults the history index
   (`internal/processor/processor.go:276-299`), gets `ErrArticleNotFound` and calls `local430.Add(id)`: the
   miss is cached for the TTL. The POST handler's `local430.Remove` (BUG-8 fix) ran before, so it does not help.
3. IHAVE (`nntp-cmd-posting.go:176`) and TAKETHIS (`:274`) have the same window (235/239 before the commit).

The batch writer already has a commit barrier (`internal/database/db_batch_commit.go`: `CommitMark`,
`WaitCommitted`, used by the fetcher's `commitThenProgress`): a waiter asks the orchestrator to flush every
125 ms, so waiting for a commit normally takes ~150 ms, not 3 s. `WaitCommitted` on a group that captured
nothing returns at once, so marks may be taken for every candidate group name before the groups are resolved.

## Design
### A. POST waits for the group DB commit (correct 240 semantics)
- `internal/nntp/nntp-server.go` `ArticleProcessor`: new method
  `ProcessPostedArticle(article *models.Article, stop <-chan struct{}, timeout time.Duration) (int, error)`.
  Semantics: like `ProcessIncomingArticle`, but when the article was accepted (CasePass) it waits until the
  batch writer committed it to every target group, at most `timeout` (<= 0: no wait).
- `internal/processor/processor.go`: implementation: take `proc.DB.Batch.CommitMark(name)` for every name in
  `article.NewsgroupsPtr` (before `processArticle`, which calls NoteCaptured synchronously), run
  `processArticle(article, primary, false)`, and on CasePass call `WaitCommitted(mark, stop, timeout)` for each
  mark (sequentially; the remaining timeout is shared). Results: all nil → CasePass; `ErrBatchDropped` →
  `CaseError` with `fmt.Errorf("%w: not stored", common.ErrIntakeRejected)` (the handler answers 441 "Posting
  failed (not stored)"); `ErrBatchWaitTimeout`/`ErrBatchWaitStopped` → still CasePass (the article is queued and
  will be stored) plus one log line `[INTAKE] POST <id>: commit not confirmed within <timeout>: <err>`.
- `cmd/nntp-server/processor_adapter.go` forwards; test fakes (`hFakeProcessor`, `wiringTestProcessor`)
  implement it (the fake delegates to `ProcessIncomingArticle` and records the timeout it was given).
- `handlePost` calls `ProcessPostedArticle(article, c.server.stopChan-or-equivalent, c.server.PostCommitWait())`.
  IHAVE/TAKETHIS keep `ProcessIncomingArticle` (feeds: throughput, the peer retries anyway); they rely on B.
- `cmd/nntp-server/main.go`: `-post-commit-wait duration` (default 5s; 0 = reply 240 without waiting, the old
  behaviour); `NNTPServer.SetPostCommitWait`/`PostCommitWait()` (atomic, default 5 s in NewNNTPServer); in
  the startup log line. README nntp-server block: one line.

### B. Arrival grace for the negative cache + group hints (covers IHAVE/TAKETHIS and the history lag)
- `internal/nntp/nntp-cache-local.go`: a second small table on the server, `recentArrivals`
  (message-id → {groups []string, at time.Time}; same bounds as Local430: 250-octet ids, max entries, cleanup
  with the existing `run` loop). `NoteArrival(id, groups)` is called by POST/IHAVE/TAKETHIS right where they
  call `local430.Remove` (groups = the article's Newsgroups names as received; the processor may have kept
  fewer, that only makes some hints miss). `ArrivalGrace` = 10 s constant (> BatchInterval 3 s + history
  BatchTimeout 1 s + margin), not a flag.
- Lookup step (b) in `nntp-article-common.go`: before `local430.Check`:
  1. `if groups, ok := c.server.recentArrivals.Get(id); ok` (younger than ArrivalGrace): for each hinted group
     call `Processor.FindArticleByMessageID(id, group)` (that call looks in the group's DB first, no history
     needed) and serve the first hit through the existing `visibleInOtherGroup` path. Not found in any hint:
     fall through to the history lookup but **never** `local430.Add` for this id while the grace lasts
     (and skip `local430.Check` for it: a 430 cached just before the arrival must not win).
  2. Otherwise unchanged.
- `local430.Remove` stays (BUG-8).
- With A, a POST client sees 223 immediately (the row is committed, the hint finds it, the history may still
  lag). With B alone (IHAVE/TAKETHIS), the reader gets an uncached 430 until the commit (~150 ms to 3 s), then
  223; the 60 s poisoning is gone in both cases.

### C. Docs
- BUGS.md: replace "same-process arrivals are removed at once" with the new rule (POST waits for the commit,
  default 5 s; every same-process arrival gets a 10 s grace in which misses are not cached and the arrival
  groups are tried directly); T-nntp-article-1 (other-process arrival, 60 s) stays an accepted limitation.
- docs/audit/README.md BUG-8 row: note the window fix. internal/nntp/README.md: one bullet.

## Waves
### Wave 0 (inline, done on plan-post-430-window)
- Decision (user, 2026-10-16): option 2, POST waits for the commit; IHAVE/TAKETHIS do not wait.
- `internal/nntp/nntp-server.go`: interface method `ProcessPostedArticle(article, stop, timeout)`; `postWait`
  atomic; `NewNNTPServer` calls `SetPostCommitWait(DefaultPostCommitWait)`. `internal/nntp/nntp-cache-local.go`:
  `DefaultPostCommitWait` (5 s), `SetPostCommitWait`/`PostCommitWait` (d <= 0 → 0 = no wait). Stubs:
  `internal/processor/processor.go` (calls ProcessIncomingArticle), `cmd/nntp-server/processor_adapter.go`
  (forwards), `wiringTestProcessor` (delegates), `hFakeProcessor` (delegates, records the timeouts in `postWaits`).
### Wave 1
- slice `post-commit-wait`: `internal/processor/processor.go` (real `ProcessPostedArticle` with marks +
  WaitCommitted, timeout sharing, error mapping), `internal/processor/intake_test.go` or a new
  `internal/processor/post_commit_test.go` (scratch DB: CasePass only after the row is in the group DB;
  dropped batch → CaseError wrapping ErrIntakeRejected; timeout 0 → no wait; stop closed → CasePass + log;
  use the `downloadWaitCommitted`-style seam if a fake barrier is easier), `cmd/nntp-server/main.go`
  (`-post-commit-wait` flag, validation >= 0, startup log), `README.md` (one line). Checks: gofmt; vet; build;
  `go test -race ./internal/processor/... ./cmd/nntp-server/...`.
- slice `arrival-grace`: `internal/nntp/nntp-cache-local.go` (recentArrivals), `internal/nntp/nntp-article-common.go`
  (hint lookup + no-cache rule), `internal/nntp/nntp-cmd-posting.go` (POST uses ProcessPostedArticle with the
  server's wait; POST/IHAVE/TAKETHIS call NoteArrival), `internal/nntp/nntp-server-article_test.go` (harness:
  fake processor whose `FindArticleByMessageID(id, "")` misses while `(id, group)` hits → POST then STAT/HEAD/
  ARTICLE/OVER/HDR by message-id on the same and on a new connection answer 2xx with 0 local430 entries; IHAVE
  and TAKETHIS the same; a miss during the grace is not cached: after the fake starts returning the article,
  STAT answers 223 without waiting for any TTL; after the grace the old rules apply; BUG-8 test unchanged),
  `internal/nntp/fl_nntpdb_server_test.go` (bounds/cleanup of recentArrivals), `BUGS.md`, `docs/audit/README.md`,
  `internal/nntp/README.md`. Checks: gofmt; vet; build; `go test -race ./internal/nntp/... ./cmd/nntp-server/...`;
  `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` still lists only T-nntp-article-1.
### Wave 2
- slice `post-430-e2e`: `cmd/nntp-server/e2e_xref_test.go` or a new `e2e_post430_test.go` (binary: POST, then on
  the same connection and on a fresh one `STAT <id>` without GROUP → 223 immediately; `OVER <id>` → 224;
  IHAVE an article, poll `STAT <id>` until 223 within the deadline and assert it never flips back to 430;
  `-post-commit-wait 0` run: STAT right after POST may be 430 but must be 223 within the deadline on a new
  connection, i.e. not poisoned). Checks: `go test -race ./cmd/nntp-server/...`.

## Checks
gofmt -l internal/nntp internal/processor cmd/nntp-server; go vet ./...; go build ./...; the full CLAUDE.md
race list; `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` lists only T-nntp-article-1;
`./build_nntp-server.sh`.

## End-to-end (scratch `-data ./data-test-post430`, hostname example.org, port 11193)
1. Import two groups, create a posting user, start `pugleaf-nntp-server -nntptcpport 11193` (default
   `-post-commit-wait 5s`, log line shows it). POST an article to group a; on the same connection, without
   GROUP: `STAT <id>` → `223 0 <id>`, `HEAD <id>` → 221 with the Xref line (partial mode: none without a group;
   with `-xref full` the line appears once the history flushed, within ~1 s). On a new connection: `STAT <id>`
   → 223. The POST reply arrives within well under 1 s (log the measured time, no assertion).
2. IHAVE an article (same user): `235`, then `STAT <id>` on a new connection polled every 100 ms: the first
   answers may be 430, then 223, and never 430 again (poll for 5 s after the first 223).
3. Restart with `-post-commit-wait 0`: POST then immediate `STAT <id>` may be 430; on a new connection within
   5 s it is 223 (not poisoned).
4. `-post-commit-wait -1s` → exit 1 with a message. SIGINT → exit 0 for every run.

### Progress
- Wave 1 (merged into plan-post-430-window):
  - `post-commit-wait` (worktree-agent-a9f483327cd5b42df, 8bb1261 + fe75ca0, merge 395e236): `ProcessPostedArticle`
    takes marks before the hand-off, waits only on the groups the article was captured for (snapshot of
    article.NewsgroupsPtr under article.Mux), and is authoritative by the row: after each WaitCommitted (nil or
    dropped) it looks the row up in the group DB (`postArticleStored` seam); absent+nil → fresh mark and wait again
    until the deadline (50 ms recheck pause); absent+dropped → CaseError "not stored" (441); timeout/stop → CasePass
    + one log line. `-post-commit-wait` flag (default 5 s). Review MERGE; the two minors (per-group counter can
    return early under a concurrent feed; a drop of another article could 441 a stored post) fixed by the row check.
  - `arrival-grace` (worktree-agent-a4d7ee94769267805, 51c0fa5 + 6f051b7, merge b0e0862): `RecentArrivals`
    (10 s grace, bounds, cleaned on the local430 ticker), `noteArrival` after 240/235/239 (NoteArrival before
    Remove), `recentArrivalArticle` helper: hints resolved with `selectableGroup` and read directly from the group
    DB (no processor/history call, hidden copies skip to the next hint), used by step (b) and by XHDR by message-id;
    during the grace local430.Check/Add are skipped and a late Add is undone; docs. Review MERGE AFTER FIXES:
    MAJOR (a hint for an unknown group created its group DB on disk, SEC-2 pattern) fixed + test
    `unknown-group-hint`; minors 4-7 fixed (ordering race, BUG-8 test proves Remove again, docs wording, XHDR,
    test runtime 19 s → 3 s).
  - Merged tree (b0e0862): gofmt empty, vet, build, full race list green, known bugs only T-nntp-article-1.
  - Open (accepted): hints follow the GROUP rule (an admin-activated reserved name would be read); a POST whose
    article is dropped before a repeat round's fresh mark still gets 240 + log; in the first round a drop of
    another article in the same group can 441 a still-queued post.
