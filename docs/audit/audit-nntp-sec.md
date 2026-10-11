# Audit audit-nntp-sec (base 66c36f8)

Security, resource-exhaustion and concurrency audit of the NNTP **server** (reading,
posting/transfer, auth, caches, TLS and shutdown). Client/transfer code is `audit-nntp-client`;
RFC command conformance is `audit-nntp-rfc`; the DB side of SEC-2/SEC-6 and the batch writer are
`audit-db`. This report follows `docs/audit/README.md` (D1) and K6. All `path:line` anchors are at
66c36f8 and were re-checked with `grep -n` on the symbol.

## Scope and method

Files read in full at 66c36f8:

- `internal/nntp/nntp-server.go`, `nntp-server-cliconns.go`, `nntp-server-statistics.go`
- `internal/nntp/nntp-cmd-basic.go`, `nntp-cmd-auth.go`, `nntp-cmd-group.go`, `nntp-cmd-list.go`,
  `nntp-cmd-helpers.go`, `nntp-cmd-xhdr.go`, `nntp-cmd-xover.go`, `nntp-cmd-posting.go`,
  `nntp-cmd-article.go`, `nntp-cmd-{head,body,stat,reader}.go`
- `internal/nntp/nntp-article-common.go`, `nntp-cache-local.go`, `nntp-auth-manager.go`
- DB functions the handlers call: `GetGroupDB`/`initGroupDB`/`Return` (`db_groupdbs.go`),
  `GetOverviews`, `GetOverviewsRange`, `GetOverviewByArticleNum` (`thread_cache.go`),
  `GetHeaderFieldRange`, `GetArticleByNum`, `GetArticleByMessageID`, `MainDBGetNewsgroup`,
  `GetActiveNewsgroups` (`queries.go`); `GroupToHash` (`groups_hashmap.go`),
  `GetNewsgroupPointer`/`ExpireCache` (`db_batch.go`), `SanitizeGroupName` (`utils.go`),
  `cronDBEvery`/`cleanupIdleGroups` (`database.go`)
- `internal/database/db_nntp_users.go` and `internal/database/nntp_auth_cache.go`, in full
- `cmd/nntp-server/main.go` (startup and shutdown)

Tools (from `docs/audit/tools/`, filtered to this scope): staticcheck flags
`(*ClientConnection).parseArticleHeadersShort` unused (`nntp-cmd-helpers.go:25`); golangci-lint/errcheck
flags the unchecked `db.UpdateNNTPUserLastLogin` return in `AuthenticateNNTPUser`
(`db_nntp_users.go:325`); gosec flags `queries.go:806` G101 (a false positive on a SQL const).
govulncheck's NNTP hits are all in `nntp-client-commands.go` (client, out of scope).

Repros: a temporary `internal/nntp/zz_audit_nntp-sec_test.go` using the wave-0 harness
(`newTestServer`, `hGroup`, `hUser`, `runIsolated`, `hFakeProcessor`), run with
`go test -race -run TestZZ -v ./internal/nntp/`. SEC-1 and the QUIT crash went through `runIsolated`.
The file was **deleted before commit** (D1 / K5). No RFC fetch was needed for this slice.

## Summary

Counts by severity: **critical 2, high 3, medium 8, low 4** (17 findings). By category:
security 12, bug 4, robustness 1.

| severity | findings |
|----|----|
| critical | A-nntp-sec-1, SEC-1 |
| high | SEC-2, SEC-3, SEC-7 |
| medium | SEC-4, SEC-5, SEC-6, BUG-1, BUG-2, BUG-7, A-nntp-sec-2, A-nntp-sec-6 |
| low | BUG-8, A-nntp-sec-3, A-nntp-sec-4, A-nntp-sec-5 |

Five most important:

1. **A-nntp-sec-1 (critical)**: a pipelined `QUIT` followed by any command crashes the whole
   server process with a nil-pointer dereference (`handleConnection` has no `recover`). Unauthenticated.
2. **SEC-1 (critical)**: `XHDR <hdr> 1-9223372036854775807` spins a core forever (103% CPU measured).
   `Stop()` cannot end it and `main`'s `wg.Wait()` hangs. Unauthenticated.
3. **SEC-2 (high)**: `LISTGROUP <anyname>` creates a directory and SQLite file for a group name that
   was never validated (4 files from 4 requests, measured). Unauthenticated disk/inode exhaustion.
4. **SEC-3 (high)**: no byte limit on command or article lines. `Server.NNTP.MaxArtSize` and
   `newsgroups.max_art_size` are never read (an 8 MiB command line was accepted, measured).
5. **SEC-7 (high)**: LISTGROUP loads every overview row of a group into memory before sending
   (+32 MiB heap for a 50 000-row group, measured). Unauthenticated.

SEC-6 (old password valid for up to 15 min after a change) is confirmed with a repro and rated
medium; the revoked-user half is already fixed (LO:F22) and is only verified here, not re-reported.
No data race was found under `-race` with 100 concurrent clients (see `## Races`).

## Findings

### A-nntp-sec-1: pipelined QUIT crashes the whole process (nil conn deref)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-1 | critical | security | `nntp-cmd-basic.go:53-64`, `nntp-server-cliconns.go:60,81`, `nntp-server.go:196,202-214` | CONFIRMED |

**Scenario:** an unauthenticated client sends `QUIT\r\nMODE READER\r\n` in a single write → the
server process dies with SIGSEGV. One short write takes the server down for every client.

