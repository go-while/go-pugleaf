# Audit audit-nntp-sec (base 66c36f8)

Security, resource-exhaustion and concurrency audit of the NNTP **server** (reading,
posting/transfer, auth, caches, TLS and shutdown). Client/transfer code is `audit-nntp-client`;
RFC command conformance is `audit-nntp-rfc`; the DB-side of SEC-2/SEC-6 and the batch writer are
`audit-db`. This report follows `docs/audit/README.md` (D1) and K6.

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
  `GetNewsgroupPointer` (`db_batch.go`), `SanitizeGroupName` (`utils.go`)
- `internal/database/db_nntp_users.go` and `internal/database/nntp_auth_cache.go`, in full
- `cmd/nntp-server/main.go` (startup and shutdown)

Tools (from `docs/audit/tools/`, filtered to this scope): staticcheck flags
`(*ClientConnection).parseArticleHeadersShort` unused; golangci-lint/errcheck flags the unchecked
`db.UpdateNNTPUserLastLogin` return in `AuthenticateNNTPUser` (`db_nntp_users.go:325`); gosec flags
`queries.go:806` G101 (a false positive on a SQL const). govulncheck's NNTP hits are all in
`nntp-client-commands.go` (client, out of scope).

Repros: a temporary `internal/nntp/zz_audit_nntp-sec_test.go` using the wave-0 harness
(`newTestServer`, `hGroup`, `hUser`, `runIsolated`, `hFakeProcessor`), run with
`go test -race -run TestZZ -v ./internal/nntp/`. SEC-1 and the QUIT crash went through `runIsolated`.
The file was **deleted before commit** (D1 / K5). No RFC fetch was needed for this slice.

## Summary

Counts by severity: **critical 2, high 4, medium 7, low 3** (16 findings). By category:
security 9, bug 5, robustness 2.

Five most important:

1. **A-nntp-sec-1 (critical)** — a pipelined `QUIT` + any following command crashes the whole
   server process with a nil-pointer dereference (`handleConnection` has no `recover`). Unauthenticated.
2. **SEC-1 (critical)** — `XHDR <hdr> 1-9223372036854775807` spins a core forever (103% CPU measured);
   `Stop()` cannot end it and `main`'s `wg.Wait()` hangs. Unauthenticated.
3. **SEC-2 (high)** — `LISTGROUP <anyname>` creates a directory + SQLite file for a never-validated
   group name (4 files created from 4 requests, measured). Unauthenticated disk/inode exhaustion.
4. **SEC-3 (high)** — no byte limit on command or article lines; `Server.NNTP.MaxArtSize` and
   `newsgroups.max_art_size` are never read (an 8 MiB command line was accepted, measured).
5. **SEC-6 (high)** — after `UpdateNNTPUserPassword` the old password keeps authenticating for up to
   the 15-minute auth-cache TTL (confirmed with a repro). The revoked-user half is already fixed
   (LO:F22) and is only verified here, not re-reported.

No data race was found under `-race` with 100 concurrent clients (see `## Races`).

## Findings

### A-nntp-sec-1 — pipelined QUIT crashes the whole process (nil conn deref)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-1 | critical | security | `nntp-cmd-basic.go:59-64`, `nntp-server-cliconns.go:60,81`, `nntp-server.go:196,202-214` | CONFIRMED |

**Scenario:** an unauthenticated client sends `QUIT\r\nMODE READER\r\n` in a single packet → the
server process dies with SIGSEGV. One short write per connection takes the server down for everyone.

**Evidence:** `handleQuit` (`nntp-cmd-basic.go:53-57`) calls `c.Close()`, which sets `c.conn = nil`
(`:63`) but does **not** close `c.textConn`. The `Handle` loop (`nntp-server-cliconns.go:75-93`)
returns to `c.textConn.ReadLine()`; because `QUIT` and the next command arrived together,
`textproto`'s `bufio.Reader` has already buffered `MODE READER`, so `ReadLine` returns it without
touching the (now closed) socket. Line 81 then calls `c.UpdateDeadlines()` →
`c.conn.SetReadDeadline` on the nil `c.conn` (`:60`). `handleConnection` (`nntp-server.go:202`) has
no `recover()`, so the panic in that goroutine crashes the process. Measured stack:

```
panic: runtime error: invalid memory address or nil pointer dereference [signal SIGSEGV ...]
nntp.(*ClientConnection).UpdateDeadlines(...)   internal/nntp/nntp-server-cliconns.go:60
nntp.(*ClientConnection).Handle(...)            internal/nntp/nntp-server-cliconns.go:81
nntp.(*NNTPServer).handleConnection(...)        internal/nntp/nntp-server.go:211
                                                internal/nntp/nntp-server.go:196 (go s.handleConnection)
```

