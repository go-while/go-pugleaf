# Plan: NNTP server hardening (security, DoS, RFC conformity of the reader side)

Slug: `nntp-server-hardening` · Integration branch: `plan-nntp-server-hardening` (from `testing-001`)
Run with: `/run-plan .claude/plans/queued/nntp-server-hardening.md`
Parallelism: wave 0 is inline (orchestrator), wave 1 has 2 implementer slices, wave 2 has 2, wave 3 is inline.
Within a wave, no two slices own the same file.
Source: `docs/audit/AUDIT-2026-10.md` (§4 "nntp-server" table, fix-plan column `nntp-server-hardening`), slice reports
`docs/audit/audit-nntp-sec.md`, `docs/audit/audit-nntp-rfc.md`, plus `audit-db.md` (SEC-6/BUG-1/BUG-2 DB side),
`audit-ops.md` (OPS-2 nntpmgr part, A-ops-9) and `audit-web.md` (A-web-1 hide decision, A-web-5 DB part).
Written at `plan-nntp-audit-tests` @ 03d5ef7; every `file:line` below was checked with `grep -n`/`sed -n` at 03d5ef7.

**Ordering with the other queued plans (all branch from `testing-001` when they start):** fixed run order
`nntp-server-hardening` → `nntp-intake-injection` → `nntp-client-transfer` → `nntp-rfc-commands` → `audit-delta-fixes`.
- `nntp-intake-injection` wires `readArticleBlock`/`MaxArticleBytes` from this plan into `readArticleLines`
  (`nntp-cmd-posting.go`) and calls `local430.Remove` after accepted articles (which closes BUG-8: that plan removes
  the `TestArtLocal430/ihave-arrival` marker; T-nntp-article-1 stays, decision 9).
- `nntp-rfc-commands` (`Depends on: nntp-server-hardening`) reuses the range parser, the dispatch/CAPABILITIES layout,
  `commandMaxArgs`/`knownStatCommands` and `dataBlockWriter`.
- `audit-delta-fixes` uses the A-web-5 DB contract of slice `nntp-auth-db` (Design K1, "Contracts for other plans").

---

## Context

The October 2026 audit (`plan-nntp-audit-tests`, 153 findings) found two critical, unauthenticated remote DoS bugs in
the NNTP server (one packet kills the process; one command pins a core forever), group-DB creation for arbitrary names,
unbounded memory per command, wrong or missing RFC 3977/2980/4643 behaviour, and an auth cache that accepts an old
password for 15 minutes. The wave-2 test suite (`internal/nntp/nntp-server-*_test.go`, `cmd/nntp-server/server_e2e_test.go`)
marks every one of them with `knownBug(t, "<id>", err)`; this plan removes those markers (keeping the assertions).

### Findings (anchors at 03d5ef7; paths under `internal/` unless they start with `cmd/`)