**Evidence:** `handleQuit` (`nntp-cmd-basic.go:53`) calls `c.Close()` (`:59`), which sets
`c.conn = nil` (`:63`) but does **not** close `c.textConn`. The `Handle` loop
(`nntp-server-cliconns.go:75-93`) returns to `c.textConn.ReadLine()`. Because `QUIT` and the next
command arrived together, `textproto`'s `bufio.Reader` has already buffered `MODE READER`, so
`ReadLine` returns it without touching the closed socket. Line 81 then calls `c.UpdateDeadlines()`,
which calls `c.conn.SetReadDeadline` on the nil `c.conn` (`:60`). `handleConnection`
(`nntp-server.go:202`) has no `recover()`, so the panic in that goroutine kills the process.
Measured stack:

```
panic: runtime error: invalid memory address or nil pointer dereference [signal SIGSEGV ...]
nntp.(*ClientConnection).UpdateDeadlines(...)   internal/nntp/nntp-server-cliconns.go:60
nntp.(*ClientConnection).Handle(...)            internal/nntp/nntp-server-cliconns.go:81
nntp.(*NNTPServer).handleConnection(...)        internal/nntp/nntp-server.go:211
                                                internal/nntp/nntp-server.go:196 (go s.handleConnection)
```

Repro: `TestZZQuitPipelineCrash` (pipelined write inside `runIsolated`): the child exits with
status 2 and the SIGSEGV above.

**Fix (M):** in `Handle`, leave the loop when `c.conn == nil` (or when `handleCommand` signals a
close) before `UpdateDeadlines`; make `UpdateDeadlines` a no-op on a nil conn; add a `recover()` in
`handleConnection` so one connection can never crash the server. The nil guard alone is S.

---

### SEC-1: XHDR unbounded send loop pins a core and hangs shutdown

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-1 | SEC-1 | critical | security | `nntp-cmd-xhdr.go:85,97-104`, `queries.go:1878-1880` | CONFIRMED |

**Scenario:** `GROUP x`, then `XHDR subject 1-9223372036854775807` (or `XHDR subject 1-` on a group
whose `last_article` is huge). The DB read is clamped to 1000 rows, but the send loop walks the whole
client range with a map lookup per iteration and no I/O, so it never returns. Unauthenticated.

**Evidence:** `GetHeaderFieldRange` (`queries.go:1872`) clamps `endNum = startNum + 1000`
(`:1878-1880`) and returns a `map[int64]string` of at most 1001 entries. `handleXHdr` calls it at
`nntp-cmd-xhdr.go:85` and then loops
`for articleNum := startNum; articleNum <= endNum; articleNum++` (`:97`) over the **unclamped**
range (up to `MaxInt64`, where `articleNum++` wraps to `MinInt64` and the condition stays true),
calling `c.sendLine` only for the few numbers present in the map. The remaining iterations miss the
map and continue: no read and no write, so neither a client disconnect nor `Stop()` interrupts them.
An unsupported header (`XHDR foo ...`) gets an empty map with no DB query at all and spins the same
way. The goroutine is `handleConnection`, counted in `s.wg`, which `cmd/nntp-server/main.go:148`
waits on at shutdown, so shutdown hangs forever.

Measured (`TestZZSec1XHDRSpin`, isolated child, process CPU via `getrusage`):
`SEC1_CPU_PERCENT=103.1 over 3s` (one core == 100%), after `221 Header subject follows`.

**Fix (M):** iterate the returned rows in article-number order instead of the raw range, as XOVER
does with `GetOverviewsRange`; clamp the requested range to the group's high-water mark. Also move
the 1000-row limit into SQL (`LIMIT`) instead of a Go clamp (see BUG-2).

---

### SEC-2: LISTGROUP creates a group DB for any unvalidated name

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-2 | SEC-2 | high | security | `nntp-cmd-group.go:39-51`, `db_groupdbs.go:117-190,194-247` | CONFIRMED (NNTP side) |

**Scenario:** unauthenticated `LISTGROUP comp.lang.fictional.whatever` for a group that does not
exist creates `<DataDir>/db/<md5>/<sanitized>.db` with its directory and full schema. Repeating with
fresh names exhausts inodes and disk.

**Evidence:** `handleGroup` (`nntp-cmd-group.go:9`) validates with `MainDBGetNewsgroup` (`:20`) and
checks `Active` (`:25`) before it sets `c.currentGroup` (`:30`); an unknown group gets 411.
`handleListGroup` (`:39`) does neither: it calls `GetGroupDB(groupName)` directly (`:51`). Finding no
map entry, `GetGroupDB` creates one and runs `initGroupDB` (`db_groupdbs.go:164,194`), which calls
`createDirIfNotExists` (`:224`), `sql.Open` (`:232`) and `migrateGroupDB` (`:246`). The file and
schema are created for a name that was never checked against `newsgroups`. `handleListGroup` does
not set `c.currentGroup`, so this path does not "select" the group for later commands.

Measured (`TestZZSec2ListgroupFiles`): `SEC2_DBFILES before=0 after=4 created=4` for four
non-existent names. `MainDBGetNewsgroup(<each>)` still returns `sql: no rows in result set`
afterwards: the group is not a real newsgroup, yet its DB exists on disk.

In-memory and open-handle cost of the same path: see A-nntp-sec-4.

**Fix (S):** NNTP handlers call `MainDBGetNewsgroup` (returns `sql.ErrNoRows`) and check `Active`
before `GetGroupDB`, as D4 Appendix B specifies. Auto-creation in `GetGroupDB` is shared with the
batch writer and stays; the guard belongs in the callers. The intake half of SEC-2 is out of scope
here (see Not covered).

---

### SEC-3: no byte limits on command or article lines; MaxArtSize never read

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-3 | SEC-3 | high | security | `nntp-server-cliconns.go:77`, `nntp-cmd-posting.go:221-260`, `config/config.go:131` | CONFIRMED |