Repro: `TestZZQuitPipelineCrash` (pipelined write inside `runIsolated`) → child exits with status 2
and the SIGSEGV above.

**Fix (M):** in `Handle`, break the loop when `c.conn == nil` (or when `handleCommand` signals a
close) before `UpdateDeadlines`; have `UpdateDeadlines` no-op on a nil conn; add a `recover()` in
`handleConnection` so a single connection can never crash the server. S for the nil guard alone.

---

### SEC-1 — XHDR unbounded send loop pins a core and hangs shutdown

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-1 | SEC-1 | critical | security | `nntp-cmd-xhdr.go:52,85,97-104`, `queries.go:1888-1890` | CONFIRMED |

**Scenario:** `GROUP x` then `XHDR subject 1-9223372036854775807` (or `1-` on a group whose
`last_article` is huge). The DB read is clamped to 1000 rows, but the send loop walks the full
client range doing a map lookup each iteration with no I/O, so it never returns. Unauthenticated.

**Evidence:** `GetHeaderFieldRange` clamps `endNum = startNum + 1000` (`queries.go:1888-1890`) and
returns a `map[int64]string` of at most ~1001 entries. `handleXHdr` then loops
`for articleNum := startNum; articleNum <= endNum; articleNum++` (`:97`) over the **unclamped**
`startNum..endNum` (here up to `MaxInt64`), calling `c.sendLine` only for the handful of nums present
in the map. The remaining ~9.2e18 iterations do a map miss and continue — no read, no write, nothing
that a client disconnect or `Stop()` can interrupt. The goroutine is `handleConnection`, counted in
`s.wg`, which `cmd/nntp-server/main.go:148` waits on at shutdown → shutdown hangs forever.

Measured (`TestZZSec1XHDRSpin`, isolated child, process CPU via `getrusage`):
`SEC1_CPU_PERCENT=103.1 over 3s` (one core == 100%), after `221 Header subject follows`.

**Fix (M):** iterate the returned rows (`for num := range sorted(headerData)`) instead of the raw
range, exactly like `GetOverviewsRange`/XOVER does; and clamp the request range to the group's
high-water. Also push the 1000-row limit into SQL (`LIMIT`) rather than a Go clamp (see BUG-2).

---

### SEC-2 — LISTGROUP creates a group DB for any unvalidated name

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-2 | SEC-2 | high | security | `nntp-cmd-group.go:156-174`, `db_groupdbs.go:117-190,194-227` | CONFIRMED (server side) |

**Scenario:** unauthenticated `LISTGROUP comp.lang.fictional.whatever` for a group that does not
exist creates `data/db/<md5-prefix>/<sanitized>.db` plus its directory and full schema. Repeating
with fresh names exhausts inodes / disk.

**Evidence:** `handleGroup` (`nntp-cmd-group.go:138`) validates with `MainDBGetNewsgroup` first and
returns 411 for an unknown group. `handleListGroup` (`:156`) does **not**: it calls
`GetGroupDB(groupName)` directly (`:169`), which, finding no map entry, creates one and runs
`initGroupDB` (`db_groupdbs.go:164,194`). `initGroupDB` calls `createDirIfNotExists` (`:224`),
`sql.Open` (`:232`) and `migrateGroupDB` (`:246`) — i.e. it materialises the file and schema for a
name that was never checked against `newsgroups`. The same unchecked path is reached by XOVER/XHDR/
ARTICLE/STAT when a group is "selected" via LISTGROUP.

Measured (`TestZZSec2ListgroupFiles`): `SEC2_DBFILES before=0 after=4 created=4` for four
non-existent names; `MainDBGetNewsgroup(<each>)` returns `sql: no rows in result set` afterwards
(the group was never a real newsgroup, yet its DB now exists on disk).

**Fix (S):** NNTP handlers must call `MainDBGetNewsgroup` (returns `sql.ErrNoRows`) before
`GetGroupDB`, as D4 Appendix B specifies. Auto-creation in `GetGroupDB` is shared with the batch
writer and stays; the guard belongs in the callers.

---

### SEC-3 — no byte limits on command or article lines; MaxArtSize never read

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-3 | SEC-3 | high | security | `nntp-server-cliconns.go:77`, `nntp-cmd-posting.go:221-260`, `config/config.go:131` | CONFIRMED |

**Scenario:** a client sends an arbitrarily long single line (command or article line). It is read
and buffered whole; there is no per-line or per-article byte cap, and the configured maxima are
ignored. Many connections each buffering large lines exhaust memory.

