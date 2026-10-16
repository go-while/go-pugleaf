# Plan: xref-leftovers

## Context
Four pre-existing items noted by the xref-history / xref-batched-lookup verifications (`.claude/plans/done/`),
none caused by those plans:

1. **expire-news "no such table: overview" warning.** `cmd/expire-news/main.go:626-632` runs
   `DELETE FROM overview WHERE article_num IN (...)` for every delete batch and logs
   `Warning: failed to delete overview batch (table may not exist)` when it fails. No current group DB has an
   `overview` table: the unified schema (`internal/database/migrations/0001_single_db_schema.sql`) only creates
   `articles`, `threads`, `thread_cache`, `cached_trees`, `tree_stats` (+ `article_num_seq` from 0010); the old
   overview schema lives in `migrations/old/`. So every run prints one warning per batch for nothing.
   `deleteDerivedThreadRows` (`main.go:682`) already shows the right pattern: skip "no such table", fail on
   anything else.
2. **An emptied group answers `211 0 0 0`.** `selectGroup` (`internal/nntp/nntp-cmd-group.go:46-70`) reads
   `NNTPGroupWatermarks` (`internal/database/nntp_reads.go:69`: MIN/MAX of visible articles, 0/0 when none) and
   answers `0 0 0` when `high <= 0`. RFC 3977 §6.1.1.2 allows that, but recommends `low = high + 1` for a group
   that had articles ("211 0 4000 3999"), so a reader keeps its place and knows the next number. The group DB
   knows that number: `article_num_seq.last_num` (migration 0010, never decreases; `DeleteNewsgroup`-style resets
   set it to 0 on purpose). The main DB's `newsgroups.high_water`/`low_water` are only used by `LIST ACTIVE` /
   `LIST ACTIVE wildmat` (`nntp-cmd-list.go:45`, `nntp-cmd-list-wildmat.go:16`), where `last_article` is the
   high mark; `expire-news` updates `message_count` and `last_article` (`updateNewsgroupCounters`, `main.go:826`)
   but never `low_water`, so after an expiry LIST ACTIVE can show a low mark below the first surviving article.
3. **nntp-server has no bind-address flag.** `NNTPServer.Start` (`internal/nntp/nntp-server.go:209,216`) listens
   on `":port"` (all interfaces) for both the plain and the TLS port; `config.ServerConfig.NNTP` has no address
   field, `cmd/nntp-server/main.go` no flag. Operators who want loopback-only (behind stunnel/a proxy) or one
   interface cannot have it. (The webserver is out of scope here.)
4. **`cmd/post-queue` has no tests.** `run()` (`cmd/post-queue/main.go:33`) parses `flag.CommandLine` from
   `os.Args` and does everything inline: open DB, cleanup flags, reset `in_processing`, providers, pools,
   one-shot `ProcessPendingPosts` or daemon `Run`. `internal/postmgr/adf_opt_postmgr_test.go` already has a fake
   NNTP server (`flPqFake*`, answers POST with a configurable code or hangs) and `flPqPool` to build a pool
   against it; the DB API is in `internal/database/db_post_queue.go` (`InsertPostQueueEntry`,
   `GetPendingPostQueueEntries`, `ResetAllPostQueueProcessing`, `CleanupPostedEntries`, `GetPostQueueStats`, ...).

## Design
### 1. expire-news overview delete
Remove the `DELETE FROM overview` statement and its warning (the table does not exist in any group DB the
current migrations create; `migrations/old/` is not applied). Keep the comment trail: one line saying the
legacy overview table is gone since the unified schema. `cmd/expire-news/README.md`: drop any mention of the
overview table if present. Test: `TestToolsTestDeleteArticles*` in `main_test.go` (or a new small test) runs a
delete batch against a `toolsTestGroupDB` with the log captured and asserts no "overview" line is logged and the
articles/threads rows are gone.

### 2. GROUP on an empty group, LIST ACTIVE low mark after expiry
- `internal/database/nntp_reads.go`: `NNTPGroupNextArticleNum(groupDB) (int64, error)` reads
  `SELECT MAX(last_num, COALESCE((SELECT MAX(article_num) FROM articles), 0)) + 1 FROM article_num_seq WHERE id = 1`
  (the exact expression the inserts use, so the answer is the number the next article gets); a group DB without
  the row (should not happen after migration 0010) returns `MAX(article_num)+1`.