**Scenario:** a client sends an arbitrarily long line (command or article line). It is read and
buffered whole; there is no per-line or per-article byte cap, and the configured maxima are ignored.
Many connections that each send large lines exhaust memory.

**Evidence:** command lines use `c.textConn.ReadLine()` (`nntp-server-cliconns.go:77`) with no size
limit. Article reception (`readArticleLines`, `nntp-cmd-posting.go:221`) caps only line *counts*
(`maxLines, maxHead := 16384, 1024`, `:224`); each of up to 16384 lines may be arbitrarily long, and
the whole head and body are held in slices. TAKETHIS reads the article (`:128`) before it checks
authentication (`:145`), so this buffering is reachable without auth. `Server.NNTP.MaxArtSize`
(`config/config.go:131`, default 32768) and `newsgroups.max_art_size` are never read in
`internal/nntp` (grep: `max_art_size` appears only in provider/newsgroup CRUD in `queries.go`).

Measured (`TestZZSec3CommandLineSize`, `TestZZSec3Alloc`): command lines of 1 KiB, 1 MiB and
**8 MiB** were read and answered (`500 Unknown MODE: AAAA...`). For the 8 MiB line,
`runtime.MemStats.TotalAlloc` grew by **111.8 MiB** (3 runs, identical). This is cumulative
allocation in the in-process test (server read and command parsing, plus the `Unknown MODE: %s`
reply that echoes the full argument and the client reading it back), not peak RSS.

**Fix (M):** wrap command reads in a byte-limited reader, and enforce `MaxArtSize` (server, then
per group) in `readArticleLines`, replying 501 or closing with 400 when it is exceeded. Stop echoing
the argument in error replies.

---

### SEC-4: unbounded command-stats map growth from unknown commands

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-4 | SEC-4 | medium | security | `nntp-server-cliconns.go:112-116,154-155`, `nntp-server-statistics.go:57-61` | CONFIRMED |

**Scenario:** an unauthenticated client sends many distinct unknown command words. Each is stored as
a new key in `Stats.commandCounts` before dispatch. The map grows without bound and never shrinks.

**Evidence:** `handleCommand` computes `command := strings.ToUpper(parts[0])` from the client line
and calls `c.server.Stats.CommandExecuted(command)` (`:112-116`) *before* the switch. Unknown
commands reach the `default:` branch (`:154-155`), which sends `500` and never calls
`rateLimitOnError`, so there is no delay at any `ErrorDelay` setting. `CommandExecuted`
(`nntp-server-statistics.go:57-61`) does `s.commandCounts[command]++` with no whitelist and no cap;
`Reset` is never called by the server. Because of SEC-3, each key can be as long as a full
(unbounded) command line.

Measured (`TestZZSec4StatsMap`): one connection sending 5000 distinct unknown words gave
`SEC4_STATSMAP_ENTRIES=5000` in 0.14 s.

**Fix (S):** record stats only for recognised commands (count inside the switch cases, or map
unknown words to one `UNKNOWN` key), and rate-limit the `default:` branch.

---

### SEC-5: reading never requires auth; AUTHINFO weaknesses

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-5 | SEC-5 | medium | security | `nntp-cmd-auth.go:9-47` (481 at `:34`), `nntp-server-cliconns.go:119-156`, `nntp-auth-manager.go:47-90` | CONFIRMED |

**Scenario:** several RFC 4643 and policy gaps on the auth path.

**Evidence (code trace):**
- **No read auth.** GROUP, LISTGROUP, ARTICLE/HEAD/BODY/STAT, XOVER, XHDR and LIST never check
  `c.authenticated`; only POST, IHAVE and TAKETHIS do. The whole read surface, including the
  file-creating and memory-heavy paths (SEC-2, SEC-7, A-nntp-sec-4), is anonymous. This may be
  intended for a public reader; it is listed in the attack-surface table.
- **AUTHINFO in plaintext.** `handleAuthInfo` (`nntp-cmd-auth.go:9`) accepts `USER`/`PASS`
  (`:18`, `:23`) on a plain connection. There is no STARTTLS and no 483, so credentials cross the
  wire in clear text.
- **No back-off on failure.** The `481` reply (`:34`) has no `rateLimitOnError()`, so password
  guessing is not throttled at the protocol layer. The same missing delay keeps the timing gap of
  A-nntp-sec-6 measurable.
- **Re-AUTH after success.** `USER`/`PASS` are handled the same way whether or not
  `c.authenticated` is already true (`:18-42`), so a second AUTHINFO after success re-runs auth and
  can switch identity. RFC 4643 wants `502`.
- **Dead auth helpers.** `AuthManager.CheckGroupAccess` (`nntp-auth-manager.go:47`), `CanPost`
  (`:58`), `IsAdmin` (`:66`) and `CheckConnectionLimit` (`:77`) are never called, so the per-user
  `maxconns` column is not enforced; only the global `MaxConns` in `serve` applies. See the function
  inventory.

**Fix (M):** require TLS (or STARTTLS) before AUTHINFO; add `rateLimitOnError` to the 481 path;
reject a second AUTHINFO with 502; wire `CheckConnectionLimit` into `handleAuthInfo` or remove it.

---

### SEC-6: old password keeps working after a change (auth cache)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-6 | SEC-6 | medium | security | `db_nntp_users.go:136-147,281-328`, `nntp_auth_cache.go:11-16,50-90`, `db_init.go:22` | CONFIRMED (old-password half) |