**Evidence:** command lines use `c.textConn.ReadLine()` (`nntp-server-cliconns.go:77`) with no size
limit. Article reception (`readArticleLines`, `nntp-cmd-posting.go:221-260`) caps only line *counts*
(`maxLines, maxHead := 16384, 1024`, `:224`) — each of the up to 16384 lines may itself be
arbitrarily long, and the whole head/body is held in slices. `Server.NNTP.MaxArtSize`
(`config/config.go:131`, default 32768) and `newsgroups.max_art_size` are never read anywhere in
`internal/nntp` (grep: `max_art_size` appears only in provider/newsgroup CRUD in `queries.go`).

Measured (`TestZZSec3CommandLineSize`): command lines of 1 KiB, 1 MiB and **8 MiB** were all read
and answered (`500 Unknown MODE...`) — the full 8 MiB was buffered server-side before the reply.

**Fix (M):** wrap command reads in a byte-limited reader and enforce `MaxArtSize` (server, then
per-group) in `readArticleLines`, replying 501/400 when exceeded.

---

### SEC-4 — unbounded command-stats map growth from unknown commands

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-4 | SEC-4 | medium | security | `nntp-server-cliconns.go:112-116`, `nntp-server-statistics.go:279-283` | CONFIRMED |

**Scenario:** an unauthenticated client sends many distinct unknown command words; each is stored as
a new key in `Stats.commandCounts` before dispatch. The map grows without bound and never shrinks.

**Evidence:** `handleCommand` computes `command := strings.ToUpper(parts[0])` from the client line
and calls `c.server.Stats.CommandExecuted(command)` (`:112-116`) *before* the switch. Unknown
commands fall through to `default` (`:154`, `500`) but are already recorded.
`CommandExecuted` (`statistics.go:279`) does `s.commandCounts[command]++` with no key whitelist and
no cap; `Reset` is never called from the server.

Measured (`TestZZSec4StatsMap`): one connection sending 5000 distinct unknown words →
`SEC4_STATSMAP_ENTRIES=5000`. With no `ErrorDelay` the loop is cheap; the default 1 s error delay
slows it but does not bound the map.

**Fix (S):** record stats only for recognised commands (move the call into each handler, or only on
a successful dispatch), or key on a fixed command set.

---

### SEC-5 — reading never requires auth; AUTHINFO weaknesses

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-5 | SEC-5 | medium | security | `nntp-cmd-auth.go:34,38,89-116`, `nntp-server-cliconns.go` (dispatch), `nntp-auth-manager.go:47-90` | CONFIRMED |

**Scenario:** several RFC-4643 / policy gaps on the auth path.

**Evidence (code trace):**
- **No read auth.** GROUP, LISTGROUP, ARTICLE/HEAD/BODY/STAT, XOVER, XHDR, LIST check
  `c.authenticated` nowhere; only POST/IHAVE/TAKETHIS do. All read + file-creating surface (SEC-2,
  SEC-7, A-nntp-sec-4) is anonymous. (This may be intentional for a public reader; documented in the
  attack-surface table.)
- **AUTHINFO in plaintext.** `handleAuthInfo` accepts `USER`/`PASS` on a plain connection with no
  STARTTLS and never returns 483 (`nntp-cmd-auth.go:89-113`); credentials cross the wire in clear.
- **No back-off on failure.** `481` (`:105`) has no `rateLimitOnError()`, so password guessing is
  not throttled at the protocol layer (the 15-min auth cache only helps correct logins).
- **Re-AUTH after success.** A second `AUTHINFO USER/PASS` after a successful one is accepted and
  re-runs auth; RFC 4643 wants `502`.
- **Dead auth helpers.** `AuthManager.CheckGroupAccess`, `CanPost`, `IsAdmin`,
  `CheckConnectionLimit` (`nntp-auth-manager.go:47-90`) and `MaxConns`/session tracking are unused —
  per-user connection limits are not enforced (only the global `MaxConns` in `serve`). See the
  function inventory.

**Fix (M):** require TLS (or STARTTLS) before AUTHINFO; add `rateLimitOnError` to the 481 path;
reject a second AUTHINFO with 502; wire `CheckConnectionLimit` into `handleAuthInfo` or remove it.

---

### SEC-6 — old password keeps working after a change (auth cache)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-6 | SEC-6 | high | security | `db_nntp_users.go:136-147,281-328`, `nntp_auth_cache.go:50-90`, `db_init.go:22` | CONFIRMED (old-password half) |