**Critical / high**
| ID | Where | Defect | Sev |
|----|----|----|----|
| A-nntp-sec-1 (alias A-nntp-rfc-1) | `nntp/nntp-cmd-basic.go:53-64`, `nntp/nntp-server-cliconns.go:60,77,81`, `nntp/nntp-server.go:202-214` | `QUIT\r\n<cmd>\r\n` in one write: `Close()` nils `c.conn` (`basic.go:63`), the buffered next line reaches `UpdateDeadlines` (`cliconns.go:81` → nil deref at `:60`); `handleConnection` has no `recover`, the whole process exits. Unauthenticated. | critical |
| SEC-1 | `nntp/nntp-cmd-xhdr.go:85,97-104`, `database/queries.go:1878-1880` | `XHDR <hdr> 1-9223372036854775807`: DB read clamped to 1001 rows, the send loop walks the unclamped range with no I/O (103 % CPU, `Stop()` and `main`'s `wg.Wait()` hang). Unauthenticated. | critical |
| SEC-2 (server side) | `nntp/nntp-cmd-group.go:51`, `database/db_groupdbs.go:117-190` | `LISTGROUP <any name>` calls `GetGroupDB` without `MainDBGetNewsgroup`: a directory, SQLite file and schema per name. | high |
| SEC-3 (server side) | `nntp/nntp-server-cliconns.go:77`, `config/config.go:131` | Command lines use unbounded `textConn.ReadLine` (8 MiB accepted); `Server.NNTP.MaxArtSize` is never read. (The article reader in `nntp-cmd-posting.go:221-261` is wired by `nntp-intake-injection`.) | high |
| SEC-7 | `nntp/nntp-cmd-group.go:59`, `database/queries.go:696-715` | LISTGROUP loads every overview row (+32 MiB for 50 000 rows) and logs each call (`queries.go:699`). | high |
| A-nntp-rfc-2 | `nntp/nntp-cmd-helpers.go:20-23`, `nntp/nntp-article-common.go:231-235,267-271` | Stored header lines are sent unstuffed and unsanitized: a stored `.` header line ends HEAD/ARTICLE early, the rest is read as status lines (stream desync). Served side only; the intake root cause A-intake-2 is `nntp-intake-injection`'s. | high |

**Medium**
| ID | Where | Defect | Sev |
|----|----|----|----|
| SEC-4 | `nntp/nntp-server-cliconns.go:116,154-155`, `nntp/nntp-server-statistics.go:57-61` | Every unknown command word becomes a stats-map key; the `default:` branch has no error delay. | medium |
| SEC-5 | `nntp/nntp-cmd-auth.go:18-42` (481 at `:34`), `nntp/nntp-server-cliconns.go:200-217`, `nntp/nntp-auth-manager.go:47-90` | No read-auth option; AUTHINFO USER advertised on plaintext; 481 not delayed; a second AUTHINFO after 281 accepted (RFC 4643: 502); per-user `maxconns` never enforced. | medium |
| SEC-6 | `database/db_nntp_users.go:281-328` (hit at `:284`), `database/nntp_auth_cache.go:50-90` | The auth cache keys a hit on sha256(login password): the old password works up to 15 min after `UpdateNNTPUserPassword` (`:137-147`). Separate processes, so the fix re-checks the stored hash on a hit (AUDIT §11.8). | medium |
| A-nntp-sec-2 | `database/queries.go:890,917,1805-1808,1864-1870`, `database/thread_cache.go:309-315` | No NNTP read query filters `articles.hide`: hidden articles are served by LISTGROUP/XOVER/XHDR/ARTICLE/HEAD/BODY/STAT. | medium |
| A-nntp-sec-6 | `database/db_nntp_users.go:302-305,308`, `nntp/nntp-cmd-auth.go:34` | Username enumeration by timing: unknown user 144 µs vs known user 109.8 ms (bcrypt only for existing users). | medium |
| BUG-1 | `database/queries.go:892-914` (`:906`), `nntp/nntp-article-common.go:60` | `GetArticleByNum` never sets `DBArtNum`: ARTICLE/HEAD/BODY `<n>` leave the current article at 0. | medium |
| BUG-2 | `database/queries.go:1816-1818,1878-1880` | XOVER/XHDR clamp the number range to start+1000 in Go (no SQL `LIMIT`): silent truncation. | medium |
| BUG-4 | `nntp/nntp-cmd-group.go:29-35,39-73`, `nntp/nntp-cmd-list.go:36-37` | GROUP low=1 and no current article; LISTGROUP bare `211`, no selection, range ignored; LIST ACTIVE low=1, status always `n`. | medium |
| BUG-5 | `nntp/nntp-cmd-helpers.go:68-80` | Overview/XHDR fields not sanitized for TAB/CR/LF; trailing empty 9th field (`:70`). | medium |
| BUG-6 | `nntp/nntp-cmd-helpers.go:20-23,49-53`, `nntp/nntp-article-common.go:231-247,267-271` | Header lines not dot-stuffed; empty `headers_json` gives a blank line in HEAD; an empty body gives an extra blank line. | medium |
| BUG-7 (server part) | `nntp/nntp-server.go:95,108-137,202-214,217-255` | `Stop()` neither closes clients nor waits (`:238-251`); `CronLocal430` leaks a goroutine per server (`:95`); a TLS cert failure leaves the plain listener running with `running=false` (`:115` before `:120-122`); QUIT ends in a logged "Connection error" (`:212`). The "too large → 441 after close" part is in `nntp-cmd-posting.go:227-229` → `nntp-intake-injection`. | medium |
| RFC-3 | `nntp/nntp-article-common.go:58,95-98,195-196` | ARTICLE/HEAD/BODY/STAT without argument → 501 (current article / 412 / 420 expected); `0`/negative → 502 (423 expected). | medium |
| RFC-5 (MODE/AUTHINFO part) | `nntp/nntp-cmd-basic.go:25`, `nntp/nntp-cmd-auth.go:45` | Unknown MODE/AUTHINFO variant → 500 (501). POST 440 and IHAVE 437 live in `nntp-cmd-posting.go` → `nntp-intake-injection`. | medium |
| RFC-6 | `nntp/nntp-cmd-xhdr.go:35-74,97-104`, `nntp/nntp-cmd-xover.go:65-68,86`, `database/queries.go:1907-1910` | XHDR serves 7 headers only (others: empty 221), no message-id form; empty/reversed range → empty 224/221 or 501 instead of 423/420. | medium |

**Low / info**
| ID | Where | Defect | Sev |
|----|----|----|----|
| RFC-2 | `nntp/nntp-server-cliconns.go:200-217` | Static CAPABILITIES: READER next to MODE-READER, unregistered `TAKETHIS`, AUTHINFO after auth and on plaintext, POST for non-posting users. | low |
| RFC-4 | `nntp/nntp-server-cliconns.go:97-103` (hostname `:98`), `nntp/nntp-cmd-basic.go:23` | Greeting and MODE READER always 200 (201 without a processor); hostname hardcoded. | low |
| BUG-3 | `nntp/nntp-server.go:38-42`, `nntp/nntp-article-common.go:46-48`, `nntp/nntp-cmd-group.go:10-12`, `nntp/nntp-cache-local.go:15-20` | Delays not configurable (200 ms per article command, 333 ms per GROUP, 1 s error, 15 s cron tick); error delay applied inconsistently. | low |
| BUG-8 | `nntp/nntp-cache-local.go:15-52`, `nntp/nntp-article-common.go:131,150` | `local430` never invalidated when the article arrives: 430 for up to ~75 s after 235/239/240. | low |
| BUG-9 | `nntp/nntp-server-cliconns.go:15,54-63,81` | Idle timeout 60 s (RFC 3977 §3.1: ≥ 3 min); one write deadline per command covers the whole response. | low |
| A-nntp-rfc-3 | `nntp/nntp-server.go:188-192,206` | At `MaxConns` the socket is closed without a 400 greeting; the slot is counted in the handler goroutine, so a burst overshoots. | low |
| A-nntp-rfc-4 | `nntp/nntp-cmd-group.go:62`, `nntp/nntp-cmd-list.go:30,50`, `nntp/nntp-cmd-xover.go:74,82`, `nntp/nntp-cmd-xhdr.go:80,88`, `nntp/nntp-article-common.go:84,166`, `nntp/nntp-server-cliconns.go:89` | Internal faults answered 503/411/500 instead of 403. | low |
| A-nntp-rfc-5 | `nntp/nntp-cmd-group.go:20-35`, `nntp/nntp-article-common.go:101-119`, `nntp/nntp-cmd-basic.go:15-26`, `nntp/nntp-cmd-list.go:9-22`, `nntp/nntp-cmd-xover.go:19-62` | Surplus arguments silently ignored (501 required). | low |
| A-nntp-rfc-6 | `nntp/nntp-server-cliconns.go:155`, `nntp/nntp-cmd-basic.go:25`, `nntp/nntp-cmd-list.go:21`, `nntp/nntp-cmd-auth.go:21,45` | Status lines echo client input without bound (633-octet status line). | low |
| A-nntp-rfc-10 | `nntp/nntp-server-cliconns.go:165-173,160-163` | One write syscall per response line; two buffered writers (`c.writer`, `textConn.W`) on one socket. | low |
| A-nntp-rfc-12 | `cmd/nntp-server/main.go:43,51-66,76`, `nntp/nntp-server.go:137` | No port flags → "started" and serves nothing; `-maxconnections` text says "authenticated"; `Server.Hostname` unused. | low |
| A-nntp-sec-3 | `database/queries.go:1820,1836,1913,1937` | The range reads skip `rows.Err()` and bypass `RetryableQuery`. | low |
| A-nntp-sec-4 | `database/db_groupdbs.go:153-167`, `database/db_batch.go:187-210`, `database/groups_hashmap.go:112-139` | Each unvalidated LISTGROUP name also leaves groupDB/TasksMap/GHmap entries and an open `*sql.DB`. | low |
| A-nntp-sec-5 | `nntp/nntp-server.go:125-127` | Server `tls.Config` sets only `Certificates` (no `MinVersion`). | low |
| T-nntp-group-1 | `nntp/nntp-cmd-group.go:39-56` (test `nntp-server-group_test.go:388`) | LISTGROUP ignores `newsgroups.active`: inactive group → 211 (411 expected). | low |
| T-nntp-article-1 | `nntp/nntp-cache-local.go:46` (test `nntp-server-article_test.go:859`) | `local430` has no coherence with intake in other processes: 430 for up to ~75 s after the fetcher stored the article. | low |
| OPS-2 (nntpmgr part) | `cmd/nntpmgr/main.go:153,172-192` (hash at `:186`) | `-list` prints bcrypt hashes; `-create` echoes the plaintext password. | low |
| A-ops-9 | `cmd/nntpmgr/main.go:31-32,109-116,226-249`, `database/db_nntp_users.go:30-41` | `-update` writes both flag defaults (revokes posting / resets maxconns), no range check; `GetNNTPUserByUsername` filters `is_active`, so inactive users can't be updated, deleted or re-activated. | low |
| A-web-5 (DB part) | `database/db_nntp_users.go:58-69` | `GetNNTPUserByWebUserID` picks an arbitrary row (no `ORDER BY`), `0` is not rejected, no way to count or update the mapping. Web handler part → `audit-delta-fixes`. | medium |
| FN-1 | `nntp/nntp-cmd-{article,body,head,stat}.go` (stubs), `nntp/nntp-cmd-reader.go`, `nntp/nntp-server-cliconns.go:31-33,220`, `nntp/nntp-auth-manager.go:47-90`, `nntp/nntp-cache-local.go:54-146`, `nntp/nntp-cmd-basic.go:30-50`, `nntp/README.md` | Stub files, dead helpers and fields, commented-out cache type, misleading HELP text and README. | info |
| A-nntp-rfc-9 | `nntp/nntp-article-common.go:131-143,155` | Read-only (no processor) servers can't find message-ids outside the current group; debug reply texts `Cache says no!`, `NotF0`, `NotF1`. | info |
| A-nntp-rfc-11 (alias T-nntp-auth-1) | `nntp/nntp-cmd-auth.go:10-15`, `nntp/nntp-server-cliconns.go:107` | `strings.Fields` cuts `AUTHINFO PASS` at the first space: a stored password with spaces never authenticates. | info |

### Decisions (AUDIT §11 and the open questions of the reports; behaviour changes to report at the end)
1. **Reads without auth (§11.1).** The server stays a public reader by default. New opt-in `NNTPServer.ReadRequiresAuth`
   (flag `-readauth`, default false): reader commands before a successful AUTHINFO get `480 Authentication required`.
   SEC-2/SEC-7/A-nntp-sec-4 are fixed for anonymous clients anyway, so the default does not depend on this option.
2. **Hide/spam on NNTP (§11.2, A-nntp-sec-2; must match A-web-1 in `audit-delta-fixes`).** An article with
   `hide > 0` is never served over NNTP: every NNTP read query carries `hide = 0` (NNTP has no admins). It is absent
   from LISTGROUP, XOVER, XHDR, GROUP counts/marks, and ARTICLE/HEAD/BODY/STAT answer 423/430 as if it did not exist.
   The `spam` counter (user flags) is **not** filtered. The web plan applies the same predicate for non-admins.
3. **XHDR values (§11.3, RFC-6).** An absent header gives the line `<n>` (number only; RFC 3977 §8.5.2 allows
   omitting the space). Headers outside the 7 indexed columns are served from `headers_json` (first occurrence,
   unfolded, sanitized). An empty or reversed range gives **423** for XOVER/XHDR (as INN, which implements XOVER as an
   OVER alias; the tests accept 420 or 423), and the same for OVER/HDR in `nntp-rfc-commands`.
4. **Plaintext AUTHINFO (§11.7, review routing of 2026-10-11).** New `NNTPServer.PlaintextAuth` policy (flag
   `-plaintextauth`): `accept` (default: AUTHINFO USER/PASS accepted on plaintext but **not advertised** in
   CAPABILITIES; advertised on TLS), `advertise` (old behaviour: advertised on plaintext too), `refuse` (`483` on
   plaintext, not advertised). Logins over plain TCP keep working by default (harness, e2e, `scripts/test-nntp-server.sh`).
5. **SEC-6 (§11.8).** The cache entry stores the bcrypt hash seen at login; a hit is honoured only while
   `nntp_users.password` still equals it (one row read that a hit already does). Same-process invalidation in
   `UpdateNNTPUserPassword`/`DeactivateNNTPUser`/`DeleteNNTPUser` is added as a secondary measure.
6. **Drain before close (§11.9).** This plan provides the bounded article reader `readArticleBlock` (reads and discards
   an oversized article up to its terminator within a drain bound, then lets the caller reply; reports when the bound is
   exceeded so the caller sends 400 and closes). Wiring it into POST/IHAVE/TAKETHIS (`nntp-cmd-posting.go`) and the
   441/437/439 replies are `nntp-intake-injection`'s (it owns `nntp-cmd-posting.go`; review routing of 2026-10-11).
7. **Delays (BUG-3).** Defaults: `ArticleCmdDelay` 25 ms (was 200 ms), `GroupCmdDelay` 25 ms (was 333 ms; readers
   GROUP every subscription at start-up),
   `ErrorDelay` 1 s (unchanged); flags `-articledelay`, `-groupdelay`, `-errordelay`. Error-delay rule for all
   handlers: call `rateLimitOnError()` before every 412, 420, 423, 430, 481, 482, 500 and 501 reply; never before
   2xx/3xx, 205, 400, 403, 411, 440, 480, 483 or 502 (411 stays undelayed: newsrc scans of dead groups).
8. **Idle and write timeouts (BUG-9).** `DefaultNNTPcliconnTimeout` = 3 min (flag `-idletimeout`). The read deadline
   is refreshed per command and per received article line; the write deadline (same duration) is set per flush, not
   once per command.
9. **local430 (BUG-8, T-nntp-article-1).** local430 stays as cross-connection damping of repeated misses, with a TTL
   of **60 s** (`DefaultLocal430TTL`; user decision of 2026-10-11, the plan's draft said 1 s) enforced in `Check`, at
   most 65 536 entries, and message-ids over 250 octets never cached. New `func (lc *Local430) Remove(messageID string)`:
   `nntp-intake-injection` (its slice S4) calls `c.server.local430.Remove(id)` after a 235/239/240 in
   `nntp-cmd-posting.go`, which closes BUG-8 (that plan removes the `TestArtLocal430/ihave-arrival` marker).
   T-nntp-article-1 (an article stored by another process answers 430 for up to 60 s) is an accepted limitation; its
   marker stays open and documented. Both `TestArtLocal430` markers therefore stay after this plan.
10. **Command line limit (SEC-3).** 512 octets including CRLF (RFC 3977 §3.1): a longer line that ends within
    4096 bytes gets `501 Command line too long` and the session continues; 4096 bytes without LF gets
    `400 Command line too long` and the connection is closed.
11. **Article byte limit (SEC-3, used by `nntp-intake-injection`).** `(*NNTPServer).MaxArticleBytes()` =
    `Config.NNTP.MaxArtSize` (config default 32 KiB; `cmd/nntp-server` flag `-maxartsize`), or 1 MiB when ≤ 0
    (harness servers). Drain bound = max(4 × limit, 1 MiB), capped at 64 MiB. `DefaultMaxArticleBytes`,
    `maxArticleDrainBytes`, `MaxArticleBytes`, `readArticleBlock`, `ErrArticleTooLarge` and `ErrArticleDrainLimit`
    are defined here only; `nntp-intake-injection` consumes them (it drops its own copy of the default).
12. **Per-user `maxconns` (SEC-5) is opt-in.** Existing users default to `maxconns = 1` (nntpmgr `-maxconns` default,
    `CreateNNTPUserForWebUser`), and nntpmgr's 1–25 range cannot set 0, so enforcing it by default would cut every
    reader that opens two connections. New `NNTPServer.EnforceUserMaxConns` (flag `-enforce-user-maxconns`, default
    false). When on: counted in memory per server (the `nntp_sessions` table is not used: a crash would leave stale
    rows), `maxconns <= 0` means unlimited, over the limit → `481 Too many connections for this user`. Open question 1
    asks the user whether to turn it on by default later (after raising existing users' values).
13. **QUIT ignores surplus arguments** (deliberate exception to A-nntp-rfc-5: a client that wants to leave must not be
    kept; `FuzzSessCommandLine` asserts `QUIT now` → 205). TAKETHIS and AUTHINFO are exempt from the argument-count
    table (TAKETHIS must consume its article; AUTHINFO takes the raw rest of the line).
14. **GROUP numbers (BUG-4).** `211 count low high group` with low/high = MIN/MAX(`article_num`) of the visible
    articles in the group DB, count = `newsgroups.message_count` clamped to `high-low+1`; an empty group gives
    `211 0 0 0 group` (an RFC 3977 §6.1.1.2 form). The count is an estimate: `message_count` includes hidden
    articles (decision 2 filters only the marks and the listings), and RFC 3977 §6.1.1.2 allows a count at or above
    the actual number, so tests accept `count >= visible`. The current article becomes `low` (0 = invalid for an empty group).
    LIST ACTIVE reads the main DB only (no group DB opens): `name last_article max(low_water,1) status`, status `y`/`m`
    when posting is allowed for this client and `newsgroups.status` is `y`/`m`, else `n`.
15. **Faults and stream errors (A-nntp-rfc-4).** DB/internal faults → `403 Internal fault`. A DB error in the middle
    of a multi-line block closes the connection (no silent truncation). A handler write error closes the connection
    without a 500.
16. **nntpmgr (OPS-2, A-ops-9).** `-list` never prints password hashes; `-create` prints the password only when it was
    generated (`-password random`); `-update` changes only flags given on the command line, with the `-create` range
    checks; new `-activate`, `-deactivate`, `-setpassword`; lookups include inactive users.
17. **Startup validation in `Start()` (BUG-7, A-nntp-rfc-12).** A TLS port without a cert or key, or no port at all,
    is now an error from `Start()`. Both callers treat it as fatal: `cmd/nntp-server` (`main.go:133`) and the
    embedded server in `cmd/web` (`main.go:470`, only reachable once `-withnntp` is revived by `audit-delta-fixes`):
    `cmd/web -nntptlsport` without `-nntpcertfile`/`-nntpkeyfile` used to skip TLS silently and now exits.
    Behaviour change to report.

### Open questions for the user (answered 2026-10-11 at the wave-1 boundary)
1. Per-user `maxconns` (decision 12): **answered "default to 3"**. The default for new users becomes 3: nntpmgr's
   `-maxconns` flag default (`cmd/nntpmgr/main.go:31`) and `CreateNNTPUserForWebUser` (`db_nntp_users.go:270`) both
   use a new `database.DefaultNNTPUserMaxConns = 3` (defined in `nntp_user_admin.go`, slice `nntp-auth-db`).
   `-enforce-user-maxconns` stays off by default (existing users keep their stored 1 until raised with
   `nntpmgr -update -maxconns`); the user can turn it on later.
2. local430 TTL (decision 9): **answered "set local430 to 60s"** (first answer "not sure what it was made for";
   local430 is a negative cache of message-ids that answered 430, so repeated lookups of a missing article by many
   readers or peers do not hit the DB/processor each time). Consequence: BUG-8 and T-nntp-article-1 markers stay open
   after this plan (see decision 9).
3. Default delays (decision 7): **accepted** (25 ms per article command, 25 ms per GROUP, 1 s error delay).

### Out of scope (owned elsewhere; cross-reference only)
- `nntp-rfc-commands`: RFC-1 (DATE, NEXT, LAST, NEWGROUPS, NEWNEWS, OVER/HDR, LIST OVERVIEW.FMT/HEADERS, the LIST
  wildmat in `nntp-cmd-list.go`, MODE STREAM + CHECK + STREAMING, STARTTLS). It reuses `parseRangeArg`,
  `requireGroup`, `dataBlockWriter`, `commandMaxArgs`, `readerCommands`, `knownStatCommands` and adds its own
  dispatch/CAPABILITIES lines.
- `nntp-intake-injection` (owns `internal/nntp/nntp-cmd-posting.go`): SEC-2 intake side, SEC-3 article limit wiring
  (`readArticleLines` → `readArticleBlock(c.server.MaxArticleBytes())`), BUG-7 "too large → reply after close" and the
  drain requirement (§11.9), every RFC-5 code in that file (POST not permitted 502 → 440, POST/IHAVE/TAKETHIS without
  a processor 502 → 500/503 in the `handlePost`/`handleIHave`/`handleTakeThis` gate lines, IHAVE 436 → 437; this
  plan's RFC-5 part is only the MODE/AUTHINFO unknown variant 500 → 501 in `nntp-cmd-basic.go`/`nntp-cmd-auth.go`),
  A-nntp-rfc-7 (message-id
  syntax), A-nntp-rfc-8 (TAKETHIS reply details), INT-1…6, A-intake-*. Its markers (`TestE2ESizeLimits/*`,
  `TestE2ERejectUnknownGroup`, `TestE2ERejectMissingNewsgroups/ihave`, `TestAuthPostingGates/no-posting-post`, INT-1,
  INT-2) stay open after this plan.
- `audit-delta-fixes`: A-web-1 (web hide gating, same predicate as decision 2), A-web-5 web handler part (uses the DB
  contract below), OPS-2 `-withnntp` and the peering test cwd writes, A-ops-10, A-db-2 (article cache; NNTP stops
  using it here), OPS-1, `GetOverviews`'s per-call log (`queries.go:699`, web caller only after this plan).
- Not touched: `internal/database/thread_cache.go`, `internal/web/*`, `internal/config/*`, `go.mod`.

---

## Design

### K1 Contracts (stable unless listed here)
- Unchanged: `NewNNTPServer(db, cfg, mainWG, processor)`, `Start`, `Serve`, `Stop`, `IsRunning`, the
  `ArticleProcessor` interface, `NewClientConnection`, every `handle*` method name, `ProcessorAdapter`,
  `ErrArticleNotFound`, the exported `NNTPServer` fields `ArticleCmdDelay`, `GroupCmdDelay`, `ErrorDelay`,
  `IdleTimeout`, `Config`, `DB`, `Stats`, `AuthManager`, `Listener`, `TLSListener`, `NNTPAuthCache.Get`
  (signature `Get(username, password string) (int, bool)` is used by `nntp-server-auth_test.go:406` and
  `database/lo2_writer_test.go`), and all other `internal/database` exported functions.
- Changed on purpose (all callers are listed and updated by the owning slice):
  - `handleAuthInfo(args []string, line string) error` and `handleQuit(args []string) error` (only caller: the
    dispatch; applied in wave 0).
  - `(*Database).GetOverviewsRange(groupDB, startNum, endNum int64, limit int)` and
    `(*Database).GetHeaderFieldRange(groupDB, field string, startNum, endNum int64, limit int) ([]HeaderFieldValue, error)`
    (only callers: `nntp-cmd-xover.go:79`, `nntp-cmd-xhdr.go:85`).
  - `(*NNTPAuthCache).Set(userID int, username, password, storedHash string)` (only caller `db_nntp_users.go:321`).
  - Removed: `AuthManager.{CheckGroupAccess,CanPost,IsAdmin,CheckConnectionLimit}`, `ClientConnection.{capabilities,
    created,lastCommand,writer}` fields, `ClientConnection.RemoteAddr`, `parseArticleHeadersShort`,
    `formatOverviewLine`, `Local430.CronLocal430`.

### K2 Reserved names per slice (check `grep -rnw '<name>' internal cmd` is empty at the wave base)
- **wave 0 (orchestrator):** file `internal/nntp/nntp-server-dataio.go`: `dataBlockWriter`, `newDataBlock`,
  `(*dataBlockWriter).Line`, `(*dataBlockWriter).Close`, `sendDataBlock`, `cleanDataLine`, `dataBlockFlushLines`,
  `(*ClientConnection).setWriteDeadline`, `(*ClientConnection).ioTimeout`. File `internal/nntp/nntp-server-commands.go`
  (stubs, owned by `nntp-session` from wave 1): `argCountOK`, `readGateOK`, `authinfoAdvertised`, `postingAllowed`.
- **nntp-session:** `errQuit`, `errCommandTooLong`, `errCommandOverflow`, `readCommandLine`, `readArticleBlock`,
  `ErrArticleTooLarge`, `ErrArticleDrainLimit`, `DefaultMaxCommandLineBytes`, `commandLineHardLimit`,
  `DefaultMaxArticleBytes`, `maxArticleDrainBytes`, `(*NNTPServer).MaxArticleBytes`, `commandMaxArgs`,
  `readerCommands`, `knownStatCommands`, `PlaintextAuthPolicy`, `PlaintextAuthAccept`, `PlaintextAuthAdvertise`,
  `PlaintextAuthRefuse`, `ParsePlaintextAuthPolicy`, fields `PlaintextAuth`, `ReadRequiresAuth`, `ShutdownTimeout`,
  `DefaultShutdownTimeout`, `DefaultLocal430TTL`, `local430MaxEntries`, `local430CleanupEvery`, fields
  `EnforceUserMaxConns`, `stopping`,
  `connsMu`, `conns`, `handlers`, `userConns`, `bgOnce`, `(*NNTPServer).startBackgroundLocked`,
  `(*NNTPServer).trackConn`, `(*NNTPServer).untrackConn`, `(*NNTPServer).acquireUserSlot`,
  `(*NNTPServer).releaseUserSlot`, `authinfoArgument`, `(*Local430).Remove`, `(*Local430).run`, field `Local430.ttl`,
  test helpers `sessPipeConn`, `sessFreePort`.
- **nntp-overview:** file `internal/nntp/nntp-cmd-range.go`: `articleRange`, `(articleRange).Empty`, `parseRangeArg`,
  `errRangeSyntax`, `(*ClientConnection).requireGroup`; in `nntp-cmd-xover.go`/`nntp-cmd-xhdr.go`: `overviewLine`,
  `sanitizeOverviewField`, `(*ClientConnection).xhdrByMessageID`; DB file `internal/database/nntp_overview.go`:
  `HeaderFieldValue`, `OverviewRangeChunk`, `HeaderValueFromJSON`, `NNTPHeaderByMessageID`; test prefixes `nshOvr`/`TestNSHOvr`.
- **nntp-read:** DB file `internal/database/nntp_reads.go`: `NNTPArticleByNum`, `NNTPArticleByMessageID`,
  `NNTPArticleMessageID`, `NNTPGroupWatermarks`, `NNTPArticleNumbers`, `NNTPActiveNewsgroups`, `NNTPListGroupChunk`;
  nntp: `articleHeaderLines`, `articleBodyLines`, `(*ClientConnection).selectableGroup`,
  `(*ClientConnection).selectGroup`, `(*ClientConnection).resolveArticleArg`, `(*ClientConnection).visibleInOtherGroup`;
  test prefixes `nshRead`/`TestNSHRead`.
- **nntp-auth-db:** `nntpDummyHash`, `nntpDummyOnce`, `(*NNTPAuthCache).GetEntry`, `(*NNTPAuthCache).RemoveUserID`,
  field `AuthCacheEntry.StoredHash`; DB file `internal/database/nntp_user_admin.go`: `GetNNTPUserByUsernameAnyState`,
  `CountNNTPUsersByWebUserID`, `UpdateNNTPUserWebUserID`, `DefaultNNTPUserMaxConns`; nntpmgr: `flagsSet`, `setNNTPUserActive`,
  `setNNTPUserPassword`; test prefixes `nshAuth`/`TestNSHAuth`.

### K3 Tests
- Existing test files keep their K2 prefixes from `nntp-audit-tests` (`sess`, `auth`, `grp`, `art`, `ovr`, `e2e`); new
  tests go into the file of their topic with its prefix. New DB tests: `internal/database/nsh_overview_test.go`,
  `nsh_reads_test.go`, `nsh_auth_test.go`, using `w0DB(t)`/`w0Name(prefix)` from `internal/database/testmain_test.go`.
- Removing a marker means: replace `knownBug(t, "<id>", <expr>)` by `if err := <expr>; err != nil { t.Fatal(err) }`
  (or `t.Fatal(err)` on the existing `err`), keeping the assertion text. A marker whose check now passes makes the
  suite fail ("known bug … no longer reproduces"), so every slice must remove exactly the markers listed for it.
- No `t.Parallel()`, no package global mutated after `TestMain`, use per-server fields; no sleep longer than 100 ms
  (poll with a deadline); anything that can spin goes through `runIsolated`.
- The harness (`nntp-server-harness_test.go`) and `nntp-wiring_test.go` stay read-only. Note: `wiringTestConn`
  (`nntp-wiring_test.go:59-80`) builds an `NNTPServer` literal with only `DB` (a bare `&database.Database{}`),
  `local430` (no `ttl`) and `Processor`: handler code reachable from the wiring tests (STAT/ARTICLE by message-id
  with no group selected, IHAVE/TAKETHIS) must not dereference `Config`, `Stats`, `AuthManager` or the DB beyond what
  it does today, and `Local430` must treat `ttl <= 0` as `DefaultLocal430TTL`.

### K4 Hygiene
Nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt`, or files outside their ownership list.
`gofmt` runs on owned files only; the 2-file baseline (`internal/database/embedded_migrations.go`,
`internal/web/web_admin_provider.go`) stays. Logs use `[NNTP]` (server), `[NNTP-AUTH]` (auth), `[NNTPMGR]` (tool).

### K5 Same-wave rule
No slice calls a helper another slice of the same wave adds. Wave-0 helpers (`dataBlockWriter`, the commands stubs)
may be used by everyone; wave-1 helpers (`parseRangeArg`, `requireGroup`, `readArticleBlock`, `postingAllowed` as
finished by `nntp-session`) may be used in wave 2.

### D1 Shared data-block writer (wave 0, `internal/nntp/nntp-server-dataio.go`)
One writer per connection: everything goes through `c.textConn.W` (the `bufio.Writer` of the textproto conn).
```go
const dataBlockFlushLines = 256

// dataBlockWriter writes one multi-line data block (RFC 3977 §3.1.1).
type dataBlockWriter struct {
	c *ClientConnection
	n int // lines since the last flush
}

func (c *ClientConnection) newDataBlock() *dataBlockWriter { return &dataBlockWriter{c: c} }

// cleanDataLine strips one trailing CR, replaces every other CR, LF and NUL by a space.
func cleanDataLine(s string) string

// Line writes cleanDataLine(s), dot-stuffed (a leading "." becomes ".."), plus CRLF; it flushes
// (after setWriteDeadline) every dataBlockFlushLines lines.
func (d *dataBlockWriter) Line(s string) error

// Close writes ".\r\n" and flushes.
func (d *dataBlockWriter) Close() error

// sendDataBlock sends "code msg" and lines as one block (CAPABILITIES, HELP, short lists).
func (c *ClientConnection) sendDataBlock(code int, msg string, lines []string) error

// ioTimeout is IdleTimeout, or DefaultNNTPcliconnTimeout when <= 0 or c.server == nil.
func (c *ClientConnection) ioTimeout() time.Duration
// setWriteDeadline sets conn write deadline now+ioTimeout(); no-op when c.conn == nil.
func (c *ClientConnection) setWriteDeadline()
```
Status lines stay on `sendResponse` (`textConn.PrintfLine`, same writer). Wave 0 also changes `sendLine`
(`cliconns.go:165-173`) to write through `c.textConn.W` and removes the `writer` field (`:21`, init `:43`), so there
is one writer from wave 0 on; slices convert their blocks to `dataBlockWriter`.

### D2 Range parser (wave 1, owned by `nntp-overview`, `internal/nntp/nntp-cmd-range.go`; also used by `nntp-read` and `nntp-rfc-commands`)
```go
// articleRange is a parsed RFC 3977 §3.2.1.1 range argument: "n", "n-" or "n-m".
// First and Last are inclusive; for "n-" Last is math.MaxInt64 and Open is true.
// A reversed range ("5-3") parses fine and is Empty(); it is not a syntax error.
type articleRange struct {
	First, Last int64
	Open        bool
}

func (r articleRange) Empty() bool { return r.Last < r.First }

var errRangeSyntax = errors.New("syntax error in range")

// parseRangeArg parses arg. Each number is 1..19 ASCII digits whose value fits in int64
// (RFC 3977 §9.8 requires 1..16; 17-19 digits are accepted because TestOvrHugeRange and the
// N22 probe send 9223372036854775807), 0 allowed; anything else ("", "-", "x-5", "5-y",
// "1-2-3", "-5", "+1", 20+ digits, a value above math.MaxInt64) returns errRangeSyntax. It sends nothing: the caller answers rateLimitOnError() + 501 "Syntax error in range".
func (c *ClientConnection) parseRangeArg(arg string) (articleRange, error)

// requireGroup sends rateLimitOnError() + "412 No newsgroup selected" and returns false when no
// group is selected. A send error is dropped on purpose (the next read on that connection fails).
func (c *ClientConnection) requireGroup() bool
```
Caller rules: syntax error → 501; no group → 412 (via `requireGroup`); `Empty()` or no visible article in the range →
423 `No articles in that range` (XOVER, XHDR, LISTGROUP returns an empty list instead, RFC 3977 §6.1.2.2; OVER/HDR in
`nntp-rfc-commands` likewise 423); the no-argument forms use `c.currentArticle` and answer 420 when it is 0.
Open ranges need no clamping: the chunked SQL reads stop at the last row.

### D3 Command table and gates (wave 0 stubs, finished by `nntp-session`, `internal/nntp/nntp-server-commands.go`)
```go
// commandMaxArgs: maximum number of arguments per command; -1 = not checked here.
var commandMaxArgs = map[string]int{
	"CAPABILITIES": 1, "MODE": 1, "AUTHINFO": -1, "QUIT": -1, "HELP": 0, "LIST": 2, "GROUP": 1,
	"LISTGROUP": 2, "STAT": 1, "HEAD": 1, "BODY": 1, "ARTICLE": 1, "XOVER": 1, "XHDR": 2,
	"POST": 0, "IHAVE": 1, "TAKETHIS": -1,
}
// readerCommands need auth when NNTPServer.ReadRequiresAuth is set.
var readerCommands = map[string]bool{
	"GROUP": true, "LISTGROUP": true, "LIST": true, "STAT": true, "HEAD": true, "BODY": true,
	"ARTICLE": true, "XOVER": true, "XHDR": true, "NEXT": true, "LAST": true, "OVER": true,
	"HDR": true, "NEWGROUPS": true, "NEWNEWS": true,
}
func (c *ClientConnection) argCountOK(command string, args []string) bool // unknown commands: true
func (c *ClientConnection) readGateOK(command string) bool                // !ReadRequiresAuth || authenticated || !readerCommands[command]
func (c *ClientConnection) authinfoAdvertised() bool
func (c *ClientConnection) postingAllowed() bool // Processor != nil && (!authenticated || user == nil || user.Posting)
```
Wave-0 stub bodies: `argCountOK` and `readGateOK` return `true`; `authinfoAdvertised` returns
`!c.authenticated && c.isTLS`; `postingAllowed` as in the comment (final). `nntp-session` adds the tables and the
policy: `authinfoAdvertised` = `!c.authenticated && (c.isTLS || c.server.PlaintextAuth == PlaintextAuthAdvertise)`.

### D4 Dispatch lines (handed over by `nntp-session`; applied **inline by the orchestrator in wave 0**)
`handleCommand` (`nntp-server-cliconns.go:106-157`) becomes exactly:
```go
// handleCommand parses and dispatches a command
func (c *ClientConnection) handleCommand(line string) error {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		c.rateLimitOnError()
		return c.sendResponse(500, "Empty command")
	}

	command := strings.ToUpper(parts[0])
	args := parts[1:]

	// Track command execution (unknown words are folded into one bucket, SEC-4)
	c.server.Stats.CommandExecuted(command)

	if !c.argCountOK(command, args) {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error: too many arguments")
	}
	if !c.readGateOK(command) {
		return c.sendResponse(480, "Authentication required")
	}

	// Dispatch to appropriate handler
	switch command {
	case "CAPABILITIES":
		return c.handleCapabilities()
	case "MODE":
		return c.handleMode(args)
	case "AUTHINFO":
		return c.handleAuthInfo(args, line)
	case "QUIT":
		return c.handleQuit(args)
	case "HELP":
		return c.handleHelp()
	case "LIST":
		return c.handleList(args)
	case "GROUP":
		return c.handleGroup(args)
	case "LISTGROUP":
		return c.handleListGroup(args)
	case "STAT":
		return c.handleStat(args)
	case "HEAD":
		return c.handleHead(args)
	case "BODY":
		return c.handleBody(args)
	case "ARTICLE":
		return c.handleArticle(args)
	case "XOVER":
		return c.handleXOver(args)
	case "XHDR":
		return c.handleXHdr(args)
	case "POST":
		return c.handlePost()
	case "IHAVE":
		return c.handleIHave(args)
	case "TAKETHIS":
		return c.handleTakeThis(args)
	default:
		c.rateLimitOnError()
		return c.sendResponse(500, "Command not recognized")
	}
}
```
Compile-safe signature stubs applied in the same wave-0 commit:
- `nntp-cmd-basic.go:53`: `func (c *ClientConnection) handleQuit(args []string) error {` (body unchanged).
- `nntp-cmd-auth.go:9`: `func (c *ClientConnection) handleAuthInfo(args []string, line string) error {` (body unchanged).

### D5 CAPABILITIES lines (handed over by `nntp-session`; applied inline by the orchestrator in wave 0)
`getServerCapabilities` (`nntp-server-cliconns.go:200-217`) becomes exactly:
```go
// getServerCapabilities returns the capability list for the current connection state
// (RFC 3977 §5.2: the list may change with state; RFC 4643 §2.1: no AUTHINFO after login).
func (c *ClientConnection) getServerCapabilities() []string {
	capabilities := []string{"VERSION 2", "READER"}
	if c.authinfoAdvertised() {
		capabilities = append(capabilities, "AUTHINFO USER")
	}
	capabilities = append(capabilities, "LIST ACTIVE NEWSGROUPS", "XOVER", "XHDR")
	if c.postingAllowed() {
		capabilities = append(capabilities, "POST", "IHAVE")
	}
	return capabilities
}
```
MODE-READER and TAKETHIS are no longer listed (TAKETHIS stays dispatched; `STREAMING` is added by
`nntp-rfc-commands` together with MODE STREAM and CHECK). READER stays listed although DATE/NEXT/LAST/NEWGROUPS
arrive only with `nntp-rfc-commands` (RFC-1, open marker).

### D6 Contracts for other plans
- **`nntp-intake-injection`** calls `c.server.local430.Remove(messageID)` after each 235/239/240 (decision 9), and
  replaces the body of `readArticleLines` (`nntp-cmd-posting.go:221-261`) with
  `return c.readArticleBlock(c.server.MaxArticleBytes())` (or a per-group minimum with `newsgroups.max_art_size`)
  and maps `errors.Is(err, ErrArticleTooLarge)` → 441 (POST) / 437 (IHAVE) / `439 <id>` (TAKETHIS) with the
  connection kept, and `errors.Is(err, ErrArticleDrainLimit)` → `400 Article too large, closing connection` + return
  `errQuit` (clean close; the intake plan wraps this in its own `closeAfterReply` helper and adds
  `common.WireArticleSize`: this plan defines neither name; E2E ids: this plan uses N20–N39 in
  `scripts/test-nntp-server.sh`, N40–N49 are reserved for `nntp-intake-injection`). `readArticleBlock` already un-stuffs `..`, splits head/body at the first empty line,
  keeps the 1024-head-line / 16384-line caps (exceeding them is `ErrArticleTooLarge` too) and refreshes the read
  deadline per line. Note for its `TestE2ESizeLimits/lines-20000` child: after this plan a clean client EOF is no
  longer logged as `Connection error from …`, so the child's wait for that line times out harmlessly (it only logs
  when a status line was received); write errors on a closed socket are still logged with that prefix.
- **`audit-delta-fixes`** must not edit `cmd/nntpmgr/main.go` or `internal/database/db_nntp_users.go`: A-ops-9 and the
  A-web-5 DB part are done here (review routing of 2026-10-11); its A-web-5 web handler change uses the contract below.
- **`nntp-rfc-commands`** adds its commands to `commandMaxArgs` (wave owner of `nntp-server-commands.go` then), uses
  `parseRangeArg`/`requireGroup`/`dataBlockWriter`, and extends `getServerCapabilities` and the dispatch inline.
  `knownStatCommands` already contains its command words.
- **`audit-delta-fixes`** (A-web-5 web handlers) relies on: `GetNNTPUserByWebUserID(webUserID int64)` returns the
  active user with the **lowest id** for that mapping and `sql.ErrNoRows` for `webUserID <= 0`;
  `CountNNTPUsersByWebUserID(webUserID int64) (int, error)` counts users (any state) with that mapping (0 for
  `webUserID <= 0`); `UpdateNNTPUserWebUserID(userID int, webUserID int64) error` stores `NULLIF(?, 0)`.
  No UNIQUE constraint is added (existing installs have many rows mapped to user 1); the web handler refuses a
  second mapping with `CountNNTPUsersByWebUserID`.

---

## Waves

### Wave 0 (inline, orchestrator on `plan-nntp-server-hardening`)

Owned files:
- `internal/nntp/nntp-server-cliconns.go` (`handleCommand`, `getServerCapabilities`, `sendLine`, the `writer` field)
- `internal/nntp/nntp-cmd-basic.go` (only the `handleQuit` signature), `internal/nntp/nntp-cmd-auth.go` (only the
  `handleAuthInfo` signature)
- `internal/nntp/nntp-server-dataio.go` (new), `internal/nntp/nntp-server-commands.go` (new, stubs)
- `internal/nntp/nntp-server-session_test.go`, `internal/nntp/nntp-server-auth_test.go` (marker removals below only)
- `scripts/test-nntp-server.sh`

Steps:
1. Preflight from the skill (`git mv` the plan to `wip/`). Baseline `## Checks` on the untouched branch; record
   PASS/FAIL and the known-bug list (expected: the 29 ids of AUDIT §10).
2. Write `nntp-server-dataio.go` (D1) and `nntp-server-commands.go` (D3 stubs). Change `sendLine` to
   `c.setWriteDeadline(); c.textConn.W.WriteString(line + "\r\n"); return c.textConn.W.Flush()` (check both errors),
   remove the `writer` field and its initialization (`bufio` stays imported: `sendMultilineResponse` uses it).
3. Apply D4 (dispatch) and D5 (CAPABILITIES) exactly, plus the two signature stubs.
4. Remove the markers the wave-0 change fixes (each check now passes):
   - `nntp-server-session_test.go:213` `TestSessCapabilities/reader-without-mode-reader` (RFC-2),
     `:231` `/no-nonstandard-labels` (RFC-2), `:297` `/post-not-allowed-user` (RFC-2), `:311` `/authinfo-after-auth` (RFC-2).
   - `nntp-server-auth_test.go:491` `TestAuthPlaintextNotOffered` (SEC-5): replace `knownBug(t, "SEC-5", err)` by
     `if err != nil { t.Fatal(err) }`.
5. Update `scripts/test-nntp-server.sh` per `## End-to-end` (new helpers, N20–N26 and N28 as PASS/FAIL checks with
   their slice tag). Run `bash -n`.
6. `## Checks` (the known-bug list must equal the baseline minus RFC-2; SEC-5 stays because of
   `TestAuthSecondAuthinfo`/`TestAuthFailureDelay`). Build `./build_nntp-server.sh && ./build_nntpmgr.sh` and run
   `PORT=21120 DATA=./data-test-nntp-server-hardening scripts/test-nntp-server.sh`; record the PASS/FAIL list
   (expected: N01–N11 and N24 PASS, the others FAIL; see `## End-to-end`).
7. Commit `fix(nntp): wave-0 dispatch, CAPABILITIES, data-block writer for nntp-server-hardening` and record the base
   SHA for wave 1.

### Wave 1 (2 parallel `pugleaf-implementer` slices, from the wave-0 commit)

Common rules: follow K1–K5; read the findings rows for your ids and the owned files first. Checks in your worktree:
```bash
gofmt -l internal/nntp internal/database cmd/nntp-server cmd/nntpmgr   # none of your owned files listed
go vet ./...
go build ./...
go test -race -count=1 -timeout 900s ./internal/nntp/... ./internal/database/... ./cmd/nntp-server/...
go test -race ./internal/web/... ./internal/history/... ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/...
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u
./build_nntp-server.sh && ./build_nntpmgr.sh && \
  PORT=<your port> DATA=./data-test-nntp-server-hardening-<slice> scripts/test-nntp-server.sh | grep -E '\[(<your tag>)\]|SUMMARY'
```
Checks tagged for other slices may FAIL in your worktree. Report the known-bug list and your E-lines.

---

#### Slice `nntp-session`: connection loop, shutdown, limits, auth policy, greeting, stats, local430
Findings: A-nntp-sec-1 (A-nntp-rfc-1), BUG-7 (server part), BUG-8, T-nntp-article-1, BUG-9, SEC-3 (server side),
SEC-4, SEC-5, RFC-2 (lines applied in wave 0), RFC-4, RFC-5 (MODE/AUTHINFO), A-nntp-rfc-3, A-nntp-rfc-5,
A-nntp-rfc-6, A-nntp-rfc-11 (T-nntp-auth-1), A-nntp-rfc-12, A-nntp-sec-5, BUG-3, FN-1. E-tag `nntp-session`, port 21121.

Owned files:
- `internal/nntp/nntp-server.go`
- `internal/nntp/nntp-server-cliconns.go` (everything except `handleCommand` and `getServerCapabilities`)
- `internal/nntp/nntp-server-commands.go`
- `internal/nntp/nntp-server-lineio.go` (new)
- `internal/nntp/nntp-cmd-basic.go`, `internal/nntp/nntp-cmd-auth.go`, `internal/nntp/nntp-auth-manager.go`
- `internal/nntp/nntp-server-statistics.go`, `internal/nntp/nntp-cache-local.go`
- `internal/nntp/nntp-cmd-article.go`, `nntp-cmd-body.go`, `nntp-cmd-head.go`, `nntp-cmd-stat.go`, `nntp-cmd-reader.go` (delete)
- `internal/nntp/README.md`
- `cmd/nntp-server/main.go`
- tests: `internal/nntp/nntp-server-session_test.go`, `internal/nntp/nntp-server-auth_test.go`,

Changes:
1. **Connection loop (A-nntp-sec-1, BUG-7 QUIT, decision 15 for handler errors).**
   - `var errQuit = errors.New("client quit")`. `handleQuit(args)` sends `205 Goodbye` and returns `errQuit`
     (arguments ignored, decision 13). `ClientConnection.Close()` closes `c.conn` and no longer sets it to nil.
   - `UpdateDeadlines` (`cliconns.go:54-63`): return early when `c.conn == nil`; set **only** the read deadline
     (`time.Now().Add(c.ioTimeout())`); write deadlines come from `setWriteDeadline` (D1). `sendResponse` calls
     `c.setWriteDeadline()` before `PrintfLine`.
   - `Handle` (`:66-94`): read with `c.readCommandLine()` (change 3). `errCommandTooLong` → `rateLimitOnError()`,
     `501 Command line too long`, continue. `errCommandOverflow` → `400 Command line too long`, return nil.
     After `handleCommand`: `errors.Is(err, errQuit)` → return nil; any other error → return it (no 500, no
     `rateLimitOnError`). Read errors: return nil for `io.EOF` and `net.ErrClosed`, else the wrapped error.
   - `handleConnection` (`nntp-server.go:202-214`): `defer` a `recover()` that logs
     `[NNTP] panic in connection %s: %v\n%s` (with `debug.Stack()`) and lets `conn.Close` and `wg.Done` run
     (register it last so it runs first). Log `Connection error from %s: %v` only for errors other than nil
     (keep this exact prefix: `server_e2e_test.go` waits for it).
2. **Accept loop and shutdown (BUG-7, A-nntp-rfc-3, A-nntp-sec-5, A-nntp-rfc-12, SEC-5 per-user limit).**
   - New fields: `connsMu sync.Mutex`, `conns map[net.Conn]struct{}`, `handlers sync.WaitGroup`,
     `userConns map[int]int`, `bgOnce sync.Once`, `ShutdownTimeout time.Duration` (seeded from
     `DefaultShutdownTimeout = 10 * time.Second`), `PlaintextAuth PlaintextAuthPolicy`, `ReadRequiresAuth bool`,
     `EnforceUserMaxConns bool`, `stopping bool`.
     `type PlaintextAuthPolicy int` with `PlaintextAuthAccept` (0), `PlaintextAuthAdvertise`, `PlaintextAuthRefuse`,
     and `ParsePlaintextAuthPolicy(s string) (PlaintextAuthPolicy, error)` for `accept|advertise|refuse`.
   - `serve` (`:168-199`): the MaxConns check counts at accept: if `s.Stats.GetActiveConnections() >= MaxConns`, set a
     2 s deadline with `conn.SetDeadline` (read **and** write: on a TLS listener the first `Write` runs the handshake,
     which reads), write `400 Too many connections, try again later\r\n`, close, continue. Otherwise
     `s.Stats.ConnectionStarted()` **in the accept loop**, then `if !s.trackConn(conn) { ConnectionEnded(); continue }`,
     `s.handlers.Add(1)`, `s.wg.Add(1)`, `go s.handleConnection(conn, isTLS)`; `handleConnection` defers
     `ConnectionEnded`, `untrackConn`, `handlers.Done`, and `s.releaseUserSlot(client.user)` when a slot was taken.
   - Stop/accept race: field `stopping bool` (guarded by `connsMu`). `trackConn(conn) bool` takes `connsMu`; when
     `stopping` is set it closes `conn` and returns false (so `handlers.Add` is skipped); otherwise it adds the conn.
     `Stop` sets `stopping = true` under `connsMu` **before** it copies the set to close it, so no connection accepted
     during `Stop` escapes the close or the `handlers` wait.
   - `NewNNTPServer`: no goroutine any more (`:95` removed); seed `ShutdownTimeout`, `local430.ttl =
     DefaultLocal430TTL`, `conns`, `userConns`. `startBackgroundLocked()`
     (called by `Start` after all listeners are up and by `Serve`) starts once (`bgOnce`) `go s.local430.run(s.shutdown)`
     with `s.wg.Add(1)` and `defer s.wg.Done()` inside.
   - `Start` (`:100-140`): validate first: no plain port and no TLS port → error `no listener configured (set
     -nntptcpport and/or -nntptlsport)`; TLS port without cert or key → error. Load the cert **before** any listen;
     `tls.Config{Certificates: …, MinVersion: tls.VersionTLS12}`. Create both listeners, and if the second fails close
     the first; only then `serveLocked` each, `startBackgroundLocked()`, `running = true`.
   - `Stop` (`:217-255`): under `s.mu`: return nil when not running; `running = false`; `close(s.shutdown)`; close
     the listeners; unlock. Then close every tracked conn (under `connsMu`, copy first) and wait for `s.handlers`
     up to `ShutdownTimeout` (log `[NNTP] %d connection(s) did not end within %s` on timeout). Remove the fake
     "done" goroutine. `s.wg` stays the caller's WaitGroup (`main` waits on it).
   - `acquireUserSlot(u *models.NNTPUser) bool` / `releaseUserSlot(u)`: always true / no-op unless
     `s.EnforceUserMaxConns`; then under `connsMu`, `u.MaxConns <= 0` → unlimited (decision 12).
3. **Line reader (SEC-3 server side, BUG-9; new file `nntp-server-lineio.go`).**
   - `DefaultMaxCommandLineBytes = 512`, `commandLineHardLimit = 4096` (the `bufio.Reader` size of
     `textproto.NewConn`). `readCommandLine()` uses `c.textConn.R.ReadSlice('\n')`; `bufio.ErrBufferFull` →
     `errCommandOverflow`; a line longer than 512 bytes including CRLF → `errCommandTooLong`; otherwise the line
     without CR/LF as a new string.
   - `DefaultMaxArticleBytes = 1 << 20`, `maxArticleDrainBytes(limit int64) int64` = max(4×limit, 1 MiB) capped at
     64 MiB. `(*NNTPServer).MaxArticleBytes() int64` (decision 11). `readArticleBlock(maxBytes int64) (headLines,
     bodyLines []string, err error)` per D6: reads with `ReadSlice`, accumulating `ErrBufferFull` fragments; counts
     every byte including CRLF; refreshes the read deadline with `c.conn.SetReadDeadline(time.Now().Add(c.ioTimeout()))`
     before **every** article line (BUG-9 upload half: a slow but steady upload is never cut off by a per-command
     deadline); the byte limit is the primary bound, and the existing 1024-head-line / 16384-line caps stay as a
     secondary bound. On exceeding `maxBytes` or a cap it stops storing and discards until the `.` line, then returns
     `ErrArticleTooLarge`; past the drain bound it returns `ErrArticleDrainLimit` at once. Not wired by this plan:
     `nntp-intake-injection` wires it into `readArticleLines` (D6). `DefaultMaxArticleBytes` is defined here only.
4. **Statistics (SEC-4).** `knownStatCommands` = the dispatch commands plus `DATE NEXT LAST NEWGROUPS NEWNEWS OVER HDR
   CHECK STARTTLS`; `CommandExecuted` counts any other word under `"UNKNOWN"`. Getters stay (used by
   `TestSessServerStats`).
5. **AUTHINFO (SEC-5, RFC-5, A-nntp-rfc-6, A-nntp-rfc-11/T-nntp-auth-1).** In `handleAuthInfo(args, line)`:
   - `c.authenticated` → `502 Already authenticated` (both USER and PASS).
   - Plaintext with `PlaintextAuthRefuse` → `483 Encryption required`.
   - `authinfoArgument(line string) string`: the raw remainder after `AUTHINFO`, the subcommand and one run of
     SP/TAB (spaces inside and at the end are kept); used for USER and PASS instead of `args[1]`.
   - `USER` → `381 Password required` (no echo); `PASS` without USER → `rateLimitOnError()`, `482`; failure →
     `Stats.AuthFailure()`, `rateLimitOnError()`, `481 Authentication failed`; success but
     `!c.server.acquireUserSlot(user)` → `rateLimitOnError()`, `481 Too many connections for this user`; success →
     `281 Authentication accepted` (no username echo). Unknown subcommand → `rateLimitOnError()`,
     `501 Unknown AUTHINFO subcommand` (no echo).
   - `nntp-server-commands.go`: fill `commandMaxArgs`/`readerCommands` (D3), `argCountOK`, `readGateOK`, and
     `authinfoAdvertised()` = `!c.authenticated && (c.isTLS || c.server.PlaintextAuth == PlaintextAuthAdvertise)`
     (TLS always advertises before login; `refuse` and `accept` never advertise on plaintext).
   - `readGateOK`: true unless `c.server.ReadRequiresAuth && !c.authenticated && readerCommands[command]`;
     `argCountOK`: `max, ok := commandMaxArgs[command]; !ok || max < 0 || len(args) <= max`.
6. **local430 (BUG-8, T-nntp-article-1, BUG-3 tick).** `Local430` gets `ttl time.Duration` (≤ 0 → `DefaultLocal430TTL
   = time.Second`); `Check` returns false (and deletes) for entries older than ttl; `Add` refuses ids longer than 250
   octets and stops adding at `local430MaxEntries = 65536` (returns false); `Remove(messageID)`; `run(stop <-chan
   struct{})` cleans every `local430CleanupEvery = 15 * time.Second` until stop; `Cleanup` uses ttl. Delete
   `CronLocal430` and the commented `CacheMessageIDNumtoGroup` block (`:54-146`). `func (lc *Local430)
   Remove(messageID string)` deletes the entry (no-op when absent); it has no caller in this plan:
   `nntp-intake-injection` calls `c.server.local430.Remove(id)` after 235/239/240 (decision 9). `NNTPServer.Processor`
   is not wrapped.
7. **Greeting, MODE, HELP (RFC-4, RFC-5, A-nntp-rfc-6, FN-1).** Greeting: `200` when `postingAllowed()` else `201`,
   hostname `c.server.Config.Hostname` or `go-pugleaf` when empty (`… NNTP server ready` / `… (TLS)`).
   `MODE READER` → the same 200/201 (`Reader mode, posting allowed|prohibited`); other variants →
   `rateLimitOnError()`, `501 Unknown MODE variant`. HELP: `sendDataBlock(100, …)` listing exactly the dispatched
   commands (`STAT|HEAD|BODY|ARTICLE [<message-id>|<number>]`, `XOVER [<range>]`, `XHDR <header> [<range>|<message-id>]`,
   `LISTGROUP [<group> [<range>]]`, `POST`, `IHAVE <message-id>`, `TAKETHIS <message-id>`, …).
   CAPABILITIES uses `sendDataBlock`; delete `sendMultilineResponse` if unused afterwards (grep).
8. **Delays (BUG-3).** `DefaultArticleCmdDelay = 25 * time.Millisecond`, `DefaultGroupCmdDelay = 25 *
   time.Millisecond`; `DefaultNNTPcliconnTimeout = 3 * time.Minute` (BUG-9). Apply the decision-7 rule in owned files.
9. **FN-1.** Delete the five stub files; remove `ClientConnection.capabilities/created/lastCommand` and `RemoteAddr`;
   `AuthManager` keeps only `NewAuthManager`/`AuthenticateUser` (delete `CheckGroupAccess`, `CanPost`, `IsAdmin`,
   `CheckConnectionLimit`). Rewrite `internal/nntp/README.md` from the dispatch: file map (handlers in
   `nntp-article-common.go`, `nntp-cmd-*.go`), the server options and flags, the decisions 1–4/7–12, and no
   claims about `examples/` or `GetUserByUsername`.
10. **`cmd/nntp-server/main.go` (A-nntp-rfc-12, BUG-3, BUG-9, SEC-5, decision 11).** After `processor.SetHostname`,
    set `mainConfig.Server.Hostname = processor.LocalNNTPHostname`. Flag text `-maxconnections`: "maximum number of
    concurrent client connections (all clients)". New flags: `-idletimeout` (3m), `-articledelay` (25ms),
    `-groupdelay` (25ms), `-errordelay` (1s), `-plaintextauth` (accept), `-readauth` (false),
    `-enforce-user-maxconns` (false), `-maxartsize` (0 = config default 32768). Set them on the server after
    `NewNNTPServer`. A failing `Start()` stays fatal.
11. Tests (`sess`/`auth`/`art` prefixes):
    - Remove markers (not the two `TestArtLocal430` markers: decision 9, 60 s TTL): `nntp-server-session_test.go:184` `TestSessGreeting/read-only` (RFC-4), `:331`
      `TestSessMode/reader-read-only` (RFC-4), `:346` `TestSessMode/stream` (RFC-5), `:352`
      `TestSessMode/unknown-variant` (RFC-5), `:515` `TestSessPipelining/quit-then-command` (A-nntp-sec-1), `:538`
      `TestSessLineLimit/over-512-octets` (SEC-3), `:585` `TestSessLineLimit/1MiB-without-LF` (SEC-3), `:636`
      `TestSessStatsGrowth` (SEC-4), `:684` `TestSessIdleTimeout/default-at-least-3min` (BUG-9), `:744`
      `TestSessMaxConns/rejection-status` (A-nntp-rfc-3), `:784` `TestSessStop` (BUG-7);
      `nntp-server-auth_test.go:159` `TestAuthArgErrors/unknown-subcommand` (RFC-5), `:177` `TestAuthSecondAuthinfo`
      (SEC-5), `:212` `TestAuthPasswordWithSpace` (T-nntp-auth-1), `:244` `TestAuthFailureDelay` (SEC-5).
    - Adapt `TestSessLocal430` (`:846-861`): ages relative to `DefaultLocal430TTL` (old = now − 2×TTL, young = now),
      plus `Remove` and the 250-octet rule. Delete `TestAuthManagerUnits` (`auth_test.go:339-398`, helpers removed).
    - `FuzzSessCommandLine` (`:1071`): change the skip filter `len(line) > 4096` to `len(line) > 4094` (a 4095-byte
      line plus CRLF overflows the 4096-byte reader and is closed with 400, which is correct but not "one status line
      per command").
    - Tests that need non-default server fields (`ReadRequiresAuth`, `PlaintextAuth`, `EnforceUserMaxConns`, TLS) build
      their own server like `sessFuzzServer` (`NewNNTPServer`, set the fields, then `Serve` on `127.0.0.1:0`, with a
      cleanup that calls `Stop` and waits for the WaitGroup at most 10 s); the harness stays read-only and setting
      fields on a running `hs.S` would race.
    - New: `TestSessCommandArgs` (`GROUP a b`, `MODE READER x`, `HELP x` → 501; `QUIT now` → 205),
      `TestSessNoEcho` (`XYZZY` + 400×`A` → 500 whose line does not contain the input; `MODE FOO`, `AUTHINFO USER
      <name>` lines don't echo), `TestSessReadArticleBlock` (net.Pipe `sessPipeConn`: normal article, `..` unstuffing,
      empty body, over-limit → `ErrArticleTooLarge` with the connection still reading the next line, over the drain
      bound → `ErrArticleDrainLimit`, a 6 000-byte line inside the limit), `TestSessStartErrors` (no ports → error;
      TLS port with a missing cert and a free plain port from `sessFreePort` → error, the plain port is not bound
      afterwards, `IsRunning()` false), `TestSessTLSMinVersion` (goes through `Start()`: a self-signed cert written to
      `t.TempDir()` with `sessSelfSignedTLS`-style code, `TLSPort` from `sessFreePort`, `Port` 0; a client with
      `MaxVersion: tls.VersionTLS11` fails the handshake, a TLS 1.2+ client gets the greeting; `Stop` in cleanup),
      `TestSessReadRequiresAuth` (own server with `ReadRequiresAuth=true`: `GROUP` → 480, after AUTHINFO → 211/411),
      `TestSessPlaintextPolicy` (own servers: refuse → 483 and not advertised; advertise → advertised; TLS →
      advertised, gone after 281), `TestAuthUserMaxConns` (own server with `EnforceUserMaxConns=true`, user with
      maxconns 1: second connection's PASS → 481, after the first QUITs → 281; and with the default server both
      connections get 281).
      `TestSessCommandArgs` also checks `XOVER 1-2 x` and `LISTGROUP g 1- x` → 501 (the table rejects them before the
      handler, so no group is needed). No production hook is added just to inject a panic: the `recover` is covered by
      code review and by `TestSessPipelining/quit-then-command` staying green under `runIsolated`.

Acceptance: E-lines N21, N23, N24, N25, N26, N30 PASS; the markers above are gone and their tests pass; the known-bug
list equals wave 0's minus A-nntp-rfc-3, A-nntp-sec-1, BUG-9, RFC-4, SEC-4, SEC-5, T-nntp-auth-1 (BUG-7, SEC-3 and
RFC-5 stay because of their intake markers; BUG-8 and T-nntp-article-1 stay because of the 60 s TTL, decision 9).

---

#### Slice `nntp-overview`: range parser, XOVER, XHDR, chunked range reads
Findings: SEC-1, BUG-2, BUG-5, RFC-6, A-nntp-sec-3. E-tag `nntp-overview`, port 21122.
Also the XOVER/XHDR parts of three ids owned elsewhere: A-nntp-sec-2 (`hide = 0` in the range reads and
`NNTPHeaderByMessageID`; id owned by `nntp-read`), A-nntp-rfc-4 (403 instead of 411/503 at `nntp-cmd-xover.go:74,82`,
`nntp-cmd-xhdr.go:80,88`; id owned by `nntp-read`) and A-nntp-rfc-6 (no unbounded echo in the XHDR status line; id
owned by `nntp-session`). Their markers/acceptance stay with the owning slice.

Owned files:
- `internal/nntp/nntp-cmd-range.go` (new)
- `internal/nntp/nntp-cmd-xover.go`, `internal/nntp/nntp-cmd-xhdr.go`
- `internal/database/queries.go` (only `GetOverviewsRange`, `GetHeaderFieldRange` and their query constants
  `query_GetOverviewsRange`, `query_GetHeaderFieldRange1…7`, lines 1804-1937)
- `internal/database/nntp_overview.go` (new)
- tests: `internal/nntp/nntp-server-overview_test.go`, `internal/database/nsh_overview_test.go` (new)

Changes:
1. **Range parser (D2)** in `nntp-cmd-range.go`, exactly the contract above, with `requireGroup`.
2. **DB reads (BUG-2, A-nntp-sec-3, decision 2).**
   - `const OverviewRangeChunk = 1000` and `type HeaderFieldValue struct { ArticleNum int64; Value string }` in
     `nntp_overview.go`.
   - `GetOverviewsRange(groupDB, startNum, endNum int64, limit int)`: `limit <= 0` → `OverviewRangeChunk`; query
     `… FROM articles WHERE hide = 0 AND article_num >= ? AND article_num <= ? ORDER BY article_num ASC LIMIT ?`
     through `RetryableQuery`; `defer rows.Close()`; check `rows.Err()`. The Go clamp (`:1815-1818`) goes.
   - `GetHeaderFieldRange(groupDB, field string, startNum, endNum int64, limit int) ([]HeaderFieldValue, error)`:
     the 7 column queries get `hide = 0` and `LIMIT ?`; any other field selects `article_num, headers_json` and fills
     `Value` with `HeaderValueFromJSON(headersJSON, field)` (empty when absent); one element per visible row in
     number order; `RetryableQuery`, `rows.Err()`; the clamp goes.
   - `HeaderValueFromJSON(headersJSON, field string) string`: split on `\n`, case-insensitive name match on the first
     occurrence, unfold continuation lines (leading SP/TAB) with one space, trim.
   - `NNTPHeaderByMessageID(groupDB *GroupDB, field, messageID string) (articleNum int64, value string, err error)`:
     `SELECT article_num, <column or headers_json> FROM articles WHERE message_id = ? AND hide = 0`;
     `sql.ErrNoRows` when absent or hidden.
3. **XOVER (SEC-1 via chunking, BUG-5, RFC-6, decisions 3/7/15).** `handleXOver`: `requireGroup`; no argument →
   current article (420 when 0); else `parseRangeArg` (501); `Empty()` → `rateLimitOnError()`, 423. `GetGroupDB`
   error → 403. Fetch the first chunk **before** the status line: no row → 423. Then `224`, write each row with
   `overviewLine(o)` through `dataBlockWriter`, and fetch the next chunk from `last+1` (stop when a chunk is shorter
   than `OverviewRangeChunk` or `last == math.MaxInt64`). A DB error after `224` returns the error (connection
   closed, decision 15). `overviewLine`: `n\tsubject\tfrom\tdate\tmessage-id\treferences\tbytes\tlines` (8 fields, no
   trailing TAB) with `sanitizeOverviewField` (drop CRLF pairs, then TAB/CR/LF/NUL → one space each) on the five text
   fields.
4. **XHDR (SEC-1, BUG-5, RFC-6).** `handleXHdr`: header required (501). Message-id form (second argument starts with
   `<` and ends with `>`; a group is not required): `xhdrByMessageID` looks in the current group with
   `NNTPHeaderByMessageID`, else (processor set) `Processor.FindArticleByMessageID(id, "")` and re-reads the value
   with `NNTPHeaderByMessageID` in the article's group (`*article.NewsgroupsPtr[0]`, so a hidden article stays
   hidden); miss → `rateLimitOnError()`, `430 No article with that message-id`; hit → `221 Header follows` and the
   line `<message-id> <value>` (value sanitized; empty value → just the message-id). Range form: as XOVER with
   `GetHeaderFieldRange` chunks; each line `n value` with `sanitizeOverviewField`, or `n` when the value is empty
   (decision 3). Status `221 <Header> follows` without echoing more than the field name, truncated to 64 bytes.
5. Tests:
   - Remove markers in `nntp-server-overview_test.go`: `:260` `TestOvrXoverRanges/{deleted-number,past-high,reversed}`
     (RFC-6), `:337` `TestOvrLineFormat/no-trailing-field` (BUG-5), `:408` `TestOvrSanitizing/XOVER` (BUG-5), `:474`
     `TestOvrSanitizing/XHDR` (BUG-5), `:499` `TestOvrFullRange/XOVER` and `:512` `/XHDR` (BUG-2), `:570`
     `TestOvrXhdr/present-{Newsgroups,Path}` (RFC-6), `:596` `TestOvrXhdr/absent-X-Ovr-Absent` (RFC-6), `:611`
     `TestOvrXhdr/{past-high,reversed}` (RFC-6), `:626` `TestOvrXhdr/message-id` (RFC-6), `:668` `TestOvrHugeRange`
     (SEC-1). (`:688`/`:698` RFC-1 stay.)
   - New in `nntp-server-overview_test.go`: `TestOvrRangeParse` (table: `5`, `5-`, `5-9`, `9-5` (Empty), `0-`,
     16 and 19 digits within int64 ok, 20 digits / `9223372036854775808` / `-` / `x-5` / `1-2-3` / `+1` / `-5` / `` →
     `errRangeSyntax`), `TestOvrHidden`
     (an article set `hide = 1` by raw SQL is absent from `XOVER 1-` and `XHDR subject 1-`, and `XHDR subject <its id>`
     → 430). (Surplus-argument checks are `nntp-session`'s `TestSessCommandArgs`: K5.)
   - New `internal/database/nsh_overview_test.go`: `TestNSHOvrHeaderValueFromJSON` (case, folding, absent, first
     occurrence), `TestNSHOvrRangeChunks` (2 500 rows: limit 1000 returns 1000/1000/500 in order; `hide = 1` rows
     skipped; `math.MaxInt64` end), `TestNSHOvrHeaderByMessageID` (column field, `headers_json` field, hidden →
     `sql.ErrNoRows`).

Acceptance: E-line N22 PASS; markers above gone; the known-bug list loses BUG-2, BUG-5, RFC-6, SEC-1.

---

### Merge order wave 1
`nntp-overview` first (database + leaf handlers), then `nntp-session`; run `## Checks` after each merge. Expected
known-bug list after wave 1: A-nntp-rfc-2, BUG-1, BUG-4, BUG-6, BUG-7, BUG-8, INT-1, INT-2, RFC-1, RFC-3, RFC-5, SEC-2,
SEC-3, SEC-6, SEC-7, T-nntp-article-1, T-nntp-group-1 (17).

### Wave 2 (2 parallel slices, from the merged wave 1)

#### Slice `nntp-read`: GROUP, LISTGROUP, LIST, ARTICLE/HEAD/BODY/STAT, hide filter, data blocks
Findings: SEC-2 (server side), SEC-7, A-nntp-sec-2, A-nntp-sec-4, T-nntp-group-1, BUG-1, BUG-4, BUG-6, RFC-3,
A-nntp-rfc-2, A-nntp-rfc-4, A-nntp-rfc-9, A-nntp-rfc-10. E-tag `nntp-read`, port 21123.

Owned files:
- `internal/nntp/nntp-article-common.go`, `internal/nntp/nntp-cmd-helpers.go`, `internal/nntp/nntp-cmd-group.go`,
  `internal/nntp/nntp-cmd-list.go`
- `internal/database/nntp_reads.go` (new)
- `internal/database/queries.go` (only `GetArticleByNum`, one line after `:906`)
- tests: `internal/nntp/nntp-server-article_test.go`, `internal/nntp/nntp-server-group_test.go`,
  `cmd/nntp-server/server_e2e_test.go` (only the `TestE2EDotHeaderLine` marker), `internal/database/nsh_reads_test.go` (new)

Changes:
1. **DB (`nntp_reads.go`; every query has `hide = 0`; none uses `ArticleCache`; all through the `Retryable*` helpers).**
   - `NNTPArticleByNum(groupDB, num int64) (*models.Article, error)` and `NNTPArticleByMessageID(groupDB, messageID)`:
     the columns of `query_GetArticleByNum`, set `ArticleNums[groupDB.NewsgroupPtr]`, `NewsgroupsPtr` and
     **`DBArtNum`**; `sql.ErrNoRows` when absent or hidden.
   - `NNTPArticleMessageID(groupDB, num int64) (string, error)` (STAT).
   - `NNTPGroupWatermarks(groupDB) (low, high int64, err error)`: `SELECT COALESCE(MIN(article_num),0),
     COALESCE(MAX(article_num),0) FROM articles WHERE hide = 0`.
   - `NNTPArticleNumbers(groupDB, first, last int64, limit int) ([]int64, error)`: `SELECT article_num … WHERE hide = 0
     AND article_num >= ? AND article_num <= ? ORDER BY article_num LIMIT ?` (chunk 10 000), `rows.Err()`.
   - `NNTPActiveNewsgroups() ([]*models.Newsgroup, error)`: `SELECT name, description, last_article, low_water,
     COALESCE(status,'y') FROM newsgroups WHERE active = 1 ORDER BY name` (NULL-safe scans: `COALESCE` on every
     nullable column, so one bad row does not abort the list).
   - `queries.go:906`: add `a.DBArtNum = artnum` after the `ArticleNums` assignment in `GetArticleByNum` (BUG-1 DB side,
     helps the web callers too).
2. **GROUP / LISTGROUP (SEC-2, A-nntp-sec-4, T-nntp-group-1, SEC-7, BUG-4, decision 14).**
   - `selectableGroup(name string) (*models.Newsgroup, bool)`: `MainDBGetNewsgroup`; any error or `!Active` → false.
     No `GetGroupDB` call happens for a name that fails this check.
   - `selectGroup(g)`: `GetGroupDB` (error → 403), `NNTPGroupWatermarks`, set `currentGroup`, `currentFirst = low`,
     `currentLast = high`, `currentArticle = low`; returns count/low/high per decision 14.
   - `handleGroup`: one argument (501 when none); unknown/inactive → `411 No such newsgroup` (current group unchanged);
     else `211 count low high name`.
   - `handleListGroup`: group from the argument or the current one (412 via `requireGroup` when neither);
     `selectableGroup` → 411; `selectGroup` (LISTGROUP selects, RFC 3977 §6.1.2.2); optional range via
     `parseRangeArg` (501; an `Empty()` or out-of-group range gives an empty list); `211 count low high name`, then the
     numbers from `NNTPArticleNumbers` in chunks through `dataBlockWriter`; a DB error after 211 returns the error.
   - Remove the `GetOverviews` call (`:59`); `GetOverviews` stays for the web.
3. **LIST (BUG-4, A-nntp-rfc-4, decision 14).** `NNTPActiveNewsgroups`; ACTIVE lines `name high low status` with
   low = `max(low_water, 1)` and the status rule of decision 14 (uses `c.postingAllowed()`); NEWSGROUPS lines
   `name description` as today, with the description's TAB/CR/LF replaced by spaces.
   DB error → 403. Unknown keyword → `rateLimitOnError()`, `501 Unknown LIST keyword` (no echo). Both through
   `dataBlockWriter`. The wildmat stays ignored (RFC-1, `nntp-rfc-commands`).
4. **ARTICLE/HEAD/BODY/STAT (RFC-3, BUG-1, A-nntp-sec-2, A-nntp-rfc-9, A-nntp-rfc-4).** `resolveArticleArg(args)`:
   - no argument: `requireGroup` (412), `currentArticle == 0` → `rateLimitOnError()`, `420 Current article number is
     invalid`, else the current number; never read `args[0]` on this path (`:58`).
   - message-id: current group first (`NNTPArticleByMessageID`), then `local430.Check`, then (processor set)
     `FindArticleByMessageID(id, "")` and `visibleInOtherGroup(article)`: re-read it with `NNTPArticleByMessageID`
     in `*article.NewsgroupsPtr[0]` (a hidden copy is a miss); replies number 0 outside the current group. Known
     limitation (document it in a code comment): `hide` is per group DB, so for a crosspost hidden in the group the
     processor returns but visible in another one the lookup answers 430 instead of trying the next group (the
     processor returns only the first hit). Misses:
     `430 No article with that message-id` (all three texts). Without a processor only the current group is searched
     (documented in the README by `nntp-session`; A-nntp-rfc-9).
   - number: `requireGroup` first (412 without a selected group), then more than 19 digits, not a number or above
     `math.MaxInt64` → 501 (same rule as `parseRangeArg`);
     `n <= 0` → `rateLimitOnError()`, 423; `GetGroupDB` error →
     403; `NNTPArticleByNum`/`NNTPArticleMessageID` miss → 423.
   - The number form sets `currentArticle` from the requested number (BUG-1); the message-id form never changes it.
5. **Data blocks (BUG-6, A-nntp-rfc-2, A-nntp-rfc-10).** `articleHeaderLines(a)`: `headers_json` split on `\n`,
   empty lines dropped (an empty `headers_json` gives no lines). `articleBodyLines(a)`: `nil` for an empty body, else
   split on `\n` (no manual stuffing: `dataBlockWriter.Line` stuffs and cleans). HEAD = header lines; BODY = body
   lines; ARTICLE = header lines, one empty line, body lines; all via `dataBlockWriter`. Delete
   `parseArticleHeadersFull`, `parseArticleHeadersShort`, `parseArticleBody`, `formatOverviewLine` (replaced by
   `overviewLine` in wave 1) from `nntp-cmd-helpers.go`; keep `rateLimitOnError`. After this slice no code calls
   `sendLine` (`nntp-cmd-posting.go` never did); wave 3 deletes it.
6. Tests:
   - Remove markers in `nntp-server-article_test.go`: `:366` `TestArtNoArgument/current-article`, `:380`
     `/invalid-current-article-420`, `:393` `/no-group-412` (RFC-3); set `bug` to `""` in the rows `:406` (`"0"`) and
     `:407` (`"-1"`) of `TestArtInvalidNumbers` (RFC-3); `:523` `TestArtCurrentArticle/after-{ARTICLE,HEAD,BODY}`
     (BUG-1); `:539` `/after-STAT` and `:557` `/message-id-lookup-unchanged` (RFC-3); `:653`
     `TestArtByteExact/header-dot-continuation`, `:680` `/header-lone-dot`, `:697` `/empty-body`, `:716`
     `/empty-headers` (BUG-6).
   - Remove markers in `nntp-server-group_test.go`: `:224` `TestGrpGroupSeeded/reply`, `:246` `/current-article`,
     `:339` `TestGrpListGroup/reply-and-numbers`, `:354` `/range`, `:367` `/selects-group`, `:545` `TestGrpListActive`
     (BUG-4); `:388` `TestGrpListGroup/inactive` (T-nntp-group-1); `:427` `TestGrpListGroupUnknownNoFile` (SEC-2);
     `:512` `TestGrpListGroupMemory` (SEC-7).
   - Remove the marker `cmd/nntp-server/server_e2e_test.go:1329` `TestE2EDotHeaderLine` (A-nntp-rfc-2):
     `e2eKnownBug(t, "A-nntp-rfc-2", bugErr)` → `if bugErr != nil { t.Fatal(bugErr) }`.
   - New in `nntp-server-article_test.go`: `TestArtHidden` (an article with `hide = 1`: `ARTICLE 2`/`STAT 2` → 423,
     `STAT <id>` → 430 in the current group and through a fake processor that returns it from another group) and
     `TestArtNotFoundTexts` (430 lines contain none of `Cache says no`, `NotF0`, `NotF1`). New in
     `nntp-server-group_test.go`: `TestGrpHiddenNotListed` (`LISTGROUP` skips a hidden article; `GROUP`'s low/high skip
     it; `GROUP`'s count only has to be `>=` the visible count, decision 14), `TestGrpListGroupRangeEmpty` (`LISTGROUP g 9-3` → 211 with an empty list, group selected),
     `TestGrpListNewsgroupsSanitized` (a description with a TAB/CR is sent with spaces, one line per group).
   - New `internal/database/nsh_reads_test.go`: `TestNSHReadByNumSetsDBArtNum` (also for `GetArticleByNum`),
     `TestNSHReadHideFiltered` (all six functions), `TestNSHReadWatermarks` (empty group 0/0), `TestNSHReadNumbersChunks`,
     `TestNSHReadActiveNullSafe` (a row with NULL `status`/`low_water` is listed).

Acceptance: E-line N20 PASS; markers above gone; the known-bug list loses A-nntp-rfc-2, BUG-1, BUG-4, BUG-6, RFC-3,
SEC-7, T-nntp-group-1 (SEC-2 stays: `TestE2ERejectUnknownGroup`, intake).

---

#### Slice `nntp-auth-db`: auth cache, timing, nntpmgr, NNTP user lookups
Findings: SEC-6 (old-password half), A-nntp-sec-6, OPS-2 (nntpmgr part), A-ops-9, A-web-5 (DB part). E-tag
`nntp-auth-db`, port 21124.

Owned files:
- `internal/database/nntp_auth_cache.go`, `internal/database/db_nntp_users.go`
- `internal/database/nntp_user_admin.go` (new)
- `cmd/nntpmgr/main.go`
- tests: `internal/database/nsh_auth_test.go` (new), `internal/nntp/nntp-server-auth_test.go` (only the SEC-6 marker)

Changes:
1. **Cache (SEC-6, decision 5).** `AuthCacheEntry.StoredHash string`; `Set(userID int, username, password, storedHash
   string)`; `GetEntry(username, password string) (userID int, storedHash string, ok bool)` (same expiry and
   password-hash checks as `Get`, which becomes a wrapper returning `(userID, ok)`); `RemoveUserID(userID int)`.
2. **`AuthenticateNNTPUser` (`db_nntp_users.go:281-328`).** Hit: `GetNNTPUserByID`; if `user.Password != storedHash`,
   `InvalidateNNTPUserAuth(username)` and fall through to the full check (so the old password fails bcrypt and the new
   one is re-cached); then `refuseDisabledNNTPUser` as today. Miss: on a `GetNNTPUserByUsername` error run
   `bcrypt.CompareHashAndPassword(nntpDummyHash(), []byte(password))` (A-nntp-sec-6; `nntpDummyHash` generated once
   with `sync.Once` at `bcrypt.DefaultCost` from a fixed string) before returning the error. Cache with
   `Set(user.ID, username, password, user.Password)`. Check the error of `UpdateNNTPUserLastLogin` (`:325`) and log
   `[NNTP-AUTH] last login update for user %d: %v`.
3. **Same-process invalidation.** `UpdateNNTPUserPassword`, `DeactivateNNTPUser`, `DeleteNNTPUser` call
   `db.NNTPAuthCache.RemoveUserID(userID)` (nil-safe) after a successful write.
4. **Lookups (A-ops-9, A-web-5 DB part; `nntp_user_admin.go`).** `GetNNTPUserByUsernameAnyState(username)` (no
   `is_active` filter); `CountNNTPUsersByWebUserID(webUserID int64) (int, error)`;
   `UpdateNNTPUserWebUserID(userID int, webUserID int64) error` (`NULLIF(?, 0)`, `updated_at`). In
   `db_nntp_users.go`: `GetNNTPUserByWebUserID` returns `sql.ErrNoRows` for `webUserID <= 0` and adds
   `ORDER BY id ASC LIMIT 1` (D6 contract).
5. **nntpmgr (OPS-2, A-ops-9, decision 16, open question 1).** `flagsSet()` from `flag.Visit`. The `-maxconns`
   flag default and `CreateNNTPUserForWebUser` use `DefaultNNTPUserMaxConns = 3` (was 1). `-list`: drop the Password column.
   `-create`: existence check with `GetNNTPUserByUsernameAnyState`; print the password only when `-password` was
   `random`; otherwise `Password: (as given)`. `-update`: `GetNNTPUserByUsernameAnyState`; change maxconns only if
   `-maxconns` was given (range 1–25) and posting only if `-posting` was given; error when neither was given. New
   `-activate`/`-deactivate` (`ActivateNNTPUser`/`DeactivateNNTPUser`) and `-setpassword` (uses `-password`, prints
   it only when generated). One password rule for `-create` and `-setpassword`: 10–72 bytes (72 = the bcrypt limit;
   the old `-create` limit of 20 goes). `-delete` uses the any-state lookup. Logs keep `log.Printf` with
   `[NNTPMGR]`.
6. Tests:
   - Remove the marker `internal/nntp/nntp-server-auth_test.go:458` `TestAuthCacheOldPassword` (SEC-6).
   - New `internal/database/nsh_auth_test.go`: `TestNSHAuthOldPasswordRejected` (login cached, change the password,
     old password fails without waiting for the TTL, new one works), `TestNSHAuthRemoveUserID`,
     `TestNSHAuthUnknownUserTiming` (min of 3 unknown-user attempts ≥ 0.5 × min of 3 known-user wrong-password
     attempts), `TestNSHAuthWebUserIDLookup` (two users mapped to one web user → lowest id; `0` → `sql.ErrNoRows`;
     count; update to 0 stores NULL), `TestNSHAuthAnyState` (an inactive user is found).

Acceptance: E-line N28 PASS; the known-bug list loses SEC-6.

### Merge order wave 2
`nntp-auth-db` first, then `nntp-read`; `## Checks` after each. Expected known-bug list after wave 2:
**BUG-7, BUG-8, INT-1, INT-2, RFC-1, RFC-5, SEC-2, SEC-3, T-nntp-article-1** (BUG-8 and T-nntp-article-1 per decision 9,
the rest owned by `nntp-intake-injection` / `nntp-rfc-commands`).

### Wave 3 (inline, orchestrator)
1. `grep -rn 'sendLine(' internal/nntp` must show only its definition (no caller after wave 2; at 03d5ef7
   `nntp-cmd-posting.go` has none): delete `sendLine` from `nntp-server-cliconns.go`, then run the checks.
   Run staticcheck: `~/go/bin/staticcheck ./internal/nntp/ ./internal/database/` if that build loads the module,
   otherwise a fresh `go install honnef.co/go/tools/cmd/staticcheck@latest` into the scratchpad (`GOBIN=<scratchpad>`);
   it must report no U1000 for the files this plan touched.
2. Full `## Checks`, build scripts, `## End-to-end`; record results in `## Progress`.
3. `BUGS.md`, section `# nntp-server`: strike the ids fixed here and point at this plan; leave the intake/rfc ids.

---

## Checks
On every merged tree (after wave 0 and after each merge in waves 1 and 2):
```bash
gofmt -l ./cmd ./internal            # may list only the 2 baseline files
go vet ./...
go build ./...
go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
  ./internal/nntp/... ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/...
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u
git status --short                   # no data-test-*, active.out, analysis_*.txt leftovers
./build_nntp-server.sh && ./build_nntpmgr.sh
```
Expected known-bug sets (the `sort -u` output, ids only):
- baseline (03d5ef7 / wave-0 start): the 29 ids of AUDIT §10.
- after wave 0: baseline minus RFC-2 (28).
- after wave 1: A-nntp-rfc-2, BUG-1, BUG-4, BUG-6, BUG-7, BUG-8, INT-1, INT-2, RFC-1, RFC-3, RFC-5, SEC-2, SEC-3, SEC-6,
  SEC-7, T-nntp-article-1, T-nntp-group-1 (17).
- after wave 2 (final): **BUG-7, BUG-8, INT-1, INT-2, RFC-1, RFC-5, SEC-2, SEC-3, T-nntp-article-1** (9). Every remaining
  marker is in a test listed under "Out of scope" or kept by decision 9 (60 s local430 TTL). `PUGLEAF_KNOWN_BUGS=1` runs fail only with `known bug … still open` lines; plain runs
  pass.

## End-to-end

Runner: `pugleaf-verifier` on the main checkout (`plan-nntp-server-hardening`), after wave 3.
```bash
./build_nntp-server.sh && ./build_nntpmgr.sh
PORT=21119 DATA=./data-test-nntp-server-hardening scripts/test-nntp-server.sh
```
Expected: `SUMMARY pass=20 fail=0 info=1` (PASS: N01–N11, N20–N26, N28, N30; the INFO line is N05's commit timing,
printed on every run), no `panic:` and no `DATA RACE` in
`$DATA/server.log`, the server exits on SIGINT in both cycles.

### Changes to `scripts/test-nntp-server.sh` (wave 0 writes them; fix only real script bugs later)
- Header comment: "N20–N28 are PASS/FAIL checks of plan nntp-server-hardening (tags nntp-session, nntp-overview,
  nntp-read, nntp-auth-db)". Line 87's comment: nntpmgr no longer echoes a given password (N28).
- Helpers after `q()`:
```bash
nn_open() { { exec 3<>"/dev/tcp/127.0.0.1/$PORT"; } 2>/dev/null || return 1
  IFS= read -r -t 5 NN_GREETING <&3 || { nn_close; return 1; }; NN_GREETING=${NN_GREETING%$'\r'}; }
nn_cmd() { printf '%s\r\n' "$1" >&3 2>/dev/null || return 1; NN_LINE=""
  IFS= read -r -t "${2:-5}" NN_LINE <&3 || return 1; NN_LINE=${NN_LINE%$'\r'}; }
nn_block() { NN_BLOCK="|"; local l; while IFS= read -r -t "${1:-5}" l <&3; do l=${l%$'\r'}
  [ "$l" = . ] && return 0; NN_BLOCK="$NN_BLOCK$l|"; done; return 1; }
nn_close() { { exec 3>&- 3<&-; } 2>/dev/null; }
ms_now() { date +%s%3N; }
```
- **N28 [nntp-auth-db]** right after setup step 3 (server stopped):
```bash
lst=$( (cd "$RUN" && exec "$MGR_ABS" -data "$DATA_ABS" -list) 2>&1 )
if printf %s "$lst" | grep -q "$E2E_USER" && ! printf %s "$lst" | grep -qE '\$2[aby]\$' \
   && ! grep -qF "$E2E_PASS" "$DATA/nntpmgr.log"; then pass N28 nntp-auth-db "nntpmgr prints no hash or given password"
else fail N28 nntp-auth-db "nntpmgr prints no hash or given password" "list/create output leaks secrets"; fi
```
- **N24 [nntp-session]** after the N01–N11 block:
```bash
pre=""; post=""; cap_detail=""
if nn_open; then
  if nn_cmd CAPABILITIES && nn_block; then pre="$NN_BLOCK"; fi
  if nn_cmd "AUTHINFO USER $E2E_USER" && nn_cmd "AUTHINFO PASS $E2E_PASS" && [ "${NN_LINE%% *}" = 281 ] \
     && nn_cmd CAPABILITIES && nn_block; then post="$NN_BLOCK"; else cap_detail="login failed: $NN_LINE"; fi
  nn_close
fi
case "$pre$post" in *"|MODE-READER|"*|*"|TAKETHIS|"*) cap_detail="$cap_detail MODE-READER/TAKETHIS listed" ;; esac
case "$pre" in *"|AUTHINFO"*) cap_detail="$cap_detail AUTHINFO on plaintext" ;; esac
case "$post" in *"|AUTHINFO"*) cap_detail="$cap_detail AUTHINFO after 281" ;; esac
if [ -n "$pre" ] && [ -n "$post" ] && [ -z "$cap_detail" ]; then pass N24 nntp-session "CAPABILITIES by state"
else fail N24 nntp-session "CAPABILITIES by state" "${cap_detail:-no reply} pre='$pre' post='$post'"; fi
```
- **N26 [nntp-session]** 481 delay (default `-errordelay 1s`), placed after N24 and before N20:
```bash
if nn_open && nn_cmd "AUTHINFO USER $E2E_USER"; then
  t0=$(ms_now); nn_cmd "AUTHINFO PASS wrong-password-n26" 5; el=$(( $(ms_now) - t0 )); nn_close
  if [ "${NN_LINE%% *}" = 481 ] && [ "$el" -ge 900 ]; then pass N26 nntp-session "481 delayed (${el}ms)"
  else fail N26 nntp-session "481 delayed >= 900ms" "reply='$NN_LINE' after ${el}ms"; fi
else fail N26 nntp-session "481 delayed >= 900ms" "connect failed"; fi
```
- **N20 [nntp-read]** keeps the probe; replace the `info` line with:
```bash
case "$reply" in *"|411 "*) r411=yes ;; *) r411=no ;; esac
if [ "$r411" = yes ] && [ "$after" = 0 ]; then pass N20 nntp-read "LISTGROUP unknown group: 411, no file"
else fail N20 nntp-read "LISTGROUP unknown group: 411, no file" "files before=$before after=$after reply='${reply:0:120}'"; fi
```
- **N21 [nntp-session]** replaces the INFO probe:
```bash
r0=$(rss_kb); r1="$r0"; closed=no
if nn_open; then
  timeout 10 bash -c 'head -c 2097152 /dev/zero | tr "\000" A' >&3 2>/dev/null
  for _ in 1 2 3; do IFS= read -r -t 2 _l <&3; rc=$?; [ "$rc" -eq 0 ] && continue; [ "$rc" -le 128 ] && closed=yes; break; done
  r1=$(rss_kb); nn_close
fi
greets=no; if alive && nn_open; then greets=yes; nn_close; fi
grow=$(( ${r1:-0} - ${r0:-0} ))
if [ "$closed" = yes ] && [ "$greets" = yes ] && [ "$grow" -lt 16384 ]; then pass N21 nntp-session "2 MiB line: closed, RSS +${grow}kB"
else fail N21 nntp-session "2 MiB line: closed, bounded" "closed=$closed greets=$greets rss_before=${r0:-?} after=${r1:-?}"; fi
```
- **N23 [nntp-session]** after N21:
```bash
q205=no
if nn_open; then printf 'QUIT\r\nHELP\r\n' >&3; IFS= read -r -t 5 l <&3 && case "$l" in 205*) q205=yes ;; esac; nn_close; fi
sleep 1; greets=no; if alive && nn_open; then greets=yes; nn_close; fi
if [ "$q205" = yes ] && [ "$greets" = yes ]; then pass N23 nntp-session "pipelined QUIT+HELP: 205, server survives"
else fail N23 nntp-session "pipelined QUIT+HELP" "205=$q205 alive_and_greets=$greets"; fi
```
- **N25 [nntp-session] + N30**: before the existing N30 `kill -INT`, open an idle client
  (`idle=no; { exec 4<>"/dev/tcp/127.0.0.1/$PORT"; } 2>/dev/null && idle=yes`). The existing loop sets `t0` before
  the signal; after it add `el=$(( $(date +%s) - t0 ))`, then
  `[ "$idle" = yes ] && [ "$exited" = yes ] && [ "$el" -le 10 ]` → `pass N25 nntp-session "SIGINT with an idle client
  exits within 10s (${el}s)"`, else `fail N25 nntp-session "SIGINT with an idle client exits within 10s"
  "idle=$idle exited=$exited el=${el}s"`; then `{ exec 4>&- 4<&-; } 2>/dev/null`. N30's tag becomes `nntp-session`
  (was `shutdown`); its limit is unchanged (30 s) but it now runs with that idle client connected.
- **N22 [nntp-overview]** replaces the INFO probe (fresh start as today):
```bash
if start_server; then
  blk=no; el=0; cpu="?"; exited=no
  if nn_open; then
    nn_cmd "GROUP e2e.test.a"
    t0=$(ms_now)
    if nn_cmd "XHDR subject 1-9223372036854775807" 3 && [ "${NN_LINE%% *}" = 221 ] && nn_block 3; then blk=yes; fi
    el=$(( $(ms_now) - t0 ))
    hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
    c0=$(cpu_ticks); sleep 5; c1=$(cpu_ticks)
    [ -n "$c0" ] && [ -n "$c1" ] && cpu=$(( (c1 - c0) * 100 / (hz * 5) ))
    nn_close
  fi
  kill -INT "$PID" 2>/dev/null
  for _ in $(seq 1 10); do alive || { exited=yes; break; }; sleep 1; done
  [ "$exited" = no ] && kill -KILL "$PID" 2>/dev/null; PID=""
  if [ "$blk" = yes ] && [ "$el" -le 3000 ] && [ "$cpu" != "?" ] && [ "$cpu" -lt 50 ] && [ "$exited" = yes ]; then
    pass N22 nntp-overview "XHDR huge range: block in ${el}ms, cpu ${cpu}%, SIGINT exit"
  else fail N22 nntp-overview "XHDR huge range bounded" "block=$blk ${el}ms cpu=${cpu}% exited=$exited"; fi
else fail N22 nntp-overview "fresh start for the spin probe" "server failed to start; log tail: $(tail -5 "$LOG" | tr '\n' ' ')"; fi
```
Expected after wave 0 (no slice merged): N01–N11 and N24 PASS; N20, N21, N22, N23 (the server crashes: later lines
then fail too), N25, N26, N28 and N30 FAIL (N30 now runs with an idle client, which holds the old server up to the
60 s idle timeout). After each slice its tagged lines PASS in its worktree; N30 needs `nntp-session`.

---

## Progress
(filled in by `/run-plan`)

### Wave 0 (2026-10-11, inline on `plan-nntp-server-hardening`, base `testing-001` @ baea7c0)
- Baseline on 9bfc068: gofmt baseline 2 files, vet/build OK, all 8 package sets green under `-race`, known bugs = the 29 ids of AUDIT §10.
- Added `nntp-server-dataio.go` (D1) and `nntp-server-commands.go` (D3 stubs); `handleCommand` = D4, `getServerCapabilities` = D5;
  `sendLine` writes through `textConn.W` with `setWriteDeadline`, the `writer` field is gone; `handleQuit(args)` /
  `handleAuthInfo(args, line)` signature stubs; markers removed: 4× RFC-2 (`TestSessCapabilities`), SEC-5 in `TestAuthPlaintextNotOffered`.
- `scripts/test-nntp-server.sh` rewritten per `## End-to-end` (nn_* helpers, N20–N26, N28 PASS/FAIL, N25+N30 with an idle client).
- Checks: gofmt/vet/build OK, `-race` 8 package sets green, known bugs = baseline minus RFC-2 (28 ids). Build scripts OK.
  `PORT=21120 DATA=./data-test-nntp-server-hardening scripts/test-nntp-server.sh` → `SUMMARY pass=12 fail=8 info=1`:
  PASS N01–N11, N24; FAIL N20, N21, N22, N23, N25, N26, N28, N30 (as expected before the slices).

### Wave 1 (2026-10-11; 2 implementers, each reviewed before merge)
| Slice | Branch (commits) | Review | Merge |
|---|---|---|---|
| nntp-overview | worktree-agent-a1c463190bb3b91a2 (beeeb50, 488cc29) | 1 major fixed (XHDR Xref/Path served raw upstream headers from headers_json: Xref now always absent, Path from the `path` column), 2 minors fixed (403 on history fault in XHDR by message-id, NULL-safe overview scans); accepted deviation: range numbers up to 19 digits within int64 (D2 amended) | 8a1c79b |
| nntp-session | worktree-agent-a812695d2f0f38332 (08efe1e, 5ae9904) | merge; 4 minors fixed (Stop closes the raw socket of TLS conns, MaxConns 400 reject in a bounded goroutine with `rejectSem`, net.ErrClosed after handleCommand not logged, bare AUTHINFO after login → 502); deviations accepted (`handlers.Add` inside `trackConn`, Start/Serve error after Stop, accept back-off, `MaxConns <= 0` → config default 500); `-local430ttl` flag skipped | bf684b1 |

Mid-wave decisions: local430 TTL 60 s (user), BUG-8 and T-nntp-article-1 markers stay (f20cb78); new-user maxconns default 3 (97e6c3b, done in wave 2).
Checks on bf684b1: gofmt baseline 2 files, vet/build OK, 8 package sets green under `-race`, known bugs = the 17 ids expected after wave 1
(A-nntp-rfc-2 BUG-1 BUG-4 BUG-6 BUG-7 BUG-8 INT-1 INT-2 RFC-1 RFC-3 RFC-5 SEC-2 SEC-3 SEC-6 SEC-7 T-nntp-article-1 T-nntp-group-1),
build scripts OK, e2e `SUMMARY pass=18 fail=2 info=1` (FAIL only N20 [nntp-read], N28 [nntp-auth-db]); no panic / DATA RACE in server.log.
Deferred: 64 MiB drain cap stays per decision 11 (articles between 64 MiB and a larger MaxArtSize would be refused); RST after the overflow 400 accepted.