**Scenario:** a user or admin changes an NNTP password with `UpdateNNTPUserPassword`. The auth cache
keys a hit on `sha256(password-at-login)` and is not invalidated, so a connection presenting the
**old** password still authenticates until the 15-minute TTL expires. Medium rather than high:
reading needs no auth anyway (SEC-5), the window is at most 15 minutes after the last successful
login, and the attacker must already know the old password.

**Evidence:** `AuthenticateNNTPUser` (`db_nntp_users.go:281`) first calls
`NNTPAuthCache.Get(username, password)` (`:284`). `Get` returns a hit when the stored
`sha256(oldPass)` matches (`nntp_auth_cache.go:82-89`). `UpdateNNTPUserPassword` (`:136-147`) only
runs an `UPDATE` and never calls `InvalidateNNTPUserAuth`; `DeactivateNNTPUser` and `DeleteNNTPUser`
don't either. TTL is `NNTP_AUTH_CACHE_TIME = 15 * time.Minute` (`db_init.go:22`).

Measured (`TestZZSec6OldPassword`): authenticate, `UpdateNNTPUserPassword`, authenticate again:
`oldPass err=<nil>  newPass err=<nil>`. Both the old and the new password are accepted. (A login
with the new password misses the cache, passes bcrypt and overwrites the entry; after that the old
password fails.)

**Verified, not re-reported (LO:F22, revoked-user half):** on every cache hit
`AuthenticateNNTPUser` calls `refuseDisabledNNTPUser` (`:294`, `:338-350`), which re-reads
`nntp_users.is_active` and `users.disabled` and drops the cache entry on refusal (a read error fails
closed). A deactivated or disabled user is rejected right away despite a cached entry. This half is
correct at 66c36f8.

**Fix (S):** `NNTPAuthCache` lives in memory in each process, and passwords are changed from
`cmd/web` or `cmd/nntpmgr`, not from `cmd/nntp-server` (OPS-2), so an invalidation call in
`UpdateNNTPUserPassword` cannot reach the server's cache. Instead, validate each hit against the DB
row that `AuthenticateNNTPUser` already loads on a hit (`GetNNTPUserByID(userID)`,
`db_nntp_users.go:286`): store the bcrypt hash string (or `updated_at`) in `AuthCacheEntry` at `Set`,
compare it with `user.Password` (or `user.UpdatedAt`) on `Get`, and on a mismatch invalidate the
entry and fall through to the full bcrypt check. Calling `InvalidateNNTPUserAuth` from
`UpdateNNTPUserPassword`/`DeactivateNNTPUser`/`DeleteNNTPUser` is a secondary nicety that only helps
when the change happens in the same process.

---

### SEC-7: LISTGROUP / GetOverviews load every row into memory and log each call

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-7 | SEC-7 | high | security | `nntp-cmd-group.go:59`, `queries.go:698-715` | CONFIRMED |

**Scenario:** `LISTGROUP <biggroup>` (unauthenticated) reads *all* overview rows of the group into a
slice before it sends anything. On a large group the allocation grows with the group size, and every
call is logged.

**Evidence:** `handleListGroup` calls `GetOverviews(groupDB)` (`nntp-cmd-group.go:59`).
`GetOverviews` (`queries.go:698`) runs `query_GetOverviews` (`:696`, `SELECT ... FROM articles`, no
`LIMIT`) and appends every row to `out` (`:706-713`), and logs
`GetOverviews: group '%s' fetching overviews ...` on every call (`:699`). There is no streaming and no
range (LISTGROUP ignores its optional range: BUG-4, rfc slice).

Measured (`TestZZSec7Memory`): a group with 50 000 rows → `GetOverviews` returned 50 000 overviews
with `heap_delta_MB=32.13` for one call. N concurrent LISTGROUPs multiply this.

**Fix (M):** select only `article_num`, stream rows to the client as `rows.Next()` advances, honour
the LISTGROUP range, and move the per-call log to debug.

---

### BUG-1: GetArticleByNum never sets DBArtNum (current article stuck at 0)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-1 | BUG-1 | medium | bug | `queries.go:892-914`, `nntp-article-common.go:60` | CONFIRMED |

**Scenario:** `ARTICLE 5` (or HEAD/BODY by number) succeeds but leaves the current article at 0, so
a following `XOVER` or `XHDR <hdr>` with no range returns `420 Current article number is invalid`.

**Evidence:** `GetArticleByNum` (`queries.go:892`) scans into `artnum` and sets
`a.ArticleNums[groupDB.NewsgroupPtr] = artnum` (`:906`) but never `a.DBArtNum`. Compare
`GetArticleByMessageID` (`:919`), which does `a.DBArtNum = artnum` (`:930`). In
`retrieveArticleCommon`, `c.currentArticle = article.DBArtNum` (`nntp-article-common.go:60`) stores
0. STAT is not affected because `getArticleData` builds its result with
`DBArtNum: wantArticleNum` (`:179-182`). Whether the bug shows depends on the article cache (an entry
put by the message-id path carries a correct `DBArtNum`); with `ENABLE_ARTICLE_CACHE=false` it always
reproduces.

**Fix (S):** set `a.DBArtNum = artnum` in `GetArticleByNum`.

---

### BUG-2: XOVER / XHDR silently truncated to 1001 rows (no SQL LIMIT)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-2 | BUG-2 | medium | bug | `queries.go:1816-1818` (`GetOverviewsRange`), `:1878-1880` (`GetHeaderFieldRange`) | CONFIRMED |

**Scenario:** `XOVER 1-5000` returns only the first 1001 rows. The client sees a silent gap, not an
error.

**Evidence:** both functions clamp in Go (`if endNum-startNum > 1000 { endNum = startNum + 1000 }`,
`queries.go:1816-1818` and `:1878-1880`) and have no SQL `LIMIT`. XOVER iterates the returned rows
(`nntp-cmd-xover.go:79`), so it truncates quietly without spinning; XHDR shares the clamp but spins
on the send side (SEC-1).