- `selectGroup`: when there is no visible article, answer `211 0 next next-1` (`next` from
  `NNTPGroupNextArticleNum`; a never-used group gives `211 0 1 0`), and set `currentFirst = next`,
  `currentLast = next-1`, `currentArticle = 0` (no current article, as today: §6.1.1.2 "the current article
  number is invalid"). `LISTGROUP` on an empty group answers the same `211` line with an empty list. Update the
  comment block above `selectGroup` ("decision 14") and `internal/nntp/README.md` if it states `0 0 0`.
- `expire-news`: `updateNewsgroupCounters` also writes `low_water` = lowest remaining `article_num` (0 → keep
  the next number, i.e. `last_article + 1`) and `high_water = last_article` through the existing
  `UpdateNewsgroupWatermarks`, so `LIST ACTIVE` (`g.LastArticle`, `max(low_water,1)`) matches GROUP after an
  expiry. One UPDATE statement is fine (extend the existing one) as long as the column names stay those of
  `query_UpdateNewsgroupWatermarks`.
- Tests: `internal/nntp` harness (`hGroup` with 0 rows, and a group whose rows were all deleted after insert so
  `article_num_seq.last_num` > 0): GROUP answers `211 0 1 0` / `211 0 N+1 N`, LISTGROUP the same with an empty
  list, `NEXT`/`LAST`/`STAT` without a number answer 420 as today; `LIST ACTIVE` after a simulated expiry shows
  the updated marks (the harness writes the main DB). `cmd/expire-news`: after a delete batch, `newsgroups`
  has `low_water` = first survivor, `high_water` = `last_article`; emptied group → `low_water = last_article+1`.
  Byte-exact: existing tests that assert `211 0 0 0` (grep `"211 0 0 0"` in `internal/nntp/*_test.go`,
  `cmd/nntp-server/*_test.go`, `docs/audit`) are updated to the new form; docs/audit decision 14 wording amended.

### 3. nntp-server bind address
- `internal/config/config.go`: `ServerConfig.NNTP.Addr string \`json:"addr"\`` ("" = all interfaces), default "".
- `internal/nntp/nntp-server.go` `Start`: `net.JoinHostPort(s.Config.NNTP.Addr, strconv.Itoa(port))` for both
  listeners (JoinHostPort handles IPv6 literals); log lines say "listening on <addr>:<port>" (for "" keep
  "port %d"). `Serve(l, isTLS)` is unchanged (tests bind their own listener).
- `cmd/nntp-server/main.go`: `-nntpbind string` (default "", help: "IP address to bind the NNTP listeners to
  (default: all interfaces; e.g. 127.0.0.1 or ::1)"); an invalid value (`net.ParseIP == nil` and not "") is
  fatal; in the startup log line. README.md nntp-server block: one line for `-nntpbind`.
- Tests: `internal/nntp` unit test that `Start` with `Addr: "127.0.0.1"` and port 0 is not possible (Start
  needs a port > 0) → test the address formatting through a small helper `listenAddr(addr string, port int)
  string` ("" → ":1119", "127.0.0.1" → "127.0.0.1:1119", "::1" → "[::1]:1119"); the e2e binary test
  (`cmd/nntp-server/e2e_xref_test.go` or `server_e2e_test.go`) already starts the binary on a loopback port:
  add `-nntpbind 127.0.0.1` to one run and assert the greeting arrives, and that `-nntpbind 999.1.1.1` exits 1.

### 4. post-queue tests
- Refactor `cmd/post-queue/main.go` so the flow is testable without touching behaviour: `run()` becomes
  `run(args []string) int` using `flag.NewFlagSet("post-queue", flag.ContinueOnError)` (usage/`-help` output
  unchanged; `main` calls `run(os.Args[1:])`); the signal channel and the provider/pool construction stay as
  they are. No change to exit codes (0 / 1 / 130) or log lines.
- New `cmd/post-queue/main_test.go` (scratch `t.TempDir()` data dir, `database.OpenDatabase` like
  `internal/postmgr/adf_opt_postmgr_test.go` does; copy the fake NNTP server `flPqFake*` idea into the package,
  since test helpers cannot be imported across packages):
  - `-help` → 0, no DB opened (TempDir stays empty).
  - cleanup: `-cleanup-posted-entries` deletes posted rows only; `-cleanup-older N` deletes old rows only;
    both flags together; returns 0 and the counts; a DB open failure (unwritable `-data`) → 1.
  - no posting-enabled provider → 0, rows untouched, stale `in_processing` rows reset to pending first.
  - one-shot run with a fake provider answering 240: pending rows become `posted_to_remote = 1`, exit 0;
    answering 441: `attempts` incremented, error recorded, exit 0 (a failed post is not a tool error);
    a provider port that is closed: pool creation / posting error path, exit as the code defines today
    (assert the actual value, document it in the test).
  - `-max-batch N` posts at most N rows per run.
  - a signal before the batch (send `os.Interrupt` to the test process is not safe; instead the refactor exposes
    the shutdown channel: `runWith(args, shutdown <-chan struct{})` used by `run`) → 130 and nothing posted;
    a stop during a hanging provider (`flPqFakeHang`) → 130 and the remaining rows back to pending
    (mirrors `TestAdfOptPostmgrStopResetsRemainder` at the tool level).
  - all waits bounded by `PUGLEAF_TEST_DEADLINE` (`testWaitFromEnv` pattern), no wall-clock assertions.
- Providers: inserted directly into the main DB (`providers` table) with `posting = 1`, host 127.0.0.1 and the
  fake's port; look at how `getPostingProviders` filters (enabled + posting) and `createPostingPools` builds the
  `nntp.Pool` (connection counts, timeouts) and keep the fake compatible (greeting `200`, `MODE READER`,
  `POST` → `340` then the configured final code, `QUIT`).

## Waves
### Wave 0 (inline)
- `internal/config/config.go`: `NNTP.Addr` field (shared struct, one owner).
- `internal/database/nntp_reads.go`: `NNTPGroupNextArticleNum` (used by the nntp slice and the expire slice).
### Wave 1
- slice `expire-overview-marks`: items 1 and the expire-news half of 2. Owned: `cmd/expire-news/main.go`,
  `cmd/expire-news/main_test.go`, `cmd/expire-news/README.md`, `internal/database/queries.go` (only if
  `UpdateNewsgroupWatermarks` needs a sibling that also sets `last_article`/`message_count`; otherwise untouched).
  Checks: gofmt; vet; build; `go test -race ./cmd/expire-news/... ./internal/database/...`.
- slice `group-empty`: the nntp half of 2. Owned: `internal/nntp/nntp-cmd-group.go`, `internal/nntp/README.md`,
  `internal/nntp/nntp-server-group_test.go` (or the existing group test file; name it after the current file),
  `docs/audit/README.md` (decision 14 wording), any `internal/nntp/*_test.go` / `cmd/nntp-server/*_test.go` line
  that asserts `211 0 0 0`. Checks: gofmt; vet; build; `go test -race ./internal/nntp/... ./cmd/nntp-server/...`;
  `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` still only T-nntp-article-1.
- slice `nntp-bind`: item 3. Owned: `internal/nntp/nntp-server.go` (Start + `listenAddr` helper),
  `internal/nntp/nntp-server-bind_test.go` (new), `cmd/nntp-server/main.go`, `cmd/nntp-server/e2e_xref_test.go`
  (one `-nntpbind` run + invalid value), `README.md` (nntp-server flag block). Checks: gofmt; vet; build;
  `go test -race ./internal/nntp/... ./cmd/nntp-server/...`.
- slice `post-queue-tests`: item 4. Owned: `cmd/post-queue/main.go`, `cmd/post-queue/main_test.go` (new),
  `cmd/post-queue/fake_nntp_test.go` (new). Checks: gofmt; vet; build; `go test -race ./cmd/post-queue/...`
  (and `./internal/postmgr/...` unchanged).

## Checks
gofmt -l cmd/expire-news cmd/nntp-server cmd/post-queue internal/nntp internal/database internal/config;
go vet ./...; go build ./...; the full CLAUDE.md race list; `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/
./cmd/nntp-server/` lists only T-nntp-article-1; `./build_expire-news.sh`, `./build_nntp-server.sh`,
`./build_post-queue.sh` (check exact names with `ls build_*.sh`).

## End-to-end (scratch `-data ./data-test-leftovers`, hostname example.org, port 11192)
1. Create two groups and post 3 articles into group a (nntp-server on 127.0.0.1 via `-nntpbind 127.0.0.1`,
   `-nntptcpport 11192`); `GROUP xref.test.b` (never used) → `211 0 1 0`; `GROUP xref.test.a` → `211 3 1 3`.
2. `expire-news -data ./data-test-leftovers -group xref.test.a -days 1 -force` on backdated copies of all three
   articles: the log has no "overview" warning; `GROUP xref.test.a` → `211 0 4 3`; `LISTGROUP xref.test.a` →
   `211 0 4 3 xref.test.a` + empty list; `LIST ACTIVE` shows `xref.test.a 3 4 y` (last_article 3, low 4);
   posting a new article gets number 4 and `GROUP` → `211 1 4 4`.
3. `-nntpbind 999.1.1.1` → exit 1 with a clear message; `-nntpbind ::1` + `-nntptcpport 11192` → greeting on
   `[::1]:11192`, connection refused on 127.0.0.1:11192.
4. `post-queue -data ./data-test-leftovers -help` → 0; `-cleanup-older 30` on the empty queue → 0, "deleted 0".
   (A real posting run needs a provider: covered by the package tests with the fake server, not here.)
5. Every server exits 0 on SIGINT.

### Progress
- Wave 0 (df1c7d2): `config.ServerConfig.NNTP.Addr`; `Database.NNTPGroupNextArticleNum` (+ test in nsh_reads_test.go).
- Wave 1 (all merged into plan-xref-leftovers):
  - `expire-overview-marks` (worktree-agent-a7aa8a603c8216e96, 64bc68a, merge 1073b81): overview delete + warning gone;
    `updateNewsgroupCounters` writes message_count/last_article/high_water/low_water in one UPDATE; emptied group keeps
    its place (last_article = high_water = next-1, low_water = next; deviation accepted: last_article no longer reset
    to 0). Review MERGE, 3 minors.
  - `nntp-bind` (worktree-agent-aefa9f58554f35bac, 31f53b6, merge 51b4f88): `listenAddr`, both listeners on
    Config.NNTP.Addr, `-nntpbind` with net.ParseIP check, `bind=%q` in the startup line, e2e X24/X32. Review MERGE, 2 minors.
  - `group-empty` (worktree-agent-a0d81df3278dc61fa, 93036e5, merge 991319c): `211 0 next next-1`, LISTGROUP lists
    only when count > 0, tests for empty/emptied/all-hidden. Review MERGE, 2 minors. docs/audit/README.md has no
    decision-14 text (it lives in .claude/plans/done/nntp-server-hardening.md), left alone.
  - `post-queue-tests` (worktree-agent-a03f4cab717a702e3, eb726ee + c0924c5, merge 9934297): `run(args)`/`runWith(args,
    stopChan)` with a FlagSet, signal watcher installed at the old point only when stopChan is nil; 9 tests with a fake
    NNTP server; ~45 s package time (2 OpenDatabase/Shutdown cycles per scenario at ~2.4 s each). Deviations accepted:
    CleanupPostedEntries only deletes posted rows (test asserts that), usage header "Usage of post-queue:", closed
    provider port exits 0 (transient failure). Review MERGE, 2 minors.
  - Minors fixed inline (63235c7): expire test seeds stale values for the never-used group so the write is proven,
    log capture restored via t.Cleanup, hidden-rows/LIST ACTIVE note in comment + README; bind test only skips ::1;
    `TestGrpGroupEmptyNextNumFails` (403, nothing selected, clean stop); `preloader.RepairNewsgroupWatermarks`
    (web `-repair-watermarks`) now keeps an emptied group's place with the same next-number rule instead of 0/0/0/1.
  - Dropped/deferred: zoned link-local `-nntpbind` values (ParseIP rejects them; documented limit); recover-db
    consistency report flags an emptied group ("Main DB Last Article: N / Articles Max Num: 0"), report only;
    the closed-port test releases its port before use (negligible race on loopback); 2.4 s DB shutdown cost.
- Merged tree (63235c7): gofmt empty, vet, build, full race list green, known bugs only T-nntp-article-1.

### Verification (6405d1a)
- Verifier: gofmt/vet/build green, full race list green (uncached: post-queue 45 s, nntp 170 s, expire-news 8 s),
  known bugs only T-nntp-article-1, five builds ok.
- End-to-end on ./data-test-leftovers (example.org, 127.0.0.1:11192): `GROUP xref.test.b` → `211 0 1 0`, LISTGROUP
  same + empty list, STAT/NEXT/LAST → 420; after expire-news of all three articles (no "overview" line; counters line
  "0 articles, last article 3, high water 3, low water 4") `GROUP xref.test.a` → `211 0 4 3`, `ARTICLE 1` → 423,
  `LIST ACTIVE` → `xref.test.a 3 4 y`, the next post gets number 4 and `GROUP` → `211 1 4 4`; `-nntpbind 999.1.1.1`
  exits 1 with the message, `-nntpbind ::1` listens on [::1]:11192 only; post-queue -help 0, -cleanup-older 30 → 0
  ("deleted 0"), -bogus → 2 with "Usage of post-queue:"; all server runs exit 0 on SIGINT.
- Found by the verifier, pre-existing (not from this plan): `STAT <msgid>` without a selected group right after a
  POST in the same process answers 430 for up to the local430 TTL (60 s): the POST handler removes the id from the
  negative cache, but the history batch (1 s flush) has not indexed it yet, the lookup misses and re-adds it
  (`internal/nntp/nntp-article-common.go:127-135`). BUGS.md says same-process arrivals are removed at once, which is
  not true in that window. Left as a follow-up.
- Fixed after verification: `-cleanup-older` help text said "all post_queue entries", the code deletes posted ones only.