**Scenario:** a user (or admin) changes an NNTP password via `UpdateNNTPUserPassword`. Because the
auth cache keys a hit on `sha256(password-at-login)` and is not invalidated, a connection presenting
the **old** password still authenticates until the 15-minute TTL expires.

**Evidence:** `AuthenticateNNTPUser` first consults `NNTPAuthCache.Get(username, password)`
(`db_nntp_users.go:284`); `Get` returns a hit when the stored `sha256(oldPass)` matches
(`nntp_auth_cache.go:82-89`). `UpdateNNTPUserPassword` (`:136-147`) only runs an `UPDATE` and never
calls `InvalidateNNTPUserAuth`; neither do `DeactivateNNTPUser`/`DeleteNNTPUser`. TTL =
`NNTP_AUTH_CACHE_TIME = 15 * time.Minute` (`db_init.go:22`). The web and NNTP processes are separate
(OPS-2), so the fix belongs in the DB layer.

Measured (`TestZZSec6OldPassword`): authenticate → `UpdateNNTPUserPassword` → authenticate again:
`oldPass err=<nil>  newPass err=<nil>` — both the old and the new password are accepted.

**Verified, not re-reported (LO:F22, revoked-user half):** on every cache hit
`AuthenticateNNTPUser` calls `refuseDisabledNNTPUser` (`:294`, `:338-350`), which re-reads
`nntp_users.is_active` and `users.disabled` and invalidates the cache on refusal (fails closed on a
read error). So a deactivated/disabled user is rejected immediately despite a cached entry. This
half is correct at 66c36f8.

**Fix (S):** call `InvalidateNNTPUserAuth(username)` inside `UpdateNNTPUserPassword` (and the
deactivate/delete functions), in the DB layer, so it applies regardless of which process makes the
change.

---

### SEC-7 — LISTGROUP / GetOverviews load every row into memory and log each call

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| SEC-7 | SEC-7 | high | security | `nntp-cmd-group.go:177`, `queries.go:698-715` | CONFIRMED |

**Scenario:** `LISTGROUP <biggroup>` (unauthenticated) reads *all* overview rows of the group into a
slice before sending anything; on a large group this allocates proportionally to the group size, and
every call is logged.

**Evidence:** `handleListGroup` calls `GetOverviews(groupDB)` (`nntp-cmd-group.go:177`).
`GetOverviews` (`queries.go:698`) runs `SELECT ... FROM articles` with **no LIMIT** and appends
every row to `out` (`:706-714`), and logs `GetOverviews: group '%s' fetching overviews ...` on each
call (`:699`). There is no streaming and no range clamp (LISTGROUP ignores its optional range —
BUG-4, rfc slice).

Measured (`TestZZSec7Memory`): a group with 50 000 rows → `GetOverviews` returned 50 000 overviews
with `heap_delta_MB=32.13` for the single call. N concurrent LISTGROUPs multiply this.

**Fix (M):** stream rows to the client (iterate `rows.Next()` and write as you go), honour the
LISTGROUP range, and drop the per-call log to debug.

---

### BUG-1 — GetArticleByNum never sets DBArtNum (current article stuck at 0)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-1 | BUG-1 | medium | bug | `queries.go:900-914`, `nntp-article-common.go:60` | CONFIRMED |

**Scenario:** `ARTICLE 5` / `HEAD`/`BODY` by number succeeds but leaves the current article pointer
at 0, so a subsequent `XOVER` or `XHDR` with no range returns `420 Current article number is invalid`.

**Evidence:** `GetArticleByNum` (`queries.go:900`) scans into `artnum` and sets
`a.ArticleNums[groupDB.NewsgroupPtr] = artnum` but never sets `a.DBArtNum` (contrast
`GetArticleByMessageID` at `:930`, which does `a.DBArtNum = artnum`). In `retrieveArticleCommon`,
`c.currentArticle = article.DBArtNum` (`nntp-article-common.go:60`) therefore stores 0. STAT is
unaffected because `getArticleData` builds the overview result with `DBArtNum: wantArticleNum`
explicitly (`:179-182`). Whether the bug shows depends on the article cache (a cached entry may
carry a correct `DBArtNum`); with `ENABLE_ARTICLE_CACHE=false` it always reproduces.

**Fix (S):** set `a.DBArtNum = artnum` in `GetArticleByNum`.

---

### BUG-2 — XOVER / XHDR silently truncated to ~1001 rows (no SQL LIMIT)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-2 | BUG-2 | medium | bug | `queries.go:1810-1825` (`GetOverviewsRange`), `1872-1890` (`GetHeaderFieldRange`) | CONFIRMED |