**Fix (S):** raise or parameterise the clamp and let the client page on, or move the limit into SQL
so clamp and query agree; document the cap.

---

### BUG-7: shutdown, error-ordering and listener-leak defects

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-7 | BUG-7 | medium | robustness | `nntp-server.go:95,108-137,217-255`, `nntp-cmd-posting.go:227-229`, `nntp-server-cliconns.go:86` | CONFIRMED |

Several defects, confirmed by code trace. Medium rather than high: the shutdown hang needs a client
that keeps sending commands or the SEC-1 spin, and the TLS listener leak is masked in the shipped
binary by `log.Fatalf`.

- **Stop() does not close clients or wait.** `Stop()` (`nntp-server.go:217-255`) closes `shutdown`
  and the listeners, but it leaves live client connections alone and explicitly does not call
  `wg.Wait()` (comment `:240-244`). An idle client is bounded: its read deadline (`IdleTimeout`,
  60 s by default) is set at connect (`:210`) and before every command (`cliconns.go:81`). A client
  that keeps sending commands, or a handler stuck in the SEC-1 spin, keeps its `handleConnection`
  goroutine alive, and `main` blocks on `wg.Wait()` (`cmd/nntp-server/main.go:148`) indefinitely.
- **CronLocal430 goroutine leak.** `NewNNTPServer` starts `go server.local430.CronLocal430()`
  (`nntp-server.go:95`). That loop (`nntp-cache-local.go:15-20`) has no stop channel and is never
  joined: one leaked goroutine per server instance.
- **TLS cert failure leaves the plain listener running.** `Start` starts the plain listener
  (`serveLocked`, `:115`) before the TLS block. If `tls.LoadX509KeyPair` fails (`:120-122`), `Start`
  returns an error while `running` is still false (set only at `:137`). A caller that keeps running
  then has a live accept goroutine that `Stop()` skips (`if !s.running { return nil }`, `:221-223`).
  The shipped binary masks this with `log.Fatalf` (`cmd/nntp-server/main.go:133-134`); the defect is
  in the package API for embedded callers.
- **"Too large" ordering, QUIT logged as error.** `readArticleLines` closes `c.textConn` on overflow
  (`nntp-cmd-posting.go:228`) and the caller still sends 441/436 afterwards. The normal QUIT path ends
  with a failed `ReadLine` on the closed conn, which `Handle` returns as an error and
  `handleConnection` logs as `Connection error from ...` (`nntp-server.go:212`).

**Fix (M):** give `CronLocal430` a stop channel that `Stop` closes; track clients and close them in
`Stop`; on a TLS setup failure close the plain listener that was already started (or load the cert
before starting any listener); treat a normal close after QUIT as no error.

---

### BUG-8: local430 negative cache not invalidated when the article arrives

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-8 | BUG-8 | low | bug | `nntp-cache-local.go:15-50`, `nntp-article-common.go:131,150` | CONFIRMED |

**Scenario:** `ARTICLE <mid>` for an article that is not there yet caches a 430. The article then
arrives via IHAVE/TAKETHIS/POST; for up to about 75 s a retry of `ARTICLE <mid>` still gets 430.

**Evidence:** `getArticleData` adds the id to `local430` on a definite miss
(`nntp-article-common.go:150`) and checks it first (`:131`). `Local430` has only `CronLocal430`
(`nntp-cache-local.go:15`), `Check` (`:23`), `Add` (`:32`) and `Cleanup` (`:41`), with no remove; the
intake path (`ProcessIncomingArticle`) never touches it (grep: the only `local430` callers are
`nntp-article-common.go:131,150` and `nntp-server.go:95`). `Cleanup` drops entries older than
1 minute (`:46`) and runs every 15 s (`:17`), so the stale 430 lasts up to about 75 s.

**Fix (S):** add `Local430.Remove(messageID)` and call it when an article is accepted (the server
knows the message-id in POST/IHAVE/TAKETHIS).

---

### A-nntp-sec-2: NNTP serves articles the web hides (articles.hide ignored)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-2 | medium | security | `queries.go:698,892,919,1810,1872`, `thread_cache.go:309` | CONFIRMED |

**Scenario:** an admin hides a spam or abuse article (`UPDATE articles SET hide = 1`, e.g.
`IncrementArticleHide`, `database.go:416`, or the future-dated-spam sweep,
`web_admin_newsgroups.go:806`). The web lists stop showing it (`web_threadPage.go:61,117`,
`web_apiHandlers.go:188` with `WHERE hide = 0`), but NNTP keeps serving it.

**Evidence:** none of the server read queries filter on `hide`: `GetOverviews` (LISTGROUP,
`queries.go:698`), `GetOverviewsRange` (XOVER, `:1810`), `GetHeaderFieldRange` (XHDR, `:1872`),
`GetArticleByNum` (ARTICLE/HEAD/BODY by number, `:892`), `GetArticleByMessageID` (`:919`) and
`GetOverviewByArticleNum` (STAT, `thread_cache.go:309`, which selects `hide` but does not filter on
it). A hidden article is still returned by LISTGROUP, XOVER, XHDR, ARTICLE, HEAD, BODY and STAT.

**Fix (M):** add `AND hide = 0` (and decide about `spam`) to the server read queries, or add
hide-aware variants for the NNTP path. Same queries as BUG-1/BUG-2, so one fix plan should take them
together.

---

### A-nntp-sec-3: range reads skip rows.Err(); two of them bypass the retry helper

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-3 | low | bug | `queries.go:1820,1836` (`GetOverviewsRange`), `:1913,1937` (`GetHeaderFieldRange`), `:701,714` (`GetOverviews`) | CONFIRMED |

