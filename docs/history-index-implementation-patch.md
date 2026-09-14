# History index: implementation patch (Sept 2026)

This records the analysis, design, the parallel agent work, integration, end-to-end tests and follow-up fixes that made the message-id history index work again. Base commit: `f1429de`. All work sits on branch `claude-get-the-history-index-working`, commits `ba4a7da` … `3307dcb` (the later `b3e19b4 active_files` is unrelated).

## Contents
1. [What the history index is for](#1-what-the-history-index-is-for)
2. [How it got here](#2-how-it-got-here)
3. [Bugs found in the analysis](#3-bugs-found-in-the-analysis)
4. [Design of the fix](#4-design-of-the-fix)
5. [How the pieces work now](#5-how-the-pieces-work-now)
6. [Parallel agent work](#6-parallel-agent-work)
7. [Integration](#7-integration)
8. [End-to-end test](#8-end-to-end-test)
9. [Follow-up fixes](#9-follow-up-fixes)
10. [Operations: flags, tools, rollout](#10-operations-flags-tools-rollout)
11. [Known open issues](#11-known-open-issues)
12. [Commit list](#12-commit-list)

---

## 1. What the history index is for

One global index across all per-newsgroup databases: **message-id → IDs of the newsgroups (main DB `newsgroups.id`) the article is stored in**. It is needed for:
- NNTP `ARTICLE/HEAD/BODY/STAT <message-id>` without a selected group (otherwise every group DB would have to be searched);
- duplicate checks for incoming `POST` / `IHAVE` / `TAKETHIS`;
- fetcher crosspost reuse: copy an article already stored in another group instead of downloading it again.

The group databases stay the source of truth (`articles.message_id UNIQUE`, `INSERT OR IGNORE`). History is an index that can always be rebuilt from them.

## 2. How it got here

Reconstructed from git:

| When | Commit | What happened |
|---|---|---|
| 2025-08-03 | 61312d3 v0.4.3.3 | INN2-style design: append-only `history.dat` (`msgid \t 0x1 \t group:artnum \t time`) plus 16 SQLite files × 256 tables mapping a short hash to file offsets. Written **after** the batch insert (db_batch.go called `AddProcessedArticleToHistory(item, group, artnum)`). `const ENABLE_HISTORY = false`. |
| 2025-08-04 | cc2875e | BUGS.md: "requesting articles via message-id does not work", "posting does not work because history and hashdb is disabled", "history + hashdb eats memory and has IO issues". |
| 2025-10-02 | 1d29f62 | Writer returns early on shutdown when disabled. |
| 2025-11-06 | fb0c9a7 "prepare nntp-server history" | Dropped `history.dat` and short hashes. Rows became `message_id TEXT PK, newsgroups TEXT`. Writing moved **before** the insert (`processArticle`). Turned on (`const ENABLE_HISTORY = true`). |
| 2025-11-17 | 0ea7c09 "testing" | Turned off again: `var ENABLE_HISTORY = false` as package default, plus explicit `false` in web, nntp-fetcher and post-queue. The writer was only started when enabled, but nntp-transfer, rslight-importer and nntp-server still added a WaitGroup count for it, so they hung on exit. |

The UseShortHashLen setting (flags, value locked in the main DB) has had no effect since fb0c9a7.

## 3. Bugs found in the analysis

Items marked *(repro)* were reproduced with standalone tests on a copy of the package.

### History write path (when enabled)
- **A. Shutdown hang** *(repro)*: `Add` put the item in a pointer-keyed `dbQueued` map, and three early returns (empty id, `Response != CaseLock`, duplicate found) never removed it. One duplicate was enough for `CheckNoMoreWorkInHistory()` to stay false forever. Orchestrators and `WaitForBatchCompletion` looped on "History still has work", and the writer never called Done.
- **B. Writer stall** *(repro)*: plain multi-row `INSERT` (UPSERT commented out), and any `Exec` error was retried forever inside the transaction. The same message-id queued twice → `UNIQUE constraint failed` every 100ms, and nothing else was ever committed. The queue then filled and `Add` blocked intake.
- **C. Cache duplicate items** *(repro)*: `MsgIdItemCache.resize()` swapped in an empty map and unlocked before rehashing. 8 goroutines × 200,000 IDs got more than one item for 94,684 IDs (item count 295,549). All dedupe state was per pointer, so this triggered B. A second trigger for B: the writer marked items `CaseDupes` with a 3s TTL before the commit.
- **D. Cache never evicts** *(repro)*: `CleanExpiredEntries` never removed `CaseLock`/`CaseWrite` or `Response==0` items and logged a full `%#v` dump for every one of them each cycle. Every NNTP `STAT <msgid>` created such an item, so a client could grow memory and logs at will.
- PRAGMAs were set via `db.Exec` on a pool of 8 connections, so only one connection got them (part of the "IO issues"). 4096 `CREATE TABLE IF NOT EXISTS` and a `wal_checkpoint(TRUNCATE)` ran at every process start.
- The `err` variable in `executeDBTransaction` was shadowed, so its critical rollback + `os.Exit` defer never saw errors.

### Intake / NNTP
- **E.** Non-bulk `processArticle` rejected every new article, because nothing ever set `Response = CasePass`. So POST/IHAVE/TAKETHIS never worked, with history on or off.
- **F.** IHAVE/TAKETHIS sent 435/439 on duplicates but didn't return, so a second reply desynced the stream.
- `readArticleData` checked `currentHeader == ""`, which is true for the first header, so every header line was dropped. Every incoming article failed with "no Newsgroups header". It also never set `HeadersJSON`/`RefSlice`.
- No NNTP command except POST checked authentication. Once intake worked, anyone could have injected articles via IHAVE/TAKETHIS.
- `GetArticleFromAnyNewsgroupDB` never called `groupDB.Return()`.

### Tools / binaries
- nntp-server never closed `db.StopChan` or called `proc.Close()`, so it always hung on exit.
- history-rebuild: never added to `db.WG` (negative WaitGroup panic); `-analyze-only` printed made-up numbers; `-read-offset` read the removed history.dat; `-clear-first` wasn't implemented.
- expire-news: age expiry scanned into a non-pointer (every run failed) and paged with OFFSET while deleting (skipping rows).

## 4. Design of the fix

### Decisions made by the user
- Use history for message-id lookups, intake duplicate checks and fetcher crosspost reuse.
- On by default in every process that stores articles (`-history=false` to opt out); nntp-transfer skips it.
- Expired articles: keep their entries by default (INN-style "remember"); remove them with `expire-news -trim-history` or `history-rebuild -trim`.
- Rewrite the cache as simple maps.
- Work in parallel agent waves on Opus 5 with high effort.

### Design rules
1. **Index, not truth.** Group DBs decide what is stored; history bugs can't corrupt articles.
2. **Record after commit.** Add `(message-id, group ID)` in PHASE 3 of `processNewsgroupBatch`, after the article has its number (the Aug 2025 placement).
3. **Idempotent writes.** UPSERT that merges group IDs.
4. **Reads verify.** A hit counts only when the group DB returns the article; stale IDs are skipped.
5. **History owns its lifecycle.** Own WaitGroup count, string API with no shared `MessageIdItem` pointers, switch read once at startup.
6. **Keep the on-disk layout.** 16 × 256 files and tables (`SHARD_16_256`) and the same row schema. Keep the `@AI DO NOT CHANGE` TTL constants and the critical `os.Exit` in `executeDBTransaction`.

## 5. How the pieces work now

### History index (on disk)
- **Files:** `<data>/history/hashdb_0..f.sqlite3`, each with tables `_00.._ff`: `message_id TEXT PRIMARY KEY, newsgroups TEXT` (`WITHOUT ROWID`). Routing uses md5(message-id): the 1st hex char picks the file, the next two pick the table. The directory follows `-data` (`filepath.Join(db.GetDataDir(), "history")`).
- **Connections:** a dedicated driver `sqlite3_history` with a ConnectHook sets `temp_store`, `mmap_size` and `wal_autocheckpoint` on every connection. The DSN is `?_journal_mode=WAL&_synchronous=OFF&_busy_timeout=30000&_txlock=immediate&_cache_size=-8000`.
- **Startup:** tables are created only if a file lacks them.
- **Writes:** one prepared single-row statement per op, one transaction per file, files written in parallel.
  - add: `INSERT … ON CONFLICT(message_id) DO UPDATE SET newsgroups = CASE WHEN coalesce(newsgroups,'')='' THEN excluded.newsgroups ELSE newsgroups||','||excluded.newsgroups END WHERE instr(','||coalesce(newsgroups,'')||',', ','||excluded.newsgroups||',') = 0`
  - remove: `UPDATE … SET newsgroups = trim(replace(','||newsgroups||',', ','||?||',', ','), ',')`, then `DELETE` rows left with an empty `newsgroups`.
- **Errors:** only busy/locked errors on `Begin` are retried, backing off from 100ms to 5s, up to 100 attempts, then `log.Fatalf`. Any error after `Begin` goes through the kept rollback + `os.Exit(1)` defer.

### History API (`internal/history`)
```go
var ENABLE_HISTORY = false // package default; binaries set it from -history (default true)
var ErrHistoryClosed = errors.New("history is closed")
func NewHistory(cfg *HistoryConfig, mainWG *sync.WaitGroup) (*History, error) // reads ENABLE_HISTORY once; mainWG.Add(1) when enabled
func (h *History) Enabled() bool
func (h *History) AddArticle(messageID string, groupID int64)    // queued, idempotent, blocks on backpressure
func (h *History) RemoveArticle(messageID string, groupID int64) // queued
func (h *History) Exists(messageID string) (bool, error)
func (h *History) LookupGroups(messageID string) ([]int64, error) // nil,nil = not found
func (h *History) CheckNoMoreWorkInHistory() bool                 // pending == 0
func (h *History) SetDatabaseWorkChecker(checker DatabaseWorkChecker)
func (h *History) GetStats() HistoryStats                         // TotalAdds, TotalRemoves, TotalCommitted, Flushes, Pending, Errors, …
func (h *History) Close() error                                   // idempotent; returns after the final flush
```
- **Safety:** every method is safe on a nil or disabled instance.
- **Queueing:** `enqueue` increments `pending` before checking `closed`, and the writer checks `closed` before `pending`, so no op is lost at shutdown.
- **Writer exit:** it exits when `Close()` was called, `pending == 0`, and the batch system (the work checker) reports no work. It then runs `wal_checkpoint(TRUNCATE)` and calls `mainWG.Done()`.

### MsgIdItemCache (in memory, per process)
- **Purpose:** a short-term notepad, message-id → `*MessageIdItem`, whose `Response` field says `CaseLock` (being processed), `CaseDupes` (stored a moment ago) or `CaseError` (failed, may retry).
- **Structure:** 256 shards (FNV-1a), each a mutex plus a map. `GetORCreate` returns exactly one item per ID.
- **Cleanup:**
  - Terminal/unset states are evicted after `CachedEntryExpires`.
  - `CaseLock`/`CaseWrite` are evicted after `CachedEntryExpires + StuckEntryMaxAge` (10 min) and counted as stuck.
  - Eviction only removes the map entry and never blanks fields.
- **Kept API:** `MsgIdCache`, `NewMsgIdItemCache`, `GetORCreate`, `Delete`, `Clear`, `Stats`, `DetailedStats`, `CleanExpiredEntries`, `StartCleanupRoutine`.
- **Removed:** the L1 cache (`history_L1-cache.go`, dead code) and `MessageIdItem.GroupThreading/Arrival/NewsgroupIDs/MessageIdHash`.

### Pipeline
- **Write after commit:** `db_batch.go` PHASE 3 resolves the group ID once per batch. For each committed article it calls `ProcessorInterface.AddArticleToHistory(messageID, groupID)` and sets the cache item to `CaseDupes`.
- **Intake (non-bulk `processArticle`), claim then check:**
  - item already `CaseLock`/`CaseWrite`/`CaseDupes` → duplicate;
  - otherwise set `CaseLock`, then `History.Exists`: error → `CaseError`; found → `CaseDupes`; else process.
  - The bulk path (fetcher, importer, PostQueue) has no history read; the per-group `ExistsMsgIdInArticlesDB` check stays.
- **Processor for the NNTP server:**
  - `CheckMessageID(messageID) int`: item in progress → `CaseRetry`; cache `CaseDupes` or `History.Exists` → `CaseDupes`; error → `CaseError`; else `CasePass`.
  - `FindArticleByMessageID(messageID, currentGroup)`: current group first, then history groups; first hit wins; a miss wraps `nntp.ErrArticleNotFound`.
- **NNTP `getArticleData` (message-id form):**
  1. current group;
  2. `local430` (string-keyed, only filled on definite misses);
  3. `FindArticleByMessageID(msgid, "")`.
  - The reply carries article number 0 when the article was found outside the current group.
  - A message-id lookup never changes `currentArticle`.
- **Fetcher reuse:**
  - `DownloadArticles` checks `reuseStoredArticle` in the XHDR loop, then sends a rebuilt article straight to `ReturnQ` (counted in `queued` and `reused`).
  - The rebuild splits `HeadersJSON` (raw header lines joined by `\n`), adds `""`, then the body lines, and parses them with `nntp.ParseLegacyArticleLines`.
- **Shutdown and WaitGroups:**
  - `OpenDatabase` does `db.WG.Add(2)` for the two orchestrators; `NewHistory` adds its own count.
  - Binaries: `close(db.StopChan)` → `proc.Close()` (`WaitForBatchCompletion` + `History.Close`) → `db.WG.Wait()` → `db.Shutdown()`.
  - Orchestrators exit after ~2s idle (16 checks × 125ms).

### NNTP reply codes (IHAVE/TAKETHIS)
| Case | IHAVE | TAKETHIS (article always read first) |
|---|---|---|
| no processor / wrong args | 502 / 501 | 502 / 501 (article still consumed) |
| not authenticated / no posting permission | 480 / 502 | 480 / 502 |
| duplicate | 435 | 439 `<id>` |
| in progress / check error | 436 | 439 `<id>` |
| accepted, send | 335 | – |
| Message-ID header differs from argument | 437 | 439 `<id>` |
| no Message-ID header | the argument is used | the argument is used |
| processed OK | 235 | 239 `<id>` |
| processor says duplicate | 437 | 439 `<id>` |
| other processing error | 436 | 439 `<id>` |

## 6. Parallel agent work

**Agent type:** `pugleaf-history-worker`, defined in `~/.claude/agents/pugleaf-history-worker.md` with `model: claude-opus-5` and `effort: high`. A definition file only loads at session start, so the session had to be restarted with `claude --continue`.

**Running the agents:**
- Each agent ran with `isolation: "worktree"` under `.claude/worktrees/`.
- Integration happened in a separate worktree `../go-pugleaf-history-index` on branch `history-index`, because the 28 staged files in the main checkout blocked `git merge` there.
- All agent worktrees and branches were removed after merging.

**Gotcha:** the tool created new worktrees at an unrelated old commit (`01ff542`, v0.4.7-1). Every wave-1 agent had to `git reset --hard f1429de` first, and wave-2 briefs told agents to reset to `history-index` (7bcef8c) before starting.

**Rules in every brief:**
- edit only the listed files;
- keep `@AI DO NOT CHANGE` code;
- no new dependencies;
- prefix test helpers per file;
- temp dirs only, never `./data`;
- commit on the agent's own branch, never push;
- report out-of-scope issues instead of fixing them.

### Wave 1 (4 agents, parallel, no shared files)

#### plumbing → `ba4a7da`
- `OpenDatabase` does `db.WG.Add(2)`; the per-binary `Add(2)` and history `Add(1)` were removed from web, nntp-fetcher, nntp-server, nntp-transfer and rslight-importer.
- nntp-server shutdown order fixed; its `defer db.Shutdown()` removed.
- expire-news: `getArticleBatch` returns `[]expireCandidate{Num, DateSent}` with keyset paging (`article_num > ?`); the progress log only fires every 100 deletions.
- WaitGroup accounting after the change: web 2+1 cron, all other binaries 2; history-rebuild and nntp-analyze balanced automatically.
- Reported, not fixed:
  - nntp-server `log.Fatalf` paths skip `db.Shutdown()`;
  - a NULL `date_sent` fails expire-news (fixed later in wave 2);
  - the expire-news progress log only fires when the batch size divides 10000;
  - web might call `StartCronManager` on a nil `CronManager` (unverified);
  - the history writer needed its own `Add(1)` (done by history-core).

#### cache → `12d33be`
- `MsgIdItemCache` rewritten with 256 shards, `StuckEntryMaxAge`, and the unexported constructor `newMsgIdItemCache()`.
- `ThreadingInfo`, `Arrival` and `GroupThreading` removed; `history_L1-cache.go` deleted.
- `cache_test.go`: 8 × 200,000 IDs gives exactly one pointer each (1.22s with -race), plus eviction, stats and re-creation tests.
- Reported:
  - admin page utilisation / `max_buckets` became meaningless (fixed in wave 2);
  - history-rebuild relied on the global singleton (rewritten in wave 2).

#### history-core → `0dfa31b`
- The API above, the UPSERT writer, the per-connection settings via ConnectHook, and table creation only when missing.
- `HistoryStats` no longer carries a mutex.
- A non-16/256 `ShardMode` is forced to `SHARD_16_256`.
- Temporary wrappers `Add(item)`, `Lookup(item, quick)` and `LookupMID` kept other packages compiling; they were removed at integration.
- `history_test.go`: dedupe, merge/remove, op order, legacy `''`/`NULL` rows, invalid ops, 16-goroutine concurrency, Close drains/idempotent/WaitGroup behaviour, reopen, per-connection pragmas, disabled/nil instances.
- Reported:
  - `CheckNoMoreWorkInMaps` dereferences a nil `sq.proc` and floods `[CRON-SHUTDOWN]` logs (log rate-limited in wave 2);
  - history-rebuild leftovers (rewritten in wave 2);
  - unused `HistoryConfig` fields `CacheExpires`/`CachePurge`/`UseShortHashLen`;
  - mains still read the package variable.

#### nntp → `bffb4da`
- `Local430` keyed by string; `getArticleData` checks the current group, then local430, then history via a throwaway item.
- `GetArticleFromAnyNewsgroupDB` now calls `Return()` (the function was later deleted).
- IHAVE/TAKETHIS require auth (480/502) and send one reply per command.
- `readArticleData` split into `readArticleLines` + `parseIncomingArticleLines` (using `ParseLegacyArticleLines`) + `buildIncomingArticle`, plus `extractHeaderValue`.
- Tests in `nntp-history-parse_test.go`: parser round-trip, incoming parser, header extraction, Local430.
- Behaviour change: incoming `Bytes` = body length (same as the fetcher); `Xref` is kept in `HeadersJSON`.
- Reported:
  - a message-id lookup changed `currentArticle` (fixed in wave 2);
  - TAKETHIS 501/502 didn't read the article (fixed in wave 2);
  - no Message-ID header/argument comparison (fixed in wave 2);
  - `sendArticleContent` uses `PrintfLine` directly.
- The agent stopped once while a cold race build was compiling and needed a nudge to finish and commit.

**Wave 1 integration:**
- Fast-forward plumbing, then merges of cache, nntp and history-core, with no conflicts.
- Build, vet and race tests green at `7bcef8c`.

### Wave 2 (3 agents, parallel)

#### wiring → `608ac3a`
- PHASE 3 history writes; `AddArticleToHistory` added to `ProcessorInterface`.
- `[CRON-SHUTDOWN]` logs rate-limited to once per 5s per kind.
- `processor.go`: `AddArticleToHistory`, `CheckMessageID`, `FindArticleByMessageID`; `HistoryDir` follows `-data`; `Lookup` / `AddProcessedArticleToHistory` removed.
- `threading.go`:
  - claim-then-check intake;
  - pre-insert add and both `MainDBGetNewsgroup` loops removed;
  - item set to `CaseDupes` when no group was queued.
- `ArticleProcessor` interface changed; both adapters updated.
- `-history` flag in web, nntp-server, nntp-fetcher and rslight-importer; nntp-transfer sets `false`; the post-queue line removed.
- Admin page cache stats reworked (shards, occupied, largest, items per shard, skew); the old template keys are still filled.
- Tests: `nntp-wiring_test.go` (fake processor over `net.Pipe`, all IHAVE/TAKETHIS reply sequences) and `processor/wiring_test.go`.
- Reported:
  1. `processArticle` duplicates entries in `NewsgroupsPtr` for NNTP articles, so their fields are never cleared (open);
  2. message-id replies showed another group's article number (fixed in §9);
  3. the current group was queried twice (fixed in §9);
  4. with history off, a duplicate IHAVE is answered 235/239;
  5. an article just POSTed is a definite miss until its batch commits and gets cached in local430 for ~15s;
  6. `MainDBGetNewsgroup` is called once per batch even with history off;
  7. early bulk-path returns can leave items in `CaseLock` (cleaned up after `StuckEntryMaxAge`).

#### tools → `e959d36`
- **history-rebuild rewritten** without processor or cache:
  - rebuild (resumable via `<history>/rebuild.progress`, `-restart`);
  - `-validate-only` (via `LookupGroups`, reports missing vs wrong group);
  - `-analyze-only` (read-only COUNT per file/table plus groups-per-message-id histogram);
  - `-trim`, which only removes a reference when the group is really gone (`sql.ErrNoRows`), its DB file is missing, or the article is missing;
  - `-read-offset`, `-clear-first` and `-show-collisions` removed;
  - exit codes 0 ok, 1 errors, 130 interrupted;
  - helpers in `helpers.go` with tests, README rewritten.
- **expire-news:**
  - `toNullTime` skips NULL, unparsable or zero dates and counts them;
  - `-trim-history` reads the message-ids in the delete transaction and calls `RemoveArticle` after commit;
  - clean shutdown in both modes; README updated.
- **BUGS.md** nntp-server section updated.
- **Deviations:**
  - trim does not use `ExistsMsgIdInArticlesDB`/`GetNewsgroupNameByID`, because both treat DB errors as "not found";
  - it checks the group DB file path directly so it doesn't create empty DBs;
  - the progress file is removed after a complete run;
  - an article-number gap jumps straight to the next article.
- **Smoke test** on 2 groups × 25,000 articles: rebuild, validate at 100%, analyze, resume, Ctrl+C (exit 130), trim removed 25,100 references, expire trim removed 24,875.
- **Reported:**
  1. ~15s orchestrator shutdown and the "sq.proc not set" flood (fixed in §9);
  2. writes are slow: a 30,000-op flush took ~3s;
  3. `history-rebuild.sh`/`scripts.sh` pass `-nntphostname` (fixed in §9);
  4. `expire-news -group name -prune` does nothing because `MaxArticles` stays 0;
  5. read-only analyze leaves `-wal`/`-shm` files;
  6. `DefaultHistoryDir`/`UseShortHashLen` leftovers;
  7. "missing overview table" warning for every chunk.

#### reuse → `0cca1b7`
- `proc_reuse.go`: `ReuseCrossposts`, `reuseStoredArticle`, the pure `storedArticleLines` (strips `\r`, refuses headers containing an empty line), and a `ReuseStats` hits/misses/errors counter.
- `DownloadArticles`: group ID resolved once; reused items get `CaseLock` and go straight to `ReturnQ`; a `reused` atomic counter is added to the final log line.
- nntp-fetcher gets the `-reuse-crossposts` flag.
- `proc_reuse_test.go`: 6 tests.
- Reported:
  1. `GetNewsgroupNameByID` logs every missing ID;
  2. `GetArticleByMessageID` fills `ArticleCache` for the source group;
  3. the XHDR goroutine returns on shutdown without notifying the consumer (older issue);
  4. an unchecked `*item.MessageID` dereference in an error log.

## 7. Integration

1. **Merges:** reuse, then wiring (conflict in the nntp-fetcher flag block, resolved as `history.ENABLE_HISTORY = *useHistory` and `processor.ReuseCrossposts = *reuseCrossposts && *useHistory`), then tools.
2. **`3af3c3d`:** removed the temporary wrappers, `MessageIdItem.NewsgroupIDs/MessageIdHash`, `HistoryFileName`, and the now unused `GetArticleFromAnyNewsgroupDB`; adapted the tests.
3. **Checks:** `go build ./...`, `go vet ./internal/... ./cmd/...`, and race tests for history, nntp, processor, history-rebuild and expire-news, all green.
4. **Merge into the main checkout:** `git rm --cached transfer.log transfer.log.shorted` (files already deleted), `git stash push`, `git merge --ff-only history-index`, `git stash pop --index`. The status was identical before and after.

## 8. End-to-end test

The test ran inside the integration worktree, in test roots `data-histtest/` and `data-histtest2/` (covered by `/data*` in `.gitignore`), with binaries built with `-ldflags "-X main.appVersion=$(cat appVersion.txt)"`. Binaries refuse to start without an app version.

**Input:** `legacy-sqlite3/` (untracked) holds 25 RockSolid group DBs with 18,151 articles, 18,133 distinct message-ids and 11 crossposts (up to 3 groups). Sections come from `./etc/menu.conf` plus `./etc/<section>/groups.txt`. The hostname `i2pn2.pugleaf.net` resolves.

| Step | Command / check | Result |
|---|---|---|
| Import | `rslight-importer -etc ../etc -spool $L -nntphostname i2pn2.pugleaf.net -threads 4` | first run hung in `db.WG.Wait()`: `LegacyImporter.Close()` never closed the processor, so the history writer never exited (confirmed via goroutine dump). Fixed in `3134cbe`; afterwards 18,150 imported, clean exit in 84s |
| Index = storage | script comparing group DBs and history | 18,132 distinct message-ids stored, 18,132 rows, 0 missing, 0 extra, 0 group-set mismatches, 11 crossposted |
| Rebuild | `history-rebuild` twice, `-validate-only`, `-analyze-only` | 18,132 rows both times; coverage 100%; histogram 18,121 / 4 / 7 IDs in 1 / 2 / 3 groups |
| NNTP server | scripted client, 22 scenarios (see §5 codes) | 22/22 pass; new POST/IHAVE/TAKETHIS articles present in history; SIGINT shutdown 16s at the time |
| Trim | `expire-news -group rocksolid.social -days 1 -force -trim-history` | 226 rows removed, 3 crossposted rows kept their other group, 0 unrelated rows changed |
| Reuse | second root with only the `localhost` provider (port 11120) → local nntp-server; fetch `rocksolid.programming`, then `rocksolid.shared.i2p` | second fetch `reused: 7` of 629; the 7 reused copies are identical to the source and the original import |

Test setup notes:
- `nntpmgr -create` failed with a FOREIGN KEY error, and a hand-inserted user with `web_user_id NULL` failed at login ("converting NULL to int64"). Both are fixed in §9.
- The user's real `./data` was later imported with the new code by the user (reported as "ran fine").

## 9. Follow-up fixes

Commit `3307dcb`:
- **NNTP users** (`db_nntp_users.go`): insert `NULLIF(?, 0)` for `web_user_id`; all five selects use `COALESCE(web_user_id, 0)`. Verified: `nntpmgr -create` stores NULL, and login returns `281`.
- **Message-id reply number** (`nntp-article-common.go`):
  - `getArticleData` returns `(article, replyNum)`: the current-group number, or 0 for hits in other groups;
  - `send*Content` take `replyNum`, so the shared cached article is never modified;
  - the global lookup passes `""` as the current group (already checked).
  - Verified: `STAT` without a group gives `223 0`; inside the group, the real number (614); from another group, `223 0` / `220 0`.
  - The wiring test asserts `223 0`, an unchanged `DBArtNum`, and an empty `currentGroup` argument.
- **Shutdown** (`db_batch.go`): `DefaultShutDownCounter` 120 → 16 (~2s of consecutive idle checks, any work resets it); the "sq.proc not set" log is rate-limited. nntp-server shutdown dropped from 16s to 2.4s.
- **Scripts:** `history-rebuild.sh` and `scripts.sh` no longer pass `-nntphostname`.

## 10. Operations: flags, tools, rollout

- **`-history`** (default true): web, nntp-server, nntp-fetcher, rslight-importer. nntp-transfer never uses history.
- **`-reuse-crossposts`** (default true, needs history): nntp-fetcher.
- **history-rebuild:** `-data`, `-progress`, `-batch-size`, `-verbose`, `-pprof`, `-restart`, `-validate-only`, `-analyze-only`, `-trim`; `-useshorthashlen` is accepted but has no effect.
- **expire-news:** `-group`, `-days`, `-dry-run`, `-force`, `-batch-size`, `-respect-expiry`, `-prune`, `-trim-history`, `-help`, `-data`.
- **Import legacy data:**
  ```
  go build -ldflags "-X main.appVersion=$(cat appVersion.txt)" -o build/rslight-importer ./cmd/rslight-importer
  ./build/rslight-importer -etc ./etc -spool ./legacy-sqlite3 -nntphostname i2pn2.pugleaf.net
  ```
- **After an import or expiry without trim:** `history-rebuild -validate-only`, optionally `history-rebuild -trim`.
- **Resetting all groups** (`rslight-importer -YESresetallgroupsYES`): delete `data/history` as well.
- **`history-rebuild.sh`** still deletes `data/history` without asking (its safety line is commented out) and still sets the obsolete `history_use_short_hash_len`. With the idempotent rebuild the delete isn't needed.

## 11. Known open issues

- `processArticle` duplicates `NewsgroupsPtr` entries for NNTP articles, so their fields are never cleared in PHASE 3.
- With `-history=false`, a duplicate IHAVE/TAKETHIS is answered 235/239 (the group DB still prevents a second copy).
- An article POSTed moments ago can be cached as a 430 miss in local430 for ~15s until its batch commits.
- History writes are relatively slow (~3s for 30,000 ops in one flush).
- The fetcher's XHDR goroutine returns on shutdown without notifying its consumer.
- `GetNewsgroupNameByID` logs every unknown ID; `GetArticleByMessageID` during reuse fills the article cache for the source group.
- `expire-news -group <name> -prune` does nothing for a single named group (`MaxArticles` stays 0).
- Leftovers: `history.DefaultHistoryDir`, `HistoryConfig.CacheExpires/CachePurge/UseShortHashLen`, the UseShortHashLen DB setting and flags.
- nntp-server `log.Fatalf` paths skip `db.Shutdown()`.
- NNTP `CHECK` / full peering is not implemented (out of scope).

## 12. Commit list

| Commit | Summary |
|---|---|
| `ba4a7da` | history plumbing: WaitGroup ownership in OpenDatabase, nntp-server shutdown, expire-news scan fix |
| `12d33be` | history cache: sharded MsgIdItemCache, evict stuck entries, drop dead L1 cache |
| `bffb4da` | nntp: message-id reads without cache pollution, IHAVE/TAKETHIS auth and single replies, fix readArticleData headers |
| `0dfa31b` | history core: string API, idempotent UPSERT writer, per-connection SQLite settings |
| `0cca1b7` | fetcher: reuse crossposts already stored in another group via history |
| `608ac3a` | history wiring: record after commit, intake duplicate check, message-id lookup API, -history flags |
| `e959d36` | history tools: rebuild/validate/analyze/trim for message-id index, expire-news -trim-history |
| `3af3c3d` | history: remove wave-1 compatibility wrappers and unused helpers |
| `3134cbe` | rslight-importer: close the processor on shutdown so the history writer flushes and exits |
| `3307dcb` | fix NNTP user web_user_id NULL handling, article number 0 for message-id replies, faster orchestrator shutdown, history-rebuild script flag |

The merge commits `6dbb85d`, `8de2ace`, `7bcef8c`, `9c07a13` and `5b11037` come from the integration branch.