**Scenario:** `XOVER 1-5000` returns only the first ~1001 rows; the client sees a silent gap, not an
error.

**Evidence:** both functions clamp in Go (`if endNum-startNum > 1000 { endNum = startNum + 1000 }`,
`queries.go:1820-1822` and `1885-1890`) and have no SQL `LIMIT`. XOVER iterates the returned rows,
so it truncates quietly (no spin); XHDR shares the clamp but spins on the send side (SEC-1).

**Fix (S):** either raise/parameterise the clamp and tell the client, or paginate; push the limit
into SQL so the clamp and the query agree.

---

### BUG-7 — shutdown, error-ordering and listener-leak defects

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-7 | BUG-7 | high | bug/robustness | `nntp-server.go:95,117-137,217-255`, `nntp-cmd-posting.go:227-229`, `nntp-server-cliconns.go:86` | CONFIRMED |

Several defects confirmed by code trace:

- **Stop() does not close clients or wait.** `Stop()` (`nntp-server.go:217-255`) closes `shutdown`
  and the listeners but deliberately does not touch live client connections and explicitly does not
  `wg.Wait()` (comment `:240-244`). So a connection in a long/stuck handler (e.g. SEC-1 spin, or an
  idle client) keeps its `handleConnection` goroutine alive. `main` then blocks on `wg.Wait()`
  (`cmd/nntp-server/main.go:148`) indefinitely. Combined with SEC-1, shutdown never completes.
- **CronLocal430 goroutine leak.** `NewNNTPServer` starts `go server.local430.CronLocal430()`
  (`nntp-server.go:95`); that loop (`nntp-cache-local.go:15-21`) has no stop channel and is never
  joined — one leaked goroutine per server instance.
- **TLS cert failure leaves the plain listener running.** In `Start`, the plain listener is started
  (`serveLocked`, `:115`) before the TLS block; if `tls.LoadX509KeyPair` fails (`:120-122`) `Start`
  returns an error while `running` is still false (set only at `:137`). A caller that does not exit
  then has a live accept goroutine that `Stop()` skips (`if !s.running { return nil }`, `:221-223`).
  In the shipped binary `main` does `log.Fatalf` on the error (`main.go:134`), which masks the leak;
  the bug is in the package API for embedded use.
- **"Too large" ordering / QUIT logged as error.** `readArticleLines` closes `c.textConn` on
  overflow (`nntp-cmd-posting.go:228`) and the caller still sends 441/436 afterwards; and the normal
  QUIT/close path returns an error from `Handle` that `handleConnection` logs as
  `Connection error from ...` (`nntp-server.go:212`, via `cliconns.go:86`).

**Fix (M):** give `CronLocal430` a stop channel closed by `Stop`; have `Stop` close tracked clients;
set `running=true` before starting any listener (or roll back the plain listener on TLS failure);
treat io.EOF / normal close as non-errors.

---

### BUG-8 — local430 negative cache not invalidated when the article arrives

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-8 | BUG-8 | low | bug | `nntp-cache-local.go:23-45`, `nntp-article-common.go:131,150` | CONFIRMED |

**Scenario:** `ARTICLE <mid>` for a not-yet-present article caches a 430; the article then arrives
via IHAVE/TAKETHIS/POST; for up to ~75 s a retry of `ARTICLE <mid>` still gets 430.

**Evidence:** `getArticleData` adds the id to `local430` on a definite miss
(`nntp-article-common.go:150`) and checks it on entry (`:131`). `Local430` exposes only `Check`,
`Add`, `Cleanup` and `CronLocal430` (`nntp-cache-local.go`) — no `Remove`/invalidate — and nothing
on the intake path (`ProcessIncomingArticle`) removes an id. `Cleanup` drops entries older than
1 minute and runs every 15 s (`:15,41-45`), so the stale 430 lasts up to ~75 s.

**Fix (S):** add `Local430.Remove(messageID)` and call it when an article is accepted; or key the
cache with a short TTL that intake can shorten.

---

### A-nntp-sec-2 — NNTP serves articles the web hides (articles.hide ignored)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-2 | medium | security | `queries.go:698-715,1810-1825,1872-1937,892-914`, `thread_cache.go:309` | CONFIRMED |

**Scenario:** an admin hides a spam/abuse article (`UPDATE articles SET hide = 1`, e.g.
`IncrementArticleHide`, `database.go:416`; the future-dated-spam sweep,
`web_admin_newsgroups.go:806`). The web UI stops showing it (`web_threadPage.go:61,117`,
`web_apiHandlers.go:188 WHERE hide = 0`), but NNTP keeps serving it.