**Scenario:** an iteration error after a partial read is reported as success; the client gets a
truncated `224`/`221`/`211` list with no error.

**Evidence:** all three functions return scan errors from inside the loop, but after
`for rows.Next()` they `return out, nil` / `return result, nil` without calling `rows.Err()`
(`queries.go:714`, `:1836`, `:1937`). An error that ends iteration early (busy/locked database, I/O
error) is therefore dropped. In addition, `GetOverviewsRange` (`:1820`) and `GetHeaderFieldRange`
(`:1913`) call `DB.Query` directly instead of `RetryableQuery`; `GetOverviews` already uses
`RetryableQuery` (`:701`).

**Fix (S):** check `rows.Err()` after each loop; route the two range reads through `RetryableQuery`
like the rest of the package.

---

### A-nntp-sec-4: per-name memory and open DB handles from unvalidated LISTGROUP names

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-4 | low | security | `db_groupdbs.go:153-167,218`, `db_batch.go:166-183,187-210`, `groups_hashmap.go:9,112-139`, `database.go:15,23-25,62-63` | CONFIRMED |

**Scenario:** besides the file on disk (SEC-2), each distinct name sent to LISTGROUP leaves
in-memory entries and an open `*sql.DB`. Only LISTGROUP reaches this; GROUP validates first and never
calls `GetGroupDB` for an unknown name.

**Evidence:** `GetGroupDB` creates a `groupDB` map entry and calls `GetNewsgroupPointer`
(`db_groupdbs.go:153-167`); `initGroupDB` calls `GroupToHash` (`:218`) and opens the database.
- `GetNewsgroupPointer` adds a `TasksMap` entry (`db_batch.go:187-210`) that expires after 120 s
  (`:204`; swept by `ExpireCache`, `:166-183`).
- `GroupToHash` (`groups_hashmap.go:112-139`) adds `GHmap`/`Groups` entries that expire after
  `DefaultGroupHashExpiry = 12 * time.Hour` (`:9`).
- The `groupDB` entry with its open `*sql.DB` (file descriptors for the DB and its WAL/SHM files)
  stays until `cleanupIdleGroups` closes it: after `DBidleTimeOut = 1h` (`database.go:15`), or
  earlier when `MaxOpenDatabases` (256) or more are open, in which case the oldest worker-less DBs are
  closed down to 128. `CronDB` checks every 10 s (`database.go:23-25`).

So the cost is bounded (expiry and the 256-handle cap) but attacker-driven, and the GHmap entries
last 12 h. The SEC-2 guard removes it.

**Fix (S):** the SEC-2 `MainDBGetNewsgroup` check in `handleListGroup` rejects an unknown name before
any map entry, pointer or handle exists.

---

### A-nntp-sec-5: server tls.Config sets only Certificates

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-5 | low | security | `nntp-server.go:125-127` | CONFIRMED |

**Scenario:** the TLS listener relies entirely on Go's crypto/tls defaults for protocol version and
cipher selection.

**Evidence:** `tlsConfig := &tls.Config{ Certificates: []tls.Certificate{cert} }`
(`nntp-server.go:125-127`) sets no `MinVersion`, `CipherSuites` or `NextProtos`. The outbound client
in the same package pins `MinVersion: tls.VersionTLS12` (`nntp-client.go:183`), so server and client
differ. With the current Go toolchain the default server minimum is TLS 1.2, so the practical risk is
low today, but the floor is implicit.

**Fix (S):** set `MinVersion: tls.VersionTLS12` on the server config, matching the client.

---

### A-nntp-sec-6: NNTP username enumeration by response timing

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-6 | medium | security | `db_nntp_users.go:302-310`, `nntp-cmd-auth.go:34` | CONFIRMED |

**Scenario:** an unauthenticated client sends `AUTHINFO USER <name>` / `AUTHINFO PASS x` for
candidate names. Unknown names are answered with 481 almost at once; known names take a full bcrypt
comparison first. The difference reveals which usernames exist.

**Evidence:** on a cache miss, `AuthenticateNNTPUser` returns right after `GetNNTPUserByUsername`
fails (`db_nntp_users.go:302-305`) and runs `bcrypt.CompareHashAndPassword` only for an existing
user (`:308`). The 481 reply (`nntp-cmd-auth.go:34`) has no delay (SEC-5), so nothing hides the gap.

Measured (temporary `TestZZSec6Timing`, 20 calls each, `bcrypt.DefaultCost`):
`unknown_user_avg=144µs`, `known_user_wrong_pass_avg=109.8ms`, a factor of about 760.
(The `test-nntp-auth` slice measured the same gap over the wire: about 0.3 ms against a full bcrypt.)

**Fix (S):** when the user is unknown (or inactive), compare the password against a fixed dummy
bcrypt hash of the same cost so both paths take the same time; add the 481 delay from SEC-5 in the
same change.

---

## Unauthenticated attack surface

Every command below is reachable **before any AUTHINFO** (SEC-5: reads are never gated). "Limit
today" is what bounds the resource at 66c36f8.

| Command | Resources touched | Limit today | Proposed limit |
|----|----|----|----|
| TCP connect (connection slot) | goroutine, fd, one `MaxConns` slot | one global `MaxConns` (`nntp-server.go:188`, counted after accept, so a burst can overshoot); no per-IP limit; idle slot held up to `IdleTimeout` (60 s) | per-IP connection limit; count the slot at accept |
| `LISTGROUP <name>` | **disk/inodes** (new dir, `.db`, schema), **memory and fds** (groupDB entry, open `*sql.DB`, TasksMap, GHmap), **memory** (all overview rows loaded) | none (SEC-2, A-nntp-sec-4, SEC-7) | reject unknown/inactive group via `MainDBGetNewsgroup`; stream article numbers; honour the range |
| `XHDR <hdr> <range>` | **CPU** (send loop walks the whole range) | none; DB read clamped to 1000 rows but the loop is not (SEC-1) | iterate the returned rows; clamp the range to the high-water mark |
| `XOVER <range>` | **memory** (up to 1001 overview rows), silent truncation | Go clamp to 1001 rows (BUG-2) | SQL `LIMIT`, documented paging |
| `GROUP`, then `ARTICLE`/`HEAD`/`BODY`/`STAT <num>` | group DB reads; serves hidden articles (A-nntp-sec-2); current article left at 0 (BUG-1) | GROUP validates the name; `ArticleCmdDelay` 200 ms per article command (default) | filter `hide`; set `DBArtNum` |
| `ARTICLE`/`HEAD`/`BODY`/`STAT <message-id>` | **DB/fds**: global lookup through `Processor.FindArticleByMessageID` (history index, then opening other groups' DBs) (`nntp-article-common.go:146`); **memory**: each definite miss adds the id to `local430` for up to 75 s | `ArticleCmdDelay` 200 ms plus `ErrorDelay` 1 s on a miss | keep the delays; cap `local430` size and message-id length |
| `LIST` / `LIST NEWSGROUPS` | **memory/DB**: loads every active newsgroup row on each call (`nntp-cmd-list.go:28,48`) | none, no delay | cache the active list; rate-limit |
| `POST`/`IHAVE`/`TAKETHIS` article | **memory** (article lines) | 16384 lines × unbounded line length; `MaxArtSize` ignored (SEC-3). TAKETHIS buffers the article before the auth check | enforce `MaxArtSize` in bytes before buffering |
| any unknown word | **memory** (a stats map key per distinct word, each up to a full line) | none, and no error delay (SEC-4) | count only known commands; delay the `default:` branch |
| any command line | **memory** (whole line buffered) | none, `ReadLine` is unbounded (SEC-3) | byte-limited reader |
| `QUIT` + pipelined command | **process** (crash) | none: nil deref, no `recover` (A-nntp-sec-1) | nil guard and `recover` |
| `AUTHINFO USER/PASS` | **CPU** (one bcrypt per attempt on a known user); credentials in clear text; username enumeration by timing | plaintext accepted, no back-off on 481 (SEC-5, A-nntp-sec-6) | require TLS; delay 481; dummy bcrypt for unknown users |

## Measured repros

The harness server runs in the test process, with delays 0, `ENABLE_ARTICLE_CACHE=false` and
`127.0.0.1:0`. Memory figures are Go heap/allocation deltas or map sizes in that process, which is
the resource the attacker grows; they are reported in place of RSS.

| Finding | Command / input | Number |
|----|----|----|
| SEC-1 (CPU) | `GROUP g; XHDR subject 1-9223372036854775807`, sampled 3 s via `getrusage` | **103.1 %** of one core (spin) |
| SEC-2 (files) | 4× `LISTGROUP <non-existent>` | **4** `.db` files created (before=0 after=4); `MainDBGetNewsgroup` still `ErrNoRows` for all |
| SEC-3 (memory) | one command line of 1 KiB / 1 MiB / 8 MiB | 8 MiB line accepted; **+111.8 MiB TotalAlloc** for the 8 MiB line (cumulative allocation, includes the echoed reply and the client read; not peak RSS) |
| SEC-4 (memory) | 5000 distinct unknown commands, one connection | stats map = **5000** entries, in 0.14 s |
| SEC-7 (memory) | `GetOverviews` on a 50 000-row group (LISTGROUP source) | 50 000 rows loaded, **+32.13 MiB** heap for one call |
| SEC-6 (auth) | authenticate → `UpdateNNTPUserPassword` → authenticate with old password | old password `err=<nil>`, new password `err=<nil>` |
| A-nntp-sec-6 (timing) | `AuthenticateNNTPUser`, unknown user vs known user with wrong password, 20 each | **144 µs** vs **109.8 ms** average |
| A-nntp-sec-1 (crash) | `QUIT\r\nMODE READER\r\n` in one write | child exits with status 2, SIGSEGV at `cliconns.go:60` via `:81` |

Commands (temporary `zz_audit_nntp-sec_test.go`, now deleted):
`go test -race -run TestZZ -v -timeout 600s ./internal/nntp/`. SEC-1 ran as the isolated child
directly: `PUGLEAF_ISOLATED=TestZZSec1XHDRSpin PUGLEAF_TEST_DATADIR=<tmp> go test -run
TestZZSec1XHDRSpin -v ./internal/nntp/`.

## Races

A temporary stress test (`TestZZRaceConcurrent`) ran **100 concurrent clients**, each doing
`GROUP → XOVER 1-50 → XOVER 1-50 (drain) → STAT 1 → ARTICLE 2 (drain) → QUIT` against the harness
server (with an `hFakeProcessor`, `MaxConns 200`), under `go test -race`:

```
--- PASS: TestZZRaceConcurrent (0.34s)    RACE done: 100 clients
```

No data race was reported. The shared state on this path is guarded: `ServerStats` by `mux`
(`nntp-server-statistics.go`), `Local430` by `mu` (`nntp-cache-local.go`), and GroupDB worker
accounting by `MainMutex`/`GroupDB.mux` (`db_groupdbs.go`, documented lock order). The article cache
was disabled (`ENABLE_ARTICLE_CACHE=false`, harness default), so its locking was not exercised. The
concurrency defects in this report are liveness and resource issues (SEC-1 spin, A-nntp-sec-1 crash,
BUG-7 leaks), not data races.

## TLS and shutdown

- **tls.Config defaults.** The server sets only `Certificates` (`nntp-server.go:125-127`): no
  `MinVersion`, `CipherSuites` or `NextProtos`. The protocol floor and ciphers come from Go's defaults
  (TLS 1.2 minimum on the current toolchain). The client pins TLS 1.2 explicitly
  (`nntp-client.go:183`); the server does not (A-nntp-sec-5). The TLS handshake runs lazily on the
  first write (the greeting), under the write deadline set by `UpdateDeadlines`, so a silent TLS
  client holds a slot for at most `IdleTimeout`. No STARTTLS is offered, so AUTHINFO on the plain port
  is clear text (SEC-5).
- **Stop() with live clients.** `Stop()` closes `shutdown` and the listeners, but it does **not**
  close existing client connections and does **not** wait on `wg` (`nntp-server.go:238-251`, comment
  `:240-244`). Idle clients end at their read deadline (`IdleTimeout`, 60 s); a client that keeps
  sending commands, or a handler stuck in the SEC-1 spin, never ends, and the process-level
  `wg.Wait()` (`cmd/nntp-server/main.go:148`) blocks indefinitely.
- **Leak on cert failure.** `Start()` starts the plain listener before it loads the TLS cert. A
  `LoadX509KeyPair` failure returns an error while `running` is still false, which leaves the plain
  accept goroutine running and out of reach of `Stop()` (it returns early on `!running`). The shipped
  `cmd/nntp-server` masks this with `log.Fatalf` (`main.go:133-134`); embedded callers leak (BUG-7).
- **Leaked cleanup goroutine.** `NewNNTPServer` starts `CronLocal430` with no stop path
  (`nntp-server.go:95`); one goroutine leaks per server instance (BUG-7).

## Function inventory

| function / file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `nntp-cmd-{article,head,body,stat}.go` (FN-1) | yes | — | — | — | — | `// stub` only; the handlers live in `nntp-article-common.go` |
| `nntp-cmd-reader.go` (FN-1) | yes | — | — | — | — | `package nntp` only |
| `(*ClientConnection).parseArticleHeadersShort` (`nntp-cmd-helpers.go:25`) (FN-1) | — | yes | yes | — | — | staticcheck U1000; only `parseArticleHeadersFull` (`:20`) is used |
| `AuthManager.CheckGroupAccess` (`nntp-auth-manager.go:47`) (FN-1) | — | yes | yes | yes | — | never called; returns true for any non-nil user (no group ACL) |
| `AuthManager.CanPost` (`:58`) / `IsAdmin` (`:66`) (FN-1) | — | yes | yes | — | — | never called; posting is checked inline via `c.user.Posting` |
| `AuthManager.CheckConnectionLimit` (`:77`) (FN-1) | — | yes | yes | — | — | never called; per-user `maxconns` not enforced (SEC-5) |
| `ServerStats.{GetTotalConnections,GetCommandCount,GetAllCommandCounts,GetAuthStats,GetUptime,Reset}` (`nntp-server-statistics.go:50,64,71,97,104,111`) (FN-1) | — | yes | yes | — | — | no caller in the server; `Reset` would be the only bound on SEC-4 |
| `ClientConnection.capabilities` (`nntp-server-cliconns.go:31`) (FN-1) | — | yes | yes | — | — | set to `[]string{}` and never read; CAPABILITIES uses `getServerCapabilities` |
| `CacheMessageIDNumtoGroup` (`nntp-cache-local.go:54-146`) | — | yes | — | — | — | the whole type is inside a comment block |
| `NNTPAuthCache.generatePasswordHash` (`nntp_auth_cache.go:44`) | — | — | — | — | — | unsalted sha256 used only as a cache key; its comment says "NOT for storage". Listed because the cache key is the root of SEC-6 |

## Seed items rejected

None. SEC-1…7, BUG-1, BUG-2, BUG-7 and BUG-8 are all CONFIRMED above. The SEC-6 revoked-user half
was verified as already fixed (LO:F22) and, as the slice requires, is not re-reported.

## Not covered / follow-ups

- **SEC-2 intake half → `audit-intake`**: unknown `Newsgroups:` on POST/IHAVE/TAKETHIS creating
  group DBs and `active=1` rows (`processor/threading.go`, `db_batch.go`).
- RFC conformance of these commands (501 vs 500, missing OVER/HDR/DATE/NEXT/LAST, CAPABILITIES
  content) is `audit-nntp-rfc` (RFC-1…6, BUG-3…6, BUG-9, FN-1).
- The DB side of SEC-2 (auto-creation in `GetGroupDB`), SEC-6 (`nntp_auth_cache` keying), BUG-1/BUG-2
  (query clamps), NULL scans and re-open globals belong to `audit-db`.
- Intake correctness of POST/IHAVE/TAKETHIS (message-id, date, newsgroups parsing) is `audit-intake`
  (INT-1…6); this slice covers only their resource and size behaviour (SEC-3).
- The unchecked `UpdateNNTPUserLastLogin` return (`db_nntp_users.go:325`, errcheck; it also runs a
  second time from `AuthManager.AuthenticateUser`) is noted for `audit-db`.
- The article-cache path was not exercised by the race test (harness disables it).
- Suggested `knownBug` ids for wave-2 tests: SEC-1, SEC-2, SEC-3, SEC-4, SEC-6, SEC-7, BUG-1, BUG-2,
  BUG-8, A-nntp-sec-1, A-nntp-sec-2, A-nntp-sec-6.