**Evidence:** every server read query ignores `hide`: `GetOverviews` (XOVER full / LISTGROUP source,
`queries.go:706`), `GetOverviewsRange` (XOVER, `:1815`), `GetHeaderFieldRange` (XHDR, `:1872`),
`GetArticleByNum` (ARTICLE/HEAD/BODY, `:900`), `GetArticleByMessageID` (`:919`) and
`GetOverviewByArticleNum` (STAT, `thread_cache.go:309`, which even selects `hide` then does not
filter on it). So a hidden article is still returned over XOVER/XHDR/ARTICLE/HEAD/BODY/STAT.

**Fix (M):** add `AND hide = 0` (and decide on `spam`) to the server read queries, or add
hide-aware variants used by the NNTP path. Note this interacts with BUG-1/BUG-2 (same queries).

---

### A-nntp-sec-3 — range reads use DB.Query directly and skip rows.Err()

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-3 | low | bug | `queries.go:1820-1837` (`GetOverviewsRange`), `1915-1937` (`GetHeaderFieldRange`), `698-714` (`GetOverviews`) | CONFIRMED |

**Scenario:** a scan or iteration error part-way through a range read is swallowed; the client gets a
truncated `224`/`221` with no error.

**Evidence:** these three functions call `groupDB.DB.Query(...)` directly (not the
`RetryableQuery` helper used elsewhere, e.g. `MainDBGetNewsgroup`) and, after the `for rows.Next()`
loop, `return out, nil` without ever calling `rows.Err()` (`queries.go:1835-1837`, `1936`,
`713-714`). A row error mid-iteration therefore ends the loop early and is reported as success.
(golangci sqlclosecheck does not flag this because `defer rows.Close()` is present; it is the missing
`rows.Err()` that is the defect.)

**Fix (S):** check `rows.Err()` after each loop and route these reads through `RetryableQuery` for
consistency with the rest of the package.

---

### A-nntp-sec-4 — unbounded group maps from unvalidated LISTGROUP/GROUP names

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-4 | medium | security | `groups_hashmap.go:111-139`, `db_batch.go:187-210`, `db_groupdbs.go:157,218` | CONFIRMED |

**Scenario:** besides the on-disk file (SEC-2), every distinct group name a client names adds
permanent in-memory entries, so an unauthenticated client can inflate server memory by naming
endless random groups.

**Evidence:** on the LISTGROUP/GROUP path, `GetGroupDB` calls `GetNewsgroupPointer(groupName)`
(`db_groupdbs.go:157`) and `GroupToHash` via `initGroupDB` (`:218`). `GetNewsgroupPointer`
(`db_batch.go:187-210`) inserts a `BatchTasks` entry into `TasksMap` for any new name;
`GroupToHash` (`groups_hashmap.go:111-139`) inserts into both `GHmap` and `Groups`. `GHmap`/`Groups`
entries carry an expiry, but `TasksMap` entries and the `groupDB` map entry persist until idle
cleanup (`DBidleTimeOut = 1h`, `database.go:15`). Combined with the lack of a validity check (SEC-2),
this is unauthenticated memory growth keyed on attacker-chosen strings.

**Fix (S):** the SEC-2 `MainDBGetNewsgroup` guard also fixes this — an unknown name is rejected
before any map entry or pointer is created.

---

### A-nntp-sec-5 — server tls.Config sets only Certificates

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-sec-5 | low | security | `nntp-server.go:125-127` | CONFIRMED |

**Scenario:** the TLS listener relies entirely on Go's crypto/tls defaults for protocol version and
cipher selection.

**Evidence:** `tlsConfig := &tls.Config{ Certificates: []tls.Certificate{cert} }`
(`nntp-server.go:125-127`) sets no `MinVersion`, `CipherSuites` or `NextProtos`. The outbound client
in the same package *does* pin `MinVersion: tls.VersionTLS12` (`nntp-client.go:183`), so the server
is inconsistent with the client. With the current Go toolchain the default server minimum is
TLS 1.2, so this is low/informational today, but it is implicit and should be pinned.

**Fix (S):** set `MinVersion: tls.VersionTLS12` (matching the client) explicitly on the server
config.

---

## Unauthenticated attack surface

Every command below is reachable **before any AUTHINFO** (SEC-5: reads are never gated). "Limit
today" is what bounds the resource at 66c36f8.

| Command | Resources touched | Limit today | Proposed limit |
|----|----|----|----|
| `LISTGROUP <name>` | **disk/inodes** (new dir + `.db` + schema), **memory** (TasksMap, GHmap, groupDB entry), **memory** (all overview rows loaded) | none (SEC-2, A-nntp-sec-4, SEC-7) | reject unknown group via `MainDBGetNewsgroup`; stream rows; honour range |
| `XHDR <hdr> <range>` | **CPU** (send-loop walks full range) | none; DB read clamped to 1000 but loop unclamped (SEC-1) | iterate returned rows; clamp range to high-water |
| `XOVER <range>` | **memory** (range rows), truncation | Go clamp to ~1001 rows, silent (BUG-2) | SQL LIMIT + client-visible paging |
| `GROUP`/`ARTICLE`/`HEAD`/`BODY`/`STAT <name/num>` | **disk/memory** (same group-DB creation if name selected via LISTGROUP), serves hidden articles | GROUP validates; ARTICLE-by-num path does not clamp count | validate group; filter `hide`; set DBArtNum |
| any article command | **CPU** (200 ms sleep/cmd default — self-limiting but serial) | `ArticleCmdDelay` default 200 ms | keep; add global rate limit |
| `POST`/`IHAVE`/`TAKETHIS` body | **memory** (article lines) | 16384 lines × unbounded line length; `MaxArtSize` ignored (SEC-3) | enforce `MaxArtSize` bytes |
| any unknown word | **memory** (stats map key per distinct word) | none (SEC-4) | record only known commands |
| any command line | **memory** (whole line buffered) | none; `ReadLine` unbounded (SEC-3) | byte-limited reader |
| `QUIT`+pipelined cmd | **process** (crash) | none — nil deref, no recover (A-nntp-sec-1) | nil guard + `recover` |
| `AUTHINFO USER/PASS` | credentials in cleartext; no throttle | plaintext accepted, no back-off (SEC-5) | require TLS; rate-limit 481 |

## Measured repros

Harness in-process (the test binary *is* the server), delays 0, `ENABLE_ARTICLE_CACHE=false`,
`127.0.0.1:0`. Memory figures are Go heap deltas / map sizes, which is the resource the attacker
grows; "RSS" in the plan is reported as these in-process allocations.

| Finding | Command / input | Number |
|----|----|----|
| SEC-1 (CPU) | `GROUP g; XHDR subject 1-9223372036854775807`, sampled 3 s via getrusage | **103.1 %** of one core (spin) |
| SEC-2 (files) | 4× `LISTGROUP <non-existent>` | **4** `.db` files created (before=0 after=4); all names return `ErrNoRows` from `MainDBGetNewsgroup` |
| SEC-3 (memory) | one command line of 1 KiB / 1 MiB / 8 MiB | **8 MiB** line accepted and buffered before reply |
| SEC-4 (memory) | 5000 distinct unknown commands, one connection | stats map = **5000** entries |
| SEC-7 (memory) | `GetOverviews` on a 50 000-row group (LISTGROUP source) | 50 000 rows loaded, **+32.13 MiB** heap for one call |
| SEC-6 (auth) | authenticate → `UpdateNNTPUserPassword` → authenticate old pass | old pass `err=<nil>`, new pass `err=<nil>` (both accepted) |
| A-nntp-sec-1 (crash) | `QUIT\r\nMODE READER\r\n` (one write) | child exits status 2, SIGSEGV at `cliconns.go:60` via `:81` |

Commands (temporary `zz_audit_nntp-sec_test.go`, now deleted):
`go test -race -run TestZZ -v -timeout 600s ./internal/nntp/`; SEC-1 was run as the isolated child
directly (`PUGLEAF_ISOLATED=TestZZSec1XHDRSpin PUGLEAF_TEST_DATADIR=<tmp> go test -run
TestZZSec1XHDRSpin -v ./internal/nntp/`).

## Races

A temporary stress test (`TestZZRaceConcurrent`) ran **100 concurrent clients**, each doing
`GROUP → XOVER 1-50 → XOVER 1-50 (drain) → STAT 1 → ARTICLE 2 (drain) → QUIT`, against the harness
server (with an `hFakeProcessor`, `MaxConns 200`), under `go test -race`:

```
--- PASS: TestZZRaceConcurrent (0.34s)    RACE done: 100 clients
```

No data race was reported. The read path's shared state holds up under `-race`: `ServerStats` is
guarded by `mux` (`statistics.go`), `Local430` by `mu` (`nntp-cache-local.go`), and GroupDB
worker accounting by `MainMutex`/`GroupDB.mux` (`db_groupdbs.go`, documented locking order). The
concurrency defects in this report are liveness/resource issues (SEC-1 spin, A-nntp-sec-1 crash,
BUG-7 leaks), not data races.

## TLS and shutdown

- **tls.Config defaults.** The server sets only `Certificates` (`nntp-server.go:125-127`): no
  `MinVersion`, `CipherSuites` or `NextProtos`. Protocol floor and ciphers come from Go's defaults
  (TLS 1.2 minimum on the current toolchain). The client pins TLS 1.2 explicitly
  (`nntp-client.go:183`); the server does not (A-nntp-sec-5). No STARTTLS is offered, so AUTHINFO on
  the plain port is cleartext (SEC-5).
- **Stop() with live clients.** `Stop()` closes `shutdown` and the listeners but does **not** close
  existing client connections and does **not** wait on `wg` (`nntp-server.go:238-251`, comment
  `:240-244`). Idle clients persist until their deadline; a stuck handler (SEC-1) never ends. The
  process-level `wg.Wait()` (`cmd/nntp-server/main.go:148`) then blocks indefinitely — shutdown is
  not actually graceful.
- **Leak on cert failure.** `Start()` starts the plain listener before loading the TLS cert; a
  `LoadX509KeyPair` failure returns an error while `running` is still false, leaving the plain
  accept goroutine running and un-stoppable by `Stop()` (which early-returns on `!running`). The
  shipped `cmd/nntp-server` masks this with `log.Fatalf`, but the package API leaks for embedded
  callers (BUG-7).
- **Leaked cleanup goroutine.** `NewNNTPServer` starts `CronLocal430` with no stop path
  (`nntp-server.go:95`), leaked on every server instance (BUG-7).

## Function inventory

| function / file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `nntp-cmd-{article,head,body,stat}.go` | yes | — | — | — | — | `// stub` only; real handlers are in `nntp-article-common.go` |
| `nntp-cmd-reader.go` | yes | — | — | — | — | `package nntp` only |
| `(*ClientConnection).parseArticleHeadersShort` (`nntp-cmd-helpers.go:217`) | — | yes | yes | — | — | staticcheck U1000; only `parseArticleHeadersFull` is used |
| `AuthManager.CheckGroupAccess` (`nntp-auth-manager.go:47`) | — | yes | yes | yes | — | never called; always returns true (no real group ACL) |
| `AuthManager.CanPost` / `IsAdmin` (`:60,72`) | — | yes | yes | — | — | never called; posting is checked via `c.user.Posting` inline |
| `AuthManager.CheckConnectionLimit` (`:84`) | — | yes | yes | — | — | never called; per-user `MaxConns`/sessions unenforced (SEC-5) |
| `ServerStats.{GetTotalConnections,GetCommandCount,GetAllCommandCounts,GetAuthStats,GetUptime,Reset}` (`statistics.go`) | — | yes | yes | — | — | no caller in the server; `Reset` would be the only bound on SEC-4 |
| `ClientConnection.capabilities` field (`cliconns.go:31`) | — | yes | yes | — | — | set to `[]string{}` and never read; caps come from `getServerCapabilities` |
| `CacheMessageIDNumtoGroup` (`nntp-cache-local.go:262-354`) | — | yes | — | — | — | entire type commented out |
| `generatePasswordHash` naming (`nntp_auth_cache.go:44`) | — | — | — | yes | — | sha256 cache key; comment correctly warns "NOT for storage" |

## Seed items rejected

None. SEC-1…7, BUG-1, BUG-2, BUG-7, BUG-8 are all CONFIRMED above. The SEC-6 revoked-user half was
verified as already fixed (LO:F22) and, per the slice instructions, is not re-reported.

## Not covered / follow-ups

- RFC conformance of these commands (501 vs 500, missing OVER/HDR/DATE/NEXT/LAST, CAPABILITIES
  content) is `audit-nntp-rfc` (RFC-1…6, BUG-3…6, BUG-9, FN-1).
- The DB-side of SEC-2 (auto-creation in `GetGroupDB`), SEC-6 (`nntp_auth_cache` keying), BUG-1/BUG-2
  (query clamps), and NULL-scan / re-open globals are `audit-db`.
- Intake correctness of POST/IHAVE/TAKETHIS (message-id, date, newsgroups parsing) is `audit-intake`
  (INT-1…6); this slice only covers their resource/size behaviour (SEC-3).
- `errcheck` on `UpdateNNTPUserLastLogin` (`db_nntp_users.go:325`) is a minor unchecked-error, noted
  for `audit-db`.
- Suggested test markers for wave 2 (`knownBug` ids): SEC-1, SEC-2, SEC-3, SEC-4, SEC-6, SEC-7,
  BUG-1, BUG-2, BUG-8, A-nntp-sec-1, A-nntp-sec-2.
